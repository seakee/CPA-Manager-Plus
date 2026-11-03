package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradeauth"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

func TestStartupUpgradeAuthenticationReadOnlyAndSourceBound(t *testing.T) {
	key := "old-key" // Deliberately fails Fresh's new-key strength policy.
	dir, paths := upgradeAuthSource(t, key)
	database := paths.Database
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
	// A wrong verb is only answered to a proved credential: an unauthenticated
	// caller learns neither that this path exists nor which method it takes.
	for _, method := range []string{"GET", "PUT", "DELETE", "OPTIONS"} {
		request(h, method, upgradeauth.Path, "", "", "", 401)
	}
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
	db, _ := sql.Open("sqlite", database)
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
	// Neither is a verdict on the key: the source could not be read to compare
	// it, so the answer names the source, exactly as on the wizard reads.
	h = newHandler()
	if err := os.WriteFile(database, []byte("not SQLite"), 0600); err != nil {
		t.Fatal(err)
	}
	request(h, "POST", upgradeauth.Path, key, "", "", 503)
	if err := os.Remove(database); err != nil {
		t.Fatal(err)
	}
	request(h, "POST", upgradeauth.Path, key, "", "", 503)
}

// upgradeAuthSource writes the smallest 1.x source the entry page can verify and
// returns its root with the inspection paths the leaf reads.
func upgradeAuthSource(t *testing.T, key string) (string, sqliterepo.InspectionPaths) {
	t.Helper()
	dir := t.TempDir()
	database := filepath.Join(dir, "usage.sqlite")
	credential, err := security.NewAdminCredential(key, "test")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`pragma journal_mode=delete;
create table settings(key text primary key, value text);
create table usage_events(id integer primary key, fail_body blob);
insert into usage_events values(1,x'00ff');`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into settings values('admin_credential_v1',?)`, string(raw)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	paths := legacypreflight.DefaultPaths(database)
	if err := os.WriteFile(paths.DataKey, []byte("preserve-existing-data-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The 1.x ownership marker: a read-only mount makes it unwritable, which is
	// exactly what the fence would demand.
	if err := os.WriteFile(database+".manager.lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, paths
}

// readOnlyTree makes a fixture tree look like a volume mounted read-only: no
// directory accepts a new entry, no file accepts a write, and the ownership
// marker included. The original modes are restored so the temporary directory
// can still be removed.
func readOnlyTree(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o555)
		}
		if entry.Type().IsRegular() {
			return os.Chmod(path, 0o444)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("mount the source tree read-only: %v", err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() {
				return os.Chmod(path, 0o755)
			}
			return os.Chmod(path, 0o644)
		})
	})
}

// TestStartupUpgradeAuthenticationAcceptsAReadOnlySource is the R2 contract: the
// wizard must mount on a 1.x volume the operator mounted read-only, so the entry
// page authenticates on a 0555 tree with a 0444 ownership marker. The leaf
// observes the marker instead of taking the fence, and it still writes nothing.
func TestStartupUpgradeAuthenticationAcceptsAReadOnlySource(t *testing.T) {
	const key = "read-only-source-key"
	dir, paths := upgradeAuthSource(t, key)
	readOnlyTree(t, dir)
	before := readSourceTree(t, dir)

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
	r := httptest.NewRequest("POST", upgradeauth.Path, nil)
	r.Host = "manager.test"
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("entry page on a read-only source = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Authenticated bool   `json:"authenticated"`
		Revision      string `json:"source_revision"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Authenticated || len(response.Revision) != 64 {
		t.Fatalf("entry page response = %s", w.Body.String())
	}
	if after := readSourceTree(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("authentication wrote into the read-only source: before %v after %v", before, after)
	}
	// A wrong key is still a credential verdict, not a mount failure.
	r = httptest.NewRequest("POST", upgradeauth.Path, nil)
	r.Host = "manager.test"
	r.Header.Set("Authorization", "Bearer another-Manager-key")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("foreign key on a read-only source = %d: %s", w.Code, w.Body.String())
	}
}

// readSourceTree records every entry under a source root with its mode and
// content, so a test can prove a read-only mount was neither written nor
// recreated.
func readSourceTree(t *testing.T, root string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		entries[path] = info.Mode().String() + "\x00" + string(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("read the source tree: %v", err)
	}
	return entries
}
