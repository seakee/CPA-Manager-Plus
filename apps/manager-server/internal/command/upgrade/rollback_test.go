package upgrade

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

// rollbackReadyTarget copies a stopped 1.x source into a v2 data root, which is
// the recorded state a rollback CLI is run against.
func rollbackReadyTarget(t *testing.T, source string) string {
	t.Helper()
	target := t.TempDir()
	code, _, stderr := run(t, "run", "--json", "--legacy", source, "--target", target)
	if code != 0 {
		t.Fatalf("prepare the copy-in target: exit %d; stderr: %s", code, stderr)
	}
	return target
}

// statusProbe serves the 1.x health endpoint the rollback verify path probes.
func statusProbe(t *testing.T, healthy bool) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/status"
}

func TestRollbackPreparePrintsAPinnedPlan(t *testing.T) {
	source := copyReadySource(t)
	target := rollbackReadyTarget(t, source)
	code, stdout, stderr := run(t, "rollback", "--prepare", "--legacy", source, "--target", target, "--json")
	if code != 0 {
		t.Fatalf("prepare exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var plan upgradeservice.Plan
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatalf("decode plan: %v; output: %s", err, stdout)
	}
	if plan.Image == "" || strings.Contains(plan.Image, ":latest") {
		t.Fatalf("pinned image = %q, want an exact version", plan.Image)
	}
	if !strings.Contains(plan.Image, upgradeservice.LegacyImageRepository+":v") {
		t.Fatalf("pinned image = %q, want the 1.x repository at an exact tag", plan.Image)
	}
	if strings.Contains(plan.ComposeFile, ":latest") {
		t.Fatalf("compose file names a moving tag: %s", plan.ComposeFile)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("resolve the target root: %v", err)
	}
	if plan.ReportPath != filepath.Join(resolved, ".upgrade-rollback", "plan.json") {
		t.Fatalf("report path = %q, want it under the v2 data root", plan.ReportPath)
	}
	if _, err := os.Lstat(plan.ReportPath); err != nil {
		t.Fatalf("plan file was not written: %v", err)
	}
	// The human form names the same decision.
	code, human, stderr := run(t, "rollback", "--prepare", "--legacy", source, "--target", target)
	if code != 0 {
		t.Fatalf("human prepare exit code = %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(human, plan.Image) || !strings.Contains(human, "docker compose -p cpamp -f - up -d") {
		t.Fatalf("human plan = %s", human)
	}
}

func TestRollbackPrepareRefusesAndWritesNothing(t *testing.T) {
	source := copyReadySource(t)
	target := t.TempDir()
	code, stdout, stderr := run(t, "rollback", "--prepare", "--legacy", source, "--target", target)
	if code != 1 {
		t.Fatalf("prepare without a record exit code = %d, want 1; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, string(upgradeservice.UpgradeErrorRollbackNotApplicable)) {
		t.Fatalf("refusal did not name its code: %s", stderr)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a refused prepare wrote %v", entries)
	}
}

func TestRollbackVerifyRecordsAProvenVerdict(t *testing.T) {
	source := copyReadySource(t)
	target := rollbackReadyTarget(t, source)
	probe := statusProbe(t, true)
	code, stdout, stderr := run(t, "rollback", "--verify", "--legacy", source, "--target", target,
		"--status-url", probe, "--json")
	if code != 0 {
		t.Fatalf("verify exit code = %d, want 0; stdout: %s stderr: %s", code, stdout, stderr)
	}
	var operation model.UpgradeOperation
	if err := json.Unmarshal([]byte(stdout), &operation); err != nil {
		t.Fatalf("decode recorded operation: %v; output: %s", err, stdout)
	}
	if operation.Stage != model.UpgradeStageRolledBack {
		t.Fatalf("stage = %q, want %q", operation.Stage, model.UpgradeStageRolledBack)
	}
	if operation.SourceKind != upgradeservice.SourceKindUpgradedLegacy ||
		operation.SourceFingerprintBefore == "" || operation.BackupManifestSHA256 == "" {
		t.Fatalf("recorded operation lost provenance: %+v", operation)
	}
	code, statusJSON, stderr := run(t, "status", "--json", "--db-path", filepath.Join(target, "usage.sqlite"))
	if code != 0 {
		t.Fatalf("status exit code = %d; stderr: %s", code, stderr)
	}
	var status statusOutput
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Operation == nil || status.Operation.Stage != model.UpgradeStageRolledBack {
		t.Fatalf("persisted stage = %+v, want rolled_back", status.Operation)
	}
}

func TestRollbackVerifyRefusesUnprovenEvidence(t *testing.T) {
	source := copyReadySource(t)
	target := rollbackReadyTarget(t, source)
	for _, test := range []struct {
		name   string
		probe  string
		mutate func(string) string
	}{
		{name: "unhealthy probe", probe: statusProbe(t, false)},
		// The volume no longer holds a 1.x database, so no read-only open can
		// prove the rollback. This case is deliberately independent of the host
		// network: it must not depend on nothing listening on the 1.x port.
		{name: "volume without a database", mutate: func(string) string { return t.TempDir() }},
	} {
		legacy := source
		if test.mutate != nil {
			legacy = test.mutate(source)
		}
		code, stdout, stderr := run(t, "rollback", "--verify", "--legacy", legacy, "--target", target,
			"--status-url", test.probe)
		if code != 1 {
			t.Fatalf("%s exit code = %d, want 1; stdout: %s stderr: %s", test.name, code, stdout, stderr)
		}
		if stderr == "" {
			t.Fatalf("%s produced no explanation", test.name)
		}
	}
	// Nothing was recorded by any refusal.
	code, statusJSON, _ := run(t, "status", "--json", "--db-path", filepath.Join(target, "usage.sqlite"))
	if code != 0 {
		t.Fatalf("status exit code = %d", code)
	}
	var status statusOutput
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Operation == nil || status.Operation.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("a refused verify moved the row: %+v", status.Operation)
	}
}

func TestRollbackUsageErrors(t *testing.T) {
	source := copyReadySource(t)
	target := t.TempDir()
	for name, args := range map[string][]string{
		"no mode":    {"rollback", "--legacy", source, "--target", target},
		"both modes": {"rollback", "--prepare", "--verify", "--legacy", source, "--target", target},
		"no legacy":  {"rollback", "--prepare", "--target", target},
		"positional": {"rollback", "--prepare", "--legacy", source, "--target", target, "extra"},
	} {
		if code, _, _ := run(t, args...); code != 2 {
			t.Fatalf("%s exit code = %d, want 2", name, code)
		}
	}
}

func TestRunStopsAfterTheCopyInAndPrintsTheNextCommand(t *testing.T) {
	source := copyReadySource(t)
	target := t.TempDir()
	code, stdout, stderr := run(t, "run", "--json", "--legacy", source, "--target", target)
	if code != 0 {
		t.Fatalf("run exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var output runOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode run output: %v; output: %s", err, stdout)
	}
	if output.Result == nil || output.Result.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("run result = %+v, want a copy_in", output.Result)
	}
	// The run never advances past the copy-in and names the next step.
	if !strings.Contains(output.Next, "upgrade reconcile") || !strings.Contains(output.Next, "--legacy") {
		t.Fatalf("next = %q, want the reconcile command", output.Next)
	}
	operation, found, err := upgradeservice.ReadRecord(context.Background(), filepath.Join(target, "usage.sqlite"))
	if err != nil || !found {
		t.Fatalf("read recorded operation: found=%t err=%v", found, err)
	}
	if operation.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("recorded stage = %q, want the copy-in to be the last step run performs", operation.Stage)
	}
	// The human form carries the same next step.
	code, human, stderr := run(t, "run", "--legacy", source, "--target", target)
	if code != 0 {
		t.Fatalf("human run exit code = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(human, "reconcile") {
		t.Fatalf("human run did not print the next step: %s", human)
	}
}

func TestRunReportsALaterStageWithoutTouchingIt(t *testing.T) {
	source := copyReadySource(t)
	target := rollbackReadyTarget(t, source)
	databasePath := filepath.Join(target, "usage.sqlite")
	db, err := sqliterepo.Open(databasePath)
	if err != nil {
		t.Fatalf("open the v2 store: %v", err)
	}
	if _, err := upgradeoperation.New(db).Advance(context.Background(), "", model.UpgradeStageReconcile); err != nil {
		_ = db.Close()
		t.Fatalf("record the reconcile stage: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the v2 store: %v", err)
	}
	before, found, err := upgradeservice.ReadRecord(context.Background(), databasePath)
	if err != nil || !found {
		t.Fatalf("read the recorded operation: found=%t err=%v", found, err)
	}

	code, stdout, stderr := run(t, "run", "--json", "--legacy", source, "--target", target)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var output runOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode run output: %v; output: %s", err, stdout)
	}
	if output.Operation == nil || output.Operation.Stage != model.UpgradeStageReconcile {
		t.Fatalf("run = %+v, want the recorded stage reported", output.Operation)
	}
	if output.Result != nil {
		t.Fatalf("run copied again: %+v", output.Result)
	}
	after, found, err := upgradeservice.ReadRecord(context.Background(), databasePath)
	if err != nil || !found {
		t.Fatalf("re-read the recorded operation: found=%t err=%v", found, err)
	}
	if after.Stage != before.Stage || after.UpdatedAtMS != before.UpdatedAtMS {
		t.Fatalf("run touched a later stage: before=%+v after=%+v", before, after)
	}
}

