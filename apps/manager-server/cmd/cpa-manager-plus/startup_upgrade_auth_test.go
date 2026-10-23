package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradeauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

func TestStartupUpgradeAuthenticationReadOnlyAndSourceBound(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "usage.sqlite")
	key := "old-key" // Deliberately fails Fresh's new-key strength policy.
	credential, err := security.NewAdminCredential(key, "test")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(credential)
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`pragma journal_mode=delete;
create table settings(key text primary key, value text);
create table usage_events(id integer primary key, fail_body blob);
insert into usage_events values(1,x'00ff');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into settings values('admin_credential_v1',?)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	paths := legacypreflight.DefaultPaths(database)
	if err := os.WriteFile(paths.DataKey, []byte("preserve-existing-data-key"), 0600); err != nil {
		t.Fatal(err)
	}
	newHandler := func() *startupHTTPHandler {
		h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
		return h
	}
	h := newHandler()
	request := func(handler http.Handler, method, path, key, source, origin string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.Host = "manager.test"
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		if source != "" {
			r.Header.Set("X-CPAMP-Upgrade-Source", source)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		body := w.Body.String()
		for _, secret := range []string{database, "preserve-existing-data-key", "fail_body", "keyHash", "salt", "controlBasePath"} {
			if strings.Contains(body, secret) {
				t.Fatalf("sensitive response: %s", body)
			}
		}
		return body
	}
	before, _ := os.ReadFile(database)
	keyBefore, _ := os.ReadFile(paths.DataKey)
	entriesBefore, _ := os.ReadDir(dir)
	request(h, "GET", bootstrapdiscovery.Path, "", "", "", 200)
	for _, candidate := range []string{"", "CPA-management-key", "another-Manager-key", "fresh-bootstrap-session"} {
		request(h, "POST", upgradeauth.Path, candidate, "", "", 401)
	}
	request(h, "GET", upgradeauth.Path, key, "", "", 405)
	request(h, "OPTIONS", upgradeauth.Path, "", "", "", 405)
	request(h, "POST", upgradeauth.Path, key, "", "https://other.test", 403)
	for _, path := range []string{upgradeauth.Path + "?credential=bad", upgradeauth.Path + "/", "/.well-known/cpamp-bootstrap/upgrade//authenticate", "/.well-known/cpamp-bootstrap/api/v2/admin/commit", "/v0/management/config"} {
		request(h, "POST", path, key, "", "", 404)
	}
	body := request(h, "POST", upgradeauth.Path, key, "", "http://manager.test", 200)
	var response struct {
		Revision  string `json:"source_revision"`
		Migration bool   `json:"migration_available"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil || len(response.Revision) != 64 || response.Migration {
		t.Fatal(body)
	}
	request(h, "POST", upgradeauth.Path, key, response.Revision, "", 200)
	request(h, "POST", upgradeauth.Path, "", response.Revision, "", 401)             // digest is not authority
	request(newHandler(), "POST", upgradeauth.Path, key, response.Revision, "", 409) // restart
	after, _ := os.ReadFile(database)
	keyAfter, _ := os.ReadFile(paths.DataKey)
	entriesAfter, _ := os.ReadDir(dir)
	if !bytes.Equal(before, after) || !bytes.Equal(keyBefore, keyAfter) || len(entriesBefore) != len(entriesAfter) {
		t.Fatal("authentication changed source bytes, credential/history/schema, data.key or directory entries")
	}
	// A renewed salted credential for the same key must invalidate the binding
	// even if source metadata is restored. The password alone is not the context.
	info, err := os.Stat(database)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := security.NewAdminCredential(key, "test")
	if err != nil {
		t.Fatal(err)
	}
	renewedRaw, _ := json.Marshal(renewed)
	db, _ = sql.Open("sqlite", database)
	if _, err := db.Exec(`update settings set value=? where key='admin_credential_v1'`, string(renewedRaw)); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if err := os.Chtimes(database, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	request(h, "POST", upgradeauth.Path, key, response.Revision, "", 409)
	body = request(h, "POST", upgradeauth.Path, key, "", "", 200)
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatal(err)
	}

	db, _ = sql.Open("sqlite", database)
	if _, err := db.Exec(`insert into usage_events values(2,x'0102')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	request(h, "POST", upgradeauth.Path, key, response.Revision, "", 409)
	// Rotating the credential invalidates the old key before any detail is read.
	rotated, _ := security.NewAdminCredential("replacement-key", "test")
	rotatedRaw, _ := json.Marshal(rotated)
	db, _ = sql.Open("sqlite", database)
	_, _ = db.Exec(`update settings set value=? where key='admin_credential_v1'`, string(rotatedRaw))
	_ = db.Close()
	request(h, "POST", upgradeauth.Path, key, response.Revision, "", 401)
	request(h, "POST", upgradeauth.Path, "replacement-key", response.Revision, "", 409)
	// Unverifiable credentials never fall back to a fresh administrator.
	db, _ = sql.Open("sqlite", database)
	_, _ = db.Exec(`update settings set value='{}' where key='admin_credential_v1'`)
	_ = db.Close()
	request(h, "POST", upgradeauth.Path, key, "", "", 401)
	if body := request(h, "GET", bootstrapdiscovery.Path, "", "", "", 200); !strings.Contains(body, `"status":"recovery_required"`) {
		t.Fatalf("unverifiable credential did not retain Recovery: %s", body)
	}
	h.BeginWritableStartup()
	request(h, "POST", upgradeauth.Path, key, "", "", 404)
	h = newHandler()
	h.SetReady(http.NotFoundHandler())
	request(h, "POST", upgradeauth.Path, key, "", "", 404)
	h = newHandler()
	h.SetCompleted()
	request(h, "POST", upgradeauth.Path, key, "", "", 404)
	// Damaged and missing sources cannot be authenticated or initialized here.
	h = newHandler()
	if err := os.WriteFile(database, []byte("not SQLite"), 0600); err != nil {
		t.Fatal(err)
	}
	request(h, "POST", upgradeauth.Path, key, "", "", 401)
	if err := os.Remove(database); err != nil {
		t.Fatal(err)
	}
	request(h, "POST", upgradeauth.Path, key, "", "", 401)
}
