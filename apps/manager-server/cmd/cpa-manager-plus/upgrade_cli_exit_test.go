package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
)

// runUpgradeCLI executes the real process entry point through the helper test
// binary, so the observed exit status and stderr are the ones an operator sees.
func runUpgradeCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	command := exec.Command(os.Args[0], append([]string{"-test.run=^TestManagerServerHelperProcess$", "--"}, args...)...)
	command.Env = append(os.Environ(), "CPA_MANAGER_TEST_HELPER=1")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run the upgrade CLI: %v", err)
		}
		code = exit.ExitCode()
	}
	return code, stdout.String(), stderr.String()
}

// writeOldUpgradeStore writes an earlier build's copy-in row: the upgrade table
// exists, the columns a rollback records do not.
func writeOldUpgradeStore(t *testing.T, databasePath string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath))
	if err != nil {
		t.Fatalf("create the earlier store: %v", err)
	}
	for _, statement := range []string{
		`create table upgrade_operations (
			id integer primary key check(id = 1),
			stage text not null default '',
			domains_done text not null default '[]',
			source_kind text,
			source_version text,
			source_fingerprint_before text,
			source_fingerprint_after text,
			backup_manifest_sha256 text,
			reconciliation text not null default '{}',
			manual_items text not null default '[]',
			error_code text,
			error_detail text,
			created_at_ms integer not null,
			updated_at_ms integer not null
		)`,
		`insert into upgrade_operations (id, stage, domains_done, source_kind, source_version,
			source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256,
			created_at_ms, updated_at_ms)
			values (1, 'copy_in', '["source_copy"]', 'upgraded-legacy', 'v1.14.4',
				'before', 'after', 'manifest', 1, 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("write the earlier store: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the earlier store: %v", err)
	}
}

// TestUpgradeCLITypedRefusalsExitOneAndPrintOnce is the L4 regression through
// the real entry point: a typed operational refusal exits 1 and its reason
// appears in stderr exactly once. The command printed it and main logged the
// same error again; `upgrade run` reported exit 2 with an empty stderr because
// it returned the typed error unwrapped.
func TestUpgradeCLITypedRefusalsExitOneAndPrintOnce(t *testing.T) {
	const rolledBackReason = "upgrade_rollback_not_applicable"
	code, stdout, stderr := runUpgradeCLI(t, "upgrade", "rollback", "--prepare",
		"--legacy", t.TempDir(), "--target", t.TempDir())
	if code != 1 {
		t.Fatalf("rollback refusal exit code = %d, want 1; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if count := strings.Count(stderr, rolledBackReason); count != 1 {
		t.Fatalf("%s appears %d times in stderr, want once: %s", rolledBackReason, count, stderr)
	}

	const outdatedStoreCode = "upgrade_store_schema_outdated"
	target := t.TempDir()
	writeOldUpgradeStore(t, filepath.Join(target, "usage.sqlite"))
	code, stdout, stderr = runUpgradeCLI(t, "upgrade", "run", "--legacy", t.TempDir(),
		"--source-version", bootstrap.PlannedDirectRelease, "--target", target)
	if code != 1 {
		t.Fatalf("run refusal exit code = %d, want 1; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if count := strings.Count(stderr, outdatedStoreCode); count != 1 {
		t.Fatalf("%s appears %d times in stderr, want once: %s", outdatedStoreCode, count, stderr)
	}

	// A usage failure stays 2 and prints no operational code.
	code, _, stderr = runUpgradeCLI(t, "upgrade", "run", "--target", t.TempDir())
	if code != 2 {
		t.Fatalf("usage exit code = %d, want 2; stderr: %s", code, stderr)
	}
}

// TestUpgradeCLIKeepsAChangedSourceRetryable is the M-1 regression through the
// real process entry: the rollback lane wrapped every typed failure as a
// refusal, so upgrade_source_changed exited 1 there while run and reconcile
// kept the documented retryable 2. One typed code now reads one table, so the
// same failure exits 2 here without the rollback path choosing it.
func TestUpgradeCLIKeepsAChangedSourceRetryable(t *testing.T) {
	const sourceChanged = "upgrade_source_changed"
	source := t.TempDir()
	target := t.TempDir()
	db, err := sqliterepo.Open(filepath.Join(target, "usage.sqlite"))
	if err != nil {
		t.Fatalf("open the v2 store: %v", err)
	}
	if err := upgradeoperation.New(db).Put(context.Background(), model.UpgradeOperation{
		Stage:                   model.UpgradeStageCopyIn,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy},
		SourceVersion:           "v1.14.4",
		SourceRoot:              source,
		SourceFingerprintBefore: strings.Repeat("0", 64),
		SourceFingerprintAfter:  strings.Repeat("0", 64),
		BackupManifestSHA256:    strings.Repeat("0", 64),
	}); err != nil {
		_ = db.Close()
		t.Fatalf("record the copy-in: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the v2 store: %v", err)
	}

	code, stdout, stderr := runUpgradeCLI(t, "upgrade", "rollback", "--prepare",
		"--legacy", source, "--target", target)
	if code != 2 {
		t.Fatalf("changed-source exit code = %d, want the retryable 2; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if count := strings.Count(stderr, sourceChanged); count != 1 {
		t.Fatalf("%s appears %d times in stderr, want once: %s", sourceChanged, count, stderr)
	}
}
