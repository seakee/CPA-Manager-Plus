package upgrade

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

const cliManagementKey = "management-key"

// externalCPAServer is a synthetic External CPA serving the v8 management reads
// the connect step makes, the version header it reads, and the gateway model
// list. rejectKey answers 401 to every authenticated read.
type externalCPAServer struct {
	*httptest.Server
	version string
	// configDocument is what /v8/management/config answers. It defaults to a v8
	// document; a translated target answers a v7 one while still advertising v8.
	configDocument string
	rejectKey      bool
	requests       []string
}

// v8ConfigDocument is a v8 configuration document: the generation a v8 CPA
// declares, a bounded port, and the client-key list. The auth directory and the
// list itself are optional in a real document; this one carries both so the
// server's other answers stay consistent with it.
const v8ConfigDocument = `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/auth"},` +
	`"access":{"api-keys":["client-key-1"]}}`

func newExternalCPAServer(t *testing.T, version string) *externalCPAServer {
	t.Helper()
	server := &externalCPAServer{version: version, configDocument: v8ConfigDocument}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.requests = append(server.requests, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-test"}]}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+cliManagementKey || server.rejectKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v0/management/config":
			if server.version != "" {
				w.Header().Set("X-CPA-Version", server.version)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case "/v8/management/config":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(server.configDocument))
		case "/v8/management/config/access/api-keys":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`["client-key-1"]`))
		case "/v8/management/config.yaml":
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write([]byte("port: 8317\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// copiedTarget copies a stopped 1.x source into a v2 data root and returns that
// root: the state the External connect step runs from.
func copiedTarget(t *testing.T) string {
	t.Helper()
	source := copyReadySource(t)
	target := t.TempDir()
	if code, _, stderr := run(t, "run", "--legacy", source, "--target", target,
		"--bundle", filepath.Join(t.TempDir(), "backup")); code != 0 {
		t.Fatalf("copy in: exit %d; stderr: %s", code, stderr)
	}
	return target
}

func managementKeyFile(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "management-key")
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		t.Fatalf("write management key: %v", err)
	}
	return path
}

// TestExternalConnectCLIConnectsAV8CPA drives the real observation path against a
// synthetic CPA: the version probe, the authenticated key read and the model
// surface are the production ones.
func TestExternalConnectCLIConnectsAV8CPA(t *testing.T) {
	target := copiedTarget(t)
	server := newExternalCPAServer(t, "v8.0.13")

	code, stdout, stderr := run(t, "external-connect", "--json",
		"--target", target, "--cpa-base-url", server.URL,
		"--management-key-file", managementKeyFile(t, cliManagementKey))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var output externalConnectOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode output: %v; output: %s", err, stdout)
	}
	if output.Result == nil || output.Result.Stage != "external_connect" {
		t.Fatalf("result = %+v", output.Result)
	}
	if !strings.HasPrefix(output.Result.Identity, "external:sha256:") ||
		output.Result.Identity != output.Result.Generation {
		t.Fatalf("identity/generation = %q/%q", output.Result.Identity, output.Result.Generation)
	}
	if output.Result.CPAVersion != "v8.0.13" || output.Error != "" {
		t.Fatalf("result = %+v, error %q", output.Result, output.Error)
	}
	// The gateway was asked for its model list, which is what proves the surface
	// behind the management endpoint.
	for _, request := range server.requests {
		if request == "GET /v1/models" {
			return
		}
	}
	t.Fatalf("the connect step never proved the model surface: %v", server.requests)
}

// TestExternalConnectCLIExitsOneOnAV7CPA proves the stop is the CLI's refusal
// contract with the typed code, and that nothing advanced.
func TestExternalConnectCLIExitsOneOnAV7CPA(t *testing.T) {
	target := copiedTarget(t)
	server := newExternalCPAServer(t, "v7.2.1")
	keyFile := managementKeyFile(t, cliManagementKey)

	code, stdout, stderr := run(t, "external-connect", "--json",
		"--target", target, "--cpa-base-url", server.URL, "--management-key-file", keyFile)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, stderr)
	}
	var output externalConnectOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode output: %v; output: %s", err, stdout)
	}
	if output.ErrorCode != string(upgradeservice.UpgradeErrorExternalVersionUnsupported) {
		t.Fatalf("error code = %q", output.ErrorCode)
	}
	if !strings.Contains(output.Error, "v7.2.1") || !strings.Contains(output.Error, "upgrade the CPA") {
		t.Fatalf("refusal = %q", output.Error)
	}
	// The stop is before any write to the CPA: only the version read happened.
	for _, request := range server.requests {
		if strings.HasPrefix(request, "PUT ") || strings.HasPrefix(request, "POST ") {
			t.Fatalf("a v7 stop wrote to the CPA: %v", server.requests)
		}
	}

	code, statusJSON, stderr := run(t, "status", "--json", "--db-path", filepath.Join(target, "usage.sqlite"))
	if code != 0 {
		t.Fatalf("status exit code = %d; stderr: %s", code, stderr)
	}
	var status statusOutput
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Operation == nil || status.Operation.Stage != "copy_in" {
		t.Fatalf("the v7 stop advanced the row: %+v", status.Operation)
	}
	if status.Operation.ErrorCode != string(upgradeservice.UpgradeErrorExternalVersionUnsupported) {
		t.Fatalf("recorded code = %q", status.Operation.ErrorCode)
	}
}

