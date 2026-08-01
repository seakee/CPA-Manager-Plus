package usage

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ResponseHeaderMetadata struct {
	Quota         *HeaderQuotaMetadata      `json:"quota,omitempty"`
	Errors        *HeaderErrorMetadata      `json:"errors,omitempty"`
	Trace         *HeaderTraceMetadata      `json:"trace,omitempty"`
	Routing       *HeaderRoutingMetadata    `json:"routing,omitempty"`
	Response      *HeaderResponseMetadata   `json:"response,omitempty"`
	Providers     *HeaderProviderMetadata   `json:"providers,omitempty"`
	RateLimit     *HeaderRateLimitMetadata  `json:"rate_limit,omitempty"`
	DataPolicy    *HeaderDataPolicyMetadata `json:"data_policy,omitempty"`
	ProviderUsage *ProviderUsageMetadata    `json:"provider_usage,omitempty"`
}

type HeaderQuotaMetadata struct {
	PlanType                         string             `json:"plan_type,omitempty"`
	ActiveLimit                      string             `json:"active_limit,omitempty"`
	RateLimitReachedType             string             `json:"rate_limit_reached_type,omitempty"`
	SummaryWindowKind                string             `json:"summary_window_kind,omitempty"`
	SummaryWindowSource              string             `json:"summary_window_source,omitempty"`
	ReachedWindowKind                string             `json:"reached_window_kind,omitempty"`
	ReachedWindowSource              string             `json:"reached_window_source,omitempty"`
	CreditsBalance                   string             `json:"credits_balance,omitempty"`
	CreditsHasCredits                *bool              `json:"credits_has_credits,omitempty"`
	CreditsUnlimited                 *bool              `json:"credits_unlimited,omitempty"`
	PrimaryOverSecondaryLimitPercent *float64           `json:"primary_over_secondary_limit_percent,omitempty"`
	Primary                          *HeaderQuotaWindow `json:"primary,omitempty"`
	Secondary                        *HeaderQuotaWindow `json:"secondary,omitempty"`
	RecoverAtMS                      int64              `json:"recover_at_ms,omitempty"`
	UsedPercent                      *float64           `json:"used_percent,omitempty"`
}

type HeaderQuotaWindow struct {
	UsedPercent       *float64 `json:"used_percent,omitempty"`
	ResetAtMS         int64    `json:"reset_at_ms,omitempty"`
	ResetAfterSeconds *float64 `json:"reset_after_seconds,omitempty"`
	WindowMinutes     *float64 `json:"window_minutes,omitempty"`
}

type HeaderErrorMetadata struct {
	Kind                  string   `json:"kind,omitempty"`
	Code                  string   `json:"code,omitempty"`
	AuthorizationError    string   `json:"authorization_error,omitempty"`
	IDEErrorCode          string   `json:"ide_error_code,omitempty"`
	IDERootErrorCode      string   `json:"ide_root_error_code,omitempty"`
	ShouldRetry           *bool    `json:"should_retry,omitempty"`
	RetryAfterSeconds     *float64 `json:"retry_after_seconds,omitempty"`
	RetryAfterRecoverAtMS int64    `json:"retry_after_recover_at_ms,omitempty"`
	RateLimitBypass       string   `json:"rate_limit_bypass,omitempty"`
}

type HeaderTraceMetadata struct {
	PrimaryTraceID          string `json:"primary_trace_id,omitempty"`
	OpenAIRequestID         string `json:"openai_request_id,omitempty"`
	RequestID               string `json:"request_id,omitempty"`
	OneAPIRequestID         string `json:"oneapi_request_id,omitempty"`
	CFRay                   string `json:"cf_ray,omitempty"`
	EagleID                 string `json:"eagle_id,omitempty"`
	CloudAICompanionTraceID string `json:"cloud_ai_companion_trace_id,omitempty"`
	ClientRequestID         string `json:"client_request_id,omitempty"`
	ZeaburRequestID         string `json:"zeabur_request_id,omitempty"`
	Traceparent             string `json:"traceparent,omitempty"`
}

type HeaderRoutingMetadata struct {
	OpenAIProxyWasm string `json:"openai_proxy_wasm,omitempty"`
	ModelsETag      string `json:"models_etag,omitempty"`
	NewAPIVersion   string `json:"new_api_version,omitempty"`
	Server          string `json:"server,omitempty"`
	Via             string `json:"via,omitempty"`
	CFCacheStatus   string `json:"cf_cache_status,omitempty"`
	SiteCacheStatus string `json:"site_cache_status,omitempty"`
	ServedBy        string `json:"served_by,omitempty"`
	MiFEUpstream    string `json:"mife_upstream_status,omitempty"`
}

type HeaderResponseMetadata struct {
	ContentType        string `json:"content_type,omitempty"`
	ContentLength      *int64 `json:"content_length,omitempty"`
	ContentDisposition string `json:"content_disposition,omitempty"`
	ServerTiming       string `json:"server_timing,omitempty"`
}

type HeaderProviderMetadata struct {
	AntigravityTraceID      string `json:"antigravity_trace_id,omitempty"`
	AntigravityServerTiming string `json:"antigravity_server_timing,omitempty"`
	MiFEUpstreamStatus      string `json:"mife_upstream_status,omitempty"`
	OneAPIRequestID         string `json:"oneapi_request_id,omitempty"`
	CloudflareRay           string `json:"cloudflare_ray,omitempty"`
	CloudflareCacheStatus   string `json:"cloudflare_cache_status,omitempty"`
}

type HeaderRateLimitBucket struct {
	Limit     *int64 `json:"limit,omitempty"`
	Remaining *int64 `json:"remaining,omitempty"`
}

type HeaderRateLimitMetadata struct {
	Requests *HeaderRateLimitBucket `json:"requests,omitempty"`
	Tokens   *HeaderRateLimitBucket `json:"tokens,omitempty"`
}

type HeaderDataPolicyMetadata struct {
	RetentionMode string `json:"retention_mode,omitempty"`
	ZeroRetention *bool  `json:"zero_retention,omitempty"`
}

type ResponseHeaderDerived struct {
	MetadataJSON     string
	QuotaRecoverAtMS int64
	QuotaUsedPercent *float64
	QuotaPlanType    string
	ErrorKind        string
	ErrorCode        string
	TraceID          string
}

