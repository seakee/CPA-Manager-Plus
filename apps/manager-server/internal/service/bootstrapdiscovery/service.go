// Package bootstrapdiscovery projects safe, no-migrate inspection evidence
// into the deliberately small unauthenticated discovery contract. It has no
// writable store dependency and cannot grant native startup admission.
package bootstrapdiscovery

import (
	"encoding/json"
	"net/http"
	"strings"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	bootstrapcontract "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/contract/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

const (
	// Path aliases the single domain-owned fixed discovery path. The HTTP
	// routing package and the checked-in route ledger use the same value.
	Path = bootstrap.FixedBootstrapDiscoveryPath
	// HealthPath preserves the existing readiness consumer contract. It must
	// not succeed until the full Manager graph is ready.
	HealthPath = bootstrap.FixedLivenessPath
)

// Input keeps transport projection explicit. InspectionError is intentionally
// retained: an unstable/unsafe observation cannot be converted into Recovery.
type Input struct {
	Report          legacypreflight.Report
	InspectionError error
	// Supplied only by the already-held Manager store owner. Unauthenticated
	// requests and schema inspection never manufacture this evidence.
	FreshOperation *bootstrap.FreshOperation
}

func Unavailable() bootstrap.PublicDiscovery {
	return bootstrap.PublicDiscovery{
		ContractVersion: bootstrap.PublicDiscoveryContractVersion,
		Status:          bootstrap.PublicUnavailable,
		AuthEntry:       bootstrap.AuthEntryUnavailable,
		Reason:          bootstrap.ReasonObservationUnavailable,
	}
}

func Project(input Input) bootstrap.PublicDiscovery {
	if input.InspectionError != nil {
		return Unavailable()
	}
	if input.FreshOperation != nil {
		op := *input.FreshOperation
		if op.Validate() != nil || op.ProductComplete || op.Routing.Committed || op.AdminInitialized {
			return Unavailable()
		}
		// Only the canonical store consumer can supply this positively owned
		// checkpoint. The ordinary native-v2 branch below is unchanged.
		return publicDiscovery(bootstrap.PublicFreshClaimRequired, bootstrap.Fresh, true, bootstrap.AuthEntryFreshClaim, bootstrap.ReasonFreshClaimRequired)
	}
	if !safeObservation(input.Report) {
		return Unavailable()
	}

	admin := bootstrap.AdminCredentialUninitialized
	switch input.Report.Observed.BootstrapState.AdminCredential {
	case "initialized":
		admin = bootstrap.AdminCredentialInitialized
	case "unverifiable":
		admin = bootstrap.AdminCredentialUnverifiable
	}
	sourceKind := strings.TrimSpace(input.Report.SourceKind)
	if sourceKind == "native-v2" {
		// A same-schema source still lacks trusted exact-release authority. It
		// may be writable under the existing native gate, but that gate is not
		// a Bootstrap discovery qualification grant and cannot imply completed.
		if admin != bootstrap.AdminCredentialInitialized {
			return publicDiscovery(bootstrap.PublicRecoveryRequired, bootstrap.Recovery, false, bootstrap.AuthEntryHostIntervention, bootstrap.ReasonRecoveryHostIntervention)
		}
		return publicDiscovery(bootstrap.PublicUnsupportedUpgrade, bootstrap.UnsupportedUpgrade, true, bootstrap.AuthEntryAdminSession, bootstrap.ReasonQualificationUnavailable)
	}
	if sourceKind == "empty-database" || sourceKind == "partial-source" {
		return publicDiscovery(bootstrap.PublicRecoveryRequired, bootstrap.Recovery, false, bootstrap.AuthEntryHostIntervention, bootstrap.ReasonRecoveryHostIntervention)
	}

	evidence := bootstrap.ClassificationEvidence{
		HasV1ProductState: sourceKind == "legacy-manager",
		AdminCredential:   admin,
		Session:           bootstrap.SessionMissing,
		RequiredCheckpoints: bootstrap.RequiredMigrationCheckpoints{
			CacheAccounting: bootstrap.CheckpointPending,
			QuotaSnapshot:   bootstrap.CheckpointPending,
		},
	}
	mode, err := bootstrap.Classify(evidence)
	if err != nil {
		return Unavailable()
	}
	switch mode {
	case bootstrap.Fresh:
		return publicDiscovery(bootstrap.PublicFreshClaimRequired, mode, true, bootstrap.AuthEntryFreshClaim, bootstrap.ReasonFreshClaimRequired)
	case bootstrap.UnsupportedUpgrade:
		return publicDiscovery(bootstrap.PublicUnsupportedUpgrade, mode, true, bootstrap.AuthEntryAdminSession, bootstrap.ReasonQualificationUnavailable)
	case bootstrap.Recovery:
		return publicDiscovery(bootstrap.PublicRecoveryRequired, mode, false, bootstrap.AuthEntryHostIntervention, bootstrap.ReasonRecoveryHostIntervention)
	default:
		// Upgrade and Completed are intentionally not public discovery outputs
		// in Phase4-01. They require later source/routing authority.
		return Unavailable()
	}
}

func publicDiscovery(status bootstrap.PublicStatus, mode bootstrap.Mode, authenticationRequired bool, authEntry bootstrap.AuthEntry, reason bootstrap.PublicReason) bootstrap.PublicDiscovery {
	modeCopy := mode
	return bootstrap.PublicDiscovery{
		ContractVersion:        bootstrap.PublicDiscoveryContractVersion,
		Status:                 status,
		Mode:                   &modeCopy,
		AuthenticationRequired: authenticationRequired,
		AuthEntry:              authEntry,
		Reason:                 reason,
	}
}

func safeObservation(report legacypreflight.Report) bool {
	if report.SourceKind == "fresh" {
		return len(report.Observed.Files) > 0 && report.Observed.Files[0].Role == "database" &&
			report.Observed.Files[0].State == "missing" && len(report.Observed.Issues) == 0 &&
			report.Observed.SQLite == "not-inspected" && report.Observed.BootstrapState.Valid &&
			!report.Observed.BootstrapState.Present && !report.Observed.BootstrapState.AdminCredentialPresent &&
			report.Observed.BootstrapState.AdminCredential == "uninitialized"
	}
	if report.SourceKind != "legacy-manager" && report.SourceKind != "native-v2" && report.SourceKind != "empty-database" && report.SourceKind != "partial-source" {
		return false
	}
	if len(report.Observed.Files) == 0 || report.Observed.Consistency == "changed" {
		return false
	}
	if report.SourceKind == "legacy-manager" || report.SourceKind == "native-v2" {
		if report.Observed.SQLite != "observed" || len(report.Observed.Issues) != 0 ||
			!stableConsistency(report.Observed.Consistency) || !report.Observed.BootstrapState.Valid {
			return false
		}
		if report.Observed.BootstrapState.AdminCredential == "initialized" && !report.Observed.BootstrapState.AdminCredentialPresent {
			return false
		}
		switch report.Observed.BootstrapState.AdminCredential {
		case "", "uninitialized", "initialized", "unverifiable":
			return true
		default:
			return false
		}
	}
	return report.Observed.SQLite == "not-inspected" && len(report.Observed.Issues) > 0
}

func stableConsistency(value string) bool {
	switch value {
	case "operator-asserted-cold", "manager-fenced-cold", "sqlite-read-snapshot":
		return true
	default:
		return false
	}
}

// Handler serializes the unified versioned discovery DTO. Fixed-path ownership,
// lifecycle and method capture belong to the routing package; this handler has
// no writable dependency and deliberately exposes no source details.
type Handler struct {
	Discovery bootstrap.PublicDiscovery
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.URL == nil || r.URL.Path != Path {
		http.NotFound(w, r)
		return
	}
	if h.Discovery.Mode != nil && *h.Discovery.Mode == bootstrap.Completed {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	dto, err := bootstrapcontract.DiscoveryFromDomain(h.Discovery)
	if err != nil {
		dto, _ = bootstrapcontract.DiscoveryFromDomain(Unavailable())
		writeJSON(w, http.StatusServiceUnavailable, dto)
		return
	}
	status := http.StatusOK
	if dto.Status == bootstrap.PublicUnavailable {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, dto)
}

// NotReadyHandler keeps installer and Docker readiness probes unsuccessful
// while only read-only discovery is available.
type NotReadyHandler struct{}

func (NotReadyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.URL == nil || r.URL.Path != HealthPath {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
