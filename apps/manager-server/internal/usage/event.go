package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Event struct {
	RequestID             string `json:"request_id,omitempty"`
	EventHash             string `json:"event_hash"`
	TimestampMS           int64  `json:"timestamp_ms"`
	Timestamp             string `json:"timestamp"`
	Provider              string `json:"provider,omitempty"`
	ExecutorType          string `json:"executor_type,omitempty"`
	Model                 string `json:"model"`
	RequestedModel        string `json:"requested_model,omitempty"`
	ResolvedModel         string `json:"resolved_model,omitempty"`
	Endpoint              string `json:"endpoint,omitempty"`
	Method                string `json:"method,omitempty"`
	Path                  string `json:"path,omitempty"`
	AuthType              string `json:"auth_type,omitempty"`
	AuthIndex             string `json:"auth_index,omitempty"`
	Source                string `json:"source,omitempty"`
	SourceHash            string `json:"source_hash,omitempty"`
	APIKeyHash            string `json:"api_key_hash,omitempty"`
	AccountSnapshot       string `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot     string `json:"auth_label_snapshot,omitempty"`
	AuthFileSnapshot      string `json:"auth_file_snapshot,omitempty"`
	AuthProviderSnapshot  string `json:"auth_provider_snapshot,omitempty"`
	AuthProjectIDSnapshot string `json:"auth_project_id_snapshot,omitempty"`
	AuthSnapshotAtMS      int64  `json:"auth_snapshot_at_ms,omitempty"`
	// ReasoningEffort is the request-side effort setting added by CPA v7.1.18+.
	// It is not the same as response-side tokens.reasoning_tokens usage.
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	ServiceTier         string `json:"service_tier,omitempty"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	ReasoningTokens     int64  `json:"reasoning_tokens"`
	CachedTokens        int64  `json:"cached_tokens"`
	CacheTokens         int64  `json:"cache_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	TotalTokens         int64  `json:"total_tokens"`
	LatencyMS           *int64 `json:"latency_ms,omitempty"`
	TTFTMS              *int64 `json:"ttft_ms,omitempty"`
	Failed              bool   `json:"failed"`
	FailStatusCode      int    `json:"fail_status_code,omitempty"`
	FailSummary         string `json:"fail_summary,omitempty"`
	// FailBody is retained only in the local DB as a sensitive internal field.
	// Public APIs, compatible payloads, and exports must use FailSummary instead.
	FailBody               string                  `json:"-"`
	ResponseMetadata       *ResponseHeaderMetadata `json:"response_metadata,omitempty"`
	ResponseMetadataJSON   string                  `json:"-"`
	HeaderQuotaRecoverAtMS int64                   `json:"header_quota_recover_at_ms,omitempty"`
	HeaderQuotaUsedPercent *float64                `json:"header_quota_used_percent,omitempty"`
	HeaderQuotaPlanType    string                  `json:"header_quota_plan_type,omitempty"`
	HeaderErrorKind        string                  `json:"header_error_kind,omitempty"`
	HeaderErrorCode        string                  `json:"header_error_code,omitempty"`
	HeaderTraceID          string                  `json:"header_trace_id,omitempty"`
	RawJSON                string                  `json:"raw_json,omitempty"`
	CreatedAtMS            int64                   `json:"created_at_ms"`
}

type Tokens struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheTokens         int64 `json:"cache_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

// LongContextTokens preserves the portions of token aggregates that came from
// requests over a model-specific context threshold. It stays internal to
// aggregation and pricing so public usage payloads remain backward compatible.
type LongContextTokens struct {
	LongInputTokens         int64
	LongOutputTokens        int64
	LongCachedTokens        int64
	LongCacheReadTokens     int64
	LongCacheCreationTokens int64
}

func (tokens *LongContextTokens) AddIfLongContext(input, output, cached, cacheRead, cacheCreation int64) {
	if tokens == nil || !IsLongContextInput(input) {
		return
	}
	tokens.LongInputTokens += input
	tokens.LongOutputTokens += output
	tokens.LongCachedTokens += cached
	tokens.LongCacheReadTokens += cacheRead
	tokens.LongCacheCreationTokens += cacheCreation
}

type Detail struct {
	Timestamp             string                  `json:"timestamp"`
	Source                string                  `json:"source"`
	AuthIndex             string                  `json:"auth_index,omitempty"`
	APIKeyHash            string                  `json:"api_key_hash,omitempty"`
	AccountSnapshot       string                  `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot     string                  `json:"auth_label_snapshot,omitempty"`
	AuthFileSnapshot      string                  `json:"auth_file_snapshot,omitempty"`
	AuthProviderSnapshot  string                  `json:"auth_provider_snapshot,omitempty"`
	AuthProjectIDSnapshot string                  `json:"auth_project_id_snapshot,omitempty"`
	AuthSnapshotAtMS      int64                   `json:"auth_snapshot_at_ms,omitempty"`
	LatencyMS             *int64                  `json:"latency_ms,omitempty"`
	TTFTMS                *int64                  `json:"ttft_ms,omitempty"`
	ResolvedModel         string                  `json:"resolved_model,omitempty"`
	ReasoningEffort       string                  `json:"reasoning_effort,omitempty"`
	ServiceTier           string                  `json:"service_tier,omitempty"`
	ExecutorType          string                  `json:"executor_type,omitempty"`
	Tokens                Tokens                  `json:"tokens"`
	Failed                bool                    `json:"failed"`
	FailStatusCode        int                     `json:"fail_status_code,omitempty"`
	FailSummary           string                  `json:"fail_summary,omitempty"`
	ResponseMetadata      *ResponseHeaderMetadata `json:"response_metadata,omitempty"`
}

type ModelAggregate struct {
	Details []Detail `json:"details"`
}

type APIAggregate struct {
	Models map[string]*ModelAggregate `json:"models"`
}

type Payload struct {
	TotalRequests int64                    `json:"total_requests"`
	SuccessCount  int64                    `json:"success_count"`
	FailureCount  int64                    `json:"failure_count"`
	TotalTokens   int64                    `json:"total_tokens"`
	APIs          map[string]*APIAggregate `json:"apis"`
}

const (
	maxFailSummaryBytes            = 4096
	LongContextInputTokenThreshold = int64(272_000)
)

func IsLongContextInput(inputTokens int64) bool {
	return inputTokens > LongContextInputTokenThreshold
}

var (
	endpointPattern          = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE|OPTIONS|HEAD)\s+(\S+)`)
	authorizationHeaderRegex = regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*)(?:bearer\s+)?[^\s,"'{}]+`)
	bearerTokenRegex         = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`)
	apiKeyTokenRegex         = regexp.MustCompile(`(sk-proj-[A-Za-z0-9-_]{6,}|sk-ant-[A-Za-z0-9-_]{6,}|sk-[A-Za-z0-9-_]{6,}|sess-[A-Za-z0-9-_]{6,}|ghp_[A-Za-z0-9]{6,}|github_pat_[A-Za-z0-9_]{20,}|AIza[0-9A-Za-z-_]{8,}|hf_[A-Za-z0-9]{6,}|pk_[A-Za-z0-9]{6,}|rk_[A-Za-z0-9]{6,})`)
	tokenFieldRegex          = regexp.MustCompile(`(?i)\b(access_token|refresh_token|id_token)\b(\s*["']?\s*[:=]\s*["']?)[^"',\s&}]+`)
	apiKeyFieldRegex         = regexp.MustCompile(`(?i)\b(api[-_ ]?key|x-api-key)\b(\s*["']?\s*[:=]\s*["']?)[^"',\s&}]+`)
	cookieJSONFieldRegex     = regexp.MustCompile(`(?i)("?(?:cookie|set-cookie)"?\s*:\s*")[^"]*(")`)
	cookieHeaderRegex        = regexp.MustCompile(`(?i)\b(cookie|set-cookie)\s*:\s*[^,\r\n"}]+`)
	emailRegex               = regexp.MustCompile(`([A-Za-z0-9._%+\-])([A-Za-z0-9._%+\-]*)(@[A-Za-z0-9.\-]+\.[A-Za-z]{2,})`)
)

// CompatibleCachedTokens returns the legacy cached_tokens value after removing
// fine-grained cache dimensions. CPA's Claude parser mirrors cache read/create
// values into cached_tokens for older consumers, so public payloads must not
// expose both as independently addable token buckets.
func CompatibleCachedTokens(cachedTokens, cacheTokens, cacheReadTokens, cacheCreationTokens int64) int64 {
	cached := cachedTokens
	if cacheTokens > cached {
		cached = cacheTokens
	}
	if cached <= 0 {
		return 0
	}
	fineGrained := int64(0)
	if cacheReadTokens > 0 {
		fineGrained += cacheReadTokens
	}
	if cacheCreationTokens > 0 {
		fineGrained += cacheCreationTokens
	}
	if cached <= fineGrained {
		return 0
	}
	return cached - fineGrained
}

func NormalizeRaw(raw []byte) (Event, error) {
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Event{}, err
	}
	record, ok := payload.(map[string]any)
	if !ok {
		return Event{}, fmt.Errorf("usage payload is not a JSON object")
	}

	timestampMS, timestamp := readTimestamp(record)
	method := strings.ToUpper(readString(record, "method", "http_method", "httpMethod"))
	path := readString(record, "path", "url_path", "urlPath", "route")
	endpoint := readString(record, "endpoint", "api", "request", "operation")
	if endpoint == "" && method != "" && path != "" {
		endpoint = method + " " + path
	}
	if endpoint != "" {
		if match := endpointPattern.FindStringSubmatch(endpoint); len(match) == 3 {
			if method == "" {
				method = strings.ToUpper(match[1])
			}
			if path == "" {
				path = match[2]
			}
		}
	}
	if endpoint == "" {
		endpoint = "-"
	}

	inputTokens, outputTokens, reasoningTokens, cachedTokens, cacheTokens, cacheReadTokens, cacheCreationTokens, totalTokens := readTokenFields(record)
	if totalTokens <= 0 {
		totalTokens = inputTokens + outputTokens + reasoningTokens +
			CompatibleCachedTokens(cachedTokens, cacheTokens, cacheReadTokens, cacheCreationTokens) +
			maxInt64(cacheReadTokens, 0) + maxInt64(cacheCreationTokens, 0)
	}

	latencyMS := readOptionalInt(record, "latency_ms", "latencyMs", "duration_ms", "durationMs", "elapsed_ms", "elapsedMs")
	ttftMS := readOptionalInt(record, "ttft_ms", "ttftMs", "time_to_first_token_ms", "timeToFirstTokenMs")
	failed := readFailed(record)
	failStatusCode, failBody := readFailFields(record)
	failSummary := FailSummaryFromBody(failBody)
	redacted := redactValue(payload)
	redactedJSON, _ := json.Marshal(redacted)
	sourceRaw := readString(record, "source", "api_key", "apiKey", "key", "account", "email")
	source := maskSource(sourceRaw)
	apiKey := readString(record, "api_key", "apiKey", "key")
	authIndex := readString(record, "auth_index", "authIndex", "AuthIndex")
	requestedModel := readString(record, "alias", "requested_model", "requestedModel")
	resolvedModel := readString(record, "model", "model_name", "modelName", "resolved_model", "resolvedModel")
	model := requestedModel
	if model == "" {
		model = resolvedModel
	}

	event := Event{
		RequestID:             readString(record, "request_id", "requestId", "id"),
		TimestampMS:           timestampMS,
		Timestamp:             timestamp,
		Provider:              readString(record, "provider", "type", "auth_type", "authType"),
		ExecutorType:          readString(record, "executor_type", "executorType"),
		Model:                 model,
		RequestedModel:        requestedModel,
		ResolvedModel:         resolvedModel,
		Endpoint:              endpoint,
		Method:                method,
		Path:                  path,
		AuthType:              readString(record, "auth_type", "authType"),
		AuthIndex:             authIndex,
		Source:                source,
		SourceHash:            hashString(sourceRaw),
		APIKeyHash:            hashString(apiKey),
		AccountSnapshot:       readString(record, "account_snapshot", "accountSnapshot"),
		AuthLabelSnapshot:     readString(record, "auth_label_snapshot", "authLabelSnapshot"),
		AuthFileSnapshot:      readString(record, "auth_file_snapshot", "authFileSnapshot"),
		AuthProviderSnapshot:  readString(record, "auth_provider_snapshot", "authProviderSnapshot"),
		AuthProjectIDSnapshot: readString(record, "auth_project_id_snapshot", "authProjectIdSnapshot", "project_id", "projectId"),
		AuthSnapshotAtMS:      readInt(record, "auth_snapshot_at_ms", "authSnapshotAtMs"),
		ReasoningEffort:       readString(record, "reasoning_effort", "reasoningEffort"),
		ServiceTier:           readString(record, "service_tier", "serviceTier"),
		InputTokens:           inputTokens,
		OutputTokens:          outputTokens,
		ReasoningTokens:       reasoningTokens,
		CachedTokens:          cachedTokens,
		CacheTokens:           cacheTokens,
		CacheReadTokens:       cacheReadTokens,
		CacheCreationTokens:   cacheCreationTokens,
		TotalTokens:           totalTokens,
		LatencyMS:             latencyMS,
		TTFTMS:                ttftMS,
		Failed:                failed,
		FailStatusCode:        int(failStatusCode),
		FailSummary:           failSummary,
		FailBody:              failBody,
		RawJSON:               string(redactedJSON),
		CreatedAtMS:           time.Now().UnixMilli(),
	}
	if event.Model == "" {
		event.Model = "-"
	}
	AttachResponseHeaderMetadata(&event, ResponseHeaderMetadataFromRecord(record, time.UnixMilli(timestampMS)))
	event.EventHash = buildEventHash(event)
	return event, nil
}

