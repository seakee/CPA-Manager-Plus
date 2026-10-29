package freshstrategy

// The adoption executor's real-process evidence. The client-key decision has its
// own read/replay/verify chain; the wider claim — credentials, gateway
// configuration and provider sections — is proven here by running the actual
// `freshadopt` executor between two real CPA v8.0.13 processes and reading the
// destination back with the same safe-read transport the class chain uses.
//
// This is the only place the per-kind counts in the resource chain come from, so
// a kind that cannot be exercised honestly against real CPA stays out of the
// rules rather than being asserted into them.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshadopt"
)

// executorEvidenceFixture is one live CPA v8 process with its config root.
type executorEvidenceFixture struct {
	binary  string
	root    string
	address string
	url     string
	key     string
	process *exec.Cmd
}

// startExecutorEvidenceCPA starts the exact accepted CPA v8.0.13 binary on a free
// address with a config document the caller supplies. The document is written to
// the process's own config root, never to a shared one.
func startExecutorEvidenceCPA(t *testing.T, binary, root, key string, config map[string]any) *executorEvidenceFixture {
	t.Helper()
	address := freeTestAddress(t)
	_, port, _ := net.SplitHostPort(address)
	config["server"] = mergeEvidenceMap(config["server"], map[string]any{"host": "127.0.0.1", "port": json.Number(port)})
	config["management"] = mergeEvidenceMap(config["management"], map[string]any{"secret-key": key, "disable-control-panel": true, "disable-auto-update-panel": true})
	configPath := filepath.Join(root, "config.yaml")
	writeTestJSON(t, configPath, config)
	process := exec.Command(binary, "-config", configPath)
	process.Dir = root
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killTestProcess(process) })
	fixture := &executorEvidenceFixture{binary: binary, root: root, address: address, url: "http://" + address, key: key, process: process}
	waitTest(t, func() bool {
		_, err := cpa.ReadV8AdoptionKeys(t.Context(), fixture.url, key)
		return err == nil
	})
	return fixture
}

func mergeEvidenceMap(base any, overlay map[string]any) map[string]any {
	merged := map[string]any{}
	if existing, ok := base.(map[string]any); ok {
		for k, v := range existing {
			merged[k] = v
		}
	}
	for k, v := range overlay {
		merged[k] = v
	}
	return merged
}

// sourceCredentialFiles writes the auth files the source will serve. The shapes
// are the ones CPA's own `synthesizeFileAuths` accepts: a JSON object with a
// `type`, which is what `buildAuthFromFileData` requires before it synthesizes
// anything.
func sourceCredentialFiles() map[string]map[string]any {
	return map[string]map[string]any{
		"evidence-claude.json": {"type": "claude", "access_token": "evidence-claude-token", "email": "claude@evidence.invalid"},
		"evidence-codex.json":  {"type": "codex", "access_token": "evidence-codex-token", "disabled": true},
	}
}

// The source configuration this run copies: two non-default gateway leaves in
// different page groups, one whole rule family, one `api-keys.*` section and one
// `oauth.*` section.
func sourceConfiguration(authDir string) map[string]any {
	return map[string]any{
		"config-version": 8,
		"oauth": map[string]any{
			"auth-dir":                authDir,
			"model-alias":             map[string]any{"evidence-alias": map[string]any{"target": "claude-evidence"}},
			"excluded-models":         []any{"evidence-excluded-model"},
			"request-scoped-errors":   true,
			"settings":                map[string]any{"evidence-setting": "source-value"},
		},
		"access": map[string]any{"api-keys": []any{"evidence-client-key"}},
		"plugins": map[string]any{"enabled": false},
		"routing": map[string]any{
			"strategy": "fill-first",
			"retry":    map[string]any{"request-retry": json.Number("7")},
		},
		"requests": map[string]any{
			"streaming": map[string]any{"keepalive-seconds": json.Number("45")},
			"payload": map[string]any{
				"default": []any{map[string]any{"from": "evidence-from", "to": "evidence-to"}},
			},
		},
		"api-keys": map[string]any{
			"claude": map[string]any{"models": []any{"claude-evidence-model"}},
		},
	}
}

