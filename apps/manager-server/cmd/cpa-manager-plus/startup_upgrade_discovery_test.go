package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	bootstrapcontract "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/contract/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradeauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

// TestWALStoreWithoutAdmissionStillProjectsUnavailable is the negative half of
// the UpgradeOnly projection. Only a caller that already holds the admission may
// project its inspection, so the fenced discovery path is unchanged: a v2 WAL
// store that no admission granted still fails closed instead of borrowing the
// admitted projection. Legacy and recovery sources keep exactly the answer they
// had.
func TestWALStoreWithoutAdmissionStillProjectsUnavailable(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	database := upgradeSurfaceManagerStore(t, "manager-store-administrator-key", false)
	assertWALStore(t, database)

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(database), legacypreflight.Options{}, 5*time.Second)
	if h.readOnlyDiscovery == nil {
		t.Fatal("the fixed discovery path was not mounted")
	}
	server := httptest.NewServer(h)
	defer server.Close()

	response, err := server.Client().Get(server.URL + bootstrapdiscovery.Path)
	if err != nil {
		t.Fatalf("read discovery: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read discovery body: %v", err)
	}
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("discovery status = %d, want 503; body = %s", response.StatusCode, body)
	}
	var discovery bootstrapcontract.DiscoveryDTO
	if err := json.Unmarshal(body, &discovery); err != nil {
		t.Fatalf("decode discovery: %v; body = %s", err, body)
	}
	if discovery.Status != bootstrap.PublicUnavailable ||
		discovery.AuthEntry != bootstrap.AuthEntryUnavailable ||
		discovery.Reason != bootstrap.ReasonObservationUnavailable ||
		discovery.Mode != nil {
		t.Fatalf("discovery = %+v, want the fail-closed observation", discovery)
	}
}

// assertWALStore proves the fixture is the shape the rollback-mode discovery
// snapshot refuses: the SQLite header's write/read format bytes are 2/2.
func assertWALStore(t *testing.T, database string) {
	t.Helper()
	file, err := os.Open(database)
	if err != nil {
		t.Fatalf("open the fixture store: %v", err)
	}
	defer func() { _ = file.Close() }()
	header := make([]byte, 20)
	if _, err := io.ReadFull(file, header); err != nil {
		t.Fatalf("read the fixture header: %v", err)
	}
	if header[18] != 2 || header[19] != 2 {
		t.Fatalf("the fixture store is not in WAL mode: header=%v", header[18:20])
	}
}

// legacyWALSourceFixture builds a stopped 1.x Manager database in SQLite WAL
// mode (header bytes 18..19 = 2/2, matching every 1.14.x release) with an
// existing ownership lock marker and optional administrator credential.
func legacyWALSourceFixture(t *testing.T, adminKey string) (string, sqliterepo.InspectionPaths) {
	t.Helper()
	dir := t.TempDir()
	database := filepath.Join(dir, "usage.sqlite")
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatalf("open legacy WAL fixture: %v", err)
	}
	for _, stmt := range []string{
		`pragma journal_mode = wal`,
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null default 0)`,
		`create table usage_events (id integer primary key, total_tokens integer not null default 0)`,
		`insert into usage_events (id, total_tokens) values (1, 42)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			t.Fatalf("seed legacy WAL fixture: %v", err)
		}
	}
	if adminKey != "" {
		credential, err := security.NewAdminCredential(adminKey, "legacy-v1")
		if err != nil {
			_ = db.Close()
			t.Fatalf("create legacy admin credential: %v", err)
		}
		if err := setting.New(db).SaveAdminCredential(t.Context(), credential); err != nil {
			_ = db.Close()
			t.Fatalf("save legacy admin credential: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy WAL fixture: %v", err)
	}
	paths := legacypreflight.DefaultPaths(database)
	if err := os.WriteFile(paths.DataKey, []byte("legacy-data-key-bytes-preserved"), 0o600); err != nil {
		t.Fatalf("write legacy data.key: %v", err)
	}
	if err := os.WriteFile(database+".manager.lock", nil, 0o600); err != nil {
		t.Fatalf("write legacy lock marker: %v", err)
	}
	assertWALStore(t, database)
	return dir, paths
}

