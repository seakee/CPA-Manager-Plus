package collector

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/resp"
	monitoringsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/monitoring"
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
