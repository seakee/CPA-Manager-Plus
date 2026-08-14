// Package usageaccountingsql centralizes the SQLite projections used while
// token-accounting-v2 migration is still pending. Persisted canonical buckets
// are authoritative; legacy rows stay wholly unclassified instead of being
// split with provider-specific guesses inside SQL.
package usageaccountingsql

import "strings"

const sqliteMaxInt64 = "9223372036854775807"

// Expressions contains non-overlapping token projections for one usage_events
// table alias. Alias must be a trusted SQL identifier supplied by the caller.
type Expressions struct {
	Ready              string
	Valid              string
	Quality            string
	UncachedInput      string
	TotalInput         string
	TotalOutput        string
	NonReasoningOutput string
	ReasoningOutput    string
	CacheRead          string
	CacheCreation      string
	CompatibleCached   string
	Unclassified       string
	Total              string
	Incomplete         string
	PricingSafe        string
}

// For returns canonical-first SQLite expressions. When any persisted
// accounting-v2 bucket is missing, every billable bucket becomes zero and a
// conservative lower bound is exposed through unclassified/total instead.
func For(alias string) Expressions {
	column := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	canonicalColumns := []string{
		"normalized_uncached_input_tokens",
		"normalized_total_input_tokens",
		"normalized_cache_read_tokens",
		"normalized_cache_creation_tokens",
		"normalized_non_reasoning_output_tokens",
		"normalized_reasoning_output_tokens",
		"normalized_total_output_tokens",
		"unclassified_tokens",
		"total_tokens",
	}
	canonicalColumnRefs := make([]string, 0, len(canonicalColumns))
	readyParts := make([]string, 0, len(canonicalColumns)+6)
	for _, name := range canonicalColumns {
		ref := column(name)
		canonicalColumnRefs = append(canonicalColumnRefs, ref)
		readyParts = append(readyParts, ref+" is not null", "typeof("+ref+") = 'integer'")
	}
	quality := "lower(trim(coalesce(" + column("accounting_quality") + ", '')))"
	canonicalInputTotal, canonicalInputValid := saturatedNonNegativeSum(
		column("normalized_uncached_input_tokens"),
		column("normalized_cache_read_tokens"),
		column("normalized_cache_creation_tokens"),
	)
	canonicalOutputTotal, canonicalOutputValid := saturatedNonNegativeSum(
		column("normalized_non_reasoning_output_tokens"),
		column("normalized_reasoning_output_tokens"),
	)
	canonicalTotal, canonicalTotalValid := saturatedNonNegativeSum(
		column("normalized_total_input_tokens"),
		column("normalized_total_output_tokens"),
		column("unclassified_tokens"),
	)
	readyParts = append(readyParts,
		"coalesce("+column("accounting_version")+", 0) in (0, 2)",
		quality+" in ('complete', 'inconsistent', 'unclassified')",
		"min("+strings.Join(canonicalColumnRefs, ", ")+") >= 0",
		canonicalInputValid,
		canonicalOutputValid,
		canonicalTotalValid,
		column("normalized_total_input_tokens")+" = "+canonicalInputTotal,
		column("normalized_total_output_tokens")+" = "+canonicalOutputTotal,
		column("total_tokens")+" = "+canonicalTotal,
		"("+quality+" <> 'complete' or "+column("unclassified_tokens")+" = 0)",
	)
	ready := "(" + strings.Join(readyParts, " and ") + ")"
	valid := "case when " + ready + " and coalesce(" + column("accounting_version") + ", 0) = 2 and coalesce(" + column("accounting_valid") + ", 0) <> 0 then 1 else 0 end"

	cacheRead := "max(coalesce(" + column("cache_read_tokens") + ", 0), 0)"
	cacheCreation := "max(coalesce(" + column("cache_creation_tokens") + ", 0), 0)"
	fineGrainedCache, _ := saturatedNonNegativeSum(cacheRead, cacheCreation)
	compatibleLegacyCache := "max(max(coalesce(" + column("cached_tokens") + ", 0), coalesce(" + column("cache_tokens") + ", 0)) - " + fineGrainedCache + ", 0)"
	legacyCacheLowerBound, _ := saturatedNonNegativeSum(fineGrainedCache, compatibleLegacyCache)
	legacyInputLowerBound := "max(max(coalesce(" + column("input_tokens") + ", 0), 0), " + legacyCacheLowerBound + ")"
	legacyOutputLowerBound := "max(max(coalesce(" + column("output_tokens") + ", 0), 0), max(coalesce(" + column("reasoning_tokens") + ", 0), 0))"
	legacyDerivedLowerBound, _ := saturatedNonNegativeSum(legacyInputLowerBound, legacyOutputLowerBound)
	legacyTotalLowerBound := "max(max(coalesce(" + column("total_tokens") + ", 0), 0), " + legacyDerivedLowerBound + ")"
	canonicalClaim := "(coalesce(" + column("accounting_version") + ", 0) <> 0 or coalesce(" + column("accounting_valid") + ", 0) <> 0 or trim(coalesce(" + column("accounting_quality") + ", '')) <> '')"
	projectedQuality := "case when " + ready + " then " + quality + " when " + canonicalClaim + " then 'inconsistent' when " + legacyTotalLowerBound + " > 0 then 'unclassified' else 'complete' end"

	canonical := func(name string) string {
		return "case when " + ready + " then max(coalesce(" + column(name) + ", 0), 0) else 0 end"
	}
	canonicalCacheRead := canonical("normalized_cache_read_tokens")
	canonicalCacheCreation := canonical("normalized_cache_creation_tokens")
	// The canonical breakdown has explicit read/write buckets and no generic
	// cached bucket. Legacy cached values are folded into normalized cache read
	// during migration; exposing any residual here would overlap canonical or
	// unclassified tokens.
	compatibleCached := "0"
	unclassified := "case when " + ready + " then max(coalesce(" + column("unclassified_tokens") + ", 0), 0) else " + legacyTotalLowerBound + " end"
	total := "case when " + ready + " then max(coalesce(" + column("total_tokens") + ", 0), 0) else " + legacyTotalLowerBound + " end"
	incomplete := "case when " + ready + " then case when max(coalesce(" + column("unclassified_tokens") + ", 0), 0) > 0 or " + quality + " in ('inconsistent', 'unclassified') then 1 else 0 end else case when " + canonicalClaim + " or " + legacyTotalLowerBound + " > 0 then 1 else 0 end end"

	return Expressions{
		Ready:              ready,
		Valid:              valid,
		Quality:            projectedQuality,
		UncachedInput:      canonical("normalized_uncached_input_tokens"),
		TotalInput:         canonical("normalized_total_input_tokens"),
		TotalOutput:        canonical("normalized_total_output_tokens"),
		NonReasoningOutput: canonical("normalized_non_reasoning_output_tokens"),
		ReasoningOutput:    canonical("normalized_reasoning_output_tokens"),
		CacheRead:          canonicalCacheRead,
		CacheCreation:      canonicalCacheCreation,
		CompatibleCached:   compatibleCached,
		Unclassified:       unclassified,
		Total:              total,
		Incomplete:         incomplete,
		// Invalid or legacy rows already project every billable bucket to zero.
		// This additional guard only needs the stored canonical quality to keep a
		// structurally valid but inconsistent breakdown out of pricing rollups.
		PricingSafe: "(" + quality + " <> 'inconsistent')",
	}
}

func saturatedNonNegativeSum(terms ...string) (string, string) {
	sum := "0"
	validParts := make([]string, 0, len(terms))
	for _, term := range terms {
		valid := "(" + term + " <= " + sqliteMaxInt64 + " - " + sum + ")"
		validParts = append(validParts, valid)
		sum = "(case when " + valid + " then " + sum + " + " + term + " else " + sqliteMaxInt64 + " end)"
	}
	return sum, "(" + strings.Join(validParts, " and ") + ")"
}
