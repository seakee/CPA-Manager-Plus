package upgrade

// These tests pin the source half of the Managed CPA import: the version gate,
// the derived path mapping, the refusal to guess, and the manual items. None of
// them writes: the adapter only ever reads a directory.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCPAFixture writes a stopped 1.x stack: a Manager data directory, the
// managed CPA directory beside it, and (optionally) an auth directory.
func writeCPAFixture(t *testing.T, config string, credentials map[string]string) (legacyRoot, cpaDirectory string) {
	t.Helper()
	root := canonicalTempDir(t)
	legacyRoot = filepath.Join(root, "data")
	if err := os.MkdirAll(legacyRoot, 0o755); err != nil {
		t.Fatalf("create fixture data root: %v", err)
	}
	return legacyRoot, writeCPABeside(t, legacyRoot, config, credentials)
}

// writeCPABeside writes the managed CPA directory next to an existing 1.x data
// directory, which is where the v1 stack keeps it.
func writeCPABeside(t *testing.T, legacyRoot, config string, credentials map[string]string) string {
	t.Helper()
	cpaDirectory := filepath.Join(filepath.Dir(legacyRoot), cpaDirectoryName())
	if err := os.MkdirAll(cpaDirectory, 0o755); err != nil {
		t.Fatalf("create fixture CPA directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cpaDirectory, cpaConfigFileName), []byte(config), 0o600); err != nil {
		t.Fatalf("write fixture CPA configuration: %v", err)
	}
	if len(credentials) > 0 {
		authDir := filepath.Join(cpaDirectory, cpaAuthDirectoryName)
		if err := os.MkdirAll(authDir, 0o700); err != nil {
			t.Fatalf("create fixture auth directory: %v", err)
		}
		for name, body := range credentials {
			if err := os.WriteFile(filepath.Join(authDir, name), []byte(body), 0o600); err != nil {
				t.Fatalf("write fixture credential %s: %v", name, err)
			}
		}
	}
	return cpaDirectory
}

// mustJSON renders a document for a substring assertion.
func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode the mapped document: %v", err)
	}
	return string(encoded)
}

// cpaDirectoryName mirrors the sibling directory name the adapter resolves.
func cpaDirectoryName() string { return cpaSiblingDirectoryName }

// v8FixtureConfig is a canonical CPA 8.0.55 configuration: the v8 schema paths
// this import copies, excluded Manager-owned paths (`server`, `management`,
// `plugins`, `oauth.auth-dir`, `access.api-keys`), provider key groups, one
// removed section (`ampcode`), and one out-of-scope section (`something-new`).
const v8FixtureConfig = `config-version: 8
server:
  host: "127.0.0.1"
  port: 8317
management:
  allow-remote: true
  secret-key: "legacy-management-secret-that-the-destination-must-not-inherit"
plugins:
  enabled: true
oauth:
  auth-dir: "/root/.cli-proxy-api"
  model-alias:
    claude:
      - name: "claude-sonnet-4"
        alias: "sonnet"
access:
  api-keys:
    - "legacy-client-key-1"
    - "legacy-client-key-2"
routing:
  retry:
    request-retry: 3
    max-retry-credentials: 5
  cooldown:
    disable-cooling: true
requests:
  proxy-url: "http://127.0.0.1:7890"
  payload:
    default:
      - models:
          - name: "gpt-5"
        params:
          temperature: 0.2
upstream:
  codex:
    disable-codex-cloaking: true
api-keys:
  gemini:
    - name: "gemini-1"
      base-url: "https://generativelanguage.googleapis.com"
      keys:
        - api-key: "gemini-provider-key"
  claude:
    - name: "claude-1"
      keys:
        - api-key: "claude-provider-key"
      models:
        - name: "claude-sonnet-4"
ampcode:
  upstream-url: "https://amp.example"
something-new:
  enabled: true
`

// v7LayoutFixtureConfig exercises the legacy unversioned `v7` YAML grammar that
// pinned CPA 8.0.55 still loads (`internal/config/config_v8.go`): it describes
// file grammar on a CPA 8.0.55 runtime, not a supported CPA 7 runtime.
const v7LayoutFixtureConfig = `host: "127.0.0.1"
port: 8317
auth-dir: "/root/.cli-proxy-api"
remote-management:
  allow-remote: true
  secret-key: "legacy-management-secret-that-the-destination-must-not-inherit"
api-keys:
  - "legacy-client-key-1"
  - "legacy-client-key-2"
request-retry: 3
max-retry-credentials: 5
disable-cooling: true
proxy-url: "http://127.0.0.1:7890"
payload:
  default:
    - models:
        - name: "gpt-5"
      params:
        temperature: 0.2
codex:
  identity-confuse: true
  disable-codex-cloaking: true
ampcode:
  upstream-url: "https://amp.example"
gemini-api-key:
  - api-key: "gemini-provider-key"
    base-url: "https://generativelanguage.googleapis.com"
claude-api-key:
  - api-key: "claude-provider-key"
    models:
      - name: "claude-sonnet-4"
oauth-model-alias:
  claude:
    - name: "claude-sonnet-4"
      alias: "sonnet"
something-new:
  enabled: true
`

