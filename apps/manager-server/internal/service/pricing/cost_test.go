package pricing

import (
	"math"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

func TestCostForModelSeparatesCachedInputTokens(t *testing.T) {
	prices := map[string]model.ModelPrice{
		"gpt-cached": {Prompt: 2, Completion: 4, Cache: 1},
	}

	cost := CostForModel("gpt-cached", ModelTokens{
		InputTokens:  1_000_000,
		OutputTokens: 500_000,
		CachedTokens: 250_000,
	}, prices)

	if math.Abs(cost-3.75) > 0.000001 {
		t.Fatalf("cost = %v, want 3.75", cost)
	}
}

func TestCostForModelDoesNotCreateNegativePromptCost(t *testing.T) {
	prices := map[string]model.ModelPrice{
		"gpt-cached": {Prompt: 10, Cache: 1},
	}

	cost := CostForModel("gpt-cached", ModelTokens{
		InputTokens:  100_000,
		CachedTokens: 250_000,
	}, prices)

	if math.Abs(cost-0.25) > 0.000001 {
		t.Fatalf("cost = %v, want 0.25", cost)
	}
}

func TestCostForModelPricesFineGrainedCacheOutsideInput(t *testing.T) {
	prices := map[string]model.ModelPrice{
		"claude-cached": {Prompt: 2, Completion: 4, Cache: 1, CacheRead: 1, CacheCreation: 3},
	}

	cost := CostForModel("claude-cached", ModelTokens{
		InputTokens:         500_000,
		OutputTokens:        250_000,
		CachedTokens:        0,
		CacheReadTokens:     2_000_000,
		CacheCreationTokens: 100_000,
	}, prices)

	if math.Abs(cost-4.3) > 0.000001 {
		t.Fatalf("cost = %v, want 4.3", cost)
	}
}

func TestCostForModelPricesResidualCompatCachedWithFineGrainedCache(t *testing.T) {
	prices := map[string]model.ModelPrice{
		"mixed-cache": {Prompt: 2, Completion: 4, Cache: 1, CacheRead: 0.5, CacheCreation: 3},
	}

	cost := CostForModel("mixed-cache", ModelTokens{
		InputTokens:         1_000_000,
		CachedTokens:        100_000,
		CacheReadTokens:     200_000,
		CacheCreationTokens: 100_000,
	}, prices)

	if math.Abs(cost-2.3) > 0.000001 {
		t.Fatalf("cost = %v, want 2.3", cost)
	}
}

func TestServiceTierMultiplier(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		serviceTier string
		want        float64
	}{
		{name: "gpt-5.4 default", model: "gpt-5.4", serviceTier: "default", want: 1},
		{name: "gpt-5.4 priority", model: "gpt-5.4", serviceTier: "priority", want: 2},
		{name: "gpt-5.4 fast", model: "gpt-5.4", serviceTier: "fast", want: 2},
		{name: "gpt-5.4 mini priority", model: "gpt-5.4-mini", serviceTier: "priority", want: 2},
		{name: "gpt-5.5 priority", model: "gpt-5.5", serviceTier: "priority", want: 2.5},
		{name: "gpt-5.3 codex priority", model: "gpt-5.3-codex", serviceTier: "priority", want: 2},
		{name: "unknown tier", model: "gpt-5.4", serviceTier: "accelerated", want: 1},
		{name: "unknown priority model", model: "gpt-unknown", serviceTier: "priority", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ServiceTierMultiplier(tt.model, tt.serviceTier)
			if got != tt.want {
				t.Fatalf("ServiceTierMultiplier(%q, %q) = %v, want %v", tt.model, tt.serviceTier, got, tt.want)
			}
		})
	}
}

func TestCostForModelWithServiceTier(t *testing.T) {
	prices := map[string]model.ModelPrice{
		"gpt-5.4": {Prompt: 2.5, Completion: 5, Cache: 1},
	}

	tokens := ModelTokens{InputTokens: 1_000_000}
	if cost := CostForModelWithServiceTier("gpt-5.4", "default", tokens, prices); math.Abs(cost-2.5) > 0.000001 {
		t.Fatalf("default cost = %v, want 2.5", cost)
	}
	if cost := CostForModelWithServiceTier("gpt-5.4", "priority", tokens, prices); math.Abs(cost-5) > 0.000001 {
		t.Fatalf("priority cost = %v, want 5", cost)
	}
	if cost := CostForModelWithServiceTier("missing-model", "priority", tokens, prices); cost != 0 {
		t.Fatalf("missing model cost = %v, want 0", cost)
	}
}

func TestCostForModelCandidatesWithServiceTierFallsBackToRequestedModel(t *testing.T) {
	prices := map[string]model.ModelPrice{
		"gpt-5.4": {Prompt: 2.5, Completion: 5, Cache: 1},
	}

	cost := CostForModelCandidatesWithServiceTier(
		[]string{"missing-upstream", "gpt-5.4"},
		"priority",
		ModelTokens{InputTokens: 1_000_000},
		prices,
	)

	if math.Abs(cost-5) > 0.000001 {
		t.Fatalf("fallback cost = %v, want 5", cost)
	}
}

func TestCostForModelCandidatesWithServiceTierPrefersResolvedModel(t *testing.T) {
	prices := map[string]model.ModelPrice{
		"gpt-resolved": {Prompt: 1, Completion: 2, Cache: 0.5},
		"gpt-5.4":      {Prompt: 2.5, Completion: 5, Cache: 1},
	}

	cost := CostForModelCandidatesWithServiceTier(
		[]string{"gpt-resolved", "gpt-5.4"},
		"priority",
		ModelTokens{InputTokens: 1_000_000},
		prices,
	)

	if math.Abs(cost-1) > 0.000001 {
		t.Fatalf("resolved cost = %v, want 1", cost)
	}
}

func TestCostForModelWithServiceTierPreservesCacheBuckets(t *testing.T) {
	prices := map[string]model.ModelPrice{
		"gpt-5.4": {Prompt: 2, Completion: 4, Cache: 1, CacheRead: 0.5, CacheCreation: 3},
	}

	cost := CostForModelWithServiceTier("gpt-5.4", "priority", ModelTokens{
		InputTokens:         1_000_000,
		CachedTokens:        100_000,
		CacheReadTokens:     200_000,
		CacheCreationTokens: 100_000,
	}, prices)

	if math.Abs(cost-4.6) > 0.000001 {
		t.Fatalf("priority cache cost = %v, want 4.6", cost)
	}
}
