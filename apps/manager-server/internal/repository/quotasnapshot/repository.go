package quotasnapshot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

const (
	defaultCandidateLimit            = 1000
	candidateRowsPerSource           = 8
	LegacySnapshotMigrationName      = "quota_snapshot_lifecycle_v1"
	LegacySnapshotOfflineErrorMarker = "legacy quota observation group exceeds safe batch limit"
)

var ErrLegacySnapshotGroupTooLarge = errors.New("legacy quota observation group exceeds online batch limit")

type LegacySnapshotGroupTooLargeError struct {
	Limit int
}

func (e LegacySnapshotGroupTooLargeError) Error() string {
	return fmt.Sprintf("legacy quota observation group exceeds safe batch limit %d; stop Manager Server and run cleanup-derived", e.Limit)
}

func (e LegacySnapshotGroupTooLargeError) Unwrap() error {
	return ErrLegacySnapshotGroupTooLarge
}

type LegacyBackfillResult struct {
	Processed      int
	LastSnapshotID int64
	Pending        bool
	Completed      bool
}

type CyclePlanEvidence struct {
	SnapshotCount    int64
	MissingPlanCount int64
	PlanTypes        []string
}

type Repository interface {
	InsertObservationWrites(ctx context.Context, writes []model.AccountQuotaObservationWrite) error
	ListCandidates(ctx context.Context, accountKey, provider string, limit int) ([]model.AccountQuotaSnapshot, error)
	ListWindowStates(ctx context.Context, accountKey, provider string) ([]model.AccountQuotaWindowState, error)
	ListLegacyCodexWorkspaceWindowStates(ctx context.Context, accountKey, provider string) ([]model.AccountQuotaWindowState, error)
	ListCyclePlanEvidence(ctx context.Context, cycleIDs []int64) (map[int64]CyclePlanEvidence, error)
}

type repository struct {
	db *sql.DB
}

func New(db *sql.DB) Repository {
	return &repository{db: db}
}

// ScopeFingerprint returns the canonical identity for one provider quota
// window scope. It is shared by live writes and the legacy snapshot backfill so
// upgraded rows continue in the same logical-window lifecycle.
func ScopeFingerprint(kind, key string, modelIDs []string) string {
	normalizedModels := make([]string, 0, len(modelIDs))
	seenModels := make(map[string]struct{}, len(modelIDs))
	for _, modelID := range modelIDs {
		normalized := strings.ToLower(strings.TrimSpace(modelID))
		if normalized == "" {
			continue
		}
		if _, exists := seenModels[normalized]; exists {
			continue
		}
		seenModels[normalized] = struct{}{}
		normalizedModels = append(normalizedModels, normalized)
	}
	sort.Strings(normalizedModels)
	payload := strings.Join([]string{
		strings.ToLower(strings.TrimSpace(kind)),
		strings.ToLower(strings.TrimSpace(key)),
		strings.Join(normalizedModels, "\x00"),
	}, "\x00")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
}

