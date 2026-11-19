package upgradewizard

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

// RoutingSuffix persists Upgrade access prefixes onto the single
// upgrade_operations row before completion.
const RoutingSuffix = "/installation/upgrade/routing"

type upgradeRoutingRequest struct {
	ControlBasePath   string `json:"controlBasePath"`
	InferenceBasePath string `json:"inferenceBasePath,omitempty"`
	SharedEntry       bool   `json:"sharedEntry,omitempty"`
}

type upgradeRoutingDocument struct {
	Configured                 bool   `json:"configured"`
	Committed                  bool   `json:"committed"`
	ExistingPrefixesPreserved  bool   `json:"existingPrefixesPreserved"`
	TargetMode                 string `json:"targetMode,omitempty"`
	ControlBasePath            string `json:"controlBasePath"`
	InferenceBasePath          string `json:"inferenceBasePath"`
	SharedEntry                bool   `json:"sharedEntry"`
	ExternalBaseURL            string `json:"externalBaseUrl"`
	SuggestedControlBasePath   string `json:"suggestedControlBasePath,omitempty"`
	SuggestedInferenceBasePath string `json:"suggestedInferenceBasePath,omitempty"`
}

type upgradeAccessInfoDocument struct {
	Mode               string                   `json:"mode"`
	ManagementBasePath string                   `json:"managementBasePath"`
	InferenceBasePath  string                   `json:"inferenceBasePath"`
	SharedEntry        bool                     `json:"sharedEntry"`
	ExternalBaseURL    string                   `json:"externalBaseUrl"`
	Protocols          []routing.AccessProtocol `json:"protocols"`
	Ready              bool                     `json:"ready"`
	Code               string                   `json:"code"`
}

type routingActionDocument struct {
	ContractVersion string                    `json:"contractVersion"`
	TargetMode      string                    `json:"targetMode,omitempty"`
	Routing         upgradeRoutingDocument    `json:"routing"`
	AccessInfo      upgradeAccessInfoDocument `json:"accessInfo"`
	UpdatedAtMS     int64                     `json:"updatedAtMs"`
}

func buildUpgradeRoutingAndAccessInfo(op model.UpgradeOperation, targetMode string, externalBaseURL string) (upgradeRoutingDocument, upgradeAccessInfoDocument) {
	sugCtrl, sugInf := strings.TrimSpace(op.SuggestedControlBasePath), strings.TrimSpace(op.SuggestedInferenceBasePath)
	infPath := op.InferenceBasePath
	sharedEntry := op.SharedEntry
	extURL := ""
	if targetMode == model.UpgradeTargetExternal {
		infPath = ""
		sharedEntry = false
		extURL = externalBaseURL
	}
	configured := op.ControlBasePath != "" && (targetMode != model.UpgradeTargetManaged || infPath != "")
	routeDoc := upgradeRoutingDocument{
		Configured:                configured,
		Committed:                 op.RoutingCommitted,
		ExistingPrefixesPreserved: op.RoutingCommitted,
		TargetMode:                targetMode,
		ControlBasePath:           op.ControlBasePath,
		InferenceBasePath:         infPath,
		SharedEntry:               sharedEntry,
		ExternalBaseURL:           extURL,
	}
	if !op.RoutingCommitted {
		routeDoc.SuggestedControlBasePath = sugCtrl
		if targetMode != model.UpgradeTargetExternal {
			routeDoc.SuggestedInferenceBasePath = sugInf
		}
	} else if targetMode == model.UpgradeTargetManaged && !op.InferenceCommitted {
		routeDoc.SuggestedInferenceBasePath = sugInf
	}

	mode := ""
	switch targetMode {
	case model.UpgradeTargetManaged:
		mode = "migrate_legacy"
	case model.UpgradeTargetExternal:
		mode = "connect_external"
	}
	protocols := []routing.AccessProtocol{}
	switch targetMode {
	case model.UpgradeTargetManaged:
		if infPath != "" {
			if list := routing.InferenceProtocols(infPath); len(list) > 0 {
				protocols = list
			}
		}
	case model.UpgradeTargetExternal:
		if extURL != "" {
			if list := routing.InferenceProtocols(extURL); len(list) > 0 {
				protocols = list
			}
		}
	}
	code := "upgrade_access_prefix_required"
	if targetMode == model.UpgradeTargetExternal && extURL == "" {
		code = "external_target_unavailable"
	} else if configured || op.Stage == model.UpgradeStageComplete {
		code = "upgrade_restart_required"
	}
	accessDoc := upgradeAccessInfoDocument{
		Mode:               mode,
		ManagementBasePath: op.ControlBasePath,
		InferenceBasePath:  infPath,
		SharedEntry:        sharedEntry,
		ExternalBaseURL:    extURL,
		Protocols:          protocols,
		Ready:              false,
		Code:               code,
	}
	return routeDoc, accessDoc
}

