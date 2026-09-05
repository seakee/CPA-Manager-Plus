package quotasnapshot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/codexquota"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	quotaBoundaryJitterMS             = int64(60 * 1000)
	quotaProvisionalStartJitterMS     = int64(60 * 1000)
	quotaPreviousCycleOverlapJitterMS = int64(60 * 1000)
	quotaResetNearZeroPercent         = 1.0
	quotaResetMinimumPriorPercent     = 5.0
	quotaResetLargeDropPercent        = 20.0
)

type logicalWindowRow struct {
	id                        int64
	providerWindowID          string
	windowKind                string
	windowMode                string
	scopeFingerprint          string
	inventoryScopeKey         string
	availability              string
	generation                int
	absenceCount              int
	firstSeenAtMS             int64
	lastSeenAtMS              int64
	missingSinceMS            sql.NullInt64
	deactivatedAtMS           sql.NullInt64
	relationshipKind          string
	containerProviderWindowID string
}

func (r *repository) InsertObservationWrites(ctx context.Context, writes []model.AccountQuotaObservationWrite) error {
	if len(writes) == 0 {
		return nil
	}
	sortObservationWrites(writes)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertObservationWrites(ctx, tx, writes); err != nil {
		return err
	}
	return tx.Commit()
}

func insertObservationWrites(ctx context.Context, tx *sql.Tx, writes []model.AccountQuotaObservationWrite) error {
	for writeIndex := range writes {
		write := &writes[writeIndex]
		write.InsertedSnapshotCount = 0
		lifecycleApplied, err := observationAdvancesLifecycle(ctx, tx, write.Observation)
		if err != nil {
			return err
		}
		write.Observation.LifecycleApplied = lifecycleApplied
		observationID, inserted, err := insertObservation(ctx, tx, write.Observation)
		if err != nil {
			return err
		}
		if !inserted {
			legacySnapshots := make([]model.AccountQuotaSnapshot, 0, len(write.Snapshots))
			for _, snapshot := range write.Snapshots {
				if snapshot.ID > 0 {
					legacySnapshots = append(legacySnapshots, snapshot)
				}
			}
			if len(legacySnapshots) == 0 {
				continue
			}
			write.Snapshots = legacySnapshots
			var storedLifecycleApplied int
			if err := tx.QueryRowContext(ctx, `select lifecycle_applied
				from account_quota_observations where id = ?`, observationID).Scan(&storedLifecycleApplied); err != nil {
				return err
			}
			write.Observation.LifecycleApplied = storedLifecycleApplied != 0
		}
		write.Observation.ID = observationID
		if err := persistObservationSnapshots(ctx, tx, write); err != nil {
			return err
		}
	}
	return nil
}

func persistObservationSnapshots(ctx context.Context, tx *sql.Tx, write *model.AccountQuotaObservationWrite) error {
	observationID := write.Observation.ID
	if !write.Observation.LifecycleApplied {
		for snapshotIndex := range write.Snapshots {
			snapshot := &write.Snapshots[snapshotIndex]
			snapshot.ObservationID = observationID
			snapshot.LogicalWindowID = 0
			snapshot.ActivationID = 0
			snapshot.CycleID = 0
			if err := persistSnapshot(ctx, tx, *snapshot); err != nil {
				return err
			}
			write.InsertedSnapshotCount++
		}
		return nil
	}
	_, err := restoreSameTimestampDeltaRemovalsForCompleteObservation(
		ctx,
		tx,
		write.Observation,
	)
	if err != nil {
		return err
	}
	reported := make(map[string]struct{}, len(write.Snapshots))
	earlyClosedCycleIDs := make([]int64, 0, len(write.Snapshots))
	for snapshotIndex := range write.Snapshots {
		snapshot := &write.Snapshots[snapshotIndex]
		snapshot.ObservationID = observationID
		if err := reclassifyLegacyCodexAllScope(ctx, tx, write.Observation, snapshot); err != nil {
			return err
		}
		window, activationID, lifecycleOwned, err := reconcileReportedWindow(ctx, tx, write.Observation, snapshot)
		if err != nil {
			return err
		}
		if !lifecycleOwned {
			snapshot.LogicalWindowID = 0
			snapshot.ActivationID = 0
			snapshot.CycleID = 0
			if err := persistSnapshot(ctx, tx, *snapshot); err != nil {
				return err
			}
			write.InsertedSnapshotCount++
			continue
		}
		snapshot.LogicalWindowID = window.id
		snapshot.ActivationID = activationID
		cycleID, closedCycleID, closedEarly, err := reconcileCycle(ctx, tx, activationID, observationID, snapshot)
		if err != nil {
			return err
		}
		snapshot.CycleID = cycleID
		if closedEarly && closedCycleID > 0 {
			earlyClosedCycleIDs = append(earlyClosedCycleIDs, closedCycleID)
		}
		if err := persistSnapshot(ctx, tx, *snapshot); err != nil {
			return err
		}
		write.InsertedSnapshotCount++
		reported[windowIdentity(snapshot.ProviderWindowID, snapshot.ScopeFingerprint)] = struct{}{}
	}
	if len(earlyClosedCycleIDs) > 1 {
		for _, cycleID := range earlyClosedCycleIDs {
			if _, err := tx.ExecContext(ctx, `update account_quota_cycles
				set end_reason = 'provider_reset', updated_at_ms = ? where id = ?`,
				write.Observation.ObservedAtMS, cycleID); err != nil {
				return err
			}
		}
	}
	if err := restoreCodexRelationships(ctx, tx, write.Observation); err != nil {
		return err
	}
	if err := reconcileAbsentWindows(ctx, tx, observationID, write.Observation, reported, write.Removed); err != nil {
		return err
	}
	if err := clearInactiveContainerRelationships(
		ctx,
		tx,
		write.Observation.AccountKey,
		write.Observation.Provider,
		write.Observation.CreatedAtMS,
	); err != nil {
		return err
	}
	return linkContainerCycles(ctx, tx, write.Observation.AccountKey, write.Observation.Provider)
}

func sortObservationWrites(writes []model.AccountQuotaObservationWrite) {
	sort.SliceStable(writes, func(i, j int) bool {
		left := writes[i].Observation
		right := writes[j].Observation
		if left.AccountKey != right.AccountKey {
			return left.AccountKey < right.AccountKey
		}
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		if left.InventoryScopeKey != right.InventoryScopeKey {
			return left.InventoryScopeKey < right.InventoryScopeKey
		}
		return compareObservationOrder(left, right) < 0
	})
}

func compareObservationOrder(left, right model.AccountQuotaObservation) int {
	if left.ObservedAtMS < right.ObservedAtMS {
		return -1
	}
	if left.ObservedAtMS > right.ObservedAtMS {
		return 1
	}
	leftRank := observationAuthorityRank(left)
	rightRank := observationAuthorityRank(right)
	if leftRank < rightRank {
		return -1
	}
	if leftRank > rightRank {
		return 1
	}
	if left.SourceObservationID < right.SourceObservationID {
		return -1
	}
	if left.SourceObservationID > right.SourceObservationID {
		return 1
	}
	if left.ObservationHash < right.ObservationHash {
		return -1
	}
	if left.ObservationHash > right.ObservationHash {
		return 1
	}
	return 0
}

func observationAuthorityRank(observation model.AccountQuotaObservation) int {
	inventoryRank := 0
	switch observation.InventoryMode {
	case "delta":
		inventoryRank = 1
	case "complete":
		inventoryRank = 2
	}
	sourceRank := 0
	switch observation.Source {
	case "response_body":
		sourceRank = 1
	case "api_query":
		sourceRank = 2
	case "inspection":
		sourceRank = 3
	}
	return inventoryRank*10 + sourceRank
}