func assertWriteAndCanonicalPathsClosed(t *testing.T, server *httptest.Server, adminKey string) {
	t.Helper()
	for _, target := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/health", http.StatusServiceUnavailable},
		{http.MethodGet, "/", http.StatusNotFound},
		{http.MethodGet, "/management.html", http.StatusNotFound},
		{http.MethodGet, "/setup", http.StatusNotFound},
		{http.MethodGet, "/api/v1/status", http.StatusNotFound},
		{http.MethodPost, "/api/v1/settings", http.StatusNotFound},
		{http.MethodGet, "/control/api/v2/status", http.StatusNotFound},
		{http.MethodPost, "/v1/chat/completions", http.StatusNotFound},
		{http.MethodPost, bootstrapdiscovery.Path, http.StatusMethodNotAllowed},
		{http.MethodPost, bootstrap.FixedBootstrapAPIPath + "installation/fresh/claim/exchange", http.StatusNotFound},
	} {
		req, err := http.NewRequest(target.method, server.URL+target.path, strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("new request %s %s: %v", target.method, target.path, err)
		}
		if adminKey != "" {
			req.Header.Set("Authorization", "Bearer "+adminKey)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", target.method, target.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != target.want {
			t.Fatalf("%s %s status = %d, want %d", target.method, target.path, resp.StatusCode, target.want)
		}
	}
}

