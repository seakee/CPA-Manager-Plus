package model

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	// ModelPriceBaseContextThreshold is the internal rollup band for requests
	// that do not cross a configured context-price threshold.
	ModelPriceBaseContextThreshold     int64 = -1
	modelPriceStructureRevisionVersion       = "context-v1"
)

// ModelPriceContextTier is a context-size price override. A request selects
// the highest threshold for which normalized input tokens are strictly greater
// than ThresholdTokens. Configured flags distinguish a missing override from
// an explicit zero price.
type ModelPriceContextTier struct {
	ThresholdTokens         int64   `json:"thresholdTokens"`
	Prompt                  float64 `json:"prompt"`
	Completion              float64 `json:"completion"`
	Cache                   float64 `json:"cache"`
	CacheRead               float64 `json:"cacheRead,omitempty"`
	CacheCreation           float64 `json:"cacheCreation,omitempty"`
	PromptConfigured        bool    `json:"promptConfigured,omitempty"`
	CompletionConfigured    bool    `json:"completionConfigured,omitempty"`
	CacheConfigured         bool    `json:"cacheConfigured,omitempty"`
	CacheReadConfigured     bool    `json:"cacheReadConfigured,omitempty"`
	CacheCreationConfigured bool    `json:"cacheCreationConfigured,omitempty"`
}

// ModelPriceServiceTier is a complete or partial price override for a
// provider mode and its emitted service tier. Mode matches source-side names
// such as "fast", while ServiceTier matches usage values such as "priority".
// Configured flags distinguish missing overrides from explicit zero prices.
type ModelPriceServiceTier struct {
	Mode                    string  `json:"mode"`
	ServiceTier             string  `json:"serviceTier"`
	Prompt                  float64 `json:"prompt"`
	Completion              float64 `json:"completion"`
	Cache                   float64 `json:"cache"`
	CacheRead               float64 `json:"cacheRead,omitempty"`
	CacheCreation           float64 `json:"cacheCreation,omitempty"`
	PromptConfigured        bool    `json:"promptConfigured,omitempty"`
	CompletionConfigured    bool    `json:"completionConfigured,omitempty"`
	CacheConfigured         bool    `json:"cacheConfigured,omitempty"`
	CacheReadConfigured     bool    `json:"cacheReadConfigured,omitempty"`
	CacheCreationConfigured bool    `json:"cacheCreationConfigured,omitempty"`
}

// NormalizeModelPriceContextTiers validates and returns a threshold-sorted
// copy. The input slice is never mutated.
func NormalizeModelPriceContextTiers(tiers []ModelPriceContextTier) ([]ModelPriceContextTier, error) {
	if len(tiers) == 0 {
		return nil, nil
	}
	normalized := append([]ModelPriceContextTier(nil), tiers...)
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].ThresholdTokens < normalized[j].ThresholdTokens
	})
	for index, tier := range normalized {
		if tier.ThresholdTokens <= 0 {
			return nil, fmt.Errorf("context tier threshold must be positive: %d", tier.ThresholdTokens)
		}
		if index > 0 && normalized[index-1].ThresholdTokens == tier.ThresholdTokens {
			return nil, fmt.Errorf("duplicate context tier threshold: %d", tier.ThresholdTokens)
		}
		if !validModelPriceRuleValue(tier.Prompt) ||
			!validModelPriceRuleValue(tier.Completion) ||
			!validModelPriceRuleValue(tier.Cache) ||
			!validModelPriceRuleValue(tier.CacheRead) ||
			!validModelPriceRuleValue(tier.CacheCreation) {
			return nil, fmt.Errorf("invalid context tier price at threshold %d", tier.ThresholdTokens)
		}
		if !tier.PromptConfigured && !tier.CompletionConfigured && !tier.CacheConfigured &&
			!tier.CacheReadConfigured && !tier.CacheCreationConfigured {
			return nil, fmt.Errorf("context tier at threshold %d has no configured prices", tier.ThresholdTokens)
		}
	}
	return normalized, nil
}

// SelectModelPriceContextTier returns the active tier and its threshold band.
// Exact-threshold requests remain in the lower band, matching models.dev's
// strictly-greater-than semantics.
func SelectModelPriceContextTier(price ModelPrice, normalizedInputTokens int64) (ModelPriceContextTier, bool) {
	var selected ModelPriceContextTier
	found := false
	for _, tier := range price.ContextTiers {
		if normalizedInputTokens <= tier.ThresholdTokens {
			break
		}
		selected = tier
		found = true
	}
	return selected, found
}

