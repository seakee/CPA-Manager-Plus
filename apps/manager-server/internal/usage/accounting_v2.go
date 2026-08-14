package usage

import (
	"encoding/json"
	"strings"
)

const TokenAccountingSchemaVersion = 2

const (
	TokenAccountingQualityComplete     = "complete"
	TokenAccountingQualityInconsistent = "inconsistent"
	TokenAccountingQualityUnclassified = "unclassified"
)

// TokenInputBreakdown contains mutually exclusive input token buckets.
type TokenInputBreakdown struct {
	TotalTokens      int64 `json:"total_tokens"`
	UncachedTokens   int64 `json:"uncached_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

// TokenOutputBreakdown contains mutually exclusive output token buckets.
type TokenOutputBreakdown struct {
	TotalTokens        int64 `json:"total_tokens"`
	NonReasoningTokens int64 `json:"non_reasoning_tokens"`
	ReasoningTokens    int64 `json:"reasoning_tokens"`
}

// TokenBreakdown mirrors CPA's canonical, non-overlapping accounting contract.
type TokenBreakdown struct {
	SchemaVersion      int                  `json:"schema_version"`
	Quality            string               `json:"quality"`
	TotalTokens        int64                `json:"total_tokens"`
	Input              TokenInputBreakdown  `json:"input"`
	Output             TokenOutputBreakdown `json:"output"`
	UnclassifiedTokens int64                `json:"unclassified_tokens"`
}

// Valid reports whether the breakdown satisfies the CPA v2 invariants.
func (b TokenBreakdown) Valid() bool {
	if b.SchemaVersion != TokenAccountingSchemaVersion || !validTokenAccountingQuality(b.Quality) {
		return false
	}
	if b.TotalTokens < 0 || b.UnclassifiedTokens < 0 ||
		b.Input.TotalTokens < 0 || b.Input.UncachedTokens < 0 ||
		b.Input.CacheReadTokens < 0 || b.Input.CacheWriteTokens < 0 ||
		b.Output.TotalTokens < 0 || b.Output.NonReasoningTokens < 0 ||
		b.Output.ReasoningTokens < 0 {
		return false
	}
	inputTotal, inputValid := nonNegativeSum(
		b.Input.UncachedTokens,
		b.Input.CacheReadTokens,
		b.Input.CacheWriteTokens,
	)
	if !inputValid || b.Input.TotalTokens != inputTotal {
		return false
	}
	outputTotal, outputValid := nonNegativeSum(
		b.Output.NonReasoningTokens,
		b.Output.ReasoningTokens,
	)
	if !outputValid || b.Output.TotalTokens != outputTotal {
		return false
	}
	total, totalValid := nonNegativeSum(
		b.Input.TotalTokens,
		b.Output.TotalTokens,
		b.UnclassifiedTokens,
	)
	if !totalValid || b.TotalTokens != total {
		return false
	}
	return b.Quality != TokenAccountingQualityComplete || b.UnclassifiedTokens == 0
}

func validTokenAccountingQuality(quality string) bool {
	switch quality {
	case TokenAccountingQualityComplete, TokenAccountingQualityInconsistent, TokenAccountingQualityUnclassified:
		return true
	default:
		return false
	}
}

type TokenAccountingInput struct {
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheTokens         int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
}

type TokenAccounting struct {
	ReportedVersion int
	SourceValid     bool
	CacheInputMode  string
	Breakdown       TokenBreakdown
}

// ApplyTokenAccounting makes the canonical breakdown authoritative for all
// persisted and aggregated fields while retaining the original legacy fields.
func ApplyTokenAccounting(event *Event, record map[string]any) {
	if event == nil {
		return
	}
	context := CacheInputContext{
		ExplicitMode:     event.CacheInputMode,
		ExecutorType:     event.ExecutorType,
		Provider:         event.Provider,
		ProviderSnapshot: event.AuthProviderSnapshot,
		AuthType:         event.AuthType,
		ResolvedModel:    event.ResolvedModel,
		RequestedModel:   event.RequestedModel,
		DisplayModel:     event.Model,
	}
	rawHints := RawCacheAccountingHintsFromJSON(event.RawJSON)
	if rawHints.ExplicitMode != "" {
		context.ExplicitMode = rawHints.ExplicitMode
	}
	input := TokenAccountingInput{
		InputTokens:         event.InputTokens,
		OutputTokens:        event.OutputTokens,
		ReasoningTokens:     event.ReasoningTokens,
		CachedTokens:        event.CachedTokens,
		CacheTokens:         event.CacheTokens,
		CacheReadTokens:     event.CacheReadTokens,
		CacheCreationTokens: event.CacheCreationTokens,
		TotalTokens:         event.TotalTokens,
	}

	accounting := TokenAccounting{}
	if record != nil {
		accounting = ResolveTokenAccounting(record, context, input)
	} else if supportedPersistedTokenAccountingVersion(event.AccountingVersion) && event.TokenBreakdown.Valid() {
		accounting = TokenAccounting{
			ReportedVersion: event.AccountingVersion,
			SourceValid:     event.AccountingVersion == TokenAccountingSchemaVersion && event.AccountingValid,
			CacheInputMode:  InferCacheInputMode(context, event.CacheReadTokens, event.CacheCreationTokens),
			Breakdown:       event.TokenBreakdown,
		}
	} else if !supportedPersistedTokenAccountingVersion(event.AccountingVersion) {
		accounting = TokenAccounting{
			ReportedVersion: event.AccountingVersion,
			SourceValid:     false,
			CacheInputMode:  InferCacheInputMode(context, event.CacheReadTokens, event.CacheCreationTokens),
			Breakdown: inconsistentTokenBreakdown(
				input.TotalTokens,
				legacyTokenLowerBound(context, input),
			),
		}
	} else if rawRecord, ok := tokenAccountingRecordFromRawJSON(event.RawJSON); ok {
		accounting = ResolveTokenAccounting(rawRecord, context, input)
	} else if hasPersistedTokenAccountingClaim(event) {
		accounting = TokenAccounting{
			ReportedVersion: event.AccountingVersion,
			SourceValid:     false,
			CacheInputMode:  InferCacheInputMode(context, event.CacheReadTokens, event.CacheCreationTokens),
			Breakdown: inconsistentTokenBreakdown(
				input.TotalTokens,
				legacyTokenLowerBound(context, input),
			),
		}
	} else {
		if rawHints.HasInvalidExplicitTotal {
			input.TotalTokens = -1
		}
		accounting = ResolveTokenAccounting(nil, context, input)
	}

	event.AccountingVersion = accounting.ReportedVersion
	event.AccountingValid = accounting.SourceValid
	event.TokenBreakdown = accounting.Breakdown
	event.CacheInputMode = accounting.CacheInputMode
	event.NormalizedUncachedInputTokens = accounting.Breakdown.Input.UncachedTokens
	event.NormalizedTotalInputTokens = accounting.Breakdown.Input.TotalTokens
	event.NormalizedCacheReadTokens = accounting.Breakdown.Input.CacheReadTokens
	event.NormalizedCacheCreationTokens = accounting.Breakdown.Input.CacheWriteTokens
	event.NormalizedNonReasoningOutputTokens = accounting.Breakdown.Output.NonReasoningTokens
	event.NormalizedReasoningOutputTokens = accounting.Breakdown.Output.ReasoningTokens
	event.NormalizedTotalOutputTokens = accounting.Breakdown.Output.TotalTokens
	event.UnclassifiedTokens = accounting.Breakdown.UnclassifiedTokens
	event.TotalTokens = accounting.Breakdown.TotalTokens
}

func hasPersistedTokenAccountingClaim(event *Event) bool {
	return event != nil && (event.AccountingVersion != 0 || event.AccountingValid ||
		strings.TrimSpace(event.TokenBreakdown.Quality) != "")
}

func supportedPersistedTokenAccountingVersion(version int) bool {
	return version == 0 || version == TokenAccountingSchemaVersion
}

// RestorePersistedTokenAccounting rebuilds the public contract from canonical
// SQLite columns. It returns false for legacy or partially migrated rows.
func RestorePersistedTokenAccounting(event *Event, quality string) bool {
	if event == nil || !supportedPersistedTokenAccountingVersion(event.AccountingVersion) {
		return false
	}
	breakdown := TokenBreakdown{
		SchemaVersion: TokenAccountingSchemaVersion,
		Quality:       quality,
		TotalTokens:   event.TotalTokens,
		Input: TokenInputBreakdown{
			TotalTokens:      event.NormalizedTotalInputTokens,
			UncachedTokens:   event.NormalizedUncachedInputTokens,
			CacheReadTokens:  event.NormalizedCacheReadTokens,
			CacheWriteTokens: event.NormalizedCacheCreationTokens,
		},
		Output: TokenOutputBreakdown{
			TotalTokens:        event.NormalizedTotalOutputTokens,
			NonReasoningTokens: event.NormalizedNonReasoningOutputTokens,
			ReasoningTokens:    event.NormalizedReasoningOutputTokens,
		},
		UnclassifiedTokens: event.UnclassifiedTokens,
	}
	if !breakdown.Valid() {
		return false
	}
	event.AccountingValid = event.AccountingVersion == TokenAccountingSchemaVersion && event.AccountingValid
	event.TokenBreakdown = breakdown
	return true
}

// ResolveTokenAccounting prefers a valid CPA v2 payload and otherwise applies
// the provider-aware legacy compatibility rules. Unknown legacy semantics stay
// unclassified instead of being assigned to a billable bucket by guesswork.
func ResolveTokenAccounting(record map[string]any, context CacheInputContext, input TokenAccountingInput) TokenAccounting {
	version, breakdown, present := tokenBreakdownFromRecord(record)
	versionExplicit := hasRecordField(record, "accounting_version", "accountingVersion")
	validityExplicit := hasRecordField(record, "accounting_valid", "accountingValid")
	qualityExplicit := hasRecordField(record, "accounting_quality", "accountingQuality")
	versionValue, versionValid := readCanonicalInt(record, "accounting_version", "accountingVersion")
	validityValue, validityValid := readCanonicalBool(record, "accounting_valid", "accountingValid")
	explicitLegacyCanonical := versionExplicit && versionValid && versionValue == 0
	reportedVersion := version
	mode := InferCacheInputMode(context, input.CacheReadTokens, input.CacheCreationTokens)
	if (reportedVersion == TokenAccountingSchemaVersion || explicitLegacyCanonical) && present && breakdown.Valid() &&
		(!validityExplicit || validityValid) {
		sourceValid := reportedVersion == TokenAccountingSchemaVersion
		if validityExplicit {
			sourceValid = sourceValid && validityValue
		}
		return TokenAccounting{
			ReportedVersion: reportedVersion,
			SourceValid:     sourceValid,
			CacheInputMode:  mode,
			Breakdown:       breakdown,
		}
	}
	if version != 0 || present || validityExplicit || qualityExplicit {
		return TokenAccounting{
			ReportedVersion: reportedVersion,
			SourceValid:     false,
			CacheInputMode:  mode,
			Breakdown: inconsistentTokenBreakdown(
				input.TotalTokens,
				legacyTokenLowerBound(context, input),
			),
		}
	}
	if _, totalState := explicitTotalFromRecord(record); totalState == explicitTotalInvalid {
		return TokenAccounting{
			ReportedVersion: reportedVersion,
			SourceValid:     false,
			CacheInputMode:  mode,
			Breakdown: inconsistentTokenBreakdown(
				input.TotalTokens,
				legacyTokenLowerBound(context, input),
			),
		}
	}

	return TokenAccounting{
		ReportedVersion: reportedVersion,
		SourceValid:     false,
		CacheInputMode:  mode,
		Breakdown:       legacyTokenBreakdown(context, input),
	}
}

func hasRecordField(record map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := record[key]; ok {
			return true
		}
	}
	return false
}

func tokenBreakdownFromRecord(record map[string]any) (int, TokenBreakdown, bool) {
	if record == nil {
		return 0, TokenBreakdown{}, false
	}
	versionPresent := false
	for _, key := range []string{"accounting_version", "accountingVersion"} {
		if _, ok := record[key]; ok {
			versionPresent = true
			break
		}
	}
	versionValue, versionValid := readCanonicalInt(record, "accounting_version", "accountingVersion")
	version := int(versionValue)
	if versionPresent && (!versionValid || int64(version) != versionValue) {
		return 0, TokenBreakdown{}, true
	}
	var raw any
	breakdownPresent := false
	for _, key := range []string{"token_breakdown", "tokenBreakdown"} {
		if value, ok := record[key]; ok {
			raw = value
			breakdownPresent = true
			break
		}
	}
	if !breakdownPresent {
		return version, TokenBreakdown{}, false
	}
	breakdownRecord, ok := raw.(map[string]any)
	if !ok {
		return version, TokenBreakdown{}, true
	}
	inputRecord, inputPresent := first(breakdownRecord, "input").(map[string]any)
	outputRecord, outputPresent := first(breakdownRecord, "output").(map[string]any)
	schemaVersion, schemaVersionValid := readCanonicalInt(breakdownRecord, "schema_version", "schemaVersion")
	totalTokens, totalTokensValid := readCanonicalInt(breakdownRecord, "total_tokens", "totalTokens")
	inputTotalTokens, inputTotalTokensValid := readCanonicalInt(inputRecord, "total_tokens", "totalTokens")
	uncachedTokens, uncachedTokensValid := readCanonicalInt(inputRecord, "uncached_tokens", "uncachedTokens")
	cacheReadTokens, cacheReadTokensValid := readCanonicalInt(inputRecord, "cache_read_tokens", "cacheReadTokens")
	cacheWriteTokens, cacheWriteTokensValid := readCanonicalInt(inputRecord, "cache_write_tokens", "cacheWriteTokens")
	outputTotalTokens, outputTotalTokensValid := readCanonicalInt(outputRecord, "total_tokens", "totalTokens")
	nonReasoningTokens, nonReasoningTokensValid := readCanonicalInt(outputRecord, "non_reasoning_tokens", "nonReasoningTokens")
	reasoningTokens, reasoningTokensValid := readCanonicalInt(outputRecord, "reasoning_tokens", "reasoningTokens")
	unclassifiedTokens, unclassifiedTokensValid := readCanonicalInt(breakdownRecord, "unclassified_tokens", "unclassifiedTokens")
	breakdown := TokenBreakdown{
		SchemaVersion: int(schemaVersion),
		Quality:       strings.ToLower(strings.TrimSpace(readString(breakdownRecord, "quality"))),
		TotalTokens:   totalTokens,
		Input: TokenInputBreakdown{
			TotalTokens:      inputTotalTokens,
			UncachedTokens:   uncachedTokens,
			CacheReadTokens:  cacheReadTokens,
			CacheWriteTokens: cacheWriteTokens,
		},
		Output: TokenOutputBreakdown{
			TotalTokens:        outputTotalTokens,
			NonReasoningTokens: nonReasoningTokens,
			ReasoningTokens:    reasoningTokens,
		},
		UnclassifiedTokens: unclassifiedTokens,
	}
	if !inputPresent || !outputPresent ||
		!schemaVersionValid || !totalTokensValid || !inputTotalTokensValid ||
		!uncachedTokensValid || !cacheReadTokensValid || !cacheWriteTokensValid ||
		!outputTotalTokensValid || !nonReasoningTokensValid || !reasoningTokensValid ||
		!unclassifiedTokensValid {
		breakdown.Quality = ""
	}
	return version, breakdown, true
}

func readCanonicalInt(record map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		raw, ok := record[key]
		if !ok || raw == nil {
			continue
		}
		switch value := raw.(type) {
		case float64:
			parsed := int64(value)
			if float64(parsed) != value {
				return 0, false
			}
			return parsed, true
		case int:
			return int64(value), true
		case int32:
			return int64(value), true
		case int64:
			return value, true
		case uint:
			parsed := int64(value)
			if uint(parsed) != value {
				return 0, false
			}
			return parsed, true
		case uint32:
			return int64(value), true
		case uint64:
			parsed := int64(value)
			if uint64(parsed) != value {
				return 0, false
			}
			return parsed, true
		case json.Number:
			parsed, err := value.Int64()
			return parsed, err == nil
		default:
			return 0, false
		}
	}
	return 0, false
}

func readCanonicalBool(record map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		raw, ok := record[key]
		if !ok || raw == nil {
			continue
		}
		value, ok := raw.(bool)
		return value, ok
	}
	return false, false
}

func tokenAccountingRecordFromRawJSON(raw string) (map[string]any, bool) {
	for depth := 0; depth <= maxRawAccountingJSONDepth; depth++ {
		if strings.TrimSpace(raw) == "" {
			return nil, false
		}
		var record map[string]any
		if err := decodeJSON([]byte(raw), &record); err != nil {
			return nil, false
		}
		if detail, ok := record["detail"].(map[string]any); ok {
			record = detail
		}
		version, _, present := tokenBreakdownFromRecord(record)
		if present || version != 0 ||
			hasRecordField(record, "accounting_valid", "accountingValid") ||
			hasRecordField(record, "accounting_quality", "accountingQuality") {
			return record, true
		}
		raw = readString(record, "raw_json", "rawJson")
	}
	return nil, false
}

func legacyTokenBreakdown(context CacheInputContext, input TokenAccountingInput) TokenBreakdown {
	if input.InputTokens < 0 || input.OutputTokens < 0 || input.ReasoningTokens < 0 ||
		input.CachedTokens < 0 || input.CacheTokens < 0 || input.CacheReadTokens < 0 ||
		input.CacheCreationTokens < 0 || input.TotalTokens < 0 {
		return inconsistentTokenBreakdown(input.TotalTokens, legacyTokenLowerBound(context, input))
	}

	cache := NormalizeCacheAccounting(
		context,
		input.InputTokens,
		input.CachedTokens,
		input.CacheTokens,
		input.CacheReadTokens,
		input.CacheCreationTokens,
	)
	lowerBound := legacyTokenLowerBound(context, input)
	semantics := inferLegacyOutputSemantics(context)
	if semantics == legacyOutputUnknown {
		return unclassifiedTokenBreakdown(lowerBound)
	}
	cacheTotal, cacheValid := nonNegativeSum(
		cache.UncachedInputTokens,
		cache.CacheReadTokens,
		cache.CacheCreationTokens,
	)
	if !cacheValid || cache.TotalInputTokens != cacheTotal {
		return inconsistentTokenBreakdown(input.TotalTokens, lowerBound)
	}
	outputTotal := input.OutputTokens
	nonReasoning := input.OutputTokens - input.ReasoningTokens
	if semantics == legacyOutputSeparateReasoning {
		var outputValid bool
		outputTotal, outputValid = nonNegativeSum(input.OutputTokens, input.ReasoningTokens)
		if !outputValid {
			return inconsistentTokenBreakdown(input.TotalTokens, maxInt64(outputTotal, lowerBound))
		}
		nonReasoning = input.OutputTokens
	}
	if nonReasoning < 0 {
		return inconsistentTokenBreakdown(
			input.TotalTokens,
			lowerBound,
		)
	}

	expected, expectedValid := nonNegativeSum(cache.TotalInputTokens, outputTotal)
	if !expectedValid {
		return inconsistentTokenBreakdown(input.TotalTokens, maxInt64(expected, lowerBound))
	}
	resolved, quality, unclassified, ok := resolveLegacyAccountingTotal(input.TotalTokens, expected)
	if !ok {
		return inconsistentTokenBreakdown(input.TotalTokens, maxInt64(expected, lowerBound))
	}
	return TokenBreakdown{
		SchemaVersion: TokenAccountingSchemaVersion,
		Quality:       quality,
		TotalTokens:   resolved,
		Input: TokenInputBreakdown{
			TotalTokens:      cache.TotalInputTokens,
			UncachedTokens:   cache.UncachedInputTokens,
			CacheReadTokens:  cache.CacheReadTokens,
			CacheWriteTokens: cache.CacheCreationTokens,
		},
		Output: TokenOutputBreakdown{
			TotalTokens:        outputTotal,
			NonReasoningTokens: nonReasoning,
			ReasoningTokens:    input.ReasoningTokens,
		},
		UnclassifiedTokens: unclassified,
	}
}

type legacyOutputSemantics uint8

const (
	legacyOutputUnknown legacyOutputSemantics = iota
	legacyOutputReasoningSubset
	legacyOutputSeparateReasoning
)

func inferLegacyOutputSemantics(context CacheInputContext) legacyOutputSemantics {
	for _, value := range []string{context.ExecutorType, context.Provider, context.ProviderSnapshot, context.AuthType} {
		if semantics := classifyLegacyOutputSemantics(value); semantics != legacyOutputUnknown {
			return semantics
		}
	}
	for _, value := range []string{context.ResolvedModel, context.RequestedModel, context.DisplayModel} {
		if semantics := classifyLegacyOutputSemantics(value); semantics != legacyOutputUnknown {
			return semantics
		}
	}
	return legacyOutputUnknown
}

func classifyLegacyOutputSemantics(value string) legacyOutputSemantics {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" || normalized == "unknown" {
		return legacyOutputUnknown
	}
	if normalized == "openaicompatexecutor" || normalized == "openai-compatibility" || strings.HasPrefix(normalized, "openai-compatible-") {
		return legacyOutputReasoningSubset
	}
	if strings.Contains(normalized, "claude") || strings.Contains(normalized, "anthropic") {
		return legacyOutputSeparateReasoning
	}
	for _, marker := range []string{"gemini", "aistudio", "ai_studio", "ai-studio", "antigravity", "vertex", "interaction"} {
		if strings.Contains(normalized, marker) {
			return legacyOutputSeparateReasoning
		}
	}
	for _, marker := range []string{"openaicompat", "openai_compat", "openai-compat", "openai", "gpt-", "codex", "xai", "grok", "kimi", "qwen", "deepseek", "openrouter"} {
		if strings.Contains(normalized, marker) {
			return legacyOutputReasoningSubset
		}
	}
	return legacyOutputUnknown
}

func resolveLegacyAccountingTotal(total, expected int64) (int64, string, int64, bool) {
	if total < 0 || expected < 0 {
		return 0, TokenAccountingQualityInconsistent, 0, false
	}
	if total == 0 || total == expected {
		return expected, TokenAccountingQualityComplete, 0, true
	}
	if total > expected {
		return total, TokenAccountingQualityUnclassified, total - expected, true
	}
	return 0, TokenAccountingQualityInconsistent, 0, false
}

func legacyTokenLowerBound(context CacheInputContext, input TokenAccountingInput) int64 {
	cache := NormalizeCacheAccounting(
		context,
		input.InputTokens,
		input.CachedTokens,
		input.CacheTokens,
		input.CacheReadTokens,
		input.CacheCreationTokens,
	)
	cacheTotal, _ := nonNegativeSum(cache.CacheReadTokens, cache.CacheCreationTokens)
	inputTotal := maxInt64(cache.TotalInputTokens, cacheTotal)
	outputTotal := maxInt64(input.OutputTokens, 0)
	if input.ReasoningTokens > outputTotal {
		outputTotal = input.ReasoningTokens
	}
	lowerBound, _ := nonNegativeSum(inputTotal, outputTotal)
	if input.TotalTokens > lowerBound {
		return input.TotalTokens
	}
	return lowerBound
}

func unclassifiedTokenBreakdown(total int64) TokenBreakdown {
	if total < 0 {
		total = 0
	}
	quality := TokenAccountingQualityComplete
	if total > 0 {
		quality = TokenAccountingQualityUnclassified
	}
	return TokenBreakdown{
		SchemaVersion:      TokenAccountingSchemaVersion,
		Quality:            quality,
		TotalTokens:        total,
		UnclassifiedTokens: total,
	}
}

func inconsistentTokenBreakdown(total, fallback int64) TokenBreakdown {
	resolved := maxInt64(total, fallback)
	if resolved < 0 {
		resolved = 0
	}
	return TokenBreakdown{
		SchemaVersion:      TokenAccountingSchemaVersion,
		Quality:            TokenAccountingQualityInconsistent,
		TotalTokens:        resolved,
		UnclassifiedTokens: resolved,
	}
}
