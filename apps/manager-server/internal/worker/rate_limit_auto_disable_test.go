package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	quotacooldownrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotacooldown"
	accountactionsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/accountaction"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	managerconfigsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestQuotaAutoDisableCandidateRequiresStrictCodexUsageLimit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	base := usage.Event{
		EventHash:        "evt-1",
		Failed:           true,
		FailStatusCode:   http.StatusTooManyRequests,
		FailBody:         `{"error":{"type":"usage_limit_reached","resets_in_seconds":60}}`,
		AuthFileSnapshot: "codex-auth.json",
		AuthIndex:        "auth-1",
		AccountSnapshot:  "user@example.com",
		Provider:         "codex",
	}

	candidate, ok := quotaAutoDisableCandidateFromEvent(base, "http://cpa", "key", now)
	if !ok {
		t.Fatalf("candidate not detected")
	}
	if candidate.FileName != "codex-auth.json" || candidate.AuthIndex != "auth-1" || candidate.DisplayAccount != "user@example.com" || candidate.AccountSnapshot != "user@example.com" {
		t.Fatalf("candidate identity = %#v", candidate)
	}
	if got := candidate.ResetAt.Unix(); got != 1_700_000_060 {
		t.Fatalf("reset unix = %d", got)
	}
	if candidate.ReasonCode != quotaReasonCodexUsageLimit || candidate.WindowKind != quotaWindowUnknown {
		t.Fatalf("candidate metadata = %#v", candidate)
	}

	cases := []struct {
		name   string
		mutate func(*usage.Event)
	}{
		{
			name: "broad quota exhausted text is ignored",
			mutate: func(event *usage.Event) {
				event.FailBody = `{"error":{"code":"quota_exhausted","message":"quota exhausted","resets_in_seconds":60}}`
			},
		},
		{
			name: "non 429 is ignored",
			mutate: func(event *usage.Event) {
				event.FailStatusCode = http.StatusPaymentRequired
			},
		},
		{
			name: "non codex provider is ignored",
			mutate: func(event *usage.Event) {
				event.Provider = "openai"
			},
		},
		{
			name: "missing explicit reset is ignored",
			mutate: func(event *usage.Event) {
				event.FailBody = `{"error":{"type":"usage_limit_reached"}}`
			},
		},
		{
			name: "legacy reset_at is ignored",
			mutate: func(event *usage.Event) {
				event.FailBody = `{"error":{"type":"usage_limit_reached","reset_at":1700000060}}`
			},
		},
		{
			name: "auth file snapshot required",
			mutate: func(event *usage.Event) {
				event.AuthFileSnapshot = ""
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := base
			tc.mutate(&event)
			if _, ok := quotaAutoDisableCandidateFromEvent(event, "http://cpa", "key", now); ok {
				t.Fatalf("candidate should not be detected")
			}
		})
	}
}

func TestQuotaAutoDisableCandidateAcceptsXAIIncludedFreeUsageExhausted(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, statusCode := range []int{http.StatusPaymentRequired, http.StatusTooManyRequests} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			event := usage.Event{
				EventHash:        "evt-xai-free-exhausted",
				Failed:           true,
				FailStatusCode:   statusCode,
				FailBody:         `{"code":"subscription:free-usage-exhausted","error":"You've used all the included free usage for model grok-4.5-build-free for now. Usage resets over a rolling 24-hour window — tokens (actual/limit): 2033137/2000000."}`,
				AuthFileSnapshot: "xai-auth.json",
				AuthIndex:        "auth-xai-1",
				AccountSnapshot:  "[邮箱]",
				Provider:         "xai",
			}

			candidate, ok := quotaAutoDisableCandidateFromEvent(event, "http://cpa", "key", now)
			if !ok {
				t.Fatal("xAI free-usage-exhausted candidate not detected")
			}
			if candidate.Provider != "xai" {
				t.Fatalf("provider = %q, want xai", candidate.Provider)
			}
			if candidate.FileName != "xai-auth.json" || candidate.AuthIndex != "auth-xai-1" {
				t.Fatalf("candidate identity = %#v", candidate)
			}
			if got, want := candidate.ResetAt, now.Add(24*time.Hour); !got.Equal(want) {
				t.Fatalf("reset time = %s, want %s", got, want)
			}
			if candidate.ReasonCode != quotaReasonXAIFreeUsage || candidate.WindowKind != quotaWindowRolling24H {
				t.Fatalf("candidate metadata = %#v", candidate)
			}
			var evidence usage.ProviderUsageMetadata
			if err := json.Unmarshal([]byte(candidate.EvidenceJSON), &evidence); err != nil {
				t.Fatalf("decode evidence: %v", err)
			}
			if evidence.Actual == nil || *evidence.Actual != 2_033_137 || evidence.Limit == nil || *evidence.Limit != 2_000_000 || evidence.Overage == nil || *evidence.Overage != 33_137 {
				t.Fatalf("evidence = %#v", evidence)
			}
		})
	}
}

func TestQuotaAutoDisableCandidateUsesEventTimestampForXAIEstimate(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	eventAt := now.Add(-6 * time.Hour)
	event := usage.Event{
		EventHash:        "evt-xai-event-time",
		TimestampMS:      eventAt.UnixMilli(),
		Failed:           true,
		FailStatusCode:   http.StatusTooManyRequests,
		FailBody:         `{"code":"subscription:free-usage-exhausted"}`,
		AuthFileSnapshot: "xai-auth.json",
		AuthIndex:        "auth-xai-1",
		Provider:         "xai",
	}
	candidate, ok := quotaAutoDisableCandidateFromEvent(event, "http://cpa", "key", now)
	if !ok {
		t.Fatal("xAI event should produce a candidate")
	}
	want := eventAt.Add(24 * time.Hour)
	if !candidate.ResetAt.Equal(want) {
		t.Fatalf("reset time = %s, want event time + 24h %s", candidate.ResetAt, want)
	}
	if candidate.ResetAt.Equal(now.Add(24 * time.Hour)) {
		t.Fatal("reset time was incorrectly based on processing time")
	}

	stale := event
	stale.TimestampMS = now.Add(-25 * time.Hour).UnixMilli()
	if _, ok := quotaAutoDisableCandidateFromEvent(stale, "http://cpa", "key", now); ok {
		t.Fatal("an already expired event-time estimate must not create a cooldown")
	}
}

func TestQuotaAutoDisableCandidatePrefersXAIExplicitResetSignals(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name      string
		event     usage.Event
		want      time.Time
		estimated bool
	}{
		{
			name: "retry after header ignored for free usage",
			event: usage.Event{
				ResponseMetadata: usage.ParseResponseHeaderMetadata(map[string]any{
					"Retry-After": []any{"90"},
				}, now),
			},
			want:      now.Add(24 * time.Hour),
			estimated: true,
		},
		{
			name: "billing period end",
			event: usage.Event{
				FailBody: fmt.Sprintf(
					`{"code":"subscription:free-usage-exhausted","billing_period_end":%d}`,
					now.Add(6*time.Hour).Unix(),
				),
			},
			want:      now.Add(6 * time.Hour),
			estimated: false,
		},
		{
			name: "code and reset split across event fields",
			event: usage.Event{
				FailBody: `{"code":"subscription:free-usage-exhausted"}`,
				RawJSON: fmt.Sprintf(
					`{"response":{"billing_period_end":%d}}`,
					now.Add(8*time.Hour).Unix(),
				),
			},
			want:      now.Add(8 * time.Hour),
			estimated: false,
		},
		{
			name: "absolute reset wins over nested relative reset",
			event: usage.Event{
				FailBody: fmt.Sprintf(
					`{"code":"subscription:free-usage-exhausted","a":{"reset_after_seconds":60},"z":{"billing_period_end":%d}}`,
					now.Add(10*time.Hour).Unix(),
				),
			},
			want:      now.Add(10 * time.Hour),
			estimated: false,
		},
		{
			name: "retry after body backoff ignored",
			event: usage.Event{
				FailBody: `{"code":"subscription:free-usage-exhausted","retry_after":60}`,
			},
			want:      now.Add(24 * time.Hour),
			estimated: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := tc.event
			event.EventHash = "evt-xai-reset"
			event.Failed = true
			event.FailStatusCode = http.StatusPaymentRequired
			if event.FailBody == "" {
				event.FailBody = `{"code":"subscription:free-usage-exhausted"}`
			}
			event.AuthFileSnapshot = "xai-auth.json"
			event.AuthIndex = "auth-xai-1"
			event.Provider = "xai"

			candidate, ok := quotaAutoDisableCandidateFromEvent(event, "http://cpa", "key", now)
			if !ok {
				t.Fatal("xAI free-usage-exhausted candidate not detected")
			}
			if !candidate.ResetAt.Equal(tc.want) {
				t.Fatalf("reset time = %s, want %s", candidate.ResetAt, tc.want)
			}
			var evidence usage.ProviderUsageMetadata
			if err := json.Unmarshal([]byte(candidate.EvidenceJSON), &evidence); err != nil {
				t.Fatalf("decode evidence: %v", err)
			}
			if evidence.RecoverAtMS != tc.want.UnixMilli() || evidence.RecoverAtEstimated != tc.estimated {
				t.Fatalf("evidence recovery = %#v, estimated want %v", evidence, tc.estimated)
			}
		})
	}
}

func TestXAIProviderUsageEvidencePreservesStructuredRecoverySource(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	recoverAt := now.Add(6 * time.Hour)
	event := usage.Event{
		Failed:           true,
		FailStatusCode:   http.StatusTooManyRequests,
		AuthFileSnapshot: "xai-auth.json",
		Provider:         "xai",
		RawJSON:          `{"response":{"reset_after_seconds":60}}`,
		ResponseMetadata: &usage.ResponseHeaderMetadata{ProviderUsage: &usage.ProviderUsageMetadata{
			Provider:    "xai",
			Kind:        usage.ProviderUsageKindIncludedFree,
			State:       usage.ProviderUsageStateExhausted,
			Code:        usage.ProviderUsageCodeXAIFree,
			RecoverAtMS: recoverAt.UnixMilli(),
		}},
	}
	candidate, ok := quotaAutoDisableCandidateFromEvent(event, "http://cpa", "key", now)
	if !ok {
		t.Fatal("structured xAI recovery did not produce a candidate")
	}
	var evidence usage.ProviderUsageMetadata
	if err := json.Unmarshal([]byte(candidate.EvidenceJSON), &evidence); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if evidence.RecoverAtMS != recoverAt.UnixMilli() || evidence.RecoverAtEstimated {
		t.Fatalf("structured provider recovery source was lost: %#v", evidence)
	}
}

