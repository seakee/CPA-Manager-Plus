package collector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpqueue"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/resp"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	quotasnapshotsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

type Status struct {
	Collector      string `json:"collector"`
	Upstream       string `json:"upstream"`
	Mode           string `json:"mode"`
	Transport      string `json:"transport"`
	Queue          string `json:"queue"`
	LastConsumedAt int64  `json:"lastConsumedAt"`
	LastInsertedAt int64  `json:"lastInsertedAt"`
	TotalInserted  int64  `json:"totalInserted"`
	TotalSkipped   int64  `json:"totalSkipped"`
	DeadLetters    int64  `json:"deadLetters"`
	LastError      string `json:"lastError,omitempty"`
}

type RuntimeConfig struct {
	CPAUpstreamURL          string
	ManagementKey           string
	CollectorMode           string
	Queue                   string
	PopSide                 string
	BatchSize               int
	PollInterval            time.Duration
	TLSSkipVerify           bool
	SourceScope             string
	RetentionBudgetMS       int64
	RetentionBudgetVerified bool
}

type UsageEventHandler interface {
	HandleUsageEvents(ctx context.Context, cfg RuntimeConfig, events []usage.Event)
}

type UsageEventHandlerFunc func(ctx context.Context, cfg RuntimeConfig, events []usage.Event)

func (f UsageEventHandlerFunc) HandleUsageEvents(ctx context.Context, cfg RuntimeConfig, events []usage.Event) {
	if f != nil {
		f(ctx, cfg, events)
	}
}

type UsageRuntimeConfigHandler interface {
	UpdateRuntimeConfig(ctx context.Context, cfg RuntimeConfig)
}

type UsageRuntimeConfigRejecter interface {
	RejectRuntimeConfig(cfg RuntimeConfig)
}

type Manager struct {
	base              config.Config
	store             *store.Store
	snapshotResolver  *authSnapshotResolver
	quotaSnapshots    *quotasnapshotsvc.Service
	usageEventHandler UsageEventHandler
	mu                sync.Mutex
	cancel            context.CancelFunc
	status            Status
	runtimeCfg        RuntimeConfig
	// authRejected reports that the CPA refused the management credential this
	// Collector is running with. The Collector stops itself first: resending a
	// refused credential is what turns a rotation into an IP ban.
	authRejected func(runID uint64)
	// running notifies a consumer on healthy Collector polls until it
	// acknowledges readiness for runID by returning true.
	running             func(ctx context.Context, runID uint64, cfg RuntimeConfig) bool
	runningAckRunID     uint64
	runningAckZero      bool
	runningAttemptRunID uint64
	runningLastAttempt  time.Time
	// starts counts how many runs this Manager has opened. It is the identity of
	// the running Collector: every Start supersedes the previous run, so a caller
	// that must not restart one can prove it did not by seeing this unchanged.
	starts                 uint64
	inFlight               map[uint64]chan struct{}
	runRetention           map[uint64]runRetentionState
	activePollMu           sync.Mutex
	activePollCond         *sync.Cond
	activePolls            int
	nowFn                  func() int64
	httpClient             *http.Client
	preObserveSeqFn        func(ctx context.Context) (int64, bool)
	onObserved             func(ctx context.Context, ev ObservationEvent) error
	onUnreplayedDisconnect func(ctx context.Context, runID uint64, transport string)
	onFirstPollError       func(ctx context.Context, runID uint64, transport string, err error)
}

type runRetentionState struct {
	budgetMS int64
	verified bool
}

// ObservationEvent describes one Collector poll/subscribe observation or
// retained-queue heartbeat for atomic provenance persistence.
type ObservationEvent struct {
	RunID                   uint64
	Transport               string
	SourceID                string
	RetentionBudgetMS       int64
	RetentionBudgetVerified bool
	ObservedAtMS            int64
	RetainedQueue           bool
	InitialRunObserve       bool
	ForceGapAdvance         bool
	HadPollError            bool
	OutageMS                int64
	PreObserveSeq           int64
	PreObserveSeqSet        bool
}

func NewManager(base config.Config, store *store.Store) *Manager {
	return &Manager{
		base:             base,
		store:            store,
		snapshotResolver: newAuthSnapshotResolver(),
		quotaSnapshots:   quotasnapshotsvc.New(store),
		status: Status{
			Collector: "stopped",
			Mode:      collectorMode(base.CollectorMode),
			Queue:     base.Queue,
		},
		inFlight:     make(map[uint64]chan struct{}),
		runRetention: make(map[uint64]runRetentionState),
	}
}

const trialHoldCollectorPollInterval = 100 * time.Millisecond

// MaxRetainedQueueOutageMS bounds the FIFO queue outage duration across which
// CPA's 60s retained queue can recover without an unreplayed gap.
const (
	MaxRetainedQueueOutageMS         int64 = 45_000
	RetainedQueueHeartbeatIntervalMS int64 = 5_000
)

var errPreObserveSeqUnavailable = errors.New("usage monitoring disable sequence unavailable")

// VerifiedRetentionBudgetMS computes the conservative retained-queue outage
// budget (75% of CPA retention, capped at 45s) when CPA config retention is
// verified. Unverified or non-positive values return 0 so callers fail closed.
func VerifiedRetentionBudgetMS(cfg cpa.UsageConfig) int64 {
	if !cfg.RetentionVerified || cfg.RedisUsageQueueRetentionSeconds <= 0 {
		return 0
	}
	if cfg.RedisUsageQueueRetentionSeconds >= 60 {
		return MaxRetainedQueueOutageMS
	}
	budget := (int64(cfg.RedisUsageQueueRetentionSeconds) * 1000 * 3) / 4
	if budget > MaxRetainedQueueOutageMS {
		return MaxRetainedQueueOutageMS
	}
	if budget <= 0 {
		return 0
	}
	return budget
}

// ComputeSourceID returns a deterministic, secret-free identifier for one
// upstream queue/channel source so switching CPA URL, transport, queue, or
// installation scope cannot inherit continuity from an earlier source.
func ComputeSourceID(transport string, cfg RuntimeConfig, fallbackQueue string) string {
	t := strings.ToLower(strings.TrimSpace(transport))
	endpoint := canonicalSourceEndpoint(cfg.CPAUpstreamURL)
	queue := ""
	popSide := ""
	if t == "resp" || t == "subscribe" {
		queue = strings.TrimSpace(valueOr(cfg.Queue, fallbackQueue))
	}
	if t == "resp" {
		popSide = strings.ToLower(strings.TrimSpace(cfg.PopSide))
	}
	h := sha256.New()
	_, _ = io.WriteString(h, t)
	_, _ = h.Write([]byte{0})
	_, _ = io.WriteString(h, endpoint)
	_, _ = h.Write([]byte{0})
	_, _ = io.WriteString(h, queue)
	_, _ = h.Write([]byte{0})
	_, _ = io.WriteString(h, popSide)
	_, _ = h.Write([]byte{0})
	_, _ = io.WriteString(h, strings.TrimSpace(cfg.SourceScope))
	return hex.EncodeToString(h.Sum(nil))
}

