package legacypreflight

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

type Report struct {
	SourceKind        string                  `json:"sourceKind"`
	Observed          sqlite.LegacyInspection `json:"observed"`
	Unknown           []string                `json:"unknown"`
	Blocked           []string                `json:"blocked"`
	UpgradeAuthorized bool                    `json:"upgradeAuthorized"`
}

type Options struct {
	SourceStopped bool
	// Only an otherwise empty location may accept a parsed provisioning-only
	// config. Old CPA connection configuration remains partial source evidence.
	ProvisioningConfig bool
}

type Admission struct {
	*processlock.Lock
	ConnectionStorage sqlite.PersistedCPAConnectionStorageInspection
}

func Inspect(ctx context.Context, paths sqlite.InspectionPaths, options Options) (Report, error) {
	lock, err := processlock.AcquireExisting(paths.Database)
	if err != nil && !errors.Is(err, processlock.ErrNoExistingLock) {
		return Report{SourceKind: "unknown", Unknown: []string{"source-consistency"}, Blocked: []string{"source-ownership-unavailable"}}, errors.New("source ownership unavailable")
	}
	if lock != nil {
		defer lock.Close()
	}
	return inspect(ctx, paths, options, lock)
}

func inspect(ctx context.Context, paths sqlite.InspectionPaths, options Options, lock *processlock.Lock) (Report, error) {
	observed, err := sqlite.InspectLegacySource(ctx, paths, sqlite.InspectionOptions{SourceStopped: options.SourceStopped, ManagerLock: lock})
	report := Report{SourceKind: "unknown", Observed: observed, Unknown: []string{
		"exact-source-release", "compatibility-profile", "verified-backup", "full-database-integrity", "archive-content",
	}, Blocked: append([]string{}, observed.Issues...)}
	if err != nil {
		report.Blocked = append(report.Blocked, "inspection-failed")
		return report, err
	}
	if len(observed.Files) == 0 {
		return report, errors.New("source inventory unavailable")
	}
	if observed.Files[0].State == "missing" {
		report.SourceKind = "fresh"
		for _, file := range observed.Files[1:7] {
			if file.Role == "config" && options.ProvisioningConfig && file.State == "present" {
				continue
			}
			if file.State == "not-inspected" {
				report.SourceKind = "missing-database"
				report.Unknown = append(report.Unknown, "source-inventory-incomplete")
			}
			if file.State != "missing" && file.State != "not-inspected" {
				report.SourceKind = "partial-source"
				report.Blocked = append(report.Blocked, "database-missing-with-source-artifacts")
			}
		}
		return report, nil
	}
	if observed.Files[0].Size == 0 {
		report.SourceKind = "empty-database"
		return report, nil
	}
	if observed.SQLite != "observed" {
		return report, nil
	}
	if nativeV2Schema(observed) {
		report.SourceKind = "native-v2"
	} else if hasColumns(observed, "settings", "key:text", "value:text") && hasColumns(observed, "usage_events", "id:integer") {
		report.SourceKind = "legacy-manager"
	}
	return report, nil
}

