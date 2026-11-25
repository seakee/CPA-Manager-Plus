package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	bootstrapservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	cpaupdateservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaupdate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	setupservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/setup"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestWriteVersion(t *testing.T) {
	for _, arg := range []string{"-v", "--version"} {
		t.Run(arg, func(t *testing.T) {
			var stdout bytes.Buffer
			handled, err := writeVersion([]string{arg}, &stdout)
			if err != nil {
				t.Fatalf("writeVersion(%q): %v", arg, err)
			}
			if !handled {
				t.Fatalf("writeVersion(%q) handled = false, want true", arg)
			}
			if got, want := stdout.String(), buildinfo.Version+"\n"; got != want {
				t.Fatalf("writeVersion(%q) output = %q, want %q", arg, got, want)
			}
		})
	}
}

func TestStartupLogsBuildIdentity(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "usage.sqlite")
	process := startManagerServerProcess(t, dataDir, dbPath)
	process.stop(t)

	logs := process.logs.String()
	banner := buildinfo.StartupLine()
	bannerAt := strings.Index(logs, banner)
	listenAt := strings.Index(logs, "cpa-manager-plus listening on ")
	if bannerAt < 0 {
		t.Fatalf("startup logs missing build identity banner %q:\n%s", banner, logs)
	}
	if listenAt < 0 || bannerAt > listenAt {
		t.Fatalf("build identity banner must precede listening log (banner=%d listen=%d):\n%s", bannerAt, listenAt, logs)
	}
}

func TestStartupPreflightTimeout(t *testing.T) {
	for _, test := range []struct {
		value   string
		want    time.Duration
		invalid bool
	}{
		{"", 2 * time.Minute, false}, {" 5m ", 5 * time.Minute, false}, {"500ms", 500 * time.Millisecond, false},
		{"0", 0, true}, {"-1s", 0, true}, {"invalid", 0, true}, {"999999999h", 0, true},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("CPA_MANAGER_PREFLIGHT_TIMEOUT", test.value)
			got, err := startupPreflightTimeout()
			if (err != nil) != test.invalid || got != test.want {
				t.Fatalf("timeout=%s err=%v", got, err)
			}
		})
	}
}

type recordingInspectionStopper struct {
	calls       int
	firstErr    error
	hasDeadline bool
}

func (s *recordingInspectionStopper) StopAndWait(ctx context.Context) error {
	s.calls++
	_, s.hasDeadline = ctx.Deadline()
	return s.firstErr
}

func TestNewPprofServer(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantNil bool
		wantErr bool
	}{
		{name: "disabled", wantNil: true},
		{name: "ipv4 loopback", addr: "127.0.0.1:6060"},
		{name: "ipv6 loopback", addr: "[::1]:6060"},
		{name: "localhost", addr: "localhost:6060"},
		{name: "all interfaces", addr: ":6060", wantErr: true},
		{name: "public address", addr: "0.0.0.0:6060", wantErr: true},
		{name: "invalid", addr: "localhost", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, err := newPprofServer(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("newPprofServer(%q) error = %v", tt.addr, err)
			}
			if tt.wantNil && server != nil {
				t.Fatalf("newPprofServer(%q) = %#v, want nil", tt.addr, server)
			}
			if !tt.wantNil && !tt.wantErr && server == nil {
				t.Fatalf("newPprofServer(%q) = nil", tt.addr)
			}
		})
	}
}

