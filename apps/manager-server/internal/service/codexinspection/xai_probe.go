package codexinspection

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	xaiBillingWeeklyURL     = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
	xaiBillingMonthlyURL    = "https://cli-chat-proxy.grok.com/v1/billing"
	xaiOfficialAPIMeURL     = "https://api.x.ai/v1/me"
	xaiOfficialAPIBaseURL   = "https://api.x.ai/v1"
	xaiCLIChatProxyBaseURL  = "https://cli-chat-proxy.grok.com/v1"
	xaiGrokVersion          = "0.2.101"
	xaiGrokUserAgent        = "grok-pager/0.2.101 grok-shell/0.2.101 (macos; aarch64)"
	xaiInferenceUserAgent   = model.DefaultXAIInferenceUserAgent
	xaiLegacyBillingTimeout = 3 * time.Second
)

type xaiProbeDecision struct {
	Classification string
	Action         string
	ReasonCode     string
	Reason         string
	IsQuota        bool
	StatusCode     int
	ErrorDetail    string
}

type xaiBillingSummary struct {
	UsagePercent        *float64
	UsedPercent         *float64
	OnDemandUsedPercent *float64
	MonthlyLimitCents   *float64
	UsedCents           *float64
	IncludedUsedCents   *float64
	OnDemandCapCents    *float64
	OnDemandUsedCents   *float64
	HasWeeklyData       bool
	HasMonthlyData      bool
	HasMonthlyLimit     bool
	HasUsed             bool
	HasIncludedUsed     bool
	HasOnDemandCap      bool
	HasOnDemandUsed     bool
	PeriodEnd           string
	BillingPeriodEnd    string
	ProductUsage        []xaiProductUsage
}

type xaiProductUsage struct {
	Product      string
	UsagePercent *float64
}

type xaiBillingProbe struct {
	Summary            *xaiBillingSummary
	Failures           []xaiProbeDecision
	Partial            bool
	Healthy            bool
	OfficialAPIHealthy bool
	StatusCode         int
	OfficialAPIStatus  int
}

type xaiInferenceProbe struct {
	Healthy    bool
	StatusCode int
	Decision   *xaiProbeDecision
}

func (s *Service) inspectSingleXAIAccount(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
	logger runLogger,
) model.CodexInspectionResult {
	base := resultFromAccount(item)
	if item.AuthIndex == "" {
		base.ActionReason = "monitoring.xai_inspection_reason_missing_auth_index"
		base.Error = "missing auth_index"
		base.ErrorKind = "missing_auth_index"
		base.ErrorDetail = "missing auth_index"
		logger.warning(ctx, "monitoring.xai_inspection_log_server_missing_auth_index", map[string]any{
			"provider":         "xai",
			"fileName":         item.FileName,
			"displayAccount":   item.DisplayAccount,
			"inspectionMode":   "skipped",
			"healthEvidence":   base.ErrorKind,
			"billingAvailable": false,
			"billingPartial":   false,
			"inferenceEnabled": settings.XAIInferenceEnabled,
			"action":           base.Action,
		})
		return base
	}

	var billing xaiBillingProbe
	var billingErr error
	for attempt := 0; attempt <= settings.Retries; attempt++ {
		billing, billingErr = s.requestXAIBilling(ctx, setup, settings, item)
		if billingErr == nil && (!xaiProbeShouldRetry(billing) || attempt == settings.Retries) {
			break
		}
	}
	if billingErr != nil {
		billing.Failures = append(billing.Failures, *xaiDecision(0, "upstream_error", billingErr.Error()))
		billing.Partial = true
	}
	if billing.Summary != nil {
		base.UsedPercent = xaiSummaryUsedPercent(billing.Summary)
		base.QuotaWindows = xaiSummaryWindows(billing.Summary)
	}
	if !settings.XAIInferenceEnabled {
		base = resolveXAIBasicInspectionResult(base, billing)
		logXAIInspectionResult(ctx, logger, item, base, billing, nil)
		return base
	}

	inference := s.requestXAIInferenceWithRetries(ctx, setup, settings, item, billing.OfficialAPIHealthy)
	if inference.Healthy {
		base.Action = "keep"
		base.ActionReason = "monitoring.xai_inspection_reason_inference_healthy"
		base.StatusCode = intPointer(inference.StatusCode)
		base.IsQuota = false
		base.AutoRecoverEligible = false
		base.Error = ""
		base.ErrorKind = "inference_healthy"
		base.ErrorDetail = ""
		if base.Disabled && !item.AutoRecoverOwned {
			base.ActionReason = "monitoring.xai_inspection_reason_inference_manual_disable"
		} else if base.Disabled && item.AutoRecoverOwned {
			base.Action = "enable"
			base.ActionReason = "monitoring.xai_inspection_reason_enable_owned"
			base.AutoRecoverEligible = true
		}
	} else {
		decision := inference.Decision
		if decision == nil {
			decision = xaiInferenceDecision(0, "protocol_changed", "xAI inference did not return a completion event")
		}
		if base.Disabled && decision.Action == "disable" {
			decision.Action = "keep"
			decision.Reason = xaiDisabledReasonKey(decision.Classification)
		}
		base.Action = decision.Action
		base.ActionReason = decision.Reason
		base.StatusCode = intPointer(decision.StatusCode)
		base.IsQuota = decision.IsQuota
		base.AutoRecoverEligible = false
		base.Error = decision.ErrorDetail
		base.ErrorKind = decision.Classification
		base.ErrorDetail = decision.ErrorDetail
	}
	logXAIInspectionResult(ctx, logger, item, base, billing, &inference.Healthy)
	return base
}

func logXAIInspectionResult(
	ctx context.Context,
	logger runLogger,
	item account,
	result model.CodexInspectionResult,
	billing xaiBillingProbe,
	inferenceHealthy *bool,
) {
	mode := "billing"
	if inferenceHealthy != nil {
		mode = "inference"
	} else if result.ErrorKind == "official_api_healthy" {
		mode = "identity"
	}
	detail := map[string]any{
		"provider":         "xai",
		"fileName":         item.FileName,
		"displayAccount":   item.DisplayAccount,
		"inspectionMode":   mode,
		"healthEvidence":   result.ErrorKind,
		"billingAvailable": billing.Summary != nil,
		"billingPartial":   billing.Partial,
		"inferenceEnabled": inferenceHealthy != nil,
		"action":           result.Action,
	}
	if result.StatusCode != nil {
		detail["statusCode"] = *result.StatusCode
	}
	if result.UsedPercent != nil {
		detail["usedPercent"] = *result.UsedPercent
	}
	if inferenceHealthy != nil {
		detail["inferenceHealthy"] = *inferenceHealthy
	}
	logger.log(ctx, xaiInspectionLogLevel(result), "monitoring.xai_inspection_log_server_complete", detail)
}

