package codexquota

import (
	"sort"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	MainScopeKey              = "codex_main"
	SparkModelID              = "gpt-5.3-codex-spark"
	CodeReviewScopeKey        = "code_review"
	UnknownRequestScopeKey    = "request_scope_unknown"
	SparkProviderWindowPrefix = "spark"
)

type ModelScope struct {
	Kind     string
	Key      string
	Models   []string
	Complete bool
}

type AdditionalScopeResolution struct {
	Scope                ModelScope
	ProviderWindowPrefix string
	LegacyPrefixes       []string
}

type AdditionalScopeInput struct {
	MeteredFeature    string
	LimitName         string
	AnonymousIdentity string
}

type UsageScopeResolution struct {
	Scope                ModelScope
	ProviderWindowPrefix string
}

var sparkQuotaIdentifiers = map[string]struct{}{
	"spark":               {},
	"codex_spark":         {},
	"gpt_5_3_codex_spark": {},
}

var sparkProviderWindowPrefixes = []string{
	"gpt-5-3-codex-spark",
	"codex-spark",
	SparkProviderWindowPrefix,
}

// sparkLegacyProviderWindowPrefixes contains identifiers emitted by the
// pre-scoped additional-rate-limit parser. They are aliases only: keeping
// them out of CanonicalProviderWindowID prevents a legacy row from being
// rewritten before the lifecycle can mark it inactive.
var sparkLegacyProviderWindowPrefixes = []string{
	"fast-coding",
}

func MainScope() ModelScope {
	return ModelScope{Kind: "family", Key: MainScopeKey, Complete: true}
}

func SparkScope() ModelScope {
	return ModelScope{Kind: "models", Models: []string{SparkModelID}, Complete: true}
}

func CodeReviewScope() ModelScope {
	return ModelScope{Kind: "feature", Key: CodeReviewScopeKey, Complete: false}
}

func UnknownRequestScope() ModelScope {
	return ModelScope{Kind: "feature", Key: UnknownRequestScopeKey, Complete: false}
}

func FeatureScope(key string) ModelScope {
	key = NormalizeFeatureKey(key)
	if key == "" {
		key = "unknown"
	}
	return ModelScope{Kind: "feature", Key: key, Complete: false}
}

func NormalizeScope(scope ModelScope) ModelScope {
	scope.Kind = strings.ToLower(strings.TrimSpace(scope.Kind))
	scope.Key = strings.ToLower(strings.TrimSpace(scope.Key))
	scope.Models = normalizedUniqueModels(scope.Models)
	return scope
}

// ScopeFromFields reconstructs the scope completeness that can be represented
// by the persisted snapshot fields. The snapshot schema does not store the
// complete bit, so callers must derive it from the kind and its identity.
func ScopeFromFields(kind, key string, models []string) ModelScope {
	scope := NormalizeScope(ModelScope{Kind: kind, Key: key, Models: models})
	switch scope.Kind {
	case "all":
		scope.Complete = true
	case "models":
		scope.Complete = len(scope.Models) > 0
	case "family":
		scope.Complete = scope.Key != "" || len(scope.Models) > 0
	default:
		scope.Complete = false
	}
	return scope
}

// ResolveProviderWindowScope applies the provider-window identity as a
// stronger signal than a legacy, implicit `all` scope. Codex additional and
// feature windows are never allowed to become account-wide merely because an
// old client omitted the scoped model information.
func ResolveProviderWindowScope(providerWindowID, windowKind string, provided ModelScope) ModelScope {
	provided = NormalizeScope(provided)
	if provided.Kind != "all" {
		return provided
	}
	canonical := CanonicalProviderWindowID(providerWindowID, windowKind)
	if IsMainProviderWindowID(canonical) {
		// Preserve the caller's explicit account-wide representation. The web
		// view may expose Codex Main as its stable family scope, but legacy
		// lifecycle rows intentionally retain `all` until an explicit family
		// observation migrates them in place.
		return provided
	}
	if IsSparkProviderWindowID(providerWindowID) {
		return SparkScope()
	}
	canonical = CanonicalProviderWindowID(providerWindowID, windowKind)
	if canonical == "code-review" || strings.HasPrefix(canonical, "code-review-") {
		return CodeReviewScope()
	}
	// An arbitrary additional ID is not enough evidence to establish an
	// account-wide usage mapping. Legacy rows that omitted scope information
	// therefore fail closed as an incomplete feature scope; the lifecycle layer
	// can suppress that row once a scoped observation arrives.
	return FeatureScope(additionalProviderWindowFamily(canonical))
}