func TestLegacyWALVolumeProjectsUnsupportedUpgradeAndKeepsWritePathsClosed(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	const legacyKey = "legacy-v1-administrator-key-123456"
	dir, paths := legacyWALSourceFixture(t, legacyKey)
	before := readSourceTree(t, dir)

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)

	// Prove the ownership fence is held while the HTTP response is written and
	// released once the request finishes.
	rec := httptest.NewRecorder()
	checkedFence := false
	h.ServeHTTP(fenceCheckingDiscoveryWriter{rec, func() {
		checkedFence = true
		contender, lockErr := processlock.AcquireExisting(paths.Database)
		if contender != nil {
			_ = contender.Close()
		}
		if !errors.Is(lockErr, processlock.ErrLocked) {
			t.Fatalf("legacy WAL discovery wrote response without holding fence: %v", lockErr)
		}
	}}, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
	if !checkedFence || rec.Code != http.StatusOK {
		t.Fatalf("fenced legacy WAL discovery status=%d body=%s", rec.Code, rec.Body.String())
	}

	server := httptest.NewServer(h)
	defer server.Close()

	resp, err := server.Client().Get(server.URL + bootstrapdiscovery.Path)
	if err != nil {
		t.Fatalf("GET discovery: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read discovery body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("discovery status = %d, want 200; body = %s", resp.StatusCode, raw)
	}
	var discovery bootstrapcontract.DiscoveryDTO
	if err := json.Unmarshal(raw, &discovery); err != nil {
		t.Fatalf("decode discovery: %v; body = %s", err, raw)
	}
	if discovery.Status != bootstrap.PublicUnsupportedUpgrade ||
		discovery.Mode == nil || *discovery.Mode != bootstrap.UnsupportedUpgrade ||
		discovery.AuthEntry != bootstrap.AuthEntryAdminSession ||
		discovery.Reason != bootstrap.ReasonQualificationUnavailable ||
		!discovery.AuthenticationRequired {
		t.Fatalf("discovery = %+v, want unsupported_upgrade", discovery)
	}

	// Upgrade authentication entry accepts the 1.x key without mutating the source.
	authReq, err := http.NewRequest(http.MethodPost, server.URL+upgradeauth.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	authReq.Header.Set("Authorization", "Bearer "+legacyKey)
	authResp, err := server.Client().Do(authReq)
	if err != nil {
		t.Fatalf("POST upgrade authenticate: %v", err)
	}
	authBody, _ := io.ReadAll(authResp.Body)
	_ = authResp.Body.Close()
	if authResp.StatusCode != http.StatusOK || !strings.Contains(string(authBody), `"authenticated":true`) {
		t.Fatalf("upgrade authenticate = %d: %s", authResp.StatusCode, authBody)
	}

	assertWriteAndCanonicalPathsClosed(t, server, legacyKey)

	if after := readSourceTree(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("legacy WAL discovery or auth mutated source tree:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestReadOnlyMountedLegacyWALVolumeProjectsUnsupportedUpgrade(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	const legacyKey = "readonly-legacy-wal-key-123456"
	dir, paths := legacyWALSourceFixture(t, legacyKey)
	readOnlyTree(t, dir)
	before := readSourceTree(t, dir)

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
	server := httptest.NewServer(h)
	defer server.Close()

	resp, err := server.Client().Get(server.URL + bootstrapdiscovery.Path)
	if err != nil {
		t.Fatalf("GET discovery: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"status":"unsupported_upgrade"`) {
		t.Fatalf("readonly legacy WAL discovery = %d: %s", resp.StatusCode, raw)
	}
	if after := readSourceTree(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("readonly legacy WAL discovery mutated source tree")
	}
}

func TestLegacyWALVolumeWithoutAdminCredentialProjectsRecoveryRequired(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	dir, paths := legacyWALSourceFixture(t, "")
	before := readSourceTree(t, dir)

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
	server := httptest.NewServer(h)
	defer server.Close()

	resp, err := server.Client().Get(server.URL + bootstrapdiscovery.Path)
	if err != nil {
		t.Fatalf("GET discovery: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK ||
		!strings.Contains(string(raw), `"status":"recovery_required"`) ||
		!strings.Contains(string(raw), `"reason":"recovery_host_intervention"`) {
		t.Fatalf("credential-less legacy WAL discovery = %d: %s", resp.StatusCode, raw)
	}
	if after := readSourceTree(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("credential-less legacy WAL discovery mutated source tree")
	}
}

func TestPermissionBlockedLegacyVolumeProjectsSourceOwnershipUnavailable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses 0000 file permissions")
	}
	t.Setenv(legacyRootEnvironment, "")
	const legacyKey = "root-owned-legacy-key-123456"
	dir, paths := legacyWALSourceFixture(t, legacyKey)
	lockPath := paths.Database + ".manager.lock"
	if err := os.Chmod(lockPath, 0o000); err != nil {
		t.Fatalf("chmod lock 0000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockPath, 0o600) })

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
	server := httptest.NewServer(h)
	defer server.Close()

	resp, err := server.Client().Get(server.URL + bootstrapdiscovery.Path)
	if err != nil {
		t.Fatalf("GET discovery: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("permission-blocked discovery status = %d, want 200; body = %s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), paths.Database) || strings.Contains(string(raw), dir) {
		t.Fatalf("discovery leaked filesystem path: %s", raw)
	}
	var discovery bootstrapcontract.DiscoveryDTO
	if err := json.Unmarshal(raw, &discovery); err != nil {
		t.Fatalf("decode discovery: %v; body = %s", err, raw)
	}
	if discovery.Status != bootstrap.PublicRecoveryRequired ||
		discovery.Mode == nil || *discovery.Mode != bootstrap.Recovery ||
		discovery.AuthEntry != bootstrap.AuthEntryHostIntervention ||
		discovery.Reason != bootstrap.ReasonSourceOwnershipUnavailable ||
		discovery.AuthenticationRequired {
		t.Fatalf("discovery = %+v, want recovery_required/source_ownership_unavailable", discovery)
	}
	assertWriteAndCanonicalPathsClosed(t, server, legacyKey)

	// Non-SQLite file with unreadable lock marker must NOT manufacture ownership classification.
	garbageDir := t.TempDir()
	garbageDB := filepath.Join(garbageDir, "usage.sqlite")
	if err := os.WriteFile(garbageDB, []byte("not-a-sqlite-database-header-at-all"), 0o644); err != nil {
		t.Fatal(err)
	}
	garbageLock := garbageDB + ".manager.lock"
	if err := os.WriteFile(garbageLock, nil, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(garbageLock, 0o600) })

	garbageHandler := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	garbageHandler.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(garbageDB), legacypreflight.Options{}, 5*time.Second)
	garbageServer := httptest.NewServer(garbageHandler)
	defer garbageServer.Close()
	garbageResp, err := garbageServer.Client().Get(garbageServer.URL + bootstrapdiscovery.Path)
	if err != nil {
		t.Fatal(err)
	}
	_ = garbageResp.Body.Close()
	if garbageResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("non-SQLite file with unreadable lock status = %d, want 503", garbageResp.StatusCode)
	}
}

func TestLegacyWALDiscoveryFailsClosedWhenUnfencedOrBusyOrScratchOverlaps(t *testing.T) {
	t.Run("busy lock stays 503 unavailable", func(t *testing.T) {
		t.Setenv(legacyRootEnvironment, "")
		_, paths := legacyWALSourceFixture(t, "busy-legacy-key-123456")
		held, err := processlock.AcquireExisting(paths.Database)
		if err != nil {
			t.Fatalf("hold lock: %v", err)
		}
		defer held.Close()

		h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("busy lock status = %d, want 503: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing lock marker stays 503 unavailable", func(t *testing.T) {
		t.Setenv(legacyRootEnvironment, "")
		_, paths := legacyWALSourceFixture(t, "missing-lock-key-123456")
		if err := os.Remove(paths.Database + ".manager.lock"); err != nil {
			t.Fatal(err)
		}
		h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("missing lock status = %d, want 503: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("TMPDIR inside source or configured legacy root fails closed without staging", func(t *testing.T) {
		dir, paths := legacyWALSourceFixture(t, "scratch-overlap-key-123456")
		legacyMount := t.TempDir()
		t.Setenv(legacyRootEnvironment, legacyMount)
		for _, badTmp := range []string{dir, legacyMount} {
			t.Setenv("TMPDIR", badTmp)
			before := readSourceTree(t, dir)
			h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
			h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("TMPDIR=%s status = %d, want 503: %s", badTmp, rec.Code, rec.Body.String())
			}
			if after := readSourceTree(t, dir); !reflect.DeepEqual(before, after) {
				t.Fatalf("TMPDIR=%s staged files inside source", badTmp)
			}
		}
	})
}