func xaiInspectionLogLevel(result model.CodexInspectionResult) string {
	switch result.Action {
	case "delete", "reauth":
		return "error"
	case "disable":
		return "warning"
	case "enable":
		return "success"
	}
	switch result.ErrorKind {
	case "", "billing_healthy", "official_api_healthy", "inference_healthy":
		return "info"
	default:
		return "warning"
	}
}

func resolveXAIBasicInspectionResult(
	base model.CodexInspectionResult,
	billing xaiBillingProbe,
) model.CodexInspectionResult {
	if billing.Summary != nil {
		if decision, ok := xaiRelevantFailure(billing.Failures, false); ok {
			if base.Disabled && decision.Action == "disable" {
				decision.Action = "keep"
				decision.Reason = xaiDisabledReasonKey(decision.Classification)
			}
			base.Action = decision.Action
			base.ActionReason = decision.Reason
			base.StatusCode = intPointer(decision.StatusCode)
			base.IsQuota = decision.IsQuota
			base.AutoRecoverEligible = false
			base.Error = decision.ErrorDetail
			base.ErrorKind = decision.Classification
			base.ErrorDetail = decision.ErrorDetail
			return base
		}
	}

	if billing.Healthy || billing.OfficialAPIHealthy {
		base.Action = "keep"
		base.ActionReason = "monitoring.xai_inspection_reason_billing_healthy"
		statusCode := billing.StatusCode
		if billing.OfficialAPIHealthy {
			statusCode = billing.OfficialAPIStatus
		}
		base.StatusCode = intPointer(statusCode)
		base.IsQuota = false
		base.AutoRecoverEligible = false
		base.Error = ""
		base.ErrorKind = "billing_healthy"
		if billing.OfficialAPIHealthy && billing.Summary == nil {
			base.ActionReason = "monitoring.xai_inspection_reason_official_api_healthy"
			if base.Disabled {
				base.ActionReason = "monitoring.xai_inspection_reason_official_api_manual_disable"
			}
			base.ErrorKind = "official_api_healthy"
		} else if billing.Partial {
			base.ActionReason = "monitoring.xai_inspection_reason_billing_partial"
			base.ErrorKind = "billing_partial"
		}
		base.ErrorDetail = ""
		return base
	}

	decision, ok := xaiRelevantFailure(billing.Failures, true)
	if !ok {
		decision = *xaiDecision(0, "upstream_error", "xAI billing returned no usable health evidence")
	}
	if base.Disabled && decision.Action == "disable" {
		decision.Action = "keep"
		decision.Reason = xaiDisabledReasonKey(decision.Classification)
	}
	base.Action = decision.Action
	base.ActionReason = decision.Reason
	base.StatusCode = intPointer(decision.StatusCode)
	base.IsQuota = decision.IsQuota
	base.AutoRecoverEligible = false
	base.Error = decision.ErrorDetail
	base.ErrorKind = decision.Classification
	base.ErrorDetail = decision.ErrorDetail
	return base
}

func (s *Service) requestXAIInferenceWithRetries(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
	forceOfficialAPI bool,
) xaiInferenceProbe {
	var probe xaiInferenceProbe
	for attempt := 0; attempt <= settings.Retries; attempt++ {
		probe = s.requestXAIInference(ctx, setup, settings, item, forceOfficialAPI)
		if probe.Healthy || probe.Decision == nil || !xaiInferenceShouldRetry(*probe.Decision) {
			break
		}
	}
	return probe
}

func (s *Service) requestXAIInference(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
	forceOfficialAPI bool,
) xaiInferenceProbe {
	targetURL, usesCLIChatProxy := resolveXAIInferenceURL(item.File, forceOfficialAPI)
	header := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Accept":        "application/json",
		"Content-Type":  "application/json",
		"User-Agent":    firstNonEmpty(settings.XAIInferenceUserAgent, xaiInferenceUserAgent),
	}
	if usesCLIChatProxy {
		header["x-xai-token-auth"] = "xai-grok-cli"
		header["x-grok-client-version"] = xaiGrokVersion
		if userID := resolveXAIUserID(item.File); userID != "" {
			header["x-userid"] = userID
		}
	}

	data, err := json.Marshal(map[string]any{
		"model":  settings.XAIInferenceModel,
		"input":  settings.XAIInferencePrompt,
		"stream": false,
	})
	if err != nil {
		return xaiInferenceProbe{Decision: xaiInferenceDecision(0, "upstream_error", err.Error())}
	}

	response, _, err := s.requestProviderAPICallAt(
		ctx,
		setup,
		settings,
		item,
		http.MethodPost,
		targetURL,
		header,
		string(data),
	)
	if err != nil {
		return xaiInferenceProbe{Decision: xaiInferenceDecision(0, "upstream_error", err.Error())}
	}
	if !response.HasStatusCode {
		return xaiInferenceProbe{Decision: xaiInferenceDecision(0, "protocol_changed", "xAI inference response missing status_code")}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return xaiInferenceProbe{Decision: xaiInferenceDecision(
			response.StatusCode,
			xaiClassification(response.StatusCode, response.Body),
			firstNonEmpty(response.BodyText, fmt.Sprint(response.Body)),
		)}
	}
	if healthy, detail := hasCompletedXAIInferenceOutput(response.Body, response.BodyText); !healthy {
		classification := xaiClassification(response.StatusCode, response.Body)
		if classification == "unknown" {
			classification = "protocol_changed"
		}
		return xaiInferenceProbe{Decision: xaiInferenceDecision(response.StatusCode, classification, detail)}
	}
	return xaiInferenceProbe{Healthy: true, StatusCode: response.StatusCode}
}

