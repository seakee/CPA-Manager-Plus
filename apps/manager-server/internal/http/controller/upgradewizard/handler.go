// Package upgradewizard serves the two read-only routes of the Full Web Upgrade
// wizard: the preflight report and the persisted operation projection.
//
// Nothing here advances the operation, opens a writable store, migrates, or
// writes anything. Authentication proves the caller's key by reading one
// credential row from the surface's single resolved source; the status
// projection then reads the Manager store, and the preflight inspects the 1.x
// source. Both selects are bounded: the projection names its columns, so a value
// this package never selected (a source fingerprint, a backup digest, a data
// key, a filesystem path) cannot reach the browser even if the row carries it.
package upgradewizard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	// The projection opens the Manager store read-only through database/sql.
	_ "modernc.org/sqlite"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradesurface"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

// Contract is the wire contract of both routes.
const Contract = "cpamp.upgrade-wizard.v1"

// Route suffixes. Each surface mounts them under its own prefix: the fixed
// bootstrap API while the installation is unfinished, and the canonical
// continuation prefix before the product is complete.
const (
	PreflightSuffix = "/installation/upgrade/preflight"
	StatusSuffix    = "/installation/upgrade/status"
)

// DefaultTimeout bounds one read-only preflight. A 1.x source is inspected
// through a read-only snapshot and a bounded SQLite read; it must not hold a
// request open indefinitely. The credential read is bounded separately by
// upgradeservice.CredentialVerifyTimeout, which is far shorter: authentication
// runs on every request, the preflight only when the wizard asks for a report.
const DefaultTimeout = 30 * time.Second

// MountLookupTimeout bounds the per-request wait on the mount lookup. The
// lookup is a few filesystem calls on the operator's mount and normally answers
// in microseconds, but none of them can be cancelled: on a hung network mount
// they never return, and every status poll would wait on them. It is set beside
// upgradeservice.CredentialVerifyTimeout because it gates the same
// authentication, and kept separate from it because the two failures differ: a
// lookup that outlives this bound is a mount that did not answer in time —
// reported as CodeMountTimeout, and a retry is reasonable — while a credential
// read that outlives its own bound is a retry on a slow source.
//
// The bound is not the only protection: at most one lookup per handler is in
// flight at a time, so a mount that never answers holds exactly one goroutine
// however many status polls arrive; see Handler.resolveMount.
const MountLookupTimeout = 2 * time.Second

// CodeAdminUnauthorized is the negative identity verdict. It is the only 401
// this package writes.
const CodeAdminUnauthorized = "admin_unauthorized"

// Typed environment codes. They are reported instead of a bare 500 so the
// wizard can name the condition and offer a retry.
const (
	CodeLegacyRootMissing  = "upgrade_legacy_root_missing"
	CodeLegacyRootUnusable = "upgrade_legacy_root_unusable"
	CodePreflightTimeout   = "upgrade_preflight_timeout"
	CodePreflightFailed    = "upgrade_preflight_failed"
	CodeOperationUnread    = "upgrade_operation_unreadable"
	// CodeCredentialTimeout is the credential read's deadline verdict: the source
	// exists but did not answer in time. It is not an unreadable source and never
	// a wrong key.
	CodeCredentialTimeout = upgradeservice.CodeCredentialTimeout
	// CodeMountTimeout is the mount lookup's deadline verdict: the configured
	// mount did not answer inside MountLookupTimeout, so it was never assessed.
	// It is deliberately not CodeSourceUnreadable — that code says the database
	// is there and cannot be read, and its copy sends the operator to volume
	// permissions — because a mount that is only slow is repaired by trying
	// again, not by changing a file this surface never opened. The status marker
	// and a 503 both carry this code.
	CodeMountTimeout = "upgrade_mount_timeout"
	// CodeSourceSymlinked is the shared database file policy's refusal of a source
	// reached through a link. The preflight reports the same code for the same
	// mount.
	CodeSourceSymlinked = upgradeservice.CodeSourceSymlinked
	// CodeSourceMissing and CodeSourceUnreadable are the same codes the
	// preflight reports: the source itself is absent or cannot be read, so this
	// step can neither prove the key nor report on the source. They are never a
	// verdict on the credential, and they are never the ownership marker —
	// authentication does not inspect that marker at all.
	CodeSourceMissing    = upgradeservice.CodeSourceMissing
	CodeSourceUnreadable = upgradeservice.CodeSourceUnreadable
)

// Stages the projection understands. The list is deliberately a string
// allow-list rather than model.UpgradeStage: a row written by a newer build
// (for example a rolled_back stage) must still be readable, and the model
// validator would reject it, turning a real observation into a 500.
const (
	StagePreflight  = "preflight"
	StageCopyIn     = "copy_in"
	StageReconcile  = "reconcile"
	StageCPAImport  = "cpa_import"
	StageExternal   = "external_connect"
	StageVerify     = "verify"
	StageComplete   = "complete"
	StageFailed     = "failed"
	StageRolledBack = "rolled_back"
)

// NextAction is the single next decision the wizard may offer.
const (
	ActionPreflight  = "preflight"
	ActionCopyIn     = "copy_in"
	ActionCPAImport  = "cpa_import"
	ActionReconcile  = "reconcile"
	ActionComplete   = "complete"
	ActionRolledBack = "rolled_back"
)

