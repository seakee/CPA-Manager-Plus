package collector

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/resp"
	monitoringsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/monitoring"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	quotasnapshotsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/quotasnapshot"
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
	snapshots, ambiguous, err := resolver.fetch(context.Background(), upstream.URL, "management-key")
	if err != nil {
		t.Fatalf("fetch snapshots: %v", err)
	}
	if len(ambiguous) != 0 {
		t.Fatalf("unexpected ambiguous auth indexes: %#v", ambiguous)
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
			_, _ = w.Write([]byte(`{"files":[{"auth_index":"auth-1","account":"alice@example.com","label":"Alice","name":"alice.json","provider":"vertex","project_id":"vertex-project-42"}]}`))
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

func TestManagerEnrichesCodexProjectSnapshotFromAccountID(t *testing.T) {
	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"files":[{"auth_index":"auth-1","account":"same@example.com","name":"codex.json","provider":"codex","project_id":"unsafe-generic-project","id_token":{"chatgpt_account_id":"account-a"}}]}`))
			return
		}
		if r.URL.Path != "/v0/management/usage-queue" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if atomic.AddInt32(&calls, 1) == 1 {
			_, _ = w.Write([]byte(`[{"timestamp":"2026-05-06T00:00:00Z","model":"gpt-test","auth_index":"auth-1","input_tokens":10,"output_tokens":5}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(upstream.Close)

	db := newTestStore(t)
	manager := NewManager(testConfig(t, "auto"), db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"})

	waitFor(t, func() bool {
		events, _, err := db.Counts(context.Background())
		return err == nil && events == 1
	})
	events, err := db.RecentEvents(context.Background(), 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("recent events: len=%d err=%v", len(events), err)
	}
	if events[0].AuthAccountIDSnapshot != "account-a" {
		t.Fatalf("codex account snapshot = %q, want %q", events[0].AuthAccountIDSnapshot, "account-a")
	}
	if events[0].AuthProjectIDSnapshot != "unsafe-generic-project" {
		t.Fatalf("codex project snapshot = %q, want %q", events[0].AuthProjectIDSnapshot, "unsafe-generic-project")
	}
}

// TestManagerEnrichesCodexAccountIDFromEffectiveProvider verifies that a raw
// event with provider="codex" but an empty auth_provider_snapshot still
// triggers auth-files enrichment so an explicit ChatGPT account_id can be
// resolved. Previously needsAccountSnapshotEnrichment only checked
// AuthProviderSnapshot, so a Codex event with all snapshot fields populated
// except AuthAccountIDSnapshot would skip enrichment entirely.
func TestManagerEnrichesCodexAccountIDFromEffectiveProvider(t *testing.T) {
	var calls int32
	var authFilesRequested int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			atomic.AddInt32(&authFilesRequested, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"files":[{"auth_index":"auth-1","account":"preserved@example.com","name":"codex.json","provider":"codex","project_id":"preserved-project","id_token":{"chatgpt_account_id":"account-a"}}]}`))
			return
		}
		if r.URL.Path != "/v0/management/usage-queue" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if atomic.AddInt32(&calls, 1) == 1 {
			// Raw event has provider=codex but no auth_provider_snapshot; account
			// and project snapshots are already present; only auth_account_id_snapshot
			// is missing.
			_, _ = w.Write([]byte(`[{"timestamp":"2026-05-06T00:00:00Z","model":"gpt-test","provider":"codex","auth_index":"auth-1","account_snapshot":"preserved@example.com","auth_project_id_snapshot":"preserved-project","input_tokens":10,"output_tokens":5}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(upstream.Close)

	db := newTestStore(t)
	manager := NewManager(testConfig(t, "auto"), db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"})

	waitFor(t, func() bool {
		events, _, err := db.Counts(context.Background())
		return err == nil && events == 1
	})
	if atomic.LoadInt32(&authFilesRequested) == 0 {
		t.Fatal("auth-files were never requested for effective-provider Codex event")
	}
	events, err := db.RecentEvents(context.Background(), 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("recent events: len=%d err=%v", len(events), err)
	}
	if events[0].AuthAccountIDSnapshot != "account-a" {
		t.Fatalf("codex account id snapshot = %q, want %q", events[0].AuthAccountIDSnapshot, "account-a")
	}
	if events[0].AccountSnapshot != "preserved@example.com" {
		t.Fatalf("account snapshot overwritten = %q, want %q", events[0].AccountSnapshot, "preserved@example.com")
	}
	if events[0].AuthProjectIDSnapshot != "preserved-project" {
		t.Fatalf("project snapshot overwritten = %q, want %q", events[0].AuthProjectIDSnapshot, "preserved-project")
	}
}

func TestManagerReplacesWeakCodexAccountSnapshotWithMemberEmail(t *testing.T) {
	db := newTestStore(t)
	manager := NewManager(testConfig(t, "auto"), db)
	manager.snapshotResolver.baseURL = "http://cpa.local:8317"
	manager.snapshotResolver.managementKey = "management-key"
	manager.snapshotResolver.expiresAt = time.Now().Add(time.Minute)
	manager.snapshotResolver.snapshots = map[string]authSnapshot{
		"auth-1": {
			Account:   "alice@example.com",
			AccountID: "workspace-1",
			Provider:  "codex",
		},
	}

	events := []usage.Event{{
		Provider:              "codex",
		AuthIndex:             "auth-1",
		AuthProviderSnapshot:  "codex",
		AuthAccountIDSnapshot: "workspace-1",
		AccountSnapshot:       "Alice",
		AuthProjectIDSnapshot: "project-1",
	}}
	manager.enrichAccountSnapshots(context.Background(), RuntimeConfig{
		CPAUpstreamURL: "http://cpa.local:8317",
		ManagementKey:  "management-key",
	}, events)

	if events[0].AccountSnapshot != "alice@example.com" {
		t.Fatalf("account snapshot = %q, want strong Codex member email", events[0].AccountSnapshot)
	}
}

func TestManagerDoesNotCombineConflictingCodexIdentityEvidence(t *testing.T) {
	tests := []struct {
		name              string
		eventMember       string
		eventWorkspace    string
		snapshotMember    string
		snapshotWorkspace string
		wantMember        string
		wantWorkspace     string
		wantFile          string
	}{
		{
			name:              "conflicting member",
			eventMember:       "alice@example.com",
			snapshotMember:    "bob@example.com",
			snapshotWorkspace: "workspace-1",
			wantMember:        "alice@example.com",
		},
		{
			name:              "matching member",
			eventMember:       "alice@example.com",
			snapshotMember:    "alice@example.com",
			snapshotWorkspace: "workspace-1",
			wantMember:        "alice@example.com",
			wantWorkspace:     "workspace-1",
			wantFile:          "alice.json",
		},
		{
			name:              "missing event member",
			snapshotMember:    "alice@example.com",
			snapshotWorkspace: "workspace-1",
			wantMember:        "alice@example.com",
			wantWorkspace:     "workspace-1",
			wantFile:          "alice.json",
		},
		{
			name:              "conflicting workspace",
			eventMember:       "alice@example.com",
			eventWorkspace:    "workspace-1",
			snapshotMember:    "alice@example.com",
			snapshotWorkspace: "workspace-2",
			wantMember:        "alice@example.com",
			wantWorkspace:     "workspace-1",
		},
		{
			name:              "strong member plus workspace-only snapshot",
			eventMember:       "alice@example.com",
			snapshotWorkspace: "workspace-2",
			wantMember:        "alice@example.com",
		},
		{
			name:           "strong workspace plus member-only snapshot",
			eventWorkspace: "workspace-1",
			snapshotMember: "alice@example.com",
			wantWorkspace:  "workspace-1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newTestStore(t)
			manager := NewManager(testConfig(t, "auto"), db)
			manager.snapshotResolver.baseURL = "http://cpa.local:8317"
			manager.snapshotResolver.managementKey = "management-key"
			manager.snapshotResolver.expiresAt = time.Now().Add(time.Minute)
			manager.snapshotResolver.snapshots = map[string]authSnapshot{
				"auth-1": {
					Account:   test.snapshotMember,
					AccountID: test.snapshotWorkspace,
					FileName:  "alice.json",
					Provider:  "codex",
				},
			}

			events := []usage.Event{{
				Provider:              "codex",
				AuthIndex:             "auth-1",
				AuthProviderSnapshot:  "codex",
				AuthAccountIDSnapshot: test.eventWorkspace,
				AccountSnapshot:       test.eventMember,
			}}
			manager.enrichAccountSnapshots(context.Background(), RuntimeConfig{
				CPAUpstreamURL: "http://cpa.local:8317",
				ManagementKey:  "management-key",
			}, events)

			got := events[0]
			if got.AccountSnapshot != test.wantMember || got.AuthAccountIDSnapshot != test.wantWorkspace || got.AuthFileSnapshot != test.wantFile {
				t.Fatalf("enriched identity = member:%q workspace:%q file:%q, want member:%q workspace:%q file:%q", got.AccountSnapshot, got.AuthAccountIDSnapshot, got.AuthFileSnapshot, test.wantMember, test.wantWorkspace, test.wantFile)
			}
		})
	}
}

func TestManagerDoesNotEnrichCodexEventFromMismatchedSnapshotProvider(t *testing.T) {
	db := newTestStore(t)
	manager := NewManager(testConfig(t, "auto"), db)
	manager.snapshotResolver.baseURL = "http://cpa.local:8317"
	manager.snapshotResolver.managementKey = "management-key"
	manager.snapshotResolver.expiresAt = time.Now().Add(time.Minute)
	manager.snapshotResolver.snapshots = map[string]authSnapshot{
		"auth-1": {
			Account:   "alice@example.com",
			AccountID: "workspace-1",
			Provider:  "codex",
		},
	}
	events := []usage.Event{{
		Provider:             "openai",
		AuthIndex:            "auth-1",
		AuthProviderSnapshot: "openai",
	}}
	manager.enrichAccountSnapshots(context.Background(), RuntimeConfig{
		CPAUpstreamURL: "http://cpa.local:8317",
		ManagementKey:  "management-key",
	}, events)
	if events[0].AccountSnapshot != "" || events[0].AuthAccountIDSnapshot != "" || events[0].AuthProviderSnapshot != "openai" {
		t.Fatalf("mismatched Codex snapshot enriched non-Codex event: %#v", events[0])
	}
}

func TestManagerDoesNotEnrichFromInvalidCodexSnapshotEvidence(t *testing.T) {
	db := newTestStore(t)
	manager := NewManager(testConfig(t, "auto"), db)
	manager.snapshotResolver.baseURL = "http://cpa.local:8317"
	manager.snapshotResolver.managementKey = "management-key"
	manager.snapshotResolver.expiresAt = time.Now().Add(time.Minute)
	manager.snapshotResolver.snapshots = map[string]authSnapshot{
		"auth-1": {
			Account:                "alice@example.com",
			AccountID:              "workspace-1",
			AccountSnapshotInvalid: true,
			Provider:               "codex",
			FileName:               "alice.json",
		},
	}
	events := []usage.Event{{
		Provider:              "codex",
		AuthIndex:             "auth-1",
		AuthProviderSnapshot:  "codex",
		AuthAccountIDSnapshot: "workspace-1",
	}}
	manager.enrichAccountSnapshots(context.Background(), RuntimeConfig{
		CPAUpstreamURL: "http://cpa.local:8317",
		ManagementKey:  "management-key",
	}, events)
	if events[0].AccountSnapshot != "" || events[0].AuthAccountIDSnapshot != "workspace-1" || events[0].AuthFileSnapshot != "" {
		t.Fatalf("invalid Codex auth snapshot was used for enrichment: %#v", events[0])
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

func TestManagerPersistsQuotaEvidenceAfterUsageInsert(t *testing.T) {
	db := newTestStore(t)
	manager := NewManager(testConfig(t, "http"), db)
	payload := `{
		"request_id":"quota-header",
		"timestamp":"2026-05-06T00:00:00Z",
		"provider":"codex",
		"model":"gpt-test",
		"endpoint":"POST /v1/responses",
		"auth_file_snapshot":"codex.json",
		"auth_provider_snapshot":"codex",
		"auth_index":"auth-1",
		"account_snapshot":"user@example.com",
		"response_headers":{
			"X-Codex-Primary-Used-Percent":["40"],
			"X-Codex-Primary-Reset-After-Seconds":["18000"],
			"X-Codex-Primary-Window-Minutes":["300"]
		}
	}`
	if err := manager.processItems(context.Background(), RuntimeConfig{}, []string{payload}); err != nil {
		t.Fatalf("process quota event: %v", err)
	}
	query, err := quotasnapshotsvc.New(db).Query(context.Background(), quotasnapshotsvc.QueryRequest{
		Accounts: []quotasnapshotsvc.QueryAccount{{
			RowKey: "row-1", Provider: "codex", Account: quotasnapshotsvc.AccountTarget{
				AuthFileSnapshot: "codex.json", AuthProviderSnapshot: "codex", AuthIndex: "auth-1",
			},
		}},
	})
	if err != nil {
		t.Fatalf("query quota snapshots: %v", err)
	}
	if len(query.Items) != 1 || len(query.Items[0].Windows) != 1 {
		t.Fatalf("quota snapshots = %#v", query)
	}
	window := query.Items[0].Windows[0]
	if window.ProviderWindowID != "five-hour" || window.Source != "response_header" {
		t.Fatalf("quota window = %#v", window)
	}
}

func TestManagerKeepsFirstUsageInCurrentWindowWhenHeaderBoundaryDrifts(t *testing.T) {
	const (
		firstObservedAtMS  = int64(1_785_928_574_638)
		secondObservedAtMS = int64(1_785_928_787_294)
		firstCycleStartMS  = int64(1_785_928_573_000)
		firstCycleEndMS    = int64(1_788_520_573_000)
		durationMS         = int64(30 * 24 * 60 * 60 * 1000)
	)
	db := newTestStore(t)
	manager := NewManager(testConfig(t, "http"), db)
	payloads := []string{
		`{
			"request_id":"quota-first-use",
			"timestamp":"2026-08-05T11:16:14.638Z",
			"provider":"codex",
			"model":"gpt-5.6-terra",
			"endpoint":"POST /v1/responses",
			"auth_file_snapshot":"codex-free.json",
			"auth_provider_snapshot":"codex",
			"auth_index":"auth-first-use",
			"account_snapshot":"first-use@example.com",
			"input_tokens":2500,
			"output_tokens":21,
			"response_headers":{
				"X-Codex-Primary-Used-Percent":["0"],
				"X-Codex-Primary-Reset-At":["1788520573000"],
				"X-Codex-Primary-Reset-After-Seconds":["2592000"],
				"X-Codex-Primary-Window-Minutes":["43200"],
				"X-Codex-Secondary-Used-Percent":["0"],
				"X-Codex-Secondary-Reset-After-Seconds":["0"],
				"X-Codex-Secondary-Window-Minutes":["0"]
			}
		}`,
		`{
			"request_id":"quota-second-use",
			"timestamp":"2026-08-05T11:19:47.294Z",
			"provider":"codex",
			"model":"gpt-5.6-terra",
			"endpoint":"POST /v1/responses",
			"auth_file_snapshot":"codex-free.json",
			"auth_provider_snapshot":"codex",
			"auth_index":"auth-first-use",
			"account_snapshot":"first-use@example.com",
			"input_tokens":1000,
			"output_tokens":24,
			"response_headers":{
				"X-Codex-Primary-Used-Percent":["0"],
				"X-Codex-Primary-Reset-At":["1788520580000"],
				"X-Codex-Primary-Reset-After-Seconds":["2591796"],
				"X-Codex-Primary-Window-Minutes":["43200"],
				"X-Codex-Secondary-Used-Percent":["0"],
				"X-Codex-Secondary-Reset-After-Seconds":["0"],
				"X-Codex-Secondary-Window-Minutes":["0"]
			}
		}`,
	}
	if err := manager.processItems(context.Background(), RuntimeConfig{}, payloads); err != nil {
		t.Fatalf("process first-use quota events: %v", err)
	}

	account := quotasnapshotsvc.AccountTarget{
		AuthFileSnapshot: "codex-free.json", AuthProviderSnapshot: "codex", AuthIndex: "auth-first-use",
	}
	query, err := quotasnapshotsvc.New(db).Query(context.Background(), quotasnapshotsvc.QueryRequest{
		NowMS: secondObservedAtMS + 1_000,
		Accounts: []quotasnapshotsvc.QueryAccount{{
			RowKey: "row-first-use", Provider: "codex", Account: account,
		}},
	})
	if err != nil {
		t.Fatalf("query first-use quota snapshots: %v", err)
	}
	if len(query.Items) != 1 || len(query.Items[0].Windows) != 1 {
		t.Fatalf("quota snapshots = %#v", query)
	}
	window := query.Items[0].Windows[0]
	if window.ProviderWindowID != "monthly" || window.CycleStartMS == nil || *window.CycleStartMS != firstCycleStartMS {
		t.Fatalf("stabilized quota window = %#v", window)
	}

	usageResult, err := monitoringsvc.New(db).AccountWindowUsage(context.Background(), monitoringsvc.AccountWindowUsageRequest{
		Windows: []monitoringsvc.AccountWindowUsageTarget{
			{
				RequestKey: "current", RowKey: "row-first-use", ProviderWindowID: "monthly", Period: "current",
				FromMS: firstCycleStartMS, ToMS: secondObservedAtMS + 1_000,
				ModelScope:           monitoringsvc.AccountWindowModelScope{Kind: "all"},
				AccountSnapshot:      "first-use@example.com",
				AuthFileSnapshot:     account.AuthFileSnapshot,
				AuthProviderSnapshot: account.AuthProviderSnapshot,
				AuthIndex:            account.AuthIndex,
			},
			{
				RequestKey: "previous", RowKey: "row-first-use", ProviderWindowID: "monthly", Period: "previous",
				FromMS: firstCycleStartMS - durationMS, ToMS: firstCycleStartMS,
				ModelScope:           monitoringsvc.AccountWindowModelScope{Kind: "all"},
				AccountSnapshot:      "first-use@example.com",
				AuthFileSnapshot:     account.AuthFileSnapshot,
				AuthProviderSnapshot: account.AuthProviderSnapshot,
				AuthIndex:            account.AuthIndex,
			},
		},
	})
	if err != nil {
		t.Fatalf("query first-use window usage: %v", err)
	}
	if len(usageResult.Items) != 2 {
		t.Fatalf("window usage = %#v", usageResult)
	}
	current, previous := usageResult.Items[0], usageResult.Items[1]
	if !current.Matched || current.TotalRequests != 2 || current.TotalTokens != 3_545 {
		t.Fatalf("current window usage = %#v", current)
	}
	if previous.Matched || previous.TotalRequests != 0 || previous.TotalTokens != 0 {
		t.Fatalf("previous window usage = %#v", previous)
	}
	if firstCycleEndMS-firstCycleStartMS != durationMS || firstObservedAtMS < firstCycleStartMS {
		t.Fatal("invalid first-use test fixture")
	}
}

func TestManagerKeepsFirstRequestAcrossFiveHourScheduledRollover(t *testing.T) {
	for _, scenario := range []struct {
		name              string
		firstOffset       time.Duration
		latencyMS         int64
		previousRequests  int64
		actualStartOffset time.Duration
		outOfOrder        bool
	}{
		{"main_five_seconds_before_header_start", 10 * time.Second, 8_000, 1, 10 * time.Second, false},
		{"A_equal_to_header_start", 15 * time.Second, 1_000, 1, 15 * time.Second, false},
		{"B_after_header_start", 16 * time.Second, 1_000, 1, 15 * time.Second, false},
		{"C_one_second_before_header_start", 14 * time.Second, 2_000, 1, 14 * time.Second, false},
		// Start-time attribution puts this call in the old cycle. Its response
		// headers alone cannot prove which upstream cycle charged the tokens.
		{"D_started_before_old_end_completed_after_new_start", -2 * time.Second, 20_000, 2, 15 * time.Second, false},
		{"out_of_order_completion", 10 * time.Second, 20_000, 1, 10 * time.Second, true},
		{"out_of_order_started_before_old_end_stays_previous", -2 * time.Second, 25_000, 2, 12 * time.Second, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			// Keep this fixture on one UTC date. Header boundaries come from reset_at
			// minus window_minutes; usage timestamps retain CPA's request-start time.
			base := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
			oldEnd := base.Add(5 * time.Hour)
			providerStart := oldEnd.Add(15 * time.Second)
			providerEnd := providerStart.Add(5 * time.Hour)
			firstAt := oldEnd.Add(scenario.firstOffset)
			secondAt := oldEnd.Add(20 * time.Second)
			secondLatencyMS := int64(1_000)
			firstUsed, secondUsed := int64(1), int64(2)
			if scenario.outOfOrder {
				secondAt = oldEnd.Add(12 * time.Second)
				secondLatencyMS = 6_000
				firstUsed, secondUsed = 2, 1
			}
			now := oldEnd.Add(30 * time.Second)
			ctx := context.Background()
			db := newTestStore(t)
			manager := NewManager(testConfig(t, "http"), db)
			if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
				"gpt-5.6-terra": {Prompt: 1, Completion: 2},
			}); err != nil {
				t.Fatalf("save fixed model price: %v", err)
			}
			payload := func(id string, at, reset time.Time, input, used, latencyMS int64) string {
				return fmt.Sprintf(`{
			"request_id":%q,"timestamp":%q,"latency_ms":%d,
			"provider":"codex","model":"gpt-5.6-terra",
			"endpoint":"POST /v1/responses","source":"codex-five-hour.json",
			"auth_file_snapshot":"codex-five-hour.json",
			"auth_provider_snapshot":"codex","auth_index":"auth-five-hour",
			"account_snapshot":"five-hour@example.com",
			"tokens":{"input_tokens":%d,"output_tokens":1000,"total_tokens":%d},
			"response_headers":{
				"X-Codex-Primary-Used-Percent":["%d"],
				"X-Codex-Primary-Reset-At":["%d"],
				"X-Codex-Primary-Window-Minutes":["300"]
			}
		}`, id, at.UTC().Format(time.RFC3339Nano), latencyMS, input, input+1000, used, reset.Unix())
			}
			payloads := []string{
				payload("scheduled-old", oldEnd.Add(-10*time.Second), oldEnd, 10_000, 80, 1_000),
				payload("scheduled-first", firstAt, providerEnd, 132_000, firstUsed, scenario.latencyMS),
				payload("scheduled-second", secondAt, providerEnd, 20_000, secondUsed, secondLatencyMS),
			}
			if scenario.outOfOrder {
				// B completes at 05:00:18; A completes at 05:00:30.
				// Separate collector calls preserve completion-order delivery.
				payloads[1], payloads[2] = payloads[2], payloads[1]
			}
			// Import one completed call at a time through the normal collector path.
			for _, item := range payloads {
				if err := manager.processItems(ctx, RuntimeConfig{}, []string{item}); err != nil {
					t.Fatalf("process scheduled rollover event: %v", err)
				}
			}
			count, deadLetters, err := db.Counts(ctx)
			if err != nil || count != 3 || deadLetters != 0 {
				t.Fatalf("persisted=%d dead_letters=%d err=%v, want 3/0/nil", count, deadLetters, err)
			}
			events, err := db.RecentEvents(ctx, 10)
			if err != nil || len(events) != 3 {
				t.Fatalf("read persisted events: len=%d err=%v", len(events), err)
			}
			wantTimes := map[string]int64{
				"scheduled-old":   oldEnd.Add(-10 * time.Second).UnixMilli(),
				"scheduled-first": firstAt.UnixMilli(), "scheduled-second": secondAt.UnixMilli(),
			}
			for _, event := range events {
				want, ok := wantTimes[event.RequestID]
				if !ok || event.TimestampMS != want || event.AuthFileSnapshot != "codex-five-hour.json" ||
					event.AuthProviderSnapshot != "codex" || event.AuthIndex != "auth-five-hour" ||
					event.AccountSnapshot != "five-hour@example.com" || event.Model != "gpt-5.6-terra" ||
					event.Provider != "codex" || event.ResponseMetadata == nil || event.ResponseMetadata.Quota == nil {
					t.Fatalf("persisted event lost timestamp, identity, model or quota evidence: %#v", event)
				}
				delete(wantTimes, event.RequestID)
				if event.RequestID == "scheduled-first" {
					if event.LatencyMS == nil || *event.LatencyMS != scenario.latencyMS {
						t.Fatalf("first request latency = %v", event.LatencyMS)
					}
					t.Logf("first_request.completed_at_ms=%d", event.TimestampMS+*event.LatencyMS)
				}
			}
			account := quotasnapshotsvc.AccountTarget{
				AccountSnapshot: "five-hour@example.com", AuthFileSnapshot: "codex-five-hour.json",
				AuthProviderSnapshot: "codex", AuthIndex: "auth-five-hour",
			}
			query, err := quotasnapshotsvc.New(db).Query(ctx, quotasnapshotsvc.QueryRequest{
				NowMS: now.UnixMilli(), Accounts: []quotasnapshotsvc.QueryAccount{{
					RowKey: "row-five-hour", Provider: "codex", Account: account,
				}},
			})
			if err != nil || len(query.Items) != 1 || len(query.Items[0].Windows) != 1 {
				t.Fatalf("quota query=%#v err=%v", query, err)
			}
			window := query.Items[0].Windows[0]
			current, previous := window.CurrentCycle, window.PreviousCycle
			if window.ProviderWindowID != "five-hour" || window.ModelScopeKind != "family" || window.ModelScopeKey != "codex_main" || window.Stale ||
				current == nil || previous == nil || previous.ActualEndMS == nil ||
				current.ScheduledStartMS == nil || current.ScheduledEndMS == nil {
				t.Fatalf("missing queryable five-hour lifecycle: %#v", window)
			}
			t.Logf("old_cycle.actual_start_ms=%d old_cycle.actual_end_ms=%d end_reason=%s",
				previous.ActualStartMS, *previous.ActualEndMS, previous.EndReason)
			t.Logf("current_cycle.actual_start_ms=%d current_cycle.scheduled_start_ms=%d current_cycle.scheduled_end_ms=%d",
				current.ActualStartMS, *current.ScheduledStartMS, *current.ScheduledEndMS)
			t.Logf("first_request.timestamp_ms=%d second_request.timestamp_ms=%d", firstAt.UnixMilli(), secondAt.UnixMilli())
			if previous.ActualStartMS != base.UnixMilli() || *previous.ActualEndMS != oldEnd.UnixMilli() ||
				previous.EndReason != "scheduled" || *current.ScheduledStartMS != providerStart.UnixMilli() ||
				*current.ScheduledEndMS != providerEnd.UnixMilli() {
				t.Fatalf("unexpected provider timing or scheduled rollover: previous=%#v current=%#v", previous, current)
			}
			if current.ActualStartMS != oldEnd.Add(scenario.actualStartOffset).UnixMilli() {
				t.Errorf("actual_start_ms=%d, want %d after both requests", current.ActualStartMS, oldEnd.Add(scenario.actualStartOffset).UnixMilli())
			}
			monitoring := monitoringsvc.New(db)
			history, err := monitoring.AccountHistory(ctx, monitoringsvc.AccountHistoryRequest{
				CatchUp: true, Accounts: []monitoringsvc.AccountHistoryTarget{{
					RowKey: "row-five-hour", AccountSnapshot: account.AccountSnapshot,
					AuthFileSnapshot: account.AuthFileSnapshot, AuthProviderSnapshot: account.AuthProviderSnapshot,
					AuthIndex: account.AuthIndex,
				}},
			})
			if err != nil || len(history.Items) != 1 || !history.Items[0].Matched || history.Items[0].TotalRequests != 3 {
				t.Fatalf("account history=%#v err=%v", history, err)
			}
			snapshots, err := db.QuotaSnapshots.ListCandidates(ctx, history.Items[0].AccountKey, "codex", 10)
			wantCandidates := 3
			if scenario.outOfOrder {
				// ListCandidates excludes historical observations; the three
				// persisted usage events were checked independently above.
				wantCandidates = 2
			}
			if err != nil || len(snapshots) != wantCandidates {
				t.Fatalf("quota evidence candidates: count=%d err=%v, want %d", len(snapshots), err, wantCandidates)
			}
			for _, snapshot := range snapshots {
				wantCycleID := current.ID
				if snapshot.SourceObservationID == "scheduled-old" {
					wantCycleID = previous.ID
				}
				if snapshot.CycleID != wantCycleID {
					t.Fatalf("Header evidence assigned to wrong cycle: %#v, want cycle %d", snapshot, wantCycleID)
				}
				t.Logf("accepted_header.request=%s cycle_id=%d", snapshot.SourceObservationID, snapshot.CycleID)
			}
			target := func(key, period string, fromMS, toMS int64) monitoringsvc.AccountWindowUsageTarget {
				return monitoringsvc.AccountWindowUsageTarget{
					RequestKey: key, RowKey: "row-five-hour", ProviderWindowID: window.ProviderWindowID,
					Period: period, FromMS: fromMS, ToMS: toMS,
					ModelScope: monitoringsvc.AccountWindowModelScope{
						Kind: window.ModelScopeKind, Key: window.ModelScopeKey, Models: window.ModelIDs, Complete: true,
					},
					AccountSnapshot: account.AccountSnapshot, AuthFileSnapshot: account.AuthFileSnapshot,
					AuthProviderSnapshot: account.AuthProviderSnapshot, AuthIndex: account.AuthIndex,
				}
			}
			result, err := monitoring.AccountWindowUsage(ctx, monitoringsvc.AccountWindowUsageRequest{
				Windows: []monitoringsvc.AccountWindowUsageTarget{
					target("previous", "previous", previous.ActualStartMS, *previous.ActualEndMS),
					target("current", "current", current.ActualStartMS, now.UnixMilli()),
					target("all", "current", base.UnixMilli(), now.UnixMilli()),
				},
			})
			if err != nil || len(result.Items) != 3 {
				t.Fatalf("window usage=%#v err=%v", result, err)
			}
			oldUsage, currentUsage, allUsage := result.Items[0], result.Items[1], result.Items[2]
			for _, item := range result.Items {
				if item.ScopeMatchStatus != "complete" || item.UnmatchedRequests != 0 {
					t.Fatalf("incomplete model scope match: %#v", item)
				}
			}
			for _, event := range events {
				inPrevious := event.TimestampMS >= previous.ActualStartMS && event.TimestampMS < *previous.ActualEndMS
				inCurrent := event.TimestampMS >= current.ActualStartMS && event.TimestampMS < now.UnixMilli()
				t.Logf("request=%s timestamp_ms=%d in_previous=%t in_current=%t", event.RequestID, event.TimestampMS, inPrevious, inCurrent)
				if inPrevious && inCurrent {
					t.Errorf("request %s belongs to both query intervals", event.RequestID)
				}
			}
			t.Logf("previous_window.total_requests=%d current_window.total_requests=%d account_total_requests=%d",
				oldUsage.TotalRequests, currentUsage.TotalRequests, history.Items[0].TotalRequests)
			t.Logf("costs: previous=%.6f current=%.6f all=%.6f gap=%.6f; tokens: previous=%d current=%d all=%d",
				oldUsage.TotalCost, currentUsage.TotalCost, allUsage.TotalCost,
				allUsage.TotalCost-oldUsage.TotalCost-currentUsage.TotalCost,
				oldUsage.TotalTokens, currentUsage.TotalTokens, allUsage.TotalTokens)
			if !allUsage.Matched || allUsage.TotalRequests != 3 || allUsage.TotalTokens != 165_000 ||
				math.Abs(allUsage.TotalCost-0.168) > 1e-9 || history.Items[0].TotalCost == nil ||
				math.Abs(*history.Items[0].TotalCost-allUsage.TotalCost) > 1e-9 {
				t.Fatalf("account identity or price calculation mismatch: all=%#v history=%#v", allUsage, history.Items[0])
			}
			wantPreviousTokens, wantCurrentTokens := int64(11_000), int64(154_000)
			wantPreviousCost, wantCurrentCost := 0.012, 0.156
			if scenario.previousRequests == 2 {
				wantPreviousTokens, wantCurrentTokens = 144_000, 21_000
				wantPreviousCost, wantCurrentCost = 0.146, 0.022
			}
			if !oldUsage.Matched || oldUsage.TotalRequests != scenario.previousRequests || oldUsage.TotalTokens != wantPreviousTokens ||
				math.Abs(oldUsage.TotalCost-wantPreviousCost) > 1e-9 {
				t.Errorf("previous window = %#v, want %d requests", oldUsage, scenario.previousRequests)
			}
			if !currentUsage.Matched || currentUsage.TotalRequests != 3-scenario.previousRequests || currentUsage.TotalTokens != wantCurrentTokens ||
				math.Abs(currentUsage.TotalCost-wantCurrentCost) > 1e-9 {
				t.Errorf("current window = %#v, want %d requests, %d tokens and $%.3f", currentUsage, 3-scenario.previousRequests, wantCurrentTokens, wantCurrentCost)
			}
			if oldUsage.TotalRequests+currentUsage.TotalRequests != allUsage.TotalRequests {
				t.Errorf("cycle partition lost or duplicated requests: previous=%d current=%d all=%d",
					oldUsage.TotalRequests, currentUsage.TotalRequests, allUsage.TotalRequests)
			}
		})
	}
}

func TestManagerAssignsFirstEarlyResetRequestToNewWindow(t *testing.T) {
	const (
		oldRequestAtMS   = int64(1_785_900_000_000)
		firstRequestAtMS = int64(1_785_928_574_638)
		providerStartMS  = int64(1_785_928_600_000)
		durationMS       = int64(7 * 24 * 60 * 60 * 1000)
	)
	db := newTestStore(t)
	manager := NewManager(testConfig(t, "http"), db)
	oldResetAtMS := oldRequestAtMS + durationMS
	providerResetAtMS := providerStartMS + durationMS
	payloads := []string{
		fmt.Sprintf(`{
			"request_id":"quota-before-early-reset",
			"timestamp":"%s",
			"provider":"codex",
			"model":"gpt-5.6-terra",
			"endpoint":"POST /v1/responses",
			"auth_file_snapshot":"codex-early-reset.json",
			"auth_provider_snapshot":"codex",
			"auth_index":"auth-early-reset",
			"account_snapshot":"early-reset@example.com",
			"input_tokens":100,
			"output_tokens":10,
			"response_headers":{
				"X-Codex-Primary-Used-Percent":["4"],
				"X-Codex-Primary-Reset-At":["%d"],
				"X-Codex-Primary-Window-Minutes":["10080"]
			}
		}`, time.UnixMilli(oldRequestAtMS).UTC().Format(time.RFC3339Nano), oldResetAtMS),
		fmt.Sprintf(`{
			"request_id":"quota-first-early-reset-use",
			"timestamp":"%s",
			"provider":"codex",
			"model":"gpt-5.6-terra",
			"endpoint":"POST /v1/responses",
			"auth_file_snapshot":"codex-early-reset.json",
			"auth_provider_snapshot":"codex",
			"auth_index":"auth-early-reset",
			"account_snapshot":"early-reset@example.com",
			"input_tokens":2500,
			"output_tokens":21,
			"response_headers":{
				"X-Codex-Primary-Used-Percent":["0"],
				"X-Codex-Primary-Reset-At":["%d"],
				"X-Codex-Primary-Window-Minutes":["10080"]
			}
		}`, time.UnixMilli(firstRequestAtMS).UTC().Format(time.RFC3339Nano), providerResetAtMS),
	}
	if err := manager.processItems(context.Background(), RuntimeConfig{}, payloads); err != nil {
		t.Fatalf("process early-reset quota events: %v", err)
	}

	account := quotasnapshotsvc.AccountTarget{
		AuthFileSnapshot: "codex-early-reset.json", AuthProviderSnapshot: "codex", AuthIndex: "auth-early-reset",
	}
	query, err := quotasnapshotsvc.New(db).Query(context.Background(), quotasnapshotsvc.QueryRequest{
		NowMS: firstRequestAtMS + 1_000,
		Accounts: []quotasnapshotsvc.QueryAccount{{
			RowKey: "row-early-reset", Provider: "codex", Account: account,
		}},
	})
	if err != nil {
		t.Fatalf("query early-reset quota snapshots: %v", err)
	}
	if len(query.Items) != 1 || len(query.Items[0].Windows) != 1 {
		t.Fatalf("early-reset quota snapshots = %#v", query)
	}
	window := query.Items[0].Windows[0]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != firstRequestAtMS ||
		window.PreviousCycle == nil || window.PreviousCycle.ActualEndMS == nil ||
		*window.PreviousCycle.ActualEndMS != firstRequestAtMS {
		t.Fatalf("early-reset lifecycle = %#v", window)
	}

	usageResult, err := monitoringsvc.New(db).AccountWindowUsage(context.Background(), monitoringsvc.AccountWindowUsageRequest{
		Windows: []monitoringsvc.AccountWindowUsageTarget{
			{
				RequestKey: "current", RowKey: "row-early-reset", ProviderWindowID: "weekly", Period: "current",
				FromMS: firstRequestAtMS, ToMS: firstRequestAtMS + 1_000,
				ModelScope:           monitoringsvc.AccountWindowModelScope{Kind: "all"},
				AccountSnapshot:      "early-reset@example.com",
				AuthFileSnapshot:     account.AuthFileSnapshot,
				AuthProviderSnapshot: account.AuthProviderSnapshot,
				AuthIndex:            account.AuthIndex,
			},
			{
				RequestKey: "previous", RowKey: "row-early-reset", ProviderWindowID: "weekly", Period: "previous",
				FromMS: oldRequestAtMS - 1_000, ToMS: firstRequestAtMS,
				ModelScope:           monitoringsvc.AccountWindowModelScope{Kind: "all"},
				AccountSnapshot:      "early-reset@example.com",
				AuthFileSnapshot:     account.AuthFileSnapshot,
				AuthProviderSnapshot: account.AuthProviderSnapshot,
				AuthIndex:            account.AuthIndex,
			},
		},
	})
	if err != nil {
		t.Fatalf("query early-reset window usage: %v", err)
	}
	if len(usageResult.Items) != 2 {
		t.Fatalf("early-reset window usage = %#v", usageResult)
	}
	current, previous := usageResult.Items[0], usageResult.Items[1]
	if !current.Matched || current.TotalRequests != 1 || current.TotalTokens != 2_521 {
		t.Fatalf("current early-reset usage = %#v", current)
	}
	if !previous.Matched || previous.TotalRequests != 1 || previous.TotalTokens != 110 {
		t.Fatalf("previous early-reset usage = %#v", previous)
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
		manager.snapshotResolver.snapshots != nil ||
		manager.snapshotResolver.ambiguous != nil {
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

// scriptedCPA is a CPA stand-in on one TCP address that answers both protocols the
// Collector speaks: a RESP command gets respReply as an error frame, an HTTP
// request gets an empty usage queue. Which one a run settled on is therefore
// visible from the outside, which is what tells a fallback apart from a stop.
type scriptedCPA struct {
	listener  net.Listener
	respReply string
	// okKey is the one management credential this CPA accepts. When it is empty
	// every credential gets respReply.
	okKey string
	// authGate delays the reply of the nth connection index, so a test can land a
	// refusal after the run that sent it was superseded.
	authGate chan struct{}
	gateConn int
	accepted atomic.Int32
}

func newScriptedCPA(t *testing.T, respReply string) *scriptedCPA {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &scriptedCPA{listener: listener, respReply: respReply, gateConn: -1}
	t.Cleanup(func() { _ = listener.Close() })
	go s.serve()
	return s
}

func (s *scriptedCPA) URL() string { return "http://" + s.listener.Addr().String() }

// gate delays the reply of the connection with the given zero-based index.
func (s *scriptedCPA) gate(index int) chan struct{} {
	s.gateConn = index
	s.authGate = make(chan struct{})
	return s.authGate
}

func (s *scriptedCPA) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		index := int(s.accepted.Add(1)) - 1
		go s.handle(conn, index)
	}
}

func (s *scriptedCPA) handle(conn net.Conn, index int) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		first, err := reader.Peek(1)
		if err != nil {
			return
		}
		if first[0] != '*' {
			if err := s.answerHTTP(reader, conn); err != nil {
				return
			}
			continue
		}
		command, err := readRESPCommand(reader)
		if err != nil {
			return
		}
		fields := strings.Fields(command)
		if len(fields) > 0 && strings.EqualFold(fields[0], "AUTH") {
			if index == s.gateConn && s.authGate != nil {
				<-s.authGate
			}
			if s.okKey != "" && len(fields) == 2 && fields[1] == s.okKey {
				_, _ = io.WriteString(conn, "+OK\r\n")
				continue
			}
			_, _ = io.WriteString(conn, "-"+s.respReply+"\r\n")
			continue
		}
		// An authenticated command answers with an empty pop result.
		_, _ = io.WriteString(conn, "*0\r\n")
	}
}

// answerHTTP serves one HTTP request with an empty usage queue and reports whether
// the connection is still usable.
func (s *scriptedCPA) answerHTTP(reader *bufio.Reader, conn net.Conn) error {
	requestLine, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}
	if !strings.Contains(requestLine, "/v0/management/usage-queue") {
		_, err = io.WriteString(conn, "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n")
		return err
	}
	body := "[]"
	_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"+body)
	return err
}

