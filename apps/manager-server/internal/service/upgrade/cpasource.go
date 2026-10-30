package upgrade

// This file is the source side of the Managed CPA import: a file-backed reader of
// the stopped 1.x CPA directory that presents the tree through the existing
// freshadopt.Source interface. It is deliberately not a general configuration
// converter. It maps exactly the paths this step copies and reports everything
// else the source holds as manual work, so no key is ever dropped silently.
//
// # Where the mapping comes from
//
// The table below is derived from the CPA releases, never invented. Two upstream
// readings produce it and both are checked into the derivation:
//
//   - the v8 converter, which states the v7 spelling of every v8 path it accepts:
//     internal/config/config_v8.go:18-108 (configPath, v8KeyFamilies,
//     buildV8Paths), :23-58 (v8ClientPaths / v8SharedPaths, the historical
//     spellings of the client.codex.* and upstream.* leaves) and :419-448
//     (groupLegacyKeys, the v7 -> v8 grouping of the *-api-key lists);
//   - the v7 releases themselves, read at every tag between v7.0.0 and v7.3.20,
//     which is what the `introduced` / `removed` columns report.
//
// # What the columns mean
//
// `introduced` names the exact release the v7 spelling appeared in. When the
// spelling already exists in the first v7 release it is `v7.0.0`: the entry was
// proven present at v7.0.0, v7.2.0, v7.3.4 and v7.3.20 and the releases before
// v7 are outside the range this import accepts. `removed` names the exact release
// the spelling disappeared in, or is empty when it is still present in the last
// v7 release (v7.3.20). Every entry therefore names the release that introduced
// it, the release that removed it, or both.
//
// # What is never mapped
//
// The destination keeps v2's own identity: its management secret, its listener,
// its plugin choice and its client key list belong to the Manager, and the auth
// directory is the one the destination enumerated. A source key under one of
// those is excluded by name and never reported as manual work, because the
// destination's own value is the intended one.
//
// A path in the table is one the step copies. A source path outside the table is
// reported as a manual item: an operator reads it and does the work, which is
// the only alternative to dropping the key.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshadopt"
)

const (
	// CPAImportMinimumVersion is the lowest Managed CPA source this import
	// accepts. Older releases carry shapes CPA v8 drops (`ampcode`, `gemini`
	// credentials v8 silently skips and `codex.identity-confuse`), so an
	// operator below it is stopped instead of half-migrated.
	CPAImportMinimumVersion = "7.3.4"
	// cpaLayoutV7 is a configuration file with no `config-version` key: the v7
	// layout, which v8 still loads (§ u1-import-facts.md).
	cpaLayoutV7 = "v7"
	// cpaLayoutV8Legacy is a v8 file that still carries the v7 spellings.
	cpaLayoutV8Legacy = "v8-legacy"
)

const (
	cpaConfigFileName = "config.yaml"
	// cpaSiblingDirectoryName is where the v1 stack keeps the managed CPA next to
	// the Manager data directory. It is the same path the upgrade preflight
	// classifies (`managedCPAConfigRelativePath`), never a second convention.
	cpaSiblingDirectoryName = "legacy-cpa"
	// cpaAuthDirectoryName is the directory the v1 installer bind-mounts as the
	// CPA's `/root/.cli-proxy-api` (`install-cpamp.sh:2948-2995`), so it is the
	// auth directory beside a stopped source's config.yaml.
	cpaAuthDirectoryName = "auths"
	// cpaStackComposeFiles are the stack files the v1 installer writes beside the
	// CPA directory. The image tag of the `cli-proxy-api` service they name is the
	// only on-disk statement of which CPA release a stopped stack ran.
	cpaStackComposeFiles = "docker-compose.yml,docker-compose.yaml"
	// cpaMaxConfigBytes bounds the source configuration this reader accepts,
	// matching the destination's own configuration body bound.
	cpaMaxConfigBytes = 8 << 20
	// cpaMaxAuthFileBytes bounds one credential file, as the adoption transport
	// does.
	cpaMaxAuthFileBytes = 1 << 20
	// cpaMaxManualItems bounds how many manual items one run reports, so an
	// unexpected source cannot fill the operation record. The remainder is
	// reported as one aggregate item.
	cpaMaxManualItems = 32
	// cpaMaxUnmappedLeavesInSection bounds the unmapped leaves reported inside
	// one mapped section.
	cpaMaxUnmappedLeavesInSection = 8
)

// cpaConfigVersionKey is the only top-level key a v8 configuration declares its
// layout with. v7 has no such key at all (checked at every v7 release), so its
// absence means "v7 or earlier", never "v8".
const cpaConfigVersionKey = "config-version"

// cpaPresenceAtFirstV7 is the `introduced` value of an entry that exists in the
// first v7 release. It is a release, not a placeholder: the table was read at
// v7.0.0 for exactly these entries.
const cpaPresenceAtFirstV7 = "v7.0.0"

// cpaConfigPath is one path this step copies.
//
// `legacy` is the v7 spelling; empty means the path exists only in the v8 layout
// (a v8 old-layout source can carry it, a v7 one cannot). `current` is the v8
// path the destination accepts. `whole` marks a value copied as a whole (a
// provider group, a rule family) instead of a single leaf; `grouped` marks the
// eight `api-keys.*` families, whose v7 list is one flat entry per credential and
// whose v8 form is one group per entry.
type cpaConfigPath struct {
	legacy     string
	current    string
	introduced string
	whole      bool
	grouped    bool
}