// SourceRoot states whether the 1.x source the later steps need is still
// mounted. A recorded operation is readable without it — the row and the copy-in
// marker live in the Manager store — but its stage is not proof that a step that
// needs the source can run. "available" is the only non-error value; everything
// else is one of the typed codes above.
const SourceRootAvailable = "available"

// Evidence names the durable record the projection was read from. A copy-in
// marker is a record of an unfinished operation whose row has not committed yet,
// so it is evidence of progress, not of a remembered default.
const (
	EvidenceOperationRow = "operation_row"
	EvidenceCopyInMarker = "copy_in_marker"
	EvidenceNone         = "none"
)

// domainSourceCopy is the one domain the copy-in marker proves: the marker is
// published after the target was materialized, which is the same work the
// recorded copy_in row names.
const domainSourceCopy = "source_copy"

// Handler serves both routes. It has no write path at all.
//
// Authentication is the Upgrade surface's own credential: the source
// administrator key the bootstrap Upgrade entry already verified, presented as
// `Authorization: Bearer <key>`. Each request proves it through
// upgradeservice.VerifyAdminSource, which reads one resolved source file
// read-only — the credential row only, never a whole-file integrity check,
// because this runs on every request including every status poll. The file is
// the one the surface's shared resolution names: the mount's database when the
// shared file policy accepts it, otherwise the Manager store. That resolution
// has exactly one implementation (upgradeservice.CredentialDatabaseFrom) and one
// input — the mount this request inspected — so no caller-side alternative can
// point authentication at a file the policy refused. No session, cookie or Fresh
// claim is a substitute, and an absent decision cannot read as allow.
//
// The order is credential shape, then browser origin, then the credential, then
// the route's own question. The credential's file is the mount's database when
// the shared file policy accepts it, so that lookup is part of proving the key:
// it runs after the two gates that need no filesystem at all, it is bounded, at
// most one lookup per handler is in flight, and the sourceRoot marker reuses the
// answer the request already paid for. A caller that presents no credential and
// a cross-site caller reach no filesystem call at all.
//
// Browser callers are same-origin only, exactly as on the entry leaf.
type Handler struct {
	// LegacyRoot returns the configured 1.x data directory. It is read only,
	// and it is what the preflight qualifies; it is not automatically the
	// credential source.
	LegacyRoot func() string
	// PublicOrigin is the trusted public origin (CPAMP_PUBLIC_ORIGIN). Empty
	// means the request's own scheme and Host are the expected origin; an
	// unusable value trusts no browser origin at all.
	PublicOrigin string
	// Database returns the Manager database path that holds the operation row,
	// and it is the surface's credential source when the mount holds no
	// database the shared file policy accepts.
	Database func() string
	// Timeout bounds one preflight. Zero uses DefaultTimeout.
	Timeout time.Duration
	// MountTimeout bounds the per-request wait on the mount lookup. Zero uses
	// MountLookupTimeout.
	MountTimeout time.Duration

	// lookupMount is the test seam for the mount lookup: a test substitutes a
	// lookup that never answers to prove the bound and the one-in-flight rule.
	// Production leaves it nil and the package's own inspectMount runs, so no
	// caller — and no test — can install a second filesystem policy. It is a
	// field rather than a package variable so the substitution belongs to one
	// handler and cannot leak into a parallel test.
	lookupMount func(root string) mountResolution
	// mountMu guards mountFlight, and mountFlight is the lookup a request joins
	// instead of starting another. Both live on the Handler: the one-in-flight
	// rule is per handler, and the previous answer (or the bound's verdict) is
	// shared by every poll that arrives while a hung mount is being probed.
	mountMu     sync.Mutex
	mountFlight *mountFlight
}

// mountFlight is one mount lookup in progress. The goroutine that runs it fills
// answer and closes done; every request that arrives meanwhile waits on done
// instead of starting a second lookup, so a mount that never answers cannot
// accumulate one goroutine per poll.
type mountFlight struct {
	root   string
	done   chan struct{}
	answer mountResolution
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r == nil || r.URL == nil {
		http.NotFound(w, r)
		return
	}
	// The credential comes first on every surface order. An unauthenticated
	// caller therefore learns nothing about which resources exist or which verb
	// they take: an absent, wrong or unreadable credential is the only answer it
	// ever gets, whether the path is real, unknown, or read with the wrong
	// method.
	//
	// The shape of the credential and the browser's origin are decided from the
	// request alone, and both are decided before the mount is touched: a caller
	// that presents no credential, and a cross-site caller whatever it presents,
	// performs no filesystem lookup at all. A caller that does present a
	// credential shape from its own origin is one the surface has to test, and
	// the file it tests is the mount's own database, so it pays exactly one
	// bounded, shared lookup (see resolveMount).
	key, ok := bearerKey(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, CodeAdminUnauthorized)
		return
	}
	// One origin rule for the whole surface: the entry leaf applies it with this
	// same helper, so the page that authenticates and the reads behind it cannot
	// accept different browser origins.
	if upgradesurface.CrossSite(r, h.PublicOrigin) {
		writeError(w, http.StatusForbidden, upgradesurface.CodeOriginRejected)
		return
	}
	// One mount lookup serves the whole request: the credential's file is the
	// surface's one resolved source — the mount's database when the shared file
	// policy accepts it, otherwise the Manager store — so proving the key needs
	// that answer, and the sourceRoot marker asks the same question again.
	mount := h.resolveMount(r.Context())
	if !h.authenticate(w, r, mount, key) {
		return
	}
	if r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	path := r.URL.Path
	var preflight bool
	switch {
	case strings.HasSuffix(path, PreflightSuffix) && path != PreflightSuffix:
		preflight = true
	case strings.HasSuffix(path, StatusSuffix) && path != StatusSuffix:
	default:
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if preflight {
		h.servePreflight(w, r, mount)
		return
	}
	h.serveStatus(w, r, mount)
}

