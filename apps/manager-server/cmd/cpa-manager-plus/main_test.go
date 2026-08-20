package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
)

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
		"db.RunDerivedStartupMaintenance(ctx)",
		"automationRuntime.Start(ctx)",
		"codexInspectionWorker.Start(ctx)",
		"accountHistoryRollupWorker.Start(ctx)",
		"usageDerivedRollupWorker.Start(ctx)",
		"usageHourlyAggregateWorker.Start(ctx)",
		"db.StartDerivedMaintenance(ctx)",
		"collectorWorker.Start(ctx)",
		"NewLegacyQuotaSnapshotMigrationWorker(db).Start(ctx)",
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
	if !strings.Contains(source, "cfg.UsageArchiveRetentionEnabled && cfg.UsageArchiveRetentionDays > 0 && cfg.DashboardHourlyRollupEnabled") {
		t.Fatal("retention worker construction is not gated by explicit enablement, positive days, and hourly rollups")
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
	lockAt := strings.Index(source, "processlock.Acquire(cfg.DBPath)")
	storeOpenAt := strings.Index(source, "store.Open(cfg.DBPath, protector)")
	lockCloseAt := strings.Index(source, "databaseLock.Close()")
	if lockAt < 0 || storeOpenAt < lockAt || lockCloseAt < lockAt {
		t.Fatalf("database lock ordering invalid: lock=%d open=%d close=%d", lockAt, storeOpenAt, lockCloseAt)
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

func startManagerServerProcess(t testing.TB, dataDir, dbPath string) *managerServerProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestManagerServerHelperProcess$")
	cmd.Env = managerServerTestEnvironment(dataDir, dbPath)
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
		"CPA_MANAGER_TEST_HELPER":               "1",
		"CPA_MANAGER_ADMIN_KEY":                 "cpamp_startup_migration_test_admin_key",
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
	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal manager server helper: %v", err)
	}
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatalf("manager server helper shutdown: %v\n%s", err, p.logs.String())
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
