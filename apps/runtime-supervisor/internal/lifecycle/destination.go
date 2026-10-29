package lifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/filetrust"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
)

// DestinationRequest deliberately permits only one native resource: client
// API keys. All paths, listener settings, auth storage and process ownership
// belong to this Supervisor, never to the source or the HTTP caller.
type DestinationRequest struct {
	OperationID               string      `json:"operationId"`
	ExpectedRuntimeIdentity   string      `json:"expectedRuntimeIdentity"`
	ExpectedRuntimeGeneration uint64      `json:"expectedRuntimeGeneration"`
	ExpectedActiveArtifactID  artifact.ID `json:"expectedActiveArtifactId"`
	SourceIdentity            string      `json:"sourceIdentity"`
	SourceClass               string      `json:"sourceClass"`
	APIKeys                   []string    `json:"apiKeys"`
	ManagementKey             string      `json:"managementKey"`
	// PluginsEnabled is the install-time opt-in carried by the Fresh operation.
	// It is a product decision, not a path: the plugin root is always the
	// Supervisor-owned Gateway directory, so a caller can never direct plugin
	// discovery elsewhere. Its zero value keeps plugins off.
	PluginsEnabled bool `json:"pluginsEnabled,omitempty"`
}

// SourceClassNewManaged is the default Fresh New Managed installation. It has
// no external source: its identity is the hashed Fresh operation. It carries
// exactly the single Client Key the Manager generated and persisted before
// staging this destination, because CPA v8 treats an empty client-key list as
// "client authentication disabled" and would publish the model API to every
// peer that can reach the child listener.
const SourceClassNewManaged = "new_managed"

// The two adopted source classes. An adopted destination is staged from this
// Supervisor's own template exactly like a New Managed one: the Manager owns it,
// so the template facts that belong to the installation rather than to the source
// — the usage switch included — are the installation's, not the operator's
// original CPA posture.
const (
	SourceClassAdoptNetwork = "network_existing"
	SourceClassAdoptLegacy  = "legacy_managed"
)

// managedSourceClass reports whether this Supervisor staged the destination, so
// its template facts apply. It is the one copy of that decision in this package.
func managedSourceClass(class string) bool {
	return class == SourceClassNewManaged || class == SourceClassAdoptNetwork || class == SourceClassAdoptLegacy
}

// initialClientKeyLength is the length of the Manager's install-generated
// Client Key ("sk-" followed by 64 [A-Za-z0-9] characters), the same shape
// v1.x installations were seeded with.
const initialClientKeyLength = 3 + 64

// validInitialClientKey reports whether key is the install-generated Client Key
// the Manager itself persists as a canonical Key. Any other value is refused:
// the Supervisor cannot distinguish a Manager-provided key from a caller-chosen
// one, so only this exact shape is admitted for new_managed.
func validInitialClientKey(key string) bool {
	if len(key) != initialClientKeyLength || !strings.HasPrefix(key, "sk-") {
		return false
	}
	for _, character := range key[3:] {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z') {
			return false
		}
	}
	return true
}

// ErrGatewayAuthPresent refuses to replace a bootstrap Gateway whose auth
// directory still holds provider logins the new destination would orphan.
var ErrGatewayAuthPresent = errors.New("bootstrap Gateway auth directory is not empty")

