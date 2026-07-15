package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
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
	if candidate.FileName != "codex-auth.json" || candidate.AuthIndex != "auth-1" || candidate.DisplayAccount != "user@example.com" {
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
		})
	}
}

func TestQuotaAutoDisableCandidatePrefersXAIExplicitResetSignals(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name  string
		event usage.Event
		want  time.Time
	}{
		{
			name: "retry after header",
			event: usage.Event{
				ResponseMetadata: usage.ParseResponseHeaderMetadata(map[string]any{
					"Retry-After": []any{"90"},
				}, now),
			},
			want: now.Add(90 * time.Second),
		},
		{
			name: "billing period end",
			event: usage.Event{
				FailBody: fmt.Sprintf(
					`{"code":"subscription:free-usage-exhausted","billing_period_end":%d}`,
					now.Add(6*time.Hour).Unix(),
				),
			},
			want: now.Add(6 * time.Hour),
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
			want: now.Add(8 * time.Hour),
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
		})
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
		if fixture.Expected.Classification != "free_quota_exhausted" || fixture.Expected.RetryAfterSeconds == nil {
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
		want := now.Add(time.Duration(*fixture.Expected.RetryAfterSeconds) * time.Second)
		if !resetAt.Equal(want) {
			t.Fatalf("fixture %q reset = %s, want %s", fixture.Name, resetAt, want)
		}
	}
}

func TestQuotaAutoDisableCandidateAcceptsXAIIncludedFreeUsageExhaustedAliasesAndNestedCode(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, provider := range []string{"xai", "x-ai", "grok"} {
		t.Run(provider, func(t *testing.T) {
			event := usage.Event{
				EventHash:        "evt-xai-nested-" + provider,
				Failed:           true,
				FailStatusCode:   http.StatusTooManyRequests,
				FailBody:         `{"error":{"code":"subscription:free-usage-exhausted","message":"rolling 24-hour window"}}`,
				AuthFileSnapshot: "xai-auth.json",
				AuthIndex:        "auth-xai-1",
				Provider:         provider,
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
				"name":       "codex-auth.json",
				"auth_index": "auth-1",
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
		Provider:         "codex",
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
		defer mu.Unlock()
		return patches == 1 && !disabled
	})

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

	disabled := false
	patches := []bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-management-key" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "xai-auth.json", "authIndex": "auth-xai-1", "disabled": disabled}})
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

	now := time.Now()
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

	ctx := context.Background()
	worker := NewRateLimitAutoDisableWorker(st, collectorpkg.RuntimeConfig{CPAUpstreamURL: server.URL, ManagementKey: "test-management-key"})
	worker.handleCandidate(ctx, candidate)
	if !disabled || len(patches) != 1 || !patches[0] {
		t.Fatalf("disable state=%v patches=%#v", disabled, patches)
	}
	active, err := st.QuotaCooldowns.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active cooldowns: %v", err)
	}
	if len(active) != 1 || active[0].Owner != model.QuotaCooldownOwnerXAIFreeUsage || active[0].Provider != "xai" || active[0].ReasonCode != quotaReasonXAIFreeUsage || active[0].WindowKind != quotaWindowRolling24H {
		t.Fatalf("xAI cooldown = %#v", active)
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
				files = append(files, map[string]any{"name": name, "authIndex": name, "disabled": state.disabled})
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
				"name":      "codex-auth.json",
				"authIndex": "auth-1",
				"disabled":  currentDisabled,
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
		BaseURL:        server.URL,
		ManagementKey:  "test-management-key",
		FileName:       "codex-auth.json",
		AuthIndex:      "auth-1",
		DisplayAccount: "user@example.com",
		Provider:       "codex",
		ResetAt:        time.Now().Add(time.Minute),
		EventHash:      "evt-quota",
	})

	mu.Lock()
	if len(actions) != 1 || actions[0].Name != "codex-auth.json" || !actions[0].Disabled || !disabled {
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
	if actions[1].Name != "codex-auth.json" || actions[1].Disabled || disabled {
		t.Fatalf("enable action = %#v disabled=%v", actions[1], disabled)
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