// cpaConfigPaths is the bounded mapping this import applies. Order is the order
// the copy visits the paths.
var cpaConfigPaths = []cpaConfigPath{
	// The eight api-keys.* provider groups. CPA v8 keeps one group per legacy
	// entry because equal endpoints do not imply equal routing, headers, models
	// or credential policies (config_v8.go:419-425), so the copy reproduces that
	// grouping rather than handing the destination a v7-shaped list.
	{legacy: "gemini-api-key", current: "api-keys.gemini", introduced: cpaPresenceAtFirstV7, whole: true, grouped: true},
	{legacy: "interactions-api-key", current: "api-keys.interactions", introduced: "v7.2.51", whole: true, grouped: true},
	{legacy: "vertex-api-key", current: "api-keys.vertex", introduced: cpaPresenceAtFirstV7, whole: true, grouped: true},
	{legacy: "codex-api-key", current: "api-keys.codex", introduced: cpaPresenceAtFirstV7, whole: true, grouped: true},
	{legacy: "claude-api-key", current: "api-keys.claude", introduced: cpaPresenceAtFirstV7, whole: true, grouped: true},
	{legacy: "xai-api-key", current: "api-keys.xai", introduced: "v7.2.73", whole: true, grouped: true},
	{legacy: "meta-api-key", current: "api-keys.meta", introduced: "v7.3.4", whole: true, grouped: true},
	{legacy: "openai-compatibility", current: "api-keys.openai-compatibility", introduced: cpaPresenceAtFirstV7, whole: true, grouped: true},

	// The oauth maps that are provider state. `oauth.settings` is deliberately
	// absent: no v7 release has an `oauth-settings` key (checked at every tag), so
	// a v7 source cannot carry it and mapping it would be an invented entry.
	{legacy: "oauth-model-alias", current: "oauth.model-alias", introduced: cpaPresenceAtFirstV7, whole: true},
	{legacy: "oauth-excluded-models", current: "oauth.excluded-models", introduced: cpaPresenceAtFirstV7, whole: true},
	{legacy: "oauth-request-scoped-errors", current: "oauth.request-scoped-errors", introduced: "v7.2.138", whole: true},

	// The five payload rule families, copied as whole values.
	{legacy: "payload.default", current: "requests.payload.default", introduced: cpaPresenceAtFirstV7, whole: true},
	{legacy: "payload.default-raw", current: "requests.payload.default-raw", introduced: cpaPresenceAtFirstV7, whole: true},
	{legacy: "payload.override", current: "requests.payload.override", introduced: cpaPresenceAtFirstV7, whole: true},
	{legacy: "payload.override-raw", current: "requests.payload.override-raw", introduced: cpaPresenceAtFirstV7, whole: true},
	{legacy: "payload.filter", current: "requests.payload.filter", introduced: cpaPresenceAtFirstV7, whole: true},

	// Gateway leaves. Each one is a path the gateway configuration page reads and
	// writes against CPA v8 (cpa.ConfigLeafPaths), so each one has a readback.
	{legacy: "request-retry", current: "routing.retry.request-retry", introduced: cpaPresenceAtFirstV7},
	{legacy: "max-retry-credentials", current: "routing.retry.max-retry-credentials", introduced: cpaPresenceAtFirstV7},
	{legacy: "max-retry-interval", current: "routing.retry.max-retry-interval", introduced: cpaPresenceAtFirstV7},
	{legacy: "disable-cooling", current: "routing.cooldown.disable-cooling", introduced: cpaPresenceAtFirstV7},
	{legacy: "save-cooldown-status", current: "routing.cooldown.save-cooldown-status", introduced: "v7.2.19"},
	{legacy: "transient-error-cooldown-seconds", current: "routing.cooldown.transient-error-cooldown-seconds", introduced: "v7.2.19"},
	{legacy: "proxy-url", current: "requests.proxy-url", introduced: cpaPresenceAtFirstV7},

	// The codex provider block. v8 loads `codex.<leaf>` as
	// `oauth.providers.codex.<leaf>` and then prefers the canonical
	// `upstream.codex.*` spelling (config_v8.go:23-46), so the canonical path is
	// what this table writes. `codex.identity-confuse` is absent on purpose: it is
	// a v7 field v8 drops, reported as a manual item instead.
	//
	// `codex-header-defaults` is absent for a different reason, recorded in
	// cpaUnadoptableSections: v8 still loads it, but the destination gateway table
	// has no `upstream.codex.header-defaults` leaf, so there is no path to write.
	// The sibling `claude-header-defaults.*` leaves below do exist in that table,
	// which is why they are mapped and the codex ones are not.
	{legacy: "codex.disable-codex-cloaking", current: "upstream.codex.disable-codex-cloaking", introduced: "v7.2.110"},
	{legacy: "codex.stream-bootstrap-buffering", current: "upstream.codex.stream-bootstrap-buffering", introduced: "v7.2.138"},
	{legacy: "codex.stream-bootstrap-timeout", current: "upstream.codex.stream-bootstrap-timeout", introduced: "v7.3.3"},
	{legacy: "codex.orphan-delegation-compatibility", current: "upstream.codex.orphan-delegation-compatibility", introduced: "v7.2.149"},
	{legacy: "codex.model-level-cooling", current: "upstream.codex.model-level-cooling", introduced: "v7.2.156"},
	{legacy: "codex.response-steering", current: "upstream.codex.response-steering", introduced: "v7.3.9"},
	{legacy: "codex.optimize-multi-agent-v2", current: "client.codex.optimize-multi-agent-v2", introduced: "v7.2.98"},

	// The claude provider block.
	{legacy: "claude.model-level-cooling", current: "upstream.claude.model-level-cooling", introduced: "v7.3.7"},
	{legacy: "disable-claude-cloak-mode", current: "upstream.claude.disable-claude-cloak-mode", introduced: "v7.2.8"},
	{legacy: "claude-header-defaults.user-agent", current: "upstream.claude.header-defaults.user-agent", introduced: cpaPresenceAtFirstV7},
	{legacy: "claude-header-defaults.package-version", current: "upstream.claude.header-defaults.package-version", introduced: cpaPresenceAtFirstV7},
	{legacy: "claude-header-defaults.runtime-version", current: "upstream.claude.header-defaults.runtime-version", introduced: cpaPresenceAtFirstV7},
	{legacy: "claude-header-defaults.os", current: "upstream.claude.header-defaults.os", introduced: cpaPresenceAtFirstV7},
	{legacy: "claude-header-defaults.arch", current: "upstream.claude.header-defaults.arch", introduced: cpaPresenceAtFirstV7},
	{legacy: "claude-header-defaults.timeout", current: "upstream.claude.header-defaults.timeout", introduced: cpaPresenceAtFirstV7},
	{legacy: "claude-header-defaults.timezone", current: "upstream.claude.header-defaults.timezone", introduced: "v7.2.116"},
	{legacy: "claude-header-defaults.stabilize-device-profile", current: "upstream.claude.header-defaults.stabilize-device-profile", introduced: cpaPresenceAtFirstV7},

	// The xai provider block.
	{legacy: "xai.inject-x-search", current: "upstream.xai.inject-x-search", introduced: "v7.2.102"},
}

