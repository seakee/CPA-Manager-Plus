package upgrade

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
	if !strings.HasSuffix(evidence.StatusURL, upgradeservice.LegacyHealthPath) {
		t.Fatalf("recorded health endpoint = %q, want %q", evidence.StatusURL, upgradeservice.LegacyHealthPath)
	}
	if evidence.SourceFingerprint == "" || evidence.ObservedAtMS <= 0 {
		t.Fatalf("recorded observation = %+v", evidence)
	}
}

func TestRollbackVerifyRefusesUnprovenEvidence(t *testing.T) {
	source := copyReadySource(t)
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
	source := copyReadySource(t)
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
	source := copyReadySource(t)
	target := rollbackReadyTarget(t, source)
	code, human, stderr := run(t, "rollback", "--prepare", "--legacy", source, "--target", target,
		"--legacy-image", "v1.13.0", "--allow-image-family-mismatch")
	if code != 0 {
		t.Fatalf("prepare exit code = %d, want 0; stderr: %s", code, stderr)
	}
	for _, want := range []string{
		"--legacy-image \"" + upgradeservice.LegacyImageRepository + ":v1.13.0\"",
		"--allow-image-family-mismatch",
		"--status-url \"" + upgradeservice.LegacyHealthURL() + "\"",
		"--backup \"",
		"--target \"",
		"--legacy \"",
	} {
		if !strings.Contains(human, want) {
			t.Fatalf("printed verify command does not carry %q: %s", want, human)
		}
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
	// The copy-in records the 1.x directory it read, so a later rollback can
	// bind its volume instead of asserting "the original volume".
	if operation.SourceRoot == "" {
		t.Fatalf("the copy-in recorded no source root: %+v", operation)
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
	if _, err := upgradeoperation.New(db).RecordRollback(context.Background(), model.RollbackEvidence{
		Image:             upgradeservice.LegacyImageRepository + ":v1.14.7",
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
	source := copyReadySource(t)
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
	source := copyReadySource(t)
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

	source := copyReadySource(t)
	code, stdout, stderr = run(t, "rollback", "--prepare", "--legacy", source, "--target", target)
	if code != 1 {
		t.Fatalf("prepare exit code = %d, want 1; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, string(upgradeservice.UpgradeErrorStoreSchemaOutdated)) {
		t.Fatalf("prepare did not name the typed code: %s", stderr)
	}
	if strings.Contains(stderr, "no such column") {
		t.Fatalf("prepare surfaced raw SQL: %s", stderr)
	}
	if _, err := os.Lstat(filepath.Join(target, ".upgrade-rollback")); !os.IsNotExist(err) {
		t.Fatalf("a refused prepare wrote a plan (err=%v)", err)
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
		`insert into upgrade_operations (id, stage, domains_done, source_kind, created_at_ms, updated_at_ms)
			values (1, 'copy_in', '[]', 'upgraded-legacy', 1, 1)`,
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
	source := copyReadySource(t)
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
