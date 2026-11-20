package upgrade

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen ephemeral loopback port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestCPAImportCLIRefusesOverlappingTargetAndEmptyTargetWithoutCreatingLocks(t *testing.T) {
	t.Run("target inside legacy refuses without creating target dirs or locks inside legacy", func(t *testing.T) {
		legacy := copyReadySource(t)
		cpaDir := t.TempDir()
		nestedTarget := filepath.Join(legacy, "nested-v2-target")

		code, stdout, stderr := run(t, "cpa-import",
			"--legacy", legacy,
			"--target", nestedTarget,
			"--cpa-dir", cpaDir,
			"--cpa-version", "8.0.55",
			"--plugins", "false",
			"--destination-url", "http://127.0.0.1:8317",
			"--json",
		)
		if code == 0 {
			t.Fatalf("expected non-zero exit for --target inside --legacy; stdout=%s stderr=%s", stdout, stderr)
		}
		if _, err := os.Lstat(nestedTarget); !os.IsNotExist(err) {
			t.Fatalf("refused overlapping cpa-import created %s (err=%v)", nestedTarget, err)
		}
		if _, err := os.Lstat(filepath.Join(nestedTarget, upgradeservice.SourceDatabaseName+".manager.lock")); !os.IsNotExist(err) {
			t.Fatalf("refused overlapping cpa-import created target lock inside legacy (err=%v)", err)
		}
	})

	t.Run("empty target refuses before creating target database lock file", func(t *testing.T) {
		legacy := copyReadySource(t)
		emptyTarget := t.TempDir()
		cpaDir := t.TempDir()
		runtimeToken := filepath.Join(t.TempDir(), "runtime.token")
		if err := os.WriteFile(runtimeToken, []byte("test-token"), 0o600); err != nil {
			t.Fatalf("write runtime token: %v", err)
		}
		t.Setenv("CPAMP_RUNTIME_URL", "http://127.0.0.1:1")
		t.Setenv("CPAMP_RUNTIME_TOKEN_FILE", runtimeToken)

		code, stdout, stderr := run(t, "cpa-import",
			"--legacy", legacy,
			"--target", emptyTarget,
			"--cpa-dir", cpaDir,
			"--cpa-version", "8.0.55",
			"--plugins", "false",
			"--destination-url", "http://127.0.0.1:8317",
			"--json",
		)
		if code == 0 {
			t.Fatalf("expected non-zero exit for empty target cpa-import; stdout=%s stderr=%s", stdout, stderr)
		}
		lockPath := filepath.Join(emptyTarget, upgradeservice.SourceDatabaseName+".manager.lock")
		if _, err := os.Lstat(lockPath); !os.IsNotExist(err) {
			t.Fatalf("refused cpa-import on empty target created stray lock %s (err=%v)", lockPath, err)
		}
	})

	t.Run("pre-existing unlocked target lock at copy_in returns stage conflict before runtime usage check", func(t *testing.T) {
		source := copyReadySource(t)
		target, bundle := reconcileRoots(t)
		if code, _, stderr := run(t, "run", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
			t.Fatalf("run failed: code=%d stderr=%s", code, stderr)
		}
		// Ensure usage.sqlite.manager.lock exists on disk and is unlocked, while
		// CPAMP_RUNTIME_URL is unset so skipping the pre-fence stage gate would
		// hit cpaImportRuntime() and exit 2 instead of stage conflict exit 1.
		lockPath := filepath.Join(target, upgradeservice.SourceDatabaseName+".manager.lock")
		if _, err := os.Lstat(lockPath); err != nil {
			t.Fatalf("expected pre-existing target lock file %s after copy-in: %v", lockPath, err)
		}
		t.Setenv("CPAMP_RUNTIME_URL", "")
		t.Setenv("CPAMP_RUNTIME_TOKEN_FILE", "")

		cpaDir := t.TempDir()
		code, stdout, stderr := run(t, "cpa-import",
			"--legacy", source,
			"--target", target,
			"--cpa-dir", cpaDir,
			"--cpa-version", "8.0.55",
			"--plugins", "false",
			"--destination-url", "http://127.0.0.1:8317",
			"--json",
		)
		if code != 1 {
			t.Fatalf("cpa-import at copy_in with pre-existing unlocked target lock exited %d, want 1; stdout=%s stderr=%s", code, stdout, stderr)
		}
		var out cpaImportOutput
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("decode cpa-import output: %v; stdout=%s", err, stdout)
		}
		if out.ErrorCode != string(upgradeservice.UpgradeErrorCPAStageConflict) {
			t.Fatalf("errorCode = %q, want %s; stdout=%s stderr=%s", out.ErrorCode, upgradeservice.UpgradeErrorCPAStageConflict, stdout, stderr)
		}
	})

	t.Run("legacy equals target with unlocked read-only lock refuses overlap without writable open", func(t *testing.T) {
		legacy := copyReadySource(t)
		lockPath := filepath.Join(legacy, upgradeservice.SourceDatabaseName+".manager.lock")
		if runtime.GOOS != "windows" && os.Geteuid() != 0 {
			if err := os.Chmod(lockPath, 0o400); err != nil {
				t.Fatalf("chmod source lock read-only: %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(lockPath, 0o600) })
		}
		cpaDir := t.TempDir()
		code, stdout, stderr := run(t, "cpa-import",
			"--legacy", legacy,
			"--target", legacy,
			"--cpa-dir", cpaDir,
			"--cpa-version", "8.0.55",
			"--plugins", "false",
			"--destination-url", "http://127.0.0.1:8317",
			"--json",
		)
		if code == 0 {
			t.Fatalf("expected non-zero exit for --legacy == --target; stdout=%s stderr=%s", stdout, stderr)
		}
		var out cpaImportOutput
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("decode cpa-import overlap output: %v; stdout=%s stderr=%s", err, stdout, stderr)
		}
		if out.ErrorCode == string(upgradeservice.UpgradeErrorStoreLocked) || !strings.Contains(out.Error, "must not contain each other") {
			t.Fatalf("expected clean overlap refusal rather than store_locked O_RDWR failure, got errorCode=%q error=%q stderr=%q", out.ErrorCode, out.Error, stderr)
		}
	})
}