// ModelPriceForContext applies the selected tier's configured overrides to the
// base price, producing the complete rate set for the whole request.
func ModelPriceForContext(price ModelPrice, normalizedInputTokens int64) (ModelPrice, int64) {
	tier, ok := SelectModelPriceContextTier(price, normalizedInputTokens)
	if !ok {
		return price, ModelPriceBaseContextThreshold
	}
	return applyModelPriceContextTier(price, tier), tier.ThresholdTokens
}

// ModelPriceForContextThreshold resolves a previously classified rollup band.
// A positive threshold must exactly match a currently configured tier.
func ModelPriceForContextThreshold(price ModelPrice, thresholdTokens int64) (ModelPrice, bool) {
	if thresholdTokens == ModelPriceBaseContextThreshold {
		price.ContextTiers = nil
		return price, true
	}
	if thresholdTokens <= 0 {
		return ModelPrice{}, false
	}
	for _, tier := range price.ContextTiers {
		if tier.ThresholdTokens == thresholdTokens {
			return applyModelPriceContextTier(price, tier), true
		}
	}
	return ModelPrice{}, false
}

func applyModelPriceContextTier(price ModelPrice, tier ModelPriceContextTier) ModelPrice {
	effective := price
	effective.ContextTiers = nil
	effective.ServiceTiers = nil
	if tier.PromptConfigured {
		effective.Prompt = tier.Prompt
		effective.PromptConfigured = true
	}
	if tier.CompletionConfigured {
		effective.Completion = tier.Completion
		effective.CompletionConfigured = true
	}
	if tier.CacheConfigured {
		effective.Cache = tier.Cache
	}
	if tier.CacheReadConfigured {
		effective.CacheRead = tier.CacheRead
		effective.CacheReadConfigured = true
	}
	if tier.CacheCreationConfigured {
		effective.CacheCreation = tier.CacheCreation
		effective.CacheCreationConfigured = true
	}
	return effective
}

// NormalizeModelPriceServiceTiers validates and returns a deterministic copy.
// A usage tier may match either Mode or ServiceTier, so identifiers cannot be
// shared by different rules.
func NormalizeModelPriceServiceTiers(tiers []ModelPriceServiceTier) ([]ModelPriceServiceTier, error) {
	if len(tiers) == 0 {
		return nil, nil
	}
	normalized := append([]ModelPriceServiceTier(nil), tiers...)
	claimed := make(map[string]int, len(normalized)*2)
	for index := range normalized {
		tier := &normalized[index]
		tier.Mode = strings.ToLower(strings.TrimSpace(tier.Mode))
		tier.ServiceTier = strings.ToLower(strings.TrimSpace(tier.ServiceTier))
		if tier.Mode == "" || tier.ServiceTier == "" {
			return nil, errors.New("service tier mode and provider tier are required")
		}
		if !validModelPriceRuleValue(tier.Prompt) ||
			!validModelPriceRuleValue(tier.Completion) ||
			!validModelPriceRuleValue(tier.Cache) ||
			!validModelPriceRuleValue(tier.CacheRead) ||
			!validModelPriceRuleValue(tier.CacheCreation) {
			return nil, fmt.Errorf("invalid service tier price for %s/%s", tier.Mode, tier.ServiceTier)
		}
		if !tier.PromptConfigured && !tier.CompletionConfigured && !tier.CacheConfigured &&
			!tier.CacheReadConfigured && !tier.CacheCreationConfigured {
			return nil, fmt.Errorf("service tier %s/%s has no configured prices", tier.Mode, tier.ServiceTier)
		}
		for _, identifier := range []string{tier.Mode, tier.ServiceTier} {
			if owner, exists := claimed[identifier]; exists && owner != index {
				return nil, fmt.Errorf("duplicate service tier identifier: %s", identifier)
			}
			claimed[identifier] = index
		}
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Mode != normalized[j].Mode {
			return normalized[i].Mode < normalized[j].Mode
		}
		return normalized[i].ServiceTier < normalized[j].ServiceTier
	})
	return normalized, nil
}

