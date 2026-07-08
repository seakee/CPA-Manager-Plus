// Package pricing converts token aggregates into monetary cost given a model price book.
package pricing

import (
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

// PerMillion divides by one million to convert token-priced units (per 1M tokens).
const PerMillion = 1_000_000.0

// ModelTokens represents the token totals consumed by a single model.
// CachedTokens is the remaining legacy/OpenAI-style cached input after any
// fine-grained cache_read/cache_creation values have already been removed.
type ModelTokens struct {
	InputTokens         int64
	OutputTokens        int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
}

// CostForModel computes the dollar cost for a single (model, tokens) pair.
// When CPA provides fine-grained cache read/create tokens, input tokens are
// treated as non-cache input and those cache dimensions are priced separately.
// Any residual CachedTokens are still charged at the legacy cache price; callers
// must pass the compatibility cached value, not CPA's Claude mirror copy.
// Older payloads keep the OpenAI-style cached-in-input behavior.
func CostForModel(modelName string, tokens ModelTokens, prices map[string]model.ModelPrice) float64 {
	price, ok := prices[modelName]
	if !ok {
		return 0
	}
	inputTokens := maxInt64(tokens.InputTokens, 0)
	outputTokens := maxInt64(tokens.OutputTokens, 0)
	cachedTokens := maxInt64(tokens.CachedTokens, 0)
	cacheReadTokens := maxInt64(tokens.CacheReadTokens, 0)
	cacheCreationTokens := maxInt64(tokens.CacheCreationTokens, 0)
	if cacheReadTokens > 0 || cacheCreationTokens > 0 {
		promptTokens := maxInt64(inputTokens-cachedTokens, 0)
		cacheReadPrice := fallbackPrice(price.CacheRead, price.Cache)
		cacheCreationPrice := fallbackPrice(price.CacheCreation, price.Prompt)
		return float64(promptTokens)*price.Prompt/PerMillion +
			float64(outputTokens)*price.Completion/PerMillion +
			float64(cachedTokens)*price.Cache/PerMillion +
			float64(cacheReadTokens)*cacheReadPrice/PerMillion +
			float64(cacheCreationTokens)*cacheCreationPrice/PerMillion
	}

	promptTokens := maxInt64(inputTokens-cachedTokens, 0)

	return float64(promptTokens)*price.Prompt/PerMillion +
		float64(outputTokens)*price.Completion/PerMillion +
		float64(cachedTokens)*price.Cache/PerMillion
}

// ServiceTierMultiplier returns the OpenAI Priority processing multiplier for
// the actual usage service tier. This compatibility layer keeps today's tier
// multiplier rules centralized; a future price model should store explicit
// per-tier prices such as standard, priority, flex, and batch.
func ServiceTierMultiplier(modelName string, serviceTier string) float64 {
	tier := strings.ToLower(strings.TrimSpace(serviceTier))
	if tier != "priority" && tier != "fast" {
		return 1
	}

	modelName = strings.ToLower(strings.TrimSpace(modelName))
	switch {
	case isModelFamily(modelName, "gpt-5.5"):
		return 2.5
	case isModelFamily(modelName, "gpt-5.4-mini"):
		return 2
	case isModelFamily(modelName, "gpt-5.4"):
		return 2
	case isModelFamily(modelName, "gpt-5.3-codex"):
		return 2
	default:
		return 1
	}
}

// CostForModelWithServiceTier computes standard token cost first, then applies
// the multiplier for the actual service_tier recorded by usage.
func CostForModelWithServiceTier(modelName string, serviceTier string, tokens ModelTokens, prices map[string]model.ModelPrice) float64 {
	return CostForModel(modelName, tokens, prices) * ServiceTierMultiplier(modelName, serviceTier)
}

// CostForModelCandidatesWithServiceTier computes cost using the first priced
// model name from the candidate list. Callers should pass resolved/upstream
// model first, followed by the requested display model or alias as fallback.
func CostForModelCandidatesWithServiceTier(modelNames []string, serviceTier string, tokens ModelTokens, prices map[string]model.ModelPrice) float64 {
	seen := map[string]bool{}
	for _, modelName := range modelNames {
		modelName = strings.TrimSpace(modelName)
		if modelName == "" || seen[modelName] {
			continue
		}
		seen[modelName] = true
		if _, ok := prices[modelName]; !ok {
			continue
		}
		return CostForModelWithServiceTier(modelName, serviceTier, tokens, prices)
	}
	return 0
}

// SumCost folds CostForModel over a slice of (model, tokens) tuples.
type Item struct {
	Model  string
	Tokens ModelTokens
}

// SumCost adds up the cost across multiple items.
func SumCost(items []Item, prices map[string]model.ModelPrice) float64 {
	total := 0.0
	for _, item := range items {
		total += CostForModel(item.Model, item.Tokens, prices)
	}
	return total
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func fallbackPrice(value float64, fallback float64) float64 {
	if value > 0 {
		return value
	}
	return fallback
}

func isModelFamily(modelName string, family string) bool {
	return modelName == family || strings.HasPrefix(modelName, family+"-")
}