func TestXAIResetMatchesSharedFixture(t *testing.T) {
	type fixtureCase struct {
		Name       string            `json:"name"`
		StatusCode int               `json:"statusCode"`
		Body       any               `json:"body"`
		Headers    map[string]string `json:"headers"`
		Expected   struct {
			Classification    string `json:"classification"`
			RetryAfterSeconds *int64 `json:"retryAfterSeconds"`
		} `json:"expected"`
	}
	data, err := os.ReadFile("../../../../tests/fixtures/xai-inspection-cases.json")
	if err != nil {
		t.Fatalf("read shared xAI fixtures: %v", err)
	}
	var fixtures []fixtureCase
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("decode shared xAI fixtures: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	for _, fixture := range fixtures {
		if fixture.Expected.Classification != "free_quota_exhausted" {
			continue
		}
		body, err := json.Marshal(fixture.Body)
		if err != nil {
			t.Fatalf("marshal fixture body: %v", err)
		}
		headerValues := map[string]any{}
		for key, value := range fixture.Headers {
			headerValues[key] = []any{value}
		}
		event := usage.Event{
			Failed:           true,
			FailStatusCode:   fixture.StatusCode,
			FailBody:         string(body),
			Provider:         "xai",
			ResponseMetadata: usage.ParseResponseHeaderMetadata(headerValues, now),
		}
		resetAt, ok := xaiFreeUsageResetTimeFromEvent(event, now)
		if !ok {
			t.Fatalf("fixture %q did not produce reset time", fixture.Name)
		}
		// Fixture RetryAfterSeconds is transport backoff only. Free-usage
		// credential cooldown uses the rolling 24h estimate unless the body
		// publishes an explicit quota reset field.
		want := now.Add(xaiFreeUsageCooldown)
		if !resetAt.Equal(want) {
			t.Fatalf("fixture %q reset = %s, want %s", fixture.Name, resetAt, want)
		}
	}
}

func TestQuotaAutoDisableCandidateAcceptsXAIIncludedFreeUsageExhaustedAliasesAndNestedCode(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name         string
		provider     string
		executorType string
	}{
		{name: "xai", provider: "xai"},
		{name: "x-ai", provider: "x-ai"},
		{name: "grok-with-xai-executor", provider: "grok", executorType: "XAIExecutor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := usage.Event{
				EventHash:            "evt-xai-nested-" + tc.name,
				Failed:               true,
				FailStatusCode:       http.StatusTooManyRequests,
				FailBody:             `{"error":{"code":"subscription:free-usage-exhausted","message":"rolling 24-hour window"}}`,
				AuthFileSnapshot:     "xai-auth.json",
				AuthIndex:            "auth-xai-1",
				Provider:             tc.provider,
				ExecutorType:         tc.executorType,
				AuthProviderSnapshot: tc.provider,
			}
			candidate, ok := quotaAutoDisableCandidateFromEvent(event, "http://cpa", "key", now)
			if !ok {
				t.Fatal("nested xAI free-usage-exhausted candidate not detected")
			}
			if candidate.Provider != "xai" {
				t.Fatalf("provider = %q, want normalized xai", candidate.Provider)
			}
		})
	}
}

func TestQuotaAutoDisableCandidateRejectsBareGrokWithoutXAIExecutor(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	event := usage.Event{
		EventHash:        "evt-grok-proxy",
		Failed:           true,
		FailStatusCode:   http.StatusTooManyRequests,
		FailBody:         `{"error":{"code":"subscription:free-usage-exhausted","message":"rolling 24-hour window"}}`,
		AuthFileSnapshot: "grok-proxy.json",
		AuthIndex:        "auth-1",
		Provider:         "grok",
		ExecutorType:     "OpenAICompatExecutor",
	}
	if _, ok := quotaAutoDisableCandidateFromEvent(event, "http://cpa", "key", now); ok {
		t.Fatal("bare grok without xAI executor must not auto-disable")
	}
}

func TestQuotaAutoDisableCandidateRejectsExecutorSubstringAndAcceptsNativeSnapshot(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	base := usage.Event{
		Failed:           true,
		FailStatusCode:   http.StatusTooManyRequests,
		FailBody:         `{"code":"subscription:free-usage-exhausted"}`,
		AuthFileSnapshot: "xai-auth.json",
		Provider:         "grok",
	}

	rejected := base
	rejected.ExecutorType = "NotXAIExecutor"
	if _, ok := quotaAutoDisableCandidateFromEvent(rejected, "http://cpa", "key", now); ok {
		t.Fatal("executor substring caused false native xAI match")
	}

	accepted := base
	accepted.AuthProviderSnapshot = "xai"
	if _, ok := quotaAutoDisableCandidateFromEvent(accepted, "http://cpa", "key", now); !ok {
		t.Fatal("native xAI auth snapshot was ignored")
	}

	conflicting := base
	conflicting.Provider = "openai-compatible-example"
	conflicting.ExecutorType = "XAIExecutor"
	if _, ok := quotaAutoDisableCandidateFromEvent(conflicting, "http://cpa", "key", now); ok {
		t.Fatal("conflicting provider identity was treated as native xAI")
	}
}

func TestQuotaAutoDisableCandidateRejectsUnrelatedXAIErrors(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	base := usage.Event{
		EventHash:        "evt-xai-error",
		Failed:           true,
		AuthFileSnapshot: "xai-auth.json",
		AuthIndex:        "auth-xai-1",
		Provider:         "xai",
	}

	cases := []struct {
		name string
		body string
		code int
	}{
		{name: "regional permission denied", body: `{"code":"permission-denied","error":"The model grok-4.5 is not available in your region."}`, code: http.StatusForbidden},
		{name: "bad credentials", body: `{"code":"unauthenticated:bad-credentials","error":"The OAuth2 access token could not be validated."}`, code: http.StatusForbidden},
		{name: "generic rate limit", body: `{"code":"rate-limited","error":"try again later"}`, code: http.StatusTooManyRequests},
		{name: "error text only mentions free usage code", body: `{"code":"rate-limited","error":"This is not subscription:free-usage-exhausted."}`, code: http.StatusTooManyRequests},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := base
			event.FailStatusCode = tc.code
			event.FailBody = tc.body
			if _, ok := quotaAutoDisableCandidateFromEvent(event, "http://cpa", "key", now); ok {
				t.Fatal("unrelated xAI error should not create quota cooldown")
			}
		})
	}
}

func TestExtendExistingCooldownKeepsEvidenceForLaterRecovery(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	now := time.Now()
	existingRecoverAt := now.Add(12 * time.Hour)
	existingEvidence := fmt.Sprintf(`{"provider":"xai","kind":"included_free_usage","code":"subscription:free-usage-exhausted","recover_at_ms":%d}`, existingRecoverAt.UnixMilli())
	if _, err := st.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName: "xai-auth.json",
		AuthIndex:    "auth-xai-1",
		Provider:     "xai",
		RecoverAtMS:  existingRecoverAt.UnixMilli(),
		Owner:        model.QuotaCooldownOwnerXAIFreeUsage,
		EvidenceJSON: existingEvidence,
		DisabledAtMS: now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("seed cooldown: %v", err)
	}

	worker := NewRateLimitAutoDisableWorker(st)
	candidate := quotaAutoDisableCandidate{
		FileName:     "xai-auth.json",
		AuthIndex:    "auth-xai-1",
		Provider:     "xai",
		Owner:        model.QuotaCooldownOwnerXAIFreeUsage,
		ResetAt:      now.Add(6 * time.Hour),
		EvidenceJSON: `{"provider":"xai","kind":"included_free_usage","code":"subscription:free-usage-exhausted","recover_at_ms":1}`,
	}
	if !worker.extendExistingCooldown(ctx, candidate, authFile{Name: candidate.FileName, AuthIndex: candidate.AuthIndex, Disabled: true}) {
		t.Fatal("existing cooldown was not extended")
	}

	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 1 || active[0].RecoverAtMS != existingRecoverAt.UnixMilli() || active[0].EvidenceJSON != existingEvidence {
		t.Fatalf("active cooldown = %#v", active)
	}
}

func TestExtendExistingCooldownKeepsWinningRecoveryMetadata(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	now := time.Now()
	existingRecoverAt := now.Add(12 * time.Hour)
	existingEvidence := fmt.Sprintf(`{"source":"codex-window","recover_at_ms":%d}`, existingRecoverAt.UnixMilli())
	if _, err := st.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName: "codex-auth.json",
		AuthIndex:    "auth-codex-1",
		Provider:     "codex",
		ReasonCode:   "weekly_limit",
		WindowKind:   "weekly",
		EvidenceJSON: existingEvidence,
		RecoverAtMS:  existingRecoverAt.UnixMilli(),
		Owner:        model.QuotaCooldownOwnerUsage429,
		EventHash:    "evt-weekly",
		DisabledAtMS: now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("seed cooldown: %v", err)
	}

	worker := NewRateLimitAutoDisableWorker(st)
	candidate := quotaAutoDisableCandidate{
		FileName:        "codex-auth.json",
		AuthIndex:       "auth-codex-1",
		AccountSnapshot: "user@example.com",
		AccountID:       "workspace-1",
		Provider:        "codex",
		Owner:           model.QuotaCooldownOwnerUsage429,
		ReasonCode:      "five_hour_limit",
		WindowKind:      "five_hour",
		ResetAt:         now.Add(6 * time.Hour),
		EventHash:       "evt-five-hour",
	}
	if !worker.extendExistingCooldown(ctx, candidate, authFile{
		Name:            candidate.FileName,
		AuthIndex:       candidate.AuthIndex,
		Provider:        candidate.Provider,
		AccountSnapshot: candidate.AccountSnapshot,
		AccountID:       candidate.AccountID,
		Disabled:        true,
	}) {
		t.Fatal("existing cooldown was not updated")
	}

	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 1 || active[0].RecoverAtMS != existingRecoverAt.UnixMilli() || active[0].ReasonCode != "weekly_limit" || active[0].WindowKind != "weekly" || active[0].EvidenceJSON != existingEvidence || active[0].EventHash != "evt-weekly" {
		t.Fatalf("active cooldown = %#v", active)
	}
}

