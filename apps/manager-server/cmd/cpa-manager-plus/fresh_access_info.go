package main

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
)

type keyAccessInfo struct {
	Mode               string                   `json:"mode"`
	ManagementBasePath string                   `json:"managementBasePath"`
	InferenceBasePath  string                   `json:"inferenceBasePath"`
	ExternalBaseURL    string                   `json:"externalBaseUrl,omitempty"`
	Protocols          []routing.AccessProtocol `json:"protocols"`
	Ready              bool                     `json:"ready"`
	Code               string                   `json:"code,omitempty"`
}

// makeKeyAccessInfo consumes verified installation facts. Inference readiness
// and the chosen path are explicit inputs; an old installation without the
// field must never acquire a public-root fallback by formatting an empty path.
func makeKeyAccessInfo(mode bootstrap.CPAStrategy, control, inference string, ready bool, external string) keyAccessInfo {
	result := keyAccessInfo{Mode: string(mode), ManagementBasePath: control, InferenceBasePath: inference, Protocols: []routing.AccessProtocol{}}
	if !routing.ValidControlBasePath(control) {
		result.Code = "routing_unavailable"
		return result
	}
	if mode == bootstrap.ConnectExternal {
		u, err := url.Parse(external)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			result.Code = "external_target_unavailable"
			return result
		}
		// The stored connection is the external CPA's actual base, including
		// any existing operator-controlled path. CPAMP does not rewrite it.
		base := strings.TrimRight(u.String(), "/")
		result.ExternalBaseURL = base
		result.InferenceBasePath = ""
		result.Protocols = routing.InferenceProtocols(base)
		result.Ready = ready
		if !ready {
			result.Code = "installation_incomplete"
		}
		return result
	}
	if !routing.ValidControlBasePath(inference) {
		result.Code = "inference_prefix_required"
		return result
	}
	result.Protocols = routing.InferenceProtocols(inference)
	result.Ready = ready
	if !ready {
		result.Code = "installation_incomplete"
	}
	return result
}

func writeKeyAccessInfo(w http.ResponseWriter, value keyAccessInfo) {
	w.Header().Set("Cache-Control", "no-store")
	writeFreshJSON(w, value)
}

// clientAccessInfo is called only after canonicalAPI has validated either the
// Admin or the original installation's still-live continuation credential.
// It reads address facts only; no CPA request or management credential leaves
// this process and no browser-supplied origin becomes routing authority.
func (a *freshApplication) clientAccessInfo(w http.ResponseWriter, r *http.Request, record freshstore.Record) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	op := record.Operation
	snapshot := freshrouting.SnapshotForRecord(record)
	control, verified := snapshot.EffectiveControlBasePath()
	if !verified || control != op.Routing.Target || snapshot.Revision != op.Routing.Revision {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeKeyAccessInfo(w, keyAccessInfo{Protocols: []routing.AccessProtocol{}, Code: "routing_unavailable"})
		return
	}
	inference, _ := snapshot.EffectiveInferenceBasePath()
	if inference != op.Routing.InferenceTarget {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeKeyAccessInfo(w, keyAccessInfo{Protocols: []routing.AccessProtocol{}, Code: "routing_unavailable"})
		return
	}
	ready := op.ProductComplete && op.Routing.Committed && op.Strategy.Stage == "committed" &&
		(op.Strategy.Strategy == bootstrap.ConnectExternal || op.Routing.InferenceCommitted)
	external := ""
	if op.Strategy.Strategy == bootstrap.ConnectExternal {
		if a.externalSource == nil {
			writeKeyAccessInfo(w, keyAccessInfo{Mode: string(bootstrap.ConnectExternal), ManagementBasePath: control, Protocols: []routing.AccessProtocol{}, Code: "external_target_unavailable"})
			return
		}
		install, err := a.externalSource(withFreshRecordSnapshot(r.Context(), record))
		if err != nil || install.Identity != op.Strategy.TargetIdentity {
			writeKeyAccessInfo(w, keyAccessInfo{Mode: string(bootstrap.ConnectExternal), ManagementBasePath: control, Protocols: []routing.AccessProtocol{}, Code: "external_target_unavailable"})
			return
		}
		external = install.BaseURL
	}
	writeKeyAccessInfo(w, makeKeyAccessInfo(op.Strategy.Strategy, control, inference, ready, external))
}
