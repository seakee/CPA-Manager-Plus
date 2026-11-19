package upgrade

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

func TestCLIBundleBackupSymmetryAndCanonicalEquivalenceR8(t *testing.T) {
	t.Run("run, reconcile, and rollback accept --backup alias and matching relative/absolute --bundle + --backup", func(t *testing.T) {
		isolateManagerAddress(t)
		source := copyReadySource(t)
		target, bundle := reconcileRoots(t)

		origWD, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		parentDir := filepath.Dir(bundle)
		relBundle := "./" + filepath.Base(bundle)
		if err := os.Chdir(parentDir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(origWD) })

		// `run` accepts --bundle ./rel --backup /abs/bundle when they name the same canonical path.
		code, stdout, stderr := run(t,
			"run",
			"--legacy", source,
			"--source-version", bootstrap.PlannedDirectRelease,
			"--target", target,
			"--bundle", relBundle,
			"--backup", bundle,
			"--json",
		)
		if code != 0 {
			t.Fatalf("run with matching --bundle/--backup exited %d; stdout=%s stderr=%s", code, stdout, stderr)
		}

		// `reconcile` accepts --backup alone (symmetric with --bundle).
		recCode, recOut, recErr := run(t,
			"reconcile",
			"--legacy", source,
			"--source-version", bootstrap.PlannedDirectRelease,
			"--target", target,
			"--backup", bundle,
			"--json",
		)
		if recCode != 0 {
			t.Fatalf("reconcile with --backup exited %d; stdout=%s stderr=%s", recCode, recOut, recErr)
		}

		// `rollback --prepare` accepts --bundle alone (symmetric with --backup) and matching --bundle + --backup.
		rbCode, rbOut, rbErr := run(t,
			"rollback",
			"--prepare",
			"--legacy", source,
			"--target", target,
			"--bundle", relBundle,
			"--backup", bundle,
			"--port", strconv.Itoa(freePort(t)),
			"--json",
		)
		if rbCode != 0 {
			t.Fatalf("rollback --prepare with --bundle/--backup exited %d; stdout=%s stderr=%s", rbCode, rbOut, rbErr)
		}
		// `rollback --verify` also accepts --bundle (symmetric with --backup).
		probeURL := healthyProbe(t)
		rbVerCode, rbVerOut, rbVerErr := run(t,
			"rollback",
			"--verify",
			"--legacy", source,
			"--target", target,
			"--bundle", relBundle,
			"--status-url", probeURL,
			"--json",
		)
		if rbVerCode != 0 {
			t.Fatalf("rollback --verify with --bundle exited %d; stdout=%s stderr=%s", rbVerCode, rbVerOut, rbVerErr)
		}

		// Conflicting --bundle and --backup fail with exit code 2 across run, reconcile, and rollback.
		otherBundle := t.TempDir()
		for _, subArgs := range [][]string{
			{"run", "--legacy", source, "--target", t.TempDir(), "--bundle", bundle, "--backup", otherBundle},
			{"reconcile", "--legacy", source, "--target", target, "--bundle", bundle, "--backup", otherBundle},
			{"rollback", "--prepare", "--legacy", source, "--target", target, "--bundle", bundle, "--backup", otherBundle, "--port", strconv.Itoa(freePort(t))},
			{"rollback", "--verify", "--legacy", source, "--target", target, "--bundle", bundle, "--backup", otherBundle, "--status-url", probeURL},
		} {
			c, _, _, runErr := runWithErr(t, subArgs...)
			if c != 2 || runErr == nil || !strings.Contains(runErr.Error(), "--bundle and --backup") {
				t.Fatalf("%v exited %d (want 2), err=%v", subArgs[0], c, runErr)
			}
		}
	})
}

func TestCLIRefusalCodesIncludeLockUnavailableAlreadyV2AndVersionUndeclaredR8(t *testing.T) {
	// Direct ExitCode checks on typed UpgradeError values.
	for _, code := range []upgradeservice.UpgradeErrorCode{
		upgradeservice.UpgradeErrorCode(upgradeservice.CodeSourceLockUnavailable),
		upgradeservice.UpgradeErrorCode(upgradeservice.CodeSourceAlreadyV2),
		upgradeservice.UpgradeErrorCode(upgradeservice.CodeSourceVersionUndeclared),
	} {
		err := &upgradeservice.UpgradeError{Code: code, Message: "operator decision required"}
		if got := ExitCode(err); got != 1 {
			t.Fatalf("ExitCode(%s) = %d, want 1", code, got)
		}
	}

	// End-to-end `upgrade run` against a native v2 database returns exit code 1.
	v2Source := t.TempDir()
	dbPath := filepath.Join(v2Source, "usage.sqlite")
	db, err := managersqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, _, err := security.LoadOrCreateDataKey("", filepath.Join(v2Source, "data.key")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(v2Source, "usage-archives"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath+".manager.lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}

	exitCode, stdout, stderr := run(t, "run", "--legacy", v2Source, "--target", t.TempDir(), "--json")
	if exitCode != 1 {
		t.Fatalf("run against native v2 source exited %d, want 1; stdout=%s stderr=%s", exitCode, stdout, stderr)
	}
	if !strings.Contains(stdout, upgradeservice.CodeSourceAlreadyV2) {
		t.Fatalf("stdout = %s, want %s", stdout, upgradeservice.CodeSourceAlreadyV2)
	}
}