func TestWaitForBackfillReadinessRetriesUntilSuccess(t *testing.T) {
	attempts := 0
	wantErr := errors.New("temporary backfill failure")
	err := waitForBackfillReadiness(context.Background(), time.Millisecond, func(context.Context) error {
		attempts++
		if attempts < 3 {
			return wantErr
		}
		return nil
	})
	if err != nil {
		t.Fatalf("waitForBackfillReadiness error = %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestWaitForBackfillReadinessStopsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	err := waitForBackfillReadiness(ctx, time.Hour, func(context.Context) error {
		attempts++
		cancel()
		return errors.New("temporary backfill failure")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForBackfillReadiness error = %v, want context canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestWaitForBackfillReadinessRejectsNilRunAndAcceptsNonPositiveDelay(t *testing.T) {
	if err := waitForBackfillReadiness(context.Background(), time.Millisecond, nil); err == nil {
		t.Fatal("nil run unexpectedly succeeded")
	}
	attempts := 0
	if err := waitForBackfillReadiness(context.Background(), 0, func(context.Context) error {
		attempts++
		return nil
	}); err != nil {
		t.Fatalf("zero-delay backfill error = %v", err)
	}
	if attempts != 1 {
		t.Fatalf("zero-delay attempts = %d, want 1", attempts)
	}
}

func TestStopCodexInspectionWorkerRemainsBoundedAfterTimeout(t *testing.T) {
	stopper := &recordingInspectionStopper{firstErr: context.DeadlineExceeded}
	stopCodexInspectionWorker(stopper, time.Millisecond)
	if stopper.calls != 1 || !stopper.hasDeadline {
		t.Fatalf("stop calls = %d hasDeadline=%v, want one bounded stop", stopper.calls, stopper.hasDeadline)
	}
}

func TestStopCodexInspectionWorkerDoesNotDrainAfterCleanStop(t *testing.T) {
	stopper := &recordingInspectionStopper{}
	stopCodexInspectionWorker(stopper, time.Millisecond)
	if stopper.calls != 1 {
		t.Fatalf("stop calls = %d, want 1", stopper.calls)
	}
}

func TestServeHTTPServerCancelsRuntimeOnUnexpectedExit(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go serveHTTPServer(&http.Server{Handler: http.NewServeMux()}, listener, cancel, result)
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("runtime context was not canceled after HTTP listener exit")
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("unexpected HTTP listener exit error = nil")
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP listener exit result was not reported")
	}
}

func TestStartupHTTPHandlerKeepsDiscoveryBeforeReadyAndDelegatesAfter(t *testing.T) {
	mode := bootstrap.Fresh
	result := bootstrap.PublicDiscovery{
		ContractVersion:        bootstrap.PublicDiscoveryContractVersion,
		Status:                 bootstrap.PublicFreshClaimRequired,
		Mode:                   &mode,
		AuthenticationRequired: true,
		AuthEntry:              bootstrap.AuthEntryFreshClaim,
		Reason:                 bootstrap.ReasonFreshClaimRequired,
	}
	handler := newStartupHTTPHandler(result)
	request := httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"fresh"`) {
		t.Fatalf("pre-ready discovery response=%d body=%s", response.Code, response.Body.String())
	}
	unrelated := httptest.NewRecorder()
	handler.ServeHTTP(unrelated, httptest.NewRequest(http.MethodPost, "/usage-service/config", nil))
	if unrelated.Code != http.StatusNotFound {
		t.Fatalf("pre-ready mutation path status=%d want 404", unrelated.Code)
	}
	delegated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	handler.SetReady(delegated)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/usage-service/info", nil))
	if response.Code != http.StatusCreated {
		t.Fatalf("ready delegated status=%d want 201", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("fixed discovery lost ownership after ready: status=%d", response.Code)
	}
	handler.SetCompleted()
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "completed") || strings.Contains(response.Body.String(), "controlBasePath") {
		t.Fatalf("explicit completed lifecycle disclosed discovery: status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestWritableStartupInvalidatesFreshWithoutCompletingBootstrap(t *testing.T) {
	mode := bootstrap.Fresh
	handler := newStartupHTTPHandler(bootstrap.PublicDiscovery{
		ContractVersion: bootstrap.PublicDiscoveryContractVersion,
		Status:          bootstrap.PublicFreshClaimRequired, Mode: &mode,
		AuthenticationRequired: true, AuthEntry: bootstrap.AuthEntryFreshClaim,
		Reason: bootstrap.ReasonFreshClaimRequired,
	})
	handler.BeginWritableStartup()
	for _, ready := range []bool{false, true} {
		if ready {
			handler.SetReady(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
		if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), `"mode"`) ||
			!strings.Contains(response.Body.String(), `"status":"unavailable"`) {
			t.Fatalf("ready=%v reused a stale mode or completed Bootstrap: status=%d body=%s", ready, response.Code, response.Body.String())
		}
		if handler.discoveryLifecycle() != "active" {
			t.Fatalf("ready=%v changed Bootstrap lifecycle: %s", ready, handler.discoveryLifecycle())
		}
	}
}

func TestStartupHealthRemainsUnreadyUntilGraphReady(t *testing.T) {
	handler := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"ok":false`) {
		t.Fatalf("pre-ready health succeeded: status=%d body=%s", response.Code, response.Body.String())
	}
	handler.SetReady(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"service":"existing-manager"}`))
	}))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "existing-manager") {
		t.Fatalf("ready health did not delegate: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConcurrentDiscoveryDoesNotFallThroughDuringWritableStartup(t *testing.T) {
	mode := bootstrap.Fresh
	handler := newStartupHTTPHandler(bootstrap.PublicDiscovery{
		ContractVersion: bootstrap.PublicDiscoveryContractVersion,
		Status:          bootstrap.PublicFreshClaimRequired, Mode: &mode,
		AuthenticationRequired: true, AuthEntry: bootstrap.AuthEntryFreshClaim,
		Reason: bootstrap.ReasonFreshClaimRequired,
	})
	var requests sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		requests.Add(1)
		go func() {
			defer requests.Done()
			<-start
			for range 50 {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
				if response.Code != http.StatusOK && response.Code != http.StatusServiceUnavailable {
					t.Errorf("discovery lost ownership during transition: %d", response.Code)
				}
			}
		}()
	}
	close(start)
	handler.BeginWritableStartup()
	handler.SetReady(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	requests.Wait()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), `"mode"`) {
		t.Fatalf("pre-write projection survived invalidation: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestStartupDiscoveryDoesNotInventAuthorityAndExpiresBeforeWrites(t *testing.T) {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(content)
	if strings.Contains(source, "SourceStopped = true") || strings.Contains(source, "startupHandler.SetCompleted()") {
		t.Fatal("normal startup manufactured source quiescence or Bootstrap completion")
	}
	admitAt := strings.Index(source, "legacypreflight.AdmitStartup(")
	expireAt := strings.Index(source, "startupHandler.BeginWritableStartup()")
	writeAt := strings.Index(source, "config.Load()")
	if admitAt < 0 || expireAt < admitAt || writeAt < expireAt {
		t.Fatalf("discovery invalidation does not precede writable initialization: admit=%d expire=%d write=%d", admitAt, expireAt, writeAt)
	}
}

func TestDerivedMigrationsStartAfterHTTPListenerIsBound(t *testing.T) {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(content)
	listenAt := strings.Index(source, `net.Listen("tcp", cfg.HTTPAddr)`)
	listeningLogAt := strings.Index(source, `log.Printf("cpa-manager-plus listening on %s", listener.Addr())`)
	serveAt := strings.Index(source, "go serveHTTPServer(server, listener, stop, serverResult)")
	if listenAt < 0 || listeningLogAt < listenAt || serveAt < listeningLogAt {
		t.Fatalf("HTTP listener ordering not found: listen=%d log=%d serve=%d", listenAt, listeningLogAt, serveAt)
	}
	for _, startCall := range []string{
		"runtimeReconciler.Run(ctx)",
		"db.RunDerivedStartupMaintenance(ctx)",
		"automationRuntime.Start(ctx)",
		"codexInspectionWorker.Start(ctx)",
		"accountHistoryRollupWorker.Start(ctx)",
		"usageDerivedRollupWorker.Start(ctx)",
		"usageHourlyAggregateWorker.Start(ctx)",
		"db.StartDerivedMaintenance(ctx)",
		"collectorWorker.Start(ctx)",
		"legacyQuotaSnapshotMigrationWorker.Start(ctx)",
		"usageCacheAccountingMigrationWorker.Start(ctx)",
		"waitForUsageResponseMetadataBackfill(ctx, db)",
		"usageArchiveRetentionWorker.Start(ctx)",
	} {
		startAt := strings.Index(source, startCall)
		if startAt < serveAt {
			t.Fatalf("%s starts before HTTP Serve is launched: start=%d serve=%d", startCall, startAt, serveAt)
		}
	}
	maintenanceAt := strings.Index(source, "db.RunDerivedStartupMaintenance(ctx)")
	collectorAt := strings.Index(source, "collectorWorker.Start(ctx)")
	if maintenanceAt < serveAt || collectorAt < maintenanceAt {
		t.Fatalf("startup maintenance/collector ordering invalid: serve=%d maintenance=%d collector=%d", serveAt, maintenanceAt, collectorAt)
	}
	migrationWorkerAt := strings.Index(source, "usageCacheAccountingMigrationWorker :=")
	migrationStartAt := strings.Index(source, "usageCacheAccountingMigrationWorker.Start(ctx)")
	backfillAt := strings.Index(source, "waitForUsageResponseMetadataBackfill(ctx, db)")
	retentionAt := strings.Index(source, "usageArchiveRetentionWorker.Start(ctx)")
	if migrationWorkerAt < serveAt || migrationStartAt < migrationWorkerAt ||
		backfillAt < migrationWorkerAt || retentionAt < backfillAt {
		t.Fatalf(
			"retention readiness ordering invalid: serve=%d migration_worker=%d migration_start=%d backfill=%d retention=%d",
			serveAt,
			migrationWorkerAt,
			migrationStartAt,
			backfillAt,
			retentionAt,
		)
	}
	if !strings.Contains(source, "newUsageArchiveRetentionWorker(cfg, serverApp)") ||
		!strings.Contains(source, "!cfg.DashboardHourlyRollupEnabled") ||
		!strings.Contains(source, "SetSettingsResolver(") ||
		!strings.Contains(source, "SetRetentionChangedNotifier(") {
		t.Fatal("retention worker construction is not gated by hourly rollups and wired to dynamic retention settings")
	}
	if !strings.Contains(source, "continuing without blocking background workers") {
		t.Fatal("post-listen index failure does not explicitly preserve background worker startup")
	}
}

func TestManagerDatabaseProcessLockPrecedesStoreOpen(t *testing.T) {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(content)
	lockAt := strings.Index(source, "legacypreflight.AdmitStartup(")
	storeOpenAt := strings.Index(source, "store.Open(cfg.DBPath, protector)")
	lockCloseAt := strings.Index(source, "databaseLock.Close()")
	if lockAt < 0 || storeOpenAt < lockAt || lockCloseAt < lockAt {
		t.Fatalf("database lock ordering invalid: lock=%d open=%d close=%d", lockAt, storeOpenAt, lockCloseAt)
	}
}

func TestBootstrapDiscoveryBindsBeforeWritableStartup(t *testing.T) {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(content)
	positions := []struct {
		name string
		at   int
	}{
		{name: "listener", at: strings.Index(source, `net.Listen("tcp", cfg.HTTPAddr)`)},
		{name: "admission", at: strings.Index(source, "legacypreflight.AdmitStartup(")},
		{name: "data key", at: strings.Index(source, "security.LoadOrCreateDataKey(")},
		{name: "store open", at: strings.Index(source, "store.Open(cfg.DBPath, protector)")},
		{name: "legacy bootstrap", at: strings.Index(source, "bootstrapservice.Run(")},
	}
	for _, position := range positions {
		if position.at < 0 {
			t.Fatalf("startup %s call not found", position.name)
		}
	}
	for i := 1; i < len(positions); i++ {
		if positions[i-1].at >= positions[i].at {
			t.Fatalf("startup ordering %s=%d must precede %s=%d", positions[i-1].name, positions[i-1].at, positions[i].name, positions[i].at)
		}
	}
}

func TestCleanupDerivedCommandUsesSignalContext(t *testing.T) {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(content)
	commandAt := strings.Index(source, `case "cleanup-derived":`)
	if commandAt < 0 {
		t.Fatal("cleanup-derived command entry not found")
	}
	commandSource := source[commandAt:]
	contextAt := strings.Index(commandSource, "signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)")
	runAt := strings.Index(commandSource, "derivedmaintenance.Run(ctx, os.Args[2:], os.Stdout, os.Stderr)")
	if contextAt < 0 || runAt < contextAt {
		t.Fatalf("cleanup-derived signal context ordering invalid: context=%d run=%d", contextAt, runAt)
	}
}

func TestCompactUsageCommandUsesSignalContextAndHandlesHelp(t *testing.T) {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(content)
	commandAt := strings.Index(source, `case "compact-usage":`)
	if commandAt < 0 {
		t.Fatal("compact-usage command entry not found")
	}
	commandSource := source[commandAt:]
	contextAt := strings.Index(commandSource, "signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)")
	runAt := strings.Index(commandSource, "usagecompact.Run(ctx, os.Args[2:], os.Stdout, os.Stderr)")
	helpAt := strings.Index(commandSource, "usagecompact.IsHelp(err)")
	returnAt := strings.Index(commandSource, "return")
	if contextAt < 0 || runAt < contextAt || helpAt < runAt || returnAt < helpAt {
		t.Fatalf("compact-usage command ordering invalid: context=%d run=%d help=%d return=%d", contextAt, runAt, helpAt, returnAt)
	}
}

func TestManagerDataSnapshotCommandUsesSignalContext(t *testing.T) {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(content)
	commandAt := strings.Index(source, `case "manager-data-snapshot":`)
	if commandAt < 0 {
		t.Fatal("manager-data-snapshot command entry not found")
	}
	commandSource := source[commandAt:]
	runAt := strings.Index(commandSource, "runManagerDataSnapshotCommand(os.Args[2:], os.Stdout, os.Stderr)")
	contextAt := strings.Index(source, "func runManagerDataSnapshotCommand")
	if runAt < 0 || contextAt < 0 {
		t.Fatalf("manager-data-snapshot command/helper wiring invalid: run=%d helper=%d", runAt, contextAt)
	}
	helpersource := source[contextAt:]
	notifyAt := strings.Index(helpersource, "signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)")
	snapshotAt := strings.Index(helpersource, "managerdatasnapshot.Run(ctx, args, stdout, stderr)")
	if notifyAt < 0 || snapshotAt < notifyAt {
		t.Fatalf("manager-data-snapshot signal wiring invalid: run=%d helper=%d notify=%d snapshot=%d", runAt, contextAt, notifyAt, snapshotAt)
	}
}

func TestLargeDerivedMigrationServesHTTPAndResumesAfterRestart(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("external-process availability test is covered by the normal suite; race instrumentation makes the 100k index phase exceed its operational timing budget")
	}
	const rowCount = int64(100_001)
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "usage.sqlite")
	before := prepareLargeStartupMigrationFixture(t, dbPath, rowCount)

	first := startManagerServerProcess(t, tempDir, dbPath)
	firstStopped := false
	t.Cleanup(func() {
		if !firstStopped {
			first.stop(t)
		}
	})
	assertManagerEndpoint(t, first.addr, "/management.html")
	assertManagerEndpoint(t, first.addr, "/usage-service/info")

	observer := openMigrationObserver(t, dbPath)
	t.Cleanup(func() { _ = observer.Close() })
	status, processed := readMonitoringCleanupProgress(t, observer)
	if status != "online_cleanup" || processed >= rowCount {
		t.Fatalf("cleanup completed before initial HTTP acceptance: status=%q processed=%d", status, processed)
	}
	waitForMonitoringCleanupProgress(t, observer, processed)
	first.stop(t)
	firstStopped = true
	status, checkpoint := readMonitoringCleanupProgress(t, observer)
	if status != "online_cleanup" || checkpoint <= 0 || checkpoint >= rowCount {
		t.Fatalf("interrupted cleanup checkpoint = status:%q processed:%d", status, checkpoint)
	}
	if after := readUsageEventsSummaryFromDB(t, observer); after != before {
		t.Fatalf("usage_events changed after interrupted startup cleanup: before=%+v after=%+v", before, after)
	}

	second := startManagerServerProcess(t, tempDir, dbPath)
	secondStopped := false
	t.Cleanup(func() {
		if !secondStopped {
			second.stop(t)
		}
	})
	assertManagerEndpoint(t, second.addr, "/management.html")
	assertManagerEndpoint(t, second.addr, "/usage-service/info")
	waitForMonitoringCleanupProgress(t, observer, checkpoint)
	second.stop(t)
	secondStopped = true
	_, resumedCheckpoint := readMonitoringCleanupProgress(t, observer)
	if resumedCheckpoint <= checkpoint {
		t.Fatalf("cleanup did not resume after restart: before=%d after=%d", checkpoint, resumedCheckpoint)
	}
	if after := readUsageEventsSummaryFromDB(t, observer); after != before {
		t.Fatalf("usage_events changed after resumed startup cleanup: before=%+v after=%+v", before, after)
	}
}

func TestManagerServerHelperProcess(t *testing.T) {
	if os.Getenv("CPA_MANAGER_TEST_HELPER") != "1" {
		t.Skip("helper process only")
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"cpa-manager-plus"}, os.Args[i+1:]...)
			main()
			return
		}
	}
	runServer()
}

type startupUsageEventsSummary struct {
	rows          int64
	idSum         int64
	timestampSum  int64
	eventHashSize int64
}

func prepareLargeStartupMigrationFixture(t testing.TB, dbPath string, rowCount int64) startupUsageEventsSummary {
	t.Helper()
	db, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open startup migration fixture: %v", err)
	}
	if _, err := db.Exec(`with recursive ids(id) as (
		select 1 union all select id + 1 from ids where id < ?
	) insert into usage_events (
		id, request_id, event_hash, timestamp_ms, timestamp, model,
		requested_model, created_at_ms
	) select id, 'request-' || id, 'event-' || id, id, cast(id as text),
		'gpt-test', 'gpt-test', id from ids`, rowCount); err != nil {
		_ = db.Close()
		t.Fatalf("seed large startup usage events: %v", err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		_ = db.Close()
		t.Fatalf("begin large startup projection seed: %v", err)
	}
	if err := usageprojection.UpsertEventRange(context.Background(), tx, 0, rowCount, 1); err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		t.Fatalf("seed large startup projection: %v", err)
	}
	if err := tx.Commit(); err != nil {
		_ = db.Close()
		t.Fatalf("commit large startup projection: %v", err)
	}
	if _, err := db.Exec(`delete from settings where key = 'usage_monitoring_model_format_version'`); err != nil {
		_ = db.Close()
		t.Fatalf("mark monitoring projection as v1.11.12-style: %v", err)
	}
	summary := readUsageEventsSummaryFromDB(t, db)
	if err := db.Close(); err != nil {
		t.Fatalf("close startup migration fixture: %v", err)
	}
	return summary
}

type managerServerProcess struct {
	cmd  *exec.Cmd
	addr string
	done chan error
	logs *synchronizedLog
}

type synchronizedLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *synchronizedLog) append(line string) {
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
}

func (l *synchronizedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func startManagerServerProcess(t testing.TB, dataDir, dbPath string, startupArgs ...string) *managerServerProcess {
	t.Helper()
	return startManagerServerProcessWithEnvironment(t, managerServerTestEnvironment(dataDir, dbPath), startupArgs...)
}

func startManagerServerProcessWithEnvironment(t testing.TB, environment []string, startupArgs ...string) *managerServerProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestManagerServerHelperProcess$", "--"}, startupArgs...)...)
	cmd.Env = environment
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("open manager server stderr: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start manager server helper: %v", err)
	}
	process := &managerServerProcess{
		cmd:  cmd,
		done: make(chan error, 1),
		logs: &synchronizedLog{},
	}
	lines := make(chan string, 128)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			process.logs.append(line)
			lines <- line
		}
		close(lines)
	}()
	go func() { process.done <- cmd.Wait() }()

	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for process.addr == "" {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("manager server helper exited before listening:\n%s", process.logs.String())
			}
			const marker = "cpa-manager-plus listening on "
			if index := strings.Index(line, marker); index >= 0 {
				process.addr = strings.TrimSpace(line[index+len(marker):])
			}
		case err := <-process.done:
			t.Fatalf("manager server helper exited before listening: %v\n%s", err, process.logs.String())
		case <-deadline.C:
			_ = cmd.Process.Kill()
			<-process.done
			t.Fatalf("manager server helper did not listen within timeout:\n%s", process.logs.String())
		}
	}
	return process
}

func managerServerTestEnvironment(dataDir, dbPath string) []string {
	overrides := map[string]string{
		"CPA_MANAGER_PREFLIGHT_TIMEOUT":         "",
		"CPA_MANAGER_TEST_HELPER":               "1",
		"CPA_MANAGER_ADMIN_KEY":                 "cpamp_Startup_Migration_Test_7391_Strong_Key",
		"CPA_MANAGER_DATA_KEY_PATH":             filepath.Join(dataDir, "data.key"),
		"HTTP_ADDR":                             "127.0.0.1:0",
		"USAGE_DATA_DIR":                        dataDir,
		"USAGE_DB_PATH":                         dbPath,
		"USAGE_COLLECTOR_MODE":                  "http",
		"USAGE_POLL_INTERVAL_MS":                "1000",
		"USAGE_DASHBOARD_HOURLY_ROLLUP_ENABLED": "false",
	}
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; !replaced {
			environment = append(environment, entry)
		}
	}
	for key, value := range overrides {
		environment = append(environment, key+"="+value)
	}
	return environment
}

func (p *managerServerProcess) stop(t testing.TB) {
	t.Helper()
	signal := os.Interrupt
	if runtime.GOOS == "windows" {
		// Windows cannot send Interrupt through os.Process. Terminate this
		// owned helper; reopen tests then exercise native crash recovery.
		signal = os.Kill
	}
	if err := p.cmd.Process.Signal(signal); err != nil {
		t.Fatalf("signal manager server helper: %v", err)
	}
	select {
	case err := <-p.done:
		if err != nil {
			var exitErr *exec.ExitError
			if signal != os.Kill || !errors.As(err, &exitErr) {
				t.Fatalf("manager server helper shutdown: %v\n%s", err, p.logs.String())
			}
		}
	case <-time.After(20 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		t.Fatalf("manager server helper shutdown timed out:\n%s", p.logs.String())
	}
}

func assertManagerEndpoint(t testing.TB, addr, path string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + addr + path)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("status %d", response.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("GET %s%s did not become available: %v", addr, path, lastErr)
}

func waitForManagerResponse(t testing.TB, addr, method, path string) (int, string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(method, "http://"+addr+path, nil)
		if err != nil {
			t.Fatalf("create %s %s request: %v", method, path, err)
		}
		response, err := client.Do(request)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatalf("read %s %s response: %v", method, path, readErr)
			}
			return response.StatusCode, string(body)
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s %s did not become reachable: %v", method, path, lastErr)
	return 0, ""
}

func openMigrationObserver(t testing.TB, dbPath string) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(dbPath) + "?_pragma=busy_timeout(5000)&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open migration observer: %v", err)
	}
	return db
}

func readMonitoringCleanupProgress(t testing.TB, db *sql.DB) (string, int64) {
	t.Helper()
	var status string
	var processed int64
	if err := db.QueryRow(`select status, processed_rows from usage_derived_cleanup_jobs
		where kind = 'monitoring_fts' order by generation desc limit 1`).Scan(&status, &processed); err != nil {
		t.Fatalf("read monitoring cleanup progress: %v", err)
	}
	return status, processed
}

func waitForMonitoringCleanupProgress(t testing.TB, db *sql.DB, after int64) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		status, processed := readMonitoringCleanupProgress(t, db)
		if processed > after {
			return
		}
		if status != "online_cleanup" {
			t.Fatalf("monitoring cleanup stopped before checkpoint advanced: status=%q processed=%d after=%d", status, processed, after)
		}
		time.Sleep(20 * time.Millisecond)
	}
	status, processed := readMonitoringCleanupProgress(t, db)
	t.Fatalf("monitoring cleanup checkpoint did not advance: status=%q processed=%d after=%d", status, processed, after)
}

func readUsageEventsSummaryFromDB(t testing.TB, db *sql.DB) startupUsageEventsSummary {
	t.Helper()
	var summary startupUsageEventsSummary
	if err := db.QueryRow(`select count(*), coalesce(sum(id), 0),
		coalesce(sum(timestamp_ms), 0), coalesce(sum(length(event_hash)), 0)
		from usage_events`).Scan(&summary.rows, &summary.idSum, &summary.timestampSum, &summary.eventHashSize); err != nil {
		t.Fatalf("read usage_events summary: %v", err)
	}
	return summary
}

type gatingCodexStopper struct {
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (s *gatingCodexStopper) StopAndWait(ctx context.Context) error {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestLegacyShutdownAbortsNonFreshBodyBeforeCodexWorkerDrain(t *testing.T) {
	startup := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	startup.earlyCloseDrainTimeout = -1

	secondReadEntered := make(chan struct{})
	var readOnce sync.Once
	startup.onRequestBodyRead = func(r *http.Request, attempt int) {
		if r != nil && r.URL != nil && r.URL.Path == "/usage-service/upload" && attempt >= 2 {
			readOnce.Do(func() { close(secondReadEntered) })
		}
	}

	readErrCh := make(chan error, 1)
	startup.SetReady(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		readErrCh <- err
		w.WriteHeader(http.StatusRequestTimeout)
	}))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{
		Handler:           startup,
		ReadHeaderTimeout: 5 * time.Second,
	}
	procCtx, stopProc := context.WithCancel(context.Background())
	defer stopProc()
	serverResult := make(chan error, 1)
	go serveHTTPServer(server, listener, stopProc, serverResult)
	t.Cleanup(func() { _ = server.Close() })

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Send 4 of 16 bytes so the handler blocks on attempt #2 inside r.Body.Read.
	if _, err := fmt.Fprintf(conn, "POST /usage-service/upload HTTP/1.1\r\nHost: %s\r\nContent-Length: 16\r\n\r\nabcd", listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondReadEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler never entered blocked second body Read")
	}

	releaseCodex := make(chan struct{})
	codexStopper := &gatingCodexStopper{
		entered: make(chan struct{}),
		release: releaseCodex,
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		shutdownLegacyRuntime(
			procCtx,
			stopProc,
			serverResult,
			startup,
			codexStopper,
			nil,
			nil,
			server,
			nil,
			nil,
		)
	}()

	stopProc()
	select {
	case <-codexStopper.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Legacy shutdown did not reach Codex worker stop")
	}

	// While Codex worker stop is STILL blocked on releaseCodex, BeginShutdown()
	// must have already aborted the unfinished non-Fresh HTTP body Read.
	select {
	case rErr := <-readErrCh:
		if !errors.Is(rErr, context.Canceled) {
			close(releaseCodex)
			t.Fatalf("aborted Legacy body Read = %v, want context.Canceled", rErr)
		}
	case <-time.After(300 * time.Millisecond):
		close(releaseCodex)
		<-shutdownDone
		t.Fatal("unfinished non-Fresh body Read remained blocked while Codex worker stop was waiting")
	}

	close(releaseCodex)
	select {
	case <-shutdownDone:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdownLegacyRuntime did not complete after Codex gate released")
	}
}

type blockingArchiveJobs struct {
	started chan struct{}
	release chan struct{}
	waited  bool
}

func (b *blockingArchiveJobs) WaitArchiveJobs(context.Context) error {
	close(b.started)
	<-b.release
	b.waited = true
	return nil
}

type trackingImportSessions struct {
	waited bool
}

func (t *trackingImportSessions) StopAndWaitImportSessions(context.Context) error {
	t.waited = true
	return nil
}

func TestLegacyShutdownCancelsAndDrainsBackgroundAndSQLiteWorkersBeforeReturn(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	var retentionMu sync.Mutex
	retentionStopped := false
	var bgWG sync.WaitGroup

	backfillStarted := make(chan struct{})
	backfillExited := false
	bgWG.Add(1)
	go func() {
		defer bgWG.Done()
		close(backfillStarted)
		<-ctx.Done()
		retentionMu.Lock()
		canStart := !retentionStopped && ctx.Err() == nil
		retentionMu.Unlock()
		if canStart {
			t.Error("backfill goroutine observed retention starter open after shutdown")
		}
		backfillExited = true
	}()
	<-backfillStarted

	migrationExited := false
	workerExited := false
	importSessions := &trackingImportSessions{}
	archiveJobs := &blockingArchiveJobs{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	releaseCodex := make(chan struct{})
	codexGate := &gatingCodexStopper{
		entered: make(chan struct{}),
		release: releaseCodex,
	}
	releaseRetention := make(chan struct{})
	retentionStopper := &gatingCodexStopper{
		entered: make(chan struct{}),
		release: releaseRetention,
	}

	serverResult := make(chan error, 1)
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		shutdownLegacyRuntime(
			ctx,
			stop,
			serverResult,
			nil,
			codexGate,
			nil,
			nil,
			nil,
			retentionStopper,
			archiveJobs,
			legacyRuntimeShutdownHooks{
				importSessions: importSessions,
				stopRetentionStart: func() {
					retentionMu.Lock()
					retentionStopped = true
					retentionMu.Unlock()
				},
				waitMigration: func() {
					migrationExited = true
				},
				waitBackground: bgWG.Wait,
				workerWaits: []func(){
					func() {
						workerExited = true
					},
				},
			},
		)
	}()

	// Server termination triggers stop() -> cancels ctx -> unblocks backfill goroutine.
	serverResult <- nil

	<-codexGate.entered
	close(releaseCodex)
	<-retentionStopper.entered
	close(releaseRetention)
	<-archiveJobs.started
	close(archiveJobs.release)

	select {
	case <-shutdownDone:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdownLegacyRuntime did not complete")
	}
	if !backfillExited || !migrationExited || !workerExited || !importSessions.waited || !archiveJobs.waited {
		t.Fatalf("expected all background/SQLite drains completed before return: backfill=%v migration=%v worker=%v imports=%v archive=%v",
			backfillExited, migrationExited, workerExited, importSessions.waited, archiveJobs.waited)
	}
}

type recordingShutdownCollectorManager struct {
	onStop func(context.Context)
}

func (m *recordingShutdownCollectorManager) StopAndWait(ctx context.Context) error {
	if m.onStop != nil {
		m.onStop(ctx)
	}
	return nil
}

type recordingShutdownCollectorWorker struct {
	onStop func(context.Context)
}

func (w *recordingShutdownCollectorWorker) Stop(ctx context.Context) {
	if w.onStop != nil {
		w.onStop(ctx)
	}
}

type recordingShutdownDrainComponent struct {
	mu      sync.Mutex
	ctxErr  error
	hasDL   bool
	remain  time.Duration
	called  bool
	onEnter func(context.Context)
}

func (r *recordingShutdownDrainComponent) record(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called = true
	r.ctxErr = ctx.Err()
	if dl, ok := ctx.Deadline(); ok {
		r.hasDL = true
		r.remain = time.Until(dl)
	}
	if r.onEnter != nil {
		r.onEnter(ctx)
	}
}

func (r *recordingShutdownDrainComponent) StopAndWait(ctx context.Context) error {
	r.record(ctx)
	return nil
}

func (r *recordingShutdownDrainComponent) StopAndWaitImportSessions(ctx context.Context) error {
	r.record(ctx)
	return nil
}

func (r *recordingShutdownDrainComponent) WaitArchiveJobs(ctx context.Context) error {
	r.record(ctx)
	return nil
}

func TestLegacyShutdownDrainsHTTPAndPprofBeforeWorkersAndUsesFreshDrainContext(t *testing.T) {
	t.Run("drains HTTP server and pprof before stopping workers", func(t *testing.T) {
		ctx, stop := context.WithCancel(context.Background())
		defer stop()

		httpEntered := make(chan struct{})
		releaseHTTP := make(chan struct{})
		httpFinished := atomic.Bool{}

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(httpEntered)
				<-releaseHTTP
				httpFinished.Store(true)
				w.WriteHeader(http.StatusOK)
			}),
		}
		serverResult := make(chan error, 1)
		go serveHTTPServer(srv, ln, stop, serverResult)

		pprofLn, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		pprofSrv := &http.Server{Handler: http.NewServeMux()}
		var bgWG sync.WaitGroup
		bgWG.Add(1)
		go func() {
			defer bgWG.Done()
			_ = pprofSrv.Serve(pprofLn)
		}()

		httpRespErr := make(chan error, 1)
		httpStatus := make(chan int, 1)
		go func() {
			resp, reqErr := http.Get("http://" + ln.Addr().String() + "/work")
			if reqErr != nil {
				httpRespErr <- reqErr
				return
			}
			_ = resp.Body.Close()
			httpStatus <- resp.StatusCode
		}()
		<-httpEntered

		var orderMu sync.Mutex
		var order []string
		appendOrder := func(step string) {
			orderMu.Lock()
			order = append(order, step)
			orderMu.Unlock()
		}

		codexEntered := make(chan struct{})
		codexStopper := &recordingShutdownDrainComponent{
			onEnter: func(context.Context) {
				if !httpFinished.Load() {
					t.Error("codexInspectionWorker stopped before inflight HTTP handler finished")
				}
				appendOrder("codex")
				close(codexEntered)
			},
		}
		collectorWorker := &recordingShutdownCollectorWorker{
			onStop: func(context.Context) {
				if !httpFinished.Load() {
					t.Error("collectorWorker stopped before inflight HTTP handler finished")
				}
				appendOrder("collectorWorker")
			},
		}
		collectorManager := &recordingShutdownCollectorManager{
			onStop: func(context.Context) {
				if !httpFinished.Load() {
					t.Error("collectorManager stopped before inflight HTTP handler finished")
				}
				appendOrder("collectorManager")
			},
		}
		retention := &recordingShutdownDrainComponent{onEnter: func(context.Context) { appendOrder("retention") }}
		imports := &recordingShutdownDrainComponent{onEnter: func(context.Context) { appendOrder("imports") }}
		archive := &recordingShutdownDrainComponent{onEnter: func(context.Context) { appendOrder("archive") }}

		shutdownDone := make(chan struct{})
		go func() {
			defer close(shutdownDone)
			shutdownLegacyRuntime(
				ctx,
				stop,
				serverResult,
				nil,
				codexStopper,
				collectorWorker,
				pprofSrv,
				srv,
				retention,
				archive,
				legacyRuntimeShutdownHooks{
					collectorManager: collectorManager,
					importSessions:   imports,
					waitBackground:   func() { bgWG.Wait(); appendOrder("bgWG") },
					waitUpgradeCanonical: func() {
						appendOrder("upgradeCanonical")
					},
					workerWaits: []func(){
						func() { appendOrder("derivedMaintenance") },
					},
				},
			)
		}()

		stop()

		// While HTTP handler is still inflight, workers must wait for HTTP drain.
		select {
		case <-codexEntered:
			close(releaseHTTP)
			t.Fatal("workers began stopping before HTTP server finished draining inflight request")
		case <-time.After(80 * time.Millisecond):
		}

		close(releaseHTTP)
		select {
		case code := <-httpStatus:
			if code != http.StatusOK {
				t.Fatalf("inflight HTTP status = %d, want 200", code)
			}
		case reqErr := <-httpRespErr:
			t.Fatalf("inflight HTTP request failed: %v", reqErr)
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for inflight HTTP request")
		}

		select {
		case <-shutdownDone:
		case <-time.After(3 * time.Second):
			t.Fatal("shutdownLegacyRuntime did not complete")
		}

		wantOrder := []string{"codex", "collectorWorker", "collectorManager", "bgWG", "upgradeCanonical", "retention", "imports", "archive", "derivedMaintenance"}
		orderMu.Lock()
		gotOrder := strings.Join(order, ",")
		orderMu.Unlock()
		if gotOrder != strings.Join(wantOrder, ",") {
			t.Fatalf("shutdown order = %q, want %q", gotOrder, strings.Join(wantOrder, ","))
		}
	})

	t.Run("allocates fresh bounded drain context after HTTP shutdown timeout", func(t *testing.T) {
		ctx, stop := context.WithCancel(context.Background())
		defer stop()

		httpEntered := make(chan struct{})
		releaseStuck := make(chan struct{})
		defer close(releaseStuck)

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(httpEntered)
				<-releaseStuck
			}),
		}
		serverResult := make(chan error, 1)
		go serveHTTPServer(srv, ln, stop, serverResult)

		go func() {
			resp, reqErr := http.Get("http://" + ln.Addr().String() + "/stuck")
			if reqErr == nil {
				_ = resp.Body.Close()
			}
		}()
		<-httpEntered

		retention := &recordingShutdownDrainComponent{}
		imports := &recordingShutdownDrainComponent{}
		archive := &recordingShutdownDrainComponent{}

		stop()
		shutdownLegacyRuntime(
			ctx,
			stop,
			serverResult,
			nil,
			nil,
			nil,
			nil,
			srv,
			retention,
			archive,
			legacyRuntimeShutdownHooks{
				importSessions:      imports,
				httpShutdownTimeout: 30 * time.Millisecond,
				drainTimeout:        2 * time.Second,
			},
		)

		for name, rec := range map[string]*recordingShutdownDrainComponent{
			"retention": retention,
			"imports":   imports,
			"archive":   archive,
		} {
			if !rec.called || rec.ctxErr != nil || !rec.hasDL || rec.remain < 500*time.Millisecond {
				t.Fatalf("%s did not receive fresh bounded drain context: called=%t err=%v hasDL=%t remain=%v",
					name, rec.called, rec.ctxErr, rec.hasDL, rec.remain)
			}
		}
	})
}

func TestUpgradeCanonicalWaitTracksIngressLoopAndPluginCapabilityClose(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	ingressSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			raw, _ := io.ReadAll(r.Body)
			_, _ = w.Write(raw)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ingressSrv.Close()
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", ingressSrv.URL)
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "https://manager.test")

	st, cfg, _ := seedCompletedManagedUpgradeStore(t, "/console", "/inference")
	serverApp := httpapi.New(cfg, st, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var canonical *upgradeCanonicalServer
	if err := attachCompletedUpgradeRouting(ctx, cfg, st, serverApp, nil, upgradeCanonicalDependencies{
		CanonicalOut: &canonical,
	}); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}
	if canonical == nil || canonical.driver == nil || canonical.pluginCapability == nil {
		t.Fatalf("expected non-nil canonical server, driver, and pluginCapability: %+v", canonical)
	}

	waitDone := make(chan struct{})
	go func() {
		canonical.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		t.Fatal("canonical.Wait() returned while ctx was active and reconcileIngressLoop/pluginCapability goroutines were running")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("canonical.Wait() did not return after ctx cancellation")
	}
}

type trackingReadCloser struct {
	io.Reader
	closed atomic.Bool
}

func (r *trackingReadCloser) Close() error {
	r.closed.Store(true)
	return nil
}

func TestFreshApplicationServeHTTPRestoresOriginalBodyBeforeReturn(t *testing.T) {
	paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "manager.sqlite"))
	paths.Config = filepath.Join(filepath.Dir(paths.Database), "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{
		Initialize: true,
		AdminKey:   strategyTestAdmin,
	})
	if err != nil {
		t.Fatalf("fresh.Open: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })

	app := newFreshApplication(opened, config.Config{DBPath: paths.Database}, nil)
	t.Cleanup(app.Close)

	origBody := &trackingReadCloser{Reader: strings.NewReader("unread-payload")}
	req := httptest.NewRequest(http.MethodPost, "/unrelated", origBody)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if req.Body != origBody {
		t.Fatalf("ServeHTTP did not restore original r.Body before returning: got %T, want *trackingReadCloser", req.Body)
	}
	if !origBody.closed.Load() {
		t.Fatal("ServeHTTP did not close wrapped body before returning")
	}
}

func TestAwaitFreshStoreShutdownStopsPollingDrainsHandlerBeforeOpenedClose(t *testing.T) {
	type shutdownMode int
	const (
		modeContextCancel shutdownMode = iota
		modeServerExitWithoutParentCancel
		modeDirectStopAwait
	)
	runCase := func(t *testing.T, mode shutdownMode) {
		t.Helper()
		dbDir := t.TempDir()
		paths := legacypreflight.DefaultPaths(filepath.Join(dbDir, "manager.sqlite"))
		paths.Config = filepath.Join(dbDir, "config.json")

		startupHandler := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		ctx, stop := context.WithCancel(context.Background())
		defer stop()
		server := &http.Server{Handler: startupHandler}
		defer func() { _ = server.Close() }()
		serverResult := make(chan error, 1)
		if mode == modeServerExitWithoutParentCancel {
			go func() { _ = server.Serve(ln) }()
		} else {
			go serveHTTPServer(server, ln, stop, serverResult)
		}

		cfg := config.Config{DBPath: paths.Database}
		await := startFreshStoreAwaiterWithInterval(ctx, cfg, paths, startupHandler, 10*time.Millisecond)
		defer await.Close()

		initOpened, err := fresh.Open(ctx, paths, fresh.OpenOptions{
			Initialize: true,
			AdminKey:   strategyTestAdmin,
		})
		if err != nil {
			t.Fatalf("fresh.Open initialize: %v", err)
		}
		if err := initOpened.Close(); err != nil {
			t.Fatalf("initOpened.Close: %v", err)
		}

		var (
			app    *freshApplication
			opened *fresh.Opened
		)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if target := startupHandler.fresh.Load(); target != nil {
				if candidate, ok := target.handler.(*freshApplication); ok {
					await.mu.Lock()
					admitted := await.opened
					await.mu.Unlock()
					if admitted != nil {
						app = candidate
						opened = admitted
						break
					}
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
		if app == nil || opened == nil {
			t.Fatal("startFreshStoreAwaiter did not admit initialized Fresh store")
		}

		workerStarted := make(chan struct{})
		releaseWorker := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(releaseWorker) }) }
		defer release()
		var workerFinished atomic.Bool
		if !app.launchBackground(func() {
			close(workerStarted)
			<-releaseWorker
			workerFinished.Store(true)
		}) {
			t.Fatal("failed to launch fresh background worker")
		}
		<-workerStarted

		var (
			orderMu        sync.Mutex
			order          []string
			handlerEntered = make(chan struct{})
			openedCloseHit = make(chan struct{})
		)
		record := func(step string) {
			orderMu.Lock()
			order = append(order, step)
			orderMu.Unlock()
		}
		app.afterCloseMarked = func() {
			select {
			case <-await.done:
			default:
				t.Error("polling goroutine was still active when handler.Close started")
			}
			record("handler_close_entered")
			close(handlerEntered)
		}
		opened.BeforeClose(func() {
			if !workerFinished.Load() {
				t.Error("opened.Close started before freshApplication drained background worker")
			}
			record("opened_close_entered")
			close(openedCloseHit)
		})

		shutdownDone := make(chan struct{})
		go func() {
			defer close(shutdownDone)
			if mode == modeDirectStopAwait {
				await.Close()
			} else {
				waitForFreshShutdown(ctx, server, serverResult, startupHandler, await, nil)
			}
		}()
		switch mode {
		case modeContextCancel:
			stop()
		case modeServerExitWithoutParentCancel:
			serverResult <- http.ErrServerClosed
		}

		select {
		case <-handlerEntered:
		case <-time.After(3 * time.Second):
			release()
			t.Fatal("handler.Close did not enter during shutdown")
		}

		select {
		case <-openedCloseHit:
			release()
			t.Fatal("opened.Close ran before handler.Close completed")
		case <-time.After(50 * time.Millisecond):
		}

		release()
		select {
		case <-shutdownDone:
		case <-time.After(3 * time.Second):
			t.Fatal("shutdown did not complete after releasing worker")
		}

		orderMu.Lock()
		gotOrder := strings.Join(order, ",")
		orderMu.Unlock()
		if gotOrder != "handler_close_entered,opened_close_entered" {
			t.Fatalf("shutdown order = %q, want %q", gotOrder, "handler_close_entered,opened_close_entered")
		}
		if !workerFinished.Load() {
			t.Fatal("background worker did not finish before shutdown completed")
		}
	}

	t.Run("waitForFreshShutdown_context_cancel", func(t *testing.T) {
		runCase(t, modeContextCancel)
	})
	t.Run("waitForFreshShutdown_server_exit_without_parent_cancel", func(t *testing.T) {
		runCase(t, modeServerExitWithoutParentCancel)
	})
	t.Run("await.Close", func(t *testing.T) {
		runCase(t, modeDirectStopAwait)
	})
}

func TestServerProductCompleteProbeReflectsLiveSetupAuthority(t *testing.T) {
	ctx := context.Background()

	if complete, err := newServerProductCompleteProbe(nil)(ctx); err == nil || complete {
		t.Fatalf("nil serverApp probe = (%v, %v), want (false, err)", complete, err)
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "manager.sqlite")
	dataKeyPath := filepath.Join(dataDir, "data.key")
	rawKey, dataKeyCreated, err := security.LoadOrCreateDataKey("", dataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(rawKey)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.SaveBootstrapState(ctx, store.BootstrapState{
		Version:            1,
		Status:             "needs_setup",
		AdminReady:         true,
		ProjectInitialized: false,
		DataKeyReady:       true,
	}); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		DataDir:      dataDir,
		DBPath:       dbPath,
		DataKeyPath:  dataKeyPath,
		AdminKey:     "cmp_admin_LiveProbeAuthorityTest_987654",
		Queue:        "cpa:usage:events",
		PopSide:      "left",
		BatchSize:    100,
		PollInterval: 500 * time.Millisecond,
		QueryLimit:   50000,
	}
	bootstrapResult, err := bootstrapservice.Run(ctx, cfg, db, dataKeyCreated)
	if err != nil {
		t.Fatal(err)
	}
	if !bootstrapResult.State.AdminReady || bootstrapResult.State.ProjectInitialized {
		t.Fatalf("unexpected initial bootstrap state: %+v", bootstrapResult.State)
	}

	const activeArtifact = model.RuntimeArtifactID("sha256:1111111111111111111111111111111111111111111111111111111111111111")
	mockRT := &upgradeCPAUpdateMockRuntime{}
	mockRT.setStatus(model.RuntimeObservedStatus{
		Identity:           "runtime-live-probe",
		Generation:         1,
		ProtocolVersion:    "v1",
		State:              model.RuntimeStateReady,
		CPAObservedVersion: "8.3.3",
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{
			Engine:     "cpa",
			ArtifactID: activeArtifact,
			Version:    "8.3.3",
		},
		Capabilities: model.RuntimeCapabilities{
			model.RuntimeCapabilityPrepareUpdate,
			model.RuntimeCapabilityActivateUpdate,
		},
	})

	serverApp := httpapi.New(cfg, db, collector.NewManager(cfg, db))
	probe := newServerProductCompleteProbe(serverApp)
	updateSvc := cpaupdateservice.New(db, mockRT, model.RuntimeModeEmbedded).WithProductCompleteProbe(probe)
	serverApp.AppContext().CPAUpdateService = updateSvc

	now := time.Now().UTC()
	seedBytes := []byte(`{"schema_version":1,"last_attempt_at":"` + now.Format(time.RFC3339Nano) + `","last_success_at":"` + now.Format(time.RFC3339Nano) + `","target_version":"8.3.7"}`)
	if err := db.SaveCPAUpdateCheck(ctx, seedBytes); err != nil {
		t.Fatal(err)
	}

	infoBefore, err := serverApp.AppContext().SetupService.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !infoBefore.AdminReady || infoBefore.ProjectInitialized || !infoBefore.SetupRequired {
		t.Fatalf("unexpected pre-setup Info(): %+v", infoBefore)
	}
	if complete, err := probe(ctx); err != nil || complete {
		t.Fatalf("pre-setup probe = (%v, %v), want (false, nil)", complete, err)
	}
	statusBefore, err := updateSvc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if statusBefore.State != cpaupdateservice.StateUpdateAvailable || statusBefore.Actionable {
		t.Fatalf("pre-setup CPAUpdateService.Status = %+v, want update_available with Actionable=false", statusBefore)
	}
	prepReq := cpaupdateservice.MutationRequest{
		RequestID:                "live-setup-probe-req",
		TargetVersion:            "8.3.7",
		ExpectedActiveArtifactID: activeArtifact,
	}
	if _, err := updateSvc.Prepare(ctx, prepReq); err == nil {
		t.Fatal("Prepare succeeded while SetupRequired=true")
	} else if kind, code, ok := cpaupdateservice.MutationErrorDetails(err); !ok || kind != cpaupdateservice.MutationErrorConflict || code != "product_incomplete" {
		t.Fatalf("pre-setup Prepare error = (%v, %q, %q), want conflict/product_incomplete", err, kind, code)
	}
	if mockRT.prepareCalls.Load() != 0 {
		t.Fatalf("pre-setup Prepare invoked runtime %d times, want 0", mockRT.prepareCalls.Load())
	}

	const mgmtKey = "cpa-mgmt-key-live-probe"
	cpaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v0/management/config" && r.Header.Get("Authorization") == "Bearer "+mgmtKey {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer cpaSrv.Close()

	monitoringDisabled := false
	if _, err := serverApp.AppContext().SetupService.Setup(ctx, setupservice.Request{
		CPAUpstreamURL:           cpaSrv.URL,
		CPAManagementKey:         mgmtKey,
		RequestMonitoringEnabled: &monitoringDisabled,
	}, ""); err != nil {
		t.Fatalf("SetupService.Setup failed: %v", err)
	}

	if complete, err := probe(ctx); err != nil || !complete {
		t.Fatalf("post-setup probe = (%v, %v), want (true, nil)", complete, err)
	}
	statusAfter, err := updateSvc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if statusAfter.State != cpaupdateservice.StateUpdateAvailable || !statusAfter.Actionable {
		t.Fatalf("post-setup CPAUpdateService.Status = %+v, want Actionable=true", statusAfter)
	}
	prepRes, err := updateSvc.Prepare(ctx, prepReq)
	if err != nil || prepRes.State != string(model.RuntimeOperationSucceeded) {
		t.Fatalf("post-setup Prepare = (%+v, %v), want succeeded", prepRes, err)
	}
	if mockRT.prepareCalls.Load() != 1 {
		t.Fatalf("post-setup Prepare calls = %d, want 1", mockRT.prepareCalls.Load())
	}
}
