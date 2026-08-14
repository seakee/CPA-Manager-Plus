package usage

import (
	"encoding/json"
	"math"
	"testing"
)

func TestTokenBreakdownValidatesCanonicalInvariants(t *testing.T) {
	valid := TokenBreakdown{
		SchemaVersion: TokenAccountingSchemaVersion,
		Quality:       TokenAccountingQualityComplete,
		TotalTokens:   165,
		Input: TokenInputBreakdown{
			TotalTokens:      125,
			UncachedTokens:   100,
			CacheReadTokens:  20,
			CacheWriteTokens: 5,
		},
		Output: TokenOutputBreakdown{
			TotalTokens:        40,
			NonReasoningTokens: 30,
			ReasoningTokens:    10,
		},
	}
	if !valid.Valid() {
		t.Fatal("valid canonical breakdown was rejected")
	}

	tests := []struct {
		name   string
		mutate func(*TokenBreakdown)
	}{
		{name: "schema", mutate: func(b *TokenBreakdown) { b.SchemaVersion = 1 }},
		{name: "input sum", mutate: func(b *TokenBreakdown) { b.Input.TotalTokens++ }},
		{name: "output sum", mutate: func(b *TokenBreakdown) { b.Output.TotalTokens++ }},
		{name: "overall sum", mutate: func(b *TokenBreakdown) { b.TotalTokens++ }},
		{name: "complete unclassified", mutate: func(b *TokenBreakdown) {
			b.UnclassifiedTokens = 1
			b.TotalTokens++
		}},
		{name: "negative", mutate: func(b *TokenBreakdown) { b.Output.ReasoningTokens = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := valid
			tt.mutate(&got)
			if got.Valid() {
				t.Fatalf("invalid breakdown accepted: %+v", got)
			}
		})
	}
}