func BuildPayload(events []Event) Payload {
	payload := Payload{APIs: map[string]*APIAggregate{}}
	for _, event := range events {
		payload.TotalRequests++
		if event.Failed {
			payload.FailureCount++
		} else {
			payload.SuccessCount++
		}
		payload.TotalTokens += event.TotalTokens

		endpoint := event.Endpoint
		if endpoint == "" {
			endpoint = "-"
		}
		apiEntry := payload.APIs[endpoint]
		if apiEntry == nil {
			apiEntry = &APIAggregate{Models: map[string]*ModelAggregate{}}
			payload.APIs[endpoint] = apiEntry
		}
		model := event.Model
		if model == "" {
			model = "-"
		}
		modelEntry := apiEntry.Models[model]
		if modelEntry == nil {
			modelEntry = &ModelAggregate{}
			apiEntry.Models[model] = modelEntry
		}
		compatCachedTokens := CompatibleCachedTokens(
			event.CachedTokens,
			event.CacheTokens,
			event.CacheReadTokens,
			event.CacheCreationTokens,
		)
		modelEntry.Details = append(modelEntry.Details, Detail{
			Timestamp:             event.Timestamp,
			Source:                event.Source,
			AuthIndex:             event.AuthIndex,
			APIKeyHash:            event.APIKeyHash,
			AccountSnapshot:       event.AccountSnapshot,
			AuthLabelSnapshot:     event.AuthLabelSnapshot,
			AuthFileSnapshot:      event.AuthFileSnapshot,
			AuthProviderSnapshot:  event.AuthProviderSnapshot,
			AuthProjectIDSnapshot: event.AuthProjectIDSnapshot,
			AuthSnapshotAtMS:      event.AuthSnapshotAtMS,
			LatencyMS:             event.LatencyMS,
			TTFTMS:                event.TTFTMS,
			ResolvedModel:         event.ResolvedModel,
			ReasoningEffort:       event.ReasoningEffort,
			ServiceTier:           event.ServiceTier,
			ExecutorType:          event.ExecutorType,
			Failed:                event.Failed,
			FailStatusCode:        event.FailStatusCode,
			FailSummary:           event.FailSummary,
			ResponseMetadata:      event.ResponseMetadata,
			Tokens: Tokens{
				InputTokens:         event.InputTokens,
				OutputTokens:        event.OutputTokens,
				ReasoningTokens:     event.ReasoningTokens,
				CachedTokens:        compatCachedTokens,
				CacheTokens:         compatCachedTokens,
				CacheReadTokens:     event.CacheReadTokens,
				CacheCreationTokens: event.CacheCreationTokens,
				TotalTokens:         event.TotalTokens,
			},
		})
	}
	return payload
}

