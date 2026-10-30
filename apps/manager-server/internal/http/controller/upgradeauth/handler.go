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
	paths              sqlite.InspectionPaths
	instance           [32]byte
	mu                 sync.Mutex
	observed           sqlite.LegacyInspection
	encoded            string
	credentialRevision string
	publicOrigin       string
}

func New(paths sqlite.InspectionPaths, publicOrigin string) (*Handler, error) {
	normalized, err := upgradesurface.NormalizeOrigin(publicOrigin)
	if err != nil {
		return nil, err
	}
	h := &Handler{paths: paths, publicOrigin: normalized}
	if _, err := rand.Read(h.instance[:]); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// One order, shared with the wizard routes: a credential is required first,
	// then the path, then the verb, and only a proved credential ever reveals
	// which of the two exists. Nothing about this resource is answered to a
	// caller without one.
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
	if r.URL.Path != Path || r.URL.RawQuery != "" {
		http.NotFound(w, r)
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
		if errors.Is(err, sqlite.ErrLegacyAdminUnauthorized) {
			// The public observation can refuse a source before it ever reads the
			// credential (a WAL source, a changed inventory, an ownership marker it
			// cannot prove). That is a source condition, not a verdict on the key,
			// so ask the surface's one credential read — the same function the
			// wizard reads use, on the same file — before answering 401. A proved
			// key on a source that cannot be publicly observed still answers 503:
			// only the observation is unavailable, and it never becomes a 401.
			if verifyErr := upgradeservice.VerifyAdminSource(ctx, h.paths.Database, key); errors.Is(verifyErr, upgradeservice.ErrAdminUnauthorized) {
				write(w, http.StatusUnauthorized, "admin_unauthorized")
			} else {
				write(w, http.StatusServiceUnavailable, "source_unavailable")
			}
			return
		}
		if err != nil {
			write(w, http.StatusServiceUnavailable, "source_unavailable")
			return
		}
		// The credential is proved before the verb is answered, exactly as on the
		// wizard routes: a caller without one learns nothing about this resource,
		// including which method it takes.
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			write(w, http.StatusMethodNotAllowed, "method_not_allowed")
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"contract_version": Contract, "authenticated": true,
			"source_revision": revision, "status": projection.Status,
			"migration_available": false, "expires_in_seconds": 60,
		})
	})
}

func write(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"contract_version": Contract, "error": map[string]string{"code": code}})
}
