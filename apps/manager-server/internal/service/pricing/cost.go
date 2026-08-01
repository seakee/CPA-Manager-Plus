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
	PricingModel            string
	ContextThresholdTokens  int64
	InputTokens             int64
	OutputTokens            int64
	CachedTokens            int64
	CacheReadTokens         int64
	CacheCreationTokens     int64
	LongInputTokens         int64
	LongOutputTokens        int64
	LongCachedTokens        int64
	LongCacheReadTokens     int64
	LongCacheCreationTokens int64
}

// CostForModel computes the dollar cost for a single (model, tokens) pair.
// InputTokens is the normalized total input, including cache buckets when the
// upstream protocol reports them separately. Fine-grained cache read/create
// dimensions are removed from prompt input and priced separately.
// Any residual CachedTokens are still charged at the legacy cache price; callers
// must pass the compatibility cached value, not CPA's Claude mirror copy.
// Older payloads keep the OpenAI-style cached-in-input behavior.
func CostForModel(modelName string, tokens ModelTokens, prices map[string]model.ModelPrice) float64 {
	price, ok := resolveModelPrice(modelName, prices)
	if !ok {
		return 0
	}
	return costForPrice(modelName, tokens, price)
}

func costForPrice(modelName string, tokens ModelTokens, price model.ModelPrice) float64 {
	return costForPriceWithLegacyLongContext(modelName, tokens, price, true)
}

func costForPriceWithLegacyLongContext(modelName string, tokens ModelTokens, price model.ModelPrice, allowLegacyLongContext bool) float64 {
	if isGPT56Model(modelName) {
		price = enrichGPT56BasePrice(modelName, price)
	}
	if effectivePrice, ok := activeContextPrice(tokens, price); ok {
		return costForSegment(
			maxInt64(tokens.InputTokens, 0),
			maxInt64(tokens.OutputTokens, 0),
			maxInt64(tokens.CachedTokens, 0),
			maxInt64(tokens.CacheReadTokens, 0),
			maxInt64(tokens.CacheCreationTokens, 0),
			effectivePrice,
			1,
			1,
		)
	}
	if allowLegacyLongContext && supportsLongContextPremium(modelName) {
		return costForLongContextModel(tokens, price)
	}
	return costForSegment(
		maxInt64(tokens.InputTokens, 0),
		maxInt64(tokens.OutputTokens, 0),
		maxInt64(tokens.CachedTokens, 0),
		maxInt64(tokens.CacheReadTokens, 0),
		maxInt64(tokens.CacheCreationTokens, 0),
		price,
		1,
		1,
	)
}

func costForLongContextModel(tokens ModelTokens, price model.ModelPrice) float64 {
	inputTokens := maxInt64(tokens.InputTokens, 0)
	outputTokens := maxInt64(tokens.OutputTokens, 0)
	cachedTokens := maxInt64(tokens.CachedTokens, 0)
	cacheReadTokens := maxInt64(tokens.CacheReadTokens, 0)
	cacheCreationTokens := maxInt64(tokens.CacheCreationTokens, 0)

	longInputTokens := clampTokens(tokens.LongInputTokens, inputTokens)
	longOutputTokens := clampTokens(tokens.LongOutputTokens, outputTokens)
	longCachedTokens := clampTokens(tokens.LongCachedTokens, cachedTokens)
	longCacheReadTokens := clampTokens(tokens.LongCacheReadTokens, cacheReadTokens)
	longCacheCreationTokens := clampTokens(tokens.LongCacheCreationTokens, cacheCreationTokens)

	shortCost := costForSegment(
		inputTokens-longInputTokens,
		outputTokens-longOutputTokens,
		cachedTokens-longCachedTokens,
		cacheReadTokens-longCacheReadTokens,
		cacheCreationTokens-longCacheCreationTokens,
		price,
		1,
		1,
	)
	longCost := costForSegment(
		longInputTokens,
		longOutputTokens,
		longCachedTokens,
		longCacheReadTokens,
		longCacheCreationTokens,
		price,
		2,
		1.5,
	)
	return shortCost + longCost
}

