package sqlite

import (
	"path/filepath"
	"testing"
)

// Adding policy storage must leave immutable usage truth and the derived G1
// projection untouched. It also must not create historical policy bindings.
func TestPolicyMigrationIsAdditiveWithoutHistoricalBackfill(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "policy-migrate.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`insert into usage_events
		(event_hash, timestamp_ms, timestamp, model, input_tokens, created_at_ms)
		values ('policy-immutable-event', 1234, '1234', 'test-model', 17, 1234)`); err != nil {
		t.Fatal(err)
	}
	var eventID int64
	if err := db.QueryRow(`select id from usage_events where event_hash = 'policy-immutable-event'`).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into gateway_usage_identity_projection_v1
		(usage_event_id, event_hash, evidence_timestamp_ms, api_key_state, credential_state, schema_version, projected_at_ms)
		values (?, 'policy-immutable-event', 1234, 'unknown', 'unknown', 1, 1235)`, eventID); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var hash, model string
	var tokens int64
	if err := db.QueryRow(`select event_hash, model, input_tokens from usage_events where id = ?`, eventID).Scan(&hash, &model, &tokens); err != nil {
		t.Fatal(err)
	}
	if hash != "policy-immutable-event" || model != "test-model" || tokens != 17 {
		t.Fatalf("usage event changed: %q %q %d", hash, model, tokens)
	}
	var state string
	if err := db.QueryRow(`select api_key_state from gateway_usage_identity_projection_v1 where usage_event_id = ?`, eventID).Scan(&state); err != nil || state != "unknown" {
		t.Fatalf("projection changed: %q %v", state, err)
	}
	for _, table := range []string{GatewayQuotaPoliciesTable, GatewayQuotaPolicyRulesTable, GatewayAPIKeyPolicyBindingsTable} {
		var n int
		if err := db.QueryRow(`select count(*) from ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s backfilled history: count=%d err=%v", table, n, err)
		}
	}
}