// cpaRemovedConfigPaths are the v7 spellings this import knows v8 dropped. They
// are never mapped; each one becomes a manual item that names the release it was
// removed in, so the operator learns why it is not copied instead of finding it
// gone.
var cpaRemovedConfigPaths = []struct {
	legacy      string
	suffix      string
	removed     string
	instruction string
}{
	{
		legacy: "ampcode", suffix: "amp-", removed: "v7.2.0",
		instruction: "CPA v8 has no ampcode integration (the section was removed upstream in v7.2.0). Re-create it in the destination Console if the destination CPA still supports it.",
	},
	{
		legacy: "codex.identity-confuse", suffix: "", removed: "v8.0.4",
		instruction: "CPA v8 has no codex.identity-confuse field (removed upstream in v8.0.4). There is nothing to copy to the destination.",
	},
}

// cpaUnadoptableSections are the v7 sections CPA v8 still loads but this import
// cannot copy, because the destination's adoptable surface has no leaf for them.
// Each one is kept as a manual item that names the destination path and says why
// nothing was written, so the gap is recorded instead of the key disappearing.
//
// `codex-header-defaults` is the one such section today. CPA v8 keeps the field
// — `codex-header-defaults` loads as `oauth.providers.codex.header-defaults`
// (internal/config/config_v8.go:93, internal/config/config.go:144 at v8.0.13) —
// but that subtree is provider runtime state the destination's gateway table
// does not expose, so the adoption executor has no leaf to write and `oauth` is
// a Manager-owned root (freshadopt.ManagerOwned). There is no
// `upstream.codex.header-defaults` destination path, and inventing one would
// write a key CPA never reads.
var cpaUnadoptableSections = map[string]string{
	"codex-header-defaults": "CPA v8 keeps codex header defaults (`codex-header-defaults` loads as `oauth.providers.codex.header-defaults`), but this import has no adoptable destination path for them, so they were not copied. Set them in the destination CPA configuration if you still need them.",
}

// cpaExcludedSourceSections are the source keys the destination must keep its
// own value for. They are neither copied nor reported: the Manager owns the
// destination's management secret, listener, plugin root, auth directory and
// client key list, and copying the source's would hand the destination the wrong
// identity.
var cpaExcludedSourceSections = map[string]bool{
	"server":            true,
	"host":              true,
	"port":              true,
	"tls":               true,
	"management":        true,
	"remote-management": true,
	"plugins":           true,
	"auth-dir":          true,
	cpaConfigVersionKey: true,
}

// cpaClientKeyPaths are the two spellings of the client key list. The list is
// installed by the destination stage and proved by cpa.VerifyV8Adoption, never
// copied as configuration.
var cpaClientKeyPaths = []string{"api-keys", "access.api-keys"}

// cpaVersion is a parsed CPA release.
type cpaVersion struct {
	major int
	minor int
	patch int
}

