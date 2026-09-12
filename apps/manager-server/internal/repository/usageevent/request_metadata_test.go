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

func TestInsertBatchSanitizesCredentialsAtPersistenceBoundary(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)

	const secretMarker = "synthetic0123456789"
	const syntheticToken = "sk-proj-" + secretMarker
	event := streamTestEvent("credential-sanitization", 102, "POST /v1/responses", "gpt-5.4")
	event.Source = "paid provider " + syntheticToken
	event.RawJSON = `{"source":"paid provider ` + syntheticToken + `"}`
	event.FailSummary = "Authorization: Bearer " + syntheticToken
	event.FailBody = `{"access_token":"` + syntheticToken + `"}`
	if _, err := repo.InsertBatch(context.Background(), []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	var source, rawJSON, failSummary, failBody string
	if err := db.QueryRow(`select
		coalesce(source, ''), coalesce(raw_json, ''), coalesce(fail_summary, ''), coalesce(fail_body, '')
		from usage_events where event_hash = ?`, event.EventHash).Scan(
		&source,
		&rawJSON,
		&failSummary,
		&failBody,
	); err != nil {
		t.Fatalf("query persisted credential-bearing fields: %v", err)
	}
	for field, value := range map[string]string{
		"source":       source,
		"raw_json":     rawJSON,
		"fail_summary": failSummary,
		"fail_body":    failBody,
	} {
		if strings.Contains(value, secretMarker) {
			t.Errorf("persisted %s contains the synthetic credential marker", field)
		}
	}
}
