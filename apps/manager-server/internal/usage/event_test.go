package usage

import (
	"math"
	"strings"
	"testing"
)

func TestCacheHitRateUsesNormalizedInputTotals(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		input         int64
		cached        int64
		cacheRead     int64
		cacheCreation int64
		want          float64
	}{
		{
			name:   "legacy openai cache is included in input",
			model:  "gpt-5.4",
			input:  1_000,
			cached: 400,
			want:   0.4,
		},
		{
			name:          "anthropic fine grained cache is outside input",
			model:         "claude-sonnet-4",
			input:         450,
			cacheRead:     300,
			cacheCreation: 50,
			want:          300.0 / 450.0,
		},
		{
			name:          "gpt 5.6 fine grained cache is included in input",
			model:         "openai/gpt-5.6-sol",
			input:         152_600,
			cacheRead:     151_000,
			cacheCreation: 1_000,
			want:          151_000.0 / 152_600.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CacheHitRate(tt.model, tt.input, tt.cached, tt.cacheRead, tt.cacheCreation)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("cache hit rate = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeCacheAccounting(t *testing.T) {
	tests := []struct {
		name      string
		context   CacheInputContext
		input     int64
		cached    int64
		read      int64
		creation  int64
		wantMode  string
		wantInput int64
		wantTotal int64
		wantRead  int64
	}{
		{name: "openai mirror is included", context: CacheInputContext{Provider: "openai", DisplayModel: "gpt-5.4"}, input: 1_000, cached: 400, read: 400, wantMode: CacheInputModeIncluded, wantInput: 600, wantTotal: 1_000, wantRead: 400},
		{name: "gpt 5.6 read and write are included", context: CacheInputContext{ExplicitMode: CacheInputModeIncluded, DisplayModel: "gpt-5.6-sol"}, input: 1_000, read: 300, creation: 100, wantMode: CacheInputModeIncluded, wantInput: 600, wantTotal: 1_000, wantRead: 300},
		{name: "claude cache is separate", context: CacheInputContext{ExplicitMode: CacheInputModeSeparate, DisplayModel: "claude-sonnet-4"}, input: 100, read: 300, creation: 50, wantMode: CacheInputModeSeparate, wantInput: 100, wantTotal: 450, wantRead: 300},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeCacheAccounting(tt.context, tt.input, tt.cached, 0, tt.read, tt.creation)
			if got.Mode != tt.wantMode || got.UncachedInputTokens != tt.wantInput || got.TotalInputTokens != tt.wantTotal || got.CacheReadTokens != tt.wantRead {
				t.Fatalf("accounting = %+v, want mode=%s input=%d total=%d read=%d", got, tt.wantMode, tt.wantInput, tt.wantTotal, tt.wantRead)
			}
		})
	}
}

