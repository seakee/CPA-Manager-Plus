package runtimeconfig

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRemovesOnlyLegacyConnectionFields(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "legacy.json")
	outputPath := filepath.Join(dir, "runtime", "config.json")
	input := `{
  "httpAddr": "0.0.0.0:18317",
  "unknownObject": {"enabled": true, "values": [1, 2, 3]},
  "cpaUpstreamUrl": "http://127.0.0.1:8317",
  "managementKeyFile": "../../secrets/cpa-management-key"
}`
	if err := os.WriteFile(inputPath, []byte(input), 0o640); err != nil {
		t.Fatalf("write input: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run([]string{"--input", inputPath, "--output", outputPath}, &stdout, &stderr); err != nil {
		t.Fatalf("run sanitizer: %v stderr=%s", err, stderr.String())
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("output is invalid JSON: %v\n%s", err, data)
	}
	if _, ok := fields["cpaUpstreamUrl"]; ok {
		t.Fatal("cpaUpstreamUrl was retained")
	}
	if _, ok := fields["managementKeyFile"]; ok {
		t.Fatal("managementKeyFile was retained")
	}
	if string(fields["httpAddr"]) != `"0.0.0.0:18317"` {
		t.Fatalf("httpAddr=%s", fields["httpAddr"])
	}
	var unknown map[string]any
	if err := json.Unmarshal(fields["unknownObject"], &unknown); err != nil || unknown["enabled"] != true {
		t.Fatalf("unknownObject=%v err=%v", unknown, err)
	}
	if info, err := os.Stat(outputPath); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("output mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestRunRejectsInvalidJSONWithoutReplacingOutput(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "legacy.json")
	outputPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(inputPath, []byte(`{"httpAddr":"value",}`), 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}
	if err := os.WriteFile(outputPath, []byte("existing-output\n"), 0o600); err != nil {
		t.Fatalf("write output: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run([]string{"--input", inputPath, "--output", outputPath}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "decode runtime config") {
		t.Fatalf("err=%v", err)
	}
	data, readErr := os.ReadFile(outputPath)
	if readErr != nil || string(data) != "existing-output\n" {
		t.Fatalf("output=%q err=%v", data, readErr)
	}
}

func TestRunRejectsTrailingJSONValue(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "legacy.json")
	outputPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(inputPath, []byte(`{"httpAddr":"value"} {"extra":true}`), 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run([]string{"--input", inputPath, "--output", outputPath}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("output unexpectedly exists: %v", err)
	}
}