func TestTokenBreakdownRejectsOverflowingCanonicalSums(t *testing.T) {
	tests := []struct {
		name      string
		breakdown TokenBreakdown
	}{
		{
			name: "input buckets wrap back to a positive total",
			breakdown: TokenBreakdown{
				SchemaVersion: TokenAccountingSchemaVersion,
				Quality:       TokenAccountingQualityComplete,
				TotalTokens:   3,
				Input: TokenInputBreakdown{
					TotalTokens:      3,
					UncachedTokens:   math.MaxInt64,
					CacheReadTokens:  math.MaxInt64,
					CacheWriteTokens: 5,
				},
			},
		},
		{
			name: "overall buckets wrap back to a positive total",
			breakdown: TokenBreakdown{
				SchemaVersion:      TokenAccountingSchemaVersion,
				Quality:            TokenAccountingQualityUnclassified,
				TotalTokens:        3,
				UnclassifiedTokens: 5,
				Input: TokenInputBreakdown{
					TotalTokens:    math.MaxInt64,
					UncachedTokens: math.MaxInt64,
				},
				Output: TokenOutputBreakdown{
					TotalTokens:        math.MaxInt64,
					NonReasoningTokens: math.MaxInt64,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.breakdown.Valid() {
				t.Fatalf("overflowing breakdown accepted: %+v", tt.breakdown)
			}
		})
	}
}

func TestResolveTokenAccountingPrefersValidV2(t *testing.T) {
	record := map[string]any{
		"accounting_version": float64(2),
		"token_breakdown": map[string]any{
			"schema_version": float64(2),
			"quality":        "complete",
			"total_tokens":   float64(165),
			"input": map[string]any{
				"total_tokens":       float64(125),
				"uncached_tokens":    float64(100),
				"cache_read_tokens":  float64(20),
				"cache_write_tokens": float64(5),
			},
			"output": map[string]any{
				"total_tokens":         float64(40),
				"non_reasoning_tokens": float64(30),
				"reasoning_tokens":     float64(10),
			},
			"unclassified_tokens": float64(0),
		},
	}
	got := ResolveTokenAccounting(record, CacheInputContext{Provider: "openai"}, TokenAccountingInput{
		InputTokens: 999, OutputTokens: 888, ReasoningTokens: 777, TotalTokens: 666,
	})
	if !got.SourceValid || got.ReportedVersion != 2 || !got.Breakdown.Valid() {
		t.Fatalf("accounting metadata = %+v", got)
	}
	if got.Breakdown.Input.UncachedTokens != 100 || got.Breakdown.Output.NonReasoningTokens != 30 || got.Breakdown.TotalTokens != 165 {
		t.Fatalf("v2 breakdown was not authoritative: %+v", got.Breakdown)
	}
}

func TestResolveTokenAccountingPreservesExplicitInvalidProvenance(t *testing.T) {
	record := map[string]any{
		"accounting_version": float64(2),
		"accounting_valid":   false,
		"token_breakdown": map[string]any{
			"schema_version":      float64(2),
			"quality":             "complete",
			"total_tokens":        float64(140),
			"unclassified_tokens": float64(0),
			"input": map[string]any{
				"total_tokens":       float64(100),
				"uncached_tokens":    float64(70),
				"cache_read_tokens":  float64(20),
				"cache_write_tokens": float64(10),
			},
			"output": map[string]any{
				"total_tokens":         float64(40),
				"non_reasoning_tokens": float64(30),
				"reasoning_tokens":     float64(10),
			},
		},
	}

	got := ResolveTokenAccounting(record, CacheInputContext{Provider: "openai"}, TokenAccountingInput{})
	if got.SourceValid || got.ReportedVersion != 2 || got.Breakdown.Quality != TokenAccountingQualityComplete ||
		got.Breakdown.TotalTokens != 140 || !got.Breakdown.Valid() {
		t.Fatalf("accounting provenance = %+v", got)
	}
}

func TestResolveTokenAccountingPreservesExplicitLegacyProvenance(t *testing.T) {
	record := map[string]any{
		"accounting_version": float64(0),
		"accounting_valid":   false,
		"token_breakdown": map[string]any{
			"schema_version":      float64(2),
			"quality":             "complete",
			"total_tokens":        float64(140),
			"unclassified_tokens": float64(0),
			"input": map[string]any{
				"total_tokens":       float64(100),
				"uncached_tokens":    float64(70),
				"cache_read_tokens":  float64(20),
				"cache_write_tokens": float64(10),
			},
			"output": map[string]any{
				"total_tokens":         float64(40),
				"non_reasoning_tokens": float64(30),
				"reasoning_tokens":     float64(10),
			},
		},
	}

	got := ResolveTokenAccounting(record, CacheInputContext{Provider: "openai"}, TokenAccountingInput{})
	if got.SourceValid || got.ReportedVersion != 0 || got.Breakdown.Quality != TokenAccountingQualityComplete ||
		got.Breakdown.TotalTokens != 140 || !got.Breakdown.Valid() {
		t.Fatalf("legacy accounting provenance = %+v", got)
	}
}

func TestResolveTokenAccountingKeepsInvalidV2IncompleteAndUnpriced(t *testing.T) {
	record := map[string]any{
		"accounting_version": float64(2),
		"token_breakdown": map[string]any{
			"schema_version": float64(2),
			"quality":        "complete",
			"total_tokens":   float64(139),
			"input": map[string]any{
				"total_tokens":       float64(100),
				"uncached_tokens":    float64(100),
				"cache_read_tokens":  float64(0),
				"cache_write_tokens": float64(0),
			},
			"output": map[string]any{
				"total_tokens":         float64(40),
				"non_reasoning_tokens": float64(30),
				"reasoning_tokens":     float64(10),
			},
			"unclassified_tokens": float64(0),
		},
	}
	got := ResolveTokenAccounting(record, CacheInputContext{Provider: "openai"}, TokenAccountingInput{
		InputTokens: 100, OutputTokens: 40, ReasoningTokens: 10, TotalTokens: 140,
	})
	if got.SourceValid || got.ReportedVersion != 2 {
		t.Fatalf("accounting metadata = %+v", got)
	}
	if got.Breakdown.Quality != TokenAccountingQualityInconsistent || got.Breakdown.UnclassifiedTokens != 140 ||
		got.Breakdown.TotalTokens != 140 || got.Breakdown.Input.TotalTokens != 0 || got.Breakdown.Output.TotalTokens != 0 ||
		!got.Breakdown.Valid() {
		t.Fatalf("invalid v2 fallback = %+v", got.Breakdown)
	}
}

func TestApplyTokenAccountingDoesNotReclassifyInvalidPersistedCanonicalWithoutRawProvenance(t *testing.T) {
	event := Event{
		Provider:          "openai",
		Model:             "gpt-5",
		AccountingVersion: TokenAccountingSchemaVersion,
		AccountingValid:   true,
		TokenBreakdown: TokenBreakdown{
			Quality: TokenAccountingQualityComplete,
		},
		InputTokens:  100,
		OutputTokens: 20,
		TotalTokens:  120,
	}

	ApplyTokenAccounting(&event, nil)

	if event.AccountingVersion != TokenAccountingSchemaVersion || event.AccountingValid ||
		event.TokenBreakdown.Quality != TokenAccountingQualityInconsistent ||
		event.TokenBreakdown.UnclassifiedTokens != 120 || event.TokenBreakdown.TotalTokens != 120 ||
		event.TokenBreakdown.Input.TotalTokens != 0 || event.TokenBreakdown.Output.TotalTokens != 0 ||
		!event.TokenBreakdown.Valid() {
		t.Fatalf("invalid persisted canonical fallback = %+v", event)
	}
}

func TestApplyTokenAccountingDoesNotTrustVersionZeroValidityFlag(t *testing.T) {
	event := Event{
		Provider:          "openai",
		Model:             "gpt-5",
		AccountingVersion: 0,
		AccountingValid:   true,
		TokenBreakdown: TokenBreakdown{
			SchemaVersion: TokenAccountingSchemaVersion,
			Quality:       TokenAccountingQualityComplete,
			TotalTokens:   120,
			Input: TokenInputBreakdown{
				TotalTokens:    100,
				UncachedTokens: 100,
			},
			Output: TokenOutputBreakdown{
				TotalTokens:        20,
				NonReasoningTokens: 20,
			},
		},
		InputTokens:  100,
		OutputTokens: 20,
		TotalTokens:  120,
	}

	ApplyTokenAccounting(&event, nil)

	if event.AccountingVersion != 0 || event.AccountingValid ||
		event.TokenBreakdown.Quality != TokenAccountingQualityComplete ||
		event.TokenBreakdown.TotalTokens != 120 || !event.TokenBreakdown.Valid() {
		t.Fatalf("version zero validity flag was trusted: %+v", event)
	}
}

func TestApplyTokenAccountingRejectsFuturePersistedAccountingVersion(t *testing.T) {
	event := Event{
		Provider:          "openai",
		Model:             "gpt-5",
		AccountingVersion: TokenAccountingSchemaVersion + 1,
		AccountingValid:   true,
		TokenBreakdown: TokenBreakdown{
			SchemaVersion: TokenAccountingSchemaVersion,
			Quality:       TokenAccountingQualityComplete,
			TotalTokens:   120,
			Input: TokenInputBreakdown{
				TotalTokens:    100,
				UncachedTokens: 100,
			},
			Output: TokenOutputBreakdown{
				TotalTokens:        20,
				NonReasoningTokens: 20,
			},
		},
		InputTokens:  100,
		OutputTokens: 20,
		TotalTokens:  120,
	}

	ApplyTokenAccounting(&event, nil)

	if event.AccountingVersion != TokenAccountingSchemaVersion+1 || event.AccountingValid ||
		event.TokenBreakdown.Quality != TokenAccountingQualityInconsistent ||
		event.TokenBreakdown.UnclassifiedTokens != 120 || event.TokenBreakdown.TotalTokens != 120 ||
		event.TokenBreakdown.Input.TotalTokens != 0 || event.TokenBreakdown.Output.TotalTokens != 0 ||
		!event.TokenBreakdown.Valid() {
		t.Fatalf("future persisted accounting was trusted: %+v", event)
	}
}

func TestApplyTokenAccountingRejectsFuturePersistedVersionEvenWithValidV2RawJSON(t *testing.T) {
	event := Event{
		Provider:          "openai",
		Model:             "gpt-5",
		AccountingVersion: TokenAccountingSchemaVersion + 1,
		AccountingValid:   true,
		InputTokens:       100,
		OutputTokens:      20,
		TotalTokens:       120,
		RawJSON: `{
			"accounting_version": 2,
			"accounting_valid": true,
			"token_breakdown": {
				"schema_version": 2,
				"quality": "complete",
				"total_tokens": 120,
				"input": {
					"total_tokens": 100,
					"uncached_tokens": 100,
					"cache_read_tokens": 0,
					"cache_write_tokens": 0
				},
				"output": {
					"total_tokens": 20,
					"non_reasoning_tokens": 20,
					"reasoning_tokens": 0
				},
				"unclassified_tokens": 0
			}
		}`,
	}

	ApplyTokenAccounting(&event, nil)

	if event.AccountingVersion != TokenAccountingSchemaVersion+1 || event.AccountingValid ||
		event.TokenBreakdown.Quality != TokenAccountingQualityInconsistent ||
		event.TokenBreakdown.UnclassifiedTokens != 120 || event.TokenBreakdown.TotalTokens != 120 ||
		event.TokenBreakdown.Input.TotalTokens != 0 || event.TokenBreakdown.Output.TotalTokens != 0 ||
		!event.TokenBreakdown.Valid() {
		t.Fatalf("future persisted accounting was repaired from v2 raw JSON: %+v", event)
	}
}

func TestRestorePersistedTokenAccountingRejectsFutureVersion(t *testing.T) {
	event := Event{
		AccountingVersion:                  TokenAccountingSchemaVersion + 1,
		AccountingValid:                    true,
		NormalizedUncachedInputTokens:      100,
		NormalizedTotalInputTokens:         100,
		NormalizedNonReasoningOutputTokens: 20,
		NormalizedTotalOutputTokens:        20,
		TotalTokens:                        120,
	}

	if RestorePersistedTokenAccounting(&event, TokenAccountingQualityComplete) {
		t.Fatal("future persisted accounting version was restored")
	}
	if event.TokenBreakdown != (TokenBreakdown{}) {
		t.Fatalf("future persisted accounting mutated breakdown: %+v", event.TokenBreakdown)
	}
}

func TestRestorePersistedTokenAccountingDoesNotTrustVersionZeroValidityFlag(t *testing.T) {
	event := Event{
		AccountingVersion:                  0,
		AccountingValid:                    true,
		NormalizedUncachedInputTokens:      100,
		NormalizedTotalInputTokens:         100,
		NormalizedNonReasoningOutputTokens: 20,
		NormalizedTotalOutputTokens:        20,
		TotalTokens:                        120,
	}

	if !RestorePersistedTokenAccounting(&event, TokenAccountingQualityComplete) {
		t.Fatal("valid version zero persisted breakdown was not restored")
	}
	if event.AccountingValid || event.TokenBreakdown.TotalTokens != 120 || !event.TokenBreakdown.Valid() {
		t.Fatalf("version zero persisted validity flag was trusted: %+v", event)
	}
}

func TestResolveTokenAccountingRejectsMissingOrFractionalCanonicalFields(t *testing.T) {
	validRecord := func() map[string]any {
		return map[string]any{
			"accounting_version": float64(2),
			"token_breakdown": map[string]any{
				"schema_version": float64(2),
				"quality":        "complete",
				"total_tokens":   float64(140),
				"input": map[string]any{
					"total_tokens":       float64(100),
					"uncached_tokens":    float64(100),
					"cache_read_tokens":  float64(0),
					"cache_write_tokens": float64(0),
				},
				"output": map[string]any{
					"total_tokens":         float64(40),
					"non_reasoning_tokens": float64(30),
					"reasoning_tokens":     float64(10),
				},
				"unclassified_tokens": float64(0),
			},
		}
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "missing zero-valued required field",
			mutate: func(record map[string]any) {
				delete(record["token_breakdown"].(map[string]any), "unclassified_tokens")
			},
		},
		{
			name: "fractional required field",
			mutate: func(record map[string]any) {
				output := record["token_breakdown"].(map[string]any)["output"].(map[string]any)
				output["non_reasoning_tokens"] = float64(30.5)
			},
		},
		{
			name: "non-boolean accounting validity",
			mutate: func(record map[string]any) {
				record["accounting_valid"] = "false"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := validRecord()
			tt.mutate(record)
			got := ResolveTokenAccounting(record, CacheInputContext{Provider: "openai"}, TokenAccountingInput{
				InputTokens: 100, OutputTokens: 40, ReasoningTokens: 10, TotalTokens: 140,
			})
			if got.SourceValid || got.ReportedVersion != 2 ||
				got.Breakdown.Quality != TokenAccountingQualityInconsistent ||
				got.Breakdown.TotalTokens != 140 || got.Breakdown.UnclassifiedTokens != 140 ||
				!got.Breakdown.Valid() {
				t.Fatalf("invalid canonical fallback = %+v", got)
			}
		})
	}
}

