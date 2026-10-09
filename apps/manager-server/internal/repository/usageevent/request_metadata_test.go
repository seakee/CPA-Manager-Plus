package usageevent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestRequestMetadataPersistsAndIsSearchable(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)

	event := streamTestEvent("request-metadata", 100, "POST /v1/responses", "gpt-5.4")
	event.ClientIP = "192.0.2.10"
	event.XForwardedFor = "203.0.113.5, 198.51.100.8"
	event.UserAgent = "test-client/1.0"
	if _, err := repo.InsertBatch(context.Background(), []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	recent, err := repo.ListRecent(context.Background(), 1)
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("recent event count = %d", len(recent))
	}
	if recent[0].ClientIP != event.ClientIP || recent[0].XForwardedFor != event.XForwardedFor || recent[0].UserAgent != event.UserAgent {
		t.Fatalf("persisted request metadata = client:%q forwarded:%q agent:%q", recent[0].ClientIP, recent[0].XForwardedFor, recent[0].UserAgent)
	}

	for _, query := range []string{"192.0.2.10", "198.51.100.8", "test-client/1.0"} {
		page, err := repo.EventsPageWithFilter(context.Background(), AnalyticsFilter{
			FromMS:      1,
			ToMS:        1000,
			SearchQuery: query,
		}, 0, 0, 10)
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("search %q item count = %d", query, len(page.Items))
		}
		item := page.Items[0]
		if item.ClientIP != event.ClientIP || item.XForwardedFor != event.XForwardedFor || item.UserAgent != event.UserAgent {
			t.Fatalf("search %q request metadata = client:%q forwarded:%q agent:%q", query, item.ClientIP, item.XForwardedFor, item.UserAgent)
		}
	}
}

func TestNewRequestMetadataFieldsPersistAndLoad(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)

	genTrue := true
	streamFalse := false
	ev1 := streamTestEvent("metadata-ev-1", 100, "POST /v1/chat/completions", "gpt-4o")
	ev1.ResponseModel = "gpt-4o-mini"
	ev1.SessionID = "sess-001"
	ev1.ParentSessionID = "parent-001"
	ev1.AccessTokenSHA256 = "sha256-hash-001"
	ev1.Generate = &genTrue
	ev1.Stream = &streamFalse

	genFalse := false
	ev2 := streamTestEvent("metadata-ev-2", 200, "POST /v1/chat/completions", "claude-3-5-sonnet")
	ev2.ResponseModel = "claude-3-haiku"
	ev2.SessionID = "sess-002"
	ev2.Generate = &genFalse
	ev2.Stream = nil // nil unknown

	if _, err := repo.InsertBatch(context.Background(), []usage.Event{ev1, ev2}); err != nil {
		t.Fatalf("insert batch: %v", err)
	}

	recent, err := repo.ListRecent(context.Background(), 2)
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("recent len = %d, want 2", len(recent))
	}

	// ListRecent orders by timestamp_ms desc -> ev2 first, then ev1
	rEv2 := recent[0]
	if rEv2.ResponseModel != "claude-3-haiku" || rEv2.SessionID != "sess-002" || rEv2.Generate == nil || *rEv2.Generate != false || rEv2.Stream != nil {
		t.Fatalf("rEv2 metadata mismatch: %+v", rEv2)
	}

	rEv1 := recent[1]
	if rEv1.ResponseModel != "gpt-4o-mini" || rEv1.SessionID != "sess-001" || rEv1.ParentSessionID != "parent-001" || rEv1.AccessTokenSHA256 != "sha256-hash-001" || rEv1.Generate == nil || *rEv1.Generate != true || rEv1.Stream == nil || *rEv1.Stream != false {
		t.Fatalf("rEv1 metadata mismatch: %+v", rEv1)
	}

	// Verify EventsPageWithFilter read
	page, err := repo.EventsPageWithFilter(context.Background(), AnalyticsFilter{
		FromMS: 50,
		ToMS:   300,
	}, 0, 0, 10)
	if err != nil {
		t.Fatalf("events page: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("page items = %d, want 2", len(page.Items))
	}

	pEv1 := page.Items[1]
	if pEv1.ResponseModel != "gpt-4o-mini" || pEv1.SessionID != "sess-001" || pEv1.ParentSessionID != "parent-001" || pEv1.AccessTokenSHA256 != "sha256-hash-001" || pEv1.Generate == nil || *pEv1.Generate != true || pEv1.Stream == nil || *pEv1.Stream != false {
		t.Fatalf("page item 1 mismatch: %+v", pEv1)
	}
}

func TestInsertBatchNormalizesRequestMetadataAtPersistenceBoundary(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)

	rawPayload, err := json.Marshal(map[string]any{
		"clientIp":      " 192.0.2.10\r\n\u202espoofed ",
		"xForwardedFor": strings.Repeat("203.0.113.5, ", 220),
		"userAgent":     strings.Repeat("test-client/1.0\u200b ", 80),
	})
	if err != nil {
		t.Fatalf("marshal raw payload: %v", err)
	}

	event := streamTestEvent("request-metadata-normalization", 101, "POST /v1/responses", "gpt-5.4")
	event.ClientIP = " 192.0.2.10\r\n\u202espoofed "
	event.XForwardedFor = strings.Repeat("203.0.113.5, ", 220)
	event.UserAgent = strings.Repeat("test-client/1.0\u200b ", 80)
	event.RawJSON = string(rawPayload)
	if _, err := repo.InsertBatch(context.Background(), []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	recent, err := repo.ListRecent(context.Background(), 1)
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("recent event count = %d", len(recent))
	}
	persisted := recent[0]
	if persisted.ClientIP != "192.0.2.10 spoofed" {
		t.Fatalf("client ip = %q", persisted.ClientIP)
	}
	for name, value := range map[string]string{
		"client_ip":       persisted.ClientIP,
		"x_forwarded_for": persisted.XForwardedFor,
		"user_agent":      persisted.UserAgent,
	} {
		if strings.IndexFunc(value, func(r rune) bool { return !unicode.IsGraphic(r) }) >= 0 {
			t.Fatalf("%s contains non-graphic characters: %q", name, value)
		}
	}
	if len(persisted.XForwardedFor) > 2048 || !strings.HasSuffix(persisted.XForwardedFor, "...") {
		t.Fatalf("x-forwarded-for length = %d", len(persisted.XForwardedFor))
	}
	if len(persisted.UserAgent) > 1024 || !strings.HasSuffix(persisted.UserAgent, "...") {
		t.Fatalf("user-agent length = %d", len(persisted.UserAgent))
	}

	var persistedRawJSON string
	if err := db.QueryRow(`select coalesce(raw_json, '') from usage_events where event_hash = ?`, event.EventHash).Scan(&persistedRawJSON); err != nil {
		t.Fatalf("query persisted raw json: %v", err)
	}
	var persistedRaw map[string]any
	if err := json.Unmarshal([]byte(persistedRawJSON), &persistedRaw); err != nil {
		t.Fatalf("decode persisted raw json: %v", err)
	}
	for key, structuredValue := range map[string]string{
		"clientIp":      persisted.ClientIP,
		"xForwardedFor": persisted.XForwardedFor,
		"userAgent":     persisted.UserAgent,
	} {
		if persistedRaw[key] != structuredValue {
			t.Fatalf("raw %s = %#v, structured = %q", key, persistedRaw[key], structuredValue)
		}
	}
}
