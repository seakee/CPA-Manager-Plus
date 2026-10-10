package usage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	usagesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

func TestGetArchiveDetailReturnsOnlySafePersistedProgress(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	db, err := sqliterepo.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runID := strings.Repeat("a", 32)
	_, err = db.ExecContext(context.Background(), `insert into usage_archive_runs (
		id, mode, schema_version, format, status, cutoff_timestamp_ms, target_event_id,
		event_count, created_at_ms, updated_at_ms, progress_phase, progress_current,
		progress_total, progress_unit, progress_updated_at_ms, manifest_file, manifest_sha256,
		archive_digest, last_error
	) values (?, 'manual', 1, 'gzip-jsonl-v1', 'failed', 100, 100, 100,
		1, 2, 'archive_finalizing', 12, 14, 'segments', 3, 'secret-manifest', 'secret-sha',
		'secret-digest', 'secret-error')`, runID)
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{App: &app.Context{UsageService: usagesvc.New(st, usagesvc.WithArchive(usagesvc.ArchiveConfig{Directory: cfg.UsageArchiveDir}))}}
	request := httptest.NewRequest(http.MethodGet, usageArchivesPath+"/"+runID, nil)
	recorder := httptest.NewRecorder()
	handler.handleArchive(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"progress":{"phase":"archive_finalizing","current":12,"total":14,"unit":"segments","updated_at_ms":3}`) {
		t.Fatalf("progress missing: %s", body)
	}
	for _, forbidden := range []string{"manifest_file", "manifest_sha256", "archive_digest", "file_name", "content_sha256", "event_hash_digest", "last_error", "secret-manifest", "secret-sha", "secret-digest", "secret-error"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %s: %s", forbidden, body)
		}
	}
}

func TestImportReturnsBadRequestWhenUncommittedArrayParsingFails(t *testing.T) {
	st := testutil.NewStore(t, testutil.NewConfig(t))
	handler := &Handler{App: &app.Context{UsageService: usagesvc.New(st)}}
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", strings.NewReader(`[{"event_hash":"one","timestamp_ms":1,"timestamp":"2026-01-01T00:00:00Z","model":"gpt-test"},`))
	recorder := httptest.NewRecorder()

	handler.Import(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestImportReturnsInternalServerErrorForPersistenceFailure(t *testing.T) {
	st := testutil.NewStore(t, testutil.NewConfig(t))
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	handler := &Handler{App: &app.Context{UsageService: usagesvc.New(st)}}
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", strings.NewReader(`{"event_hash":"one","timestamp_ms":1,"timestamp":"2026-01-01T00:00:00Z","model":"gpt-test"}`))
	recorder := httptest.NewRecorder()

	handler.Import(recorder, req)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestImportRejectsKnownOversizedContentLengthBeforeReading(t *testing.T) {
	st := testutil.NewStore(t, testutil.NewConfig(t))
	handler := &Handler{App: &app.Context{UsageService: usagesvc.New(st)}}
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", strings.NewReader("{}"))
	req.ContentLength = maxUsageImportBytes + 1
	recorder := httptest.NewRecorder()

	handler.Import(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestParseImportSessionPath(t *testing.T) {
	cases := []struct {
		path   string
		id     string
		action string
		ok     bool
	}{
		{path: "/v0/management/usage/import-sessions", ok: true},
		{path: "/v0/management/usage/import-sessions/", ok: true},
		{path: "/v0/management/usage/import-sessions/abc", id: "abc", ok: true},
		{path: "/v0/management/usage/import-sessions/abc/chunk", id: "abc", action: "chunk", ok: true},
		{path: "/v0/management/usage/import-sessions/abc/validate", id: "abc", action: "validate", ok: true},
		{path: "/v0/management/usage/import-sessions/abc/complete", id: "abc", action: "complete", ok: true},
		{path: "/v0/management/usage/import-sessions-legacy", ok: false},
		{path: "/v0/management/usage/import-sessions/abc/delete", ok: false},
	}
	for _, test := range cases {
		id, action, ok := parseImportSessionPath(test.path)
		if id != test.id || action != test.action || ok != test.ok {
			t.Errorf("parseImportSessionPath(%q) = (%q, %q, %t)", test.path, id, action, ok)
		}
	}
}
func TestParseArchivePath(t *testing.T) {
	cases := []struct {
		path   string
		id     string
		action string
		ok     bool
	}{
		{path: "/v0/management/usage/archives", ok: true},
		{path: "/v0/management/usage/archives/", ok: true},
		{path: "/v0/management/usage/archives/preview", action: "preview", ok: true},
		{path: "/v0/management/usage/archives/abc", id: "abc", ok: true},
		{path: "/v0/management/usage/archives/abc/resume", id: "abc", action: "resume", ok: true},
		{path: "/v0/management/usage/archives/abc/verify", id: "abc", action: "verify", ok: true},
		{path: "/v0/management/usage/archives/abc/delete", id: "abc", action: "delete", ok: true},
		{path: "/v0/management/usage/archives/abc/cancel", id: "abc", action: "cancel", ok: true},
		{path: "/v0/management/usage/archives-legacy", ok: false},
		{path: "/v0/management/usage/archives/abc/unknown", ok: false},
	}
	for _, test := range cases {
		id, action, ok := parseArchivePath(test.path)
		if id != test.id || action != test.action || ok != test.ok {
			t.Errorf("parseArchivePath(%q) = (%q, %q, %t)", test.path, id, action, ok)
		}
	}
}

func TestParseArchiveLimit(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want int
		ok   bool
	}{
		{raw: "", want: 20, ok: true},
		{raw: "1", want: 1, ok: true},
		{raw: "100", want: 100, ok: true},
		{raw: "0", ok: false},
		{raw: "101", ok: false},
		{raw: "invalid", ok: false},
	} {
		got, err := parseArchiveLimit(test.raw)
		if test.ok && (err != nil || got != test.want) {
			t.Errorf("parseArchiveLimit(%q) = (%d, %v), want %d", test.raw, got, err, test.want)
		}
		if !test.ok && err == nil {
			t.Errorf("parseArchiveLimit(%q) error = nil", test.raw)
		}
	}
}

func TestParseArchiveWait(t *testing.T) {
	for _, test := range []struct {
		query string
		wait  bool
		ok    bool
	}{
		{wait: true, ok: true},
		{query: "background=true", wait: false, ok: true},
		{query: "background=false", wait: true, ok: true},
		{query: "wait=false", wait: false, ok: true},
		{query: "wait=true", wait: true, ok: true},
		{query: "background=invalid"},
		{query: "wait=invalid"},
		{query: "background=true&wait=false"},
	} {
		request := httptest.NewRequest(http.MethodPost, usageArchivesPath+"/run/resume?"+test.query, nil)
		wait, err := parseArchiveWait(request)
		if test.ok && (err != nil || wait != test.wait) {
			t.Errorf("parseArchiveWait(%q) = (%t, %v), want %t", test.query, wait, err, test.wait)
		}
		if !test.ok && err == nil {
			t.Errorf("parseArchiveWait(%q) error = nil", test.query)
		}
	}
}

func TestWriteImportSessionErrorSanitizesUnavailableFailures(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeImportSessionError(recorder, &usagesvc.ImportSessionError{
		Code:    usagesvc.ImportSessionErrorUnavailable,
		Message: "validate usage import session directory /private/secret",
	})

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"error":"usage import session request failed"`) ||
		!strings.Contains(body, `"code":"usage_import_session_unavailable"`) {
		t.Fatalf("sanitized unavailable body = %s", body)
	}
	if strings.Contains(body, "/private/secret") || strings.Contains(body, "validate usage import session directory") {
		t.Fatalf("unavailable body leaked internal details: %s", body)
	}
}

func TestArchiveErrorCodeDoesNotDependOnInternalErrorText(t *testing.T) {
	for _, message := range []string{
		"invalid admin key from an internal dependency",
		"usage service is not configured in an internal dependency",
		"management API validation failed in an internal dependency",
	} {
		if code := archiveErrorCode(errors.New(message)); code != "request_failed" {
			t.Fatalf("archiveErrorCode(%q) = %q, want request_failed", message, code)
		}
	}
}

func TestDecodeSingleJSONRejectsOversizedRequests(t *testing.T) {
	for _, test := range []struct {
		name          string
		body          string
		contentLength int64
	}{
		{
			name:          "known content length",
			body:          `{}`,
			contentLength: maxUsageArchiveRequestBytes + 1,
		},
		{
			name:          "unknown content length",
			body:          strings.Repeat(" ", int(maxUsageArchiveRequestBytes)+1) + `{}`,
			contentLength: -1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, usageArchivesPath+"/preview", strings.NewReader(test.body))
			request.ContentLength = test.contentLength
			recorder := httptest.NewRecorder()
			var target archiveCutoffRequest

			err := decodeSingleJSON(recorder, request, &target)
			var maxBytesError *http.MaxBytesError
			if !errors.As(err, &maxBytesError) || maxBytesError.Limit != maxUsageArchiveRequestBytes {
				t.Fatalf("decodeSingleJSON() error = %#v, want MaxBytesError limit %d", err, maxUsageArchiveRequestBytes)
			}
		})
	}
}