func readTimestamp(record map[string]any) (int64, string) {
	raw := first(record, "timestamp", "time", "created_at", "createdAt", "created", "request_time", "requestTime")
	now := time.Now()
	if raw == nil {
		return now.UnixMilli(), now.UTC().Format(time.RFC3339Nano)
	}
	switch value := raw.(type) {
	case float64:
		ms := int64(value)
		if ms < 10_000_000_000 {
			ms *= 1000
		}
		return ms, time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
	case string:
		trimmed := strings.TrimSpace(value)
		if number, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			if number < 10_000_000_000 {
				number *= 1000
			}
			return number, time.UnixMilli(number).UTC().Format(time.RFC3339Nano)
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
			if parsed, err := time.Parse(layout, trimmed); err == nil {
				return parsed.UnixMilli(), parsed.UTC().Format(time.RFC3339Nano)
			}
		}
	}
	return now.UnixMilli(), now.UTC().Format(time.RFC3339Nano)
}

func readTokenFields(record map[string]any) (int64, int64, int64, int64, int64, int64, int64, int64) {
	tokens := map[string]any{}
	if nested, ok := first(record, "tokens", "usage").(map[string]any); ok {
		tokens = nested
	}
	input := readIntFrom(tokens, "input_tokens", "inputTokens", "prompt_tokens", "promptTokens")
	if input == 0 {
		input = readInt(record, "input_tokens", "inputTokens", "prompt_tokens", "promptTokens")
	}
	output := readIntFrom(tokens, "output_tokens", "outputTokens", "completion_tokens", "completionTokens")
	if output == 0 {
		output = readInt(record, "output_tokens", "outputTokens", "completion_tokens", "completionTokens")
	}
	reasoning := readIntFrom(tokens, "reasoning_tokens", "reasoningTokens")
	if reasoning == 0 {
		reasoning = readInt(record, "reasoning_tokens", "reasoningTokens")
	}
	cached := readIntFrom(tokens, "cached_tokens", "cachedTokens")
	if cached == 0 {
		cached = readInt(record, "cached_tokens", "cachedTokens")
	}
	cache := readIntFrom(tokens, "cache_tokens", "cacheTokens")
	if cache == 0 {
		cache = readInt(record, "cache_tokens", "cacheTokens")
	}
	cacheRead := readFirstIntFrom(tokens,
		"cache_read_tokens",
		"cacheReadTokens",
		"cache_read_input_tokens",
		"cacheReadInputTokens",
	)
	if cacheRead == 0 {
		cacheRead = readFirstIntFrom(record,
			"cache_read_tokens",
			"cacheReadTokens",
			"cache_read_input_tokens",
			"cacheReadInputTokens",
		)
	}
	cacheCreation := readFirstIntFrom(tokens,
		"cache_creation_tokens",
		"cacheCreationTokens",
		"cache_creation_input_tokens",
		"cacheCreationInputTokens",
		"cache_write_tokens",
		"cacheWriteTokens",
		"cache_write_input_tokens",
		"cacheWriteInputTokens",
	)
	if cacheCreation == 0 {
		cacheCreation = readFirstIntFrom(record,
			"cache_creation_tokens",
			"cacheCreationTokens",
			"cache_creation_input_tokens",
			"cacheCreationInputTokens",
			"cache_write_tokens",
			"cacheWriteTokens",
			"cache_write_input_tokens",
			"cacheWriteInputTokens",
		)
	}
	total := readIntFrom(tokens, "total_tokens", "totalTokens", "total")
	if total == 0 {
		total = readInt(record, "total_tokens", "totalTokens", "total")
	}
	return input, output, reasoning, cached, cache, cacheRead, cacheCreation, total
}

