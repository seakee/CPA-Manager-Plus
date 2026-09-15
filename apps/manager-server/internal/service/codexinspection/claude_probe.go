package codexinspection

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

func (s *Service) inspectSingleClaudeAccount(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
	logger runLogger,
) model.CodexInspectionResult {
	base := resultFromAccount(item)
	base.PlanType = ""
	base.Action = "keep"
	base.AutoRecoverEligible = false

	if strings.TrimSpace(item.AuthIndex) == "" {
		return claudeKeepResult(base, "missing_auth_index", "Claude inspection skipped because auth_index is missing", nil)
	}

	var response apiCallResponse
	var err error
	for attempt := 0; attempt <= settings.Retries; attempt++ {
		response, _, err = s.requestProviderAPICallAt(
			ctx,
			setup,
			settings,
			item,
			http.MethodGet,
			claudeUsageURL,
				map[string]string{
					"Authorization":  "Bearer $TOKEN$",
					"Content-Type":   "application/json",
					"anthropic-beta": "oauth-2025-04-20",
				},
			"",
		)
		if err == nil {
			break
		}
	}
	if err != nil {
		return claudeKeepResult(base, "request_error", truncate(err.Error(), maxStoredBodyText), nil)
	}
	if !response.HasStatusCode {
		detail := firstNonEmpty(truncate(response.BodyText, maxStoredBodyText), "Claude usage response missing status_code")
		return claudeKeepResult(base, "missing_status", detail, nil)
	}

	statusCode := response.StatusCode
	base.StatusCode = &statusCode
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return claudeKeepResult(base, "http_status", firstNonEmpty(truncate(response.BodyText, maxStoredBodyText), "HTTP "+strconv.Itoa(statusCode)), &statusCode)
	}

	payload := parseRecord(response.Body)
	if payload == nil {
		payload = parseRecord(response.BodyText)
	}
	windows, observed := parseClaudeUsagePayload(payload)
	if !observed {
		return claudeKeepResult(base, "unrecognized_payload", "Claude usage response did not contain a recognized quota inventory", &statusCode)
	}
	base.QuotaWindows = windows
	base.QuotaInventoryObserved = true
	if len(windows) == 0 {
		base.QuotaWindowsJSON = "[]"
	}
	base.UsedPercent = claudeMaximumUsedPercent(windows)
	base.Action = "keep"
	base.ActionReason = "Claude usage observed; inspection is read-only"
	base.AutoRecoverEligible = false
	base.Error = ""
	base.ErrorKind = ""
	base.ErrorDetail = ""
	logger.info(ctx, "Claude usage inspection complete", map[string]any{
		"provider":       model.CodexInspectionTargetClaude,
		"fileName":       item.FileName,
		"displayAccount": item.DisplayAccount,
		"statusCode":     statusCode,
		"windowCount":    len(windows),
	})
	return base
}

func claudeKeepResult(
	base model.CodexInspectionResult,
	errorKind string,
	detail string,
	statusCode *int,
) model.CodexInspectionResult {
	base.Action = "keep"
	base.ActionReason = "Claude inspection retained account; no action is permitted"
	base.AutoRecoverEligible = false
	base.IsQuota = false
	base.ErrorKind = strings.TrimSpace(errorKind)
	base.ErrorDetail = truncate(strings.TrimSpace(detail), maxStoredBodyText)
	base.Error = base.ErrorDetail
	if statusCode != nil && *statusCode > 0 {
		base.StatusCode = statusCode
	}
	return base
}

func parseClaudeUsagePayload(payload map[string]any) ([]model.CodexInspectionQuotaWindow, bool) {
	if payload == nil {
		return nil, false
	}
	windows := make([]model.CodexInspectionQuotaWindow, 0, 4)
	knownFound := false
	knownObserved := false
	for _, definition := range []struct {
		key      string
		id       string
		labelKey string
		allScope bool
		alias    string
	}{
		{key: "five_hour", id: "five-hour", labelKey: "claude_quota.five_hour", allScope: true, alias: "five_hour"},
		{key: "seven_day", id: "seven-day", labelKey: "claude_quota.seven_day", allScope: true, alias: "seven_day"},
		{key: "seven_day_sonnet", id: "seven-day-sonnet", labelKey: "claude_quota.seven_day_sonnet", alias: "seven_day_sonnet"},
		{key: "seven_day_opus", id: "seven-day-opus", labelKey: "claude_quota.seven_day_opus", alias: "seven_day_opus"},
		{key: "seven_day_oauth_apps", id: "seven-day-oauth-apps", labelKey: "claude_quota.seven_day_oauth_apps", alias: "seven_day_oauth_apps"},
		{key: "seven_day_cowork", id: "seven-day-cowork", labelKey: "claude_quota.seven_day_cowork", alias: "seven_day_cowork"},
		{key: "seven_day_omelette", id: "seven-day-omelette", labelKey: "claude_quota.seven_day_omelette", alias: "seven_day_omelette"},
	} {
		raw, exists := payload[definition.key]
		if !exists {
			continue
		}
		knownFound = true
		bucket := toMap(raw)
		if bucket == nil {
			continue
		}
		window, ok := parseClaudeUsageWindow(definition.id, definition.labelKey, definition.alias, definition.allScope, bucket)
		if !ok {
			continue
		}
		windows = append(windows, window)
		knownObserved = true
	}
	if knownObserved {
		return windows, true
	}
	if knownFound {
		// A present but malformed or empty known bucket is not an observed empty
		// inventory. Only an explicit empty limits array has that meaning.
		return nil, false
	}
	if rawLimits, exists := payload["limits"]; exists {
		if limits, ok := rawLimits.([]any); ok {
			if len(limits) == 0 {
				return windows, true
			}
			for _, rawLimit := range limits {
				limit := toMap(rawLimit)
				if limit == nil {
					return nil, false
				}
				window, ok := parseClaudeLimitWindow(limit)
				if !ok {
					return nil, false
				}
				windows = append(windows, window)
			}
			return windows, true
		}
	}
	return nil, false
}

