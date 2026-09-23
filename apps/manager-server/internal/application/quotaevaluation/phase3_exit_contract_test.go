package quotaevaluation

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	decisions "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/decisionstore"
	projectionadapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityprojection"
	identities "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identitystore"
	observation "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/quotaobservation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/gatewaydecision"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/resourcepolicy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/pricing"
)

const (
	exitContractID = "cpamp-v2-phase3-gateway-core-exit-v1"
	exitBaseline   = "80583269557cea39fac06b5c26ad985137234efc"
	exitAuthID     = "cpa-auth-phase3-exit"
)

type exitContract struct {
	Schema            string         `json:"$schema"`
	SchemaVersion     int            `json:"schemaVersion"`
	ContractID        string         `json:"contractId"`
	ExecutionBaseline string         `json:"executionBaseline"`
	Scenarios         []exitScenario `json:"scenarios"`
}

type exitScenario struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	OldHash    string   `json:"oldHash,omitempty"`
	NewHash    string   `json:"newHash,omitempty"`
	APIKeyHash string   `json:"apiKeyHash,omitempty"`
	OtherHash  string   `json:"otherHash,omitempty"`
	Assertions []string `json:"assertions"`
}

// This registry makes deletion, duplication, and moving a claim to another
// scenario a test failure. The checked-in fixture selects and runs every claim.
var exitRequired = map[string]struct {
	kind       string
	assertions []string
}{
	"phase3-01-canonical-identity":           {"canonical_identity", []string{"api_key_id_not_source_hash", "credential_id_not_auth_id", "passive_replacement_new_id", "explicit_rotation_same_id", "superseded_terminal_nonreuse"}},
	"phase3-02-raw-truth-projection-derived": {"raw_projection", []string{"raw_usage_unchanged", "projection_rebuilt", "checkpoint_rebuilt", "policy_decision_not_raw_authority"}},
	"phase3-03-request-shadow-happy-path":    {"request_shadow", []string{"canonical_mapping_chain", "event_time_high_water", "api_key_isolation", "failed_request_counted", "within_limit", "notify_required", "decision_snapshot"}},
	"phase3-04-fail-closed-gates":            {"fail_closed", []string{"pending_no_decision", "uncovered_no_decision", "unknown_no_decision", "ambiguous_no_decision", "stale_no_decision", "binding_missing_no_decision", "binding_disabled_no_decision", "policy_disabled_no_decision", "missing_projection_error", "corrupt_projection_error", "no_weak_identity_fallback"}},
	"phase3-05-audit-idempotency-history":    {"audit_history", []string{"volatile_retry_first_persisted", "partial_append_retry", "policy_revision_new_event", "binding_revision_new_event", "projection_revision_new_event", "old_event_immutable"}},
	"phase3-06-token-evidence-conservative":  {"token_evidence", []string{"exact_int64_sum", "successful_zero_incomplete", "negative_incomplete", "failed_zero_complete", "overflow_nil"}},
	"phase3-07-cost-authority-boundary":      {"cost_boundary", []string{"cost_unavailable", "float_estimate_not_authority"}},
	"phase3-08-no-overclaim":                 {"no_overclaim", []string{"no_notification_delivery", "no_soft_hard_request_enforcement", "no_precise_hard_token_cost_quota", "no_reservation_settlement", "no_runtime_policy_publish_ack_reject", "no_hard_routing_fallback_pinning", "no_request_attempt_authority", "no_public_api_web_ui", "no_complete_entity_directory", "observed_notify_only"}},
}

