package upgrade

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

func runWithErr(t *testing.T, args ...string) (int, string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), args, &stdout, &stderr)
	return ExitCode(err), stdout.String(), stderr.String(), err
}

func TestCLIRefusalCodesForSourceFailuresExitOne(t *testing.T) {
	source := copyReadySource(t)
	target, bundle := reconcileRoots(t)
	if code, _, stderr := run(t, "run", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
		t.Fatalf("initial run failed: code=%d stderr=%s", code, stderr)
	}

	// Replace a source archive segment with a symlink so reconcile hits CodeSourceSymlinked.
	seg := filepath.Join(source, "usage-archives", "2026-03", "segment-000001.jsonl")
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(seg); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, seg); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	code, stdout, stderr := run(t, "reconcile", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle, "--json")
	if code != 1 {
		t.Fatalf("reconcile with symlinked source exited %d, want 1; stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, upgradeservice.CodeSourceSymlinked) {
		t.Fatalf("stdout = %s, want %s", stdout, upgradeservice.CodeSourceSymlinked)
	}

	// Restore segment and make it unreadable (0000) so reconcile hits CodeSourceUnreadable and exits 1.
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		if err := os.Remove(seg); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(seg, []byte(`{"events":1}`+"\n"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(seg, 0o600) })
		code2, stdout2, stderr2 := run(t, "reconcile", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle, "--json")
		if code2 != 1 {
			t.Fatalf("reconcile with unreadable source exited %d, want 1; stdout=%s stderr=%s", code2, stdout2, stderr2)
		}
		if !strings.Contains(stdout2, upgradeservice.CodeSourceUnreadable) {
			t.Fatalf("stdout = %s, want %s", stdout2, upgradeservice.CodeSourceUnreadable)
		}
	}
}

