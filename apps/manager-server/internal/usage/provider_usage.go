package usage

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ProviderUsageKindIncludedFree = "included_free_usage"
	ProviderUsageStateExhausted   = "exhausted"
	ProviderUsageWindowRolling24H = "rolling_24h"
	ProviderUsageSourceBody       = "response_body"
	ProviderUsageCodeXAIFree      = "subscription:free-usage-exhausted"
	xaiFreeUsageExhaustedCode     = ProviderUsageCodeXAIFree
)

var (
	xaiUsageTokensPattern          = regexp.MustCompile(`(?i)tokens\s*\(actual/limit\)\s*:\s*([0-9][0-9,]*)\s*/\s*([0-9][0-9,]*)`)
	xaiUsageModelPattern           = regexp.MustCompile(`(?i)free usage for model\s+([a-z0-9][a-z0-9._:-]*)`)
	providerUsageAbsoluteResetKeys = []string{"reset_at", "resetAt", "resets_at", "resetsAt", "period_end", "periodEnd", "billing_period_end", "billingPeriodEnd"}
	providerUsageRelativeResetKeys = []string{"reset_after_seconds", "resetAfterSeconds"}
)

// ProviderUsageMetadata contains provider-specific usage evidence derived from
// a response. It deliberately keeps transport rate-limit headers separate:
// those headers do not represent xAI included-free-usage balance.
type ProviderUsageMetadata struct {
	Provider           string `json:"provider,omitempty"`
	Kind               string `json:"kind,omitempty"`
	State              string `json:"state,omitempty"`
	Code               string `json:"code,omitempty"`
	Model              string `json:"model,omitempty"`
	Unit               string `json:"unit,omitempty"`
	Actual             *int64 `json:"actual,omitempty"`
	Limit              *int64 `json:"limit,omitempty"`
	Remaining          *int64 `json:"remaining,omitempty"`
	Overage            *int64 `json:"overage,omitempty"`
	WindowKind         string `json:"window_kind,omitempty"`
	ObservedAtMS       int64  `json:"observed_at_ms,omitempty"`
	RecoverAtMS        int64  `json:"recover_at_ms,omitempty"`
	RecoverAtEstimated bool   `json:"recover_at_estimated,omitempty"`
	Source             string `json:"source,omitempty"`
}

func ProviderUsageMetadataFromRecord(record map[string]any, base time.Time) *ProviderUsageMetadata {
	if record == nil || normalizeProviderUsageProvider(record) != "xai" {
		return nil
	}
	fail, _ := first(record, "fail").(map[string]any)
	statusCode := readIntFrom(fail, "status_code", "statusCode")
	if statusCode == 0 {
		statusCode = readInt(record, "fail_status_code", "failStatusCode", "status_code", "statusCode")
	}
	if statusCode != 402 && statusCode != 429 {
		return nil
	}
	body := readString(fail, "body")
	if body == "" {
		body = readString(record, "fail_body", "failBody")
	}
	if body == "" {
		return nil
	}

	var decoded any
	_ = json.Unmarshal([]byte(body), &decoded)
	if !containsProviderUsageCode(decoded, xaiFreeUsageExhaustedCode) {
		return nil
	}

	metadata := &ProviderUsageMetadata{
		Provider:   "xai",
		Kind:       ProviderUsageKindIncludedFree,
		State:      ProviderUsageStateExhausted,
		Code:       xaiFreeUsageExhaustedCode,
		Unit:       "tokens",
		WindowKind: ProviderUsageWindowRolling24H,
		Source:     ProviderUsageSourceBody,
	}
	if !base.IsZero() {
		metadata.ObservedAtMS = base.UnixMilli()
	}
	if match := xaiUsageModelPattern.FindStringSubmatch(body); len(match) == 2 {
		metadata.Model = strings.TrimSpace(match[1])
	}
	if match := xaiUsageTokensPattern.FindStringSubmatch(body); len(match) == 3 {
		actual, actualOK := parseProviderUsageCount(match[1])
		limit, limitOK := parseProviderUsageCount(match[2])
		if actualOK && limitOK && limit > 0 {
			metadata.Actual = int64Pointer(actual)
			metadata.Limit = int64Pointer(limit)
			remaining := limit - actual
			if remaining < 0 {
				remaining = 0
			}
			overage := actual - limit
			if overage < 0 {
				overage = 0
			}
			metadata.Remaining = int64Pointer(remaining)
			metadata.Overage = int64Pointer(overage)
		}
	}
	if resetAt, ok := providerUsageExplicitResetTime(decoded, base); ok {
		metadata.RecoverAtMS = resetAt.UnixMilli()
	} else if !base.IsZero() {
		// Rolling 24h free-usage windows do not publish a wall-clock reset.
		// event_time+24h is an upper-bound estimate for cooldown scheduling,
		// not a precise reconstruction of the rolling window.
		metadata.RecoverAtMS = base.Add(24 * time.Hour).UnixMilli()
		metadata.RecoverAtEstimated = true
	}
	sanitizeProviderUsageMetadata(metadata)
	return metadata
}

