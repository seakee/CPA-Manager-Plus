package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
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

	// The observation must not fence the source, and therefore must not rewrite
	// the ownership marker the discovery cache is keyed on.
	locked := paths.Database + ".manager.lock"
	markerBefore := discoveryMarkerState(t, locked)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"unsupported_upgrade"`) {
		t.Fatalf("legacy WAL discovery status=%d body=%s", rec.Code, rec.Body.String())
	}
	if markerAfter := discoveryMarkerState(t, locked); markerAfter != markerBefore {
		t.Fatalf("legacy WAL discovery rewrote the ownership marker: %s -> %s", markerBefore, markerAfter)
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
	beforeTree := readSourceTree(t, dir)
	lockPath := paths.Database + ".manager.lock"
	if err := os.Chmod(lockPath, 0o000); err != nil {
		t.Fatalf("chmod lock 0000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockPath, 0o600) })
	beforeMarker := discoveryMarkerState(t, lockPath)
	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
	server := httptest.NewServer(h)
	defer server.Close()

	for poll := range 4 {
		resp, err := server.Client().Get(server.URL + bootstrapdiscovery.Path)
		if err != nil {
			t.Fatalf("poll %d GET discovery: %v", poll, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("poll %d permission-blocked discovery status = %d, want 200; body = %s", poll, resp.StatusCode, raw)
		}
		if strings.Contains(string(raw), paths.Database) || strings.Contains(string(raw), dir) {
			t.Fatalf("poll %d discovery leaked filesystem path: %s", poll, raw)
		}
		var discovery bootstrapcontract.DiscoveryDTO
		if err := json.Unmarshal(raw, &discovery); err != nil {
			t.Fatalf("poll %d decode discovery: %v; body = %s", poll, err, raw)
		}
		if discovery.Status != bootstrap.PublicRecoveryRequired ||
			discovery.Mode == nil || *discovery.Mode != bootstrap.Recovery ||
			discovery.AuthEntry != bootstrap.AuthEntryHostIntervention ||
			discovery.Reason != bootstrap.ReasonSourceOwnershipUnavailable ||
			discovery.AuthenticationRequired {
			t.Fatalf("poll %d discovery = %+v, want recovery_required/source_ownership_unavailable", poll, discovery)
		}
	}
	assertWriteAndCanonicalPathsClosed(t, server, legacyKey)
	if afterMarker := discoveryMarkerState(t, lockPath); afterMarker != beforeMarker {
		t.Fatalf("permission-blocked discovery rewrote marker: %s -> %s", beforeMarker, afterMarker)
	}
	if err := os.Chmod(lockPath, 0o600); err != nil {
		t.Fatalf("restore lock 0600: %v", err)
	}
	if afterTree := readSourceTree(t, dir); !reflect.DeepEqual(beforeTree, afterTree) {
		t.Fatalf("permission-blocked discovery mutated source tree")
	}
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

// legacyWALVolumeWithPendingWAL builds a stopped 1.x volume whose committed rows
// are still in usage.sqlite-wal. A normal Close checkpoints the journal away, so
// the writer is a helper process that exits without closing its store — the state
// a 1.x container leaves behind when it is stopped hard.
func legacyWALVolumeWithPendingWAL(t *testing.T, adminKey string) (string, sqliterepo.InspectionPaths) {
	t.Helper()
	dir := t.TempDir()
	database := filepath.Join(dir, "usage.sqlite")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLegacyWALPendingWriterFixtureProcess$")
	cmd.Env = append(os.Environ(),
		"CPAMP_LEGACY_WAL_FIXTURE="+database,
		"CPAMP_LEGACY_WAL_FIXTURE_KEY="+adminKey,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("legacy pending WAL fixture: %v %s", err, output)
	}
	wal, err := os.Lstat(database + "-wal")
	if err != nil || wal.Size() == 0 {
		t.Fatalf("the fixture did not keep a pending WAL (err=%v)", err)
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

// TestLegacyWALPendingWriterFixtureProcess is the seeding half of
// legacyWALVolumeWithPendingWAL: it commits rows and leaves without closing, so
// the journal is still on disk when it exits.
func TestLegacyWALPendingWriterFixtureProcess(t *testing.T) {
	database := os.Getenv("CPAMP_LEGACY_WAL_FIXTURE")
	if database == "" {
		t.Skip("helper process")
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatalf("open legacy WAL fixture: %v", err)
	}
	for _, stmt := range []string{
		`pragma journal_mode = wal`,
		`pragma wal_autocheckpoint = 0`,
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null default 0)`,
		`create table usage_events (id integer primary key, total_tokens integer not null default 0)`,
		`insert into usage_events (id, total_tokens) values (1, 42)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed legacy WAL fixture: %v", err)
		}
	}
	if key := os.Getenv("CPAMP_LEGACY_WAL_FIXTURE_KEY"); key != "" {
		credential, err := security.NewAdminCredential(key, "legacy-v1")
		if err != nil {
			t.Fatalf("create legacy admin credential: %v", err)
		}
		if err := setting.New(db).SaveAdminCredential(t.Context(), credential); err != nil {
			t.Fatalf("save legacy admin credential: %v", err)
		}
	}
	os.Exit(0)
}

// TestLegacyVolumeWithoutOwnershipMarkerStaysUnavailableAndCreatesNone is the
// missing-marker contract at the HTTP boundary: a source that cannot prove
// exclusivity is not an upgrade candidate, and the observation must not create
// the marker it failed to find.
func TestLegacyVolumeWithoutOwnershipMarkerStaysUnavailableAndCreatesNone(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	dir, paths := legacyWALSourceFixture(t, "no-marker-legacy-key-123456")
	lockPath := paths.Database + ".manager.lock"
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove the ownership marker: %v", err)
	}
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
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("discovery without an ownership marker = %d, want 503: %s", resp.StatusCode, raw)
	}
	if _, err := os.Lstat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("discovery created the ownership marker it could not find (err=%v)", err)
	}
	if after := readSourceTree(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("discovery without an ownership marker mutated the source tree")
	}
}

// TestWALBackedLegacyVolumeServesReadOnlyUpgradeEntryWithoutWrites is the P0
// contract of the read-only Upgrade entry: a 1.x volume mounted at /data with a
// non-empty journal serves discovery, the administrator-key entry and the
// recorded-status read, and none of the three may create usage.sqlite-shm beside
// the source or rewrite anything in it — including when the volume is mounted
// read-only, where an in-place open cannot even succeed.
func TestWALBackedLegacyVolumeServesReadOnlyUpgradeEntryWithoutWrites(t *testing.T) {
	const legacyKey = "pending-wal-legacy-key-123456"
	for _, mount := range []struct {
		name      string
		removeSHM bool
		readOnly  bool
	}{
		{name: "writable volume without a -shm", removeSHM: true},
		{name: "read-only mount with the stopped writer's -shm", readOnly: true},
	} {
		t.Run(mount.name, func(t *testing.T) {
			t.Setenv(legacyRootEnvironment, "")
			dir, paths := legacyWALVolumeWithPendingWAL(t, legacyKey)
			if mount.removeSHM {
				if err := os.Remove(paths.Database + "-shm"); err != nil {
					t.Fatalf("drop the stale -shm: %v", err)
				}
			}
			if mount.readOnly {
				readOnlyTree(t, dir)
			}
			before := readSourceTree(t, dir)

			h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
			h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
			server := httptest.NewServer(h)
			defer server.Close()

			request := func(method, path, authorization string) (int, string) {
				t.Helper()
				req, err := http.NewRequest(method, server.URL+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				if authorization != "" {
					req.Header.Set("Authorization", authorization)
				}
				response, err := server.Client().Do(req)
				if err != nil {
					t.Fatalf("%s %s: %v", method, path, err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatalf("read %s %s: %v", method, path, err)
				}
				return response.StatusCode, string(body)
			}

			status, body := request(http.MethodGet, bootstrapdiscovery.Path, "")
			if status != http.StatusOK ||
				!strings.Contains(body, `"status":"unsupported_upgrade"`) ||
				!strings.Contains(body, `"auth_entry":"admin_session"`) {
				t.Fatalf("pending WAL discovery = %d: %s", status, body)
			}

			status, body = request(http.MethodPost, upgradeauth.Path, "Bearer "+legacyKey)
			if status != http.StatusOK || !strings.Contains(body, `"authenticated":true`) {
				t.Fatalf("pending WAL upgrade authenticate = %d: %s", status, body)
			}

			target := bootstrap.FixedBootstrapAPIPath + "installation/upgrade/status"
			status, body = request(http.MethodGet, target, "Bearer "+legacyKey)
			if status != http.StatusOK ||
				!strings.Contains(body, `"recorded":false`) ||
				!strings.Contains(body, `"evidence":"none"`) {
				t.Fatalf("pending WAL upgrade status = %d: %s", status, body)
			}

			if after := readSourceTree(t, dir); !reflect.DeepEqual(before, after) {
				t.Fatalf("the read-only Upgrade entry mutated the source tree:\nbefore=%v\nafter=%v", before, after)
			}
			if mount.removeSHM {
				if _, err := os.Lstat(paths.Database + "-shm"); !os.IsNotExist(err) {
					t.Fatalf("the read-only Upgrade entry created usage.sqlite-shm (err=%v)", err)
				}
			}
		})
	}
}

// nativeV2StoreWithUnreadableMarker builds a native v2 WAL store whose ownership
// marker this process may not open: the shape a root member leaves behind after
// running the Manager as 0:0 on the v2 image.
func nativeV2StoreWithUnreadableMarker(t *testing.T) (string, sqliterepo.InspectionPaths) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses 0000 file permissions")
	}
	dir := t.TempDir()
	database := filepath.Join(dir, "usage.sqlite")
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatalf("open native v2 store: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close native v2 store: %v", err)
	}
	marker, err := processlock.Acquire(database)
	if err != nil {
		t.Fatalf("create the native v2 marker: %v", err)
	}
	if err := marker.Close(); err != nil {
		t.Fatalf("release the native v2 marker: %v", err)
	}
	return dir, legacypreflight.DefaultPaths(database)
}

// TestNativeV2StoreWithUnreadableMarkerStaysUnavailable is the fail-closed half
// of the ownership classification: a marker this process may not open looks the
// same for a root-owned 1.x volume and for a native v2 store, so only the
// database may decide. A v2 store is never reported as an upgradable 1.x source.
func TestNativeV2StoreWithUnreadableMarkerStaysUnavailable(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	dir, paths := nativeV2StoreWithUnreadableMarker(t)
	lockPath := paths.Database + ".manager.lock"
	before := readSourceTree(t, dir)
	if err := os.Chmod(lockPath, 0o000); err != nil {
		t.Fatalf("make the native v2 marker unreadable: %v", err)
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
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("native v2 store with an unreadable marker = %d, want 503: %s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), `"reason":"source_ownership_unavailable"`) {
		t.Fatalf("a native v2 store was presented as an upgradable 1.x source: %s", raw)
	}

	if err := os.Chmod(lockPath, 0o600); err != nil {
		t.Fatalf("restore the native v2 marker: %v", err)
	}
	if after := readSourceTree(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("the ownership classification mutated the native v2 store")
	}
}

// TestConcurrentLegacyDiscoveryObservationsDoNotSelfContend is the coalescing
// contract: the unauthenticated discovery path is reached by several callers at
// once, and the ownership fence one observation holds must never be what refuses
// the next one.
func TestConcurrentLegacyDiscoveryObservationsDoNotSelfContend(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	dir, paths := legacyWALVolumeWithPendingWAL(t, "concurrent-legacy-key-123456")
	before := readSourceTree(t, dir)

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
	server := httptest.NewServer(h)
	defer server.Close()

	const requests = 10
	codes := make([]int, requests)
	bodies := make([]string, requests)
	failures := make([]error, requests)
	var wait sync.WaitGroup
	wait.Add(requests)
	for index := 0; index < requests; index++ {
		go func(index int) {
			defer wait.Done()
			resp, err := server.Client().Get(server.URL + bootstrapdiscovery.Path)
			if err != nil {
				failures[index] = err
				return
			}
			defer resp.Body.Close()
			raw, err := io.ReadAll(resp.Body)
			codes[index], bodies[index] = resp.StatusCode, string(raw)
			if err != nil {
				failures[index] = err
			}
		}(index)
	}
	wait.Wait()

	for index := 0; index < requests; index++ {
		if failures[index] != nil {
			t.Fatalf("concurrent discovery %d: %v", index, failures[index])
		}
		if codes[index] != http.StatusOK || !strings.Contains(bodies[index], `"status":"unsupported_upgrade"`) {
			t.Fatalf("concurrent discovery %d = %d: %s", index, codes[index], bodies[index])
		}
	}
	if after := readSourceTree(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("concurrent discovery mutated the source tree")
	}
}

// TestAdmitStartupReadsAPendingWALSourceWithoutTouchingIt is the startup half of
// the same invariant: the pre-classification operation read runs before anything
// knows whose store this is, so it must answer for a 1.x volume with a pending
// journal without creating usage.sqlite-shm or rewriting a byte.
func TestAdmitStartupReadsAPendingWALSourceWithoutTouchingIt(t *testing.T) {
	dir, paths := legacyWALVolumeWithPendingWAL(t, "pending-wal-admission-key-123456")
	if err := os.Remove(paths.Database + "-shm"); err != nil {
		t.Fatalf("drop the stale -shm: %v", err)
	}
	before := readSourceTree(t, dir)

	if _, err := legacypreflight.AdmitStartup(t.Context(), paths, legacypreflight.Options{}); err == nil {
		t.Fatalf("a stopped 1.x volume with a pending journal must not be admitted")
	}

	if _, err := os.Lstat(paths.Database + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("startup admission created usage.sqlite-shm (err=%v)", err)
	}
	if after := readSourceTree(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("startup admission mutated the source tree:\nbefore=%v\nafter=%v", before, after)
	}
}

// discoveryMarkerState is the ownership marker as the discovery cache sees it:
// permission bits plus the POSIX change stamp, which a chmod refreshes.
func discoveryMarkerState(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat the ownership marker: %v", err)
	}
	stamp, err := copyinmarker.ChangeStamp(path)
	if err != nil {
		t.Fatalf("change stamp of the ownership marker: %v", err)
	}
	return fmt.Sprintf("%s:%d:%s", info.Mode(), info.Size(), stamp)
}

// TestRepeatedDiscoveryObservationsAreServedFromTheCache is the polling
// contract on a writable volume: three identical GETs must be answered by one
// observation, and the source — including the ownership marker the cache is
// keyed on — must not be rewritten by any of them. A method other than GET is
// refused before the source is read at all.
func TestRepeatedDiscoveryObservationsAreServedFromTheCache(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	dir, paths := legacyWALVolumeWithPendingWAL(t, "cached-legacy-key-123456")
	before := readSourceTree(t, dir)
	locked := paths.Database + ".manager.lock"
	markerBefore := discoveryMarkerState(t, locked)

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
	server := httptest.NewServer(h)
	defer server.Close()

	request := func(method string) (int, string) {
		t.Helper()
		resp, err := server.Client().Do(mustRequest(t, method, server.URL+bootstrapdiscovery.Path))
		if err != nil {
			t.Fatalf("%s discovery: %v", method, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read %s discovery: %v", method, err)
		}
		return resp.StatusCode, string(body)
	}

	for poll := 0; poll < 3; poll++ {
		if status, body := request(http.MethodGet); status != http.StatusOK ||
			!strings.Contains(body, `"status":"unsupported_upgrade"`) {
			t.Fatalf("poll %d discovery = %d: %s", poll, status, body)
		}
	}
	if ran := h.discoveryObserver.observations(); ran != 1 {
		t.Fatalf("%d observations for three identical GETs, want 1: the cache never warmed", ran)
	}

	if status, _ := request(http.MethodPost); status != http.StatusMethodNotAllowed {
		t.Fatalf("POST discovery = %d, want 405", status)
	}
	if ran := h.discoveryObserver.observations(); ran != 1 {
		t.Fatalf("refusing a POST ran an observation (%d total)", ran)
	}

	if markerAfter := discoveryMarkerState(t, locked); markerAfter != markerBefore {
		t.Fatalf("discovery rewrote the ownership marker: %s -> %s", markerBefore, markerAfter)
	}
	if after := readSourceTree(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("discovery mutated the source tree")
	}
}

func mustRequest(t *testing.T, method string, target string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	return req
}

// TestAdmitStartupReadsAPendingOperationThroughAPrivateCopy pins the branch that
// reads an operation recorded only in a journal: the table is in the database
// file, the row is not, and the answer must still name the unfinished operation
// without creating usage.sqlite-shm or rewriting the store.
func TestAdmitStartupReadsAPendingOperationThroughAPrivateCopy(t *testing.T) {
	dir, paths := nativeV2StoreWithPendingOperation(t)
	if err := os.Remove(paths.Database + "-shm"); err != nil {
		t.Fatalf("drop the stale -shm: %v", err)
	}
	before := readSourceTree(t, dir)

	admission, err := legacypreflight.AdmitStartup(t.Context(), paths, legacypreflight.Options{
		UpgradeAdmission: legacypreflight.DatabaseUpgradeAdmission{DatabasePath: paths.Database},
	})
	if err != nil {
		t.Fatalf("admit a store with a recorded operation in its journal: %v", err)
	}
	if admission.UpgradeStage != legacypreflight.UpgradeStageInProgress {
		t.Fatalf("admitted stage = %q, want the operation recorded only in the journal", admission.UpgradeStage)
	}
	if err := admission.Close(); err != nil {
		t.Fatalf("release the admission: %v", err)
	}

	if _, err := os.Lstat(paths.Database + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("startup admission created usage.sqlite-shm (err=%v)", err)
	}
	if after := readSourceTree(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("startup admission mutated the store:\nbefore=%v\nafter=%v", before, after)
	}
}

// nativeV2StoreWithPendingOperation builds a native v2 store whose single
// Upgrade operation row exists only in a pending journal: the schema is
// checkpointed, the row is written with autocheckpoint off, and the writer exits
// without closing.
func nativeV2StoreWithPendingOperation(t *testing.T) (string, sqliterepo.InspectionPaths) {
	t.Helper()
	dir := t.TempDir()
	database := filepath.Join(dir, "usage.sqlite")
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeV2PendingOperationFixtureProcess$")
	cmd.Env = append(os.Environ(), "CPAMP_V2_PENDING_OPERATION="+database)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native v2 pending operation fixture: %v %s", err, output)
	}
	wal, err := os.Lstat(database + "-wal")
	if err != nil || wal.Size() == 0 {
		t.Fatalf("the fixture did not keep a pending journal (err=%v)", err)
	}
	paths := legacypreflight.DefaultPaths(database)
	if err := os.WriteFile(paths.DataKey, []byte("v2-data-key-bytes-preserved"), 0o600); err != nil {
		t.Fatalf("write v2 data.key: %v", err)
	}
	if err := os.WriteFile(database+".manager.lock", nil, 0o600); err != nil {
		t.Fatalf("write v2 lock marker: %v", err)
	}
	return dir, paths
}

// TestNativeV2PendingOperationFixtureProcess is the seeding half of
// nativeV2StoreWithPendingOperation: the schema is checkpointed by a clean close,
// the operation row is committed with autocheckpoint off, and the process exits
// without closing, so the row is still in the journal.
func TestNativeV2PendingOperationFixtureProcess(t *testing.T) {
	database := os.Getenv("CPAMP_V2_PENDING_OPERATION")
	if database == "" {
		t.Skip("helper process")
	}
	seed, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatalf("create the native v2 schema: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("checkpoint the native v2 schema: %v", err)
	}
	wal, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatalf("open the pending journal fixture: %v", err)
	}
	for _, stmt := range []string{
		`pragma journal_mode = wal`,
		`pragma wal_autocheckpoint = 0`,
		`insert into upgrade_operations (id, stage, created_at_ms, updated_at_ms) values (1, 'reconcile', 1, 1)`,
	} {
		if _, err := wal.Exec(stmt); err != nil {
			t.Fatalf("seed the pending operation: %v", err)
		}
	}
	os.Exit(0)
}
