package usage

import "testing"

// Cross-check against CPA v7.2.77 executor type names (reflect.Type.Name)
// and usage parser families in internal/runtime/executor/helps/usage_helpers.go.
func TestCPAExecutorTypeNamesMapToExpectedCacheMode(t *testing.T) {
	cases := []struct {
		executor string
		provider string
		model    string
		parser   string
		wantMode string
	}{
		{"ClaudeExecutor", "claude", "claude-sonnet-4", "ParseClaudeUsage", CacheInputModeSeparate},
		{"OpenAICompatExecutor", "openai", "gpt-5.4", "ParseOpenAIUsage", CacheInputModeIncluded},
		{"OpenAICompatExecutor", "custom", "claude-sonnet-4", "ParseOpenAIUsage", CacheInputModeIncluded},
		{"CodexExecutor", "codex", "gpt-5.4", "ParseCodexUsage", CacheInputModeIncluded},
		{"CodexWebsocketsExecutor", "codex", "gpt-5.4", "ParseCodexUsage", CacheInputModeIncluded},
		{"CodexAutoExecutor", "codex", "gpt-5.4", "ParseCodexUsage", CacheInputModeIncluded},
		{"GeminiExecutor", "gemini", "gemini-2.5-pro", "ParseGeminiUsage", CacheInputModeIncluded},
		{"GeminiVertexExecutor", "vertex", "gemini-2.5-pro", "ParseGeminiUsage", CacheInputModeIncluded},
		{"AIStudioExecutor", "aistudio", "gemini-2.5-flash", "ParseGeminiUsage", CacheInputModeIncluded},
		{"AntigravityExecutor", "antigravity", "gemini-2.5-pro", "ParseAntigravityUsage", CacheInputModeIncluded},
		{"XAIExecutor", "xai", "grok-4.5", "ParseOpenAI/CodexUsage", CacheInputModeIncluded},
		{"XAIWebsocketsExecutor", "xai", "grok-4.5", "ParseCodexUsage", CacheInputModeIncluded},
		{"XAIAutoExecutor", "xai", "grok-4.5", "ParseCodexUsage", CacheInputModeIncluded},
		{"KimiExecutor", "kimi", "kimi-k2", "ParseOpenAIUsage", CacheInputModeIncluded},
		{"ClaudeExecutor", "anthropic", "grok-4.5", "ParseClaudeUsage", CacheInputModeSeparate},
		{"ClaudeExecutor", "anthropic", "kimi-k2", "ParseClaudeUsage", CacheInputModeSeparate},
	}
	for _, tt := range cases {
		t.Run(tt.executor+"/"+tt.model, func(t *testing.T) {
			got := InferCacheInputMode("", tt.executor, tt.provider, "", tt.model, 1000, 0)
			if got != tt.wantMode {
				t.Fatalf("mode=%s want=%s parser=%s", got, tt.wantMode, tt.parser)
			}
		})
	}
}

func TestCPAOpenAIStylePayloadDoesNotDoubleCount(t *testing.T) {
	acc := NormalizeCacheAccounting("", "xai", "", "XAIExecutor", "grok-4.5",
		130482, 125824, 0, 125824, 0)
	if acc.Mode != CacheInputModeIncluded || acc.TotalInputTokens != 130482 {
		t.Fatalf("acc=%+v", acc)
	}
	if acc.TotalInputTokens > 130552 {
		t.Fatalf("normalized input exceeds upstream total")
	}
}

func TestCPAClaudeStylePayloadAddsCacheOutsideInput(t *testing.T) {
	acc := NormalizeCacheAccounting("", "claude", "", "ClaudeExecutor", "claude-sonnet-4",
		3085, 7, 0, 7, 19514)
	if acc.Mode != CacheInputModeSeparate || acc.TotalInputTokens != 3085+7+19514 {
		t.Fatalf("acc=%+v", acc)
	}
}

func TestCPAProviderIdentifiersWithoutExecutor(t *testing.T) {
	for _, p := range []string{"xai", "kimi", "codex", "gemini", "aistudio", "vertex", "antigravity", "openai"} {
		if got := InferCacheInputMode("", "", p, "", "unknown-model", 10, 0); got != CacheInputModeIncluded {
			t.Fatalf("provider %s → %s", p, got)
		}
	}
	if got := InferCacheInputMode("", "", "claude", "", "unknown-model", 10, 0); got != CacheInputModeSeparate {
		t.Fatalf("provider claude → %s", got)
	}
}