// TestRunAfterARollbackReportsTheTerminalStage is the blocking half of a
// rollback at the run boundary: a rolled-back operation already copied in, so
// `upgrade run` must neither copy again nor print a reconciliation command that
// would resume an abandoned upgrade.
func TestRunAfterARollbackReportsTheTerminalStage(t *testing.T) {
	source := copyReadySource(t)
	target := rollbackReadyTarget(t, source)
	databasePath := filepath.Join(target, "usage.sqlite")
	db, err := sqliterepo.Open(databasePath)
	if err != nil {
		t.Fatalf("open the v2 store: %v", err)
	}
	if _, err := upgradeoperation.New(db).Advance(context.Background(), "", model.UpgradeStageRolledBack); err != nil {
		_ = db.Close()
		t.Fatalf("record the rolled_back stage: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the v2 store: %v", err)
	}
	before, found, err := upgradeservice.ReadRecord(context.Background(), databasePath)
	if err != nil || !found {
		t.Fatalf("read the recorded operation: found=%t err=%v", found, err)
	}

	code, stdout, stderr := run(t, "run", "--json", "--legacy", source, "--target", target)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var output runOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode run output: %v; output: %s", err, stdout)
	}
	if output.Operation == nil || output.Operation.Stage != model.UpgradeStageRolledBack {
		t.Fatalf("run = %+v, want the rolled_back stage reported", output.Operation)
	}
	if output.Result != nil || output.Next != "" {
		t.Fatalf("run resumed a rolled-back operation: result=%+v next=%q", output.Result, output.Next)
	}
	after, found, err := upgradeservice.ReadRecord(context.Background(), databasePath)
	if err != nil || !found {
		t.Fatalf("re-read the recorded operation: found=%t err=%v", found, err)
	}
	if after.Stage != before.Stage || after.UpdatedAtMS != before.UpdatedAtMS {
		t.Fatalf("run touched a rolled-back row: before=%+v after=%+v", before, after)
	}
}