// readRESPCommand consumes one RESP array command and returns it joined by spaces.
func readRESPCommand(reader *bufio.Reader) (string, error) {
	header, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	header = strings.TrimSpace(header)
	if header == "" || header[0] != '*' {
		return "", fmt.Errorf("unexpected RESP header %q", header)
	}
	count, err := strconv.Atoi(header[1:])
	if err != nil {
		return "", err
	}
	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		lengthLine, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		lengthLine = strings.TrimSpace(lengthLine)
		if lengthLine == "" || lengthLine[0] != '$' {
			return "", fmt.Errorf("unexpected RESP bulk header %q", lengthLine)
		}
		length, err := strconv.Atoi(lengthLine[1:])
		if err != nil {
			return "", err
		}
		buffer := make([]byte, length+2)
		if _, err := io.ReadFull(reader, buffer); err != nil {
			return "", err
		}
		args = append(args, string(buffer[:length]))
	}
	return strings.Join(args, " "), nil
}

// TestManagerFallsBackToHTTPWhenCPAReportsHomeMode pins CPA's home-mode reply as
// something other than a refused credential. CPA answers every RESP connection
// with it before any command, so auto mode must keep falling back to the HTTP
// queue: a refused-credential stop here would end collection on a CPA that has
// simply disabled the RESP output, and report a key rejection that never happened.
func TestManagerFallsBackToHTTPWhenCPAReportsHomeMode(t *testing.T) {
	upstream := newScriptedCPA(t, "ERR redis usage output disabled in home mode")
	db := newTestStore(t)
	manager := NewManager(testConfig(t, "auto"), db)
	var rejections atomic.Int32
	manager.SetAuthRejectionHandler(func() { rejections.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream.URL(), ManagementKey: "management-key"})
	waitFor(t, func() bool {
		status := manager.Status()
		return status.Transport == "http" && status.Collector == "running"
	})
	if rejections.Load() != 0 {
		t.Fatalf("a home-mode reply was reported as a refused credential %d times", rejections.Load())
	}
	if status := manager.Status(); strings.Contains(status.LastError, "rejected") {
		t.Fatalf("last error = %q", status.LastError)
	}
}