func readFailed(record map[string]any) bool {
	if value, ok := first(record, "failed", "is_failed", "isFailed").(bool); ok {
		return value
	}
	if value, ok := first(record, "success", "ok").(bool); ok {
		return !value
	}
	status := readInt(record, "status", "status_code", "statusCode", "http_status", "httpStatus")
	if status >= 400 {
		return true
	}
	return first(record, "error", "error_message", "errorMessage") != nil
}

func readFailFields(record map[string]any) (int64, string) {
	fail := map[string]any{}
	if nested, ok := first(record, "fail").(map[string]any); ok {
		fail = nested
	}
	statusCode := readIntFrom(fail, "status_code", "statusCode")
	if statusCode == 0 {
		statusCode = readInt(record, "fail_status_code", "failStatusCode")
	}
	body := readString(fail, "body")
	if body == "" {
		body = readString(record, "fail_body", "failBody")
	}
	if headers, ok := compactJSON(first(record, "response_headers", "responseHeaders", "headers")); ok && headers != "{}" && headers != "[]" {
		if body == "" {
			body = headers
		} else {
			body = body + "\n" + headers
		}
	}
	return statusCode, body
}

func compactJSON(value any) (string, bool) {
	if value == nil {
		return "", false
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	return string(data), true
}

func readOptionalInt(record map[string]any, keys ...string) *int64 {
	value := readInt(record, keys...)
	if value == 0 && first(record, keys...) == nil {
		return nil
	}
	return &value
}

func readString(record map[string]any, keys ...string) string {
	raw := first(record, keys...)
	if raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case json.Number:
		return value.String()
	case float64:
		if value == float64(int64(value)) {
			return strconv.FormatInt(int64(value), 10)
		}
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func readInt(record map[string]any, keys ...string) int64 {
	return readIntFrom(record, keys...)
}

func readFirstIntFrom(record map[string]any, keys ...string) int64 {
	for _, key := range keys {
		value := readIntFrom(record, key)
		if value != 0 {
			return value
		}
	}
	return 0
}

func readIntFrom(record map[string]any, keys ...string) int64 {
	raw := first(record, keys...)
	switch value := raw.(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	case json.Number:
		number, _ := value.Int64()
		return number
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return parsed
	default:
		return 0
	}
}

func first(record map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := record[key]; ok {
			return value
		}
	}
	return nil
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func hashString(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:])
}