// BackfillLegacySnapshotsBatch attaches one complete pre-lifecycle observation
// group per transaction. Partial inventory is intentional: migration must not
// infer provider removals that were never observed by the legacy writer.
func BackfillLegacySnapshotsBatch(ctx context.Context, db *sql.DB, maxGroupSize int) (LegacyBackfillResult, error) {
	if maxGroupSize <= 0 {
		maxGroupSize = 1000
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return LegacyBackfillResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	firstRows, err := loadLegacySnapshots(ctx, tx, `where observation_id is null
		and `+excludeLegacyCodexWorkspaceSnapshotSQL("")+`
		order by account_key, provider, observed_at_ms,
			case lower(trim(source))
				when 'response_body' then 1
				when 'api_query' then 2
				when 'inspection' then 3
				else 0
			end,
			coalesce(source_observation_id, ''), id
		limit 1`)
	if err != nil {
		return LegacyBackfillResult{}, err
	}
	if len(firstRows) == 0 {
		result := LegacyBackfillResult{Completed: true}
		if err := updateLegacyBackfillState(ctx, tx, result); err != nil {
			return LegacyBackfillResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return LegacyBackfillResult{}, err
		}
		return result, nil
	}
	first := firstRows[0]
	groupWhere := `where observation_id is null
		and ` + excludeLegacyCodexWorkspaceSnapshotSQL("") + `
		and account_key = ? and provider = ? and source = ?
		and coalesce(source_observation_id, '') = ? and observed_at_ms = ?`
	args := []any{first.AccountKey, first.Provider, first.Source, first.SourceObservationID, first.ObservedAtMS}
	if strings.EqualFold(strings.TrimSpace(first.Provider), "xai") {
		operator := "not"
		if first.InventoryScopeKey == "xai:included-free" {
			operator = ""
		}
		groupWhere += ` and ` + operator + ` (
			lower(trim(source)) = 'response_body'
			or lower(trim(provider_window_id)) = 'included-free-rolling-24h'
		)`
	}
	groupWhere += ` order by id limit ?`
	args = append(args, maxGroupSize+1)
	snapshots, err := loadLegacySnapshots(ctx, tx, groupWhere, args...)
	if err != nil {
		return LegacyBackfillResult{}, err
	}
	if len(snapshots) > maxGroupSize {
		return LegacyBackfillResult{}, LegacySnapshotGroupTooLargeError{Limit: maxGroupSize}
	}
	groupKey := strings.Join([]string{
		first.AccountKey,
		first.Provider,
		first.InventoryScopeKey,
		first.Source,
		first.SourceObservationID,
		fmt.Sprintf("%d", first.ObservedAtMS),
	}, "\x00")
	write := model.AccountQuotaObservationWrite{
		Observation: model.AccountQuotaObservation{
			AccountKey:          first.AccountKey,
			Provider:            first.Provider,
			Source:              first.Source,
			SourceObservationID: first.SourceObservationID,
			InventoryScopeKey:   first.InventoryScopeKey,
			InventoryMode:       "partial",
			ObservedAtMS:        first.ObservedAtMS,
			CreatedAtMS:         first.CreatedAtMS,
		},
		Snapshots: snapshots,
	}
	lastSnapshotID := int64(0)
	for _, snapshot := range snapshots {
		if snapshot.CreatedAtMS > write.Observation.CreatedAtMS {
			write.Observation.CreatedAtMS = snapshot.CreatedAtMS
		}
		if snapshot.ID > lastSnapshotID {
			lastSnapshotID = snapshot.ID
		}
	}
	applyLegacyCodexRelationships(write.Snapshots)
	write.Observation.WindowCount = len(write.Snapshots)
	write.Observation.ObservationHash = legacyObservationHash(groupKey, write.Snapshots)
	writes := []model.AccountQuotaObservationWrite{write}
	if err := insertObservationWrites(ctx, tx, writes); err != nil {
		return LegacyBackfillResult{}, err
	}
	processed := writes[0].InsertedSnapshotCount
	var pending int
	if err := tx.QueryRowContext(ctx, `select exists (
		select 1 from account_quota_snapshots
		where observation_id is null and `+excludeLegacyCodexWorkspaceSnapshotSQL("")+` limit 1
	)`).Scan(&pending); err != nil {
		return LegacyBackfillResult{}, err
	}
	result := LegacyBackfillResult{
		Processed:      processed,
		LastSnapshotID: lastSnapshotID,
		Pending:        pending != 0,
		Completed:      pending == 0,
	}
	if err := updateLegacyBackfillState(ctx, tx, result); err != nil {
		return LegacyBackfillResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return LegacyBackfillResult{}, err
	}
	return result, nil
}

type legacySnapshotRows interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadLegacySnapshots(ctx context.Context, db legacySnapshotRows, suffix string, args ...any) ([]model.AccountQuotaSnapshot, error) {
	rows, err := db.QueryContext(ctx, `select
		id, account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, coalesce(model_scope_key, ''), coalesce(model_ids_json, ''),
		source, coalesce(source_observation_id, ''), observed_at_ms,
		boundary_accuracy, cycle_start_ms, cycle_end_ms, duration_seconds,
		used_percent, remaining_percent, used_value, limit_value,
		coalesce(quota_unit, ''), reset_credits_available,
		coalesce(reset_credits_json, ''), coalesce(plan_type, ''), created_at_ms
		from account_quota_snapshots `+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.AccountQuotaSnapshot, 0)
	for rows.Next() {
		var snapshot model.AccountQuotaSnapshot
		var cycleStart, cycleEnd, duration sql.NullInt64
		var usedPercent, remainingPercent, usedValue, limitValue sql.NullFloat64
		var resetCreditsAvailable sql.NullInt64
		if err := rows.Scan(
			&snapshot.ID,
			&snapshot.AccountKey,
			&snapshot.Provider,
			&snapshot.ProviderWindowID,
			&snapshot.WindowKind,
			&snapshot.WindowMode,
			&snapshot.ModelScopeKind,
			&snapshot.ModelScopeKey,
			&snapshot.ModelIDsJSON,
			&snapshot.Source,
			&snapshot.SourceObservationID,
			&snapshot.ObservedAtMS,
			&snapshot.BoundaryAccuracy,
			&cycleStart,
			&cycleEnd,
			&duration,
			&usedPercent,
			&remainingPercent,
			&usedValue,
			&limitValue,
			&snapshot.QuotaUnit,
			&resetCreditsAvailable,
			&snapshot.ResetCreditsJSON,
			&snapshot.PlanType,
			&snapshot.CreatedAtMS,
		); err != nil {
			return nil, err
		}
		snapshot.CycleStartMS = int64Pointer(cycleStart)
		snapshot.CycleEndMS = int64Pointer(cycleEnd)
		snapshot.DurationSeconds = int64Pointer(duration)
		snapshot.UsedPercent = float64Pointer(usedPercent)
		snapshot.RemainingPercent = float64Pointer(remainingPercent)
		snapshot.UsedValue = float64Pointer(usedValue)
		snapshot.LimitValue = float64Pointer(limitValue)
		snapshot.ResetCreditsAvailable = int64Pointer(resetCreditsAvailable)
		snapshot.ScopeFingerprint = ScopeFingerprint(snapshot.ModelScopeKind, snapshot.ModelScopeKey, legacyModelIDs(snapshot.ModelIDsJSON))
		snapshot.ContentHash = legacySnapshotContentHash(snapshot)
		snapshot.InventoryScopeKey = legacyInventoryScopeKey(snapshot)
		result = append(result, snapshot)
	}
	return result, rows.Err()
}

type legacyBackfillStateWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func updateLegacyBackfillState(ctx context.Context, db legacyBackfillStateWriter, result LegacyBackfillResult) error {
	nowMS := time.Now().UnixMilli()
	status := "running"
	finishedAt := any(nil)
	if result.Completed {
		status = "completed"
		finishedAt = nowMS
	}
	_, err := db.ExecContext(ctx, `update usage_data_migrations set
		status = ?, last_event_id = max(last_event_id, ?),
		target_event_id = max(target_event_id, coalesce((select max(id) from account_quota_snapshots), 0)),
		processed_rows = processed_rows + ?, changed_rows = changed_rows + ?,
		started_at_ms = coalesce(started_at_ms, ?), updated_at_ms = ?,
		finished_at_ms = ?, last_error = null where name = ?`,
		status,
		result.LastSnapshotID,
		result.Processed,
		result.Processed,
		nowMS,
		nowMS,
		finishedAt,
		LegacySnapshotMigrationName,
	)
	return err
}

// Legacy Codex quota snapshots were keyed by chatgpt_account_id, which is a
// shared Workspace identifier for Team/Business credentials. They cannot be
// attributed to a member after the fact. Keep those derived rows as orphaned
// evidence: do not attach them to the lifecycle graph or expose them through
// the normal quota readers. The key kind is intentionally matched as a
// segment, rather than by decoding the opaque value, so this remains valid for
// every v3 codex-account key without adding schema or a mapping table. Do not
// require provider='codex' here: old derived rows can have incomplete provider
// metadata, and the opaque key kind is the stronger evidence that this is the
// un-attributable workspace-level bucket.
func excludeLegacyCodexWorkspaceSnapshotSQL(alias string) string {
	column := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	return "not (instr(coalesce(" + column("account_key") + ", ''), ':codex-account:') > 0)"
}

func RecordLegacyBackfillFailure(ctx context.Context, db *sql.DB, migrationErr error) error {
	if migrationErr == nil {
		return nil
	}
	nowMS := time.Now().UnixMilli()
	status := "failed"
	finishedAt := any(nowMS)
	if errors.Is(migrationErr, ErrLegacySnapshotGroupTooLarge) {
		status = "offline_required"
		finishedAt = nil
	}
	_, err := db.ExecContext(ctx, `update usage_data_migrations set
		status = ?, started_at_ms = coalesce(started_at_ms, ?),
		updated_at_ms = ?, finished_at_ms = ?, last_error = ? where name = ?`,
		status,
		nowMS,
		nowMS,
		finishedAt,
		migrationErr.Error(),
		LegacySnapshotMigrationName,
	)
	return err
}

// InventoryScopeKey returns the canonical provider inventory scope shared by
// live evidence and legacy snapshot backfills.
func InventoryScopeKey(provider string) string {
	if strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return "codex:rate-limits"
	}
	return strings.ToLower(strings.TrimSpace(provider)) + ":quota-windows"
}

