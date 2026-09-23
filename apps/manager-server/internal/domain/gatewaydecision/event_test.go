package gatewaydecision

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/resourcepolicy"
)

func number(v int64) *int64 { return &v }

func validEvent() QuotaDecisionEvent {
	return QuotaDecisionEvent{
		DecisionID: DecisionID(strings.Repeat("a", 32)), SchemaVersion: SchemaVersion,
		DedupeKey: DedupeKey(strings.Repeat("b", 64)),
		APIKeyID:  identity.APIKeyID(strings.Repeat("c", 32)), PolicyID: resourcepolicy.PolicyID(strings.Repeat("d", 32)),
		PolicyRevision: 1, BindingRevision: 1,
		Metric: resourcepolicy.MetricRequest, Enforcement: resourcepolicy.EnforcementObserved,
		Action: resourcepolicy.ActionNotify, Outcome: OutcomeWithinLimit, ReasonCode: "within_limit",
		LimitValue: 10, ObservedValue: number(9), WindowStartMS: number(100), WindowEndMS: number(200),
		SourceUsageEventID: 1, SourceEventFingerprint: strings.Repeat("e", 64),
		EvidenceTimestampMS: 150, EvaluatedAtMS: 250,
	}
}

func TestDecisionID(t *testing.T) {
	if reflect.TypeOf(DecisionID("")) == reflect.TypeOf(identity.APIKeyID("")) ||
		reflect.TypeOf(DecisionID("")) == reflect.TypeOf(identity.CredentialID("")) ||
		reflect.TypeOf(DecisionID("")) == reflect.TypeOf(resourcepolicy.PolicyID("")) {
		t.Fatal("DecisionID must be a distinct Go type")
	}
	first, err := NewDecisionID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewDecisionID()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 32 || first.Validate() != nil || first.String() != string(first) {
		t.Fatalf("invalid generated IDs: %q, %q", first, second)
	}
	parsed, err := ParseDecisionID(first.String())
	if err != nil || parsed != first {
		t.Fatalf("parse: %q, %v", parsed, err)
	}
	for _, raw := range []string{"", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("A", 32), strings.Repeat("g", 32)} {
		if _, err := ParseDecisionID(raw); !errors.Is(err, ErrInvalidDecisionID) {
			t.Fatalf("accepted malformed DecisionID %q: %v", raw, err)
		}
	}
}

func TestDedupeKey(t *testing.T) {
	valid := strings.Repeat("a", 64)
	key, err := ParseDedupeKey(valid)
	if err != nil || key.String() != valid {
		t.Fatalf("valid DedupeKey: %q, %v", key, err)
	}
	for _, raw := range []string{"", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		if _, err := ParseDedupeKey(raw); !errors.Is(err, ErrInvalidDedupeKey) {
			t.Fatalf("accepted malformed DedupeKey %q: %v", raw, err)
		}
	}
}

func TestEventOutcomesAndMetrics(t *testing.T) {
	for _, metric := range []resourcepolicy.Metric{resourcepolicy.MetricRequest, resourcepolicy.MetricToken, resourcepolicy.MetricCost} {
		e := validEvent()
		e.Metric = metric
		if metric == resourcepolicy.MetricCost {
			e.LimitValue = 1_000_000 // One USD in integer micro-USD.
			e.ObservedValue = number(999_999)
		}
		if err := e.Validate(); err != nil {
			t.Fatalf("metric %s: %v", metric, err)
		}
	}
	for _, tc := range []struct {
		outcome Outcome
		value   *int64
		window  bool
	}{
		{OutcomeWithinLimit, number(0), true},
		{OutcomeNotifyRequired, number(10), true},
		{OutcomeNotifyRequired, number(11), true},
		{OutcomeIndeterminate, nil, false},
		{OutcomeIndeterminate, number(2), false},
		{OutcomeIndeterminate, number(2), true},
	} {
		e := validEvent()
		e.Outcome, e.ObservedValue = tc.outcome, tc.value
		if !tc.window {
			e.WindowStartMS, e.WindowEndMS = nil, nil
		}
		if err := e.Validate(); err != nil {
			t.Fatalf("outcome %s: %v", tc.outcome, err)
		}
	}
}