func observationAdvancesLifecycle(
	ctx context.Context,
	tx *sql.Tx,
	observation model.AccountQuotaObservation,
) (bool, error) {
	var watermark sql.NullInt64
	if err := tx.QueryRowContext(ctx, `select max(observed_at_ms)
		from account_quota_observations
		where account_key = ? and provider = ? and inventory_scope_key = ?
			and lifecycle_applied = 1`,
		observation.AccountKey,
		observation.Provider,
		observation.InventoryScopeKey,
	).Scan(&watermark); err != nil {
		return false, err
	}
	if !watermark.Valid || observation.ObservedAtMS > watermark.Int64 {
		return true, nil
	}
	if observation.ObservedAtMS < watermark.Int64 {
		return false, nil
	}

	rows, err := tx.QueryContext(ctx, `select
		inventory_mode, source, coalesce(source_observation_id, ''), observation_hash
		from account_quota_observations
		where account_key = ? and provider = ? and inventory_scope_key = ?
			and lifecycle_applied = 1 and observed_at_ms = ?`,
		observation.AccountKey,
		observation.Provider,
		observation.InventoryScopeKey,
		watermark.Int64,
	)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()

	var highest model.AccountQuotaObservation
	hasHighest := false
	for rows.Next() {
		candidate := model.AccountQuotaObservation{ObservedAtMS: watermark.Int64}
		if err := rows.Scan(
			&candidate.InventoryMode,
			&candidate.Source,
			&candidate.SourceObservationID,
			&candidate.ObservationHash,
		); err != nil {
			return false, err
		}
		if !hasHighest || compareObservationOrder(candidate, highest) > 0 {
			highest = candidate
			hasHighest = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return !hasHighest || compareObservationOrder(observation, highest) > 0, nil
}

func insertObservation(ctx context.Context, tx *sql.Tx, observation model.AccountQuotaObservation) (int64, bool, error) {
	result, err := tx.ExecContext(ctx, `insert or ignore into account_quota_observations (
		observation_hash, account_key, provider, source, source_observation_id,
		inventory_scope_key, inventory_mode, observed_at_ms, window_count,
		lifecycle_applied, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		observation.ObservationHash,
		observation.AccountKey,
		observation.Provider,
		observation.Source,
		nullString(observation.SourceObservationID),
		observation.InventoryScopeKey,
		observation.InventoryMode,
		observation.ObservedAtMS,
		observation.WindowCount,
		boolInteger(observation.LifecycleApplied),
		observation.CreatedAtMS,
	)
	if err != nil {
		return 0, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	if rows == 0 {
		var id int64
		if err := tx.QueryRowContext(ctx,
			`select id from account_quota_observations where observation_hash = ?`,
			observation.ObservationHash,
		).Scan(&id); err != nil {
			return 0, false, err
		}
		return id, false, nil
	}
	id, err := result.LastInsertId()
	return id, true, err
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

func reconcileReportedWindow(
	ctx context.Context,
	tx *sql.Tx,
	observation model.AccountQuotaObservation,
	snapshot *model.AccountQuotaSnapshot,
) (logicalWindowRow, int64, bool, error) {
	window, err := findLogicalWindow(ctx, tx, snapshot.AccountKey, snapshot.Provider, snapshot.ProviderWindowID, snapshot.ScopeFingerprint)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return logicalWindowRow{}, 0, false, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		relationshipKind, containerWindowID, relationErr := resolveWindowRelationship(ctx, tx, observation, *snapshot, nil)
		if relationErr != nil {
			return logicalWindowRow{}, 0, false, relationErr
		}
		snapshot.RelationshipKind = relationshipKind
		snapshot.ContainerWindowID = containerWindowID
		result, insertErr := tx.ExecContext(ctx, `insert into account_quota_windows (
			account_key, provider, provider_window_id, window_kind, window_mode,
			model_scope_kind, model_scope_key, model_ids_json, scope_fingerprint,
			inventory_scope_key, relationship_kind, container_provider_window_id,
			availability, generation, absence_count, first_seen_at_ms, last_seen_at_ms,
			last_observation_id, created_at_ms, updated_at_ms
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', 1, 0, ?, ?, ?, ?, ?)`,
			snapshot.AccountKey,
			snapshot.Provider,
			snapshot.ProviderWindowID,
			snapshot.WindowKind,
			snapshot.WindowMode,
			snapshot.ModelScopeKind,
			nullString(snapshot.ModelScopeKey),
			nullString(snapshot.ModelIDsJSON),
			snapshot.ScopeFingerprint,
			observation.InventoryScopeKey,
			nullString(snapshot.RelationshipKind),
			nullString(snapshot.ContainerWindowID),
			observation.ObservedAtMS,
			observation.ObservedAtMS,
			observation.ID,
			observation.CreatedAtMS,
			observation.CreatedAtMS,
		)
		if insertErr != nil {
			return logicalWindowRow{}, 0, false, insertErr
		}
		windowID, insertErr := result.LastInsertId()
		if insertErr != nil {
			return logicalWindowRow{}, 0, false, insertErr
		}
		window = logicalWindowRow{
			id: windowID, providerWindowID: snapshot.ProviderWindowID,
			windowKind: snapshot.WindowKind, windowMode: snapshot.WindowMode,
			scopeFingerprint: snapshot.ScopeFingerprint, inventoryScopeKey: observation.InventoryScopeKey,
			availability: "active", generation: 1, firstSeenAtMS: observation.ObservedAtMS,
			lastSeenAtMS: observation.ObservedAtMS, relationshipKind: snapshot.RelationshipKind,
			containerProviderWindowID: snapshot.ContainerWindowID,
		}
	} else {
		if window.inventoryScopeKey != observation.InventoryScopeKey {
			return window, 0, false, nil
		}
		if snapshot.WindowKind == "unknown" && window.windowKind != "" && window.windowKind != "unknown" {
			snapshot.WindowKind = window.windowKind
		}
		if snapshot.WindowMode == "unknown" && window.windowMode != "" && window.windowMode != "unknown" {
			snapshot.WindowMode = window.windowMode
		}
		restoredSameTimestampRemoval, restoreErr := restoreSameTimestampRemoval(
			ctx,
			tx,
			observation,
			window,
		)
		if restoreErr != nil {
			return logicalWindowRow{}, 0, false, restoreErr
		}
		if restoredSameTimestampRemoval {
			window.availability = "active"
			window.deactivatedAtMS = sql.NullInt64{}
		}
		relationshipKind, containerWindowID, relationErr := resolveWindowRelationship(ctx, tx, observation, *snapshot, &window)
		if relationErr != nil {
			return logicalWindowRow{}, 0, false, relationErr
		}
		snapshot.RelationshipKind = relationshipKind
		snapshot.ContainerWindowID = containerWindowID
		generation := window.generation
		if window.availability == "inactive" {
			generation++
		}
		lastSeenAtMS := window.lastSeenAtMS
		if observation.ObservedAtMS > lastSeenAtMS {
			lastSeenAtMS = observation.ObservedAtMS
		}
		if _, err := tx.ExecContext(ctx, `update account_quota_windows set
			window_kind = ?, window_mode = ?, model_scope_kind = ?, model_scope_key = ?,
			model_ids_json = ?, inventory_scope_key = ?, relationship_kind = ?,
			container_provider_window_id = ?, availability = 'active', generation = ?,
			absence_count = 0, last_seen_at_ms = ?, missing_since_ms = null,
			deactivated_at_ms = null, last_observation_id = ?, updated_at_ms = ?
			where id = ?`,
			snapshot.WindowKind,
			snapshot.WindowMode,
			snapshot.ModelScopeKind,
			nullString(snapshot.ModelScopeKey),
			nullString(snapshot.ModelIDsJSON),
			window.inventoryScopeKey,
			nullString(snapshot.RelationshipKind),
			nullString(snapshot.ContainerWindowID),
			generation,
			lastSeenAtMS,
			observation.ID,
			observation.CreatedAtMS,
			window.id,
		); err != nil {
			return logicalWindowRow{}, 0, false, err
		}
		window.availability = "active"
		window.windowKind = snapshot.WindowKind
		window.windowMode = snapshot.WindowMode
		window.generation = generation
		window.absenceCount = 0
		window.lastSeenAtMS = lastSeenAtMS
		window.missingSinceMS = sql.NullInt64{}
		window.deactivatedAtMS = sql.NullInt64{}
		window.relationshipKind = snapshot.RelationshipKind
		window.containerProviderWindowID = snapshot.ContainerWindowID
	}

	activationID, err := activeActivationID(ctx, tx, window.id)
	if errors.Is(err, sql.ErrNoRows) {
		result, insertErr := tx.ExecContext(ctx, `insert into account_quota_window_activations (
			window_id, generation, status, activated_at_ms, activation_accuracy,
			activate_observation_id, created_at_ms, updated_at_ms
		) values (?, ?, 'active', ?, ?, ?, ?, ?)`,
			window.id,
			window.generation,
			activationStart(*snapshot, observation.ObservedAtMS),
			activationAccuracy(*snapshot),
			observation.ID,
			observation.CreatedAtMS,
			observation.CreatedAtMS,
		)
		if insertErr != nil {
			return logicalWindowRow{}, 0, false, insertErr
		}
		activationID, insertErr = result.LastInsertId()
		if insertErr != nil {
			return logicalWindowRow{}, 0, false, insertErr
		}
	} else if err != nil {
		return logicalWindowRow{}, 0, false, err
	}
	return window, activationID, true, nil
}

func reclassifyLegacyCodexAllScope(
	ctx context.Context,
	tx *sql.Tx,
	observation model.AccountQuotaObservation,
	snapshot *model.AccountQuotaSnapshot,
) error {
	if snapshot == nil || observation.Provider != "codex" || strings.EqualFold(strings.TrimSpace(snapshot.ModelScopeKind), "all") {
		return nil
	}
	scope := codexquota.NormalizeScope(codexquota.ModelScope{
		Kind:     snapshot.ModelScopeKind,
		Key:      snapshot.ModelScopeKey,
		Models:   legacyModelIDs(snapshot.ModelIDsJSON),
		Complete: false,
	})
	scope.Complete = len(scope.Models) > 0 || codexquota.IsMainScope(scope.Kind, scope.Key)
	migrateMainScope := codexquota.IsLegacyMainAllScopeMigration(snapshot.ProviderWindowID, scope)
	deactivateLegacyScope := codexquota.IsLegacyAllScopeReplacement(snapshot.ProviderWindowID, scope)
	if !migrateMainScope && !deactivateLegacyScope {
		return nil
	}
	aliases := codexquota.ProviderWindowAliases(
		snapshot.ProviderWindowID,
		scope,
		snapshot.ProviderWindowAliases...,
	)
	if len(aliases) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(aliases)), ",")
	args := make([]any, 0, 3+len(aliases))
	args = append(args, observation.AccountKey, observation.Provider, observation.InventoryScopeKey)
	for _, alias := range aliases {
		args = append(args, alias)
	}
	rows, err := tx.QueryContext(ctx, `select
		id, provider_window_id, window_kind, window_mode, scope_fingerprint,
		inventory_scope_key, availability, generation, absence_count,
		first_seen_at_ms, last_seen_at_ms, missing_since_ms, deactivated_at_ms,
		coalesce(relationship_kind, ''), coalesce(container_provider_window_id, '')
		from account_quota_windows
		where account_key = ? and provider = ? and inventory_scope_key = ?
			and lower(trim(model_scope_kind)) = 'all' and availability <> 'inactive'
			and lower(trim(provider_window_id)) in (`+placeholders+`)`, args...)
	if err != nil {
		return err
	}
	legacyWindows := make([]logicalWindowRow, 0)
	for rows.Next() {
		var window logicalWindowRow
		if err := rows.Scan(
			&window.id,
			&window.providerWindowID,
			&window.windowKind,
			&window.windowMode,
			&window.scopeFingerprint,
			&window.inventoryScopeKey,
			&window.availability,
			&window.generation,
			&window.absenceCount,
			&window.firstSeenAtMS,
			&window.lastSeenAtMS,
			&window.missingSinceMS,
			&window.deactivatedAtMS,
			&window.relationshipKind,
			&window.containerProviderWindowID,
		); err != nil {
			_ = rows.Close()
			return err
		}
		legacyWindows = append(legacyWindows, window)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if migrateMainScope && len(legacyWindows) > 0 {
		var targetExists int
		if err := tx.QueryRowContext(ctx, `select exists (
			select 1 from account_quota_windows
			where account_key = ? and provider = ? and provider_window_id = ?
				and scope_fingerprint = ?
		)`, observation.AccountKey, observation.Provider, snapshot.ProviderWindowID, snapshot.ScopeFingerprint).Scan(&targetExists); err != nil {
			return err
		}
		if targetExists == 0 {
			keeperIndex := 0
			for index, window := range legacyWindows {
				if strings.EqualFold(strings.TrimSpace(window.providerWindowID), strings.TrimSpace(snapshot.ProviderWindowID)) {
					keeperIndex = index
					break
				}
			}
			keeper := legacyWindows[keeperIndex]
			snapshot.ProviderWindowID = keeper.providerWindowID
			snapshot.ModelScopeKind = "all"
			snapshot.ModelScopeKey = ""
			snapshot.ModelIDsJSON = ""
			snapshot.ScopeFingerprint = keeper.scopeFingerprint
			for index, window := range legacyWindows {
				if index == keeperIndex {
					continue
				}
				if err := deactivateWindowWithReason(
					ctx,
					tx,
					window,
					observation.ID,
					observation.ObservedAtMS,
					observation.CreatedAtMS,
					"scope_reclassified",
					"scope_reclassified",
					false,
				); err != nil {
					return err
				}
			}
			return nil
		}
	}

	for _, window := range legacyWindows {
		if err := deactivateWindowWithReason(
			ctx,
			tx,
			window,
			observation.ID,
			observation.ObservedAtMS,
			observation.CreatedAtMS,
			"scope_reclassified",
			"scope_reclassified",
			false,
		); err != nil {
			return err
		}
	}
	return nil
}

func restoreSameTimestampRemoval(
	ctx context.Context,
	tx *sql.Tx,
	observation model.AccountQuotaObservation,
	window logicalWindowRow,
) (bool, error) {
	if window.availability != "inactive" {
		return false, nil
	}

	var activationID, removalObservationID int64
	err := tx.QueryRowContext(ctx, `select activation.id, removal.id
		from account_quota_window_activations activation
		join account_quota_observations removal
			on removal.id = activation.deactivate_observation_id
		where activation.window_id = ? and activation.generation = ?
			and activation.deactivated_at_ms is not null
			and removal.account_key = ? and removal.provider = ?
			and removal.inventory_scope_key = ? and removal.observed_at_ms = ?
		limit 1`,
		window.id,
		window.generation,
		observation.AccountKey,
		observation.Provider,
		observation.InventoryScopeKey,
		observation.ObservedAtMS,
	).Scan(&activationID, &removalObservationID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if _, err := tx.ExecContext(ctx, `update account_quota_window_activations set
		status = 'active', deactivated_at_ms = null, deactivation_reason = null,
		deactivate_observation_id = null, updated_at_ms = ? where id = ?`,
		observation.CreatedAtMS,
		activationID,
	); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `update account_quota_cycles set
		state = 'active', actual_end_ms = null, end_reason = null,
		updated_at_ms = ? where activation_id = ? and actual_end_ms is not null
			and end_reason = 'window_removed' and last_observation_id = ?`,
		observation.CreatedAtMS,
		activationID,
		removalObservationID,
	); err != nil {
		return false, err
	}
	return true, nil
}

func restoreSameTimestampDeltaRemovalsForCompleteObservation(
	ctx context.Context,
	tx *sql.Tx,
	observation model.AccountQuotaObservation,
) (bool, error) {
	if observation.InventoryMode != "complete" {
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, `select
		window.id, window.provider_window_id, window.scope_fingerprint, window.inventory_scope_key,
		window.availability, window.generation, window.absence_count,
		window.first_seen_at_ms, window.last_seen_at_ms,
		window.missing_since_ms, window.deactivated_at_ms,
		coalesce(window.relationship_kind, ''), coalesce(window.container_provider_window_id, '')
		from account_quota_windows window
		join account_quota_window_activations activation
			on activation.window_id = window.id and activation.generation = window.generation
		join account_quota_observations removal
			on removal.id = activation.deactivate_observation_id
		where window.account_key = ? and window.provider = ?
			and window.inventory_scope_key = ? and window.availability = 'inactive'
			and activation.deactivated_at_ms is not null
			and removal.inventory_mode = 'delta' and removal.observed_at_ms = ?`,
		observation.AccountKey,
		observation.Provider,
		observation.InventoryScopeKey,
		observation.ObservedAtMS,
	)
	if err != nil {
		return false, err
	}
	windows := make([]logicalWindowRow, 0)
	for rows.Next() {
		var window logicalWindowRow
		if err := rows.Scan(
			&window.id,
			&window.providerWindowID,
			&window.scopeFingerprint,
			&window.inventoryScopeKey,
			&window.availability,
			&window.generation,
			&window.absenceCount,
			&window.firstSeenAtMS,
			&window.lastSeenAtMS,
			&window.missingSinceMS,
			&window.deactivatedAtMS,
			&window.relationshipKind,
			&window.containerProviderWindowID,
		); err != nil {
			_ = rows.Close()
			return false, err
		}
		windows = append(windows, window)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}

	restoredAny := false
	for _, window := range windows {
		restored, err := restoreSameTimestampRemoval(ctx, tx, observation, window)
		if err != nil {
			return false, err
		}
		if !restored {
			continue
		}
		availability := "active"
		if window.missingSinceMS.Valid {
			availability = "pending_absent"
		}
		absenceCount := window.absenceCount - 1
		if absenceCount < 0 {
			absenceCount = 0
		}
		if _, err := tx.ExecContext(ctx, `update account_quota_windows set
			availability = ?, absence_count = ?, deactivated_at_ms = null,
			last_observation_id = ?, updated_at_ms = ? where id = ?`,
			availability,
			absenceCount,
			observation.ID,
			observation.CreatedAtMS,
			window.id,
		); err != nil {
			return false, err
		}
		restoredAny = true
	}
	return restoredAny, nil
}

func restoreCodexRelationships(
	ctx context.Context,
	tx *sql.Tx,
	observation model.AccountQuotaObservation,
) error {
	if observation.Provider != "codex" {
		return nil
	}
	type relationshipWindow struct {
		id               int64
		providerWindowID string
		scopeFingerprint string
		availability     string
		relationshipKind string
		containerID      string
	}
	rows, err := tx.QueryContext(ctx, `select
		id, provider_window_id, scope_fingerprint, availability,
		coalesce(relationship_kind, ''), coalesce(container_provider_window_id, '')
		from account_quota_windows
		where account_key = ? and provider = ? and inventory_scope_key = ?
			and availability <> 'inactive'`,
		observation.AccountKey,
		observation.Provider,
		observation.InventoryScopeKey,
	)
	if err != nil {
		return err
	}
	windows := make([]relationshipWindow, 0)
	for rows.Next() {
		var window relationshipWindow
		if err := rows.Scan(
			&window.id,
			&window.providerWindowID,
			&window.scopeFingerprint,
			&window.availability,
			&window.relationshipKind,
			&window.containerID,
		); err != nil {
			_ = rows.Close()
			return err
		}
		windows = append(windows, window)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	type containers struct {
		weekly  string
		monthly string
	}
	containersByFamily := make(map[string]containers)
	for _, window := range windows {
		if window.availability != "active" {
			continue
		}
		family, role, ok := codexWindowFamilyRole(window.providerWindowID)
		if !ok || (role != "weekly" && role != "monthly") {
			continue
		}
		key := window.scopeFingerprint + "\x00" + family
		item := containersByFamily[key]
		if role == "weekly" {
			item.weekly = window.providerWindowID
		} else {
			item.monthly = window.providerWindowID
		}
		containersByFamily[key] = item
	}
	for _, window := range windows {
		if strings.TrimSpace(window.relationshipKind) != "" || strings.TrimSpace(window.containerID) != "" {
			continue
		}
		family, role, ok := codexWindowFamilyRole(window.providerWindowID)
		if !ok || role != "five-hour" {
			continue
		}
		container := containersByFamily[window.scopeFingerprint+"\x00"+family]
		containerID := container.weekly
		if containerID == "" {
			containerID = container.monthly
		}
		if containerID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `update account_quota_windows set
			relationship_kind = 'concurrent_subwindow', container_provider_window_id = ?,
			updated_at_ms = ? where id = ?`,
			containerID,
			observation.CreatedAtMS,
			window.id,
		); err != nil {
			return err
		}
	}
	return nil
}

func resolveWindowRelationship(
	ctx context.Context,
	tx *sql.Tx,
	observation model.AccountQuotaObservation,
	snapshot model.AccountQuotaSnapshot,
	existing *logicalWindowRow,
) (string, string, error) {
	relationshipKind := strings.TrimSpace(snapshot.RelationshipKind)
	containerWindowID := strings.TrimSpace(snapshot.ContainerWindowID)
	if observation.InventoryMode != "complete" && existing != nil {
		if relationshipKind == "" {
			relationshipKind = existing.relationshipKind
		}
		if containerWindowID == "" {
			containerWindowID = existing.containerProviderWindowID
		}
	}
	if observation.InventoryMode == "complete" && existing != nil &&
		relationshipKind == "" && containerWindowID == "" &&
		existing.relationshipKind != "" && existing.containerProviderWindowID != "" {
		// A complete inventory omission only moves the container into
		// pending_absent on its first occurrence. Preserve the established
		// relationship until the second omission confirms that the container is
		// actually inactive.
		relationshipKind = existing.relationshipKind
		containerWindowID = existing.containerProviderWindowID
	}
	if observation.InventoryMode == "complete" || relationshipKind != "" || containerWindowID != "" {
		return relationshipKind, containerWindowID, nil
	}
	if snapshot.Provider != "codex" {
		return "", "", nil
	}
	family, role, ok := codexWindowFamilyRole(snapshot.ProviderWindowID)
	if !ok || role != "five-hour" {
		return "", "", nil
	}
	rows, err := tx.QueryContext(ctx, `select provider_window_id
		from account_quota_windows
		where account_key = ? and provider = ?
			and scope_fingerprint = ? and inventory_scope_key = ? and availability = 'active'
		order by updated_at_ms desc, id desc`,
		snapshot.AccountKey,
		snapshot.Provider,
		snapshot.ScopeFingerprint,
		observation.InventoryScopeKey,
	)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = rows.Close() }()
	weeklyID := ""
	monthlyID := ""
	for rows.Next() {
		var candidateID string
		if err := rows.Scan(&candidateID); err != nil {
			return "", "", err
		}
		candidateFamily, candidateRole, ok := codexWindowFamilyRole(candidateID)
		if !ok || candidateFamily != family {
			continue
		}
		switch candidateRole {
		case "weekly":
			if weeklyID == "" {
				weeklyID = candidateID
			}
		case "monthly":
			if monthlyID == "" {
				monthlyID = candidateID
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	containerID := weeklyID
	if containerID == "" {
		containerID = monthlyID
	}
	if containerID == "" {
		return "", "", nil
	}
	return "concurrent_subwindow", containerID, nil
}

func reconcileCycle(
	ctx context.Context,
	tx *sql.Tx,
	activationID int64,
	observationID int64,
	snapshot *model.AccountQuotaSnapshot,
) (cycleID, closedCycleID int64, closedEarly bool, err error) {
	active, activeErr := activeCycle(ctx, tx, activationID)
	if activeErr != nil && !errors.Is(activeErr, sql.ErrNoRows) {
		return 0, 0, false, activeErr
	}
	if snapshot.WindowMode != "fixed" && snapshot.WindowMode != "calendar" {
		if (snapshot.WindowMode == "rolling" || snapshot.WindowMode == "non_window") && activeErr == nil {
			endMS := snapshot.ObservedAtMS
			if endMS < active.ActualStartMS {
				endMS = active.ActualStartMS
			}
			if _, err := tx.ExecContext(ctx, `update account_quota_cycles set
				state = 'closed', actual_end_ms = ?, end_reason = 'mode_changed',
				last_observation_id = ?, updated_at_ms = ? where id = ?`,
				endMS, observationID, snapshot.CreatedAtMS, active.ID); err != nil {
				return 0, 0, false, err
			}
			return 0, active.ID, false, nil
		}
		return 0, 0, false, nil
	}
	if snapshot.CycleStartMS == nil || snapshot.CycleEndMS == nil || snapshot.DurationSeconds == nil {
		if activeErr != nil {
			return 0, 0, false, nil
		}
		applyActiveCycleBoundary(snapshot, active)
		if _, err := tx.ExecContext(ctx, `update account_quota_cycles set
			last_observation_id = ?, updated_at_ms = ? where id = ?`,
			observationID, snapshot.CreatedAtMS, active.ID); err != nil {
			return 0, 0, false, err
		}
		return active.ID, 0, false, nil
	}
	if activeErr == nil && isReliableLifecycleBoundary(*snapshot) && active.ScheduledEndMS != nil {
		strictScheduledRollover := *snapshot.CycleStartMS >= *active.ScheduledEndMS
		nearScheduledRollover := cycleBoundaryIsNearScheduledRollover(active, *snapshot)
		if strictScheduledRollover || nearScheduledRollover {
			actualEndMS := *active.ScheduledEndMS
			if actualEndMS < active.ActualStartMS {
				actualEndMS = active.ActualStartMS
			}
			if _, err := tx.ExecContext(ctx, `update account_quota_cycles set
				state = 'closed', actual_end_ms = ?, end_reason = 'scheduled',
				last_observation_id = ?, updated_at_ms = ? where id = ?`,
				actualEndMS, observationID, snapshot.CreatedAtMS, active.ID); err != nil {
				return 0, 0, false, err
			}
			var id int64
			if nearScheduledRollover {
				id, err = restoreOrInsertCycleAt(ctx, tx, activationID, observationID, &actualEndMS, snapshot)
			} else {
				id, err = restoreOrInsertCycle(ctx, tx, activationID, observationID, snapshot)
			}
			return id, active.ID, false, err
		}
	}
	if isProvisionalZeroAPIBoundary(*snapshot) {
		// A zero-use Codex API response whose calculated start follows the query
		// time is not proof that a provider cycle has started. Keep the evidence,
		// but do not let it establish, split, or reset lifecycle state. Marking the
		// persisted boundary unknown also prevents consumers from deriving usage
		// ranges from this snapshot when there is no confirmed active cycle yet.
		snapshot.BoundaryAccuracy = "unknown"
		return 0, 0, false, nil
	}
	if errors.Is(activeErr, sql.ErrNoRows) {
		id, restoreErr := restoreOrInsertCycle(ctx, tx, activationID, observationID, snapshot)
		return id, 0, false, restoreErr
	}
	// Canonicalization below replaces stale timing geometry with the active
	// boundary, so the incoming evidence authority must be captured first:
	// geometry correction must not promote estimated or unknown evidence into
	// confirmed lifecycle authority.
	incomingBoundaryConfirmed := isConfirmedLifecycleBoundary(*snapshot)
	temporallyInvalidBoundary := cycleBoundaryIsTemporallyInvalidAgainstActive(active, *snapshot)
	if temporallyInvalidBoundary {
		// The incoming boundary is already expired while the active cycle's
		// scheduled end still covers the observation time. Sanitize the
		// evidence to the current boundary before any lifecycle transition
		// consumes it; the counter and observation time stay untouched.
		applyActiveCycleBoundary(snapshot, active)
	}
	cycleMatches := cycleMatchesSnapshot(active, *snapshot)
	// Counter-reset reconciliation splits lifecycle state, so it needs a
	// canonical boundary that is still current at the observation time to
	// carry the transition. An already expired boundary keeps its usage-drop
	// evidence but cannot drive the split on its own.
	counterResetBoundaryCurrent := cycleBoundaryCoversObservation(*snapshot)
	if incomingBoundaryConfirmed && counterResetBoundaryCurrent {
		counterReset, err := quotaCounterResetDetected(ctx, tx, active, *snapshot)
		if err != nil {
			return 0, 0, false, err
		}
		if counterReset {
			transitionMS := confirmedResetTransitionMS(active, *snapshot, cycleMatches)
			if _, err := tx.ExecContext(ctx, `update account_quota_cycles set
				state = 'closed', actual_end_ms = ?, end_reason = 'early_reset',
				last_observation_id = ?, updated_at_ms = ? where id = ?`,
				transitionMS, observationID, snapshot.CreatedAtMS, active.ID); err != nil {
				return 0, 0, false, err
			}
			id, err := insertCounterResetCycle(ctx, tx, activationID, observationID, transitionMS, *snapshot)
			return id, active.ID, true, err
		}
	}
	refreshExpiredBoundary := cycleBoundaryShouldRefreshExpiredCurrent(active, *snapshot)
	actualStartMS := correctedActiveCycleActualStart(active, *snapshot)
	if cycleMatches {
		if cycleBoundaryCanReplaceActive(active, *snapshot) || refreshExpiredBoundary {
			if _, err := tx.ExecContext(ctx, `update account_quota_cycles set
				scheduled_start_ms = ?, scheduled_end_ms = ?, actual_start_ms = ?,
				duration_seconds = ?, boundary_accuracy = ?, last_observation_id = ?, updated_at_ms = ?
				where id = ?`,
				snapshot.CycleStartMS,
				snapshot.CycleEndMS,
				actualStartMS,
				snapshot.DurationSeconds,
				snapshot.BoundaryAccuracy,
				observationID,
				snapshot.CreatedAtMS,
				active.ID,
			); err != nil {
				return 0, 0, false, err
			}
		} else {
			applyActiveCycleBoundary(snapshot, active)
			if _, err := tx.ExecContext(ctx, `update account_quota_cycles set
				last_observation_id = ?, updated_at_ms = ? where id = ?`,
				observationID, snapshot.CreatedAtMS, active.ID); err != nil {
				return 0, 0, false, err
			}
		}
		return active.ID, 0, false, nil
	}
	if isConfirmedLifecycleBoundary(*snapshot) &&
		(cycleBoundaryCanReplaceActive(active, *snapshot) || refreshExpiredBoundary) {
		if _, err := tx.ExecContext(ctx, `update account_quota_cycles set
			scheduled_start_ms = ?, scheduled_end_ms = ?, actual_start_ms = ?,
			duration_seconds = ?, boundary_accuracy = ?, last_observation_id = ?, updated_at_ms = ?
			where id = ?`,
			snapshot.CycleStartMS,
			snapshot.CycleEndMS,
			actualStartMS,
			snapshot.DurationSeconds,
			snapshot.BoundaryAccuracy,
			observationID,
			snapshot.CreatedAtMS,
			active.ID,
		); err != nil {
			return 0, 0, false, err
		}
		return active.ID, 0, false, nil
	}

	// A boundary change before the confirmed scheduled end is not sufficient
	// evidence of a reset. Preserve the confirmed cycle until a counter reset,
	// scheduled rollover, or stronger boundary correction proves otherwise.
	applyActiveCycleBoundary(snapshot, active)
	if _, err := tx.ExecContext(ctx, `update account_quota_cycles set
		last_observation_id = ?, updated_at_ms = ? where id = ?`,
		observationID, snapshot.CreatedAtMS, active.ID); err != nil {
		return 0, 0, false, err
	}
	return active.ID, 0, false, nil
}

func isConfirmedLifecycleBoundary(snapshot model.AccountQuotaSnapshot) bool {
	return isReliableLifecycleBoundary(snapshot) && !isProvisionalZeroAPIBoundary(snapshot)
}

func isReliableLifecycleBoundary(snapshot model.AccountQuotaSnapshot) bool {
	return (snapshot.Source == "response_header" || snapshot.Source == "api_query" || snapshot.Source == "inspection") &&
		(snapshot.WindowMode == "fixed" || snapshot.WindowMode == "calendar") &&
		(snapshot.BoundaryAccuracy == "exact" || snapshot.BoundaryAccuracy == "derived") &&
		snapshot.CycleStartMS != nil && snapshot.CycleEndMS != nil && snapshot.DurationSeconds != nil
}

func isProvisionalZeroAPIBoundary(snapshot model.AccountQuotaSnapshot) bool {
	return snapshot.Provider == "codex" &&
		snapshot.Source == "api_query" &&
		snapshot.WindowMode == "fixed" &&
		(snapshot.BoundaryAccuracy == "exact" || snapshot.BoundaryAccuracy == "derived") &&
		snapshot.UsedPercent != nil && *snapshot.UsedPercent == 0 &&
		snapshot.CycleStartMS != nil && snapshot.CycleEndMS != nil && snapshot.DurationSeconds != nil &&
		absInt64(*snapshot.CycleStartMS-snapshot.ObservedAtMS) <= quotaProvisionalStartJitterMS
}

func confirmedResetTransitionMS(cycle model.AccountQuotaCycle, snapshot model.AccountQuotaSnapshot, sameBoundary bool) int64 {
	transitionMS := *snapshot.CycleStartMS
	if sameBoundary {
		transitionMS = snapshot.ObservedAtMS
	}
	if transitionMS > snapshot.ObservedAtMS {
		transitionMS = snapshot.ObservedAtMS
	}
	if transitionMS < cycle.ActualStartMS {
		transitionMS = cycle.ActualStartMS
	}
	return transitionMS
}

func cycleBoundaryShouldUpgrade(cycle model.AccountQuotaCycle, snapshot model.AccountQuotaSnapshot) bool {
	return boundaryAccuracyValue(snapshot.BoundaryAccuracy) > boundaryAccuracyValue(cycle.BoundaryAccuracy)
}

// cycleBoundaryShouldRefreshExpiredCurrent reports whether a confirmed fresh
// boundary that still covers the observation time must replace an active cycle
// whose scheduled end has already passed. This is a temporal-validity
// correction rather than a precision upgrade: once the stored scheduled end is
// in the past it can no longer represent the current window, even when its
// accuracy rank is higher than the fresh evidence. The cycle keeps its
// identity, so no rollover, reset, or additional cycle is created.
func cycleBoundaryShouldRefreshExpiredCurrent(
	cycle model.AccountQuotaCycle,
	snapshot model.AccountQuotaSnapshot,
) bool {
	if !isConfirmedLifecycleBoundary(snapshot) ||
		cycle.ScheduledEndMS == nil ||
		cycle.DurationSeconds == nil ||
		snapshot.CycleEndMS == nil ||
		snapshot.DurationSeconds == nil {
		return false
	}
	if *cycle.DurationSeconds != *snapshot.DurationSeconds {
		return false
	}
	return snapshot.ObservedAtMS >= *cycle.ScheduledEndMS &&
		*snapshot.CycleEndMS > snapshot.ObservedAtMS
}

// cycleBoundaryIsNearScheduledRollover reports whether a reliable incoming
// boundary is just before the active scheduled end because of provider timing
// jitter. The observation must already be at the old end, while the incoming
// boundary must still cover that observation, so material overlaps continue to
// use the expired-boundary refresh path.
func cycleBoundaryIsNearScheduledRollover(
	cycle model.AccountQuotaCycle,
	snapshot model.AccountQuotaSnapshot,
) bool {
	if isProvisionalZeroAPIBoundary(snapshot) {
		return false
	}
	if cycle.ScheduledEndMS == nil ||
		cycle.DurationSeconds == nil ||
		snapshot.CycleStartMS == nil ||
		snapshot.CycleEndMS == nil ||
		snapshot.DurationSeconds == nil {
		return false
	}
	if *cycle.DurationSeconds != *snapshot.DurationSeconds ||
		*snapshot.CycleStartMS >= *cycle.ScheduledEndMS {
		return false
	}
	return *cycle.ScheduledEndMS-*snapshot.CycleStartMS <= quotaBoundaryJitterMS &&
		snapshot.ObservedAtMS >= *cycle.ScheduledEndMS &&
		*snapshot.CycleEndMS > snapshot.ObservedAtMS
}

// cycleBoundaryIsTemporallyInvalidAgainstActive reports whether an incoming
// boundary is already expired while the active cycle's scheduled end still
// covers the observation time. Such evidence is temporally stale: temporal
// validity outranks accuracy, so it must not move a current cycle into the
// past.
func cycleBoundaryIsTemporallyInvalidAgainstActive(
	cycle model.AccountQuotaCycle,
	snapshot model.AccountQuotaSnapshot,
) bool {
	if cycle.ScheduledEndMS == nil ||
		snapshot.CycleEndMS == nil {
		return false
	}
	return *cycle.ScheduledEndMS > snapshot.ObservedAtMS &&
		*snapshot.CycleEndMS <= snapshot.ObservedAtMS
}

// cycleBoundaryCoversObservation reports whether the snapshot's canonical
// boundary still extends beyond the observation time. Counter-reset
// reconciliation needs a boundary that is current at the observation to carry
// the transition; an expired boundary keeps its usage-drop evidence but cannot
// split lifecycle state on its own.
func cycleBoundaryCoversObservation(snapshot model.AccountQuotaSnapshot) bool {
	return snapshot.CycleEndMS != nil &&
		*snapshot.CycleEndMS > snapshot.ObservedAtMS
}

// cycleBoundaryWouldRegressExpiredActive reports whether an already expired
// incoming boundary is materially older than the active boundary. Differences
// within the provider jitter stay the same canonical boundary and may still be
// upgraded for precision; a larger backward movement would rewind a previously
// refreshed boundary into the past. The difference is computed after the
// ordering precondition, so the subtraction cannot overflow or go negative.
func cycleBoundaryWouldRegressExpiredActive(
	cycle model.AccountQuotaCycle,
	snapshot model.AccountQuotaSnapshot,
) bool {
	if cycle.ScheduledEndMS == nil ||
		snapshot.CycleEndMS == nil ||
		*snapshot.CycleEndMS > snapshot.ObservedAtMS ||
		*cycle.ScheduledEndMS <= *snapshot.CycleEndMS {
		return false
	}
	return *cycle.ScheduledEndMS-*snapshot.CycleEndMS >
		quotaBoundaryJitterMS
}

// cycleBoundaryCanReplaceActive reports whether an incoming higher-accuracy
// boundary may replace the active cycle boundary. A temporally stale incoming
// boundary never qualifies, even on a precision upgrade, and neither does an
// expired boundary that is materially older than the active one: accuracy
// alone must not rewind a known boundary into the past.
func cycleBoundaryCanReplaceActive(
	cycle model.AccountQuotaCycle,
	snapshot model.AccountQuotaSnapshot,
) bool {
	if snapshot.CycleEndMS == nil {
		return false
	}
	if cycleBoundaryIsTemporallyInvalidAgainstActive(cycle, snapshot) {
		return false
	}
	if cycleBoundaryWouldRegressExpiredActive(cycle, snapshot) {
		return false
	}
	return cycleBoundaryShouldUpgrade(cycle, snapshot)
}

// correctedActiveCycleActualStart preserves an independently established
// actual transition when the active cycle's actual start differs from its
// scheduled start. Boundary evidence updates scheduled timing and must not
// rewrite that transition.
func correctedActiveCycleActualStart(
	cycle model.AccountQuotaCycle,
	snapshot model.AccountQuotaSnapshot,
) int64 {
	if cycle.ScheduledStartMS != nil &&
		cycle.ActualStartMS != *cycle.ScheduledStartMS {
		return cycle.ActualStartMS
	}
	return *snapshot.CycleStartMS
}

func quotaCounterResetDetected(
	ctx context.Context,
	tx *sql.Tx,
	cycle model.AccountQuotaCycle,
	snapshot model.AccountQuotaSnapshot,
) (bool, error) {
	if snapshot.UsedPercent == nil || snapshot.ObservedAtMS <= cycle.ActualStartMS {
		return false, nil
	}
	var previous float64
	err := tx.QueryRowContext(ctx, `select used_percent
		from account_quota_snapshots
		where cycle_id = ? and used_percent is not null and observed_at_ms < ?
		order by observed_at_ms desc, id desc limit 1`,
		cycle.ID,
		snapshot.ObservedAtMS,
	).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	current := *snapshot.UsedPercent
	return quotaCounterResetValues(previous, current) ||
		quotaLowUsageBoundaryResetForSnapshot(cycle, snapshot, previous, current), nil
}

func quotaCounterResetValues(previous, current float64) bool {
	if previous < quotaResetMinimumPriorPercent || current >= previous {
		return false
	}
	drop := previous - current
	return current <= quotaResetNearZeroPercent ||
		(drop >= quotaResetLargeDropPercent && current <= previous/2)
}

func quotaLowUsageBoundaryResetForSnapshot(
	cycle model.AccountQuotaCycle,
	snapshot model.AccountQuotaSnapshot,
	previous, current float64,
) bool {
	return quotaLowUsageBoundaryResetValues(
		previous,
		current,
		cycle.ScheduledStartMS,
		cycle.ScheduledEndMS,
		cycle.DurationSeconds,
		cycle.BoundaryAccuracy,
		snapshot.CycleStartMS,
		snapshot.CycleEndMS,
		snapshot.DurationSeconds,
		snapshot.BoundaryAccuracy,
	)
}

func quotaLowUsageBoundaryResetValues(
	previous, current float64,
	previousStartMS, previousEndMS, previousDurationSeconds *int64,
	previousAccuracy string,
	nextStartMS, nextEndMS, nextDurationSeconds *int64,
	nextAccuracy string,
) bool {
	if previous >= quotaResetMinimumPriorPercent || previous <= current || current > quotaResetNearZeroPercent ||
		previousStartMS == nil || previousEndMS == nil || previousDurationSeconds == nil ||
		nextStartMS == nil || nextEndMS == nil || nextDurationSeconds == nil ||
		boundaryAccuracyValue(previousAccuracy) < boundaryAccuracyValue("derived") ||
		boundaryAccuracyValue(nextAccuracy) < boundaryAccuracyValue("derived") ||
		boundaryAccuracyValue(nextAccuracy) > boundaryAccuracyValue(previousAccuracy) ||
		*previousDurationSeconds != *nextDurationSeconds {
		return false
	}
	startShiftMS := *nextStartMS - *previousStartMS
	endShiftMS := *nextEndMS - *previousEndMS
	return startShiftMS > quotaBoundaryJitterMS &&
		absInt64(startShiftMS-endShiftMS) <= quotaBoundaryJitterMS &&
		*nextStartMS < *previousEndMS-quotaBoundaryJitterMS
}

func boundaryAccuracyValue(value string) int {
	switch value {
	case "exact":
		return 3
	case "derived":
		return 2
	case "estimated":
		return 1
	default:
		return 0
	}
}

func restoreOrInsertCycle(
	ctx context.Context,
	tx *sql.Tx,
	activationID, observationID int64,
	snapshot *model.AccountQuotaSnapshot,
) (int64, error) {
	return restoreOrInsertCycleAt(ctx, tx, activationID, observationID, nil, snapshot)
}

func restoreOrInsertCycleAt(
	ctx context.Context,
	tx *sql.Tx,
	activationID, observationID int64,
	actualStartMS *int64,
	snapshot *model.AccountQuotaSnapshot,
) (int64, error) {
	closed, err := matchingClosedCycle(ctx, tx, activationID, *snapshot)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		if actualStartMS == nil {
			return insertCycle(ctx, tx, activationID, observationID, *snapshot)
		}
		return insertCycleWithKey(
			ctx,
			tx,
			activationID,
			observationID,
			*actualStartMS,
			providerCycleKey(*snapshot),
			*snapshot,
		)
	}

	if !cycleBoundaryShouldUpgrade(closed, *snapshot) {
		applyActiveCycleBoundary(snapshot, closed)
	}
	if actualStartMS == nil {
		actualStartMS = snapshot.CycleStartMS
	}
	if _, err := tx.ExecContext(ctx, `update account_quota_cycles set
		state = 'active', scheduled_start_ms = ?, scheduled_end_ms = ?, actual_start_ms = ?,
		actual_end_ms = null, duration_seconds = ?, boundary_accuracy = ?, end_reason = null,
		last_observation_id = ?, updated_at_ms = ? where id = ?`,
		snapshot.CycleStartMS,
		snapshot.CycleEndMS,
		*actualStartMS,
		snapshot.DurationSeconds,
		snapshot.BoundaryAccuracy,
		observationID,
		snapshot.CreatedAtMS,
		closed.ID,
	); err != nil {
		return 0, err
	}
	return closed.ID, nil
}

func matchingClosedCycle(
	ctx context.Context,
	tx *sql.Tx,
	activationID int64,
	snapshot model.AccountQuotaSnapshot,
) (model.AccountQuotaCycle, error) {
	cycleKey := providerCycleKey(snapshot)
	return scanCycle(tx.QueryRowContext(ctx, `select
		id, activation_id, provider_cycle_key, state, scheduled_start_ms, scheduled_end_ms,
		actual_start_ms, actual_end_ms, duration_seconds, boundary_accuracy,
		coalesce(end_reason, ''), first_observation_id, last_observation_id, parent_cycle_id,
		created_at_ms, updated_at_ms
		from account_quota_cycles
		where activation_id = ? and actual_end_ms is not null and end_reason = 'mode_changed' and (
			provider_cycle_key = ? or (
				duration_seconds = ? and scheduled_end_ms is not null
				and abs(scheduled_end_ms - ?) <= ?
			)
		)
		order by case when provider_cycle_key = ? then 0 else 1 end,
			case boundary_accuracy when 'exact' then 0 when 'derived' then 1 else 2 end,
			actual_end_ms desc, id desc limit 1`,
		activationID,
		cycleKey,
		snapshot.DurationSeconds,
		snapshot.CycleEndMS,
		quotaBoundaryJitterMS,
		cycleKey,
	))
}

func applyActiveCycleBoundary(snapshot *model.AccountQuotaSnapshot, cycle model.AccountQuotaCycle) {
	if cycle.ScheduledStartMS != nil {
		snapshot.CycleStartMS = copyInt64(cycle.ScheduledStartMS)
	} else {
		startMS := cycle.ActualStartMS
		snapshot.CycleStartMS = &startMS
	}
	snapshot.CycleEndMS = copyInt64(cycle.ScheduledEndMS)
	snapshot.DurationSeconds = copyInt64(cycle.DurationSeconds)
	snapshot.BoundaryAccuracy = cycle.BoundaryAccuracy
}

func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func insertCycle(ctx context.Context, tx *sql.Tx, activationID, observationID int64, snapshot model.AccountQuotaSnapshot) (int64, error) {
	return insertCycleWithKey(ctx, tx, activationID, observationID, *snapshot.CycleStartMS, providerCycleKey(snapshot), snapshot)
}

func insertCounterResetCycle(
	ctx context.Context,
	tx *sql.Tx,
	activationID, observationID, actualStartMS int64,
	snapshot model.AccountQuotaSnapshot,
) (int64, error) {
	cycleKey := fmt.Sprintf("%s:reset:%d", providerCycleKey(snapshot), actualStartMS)
	return insertCycleWithKey(ctx, tx, activationID, observationID, actualStartMS, cycleKey, snapshot)
}

func insertCycleWithKey(
	ctx context.Context,
	tx *sql.Tx,
	activationID, observationID, actualStartMS int64,
	cycleKey string,
	snapshot model.AccountQuotaSnapshot,
) (int64, error) {
	result, err := tx.ExecContext(ctx, `insert into account_quota_cycles (
		activation_id, provider_cycle_key, state, scheduled_start_ms, scheduled_end_ms,
		actual_start_ms, duration_seconds, boundary_accuracy, first_observation_id,
		last_observation_id, created_at_ms, updated_at_ms
	) values (?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		activationID,
		cycleKey,
		snapshot.CycleStartMS,
		snapshot.CycleEndMS,
		actualStartMS,
		snapshot.DurationSeconds,
		snapshot.BoundaryAccuracy,
		observationID,
		observationID,
		snapshot.CreatedAtMS,
		snapshot.CreatedAtMS,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func reconcileAbsentWindows(
	ctx context.Context,
	tx *sql.Tx,
	observationID int64,
	observation model.AccountQuotaObservation,
	reported map[string]struct{},
	removed []model.AccountQuotaWindowRemoval,
) error {
	for _, item := range removed {
		window, err := findLogicalWindow(ctx, tx, observation.AccountKey, observation.Provider, item.ProviderWindowID, item.ScopeFingerprint)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if window.inventoryScopeKey != observation.InventoryScopeKey {
			continue
		}
		if window.availability == "inactive" {
			continue
		}
		if err := deactivateWindow(ctx, tx, window, observationID, observation.ObservedAtMS, observation.CreatedAtMS); err != nil {
			return err
		}
	}
	if observation.InventoryMode != "complete" {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `select
		id, provider_window_id, scope_fingerprint, inventory_scope_key,
		availability, generation, absence_count, first_seen_at_ms, last_seen_at_ms,
		missing_since_ms, deactivated_at_ms,
		coalesce(relationship_kind, ''), coalesce(container_provider_window_id, '')
		from account_quota_windows
		where account_key = ? and provider = ? and inventory_scope_key = ? and availability <> 'inactive'`,
		observation.AccountKey, observation.Provider, observation.InventoryScopeKey)
	if err != nil {
		return err
	}
	windows := make([]logicalWindowRow, 0)
	for rows.Next() {
		var window logicalWindowRow
		if err := rows.Scan(
			&window.id, &window.providerWindowID, &window.scopeFingerprint,
			&window.inventoryScopeKey, &window.availability, &window.generation,
			&window.absenceCount, &window.firstSeenAtMS, &window.lastSeenAtMS,
			&window.missingSinceMS, &window.deactivatedAtMS,
			&window.relationshipKind, &window.containerProviderWindowID,
		); err != nil {
			_ = rows.Close()
			return err
		}
		windows = append(windows, window)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, window := range windows {
		if _, ok := reported[windowIdentity(window.providerWindowID, window.scopeFingerprint)]; ok {
			continue
		}
		if window.availability == "pending_absent" && window.absenceCount >= 1 {
			if window.missingSinceMS.Valid && observation.ObservedAtMS <= window.missingSinceMS.Int64 {
				continue
			}
			if err := deactivateWindow(ctx, tx, window, observationID, observation.ObservedAtMS, observation.CreatedAtMS); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `update account_quota_windows set
			availability = 'pending_absent', absence_count = absence_count + 1,
			missing_since_ms = coalesce(missing_since_ms, ?), last_observation_id = ?, updated_at_ms = ?
			where id = ?`, observation.ObservedAtMS, observationID, observation.CreatedAtMS, window.id); err != nil {
			return err
		}
	}
	return nil
}

func deactivateWindow(
	ctx context.Context,
	tx *sql.Tx,
	window logicalWindowRow,
	observationID, observedAtMS, updatedAtMS int64,
) error {
	return deactivateWindowWithReason(
		ctx,
		tx,
		window,
		observationID,
		observedAtMS,
		updatedAtMS,
		"provider_removed",
		"window_removed",
		true,
	)
}

func deactivateWindowWithReason(
	ctx context.Context,
	tx *sql.Tx,
	window logicalWindowRow,
	observationID, observedAtMS, updatedAtMS int64,
	activationReason, cycleReason string,
	preferMissingSince bool,
) error {
	transitionMS := observedAtMS
	if preferMissingSince && window.missingSinceMS.Valid {
		transitionMS = window.missingSinceMS.Int64
	}
	if _, err := tx.ExecContext(ctx, `update account_quota_windows set
		availability = 'inactive', deactivated_at_ms = ?, absence_count = absence_count + 1,
		last_observation_id = ?, updated_at_ms = ? where id = ?`,
		transitionMS, observationID, updatedAtMS, window.id); err != nil {
		return err
	}
	activationID, err := activeActivationID(ctx, tx, window.id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update account_quota_window_activations set
		status = 'inactive', deactivated_at_ms = ?, deactivation_reason = ?,
		deactivate_observation_id = ?, updated_at_ms = ? where id = ?`,
		transitionMS, activationReason, observationID, updatedAtMS, activationID); err != nil {
		return err
	}
	active, err := activeCycle(ctx, tx, activationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	endMS := transitionMS
	if endMS < active.ActualStartMS {
		endMS = active.ActualStartMS
	}
	_, err = tx.ExecContext(ctx, `update account_quota_cycles set
		state = 'closed', actual_end_ms = ?, end_reason = ?,
		last_observation_id = ?, updated_at_ms = ? where id = ?`,
		endMS, cycleReason, observationID, updatedAtMS, active.ID)
	return err
}

func clearInactiveContainerRelationships(
	ctx context.Context,
	tx *sql.Tx,
	accountKey, provider string,
	updatedAtMS int64,
) error {
	if _, err := tx.ExecContext(ctx, `update account_quota_cycles
		set parent_cycle_id = null, updated_at_ms = ?
		where actual_end_ms is null and activation_id in (
			select activation.id
			from account_quota_window_activations activation
			join account_quota_windows child on child.id = activation.window_id
			join account_quota_windows parent
				on parent.account_key = child.account_key
				and parent.provider = child.provider
				and parent.provider_window_id = child.container_provider_window_id
				and parent.scope_fingerprint = child.scope_fingerprint
				and parent.inventory_scope_key = child.inventory_scope_key
			where child.account_key = ? and child.provider = ?
				and activation.deactivated_at_ms is null
				and coalesce(trim(child.relationship_kind), '') <> ''
				and coalesce(trim(child.container_provider_window_id), '') <> ''
				and parent.availability = 'inactive'
		)`, updatedAtMS, accountKey, provider); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `update account_quota_windows
		set relationship_kind = null, container_provider_window_id = null, updated_at_ms = ?
		where account_key = ? and provider = ?
			and coalesce(trim(relationship_kind), '') <> ''
			and coalesce(trim(container_provider_window_id), '') <> ''
			and exists (
				select 1 from account_quota_windows parent
				where parent.account_key = account_quota_windows.account_key
					and parent.provider = account_quota_windows.provider
					and parent.provider_window_id = account_quota_windows.container_provider_window_id
					and parent.scope_fingerprint = account_quota_windows.scope_fingerprint
					and parent.inventory_scope_key = account_quota_windows.inventory_scope_key
					and parent.availability = 'inactive'
			)`, updatedAtMS, accountKey, provider)
	return err
}

func insertSnapshot(ctx context.Context, tx *sql.Tx, snapshot model.AccountQuotaSnapshot) error {
	_, err := tx.ExecContext(ctx, `insert into account_quota_snapshots (
		observation_id, logical_window_id, activation_id, cycle_id,
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, model_scope_key, model_ids_json, scope_fingerprint, content_hash,
		source, source_observation_id, observed_at_ms, boundary_accuracy,
		cycle_start_ms, cycle_end_ms, duration_seconds, used_percent,
		remaining_percent, used_value, limit_value, quota_unit,
		reset_credits_available, reset_credits_json, plan_type, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullInt64(snapshot.ObservationID),
		nullInt64(snapshot.LogicalWindowID),
		nullInt64(snapshot.ActivationID),
		nullInt64(snapshot.CycleID),
		snapshot.AccountKey,
		snapshot.Provider,
		snapshot.ProviderWindowID,
		snapshot.WindowKind,
		snapshot.WindowMode,
		snapshot.ModelScopeKind,
		nullString(snapshot.ModelScopeKey),
		nullString(snapshot.ModelIDsJSON),
		snapshot.ScopeFingerprint,
		snapshot.ContentHash,
		snapshot.Source,
		nullString(snapshot.SourceObservationID),
		snapshot.ObservedAtMS,
		snapshot.BoundaryAccuracy,
		snapshot.CycleStartMS,
		snapshot.CycleEndMS,
		snapshot.DurationSeconds,
		snapshot.UsedPercent,
		snapshot.RemainingPercent,
		snapshot.UsedValue,
		snapshot.LimitValue,
		nullString(snapshot.QuotaUnit),
		snapshot.ResetCreditsAvailable,
		nullString(snapshot.ResetCreditsJSON),
		nullString(snapshot.PlanType),
		snapshot.CreatedAtMS,
	)
	return err
}

func persistSnapshot(ctx context.Context, tx *sql.Tx, snapshot model.AccountQuotaSnapshot) error {
	if snapshot.ID <= 0 {
		return insertSnapshot(ctx, tx, snapshot)
	}
	result, err := tx.ExecContext(ctx, `update account_quota_snapshots set
		observation_id = ?, logical_window_id = ?, activation_id = ?, cycle_id = ?,
		scope_fingerprint = ?, content_hash = ?
		where id = ? and observation_id is null`,
		nullInt64(snapshot.ObservationID),
		nullInt64(snapshot.LogicalWindowID),
		nullInt64(snapshot.ActivationID),
		nullInt64(snapshot.CycleID),
		snapshot.ScopeFingerprint,
		snapshot.ContentHash,
		snapshot.ID,
	)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return fmt.Errorf("legacy quota snapshot %d was not attached to lifecycle", snapshot.ID)
	}
	return nil
}

func findLogicalWindow(ctx context.Context, tx *sql.Tx, accountKey, provider, providerWindowID, scopeFingerprint string) (logicalWindowRow, error) {
	var window logicalWindowRow
	err := tx.QueryRowContext(ctx, `select
		id, provider_window_id, window_kind, window_mode, scope_fingerprint, inventory_scope_key,
		availability, generation, absence_count, first_seen_at_ms, last_seen_at_ms,
		missing_since_ms, deactivated_at_ms,
		coalesce(relationship_kind, ''), coalesce(container_provider_window_id, '')
		from account_quota_windows
		where account_key = ? and provider = ? and provider_window_id = ? and scope_fingerprint = ?`,
		accountKey, provider, providerWindowID, scopeFingerprint,
	).Scan(
		&window.id, &window.providerWindowID, &window.windowKind, &window.windowMode,
		&window.scopeFingerprint,
		&window.inventoryScopeKey, &window.availability, &window.generation,
		&window.absenceCount, &window.firstSeenAtMS, &window.lastSeenAtMS,
		&window.missingSinceMS, &window.deactivatedAtMS,
		&window.relationshipKind, &window.containerProviderWindowID,
	)
	return window, err
}

func activeActivationID(ctx context.Context, tx *sql.Tx, windowID int64) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `select id from account_quota_window_activations
		where window_id = ? and deactivated_at_ms is null order by generation desc limit 1`, windowID).Scan(&id)
	return id, err
}

func activeCycle(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, activationID int64) (model.AccountQuotaCycle, error) {
	return scanCycle(queryer.QueryRowContext(ctx, `select
		id, activation_id, provider_cycle_key, state, scheduled_start_ms, scheduled_end_ms,
		actual_start_ms, actual_end_ms, duration_seconds, boundary_accuracy,
		coalesce(end_reason, ''), first_observation_id, last_observation_id, parent_cycle_id,
		created_at_ms, updated_at_ms
		from account_quota_cycles where activation_id = ? and actual_end_ms is null
		order by actual_start_ms desc limit 1`, activationID))
}

func scanCycle(row *sql.Row) (model.AccountQuotaCycle, error) {
	var cycle model.AccountQuotaCycle
	var scheduledStart, scheduledEnd, actualEnd, duration sql.NullInt64
	var firstObservationID, lastObservationID, parentCycleID sql.NullInt64
	err := row.Scan(
		&cycle.ID, &cycle.ActivationID, &cycle.ProviderCycleKey, &cycle.State,
		&scheduledStart, &scheduledEnd, &cycle.ActualStartMS, &actualEnd, &duration,
		&cycle.BoundaryAccuracy, &cycle.EndReason, &firstObservationID,
		&lastObservationID, &parentCycleID, &cycle.CreatedAtMS, &cycle.UpdatedAtMS,
	)
	if err != nil {
		return model.AccountQuotaCycle{}, err
	}
	cycle.ScheduledStartMS = int64Pointer(scheduledStart)
	cycle.ScheduledEndMS = int64Pointer(scheduledEnd)
	cycle.ActualEndMS = int64Pointer(actualEnd)
	cycle.DurationSeconds = int64Pointer(duration)
	cycle.FirstObservationID = int64Pointer(firstObservationID)
	cycle.LastObservationID = int64Pointer(lastObservationID)
	cycle.ParentCycleID = int64Pointer(parentCycleID)
	return cycle, nil
}

func linkContainerCycles(ctx context.Context, tx *sql.Tx, accountKey, provider string) error {
	type childCycle struct {
		id                int64
		containerWindowID string
		scopeFingerprint  string
		inventoryScopeKey string
		actualStartMS     int64
	}
	rows, err := tx.QueryContext(ctx, `select c.id, w.container_provider_window_id,
			w.scope_fingerprint, w.inventory_scope_key, c.actual_start_ms
		from account_quota_windows w
		join account_quota_window_activations a
			on a.window_id = w.id and a.deactivated_at_ms is null
		join account_quota_cycles c
			on c.activation_id = a.id and c.actual_end_ms is null
		where w.account_key = ? and w.provider = ? and w.availability = 'active'
			and coalesce(trim(w.relationship_kind), '') <> ''
			and coalesce(trim(w.container_provider_window_id), '') <> ''`,
		accountKey,
		provider,
	)
	if err != nil {
		return err
	}
	children := make([]childCycle, 0)
	for rows.Next() {
		var child childCycle
		if err := rows.Scan(
			&child.id,
			&child.containerWindowID,
			&child.scopeFingerprint,
			&child.inventoryScopeKey,
			&child.actualStartMS,
		); err != nil {
			_ = rows.Close()
			return err
		}
		children = append(children, child)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, child := range children {
		parentCycleID, found, err := resolveContainerCycleID(
			ctx,
			tx,
			accountKey,
			provider,
			child.containerWindowID,
			child.scopeFingerprint,
			child.inventoryScopeKey,
			child.actualStartMS,
		)
		if err != nil {
			return err
		}
		if !found {
			if _, err := tx.ExecContext(ctx, `update account_quota_cycles
				set parent_cycle_id = null where id = ?`, child.id); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `update account_quota_cycles
			set parent_cycle_id = ? where id = ?`, parentCycleID, child.id); err != nil {
			return err
		}
	}
	return nil
}

func resolveContainerCycleID(
	ctx context.Context,
	tx *sql.Tx,
	accountKey, provider, providerWindowID, scopeFingerprint, inventoryScopeKey string,
	childStartMS int64,
) (int64, bool, error) {
	var parentCycleID int64
	err := tx.QueryRowContext(ctx, `select c.id
		from account_quota_windows w
		join account_quota_window_activations a
			on a.window_id = w.id and a.deactivated_at_ms is null
		join account_quota_cycles c
			on c.activation_id = a.id
		where w.account_key = ? and w.provider = ? and w.provider_window_id = ?
			and w.scope_fingerprint = ? and w.inventory_scope_key = ?
			and w.availability <> 'inactive'
			and c.actual_start_ms <= ?
			and ((c.actual_end_ms is null and (c.scheduled_end_ms is null or ? < c.scheduled_end_ms))
				or (c.actual_end_ms is not null and ? < c.actual_end_ms))
		order by c.actual_start_ms desc, c.id desc limit 1`,
		accountKey,
		provider,
		providerWindowID,
		scopeFingerprint,
		inventoryScopeKey,
		childStartMS,
		childStartMS,
		childStartMS,
	).Scan(&parentCycleID)
	if err == nil {
		return parentCycleID, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	return 0, false, nil
}

func (r *repository) ListWindowStates(ctx context.Context, accountKey, provider string) ([]model.AccountQuotaWindowState, error) {
	return r.listWindowStates(ctx, accountKey, provider, false)
}

func (r *repository) ListLegacyCodexWorkspaceWindowStates(
	ctx context.Context,
	accountKey, provider string,
) ([]model.AccountQuotaWindowState, error) {
	legacyPrefix := "usage-account-history:" + usageidentity.FormatVersion + ":codex-account:"
	if !strings.EqualFold(strings.TrimSpace(provider), "codex") || !strings.HasPrefix(strings.TrimSpace(accountKey), legacyPrefix) {
		return []model.AccountQuotaWindowState{}, nil
	}
	return r.listWindowStates(ctx, accountKey, provider, true)
}

func (r *repository) listWindowStates(
	ctx context.Context,
	accountKey, provider string,
	includeLegacyCodexWorkspace bool,
) ([]model.AccountQuotaWindowState, error) {
	legacyFilter := `and ` + excludeLegacyCodexWorkspaceSnapshotSQL("")
	if includeLegacyCodexWorkspace {
		legacyFilter = ""
	}
	rows, err := r.db.QueryContext(ctx, `select
		id, account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, coalesce(model_scope_key, ''), coalesce(model_ids_json, ''),
		scope_fingerprint, inventory_scope_key, coalesce(relationship_kind, ''),
		coalesce(container_provider_window_id, ''), availability, generation,
		first_seen_at_ms, last_seen_at_ms, missing_since_ms, deactivated_at_ms
		from account_quota_windows where account_key = ? and provider = ?
			`+legacyFilter+`
		order by updated_at_ms desc, id desc`, strings.TrimSpace(accountKey), strings.TrimSpace(provider))
	if err != nil {
		return nil, err
	}
	states := make([]model.AccountQuotaWindowState, 0)
	for rows.Next() {
		var state model.AccountQuotaWindowState
		var missingSince, deactivatedAt sql.NullInt64
		if err := rows.Scan(
			&state.ID, &state.AccountKey, &state.Provider, &state.ProviderWindowID,
			&state.WindowKind, &state.WindowMode, &state.ModelScopeKind,
			&state.ModelScopeKey, &state.ModelIDsJSON, &state.ScopeFingerprint,
			&state.InventoryScopeKey, &state.RelationshipKind,
			&state.ContainerProviderWindowID, &state.Availability, &state.Generation,
			&state.FirstSeenAtMS, &state.LastSeenAtMS, &missingSince, &deactivatedAt,
		); err != nil {
			return nil, err
		}
		state.MissingSinceMS = int64Pointer(missingSince)
		state.DeactivatedAtMS = int64Pointer(deactivatedAt)
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	cycleAliases := make(map[int64]int64)
	for index := range states {
		state := &states[index]
		activationID, err := latestActivationID(ctx, r.db, state.ID, state.Generation)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			state.ActivationID = activationID
			current, currentErr := activeCycle(ctx, r.db, activationID)
			if currentErr != nil && !errors.Is(currentErr, sql.ErrNoRows) {
				return nil, currentErr
			}
			if currentErr == nil {
				normalizedCurrent, normalizedPrevious, aliases, normalizeErr := normalizeCycleView(ctx, r.db, activationID, current)
				if normalizeErr != nil {
					return nil, normalizeErr
				}
				for cycleID, canonicalID := range aliases {
					cycleAliases[cycleID] = canonicalID
				}
				state.CurrentCycle = &normalizedCurrent
				state.PreviousCycle = normalizedPrevious
			}
		}
	}
	for index := range states {
		applyCycleParentAliases(states[index].CurrentCycle, cycleAliases)
		applyCycleParentAliases(states[index].PreviousCycle, cycleAliases)
	}
	return states, nil
}

func applyCycleParentAliases(cycle *model.AccountQuotaCycle, aliases map[int64]int64) {
	if cycle == nil || cycle.ParentCycleID == nil {
		return
	}
	canonicalID, ok := aliases[*cycle.ParentCycleID]
	if !ok {
		return
	}
	cycle.ParentCycleID = &canonicalID
}

func latestActivationID(ctx context.Context, db *sql.DB, windowID int64, generation int) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `select id from account_quota_window_activations
		where window_id = ? and generation = ? limit 1`, windowID, generation).Scan(&id)
	return id, err
}

// adjacentPreviousCycle returns the cycle immediately preceding currentStartMS.
// The jitter bound is intentional: this helper is used only while normalizing
// early-reset fragments, where time adjacency is part of the collapse proof.
func adjacentPreviousCycle(ctx context.Context, db *sql.DB, activationID, currentStartMS int64) (model.AccountQuotaCycle, error) {
	return scanCycle(db.QueryRowContext(ctx, `select
		id, activation_id, provider_cycle_key, state, scheduled_start_ms, scheduled_end_ms,
		actual_start_ms, actual_end_ms, duration_seconds, boundary_accuracy,
		coalesce(end_reason, ''), first_observation_id, last_observation_id, parent_cycle_id,
		created_at_ms, updated_at_ms
		from account_quota_cycles
		where activation_id = ? and actual_end_ms is not null
			and actual_start_ms < ? and abs(actual_end_ms - ?) <= ?
		order by actual_end_ms desc, id desc limit 1`,
		activationID, currentStartMS, currentStartMS, quotaBoundaryJitterMS))
}

// latestClosedCycleBefore returns the most recent completed quota cycle in the
// same activation before cutoffMS. Unlike adjacentPreviousCycle, it intentionally
// permits observation gaps; activation identity provides the lifecycle fence.
// A mode change is an explicit boundary between fixed/calendar cycle histories,
// so candidates before that boundary are not exposed as a previous quota cycle.
func latestClosedCycleBefore(ctx context.Context, db *sql.DB, activationID, cutoffMS int64) (model.AccountQuotaCycle, error) {
	overlapCutoffMS := cutoffMS
	if overlapCutoffMS > math.MaxInt64-quotaPreviousCycleOverlapJitterMS {
		overlapCutoffMS = math.MaxInt64
	} else {
		overlapCutoffMS += quotaPreviousCycleOverlapJitterMS
	}
	return scanCycle(db.QueryRowContext(ctx, `select
		candidate.id, candidate.activation_id, candidate.provider_cycle_key, candidate.state,
		candidate.scheduled_start_ms, candidate.scheduled_end_ms, candidate.actual_start_ms,
		candidate.actual_end_ms, candidate.duration_seconds, candidate.boundary_accuracy,
		coalesce(candidate.end_reason, ''), candidate.first_observation_id,
		candidate.last_observation_id, candidate.parent_cycle_id,
		candidate.created_at_ms, candidate.updated_at_ms
		from account_quota_cycles candidate
		where candidate.activation_id = ? and candidate.actual_end_ms is not null
			and coalesce(candidate.end_reason, '') <> 'mode_changed'
			and candidate.actual_start_ms < ?
			and candidate.actual_end_ms <= ?
			and not exists (
				select 1 from account_quota_cycles barrier
				where barrier.activation_id = candidate.activation_id
					and barrier.end_reason = 'mode_changed'
					and barrier.actual_end_ms is not null
					and barrier.actual_end_ms >= candidate.actual_start_ms
					and barrier.actual_end_ms <= ?
			)
		order by candidate.actual_end_ms desc, candidate.id desc limit 1`,
		activationID, cutoffMS, overlapCutoffMS, cutoffMS))
}

type quotaCycleEvidence struct {
	firstObservedAtMS  int64
	firstUsedPercent   *float64
	lastUsedPercent    *float64
	confirmed          bool
	confirmedAtMS      int64
	onlyProvisionalAPI bool
}

func normalizeCycleView(
	ctx context.Context,
	db *sql.DB,
	activationID int64,
	current model.AccountQuotaCycle,
) (model.AccountQuotaCycle, *model.AccountQuotaCycle, map[int64]int64, error) {
	currentOnlyProvisionalAPI, err := cycleHasOnlyProvisionalZeroAPIBoundaries(ctx, db, current.ID)
	if err != nil {
		return model.AccountQuotaCycle{}, nil, nil, err
	}

	canonical := current
	canonicalEvidence := quotaCycleEvidence{onlyProvisionalAPI: currentOnlyProvisionalAPI}
	next := current
	var nextEvidence quotaCycleEvidence
	evidenceLoaded := false
	lookupStartMS := current.ActualStartMS
	collapsed := false
	collapsedCycleIDs := []int64{current.ID}
	var previousResult *model.AccountQuotaCycle
	for {
		previous, previousErr := adjacentPreviousCycle(ctx, db, activationID, lookupStartMS)
		if errors.Is(previousErr, sql.ErrNoRows) {
			break
		}
		if previousErr != nil {
			return model.AccountQuotaCycle{}, nil, nil, previousErr
		}
		if !structurallyCollapsibleEarlyResetFragment(previous, next) {
			previousResult = &previous
			break
		}
		if !evidenceLoaded {
			currentEvidence, evidenceErr := loadQuotaCycleEvidence(ctx, db, current.ID)
			if evidenceErr != nil {
				return model.AccountQuotaCycle{}, nil, nil, evidenceErr
			}
			canonicalEvidence = currentEvidence
			nextEvidence = currentEvidence
			evidenceLoaded = true
		}
		previousEvidence, evidenceErr := loadQuotaCycleEvidence(ctx, db, previous.ID)
		if evidenceErr != nil {
			return model.AccountQuotaCycle{}, nil, nil, evidenceErr
		}
		if !collapsibleEarlyResetFragment(previous, next, previousEvidence, nextEvidence) {
			previousResult = &previous
			break
		}
		collapsed = true
		collapsedCycleIDs = append(collapsedCycleIDs, previous.ID)
		if quotaCycleEvidencePreferred(previous, previousEvidence, canonical, canonicalEvidence) {
			canonical = previous
			canonicalEvidence = previousEvidence
		}
		lookupStartMS = previous.ActualStartMS
		next = previous
		nextEvidence = previousEvidence
	}

	if canonical.ID != current.ID {
		current.ID = canonical.ID
		current.ActivationID = canonical.ActivationID
		current.ProviderCycleKey = canonical.ProviderCycleKey
		current.ScheduledStartMS = copyInt64(canonical.ScheduledStartMS)
		current.ScheduledEndMS = copyInt64(canonical.ScheduledEndMS)
		current.ActualStartMS = canonical.ActualStartMS
		current.DurationSeconds = copyInt64(canonical.DurationSeconds)
		current.BoundaryAccuracy = canonical.BoundaryAccuracy
		current.FirstObservationID = copyInt64(canonical.FirstObservationID)
		current.ParentCycleID = copyInt64(canonical.ParentCycleID)
		current.CreatedAtMS = canonical.CreatedAtMS
	}
	if canonicalEvidence.onlyProvisionalAPI {
		current.State = "provisional"
		current.BoundaryAccuracy = "unknown"
	}
	if previousResult != nil && previousResult.EndReason == "early_reset" &&
		canonicalEvidence.confirmedAtMS > 0 && canonicalEvidence.confirmedAtMS < current.ActualStartMS {
		current.ActualStartMS = canonicalEvidence.confirmedAtMS
		actualEndMS := canonicalEvidence.confirmedAtMS
		previousResult.ActualEndMS = &actualEndMS
	}
	if collapsed && previousResult != nil && previousResult.ActualEndMS != nil {
		actualEndMS := current.ActualStartMS
		previousResult.ActualEndMS = &actualEndMS
	}
	if previousResult != nil && previousResult.EndReason == "mode_changed" {
		previousResult = nil
	}
	if previousResult == nil {
		fallback, fallbackErr := latestClosedCycleBefore(ctx, db, activationID, lookupStartMS)
		if fallbackErr != nil && !errors.Is(fallbackErr, sql.ErrNoRows) {
			return model.AccountQuotaCycle{}, nil, nil, fallbackErr
		}
		if fallbackErr == nil {
			previousResult = &fallback
		}
	}
	aliases := make(map[int64]int64)
	if collapsed {
		for _, cycleID := range collapsedCycleIDs {
			if cycleID != canonical.ID {
				aliases[cycleID] = canonical.ID
			}
		}
	}
	return current, previousResult, aliases, nil
}

func cycleHasOnlyProvisionalZeroAPIBoundaries(ctx context.Context, db *sql.DB, cycleID int64) (bool, error) {
	var hasSnapshots, hasNonProvisional, hasScheduledPredecessor int
	err := db.QueryRowContext(ctx, `select
		exists(select 1 from account_quota_snapshots where cycle_id = ? limit 1),
		exists(select 1 from account_quota_snapshots where cycle_id = ? and coalesce((
			provider = 'codex' and source = 'api_query' and window_mode = 'fixed'
			and boundary_accuracy in ('exact', 'derived') and used_percent = 0
			and cycle_start_ms is not null and cycle_end_ms is not null
			and duration_seconds is not null
			and abs(cycle_start_ms - observed_at_ms) <= ?
		), 0) = 0 limit 1),
		exists(select 1
			from account_quota_cycles current
			join account_quota_cycles previous
				on previous.activation_id = current.activation_id
				and previous.actual_start_ms < current.actual_start_ms
			where current.id = ? and previous.end_reason = 'scheduled'
				and previous.actual_end_ms is not null
				and abs(previous.actual_end_ms - current.actual_start_ms) <= ?
			limit 1)`,
		cycleID,
		cycleID,
		quotaProvisionalStartJitterMS,
		cycleID,
		quotaBoundaryJitterMS,
	).Scan(&hasSnapshots, &hasNonProvisional, &hasScheduledPredecessor)
	if err != nil {
		return false, err
	}
	return hasSnapshots != 0 && hasNonProvisional == 0 && hasScheduledPredecessor == 0, nil
}

func loadQuotaCycleEvidence(ctx context.Context, db *sql.DB, cycleID int64) (quotaCycleEvidence, error) {
	rows, err := db.QueryContext(ctx, `select
		provider, source, window_mode, boundary_accuracy, cycle_start_ms, cycle_end_ms,
		duration_seconds, used_percent, observed_at_ms
		from account_quota_snapshots where cycle_id = ?
		order by observed_at_ms asc, id asc`, cycleID)
	if err != nil {
		return quotaCycleEvidence{}, err
	}
	defer func() { _ = rows.Close() }()
	evidence := quotaCycleEvidence{onlyProvisionalAPI: true}
	total := 0
	for rows.Next() {
		var snapshot model.AccountQuotaSnapshot
		var cycleStart, cycleEnd, duration sql.NullInt64
		var used sql.NullFloat64
		if err := rows.Scan(
			&snapshot.Provider,
			&snapshot.Source,
			&snapshot.WindowMode,
			&snapshot.BoundaryAccuracy,
			&cycleStart,
			&cycleEnd,
			&duration,
			&used,
			&snapshot.ObservedAtMS,
		); err != nil {
			return quotaCycleEvidence{}, err
		}
		snapshot.CycleStartMS = int64Pointer(cycleStart)
		snapshot.CycleEndMS = int64Pointer(cycleEnd)
		snapshot.DurationSeconds = int64Pointer(duration)
		if used.Valid {
			value := used.Float64
			snapshot.UsedPercent = &value
			if evidence.firstUsedPercent == nil {
				evidence.firstUsedPercent = copyFloat64(snapshot.UsedPercent)
			}
			evidence.lastUsedPercent = copyFloat64(snapshot.UsedPercent)
		}
		if total == 0 {
			evidence.firstObservedAtMS = snapshot.ObservedAtMS
		}
		total++
		provisional := isProvisionalZeroAPIBoundary(snapshot)
		evidence.onlyProvisionalAPI = evidence.onlyProvisionalAPI && provisional
		if isConfirmedLifecycleBoundary(snapshot) {
			evidence.confirmed = true
			if evidence.confirmedAtMS == 0 || snapshot.ObservedAtMS < evidence.confirmedAtMS {
				evidence.confirmedAtMS = snapshot.ObservedAtMS
			}
		}
	}
	if err := rows.Err(); err != nil {
		return quotaCycleEvidence{}, err
	}
	evidence.onlyProvisionalAPI = total > 0 && evidence.onlyProvisionalAPI
	return evidence, nil
}

func collapsibleEarlyResetFragment(
	previous model.AccountQuotaCycle,
	next model.AccountQuotaCycle,
	previousEvidence quotaCycleEvidence,
	nextEvidence quotaCycleEvidence,
) bool {
	if !structurallyCollapsibleEarlyResetFragment(previous, next) ||
		previousEvidence.firstObservedAtMS == 0 || nextEvidence.firstObservedAtMS == 0 ||
		previousEvidence.lastUsedPercent == nil || nextEvidence.firstUsedPercent == nil {
		return false
	}
	startShiftMS := *next.ScheduledStartMS - *previous.ScheduledStartMS
	endShiftMS := *next.ScheduledEndMS - *previous.ScheduledEndMS
	observationShiftMS := nextEvidence.firstObservedAtMS - previousEvidence.firstObservedAtMS
	if absInt64(startShiftMS-endShiftMS) > quotaBoundaryJitterMS ||
		absInt64(startShiftMS-observationShiftMS) > quotaBoundaryJitterMS {
		return false
	}
	return !quotaCounterResetValues(*previousEvidence.lastUsedPercent, *nextEvidence.firstUsedPercent) &&
		!quotaLowUsageBoundaryResetValues(
			*previousEvidence.lastUsedPercent,
			*nextEvidence.firstUsedPercent,
			previous.ScheduledStartMS,
			previous.ScheduledEndMS,
			previous.DurationSeconds,
			previous.BoundaryAccuracy,
			next.ScheduledStartMS,
			next.ScheduledEndMS,
			next.DurationSeconds,
			next.BoundaryAccuracy,
		)
}

func structurallyCollapsibleEarlyResetFragment(previous, next model.AccountQuotaCycle) bool {
	return previous.EndReason == "early_reset" && previous.ActualEndMS != nil &&
		absInt64(*previous.ActualEndMS-next.ActualStartMS) <= quotaBoundaryJitterMS &&
		previous.ScheduledStartMS != nil && previous.ScheduledEndMS != nil &&
		next.ScheduledStartMS != nil && next.ScheduledEndMS != nil &&
		previous.DurationSeconds != nil && next.DurationSeconds != nil &&
		*previous.DurationSeconds == *next.DurationSeconds &&
		next.ActualStartMS < *previous.ScheduledEndMS
}

func quotaCycleEvidencePreferred(
	candidate model.AccountQuotaCycle,
	candidateEvidence quotaCycleEvidence,
	current model.AccountQuotaCycle,
	currentEvidence quotaCycleEvidence,
) bool {
	if candidateEvidence.confirmed != currentEvidence.confirmed {
		return candidateEvidence.confirmed
	}
	if candidateEvidence.onlyProvisionalAPI != currentEvidence.onlyProvisionalAPI {
		return !candidateEvidence.onlyProvisionalAPI
	}
	candidateAccuracy := boundaryAccuracyValue(candidate.BoundaryAccuracy)
	currentAccuracy := boundaryAccuracyValue(current.BoundaryAccuracy)
	if candidateAccuracy != currentAccuracy {
		return candidateAccuracy > currentAccuracy
	}
	if candidateEvidence.confirmedAtMS != currentEvidence.confirmedAtMS {
		if candidateEvidence.confirmedAtMS == 0 {
			return false
		}
		if currentEvidence.confirmedAtMS == 0 {
			return true
		}
		return candidateEvidence.confirmedAtMS < currentEvidence.confirmedAtMS
	}
	return candidate.ActualStartMS < current.ActualStartMS
}

func copyFloat64(value *float64) *float64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func activationStart(snapshot model.AccountQuotaSnapshot, fallback int64) int64 {
	if snapshot.CycleStartMS != nil && *snapshot.CycleStartMS > 0 {
		return *snapshot.CycleStartMS
	}
	return fallback
}

func activationAccuracy(snapshot model.AccountQuotaSnapshot) string {
	if snapshot.CycleStartMS == nil {
		return "estimated"
	}
	return snapshot.BoundaryAccuracy
}

func cycleMatchesSnapshot(cycle model.AccountQuotaCycle, snapshot model.AccountQuotaSnapshot) bool {
	if cycle.ScheduledEndMS == nil || cycle.DurationSeconds == nil || snapshot.CycleEndMS == nil || snapshot.DurationSeconds == nil {
		return false
	}
	return *cycle.DurationSeconds == *snapshot.DurationSeconds &&
		absInt64(*cycle.ScheduledEndMS-*snapshot.CycleEndMS) <= quotaBoundaryJitterMS
}

func preferredAccuracy(current, next string) string {
	rank := func(value string) int {
		switch value {
		case "exact":
			return 3
		case "derived":
			return 2
		case "estimated":
			return 1
		default:
			return 0
		}
	}
	if rank(next) > rank(current) {
		return next
	}
	return current
}

func providerCycleKey(snapshot model.AccountQuotaSnapshot) string {
	return fmt.Sprintf("%d:%d:%d", *snapshot.CycleStartMS, *snapshot.CycleEndMS, *snapshot.DurationSeconds)
}

func windowIdentity(providerWindowID, scopeFingerprint string) string {
	return providerWindowID + "\x00" + scopeFingerprint
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
