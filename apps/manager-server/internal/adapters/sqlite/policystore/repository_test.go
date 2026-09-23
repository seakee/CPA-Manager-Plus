package policystore_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	identityadapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identitystore"
	adapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/policystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/resourcepolicy"
	identityports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/policystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

var ctx = context.Background()

func ms(v int64) *int64    { return &v }
func hash(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

func open(t *testing.T) (*sql.DB, ports.Repository, identityports.Repository) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "policy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, adapter.New(db), identityadapter.New(db)
}

func rolling() resourcepolicy.WindowSpec {
	return resourcepolicy.WindowSpec{Kind: resourcepolicy.WindowRolling, DurationMS: ms(5 * 60 * 60 * 1000)}
}

func policy(t *testing.T) resourcepolicy.QuotaPolicy {
	t.Helper()
	id, err := resourcepolicy.NewPolicyID()
	if err != nil {
		t.Fatal(err)
	}
	return resourcepolicy.QuotaPolicy{
		ID: id, Revision: 1, State: resourcepolicy.StateActive,
		PolicySpec: resourcepolicy.PolicySpec{Enforcement: resourcepolicy.EnforcementObserved,
			Action: resourcepolicy.ActionNotify, Rules: []resourcepolicy.QuotaRule{
				{Metric: resourcepolicy.MetricRequest, LimitValue: 10, Window: rolling()},
				{Metric: resourcepolicy.MetricToken, LimitValue: 1000, Window: resourcepolicy.WindowSpec{
					Kind: resourcepolicy.WindowFixed, DurationMS: ms(7 * 24 * 60 * 60 * 1000), AnchorAtMS: ms(100)}},
				{Metric: resourcepolicy.MetricCost, LimitValue: 5_000_000, Window: resourcepolicy.WindowSpec{
					Kind: resourcepolicy.WindowFixed, CalendarMonths: ms(1), AnchorAtMS: ms(100), Timezone: "America/Phoenix"}},
			}},
		CreatedAtMS: 1000, UpdatedAtMS: 1000,
	}
}

