package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/config"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/store"
)

func TestManagerConsumesHTTPUsageQueue(t *testing.T) {
	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			if r.Header.Get("Authorization") != "Bearer management-key" {
				http.Error(w, "bad key", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"files":[{"auth_index":"auth-1","account":"alice@example.com","label":"Alice","name":"alice.json","provider":"codex"}]}`))
			return
		}
		if r.URL.Path != "/v0/management/usage-queue" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer management-key" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if atomic.AddInt32(&calls, 1) == 1 {
			_, _ = w.Write([]byte(`[{
				"timestamp": "2026-05-06T00:00:00Z",
				"model": "gpt-test",
				"endpoint": "POST /v1/chat/completions",
				"auth_index": "auth-1",
				"input_tokens": 10,
				"output_tokens": 5
			}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(upstream.Close)

	db := newTestStore(t)
	cfg := testConfig(t, "auto")
	manager := NewManager(cfg, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.Start(ctx, RuntimeConfig{
		CPAUpstreamURL: upstream.URL,
		ManagementKey:  "management-key",
	})

	waitFor(t, func() bool {
		events, _, err := db.Counts(context.Background())
		return err == nil && events == 1
	})

	status := manager.Status()
	if status.Transport != "http" {
		t.Fatalf("transport = %q, want http", status.Transport)
	}
	if status.TotalInserted != 1 {
		t.Fatalf("total inserted = %d, want 1", status.TotalInserted)
	}
	events, err := db.RecentEvents(context.Background(), 10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].AccountSnapshot != "alice@example.com" {
		t.Fatalf("account snapshot = %q", events[0].AccountSnapshot)
	}
	if events[0].AuthLabelSnapshot != "Alice" {
		t.Fatalf("auth label snapshot = %q", events[0].AuthLabelSnapshot)
	}
}

func TestManagerFallsBackToRESPWhenHTTPQueueUnsupported(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(upstream.Close)

	db := newTestStore(t)
	cfg := testConfig(t, "auto")
	manager := NewManager(cfg, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.Start(ctx, RuntimeConfig{
		CPAUpstreamURL: upstream.URL,
		ManagementKey:  "management-key",
	})

	waitFor(t, func() bool {
		status := manager.Status()
		return status.Transport == "resp" && strings.Contains(status.LastError, "unsupported RESP prefix")
	})
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func testConfig(t *testing.T, mode string) config.Config {
	t.Helper()
	return config.Config{
		DBPath:        filepath.Join(t.TempDir(), "usage.sqlite"),
		CollectorMode: mode,
		Queue:         "usage",
		PopSide:       "right",
		BatchSize:     10,
		PollInterval:  10 * time.Millisecond,
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before deadline")
}