// TestManagerStopsOnEachCPAAuthReply proves the stop is driven by the exact
// messages CPA writes when it refuses a management credential, and by nothing
// else. Each of these is terminal: retrying is one more failed authentication
// against a CPA that bans the address after a handful of them.
func TestManagerStopsOnEachCPAAuthReply(t *testing.T) {
	for _, message := range []string{
		"NOAUTH Authentication required.",
		"ERR invalid management key",
		"ERR missing management key",
		"ERR remote management key not set",
		"ERR remote management disabled",
		"ERR IP banned due to too many failed attempts. Try again in 29m59s",
	} {
		t.Run(message, func(t *testing.T) {
			upstream := newScriptedCPA(t, message)
			manager := NewManager(testConfig(t, "resp"), newTestStore(t))
			var rejections atomic.Int32
			manager.SetAuthRejectionHandler(func() { rejections.Add(1) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream.URL(), ManagementKey: "management-key"})
			waitFor(t, func() bool { return rejections.Load() == 1 })
			waitFor(t, func() bool { return manager.Status().Collector == "stopped" })
			if attempts := upstream.accepted.Load(); attempts > 2 {
				t.Fatalf("a refused credential was retried %d times", attempts)
			}
		})
	}
}

// TestManagerKeepsRetryingOtherRESPFrames is the other half of the rule, and the
// legacy Collector's own behaviour: an error frame that is not about the
// credential stays retryable. CPA's home-mode greeting is the frame that used to
// stop collection for good.
func TestManagerKeepsRetryingOtherRESPFrames(t *testing.T) {
	upstream := newScriptedCPA(t, "ERR redis usage output disabled in home mode")
	manager := NewManager(testConfig(t, "resp"), newTestStore(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream.URL(), ManagementKey: "management-key"})
	waitFor(t, func() bool {
		return strings.Contains(manager.Status().LastError, "redis usage output disabled in home mode")
	})
	waitFor(t, func() bool { return upstream.accepted.Load() >= 2 })
	if status := manager.Status(); status.Collector != "error" {
		t.Fatalf("collector = %q, want a retrying error state", status.Collector)
	}
}