func mustPolicy(t *testing.T, repo ports.Repository) resourcepolicy.QuotaPolicy {
	t.Helper()
	p := policy(t)
	if err := repo.CreatePolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPolicyRoundTripAndWholeSpecCAS(t *testing.T) {
	_, repo, _ := open(t)
	p := mustPolicy(t, repo)
	got, err := repo.LoadPolicy(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Query order is metric order; compare by metric instead of insertion order.
	for _, want := range p.Rules {
		found := false
		for _, rule := range got.Rules {
			if rule.Metric == want.Metric {
				found = reflect.DeepEqual(rule, want)
				break
			}
		}
		if !found {
			t.Fatalf("rule did not round-trip: %+v in %+v", want, got.Rules)
		}
	}
	if got.Rules[0].Metric != resourcepolicy.MetricCost || got.Rules[0].LimitValue != 5_000_000 {
		t.Fatalf("cost lost exact micro-USD limit: %+v", got.Rules)
	}
	// Reordering rules is a semantic no-op; expected revision is still required.
	noOp, err := repo.ReplacePolicySpec(ctx, p.ID, 1, p.PolicySpec, 900)
	if err != nil || noOp.Revision != 1 || noOp.UpdatedAtMS != 1000 {
		t.Fatalf("no-op: %+v %v", noOp, err)
	}
	changed := p.PolicySpec
	changed.Rules = []resourcepolicy.QuotaRule{{Metric: resourcepolicy.MetricCost, LimitValue: 250_000, Window: resourcepolicy.WindowSpec{
		Kind: resourcepolicy.WindowFixed, CalendarMonths: ms(1), AnchorAtMS: ms(100), Timezone: "UTC"}}}
	updated, err := repo.ReplacePolicySpec(ctx, p.ID, 1, changed, 900)
	if err != nil || updated.Revision != 2 || updated.UpdatedAtMS != 1001 {
		t.Fatalf("replace: %+v %v", updated, err)
	}
	got, err = repo.LoadPolicy(ctx, p.ID)
	if err != nil || !reflect.DeepEqual(got, updated) || len(got.Rules) != 1 {
		t.Fatalf("whole replacement: %+v %+v %v", got, updated, err)
	}
	if _, err := repo.ReplacePolicySpec(ctx, p.ID, 1, changed, 2000); !errors.Is(err, ports.ErrRevisionConflict) {
		t.Fatalf("stale replacement: %v", err)
	}
	if _, err := repo.ReplacePolicySpec(ctx, p.ID, 1, changed, 2000); !errors.Is(err, ports.ErrRevisionConflict) {
		t.Fatalf("stale semantic no-op: %v", err)
	}
	disabled, err := repo.SetPolicyState(ctx, p.ID, 2, resourcepolicy.StateDisabled, 800)
	if err != nil || disabled.Revision != 3 || disabled.UpdatedAtMS != 1002 {
		t.Fatalf("disable: %+v %v", disabled, err)
	}
	if _, err := repo.SetPolicyState(ctx, p.ID, 2, resourcepolicy.StateDisabled, 3000); !errors.Is(err, ports.ErrRevisionConflict) {
		t.Fatalf("stale state no-op: %v", err)
	}
	active, err := repo.SetPolicyState(ctx, p.ID, 3, resourcepolicy.StateActive, 900)
	if err != nil || active.Revision != 4 || active.UpdatedAtMS != 1003 {
		t.Fatalf("re-enable: %+v %v", active, err)
	}
}

func TestPolicyTransactionRollback(t *testing.T) {
	db, repo, _ := open(t)
	p := policy(t)
	if _, err := db.Exec(`create trigger fail_token before insert on gateway_quota_policy_rules
		when new.metric = 'token' begin select raise(fail, 'injected rule failure'); end`); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreatePolicy(ctx, p); err == nil {
		t.Fatal("create succeeded despite rule failure")
	}
	var n int
	if err := db.QueryRow(`select count(*) from gateway_quota_policies where id = ?`, p.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial policy create: count=%d err=%v", n, err)
	}
	if _, err := db.Exec(`drop trigger fail_token`); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreatePolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create trigger fail_cost before insert on gateway_quota_policy_rules
		when new.metric = 'cost' begin select raise(fail, 'injected replacement failure'); end`); err != nil {
		t.Fatal(err)
	}
	replacement := p.PolicySpec
	replacement.Rules = []resourcepolicy.QuotaRule{{Metric: resourcepolicy.MetricCost, LimitValue: 250_000, Window: rolling()}}
	if _, err := repo.ReplacePolicySpec(ctx, p.ID, 1, replacement, 2000); err == nil {
		t.Fatal("replace succeeded despite rule failure")
	}
	got, err := repo.LoadPolicy(ctx, p.ID)
	if err != nil || got.Revision != 1 || len(got.Rules) != 3 || got.UpdatedAtMS != 1000 {
		t.Fatalf("partial replacement: %+v %v", got, err)
	}
}

func TestSQLiteConstraints(t *testing.T) {
	db, repo, _ := open(t)
	p := mustPolicy(t, repo)
	for _, row := range []struct{ field, value string }{
		{"state", "draft"}, {"enforcement", "soft"}, {"enforcement", "hard"},
		{"enforcement", "block"}, {"enforcement", "deny"}, {"enforcement", "reserve"}, {"action", "block"}, {"action", "deny"},
	} {
		if _, err := db.Exec(`update gateway_quota_policies set `+row.field+` = ? where id = ?`, row.value, p.ID); err == nil {
			t.Fatalf("SQLite accepted %s=%s", row.field, row.value)
		}
	}
	if _, err := db.Exec(`update gateway_quota_policies set revision = 0 where id = ?`, p.ID); err == nil {
		t.Fatal("SQLite accepted revision 0")
	}
	if _, err := db.Exec(`update gateway_quota_policies set id = ? where id = ?`, strings.Repeat("A", 32), p.ID); err == nil {
		t.Fatal("SQLite accepted uppercase PolicyID")
	}
	if _, err := db.Exec(`insert into gateway_quota_policies values (?,1,'active','observed','notify',1,1)`, "not-a-policy-id"); err == nil {
		t.Fatal("SQLite accepted malformed PolicyID")
	}
	for _, metric := range []string{"request", "other"} {
		_, err := db.Exec(`insert into gateway_quota_policy_rules
			(policy_id,metric,limit_value,window_kind,duration_ms,timezone) values (?,?,?,?,?,?)`,
			p.ID, metric, 1, "rolling", 1, "")
		if err == nil {
			t.Fatalf("SQLite accepted duplicate or unknown metric %q", metric)
		}
	}
	p2 := policy(t)
	p2.Rules = p2.Rules[:1]
	if err := repo.CreatePolicy(ctx, p2); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		kind                     string
		duration, months, anchor any
		timezone                 string
	}{
		{"rolling", nil, nil, nil, ""}, {"rolling", int64(1), nil, int64(1), ""},
		{"rolling", int64(1), int64(1), nil, ""}, {"fixed", int64(1), nil, nil, ""},
		{"fixed", int64(1), int64(1), int64(1), ""}, {"fixed", nil, int64(1), int64(1), ""},
		{"fixed", int64(1), nil, int64(1), "UTC"}, {"rolling", int64(0), nil, nil, ""},
		{"fixed", nil, int64(-1), int64(1), "UTC"},
	} {
		_, err := db.Exec(`insert into gateway_quota_policy_rules
			(policy_id,metric,limit_value,window_kind,duration_ms,calendar_months,anchor_at_ms,timezone)
			values (?,?,?,?,?,?,?,?)`, p2.ID, "token", 1, row.kind, row.duration, row.months, row.anchor, row.timezone)
		if err == nil {
			t.Fatalf("SQLite accepted invalid window %+v", row)
		}
	}
	if _, err := db.Exec(`update gateway_quota_policy_rules set limit_value = 0 where policy_id = ?`, p.ID); err == nil {
		t.Fatal("SQLite accepted zero limit")
	}
	if _, err := db.Exec(`update gateway_quota_policy_rules set limit_value = 1.5 where policy_id = ?`, p.ID); err == nil {
		t.Fatal("SQLite accepted floating-point authoritative limit")
	}
	if _, err := db.Exec(`update gateway_quota_policies set revision = 1.5 where id = ?`, p.ID); err == nil {
		t.Fatal("SQLite accepted fractional revision")
	}
	if _, err := db.Exec(`update gateway_quota_policy_rules set duration_ms = null where policy_id = ? and metric = 'request'`, p.ID); err == nil {
		t.Fatal("SQLite accepted null rolling duration")
	}
	if _, err := db.Exec(`insert into gateway_quota_policy_rules
		(policy_id,metric,limit_value,window_kind,duration_ms,timezone) values (?,?,?,?,?,?)`,
		strings.Repeat("a", 32), "request", 1, "rolling", 1, ""); err == nil {
		t.Fatal("SQLite accepted missing policy FK")
	}
}
