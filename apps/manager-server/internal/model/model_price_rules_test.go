package model

import "testing"

func TestNormalizeModelPriceContextTiersSortsAndRejectsDuplicates(t *testing.T) {
	tiers, err := NormalizeModelPriceContextTiers([]ModelPriceContextTier{
		{ThresholdTokens: 200_000, Prompt: 4, PromptConfigured: true},
		{ThresholdTokens: 32_000, Prompt: 2, PromptConfigured: true},
	})
	if err != nil {
		t.Fatalf("normalize tiers: %v", err)
	}
	if len(tiers) != 2 || tiers[0].ThresholdTokens != 32_000 || tiers[1].ThresholdTokens != 200_000 {
		t.Fatalf("normalized tiers = %#v", tiers)
	}
	if _, err := NormalizeModelPriceContextTiers([]ModelPriceContextTier{
		{ThresholdTokens: 32_000, Prompt: 2, PromptConfigured: true},
		{ThresholdTokens: 32_000, Prompt: 3, PromptConfigured: true},
	}); err == nil {
		t.Fatal("duplicate threshold error = nil")
	}
}

func TestModelPriceForContextUsesStrictHighestTierAndInheritsMissingRates(t *testing.T) {
	price := ModelPrice{
		Prompt: 1, Completion: 2, Cache: 0.1, CacheRead: 0.1, CacheCreation: 0.2,
		PromptConfigured: true, CompletionConfigured: true, CacheReadConfigured: true, CacheCreationConfigured: true,
		ContextTiers: []ModelPriceContextTier{
			{ThresholdTokens: 32_000, Prompt: 3, Completion: 4, PromptConfigured: true, CompletionConfigured: true},
			{ThresholdTokens: 200_000, Prompt: 0, Completion: 8, PromptConfigured: true, CompletionConfigured: true},
		},
	}

	atThreshold, band := ModelPriceForContext(price, 32_000)
	if band != ModelPriceBaseContextThreshold || atThreshold.Prompt != 1 {
		t.Fatalf("exact threshold price = %#v, band = %d", atThreshold, band)
	}
	firstTier, band := ModelPriceForContext(price, 200_000)
	if band != 32_000 || firstTier.Prompt != 3 || firstTier.CacheRead != 0.1 {
		t.Fatalf("first tier price = %#v, band = %d", firstTier, band)
	}
	highestTier, band := ModelPriceForContext(price, 200_001)
	if band != 200_000 || highestTier.Prompt != 0 || !highestTier.PromptConfigured || highestTier.CacheCreation != 0.2 {
		t.Fatalf("highest tier price = %#v, band = %d", highestTier, band)
	}
	resolvedTier, ok := ModelPriceForContextThreshold(price, 32_000)
	if !ok || resolvedTier.Prompt != 3 || resolvedTier.CacheRead != 0.1 {
		t.Fatalf("resolved tier price = %#v, ok = %v", resolvedTier, ok)
	}
	if _, ok := ModelPriceForContextThreshold(price, 128_000); ok {
		t.Fatal("unknown threshold unexpectedly resolved")
	}
}

func TestModelPriceServiceTiersNormalizeMatchAliasesAndInheritRates(t *testing.T) {
	tiers, err := NormalizeModelPriceServiceTiers([]ModelPriceServiceTier{
		{
			Mode: " FAST ", ServiceTier: " Priority ", Prompt: 0, Completion: 75,
			PromptConfigured: true, CompletionConfigured: true,
		},
	})
	if err != nil {
		t.Fatalf("normalize service tiers: %v", err)
	}
	if len(tiers) != 1 || tiers[0].Mode != "fast" || tiers[0].ServiceTier != "priority" {
		t.Fatalf("normalized service tiers = %#v", tiers)
	}
	price := ModelPrice{
		Prompt: 5, Completion: 30, Cache: 0.5, CacheRead: 0.5, CacheCreation: 6.25,
		PromptConfigured: true, CompletionConfigured: true, CacheReadConfigured: true, CacheCreationConfigured: true,
		ContextTiers: []ModelPriceContextTier{{ThresholdTokens: 272_000, Prompt: 10, PromptConfigured: true}},
		ServiceTiers: tiers,
	}
	for _, identifier := range []string{"fast", "priority", " PRIORITY "} {
		effective, ok := ModelPriceForServiceTier(price, identifier)
		if !ok || effective.Prompt != 0 || !effective.PromptConfigured || effective.Completion != 75 ||
			effective.CacheRead != 0.5 || len(effective.ContextTiers) != 0 || len(effective.ServiceTiers) != 0 {
			t.Fatalf("effective service tier for %q = %#v, ok = %v", identifier, effective, ok)
		}
	}
	if _, ok := ModelPriceForServiceTier(price, "default"); ok {
		t.Fatal("default unexpectedly matched service tier rule")
	}
}

func TestNormalizeModelPriceServiceTiersRejectsAmbiguousAndEmptyRules(t *testing.T) {
	for name, tiers := range map[string][]ModelPriceServiceTier{
		"shared identifier": {
			{Mode: "fast", ServiceTier: "priority", Prompt: 1, PromptConfigured: true},
			{Mode: "priority", ServiceTier: "turbo", Prompt: 2, PromptConfigured: true},
		},
		"missing identifier": {
			{Mode: "fast", Prompt: 1, PromptConfigured: true},
		},
		"no prices": {
			{Mode: "fast", ServiceTier: "priority"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeModelPriceServiceTiers(tiers); err == nil {
				t.Fatal("normalize error = nil")
			}
		})
	}
}

func TestModelPriceStructureRevisionIgnoresRatesButTracksThresholds(t *testing.T) {
	base := map[string]ModelPrice{
		"model-b": {ContextTiers: []ModelPriceContextTier{{ThresholdTokens: 200_000, Prompt: 2, PromptConfigured: true}}},
		"model-a": {Prompt: 1},
	}
	revision := ModelPriceStructureRevision(base)
	rateUpdate := map[string]ModelPrice{
		"model-a": {Prompt: 99},
		"model-b": {ContextTiers: []ModelPriceContextTier{{ThresholdTokens: 200_000, Prompt: 9, PromptConfigured: true}}},
	}
	if got := ModelPriceStructureRevision(rateUpdate); got != revision {
		t.Fatalf("rate-only revision = %q, want %q", got, revision)
	}
	serviceTierUpdate := map[string]ModelPrice{
		"model-a": {
			Prompt: 1,
			ServiceTiers: []ModelPriceServiceTier{{
				Mode: "fast", ServiceTier: "priority", Prompt: 9, PromptConfigured: true,
			}},
		},
		"model-b": {ContextTiers: []ModelPriceContextTier{{ThresholdTokens: 200_000, Prompt: 2, PromptConfigured: true}}},
	}
	if got := ModelPriceStructureRevision(serviceTierUpdate); got != revision {
		t.Fatalf("service-tier revision = %q, want %q", got, revision)
	}
	modelSetUpdate := map[string]ModelPrice{
		"model-b": {ContextTiers: []ModelPriceContextTier{{ThresholdTokens: 200_000, Prompt: 2, PromptConfigured: true}}},
	}
	if got := ModelPriceStructureRevision(modelSetUpdate); got == revision {
		t.Fatalf("model-set revision did not change: %q", got)
	}
	thresholdUpdate := map[string]ModelPrice{
		"model-b": {ContextTiers: []ModelPriceContextTier{{ThresholdTokens: 256_000, Prompt: 2, PromptConfigured: true}}},
	}
	if got := ModelPriceStructureRevision(thresholdUpdate); got == revision {
		t.Fatalf("threshold revision did not change: %q", got)
	}
}