// SelectModelPriceServiceTier finds an explicit price rule for a recorded
// service tier. A single models.dev fast rule therefore matches both "fast"
// request telemetry and "priority" API telemetry.
func SelectModelPriceServiceTier(price ModelPrice, serviceTier string) (ModelPriceServiceTier, bool) {
	serviceTier = strings.ToLower(strings.TrimSpace(serviceTier))
	if serviceTier == "" {
		return ModelPriceServiceTier{}, false
	}
	for _, tier := range price.ServiceTiers {
		if serviceTier == tier.Mode || serviceTier == tier.ServiceTier {
			return tier, true
		}
	}
	return ModelPriceServiceTier{}, false
}

// ModelPriceForServiceTier applies a matched rule to the base price. Missing
// fields inherit the base rates, while explicit zero values remain configured.
func ModelPriceForServiceTier(price ModelPrice, serviceTier string) (ModelPrice, bool) {
	tier, ok := SelectModelPriceServiceTier(price, serviceTier)
	if !ok {
		return ModelPrice{}, false
	}
	effective := price
	effective.ContextTiers = nil
	effective.ServiceTiers = nil
	if tier.PromptConfigured {
		effective.Prompt = tier.Prompt
		effective.PromptConfigured = true
	}
	if tier.CompletionConfigured {
		effective.Completion = tier.Completion
		effective.CompletionConfigured = true
	}
	if tier.CacheConfigured {
		effective.Cache = tier.Cache
	}
	if tier.CacheReadConfigured {
		effective.CacheRead = tier.CacheRead
		effective.CacheReadConfigured = true
	}
	if tier.CacheCreationConfigured {
		effective.CacheCreation = tier.CacheCreation
		effective.CacheCreationConfigured = true
	}
	return effective, true
}

// ModelPriceStructureRevision changes only when the set of priced model IDs or
// their context-tier thresholds changes. Rate-only updates, including explicit
// service-tier rules, intentionally keep the revision stable because pricing
// rollups already group usage by service tier.
func ModelPriceStructureRevision(prices map[string]ModelPrice) string {
	modelIDs := make([]string, 0, len(prices))
	for modelID := range prices {
		modelIDs = append(modelIDs, modelID)
	}
	sort.Strings(modelIDs)
	hash := sha256.New()
	_, _ = hash.Write([]byte(modelPriceStructureRevisionVersion))
	var encoded [8]byte
	for _, modelID := range modelIDs {
		binary.BigEndian.PutUint64(encoded[:], uint64(len(modelID)))
		_, _ = hash.Write(encoded[:])
		_, _ = hash.Write([]byte(modelID))
		tiers := append([]ModelPriceContextTier(nil), prices[modelID].ContextTiers...)
		sort.Slice(tiers, func(i, j int) bool {
			return tiers[i].ThresholdTokens < tiers[j].ThresholdTokens
		})
		binary.BigEndian.PutUint64(encoded[:], uint64(len(tiers)))
		_, _ = hash.Write(encoded[:])
		for _, tier := range tiers {
			binary.BigEndian.PutUint64(encoded[:], uint64(tier.ThresholdTokens))
			_, _ = hash.Write(encoded[:])
		}
	}
	return modelPriceStructureRevisionVersion + ":" + hex.EncodeToString(hash.Sum(nil))
}

func ValidateModelPrice(modelID string, price ModelPrice) error {
	if strings.TrimSpace(modelID) == "" {
		return errors.New("model is required")
	}
	if !validModelPriceRuleValue(price.Prompt) ||
		!validModelPriceRuleValue(price.Completion) ||
		!validModelPriceRuleValue(price.Cache) ||
		!validModelPriceRuleValue(price.CacheRead) ||
		!validModelPriceRuleValue(price.CacheCreation) {
		return fmt.Errorf("invalid model price for %s", modelID)
	}
	_, err := NormalizeModelPriceContextTiers(price.ContextTiers)
	if err != nil {
		return fmt.Errorf("invalid model price for %s: %w", modelID, err)
	}
	_, err = NormalizeModelPriceServiceTiers(price.ServiceTiers)
	if err != nil {
		return fmt.Errorf("invalid model price for %s: %w", modelID, err)
	}
	return nil
}

func validModelPriceRuleValue(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
