package codexinspection

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/musequota"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	// Meta/Muse subscription usage endpoint. Unlike Codex/xAI/Claude, Muse
	// authenticates with the account's own `dca:` device token read from the
	// auth file (never the api-call bridge's $TOKEN$ placeholder), matching the
	// web metaQuota.ts behaviour.
	museUsageURL    = "https://api.meta.ai/muse-code/key"
	museAPIVersion  = "1.0.0"
	museTokenPrefix = "dca:"
)

// inspectSingleMuseAccount fetches a Meta/Muse subscription account's quota
// windows (current + weekly) on the inspection schedule, mirroring
// inspectSingleClaudeAccount and inspectSingleXAIAccount.
func (s *Service) inspectSingleMuseAccount(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
	logger runLogger,
) model.CodexInspectionResult {
	base := resultFromAccount(item)
	if item.AuthIndex == "" {
		base.Action = "keep"
		base.ActionReason = "monitoring.muse_inspection_reason_missing_auth_index"
		base.Error = "missing auth_index"
		base.ErrorKind = "missing_auth_index"
		base.ErrorDetail = "missing auth_index"
		logMuseInspectionResult(ctx, logger, item, base)
		return base
	}
	dcaToken := readMuseDcaToken(item.File)
	if dcaToken == "" {
		base.Action = "keep"
		base.ActionReason = "monitoring.muse_inspection_reason_missing_dca_token"
		base.Error = "missing or invalid dca token"
		base.ErrorKind = "missing_dca_token"
		base.ErrorDetail = base.Error
		logMuseInspectionResult(ctx, logger, item, base)
		return base
	}

	var response apiCallResponse
	var requestErr error
	for attempt := 0; attempt <= settings.Retries; attempt++ {
		response, _, requestErr = s.requestMuseUsage(ctx, setup, settings, item, dcaToken)
		if requestErr == nil && !museShouldRetry(response.StatusCode) {
			break
		}
		if attempt == settings.Retries {
			break
		}
	}
	if requestErr != nil {
		base.Action = "keep"
		base.ActionReason = "monitoring.muse_inspection_reason_upstream_error"
		base.ErrorKind = "upstream_error"
		base.Error = truncate(requestErr.Error(), maxStoredBodyText)
		base.ErrorDetail = base.Error
		logMuseInspectionResult(ctx, logger, item, base)
		return base
	}

	base.StatusCode = intPointer(response.StatusCode)
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		base.Action = "reauth"
		base.ActionReason = "monitoring.muse_inspection_reason_auth_invalid"
		base.ErrorKind = "auth_invalid"
		base.Error = firstNonEmpty(response.BodyText, "Muse usage request was not authorized")
		base.ErrorDetail = truncate(base.Error, maxStoredBodyText)
		logMuseInspectionResult(ctx, logger, item, base)
		return base
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		base.Action = "keep"
		base.ActionReason = museReasonForStatus(response.StatusCode)
		base.ErrorKind = museClassification(response.StatusCode)
		base.Error = truncate(firstNonEmpty(response.BodyText, fmt.Sprint(response.Body)), maxStoredBodyText)
		base.ErrorDetail = base.Error
		logMuseInspectionResult(ctx, logger, item, base)
		return base
	}

	payload := parseRecord(response.Body)
	if payload == nil {
		payload = parseRecord(response.BodyText)
	}
	windows := musequota.ParseMap(payload)
	if len(windows) == 0 {
		base.Action = "keep"
		base.ActionReason = "monitoring.muse_inspection_reason_protocol_changed"
		base.ErrorKind = "protocol_changed"
		base.Error = "Muse usage response contained no readable quota windows"
		base.ErrorDetail = base.Error
		logMuseInspectionResult(ctx, logger, item, base)
		return base
	}

	base.QuotaWindows = museWindowsToModel(windows)
	base.UsedPercent = museMaxUsedPercent(windows)
	base.Action = "keep"
	base.ActionReason = "monitoring.muse_inspection_reason_billing_healthy"
	base.Error = ""
	base.ErrorKind = "billing_healthy"
	base.ErrorDetail = ""
	if base.Disabled && item.AutoRecoverOwned {
		base.Action = "enable"
		base.ActionReason = "monitoring.muse_inspection_reason_enable_owned"
		base.AutoRecoverEligible = true
	}
	logMuseInspectionResult(ctx, logger, item, base)
	return base
}

func (s *Service) requestMuseUsage(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
	dcaToken string,
) (apiCallResponse, int, error) {
	header := map[string]string{
		"Authorization": "Bearer " + dcaToken,
		"x-api-version": museAPIVersion,
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	}
	return s.requestProviderAPICallAt(ctx, setup, settings, item, http.MethodPost, museUsageURL, header, "{}")
}

// readMuseDcaToken returns the account's dca: device token from the auth file,
// or "" when absent/invalid. Only the explicit dca_token field is accepted,
// never api_key or a bridge placeholder, matching the web parser.
func readMuseDcaToken(file authFile) string {
	for _, record := range []map[string]any{file, readMap(file, "metadata"), readMap(file, "attributes"), readMap(file, "providers"), readMap(readMap(file, "providers"), "meta")} {
		token := strings.TrimSpace(readString(record, "dca_token", "dcaToken", "access_token"))
		if strings.HasPrefix(token, museTokenPrefix) && len(token) > len(museTokenPrefix) &&
			!strings.ContainsAny(token, "\r\n ") {
			return token
		}
	}
	return ""
}

func museWindowsToModel(windows []musequota.Window) []model.CodexInspectionQuotaWindow {
	result := make([]model.CodexInspectionQuotaWindow, 0, len(windows))
	for _, window := range windows {
		result = append(result, model.CodexInspectionQuotaWindow{
			ID:                 window.ID,
			LabelKey:           window.LabelKey,
			UsedPercent:        window.UsedPercent,
			ResetLabel:         window.ResetLabel,
			ResetAtMS:          window.ResetAtMS,
			LimitWindowSeconds: window.LimitWindowSeconds,
		})
	}
	return result
}

func museMaxUsedPercent(windows []musequota.Window) *float64 {
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

func museShouldRetry(statusCode int) bool {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return true
	case statusCode >= 500:
		return true
	default:
		return false
	}
}

func museClassification(statusCode int) string {
	switch {
	case statusCode == http.StatusUnauthorized, statusCode == http.StatusForbidden:
		return "auth_invalid"
	case statusCode == http.StatusTooManyRequests:
		return "rate_limited"
	case statusCode >= 500:
		return "upstream_error"
	default:
		return "unknown"
	}
}

func museReasonForStatus(statusCode int) string {
	switch museClassification(statusCode) {
	case "rate_limited":
		return "monitoring.muse_inspection_reason_rate_limited"
	case "upstream_error":
		return "monitoring.muse_inspection_reason_upstream_error"
	default:
		return "monitoring.muse_inspection_reason_unknown"
	}
}

func logMuseInspectionResult(
	ctx context.Context,
	logger runLogger,
	item account,
	result model.CodexInspectionResult,
) {
	detail := map[string]any{
		"provider":       "muse",
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
		switch strings.TrimSpace(result.ErrorKind) {
		case "", "billing_healthy":
			level = "info"
		default:
			level = "warning"
		}
	}
	logger.log(ctx, level, "monitoring.muse_inspection_log_server_complete", detail)
}