func TestInferCacheInputModeUsesStrictFieldPriority(t *testing.T) {
	tests := []struct {
		name    string
		context CacheInputContext
		want    string
	}{
		{name: "explicit wins", context: CacheInputContext{ExplicitMode: CacheInputModeSeparate, ExecutorType: "OpenAICompatExecutor"}, want: CacheInputModeSeparate},
		{name: "explicit devin wins over claude executor", context: CacheInputContext{ExplicitMode: CacheInputModeReadIncludedCreationSeparate, ExecutorType: "ClaudeExecutor"}, want: CacheInputModeReadIncludedCreationSeparate},
		{name: "explicit included wins over devin executor", context: CacheInputContext{ExplicitMode: CacheInputModeIncluded, ExecutorType: "DevinExecutor"}, want: CacheInputModeIncluded},
		{name: "invalid explicit is ignored", context: CacheInputContext{ExplicitMode: "legacy", ExecutorType: "XAIExecutor"}, want: CacheInputModeIncluded},
		{name: "devin executor standalone", context: CacheInputContext{ExecutorType: "DevinExecutor"}, want: CacheInputModeReadIncludedCreationSeparate},
		{name: "devin executor beats claude alias", context: CacheInputContext{ExecutorType: "DevinExecutor", ResolvedModel: "claude-fable-5-1"}, want: CacheInputModeReadIncludedCreationSeparate},
		{name: "executor contains devin does not match DevinExecutor", context: CacheInputContext{ExecutorType: "SomeDevinLikeExecutor", ResolvedModel: "claude-fable-5-1"}, want: CacheInputModeSeparate},
		{name: "executor contains devin falls back to model", context: CacheInputContext{ExecutorType: "SomeDevinLikeExecutor", ResolvedModel: "gpt-5"}, want: CacheInputModeIncluded},
		{name: "devin provider beats claude alias", context: CacheInputContext{Provider: "devin", RequestedModel: "claude-fable-5-1"}, want: CacheInputModeReadIncludedCreationSeparate},
		{name: "devin provider prefix", context: CacheInputContext{Provider: "devin/custom"}, want: CacheInputModeReadIncludedCreationSeparate},
		{name: "devin snapshot beats claude alias", context: CacheInputContext{ProviderSnapshot: "devin", DisplayModel: "claude-fable-5-1"}, want: CacheInputModeReadIncludedCreationSeparate},
		{name: "devin exact model", context: CacheInputContext{ResolvedModel: "devin"}, want: CacheInputModeReadIncludedCreationSeparate},
		{name: "devin swe-2 model", context: CacheInputContext{ResolvedModel: "devin/swe-2"}, want: CacheInputModeReadIncludedCreationSeparate},
		{name: "devin claude hybrid model", context: CacheInputContext{ResolvedModel: "devin/claude-3-7-sonnet"}, want: CacheInputModeReadIncludedCreationSeparate},
		{name: "openai compat executor beats claude alias", context: CacheInputContext{ExecutorType: "OpenAICompatExecutor", ResolvedModel: "claude-sonnet-4"}, want: CacheInputModeIncluded},
		{name: "claude executor beats grok alias", context: CacheInputContext{ExecutorType: "ClaudeExecutor", ResolvedModel: "grok-4"}, want: CacheInputModeSeparate},
		{name: "claude executor beats kimi alias", context: CacheInputContext{ExecutorType: "ClaudeExecutor", RequestedModel: "kimi-k2"}, want: CacheInputModeSeparate},
		{name: "xai executor beats claude alias", context: CacheInputContext{ExecutorType: "XAIWebsocketsExecutor", DisplayModel: "claude-alias"}, want: CacheInputModeIncluded},
		{name: "provider beats snapshot", context: CacheInputContext{Provider: "anthropic", ProviderSnapshot: "openai"}, want: CacheInputModeSeparate},
		{name: "snapshot beats model", context: CacheInputContext{ProviderSnapshot: "moonshot", ResolvedModel: "claude-sonnet"}, want: CacheInputModeIncluded},
		{name: "resolved beats requested", context: CacheInputContext{ResolvedModel: "claude-sonnet", RequestedModel: "gpt-5"}, want: CacheInputModeSeparate},
		{name: "requested beats display", context: CacheInputContext{RequestedModel: "grok-4", DisplayModel: "claude-sonnet"}, want: CacheInputModeIncluded},
		{name: "xai model fallback", context: CacheInputContext{DisplayModel: "grok-4"}, want: CacheInputModeIncluded},
		{name: "kimi model fallback", context: CacheInputContext{DisplayModel: "moonshot/kimi-k2"}, want: CacheInputModeIncluded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InferCacheInputMode(tt.context, 10, 0); got != tt.want {
				t.Fatalf("mode = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRawCacheAccountingHintsFromJSON(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantMode  string
		wantTotal int64
		hasTotal  bool
	}{
		{name: "tokens snake case", raw: `{"tokens":{"cache_input_mode":"included_in_input","total_tokens":123}}`, wantMode: CacheInputModeIncluded, wantTotal: 123, hasTotal: true},
		{name: "usage camel case", raw: `{"usage":{"cacheInputMode":"separate_from_input","totalTokens":"456"}}`, wantMode: CacheInputModeSeparate, wantTotal: 456, hasTotal: true},
		{name: "devin mixed mode", raw: `{"tokens":{"cache_input_mode":"read_included_creation_separate","total_tokens":789}}`, wantMode: CacheInputModeReadIncludedCreationSeparate, wantTotal: 789, hasTotal: true},
		{name: "legacy detail wrapper", raw: `{"detail":{"tokens":{"cache_input_mode":"included_in_input","total_tokens":789}}}`, wantMode: CacheInputModeIncluded, wantTotal: 789, hasTotal: true},
		{name: "nested raw json", raw: `{"raw_json":"{\"tokens\":{\"cache_input_mode\":\"separate_from_input\",\"total_tokens\":321}}"}`, wantMode: CacheInputModeSeparate, wantTotal: 321, hasTotal: true},
		{name: "invalid values", raw: `{"cache_input_mode":"legacy","total_tokens":0}`, wantMode: "", hasTotal: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RawCacheAccountingHintsFromJSON(tt.raw)
			if got.ExplicitMode != tt.wantMode || got.ExplicitTotal != tt.wantTotal || got.HasExplicitTotal != tt.hasTotal || !got.ValidPayload {
				t.Fatalf("hints = %+v, want mode=%q total=%d hasTotal=%v", got, tt.wantMode, tt.wantTotal, tt.hasTotal)
			}
		})
	}
}

func TestNormalizeRawPrefersResolvedModelOverRequestedAndDisplayAliases(t *testing.T) {
	event, err := NormalizeRaw([]byte(`{
		"timestamp":"2026-07-15T00:00:00Z",
		"alias":"gpt-5-alias",
		"model":"grok-display",
		"resolved_model":"claude-sonnet-4",
		"tokens":{"input_tokens":100,"cache_read_tokens":20}
	}`))
	if err != nil {
		t.Fatalf("normalize raw: %v", err)
	}
	if event.ResolvedModel != "claude-sonnet-4" || event.RequestedModel != "gpt-5-alias" || event.Model != "gpt-5-alias" {
		t.Fatalf("models = resolved:%q requested:%q display:%q", event.ResolvedModel, event.RequestedModel, event.Model)
	}
	if event.AnalyticsModel != "gpt-5-alias" {
		t.Fatalf("analytics model = %q", event.AnalyticsModel)
	}
	if event.CacheInputMode != CacheInputModeSeparate || event.NormalizedTotalInputTokens != 120 {
		t.Fatalf("accounting = mode:%q total:%d", event.CacheInputMode, event.NormalizedTotalInputTokens)
	}
}

func TestNormalizeRawPreservesRequestedModelWhileCanonicalizingReasoningSuffix(t *testing.T) {
	event, err := NormalizeRaw([]byte(`{
		"timestamp":"2026-08-12T00:00:00Z",
		"alias":"deepseek-v4-flash(max)",
		"model":"deepseek-v4-flash",
		"reasoning_effort":"max"
	}`))
	if err != nil {
		t.Fatalf("NormalizeRaw: %v", err)
	}
	if event.Model != "deepseek-v4-flash(max)" || event.RequestedModel != "deepseek-v4-flash(max)" {
		t.Fatalf("requested models = model:%q requested:%q", event.Model, event.RequestedModel)
	}
	if event.AnalyticsModel != "deepseek-v4-flash" {
		t.Fatalf("analytics model = %q", event.AnalyticsModel)
	}
	if event.ResolvedModel != "deepseek-v4-flash" || event.ReasoningEffort != "max" {
		t.Fatalf("resolved/effort = %q/%q", event.ResolvedModel, event.ReasoningEffort)
	}
	wantHash := event.EventHash
	event.AnalyticsModel = "different-derived-value"
	if got := buildEventHash(event); got != wantHash {
		t.Fatalf("derived analytics model changed event hash: got %q want %q", got, wantHash)
	}
}

func TestCacheHitRateFromTotalsClampsMalformedData(t *testing.T) {
	if got := CacheHitRateFromTotals(1_500, 1_000); got != 1 {
		t.Fatalf("cache hit rate = %v, want 1", got)
	}
}

func TestIsLongContextInputBoundary(t *testing.T) {
	if IsLongContextInput(272_000) {
		t.Fatal("272000 input tokens should use standard pricing")
	}
	if !IsLongContextInput(272_001) {
		t.Fatal("272001 input tokens should use long-context pricing")
	}
}

func TestSafeRawJSONPreservesAuthIDWhileRedactingAPIKey(t *testing.T) {
	raw := `{
		"api_key": "sk-proj-super-secret-key-12345",
		"auth_id": "cpa-auth-target-id-42",
		"authId": "cpa-auth-target-id-42",
		"authorization": "Bearer secret-token-abcdef",
		"model": "gpt-4",
		"detail": {
			"api_key": "sk-nested-key",
			"auth_id": "cpa-auth-target-id-42"
		}
	}`

	sanitized := SafeRawJSON(raw)

	if strings.Contains(sanitized, "sk-proj-super-secret-key-12345") || strings.Contains(sanitized, "secret-token-abcdef") || strings.Contains(sanitized, "sk-nested-key") {
		t.Fatalf("SafeRawJSON leaked secret raw keys: %s", sanitized)
	}
	if !strings.Contains(sanitized, "[redacted]") {
		t.Fatalf("SafeRawJSON did not redact secret fields: %s", sanitized)
	}
	if !strings.Contains(sanitized, `"cpa-auth-target-id-42"`) {
		t.Fatalf("SafeRawJSON failed to preserve CPA Auth.ID for shadow projection: %s", sanitized)
	}
}

func TestNormalizeRawParsesRequestMetadataFields(t *testing.T) {
	raw := []byte(`{
		"timestamp":"2026-08-12T00:00:00Z",
		"model":"gpt-4o",
		"response_model":"gpt-4o-mini",
		"session_id":"sess-12345",
		"parent_session_id":"parent-67890",
		"access_token_sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"generate":true,
		"stream":false
	}`)
	event, err := NormalizeRaw(raw)
	if err != nil {
		t.Fatalf("NormalizeRaw failed: %v", err)
	}
	if event.ResponseModel != "gpt-4o-mini" {
		t.Errorf("ResponseModel = %q, want %q", event.ResponseModel, "gpt-4o-mini")
	}
	if event.SessionID != "sess-12345" {
		t.Errorf("SessionID = %q, want %q", event.SessionID, "sess-12345")
	}
	if event.ParentSessionID != "parent-67890" {
		t.Errorf("ParentSessionID = %q, want %q", event.ParentSessionID, "parent-67890")
	}
	if event.AccessTokenSHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("AccessTokenSHA256 = %q", event.AccessTokenSHA256)
	}
	if event.Generate == nil || *event.Generate != true {
		t.Errorf("Generate = %v, want true", event.Generate)
	}
	if event.Stream == nil || *event.Stream != false {
		t.Errorf("Stream = %v, want false", event.Stream)
	}

	payload := BuildPayload([]Event{event})
	api := payload.APIs[event.Endpoint]
	if api == nil || api.Models[event.Model] == nil || len(api.Models[event.Model].Details) != 1 {
		t.Fatalf("BuildPayload did not generate detail: %+v", payload)
	}
	detail := api.Models[event.Model].Details[0]
	if detail.ResponseModel != "gpt-4o-mini" ||
		detail.SessionID != "sess-12345" ||
		detail.ParentSessionID != "parent-67890" ||
		detail.AccessTokenSHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" ||
		detail.Generate == nil || *detail.Generate != true ||
		detail.Stream == nil || *detail.Stream != false {
		t.Fatalf("BuildPayload did not preserve request metadata: %+v", detail)
	}
}

func TestNormalizeRawLegacyPayloadLeavesMetadataFieldsNil(t *testing.T) {
	raw := []byte(`{
		"timestamp":"2026-08-12T00:00:00Z",
		"model":"gpt-4o"
	}`)
	event, err := NormalizeRaw(raw)
	if err != nil {
		t.Fatalf("NormalizeRaw failed: %v", err)
	}
	if event.ResponseModel != "" {
		t.Errorf("ResponseModel = %q, want empty", event.ResponseModel)
	}
	if event.SessionID != "" || event.ParentSessionID != "" || event.AccessTokenSHA256 != "" {
		t.Errorf("session fields not empty: %+v", event)
	}
	if event.Generate != nil {
		t.Errorf("Generate = %v, want nil for legacy payload", event.Generate)
	}
	if event.Stream != nil {
		t.Errorf("Stream = %v, want nil for legacy payload", event.Stream)
	}
}

func TestNormalizeRawRequestMetadataDoesNotAffectEventHash(t *testing.T) {
	baseRaw := []byte(`{
		"timestamp":"2026-08-12T00:00:00Z",
		"model":"gpt-4o",
		"tokens":10
	}`)
	withMetaRaw := []byte(`{
		"timestamp":"2026-08-12T00:00:00Z",
		"model":"gpt-4o",
		"tokens":10,
		"response_model":"gpt-4o-mini",
		"session_id":"sess-999",
		"parent_session_id":"parent-888",
		"access_token_sha256":"hash-777",
		"generate":false,
		"stream":true
	}`)

	evBase, err := NormalizeRaw(baseRaw)
	if err != nil {
		t.Fatalf("NormalizeRaw base: %v", err)
	}
	evMeta, err := NormalizeRaw(withMetaRaw)
	if err != nil {
		t.Fatalf("NormalizeRaw withMeta: %v", err)
	}

	if evBase.EventHash != evMeta.EventHash {
		t.Fatalf("EventHash mismatch: base=%q meta=%q; metadata must not alter event hash", evBase.EventHash, evMeta.EventHash)
	}
}

func TestReadOptionalBool(t *testing.T) {
	tests := []struct {
		input any
		want  *bool
	}{
		{nil, nil},
		{true, ptrBool(true)},
		{false, ptrBool(false)},
		{"true", ptrBool(true)},
		{"True", ptrBool(true)},
		{"TRUE", ptrBool(true)},
		{"false", ptrBool(false)},
		{"False", ptrBool(false)},
		{"FALSE", ptrBool(false)},
		{"1", ptrBool(true)},
		{"0", ptrBool(false)},
		{1, ptrBool(true)},
		{0, ptrBool(false)},
		{int64(1), ptrBool(true)},
		{int64(0), ptrBool(false)},
		{float64(1), ptrBool(true)},
		{float64(0), ptrBool(false)},
		{"invalid", nil},
		{42, nil},
	}

	for _, tc := range tests {
		raw := map[string]any{"val": tc.input}
		got := readOptionalBool(raw, "val")
		if tc.want == nil {
			if got != nil {
				t.Errorf("readOptionalBool(%v) = %v, want nil", tc.input, *got)
			}
		} else {
			if got == nil || *got != *tc.want {
				t.Errorf("readOptionalBool(%v) = %v, want %v", tc.input, got, *tc.want)
			}
		}
	}
}

func ptrBool(b bool) *bool {
	return &b
}

func TestNormalizeRawIssue838DevinRegression(t *testing.T) {
	raw := []byte(`{
		"provider": "devin",
		"executor_type": "DevinExecutor",
		"alias": "claude-fable-5-1",
		"tokens": {
			"input_tokens": 229788,
			"output_tokens": 1775,
			"cached_tokens": 228021,
			"cache_read_tokens": 228021,
			"cache_creation_tokens": 0,
			"total_tokens": 231563
		}
	}`)

	event, err := NormalizeRaw(raw)
	if err != nil {
		t.Fatalf("normalize raw: %v", err)
	}

	if event.CacheInputMode != CacheInputModeReadIncludedCreationSeparate {
		t.Errorf("CacheInputMode = %q, want %q", event.CacheInputMode, CacheInputModeReadIncludedCreationSeparate)
	}
	if event.NormalizedUncachedInputTokens != 1767 {
		t.Errorf("NormalizedUncachedInputTokens = %d, want 1767", event.NormalizedUncachedInputTokens)
	}
	if event.NormalizedTotalInputTokens != 229788 {
		t.Errorf("NormalizedTotalInputTokens = %d, want 229788", event.NormalizedTotalInputTokens)
	}
	if event.NormalizedCacheReadTokens != 228021 {
		t.Errorf("NormalizedCacheReadTokens = %d, want 228021", event.NormalizedCacheReadTokens)
	}
	if event.NormalizedCacheCreationTokens != 0 {
		t.Errorf("NormalizedCacheCreationTokens = %d, want 0", event.NormalizedCacheCreationTokens)
	}
	if event.TotalTokens != 231563 {
		t.Errorf("TotalTokens = %d, want 231563", event.TotalTokens)
	}
}

func TestDevinRepositoryRoundTrip(t *testing.T) {
	raw := []byte(`{
		"provider": "devin",
		"executor_type": "DevinExecutor",
		"alias": "claude-fable-5-1",
		"tokens": {
			"input_tokens": 229788,
			"output_tokens": 1775,
			"cached_tokens": 228021,
			"cache_read_tokens": 228021,
			"cache_creation_tokens": 0,
			"total_tokens": 231563
		}
	}`)

	event, err := NormalizeRaw(raw)
	if err != nil {
		t.Fatalf("normalize raw: %v", err)
	}

	// Simulating repository prepareUsageEvent二次归一化:
	// ExplicitMode 使用第一次归一化产生的 event.CacheInputMode
	recalculated := NormalizeCacheAccounting(CacheInputContext{
		ExplicitMode:     event.CacheInputMode,
		ExecutorType:     event.ExecutorType,
		Provider:         event.Provider,
		ProviderSnapshot: event.AuthProviderSnapshot,
		ResolvedModel:    event.ResolvedModel,
		RequestedModel:   event.RequestedModel,
		DisplayModel:     event.Model,
	}, event.InputTokens, event.CachedTokens, event.CacheTokens, event.CacheReadTokens, event.CacheCreationTokens)

	if recalculated.Mode != CacheInputModeReadIncludedCreationSeparate {
		t.Errorf("recalculated Mode = %q, want %q", recalculated.Mode, CacheInputModeReadIncludedCreationSeparate)
	}
	if recalculated.UncachedInputTokens != event.NormalizedUncachedInputTokens {
		t.Errorf("recalculated UncachedInputTokens = %d, want %d", recalculated.UncachedInputTokens, event.NormalizedUncachedInputTokens)
	}
	if recalculated.TotalInputTokens != event.NormalizedTotalInputTokens {
		t.Errorf("recalculated TotalInputTokens = %d, want %d", recalculated.TotalInputTokens, event.NormalizedTotalInputTokens)
	}
	if recalculated.CacheReadTokens != event.NormalizedCacheReadTokens {
		t.Errorf("recalculated CacheReadTokens = %d, want %d", recalculated.CacheReadTokens, event.NormalizedCacheReadTokens)
	}
	if recalculated.CacheCreationTokens != event.NormalizedCacheCreationTokens {
		t.Errorf("recalculated CacheCreationTokens = %d, want %d", recalculated.CacheCreationTokens, event.NormalizedCacheCreationTokens)

	}
}