func parseClaudeLimitWindow(limit map[string]any) (model.CodexInspectionQuotaWindow, bool) {
	name := strings.ToLower(strings.TrimSpace(readString(limit, "name", "id", "key", "type", "window")))
	if name == "" {
		return model.CodexInspectionQuotaWindow{}, false
	}
	id := ""
	labelKey := ""
	allScope := false
	switch strings.ReplaceAll(name, "-", "_") {
	case "five_hour", "five_hours", "fivehour":
		id = "five-hour"
		labelKey = "claude_quota.five_hour"
		allScope = true
	case "seven_day", "seven_days", "sevenday", "weekly":
		id = "seven-day"
		labelKey = "claude_quota.seven_day"
		allScope = true
	case "seven_day_sonnet":
		id = "seven-day-sonnet"
		labelKey = "claude_quota.seven_day_sonnet"
	case "seven_day_opus":
		id = "seven-day-opus"
		labelKey = "claude_quota.seven_day_opus"
	case "seven_day_oauth_apps":
		id = "seven-day-oauth-apps"
		labelKey = "claude_quota.seven_day_oauth_apps"
	default:
		return model.CodexInspectionQuotaWindow{}, false
	}
	return parseClaudeUsageWindow(id, labelKey, name, allScope, limit)
}

func parseClaudeUsageWindow(
	id string,
	labelKey string,
	alias string,
	allScope bool,
	bucket map[string]any,
) (model.CodexInspectionQuotaWindow, bool) {
	usedPercent, hasUtilization := claudeUtilization(bucket)
	resetAtMS := claudeAbsoluteResetAtMS(bucket["resets_at"])
	if !hasUtilization && resetAtMS == 0 {
		return model.CodexInspectionQuotaWindow{}, false
	}
	window := model.CodexInspectionQuotaWindow{
		ID:                    id,
		LabelKey:              labelKey,
		UsedPercent:           usedPercent,
		ProviderWindowAliases: []string{alias},
	}
	if allScope {
		window.ModelScope = &model.CodexInspectionQuotaModelScope{Kind: "all", Complete: true}
	} else {
		window.ModelScope = &model.CodexInspectionQuotaModelScope{Kind: "feature", Key: alias, Complete: false}
	}
	if resetAtMS > 0 {
		window.ResetAtMS = resetAtMS
		window.ResetAccuracy = "exact"
	}
	return window, true
}

func claudeUtilization(bucket map[string]any) (*float64, bool) {
	raw, exists := bucket["utilization"]
	if !exists || raw == nil {
		return nil, false
	}
	var value float64
	switch typed := raw.(type) {
	case float64:
		value = typed
	case float32:
		value = float64(typed)
	case int:
		value = float64(typed)
	case int64:
		value = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return nil, false
		}
		value = parsed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return nil, false
		}
		value = parsed
	default:
		return nil, false
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return nil, false
	}
	return &value, true
}

func claudeAbsoluteResetAtMS(raw any) int64 {
	if raw == nil {
		return 0
	}
	switch value := raw.(type) {
	case float64:
		return claudeUnixTimestampMS(value)
	case float32:
		return claudeUnixTimestampMS(float64(value))
	case int:
		return claudeUnixTimestampMS(float64(value))
	case int64:
		return claudeUnixTimestampMS(float64(value))
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return 0
		}
		return claudeUnixTimestampMS(parsed)
	case string:
		text := strings.TrimSpace(value)
		if text == "" {
			return 0
		}
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed.UnixMilli()
		}
		if parsed, err := strconv.ParseFloat(text, 64); err == nil {
			return claudeUnixTimestampMS(parsed)
		}
	}
	return 0
}

func claudeUnixTimestampMS(value float64) int64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || math.Trunc(value) != value {
		return 0
	}
	if value >= 100_000_000_000 {
		if value > float64(math.MaxInt64) {
			return 0
		}
		return int64(value)
	}
	if value > float64(math.MaxInt64/1000) {
		return 0
	}
	return int64(value) * 1000
}

func claudeMaximumUsedPercent(windows []model.CodexInspectionQuotaWindow) *float64 {
	var maximum *float64
	for _, window := range windows {
		if window.UsedPercent == nil {
			continue
		}
		if maximum == nil || *window.UsedPercent > *maximum {
			value := *window.UsedPercent
			maximum = &value
		}
	}
	return maximum
}
