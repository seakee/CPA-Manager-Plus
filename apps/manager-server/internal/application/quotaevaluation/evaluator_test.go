package quotaevaluation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	decisions "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/decisionstore"
	projectionadapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityprojection"
	observation "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/quotaobservation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/gatewaydecision"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/resourcepolicy"
	decisionport "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/decisionstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

const (
	keyA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	keyB     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	policyID = "cccccccccccccccccccccccccccccccc"
)

type fixture struct {
	t  *testing.T
	db *sql.DB
	e  *Evaluator
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "shadow.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &fixture{t, db, New(observation.New(db), decisions.New(db))}
	f.exec(`insert into gateway_api_key_identities(id,revision,lifecycle,created_at_ms,updated_at_ms)
		values (?,1,'active',1,1), (?,1,'active',1,1)`, keyA, keyB)
	f.exec(`insert into gateway_quota_policies(id,revision,state,enforcement,action,created_at_ms,updated_at_ms)
		values (?,1,'active','observed','notify',1,1)`, policyID)
	f.exec(`insert into gateway_api_key_policy_bindings(api_key_id,policy_id,revision,enabled,created_at_ms,updated_at_ms)
		values (?,?,1,1,1,1)`, keyA, policyID)
	f.exec(`update gateway_usage_identity_projection_state set status='ready', last_processed_event_id=100,
		binding_revision=0 where state_name='canonical_identity_v1'`)
	return f
}

func (f *fixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(query, args...); err != nil {
		f.t.Fatalf("SQL: %v", err)
	}
}

func (f *fixture) rule(metric string, limit, duration int64) {
	f.t.Helper()
	f.exec(`insert into gateway_quota_policy_rules(policy_id,metric,limit_value,window_kind,duration_ms,timezone)
		values (?,?,?,'rolling',?,'')`, policyID, metric, limit, duration)
}

func (f *fixture) event(id int64, hash string, timestampMS, tokens, failed int64, state, key string) {
	f.t.Helper()
	f.exec(`insert into usage_events(id,event_hash,timestamp_ms,timestamp,model,total_tokens,failed,created_at_ms)
		values (?,?,?,'2026-01-01T00:00:00Z','model',?,?,1)`, id, hash, timestampMS, tokens, failed)
	var keyValue any
	if state == "mapped" {
		keyValue = key
	}
	f.exec(`insert into gateway_usage_identity_projection_v1
		(usage_event_id,event_hash,evidence_timestamp_ms,api_key_state,api_key_id,
		 credential_state,schema_version,projected_at_ms)
		values (?,?,?,?,?,'unknown',1,1)`, id, hash, timestampMS, state, keyValue)
}

func (f *fixture) evaluate(id, at int64) EvaluationResult {
	f.t.Helper()
	result, err := f.e.EvaluateUsageEvent(context.Background(), id, at)
	if err != nil {
		f.t.Fatalf("EvaluateUsageEvent: %v", err)
	}
	return result
}

func TestProjectionAndConfigurationGate(t *testing.T) {
	for _, tt := range []struct {
		name, update, status, reason string
		wantError                    bool
	}{
		{"pending", `update gateway_usage_identity_projection_state set status='pending'`, "pending", "projection_not_ready", false},
		{"coverage", `update gateway_usage_identity_projection_state set last_processed_event_id=0`, "pending", "projection_not_ready", false},
		{"missing projection", `delete from gateway_usage_identity_projection_v1 where usage_event_id=1`, "", "", true},
		{"corrupt identity", `update gateway_usage_identity_projection_v1 set event_hash='other' where usage_event_id=1`, "", "", true},
		{"unknown", `update gateway_usage_identity_projection_v1 set api_key_state='unknown',api_key_id=null where usage_event_id=1`, "skipped", "api_key_unknown", false},
		{"ambiguous", `update gateway_usage_identity_projection_v1 set api_key_state='ambiguous',api_key_id=null where usage_event_id=1`, "skipped", "api_key_ambiguous", false},
		{"stale", `update gateway_usage_identity_projection_v1 set api_key_state='stale',api_key_id=null where usage_event_id=1`, "skipped", "api_key_stale", false},
		{"no binding", `delete from gateway_api_key_policy_bindings`, "skipped", "binding_missing", false},
		{"binding disabled", `update gateway_api_key_policy_bindings set enabled=0`, "skipped", "binding_disabled", false},
		{"policy disabled", `update gateway_quota_policies set state='disabled'`, "skipped", "policy_disabled", false},
		{"key missing", `update gateway_api_key_identities set lifecycle='missing' where id='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`, "evaluated", "", false},
		{"key superseded", `update gateway_api_key_identities set lifecycle='superseded' where id='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`, "evaluated", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.rule("request", 2, 100)
			f.event(1, "source", 100, 1, 0, "mapped", keyA)
			f.exec(tt.update)
			result, err := f.e.EvaluateUsageEvent(context.Background(), 1, 300)
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error, got %+v", result)
				}
			} else if err != nil || string(result.Status) != tt.status || result.Reason != tt.reason {
				t.Fatalf("result %+v, error %v", result, err)
			}
			var persisted int
			if err := f.db.QueryRow(`select count(*) from gateway_quota_decision_events_v1`).Scan(&persisted); err != nil {
				t.Fatal(err)
			}
			if tt.status != "evaluated" && persisted != 0 {
				t.Fatalf("gate wrote %d decisions", persisted)
			}
		})
	}
	f := newFixture(t)
	if _, err := f.e.EvaluateUsageEvent(context.Background(), 999, 300); !errors.Is(err, observation.ErrSourceNotFound) {
		t.Fatalf("missing source error = %v", err)
	}
	if _, err := f.e.EvaluateUsageEvent(context.Background(), 1, 0); err == nil {
		t.Fatal("zero evaluatedAt accepted")
	}
}