func decodeOptionalRoutingBody(w http.ResponseWriter, r *http.Request) (upgradeRoutingRequest, bool, bool) {
	if r.Body == nil || r.ContentLength == 0 {
		return upgradeRoutingRequest{}, false, true
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
	if err != nil {
		return upgradeRoutingRequest{}, false, false
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "{}" {
		return upgradeRoutingRequest{}, false, true
	}
	var req upgradeRoutingRequest
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		return upgradeRoutingRequest{}, false, false
	}
	if req.ControlBasePath == "" && req.InferenceBasePath == "" && !req.SharedEntry {
		return upgradeRoutingRequest{}, false, true
	}
	return req, true, true
}

func (h *Handler) serveRouting(w http.ResponseWriter, r *http.Request) {
	target := h.managerDatabase()
	if target == "" {
		writeError(w, http.StatusServiceUnavailable, CodeOperationUnread)
		return
	}
	var req upgradeRoutingRequest
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, string(bootstrap.CodeInvalidAccessPrefix))
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, CodeRequestInvalid)
		return
	}
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	res, err := upgradeservice.ConfigureAccessRouting(ctx, upgradeservice.AccessRoutingOptions{
		TargetRoot:        filepath.Dir(target),
		ControlBasePath:   req.ControlBasePath,
		InferenceBasePath: req.InferenceBasePath,
		SharedEntry:       req.SharedEntry,
	})
	if err != nil {
		writeError(w, actionFailureStatus(err), actionFailureCode(err))
		return
	}
	routeDoc, accessDoc := buildUpgradeRoutingAndAccessInfo(res.Operation, res.TargetMode, res.ExternalBaseURL)
	writeJSON(w, http.StatusOK, routingActionDocument{
		ContractVersion: Contract,
		TargetMode:      res.TargetMode,
		Routing:         routeDoc,
		AccessInfo:      accessDoc,
		UpdatedAtMS:     res.Operation.UpdatedAtMS,
	})
}

