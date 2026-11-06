// Package cpaprocess owns the Supervisor's in-memory CPA child process.
// It exposes no product entry point. Lifecycle callers must durably record
// intent before Start or prepared Stop termination; this primitive does not
// own that policy.
package cpaprocess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

var (
	ErrStateConflict   = errors.New("CPA child is already owned")
	ErrInvalidSpec     = errors.New("invalid CPA start spec")
	ErrInvalidIdentity = errors.New("invalid CPA child identity")
	ErrSpawnFailed     = errors.New("CPA child spawn failed")
	ErrStopFailed      = errors.New("CPA child stop failed")
)

// StartSpec supplies an executable and literal arguments, never a shell command.
// The child inherits the working directory and ordinary parent environment.
// Supervisor-private environment variables are excluded at spawn.
// Standard input/output/error use os/exec's null-device defaults; no logs are collected.
type StartSpec struct {
	Executable string
	Args       []string
}

// ChildIdentity is a Supervisor-local OS credential, never Runtime Protocol
// authority. A zero UID or GID is invalid because configured identity must
// never permit a root CPA fallback.
type ChildIdentity struct {
	UID uint32
	GID uint32
}

func (identity ChildIdentity) validate() error {
	if identity.UID == 0 || identity.GID == 0 {
		return fmt.Errorf("%w: UID and GID must be non-zero", ErrInvalidIdentity)
	}
	return nil
}

type State string

const (
	StateNotStarted State = "not_started"
	StateRunning    State = "running"
	StateExited     State = "exited"
	StateUnknown    State = "unknown"
)

// Observation contains process facts, never CPA readiness. PID is cleared after
// a confirmed exit; it is only an in-memory observation, not restart authority.
// ExitCode is meaningful only when ExitCodeKnown is true. WaitError reports an
// unexpected wait failure, not an ordinary non-zero exit or signal termination.
// If Wait cannot confirm termination, StateUnknown retains ownership and the
// last observed PID, and subsequent Start calls fail closed.
type Observation struct {
	State State
	// InstanceID correlates observations of one successful spawn within this
	// Manager only. It is never persisted or exported as protocol authority.
	InstanceID    uint64 `json:"-"`
	PID           int
	ExitCode      int
	ExitCodeKnown bool
	WaitError     error
}

// ExitEvent reports only a child exit confirmed by the Manager's Wait/reap
// path. InstanceID is the in-memory exact-child correlation authority.
type ExitEvent struct {
	InstanceID uint64
}

const exitEventBuffer = 1

// Manager owns at most one CPA child. Use one Manager per Supervisor incarnation.
// Its zero value is ready to use; it must not be copied after first use.
type Manager struct {
	mu            sync.Mutex
	beforeSpawn   func(StartSpec) error
	childIdentity *ChildIdentity
	applyIdentity func(*exec.Cmd, ChildIdentity) error
	cmd           *exec.Cmd
	waitDone      chan struct{}
	stopTarget    *exec.Cmd
	nextInstance  uint64
	observation   Observation
	exitEvents    chan ExitEvent
	exitPublish   sync.Mutex
}

// NewManager creates one Supervisor-incarnation child owner. beforeSpawn runs
// synchronously after Start has proved that a new child may be created and
// immediately before the configured executable is handed to the OS. It lets
// the Supervisor refresh cached executable facts without making status polling
// or rejected duplicate Start calls touch the filesystem.
func NewManager(beforeSpawn func(StartSpec) error) *Manager {
	return &Manager{beforeSpawn: beforeSpawn}
}

// NewManagerWithIdentity configures the one credential boundary used by every
// CPA spawn path. The identity is applied to the actual OS command immediately
// before the existing pre-spawn observation and exec boundary.
func NewManagerWithIdentity(beforeSpawn func(StartSpec) error, identity ChildIdentity) (*Manager, error) {
	if err := identity.validate(); err != nil {
		return nil, err
	}
	return &Manager{beforeSpawn: beforeSpawn, childIdentity: &identity}, nil
}

// StopTarget is an opaque reservation for the exact child owned when Stop was
// prepared. It carries no PID or caller-selected termination policy.
type StopTarget interface {
	Terminate(context.Context) (Observation, error)
	Release()
}

