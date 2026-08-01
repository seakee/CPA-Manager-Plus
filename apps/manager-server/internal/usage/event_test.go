package usage

import (
	"math"
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
		{name: "invalid explicit is ignored", context: CacheInputContext{ExplicitMode: "legacy", ExecutorType: "XAIExecutor"}, want: CacheInputModeIncluded},
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
	if event.CacheInputMode != CacheInputModeSeparate || event.NormalizedTotalInputTokens != 120 {
		t.Fatalf("accounting = mode:%q total:%d", event.CacheInputMode, event.NormalizedTotalInputTokens)
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
