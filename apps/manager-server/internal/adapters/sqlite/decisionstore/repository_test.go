package decisionstore_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	adapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/decisionstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/gatewaydecision"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/resourcepolicy"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/decisionstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

var ctx = context.Background()

func number(v int64) *int64 { return &v }

func event() gatewaydecision.QuotaDecisionEvent {
	return gatewaydecision.QuotaDecisionEvent{
		DecisionID:     gatewaydecision.DecisionID(strings.Repeat("a", 32)),
		SchemaVersion:  gatewaydecision.SchemaVersion,
		DedupeKey:      gatewaydecision.DedupeKey(strings.Repeat("b", 64)),
		APIKeyID:       identity.APIKeyID(strings.Repeat("c", 32)),
		PolicyID:       resourcepolicy.PolicyID(strings.Repeat("d", 32)),
		PolicyRevision: 1, BindingRevision: 1,
		Metric: resourcepolicy.MetricRequest, Enforcement: resourcepolicy.EnforcementObserved,
		Action: resourcepolicy.ActionNotify, Outcome: gatewaydecision.OutcomeWithinLimit,
		ReasonCode: "within_limit", LimitValue: 10, ObservedValue: number(9),
		WindowStartMS: number(100), WindowEndMS: number(200),
		SourceUsageEventID: 42, SourceEventFingerprint: strings.Repeat("e", 64),
		EvidenceTimestampMS: 150, EvaluatedAtMS: 250,
	}
}

func open(t *testing.T) (*sql.DB, ports.Repository) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "decisions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seed(t, db)
	return db, adapter.New(db)
}