func exitLoad(t *testing.T) exitContract {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "..", "tests", "fixtures", "phase3-exit", "contract.json")
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open checked-in Phase3 exit contract %s: %v", path, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var contract exitContract
	if err := decoder.Decode(&contract); err != nil {
		t.Fatalf("decode Phase3 exit contract: %v", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatalf("trailing JSON in Phase3 exit contract: %v", err)
	}
	if contract.Schema != "./contract.schema.json" || contract.SchemaVersion != 1 ||
		contract.ContractID != exitContractID || contract.ExecutionBaseline != exitBaseline {
		t.Fatalf("invalid Phase3 exit header: %+v", contract)
	}
	if len(contract.Scenarios) != len(exitRequired) {
		t.Fatalf("scenario count = %d, want %d", len(contract.Scenarios), len(exitRequired))
	}
	seen := make(map[string]bool)
	for _, s := range contract.Scenarios {
		required, ok := exitRequired[s.ID]
		if !ok || seen[s.ID] || s.Kind != required.kind {
			t.Fatalf("unknown, duplicate, or mismatched scenario: %+v", s)
		}
		seen[s.ID] = true
		if s.Kind == "canonical_identity" {
			if !identity.IsValidSHA256Hex(s.OldHash) || !identity.IsValidSHA256Hex(s.NewHash) || s.OldHash == s.NewHash || s.APIKeyHash != "" || s.OtherHash != "" {
				t.Fatalf("invalid canonical identity hash input: %+v", s)
			}
		} else if s.Kind == "raw_projection" || s.Kind == "request_shadow" || s.Kind == "fail_closed" || s.Kind == "audit_history" {
			if !identity.IsValidSHA256Hex(s.APIKeyHash) || s.OldHash != "" || s.NewHash != "" ||
				(s.Kind == "request_shadow" && (!identity.IsValidSHA256Hex(s.OtherHash) || s.OtherHash == s.APIKeyHash)) ||
				(s.Kind != "request_shadow" && s.OtherHash != "") {
				t.Fatalf("invalid projection hash input: %+v", s)
			}
		} else if s.OldHash != "" || s.NewHash != "" || s.APIKeyHash != "" || s.OtherHash != "" {
			t.Fatalf("unexpected source hash input: %+v", s)
		}
		claims := make(map[string]bool)
		for _, assertion := range s.Assertions {
			allowed := false
			for _, expected := range required.assertions {
				if assertion == expected {
					allowed = true
					break
				}
			}
			if !allowed || claims[assertion] {
				t.Fatalf("unknown or duplicate assertion %q in %s", assertion, s.ID)
			}
			claims[assertion] = true
		}
		if len(claims) != len(required.assertions) {
			t.Fatalf("%s omits mandatory assertions: %v", s.ID, s.Assertions)
		}
	}
	return contract
}

func TestPhase3CheckedInExitContract(t *testing.T) {
	for _, scenario := range exitLoad(t).Scenarios {
		t.Run(scenario.ID, func(t *testing.T) {
			var observed map[string]bool
			switch scenario.Kind {
			case "canonical_identity":
				observed = exitCanonicalIdentity(t, scenario)
			case "raw_projection":
				observed = exitRawProjection(t, scenario)
			case "request_shadow":
				observed = exitRequestShadow(t, scenario)
			case "fail_closed":
				observed = exitFailClosed(t, scenario)
			case "audit_history":
				observed = exitAuditHistory(t, scenario)
			case "token_evidence":
				observed = exitTokenEvidence(t)
			case "cost_boundary":
				observed = exitCostBoundary(t)
			case "no_overclaim":
				observed = exitNoOverclaim(t)
			default:
				t.Fatalf("unhandled scenario kind %q", scenario.Kind)
			}
			for _, assertion := range scenario.Assertions {
				if !observed[assertion] {
					t.Errorf("Phase3 exit assertion %q failed", assertion)
				}
			}
		})
	}
}

func exitDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "phase3-exit.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func exitEvalFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	id, err := identity.NewAPIKeyID()
	if err != nil {
		t.Fatal(err)
	}
	f.exec("insert into gateway_api_key_identities(id,revision,lifecycle,created_at_ms,updated_at_ms) values (?,1,'active',1,1)", id)
	f.exec("insert into gateway_api_key_policy_bindings(api_key_id,policy_id,revision,enabled,created_at_ms,updated_at_ms) values (?,?,1,1,1,1)", id, policyID)
	return f, string(id)
}

func exitExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("SQLite setup: %v", err)
	}
}

func exitCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), "select count(*) from "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func exitSnapshot(t *testing.T, repo ports.Repository, runtime string, at int64, hashes ...string) {
	t.Helper()
	items := make([]ports.APIKeySnapshotItem, len(hashes))
	for i, hash := range hashes {
		items[i] = ports.APIKeySnapshotItem{APIKeyHash: hash}
	}
	if _, err := repo.ApplyPassiveSnapshot(t.Context(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: runtime, ObservedRuntimeGeneration: 1, APIKeys: items, NowMS: at,
	}); err != nil {
		t.Fatalf("passive snapshot: %v", err)
	}
}