func TestEvidenceHorizonAndThreeRules(t *testing.T) {
	f := newFixture(t)
	f.rule("request", 3, 100)
	f.rule("token", 6, 100)
	f.rule("cost", 1, 100)
	f.event(1, "first", 90, 2, 0, "mapped", keyA)
	f.event(2, "failed", 95, 0, 1, "mapped", keyA)
	f.event(3, "future timestamp", 105, 500, 0, "mapped", keyA)
	f.event(4, "  legacy hash  ", 100, 3, 0, "mapped", keyA)
	f.event(5, "other key", 90, 1000, 0, "mapped", keyB)
	f.event(6, "later id historical time", 80, 1000, 0, "mapped", keyA)
	for _, table := range []string{"usage_events", "gateway_usage_identity_projection_v1"} {
		for _, operation := range []string{"update", "delete"} {
			f.exec(`create trigger guard_` + table + `_` + operation + ` before ` + operation + ` on ` + table + `
				begin select raise(abort, 'raw/projection mutation forbidden'); end`)
		}
	}
	result := f.evaluate(4, 300)
	if result.Status != StatusEvaluated || len(result.Events) != 3 {
		t.Fatalf("result %+v", result)
	}
	byMetric := map[resourcepolicy.Metric]gatewaydecision.QuotaDecisionEvent{}
	for _, event := range result.Events {
		if err := event.Validate(); err != nil {
			t.Fatal(err)
		}
		byMetric[event.Metric] = event
	}
	request := byMetric[resourcepolicy.MetricRequest]
	if *request.ObservedValue != 3 || request.Outcome != gatewaydecision.OutcomeNotifyRequired || request.ReasonCode != "limit_reached" {
		t.Fatalf("request %+v", request)
	}
	if *request.WindowStartMS != 1 || *request.WindowEndMS != 101 {
		t.Fatalf("window %+v", request)
	}
	token := byMetric[resourcepolicy.MetricToken]
	if *token.ObservedValue != 5 || token.Outcome != gatewaydecision.OutcomeWithinLimit {
		t.Fatalf("token %+v", token)
	}
	cost := byMetric[resourcepolicy.MetricCost]
	if cost.ObservedValue != nil || cost.Outcome != gatewaydecision.OutcomeIndeterminate || cost.ReasonCode != "cost_observation_unavailable" || cost.WindowStartMS == nil {
		t.Fatalf("cost %+v", cost)
	}
	hash := sha256.Sum256([]byte("  legacy hash  "))
	if request.SourceEventFingerprint != hex.EncodeToString(hash[:]) {
		t.Fatalf("fingerprint %s", request.SourceEventFingerprint)
	}
	retry := f.evaluate(4, 999)
	for i, event := range retry.Events {
		if event.DecisionID != result.Events[i].DecisionID || event.EvaluatedAtMS != 300 {
			t.Fatalf("retry changed first row: %+v", event)
		}
	}
	var count int
	if err := f.db.QueryRow(`select count(*) from gateway_quota_decision_events_v1`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("decision count %d, %v", count, err)
	}
	f.exec(`update gateway_quota_policies set revision=2,updated_at_ms=2 where id=?`, policyID)
	policyChanged := f.evaluate(4, 1000)
	if policyChanged.Events[0].DedupeKey == result.Events[0].DedupeKey {
		t.Fatal("policy revision retained key")
	}
	f.exec(`update gateway_api_key_policy_bindings set revision=2,updated_at_ms=2 where api_key_id=?`, keyA)
	bindingChanged := f.evaluate(4, 1001)
	if bindingChanged.Events[0].DedupeKey == policyChanged.Events[0].DedupeKey {
		t.Fatal("binding revision retained key")
	}
	f.exec(`update gateway_usage_identity_projection_state set binding_revision=1`)
	projectionChanged := f.evaluate(4, 1002)
	if projectionChanged.Events[0].DedupeKey == bindingChanged.Events[0].DedupeKey {
		t.Fatal("projection revision retained key")
	}
	if err := f.db.QueryRow(`select count(*) from gateway_quota_decision_events_v1`).Scan(&count); err != nil || count != 12 {
		t.Fatalf("decision count %d, %v", count, err)
	}
	if err := f.db.QueryRow(`select evaluated_at_ms from gateway_quota_decision_events_v1 where decision_id=?`, request.DecisionID).Scan(&count); err != nil || count != 300 {
		t.Fatalf("historical row changed: %d, %v", count, err)
	}
}

