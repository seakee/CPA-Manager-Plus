package desktopmonitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // IANA names remain available on Windows without zoneinfo files.

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/pricing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const maxResponseBytes = 2 * 1024 * 1024

// Client never follows redirects (including same-origin redirects). It only
// sends the key as an Authorization header to the validated base origin.
type Client struct {
	base string
	key  string
	http *http.Client
}

func NewClient(baseURL, adminKey string) (*Client, error) {
	base, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(adminKey) == "" || strings.ContainsAny(adminKey, "\r\n\x00") || len(adminKey) > 8192 {
		return nil, errors.New("invalid administrator key")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 8 * time.Second
	transport.MaxResponseHeaderBytes = 64 * 1024
	transport.TLSHandshakeTimeout = 5 * time.Second
	return &Client{base: base, key: strings.TrimSpace(adminKey), http: &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}}, nil
}
func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	bad := errors.New("use loopback HTTP or HTTPS Manager URL without credentials, query or fragment")
	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "?#\x00\r\n") {
		return "", bad
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !(strings.EqualFold(u.Hostname(), "localhost") || ip != nil && ip.IsLoopback()) {
			return "", bad
		}
	}
	// Keep reverse-proxy prefixes, but reject ambiguous escaped paths/traversal.
	if u.RawPath != "" || strings.Contains(u.Path, "\\") {
		return "", bad
	}
	for _, p := range strings.Split(u.Path, "/") {
		if p == "." || p == ".." {
			return "", bad
		}
	}
	return strings.TrimRight(u.String(), "/"), nil
}
func panelURL(base string) (string, error) {
	normalized, err := normalizeBaseURL(base)
	if err != nil {
		return "", err
	}
	return normalized + "/management.html", nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) read(ctx context.Context, method, path string, body any, out any) error {
	var data io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return errUnavailable
		}
		data = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, data)
	if err != nil {
		return errUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("monitor HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errUnavailable
	}
	if len(b) > maxResponseBytes {
		return errors.New("monitor response too large")
	}
	// Decode only whitelisted structs; unknown account/body fields are skipped.
	if err = json.Unmarshal(b, out); err != nil {
		return errors.New("invalid monitor response")
	}
	return nil
}

type eventRow struct {
	Model          string   `json:"model"`
	RequestedModel string   `json:"requested_model"`
	ResolvedModel  string   `json:"resolved_model"`
	ResponseModel  string   `json:"response_model"`
	ServiceTier    string   `json:"service_tier"`
	TimestampMS    int64    `json:"timestamp_ms"`
	Input          *int64   `json:"input_tokens"`
	Output         *int64   `json:"output_tokens"`
	Cached         *int64   `json:"cached_tokens"`
	Read           int64    `json:"cache_read_tokens"`
	Creation       int64    `json:"cache_creation_tokens"`
	Total          *int64   `json:"total_tokens"`
	Latency        *float64 `json:"latency_ms"`
	TTFT           *float64 `json:"ttft_ms"`
	Stream         bool     `json:"stream"`
	Failed         bool     `json:"failed"`
}

// This projection deliberately excludes ModelPrice.RawJSON and source metadata.
type priceRow struct {
	Prompt                  *float64                      `json:"prompt"`
	Completion              *float64                      `json:"completion"`
	Cache                   float64                       `json:"cache"`
	CacheRead               float64                       `json:"cacheRead"`
	CacheCreation           float64                       `json:"cacheCreation"`
	PromptConfigured        bool                          `json:"promptConfigured"`
	CompletionConfigured    bool                          `json:"completionConfigured"`
	CacheReadConfigured     bool                          `json:"cacheReadConfigured"`
	CacheCreationConfigured bool                          `json:"cacheCreationConfigured"`
	ContextTiers            []model.ModelPriceContextTier `json:"contextTiers"`
	ServiceTiers            []model.ModelPriceServiceTier `json:"serviceTiers"`
}