// TestRefuseAuthKeepsLastError and its run scoping: a stop is not evidence that an
// earlier failure never happened, and a report belongs to the run that met the
// refusal.
func TestRefuseAuthKeepsLastError(t *testing.T) {
	manager := NewManager(testConfig(t, "http"), newTestStore(t))
	manager.setStatus(func(status *Status) { status.LastError = "consume: connection reset" })
	rejections := 0
	manager.SetAuthRejectionHandler(func() { rejections++ })

	ctx, cancel := context.WithCancel(context.Background())
	manager.refuseAuth(ctx, cancel, &resp.ErrorReply{Message: "ERR invalid management key"})
	// The earlier cause survives, and the refusal is added to it: a stop is not
	// evidence that the earlier failure never happened.
	if last := manager.Status().LastError; !strings.HasPrefix(last, "consume: connection reset; management credential refused: ERR invalid management key") {
		t.Fatalf("last error = %q, want the earlier cause kept and the refusal added", last)
	}
	if ctx.Err() == nil {
		t.Fatal("the refused run was not cancelled")
	}
	if rejections != 1 {
		t.Fatalf("rejections = %d, want one report", rejections)
	}
}

// TestRefuseAuthIgnoresASupersededRun covers the restart race: a refusal that
// lands after the run it belongs to was cancelled says nothing about the Collector
// running now, so it neither stops it nor reports a blocker for its credential.
func TestManagerIgnoresASupersededRunsLateRefusal(t *testing.T) {
	upstream := newScriptedCPA(t, "ERR invalid management key")
	upstream.okKey = "new-key"
	gate := upstream.gate(0)
	manager := NewManager(testConfig(t, "resp"), newTestStore(t))
	var rejections atomic.Int32
	manager.SetAuthRejectionHandler(func() { rejections.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The first run is held in AUTH, so the restart below supersedes it while its
	// reply is still in flight.
	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream.URL(), ManagementKey: "old-key"})
	waitFor(t, func() bool { return upstream.accepted.Load() == 1 })
	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream.URL(), ManagementKey: "new-key"})
	waitFor(t, func() bool { return upstream.accepted.Load() >= 2 })

	// The superseded run's reply lands only now.
	close(gate)
	time.Sleep(100 * time.Millisecond)
	if rejections.Load() != 0 {
		t.Fatalf("a superseded run's refusal reported %d times", rejections.Load())
	}
	// The new run is the one that owns the Collector, and it is still collecting.
	waitFor(t, func() bool {
		status := manager.Status()
		return status.Transport == "resp" && status.Collector == "running"
	})
}

