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
	"net/url"
	"strings"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
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
	publicOrigin = strings.TrimRight(strings.TrimSpace(publicOrigin), "/")
	if publicOrigin != "" {
		origin, err := url.Parse(publicOrigin)
		if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
			return nil, errors.New("invalid Upgrade public origin")
		}
	}
	h := &Handler{paths: paths, publicOrigin: publicOrigin}
	_, err := rand.Read(h.instance[:])
	return h, err
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path != Path || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		write(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	// Browser clients are same-origin only. Do not trust forwarded hosts or
	// cookies; CLI callers without Origin still need the original Manager key.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	expectedOrigin := h.publicOrigin
	if expectedOrigin == "" {
		expectedOrigin = scheme + "://" + r.Host
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != expectedOrigin {
		write(w, http.StatusForbidden, "origin_rejected")
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		write(w, http.StatusForbidden, "origin_rejected")
		return
	}
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
	ctx, cancel := context.WithTimeout(r.Context(), legacypreflight.DiscoveryInspectionTimeout)
	defer cancel()
	legacypreflight.WithAuthenticatedInspection(ctx, h.paths, key, func(report legacypreflight.Report, err error) {
		if errors.Is(err, sqlite.ErrLegacyAdminUnauthorized) {
			write(w, http.StatusUnauthorized, "admin_unauthorized")
			return
		}
		projection := bootstrapdiscovery.Project(bootstrapdiscovery.Input{Report: report, InspectionError: err})
		if err != nil || string(projection.Status) != "unsupported_upgrade" || report.Observed.FreshOwnershipCheckpointPresent {
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
