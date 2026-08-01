package usage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseResponseHeaderMetadataCodexQuotaAndTrace(t *testing.T) {
	base := time.Unix(1_780_000_000, 0)
	metadata := ParseResponseHeaderMetadata(map[string]any{
		"X-Codex-Plan-Type":                            []any{"plus"},
		"X-Codex-Active-Limit":                         []any{"premium"},
		"X-Codex-Primary-Used-Percent":                 []any{"87"},
		"X-Codex-Secondary-Used-Percent":               []any{"12"},
		"X-Codex-Primary-Reset-After-Seconds":          []any{"60"},
		"X-Codex-Secondary-Reset-At":                   []any{"1780003600"},
		"X-Codex-Rate-Limit-Reached-Type":              []any{"primary"},
		"X-OAI-Request-ID":                             []any{"req_123"},
		"CF-Ray":                                       []any{"ray-abc"},
		"Content-Type":                                 []any{"text/event-stream"},
		"Set-Cookie":                                   []any{"session=secret"},
		"Authorization":                                []any{"Bearer secret"},
		"X-Codex-Primary-Over-Secondary-Limit-Percent": []any{"50"},
	}, base)
	if metadata == nil {
		t.Fatal("metadata is nil")
	}
	if metadata.Quota == nil || metadata.Quota.PlanType != "plus" || metadata.Quota.ActiveLimit != "premium" {
		t.Fatalf("quota metadata = %#v", metadata.Quota)
	}
	if metadata.Quota.Primary == nil || metadata.Quota.Primary.UsedPercent == nil || *metadata.Quota.Primary.UsedPercent != 87 {
		t.Fatalf("primary quota = %#v", metadata.Quota.Primary)
	}
	if metadata.Quota.Primary.ResetAtMS != base.Add(time.Minute).UnixMilli() {
		t.Fatalf("primary reset = %d, want %d", metadata.Quota.Primary.ResetAtMS, base.Add(time.Minute).UnixMilli())
	}
	if metadata.Quota.Secondary == nil || metadata.Quota.Secondary.ResetAtMS != time.Unix(1_780_003_600, 0).UnixMilli() {
		t.Fatalf("secondary quota = %#v", metadata.Quota.Secondary)
	}
	if metadata.Trace == nil || metadata.Trace.PrimaryTraceID != "req_123" || metadata.Trace.CFRay != "ray-abc" {
		t.Fatalf("trace metadata = %#v", metadata.Trace)
	}
	if metadata.Response == nil || metadata.Response.ContentType != "text/event-stream" {
		t.Fatalf("response metadata = %#v", metadata.Response)
	}
	derived := DeriveResponseHeaderMetadata(metadata)
	if derived.MetadataJSON == "" || derived.QuotaPlanType != "plus" || derived.TraceID != "req_123" {
		t.Fatalf("derived metadata = %#v", derived)
	}
}

func TestParseResponseHeaderMetadataKeepsRecoverAtOnSummaryWindow(t *testing.T) {
	base := time.Unix(1_780_000_000, 0)
	metadata := ParseResponseHeaderMetadata(map[string]any{
		"X-Codex-Primary-Used-Percent":        []any{"100"},
		"X-Codex-Primary-Reset-After-Seconds": []any{"18000"},
		"X-Codex-Primary-Window-Minutes":      []any{"300"},
		"X-Codex-Secondary-Used-Percent":      []any{"20"},
		"X-Codex-Secondary-Reset-At":          []any{base.Add(7 * 24 * time.Hour).UnixMilli()},
		"X-Codex-Secondary-Window-Minutes":    []any{"10080"},
	}, base)
	if metadata == nil || metadata.Quota == nil {
		t.Fatalf("quota metadata missing: %#v", metadata)
	}
	if metadata.Quota.UsedPercent == nil || *metadata.Quota.UsedPercent != 100 {
		t.Fatalf("used percent = %#v", metadata.Quota.UsedPercent)
	}
	if got, want := metadata.Quota.RecoverAtMS, base.Add(5*time.Hour).UnixMilli(); got != want {
		t.Fatalf("recover at = %d, want %d", got, want)
	}
	if metadata.Quota.SummaryWindowKind != "five_hour" || metadata.Quota.SummaryWindowSource != "primary" {
		t.Fatalf("summary window = %s/%s", metadata.Quota.SummaryWindowKind, metadata.Quota.SummaryWindowSource)
	}
	if metadata.Quota.ReachedWindowKind != "five_hour" || metadata.Quota.ReachedWindowSource != "primary" {
		t.Fatalf("reached window = %s/%s", metadata.Quota.ReachedWindowKind, metadata.Quota.ReachedWindowSource)
	}
}