func TestUpgradeCLIScratchDirOverridesTMPDIRAcrossSubcommands(t *testing.T) {
	t.Run("cpa-import --scratch-dir overrides TMPDIR inside legacy", func(t *testing.T) {
		source := copyReadySource(t)
		target, bundle := reconcileRoots(t)
		if code, _, stderr := run(t, "run", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
			t.Fatalf("run failed: code=%d stderr=%s", code, stderr)
		}
		if code, _, stderr := run(t, "reconcile", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
			t.Fatalf("reconcile failed: code=%d stderr=%s", code, stderr)
		}
		cpaDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(cpaDir, "config.yaml"), []byte("config-version: 8\n"), 0o600); err != nil {
			t.Fatalf("seed cpa config: %v", err)
		}
		runtimeToken := filepath.Join(t.TempDir(), "runtime.token")
		if err := os.WriteFile(runtimeToken, []byte("test-token"), 0o600); err != nil {
			t.Fatalf("write runtime token: %v", err)
		}
		rtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		t.Cleanup(rtServer.Close)
		t.Setenv("CPAMP_RUNTIME_URL", rtServer.URL)
		t.Setenv("CPAMP_RUNTIME_TOKEN_FILE", runtimeToken)
		t.Setenv("TMPDIR", source)

		code, stdout, stderr := run(t, "cpa-import",
			"--legacy", source,
			"--target", target,
			"--cpa-dir", cpaDir,
			"--cpa-version", "8.0.55",
			"--plugins", "false",
			"--destination-url", "http://127.0.0.1:8317",
			"--json",
		)
		var out cpaImportOutput
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("decode cpa-import output without --scratch-dir: %v; stdout=%s stderr=%s", err, stdout, stderr)
		}
		if code == 0 || out.ErrorCode != string(upgradeservice.UpgradeErrorSnapshotFailed) {
			t.Fatalf("cpa-import without --scratch-dir code=%d errCode=%q, want %s", code, out.ErrorCode, upgradeservice.UpgradeErrorSnapshotFailed)
		}

		cleanScratch := t.TempDir()
		code, stdout, stderr = run(t, "cpa-import",
			"--legacy", source,
			"--target", target,
			"--scratch-dir", cleanScratch,
			"--cpa-dir", cpaDir,
			"--cpa-version", "8.0.55",
			"--plugins", "false",
			"--destination-url", "http://127.0.0.1:8317",
			"--json",
		)
		var outWithScratch cpaImportOutput
		if err := json.Unmarshal([]byte(stdout), &outWithScratch); err != nil {
			t.Fatalf("decode cpa-import output with --scratch-dir: %v; stdout=%s stderr=%s", err, stdout, stderr)
		}
		if code == 0 || outWithScratch.ErrorCode == string(upgradeservice.UpgradeErrorSnapshotFailed) {
			t.Fatalf("cpa-import with --scratch-dir still failed on snapshot (code=%d errCode=%q): %s", code, outWithScratch.ErrorCode, stderr)
		}
	})

	t.Run("external-connect --scratch-dir overrides TMPDIR inside target", func(t *testing.T) {
		source := copyReadySource(t)
		target, bundle := reconcileRoots(t)
		if code, _, stderr := run(t, "run", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
			t.Fatalf("run failed: code=%d stderr=%s", code, stderr)
		}
		if code, _, stderr := run(t, "reconcile", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
			t.Fatalf("reconcile failed: code=%d stderr=%s", code, stderr)
		}
		server := newExternalCPAServer(t, "v8.0.13")
		keyFile := managementKeyFile(t, cliManagementKey)
		t.Setenv("TMPDIR", target)

		code, stdout, stderr := run(t, "external-connect",
			"--target", target,
			"--cpa-base-url", server.URL,
			"--management-key-file", keyFile,
			"--json",
		)
		var blocked externalConnectOutput
		if err := json.Unmarshal([]byte(stdout), &blocked); err != nil {
			t.Fatalf("decode external-connect output without --scratch-dir: %v; stdout=%s stderr=%s", err, stdout, stderr)
		}
		if code == 0 || blocked.ErrorCode != string(upgradeservice.UpgradeErrorSnapshotFailed) {
			t.Fatalf("external-connect without --scratch-dir code=%d errCode=%q, want %s", code, blocked.ErrorCode, upgradeservice.UpgradeErrorSnapshotFailed)
		}

		cleanScratch := t.TempDir()
		code, stdout, stderr = run(t, "external-connect",
			"--target", target,
			"--scratch-dir", cleanScratch,
			"--cpa-base-url", server.URL,
			"--management-key-file", keyFile,
			"--json",
		)
		if code != 0 {
			t.Fatalf("external-connect with --scratch-dir failed: code=%d stdout=%s stderr=%s", code, stdout, stderr)
		}
	})

	t.Run("verify --scratch-dir overrides TMPDIR inside legacy", func(t *testing.T) {
		server := newExternalCPAServer(t, "v8.0.13")
		source, target := verifiedTarget(t, server.URL)
		t.Setenv("TMPDIR", source)

		code, stdout, stderr := run(t, "verify",
			"--legacy", source,
			"--target", target,
			"--control-base-path", testControlBasePath,
			"--json",
		)
		var blocked verifyOutput
		if err := json.Unmarshal([]byte(stdout), &blocked); err != nil {
			t.Fatalf("decode verify output without --scratch-dir: %v; stdout=%s stderr=%s", err, stdout, stderr)
		}
		if code == 0 || blocked.ErrorCode != string(upgradeservice.UpgradeErrorSnapshotFailed) {
			t.Fatalf("verify without --scratch-dir code=%d errCode=%q, want %s", code, blocked.ErrorCode, upgradeservice.UpgradeErrorSnapshotFailed)
		}

		cleanScratch := t.TempDir()
		code, stdout, stderr = run(t, "verify",
			"--legacy", source,
			"--target", target,
			"--scratch-dir", cleanScratch,
			"--control-base-path", testControlBasePath,
			"--json",
		)
		if code != 0 {
			t.Fatalf("verify with --scratch-dir failed: code=%d stdout=%s stderr=%s", code, stdout, stderr)
		}
	})

	t.Run("rollback --prepare and --verify --scratch-dir override TMPDIR inside legacy", func(t *testing.T) {
		source := copyReadySource(t)
		target, bundle := reconcileRoots(t)
		if code, _, stderr := run(t, "run", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
			t.Fatalf("run failed: code=%d stderr=%s", code, stderr)
		}
		managerAddr := freeLoopbackAddr(t)
		_, rollbackPort, err := net.SplitHostPort(freeLoopbackAddr(t))
		if err != nil {
			t.Fatalf("split free loopback port: %v", err)
		}
		if _, err := strconv.Atoi(rollbackPort); err != nil {
			t.Fatalf("parse free loopback port: %v", err)
		}
		probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != upgradeservice.LegacyHealthPath {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"service":"` + upgradeservice.LegacyServiceID + `"}`))
		}))
		t.Cleanup(probe.Close)
		t.Setenv("TMPDIR", source)

		code, _, stderr := run(t, "rollback",
			"--prepare",
			"--legacy", source,
			"--target", target,
			"--backup", bundle,
			"--port", rollbackPort,
			"--manager-addr", managerAddr,
			"--json",
		)
		if code == 0 || !strings.Contains(stderr, string(upgradeservice.UpgradeErrorSnapshotFailed)) {
			t.Fatalf("rollback --prepare without --scratch-dir code=%d stderr=%q, want %s", code, stderr, upgradeservice.UpgradeErrorSnapshotFailed)
		}

		cleanScratch := t.TempDir()
		code, stdout, stderr := run(t, "rollback",
			"--prepare",
			"--legacy", source,
			"--target", target,
			"--backup", bundle,
			"--port", rollbackPort,
			"--scratch-dir", cleanScratch,
			"--manager-addr", managerAddr,
			"--json",
		)
		if code != 0 {
			t.Fatalf("rollback --prepare with --scratch-dir failed: code=%d stdout=%s stderr=%s", code, stdout, stderr)
		}

		code, _, stderr = run(t, "rollback",
			"--verify",
			"--legacy", source,
			"--target", target,
			"--backup", bundle,
			"--status-url", probe.URL+upgradeservice.LegacyHealthPath,
			"--manager-addr", managerAddr,
			"--json",
		)
		if code == 0 || !strings.Contains(stderr, string(upgradeservice.UpgradeErrorSnapshotFailed)) {
			t.Fatalf("rollback --verify without --scratch-dir code=%d stderr=%q, want %s", code, stderr, upgradeservice.UpgradeErrorSnapshotFailed)
		}

		code, stdout, stderr = run(t, "rollback",
			"--verify",
			"--legacy", source,
			"--target", target,
			"--backup", bundle,
			"--scratch-dir", cleanScratch,
			"--status-url", probe.URL+upgradeservice.LegacyHealthPath,
			"--manager-addr", managerAddr,
			"--json",
		)
		if code != 0 {
			t.Fatalf("rollback --verify with --scratch-dir failed: code=%d stdout=%s stderr=%s", code, stdout, stderr)
		}
	})

	t.Run("run --scratch-dir succeeds when TMPDIR points to read-only legacy and refuses scratch inside --bundle", func(t *testing.T) {
		source := copyReadySource(t)
		target, bundle := reconcileRoots(t)
		cleanScratch := t.TempDir()

		// First verify --scratch-dir inside external --bundle fails closed with zero files written into bundle.
		badScratch := filepath.Join(bundle, "scratch")
		code, stdout, stderr := run(t, "run",
			"--legacy", source,
			"--source-version", bootstrap.PlannedDirectRelease,
			"--target", target,
			"--bundle", bundle,
			"--scratch-dir", badScratch,
			"--json",
		)
		if code == 0 || !strings.Contains(stdout, upgradeservice.CodeSnapshotFailed) {
			t.Fatalf("run with --scratch-dir inside --bundle code=%d stdout=%s stderr=%s, want blocked %s", code, stdout, stderr, upgradeservice.CodeSnapshotFailed)
		}
		if _, err := os.Lstat(bundle); !os.IsNotExist(err) {
			t.Fatalf("run with --scratch-dir inside --bundle created %s (err=%v)", bundle, err)
		}

		// Now set TMPDIR to a read-only --legacy directory and prove explicit --scratch-dir succeeds cleanly.
		beforeEntries, err := os.ReadDir(source)
		if err != nil {
			t.Fatalf("read legacy source before run: %v", err)
		}
		t.Setenv("TMPDIR", source)
		t.Setenv("TMP", source)
		t.Setenv("TEMP", source)
		if runtime.GOOS != "windows" && os.Geteuid() != 0 {
			if err := os.Chmod(source, 0o555); err != nil {
				t.Fatalf("chmod legacy source read-only: %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(source, 0o700) })
		}

		code, stdout, stderr = run(t, "run",
			"--legacy", source,
			"--source-version", bootstrap.PlannedDirectRelease,
			"--target", target,
			"--bundle", bundle,
			"--scratch-dir", cleanScratch,
			"--json",
		)
		if code != 0 {
			t.Fatalf("run with --scratch-dir and read-only TMPDIR=legacy failed: code=%d stdout=%s stderr=%s", code, stdout, stderr)
		}
		afterEntries, err := os.ReadDir(source)
		if err != nil {
			t.Fatalf("read legacy source after run: %v", err)
		}
		if len(afterEntries) != len(beforeEntries) {
			t.Fatalf("legacy source entry count changed: before=%d after=%d", len(beforeEntries), len(afterEntries))
		}

		// Also verify CLI reconcile refuses --scratch-dir inside external --bundle before writing into bundle.
		code, stdout, stderr = run(t, "reconcile",
			"--legacy", source,
			"--source-version", bootstrap.PlannedDirectRelease,
			"--target", target,
			"--bundle", bundle,
			"--scratch-dir", filepath.Join(bundle, "reconcile-scratch"),
			"--json",
		)
		if code == 0 || !strings.Contains(stdout, string(upgradeservice.UpgradeErrorSnapshotFailed)) {
			t.Fatalf("reconcile with --scratch-dir inside --bundle code=%d stdout=%s stderr=%s, want %s", code, stdout, stderr, upgradeservice.UpgradeErrorSnapshotFailed)
		}
		if _, err := os.Lstat(filepath.Join(bundle, "reconcile-scratch")); !os.IsNotExist(err) {
			t.Fatalf("reconcile created scratch inside --bundle (err=%v)", err)
		}
	})
}
