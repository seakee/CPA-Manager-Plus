package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
)

const corruptQuarantineSuffix = "_corrupt_quarantine_"

// integrityCoreTables hold rows that cannot be recomputed from anything else in
// the database. Corruption here is reported and left untouched because only an
// operator can choose between restoring a backup and accepting the loss.
var integrityCoreTables = []string{
	"usage_events",
	"settings",
}

// integrityDerivedTables are recomputable from usage_events. Migrate already
// treats their absence as damage and reschedules a full rebuild, so renaming a
// corrupt one out of the way is enough to bring the derivation back.
var integrityDerivedTables = []string{
	usageMonitoringAccountDailyTable,
	usageMonitoringAPIKeyDailyTable,
	usageMonitoringSelectorDailyTable,
	usageMonitoringHeaderLatestTable,
	usageprojection.EventTable,
	usageprojection.SearchIndexTable,
	usageAccountModelRollupsTable,
}

// corruptionErrorMarkers match the SQLite messages that mean stored pages no
// longer decode. SQLITE_CANTOPEN is deliberately absent: it describes the whole
// file, not one table, so it must not be attributed to a derived table.
var corruptionErrorMarkers = []string{
	"database disk image is malformed",
	"malformed database schema",
	"file is not a database",
	"invalid fts5 file format",
}

// CoreCorruptionError reports corruption in a table the server cannot rebuild.
// Startup fails with it so the operator sees one actionable message instead of
// every worker logging the same unusable-database error forever.
type CoreCorruptionError struct {
	Tables []string
}

func (e *CoreCorruptionError) Error() string {
	return fmt.Sprintf(
		"SQLite integrity check failed for non-recomputable tables %s; restore a backup or move the database aside before restarting",
		strings.Join(e.Tables, ", "),
	)
}

// IntegrityReport separates corruption the server can repair by rebuilding from
// corruption that needs an operator.
type IntegrityReport struct {
	CorruptCore    []string
	CorruptDerived []string
}

func (r IntegrityReport) Healthy() bool {
	return len(r.CorruptCore) == 0 && len(r.CorruptDerived) == 0
}

