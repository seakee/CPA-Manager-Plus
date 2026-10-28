package usagepricing

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
	_ "modernc.org/sqlite"
)

// retainedEventSourceFixture mirrors the production schema pieces the retained
// event sources depend on: the compatible projection state row and its indexes,
// the raw event table and the archive reference timestamp index.
func retainedEventSourceFixture(t *testing.T) (context.Context, *sql.Tx) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	columns := make([]string, 0, len(retainedPricingColumns)+2)
	columns = append(columns, "event_id integer primary key", "account_key text not null")
	for _, column := range retainedPricingColumns {
		columns = append(columns, column+" text")
	}
	rawColumns := make([]string, 0, len(retainedPricingColumns)+2)
	rawColumns = append(rawColumns, "id integer primary key", "event_hash text")
	for _, column := range retainedPricingColumns {
		rawColumns = append(rawColumns, column+" text")
	}
	for _, statement := range []string{
		"create table usage_monitoring_event_projection_v1 (" + strings.Join(columns, ",") + ")",
		"create table usage_events (" + strings.Join(rawColumns, ",") + ")",
		`create index idx_usage_events_event_hash on usage_events(event_hash)`,
		`create index idx_usage_events_timestamp on usage_events(timestamp_ms)`,
		`create table usage_archive_event_refs (
			raw_event_id integer not null,
			timestamp_ms integer not null,
			raw_deleted_at_ms integer,
			event_hash text not null
		)`,
		`create index idx_usage_archive_event_refs_timestamp_deleted
			on usage_archive_event_refs(timestamp_ms, raw_deleted_at_ms)`,
		`create index idx_usage_monitoring_event_projection_timestamp
			on usage_monitoring_event_projection_v1(timestamp_ms desc, event_id desc)`,
		`create index idx_usage_monitoring_event_projection_account_window
			on usage_monitoring_event_projection_v1(account_key, timestamp_ms, event_id)`,
		`create table usage_monitoring_rollup_state (
			rollup_name text primary key,
			schema_version integer not null,
			structure_revision text not null,
			status text not null,
			coverage_event_id integer not null
		)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`insert into usage_monitoring_rollup_state(
		rollup_name, schema_version, structure_revision, status, coverage_event_id
	) values('projection_v1', 1, ?, 'ready', 100)`,
		usageidentity.MonitoringProjectionStructureRevision()); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return ctx, tx
}

func retainedEventSourcePlan(t *testing.T, ctx context.Context, tx *sql.Tx, query string, args ...any) string {
	t.Helper()
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, "\n")
}

func TestRetainedAccountEventSourceUsesProjectionAccountIndex(t *testing.T) {
	ctx, tx := retainedEventSourceFixture(t)
	source, args, err := retainedAccountEventSourceTx(ctx, tx, []string{"account-a", "account-b"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source, "p.account_key in (?,?)") {
		t.Fatalf("retained account source is not account-scoped: %s", source)
	}

	plan := retainedEventSourcePlan(t, ctx, tx, "explain query plan select id from "+source+" retained", args...)
	if !strings.Contains(plan, "idx_usage_monitoring_event_projection_account_window") {
		t.Fatalf("retained account projection plan does not use account index: %s", plan)
	}
	if !strings.Contains(plan, "idx_usage_archive_event_refs_timestamp_deleted") {
		t.Fatalf("retained account archive check does not use the archive timestamp index: %s", plan)
	}
}

// The hourly source must drive archived projection rows from the archive
// timestamp index instead of scanning every retained archive reference.
func TestRetainedEventSourceUsesArchiveTimestampIndex(t *testing.T) {
	ctx, tx := retainedEventSourceFixture(t)
	source, err := retainedEventSourceTx(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	plan := retainedEventSourcePlan(t, ctx, tx,
		"explain query plan select id from "+source+" e where e.timestamp_ms >= ? and e.timestamp_ms < ?",
		int64(1_000), int64(2_000))
	if strings.Contains(plan, "SCAN archived") {
		t.Fatalf("retained event source scans every archive reference: %s", plan)
	}
	if !strings.Contains(plan, "idx_usage_archive_event_refs_timestamp_deleted") {
		t.Fatalf("retained event source does not use the archive timestamp index: %s", plan)
	}
}