// authenticate proves the caller holds the source administrator credential. The
// key was bounded and trimmed by bearerKey, and the browser's origin was decided
// before this runs; nothing else about the request (a session header, a cookie)
// can stand in for the key. The mount was inspected once for this request, so
// both routes and both questions share one answer.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request, mount mountResolution, key string) bool {
	if mount.expired {
		// The caller's own deadline had already passed. The credential read
		// reports that as a deadline, and it is the honest verdict here too: the
		// mount was never assessed, so no mount condition may replace it.
		writeError(w, http.StatusServiceUnavailable, CodeCredentialTimeout)
		return false
	}
	database, status, code := h.credentialDatabase(mount)
	if status != 0 {
		writeError(w, status, code)
		return false
	}
	// VerifyAdminSource bounds its own read: this runs on every request,
	// including every status poll, and reads the credential row only.
	switch err := upgradeservice.VerifyAdminSource(r.Context(), database, key); {
	case err == nil:
		return true
	case errors.Is(err, upgradeservice.ErrAdminUnauthorized):
		writeError(w, http.StatusUnauthorized, CodeAdminUnauthorized)
	case errors.Is(err, upgradeservice.ErrCredentialVerifyTimeout):
		// A deadline is not an unreadable source: the volume may be healthy and
		// only slow, and the operator's next move is to retry, not to repair it.
		writeError(w, http.StatusServiceUnavailable, CodeCredentialTimeout)
	case errors.Is(err, upgradeservice.ErrSourceDatabaseMissing):
		writeError(w, http.StatusServiceUnavailable, CodeSourceMissing)
	case errors.Is(err, upgradeservice.ErrSourceSymlinked):
		// The preflight refuses the same mount with the same code: both paths read
		// the file itself, never whatever a link points at.
		writeError(w, http.StatusServiceUnavailable, CodeSourceSymlinked)
	default:
		// The credential is not wrong: the source could not be read to verify it.
		// Naming that condition keeps a wrong-password message off a mount the
		// operator has to fix first.
		writeError(w, http.StatusServiceUnavailable, CodeSourceUnreadable)
	}
	return false
}

// credentialDatabase names the one file whose administrator credential both
// routes verify, and it derives it from the mount this request already
// inspected. The fallback rule — the mount's database when the shared file
// policy accepts it, otherwise the Manager store — has one implementation
// (upgradeservice.CredentialDatabaseFrom) and one input (this request's mount),
// so no leaf and no caller can point authentication at a file the policy
// refused. When neither file exists the answer is the mount's own condition:
// the operator's next move is to fix that mount, not to guess at a credential.
func (h *Handler) credentialDatabase(mount mountResolution) (string, int, string) {
	if database := upgradeservice.CredentialDatabaseFrom(mount.database, h.managerDatabase()); database != "" {
		return database, 0, ""
	}
	if mount.code != "" {
		return "", http.StatusServiceUnavailable, mount.code
	}
	return "", http.StatusServiceUnavailable, CodeLegacyRootMissing
}

// managerDatabase is the Manager store the surface reads and falls back to. It
// is the operation row's own database on both carriers.
func (h *Handler) managerDatabase() string {
	if h.Database == nil {
		return ""
	}
	return strings.TrimSpace(h.Database())
}

// mountResolution is the one answer about the operator's mount that a request
// needs. A zero-lookup request (no root configured) is a resolution with no
// database and the legacy-root code.
type mountResolution struct {
	// root is the configured 1.x data directory, empty when none is configured.
	root string
	// database is the mount's database the shared file policy accepts. Empty
	// when the mount holds no database this build may read.
	database string
	// code names why the mount is not usable: the configured directory itself
	// (upgrade_legacy_root_missing / _unusable), the shared database file
	// policy's refusal of the file inside it, or upgrade_mount_timeout when the
	// lookup outlived its bound. It is the code the preflight uses for the same
	// condition, except the deadline: a mount that did not answer in time is
	// named as that, not as an unreadable database.
	code string
	// expired reports a request whose own deadline had already passed, so the
	// lookup never ran.
	expired bool
}

// state is the projection's sourceRoot marker: the only non-error value, or the
// typed code naming why a later step that needs the source cannot run.
func (m mountResolution) state() string {
	if m.database != "" {
		return SourceRootAvailable
	}
	if m.code != "" {
		return m.code
	}
	// Unreachable through resolveMount: a resolution with no accepted database
	// always names a condition. It is named rather than left empty so a marker
	// is never blank.
	return CodeSourceMissing
}

// rootInspects reports whether the resolution's code is about the configured
// directory rather than the database inside it. The preflight has nothing to
// inspect in that case; a file-level refusal is a report it produces itself.
func (m mountResolution) rootInspects() bool {
	return m.code == CodeLegacyRootMissing || m.code == CodeLegacyRootUnusable
}

