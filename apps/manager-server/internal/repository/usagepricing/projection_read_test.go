package usagepricing

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
	_ "modernc.org/sqlite"
)

func TestRetainedAccountEventSourceUsesProjectionAccountIndex(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	columns := make([]string, 0, len(retainedPricingColumns)+2)
	columns = append(columns, "event_id integer primary key", "account_key text not null")
	for _, column := range retainedPricingColumns {
		columns = append(columns, column+" text")
	}
	if _, err := db.Exec("create table usage_monitoring_event_projection_v1 (" + strings.Join(columns, ",") + ")"); err != nil {
		t.Fatal(err)
	}
	rawColumns := make([]string, 0, len(retainedPricingColumns)+1)
	rawColumns = append(rawColumns, "id integer primary key")
	for _, column := range retainedPricingColumns {
		rawColumns = append(rawColumns, column+" text")
	}
	if _, err := db.Exec("create table usage_events (" + strings.Join(rawColumns, ",") + ")"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create index idx_usage_monitoring_event_projection_account_window
		on usage_monitoring_event_projection_v1(account_key, timestamp_ms, event_id)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table usage_monitoring_rollup_state (
		rollup_name text primary key,
		schema_version integer not null,
		structure_revision text not null,
		status text not null,
		coverage_event_id integer not null
	)`); err != nil {
		t.Fatal(err)
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
	defer tx.Rollback()
	source, args, err := retainedAccountEventSourceTx(ctx, tx, []string{"account-a", "account-b"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source, "p.account_key in (?,?)") {
		t.Fatalf("retained account source is not account-scoped: %s", source)
	}

	rows, err := tx.QueryContext(ctx, "explain query plan select id from "+source+" retained", args...)
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
	plan := strings.Join(details, "\n")
	if !strings.Contains(plan, "idx_usage_monitoring_event_projection_account_window") {
		t.Fatalf("retained account projection plan does not use account index: %s", plan)
	}
}
