package resourcepolicy

import (
	"reflect"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
)

func pointer(v int64) *int64 { return &v }

func TestPolicyID(t *testing.T) {
	a, err := NewPolicyID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewPolicyID()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || len(a) != 32 {
		t.Fatalf("IDs are not unique 32-char values: %q %q", a, b)
	}
	if parsed, err := ParsePolicyID(a.String()); err != nil || parsed != a {
		t.Fatalf("parse: %q %v", parsed, err)
	}
	for _, raw := range []string{"", "1", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "gggggggggggggggggggggggggggggggg", " 0000000000000000000000000000000"} {
		if _, err := ParsePolicyID(raw); err == nil {
			t.Fatalf("accepted invalid PolicyID %q", raw)
		}
	}
	if reflect.TypeOf(a) == reflect.TypeOf(identity.APIKeyID("")) || reflect.TypeOf(a) == reflect.TypeOf(identity.CredentialID("")) {
		t.Fatal("PolicyID must be a distinct Go type")
	}
}

func TestRuleLimitsAndWindows(t *testing.T) {
	rolling := WindowSpec{Kind: WindowRolling, DurationMS: pointer(5 * 60 * 60 * 1000)}
	fixed := WindowSpec{Kind: WindowFixed, DurationMS: pointer(7 * 24 * 60 * 60 * 1000), AnchorAtMS: pointer(1234)}
	calendar := WindowSpec{Kind: WindowFixed, CalendarMonths: pointer(1), AnchorAtMS: pointer(1234), Timezone: "Asia/Shanghai"}
	for _, w := range []WindowSpec{rolling, fixed, calendar,
		{Kind: WindowFixed, CalendarMonths: pointer(1), AnchorAtMS: pointer(1234), Timezone: "America/Phoenix"},
		{Kind: WindowFixed, CalendarMonths: pointer(1), AnchorAtMS: pointer(1234), Timezone: "UTC"}} {
		if err := w.Validate(); err != nil {
			t.Fatalf("valid window %+v: %v", w, err)
		}
	}
	for _, w := range []WindowSpec{
		{Kind: WindowRolling, DurationMS: pointer(1), AnchorAtMS: pointer(1)},
		{Kind: WindowRolling, DurationMS: pointer(1), CalendarMonths: pointer(1)},
		{Kind: WindowFixed, DurationMS: pointer(1)},
		{Kind: WindowFixed, DurationMS: pointer(1), CalendarMonths: pointer(1), AnchorAtMS: pointer(1)},
		{Kind: WindowFixed, CalendarMonths: pointer(1), AnchorAtMS: pointer(1)},
		{Kind: WindowFixed, CalendarMonths: pointer(1), AnchorAtMS: pointer(1), Timezone: "Mars/Base"},
		{Kind: WindowFixed, CalendarMonths: pointer(1), AnchorAtMS: pointer(1), Timezone: "Local"},
		{Kind: WindowFixed, DurationMS: pointer(1), AnchorAtMS: pointer(1), Timezone: "UTC"},
		{Kind: WindowRolling, DurationMS: pointer(0)},
		{Kind: WindowFixed, CalendarMonths: pointer(-1), AnchorAtMS: pointer(1), Timezone: "UTC"},
		{Kind: WindowFixed, CalendarMonths: pointer(1), AnchorAtMS: pointer(0), Timezone: "UTC"},
	} {
		if err := w.Validate(); err == nil {
			t.Fatalf("accepted invalid window %+v", w)
		}
	}
	valid := PolicySpec{Enforcement: EnforcementObserved, Action: ActionNotify, Rules: []QuotaRule{
		{Metric: MetricRequest, LimitValue: 10, Window: rolling},
		{Metric: MetricToken, LimitValue: 200, Window: fixed},
		{Metric: MetricCost, LimitValue: 5_000_000, Window: calendar}, // exactly $5.00 in micro-USD
	}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.Rules[2].LimitValue = 250_000 // exactly $0.25 in micro-USD
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.Rules = nil
	if err := invalid.Validate(); err == nil {
		t.Fatal("empty rules accepted")
	}
	invalid.Rules = append([]QuotaRule(nil), valid.Rules...)
	invalid.Rules[2].Metric = MetricToken
	if err := invalid.Validate(); err == nil {
		t.Fatal("duplicate metric accepted")
	}
	invalid.Rules = append([]QuotaRule(nil), valid.Rules...)
	invalid.Rules[0].LimitValue = 0
	if err := invalid.Validate(); err == nil {
		t.Fatal("zero limit accepted")
	}
	for _, enforcement := range []Enforcement{"soft", "hard", "block", "deny", "reserve"} {
		invalid = valid
		invalid.Enforcement = enforcement
		if err := invalid.Validate(); err == nil {
			t.Fatalf("accepted enforcement %q", enforcement)
		}
	}
	invalid = valid
	invalid.Action = "block"
	if err := invalid.Validate(); err == nil {
		t.Fatal("accepted block action")
	}
}