// resolveMount answers the mount question once for the request, inside a bound.
// The filesystem calls behind it cannot be cancelled, so the lookup runs on its
// own goroutine and the request stops waiting at the bound.
//
// Two properties come from that, and neither depends on the mount answering:
//
//   - The bound is a deadline, not a lease. A lookup that outlives
//     MountLookupTimeout answers CodeMountTimeout — the mount did not answer in
//     time, which is a retry, not a file the operator has to repair — and the
//     request is answered without it.
//   - At most one lookup is in flight per handler. A poll that arrives while a
//     hung lookup is still running joins that same flight instead of starting
//     another, and takes its answer when it lands or its own bound's verdict if
//     it does not. A mount that never answers therefore holds one goroutine,
//     however many polls arrive, instead of one goroutine per poll.
func (h *Handler) resolveMount(ctx context.Context) mountResolution {
	root := ""
	if h.LegacyRoot != nil {
		root = strings.TrimSpace(h.LegacyRoot())
	}
	// A caller whose own deadline had already passed learns nothing from a
	// lookup, and no mount condition may replace that deadline's verdict.
	if ctx.Err() != nil {
		return mountResolution{root: root, expired: true}
	}
	timeout := h.MountTimeout
	if timeout <= 0 {
		timeout = MountLookupTimeout
	}
	flight := h.joinMountFlight(root)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-flight.done:
		return flight.answer
	case <-ctx.Done():
		return mountResolution{root: root, expired: true}
	case <-timer.C:
		// The lookup outlived its bound. The goroutine keeps running — its
		// filesystem calls cannot be cancelled — but it is not replaced: the
		// flight stays in place and the next poll joins it.
		return mountResolution{root: root, code: CodeMountTimeout}
	}
}

// joinMountFlight returns this request's share of the mount answer: the lookup
// already in flight for this root, or a new one started here. The configured
// root is fixed for the lifetime of a handler, so a flight is shared by every
// concurrent poll; a different root retires the previous flight rather than
// answering about another directory.
func (h *Handler) joinMountFlight(root string) *mountFlight {
	lookup := inspectMount
	if h.lookupMount != nil {
		lookup = h.lookupMount
	}
	h.mountMu.Lock()
	defer h.mountMu.Unlock()
	if h.mountFlight != nil && h.mountFlight.root == root {
		return h.mountFlight
	}
	flight := &mountFlight{root: root, done: make(chan struct{})}
	h.mountFlight = flight
	go func() {
		answer := lookup(root)
		h.mountMu.Lock()
		flight.answer = answer
		if h.mountFlight == flight {
			h.mountFlight = nil
		}
		h.mountMu.Unlock()
		close(flight.done)
	}()
	return flight
}

// inspectMount is the body of resolveMount: the configured directory's own
// condition, then the shared database file policy. It is a function, not a
// package variable: a test substitutes it through Handler.lookupMount, so the
// package holds no mutable state and a substitution cannot leak between tests.
func inspectMount(root string) mountResolution {
	if root == "" {
		return mountResolution{code: CodeLegacyRootMissing}
	}
	info, err := os.Stat(root)
	switch {
	case os.IsNotExist(err):
		return mountResolution{root: root, code: CodeLegacyRootMissing}
	case err != nil:
		return mountResolution{root: root, code: CodeLegacyRootUnusable}
	case !info.IsDir():
		return mountResolution{root: root, code: CodeLegacyRootUnusable}
	}
	database, refusal := upgradeservice.MountDatabase(root)
	if database == "" {
		if refusal == "" {
			// The directory check above passed, so this is only reachable if the
			// mount disappears between the two inspections.
			refusal = CodeSourceMissing
		}
		return mountResolution{root: root, code: refusal}
	}
	return mountResolution{root: root, database: database}
}

// bearerKey extracts the single bounded Bearer value. Anything else — no header,
// two headers, another scheme, an empty or padded key — is not a credential.
func bearerKey(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", false
	}
	key := strings.TrimPrefix(values[0], "Bearer ")
	if key == "" || len(key) > 4096 || strings.TrimSpace(key) != key {
		return "", false
	}
	return key, true
}

// preflightCheck mirrors one Upgrade preflight observation. Detail is free text
// and is scrubbed of host paths before it leaves this package.
type preflightCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type preflightGaps struct {
	DeletedEventRefs      int      `json:"deletedEventRefs"`
	MissingProjection     int      `json:"missingProjection"`
	MismatchedProjection  int      `json:"mismatchedProjection"`
	PastWatermark         int      `json:"pastWatermark"`
	ProjectionStateUsable bool     `json:"projectionStateUsable"`
	CoverageDaysWithGaps  int      `json:"coverageDaysWithGaps"`
	AffectedDates         []string `json:"affectedDates"`
}

type preflightDocument struct {
	ContractVersion string           `json:"contractVersion"`
	SourceKind      string           `json:"sourceKind"`
	SourceVersion   string           `json:"sourceVersion"`
	Fingerprint     string           `json:"fingerprint,omitempty"`
	Checks          []preflightCheck `json:"checks"`
	ParityGaps      preflightGaps    `json:"parityGaps"`
	Blocked         []string         `json:"blocked"`
	Ready           bool             `json:"ready"`
}