// v8Source opens the canonical CPA 8.0.55 fixture with pinned release 8.0.55.
func v8Source(t *testing.T) *cpaManagedSource {
	t.Helper()
	legacyRoot, _ := writeCPAFixture(t, v8FixtureConfig, map[string]string{
		"codex-user.json":  `{"type":"codex","access_token":"token"}`,
		"gemini-user.json": `{"type":"gemini","access_token":"token"}`,
	})
	source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "8.0.55"})
	if err != nil {
		t.Fatalf("open the managed CPA 8.0.55 source: %v", err)
	}
	return source
}

// v8LegacyGrammarSource opens a CPA 8.0.55 source whose config.yaml still uses
// the unversioned `v7` YAML grammar that CPA 8.0.55 loads.
func v8LegacyGrammarSource(t *testing.T) *cpaManagedSource {
	t.Helper()
	legacyRoot, _ := writeCPAFixture(t, v7LayoutFixtureConfig, map[string]string{
		"codex-user.json":  `{"type":"codex","access_token":"token"}`,
		"gemini-user.json": `{"type":"gemini","access_token":"token"}`,
	})
	source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "8.0.55"})
	if err != nil {
		t.Fatalf("open the managed CPA 8.0.55 legacy-grammar source: %v", err)
	}
	return source
}

// TestCPASourceMapsTheV7SpellingsOntoTheV8Paths is the mapping table's own test:
// every mapped legacy-grammar path on a CPA 8.0.55 source lands where the
// destination accepts it and reads back.
func TestCPASourceMapsTheV7SpellingsOntoTheV8Paths(t *testing.T) {
	source := v8LegacyGrammarSource(t)
	for _, testCase := range []struct {
		path string
		want any
	}{
		{path: "routing.retry.request-retry", want: 3},
		{path: "routing.retry.max-retry-credentials", want: 5},
		{path: "routing.cooldown.disable-cooling", want: true},
		{path: "requests.proxy-url", want: "http://127.0.0.1:7890"},
		{path: "upstream.codex.disable-codex-cloaking", want: true},
	} {
		value, presence := lookupDocumentPath(source.document, testCase.path)
		if presence != documentPresent {
			t.Fatalf("%s is absent from the mapped document", testCase.path)
		}
		if value != testCase.want {
			t.Fatalf("%s = %#v, want %#v", testCase.path, value, testCase.want)
		}
	}
	// The eight api-keys families are grouped exactly as CPA groups them: one
	// group per legacy entry, named after the provider.
	for _, provider := range []string{"gemini", "claude"} {
		value, presence := lookupDocumentPath(source.document, "api-keys."+provider)
		if presence != documentPresent {
			t.Fatalf("api-keys.%s was not mapped", provider)
		}
		groups, ok := value.([]any)
		if !ok || len(groups) != 1 {
			t.Fatalf("api-keys.%s = %#v, want one group", provider, value)
		}
		group, ok := groups[0].(map[string]any)
		if !ok || group["name"] != provider+"-1" {
			t.Fatalf("api-keys.%s group = %#v, want name %s-1", provider, groups[0], provider)
		}
		keys, ok := group["keys"].([]any)
		if !ok || len(keys) != 1 {
			t.Fatalf("api-keys.%s group keys = %#v, want one key", provider, group["keys"])
		}
	}
	// The rule family keeps its whole value.
	family, presence := lookupDocumentPath(source.document, "requests.payload.default")
	if presence != documentPresent {
		t.Fatal("the payload default family was not mapped")
	}
	if _, ok := family.([]any); !ok {
		t.Fatalf("requests.payload.default = %#v, want the source list", family)
	}
}

// TestCPASourceNeverCopiesTheManagersOwnPaths pins the exclusions: the
// destination keeps its own management secret, listener and client key list.
func TestCPASourceNeverCopiesTheManagersOwnPaths(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		source *cpaManagedSource
	}{
		{name: "canonical v8.0.55 schema", source: v8Source(t)},
		{name: "legacy grammar on CPA 8.0.55", source: v8LegacyGrammarSource(t)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			source := testCase.source
			for _, path := range []string{
				"management", "remote-management", "server", "host", "port",
				"access.api-keys", "plugins", "auth-dir", "oauth.auth-dir",
			} {
				if value, presence := lookupDocumentPath(source.document, path); presence == documentPresent {
					t.Fatalf("%s was copied into the destination document as %#v", path, value)
				}
			}
			for _, item := range source.manual {
				if item.item == "config:oauth.auth-dir" || item.item == "config:auth-dir" {
					t.Fatalf("excluded auth-dir was reported as manual work: %+v", item)
				}
			}
			// The source's client key list is a list of strings; the destination document
			// carries `api-keys.<provider>` groups instead, never that list.
			if value, presence := lookupDocumentPath(source.document, "api-keys"); presence != documentPresent {
				t.Fatal("the provider group section was not mapped")
			} else if _, isList := value.([]any); isList {
				t.Fatalf("the source client key list was copied as api-keys: %#v", value)
			}
			if strings.Contains(mustJSON(t, source.document), "legacy-client-key-1") {
				t.Fatal("the source client key list reached the destination document")
			}
			if strings.Contains(source.sourceIdentity(), "legacy-management-secret") {
				t.Fatal("the source identity carries a secret")
			}
		})
	}
}

