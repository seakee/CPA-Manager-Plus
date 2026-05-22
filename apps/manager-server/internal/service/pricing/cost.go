// Package pricing converts token aggregates into monetary cost given a model price book.
package pricing

import "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"

// PerMillion divides by one million to convert token-priced units (per 1M tokens).
const PerMillion = 1_000_000.0

// ModelTokens represents the token totals consumed by a single model.
type ModelTokens struct {
	InputTokens  int64
	OutputTokens int64
	CachedTokens int64
}

// CostForModel computes the dollar cost for a single (model, tokens) pair.
// Cached tokens are included in input tokens by upstream usage payloads, so
// only non-cached input tokens use the prompt price.
func CostForModel(modelName string, tokens ModelTokens, prices map[string]model.ModelPrice) float64 {
	price, ok := prices[modelName]
	if !ok {
		return 0
	}
	inputTokens := maxInt64(tokens.InputTokens, 0)
	outputTokens := maxInt64(tokens.OutputTokens, 0)
	cachedTokens := maxInt64(tokens.CachedTokens, 0)
	promptTokens := maxInt64(inputTokens-cachedTokens, 0)

	return float64(promptTokens)*price.Prompt/PerMillion +
		float64(outputTokens)*price.Completion/PerMillion +
		float64(cachedTokens)*price.Cache/PerMillion
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