func TestExtendExistingCooldownUsesAuthIndexWhenDisplayAccountChanges(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	now := time.Now()
	if _, err := st.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName:    "shared.json",
		AuthIndex:       "auth-1",
		AccountSnapshot: "alice@example.com",
		Provider:        "codex",
		EvidenceJSON:    codexCooldownIdentityEvidenceJSON("workspace-1", "alice@example.com"),
		RecoverAtMS:     now.Add(time.Hour).UnixMilli(),
		Owner:           model.QuotaCooldownOwnerUsage429,
		DisabledAtMS:    now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("seed cooldown: %v", err)
	}

	worker := NewRateLimitAutoDisableWorker(st)
	candidate := quotaAutoDisableCandidate{
		FileName:        "shared.json",
		AuthIndex:       "auth-1",
		DisplayAccount:  "new-label@example.com",
		AccountSnapshot: "alice@example.com",
		AccountID:       "workspace-1",
		Provider:        "codex",
		Owner:           model.QuotaCooldownOwnerUsage429,
		ResetAt:         now.Add(2 * time.Hour),
	}
	if !worker.extendExistingCooldown(ctx, candidate, authFile{
		Name:            candidate.FileName,
		AuthIndex:       candidate.AuthIndex,
		Provider:        candidate.Provider,
		AccountSnapshot: candidate.AccountSnapshot,
		AccountID:       candidate.AccountID,
		Disabled:        true,
	}) {
		t.Fatal("stable auth_index cooldown was not extended after display account changed")
	}

	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 1 || active[0].RecoverAtMS != candidate.ResetAt.UnixMilli() ||
		active[0].AuthIndex != candidate.AuthIndex || active[0].AccountSnapshot != candidate.AccountSnapshot {
		t.Fatalf("active cooldown = %#v", active)
	}
}

func TestExtendExistingCooldownRejectsDifferentFallbackIdentity(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	now := time.Now()
	if _, err := st.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName:    "shared.json",
		AccountSnapshot: "original@example.com",
		Provider:        "codex",
		RecoverAtMS:     now.Add(time.Hour).UnixMilli(),
		Owner:           model.QuotaCooldownOwnerUsage429,
		DisabledAtMS:    now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("seed cooldown: %v", err)
	}

	worker := NewRateLimitAutoDisableWorker(st)
	candidate := quotaAutoDisableCandidate{
		FileName:        "shared.json",
		DisplayAccount:  "replacement@example.com",
		AccountSnapshot: "replacement@example.com",
		Provider:        "codex",
		Owner:           model.QuotaCooldownOwnerUsage429,
		ResetAt:         now.Add(2 * time.Hour),
	}
	if worker.extendExistingCooldown(ctx, candidate, authFile{
		Name:            candidate.FileName,
		Provider:        "codex",
		AccountSnapshot: candidate.DisplayAccount,
		Disabled:        true,
	}) {
		t.Fatal("replacement credential inherited an existing fallback cooldown")
	}

	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 1 || active[0].AccountSnapshot != "original@example.com" {
		t.Fatalf("active cooldown = %#v, want original identity unchanged", active)
	}
}

func TestExtendExistingCooldownSelectsMatchingSharedFileIdentity(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	now := time.Now()
	for _, item := range []store.QuotaCooldownUpsert{
		{
			AuthFileName:    "shared.json",
			AuthIndex:       "alice-auth-1",
			AccountSnapshot: "alice@example.com",
			Provider:        "codex",
			EvidenceJSON:    codexCooldownIdentityEvidenceJSON("workspace-1", "alice@example.com"),
			RecoverAtMS:     now.Add(time.Hour).UnixMilli(),
			Owner:           model.QuotaCooldownOwnerUsage429,
			DisabledAtMS:    now.Add(-time.Hour).UnixMilli(),
		},
		{
			AuthFileName:    "shared.json",
			AuthIndex:       "bob-auth-1",
			AccountSnapshot: "bob@example.com",
			Provider:        "codex",
			EvidenceJSON:    codexCooldownIdentityEvidenceJSON("workspace-1", "bob@example.com"),
			RecoverAtMS:     now.Add(2 * time.Hour).UnixMilli(),
			Owner:           model.QuotaCooldownOwnerUsage429,
			DisabledAtMS:    now.Add(-time.Hour).UnixMilli(),
		},
	} {
		if _, err := st.UpsertQuotaCooldown(ctx, item); err != nil {
			t.Fatalf("seed cooldown: %v", err)
		}
	}

	worker := NewRateLimitAutoDisableWorker(st)
	candidate := quotaAutoDisableCandidate{
		FileName:        "shared.json",
		AuthIndex:       "bob-auth-1",
		DisplayAccount:  "bob@example.com",
		AccountSnapshot: "bob@example.com",
		AccountID:       "workspace-1",
		Provider:        "codex",
		Owner:           model.QuotaCooldownOwnerUsage429,
		ResetAt:         now.Add(3 * time.Hour),
	}
	if !worker.extendExistingCooldown(ctx, candidate, authFile{
		Name:            candidate.FileName,
		AuthIndex:       candidate.AuthIndex,
		Provider:        candidate.Provider,
		AccountSnapshot: candidate.DisplayAccount,
		AccountID:       candidate.AccountID,
		Disabled:        true,
	}) {
		t.Fatal("matching shared-file cooldown was not extended")
	}

	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("active cooldowns = %#v, want two identities", active)
	}
	recoverAtByAccount := map[string]int64{}
	for _, item := range active {
		recoverAtByAccount[item.AccountSnapshot] = item.RecoverAtMS
	}
	if recoverAtByAccount["alice@example.com"] != now.Add(time.Hour).UnixMilli() || recoverAtByAccount["bob@example.com"] != candidate.ResetAt.UnixMilli() {
		t.Fatalf("recoveries = %#v", recoverAtByAccount)
	}
}

func TestRateLimitAutoDisableWorkerSkipsOwnershipWithoutStableIdentity(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":       "runtime-auth",
				"name":     "auth.json",
				"provider": "codex",
				"disabled": false,
			}})
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			patchCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	NewRateLimitAutoDisableWorker(st).handleCandidate(context.Background(), quotaAutoDisableCandidate{
		BaseURL:        server.URL,
		ManagementKey:  "mgmt",
		FileName:       "auth.json",
		AuthIndex:      "auth-1",
		DisplayAccount: "auth.json",
		Provider:       "codex",
		ResetAt:        time.Now().Add(time.Hour),
	})

	if patchCalls != 0 {
		t.Fatalf("patch calls = %d, want 0", patchCalls)
	}
	active, err := st.QuotaCooldowns.ListActive(context.Background())
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active cooldowns = %#v, want none", active)
	}
}

func TestRateLimitAutoDisableWorkerPersistsVerifiedFallbackSnapshot(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-auth",
				"name":       "auth.json",
				"auth_index": "auth-1",
				"provider":   "codex",
				"account":    "verified@example.com",
				"account_id": "workspace-1",
				"disabled":   false,
			}})
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	NewRateLimitAutoDisableWorker(st).handleCandidate(context.Background(), quotaAutoDisableCandidate{
		BaseURL:        server.URL,
		ManagementKey:  "mgmt",
		FileName:       "auth.json",
		AuthIndex:      "auth-1",
		DisplayAccount: "auth.json",
		ResetAt:        time.Now().Add(time.Hour),
	})

	active, err := st.QuotaCooldowns.ListActive(context.Background())
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 1 || active[0].AuthIndex != "auth-1" || active[0].AccountSnapshot != "verified@example.com" || active[0].Provider != "codex" {
		t.Fatalf("active cooldowns = %#v, want verified provider/account fallback identity", active)
	}
}

func TestRateLimitAutoDisableWorkerRejectsFallbackIdentityWithoutProvider(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":       "runtime-auth",
				"name":     "auth.json",
				"account":  "verified@example.com",
				"disabled": false,
			}})
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			patchCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	NewRateLimitAutoDisableWorker(st).handleCandidate(context.Background(), quotaAutoDisableCandidate{
		BaseURL:        server.URL,
		ManagementKey:  "mgmt",
		FileName:       "auth.json",
		DisplayAccount: "verified@example.com",
		ResetAt:        time.Now().Add(time.Hour),
	})

	if patchCalls != 0 {
		t.Fatalf("patch calls = %d, want 0", patchCalls)
	}
	active, err := st.QuotaCooldowns.ListActive(context.Background())
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active cooldowns = %#v, want none", active)
	}
}

func TestMergeXAIProviderUsageEvidenceKeepsPrimaryRecoveryAndFillsUsage(t *testing.T) {
	primaryRecoverAt := int64(2_000_000_000_000)
	primary := fmt.Sprintf(
		`{"provider":"xai","kind":"included_free_usage","state":"exhausted","code":"subscription:free-usage-exhausted","recover_at_ms":%d,"recover_at_estimated":true}`,
		primaryRecoverAt,
	)
	supplemental := `{"provider":"xai","kind":"included_free_usage","state":"exhausted","code":"subscription:free-usage-exhausted","actual":1024413,"limit":1000000,"remaining":0,"overage":24413,"recover_at_ms":1900000000000}`

	mergedJSON := mergeXAIProviderUsageEvidence(primary, supplemental, primaryRecoverAt)
	var merged usage.ProviderUsageMetadata
	if err := json.Unmarshal([]byte(mergedJSON), &merged); err != nil {
		t.Fatalf("decode merged evidence: %v", err)
	}
	if merged.RecoverAtMS != primaryRecoverAt || !merged.RecoverAtEstimated {
		t.Fatalf("merged recovery = %#v", merged)
	}
	if merged.Actual == nil || *merged.Actual != 1_024_413 || merged.Limit == nil || *merged.Limit != 1_000_000 || merged.Overage == nil || *merged.Overage != 24_413 {
		t.Fatalf("merged usage = %#v", merged)
	}
}

