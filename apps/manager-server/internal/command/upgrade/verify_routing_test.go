package upgrade

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

func TestVerifyCLIRefusesMissingPrefixesAndConflictingCompletedReplays(t *testing.T) {
	server := newExternalCPAServer(t, "v8.0.13")
	source, target := verifiedTarget(t, server.URL)

	// 1. CLI verify without --control-base-path must fail closed (no RequireAccessRouting bypass).
	code, stdout, _ := run(t, "verify", "--legacy", source, "--target", target, "--json")
	if code != 1 {
		t.Fatalf("exit code without --control-base-path = %d, want 1; stdout=%s", code, stdout)
	}
	var missingOut verifyOutput
	if err := json.Unmarshal([]byte(stdout), &missingOut); err != nil {
		t.Fatalf("decode missing-prefix refusal: %v (%s)", err, stdout)
	}
	if missingOut.ErrorCode != string(upgradeservice.UpgradeErrorVerifyAccessPrefixRequired) || missingOut.Result != nil {
		t.Fatalf("unexpected missing-prefix CLI output: %+v", missingOut)
	}

	// 2. External mode rejects --inference-base-path on CLI.
	code, stdout, _ = run(t, "verify", "--legacy", source, "--target", target,
		"--control-base-path", "/cli-admin",
		"--inference-base-path", "/cli-api",
		"--json")
	if code != 1 {
		t.Fatalf("exit code with forbidden External inference flag = %d, want 1; stdout=%s", code, stdout)
	}
	var extForbidden verifyOutput
	if err := json.Unmarshal([]byte(stdout), &extForbidden); err != nil {
		t.Fatalf("decode External inference refusal: %v (%s)", err, stdout)
	}
	if extForbidden.ErrorCode != "external_inference_prefix_forbidden" {
		t.Fatalf("ErrorCode = %q, want external_inference_prefix_forbidden", extForbidden.ErrorCode)
	}

	// 3. Valid External --control-base-path completes and commits routing.
	code, stdout, stderr := run(t, "verify", "--legacy", source, "--target", target,
		"--control-base-path", "/cli-admin", "--json")
	if code != 0 {
		t.Fatalf("valid External verify exit = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}
	var okOut verifyOutput
	if err := json.Unmarshal([]byte(stdout), &okOut); err != nil || okOut.Result == nil {
		t.Fatalf("decode completed output: err=%v out=%+v", err, okOut)
	}
	if !okOut.Result.Complete || !okOut.Result.RoutingCommitted || okOut.Result.ControlBasePath != "/cli-admin" {
		t.Fatalf("unexpected completed VerifyResult: %+v", okOut.Result)
	}

	// 4. Remove old 1.x source volume: completed status check works without --legacy,
	// while conflicting prefix flags fail closed with upgrade_prefixes_already_committed.
	if err := os.RemoveAll(source); err != nil {
		t.Fatalf("remove 1.x source volume: %v", err)
	}
	code, stdout, stderr = run(t, "verify", "--target", target, "--json")
	if code != 0 {
		t.Fatalf("completed status without --legacy exit = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}
	var replayOut verifyOutput
	if err := json.Unmarshal([]byte(stdout), &replayOut); err != nil || replayOut.Result == nil || !replayOut.Result.AlreadyVerified || replayOut.Result.ControlBasePath != "/cli-admin" {
		t.Fatalf("unexpected completed status replay without --legacy: err=%v out=%+v", err, replayOut)
	}

	code, stdout, _ = run(t, "verify", "--target", target, "--control-base-path", "/conflicting-admin", "--json")
	if code != 1 {
		t.Fatalf("conflicting completed replay exit = %d, want 1; stdout=%s", code, stdout)
	}
	var conflictOut verifyOutput
	if err := json.Unmarshal([]byte(stdout), &conflictOut); err != nil {
		t.Fatalf("decode conflict output: %v (%s)", err, stdout)
	}
	if conflictOut.ErrorCode != string(upgradeservice.UpgradeErrorPrefixesAlreadyCommitted) {
		t.Fatalf("conflict ErrorCode = %q, want %s", conflictOut.ErrorCode, upgradeservice.UpgradeErrorPrefixesAlreadyCommitted)
	}
}

func TestVerifyCLIRepairsPreExistingCompleteUnroutedStoreOnceWithoutLegacySource(t *testing.T) {
	server := newExternalCPAServer(t, "v8.0.13")
	source, target := verifiedTarget(t, server.URL)

	code, _, stderr := run(t, "verify", "--legacy", source, "--target", target, "--control-base-path", "/temp-admin")
	if code != 0 {
		t.Fatalf("initial verify failed: exit=%d stderr=%s", code, stderr)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatalf("remove 1.x source volume: %v", err)
	}

	// Simulate pre-existing v2@9060054e completed unrouted store.
	db, err := managersqlite.Open(filepath.Join(target, upgradeservice.SourceDatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `update upgrade_operations set control_base_path = '', inference_base_path = '', shared_entry = 0, routing_committed = 0, inference_committed = 0 where id = 1`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()

	// Calling verify without prefixes fails closed.
	code, stdout, _ := run(t, "verify", "--target", target, "--json")
	if code != 1 {
		t.Fatalf("unrouted complete verify without flags exit = %d, want 1; stdout=%s", code, stdout)
	}
	var unroutedOut verifyOutput
	if err := json.Unmarshal([]byte(stdout), &unroutedOut); err != nil {
		t.Fatalf("decode unrouted refusal: %v (%s)", err, stdout)
	}
	if unroutedOut.ErrorCode != string(upgradeservice.UpgradeErrorVerifyAccessPrefixRequired) {
		t.Fatalf("unrouted refusal ErrorCode = %q, want %s", unroutedOut.ErrorCode, upgradeservice.UpgradeErrorVerifyAccessPrefixRequired)
	}

	// One-time repair succeeds without --legacy and locks in the prefix permanently.
	code, stdout, stderr = run(t, "verify", "--target", target, "--control-base-path", "/repaired-admin", "--json")
	if code != 0 {
		t.Fatalf("one-time CLI repair without --legacy exit = %d, want 0; stderr=%s stdout=%s", code, stderr, stdout)
	}
	var repairedOut verifyOutput
	if err := json.Unmarshal([]byte(stdout), &repairedOut); err != nil || repairedOut.Result == nil {
		t.Fatalf("decode repaired CLI output: err=%v out=%+v", err, repairedOut)
	}
	if !repairedOut.Result.Complete || repairedOut.Result.AlreadyVerified || !repairedOut.Result.RoutingCommitted || repairedOut.Result.ControlBasePath != "/repaired-admin" {
		t.Fatalf("unexpected repaired CLI result: %+v", repairedOut.Result)
	}

	code, stdout, _ = run(t, "verify", "--target", target, "--control-base-path", "/second-admin", "--json")
	if code != 1 {
		t.Fatalf("second prefix change after repair exit = %d, want 1; stdout=%s", code, stdout)
	}
}
