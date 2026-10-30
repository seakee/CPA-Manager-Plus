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

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
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

// v7FixtureConfig is a v7.3.4-shaped configuration: the v7 spellings this import
// maps, one provider list, the three shapes it reports as manual work, and one
// section it does not know at all.
const v7FixtureConfig = `host: "127.0.0.1"
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

// v734Source opens the standard v7.3.4 fixture with the declared release.
func v734Source(t *testing.T) *cpaManagedSource {
	t.Helper()
	legacyRoot, _ := writeCPAFixture(t, v7FixtureConfig, map[string]string{
		"codex-user.json":  `{"type":"codex","access_token":"token"}`,
		"gemini-user.json": `{"type":"gemini","access_token":"token"}`,
	})
	source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "7.3.4"})
	if err != nil {
		t.Fatalf("open the managed CPA source: %v", err)
	}
	return source
}

// TestCPASourceMapsTheV7SpellingsOntoTheV8Paths is the mapping table's own test:
// every mapped path lands where the destination accepts it and reads back.
func TestCPASourceMapsTheV7SpellingsOntoTheV8Paths(t *testing.T) {
	source := v734Source(t)
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
	source := v734Source(t)
	for _, path := range []string{
		"management", "remote-management", "server", "host", "port",
		"access.api-keys", "plugins", "auth-dir", "oauth.auth-dir",
	} {
		if value, presence := lookupDocumentPath(source.document, path); presence == documentPresent {
			t.Fatalf("%s was copied into the destination document as %#v", path, value)
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
}

// TestCPASourceReportsGeminiAmpcodeAndIdentityConfuseAsManualItems pins the three
// source shapes CPA v8 drops: each one is reported, and none of them fails the
// read.
func TestCPASourceReportsGeminiAmpcodeAndIdentityConfuseAsManualItems(t *testing.T) {
	source := v734Source(t)
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
	source := v734Source(t)
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
	source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "7.3.4"})
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

// TestCPASourceVersionGateRefusesBelowTheFloor is the preflight block: a source
// below v7.3.4 is refused with the typed version code and nothing is written.
func TestCPASourceVersionGateRefusesBelowTheFloor(t *testing.T) {
	legacyRoot, _ := writeCPAFixture(t, v7FixtureConfig, nil)
	_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "7.0.12"})
	code, ok := UpgradeErrorCodeOf(err)
	if !ok || code != UpgradeErrorCPAVersionUnsupported {
		t.Fatalf("v7.0.12 refused with %v (code %q), want %s", err, code, UpgradeErrorCPAVersionUnsupported)
	}
	if !strings.Contains(err.Error(), CPAImportMinimumVersion) {
		t.Fatalf("the refusal does not name the floor %s: %v", CPAImportMinimumVersion, err)
	}
}

// TestCPASourceVersionGateAcceptsTheFloorAndAbove pins the boundary: v7.3.4
// itself imports, and a v8 old-layout file needs no declared release.
func TestCPASourceVersionGateAcceptsTheFloorAndAbove(t *testing.T) {
	for _, version := range []string{"7.3.4", "v7.3.4", "7.3.20", "v8.0.13"} {
		legacyRoot, _ := writeCPAFixture(t, v7FixtureConfig, nil)
		source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: version})
		if err != nil {
			t.Fatalf("%s was refused: %v", version, err)
		}
		if source.version == "" {
			t.Fatalf("%s produced an empty source version", version)
		}
	}
	legacyRoot, _ := writeCPAFixture(t, "config-version: 8\napi-keys:\n  - \"k1\"\n", nil)
	source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
	if err != nil {
		t.Fatalf("a v8 old-layout source was refused without a declared release: %v", err)
	}
	if source.layout != cpaLayoutV8Legacy {
		t.Fatalf("layout = %q, want %q", source.layout, cpaLayoutV8Legacy)
	}
}

// TestCPASourceVersionGateRefusesWhenNoReleaseIsNamed pins the "never guess"
// rule for the one input a v7 file cannot provide: it has no version of its own.
func TestCPASourceVersionGateRefusesWhenNoReleaseIsNamed(t *testing.T) {
	legacyRoot, _ := writeCPAFixture(t, v7FixtureConfig, nil)
	_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
	code, ok := UpgradeErrorCodeOf(err)
	if !ok || code != UpgradeErrorCPAVersionRequired {
		t.Fatalf("an unversioned v7 source produced %v (code %q), want %s", err, code, UpgradeErrorCPAVersionRequired)
	}
}

// TestCPASourceVersionComesFromTheStackFile pins the second input the gate
// accepts: the image tag of the stack file beside the CPA directory.
func TestCPASourceVersionComesFromTheStackFile(t *testing.T) {
	legacyRoot, cpaDirectory := writeCPAFixture(t, v7FixtureConfig, nil)
	stack := "services:\n  cli-proxy-api:\n    image: eceasy/cli-proxy-api:v7.0.12\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(cpaDirectory), "docker-compose.yml"), []byte(stack), 0o600); err != nil {
		t.Fatalf("write the fixture stack file: %v", err)
	}
	_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
	code, ok := UpgradeErrorCodeOf(err)
	if !ok || code != UpgradeErrorCPAVersionUnsupported {
		t.Fatalf("the stack file's v7.0.12 produced %v (code %q), want %s", err, code, UpgradeErrorCPAVersionUnsupported)
	}
	// A moving tag is not a release, so it is not a version either.
	if err := os.WriteFile(filepath.Join(filepath.Dir(cpaDirectory), "docker-compose.yml"),
		[]byte("services:\n  cli-proxy-api:\n    image: eceasy/cli-proxy-api:latest\n"), 0o600); err != nil {
		t.Fatalf("rewrite the fixture stack file: %v", err)
	}
	_, err = openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot})
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAVersionRequired {
		t.Fatalf("a latest tag produced %v (code %q), want %s", err, code, UpgradeErrorCPAVersionRequired)
	}
}

// TestCPASourceRefusesAnUnusableConfiguration pins the typed refusal of a file
// this import cannot read at all.
func TestCPASourceRefusesAnUnusableConfiguration(t *testing.T) {
	legacyRoot, _ := writeCPAFixture(t, "api-keys: [\n", nil)
	_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "7.3.4"})
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAConfigInvalid {
		t.Fatalf("a broken configuration produced %v (code %q), want %s", err, code, UpgradeErrorCPAConfigInvalid)
	}
	legacyRoot, _ = writeCPAFixture(t, "request-retry: 3\n", nil)
	_, err = openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "7.3.4"})
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAClientKeysMissing {
		t.Fatalf("a source without client keys produced %v (code %q), want %s", err, code, UpgradeErrorCPAClientKeysMissing)
	}
}

// TestCPASourceMappingTableNamesTheReleaseOfEveryEntry pins the documentation
// claim: every row names the release that introduced or removed it.
func TestCPASourceMappingTableNamesTheReleaseOfEveryEntry(t *testing.T) {
	for _, path := range cpaConfigPaths {
		if path.current == "" {
			t.Fatalf("mapping row %+v has no destination path", path)
		}
		if path.introduced == "" {
			t.Fatalf("mapping row %q/%q names no release that introduced it", path.legacy, path.current)
		}
		if _, ok := parseCPAVersion(path.introduced); !ok {
			t.Fatalf("mapping row %q names an unparsable release %q", path.current, path.introduced)
		}
		if path.removed != "" {
			t.Fatalf("mapping row %q is both mapped and removed in %q", path.current, path.removed)
		}
	}
	for _, removed := range cpaRemovedConfigPaths {
		if removed.removed == "" {
			t.Fatalf("removed row %q names no release", removed.legacy)
		}
		if _, ok := parseCPAVersion(removed.removed); !ok {
			t.Fatalf("removed row %q names an unparsable release %q", removed.legacy, removed.removed)
		}
		if removed.instruction == "" {
			t.Fatalf("removed row %q carries no operator instruction", removed.legacy)
		}
	}
}

// TestCPASourceMappingPathsAreAllCopyable pins the bound of the table: every
// mapped path is one the adoption executor walks, so a row can never describe a
// copy this step cannot make.
func TestCPASourceMappingPathsAreAllCopyable(t *testing.T) {
	copyable := map[string]bool{}
	for _, path := range configLeafPathsForTest() {
		copyable[path] = true
	}
	for _, path := range configRuleFamilyPathsForTest() {
		copyable[path] = true
	}
	for _, section := range providerSectionNamesForTest() {
		copyable[section] = true
	}
	for _, path := range cpaConfigPaths {
		if !copyable[path.current] {
			t.Fatalf("mapping row %q names %q, which the adoption executor never copies", path.legacy, path.current)
		}
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

// configLeafPathsForTest, configRuleFamilyPathsForTest and
// providerSectionNamesForTest are the copy scope of the adoption executor, read
// from the product's own tables so a mapping row cannot describe a copy the
// executor does not make.
func configLeafPathsForTest() []string { return cpa.ConfigLeafPaths() }

func configRuleFamilyPathsForTest() []string { return cpa.ConfigRuleFamilyPaths() }

func providerSectionNamesForTest() []string { return cpa.ProviderAdoptionSections() }