func (v cpaVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

// atLeast reports whether v is the floor release or newer.
func (v cpaVersion) atLeast(other cpaVersion) bool {
	if v.major != other.major {
		return v.major > other.major
	}
	if v.minor != other.minor {
		return v.minor > other.minor
	}
	return v.patch >= other.patch
}

var cpaImportFloor = cpaVersion{major: 7, minor: 3, patch: 4}

// cpaVersionPattern accepts a release tag or a bare version, with an optional
// pre-release or build suffix, and nothing else. `latest`, a branch name or a
// digest is not a version and never becomes one.
var cpaVersionPattern = regexp.MustCompile(`^[vV]?(\d+)\.(\d+)\.(\d+)(?:[-+][0-9A-Za-z.+_-]*)?$`)

// parseCPAVersion parses one CPA release. It refuses anything that is not an
// exact three-part release, because a range or a moving tag cannot be compared
// with the floor.
func parseCPAVersion(raw string) (cpaVersion, bool) {
	matches := cpaVersionPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if matches == nil {
		return cpaVersion{}, false
	}
	numbers := make([]int, 0, 3)
	for _, part := range matches[1:] {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return cpaVersion{}, false
		}
		numbers = append(numbers, value)
	}
	return cpaVersion{major: numbers[0], minor: numbers[1], patch: numbers[2]}, true
}

// cpaStackImagePattern finds the CPA image reference in a stack compose file.
var cpaStackImagePattern = regexp.MustCompile(`^\s*(?:-\s*)?image:\s*"?([^\s"#]+)"?\s*(?:#.*)?$`)

// cpaSourceVersion resolves the CPA release a stopped stack ran.
//
// There is no version anywhere else: no v7 configuration file carries a
// `version` key (verified across every v7 release), and the 1.x Manager never
// stored one. Two inputs remain, and both are declarations rather than guesses:
// the operator's own `--cpa-version`, and the image tag of the `cli-proxy-api`
// service in the stack file beside the CPA directory. When neither names an exact
// release the gate refuses; it never infers one from the shape of the file.
func cpaSourceVersion(cpaDirectory, declared string) (string, bool) {
	if trimmed := strings.TrimSpace(declared); trimmed != "" {
		return trimmed, true
	}
	for _, name := range strings.Split(cpaStackComposeFiles, ",") {
		path := filepath.Join(filepath.Dir(cpaDirectory), name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<10 {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			matches := cpaStackImagePattern.FindStringSubmatch(line)
			if matches == nil {
				continue
			}
			reference := matches[1]
			digestTrimmed, _, _ := strings.Cut(reference, "@")
			slash := strings.LastIndex(digestTrimmed, "/")
			last := digestTrimmed[slash+1:]
			repository, tag, tagged := strings.Cut(last, ":")
			if !tagged || repository != "cli-proxy-api" || tag == "" || tag == "latest" {
				continue
			}
			return tag, true
		}
	}
	return "", false
}

// cpaManualItem is one source item the import cannot copy. It carries the
// adoption item identity, the bounded reason and the operator's instruction; it
// never carries a value.
type cpaManualItem struct {
	item        string
	reason      string
	instruction string
}

// cpaManagedSource is the file-backed freshadopt.Source of one stopped 1.x CPA
// directory.
type cpaManagedSource struct {
	directory   string
	layout      string
	version     string
	document    map[string]any
	clientKeys  []string
	credentials []freshadopt.CredentialFile
	// credentialData holds the exact bytes of the uploadable credentials. The
	// import keeps them in memory for the run's lifetime and never writes them
	// anywhere else.
	credentialData map[string][]byte
	manual         []cpaManualItem
	fingerprint    string
}

// cpaSourceOptions is the file half of one import run.
type cpaSourceOptions struct {
	LegacyRoot       string
	CPADirectory     string
	CPAAuthDirectory string
	CPAVersion       string
}

// openManagedCPASource reads the stopped source. Every refusal it returns is
// typed and happens before anything is written, because it only ever reads.
func openManagedCPASource(options cpaSourceOptions) (*cpaManagedSource, error) {
	directory, err := resolveCPADirectory(options)
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(directory, cpaConfigFileName)
	info, err := os.Stat(configPath)
	if err != nil || !info.Mode().IsRegular() {
		return nil, newUpgradeError(UpgradeErrorCPASourceMissing,
			"the 1.x stack holds no readable managed CPA config.yaml, so there is no CPA state to import", nil)
	}
	if info.Size() > cpaMaxConfigBytes {
		return nil, newUpgradeError(UpgradeErrorCPASourceMissing,
			"the managed CPA config.yaml is larger than this import accepts, so it was not read", nil)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, newUpgradeError(UpgradeErrorCPASourceMissing, "the managed CPA config.yaml could not be read", err)
	}
	document, err := decodeCPAConfigDocument(data)
	if err != nil {
		return nil, newUpgradeError(UpgradeErrorCPAConfigInvalid,
			"the managed CPA config.yaml is not a readable YAML mapping, so it was not imported", err)
	}
	source := &cpaManagedSource{
		directory:      directory,
		layout:         cpaLayoutV7,
		document:       map[string]any{},
		credentialData: map[string][]byte{},
		fingerprint:    digestBytes(data),
	}
	if err := source.gateVersion(document, options.CPAVersion); err != nil {
		return nil, err
	}
	if err := source.mapDocument(document); err != nil {
		return nil, err
	}
	if len(source.clientKeys) == 0 {
		return nil, newUpgradeError(UpgradeErrorCPAClientKeysMissing,
			"the 1.x CPA holds no client API key, so the destination could not be verified with the keys the import would install", nil)
	}
	if len(source.clientKeys) > 128 {
		return nil, newUpgradeError(UpgradeErrorCPAClientKeysMissing,
			"the 1.x CPA holds more client API keys than a destination accepts, so none were installed", nil)
	}
	if err := source.readCredentials(options.CPAAuthDirectory); err != nil {
		return nil, err
	}
	return source, nil
}

// resolveCPADirectory resolves the CPA directory beside the 1.x data directory.
func resolveCPADirectory(options cpaSourceOptions) (string, error) {
	raw := strings.TrimSpace(options.CPADirectory)
	if raw == "" {
		root, err := resolveLegacyRoot(options.LegacyRoot)
		if err != nil {
			return "", err
		}
		raw = filepath.Join(filepath.Dir(root), cpaSiblingDirectoryName)
	}
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve the managed CPA directory: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return "", newUpgradeError(UpgradeErrorCPASourceMissing,
			"the 1.x stack holds no managed CPA directory beside the Manager data directory, so there is no CPA state to import", err)
	}
	return absolute, nil
}

