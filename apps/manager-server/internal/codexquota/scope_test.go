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
		wantScope      ModelScope
		wantPrefix     string
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
		},
		{
			name:           "provider feature wins over conflicting spark label",
			meteredFeature: "future_feature",
			limitName:      "Spark",
			wantScope:      FeatureScope("future_feature"),
			wantPrefix:     "future-feature",
		},
		{
			name:       "anonymous feature uses structural identity",
			wantScope:  FeatureScope("additional_p_18000_s_604800"),
			wantPrefix: "additional-p-18000-s-604800",
		},
		{
			name:       "non ascii label uses structural identity consistently",
			limitName:  "未来额度",
			wantScope:  FeatureScope("additional_p_18000_s_604800"),
			wantPrefix: "additional-p-18000-s-604800",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			anonymousIdentity := ""
			if test.name == "anonymous feature uses structural identity" ||
				test.name == "non ascii label uses structural identity consistently" {
				anonymousIdentity = "additional-p-18000-s-604800"
			}
			got := ResolveAdditionalScope(AdditionalScopeInput{
				MeteredFeature:    test.meteredFeature,
				LimitName:         test.limitName,
				AnonymousIdentity: anonymousIdentity,
			})
			if !reflect.DeepEqual(got.Scope, test.wantScope) || got.ProviderWindowPrefix != test.wantPrefix {
				t.Fatalf("ResolveAdditionalScope() = %#v, want scope=%#v prefix=%q", got, test.wantScope, test.wantPrefix)
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