func TestMergeXAIProviderUsageEvidenceMarksUnknownWinningRecoveryEstimated(t *testing.T) {
	supplemental := `{"provider":"xai","kind":"included_free_usage","state":"exhausted","code":"subscription:free-usage-exhausted","recover_at_ms":1900000000000,"recover_at_estimated":false}`
	mergedJSON := mergeXAIProviderUsageEvidence("", supplemental, 2000000000000)
	var merged usage.ProviderUsageMetadata
	if err := json.Unmarshal([]byte(mergedJSON), &merged); err != nil {
		t.Fatalf("decode merged evidence: %v", err)
	}
	if merged.RecoverAtMS != 2000000000000 || !merged.RecoverAtEstimated {
		t.Fatalf("unknown winning recovery was presented as provider-reported: %#v", merged)
	}

	matchingSupplemental := `{"provider":"xai","kind":"included_free_usage","state":"exhausted","code":"subscription:free-usage-exhausted","recover_at_ms":2000000000000,"recover_at_estimated":false}`
	mergedJSON = mergeXAIProviderUsageEvidence(
		`{"provider":"xai","kind":"included_free_usage","state":"exhausted","code":"subscription:free-usage-exhausted"}`,
		matchingSupplemental,
		2000000000000,
	)
	merged = usage.ProviderUsageMetadata{}
	if err := json.Unmarshal([]byte(mergedJSON), &merged); err != nil {
		t.Fatalf("decode matching supplemental evidence: %v", err)
	}
	if merged.RecoverAtMS != 2000000000000 || merged.RecoverAtEstimated {
		t.Fatalf("matching supplemental recovery should remain reported: %#v", merged)
	}
}

func TestQuotaAutoDisableCandidateUsesResponseHeaderReset(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	event := usage.Event{
		EventHash:        "evt-header-quota",
		Failed:           true,
		FailStatusCode:   http.StatusTooManyRequests,
		AuthFileSnapshot: "codex-auth.json",
		AuthIndex:        "auth-1",
		AccountSnapshot:  "user@example.com",
		Provider:         "codex",
		ResponseMetadata: usage.ParseResponseHeaderMetadata(map[string]any{
			"Retry-After":                     []any{"90"},
			"x-codex-rate-limit-reached-type": []any{"primary"},
		}, now),
		HeaderErrorKind: "rate_limit",
		HeaderErrorCode: "retry_after",
		HeaderTraceID:   "req-header",
	}
	candidate, ok := quotaAutoDisableCandidateFromEvent(event, "http://cpa", "key", now)
	if !ok {
		t.Fatal("candidate not detected")
	}
	if got := candidate.ResetAt.Unix(); got != now.Add(90*time.Second).Unix() {
		t.Fatalf("reset unix = %d", got)
	}
}

func TestQuotaAutoDisableCandidateUsesNormalizedHeaderMetadataWithoutFailBodyHeaders(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	payload := fmt.Sprintf(`{
		"timestamp": %q,
		"failed": true,
		"fail": {"status_code": 429, "body": "rate limit exceeded"},
		"provider": "codex",
		"model": "gpt-5.4",
		"endpoint": "POST /v1/chat/completions",
		"auth_file_snapshot": "codex-auth.json",
		"auth_index": "auth-1",
		"account_snapshot": "user@example.com",
		"response_headers": {
			"x-codex-rate-limit-reached-type": ["primary"],
			"x-codex-primary-used-percent": ["100"],
			"x-codex-primary-reset-after-seconds": ["300"],
			"x-codex-primary-window-minutes": ["300"]
		}
	}`, now.UTC().Format(time.RFC3339Nano))
	event, err := usage.NormalizeRaw([]byte(payload))
	if err != nil {
		t.Fatalf("normalize header-only quota event: %v", err)
	}
	if event.FailBody != "rate limit exceeded" || strings.Contains(event.FailBody, "x-codex") {
		t.Fatalf("fail body = %q", event.FailBody)
	}
	if event.ResponseMetadata == nil || event.ResponseMetadata.Quota == nil {
		t.Fatalf("response metadata = %#v", event.ResponseMetadata)
	}

	// Isolate the structured path: historical raw/failure fallbacks are covered
	// separately and must not be required for newly normalized events.
	event.RawJSON = ""
	candidate, ok := quotaAutoDisableCandidateFromEvent(event, "http://cpa", "key", now)
	if !ok {
		t.Fatal("candidate not detected from structured response metadata")
	}
	if got := candidate.ResetAt.Unix(); got != now.Add(5*time.Minute).Unix() {
		t.Fatalf("reset unix = %d", got)
	}
}

func TestQuotaAutoDisableCandidateUsesReachedWindowResetWithoutReachedType(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	event := usage.Event{
		EventHash:        "evt-header-quota-window",
		Failed:           true,
		FailStatusCode:   http.StatusTooManyRequests,
		AuthFileSnapshot: "codex-auth.json",
		AuthIndex:        "auth-1",
		AccountSnapshot:  "user@example.com",
		Provider:         "codex",
		ResponseMetadata: usage.ParseResponseHeaderMetadata(map[string]any{
			"x-codex-primary-used-percent":        []any{"100"},
			"x-codex-primary-reset-after-seconds": []any{"18000"},
			"x-codex-primary-window-minutes":      []any{"300"},
			"x-codex-secondary-used-percent":      []any{"20"},
			"x-codex-secondary-reset-at":          []any{now.Add(7 * 24 * time.Hour).UnixMilli()},
			"x-codex-secondary-window-minutes":    []any{"10080"},
		}, now),
		HeaderTraceID: "req-header",
	}
	candidate, ok := quotaAutoDisableCandidateFromEvent(event, "http://cpa", "key", now)
	if !ok {
		t.Fatal("candidate not detected")
	}
	if got := candidate.ResetAt.Unix(); got != now.Add(5*time.Hour).Unix() {
		t.Fatalf("reset unix = %d", got)
	}
	if candidate.WindowKind != "five_hour" {
		t.Fatalf("window kind = %q, want five_hour", candidate.WindowKind)
	}
}

func TestQuotaAutoDisableCandidateIgnoresUnreachedWindowResetWithoutRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	event := usage.Event{
		EventHash:        "evt-header-quota-unreached-window",
		Failed:           true,
		FailStatusCode:   http.StatusTooManyRequests,
		AuthFileSnapshot: "codex-auth.json",
		AuthIndex:        "auth-1",
		AccountSnapshot:  "user@example.com",
		Provider:         "codex",
		ResponseMetadata: usage.ParseResponseHeaderMetadata(map[string]any{
			"x-codex-primary-used-percent":        []any{"80"},
			"x-codex-primary-reset-after-seconds": []any{"18000"},
			"x-codex-primary-window-minutes":      []any{"300"},
			"x-codex-secondary-used-percent":      []any{"95"},
			"x-codex-secondary-reset-at":          []any{now.Add(7 * 24 * time.Hour).UnixMilli()},
			"x-codex-secondary-window-minutes":    []any{"10080"},
		}, now),
		HeaderErrorKind: "rate_limit",
		HeaderErrorCode: "usage_limit_reached",
		HeaderTraceID:   "req-header",
	}
	if _, ok := quotaAutoDisableCandidateFromEvent(event, "http://cpa", "key", now); ok {
		t.Fatal("unreached window reset should not create auto-disable candidate")
	}
}

func TestQuotaAutoDisableCandidateIgnoresGenericRetryAfterHeader(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	event := usage.Event{
		EventHash:        "evt-generic-retry-after",
		Failed:           true,
		FailStatusCode:   http.StatusTooManyRequests,
		AuthFileSnapshot: "codex-auth.json",
		AuthIndex:        "auth-1",
		AccountSnapshot:  "user@example.com",
		Provider:         "codex",
		ResponseMetadata: usage.ParseResponseHeaderMetadata(map[string]any{"Retry-After": []any{"90"}}, now),
		HeaderErrorKind:  "rate_limit",
		HeaderErrorCode:  "retry_after",
		HeaderTraceID:    "req-header",
	}

	if _, ok := quotaAutoDisableCandidateFromEvent(event, "http://cpa", "key", now); ok {
		t.Fatal("generic Retry-After header should not create auto-disable candidate")
	}
}

func TestRateLimitAutoDisableWorkerRejectsAmbiguousStatusMutationScope(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "shared.json", "name": "shared.json", "auth_index": "auth-1", "provider": "codex", "account": "user@example.com", "disabled": false},
				{"id": "runtime-auth-2", "name": "shared.json", "auth_index": "auth-2", "provider": "codex", "disabled": false},
			})
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			patchCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	worker := NewRateLimitAutoDisableWorker(st, collectorpkg.RuntimeConfig{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	})
	worker.handleCandidate(ctx, quotaAutoDisableCandidate{
		BaseURL:        server.URL,
		ManagementKey:  "mgmt",
		FileName:       "shared.json",
		AuthIndex:      "auth-1",
		DisplayAccount: "user@example.com",
		Provider:       "codex",
		ResetAt:        time.Now().Add(time.Hour),
		EventHash:      "evt-ambiguous-disable",
	})

	if patchCalls != 0 {
		t.Fatalf("patch calls = %d, want 0", patchCalls)
	}
	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active cooldowns = %#v, want none", active)
	}
}

func TestRateLimitAutoDisableWorkerRejectsSamePathReplacementIdentity(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-replacement",
				"name":       "codex-auth.json",
				"auth_index": "auth-1",
				"provider":   "codex",
				"account":    "replacement@example.com",
				"disabled":   false,
			}})
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			patchCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	worker := NewRateLimitAutoDisableWorker(st)
	worker.handleCandidate(context.Background(), quotaAutoDisableCandidate{
		BaseURL:         server.URL,
		ManagementKey:   "mgmt",
		FileName:        "codex-auth.json",
		AuthIndex:       "auth-1",
		DisplayAccount:  "original@example.com",
		AccountSnapshot: "original@example.com",
		Provider:        "codex",
		ResetAt:         time.Now().Add(time.Hour),
		EventHash:       "evt-replacement",
	})

	if patchCalls != 0 {
		t.Fatalf("patch calls = %d, want 0", patchCalls)
	}
	active, err := st.QuotaCooldowns.ListActive(context.Background())
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active cooldowns = %#v, want none", active)
	}
}

