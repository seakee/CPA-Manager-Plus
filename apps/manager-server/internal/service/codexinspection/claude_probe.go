package codexinspection

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/claudequota"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	// Anthropic's subscription usage endpoint. It returns the 5-hour session
	// window, the 7-day window, and per-model weeklies, each with a utilization
	// fraction and a reset timestamp. It requires the OAuth access token the CPA
	// pool already holds and refreshes, plus the oauth beta header.
	claudeUsageURL       = "https://api.anthropic.com/api/oauth/usage?cedar_ember=1"
	claudeOAuthBetaHeader = "oauth-2025-04-20"
	claudeUserAgent      = "claude-cli/1.0 (external, cli)"
)

// inspectSingleClaudeAccount fetches a Claude subscription account's quota
// windows through the CPA management api-call bridge (which injects and
// refreshes the real OAuth token), mirroring inspectSingleXAIAccount.
func (s *Service) inspectSingleClaudeAccount(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
	logger runLogger,
) model.CodexInspectionResult {
	base := resultFromAccount(item)
	if item.AuthIndex == "" {
		base.Action = "keep"
		base.ActionReason = "monitoring.claude_inspection_reason_missing_auth_index"
		base.Error = "missing auth_index"
		base.ErrorKind = "missing_auth_index"
		base.ErrorDetail = "missing auth_index"
		logger.warning(ctx, "monitoring.claude_inspection_log_server_missing_auth_index", map[string]any{
			"provider":       "claude",
			"fileName":       item.FileName,
			"displayAccount": item.DisplayAccount,
		})
		return base
	}

	var response apiCallResponse
	var requestErr error
	for attempt := 0; attempt <= settings.Retries; attempt++ {
		response, _, requestErr = s.requestClaudeUsage(ctx, setup, settings, item)
		if requestErr == nil && !claudeShouldRetry(response.StatusCode) {
			break
		}
		if attempt == settings.Retries {
			break
		}
	}
	if requestErr != nil {
		base.Action = "keep"
		base.ActionReason = "monitoring.claude_inspection_reason_upstream_error"
		base.ErrorKind = "upstream_error"
		base.Error = truncate(requestErr.Error(), maxStoredBodyText)
		base.ErrorDetail = base.Error
		logClaudeInspectionResult(ctx, logger, item, base)
		return base
	}

	base.StatusCode = intPointer(response.StatusCode)
	if response.StatusCode == http.StatusUnauthorized {
		base.Action = "reauth"
		base.ActionReason = "monitoring.claude_inspection_reason_auth_invalid"
		base.IsQuota = false
		base.ErrorKind = "auth_invalid"
		base.Error = firstNonEmpty(response.BodyText, "Claude usage request was not authorized")
		base.ErrorDetail = truncate(base.Error, maxStoredBodyText)
		logClaudeInspectionResult(ctx, logger, item, base)
		return base
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		base.Action = "keep"
		base.ActionReason = claudeReasonForStatus(response.StatusCode)
		base.ErrorKind = claudeClassification(response.StatusCode)
		base.Error = truncate(firstNonEmpty(response.BodyText, fmt.Sprint(response.Body)), maxStoredBodyText)
		base.ErrorDetail = base.Error
		logClaudeInspectionResult(ctx, logger, item, base)
		return base
	}

	payload := parseRecord(response.Body)
	if payload == nil {
		payload = parseRecord(response.BodyText)
	}
	windows := claudequota.ParseMap(payload)
	if len(windows) == 0 {
		base.Action = "keep"
		base.ActionReason = "monitoring.claude_inspection_reason_protocol_changed"
		base.ErrorKind = "protocol_changed"
		base.Error = "Claude usage response contained no readable quota windows"
		base.ErrorDetail = base.Error
		logClaudeInspectionResult(ctx, logger, item, base)
		return base
	}

	base.QuotaWindows = claudeWindowsToModel(windows)
	base.UsedPercent = claudeMaxUsedPercent(windows)
	base.Action = "keep"
	base.ActionReason = "monitoring.claude_inspection_reason_billing_healthy"
	base.IsQuota = false
	base.Error = ""
	base.ErrorKind = "billing_healthy"
	base.ErrorDetail = ""
	if base.Disabled && item.AutoRecoverOwned {
		base.Action = "enable"
		base.ActionReason = "monitoring.claude_inspection_reason_enable_owned"
		base.AutoRecoverEligible = true
	}
	logClaudeInspectionResult(ctx, logger, item, base)
	return base
}