func costForSegment(
	inputTokens int64,
	outputTokens int64,
	cachedTokens int64,
	cacheReadTokens int64,
	cacheCreationTokens int64,
	price model.ModelPrice,
	inputMultiplier float64,
	outputMultiplier float64,
) float64 {
	readTokens := cachedTokens + cacheReadTokens
	promptTokens := maxInt64(inputTokens-readTokens-cacheCreationTokens, 0)
	cacheReadPrice := price.CacheRead
	if !configuredPriceValue(cacheReadPrice, price.CacheReadConfigured) {
		cacheReadPrice = fallbackPrice(price.Cache, price.Prompt*0.1)
	}
	cacheCreationPrice := price.CacheCreation
	if !configuredPriceValue(cacheCreationPrice, price.CacheCreationConfigured) {
		cacheCreationPrice = price.Prompt
	}

	return float64(promptTokens)*price.Prompt*inputMultiplier/PerMillion +
		float64(cachedTokens)*price.Cache*inputMultiplier/PerMillion +
		float64(cacheReadTokens)*cacheReadPrice*inputMultiplier/PerMillion +
		float64(cacheCreationTokens)*cacheCreationPrice*inputMultiplier/PerMillion +
		float64(outputTokens)*price.Completion*outputMultiplier/PerMillion
}

