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
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := ReadV8Config(ctx, server.URL, "private"); err == nil || time.Since(start) > time.Second {
		t.Fatalf("deadline not enforced: %v", err)
	}
}
