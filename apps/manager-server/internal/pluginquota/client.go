// Package pluginquota talks to the generic plugin quota endpoints that CPA
// exposes over its management API. A CPA plugin may publish a quota provider
// for the credentials it owns; the manager server neither knows nor needs to
// know which plugin that is, because the provider catalogue, the credential
// binding and the labelled readings are all reported by CPA itself.
//
// The package is deliberately transport-only: it normalizes the provider
// catalogue and the item summary payload into plain values, and performs no
// persistence or provider-specific interpretation.
package pluginquota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

const (
	providersPath = "/v0/management/quota/providers"
	fetchPath     = "/v0/management/quota/fetch"

	// DefaultTimeout bounds one plugin quota management call. Plugins may
	// proxy an upstream provider request, so the timeout is more generous than
	// a plain configuration read.
	DefaultTimeout = 30 * time.Second

	maxResponseBytes = 4 * 1024 * 1024
	maxErrBodyBytes  = 4096
)

var (
	ErrProviderRequired  = errors.New("plugin quota provider is required")
	ErrAuthIndexRequired = errors.New("plugin quota auth index is required")
	ErrResponseTooLarge  = errors.New("plugin quota response too large")
)

// Provider is one plugin quota provider entry. SupportedProviders lists the CPA
// credential providers whose credentials this quota provider serves, which is
// what binds a panel credential to a quota provider without a hardcoded map.
//
// Provider is the normalized provider name CPAMP stores and compares, while
// PluginID is CPA's own plugin identifier and is used verbatim in management
// URLs. They only differ when CPA reports a mixed-case plugin id.
type Provider struct {
	PluginID           string   `json:"plugin_id"`
	Provider           string   `json:"provider"`
	DisplayName        string   `json:"display_name"`
	SupportedProviders []string `json:"supported_providers"`
	SupportsReset      bool     `json:"supports_reset"`
}

// PluginLocator returns the plugin identifier to use in management URLs.
func (p Provider) PluginLocator() string {
	if trimmed := strings.TrimSpace(p.PluginID); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(p.Provider)
}

// Reading is one plugin-reported value. Plugins report numbers for balances
// and check-in counts, but the field is not constrained: a boolean or string is
// preserved as text instead of being coerced into fake numeric evidence.
type Reading struct {
	Number *float64
	Text   string
}

func (r *Reading) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if trimmed == "true" || trimmed == "false" {
		r.Text = trimmed
		return nil
	}
	if trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		r.Text = value
		return nil
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return err
	}
	if !math.IsNaN(value) && !math.IsInf(value, 0) {
		r.Number = &value
	}
	return nil
}

func (r Reading) MarshalJSON() ([]byte, error) {
	if r.Number != nil {
		return json.Marshal(*r.Number)
	}
	if r.Text != "" {
		return json.Marshal(r.Text)
	}
	return []byte("null"), nil
}

// Item is one labelled quota reading from a plugin summary. Format and Currency
// are presentation hints reported by the plugin; the panel owns rendering.
type Item struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Value    Reading `json:"value"`
	Unit     string  `json:"unit"`
	Format   string  `json:"format"`
	Currency string  `json:"currency"`
}

type summaryPayload struct {
	Summary []Item `json:"summary"`
}

// Client performs plugin quota management calls against CPA.
type Client struct {
	httpClient *http.Client
	timeout    time.Duration
}

func New(httpClient *http.Client, timeouts ...time.Duration) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	timeout := DefaultTimeout
	if len(timeouts) > 0 && timeouts[0] > 0 {
		timeout = timeouts[0]
	}
	return &Client{httpClient: httpClient, timeout: timeout}
}

// Providers lists the plugin quota providers CPA currently serves. An empty
// list is a valid answer: it means no installed plugin publishes quota.
func (c *Client) Providers(ctx context.Context, baseURL, managementKey string) ([]Provider, error) {
	var payload struct {
		Providers []Provider `json:"providers"`
	}
	if err := c.get(ctx, baseURL, managementKey, providersPath, nil, &payload); err != nil {
		return nil, err
	}
	providers := make([]Provider, 0, len(payload.Providers))
	for _, provider := range payload.Providers {
		normalized := normalizeProvider(provider)
		if normalized.Provider == "" {
			continue
		}
		providers = append(providers, normalized)
	}
	return providers, nil
}

// CredentialQuota fetches the labelled quota summary one plugin reports for one
// credential. authIndex is CPA's credential locator, which is exactly the
// identity the panel already shows for an auth file.
func (c *Client) CredentialQuota(ctx context.Context, baseURL, managementKey, pluginID, authIndex string) ([]Item, error) {
	provider := strings.TrimSpace(pluginID)
	if provider == "" {
		return nil, ErrProviderRequired
	}
	index := strings.TrimSpace(authIndex)
	if index == "" {
		return nil, ErrAuthIndexRequired
	}
	query := url.Values{}
	query.Set("auth_index", index)
	path := "/v0/management/plugins/" + url.PathEscape(pluginLocator(provider)) + "/quota?" + query.Encode()
	var payload summaryPayload
	if err := c.get(ctx, baseURL, managementKey, path, nil, &payload); err != nil {
		return nil, err
	}
	return NormalizeItems(payload.Summary), nil
}