// Recognize the carried-forward Phase1–3 structure, never an exact release or
// legacy support profile. One Gateway table is insufficient for admission.
func nativeV2Schema(source sqlite.LegacyInspection) bool {
	required := map[string][]string{
		"settings":                                {"key:text:1", "value:text"},
		"usage_events":                            {"id:integer:1", "event_hash:text", "timestamp_ms:integer"},
		"gateway_api_key_identities":              {"id:text:1", "revision:integer", "lifecycle:text", "created_at_ms:integer", "updated_at_ms:integer"},
		"gateway_credential_identities":           {"id:text:1", "revision:integer", "lifecycle:text", "created_at_ms:integer", "updated_at_ms:integer"},
		"gateway_api_key_mutation_intents":        strings.Fields("id:text:1 kind:text runtime_identity:text observed_runtime_generation:text api_key_id:text expected_revision:integer old_api_key_hash:text new_api_key_hash:text created_at_ms:integer owner_instance:text forward_completed_at_ms:integer"),
		"gateway_api_key_source_bindings":         strings.Fields("binding_id:integer:1 api_key_id:text runtime_identity:text api_key_hash:text observed_runtime_generation:text first_seen_at_ms:integer last_seen_at_ms:integer retired_at_ms:integer"),
		"gateway_credential_source_bindings":      strings.Fields("binding_id:integer:1 credential_id:text runtime_identity:text source_auth_id:text auth_index:text provider:text physical_name:text account_snapshot:text account_id_snapshot:text observed_runtime_generation:text first_seen_at_ms:integer last_seen_at_ms:integer retired_at_ms:integer"),
		"gateway_credential_delete_intents":       strings.Fields("id:text:1 runtime_identity:text observed_runtime_generation:text physical_name:text owner_instance:text created_at_ms:integer forward_completed_at_ms:integer revoked_ownership_json:text"),
		"gateway_credential_delete_intent_items":  {"intent_id:text:1", "credential_id:text", "source_auth_id:text:2", "expected_revision:integer"},
		"gateway_usage_identity_projection_v1":    strings.Fields("usage_event_id:integer:1 event_hash:text request_id:text evidence_timestamp_ms:integer api_key_state:text api_key_id:text api_key_source_hash:text credential_state:text credential_id:text credential_source_auth_id:text schema_version:integer projected_at_ms:integer"),
		"gateway_usage_identity_projection_state": strings.Fields("state_name:text:1 schema_version:integer status:text last_processed_event_id:integer target_event_id:integer processed_events:integer binding_revision:integer last_run_started_at_ms:integer updated_at_ms:integer finished_at_ms:integer last_error:text"),
		"gateway_quota_policies":                  strings.Fields("id:text:1 revision:integer state:text enforcement:text action:text created_at_ms:integer updated_at_ms:integer"),
		"gateway_quota_policy_rules":              strings.Fields("policy_id:text:1 metric:text:2 limit_value:integer window_kind:text duration_ms:integer calendar_months:integer anchor_at_ms:integer timezone:text"),
		"gateway_api_key_policy_bindings":         strings.Fields("api_key_id:text:1 policy_id:text revision:integer enabled:integer created_at_ms:integer updated_at_ms:integer"),
		"gateway_quota_decision_events_v1":        strings.Fields("decision_id:text:1 schema_version:integer dedupe_key:text api_key_id:text policy_id:text policy_revision:integer binding_revision:integer metric:text enforcement:text action:text outcome:text reason_code:text limit_value:integer observed_value:integer window_start_ms:integer window_end_ms:integer source_usage_event_id:integer source_event_fingerprint:text evidence_timestamp_ms:integer evaluated_at_ms:integer"),
		"gateway_source_binding_revision":         {"id:integer:1", "revision:integer"},
	}
	for table, columns := range required {
		if !hasColumns(source, table, columns...) {
			return false
		}
	}
	return true
}

func hasColumns(source sqlite.LegacyInspection, name string, columns ...string) bool {
	for _, table := range source.Tables {
		if table.Name != name {
			continue
		}
		for _, wanted := range columns {
			parts := strings.SplitN(wanted, ":", 3)
			found := false
			for _, column := range table.Columns {
				if column.Name == parts[0] && strings.EqualFold(column.Type, parts[1]) {
					if len(parts) == 3 {
						primary, err := strconv.Atoi(parts[2])
						if err != nil || column.PrimaryKey != primary {
							continue
						}
					}
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	return false
}

// Initial cold classification is provisional until repeated under the normal
// ownership lock. The lock is retained through native startup, never reacquired.
func AdmitStartup(ctx context.Context, paths sqlite.InspectionPaths, options Options) (admission *Admission, err error) {
	lock, err := processlock.AcquireExisting(paths.Database)
	if err != nil && !errors.Is(err, processlock.ErrNoExistingLock) {
		return nil, err
	}
	owned := false
	defer func() {
		if !owned && lock != nil {
			_ = lock.Close()
			admission = nil
		}
	}()
	options.SourceStopped = true // provisional cold read; WAL still requires a fence
	before, err := inspect(ctx, paths, options, lock)
	if err != nil {
		return nil, err
	}
	if !startupAllowed(before) {
		return nil, errors.New("Manager source requires compatibility and verified-backup authorization; run legacy-preflight")
	}
	if lock == nil {
		lock, err = processlock.Acquire(paths.Database)
		if err != nil {
			return nil, err
		}
		paths.Database = lock.DatabasePath()
		after, err := inspect(ctx, paths, options, lock)
		if err != nil {
			return nil, err
		}
		if !startupAllowed(after) || !sqlite.SameInspectionSource(before.Observed, after.Observed) {
			return nil, errors.New("Manager source changed before startup admission")
		}
		before = after
	}
	if err := lock.Validate(); err != nil {
		return nil, err
	}
	owned = true
	return &Admission{Lock: lock, ConnectionStorage: before.Observed.ConnectionStorage}, nil
}

func startupAllowed(report Report) bool {
	return len(report.Blocked) == 0 && (report.SourceKind == "fresh" || report.SourceKind == "native-v2")
}

func DefaultPaths(database string) sqlite.InspectionPaths {
	return sqlite.InspectionPaths{Database: database, DataKey: filepath.Join(filepath.Dir(database), "data.key"), Archives: filepath.Join(filepath.Dir(database), "usage-archives")}
}