func TestEventRejectsInvalidContract(t *testing.T) {
	cases := map[string]func(*QuotaDecisionEvent){
		"schema zero":               func(e *QuotaDecisionEvent) { e.SchemaVersion = 0 },
		"schema future":             func(e *QuotaDecisionEvent) { e.SchemaVersion = 2 },
		"policy revision zero":      func(e *QuotaDecisionEvent) { e.PolicyRevision = 0 },
		"policy revision overflow":  func(e *QuotaDecisionEvent) { e.PolicyRevision = resourcepolicy.Revision(math.MaxInt64) + 1 },
		"binding revision zero":     func(e *QuotaDecisionEvent) { e.BindingRevision = 0 },
		"binding revision overflow": func(e *QuotaDecisionEvent) { e.BindingRevision = resourcepolicy.Revision(math.MaxInt64) + 1 },
		"metric":                    func(e *QuotaDecisionEvent) { e.Metric = "bytes" },
		"enforcement":               func(e *QuotaDecisionEvent) { e.Enforcement = "hard" },
		"action":                    func(e *QuotaDecisionEvent) { e.Action = "block" },
		"outcome":                   func(e *QuotaDecisionEvent) { e.Outcome = "sent" },
		"reason empty":              func(e *QuotaDecisionEvent) { e.ReasonCode = "" },
		"reason long":               func(e *QuotaDecisionEvent) { e.ReasonCode = strings.Repeat("a", 65) },
		"reason upper":              func(e *QuotaDecisionEvent) { e.ReasonCode = "Limit_Reached" },
		"reason prose":              func(e *QuotaDecisionEvent) { e.ReasonCode = "provider error" },
		"reason colon":              func(e *QuotaDecisionEvent) { e.ReasonCode = "error:secret" },
		"reason slash":              func(e *QuotaDecisionEvent) { e.ReasonCode = "path/file" },
		"reason unicode":            func(e *QuotaDecisionEvent) { e.ReasonCode = "错误" },
		"limit zero":                func(e *QuotaDecisionEvent) { e.LimitValue = 0 },
		"observed negative":         func(e *QuotaDecisionEvent) { e.ObservedValue = number(-1) },
		"start only":                func(e *QuotaDecisionEvent) { e.WindowEndMS = nil },
		"end only":                  func(e *QuotaDecisionEvent) { e.WindowStartMS = nil },
		"window zero":               func(e *QuotaDecisionEvent) { e.WindowStartMS = number(0) },
		"window reversed":           func(e *QuotaDecisionEvent) { e.WindowEndMS = number(100) },
		"within no observation":     func(e *QuotaDecisionEvent) { e.ObservedValue = nil },
		"within no window":          func(e *QuotaDecisionEvent) { e.WindowStartMS, e.WindowEndMS = nil, nil },
		"within at limit":           func(e *QuotaDecisionEvent) { e.ObservedValue = number(10) },
		"notify below limit":        func(e *QuotaDecisionEvent) { e.Outcome, e.ObservedValue = OutcomeNotifyRequired, number(9) },
		"notify no observation":     func(e *QuotaDecisionEvent) { e.Outcome, e.ObservedValue = OutcomeNotifyRequired, nil },
		"notify no window": func(e *QuotaDecisionEvent) {
			e.Outcome, e.ObservedValue, e.WindowStartMS, e.WindowEndMS = OutcomeNotifyRequired, number(10), nil, nil
		},
		"source id":          func(e *QuotaDecisionEvent) { e.SourceUsageEventID = 0 },
		"source fingerprint": func(e *QuotaDecisionEvent) { e.SourceEventFingerprint = strings.Repeat("A", 64) },
		"raw legacy event hash": func(e *QuotaDecisionEvent) { e.SourceEventFingerprint = "legacy-event-123" },
		"evidence time":      func(e *QuotaDecisionEvent) { e.EvidenceTimestampMS = 0 },
		"evaluation time":    func(e *QuotaDecisionEvent) { e.EvaluatedAtMS = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := validEvent()
			mutate(&e)
			if err := e.Validate(); err == nil {
				t.Fatalf("accepted invalid event: %+v", e)
			}
		})
	}
}

func TestSameSemanticContent(t *testing.T) {
	e := validEvent()
	retry := validEvent()
	retry.DecisionID = DecisionID(strings.Repeat("f", 32))
	retry.ObservedValue = number(9)
	if !e.SameSemanticContent(retry) {
		t.Fatal("different DecisionID and pointer addresses must preserve semantics")
	}
	fields := reflect.TypeOf(e)
	for i := 0; i < fields.NumField(); i++ {
		field := fields.Field(i)
		if field.Name == "DecisionID" {
			continue
		}
		changed := e
		value := reflect.ValueOf(&changed).Elem().Field(i)
		switch value.Kind() {
		case reflect.String:
			value.SetString(value.String() + "x")
		case reflect.Int64:
			value.SetInt(value.Int() + 1)
		case reflect.Uint64:
			value.SetUint(value.Uint() + 1)
		case reflect.Pointer:
			other := reflect.New(value.Type().Elem())
			if !value.IsNil() {
				other.Elem().SetInt(value.Elem().Int() + 1)
			}
			value.Set(other)
		default:
			t.Fatalf("new semantic field %s requires a comparison test", field.Name)
		}
		if e.SameSemanticContent(changed) {
			t.Errorf("semantic comparison ignored %s", field.Name)
		}
	}
}