func NormalizeModelID(value string) string {
	return strings.ToLower(strings.TrimSpace(usageidentity.AnalyticsModel(strings.TrimSpace(value))))
}

func NormalizeFeatureKey(value string) string {
	return normalizeIdentifier(value, '_')
}

func NormalizeProviderWindowPrefix(value string) string {
	return normalizeIdentifier(value, '-')
}

func normalizeIdentifier(value string, separator rune) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	var builder strings.Builder
	pendingSeparator := false
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			if pendingSeparator && builder.Len() > 0 {
				builder.WriteRune(separator)
			}
			builder.WriteRune(char)
			pendingSeparator = false
			continue
		}
		pendingSeparator = true
	}
	return strings.Trim(builder.String(), string(separator))
}

func ResolveAdditionalScope(input AdditionalScopeInput) AdditionalScopeResolution {
	featureKey := NormalizeFeatureKey(input.MeteredFeature)
	nameKey := NormalizeFeatureKey(input.LimitName)
	if isSparkQuotaIdentifier(featureKey) || (featureKey == "" && isSparkQuotaIdentifier(nameKey)) {
		legacyPrefixes := []string{
			NormalizeProviderWindowPrefix(input.MeteredFeature),
			NormalizeProviderWindowPrefix(input.LimitName),
		}
		legacyPrefixes = append(legacyPrefixes, sparkProviderWindowPrefixes...)
		legacyPrefixes = append(legacyPrefixes, sparkLegacyProviderWindowPrefixes...)
		return AdditionalScopeResolution{
			Scope:                SparkScope(),
			ProviderWindowPrefix: SparkProviderWindowPrefix,
			LegacyPrefixes:       normalizedUniqueStrings(legacyPrefixes),
		}
	}

	key := featureKey
	if key == "" {
		key = nameKey
	}
	if key == "" {
		key = NormalizeFeatureKey(input.AnonymousIdentity)
	}
	prefix := NormalizeProviderWindowPrefix(input.LimitName)
	if featureKey != "" && isSparkQuotaIdentifier(nameKey) {
		prefix = NormalizeProviderWindowPrefix(input.MeteredFeature)
	}
	if prefix == "" {
		prefix = NormalizeProviderWindowPrefix(input.MeteredFeature)
	}
	if prefix == "" {
		prefix = NormalizeProviderWindowPrefix(input.AnonymousIdentity)
	}
	if key == "" || prefix == "" {
		key = "additional_unknown"
		prefix = "additional-unknown"
	}
	return AdditionalScopeResolution{
		Scope:                FeatureScope(key),
		ProviderWindowPrefix: prefix,
		LegacyPrefixes:       []string{prefix},
	}
}

func ResolveUsageScope(model, analyticsModel, requestedModel, resolvedModel string) UsageScopeResolution {
	if resolved := NormalizeModelID(resolvedModel); resolved != "" {
		return usageScopeForModel(resolved)
	}
	identity := firstNonEmptyModel(analyticsModel, requestedModel, model)
	if identity == "" {
		return UsageScopeResolution{
			Scope:                UnknownRequestScope(),
			ProviderWindowPrefix: "request-scope-unknown",
		}
	}
	return usageScopeForModel(identity)
}

func usageScopeForModel(model string) UsageScopeResolution {
	if IsSparkModel(model) {
		return UsageScopeResolution{
			Scope:                SparkScope(),
			ProviderWindowPrefix: SparkProviderWindowPrefix,
		}
	}
	return UsageScopeResolution{Scope: MainScope()}
}

