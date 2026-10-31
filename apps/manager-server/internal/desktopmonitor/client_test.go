package desktopmonitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	_ "time/tzdata"
)

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c, err := NewClient(s.URL+"/proxy/manager/", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	return c, s
}

func TestFetchRealContractsAndWhitelist(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 11, 1, 23, 30, 0, 0, loc) // 25-hour DST day
	calls := map[string]int{}
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("missing auth")
		}
		switch r.URL.Path {
		case "/proxy/manager/v0/management/dashboard/summary":
			if r.Method != "GET" || r.URL.Query().Get("today_start_ms") != "1793505600000" || r.URL.Query().Get("now_ms") != fmt.Sprint(now.UnixMilli()) {
				t.Errorf("summary contract: %s %s", r.Method, r.URL)
			}
			fmt.Fprint(w, `{"today":{"total_calls":0,"total_tokens":0,"total_cost":0},"secret_metadata":"do-not-retain"}`)
		case "/proxy/manager/v0/management/monitoring/analytics":
			var body struct {
				From    int64                      `json:"from_ms"`
				To      int64                      `json:"to_ms"`
				Now     int64                      `json:"now_ms"`
				Zone    string                     `json:"time_zone"`
				Include map[string]json.RawMessage `json:"include"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if r.Method != "POST" || body.From != now.AddDate(0, 0, -30).UnixMilli() || body.To != now.UnixMilli() || body.Now != now.UnixMilli() || body.Zone != "America/New_York" {
				t.Errorf("analytics contract: %+v", body)
			}
			if string(body.Include["events_page"]) != `{"limit":3}` {
				t.Error("not three events")
			}
			for _, flag := range []string{"summary", "summary_percentiles", "summary_comparison", "timeline", "hourly_distribution", "model_share", "channel_share", "model_stats", "failure_sources", "account_stats", "credential_stats", "credential_timeline", "api_key_timeline", "api_key_stats", "filter_options", "filter_selectors", "heatmap", "anomaly_points", "task_buckets"} {
				if string(body.Include[flag]) != "false" {
					t.Errorf("include %s not false", flag)
				}
			}
			fmt.Fprint(w, `{"coverage":{"raw_complete":false},"events":{"items":[{"timestamp_ms":1793500000000,"model":"alias","resolved_model":"priced","requested_model":"requested","response_model":"response","input_tokens":1000,"output_tokens":200,"cached_tokens":300,"cache_read_tokens":100,"cache_creation_tokens":50,"total_tokens":1200,"latency_ms":2000,"ttft_ms":500,"stream":true,"account_snapshot":"SECRET","raw_json":{"api_key":"SECRET"}},{"timestamp_ms":1793490000000,"model":"free","input_tokens":0,"output_tokens":0,"cached_tokens":0,"total_tokens":0,"latency_ms":100,"ttft_ms":100,"stream":false,"failed":true},{"timestamp_ms":1793480000000,"requested_model":"unknown","latency_ms":-5,"ttft_ms":0}]}}`)
		case "/proxy/manager/v0/management/model-prices":
			// cached_tokens in event rows is the compatible bucket, not the raw mirror.
			fmt.Fprint(w, `{"prices":{"priced":{"prompt":2,"completion":4,"cache":0.5,"cacheRead":0.25,"cacheCreation":3},"free":{"prompt":0,"completion":0,"cache":0},"other":{"rawJson":"SECRET"}}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	snap, err := c.Fetch(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || len(snap.Requests) != 3 || !snap.Partial || snap.Today.Cost == nil || *snap.Today.Cost != 0 {
		t.Fatalf("snapshot: %+v", snap)
	}
	a := snap.Requests[0]
	if a.Model != "priced" || a.TotalTokens != 1200 || a.CachedTokens != 400 || a.TokensPerSecond == nil || *a.TokensPerSecond != 100 || a.TTFTMS == nil || *a.TTFTMS != 500 {
		t.Fatalf("request %+v", a)
	}
	// (1000-300-100-50)*2 + 300*.5 + 100*.25 + 50*3 + 200*4 = 2225 / 1M.
	if a.Cost == nil || *a.Cost < 0.00222499 || *a.Cost > 0.00222501 {
		t.Fatalf("cost %+v", a.Cost)
	}
	if snap.Requests[1].Cost == nil || *snap.Requests[1].Cost != 0 || snap.Requests[1].TTFTMS != nil || !snap.Requests[1].Failed {
		t.Error("free/failed/nonstream")
	}
	if snap.Requests[2].Cost != nil || snap.Requests[2].LatencyMS != nil || snap.Requests[2].TTFTMS != nil || snap.Requests[2].InputTokens != -1 {
		t.Error("unknown measurements fabricated")
	}
	b, _ := json.Marshal(snap)
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "raw_json") {
		t.Fatal("sensitive metadata retained")
	}
}

func TestContextTierAndLegacyLongContextPricing(t *testing.T) {
	for _, tc := range []struct {
		name, price string
		want        float64
	}{
		{"custom", `{"prompt":2,"completion":4,"cache":0.5,"contextTiers":[{"thresholdTokens":1000,"prompt":10,"completion":20,"promptConfigured":true,"completionConfigured":true}]}`, 0.0125},
		{"gpt-5.5", `{"prompt":5,"completion":30,"cache":0.5}`, 2.9955},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := int64(2000)
			if tc.name == "gpt-5.5" {
				input = 300000
			}
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "summary"):
					fmt.Fprint(w, `{"today":{}}`)
				case strings.HasSuffix(r.URL.Path, "analytics"):
					fmt.Fprintf(w, `{"events":{"items":[{"timestamp_ms":1700000000000,"model":%q,"input_tokens":%d,"output_tokens":100,"cached_tokens":1000,"total_tokens":%d}]}}`, tc.name, input, input+100)
				default:
					fmt.Fprintf(w, `{"prices":{%q:%s}}`, tc.name, tc.price)
				}
			})
			snap, err := c.Fetch(context.Background(), time.UnixMilli(1700000001000))
			if err != nil {
				t.Fatal(err)
			}
			cost := snap.Requests[0].Cost
			if cost == nil || math.Abs(*cost-tc.want) > 1e-9 {
				t.Fatalf("cost %v want %v", cost, tc.want)
			}
		})
	}
}