// TestExternalConnectCLIRejectedKeyIsNotAVersionProblem proves the CLI reports a
// 401 as the management-key code and exits with the refusal contract.
func TestExternalConnectCLIRejectedKeyIsNotAVersionProblem(t *testing.T) {
	target := copiedTarget(t)
	server := newExternalCPAServer(t, "v8.0.13")
	server.rejectKey = true

	code, stdout, _ := run(t, "external-connect", "--json",
		"--target", target, "--cpa-base-url", server.URL,
		"--management-key-file", managementKeyFile(t, cliManagementKey))
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var output externalConnectOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode output: %v; output: %s", err, stdout)
	}
	if output.ErrorCode != string(upgradeservice.UpgradeErrorExternalManagementRejected) {
		t.Fatalf("error code = %q", output.ErrorCode)
	}
	if strings.Contains(output.ErrorCode, "version") {
		t.Fatalf("a rejected key was reported as a version problem: %q", output.ErrorCode)
	}
}

// TestExternalConnectCLIRefusesATranslatedV7Document drives the strict document
// read through the real path: a target that advertises v8 and answers /v8 with a
// v7 configuration is not connected, because the document this Manager would
// write into is the contract that has to accept the write.
func TestExternalConnectCLIRefusesATranslatedV7Document(t *testing.T) {
	target := copiedTarget(t)
	server := newExternalCPAServer(t, "v8.0.13")
	server.configDocument = `{"config-version":7,"server":{"port":8317},"oauth":{"auth-dir":"/auth"},` +
		`"access":{"api-keys":["client-key-1"]}}`

	code, stdout, stderr := run(t, "external-connect", "--json",
		"--target", target, "--cpa-base-url", server.URL,
		"--management-key-file", managementKeyFile(t, cliManagementKey))
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, stderr)
	}
	var output externalConnectOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode output: %v; output: %s", err, stdout)
	}
	if output.ErrorCode != string(upgradeservice.UpgradeErrorExternalVersionUnsupported) {
		t.Fatalf("error code = %q", output.ErrorCode)
	}
	if !strings.Contains(output.Error, "upgrade the CPA") {
		t.Fatalf("refusal does not name the action: %q", output.Error)
	}
	// A refused document is not written to: no key write reached the CPA.
	for _, request := range server.requests {
		if strings.HasPrefix(request, "PUT ") || strings.HasPrefix(request, "POST ") {
			t.Fatalf("a refused document still wrote to the CPA: %v", server.requests)
		}
	}
	code, statusJSON, stderr := run(t, "status", "--json", "--db-path", filepath.Join(target, "usage.sqlite"))
	if code != 0 {
		t.Fatalf("status exit code = %d; stderr: %s", code, stderr)
	}
	var status statusOutput
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Operation == nil || status.Operation.Stage != "copy_in" {
		t.Fatalf("a refused document advanced the row: %+v", status.Operation)
	}
}

// TestExternalConnectCLIRequiresItsInputs pins the usage contract.
func TestExternalConnectCLIRequiresItsInputs(t *testing.T) {
	for name, args := range map[string][]string{
		"no base url":    {"external-connect", "--management-key-file", "key"},
		"no key file":    {"external-connect", "--cpa-base-url", "https://cpa.example"},
		"positional arg": {"external-connect", "--cpa-base-url", "https://cpa.example", "--management-key-file", "key", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, _ := run(t, args...); code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
		})
	}
}

// TestExitCodePinsTheExternalRefusals adds the External stop to the exit-code
// table: a decision about the user's CPA exits 1, an unreachable one exits 2.
func TestExitCodePinsTheExternalRefusals(t *testing.T) {
	typed := func(code upgradeservice.UpgradeErrorCode) error {
		return &upgradeservice.UpgradeError{Code: code, Message: "pinned by the exit-code table"}
	}
	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "v7 cpa", err: typed(upgradeservice.UpgradeErrorExternalVersionUnsupported), want: 1},
		{name: "v7 cpa (sentinel)", err: upgradeservice.ErrExternalVersionUnsupported, want: 1},
		{
			// The CPA answered with something that is not the v8 document: still a
			// decision the owner has to look at, never a transient failure.
			name: "a document that is not the v8 contract",
			err:  upgradeservice.ErrExternalConfigDocumentInvalid,
			want: 1,
		},
		{name: "rejected key", err: upgradeservice.ErrExternalManagementRejected, want: 1},
		{name: "moved target", err: upgradeservice.ErrExternalTargetMoved, want: 1},
		{name: "unreachable cpa", err: typed(upgradeservice.UpgradeErrorExternalAuthorityUnavailable), want: 2},
		{name: "wrong stage", err: typed(upgradeservice.UpgradeErrorExternalNotApplicable), want: 2},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if code := ExitCode(testCase.err); code != testCase.want {
				t.Fatalf("exit code = %d, want %d", code, testCase.want)
			}
		})
	}
}
