package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	quotasnapshotrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
)

const (
	DerivedMaintenanceReasonDeferredIndexes     = "deferred_indexes"
	DerivedMaintenanceReasonOfflineDerived      = "offline_derived_cleanup"
	DerivedMaintenanceReasonOfflineQuota        = "offline_quota_snapshot_migration"
	DerivedMaintenanceReasonLegacyIndexReplaced = "legacy_index_replacement"
	DerivedMaintenanceCommand                   = "cleanup-derived"
)

// DerivedMaintenanceStatus is the bounded, user-facing summary of derived
// database work that still needs offline finalization. It intentionally omits
// database paths, SQL, index names, and migration error details.
type DerivedMaintenanceStatus struct {
	Required            bool     `json:"required"`
	PerformanceDegraded bool     `json:"performanceDegraded"`
	DeferredIndexes     int      `json:"deferredIndexes"`
	OfflineJobs         int      `json:"offlineJobs"`
	Reasons             []string `json:"reasons"`
	Command             string   `json:"command,omitempty"`
}

// ReadDerivedMaintenanceStatus inspects only SQLite schema metadata, bounded
// existence probes for unrecorded missing indexes, and small maintenance
// metadata tables. It never creates, drops, or rebuilds derived data.
func ReadDerivedMaintenanceStatus(ctx context.Context, db *sql.DB) (DerivedMaintenanceStatus, error) {
	status := DerivedMaintenanceStatus{Reasons: []string{}}
	if db == nil {
		return status, nil
	}

	tables, indexes, err := readDerivedMaintenanceSchema(ctx, db)
	if err != nil {
		return status, err
	}

	pendingIndexes := make(map[string]struct{})
	legacyReplacementPending := false
	deferredIndexReasons, err := readDeferredDerivedIndexes(ctx, db, tables)
	if err != nil {
		return status, err
	}
	for indexName, entry := range deferredIndexReasons {
		// A derived table can be parked or removed by a later schema recovery.
		// Its old ledger row no longer represents work that can be performed, so
		// do not keep reporting a degraded state for a target absent from schema.
		if !tables[entry.tableName] {
			continue
		}
		if indexedTable, exists := indexes[indexName]; exists && indexedTable == entry.tableName {
			// The index was created by another process or a previous cleanup run.
			// Ignore the stale ledger row; status is derived from actual metadata.
			continue
		}
		pendingIndexes[indexName] = struct{}{}
		if entry.reason == DerivedMaintenanceReasonLegacyIndexReplaced {
			legacyReplacementPending = true
		}
	}
	tableHasRows := make(map[string]bool)
	tableChecked := make(map[string]bool)

	for _, index := range derivedIndexStatements {
		if entry, recorded := deferredIndexReasons[index.name]; recorded && entry.tableName == index.tableName {
			continue
		}
		indexedTable, indexExists := indexes[index.name]
		if indexExists && indexedTable == index.tableName {
			continue
		}

		if indexExists {
			pendingIndexes[index.name] = struct{}{}
			legacyReplacementPending = true
			continue
		}

		if !tables[index.tableName] {
			continue
		}

		if !tableChecked[index.tableName] {
			hasRows, probeErr := derivedIndexTableHasRows(ctx, db, index.tableName)
			if probeErr != nil {
				return status, fmt.Errorf("inspect derived maintenance target %s: %w", index.tableName, probeErr)
			}
			tableHasRows[index.tableName] = hasRows
			tableChecked[index.tableName] = true
		}
		if tableHasRows[index.tableName] {
			pendingIndexes[index.name] = struct{}{}
		}
	}

	// The two legacy identity indexes are handled by ensureLegacyDerivedIndexReplaced.
	// A replacement can remain pending even when its target table is empty, so
	// inspect these pairs separately from the normal table-row rule above.
	for _, replacement := range []struct {
		tableName       string
		legacyIndexName string
		replacementName string
	}{
		{
			tableName:       "account_action_candidates",
			legacyIndexName: "idx_account_action_candidates_pending_file_action",
			replacementName: "idx_account_action_candidates_pending_identity_action",
		},
		{
			tableName:       "quota_cooldowns",
			legacyIndexName: "idx_quota_cooldowns_active_owner",
			replacementName: "idx_quota_cooldowns_active_identity",
		},
	} {
		legacyTable, legacyExists := indexes[replacement.legacyIndexName]
		replacementTable, replacementExists := indexes[replacement.replacementName]
		if !legacyExists || legacyTable != replacement.tableName {
			continue
		}
		if !replacementExists || replacementTable != replacement.tableName {
			pendingIndexes[replacement.replacementName] = struct{}{}
			legacyReplacementPending = true
		}
	}

	status.DeferredIndexes = len(pendingIndexes)
	offlineDerivedJobs := 0
	if tables["usage_derived_cleanup_jobs"] {
		if err := db.QueryRowContext(ctx, `select count(*)
			from usage_derived_cleanup_jobs
			where status = 'offline_required'`).Scan(&offlineDerivedJobs); err != nil {
			return status, fmt.Errorf("inspect offline derived cleanup jobs: %w", err)
		}
	}
	offlineQuotaJobs, err := readOfflineQuotaSnapshotJobs(ctx, db, tables)
	if err != nil {
		return status, err
	}
	status.OfflineJobs = offlineDerivedJobs + offlineQuotaJobs

	if status.DeferredIndexes > 0 {
		status.Reasons = append(status.Reasons, DerivedMaintenanceReasonDeferredIndexes)
	}
	if offlineDerivedJobs > 0 {
		status.Reasons = append(status.Reasons, DerivedMaintenanceReasonOfflineDerived)
	}
	if offlineQuotaJobs > 0 {
		status.Reasons = append(status.Reasons, DerivedMaintenanceReasonOfflineQuota)
	}
	if legacyReplacementPending {
		status.Reasons = append(status.Reasons, DerivedMaintenanceReasonLegacyIndexReplaced)
	}
	status.Required = status.DeferredIndexes > 0 || status.OfflineJobs > 0
	status.PerformanceDegraded = status.Required
	if status.Required {
		status.Command = DerivedMaintenanceCommand
	}
	return status, nil
}