func buildEventHash(event Event) string {
	parts := []string{
		event.RequestID,
		event.Timestamp,
		event.Endpoint,
		event.Model,
		event.AuthIndex,
		event.SourceHash,
		strconv.FormatInt(event.InputTokens, 10),
		strconv.FormatInt(event.OutputTokens, 10),
		strconv.FormatInt(event.ReasoningTokens, 10),
		strconv.FormatInt(maxInt64(event.CachedTokens, event.CacheTokens), 10),
		strconv.FormatBool(event.Failed),
	}
	if event.LatencyMS != nil {
		parts = append(parts, strconv.FormatInt(*event.LatencyMS, 10))
	}
	return hashString(strings.Join(parts, "|"))
}

func maskSource(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "@") {
		parts := strings.SplitN(trimmed, "@", 2)
		prefix := parts[0]
		if len(prefix) > 3 {
			prefix = prefix[:3]
		}
		return prefix + "***@" + parts[1]
	}
	if looksSecret(trimmed) {
		if len(trimmed) <= 8 {
			return "m:****"
		}
		return "m:" + trimmed[:4] + "..." + trimmed[len(trimmed)-4:]
	}
	return trimmed
}

func looksSecret(value string) bool {
	if strings.ContainsAny(value, " /\\") {
		return false
	}
	return strings.HasPrefix(value, "sk-") || strings.HasPrefix(value, "AIza") || len(value) >= 32
}

