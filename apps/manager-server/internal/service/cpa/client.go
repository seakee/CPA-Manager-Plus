package cpa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type UsageConfig struct {
	UsageStatisticsEnabled          bool `json:"usageStatisticsEnabled"`
	HasUsageStatisticsEnabled       bool `json:"hasUsageStatisticsEnabled,omitempty"`
	RedisUsageQueueRetentionSeconds int  `json:"redisUsageQueueRetentionSeconds"`
	RetentionSourceDefault          bool `json:"retentionSourceDefault"`
	RetentionConfigValid            bool `json:"retentionConfigValid,omitempty"`
	RetentionVerified               bool `json:"retentionVerified,omitempty"`
}
type ManagementConfig struct {
	UsageConfig
	ProxyURL string `json:"proxyUrl,omitempty"`
}

// managementStatusError is a non-2xx answer to a management read. Its message is
// the text every existing caller already matches, so the wire contract of the
// setup paths is unchanged; the status code is exposed for the callers that must
// tell CPA's own 401 apart from an unreachable CPA.
type managementStatusError struct {
	what   string
	code   int
	status string
}

func (e *managementStatusError) Error() string { return e.what + ": " + e.status }

func (e *managementStatusError) Unwrap() error {
	if e == nil {
		return nil
	}
	switch e.code {
	case http.StatusUnauthorized:
		return ErrManagementRejected
	case http.StatusForbidden:
		return ErrManagementClientForbidden
	default:
		return nil
	}
}

func ValidateManagementAPI(ctx context.Context, baseURL string, key string) error {
	_, err := ObserveManagementAPI(ctx, baseURL, key)
	return err
}

// ObserveManagementAPI verifies authenticated access to CPA and returns the
// running version advertised by the Management API, when available.
func ObserveManagementAPI(ctx context.Context, baseURL string, key string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, NormalizeBaseURL(baseURL)+"/v0/management/config", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client := managementHTTPClient(ctx, 30*time.Second, false)
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	ObserveExternalResponseStatus(ctx, res.StatusCode)
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxResponseBytes))
		_ = res.Body.Close()
	}()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", &managementStatusError{what: "management API validation failed", code: res.StatusCode, status: res.Status}
	}
	version := strings.TrimSpace(res.Header.Get("X-CPA-Version"))
	if version == "" {
		version = strings.TrimSpace(res.Header.Get("X-Server-Version"))
	}
	return version, nil
}

func FetchUsageConfig(ctx context.Context, baseURL string, key string) (UsageConfig, error) {
	cfg, err := FetchManagementConfig(ctx, baseURL, key)
	if err != nil {
		return UsageConfig{}, err
	}
	return cfg.UsageConfig, nil
}

func FetchManagementConfig(ctx context.Context, baseURL string, key string) (ManagementConfig, error) {
	if gate := ExternalSavedCredentialGateFromContext(ctx); gate != nil {
		var cfg ManagementConfig
		err := gate(ctx, func(requestCtx context.Context, target ExternalSavedCredentialTarget) error {
			var fetchErr error
			cfg, fetchErr = fetchManagementConfigDirect(requestCtx, target.BaseURL, target.ManagementKey)
			return fetchErr
		})
		return cfg, err
	}
	return fetchManagementConfigDirect(ctx, baseURL, key)
}

func fetchManagementConfigDirect(ctx context.Context, baseURL string, key string) (ManagementConfig, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, NormalizeBaseURL(baseURL)+"/v0/management/config", nil)
	if err != nil {
		return ManagementConfig{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client := managementHTTPClient(ctx, 30*time.Second, false)
	res, err := client.Do(req)
	if err != nil {
		return ManagementConfig{}, err
	}
	defer res.Body.Close()
	ObserveExternalResponseStatus(ctx, res.StatusCode)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ManagementConfig{}, &managementStatusError{what: "management API config request failed", code: res.StatusCode, status: res.Status}
	}
	limited, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil || len(limited) > maxResponseBytes {
		return ManagementConfig{}, errors.New("management API config response invalid")
	}
	return parseManagementConfigPayload(limited)
}