func canonicalSourceEndpoint(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return trimmed
	}
	u.User = nil
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") ||
		(u.Scheme == "redis" && port == "6379") || (u.Scheme == "rediss" && port == "6380") {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else {
		u.Host = host
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

type pollRunObservationState struct {
	observed            bool
	hadPollError        bool
	unreplayedLoss      bool
	disconnectNotified  bool
	firstPollErrSent    bool
	healthyInAttempt    bool
	drainingInitial     bool
	drainPreSeq         int64
	drainPreSeqSet      bool
	firstPollFailedAtMS int64
	lastHealthyPollMS   int64
	lastHealthySeq      int64
	lastHealthySeqSet   bool
	lastPersistedAtMS   int64
}

func (s *pollRunObservationState) resetDrain() {
	s.drainingInitial = false
	s.drainPreSeq = 0
	s.drainPreSeqSet = false
}
func (m *Manager) ensurePollCondLocked() *sync.Cond {
	if m.activePollCond == nil {
		m.activePollCond = sync.NewCond(&m.activePollMu)
	}
	return m.activePollCond
}

func (m *Manager) beginPoll() (func(), bool) {
	m.activePollMu.Lock()
	if middleware.IsProcessTrialHoldActive() {
		m.activePollMu.Unlock()
		return nil, false
	}
	m.ensurePollCondLocked()
	m.activePolls++
	m.activePollMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.activePollMu.Lock()
			m.activePolls--
			m.ensurePollCondLocked().Broadcast()
			m.activePollMu.Unlock()
		})
	}, true
}

// QuiesceForTrialHold waits until any in-flight CPA batch pop and SQLite event
// persistence has completed while the process-wide trial hold is active.
func (m *Manager) QuiesceForTrialHold(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() {
		m.activePollMu.Lock()
		m.ensurePollCondLocked().Broadcast()
		m.activePollMu.Unlock()
	})
	defer stop()

	m.activePollMu.Lock()
	defer m.activePollMu.Unlock()
	cond := m.ensurePollCondLocked()
	for m.activePolls > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		cond.Wait()
	}
	return ctx.Err()
}

