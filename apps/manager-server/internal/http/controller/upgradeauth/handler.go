// Package upgradeauth exposes only current-source administrator authentication.
// It has no writable store, CPA client, migration or collector dependency.
package upgradeauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradesurface"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

const Path = "/.well-known/cpamp-bootstrap/upgrade/authenticate"
const Contract = "cpamp.upgrade-auth.v1"

// CodeSourceOwnershipUnavailable reports that the bound 1.x source (for example
// a root-owned 1.x volume mounted at /legacy:ro while Manager runs as
// 10002:10002) has been proven to be a 1.x installation by reading its SQLite
// database, while its lock marker or data.key denies read permission to the
// current user.
const CodeSourceOwnershipUnavailable = "source_ownership_unavailable"
type Handler struct {
	paths sqlite.InspectionPaths
	// mountSource reports whether paths.Database is the configured 1.x source
	// rather than the Manager's own store. The two files are read differently:
	// a source is preserved byte for byte and only ever read through a private
	// snapshot, while the Manager store is this process's live database.
	mountSource bool
	// sourceRoot is the configured 1.x data directory resolved when this leaf
	// was built. It is kept even when the mount holds no database the policy
	// accepts and the credential therefore falls back to the Manager store: the
	// fallback still stages a private snapshot for a materializing target, and
	// that copy must not land in the tree the upgrade preserves. It is resolved
	// once, here, exactly as the surface's credential rule resolved it; the
	// wizard reads resolve the mount per request.
	sourceRoot           string
	preserveManagerStore bool
	managerPaths         sqlite.InspectionPaths
	// InspectionTimeout bounds request waiting on both the ownership assessment
	// and the inspection behind it. Zero uses
	// legacypreflight.DiscoveryInspectionTimeout.
	InspectionTimeout time.Duration
	// probeOwnership, onOwnershipFlightJoined, and afterOwnershipProbe are
	// deterministic test seams (nil in production) exercising blocked filesystem
	// calls, simultaneous completion+cancellation, single-flight coalescing, and
	// mid-request source permission/identity changes without sleeps.
	probeOwnership          func(ctx context.Context, paths sqlite.InspectionPaths, checkDataKey bool) ownershipAssessment
	onOwnershipFlightJoined func(flightDone <-chan struct{})
	afterOwnershipProbe     func()
	ownershipMu             sync.Mutex
	ownershipFlight         *ownershipFlight
	instance                [32]byte
	mu                      sync.Mutex
	observed                sqlite.LegacyInspection
	encoded                 string
	credentialRevision      string
	publicOrigin            string
}

type ownershipStatus uint8

const (
	ownershipReadable ownershipStatus = iota
	ownershipDenied
	ownershipUnavailable
)

type memberSnapshot struct {
	present     bool
	device      uint64
	inode       uint64
	size        int64
	mode        os.FileMode
	modifiedNS  int64
	changeStamp string
}

type sourceSnapshot struct {
	database    memberSnapshot
	lock        memberSnapshot
	wal         memberSnapshot
	shm         memberSnapshot
	journal     memberSnapshot
	dataKey     memberSnapshot
	uncacheable bool
}

type ownershipAssessment struct {
	status   ownershipStatus
	baseline sourceSnapshot
}

type ownershipFlight struct {
	done   chan struct{}
	result ownershipAssessment
}
// StatusObservationUnavailable is the entry's answer when the presented key is
// proved but the public observation cannot describe the source — a WAL source
// refuses it. It says exactly that: the credential is verified and nothing else.
// It grants no write, no migration and no session, and the two read routes behind
// the entry name the source's own conditions.
const StatusObservationUnavailable = "observation_unavailable"

func New(paths sqlite.InspectionPaths, publicOrigin string, mountSource bool, sourceRoot string) (*Handler, error) {
	normalized, err := upgradesurface.NormalizeOrigin(publicOrigin)
	if err != nil {
		return nil, err
	}
	h := &Handler{paths: paths, publicOrigin: normalized, mountSource: mountSource, sourceRoot: strings.TrimSpace(sourceRoot)}
	if _, err := rand.Read(h.instance[:]); err != nil {
		return nil, err
	}
	return h, nil
}

// SetPreserveManagerStore marks the fallback Manager database path as an
// unadmitted, protected source so credential verification reads a private
// snapshot rather than opening the store in place.
func (h *Handler) SetPreserveManagerStore(preserve bool) {
	if h != nil {
		h.preserveManagerStore = preserve
	}
}

