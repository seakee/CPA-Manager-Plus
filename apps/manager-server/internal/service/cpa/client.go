package cpa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type UsageConfig struct {
	UsageStatisticsEnabled          bool `json:"usageStatisticsEnabled"`
	RedisUsageQueueRetentionSeconds int  `json:"redisUsageQueueRetentionSeconds"`
	RetentionSourceDefault          bool `json:"retentionSourceDefault"`
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
	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, NormalizeBaseURL(baseURL)+"/v0/management/config", nil)
	if err != nil {
		return ManagementConfig{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return ManagementConfig{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ManagementConfig{}, errors.New("management API config request failed: " + res.Status)
	}

	var raw map[string]any
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return ManagementConfig{}, err
	}
	usageEnabled := readBoolField(raw, "usage-statistics-enabled", "usageStatisticsEnabled")
	retention, hasRetention := readIntField(raw, "redis-usage-queue-retention-seconds", "redisUsageQueueRetentionSeconds")
	if !hasRetention {
		retention = 60
	}
	return ManagementConfig{
		UsageConfig: UsageConfig{
			UsageStatisticsEnabled:          usageEnabled,
			RedisUsageQueueRetentionSeconds: retention,
			RetentionSourceDefault:          !hasRetention,
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
	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
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
	retentionMS := usageCfg.RedisUsageQueueRetentionSeconds * 1000
	if retentionMS <= 0 {
		return errors.New("CPA redis-usage-queue-retention-seconds must be greater than 0")
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
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	value = strings.TrimRight(value, "/")
	value = strings.TrimSuffix(value, "/v0/management")
	value = strings.TrimSuffix(value, "/v0")
	return value
}

func readBoolField(raw map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed
		case string:
			normalized := strings.ToLower(strings.TrimSpace(typed))
			return normalized == "1" || normalized == "true" || normalized == "yes" || normalized == "on"
		}
	}
	return false
}

func readIntField(raw map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case float64:
			return int(typed), true
		case int:
			return typed, true
		case json.Number:
			parsed, err := strconv.Atoi(typed.String())
			return parsed, err == nil
		case string:
			parsed, err := strconv.Atoi(strings.TrimSpace(typed))
			return parsed, err == nil
		}
	}
	return 0, false
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