func firstNonEmptyModel(values ...string) string {
	for _, value := range values {
		if normalized := NormalizeModelID(value); normalized != "" {
			return normalized
		}
	}
	return ""
}

func IsSparkModel(value string) bool {
	return NormalizeModelID(value) == SparkModelID
}

func IsMainScope(kind, key string) bool {
	return strings.EqualFold(strings.TrimSpace(kind), "family") &&
		strings.EqualFold(strings.TrimSpace(key), MainScopeKey)
}

func IsMainProviderWindowID(providerWindowID string) bool {
	id := CanonicalProviderWindowID(providerWindowID)
	switch id {
	case "five-hour", "weekly", "monthly", "primary", "secondary":
		return true
	default:
		return strings.HasPrefix(id, "window-")
	}
}

func IsLegacyAllScopeReplacement(providerWindowID string, scope ModelScope) bool {
	scope = NormalizeScope(scope)
	if IsMainProviderWindowID(providerWindowID) {
		return false
	}
	return scope.Kind != "" && (scope.Kind != "all" || !scope.Complete)
}

func IsLegacyMainAllScopeMigration(providerWindowID string, scope ModelScope) bool {
	scope = NormalizeScope(scope)
	return scope.Complete && IsMainProviderWindowID(providerWindowID) && IsMainScope(scope.Kind, scope.Key)
}

func MatchMainUsage(requestModel, billingModel string) (matched bool, unmatched bool) {
	modelID := NormalizeModelID(billingModel)
	if modelID == "" {
		modelID = NormalizeModelID(requestModel)
	}
	if modelID == "" {
		return false, true
	}
	if IsSparkModel(modelID) {
		return false, false
	}
	return true, false
}

func MatchModelScope(rowModel, billingModel string, models []string) (matched bool, unmatched bool) {
	modelID := NormalizeModelID(billingModel)
	if modelID == "" {
		modelID = NormalizeModelID(rowModel)
	}
	if modelID == "" {
		return false, true
	}
	for _, candidate := range normalizedUniqueModels(models) {
		if modelID == candidate {
			return true, false
		}
	}
	return false, false
}

func CanonicalProviderWindowID(value string, windowKind ...string) string {
	id := strings.ToLower(strings.TrimSpace(value))
	normalizedWindowKind := ""
	if len(windowKind) > 0 {
		normalizedWindowKind = strings.ToLower(strings.TrimSpace(windowKind[0]))
		normalizedWindowKind = strings.ReplaceAll(normalizedWindowKind, "_", "-")
	}
	if id == "primary" {
		return "five-hour"
	}
	if id == "secondary" {
		if normalizedWindowKind == "monthly" {
			return "monthly"
		}
		return "weekly"
	}
	for _, prefix := range sparkProviderWindowPrefixes {
		if id == prefix {
			return SparkProviderWindowPrefix
		}
		if strings.HasPrefix(id, prefix+"-") {
			return SparkProviderWindowPrefix + strings.TrimPrefix(id, prefix)
		}
	}
	return id
}

// IsSparkScope reports whether a persisted/display scope is the verified Spark
// model scope. It is intentionally stricter than a label or provider-window
// prefix check.
func IsSparkScope(scope ModelScope) bool {
	scope = NormalizeScope(scope)
	return scope.Kind == "models" && scope.Complete && len(scope.Models) == 1 &&
		scope.Models[0] == SparkModelID
}

// CanonicalProviderWindowIDForScope canonicalizes legacy Spark identifiers
// only when the matching model scope is known. The raw legacy identifier is
// still retained by lifecycle alias queries so old rows can be suppressed.
func CanonicalProviderWindowIDForScope(value, windowKind string, scope ModelScope) string {
	canonical := CanonicalProviderWindowID(value, windowKind)
	if !IsSparkScope(scope) {
		return canonical
	}
	raw := strings.ToLower(strings.TrimSpace(value))
	for _, prefix := range append(append([]string{}, sparkProviderWindowPrefixes...), sparkLegacyProviderWindowPrefixes...) {
		if raw == prefix {
			return SparkProviderWindowPrefix
		}
		if strings.HasPrefix(raw, prefix+"-") {
			return SparkProviderWindowPrefix + strings.TrimPrefix(raw, prefix)
		}
	}
	return canonical
}