// SetManagerStorePaths records the Manager's own database and configured data
// key paths so a mounted 1.x credential snapshot also excludes the Manager's
// target and data-key directories.
func (h *Handler) SetManagerStorePaths(paths sqlite.InspectionPaths) {
	if h != nil {
		h.managerPaths = paths
	}
}

// verifyCredential proves the key against the one credential file this surface
// resolved, reading it the way that file requires.
func (h *Handler) verifyCredential(ctx context.Context, key string) error {
	if h.mountSource {
		return upgradeservice.VerifyAdminSource(ctx, upgradeservice.PreservedSource{
			Database: h.paths.Database,
			DataKey:  h.paths.DataKey,
		}, key,
			upgradeservice.ScratchRoot{Label: "the target root", Dir: parentDir(h.managerPaths.Database)},
			upgradeservice.ScratchRoot{Label: "the Manager data key directory", Dir: parentDir(h.managerPaths.DataKey)})
	}
	// The Manager-store fallback still snapshots a materializing copy-in target,
	// so both trees that copy must stay out of travel with the read: the v2 data
	// root the store lives in, and the configured 1.x source root resolved with
	// this leaf. The root is known by construction here — it is resolved once at
	// startup, and an absent mount resolves to no root at all — so an operator's
	// TMPDIR inside it is refused by the service instead of staging a copy there.
	// The store's data key travels with it for the same reason: a configuration may
	// keep that file outside the data root.
	return upgradeservice.VerifyManagerCredential(ctx, h.paths.Database, upgradeservice.CredentialSnapshotRoots{
		Target:         filepath.Dir(h.paths.Database),
		Source:         h.sourceRoot,
		SourceKnown:    true,
		PreserveTarget: h.preserveManagerStore,
		DataKey:        h.paths.DataKey,
	}, key)
}

func (h *Handler) inspectionTimeout() time.Duration {
	if h.InspectionTimeout > 0 {
		return h.InspectionTimeout
	}
	return legacypreflight.DiscoveryInspectionTimeout
}

// resolveSourceOwnership assesses only the construction-time bound source
// (h.paths) through a single-flight worker so HTTP callers honour ctx
// cancellation and deadline without running unpreemptible filesystem calls on
// the request goroutine. Note that kernel-level Lstat/Open syscalls on an
// unresponsive network mount cannot be preempted by Go: single-flight bounds
// blocked worker concurrency to at most one goroutine per Handler (and bounds
// HTTP latency via ctx.Done()), whereas an indefinitely hung mount keeps that
// one flight occupied until the filesystem answers or the container restarts.
func (h *Handler) resolveSourceOwnership(ctx context.Context) (ownershipAssessment, bool) {
	if ctx.Err() != nil {
		return ownershipAssessment{}, false
	}
	if !h.mountSource && !h.preserveManagerStore && h.probeOwnership == nil {
		return ownershipAssessment{status: ownershipReadable}, true
	}
	flight := h.joinOwnershipFlight()
	if h.onOwnershipFlightJoined != nil {
		h.onOwnershipFlightJoined(flight.done)
	}
	select {
	case <-flight.done:
		// Go's select chooses pseudorandomly when flight.done and ctx.Done() are
		// ready together; re-check ctx.Err() so any cancelled or timed-out request
		// deterministically answers generic source_unavailable.
		if ctx.Err() != nil {
			return ownershipAssessment{}, false
		}
		return flight.result, true
	case <-ctx.Done():
		return ownershipAssessment{}, false
	}
}

func (h *Handler) joinOwnershipFlight() *ownershipFlight {
	h.ownershipMu.Lock()
	defer h.ownershipMu.Unlock()
	if h.ownershipFlight != nil {
		return h.ownershipFlight
	}
	flight := &ownershipFlight{done: make(chan struct{})}
	h.ownershipFlight = flight
	paths := h.paths
	mountSource := h.mountSource
	preserveManagerStore := h.preserveManagerStore
	managerPaths := h.managerPaths
	sourceRoot := h.sourceRoot
	timeout := h.inspectionTimeout()
	customProbe := h.probeOwnership
	go func() {
		probeCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		var result ownershipAssessment
		if customProbe != nil {
			result = customProbe(probeCtx, paths, mountSource || preserveManagerStore)
		} else {
			result = assessSourceOwnership(probeCtx, paths, mountSource, preserveManagerStore, managerPaths, sourceRoot)
		}
		h.ownershipMu.Lock()
		flight.result = result
		if h.ownershipFlight == flight {
			h.ownershipFlight = nil
		}
		h.ownershipMu.Unlock()
		close(flight.done)
	}()
	return flight
}

