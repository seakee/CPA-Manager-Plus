package upgrade

// These tests pin the `upgrade cpa-import` command's contract at the boundary an
// operator sees: the flags it requires, the exit codes it shares with the other
// steps, and the fact that it reads and writes nothing before the service layer's
// own refusals.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

func TestCPAImportCommandRequiresLegacyAndAPluginChoice(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := Run(context.Background(), []string{"cpa-import"}, stdout, stderr)
	if code := ExitCode(err); code != 2 {
		t.Fatalf("a run with no arguments exited %d, want 2 (%v)", code, err)
	}
	// The plugin flag is read as a string so "absent" and "false" stay different
	// inputs; an unparsable value is a usage error, never a default.
	if _, err := parsePluginChoice("maybe"); err == nil {
		t.Fatal("parsePluginChoice accepted an unparsable value")
	}
	absent, err := parsePluginChoice("")
	if err != nil || absent != nil {
		t.Fatalf("parsePluginChoice(\"\") = %v, %v; want nil, nil", absent, err)
	}
	disabled, err := parsePluginChoice("false")
	if err != nil || disabled == nil || *disabled {
		t.Fatalf("parsePluginChoice(\"false\") = %v, %v; want false", disabled, err)
	}
	enabled, err := parsePluginChoice("true")
	if err != nil || enabled == nil || !*enabled {
		t.Fatalf("parsePluginChoice(\"true\") = %v, %v; want true", enabled, err)
	}
}

func TestCPAImportCommandRefusesAPositionalArgument(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := Run(context.Background(), []string{"cpa-import", "--legacy", "/tmp/nowhere", "--plugins", "true", "extra"}, stdout, stderr)
	if code := ExitCode(err); code != 2 {
		t.Fatalf("a positional argument exited %d, want 2 (%v)", code, err)
	}
}

