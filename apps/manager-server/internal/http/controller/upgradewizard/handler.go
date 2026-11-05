// Package upgradewizard serves the routes of the Full Web Upgrade wizard: the
// read-only preflight report, the persisted operation projection, and the one
// action that runs the completion gate.
//
// The two reads never advance the operation, open a writable store, migrate, or
// write anything. The action is the same gate the command line runs
// (upgradeservice.Verify): it re-derives the operation's evidence and records the
// one verdict the recorded target supports, so the page and the command line
// cannot disagree about whether the upgrade finished. Authentication proves the
// caller's key by reading one credential row from the surface's single resolved
// source; the status projection then reads the Manager store, and the preflight
// inspects the 1.x source. Both selects are bounded: the projection names its
// columns, so a value this package never selected (a source fingerprint, a backup
// digest, a data key, a filesystem path) cannot reach the browser even if the row
// carries it.
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
	"slices"
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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

// Contract is the wire contract of both routes.
const Contract = "cpamp.upgrade-wizard.v1"

// Route suffixes. Each surface mounts them under its own prefix: the fixed
// bootstrap API while the installation is unfinished, and the canonical
// continuation prefix before the product is complete.
const (
	PreflightSuffix = "/installation/upgrade/preflight"
	StatusSuffix    = "/installation/upgrade/status"
	// VerifySuffix is the one action route: it runs the completion gate the CLI
	// runs. GET and HEAD on it are refused, so every read of the surface stays a
	// read, and it is the only route here that writes.
	VerifySuffix = "/installation/upgrade/verify"
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
	// CodeOperationInvalid is the row's own verdict: the recorded operation
	// contradicts itself or the schema (a timestamp pair that runs backwards, a
	// list column that is not a JSON string array, a reconciliation body that is
	// not an object). It is a typed read error, never a 500 and never a stage the
	// operator can act on.
	CodeOperationInvalid = "upgrade_operation_invalid"
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
	ActionPreflight       = "preflight"
	ActionCopyIn          = "copy_in"
	ActionReconcile       = "reconcile"
	ActionCPAImport       = "cpa_import"
	ActionExternalConnect = "external_connect"
	ActionVerify          = "verify"
	ActionComplete        = "complete"
	ActionRolledBack      = "rolled_back"
)

// externalConnectRefusals are the typed codes the External connect step records
// when it stopped on the CPA itself. They are the same strings the upgrade
// service and the live External flow use; they are repeated here because this
// projection deliberately depends on no service vocabulary beyond the codes it
// reports. A row that carries one of them has not advanced, so the wizard offers
// the step that resolves it rather than the reconciliation that follows it.
var externalConnectRefusals = map[string]bool{
	"external_cpa_version_unsupported":     true,
	"external_cpa_config_document_invalid": true,
	"external_management_key_rejected":     true,
	"external_target_moved":                true,
}

// externalConnectStages are the stages a recorded External refusal can be sitting
// on. The step legitimately runs from the three of them — the copy-in of a legacy
// second volume finished, reconciliation finished, or the CPA import finished —
// and it never advances the row, so a refusal is found on whichever stage the
// operator ran it from. Offering reconciliation from any of them would send the
// operator to a step that is already finished and has nothing to do with the
// standing stop.
var externalConnectStages = map[string]bool{
	StageCopyIn:    true,
	StageReconcile: true,
	StageCPAImport: true,
}

// nextActionForRecordedStage refines the stage table with the refusal the row
// recorded. An operation parked on any stage the External connect step runs from
// by an External CPA it could not connect to must offer that step:
// reconciliation is not what resolves a v7 CPA or a rejected key, and offering it
// would send the operator past a stop that is still standing.
func nextActionForRecordedStage(stage, errorCode string, domains []string) string {
	if externalConnectStages[stage] && externalConnectRefusals[errorCode] {
		return ActionExternalConnect
	}
	return nextAction(stage, domains)
}

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

// domainCPAConfig is the domain the CPA import records: the destination holds
// the copied state and the target read it back. Its presence is what tells the
// wizard verification is next rather than a repeated import.
const domainCPAConfig = "cpa_config"

// Handler serves every route of the surface. The two reads have no write path at
// all, and the one action delegates to the shared completion gate rather than
// writing the store itself: this package decides no verdict, it reports the one
// the gate recorded.
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