func hasCompletedXAIInferenceOutput(body any, bodyText string) (bool, string) {
	payload := parseRecord(body)
	if payload == nil {
		payload = parseRecord(bodyText)
	}
	if payload == nil {
		return false, "xAI inference response schema changed"
	}
	if status := strings.ToLower(readString(payload, "status")); status != "completed" {
		if status == "" {
			return false, "xAI inference response missing completed status"
		}
		return false, fmt.Sprintf("xAI inference response status is %s", status)
	}
	if errorValue, ok := payload["error"]; ok && errorValue != nil {
		return false, firstNonEmpty(fmt.Sprint(errorValue), "xAI inference response contains an error")
	}
	for _, rawOutput := range readXAIArray(payload, "output") {
		output := toMap(rawOutput)
		if output == nil || !strings.EqualFold(readString(output, "type"), "message") {
			continue
		}
		for _, rawContent := range readXAIArray(output, "content") {
			content := toMap(rawContent)
			if content == nil || !strings.EqualFold(readString(content, "type"), "output_text") {
				continue
			}
			if strings.TrimSpace(readString(content, "text")) != "" {
				return true, ""
			}
		}
	}
	return false, "xAI inference completed without output text"
}

func xaiInferenceShouldRetry(decision xaiProbeDecision) bool {
	switch decision.Classification {
	case "upstream_error", "rate_limited", "probe_invalid", "model_unavailable", "protocol_changed":
		return true
	default:
		return false
	}
}

func resolveXAIInferenceURL(file authFile, forceOfficialAPI bool) (string, bool) {
	if forceOfficialAPI {
		return xaiOfficialAPIBaseURL + "/responses", false
	}
	baseURL := strings.TrimSuffix(strings.TrimSpace(readXAIAuthString(file, "base_url", "baseUrl")), "/")
	usingAPI, hasUsingAPI := readXAIAuthBool(file, "using_api", "usingApi")
	authKind := strings.ToLower(readXAIAuthString(file, "auth_kind", "authKind"))
	if !hasUsingAPI {
		// xAI OAuth/CLI credentials may omit auth_kind and using_api from the
		// management auth-files listing. Default ambiguous credentials to the
		// CLI proxy; API credentials must opt in explicitly with api_key or
		// using_api=true.
		usingAPI = authKind != "" && !strings.EqualFold(authKind, "oauth")
	}
	if !usingAPI && (baseURL == "" || sameXAIBaseURL(baseURL, xaiOfficialAPIBaseURL)) {
		return xaiCLIChatProxyBaseURL + "/responses", true
	}
	if baseURL == "" {
		baseURL = xaiOfficialAPIBaseURL
	}
	return baseURL + "/responses", sameXAIBaseURL(baseURL, xaiCLIChatProxyBaseURL)
}

func sameXAIBaseURL(left, right string) bool {
	return strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(left), "/"), strings.TrimSuffix(strings.TrimSpace(right), "/"))
}

func readXAIAuthString(file authFile, keys ...string) string {
	metadata := readMap(file, "metadata")
	attributes := readMap(file, "attributes")
	for _, record := range []map[string]any{file, metadata, attributes} {
		if value := readString(record, keys...); value != "" {
			return value
		}
	}
	return ""
}

func readXAIAuthBool(file authFile, keys ...string) (bool, bool) {
	metadata := readMap(file, "metadata")
	attributes := readMap(file, "attributes")
	for _, record := range []map[string]any{file, metadata, attributes} {
		for _, key := range keys {
			value, ok := record[key]
			if !ok || value == nil {
				continue
			}
			switch typed := value.(type) {
			case bool:
				return typed, true
			case string:
				switch strings.ToLower(strings.TrimSpace(typed)) {
				case "true":
					return true, true
				case "false":
					return false, true
				}
			}
		}
	}
	return false, false
}

func (s *Service) requestXAIBilling(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
) (xaiBillingProbe, error) {
	header := map[string]string{
		"Authorization":         "Bearer $TOKEN$",
		"x-xai-token-auth":      "xai-grok-cli",
		"x-grok-client-version": xaiGrokVersion,
		"User-Agent":            xaiGrokUserAgent,
		"Accept":                "*/*",
	}
	if userID := resolveXAIUserID(item.File); userID != "" {
		header["x-userid"] = userID
	}

	var weekly xaiBillingResult
	var monthly xaiBillingResult
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		result, err := s.requestProviderBilling(ctx, setup, settings, item, xaiBillingWeeklyURL, header)
		if err != nil {
			result = xaiBillingResult{Failure: xaiDecision(0, "upstream_error", err.Error())}
		}
		weekly = result
	}()
	go func() {
		defer wait.Done()
		legacyTimeout := xaiLegacyBillingTimeout
		if settings.Timeout > 0 {
			configuredTimeout := time.Duration(settings.Timeout) * time.Millisecond
			if configuredTimeout < legacyTimeout {
				legacyTimeout = configuredTimeout
			}
		}
		legacyCtx, cancel := context.WithTimeout(ctx, legacyTimeout)
		defer cancel()
		result, err := s.requestProviderBilling(legacyCtx, setup, settings, item, xaiBillingMonthlyURL, header)
		if err != nil {
			result = xaiBillingResult{Failure: xaiDecision(0, "upstream_error", err.Error())}
		}
		monthly = result
	}()
	wait.Wait()

	probe := xaiBillingProbe{}
	if weekly.Summary != nil {
		probe.Summary = mergeXAIBillingSummary(weekly.Summary, monthly.Summary)
		probe.StatusCode = weekly.StatusCode
		probe.Healthy = true
		// The deprecated monthly endpoint is optional once unified weekly billing is healthy.
		// Its absence or denial must not downgrade health or drive an automatic action.
		return probe, nil
	}
	if monthly.Summary != nil {
		probe.Summary = monthly.Summary
		probe.StatusCode = monthly.StatusCode
		probe.Partial = true
		probe.Healthy = true
		if weekly.Failure != nil {
			probe.Failures = append(probe.Failures, *weekly.Failure)
		}
		return probe, nil
	}
	if weekly.Failure != nil {
		probe.Failures = append(probe.Failures, *weekly.Failure)
	} else if monthly.Failure != nil {
		probe.Failures = append(probe.Failures, *monthly.Failure)
	}
	if probe.Summary == nil {
		if xaiOfficialAPIFallbackEligible(probe.Failures) {
			healthy, statusCode, failure, healthErr := s.requestXAIOfficialAPIHealth(ctx, setup, settings, item)
			if healthErr != nil {
				probe.Failures = append(probe.Failures, *xaiDecision(0, "upstream_error", healthErr.Error()))
			} else if failure != nil {
				probe.Failures = append(probe.Failures, *failure)
			} else if healthy {
				probe.OfficialAPIHealthy = true
				probe.OfficialAPIStatus = statusCode
				return probe, nil
			}
		}
		if len(probe.Failures) > 0 {
			return probe, nil
		}
		return probe, fmt.Errorf("xAI billing returned no usable data")
	}
	return probe, nil
}

