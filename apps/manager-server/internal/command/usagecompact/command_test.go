package usagecompact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestRunUsesConfiguredDatabaseWithoutCreatingDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	t.Setenv("USAGE_DB_PATH", path)
	configPath := filepath.Join(t.TempDir(), "missing-config.json")
	t.Setenv("CPA_MANAGER_CONFIG", configPath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), nil, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v, stderr=%s", err, stderr.String())
	}
	var result sqlite.CompactResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode output %q: %v", stdout.String(), err)
	}
	if result.DatabasePath != canonicalExistingPath(t, path) || !result.IntegrityVerified {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("default config was unexpectedly created: %v", err)
	}
}

func TestRunDBPathFlagOverridesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "override.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	malformedConfig := filepath.Join(t.TempDir(), "malformed-config.json")
	if err := os.WriteFile(malformedConfig, []byte(`{"dbPath":`), 0o600); err != nil {
		t.Fatalf("write malformed config: %v", err)
	}
	t.Setenv("CPA_MANAGER_CONFIG", malformedConfig)
	t.Setenv("USAGE_DB_PATH", filepath.Join(t.TempDir(), "must-not-be-created.sqlite"))

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"--db-path", path}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var result sqlite.CompactResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	wantPath := canonicalExistingPath(t, path)
	if result.DatabasePath != wantPath {
		t.Fatalf("database path = %q, want %q", result.DatabasePath, wantPath)
	}
}

func TestRunRejectsActiveManagerProcessLockBeforeChangingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if _, err := db.Exec(`insert into settings (key, value, updated_at_ms) values ('compact-lock', 'unchanged', 1)`); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture before command: %v", err)
	}
	databaseLock, err := processlock.Acquire(path)
	if err != nil {
		t.Fatalf("hold manager process lock: %v", err)
	}
	defer databaseLock.Close()

	err = Run(context.Background(), []string{"--db-path", path}, &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, processlock.ErrLocked) || !strings.Contains(err.Error(), "stop Manager Server") {
		t.Fatalf("Run() error = %v, want process lock guidance", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture after command: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("database bytes changed while the Manager Server process lock was held")
	}
}

func TestRunHonorsCanceledContextBeforeAcquiringProcessLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture before command: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = Run(ctx, []string{"--db-path", path}, &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(path + ".manager.lock"); !os.IsNotExist(err) {
		t.Fatalf("process lock file was unexpectedly created: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture after command: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("database bytes changed after canceled compact-usage command")
	}
}

func TestRunCancelsInFlightCompactionAndReleasesProcessLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- runWithCompactor(
			ctx,
			[]string{"--db-path", path},
			&bytes.Buffer{},
			&bytes.Buffer{},
			func(runCtx context.Context, _ string, _ sqlite.CompactProgressFunc) (sqlite.CompactResult, error) {
				close(started)
				<-runCtx.Done()
				return sqlite.CompactResult{}, runCtx.Err()
			},
		)
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight compaction did not start")
	}
	secondLock, err := processlock.Acquire(path)
	if secondLock != nil || !errors.Is(err, processlock.ErrLocked) {
		if secondLock != nil {
			_ = secondLock.Close()
		}
		t.Fatalf("process lock during compaction = %#v err=%v, want ErrLocked", secondLock, err)
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("in-flight Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight compaction did not stop after cancellation")
	}
	reacquired, err := processlock.Acquire(path)
	if err != nil {
		t.Fatalf("reacquire process lock after cancellation: %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatalf("release reacquired process lock: %v", err)
	}
}

type threadSafeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *threadSafeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *threadSafeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *threadSafeBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *threadSafeBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Bytes()
}

func TestRunEmitsProgressToStderrAndPureJSONToStdout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	var stdout threadSafeBuffer
	var stderr threadSafeBuffer

	stagesReported := make([]sqlite.CompactStage, 0)
	var stagesMu sync.Mutex

	mockCompactor := func(ctx context.Context, p string, progress sqlite.CompactProgressFunc) (sqlite.CompactResult, error) {
		allStages := []sqlite.CompactStage{
			sqlite.CompactStagePrepare,
			sqlite.CompactStagePreflight,
			sqlite.CompactStageBaseline,
			sqlite.CompactStageCheckpoint,
			sqlite.CompactStageVacuum,
			sqlite.CompactStageVerify,
			sqlite.CompactStageFinalize,
		}
		for _, s := range allStages {
			stagesMu.Lock()
			stagesReported = append(stagesReported, s)
			stagesMu.Unlock()
			if progress != nil {
				progress(s)
			}
		}
		return sqlite.CompactResult{
			DatabasePath:      p,
			IntegrityVerified: true,
			ReclaimedBytes:    1024,
		}, nil
	}

	if err := runWithCompactor(context.Background(), []string{"--db-path", path}, &stdout, &stderr, mockCompactor); err != nil {
		t.Fatalf("runWithCompactor() error = %v", err)
	}

	// Verify stdout is pure JSON
	var result sqlite.CompactResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode stdout JSON failed: %v, raw stdout: %q", err, stdout.String())
	}
	if !result.IntegrityVerified || result.ReclaimedBytes != 1024 {
		t.Fatalf("unexpected result from stdout: %#v", result)
	}
	for _, forbidden := range []string{
		"[compact-usage]",
		"stage:",
		"still running",
		"database ownership acquired",
		"compaction completed",
	} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("stdout unexpectedly contains progress output %q: %s", forbidden, stdout.String())
		}
	}

	// Verify stderr contains progress messages
	stderrOutput := stderr.String()
	for _, expected := range []string{
		"[compact-usage] database ownership acquired",
		"[compact-usage] stage: preparing database",
		"[compact-usage] stage: running preflight checks",
		"[compact-usage] stage: reading baseline statistics",
		"[compact-usage] stage: checkpointing WAL",
		"[compact-usage] stage: vacuuming database",
		"[compact-usage] stage: verifying database integrity and logical consistency",
		"[compact-usage] stage: reading final database statistics",
		"[compact-usage] compaction completed",
	} {
		if !strings.Contains(stderrOutput, expected) {
			t.Fatalf("stderr missing expected progress string %q\nFull stderr:\n%s", expected, stderrOutput)
		}
	}
}