func normalizeProviderUsageProvider(record map[string]any) string {
	provider := readString(record, "provider", "type")
	providerSnapshot := readString(record, "auth_provider_snapshot", "authProviderSnapshot")
	executor := readString(record, "executor_type", "executorType")
	if IsNativeXAIProvider(provider, providerSnapshot, executor) {
		return "xai"
	}
	return normalizeProviderUsageIdentity(firstNonEmptyString(providerSnapshot, provider))
}

// IsNativeXAIProvider distinguishes native xAI execution from arbitrary
// OpenAI-compatible providers that happen to use "grok" in their name.
func IsNativeXAIProvider(provider string, providerSnapshot string, executor string) bool {
	identities := []string{provider, providerSnapshot}
	for _, identity := range identities {
		switch normalizeProviderUsageIdentity(identity) {
		case "xai", "x-ai":
			return true
		}
	}
	if !isNativeXAIExecutor(executor) {
		return false
	}
	for _, identity := range identities {
		normalized := normalizeProviderUsageIdentity(identity)
		if normalized != "" && normalized != "grok" {
			return false
		}
	}
	return true
}

func normalizeProviderUsageIdentity(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return strings.ReplaceAll(normalized, "_", "-")
}

func isNativeXAIExecutor(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", "", "_", "", " ", "").Replace(normalized)
	return normalized == "xaiexecutor"
}

func containsProviderUsageCode(value any, target string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if strings.EqualFold(strings.TrimSpace(stringValue(typed["code"])), target) {
			return true
		}
		for _, child := range typed {
			if containsProviderUsageCode(child, target) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsProviderUsageCode(child, target) {
				return true
			}
		}
	}
	return false
}

func providerUsageExplicitResetTime(value any, base time.Time) (time.Time, bool) {
	if resetAt, ok := providerUsageResetTimeByKeys(value, base, providerUsageAbsoluteResetKeys, false); ok {
		return resetAt, true
	}
	return providerUsageResetTimeByKeys(value, base, providerUsageRelativeResetKeys, true)
}

func providerUsageResetTimeByKeys(value any, base time.Time, keys []string, relative bool) (time.Time, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if resetAt, ok := providerUsageResetValue(typed[key], base, relative); ok {
				return resetAt, true
			}
		}
		childKeys := make([]string, 0, len(typed))
		for key := range typed {
			childKeys = append(childKeys, key)
		}
		sort.Strings(childKeys)
		for _, key := range childKeys {
			if resetAt, ok := providerUsageResetTimeByKeys(typed[key], base, keys, relative); ok {
				return resetAt, true
			}
		}
	case []any:
		for _, child := range typed {
			if resetAt, ok := providerUsageResetTimeByKeys(child, base, keys, relative); ok {
				return resetAt, true
			}
		}
	}
	return time.Time{}, false
}

func providerUsageResetValue(value any, base time.Time, relative bool) (time.Time, bool) {
	if value == nil {
		return time.Time{}, false
	}
	text := strings.TrimSpace(stringValue(value))
	if text == "" {
		return time.Time{}, false
	}
	if relative {
		seconds, err := strconv.ParseFloat(text, 64)
		if err != nil || seconds <= 0 || base.IsZero() {
			return time.Time{}, false
		}
		return base.Add(time.Duration(seconds * float64(time.Second))), true
	}
	return parseHeaderTime(text, base, false)
}

func parseProviderUsageCount(value string) (int64, bool) {
	parsed, err := strconv.ParseInt(strings.ReplaceAll(strings.TrimSpace(value), ",", ""), 10, 64)
	return parsed, err == nil && parsed >= 0
}

