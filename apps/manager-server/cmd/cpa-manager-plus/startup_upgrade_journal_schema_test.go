package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradeauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

// journalOnlyOperationStore is a native-v2 store whose operation table no longer
// exists in the database file: the checkpoint that would publish it never ran, so
// only the journal names it. That is the state a v2 store left behind when it
// created the table, recorded the operation and lost its process before any
// checkpoint: a file-only lookup says "no operation recorded", and acting on that
// answer would admit the store — a complete v2 schema with an unfinished Upgrade —
// for writable startup.
//
// The fixture renames the table away and back again rather than re-creating it, so
// the shape on disk is the migration's own and the pending rename is one ordinary
// journal transaction. The writer exits without closing, which is what keeps the
// transaction in the journal.
func journalOnlyOperationStore(t *testing.T, stage string, adminKey string) (string, sqliterepo.InspectionPaths) {
	t.Helper()
	dir := t.TempDir()
	database := filepath.Join(dir, "usage.sqlite")
	cmd := exec.Command(os.Args[0], "-test.run=^TestJournalOnlyOperationStoreFixtureProcess$")
	cmd.Env = append(os.Environ(),
		"CPAMP_JOURNAL_ONLY_OPERATION="+database,
		"CPAMP_JOURNAL_ONLY_STAGE="+stage,
		"CPAMP_JOURNAL_ONLY_ADMIN="+adminKey,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("journal-only operation fixture: %v %s", err, output)
	}
	wal, err := os.Lstat(database + "-wal")
	if err != nil || wal.Size() == 0 {
		t.Fatalf("the fixture did not keep a pending journal (err=%v)", err)
	}
	// A writer that lost its process leaves an index nobody can trust; the read
	// must rebuild it from the journal inside its own copy, so the source carries
	// no index at all.
	if err := os.Remove(database + "-shm"); err != nil && !os.IsNotExist(err) {
		t.Fatalf("drop the stale -shm: %v", err)
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

// TestJournalOnlyOperationStoreFixtureProcess is the seeding half of
// journalOnlyOperationStore.
func TestJournalOnlyOperationStoreFixtureProcess(t *testing.T) {
	database := os.Getenv("CPAMP_JOURNAL_ONLY_OPERATION")
	if database == "" {
		t.Skip("helper process")
	}
	stage := model.UpgradeStage(os.Getenv("CPAMP_JOURNAL_ONLY_STAGE"))
	seed, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatalf("create the native v2 schema: %v", err)
	}
	if adminKey := os.Getenv("CPAMP_JOURNAL_ONLY_ADMIN"); adminKey != "" {
		credential, credentialErr := security.NewAdminCredential(adminKey, "test")
		if credentialErr != nil {
			t.Fatalf("mint the administrator credential: %v", credentialErr)
		}
		if saveErr := setting.New(seed).SaveAdminCredential(context.Background(), credential); saveErr != nil {
			t.Fatalf("store the administrator credential: %v", saveErr)
		}
	}
	if err := upgradeoperation.New(seed).Put(context.Background(), model.UpgradeOperation{
		Stage:                   stage,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy},
		SourceKind:              "upgraded-legacy",
		SourceVersion:           "1.14.10",
		SourceFingerprintBefore: strings.Repeat("c", 64),
		SourceFingerprintAfter:  strings.Repeat("d", 64),
		BackupManifestSHA256:    strings.Repeat("e", 64),
		ManualItems:             []string{},
	}); err != nil {
		t.Fatalf("record the operation: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("checkpoint the native v2 schema: %v", err)
	}
	journal, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatalf("open the pending journal fixture: %v", err)
	}
	for _, stmt := range []string{
		`pragma journal_mode = wal`,
		// Publish the parked rename and empty the journal, so the file alone names
		// no operation table at all.
		`alter table upgrade_operations rename to upgrade_operations_parked`,
		`pragma wal_checkpoint(truncate)`,
		// From here nothing is copied back into the file: the rename below — with
		// the row it makes visible again — stays in the journal.
		`pragma wal_autocheckpoint = 0`,
		`alter table upgrade_operations_parked rename to upgrade_operations`,
	} {
		if _, err := journal.Exec(stmt); err != nil {
			t.Fatalf("seed the journal-only operation table: %v", err)
		}
	}
	var inFile int
	if err := journal.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = 'upgrade_operations'`).Scan(&inFile); err != nil {
		t.Fatalf("count the visible operation table: %v", err)
	}
	// The writer's own handle sees the table; the assertion that matters is made
	// after the process is gone, from an immutable read of the file alone.
	os.Exit(0)
}

// TestAdmitStartupTreatsAJournalOnlyOperationTableAsRecorded pins the admission
// half of the journal rule: a store whose table and row are still in its journal
// must be admitted for the Upgrade surface only. Answering "no operation" from the
// database file alone would admit a complete v2 store with an unfinished Upgrade
// for every writable surface.
func TestAdmitStartupTreatsAJournalOnlyOperationTableAsRecorded(t *testing.T) {
	dir, paths := journalOnlyOperationStore(t, string(model.UpgradeStageReconcile), "")
	// The premise: the database file alone names no operation table.
	if present, err := legacypreflight.RecordedOperationTablePresent(t.Context(), paths.Database); err != nil || present {
		t.Fatalf("file-only operation table present=%v err=%v; the fixture is not journal-only", present, err)
	}
	before := readSourceTree(t, dir)

	admission, err := legacypreflight.AdmitStartup(t.Context(), paths, legacypreflight.Options{
		UpgradeAdmission: legacypreflight.DatabaseUpgradeAdmission{DatabasePath: paths.Database},
	})
	if err != nil {
		t.Fatalf("admit a store whose operation table is in its journal: %v", err)
	}
	defer func() { _ = admission.Close() }()
	if admission.UpgradeStage != legacypreflight.UpgradeStageInProgress {
		t.Fatalf("admitted stage = %q, want the operation recorded in the journal", admission.UpgradeStage)
	}
	if admission.Surface() != legacypreflight.SurfaceUpgradeOnly {
		t.Fatalf("admitted surface = %q, want the Upgrade surface only", admission.Surface())
	}
	if after := readSourceTree(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("reading the operation record mutated the source:\nbefore=%v\nafter=%v", before, after)
	}
}

// TestUpgradeStatusReadsAJournalOnlyOperationTable pins the status half of the
// same rule: the read-only Upgrade surface must report the recorded operation an
// operator would otherwise be told to start a preflight for.
func TestUpgradeStatusReadsAJournalOnlyOperationTable(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	const adminKey = "journal-only-operation-status-key"
	dir, paths := journalOnlyOperationStore(t, string(model.UpgradeStageReconcile), adminKey)
	before := readSourceTree(t, dir)

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
	server := httptest.NewServer(h)
	defer server.Close()

	target := server.URL + bootstrap.FixedBootstrapAPIPath + "installation/upgrade/status"
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("build the status request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminKey)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET the upgrade status: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d: %s", response.StatusCode, body)
	}
	if !strings.Contains(string(body), `"recorded":true`) ||
		!strings.Contains(string(body), `"stage":"`+string(model.UpgradeStageReconcile)+`"`) {
		t.Fatalf("status = %s, want the operation recorded in the journal", body)
	}
	if after := readSourceTree(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("the status read mutated the source:\nbefore=%v\nafter=%v", before, after)
	}
}

// TestRefusedAdmissionLeavesTheSourceMarkerUntouched pins the refusal path: the
// ownership fence an admission takes must not rewrite the marker of a source the
// admission then refuses. A 1.x volume the operator only mounted is preserved
// byte for byte, and its marker's mode and change stamp are part of that promise —
// the observation cache is keyed on them, and a chown is exactly the repair the
// guidance asks the operator to make.
func TestRefusedAdmissionLeavesTheSourceMarkerUntouched(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	dir, paths := legacyWALSourceFixture(t, "refused-admission-key-123456")
	marker := paths.Database + ".manager.lock"
	beforeMarker := discoveryMarkerState(t, marker)
	beforeTree := readSourceTree(t, dir)

	if admission, err := legacypreflight.AdmitStartup(t.Context(), paths, legacypreflight.Options{}); err == nil {
		_ = admission.Close()
		t.Fatal("admitted a 1.x source for writable startup")
	}

	if afterMarker := discoveryMarkerState(t, marker); afterMarker != beforeMarker {
		t.Fatalf("refused admission rewrote the ownership marker: %s -> %s", beforeMarker, afterMarker)
	}
	if afterTree := readSourceTree(t, dir); !reflect.DeepEqual(beforeTree, afterTree) {
		t.Fatalf("refused admission mutated the source:\nbefore=%v\nafter=%v", beforeTree, afterTree)
	}
}

// TestConcurrentFailingObservationsShareOneFlight enforces that concurrent
// public discovery HTTP requests against a failing source coalesce onto one
// observation flight without sleeping, return 503 to every caller in that
// flight, and do not remember the failed result for subsequent requests.
func TestConcurrentFailingObservationsShareOneFlight(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	_, paths := legacyWALVolumeWithPendingWAL(t, "failing-flight-key-123456")
	walPath := paths.Database + "-wal"
	walBytes, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatalf("read WAL: %v", err)
	}
	walBytes[0] ^= 0xff
	if err := os.WriteFile(walPath, walBytes, 0o600); err != nil {
		t.Fatalf("write corrupt WAL header: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce sync.Once
	origBefore := beforeDiscoveryObservation
	beforeDiscoveryObservation = func() {
		enterOnce.Do(func() {
			close(entered)
			<-release
		})
	}
	defer func() { beforeDiscoveryObservation = origBefore }()

	var joiners atomic.Int32
	allJoined := make(chan struct{})
	origJoin := afterDiscoveryFlightJoin
	afterDiscoveryFlightJoin = func() {
		if joiners.Add(1) == 2 {
			close(allJoined)
		}
	}
	defer func() { afterDiscoveryFlightJoin = origJoin }()

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
	defer h.BeginWritableStartup()

	statuses := make([]int, 3)
	bodies := make([]string, 3)
	var wg sync.WaitGroup
	callDiscovery := func(idx int) {
		defer wg.Done()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil)
		h.ServeHTTP(rec, req)
		statuses[idx] = rec.Code
		bodies[idx] = rec.Body.String()
	}

	wg.Add(1)
	go callDiscovery(0)
	<-entered

	wg.Add(2)
	go callDiscovery(1)
	go callDiscovery(2)
	<-allJoined

	close(release)
	wg.Wait()

	if ran := h.discoveryObserver.observations(); ran != 1 {
		t.Fatalf("observations for 3 concurrent requests = %d, want 1", ran)
	}
	for idx, status := range statuses {
		if status != http.StatusServiceUnavailable || !strings.Contains(bodies[idx], `"status":"unavailable"`) {
			t.Fatalf("request %d = %d (%s), want 503 unavailable", idx, status, bodies[idx])
		}
	}

	// A failed observation is never remembered across flights: a subsequent
	// request executes a fresh observation instead of hitting a cached failure.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("follow-up request = %d, want 503", rec.Code)
	}
	if ran := h.discoveryObserver.observations(); ran != 2 {
		t.Fatalf("observations after follow-up request = %d, want 2", ran)
	}
}

// TestCachedProjectionIsDroppedWhenTheProbeCannotProveQuiescence warms the
// public discovery cache through a real HTTP GET on a 1.x source, proves a
// quiet source serves the remembered 200 without re-observing, and proves a
// probe failure returns 503 and invalidates the cached projection so a later
// quiet request must re-observe.
func TestCachedProjectionIsDroppedWhenTheProbeCannotProveQuiescence(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	_, paths := legacyWALSourceFixture(t, "cache-quiescence-key-123456")

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
	defer h.BeginWritableStartup()

	getDiscovery := func() (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
		return rec.Code, rec.Body.String()
	}

	// Warm the cache naturally via HTTP.
	if code, body := getDiscovery(); code != http.StatusOK || !strings.Contains(body, `"status":"unsupported_upgrade"`) {
		t.Fatalf("initial discovery = %d (%s), want 200 unsupported_upgrade", code, body)
	}
	if ran := h.discoveryObserver.observations(); ran != 1 {
		t.Fatalf("observations after initial GET = %d, want 1", ran)
	}
	// Positive control: second GET hits the warmed cache without re-observing.
	if code, body := getDiscovery(); code != http.StatusOK || !strings.Contains(body, `"status":"unsupported_upgrade"`) {
		t.Fatalf("cached discovery = %d (%s), want 200 unsupported_upgrade", code, body)
	}
	if ran := h.discoveryObserver.observations(); ran != 1 {
		t.Fatalf("observations after cached GET = %d, want 1", ran)
	}

	origProbe := probeSource
	probeSource = func(string) error { return errors.New("marker unreadable") }
	code, body := getDiscovery()
	probeSource = origProbe
	if code != http.StatusServiceUnavailable || !strings.Contains(body, `"status":"unavailable"`) {
		t.Fatalf("discovery with failed probe = %d (%s), want 503 unavailable", code, body)
	}
	if ran := h.discoveryObserver.observations(); ran != 1 {
		t.Fatalf("observations after failed probe = %d, want 1 (no new observation on unprovable probe)", ran)
	}

	// Because the cached entry was dropped on the probe failure, the next GET
	// after probe recovery must run a fresh observation (observations == 2).
	if code, body := getDiscovery(); code != http.StatusOK || !strings.Contains(body, `"status":"unsupported_upgrade"`) {
		t.Fatalf("discovery after probe recovery = %d (%s), want 200 unsupported_upgrade", code, body)
	}
	if ran := h.discoveryObserver.observations(); ran != 2 {
		t.Fatalf("observations after probe recovery = %d, want 2 (dropped cache entry must re-observe)", ran)
	}
}

// TestCanceledRequestIsNotAnsweredFromTheCache warms the public discovery cache
// via HTTP on a real 1.x source and verifies that a request canceled during
// cache revalidation receives fail-closed 503 without triggering an observation,
// while subsequent uncanceled requests continue to hit the warmed cache.
func TestCanceledRequestIsNotAnsweredFromTheCache(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	_, paths := legacyWALSourceFixture(t, "cache-cancel-key-123456")

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
	defer h.BeginWritableStartup()

	warmRec := httptest.NewRecorder()
	h.ServeHTTP(warmRec, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
	if warmRec.Code != http.StatusOK || !strings.Contains(warmRec.Body.String(), `"status":"unsupported_upgrade"`) {
		t.Fatalf("warm discovery = %d (%s), want 200 unsupported_upgrade", warmRec.Code, warmRec.Body.String())
	}
	if ran := h.discoveryObserver.observations(); ran != 1 {
		t.Fatalf("observations after warm GET = %d, want 1", ran)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origAfter := afterCacheRevalidation
	afterCacheRevalidation = cancel
	cancelRec := httptest.NewRecorder()
	cancelReq := httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil).WithContext(ctx)
	h.ServeHTTP(cancelRec, cancelReq)
	afterCacheRevalidation = origAfter

	if cancelRec.Code != http.StatusServiceUnavailable || !strings.Contains(cancelRec.Body.String(), `"status":"unavailable"`) {
		t.Fatalf("canceled cached GET = %d (%s), want 503 unavailable", cancelRec.Code, cancelRec.Body.String())
	}
	if ran := h.discoveryObserver.observations(); ran != 1 {
		t.Fatalf("observations after canceled cached GET = %d, want 1", ran)
	}

	followRec := httptest.NewRecorder()
	h.ServeHTTP(followRec, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
	if followRec.Code != http.StatusOK || !strings.Contains(followRec.Body.String(), `"status":"unsupported_upgrade"`) {
		t.Fatalf("uncanceled follow-up GET = %d (%s), want 200 unsupported_upgrade", followRec.Code, followRec.Body.String())
	}
	if ran := h.discoveryObserver.observations(); ran != 1 {
		t.Fatalf("observations after uncanceled follow-up GET = %d, want 1", ran)
	}
}

// TestCancelingDiscoveryFlightLeaderDoesNotFailConcurrentWaiter proves the
// shared singleflight observation runs under a server-bounded context detached
// from the first caller's request context: canceling client A mid-observation
// returns fail-closed 503 to A immediately while concurrent waiter B receives
// the valid 200 projection from the same single observation.
func TestCancelingDiscoveryFlightLeaderDoesNotFailConcurrentWaiter(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	_, paths := legacyWALSourceFixture(t, "detached-flight-legacy-key")
	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
	defer h.BeginWritableStartup()

	entered := make(chan struct{})
	release := make(chan struct{})
	joined := make(chan struct{})
	prevBefore := beforeDiscoveryObservation
	prevJoin := afterDiscoveryFlightJoin
	var enteredOnce, joinedOnce sync.Once
	beforeDiscoveryObservation = func() {
		enteredOnce.Do(func() { close(entered) })
		<-release
	}
	afterDiscoveryFlightJoin = func() {
		joinedOnce.Do(func() { close(joined) })
	}
	defer func() {
		beforeDiscoveryObservation = prevBefore
		afterDiscoveryFlightJoin = prevJoin
	}()

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	recA := httptest.NewRecorder()
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		reqA := httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil).WithContext(ctxA)
		h.ServeHTTP(recA, reqA)
	}()
	<-entered

	recB := httptest.NewRecorder()
	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		reqB := httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil)
		h.ServeHTTP(recB, reqB)
	}()
	<-joined

	cancelA()
	<-doneA
	if recA.Code != http.StatusServiceUnavailable || !strings.Contains(recA.Body.String(), `"status":"unavailable"`) {
		close(release)
		<-doneB
		t.Fatalf("canceled leader status=%d body=%s, want 503 unavailable", recA.Code, recA.Body.String())
	}

	close(release)
	<-doneB
	if recB.Code != http.StatusOK || !strings.Contains(recB.Body.String(), `"status":"unsupported_upgrade"`) {
		t.Fatalf("concurrent waiter status=%d body=%s, want 200 unsupported_upgrade", recB.Code, recB.Body.String())
	}
	if ran := h.discoveryObserver.observations(); ran != 1 {
		t.Fatalf("observations = %d, want 1 shared flight", ran)
	}
}

// TestJournalOnlyOperationStoreFailsClosedOnCorruptWALHeaderAndPendingRollbackJournal
// verifies that neither a corrupt WAL header nor a non-empty rollback journal
// silently falls back to main-database pages on startup admission, status, or
// public discovery.
func TestJournalOnlyOperationStoreFailsClosedOnCorruptWALHeaderAndPendingRollbackJournal(t *testing.T) {
	t.Run("corrupt-wal-header-on-journal-only-operation", func(t *testing.T) {
		t.Setenv(legacyRootEnvironment, "")
		const adminKey = "corrupt-wal-header-admin-key"
		dir, paths := journalOnlyOperationStore(t, string(model.UpgradeStageReconcile), adminKey)
		walPath := paths.Database + "-wal"
		walBytes, err := os.ReadFile(walPath)
		if err != nil {
			t.Fatalf("read WAL: %v", err)
		}
		walBytes[0] ^= 0xff
		if err := os.WriteFile(walPath, walBytes, 0o600); err != nil {
			t.Fatalf("write corrupt WAL header: %v", err)
		}
		markerPath := paths.Database + ".manager.lock"
		if err := os.Chmod(markerPath, 0o644); err != nil {
			t.Fatalf("chmod marker: %v", err)
		}
		beforeMarker := discoveryMarkerState(t, markerPath)
		beforeTree := readSourceTree(t, dir)

		if stage, err := (legacypreflight.DatabaseUpgradeAdmission{DatabasePath: paths.Database}).Admitted(t.Context()); err == nil {
			t.Fatalf("Admitted succeeded with corrupt WAL header: stage=%q", stage)
		}
		if admission, err := legacypreflight.AdmitStartup(t.Context(), paths, legacypreflight.Options{
			UpgradeAdmission: legacypreflight.DatabaseUpgradeAdmission{DatabasePath: paths.Database},
		}); err == nil {
			_ = admission.Close()
			t.Fatal("AdmitStartup admitted a journal-only operation store with a corrupt WAL header")
		}
		if afterMarker := discoveryMarkerState(t, markerPath); afterMarker != beforeMarker {
			t.Fatalf("refused admission rewrote marker: %s -> %s", beforeMarker, afterMarker)
		}

		h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
		defer h.BeginWritableStartup()
		server := httptest.NewServer(h)
		defer server.Close()

		target := server.URL + bootstrap.FixedBootstrapAPIPath + "installation/upgrade/status"
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+adminKey)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("GET status with corrupt WAL header = %d (%s), want 503", resp.StatusCode, body)
		}
		if afterTree := readSourceTree(t, dir); !reflect.DeepEqual(beforeTree, afterTree) {
			t.Fatalf("corrupt WAL status read mutated source")
		}
	})

	t.Run("nonempty-rollback-journal", func(t *testing.T) {
		t.Setenv(legacyRootEnvironment, "")
		const adminKey = "pending-rollback-journal-key"
		dir, paths := legacyWALSourceFixture(t, adminKey)
		// Remove WAL/SHM and plant a non-empty rollback journal beside the database.
		for _, suffix := range []string{"-wal", "-shm"} {
			_ = os.Remove(paths.Database + suffix)
		}
		if err := os.WriteFile(paths.Database+"-journal", []byte("pending-rollback-journal-frames"), 0o600); err != nil {
			t.Fatalf("write rollback journal: %v", err)
		}
		beforeTree := readSourceTree(t, dir)

		if stage, err := (legacypreflight.DatabaseUpgradeAdmission{DatabasePath: paths.Database}).Admitted(t.Context()); err == nil {
			t.Fatalf("Admitted succeeded with non-empty rollback journal: stage=%q", stage)
		}
		if admission, err := legacypreflight.AdmitStartup(t.Context(), paths, legacypreflight.Options{
			UpgradeAdmission: legacypreflight.DatabaseUpgradeAdmission{DatabasePath: paths.Database},
		}); err == nil {
			_ = admission.Close()
			t.Fatal("AdmitStartup succeeded with non-empty rollback journal")
		}

		h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
		defer h.BeginWritableStartup()
		server := httptest.NewServer(h)
		defer server.Close()

		discResp, err := server.Client().Get(server.URL + bootstrapdiscovery.Path)
		if err != nil {
			t.Fatal(err)
		}
		_ = discResp.Body.Close()
		if discResp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("GET discovery with non-empty rollback journal = %d, want 503", discResp.StatusCode)
		}

		statusTarget := server.URL + bootstrap.FixedBootstrapAPIPath + "installation/upgrade/status"
		req, err := http.NewRequest(http.MethodGet, statusTarget, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+adminKey)
		statusResp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = statusResp.Body.Close()
		if statusResp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("GET status with non-empty rollback journal = %d, want 503", statusResp.StatusCode)
		}
		if afterTree := readSourceTree(t, dir); !reflect.DeepEqual(beforeTree, afterTree) {
			t.Fatalf("non-empty rollback journal checks mutated source")
		}
	})
}

// walOnlyMutationOnLegacyStore writes an uncheckpointed WAL transaction on top
// of a 1.x main database file and exits without checkpointing, so the main
// database file alone still looks like an intact 1.x store while the committed
// state in the WAL contains v2-only schema or a Fresh operation marker.
func walOnlyMutationOnLegacyStore(t *testing.T, mode string) (string, sqliterepo.InspectionPaths) {
	t.Helper()
	dir, paths := legacyWALSourceFixture(t, "wal-only-mutation-key-123456")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWALOnlyMutationOnLegacyStoreFixtureProcess$")
	cmd.Env = append(os.Environ(),
		"CPAMP_WAL_ONLY_LEGACY_DB="+paths.Database,
		"CPAMP_WAL_ONLY_LEGACY_MODE="+mode,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("wal-only mutation fixture (%s): %v %s", mode, err, out)
	}
	walInfo, err := os.Lstat(paths.Database + "-wal")
	if err != nil || walInfo.Size() == 0 {
		t.Fatalf("expected non-empty WAL for %s (err=%v)", mode, err)
	}
	_ = os.Remove(paths.Database + "-shm")
	return dir, paths
}

func TestWALOnlyMutationOnLegacyStoreFixtureProcess(t *testing.T) {
	database := os.Getenv("CPAMP_WAL_ONLY_LEGACY_DB")
	if database == "" {
		return
	}
	mode := os.Getenv("CPAMP_WAL_ONLY_LEGACY_MODE")
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA wal_autocheckpoint = 0`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	switch mode {
	case "legacy-event":
		if _, err := db.Exec(`INSERT INTO usage_events (total_tokens) VALUES (99)`); err != nil {
			t.Fatalf("insert WAL-only legacy usage event: %v", err)
		}
	case "v2-table":
		if _, err := db.Exec(`CREATE TABLE upgrade_operations (id INTEGER PRIMARY KEY, operation_id TEXT NOT NULL)`); err != nil {
			t.Fatalf("create WAL-only v2 table: %v", err)
		}
	case "fresh-marker":
		if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('fresh_operation_v1', '{"state":"pending"}')`); err != nil {
			t.Fatalf("insert WAL-only fresh marker: %v", err)
		}
	default:
		t.Fatalf("unknown mode %q", mode)
	}
	os.Exit(0)
}

// TestUnreadableLockFailsClosedOnCorruptOrUncheckpointedV2WAL enforces that
// when .manager.lock returns EACCES, public discovery still validates non-empty
// WAL headers and replays valid WALs in a private snapshot rather than reading
// only the main database file with immutable=1, while leaving the source tree
// and marker ctime unchanged.
func TestUnreadableLockFailsClosedOnCorruptOrUncheckpointedV2WAL(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores 0000 file permissions")
	}

	t.Run("valid-1x-pending-wal-projects-ownership-recovery", func(t *testing.T) {
		t.Setenv(legacyRootEnvironment, "")
		dir, paths := legacyWALVolumeWithPendingWAL(t, "unreadable-lock-valid-wal-key")
		_ = os.Remove(paths.Database + "-shm")
		beforeTree := readSourceTree(t, dir)
		markerPath := paths.Database + ".manager.lock"
		if err := os.Chmod(markerPath, 0o000); err != nil {
			t.Fatalf("chmod marker: %v", err)
		}
		beforeMarker := discoveryMarkerState(t, markerPath)

		h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
		defer h.BeginWritableStartup()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"reason":"source_ownership_unavailable"`) {
			_ = os.Chmod(markerPath, 0o600)
			t.Fatalf("valid 1.x WAL with unreadable lock = %d (%s), want 200 source_ownership_unavailable", rec.Code, rec.Body.String())
		}
		if afterMarker := discoveryMarkerState(t, markerPath); afterMarker != beforeMarker {
			_ = os.Chmod(markerPath, 0o600)
			t.Fatalf("marker changed: %s -> %s", beforeMarker, afterMarker)
		}
		if err := os.Chmod(markerPath, 0o600); err != nil {
			t.Fatalf("restore marker mode: %v", err)
		}
		if afterTree := readSourceTree(t, dir); !reflect.DeepEqual(beforeTree, afterTree) {
			t.Fatalf("source tree changed on valid 1.x WAL inspection")
		}
	})

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) (string, sqliterepo.InspectionPaths)
	}{
		{
			name: "corrupt-wal-magic",
			setup: func(t *testing.T) (string, sqliterepo.InspectionPaths) {
				dir, paths := walOnlyMutationOnLegacyStore(t, "legacy-event")
				walPath := paths.Database + "-wal"
				walBytes, err := os.ReadFile(walPath)
				if err != nil {
					t.Fatal(err)
				}
				walBytes[0] ^= 0xff
				if err := os.WriteFile(walPath, walBytes, 0o644); err != nil {
					t.Fatal(err)
				}
				return dir, paths
			},
		},
		{
			name: "corrupt-wal-checksum",
			setup: func(t *testing.T) (string, sqliterepo.InspectionPaths) {
				dir, paths := walOnlyMutationOnLegacyStore(t, "legacy-event")
				walPath := paths.Database + "-wal"
				walBytes, err := os.ReadFile(walPath)
				if err != nil {
					t.Fatal(err)
				}
				walBytes[24] ^= 0xff
				if err := os.WriteFile(walPath, walBytes, 0o644); err != nil {
					t.Fatal(err)
				}
				return dir, paths
			},
		},
		{
			name: "truncated-wal-header",
			setup: func(t *testing.T) (string, sqliterepo.InspectionPaths) {
				dir, paths := walOnlyMutationOnLegacyStore(t, "legacy-event")
				if err := os.WriteFile(paths.Database+"-wal", []byte("short-wal-header"), 0o644); err != nil {
					t.Fatal(err)
				}
				return dir, paths
			},
		},
		{
			name: "unreadable-nonempty-wal",
			setup: func(t *testing.T) (string, sqliterepo.InspectionPaths) {
				return walOnlyMutationOnLegacyStore(t, "legacy-event")
			},
		},
		{
			name: "wal-only-v2-table",
			setup: func(t *testing.T) (string, sqliterepo.InspectionPaths) {
				dir, paths := walOnlyMutationOnLegacyStore(t, "v2-table")
				if present, err := legacypreflight.RecordedOperationTablePresent(t.Context(), paths.Database); err != nil || present {
					t.Fatalf("main file unexpectedly contains upgrade_operations (present=%v, err=%v)", present, err)
				}
				return dir, paths
			},
		},
		{
			name: "wal-only-fresh-marker",
			setup: func(t *testing.T) (string, sqliterepo.InspectionPaths) {
				return walOnlyMutationOnLegacyStore(t, "fresh-marker")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(legacyRootEnvironment, "")
			dir, paths := tc.setup(t)
			beforeTree := readSourceTree(t, dir)
			walPath := paths.Database + "-wal"
			var (
				beforeWALState string
				origWALMode    os.FileMode
			)
			if tc.name == "unreadable-nonempty-wal" {
				walInfo, err := os.Lstat(walPath)
				if err != nil {
					t.Fatalf("stat WAL: %v", err)
				}
				origWALMode = walInfo.Mode().Perm()
				if err := os.Chmod(walPath, 0o000); err != nil {
					t.Fatalf("chmod WAL: %v", err)
				}
				beforeWALState = discoveryMarkerState(t, walPath)
			}
			markerPath := paths.Database + ".manager.lock"
			markerInfo, err := os.Lstat(markerPath)
			if err != nil {
				t.Fatalf("stat marker: %v", err)
			}
			origMarkerMode := markerInfo.Mode().Perm()
			if err := os.Chmod(markerPath, 0o000); err != nil {
				t.Fatalf("chmod marker: %v", err)
			}
			beforeMarker := discoveryMarkerState(t, markerPath)

			h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
			h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
			defer h.BeginWritableStartup()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
			afterMarker := discoveryMarkerState(t, markerPath)
			var afterWALState string
			if tc.name == "unreadable-nonempty-wal" {
				afterWALState = discoveryMarkerState(t, walPath)
				if err := os.Chmod(walPath, origWALMode); err != nil {
					t.Fatalf("restore WAL mode: %v", err)
				}
			}
			if err := os.Chmod(markerPath, origMarkerMode); err != nil {
				t.Fatalf("restore marker mode: %v", err)
			}
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"status":"unavailable"`) {
				t.Fatalf("%s discovery = %d (%s), want 503 unavailable", tc.name, rec.Code, rec.Body.String())
			}
			if afterMarker != beforeMarker {
				t.Fatalf("%s rewrote marker: %s -> %s", tc.name, beforeMarker, afterMarker)
			}
			if tc.name == "unreadable-nonempty-wal" && afterWALState != beforeWALState {
				t.Fatalf("%s rewrote WAL metadata: %s -> %s", tc.name, beforeWALState, afterWALState)
			}
			if afterTree := readSourceTree(t, dir); !reflect.DeepEqual(beforeTree, afterTree) {
				t.Fatalf("%s mutated source tree", tc.name)
			}
		})
	}
}

// TestUnreadableLockFailsClosedWhenWALSourceChangesAfterSnapshotOpen enforces
// that on the unreadable-lock + non-empty WAL discovery path, if an external
// process modifies the source WAL or sidecars after the private snapshot has
// been copied and verified (and before classification returns), GET discovery
// fails closed with 503 unavailable instead of returning a stale 200
// source_ownership_unavailable verdict, while Manager never writes to the source.
func TestUnreadableLockFailsClosedWhenWALSourceChangesAfterSnapshotOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores 0000 file permissions")
	}

	_, v2DonorPaths := walOnlyMutationOnLegacyStore(t, "v2-table")
	v2WALBytes, err := os.ReadFile(v2DonorPaths.Database + "-wal")
	if err != nil || len(v2WALBytes) == 0 {
		t.Fatalf("read WAL-only v2 donor WAL (len=%d, err=%v)", len(v2WALBytes), err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, dbPath string)
	}{
		{
			name: "wal-switches-to-v2-schema-after-snapshot",
			mutate: func(t *testing.T, dbPath string) {
				if err := os.WriteFile(dbPath+"-wal", v2WALBytes, 0o644); err != nil {
					t.Fatalf("replace WAL with v2-table WAL: %v", err)
				}
			},
		},
		{
			name: "wal-rewritten-in-place-with-restored-mtime-after-snapshot",
			mutate: func(t *testing.T, dbPath string) {
				walPath := dbPath + "-wal"
				origStat, err := os.Lstat(walPath)
				if err != nil {
					t.Fatalf("stat WAL: %v", err)
				}
				f, err := os.OpenFile(walPath, os.O_RDWR, 0)
				if err != nil {
					t.Fatalf("open WAL in place: %v", err)
				}
				var b [1]byte
				if _, err := f.ReadAt(b[:], 0); err != nil {
					_ = f.Close()
					t.Fatalf("read WAL byte 0: %v", err)
				}
				b[0] ^= 0xff
				if _, err := f.WriteAt(b[:], 0); err != nil {
					_ = f.Close()
					t.Fatalf("rewrite WAL byte 0: %v", err)
				}
				if err := f.Close(); err != nil {
					t.Fatalf("close WAL: %v", err)
				}
				if err := os.Chtimes(walPath, origStat.ModTime(), origStat.ModTime()); err != nil {
					t.Fatalf("restore WAL mtime: %v", err)
				}
			},
		},
		{
			name: "pending-rollback-journal-created-after-snapshot",
			mutate: func(t *testing.T, dbPath string) {
				if err := os.WriteFile(dbPath+"-journal", []byte("pending-rollback-frames"), 0o644); err != nil {
					t.Fatalf("write rollback journal: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(legacyRootEnvironment, "")
			dir, paths := walOnlyMutationOnLegacyStore(t, "legacy-event")
			markerPath := paths.Database + ".manager.lock"
			markerInfo, err := os.Lstat(markerPath)
			if err != nil {
				t.Fatalf("stat marker: %v", err)
			}
			origMarkerMode := markerInfo.Mode().Perm()
			beforeTree := readSourceTree(t, dir)
			if err := os.Chmod(markerPath, 0o000); err != nil {
				t.Fatalf("chmod marker: %v", err)
			}
			beforeMarker := discoveryMarkerState(t, markerPath)

			snapshotExceptMarker := func() map[string]string {
				entries, readErr := os.ReadDir(dir)
				if readErr != nil {
					t.Errorf("readdir source: %v", readErr)
					return nil
				}
				out := make(map[string]string, len(entries))
				for _, entry := range entries {
					full := filepath.Join(dir, entry.Name())
					if full == markerPath {
						out[entry.Name()] = discoveryMarkerState(t, markerPath)
						continue
					}
					raw, fileErr := os.ReadFile(full)
					if fileErr != nil {
						t.Errorf("read %s: %v", full, fileErr)
						continue
					}
					out[entry.Name()] = discoveryMarkerState(t, full) + ":" + string(raw)
				}
				return out
			}
			beforeHookEntries := snapshotExceptMarker()

			var (
				hookFired           bool
				expectedPostEntries map[string]string
			)
			restore := upgradeservice.SetAfterUnfenceableSnapshotOpenForTest(func(dbPath string) {
				if dbPath != paths.Database {
					return
				}
				hookFired = true
				if gotEntries := snapshotExceptMarker(); !reflect.DeepEqual(beforeHookEntries, gotEntries) {
					t.Errorf("OpenSnapshot modified source volume before hook")
				}
				tc.mutate(t, dbPath)
				expectedPostEntries = snapshotExceptMarker()
			})
			defer restore()

			h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
			h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
			defer h.BeginWritableStartup()

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
			afterMarker := discoveryMarkerState(t, markerPath)
			afterEntries := snapshotExceptMarker()
			if err := os.Chmod(markerPath, origMarkerMode); err != nil {
				t.Fatalf("restore marker mode: %v", err)
			}
			afterTree := readSourceTree(t, dir)
			if !hookFired {
				t.Fatalf("%s did not traverse unfenceable WAL snapshot path", tc.name)
			}
			if rec.Code != http.StatusServiceUnavailable ||
				!strings.Contains(rec.Body.String(), `"status":"unavailable"`) ||
				!strings.Contains(rec.Body.String(), `"reason":"observation_unavailable"`) {
				t.Fatalf("%s GET discovery = %d (%s), want 503 unavailable/observation_unavailable", tc.name, rec.Code, rec.Body.String())
			}
			if afterMarker != beforeMarker {
				t.Fatalf("%s Manager touched marker: %s -> %s", tc.name, beforeMarker, afterMarker)
			}
			if !reflect.DeepEqual(expectedPostEntries, afterEntries) {
				t.Fatalf("%s Manager modified source volume after snapshot open", tc.name)
			}
			if beforeTree[filepath.Base(markerPath)] != afterTree[filepath.Base(markerPath)] {
				t.Fatalf("%s marker bytes changed", tc.name)
			}
		})
	}
}

// TestUpgradeAuthInvalidatesCachedCredentialWhenCorruptWALRestoresMtime proves
// at the HTTP boundary that corrupting the WAL in place and restoring its mtime
// invalidates the credential verification cache via ctime so neither upgrade
// authenticate nor upgrade status returns 200.
func TestUpgradeAuthInvalidatesCachedCredentialWhenCorruptWALRestoresMtime(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	const adminKey = "mtime-restored-wal-admin-key-01"
	dir, paths := legacyWALVolumeWithPendingWAL(t, adminKey)

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
	defer h.BeginWritableStartup()
	server := httptest.NewServer(h)
	defer server.Close()

	callRoute := func(method, route string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+route, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+adminKey)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(raw)
	}

	// Warm the credential cache over HTTP.
	if code, body := callRoute(http.MethodPost, upgradeauth.Path); code != http.StatusOK || !strings.Contains(body, `"authenticated":true`) {
		t.Fatalf("initial upgrade authenticate = %d (%s), want 200", code, body)
	}

	walPath := paths.Database + "-wal"
	origStat, err := os.Lstat(walPath)
	if err != nil {
		t.Fatalf("stat WAL before rewrite: %v", err)
	}
	origID, err := copyinmarker.Identity(walPath)
	if err != nil {
		t.Fatalf("identify WAL before rewrite: %v", err)
	}
	origStamp, err := copyinmarker.ChangeStamp(walPath)
	if err != nil {
		t.Fatalf("change stamp before rewrite: %v", err)
	}

	walFile, err := os.OpenFile(walPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open WAL in place: %v", err)
	}
	var firstByte [1]byte
	if _, err := walFile.ReadAt(firstByte[:], 0); err != nil {
		_ = walFile.Close()
		t.Fatalf("read WAL byte 0: %v", err)
	}
	firstByte[0] ^= 0xff
	if _, err := walFile.WriteAt(firstByte[:], 0); err != nil {
		_ = walFile.Close()
		t.Fatalf("corrupt WAL byte 0 in place: %v", err)
	}
	if err := walFile.Close(); err != nil {
		t.Fatalf("close WAL: %v", err)
	}
	if err := os.Chtimes(walPath, origStat.ModTime(), origStat.ModTime()); err != nil {
		t.Fatalf("restore WAL mtime: %v", err)
	}

	afterStat, err := os.Lstat(walPath)
	if err != nil {
		t.Fatalf("stat WAL after rewrite: %v", err)
	}
	afterID, err := copyinmarker.Identity(walPath)
	if err != nil {
		t.Fatalf("identify WAL after rewrite: %v", err)
	}
	afterStamp, err := copyinmarker.ChangeStamp(walPath)
	if err != nil {
		t.Fatalf("change stamp after rewrite: %v", err)
	}
	if afterID != origID || afterStat.Size() != origStat.Size() || afterStat.ModTime().UnixNano() != origStat.ModTime().UnixNano() {
		t.Fatalf("WAL rewrite did not preserve inode/size/mtime")
	}
	if afterStamp == origStamp {
		t.Fatalf("WAL change stamp did not advance (%s)", afterStamp)
	}
	beforeCallsTree := readSourceTree(t, dir)

	if code, body := callRoute(http.MethodPost, upgradeauth.Path); code != http.StatusServiceUnavailable {
		t.Fatalf("upgrade authenticate after mtime-restored WAL corruption = %d (%s), want 503", code, body)
	}
	statusRoute := bootstrap.FixedBootstrapAPIPath + "installation/upgrade/status"
	if code, body := callRoute(http.MethodGet, statusRoute); code != http.StatusServiceUnavailable {
		t.Fatalf("upgrade status after mtime-restored WAL corruption = %d (%s), want 503", code, body)
	}
	if afterCallsTree := readSourceTree(t, dir); !reflect.DeepEqual(beforeCallsTree, afterCallsTree) {
		t.Fatalf("failed auth/status mutated source tree")
	}
}

// TestUpgradeAuthInvalidatesCachedCredentialWhenRotatedKeyRestoresMtime proves
// at the HTTP boundary that rotating the admin credential in place and
// restoring mtime invalidates the cached pass for the previous admin key.
func TestUpgradeAuthInvalidatesCachedCredentialWhenRotatedKeyRestoresMtime(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	const (
		originalKey = "http-original-legacy-key-00001"
		rotatedKey  = "http-rotated-legacy-key-000002"
	)
	_, paths := legacyWALSourceFixture(t, originalKey)
	_, donorPaths := legacyWALSourceFixture(t, rotatedKey)
	donorBytes, err := os.ReadFile(donorPaths.Database)
	if err != nil {
		t.Fatalf("read donor database: %v", err)
	}

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(paths, legacypreflight.Options{}, 5*time.Second)
	defer h.BeginWritableStartup()
	server := httptest.NewServer(h)
	defer server.Close()

	authenticate := func(key string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+upgradeauth.Path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(raw)
	}

	if code, body := authenticate(originalKey); code != http.StatusOK || !strings.Contains(body, `"authenticated":true`) {
		t.Fatalf("initial authenticate = %d (%s), want 200", code, body)
	}

	origStat, err := os.Lstat(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	origID, err := copyinmarker.Identity(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	dbFile, err := os.OpenFile(paths.Database, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbFile.WriteAt(donorBytes, 0); err != nil {
		_ = dbFile.Close()
		t.Fatal(err)
	}
	if err := dbFile.Truncate(int64(len(donorBytes))); err != nil {
		_ = dbFile.Close()
		t.Fatal(err)
	}
	if err := dbFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(paths.Database, origStat.ModTime(), origStat.ModTime()); err != nil {
		t.Fatal(err)
	}
	afterStat, err := os.Lstat(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	afterID, err := copyinmarker.Identity(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if afterID != origID || afterStat.Size() != origStat.Size() || afterStat.ModTime().UnixNano() != origStat.ModTime().UnixNano() {
		t.Fatalf("database rewrite did not preserve inode/size/mtime")
	}

	if code, body := authenticate(originalKey); code != http.StatusUnauthorized {
		t.Fatalf("authenticate with old key after mtime-restored rotation = %d (%s), want 401", code, body)
	}
	if code, body := authenticate(rotatedKey); code != http.StatusOK || !strings.Contains(body, `"authenticated":true`) {
		t.Fatalf("authenticate with rotated key = %d (%s), want 200", code, body)
	}
}