func legacyInventoryScopeKey(snapshot model.AccountQuotaSnapshot) string {
	if strings.EqualFold(strings.TrimSpace(snapshot.Provider), "xai") &&
		(strings.EqualFold(strings.TrimSpace(snapshot.Source), "response_body") ||
			strings.EqualFold(strings.TrimSpace(snapshot.ProviderWindowID), "included-free-rolling-24h")) {
		return "xai:included-free"
	}
	return InventoryScopeKey(snapshot.Provider)
}

func legacyModelIDs(raw string) []string {
	var result []string
	if json.Unmarshal([]byte(raw), &result) != nil {
		return nil
	}
	return result
}

func legacySnapshotContentHash(snapshot model.AccountQuotaSnapshot) string {
	payload, _ := json.Marshal(struct {
		ProviderWindowID string
		WindowKind       string
		WindowMode       string
		ScopeFingerprint string
		CycleStartMS     *int64
		CycleEndMS       *int64
		DurationSeconds  *int64
		UsedPercent      *float64
		RemainingPercent *float64
		UsedValue        *float64
		LimitValue       *float64
		QuotaUnit        string
		PlanType         string
	}{
		ProviderWindowID: snapshot.ProviderWindowID,
		WindowKind:       snapshot.WindowKind,
		WindowMode:       snapshot.WindowMode,
		ScopeFingerprint: snapshot.ScopeFingerprint,
		CycleStartMS:     snapshot.CycleStartMS,
		CycleEndMS:       snapshot.CycleEndMS,
		DurationSeconds:  snapshot.DurationSeconds,
		UsedPercent:      snapshot.UsedPercent,
		RemainingPercent: snapshot.RemainingPercent,
		UsedValue:        snapshot.UsedValue,
		LimitValue:       snapshot.LimitValue,
		QuotaUnit:        snapshot.QuotaUnit,
		PlanType:         snapshot.PlanType,
	})
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

func legacyObservationHash(groupKey string, snapshots []model.AccountQuotaSnapshot) string {
	identities := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		identities = append(identities, fmt.Sprintf("%d:%s", snapshot.ID, snapshot.ContentHash))
	}
	sort.Strings(identities)
	payload := "legacy-quota-snapshot\x00" + groupKey + "\x00" + strings.Join(identities, "\x00")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
}