func TestCPAImportCommandIsListedInUsage(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := Run(context.Background(), []string{"help"}, stdout, stderr); err != nil {
		t.Fatalf("help returned %v", err)
	}
	if !strings.Contains(stdout.String(), "cpa-import") {
		t.Fatalf("the usage text does not name the subcommand:\n%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("help wrote to stderr: %s", stderr.String())
	}
}

// TestCPAImportCommandRefusesWhileTheStoreIsLocked pins the writer fence: the
// subcommand writes the operation row, the destination attempt and the Manager
// connection, so a held v2 data-root lock must stop it with a typed refusal and
// the operator exit status 1, not race a live Manager Server.
func TestCPAImportCommandRefusesWhileTheStoreIsLocked(t *testing.T) {
	targetRoot := t.TempDir()
	held, err := processlock.Acquire(filepath.Join(targetRoot, upgradeservice.SourceDatabaseName))
	if err != nil {
		t.Fatalf("hold the v2 store lock: %v", err)
	}
	defer func() { _ = held.Close() }()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err = Run(context.Background(), []string{
		"cpa-import",
		"--legacy", filepath.Join(targetRoot, "legacy"),
		"--plugins", "true",
		"--target", targetRoot,
		"--destination-url", "http://127.0.0.1:8317",
	}, stdout, stderr)
	if code, ok := upgradeservice.UpgradeErrorCodeOf(err); !ok || code != upgradeservice.UpgradeErrorStoreLocked {
		t.Fatalf("a held store lock produced %v (code %q), want %s", err, code, upgradeservice.UpgradeErrorStoreLocked)
	}
	if code := ExitCode(err); code != 1 {
		t.Fatalf("a held store lock exited %d, want 1 (%v)", code, err)
	}
}

// TestCPAImportCommandRefusesWhileTheSourceIsHeld pins the other fence: the
// import reads a source it assumes is stopped, so it holds the 1.x ownership
// marker for the whole run. A live holder, or a source with no marker at all,
// refuses with its own typed code and exit status 1 before the CPA directory is
// read.
func TestCPAImportCommandRefusesWhileTheSourceIsHeld(t *testing.T) {
	source := copyReadySource(t)
	held, err := processlock.AcquireExisting(filepath.Join(source, "usage.sqlite"))
	if err != nil {
		t.Fatalf("hold the 1.x ownership marker: %v", err)
	}
	defer func() { _ = held.Close() }()
	targetRoot := t.TempDir()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err = Run(context.Background(), []string{
		"cpa-import",
		"--legacy", source,
		"--plugins", "true",
		"--target", targetRoot,
		"--destination-url", "http://127.0.0.1:8317",
	}, stdout, stderr)
	if code, ok := upgradeservice.UpgradeErrorCodeOf(err); !ok || code != upgradeservice.UpgradeErrorCPASourceLocked {
		t.Fatalf("a held source marker produced %v (code %q), want %s", err, code, upgradeservice.UpgradeErrorCPASourceLocked)
	}
	if code := ExitCode(err); code != 1 {
		t.Fatalf("a held source marker exited %d, want 1 (%v)", code, err)
	}
	// The marker is closed; a source that carries none cannot be proven stopped.
	if err := held.Close(); err != nil {
		t.Fatalf("release the 1.x ownership marker: %v", err)
	}
	if err := os.Remove(filepath.Join(source, "usage.sqlite.manager.lock")); err != nil {
		t.Fatalf("remove the fixture ownership marker: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	err = Run(context.Background(), []string{
		"cpa-import",
		"--legacy", source,
		"--plugins", "true",
		"--target", t.TempDir(),
		"--destination-url", "http://127.0.0.1:8317",
	}, stdout, stderr)
	if code, ok := upgradeservice.UpgradeErrorCodeOf(err); !ok || code != upgradeservice.UpgradeErrorCPASourceLocked {
		t.Fatalf("a source without a marker produced %v (code %q), want %s", err, code, upgradeservice.UpgradeErrorCPASourceLocked)
	}
}

// TestCPAImportCommandTakesAReadOnlySourceMarker pins the caller migration: the
// 1.x source may be preserved on a read-only mount, so the command's fence must
// open the ownership marker read-only. A 0400 marker is readable and not writable,
// and the run has to get past the fence; the writable open would have refused it
// with upgrade_cpa_source_locked before anything else.
func TestCPAImportCommandTakesAReadOnlySourceMarker(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens a 0400 marker for writing, so the read-only case is not observable")
	}
	source := copyReadySource(t)
	if err := os.Chmod(filepath.Join(source, "usage.sqlite.manager.lock"), 0o400); err != nil {
		t.Fatalf("make the source marker read-only: %v", err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := Run(context.Background(), []string{
		"cpa-import",
		"--legacy", source,
		"--plugins", "true",
		"--target", t.TempDir(),
		"--destination-url", "http://127.0.0.1:8317",
	}, stdout, stderr)
	if code, ok := upgradeservice.UpgradeErrorCodeOf(err); ok && code == upgradeservice.UpgradeErrorCPASourceLocked {
		t.Fatalf("a read-only source marker was refused by the fence: %v", err)
	}
	if err == nil {
		t.Fatal("the run completed without a recorded copy-in or a configured Runtime")
	}
}

// TestCPAImportOutputCarriesOnlyTheContract is the earlier output test, fixed:
// it used to send an output with no Result at all, so the only keys that could
// appear were the ones it forbade. A populated result is now written and every
// key is compared with the contract, so a field added later that carries a secret
// has to be justified instead of silently passing.
func TestCPAImportOutputCarriesOnlyTheContract(t *testing.T) {
	result := upgradeservice.CPAImportResult{
		Stage:          "cpa_import",
		DomainsDone:    []string{"source_copy"},
		SourceVersion:  "8.0.55",
		SourceLayout:   "v8-legacy",
		SourceIdentity: "external:sha256:fixture",
		DestinationURL: "http://127.0.0.1:8317",
		PluginsEnabled: true,
		ClientKeys:     1,
		MigratedCount:  5,
		ManualItems: []upgradeservice.CPAImportManualItem{
			{Item: "section:ampcode", Reason: "reconfigure_after_adoption", Instruction: "re-create it"},
		},
		UpdatedAtMS: 1700000000000,
	}
	output := cpaImportOutput{Result: &result, Error: "refused", ErrorCode: "upgrade_cpa_version_unsupported", Next: "run status"}
	buffer := &bytes.Buffer{}
	writeCPAImportOutput(buffer, true, output)
	var decoded map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &decoded); err != nil {
		t.Fatalf("the JSON output is not valid JSON: %v", err)
	}
	allowedTop := map[string]bool{"result": true, "error": true, "errorCode": true, "next": true}
	for key := range decoded {
		if !allowedTop[key] {
			t.Fatalf("the output carries the unplanned key %q", key)
		}
	}
	encodedResult, ok := decoded["result"].(map[string]any)
	if !ok || len(encodedResult) == 0 {
		t.Fatalf("the output did not encode the result: %s", buffer.String())
	}
	allowedResult := map[string]bool{
		"stage": true, "domainsDone": true, "sourceVersion": true, "sourceLayout": true,
		"sourceIdentity": true, "destinationUrl": true, "pluginsEnabled": true,
		"clientKeys": true, "migratedCount": true, "serverFingerprint": true,
		"manualItems": true, "alreadyComplete": true, "updatedAtMs": true,
	}
	for key := range encodedResult {
		if !allowedResult[key] {
			t.Fatalf("the result carries the unplanned key %q", key)
		}
	}
	// The two names an operator could mistake for the destination secret are
	// absent even though the result is populated: the client key count is a
	// count, and the management secret has no field at all.
	for _, forbidden := range []string{"managementKey", "apiKeys", "accessToken"} {
		if _, present := encodedResult[forbidden]; present {
			t.Fatalf("the result carries %q", forbidden)
		}
	}
}

// TestCPAImportRefusalDoesNotClaimProvedClientKeys is the Low finding: the text
// output printed "client keys proved: N" from the source's key count, so a refused
// import told the operator that keys had been proved against a destination it had
// already refused. The count is only printed for a run whose proof succeeded, and
// a refused result carries zero anyway.
func TestCPAImportRefusalDoesNotClaimProvedClientKeys(t *testing.T) {
	result := upgradeservice.CPAImportResult{
		SourceVersion:  "8.0.55",
		SourceLayout:   "v8-legacy",
		DestinationURL: "http://127.0.0.1:8317",
		ClientKeys:     3,
	}
	output := cpaImportOutput{
		Result:    &result,
		Error:     "upgrade_cpa_import_target_foreign: the CPA at the destination URL does not answer",
		ErrorCode: string(upgradeservice.UpgradeErrorCPAImportTargetForeign),
	}
	buffer := &bytes.Buffer{}
	writeCPAImportOutput(buffer, false, output)
	written := buffer.String()
	if strings.Contains(written, "client keys proved") {
		t.Fatalf("a refusal claimed proved client keys:\n%s", written)
	}
	if !strings.Contains(written, "cpa import error:") {
		t.Fatalf("the refusal is not printed:\n%s", written)
	}

	// The successful run still reports the count it proved.
	buffer.Reset()
	writeCPAImportOutput(buffer, false, cpaImportOutput{Result: &result})
	if !strings.Contains(buffer.String(), "client keys proved: 3") {
		t.Fatalf("a successful run did not report the proved count:\n%s", buffer.String())
	}
}

// TestCPAImportReplayReportsTheRecordedItems is the CLI half of the Medium
// finding: a completed import used to be replayed with no source version, no source
// layout and an empty manual-item list, so the operator was told `manual items: 0`
// for a store whose operation row lists them. The replay now answers with what the
// attempt recorded, and both output shapes carry it.
func TestCPAImportReplayReportsTheRecordedItems(t *testing.T) {
	result := upgradeservice.CPAImportResult{
		Stage:          "cpa_import",
		DomainsDone:    []string{"source_copy", "cpa_config"},
		SourceVersion:  "8.0.55",
		SourceLayout:   "v8-legacy",
		SourceIdentity: "external:sha256:fixture",
		DestinationURL: "http://127.0.0.1:8317",
		PluginsEnabled: true,
		ClientKeys:     2,
		ManualItems: []upgradeservice.CPAImportManualItem{
			{Item: "section:ampcode", Reason: "reconfigure_after_adoption", Instruction: "re-create it"},
			{Item: "section:something-new", Reason: "not_in_import_scope", Instruction: "re-create it by hand"},
		},
		AlreadyComplete: true,
	}
	buffer := &bytes.Buffer{}
	writeCPAImportOutput(buffer, false, cpaImportOutput{Result: &result})
	written := buffer.String()
	for _, want := range []string{
		"source version: 8.0.55", "source layout: v8-legacy", "manual items: 2",
		"section:ampcode reconfigure_after_adoption", "section:something-new not_in_import_scope",
		"already complete: true",
	} {
		if !strings.Contains(written, want) {
			t.Fatalf("the replay output does not report %q:\n%s", want, written)
		}
	}

	buffer.Reset()
	writeCPAImportOutput(buffer, true, cpaImportOutput{Result: &result})
	var decoded struct {
		Result struct {
			SourceVersion   string                               `json:"sourceVersion"`
			SourceLayout    string                               `json:"sourceLayout"`
			PluginsEnabled  bool                                 `json:"pluginsEnabled"`
			ManualItems     []upgradeservice.CPAImportManualItem `json:"manualItems"`
			AlreadyComplete bool                                 `json:"alreadyComplete"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buffer.Bytes(), &decoded); err != nil {
		t.Fatalf("the JSON replay output is not valid JSON: %v", err)
	}
	if decoded.Result.SourceVersion != "8.0.55" || decoded.Result.SourceLayout != "v8-legacy" ||
		!decoded.Result.PluginsEnabled || !decoded.Result.AlreadyComplete {
		t.Fatalf("the JSON replay output = %+v, want the recorded source facts", decoded.Result)
	}
	if len(decoded.Result.ManualItems) != len(result.ManualItems) {
		t.Fatalf("the JSON replay output carries %d manual item(s), want %d", len(decoded.Result.ManualItems), len(result.ManualItems))
	}
	if decoded.Result.ManualItems[0] != result.ManualItems[0] {
		t.Fatalf("the JSON replay output manual item = %+v, want %+v", decoded.Result.ManualItems[0], result.ManualItems[0])
	}
}

// TestCPAImportCommandRefusesPreV8SourceWithDecisionExitCode pins the CLI
// boundary for v8-only source admission: both an explicit pre-v8 --cpa-version
// and a stack-detected pre-v8 CPA_IMAGE exit 1 with upgrade_cpa_version_unsupported
// before contacting the Runtime or mutating the recorded operation.
func TestCPAImportCommandRefusesPreV8SourceWithDecisionExitCode(t *testing.T) {
	source := copyReadyRunSource(t)
	target, bundle := reconcileRoots(t)
	if code, _, stderr := run(t, "run", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
		t.Fatalf("copy-in exit code = %d, want 0; stderr: %s", code, stderr)
	}
	if code, _, stderr := run(t, "reconcile", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
		t.Fatalf("reconcile exit code = %d, want 0; stderr: %s", code, stderr)
	}
	installDir := filepath.Dir(source)
	cpaDir := filepath.Join(installDir, "legacy-cpa")
	if err := os.MkdirAll(cpaDir, 0o700); err != nil {
		t.Fatalf("create legacy-cpa directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cpaDir, "config.yaml"),
		[]byte("config-version: 8\naccess:\n  api-keys:\n    - \"legacy-client-key-1\"\n"), 0o600); err != nil {
		t.Fatalf("write legacy-cpa config.yaml: %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "runtime.token")
	if err := os.WriteFile(tokenFile, []byte("runtime-token"), 0o600); err != nil {
		t.Fatalf("write runtime token file: %v", err)
	}
	// Point RuntimeURL at a closed local port: the pre-v8 version refusal must
	// happen before Runtime.Status or destination staging is attempted.
	t.Setenv("CPAMP_RUNTIME_URL", "http://127.0.0.1:1")
	t.Setenv("CPAMP_RUNTIME_TOKEN_FILE", tokenFile)

	t.Run("declared_7.3.4", func(t *testing.T) {
		code, stdout, _ := run(t, "cpa-import", "--json", "--legacy", source, "--cpa-dir", cpaDir,
			"--cpa-version", "7.3.4", "--plugins", "true", "--target", target, "--destination-url", "http://127.0.0.1:8317")
		if code != 1 {
			t.Fatalf("cpa-import --cpa-version 7.3.4 exited %d, want 1; stdout: %s", code, stdout)
		}
		var output cpaImportOutput
		if err := json.Unmarshal([]byte(stdout), &output); err != nil {
			t.Fatalf("decode JSON output: %v; stdout: %s", err, stdout)
		}
		if output.ErrorCode != string(upgradeservice.UpgradeErrorCPAVersionUnsupported) {
			t.Fatalf("errorCode = %q, want %q", output.ErrorCode, upgradeservice.UpgradeErrorCPAVersionUnsupported)
		}
		recorded, found, err := upgradeservice.ReadRecord(context.Background(), filepath.Join(target, upgradeservice.SourceDatabaseName))
		if err != nil || !found || recorded.Stage != model.UpgradeStageReconcile || recorded.HasDomain(model.UpgradeDomainCPAConfig) {
			t.Fatalf("recorded operation after refusal = %+v (found=%t, err=%v), want untouched reconcile", recorded, found, err)
		}
	})

	t.Run("stack_detected_v7.3.4", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(installDir, "compose.yaml"),
			[]byte("services:\n  cli-proxy-api:\n    image: ${CPA_IMAGE}\n"), 0o600); err != nil {
			t.Fatalf("write compose.yaml: %v", err)
		}
		if err := os.WriteFile(filepath.Join(installDir, ".env"),
			[]byte("CPA_IMAGE=eceasy/cli-proxy-api:v7.3.4\n"), 0o600); err != nil {
			t.Fatalf("write .env: %v", err)
		}
		code, stdout, _ := run(t, "cpa-import", "--json", "--legacy", source, "--cpa-dir", cpaDir,
			"--plugins", "true", "--target", target, "--destination-url", "http://127.0.0.1:8317")
		if code != 1 {
			t.Fatalf("cpa-import with stack v7.3.4 exited %d, want 1; stdout: %s", code, stdout)
		}
		var output cpaImportOutput
		if err := json.Unmarshal([]byte(stdout), &output); err != nil {
			t.Fatalf("decode JSON output: %v; stdout: %s", err, stdout)
		}
		if output.ErrorCode != string(upgradeservice.UpgradeErrorCPAVersionUnsupported) {
			t.Fatalf("errorCode = %q, want %q", output.ErrorCode, upgradeservice.UpgradeErrorCPAVersionUnsupported)
		}
		recorded, found, err := upgradeservice.ReadRecord(context.Background(), filepath.Join(target, upgradeservice.SourceDatabaseName))
		if err != nil || !found || recorded.Stage != model.UpgradeStageReconcile || recorded.HasDomain(model.UpgradeDomainCPAConfig) {
			t.Fatalf("recorded operation after refusal = %+v (found=%t, err=%v), want untouched reconcile", recorded, found, err)
		}
	})
}