func TestRateLimitAutoDisableWorkerDoesNotRollbackSamePathReplacementAfterPersistenceFailure(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	getCalls := 0
	patchStates := make([]bool, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			getCalls++
			account := "original@example.com"
			runtimeID := "runtime-original"
			if getCalls > 1 {
				account = "replacement@example.com"
				runtimeID = "runtime-replacement"
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         runtimeID,
				"name":       "codex-auth.json",
				"auth_index": "auth-1",
				"provider":   "codex",
				"account":    account,
				"account_id": "workspace-1",
				"disabled":   getCalls > 1,
			}})
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			var payload struct {
				Disabled bool `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			patchStates = append(patchStates, payload.Disabled)
			if payload.Disabled {
				_ = st.Close()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	NewRateLimitAutoDisableWorker(st).handleCandidate(context.Background(), quotaAutoDisableCandidate{
		BaseURL:         server.URL,
		ManagementKey:   "mgmt",
		FileName:        "codex-auth.json",
		AuthIndex:       "auth-1",
		DisplayAccount:  "original@example.com",
		AccountSnapshot: "original@example.com",
		AccountID:       "workspace-1",
		Provider:        "codex",
		ResetAt:         time.Now().Add(time.Hour),
		EventHash:       "evt-persist-failure-replacement",
	})

	if getCalls != 2 {
		t.Fatalf("auth file reads = %d, want initial and rollback verification", getCalls)
	}
	if len(patchStates) != 1 || !patchStates[0] {
		t.Fatalf("patch states = %#v, replacement must not be enabled", patchStates)
	}
}

func TestRateLimitAutoDisableWorkerCompensatesAfterParentCancellation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	getCalls := 0
	patchStates := make([]bool, 0, 2)
	client := &http.Client{Transport: workerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method + " " + r.URL.Path {
		case http.MethodGet + " /v0/management/auth-files":
			getCalls++
			disabled := len(patchStates) > 0 && patchStates[len(patchStates)-1]
			body := fmt.Sprintf(
				`[{"id":"runtime-codex-7","name":"codex-auth.json","auth_index":"auth-1","provider":"codex","account":"user@example.com","account_id":"workspace-1","disabled":%t}]`,
				disabled,
			)
			return workerHTTPResponse(r, io.NopCloser(strings.NewReader(body))), nil
		case http.MethodPatch + " /v0/management/auth-files/status":
			var payload struct {
				Disabled bool `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, err
			}
			patchStates = append(patchStates, payload.Disabled)
			body := io.ReadCloser(io.NopCloser(strings.NewReader(`{"ok":true}`)))
			if payload.Disabled {
				body = &cancelAtEOFReadCloser{reader: strings.NewReader(`{"ok":true}`), cancel: cancel}
			}
			return workerHTTPResponse(r, body), nil
		default:
			return workerHTTPResponse(r, io.NopCloser(strings.NewReader(`{"error":"not found"}`))), nil
		}
	})}
	worker := NewRateLimitAutoDisableWorker(st)
	worker.client = client
	worker.handleCandidate(ctx, quotaAutoDisableCandidate{
		BaseURL:         "http://cpa.test",
		ManagementKey:   "mgmt",
		FileName:        "codex-auth.json",
		AuthIndex:       "auth-1",
		DisplayAccount:  "user@example.com",
		AccountSnapshot: "user@example.com",
		AccountID:       "workspace-1",
		Provider:        "codex",
		ResetAt:         time.Now().Add(time.Hour),
		EventHash:       "evt-parent-canceled",
	})

	if ctx.Err() != context.Canceled {
		t.Fatalf("parent context error = %v, want context.Canceled", ctx.Err())
	}
	if getCalls != 2 {
		t.Fatalf("auth file reads = %d, want initial and compensation verification", getCalls)
	}
	if len(patchStates) != 2 || !patchStates[0] || patchStates[1] {
		t.Fatalf("patch states = %#v, want [true false]", patchStates)
	}
}

