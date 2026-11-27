package cpa

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const safeV8Config = `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/private/auth"},"access":{"api-keys":["private-client-key"]}}`

func TestFreshV8SafeReadHasOneBoundedNonDestructiveRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.RequestURI() != "/v8/management/config" || r.Header.Get("Authorization") != "Bearer private-management-key" {
			t.Errorf("unexpected safe-read request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(safeV8Config))
	}))
	defer server.Close()
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	observation, err := ReadV8Config(context.Background(), server.URL, "private-management-key")
	if err != nil || calls.Load() != 1 || observation.ClientKeyCount != 1 {
		t.Fatalf("read: %+v %v calls=%d", observation, err, calls.Load())
	}
	encoded, _ := json.Marshal(observation)
	if strings.Contains(string(encoded), "private-") {
		t.Fatal("config values escaped projection")
	}
}

func TestFreshV8NeverFollowsRedirectOrFallsBackToV0(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	for _, status := range []int{301, 302, 307, 308, 404, 405, 401, 403, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", destination.URL+"/leak")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("private-management-key"))
			}))
			defer server.Close()
			_, err := ReadV8Config(context.Background(), server.URL, "private-management-key")
			if err == nil || calls.Load() != 1 || destinationCalls.Load() != 0 || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe read result: %v", err)
			}
			if (status == 404 || status == 405) && !errors.Is(err, V8OwnerUpgradeRequired) {
				t.Fatalf("v0-only missing typed blocker: %v", err)
			}
		})
	}
}

