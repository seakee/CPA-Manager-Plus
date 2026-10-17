package cpa

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const safeLegacyConfig = `{` +
	`"tls":{"enable":false,"cert":"","key":""},` +
	`"debug":false,` +
	`"pprof":{"enable":false,"addr":"127.0.0.1:8316"},` +
	`"commercial-mode":false,` +
	`"logging-to-file":false,` +
	`"logs-max-total-size-mb":0,` +
	`"error-logs-max-files":10,` +
	`"usage-statistics-enabled":false,` +
	`"redis-usage-queue-retention-seconds":60,` +
	`"request-retry":3,` +
	`"routing":{"strategy":"round-robin"},` +
	`"quota-exceeded":{"switch-project":true},` +
	`"payload":{},"proxy-url":"","request-log":false,` +
	`"gemini-api-key":[{"api-key":"private-provider-secret"}],` +
	`"claude-api-key":[{"api-key":"private-claude-secret"}],` +
	`"api-keys":["private-client-key"],` +
	`"passthrough-headers":false` +
	`}`

func TestLegacyManagedSafeReadHasOneBoundedReadOnlyRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/v0/management/config" || r.Header.Get("Authorization") != "Bearer private-management-key" {
			t.Errorf("unexpected legacy safe-read request: %s %s", r.Method, r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-CPA-VERSION", "7.0.12")
		_, _ = w.Write([]byte(safeLegacyConfig))
	}))
	defer server.Close()
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	snapshot, err := ReadLegacyManagedAdoptionSnapshot(context.Background(), server.URL, "private-management-key")
	if err != nil || calls.Load() != 1 || proxyCalls.Load() != 0 {
		t.Fatalf("read: %+v %v calls=%d proxy=%d", snapshot, err, calls.Load(), proxyCalls.Load())
	}
	if len(snapshot.APIKeys) != 1 || snapshot.APIKeys[0] != "private-client-key" {
		t.Fatalf("client keys not projected: %+v", snapshot.APIKeys)
	}
	want := []string{"access", "oauth", "server", "routing", "usage", "providers"}
	if strings.Join(snapshot.ResourceSections, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected bounded inventory: %v", snapshot.ResourceSections)
	}
	encoded, _ := json.Marshal(snapshot)
	for _, secret := range []string{"private-provider-secret", "private-claude-secret", "127.0.0.1:8316", "round-robin", "private-management-key"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("legacy config value escaped projection: %s", secret)
		}
	}
}

func TestLegacyManagedNeverFollowsRedirectRetriesOrFallsBackToV8(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	for _, status := range []int{301, 302, 307, 308, 404, 405, 410, 401, 403, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", destination.URL+"/leak")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("private-legacy-body"))
			}))
			defer server.Close()
			_, err := ReadLegacyManagedAdoptionSnapshot(context.Background(), server.URL, "private-management-key")
			if err == nil || calls.Load() != 1 || destinationCalls.Load() != 0 || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe legacy read: %v calls=%d", err, calls.Load())
			}
			switch status {
			case 404, 405, 410:
				if !errors.Is(err, LegacyOwnerNotLegacy) {
					t.Fatalf("missing typed non-legacy blocker: %v", err)
				}
			case 401, 403:
				if !errors.Is(err, LegacyAuthenticationRejected) {
					t.Fatalf("missing typed auth blocker: %v", err)
				}
			}
		})
	}
}