func xaiOfficialAPIFallbackEligible(failures []xaiProbeDecision) bool {
	if len(failures) == 0 {
		return false
	}
	for _, failure := range failures {
		if failure.Classification != "permission_unknown" {
			return false
		}
	}
	return true
}

func (s *Service) requestXAIOfficialAPIHealth(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
) (bool, int, *xaiProbeDecision, error) {
	header := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Accept":        "application/json",
	}
	response, _, err := s.requestProviderBillingAt(ctx, setup, settings, item, xaiOfficialAPIMeURL, header)
	if err != nil {
		return false, 0, nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, response.StatusCode, xaiDecision(
			response.StatusCode,
			xaiClassification(response.StatusCode, response.Body),
			fmt.Sprint(response.Body),
		), nil
	}

	payload := parseRecord(response.Body)
	if payload == nil {
		payload = parseRecord(response.BodyText)
	}
	if payload == nil {
		return false, response.StatusCode, xaiDecision(response.StatusCode, "protocol_changed", "xAI official API identity response schema changed"), nil
	}
	blocked, hasTeamBlocked := readXAIBoolPtr(payload, "team_blocked", "teamBlocked")
	if hasTeamBlocked && blocked != nil && *blocked {
		return false, response.StatusCode, xaiDecision(http.StatusForbidden, "spending_limit", "xAI official API team is blocked"), nil
	}
	userID := readXAIIdentityID(payload, "user_id", "userId")
	teamID := readXAIIdentityID(payload, "team_id", "teamId")
	if userID == "" && teamID == "" && !hasTeamBlocked {
		return false, response.StatusCode, xaiDecision(response.StatusCode, "protocol_changed", "xAI official API identity response schema changed"), nil
	}
	return true, response.StatusCode, nil, nil
}

func readXAIIdentityID(record map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := record[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			if normalized := strings.TrimSpace(typed); normalized != "" {
				return normalized
			}
		case float64:
			if !math.IsNaN(typed) && !math.IsInf(typed, 0) {
				return fmt.Sprint(typed)
			}
		case int:
			return fmt.Sprint(typed)
		case int64:
			return fmt.Sprint(typed)
		}
	}
	return ""
}

func readXAIBoolPtr(record map[string]any, keys ...string) (*bool, bool) {
	for _, key := range keys {
		value, ok := record[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return &typed, true
		case string:
			switch strings.ToLower(strings.TrimSpace(typed)) {
			case "true":
				result := true
				return &result, true
			case "false":
				result := false
				return &result, true
			}
		}
	}
	return nil, false
}