// servePreflight reports on the mount the operator supplied, never on the
// surface's resolved credential file. The credential may fall back to the
// Manager store when the mount holds no database the shared file policy accepts;
// this step still inspects the mount, so a refused mount is named as that mount's
// condition instead of being replaced by a file the operator did not ask about.
// The mount the request already inspected supplies the root; only the two
// directory-level conditions short-circuit it, because those leave nothing to
// inspect.
func (h *Handler) servePreflight(w http.ResponseWriter, r *http.Request, mount mountResolution) {
	if mount.rootInspects() {
		writeError(w, http.StatusServiceUnavailable, mount.code)
		return
	}
	root := mount.root
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	report, err := upgradeservice.Preflight(ctx, upgradeservice.Options{LegacyRoot: root})
	if err != nil {
		if ctx.Err() != nil {
			writeError(w, http.StatusServiceUnavailable, CodePreflightTimeout)
			return
		}
		writeError(w, http.StatusServiceUnavailable, CodePreflightFailed)
		return
	}
	scrub := newScrubber(root)
	checks := make([]preflightCheck, 0, len(report.Checks))
	for _, check := range report.Checks {
		checks = append(checks, preflightCheck{
			Name:   check.Name,
			Status: check.Status,
			Code:   check.Code,
			Detail: scrub.detail(check.Detail),
		})
	}
	blocked := append([]string{}, report.Blocked...)
	if blocked == nil {
		blocked = []string{}
	}
	dates := append([]string{}, report.ParityGaps.AffectedDates...)
	if dates == nil {
		dates = []string{}
	}
	writeJSON(w, http.StatusOK, preflightDocument{
		ContractVersion: Contract,
		SourceKind:      report.SourceKind,
		SourceVersion:   report.SourceVersion,
		Fingerprint:     abbreviate(report.Fingerprint),
		Checks:          checks,
		ParityGaps: preflightGaps{
			DeletedEventRefs:      report.ParityGaps.DeletedEventRefs,
			MissingProjection:     report.ParityGaps.MissingProjection,
			MismatchedProjection:  report.ParityGaps.MismatchedProjection,
			PastWatermark:         report.ParityGaps.PastWatermark,
			ProjectionStateUsable: report.ParityGaps.ProjectionStateUsable,
			CoverageDaysWithGaps:  report.ParityGaps.CoverageDaysWithGaps,
			AffectedDates:         dates,
		},
		Blocked: blocked,
		Ready:   report.Ready,
	})
}

// statusDocument is the operation row projection. Every field is either a
// bounded count, an enumerated stage, or a string the operation itself
// recorded; no fingerprint, digest or path has a field here on purpose.
type statusDocument struct {
	ContractVersion string `json:"contractVersion"`
	// Recorded reports a durable record of this operation: the row, or, before
	// the row commits, the copy-in marker that supersedes it.
	Recorded   bool   `json:"recorded"`
	Evidence   string `json:"evidence"`
	Stage      string `json:"stage"`
	StageKnown bool   `json:"stageKnown"`
	NextAction string `json:"nextAction"`
	// SourceRoot is SourceRootAvailable or the typed code naming why the 1.x
	// source is not usable. The marker mirrors the shared database file policy —
	// so a mount whose database is a link is named here rather than counted as
	// usable — and it is exactly the code the credential resolution reached for
	// this request. It is a mirror of the file policy, not of the preflight: a
	// mount whose ownership marker is absent, busy or corrupt still reads
	// available here and is refused by the preflight, which is the step that
	// actually qualifies the source. The projection never requires the mount; the
	// stage does not promise a step that needs it can run.
	SourceRoot     string          `json:"sourceRoot"`
	DomainsDone    []string        `json:"domainsDone"`
	SourceKind     string          `json:"sourceKind,omitempty"`
	SourceVersion  string          `json:"sourceVersion,omitempty"`
	Reconciliation json.RawMessage `json:"reconciliation"`
	ManualItems    []string        `json:"manualItems"`
	ErrorCode      string          `json:"errorCode,omitempty"`
	ErrorDetail    string          `json:"errorDetail,omitempty"`
	UpdatedAtMS    int64           `json:"updatedAtMs"`
}

func (h *Handler) serveStatus(w http.ResponseWriter, r *http.Request, mount mountResolution) {
	database := h.managerDatabase()
	if database == "" {
		writeError(w, http.StatusServiceUnavailable, CodeOperationUnread)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	document, err := readStatus(ctx, database)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, CodeOperationUnread)
		return
	}
	// The stage is answered from the Manager store whether or not the operator
	// mounted the source: an unfinished operation still has to be inspectable
	// when its mount is gone. What the mount would enable is stated separately,
	// from the one lookup this request already made: the shared database file
	// policy, which is the same question the credential resolution asked. A
	// stricter preflight verdict (a busy writer, an absent ownership marker, a
	// corrupt database) is not a mount-level refusal and is not claimed here.
	document.SourceRoot = mount.state()
	writeJSON(w, http.StatusOK, document)
}

type statusRow struct {
	Stage         string
	Domains       string
	SourceKind    sql.NullString
	SourceVersion sql.NullString
	Reconciled    string
	ManualItems   string
	ErrorCode     sql.NullString
	ErrorDetail   sql.NullString
	UpdatedAtMS   int64
}