// TestCPASourceReportsGeminiAmpcodeAndIdentityConfuseAsManualItems pins the three
// source shapes CPA v8 drops when a CPA 8.0.55 source still carries legacy keys:
// each one is reported, and none of them fails the read.
func TestCPASourceReportsGeminiAmpcodeAndIdentityConfuseAsManualItems(t *testing.T) {
	source := v8LegacyGrammarSource(t)
	items := map[string]cpaManualItem{}
	for _, item := range source.manual {
		items[item.item] = item
	}
	gemini, ok := items["credential:gemini-user.json"]
	if !ok || gemini.reason != "gemini_credential_unsupported" {
		t.Fatalf("gemini credential manual item = %+v, present = %t", gemini, ok)
	}
	ampcode, ok := items["section:ampcode"]
	if !ok || ampcode.reason != "removed_upstream" {
		t.Fatalf("ampcode manual item = %+v, present = %t", ampcode, ok)
	}
	identity, ok := items["config:codex.identity-confuse"]
	if !ok || identity.reason != "removed_upstream" {
		t.Fatalf("identity-confuse manual item = %+v, present = %t", identity, ok)
	}
	if !strings.Contains(identity.instruction, "v8.0.4") {
		t.Fatalf("identity-confuse instruction does not name the release that removed it: %q", identity.instruction)
	}
	if !strings.Contains(ampcode.instruction, "v7.2.0") {
		t.Fatalf("ampcode instruction does not name the release that removed it: %q", ampcode.instruction)
	}
	// The gemini file is excluded from the uploadable inventory, the codex one
	// is not.
	if len(source.credentials) != 1 || source.credentials[0].Name != "codex-user.json" {
		t.Fatalf("credentials = %+v, want only the codex file", source.credentials)
	}
}

// TestCPASourceReportsAnUnknownSectionInsteadOfDroppingIt pins the refusal to
// guess: a section the mapping does not name becomes a manual item.
func TestCPASourceReportsAnUnknownSectionInsteadOfDroppingIt(t *testing.T) {
	source := v8Source(t)
	found := false
	for _, item := range source.manual {
		if item.item != "section:something-new" {
			continue
		}
		found = true
		if item.reason != "not_in_import_scope" {
			t.Fatalf("unknown section reason = %q, want not_in_import_scope", item.reason)
		}
		if !strings.Contains(item.instruction, "something-new") {
			t.Fatalf("unknown section instruction does not name the section: %q", item.instruction)
		}
	}
	if !found {
		t.Fatalf("the unknown section was dropped; manual items = %+v", source.manual)
	}
}

// TestCPASourceReportsAnUnknownLeafInsideAMappedSection pins the same rule one
// level down, where a silently dropped key is easiest to miss.
func TestCPASourceReportsAnUnknownLeafInsideAMappedSection(t *testing.T) {
	legacyRoot, _ := writeCPAFixture(t, "api-keys:\n  - \"legacy-client-key-1\"\ncodex:\n  identity-confuse: true\n  an-unknown-setting: 7\n", nil)
	source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "8.0.55"})
	if err != nil {
		t.Fatalf("open the managed CPA source: %v", err)
	}
	found := false
	for _, item := range source.manual {
		if item.item == "config:codex.an-unknown-setting" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the unknown leaf was dropped; manual items = %+v", source.manual)
	}
}

// TestCPASourceVersionGateRefusesBelowTheFloor is the preflight block: every
// CPA release below 8.0.0 (including 7.3.4 and 8.0.0 pre-releases) and outside
// major 8 is refused with the typed version code and nothing is written.
func TestCPASourceVersionGateRefusesBelowTheFloor(t *testing.T) {
	for _, version := range []string{"6.9.0", "7.0.12", "7.3.4", "v7.3.4", "7.3.20", "7.99.99", "8.0.0-rc.1"} {
		legacyRoot, _ := writeCPAFixture(t, v7LayoutFixtureConfig, nil)
		_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: version})
		code, ok := UpgradeErrorCodeOf(err)
		if !ok || code != UpgradeErrorCPAVersionUnsupported {
			t.Fatalf("%s refused with %v (code %q), want %s", version, err, code, UpgradeErrorCPAVersionUnsupported)
		}
		if !strings.Contains(err.Error(), CPAImportMinimumVersion) {
			t.Fatalf("the refusal for %s does not name the floor %s: %v", version, CPAImportMinimumVersion, err)
		}
	}
	legacyRoot, _ := writeCPAFixture(t, v7LayoutFixtureConfig, nil)
	_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "9.0.0"})
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAVersionUnsupported {
		t.Fatalf("9.0.0 produced %v (code %q), want %s", err, code, UpgradeErrorCPAVersionUnsupported)
	}
}