func mergeXAIBillingSummary(primary, fallback *xaiBillingSummary) *xaiBillingSummary {
	if primary == nil {
		return fallback
	}
	if fallback == nil {
		return primary
	}
	merged := *primary
	primaryHasWeeklyData := primary.HasWeeklyData || primary.UsagePercent != nil || len(primary.ProductUsage) > 0
	if !primaryHasWeeklyData {
		merged.UsagePercent = fallback.UsagePercent
		merged.PeriodEnd = fallback.PeriodEnd
		merged.ProductUsage = fallback.ProductUsage
	}
	primaryMonthlyLimit := xaiEvidenceFloat(primary.HasMonthlyLimit, primary.MonthlyLimitCents)
	fallbackMonthlyLimit := xaiEvidenceFloat(fallback.HasMonthlyLimit, fallback.MonthlyLimitCents)
	merged.MonthlyLimitCents = firstXAIFloat(primaryMonthlyLimit, fallbackMonthlyLimit)
	primaryRawUsed := xaiEvidenceFloat(primary.HasUsed, primary.UsedCents)
	fallbackRawUsed := xaiEvidenceFloat(fallback.HasUsed, fallback.UsedCents)
	merged.HasIncludedUsed = primary.HasIncludedUsed
	merged.IncludedUsedCents = xaiEvidenceFloat(primary.HasIncludedUsed, primary.IncludedUsedCents)
	if merged.IncludedUsedCents == nil && primaryRawUsed != nil {
		merged.IncludedUsedCents = primaryRawUsed
	}
	if merged.IncludedUsedCents == nil && fallback.HasIncludedUsed {
		merged.IncludedUsedCents = fallback.IncludedUsedCents
		merged.HasIncludedUsed = merged.IncludedUsedCents != nil
	}
	if merged.IncludedUsedCents == nil && fallbackRawUsed != nil {
		merged.IncludedUsedCents = fallbackRawUsed
	}
	if merged.IncludedUsedCents != nil && merged.MonthlyLimitCents != nil && *merged.MonthlyLimitCents > 0 {
		includedUsed := math.Min(*merged.IncludedUsedCents, *merged.MonthlyLimitCents)
		merged.IncludedUsedCents = &includedUsed
	}

	primaryOnDemandCap := xaiEvidenceFloat(primary.HasOnDemandCap, primary.OnDemandCapCents)
	fallbackOnDemandCap := xaiEvidenceFloat(fallback.HasOnDemandCap, fallback.OnDemandCapCents)
	merged.OnDemandCapCents = firstXAIFloat(primaryOnDemandCap, fallbackOnDemandCap)
	merged.HasOnDemandUsed = primary.HasOnDemandUsed
	merged.OnDemandUsedCents = xaiEvidenceFloat(primary.HasOnDemandUsed, primary.OnDemandUsedCents)
	if merged.OnDemandUsedCents == nil && primaryRawUsed != nil && merged.MonthlyLimitCents != nil {
		onDemandUsed := math.Max(0, *primaryRawUsed-*merged.MonthlyLimitCents)
		merged.OnDemandUsedCents = &onDemandUsed
	}
	if merged.OnDemandUsedCents == nil && fallback.HasOnDemandUsed {
		merged.OnDemandUsedCents = fallback.OnDemandUsedCents
		merged.HasOnDemandUsed = merged.OnDemandUsedCents != nil
	}
	if merged.OnDemandUsedCents == nil && fallbackRawUsed != nil && merged.MonthlyLimitCents != nil {
		onDemandUsed := math.Max(0, *fallbackRawUsed-*merged.MonthlyLimitCents)
		merged.OnDemandUsedCents = &onDemandUsed
	}
	hasPrimaryComponentEvidence := primary.HasIncludedUsed || primary.HasOnDemandUsed
	if primaryRawUsed != nil {
		merged.UsedCents = primaryRawUsed
	} else if hasPrimaryComponentEvidence && merged.IncludedUsedCents != nil && merged.OnDemandUsedCents != nil {
		used := *merged.IncludedUsedCents + *merged.OnDemandUsedCents
		merged.UsedCents = &used
	} else if hasPrimaryComponentEvidence {
		merged.UsedCents = firstXAIFloat(merged.IncludedUsedCents, merged.OnDemandUsedCents)
	} else if fallbackRawUsed != nil {
		merged.UsedCents = fallbackRawUsed
	} else if merged.IncludedUsedCents != nil && merged.OnDemandUsedCents != nil {
		used := *merged.IncludedUsedCents + *merged.OnDemandUsedCents
		merged.UsedCents = &used
	} else {
		merged.UsedCents = firstXAIFloat(merged.IncludedUsedCents, merged.OnDemandUsedCents)
	}
	merged.UsedPercent = firstXAIFloat(primary.UsedPercent, fallback.UsedPercent)
	if merged.MonthlyLimitCents != nil && *merged.MonthlyLimitCents > 0 && merged.IncludedUsedCents != nil {
		usedPercent := (*merged.IncludedUsedCents / *merged.MonthlyLimitCents) * 100
		merged.UsedPercent = &usedPercent
	}
	merged.OnDemandUsedPercent = firstXAIFloat(primary.OnDemandUsedPercent, fallback.OnDemandUsedPercent)
	if merged.OnDemandCapCents != nil && *merged.OnDemandCapCents > 0 && merged.OnDemandUsedCents != nil {
		onDemandUsedPercent := (*merged.OnDemandUsedCents / *merged.OnDemandCapCents) * 100
		merged.OnDemandUsedPercent = &onDemandUsedPercent
	}
	merged.HasMonthlyData = primary.HasMonthlyData || fallback.HasMonthlyData ||
		merged.MonthlyLimitCents != nil || merged.IncludedUsedCents != nil || merged.UsedPercent != nil
	merged.HasMonthlyLimit = primary.HasMonthlyLimit || fallback.HasMonthlyLimit
	merged.HasUsed = primary.HasUsed || (!hasPrimaryComponentEvidence && fallback.HasUsed)
	merged.HasOnDemandCap = primary.HasOnDemandCap || fallback.HasOnDemandCap
	merged.BillingPeriodEnd = firstNonEmpty(primary.BillingPeriodEnd, fallback.BillingPeriodEnd)
	merged.HasWeeklyData = primaryHasWeeklyData || fallback.HasWeeklyData
	return &merged
}

func firstXAIFloat(values ...*float64) *float64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func xaiEvidenceFloat(hasEvidence bool, value *float64) *float64 {
	if !hasEvidence {
		return nil
	}
	return value
}

func positiveXAIFloat(value *float64) bool {
	return value != nil && *value > 0
}

type xaiBillingResult struct {
	Summary    *xaiBillingSummary
	Failure    *xaiProbeDecision
	Partial    bool
	StatusCode int
}

func (s *Service) requestProviderBilling(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
	targetURL string,
	header map[string]string,
) (xaiBillingResult, error) {
	response, _, err := s.requestProviderBillingAt(ctx, setup, settings, item, targetURL, header)
	if err != nil {
		return xaiBillingResult{}, err
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		payload := parseRecord(response.Body)
		if payload == nil {
			payload = parseRecord(response.BodyText)
		}
		config := readMap(payload, "config")
		if config == nil {
			config = payload
		}
		summary := parseXAIBillingSummary(config)
		if summary == nil {
			return xaiBillingResult{Failure: xaiDecision(response.StatusCode, "protocol_changed", "xAI billing response schema changed")}, nil
		}
		return xaiBillingResult{Summary: summary, StatusCode: response.StatusCode}, nil
	}
	return xaiBillingResult{Failure: xaiDecision(
		response.StatusCode,
		xaiClassification(response.StatusCode, response.Body),
		fmt.Sprint(response.Body),
	)}, nil
}

func (s *Service) requestProviderBillingAt(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
	targetURL string,
	header map[string]string,
) (apiCallResponse, int, error) {
	return s.requestProviderAPICallAt(
		ctx,
		setup,
		settings,
		item,
		http.MethodGet,
		targetURL,
		header,
		"",
	)
}

func (s *Service) requestProviderAPICallAt(
	ctx context.Context,
	setup store.Setup,
	settings model.ManagerCodexInspectionConfig,
	item account,
	method string,
	targetURL string,
	header map[string]string,
	requestData string,
) (apiCallResponse, int, error) {
	if strings.TrimSpace(method) == "" {
		method = http.MethodGet
	}
	payload := map[string]any{
		"authIndex": item.AuthIndex,
		"method":    method,
		"url":       targetURL,
		"header":    header,
	}
	if requestData != "" {
		payload["data"] = requestData
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return apiCallResponse{}, 0, err
	}
	requestCtx := ctx
	cancel := func() {}
	if settings.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, time.Duration(settings.Timeout)*time.Millisecond)
	}
	defer cancel()
	req, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		cpa.NormalizeBaseURL(setup.CPAUpstreamURL)+"/v0/management/api-call",
		strings.NewReader(string(data)),
	)
	if err != nil {
		return apiCallResponse{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+setup.ManagementKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return apiCallResponse{}, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, maxStoredBodyText))
		return apiCallResponse{}, res.StatusCode, fmt.Errorf("api-call failed: %s %s", res.Status, truncate(string(body), maxStoredBodyText))
	}
	var raw map[string]any
	if err := decodeCPAAPICallResponse(res.Body, maxCPAAPICallResponseSize, &raw); err != nil {
		return apiCallResponse{}, res.StatusCode, err
	}
	statusRaw, hasStatus := firstValue(raw, "status_code", "statusCode")
	bodyRaw, _ := firstValue(raw, "body")
	bodyText, bodyValue := normalizeBody(bodyRaw)
	return apiCallResponse{
		StatusCode:    int(readFloat(statusRaw, 0)),
		HasStatusCode: hasStatus && strings.TrimSpace(fmt.Sprint(statusRaw)) != "",
		BodyText:      bodyText,
		Body:          bodyValue,
	}, res.StatusCode, nil
}

