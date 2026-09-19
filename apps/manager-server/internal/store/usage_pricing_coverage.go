package store

import "errors"

var ErrUsagePricingCoverageIncomplete = errors.New("usage pricing coverage is incomplete")

type hourlyPricingCoverageKey struct {
	bucketMS     int64
	model        string
	billingModel string
	serviceTier  string
	failed       bool
}

type hourlyPricingCoverageTotals [16]int64

// Pricing bands may split one permanent core row into several price rows.
// Compare every conserved metric after removing only the pricing dimensions;
// global call counts alone cannot detect a missing model or an incorrect bucket.
func hourlyPricingCoverageMatches(core []UsageHourlyAggregateRow, prices []UsagePricingHourlyRow) bool {
	remaining := make(map[hourlyPricingCoverageKey]hourlyPricingCoverageTotals, len(core))
	for _, row := range core {
		key := hourlyPricingCoverageKey{row.BucketMS, row.Model, row.BillingModel, row.ServiceTier, row.Failed}
		totals := remaining[key]
		values := hourlyPricingCoverageTotals{
			row.Calls, row.InputTokens, row.OutputTokens, row.ReasoningTokens,
			row.CachedTokens, row.CacheReadTokens, row.CacheCreationTokens,
			row.LongInputTokens, row.LongOutputTokens, row.LongCachedTokens,
			row.LongCacheReadTokens, row.LongCacheCreationTokens, row.TotalTokens,
			row.LatencySumMS, row.LatencySamples, row.ZeroTokenCalls,
		}
		for index, value := range values {
			totals[index] += value
		}
		remaining[key] = totals
	}
	for _, row := range prices {
		key := hourlyPricingCoverageKey{row.BucketMS, row.Model, row.BillingModel, row.ServiceTier, row.Failed}
		totals, exists := remaining[key]
		if !exists {
			return false
		}
		values := hourlyPricingCoverageTotals{
			row.Calls, row.InputTokens, row.OutputTokens, row.ReasoningTokens,
			row.CachedTokens, row.CacheReadTokens, row.CacheCreationTokens,
			row.LongInputTokens, row.LongOutputTokens, row.LongCachedTokens,
			row.LongCacheReadTokens, row.LongCacheCreationTokens, row.TotalTokens,
			row.LatencySumMS, row.LatencySamples, row.ZeroTokenCalls,
		}
		for index, value := range values {
			totals[index] -= value
		}
		remaining[key] = totals
	}
	for _, totals := range remaining {
		if totals != (hourlyPricingCoverageTotals{}) {
			return false
		}
	}
	return true
}

type accountPricingCoverageKey struct {
	accountKey   string
	model        string
	billingModel string
	serviceTier  string
}

type accountPricingCoverage struct {
	totals      [15]int64
	firstSeenMS int64
	lastSeenMS  int64
}

func addAccountPricingCoverage(grouped map[accountPricingCoverageKey]accountPricingCoverage, key accountPricingCoverageKey, values [15]int64, firstMS, lastMS int64) {
	entry, exists := grouped[key]
	for index, value := range values {
		entry.totals[index] += value
	}
	if !exists || firstMS < entry.firstSeenMS {
		entry.firstSeenMS = firstMS
	}
	entry.lastSeenMS = max(entry.lastSeenMS, lastMS)
	grouped[key] = entry
}

func accountPricingCoverageMatches(core []AccountHistoryRollupRow, prices []UsagePricingAccountRow) bool {
	coreTotals := make(map[accountPricingCoverageKey]accountPricingCoverage, len(core))
	priceTotals := make(map[accountPricingCoverageKey]accountPricingCoverage, len(prices))
	for _, row := range core {
		addAccountPricingCoverage(coreTotals,
			accountPricingCoverageKey{row.AccountKey, row.Model, row.BillingModel, row.ServiceTier},
			[15]int64{
				row.Calls, row.SuccessCalls, row.FailureCalls,
				row.InputTokens, row.OutputTokens, row.ReasoningTokens, row.CachedTokens,
				row.CacheReadTokens, row.CacheCreationTokens,
				row.LongInputTokens, row.LongOutputTokens, row.LongCachedTokens,
				row.LongCacheReadTokens, row.LongCacheCreationTokens, row.TotalTokens,
			}, row.FirstSeenMS, row.LastSeenMS)
	}
	for _, row := range prices {
		addAccountPricingCoverage(priceTotals,
			accountPricingCoverageKey{row.AccountKey, row.Model, row.BillingModel, row.ServiceTier},
			[15]int64{
				row.Calls, row.SuccessCalls, row.FailureCalls,
				row.InputTokens, row.OutputTokens, row.ReasoningTokens, row.CachedTokens,
				row.CacheReadTokens, row.CacheCreationTokens,
				row.LongInputTokens, row.LongOutputTokens, row.LongCachedTokens,
				row.LongCacheReadTokens, row.LongCacheCreationTokens, row.TotalTokens,
			}, row.FirstSeenMS, row.LastSeenMS)
	}
	if len(coreTotals) != len(priceTotals) {
		return false
	}
	for key, expected := range coreTotals {
		if actual, exists := priceTotals[key]; !exists || actual != expected {
			return false
		}
	}
	return true
}