func FailSummaryFromBody(body string) string {
	summary := strings.TrimSpace(body)
	if summary == "" {
		return ""
	}
	summary = authorizationHeaderRegex.ReplaceAllString(summary, `${1}[redacted]`)
	summary = bearerTokenRegex.ReplaceAllString(summary, `Bearer [redacted]`)
	summary = tokenFieldRegex.ReplaceAllString(summary, `${1}${2}[redacted]`)
	summary = apiKeyFieldRegex.ReplaceAllString(summary, `${1}${2}[redacted]`)
	summary = apiKeyTokenRegex.ReplaceAllString(summary, `[redacted]`)
	summary = cookieJSONFieldRegex.ReplaceAllString(summary, `${1}[redacted]${2}`)
	summary = cookieHeaderRegex.ReplaceAllString(summary, `${1}: [redacted]`)
	summary = emailRegex.ReplaceAllString(summary, `${1}***${3}`)
	return truncateUTF8Bytes(strings.TrimSpace(summary), maxFailSummaryBytes)
}

func SafeRawJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
		redacted, err := json.Marshal(redactValue(payload))
		if err == nil {
			return string(redacted)
		}
	}
	return FailSummaryFromBody(trimmed)
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	var builder strings.Builder
	for _, r := range value {
		size := utf8.RuneLen(r)
		if size < 0 {
			size = len(string(r))
		}
		if builder.Len()+size > maxBytes {
			break
		}
		builder.WriteRune(r)
	}
	return strings.TrimSpace(builder.String()) + "..."
}