func resolveXAIUserID(file authFile) string {
	metadata := readMap(file, "metadata")
	attributes := readMap(file, "attributes")
	user := readMap(file, "user")
	for _, value := range []any{
		file["sub"], file["subject"], file["user_id"], file["userId"],
		metadata["sub"], metadata["subject"], metadata["user_id"], metadata["userId"],
		attributes["sub"], attributes["subject"], attributes["user_id"], attributes["userId"],
		user["sub"], user["id"],
	} {
		if result := strings.TrimSpace(fmt.Sprint(value)); result != "" && result != "<nil>" {
			return result
		}
	}
	return ""
}

func parseXAIBillingSummary(config map[string]any) *xaiBillingSummary {
	if config == nil {
		return nil
	}
	period := readMap(config, "current_period", "currentPeriod")
	periodType := strings.ToLower(readString(period, "type"))
	periodEnd := readString(period, "end")
	usage := readNullableFloat(config, "credit_usage_percent", "creditUsagePercent")
	monthlyLimit, hasMonthlyLimitEvidence := readXAICentFloatWithEvidence(config, "monthly_limit", "monthlyLimit")
	nestedUsage := readMap(config, "usage")
	nestedIncludedUsed, hasNestedIncludedEvidence := readXAICentFloatWithEvidence(nestedUsage, "included_used", "includedUsed")
	explicitOnDemandUsed, hasOnDemandUsedEvidence := readXAICentFloatWithEvidence(config, "on_demand_used", "onDemandUsed")
	if !hasOnDemandUsedEvidence {
		explicitOnDemandUsed, hasOnDemandUsedEvidence = readXAICentFloatWithEvidence(nestedUsage, "on_demand_used", "onDemandUsed")
	}
	used, hasUsedEvidence := readXAICentFloatWithEvidence(config, "used")
	if !hasUsedEvidence {
		used, hasUsedEvidence = readXAICentFloatWithEvidence(nestedUsage, "total_used", "totalUsed")
	}
	if used == nil && (nestedIncludedUsed != nil || explicitOnDemandUsed != nil) {
		value := float64(0)
		if nestedIncludedUsed != nil {
			value += *nestedIncludedUsed
		}
		if explicitOnDemandUsed != nil {
			value += *explicitOnDemandUsed
		}
		used = &value
	}
	onDemandCap, hasOnDemandCapEvidence := readXAICentFloatWithEvidence(config, "on_demand_cap", "onDemandCap")
	onDemandUsed := explicitOnDemandUsed
	hasMonthlyData := hasMonthlyLimitEvidence || hasUsedEvidence || hasNestedIncludedEvidence
	includedUsed := (*float64)(nil)
	if hasMonthlyData {
		includedUsed = nestedIncludedUsed
		if includedUsed == nil {
			includedUsed = used
		}
		if includedUsed != nil && monthlyLimit != nil && *monthlyLimit > 0 {
			value := math.Min(*includedUsed, *monthlyLimit)
			includedUsed = &value
		}
	}
	if onDemandUsed == nil && used != nil && monthlyLimit != nil {
		value := math.Max(0, *used-*monthlyLimit)
		onDemandUsed = &value
	}
	monthlyUsed := (*float64)(nil)
	if monthlyLimit != nil && *monthlyLimit > 0 && includedUsed != nil {
		value := (*includedUsed / *monthlyLimit) * 100
		monthlyUsed = &value
	}
	onDemandUsedPercent := (*float64)(nil)
	if onDemandCap != nil && *onDemandCap > 0 && onDemandUsed != nil {
		value := (*onDemandUsed / *onDemandCap) * 100
		onDemandUsedPercent = &value
	}
	hasOnDemandData := hasOnDemandCapEvidence || hasOnDemandUsedEvidence || positiveXAIFloat(onDemandUsed)
	hasMeaningfulOnDemandData := positiveXAIFloat(onDemandCap) || positiveXAIFloat(onDemandUsed)
	hasBillingPeriodData := hasMonthlyData || hasMeaningfulOnDemandData
	if !hasMonthlyData {
		monthlyLimit = nil
		includedUsed = nil
	}
	if !hasMonthlyData && !hasOnDemandData {
		used = nil
	}
	if !hasOnDemandData {
		onDemandCap = nil
		onDemandUsed = nil
		onDemandUsedPercent = nil
	}
	productUsage := make([]xaiProductUsage, 0)
	if items := readXAIArray(config, "product_usage", "productUsage"); len(items) > 0 {
		for _, raw := range items {
			item := toMap(raw)
			if item == nil {
				continue
			}
			productUsage = append(productUsage, xaiProductUsage{
				Product:      firstNonEmpty(readString(item, "product"), "Product"),
				UsagePercent: readNullableFloat(item, "usage_percent", "usagePercent"),
			})
		}
	}
	billingCycle := readMap(config, "billing_cycle", "billingCycle")
	billingPeriodEnd := ""
	if hasBillingPeriodData {
		billingPeriodEnd = firstNonEmpty(
			readString(config, "billing_period_end", "billingPeriodEnd"),
			readString(billingCycle, "billing_period_end", "billingPeriodEnd"),
		)
	}
	hasWeeklyData := usage != nil || len(productUsage) > 0 || strings.Contains(periodType, "weekly")
	if !hasWeeklyData && !hasMonthlyData && !hasOnDemandData {
		return nil
	}
	return &xaiBillingSummary{
		UsagePercent:        usage,
		UsedPercent:         monthlyUsed,
		OnDemandUsedPercent: onDemandUsedPercent,
		MonthlyLimitCents:   monthlyLimit,
		UsedCents:           used,
		IncludedUsedCents:   includedUsed,
		OnDemandCapCents:    onDemandCap,
		OnDemandUsedCents:   onDemandUsed,
		HasWeeklyData:       hasWeeklyData,
		HasMonthlyData:      hasMonthlyData,
		HasMonthlyLimit:     hasMonthlyLimitEvidence,
		HasUsed:             hasUsedEvidence,
		HasIncludedUsed:     hasNestedIncludedEvidence,
		HasOnDemandCap:      hasOnDemandCapEvidence,
		HasOnDemandUsed:     hasOnDemandUsedEvidence,
		PeriodEnd:           periodEnd,
		BillingPeriodEnd:    billingPeriodEnd,
		ProductUsage:        productUsage,
	}
}