// decodeCPAConfigDocument parses one CPA configuration file. Numbers keep their
// integer form (yaml returns ints for integers), so a value read here validates
// against the same gateway table the destination applies.
func decodeCPAConfigDocument(data []byte) (map[string]any, error) {
	var document map[string]any
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if document == nil {
		return nil, errors.New("the configuration file is empty")
	}
	return normalizeDocument(document), nil
}

// normalizeDocument converts every nested mapping into the map[string]any shape
// the destination document is compared in. yaml.v3 already produces that shape
// for a map[string]any target; this walks the tree so a mapping nested under a
// non-string-keyed wrapper still reaches the same type, and it drops nothing.
func normalizeDocument(document map[string]any) map[string]any {
	out := make(map[string]any, len(document))
	for key, value := range document {
		out[key] = normalizeDocumentValue(value)
	}
	return out
}

func normalizeDocumentValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return normalizeDocument(typed)
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[fmt.Sprint(key)] = normalizeDocumentValue(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, normalizeDocumentValue(item))
		}
		return out
	default:
		return value
	}
}

// gateVersion decides whether this source may be imported at all.
//
// A file that declares `config-version: 8` is a v8 CPA serving the old layout:
// v8 is above the floor whatever the file's spelling is, so no declared version
// is needed. Any other file is the v7 layout, and its release has to be named by
// the operator or the stack file; below v7.3.4 the operator is stopped here,
// before a single write.
func (s *cpaManagedSource) gateVersion(document map[string]any, declared string) error {
	rawConfigVersion, present := document[cpaConfigVersionKey]
	if present {
		version, ok := configVersionNumber(rawConfigVersion)
		if !ok || version != 8 {
			return newUpgradeError(UpgradeErrorCPAVersionUnsupported,
				"the managed CPA config.yaml declares a config-version this destination does not load, so it was not imported", nil)
		}
		s.layout = cpaLayoutV8Legacy
		declaredVersion := strings.TrimSpace(declared)
		if declaredVersion == "" {
			// A v8 file is its own evidence of the generation. The exact release is
			// not needed to gate a v8 source, so it is reported as the generation
			// rather than guessed.
			s.version = "v8"
			return nil
		}
		if parsed, ok := parseCPAVersion(declaredVersion); !ok || !(cpaVersion{major: 8}).atLeast(parsed) {
			return newUpgradeError(UpgradeErrorCPAVersionUnsupported,
				"the declared CPA version does not describe the v8 configuration this source holds, so it was not imported", nil)
		}
		s.version = declaredVersion
		return nil
	}
	version, ok := cpaSourceVersion(s.directory, declared)
	if !ok {
		return newUpgradeError(UpgradeErrorCPAVersionRequired,
			"the managed CPA source is the v7 layout and neither --cpa-version nor the stack file names its release; a v7 layout has no version of its own, and this import never guesses one", nil)
	}
	parsed, ok := parseCPAVersion(version)
	if !ok {
		return newUpgradeError(UpgradeErrorCPAVersionUnsupported,
			"the managed CPA release the source declares is not an exact release, so it cannot be compared with the accepted range and was not imported", nil)
	}
	if !parsed.atLeast(cpaImportFloor) {
		return newUpgradeError(UpgradeErrorCPAVersionUnsupported,
			fmt.Sprintf("the managed CPA source is release %s and this import accepts %s and above; upgrade the CPA inside the 1.x stack before importing it",
				parsed.String(), CPAImportMinimumVersion), nil)
	}
	s.version = parsed.String()
	return nil
}