// TestCPASourceVersionGateAcceptsTheFloorAndAbove pins the v8-only boundary:
// CPA 8.0.0 and pinned 8.0.55 import, and a standalone `config-version: 8` file
// needs no declared release.
func TestCPASourceVersionGateAcceptsTheFloorAndAbove(t *testing.T) {
	for _, version := range []string{"8.0.0", "v8.0.0", "8.0.55", "v8.0.55"} {
		legacyRoot, _ := writeCPAFixture(t, v7LayoutFixtureConfig, nil)
		source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: version})
		if err != nil {
			t.Fatalf("%s was refused: %v", version, err)
		}
		if source.version == "" {
			t.Fatalf("%s produced an empty source version", version)
		}
	}
	legacyRoot, _ := writeCPAFixture(t, v8FixtureConfig, nil)
	source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
	if err != nil {
		t.Fatalf("a standalone v8.0.55 source was refused without a declared release: %v", err)
	}
	if source.layout != cpaLayoutV8Legacy {
		t.Fatalf("layout = %q, want %q", source.layout, cpaLayoutV8Legacy)
	}
}

// TestCPASourceVersionGateComparesADeclaredReleaseWithTheV8Layout pins the
// direction of the v8 declaration check. A file that declares `config-version: 8`
// was written by a v8 release, so the operator's declared release is accepted
// only when it is a v8 release at or above 8.0.0.
func TestCPASourceVersionGateComparesADeclaredReleaseWithTheV8Layout(t *testing.T) {
	config := "config-version: 8\naccess:\n  api-keys:\n    - \"legacy-client-key-1\"\n"
	cases := []struct {
		declared string
		accepted bool
	}{
		{declared: "v8.0.55", accepted: true},
		{declared: "8.0.55", accepted: true},
		{declared: "8.0.0", accepted: true},
		{declared: "8.0.0-rc.1", accepted: false},
		{declared: "9.0.0", accepted: false},
		{declared: "7.3.20", accepted: false},
		{declared: "7.3.4", accepted: false},
		{declared: "7.0.12", accepted: false},
		{declared: "6.9.0", accepted: false},
		{declared: "8.0.55+", accepted: false},
		{declared: "v8.0.0+", accepted: false},
	}
	for _, testCase := range cases {
		legacyRoot, _ := writeCPAFixture(t, config, nil)
		source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: testCase.declared})
		if testCase.accepted {
			if err != nil {
				t.Fatalf("a config-version: 8 source declared as %s was refused: %v", testCase.declared, err)
			}
			if source.version != testCase.declared {
				t.Fatalf("source version = %q, want the declared %q", source.version, testCase.declared)
			}
			continue
		}
		if err == nil {
			t.Fatalf("a config-version: 8 source declared as %s was accepted", testCase.declared)
		}
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAVersionUnsupported {
			t.Fatalf("%s produced %v (code %q), want %s", testCase.declared, err, code, UpgradeErrorCPAVersionUnsupported)
		}
	}
}

// TestCPASourceReadsACanonicalV8Layout covers the file CPA v8 itself writes: the
// client keys live under `access.api-keys` while `api-keys` holds the provider
// groups. The reader used to take the first existing spelling, so it read the
// provider mapping as the client key list, found no string entries and refused a
// correct v8 source with "holds no client API key". The same wholesale
// consumption hid every `api-keys.<family>` the table does not map.
func TestCPASourceReadsACanonicalV8Layout(t *testing.T) {
	config := `config-version: 8
access:
  api-keys:
    - "v8-client-key-1"
api-keys:
  gemini:
    - name: gemini-1
      keys:
        - api-key: "gemini-provider-key"
  custom-family:
    - name: custom-1
      keys:
        - api-key: "custom-provider-key"
request-retry: 4
`
	legacyRoot, _ := writeCPAFixture(t, config, nil)
	source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
	if err != nil {
		t.Fatalf("open a canonical v8 source: %v", err)
	}
	if source.layout != cpaLayoutV8Legacy {
		t.Fatalf("layout = %q, want %q", source.layout, cpaLayoutV8Legacy)
	}
	if got := strings.Join(source.clientKeys, ","); got != "v8-client-key-1" {
		t.Fatalf("client keys = %q, want the access.api-keys entry", got)
	}
	if _, presence := lookupDocumentPath(source.document, "api-keys.gemini"); presence != documentPresent {
		t.Fatal("the v8 provider group was not mapped to its destination path")
	}
	// The client key list is staged and proved, never copied as configuration.
	if _, presence := lookupDocumentPath(source.document, "access.api-keys"); presence == documentPresent {
		t.Fatal("the client key list was copied into the destination document")
	}
	// A provider family the table does not map is manual work, not a silent drop.
	for _, item := range source.manual {
		if item.item == "config:api-keys.custom-family" {
			return
		}
	}
	t.Fatalf("the unknown api-keys family was swallowed; manual items = %+v", source.manual)
}