func xaiSummaryUsedPercent(summary *xaiBillingSummary) *float64 {
	if summary == nil {
		return nil
	}
	var values []float64
	for _, value := range []*float64{summary.UsagePercent, summary.UsedPercent, summary.OnDemandUsedPercent} {
		if value != nil {
			values = append(values, *value)
		}
	}
	for _, item := range summary.ProductUsage {
		if item.UsagePercent != nil {
			values = append(values, *item.UsagePercent)
		}
	}
	if len(values) == 0 {
		return nil
	}
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return &result
}

func xaiSummaryWindows(summary *xaiBillingSummary) []model.CodexInspectionQuotaWindow {
	if summary == nil {
		return nil
	}
	windows := make([]model.CodexInspectionQuotaWindow, 0)
	if summary.HasWeeklyData {
		windows = append(windows, model.CodexInspectionQuotaWindow{ID: "xai-weekly", LabelKey: "xai_quota.weekly_limit", UsedPercent: summary.UsagePercent, ResetLabel: summary.PeriodEnd})
	}
	if summary.UsedPercent != nil || positiveXAIFloat(summary.MonthlyLimitCents) {
		windows = append(windows, model.CodexInspectionQuotaWindow{ID: "xai-monthly", LabelKey: "xai_quota.monthly_limit", UsedPercent: summary.UsedPercent, ResetLabel: summary.BillingPeriodEnd})
	}
	if summary.OnDemandUsedPercent != nil || (summary.OnDemandCapCents != nil && *summary.OnDemandCapCents > 0) {
		windows = append(windows, model.CodexInspectionQuotaWindow{ID: "xai-on-demand", LabelKey: "xai_quota.on_demand_cap", UsedPercent: summary.OnDemandUsedPercent, ResetLabel: summary.BillingPeriodEnd})
	}
	for index, item := range summary.ProductUsage {
		windows = append(windows, model.CodexInspectionQuotaWindow{ID: fmt.Sprintf("xai-product-%d", index), LabelKey: "xai_quota.product_usage", LabelParams: map[string]any{"product": item.Product}, UsedPercent: item.UsagePercent, ResetLabel: summary.PeriodEnd})
	}
	return windows
}

func xaiRelevantFailure(failures []xaiProbeDecision, includeNonBlocking bool) (xaiProbeDecision, bool) {
	var selected xaiProbeDecision
	selectedPriority := -1
	for _, failure := range failures {
		if !includeNonBlocking && !xaiFailureIsBlocking(failure.Classification) {
			continue
		}
		priority := xaiFailurePriority(failure.Classification)
		if priority > selectedPriority {
			selected = failure
			selectedPriority = priority
		}
	}
	return selected, selectedPriority >= 0
}

func xaiFailureIsBlocking(classification string) bool {
	switch classification {
	case "upstream_error", "rate_limited", "probe_invalid", "model_unavailable", "protocol_changed":
		return false
	default:
		return true
	}
}

func xaiFailurePriority(classification string) int {
	switch classification {
	case "auth_invalid":
		return 100
	case "free_quota_exhausted", "spending_limit":
		return 90
	case "entitlement_denied":
		return 85
	case "client_outdated":
		return 80
	case "permission_unknown", "quota_or_entitlement_unknown":
		return 70
	case "policy_denied":
		return 60
	case "rate_limited":
		return 40
	case "probe_invalid":
		return 30
	case "upstream_error":
		return 10
	default:
		return 1
	}
}

func xaiProbeShouldRetry(probe xaiBillingProbe) bool {
	if probe.Summary != nil {
		return false
	}
	failure, ok := xaiRelevantFailure(probe.Failures, true)
	if !ok {
		return false
	}
	switch failure.Classification {
	case "upstream_error", "rate_limited", "probe_invalid", "model_unavailable", "protocol_changed":
		return true
	default:
		return false
	}
}

func xaiFailureDetails(failures []xaiProbeDecision) string {
	parts := make([]string, 0, len(failures))
	for _, failure := range failures {
		part := failure.Classification
		if failure.StatusCode > 0 {
			part = fmt.Sprintf("%s (HTTP %d)", part, failure.StatusCode)
		}
		if detail := strings.TrimSpace(failure.ErrorDetail); detail != "" {
			part += ": " + detail
		}
		parts = append(parts, part)
	}
	return truncate(strings.Join(parts, " · "), maxStoredBodyText)
}

func xaiDecision(status int, classification string, detail string) *xaiProbeDecision {
	action := "keep"
	isQuota := false
	switch classification {
	case "free_quota_exhausted", "spending_limit", "entitlement_denied":
		action = "disable"
		isQuota = true
	case "auth_invalid":
		action = "reauth"
	}
	return &xaiProbeDecision{Classification: classification, Action: action, ReasonCode: xaiReasonCode(status, classification), Reason: xaiReason(classification), IsQuota: isQuota, StatusCode: status, ErrorDetail: truncate(detail, maxStoredBodyText)}
}

func xaiInferenceDecision(status int, classification string, detail string) *xaiProbeDecision {
	decision := xaiDecision(status, classification, detail)
	decision.Reason = xaiInferenceReason(classification)
	return decision
}