// A model priced only by the Manager's built-in official fallback must match
// the dashboard rather than showing an unknown request cost.
func TestOfficialFallbackPriceIsUsedAndEmptyKeyRejected(t *testing.T) {
	if _, err := NewClient("http://127.0.0.1:18317", "   "); err == nil {
		t.Fatal("accepted an empty administrator key")
	}
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "summary"):
			fmt.Fprint(w, `{"today":{}}`)
		case strings.HasSuffix(r.URL.Path, "analytics"):
			fmt.Fprint(w, `{"events":{"items":[{"timestamp_ms":1700000000000,"model":"gpt-5.6-luna","input_tokens":1000000,"output_tokens":1000000,"cached_tokens":0,"total_tokens":2000000}]}}`)
		default:
			fmt.Fprint(w, `{"prices":{}}`)
		}
	})
	snap, err := c.Fetch(context.Background(), time.UnixMilli(1700000001000))
	if err != nil {
		t.Fatal(err)
	}
	cost := snap.Requests[0].Cost
	// The Manager applies its long-context multiplier to a 1M-token input for
	// this model family, so assert a real positive cost is resolved rather than
	// the unknown em dash that a price-book-only check produced.
	if cost == nil || *cost <= 0 || math.IsInf(*cost, 0) {
		t.Fatalf("official fallback cost %v want positive", cost)
	}
}

func TestPanelURLPreservesPrefixWithoutSecrets(t *testing.T) {
	got, err := panelURL("https://manager.example/reverse/prefix/")
	if err != nil || got != "https://manager.example/reverse/prefix/management.html" {
		t.Fatalf("panel URL %s %v", got, err)
	}
	for _, base := range []string{"https://user:SECRET@manager.example", "https://manager.example/?key=SECRET", "http://remote.example"} {
		if _, err := panelURL(base); err == nil {
			t.Fatal("unsafe panel URL")
		}
	}
}

func TestURLPolicy(t *testing.T) {
	for _, base := range []string{"http://example.com", "https://user:key@example.com", "https://example.com/?key=x", "https://example.com/#key", "file:///C:/secret", "http://127.0.0.1.evil.test", "https://example.com/a/../b", "https://example.com/%2f..", ""} {
		if _, err := NewClient(base, "key"); err == nil {
			t.Errorf("accepted %s", base)
		}
	}
	for _, base := range []string{"http://127.0.0.1:8080/prefix", "http://[::1]:8080", "http://localhost:8080", "https://remote.example/prefix/"} {
		if _, err := NewClient(base, "key"); err != nil {
			t.Errorf("rejected %s: %v", base, err)
		}
	}
	if _, err := NewClient("https://example.com", "a\nb"); err == nil {
		t.Error("accepted invalid header")
	}
}

func TestFetchErrorsAreSanitizedAndRedirectNeverForwardsKey(t *testing.T) {
	var leaked atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer sink.Close()
	for _, status := range []int{401, 403, 500, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", sink.URL)
				w.WriteHeader(status)
				fmt.Fprint(w, "SECRET")
			})
			_, err := c.Fetch(context.Background(), time.Now())
			if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "test-secret") {
				t.Fatalf("unsafe error %v", err)
			}
			if (status == 401 || status == 403) && !errors.Is(err, ErrUnauthorized) {
				t.Errorf("auth %v", err)
			}
		})
	}
	if leaked.Load() != 0 {
		t.Fatal("followed redirect")
	}
}

func TestFetchCancellationMalformedAndBoundedBodies(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		started := make(chan struct{})
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, err := c.Fetch(ctx, time.Now()); done <- err }()
		<-started
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("not canceled")
		}
	})
	for _, body := range []string{`{`, `{"today":{}} garbage`, strings.Repeat(" ", 2*1024*1024+1)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if _, err := c.Fetch(context.Background(), time.Now()); err == nil {
				t.Fatal("accepted invalid or unbounded body")
			}
		})
	}
}

func TestPriceFailureIsUnknownButAuthFailureClears(t *testing.T) {
	for _, status := range []int{500, 401} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "summary"):
					fmt.Fprint(w, `{"today":{"total_calls":1,"total_tokens":1}}`)
				case strings.HasSuffix(r.URL.Path, "analytics"):
					fmt.Fprint(w, `{"events":{"items":[{"timestamp_ms":1700000000000,"model":"x","input_tokens":1,"output_tokens":1,"cached_tokens":0,"total_tokens":2}]}}`)
				default:
					w.WriteHeader(status)
				}
			})
			snap, err := c.Fetch(context.Background(), time.UnixMilli(1700000001000))
			if status == 401 {
				if !errors.Is(err, ErrUnauthorized) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || len(snap.Requests) != 1 || snap.Requests[0].Cost != nil || snap.Today.Cost != nil {
				t.Fatalf("unknown cost: %+v %v", snap, err)
			}
		})
	}
}
