package sqlite

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// corruptibleRowPayload is wide enough that a few hundred rows spill onto their
// own b-tree pages at the end of the file, which is what lets the fixture damage
// one specific table.
var corruptibleRowPayload = strings.Repeat("abcdefghij", 40)

// writeCorruptibleDatabase migrates a database, lets the caller grow one table,
// then overwrites the trailing pages so that table's b-tree no longer decodes.
func writeCorruptibleDatabase(t *testing.T, grow func(*sql.DB)) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms)
		values ('seed-event', 1, '2026-01-01T00:00:00Z', 'seed-model', 1)`); err != nil {
		t.Fatalf("seed usage event: %v", err)
	}
	grow(db)
	var pageSize, pageCount int64
	if err := db.QueryRow(`pragma page_size`).Scan(&pageSize); err != nil {
		t.Fatalf("read page size: %v", err)
	}
	if err := db.QueryRow(`pragma page_count`).Scan(&pageCount); err != nil {
		t.Fatalf("read page count: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	file, err := os.OpenFile(dbPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open sqlite file: %v", err)
	}
	garbage := make([]byte, pageSize)
	for index := range garbage {
		garbage[index] = 0xA5
	}
	for offset := int64(1); offset <= 4; offset++ {
		if _, err := file.WriteAt(garbage, (pageCount-offset-1)*pageSize); err != nil {
			t.Fatalf("corrupt page: %v", err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close sqlite file: %v", err)
	}
	return dbPath
}

func growAccountModelRollups(t *testing.T, db *sql.DB) {
	t.Helper()
	for row := 0; row < 500; row++ {
		if _, err := db.Exec(`insert into usage_account_model_rollups (
			account_key, account_snapshot, model, billing_model, service_tier,
			first_seen_ms, last_seen_ms, updated_at_ms
		) values (?, ?, ?, 'billing', 'default', 1, 1, 1)`,
			corruptibleRowPayload+strconv.Itoa(row),
			corruptibleRowPayload,
			corruptibleRowPayload,
		); err != nil {
			t.Fatalf("grow account model rollups: %v", err)
		}
	}
}

func growUsageEvents(t *testing.T, db *sql.DB) {
	t.Helper()
	for row := 0; row < 500; row++ {
		if _, err := db.Exec(`insert into usage_events (
			event_hash, timestamp_ms, timestamp, model, account_snapshot, created_at_ms
		) values (?, ?, '2026-01-01T00:00:00Z', ?, ?, 1)`,
			corruptibleRowPayload+strconv.Itoa(row),
			row+2,
			corruptibleRowPayload,
			corruptibleRowPayload,
		); err != nil {
			t.Fatalf("grow usage events: %v", err)
		}
	}
}

func TestInspectIntegrityReportsHealthyDatabase(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	report, err := InspectIntegrity(db)
	if err != nil {
		t.Fatalf("inspect integrity: %v", err)
	}
	if !report.Healthy() {
		t.Fatalf("report = %+v, want healthy", report)
	}
}

func TestInspectIntegrityFlagsCorruptDerivedTableOnly(t *testing.T) {
	dbPath := writeCorruptibleDatabase(t, func(db *sql.DB) {
		growAccountModelRollups(t, db)
	})
	db, err := sql.Open("sqlite", dataSourceName(dbPath))
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	report, err := InspectIntegrity(db)
	if err != nil {
		t.Fatalf("inspect integrity: %v", err)
	}
	if !slices.Contains(report.CorruptDerived, usageAccountModelRollupsTable) {
		t.Fatalf("corrupt derived = %v, want it to contain %s", report.CorruptDerived, usageAccountModelRollupsTable)
	}
	// Corruption confined to a recomputable table must never be escalated to the
	// operator-only path, otherwise startup would fail on a repairable database.
	if len(report.CorruptCore) != 0 {
		t.Fatalf("corrupt core = %v, want none", report.CorruptCore)
	}
}

func TestOpenWithOptionsLeavesCorruptDerivedTableUntouchedByDefault(t *testing.T) {
	dbPath := writeCorruptibleDatabase(t, func(db *sql.DB) {
		growAccountModelRollups(t, db)
	})
	db, err := OpenWithOptions(Options{Path: dbPath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// The repair is opt-in, so an untouched deployment must keep behaving exactly
	// as it did before this change: the corrupt table still fails to read.
	var count int64
	err = db.QueryRow(`select count(*) from ` + usageAccountModelRollupsTable).Scan(&count)
	if !isCorruptionError(err) {
		t.Fatalf("read corrupt rollups = %v, want a corruption error", err)
	}
	var quarantined int
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table'
		and name glob ?`, "*"+corruptQuarantineSuffix+"*").Scan(&quarantined); err != nil {
		t.Fatalf("inspect quarantine tables: %v", err)
	}
	if quarantined != 0 {
		t.Fatalf("quarantined tables = %d, want 0", quarantined)
	}
}