func assessSourceOwnership(
	ctx context.Context,
	paths sqlite.InspectionPaths,
	mountSource bool,
	preserveManagerStore bool,
	managerPaths sqlite.InspectionPaths,
	sourceRoot string,
) ownershipAssessment {
	if !mountSource && !preserveManagerStore {
		return ownershipAssessment{status: ownershipReadable}
	}
	database := strings.TrimSpace(paths.Database)
	if database == "" {
		return ownershipAssessment{status: ownershipUnavailable}
	}

	var (
		snap          sourceSnapshot
		anyPermDenied bool
	)
	dbMember, dbUncacheable, _, dbOK := inspectReadableMember(database, true)
	if !dbOK {
		return ownershipAssessment{status: ownershipUnavailable}
	}
	snap.database = dbMember
	snap.uncacheable = snap.uncacheable || dbUncacheable

	type sidecarTarget struct {
		suffix  string
		journal bool
		dest    *memberSnapshot
	}
	sidecars := []sidecarTarget{
		{suffix: ".manager.lock", journal: false, dest: &snap.lock},
		{suffix: "-wal", journal: true, dest: &snap.wal},
		{suffix: "-shm", journal: true, dest: &snap.shm},
		{suffix: "-journal", journal: true, dest: &snap.journal},
	}
	for _, sc := range sidecars {
		candidate := database + sc.suffix
		member, uncacheable, perm, ok := inspectReadableMember(candidate, false)
		if !ok {
			if sc.journal || !perm {
				return ownershipAssessment{status: ownershipUnavailable}
			}
			anyPermDenied = true
			continue
		}
		if sc.suffix == "-journal" && member.present && member.size > 0 {
			return ownershipAssessment{status: ownershipUnavailable}
		}
		*sc.dest = member
		snap.uncacheable = snap.uncacheable || uncacheable
	}

	if dataKey := strings.TrimSpace(paths.DataKey); dataKey != "" {
		member, uncacheable, perm, ok := inspectReadableMember(dataKey, false)
		if !ok {
			if !perm {
				return ownershipAssessment{status: ownershipUnavailable}
			}
			anyPermDenied = true
		} else {
			snap.dataKey = member
			snap.uncacheable = snap.uncacheable || uncacheable
		}
	}

	if !anyPermDenied {
		return ownershipAssessment{status: ownershipReadable, baseline: snap}
	}

	preserved := upgradeservice.PreservedSource{
		Database: database,
		DataKey:  paths.DataKey,
	}
	var extraRoots []upgradeservice.ScratchRoot
	if mountSource {
		extraRoots = []upgradeservice.ScratchRoot{
			{Label: "the target root", Dir: parentDir(managerPaths.Database)},
			{Label: "the Manager data key directory", Dir: parentDir(managerPaths.DataKey)},
		}
	} else {
		extraRoots = []upgradeservice.ScratchRoot{
			{Label: "the configured source root", Dir: sourceRoot},
		}
	}
	if upgradeservice.ProvenUnfenceableLegacySource(ctx, preserved, extraRoots...) {
		return ownershipAssessment{status: ownershipDenied}
	}
	return ownershipAssessment{status: ownershipUnavailable}
}