// CPA v8 disables client authentication when its Key list is empty, so a
// well-formed source with no Key is not an invalid body: it has nothing to
// adopt, and the browser needs that specific reason instead of an unreadable
// source. The refusal is typed, and the read that discovers it stays the single
// bounded GET the adoption already performs.
func TestFreshV8AdoptionRefusesAZeroKeySourceWithItsOwnReason(t *testing.T) {
	const zeroKeys = `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/private/auth"},"access":{"api-keys":[]}}`
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(zeroKeys))
	}))
	defer server.Close()
	snapshot, err := ReadV8AdoptionSnapshot(context.Background(), server.URL, "private-management-key")
	if !errors.Is(err, V8AdoptionSourceEmpty) || calls.Load() != 1 || snapshot.APIKeys != nil {
		t.Fatalf("zero-key source: %+v %v calls=%d", snapshot, err, calls.Load())
	}
	// The refusal must not be reachable as a generic read failure: the same body
	// is a valid observation for the read-only External surface, which never
	// adopts and therefore never needs Keys.
	if observation, err := ReadV8Config(context.Background(), server.URL, "private-management-key"); err != nil || observation.ClientKeyCount != 0 {
		t.Fatalf("read-only surface rejected a zero-key source: %+v %v", observation, err)
	}
	// A malformed or oversized Key list stays a config failure, not an empty
	// source: only a well-formed empty list is the adoption-specific reason.
	for name, body := range map[string]string{
		"null-list":    strings.Replace(zeroKeys, `"api-keys":[]`, `"api-keys":null`, 1),
		"null-element": strings.Replace(zeroKeys, `"api-keys":[]`, `"api-keys":[null]`, 1),
		"bad-element":  strings.Replace(zeroKeys, `"api-keys":[]`, `"api-keys":[7]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			invalid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer invalid.Close()
			if _, err := ReadV8AdoptionSnapshot(context.Background(), invalid.URL, "private-management-key"); !errors.Is(err, V8InvalidConfig) {
				t.Fatalf("accepted %s as an empty source: %v", name, err)
			}
		})
	}
}

func TestFreshV8RejectsInvalidAndOversizedBodies(t *testing.T) {
	for name, body := range map[string][]byte{
		"wrong-generation":     []byte(strings.Replace(safeV8Config, `"config-version":8`, `"config-version":7`, 1)),
		"missing-api-keys":     []byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{}}`),
		"wrong-port-type":      []byte(strings.Replace(safeV8Config, `"port":8317`, `"port":"8317"`, 1)),
		"null-client-key":      []byte(strings.Replace(safeV8Config, `["private-client-key"]`, `[null]`, 1)),
		"null-client-key-list": []byte(strings.Replace(safeV8Config, `["private-client-key"]`, `null`, 1)),
		"oversized":            bytes.Repeat([]byte("x"), (1<<20)+1),
		"invalid-utf8":         append([]byte(safeV8Config), 0xff),
		"extra-json":           []byte(safeV8Config + `{}`),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			defer server.Close()
			if _, err := ReadV8Config(context.Background(), server.URL, "private"); !errors.Is(err, V8InvalidConfig) {
				t.Fatalf("accepted invalid body: %v", err)
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
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()
	if _, err := ReadV8Config(context.Background(), server.URL, "private"); !errors.Is(err, V8InvalidConfig) {
		t.Fatalf("accepted decompression bomb: %v", err)
	}
}

func TestFreshV8CancellationAndURLBoundary(t *testing.T) {
	for _, raw := range []string{"https://key@host", "http://host/%2e/config", "http://host/../config", "http://host?key=secret", "http://host#secret", "file:///tmp/config", "http://host/\\v8", " http://host", "http://host:bad"} {
		if _, err := NormalizeV8BaseURL(raw); err == nil {
			t.Errorf("accepted invalid URL %q", raw)
		}
	}
	// External-only admission validates impossible ports before Fresh CAS,
	// while the accepted CPA v8 safe-read implementation remains byte-identical.
	for _, raw := range []string{"http://host:0", "http://host:65536", "http://[::1]:65536", "http://host:999999999999999999999", "http://host:"} {
		if ValidV8SourcePort(raw) {
			t.Errorf("External preflight accepted impossible TCP port %q", raw)
		}
	}
	for _, raw := range []string{"http://host", "https://host:1", "http://host:65535", "http://[::1]:65535"} {
		if !ValidV8SourcePort(raw) {
			t.Errorf("External preflight refused valid TCP port %q", raw)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := ReadV8Config(ctx, server.URL, "private"); err == nil || time.Since(start) > time.Second {
		t.Fatalf("deadline not enforced: %v", err)
	}
}

func TestReadV8ConfigEmitsUnknownResourceForNestedOffTableKeysUnderNamedRoots(t *testing.T) {
	namedOnlyClean := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":["k1"]},"plugins":{"enabled":false}}`))
	}))
	defer namedOnlyClean.Close()
	cleanObs, err := ReadV8Config(t.Context(), namedOnlyClean.URL, "secret")
	if err != nil {
		t.Fatalf("ReadV8Config clean: %v", err)
	}
	if slices.Contains(cleanObs.ResourceSections, "unknown_cpa_native_resource") {
		t.Fatalf("clean named-roots config unexpectedly emitted unknown_cpa_native_resource: %v", cleanObs.ResourceSections)
	}

	namedWithMystery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":["k1"]},"plugins":{"enabled":false,"mystery_flag":true}}`))
	}))
	defer namedWithMystery.Close()
	mysteryObs, err := ReadV8Config(t.Context(), namedWithMystery.URL, "secret")
	if err != nil {
		t.Fatalf("ReadV8Config mystery: %v", err)
	}
	if !slices.Contains(mysteryObs.ResourceSections, "unknown_cpa_native_resource") {
		t.Fatalf("named-roots config with plugins.mystery_flag omitted unknown_cpa_native_resource: %v", mysteryObs.ResourceSections)
	}
}