func TestValidateEmptyArchiveBody(t *testing.T) {
	for _, test := range []struct {
		name          string
		body          string
		contentLength int64
		wantError     bool
		wantTooLarge  bool
	}{
		{name: "empty", body: "", contentLength: 0},
		{name: "whitespace", body: " \n\t", contentLength: 3},
		{name: "non-empty", body: `{}`, contentLength: 2, wantError: true},
		{
			name:          "known oversized",
			body:          "",
			contentLength: maxUsageArchiveRequestBytes + 1,
			wantError:     true,
			wantTooLarge:  true,
		},
		{
			name:          "unknown oversized",
			body:          strings.Repeat(" ", int(maxUsageArchiveRequestBytes)+1),
			contentLength: -1,
			wantError:     true,
			wantTooLarge:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, usageArchivesPath+"/run/resume", strings.NewReader(test.body))
			request.ContentLength = test.contentLength
			recorder := httptest.NewRecorder()

			err := validateEmptyArchiveBody(recorder, request)
			if !test.wantError {
				if err != nil {
					t.Fatalf("validateEmptyArchiveBody() error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validateEmptyArchiveBody() error = nil")
			}
			var maxBytesError *http.MaxBytesError
			if got := errors.As(err, &maxBytesError); got != test.wantTooLarge {
				t.Fatalf("validateEmptyArchiveBody() MaxBytesError = %t, want %t; err = %v", got, test.wantTooLarge, err)
			}
		})
	}
}

func TestImportAcceptsLegacyNoncanonicalEventHash(t *testing.T) {
	st := testutil.NewStore(t, testutil.NewConfig(t))
	handler := &Handler{App: &app.Context{UsageService: usagesvc.New(st)}}
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", strings.NewReader(`{"event_hash":"legacy-short-hash","timestamp_ms":1,"timestamp":"2026-01-01T00:00:00Z","model":"gpt-test"}`))
	recorder := httptest.NewRecorder()

	handler.Import(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200 OK for legacy event hash compatibility, got status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"added":1`) {
		t.Fatalf("expected imported event in response, got body = %s", recorder.Body.String())
	}
}