// TestManagerRecordsTheRefusalItStoppedOn proves a refused credential is not a
// silent stop. The legacy Collector installs no rejection handler, so its status
// page is the only place an operator can learn why collection ended; a stop with
// an empty LastError leaves them with a stopped Collector and no cause.
func TestManagerRecordsTheRefusalItStoppedOn(t *testing.T) {
	upstream := newScriptedCPA(t, "ERR invalid management key")
	manager := NewManager(testConfig(t, "resp"), newTestStore(t))
	manager.SetAuthRejectionHandler(func() {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream.URL(), ManagementKey: "management-key"})
	waitFor(t, func() bool { return manager.Status().Collector == "stopped" })
	if last := manager.Status().LastError; !strings.Contains(last, "ERR invalid management key") {
		t.Fatalf("last error = %q, want the CPA's own refusal", last)
	}
}

// TestManagerKeepsAnEarlierErrorWhenTheCredentialIsRefused proves the refusal is
// added to the record rather than used to erase it: an operator needs both the
// failure that came first and the refusal that ended the run.
func TestManagerKeepsAnEarlierErrorWhenTheCredentialIsRefused(t *testing.T) {
	manager := NewManager(testConfig(t, "http"), newTestStore(t))
	manager.setStatus(func(status *Status) { status.LastError = "consume: connection reset" })
	ctx, cancel := context.WithCancel(context.Background())

	manager.refuseAuth(ctx, cancel, &resp.ErrorReply{Message: "NOAUTH Authentication required."})
	last := manager.Status().LastError
	if !strings.HasPrefix(last, "consume: connection reset; ") || !strings.Contains(last, "NOAUTH") {
		t.Fatalf("last error = %q, want both causes", last)
	}
}

func TestManagerStopAndWaitDrainsSupersededAndAuthRejectionRuns(t *testing.T) {
	upstream1 := newScriptedCPA(t, "ERR invalid management key")
	gate1 := upstream1.gate(0)
	upstream2 := newScriptedCPA(t, "ERR invalid management key")

	manager := NewManager(testConfig(t, "resp"), newTestStore(t))
	rejectionEntered := make(chan struct{})
	releaseRejection := make(chan struct{})
	var enterOnce sync.Once
	manager.SetAuthRejectionHandler(func() {
		enterOnce.Do(func() { close(rejectionEntered) })
		<-releaseRejection
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream1.URL(), ManagementKey: "old-key"})
	waitFor(t, func() bool { return upstream1.accepted.Load() == 1 })

	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream2.URL(), ManagementKey: "bad-key"})
	<-rejectionEntered

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- manager.StopAndWait(context.Background())
	}()
	select {
	case err := <-stopDone:
		t.Fatalf("StopAndWait returned %v while superseded run and rejection callback were active", err)
	default:
	}

	close(gate1)
	select {
	case err := <-stopDone:
		t.Fatalf("StopAndWait returned %v while auth-rejection callback was still running", err)
	default:
	}

	close(releaseRejection)
	if err := <-stopDone; err != nil {
		t.Fatalf("StopAndWait: %v", err)
	}
}