func sanitizeProviderUsageMetadata(metadata *ProviderUsageMetadata) {
	if metadata == nil {
		return
	}
	metadata.Provider = strings.ToLower(normalizeHeaderValue(metadata.Provider))
	metadata.Kind = strings.ToLower(normalizeHeaderValue(metadata.Kind))
	metadata.State = strings.ToLower(normalizeHeaderValue(metadata.State))
	metadata.Code = strings.ToLower(normalizeHeaderValue(metadata.Code))
	metadata.Model = normalizeHeaderValue(metadata.Model)
	metadata.Unit = strings.ToLower(normalizeHeaderValue(metadata.Unit))
	metadata.WindowKind = strings.ToLower(normalizeHeaderValue(metadata.WindowKind))
	metadata.Source = strings.ToLower(normalizeHeaderValue(metadata.Source))
	for _, value := range []**int64{&metadata.Actual, &metadata.Limit, &metadata.Remaining, &metadata.Overage} {
		if *value != nil && **value < 0 {
			*value = nil
		}
	}
	if metadata.ObservedAtMS < 0 {
		metadata.ObservedAtMS = 0
	}
	if metadata.RecoverAtMS < 0 {
		metadata.RecoverAtMS = 0
	}
	if metadata.RecoverAtMS == 0 {
		metadata.RecoverAtEstimated = false
	}
}

// NormalizeProviderUsageMetadata returns a sanitized copy suitable for API
// output or persistence boundaries.
func NormalizeProviderUsageMetadata(metadata *ProviderUsageMetadata) *ProviderUsageMetadata {
	cloned := cloneProviderUsageMetadata(metadata)
	sanitizeProviderUsageMetadata(cloned)
	if cloned == nil || cloned.isEmpty() {
		return nil
	}
	return cloned
}

// MergeProviderUsageMetadata enriches existing evidence with same-response
// fields while preserving a provider-reported recovery over a later estimate.
func MergeProviderUsageMetadata(existing *ProviderUsageMetadata, overlay *ProviderUsageMetadata) *ProviderUsageMetadata {
	existing = NormalizeProviderUsageMetadata(existing)
	overlay = NormalizeProviderUsageMetadata(overlay)
	if existing == nil {
		return overlay
	}
	if overlay == nil {
		return existing
	}
	if providerUsageIdentityConflicts(existing, overlay) {
		return overlay
	}

	merged := *existing
	if overlay.Provider != "" {
		merged.Provider = overlay.Provider
	}
	if overlay.Kind != "" {
		merged.Kind = overlay.Kind
	}
	if overlay.State != "" {
		merged.State = overlay.State
	}
	if overlay.Code != "" {
		merged.Code = overlay.Code
	}
	if overlay.Model != "" {
		merged.Model = overlay.Model
	}
	if overlay.Unit != "" {
		merged.Unit = overlay.Unit
	}
	if overlay.Actual != nil {
		merged.Actual = cloneInt64Pointer(overlay.Actual)
	}
	if overlay.Limit != nil {
		merged.Limit = cloneInt64Pointer(overlay.Limit)
	}
	if overlay.Remaining != nil {
		merged.Remaining = cloneInt64Pointer(overlay.Remaining)
	}
	if overlay.Overage != nil {
		merged.Overage = cloneInt64Pointer(overlay.Overage)
	}
	if overlay.WindowKind != "" {
		merged.WindowKind = overlay.WindowKind
	}
	if overlay.ObservedAtMS > 0 {
		merged.ObservedAtMS = overlay.ObservedAtMS
	}
	if overlay.RecoverAtMS > 0 && (merged.RecoverAtMS <= 0 || !overlay.RecoverAtEstimated || merged.RecoverAtEstimated) {
		merged.RecoverAtMS = overlay.RecoverAtMS
		merged.RecoverAtEstimated = overlay.RecoverAtEstimated
	}
	if overlay.Source != "" {
		merged.Source = overlay.Source
	}
	return NormalizeProviderUsageMetadata(&merged)
}

func providerUsageIdentityConflicts(existing *ProviderUsageMetadata, overlay *ProviderUsageMetadata) bool {
	for _, pair := range [][2]string{
		{existing.Provider, overlay.Provider},
		{existing.Kind, overlay.Kind},
		{existing.State, overlay.State},
		{existing.Code, overlay.Code},
	} {
		if pair[0] != "" && pair[1] != "" && !strings.EqualFold(pair[0], pair[1]) {
			return true
		}
	}
	return false
}

func cloneProviderUsageMetadata(metadata *ProviderUsageMetadata) *ProviderUsageMetadata {
	if metadata == nil {
		return nil
	}
	cloned := *metadata
	cloned.Actual = cloneInt64Pointer(metadata.Actual)
	cloned.Limit = cloneInt64Pointer(metadata.Limit)
	cloned.Remaining = cloneInt64Pointer(metadata.Remaining)
	cloned.Overage = cloneInt64Pointer(metadata.Overage)
	return &cloned
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (m *ProviderUsageMetadata) isEmpty() bool {
	return m == nil || (m.Provider == "" && m.Kind == "" && m.State == "" && m.Code == "")
}

func int64Pointer(value int64) *int64 {
	return &value
}