func (p priceRow) price() (model.ModelPrice, bool) {
	if !validNumber(p.Prompt) || !validNumber(p.Completion) || p.Cache < 0 || p.CacheRead < 0 || p.CacheCreation < 0 {
		return model.ModelPrice{}, false
	}
	contexts, err := model.NormalizeModelPriceContextTiers(p.ContextTiers)
	if err != nil {
		return model.ModelPrice{}, false
	}
	tiers, err := model.NormalizeModelPriceServiceTiers(p.ServiceTiers)
	if err != nil {
		return model.ModelPrice{}, false
	}
	return model.ModelPrice{Prompt: *p.Prompt, Completion: *p.Completion, Cache: p.Cache, CacheRead: p.CacheRead, CacheCreation: p.CacheCreation, PromptConfigured: p.PromptConfigured, CompletionConfigured: p.CompletionConfigured, CacheReadConfigured: p.CacheReadConfigured, CacheCreationConfigured: p.CacheCreationConfigured, ContextTiers: contexts, ServiceTiers: tiers}, true
}
func intValue(v *int64) int64 {
	if v == nil || *v < 0 {
		return -1
	}
	return *v
}
func (c *Client) Fetch(ctx context.Context, now time.Time) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var daily struct {
		Today *struct {
			Calls  *int64   `json:"total_calls"`
			Tokens *int64   `json:"total_tokens"`
			Cost   *float64 `json:"total_cost"`
		} `json:"today"`
	}
	path := fmt.Sprintf("/v0/management/dashboard/summary?today_start_ms=%d&now_ms=%d", dayStart(now).UnixMilli(), now.UnixMilli())
	if err := c.read(ctx, "GET", path, nil, &daily); err != nil {
		return Snapshot{}, err
	}
	if daily.Today == nil {
		return Snapshot{}, errors.New("missing today summary")
	}
	var analytics struct {
		Events *struct {
			Items []eventRow `json:"items"`
		} `json:"events"`
		Coverage *struct {
			RawComplete bool `json:"raw_complete"`
		} `json:"coverage"`
	}
	include := map[string]any{"events_page": struct {
		Limit int `json:"limit"`
	}{3}, "recent_failures": 0}
	for _, flag := range []string{"summary", "summary_percentiles", "summary_comparison", "timeline", "hourly_distribution", "model_share", "channel_share", "model_stats", "failure_sources", "account_stats", "credential_stats", "credential_timeline", "api_key_timeline", "api_key_stats", "filter_options", "filter_selectors", "heatmap", "anomaly_points", "task_buckets"} {
		include[flag] = false
	}
	zone := now.Location().String()
	if zone == "Local" {
		zone = "UTC"
	} // Bounds are absolute; no calendar aggregation requested.
	body := struct {
		From    int64          `json:"from_ms"`
		To      int64          `json:"to_ms"`
		Now     int64          `json:"now_ms"`
		Zone    string         `json:"time_zone"`
		Include map[string]any `json:"include"`
	}{now.AddDate(0, 0, -30).UnixMilli(), now.UnixMilli(), now.UnixMilli(), zone, include}
	if err := c.read(ctx, "POST", "/v0/management/monitoring/analytics", body, &analytics); err != nil {
		return Snapshot{}, err
	}
	if analytics.Events == nil {
		return Snapshot{}, errors.New("missing recent events")
	}
	var book struct {
		Prices map[string]priceRow `json:"prices"`
	}
	err := c.read(ctx, "GET", "/v0/management/model-prices", nil, &book)
	if errors.Is(err, ErrUnauthorized) || ctx.Err() != nil {
		if ctx.Err() != nil {
			return Snapshot{}, ctx.Err()
		}
		return Snapshot{}, err
	}
	prices := map[string]model.ModelPrice{}
	if err == nil {
		for name, row := range book.Prices {
			if p, ok := row.price(); ok {
				prices[name] = p
			}
		}
	}
	snap := Snapshot{UpdatedAt: now, Today: DaySummary{Calls: intValue(daily.Today.Calls), Tokens: intValue(daily.Today.Tokens), Cost: daily.Today.Cost}, Partial: analytics.Coverage != nil && !analytics.Coverage.RawComplete}
	if !validNumber(snap.Today.Cost) {
		snap.Today.Cost = nil
	}
	sort.SliceStable(analytics.Events.Items, func(i, j int) bool {
		return analytics.Events.Items[i].TimestampMS > analytics.Events.Items[j].TimestampMS
	})
	for _, row := range analytics.Events.Items {
		// Small clock skew must not hide the genuinely latest request.
		if row.TimestampMS <= 0 || row.TimestampMS > now.Add(time.Minute).UnixMilli() || row.TimestampMS < body.From {
			continue
		}
		r := Request{Model: displayModel(row.ResolvedModel, row.ResponseModel, row.Model, row.RequestedModel), Timestamp: time.UnixMilli(row.TimestampMS), Failed: row.Failed, Stream: row.Stream, InputTokens: intValue(row.Input), OutputTokens: intValue(row.Output), CachedTokens: intValue(row.Cached), TotalTokens: intValue(row.Total)}
		if r.CachedTokens >= 0 {
			r.CachedTokens += max(row.Read, 0)
		}
		if validNumber(row.Latency) {
			r.LatencyMS = row.Latency
		}
		if row.Stream && validNumber(row.TTFT) && *row.TTFT > 0 && (r.LatencyMS == nil || *row.TTFT <= *r.LatencyMS) {
			r.TTFTMS = row.TTFT
		}
		if r.OutputTokens > 0 && r.LatencyMS != nil && *r.LatencyMS > 0 {
			v := float64(r.OutputTokens) * 1000 / *r.LatencyMS
			r.TokensPerSecond = &v
		}
		names := []string{row.ResolvedModel, usageidentity.AnalyticsModelForRequest(row.Model, row.RequestedModel), row.Model, row.RequestedModel, row.ResponseModel}
		for i := range names {
			names[i] = strings.TrimSpace(names[i])
		}
		priced := pricing.HasModelPrice(names, prices)
		if priced && r.InputTokens >= 0 && r.OutputTokens >= 0 && row.Cached != nil && *row.Cached >= 0 && row.Read >= 0 && row.Creation >= 0 {
			tokens := pricing.ModelTokens{InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CachedTokens: *row.Cached, CacheReadTokens: row.Read, CacheCreationTokens: row.Creation}
			// Classify the actual threshold band, not the input count (the pricing
			// service consumes a band identity). Reuse upstream long-context semantics.
			for _, name := range names {
				if price, ok := prices[name]; ok {
					tokens.PricingModel = name
					_, tokens.ContextThresholdTokens = model.ModelPriceForContext(price, r.InputTokens)
					break
				}
			}
			var long usage.LongContextTokens
			long.AddIfLongContext(tokens.InputTokens, tokens.OutputTokens, tokens.CachedTokens, tokens.CacheReadTokens, tokens.CacheCreationTokens)
			tokens.LongInputTokens = long.LongInputTokens
			tokens.LongOutputTokens = long.LongOutputTokens
			tokens.LongCachedTokens = long.LongCachedTokens
			tokens.LongCacheReadTokens = long.LongCacheReadTokens
			tokens.LongCacheCreationTokens = long.LongCacheCreationTokens
			cost := pricing.CostForModelCandidatesWithServiceTier(names, row.ServiceTier, tokens, prices)
			if validNumber(&cost) {
				r.Cost = &cost
			}
		}
		snap.Requests = append(snap.Requests, r)
		if len(snap.Requests) == 3 {
			break
		}
	}
	return snap, nil
}