func TestResolveTokenAccountingTreatsIncompleteValidityMetadataAsCanonicalClaim(t *testing.T) {
	got := ResolveTokenAccounting(
		map[string]any{"accounting_valid": false},
		CacheInputContext{Provider: "openai"},
		TokenAccountingInput{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
	)
	if got.SourceValid || got.Breakdown.Quality != TokenAccountingQualityInconsistent ||
		got.Breakdown.UnclassifiedTokens != 120 || got.Breakdown.TotalTokens != 120 ||
		!got.Breakdown.Valid() {
		t.Fatalf("incomplete canonical metadata fallback = %+v", got)
	}
}

func TestResolveTokenAccountingRejectsBreakdownWithoutAccountingVersion(t *testing.T) {
	got := ResolveTokenAccounting(
		map[string]any{
			"token_breakdown": map[string]any{
				"schema_version":      float64(2),
				"quality":             "complete",
				"total_tokens":        float64(120),
				"unclassified_tokens": float64(0),
				"input": map[string]any{
					"total_tokens":       float64(100),
					"uncached_tokens":    float64(100),
					"cache_read_tokens":  float64(0),
					"cache_write_tokens": float64(0),
				},
				"output": map[string]any{
					"total_tokens":         float64(20),
					"non_reasoning_tokens": float64(20),
					"reasoning_tokens":     float64(0),
				},
			},
		},
		CacheInputContext{Provider: "openai"},
		TokenAccountingInput{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
	)
	if got.ReportedVersion != 0 || got.SourceValid ||
		got.Breakdown.Quality != TokenAccountingQualityInconsistent ||
		got.Breakdown.TotalTokens != 120 || got.Breakdown.UnclassifiedTokens != 120 ||
		!got.Breakdown.Valid() {
		t.Fatalf("missing-version canonical fallback = %+v", got)
	}
}

func TestResolveTokenAccountingTreatsNullBreakdownAsCanonicalClaim(t *testing.T) {
	got := ResolveTokenAccounting(
		map[string]any{"token_breakdown": nil},
		CacheInputContext{Provider: "openai"},
		TokenAccountingInput{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
	)
	if got.SourceValid || got.Breakdown.Quality != TokenAccountingQualityInconsistent ||
		got.Breakdown.TotalTokens != 120 || got.Breakdown.UnclassifiedTokens != 120 ||
		!got.Breakdown.Valid() {
		t.Fatalf("null canonical breakdown fallback = %+v", got)
	}
}

func TestResolveTokenAccountingRejectsFractionalAccountingVersion(t *testing.T) {
	record := map[string]any{
		"accounting_version": 2.5,
		"token_breakdown": map[string]any{
			"schema_version":      float64(2),
			"quality":             "complete",
			"total_tokens":        float64(140),
			"unclassified_tokens": float64(0),
			"input": map[string]any{
				"total_tokens":       float64(100),
				"uncached_tokens":    float64(100),
				"cache_read_tokens":  float64(0),
				"cache_write_tokens": float64(0),
			},
			"output": map[string]any{
				"total_tokens":         float64(40),
				"non_reasoning_tokens": float64(30),
				"reasoning_tokens":     float64(10),
			},
		},
	}
	got := ResolveTokenAccounting(record, CacheInputContext{Provider: "openai"}, TokenAccountingInput{
		InputTokens: 100, OutputTokens: 40, ReasoningTokens: 10, TotalTokens: 140,
	})
	if got.SourceValid || got.ReportedVersion != 0 ||
		got.Breakdown.Quality != TokenAccountingQualityInconsistent ||
		got.Breakdown.UnclassifiedTokens != 140 || !got.Breakdown.Valid() {
		t.Fatalf("fractional accounting version was accepted: %+v", got)
	}
}

func TestResolveTokenAccountingLegacyProviderSemantics(t *testing.T) {
	tests := []struct {
		name             string
		context          CacheInputContext
		input            TokenAccountingInput
		wantInput        int64
		wantOutput       int64
		wantNonReasoning int64
		wantReasoning    int64
		wantTotal        int64
	}{
		{
			name:    "openai reasoning is output subset",
			context: CacheInputContext{Provider: "openai"},
			input: TokenAccountingInput{
				InputTokens: 100, OutputTokens: 40, ReasoningTokens: 10, CacheReadTokens: 20,
			},
			wantInput: 100, wantOutput: 40, wantNonReasoning: 30, wantReasoning: 10, wantTotal: 140,
		},
		{
			name:    "claude cache and reasoning are independent",
			context: CacheInputContext{Provider: "anthropic"},
			input: TokenAccountingInput{
				InputTokens: 100, OutputTokens: 40, ReasoningTokens: 10, CacheReadTokens: 20, CacheCreationTokens: 5,
			},
			wantInput: 125, wantOutput: 50, wantNonReasoning: 40, wantReasoning: 10, wantTotal: 175,
		},
		{
			name:    "gemini reasoning is separate but cache is included",
			context: CacheInputContext{ExecutorType: "GeminiExecutor"},
			input: TokenAccountingInput{
				InputTokens: 100, OutputTokens: 40, ReasoningTokens: 10, CacheReadTokens: 20,
			},
			wantInput: 100, wantOutput: 50, wantNonReasoning: 40, wantReasoning: 10, wantTotal: 150,
		},
		{
			name:    "auth type identifies codex output subset semantics",
			context: CacheInputContext{AuthType: "codex"},
			input: TokenAccountingInput{
				InputTokens: 100, OutputTokens: 40, ReasoningTokens: 10,
			},
			wantInput: 100, wantOutput: 40, wantNonReasoning: 30, wantReasoning: 10, wantTotal: 140,
		},
		{
			name:    "openrouter cache and reasoning are subsets",
			context: CacheInputContext{Provider: "openrouter"},
			input: TokenAccountingInput{
				InputTokens: 100, OutputTokens: 40, ReasoningTokens: 10, CacheReadTokens: 20,
			},
			wantInput: 100, wantOutput: 40, wantNonReasoning: 30, wantReasoning: 10, wantTotal: 140,
		},
		{
			name:    "openai compatible prefix beats anthropic suffix",
			context: CacheInputContext{Provider: "openai-compatible-anthropic"},
			input: TokenAccountingInput{
				InputTokens: 100, OutputTokens: 40, ReasoningTokens: 10, CacheReadTokens: 20,
			},
			wantInput: 100, wantOutput: 40, wantNonReasoning: 30, wantReasoning: 10, wantTotal: 140,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveTokenAccounting(nil, tt.context, tt.input)
			if got.SourceValid || !got.Breakdown.Valid() {
				t.Fatalf("legacy accounting = %+v", got)
			}
			if got.Breakdown.Input.TotalTokens != tt.wantInput ||
				got.Breakdown.Output.TotalTokens != tt.wantOutput ||
				got.Breakdown.Output.NonReasoningTokens != tt.wantNonReasoning ||
				got.Breakdown.Output.ReasoningTokens != tt.wantReasoning ||
				got.Breakdown.TotalTokens != tt.wantTotal {
				t.Fatalf("breakdown = %+v", got.Breakdown)
			}
		})
	}
}

func TestBuildDetailPreservesPersistedCanonicalBreakdownProvenance(t *testing.T) {
	breakdown := TokenBreakdown{
		SchemaVersion: TokenAccountingSchemaVersion,
		Quality:       TokenAccountingQualityComplete,
		TotalTokens:   140,
		Input: TokenInputBreakdown{
			TotalTokens:      100,
			UncachedTokens:   70,
			CacheReadTokens:  20,
			CacheWriteTokens: 10,
		},
		Output: TokenOutputBreakdown{
			TotalTokens:        40,
			NonReasoningTokens: 30,
			ReasoningTokens:    10,
		},
	}
	event := Event{
		Provider:          "unknown",
		AccountingVersion: 0,
		AccountingValid:   false,
		TokenBreakdown:    breakdown,
		InputTokens:       999,
		OutputTokens:      888,
		ReasoningTokens:   777,
		TotalTokens:       2_664,
	}

	detail := BuildDetail(event)
	if detail.AccountingVersion != 0 || detail.AccountingValid || detail.TokenBreakdown != breakdown ||
		detail.Tokens.TotalTokens != 140 || detail.Tokens.InputTokens != 999 ||
		detail.Tokens.NonReasoningOutputTokens != 30 {
		t.Fatalf("detail accounting = %+v", detail)
	}
}

func TestBuildDetailPreservesAccountingValidFalseFromRawCanonicalFallback(t *testing.T) {
	event := Event{
		Provider:        "openai",
		InputTokens:     999,
		OutputTokens:    888,
		ReasoningTokens: 777,
		TotalTokens:     2_664,
		RawJSON: `{
			"accounting_version":2,
			"accounting_valid":false,
			"token_breakdown":{
				"schema_version":2,
				"quality":"complete",
				"total_tokens":140,
				"input":{"total_tokens":100,"uncached_tokens":70,"cache_read_tokens":20,"cache_write_tokens":10},
				"output":{"total_tokens":40,"non_reasoning_tokens":30,"reasoning_tokens":10},
				"unclassified_tokens":0
			}
		}`,
	}

	detail := BuildDetail(event)
	if detail.AccountingVersion != 2 || detail.AccountingValid ||
		detail.TokenBreakdown.TotalTokens != 140 || detail.Tokens.TotalTokens != 140 {
		t.Fatalf("raw fallback accounting = %+v", detail)
	}
}

func TestBuildDetailRejectsIsolatedRawCanonicalMarker(t *testing.T) {
	event := Event{
		Provider:     "openai",
		InputTokens:  100,
		OutputTokens: 20,
		TotalTokens:  120,
		RawJSON:      `{"accounting_valid":false}`,
	}

	detail := BuildDetail(event)
	if detail.AccountingVersion != 0 || detail.AccountingValid ||
		detail.AccountingQuality != TokenAccountingQualityInconsistent ||
		detail.TokenBreakdown.TotalTokens != 120 || detail.TokenBreakdown.UnclassifiedTokens != 120 ||
		!detail.IncompleteAccounting {
		t.Fatalf("isolated raw canonical marker accounting = %+v", detail)
	}
}

func TestTokenAccountingRecordFromRawJSONLimitsNestedProvenanceDepth(t *testing.T) {
	canonical := `{
		"accounting_version":2,
		"token_breakdown":{
			"schema_version":2,"quality":"complete","total_tokens":1,
			"input":{"total_tokens":1,"uncached_tokens":1,"cache_read_tokens":0,"cache_write_tokens":0},
			"output":{"total_tokens":0,"non_reasoning_tokens":0,"reasoning_tokens":0},
			"unclassified_tokens":0
		}
	}`
	wrap := func(raw string, count int) string {
		t.Helper()
		for range count {
			encoded, err := json.Marshal(map[string]string{"raw_json": raw})
			if err != nil {
				t.Fatalf("marshal nested raw json: %v", err)
			}
			raw = string(encoded)
		}
		return raw
	}

	if _, ok := tokenAccountingRecordFromRawJSON(wrap(canonical, maxRawAccountingJSONDepth)); !ok {
		t.Fatal("canonical accounting within the depth limit was rejected")
	}
	if _, ok := tokenAccountingRecordFromRawJSON(wrap(canonical, maxRawAccountingJSONDepth+1)); ok {
		t.Fatal("canonical accounting beyond the depth limit was accepted")
	}
}

func TestBuildDetailKeepsHistoricalMalformedRawTotalInconsistent(t *testing.T) {
	event := Event{
		Provider:     "openai",
		InputTokens:  100,
		OutputTokens: 20,
		TotalTokens:  120,
		RawJSON:      `{"tokens":{"input_tokens":100,"output_tokens":20,"total_tokens":1.5}}`,
	}
	detail := BuildDetail(event)
	if detail.AccountingQuality != TokenAccountingQualityInconsistent ||
		detail.TokenBreakdown.TotalTokens != 120 || detail.TokenBreakdown.UnclassifiedTokens != 120 ||
		!detail.IncompleteAccounting {
		t.Fatalf("historical malformed raw total accounting = %+v", detail)
	}
}

func TestResolveTokenAccountingNegativeLegacyFieldPreservesKnownLowerBound(t *testing.T) {
	got := ResolveTokenAccounting(nil, CacheInputContext{Provider: "openai"}, TokenAccountingInput{
		InputTokens: 100, OutputTokens: -1,
	})
	if got.Breakdown.Quality != TokenAccountingQualityInconsistent ||
		got.Breakdown.TotalTokens != 100 || got.Breakdown.UnclassifiedTokens != 100 ||
		!got.Breakdown.Valid() {
		t.Fatalf("negative legacy fallback = %+v", got.Breakdown)
	}
}

func TestResolveTokenAccountingSaturatesOverflowingLegacyLowerBounds(t *testing.T) {
	tests := []struct {
		name        string
		context     CacheInputContext
		input       TokenAccountingInput
		wantQuality string
	}{
		{
			name:        "known semantics",
			context:     CacheInputContext{Provider: "openai"},
			input:       TokenAccountingInput{InputTokens: math.MaxInt64, OutputTokens: 1},
			wantQuality: TokenAccountingQualityInconsistent,
		},
		{
			name:        "unknown semantics",
			input:       TokenAccountingInput{InputTokens: math.MaxInt64, OutputTokens: 1},
			wantQuality: TokenAccountingQualityUnclassified,
		},
		{
			name:    "separate cache semantics",
			context: CacheInputContext{Provider: "anthropic"},
			input: TokenAccountingInput{
				InputTokens:     math.MaxInt64,
				CacheReadTokens: 1,
			},
			wantQuality: TokenAccountingQualityInconsistent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveTokenAccounting(nil, tt.context, tt.input)
			if got.Breakdown.Quality != tt.wantQuality ||
				got.Breakdown.TotalTokens != math.MaxInt64 ||
				got.Breakdown.UnclassifiedTokens != math.MaxInt64 ||
				!got.Breakdown.Valid() {
				t.Fatalf("overflow fallback = %+v", got.Breakdown)
			}
		})
	}
}

func TestResolveTokenAccountingLeavesUnknownLegacyTokensUnclassified(t *testing.T) {
	got := ResolveTokenAccounting(nil, CacheInputContext{}, TokenAccountingInput{
		InputTokens: 100, OutputTokens: 40, ReasoningTokens: 10, TotalTokens: 175,
	})
	if got.Breakdown.Quality != TokenAccountingQualityUnclassified || got.Breakdown.UnclassifiedTokens != 175 || got.Breakdown.TotalTokens != 175 {
		t.Fatalf("breakdown = %+v", got.Breakdown)
	}
	if got.Breakdown.Input.TotalTokens != 0 || got.Breakdown.Output.TotalTokens != 0 || !got.Breakdown.Valid() {
		t.Fatalf("unknown buckets should not be guessed: %+v", got.Breakdown)
	}
}

func TestResolveTokenAccountingPreservesAuthoritativeRemainder(t *testing.T) {
	got := ResolveTokenAccounting(nil, CacheInputContext{Provider: "openai"}, TokenAccountingInput{
		InputTokens: 100, OutputTokens: 40, ReasoningTokens: 10, TotalTokens: 155,
	})
	if got.Breakdown.Quality != TokenAccountingQualityUnclassified || got.Breakdown.UnclassifiedTokens != 15 || got.Breakdown.TotalTokens != 155 || !got.Breakdown.Valid() {
		t.Fatalf("breakdown = %+v", got.Breakdown)
	}
}

func TestResolveTokenAccountingRejectsIncludedCacheBucketsLargerThanInput(t *testing.T) {
	got := ResolveTokenAccounting(nil, CacheInputContext{Provider: "openai"}, TokenAccountingInput{
		InputTokens: 1, OutputTokens: 2, CachedTokens: 5, TotalTokens: 3,
	})
	if got.Breakdown.Quality != TokenAccountingQualityInconsistent ||
		got.Breakdown.UnclassifiedTokens != 7 || got.Breakdown.TotalTokens != 7 ||
		got.Breakdown.Input.TotalTokens != 0 || got.Breakdown.Output.TotalTokens != 0 ||
		!got.Breakdown.Valid() {
		t.Fatalf("breakdown = %+v", got.Breakdown)
	}
}

func TestResolveTokenAccountingUnknownCacheUsesFullConservativeLowerBound(t *testing.T) {
	got := ResolveTokenAccounting(nil, CacheInputContext{}, TokenAccountingInput{
		InputTokens: 10, OutputTokens: 20, ReasoningTokens: 3,
		CachedTokens: 10, CacheReadTokens: 4, CacheCreationTokens: 1,
	})
	if got.Breakdown.Quality != TokenAccountingQualityUnclassified ||
		got.Breakdown.UnclassifiedTokens != 40 || got.Breakdown.TotalTokens != 40 ||
		!got.Breakdown.Valid() {
		t.Fatalf("breakdown = %+v", got.Breakdown)
	}
}

func TestNormalizeRawUsesCanonicalV2WithoutChangingLegacyHashInputs(t *testing.T) {
	raw := []byte(`{
		"request_id":"req-1",
		"timestamp":"2026-07-15T00:00:00Z",
		"provider":"openai",
		"model":"gpt-5",
		"accounting_version":2,
		"tokens":{"input_tokens":100,"output_tokens":40,"reasoning_tokens":10,"total_tokens":150},
		"token_breakdown":{
			"schema_version":2,"quality":"complete","total_tokens":140,
			"input":{"total_tokens":100,"uncached_tokens":100,"cache_read_tokens":0,"cache_write_tokens":0},
			"output":{"total_tokens":40,"non_reasoning_tokens":30,"reasoning_tokens":10},
			"unclassified_tokens":0
		}
	}`)
	event, err := NormalizeRaw(raw)
	if err != nil {
		t.Fatalf("normalize raw: %v", err)
	}
	if !event.AccountingValid || event.AccountingVersion != 2 || event.TotalTokens != 140 || event.NormalizedTotalOutputTokens != 40 {
		t.Fatalf("event accounting = %+v", event)
	}
	legacy := event
	legacy.AccountingVersion = 0
	legacy.AccountingValid = false
	legacy.TokenBreakdown = TokenBreakdown{}
	legacy.TotalTokens = 150
	if buildEventHash(event) != buildEventHash(legacy) {
		t.Fatal("canonical accounting changed the legacy event identity hash")
	}
}

func TestNormalizeRawPreservesCanonicalIntegersAboveFloatPrecision(t *testing.T) {
	const exact = int64(9_007_199_254_740_993)
	raw := []byte(`{
		"request_id":"req-large-token-count",
		"timestamp":"2026-07-15T00:00:00Z",
		"provider":"openai",
		"model":"gpt-5",
		"accounting_version":2,
		"tokens":{"input_tokens":9007199254740993,"output_tokens":0,"total_tokens":9007199254740993},
		"token_breakdown":{
			"schema_version":2,"quality":"complete","total_tokens":9007199254740993,
			"input":{"total_tokens":9007199254740993,"uncached_tokens":9007199254740993,"cache_read_tokens":0,"cache_write_tokens":0},
			"output":{"total_tokens":0,"non_reasoning_tokens":0,"reasoning_tokens":0},
			"unclassified_tokens":0
		}
	}`)

	event, err := NormalizeRaw(raw)
	if err != nil {
		t.Fatalf("normalize raw: %v", err)
	}
	if !event.AccountingValid || event.TotalTokens != exact ||
		event.TokenBreakdown.Input.TotalTokens != exact ||
		event.InputTokens != exact {
		t.Fatalf("large canonical integer lost precision: %+v", event)
	}
}

func TestBuildDetailAddsAccountingV2WithoutRemovingLegacyTokens(t *testing.T) {
	event := Event{
		Provider:            "anthropic",
		Timestamp:           "2026-07-15T00:00:00Z",
		Model:               "claude-sonnet",
		InputTokens:         100,
		OutputTokens:        40,
		ReasoningTokens:     10,
		CacheReadTokens:     20,
		CacheCreationTokens: 5,
	}
	detail := BuildDetail(event)
	if !detail.TokenBreakdown.Valid() || detail.AccountingQuality != TokenAccountingQualityComplete || detail.IncompleteAccounting {
		t.Fatalf("detail accounting = %+v", detail)
	}
	if detail.Tokens.InputTokens != 100 || detail.Tokens.OutputTokens != 40 || detail.Tokens.ReasoningTokens != 10 {
		t.Fatalf("legacy tokens changed = %+v", detail.Tokens)
	}
	if detail.TokenBreakdown.Input.TotalTokens != 125 || detail.TokenBreakdown.Output.TotalTokens != 50 ||
		detail.Tokens.NonReasoningOutputTokens != 40 || detail.Tokens.TotalTokens != 175 {
		t.Fatalf("canonical tokens = %+v breakdown=%+v", detail.Tokens, detail.TokenBreakdown)
	}

	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if _, ok := payload["token_breakdown"]; !ok {
		t.Fatalf("payload missing token_breakdown: %s", encoded)
	}
	if _, ok := payload["accounting_version"]; !ok {
		t.Fatalf("payload missing accounting_version: %s", encoded)
	}
}