// TestCPASourceReportsCredentialFilesItCannotUpload pins the auth directory half
// of "never a silently dropped key": a symbolic link, a file above the upload
// bound and an entry that cannot be read become manual items. They used to be
// skipped without a trace, so the run reported a complete copy while the
// operator's credential was simply absent from both the count and the record.
func TestCPASourceReportsCredentialFilesItCannotUpload(t *testing.T) {
	config := "config-version: 8\naccess:\n  api-keys:\n    - \"legacy-client-key-1\"\n"
	legacyRoot, cpaDirectory := writeCPAFixture(t, config, map[string]string{"codex-a.json": `{"type":"codex"}`})
	authDir := filepath.Join(cpaDirectory, cpaAuthDirectoryName)
	target := filepath.Join(cpaDirectory, "outside-target.json")
	if err := os.WriteFile(target, []byte(`{"type":"claude"}`), 0o600); err != nil {
		t.Fatalf("write the symlink target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(authDir, "claude-link.json")); err != nil {
		t.Skipf("this platform does not create symbolic links: %v", err)
	}
	oversized := filepath.Join(authDir, "codex-big.json")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("x", cpaMaxAuthFileBytes+1)), 0o600); err != nil {
		t.Fatalf("write the oversized credential: %v", err)
	}
	source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "8.0.55"})
	if err != nil {
		t.Fatalf("open the source: %v", err)
	}
	uploadable := make([]string, 0, len(source.credentials))
	for _, credential := range source.credentials {
		uploadable = append(uploadable, credential.Name)
	}
	if strings.Join(uploadable, ",") != "codex-a.json" {
		t.Fatalf("uploadable credentials = %v, want only the regular file", uploadable)
	}
	want := map[string]string{
		"credential:claude-link.json": "credential_not_regular",
		"credential:codex-big.json":   "credential_too_large",
	}
	for _, item := range source.manual {
		reason, ok := want[item.item]
		if !ok {
			continue
		}
		if item.reason != reason {
			t.Fatalf("%s reason = %q, want %q", item.item, item.reason, reason)
		}
		delete(want, item.item)
	}
	if len(want) != 0 {
		t.Fatalf("credential files were dropped without a manual item: %v (manual items %+v)", want, source.manual)
	}
	if _, followed := source.credentialData["claude-link.json"]; followed {
		t.Fatal("the import read through a symbolic link")
	}
}

// TestCPASourceVersionGateRefusesWhenNoReleaseIsNamed pins the "never guess"
// rule for the one input a v7 file cannot provide: it has no version of its own.
func TestCPASourceVersionGateRefusesWhenNoReleaseIsNamed(t *testing.T) {
	legacyRoot, _ := writeCPAFixture(t, v7LayoutFixtureConfig, nil)
	_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
	code, ok := UpgradeErrorCodeOf(err)
	if !ok || code != UpgradeErrorCPAVersionRequired {
		t.Fatalf("an unversioned source produced %v (code %q), want %s", err, code, UpgradeErrorCPAVersionRequired)
	}
}

// TestCPASourceVersionComesFromTheStackFile pins the second input the gate
// accepts: the image tag of the stack file beside the CPA directory, rejecting
// every stack-detected release below v8 regardless of config layout.
func TestCPASourceVersionComesFromTheStackFile(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		config string
	}{
		{name: "canonical v8.0.55 schema", config: v8FixtureConfig},
		{name: "legacy grammar", config: v7LayoutFixtureConfig},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			legacyRoot, cpaDirectory := writeCPAFixture(t, testCase.config, nil)
			composePath := filepath.Join(filepath.Dir(cpaDirectory), "docker-compose.yml")
			for _, preV8 := range []string{"v7.0.12", "v7.3.4", "v7.3.20"} {
				stack := "services:\n  cli-proxy-api:\n    image: eceasy/cli-proxy-api:" + preV8 + "\n"
				if err := os.WriteFile(composePath, []byte(stack), 0o600); err != nil {
					t.Fatalf("write the fixture stack file: %v", err)
				}
				_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
				if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAVersionUnsupported {
					t.Fatalf("the stack file's %s produced %v (code %q), want %s", preV8, err, code, UpgradeErrorCPAVersionUnsupported)
				}
			}
			// A moving tag is not a release, so it is not a version either.
			if err := os.WriteFile(composePath,
				[]byte("services:\n  cli-proxy-api:\n    image: eceasy/cli-proxy-api:latest\n"), 0o600); err != nil {
				t.Fatalf("rewrite the fixture stack file: %v", err)
			}
			_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
			if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAVersionRequired {
				t.Fatalf("a latest tag produced %v (code %q), want %s", err, code, UpgradeErrorCPAVersionRequired)
			}
			// Pinned CPA v8.0.55 in the stack file is accepted.
			if err := os.WriteFile(composePath,
				[]byte("services:\n  cli-proxy-api:\n    image: eceasy/cli-proxy-api:v8.0.55\n"), 0o600); err != nil {
				t.Fatalf("rewrite the fixture stack file to v8.0.55: %v", err)
			}
			source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
			if err != nil {
				t.Fatalf("the stack file's v8.0.55 was refused: %v", err)
			}
			if source.version != "8.0.55" {
				t.Fatalf("source version = %q, want 8.0.55", source.version)
			}
		})
	}
}

