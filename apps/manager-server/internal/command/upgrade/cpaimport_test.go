package upgrade

// These tests pin the `upgrade cpa-import` command's contract at the boundary an
// operator sees: the flags it requires, the exit codes it shares with the other
// steps, and the fact that it reads and writes nothing before the service layer's
// own refusals.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
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
