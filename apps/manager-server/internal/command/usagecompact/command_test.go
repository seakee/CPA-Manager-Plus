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
			func(runCtx context.Context, _ string) (sqlite.CompactResult, error) {
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