func TestReadV8AdoptionSnapshotDetectsArchivedYAMLUnknownKeysWithoutV0Fallback(t *testing.T) {
	t.Run("detects archived unknown keys when JSON projection is clean", func(t *testing.T) {
		var jsonCalls, yamlCalls, v0Calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer secret-mgmt" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			switch r.URL.Path {
			case "/v8/management/config":
				jsonCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":["k1"]},"routing":{"strategy":"round-robin"},"plugins":{}}`))
			case "/v8/management/config.yaml":
				yamlCalls.Add(1)
				if r.Header.Get("Cache-Control") != "no-store" || r.Header.Get("Accept-Encoding") != "identity, gzip" {
					t.Errorf("missing safe-read headers on config.yaml: %v", r.Header)
				}
				w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
				_, _ = w.Write([]byte(`# operator header
config-version: 8
server:
    port: 8317
oauth: {auth-dir: "auth", providers: {codex: {}}}
plugins: {}
upstream:
    codex:
        disable-codex-cloaking: true

# oauth.providers.codex.retired-setting: false
# plugins.mystery_flag: true
`))
			default:
				if strings.HasPrefix(r.URL.Path, "/v0/") {
					v0Calls.Add(1)
				}
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		snapshot, err := ReadV8AdoptionSnapshot(t.Context(), server.URL, "secret-mgmt")
		if err != nil {
			t.Fatalf("ReadV8AdoptionSnapshot: %v", err)
		}
		if !slices.Contains(snapshot.ResourceSections, "unknown_cpa_native_resource") {
			t.Fatalf("expected unknown_cpa_native_resource in %v", snapshot.ResourceSections)
		}
		if jsonCalls.Load() != 1 || yamlCalls.Load() != 1 || v0Calls.Load() != 0 {
			t.Fatalf("unexpected calls json=%d yaml=%d v0=%d", jsonCalls.Load(), yamlCalls.Load(), v0Calls.Load())
		}

		// ReadV8Config (External read-only probe) must keep strictly 1 request and never touch .yaml.
		obs, err := ReadV8Config(t.Context(), server.URL, "secret-mgmt")
		if err != nil {
			t.Fatalf("ReadV8Config: %v", err)
		}
		if jsonCalls.Load() != 2 || yamlCalls.Load() != 1 || v0Calls.Load() != 0 {
			t.Fatalf("ReadV8Config made unexpected yaml/v0 requests: json=%d yaml=%d v0=%d (obs=%+v)", jsonCalls.Load(), yamlCalls.Load(), v0Calls.Load(), obs)
		}
	})

	t.Run("clean alias YAML and non-200 YAML fallback do not fail or mark unknown", func(t *testing.T) {
		for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusInternalServerError} {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v8/management/config":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(safeV8Config))
				case "/v8/management/config.yaml":
					w.WriteHeader(status)
					if status == http.StatusOK {
						_, _ = w.Write([]byte(`# operator comment
config-version: 8
server:
    port: 8317
oauth:
    auth-dir: /private/auth
    providers:
        codex:
            live-media-relay: {}

# oauth.providers.codex.live-media-relay.allow-private-remote-ips: true
`))
					}
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			snapshot, err := ReadV8AdoptionSnapshot(t.Context(), server.URL, "secret-mgmt")
			server.Close()
			if err != nil {
				t.Fatalf("status=%d ReadV8AdoptionSnapshot failed: %v", status, err)
			}
			if slices.Contains(snapshot.ResourceSections, "unknown_cpa_native_resource") {
				t.Fatalf("status=%d unexpected unknown_cpa_native_resource in %v", status, snapshot.ResourceSections)
			}
		}
	})

	t.Run("skips YAML probe when JSON already surfaced unknown key", func(t *testing.T) {
		var yamlCalls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v8/management/config":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":["k1"]},"plugins":{"mystery_flag":true}}`))
			case "/v8/management/config.yaml":
				yamlCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()
		snapshot, err := ReadV8AdoptionSnapshot(t.Context(), server.URL, "secret-mgmt")
		if err != nil || !slices.Contains(snapshot.ResourceSections, "unknown_cpa_native_resource") || yamlCalls.Load() != 0 {
			t.Fatalf("snapshot=%+v err=%v yamlCalls=%d", snapshot, err, yamlCalls.Load())
		}
	})
}