// readStatus reads the single operation row without the model validator: a
// stage this build does not know (a newer build's rolled_back, or a failed
// attempt) is still an honest observation and must reach the operator. A store
// with no row is not the same as no operation: the copy-in publishes its
// provenance before the row commits, and that window must not read "preflight".
func readStatus(ctx context.Context, databasePath string) (statusDocument, error) {
	document := statusDocument{
		ContractVersion: Contract,
		Evidence:        EvidenceNone,
		StageKnown:      true,
		NextAction:      ActionPreflight,
		DomainsDone:     []string{},
		ManualItems:     []string{},
		Reconciliation:  json.RawMessage("{}"),
	}
	info, err := os.Stat(databasePath)
	if os.IsNotExist(err) {
		return withCopyInMarker(databasePath, document)
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return withCopyInMarker(databasePath, document)
	}
	db, err := sql.Open("sqlite", readOnlyDataSourceName(databasePath))
	if err != nil {
		return statusDocument{}, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { _ = db.Close() }()
	var present int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = 'upgrade_operations'`).Scan(&present); err != nil {
		return statusDocument{}, err
	}
	if present == 0 {
		return withCopyInMarker(databasePath, document)
	}
	var row statusRow
	err = db.QueryRowContext(ctx, `select stage, domains_done, source_kind, source_version,
		reconciliation, manual_items, error_code, error_detail, updated_at_ms
		from upgrade_operations where id = 1`).Scan(&row.Stage, &row.Domains, &row.SourceKind, &row.SourceVersion,
		&row.Reconciled, &row.ManualItems, &row.ErrorCode, &row.ErrorDetail, &row.UpdatedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return withCopyInMarker(databasePath, document)
	}
	if err != nil {
		return statusDocument{}, err
	}
	document.Recorded = true
	document.Evidence = EvidenceOperationRow
	document.Stage = row.Stage
	document.StageKnown = knownStage(row.Stage)
	document.NextAction = nextAction(row.Stage)
	document.DomainsDone = decodeStrings(row.Domains)
	// Manual items are operator instructions written by another step, so they
	// pass the same content scrubber as the error detail: a copy-pasted command
	// can carry a credential, and a path in an instruction is still a path.
	document.ManualItems = scrubList(decodeStrings(row.ManualItems))
	document.SourceKind = row.SourceKind.String
	document.SourceVersion = row.SourceVersion.String
	document.ErrorCode = row.ErrorCode.String
	document.ErrorDetail = scrubError(row.ErrorDetail.String)
	document.UpdatedAtMS = row.UpdatedAtMS
	document.Reconciliation = reconciliationSummary(row.Reconciled)
	return document, nil
}

// withCopyInMarker answers the window the row cannot. The copy-in publishes its
// provenance before the migration commits and removes it only after the row
// commits, so a marker that names this database with no row means the operation
// is in progress at copy_in — the same reading the startup admission makes. The
// stage is reported with the marker as its evidence, and the next action is the
// copy-in the operator has to resume; "preflight" would claim nothing had
// started while admission refuses every normal surface.
func withCopyInMarker(databasePath string, document statusDocument) (statusDocument, error) {
	marker, found, err := copyinmarker.Read(filepath.Dir(databasePath))
	if err != nil {
		return statusDocument{}, fmt.Errorf("read the copy-in provenance marker: %w", err)
	}
	if !found || !copyinmarker.SameTarget(marker.TargetDatabase, databasePath) {
		return document, nil
	}
	document.Recorded = true
	document.Evidence = EvidenceCopyInMarker
	document.Stage = StageCopyIn
	document.StageKnown = true
	document.NextAction = nextAction(StageCopyIn)
	// The marker is written after the target was materialized, which is exactly
	// the domain the recorded copy_in row names.
	document.DomainsDone = []string{domainSourceCopy}
	document.SourceKind = marker.SourceKind
	document.SourceVersion = marker.SourceVersion
	document.UpdatedAtMS = marker.WrittenAtMS
	return document, nil
}

// knownStage reports whether this build can name the recorded stage. An empty
// stage is the unrecorded operation — the wizard's own first step — so it is
// known; anything else outside the table was written by a newer build and must
// be shown verbatim instead of guessed at.
func knownStage(stage string) bool {
	switch stage {
	case "", StagePreflight, StageCopyIn, StageReconcile, StageCPAImport, StageExternal,
		StageVerify, StageComplete, StageFailed, StageRolledBack:
		return true
	default:
		return false
	}
}

// nextAction maps a stage onto the wizard's next decision. A failed operation
// resumes at reconciliation, and a stage this build does not know keeps the
// empty string: the caller must show the recorded stage verbatim rather than
// claim a step that may not be the real position.
func nextAction(stage string) string {
	switch stage {
	case "":
		return ActionPreflight
	case StagePreflight:
		return ActionPreflight
	case StageCopyIn:
		return ActionCopyIn
	case StageCPAImport:
		// The CPA import is a step of its own: it writes the destination and
		// proves it, so the wizard offers it rather than the reconciliation that
		// precedes it.
		return ActionCPAImport
	case StageReconcile, StageExternal, StageVerify, StageFailed:
		return ActionReconcile
	case StageComplete:
		return ActionComplete
	case StageRolledBack:
		return ActionRolledBack
	default:
		return ""
	}
}

// reconciliationSummary projects the operation's reconciliation output through
// a fixed allow-list: only the days it names. A producer's free-form payload
// never reaches the browser wholesale.
func reconciliationSummary(raw string) json.RawMessage {
	empty := json.RawMessage("{}")
	if strings.TrimSpace(raw) == "" {
		return empty
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return empty
	}
	seen := map[string]bool{}
	days := []string{}
	for _, key := range []string{"mismatchDays", "affectedDates"} {
		value, ok := object[key]
		if !ok {
			continue
		}
		var list []string
		if err := json.Unmarshal(value, &list); err != nil {
			continue
		}
		for _, day := range list {
			if !isDate(day) || seen[day] {
				continue
			}
			seen[day] = true
			days = append(days, day)
		}
	}
	sort.Strings(days)
	if len(days) > 366 {
		days = days[:366]
	}
	encoded, err := json.Marshal(map[string]any{"mismatchDays": days})
	if err != nil {
		return empty
	}
	return json.RawMessage(encoded)
}

func isDate(value string) bool {
	if len(value) != 10 {
		return false
	}
	for index, r := range value {
		if index == 4 || index == 7 {
			if r != '-' {
				return false
			}
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func decodeStrings(raw string) []string {
	values := []string{}
	if strings.TrimSpace(raw) == "" {
		return values
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return []string{}
	}
	if values == nil {
		return []string{}
	}
	return values
}

// scrubList applies the content scrubber to every item and keeps the list shape.
func scrubList(values []string) []string {
	scrubbed := make([]string, 0, len(values))
	for _, value := range values {
		scrubbed = append(scrubbed, scrubError(value))
	}
	return scrubbed
}

// Operator-visible strings are scrubbed by content, not by path shape: a
// credential carries no path, and an error written by another step can quote one
// without naming a file. A multi-line detail is evidence too, so it is scrubbed
// line by line instead of being dropped.
const (
	redactedText   = "<redacted>"
	sourceText     = "<source>"
	maxDetailLine  = 600
	maxDetailLines = 40
	// minProductKeyBody is the shortest key body this product issues after its
	// prefix. The key pattern and the wrapped-key rule both read it.
	minProductKeyBody = 4
)

var (
	// bearerTokenPattern is the credential half of a Bearer value.
	bearerTokenPattern = regexp.MustCompile(`(?i)(bearer\s+)[^\s,;"'()<>]+`)
	// basicCredentialPattern is an Authorization: Basic value, which is a
	// base64-encoded password: it is a credential in a different alphabet.
	basicCredentialPattern = regexp.MustCompile(`(?i)(authorization\s*:\s*basic\s+)[A-Za-z0-9+/=]{4,}`)
	// apiKeyHeaderPattern is an x-api-key header, assignment or env-style name.
	apiKeyHeaderPattern = regexp.MustCompile(`(?i)(x[-_]api[-_]key\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`)
	// issuedKeyPattern is the issued key prefix the Manager and its clients use.
	issuedKeyPattern = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{4,}`)
	// productKeyPrefixes and productKeyPattern are this product's own key
	// vocabulary: installation, usage and administrator keys all start with one
	// of these prefixes and carry no other marker. The prefix match is
	// case-insensitive, because a key pasted with a different prefix case is
	// still a credential, and the same list is what a key wrapped across a line
	// boundary is recognised by.
	productKeyPrefixes = []string{"cpamp_", "cmp_admin_"}
	productKeyPattern  = regexp.MustCompile(
		`(?i)\b(?:` + strings.Join(productKeyPrefixes, "|") + `)[A-Za-z0-9_.\-]{` + strconv.Itoa(minProductKeyBody) + `,}`)
	// passwordPattern is a password written as an assignment: a query string, a
	// DSN, an env-style variable or a copied command.
	passwordPattern = regexp.MustCompile(`(?i)(password\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&]+)`)
	// urlCredentialPattern is a URL whose authority embeds credentials.
	urlCredentialPattern = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://)[^/\s:@]+(:[^/\s@]*)?@`)
	// localPathPattern is an absolute POSIX path at a token boundary. The colon
	// of a URL scheme is not a boundary, so a remote URL is never a local path.
	localPathPattern = regexp.MustCompile(`(^|[\s"'(=])/[^\s"'),;:]+`)
	// windowsPathPattern is a drive-letter or UNC path at a token boundary.
	windowsPathPattern = regexp.MustCompile(`(^|[\s"'(=])(?:[A-Za-z]:\\[^\s"'),;]+|\\\\[^\s"'),;]+)`)
)