func seed(t *testing.T, db *sql.DB) {
	t.Helper()
	e := event()
	if _, err := db.Exec(`insert into gateway_api_key_identities
		(id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 1, 'active', 1, 1)`, e.APIKeyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into gateway_quota_policies
		(id, revision, state, enforcement, action, created_at_ms, updated_at_ms)
		values (?, 1, 'active', 'observed', 'notify', 1, 1)`, e.PolicyID); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`select count(*) from gateway_quota_decision_events_v1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAppendRoundTripMetricsAndOutcomes(t *testing.T) {
	db, repo := open(t)
	for i, metric := range []resourcepolicy.Metric{resourcepolicy.MetricRequest, resourcepolicy.MetricToken, resourcepolicy.MetricCost} {
		e := event()
		e.DecisionID = gatewaydecision.DecisionID(strings.Repeat(string(rune('a'+i)), 32))
		e.DedupeKey = gatewaydecision.DedupeKey(strings.Repeat(string(rune('b'+i)), 64))
		e.Metric = metric
		if metric == resourcepolicy.MetricCost {
			e.LimitValue, e.ObservedValue = 1_000_000, number(1_000_000)
			e.Outcome, e.ReasonCode = gatewaydecision.OutcomeNotifyRequired, "limit_reached"
		}
		if metric == resourcepolicy.MetricToken {
			e.Outcome, e.ReasonCode = gatewaydecision.OutcomeIndeterminate, "usage_incomplete"
			e.ObservedValue, e.WindowStartMS, e.WindowEndMS = nil, nil, nil
		}
		persisted, inserted, err := repo.Append(ctx, e)
		if err != nil || !inserted || !reflect.DeepEqual(persisted, e) {
			t.Fatalf("append %s: %+v %t %v", metric, persisted, inserted, err)
		}
		loaded, err := repo.LoadByID(ctx, e.DecisionID)
		if err != nil || !reflect.DeepEqual(loaded, e) {
			t.Fatalf("load %s: %+v %v", metric, loaded, err)
		}
	}
	if got := count(t, db); got != 3 {
		t.Fatalf("row count %d", got)
	}
	if _, err := repo.LoadByID(ctx, gatewaydecision.DecisionID(strings.Repeat("f", 32))); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing event: %v", err)
	}
	// Source usage event 42 was never inserted: provenance is a snapshot, not a FK.
}

func TestStoredUsageEventHashIsFingerprintInput(t *testing.T) {
	db, repo := open(t)
	for i, rawHash := range []string{
		"legacy-event-123",
		" legacy-event-123 ",
		strings.Repeat("a", 64),
		"future:event/历史",
	} {
		result, err := db.Exec(`insert into usage_events
			(event_hash, timestamp_ms, timestamp, model, created_at_ms)
			values (?, 150, '150', 'test-model', 150)`, rawHash)
		if err != nil {
			t.Fatal(err)
		}
		usageID, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		var stored string
		if err := db.QueryRow(`select event_hash from usage_events where id = ?`, usageID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		fingerprint := sha256.Sum256([]byte(stored)) // exact stored bytes, with no trim or normalization
		e := event()
		e.DecisionID = gatewaydecision.DecisionID(fmt.Sprintf("%032x", i+1))
		e.DedupeKey = gatewaydecision.DedupeKey(fmt.Sprintf("%064x", i+1))
		e.SourceUsageEventID = usageID
		e.SourceEventFingerprint = hex.EncodeToString(fingerprint[:])
		if _, inserted, err := repo.Append(ctx, e); err != nil || !inserted {
			t.Fatalf("append for stored usage hash %q: inserted=%t err=%v", rawHash, inserted, err)
		}
		loaded, err := repo.LoadByID(ctx, e.DecisionID)
		if err != nil || !reflect.DeepEqual(loaded, e) {
			t.Fatalf("fingerprint for stored usage hash %q: %+v err=%v", rawHash, loaded, err)
		}
	}
}

func TestAppendDedupeAndRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "restart.sqlite")
	db, err := sqlite.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	seed(t, db)
	repo := adapter.New(db)
	first := event()
	if _, inserted, err := repo.Append(ctx, first); err != nil || !inserted {
		t.Fatalf("first append: inserted=%t err=%v", inserted, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo = adapter.New(db)
	retry := event()
	retry.DecisionID = gatewaydecision.DecisionID(strings.Repeat("f", 32))
	retry.EvaluatedAtMS = first.EvaluatedAtMS + 1000 // recomputed after restart
	got, inserted, err := repo.Append(ctx, retry)
	if err != nil || inserted || !reflect.DeepEqual(got, first) || count(t, db) != 1 {
		t.Fatalf("idempotent retry: %+v inserted=%t err=%v", got, inserted, err)
	}
	conflict := retry
	conflict.SourceEventFingerprint = strings.Repeat("0", 64)
	if _, inserted, err := repo.Append(ctx, conflict); !errors.Is(err, ports.ErrDedupeConflict) || inserted {
		t.Fatalf("changed semantic content: inserted=%t err=%v", inserted, err)
	}
	if loaded, err := repo.LoadByID(ctx, first.DecisionID); err != nil || !reflect.DeepEqual(loaded, first) || count(t, db) != 1 {
		t.Fatalf("first row mutated: %+v err=%v", loaded, err)
	}
	invalid := event()
	invalid.ReasonCode = "Bearer secret"
	if _, _, err := repo.Append(ctx, invalid); err == nil || count(t, db) != 1 {
		t.Fatalf("invalid input persisted: %v", err)
	}
}

func TestConcurrentDedupeRetriesKeepOneFirstRow(t *testing.T) {
	db, repo := open(t)
	const retries = 12
	results := make(chan gatewaydecision.QuotaDecisionEvent, retries)
	errorsFound := make(chan error, retries)
	var group sync.WaitGroup
	for i := 1; i <= retries; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			e := event()
			e.DecisionID = gatewaydecision.DecisionID(fmt.Sprintf("%032x", i))
			got, _, err := repo.Append(ctx, e)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- got
		}(i)
	}
	group.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent retry: %v", err)
	}
	var first gatewaydecision.DecisionID
	for result := range results {
		if first == "" {
			first = result.DecisionID
		}
		if result.DecisionID != first {
			t.Fatalf("different first rows: %q and %q", first, result.DecisionID)
		}
	}
	if first == "" || count(t, db) != 1 {
		t.Fatalf("lost or duplicated concurrent append: first=%q rows=%d", first, count(t, db))
	}
}

func TestDecisionIDCollisionCannotOverwriteFirstRow(t *testing.T) {
	db, repo := open(t)
	first := event()
	if _, _, err := repo.Append(ctx, first); err != nil {
		t.Fatal(err)
	}
	other := event()
	other.DedupeKey = gatewaydecision.DedupeKey(strings.Repeat("f", 64))
	if _, _, err := repo.Append(ctx, other); err == nil {
		t.Fatal("DecisionID collision unexpectedly overwrote or deduplicated a different operation")
	}
	if loaded, err := repo.LoadByID(ctx, first.DecisionID); err != nil || !reflect.DeepEqual(loaded, first) || count(t, db) != 1 {
		t.Fatalf("DecisionID collision changed first row: %+v err=%v", loaded, err)
	}
}