func readRoutingStatusColumns(ctx context.Context, db *sql.DB, base model.UpgradeOperation) (model.UpgradeOperation, string, string) {
	present := tableColumns(ctx, db, "upgrade_operations")
	if present["control_base_path"] && present["inference_base_path"] &&
		present["shared_entry"] && present["routing_committed"] &&
		present["suggested_control_base_path"] && present["suggested_inference_base_path"] {
		var ctrl, inf, sugCtrl, sugInf sql.NullString
		var shared, committed, infCommitted sql.NullInt64
		if present["inference_committed"] {
			if db.QueryRowContext(ctx, `select control_base_path, inference_base_path, shared_entry, routing_committed, inference_committed,
				suggested_control_base_path, suggested_inference_base_path
				from upgrade_operations where id = 1`).Scan(&ctrl, &inf, &shared, &committed, &infCommitted, &sugCtrl, &sugInf) == nil {
				base.ControlBasePath = ctrl.String
				base.InferenceBasePath = inf.String
				base.SharedEntry = shared.Int64 != 0
				base.RoutingCommitted = committed.Int64 != 0
				base.InferenceCommitted = infCommitted.Int64 != 0
				base.SuggestedControlBasePath = sugCtrl.String
				base.SuggestedInferenceBasePath = sugInf.String
			}
		} else if db.QueryRowContext(ctx, `select control_base_path, inference_base_path, shared_entry, routing_committed,
			suggested_control_base_path, suggested_inference_base_path
			from upgrade_operations where id = 1`).Scan(&ctrl, &inf, &shared, &committed, &sugCtrl, &sugInf) == nil {
			base.ControlBasePath = ctrl.String
			base.InferenceBasePath = inf.String
			base.SharedEntry = shared.Int64 != 0
			base.RoutingCommitted = committed.Int64 != 0
			base.InferenceCommitted = base.Stage == model.UpgradeStageComplete && base.RoutingCommitted && base.InferenceBasePath != ""
			base.SuggestedControlBasePath = sugCtrl.String
			base.SuggestedInferenceBasePath = sugInf.String
		}
	}
	if present["verification"] {
		var verif sql.NullString
		if db.QueryRowContext(ctx, `select verification from upgrade_operations where id = 1`).Scan(&verif) == nil && verif.String != "" {
			base.Verification = json.RawMessage(verif.String)
		}
	}
	if present["source_fingerprint_after"] && present["backup_manifest_sha256"] {
		var after, manifest sql.NullString
		if db.QueryRowContext(ctx, `select source_fingerprint_after, backup_manifest_sha256 from upgrade_operations where id = 1`).Scan(&after, &manifest) == nil {
			base.SourceFingerprintAfter = after.String
			base.BackupManifestSHA256 = manifest.String
		}
	}
	hasBinding := false
	var binding upgradeoperation.ExternalBinding
	var tbl int
	if db.QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = 'upgrade_external_connect'`).Scan(&tbl) == nil && tbl > 0 {
		extCols := tableColumns(ctx, db, "upgrade_external_connect")
		if extCols["base_url"] && extCols["identity"] && extCols["generation"] && extCols["observed_at_ms"] {
			var baseURL, identity, generation, managementKey sql.NullString
			var observedAt sql.NullInt64
			var scanErr error
			if extCols["management_key"] {
				scanErr = db.QueryRowContext(ctx, `select base_url, identity, generation, management_key, observed_at_ms from upgrade_external_connect where id = 1`).Scan(&baseURL, &identity, &generation, &managementKey, &observedAt)
			} else {
				scanErr = db.QueryRowContext(ctx, `select base_url, identity, generation, observed_at_ms from upgrade_external_connect where id = 1`).Scan(&baseURL, &identity, &generation, &observedAt)
			}
			if scanErr == nil {
				hasBinding = true
				binding = upgradeoperation.ExternalBinding{
					BaseURL:       baseURL.String,
					Identity:      identity.String,
					Generation:    generation.String,
					ManagementKey: managementKey.String,
					ObservedAtMS:  observedAt.Int64,
				}
			}
		} else {
			var cnt int
			if db.QueryRowContext(ctx, `select count(*) from upgrade_external_connect where id = 1`).Scan(&cnt) == nil && cnt > 0 {
				hasBinding = true
			}
		}
	}
	targetMode := upgradeservice.ObserveTargetMode(base, hasBinding)
	extURL := ""
	if targetMode == model.UpgradeTargetExternal {
		extURL = upgradeservice.FencedExternalBaseURLForStore(ctx, db, nil, base, binding, hasBinding)
	}
	return base, targetMode, extURL
}

func tableColumns(ctx context.Context, db *sql.DB, table string) map[string]bool {
	out := map[string]bool{}
	rows, err := db.QueryContext(ctx, `select name from pragma_table_info(?)`, table)
	if err != nil {
		return out
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			out[name] = true
		}
	}
	return out
}