func TestRateLimitAutoDisableWorkerCompensationHasTotalTimeout(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	getCalls := 0
	rollbackCanceled := make(chan error, 1)
	client := &http.Client{Transport: workerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method + " " + r.URL.Path {
		case http.MethodGet + " /v0/management/auth-files":
			getCalls++
			if getCalls > 1 {
				<-r.Context().Done()
				rollbackCanceled <- r.Context().Err()
				return nil, r.Context().Err()
			}
			return workerHTTPResponse(r, io.NopCloser(strings.NewReader(
				`[{"id":"runtime-codex-7","name":"codex-auth.json","auth_index":"auth-1","provider":"codex","account":"user@example.com","account_id":"workspace-1","disabled":false}]`,
			))), nil
		case http.MethodPatch + " /v0/management/auth-files/status":
			_ = st.Close()
			return workerHTTPResponse(r, io.NopCloser(strings.NewReader(`{"ok":true}`))), nil
		default:
			return workerHTTPResponse(r, io.NopCloser(strings.NewReader(`{"error":"not found"}`))), nil
		}
	})}
	worker := NewRateLimitAutoDisableWorker(st)
	worker.client = client
	worker.compensationTimeout = 25 * time.Millisecond
	started := time.Now()
	worker.handleCandidate(context.Background(), quotaAutoDisableCandidate{
		BaseURL:         "http://cpa.test",
		ManagementKey:   "mgmt",
		FileName:        "codex-auth.json",
		AuthIndex:       "auth-1",
		DisplayAccount:  "user@example.com",
		AccountSnapshot: "user@example.com",
		AccountID:       "workspace-1",
		Provider:        "codex",
		ResetAt:         time.Now().Add(time.Hour),
		EventHash:       "evt-compensation-timeout",
	})
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("bounded compensation elapsed = %s, want <= 500ms", elapsed)
	}
	select {
	case err := <-rollbackCanceled:
		if err != context.DeadlineExceeded {
			t.Fatalf("rollback context error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("rollback request did not observe the compensation deadline")
	}
}

func TestSharedMutationCoordinatorSerializesAccountActionAndQuotaWorker(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.SaveSetup(ctx, store.Setup{
		CPAUpstreamURL: "http://cpa.test",
		ManagementKey:  "mgmt",
	}); err != nil {
		t.Fatalf("save setup: %v", err)
	}
	candidate, err := st.UpsertAccountActionCandidate(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeDelete,
		Provider:          "codex",
		AuthFileName:      "shared.json",
		AuthIndex:         "auth-1",
		AccountSnapshot:   "user@example.com",
		AccountIDSnapshot: "acct-123",
		Reason:            "token revoked",
	})
	if err != nil {
		t.Fatalf("upsert account action candidate: %v", err)
	}

	var stateMu sync.Mutex
	getCalls := 0
	disabled := true
	patchStates := make([]bool, 0, 2)
	firstGetStarted := make(chan struct{})
	releaseFirstGet := make(chan struct{})
	client := &http.Client{Transport: workerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method + " " + r.URL.Path {
		case http.MethodGet + " /v0/management/auth-files":
			stateMu.Lock()
			getCalls++
			call := getCalls
			currentDisabled := disabled
			stateMu.Unlock()
			if call == 1 {
				close(firstGetStarted)
				<-releaseFirstGet
			}
			body := fmt.Sprintf(
				`[{"id":"runtime-auth-1","name":"shared.json","auth_index":"auth-1","provider":"codex","account":"user@example.com","account_id":"acct-123","disabled":%t}]`,
				currentDisabled,
			)
			return workerHTTPResponse(r, io.NopCloser(strings.NewReader(body))), nil
		case http.MethodPatch + " /v0/management/auth-files/status":
			var payload struct {
				Disabled bool `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, err
			}
			stateMu.Lock()
			disabled = payload.Disabled
			patchStates = append(patchStates, payload.Disabled)
			stateMu.Unlock()
			return workerHTTPResponse(r, io.NopCloser(strings.NewReader(`{"ok":true}`))), nil
		default:
			return workerHTTPResponse(r, io.NopCloser(strings.NewReader(`{"error":"not found"}`))), nil
		}
	})}
	coordinator := cpaauthfiles.NewMutationCoordinator()
	accountService := accountactionsvc.NewWithMutationCoordinator(
		st,
		managerconfigsvc.New(config.Config{}, st, collectorservice.New(nil)),
		coordinator,
		client,
	)
	quotaWorker := NewRateLimitAutoDisableWorkerWithMutationCoordinator(st, coordinator)
	quotaWorker.client = client

	accountDone := make(chan error, 1)
	go func() {
		_, enableErr := accountService.Enable(ctx, candidate.ID)
		accountDone <- enableErr
	}()
	select {
	case <-firstGetStarted:
	case <-time.After(time.Second):
		t.Fatal("account action did not start auth file verification")
	}

	quotaDone := make(chan struct{})
	go func() {
		quotaWorker.handleCandidate(ctx, quotaAutoDisableCandidate{
			BaseURL:         "http://cpa.test",
			ManagementKey:   "mgmt",
			FileName:        "shared.json",
			AuthIndex:       "auth-1",
			DisplayAccount:  "user@example.com",
			AccountSnapshot: "user@example.com",
			Provider:        "codex",
			ResetAt:         time.Now().Add(time.Hour),
			EventHash:       "evt-shared-coordinator",
		})
		close(quotaDone)
	}()

	time.Sleep(25 * time.Millisecond)
	stateMu.Lock()
	concurrentGetCalls := getCalls
	stateMu.Unlock()
	if concurrentGetCalls != 1 {
		close(releaseFirstGet)
		t.Fatalf("auth file reads before first mutation release = %d, want 1", concurrentGetCalls)
	}
	close(releaseFirstGet)
	select {
	case err := <-accountDone:
		if err != nil {
			t.Fatalf("enable account action: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("account action did not complete")
	}
	select {
	case <-quotaDone:
	case <-time.After(time.Second):
		t.Fatal("quota worker did not complete")
	}

	stateMu.Lock()
	defer stateMu.Unlock()
	if getCalls != 2 {
		t.Fatalf("auth file reads = %d, want one per serialized mutation", getCalls)
	}
	if len(patchStates) != 2 || patchStates[0] || !patchStates[1] {
		t.Fatalf("patch states = %#v, want [false true]", patchStates)
	}
}

func TestRateLimitAutoDisableWorkerDoesNotRecoverAmbiguousStatusMutationScope(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "shared.json", "name": "shared.json", "auth_index": "auth-1", "provider": "codex", "account": "user@example.com", "account_id": "workspace-1", "disabled": true},
				{"id": "runtime-auth-2", "name": "shared.json", "auth_index": "auth-2", "provider": "codex", "account": "other@example.com", "account_id": "workspace-1", "disabled": true},
			})
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			patchCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	now := time.Now()
	if _, err := st.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName:     "shared.json",
		AuthIndex:        "auth-1",
		AccountSnapshot:  "user@example.com",
		Provider:         "codex",
		EvidenceJSON:     codexCooldownIdentityEvidenceJSON("workspace-1", "user@example.com"),
		RecoverAtMS:      now.Add(-time.Minute).UnixMilli(),
		Owner:            model.QuotaCooldownOwnerUsage429,
		EventHash:        "evt-ambiguous-recovery",
		PreDisabledState: false,
		DisabledAtMS:     now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("upsert cooldown: %v", err)
	}

	worker := NewRateLimitAutoDisableWorker(st, collectorpkg.RuntimeConfig{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	})
	worker.enableDue(ctx, now)

	if patchCalls != 0 {
		t.Fatalf("patch calls = %d, want 0", patchCalls)
	}
	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 1 || !strings.Contains(active[0].LastError, "scope is ambiguous") {
		t.Fatalf("active cooldowns = %#v, want retained ambiguous failure", active)
	}
}

func TestRateLimitAutoDisableWorkerRejectsDuplicateCodexCredentialLocator(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	patchCalls := 0
	patchedName := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"id":         "runtime-alice",
					"name":       "shared.json",
					"auth_index": "shared-auth",
					"provider":   "codex",
					"account":    "alice@example.com",
					"account_id": "workspace-1",
					"disabled":   false,
				},
				{
					"id":         "runtime-bob",
					"name":       "shared.json",
					"auth_index": "shared-auth",
					"provider":   "codex",
					"account":    "bob@example.com",
					"account_id": "workspace-1",
					"disabled":   false,
				},
			})
		case "PATCH /v0/management/auth-files/status":
			var payload struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			patchCalls++
			patchedName = payload.Name
			if !payload.Disabled {
				http.Error(w, "expected disable", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	now := time.Now()
	NewRateLimitAutoDisableWorker(st).handleCandidate(context.Background(), quotaAutoDisableCandidate{
		BaseURL:         server.URL,
		ManagementKey:   "mgmt",
		FileName:        "shared.json",
		AuthIndex:       "shared-auth",
		DisplayAccount:  "alice@example.com",
		AccountSnapshot: "alice@example.com",
		AccountID:       "workspace-1",
		Provider:        "codex",
		ResetAt:         now.Add(time.Hour),
		EventHash:       "evt-shared-alice",
	})

	if patchCalls != 0 || patchedName != "" {
		t.Fatalf("patch target = %q with %d calls, want no mutation for duplicate locator", patchedName, patchCalls)
	}
	active, err := st.QuotaCooldowns.ListActive(context.Background())
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active cooldowns = %#v, want no cooldown for duplicate locator", active)
	}
}

func TestRateLimitAutoDisableWorkerRejectsDuplicateCodexRecoveryLocator(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	patchCalls := 0
	patchedName := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"id":         "runtime-alice",
					"name":       "shared.json",
					"auth_index": "shared-auth",
					"provider":   "codex",
					"account":    "alice@example.com",
					"account_id": "workspace-1",
					"disabled":   true,
				},
				{
					"id":         "runtime-bob",
					"name":       "shared.json",
					"auth_index": "shared-auth",
					"provider":   "codex",
					"account":    "bob@example.com",
					"account_id": "workspace-1",
					"disabled":   true,
				},
			})
		case "PATCH /v0/management/auth-files/status":
			var payload struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			patchCalls++
			patchedName = payload.Name
			if payload.Disabled {
				http.Error(w, "expected enable", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	if _, err := st.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName:     "shared.json",
		AuthIndex:        "shared-auth",
		AccountSnapshot:  "alice@example.com",
		Provider:         "codex",
		EvidenceJSON:     codexCooldownIdentityEvidenceJSON("workspace-1", "alice@example.com"),
		RecoverAtMS:      time.Now().Add(-time.Minute).UnixMilli(),
		Owner:            model.QuotaCooldownOwnerUsage429,
		EventHash:        "evt-shared-alice-recovery",
		PreDisabledState: false,
		DisabledAtMS:     time.Now().Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("seed cooldown: %v", err)
	}

	NewRateLimitAutoDisableWorker(st, collectorpkg.RuntimeConfig{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}).enableDue(ctx, time.Now())

	if patchCalls != 0 || patchedName != "" {
		t.Fatalf("patch target = %q with %d calls, want no mutation for duplicate locator", patchedName, patchCalls)
	}
	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 1 || !strings.Contains(active[0].LastError, "scope is ambiguous") {
		t.Fatalf("active cooldowns = %#v, want retained ambiguous failure", active)
	}
}

func TestRateLimitAutoDisableWorkerRecoversLegacyCodexCooldownWithoutIdentityEvidence(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-alice",
				"name":       "codex-auth.json",
				"auth_index": "auth-1",
				"provider":   "codex",
				"account":    "alice@example.com",
				"account_id": "workspace-1",
				"disabled":   true,
			}})
		case "PATCH /v0/management/auth-files/status":
			var payload struct {
				Disabled bool `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			patchCalls++
			if payload.Disabled {
				http.Error(w, "expected enable", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	if _, err := st.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName:     "codex-auth.json",
		AuthIndex:        "auth-1",
		Provider:         "codex",
		EvidenceJSON:     "",
		RecoverAtMS:      time.Now().Add(-time.Minute).UnixMilli(),
		Owner:            model.QuotaCooldownOwnerUsage429,
		PreDisabledState: false,
		DisabledAtMS:     time.Now().Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("seed legacy cooldown: %v", err)
	}

	NewRateLimitAutoDisableWorker(st, collectorpkg.RuntimeConfig{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}).enableDue(ctx, time.Now())

	if patchCalls != 1 {
		t.Fatalf("patch calls = %d, want one enable for legacy cooldown", patchCalls)
	}
	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active cooldowns = %#v, want legacy cooldown recovered", active)
	}
}

func TestRateLimitAutoDisableWorkerSkipsRecoveryAfterCredentialIdentityChanges(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-replacement",
				"name":       "codex-auth.json",
				"auth_index": "auth-1",
				"provider":   "codex",
				"account":    "replacement@example.com",
				"account_id": "workspace-1",
				"disabled":   true,
			}})
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			patchCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	now := time.Now()
	if _, err := st.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName:     "codex-auth.json",
		AuthIndex:        "auth-1",
		AccountSnapshot:  "original@example.com",
		Provider:         "codex",
		EvidenceJSON:     codexCooldownIdentityEvidenceJSON("workspace-1", "original@example.com"),
		RecoverAtMS:      now.Add(-time.Minute).UnixMilli(),
		Owner:            model.QuotaCooldownOwnerUsage429,
		PreDisabledState: false,
		DisabledAtMS:     now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("upsert cooldown: %v", err)
	}

	worker := NewRateLimitAutoDisableWorker(st, collectorpkg.RuntimeConfig{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	})
	worker.enableDue(ctx, now)

	if patchCalls != 0 {
		t.Fatalf("patch calls = %d, want 0", patchCalls)
	}
	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active cooldowns = %#v, want stale identity skipped", active)
	}
}

func TestRateLimitAutoDisableWorkerSkipsRecoveryWithoutStableIdentity(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	requestCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCalls++
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx := context.Background()
	now := time.Now()
	if _, err := st.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName:     "replaceable.json",
		AccountSnapshot:  "legacy@example.com",
		RecoverAtMS:      now.Add(-time.Minute).UnixMilli(),
		Owner:            model.QuotaCooldownOwnerUsage429,
		PreDisabledState: false,
		DisabledAtMS:     now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("upsert cooldown: %v", err)
	}

	worker := NewRateLimitAutoDisableWorker(st, collectorpkg.RuntimeConfig{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	})
	worker.enableDue(ctx, now)

	if requestCalls != 0 {
		t.Fatalf("request calls = %d, want 0", requestCalls)
	}
	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active cooldowns = %#v, want skipped", active)
	}
}