func TestTokenEvidenceAndLimits(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		tokens, failed, limit int64
		outcome               gatewaydecision.Outcome
		reason                string
		observed              *int64
	}{
		{"below", 4, 0, 5, gatewaydecision.OutcomeWithinLimit, "within_limit", intPtr(4)},
		{"at", 5, 0, 5, gatewaydecision.OutcomeNotifyRequired, "limit_reached", intPtr(5)},
		{"above", 6, 0, 5, gatewaydecision.OutcomeNotifyRequired, "limit_reached", intPtr(6)},
		{"success zero", 0, 0, 5, gatewaydecision.OutcomeIndeterminate, "token_evidence_incomplete", intPtr(0)},
		{"failed zero", 0, 1, 5, gatewaydecision.OutcomeWithinLimit, "within_limit", intPtr(0)},
		{"negative", -1, 0, 5, gatewaydecision.OutcomeIndeterminate, "token_evidence_incomplete", intPtr(0)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.rule("token", tt.limit, 100)
			f.event(1, "one", 100, tt.tokens, tt.failed, "mapped", keyA)
			event := f.evaluate(1, 200).Events[0]
			if event.Outcome != tt.outcome || event.ReasonCode != tt.reason || *event.ObservedValue != *tt.observed {
				t.Fatalf("event %+v", event)
			}
		})
	}
	f := newFixture(t)
	f.rule("token", math.MaxInt64, 100)
	f.event(1, "max", 99, math.MaxInt64, 0, "mapped", keyA)
	f.event(2, "plus", 100, 1, 0, "mapped", keyA)
	event := f.evaluate(2, 200).Events[0]
	if event.ReasonCode != "observation_overflow" || event.ObservedValue != nil {
		t.Fatalf("overflow %+v", event)
	}
}

func TestLateSourceAndWindowFailure(t *testing.T) {
	f := newFixture(t)
	f.rule("request", 2, 50)
	f.event(1, "earlier", 40, 1, 0, "mapped", keyA)
	f.event(2, "late source", 50, 1, 0, "mapped", keyA)
	first := f.evaluate(2, 300).Events[0]
	f.event(3, "later arrival older time", 45, 1, 0, "mapped", keyA)
	second := f.evaluate(2, 999).Events[0]
	if *first.ObservedValue != 2 || second.DecisionID != first.DecisionID {
		t.Fatalf("late retry first=%+v second=%+v", first, second)
	}
	broken := newFixture(t)
	broken.rule("request", 1, 100)
	broken.event(1, "too early", 50, 1, 0, "mapped", keyA)
	window := broken.evaluate(1, 200).Events[0]
	if window.ReasonCode != "window_unresolvable" || window.WindowStartMS != nil || window.ObservedValue != nil {
		t.Fatalf("unresolved window %+v", window)
	}
}

func intPtr(n int64) *int64 { return &n }

type failSecondAppend struct {
	decisionport.Repository
	calls int
}

func (f *failSecondAppend) Append(ctx context.Context, event gatewaydecision.QuotaDecisionEvent) (gatewaydecision.QuotaDecisionEvent, bool, error) {
	f.calls++
	if f.calls == 2 {
		return gatewaydecision.QuotaDecisionEvent{}, false, errors.New("simulated process failure")
	}
	return f.Repository.Append(ctx, event)
}

func TestRetryAfterPartialAppendReturnsFirstRows(t *testing.T) {
	f := newFixture(t)
	f.rule("request", 2, 100)
	f.rule("token", 3, 100)
	f.event(1, "source", 100, 1, 0, "mapped", keyA)
	store := &failSecondAppend{Repository: decisions.New(f.db)}
	f.e = New(observation.New(f.db), store)
	if _, err := f.e.EvaluateUsageEvent(context.Background(), 1, 300); err == nil {
		t.Fatal("expected second append failure")
	}
	var firstID string
	if err := f.db.QueryRow(`select decision_id from gateway_quota_decision_events_v1`).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	f.e = New(observation.New(f.db), decisions.New(f.db))
	result := f.evaluate(1, 999)
	if len(result.Events) != 2 || string(result.Events[0].DecisionID) != firstID || result.Events[0].EvaluatedAtMS != 300 {
		t.Fatalf("retry result %+v", result)
	}
	var count int
	if err := f.db.QueryRow(`select count(*) from gateway_quota_decision_events_v1`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("retry count %d: %v", count, err)
	}
}

