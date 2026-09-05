package codexquota

import (
	"reflect"
	"testing"
)

func TestResolveAdditionalScope(t *testing.T) {
	tests := []struct {
		name           string
		meteredFeature string
		limitName      string
		anonymous      string
		wantScope      ModelScope
		wantPrefix     string
		wantDisplay    string
		wantLegacy     string
	}{
		{
			name:           "provider feature identifies spark",
			meteredFeature: "codex_spark",
			limitName:      "Fast coding",
			wantScope:      SparkScope(),
			wantPrefix:     SparkProviderWindowPrefix,
		},
		{
			name:       "verified model name identifies spark",
			limitName:  "GPT-5.3-Codex-Spark",
			wantScope:  SparkScope(),
			wantPrefix: SparkProviderWindowPrefix,
		},
		{
			name:           "unknown feature fails closed",
			meteredFeature: "future_feature",
			limitName:      "Future Feature",
			wantScope:      FeatureScope("future_feature"),
			wantPrefix:     "future-feature",
			wantDisplay:    "Future Feature",
		},
		{
			name:           "metered feature supplies display name",
			meteredFeature: "future_feature",
			wantScope:      FeatureScope("future_feature"),
			wantPrefix:     "future-feature",
			wantDisplay:    "future_feature",
		},
		{
			name:           "provider feature wins over conflicting spark label",
			meteredFeature: "future_feature",
			limitName:      "Spark",
			wantScope:      FeatureScope("future_feature"),
			wantPrefix:     "future-feature",
			wantDisplay:    "Spark",
			wantLegacy:     "spark",
		},
		{
			name:       "anonymous feature uses structural identity",
			anonymous:  "additional-p-18000-s-604800",
			wantScope:  FeatureScope("additional_p_18000_s_604800"),
			wantPrefix: "additional-p-18000-s-604800",
		},
		{
			name:        "non ascii label uses structural identity consistently",
			limitName:   "未来额度",
			anonymous:   "additional-p-18000-s-604800",
			wantScope:   FeatureScope("additional_p_18000_s_604800"),
			wantPrefix:  "additional-p-18000-s-604800",
			wantDisplay: "未来额度",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ResolveAdditionalScope(AdditionalScopeInput{
				MeteredFeature:    test.meteredFeature,
				LimitName:         test.limitName,
				AnonymousIdentity: test.anonymous,
			})
			if !reflect.DeepEqual(got.Scope, test.wantScope) || got.ProviderWindowPrefix != test.wantPrefix || got.ScopeDisplayName != test.wantDisplay {
				t.Fatalf("ResolveAdditionalScope() = %#v, want scope=%#v prefix=%q display=%q", got, test.wantScope, test.wantPrefix, test.wantDisplay)
			}
			if test.wantLegacy != "" && !containsScopeString(got.LegacyPrefixes, test.wantLegacy) {
				t.Fatalf("ResolveAdditionalScope() legacy prefixes = %#v, want %q", got.LegacyPrefixes, test.wantLegacy)
			}
		})
	}
}