// scrubError keeps an operation's own error detail while removing host paths and
// credential material, so an error written by another step cannot become a path
// or secret disclosure here.
func scrubError(detail string) string {
	return newScrubber("").text(detail)
}

type scrubber struct{ replacements []string }

func newScrubber(root string) scrubber {
	s := scrubber{}
	if root != "" {
		s.replacements = append(s.replacements, root, filepath.Dir(root))
	}
	sort.Slice(s.replacements, func(i, j int) bool {
		return len(s.replacements[i]) > len(s.replacements[j])
	})
	return s
}

// detail scrubs one operator-visible preflight detail.
func (s scrubber) detail(detail string) string {
	return s.text(detail)
}

// text scrubs a possibly multi-line value: the configured source paths first,
// then credential material and local paths, then the per-line and total bounds.
func (s scrubber) text(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n")
	// A credential wrapped across two lines is still one credential: a lone
	// trailing `Bearer` and the line after it are joined before the patterns
	// see them. Nothing else is joined — a line break is evidence.
	lines = joinSplitBearer(lines)
	scrubbed := make([]string, 0, len(lines))
	// A product key broken mid-token is one credential too. The line that carries
	// the head is redacted by the key pattern, and the fragment on the next line
	// carries no prefix of its own, so the run that continues it is removed there
	// instead. The line break itself stays: it is still evidence of shape.
	openKey := false
	for _, line := range lines {
		line = strings.ReplaceAll(line, "\x00", "")
		if openKey {
			line = redactKeyTail(line)
		}
		openKey = endsWithOpenProductKey(line)
		for _, token := range s.replacements {
			if token == "" || token == "/" || token == "." {
				continue
			}
			line = strings.ReplaceAll(line, token, sourceText)
		}
		line = scrubCredentials(line)
		if len(line) > maxDetailLine {
			line = capText(line, maxDetailLine)
		}
		scrubbed = append(scrubbed, line)
	}
	if len(scrubbed) > maxDetailLines {
		scrubbed = scrubbed[:maxDetailLines]
	}
	return strings.Join(scrubbed, "\n")
}

