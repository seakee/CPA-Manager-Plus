package legacypreflight

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacyartifact"
)

type Report struct {
	// Internal binding witness, never inventory or authentication authority.
	AdminCredentialRevision string                  `json:"-"`
	SourceKind              string                  `json:"sourceKind"`
	Observed                sqlite.LegacyInspection `json:"observed"`
	Unknown                 []string                `json:"unknown"`
	Blocked                 []string                `json:"blocked"`
	UpgradeAuthorized       bool                    `json:"upgradeAuthorized"`
	CompatibilityProfile    *CompatibilityProfile   `json:"compatibilityProfile,omitempty"`
	ExactSourceRelease      *ProfileObservation     `json:"exactSourceRelease,omitempty"`
	BridgeReadiness         *BridgeReadiness        `json:"bridgeReadiness,omitempty"`
	PlannedBridgeCandidate  *PlannedBridgeCandidate `json:"plannedBridgeCandidate,omitempty"`
	ForwardHopEvidence      *ForwardHopEvidence     `json:"forwardHopEvidence,omitempty"`
	UpgradePreconditions    *UpgradePreconditions   `json:"upgradePreconditions,omitempty"`
}

type Options struct {
	SourceStopped bool
	Initialize    bool
	// FreshAdmission enables Phase4-03 durable real-store admission receipts.
	FreshAdmission   bool
	ValidatedHop     *legacyartifact.ValidatedHopEvidence
	readOnlySnapshot bool
	adminKey         string
}

type Admission struct {
	*processlock.Lock
	ConnectionStorage sqlite.PersistedCPAConnectionStorageInspection
	fresh             bool
	paths             sqlite.InspectionPaths
	options           Options
	initial           sqlite.LegacyInspection
	closeOnce         sync.Once
	closeErr          error
	markerPermanent   atomic.Bool
	PendingFresh      *processlock.FreshReceipt
}

// RetainMarker makes the ownership marker permanent after writable DB open.
// Later loss of source assets must never turn that marker back into Fresh data.
func (a *Admission) RetainMarker() {
	a.markerPermanent.Store(true)
}

// Close rolls back only an unchanged Fresh admission's own marker. Source
// evidence written after admission is retained for explicit recovery.
func (a *Admission) Close() error {
	if a == nil || a.Lock == nil {
		return nil
	}
	a.closeOnce.Do(func() {
		if a.fresh && !a.markerPermanent.Load() {
			// Once database creation was attempted, do not inspect or roll back it.
			if _, err := os.Lstat(a.paths.Database); os.IsNotExist(err) {
				// Use an independent, bounded context after caller cancellation.
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				report, err := inspect(ctx, a.paths, a.options, a.Lock, true)
				if err == nil && report.SourceKind == "fresh" && startupAllowed(report) &&
					sqlite.SameInspectionSource(a.initial, report.Observed) {
					a.closeErr = a.Lock.DiscardNew()
					return
				}
				a.closeErr = err
			}
		}
		a.closeErr = errors.Join(a.closeErr, a.Lock.Close())
	})
	return a.closeErr
}

const DiscoveryInspectionTimeout = 2 * time.Second

// WithFencedInspection provides the narrow public discovery observation used by
// Phase4-01. Missing-database states are inventory-only. Existing databases use
// only the bounded rollback-mode SQLite read snapshot; WAL/recovery sources
// fail closed for a later qualified owner. A pre-existing Manager marker may be
// retained as coordination evidence, but public discovery never enters the
// general recovery-copy inspection path and never creates a marker.
func WithFencedInspection(ctx context.Context, paths sqlite.InspectionPaths, options Options, consume func(Report, error)) {
	ctx, cancel := context.WithTimeout(ctx, DiscoveryInspectionTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		consume(Report{}, err)
		return
	}
	databaseInfo, err := os.Lstat(paths.Database)
	if os.IsNotExist(err) {
		report, inspectErr := inspect(ctx, paths, options, nil, false)
		consume(report, errors.Join(inspectErr, ctx.Err()))
		return
	}
	if err != nil {
		consume(Report{}, errors.New("source inventory unavailable"))
		return
	}
	if databaseInfo.Size() == 0 {
		consume(Report{}, errors.New("empty database requires host intervention"))
		return
	}

	lock, err := processlock.AcquireExisting(paths.Database)
	if err != nil && !errors.Is(err, processlock.ErrNoExistingLock) {
		consume(Report{}, errors.New("source ownership unavailable"))
		return
	}
	if lock != nil {
		defer lock.Close()
	}

	// Public discovery has one existing-database path: a bounded rollback-mode
	// SQLite read transaction. Do not inherit operator assertions or pass the
	// Manager fence into the broader diagnostic/recovery implementation.
	options.SourceStopped = false
	options.readOnlySnapshot = true
	report, inspectErr := inspect(ctx, paths, options, nil, false)
	if lock != nil {
		inspectErr = errors.Join(inspectErr, lock.Validate())
	}
	consume(report, errors.Join(inspectErr, ctx.Err()))
}

func Inspect(ctx context.Context, paths sqlite.InspectionPaths, options Options) (report Report, err error) {
	// Diagnostic projection only. Startup admission uses inspect directly and
	// never consumes the planned target as source identity or authority.
	defer func() { addPlannedBridgeCandidate(ctx, &report, paths, options.ValidatedHop) }()
	lock, err := processlock.AcquireExisting(paths.Database)
	if err != nil && !errors.Is(err, processlock.ErrNoExistingLock) {
		report := Report{SourceKind: "unknown", Unknown: []string{"source-consistency", "compatibility-profile", "exact-source-release"}, Blocked: []string{"source-ownership-unavailable"}}
		addCompatibilityObservations(&report)
		return report, errors.New("source ownership unavailable")
	}
	if lock != nil {
		defer lock.Close()
	}
	return inspect(ctx, paths, options, lock, false)
}