func TestProjectionRebuildRevisionCreatesNewDecision(t *testing.T) {
	f := newFixture(t)
	f.rule("request", 2, 100)
	sourceHash := strings.Repeat("f", 64)
	f.exec(`insert into gateway_api_key_source_bindings
		(api_key_id,runtime_identity,api_key_hash,observed_runtime_generation,first_seen_at_ms,last_seen_at_ms)
		values (?,'runtime',?,'1',100,100)`, keyA, sourceHash)
	f.exec(`insert into usage_events(id,event_hash,timestamp_ms,timestamp,model,api_key_hash,raw_json,created_at_ms)
		values (1,'source',200,'2026-01-01T00:00:00Z','model',?,'{}',100)`, sourceHash)
	projection := projectionadapter.New(f.db)
	if _, err := projection.CatchUp(context.Background(), 10, 300); err != nil {
		t.Fatal(err)
	}
	first := f.evaluate(1, 400)
	if first.Status != StatusEvaluated || len(first.Events) != 1 {
		t.Fatalf("first %+v", first)
	}
	f.exec(`update gateway_api_key_source_bindings set retired_at_ms=300 where api_key_hash=?`, sourceHash)
	pending := f.evaluate(1, 450)
	if pending.Status != StatusPending || len(pending.Events) != 0 {
		t.Fatalf("rebuild gate %+v", pending)
	}
	if _, err := projection.CatchUp(context.Background(), 10, 500); err != nil {
		t.Fatal(err)
	}
	second := f.evaluate(1, 600)
	if second.Status != StatusEvaluated || len(second.Events) != 1 || second.Events[0].DedupeKey == first.Events[0].DedupeKey {
		t.Fatalf("rebuild result first=%+v second=%+v", first, second)
	}
}

func TestHistoricalCalendarSourceBeforeAnchorIsObserved(t *testing.T) {
	f := newFixture(t)
	anchor := time.Date(2025, 2, 15, 0, 0, 0, 0, time.UTC).UnixMilli()
	sourceMS := time.Date(2025, 1, 20, 0, 0, 0, 0, time.UTC).UnixMilli()
	f.exec(`insert into gateway_quota_policy_rules
		(policy_id,metric,limit_value,window_kind,calendar_months,anchor_at_ms,timezone)
		values (?,'request',2,'fixed',1,?,'UTC')`, policyID, anchor)
	f.event(1, "historical source", sourceMS, 1, 0, "mapped", keyA)
	event := f.evaluate(1, anchor+1).Events[0]
	wantStart := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC).UnixMilli()
	if event.Outcome != gatewaydecision.OutcomeWithinLimit || event.ReasonCode != "within_limit" ||
		event.ObservedValue == nil || *event.ObservedValue != 1 ||
		event.WindowStartMS == nil || *event.WindowStartMS != wantStart ||
		event.WindowEndMS == nil || *event.WindowEndMS != anchor {
		t.Fatalf("historical calendar decision %+v", event)
	}
}

func TestDedupeKeyV1FrozenVector(t *testing.T) {
	event := gatewaydecision.QuotaDecisionEvent{
		SourceUsageEventID: 7, SourceEventFingerprint: strings.Repeat("a", 64),
		EvidenceTimestampMS: 42, APIKeyID: keyA, BindingRevision: 3,
		PolicyID: policyID, PolicyRevision: 4, Enforcement: resourcepolicy.EnforcementObserved,
		Action: resourcepolicy.ActionNotify, Metric: resourcepolicy.MetricCost,
		LimitValue: 123, Outcome: gatewaydecision.OutcomeIndeterminate,
		ReasonCode: "cost_observation_unavailable", EvaluatedAtMS: 100,
		DecisionID: "11111111111111111111111111111111",
	}
	const expected = "f7f361e373205b164beba0d690a7936635e9c28b0f6d98b9735c6dc3ad21da47"
	if got := dedupeKey(event, 1, 9); string(got) != expected {
		t.Fatalf("v1 key = %s", got)
	}
	event.DecisionID = "22222222222222222222222222222222"
	event.EvaluatedAtMS = 999
	if got := dedupeKey(event, 1, 9); string(got) != expected {
		t.Fatalf("volatile fields changed v1 key = %s", got)
	}
	zero := int64(0)
	event.ObservedValue = &zero
	if got := dedupeKey(event, 1, 9); string(got) == expected {
		t.Fatal("nil and zero are conflated")
	}
}