func TestCLIRootOverlapAndScratchExclusionsR7(t *testing.T) {
	t.Run("cpa-import refuses target overlapping cpa-dir or cpa-auth-dir and scratch inside cpa-dir, cpa-auth-dir, or external bundle", func(t *testing.T) {
		legacy := copyReadySource(t)
		target, bundle := reconcileRoots(t)
		if code, _, stderr := run(t, "run", "--legacy", legacy, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
			t.Fatalf("run failed: code=%d stderr=%s", code, stderr)
		}

		cpaDir := t.TempDir()
		cpaAuthDir := t.TempDir()

		// --target inside --cpa-dir refused before creating lock files inside cpaDir.
		nestedInCPA := filepath.Join(cpaDir, "nested-target")
		code, stdout, stderr := run(t, "cpa-import",
			"--legacy", legacy,
			"--target", nestedInCPA,
			"--cpa-dir", cpaDir,
			"--cpa-auth-dir", cpaAuthDir,
			"--cpa-version", "8.0.55",
			"--plugins", "false",
			"--destination-url", "http://127.0.0.1:8317",
			"--json",
		)
		if code == 0 {
			t.Fatalf("expected non-zero exit for --target inside --cpa-dir; stdout=%s stderr=%s", stdout, stderr)
		}
		if _, err := os.Lstat(nestedInCPA); !os.IsNotExist(err) {
			t.Fatalf("refused cpa-import created %s inside cpaDir", nestedInCPA)
		}

		// --target inside --cpa-auth-dir refused before creating lock files inside cpaAuthDir.
		nestedInAuth := filepath.Join(cpaAuthDir, "nested-target")
		code, stdout, stderr = run(t, "cpa-import",
			"--legacy", legacy,
			"--target", nestedInAuth,
			"--cpa-dir", cpaDir,
			"--cpa-auth-dir", cpaAuthDir,
			"--cpa-version", "8.0.55",
			"--plugins", "false",
			"--destination-url", "http://127.0.0.1:8317",
			"--json",
		)
		if code == 0 {
			t.Fatalf("expected non-zero exit for --target inside --cpa-auth-dir; stdout=%s stderr=%s", stdout, stderr)
		}
		if _, err := os.Lstat(nestedInAuth); !os.IsNotExist(err) {
			t.Fatalf("refused cpa-import created %s inside cpaAuthDir", nestedInAuth)
		}

		// --scratch-dir inside --cpa-dir, --cpa-auth-dir, or --bundle refused with snapshot_failed (exit 2).
		for _, badScratch := range []string{cpaDir, cpaAuthDir, bundle} {
			code, stdout, stderr = run(t, "cpa-import",
				"--legacy", legacy,
				"--target", target,
				"--bundle", bundle,
				"--cpa-dir", cpaDir,
				"--cpa-auth-dir", cpaAuthDir,
				"--scratch-dir", badScratch,
				"--cpa-version", "8.0.55",
				"--plugins", "false",
				"--destination-url", "http://127.0.0.1:8317",
				"--json",
			)
			if code != 2 {
				t.Fatalf("cpa-import with scratch=%s exited %d, want 2; stdout=%s stderr=%s", badScratch, code, stdout, stderr)
			}
			if !strings.Contains(stdout, upgradeservice.CodeSnapshotFailed) {
				t.Fatalf("cpa-import stdout=%s, want %s", stdout, upgradeservice.CodeSnapshotFailed)
			}
		}
	})

	t.Run("external-connect, status, and verify exclude CPAMP_UPGRADE_LEGACY_ROOT and --bundle/--backup from scratch", func(t *testing.T) {
		legacy := copyReadySource(t)
		target, bundle := reconcileRoots(t)
		if code, _, stderr := run(t, "run", "--legacy", legacy, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
			t.Fatalf("run failed: code=%d stderr=%s", code, stderr)
		}
		keyFile := filepath.Join(t.TempDir(), "mgmt.key")
		if err := os.WriteFile(keyFile, []byte("external-management-key-123456"), 0o600); err != nil {
			t.Fatal(err)
		}

		t.Setenv("CPAMP_UPGRADE_LEGACY_ROOT", legacy)

		// status with --scratch-dir inside CPAMP_UPGRADE_LEGACY_ROOT refused.
		code, _, _, runErr := runWithErr(t, "status", "--db-path", filepath.Join(target, upgradeservice.SourceDatabaseName), "--scratch-dir", legacy)
		if code != 2 || runErr == nil || !strings.Contains(runErr.Error(), upgradeservice.CodeSnapshotFailed) {
			t.Fatalf("status with scratch inside CPAMP_UPGRADE_LEGACY_ROOT: code=%d err=%v", code, runErr)
		}

		// status with --backup and --scratch-dir inside external backup refused.
		code, _, _, runErr = runWithErr(t, "status", "--db-path", filepath.Join(target, upgradeservice.SourceDatabaseName), "--backup", bundle, "--scratch-dir", bundle)
		if code != 2 || runErr == nil || !strings.Contains(runErr.Error(), upgradeservice.CodeSnapshotFailed) {
			t.Fatalf("status with scratch inside --backup: code=%d err=%v", code, runErr)
		}

		// external-connect with --scratch-dir inside CPAMP_UPGRADE_LEGACY_ROOT refused.
		code, stdout, stderr := run(t, "external-connect",
			"--target", target,
			"--scratch-dir", legacy,
			"--cpa-base-url", "http://127.0.0.1:8317",
			"--management-key-file", keyFile,
			"--json",
		)
		if code != 2 || !strings.Contains(stdout, upgradeservice.CodeSnapshotFailed) {
			t.Fatalf("external-connect with scratch inside CPAMP_UPGRADE_LEGACY_ROOT: code=%d stdout=%s stderr=%s", code, stdout, stderr)
		}

		// external-connect with --bundle and --scratch-dir inside external bundle refused.
		code, stdout, stderr = run(t, "external-connect",
			"--target", target,
			"--bundle", bundle,
			"--scratch-dir", bundle,
			"--cpa-base-url", "http://127.0.0.1:8317",
			"--management-key-file", keyFile,
			"--json",
		)
		if code != 2 || !strings.Contains(stdout, upgradeservice.CodeSnapshotFailed) {
			t.Fatalf("external-connect with scratch inside --bundle: code=%d stdout=%s stderr=%s", code, stdout, stderr)
		}

		// verify with --bundle and --scratch-dir inside external bundle refused.
		code, stdout, stderr = run(t, "verify",
			"--legacy", legacy,
			"--target", target,
			"--bundle", bundle,
			"--scratch-dir", bundle,
			"--control-base-path", "/cpamp-admin",
			"--json",
		)
		if code != 2 || !strings.Contains(stdout, upgradeservice.CodeSnapshotFailed) {
			t.Fatalf("verify with scratch inside --bundle: code=%d stdout=%s stderr=%s", code, stdout, stderr)
		}
	})

	t.Run("preflight and run refuse overlapping roots up front even when target is past copy_in", func(t *testing.T) {
		legacy := copyReadySource(t)
		target, bundle := reconcileRoots(t)
		if code, _, stderr := run(t, "run", "--legacy", legacy, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
			t.Fatalf("run failed: code=%d stderr=%s", code, stderr)
		}
		if code, _, stderr := run(t, "reconcile", "--legacy", legacy, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
			t.Fatalf("reconcile failed: code=%d stderr=%s", code, stderr)
		}

		// run --legacy <target> --target <target> must refuse overlap (exit 2), NOT report 0.
		code, stdout, _, runErr := runWithErr(t, "run", "--legacy", target, "--target", target)
		if code != 2 || runErr == nil || !strings.Contains(runErr.Error(), "must not contain each other") {
			t.Fatalf("run with legacy==target exited %d, want 2; stdout=%s err=%v", code, stdout, runErr)
		}

		// preflight --legacy <legacy> --target-dir <legacy/sub> must refuse overlap (exit 2).
		nested := filepath.Join(legacy, "nested")
		code, stdout, _, runErr = runWithErr(t, "preflight", "--legacy", legacy, "--target-dir", nested)
		if code != 2 || runErr == nil || !strings.Contains(runErr.Error(), "must not contain each other") {
			t.Fatalf("preflight with nested target-dir exited %d, want 2; stdout=%s err=%v", code, stdout, runErr)
		}
		if _, err := os.Lstat(nested); !os.IsNotExist(err) {
			t.Fatalf("refused preflight created %s", nested)
		}
	})
}