// TestCPASourceVersionComesFromTheInstallerStack pins compose.yaml + .env
// resolution: pre-v8 tags (including v7.3.4) are refused without leaking .env
// secrets, and pinned v8.0.55 is accepted.
func TestCPASourceVersionComesFromTheInstallerStack(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		config string
	}{
		{name: "canonical v8.0.55 schema", config: v8FixtureConfig},
		{name: "legacy grammar", config: v7LayoutFixtureConfig},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			legacyRoot, cpaDirectory := writeCPAFixture(t, testCase.config, nil)
			installDir := filepath.Dir(cpaDirectory)
			compose := "services:\n" +
				"  cli-proxy-api:\n" +
				"    image: ${CPA_IMAGE}\n" +
				"    restart: unless-stopped\n" +
				"  cpa-manager-plus:\n" +
				"    image: ${CPAMP_IMAGE}\n"
			if err := os.WriteFile(filepath.Join(installDir, "compose.yaml"), []byte(compose), 0o600); err != nil {
				t.Fatalf("write the installer compose file: %v", err)
			}
			for _, preV8 := range []string{"v7.0.12", "v7.3.4"} {
				if err := os.WriteFile(filepath.Join(installDir, ".env"),
					[]byte("COMPOSE_PROJECT_NAME=cpamp\nCPAMP_ADMIN_KEY=cpamp-admin-secret-value\nCPA_IMAGE=eceasy/cli-proxy-api:"+preV8+"\n"), 0o600); err != nil {
					t.Fatalf("write the installer environment file: %v", err)
				}
				_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
				if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAVersionUnsupported {
					t.Fatalf("the installer's %s produced %v (code %q), want %s", preV8, err, code, UpgradeErrorCPAVersionUnsupported)
				}
				if strings.Contains(err.Error(), "cpamp-admin-secret-value") {
					t.Fatalf("the refusal leaks a value from .env: %v", err)
				}
			}
			if err := os.WriteFile(filepath.Join(installDir, ".env"),
				[]byte("CPA_IMAGE=eceasy/cli-proxy-api:v8.0.55\n"), 0o600); err != nil {
				t.Fatalf("rewrite the installer environment file: %v", err)
			}
			source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
			if err != nil {
				t.Fatalf("the installer's v8.0.55 was refused: %v", err)
			}
			if source.version != "8.0.55" {
				t.Fatalf("source version = %q, want 8.0.55", source.version)
			}
			for _, validEnv := range []string{
				"CPA_IMAGE=eceasy/cli-proxy-api:v8.0.55 # pinned release\n",
				"CPA_IMAGE=\"eceasy/cli-proxy-api:v8.0.55\" # quoted release\n",
				"export CPA_IMAGE='eceasy/cli-proxy-api:v8.0.55' # exported single-quoted\n",
			} {
				if err := os.WriteFile(filepath.Join(installDir, ".env"), []byte(validEnv), 0o600); err != nil {
					t.Fatalf("write commented/quoted environment file: %v", err)
				}
				source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
				if err != nil {
					t.Fatalf("valid .env %q was refused: %v", validEnv, err)
				}
				if source.version != "8.0.55" {
					t.Fatalf("source version = %q, want 8.0.55 for %q", source.version, validEnv)
				}
			}
			// An explicit --cpa-version 8.0.55 cannot override a stack-detected non-v8 major.
			if err := os.WriteFile(filepath.Join(installDir, ".env"),
				[]byte("CPA_IMAGE=eceasy/cli-proxy-api:v9.0.0\n"), 0o600); err != nil {
				t.Fatalf("write v9 environment file: %v", err)
			}
			_, err = openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "8.0.55"})
			if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAVersionUnsupported {
				t.Fatalf("stack v9.0.0 with declared 8.0.55 produced %v (code %q), want %s", err, code, UpgradeErrorCPAVersionUnsupported)
			}
		})
	}
}