// InspectIntegrity runs a table-scoped integrity check over the core and
// derived tables. The check is scoped per table rather than run as a whole-file
// quick_check so the cost stays proportional to the tables that are actually
// classified here.
func InspectIntegrity(db *sql.DB) (IntegrityReport, error) {
	var report IntegrityReport
	if db == nil {
		return report, nil
	}
	for _, group := range []struct {
		tables  []string
		corrupt *[]string
	}{
		{integrityCoreTables, &report.CorruptCore},
		{integrityDerivedTables, &report.CorruptDerived},
	} {
		for _, tableName := range group.tables {
			var exists int
			if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`,
				tableName).Scan(&exists); err != nil {
				return IntegrityReport{}, fmt.Errorf("inspect integrity target %s: %w", tableName, err)
			}
			if exists == 0 {
				continue
			}
			corrupt, detail, err := tableIntegrityCorrupt(db, tableName)
			if err != nil {
				return IntegrityReport{}, err
			}
			if corrupt {
				log.Printf("[integrity] table %s failed integrity check: %s", tableName, detail)
				*group.corrupt = append(*group.corrupt, tableName)
			}
		}
	}
	return report, nil
}

func tableIntegrityCorrupt(db *sql.DB, tableName string) (bool, string, error) {
	rows, err := db.Query(`pragma integrity_check(` + tableName + `)`)
	if err != nil {
		if isCorruptionError(err) {
			return true, err.Error(), nil
		}
		return false, "", fmt.Errorf("integrity check %s: %w", tableName, err)
	}
	defer func() { _ = rows.Close() }()
	problems := make([]string, 0, 1)
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			if isCorruptionError(err) {
				return true, err.Error(), nil
			}
			return false, "", fmt.Errorf("scan integrity check %s: %w", tableName, err)
		}
		if !strings.EqualFold(strings.TrimSpace(line), "ok") {
			problems = append(problems, strings.TrimSpace(line))
		}
	}
	if err := rows.Err(); err != nil {
		if isCorruptionError(err) {
			return true, err.Error(), nil
		}
		return false, "", fmt.Errorf("integrity check %s: %w", tableName, err)
	}
	if len(problems) == 0 {
		return false, "", nil
	}
	return true, strings.Join(problems, "; "), nil
}

func isCorruptionError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range corruptionErrorMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// repairCorruptDerivedTables quarantines corrupt derived tables so the migration
// that runs next rebuilds them. A corrupt b-tree cannot be dropped or emptied,
// but it can be renamed, because renaming only rewrites the schema.
func repairCorruptDerivedTables(db *sql.DB) error {
	report, err := InspectIntegrity(db)
	if err != nil {
		return err
	}
	if len(report.CorruptCore) > 0 {
		return &CoreCorruptionError{Tables: report.CorruptCore}
	}
	if len(report.CorruptDerived) == 0 {
		return nil
	}
	log.Printf("[integrity] corrupt derived tables detected tables=%s; quarantining so the derivation is rebuilt",
		strings.Join(report.CorruptDerived, ","))
	quarantined, err := quarantineCorruptDerivedTables(db, report.CorruptDerived)
	if err != nil {
		return fmt.Errorf("quarantine corrupt derived tables: %w", err)
	}
	log.Printf("[integrity] quarantined corrupt derived tables as %s; their pages stay damaged, so drop them manually once the rebuild is verified",
		strings.Join(quarantined, ","))
	return nil
}

func quarantineCorruptDerivedTables(db *sql.DB, tables []string) ([]string, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if slices.Contains(tables, usageprojection.EventTable) ||
		slices.Contains(tables, usageprojection.SearchIndexTable) {
		// Renaming an FTS5 table rewrites the triggers that feed it, which would
		// keep writing into the quarantined copy. Drop them first;
		// ensureUsageMonitoringSearchIndex recreates them during Migrate.
		if err := dropUsageMonitoringSearchTriggers(tx); err != nil {
			return nil, err
		}
	}
	generation, err := nextCorruptQuarantineGeneration(tx)
	if err != nil {
		return nil, err
	}
	quarantined := make([]string, 0, len(tables))
	for _, tableName := range tables {
		quarantineName := fmt.Sprintf("%s%s%06d", tableName, corruptQuarantineSuffix, generation)
		if err := parkDerivedTable(tx, tableName, quarantineName); err != nil {
			return nil, err
		}
		quarantined = append(quarantined, quarantineName)
		if tableName == usageAccountModelRollupsTable {
			// Migrate recreates this table but only rewinds its checkpoint when
			// usage_events is missing, so an empty rollup would otherwise look
			// caught up forever.
			if err := resetAccountHistoryRollupProgress(tx); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return quarantined, nil
}

func resetAccountHistoryRollupProgress(tx *sql.Tx) error {
	var checkpointsExist int
	if err := tx.QueryRow(`select count(*) from sqlite_master where type = 'table'
		and name = 'usage_rollup_checkpoints'`).Scan(&checkpointsExist); err != nil {
		return fmt.Errorf("inspect account history rollup checkpoint: %w", err)
	}
	if checkpointsExist > 0 {
		if _, err := tx.Exec(`delete from usage_rollup_checkpoints where name = 'account_history'`); err != nil {
			return fmt.Errorf("reset account history rollup checkpoint: %w", err)
		}
	}
	return scheduleUsageRollupRebuild(tx, "account_history")
}

func nextCorruptQuarantineGeneration(tx *sql.Tx) (int, error) {
	for generation := 1; generation <= 999999; generation++ {
		var taken int
		if err := tx.QueryRow(`select count(*) from sqlite_master where type = 'table'
			and name glob ?`, fmt.Sprintf("*%s%06d", corruptQuarantineSuffix, generation)).Scan(&taken); err != nil {
			return 0, fmt.Errorf("inspect corrupt quarantine generation %d: %w", generation, err)
		}
		if taken == 0 {
			return generation, nil
		}
	}
	return 0, errors.New("exhausted corrupt quarantine generations")
}