// scrubCredentials redacts the credential shapes an error can quote. URL
// credentials go first so the userinfo cannot be read as part of a token.
func scrubCredentials(line string) string {
	line = urlCredentialPattern.ReplaceAllString(line, "$1"+redactedText+"@")
	line = basicCredentialPattern.ReplaceAllString(line, "$1"+redactedText)
	line = bearerTokenPattern.ReplaceAllString(line, "$1"+redactedText)
	line = apiKeyHeaderPattern.ReplaceAllString(line, "$1"+redactedText)
	line = productKeyPattern.ReplaceAllString(line, redactedText)
	line = issuedKeyPattern.ReplaceAllString(line, redactedText)
	line = passwordPattern.ReplaceAllString(line, "$1"+redactedText)
	line = windowsPathPattern.ReplaceAllString(line, "$1"+sourceText)
	line = localPathPattern.ReplaceAllString(line, "$1"+sourceText)
	return line
}

// joinSplitBearer joins a line whose last word is a bare `Bearer` with the line
// that carries its value, so a value wrapped onto the next line is redacted
// instead of surviving as an unlabelled secret.
func joinSplitBearer(lines []string) []string {
	joined := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(joined) != 0 && endsWithBareBearer(joined[len(joined)-1]) {
			joined[len(joined)-1] += " " + strings.TrimSpace(line)
			continue
		}
		joined = append(joined, line)
	}
	return joined
}

// endsWithBareBearer reports whether a line ends with the scheme word and no
// value after it.
func endsWithBareBearer(line string) bool {
	trimmed := strings.ToLower(strings.TrimRight(line, " \t"))
	return trimmed == "bearer" || strings.HasSuffix(trimmed, " bearer")
}

// redactKeyTail removes the leading key run of a line whose predecessor ended
// inside a product key: that run is the rest of the token, so it is credential
// material even though it carries no prefix of its own. A run shorter than the
// shortest key body is left alone — it is not a fragment this product issues.
func redactKeyTail(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	run := 0
	for _, r := range trimmed {
		if !isKeyRune(r) {
			break
		}
		run += len(string(r))
	}
	if run < minProductKeyBody {
		return line
	}
	return redactedText + trimmed[run:]
}

// endsWithOpenProductKey reports whether a line ends inside a product key: the
// text from the last prefix to the end of the line is nothing but key
// characters, so the next line may carry the rest of the same token.
func endsWithOpenProductKey(line string) bool {
	trimmed := strings.TrimRight(line, " \t")
	lower := strings.ToLower(trimmed)
	index := -1
	for _, prefix := range productKeyPrefixes {
		if at := strings.LastIndex(lower, prefix); at > index {
			index = at
		}
	}
	return index >= 0 && isKeyFragment(trimmed[index:])
}

// isKeyFragment reports whether a value is nothing but key characters.
func isKeyFragment(value string) bool {
	for _, r := range value {
		if !isKeyRune(r) {
			return false
		}
	}
	return value != ""
}

// isKeyRune reports whether r can appear in a product key body.
func isKeyRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '_', r == '.', r == '-':
		return true
	default:
		return false
	}
}

// capText truncates on a rune boundary so a cut never produces invalid text.
func capText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// abbreviate keeps the short form the console shows elsewhere: enough to tell
// two sources apart, never the full digest.
func abbreviate(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) > 12 {
		return trimmed[:12] + "…"
	}
	return trimmed
}

// readOnlyDataSourceName mirrors the bounded admission DSN: read-only, no
// pragma that persists state, immutable only when there is no live WAL.
func readOnlyDataSourceName(path string) string {
	immutable := true
	if info, err := os.Lstat(path + "-wal"); err == nil && info.Size() > 0 {
		immutable = false
	}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := &url.URL{Scheme: "file", Path: uriPath}
	query := dsn.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	if immutable {
		query.Set("immutable", "1")
	}
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{
		"contractVersion": Contract,
		"error":           map[string]string{"code": code},
	})
}