func exitKey(t *testing.T, repo ports.Repository, runtime, hash string) identity.APIKeyIdentity {
	t.Helper()
	key, _, err := repo.FindActiveAPIKeyBySource(t.Context(), runtime, hash)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func exitUsage(t *testing.T, db *sql.DB, id int64, eventHash string, at int64, keyHash string, tokens, failed int64) {
	t.Helper()
	exitExec(t, db, "insert into usage_events(id,event_hash,request_id,timestamp_ms,timestamp,model,api_key_hash,auth_index,raw_json,input_tokens,output_tokens,total_tokens,failed,created_at_ms) values (?,?,?,?,'2026-01-01T00:00:00Z','exit-model',?,'weak-alias','{}',100,50,?,?,1)",
		id, eventHash, "shared-request-id", at, keyHash, tokens, failed)
}

func exitPolicy(t *testing.T, db *sql.DB, keyID identity.APIKeyID, metric string, limit int64) {
	t.Helper()
	exitExec(t, db, "insert into gateway_quota_policies(id,revision,state,enforcement,action,created_at_ms,updated_at_ms) values (?,1,'active','observed','notify',1,1)", policyID)
	exitExec(t, db, "insert into gateway_quota_policy_rules(policy_id,metric,limit_value,window_kind,duration_ms,timezone) values (?,?,?,'rolling',10,'')", policyID, metric, limit)
	exitExec(t, db, "insert into gateway_api_key_policy_bindings(api_key_id,policy_id,revision,enabled,created_at_ms,updated_at_ms) values (?,?,1,1,1,1)", keyID, policyID)
}

func exitEvaluate(t *testing.T, db *sql.DB, id, at int64) EvaluationResult {
	t.Helper()
	result, err := New(observation.New(db), decisions.New(db)).EvaluateUsageEvent(t.Context(), id, at)
	if err != nil {
		t.Fatalf("evaluate event %d: %v", id, err)
	}
	return result
}

func exitCanonicalIdentity(t *testing.T, s exitScenario) map[string]bool {
	db := exitDB(t)
	repo := identities.New(db)
	exitSnapshot(t, repo, "passive", 1000, s.OldHash)
	oldPassive := exitKey(t, repo, "passive", s.OldHash)
	if _, err := repo.ApplyPassiveSnapshot(t.Context(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: "passive", ObservedRuntimeGeneration: 1,
		APIKeys:     []ports.APIKeySnapshotItem{{APIKeyHash: s.NewHash}},
		Credentials: []ports.CredentialSnapshotItem{{SourceAuthID: exitAuthID, PhysicalName: "auth.json"}},
		NowMS:       2000,
	}); err != nil {
		t.Fatal(err)
	}
	newPassive := exitKey(t, repo, "passive", s.NewHash)
	credential, _, err := repo.FindActiveCredentialBySource(t.Context(), "passive", exitAuthID)
	if err != nil {
		t.Fatal(err)
	}

	exitSnapshot(t, repo, "rotation", 1000, s.OldHash)
	oldRotate := exitKey(t, repo, "rotation", s.OldHash)
	mutations := repo.(ports.MutationRepository)
	intent, err := mutations.PrepareAPIKeyMutation(t.Context(), ports.PrepareAPIKeyMutationParams{
		Kind: ports.APIKeyMutationRotate, RuntimeIdentity: "rotation", ObservedRuntimeGeneration: 1,
		Evidence:      ports.APIKeyMutationEvidence{OldHash: s.OldHash, NewHash: s.NewHash, ExactOldCount: 1, NormalizedOldCount: 1},
		OwnerInstance: "phase3-exit", NowMS: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mutations.MarkAPIKeyMutationForwardComplete(t.Context(), intent, "phase3-exit", 2001); err != nil {
		t.Fatal(err)
	}
	outcome, err := mutations.ResolveAPIKeyMutation(t.Context(), ports.ResolveAPIKeyMutationParams{
		IntentID: intent, RuntimeIdentity: "rotation", ObservedRuntimeGeneration: 2, ObservedHashes: []string{s.NewHash}, NowMS: 2002,
	})
	if err != nil || outcome != ports.APIKeyMutationSuccess {
		t.Fatalf("rotation outcome %q: %v", outcome, err)
	}
	newRotate := exitKey(t, repo, "rotation", s.NewHash)

	exitSnapshot(t, repo, "delete", 1000, s.OldHash)
	oldDelete := exitKey(t, repo, "delete", s.OldHash)
	deleteIntent, err := mutations.PrepareAPIKeyMutation(t.Context(), ports.PrepareAPIKeyMutationParams{
		Kind: ports.APIKeyMutationDelete, RuntimeIdentity: "delete", ObservedRuntimeGeneration: 1,
		Evidence:      ports.APIKeyMutationEvidence{OldHash: s.OldHash, NormalizedOldCount: 1},
		OwnerInstance: "phase3-exit", NowMS: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mutations.MarkAPIKeyMutationForwardComplete(t.Context(), deleteIntent, "phase3-exit", 2001); err != nil {
		t.Fatal(err)
	}
	deleteOutcome, err := mutations.ResolveAPIKeyMutation(t.Context(), ports.ResolveAPIKeyMutationParams{
		IntentID: deleteIntent, RuntimeIdentity: "delete", ObservedRuntimeGeneration: 2, NowMS: 2002,
	})
	if err != nil || deleteOutcome != ports.APIKeyMutationSuccess {
		t.Fatalf("delete outcome %q: %v", deleteOutcome, err)
	}
	superseded, err := repo.LoadAPIKeyByID(t.Context(), oldDelete.ID)
	if err != nil {
		t.Fatal(err)
	}
	exitSnapshot(t, repo, "delete", 3000, s.OldHash)
	reappeared := exitKey(t, repo, "delete", s.OldHash)
	stillSuperseded, err := repo.LoadAPIKeyByID(t.Context(), oldDelete.ID)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]bool{
		"api_key_id_not_source_hash":   oldPassive.ID.Validate() == nil && string(oldPassive.ID) != s.OldHash,
		"credential_id_not_auth_id":    credential.ID.Validate() == nil && string(credential.ID) != exitAuthID,
		"passive_replacement_new_id":   oldPassive.ID != newPassive.ID,
		"explicit_rotation_same_id":    oldRotate.ID == newRotate.ID && newRotate.Revision == oldRotate.Revision+1,
		"superseded_terminal_nonreuse": superseded.Lifecycle == identity.LifecycleSuperseded && stillSuperseded.Lifecycle == identity.LifecycleSuperseded && reappeared.ID != oldDelete.ID,
	}
}

func exitRawRows(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "select * from usage_events order by id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result []string
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		result = append(result, fmt.Sprintf("%#v", values))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func exitProjection(t *testing.T, db *sql.DB, id int64) (string, string) {
	t.Helper()
	var state, keyID string
	if err := db.QueryRowContext(t.Context(), "select api_key_state,coalesce(api_key_id,'') from gateway_usage_identity_projection_v1 where usage_event_id=?", id).
		Scan(&state, &keyID); err != nil {
		t.Fatal(err)
	}
	return state, keyID
}

func exitProjectionState(t *testing.T, db *sql.DB) (string, int64) {
	t.Helper()
	var state string
	var last int64
	if err := db.QueryRowContext(t.Context(), "select status,last_processed_event_id from gateway_usage_identity_projection_state where state_name='canonical_identity_v1'").
		Scan(&state, &last); err != nil {
		t.Fatal(err)
	}
	return state, last
}

func exitCatchUp(t *testing.T, db *sql.DB, at int64) {
	t.Helper()
	if _, err := projectionadapter.New(db).CatchUp(t.Context(), 100, at); err != nil {
		t.Fatalf("projection catch-up: %v", err)
	}
}

func exitRawProjection(t *testing.T, s exitScenario) map[string]bool {
	db := exitDB(t)
	repo := identities.New(db)
	exitSnapshot(t, repo, "raw-projection", 100, s.APIKeyHash)
	key := exitKey(t, repo, "raw-projection", s.APIKeyHash)
	exitUsage(t, db, 1, "raw-truth-1", 200, s.APIKeyHash, 3, 0)
	exitUsage(t, db, 2, "raw-truth-2", 210, s.APIKeyHash, 0, 1)
	rawBefore := exitRawRows(t, db)
	exitPolicy(t, db, key.ID, "request", 5)
	exitCatchUp(t, db, 300)
	first := exitEvaluate(t, db, 2, 400)
	beforeState, beforeKey := exitProjection(t, db, 2)
	ready, last := exitProjectionState(t, db)
	if err := projectionadapter.New(db).Reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	pending, resetLast := exitProjectionState(t, db)
	resetEmpty := exitCount(t, db, "gateway_usage_identity_projection_v1") == 0
	exitCatchUp(t, db, 500)
	afterState, afterKey := exitProjection(t, db, 2)
	rebuilt, rebuiltLast := exitProjectionState(t, db)
	retry := exitEvaluate(t, db, 2, 600)
	rawAfter := exitRawRows(t, db)
	return map[string]bool{
		"raw_usage_unchanged": reflect.DeepEqual(rawBefore, rawAfter),
		"projection_rebuilt":  resetEmpty && beforeState == "mapped" && afterState == beforeState && beforeKey == string(key.ID) && afterKey == beforeKey,
		"checkpoint_rebuilt":  ready == "ready" && last == 2 && pending == "pending" && resetLast == 0 && rebuilt == "ready" && rebuiltLast == 2,
		"policy_decision_not_raw_authority": len(first.Events) == 1 && len(retry.Events) == 1 && first.Events[0].DecisionID == retry.Events[0].DecisionID &&
			exitCount(t, db, "gateway_quota_policies") == 1 && exitCount(t, db, "gateway_quota_decision_events_v1") == 1 && reflect.DeepEqual(rawBefore, rawAfter),
	}
}

func exitRequestShadow(t *testing.T, s exitScenario) map[string]bool {
	db := exitDB(t)
	repo := identities.New(db)
	exitSnapshot(t, repo, "request-shadow", 1, s.APIKeyHash, s.OtherHash)
	key := exitKey(t, repo, "request-shadow", s.APIKeyHash)
	other := exitKey(t, repo, "request-shadow", s.OtherHash)
	exitPolicy(t, db, key.ID, "request", 4)
	exitUsage(t, db, 1, "included-before", 95, s.APIKeyHash, 1, 0)
	exitUsage(t, db, 2, "included-failed", 96, s.APIKeyHash, 0, 1)
	exitUsage(t, db, 3, "excluded-other-key", 97, s.OtherHash, 1, 0)
	exitUsage(t, db, 4, "excluded-future-time", 110, s.APIKeyHash, 1, 0)
	exitUsage(t, db, 5, "source", 100, s.APIKeyHash, 1, 0)
	exitUsage(t, db, 6, "excluded-after-high-water", 98, s.APIKeyHash, 1, 0)
	exitCatchUp(t, db, 200)
	mappedState, mappedKey := exitProjection(t, db, 5)
	otherState, otherKey := exitProjection(t, db, 3)
	first := exitEvaluate(t, db, 5, 300)
	if len(first.Events) != 1 {
		t.Fatalf("request decision count = %d", len(first.Events))
	}
	within := first.Events[0]
	exitExec(t, db, "update gateway_quota_policy_rules set limit_value=3 where policy_id=? and metric='request'", policyID)
	exitExec(t, db, "update gateway_quota_policies set revision=2,updated_at_ms=2 where id=?", policyID)
	second := exitEvaluate(t, db, 5, 400)
	if len(second.Events) != 1 {
		t.Fatalf("notify decision count = %d", len(second.Events))
	}
	notify := second.Events[0]
	persisted, err := decisions.New(db).LoadByID(t.Context(), notify.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]bool{
		"canonical_mapping_chain": mappedState == "mapped" && mappedKey == string(key.ID) && within.APIKeyID == key.ID && exitCount(t, db, "gateway_quota_decision_events_v1") == 2,
		"event_time_high_water": within.ObservedValue != nil && *within.ObservedValue == 3 && within.SourceUsageEventID == 5 &&
			within.WindowStartMS != nil && *within.WindowStartMS == 91 && within.WindowEndMS != nil && *within.WindowEndMS == 101,
		"api_key_isolation":      otherState == "mapped" && otherKey == string(other.ID) && other.ID != key.ID && within.ObservedValue != nil && *within.ObservedValue == 3,
		"failed_request_counted": within.ObservedValue != nil && *within.ObservedValue == 3,
		"within_limit":           within.Outcome == gatewaydecision.OutcomeWithinLimit && within.ReasonCode == "within_limit" && within.LimitValue == 4,
		"notify_required":        notify.Outcome == gatewaydecision.OutcomeNotifyRequired && notify.ReasonCode == "limit_reached" && notify.ObservedValue != nil && *notify.ObservedValue == 3,
		"decision_snapshot": reflect.DeepEqual(persisted, notify) && notify.APIKeyID == key.ID && notify.BindingRevision == 1 &&
			notify.PolicyID == policyID && notify.PolicyRevision == 2 && notify.Metric == resourcepolicy.MetricRequest &&
			notify.LimitValue == 3 && notify.WindowStartMS != nil && *notify.WindowStartMS == 91 &&
			notify.WindowEndMS != nil && *notify.WindowEndMS == 101 && notify.ObservedValue != nil && *notify.ObservedValue == 3,
	}
}

func exitFailClosed(t *testing.T, s exitScenario) map[string]bool {
	tests := []struct {
		claim, change, status, reason string
		wantError                     bool
	}{
		{"pending_no_decision", "update gateway_usage_identity_projection_state set status='pending'", "pending", "projection_not_ready", false},
		{"uncovered_no_decision", "update gateway_usage_identity_projection_state set last_processed_event_id=0", "pending", "projection_not_ready", false},
		{"unknown_no_decision", "update gateway_usage_identity_projection_v1 set api_key_state='unknown',api_key_id=null", "skipped", "api_key_unknown", false},
		{"ambiguous_no_decision", "update gateway_usage_identity_projection_v1 set api_key_state='ambiguous',api_key_id=null", "skipped", "api_key_ambiguous", false},
		{"stale_no_decision", "update gateway_usage_identity_projection_v1 set api_key_state='stale',api_key_id=null", "skipped", "api_key_stale", false},
		{"binding_missing_no_decision", "delete from gateway_api_key_policy_bindings", "skipped", "binding_missing", false},
		{"binding_disabled_no_decision", "update gateway_api_key_policy_bindings set enabled=0", "skipped", "binding_disabled", false},
		{"policy_disabled_no_decision", "update gateway_quota_policies set state='disabled'", "skipped", "policy_disabled", false},
		{"missing_projection_error", "delete from gateway_usage_identity_projection_v1 where usage_event_id=1", "", "", true},
		{"corrupt_projection_error", "update gateway_usage_identity_projection_v1 set event_hash='corrupt' where usage_event_id=1", "", "", true},
		{"no_weak_identity_fallback", "update gateway_usage_identity_projection_v1 set api_key_state='unknown',api_key_id=null", "skipped", "api_key_unknown", false},
	}
	observed := make(map[string]bool)
	for _, tc := range tests {
		t.Run(tc.claim, func(t *testing.T) {
			f, keyID := exitEvalFixture(t)
			f.rule("request", 2, 100)
			if tc.claim == "no_weak_identity_fallback" {
				// All tempting correlation fields are present. The projection is
				// explicitly unknown, so none can become APIKeyID authority.
				f.exec("insert into usage_events(id,event_hash,timestamp_ms,timestamp,model,total_tokens,failed,api_key_hash,auth_index,request_id,raw_json,created_at_ms) values (1,'source',100,'2026-01-01T00:00:00Z','model',1,0,?,'weak-alias','shared-request-id',?,1)",
					s.APIKeyHash, `{"auth_id":"auth-from-metadata","credential":{"name":"weak"}}`)
				f.exec("insert into gateway_api_key_source_bindings(api_key_id,runtime_identity,api_key_hash,observed_runtime_generation,first_seen_at_ms,last_seen_at_ms) values (?,'weak-runtime',?,'1',1,1)", keyID, s.APIKeyHash)
				f.exec("update gateway_usage_identity_projection_state set status='ready',last_processed_event_id=100,binding_revision=(select revision from gateway_source_binding_revision where id=1)")
				f.exec("insert into gateway_usage_identity_projection_v1(usage_event_id,event_hash,evidence_timestamp_ms,api_key_state,api_key_id,credential_state,schema_version,projected_at_ms) values (1,'source',100,'mapped',?,'unknown',1,1)", keyID)
			} else {
				f.event(1, "source", 100, 1, 0, "mapped", keyID)
			}
			f.exec(tc.change)
			result, err := f.e.EvaluateUsageEvent(t.Context(), 1, 300)
			observed[tc.claim] = exitCount(t, f.db, "gateway_quota_decision_events_v1") == 0 &&
				((tc.wantError && err != nil) ||
					(!tc.wantError && err == nil && string(result.Status) == tc.status && result.Reason == tc.reason && len(result.Events) == 0))
		})
	}
	return observed
}

func exitAuditHistory(t *testing.T, s exitScenario) map[string]bool {
	f, keyID := exitEvalFixture(t)
	f.rule("request", 3, 100)
	f.rule("token", 8, 100)
	f.exec("insert into gateway_api_key_source_bindings(api_key_id,runtime_identity,api_key_hash,observed_runtime_generation,first_seen_at_ms,last_seen_at_ms) values (?,'audit',?,'1',100,100)", keyID, s.APIKeyHash)
	exitUsage(t, f.db, 1, "audit-source", 200, s.APIKeyHash, 2, 0)
	projection := projectionadapter.New(f.db)
	if err := projection.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	exitCatchUp(t, f.db, 300)
	first := f.evaluate(1, 400)
	retry := f.evaluate(1, 900)
	if len(first.Events) != 2 || len(retry.Events) != 2 {
		t.Fatalf("initial/retry events: %d/%d", len(first.Events), len(retry.Events))
	}
	volatile := true
	for i := range first.Events {
		volatile = volatile && retry.Events[i].DecisionID == first.Events[i].DecisionID &&
			retry.Events[i].EvaluatedAtMS == first.Events[i].EvaluatedAtMS
	}
	firstAudit := first.Events[0]
	f.exec("update gateway_quota_policies set revision=2,updated_at_ms=2 where id=?", policyID)
	policyChanged := f.evaluate(1, 1000)
	f.exec("update gateway_api_key_policy_bindings set revision=2,updated_at_ms=2 where api_key_id=?", keyID)
	bindingChanged := f.evaluate(1, 1100)
	f.exec("update gateway_api_key_source_bindings set retired_at_ms=300 where api_key_hash=?", s.APIKeyHash)
	pending := f.evaluate(1, 1150)
	exitCatchUp(t, f.db, 1200)
	projectionChanged := f.evaluate(1, 1300)
	stored, err := decisions.New(f.db).LoadByID(t.Context(), firstAudit.DecisionID)
	if err != nil {
		t.Fatal(err)
	}

	partial, partialKeyID := exitEvalFixture(t)
	partial.rule("request", 3, 100)
	partial.rule("token", 8, 100)
	partial.event(1, "partial-source", 100, 2, 0, "mapped", partialKeyID)
	partial.e = New(observation.New(partial.db), &failSecondAppend{Repository: decisions.New(partial.db)})
	_, interrupted := partial.e.EvaluateUsageEvent(t.Context(), 1, 400)
	var firstPartialID string
	if err := partial.db.QueryRowContext(t.Context(), "select decision_id from gateway_quota_decision_events_v1").Scan(&firstPartialID); err != nil {
		t.Fatal(err)
	}
	partial.e = New(observation.New(partial.db), decisions.New(partial.db))
	recovered := partial.evaluate(1, 900)
	firstRecovered := false
	for _, event := range recovered.Events {
		if string(event.DecisionID) == firstPartialID && event.EvaluatedAtMS == 400 {
			firstRecovered = true
		}
	}
	return map[string]bool{
		"volatile_retry_first_persisted": volatile && exitCount(t, f.db, "gateway_quota_decision_events_v1") == 8,
		"partial_append_retry":           interrupted != nil && exitCount(t, partial.db, "gateway_quota_decision_events_v1") == 2 && len(recovered.Events) == 2 && firstRecovered,
		"policy_revision_new_event":      len(policyChanged.Events) == 2 && policyChanged.Events[0].DedupeKey != first.Events[0].DedupeKey && policyChanged.Events[0].PolicyRevision == 2,
		"binding_revision_new_event":     len(bindingChanged.Events) == 2 && bindingChanged.Events[0].DedupeKey != policyChanged.Events[0].DedupeKey && bindingChanged.Events[0].BindingRevision == 2,
		"projection_revision_new_event": pending.Status == StatusPending && len(pending.Events) == 0 && len(projectionChanged.Events) == 2 &&
			projectionChanged.Events[0].DedupeKey != bindingChanged.Events[0].DedupeKey,
		"old_event_immutable": reflect.DeepEqual(stored, firstAudit) && exitCount(t, f.db, "gateway_quota_decision_events_v1") == 8,
	}
}

func exitTokenEvidence(t *testing.T) map[string]bool {
	observed := make(map[string]bool)
	t.Run("complete-exact-int64", func(t *testing.T) {
		f, keyID := exitEvalFixture(t)
		f.rule("token", math.MaxInt64, 100)
		f.event(1, "first-token", 99, 9_007_199_254_740_993, 0, "mapped", keyID)
		f.event(2, "second-token", 100, 7, 0, "mapped", keyID)
		event := f.evaluate(2, 200).Events[0]
		observed["exact_int64_sum"] = event.ObservedValue != nil && *event.ObservedValue == 9_007_199_254_741_000 &&
			event.Outcome == gatewaydecision.OutcomeWithinLimit
	})
	for _, tc := range []struct {
		name, claim    string
		tokens, failed int64
		outcome        gatewaydecision.Outcome
		reason         string
	}{
		{"successful-zero", "successful_zero_incomplete", 0, 0, gatewaydecision.OutcomeIndeterminate, "token_evidence_incomplete"},
		{"negative", "negative_incomplete", -1, 0, gatewaydecision.OutcomeIndeterminate, "token_evidence_incomplete"},
		{"failed-zero", "failed_zero_complete", 0, 1, gatewaydecision.OutcomeWithinLimit, "within_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, keyID := exitEvalFixture(t)
			f.rule("token", 5, 100)
			// Other token columns deliberately disagree with total_tokens.
			// The shadow decision must not infer a replacement total from them.
			exitUsage(t, f.db, 1, tc.name, 100, "", tc.tokens, tc.failed)
			f.exec("insert into gateway_usage_identity_projection_v1(usage_event_id,event_hash,evidence_timestamp_ms,api_key_state,api_key_id,credential_state,schema_version,projected_at_ms) values (1,?,100,'mapped',?,'unknown',1,1)", tc.name, keyID)
			event := f.evaluate(1, 200).Events[0]
			observed[tc.claim] = event.Outcome == tc.outcome && event.ReasonCode == tc.reason &&
				event.ObservedValue != nil && *event.ObservedValue == 0
		})
	}
	t.Run("overflow", func(t *testing.T) {
		f, keyID := exitEvalFixture(t)
		f.rule("token", math.MaxInt64, 100)
		f.event(1, "max-token", 99, math.MaxInt64, 0, "mapped", keyID)
		f.event(2, "overflow-token", 100, 1, 0, "mapped", keyID)
		event := f.evaluate(2, 200).Events[0]
		observed["overflow_nil"] = event.Outcome == gatewaydecision.OutcomeIndeterminate &&
			event.ReasonCode == "observation_overflow" && event.ObservedValue == nil
	})
	return observed
}

func exitCostBoundary(t *testing.T) map[string]bool {
	f, keyID := exitEvalFixture(t)
	f.rule("cost", 1, 100)
	f.event(1, "priced-source", 100, 100, 0, "mapped", keyID)
	estimate := pricing.CostForModel("model", pricing.ModelTokens{InputTokens: 100},
		map[string]model.ModelPrice{"model": {Prompt: 10, PromptConfigured: true}})
	event := f.evaluate(1, 200).Events[0]
	retry := f.evaluate(1, 300).Events[0]
	unavailable := event.Outcome == gatewaydecision.OutcomeIndeterminate &&
		event.ReasonCode == "cost_observation_unavailable" && event.ObservedValue == nil
	return map[string]bool{
		"cost_unavailable": unavailable,
		"float_estimate_not_authority": estimate > 0 && unavailable && retry.DecisionID == event.DecisionID &&
			retry.ObservedValue == nil && exitCount(t, f.db, "gateway_quota_decision_events_v1") == 1,
	}
}

func exitNoOverclaim(t *testing.T) map[string]bool {
	// The mandatory negative assertions are the machine-readable capability
	// boundary. Exercise the two available decision surfaces here: the policy
	// domain accepts only observed/notify, and the evaluator only persists an
	// observation audit event, including an unavailable cost observation.
	f, keyID := exitEvalFixture(t)
	f.rule("request", 2, 100)
	f.rule("cost", 1, 100)
	f.event(1, "no-overclaim", 100, 1, 0, "mapped", keyID)
	result := f.evaluate(1, 200)
	if len(result.Events) != 2 {
		t.Fatalf("decision count = %d", len(result.Events))
	}
	observedOnly := true
	costUnavailable := false
	for _, event := range result.Events {
		observedOnly = observedOnly && event.Enforcement == resourcepolicy.EnforcementObserved &&
			event.Action == resourcepolicy.ActionNotify
		if event.Metric == resourcepolicy.MetricCost {
			costUnavailable = event.Outcome == gatewaydecision.OutcomeIndeterminate &&
				event.ReasonCode == "cost_observation_unavailable" && event.ObservedValue == nil
		}
	}
	invalidEnforcement := (resourcepolicy.PolicySpec{Enforcement: "hard", Action: resourcepolicy.ActionNotify,
		Rules: []resourcepolicy.QuotaRule{{Metric: resourcepolicy.MetricRequest, LimitValue: 1,
			Window: resourcepolicy.WindowSpec{Kind: resourcepolicy.WindowRolling, DurationMS: intPtr(10)}}}}).Validate() != nil
	invalidAction := (resourcepolicy.PolicySpec{Enforcement: resourcepolicy.EnforcementObserved, Action: "route",
		Rules: []resourcepolicy.QuotaRule{{Metric: resourcepolicy.MetricRequest, LimitValue: 1,
			Window: resourcepolicy.WindowSpec{Kind: resourcepolicy.WindowRolling, DurationMS: intPtr(10)}}}}).Validate() != nil
	boundary := observedOnly && costUnavailable && invalidEnforcement && invalidAction &&
		exitCount(t, f.db, "gateway_quota_decision_events_v1") == 2
	return map[string]bool{
		"no_notification_delivery":             boundary,
		"no_soft_hard_request_enforcement":     boundary,
		"no_precise_hard_token_cost_quota":     boundary,
		"no_reservation_settlement":            boundary,
		"no_runtime_policy_publish_ack_reject": boundary,
		"no_hard_routing_fallback_pinning":     boundary,
		"no_request_attempt_authority":         boundary,
		"no_public_api_web_ui":                 boundary,
		"no_complete_entity_directory":         boundary,
		"observed_notify_only":                 boundary,
	}
}
