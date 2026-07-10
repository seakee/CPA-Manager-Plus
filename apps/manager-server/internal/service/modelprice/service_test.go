package modelprice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestUsageSummaryUsesConfiguredRecentLimit(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	if _, err := st.UsageEvents.InsertBatch(context.Background(), []usage.Event{
		{EventHash: "older", TimestampMS: 100, Timestamp: "2026-01-01T00:00:00Z", Model: "gpt-old", CreatedAtMS: 100},
		{EventHash: "newer", TimestampMS: 200, Timestamp: "2026-01-01T00:00:01Z", Model: "gpt-new", ResolvedModel: "gpt-resolved", CreatedAtMS: 200},
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	summary, err := New(st, nil).UsageSummary(context.Background(), 1)
	if err != nil {
		t.Fatalf("usage summary: %v", err)
	}
	if summary.SampledEvents != 1 || summary.TotalEvents != 2 || !summary.Truncated {
		t.Fatalf("summary metadata = %#v", summary)
	}
	if len(summary.Models) != 2 || summary.Models[0].Model != "gpt-new" || summary.Models[1].Model != "gpt-resolved" {
		t.Fatalf("models = %#v", summary.Models)
	}
}

func TestSelectModelPricesIncludesResolvedAndProviderVariants(t *testing.T) {
	remote := map[string]store.ModelPrice{
		"anthropic/claude-sonnet-4-5": {
			Prompt:        3,
			Completion:    15,
			Cache:         0.3,
			Source:        SyncSource,
			SourceModelID: "anthropic/claude-sonnet-4-5",
		},
		"openai/GPT-4.1": {
			Prompt:        2,
			Completion:    8,
			Source:        SyncSource,
			SourceModelID: "openai/GPT-4.1",
		},
	}

	selection := selectModelPrices(remote, []string{"claude-sonnet-4-5", "gpt-4.1"})

	if len(selection.Prices) != 2 {
		t.Fatalf("selected prices = %#v", selection.Prices)
	}
	if selection.Prices["claude-sonnet-4-5"].SourceModelID != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("claude source = %#v", selection.Prices["claude-sonnet-4-5"])
	}
	if selection.Prices["gpt-4.1"].SourceModelID != "openai/GPT-4.1" {
		t.Fatalf("gpt source = %#v", selection.Prices["gpt-4.1"])
	}
	if len(selection.Candidates) != 0 || len(selection.Unmatched) != 0 {
		t.Fatalf("unexpected candidates/unmatched = %#v %#v", selection.Candidates, selection.Unmatched)
	}
}

func TestFetchOpenRouterModelPrices(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{
					"id": "openai/gpt-test",
					"pricing": {
						"prompt": "0.000001",
						"completion": "0.000002",
						"input_cache_read": "0.00000025"
					}
				},
				{"id": "skip-no-pricing"}
			]
		}`))
	}))
	t.Cleanup(source.Close)

	prices, skipped, err := fetchOpenRouterModelPrices(context.Background(), source.URL, source.Client())
	if err != nil {
		t.Fatalf("fetch openrouter prices: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d", skipped)
	}
	price := prices["openai/gpt-test"]
	if price.Source != SyncSourceOpenRouter || price.SourceModelID != "openai/gpt-test" {
		t.Fatalf("source metadata = %#v", price)
	}
	if !closePrice(price.Prompt, 1) || !closePrice(price.Completion, 2) || !closePrice(price.Cache, 0.25) ||
		!price.PromptConfigured || !price.CompletionConfigured || !price.CacheReadConfigured || price.CacheCreationConfigured {
		t.Fatalf("price = %#v", price)
	}
}

func TestSelectModelPricesReturnsCandidatesForAmbiguousModels(t *testing.T) {
	remote := map[string]store.ModelPrice{
		"anthropic/claude-sonnet-4-20250514": {
			Prompt:        3,
			Completion:    15,
			SourceModelID: "anthropic/claude-sonnet-4-20250514",
		},
		"anthropic/claude-sonnet-4-20250929": {
			Prompt:        3,
			Completion:    15,
			SourceModelID: "anthropic/claude-sonnet-4-20250929",
		},
		"openai/gpt-4.1": {
			Prompt:        2,
			Completion:    8,
			SourceModelID: "openai/gpt-4.1",
		},
	}

	selection := selectModelPrices(remote, []string{"claude-sonnet-4-latest", "unknown-model"})

	if len(selection.Prices) != 0 {
		t.Fatalf("auto matched prices = %#v", selection.Prices)
	}
	if len(selection.Candidates) != 1 {
		t.Fatalf("candidates = %#v", selection.Candidates)
	}
	if selection.Candidates[0].Model != "claude-sonnet-4-latest" || len(selection.Candidates[0].Candidates) == 0 {
		t.Fatalf("candidate set = %#v", selection.Candidates[0])
	}
	if selection.Candidates[0].Candidates[0].Score < minCandidateScore {
		t.Fatalf("candidate score = %#v", selection.Candidates[0].Candidates[0])
	}
	if len(selection.Unmatched) != 1 || selection.Unmatched[0] != "unknown-model" {
		t.Fatalf("unmatched = %#v", selection.Unmatched)
	}
}

func TestSelectModelPricesReturnsWeakFamilyCandidates(t *testing.T) {
	remote := map[string]store.ModelPrice{
		"google/gemini-2.5-flash-lite": {
			Prompt:        0.3,
			Completion:    2.5,
			Source:        SyncSourceOpenRouter,
			SourceModelID: "google/gemini-2.5-flash-lite",
		},
		"qwen/qwen3.5-flash": {
			Prompt:        0.2,
			Completion:    0.8,
			Source:        SyncSourceOpenRouter,
			SourceModelID: "qwen/qwen3.5-flash",
		},
		"minimax/m2.5": {
			Prompt:        0.4,
			Completion:    1.6,
			Source:        SyncSourceOpenRouter,
			SourceModelID: "minimax/m2.5",
		},
		"openai/codex-mini": {
			Prompt:        1.5,
			Completion:    6,
			Source:        SyncSourceOpenRouter,
			SourceModelID: "openai/codex-mini",
		},
	}

	selection := selectModelPrices(remote, []string{
		"gemini-3.5-flash-low",
		"qwen3.6-plus-preview",
		"mimo-v2.5",
		"codex-auto-review",
	})

	if !hasCandidate(selection, "gemini-3.5-flash-low", "google/gemini-2.5-flash-lite") {
		t.Fatalf("gemini candidates = %#v", selection.Candidates)
	}
	if !hasCandidate(selection, "qwen3.6-plus-preview", "qwen/qwen3.5-flash") {
		t.Fatalf("qwen candidates = %#v", selection.Candidates)
	}
	if !hasCandidate(selection, "mimo-v2.5", "minimax/m2.5") {
		t.Fatalf("mimo candidates = %#v", selection.Candidates)
	}
	if len(selection.Unmatched) != 1 || selection.Unmatched[0] != "codex-auto-review" {
		t.Fatalf("unmatched = %#v", selection.Unmatched)
	}
}

func hasCandidate(selection priceSelectionResult, model string, sourceModelID string) bool {
	for _, set := range selection.Candidates {
		if set.Model != model {
			continue
		}
		for _, candidate := range set.Candidates {
			if candidate.SourceModelID == sourceModelID {
				return true
			}
		}
	}
	return false
}

func closePrice(left float64, right float64) bool {
	if left > right {
		return left-right < 0.0000001
	}
	return right-left < 0.0000001
}