// TestCPASourceVersionRefusesADigestBearingImage pins that a tag-plus-digest or
// bare-digest image pins bytes rather than a release, so it requires an explicit
// --cpa-version regardless of config layout.
func TestCPASourceVersionRefusesADigestBearingImage(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		config string
	}{
		{name: "canonical v8.0.55 schema", config: v8FixtureConfig},
		{name: "legacy grammar", config: v7LayoutFixtureConfig},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			legacyRoot, cpaDirectory := writeCPAFixture(t, testCase.config, nil)
			installDir := filepath.Dir(cpaDirectory)
			compose := "services:\n  cli-proxy-api:\n    image: ${CPA_IMAGE}\n"
			if err := os.WriteFile(filepath.Join(installDir, "compose.yaml"), []byte(compose), 0o600); err != nil {
				t.Fatalf("write the installer compose file: %v", err)
			}
			digest := strings.Repeat("a", 64)
			for _, ref := range []string{
				"eceasy/cli-proxy-api:v8.0.55@sha256:" + digest,
				"eceasy/cli-proxy-api:v7.3.4@sha256:" + digest,
				"eceasy/cli-proxy-api@sha256:" + digest,
			} {
				if err := os.WriteFile(filepath.Join(installDir, ".env"),
					[]byte("CPA_IMAGE="+ref+"\n"), 0o600); err != nil {
					t.Fatalf("write the installer environment file: %v", err)
				}
				_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
				if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAVersionRequired {
					t.Fatalf("digest reference %q produced %v (code %q), want %s", ref, err, code, UpgradeErrorCPAVersionRequired)
				}
				if strings.Contains(err.Error(), digest) {
					t.Fatalf("the refusal echoes the digest: %v", err)
				}
			}
		})
	}
}

// TestCPASourceVersionStaysUndeterminedWhenTheStackCannotNameIt pins the
// fail-closed side of the stack reader: unresolved references require
// --cpa-version, pre-v8 compose defaults are refused, and v8.0.55 compose
// defaults are accepted.
func TestCPASourceVersionStaysUndeterminedWhenTheStackCannotNameIt(t *testing.T) {
	legacyRoot, cpaDirectory := writeCPAFixture(t, v8FixtureConfig, nil)
	installDir := filepath.Dir(cpaDirectory)
	if err := os.WriteFile(filepath.Join(installDir, "compose.yaml"),
		[]byte("services:\n  cli-proxy-api:\n    image: ${CPA_IMAGE}\n"), 0o600); err != nil {
		t.Fatalf("write the installer compose file: %v", err)
	}
	// No .env at all: the reference names no release.
	_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAVersionRequired {
		t.Fatalf("an unresolved reference produced %v (code %q), want %s", err, code, UpgradeErrorCPAVersionRequired)
	}
	// A pre-v8 default in the compose reference is refused.
	if err := os.WriteFile(filepath.Join(installDir, "compose.yaml"),
		[]byte("services:\n  cli-proxy-api:\n    image: ${CPA_IMAGE:-eceasy/cli-proxy-api:v7.3.4}\n"), 0o600); err != nil {
		t.Fatalf("rewrite the installer compose file with pre-v8 default: %v", err)
	}
	_, err = openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAVersionUnsupported {
		t.Fatalf("a pre-v8 compose default produced %v (code %q), want %s", err, code, UpgradeErrorCPAVersionUnsupported)
	}
	// A pinned v8.0.55 default in the compose reference is accepted.
	if err := os.WriteFile(filepath.Join(installDir, "compose.yaml"),
		[]byte("services:\n  cli-proxy-api:\n    image: ${CPA_IMAGE:-eceasy/cli-proxy-api:v8.0.55}\n"), 0o600); err != nil {
		t.Fatalf("rewrite the installer compose file: %v", err)
	}
	source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
	if err != nil {
		t.Fatalf("the v8.0.55 compose default was refused: %v", err)
	}
	if source.version != "8.0.55" {
		t.Fatalf("source version = %q, want 8.0.55 from the compose default", source.version)
	}
	// A required reference that has no value is not a release either.
	if err := os.WriteFile(filepath.Join(installDir, "compose.yaml"),
		[]byte("services:\n  cli-proxy-api:\n    image: ${CPA_IMAGE:?set CPA_IMAGE}\n"), 0o600); err != nil {
		t.Fatalf("rewrite the installer compose file: %v", err)
	}
	_, err = openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAVersionRequired {
		t.Fatalf("an unset required reference produced %v (code %q), want %s", err, code, UpgradeErrorCPAVersionRequired)
	}
	if strings.Contains(err.Error(), "set CPA_IMAGE") {
		t.Fatalf("the refusal echoes the compose error message: %v", err)
	}
}

