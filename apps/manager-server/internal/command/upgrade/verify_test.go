package upgrade

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// storeManagerConnection records the CPA connection the Manager itself resolves.
// It is the target the verification compares the recorded External binding
// against, so a test that exercises the External path has to store the same CPA
// the connect step recorded.
func storeManagerConnection(t *testing.T, target, address, secret string) {
	t.Helper()
	dataKeyPath := filepath.Join(target, "data.key")
	key, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
	if err != nil {
		t.Fatalf("load the Manager data key: %v", err)
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		t.Fatalf("initialize the secret protector: %v", err)
	}
	db, err := managersqlite.Open(filepath.Join(target, upgradeservice.SourceDatabaseName))
	if err != nil {
		t.Fatalf("open the v2 database: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := store.New(db, protector).SaveManagerConfig(context.Background(), model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: address, ManagementKey: secret},
	}); err != nil {
		t.Fatalf("store the Manager CPA connection: %v", err)
	}
}

// verifiedTarget copies a real 1.x fixture into a v2 data root, reconciles it and
// connects an External CPA, using the operator's own subcommands. What the
// verification then proves is exactly the state the pipeline recorded.
func verifiedTarget(t *testing.T, cpaAddress string) (source, target string) {
	t.Helper()
	source = copyReadySource(t)
	target = t.TempDir()
	bundle := filepath.Join(t.TempDir(), "backup")
	if code, _, stderr := run(t, "run", "--legacy", source,
		"--source-version", bootstrap.PlannedDirectRelease,
		"--target", target, "--bundle", bundle); code != 0 {
		t.Fatalf("copy in: exit %d; stderr: %s", code, stderr)
	}
	if code, stdout, stderr := run(t, "reconcile", "--legacy", source,
		"--source-version", bootstrap.PlannedDirectRelease, "--target", target,
		"--bundle", bundle, "--json"); code != 0 {
		t.Fatalf("reconcile: exit %d; stderr: %s; stdout: %s", code, stderr, stdout)
	}
	if code, _, stderr := run(t, "external-connect", "--target", target,
		"--cpa-base-url", cpaAddress,
		"--management-key-file", managementKeyFile(t, cliManagementKey)); code != 0 {
		t.Fatalf("external connect: exit %d; stderr: %s", code, stderr)
	}
	storeManagerConnection(t, target, cpaAddress, cliManagementKey)
	return source, target
}

// TestVerifyCLICompletesTheExternalOperation drives the whole External pipeline
// through the operator's subcommands and then the gate. The printed report names
// the position and its domains, never the evidence the verdict rests on: a
// fingerprint is a digest of the operator's volume and this line is a report.
func TestVerifyCLICompletesTheExternalOperation(t *testing.T) {
	server := newExternalCPAServer(t, "v8.0.13")
	source, target := verifiedTarget(t, server.URL)

	code, stdout, stderr := run(t, "verify", "--legacy", source, "--target", target, "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var output verifyOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode output: %v; output: %s", err, stdout)
	}
	if output.Error != "" || output.ErrorCode != "" {
		t.Fatalf("output reported a failure: %+v", output)
	}
	if output.Result == nil {
		t.Fatalf("output carried no result: %s", stdout)
	}
	if !output.Result.Complete || output.Result.Stage != model.UpgradeStageComplete {
		t.Fatalf("result = %+v, want a completed operation", output.Result)
	}
	if output.Result.TargetMode != model.UpgradeTargetExternal {
		t.Fatalf("target mode = %q, want external", output.Result.TargetMode)
	}
	if output.Result.AlreadyVerified {
		t.Fatalf("the first verification reported itself as a repeat: %+v", output.Result)
	}
	// The report carries no observation of the volume and no path.
	for _, forbidden := range []string{target, source, "sha256:", "sourceFingerprint", "bindingIdentity", cliManagementKey} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("the report leaked %q: %s", forbidden, stdout)
		}
	}
	// The verdict was printed once, and nothing was printed to stderr.
	if got := strings.Count(stdout, `"result"`); got != 1 {
		t.Fatalf("the report was printed %d time(s): %s", got, stdout)
	}
	if stderr != "" {
		t.Fatalf("a successful verification wrote to stderr: %s", stderr)
	}

	// A second run reports the recorded verdict and writes nothing.
	repeatCode, repeatOut, _ := run(t, "verify", "--legacy", source, "--target", target, "--json")
	if repeatCode != 0 {
		t.Fatalf("repeat exit code = %d, want 0", repeatCode)
	}
	var repeat verifyOutput
	if err := json.Unmarshal([]byte(repeatOut), &repeat); err != nil {
		t.Fatalf("decode repeat output: %v; output: %s", err, repeatOut)
	}
	if repeat.Result == nil || !repeat.Result.Complete || !repeat.Result.AlreadyVerified {
		t.Fatalf("repeat result = %+v, want the recorded verdict", repeat.Result)
	}
	if repeat.Result.UpdatedAtMS != output.Result.UpdatedAtMS {
		t.Fatalf("the repeat moved the timestamp: %d -> %d", output.Result.UpdatedAtMS, repeat.Result.UpdatedAtMS)
	}
}

