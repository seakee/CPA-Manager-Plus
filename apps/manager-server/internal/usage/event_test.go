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
		mode      string
		provider  string
		authSnap  string
		executor  string
		model     string
		input     int64
		cached    int64
		read      int64
		creation  int64
		wantMode  string
		wantInput int64
		wantTotal int64
		wantRead  int64
	}{
		{name: "openai mirror is included", provider: "openai", model: "gpt-5.4", input: 1_000, cached: 400, read: 400, wantMode: CacheInputModeIncluded, wantInput: 600, wantTotal: 1_000, wantRead: 400},
		{name: "gpt 5.6 read and write are included", mode: CacheInputModeIncluded, model: "gpt-5.6-sol", input: 1_000, read: 300, creation: 100, wantMode: CacheInputModeIncluded, wantInput: 600, wantTotal: 1_000, wantRead: 300},
		{name: "claude cache is separate", mode: CacheInputModeSeparate, model: "claude-sonnet-4", input: 100, read: 300, creation: 50, wantMode: CacheInputModeSeparate, wantInput: 100, wantTotal: 450, wantRead: 300},
		// CPA 7.2.72+ xAI reports cache_read via ParseOpenAIUsage; input already includes cache.
		{name: "XAIExecutor + Grok is included", provider: "xai", executor: "XAIExecutor", model: "grok-4.5", input: 130_482, cached: 125_824, read: 125_824, wantMode: CacheInputModeIncluded, wantInput: 4_658, wantTotal: 130_482, wantRead: 125_824},
		{name: "KimiExecutor + Kimi is included", provider: "kimi", executor: "KimiExecutor", model: "kimi-k2", input: 1_000, cached: 400, read: 400, wantMode: CacheInputModeIncluded, wantInput: 600, wantTotal: 1_000, wantRead: 400},
		{name: "OpenAICompatExecutor + Claude model is included", provider: "openai-compat", executor: "OpenAICompatExecutor", model: "claude-sonnet-4", input: 1_000, cached: 400, read: 400, wantMode: CacheInputModeIncluded, wantInput: 600, wantTotal: 1_000, wantRead: 400},
		{name: "ClaudeExecutor + Kimi alias is separate", provider: "anthropic", executor: "ClaudeExecutor", model: "kimi-k2", input: 100, read: 300, creation: 50, wantMode: CacheInputModeSeparate, wantInput: 100, wantTotal: 450, wantRead: 300},
		{name: "ClaudeExecutor + Grok alias is separate", provider: "anthropic", executor: "ClaudeExecutor", model: "grok-4.5", input: 100, read: 200, wantMode: CacheInputModeSeparate, wantInput: 100, wantTotal: 300, wantRead: 200},
		{name: "explicit separate overrides XAIExecutor", mode: CacheInputModeSeparate, provider: "xai", executor: "XAIExecutor", model: "grok-4.5", input: 100, read: 50, wantMode: CacheInputModeSeparate, wantInput: 100, wantTotal: 150, wantRead: 50},
		{name: "explicit included overrides ClaudeExecutor", mode: CacheInputModeIncluded, provider: "anthropic", executor: "ClaudeExecutor", model: "claude-sonnet-4", input: 1_000, read: 400, wantMode: CacheInputModeIncluded, wantInput: 600, wantTotal: 1_000, wantRead: 400},
		{name: "provider snapshot moonshot is included", authSnap: "moonshot", model: "other", input: 500, cached: 100, read: 100, wantMode: CacheInputModeIncluded, wantInput: 400, wantTotal: 500, wantRead: 100},
		{name: "model grok fallback without executor is included", model: "grok-4.5", input: 500, cached: 100, read: 100, wantMode: CacheInputModeIncluded, wantInput: 400, wantTotal: 500, wantRead: 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeCacheAccounting(tt.mode, tt.provider, tt.authSnap, tt.executor, tt.model, tt.input, tt.cached, 0, tt.read, tt.creation)
			if got.Mode != tt.wantMode || got.UncachedInputTokens != tt.wantInput || got.TotalInputTokens != tt.wantTotal || got.CacheReadTokens != tt.wantRead {
				t.Fatalf("accounting = %+v, want mode=%s input=%d total=%d read=%d", got, tt.wantMode, tt.wantInput, tt.wantTotal, tt.wantRead)
			}
		})
	}
}

func TestInferCacheInputModePriority(t *testing.T) {
	if got := InferCacheInputMode("", "OpenAICompatExecutor", "anthropic", "", "claude-sonnet-4", 10, 0); got != CacheInputModeIncluded {
		t.Fatalf("compat+claude model = %s, want included", got)
	}
	if got := InferCacheInputMode("", "ClaudeExecutor", "xai", "", "grok-4.5", 10, 0); got != CacheInputModeSeparate {
		t.Fatalf("claude exec+grok = %s, want separate", got)
	}
	if got := InferCacheInputMode("", "", "xai", "", "claude-fake", 10, 0); got != CacheInputModeIncluded {
		t.Fatalf("xai provider = %s, want included", got)
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
