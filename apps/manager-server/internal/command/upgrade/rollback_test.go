package upgrade

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

// rollbackReadyTarget copies a stopped 1.x source into a v2 data root, which is
// the recorded state a rollback CLI is run against.
func rollbackReadyTarget(t *testing.T, source string) string {
	t.Helper()
	target := t.TempDir()
	code, _, stderr := run(t, "run", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target)
	if code != 0 {
		t.Fatalf("prepare the copy-in target: exit %d; stderr: %s", code, stderr)
	}
	return target
}

// healthProbe serves the 1.x health endpoint the rollback verify path probes.
// The body is the documented contract, so a test can decide what answer, and
// which identity, the probe sees.
func healthProbe(t *testing.T, status int, body string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != upgradeservice.LegacyHealthPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server.URL + upgradeservice.LegacyHealthPath
}

func healthyProbe(t *testing.T) string {
	t.Helper()
	return healthProbe(t, http.StatusOK, `{"ok":true,"service":"`+upgradeservice.LegacyServiceID+`"}`)
}

func TestRollbackPreparePrintsAPinnedPlan(t *testing.T) {
	source := copyReadyRunSource(t)
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
	if plan.Volume != upgradeservice.LegacyVolumeName ||
		plan.VolumeMountPoint != upgradeservice.LegacyVolumeMountPoint ||
		!strings.Contains(plan.ComposeFile, plan.Volume+":"+plan.VolumeMountPoint) {
		t.Fatalf("plan volume = %q at %q", plan.Volume, plan.VolumeMountPoint)
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
	// The human form names the same decision, and its next step uses the
	// unauthenticated health endpoint with the identity it must state.
	code, human, stderr := run(t, "rollback", "--prepare", "--legacy", source, "--target", target)
	if code != 0 {
		t.Fatalf("human prepare exit code = %d, want 0; stderr: %s", code, stderr)
	}
	for _, want := range []string{
		plan.Image,
		"docker compose -p cpamp -f - up -d",
		upgradeservice.LegacyHealthURL(),
		upgradeservice.LegacyServiceID,
		"--filter publish=",
		"-sTCP:LISTEN",
		"stop the v2 stack",
	} {
		if !strings.Contains(human, want) {
			t.Fatalf("human plan does not mention %q: %s", want, human)
		}
	}
	if strings.Contains(human, "/status") {
		t.Fatalf("human plan still proves health with /status: %s", human)
	}
}

func TestRollbackPrepareRefusesAndWritesNothing(t *testing.T) {
	source := copyReadyRunSource(t)
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
	source := copyReadyRunSource(t)
	target := rollbackReadyTarget(t, source)
	code, stdout, stderr := run(t, "rollback", "--verify", "--legacy", source, "--target", target,
		"--status-url", healthyProbe(t), "--json")
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
		operation.SourceFingerprintBefore == "" || operation.BackupManifestSHA256 == "" ||
		operation.SourceRoot == "" {
		t.Fatalf("recorded operation lost provenance: %+v", operation)
	}
	// The evidence is readable from the persisted row, not only from stdout.
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
	evidence := status.Operation.Rollback
	if evidence == nil {
		t.Fatalf("persisted row carries no rollback evidence: %+v", status.Operation)
	}
	if !strings.Contains(evidence.Image, ":v") || strings.Contains(evidence.Image, ":latest") {
		t.Fatalf("recorded image = %q, want an exact release", evidence.Image)
	}
	if evidence.ImageSource != upgradeservice.RollbackImageSourceDerived ||
		evidence.ImageForm != upgradeservice.RollbackImageFormReleaseTag {
		t.Fatalf("recorded image provenance = %q/%q, want a derived release tag", evidence.ImageSource, evidence.ImageForm)
	}
	if !strings.HasSuffix(evidence.StatusURL, upgradeservice.LegacyHealthPath) {
		t.Fatalf("recorded health endpoint = %q, want %q", evidence.StatusURL, upgradeservice.LegacyHealthPath)
	}
	if evidence.SourceFingerprint == "" || evidence.ObservedAtMS <= 0 {
		t.Fatalf("recorded observation = %+v", evidence)
	}
}

func TestRollbackVerifyRefusesUnprovenEvidence(t *testing.T) {
	source := copyReadyRunSource(t)
	target := rollbackReadyTarget(t, source)
	for _, test := range []struct {
		name   string
		probe  func(t *testing.T) string
		mutate func(string) string
		code   string
	}{
		{
			name:  "unhealthy probe",
			probe: func(t *testing.T) string { return healthProbe(t, http.StatusServiceUnavailable, "") },
			code:  string(upgradeservice.UpgradeErrorRollbackUnproven),
		},
		{
			// A 2xx from something that is not this install: a stray 200 that
			// names another service must not pass as a health proof.
			name: "another service answers",
			probe: func(t *testing.T) string {
				return healthProbe(t, http.StatusOK, `{"ok":true,"service":"existing-manager"}`)
			},
			code: string(upgradeservice.UpgradeErrorRollbackIdentityForeign),
		},
		{
			// A bare 2xx without the documented body is not a health proof.
			name: "bare 200",
			probe: func(t *testing.T) string {
				return healthProbe(t, http.StatusOK, "OK")
			},
			code: string(upgradeservice.UpgradeErrorRollbackIdentityForeign),
		},
		{
			// The credentialed endpoint answers 401 without a key, so it can
			// never prove the volume.
			name: "the credentialed endpoint",
			probe: func(t *testing.T) string {
				return strings.TrimSuffix(healthyProbe(t), upgradeservice.LegacyHealthPath) + "/status"
			},
			code: string(upgradeservice.UpgradeErrorRollbackProbeEndpoint),
		},
		{
			// The volume no longer holds a 1.x database, so no read-only open
			// can prove the rollback. This case is deliberately independent of
			// the host network: it must not depend on nothing listening on the
			// 1.x port.
			name:   "volume without a database",
			mutate: func(string) string { return t.TempDir() },
			code:   string(upgradeservice.UpgradeErrorRollbackVolumeUnbound),
		},
	} {
		legacy := source
		if test.mutate != nil {
			legacy = test.mutate(source)
		}
		probe := ""
		if test.probe != nil {
			probe = test.probe(t)
		}
		code, stdout, stderr := run(t, "rollback", "--verify", "--legacy", legacy, "--target", target,
			"--status-url", probe)
		if code != 1 {
			t.Fatalf("%s exit code = %d, want 1; stdout: %s stderr: %s", test.name, code, stdout, stderr)
		}
		if !strings.Contains(stderr, test.code) {
			t.Fatalf("%s refusal did not name %q: %s", test.name, test.code, stderr)
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
	if status.Operation.Rollback != nil {
		t.Fatalf("a refused verify recorded evidence: %+v", status.Operation.Rollback)
	}
}

// TestRollbackVerifyKeepsTheImageInTheSourceFamily covers the CLI half of M6:
// another version family needs the explicit override, and the override refuses
// to be passed without an image.
func TestRollbackVerifyKeepsTheImageInTheSourceFamily(t *testing.T) {
	source := copyReadyRunSource(t)
	target := rollbackReadyTarget(t, source)
	databasePath := filepath.Join(target, "usage.sqlite")
	operation, found, err := upgradeservice.ReadRecord(context.Background(), databasePath)
	if err != nil || !found {
		t.Fatalf("read the recorded operation: found=%t err=%v", found, err)
	}
	probe := healthyProbe(t)
	code, _, stderr := run(t, "rollback", "--verify", "--legacy", source, "--target", target,
		"--status-url", probe, "--legacy-image", "v1.13.0")
	if code != 1 {
		t.Fatalf("foreign family exit code = %d, want 1; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, string(upgradeservice.UpgradeErrorRollbackImageFamily)) {
		t.Fatalf("foreign family refusal did not name its code: %s", stderr)
	}
	if after, _, err := upgradeservice.ReadRecord(context.Background(), databasePath); err != nil || after.Stage != operation.Stage {
		t.Fatalf("a refused family moved the row to %q", after.Stage)
	}

	// The override flag is refused on its own: it authorizes nothing without an
	// explicit image.
	if code, _, _ := run(t, "rollback", "--verify", "--legacy", source, "--target", target,
		"--allow-image-family-mismatch"); code != 2 {
		t.Fatalf("family override without an image exit code = %d, want 2", code)
	}
}

// TestRollbackPreparePrintsAnExactVerifyCommand keeps the printed next step
// runnable as-is: it names the image the plan pinned, and the family override
// when the plan needed one.
func TestRollbackPreparePrintsAnExactVerifyCommand(t *testing.T) {
	source := copyReadyRunSource(t)
	target := rollbackReadyTarget(t, source)
	code, human, stderr := run(t, "rollback", "--prepare", "--legacy", source, "--target", target,
		"--legacy-image", "v1.13.0", "--allow-image-family-mismatch")
	if code != 0 {
		t.Fatalf("prepare exit code = %d, want 0; stderr: %s", code, stderr)
	}
	for _, want := range []string{
		"--legacy-image '" + upgradeservice.LegacyImageRepository + ":v1.13.0'",
		"--allow-image-family-mismatch",
		"--status-url '" + upgradeservice.LegacyHealthURL() + "'",
		"--backup '",
		"--target '",
		"--legacy '",
	} {
		if !strings.Contains(human, want) {
			t.Fatalf("printed verify command does not carry %q: %s", want, human)
		}
	}
}

func TestRollbackUsageErrors(t *testing.T) {
	source := copyReadyRunSource(t)
	target := t.TempDir()
	for name, args := range map[string][]string{
		"no mode":    {"rollback", "--legacy", source, "--target", target},
		"both modes": {"rollback", "--prepare", "--verify", "--legacy", source, "--target", target},
		"no legacy":  {"rollback", "--prepare", "--target", target},
		"positional": {"rollback", "--prepare", "--legacy", source, "--target", target, "extra"},
		"bad manager address": {"rollback", "--prepare", "--legacy", source, "--target", target,
			"--manager-addr", "not-an-address"},
		"port on verify": {"rollback", "--verify", "--legacy", source, "--target", target,
			"--port", "18319"},
	} {
		if code, _, _ := run(t, args...); code != 2 {
			t.Fatalf("%s exit code = %d, want 2", name, code)
		}
	}
}

func TestRunStopsAfterTheCopyInAndPrintsTheNextCommand(t *testing.T) {
	source := copyReadyRunSource(t)
	target := t.TempDir()
	code, stdout, stderr := run(t, "run", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target)
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
	// The run never advances past the copy-in and names a next step that exists
	// on this build. Reconciliation is a separate authorised step that this
	// build does not ship, so the printed command reports the recorded operation
	// instead of naming a subcommand that would exit as a usage error.
	nextArgs, err := printedCommandArgs(output.Next)
	if err != nil {
		t.Fatalf("next = %q: %v", output.Next, err)
	}
	nextCode, nextStdout, nextStderr := run(t, nextArgs...)
	if nextCode != 0 {
		t.Fatalf("the printed next command is not runnable on this build: exit %d; stderr: %s", nextCode, nextStderr)
	}
	if !strings.Contains(nextStdout, "stage: "+string(model.UpgradeStageCopyIn)) {
		t.Fatalf("the printed next command did not report the recorded stage: %s", nextStdout)
	}
	operation, found, err := upgradeservice.ReadRecord(context.Background(), filepath.Join(target, "usage.sqlite"))
	if err != nil || !found {
		t.Fatalf("read recorded operation: found=%t err=%v", found, err)
	}
	if operation.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("recorded stage = %q, want the copy-in to be the last step run performs", operation.Stage)
	}
	// The copy-in records the 1.x directory it read, so a later rollback can
	// bind its volume instead of asserting "the original volume".
	if operation.SourceRoot == "" {
		t.Fatalf("the copy-in recorded no source root: %+v", operation)
	}
	// The human form carries the same next step and says what remains.
	code, human, stderr := run(t, "run", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target)
	if code != 0 {
		t.Fatalf("human run exit code = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(human, "upgrade status") {
		t.Fatalf("human run did not print the next step: %s", human)
	}
	if strings.Contains(human, "upgrade reconcile") {
		t.Fatalf("human run printed a subcommand this build does not ship: %s", human)
	}
	if !strings.Contains(human, "not part of this build") {
		t.Fatalf("human run does not say reconciliation is unavailable: %s", human)
	}
}

// TestNextStepCommandIsShellQuoted is the L3 regression: the next step was
// rendered with Go quoting, so a path containing `$` or backticks was expanded
// by the operator's shell before the command saw it. The printed argument is now
// one single-quoted shell word, and the shell hands it back unchanged.
func TestNextStepCommandIsShellQuoted(t *testing.T) {
	target := filepath.Join(t.TempDir(), "v2 $HOME `x` with space")
	path := filepath.Join(target, upgradeservice.SourceDatabaseName)
	command := nextStepCommand(target)
	if !strings.Contains(command, upgradeservice.ShellQuote(path)) {
		t.Fatalf("next step is not shell-quoted for %q: %s", path, command)
	}
	output, err := exec.Command("sh", "-c", "printf '%s' "+upgradeservice.ShellQuote(path)).Output()
	if err != nil {
		t.Fatalf("render the path through the shell: %v", err)
	}
	if string(output) != path {
		t.Fatalf("the shell expanded the printed path to %q, want %q", output, path)
	}
}

// printedCommandArgs splits the command `upgrade run` prints into the arguments
// this build accepts, so a test can execute exactly what the operator would
// paste. The printed words are POSIX shell words: single-quoted with each
// embedded single quote written as the close-escape-reopen sequence.
func printedCommandArgs(command string) ([]string, error) {
	fields := []string{}
	current := strings.Builder{}
	inSingle := false
	flush := func() {
		if current.Len() > 0 {
			fields = append(fields, current.String())
			current.Reset()
		}
	}
	runes := []rune(command)
	for index := 0; index < len(runes); index++ {
		switch r := runes[index]; {
		case r == '\'':
			inSingle = !inSingle
		case r == '\\' && !inSingle && index+1 < len(runes) && runes[index+1] == '\'':
			// The close-escape-reopen sequence shellQuote emits for an embedded
			// single quote.
			current.WriteRune('\'')
			index++
		case (r == ' ' || r == '\t') && !inSingle:
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if inSingle {
		return nil, fmt.Errorf("unterminated single quote in %q", command)
	}
	flush()
	if len(fields) < 3 || fields[0] != "cpa-manager-plus" || fields[1] != "upgrade" {
		return nil, fmt.Errorf("want a `cpa-manager-plus upgrade ...` command, got %q", command)
	}
	return fields[2:], nil
}

func TestRunReportsALaterStageWithoutTouchingIt(t *testing.T) {
	source := copyReadyRunSource(t)
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

	code, stdout, stderr := run(t, "run", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target)
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
	source := copyReadyRunSource(t)
	target := rollbackReadyTarget(t, source)
	databasePath := filepath.Join(target, "usage.sqlite")
	db, err := sqliterepo.Open(databasePath)
	if err != nil {
		t.Fatalf("open the v2 store: %v", err)
	}
	if _, err := upgradeoperation.New(db).RecordRollback(context.Background(), model.RollbackEvidence{
		Image:             upgradeservice.LegacyImageRepository + ":v1.14.7",
		ImageSource:       upgradeservice.RollbackImageSourceOperator,
		ImageForm:         upgradeservice.RollbackImageFormReleaseTag,
		StatusURL:         upgradeservice.LegacyHealthURL(),
		SourceFingerprint: "observed",
		ObservedAtMS:      time.Now().UnixMilli(),
	}); err != nil {
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

	code, stdout, stderr := run(t, "run", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target)
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

// TestRunReportsARolledBackRowWithASurvivingMarkerWithoutWriting is the N2
// regression: a marker that outlived its committed row used to skip the
// preflight guard entirely, so `upgrade run` reopened the abandoned store,
// migrated it once more and rewrote the terminal row. The guard now skips only a
// materializing marker; every other state is read through a private snapshot and
// a later stage is reported with no write.
func TestRunReportsARolledBackRowWithASurvivingMarkerWithoutWriting(t *testing.T) {
	source := copyReadyRunSource(t)
	target := rollbackReadyTarget(t, source)
	databasePath := filepath.Join(target, "usage.sqlite")
	db, err := sqliterepo.Open(databasePath)
	if err != nil {
		t.Fatalf("open the v2 store: %v", err)
	}
	if _, err := upgradeoperation.New(db).RecordRollback(context.Background(), model.RollbackEvidence{
		Image:             upgradeservice.LegacyImageRepository + ":v1.14.7",
		ImageSource:       upgradeservice.RollbackImageSourceOperator,
		ImageForm:         upgradeservice.RollbackImageFormReleaseTag,
		StatusURL:         upgradeservice.LegacyHealthURL(),
		SourceFingerprint: "observed",
		ObservedAtMS:      time.Now().UnixMilli(),
	}); err != nil {
		_ = db.Close()
		t.Fatalf("record the rolled_back stage: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the v2 store: %v", err)
	}
	rolled, found, err := upgradeservice.ReadRecord(context.Background(), databasePath)
	if err != nil || !found {
		t.Fatalf("read the rolled-back row: found=%t err=%v", found, err)
	}
	identity, err := copyinmarker.Identity(databasePath)
	if err != nil {
		t.Fatalf("identity the v2 database: %v", err)
	}
	// The marker a crash between the record commit and the work-directory
	// removal can leave: migrating, and naming exactly this database and backup.
	if err := copyinmarker.Write(target, copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseMigrating,
		TargetDatabase:          databasePath,
		TargetDatabaseIdentity:  identity,
		SourceKind:              rolled.SourceKind,
		SourceVersion:           rolled.SourceVersion,
		SourceFingerprintBefore: rolled.SourceFingerprintBefore,
		SourceFingerprintAfter:  rolled.SourceFingerprintAfter,
		BackupManifestSHA256:    rolled.BackupManifestSHA256,
		BackupDirectory:         filepath.Join(target, "upgrade-backup"),
		WrittenAtMS:             time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("publish the surviving marker: %v", err)
	}

	code, stdout, stderr := run(t, "run", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target)
	if code != 0 {
		t.Fatalf("run exit code = %d, want 0; stdout: %s stderr: %s", code, stdout, stderr)
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
		t.Fatalf("re-read the rolled-back row: found=%t err=%v", found, err)
	}
	if after.Stage != rolled.Stage || after.UpdatedAtMS != rolled.UpdatedAtMS {
		t.Fatalf("run touched a rolled-back row: before=%+v after=%+v", rolled, after)
	}
	if after.Rollback == nil || rolled.Rollback == nil || *after.Rollback != *rolled.Rollback {
		t.Fatalf("run rewrote the recorded evidence: before=%+v after=%+v", rolled.Rollback, after.Rollback)
	}
	if _, found, err := copyinmarker.Read(target); err != nil || !found {
		t.Fatalf("run removed the marker the resume owns: found=%t err=%v", found, err)
	}
}

// managerAnswer is a listener that answers exactly as the Manager does on
// GET /health. The 1.x and the v2 Manager serve the same body, so a test can
// prove the refusal is about the listener and not about the identity.
func managerAnswer(t *testing.T) (address string, statusURL string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != upgradeservice.LegacyHealthPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"service":"`+upgradeservice.LegacyServiceID+`"}`)
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://"), server.URL + upgradeservice.LegacyHealthPath
}

// freePort reserves and releases a TCP port, so a test can name a port that is
// free at the moment it is asked.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release the port: %v", err)
	}
	return port
}

// TestRollbackPrepareRefusesARunningV2Stack covers the missing precondition: a
// plan is not rollback-ready while the v2 stack still answers on the address the
// operator named, and nothing is written.
func TestRollbackPrepareRefusesARunningV2Stack(t *testing.T) {
	source := copyReadyRunSource(t)
	target := rollbackReadyTarget(t, source)
	address, _ := managerAnswer(t)

	code, stdout, stderr := run(t, "rollback", "--prepare", "--legacy", source, "--target", target,
		"--manager-addr", address, "--port", strconv.Itoa(freePort(t)))
	if code != 1 {
		t.Fatalf("prepare against a running v2 stack exit code = %d, want 1; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, string(upgradeservice.UpgradeErrorRollbackV2Running)) {
		t.Fatalf("refusal did not name its code: %s", stderr)
	}
	if _, err := os.Lstat(filepath.Join(target, ".upgrade-rollback")); !os.IsNotExist(err) {
		t.Fatalf("a refused prepare wrote a plan (err=%v)", err)
	}
}

// TestRollbackVerifyRefusesAProbeOfThisManager covers M4 end to end: the Manager
// answers /health with the 1.x identity, so a probe of the Manager's own
// listener is refused and no stage is written.
func TestRollbackVerifyRefusesAProbeOfThisManager(t *testing.T) {
	source := copyReadyRunSource(t)
	target := rollbackReadyTarget(t, source)
	address, statusURL := managerAnswer(t)

	code, stdout, stderr := run(t, "rollback", "--verify", "--legacy", source, "--target", target,
		"--status-url", statusURL, "--manager-addr", address)
	if code != 1 {
		t.Fatalf("verify against this Manager exit code = %d, want 1; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, string(upgradeservice.UpgradeErrorRollbackProbeSelf)) {
		t.Fatalf("refusal did not name its code: %s", stderr)
	}
	databasePath := filepath.Join(target, "usage.sqlite")
	recorded, found, err := upgradeservice.ReadRecord(context.Background(), databasePath)
	if err != nil || !found {
		t.Fatalf("read the recorded operation: found=%t err=%v", found, err)
	}
	if recorded.Stage != model.UpgradeStageCopyIn || recorded.Rollback != nil {
		t.Fatalf("a refused self probe wrote the row: %+v", recorded)
	}
}

// TestStatusAndPrepareReportAnOutdatedStore covers L-A: a store written before
// the rollback columns existed cannot be read read-only, and both commands say
// so with a typed code instead of surfacing raw SQL.
func TestStatusAndPrepareReportAnOutdatedStore(t *testing.T) {
	target := t.TempDir()
	databasePath := filepath.Join(target, "usage.sqlite")
	writeOutdatedStore(t, databasePath)

	code, stdout, stderr := run(t, "status", "--db-path", databasePath)
	if code != 1 {
		t.Fatalf("status exit code = %d, want 1; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, string(upgradeservice.UpgradeErrorStoreSchemaOutdated)) {
		t.Fatalf("status did not name the typed code: %s", stderr)
	}
	if strings.Contains(stderr, "no such column") || strings.Contains(stderr, "SQL logic error") {
		t.Fatalf("status surfaced raw SQL: %s", stderr)
	}

	// The refusal names the command that can actually add the columns.
	if !strings.Contains(stderr, "upgrade migrate") {
		t.Fatalf("status does not name a reachable remedy: %s", stderr)
	}
	// Running that command is the remedy: the same store becomes readable.
	if code, stdout, stderr := run(t, "migrate", "--db-path", databasePath); code != 0 {
		t.Fatalf("migrate exit code = %d; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if code, statusOut, statusErr := run(t, "status", "--db-path", databasePath); code != 0 {
		t.Fatalf("status after migrate exit code = %d; stdout: %s stderr: %s", code, statusOut, statusErr)
	}

	source := copyReadyRunSource(t)
	code, stdout, stderr = run(t, "rollback", "--prepare", "--legacy", source, "--target", target)
	if code != 1 {
		t.Fatalf("prepare exit code = %d, want 1; stdout: %s stderr: %s", code, stdout, stderr)
	}
	// The synthetic row names no fingerprints, so the plan is refused as
	// unproven; what matters is that the dead end is gone.
	if strings.Contains(stderr, string(upgradeservice.UpgradeErrorStoreSchemaOutdated)) {
		t.Fatalf("prepare still reports an outdated store after migrate: %s", stderr)
	}
	if strings.Contains(stderr, "no such column") {
		t.Fatalf("prepare surfaced raw SQL: %s", stderr)
	}
	if _, err := os.Lstat(filepath.Join(target, ".upgrade-rollback")); !os.IsNotExist(err) {
		t.Fatalf("a refused prepare wrote a plan (err=%v)", err)
	}
}

// TestUpgradeMigrateRefusesWhileTheV2StoreIsLocked keeps the remedy honest: the
// migration is a write, so a running Manager blocks it with a typed code and the
// columns stay missing.
func TestUpgradeMigrateRefusesWhileTheV2StoreIsLocked(t *testing.T) {
	target := t.TempDir()
	databasePath := filepath.Join(target, "usage.sqlite")
	writeOutdatedStore(t, databasePath)
	lock, err := processlock.Acquire(databasePath)
	if err != nil {
		t.Fatalf("hold the v2 process lock: %v", err)
	}
	defer func() { _ = lock.Close() }()
	code, stdout, stderr := run(t, "migrate", "--db-path", databasePath)
	if code != 1 {
		t.Fatalf("migrate exit code = %d, want 1; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, string(upgradeservice.UpgradeErrorStoreLocked)) {
		t.Fatalf("migrate did not name the typed code: %s", stderr)
	}
	if code, _, stderr := run(t, "status", "--db-path", databasePath); code != 1 ||
		!strings.Contains(stderr, string(upgradeservice.UpgradeErrorStoreSchemaOutdated)) {
		t.Fatalf("a refused migrate added the columns anyway: code=%d stderr=%s", code, stderr)
	}
}

// writeOutdatedStore writes the upgrade table as an earlier build created it:
// the table exists, the columns a rollback records do not.
func writeOutdatedStore(t *testing.T, databasePath string) {
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

// TestStatusAndPrepareReportATerminalRowWithoutEvidence covers the second shape
// of an outdated store: the row names the rolled_back stage and none of its
// evidence. This build refuses such a row on both write paths and a writable
// open repairs nothing, so both read paths have to report the same typed code,
// the same copy and the same exit status, without promising a repair.
func TestStatusAndPrepareReportATerminalRowWithoutEvidence(t *testing.T) {
	source := copyReadyRunSource(t)
	target := rollbackReadyTarget(t, source)
	databasePath := filepath.Join(target, "usage.sqlite")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath))
	if err != nil {
		t.Fatalf("open the v2 store: %v", err)
	}
	// Write behind the repository: this is the row the production paths refuse,
	// so only a build from before the evidence invariant could leave it.
	if _, err := db.Exec(`update upgrade_operations
		set stage = 'rolled_back',
			rollback_image = null, rollback_status_url = null,
			rollback_source_fingerprint = null, rollback_observed_at_ms = null
		where id = 1`); err != nil {
		_ = db.Close()
		t.Fatalf("write the evidence-less verdict: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the v2 store: %v", err)
	}

	statusCode, _, statusErr := run(t, "status", "--db-path", databasePath)
	prepareCode, _, prepareErr := run(t, "rollback", "--prepare", "--legacy", source, "--target", target)
	if statusCode != prepareCode {
		t.Fatalf("status exit code = %d, prepare exit code = %d, want one status", statusCode, prepareCode)
	}
	if statusCode != 1 {
		t.Fatalf("exit code = %d, want 1; status stderr: %s prepare stderr: %s", statusCode, statusErr, prepareErr)
	}
	for name, stderr := range map[string]string{"status": statusErr, "prepare": prepareErr} {
		if !strings.Contains(stderr, string(upgradeservice.UpgradeErrorStoreSchemaOutdated)) {
			t.Fatalf("%s did not name the typed code: %s", name, stderr)
		}
		for _, want := range []string{"cannot be read as recorded", "new v2 data root"} {
			if !strings.Contains(stderr, want) {
				t.Fatalf("%s does not say %q: %s", name, want, stderr)
			}
		}
		if strings.Contains(stderr, "open it once with this build") {
			t.Fatalf("%s promises a repair a writable open cannot make: %s", name, stderr)
		}
	}
	if _, err := os.Lstat(filepath.Join(target, ".upgrade-rollback")); !os.IsNotExist(err) {
		t.Fatalf("a refused prepare wrote a plan (err=%v)", err)
	}
}

// TestRollbackManagerAddressDefaultsFromConfiguredHTTPAddr is the L5
// regression: the CLI left --manager-addr empty, so the service assumed port
// 18317 while the Manager may listen elsewhere. The configured HTTP_ADDR is the
// address the checks have to compare the probe with and prove stopped.
func TestRollbackManagerAddressDefaultsFromConfiguredHTTPAddr(t *testing.T) {
	source := copyReadyRunSource(t)
	target := rollbackReadyTarget(t, source)
	configuredPort := freePort(t)
	t.Setenv("HTTP_ADDR", "0.0.0.0:"+strconv.Itoa(configuredPort))

	code, stdout, stderr := run(t, "rollback", "--prepare", "--legacy", source, "--target", target, "--json")
	if code != 0 {
		t.Fatalf("prepare exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var plan upgradeservice.Plan
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatalf("decode plan: %v; output: %s", err, stdout)
	}
	if plan.ManagerPort != configuredPort {
		t.Fatalf("plan ManagerPort = %d, want the configured %d", plan.ManagerPort, configuredPort)
	}
	if plan.ManagerAddress != "0.0.0.0:"+strconv.Itoa(configuredPort) {
		t.Fatalf("plan ManagerAddress = %q, want the configured listen address", plan.ManagerAddress)
	}
	// The printed verify command carries the same address, so the verdict is
	// decided against the port the Manager actually holds.
	code, human, stderr := run(t, "rollback", "--prepare", "--legacy", source, "--target", target)
	if code != 0 {
		t.Fatalf("human prepare exit code = %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(human, `--manager-addr '0.0.0.0:`+strconv.Itoa(configuredPort)+`'`) {
		t.Fatalf("printed verify command does not carry the configured address:\n%s", human)
	}
}

// TestRollbackManagerAddressTreatsAWildcardBindAsThisHost keeps the wildcard
// half honest: a Manager bound to 0.0.0.0 listens on loopback too, so a
// listener there is the v2 stack and the plan must be refused.
func TestRollbackManagerAddressTreatsAWildcardBindAsThisHost(t *testing.T) {
	source := copyReadyRunSource(t)
	target := rollbackReadyTarget(t, source)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	t.Setenv("HTTP_ADDR", "0.0.0.0:"+strconv.Itoa(port))

	code, stdout, stderr := run(t, "rollback", "--prepare", "--legacy", source, "--target", target)
	if code != 1 {
		t.Fatalf("prepare against a wildcard-bound v2 stack exit code = %d, want 1; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, string(upgradeservice.UpgradeErrorRollbackV2Running)) {
		t.Fatalf("refusal did not name %q: %s", upgradeservice.UpgradeErrorRollbackV2Running, stderr)
	}
}

// TestRollbackPreparePrintsTheOperatorVerifiedIdentity covers the output half of
// M6: the console has to say what the plan proves and what the operator has to
// confirm, and it has to accept the volume forms 1.x installs actually use.
func TestRollbackPreparePrintsTheOperatorVerifiedIdentity(t *testing.T) {
	source := copyReadyRunSource(t)
	target := rollbackReadyTarget(t, source)
	code, stdout, stderr := run(t, "rollback", "--prepare", "--legacy", source, "--target", target,
		"--volume", "cpa-manager-plus-data")
	if code != 0 {
		t.Fatalf("prepare exit code = %d; stderr: %s", code, stderr)
	}
	for _, want := range []string{"cpa-manager-plus-data (named)", "v1 identity: operator-verified", "cannot reach the Docker socket"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("prepare output does not contain %q:\n%s", want, stdout)
		}
	}
}

// TestStatusShowsTheRolledBackReason is the L1 regression: a rolled-back
// operation keeps the Upgrade surface closed, and the typed reason the admission
// attaches was only in the process log. The operator console reads it from
// `upgrade status` in both forms; a copy-in row carries no such reason.
func TestStatusShowsTheRolledBackReason(t *testing.T) {
	source := copyReadyRunSource(t)
	target := rollbackReadyTarget(t, source)
	databasePath := filepath.Join(target, "usage.sqlite")

	code, statusJSON, stderr := run(t, "status", "--json", "--db-path", databasePath)
	if code != 0 {
		t.Fatalf("status before the verdict exit code = %d; stderr: %s", code, stderr)
	}
	var before statusOutput
	if err := json.Unmarshal([]byte(statusJSON), &before); err != nil {
		t.Fatalf("decode status: %v; output: %s", err, statusJSON)
	}
	if before.Reason != "" {
		t.Fatalf("a copy-in row reported a reason: %q", before.Reason)
	}

	if code, stdout, stderr := run(t, "rollback", "--verify", "--legacy", source, "--target", target,
		"--status-url", healthyProbe(t)); code != 0 {
		t.Fatalf("verify exit code = %d; stdout: %s stderr: %s", code, stdout, stderr)
	}

	code, human, stderr := run(t, "status", "--db-path", databasePath)
	if code != 0 {
		t.Fatalf("status exit code = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(human, "reason: upgrade_rolled_back") {
		t.Fatalf("status does not show the typed reason:\n%s", human)
	}
	code, statusJSON, stderr = run(t, "status", "--json", "--db-path", databasePath)
	if code != 0 {
		t.Fatalf("status --json exit code = %d; stderr: %s", code, stderr)
	}
	var after statusOutput
	if err := json.Unmarshal([]byte(statusJSON), &after); err != nil {
		t.Fatalf("decode status: %v; output: %s", err, statusJSON)
	}
	if after.Reason != "upgrade_rolled_back" {
		t.Fatalf("status reason = %q, want upgrade_rolled_back", after.Reason)
	}
	if after.Operation == nil || after.Operation.Stage != model.UpgradeStageRolledBack {
		t.Fatalf("status operation = %+v, want the rolled_back row", after.Operation)
	}
}

// writeLegacyManagerStore creates the shape a 1.x install has: no
// upgrade_operations table at all. A writable open of this build would migrate
// it into a native-v2 store, which is exactly what `upgrade migrate` may not do.
func writeLegacyManagerStore(t *testing.T, databasePath string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath))
	if err != nil {
		t.Fatalf("create the 1.x store: %v", err)
	}
	for _, statement := range []string{
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
		`insert into settings (key, value, updated_at_ms) values ('legacy_fixture_marker', 'kept', 1)`,
		`create table usage_events (
			id integer primary key autoincrement,
			event_hash text not null unique,
			timestamp_ms integer not null
		)`,
		`insert into usage_events (event_hash, timestamp_ms) values ('legacy-event-1', 1000)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("write the 1.x store: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the 1.x store: %v", err)
	}
}

// TestRunRefusesATypedStoreErrorWithExitOneAndOneStderrLine is the L4
// regression at the command boundary: an operational refusal is exit 1 and is
// written to stderr, while an environment or usage failure stays exit 2. It used
// to return the typed error unwrapped, which reported exit 2 with empty stderr.
func TestRunRefusesATypedStoreErrorWithExitOneAndOneStderrLine(t *testing.T) {
	target := t.TempDir()
	writeOutdatedStore(t, filepath.Join(target, "usage.sqlite"))
	code, stdout, stderr := run(t, "run", "--legacy", t.TempDir(),
		"--source-version", bootstrap.PlannedDirectRelease, "--target", target)
	if code != 1 {
		t.Fatalf("run exit code = %d, want 1; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("a refusal printed a report: %s", stdout)
	}
	if count := strings.Count(stderr, string(upgradeservice.UpgradeErrorStoreSchemaOutdated)); count != 1 {
		t.Fatalf("the typed code appears %d times in stderr, want once: %s", count, stderr)
	}
}

// TestUpgradeMigrateRefusesAStoreItDidNotCopyIn is the regression for the
// migration gate: `upgrade migrate` is the remedy for a copied-in store whose
// upgrade table predates this build's columns. A 1.x store or a native store
// without a recorded copy-in has no such row, and opening it writable would
// migrate it into a store this build admits as native - without the copy
// evidence the Upgrade admission requires. Such a store is refused before any
// writable open, so its bytes do not change.
func TestUpgradeMigrateRefusesAStoreItDidNotCopyIn(t *testing.T) {
	refusal := string(upgradeservice.UpgradeErrorMigrateNotCopiedIn)
	t.Run("a 1.x store", func(t *testing.T) {
		databasePath := filepath.Join(t.TempDir(), "usage.sqlite")
		writeLegacyManagerStore(t, databasePath)
		before, err := os.ReadFile(databasePath)
		if err != nil {
			t.Fatalf("read the 1.x store before migrate: %v", err)
		}
		code, stdout, stderr := run(t, "migrate", "--db-path", databasePath)
		if code != 1 {
			t.Fatalf("migrate exit code = %d, want 1; stdout: %s stderr: %s", code, stdout, stderr)
		}
		if !strings.Contains(stderr, refusal) {
			t.Fatalf("migrate did not name %q: %s", refusal, stderr)
		}
		after, err := os.ReadFile(databasePath)
		if err != nil {
			t.Fatalf("read the 1.x store after migrate: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("a refused migrate opened the 1.x store writable and rewrote its bytes")
		}
	})
	t.Run("a native v2 store without a recorded operation", func(t *testing.T) {
		databasePath := filepath.Join(t.TempDir(), "usage.sqlite")
		db, err := sqliterepo.Open(databasePath)
		if err != nil {
			t.Fatalf("create the native v2 store: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close the native v2 store: %v", err)
		}
		code, stdout, stderr := run(t, "migrate", "--db-path", databasePath)
		if code != 1 {
			t.Fatalf("migrate exit code = %d, want 1; stdout: %s stderr: %s", code, stdout, stderr)
		}
		if !strings.Contains(stderr, refusal) {
			t.Fatalf("migrate did not name %q: %s", refusal, stderr)
		}
	})
}

// TestUpgradeMigrateStillAddsColumnsToACopiedInStore keeps the remedy reachable:
// a store whose row records the copy-in may have its upgrade table brought up to
// this build's columns.
func TestUpgradeMigrateStillAddsColumnsToACopiedInStore(t *testing.T) {
	source := copyReadyRunSource(t)
	target := rollbackReadyTarget(t, source)
	databasePath := filepath.Join(target, "usage.sqlite")
	downgradeRollbackColumns(t, databasePath)

	if code, _, stderr := run(t, "status", "--db-path", databasePath); code != 1 ||
		!strings.Contains(stderr, string(upgradeservice.UpgradeErrorStoreSchemaOutdated)) {
		t.Fatalf("status on the copied-in store exit code = %d, want 1; stderr: %s", code, stderr)
	}
	if code, stdout, stderr := run(t, "migrate", "--db-path", databasePath); code != 0 {
		t.Fatalf("migrate of the copied-in store exit code = %d; stdout: %s stderr: %s", code, stdout, stderr)
	}
	code, stdout, stderr := run(t, "status", "--json", "--db-path", databasePath)
	if code != 0 {
		t.Fatalf("status after migrate exit code = %d; stderr: %s", code, stderr)
	}
	var status statusOutput
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("decode status: %v; output: %s", err, stdout)
	}
	if status.Operation == nil || status.Operation.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("status after migrate = %+v, want the recorded copy_in", status.Operation)
	}
}

// downgradeRollbackColumns rewrites the copied-in store's upgrade table into the
// shape an earlier build wrote: the table and its row survive, the columns a
// rollback records do not exist.
func downgradeRollbackColumns(t *testing.T, databasePath string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath))
	if err != nil {
		t.Fatalf("open the copied-in store: %v", err)
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`pragma foreign_keys = off`,
		`create table upgrade_operations_previous (
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
		`insert into upgrade_operations_previous (
			id, stage, domains_done, source_kind, source_version,
			source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256,
			reconciliation, manual_items, error_code, error_detail, created_at_ms, updated_at_ms)
		select id, stage, domains_done, source_kind, source_version,
			source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256,
			reconciliation, manual_items, error_code, error_detail, created_at_ms, updated_at_ms
		from upgrade_operations`,
		`drop table upgrade_operations`,
		`alter table upgrade_operations_previous rename to upgrade_operations`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("downgrade the copied-in store: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the downgraded store: %v", err)
	}
}

// TestRunLeavesAMaterializingTargetUntouched is the regression for the copy-in
// pre-guard: a resume reads the marker and the members beside it with file
// digests, never the database. The CLI read the operation row first, which
// opened the materializing target's WAL and created its WAL index, so the run
// the operator was resuming could no longer match the marker.
func TestRunLeavesAMaterializingTargetUntouched(t *testing.T) {
	source := copyReadyRunSource(t)
	target := t.TempDir()
	writeMaterializingTarget(t, target, source)
	databasePath := filepath.Join(target, "usage.sqlite")
	walPath := databasePath + "-wal"
	shmPath := databasePath + "-shm"
	if _, err := os.Lstat(shmPath); !os.IsNotExist(err) {
		t.Fatalf("the fixture already has a WAL index (err=%v)", err)
	}
	walBefore, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatalf("read the target WAL before the run: %v", err)
	}

	run(t, "run", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target)

	if _, err := os.Lstat(shmPath); !os.IsNotExist(err) {
		t.Fatalf("the pre-guard opened the materializing target and created its WAL index (err=%v)", err)
	}
	walAfter, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatalf("read the target WAL after the run: %v", err)
	}
	if !bytes.Equal(walBefore, walAfter) {
		t.Fatal("the pre-guard opened the materializing target and rewrote its WAL")
	}
}

// holdSourceWAL keeps committed frames in the 1.x WAL, exactly as a stopped
// source preserved on disk may still hold them. A copy-in interrupted before
// the migration then leaves a target whose database plus WAL is the restored
// member, which an in-place read would rewrite.
func holdSourceWAL(t *testing.T, source string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(source, "usage.sqlite"))
	if err != nil {
		t.Fatalf("open the 1.x fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`pragma journal_mode = wal`,
		`pragma wal_autocheckpoint = 0`,
		`insert into settings (key, value, updated_at_ms) values ('wal_frame_probe', '1', 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("hold the 1.x WAL: %v", err)
		}
	}
}

// copyTestTree copies a file or directory into destination byte for byte.
func copyTestTree(t *testing.T, source, destination string) {
	t.Helper()
	info, err := os.Stat(source)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("inspect %s: %v", source, err)
	}
	if !info.IsDir() {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read %s: %v", source, err)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatalf("create %s: %v", filepath.Dir(destination), err)
		}
		if err := os.WriteFile(destination, data, info.Mode().Perm()); err != nil {
			t.Fatalf("write %s: %v", destination, err)
		}
		return
	}
	if err := os.MkdirAll(destination, info.Mode().Perm()); err != nil {
		t.Fatalf("create %s: %v", destination, err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatalf("read %s: %v", source, err)
	}
	for _, entry := range entries {
		copyTestTree(t, filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name()))
	}
}

// interruptedCopyInTarget rebuilds the v2 data root a copy-in leaves at its
// target-restored checkpoint: every member restored from the verified backup and
// the materializing marker published, but no operation row yet. It is built
// from a real copy-in's backup, so the marker names the backup the resume will
// verify, which is what makes the following resume a real resume and not a
// second copy.
func interruptedCopyInTarget(t *testing.T, source string) (string, model.UpgradeOperation) {
	t.Helper()
	reference := rollbackReadyTarget(t, source)
	recorded, found, err := upgradeservice.ReadRecord(context.Background(), filepath.Join(reference, "usage.sqlite"))
	if err != nil || !found {
		t.Fatalf("read the reference copy-in: found=%t err=%v", found, err)
	}
	target := t.TempDir()
	bundle := filepath.Join(reference, "upgrade-backup")
	copyTestTree(t, bundle, filepath.Join(target, "upgrade-backup"))
	for _, member := range []struct{ bundle, target string }{
		{"database", "usage.sqlite"},
		{"database-wal", "usage.sqlite-wal"},
		{"database-shm", "usage.sqlite-shm"},
		{"database-journal", "usage.sqlite-journal"},
		{"data-key", "data.key"},
	} {
		copyTestTree(t, filepath.Join(bundle, member.bundle), filepath.Join(target, member.target))
	}
	copyTestTree(t, filepath.Join(bundle, "usage-archives"), filepath.Join(target, "usage-archives"))
	if err := copyinmarker.Write(target, copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseMaterializing,
		TargetDatabase:          filepath.Join(target, "usage.sqlite"),
		SourceKind:              recorded.SourceKind,
		SourceVersion:           recorded.SourceVersion,
		SourceFingerprintBefore: recorded.SourceFingerprintBefore,
		SourceFingerprintAfter:  recorded.SourceFingerprintAfter,
		BackupManifestSHA256:    recorded.BackupManifestSHA256,
		BackupDirectory:         filepath.Join(target, "upgrade-backup"),
		WrittenAtMS:             time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("publish the materializing marker: %v", err)
	}
	return target, recorded
}

// TestRollbackRefusesAMaterializingTargetAndTheResumeStillFinishes is the N1
// regression: --prepare and --verify used to read the operation row first, which
// opened the interrupted copy-in's target in place, created or rewrote its WAL
// index, and left `upgrade run` unable to resume. Both commands now decide from
// the marker, write nothing, and the resume still reaches copy_in.
func TestRollbackRefusesAMaterializingTargetAndTheResumeStillFinishes(t *testing.T) {
	for _, liveWAL := range []bool{false, true} {
		for _, mode := range []string{"prepare", "verify"} {
			name := fmt.Sprintf("%s/live-wal-%t", mode, liveWAL)
			t.Run(name, func(t *testing.T) {
				source := copyReadyRunSource(t)
				if liveWAL {
					holdSourceWAL(t, source)
				}
				target, _ := interruptedCopyInTarget(t, source)
				databasePath := filepath.Join(target, "usage.sqlite")
				shmPath := databasePath + "-shm"
				walPath := databasePath + "-wal"
				shmBefore, shmErr := os.Lstat(shmPath)
				databaseBefore, err := os.ReadFile(databasePath)
				if err != nil {
					t.Fatalf("read the target database: %v", err)
				}
				walBefore, walErr := os.ReadFile(walPath)

				args := []string{"rollback", "--" + mode, "--legacy", source, "--target", target}
				if mode == "verify" {
					args = append(args, "--status-url", healthyProbe(t))
				}
				code, stdout, stderr := run(t, args...)
				if code != 1 {
					t.Fatalf("rollback --%s exit code = %d, want 1; stdout: %s stderr: %s", mode, code, stdout, stderr)
				}
				if !strings.Contains(stderr, string(upgradeservice.UpgradeErrorRollbackNotApplicable)) {
					t.Fatalf("rollback --%s did not refuse as not applicable: %s", mode, stderr)
				}
				if !strings.Contains(stderr, "upgrade run") {
					t.Fatalf("rollback --%s does not name the resume command: %s", mode, stderr)
				}
				if _, err := os.Lstat(filepath.Join(target, ".upgrade-rollback")); !os.IsNotExist(err) {
					t.Fatalf("a refused rollback --%s wrote a plan (err=%v)", mode, err)
				}
				// The refusal must not have opened the target: no WAL index may
				// appear and the database and WAL bytes must be untouched.
				shmAfter, afterErr := os.Lstat(shmPath)
				if (shmErr == nil) != (afterErr == nil) {
					t.Fatalf("rollback --%s changed the target WAL index presence: before=%v after=%v", mode, shmErr, afterErr)
				}
				if shmErr == nil && shmAfter.Size() != shmBefore.Size() {
					t.Fatalf("rollback --%s rewrote the target WAL index: %d -> %d bytes", mode, shmBefore.Size(), shmAfter.Size())
				}
				databaseAfter, err := os.ReadFile(databasePath)
				if err != nil {
					t.Fatalf("re-read the target database: %v", err)
				}
				if !bytes.Equal(databaseBefore, databaseAfter) {
					t.Fatalf("rollback --%s rewrote the target database", mode)
				}
				walAfter, afterWalErr := os.ReadFile(walPath)
				if (walErr == nil) != (afterWalErr == nil) || (walErr == nil && !bytes.Equal(walBefore, walAfter)) {
					t.Fatalf("rollback --%s rewrote the target WAL", mode)
				}

				code, resumeOut, resumeErr := run(t, "run", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target)
				if code != 0 {
					t.Fatalf("resume after rollback --%s exit code = %d; stdout: %s stderr: %s", mode, code, resumeOut, resumeErr)
				}
				operation, found, err := upgradeservice.ReadRecord(context.Background(), databasePath)
				if err != nil || !found {
					t.Fatalf("read the resumed operation: found=%t err=%v", found, err)
				}
				if operation.Stage != model.UpgradeStageCopyIn {
					t.Fatalf("resume after rollback --%s stage = %q, want %q", mode, operation.Stage, model.UpgradeStageCopyIn)
				}
			})
		}
	}
}

// writeMaterializingTarget leaves the target of an interrupted copy-in: a
// WAL-mode database with an un-checkpointed WAL and no WAL index, plus the
// materializing marker that vouches for a backup this run will not hold.
func writeMaterializingTarget(t *testing.T, targetRoot, sourceRoot string) {
	t.Helper()
	staging := t.TempDir()
	stagedPath := filepath.Join(staging, "usage.sqlite")
	source, err := os.ReadFile(filepath.Join(sourceRoot, "usage.sqlite"))
	if err != nil {
		t.Fatalf("read the 1.x fixture database: %v", err)
	}
	if err := os.WriteFile(stagedPath, source, 0o600); err != nil {
		t.Fatalf("stage the target database: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(stagedPath))
	if err != nil {
		t.Fatalf("open the staged target: %v", err)
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`pragma journal_mode = wal`,
		`create table if not exists copy_in_probe (id integer primary key)`,
		`insert into copy_in_probe (id) values (1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("write the staged target: %v", err)
		}
	}
	// Copy the database and its WAL while the writer is still open, so the WAL
	// holds committed pages the main file does not. The WAL index is deliberately
	// not copied, exactly as a killed process leaves it.
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(stagedPath + suffix)
		if err != nil {
			_ = db.Close()
			t.Fatalf("read the staged target %s: %v", suffix, err)
		}
		if err := os.WriteFile(filepath.Join(targetRoot, "usage.sqlite"+suffix), data, 0o600); err != nil {
			_ = db.Close()
			t.Fatalf("write the materializing target %s: %v", suffix, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the staged target: %v", err)
	}
	if err := copyinmarker.Write(targetRoot, copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseMaterializing,
		TargetDatabase:          filepath.Join(targetRoot, "usage.sqlite"),
		SourceKind:              upgradeservice.SourceKindUpgradedLegacy,
		SourceVersion:           bootstrap.PlannedDirectRelease,
		SourceFingerprintBefore: "before",
		BackupManifestSHA256:    "0000000000000000000000000000000000000000000000000000000000000000",
		WrittenAtMS:             time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("publish the materializing marker: %v", err)
	}
}