func applyLegacyCodexRelationships(snapshots []model.AccountQuotaSnapshot) {
	type familyContainer struct {
		weeklyID  string
		monthlyID string
	}
	containersByFamily := make(map[string]familyContainer)
	weeklyByScope := make(map[string][]string)
	for _, snapshot := range snapshots {
		if snapshot.Provider != "codex" {
			continue
		}
		if snapshot.WindowKind == "weekly" {
			weeklyByScope[snapshot.ScopeFingerprint] = append(
				weeklyByScope[snapshot.ScopeFingerprint],
				snapshot.ProviderWindowID,
			)
		}
		family, role, ok := codexWindowFamilyRole(snapshot.ProviderWindowID)
		if !ok || (role != "weekly" && role != "monthly") {
			continue
		}
		key := snapshot.ScopeFingerprint + "\x00" + family
		container := containersByFamily[key]
		if role == "weekly" {
			container.weeklyID = snapshot.ProviderWindowID
		} else {
			container.monthlyID = snapshot.ProviderWindowID
		}
		containersByFamily[key] = container
	}
	for index := range snapshots {
		snapshot := &snapshots[index]
		if snapshot.Provider != "codex" || snapshot.WindowKind != "five_hour" {
			continue
		}
		if family, role, ok := codexWindowFamilyRole(snapshot.ProviderWindowID); ok && role == "five-hour" {
			container := containersByFamily[snapshot.ScopeFingerprint+"\x00"+family]
			containerID := container.weeklyID
			if containerID == "" {
				containerID = container.monthlyID
			}
			if containerID != "" {
				snapshot.RelationshipKind = "concurrent_subwindow"
				snapshot.ContainerWindowID = containerID
				continue
			}
		}
		containers := weeklyByScope[snapshot.ScopeFingerprint]
		if len(containers) != 1 {
			continue
		}
		snapshot.RelationshipKind = "concurrent_subwindow"
		snapshot.ContainerWindowID = containers[0]
	}
}

