package collector

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
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