// inspectReadableMember validates path as a non-symlink, single-link regular
// file, opens it read-only to confirm descriptor identity and read permission,
// and records its device/inode/size/mode/mtime/ChangeStamp snapshot.
func inspectReadableMember(path string, required bool) (member memberSnapshot, uncacheable bool, permDenied bool, ok bool) {
	info, err := processlock.InspectRegularSingleLink(path)
	if os.IsNotExist(err) {
		if required {
			return memberSnapshot{}, false, false, false
		}
		return memberSnapshot{present: false}, false, false, true
	}
	if err != nil {
		return memberSnapshot{}, false, errors.Is(err, os.ErrPermission), false
	}
	if required && info.Size() == 0 {
		return memberSnapshot{}, false, false, false
	}
	file, err := os.Open(path)
	if err != nil {
		return memberSnapshot{}, false, errors.Is(err, os.ErrPermission), false
	}
	opened, statErr := file.Stat()
	validateErr := processlock.OpenedDatabase(file, info)
	_ = file.Close()
	if statErr != nil || validateErr != nil {
		joined := errors.Join(statErr, validateErr)
		return memberSnapshot{}, false, errors.Is(joined, os.ErrPermission), false
	}
	id, err := copyinmarker.Identity(path)
	if err != nil {
		return memberSnapshot{}, false, errors.Is(err, os.ErrPermission), false
	}
	stamp, stampErr := copyinmarker.ChangeStamp(path)
	if stampErr != nil {
		if errors.Is(stampErr, copyinmarker.ErrChangeStampUnavailable) {
			return memberSnapshot{
				present:    true,
				device:     id.Device,
				inode:      id.Inode,
				size:       opened.Size(),
				mode:       opened.Mode(),
				modifiedNS: opened.ModTime().UnixNano(),
			}, true, false, true
		}
		return memberSnapshot{}, false, errors.Is(stampErr, os.ErrPermission), false
	}
	return memberSnapshot{
		present:     true,
		device:      id.Device,
		inode:       id.Inode,
		size:        opened.Size(),
		mode:        opened.Mode(),
		modifiedNS:  opened.ModTime().UnixNano(),
		changeStamp: stamp,
	}, false, false, true
}