// TestCPASourceRefusesAnUnusableConfiguration pins the typed refusal of a file
// this import cannot read at all.
func TestCPASourceRefusesAnUnusableConfiguration(t *testing.T) {
	legacyRoot, _ := writeCPAFixture(t, "api-keys: [\n", nil)
	_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "8.0.55"})
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAConfigInvalid {
		t.Fatalf("a broken configuration produced %v (code %q), want %s", err, code, UpgradeErrorCPAConfigInvalid)
	}
	legacyRoot, _ = writeCPAFixture(t, "config-version: 8\nrouting:\n  retry:\n    request-retry: 3\n", nil)
	_, err = openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "8.0.55"})
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAClientKeysMissing {
		t.Fatalf("a source without client keys produced %v (code %q), want %s", err, code, UpgradeErrorCPAClientKeysMissing)
	}
	for _, rawConfigVersion := range []string{`"8"`, `"+8"`, `"08"`, `8.0`, `+8`, `0x8`, `7`, `9`} {
		body := "config-version: " + rawConfigVersion + "\naccess:\n  api-keys:\n    - \"legacy-client-key-1\"\n"
		legacyRoot, _ = writeCPAFixture(t, body, nil)
		_, err = openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAVersionUnsupported {
			t.Fatalf("config-version %s produced %v (code %q), want %s", rawConfigVersion, err, code, UpgradeErrorCPAVersionUnsupported)
		}
	}
	for _, multiDoc := range []string{
		"config-version: 8\naccess:\n  api-keys:\n    - \"legacy-client-key-1\"\n---\nrouting:\n  retry:\n    request-retry: 3\n",
		"config-version: 8\naccess:\n  api-keys:\n    - \"legacy-client-key-1\"\n---\n",
	} {
		legacyRoot, _ = writeCPAFixture(t, multiDoc, nil)
		_, err = openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "8.0.55"})
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAConfigInvalid {
			t.Fatalf("multi-document configuration produced %v (code %q), want %s", err, code, UpgradeErrorCPAConfigInvalid)
		}
	}
	outsideDir := filepath.Join(t.TempDir(), "outside-auths")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatalf("create outside auth directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "outside.json"), []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatalf("write outside credential: %v", err)
	}
	fenceConfig := "config-version: 8\noauth:\n  auth-dir: linked-outside\naccess:\n  api-keys:\n    - \"legacy-client-key-1\"\n"
	legacyRoot, cpaDirectory := writeCPAFixture(t, fenceConfig, map[string]string{"in-tree.json": `{"type":"codex"}`})
	if err := os.Symlink(outsideDir, filepath.Join(cpaDirectory, "linked-outside")); err != nil {
		t.Skipf("this platform does not create symbolic links: %v", err)
	}
	aliasDir := filepath.Join(t.TempDir(), "cpa-alias")
	if err := os.Symlink(cpaDirectory, aliasDir); err != nil {
		t.Fatalf("create symlinked CPA directory: %v", err)
	}
	fencedSource, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPADirectory: aliasDir, CPAVersion: "8.0.55"})
	if err != nil {
		t.Fatalf("open fenced source: %v", err)
	}
	if _, ok := fencedSource.credentialData["outside.json"]; ok {
		t.Fatal("oauth.auth-dir read outside the CPA directory tree")
	}
	if _, ok := fencedSource.credentialData["in-tree.json"]; !ok {
		t.Fatalf("expected fallback to in-tree auths directory when oauth.auth-dir resolves outside tree, got %v", fencedSource.credentials)
	}
}

// TestCPASourceGroupingReproducesCPAsOwnShape pins the one transformation the
// table performs: a v7 list becomes the v8 grouping, one group per entry.
func TestCPASourceGroupingReproducesCPAsOwnShape(t *testing.T) {
	list := []any{
		map[string]any{"api-key": "first", "base-url": "https://a.example", "models": []any{map[string]any{"name": "m"}}},
		map[string]any{"api-key": "second"},
	}
	grouped, ok := groupLegacyAPIKeys(list, "gemini")
	if !ok {
		t.Fatal("the v7 list was refused")
	}
	groups := grouped.([]any)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want one per legacy entry", len(groups))
	}
	first := groups[0].(map[string]any)
	if first["name"] != "gemini-1" || first["base-url"] != "https://a.example" {
		t.Fatalf("first group = %#v, want the endpoint lifted and the name synthesized", first)
	}
	keys := first["keys"].([]any)
	if len(keys) != 1 {
		t.Fatalf("first group keys = %#v, want exactly one", keys)
	}
	key := keys[0].(map[string]any)
	if key["api-key"] != "first" {
		t.Fatalf("the key entry = %#v, want the api-key", key)
	}
	if _, lifted := key["base-url"]; lifted {
		t.Fatal("the endpoint stayed on the key entry; CPA lifts it to the group")
	}
	// An already-grouped list is passed through: an old-layout v8 source stores
	// the v8 shape under the v7 key, and grouping it again would nest groups.
	again, ok := groupLegacyAPIKeys(groups, "gemini")
	if !ok || len(again.([]any)) != 2 {
		t.Fatalf("an already grouped list was regrouped: %#v", again)
	}
}
