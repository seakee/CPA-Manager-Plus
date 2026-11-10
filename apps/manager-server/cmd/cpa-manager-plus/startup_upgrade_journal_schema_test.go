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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
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

// TestConcurrentFailingObservationsShareOneFlight pins the coalescing rule for a
// failed observation: a document about this moment (an unavailable one) is not
// remembered, but every caller that joined the flight is answered with the one
// observation that produced it. COALESCE only successes would make each waiter run
// its own fenced read of a source nobody is writing.
func TestConcurrentFailingObservationsShareOneFlight(t *testing.T) {
	observer := &discoveryObservation{}
	databasePath := filepath.Join(t.TempDir(), "usage.sqlite")
	release := make(chan struct{})
	started := make(chan struct{})
	var reads atomic.Int32
	read := func() (bootstrap.PublicDiscovery, bool) {
		if reads.Add(1) == 1 {
			close(started)
		}
		<-release
		return bootstrapdiscovery.Unavailable(), false
	}

	results := make([]bootstrap.PublicDiscovery, 3)
	var wg sync.WaitGroup
	observe := func(index int) {
		defer wg.Done()
		observer.observe(context.Background(), databasePath, read, func(projection bootstrap.PublicDiscovery) {
			results[index] = projection
		})
	}
	wg.Add(1)
	go observe(0)
	<-started
	// The first observation cannot clear its flight while it is blocked in read,
	// so the others join it as soon as they reach it.
	wg.Add(2)
	go observe(1)
	go observe(2)
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if ran := observer.observations(); ran != 1 {
		t.Fatalf("%d observations for three concurrent requests, want 1", ran)
	}
	if executed := reads.Load(); executed != 1 {
		t.Fatalf("%d reads for three concurrent requests, want 1", executed)
	}
	for index, projection := range results {
		if projection.Status != bootstrap.PublicUnavailable {
			t.Fatalf("request %d was answered %+v, want the fail-closed document", index, projection)
		}
	}
}

// TestCachedProjectionIsDroppedWhenTheProbeCannotProveQuiescence pins the cache's
// revalidation answer: only "nobody holds this marker" (or no marker at all) may
// serve a remembered document. Any other probe failure drops the entry and answers
// unavailable, because an unreadable marker is not proof of the quiescence the
// projection was granted on.
func TestCachedProjectionIsDroppedWhenTheProbeCannotProveQuiescence(t *testing.T) {
	observer := &discoveryObservation{}
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "usage.sqlite")
	if err := os.WriteFile(databasePath, []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatalf("write the source database: %v", err)
	}
	identity, ok := upgradeservice.LegacySourceIdentity(databasePath)
	if !ok {
		t.Fatal("identify the source")
	}
	seedCache := func() {
		observer.mu.Lock()
		defer observer.mu.Unlock()
		observer.sourceState(databasePath).cached = discoveryObservationCache{
			identity:   identity,
			ready:      true,
			projection: bootstrapdiscovery.UnsupportedUpgrade(),
		}
	}
	// Positive control: with the marker proving quiescence, the document is served
	// from memory and no observation runs.
	seedCache()
	var served bootstrap.PublicDiscovery
	observer.observe(context.Background(), databasePath, func() (bootstrap.PublicDiscovery, bool) {
		t.Fatal("a remembered projection must be served without a new observation")
		return bootstrap.PublicDiscovery{}, false
	}, func(projection bootstrap.PublicDiscovery) {
		served = projection
	})
	if served.Status != bootstrap.PublicUnsupportedUpgrade {
		t.Fatalf("cached projection = %+v, want the remembered document", served)
	}

	original := probeSource
	probeSource = func(string) error { return errors.New("marker unreadable") }
	defer func() { probeSource = original }()
	seedCache()
	var dropped bootstrap.PublicDiscovery
	observer.observe(context.Background(), databasePath, func() (bootstrap.PublicDiscovery, bool) {
		t.Fatal("an unprovable source must not be answered by a new observation")
		return bootstrap.PublicDiscovery{}, false
	}, func(projection bootstrap.PublicDiscovery) {
		dropped = projection
	})
	if dropped.Status != bootstrap.PublicUnavailable {
		t.Fatalf("projection after a failed revalidation = %+v, want the fail-closed document", dropped)
	}
	observer.mu.Lock()
	ready := observer.sourceState(databasePath).cached.ready
	observer.mu.Unlock()
	if ready {
		t.Fatal("a failed revalidation left the remembered projection in place")
	}
}

// TestCanceledRequestIsNotAnsweredFromTheCache is the cancellation boundary of the
// cache hit: the request may be canceled while the revalidation is still running,
// and a caller that is already gone must never receive the remembered document.
func TestCanceledRequestIsNotAnsweredFromTheCache(t *testing.T) {
	observer := &discoveryObservation{}
	databasePath := filepath.Join(t.TempDir(), "usage.sqlite")
	if err := os.WriteFile(databasePath, []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatalf("write the source database: %v", err)
	}
	identity, ok := upgradeservice.LegacySourceIdentity(databasePath)
	if !ok {
		t.Fatal("identify the source")
	}
	observer.mu.Lock()
	observer.sourceState(databasePath).cached = discoveryObservationCache{
		identity:   identity,
		ready:      true,
		projection: bootstrapdiscovery.UnsupportedUpgrade(),
	}
	observer.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := afterCacheRevalidation
	afterCacheRevalidation = cancel
	defer func() { afterCacheRevalidation = original }()

	var served bootstrap.PublicDiscovery
	observed := false
	observer.observe(ctx, databasePath, func() (bootstrap.PublicDiscovery, bool) {
		observed = true
		return bootstrap.PublicDiscovery{}, false
	}, func(projection bootstrap.PublicDiscovery) {
		served = projection
	})
	if observed {
		t.Fatal("a canceled request ran an observation")
	}
	if served.Status != bootstrap.PublicUnavailable {
		t.Fatalf("canceled request was answered %+v, want the fail-closed document", served)
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