func (r DestinationRequest) Validate() error {
	prefix := "external:sha256:"
	switch r.SourceClass {
	case SourceClassAdoptNetwork, SourceClassAdoptLegacy:
	case SourceClassNewManaged:
		prefix = "managed:sha256:"
		if len(r.APIKeys) != 1 || !validInitialClientKey(r.APIKeys[0]) {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	if validateRequest(r.OperationID, r.ExpectedRuntimeIdentity, r.ExpectedRuntimeGeneration) != nil || !r.ExpectedActiveArtifactID.IsValid() ||
		!strings.HasPrefix(r.SourceIdentity, prefix) || len(r.SourceIdentity) != len(prefix)+64 ||
		len(r.ManagementKey) != 64 || r.APIKeys == nil || len(r.APIKeys) > 128 {
		return ErrInvalidRequest
	}
	if _, err := hex.DecodeString(r.ManagementKey); err != nil {
		return ErrInvalidRequest
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(r.SourceIdentity, prefix)); err != nil {
		return ErrInvalidRequest
	}
	seen := map[string]bool{}
	for _, key := range r.APIKeys {
		if key == "" || key != strings.TrimSpace(key) || len(key) > 4096 || strings.ContainsAny(key, "\x00\r\n") || seen[key] {
			return ErrInvalidRequest
		}
		for _, character := range key {
			if character < 0x21 || character == 0x7f {
				return ErrInvalidRequest
			}
		}
		seen[key] = true
	}
	return nil
}

type destination struct {
	root        string
	port        string
	child       *cpaprocess.ChildIdentity
	gatewayAuth string
}

// EnableDestination uses the existing Supervisor-private root and incarnation.
// The stage snapshot is immutable resource input, not a second operation ledger.
// gatewayAuth is the bootstrap Gateway auth directory a new_managed destination
// replaces; staging refuses to orphan provider logins stored there.
func (e *Executor) EnableDestination(root, gatewayAuth, address string, child *cpaprocess.ChildIdentity) error {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return ErrInvalidRequest
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	auth, err := filepath.Abs(gatewayAuth)
	if err != nil || gatewayAuth == "" {
		return ErrInvalidRequest
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.destination = &destination{root: abs, port: port, child: child, gatewayAuth: auth}
	_, err = e.destination.load()
	return err
}

// gatewayAuthEmpty reports whether the bootstrap Gateway holds no auth files.
// A missing directory is empty; anything but a real directory is untrusted.
func (d *destination) gatewayAuthEmpty() error {
	info, err := os.Lstat(d.gatewayAuth)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return ErrPersistenceUnavailable
	}
	entries, err := os.ReadDir(d.gatewayAuth)
	if err != nil {
		return ErrPersistenceUnavailable
	}
	if len(entries) != 0 {
		return ErrGatewayAuthPresent
	}
	return nil
}

func (d *destination) load() (*DestinationRequest, error) {
	info, err := os.Lstat(d.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.IsDir() || filetrust.RequireCurrentProcessOwner(info) != nil || filetrust.RequirePrivateDirectoryPermissions(info) != nil {
		return nil, ErrPersistenceUnavailable
	}
	name := filepath.Join(d.root, "stage.json")
	info, err = os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 || filetrust.RequireCurrentProcessOwner(info) != nil || (os.Geteuid() >= 0 && info.Mode().Perm()&0o077 != 0) {
		return nil, ErrPersistenceUnavailable
	}
	data, err := os.ReadFile(name)
	var r DestinationRequest
	if err != nil || json.Unmarshal(data, &r) != nil || r.Validate() != nil {
		return nil, ErrPersistenceUnavailable
	}
	canonical, _ := json.Marshal(r)
	if !bytes.Equal(data, canonical) {
		return nil, ErrPersistenceUnavailable
	}
	return &r, nil
}

// destinationIntent is the secret-free journal identity of one destination
// request. Not even a hash of keys enters the journal.
func destinationIntent(authority journal.Authority, r DestinationRequest) journal.Intent {
	public, _ := json.Marshal([]string{string(r.ExpectedActiveArtifactID), r.SourceIdentity, r.SourceClass})
	return journal.Intent{OperationID: r.OperationID, OperationType: "stage_destination", ExpectedRuntimeIdentity: r.ExpectedRuntimeIdentity, ExpectedRuntimeGeneration: r.ExpectedRuntimeGeneration, RequestFingerprint: sha256.Sum256(public)}
}

func (e *Executor) StageDestination(ctx context.Context, r DestinationRequest) (journal.Operation, error) {
	if err := r.Validate(); err != nil {
		return journal.Operation{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return journal.Operation{}, err
	}
	if e.closed.Load() || e.destination == nil {
		return journal.Operation{}, ErrPersistenceUnavailable
	}
	op, found, err := e.journal.Resolve(ctx, e.authority, destinationIntent(e.authority, r))
	if err != nil {
		return journal.Operation{}, submissionError(err)
	}
	stored, err := e.destination.load()
	if err != nil {
		return journal.Operation{}, err
	}
	// A retried attempt stages the identical destination under a fresh operation
	// ID, because the journal never resumes a terminal operation. Only terminal
	// failure of the superseded attempt, with no child it could have launched,
	// makes that safe; the replaced directory is retired, never deleted.
	supersedes := ""
	if stored != nil && !reflect.DeepEqual(*stored, r) {
		if err := e.supersedeDestination(ctx, *stored, r); err != nil {
			return journal.Operation{}, err
		}
		supersedes = stored.OperationID
	}
	if found {
		if op.State == journal.StateSucceeded && stored == nil {
			return journal.Operation{}, ErrPersistenceUnavailable
		}
		return op, nil
	}
	if (stored != nil && supersedes == "") || e.activeSelectionAmbiguous {
		return journal.Operation{}, journal.ErrOperationStateConflict
	}
	if state := e.process.Observe().State; state != cpaprocess.StateNotStarted && state != cpaprocess.StateExited {
		return journal.Operation{}, journal.ErrOperationStateConflict
	}
	if r.SourceClass == SourceClassNewManaged {
		if err := e.destination.gatewayAuthEmpty(); err != nil {
			return journal.Operation{}, err
		}
	}
	if e.artifact == nil {
		return journal.Operation{}, ErrActiveArtifactUnavailable
	}
	if err := refreshExpectedActiveArtifact(e.artifact, e.currentDescriptorLocked(), r.ExpectedActiveArtifactID); err != nil {
		return journal.Operation{}, err
	}
	op, created, err := e.journal.Begin(ctx, e.authority, destinationIntent(e.authority, r))
	if err != nil {
		e.failRecoveryForAmbiguousBegin(err)
		return journal.Operation{}, submissionError(err)
	}
	if !created {
		return op, nil
	}
	// A new destination supersedes any old child's pending recovery epoch.
	// Reuse the same recovery authority as explicit lifecycle admission.
	e.disableRecovery(RecoveryStateInactive)
	execution := context.Background()
	op, err = e.journal.MarkRunning(execution, e.authority.RuntimeIdentity, r.OperationID)
	if err != nil {
		e.disableRecovery(RecoveryStateManualIntervention)
		return op, ErrPersistenceUnavailable
	}
	if err = e.destination.publish(r, supersedes); err != nil {
		failed, completeErr := e.journal.Complete(execution, e.authority.RuntimeIdentity, r.OperationID, journal.StateFailed, "destination_stage_failed")
		return failed, errors.Join(ErrExecutionFailed, completeErr)
	}
	op, err = e.journal.Complete(execution, e.authority.RuntimeIdentity, r.OperationID, journal.StateSucceeded, "")
	if err != nil {
		e.disableRecovery(RecoveryStateManualIntervention)
	}
	return op, err
}

// supersedeDestination admits a stage that replaces the destination a previous
// attempt published. The journal never resumes a terminal operation, so a
// Manager that observed a terminal failure must retry under a new operation ID;
// refusing that here would make the install uncompletable, while admitting it
// unconditionally would let any caller displace a live destination.
//
// Admission requires the identical destination under a different operation ID
// and terminal evidence that the superseded attempt never owned a child:
// - its stage operation must be terminal, and
// - its Start operation must be absent or terminally failed.
// A stage may publish its live root and still be recorded as failed, because the
// final directory sync can fail after the rename. Absent Start evidence is only
// safe for such a stage, and only because the caller's process gate runs before
// any publish: a Start that is accepted, running or succeeded, or a live owned
// child, is an operator decision rather than a silent retry. A successful stage
// always needs its Start to be terminally failed, because that attempt may own
// the child the retry would replant.
//
// The Start operation ID of a destination attempt is that attempt's stage ID
// followed by ":start", the same derivation the Manager submits.
func (e *Executor) supersedeDestination(ctx context.Context, stored, r DestinationRequest) error {
	previous := stored
	previous.OperationID = r.OperationID
	if stored.OperationID == r.OperationID || !reflect.DeepEqual(previous, r) {
		return journal.ErrOperationIDConflict
	}
	stage, err := e.journal.Get(ctx, e.authority.RuntimeIdentity, stored.OperationID)
	if errors.Is(err, journal.ErrOperationNotFound) {
		return journal.ErrOperationStateConflict
	}
	if err != nil {
		return ErrPersistenceUnavailable
	}
	if stage.OperationType != "stage_destination" || (stage.State != journal.StateFailed && stage.State != journal.StateSucceeded) {
		return journal.ErrOperationStateConflict
	}
	start, err := e.journal.Get(ctx, e.authority.RuntimeIdentity, stored.OperationID+":start")
	startFailed := false
	switch {
	case errors.Is(err, journal.ErrOperationNotFound):
		// No Start was ever submitted for this attempt.
	case err != nil:
		return ErrPersistenceUnavailable
	case start.OperationType != "start":
		return journal.ErrOperationStateConflict
	case start.State == journal.StateFailed:
		startFailed = true
	default:
		return journal.ErrOperationStateConflict
	}
	if stage.State == journal.StateSucceeded && !startFailed {
		return journal.ErrOperationStateConflict
	}
	return nil
}

// requireStagedDestination refuses to spawn from a destination whose stage
// operation is not durably succeeded. The private profile enables no destination
// and is unaffected. Combined with the process gate it also covers a live owned
// child, so nothing is replanted under a running process.
func (e *Executor) requireStagedDestination(ctx context.Context) error {
	if e.destination == nil {
		return nil
	}
	staged, err := e.destination.load()
	if err != nil {
		return err
	}
	if staged == nil {
		return nil
	}
	stage, err := e.journal.Get(ctx, e.authority.RuntimeIdentity, staged.OperationID)
	if errors.Is(err, journal.ErrOperationNotFound) {
		return journal.ErrOperationStateConflict
	}
	if err != nil {
		return ErrPersistenceUnavailable
	}
	if stage.OperationType != "stage_destination" || stage.State != journal.StateSucceeded {
		return journal.ErrOperationStateConflict
	}
	return nil
}

// retiredDestination is the durable resting place of a superseded attempt. It
// is content-addressed by the superseded operation ID so a replay cannot
// overwrite the evidence of the attempt it replaced.
func (d *destination) retiredDestination(operationID string) string {
	digest := sha256.Sum256([]byte(operationID))
	return filepath.Join(filepath.Dir(d.root), ".destination-retired-"+hex.EncodeToString(digest[:16]))
}

// publish writes a complete destination beside the live one and swaps it in.
// A superseded attempt's directory is renamed aside, never deleted and never
// left as the live destination, so a failed retry cannot orphan it.
func (d *destination) publish(r DestinationRequest, supersedes string) error {
	parent := filepath.Dir(d.root)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || filetrust.RequireCurrentProcessOwner(info) != nil || filetrust.RequirePrivateDirectoryPermissions(info) != nil {
		return ErrPersistenceUnavailable
	}
	tmp, err := os.MkdirTemp(parent, ".destination-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	auth := filepath.Join(tmp, "auth")
	if err = os.Mkdir(auth, 0o700); err != nil {
		return err
	}
	if d.child != nil {
		if err = os.Chown(tmp, -1, int(d.child.GID)); err != nil {
			return err
		}
		if err = os.Chmod(tmp, 0o710); err != nil {
			return err
		}
		if err = os.Chown(auth, int(d.child.UID), int(d.child.GID)); err != nil {
			return err
		}
	}
	stage, _ := json.Marshal(r)
	if err = writeDestinationFile(filepath.Join(tmp, "stage.json"), stage, 0o600); err != nil {
		return err
	}
	// JSON is a YAML subset accepted by the exact CPA v8 config parser. Only
	// this new target receives resources; the source is never contacted here.
	// Usage statistics are productized for the installations this Supervisor
	// stages — New Managed and both adopted classes — because Key usage and the
	// Key workspace read the destination's own aggregation. A source CPA that is
	// not staged here is never rebuilt, so its posture is never touched.
	// The plugin root belongs to this Supervisor, never to the source or the
	// caller: CPA discovers native code there, so a template that let a request
	// name the directory would be a code-loading path. It sits in the Supervisor
	// root rather than inside the destination, because publish replaces the
	// whole destination directory while an installed plugin must survive both a
	// re-stage and a Runtime restart. It is child-owned so CPA can install into it.
	pluginDir := filepath.Join(filepath.Dir(d.root), "plugins")
	if err = ensurePluginDir(pluginDir, d.child); err != nil {
		return err
	}
	usage := managedSourceClass(r.SourceClass)
	config := map[string]any{"config-version": 8, "server": map[string]any{"host": "0.0.0.0", "port": json.Number(d.port)},
		"management": map[string]any{"allow-remote": true, "secret-key": r.ManagementKey, "disable-control-panel": true, "disable-auto-update-panel": true},
		"access":     map[string]any{"api-keys": r.APIKeys}, "oauth": map[string]any{"auth-dir": filepath.Join(d.root, "auth")},
		"plugins": map[string]any{"enabled": r.PluginsEnabled, "dir": pluginDir}, "observability": map[string]any{"logs": map[string]any{"logging-to-file": false}, "usage": map[string]any{"usage-statistics-enabled": usage}}}
	if d.child == nil {
		config["server"].(map[string]any)["host"] = "127.0.0.1"
	}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	configPath := filepath.Join(tmp, "config.yaml")
	if err = writeDestinationFile(configPath, data, 0o600); err != nil {
		return err
	}
	// CPA hashes its own management key at first startup; the immutable stage
	// snapshot remains Supervisor-only, while this config is child-owned.
	if d.child != nil {
		if err = os.Chown(configPath, int(d.child.UID), int(d.child.GID)); err != nil {
			return err
		}
		f, err := os.Open(configPath)
		if err != nil {
			return err
		}
		if err = errors.Join(f.Sync(), f.Close()); err != nil {
			return err
		}
	}
	if err = syncDestinationDirectory(auth); err != nil {
		return err
	}
	if err = syncDestinationDirectory(tmp); err != nil {
		return err
	}
	retired := ""
	if supersedes != "" {
		retired = d.retiredDestination(supersedes)
		if _, err = os.Lstat(retired); !errors.Is(err, os.ErrNotExist) {
			return ErrPersistenceUnavailable
		}
		if err = os.Rename(d.root, retired); err != nil {
			return err
		}
	}
	if err = os.Rename(tmp, d.root); err != nil {
		if retired == "" {
			return err
		}
		// A failed retry must not leave the Supervisor without a destination;
		// restore the superseded one before reporting the failure.
		return errors.Join(err, os.Rename(retired, d.root))
	}
	return syncDestinationDirectory(parent)
}

// ensurePluginDir creates the Supervisor-owned plugin root if it is absent and
// proves an existing one is a real, trusted directory. It never removes a
// plugin: the directory outlives a re-staged destination and a Runtime restart,
// and the child owns it so CPA can install into it. A directory a previous
// publish already handed to the child therefore stays admissible; anything else
// (a symlink, a file, a foreign owner) is refused instead of adopted.
func ensurePluginDir(name string, child *cpaprocess.ChildIdentity) error {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.Mkdir(name, 0o700); err != nil {
			return err
		}
		if info, err = os.Lstat(name); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrPersistenceUnavailable
	}
	// The directory is either ours or already handed to the child by an earlier
	// publish. A foreign owner is refused only when there is no child: with a
	// child identity the chown below reassigns it, so the child's own directory
	// from a previous publish is adopted rather than rejected. The parent
	// directory is private (checked earlier), so nothing else can have planted it.
	if filetrust.RequireCurrentProcessOwner(info) != nil && child == nil {
		return ErrPersistenceUnavailable
	}
	if child != nil {
		if err = os.Chown(name, int(child.UID), int(child.GID)); err != nil {
			return errors.Join(ErrPersistenceUnavailable, err)
		}
	}
	return os.Chmod(name, 0o700)
}

func writeDestinationFile(name string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

func syncDestinationDirectory(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func (e *Executor) startProcess(ctx context.Context, executable string) (cpaprocess.Observation, error) {
	spec := cpaprocess.StartSpec{Executable: executable}
	if e.destination != nil {
		staged, err := e.destination.load()
		if err != nil {
			return cpaprocess.Observation{}, err
		}
		if staged != nil {
			// Every Start, Restart and recovery spawn preserves this destination
			// and exact artifact, including after Supervisor reconstruction.
			f, err := os.Open(executable)
			if err != nil {
				return cpaprocess.Observation{}, err
			}
			h := sha256.New()
			_, hashErr := io.Copy(h, f)
			closeErr := f.Close()
			if hashErr != nil || closeErr != nil || "sha256:"+hex.EncodeToString(h.Sum(nil)) != string(staged.ExpectedActiveArtifactID) {
				return cpaprocess.Observation{}, ErrActiveArtifactMismatch
			}
			spec.Args = []string{"-config", filepath.Join(e.destination.root, "config.yaml")}
		}
	}
	return e.process.Start(ctx, spec)
}