// createdLock is only set for the fenced recheck after an initially admitted
// source had no ownership marker. Diagnostic callers never ignore an old lock.
func inspect(ctx context.Context, paths sqlite.InspectionPaths, options Options, lock *processlock.Lock, createdLock bool) (report Report, err error) {
	inspectionOptions := sqlite.InspectionOptions{
		SourceStopped:    options.SourceStopped,
		ManagerLock:      lock,
		ReadOnlySnapshot: options.readOnlySnapshot,
	}
	var observed sqlite.LegacyInspection
	var credentialRevision string
	if options.adminKey != "" {
		observed, err = sqlite.InspectLegacySourceForAdmin(ctx, paths, inspectionOptions, func(db *sql.DB) error {
			credential, found, err := setting.New(db).LoadAdminCredential(ctx)
			if err != nil || !found || !security.VerifyAdminKey(credential, options.adminKey) {
				return sqlite.ErrLegacyAdminUnauthorized
			}
			raw, err := json.Marshal(credential)
			if err != nil {
				return sqlite.ErrLegacyAdminUnauthorized
			}
			digest := sha256.Sum256(raw)
			credentialRevision = hex.EncodeToString(digest[:])
			return nil
		})
	} else {
		observed, err = sqlite.InspectLegacySource(ctx, paths, inspectionOptions)
	}
	report = Report{SourceKind: "unknown", Observed: observed, Unknown: []string{
		"exact-source-release", "compatibility-profile", "verified-backup", "full-database-integrity", "archive-content",
	}, Blocked: append([]string{}, observed.Issues...)}
	report.AdminCredentialRevision = credentialRevision
	defer func() { addCompatibilityObservations(&report) }()
	if err != nil {
		report.Blocked = append(report.Blocked, "inspection-failed")
		return report, err
	}
	if len(observed.Files) == 0 {
		return report, errors.New("source inventory unavailable")
	}
	if observed.Files[0].State == "missing" {
		report.SourceKind = "fresh"
		for _, file := range observed.Files[1:] {
			if file.Role == "source-directory" || (file.Role == "process-lock" && createdLock && file.State == "present") {
				continue
			}
			if file.Role == "config" && options.Initialize && file.State == "present" {
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
		if report.SourceKind == "fresh" {
			// A Fresh projection carries an explicit no-v1-product witness. The
			// discovery layer must not infer Fresh from a missing SQLite file
			// without this bounded administrator-state observation.
			observed.BootstrapState = sqlite.BootstrapStateInspection{Valid: true, AdminCredential: "uninitialized"}
			report.Observed = observed
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
	var freshAdmission *Admission
	defer func() {
		if !owned && lock != nil {
			if freshAdmission != nil {
				err = errors.Join(err, freshAdmission.Close())
			} else {
				err = errors.Join(err, lock.Close())
			}
			admission = nil
		}
	}()
	options.SourceStopped = true // provisional cold read; WAL still requires a fence
	if options.FreshAdmission && lock != nil {
		receipt, receiptErr := lock.PendingFreshAdmission()
		if receiptErr != nil {
			return nil, receiptErr
		}
		if receipt != nil {
			if err := validatePendingSource(ctx, paths, options, lock); err != nil {
				return nil, err
			}
			owned = true
			return &Admission{Lock: lock, PendingFresh: receipt}, nil
		}
	}
	before, err := inspect(ctx, paths, options, lock, false)
	if err != nil {
		return nil, err
	}
	if !options.FreshAdmission && before.Observed.FreshOwnershipCheckpointPresent {
		return nil, errors.New("Fresh store requires its product-owned admission path")
	}
	if !startupAllowed(before) {
		return nil, errors.New("Manager source is not proven fresh or native v2; run legacy-preflight and restore missing source assets; use initialize only for a deliberate config-only new installation")
	}
	if lock == nil {
		fresh := before.SourceKind == "fresh"
		lock, err = acquireStartupLock(paths.Database, fresh)
		if err != nil {
			return nil, err
		}
		paths.Database = lock.DatabasePath()
		if fresh {
			freshAdmission = &Admission{Lock: lock, fresh: true, paths: paths, options: options, initial: before.Observed}
			if options.FreshAdmission {
				receipt, receiptErr := lock.BeginFreshAdmission()
				if receiptErr != nil {
					return nil, receiptErr
				}
				freshAdmission.PendingFresh = &receipt
				freshAdmission.RetainMarker()
			}
		}
		after, err := inspect(ctx, paths, options, lock, fresh)
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
	if !options.FreshAdmission && before.Observed.FreshOwnershipCheckpointPresent {
		return nil, errors.New("Fresh store requires its product-owned admission path")
	}
	owned = true
	if freshAdmission != nil {
		freshAdmission.ConnectionStorage = before.Observed.ConnectionStorage
		return freshAdmission, nil
	}
	return &Admission{Lock: lock, ConnectionStorage: before.Observed.ConnectionStorage}, nil
}

func acquireStartupLock(databasePath string, fresh bool) (*processlock.Lock, error) {
	if fresh {
		return processlock.AcquireNew(databasePath)
	}
	return processlock.Acquire(databasePath)
}

func startupAllowed(report Report) bool {
	return len(report.Blocked) == 0 && (report.SourceKind == "fresh" || report.SourceKind == "native-v2")
}

func DefaultPaths(database string) sqlite.InspectionPaths {
	return sqlite.InspectionPaths{Database: database, DataKey: filepath.Join(filepath.Dir(database), "data.key"), Archives: filepath.Join(filepath.Dir(database), "usage-archives")}
}