func TestResolveUsageScopeUsesRequestAndResolvedIdentity(t *testing.T) {
	tests := []struct {
		name           string
		model          string
		analyticsModel string
		requestedModel string
		resolvedModel  string
		want           UsageScopeResolution
	}{
		{
			name:  "direct spark",
			model: SparkModelID,
			want:  UsageScopeResolution{Scope: SparkScope(), ProviderWindowPrefix: SparkProviderWindowPrefix},
		},
		{
			name:           "alias resolves to spark",
			model:          "my-spark",
			analyticsModel: "my-spark",
			requestedModel: "my-spark",
			resolvedModel:  SparkModelID,
			want:           UsageScopeResolution{Scope: SparkScope(), ProviderWindowPrefix: SparkProviderWindowPrefix},
		},
		{
			name:           "ordinary alias stays main",
			model:          "my-codex",
			analyticsModel: "my-codex",
			requestedModel: "my-codex",
			resolvedModel:  "gpt-5.6-sol",
			want:           UsageScopeResolution{Scope: MainScope()},
		},
		{
			name:           "resolved main wins over spark-shaped request alias",
			model:          SparkModelID,
			analyticsModel: SparkModelID,
			requestedModel: SparkModelID,
			resolvedModel:  "gpt-5.6-sol",
			want:           UsageScopeResolution{Scope: MainScope()},
		},
		{
			name: "missing identity fails closed",
			want: UsageScopeResolution{Scope: UnknownRequestScope(), ProviderWindowPrefix: "request-scope-unknown"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ResolveUsageScope(test.model, test.analyticsModel, test.requestedModel, test.resolvedModel)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ResolveUsageScope() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestResolveAdditionalScopeKeepsStablePrefixAcrossDisplayRename(t *testing.T) {
	oldName := ResolveAdditionalScope(AdditionalScopeInput{
		MeteredFeature: "future_feature",
		LimitName:      "Old Name",
	})
	newName := ResolveAdditionalScope(AdditionalScopeInput{
		MeteredFeature: "future_feature",
		LimitName:      "New Name",
	})
	if oldName.ProviderWindowPrefix != "future-feature" || newName.ProviderWindowPrefix != "future-feature" {
		t.Fatalf("stable provider prefixes = %q/%q, want future-feature", oldName.ProviderWindowPrefix, newName.ProviderWindowPrefix)
	}
	if oldName.ScopeDisplayName != "Old Name" || newName.ScopeDisplayName != "New Name" {
		t.Fatalf("display names = %q/%q", oldName.ScopeDisplayName, newName.ScopeDisplayName)
	}
	if !containsScopeString(oldName.LegacyPrefixes, "old-name") || !containsScopeString(newName.LegacyPrefixes, "new-name") {
		t.Fatalf("rename aliases = old:%#v new:%#v", oldName.LegacyPrefixes, newName.LegacyPrefixes)
	}
	if containsScopeString(oldName.LegacyPrefixes, "future-feature") || containsScopeString(newName.LegacyPrefixes, "future-feature") {
		t.Fatalf("stable feature was returned as a legacy alias: old:%#v new:%#v", oldName.LegacyPrefixes, newName.LegacyPrefixes)
	}
	meteredOnly := ResolveAdditionalScope(AdditionalScopeInput{MeteredFeature: "future_feature"})
	if len(meteredOnly.LegacyPrefixes) != 0 {
		t.Fatalf("metered-only legacy prefixes = %#v, want none", meteredOnly.LegacyPrefixes)
	}
}

func TestMatchMainUsageExcludesSparkOnEitherIdentitySide(t *testing.T) {
	tests := []struct {
		name          string
		requestModel  string
		billingModel  string
		wantMatched   bool
		wantUnmatched bool
	}{
		{name: "ordinary request", requestModel: "my-codex", billingModel: "gpt-5.6-sol", wantMatched: true},
		{name: "direct spark request", requestModel: SparkModelID, billingModel: SparkModelID},
		{name: "alias resolved to spark", requestModel: "my-spark", billingModel: SparkModelID},
		{name: "spark-shaped request resolved to main", requestModel: SparkModelID, billingModel: "gpt-5.6-sol", wantMatched: true},
		{name: "missing identity", wantUnmatched: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matched, unmatched := MatchMainUsage(test.requestModel, test.billingModel)
			if matched != test.wantMatched || unmatched != test.wantUnmatched {
				t.Fatalf("MatchMainUsage() = (%v, %v), want (%v, %v)", matched, unmatched, test.wantMatched, test.wantUnmatched)
			}
		})
	}
}

func TestCanonicalProviderWindowIDNormalizesLegacySparkAliases(t *testing.T) {
	for input, want := range map[string]string{
		"GPT-5-3-Codex-Spark-Weekly-0": "spark-weekly-0",
		"codex-spark-five-hour-0":      "spark-five-hour-0",
		"spark-monthly-0":              "spark-monthly-0",
		"weekly":                       "weekly",
	} {
		if got := CanonicalProviderWindowID(input); got != want {
			t.Errorf("CanonicalProviderWindowID(%q) = %q, want %q", input, got, want)
		}
	}
	if got := CanonicalProviderWindowID("primary", "five_hour"); got != "five-hour" {
		t.Fatalf("primary alias canonicalized to %q, want five-hour", got)
	}
	if got := CanonicalProviderWindowID("secondary", "weekly"); got != "weekly" {
		t.Fatalf("weekly secondary alias canonicalized to %q, want weekly", got)
	}
	if got := CanonicalProviderWindowID("secondary", "monthly"); got != "monthly" {
		t.Fatalf("monthly secondary alias canonicalized to %q, want monthly", got)
	}
}

func TestAmbiguousProviderWindowNamespaceIsReservedWithoutNormalizingAway(t *testing.T) {
	const syntheticID = "cpamp:ambiguous:future-feature-weekly-0"
	if AmbiguousProviderWindowPrefix != "cpamp:ambiguous:" {
		t.Fatalf("ambiguous provider prefix = %q", AmbiguousProviderWindowPrefix)
	}
	if !IsAmbiguousAdditionalProviderWindowID(syntheticID) {
		t.Fatalf("synthetic id was not recognized: %q", syntheticID)
	}
	if got := CanonicalProviderWindowID(syntheticID, "weekly"); got != syntheticID {
		t.Fatalf("synthetic id canonicalized to %q, want %q", got, syntheticID)
	}
	for _, providerID := range []string{
		"ambiguous-feature-weekly-0",
		"ambiguous-quota-weekly-0",
		"ambiguous-feature-0-window-1d-0",
	} {
		if IsAmbiguousAdditionalProviderWindowID(providerID) {
			t.Fatalf("legitimate provider id was treated as synthetic: %q", providerID)
		}
	}
}

func TestResolveAdditionalScopeKeepsLegitimateAmbiguousNamesIdentifiable(t *testing.T) {
	for _, test := range []struct {
		name           string
		meteredFeature string
		limitName      string
		wantPrefix     string
		wantDisplay    string
	}{
		{name: "metered feature", meteredFeature: "ambiguous_feature", limitName: "My quota", wantPrefix: "ambiguous-feature", wantDisplay: "My quota"},
		{name: "limit name fallback", limitName: "Ambiguous Quota", wantPrefix: "ambiguous-quota", wantDisplay: "Ambiguous Quota"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := ResolveAdditionalScope(AdditionalScopeInput{
				MeteredFeature:    test.meteredFeature,
				LimitName:         test.limitName,
				AnonymousIdentity: "additional-p-18000-s-none",
			})
			if got.ProviderWindowPrefix != test.wantPrefix || got.ScopeDisplayName != test.wantDisplay || got.Scope.Kind != "feature" {
				t.Fatalf("legitimate ambiguous scope = %#v", got)
			}
			if IsAmbiguousAdditionalProviderWindowID(got.ProviderWindowPrefix + "-weekly-0") {
				t.Fatalf("legitimate ambiguous provider prefix was marked synthetic: %#v", got)
			}
		})
	}
}

func TestResolveProviderWindowScopeFailsClosedForLegacyCodexScopedIDs(t *testing.T) {
	tests := []struct {
		name             string
		providerWindowID string
		windowKind       string
		want             ModelScope
	}{
		{name: "spark", providerWindowID: "fast-coding-weekly-0", windowKind: "weekly", want: SparkScope()},
		{name: "code review", providerWindowID: "code-review-weekly-0", windowKind: "weekly", want: CodeReviewScope()},
		{name: "unknown feature fails closed", providerWindowID: "future-feature-weekly-0", windowKind: "weekly", want: FeatureScope("future_feature")},
		{name: "main primary", providerWindowID: "primary", windowKind: "five_hour", want: ModelScope{Kind: "all", Models: []string{}, Complete: true}},
		{name: "main monthly secondary", providerWindowID: "secondary", windowKind: "monthly", want: ModelScope{Kind: "all", Models: []string{}, Complete: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ResolveProviderWindowScope(test.providerWindowID, test.windowKind, ModelScope{Kind: "all", Complete: true})
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ResolveProviderWindowScope() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestCanonicalProviderWindowIDForScopeCollapsesLegacySparkIdentity(t *testing.T) {
	if got := CanonicalProviderWindowIDForScope("fast-coding-weekly-0", "weekly", SparkScope()); got != "spark-weekly-0" {
		t.Fatalf("legacy Spark identity = %q, want spark-weekly-0", got)
	}
	if got := CanonicalProviderWindowID("fast-coding-weekly-0"); got != "fast-coding-weekly-0" {
		t.Fatalf("raw legacy lifecycle id = %q, want unchanged", got)
	}
}

func TestInferScopeFromLegacyFastCodingWindowID(t *testing.T) {
	if got := InferScopeFromProviderWindowID("fast-coding-weekly-0"); !reflect.DeepEqual(got, SparkScope()) {
		t.Fatalf("InferScopeFromProviderWindowID() = %#v, want Spark scope", got)
	}
}

func TestProviderWindowAliasesIncludeLegacySparkLabelWithoutCanonicalizingIt(t *testing.T) {
	aliases := ProviderWindowAliases("spark-weekly-0", SparkScope())
	want := "fast-coding-weekly-0"
	if !containsScopeString(aliases, want) {
		t.Fatalf("Spark provider aliases = %#v, want %q", aliases, want)
	}
	if got := CanonicalProviderWindowID(want); got != want {
		t.Fatalf("legacy Spark alias was canonicalized to %q; lifecycle needs the raw alias", got)
	}
}

func TestProviderWindowAliasesIncludeLegacyCodexMainNames(t *testing.T) {
	tests := map[string]string{
		"five-hour": "primary",
		"weekly":    "secondary",
		"monthly":   "secondary",
	}
	for providerWindowID, want := range tests {
		aliases := ProviderWindowAliases(providerWindowID, MainScope())
		if !containsScopeString(aliases, want) {
			t.Fatalf("main provider aliases for %q = %#v, want %q", providerWindowID, aliases, want)
		}
	}
}

func containsScopeString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestLegacyAllScopeReplacementUsesKnownQuotaIdentityNotUsageCompleteness(t *testing.T) {
	tests := []struct {
		name             string
		providerWindowID string
		scope            ModelScope
		want             bool
	}{
		{name: "spark model scope", providerWindowID: "spark-weekly-0", scope: SparkScope(), want: true},
		{name: "incomplete code review feature", providerWindowID: "code-review-weekly-0", scope: CodeReviewScope(), want: true},
		{name: "incomplete unknown feature", providerWindowID: "future-feature-weekly-0", scope: FeatureScope("future_feature"), want: true},
		{name: "main family preserves legacy identity", providerWindowID: "weekly", scope: MainScope()},
		{name: "all scope does not replace itself", providerWindowID: "future-feature-weekly-0", scope: ModelScope{Kind: "all", Complete: true}},
		{name: "incomplete all scope fails closed for non-main window", providerWindowID: "future-feature-weekly-0", scope: ModelScope{Kind: "all", Complete: false}, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsLegacyAllScopeReplacement(test.providerWindowID, test.scope); got != test.want {
				t.Fatalf("IsLegacyAllScopeReplacement(%q, %#v) = %v, want %v", test.providerWindowID, test.scope, got, test.want)
			}
		})
	}
}

func TestStrictSemanticClassificationIsNotPrefixBased(t *testing.T) {
	tests := []struct {
		name             string
		providerWindowID string
		wantSpark        bool
		wantMain         bool
		wantCodeReview   bool
		wantFeatureKey   string
	}{
		{name: "real spark", providerWindowID: "spark-weekly-0", wantSpark: true},
		{name: "real spark canonical alias", providerWindowID: "gpt-5-3-codex-spark-weekly-0", wantSpark: true},
		{name: "real spark generic", providerWindowID: "spark-0-window-7d-0", wantSpark: true},
		{name: "legacy fast coding alias", providerWindowID: "fast-coding-weekly-0", wantSpark: true},
		{name: "spark prefixed feature", providerWindowID: "spark-feature-weekly-0", wantFeatureKey: "spark_feature"},
		{name: "codex spark prefixed feature", providerWindowID: "codex-spark-feature-weekly-0", wantFeatureKey: "codex_spark_feature"},
		{name: "fast coding prefixed feature", providerWindowID: "fast-coding-premium-weekly-0", wantFeatureKey: "fast_coding_premium"},
		{name: "real main five hour", providerWindowID: "five-hour", wantMain: true},
		{name: "real main generic", providerWindowID: "window-7d-0", wantMain: true},
		{name: "real main generic unknown", providerWindowID: "window-unknown-0", wantMain: true},
		{name: "window prefixed feature", providerWindowID: "window-feature-weekly-0", wantFeatureKey: "window_feature"},
		{name: "window beta is not main", providerWindowID: "window-beta", wantFeatureKey: "window_beta"},
		{name: "real code review weekly", providerWindowID: "code-review-weekly", wantCodeReview: true},
		{name: "real code review generic", providerWindowID: "code-review-window-7d-0", wantCodeReview: true},
		{name: "code review family window", providerWindowID: "code-review-weekly-0", wantFeatureKey: "code_review"},
		{name: "code review premium family", providerWindowID: "code-review-premium-weekly-0", wantFeatureKey: "code_review_premium"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsSparkProviderWindowID(test.providerWindowID); got != test.wantSpark {
				t.Fatalf("IsSparkProviderWindowID(%q) = %v, want %v", test.providerWindowID, got, test.wantSpark)
			}
			if got := IsMainProviderWindowID(test.providerWindowID); got != test.wantMain {
				t.Fatalf("IsMainProviderWindowID(%q) = %v, want %v", test.providerWindowID, got, test.wantMain)
			}
			if got := IsCodeReviewProviderWindowID(test.providerWindowID); got != test.wantCodeReview {
				t.Fatalf("IsCodeReviewProviderWindowID(%q) = %v, want %v", test.providerWindowID, got, test.wantCodeReview)
			}
			scope := InferScopeFromProviderWindowID(test.providerWindowID)
			switch {
			case test.wantSpark:
				if !IsSparkScope(scope) {
					t.Fatalf("InferScopeFromProviderWindowID(%q) = %#v, want Spark", test.providerWindowID, scope)
				}
			case test.wantMain:
				if !IsMainScope(scope.Kind, scope.Key) {
					t.Fatalf("InferScopeFromProviderWindowID(%q) = %#v, want Main", test.providerWindowID, scope)
				}
			case test.wantCodeReview:
				if scope.Kind != "feature" || scope.Key != CodeReviewScopeKey || scope.Complete {
					t.Fatalf("InferScopeFromProviderWindowID(%q) = %#v, want code review", test.providerWindowID, scope)
				}
			default:
				if scope.Kind != "feature" || scope.Key != test.wantFeatureKey || scope.Complete {
					t.Fatalf("InferScopeFromProviderWindowID(%q) = %#v, want incomplete feature %q", test.providerWindowID, scope, test.wantFeatureKey)
				}
			}
		})
	}
}

func TestCanonicalProviderWindowIDKeepsImpostorIdsIntact(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "spark exact", value: "spark", want: "spark"},
		{name: "spark alias suffix", value: "codex-spark-weekly-0", want: "spark-weekly-0"},
		{name: "spark generic suffix", value: "gpt-5-3-codex-spark-0-window-7d-0", want: "spark-0-window-7d-0"},
		{name: "spark prefixed feature stays", value: "codex-spark-feature-weekly-0", want: "codex-spark-feature-weekly-0"},
		{name: "spark feature stays", value: "spark-feature-weekly-0", want: "spark-feature-weekly-0"},
		{name: "spark main-shaped stays", value: "spark-window-7d-0", want: "spark-window-7d-0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CanonicalProviderWindowID(test.value); got != test.want {
				t.Fatalf("CanonicalProviderWindowID(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestResolveProviderWindowScopeKeepsExplicitScopeAndFailsClosed(t *testing.T) {
	explicit := FeatureScope("fast_coding")
	got := ResolveProviderWindowScope("fast-coding-weekly-0", "weekly", explicit)
	if got.Kind != explicit.Kind || got.Key != explicit.Key || got.Complete != explicit.Complete {
		t.Fatalf("explicit scope was overridden: %#v", got)
	}

	legacyAll := ModelScope{Kind: "all", Complete: true}
	if got := ResolveProviderWindowScope("spark-feature-weekly-0", "weekly", legacyAll); got.Kind != "feature" ||
		got.Key != "spark_feature" || got.Complete {
		t.Fatalf("legacy all spark-feature = %#v, want incomplete spark_feature feature", got)
	}
	if got := ResolveProviderWindowScope("window-feature-weekly-0", "weekly", legacyAll); got.Kind != "feature" ||
		got.Key != "window_feature" || got.Complete {
		t.Fatalf("legacy all window-feature = %#v, want incomplete window_feature feature", got)
	}
	if got := ResolveProviderWindowScope("code-review-premium-weekly-0", "weekly", legacyAll); got.Kind != "feature" ||
		got.Key != "code_review_premium" || got.Complete {
		t.Fatalf("legacy all code-review-premium = %#v, want incomplete code_review_premium feature", got)
	}
	if got := ResolveProviderWindowScope("fast-coding-weekly-0", "weekly", legacyAll); !IsSparkScope(got) {
		t.Fatalf("legacy all fast-coding-weekly-0 = %#v, want Spark", got)
	}
	if got := ResolveProviderWindowScope("code-review-weekly", "weekly", legacyAll); got.Kind != "feature" ||
		got.Key != CodeReviewScopeKey {
		t.Fatalf("legacy all code-review-weekly = %#v, want code review", got)
	}
	if got := ResolveProviderWindowScope("window-7d-0", "weekly", legacyAll); !got.Complete ||
		got.Kind != "all" {
		t.Fatalf("legacy all window-7d-0 = %#v, want preserved all", got)
	}
}

func TestCanonicalProviderWindowIDForScopeRequiresVerifiedSparkScope(t *testing.T) {
	if got := CanonicalProviderWindowIDForScope("fast-coding-weekly-0", "weekly", FeatureScope("fast_coding")); got != "fast-coding-weekly-0" {
		t.Fatalf("explicit feature scope rewrote id: %q", got)
	}
	if got := CanonicalProviderWindowIDForScope("fast-coding-feature-weekly-0", "weekly", SparkScope()); got != "fast-coding-feature-weekly-0" {
		t.Fatalf("spark scope rewrote impostor id: %q", got)
	}
	if got := CanonicalProviderWindowIDForScope("fast-coding-weekly-0", "weekly", SparkScope()); got != "spark-weekly-0" {
		t.Fatalf("spark scope canonicalization = %q, want spark-weekly-0", got)
	}
}