func codexWindowFamilyRole(providerWindowID string) (string, string, bool) {
	id := strings.TrimSpace(providerWindowID)
	switch id {
	case "five-hour", "weekly", "monthly":
		return "main", id, true
	case "code-review-five-hour":
		return "code-review", "five-hour", true
	case "code-review-weekly":
		return "code-review", "weekly", true
	case "code-review-monthly":
		return "code-review", "monthly", true
	}
	for _, role := range []string{"five-hour", "weekly", "monthly"} {
		marker := "-" + role + "-"
		position := strings.LastIndex(id, marker)
		if position <= 0 {
			continue
		}
		index := id[position+len(marker):]
		if index == "" {
			continue
		}
		if _, err := strconv.Atoi(index); err != nil {
			continue
		}
		return id[:position] + "\x00" + index, role, true
	}
	return "", "", false
}

func (r *repository) ListCandidates(ctx context.Context, accountKey, provider string, limit int) ([]model.AccountQuotaSnapshot, error) {
	if limit <= 0 {
		limit = defaultCandidateLimit
	}
	rows, err := r.db.QueryContext(ctx, `with ranked as (
	select
		id, coalesce(observation_id, 0) as observation_id,
		coalesce(logical_window_id, 0) as logical_window_id,
		coalesce(activation_id, 0) as activation_id,
		coalesce(cycle_id, 0) as cycle_id,
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, coalesce(model_scope_key, '') as model_scope_key,
		coalesce(model_ids_json, '') as model_ids_json,
		coalesce(scope_fingerprint, '') as scope_fingerprint,
		coalesce(content_hash, '') as content_hash,
		source, coalesce(source_observation_id, '') as source_observation_id, observed_at_ms,
		boundary_accuracy, cycle_start_ms, cycle_end_ms, duration_seconds,
		used_percent, remaining_percent, used_value, limit_value,
		coalesce(quota_unit, '') as quota_unit, reset_credits_available,
		coalesce(reset_credits_json, '') as reset_credits_json,
		coalesce(plan_type, '') as plan_type, created_at_ms,
		case when observation_id is null then 'active' else coalesce((
			select availability from account_quota_windows window
			where window.id = account_quota_snapshots.logical_window_id
		), 'inactive') end as window_availability,
		row_number() over (
			partition by coalesce(
				cast(logical_window_id as text),
				'legacy:' || provider_window_id || char(0) || model_scope_kind ||
				char(0) || coalesce(model_scope_key, '') || char(0) ||
				coalesce(model_ids_json, '')
			), source
			order by observed_at_ms desc, id desc
		) as source_rank
		from account_quota_snapshots
		where account_key = ? and provider = ?
			and `+excludeLegacyCodexWorkspaceSnapshotSQL("")+`
			and (observation_id is null or (
				logical_window_id is not null and exists (
					select 1 from account_quota_observations observation
					where observation.id = account_quota_snapshots.observation_id
						and observation.lifecycle_applied = 1
				)
			))
	)
	select
		id, coalesce(observation_id, 0), coalesce(logical_window_id, 0),
		coalesce(activation_id, 0), coalesce(cycle_id, 0),
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, coalesce(model_scope_key, ''), coalesce(model_ids_json, ''),
		coalesce(scope_fingerprint, ''), coalesce(content_hash, ''),
		source, coalesce(source_observation_id, ''), observed_at_ms,
		boundary_accuracy, cycle_start_ms, cycle_end_ms, duration_seconds,
		used_percent, remaining_percent, used_value, limit_value,
		coalesce(quota_unit, ''), reset_credits_available,
		coalesce(reset_credits_json, ''), coalesce(plan_type, ''), created_at_ms
	from ranked
	where source_rank <= ?
	order by case window_availability when 'active' then 0 when 'pending_absent' then 1 else 2 end,
		source_rank, observed_at_ms desc, id desc
	limit ?`, strings.TrimSpace(accountKey), strings.TrimSpace(provider), candidateRowsPerSource, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.AccountQuotaSnapshot, 0)
	for rows.Next() {
		var item model.AccountQuotaSnapshot
		var cycleStart, cycleEnd, duration sql.NullInt64
		var usedPercent, remainingPercent, usedValue, limitValue sql.NullFloat64
		var resetCreditsAvailable sql.NullInt64
		if err := rows.Scan(
			&item.ID,
			&item.ObservationID,
			&item.LogicalWindowID,
			&item.ActivationID,
			&item.CycleID,
			&item.AccountKey,
			&item.Provider,
			&item.ProviderWindowID,
			&item.WindowKind,
			&item.WindowMode,
			&item.ModelScopeKind,
			&item.ModelScopeKey,
			&item.ModelIDsJSON,
			&item.ScopeFingerprint,
			&item.ContentHash,
			&item.Source,
			&item.SourceObservationID,
			&item.ObservedAtMS,
			&item.BoundaryAccuracy,
			&cycleStart,
			&cycleEnd,
			&duration,
			&usedPercent,
			&remainingPercent,
			&usedValue,
			&limitValue,
			&item.QuotaUnit,
			&resetCreditsAvailable,
			&item.ResetCreditsJSON,
			&item.PlanType,
			&item.CreatedAtMS,
		); err != nil {
			return nil, err
		}
		item.CycleStartMS = int64Pointer(cycleStart)
		item.CycleEndMS = int64Pointer(cycleEnd)
		item.DurationSeconds = int64Pointer(duration)
		item.UsedPercent = float64Pointer(usedPercent)
		item.RemainingPercent = float64Pointer(remainingPercent)
		item.UsedValue = float64Pointer(usedValue)
		item.LimitValue = float64Pointer(limitValue)
		item.ResetCreditsAvailable = int64Pointer(resetCreditsAvailable)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *repository) ListCyclePlanEvidence(ctx context.Context, cycleIDs []int64) (map[int64]CyclePlanEvidence, error) {
	uniqueIDs := make([]int64, 0, len(cycleIDs))
	seen := make(map[int64]struct{}, len(cycleIDs))
	for _, cycleID := range cycleIDs {
		if cycleID <= 0 {
			continue
		}
		if _, exists := seen[cycleID]; exists {
			continue
		}
		seen[cycleID] = struct{}{}
		uniqueIDs = append(uniqueIDs, cycleID)
	}
	if len(uniqueIDs) == 0 {
		return map[int64]CyclePlanEvidence{}, nil
	}
	sort.Slice(uniqueIDs, func(i, j int) bool { return uniqueIDs[i] < uniqueIDs[j] })
	placeholders := make([]string, len(uniqueIDs))
	args := make([]any, len(uniqueIDs))
	for index, cycleID := range uniqueIDs {
		placeholders[index] = "?"
		args[index] = cycleID
	}
	rows, err := r.db.QueryContext(ctx, `select
		cycle_id, lower(trim(coalesce(plan_type, ''))) as normalized_plan_type, count(*)
		from account_quota_snapshots
		where cycle_id in (`+strings.Join(placeholders, ",")+`)
		group by cycle_id, normalized_plan_type
		order by cycle_id, normalized_plan_type`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	evidenceByCycle := make(map[int64]CyclePlanEvidence, len(uniqueIDs))
	for rows.Next() {
		var cycleID, count int64
		var planType string
		if err := rows.Scan(&cycleID, &planType, &count); err != nil {
			return nil, err
		}
		evidence := evidenceByCycle[cycleID]
		evidence.SnapshotCount += count
		if planType == "" {
			evidence.MissingPlanCount += count
		} else {
			evidence.PlanTypes = append(evidence.PlanTypes, planType)
		}
		evidenceByCycle[cycleID] = evidence
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return evidenceByCycle, nil
}

func nullString(value string) any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

func nullInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func int64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func float64Pointer(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}