// configVersionNumber reads the scalar `config-version` value. CPA requires an
// integer 8 and refuses anything else on load, so a string is accepted only when
// it is exactly that number.
func configVersionNumber(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// mapDocument maps the source onto the v8 document the destination accepts and
// collects everything it does not map.
func (s *cpaManagedSource) mapDocument(document map[string]any) error {
	consumed := map[string]bool{}
	roots := map[string]bool{}
	for _, path := range cpaConfigPaths {
		if path.legacy != "" {
			roots[firstSegment(path.legacy)] = true
		}
		if path.current != "" {
			roots[firstSegment(path.current)] = true
		}
	}
	for _, key := range cpaClientKeyPaths {
		roots[firstSegment(key)] = true
	}

	for _, path := range cpaConfigPaths {
		value, found := s.sourceValue(document, path)
		if !found {
			continue
		}
		if path.grouped {
			grouped, ok := groupLegacyAPIKeys(value, sectionProvider(path.current))
			if !ok {
				s.addManual(cpaManualItem{
					item:        "section:" + path.current,
					reason:      "value_shape_unsupported",
					instruction: "The source " + path.legacy + " entries are not the list CPA reads. Re-add this provider's keys on the destination Console.",
				})
				continue
			}
			value = grouped
		}
		setDocumentPath(s.document, path.current, value)
		if path.whole {
			consumed[path.current] = true
			if path.legacy != "" {
				consumed[path.legacy] = true
			}
			continue
		}
		consumed[path.legacy] = true
		consumed[path.current] = true
	}

	// The client key list is installed by the destination stage and proved by
	// cpa.VerifyV8Adoption, so the source spelling is consumed here: it is copied,
	// just not as configuration.
	for _, key := range cpaClientKeyPaths {
		consumed[key] = true
	}
	keys, found := lookupFirst(document, cpaClientKeyPaths...)
	if !found {
		// No client key is a refusal the caller makes after mapping; leaving the
		// list empty here keeps the reason in one place.
		s.clientKeys = nil
	} else {
		s.clientKeys = stringList(keys)
	}

	s.reportUnmapped(document, roots, consumed)
	return nil
}

// sourceValue reads one table path from the source. The v8 spelling wins when
// both spellings are present, which is exactly how CPA itself resolves an
// old-layout file (config_v8.go:241-347).
func (s *cpaManagedSource) sourceValue(document map[string]any, path cpaConfigPath) (any, bool) {
	if path.current != "" {
		if value, presence := lookupDocumentPath(document, path.current); presence == documentPresent {
			return value, true
		}
	}
	if path.legacy != "" {
		if value, presence := lookupDocumentPath(document, path.legacy); presence == documentPresent {
			return value, true
		}
	}
	return nil, false
}

// reportUnmapped turns every source key this step does not copy into a manual
// item. Nothing is dropped silently: a top-level section the table does not name
// is reported by name, and a leaf inside a mapped section that the table does not
// name is reported by its dotted path.
func (s *cpaManagedSource) reportUnmapped(document map[string]any, roots, consumed map[string]bool) {
	topLevel := make([]string, 0, len(document))
	for key := range document {
		topLevel = append(topLevel, key)
	}
	sort.Strings(topLevel)
	for _, key := range topLevel {
		if cpaExcludedSourceSections[key] {
			continue
		}
		// A mapped root is walked, even when the removal table also names a
		// section of the same name: `codex` is a mapped root whose one removed
		// leaf is reported by its own path.
		if roots[key] {
			s.reportUnmappedLeaves(key, document[key], consumed)
			continue
		}
		if isRemovedSection(key) {
			s.reportRemovedSection(key)
			continue
		}
		if instruction, ok := cpaUnadoptableSections[key]; ok {
			s.addManual(cpaManualItem{
				item:        "section:" + key,
				reason:      "destination_surface_missing",
				instruction: instruction,
			})
			continue
		}
		s.addManual(cpaManualItem{
			item:        "section:" + key,
			reason:      "not_in_import_scope",
			instruction: "The source section " + key + " is outside the CPA state this import copies. Set it on the destination Console if the destination still supports it.",
		})
	}
}

// reportRemovedSection records one section this import knows CPA v8 dropped.
func (s *cpaManagedSource) reportRemovedSection(key string) {
	instruction, ok := cpaRemovedSectionInstruction(key)
	if !ok {
		return
	}
	s.addManual(cpaManualItem{
		item:        "section:" + key,
		reason:      "removed_upstream",
		instruction: instruction,
	})
}

// cpaRemovedSectionInstruction reports the operator instruction of a top-level
// section this import knows CPA v8 no longer has. An `amp-*` sibling is removed
// with the section that declares it, which is why the row names a prefix as well
// as an exact key.
func cpaRemovedSectionInstruction(key string) (string, bool) {
	for _, removed := range cpaRemovedConfigPaths {
		if firstSegment(removed.legacy) == key {
			return removed.instruction, true
		}
		if removed.suffix != "" && strings.HasPrefix(key, removed.suffix) {
			return removed.instruction, true
		}
	}
	return "", false
}

// reportUnmappedLeaves reports the leaves of one mapped section the table does
// not carry.
func (s *cpaManagedSource) reportUnmappedLeaves(section string, value any, consumed map[string]bool) {
	leaves := map[string]bool{}
	collectDocumentLeaves(section, value, leaves)
	paths := make([]string, 0, len(leaves))
	for path := range leaves {
		if consumed[path] || underConsumedPrefix(path, consumed) {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for index, path := range paths {
		if index >= cpaMaxUnmappedLeavesInSection {
			s.addManual(cpaManualItem{
				item:        "section:" + section + "-unmapped",
				reason:      "not_in_import_scope",
				instruction: fmt.Sprintf("%d further keys under %s are outside the CPA state this import copies. Review them on the destination Console.", len(paths)-index, section),
			})
			return
		}
		if reason, instruction, ok := cpaRemovedLeaf(path); ok {
			s.addManual(cpaManualItem{item: "config:" + path, reason: reason, instruction: instruction})
			continue
		}
		s.addManual(cpaManualItem{
			item:        "config:" + path,
			reason:      "not_in_import_scope",
			instruction: "The source setting " + path + " is outside the CPA state this import copies. Set it on the destination Console if the destination still supports it.",
		})
	}
}

// cpaRemovedLeaf names the release a leaf inside a mapped section was removed in.
func cpaRemovedLeaf(path string) (string, string, bool) {
	for _, removed := range cpaRemovedConfigPaths {
		if removed.legacy != path {
			continue
		}
		return "removed_upstream", removed.instruction, true
	}
	return "", "", false
}

// isRemovedSection reports a top-level key this import knows CPA v8 removed.
func isRemovedSection(key string) bool {
	_, ok := cpaRemovedSectionInstruction(key)
	return ok
}

func underConsumedPrefix(path string, consumed map[string]bool) bool {
	for index := strings.LastIndex(path, "."); index > 0; index = strings.LastIndex(path[:index], ".") {
		if consumed[path[:index]] {
			return true
		}
	}
	return false
}

// addManual appends one manual item, bounded by cpaMaxManualItems.
func (s *cpaManagedSource) addManual(item cpaManualItem) {
	if len(s.manual) >= cpaMaxManualItems {
		for index := range s.manual {
			if s.manual[index].item == "cpa-config-unknown-remainder" {
				return
			}
		}
		s.manual = append(s.manual, cpaManualItem{
			item:        "cpa-config-unknown-remainder",
			reason:      "not_in_import_scope",
			instruction: "Further source keys beyond the reported ones are outside the CPA state this import copies. Review the source CPA configuration on the destination Console.",
		})
		return
	}
	for _, existing := range s.manual {
		if existing.item == item.item {
			return
		}
	}
	s.manual = append(s.manual, item)
}

// readCredentials reads the auth directory beside the CPA config.
//
// The directory is the operator's override, then the `auth-dir` the source
// configures when that resolves inside the CPA directory, then the sibling
// `auths` the v1 stack bind-mounts. A source without any of them has no
// credential files, which is a valid state: the run reports zero credentials
// instead of inventing a location.
//
// A `type: gemini` file is excluded from the copy and reported as a manual item:
// CPA v8 silently skips gemini credentials, so uploading one would produce a run
// that claims a copy the destination never lists.
func (s *cpaManagedSource) readCredentials(override string) error {
	directory, ok := s.resolveAuthDirectory(override)
	if !ok {
		return nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return newUpgradeError(UpgradeErrorCPASourceMissing, "the managed CPA auth directory could not be read", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > cpaMaxAuthFileBytes {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		credential := freshadopt.CredentialFile{Name: entry.Name()}
		if parsed, ok := credentialMetadata(data); ok {
			credential.Type = parsed.Type
			credential.Disabled = parsed.Disabled
		}
		if strings.EqualFold(credential.Type, "gemini") {
			s.addManual(cpaManualItem{
				item:        "credential:" + entry.Name(),
				reason:      "gemini_credential_unsupported",
				instruction: "CPA v8 never lists a gemini credential, so " + entry.Name() + " was not uploaded. Re-authenticate Gemini on the destination if the destination release supports it.",
			})
			continue
		}
		s.credentials = append(s.credentials, credential)
		s.credentialData[entry.Name()] = data
	}
	sort.Slice(s.credentials, func(i, j int) bool { return s.credentials[i].Name < s.credentials[j].Name })
	return nil
}

// resolveAuthDirectory resolves the source auth directory inside the CPA tree.
func (s *cpaManagedSource) resolveAuthDirectory(override string) (string, bool) {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		absolute, err := filepath.Abs(trimmed)
		if err != nil {
			return "", false
		}
		if info, err := os.Stat(absolute); err == nil && info.IsDir() {
			return absolute, true
		}
		return "", false
	}
	configured, found := lookupFirst(s.document, "auth-dir", "oauth.auth-dir")
	if raw, ok := configured.(string); found && ok && strings.TrimSpace(raw) != "" {
		candidate := strings.TrimSpace(raw)
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(s.directory, candidate)
		}
		candidate, err := canonicalPath(candidate)
		if err == nil && within(s.directory, candidate) {
			if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
				return candidate, true
			}
		}
	}
	sibling := filepath.Join(s.directory, cpaAuthDirectoryName)
	if info, err := os.Stat(sibling); err == nil && info.IsDir() {
		return sibling, true
	}
	return "", false
}

// credentialType is the two metadata fields this import reads from one
// credential file. Nothing else leaves the file.
type credentialType struct {
	Type     string `json:"type"`
	Disabled bool   `json:"disabled"`
}

func credentialMetadata(data []byte) (credentialType, bool) {
	var parsed credentialType
	if err := json.Unmarshal(data, &parsed); err != nil {
		return credentialType{}, false
	}
	parsed.Type = strings.TrimSpace(parsed.Type)
	return parsed, true
}

// ListCredentialFiles reports the credential files this run may upload.
func (s *cpaManagedSource) ListCredentialFiles(_ context.Context) ([]freshadopt.CredentialFile, error) {
	return slices.Clone(s.credentials), nil
}

// DownloadCredential returns one credential's exact bytes. The caller keeps them
// in memory only.
func (s *cpaManagedSource) DownloadCredential(_ context.Context, name string) ([]byte, error) {
	data, ok := s.credentialData[name]
	if !ok {
		return nil, fmt.Errorf("the 1.x CPA holds no credential file named %q", name)
	}
	return slices.Clone(data), nil
}

// ReadConfigDocument returns the mapped v8 document. It is the source's resolved
// value, never a second read: the source is a stopped directory, so two reads
// could not observe two states.
func (s *cpaManagedSource) ReadConfigDocument(_ context.Context) (map[string]any, error) {
	return s.document, nil
}

// sourceIdentity is the bounded, non-secret identity of the source this run
// imported. It names the exact configuration bytes, so a later stage can tell
// the source it was staged from apart from a different one.
func (s *cpaManagedSource) sourceIdentity() string {
	return "legacy-managed:sha256:" + s.fingerprint
}

// groupLegacyAPIKeys reproduces CPA's own v7 -> v8 grouping of one `*-api-key`
// list (config_v8.go:419-448, groupLegacyKeys): one group per legacy entry,
// named `<provider>-<n>`, carrying the endpoint and the shared key fields, with
// the entry itself as the group's single key. `openai-compatibility` keeps its
// own shape and moves `api-key-entries` into `keys`.
//
// A list whose entries are already v8 groups is passed through: an old-layout v8
// source stores the new shape under the old key.
func groupLegacyAPIKeys(value any, provider string) (any, bool) {
	if provider == "" {
		return nil, false
	}
	entries, ok := value.([]any)
	if !ok {
		return nil, false
	}
	if provider == "openai-compatibility" && len(entries) == 0 {
		return entries, true
	}
	grouped := make([]any, 0, len(entries))
	for index, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, false
		}
		if _, alreadyGrouped := entry["keys"]; alreadyGrouped {
			// The source already stores the v8 shape; a second grouping would
			// nest groups inside groups.
			return entries, true
		}
		if provider == "openai-compatibility" {
			group := make(map[string]any, len(entry))
			for key, item := range entry {
				group[key] = item
			}
			group["keys"] = legacyOpenAICompatibilityKeys(entry)
			delete(group, "api-key-entries")
			grouped = append(grouped, group)
			continue
		}
		group := map[string]any{"name": fmt.Sprintf("%s-%d", provider, index+1)}
		key := make(map[string]any, len(entry))
		for field, item := range entry {
			if field == "base-url" || cpaSharedKeyFields[field] {
				group[field] = item
				continue
			}
			key[field] = item
		}
		group["keys"] = []any{key}
		grouped = append(grouped, group)
	}
	return grouped, true
}