func ProviderWindowAliases(providerWindowID string, scope ModelScope, additional ...string) []string {
	canonical := CanonicalProviderWindowID(providerWindowID)
	aliases := append([]string{strings.ToLower(strings.TrimSpace(providerWindowID)), canonical}, additional...)
	if scope.Kind == "family" && IsMainScope(scope.Kind, scope.Key) {
		switch canonical {
		case "five-hour":
			// Older response-header observations used the provider's primary
			// name. Keep the raw alias so lifecycle reclassification can attach
			// new evidence to that existing logical window.
			aliases = append(aliases, "primary")
		case "weekly", "monthly":
			// Team accounts historically exposed the long window as
			// `secondary` too, so both long-window candidates can reconcile it.
			aliases = append(aliases, "secondary")
		}
	}
	if scope.Kind == "models" && len(scope.Models) == 1 && IsSparkModel(scope.Models[0]) &&
		(canonical == SparkProviderWindowPrefix || strings.HasPrefix(canonical, SparkProviderWindowPrefix+"-")) {
		suffix := strings.TrimPrefix(canonical, SparkProviderWindowPrefix)
		for _, prefix := range sparkProviderWindowPrefixes {
			aliases = append(aliases, prefix+suffix)
		}
		for _, prefix := range sparkLegacyProviderWindowPrefixes {
			aliases = append(aliases, prefix+suffix)
		}
	}
	return normalizedUniqueStrings(aliases)
}

func InferScopeFromProviderWindowID(providerWindowID string) ModelScope {
	if IsSparkProviderWindowID(providerWindowID) {
		return SparkScope()
	}
	id := CanonicalProviderWindowID(providerWindowID)
	if IsMainProviderWindowID(id) {
		return MainScope()
	}
	if id == SparkProviderWindowPrefix || strings.HasPrefix(id, SparkProviderWindowPrefix+"-") {
		return SparkScope()
	}
	if id == "code-review" || strings.HasPrefix(id, "code-review-") {
		return CodeReviewScope()
	}
	return FeatureScope(additionalProviderWindowFamily(id))
}

func IsSparkProviderWindowID(providerWindowID string) bool {
	raw := strings.ToLower(strings.TrimSpace(providerWindowID))
	id := CanonicalProviderWindowID(raw)
	if id == SparkProviderWindowPrefix || strings.HasPrefix(id, SparkProviderWindowPrefix+"-") {
		return true
	}
	for _, prefix := range sparkLegacyProviderWindowPrefixes {
		if raw == prefix || strings.HasPrefix(raw, prefix+"-") {
			return true
		}
	}
	return false
}

func additionalProviderWindowFamily(providerWindowID string) string {
	for _, role := range []string{"five-hour", "weekly", "monthly"} {
		marker := "-" + role + "-"
		position := strings.LastIndex(providerWindowID, marker)
		if position <= 0 {
			continue
		}
		if _, err := strconv.Atoi(providerWindowID[position+len(marker):]); err == nil {
			return NormalizeFeatureKey(providerWindowID[:position])
		}
	}
	if position := strings.Index(providerWindowID, "-window-"); position > 0 {
		return NormalizeFeatureKey(providerWindowID[:position])
	}
	return NormalizeFeatureKey(providerWindowID)
}

func isSparkQuotaIdentifier(value string) bool {
	_, ok := sparkQuotaIdentifiers[value]
	return ok
}

func normalizedUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func normalizedUniqueModels(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		normalized = append(normalized, NormalizeModelID(value))
	}
	return normalizedUniqueStrings(normalized)
}