func waitWhileTrialHoldActive(ctx context.Context) error {
	for middleware.IsProcessTrialHoldActive() {
		if err := ctx.Err(); err != nil {
			return err
		}
		timer := time.NewTimer(trialHoldCollectorPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

func (m *Manager) Start(ctx context.Context, cfg RuntimeConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.runtimeCfg = cfg
	m.starts++
	runID := m.starts
	m.runningAckZero = false
	m.runningAttemptRunID = 0
	m.runningLastAttempt = time.Time{}
	handler := m.usageEventHandler
	m.status.Collector = "starting"
	m.status.Upstream = cfg.CPAUpstreamURL
	m.status.Mode = collectorMode(valueOr(cfg.CollectorMode, m.base.CollectorMode))
	m.status.Transport = ""
	m.status.Queue = valueOr(cfg.Queue, m.base.Queue)
	m.status.LastError = ""

	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	if m.inFlight == nil {
		m.inFlight = make(map[uint64]chan struct{})
	}
	if m.runRetention == nil {
		m.runRetention = make(map[uint64]runRetentionState)
	}
	for id := range m.runRetention {
		if id+4 < runID {
			delete(m.runRetention, id)
		}
	}
	if cfg.RetentionBudgetVerified {
		budget := cfg.RetentionBudgetMS
		if budget < 0 {
			budget = 0
		}
		m.runRetention[runID] = runRetentionState{budgetMS: budget, verified: true}
	}
	runDone := make(chan struct{})
	m.inFlight[runID] = runDone
	var remaining atomic.Int32
	remaining.Store(1)
	finishPart := func() {
		if remaining.Add(-1) == 0 {
			m.mu.Lock()
			delete(m.inFlight, runID)
			m.mu.Unlock()
			close(runDone)
		}
	}
	if runtimeHandler, ok := handler.(UsageRuntimeConfigHandler); ok {
		remaining.Add(1)
		go func() {
			defer finishPart()
			runtimeHandler.UpdateRuntimeConfig(runCtx, cfg)
		}()
	}
	go func() {
		defer finishPart()
		m.run(runCtx, cancel, cfg, runID)
	}()
}

func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.status.Collector = "stopped"
}

// RejectRuntimeConfig notifies the registered usage event handler that cfg's
// management credential was refused so downstream automation workers immediately
// stop presenting that credential on background retries or cooldown ticks.
func (m *Manager) RejectRuntimeConfig(cfg RuntimeConfig) {
	if m == nil {
		return
	}
	m.mu.Lock()
	handler := m.usageEventHandler
	m.mu.Unlock()
	if rejecter, ok := handler.(UsageRuntimeConfigRejecter); ok {
		rejecter.RejectRuntimeConfig(cfg)
	}
}

// StopAndWait cancels the active run and blocks until every started Collector
// goroutine (including superseded runs and auth-rejection callbacks) has exited
// or ctx expires.
func (m *Manager) StopAndWait(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		m.mu.Lock()
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		m.status.Collector = "stopped"
		if len(m.inFlight) == 0 {
			m.mu.Unlock()
			return nil
		}
		var waitCh chan struct{}
		for _, ch := range m.inFlight {
			waitCh = ch
			break
		}
		m.mu.Unlock()
		select {
		case <-waitCh:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// CancelRuntime cancels the active Collector run without waiting on goroutines
// and returns a wait closure that blocks until all currently in-flight runs
// (including auth-rejection callbacks) exit or ctx expires.
func (m *Manager) CancelRuntime() func(context.Context) error {
	return m.CancelRun(0)
}

// CancelRun cancels runID if it is the current run (or any active run when
// runID == 0) without waiting on goroutines, and returns a wait closure that
// blocks until that run (or all snapshotted in-flight runs when runID == 0)
// exits or ctx expires.
func (m *Manager) CancelRun(runID uint64) func(context.Context) error {
	if m == nil {
		return func(context.Context) error { return nil }
	}
	m.mu.Lock()
	if runID == 0 || m.starts == runID {
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		m.status.Collector = "stopped"
	}
	var waitChans []chan struct{}
	if runID != 0 {
		if ch := m.inFlight[runID]; ch != nil {
			waitChans = []chan struct{}{ch}
		}
	} else if len(m.inFlight) > 0 {
		waitChans = make([]chan struct{}, 0, len(m.inFlight))
		for _, ch := range m.inFlight {
			waitChans = append(waitChans, ch)
		}
	}
	m.mu.Unlock()
	return func(ctx context.Context) error {
		if ctx == nil {
			ctx = context.Background()
		}
		for _, ch := range waitChans {
			select {
			case <-ch:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
}

// ActiveRun reports whether runID (or any run when runID == 0) is currently active.
func (m *Manager) ActiveRun(runID uint64) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel == nil || m.status.Collector == "stopped" {
		return false
	}
	return runID == 0 || m.starts == runID
}
// StartCount is the identity of the running Collector: one increment per Start.
// A caller that must not restart a live Collector proves it did not by comparing
// this before and after, which a phase string alone cannot show — a restart is
// observable as "starting" only for a moment.
func (m *Manager) StartCount() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.starts
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// ActiveRuntimeConfig reports the configuration of the currently active
// Collector run, or false when the Collector is stopped.
func (m *Manager) ActiveRuntimeConfig() (RuntimeConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel == nil || m.status.Collector == "stopped" {
		return RuntimeConfig{}, false
	}
	return m.runtimeCfg, true
}

// ActiveRunConfig reports the configuration of runID when runID is the
// currently active, non-stopped Collector run.
func (m *Manager) ActiveRunConfig(runID uint64) (RuntimeConfig, bool) {
	if m == nil || runID == 0 {
		return RuntimeConfig{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.starts != runID || m.cancel == nil || m.status.Collector == "stopped" {
		return RuntimeConfig{}, false
	}
	return m.runtimeCfg, true
}

func (m *Manager) SetUsageEventHandler(handler UsageEventHandler) {
	m.mu.Lock()
	m.usageEventHandler = handler
	cfg := m.runtimeCfg
	running := m.cancel != nil
	m.mu.Unlock()
	if running {
		if runtimeHandler, ok := handler.(UsageRuntimeConfigHandler); ok {
			runtimeHandler.UpdateRuntimeConfig(context.Background(), cfg)
		}
	}
}

func (m *Manager) setStatus(update func(*Status)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	update(&m.status)
}

// SetAuthRejectionHandler installs the report of a CPA refusal of this
// Collector's management credential. The Collector stops before it reports: a
// refused credential is terminal until the connection is rebound, and every
// further request is one more failed attempt against a CPA that bans an address
// after a handful of them.
func (m *Manager) SetAuthRejectionHandler(handler func()) {
	if handler == nil {
		m.SetAuthRejectionRunHandler(nil)
		return
	}
	m.SetAuthRejectionRunHandler(func(uint64) { handler() })
}

// SetAuthRejectionRunHandler installs a run-scoped credential refusal callback
// receiving the StartCount identity of the Collector run that hit the refusal.
func (m *Manager) SetAuthRejectionRunHandler(handler func(runID uint64)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.authRejected = handler
}

// SetRunningHandler installs a callback invoked once when a Collector run
// proves transport readiness and transitions into "running".
func (m *Manager) SetRunningHandler(handler func(ctx context.Context, cfg RuntimeConfig)) {
	if handler == nil {
		m.SetRunningRunHandler(nil)
		return
	}
	m.SetRunningRunHandler(func(ctx context.Context, _ uint64, cfg RuntimeConfig) bool {
		handler(ctx, cfg)
		return true
	})
}

// SetRunningRunHandler installs a run-scoped readiness callback invoked on
// healthy Collector polls until it acknowledges readiness for runID by
// returning true.
func (m *Manager) SetRunningRunHandler(handler func(ctx context.Context, runID uint64, cfg RuntimeConfig) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = handler
	m.runningAckRunID = 0
	m.runningAckZero = false
	m.runningAttemptRunID = 0
	m.runningLastAttempt = time.Time{}
}

// SetNowFunc overrides the Manager millisecond clock for deterministic tests.
func (m *Manager) SetNowFunc(fn func() int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nowFn = fn
}

func (m *Manager) nowMS() int64 {
	m.mu.Lock()
	fn := m.nowFn
	m.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return time.Now().UnixMilli()
}

// SetPreObserveSeqFunc installs a callback snapshotting the current durable
// disable sequence immediately before a poll or subscription attempt begins.
func (m *Manager) SetPreObserveSeqFunc(fn func(ctx context.Context) (int64, bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.preObserveSeqFn = fn
}

// SetObservationCallbacks installs run-scoped callbacks invoked when a
// Collector run establishes proven observation or heartbeat and when an
// unreplayed gap occurs (Pub/Sub disconnect, post-pop DB failure, or outage
// exceeding MaxRetainedQueueOutageMS).
func (m *Manager) SetObservationCallbacks(
	onObserved func(ctx context.Context, ev ObservationEvent) error,
	onUnreplayedDisconnect func(ctx context.Context, runID uint64, transport string),
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onObserved = onObserved
	m.onUnreplayedDisconnect = onUnreplayedDisconnect
}

// SetFirstPollErrorHandler installs a callback invoked once per run if its
// first poll fails before establishing observation.
func (m *Manager) SetFirstPollErrorHandler(fn func(ctx context.Context, runID uint64, transport string, err error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onFirstPollError = fn
}

// SetHTTPClient overrides the HTTP client used by HTTP usage-queue polling.
func (m *Manager) SetHTTPClient(client *http.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.httpClient = client
}
// SetRunRetentionBudget binds the verified retained-queue outage budget for
// runID after CPA management config inspection finishes.
func (m *Manager) SetRunRetentionBudget(runID uint64, budgetMS int64) {
	if m == nil || runID == 0 {
		return
	}
	if budgetMS < 0 {
		budgetMS = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.starts != runID {
		return
	}
	if m.runRetention == nil {
		m.runRetention = make(map[uint64]runRetentionState)
	}
	m.runRetention[runID] = runRetentionState{budgetMS: budgetMS, verified: true}
}

func (m *Manager) retentionBudget(runID uint64, cfg RuntimeConfig) (int64, bool) {
	m.mu.Lock()
	state, ok := m.runRetention[runID]
	m.mu.Unlock()
	if ok && state.verified {
		return state.budgetMS, true
	}
	if cfg.RetentionBudgetVerified {
		if cfg.RetentionBudgetMS < 0 {
			return 0, true
		}
		return cfg.RetentionBudgetMS, true
	}
	return 0, false
}

func (m *Manager) snapshotPreObserveSeq(ctx context.Context) (seq int64, ok bool, configured bool) {
	m.mu.Lock()
	fn := m.preObserveSeqFn
	m.mu.Unlock()
	if fn == nil {
		return 0, false, false
	}
	seq, ok = fn(ctx)
	return seq, ok, true
}

func (m *Manager) notifyObserved(ctx context.Context, ev ObservationEvent, allowStoppedFlush bool) error {
	m.mu.Lock()
	sameRun := ev.RunID == 0 || m.starts == ev.RunID
	active := sameRun && ((ctx == nil || ctx.Err() == nil) && m.cancel != nil && m.status.Collector != "stopped" || allowStoppedFlush)
	fn := m.onObserved
	m.mu.Unlock()
	if !active || fn == nil {
		return nil
	}
	return fn(ctx, ev)
}

func (m *Manager) notifyUnreplayedDisconnect(ctx context.Context, runID uint64, transport string) {
	m.mu.Lock()
	sameRun := runID == 0 || m.starts == runID
	fn := m.onUnreplayedDisconnect
	m.mu.Unlock()
	if sameRun && fn != nil {
		fn(ctx, runID, transport)
	}
}

func (m *Manager) notifyFirstPollError(ctx context.Context, runID uint64, transport string, err error) {
	m.mu.Lock()
	active := (ctx == nil || ctx.Err() == nil) && m.cancel != nil && m.status.Collector != "stopped" && (runID == 0 || m.starts == runID)
	fn := m.onFirstPollError
	m.mu.Unlock()
	if active && fn != nil {
		fn(ctx, runID, transport, err)
	}
}

func (m *Manager) recordRetainedPollError(ctx context.Context, runID uint64, transport string, cfg RuntimeConfig, obs *pollRunObservationState, nowMS int64, postPopLoss bool) {
	obs.hadPollError = true
	if !obs.observed && obs.firstPollFailedAtMS == 0 {
		obs.firstPollFailedAtMS = nowMS
	}
	budgetMS, verified := m.retentionBudget(runID, cfg)
	effectiveBudget := budgetMS
	if !verified {
		effectiveBudget = MaxRetainedQueueOutageMS
	}
	exceededStartup := !obs.observed && obs.firstPollFailedAtMS > 0 && (effectiveBudget <= 0 || nowMS-obs.firstPollFailedAtMS > effectiveBudget)
	exceededHealthy := obs.observed && obs.lastHealthyPollMS > 0 && (effectiveBudget <= 0 || nowMS-obs.lastHealthyPollMS > effectiveBudget)
	if postPopLoss || exceededStartup || exceededHealthy {
		obs.unreplayedLoss = true
		if !obs.disconnectNotified {
			obs.disconnectNotified = true
			m.notifyUnreplayedDisconnect(context.WithoutCancel(ctx), runID, transport)
		}
	}
}

func (m *Manager) recordRetainedPollSuccess(ctx context.Context, runID uint64, transport string, cfg RuntimeConfig, sourceID string, obs *pollRunObservationState, nowMS int64, preSeq int64, preSeqOK bool) {
	budgetMS, verified := m.retentionBudget(runID, cfg)
	effectiveBudget := budgetMS
	if !verified {
		effectiveBudget = MaxRetainedQueueOutageMS
	}
	var outageMS int64
	if obs.hadPollError && !obs.observed && obs.firstPollFailedAtMS > 0 && nowMS >= obs.firstPollFailedAtMS {
		outageMS = nowMS - obs.firstPollFailedAtMS
	}
	if obs.observed && obs.lastHealthyPollMS > 0 && nowMS > obs.lastHealthyPollMS {
		elapsed := nowMS - obs.lastHealthyPollMS
		if (obs.hadPollError || effectiveBudget <= 0 || elapsed > effectiveBudget) && elapsed > outageMS {
			outageMS = elapsed
		}
	}
	exceededStartup := !obs.observed && obs.hadPollError && obs.firstPollFailedAtMS > 0 && (effectiveBudget <= 0 || nowMS-obs.firstPollFailedAtMS > effectiveBudget)
	exceededHealthy := obs.observed && obs.lastHealthyPollMS > 0 && (effectiveBudget <= 0 || nowMS-obs.lastHealthyPollMS > effectiveBudget)
	if exceededStartup || exceededHealthy {
		obs.unreplayedLoss = true
		if !obs.disconnectNotified {
			obs.disconnectNotified = true
			m.notifyUnreplayedDisconnect(context.WithoutCancel(ctx), runID, transport)
		}
	}
	needPersist := !obs.observed ||
		obs.unreplayedLoss ||
		obs.hadPollError ||
		obs.lastPersistedAtMS == 0 ||
		nowMS-obs.lastPersistedAtMS >= RetainedQueueHeartbeatIntervalMS
	if needPersist {
		ev := ObservationEvent{
			RunID:                   runID,
			Transport:               transport,
			SourceID:                sourceID,
			RetentionBudgetMS:       budgetMS,
			RetentionBudgetVerified: verified,
			ObservedAtMS:            nowMS,
			RetainedQueue:           true,
			InitialRunObserve:       !obs.observed,
			ForceGapAdvance:         obs.unreplayedLoss,
			HadPollError:            obs.hadPollError,
			OutageMS:                outageMS,
			PreObserveSeq:           preSeq,
			PreObserveSeqSet:        preSeqOK,
		}
		if err := m.notifyObserved(ctx, ev, false); err == nil {
			obs.observed = true
			obs.hadPollError = false
			obs.unreplayedLoss = false
			obs.disconnectNotified = false
			obs.lastPersistedAtMS = nowMS
			obs.resetDrain()
		}
	} else {
		obs.hadPollError = false
		obs.resetDrain()
	}
	obs.lastHealthyPollMS = nowMS
	obs.lastHealthySeq = preSeq
	obs.lastHealthySeqSet = preSeqOK
	obs.healthyInAttempt = true
}

func (m *Manager) flushRetainedStop(ctx context.Context, runID uint64, transport string, cfg RuntimeConfig, sourceID string, obs *pollRunObservationState) {
	if !obs.observed || obs.hadPollError || obs.unreplayedLoss || obs.drainingInitial || obs.lastHealthyPollMS <= obs.lastPersistedAtMS {
		return
	}
	seq, seqOK := obs.lastHealthySeq, obs.lastHealthySeqSet
	if !seqOK {
		var configured bool
		seq, seqOK, configured = m.snapshotPreObserveSeq(context.WithoutCancel(ctx))
		if configured && !seqOK {
			return
		}
	}
	budgetMS, verified := m.retentionBudget(runID, cfg)
	_ = m.notifyObserved(context.WithoutCancel(ctx), ObservationEvent{
		RunID:                   runID,
		Transport:               transport,
		SourceID:                sourceID,
		RetentionBudgetMS:       budgetMS,
		RetentionBudgetVerified: verified,
		ObservedAtMS:            obs.lastHealthyPollMS,
		RetainedQueue:           true,
		InitialRunObserve:       false,
		ForceGapAdvance:         false,
		PreObserveSeq:           seq,
		PreObserveSeqSet:        seqOK,
	}, true)
}
func (m *Manager) setRunStatus(ctx context.Context, runID uint64, update func(*Status)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if (ctx != nil && ctx.Err() != nil) || (runID != 0 && m.starts != runID) {
		return
	}
	update(&m.status)
}

func (m *Manager) needsRunningProof(runID uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel == nil || m.status.Collector == "stopped" || (runID != 0 && m.starts != runID) {
		return false
	}
	if m.running == nil {
		return false
	}
	if runID != 0 {
		return m.runningAckRunID != runID
	}
	return !m.runningAckZero
}

func (m *Manager) markRunning(ctx context.Context, runID uint64, cfg RuntimeConfig, transport string) {
	m.markRunningPoll(ctx, runID, cfg, transport, true)
}

func (m *Manager) markRunningPoll(ctx context.Context, runID uint64, cfg RuntimeConfig, transport string, cadenceElapsed bool) {
	m.mu.Lock()
	if (ctx != nil && ctx.Err() != nil) || m.cancel == nil || m.status.Collector == "stopped" || (runID != 0 && m.starts != runID) {
		m.mu.Unlock()
		return
	}
	notify := m.running
	acked := notify == nil || (runID != 0 && m.runningAckRunID == runID) || (runID == 0 && m.runningAckZero)
	now := time.Now()
	if !acked && !cadenceElapsed && m.runningAttemptRunID == runID && !m.runningLastAttempt.IsZero() &&
		now.Sub(m.runningLastAttempt) < m.pollInterval(cfg) {
		m.status.Collector = "running"
		m.status.Transport = transport
		m.status.LastError = ""
		m.mu.Unlock()
		return
	}
	if acked {
		m.status.Collector = "running"
		m.status.Transport = transport
		m.status.LastError = ""
		m.mu.Unlock()
		return
	}
	m.runningAttemptRunID = runID
	m.runningLastAttempt = now
	m.mu.Unlock()

	proven := notify(ctx, runID, cfg)

	m.mu.Lock()
	defer m.mu.Unlock()
	if (ctx != nil && ctx.Err() != nil) || m.cancel == nil || m.status.Collector == "stopped" || (runID != 0 && m.starts != runID) {
		return
	}
	if proven {
		if runID != 0 {
			m.runningAckRunID = runID
		} else {
			m.runningAckZero = true
		}
	}
	m.status.Collector = "running"
	m.status.Transport = transport
	m.status.LastError = ""
}

// refuseAuth stops one run and reports the refusal. It acts only on the run that
// met it: the cancel func is that run's own, and a run whose context is already
// cancelled or whose StartCount was superseded by a restart must neither stop
// the new Collector nor record a blocker for a credential the new run does not
// use.
//
// The refusal itself is recorded as LastError, because a stop with no reason is
// the worst answer: the legacy Collector installs no handler, so without this it
// would stop silently and its status page would report a stopped Collector with an
// empty cause. An earlier error is kept if there is one — the refusal is added to
// the record, not used to erase why the Collector was already failing.
func (m *Manager) refuseAuth(ctx context.Context, cancel context.CancelFunc, err error) {
	m.refuseAuthRun(ctx, cancel, 0, err)
}

func (m *Manager) refuseAuthRun(ctx context.Context, cancel context.CancelFunc, runID uint64, err error) {
	if ctx != nil && ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	if (ctx != nil && ctx.Err() != nil) || (runID != 0 && m.starts != runID) {
		m.mu.Unlock()
		return
	}
	m.status.Collector = "stopped"
	if message := authRefusalMessage(err); message != "" {
		if m.status.LastError == "" {
			m.status.LastError = message
		} else {
			m.status.LastError += "; " + message
		}
	}
	rejectedCfg := m.runtimeCfg
	handler := m.usageEventHandler
	report := m.authRejected
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if rejecter, ok := handler.(UsageRuntimeConfigRejecter); ok {
		rejecter.RejectRuntimeConfig(rejectedCfg)
	}
	if report != nil {
		report(runID)
	}
}

// authRefusals are CPA's own replies to a management authentication it refused:
// the messages its RESP protocol writes for an invalid or missing key, a banned
// address, disabled remote management, and the NOAUTH it answers a command with.
// The list is deliberately closed. CPA answers other frames too — its home mode
// greets every RESP connection with `ERR redis usage output disabled in home
// mode` before any command — and treating those as a refused credential would
// stop a Collector that used to fall back to the HTTP queue instead.
var authRefusals = []string{
	"NOAUTH Authentication required.",
	"ERR invalid management key",
	"ERR missing management key",
	"ERR remote management key not set",
	"ERR remote management disabled",
	"ERR IP banned due to too many failed attempts",
}

// authRefusalMessage is the refusal as it is recorded for an operator. It is CPA's
// own text when there is one, so the status page says what the CPA said rather than
// paraphrasing it, and it never carries a credential: CPA's auth frames quote none.
func authRefusalMessage(err error) string {
	var reply *resp.ErrorReply
	if errors.As(err, &reply) {
		if message := strings.TrimSpace(reply.Message); message != "" {
			return "management credential refused: " + message
		}
	}
	if errors.Is(err, httpqueue.ErrRejected) {
		return "management credential refused: the CPA rejected the request"
	}
	return ""
}

// isAuthRefusal reports a management credential the CPA itself refused. Only the
// replies above and an HTTP 401/403 from the usage queue qualify: the server
// answered and refused this credential. Any other error frame, and any plain
// transport error, is not an answer about the credential and stays retryable.
func isAuthRefusal(err error) bool {
	var reply *resp.ErrorReply
	if errors.As(err, &reply) {
		message := strings.TrimSpace(reply.Message)
		for _, refusal := range authRefusals {
			if strings.HasPrefix(message, refusal) {
				return true
			}
		}
		return false
	}
	return errors.Is(err, httpqueue.ErrRejected)
}

func (m *Manager) run(ctx context.Context, cancel context.CancelFunc, cfg RuntimeConfig, runID uint64) {
	mode := collectorMode(valueOr(cfg.CollectorMode, m.base.CollectorMode))

	if mode == "subscribe" {
		m.runSubscribe(ctx, cancel, cfg, mode, runID)
		return
	}
	if mode == "auto" && m.runSubscribe(ctx, cancel, cfg, mode, runID) {
		return
	}
	if mode == "http" {
		m.runHTTP(ctx, cancel, cfg, mode, runID)
		return
	}
	if mode == "auto" && m.runHTTP(ctx, cancel, cfg, mode, runID) {
		return
	}
	m.runRESP(ctx, cancel, cfg, runID)
}

func (m *Manager) runSubscribe(ctx context.Context, cancel context.CancelFunc, cfg RuntimeConfig, mode string, runID uint64) bool {
	channel := valueOr(cfg.Queue, m.base.Queue)
	backoff := time.Second
	subscribed := false
	subObservedOnce := false
	subLost := false

	fallback := func() bool {
		m.setRunStatus(ctx, runID, func(status *Status) {
			status.Collector = "starting"
			status.Transport = "http"
			status.LastError = ""
		})
		return false
	}

	for {
		if ctx.Err() != nil {
			return true
		}
		if middleware.IsProcessTrialHoldActive() {
			if waitWhileTrialHoldActive(ctx) != nil {
				return true
			}
			continue
		}
		client, err := resp.DialContext(ctx, cfg.CPAUpstreamURL, cfg.TLSSkipVerify)
		if err != nil {
			if mode == "auto" && !subscribed {
				return fallback()
			}
			if !subscribed {
				m.notifyFirstPollError(ctx, runID, "subscribe", err)
			}
			m.markRunError(ctx, runID, "connect", err)
			sleep(ctx, backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		stopClose := context.AfterFunc(ctx, func() { _ = client.Close() })
		if err := client.Auth(cfg.ManagementKey); err != nil {
			stopClose()
			_ = client.Close()
			// A reply can land after a restart superseded this run. It says
			// nothing about the Collector now running — and that one uses another
			// credential — so the run ends here without stopping or reporting.
			if ctx.Err() != nil {
				return true
			}
			if isAuthRefusal(err) {
				// The CPA answered and refused this management credential. That
				// is terminal: another attempt is one more failed authentication
				// against a CPA that bans the address after a handful of them.
				m.refuseAuthRun(ctx, cancel, runID, err)
				return true
			}
			if mode == "auto" && !subscribed {
				return fallback()
			}
			if !subscribed {
				m.notifyFirstPollError(ctx, runID, "subscribe", err)
			}
			m.markRunError(ctx, runID, "auth", err)
			sleep(ctx, backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		preSeq, preSeqOK, preSeqConfigured := m.snapshotPreObserveSeq(ctx)
		if preSeqConfigured && !preSeqOK {
			stopClose()
			_ = client.Close()
			if !subscribed {
				m.notifyFirstPollError(ctx, runID, "subscribe", errPreObserveSeqUnavailable)
			}
			m.markRunError(ctx, runID, "pre-observe", errPreObserveSeqUnavailable)
			sleep(ctx, backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		if err := client.Subscribe(channel); err != nil {
			stopClose()
			_ = client.Close()
			if mode == "auto" && !subscribed {
				return fallback()
			}
			if !subscribed {
				m.notifyFirstPollError(ctx, runID, "subscribe", err)
			}
			m.markRunError(ctx, runID, "subscribe", err)
			sleep(ctx, backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		subscribed = true
		backoff = time.Second
		subReadyAtMS := m.nowMS()
		m.markRunning(ctx, runID, cfg, "subscribe")
		if obsErr := m.notifyObserved(ctx, ObservationEvent{
			RunID:             runID,
			Transport:         "subscribe",
			SourceID:          ComputeSourceID("subscribe", cfg, channel),
			ObservedAtMS:      subReadyAtMS,
			RetainedQueue:     false,
			InitialRunObserve: !subObservedOnce || subLost,
			ForceGapAdvance:   subLost,
			PreObserveSeq:     preSeq,
			PreObserveSeqSet:  preSeqOK,
		}, false); obsErr == nil {
			subObservedOnce = true
			subLost = false
		}
		err = m.consumeSubscribe(ctx, cfg, client, runID)
		stopClose()
		_ = client.Close()
		subLost = true
		m.notifyUnreplayedDisconnect(context.WithoutCancel(ctx), runID, "subscribe")
		if ctx.Err() != nil {
			return true
		}
		if err != nil {
			m.markRunError(ctx, runID, "subscribe", err)
			sleep(ctx, backoff)
			backoff = nextBackoff(backoff)
		}
	}
}

func (m *Manager) consumeSubscribe(ctx context.Context, cfg RuntimeConfig, client *resp.Client, runID uint64) error {
	const pingInterval = 30 * time.Second
	const readWindow = pingInterval + 10*time.Second

	stopDeadline := context.AfterFunc(ctx, func() {
		_ = client.SetReadDeadline(time.Now())
		_ = client.Close()
	})
	defer stopDeadline()

	lastPing := time.Now()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if middleware.IsProcessTrialHoldActive() {
			return nil
		}
		window := readWindow
		if m.needsRunningProof(runID) {
			if poll := m.pollInterval(cfg); poll < window {
				window = poll
			}
		}
		if err := client.SetReadDeadline(time.Now().Add(window)); err != nil {
			return err
		}
		_, payload, err := client.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if time.Since(lastPing) >= pingInterval {
					if perr := client.SendSubscribePing(); perr != nil {
						return perr
					}
					lastPing = time.Now()
				}
				if m.needsRunningProof(runID) {
					m.markRunningPoll(ctx, runID, cfg, "subscribe", true)
				}
				continue
			}
			return err
		}
		if m.needsRunningProof(runID) {
			m.markRunningPoll(ctx, runID, cfg, "subscribe", false)
		}
		if strings.TrimSpace(payload) == "" {
			continue
		}
		for {
			endPoll, ok := m.beginPoll()
			if ok {
				err := m.processItems(ctx, cfg, []string{payload})
				endPoll()
				if err != nil {
					return err
				}
				break
			}
			if err := waitWhileTrialHoldActive(ctx); err != nil {
				return err
			}
		}
	}
}

func (m *Manager) runHTTP(ctx context.Context, cancel context.CancelFunc, cfg RuntimeConfig, mode string, runID uint64) bool {
	client := httpqueue.New(cfg.CPAUpstreamURL, cfg.ManagementKey)
	m.mu.Lock()
	if m.httpClient != nil {
		client.HTTPClient = m.httpClient
	}
	m.mu.Unlock()
	sourceID := ComputeSourceID("http", cfg, "")
	backoff := time.Second
	obs := &pollRunObservationState{}

	for {
		if ctx.Err() != nil {
			m.flushRetainedStop(ctx, runID, "http", cfg, sourceID, obs)
			return true
		}
		obs.healthyInAttempt = false
		err := m.consumeHTTP(ctx, cfg, client, sourceID, runID, obs)
		if ctx.Err() != nil {
			m.flushRetainedStop(ctx, runID, "http", cfg, sourceID, obs)
			return true
		}
		if obs.healthyInAttempt {
			backoff = time.Second
		}
		if errors.Is(err, httpqueue.ErrUnsupported) && mode == "auto" {
			m.setRunStatus(ctx, runID, func(status *Status) {
				status.Collector = "starting"
				status.Transport = "resp"
				status.LastError = ""
			})
			return false
		}
		if ctx.Err() == nil && isAuthRefusal(err) {
			// The CPA answered and refused the management credential. That is
			// terminal until the connection is rebound, and every retry would be
			// another failed authentication against a CPA that bans the address.
			m.refuseAuthRun(ctx, cancel, runID, err)
			return true
		}
		if err != nil {
			m.markRunError(ctx, runID, "http", err)
			sleep(ctx, backoff)
			backoff = nextBackoff(backoff)
		}
	}
}

func (m *Manager) runRESP(ctx context.Context, cancel context.CancelFunc, cfg RuntimeConfig, runID uint64) {
	queue := valueOr(cfg.Queue, m.base.Queue)
	popSide := valueOr(cfg.PopSide, m.base.PopSide)
	sourceID := ComputeSourceID("resp", cfg, queue)
	backoff := time.Second
	obs := &pollRunObservationState{}

	for {
		if ctx.Err() != nil {
			m.flushRetainedStop(ctx, runID, "resp", cfg, sourceID, obs)
			return
		}
		client, err := resp.DialContext(ctx, cfg.CPAUpstreamURL, cfg.TLSSkipVerify)
		if err != nil {
			obs.resetDrain()
			m.recordRetainedPollError(ctx, runID, "resp", cfg, obs, m.nowMS(), false)
			if !obs.observed && !obs.firstPollErrSent {
				obs.firstPollErrSent = true
				m.notifyFirstPollError(ctx, runID, "resp", err)
			}
			m.markRunError(ctx, runID, "connect", err)
			sleep(ctx, backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		stopClose := context.AfterFunc(ctx, func() { _ = client.Close() })
		if err := client.Auth(cfg.ManagementKey); err != nil {
			stopClose()
			_ = client.Close()
			// A superseded run's late reply is not about the Collector running
			// now: the run ends without stopping it or recording its blocker.
			if ctx.Err() != nil {
				m.flushRetainedStop(ctx, runID, "resp", cfg, sourceID, obs)
				return
			}
			if isAuthRefusal(err) {
				m.refuseAuthRun(ctx, cancel, runID, err)
				return
			}
			obs.resetDrain()
			m.recordRetainedPollError(ctx, runID, "resp", cfg, obs, m.nowMS(), false)
			if !obs.observed && !obs.firstPollErrSent {
				obs.firstPollErrSent = true
				m.notifyFirstPollError(ctx, runID, "resp", err)
			}
			m.markRunError(ctx, runID, "auth", err)
			sleep(ctx, backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = time.Second
		err = m.consumeRESP(ctx, cfg, client, queue, popSide, sourceID, runID, obs)
		stopClose()
		_ = client.Close()
		if ctx.Err() != nil {
			m.flushRetainedStop(ctx, runID, "resp", cfg, sourceID, obs)
			return
		}
		if err != nil {
			obs.hadPollError = true
			m.markRunError(ctx, runID, "consume", err)
			sleep(ctx, backoff)
			backoff = nextBackoff(backoff)
		}
	}
}

func (m *Manager) consumeHTTP(ctx context.Context, cfg RuntimeConfig, client *httpqueue.Client, sourceID string, runID uint64, obs *pollRunObservationState) error {
	ticker := time.NewTicker(m.pollInterval(cfg))
	defer ticker.Stop()

	batchSize := m.batchSize(cfg)
	cadenceElapsed := true
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		endPoll, ok := m.beginPoll()
		if !ok {
			if err := waitWhileTrialHoldActive(ctx); err != nil {
				return err
			}
			cadenceElapsed = true
			continue
		}
		popStartedAtMS := m.nowMS()
		preSeq, preSeqOK, preSeqConfigured := m.snapshotPreObserveSeq(ctx)
		if preSeqConfigured && !preSeqOK {
			endPoll()
			obs.resetDrain()
			m.recordRetainedPollError(ctx, runID, "http", cfg, obs, popStartedAtMS, false)
			if !obs.observed && !obs.firstPollErrSent {
				obs.firstPollErrSent = true
				m.notifyFirstPollError(ctx, runID, "http", errPreObserveSeqUnavailable)
			}
			return errPreObserveSeqUnavailable
		}
		items, err := client.Pop(ctx, batchSize)
		if err != nil {
			endPoll()
			obs.resetDrain()
			if !errors.Is(err, httpqueue.ErrUnsupported) {
				m.recordRetainedPollError(ctx, runID, "http", cfg, obs, popStartedAtMS, httpqueue.IsUncertainPop(err))
				if !obs.observed && !obs.firstPollErrSent {
					obs.firstPollErrSent = true
					m.notifyFirstPollError(ctx, runID, "http", err)
				}
			}
			return err
		}
		m.markRunningPoll(ctx, runID, cfg, "http", cadenceElapsed)
		if err := m.processItems(ctx, cfg, items); err != nil {
			endPoll()
			obs.resetDrain()
			m.recordRetainedPollError(ctx, runID, "http", cfg, obs, popStartedAtMS, len(items) > 0)
			return err
		}
		endPoll()
		needsDrainObserve := !obs.observed || obs.unreplayedLoss || obs.hadPollError
		if needsDrainObserve {
			if !obs.drainingInitial || (preSeqConfigured && obs.drainPreSeqSet && preSeq != obs.drainPreSeq) {
				obs.drainingInitial = true
				obs.drainPreSeq = preSeq
				obs.drainPreSeqSet = preSeqOK
			}
			if len(items) >= batchSize {
				cadenceElapsed = false
				continue
			}
		} else {
			obs.resetDrain()
		}
		m.recordRetainedPollSuccess(ctx, runID, "http", cfg, sourceID, obs, popStartedAtMS, preSeq, preSeqOK)
		if len(items) == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				cadenceElapsed = true
				continue
			}
		}
		cadenceElapsed = false
	}
}

func (m *Manager) consumeRESP(ctx context.Context, cfg RuntimeConfig, client *resp.Client, queue string, popSide string, sourceID string, runID uint64, obs *pollRunObservationState) error {
	ticker := time.NewTicker(m.pollInterval(cfg))
	defer ticker.Stop()

	batchSize := m.batchSize(cfg)
	cadenceElapsed := true
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		endPoll, ok := m.beginPoll()
		if !ok {
			if err := waitWhileTrialHoldActive(ctx); err != nil {
				return err
			}
			cadenceElapsed = true
			continue
		}
		popStartedAtMS := m.nowMS()
		preSeq, preSeqOK, preSeqConfigured := m.snapshotPreObserveSeq(ctx)
		if preSeqConfigured && !preSeqOK {
			endPoll()
			obs.resetDrain()
			m.recordRetainedPollError(ctx, runID, "resp", cfg, obs, popStartedAtMS, false)
			if !obs.observed && !obs.firstPollErrSent {
				obs.firstPollErrSent = true
				m.notifyFirstPollError(ctx, runID, "resp", errPreObserveSeqUnavailable)
			}
			return errPreObserveSeqUnavailable
		}
		items, err := client.Pop(queue, popSide, batchSize)
		if err != nil {
			endPoll()
			obs.resetDrain()
			m.recordRetainedPollError(ctx, runID, "resp", cfg, obs, popStartedAtMS, resp.IsUncertainPop(err))
			if !obs.observed && !obs.firstPollErrSent {
				obs.firstPollErrSent = true
				m.notifyFirstPollError(ctx, runID, "resp", err)
			}
			return err
		}
		m.markRunningPoll(ctx, runID, cfg, "resp", cadenceElapsed)
		if err := m.processItems(ctx, cfg, items); err != nil {
			endPoll()
			obs.resetDrain()
			m.recordRetainedPollError(ctx, runID, "resp", cfg, obs, popStartedAtMS, len(items) > 0)
			return err
		}
		endPoll()
		needsDrainObserve := !obs.observed || obs.unreplayedLoss || obs.hadPollError
		if needsDrainObserve {
			if !obs.drainingInitial || (preSeqConfigured && obs.drainPreSeqSet && preSeq != obs.drainPreSeq) {
				obs.drainingInitial = true
				obs.drainPreSeq = preSeq
				obs.drainPreSeqSet = preSeqOK
			}
			if len(items) >= batchSize {
				cadenceElapsed = false
				continue
			}
		} else {
			obs.resetDrain()
		}
		m.recordRetainedPollSuccess(ctx, runID, "resp", cfg, sourceID, obs, popStartedAtMS, preSeq, preSeqOK)
		if len(items) == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				cadenceElapsed = true
				continue
			}
		}
		cadenceElapsed = false
	}
}

func (m *Manager) processItems(ctx context.Context, cfg RuntimeConfig, items []string) error {
	if len(items) == 0 {
		return nil
	}
	m.setStatus(func(status *Status) {
		status.LastConsumedAt = time.Now().UnixMilli()
	})
	events := make([]usage.Event, 0, len(items))
	for _, item := range items {
		payload := strings.TrimSpace(item)
		if payload == "" {
			continue
		}
		if control := classifyUsageControlPayload(payload); control != usageControlNone {
			if control == usageControlRefresh && m.snapshotResolver != nil {
				m.snapshotResolver.clear()
			}
			continue
		}
		event, err := usage.NormalizeRaw([]byte(payload))
		if err != nil {
			_ = m.store.AddDeadLetter(ctx, item, err)
			m.setStatus(func(status *Status) {
				status.DeadLetters++
			})
			continue
		}
		events = append(events, event)
	}
	m.enrichAccountSnapshots(ctx, cfg, events)
	result, err := m.store.InsertEvents(ctx, events)
	if err != nil {
		return err
	}
	if result.Inserted > 0 {
		inserted := insertedEvents(events, result.InsertedEventHashes)
		if err := m.quotaSnapshots.WriteUsageEvents(ctx, inserted); err != nil {
			log.Printf("persist usage quota snapshots: %v", err)
		}
		m.handleUsageEvents(ctx, cfg, inserted)
	}
	if result.Inserted > 0 || result.Skipped > 0 {
		m.setStatus(func(status *Status) {
			status.LastInsertedAt = time.Now().UnixMilli()
			status.TotalInserted += int64(result.Inserted)
			status.TotalSkipped += int64(result.Skipped)
		})
	}
	return nil
}

type usageControlPayload int

const (
	usageControlNone usageControlPayload = iota
	usageControlSupportRefresh
	usageControlRefresh
)

func classifyUsageControlPayload(payload string) usageControlPayload {
	var record map[string]bool
	if err := json.Unmarshal([]byte(payload), &record); err != nil {
		return usageControlNone
	}
	if len(record) != 1 {
		return usageControlNone
	}
	if record["refresh"] {
		return usageControlRefresh
	}
	if record["support_refresh"] {
		return usageControlSupportRefresh
	}
	return usageControlNone
}

func insertedEvents(events []usage.Event, insertedHashes []string) []usage.Event {
	if len(events) == 0 || len(insertedHashes) == 0 {
		return nil
	}
	remaining := make(map[string]int, len(insertedHashes))
	for _, hash := range insertedHashes {
		remaining[hash]++
	}
	inserted := make([]usage.Event, 0, len(insertedHashes))
	for _, event := range events {
		if remaining[event.EventHash] <= 0 {
			continue
		}
		inserted = append(inserted, event)
		remaining[event.EventHash]--
	}
	return inserted
}

func (m *Manager) handleUsageEvents(ctx context.Context, cfg RuntimeConfig, events []usage.Event) {
	m.mu.Lock()
	handler := m.usageEventHandler
	m.mu.Unlock()
	if handler == nil {
		return
	}
	handler.HandleUsageEvents(ctx, cfg, events)
}

func (m *Manager) enrichAccountSnapshots(ctx context.Context, cfg RuntimeConfig, events []usage.Event) {
	if len(events) == 0 || m.snapshotResolver == nil {
		return
	}
	authIndices := make(map[string]struct{})
	for i := range events {
		if events[i].AuthIndex == "" || !needsAccountSnapshotEnrichment(events[i]) {
			continue
		}
		authIndices[events[i].AuthIndex] = struct{}{}
	}
	if len(authIndices) == 0 {
		return
	}
	snapshots := m.snapshotResolver.lookup(ctx, cfg, authIndices)
	if len(snapshots) == 0 {
		return
	}
	for i := range events {
		if events[i].AuthIndex == "" || !needsAccountSnapshotEnrichment(events[i]) {
			continue
		}
		snapshot, ok := snapshots[events[i].AuthIndex]
		if !ok {
			continue
		}
		if (isCodexEvent(events[i]) || isCodexSnapshot(snapshot)) && !codexSnapshotCanEnrichEvent(events[i], snapshot) {
			// An auth-index lookup is only a credential locator. If the raw event
			// and the current auth-file response provide conflicting strong Codex
			// identity evidence, do not manufacture a workspace/member pair by
			// combining fields from the two observations.
			continue
		}
		updated := false
		accountSnapshotMissing := events[i].AccountSnapshot == ""
		if isCodexEvent(events[i]) {
			_, strongMember := usageidentity.NormalizeCodexMemberSnapshot(events[i].AccountSnapshot)
			accountSnapshotMissing = !strongMember
		}
		if accountSnapshotMissing && snapshot.Account != "" {
			events[i].AccountSnapshot = snapshot.Account
			updated = true
		}
		if events[i].AuthLabelSnapshot == "" && snapshot.Label != "" {
			events[i].AuthLabelSnapshot = snapshot.Label
			updated = true
		}
		if events[i].AuthFileSnapshot == "" && snapshot.FileName != "" {
			events[i].AuthFileSnapshot = snapshot.FileName
			updated = true
		}
		if events[i].AuthProviderSnapshot == "" && snapshot.Provider != "" {
			events[i].AuthProviderSnapshot = snapshot.Provider
			updated = true
		}
		if events[i].AuthAccountIDSnapshot == "" && snapshot.AccountID != "" {
			events[i].AuthAccountIDSnapshot = snapshot.AccountID
			updated = true
		}
		if events[i].AuthProjectIDSnapshot == "" && snapshot.ProjectID != "" {
			events[i].AuthProjectIDSnapshot = snapshot.ProjectID
			updated = true
		}
		if updated && events[i].AuthSnapshotAtMS == 0 {
			events[i].AuthSnapshotAtMS = snapshot.CapturedAtMS
		}
	}
}

func needsAccountSnapshotEnrichment(event usage.Event) bool {
	return accountSnapshotNeedsEnrichment(event) ||
		(isCodexEvent(event) && event.AuthAccountIDSnapshot == "") ||
		event.AuthProjectIDSnapshot == ""
}

func isCodexEvent(event usage.Event) bool {
	provider := strings.TrimSpace(event.AuthProviderSnapshot)
	if provider == "" {
		provider = strings.TrimSpace(event.Provider)
	}
	return strings.EqualFold(provider, "codex")
}

func isCodexSnapshot(snapshot authSnapshot) bool {
	return strings.EqualFold(strings.TrimSpace(snapshot.Provider), "codex")
}

func accountSnapshotNeedsEnrichment(event usage.Event) bool {
	if isCodexEvent(event) {
		_, ok := usageidentity.NormalizeCodexMemberSnapshot(event.AccountSnapshot)
		return !ok
	}
	return event.AccountSnapshot == ""
}

func codexSnapshotCanEnrichEvent(event usage.Event, snapshot authSnapshot) bool {
	if snapshot.AccountSnapshotInvalid || snapshot.AccountIDInvalid {
		return false
	}
	eventProvider := strings.TrimSpace(event.AuthProviderSnapshot)
	if eventProvider == "" {
		eventProvider = strings.TrimSpace(event.Provider)
	}
	snapshotProvider := strings.TrimSpace(snapshot.Provider)
	if eventProvider != "" && snapshotProvider != "" && !strings.EqualFold(eventProvider, snapshotProvider) {
		return false
	}
	eventMember, eventMemberOK := usageidentity.NormalizeCodexMemberSnapshot(event.AccountSnapshot)
	snapshotMember, snapshotMemberOK := usageidentity.NormalizeCodexMemberSnapshot(snapshot.Account)
	if eventMemberOK && snapshotMemberOK && eventMember != snapshotMember {
		return false
	}

	eventWorkspace, eventWorkspaceOK := usageidentity.NormalizeCodexWorkspaceSnapshot(event.AuthAccountIDSnapshot)
	snapshotWorkspace, snapshotWorkspaceOK := usageidentity.NormalizeCodexWorkspaceSnapshot(snapshot.AccountID)
	if eventWorkspaceOK && snapshotWorkspaceOK && eventWorkspace != snapshotWorkspace {
		return false
	}
	// An auth-index lookup may fill missing fields only when the two observations
	// retain enough evidence to prove that they describe the same member. Do not
	// combine a strong member from one side with a workspace-only observation from
	// the other side: that would manufacture e.g. workspace-B + member-A.
	if eventMemberOK && snapshotWorkspaceOK && !snapshotMemberOK && !eventWorkspaceOK {
		return false
	}
	if snapshotMemberOK && eventWorkspaceOK && !eventMemberOK && !snapshotWorkspaceOK {
		return false
	}
	return true
}

func (m *Manager) markError(stage string, err error) {
	m.markRunError(context.Background(), 0, stage, err)
}

func (m *Manager) markRunError(ctx context.Context, runID uint64, stage string, err error) {
	m.setRunStatus(ctx, runID, func(status *Status) {
		status.Collector = "error"
		status.LastError = stage + ": " + err.Error()
	})
}

func sleep(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > 30*time.Second {
		return 30 * time.Second
	}
	return next
}

func valueOr(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func collectorMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "http", "resp", "subscribe":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "auto"
	}
}

func (m *Manager) batchSize(cfg RuntimeConfig) int {
	if cfg.BatchSize > 0 {
		return cfg.BatchSize
	}
	if m.base.BatchSize <= 0 {
		return 100
	}
	return m.base.BatchSize
}

func (m *Manager) pollInterval(cfg RuntimeConfig) time.Duration {
	if cfg.PollInterval > 0 {
		return cfg.PollInterval
	}
	if m.base.PollInterval <= 0 {
		return 500 * time.Millisecond
	}
	return m.base.PollInterval
}