// FetchAll asks CPA to refresh the quota cached by every plugin quota provider.
// It is the explicit refresh action: a plain CredentialQuota read returns the
// plugin's current view without forcing an upstream round trip.
func (c *Client) FetchAll(ctx context.Context, baseURL, managementKey, authIndex string) error {
	body := map[string]string{}
	if index := strings.TrimSpace(authIndex); index != "" {
		body["auth_index"] = index
	}
	return c.post(ctx, baseURL, managementKey, fetchPath, body)
}

// ResetCredentialQuota asks a plugin to reset the quota it reports for one
// credential. It is only valid for a provider that advertises supports_reset.
func (c *Client) ResetCredentialQuota(ctx context.Context, baseURL, managementKey, pluginID, authIndex string) error {
	provider := strings.TrimSpace(pluginID)
	if provider == "" {
		return ErrProviderRequired
	}
	index := strings.TrimSpace(authIndex)
	if index == "" {
		return ErrAuthIndexRequired
	}
	query := url.Values{}
	query.Set("auth_index", index)
	path := "/v0/management/plugins/" + url.PathEscape(pluginLocator(provider)) + "/quota/reset?" + query.Encode()
	return c.post(ctx, baseURL, managementKey, path, map[string]string{"auth_index": index})
}

// NormalizeItems drops unusable entries and trims the plugin's own identifiers.
// The plugin's item keys are the stable window identities CPAMP stores, so an
// item without a key cannot be observed.
func NormalizeItems(items []Item) []Item {
	result := make([]Item, 0, len(items))
	for _, item := range items {
		normalized := Item{
			Key:      strings.TrimSpace(item.Key),
			Label:    strings.TrimSpace(item.Label),
			Value:    item.Value,
			Unit:     strings.TrimSpace(item.Unit),
			Format:   strings.ToLower(strings.TrimSpace(item.Format)),
			Currency: strings.ToUpper(strings.TrimSpace(item.Currency)),
		}
		if normalized.Key == "" {
			continue
		}
		result = append(result, normalized)
	}
	return result
}

// SupportedCredentialProviders maps the catalogue onto the CPA credential
// providers it serves.
func SupportedCredentialProviders(providers []Provider) map[string]string {
	result := make(map[string]string, len(providers))
	for _, provider := range providers {
		for _, supported := range provider.SupportedProviders {
			name := normalizeCredentialProvider(supported)
			if name == "" {
				continue
			}
			if _, exists := result[name]; !exists {
				result[name] = provider.Provider
			}
		}
	}
	return result
}

// ProviderNames returns the catalogue's provider identifiers in payload order.
func ProviderNames(providers []Provider) []string {
	result := make([]string, 0, len(providers))
	for _, provider := range providers {
		if provider.Provider != "" {
			result = append(result, provider.Provider)
		}
	}
	return result
}

// pluginLocator keeps CPA's plugin identifier verbatim; only the provider name
// CPAMP stores is normalized.
func pluginLocator(pluginID string) string {
	return strings.TrimSpace(pluginID)
}

func normalizeProvider(provider Provider) Provider {
	rawPluginID := strings.TrimSpace(provider.PluginID)
	rawProvider := strings.TrimSpace(provider.Provider)
	normalized := Provider{
		PluginID:      rawPluginID,
		Provider:      normalizeCredentialProvider(rawProvider),
		DisplayName:   strings.TrimSpace(provider.DisplayName),
		SupportsReset: provider.SupportsReset,
	}
	if normalized.Provider == "" {
		normalized.Provider = normalizeCredentialProvider(rawPluginID)
	}
	if normalized.PluginID == "" {
		normalized.PluginID = rawProvider
	}
	if normalized.PluginID == "" {
		normalized.PluginID = normalized.Provider
	}
	for _, supported := range provider.SupportedProviders {
		name := normalizeCredentialProvider(supported)
		if name == "" {
			continue
		}
		normalized.SupportedProviders = append(normalized.SupportedProviders, name)
	}
	if len(normalized.SupportedProviders) == 0 && normalized.Provider != "" {
		normalized.SupportedProviders = []string{normalized.Provider}
	}
	return normalized
}

// normalizeCredentialProvider mirrors the credential provider normalization the
// quota snapshot store applies, so a catalogue entry and an auth file always
// compare equal.
func normalizeCredentialProvider(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
}

func (c *Client) get(ctx context.Context, baseURL, managementKey, path string, body io.Reader, target any) error {
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, cpa.NormalizeBaseURL(baseURL)+path, body)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+managementKey)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, maxErrBodyBytes))
		return fmt.Errorf("GET %s: HTTP %d %s", path, res.StatusCode, strings.TrimSpace(string(detail)))
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, maxResponseBytes))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	return nil
}

func (c *Client) post(ctx context.Context, baseURL, managementKey, path string, payload map[string]string) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, cpa.NormalizeBaseURL(baseURL)+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+managementKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, maxErrBodyBytes))
		return fmt.Errorf("POST %s: HTTP %d %s", path, res.StatusCode, strings.TrimSpace(string(detail)))
	}
	return nil
}