func redactValue(value any) any {
	return redactValueWithParent("", value)
}

func redactValueWithParent(parentKey string, value any) any {
	switch item := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(item))
		for key, child := range item {
			normalizedKey := normalizeSecretKey(key)
			if isSecretKey(key) {
				result[key] = "[redacted]"
				continue
			}
			if normalizedKey == "fail_body" || (parentKey == "fail" && normalizedKey == "body") {
				result[key] = FailSummaryFromBody(stringValue(child))
				continue
			}
			result[key] = redactValueWithParent(normalizedKey, child)
		}
		return result
	case []any:
		result := make([]any, 0, len(item))
		for _, child := range item {
			result = append(result, redactValueWithParent(parentKey, child))
		}
		return result
	default:
		return value
	}
}

func isSecretKey(key string) bool {
	normalized := normalizeSecretKey(key)
	return normalized == "api_key" ||
		normalized == "apikey" ||
		normalized == "authorization" ||
		normalized == "cookie" ||
		normalized == "set_cookie" ||
		normalized == "access_token" ||
		normalized == "refresh_token" ||
		normalized == "id_token" ||
		normalized == "token" ||
		strings.Contains(normalized, "secret")
}

func normalizeSecretKey(key string) string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
	normalized = strings.ReplaceAll(normalized, " ", "_")
	return normalized
}

func stringValue(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}