const maxResponseHeaderMetadataValueBytes = 1024

var traceparentPattern = regexp.MustCompile(`^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)

func ParseResponseHeaderMetadata(raw any, base time.Time) *ResponseHeaderMetadata {
	headers := normalizeResponseHeaders(raw)
	if len(headers) == 0 {
		return nil
	}

	metadata := &ResponseHeaderMetadata{
		Quota:      parseQuotaHeaders(headers, base),
		Errors:     parseErrorHeaders(headers, base),
		Trace:      parseTraceHeaders(headers),
		Routing:    parseRoutingHeaders(headers),
		Response:   parseResponseShapeHeaders(headers),
		Providers:  parseProviderHeaders(headers),
		RateLimit:  parseRateLimitHeaders(headers),
		DataPolicy: parseDataPolicyHeaders(headers),
	}
	if metadata.isEmpty() {
		return nil
	}
	return metadata
}

func ParseResponseHeaderMetadataFromRawJSON(rawJSON string, base time.Time) *ResponseHeaderMetadata {
	trimmed := strings.TrimSpace(rawJSON)
	if trimmed == "" {
		return nil
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(trimmed), &record); err != nil {
		return nil
	}
	metadata := ParseResponseHeaderMetadata(first(record, "response_headers", "responseHeaders", "headers"), base)
	return attachProviderUsageMetadata(metadata, ProviderUsageMetadataFromRecord(record, base))
}

func ResponseHeaderMetadataFromRecord(record map[string]any, base time.Time) *ResponseHeaderMetadata {
	if record == nil {
		return nil
	}
	var metadata *ResponseHeaderMetadata
	if raw := first(record, "response_metadata", "responseMetadata"); raw != nil {
		metadata = responseHeaderMetadataFromAny(raw)
	}
	// Imported JSONL can contain metadata produced by an older CPAMP version
	// alongside newer signals in raw_json or response_headers. Enrich the stored
	// metadata instead of letting its presence hide those same-record signals.
	metadata = MergeResponseHeaderMetadata(
		metadata,
		ParseResponseHeaderMetadataFromRawJSON(readString(record, "raw_json", "rawJson"), base),
	)
	metadata = MergeResponseHeaderMetadata(
		metadata,
		ParseResponseHeaderMetadata(first(record, "response_headers", "responseHeaders", "headers"), base),
	)
	return attachProviderUsageMetadata(metadata, ProviderUsageMetadataFromRecord(record, base))
}

func attachProviderUsageMetadata(metadata *ResponseHeaderMetadata, providerUsage *ProviderUsageMetadata) *ResponseHeaderMetadata {
	if providerUsage == nil || providerUsage.isEmpty() {
		return metadata
	}
	if metadata == nil {
		metadata = &ResponseHeaderMetadata{}
	}
	// Do not overlay transport Retry-After onto provider_usage recovery.
	// xAI included-free exhaustion uses a rolling 24h quota window; short
	// Retry-After headers only describe request retry backoff.
	return MergeResponseHeaderMetadata(metadata, &ResponseHeaderMetadata{ProviderUsage: providerUsage})
}

func ResponseHeaderMetadataFromJSON(raw string) *ResponseHeaderMetadata {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var metadata ResponseHeaderMetadata
	if err := json.Unmarshal([]byte(trimmed), &metadata); err != nil {
		return nil
	}
	sanitizeResponseHeaderMetadata(&metadata)
	if metadata.isEmpty() {
		return nil
	}
	return &metadata
}

func responseHeaderMetadataFromAny(raw any) *ResponseHeaderMetadata {
	if raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var metadata ResponseHeaderMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil
	}
	sanitizeResponseHeaderMetadata(&metadata)
	if metadata.isEmpty() {
		return nil
	}
	return &metadata
}

// MergeResponseHeaderMetadata enriches existing metadata with non-empty values
// from the same response. Nested objects are merged so a newly supported header
// does not discard diagnostics already carried by an imported record.
func MergeResponseHeaderMetadata(existing *ResponseHeaderMetadata, overlay *ResponseHeaderMetadata) *ResponseHeaderMetadata {
	if existing == nil {
		return cloneResponseHeaderMetadata(overlay)
	}
	if overlay == nil {
		return cloneResponseHeaderMetadata(existing)
	}
	providerUsage := MergeProviderUsageMetadata(existing.ProviderUsage, overlay.ProviderUsage)

	existingRaw, err := json.Marshal(existing)
	if err != nil {
		return cloneResponseHeaderMetadata(existing)
	}
	overlayRaw, err := json.Marshal(overlay)
	if err != nil {
		return cloneResponseHeaderMetadata(existing)
	}
	var existingObject map[string]any
	var overlayObject map[string]any
	if json.Unmarshal(existingRaw, &existingObject) != nil || json.Unmarshal(overlayRaw, &overlayObject) != nil {
		return cloneResponseHeaderMetadata(existing)
	}
	mergeResponseHeaderMetadataObjects(existingObject, overlayObject)
	mergedRaw, err := json.Marshal(existingObject)
	if err != nil {
		return cloneResponseHeaderMetadata(existing)
	}
	var merged ResponseHeaderMetadata
	if json.Unmarshal(mergedRaw, &merged) != nil {
		return cloneResponseHeaderMetadata(existing)
	}
	merged.ProviderUsage = providerUsage
	sanitizeResponseHeaderMetadata(&merged)
	if merged.isEmpty() {
		return nil
	}
	return &merged
}

func cloneResponseHeaderMetadata(metadata *ResponseHeaderMetadata) *ResponseHeaderMetadata {
	if metadata == nil {
		return nil
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil
	}
	var cloned ResponseHeaderMetadata
	if json.Unmarshal(raw, &cloned) != nil {
		return nil
	}
	sanitizeResponseHeaderMetadata(&cloned)
	if cloned.isEmpty() {
		return nil
	}
	return &cloned
}

func mergeResponseHeaderMetadataObjects(existing map[string]any, overlay map[string]any) {
	for key, value := range overlay {
		overlayObject, overlayOK := value.(map[string]any)
		existingObject, existingOK := existing[key].(map[string]any)
		if overlayOK && existingOK {
			mergeResponseHeaderMetadataObjects(existingObject, overlayObject)
			continue
		}
		existing[key] = value
	}
}

func DeriveResponseHeaderMetadata(metadata *ResponseHeaderMetadata) ResponseHeaderDerived {
	sanitizeResponseHeaderMetadata(metadata)
	if metadata == nil || metadata.isEmpty() {
		return ResponseHeaderDerived{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return ResponseHeaderDerived{}
	}
	derived := ResponseHeaderDerived{MetadataJSON: string(raw)}
	if metadata.Quota != nil {
		derived.QuotaRecoverAtMS = metadata.Quota.RecoverAtMS
		derived.QuotaUsedPercent = metadata.Quota.UsedPercent
		derived.QuotaPlanType = metadata.Quota.PlanType
	}
	if metadata.Errors != nil {
		derived.ErrorKind = metadata.Errors.Kind
		derived.ErrorCode = metadata.Errors.Code
	}
	if metadata.Trace != nil {
		derived.TraceID = metadata.Trace.PrimaryTraceID
	}
	return derived
}

func AttachResponseHeaderMetadata(event *Event, metadata *ResponseHeaderMetadata) {
	if event == nil {
		return
	}
	sanitizeResponseHeaderMetadata(metadata)
	if metadata == nil || metadata.isEmpty() {
		return
	}
	derived := DeriveResponseHeaderMetadata(metadata)
	event.ResponseMetadata = metadata
	event.ResponseMetadataJSON = derived.MetadataJSON
	event.HeaderQuotaRecoverAtMS = derived.QuotaRecoverAtMS
	event.HeaderQuotaUsedPercent = derived.QuotaUsedPercent
	event.HeaderQuotaPlanType = derived.QuotaPlanType
	event.HeaderErrorKind = derived.ErrorKind
	event.HeaderErrorCode = derived.ErrorCode
	event.HeaderTraceID = derived.TraceID
}

func sanitizeResponseHeaderMetadata(metadata *ResponseHeaderMetadata) {
	if metadata == nil {
		return
	}
	if metadata.Quota != nil {
		metadata.Quota.PlanType = normalizeHeaderValue(metadata.Quota.PlanType)
		metadata.Quota.ActiveLimit = normalizeHeaderValue(metadata.Quota.ActiveLimit)
		metadata.Quota.RateLimitReachedType = normalizeHeaderValue(metadata.Quota.RateLimitReachedType)
		metadata.Quota.SummaryWindowKind = normalizeQuotaWindowKind(metadata.Quota.SummaryWindowKind)
		metadata.Quota.SummaryWindowSource = normalizeQuotaWindowSource(metadata.Quota.SummaryWindowSource)
		metadata.Quota.ReachedWindowKind = normalizeQuotaWindowKind(metadata.Quota.ReachedWindowKind)
		metadata.Quota.ReachedWindowSource = normalizeQuotaWindowSource(metadata.Quota.ReachedWindowSource)
		metadata.Quota.CreditsBalance = normalizeHeaderValue(metadata.Quota.CreditsBalance)
		applyQuotaWindowSemantics(metadata.Quota)
		if metadata.Quota.isEmpty() {
			metadata.Quota = nil
		}
	}
	if metadata.Errors != nil {
		metadata.Errors.Kind = normalizeHeaderValue(metadata.Errors.Kind)
		metadata.Errors.Code = normalizeHeaderValue(metadata.Errors.Code)
		metadata.Errors.AuthorizationError = normalizeHeaderValue(metadata.Errors.AuthorizationError)
		metadata.Errors.IDEErrorCode = normalizeHeaderValue(metadata.Errors.IDEErrorCode)
		metadata.Errors.IDERootErrorCode = normalizeHeaderValue(metadata.Errors.IDERootErrorCode)
		metadata.Errors.RateLimitBypass = normalizeHeaderValue(metadata.Errors.RateLimitBypass)
		if metadata.Errors.isEmpty() {
			metadata.Errors = nil
		}
	}
	if metadata.Trace != nil {
		metadata.Trace.PrimaryTraceID = normalizeHeaderValue(metadata.Trace.PrimaryTraceID)
		metadata.Trace.OpenAIRequestID = normalizeHeaderValue(metadata.Trace.OpenAIRequestID)
		metadata.Trace.RequestID = normalizeHeaderValue(metadata.Trace.RequestID)
		metadata.Trace.OneAPIRequestID = normalizeHeaderValue(metadata.Trace.OneAPIRequestID)
		metadata.Trace.CFRay = normalizeHeaderValue(metadata.Trace.CFRay)
		metadata.Trace.EagleID = normalizeHeaderValue(metadata.Trace.EagleID)
		metadata.Trace.CloudAICompanionTraceID = normalizeHeaderValue(metadata.Trace.CloudAICompanionTraceID)
		metadata.Trace.ClientRequestID = normalizeHeaderValue(metadata.Trace.ClientRequestID)
		metadata.Trace.ZeaburRequestID = normalizeHeaderValue(metadata.Trace.ZeaburRequestID)
		metadata.Trace.Traceparent = normalizeTraceparent(metadata.Trace.Traceparent)
		metadata.Trace.PrimaryTraceID = firstNonEmptyString(
			metadata.Trace.PrimaryTraceID,
			metadata.Trace.OpenAIRequestID,
			metadata.Trace.RequestID,
			metadata.Trace.OneAPIRequestID,
			metadata.Trace.CloudAICompanionTraceID,
			metadata.Trace.CFRay,
			metadata.Trace.EagleID,
			metadata.Trace.ClientRequestID,
			metadata.Trace.ZeaburRequestID,
			traceIDFromTraceparent(metadata.Trace.Traceparent),
		)
		if metadata.Trace.isEmpty() {
			metadata.Trace = nil
		}
	}
	if metadata.Routing != nil {
		metadata.Routing.OpenAIProxyWasm = normalizeHeaderValue(metadata.Routing.OpenAIProxyWasm)
		metadata.Routing.ModelsETag = normalizeHeaderValue(metadata.Routing.ModelsETag)
		metadata.Routing.NewAPIVersion = normalizeHeaderValue(metadata.Routing.NewAPIVersion)
		metadata.Routing.Server = normalizeHeaderValue(metadata.Routing.Server)
		metadata.Routing.Via = normalizeHeaderValue(metadata.Routing.Via)
		metadata.Routing.CFCacheStatus = normalizeHeaderValue(metadata.Routing.CFCacheStatus)
		metadata.Routing.SiteCacheStatus = normalizeHeaderValue(metadata.Routing.SiteCacheStatus)
		metadata.Routing.ServedBy = normalizeHeaderValue(metadata.Routing.ServedBy)
		metadata.Routing.MiFEUpstream = normalizeHeaderValue(metadata.Routing.MiFEUpstream)
		if metadata.Routing.isEmpty() {
			metadata.Routing = nil
		}
	}
	if metadata.Response != nil {
		metadata.Response.ContentType = normalizeHeaderValue(metadata.Response.ContentType)
		metadata.Response.ContentDisposition = normalizeHeaderValue(metadata.Response.ContentDisposition)
		metadata.Response.ServerTiming = normalizeHeaderValue(metadata.Response.ServerTiming)
		if metadata.Response.isEmpty() {
			metadata.Response = nil
		}
	}
	if metadata.Providers != nil {
		metadata.Providers.AntigravityTraceID = normalizeHeaderValue(metadata.Providers.AntigravityTraceID)
		metadata.Providers.AntigravityServerTiming = normalizeHeaderValue(metadata.Providers.AntigravityServerTiming)
		metadata.Providers.MiFEUpstreamStatus = normalizeHeaderValue(metadata.Providers.MiFEUpstreamStatus)
		metadata.Providers.OneAPIRequestID = normalizeHeaderValue(metadata.Providers.OneAPIRequestID)
		metadata.Providers.CloudflareRay = normalizeHeaderValue(metadata.Providers.CloudflareRay)
		metadata.Providers.CloudflareCacheStatus = normalizeHeaderValue(metadata.Providers.CloudflareCacheStatus)
		if metadata.Providers.isEmpty() {
			metadata.Providers = nil
		}
	}
	if metadata.RateLimit != nil {
		sanitizeRateLimitBucket(metadata.RateLimit.Requests)
		sanitizeRateLimitBucket(metadata.RateLimit.Tokens)
		if metadata.RateLimit.isEmpty() {
			metadata.RateLimit = nil
		}
	}
	if metadata.DataPolicy != nil {
		metadata.DataPolicy.RetentionMode = strings.ToLower(normalizeHeaderValue(metadata.DataPolicy.RetentionMode))
		if metadata.DataPolicy.isEmpty() {
			metadata.DataPolicy = nil
		}
	}
	if metadata.ProviderUsage != nil {
		sanitizeProviderUsageMetadata(metadata.ProviderUsage)
		if metadata.ProviderUsage.isEmpty() {
			metadata.ProviderUsage = nil
		}
	}
}

func (m *ResponseHeaderMetadata) isEmpty() bool {
	return m == nil ||
		(m.Quota == nil &&
			m.Errors == nil &&
			m.Trace == nil &&
			m.Routing == nil &&
			m.Response == nil &&
			m.Providers == nil &&
			m.RateLimit == nil &&
			m.DataPolicy == nil &&
			m.ProviderUsage == nil)
}

func sanitizeRateLimitBucket(bucket *HeaderRateLimitBucket) {
	if bucket == nil {
		return
	}
	if bucket.Limit != nil && *bucket.Limit < 0 {
		bucket.Limit = nil
	}
	if bucket.Remaining != nil && *bucket.Remaining < 0 {
		bucket.Remaining = nil
	}
}

func normalizeResponseHeaders(raw any) map[string][]string {
	record, ok := raw.(map[string]any)
	if !ok || len(record) == 0 {
		return nil
	}
	headers := make(map[string][]string, len(record))
	keys := make([]string, 0, len(record))
	for key := range record {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := record[key]
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if normalizedKey == "" || !isResponseHeaderAllowed(normalizedKey) {
			continue
		}
		values := headerValues(value)
		if len(values) == 0 {
			continue
		}
		headers[normalizedKey] = append(headers[normalizedKey], values...)
	}
	return headers
}

// Future response-header candidates observed in real CPA usage data but not
// structured yet:
//   - x-error-json: base64/error JSON that can enrich auth-action evidence after
//     a dedicated redaction and parsing path exists.
//   - x-codex-promo-message and x-codex-promo-campaign-id: low-volume Codex
//     upgrade hints that may become a quota-page informational notice.
//   - x-openai-internal-caller and x-zeabur-ip-country: deployment/source
//     diagnostics that need clearer UI semantics before exposure.
//   - date, cache-control, alt-svc, and security-policy headers: high-volume,
//     low-business-value transport data intentionally kept out of structured
//     filters to avoid noisy UI and expensive distinct queries.
//   - x-models-etag and x-new-api-version are already retained under routing;
//     future work can correlate them with model-list or proxy-version changes.
func isResponseHeaderAllowed(key string) bool {
	if key == "set-cookie" ||
		(strings.Contains(key, "token") && !isSafeTokenRateLimitHeader(key)) ||
		strings.Contains(key, "secret") ||
		strings.Contains(key, "authorization") && key != "x-openai-authorization-error" {
		return false
	}
	switch key {
	case "x-codex-plan-type",
		"x-codex-active-limit",
		"x-codex-rate-limit-reached-type",
		"x-codex-credits-balance",
		"x-codex-credits-has-credits",
		"x-codex-credits-unlimited",
		"x-codex-primary-over-secondary-limit-percent",
		"x-codex-primary-used-percent",
		"x-codex-secondary-used-percent",
		"x-codex-primary-reset-at",
		"x-codex-secondary-reset-at",
		"x-codex-primary-reset-after-seconds",
		"x-codex-secondary-reset-after-seconds",
		"x-codex-primary-window-minutes",
		"x-codex-secondary-window-minutes",
		"retry-after",
		"x-should-retry",
		"x-openai-authorization-error",
		"x-openai-ide-error-code",
		"x-openai-ide-root-error-code",
		"x-ratelimit-bypass",
		"x-oai-request-id",
		"x-request-id",
		"x-oneapi-request-id",
		"cf-ray",
		"eagleid",
		"x-cloudaicompanion-trace-id",
		"x-client-request-id",
		"x-zeabur-request-id",
		"traceparent",
		"x-openai-proxy-wasm",
		"x-models-etag",
		"x-new-api-version",
		"server",
		"via",
		"cf-cache-status",
		"x-site-cache-status",
		"x-served-by",
		"x-mife-upstream-status",
		"content-type",
		"content-length",
		"content-disposition",
		"server-timing",
		"x-ratelimit-limit-requests",
		"x-ratelimit-remaining-requests",
		"x-ratelimit-limit-tokens",
		"x-ratelimit-remaining-tokens",
		"x-data-retention",
		"x-zero-data-retention",
		"x-zero-retention":
		return true
	default:
		return false
	}
}

func isSafeTokenRateLimitHeader(key string) bool {
	return key == "x-ratelimit-limit-tokens" || key == "x-ratelimit-remaining-tokens"
}

func headerValues(raw any) []string {
	switch value := raw.(type) {
	case nil:
		return nil
	case []any:
		values := make([]string, 0, len(value))
		for _, item := range value {
			if text := scalarHeaderValue(item); text != "" {
				values = append(values, text)
			}
		}
		return values
	case []string:
		values := make([]string, 0, len(value))
		for _, item := range value {
			if text := strings.TrimSpace(item); text != "" {
				values = append(values, text)
			}
		}
		return values
	default:
		if text := scalarHeaderValue(value); text != "" {
			return []string{text}
		}
		return nil
	}
}

func scalarHeaderValue(raw any) string {
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case json.Number:
		return value.String()
	case float64:
		if value == math.Trunc(value) {
			return strconv.FormatInt(int64(value), 10)
		}
		return strconv.FormatFloat(value, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(value), 'f', -1, 32)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case bool:
		return strconv.FormatBool(value)
	default:
		return ""
	}
}

func headerFirst(headers map[string][]string, keys ...string) string {
	for _, key := range keys {
		values := headers[strings.ToLower(key)]
		for _, value := range values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func parseQuotaHeaders(headers map[string][]string, base time.Time) *HeaderQuotaMetadata {
	quota := &HeaderQuotaMetadata{
		PlanType:             normalizeHeaderValue(headerFirst(headers, "x-codex-plan-type")),
		ActiveLimit:          normalizeHeaderValue(headerFirst(headers, "x-codex-active-limit")),
		RateLimitReachedType: normalizeHeaderValue(headerFirst(headers, "x-codex-rate-limit-reached-type")),
		CreditsBalance:       normalizeHeaderValue(headerFirst(headers, "x-codex-credits-balance")),
		Primary: parseQuotaWindowHeaders(
			headers,
			base,
			"x-codex-primary-used-percent",
			"x-codex-primary-reset-at",
			"x-codex-primary-reset-after-seconds",
			"x-codex-primary-window-minutes",
		),
		Secondary: parseQuotaWindowHeaders(
			headers,
			base,
			"x-codex-secondary-used-percent",
			"x-codex-secondary-reset-at",
			"x-codex-secondary-reset-after-seconds",
			"x-codex-secondary-window-minutes",
		),
	}
	if value, ok := parseBoolHeader(headerFirst(headers, "x-codex-credits-has-credits")); ok {
		quota.CreditsHasCredits = &value
	}
	if value, ok := parseBoolHeader(headerFirst(headers, "x-codex-credits-unlimited")); ok {
		quota.CreditsUnlimited = &value
	}
	if value, ok := parseFloatHeader(headerFirst(headers, "x-codex-primary-over-secondary-limit-percent")); ok {
		quota.PrimaryOverSecondaryLimitPercent = &value
	}
	applyQuotaWindowSemantics(quota)
	if quota.isEmpty() {
		return nil
	}
	return quota
}

func parseQuotaWindowHeaders(headers map[string][]string, base time.Time, usedKey string, resetAtKey string, resetAfterKey string, windowKey string) *HeaderQuotaWindow {
	window := &HeaderQuotaWindow{}
	if value, ok := parseFloatHeader(headerFirst(headers, usedKey)); ok {
		window.UsedPercent = &value
	}
	if value, ok := parseFloatHeader(headerFirst(headers, resetAfterKey)); ok {
		window.ResetAfterSeconds = &value
		if !base.IsZero() && value > 0 {
			window.ResetAtMS = base.Add(time.Duration(value * float64(time.Second))).UnixMilli()
		}
	}
	if resetAt, ok := parseHeaderTime(headerFirst(headers, resetAtKey), base, false); ok {
		window.ResetAtMS = resetAt.UnixMilli()
	}
	if value, ok := parseFloatHeader(headerFirst(headers, windowKey)); ok {
		window.WindowMinutes = &value
	}
	if window.isEmpty() {
		return nil
	}
	return window
}

func parseErrorHeaders(headers map[string][]string, base time.Time) *HeaderErrorMetadata {
	errors := &HeaderErrorMetadata{
		AuthorizationError: normalizeHeaderValue(headerFirst(headers, "x-openai-authorization-error")),
		IDEErrorCode:       normalizeHeaderValue(headerFirst(headers, "x-openai-ide-error-code")),
		IDERootErrorCode:   normalizeHeaderValue(headerFirst(headers, "x-openai-ide-root-error-code")),
		RateLimitBypass:    normalizeHeaderValue(headerFirst(headers, "x-ratelimit-bypass")),
	}
	if value, ok := parseBoolHeader(headerFirst(headers, "x-should-retry")); ok {
		errors.ShouldRetry = &value
	}
	retryAfter := headerFirst(headers, "retry-after")
	if retryAfter != "" {
		if seconds, recoverAt, ok := parseRetryAfter(retryAfter, base); ok {
			errors.RetryAfterSeconds = &seconds
			errors.RetryAfterRecoverAtMS = recoverAt.UnixMilli()
		}
	}
	errors.Kind, errors.Code = classifyHeaderError(errors)
	if errors.isEmpty() {
		return nil
	}
	return errors
}

func parseTraceHeaders(headers map[string][]string) *HeaderTraceMetadata {
	trace := &HeaderTraceMetadata{
		OpenAIRequestID:         normalizeHeaderValue(headerFirst(headers, "x-oai-request-id")),
		RequestID:               normalizeHeaderValue(headerFirst(headers, "x-request-id")),
		OneAPIRequestID:         normalizeHeaderValue(headerFirst(headers, "x-oneapi-request-id")),
		CFRay:                   normalizeHeaderValue(headerFirst(headers, "cf-ray")),
		EagleID:                 normalizeHeaderValue(headerFirst(headers, "eagleid")),
		CloudAICompanionTraceID: normalizeHeaderValue(headerFirst(headers, "x-cloudaicompanion-trace-id")),
		ClientRequestID:         normalizeHeaderValue(headerFirst(headers, "x-client-request-id")),
		ZeaburRequestID:         normalizeHeaderValue(headerFirst(headers, "x-zeabur-request-id")),
		Traceparent:             normalizeTraceparent(headerFirst(headers, "traceparent")),
	}
	trace.PrimaryTraceID = firstNonEmptyString(
		trace.OpenAIRequestID,
		trace.RequestID,
		trace.OneAPIRequestID,
		trace.CloudAICompanionTraceID,
		trace.CFRay,
		trace.EagleID,
		trace.ClientRequestID,
		trace.ZeaburRequestID,
		traceIDFromTraceparent(trace.Traceparent),
	)
	if trace.isEmpty() {
		return nil
	}
	return trace
}

func parseRoutingHeaders(headers map[string][]string) *HeaderRoutingMetadata {
	routing := &HeaderRoutingMetadata{
		OpenAIProxyWasm: normalizeHeaderValue(headerFirst(headers, "x-openai-proxy-wasm")),
		ModelsETag:      normalizeHeaderValue(headerFirst(headers, "x-models-etag")),
		NewAPIVersion:   normalizeHeaderValue(headerFirst(headers, "x-new-api-version")),
		Server:          normalizeHeaderValue(headerFirst(headers, "server")),
		Via:             normalizeHeaderValue(headerFirst(headers, "via")),
		CFCacheStatus:   normalizeHeaderValue(headerFirst(headers, "cf-cache-status")),
		SiteCacheStatus: normalizeHeaderValue(headerFirst(headers, "x-site-cache-status")),
		ServedBy:        normalizeHeaderValue(headerFirst(headers, "x-served-by")),
		MiFEUpstream:    normalizeHeaderValue(headerFirst(headers, "x-mife-upstream-status")),
	}
	if routing.isEmpty() {
		return nil
	}
	return routing
}

func parseResponseShapeHeaders(headers map[string][]string) *HeaderResponseMetadata {
	response := &HeaderResponseMetadata{
		ContentType:        normalizeHeaderValue(headerFirst(headers, "content-type")),
		ContentDisposition: normalizeHeaderValue(headerFirst(headers, "content-disposition")),
		ServerTiming:       normalizeHeaderValue(headerFirst(headers, "server-timing")),
	}
	if value, ok := parseIntHeader(headerFirst(headers, "content-length")); ok {
		response.ContentLength = &value
	}
	if response.isEmpty() {
		return nil
	}
	return response
}

func parseProviderHeaders(headers map[string][]string) *HeaderProviderMetadata {
	providers := &HeaderProviderMetadata{
		AntigravityTraceID:      normalizeHeaderValue(headerFirst(headers, "x-cloudaicompanion-trace-id")),
		AntigravityServerTiming: normalizeHeaderValue(headerFirst(headers, "server-timing")),
		MiFEUpstreamStatus:      normalizeHeaderValue(headerFirst(headers, "x-mife-upstream-status")),
		OneAPIRequestID:         normalizeHeaderValue(headerFirst(headers, "x-oneapi-request-id")),
		CloudflareRay:           normalizeHeaderValue(headerFirst(headers, "cf-ray")),
		CloudflareCacheStatus:   normalizeHeaderValue(headerFirst(headers, "cf-cache-status")),
	}
	if providers.isEmpty() {
		return nil
	}
	return providers
}

func parseRateLimitHeaders(headers map[string][]string) *HeaderRateLimitMetadata {
	metadata := &HeaderRateLimitMetadata{
		Requests: parseRateLimitBucket(
			headers,
			"x-ratelimit-limit-requests",
			"x-ratelimit-remaining-requests",
		),
		Tokens: parseRateLimitBucket(
			headers,
			"x-ratelimit-limit-tokens",
			"x-ratelimit-remaining-tokens",
		),
	}
	if metadata.isEmpty() {
		return nil
	}
	return metadata
}

func parseRateLimitBucket(headers map[string][]string, limitKey string, remainingKey string) *HeaderRateLimitBucket {
	bucket := &HeaderRateLimitBucket{}
	if value, ok := parseIntHeader(headerFirst(headers, limitKey)); ok && value >= 0 {
		bucket.Limit = int64Pointer(value)
	}
	if value, ok := parseIntHeader(headerFirst(headers, remainingKey)); ok && value >= 0 {
		bucket.Remaining = int64Pointer(value)
	}
	if bucket.isEmpty() {
		return nil
	}
	return bucket
}

func parseDataPolicyHeaders(headers map[string][]string) *HeaderDataPolicyMetadata {
	metadata := &HeaderDataPolicyMetadata{
		RetentionMode: normalizeHeaderValue(headerFirst(headers, "x-data-retention")),
	}
	for _, key := range []string{"x-zero-data-retention", "x-zero-retention"} {
		if value, ok := parseBoolHeader(headerFirst(headers, key)); ok {
			metadata.ZeroRetention = boolPointer(value)
			break
		}
	}
	if metadata.isEmpty() {
		return nil
	}
	return metadata
}

func normalizeHeaderValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	return truncateUTF8Bytes(FailSummaryFromBody(trimmed), maxResponseHeaderMetadataValueBytes)
}

func normalizeTraceparent(value string) string {
	normalized := strings.ToLower(normalizeHeaderValue(value))
	if !traceparentPattern.MatchString(normalized) {
		return ""
	}
	parts := strings.Split(normalized, "-")
	if parts[0] == "ff" || parts[1] == strings.Repeat("0", 32) || parts[2] == strings.Repeat("0", 16) {
		return ""
	}
	return normalized
}

// traceIDFromTraceparent extracts the 32-hex trace-id from a validated W3C
// traceparent value. PrimaryTraceID / header_trace_id use this compact id for
// filtering and correlation; the full traceparent stays in Traceparent.
func traceIDFromTraceparent(value string) string {
	normalized := normalizeTraceparent(value)
	if normalized == "" {
		return ""
	}
	parts := strings.Split(normalized, "-")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

func parseFloatHeader(value string) (float64, bool) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(value, "%"))
	if trimmed == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false
	}
	return parsed, true
}

func parseIntHeader(value string) (int64, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	return parsed, err == nil
}

func parseBoolHeader(value string) (bool, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	switch trimmed {
	case "true", "1", "yes":
		return true, true
	case "false", "0", "no":
		return false, true
	default:
		return false, false
	}
}

func parseRetryAfter(value string, base time.Time) (float64, time.Time, bool) {
	if seconds, ok := parseFloatHeader(value); ok && seconds >= 0 {
		return seconds, base.Add(time.Duration(seconds * float64(time.Second))), true
	}
	if at, ok := parseHeaderTime(value, base, false); ok {
		seconds := at.Sub(base).Seconds()
		if seconds < 0 {
			seconds = 0
		}
		return seconds, at, true
	}
	return 0, time.Time{}, false
}

func parseHeaderTime(value string, base time.Time, relative bool) (time.Time, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || strings.EqualFold(trimmed, "null") {
		return time.Time{}, false
	}
	if !relative {
		if parsed, ok := parseHeaderCommonTime(trimmed); ok {
			return parsed, true
		}
	}
	number, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || number <= 0 {
		return time.Time{}, false
	}
	if relative {
		return base.Add(time.Duration(number * float64(time.Second))), true
	}
	if number > 1_000_000_000_000 {
		return time.UnixMilli(int64(number)), true
	}
	if number > 1_000_000_000 {
		return time.Unix(int64(number), 0), true
	}
	return time.Time{}, false
}

func parseHeaderCommonTime(value string) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		time.RFC1123,
		time.RFC1123Z,
		"2006-01-02T15:04:05.000Z07:00",
		"2006-01-02 15:04:05 MST",
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func classifyHeaderError(errors *HeaderErrorMetadata) (string, string) {
	if errors == nil {
		return "", ""
	}
	for _, code := range []string{errors.IDERootErrorCode, errors.IDEErrorCode, errors.AuthorizationError} {
		normalized := strings.ToLower(strings.TrimSpace(code))
		switch normalized {
		case "token_revoked", "token_invalidated", "account_deactivated", "401":
			return "auth", normalized
		case "identity_edge_internal_error":
			return "identity", normalized
		}
	}
	if errors.RateLimitBypass != "" {
		return "rate_limit", errors.RateLimitBypass
	}
	if errors.RetryAfterSeconds != nil {
		return "rate_limit", "retry_after"
	}
	return "", ""
}

const (
	quotaWindowKindFiveHour = "five_hour"
	quotaWindowKindWeekly   = "weekly"
	quotaWindowKindMonthly  = "monthly"
	quotaWindowKindUnknown  = "unknown"

	quotaWindowSourcePrimary    = "primary"
	quotaWindowSourceSecondary  = "secondary"
	quotaWindowSourceAggregate  = "aggregate"
	quotaWindowSourceRetryAfter = "retry_after"
	quotaWindowSourceUnknown    = "unknown"
)

type quotaWindowSelection struct {
	source         string
	kind           string
	usedPercent    float64
	hasUsedPercent bool
	resetAtMS      int64
}

func applyQuotaWindowSemantics(quota *HeaderQuotaMetadata) {
	if quota == nil {
		return
	}
	selections := quotaWindowSelections(quota)
	if len(selections) == 0 {
		if quota.UsedPercent != nil || quota.RecoverAtMS > 0 {
			if quota.SummaryWindowKind == "" {
				quota.SummaryWindowKind = quotaWindowKindUnknown
			}
			if quota.SummaryWindowSource == "" {
				quota.SummaryWindowSource = quotaWindowSourceAggregate
			}
		}
		if strings.TrimSpace(quota.RateLimitReachedType) != "" {
			if quota.ReachedWindowKind == "" {
				quota.ReachedWindowKind = quotaWindowKindUnknown
			}
			if quota.ReachedWindowSource == "" {
				quota.ReachedWindowSource = quotaWindowSourceUnknown
			}
		}
		return
	}

	if summary, ok := selectQuotaSummaryWindow(selections); ok {
		quota.UsedPercent = float64Pointer(summary.usedPercent)
		quota.RecoverAtMS = summary.resetAtMS
		quota.SummaryWindowKind = summary.kind
		quota.SummaryWindowSource = summary.source
	} else if quota.UsedPercent != nil || quota.RecoverAtMS > 0 {
		if quota.SummaryWindowKind == "" {
			quota.SummaryWindowKind = quotaWindowKindUnknown
		}
		if quota.SummaryWindowSource == "" {
			quota.SummaryWindowSource = quotaWindowSourceAggregate
		}
	}

	if reached, ok := selectQuotaReachedWindow(quota, selections); ok {
		quota.ReachedWindowKind = reached.kind
		quota.ReachedWindowSource = reached.source
		if quota.RecoverAtMS <= 0 && reached.resetAtMS > 0 {
			quota.RecoverAtMS = reached.resetAtMS
		}
	} else if strings.TrimSpace(quota.RateLimitReachedType) != "" {
		quota.ReachedWindowKind = quotaWindowKindUnknown
		quota.ReachedWindowSource = quotaWindowSourceUnknown
	}
}

func quotaWindowSelections(quota *HeaderQuotaMetadata) []quotaWindowSelection {
	if quota == nil {
		return nil
	}
	selections := make([]quotaWindowSelection, 0, 2)
	if quota.Primary != nil {
		selections = append(selections, newQuotaWindowSelection(quotaWindowSourcePrimary, quota.Primary))
	}
	if quota.Secondary != nil {
		selections = append(selections, newQuotaWindowSelection(quotaWindowSourceSecondary, quota.Secondary))
	}
	return selections
}

func newQuotaWindowSelection(source string, window *HeaderQuotaWindow) quotaWindowSelection {
	selection := quotaWindowSelection{
		source:    source,
		kind:      quotaWindowKind(window),
		resetAtMS: quotaWindowResetAtMS(window),
	}
	if window != nil && window.UsedPercent != nil {
		selection.usedPercent = *window.UsedPercent
		selection.hasUsedPercent = true
	}
	return selection
}

func selectQuotaSummaryWindow(selections []quotaWindowSelection) (quotaWindowSelection, bool) {
	var selected quotaWindowSelection
	found := false
	for _, selection := range selections {
		if !selection.hasUsedPercent {
			continue
		}
		if !found ||
			selection.usedPercent > selected.usedPercent ||
			(selection.usedPercent == selected.usedPercent && selection.resetAtMS > selected.resetAtMS) {
			selected = selection
			found = true
		}
	}
	return selected, found
}

func selectQuotaReachedWindow(quota *HeaderQuotaMetadata, selections []quotaWindowSelection) (quotaWindowSelection, bool) {
	reachedType := strings.ToLower(strings.TrimSpace(quota.RateLimitReachedType))
	switch reachedType {
	case quotaWindowSourcePrimary, quotaWindowSourceSecondary:
		for _, selection := range selections {
			if selection.source == reachedType {
				return selection, true
			}
		}
	}

	var selected quotaWindowSelection
	found := false
	for _, selection := range selections {
		if !selection.hasUsedPercent || selection.usedPercent < 100 {
			continue
		}
		if !found || selection.resetAtMS > selected.resetAtMS {
			selected = selection
			found = true
		}
	}
	return selected, found
}

func quotaWindowKind(window *HeaderQuotaWindow) string {
	if window == nil || window.WindowMinutes == nil || *window.WindowMinutes <= 0 {
		return quotaWindowKindUnknown
	}
	minutes := *window.WindowMinutes
	switch {
	case nearlyEqualFloat(minutes, 300):
		return quotaWindowKindFiveHour
	case nearlyEqualFloat(minutes, 10_080):
		return quotaWindowKindWeekly
	case minutes >= 28*24*60 && minutes <= 31*24*60:
		return quotaWindowKindMonthly
	default:
		return quotaWindowKindUnknown
	}
}

func normalizeQuotaWindowKind(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	switch normalized {
	case quotaWindowKindFiveHour, quotaWindowKindWeekly, quotaWindowKindMonthly, quotaWindowKindUnknown:
		return normalized
	default:
		return ""
	}
}

func normalizeQuotaWindowSource(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	switch normalized {
	case quotaWindowSourcePrimary, quotaWindowSourceSecondary, quotaWindowSourceAggregate, quotaWindowSourceRetryAfter, quotaWindowSourceUnknown:
		return normalized
	default:
		return ""
	}
}

func quotaWindowResetAtMS(window *HeaderQuotaWindow) int64 {
	if window == nil {
		return 0
	}
	return window.ResetAtMS
}

func float64Pointer(value float64) *float64 {
	return &value
}

func boolPointer(value bool) *bool {
	return &value
}

func nearlyEqualFloat(left float64, right float64) bool {
	return math.Abs(left-right) < 0.001
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (q *HeaderQuotaMetadata) isEmpty() bool {
	return q == nil ||
		(q.PlanType == "" &&
			q.ActiveLimit == "" &&
			q.RateLimitReachedType == "" &&
			q.SummaryWindowKind == "" &&
			q.SummaryWindowSource == "" &&
			q.ReachedWindowKind == "" &&
			q.ReachedWindowSource == "" &&
			q.CreditsBalance == "" &&
			q.CreditsHasCredits == nil &&
			q.CreditsUnlimited == nil &&
			q.PrimaryOverSecondaryLimitPercent == nil &&
			q.Primary == nil &&
			q.Secondary == nil &&
			q.RecoverAtMS == 0 &&
			q.UsedPercent == nil)
}

func (w *HeaderQuotaWindow) isEmpty() bool {
	return w == nil ||
		(w.UsedPercent == nil &&
			w.ResetAtMS == 0 &&
			w.ResetAfterSeconds == nil &&
			w.WindowMinutes == nil)
}

func (e *HeaderErrorMetadata) isEmpty() bool {
	return e == nil ||
		(e.Kind == "" &&
			e.Code == "" &&
			e.AuthorizationError == "" &&
			e.IDEErrorCode == "" &&
			e.IDERootErrorCode == "" &&
			e.ShouldRetry == nil &&
			e.RetryAfterSeconds == nil &&
			e.RetryAfterRecoverAtMS == 0 &&
			e.RateLimitBypass == "")
}

func (t *HeaderTraceMetadata) isEmpty() bool {
	return t == nil ||
		(t.PrimaryTraceID == "" &&
			t.OpenAIRequestID == "" &&
			t.RequestID == "" &&
			t.OneAPIRequestID == "" &&
			t.CFRay == "" &&
			t.EagleID == "" &&
			t.CloudAICompanionTraceID == "" &&
			t.ClientRequestID == "" &&
			t.ZeaburRequestID == "" &&
			t.Traceparent == "")
}

func (r *HeaderRoutingMetadata) isEmpty() bool {
	return r == nil ||
		(r.OpenAIProxyWasm == "" &&
			r.ModelsETag == "" &&
			r.NewAPIVersion == "" &&
			r.Server == "" &&
			r.Via == "" &&
			r.CFCacheStatus == "" &&
			r.SiteCacheStatus == "" &&
			r.ServedBy == "" &&
			r.MiFEUpstream == "")
}

func (r *HeaderResponseMetadata) isEmpty() bool {
	return r == nil ||
		(r.ContentType == "" &&
			r.ContentLength == nil &&
			r.ContentDisposition == "" &&
			r.ServerTiming == "")
}

func (p *HeaderProviderMetadata) isEmpty() bool {
	return p == nil ||
		(p.AntigravityTraceID == "" &&
			p.AntigravityServerTiming == "" &&
			p.MiFEUpstreamStatus == "" &&
			p.OneAPIRequestID == "" &&
			p.CloudflareRay == "" &&
			p.CloudflareCacheStatus == "")
}

func (b *HeaderRateLimitBucket) isEmpty() bool {
	return b == nil || (b.Limit == nil && b.Remaining == nil)
}

func (m *HeaderRateLimitMetadata) isEmpty() bool {
	return m == nil || (m.Requests == nil && m.Tokens == nil)
}

func (m *HeaderDataPolicyMetadata) isEmpty() bool {
	return m == nil || (m.RetentionMode == "" && m.ZeroRetention == nil)
}