// legacyOpenAICompatibilityKeys moves one v7 `api-key-entries` list into the v8
// group's `keys`. An entry without the list becomes a group without keys, which
// is the same shape CPA produces.
func legacyOpenAICompatibilityKeys(entry map[string]any) []any {
	raw, ok := entry["api-key-entries"]
	if !ok {
		return []any{}
	}
	entries, ok := raw.([]any)
	if !ok {
		return []any{}
	}
	keys := make([]any, 0, len(entries))
	for _, item := range entries {
		if mapping, ok := item.(map[string]any); ok {
			clean := make(map[string]any, len(mapping))
			for key, value := range mapping {
				if key == "auth_index" || key == "auth-index" {
					continue
				}
				clean[key] = value
			}
			keys = append(keys, clean)
			continue
		}
		keys = append(keys, item)
	}
	return keys
}

// cpaSharedKeyFields are the fields v8 lifts from a legacy entry to its group
// (config_v8.go:350-353).
var cpaSharedKeyFields = map[string]bool{
	"priority": true, "prefix": true, "proxy-url": true, "headers": true,
	"models": true, "excluded-models": true, "disable-cooling": true,
	"request-retry": true, "request-scoped-errors": true,
}

// documentPresence is the three-state presence a source path can have.
type documentPresence int