func TestManagerRunningHandlerRequiresSuccessfulHTTPPollAndFiresAcrossRestarts(t *testing.T) {
	var failQueue atomic.Bool
	failQueue.Store(true)
	var queueCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/usage-queue" {
			http.NotFound(w, r)
			return
		}
		queueCalls.Add(1)
		if failQueue.Load() {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(upstream.Close)

	manager := NewManager(testConfig(t, "http"), newTestStore(t))
	var runningCalls atomic.Int32
	var seenStatusRunningDuringCallback atomic.Bool
	manager.SetRunningRunHandler(func(ctx context.Context, _ uint64, cfg RuntimeConfig) bool {
		if ctx == nil || cfg.CPAUpstreamURL != upstream.URL {
			return false
		}
		if manager.Status().Collector == "running" {
			seenStatusRunningDuringCallback.Store(true)
		}
		runningCalls.Add(1)
		return true
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"})
	waitFor(t, func() bool { return queueCalls.Load() >= 1 && manager.Status().Collector == "error" })
	if got := runningCalls.Load(); got != 0 {
		t.Fatalf("running callback fired during failed HTTP polls: got %d, want 0", got)
	}

	failQueue.Store(false)
	waitFor(t, func() bool { return manager.Status().Collector == "running" })
	if got := runningCalls.Load(); got != 1 {
		t.Fatalf("running callback count after recovery = %d, want 1", got)
	}
	if seenStatusRunningDuringCallback.Load() {
		t.Fatal("status transitioned to running before running callback finished")
	}

	callsAtReady := queueCalls.Load()
	waitFor(t, func() bool { return queueCalls.Load() >= callsAtReady+2 })
	if got := runningCalls.Load(); got != 1 {
		t.Fatalf("running callback fired redundantly across steady polls: got %d, want 1", got)
	}

	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"})
	waitFor(t, func() bool { return runningCalls.Load() == 2 && manager.Status().Collector == "running" })
	if err := manager.StopAndWait(context.Background()); err != nil {
		t.Fatalf("StopAndWait: %v", err)
	}
}

func TestManagerRunningHandlerStaysSilentOnAuthRejection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/usage-queue" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	manager := NewManager(testConfig(t, "http"), newTestStore(t))
	var runningCalls atomic.Int32
	manager.SetRunningHandler(func(context.Context, RuntimeConfig) {
		runningCalls.Add(1)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream.URL, ManagementKey: "bad-key"})
	waitFor(t, func() bool { return manager.Status().Collector == "stopped" })
	if err := manager.StopAndWait(context.Background()); err != nil {
		t.Fatalf("StopAndWait: %v", err)
	}
	if got := runningCalls.Load(); got != 0 {
		t.Fatalf("running callback fired on auth rejection: got %d, want 0", got)
	}
}

func TestManagerRunningHandlerRetriesBoundedPollsUntilProvenAndFencesStaleRun(t *testing.T) {
	var queueCalls atomic.Int32
	var nonEmptyBurst atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/usage-queue" {
			http.NotFound(w, r)
			return
		}
		queueCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if nonEmptyBurst.Add(-1) >= 0 {
			_, _ = w.Write([]byte(`["{\"request_id\":\"burst\",\"timestamp\":\"1700000000000\",\"model\":\"gpt-4\",\"api_key\":\"k1\",\"total_tokens\":1}"]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(upstream.Close)

	baseCfg := testConfig(t, "http")
	baseCfg.PollInterval = 40 * time.Millisecond
	manager := NewManager(baseCfg, newTestStore(t))

	var (
		allowAck       atomic.Bool
		run1Calls      atomic.Int32
		run2Calls      atomic.Int32
		holdRun1       atomic.Bool
		run1Entered    = make(chan struct{})
		run1EnteredOne sync.Once
		releaseRun1    = make(chan struct{})
		staleObserved  atomic.Bool
	)
	nonEmptyBurst.Store(5)
	manager.SetRunningRunHandler(func(ctx context.Context, runID uint64, cfg RuntimeConfig) bool {
		// Status() and ActiveRunConfig() must never deadlock inside callback.
		_ = manager.Status()
		if runID == 1 {
			run1Calls.Add(1)
			if holdRun1.Load() {
				run1EnteredOne.Do(func() { close(run1Entered) })
				<-releaseRun1
				_, active := manager.ActiveRunConfig(runID)
				if ctx.Err() != nil && !active {
					staleObserved.Store(true)
				}
				return true
			}
			return allowAck.Load() && cfg.CPAUpstreamURL == upstream.URL
		}
		if runID == 2 {
			run2Calls.Add(1)
			return true
		}
		return false
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"})
	// Non-empty burst drains back-to-back, but unacknowledged proof retries stay bounded by PollInterval
	// while Collector itself reaches "running" and keeps retrying across subsequent healthy ticks.
	waitFor(t, func() bool { return manager.Status().Collector == "running" && queueCalls.Load() >= 6 })
	waitFor(t, func() bool { return run1Calls.Load() >= 2 })

	allowAck.Store(true)
	waitFor(t, func() bool { return !manager.needsRunningProof(1) })
	ackedCalls := run1Calls.Load()
	pollsAtAck := queueCalls.Load()
	waitFor(t, func() bool { return queueCalls.Load() >= pollsAtAck+2 })
	if got := run1Calls.Load(); got != ackedCalls {
		t.Fatalf("running callback fired after acknowledgement: got %d, want %d", got, ackedCalls)
	}

	// Reset acknowledgement and hold run 1 inside callback while superseding with run 2.
	allowAck.Store(false)
	holdRun1.Store(true)
	manager.SetRunningRunHandler(func(ctx context.Context, runID uint64, cfg RuntimeConfig) bool {
		_ = manager.Status()
		if runID == 1 {
			run1EnteredOne.Do(func() { close(run1Entered) })
			<-releaseRun1
			_, active := manager.ActiveRunConfig(runID)
			if ctx.Err() != nil && !active {
				staleObserved.Store(true)
			}
			return true
		}
		if runID == 2 {
			if _, active := manager.ActiveRunConfig(runID); !active || cfg.ManagementKey != "management-key-2" {
				return false
			}
			run2Calls.Add(1)
			return true
		}
		return false
	})
	<-run1Entered
	manager.Start(ctx, RuntimeConfig{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key-2"})
	close(releaseRun1)

	waitFor(t, func() bool { return run2Calls.Load() == 1 && !manager.needsRunningProof(2) })
	if !staleObserved.Load() {
		t.Fatal("superseded run 1 callback did not observe cancelled context and inactive runID")
	}
	if err := manager.StopAndWait(context.Background()); err != nil {
		t.Fatalf("StopAndWait: %v", err)
	}
}

func TestCollectorDoesNotPopUnderTrialHoldAndQuiescesInflightBatch(t *testing.T) {
	t.Cleanup(func() {
		middleware.SetProcessTrialHoldGate(nil)
		middleware.ReleaseProcessTrialHold()
	})

	t.Run("HTTP queue skips Pop while trial hold is active and resumes on release", func(t *testing.T) {
		middleware.EngageProcessTrialHold()
		defer middleware.ReleaseProcessTrialHold()

		var popHits atomic.Int32
		var delivered atomic.Bool
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v0/management/usage-queue" {
				popHits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if delivered.CompareAndSwap(false, true) {
					_, _ = io.WriteString(w, `[{"request_id":"trial-hold-http-1","timestamp":"2026-11-26T00:00:01Z","provider":"codex","model":"gpt-test","endpoint":"POST /v1/chat/completions","input_tokens":3,"output_tokens":4}]`)
					return
				}
				_, _ = io.WriteString(w, `[]`)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(upstream.Close)

		st := newTestStore(t)
		cfg := testConfig(t, "http")
		cfg.PollInterval = 20 * time.Millisecond
		manager := NewManager(cfg, st)
		ctx, cancel := context.WithCancel(context.Background())
		manager.Start(ctx, RuntimeConfig{
			CPAUpstreamURL: upstream.URL,
			ManagementKey:  "management-key",
			CollectorMode:  "http",
			PollInterval:   20 * time.Millisecond,
		})
		t.Cleanup(func() {
			cancel()
			_ = manager.StopAndWait(context.Background())
		})
		time.Sleep(250 * time.Millisecond)
		if got := popHits.Load(); got != 0 {
			t.Fatalf("expected 0 HTTP queue Pop calls under TrialHoldGate, got %d", got)
		}

		middleware.ReleaseProcessTrialHold()
		waitFor(t, func() bool {
			return popHits.Load() >= 1 && manager.Status().TotalInserted == 1
		})
	})

	t.Run("RESP queue skips Pop while trial hold is active and resumes on release", func(t *testing.T) {
		middleware.EngageProcessTrialHold()
		defer middleware.ReleaseProcessTrialHold()

		var respPopHits atomic.Int32
		var delivered atomic.Bool
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			for {
				conn, acceptErr := ln.Accept()
				if acceptErr != nil {
					return
				}
				go func(c net.Conn) {
					defer c.Close()
					reader := bufio.NewReader(c)
					for {
						cmd, cmdErr := readRESPCommand(reader)
						if cmdErr != nil {
							return
						}
						upper := strings.ToUpper(cmd)
						switch {
						case strings.HasPrefix(upper, "AUTH "):
							_, _ = io.WriteString(c, "+OK\r\n")
						case strings.HasPrefix(upper, "LPOP ") || strings.HasPrefix(upper, "RPOP "):
							respPopHits.Add(1)
							if delivered.CompareAndSwap(false, true) {
								payload := `{"request_id":"trial-hold-resp-1","timestamp":"2026-11-26T00:00:02Z","provider":"codex","model":"gpt-test","endpoint":"POST /v1/chat/completions","input_tokens":5,"output_tokens":6}`
								_, _ = fmt.Fprintf(c, "*1\r\n$%d\r\n%s\r\n", len(payload), payload)
							} else {
								_, _ = io.WriteString(c, "$-1\r\n")
							}
						default:
							_, _ = io.WriteString(c, "+OK\r\n")
						}
					}
				}(conn)
			}
		}()

		st := newTestStore(t)
		cfg := testConfig(t, "resp")
		cfg.PollInterval = 20 * time.Millisecond
		manager := NewManager(cfg, st)
		ctx, cancel := context.WithCancel(context.Background())
		manager.Start(ctx, RuntimeConfig{
			CPAUpstreamURL: "http://" + ln.Addr().String(),
			ManagementKey:  "management-key",
			CollectorMode:  "resp",
			PollInterval:   20 * time.Millisecond,
		})
		t.Cleanup(func() {
			cancel()
			_ = manager.StopAndWait(context.Background())
		})
		time.Sleep(250 * time.Millisecond)
		if got := respPopHits.Load(); got != 0 {
			t.Fatalf("expected 0 RESP Pop calls under TrialHoldGate, got %d", got)
		}

		middleware.ReleaseProcessTrialHold()
		waitFor(t, func() bool {
			return respPopHits.Load() >= 1 && manager.Status().TotalInserted == 1
		})
	})

	t.Run("QuiesceForTrialHold waits for in-flight batch and fences subsequent pops", func(t *testing.T) {
		middleware.ReleaseProcessTrialHold()
		defer middleware.ReleaseProcessTrialHold()

		var popHits atomic.Int32
		var batchDelivered atomic.Bool
		enteredBatch := make(chan struct{})
		var enteredOnce sync.Once
		allowBatchFinish := make(chan struct{})
		var batchCompleted atomic.Bool

		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v0/management/usage-queue" {
				popHits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if batchDelivered.CompareAndSwap(false, true) {
					_, _ = io.WriteString(w, `[{"request_id":"quiesce-inflight-1","timestamp":"2026-11-26T00:00:03Z","provider":"codex","model":"gpt-test","endpoint":"POST /v1/chat/completions","input_tokens":1,"output_tokens":2}]`)
					return
				}
				_, _ = io.WriteString(w, `[]`)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(upstream.Close)

		st := newTestStore(t)
		cfg := testConfig(t, "http")
		cfg.PollInterval = 15 * time.Millisecond
		manager := NewManager(cfg, st)
		manager.SetUsageEventHandler(UsageEventHandlerFunc(func(_ context.Context, _ RuntimeConfig, _ []usage.Event) {
			enteredOnce.Do(func() { close(enteredBatch) })
			<-allowBatchFinish
			batchCompleted.Store(true)
		}))

		ctx, cancel := context.WithCancel(context.Background())
		manager.Start(ctx, RuntimeConfig{
			CPAUpstreamURL: upstream.URL,
			ManagementKey:  "management-key",
			CollectorMode:  "http",
			PollInterval:   15 * time.Millisecond,
		})
		t.Cleanup(func() {
			cancel()
			_ = manager.StopAndWait(context.Background())
		})
		<-enteredBatch
		middleware.EngageProcessTrialHold()

		quiesceDone := make(chan error, 1)
		go func() {
			quiesceDone <- manager.QuiesceForTrialHold(context.Background())
		}()
		select {
		case err := <-quiesceDone:
			t.Fatalf("QuiesceForTrialHold returned before in-flight batch finished: %v", err)
		case <-time.After(30 * time.Millisecond):
		}

		close(allowBatchFinish)
		if err := <-quiesceDone; err != nil {
			t.Fatalf("QuiesceForTrialHold failed: %v", err)
		}
		if !batchCompleted.Load() {
			t.Fatal("QuiesceForTrialHold returned before UsageEventHandler completed")
		}
		popsAfterQuiesce := popHits.Load()
		time.Sleep(150 * time.Millisecond)
		if got := popHits.Load(); got != popsAfterQuiesce {
			t.Fatalf("expected zero Pop calls after QuiesceForTrialHold, got %d -> %d", popsAfterQuiesce, got)
		}
	})
}

func TestObservationCallbacksAcrossSubscribeAndRetainedQueueOutages(t *testing.T) {
	t.Run("subscribe notifies on subscribe ready, reports disconnect gap, and re-observes on reconnect", func(t *testing.T) {
		var connCount atomic.Int32
		dropFirst := make(chan struct{})
		var dropOnce sync.Once

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })

		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				id := connCount.Add(1)
				go func(conn net.Conn, connID int32) {
					defer func() { _ = conn.Close() }()
					reader := bufio.NewReader(conn)
					for {
						line, err := reader.ReadString('\n')
						if err != nil {
							return
						}
						switch {
						case strings.HasPrefix(line, "*"):
							// Read array length args loosely for AUTH / SUBSCRIBE / PING.
							n := 0
							_, _ = fmt.Sscanf(line, "*%d", &n)
							parts := make([]string, 0, n)
							for range n {
								_, _ = reader.ReadString('\n') // $len
								arg, _ := reader.ReadString('\n')
								parts = append(parts, strings.TrimSpace(arg))
							}
							if len(parts) == 0 {
								continue
							}
							switch strings.ToUpper(parts[0]) {
							case "AUTH":
								_, _ = io.WriteString(conn, "+OK\r\n")
							case "SUBSCRIBE":
								ch := ""
								if len(parts) > 1 {
									ch = parts[1]
								}
								_, _ = fmt.Fprintf(conn, "*3\r\n$9\r\nsubscribe\r\n$%d\r\n%s\r\n:1\r\n", len(ch), ch)
								if connID == 1 {
									go func() {
										<-dropFirst
										_ = conn.Close()
									}()
								}
							case "PING":
								_, _ = io.WriteString(conn, "+PONG\r\n")
							}
						}
					}
				}(c, id)
			}
		}()

		st := newTestStore(t)
		cfg := testConfig(t, "subscribe")
		cfg.PollInterval = 20 * time.Millisecond
		manager := NewManager(cfg, st)

		var clockMS atomic.Int64
		clockMS.Store(1_760_000_000_000)
		manager.SetNowFunc(clockMS.Load)

		var mu sync.Mutex
		var observed []ObservationEvent
		var disconnects []string
		manager.SetObservationCallbacks(
			func(_ context.Context, ev ObservationEvent) error {
				mu.Lock()
				observed = append(observed, ev)
				mu.Unlock()
				return nil
			},
			func(_ context.Context, _ uint64, transport string) {
				mu.Lock()
				disconnects = append(disconnects, transport)
				mu.Unlock()
			},
		)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		manager.Start(ctx, RuntimeConfig{
			CPAUpstreamURL: "http://" + ln.Addr().String(),
			ManagementKey:  "management-key",
			CollectorMode:  "subscribe",
			Queue:          "usage:queue",
			PollInterval:   20 * time.Millisecond,
		})
		t.Cleanup(func() { _ = manager.StopAndWait(context.Background()) })

		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(observed) == 1
		})
		mu.Lock()
		first := observed[0]
		mu.Unlock()
		if first.Transport != "subscribe" || first.ObservedAtMS != 1_760_000_000_000 || first.RetainedQueue || !first.InitialRunObserve || first.ForceGapAdvance {
			t.Fatalf("first subscribe observation = %+v, want subscribe/1760000000000/false/initial/no-gap", first)
		}

		clockMS.Store(1_760_000_005_000)
		dropOnce.Do(func() { close(dropFirst) })

		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(disconnects) >= 1 && len(observed) >= 2
		})
		mu.Lock()
		disc := disconnects[0]
		second := observed[1]
		mu.Unlock()
		if disc != "subscribe" {
			t.Fatalf("disconnect transport = %q, want subscribe", disc)
		}
		if second.Transport != "subscribe" || second.ObservedAtMS != 1_760_000_005_000 || second.RetainedQueue || !second.InitialRunObserve || !second.ForceGapAdvance {
			t.Fatalf("reconnect subscribe observation = %+v, want subscribe/1760000005000/false/initial/force-gap", second)
		}
	})

	t.Run("http short pre-pop error keeps continuity while outage exceeding budget or uncertain 500 emits disconnect and gap", func(t *testing.T) {
		var failDial atomic.Bool
		var fail500 atomic.Bool
		errServed := make(chan struct{}, 4)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v0/management/usage-queue" {
				http.NotFound(w, r)
				return
			}
			if fail500.CompareAndSwap(true, false) {
				http.Error(w, "post-pop server failure", http.StatusInternalServerError)
				select {
				case errServed <- struct{}{}:
				default:
				}
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[]`)
		}))
		t.Cleanup(upstream.Close)

		st := newTestStore(t)
		cfg := testConfig(t, "http")
		cfg.PollInterval = 15 * time.Millisecond
		manager := NewManager(cfg, st)
		baseTransport := http.DefaultTransport.(*http.Transport).Clone()
		t.Cleanup(baseTransport.CloseIdleConnections)
		manager.SetHTTPClient(&http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.HasSuffix(req.URL.Path, "/usage-queue") && failDial.CompareAndSwap(true, false) {
					select {
					case errServed <- struct{}{}:
					default:
					}
					return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
				}
				return baseTransport.RoundTrip(req)
			}),
		})

		var clockMS atomic.Int64
		clockMS.Store(1_760_000_000_000)
		manager.SetNowFunc(clockMS.Load)

		var mu sync.Mutex
		var observed []ObservationEvent
		var disconnects []string
		manager.SetObservationCallbacks(
			func(_ context.Context, ev ObservationEvent) error {
				mu.Lock()
				observed = append(observed, ev)
				mu.Unlock()
				return nil
			},
			func(_ context.Context, _ uint64, transport string) {
				mu.Lock()
				disconnects = append(disconnects, transport)
				mu.Unlock()
			},
		)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		manager.Start(ctx, RuntimeConfig{
			CPAUpstreamURL:          upstream.URL,
			ManagementKey:           "management-key",
			CollectorMode:           "http",
			PollInterval:            15 * time.Millisecond,
			RetentionBudgetMS:       7_500,
			RetentionBudgetVerified: true,
		})
		t.Cleanup(func() { _ = manager.StopAndWait(context.Background()) })

		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(observed) == 1
		})

		// Short 5s pre-pop dial outage -> within 7.5s budget (NO gap).
		clockMS.Store(1_760_000_005_000)
		failDial.Store(true)
		<-errServed
		waitFor(t, func() bool { return manager.Status().Collector == "error" })
		waitFor(t, func() bool { return manager.Status().Collector == "running" })
		mu.Lock()
		for _, ev := range observed {
			if ev.ForceGapAdvance {
				t.Fatalf("short 5s dial outage wrongly marked ForceGapAdvance: %+v", ev)
			}
		}
		if len(disconnects) != 0 {
			t.Fatalf("short 5s dial outage wrongly emitted gap: disconnects=%v observed=%+v", disconnects, observed)
		}
		mu.Unlock()

		// 9s pre-pop dial outage (> 7.5s verified budget) -> emits unreplayed disconnect and ForceGapAdvance.
		clockMS.Store(1_760_000_014_001)
		failDial.Store(true)
		<-errServed
		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(disconnects) == 1 && len(observed) > 0 && observed[len(observed)-1].ForceGapAdvance
		})
		mu.Lock()
		disc := disconnects[0]
		reobs := observed[len(observed)-1]
		mu.Unlock()
		if disc != "http" || reobs.Transport != "http" || reobs.ObservedAtMS != 1_760_000_014_001 || !reobs.ForceGapAdvance {
			t.Fatalf("outage >7.5s callbacks = disc:%q obs:%+v", disc, reobs)
		}

		// 1ms HTTP 500 error -> uncertain post-pop removal triggers disconnect & ForceGapAdvance immediately.
		clockMS.Store(1_760_000_014_002)
		fail500.Store(true)
		<-errServed
		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(disconnects) >= 2 && len(observed) > 0 && observed[len(observed)-1].ObservedAtMS == 1_760_000_014_002
		})
		mu.Lock()
		lastObs := observed[len(observed)-1]
		mu.Unlock()
		if !lastObs.ForceGapAdvance {
			t.Fatalf("500 error within 1ms did not mark ForceGapAdvance: %+v", lastObs)
		}
	})

	t.Run("resp outage exceeding 45s emits disconnect and non-retained re-observe", func(t *testing.T) {
		var failNextPop atomic.Bool
		errServed := make(chan struct{}, 4)

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })

		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func(conn net.Conn) {
					defer func() { _ = conn.Close() }()
					reader := bufio.NewReader(conn)
					for {
						line, err := reader.ReadString('\n')
						if err != nil {
							return
						}
						if !strings.HasPrefix(line, "*") {
							continue
						}
						n := 0
						_, _ = fmt.Sscanf(line, "*%d", &n)
						parts := make([]string, 0, n)
						for range n {
							_, _ = reader.ReadString('\n')
							arg, _ := reader.ReadString('\n')
							parts = append(parts, strings.TrimSpace(arg))
						}
						if len(parts) == 0 {
							continue
						}
						switch strings.ToUpper(parts[0]) {
						case "AUTH":
							_, _ = io.WriteString(conn, "+OK\r\n")
						case "RPOP", "LPOP":
							if failNextPop.CompareAndSwap(true, false) {
								_, _ = io.WriteString(conn, "-ERR temporary redis failure\r\n")
								select {
								case errServed <- struct{}{}:
								default:
								}
								return
							}
							_, _ = io.WriteString(conn, "*-1\r\n")
						}
					}
				}(c)
			}
		}()

		st := newTestStore(t)
		cfg := testConfig(t, "resp")
		cfg.PollInterval = 15 * time.Millisecond
		manager := NewManager(cfg, st)

		var clockMS atomic.Int64
		clockMS.Store(1_760_000_000_000)
		manager.SetNowFunc(clockMS.Load)

		var mu sync.Mutex
		var observed []ObservationEvent
		var disconnects []string
		manager.SetObservationCallbacks(
			func(_ context.Context, ev ObservationEvent) error {
				mu.Lock()
				observed = append(observed, ev)
				mu.Unlock()
				return nil
			},
			func(_ context.Context, _ uint64, transport string) {
				mu.Lock()
				disconnects = append(disconnects, transport)
				mu.Unlock()
			},
		)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		manager.Start(ctx, RuntimeConfig{
			CPAUpstreamURL:          "http://" + ln.Addr().String(),
			ManagementKey:           "management-key",
			CollectorMode:           "resp",
			Queue:                   "usage:queue",
			PopSide:                 "right",
			PollInterval:            15 * time.Millisecond,
			RetentionBudgetMS:       45_000,
			RetentionBudgetVerified: true,
		})

		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(observed) == 1
		})

		clockMS.Store(1_760_000_050_001)
		failNextPop.Store(true)
		<-errServed
		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(disconnects) == 1 && len(observed) > 0 && observed[len(observed)-1].ForceGapAdvance
		})
		mu.Lock()
		disc := disconnects[0]
		reobs := observed[len(observed)-1]
		mu.Unlock()
		if disc != "resp" || reobs.Transport != "resp" || reobs.ObservedAtMS != 1_760_000_050_001 || !reobs.ForceGapAdvance {
			t.Fatalf("resp outage >45s callbacks = disc:%q obs:%+v", disc, reobs)
		}
	})

	t.Run("post-pop DB ingest failure emits unreplayed disconnect and ForceGapAdvance on next poll", func(t *testing.T) {
		var poppedOnce atomic.Bool
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v0/management/usage-queue" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if poppedOnce.CompareAndSwap(false, true) {
				_, _ = io.WriteString(w, `[{"timestamp":"2026-11-27T00:00:00Z","model":"m","endpoint":"POST /v1/chat/completions","api_key":"k","input_tokens":1,"output_tokens":1,"total_tokens":2}]`)
				return
			}
			_, _ = io.WriteString(w, `[]`)
		}))
		t.Cleanup(upstream.Close)

		st := newTestStore(t)
		cfg := testConfig(t, "http")
		cfg.PollInterval = 15 * time.Millisecond
		manager := NewManager(cfg, st)

		var clockMS atomic.Int64
		clockMS.Store(1_760_000_000_000)
		manager.SetNowFunc(clockMS.Load)

		var mu sync.Mutex
		var observed []ObservationEvent
		var disconnects []string
		manager.SetObservationCallbacks(
			func(_ context.Context, ev ObservationEvent) error {
				mu.Lock()
				observed = append(observed, ev)
				mu.Unlock()
				return nil
			},
			func(_ context.Context, _ uint64, transport string) {
				mu.Lock()
				disconnects = append(disconnects, transport)
				mu.Unlock()
			},
		)

		// Close underlying SQLite store so InsertEvents fails AFTER popping items from CPA.
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		manager.Start(ctx, RuntimeConfig{
			CPAUpstreamURL: upstream.URL,
			ManagementKey:  "management-key",
			CollectorMode:  "http",
			PollInterval:   15 * time.Millisecond,
		})
		t.Cleanup(func() { _ = manager.StopAndWait(context.Background()) })

		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(disconnects) >= 1 && len(observed) >= 1
		})
		mu.Lock()
		disc := disconnects[0]
		recovery := observed[len(observed)-1]
		mu.Unlock()
		if disc != "http" || !recovery.ForceGapAdvance {
			t.Fatalf("post-pop DB ingest failure = disc:%q recovery:%+v, want http + ForceGapAdvance=true", disc, recovery)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestCollectorPreObserveSeqFailClosedAndInitialDrainGating(t *testing.T) {
	t.Run("unreadable preObserveSeqFn refuses Pop and Subscribe without popping items", func(t *testing.T) {
		var httpPops atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v0/management/usage-queue" {
				httpPops.Add(1)
				_, _ = io.WriteString(w, `[]`)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(upstream.Close)

		st := newTestStore(t)
		cfg := testConfig(t, "http")
		cfg.PollInterval = 15 * time.Millisecond
		manager := NewManager(cfg, st)

		var seqAvailable atomic.Bool
		manager.SetPreObserveSeqFunc(func(context.Context) (int64, bool) {
			if seqAvailable.Load() {
				return 0, true
			}
			return 0, false
		})
		firstErrCh := make(chan error, 1)
		manager.SetFirstPollErrorHandler(func(_ context.Context, _ uint64, _ string, err error) {
			select {
			case firstErrCh <- err:
			default:
			}
		})
		observedCh := make(chan ObservationEvent, 4)
		manager.SetObservationCallbacks(func(_ context.Context, ev ObservationEvent) error {
			observedCh <- ev
			return nil
		}, nil)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		manager.Start(ctx, RuntimeConfig{
			CPAUpstreamURL:          upstream.URL,
			ManagementKey:           "management-key",
			CollectorMode:           "http",
			PollInterval:            15 * time.Millisecond,
			RetentionBudgetMS:       45_000,
			RetentionBudgetVerified: true,
		})
		t.Cleanup(func() { _ = manager.StopAndWait(context.Background()) })

		select {
		case <-firstErrCh:
		case <-time.After(3 * time.Second):
			t.Fatal("expected firstPollError when preObserveSeqFn fails closed")
		}
		if got := httpPops.Load(); got != 0 {
			t.Fatalf("Pop called %d times while preObserveSeqFn failed closed, want 0", got)
		}
		select {
		case ev := <-observedCh:
			t.Fatalf("unexpected observation while preObserveSeqFn failed closed: %+v", ev)
		default:
		}

		seqAvailable.Store(true)
		select {
		case ev := <-observedCh:
			if !ev.PreObserveSeqSet || ev.PreObserveSeq != 0 {
				t.Fatalf("recovered observation = %+v, want PreObserveSeqSet=true seq=0", ev)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("collector never observed after preObserveSeqFn recovered")
		}
	})

	t.Run("holds InitialRunObserve until multi-batch retained queue drains below batchSize", func(t *testing.T) {
		var pollNum atomic.Int32
		var obsMu sync.Mutex
		var observed []ObservationEvent
		var observedPollCount int32

		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v0/management/usage-queue" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			n := pollNum.Add(1)
			switch n {
			case 1:
				_, _ = io.WriteString(w, `[{"timestamp":"2026-11-27T00:00:01Z","model":"m","endpoint":"POST /v1/chat/completions","api_key":"k1","input_tokens":1,"output_tokens":1,"total_tokens":2},{"timestamp":"2026-11-27T00:00:02Z","model":"m","endpoint":"POST /v1/chat/completions","api_key":"k2","input_tokens":1,"output_tokens":1,"total_tokens":2}]`)
			case 2:
				_, _ = io.WriteString(w, `[{"timestamp":"2026-11-27T00:00:03Z","model":"m","endpoint":"POST /v1/chat/completions","api_key":"k3","input_tokens":1,"output_tokens":1,"total_tokens":2},{"timestamp":"2026-11-27T00:00:04Z","model":"m","endpoint":"POST /v1/chat/completions","api_key":"k4","input_tokens":1,"output_tokens":1,"total_tokens":2}]`)
			case 3:
				_, _ = io.WriteString(w, `[{"timestamp":"2026-11-27T00:00:05Z","model":"m","endpoint":"POST /v1/chat/completions","api_key":"k5","input_tokens":1,"output_tokens":1,"total_tokens":2}]`)
			default:
				_, _ = io.WriteString(w, `[]`)
			}
		}))
		t.Cleanup(upstream.Close)

		st := newTestStore(t)
		cfg := testConfig(t, "http")
		cfg.BatchSize = 2
		cfg.PollInterval = 15 * time.Millisecond
		manager := NewManager(cfg, st)
		manager.SetPreObserveSeqFunc(func(context.Context) (int64, bool) { return 0, true })
		manager.SetObservationCallbacks(func(_ context.Context, ev ObservationEvent) error {
			obsMu.Lock()
			if len(observed) == 0 {
				observedPollCount = pollNum.Load()
			}
			observed = append(observed, ev)
			obsMu.Unlock()
			return nil
		}, nil)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		manager.Start(ctx, RuntimeConfig{
			CPAUpstreamURL:          upstream.URL,
			ManagementKey:           "management-key",
			CollectorMode:           "http",
			BatchSize:               2,
			PollInterval:            15 * time.Millisecond,
			RetentionBudgetMS:       45_000,
			RetentionBudgetVerified: true,
		})
		t.Cleanup(func() { _ = manager.StopAndWait(context.Background()) })

		waitFor(t, func() bool {
			obsMu.Lock()
			defer obsMu.Unlock()
			return len(observed) >= 1
		})
		if observedPollCount < 3 {
			t.Fatalf("InitialRunObserve fired on poll %d, want >= 3 after full batches drained (< batchSize=2)", observedPollCount)
		}
		count, _, err := st.Counts(context.Background())
		if err != nil || count != 5 {
			t.Fatalf("ingested events at InitialRunObserve = (%d, %v), want 5", count, err)
		}
	})

	t.Run("multi-batch drain preserves budget only when final batch finishes within budget", func(t *testing.T) {
		var clockMS atomic.Int64
		clockMS.Store(1_760_000_000_000)

		var pollNum atomic.Int32
		var obsMu sync.Mutex
		var observed []ObservationEvent
		var disconnects atomic.Int32

		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v0/management/usage-queue" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch pollNum.Add(1) {
			case 1:
				// Poll 1 at +2s pops full batch; Poll 2 finishes at +5s (still within 7.5s budget).
				clockMS.Store(1_760_000_005_000)
				_, _ = io.WriteString(w, `[{"timestamp":"2026-11-27T00:00:01Z","model":"m","endpoint":"POST /v1/chat/completions","api_key":"k1","input_tokens":1,"output_tokens":1,"total_tokens":2},{"timestamp":"2026-11-27T00:00:02Z","model":"m","endpoint":"POST /v1/chat/completions","api_key":"k2","input_tokens":1,"output_tokens":1,"total_tokens":2}]`)
			case 2:
				_, _ = io.WriteString(w, `[{"timestamp":"2026-11-27T00:00:03Z","model":"m","endpoint":"POST /v1/chat/completions","api_key":"k3","input_tokens":1,"output_tokens":1,"total_tokens":2}]`)
			default:
				_, _ = io.WriteString(w, `[]`)
			}
		}))
		t.Cleanup(upstream.Close)

		st := newTestStore(t)
		cfg := testConfig(t, "http")
		cfg.BatchSize = 2
		cfg.PollInterval = 15 * time.Millisecond
		manager := NewManager(cfg, st)
		manager.SetNowFunc(clockMS.Load)
		manager.SetPreObserveSeqFunc(func(context.Context) (int64, bool) { return 0, true })
		var failedDial atomic.Bool
		manager.SetHTTPClient(&http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if !failedDial.Swap(true) {
					clockMS.Store(1_760_000_002_000)
					return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
				}
				return http.DefaultTransport.RoundTrip(req)
			}),
		})
		manager.SetObservationCallbacks(func(_ context.Context, ev ObservationEvent) error {
			obsMu.Lock()
			observed = append(observed, ev)
			obsMu.Unlock()
			return nil
		}, func(_ context.Context, _ uint64, _ string) {
			disconnects.Add(1)
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		manager.Start(ctx, RuntimeConfig{
			CPAUpstreamURL:          upstream.URL,
			ManagementKey:           "management-key",
			CollectorMode:           "http",
			BatchSize:               2,
			PollInterval:            15 * time.Millisecond,
			RetentionBudgetMS:       7_500,
			RetentionBudgetVerified: true,
		})
		t.Cleanup(func() { _ = manager.StopAndWait(context.Background()) })

		waitFor(t, func() bool {
			obsMu.Lock()
			defer obsMu.Unlock()
			return len(observed) >= 1
		})
		obsMu.Lock()
		first := observed[0]
		obsMu.Unlock()
		if first.ForceGapAdvance || disconnects.Load() != 0 {
			t.Fatalf("final batch within budget got ForceGapAdvance=%v disconnects=%d, want (false, 0): %+v", first.ForceGapAdvance, disconnects.Load(), first)
		}
		if first.ObservedAtMS != 1_760_000_005_000 {
			t.Fatalf("ObservedAtMS = %d, want 1760000005000", first.ObservedAtMS)
		}
		_ = manager.StopAndWait(context.Background())

		// Batch 1 starts within budget (+5s <= 7.5s) and inter-batch step is <= 7.5s (+7s),
		// yet final < batchSize batch finishes at +12s (> 7.5s budget): must emit disconnect and ForceGapAdvance=true.
		var exceedPoll atomic.Int32
		var exceedClock atomic.Int64
		exceedClock.Store(1_760_000_000_000)
		var exceedDisconnects atomic.Int32
		exceedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v0/management/usage-queue" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch exceedPoll.Add(1) {
			case 1:
				exceedClock.Store(1_760_000_012_000)
				_, _ = io.WriteString(w, `[{"timestamp":"2026-11-27T00:00:01Z","model":"m","endpoint":"POST /v1/chat/completions","api_key":"k1","input_tokens":1,"output_tokens":1,"total_tokens":2},{"timestamp":"2026-11-27T00:00:02Z","model":"m","endpoint":"POST /v1/chat/completions","api_key":"k2","input_tokens":1,"output_tokens":1,"total_tokens":2}]`)
			case 2:
				_, _ = io.WriteString(w, `[{"timestamp":"2026-11-27T00:00:03Z","model":"m","endpoint":"POST /v1/chat/completions","api_key":"k3","input_tokens":1,"output_tokens":1,"total_tokens":2}]`)
			default:
				_, _ = io.WriteString(w, `[]`)
			}
		}))
		t.Cleanup(exceedServer.Close)

		exceedMgr := NewManager(cfg, newTestStore(t))
		exceedMgr.SetNowFunc(exceedClock.Load)
		exceedMgr.SetPreObserveSeqFunc(func(context.Context) (int64, bool) { return 0, true })
		var exceedDial atomic.Bool
		exceedMgr.SetHTTPClient(&http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if !exceedDial.Swap(true) {
					exceedClock.Store(1_760_000_005_000)
					return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
				}
				return http.DefaultTransport.RoundTrip(req)
			}),
		})
		var exceedObsMu sync.Mutex
		var exceedObserved []ObservationEvent
		exceedMgr.SetObservationCallbacks(func(_ context.Context, ev ObservationEvent) error {
			exceedObsMu.Lock()
			exceedObserved = append(exceedObserved, ev)
			exceedObsMu.Unlock()
			return nil
		}, func(_ context.Context, _ uint64, _ string) {
			exceedDisconnects.Add(1)
		})
		exceedMgr.Start(ctx, RuntimeConfig{
			CPAUpstreamURL:          exceedServer.URL,
			ManagementKey:           "management-key",
			CollectorMode:           "http",
			BatchSize:               2,
			PollInterval:            15 * time.Millisecond,
			RetentionBudgetMS:       7_500,
			RetentionBudgetVerified: true,
		})
		t.Cleanup(func() { _ = exceedMgr.StopAndWait(context.Background()) })
		waitFor(t, func() bool {
			exceedObsMu.Lock()
			defer exceedObsMu.Unlock()
			return exceedDisconnects.Load() >= 1 && len(exceedObserved) >= 1
		})
		exceedObsMu.Lock()
		exceeded := exceedObserved[0]
		exceedObsMu.Unlock()
		if !exceeded.ForceGapAdvance || exceeded.OutageMS != 12_000 || exceeded.ObservedAtMS != 1_760_000_012_000 {
			t.Fatalf("final batch >7.5s budget = %+v, want ForceGapAdvance=true OutageMS=12000 ObservedAtMS=1760000012000", exceeded)
		}
	})
}