// ServiceTierMultiplier returns the OpenAI Priority processing multiplier for
// the actual usage service tier. This compatibility layer keeps today's tier
// multiplier rules centralized; a future price model should store explicit
// per-tier prices such as standard, priority, flex, and batch.
func ServiceTierMultiplier(modelName string, serviceTier string) float64 {
	tier := strings.ToLower(strings.TrimSpace(serviceTier))
	if tier == "flex" || tier == "batch" {
		return 0.5
	}
	if tier != "priority" && tier != "fast" {
		return 1
	}

	modelName = strings.ToLower(strings.TrimSpace(modelName))
	switch {
	case isModelFamily(modelName, "gpt-5.6"):
		return 2
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

// CostForModelWithServiceTier selects context or explicit service-tier prices
// before falling back to the compatibility multiplier for older price books.
func CostForModelWithServiceTier(modelName string, serviceTier string, tokens ModelTokens, prices map[string]model.ModelPrice) float64 {
	price, ok := resolveModelPrice(modelName, prices)
	if !ok {
		return 0
	}
	return costForPriceWithServiceTier(modelName, serviceTier, tokens, price)
}

// CostForModelCandidatesWithServiceTier uses the first candidate to determine
// model-specific billing behavior and the first priced candidate for rates.
// Callers should pass resolved/upstream model first, followed by the requested
// display model or alias as a price fallback.
func CostForModelCandidatesWithServiceTier(modelNames []string, serviceTier string, tokens ModelTokens, prices map[string]model.ModelPrice) float64 {
	seen := map[string]bool{}
	candidates := make([]string, 0, len(modelNames))
	for _, modelName := range modelNames {
		modelName = strings.TrimSpace(modelName)
		if modelName == "" || seen[modelName] {
			continue
		}
		seen[modelName] = true
		candidates = append(candidates, modelName)
	}
	behaviorModel := ""
	if len(candidates) > 0 {
		behaviorModel = candidates[0]
	}
	if pricingModel := strings.TrimSpace(tokens.PricingModel); pricingModel != "" {
		if price, ok := prices[pricingModel]; ok {
			return costForPriceWithServiceTier(behaviorModel, serviceTier, tokens, price)
		}
	}
	for _, modelName := range candidates {
		price, ok := prices[modelName]
		if !ok {
			continue
		}
		return costForPriceWithServiceTier(behaviorModel, serviceTier, tokens, price)
	}
	for _, modelName := range candidates {
		price, ok := officialGPT56Price(modelName)
		if !ok {
			continue
		}
		return costForPriceWithServiceTier(behaviorModel, serviceTier, tokens, price)
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
	modelName = normalizedModelSlug(modelName)
	return modelName == family || strings.HasPrefix(modelName, family+"-")
}

func isGPT56Model(modelName string) bool {
	return isModelFamily(modelName, "gpt-5.6")
}

func supportsLongContextPremium(modelName string) bool {
	slug := normalizedModelSlug(modelName)
	if isGPT56Model(slug) {
		return true
	}
	if slug == "gpt-5.5" || strings.HasPrefix(slug, "gpt-5.5-20") {
		return true
	}
	return slug == "gpt-5.4" || strings.HasPrefix(slug, "gpt-5.4-20") ||
		slug == "gpt-5.4-pro" || strings.HasPrefix(slug, "gpt-5.4-pro-20")
}

func costForPriceWithServiceTier(modelName, serviceTier string, tokens ModelTokens, price model.ModelPrice) float64 {
	if activeContextTier(tokens, price) {
		return costForPrice(modelName, tokens, price)
	}
	legacyLongContext := len(price.ContextTiers) == 0 && supportsLongContextPremium(modelName) && tokens.LongInputTokens > 0
	if legacyLongContext {
		tier := strings.ToLower(strings.TrimSpace(serviceTier))
		if tier != "priority" && tier != "fast" {
			if effectivePrice, ok := model.ModelPriceForServiceTier(price, serviceTier); ok {
				return costForPriceWithLegacyLongContext(modelName, tokens, effectivePrice, true)
			}
			return costForPrice(modelName, tokens, price) * ServiceTierMultiplier(modelName, serviceTier)
		}
		shortTokens, longTokens := splitLegacyLongContextTokens(tokens)
		longCost := costForPriceWithLegacyLongContext(modelName, longTokens, price, true)
		if effectivePrice, ok := model.ModelPriceForServiceTier(price, serviceTier); ok {
			return costForPriceWithLegacyLongContext(modelName, shortTokens, effectivePrice, false) + longCost
		}
		return costForPriceWithLegacyLongContext(modelName, shortTokens, price, false)*ServiceTierMultiplier(modelName, serviceTier) + longCost
	}
	if effectivePrice, ok := model.ModelPriceForServiceTier(price, serviceTier); ok {
		return costForPriceWithLegacyLongContext(modelName, tokens, effectivePrice, len(price.ContextTiers) == 0)
	}
	return costForPrice(modelName, tokens, price) * ServiceTierMultiplier(modelName, serviceTier)
}

func splitLegacyLongContextTokens(tokens ModelTokens) (ModelTokens, ModelTokens) {
	longTokens := ModelTokens{
		PricingModel:        tokens.PricingModel,
		InputTokens:         clampTokens(tokens.LongInputTokens, maxInt64(tokens.InputTokens, 0)),
		OutputTokens:        clampTokens(tokens.LongOutputTokens, maxInt64(tokens.OutputTokens, 0)),
		CachedTokens:        clampTokens(tokens.LongCachedTokens, maxInt64(tokens.CachedTokens, 0)),
		CacheReadTokens:     clampTokens(tokens.LongCacheReadTokens, maxInt64(tokens.CacheReadTokens, 0)),
		CacheCreationTokens: clampTokens(tokens.LongCacheCreationTokens, maxInt64(tokens.CacheCreationTokens, 0)),
	}
	longTokens.LongInputTokens = longTokens.InputTokens
	longTokens.LongOutputTokens = longTokens.OutputTokens
	longTokens.LongCachedTokens = longTokens.CachedTokens
	longTokens.LongCacheReadTokens = longTokens.CacheReadTokens
	longTokens.LongCacheCreationTokens = longTokens.CacheCreationTokens

	shortTokens := ModelTokens{
		PricingModel:        tokens.PricingModel,
		InputTokens:         maxInt64(tokens.InputTokens, 0) - longTokens.InputTokens,
		OutputTokens:        maxInt64(tokens.OutputTokens, 0) - longTokens.OutputTokens,
		CachedTokens:        maxInt64(tokens.CachedTokens, 0) - longTokens.CachedTokens,
		CacheReadTokens:     maxInt64(tokens.CacheReadTokens, 0) - longTokens.CacheReadTokens,
		CacheCreationTokens: maxInt64(tokens.CacheCreationTokens, 0) - longTokens.CacheCreationTokens,
	}
	return shortTokens, longTokens
}

func activeContextTier(tokens ModelTokens, price model.ModelPrice) bool {
	if tokens.ContextThresholdTokens <= 0 {
		return false
	}
	_, ok := model.ModelPriceForContextThreshold(price, tokens.ContextThresholdTokens)
	return ok
}

func activeContextPrice(tokens ModelTokens, price model.ModelPrice) (model.ModelPrice, bool) {
	if len(price.ContextTiers) == 0 || tokens.ContextThresholdTokens == 0 {
		return model.ModelPrice{}, false
	}
	effective, ok := model.ModelPriceForContextThreshold(price, tokens.ContextThresholdTokens)
	return effective, ok
}

func resolveModelPrice(modelName string, prices map[string]model.ModelPrice) (model.ModelPrice, bool) {
	if price, ok := prices[modelName]; ok {
		return price, true
	}
	return officialGPT56Price(modelName)
}

func enrichGPT56BasePrice(modelName string, price model.ModelPrice) model.ModelPrice {
	fallback, ok := officialGPT56Price(modelName)
	if !ok {
		return price
	}
	if !configuredPriceValue(price.Prompt, price.PromptConfigured) {
		price.Prompt = fallback.Prompt
	}
	if !configuredPriceValue(price.Completion, price.CompletionConfigured) {
		price.Completion = fallback.Completion
	}
	if !configuredPriceValue(price.CacheRead, price.CacheReadConfigured) {
		price.CacheRead = price.Prompt * 0.1
	}
	if !configuredPriceValue(price.CacheCreation, price.CacheCreationConfigured) {
		price.CacheCreation = price.Prompt * 1.25
	}
	return price
}

func officialGPT56Price(modelName string) (model.ModelPrice, bool) {
	slug := normalizedModelSlug(modelName)
	switch {
	case isModelFamily(slug, "gpt-5.6-sol"):
		return model.ModelPrice{
			Prompt: 5, Completion: 30, Cache: 0.5, CacheRead: 0.5, CacheCreation: 6.25,
			PromptConfigured: true, CompletionConfigured: true, CacheReadConfigured: true, CacheCreationConfigured: true,
		}, true
	case isModelFamily(slug, "gpt-5.6-terra"):
		return model.ModelPrice{
			Prompt: 2.5, Completion: 15, Cache: 0.25, CacheRead: 0.25, CacheCreation: 3.125,
			PromptConfigured: true, CompletionConfigured: true, CacheReadConfigured: true, CacheCreationConfigured: true,
		}, true
	case isModelFamily(slug, "gpt-5.6-luna"):
		return model.ModelPrice{
			Prompt: 1, Completion: 6, Cache: 0.1, CacheRead: 0.1, CacheCreation: 1.25,
			PromptConfigured: true, CompletionConfigured: true, CacheReadConfigured: true, CacheCreationConfigured: true,
		}, true
	default:
		return model.ModelPrice{}, false
	}
}

func normalizedModelSlug(modelName string) string {
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	if index := strings.LastIndex(modelName, "/"); index >= 0 {
		modelName = modelName[index+1:]
	}
	return modelName
}

func clampTokens(value int64, total int64) int64 {
	if value <= 0 || total <= 0 {
		return 0
	}
	if value > total {
		return total
	}
	return value
}

func configuredPriceValue(value float64, configured bool) bool {
	return configured || value > 0
}