func TestRateLimitAutoDisableWorkerSerializesRecoveryAndCooldownExtension(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	now := time.Now()
	if _, err := st.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName:     "codex-auth.json",
		AuthIndex:        "auth-1",
		AccountSnapshot:  "user@example.com",
		Provider:         "codex",
		EvidenceJSON:     codexCooldownIdentityEvidenceJSON("workspace-1", "user@example.com"),
		RecoverAtMS:      now.Add(-time.Minute).UnixMilli(),
		Owner:            model.QuotaCooldownOwnerUsage429,
		PreDisabledState: false,
		DisabledAtMS:     now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("upsert cooldown: %v", err)
	}

	var stateMu sync.Mutex
	disabled := true
	getCalls := 0
	firstGetStarted := make(chan struct{})
	releaseFirstGet := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			stateMu.Lock()
			getCalls++
			call := getCalls
			currentDisabled := disabled
			stateMu.Unlock()
			if call == 1 {
				close(firstGetStarted)
				<-releaseFirstGet
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-auth-1",
				"name":       "codex-auth.json",
				"auth_index": "auth-1",
				"provider":   "codex",
				"account":    "user@example.com",
				"account_id": "workspace-1",
				"disabled":   currentDisabled,
			}})
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			var payload struct {
				Disabled bool `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			stateMu.Lock()
			disabled = payload.Disabled
			stateMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	worker := NewRateLimitAutoDisableWorker(st, collectorpkg.RuntimeConfig{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	})
	recoveryDone := make(chan struct{})
	go func() {
		worker.enableDue(ctx, now)
		close(recoveryDone)
	}()
	<-firstGetStarted

	extensionDone := make(chan struct{})
	go func() {
		worker.handleCandidate(ctx, quotaAutoDisableCandidate{
			BaseURL:         server.URL,
			ManagementKey:   "mgmt",
			FileName:        "codex-auth.json",
			AuthIndex:       "auth-1",
			DisplayAccount:  "user@example.com",
			AccountSnapshot: "user@example.com",
			AccountID:       "workspace-1",
			Provider:        "codex",
			ResetAt:         now.Add(time.Hour),
			EventHash:       "evt-extended",
		})
		close(extensionDone)
	}()

	time.Sleep(50 * time.Millisecond)
	stateMu.Lock()
	concurrentGetCalls := getCalls
	stateMu.Unlock()
	if concurrentGetCalls != 1 {
		close(releaseFirstGet)
		t.Fatalf("auth file reads before recovery release = %d, want serialized single read", concurrentGetCalls)
	}
	close(releaseFirstGet)
	<-recoveryDone
	<-extensionDone

	stateMu.Lock()
	finalDisabled := disabled
	stateMu.Unlock()
	if !finalDisabled {
		t.Fatal("credential was left enabled after a concurrent later cooldown event")
	}
	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 1 || active[0].RecoverAtMS != now.Add(time.Hour).UnixMilli() {
		t.Fatalf("active cooldowns = %#v, want later cooldown preserved", active)
	}
}

func TestRateLimitAutoDisableWorkerRecoversDueCooldownFromManagerRuntimeConfigAfterRestart(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	var mu sync.Mutex
	disabled := true
	patches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer db-management-key" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v0/management/auth-files":
			if r.Method != http.MethodGet {
				http.NotFound(w, r)
				return
			}
			mu.Lock()
			currentDisabled := disabled
			mu.Unlock()
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-codex-auth-1",
				"name":       "codex-auth.json",
				"auth_index": "auth-1",
				"provider":   "codex",
				"account":    "user@example.com",
				"account_id": "workspace-1",
				"disabled":   currentDisabled,
			}})
		case "/v0/management/auth-files/status":
			if r.Method != http.MethodPatch {
				http.NotFound(w, r)
				return
			}
			var item struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			disabled = item.Disabled
			patches++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case "/v0/management/usage-queue":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := st.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName:     "codex-auth.json",
		AuthIndex:        "auth-1",
		AccountSnapshot:  "user@example.com",
		Provider:         "codex",
		EvidenceJSON:     codexCooldownIdentityEvidenceJSON("workspace-1", "user@example.com"),
		RecoverAtMS:      time.Now().Add(-time.Minute).UnixMilli(),
		Owner:            model.QuotaCooldownOwnerUsage429,
		EventHash:        "evt-due",
		PreDisabledState: false,
		DisabledAtMS:     time.Now().Add(-2 * time.Minute).UnixMilli(),
	}); err != nil {
		t.Fatalf("upsert due cooldown: %v", err)
	}
	if err := st.SaveManagerConfig(ctx, store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    server.URL,
			ManagementKey: "db-management-key",
		},
		Collector: store.ManagerCollectorConfig{
			CollectorMode:  "http",
			BatchSize:      10,
			PollIntervalMS: 10,
		},
	}); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	manager := collectorpkg.NewManager(config.Config{CollectorMode: "http", PollInterval: 10 * time.Millisecond}, st)
	rateLimitWorker := NewRateLimitAutoDisableWorker(st)
	manager.SetUsageEventHandler(rateLimitWorker)
	collectorWorker := NewCollectorWorker(config.Config{CollectorMode: "http", PollInterval: 10 * time.Millisecond}, st, collectorservice.New(manager))
	collectorWorker.Start(ctx)

	waitForWorkerTest(t, func() bool {
		mu.Lock()
		patched := patches == 1 && !disabled
		mu.Unlock()
		if !patched {
			return false
		}
		active, err := st.QuotaCooldowns.ListActive(ctx)
		return err == nil && len(active) == 0
	})

	mu.Lock()
	gotPatches := patches
	gotDisabled := disabled
	mu.Unlock()
	if gotPatches != 1 {
		t.Fatalf("patches = %d, want 1", gotPatches)
	}
	if gotDisabled {
		t.Fatal("credential remained disabled after cooldown recovery")
	}

	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active cooldowns = %#v, want recovered", active)
	}
}

func TestRateLimitAutoDisableWorkerXAIEventDisablesAndRecoversEndToEnd(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	now := time.Now()
	oldRecoverAt := now.Add(7 * 24 * time.Hour)
	oldEvidence := fmt.Sprintf(`{"provider":"xai","kind":"included_free_usage","code":"subscription:free-usage-exhausted","model":"grok-old","actual":1000,"limit":1000,"recover_at_ms":%d}`, oldRecoverAt.UnixMilli())
	old, err := st.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName: "xai-auth.json",
		AuthIndex:    "auth-xai-1",
		Provider:     "xai",
		ReasonCode:   quotaReasonXAIFreeUsage,
		WindowKind:   quotaWindowRolling24H,
		EvidenceJSON: oldEvidence,
		RecoverAtMS:  oldRecoverAt.UnixMilli(),
		Owner:        model.QuotaCooldownOwnerXAIFreeUsage,
		EventHash:    "evt-xai-old",
		DisabledAtMS: now.Add(-time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatalf("seed stale xAI cooldown: %v", err)
	}

	disabled := false
	patches := []bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-management-key" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "runtime-xai-auth-1", "name": "xai-auth.json", "authIndex": "auth-xai-1", "provider": "xai", "disabled": disabled}})
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			var item struct {
				Disabled bool `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			disabled = item.Disabled
			patches = append(patches, item.Disabled)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	event := usage.Event{
		EventHash:        "evt-xai-e2e",
		Failed:           true,
		FailStatusCode:   http.StatusTooManyRequests,
		FailBody:         `{"code":"subscription:free-usage-exhausted","error":"rolling 24-hour window"}`,
		AuthFileSnapshot: "xai-auth.json",
		AuthIndex:        "auth-xai-1",
		Provider:         "xai",
	}
	candidate, ok := quotaAutoDisableCandidateFromEvent(event, server.URL, "test-management-key", now)
	if !ok {
		t.Fatal("xAI candidate not detected")
	}
	if candidate.EvidenceJSON == "" {
		t.Fatal("xAI candidate has no evidence")
	}

	worker := NewRateLimitAutoDisableWorker(st, collectorpkg.RuntimeConfig{CPAUpstreamURL: server.URL, ManagementKey: "test-management-key"})
	worker.handleCandidate(ctx, candidate)
	if !disabled || len(patches) != 1 || !patches[0] {
		t.Fatalf("disable state=%v patches=%#v", disabled, patches)
	}
	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 1 || active[0].ID == old.ID || active[0].Owner != model.QuotaCooldownOwnerXAIFreeUsage || active[0].Provider != "xai" || active[0].ReasonCode != quotaReasonXAIFreeUsage || active[0].WindowKind != quotaWindowRolling24H || active[0].EventHash != event.EventHash {
		t.Fatalf("xAI cooldown = %#v", active)
	}
	if active[0].EvidenceJSON != candidate.EvidenceJSON || strings.Contains(active[0].EvidenceJSON, "grok-old") {
		t.Fatalf("xAI cooldown carried stale evidence: %s", active[0].EvidenceJSON)
	}

	worker.enableDue(ctx, now.Add(24*time.Hour+time.Second))
	if disabled || len(patches) != 2 || patches[1] {
		t.Fatalf("recovery state=%v patches=%#v", disabled, patches)
	}
}