// TestVerifyCLIRendersTheManagedCompletion: the print half of a Managed
// completion. The gate records `complete` for both target modes now, so the
// shell reports the verdict and the remaining manual items and exits 0.
func TestVerifyCLIRendersTheManagedCompletion(t *testing.T) {
	stdout := &strings.Builder{}
	writeVerifyOutput(stdout, false, verifyOutput{Result: &upgradeservice.VerifyResult{
		Stage:       model.UpgradeStageComplete,
		DomainsDone: []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig},
		Complete:    true,
		TargetMode:  model.UpgradeTargetManaged,
		ManualItems: []string{"section:ampcode: re-create it (reconfigure_after_adoption)"},
		UpdatedAtMS: 1,
	}})
	report := stdout.String()
	for _, expected := range []string{
		"stage: complete",
		"target: managed",
		"the operation is complete",
		"manual items: 1 recorded",
		"reconfigure_after_adoption",
	} {
		if !strings.Contains(report, expected) {
			t.Fatalf("the report does not carry %q: %s", expected, report)
		}
	}
	if strings.Contains(report, "waiting:") {
		t.Fatalf("a completed operation reported a waiting position: %s", report)
	}
}

// TestVerifyCLIExitsOneOnATypedRefusal: a decision about the recorded operation
// exits 1, and the code appears once on stderr and once in the JSON report.
func TestVerifyCLIExitsOneOnATypedRefusal(t *testing.T) {
	server := newExternalCPAServer(t, "v8.0.13")
	source, target := verifiedTarget(t, server.URL)
	// The recorded binding is gone, so the CPA the op connected cannot be named.
	db, err := managersqlite.Open(filepath.Join(target, upgradeservice.SourceDatabaseName))
	if err != nil {
		t.Fatalf("open the v2 database: %v", err)
	}
	if _, err := db.Exec(`delete from upgrade_external_connect`); err != nil {
		_ = db.Close()
		t.Fatalf("drop the recorded binding: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the v2 database: %v", err)
	}

	code, stdout, stderr := run(t, "verify", "--legacy", source, "--target", target, "--json")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, stderr)
	}
	want := string(upgradeservice.UpgradeErrorVerifyBindingMissing)
	var output verifyOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode output: %v; output: %s", err, stdout)
	}
	if output.ErrorCode != want || output.Result != nil {
		t.Fatalf("output = %+v, want code %q and no result", output, want)
	}
	if got := strings.Count(stderr, want); got != 1 {
		t.Fatalf("stderr carries the failure %d time(s), want 1: %s", got, stderr)
	}
}

// TestVerifyCLIExitsOneOnAnUnmountedSource: a source the gate cannot observe is
// a decision about the recorded operation, so it exits 1 with its code.
func TestVerifyCLIExitsOneOnAnUnmountedSource(t *testing.T) {
	server := newExternalCPAServer(t, "v8.0.13")
	_, target := verifiedTarget(t, server.URL)
	absent := filepath.Join(t.TempDir(), "unmounted")
	code, stdout, stderr := run(t, "verify", "--legacy", absent, "--target", target, "--json")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, stderr)
	}
	want := string(upgradeservice.UpgradeErrorVerifySourceUnavailable)
	var output verifyOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode output: %v; output: %s", err, stdout)
	}
	if output.ErrorCode != want {
		t.Fatalf("error code = %q, want %q", output.ErrorCode, want)
	}
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr does not name the refusal: %s", stderr)
	}
}