func parentDir(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	return filepath.Dir(trimmed)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// One order, shared with the wizard routes: the credential's shape, then the
	// browser's origin, then the credential, then the path, then the verb. Only a
	// proved credential ever reveals which resource exists or which method it
	// takes, so an unknown route and a query string answer 401/403 before 404.
	authorization := r.Header.Values("Authorization")
	if len(authorization) != 1 || !strings.HasPrefix(authorization[0], "Bearer ") {
		write(w, http.StatusUnauthorized, "admin_unauthorized")
		return
	}
	key := strings.TrimPrefix(authorization[0], "Bearer ")
	if key == "" || len(key) > 4096 || strings.TrimSpace(key) != key {
		write(w, http.StatusUnauthorized, "admin_unauthorized")
		return
	}
	// Browser clients are same-origin only. Do not trust forwarded hosts or
	// cookies; CLI callers without Origin still need the original Manager key.
	// The wizard reads apply the same rule, through the same helper.
	if upgradesurface.CrossSite(r, h.publicOrigin) {
		writeOriginRejected(w, h.publicOrigin)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.inspectionTimeout())
	defer cancel()
	before, ok := h.resolveSourceOwnership(ctx)
	if !ok {
		write(w, http.StatusServiceUnavailable, "source_unavailable")
		return
	}
	switch before.status {
	case ownershipDenied:
		write(w, http.StatusServiceUnavailable, CodeSourceOwnershipUnavailable)
		return
	case ownershipUnavailable:
		write(w, http.StatusServiceUnavailable, "source_unavailable")
		return
	}
	if h.afterOwnershipProbe != nil {
		h.afterOwnershipProbe()
	}
	legacypreflight.WithAuthenticatedInspection(ctx, h.paths, key, func(report legacypreflight.Report, err error) {
		// observedFile is the projection the entry can offer this caller. When the
		// public observation refuses the source, the credential is asked directly:
		// a proved key still answers authenticated, because only the observation is
		// unavailable and a refused observation never becomes a credential verdict.
		observedFile := true
		unauthorized := false
		switch {
		case err == nil:
		case errors.Is(err, os.ErrPermission):
			h.writeSourceFault(ctx, w, err)
			return
		case errors.Is(err, sqlite.ErrLegacyAdminUnauthorized):
			observedFile = false
			verifyErr := h.verifyCredential(ctx, key)
			if errors.Is(verifyErr, upgradeservice.ErrAdminUnauthorized) {
				unauthorized = true
			} else if verifyErr != nil {
				h.writeSourceFault(ctx, w, verifyErr)
				return
			}
		default:
			// The observation failed for its own reason (a live source it cannot
			// prove quiet, an unreadable inventory). The key may still be the
			// source's credential, and closing the read-only operation view on an
			// observation fault is what stranded a resumable operation. This is not
			// a credential verdict, so a wrong key stays 503 here rather than 401.
			observedFile = false
			if verifyErr := h.verifyCredential(ctx, key); verifyErr != nil {
				h.writeSourceFault(ctx, w, verifyErr)
				return
			}
		}
		// Confirm the preserved source's member identities, ctime stamps, and read
		// permissions are unchanged before emitting any 401/200 verdict so a
		// mid-request chmod or file replacement on data.key or sidecars fails
		// closed instead of returning a credential comparison result.
		if h.mountSource || h.preserveManagerStore || h.probeOwnership != nil {
			after, afterOK := h.resolveSourceOwnership(ctx)
			if !afterOK {
				write(w, http.StatusServiceUnavailable, "source_unavailable")
				return
			}
			if after.status == ownershipDenied {
				write(w, http.StatusServiceUnavailable, CodeSourceOwnershipUnavailable)
				return
			}
			if after.status != ownershipReadable || before.baseline.uncacheable || after.baseline.uncacheable || after.baseline != before.baseline {
				write(w, http.StatusServiceUnavailable, "source_unavailable")
				return
			}
		}
		if unauthorized {
			write(w, http.StatusUnauthorized, "admin_unauthorized")
			return
		}
		// Only a proved credential learns whether this resource exists, whether it
		// carries a query string, and which verb it takes.
		if r.URL.Path != Path || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			write(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if !observedFile {
			h.answerUnobserved(w, r)
			return
		}
		projection := bootstrapdiscovery.Project(bootstrapdiscovery.Input{Report: report, InspectionError: err})
		if string(projection.Status) != "unsupported_upgrade" || report.Observed.FreshOwnershipCheckpointPresent {
			write(w, http.StatusServiceUnavailable, "source_unavailable")
			return
		}
		// Opaque change detector, never a session token or migration permission.
		// JSON omits file identity/mtime: compare the actual observation as well.
		encoded, err := json.Marshal(report.Observed)
		if err != nil {
			write(w, http.StatusServiceUnavailable, "source_unavailable")
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.observed.Files) != 0 && (!sqlite.SameInspectionSource(h.observed, report.Observed) || h.encoded != string(encoded) || h.credentialRevision != report.AdminCredentialRevision) {
			if _, err := rand.Read(h.instance[:]); err != nil {
				write(w, http.StatusServiceUnavailable, "source_unavailable")
				return
			}
		}
		h.observed, h.encoded = report.Observed, string(encoded)
		h.credentialRevision = report.AdminCredentialRevision
		revision := hex.EncodeToString(h.instance[:])
		if expected := r.Header.Get("X-CPAMP-Upgrade-Source"); expected != "" && expected != revision {
			write(w, http.StatusConflict, "source_changed")
			return
		}
		writeEntry(w, revision, string(projection.Status))
	})
}

func (h *Handler) writeSourceFault(ctx context.Context, w http.ResponseWriter, faultErr error) {
	if ctx.Err() != nil {
		write(w, http.StatusServiceUnavailable, "source_unavailable")
		return
	}
	if (h.mountSource || h.preserveManagerStore || h.probeOwnership != nil) && errors.Is(faultErr, os.ErrPermission) {
		if after, ok := h.resolveSourceOwnership(ctx); ok && after.status == ownershipDenied {
			write(w, http.StatusServiceUnavailable, CodeSourceOwnershipUnavailable)
			return
		}
	}
	write(w, http.StatusServiceUnavailable, "source_unavailable")
}

// answerUnobserved completes the entry for a proved credential whose source the
// public observation cannot describe. It claims nothing about the operation: no
// stage, no readiness and no migration permission — only the verified identity of
// the caller, which the two read routes prove again on their own.
func (h *Handler) answerUnobserved(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	revision := hex.EncodeToString(h.instance[:])
	if expected := r.Header.Get("X-CPAMP-Upgrade-Source"); expected != "" && expected != revision {
		write(w, http.StatusConflict, "source_changed")
		return
	}
	writeEntry(w, revision, StatusObservationUnavailable)
}

func writeEntry(w http.ResponseWriter, revision, status string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"contract_version": Contract, "authenticated": true,
		"source_revision": revision, "status": status,
		"migration_available": false, "expires_in_seconds": 60,
	})
}

func write(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"contract_version": Contract, "error": map[string]string{"code": code}})
}

func writeOriginRejected(w http.ResponseWriter, expectedOrigin string) {
	errBody := map[string]string{"code": upgradesurface.CodeOriginRejected}
	if expectedOrigin != "" {
		errBody["expected_origin"] = expectedOrigin
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]any{"contract_version": Contract, "error": errBody})
}