func TestLegacyManagedRejectsV8ConfigAndInvalidBodies(t *testing.T) {
	for name, item := range map[string]struct {
		contentType string
		body        []byte
		want        error
	}{
		"v8 config":            {"application/json", []byte(safeV8Config), LegacyOwnerNotLegacy},
		"non-json":             {"text/plain", []byte(safeLegacyConfig), LegacyInvalidConfig},
		"malformed":            {"application/json", []byte(`{"api-keys":`), LegacyInvalidConfig},
		"missing api keys":     {"application/json", []byte(`{"debug":false}`), LegacyInvalidConfig},
		"null api keys":        {"application/json", []byte(`{"api-keys":null}`), LegacyInvalidConfig},
		"empty api keys":       {"application/json", []byte(`{"api-keys":[]}`), LegacyInvalidConfig},
		"null entry":           {"application/json", []byte(`{"api-keys":[null]}`), LegacyInvalidConfig},
		"non-string entry":     {"application/json", []byte(`{"api-keys":[7]}`), LegacyInvalidConfig},
		"whitespace key":       {"application/json", []byte(`{"api-keys":[" private"]}`), LegacyInvalidConfig},
		"duplicate key":        {"application/json", []byte(`{"api-keys":["private","private"]}`), LegacyInvalidConfig},
		"control key":          {"application/json", []byte(`{"api-keys":["private\u0000key"]}`), LegacyInvalidConfig},
		"oversized":            {"application/json", bytes.Repeat([]byte("x"), (1<<20)+1), LegacyInvalidConfig},
		"invalid utf8":         {"application/json", append(append([]byte(`{"api-keys":["private"]}`), 0xff), '}'), LegacyInvalidConfig},
		"trailing json object": {"application/json", []byte(`{"api-keys":["private"]}{}`), LegacyInvalidConfig},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", item.contentType)
				w.Header().Set("X-CPA-VERSION", "7.0.12")
				_, _ = w.Write(item.body)
			}))
			defer server.Close()
			if _, err := ReadLegacyManagedAdoptionSnapshot(context.Background(), server.URL, "private"); !errors.Is(err, item.want) {
				t.Fatalf("accepted invalid legacy body: %v", err)
			}
		})
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write(bytes.Repeat([]byte("x"), (1<<20)+1))
	_ = writer.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("X-CPA-VERSION", "7.0.12")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()
	if _, err := ReadLegacyManagedAdoptionSnapshot(context.Background(), server.URL, "private"); !errors.Is(err, LegacyInvalidConfig) {
		t.Fatalf("accepted decompression bomb: %v", err)
	}
}

func TestLegacyManagedUnknownSectionsUseOnlyTheFixedSentinel(t *testing.T) {
	body := `{"api-keys":["private-client"],"attacker-controlled-section":{"secret":"do-not-store"},"tls":{"enable":false}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-CPA-VERSION", "7.0.12")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	snapshot, err := ReadLegacyManagedAdoptionSnapshot(context.Background(), server.URL, "private-key")
	if err != nil {
		t.Fatal(err)
	}
	want := "access,oauth,server,unknown_cpa_native_resource"
	if strings.Join(snapshot.ResourceSections, ",") != want {
		t.Fatalf("unknown upstream key escaped as identity: %v", snapshot.ResourceSections)
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "attacker-controlled-section") || strings.Contains(string(encoded), "do-not-store") {
		t.Fatal("unknown upstream key or value escaped the bounded projection")
	}
}

func TestLegacyManagedRequiresExactAcceptedSourceProfile(t *testing.T) {
	for _, version := range []string{"", "7.0.11", "7.0.13", "8.0.13", "v7.0.12", "7.0.12-rc.1", "dev"} {
		t.Run("header="+version, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if version != "" {
					w.Header().Set("X-CPA-VERSION", version)
				}
				_, _ = w.Write([]byte(safeLegacyConfig))
			}))
			defer server.Close()
			if _, err := ReadLegacyManagedAdoptionSnapshot(context.Background(), server.URL, "private"); !errors.Is(err, LegacySourceProfileUnsupported) || calls.Load() != 1 {
				t.Fatalf("accepted unaccepted source profile %q: %v calls=%d", version, err, calls.Load())
			}
		})
	}
	// A real v8.0.13 v0 response is legacy-shaped: it has client API keys and no
	// v8 nested sections, so only the version metadata rejects it.
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-CPA-VERSION", "8.0.13")
		_, _ = w.Write([]byte(`{"client":{"codex":{}},"api-keys":["private-v8-key"],"tls":{"enable":false}}`))
	}))
	defer server.Close()
	if _, err := ReadLegacyManagedAdoptionSnapshot(context.Background(), server.URL, "private"); !errors.Is(err, LegacySourceProfileUnsupported) || calls.Load() != 1 || strings.Contains(err.Error(), "8.0.13") {
		t.Fatalf("v8 v0 compatibility endpoint accepted as legacy: %v", err)
	}
}