func TestErrorFreeIntraRunPauseExceedingBudgetMarksUnreplayedLoss(t *testing.T) {
	var clockMS atomic.Int64
	clockMS.Store(1_760_000_000_000)

	var polls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/usage-queue" {
			http.NotFound(w, r)
			return
		}
		polls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Config{}
	manager := NewManager(cfg, newTestStore(t))
	manager.SetNowFunc(clockMS.Load)
	manager.SetPreObserveSeqFunc(func(context.Context) (int64, bool) { return 0, true })

	var mu sync.Mutex
	var observed []ObservationEvent
	var disconnects int
	manager.SetObservationCallbacks(func(_ context.Context, ev ObservationEvent) error {
		mu.Lock()
		observed = append(observed, ev)
		n := len(observed)
		mu.Unlock()
		if n == 1 {
			// Simulate an error-free pause > RetentionBudgetMS (7.5s) before Poll 2.
			clockMS.Store(1_760_000_010_000)
		}
		return nil
	}, func(_ context.Context, _ uint64, _ string) {
		mu.Lock()
		disconnects++
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	manager.Start(ctx, RuntimeConfig{
		CPAUpstreamURL:          upstream.URL,
		ManagementKey:           "management-key",
		CollectorMode:           "http",
		BatchSize:               10,
		PollInterval:            15 * time.Millisecond,
		RetentionBudgetMS:       7_500,
		RetentionBudgetVerified: true,
	})
	t.Cleanup(func() { _ = manager.StopAndWait(context.Background()) })

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return disconnects >= 1 && len(observed) >= 2
	})

	mu.Lock()
	first := observed[0]
	second := observed[1]
	gotDisconnects := disconnects
	mu.Unlock()

	if !first.InitialRunObserve || first.ForceGapAdvance {
		t.Fatalf("first observation = %+v, want InitialRunObserve=true ForceGapAdvance=false", first)
	}
	if gotDisconnects < 1 {
		t.Fatalf("disconnects = %d, want >= 1", gotDisconnects)
	}
	if second.InitialRunObserve || !second.ForceGapAdvance || second.OutageMS != 10_000 {
		t.Fatalf("second observation = %+v, want InitialRunObserve=false ForceGapAdvance=true OutageMS=10000", second)
	}
}

func TestVerifiedRetentionBudgetMSGuardsHugeRetentionOverflow(t *testing.T) {
	hugeValues := []int{
		math.MaxInt64/3000 + 1,
		4_000_000_000_000_000,
		math.MaxInt,
	}
	for _, seconds := range hugeValues {
		got := VerifiedRetentionBudgetMS(cpa.UsageConfig{
			RetentionVerified:               true,
			RedisUsageQueueRetentionSeconds: seconds,
		})
		if got != MaxRetainedQueueOutageMS {
			t.Fatalf("VerifiedRetentionBudgetMS(%d) = %d, want %d", seconds, got, MaxRetainedQueueOutageMS)
		}
	}
}