// mountFlight is one mount lookup in progress. The goroutine that runs it
// resolves the configured root and inspects it, then fills answer and closes
// done; every request that arrives meanwhile waits on done instead of starting a
// second lookup, so a mount that never answers cannot accumulate one goroutine
// per poll.
type mountFlight struct {
	done   chan struct{}
	answer mountResolution
	// root and rootKnown publish the configured root as soon as the flight has
	// resolved it, before the lookup that can hang. A bound that expires while
	// the lookup is still unanswered can then still name the tree a credential
	// snapshot must stay out of; a bound that expires before the root is
	// resolved leaves rootKnown false, and a snapshot is refused rather than
	// staged under a tree nobody named. Both are written under Handler.mountMu.
	root      string
	rootKnown bool
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
	var preflight, verify bool
	switch {
	case strings.HasSuffix(path, PreflightSuffix) && path != PreflightSuffix:
		preflight = true
	case strings.HasSuffix(path, StatusSuffix) && path != StatusSuffix:
	case strings.HasSuffix(path, VerifySuffix) && path != VerifySuffix:
		verify = true
	default:
		http.NotFound(w, r)
		return
	}
	// The two reads stay reads: GET and HEAD only. The action takes POST and
	// nothing else, so a browser prefetch, a link or a bookmark of the action's
	// address can never advance the operation.
	if verify {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		h.serveVerify(w, r, mount)
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
	// The credential read bounds itself: this runs on every request, including
	// every status poll, and reads the credential row only. The file decides how
	// it is read: a preserved 1.x source is never opened in place and is read
	// through a private snapshot, while the Manager's own store is this
	// process's live database.
	var verifyErr error
	if mount.database != "" {
		verifyErr = upgradeservice.VerifyAdminSource(r.Context(), database, key)
	} else {
		// The Manager fallback is normally this process's live store and is read
		// in place. When it is a copy-in target that was not materialized
		// completely, the service detects that before opening it and proves the
		// key against a private snapshot; both trees that copy must stay out of
		// are named here, so a mount lookup that timed out still hands over the
		// root it resolved. A root the flight never resolved arrives with
		// rootKnown false and the service refuses the snapshot instead of
		// staging it in a tree this request could not name. A root the flight
		// did resolve is marked with the timeout that stopped the lookup: the
		// service refuses a snapshot that would have to resolve it again rather
		// than putting the filesystem call the bound exists to stop back on this
		// request.
		verifyErr = upgradeservice.VerifyManagerCredential(r.Context(), database, upgradeservice.CredentialSnapshotRoots{
			Target:         filepath.Dir(database),
			Source:         mount.root,
			SourceKnown:    mount.rootKnown,
			SourceTimedOut: mount.code == CodeMountTimeout,
		}, key)
	}
	switch {
	case verifyErr == nil:
		return true
	case errors.Is(verifyErr, upgradeservice.ErrAdminUnauthorized):
		writeError(w, http.StatusUnauthorized, CodeAdminUnauthorized)
	case errors.Is(verifyErr, upgradeservice.ErrSnapshotRootTimedOut):
		// The mount never answered, and the private copy would have had to
		// resolve that same root to keep out of it. The mount's own deadline is
		// the answer here, exactly as it is for the preflight: a retry once the
		// mount responds, not a verdict on the source or on the key.
		writeError(w, http.StatusServiceUnavailable, CodeMountTimeout)
	case errors.Is(verifyErr, upgradeservice.ErrCredentialVerifyTimeout):
		// A deadline is not an unreadable source: the volume may be healthy and
		// only slow, and the operator's next move is to retry, not to repair it.
		writeError(w, http.StatusServiceUnavailable, CodeCredentialTimeout)
	case errors.Is(verifyErr, upgradeservice.ErrSourceDatabaseMissing):
		writeError(w, http.StatusServiceUnavailable, CodeSourceMissing)
	case errors.Is(verifyErr, upgradeservice.ErrSourceSymlinked):
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
	// rootKnown reports whether root above is the answer to "what is
	// configured" or only an empty placeholder. A resolution that never got to
	// resolve the root — a lookup bound that expired while the root itself was
	// still being resolved — leaves it false, and the credential read refuses to
	// snapshot rather than guess the tree it must stay out of.
	rootKnown bool
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
// The filesystem calls behind it cannot be cancelled, so the whole lookup — the
// configured root's resolution included — runs on its own goroutine and the
// request stops waiting at the bound.
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
//
// The root resolution is part of the flight on purpose: the default resolver
// stats /legacy, and a stat on a hung mount waits exactly as long as the lookup
// behind it. Resolving the root before starting the flight would leave that wait
// outside the only bound this surface has.
func (h *Handler) resolveMount(ctx context.Context) mountResolution {
	// A caller whose own deadline had already passed learns nothing from a
	// lookup, and no mount condition may replace that deadline's verdict.
	if ctx.Err() != nil {
		return mountResolution{expired: true}
	}
	timeout := h.MountTimeout
	if timeout <= 0 {
		timeout = MountLookupTimeout
	}
	flight := h.joinMountFlight()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-flight.done:
		return flight.answer
	case <-ctx.Done():
		return mountResolution{expired: true}
	case <-timer.C:
		// The lookup outlived its bound. The goroutine keeps running — its
		// filesystem calls cannot be cancelled — but it is not replaced: the
		// flight stays in place and the next poll joins it. Whatever the flight
		// already resolved is carried into the answer, because a credential read
		// behind this request still has to keep its private snapshot out of the
		// configured source tree. A root the flight had not published yet stays
		// unknown rather than being guessed.
		h.mountMu.Lock()
		root, known := flight.root, flight.rootKnown
		h.mountMu.Unlock()
		return mountResolution{root: root, rootKnown: known, code: CodeMountTimeout}
	}
}

// joinMountFlight returns this request's share of the mount answer: the lookup
// already in flight, or a new one started here. A handler serves one configured
// root for its lifetime, so one flight at a time is both enough and the whole
// answer: every concurrent poll joins it.
func (h *Handler) joinMountFlight() *mountFlight {
	h.mountMu.Lock()
	defer h.mountMu.Unlock()
	if h.mountFlight != nil {
		return h.mountFlight
	}
	flight := &mountFlight{done: make(chan struct{})}
	h.mountFlight = flight
	go func() {
		// Resolve the root first and publish it: on a hung mount the lookup below
		// never returns, and the request's bound must still know which tree a
		// credential snapshot cannot land in. The publication is a plain field
		// assignment under the same mutex the answer uses.
		root := h.configuredRoot()
		h.mountMu.Lock()
		flight.root, flight.rootKnown = root, true
		h.mountMu.Unlock()
		answer := h.inspectMountAt(root)
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

// configuredRoot resolves the configured 1.x data directory. A handler with no
// resolver has no configured root, which is a known answer: the empty string.
// The call is the filesystem's — the default resolver stats /legacy — so it runs
// inside the flight, never on the request's own thread.
func (h *Handler) configuredRoot() string {
	if h.LegacyRoot == nil {
		return ""
	}
	return strings.TrimSpace(h.LegacyRoot())
}

// inspectMountAt runs the mount question for a root the flight already resolved:
// the shared database file policy on the database inside it. It is a function,
// not a package variable: a test substitutes the inspection through
// Handler.lookupMount, so the package holds no mutable state and a substitution
// cannot leak between tests.
func (h *Handler) inspectMountAt(root string) mountResolution {
	lookup := inspectMount
	if h.lookupMount != nil {
		lookup = h.lookupMount
	}
	answer := lookup(root)
	answer.root = root
	answer.rootKnown = true
	return answer
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
	// PendingDeclaration reports the one check this surface cannot answer: the
	// 1.x release is declared by the CLI copy-in, and a read-only read has no
	// way to say it. It is present on every answer (false when nothing is
	// pending) so a client never has to infer it, and it exists precisely so the
	// answer cannot be mistaken for the CLI's exact-equality acceptance.
	PendingDeclaration bool `json:"pendingDeclaration"`
	// ExpectedVersion is the release the copy-in must be told when the
	// declaration is pending. Empty when the caller declared a version.
	ExpectedVersion string `json:"expectedVersion,omitempty"`
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
	// A lookup that outlived its bound was never assessed. The root such an answer
	// carries is the tree the credential snapshot must stay out of, not an
	// inspection result: running the preflight against it would put the very
	// filesystem call the bound exists to stop back on the request thread. The
	// preflight's own deadline cannot interrupt that call.
	if mount.code == CodeMountTimeout {
		writeError(w, http.StatusServiceUnavailable, CodeMountTimeout)
		return
	}
	// The configured directory's own condition is the answer: there is nothing
	// inside a root that is missing or unusable to inspect, and the preflight's
	// own failure would replace that condition with a generic code. The mount
	// lookup keeps the root string on every branch (the credential snapshot needs
	// it), so the condition — not the presence of a root — is what short-circuits.
	// A file-level refusal keeps its root and is reported by the preflight itself,
	// with the same code the mount lookup named.
	if mount.rootInspects() || mount.root == "" {
		code := mount.code
		if code == "" {
			code = CodeLegacyRootMissing
		}
		writeError(w, http.StatusServiceUnavailable, code)
		return
	}
	root := mount.root
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	// The read cannot declare the 1.x release: only the operator's copy-in can.
	// An undeclared version is therefore reported as a pending observation, not
	// as a refusal, while every other check keeps its blocking power.
	report, err := upgradeservice.Preflight(ctx, upgradeservice.Options{
		LegacyRoot:              root,
		AcceptUndeclaredVersion: true,
	})
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
	pendingDeclaration := false
	expectedVersion := ""
	for _, check := range report.Checks {
		if check.Name != upgradeservice.CheckSourceVersion || check.Status != upgradeservice.StatusPending {
			continue
		}
		pendingDeclaration = true
		expectedVersion = check.ExpectedVersion
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
		Blocked:            blocked,
		Ready:              report.Ready,
		PendingDeclaration: pendingDeclaration,
		ExpectedVersion:    expectedVersion,
	})
}

// reconciliationDocument is the reconciliation output projected through a fixed
// allow-list. A producer's free-form payload never reaches the browser wholesale,
// and the one bounded list says how much it stands for: a list cut at the bound
// is never presented as the whole history.
type reconciliationDocument struct {
	MismatchDays          []string `json:"mismatchDays"`
	MismatchDaysTotal     int      `json:"mismatchDaysTotal"`
	MismatchDaysTruncated bool     `json:"mismatchDaysTruncated"`
}

// maxMismatchDays bounds one response. It is a transport bound, not a judgement
// about the history: a longer history is reported with its total and the flag,
// so a cut list is visibly a window instead of silently reading as everything.
const maxMismatchDays = 366

func emptyReconciliation() reconciliationDocument {
	return reconciliationDocument{MismatchDays: []string{}}
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
	SourceRoot     string                 `json:"sourceRoot"`
	DomainsDone    []string               `json:"domainsDone"`
	SourceKind     string                 `json:"sourceKind,omitempty"`
	SourceVersion  string                 `json:"sourceVersion,omitempty"`
	Reconciliation reconciliationDocument `json:"reconciliation"`
	ManualItems    []string               `json:"manualItems"`
	ErrorCode      string                 `json:"errorCode,omitempty"`
	ErrorDetail    string                 `json:"errorDetail,omitempty"`
	UpdatedAtMS    int64                  `json:"updatedAtMs"`
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
		// A row that contradicts itself is its own condition: it is not the same
		// as a store this step cannot read, and neither is a 500.
		if errors.Is(err, errStatusRowInvalid) {
			writeError(w, http.StatusServiceUnavailable, CodeOperationInvalid)
			return
		}
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

// verifyDocument is the action's answer: the position the gate recorded, in the
// vocabulary the status projection uses, so a page that posts and a page that
// polls read the same verdict.
//
// Nothing the gate observed for itself is repeated here: no source fingerprint,
// no backup digest, no connection secret and no filesystem path. The evidence is
// in the operation row, and this route reports the decision it supports.
type verifyDocument struct {
	ContractVersion string   `json:"contractVersion"`
	Recorded        bool     `json:"recorded"`
	Stage           string   `json:"stage"`
	StageKnown      bool     `json:"stageKnown"`
	NextAction      string   `json:"nextAction"`
	Complete        bool     `json:"complete"`
	TargetMode      string   `json:"targetMode"`
	DomainsDone     []string `json:"domainsDone"`
	ReasonCode      string   `json:"reasonCode,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	ManualItems     []string `json:"manualItems"`
	AlreadyVerified bool     `json:"alreadyVerified"`
	UpdatedAtMS     int64    `json:"updatedAtMs"`
}

// serveVerify runs the one completion gate the CLI runs.
//
// It is an action, not a read: it holds the v2 data root's ownership fence, the
// stopped source's read-only ownership marker and one write transaction, all
// inside upgradeservice.Verify. This handler decides only what the operation
// recorded looks like on the wire, so the page cannot disagree with the command
// line about whether the upgrade finished.
//
// The mounted 1.x root is the one this request already inspected, never a fresh
// unbounded filesystem call and never a value the caller supplied: the
// fingerprint is the proof that the volume the copy-in read was not written, and
// a root taken from the request would be exactly the claim this step exists to
// check. A request whose lookup never answered is refused with the deadline's
// own code instead of verifying a source nobody named.
func (h *Handler) serveVerify(w http.ResponseWriter, r *http.Request, mount mountResolution) {
	// The action takes no input. A body would be a caller's attempt to name the
	// source, the target or the authority, and none of those is this caller's to
	// state; it is refused rather than ignored.
	if r.ContentLength != 0 {
		writeError(w, http.StatusBadRequest, CodeRequestInvalid)
		return
	}
	target := h.managerDatabase()
	if target == "" {
		writeError(w, http.StatusServiceUnavailable, CodeOperationUnread)
		return
	}
	if !mount.rootKnown {
		// The lookup never resolved the configured root, so this request cannot
		// name the tree the verification would observe. That is the deadline's
		// verdict, never a credential error and never a verified source.
		writeError(w, http.StatusServiceUnavailable, CodeMountTimeout)
		return
	}
	if mount.database == "" {
		// The mount holds no database the shared file policy accepts, so the
		// volume the operation copied cannot be observed. The gate refuses a
		// verification that skipped the fingerprint; this route names the mount
		// condition the preflight names for the same tree.
		writeError(w, http.StatusServiceUnavailable, mount.state())
		return
	}
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	result, err := upgradeservice.Verify(ctx, upgradeservice.VerifyOptions{
		LegacyRoot: mount.root,
		TargetRoot: filepath.Dir(target),
	})
	if err != nil {
		writeError(w, actionFailureStatus(err), actionFailureCode(err))
		return
	}
	document := verifyDocument{
		ContractVersion: Contract,
		Recorded:        true,
		Stage:           string(result.Stage),
		StageKnown:      knownStage(string(result.Stage)),
		Complete:        result.Complete,
		TargetMode:      result.TargetMode,
		DomainsDone:     append([]string{}, result.DomainsDone...),
		ReasonCode:      result.ReasonCode,
		// The recorded instruction is operator text written by another step, so
		// it goes through the same scrubber the projection applies to error
		// detail: a reason is evidence, a path or a key inside it is not.
		Reason:          newScrubber(mount.root).detail(result.Reason),
		ManualItems:     scrubList(result.ManualItems),
		AlreadyVerified: result.AlreadyVerified,
		UpdatedAtMS:     result.UpdatedAtMS,
	}
	document.NextAction = nextActionForRecordedStage(document.Stage, document.ReasonCode, document.DomainsDone)
	writeJSON(w, http.StatusOK, document)
}

// CodeRequestInvalid is the action's own negative verdict on its request: it
// takes no input at all, so a body is refused rather than read.
const CodeRequestInvalid = "upgrade_request_invalid"

// actionFailureStatus maps a gate refusal onto the status code that says what
// the caller may do: retry once the named condition is gone (503), or look at
// the operation and its target (409). It never turns a typed refusal into a 500:
// the store is readable and the verdict is a decision, not a fault.
func actionFailureStatus(err error) int {
	code, ok := upgradeservice.UpgradeErrorCodeOf(err)
	if !ok {
		return http.StatusServiceUnavailable
	}
	switch code {
	case upgradeservice.UpgradeErrorVerifyV2Running,
		upgradeservice.UpgradeErrorVerifySourceUnavailable,
		upgradeservice.UpgradeErrorCode(upgradeservice.CodeSourceBusy):
		return http.StatusServiceUnavailable
	default:
		return http.StatusConflict
	}
}

// actionFailureCode names the refusal on the wire. A failure the gate did not
// type is reported as its own condition so the page can say "the verification
// failed" instead of inventing a code, and it never quotes the underlying error:
// that text can carry a host path.
func actionFailureCode(err error) string {
	if code, ok := upgradeservice.UpgradeErrorCodeOf(err); ok {
		return string(code)
	}
	return CodeVerifyFailed
}

// CodeVerifyFailed is the untyped action failure: the gate could not finish and
// the reason is not one of the codes this surface names.
const CodeVerifyFailed = "upgrade_verify_failed"

type statusRow struct {
	Stage         string
	Domains       string
	SourceKind    sql.NullString
	SourceVersion sql.NullString
	Reconciled    string
	ManualItems   string
	ErrorCode     sql.NullString
	ErrorDetail   sql.NullString
	CreatedAtMS   int64
	UpdatedAtMS   int64
}

// errStatusRowInvalid reports a recorded operation that contradicts itself or
// the schema. It is a typed read error: the projection must not turn a corrupt
// row into a stage the operator could act on, and it must not turn it into a
// 500 either.
var errStatusRowInvalid = errors.New("recorded upgrade operation is invalid")

// validateStatusRow checks the row's own consistency before any of it is
// projected. The stage is deliberately not validated: an unknown stage is a
// newer build's observation and is shown verbatim. What has to hold is that the
// row is a record at all — that its timestamps run forwards and its JSON columns
// are the shape the schema promises.
func validateStatusRow(row statusRow) error {
	if row.CreatedAtMS < 0 || row.UpdatedAtMS < 0 {
		return fmt.Errorf("%w: timestamps are negative", errStatusRowInvalid)
	}
	if row.CreatedAtMS > 0 && row.UpdatedAtMS > 0 && row.UpdatedAtMS < row.CreatedAtMS {
		return fmt.Errorf("%w: updated_at_ms precedes created_at_ms", errStatusRowInvalid)
	}
	return nil
}

// readStatus reads the single operation row without the model validator: a
// stage this build does not know (a newer build's rolled_back, or a failed
// attempt) is still an honest observation and must reach the operator. What it
// does validate is the row itself (see validateStatusRow): a corrupt or
// future-dated row is a typed read error, never a fabricated stage. A store with
// no row is not the same as no operation: the copy-in publishes its provenance
// before the row commits, and that window must not read "preflight".
//
// The marker is read before the store, exactly like startup admission. Staging
// is never polled; absent targets and foreign sidecars remain resume evidence.
func readStatus(ctx context.Context, databasePath string) (statusDocument, error) {
	document := emptyStatusDocument()
	marker, inFlight, err := readCopyInMarker(databasePath)
	if err != nil {
		return statusDocument{}, err
	}
	if inFlight && marker.AwaitingCommit(databasePath) {
		return withCopyInMarker(document, marker), nil
	}
	// A committing marker may outlive the atomically published operation row.
	recorded, found, err := readRecordedStatus(ctx, databasePath, document)
	if err != nil {
		return statusDocument{}, err
	}
	if found {
		return recorded, nil
	}
	if inFlight {
		return withCopyInMarker(recorded, marker), nil
	}
	return recorded, nil
}

// emptyStatusDocument is the unrecorded projection: no operation has started,
// and the next action is the preflight.
func emptyStatusDocument() statusDocument {
	return statusDocument{
		ContractVersion: Contract,
		Evidence:        EvidenceNone,
		StageKnown:      true,
		NextAction:      ActionPreflight,
		DomainsDone:     []string{},
		ManualItems:     []string{},
		Reconciliation:  emptyReconciliation(),
	}
}

// readRecordedStatus reads the operation row from the Manager store. found is
// false when the store holds no operation yet: the file is absent, empty or not a
// database this build can read, the table is absent, or no row exists.
func readRecordedStatus(ctx context.Context, databasePath string, document statusDocument) (statusDocument, bool, error) {
	info, err := os.Stat(databasePath)
	if os.IsNotExist(err) {
		return document, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return document, false, nil
	}
	db, err := sql.Open("sqlite", readOnlyDataSourceName(databasePath))
	if err != nil {
		return statusDocument{}, false, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { _ = db.Close() }()
	var present int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = 'upgrade_operations'`).Scan(&present); err != nil {
		return statusDocument{}, false, err
	}
	if present == 0 {
		return document, false, nil
	}
	var row statusRow
	err = db.QueryRowContext(ctx, `select stage, domains_done, source_kind, source_version,
		reconciliation, manual_items, error_code, error_detail, created_at_ms, updated_at_ms
		from upgrade_operations where id = 1`).Scan(&row.Stage, &row.Domains, &row.SourceKind, &row.SourceVersion,
		&row.Reconciled, &row.ManualItems, &row.ErrorCode, &row.ErrorDetail, &row.CreatedAtMS, &row.UpdatedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return document, false, nil
	}
	if err != nil {
		return statusDocument{}, false, err
	}
	if err := validateStatusRow(row); err != nil {
		return statusDocument{}, false, err
	}
	domains, err := decodeStringColumn(row.Domains, "domains_done")
	if err != nil {
		return statusDocument{}, false, err
	}
	manualItems, err := decodeStringColumn(row.ManualItems, "manual_items")
	if err != nil {
		return statusDocument{}, false, err
	}
	reconciliation, err := reconciliationSummary(row.Reconciled)
	if err != nil {
		return statusDocument{}, false, err
	}
	document.Recorded = true
	document.Evidence = EvidenceOperationRow
	document.Stage = row.Stage
	document.StageKnown = knownStage(row.Stage)
	document.DomainsDone = domains
	// Manual items are operator instructions written by another step, so they
	// pass the same content scrubber as the error detail: a copy-pasted command
	// can carry a credential, and a path in an instruction is still a path.
	document.ManualItems = scrubList(manualItems)
	// The next decision reads the recorded domains only: `cpa_config` is the
	// import's own claim that the destination holds the copied state, and it is
	// recorded in the same statement as the manual items the operator still owns.
	document.NextAction = nextActionForRecordedStage(row.Stage, row.ErrorCode.String, document.DomainsDone)
	document.SourceKind = row.SourceKind.String
	document.SourceVersion = row.SourceVersion.String
	document.ErrorCode = row.ErrorCode.String
	document.ErrorDetail = scrubError(row.ErrorDetail.String)
	document.UpdatedAtMS = row.UpdatedAtMS
	document.Reconciliation = reconciliation
	return document, true, nil
}

// readCopyInMarker reads the provenance marker that describes this database.
// The marker sits in this data root's own reserved work directory, so it
// describes this root even when the root is mounted at another absolute path;
// a marker naming a different database file blocks status without opening the
// store, matching startup admission and preserving all recovery evidence.
func readCopyInMarker(databasePath string) (copyinmarker.Marker, bool, error) {
	marker, found, err := copyinmarker.Read(filepath.Dir(databasePath))
	if err != nil {
		return copyinmarker.Marker{}, false, fmt.Errorf("read the copy-in provenance marker: %w", err)
	}
	if !found {
		return copyinmarker.Marker{}, false, nil
	}
	if !copyinmarker.SameTarget(marker.TargetDatabase, databasePath) {
		return copyinmarker.Marker{}, false, errors.New("the copy-in marker names a different database; preserve the target and marker and recover with `cpa-manager-plus upgrade run` before reading this store")
	}
	return marker, true, nil
}

// withCopyInMarker answers the window the row cannot. The copy-in publishes its
// provenance before the migration commits and removes it only after the row
// commits, so a marker that names this database with no row means the operation
// is in progress at copy_in — the same reading the startup admission makes. The
// stage is reported with the marker as its evidence, and the next action is the
// copy-in the operator has to resume; "preflight" would claim nothing had
// started while admission refuses every normal surface.
func withCopyInMarker(document statusDocument, marker copyinmarker.Marker) statusDocument {
	document.Recorded = true
	document.Evidence = EvidenceCopyInMarker
	document.Stage = StageCopyIn
	document.StageKnown = true
	// The marker is written after the target was materialized, which is exactly
	// the domain the recorded copy_in row names.
	document.DomainsDone = []string{domainSourceCopy}
	document.NextAction = nextAction(StageCopyIn, document.DomainsDone)
	document.SourceKind = marker.SourceKind
	document.SourceVersion = marker.SourceVersion
	document.UpdatedAtMS = marker.WrittenAtMS
	return document
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

// nextAction maps the recorded stage and domains onto the wizard's next
// decision.
//
// A stage is a checkpoint, not a pending step: recording `reconcile` means the
// comparison finished, so the next decision is the import that follows it.
// Recording `cpa_import` with the `cpa_config` domain means the destination holds
// the copied state, so the next decision is verification — repeating the import
// would ask the operator to redo work the row already proves. Without that domain
// the import has not settled its copy and is offered again.
//
// Recording `verify` means the gate ran and completed the operation. A failed
// operation resumes at reconciliation, and a stage this build does not know keeps
// the empty string: the caller must show the recorded stage verbatim rather than
// claim a step that may not be the real position.
func nextAction(stage string, domains []string) string {
	switch stage {
	case "":
		return ActionPreflight
	case StagePreflight:
		return ActionPreflight
	case StageCopyIn:
		return ActionCopyIn
	case StageReconcile:
		return ActionCPAImport
	case StageCPAImport:
		// A completed import records `cpa_config`; without it the step has not
		// settled its copy and the import is offered again.
		if slices.Contains(domains, domainCPAConfig) {
			return ActionVerify
		}
		return ActionCPAImport
	case StageExternal:
		// The external connection is recorded once the source CPA answers; what
		// remains is the verification of the migrated surface.
		return ActionVerify
	case StageVerify:
		// Verification passed, so the only remaining step is completion.
		return ActionComplete
	case StageFailed:
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
// a fixed allow-list: only the days it names, deduplicated and chronological.
// A producer's free-form payload never reaches the browser wholesale.
//
// The list is bounded by maxMismatchDays, and the bound is stated: the newest
// window is kept, the total is reported, and a cut list is flagged. Silently
// dropping the tail — and, because the list was ascending, the newest days —
// presented an incomplete history as the whole one.
func reconciliationSummary(raw string) (reconciliationDocument, error) {
	empty := emptyReconciliation()
	if strings.TrimSpace(raw) == "" {
		return empty, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return empty, fmt.Errorf("%w: reconciliation is not a JSON object", errStatusRowInvalid)
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
			// A named field this projection cannot read is a corrupt row, not an
			// empty reconciliation.
			return empty, fmt.Errorf("%w: reconciliation.%s is not a string array", errStatusRowInvalid, key)
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
	total := len(days)
	truncated := false
	if len(days) > maxMismatchDays {
		// The newest window, not the oldest: the days an operator can still act
		// on are the recent ones.
		days = days[len(days)-maxMismatchDays:]
		truncated = true
	}
	return reconciliationDocument{
		MismatchDays:          days,
		MismatchDaysTotal:     total,
		MismatchDaysTruncated: truncated,
	}, nil
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

// decodeStringColumn reads one JSON string-array column. Unlike the lenient
// reader it replaces, a value that is not a JSON string array is a corrupt row:
// returning an empty list would present a damaged record as an empty one.
func decodeStringColumn(raw string, column string) ([]string, error) {
	values := []string{}
	if strings.TrimSpace(raw) == "" {
		return values, nil
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("%w: %s is not a JSON string array", errStatusRowInvalid, column)
	}
	if values == nil {
		return []string{}, nil
	}
	return values, nil
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
//
// Credential material is recognised by the project's one content scrubber
// (usage.SanitizeCredentialText), not by a second vocabulary that could disagree
// with the usage pipeline about what a key looks like. What this package adds on
// top is its own policy — source paths, which the usage pipeline does not have —
// and the cross-line handling below.
const (
	// redactedText is the marker the shared scrubber writes; the cross-line
	// continuation rule reads it back.
	redactedText   = "[redacted]"
	sourceText     = "<source>"
	maxDetailLine  = 600
	maxDetailLines = 40
	// minProductKeyBody is the shortest key body this product issues after its
	// prefix, and the shortest run this package will treat as the tail of a key
	// that a line break cut in two.
	minProductKeyBody = 4
)

var (
	// productKeyPrefixes are this product's own key prefixes. The shared scrubber
	// recognises the whole key; this list is what a key wrapped across a line
	// boundary is recognised by, because the tail carries no prefix of its own.
	productKeyPrefixes = []string{"cpamp_", "cmp_admin_"}
	// issuedKeyPattern and productKeyPattern are this package's stricter reading
	// of the two key families an error is most likely to quote. The shared
	// scrubber deliberately requires a long body before it calls something a key;
	// an error shown to the administrator is read by a human who can act on a
	// short fragment, so this surface redacts from the prefix. It is a tighter
	// threshold on the same vocabulary, not a second one.
	issuedKeyPattern  = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{4,}`)
	productKeyPattern = regexp.MustCompile(`(?i)\b(?:` + strings.Join(productKeyPrefixes, "|") + `)[A-Za-z0-9_.\-]{` + strconv.Itoa(minProductKeyBody) + `,}`)
	// urlCredentialPattern is a URL whose authority embeds credentials. The
	// shared scrubber covers the header and assignment shapes; a userinfo
	// password in a URL is a different shape and is redacted here.
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
	// A credential wrapped across two lines is still one credential. A label left
	// on its own — `Bearer`, `Basic`, `password=`, `"password":` — is joined with
	// the line that carries its value before the patterns see it. Nothing else is
	// joined: a line break is evidence.
	lines = joinSplitLabels(lines)
	scrubbed := make([]string, 0, len(lines))
	// Two shapes can be cut by a line break with no label on the tail: a product
	// key broken mid-token, and a token whose whole tail is the line after a
	// redaction that consumed the previous line's end. Both tails are removed
	// where they start. The line break itself stays: it is still evidence of
	// shape.
	openKey := false
	for _, line := range lines {
		line = strings.ReplaceAll(line, "\x00", "")
		if openKey {
			line = redactSecretTail(line, minProductKeyBody)
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

// scrubCredentials redacts the credential shapes an error can quote through the
// project's shared content scrubber, then applies this package's path policy.
func scrubCredentials(line string) string {
	line = urlCredentialPattern.ReplaceAllString(line, "$1"+redactedText+"@")
	line = usage.SanitizeCredentialText(line)
	line = productKeyPattern.ReplaceAllString(line, redactedText)
	line = issuedKeyPattern.ReplaceAllString(line, redactedText)
	line = windowsPathPattern.ReplaceAllString(line, "$1"+sourceText)
	line = localPathPattern.ReplaceAllString(line, "$1"+sourceText)
	return line
}

// joinSplitLabels joins a line that ends with a credential label with the line
// that carries its value, so a value wrapped onto the next line is redacted
// instead of surviving as an unlabelled secret. The scheme words, a bare
// `Authorization:`, and the assignment forms an error message quotes are the
// labels: a line break after one of them is a wrap, not a delimiter.
func joinSplitLabels(lines []string) []string {
	joined := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(joined) == 0 {
			joined = append(joined, line)
			continue
		}
		last := joined[len(joined)-1]
		switch {
		case endsInsideToken(last) && isSecretFragment(strings.TrimSpace(line)):
			// A line break inside a token is not a delimiter: the two halves are
			// one credential, so they are rejoined with nothing between them. Only
			// a line that is nothing but a token fragment continues the token; any
			// other line is the next sentence, and a line break there is evidence.
			joined[len(joined)-1] = last + strings.TrimSpace(line)
		case endsWithCredentialLabel(last):
			joined[len(joined)-1] = last + " " + strings.TrimSpace(line)
		default:
			joined = append(joined, line)
		}
	}
	return joined
}

// endsInsideToken reports whether a line ends in the middle of a labelled
// credential token: a scheme word and a run of token characters that reaches the
// end of the line. It is what distinguishes a wrapped token from a complete one.
func endsInsideToken(line string) bool {
	trimmed := strings.TrimRight(line, " \t")
	lower := strings.ToLower(trimmed)
	for _, label := range []string{"bearer ", "basic "} {
		at := strings.LastIndex(lower, label)
		if at < 0 {
			continue
		}
		token := trimmed[at+len(label):]
		if len(token) >= minProductKeyBody && isSecretFragment(token) && endsOnTokenSeparator(token) {
			return true
		}
	}
	return false
}

// endsOnTokenSeparator reports whether a token run ends on one of the characters
// a key body uses only between parts. A token that stops there was almost
// certainly cut by the line break; one that stops on an ordinary character is
// read as complete, because the next line is a sentence far more often than it is
// the rest of a key. The ambiguity is inherent to a line break with no marker —
// this is the reading that keeps both real credentials and real messages.
func endsOnTokenSeparator(token string) bool {
	if token == "" {
		return false
	}
	switch token[len(token)-1] {
	case '-', '_', '.', '+', '/', '=':
		return true
	default:
		return false
	}
}

// isSecretFragment reports whether a value is nothing but token characters.
func isSecretFragment(value string) bool {
	for _, r := range value {
		if !isSecretRune(r) {
			return false
		}
	}
	return value != ""
}

// endsWithCredentialLabel reports whether a line ends with a label whose value
// is not on it.
func endsWithCredentialLabel(line string) bool {
	trimmed := strings.ToLower(strings.TrimRight(line, " \t"))
	for _, label := range []string{
		"bearer", "basic", "authorization:", "authorization: basic", "authorization: bearer",
		`"authorization":`, "x-api-key:", "x_api_key:", "api-key:", "api_key:",
		"password=", "password:", `password":`,
	} {
		if trimmed == label || strings.HasSuffix(trimmed, " "+label) {
			return true
		}
	}
	return false
}

// redactSecretTail removes the leading credential run of a line whose
// predecessor ended inside a token: that run is the rest of the token, so it is
// credential material even though it carries no prefix of its own. A run shorter
// than the shortest key body is left alone — it is not a fragment this product
// issues, and redacting every short word would erase the rest of the error.
func redactSecretTail(line string, minRun int) string {
	trimmed := strings.TrimLeft(line, " \t")
	run := 0
	for _, r := range trimmed {
		if !isSecretRune(r) {
			break
		}
		run += len(string(r))
	}
	if run < minRun {
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

// isSecretRune reports whether r can appear in a credential token: the product
// key alphabet plus the base64 alphabet of a Basic value.
func isSecretRune(r rune) bool {
	return isKeyRune(r) || r == '+' || r == '/' || r == '='
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