func TestRateLimitAutoDisableWorkerRecoversXAICooldownWithoutTouchingManualDisable(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	type authState struct {
		disabled bool
		patches  int
	}
	states := map[string]*authState{
		"xai-owned.json":  {disabled: true},
		"xai-manual.json": {disabled: true},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-management-key" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			files := []map[string]any{}
			for name, state := range states {
				files = append(files, map[string]any{"id": name, "name": name, "authIndex": name, "provider": "xai", "disabled": state.disabled})
			}
			_ = json.NewEncoder(w).Encode(files)
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			var item struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			state := states[item.Name]
			state.disabled = item.Disabled
			state.patches++
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	now := time.Now()
	for _, cooldown := range []store.QuotaCooldownUpsert{
		{AuthFileName: "xai-owned.json", AuthIndex: "xai-owned.json", Provider: "xai", RecoverAtMS: now.Add(-time.Minute).UnixMilli(), Owner: model.QuotaCooldownOwnerXAIFreeUsage, EventHash: "owned", PreDisabledState: false, DisabledAtMS: now.Add(-25 * time.Hour).UnixMilli()},
		{AuthFileName: "xai-manual.json", AuthIndex: "xai-manual.json", Provider: "xai", RecoverAtMS: now.Add(-time.Minute).UnixMilli(), Owner: model.QuotaCooldownOwnerXAIFreeUsage, EventHash: "manual", PreDisabledState: true, DisabledAtMS: now.Add(-25 * time.Hour).UnixMilli()},
	} {
		if _, err := st.UpsertQuotaCooldown(ctx, cooldown); err != nil {
			t.Fatalf("upsert cooldown: %v", err)
		}
	}

	worker := NewRateLimitAutoDisableWorker(st, collectorpkg.RuntimeConfig{CPAUpstreamURL: server.URL, ManagementKey: "test-management-key"})
	worker.enableDue(ctx, now)

	if states["xai-owned.json"].disabled || states["xai-owned.json"].patches != 1 {
		t.Fatalf("CPAMP-owned state = %#v, want enabled once", states["xai-owned.json"])
	}
	if !states["xai-manual.json"].disabled || states["xai-manual.json"].patches != 0 {
		t.Fatalf("manual state = %#v, want untouched disabled", states["xai-manual.json"])
	}
}

func TestRateLimitAutoDisableWorkerRollsBackEnableWhenRecoveryPersistenceFails(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	baseRepository := st.QuotaCooldowns
	st.QuotaCooldowns = &failMarkRecoveredQuotaCooldownRepository{
		Repository: baseRepository,
		remaining:  1,
		err:        errors.New("forced recovered marker failure"),
	}

	disabled := true
	patches := make([]bool, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-auth-1",
				"name":       "shared.json",
				"auth_index": "auth-1",
				"provider":   "codex",
				"account":    "user@example.com",
				"account_id": "workspace-1",
				"disabled":   disabled,
			}})
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			var payload struct {
				Disabled bool `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			disabled = payload.Disabled
			patches = append(patches, payload.Disabled)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	now := time.Now()
	if _, err := st.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName:     "shared.json",
		AuthIndex:        "auth-1",
		AccountSnapshot:  "user@example.com",
		Provider:         "codex",
		EvidenceJSON:     codexCooldownIdentityEvidenceJSON("workspace-1", "user@example.com"),
		RecoverAtMS:      now.Add(-time.Minute).UnixMilli(),
		Owner:            model.QuotaCooldownOwnerUsage429,
		EventHash:        "recovery-persistence-failure",
		PreDisabledState: false,
		DisabledAtMS:     now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("upsert cooldown: %v", err)
	}

	worker := NewRateLimitAutoDisableWorker(st, collectorpkg.RuntimeConfig{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "test-management-key",
	})
	worker.enableDue(ctx, now)

	if !disabled || len(patches) != 2 || patches[0] || !patches[1] {
		t.Fatalf("disabled=%v patches=%v, want enable followed by rollback disable", disabled, patches)
	}
	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 1 || !strings.Contains(active[0].LastError, "recovery marker persistence failed") {
		t.Fatalf("active cooldown = %#v, want retained ownership with failure evidence", active)
	}
}

func TestRateLimitAutoDisableWorkerPersistsAndRecoversAfterRestart(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	var mu sync.Mutex
	disabled := false
	type action struct {
		Name     string `json:"name"`
		Disabled bool   `json:"disabled"`
	}
	actions := make([]action, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-management-key" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v0/management/auth-files" && r.URL.Path != "/v0/management/auth-files/status" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			mu.Lock()
			currentDisabled := disabled
			mu.Unlock()
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-codex-auth-1",
				"name":       "codex-auth.json",
				"authIndex":  "auth-1",
				"provider":   "codex",
				"account":    "user@example.com",
				"account_id": "workspace-1",
				"disabled":   currentDisabled,
			}})
		case http.MethodPatch:
			if r.URL.Path != "/v0/management/auth-files/status" {
				http.NotFound(w, r)
				return
			}
			var item action
			if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			disabled = item.Disabled
			actions = append(actions, item)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	worker := NewRateLimitAutoDisableWorker(st, collectorpkg.RuntimeConfig{CPAUpstreamURL: server.URL, ManagementKey: "test-management-key"})
	worker.handleCandidate(ctx, quotaAutoDisableCandidate{
		BaseURL:         server.URL,
		ManagementKey:   "test-management-key",
		FileName:        "codex-auth.json",
		AuthIndex:       "auth-1",
		DisplayAccount:  "user@example.com",
		AccountSnapshot: "user@example.com",
		AccountID:       "workspace-1",
		Provider:        "codex",
		ResetAt:         time.Now().Add(time.Minute),
		EventHash:       "evt-quota",
	})

	mu.Lock()
	if len(actions) != 1 || actions[0].Name != "runtime-codex-auth-1" || !actions[0].Disabled || !disabled {
		t.Fatalf("disable actions = %#v disabled=%v", actions, disabled)
	}
	mu.Unlock()
	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active cooldowns = %#v", active)
	}
	if active[0].Owner != model.QuotaCooldownOwnerUsage429 || active[0].PreDisabledState {
		t.Fatalf("cooldown ownership = %#v", active[0])
	}

	// Simulate a process restart: a fresh worker recovers from the persisted record.
	restarted := NewRateLimitAutoDisableWorker(st, collectorpkg.RuntimeConfig{CPAUpstreamURL: server.URL, ManagementKey: "test-management-key"})
	restarted.enableDue(ctx, time.Now().Add(2*time.Minute))

	mu.Lock()
	defer mu.Unlock()
	if len(actions) != 2 {
		t.Fatalf("actions = %#v, want disable and enable", actions)
	}
	if actions[1].Name != "runtime-codex-auth-1" || actions[1].Disabled || disabled {
		t.Fatalf("enable action = %#v disabled=%v", actions[1], disabled)
	}
}

func TestRateLimitAutoDisableWorkerStartsNewCycleAfterExternalEnable(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	now := time.Now()
	old, err := st.UpsertQuotaCooldown(ctx, store.QuotaCooldownUpsert{
		AuthFileName:    "codex-auth.json",
		AuthIndex:       "auth-1",
		AccountSnapshot: "user@example.com",
		Provider:        "codex",
		EvidenceJSON:    codexCooldownIdentityEvidenceJSON("workspace-1", "user@example.com"),
		ReasonCode:      "weekly_limit",
		WindowKind:      "weekly",
		RecoverAtMS:     now.Add(7 * 24 * time.Hour).UnixMilli(),
		Owner:           model.QuotaCooldownOwnerUsage429,
		EventHash:       "evt-old-weekly",
		DisabledAtMS:    now.Add(-time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatalf("seed stale cooldown: %v", err)
	}

	var mu sync.Mutex
	disabled := false
	patchStates := make([]bool, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			mu.Lock()
			currentDisabled := disabled
			mu.Unlock()
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-auth-1",
				"name":       "codex-auth.json",
				"auth_index": "auth-1",
				"provider":   "codex",
				"account":    "user@example.com",
				"account_id": "workspace-1",
				"disabled":   currentDisabled,
			}})
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			var payload struct {
				Disabled bool `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			disabled = payload.Disabled
			patchStates = append(patchStates, payload.Disabled)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	newResetAt := now.Add(5 * time.Hour)
	worker := NewRateLimitAutoDisableWorker(st, collectorpkg.RuntimeConfig{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	})
	candidate := quotaAutoDisableCandidate{
		BaseURL:         server.URL,
		ManagementKey:   "mgmt",
		FileName:        "codex-auth.json",
		AuthIndex:       "auth-1",
		DisplayAccount:  "user@example.com",
		AccountSnapshot: "user@example.com",
		AccountID:       "workspace-1",
		Provider:        "codex",
		ReasonCode:      quotaReasonCodexUsageLimit,
		WindowKind:      "five_hour",
		ResetAt:         newResetAt,
		EventHash:       "evt-new-five-hour",
	}
	worker.handleCandidate(ctx, candidate)

	mu.Lock()
	statesAfterDisable := append([]bool(nil), patchStates...)
	disabledAfterDisable := disabled
	mu.Unlock()
	if len(statesAfterDisable) != 1 || !statesAfterDisable[0] || !disabledAfterDisable {
		t.Fatalf("patch states = %#v disabled=%v, want one disable", statesAfterDisable, disabledAfterDisable)
	}
	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active cooldowns = %#v, want one new cycle", active)
	}
	if active[0].ID == old.ID || active[0].RecoverAtMS != newResetAt.UnixMilli() || active[0].WindowKind != "five_hour" || active[0].EventHash != "evt-new-five-hour" {
		t.Fatalf("active cooldown = %#v, old = %#v", active[0], old)
	}
	newCycleID := active[0].ID

	worker.handleCandidate(ctx, candidate)
	mu.Lock()
	statesAfterRepeat := append([]bool(nil), patchStates...)
	mu.Unlock()
	if len(statesAfterRepeat) != 1 {
		t.Fatalf("patch states after repeated event = %#v, want no second disable", statesAfterRepeat)
	}
	active, err = st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns after repeated event: %v", err)
	}
	if len(active) != 1 || active[0].ID != newCycleID {
		t.Fatalf("active cooldowns after repeated event = %#v, want cycle %d", active, newCycleID)
	}

	worker.enableDue(ctx, newResetAt.Add(time.Second))
	mu.Lock()
	statesAfterRecovery := append([]bool(nil), patchStates...)
	disabledAfterRecovery := disabled
	mu.Unlock()
	if len(statesAfterRecovery) != 2 || statesAfterRecovery[1] || disabledAfterRecovery {
		t.Fatalf("patch states = %#v disabled=%v, want disable then enable", statesAfterRecovery, disabledAfterRecovery)
	}
	active, err = st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns after recovery: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active cooldowns after recovery = %#v, want none", active)
	}
}

func TestRateLimitAutoDisableWorkerSkipsSameNameCredentialWithoutAuthIndex(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "runtime-first", "name": "shared.json", "provider": "codex", "account": "first@example.com", "disabled": false},
				{"id": "runtime-second", "name": "shared.json", "provider": "codex", "account": "second@example.com", "disabled": false},
			})
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			patchCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	NewRateLimitAutoDisableWorker(st).handleCandidate(context.Background(), quotaAutoDisableCandidate{
		BaseURL:         server.URL,
		ManagementKey:   "mgmt",
		FileName:        "shared.json",
		DisplayAccount:  "second@example.com",
		AccountSnapshot: "second@example.com",
		Provider:        "codex",
		ResetAt:         time.Now().Add(time.Hour),
		EventHash:       "evt-no-auth-index",
	})

	if patchCalls != 0 {
		t.Fatalf("patch calls = %d, want 0", patchCalls)
	}
	active, err := st.QuotaCooldowns.ListActive(context.Background())
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active cooldowns = %#v, want none", active)
	}
}

func waitForWorkerTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before deadline")
}

type failMarkRecoveredQuotaCooldownRepository struct {
	quotacooldownrepo.Repository
	remaining int
	err       error
}

func (r *failMarkRecoveredQuotaCooldownRepository) MarkRecovered(ctx context.Context, id int64, recoveredAtMS int64) error {
	if r.remaining > 0 {
		r.remaining--
		return r.err
	}
	return r.Repository.MarkRecovered(ctx, id, recoveredAtMS)
}