func TestRunEmitsHeartbeatDuringLongRunningStage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	var stdout threadSafeBuffer
	var stderr threadSafeBuffer

	heartbeatObserved := make(chan struct{})
	allowCompactorToFinish := make(chan struct{})

	mockCompactor := func(ctx context.Context, p string, progress sqlite.CompactProgressFunc) (sqlite.CompactResult, error) {
		if progress != nil {
			progress(sqlite.CompactStageVacuum)
		}
		// Poll stderr for the heartbeat
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		timeout := time.After(5 * time.Second)
		observed := false
		for !observed {
			select {
			case <-timeout:
				return sqlite.CompactResult{}, errors.New("timeout waiting for heartbeat in compactor")
			case <-ticker.C:
				if strings.Contains(stderr.String(), "still running: vacuuming database") {
					observed = true
					close(heartbeatObserved)
				}
			}
		}
		select {
		case <-allowCompactorToFinish:
		case <-time.After(5 * time.Second):
			return sqlite.CompactResult{}, errors.New("timeout waiting to finish")
		}
		return sqlite.CompactResult{
			DatabasePath:      p,
			IntegrityVerified: true,
		}, nil
	}

	cmdErrChan := make(chan error, 1)
	go func() {
		cmdErrChan <- runWithCompactorAndInterval(
			context.Background(),
			[]string{"--db-path", path},
			&stdout,
			&stderr,
			mockCompactor,
			10*time.Millisecond,
		)
	}()

	select {
	case <-heartbeatObserved:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat was not observed in stderr")
	}

	close(allowCompactorToFinish)

	select {
	case err := <-cmdErrChan:
		if err != nil {
			t.Fatalf("runWithCompactorAndInterval error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command did not complete in time")
	}

	stderrOutput := stderr.String()
	if !strings.Contains(stderrOutput, "still running: vacuuming database") {
		t.Fatalf("stderr missing heartbeat message:\n%s", stderrOutput)
	}
	if !strings.Contains(stderrOutput, "compaction completed") {
		t.Fatalf("stderr missing compaction completed message:\n%s", stderrOutput)
	}
}

func TestRunHeartbeatStopsPromptlyAfterCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	var stdout threadSafeBuffer
	var stderr threadSafeBuffer

	mockCompactor := func(ctx context.Context, p string, progress sqlite.CompactProgressFunc) (sqlite.CompactResult, error) {
		if progress != nil {
			progress(sqlite.CompactStageVacuum)
		}
		return sqlite.CompactResult{
			DatabasePath:      p,
			IntegrityVerified: true,
		}, nil
	}

	interval := 10 * time.Millisecond
	if err := runWithCompactorAndInterval(context.Background(), []string{"--db-path", path}, &stdout, &stderr, mockCompactor, interval); err != nil {
		t.Fatalf("runWithCompactorAndInterval error: %v", err)
	}

	initialLen := stderr.Len()
	// Wait more than 4x the heartbeat interval
	time.Sleep(50 * time.Millisecond)
	afterLen := stderr.Len()

	if afterLen != initialLen {
		t.Fatalf("stderr continued receiving output after command completion: initial %d bytes, after %d bytes\nExtra output:\n%s",
			initialLen, afterLen, stderr.String()[initialLen:])
	}
}

func TestRunRejectsMissingDatabaseWithoutCreatingProcessLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "usage.sqlite")
	err := Run(context.Background(), []string{"--db-path", path}, &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, sqlite.ErrMaintenanceInvalidDatabase) {
		t.Fatalf("Run() error = %v, want ErrMaintenanceInvalidDatabase", err)
	}
	if _, err := os.Stat(path + ".manager.lock"); !os.IsNotExist(err) {
		t.Fatalf("process lock file was unexpectedly created: %v", err)
	}
}

func TestRunHelpReturnsWithoutLoadingOrCreatingConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "missing-config.json")
	t.Setenv("CPA_MANAGER_CONFIG", configPath)
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"--help"}, &bytes.Buffer{}, &stderr)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("Run() error = %v, want flag.ErrHelp", err)
	}
	if !strings.Contains(stderr.String(), "Usage: cpa-manager-plus compact-usage") ||
		!strings.Contains(stderr.String(), "Stop Manager Server") {
		t.Fatalf("help output = %q", stderr.String())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("default config was unexpectedly created: %v", err)
	}
}

func canonicalExistingPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve canonical fixture path: %v", err)
	}
	return filepath.Clean(resolved)
}
