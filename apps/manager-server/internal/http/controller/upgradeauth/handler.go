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
	"path/filepath"
	"strings"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradesurface"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

const Path = "/.well-known/cpamp-bootstrap/upgrade/authenticate"
const Contract = "cpamp.upgrade-auth.v1"

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
	instance             [32]byte
	mu                   sync.Mutex
	observed             sqlite.LegacyInspection
	encoded              string
	credentialRevision   string
	publicOrigin         string
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

// verifyCredential proves the key against the one credential file this surface
// resolved, reading it the way that file requires.
func (h *Handler) verifyCredential(ctx context.Context, key string) error {
	if h.mountSource {
		return upgradeservice.VerifyAdminSource(ctx, h.paths.Database, key)
	}
	// The Manager-store fallback still snapshots a materializing copy-in target,
	// so both trees that copy must stay out of travel with the read: the v2 data
	// root the store lives in, and the configured 1.x source root resolved with
	// this leaf. The root is known by construction here — it is resolved once at
	// startup, and an absent mount resolves to no root at all — so an operator's
	// TMPDIR inside it is refused by the service instead of staging a copy there.
	return upgradeservice.VerifyManagerCredential(ctx, h.paths.Database, upgradeservice.CredentialSnapshotRoots{
		Target:         filepath.Dir(h.paths.Database),
		Source:         h.sourceRoot,
		SourceKnown:    true,
		PreserveTarget: h.preserveManagerStore,
	}, key)
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
		write(w, http.StatusForbidden, upgradesurface.CodeOriginRejected)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), legacypreflight.DiscoveryInspectionTimeout)
	defer cancel()
	legacypreflight.WithAuthenticatedInspection(ctx, h.paths, key, func(report legacypreflight.Report, err error) {
		// observedFile is the projection the entry can offer this caller. When the
		// public observation refuses the source, the credential is asked directly:
		// a proved key still answers authenticated, because only the observation is
		// unavailable and a refused observation never becomes a credential verdict.
		observedFile := true
		switch {
		case err == nil:
		case errors.Is(err, sqlite.ErrLegacyAdminUnauthorized):
			observedFile = false
			verifyErr := h.verifyCredential(ctx, key)
			if errors.Is(verifyErr, upgradeservice.ErrAdminUnauthorized) {
				write(w, http.StatusUnauthorized, "admin_unauthorized")
				return
			}
			if verifyErr != nil {
				write(w, http.StatusServiceUnavailable, "source_unavailable")
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
				write(w, http.StatusServiceUnavailable, "source_unavailable")
				return
			}
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