type ownedStopTarget struct {
	manager *Manager
	cmd     *exec.Cmd
	done    <-chan struct{}
}

// Start serializes validation, spawn and ownership publication. Context is
// checked before spawn, including after executable lookup. It does not bound
// the OS spawn call or the child's lifetime: once spawn succeeds, cancellation
// does not kill the child or turn the successful Start into an error.
// On failure the previous observation is unchanged. A new Start is allowed
// only after the previous child has been waited/reaped, never automatically.
func (m *Manager) Start(ctx context.Context, spec StartSpec) (Observation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return m.observeLocked(), err
	}
	if m.cmd != nil || m.stopTarget != nil {
		return m.observeLocked(), ErrStateConflict
	}
	// Never wrap and reuse an instance, even after a confirmed exit.
	if m.nextInstance == ^uint64(0) {
		return m.observeLocked(), ErrStateConflict
	}
	if strings.TrimSpace(spec.Executable) == "" || strings.ContainsRune(spec.Executable, '\x00') {
		return m.observeLocked(), fmt.Errorf("%w: executable is required and must not contain NUL", ErrInvalidSpec)
	}
	for _, arg := range spec.Args {
		if strings.ContainsRune(arg, '\x00') {
			return m.observeLocked(), fmt.Errorf("%w: arguments must not contain NUL", ErrInvalidSpec)
		}
	}
	cmd := exec.Command(spec.Executable, spec.Args...)
	cmd.Env = childEnvironment(os.Environ(), runtime.GOOS == "windows")
	if m.childIdentity != nil {
		apply := m.applyIdentity
		if apply == nil {
			apply = applyChildIdentity
		}
		if err := apply(cmd, *m.childIdentity); err != nil {
			return m.observeLocked(), fmt.Errorf("%w: configure child credentials: %v", ErrSpawnFailed, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return m.observeLocked(), err
	}
	if m.beforeSpawn != nil {
		if err := m.beforeSpawn(spec); err != nil {
			return m.observeLocked(), fmt.Errorf("%w: pre-spawn validation: %v", ErrSpawnFailed, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return m.observeLocked(), err
	}
	if err := cmd.Start(); err != nil {
		return m.observeLocked(), fmt.Errorf("%w: %w", ErrSpawnFailed, err)
	}
	m.cmd = cmd
	m.waitDone = make(chan struct{})
	m.exitEventsLocked()
	m.nextInstance++
	m.observation = Observation{State: StateRunning, InstanceID: m.nextInstance, PID: cmd.Process.Pid}
	go m.wait(cmd, m.waitDone)
	return m.observation, nil
}

// PrepareStop reserves the exact currently owned running child before durable
// Stop intent is recorded. Start remains fenced even if that child naturally
// exits before termination begins, preventing a later child from being used as
// the target of an older Stop operation. Release abandons the reservation
// without sending a termination request.
func (m *Manager) PrepareStop() (StopTarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd == nil || m.observation.State != StateRunning || m.stopTarget != nil || m.waitDone == nil {
		return nil, ErrStateConflict
	}
	m.stopTarget = m.cmd
	return &ownedStopTarget{manager: m, cmd: m.cmd, done: m.waitDone}, nil
}

// Terminate uses the reserved child handle only. Success requires the same
// child's Wait path to publish a confirmed exit and release ownership.
func (target *ownedStopTarget) Terminate(ctx context.Context) (Observation, error) {
	if target == nil || target.manager == nil || target.cmd == nil || target.done == nil {
		return Observation{}, ErrStateConflict
	}
	manager := target.manager
	defer target.Release()

	manager.mu.Lock()
	if manager.stopTarget != target.cmd {
		observation := manager.observeLocked()
		manager.mu.Unlock()
		return observation, ErrStateConflict
	}
	if err := ctx.Err(); err != nil {
		observation := manager.observeLocked()
		manager.mu.Unlock()
		return observation, err
	}
	if manager.cmd == nil && manager.observation.State == StateExited {
		observation := manager.observeLocked()
		manager.mu.Unlock()
		return observation, nil
	}
	if manager.cmd != target.cmd || manager.observation.State != StateRunning {
		observation := manager.observeLocked()
		manager.mu.Unlock()
		return observation, ErrStopFailed
	}
	manager.mu.Unlock()

	killErr := target.cmd.Process.Kill()
	if killErr != nil {
		select {
		case <-target.done:
			// A natural exit may race the hard termination request. Confirm the
			// exact child's Wait/reap result below before declaring success.
		default:
			return manager.Observe(), fmt.Errorf("%w: %w", ErrStopFailed, killErr)
		}
	} else {
		<-target.done
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	observation := manager.observeLocked()
	if manager.cmd == nil && observation.State == StateExited {
		return observation, nil
	}
	return observation, ErrStopFailed
}

// Release removes only this target's reservation. It never terminates a child.
func (target *ownedStopTarget) Release() {
	if target == nil || target.manager == nil || target.cmd == nil {
		return
	}
	target.manager.mu.Lock()
	defer target.manager.mu.Unlock()
	if target.manager.stopTarget == target.cmd {
		target.manager.stopTarget = nil
	}
}

// childEnvironment strips the Supervisor-private namespace and executable
// setting. Other entries retain their names, values and order.
func childEnvironment(parent []string, windows bool) []string {
	// A nil Cmd.Env would silently restore full Supervisor environment inheritance.
	env := make([]string, 0, len(parent))
	for _, entry := range parent {
		key, _, _ := strings.Cut(entry, "=")
		if windows {
			key = strings.ToUpper(key)
		}
		if strings.HasPrefix(key, "CPAMP_RUNTIME_") ||
			key == "CPAMP_CPA_EXECUTABLE" ||
			key == "CPAMP_CPA_ARTIFACT_MANIFEST" ||
			key == "MANAGEMENT_PASSWORD" {
			continue
		}
		env = append(env, entry)
	}
	return env
}

// Observe returns a snapshot. Running means spawn succeeded and exit has not
// yet been observed by Wait; a fast exit can precede its observation.
func (m *Manager) Observe() Observation {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.observeLocked()
}

// ExitEvents returns the Supervisor-private confirmed-exit notification
// stream. Publishing never blocks the Wait/reap path. Lifecycle recovery also
// reconciles the exact observation after every durable spawn success, so a
// fast exit remains visible if its notification was coalesced.
func (m *Manager) ExitEvents() <-chan ExitEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.exitEventsLocked()
}

func (m *Manager) exitEventsLocked() chan ExitEvent {
	if m.exitEvents == nil {
		m.exitEvents = make(chan ExitEvent, exitEventBuffer)
	}
	return m.exitEvents
}

func (m *Manager) observeLocked() Observation {
	if m.observation.State == "" {
		return Observation{State: StateNotStarted}
	}
	return m.observation
}

func (m *Manager) wait(cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()
	m.mu.Lock()

	if m.cmd != cmd || m.waitDone != done {
		m.mu.Unlock()
		close(done)
		return
	}

	if cmd.ProcessState == nil {
		// An OS wait failure is not proof the child exited. Retain the handle
		// reservation so an unconfirmed exit cannot permit a second child.
		m.observation.State = StateUnknown
		m.observation.WaitError = err
		m.mu.Unlock()
		close(done)
		return
	}
	m.cmd = nil
	m.waitDone = nil
	instanceID := m.observation.InstanceID
	m.observation = Observation{State: StateExited, InstanceID: instanceID}
	if code := cmd.ProcessState.ExitCode(); code >= 0 {
		m.observation.ExitCode = code
		m.observation.ExitCodeKnown = true
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		m.observation.WaitError = err
	}
	events := m.exitEventsLocked()
	m.mu.Unlock()
	close(done)
	m.publishConfirmedExit(events, ExitEvent{InstanceID: instanceID})
}

// publishConfirmedExit never blocks Wait/reap. Publishers are serialized, and
// a full single-slot channel retains the greatest monotonic InstanceID so a
// delayed old-child publisher cannot overwrite a newer eligible crash.
func (m *Manager) publishConfirmedExit(events chan ExitEvent, event ExitEvent) {
	m.exitPublish.Lock()
	defer m.exitPublish.Unlock()
	select {
	case events <- event:
		return
	default:
	}

	select {
	case pending := <-events:
		if pending.InstanceID > event.InstanceID {
			event = pending
		}
	default:
		// The consumer already received the pending event.
	}
	select {
	case events <- event:
	default:
		// Defensive only: the sender lock and receive-only public API make a
		// second producer impossible here.
	}
}