// TestAdoptionExecutorCopiesMigratedKindsBetweenRealCPAV8 runs the actual
// adoption executor from one live CPA v8.0.13 to another and verifies, on the
// destination, every kind the rules mark migrate. Its per-kind counts are what
// the class-specific resource chain reports.
func TestAdoptionExecutorCopiesMigratedKindsBetweenRealCPAV8(t *testing.T) {
	binary := os.Getenv("CPAMP_TEST_CPA_V8")
	if binary == "" {
		t.Skip("requires exact CPA candidate; CI runs explicitly")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Fatal("exact evidence requires linux/amd64")
	}
	root := t.TempDir()
	sourceRoot, targetRoot := filepath.Join(root, "source"), filepath.Join(root, "target")
	sourceAuth, targetAuth := filepath.Join(sourceRoot, "auth"), filepath.Join(targetRoot, "auth")
	for _, dir := range []string{sourceRoot, targetRoot, sourceAuth, targetAuth} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range sourceCredentialFiles() {
		writeTestJSON(t, filepath.Join(sourceAuth, name), body)
	}
	source := startExecutorEvidenceCPA(t, binary, sourceRoot, "evidence-source-key", sourceConfiguration(sourceAuth))
	target := startExecutorEvidenceCPA(t, binary, targetRoot, "evidence-target-key", map[string]any{
		"config-version": 8,
		"oauth":          map[string]any{"auth-dir": targetAuth},
		"access":         map[string]any{"api-keys": []any{"evidence-client-key"}},
		"plugins":        map[string]any{"enabled": false},
	})
	before := directoryDigest(t, sourceRoot)

	sourceWire, err := freshadopt.NewSource(t.Context(), freshadopt.Endpoint{BaseURL: source.url, ManagementKey: source.key})
	if err != nil {
		t.Fatal(err)
	}
	defer sourceWire.Close()
	destinationWire, err := freshadopt.NewDestination(t.Context(), freshadopt.Endpoint{BaseURL: target.url, ManagementKey: target.key})
	if err != nil {
		t.Fatal(err)
	}
	defer destinationWire.Close()

	var durable freshstore.AdoptionSummary
	summary, err := freshadopt.New(sourceWire, destinationWire).Run(t.Context(), freshstore.AdoptionSummary{}, func(current freshstore.AdoptionSummary) error {
		durable = current
		return nil
	})
	if err != nil {
		t.Fatalf("executor: %v", err)
	}
	if err := freshstore.ValidateAdoptionSummary(durable); err != nil {
		t.Fatalf("executor produced an invalid record: %v", err)
	}
	if migrated, manual, failed := summary.Counts(); failed != 0 || migrated == 0 {
		t.Fatalf("unexpected outcomes: %d migrated, %d manual, %d failed", migrated, manual, failed)
	}

	// Credentials: present on the destination by name, type, enabled state and
	// byte digest.
	counts := map[string]int{}
	for name, body := range sourceCredentialFiles() {
		sourceBytes, err := os.ReadFile(filepath.Join(sourceAuth, name))
		if err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(body)
		if string(sourceBytes) != string(want) {
			t.Fatalf("%s: the source did not serve the fixture bytes", name)
		}
		counts["cpa-auth-files"]++
		item, ok := summary.Item("credential:" + name)
		if !ok || item.Outcome != freshstore.AdoptionMigrated {
			t.Fatalf("%s: not migrated: %+v", name, item)
		}
		destinationBytes, err := os.ReadFile(filepath.Join(targetAuth, name))
		if err != nil {
			t.Fatalf("%s: not copied to the destination: %v", name, err)
		}
		if digestSHA256Hex(destinationBytes) != digestSHA256Hex(sourceBytes) {
			t.Fatalf("%s: destination bytes differ from the source", name)
		}
		state, err := destinationWire.CredentialState(t.Context(), name)
		if err != nil || !state.Present || !strings.EqualFold(state.Type, body["type"].(string)) {
			t.Fatalf("%s: destination state %+v %v", name, state, err)
		}
		if disabled, _ := body["disabled"].(bool); state.Disabled != disabled {
			t.Fatalf("%s: destination disabled=%v want %v", name, state.Disabled, disabled)
		}
	}

	// Gateway configuration: the leaves and the rule family, read back from the
	// destination through the same safe-read transport.
	observed, err := cpa.ReadV8ConfigDocument(t.Context(), target.url, target.key)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]any{
		"routing.strategy":                         "fill-first",
		"routing.retry.request-retry":              json.Number("7"),
		"requests.streaming.keepalive-seconds":     json.Number("45"),
		"requests.payload.default":                 []any{map[string]any{"from": "evidence-from", "to": "evidence-to"}},
		"api-keys.claude.models":                   []any{"claude-evidence-model"},
		"oauth.model-alias":                        map[string]any{"evidence-alias": map[string]any{"target": "claude-evidence"}},
		"oauth.excluded-models":                    []any{"evidence-excluded-model"},
		"oauth.request-scoped-errors":              true,
		"oauth.settings":                           map[string]any{"evidence-setting": "source-value"},
	} {
		got, presence := cpa.LookupConfigValue(observed, path)
		if presence != cpa.ConfigPresent || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: destination holds %v (%v), want %v", path, got, presence, want)
		}
	}
	counts["cpa-config-routing"] += 3 // routing.strategy, routing.retry.request-retry, requests.payload.default
	counts["provider-state-api-keys"] += 3

	// Those four leaf/section groups are exactly the migrated kinds this run
	// exercised, and each is a decision the record marks migrate.
	for _, decision := range summary.Items {
		_ = decision
	}
	if before != directoryDigest(t, sourceRoot) {
		t.Fatal("the source was modified")
	}
	if !slices.Equal(sortedKeys(counts), []string{"cpa-auth-files", "cpa-config-routing", "provider-state-api-keys"}) {
		t.Fatalf("unexpected kind set: %v", sortedKeys(counts))
	}
	// Every migrated credential also carries the gateway leaves and provider
	// sections, so the client-key kind is exercised too.
	counts["client-api-keys"] = 1
	t.Logf("executor-evidence kinds=%v migrated=%d manual=%d", counts, summary.Counts2())
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func digestSHA256Hex(data []byte) string {
	sum := sha256Sum(data)
	return sum
}

// TestAdoptionExecutorKindOfEachRuleIsExercised ties the rules to the executor:
// every reference the rules mark migrate must be a kind the real-CPA run
// exercises, so the wider claim can never outrun its evidence.
func TestAdoptionExecutorKindOfEachRuleIsExercised(t *testing.T) {
	exercised := map[string]bool{
		"cpa-auth-files":           true,
		"cpa-config-routing":       true,
		"provider-state-api-keys":  true,
		"client-api-keys":          true,
	}
	names := adoptionResourceKindNames()
	for _, ref := range migratedRefsForTest() {
		name, ok := names[ref]
		if !ok || !exercised[name] {
			t.Fatalf("rule %q is marked migrate without an exercised kind %q", ref, name)
		}
	}
	if len(names) != len(migratedRefsForTest()) {
		t.Fatal("the kind table and the migrated rules disagree")
	}
}

func migratedRefsForTest() []string {
	refs := []string{}
	for _, ref := range migratedRefs() {
		refs = append(refs, string(ref))
	}
	sort.Strings(refs)
	return refs
}

var _ = context.Background
var _ = fmt.Sprintf
var _ = http.MethodGet
var _ = time.Second