func TestParseResponseHeaderMetadataSummarizesHighestUsedNonReachedWindow(t *testing.T) {
	base := time.Unix(1_780_000_000, 0)
	metadata := ParseResponseHeaderMetadata(map[string]any{
		"X-Codex-Primary-Used-Percent":        []any{"80"},
		"X-Codex-Primary-Reset-After-Seconds": []any{"18000"},
		"X-Codex-Primary-Window-Minutes":      []any{"300"},
		"X-Codex-Secondary-Used-Percent":      []any{"95"},
		"X-Codex-Secondary-Reset-At":          []any{base.Add(7 * 24 * time.Hour).UnixMilli()},
		"X-Codex-Secondary-Window-Minutes":    []any{"10080"},
	}, base)
	if metadata == nil || metadata.Quota == nil {
		t.Fatalf("quota metadata missing: %#v", metadata)
	}
	if metadata.Quota.UsedPercent == nil || *metadata.Quota.UsedPercent != 95 {
		t.Fatalf("used percent = %#v", metadata.Quota.UsedPercent)
	}
	if got, want := metadata.Quota.RecoverAtMS, base.Add(7*24*time.Hour).UnixMilli(); got != want {
		t.Fatalf("recover at = %d, want %d", got, want)
	}
	if metadata.Quota.SummaryWindowKind != "weekly" || metadata.Quota.SummaryWindowSource != "secondary" {
		t.Fatalf("summary window = %s/%s", metadata.Quota.SummaryWindowKind, metadata.Quota.SummaryWindowSource)
	}
	if metadata.Quota.ReachedWindowKind != "" || metadata.Quota.ReachedWindowSource != "" {
		t.Fatalf("reached window = %s/%s", metadata.Quota.ReachedWindowKind, metadata.Quota.ReachedWindowSource)
	}
}

func TestParseResponseHeaderMetadataErrors(t *testing.T) {
	base := time.Unix(1_780_000_000, 0)
	metadata := ParseResponseHeaderMetadata(map[string]any{
		"Retry-After":                  []any{"120"},
		"X-OpenAI-IDE-Error-Code":      []any{"token_invalidated"},
		"X-OpenAI-Authorization-Error": []any{"identity_edge_internal_error"},
		"X-OpenAI-IDE-Root-Error-Code": []any{"token_revoked"},
		"X-RateLimit-Bypass":           []any{"ModelRequestRateLimit"},
		"X-Should-Retry":               []any{"false"},
		"X-CloudAICompanion-Trace-ID":  []any{"ag-trace"},
		"Server-Timing":                []any{"dur=42"},
		"X-MiFE-Upstream-Status":       []any{"200"},
		"X-OneAPI-Request-ID":          []any{"oneapi-1"},
		"X-Zeabur-Request-ID":          []any{"z-1"},
	}, base)
	if metadata == nil || metadata.Errors == nil {
		t.Fatalf("errors metadata missing: %#v", metadata)
	}
	if metadata.Errors.Kind != "auth" || metadata.Errors.Code != "token_revoked" {
		t.Fatalf("errors = %#v", metadata.Errors)
	}
	if metadata.Errors.RetryAfterRecoverAtMS != base.Add(120*time.Second).UnixMilli() {
		t.Fatalf("retry-after recover = %d", metadata.Errors.RetryAfterRecoverAtMS)
	}
	if metadata.Errors.ShouldRetry == nil || *metadata.Errors.ShouldRetry {
		t.Fatalf("should retry = %#v, want false", metadata.Errors.ShouldRetry)
	}
	if metadata.Providers == nil ||
		metadata.Providers.AntigravityTraceID != "ag-trace" ||
		metadata.Providers.MiFEUpstreamStatus != "200" ||
		metadata.Providers.OneAPIRequestID != "oneapi-1" {
		t.Fatalf("provider metadata = %#v", metadata.Providers)
	}
}

