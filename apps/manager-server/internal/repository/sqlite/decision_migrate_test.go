package sqlite

import (
	"path/filepath"
	"testing"
)

func TestDecisionMigrationAddsEmptyTableWithoutBackfill(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "decision-migrate.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`insert into usage_events
		(event_hash, timestamp_ms, timestamp, model, input_tokens, created_at_ms)
		values ('historical-usage', 1234, '1234', 'test-model', 17, 1234)`); err != nil {
		t.Fatal(err)
	}
	var usageID int64
	if err := db.QueryRow(`select id from usage_events where event_hash = 'historical-usage'`).Scan(&usageID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into gateway_usage_identity_projection_v1
		(usage_event_id, event_hash, evidence_timestamp_ms, api_key_state, credential_state, schema_version, projected_at_ms)
		values (?, 'historical-usage', 1234, 'unknown', 'unknown', 1, 1235)`, usageID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`drop table gateway_quota_decision_events_v1`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var decisions, usages, projections int
	if err := db.QueryRow(`select count(*) from gateway_quota_decision_events_v1`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`select count(*) from usage_events where id = ? and event_hash = 'historical-usage' and input_tokens = 17`, usageID).Scan(&usages); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`select count(*) from gateway_usage_identity_projection_v1 where usage_event_id = ? and api_key_state = 'unknown'`, usageID).Scan(&projections); err != nil {
		t.Fatal(err)
	}
	if decisions != 0 || usages != 1 || projections != 1 {
		t.Fatalf("migration backfilled or changed history: decisions=%d usage=%d projection=%d", decisions, usages, projections)
	}
}