// ObserveUsageAuthorityConfig authenticates against /v0/management/config in a
// single HTTP round-trip and verifies CPA retention metadata when JSON is valid.
// A 2xx response with non-JSON or <= 0 retention succeeds authentication with
// RetentionVerified=false so callers fail closed on cross-run queue continuity.
func ObserveUsageAuthorityConfig(ctx context.Context, baseURL string, key string) (UsageConfig, error) {
	doObserve := func(reqCtx context.Context, targetBaseURL, targetKey string) (UsageConfig, error) {
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, NormalizeBaseURL(targetBaseURL)+"/v0/management/config", nil)
		if err != nil {
			return UsageConfig{}, err
		}
		req.Header.Set("Authorization", "Bearer "+targetKey)
		client := managementHTTPClient(reqCtx, 30*time.Second, false)
		res, err := client.Do(req)
		if err != nil {
			return UsageConfig{}, err
		}
		ObserveExternalResponseStatus(reqCtx, res.StatusCode)
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxResponseBytes))
			return UsageConfig{}, &managementStatusError{what: "management API validation failed", code: res.StatusCode, status: res.Status}
		}
		limited, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
		if err != nil || len(limited) > maxResponseBytes {
			return UsageConfig{RetentionVerified: false}, nil
		}
		parsed, parseErr := parseManagementConfigPayload(limited)
		if parseErr != nil {
			return UsageConfig{RetentionVerified: false}, nil
		}
		return parsed.UsageConfig, nil
	}
	if gate := ExternalSavedCredentialGateFromContext(ctx); gate != nil {
		var out UsageConfig
		err := gate(ctx, func(requestCtx context.Context, target ExternalSavedCredentialTarget) error {
			var fetchErr error
			out, fetchErr = doObserve(requestCtx, target.BaseURL, target.ManagementKey)
			return fetchErr
		})
		return out, err
	}
	return doObserve(ctx, baseURL, key)
}

func parseManagementConfigPayload(limited []byte) (ManagementConfig, error) {
	raw, err := decodeJSONObject(limited)
	if err != nil {
		return ManagementConfig{}, err
	}
	maps := []map[string]any{raw}
	if obsRaw, ok := raw["observability"]; ok && obsRaw != nil {
		obsMap, isMap := obsRaw.(map[string]any)
		if !isMap {
			return ManagementConfig{}, errors.New("invalid observability config object")
		}
		if usageRaw, hasUsage := obsMap["usage"]; hasUsage && usageRaw != nil {
			usageMap, isUsageMap := usageRaw.(map[string]any)
			if !isUsageMap {
				return ManagementConfig{}, errors.New("invalid observability usage config object")
			}
			maps = append(maps, usageMap)
		}
	}
	usageEnabled, hasUsageEnabled, err := inspectBoolCandidates(maps, "usage-statistics-enabled", "usageStatisticsEnabled")
	if err != nil {
		return ManagementConfig{}, err
	}
	retention, hasRetention, err := inspectIntCandidates(maps, "redis-usage-queue-retention-seconds", "redisUsageQueueRetentionSeconds")
	if err != nil {
		return ManagementConfig{}, err
	}
	retentionConfigValid := true
	if !hasRetention {
		retention = 60
	} else if retention <= 0 {
		retentionConfigValid = false
	}
	verified := retentionConfigValid && (!hasUsageEnabled || usageEnabled)
	return ManagementConfig{
		UsageConfig: UsageConfig{
			UsageStatisticsEnabled:          usageEnabled,
			HasUsageStatisticsEnabled:       hasUsageEnabled,
			RedisUsageQueueRetentionSeconds: retention,
			RetentionSourceDefault:          !hasRetention,
			RetentionConfigValid:            retentionConfigValid,
			RetentionVerified:               verified,
		},
		ProxyURL: readStringField(raw, "proxy-url", "proxyUrl", "proxy_url"),
	}, nil
}

func SetUsageStatisticsEnabled(ctx context.Context, baseURL string, key string, enabled bool) error {
	payload := map[string]bool{"value": enabled}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPut,
		NormalizeBaseURL(baseURL)+"/v0/management/usage-statistics-enabled",
		strings.NewReader(string(data)),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	client := managementHTTPClient(ctx, 30*time.Second, false)
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxResponseBytes))
		_ = res.Body.Close()
	}()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	return errors.New("enable CPA usage statistics failed: " + res.Status)
}

