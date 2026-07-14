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

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

type recordingUsageHandler struct {
	events []usage.Event
}

func (h *recordingUsageHandler) HandleUsageEvents(_ context.Context, _ RuntimeConfig, events []usage.Event) {
	h.events = append(h.events, events...)
}

func TestAuthSnapshotResolverStreamsAuthFiles(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"files":[{"auth_index":"auth-1","account":"alice@example.com","name":"alice.json","provider":"codex","padding":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", 1024*1024)))
		_, _ = w.Write([]byte(`"}]}`))
	}))
	t.Cleanup(upstream.Close)

	resolver := newAuthSnapshotResolver()
	resolver.client = upstream.Client()
	snapshots, err := resolver.fetch(context.Background(), upstream.URL, "management-key")
	if err != nil {
		t.Fatalf("fetch snapshots: %v", err)
	}
	if snapshot := snapshots["auth-1"]; snapshot.Account != "alice@example.com" || snapshot.FileName != "alice.json" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

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

func TestManagerEnrichesMissingProjectSnapshotWithoutOverwritingAccount(t *testing.T) {
	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			if r.Header.Get("Authorization") != "Bearer management-key" {
				http.Error(w, "bad key", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"files":[{"auth_index":"auth-1","account":"alice@example.com","label":"Alice","name":"alice.json","provider":"codex","project_id":"vertex-project-42"}]}`))
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
				"account_snapshot": "preserved@example.com",
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

	events, err := db.RecentEvents(context.Background(), 10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].AccountSnapshot != "preserved@example.com" {
		t.Fatalf("account snapshot = %q", events[0].AccountSnapshot)
	}
	if events[0].AuthProjectIDSnapshot != "vertex-project-42" {
		t.Fatalf("project snapshot = %q", events[0].AuthProjectIDSnapshot)
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

func TestManagerOnlyPassesInsertedEventsToHandler(t *testing.T) {
	db := newTestStore(t)
	cfg := testConfig(t, "http")
	manager := NewManager(cfg, db)
	handler := &recordingUsageHandler{}
	manager.SetUsageEventHandler(handler)

	duplicateQuotaPayload := `{
		"request_id":"duplicate-quota",
		"timestamp":"2026-05-06T00:00:00Z",
		"provider":"codex",
		"model":"gpt-test",
		"endpoint":"POST /v1/chat/completions",
		"auth_file_snapshot":"codex-auth.json",
		"auth_index":"auth-1",
		"failed":true,
		"fail_status_code":429,
		"fail_body":"{\"error\":{\"type\":\"usage_limit_reached\",\"resets_in_seconds\":60}}"
	}`
	duplicateEvent, err := usage.NormalizeRaw([]byte(duplicateQuotaPayload))
	if err != nil {
		t.Fatalf("normalize duplicate payload: %v", err)
	}
	if _, err := db.InsertEvents(context.Background(), []usage.Event{duplicateEvent}); err != nil {
		t.Fatalf("seed duplicate event: %v", err)
	}

	newNormalPayload := `{
		"request_id":"new-normal",
		"timestamp":"2026-05-06T00:00:01Z",
		"provider":"codex",
		"model":"gpt-test",
		"endpoint":"POST /v1/chat/completions",
		"input_tokens":1,
		"output_tokens":2
	}`
	newEvent, err := usage.NormalizeRaw([]byte(newNormalPayload))
	if err != nil {
		t.Fatalf("normalize new payload: %v", err)
	}

	if err := manager.processItems(context.Background(), RuntimeConfig{}, []string{duplicateQuotaPayload, newNormalPayload}); err != nil {
		t.Fatalf("process items: %v", err)
	}

	if len(handler.events) != 1 {
		t.Fatalf("handler events = %#v, want only newly inserted normal event", handler.events)
	}
	if handler.events[0].EventHash != newEvent.EventHash {
		t.Fatalf("handler event hash = %q, want %q", handler.events[0].EventHash, newEvent.EventHash)
	}
	if handler.events[0].EventHash == duplicateEvent.EventHash || handler.events[0].FailStatusCode == http.StatusTooManyRequests {
		t.Fatalf("duplicate quota event was passed to handler: %#v", handler.events[0])
	}
}

func TestManagerSkipsUsageControlPayloadsAndRefreshesSnapshots(t *testing.T) {
	db := newTestStore(t)
	cfg := testConfig(t, "subscribe")
	manager := NewManager(cfg, db)
	manager.snapshotResolver.baseURL = "http://cpa.local:8317"
	manager.snapshotResolver.managementKey = "management-key"
	manager.snapshotResolver.expiresAt = time.Now().Add(time.Minute)
	manager.snapshotResolver.snapshots = map[string]authSnapshot{
		"auth-1": {Account: "alice@example.com"},
	}

	err := manager.processItems(context.Background(), RuntimeConfig{}, []string{
		`{"support_refresh":true}`,
		`{"refresh":true}`,
		`{"timestamp":"2026-05-06T00:00:00Z","model":"gpt-test","endpoint":"POST /v1/chat/completions","input_tokens":1,"output_tokens":2}`,
	})
	if err != nil {
		t.Fatalf("process items: %v", err)
	}

	events, deadLetters, err := db.Counts(context.Background())
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if events != 1 || deadLetters != 0 {
		t.Fatalf("counts events=%d deadLetters=%d, want 1/0", events, deadLetters)
	}
	if manager.snapshotResolver.baseURL != "" ||
		manager.snapshotResolver.managementKey != "" ||
		!manager.snapshotResolver.expiresAt.IsZero() ||
		manager.snapshotResolver.snapshots != nil {
		t.Fatalf("snapshot cache was not cleared: %#v", manager.snapshotResolver)
	}
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