func xaiClassification(status int, body any) string {
	blob := strings.ToLower(fmt.Sprint(body))
	switch {
	case containsAny(blob, "subscription:free-usage-exhausted", "free-usage-exhausted", "included free usage"):
		return "free_quota_exhausted"
	case containsAny(blob, "personal-team-blocked:spending-limit", "spending-limit", "run out of credits", "used all available credits", "monthly spending limit", "purchase more credits", "add credits"):
		return "spending_limit"
	case containsAny(blob, "invalid_grant", "refresh_token_reused", "invalid_refresh_token", "token_invalidated", "token_revoked", "refresh token has been revoked", "bad-credentials", "unauthenticated:bad-credentials", "invalid or expired credentials", "authentication token has been invalidated"), status == http.StatusUnauthorized:
		return "auth_invalid"
	case status == http.StatusUpgradeRequired:
		return "client_outdated"
	case containsAny(blob, "content violates usage guidelines", "usage guideline violation", "safety_check", "safety check", "policy violation"):
		return "policy_denied"
	case containsAny(blob, "need a grok subscription", "do not have an active grok subscription", "no active grok subscription", "not entitled", "not authorized for xai api access", "tier denied", "subscription required", "access to the chat endpoint is denied"):
		return "entitlement_denied"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status == http.StatusForbidden:
		return "permission_unknown"
	case status == http.StatusPaymentRequired:
		return "quota_or_entitlement_unknown"
	case status == http.StatusBadRequest, status == http.StatusUnprocessableEntity:
		return "probe_invalid"
	case status == http.StatusNotFound:
		return "model_unavailable"
	case status == 0:
		return "upstream_error"
	case status >= 500:
		return "upstream_error"
	default:
		return "unknown"
	}
}

func xaiReasonCode(status int, classification string) string {
	switch classification {
	case "free_quota_exhausted":
		return "xai_free_usage_exhausted"
	case "spending_limit":
		return "xai_spending_limit"
	case "auth_invalid":
		if status == http.StatusUnauthorized {
			return "xai_http_401"
		}
		return "xai_auth_invalid"
	case "client_outdated":
		return "xai_client_outdated"
	case "policy_denied":
		return "xai_policy_denied"
	case "entitlement_denied":
		return "xai_entitlement_denied"
	case "rate_limited":
		return "xai_rate_limited"
	case "permission_unknown":
		return "xai_permission_unknown"
	case "quota_or_entitlement_unknown":
		return "xai_http_402_unknown"
	case "probe_invalid":
		return fmt.Sprintf("xai_http_%d", status)
	case "model_unavailable":
		return "xai_model_or_endpoint_unavailable"
	case "upstream_error":
		return fmt.Sprintf("xai_http_%d", status)
	case "protocol_changed":
		return "xai_empty_or_changed_payload"
	default:
		return "xai_unknown_error"
	}
}

func containsAny(text string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func xaiReason(classification string) string {
	switch classification {
	case "free_quota_exhausted":
		return "monitoring.xai_inspection_reason_free_quota_disable"
	case "spending_limit":
		return "monitoring.xai_inspection_reason_spending_limit_disable"
	case "auth_invalid":
		return "monitoring.xai_inspection_reason_auth_invalid"
	case "entitlement_denied":
		return "monitoring.xai_inspection_reason_entitlement_disable"
	case "policy_denied":
		return "monitoring.xai_inspection_reason_policy_denied"
	case "permission_unknown":
		return "monitoring.xai_inspection_reason_permission_unknown"
	case "quota_or_entitlement_unknown":
		return "monitoring.xai_inspection_reason_quota_unknown"
	case "rate_limited":
		return "monitoring.xai_inspection_reason_rate_limited"
	case "client_outdated":
		return "monitoring.xai_inspection_reason_client_outdated"
	case "probe_invalid":
		return "monitoring.xai_inspection_reason_probe_invalid"
	case "model_unavailable":
		return "monitoring.xai_inspection_reason_model_unavailable"
	case "upstream_error":
		return "monitoring.xai_inspection_reason_upstream_error"
	case "protocol_changed":
		return "monitoring.xai_inspection_reason_protocol_changed"
	default:
		return "monitoring.xai_inspection_reason_unknown"
	}
}

func xaiInferenceReason(classification string) string {
	switch classification {
	case "quota_or_entitlement_unknown":
		return "monitoring.xai_inspection_reason_inference_quota_unknown"
	case "probe_invalid":
		return "monitoring.xai_inspection_reason_inference_probe_invalid"
	case "protocol_changed":
		return "monitoring.xai_inspection_reason_inference_protocol_changed"
	default:
		return xaiReason(classification)
	}
}

func xaiDisabledReasonKey(classification string) string {
	switch classification {
	case "free_quota_exhausted":
		return "monitoring.xai_inspection_reason_free_quota_disabled"
	case "spending_limit":
		return "monitoring.xai_inspection_reason_spending_limit_disabled"
	case "entitlement_denied":
		return "monitoring.xai_inspection_reason_entitlement_review"
	default:
		return xaiReason(classification)
	}
}

func readNullableFloat(record map[string]any, keys ...string) *float64 {
	if record == nil {
		return nil
	}
	for _, key := range keys {
		if raw, ok := record[key]; ok {
			value := readFloat(raw, math.NaN())
			if !math.IsNaN(value) {
				return &value
			}
		}
	}
	return nil
}

func readXAICentFloatWithEvidence(record map[string]any, keys ...string) (*float64, bool) {
	if record == nil {
		return nil, false
	}
	for _, key := range keys {
		raw, ok := record[key]
		if !ok {
			continue
		}
		if object := toMap(raw); object != nil {
			if len(object) == 0 {
				continue
			}
			normalized := math.NaN()
			for _, nestedKey := range []string{"val", "value"} {
				nestedRaw, ok := object[nestedKey]
				if !ok {
					continue
				}
				normalized = readFloat(nestedRaw, math.NaN())
				if !math.IsNaN(normalized) {
					break
				}
			}
			if math.IsNaN(normalized) {
				continue
			}
			return &normalized, true
		}
		value := readFloat(raw, math.NaN())
		if !math.IsNaN(value) {
			return &value, true
		}
	}
	return nil, false
}

func readXAIArray(record map[string]any, keys ...string) []any {
	for _, key := range keys {
		if items, ok := record[key].([]any); ok {
			return items
		}
	}
	return nil
}

func toMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func intPointer(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}