func ValidateCollectorConfig(ctx context.Context, baseURL string, key string, pollIntervalMS int) error {
	usageCfg, err := FetchUsageConfig(ctx, baseURL, key)
	if err != nil {
		return err
	}
	if usageCfg.RedisUsageQueueRetentionSeconds <= 0 {
		return errors.New("CPA redis-usage-queue-retention-seconds must be greater than 0")
	}
	retentionMS := math.MaxInt
	if usageCfg.RedisUsageQueueRetentionSeconds <= math.MaxInt/1000 {
		retentionMS = usageCfg.RedisUsageQueueRetentionSeconds * 1000
	}
	if pollIntervalMS > retentionMS {
		return fmt.Errorf(
			"pollIntervalMs must be less than or equal to CPA redis-usage-queue-retention-seconds (%d seconds)",
			usageCfg.RedisUsageQueueRetentionSeconds,
		)
	}
	return nil
}

func NormalizeBaseURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "http://"):
		value = "http://" + value[len("http://"):]
	case strings.HasPrefix(lower, "https://"):
		value = "https://" + value[len("https://"):]
	case !strings.Contains(value, "://"):
		value = "http://" + value
	}
	value = strings.TrimRight(value, "/")
	value = strings.TrimSuffix(value, "/v0/management")
	value = strings.TrimSuffix(value, "/v0")
	return value
}

func decodeJSONObject(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("expected JSON object")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected trailing JSON data")
	}
	return raw, nil
}

func inspectBoolCandidates(maps []map[string]any, keys ...string) (bool, bool, error) {
	seen := false
	seenVal := false
	for _, m := range maps {
		if m == nil {
			continue
		}
		for _, key := range keys {
			value, ok := m[key]
			if !ok {
				continue
			}
			parsed, valid := parseStrictBool(value)
			if !valid {
				return false, true, fmt.Errorf("invalid boolean field %q", key)
			}
			if seen && parsed != seenVal {
				return false, true, fmt.Errorf("conflicting boolean field %q", key)
			}
			seen = true
			seenVal = parsed
		}
	}
	return seenVal, seen, nil
}

func inspectIntCandidates(maps []map[string]any, keys ...string) (int, bool, error) {
	seen := false
	seenVal := 0
	for _, m := range maps {
		if m == nil {
			continue
		}
		for _, key := range keys {
			value, ok := m[key]
			if !ok {
				continue
			}
			parsed, valid := parseStrictInt(value)
			if !valid {
				return 0, true, fmt.Errorf("invalid integer field %q", key)
			}
			if seen && parsed != seenVal {
				return 0, true, fmt.Errorf("conflicting integer field %q", key)
			}
			seen = true
			seenVal = parsed
		}
	}
	return seenVal, seen, nil
}

func parseStrictBool(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true, true
		case "0", "false", "no", "off":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func parseStrictInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		if typed < math.MinInt || typed > math.MaxInt {
			return 0, false
		}
		return int(typed), true
	case json.Number:
		return parseStrictIntString(typed.String(), true)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed {
			return 0, false
		}
		return parseStrictIntString(strconv.FormatFloat(typed, 'f', 0, 64), false)
	case string:
		return parseStrictIntString(typed, false)
	default:
		return 0, false
	}
}

func parseStrictIntString(raw string, allowRat bool) (int, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		if n < math.MinInt || n > math.MaxInt {
			return 0, false
		}
		return int(n), true
	}
	if !allowRat || len(trimmed) > 32 || strings.Contains(trimmed, "/") {
		return 0, false
	}
	rat, ok := new(big.Rat).SetString(trimmed)
	if !ok || !rat.IsInt() || !rat.Num().IsInt64() {
		return 0, false
	}
	n := rat.Num().Int64()
	if n < math.MinInt || n > math.MaxInt {
		return 0, false
	}
	return int(n), true
}

func readStringField(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok || value == nil {
			continue
		}
		return strings.TrimSpace(fmt.Sprint(value))
	}
	return ""
}