func TestOpenWithOptionsQuarantinesCorruptDerivedTableAndSchedulesRebuild(t *testing.T) {
	dbPath := writeCorruptibleDatabase(t, func(db *sql.DB) {
		growAccountModelRollups(t, db)
	})
	db, err := OpenWithOptions(Options{Path: dbPath, RepairCorruptDerived: true})
	if err != nil {
		t.Fatalf("open sqlite with repair: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var count int64
	if err := db.QueryRow(`select count(*) from ` + usageAccountModelRollupsTable).Scan(&count); err != nil {
		t.Fatalf("read rebuilt rollups: %v", err)
	}
	if count != 0 {
		t.Fatalf("rebuilt rollup rows = %d, want 0", count)
	}
	var quarantined int
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`,
		usageAccountModelRollupsTable+corruptQuarantineSuffix+"000001").Scan(&quarantined); err != nil {
		t.Fatalf("inspect quarantine table: %v", err)
	}
	if quarantined != 1 {
		t.Fatalf("quarantine table count = %d, want 1", quarantined)
	}

	// An empty rollup is only correct if the rollup is also rewound; otherwise the
	// workers would treat the wiped table as fully caught up and never refill it.
	var checkpoints int
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'account_history'`).Scan(&checkpoints); err != nil {
		t.Fatalf("inspect account history checkpoint: %v", err)
	}
	if checkpoints != 0 {
		t.Fatalf("account history checkpoints = %d, want 0", checkpoints)
	}
	var rebuildTarget int64
	if err := db.QueryRow(`select target_event_id from usage_rollup_rebuild_state
		where name = 'account_history'`).Scan(&rebuildTarget); err != nil {
		t.Fatalf("inspect account history rebuild state: %v", err)
	}
	if rebuildTarget <= 0 {
		t.Fatalf("account history rebuild target = %d, want a scheduled rebuild", rebuildTarget)
	}
}

func TestOpenWithOptionsFailsLoudlyOnCorruptCoreTable(t *testing.T) {
	dbPath := writeCorruptibleDatabase(t, func(db *sql.DB) {
		growUsageEvents(t, db)
	})
	db, err := OpenWithOptions(Options{Path: dbPath, RepairCorruptDerived: true})
	if err == nil {
		_ = db.Close()
		t.Fatal("open sqlite with repair succeeded, want a core corruption error")
	}
	var coreErr *CoreCorruptionError
	if !errors.As(err, &coreErr) {
		t.Fatalf("open error = %v, want *CoreCorruptionError", err)
	}
	if !slices.Contains(coreErr.Tables, "usage_events") {
		t.Fatalf("core corruption tables = %v, want it to contain usage_events", coreErr.Tables)
	}
}

func TestIsCorruptionErrorIgnoresUnrelatedFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "malformed image", err: errors.New("database disk image is malformed (11)"), want: true},
		{name: "fts file format", err: errors.New("invalid fts5 file format (found 5, expected 4 or 3) - run 'rebuild'"), want: true},
		// SQLITE_CANTOPEN describes the whole file, so attributing it to a single
		// derived table would quarantine healthy tables during an outage.
		{name: "cannot open", err: errors.New("unable to open database file (14)"), want: false},
		{name: "busy", err: errors.New("database is locked (5)"), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isCorruptionError(test.err); got != test.want {
				t.Fatalf("isCorruptionError(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}
