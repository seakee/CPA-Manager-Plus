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

func TestCPAImportOutputNeverCarriesSecrets(t *testing.T) {
	output := cpaImportOutput{Error: "refused", ErrorCode: "upgrade_cpa_version_unsupported"}
	buffer := &bytes.Buffer{}
	writeCPAImportOutput(buffer, true, output)
	var decoded map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &decoded); err != nil {
		t.Fatalf("the JSON output is not valid JSON: %v", err)
	}
	for _, forbidden := range []string{"managementKey", "apiKeys", "clientKeys", "accessToken"} {
		if _, present := decoded[forbidden]; present {
			t.Fatalf("the output carries %q", forbidden)
		}
	}
}