// TestVerifyCLIRejectsAPositionalArgument keeps the usage contract: the verdict
// is never named by a caller, so no positional argument is accepted.
func TestVerifyCLIRejectsAPositionalArgument(t *testing.T) {
	server := newExternalCPAServer(t, "v8.0.13")
	source, target := verifiedTarget(t, server.URL)
	code, _, _ := run(t, "verify", "--legacy", source, "--target", target, "complete")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	// The information is --legacy; without it the command is a usage error.
	code, _, _ = run(t, "verify", "--target", target)
	if code != 2 {
		t.Fatalf("exit code without --legacy = %d, want 2", code)
	}
}

// TestExitCodePinsTheVerifyRefusals adds the completion gate to the one
// exit-code table: a decision about the recorded operation exits 1, and the one
// retryable verdict — a row another writer changed — stays out of the table.
func TestExitCodePinsTheVerifyRefusals(t *testing.T) {
	typed := func(code upgradeservice.UpgradeErrorCode) error {
		return &upgradeservice.UpgradeError{Code: code, Message: "pinned by the exit-code table"}
	}
	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "position", err: upgradeservice.ErrVerifyNotApplicable, want: 1},
		{name: "store owned", err: upgradeservice.ErrVerifyV2Running, want: 1},
		{name: "source not mounted", err: upgradeservice.ErrVerifySourceUnavailable, want: 1},
		{name: "source changed", err: upgradeservice.ErrVerifySourceMismatch, want: 1},
		{name: "reconciliation invalid", err: upgradeservice.ErrVerifyReconciliationInvalid, want: 1},
		{name: "reconciliation unacknowledged", err: upgradeservice.ErrVerifyReconciliationUnacked, want: 1},
		{name: "domain missing", err: upgradeservice.ErrVerifyDomainsIncomplete, want: 1},
		{name: "binding missing", err: upgradeservice.ErrVerifyBindingMissing, want: 1},
		{name: "binding drifted", err: upgradeservice.ErrVerifyBindingDrifted, want: 1},
		{name: "connection unproven", err: upgradeservice.ErrVerifyConnectionUnproven, want: 1},
		{name: "source held", err: typed(upgradeservice.UpgradeErrorCode(upgradeservice.CodeSourceBusy)), want: 1},
		// A row another writer changed is retryable: re-running the verification
		// re-proves the current one.
		{name: "revision conflict", err: upgradeservice.ErrVerifyConflict, want: 2},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ExitCode(testCase.err); got != testCase.want {
				t.Fatalf("ExitCode(%v) = %d, want %d", testCase.err, got, testCase.want)
			}
		})
	}
}

// TestVerifyCLIUsageNamesTheCommand keeps the usage text honest: the operator
// has to find the step that records the verdict.
func TestVerifyCLIUsageNamesTheCommand(t *testing.T) {
	stdout := &strings.Builder{}
	writeUsage(stdout)
	usage := stdout.String()
	for _, expected := range []string{
		"verify --legacy <dir>",
		"the one verdict the",
		"waits for the trusted re-pin",
		"a verification the recorded operation does not support",
	} {
		if !strings.Contains(usage, expected) {
			t.Fatalf("usage does not mention %q:\n%s", expected, usage)
		}
	}
}

// TestVerifyCLIWritesNothingInsideTheSource keeps the read-only source contract:
// the gate fingerprints the volume and never writes there.
func TestVerifyCLIWritesNothingInsideTheSource(t *testing.T) {
	server := newExternalCPAServer(t, "v8.0.13")
	source, target := verifiedTarget(t, server.URL)
	before := treeDigests(t, source)
	if code, _, stderr := run(t, "verify", "--legacy", source, "--target", target); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	after := treeDigests(t, source)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the verification wrote inside the 1.x source")
	}
	if _, err := os.Stat(filepath.Join(source, upgradeservice.SourceDatabaseName+".manager.lock")); err != nil {
		// The copy-in created the marker; the verification must not remove it.
		t.Fatalf("the source ownership marker disappeared: %v", err)
	}
}