func TestHistoricalPolicyAndBindingMutationDoesNotRewriteEvent(t *testing.T) {
	db, repo := open(t)
	e := event()
	if _, err := db.Exec(`insert into gateway_api_key_policy_bindings
		(api_key_id, policy_id, revision, enabled, created_at_ms, updated_at_ms)
		values (?, ?, 1, 1, 1, 1)`, e.APIKeyID, e.PolicyID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.Append(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update gateway_quota_policies set revision = 2, state = 'disabled', updated_at_ms = 2 where id = ?`, e.PolicyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update gateway_api_key_policy_bindings set revision = 2, enabled = 0, updated_at_ms = 2 where api_key_id = ?`, e.APIKeyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update gateway_api_key_identities set lifecycle = 'missing', updated_at_ms = 2 where id = ?`, e.APIKeyID); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.LoadByID(ctx, e.DecisionID)
	if err != nil || !reflect.DeepEqual(loaded, e) {
		t.Fatalf("historical event changed: %+v err=%v", loaded, err)
	}
}

func TestSQLiteRejectsInvalidRows(t *testing.T) {
	db, repo := open(t)
	e := event()
	if _, _, err := repo.Append(ctx, e); err != nil {
		t.Fatal(err)
	}
	// Each UPDATE is an independent raw SQLite attempt against a valid row.
	// A failed CHECK/NOT NULL leaves the original record unchanged.
	cases := []struct {
		name, column string
		value        any
	}{
		{"null DecisionID", "decision_id", nil},
		{"malformed DecisionID", "decision_id", strings.Repeat("A", 32)},
		{"null DedupeKey", "dedupe_key", nil},
		{"malformed DedupeKey", "dedupe_key", strings.Repeat("A", 64)},
		{"schema version", "schema_version", 2},
		{"APIKeyID", "api_key_id", "bad"},
		{"PolicyID", "policy_id", "bad"},
		{"policy revision zero", "policy_revision", 0},
		{"binding revision zero", "binding_revision", 0},
		{"policy revision float", "policy_revision", 1.5},
		{"metric", "metric", "bytes"},
		{"enforcement", "enforcement", "hard"},
		{"action", "action", "block"},
		{"outcome", "outcome", "notification_sent"},
		{"reason prose", "reason_code", "provider error"},
		{"reason colon", "reason_code", "error:secret"},
		{"reason long", "reason_code", strings.Repeat("a", 65)},
		{"limit zero", "limit_value", 0},
		{"limit float", "limit_value", 1.5},
		{"observed float", "observed_value", 1.5},
		{"observed negative", "observed_value", -1},
		{"partial window", "window_end_ms", nil},
		{"reversed window", "window_end_ms", 100},
		{"float window", "window_start_ms", 100.5},
		{"within missing observation", "observed_value", nil},
		{"within at limit", "observed_value", 10},
		{"source event zero", "source_usage_event_id", 0},
		{"source fingerprint", "source_event_fingerprint", strings.Repeat("A", 64)},
		{"raw legacy event hash", "source_event_fingerprint", "legacy-event-123"},
		{"evidence time", "evidence_timestamp_ms", 0},
		{"evaluation time", "evaluated_at_ms", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`update gateway_quota_decision_events_v1 set `+tc.column+` = ? where decision_id = ?`, tc.value, e.DecisionID); err == nil {
				t.Fatalf("SQLite accepted %s", tc.name)
			}
		})
	}
	for _, tc := range []struct {
		name, assignment string
	}{
		{"notify below limit", "outcome = 'notify_required'"},
		{"notify without window", "outcome = 'notify_required', observed_value = 10, window_start_ms = null, window_end_ms = null"},
		{"notify without observation", "outcome = 'notify_required', observed_value = null"},
		{"within without window", "window_start_ms = null, window_end_ms = null"},
		{"indeterminate partial window", "outcome = 'indeterminate', window_start_ms = null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`update gateway_quota_decision_events_v1 set `+tc.assignment+` where decision_id = ?`, e.DecisionID); err == nil {
				t.Fatalf("SQLite accepted %s", tc.name)
			}
		})
	}
	if _, err := db.Exec(`insert into gateway_quota_decision_events_v1
		select ?, schema_version, dedupe_key, api_key_id, policy_id, policy_revision,
		binding_revision, metric, enforcement, action, outcome, reason_code,
		limit_value, observed_value, window_start_ms, window_end_ms,
		source_usage_event_id, source_event_fingerprint, evidence_timestamp_ms, evaluated_at_ms
		from gateway_quota_decision_events_v1 where decision_id = ?`, strings.Repeat("f", 32), e.DecisionID); err == nil {
		t.Fatal("SQLite accepted duplicate DedupeKey")
	}
	if loaded, err := repo.LoadByID(ctx, e.DecisionID); err != nil || !reflect.DeepEqual(loaded, e) || count(t, db) != 1 {
		t.Fatalf("failed raw SQL changed event: %+v err=%v", loaded, err)
	}
}

func TestSQLiteRejectsSameLogicalIDOrDedupeAsBlob(t *testing.T) {
	db, repo := open(t)
	first := event()
	if _, _, err := repo.Append(ctx, first); err != nil {
		t.Fatal(err)
	}
	// A BLOB with the same ASCII bytes compares differently from TEXT in SQLite.
	// Both INSERTs would bypass logical uniqueness without the storage-class CHECKs.
	copyRow := `insert into gateway_quota_decision_events_v1
		select ?, schema_version, ?, api_key_id, policy_id, policy_revision,
		binding_revision, metric, enforcement, action, outcome, reason_code,
		limit_value, observed_value, window_start_ms, window_end_ms,
		source_usage_event_id, source_event_fingerprint, evidence_timestamp_ms, evaluated_at_ms
		from gateway_quota_decision_events_v1 where decision_id = ?`
	if _, err := db.Exec(copyRow, []byte(first.DecisionID), strings.Repeat("f", 64), first.DecisionID); err == nil {
		t.Fatal("SQLite allowed the same DecisionID bytes as a separate BLOB primary key")
	}
	if _, err := db.Exec(copyRow, strings.Repeat("f", 32), []byte(first.DedupeKey), first.DecisionID); err == nil {
		t.Fatal("SQLite allowed the same DedupeKey bytes as a separate BLOB unique key")
	}
	for _, tc := range []struct {
		column string
		value  string
	}{
		{"api_key_id", first.APIKeyID.String()},
		{"policy_id", first.PolicyID.String()},
		{"metric", string(first.Metric)},
		{"enforcement", string(first.Enforcement)},
		{"action", string(first.Action)},
		{"outcome", string(first.Outcome)},
		{"reason_code", first.ReasonCode},
		{"source_event_fingerprint", first.SourceEventFingerprint},
	} {
		if _, err := db.Exec(`update gateway_quota_decision_events_v1 set `+tc.column+` = ? where decision_id = ?`, []byte(tc.value), first.DecisionID); err == nil {
			t.Fatalf("SQLite allowed BLOB in text column %s", tc.column)
		}
	}
	if got := count(t, db); got != 1 {
		t.Fatalf("BLOB variants created extra rows: %d", got)
	}
	if loaded, err := repo.LoadByID(ctx, first.DecisionID); err != nil || !reflect.DeepEqual(loaded, first) {
		t.Fatalf("BLOB attempt altered first event: %+v err=%v", loaded, err)
	}
}

func TestSQLiteEnforcesCanonicalIdentityAndPolicyForeignKeys(t *testing.T) {
	db, repo := open(t)
	e := event()
	if _, _, err := repo.Append(ctx, e); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		column string
		value  string
	}{
		{"api_key_id", strings.Repeat("f", 32)},
		{"policy_id", strings.Repeat("f", 32)},
	} {
		if _, err := db.Exec(`update gateway_quota_decision_events_v1 set `+tc.column+` = ? where decision_id = ?`, tc.value, e.DecisionID); err == nil {
			t.Fatalf("SQLite accepted missing %s target", tc.column)
		}
	}
	if loaded, err := repo.LoadByID(ctx, e.DecisionID); err != nil || !reflect.DeepEqual(loaded, e) {
		t.Fatalf("foreign key violation changed first row: %+v err=%v", loaded, err)
	}
}

func TestSchemaOnlyAllowsApprovedColumnsAndRelationships(t *testing.T) {
	db, _ := open(t)
	want := strings.Fields(`decision_id schema_version dedupe_key api_key_id policy_id
		policy_revision binding_revision metric enforcement action outcome reason_code
		limit_value observed_value window_start_ms window_end_ms source_usage_event_id
		source_event_fingerprint evidence_timestamp_ms evaluated_at_ms`)
	rows, err := db.Query(`pragma table_info(gateway_quota_decision_events_v1)`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("schema contains missing or unapproved payload columns: %v", got)
	}
	foreign, err := db.Query(`pragma foreign_key_list(gateway_quota_decision_events_v1)`)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	var tables []string
	for foreign.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := foreign.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := foreign.Err(); err != nil {
		t.Fatal(err)
	}
	if len(tables) != 2 || !(tables[0] == sqlite.GatewayQuotaPoliciesTable && tables[1] == sqlite.GatewayAPIKeyIdentitiesTable || tables[1] == sqlite.GatewayQuotaPoliciesTable && tables[0] == sqlite.GatewayAPIKeyIdentitiesTable) {
		t.Fatalf("unexpected FK authority: %v", tables)
	}
}