func TestParseResponseHeaderMetadataXAIDiagnostics(t *testing.T) {
	metadata := ParseResponseHeaderMetadata(map[string]any{
		"X-Ratelimit-Limit-Requests":     []any{"21"},
		"X-Ratelimit-Remaining-Requests": []any{"20"},
		"X-Ratelimit-Limit-Tokens":       []any{"1000000"},
		"X-Ratelimit-Remaining-Tokens":   []any{"999000"},
		"X-Data-Retention":               []any{"zdr"},
		"X-Zero-Data-Retention":          []any{"true"},
		"Traceparent":                    []any{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		"X-Internal-Token":               []any{"must-not-leak"},
	}, time.Unix(1_780_000_000, 0))
	if metadata == nil || metadata.RateLimit == nil || metadata.DataPolicy == nil || metadata.Trace == nil {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata.RateLimit.Requests == nil || metadata.RateLimit.Requests.Limit == nil || *metadata.RateLimit.Requests.Limit != 21 || metadata.RateLimit.Requests.Remaining == nil || *metadata.RateLimit.Requests.Remaining != 20 {
		t.Fatalf("request rate limit = %#v", metadata.RateLimit.Requests)
	}
	if metadata.RateLimit.Tokens == nil || metadata.RateLimit.Tokens.Limit == nil || *metadata.RateLimit.Tokens.Limit != 1_000_000 || metadata.RateLimit.Tokens.Remaining == nil || *metadata.RateLimit.Tokens.Remaining != 999_000 {
		t.Fatalf("token rate limit = %#v", metadata.RateLimit.Tokens)
	}
	if metadata.DataPolicy.RetentionMode != "zdr" || metadata.DataPolicy.ZeroRetention == nil || !*metadata.DataPolicy.ZeroRetention {
		t.Fatalf("data policy = %#v", metadata.DataPolicy)
	}
	if metadata.Trace.Traceparent != "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" {
		t.Fatalf("traceparent = %#v", metadata.Trace.Traceparent)
	}
	if metadata.Trace.PrimaryTraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("primary trace id should be compact W3C trace-id, got %#v", metadata.Trace)
	}
}

func TestParseResponseHeaderMetadataRejectsInvalidTraceparent(t *testing.T) {
	metadata := ParseResponseHeaderMetadata(map[string]any{
		"Traceparent":  []any{"Bearer sk-sensitive"},
		"X-Request-ID": []any{"req-safe"},
	}, time.Unix(1_780_000_000, 0))
	if metadata == nil || metadata.Trace == nil {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata.Trace.Traceparent != "" || metadata.Trace.PrimaryTraceID != "req-safe" {
		t.Fatalf("trace = %#v", metadata.Trace)
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if strings.Contains(string(data), "sk-sensitive") {
		t.Fatalf("metadata leaked invalid traceparent: %s", data)
	}

	for _, invalid := range []string{
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
	} {
		if got := normalizeTraceparent(invalid); got != "" {
			t.Fatalf("normalizeTraceparent(%q) = %q", invalid, got)
		}
	}
}

func TestResponseHeaderMetadataFromRecordMergesXAIProviderUsage(t *testing.T) {
	base := time.Unix(1_784_543_105, 0)
	metadata := ResponseHeaderMetadataFromRecord(map[string]any{
		"provider": "xai",
		"fail": map[string]any{
			"status_code": 429,
			"body":        `{"code":"subscription:free-usage-exhausted","error":"You've used all the included free usage for model grok-4.5-build-free for now. Usage resets over a rolling 24-hour window — tokens (actual/limit): 1024413/1000000."}`,
		},
		"response_headers": map[string]any{
			"Retry-After": []any{"90"},
		},
	}, base)
	if metadata == nil || metadata.ProviderUsage == nil {
		t.Fatalf("metadata = %#v", metadata)
	}
	// Transport Retry-After must not override free-usage rolling-window recovery.
	if metadata.ProviderUsage.RecoverAtMS != base.Add(24*time.Hour).UnixMilli() || !metadata.ProviderUsage.RecoverAtEstimated {
		t.Fatalf("provider usage recovery = %#v", metadata.ProviderUsage)
	}
	if metadata.Errors == nil || metadata.Errors.RetryAfterRecoverAtMS != base.Add(90*time.Second).UnixMilli() {
		t.Fatalf("transport retry-after should remain on errors = %#v", metadata.Errors)
	}
}

func TestResponseHeaderMetadataFromRecordEnrichesExistingMetadataWithHeaders(t *testing.T) {
	metadata := ResponseHeaderMetadataFromRecord(map[string]any{
		"response_metadata": map[string]any{
			"trace": map[string]any{
				"request_id":        "req-existing",
				"client_request_id": "client-existing",
			},
		},
		"raw_json": `{"response_headers":{"X-Ratelimit-Limit-Requests":["21"],"X-Ratelimit-Remaining-Requests":["20"]}}`,
		"response_headers": map[string]any{
			"X-Data-Retention": []any{"zdr"},
			"X-Zero-Retention": []any{"true"},
		},
	}, time.Unix(1_780_000_000, 0))
	if metadata == nil {
		t.Fatal("metadata is nil")
	}
	if metadata.Trace == nil || metadata.Trace.RequestID != "req-existing" || metadata.Trace.ClientRequestID != "client-existing" {
		t.Fatalf("existing trace fields were not preserved: %#v", metadata.Trace)
	}
	if metadata.RateLimit == nil || metadata.RateLimit.Requests == nil || metadata.RateLimit.Requests.Remaining == nil || *metadata.RateLimit.Requests.Remaining != 20 {
		t.Fatalf("same-record rate limit headers were ignored: %#v", metadata.RateLimit)
	}
	if metadata.DataPolicy == nil || metadata.DataPolicy.ZeroRetention == nil || !*metadata.DataPolicy.ZeroRetention {
		t.Fatalf("same-record data policy headers were ignored: %#v", metadata.DataPolicy)
	}
}

func TestMergeResponseHeaderMetadataReplacesStaleEstimatedRecovery(t *testing.T) {
	estimated := true
	reported := false
	existing := &ResponseHeaderMetadata{ProviderUsage: &ProviderUsageMetadata{
		Provider: "xai", Code: xaiFreeUsageExhaustedCode, RecoverAtMS: 1_900_000_000_000, RecoverAtEstimated: estimated,
	}}
	overlay := &ResponseHeaderMetadata{ProviderUsage: &ProviderUsageMetadata{
		Provider: "xai", Code: xaiFreeUsageExhaustedCode, RecoverAtMS: 2_000_000_000_000, RecoverAtEstimated: reported,
	}}
	merged := MergeResponseHeaderMetadata(existing, overlay)
	if merged == nil || merged.ProviderUsage == nil || merged.ProviderUsage.RecoverAtMS != 2_000_000_000_000 || merged.ProviderUsage.RecoverAtEstimated {
		t.Fatalf("stale estimated recovery was not replaced: %#v", merged)
	}
}

func TestMergeResponseHeaderMetadataPreservesReportedRecoveryOverEstimate(t *testing.T) {
	existing := &ResponseHeaderMetadata{ProviderUsage: &ProviderUsageMetadata{
		Provider: "xai", Code: xaiFreeUsageExhaustedCode, RecoverAtMS: 2_000_000_000_000,
	}}
	overlay := &ResponseHeaderMetadata{ProviderUsage: &ProviderUsageMetadata{
		Provider: "xai", Code: xaiFreeUsageExhaustedCode, Actual: int64Pointer(1_024_413),
		RecoverAtMS: 1_900_000_000_000, RecoverAtEstimated: true,
	}}
	merged := MergeResponseHeaderMetadata(existing, overlay)
	if merged == nil || merged.ProviderUsage == nil || merged.ProviderUsage.RecoverAtMS != 2_000_000_000_000 || merged.ProviderUsage.RecoverAtEstimated {
		t.Fatalf("reported recovery was overwritten by estimate: %#v", merged)
	}
	if merged.ProviderUsage.Actual == nil || *merged.ProviderUsage.Actual != 1_024_413 {
		t.Fatalf("overlay usage fields were not merged: %#v", merged.ProviderUsage)
	}
}

func TestResponseHeaderMetadataFromRecordPreservesImportedReportedRecovery(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	reportedRecoverAt := base.Add(6 * time.Hour).UnixMilli()
	metadata := ResponseHeaderMetadataFromRecord(map[string]any{
		"provider": "xai",
		"response_metadata": map[string]any{
			"provider_usage": map[string]any{
				"provider":      "xai",
				"code":          xaiFreeUsageExhaustedCode,
				"recover_at_ms": reportedRecoverAt,
			},
		},
		"raw_json": `{"provider":"xai","fail":{"status_code":429,"body":"{\"code\":\"subscription:free-usage-exhausted\",\"error\":\"tokens (actual/limit): 1024413/1000000\"}"}}`,
	}, base)
	if metadata == nil || metadata.ProviderUsage == nil || metadata.ProviderUsage.RecoverAtMS != reportedRecoverAt || metadata.ProviderUsage.RecoverAtEstimated {
		t.Fatalf("imported reported recovery was not preserved: %#v", metadata)
	}
	if metadata.ProviderUsage.Actual == nil || *metadata.ProviderUsage.Actual != 1_024_413 {
		t.Fatalf("raw usage evidence was not merged: %#v", metadata.ProviderUsage)
	}
}

func TestParseResponseHeaderMetadataRejectsFractionalIntegerHeaders(t *testing.T) {
	metadata := ParseResponseHeaderMetadata(map[string]any{
		"Content-Length":                 []any{"42.5"},
		"X-Ratelimit-Limit-Requests":     []any{"21.5"},
		"X-Ratelimit-Remaining-Requests": []any{"20"},
	}, time.Unix(1_780_000_000, 0))
	if metadata == nil || metadata.Response != nil && metadata.Response.ContentLength != nil {
		t.Fatalf("fractional content length was accepted: %#v", metadata)
	}
	if metadata.RateLimit == nil || metadata.RateLimit.Requests == nil || metadata.RateLimit.Requests.Limit != nil || metadata.RateLimit.Requests.Remaining == nil || *metadata.RateLimit.Requests.Remaining != 20 {
		t.Fatalf("fractional rate limit was accepted: %#v", metadata.RateLimit)
	}
}

func TestResponseHeaderMetadataFromRecordFallsBackToRawJSON(t *testing.T) {
	base := time.Unix(1_780_000_000, 0)
	metadata := ResponseHeaderMetadataFromRecord(map[string]any{
		"raw_json": `{"response_headers":{"X-Request-ID":["req-fallback"],"Content-Length":["42"]}}`,
	}, base)
	if metadata == nil || metadata.Trace == nil || metadata.Trace.PrimaryTraceID != "req-fallback" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata.Response == nil || metadata.Response.ContentLength == nil || *metadata.Response.ContentLength != 42 {
		t.Fatalf("response metadata = %#v", metadata.Response)
	}
}

func TestParseResponseHeaderMetadataIgnoresNonScalarHeaderValues(t *testing.T) {
	metadata := ParseResponseHeaderMetadata(map[string]any{
		"X-OAI-Request-ID": []any{
			map[string]any{"secret": "sk-sensitive"},
			"req-safe",
		},
		"X-Request-ID": map[string]any{"token": "sk-leak"},
		"Set-Cookie":   []any{"session=secret"},
	}, time.Unix(1_780_000_000, 0))
	if metadata == nil || metadata.Trace == nil {
		t.Fatalf("trace metadata missing: %#v", metadata)
	}
	if metadata.Trace.PrimaryTraceID != "req-safe" {
		t.Fatalf("primary trace id = %q", metadata.Trace.PrimaryTraceID)
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if strings.Contains(string(data), "sk-sensitive") || strings.Contains(string(data), "sk-leak") || strings.Contains(string(data), "session=secret") {
		t.Fatalf("metadata leaked unsafe header value: %s", data)
	}
}

func TestParseResponseHeaderMetadataIgnoresFutureHeaderCandidates(t *testing.T) {
	metadata := ParseResponseHeaderMetadata(map[string]any{
		"X-Error-JSON":               []any{"eyJlcnJvciI6eyJjb2RlIjoidG9rZW5fcmV2b2tlZCJ9fQ=="},
		"X-Codex-Promo-Message":      []any{"Start a free trial of Plus today"},
		"X-Codex-Promo-Campaign-ID":  []any{"plus-1-month-free"},
		"X-OpenAI-Internal-Caller":   []any{"unknown_through_ide"},
		"X-Zeabur-IP-Country":        []any{"US"},
		"Date":                       []any{"Tue, 23 Jun 2026 10:48:17 GMT"},
		"Cache-Control":              []any{"no-cache"},
		"Strict-Transport-Security":  []any{"max-age=31536000"},
		"Content-Security-Policy":    []any{"default-src 'none'"},
		"X-Content-Type-Options":     []any{"nosniff"},
		"Cross-Origin-Opener-Policy": []any{"same-origin-allow-popups"},
		"Timing-Allow-Origin":        []any{"*"},
		"Alt-Svc":                    []any{`h3=":443"; ma=86400`},
		"Nel":                        []any{`{"max_age":604800}`},
		"Report-To":                  []any{`{"group":"cf-nel"}`},
		"X-Frame-Options":            []any{"DENY"},
	}, time.Unix(1_780_000_000, 0))
	if metadata != nil {
		t.Fatalf("future header candidates should not be structured yet: %#v", metadata)
	}
}

func TestResponseHeaderMetadataFromRecordSanitizesImportedMetadata(t *testing.T) {
	metadata := ResponseHeaderMetadataFromRecord(map[string]any{
		"response_metadata": map[string]any{
			"errors": map[string]any{
				"authorization_error": "sk-sensitive-token",
				"code":                "token_revoked",
				"kind":                "auth",
			},
			"trace": map[string]any{
				"primary_trace_id": "Bearer secretvalue",
			},
			"response": map[string]any{
				"content_disposition": `attachment; filename="alice@example.com"`,
			},
		},
	}, time.Unix(1_780_000_000, 0))
	if metadata == nil || metadata.Errors == nil || metadata.Trace == nil || metadata.Response == nil {
		t.Fatalf("metadata missing: %#v", metadata)
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	text := string(data)
	for _, secret := range []string{"sk-sensitive-token", "Bearer secretvalue", "alice@example.com"} {
		if strings.Contains(text, secret) {
			t.Fatalf("metadata leaked %q: %s", secret, text)
		}
	}
	if metadata.Errors.Code != "token_revoked" || metadata.Errors.Kind != "auth" {
		t.Fatalf("metadata error classification = %#v", metadata.Errors)
	}
}