const (
	documentMissing documentPresence = iota
	documentPresent
	documentNull
)

// lookupDocumentPath resolves one dotted path inside the source document.
func lookupDocumentPath(document map[string]any, path string) (any, documentPresence) {
	var current any = document
	for _, part := range strings.Split(path, ".") {
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil, documentMissing
		}
		next, ok := mapping[part]
		if !ok {
			return nil, documentMissing
		}
		current = next
	}
	if current == nil {
		return nil, documentNull
	}
	return current, documentPresent
}

// lookupFirst resolves the first path that is present.
func lookupFirst(document map[string]any, paths ...string) (any, bool) {
	for _, path := range paths {
		if value, presence := lookupDocumentPath(document, path); presence == documentPresent {
			return value, true
		}
	}
	return nil, false
}

// setDocumentPath writes one dotted path, creating the mappings above it.
func setDocumentPath(document map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	current := document
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

// collectDocumentLeaves lists every leaf path below one value.
func collectDocumentLeaves(prefix string, value any, out map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 {
			out[prefix] = true
			return
		}
		for key, item := range typed {
			collectDocumentLeaves(prefix+"."+key, item, out)
		}
	case []any:
		out[prefix] = true
	default:
		out[prefix] = true
	}
}

// firstSegment returns the first dotted segment of a path.
func firstSegment(path string) string {
	segment, _, _ := strings.Cut(path, ".")
	return segment
}

// sectionProvider is the provider name of one api-keys group path.
func sectionProvider(current string) string {
	provider, ok := strings.CutPrefix(current, "api-keys.")
	if !ok {
		return ""
	}
	return provider
}

// stringList returns the string entries of one decoded list, in order.
func stringList(value any) []string {
	entries, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	return out
}

// digestBytes is the lower-case SHA-256 of one byte slice. It is the only thing
// this file reports about credential or configuration bytes.
func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