func (s *Service) requestClaudeUsage(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
) (apiCallResponse, int, error) {
	header := map[string]string{
		"Authorization":  "Bearer $TOKEN$",
		"anthropic-beta": claudeOAuthBetaHeader,
		"User-Agent":     claudeUserAgent,
		"Accept":         "application/json",
	}
	return s.requestProviderBillingAt(ctx, setup, settings, item, claudeUsageURL, header)
}

func claudeWindowsToModel(windows []claudequota.Window) []model.CodexInspectionQuotaWindow {
	result := make([]model.CodexInspectionQuotaWindow, 0, len(windows))
	for _, window := range windows {
		result = append(result, model.CodexInspectionQuotaWindow{
			ID:          window.ID,
			LabelKey:    window.LabelKey,
			UsedPercent: window.UsedPercent,
			ResetLabel:  window.ResetLabel,
			ResetAtMS:   window.ResetAtMS,
		})
	}
	return result
}

func claudeMaxUsedPercent(windows []claudequota.Window) *float64 {
	var max *float64
	for _, window := range windows {
		if window.UsedPercent == nil {
			continue
		}
		if max == nil || *window.UsedPercent > *max {
			value := *window.UsedPercent
			max = &value
		}
	}
	return max
}

func claudeShouldRetry(statusCode int) bool {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return true
	case statusCode >= 500:
		return true
	default:
		return false
	}
}

func claudeClassification(statusCode int) string {
	switch {
	case statusCode == http.StatusUnauthorized:
		return "auth_invalid"
	case statusCode == http.StatusForbidden:
		return "permission_unknown"
	case statusCode == http.StatusTooManyRequests:
		return "rate_limited"
	case statusCode >= 500:
		return "upstream_error"
	default:
		return "unknown"
	}
}

func claudeReasonForStatus(statusCode int) string {
	switch claudeClassification(statusCode) {
	case "permission_unknown":
		return "monitoring.claude_inspection_reason_permission_unknown"
	case "rate_limited":
		return "monitoring.claude_inspection_reason_rate_limited"
	case "upstream_error":
		return "monitoring.claude_inspection_reason_upstream_error"
	default:
		return "monitoring.claude_inspection_reason_unknown"
	}
}

func logClaudeInspectionResult(
	ctx context.Context,
	logger runLogger,
	item account,
	result model.CodexInspectionResult,
) {
	detail := map[string]any{
		"provider":       "claude",
		"fileName":       item.FileName,
		"displayAccount": item.DisplayAccount,
		"healthEvidence": result.ErrorKind,
		"windowCount":    len(result.QuotaWindows),
		"action":         result.Action,
	}
	if result.StatusCode != nil {
		detail["statusCode"] = *result.StatusCode
	}
	if result.UsedPercent != nil {
		detail["usedPercent"] = *result.UsedPercent
	}
	level := "info"
	switch result.Action {
	case "reauth", "delete":
		level = "error"
	case "disable":
		level = "warning"
	case "enable":
		level = "success"
	default:
		if !isHealthyClaudeEvidence(result.ErrorKind) {
			level = "warning"
		}
	}
	logger.log(ctx, level, "monitoring.claude_inspection_log_server_complete", detail)
}

func isHealthyClaudeEvidence(errorKind string) bool {
	switch strings.TrimSpace(errorKind) {
	case "", "billing_healthy":
		return true
	default:
		return false
	}
}