type deferredDerivedIndexEntry struct {
	tableName string
	reason    string
}

func readDeferredDerivedIndexes(ctx context.Context, db *sql.DB, tables map[string]bool) (map[string]deferredDerivedIndexEntry, error) {
	entries := make(map[string]deferredDerivedIndexEntry)
	if !tables[derivedDeferredIndexesTable] {
		return entries, nil
	}
	rows, err := db.QueryContext(ctx, `select index_name, table_name, reason
		from `+derivedDeferredIndexesTable)
	if err != nil {
		return nil, fmt.Errorf("inspect deferred derived index ledger: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var indexName, tableName, reason string
		if err := rows.Scan(&indexName, &tableName, &reason); err != nil {
			return nil, fmt.Errorf("read deferred derived index ledger: %w", err)
		}
		entries[indexName] = deferredDerivedIndexEntry{tableName: tableName, reason: reason}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate deferred derived index ledger: %w", err)
	}
	return entries, nil
}

func readOfflineQuotaSnapshotJobs(ctx context.Context, db *sql.DB, tables map[string]bool) (int, error) {
	if !tables["usage_data_migrations"] {
		return 0, nil
	}
	var jobs int
	if err := db.QueryRowContext(ctx, `select count(*) from usage_data_migrations
		where name = ? and (
			status = 'offline_required' or
			(status = 'failed' and instr(coalesce(last_error, ''), ?) > 0)
		)`, quotasnapshotrepo.LegacySnapshotMigrationName, quotasnapshotrepo.LegacySnapshotOfflineErrorMarker).Scan(&jobs); err != nil {
		return 0, fmt.Errorf("inspect offline quota snapshot migration: %w", err)
	}
	return jobs, nil
}

func readDerivedMaintenanceSchema(ctx context.Context, db *sql.DB) (map[string]bool, map[string]string, error) {
	tables := make(map[string]bool)
	indexes := make(map[string]string)
	rows, err := db.QueryContext(ctx, `select type, name, tbl_name
		from sqlite_master
		where type in ('table', 'index')`)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect derived maintenance schema: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var objectType, name, tableName string
		if err := rows.Scan(&objectType, &name, &tableName); err != nil {
			return nil, nil, fmt.Errorf("read derived maintenance schema: %w", err)
		}
		switch objectType {
		case "table":
			tables[name] = true
		case "index":
			indexes[name] = tableName
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate derived maintenance schema: %w", err)
	}
	return tables, indexes, nil
}
