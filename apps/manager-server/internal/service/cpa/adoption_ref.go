package cpa

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"gopkg.in/yaml.v3"
)

// This file is the single adoption classification of the product: the reference
// one adoption item is gated under, the set of references a discovered root
// contributes, and the item universe that binds them. It is one file on purpose,
// and the consumer implementation digest pins it
// (`freshstrategy/production.go`), because a change here changes what an
// adoption copies.
//
// Two questions, two functions, and the second is derived from the first so they
// cannot disagree:
//
//   - AdoptionItemRef(item) is the reference of one copyable item. The executor
//     gates every write on it.
//   - AdoptionRootRefs(root) is the set of references items under one discovered
//     root carry. Discovery records a decision for every reference in that set,
//     so the record always covers everything the executor can copy out of that
//     root.
//
// A second hand-written table is what let the two drift apart: a root recorded as
// manual while its items were copied. Here a root's set is computed from the
// item universe and nothing else. A reference only appears in a root's set
// because some item under that root classifies as it, or — for a root with no
// enumerable item at all — because it is that root's own declared fallback.

// unknownSection is where the v8 reader puts every source key it does not name.
// It is a discovery identity, never an upstream key. `requests`, `multimedia`
// and `upstream` are not named sections, so the gateway configuration under them
// arrives under this root.
const unknownSection = "unknown_cpa_native_resource"

// The two item classes the executor records that have no enumerable path. One
// representative of each is in the universe, so the references they produce are
// grounded in a real item class rather than in a root table.
const (
	// CredentialItemClass is one discovered credential file. The executor records
	// `credential:<name>` (or the digest form for an overlong name); discovery
	// finds those files in the source's auth directory, which CPA spells
	// `oauth.auth-dir`, so the class roots at `oauth`.
	CredentialItemClass = "credential:<name>"
	// OffTableConfigItemClass is one source configuration key the reader did not
	// name. Such a key is outside the accepted gateway table by construction, so
	// it classifies as unknown configuration. The keys themselves are not
	// enumerable, which is why the record carries one `cpa-config-unknown`
	// decision for the root and the Adopt screen must list the off-table paths.
	OffTableConfigItemClass = unknownSection + ".<key>"
)

// credentialItemPrefixes are the item identity prefixes the executor uses for a
// credential file.
var credentialItemPrefixes = []string{"credential:", "credential-name-sha256:"}

// adoptionSectionNames are the stable category names a source read reports, in
// reader order. `AdoptionSectionNames` and `AdoptionRootOf` are the single copy
// of that list for the rest of the product.
var adoptionSectionNames = []string{
	"server", "management", "access", "oauth", "providers", "api-keys",
	"routing", "client", "logging", "usage", "plugins", "observability",
	unknownSection,
}

// AdoptionSectionNames returns the discovery inventory in reader order.
func AdoptionSectionNames() []string { return slices.Clone(adoptionSectionNames) }

// AdoptionRootOf is the discovery root of one item: the named section the source
// document's key belongs to, or unknownSection when the reader does not name it.
// It is the rule the source readers apply, expressed once, so the executor's
// item universe and the record cannot be talking about different documents.
func AdoptionRootOf(item string) string {
	for _, prefix := range credentialItemPrefixes {
		if strings.HasPrefix(item, prefix) {
			// Credentials are read from the source's auth directory.
			return "oauth"
		}
	}
	root, _, nested := strings.Cut(item, ".")
	if !nested {
		if slices.Contains(adoptionSectionNames, item) {
			return item
		}
		return unknownSection
	}
	if slices.Contains(adoptionSectionNames, root) {
		return root
	}
	return unknownSection
}

// adoptionRootFallbacks is the single reference a root contributes when it has
// no enumerable item at all. Such a root is still something the operator may have
// to redo by hand — an adopted source's `server` block, its plugin set, its
// provider catalogue — so it needs one decision of its own.
//
// A root that does have items gets no fallback: its set is exactly what its items
// classify as. That is what keeps a root from claiming manual work nothing stands
// behind — `client` is two gateway leaves, so it contributes routing
// configuration and nothing else.
var adoptionRootFallbacks = map[string]bootstrap.ResourceRef{
	"access":        bootstrap.AdoptionRefClientAPIKeys,
	"server":        bootstrap.AdoptionRefConfigServer,
	"management":    bootstrap.AdoptionRefConfigManagement,
	"logging":       bootstrap.AdoptionRefConfigLogging,
	"usage":         bootstrap.AdoptionRefConfigUsage,
	"plugins":       bootstrap.AdoptionRefConfigPlugins,
	"observability": bootstrap.AdoptionRefConfigObservability,
	"providers":     bootstrap.AdoptionRefProviderStateProviders,
}

// adoptionProviderSections is every whole section the adoption copies, in page
// order: the eight `api-keys.*` provider groups and the four `oauth.*` maps.
var adoptionProviderSections = []string{
	"api-keys.gemini",
	"api-keys.interactions",
	"api-keys.vertex",
	"api-keys.codex",
	"api-keys.claude",
	"api-keys.xai",
	"api-keys.meta",
	"api-keys.openai-compatibility",
	"oauth.model-alias",
	"oauth.excluded-models",
	"oauth.settings",
	"oauth.request-scoped-errors",
}

// adoptionProviderCopyRoots is the reference each copyable section root
// contributes: api-keys and oauth sections are provider configuration and belong
// to `provider-state-api-keys`, the same decision as the per-provider key groups.
var adoptionProviderCopyRoots = map[string]bootstrap.ResourceRef{
	"api-keys": bootstrap.AdoptionRefProviderStateAPIKeys,
	"oauth":    bootstrap.AdoptionRefProviderStateAPIKeys,
}

// ProviderAdoptionSections returns the copyable provider sections in page order.
func ProviderAdoptionSections() []string { return slices.Clone(adoptionProviderSections) }

// adoptionProviderSectionRefs resolves each copyable section to the reference it
// is gated under: its own root's copy reference, never another root's. It is
// computed from the list above rather than written twice.
var adoptionProviderSectionRefs = func() map[string]bootstrap.ResourceRef {
	refs := make(map[string]bootstrap.ResourceRef, len(adoptionProviderSections))
	for _, section := range adoptionProviderSections {
		ref, ok := adoptionProviderCopyRoots[AdoptionRootOf(section)]
		if !ok {
			panic("provider section without a copy reference: " + section)
		}
		refs[section] = ref
	}
	return refs
}()

// adoptionStructuredConfigSections lists structured whole-PUT CPA v8 business
// configuration sections outside api-keys/oauth maps. Each section is gated
// under AdoptionRefConfigRouting (`cpa-config-routing`).
var adoptionStructuredConfigSections = []string{
	"oauth.providers.codex.live-media-relay.ice-servers",
	"credentials.concurrency",
	"credentials.in-flight",
	"plugins.store-sources",
	"plugins.store-auth",
	"plugins.configs",
}

// StructuredConfigAdoptionSections returns the structured whole-PUT CPA v8
// business configuration sections in stable order.
func StructuredConfigAdoptionSections() []string {
	return slices.Clone(adoptionStructuredConfigSections)
}

var adoptionStructuredSectionRefs = func() map[string]bootstrap.ResourceRef {
	refs := make(map[string]bootstrap.ResourceRef, len(adoptionStructuredConfigSections))
	for _, section := range adoptionStructuredConfigSections {
		refs[section] = bootstrap.AdoptionRefConfigRouting
	}
	return refs
}()

// adoptionHostManagedSourceKeys maps host-exclusive configuration paths that
// share a named root (`server`, `plugins`, `observability`) with migrated
// business keys back to their platform-managed ResourceRef. They are never copied;
// keeping them in the universe ensures Discovery emits the root's platform
// reference alongside `cpa-config-routing`.
var adoptionHostManagedSourceKeys = map[string]bootstrap.ResourceRef{
	"server.server-id":                         bootstrap.AdoptionRefConfigServer,
	"server.host":                              bootstrap.AdoptionRefConfigServer,
	"server.port":                              bootstrap.AdoptionRefConfigServer,
	"server.tls":                               bootstrap.AdoptionRefConfigServer,
	"server.discovery":                         bootstrap.AdoptionRefConfigServer,
	"server.kv-store.path":                     bootstrap.AdoptionRefConfigServer,
	"plugins.enabled":                          bootstrap.AdoptionRefConfigPlugins,
	"plugins.dir":                              bootstrap.AdoptionRefConfigPlugins,
	"plugins.auth-revision":                    bootstrap.AdoptionRefConfigPlugins,
	"observability.usage":                      bootstrap.AdoptionRefConfigObservability,
	"observability.pprof":                      bootstrap.AdoptionRefConfigObservability,
	"observability.logs.logging-to-file":       bootstrap.AdoptionRefConfigObservability,
	"observability.logs.logs-max-total-size-mb": bootstrap.AdoptionRefConfigObservability,
	"observability.logs.error-logs-max-files":  bootstrap.AdoptionRefConfigObservability,
}

var adoptionHostManagedSourceKeyList = func() []string {
	keys := make([]string, 0, len(adoptionHostManagedSourceKeys))
	for key := range adoptionHostManagedSourceKeys {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}()
// adoptionFamilyPaths is the set of payload rule family paths. A family is a
// configuration path like any other (`requests.payload.default`), so the same
// path membership test classifies it.
var adoptionFamilyPaths = func() map[string]bool {
	paths := make(map[string]bool, len(ConfigRuleFamilyPaths()))
	for _, path := range ConfigRuleFamilyPaths() {
		paths[path] = true
	}
	return paths
}()

// AdoptionItemRef is the stable adoption reference of one copyable item: a
// gateway configuration path, a payload rule family (by path or by identifier), a
// whole provider section, a credential file, or a configuration key the reader
// did not name. It is the single classification of the product and the reference
// the operation record's decision is looked up under.
//
// A leaf or rule family the accepted gateway table names is `cpa-config-routing`
// — that table is the exact scope the gateway configuration page reads and
// writes against CPA v8.0.13. Everything else keeps `cpa-config-unknown` and
// therefore stays manual: the executor never widens a scope by classifying an
// item into a migrate verdict the record was not given.
func AdoptionItemRef(item string) bootstrap.ResourceRef {
	for _, prefix := range credentialItemPrefixes {
		if strings.HasPrefix(item, prefix) {
			return bootstrap.AdoptionRefCPAAuthFiles
		}
	}
	if ref, ok := adoptionProviderSectionRefs[item]; ok {
		return ref
	}
	if ref, ok := adoptionStructuredSectionRefs[item]; ok {
		return ref
	}
	if _, ok := LookupConfigLeaf(item); ok {
		return bootstrap.AdoptionRefConfigRouting
	}
	if adoptionFamilyPaths[item] {
		return bootstrap.AdoptionRefConfigRouting
	}
	if _, ok := LookupConfigRuleFamily(item); ok {
		// The executor records a family item by its identifier as well as by its
		// path, so both spellings classify.
		return bootstrap.AdoptionRefConfigRouting
	}
	if ref, ok := adoptionHostManagedSourceKeys[item]; ok {
		return ref
	}
	return bootstrap.AdoptionRefConfigUnknown
}

// AdoptionItemUniverse returns every item the adoption can enumerate: each
// gateway leaf, each payload rule family path, each copyable provider and
// structured section, host-managed keys under shared roots, and one
// representative of each item class that has no enumerable path.
//
// The executor walks the same three tables and records items of the same two
// classes; the invariant tests and the pin walk this, so a widened table is
// covered without a second inventory.
func AdoptionItemUniverse() []string {
	items := make([]string, 0, len(ConfigLeafPaths())+len(ConfigRuleFamilyPaths())+len(adoptionProviderSections)+len(adoptionStructuredConfigSections)+len(adoptionHostManagedSourceKeyList)+2)
	items = append(items, ConfigLeafPaths()...)
	items = append(items, ConfigRuleFamilyPaths()...)
	items = append(items, adoptionProviderSections...)
	items = append(items, adoptionStructuredConfigSections...)
	items = append(items, adoptionHostManagedSourceKeyList...)
	items = append(items, CredentialItemClass, OffTableConfigItemClass)
	return items
}

// AdoptionRootRefs is the set of references items under one discovered root
// carry, in a stable order. Discovery records one decision per reference in this
// set, which is what makes the record cover every item the executor can copy from
// that root.
//
// The set is derived: the classifier's answer for every universe item that lands
// in the root. A root with no universe item contributes only its declared
// fallback, so no root claims manual work that nothing stands behind.
func AdoptionRootRefs(root string) []bootstrap.ResourceRef {
	return slices.Clone(rootRefIndex()[root])
}

// rootRefIndex derives every root's set once and reuses it: the tables it reads
// are immutable at run time.
var rootRefIndex = sync.OnceValue(deriveRootRefIndex)

func deriveRootRefIndex() map[string][]bootstrap.ResourceRef {
	index := make(map[string]map[bootstrap.ResourceRef]bool, len(adoptionSectionNames))
	add := func(root string, refs ...bootstrap.ResourceRef) {
		if index[root] == nil {
			index[root] = map[bootstrap.ResourceRef]bool{}
		}
		for _, ref := range refs {
			index[root][ref] = true
		}
	}
	for _, item := range AdoptionItemUniverse() {
		add(AdoptionRootOf(item), AdoptionItemRef(item))
	}
	for root, fallback := range adoptionRootFallbacks {
		if len(index[root]) == 0 {
			add(root, fallback)
		}
	}
	ordered := make(map[string][]bootstrap.ResourceRef, len(index))
	for root, refs := range index {
		list := make([]bootstrap.ResourceRef, 0, len(refs))
		for ref := range refs {
			list = append(list, ref)
		}
		slices.Sort(list)
		ordered[root] = list
	}
	return ordered
}

// AdoptionUniverseDigest is the digest of the adoption item universe and the
// reference each item classifies as: the sorted (item, reference) pairs.
//
// It is folded into the safe-read implementation digest, because
// `AdoptionItemRef` decides its routing verdict from the gateway page table
// (`config_schema.go`), which is not itself pinned. Adding a gateway leaf or
// changing a classification therefore changes the consumer digest and
// invalidates an old acceptance, without coupling every gateway-page edit to the
// adoption pin through a whole-file hash.
func AdoptionUniverseDigest(items ...string) string {
	if len(items) == 0 {
		items = AdoptionItemUniverse()
	}
	pairs := make([]string, 0, len(items))
	for _, item := range items {
		pairs = append(pairs, item+"\x00"+string(AdoptionItemRef(item)))
	}
	slices.Sort(pairs)
	hash := sha256.New()
	for _, pair := range pairs {
		_, _ = hash.Write([]byte(pair))
		_, _ = hash.Write([]byte("\n"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type v8ConfigSchemaIndex struct {
	leaves  map[string]bool
	wholes  map[string]bool
	parents map[string]bool
}

var knownV8ConfigSchema = sync.OnceValue(func() v8ConfigSchemaIndex {
	leaves := make(map[string]bool, len(ConfigLeafPaths()))
	for _, path := range ConfigLeafPaths() {
		leaves[path] = true
	}
	wholes := map[string]bool{
		"config-version":  true,
		"access.api-keys": true,
		"oauth.auth-dir":  true,
		"management":      true,
		"providers":       true,
		"logging":         true,
		"usage":           true,
	}
	for key := range adoptionHostManagedSourceKeys {
		wholes[key] = true
	}
	for _, path := range ConfigRuleFamilyPaths() {
		wholes[path] = true
	}
	for _, section := range adoptionProviderSections {
		wholes[section] = true
	}
	for _, section := range adoptionStructuredConfigSections {
		wholes[section] = true
	}
	parents := make(map[string]bool)
	addParents := func(path string) {
		parts := strings.Split(path, ".")
		for i := 1; i < len(parts); i++ {
			parents[strings.Join(parts[:i], ".")] = true
		}
	}
	for path := range leaves {
		addParents(path)
	}
	for path := range wholes {
		addParents(path)
	}
	return v8ConfigSchemaIndex{leaves: leaves, wholes: wholes, parents: parents}
})

// archivedV8YAMLScanKeyPrefix reserves NUL-prefixed in-memory map keys used to
// carry YAML archive scan state on a decoded v8 config document. A NUL-prefixed
// key cannot exist in JSON or YAML config objects, and UnknownV8ConfigPaths skips
// all keys carrying this prefix at top level.
const (
	archivedV8YAMLScanKeyPrefix     = "\x00cpamp_"
	archivedV8YAMLScanCandidatesKey = "\x00cpamp_archived_unknown_v8_paths"
	archivedV8YAMLScanIncompleteKey = "\x00cpamp_archived_yaml_scan_incomplete"
)

// V8UnknownConfigScan separates definite JSON-visible unknown configuration
// paths from heuristic trailing YAML archive comment candidates and incomplete
// YAML scan state.
type V8UnknownConfigScan struct {
	JSONPaths             []string
	ArchiveCandidates     []string
	ArchiveScanIncomplete bool
}

// HasAny reports whether the source carries definite JSON unknown keys,
// possible archived/commented YAML unknown candidates, or an incomplete YAML
// archive scan that must fail closed as a manual review item.
func (s V8UnknownConfigScan) HasAny() bool {
	return len(s.JSONPaths) > 0 || len(s.ArchiveCandidates) > 0 || s.ArchiveScanIncomplete
}

// AttachV8YAMLArchiveScan attaches the YAML archive scan result from
// GET /v8/management/config.yaml to doc using reserved NUL-prefixed carrier keys
// so InspectV8UnknownConfig can read both definite JSON unknowns and heuristic/
// incomplete YAML scan state without polluting UnknownV8ConfigPaths.
func AttachV8YAMLArchiveScan(doc map[string]any, candidates []string, complete bool) {
	if doc == nil {
		return
	}
	cleaned := normalizeV8ArchiveCandidates(candidates)
	if len(cleaned) > 0 {
		doc[archivedV8YAMLScanCandidatesKey] = cleaned
	} else {
		delete(doc, archivedV8YAMLScanCandidatesKey)
	}
	if !complete {
		doc[archivedV8YAMLScanIncompleteKey] = true
	} else {
		delete(doc, archivedV8YAMLScanIncompleteKey)
	}
}

// InspectV8UnknownConfig returns the combined definite JSON unknown paths and
// attached YAML archive scan state from doc.
func InspectV8UnknownConfig(doc map[string]any) V8UnknownConfigScan {
	if len(doc) == 0 {
		return V8UnknownConfigScan{}
	}
	incomplete, _ := doc[archivedV8YAMLScanIncompleteKey].(bool)
	return V8UnknownConfigScan{
		JSONPaths:             UnknownV8ConfigPaths(doc),
		ArchiveCandidates:     attachedV8YAMLArchiveCandidates(doc),
		ArchiveScanIncomplete: incomplete,
	}
}

func normalizeV8ArchiveCandidates(candidates []string) []string {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate != "" {
			out = append(out, candidate)
		}
	}
	if len(out) == 0 {
		return nil
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func attachedV8YAMLArchiveCandidates(doc map[string]any) []string {
	if doc == nil {
		return nil
	}
	raw, ok := doc[archivedV8YAMLScanCandidatesKey]
	if !ok || raw == nil {
		return nil
	}
	switch typed := raw.(type) {
	case []string:
		return normalizeV8ArchiveCandidates(typed)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return normalizeV8ArchiveCandidates(out)
	default:
		return nil
	}
}

// ScanV8YAMLArchiveCandidates inspects the trailing root.FootComment archive
// block of GET /v8/management/config.yaml for possible CPA-archived or
// operator-commented `# <dotted.section>: ...` entries after the last active YAML
// line of the first YAML document (before `...` or any subsequent `---`).
//
// complete is true only when projectedV8YAML is valid UTF-8, non-empty, and its
// first YAML document decodes cleanly as a mapping node. Heuristic trailing
// comment matches are returned as candidates rather than exact active key
// counts, because operator comments in `# path: value` form are syntactically
// indistinguishable from CPA archive blocks.
func ScanV8YAMLArchiveCandidates(projectedV8YAML []byte) (candidates []string, complete bool) {
	if !utf8.Valid(projectedV8YAML) {
		return nil, false
	}
	raw := bytes.TrimPrefix(projectedV8YAML, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	normalized := strings.ReplaceAll(string(raw), "\r\n", "\n")
	dec := yaml.NewDecoder(strings.NewReader(normalized))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0] == nil || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false
	}
	lines := strings.Split(normalized, "\n")
	maxLine := maxYAMLNodeLine(doc.Content[0])
	firstDocEnd := len(lines)
	for i, line := range lines {
		if i+1 <= maxLine {
			continue
		}
		if yamlDocumentMarker(line, "...") || yamlDocumentMarker(line, "---") {
			firstDocEnd = i
			break
		}
	}
	firstDocLines := lines[:firstDocEnd]
	tailStart := len(firstDocLines)
	for i := len(firstDocLines) - 1; i >= 0; i-- {
		line := strings.TrimRight(firstDocLines[i], "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			tailStart = i
			continue
		}
		break
	}
	if tailStart >= len(firstDocLines) {
		return nil, true
	}
	trailing := firstDocLines[tailStart:]
	var found []string
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		blockText := strings.Join(cur, "\n")
		cur = nil
		if section, ok := decodeArchivedYAMLMappingSection(blockText); ok {
			found = append(found, section)
		}
	}
	for idx, rawLine := range trailing {
		line := strings.TrimRight(rawLine, "\r")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if !strings.HasPrefix(line, "#") {
			flush()
			continue
		}
		if isBlankArchiveCommentLine(line) {
			if len(cur) > 0 && hasArchiveContinuationAhead(trailing, idx+1) {
				cur = append(cur, "")
			} else {
				flush()
			}
			continue
		}
		if isArchiveCommentContinuationLine(line) {
			if len(cur) > 0 {
				cur = append(cur, stripArchiveCommentPrefix(line))
			}
			continue
		}
		if len(cur) > 0 && strings.HasPrefix(cur[0], "? ") && !strings.HasPrefix(line, "# ? ") {
			if _, complete := decodeArchivedYAMLMappingSection(strings.Join(cur, "\n")); !complete {
				cur = append(cur, stripArchiveCommentPrefix(line))
				continue
			}
		}
		flush()
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		bodyLine := line[2:]
		if strings.HasPrefix(bodyLine, "#") {
			continue
		}
		cur = []string{bodyLine}
	}
	flush()
	if len(found) == 0 {
		return nil, true
	}
	slices.Sort(found)
	return slices.Compact(found), true
}

func maxYAMLNodeLine(node *yaml.Node) int {
	if node == nil {
		return 0
	}
	maxLine := node.Line
	for _, child := range node.Content {
		if childLine := maxYAMLNodeLine(child); childLine > maxLine {
			maxLine = childLine
		}
	}
	return maxLine
}

func yamlDocumentMarker(line, marker string) bool {
	line = strings.TrimRight(line, "\r")
	if !strings.HasPrefix(line, marker) {
		return false
	}
	if len(line) == len(marker) {
		return true
	}
	switch line[len(marker)] {
	case ' ', '\t':
		return true
	default:
		return false
	}
}

func isBlankArchiveCommentLine(line string) bool {
	return strings.TrimSpace(strings.TrimPrefix(line, "#")) == ""
}

func isArchiveCommentContinuationLine(line string) bool {
	if strings.HasPrefix(line, "#  ") || strings.HasPrefix(line, "# \t") || strings.HasPrefix(line, "#\t") {
		return true
	}
	if line == "# -" || strings.HasPrefix(line, "# - ") || strings.HasPrefix(line, "# -\t") {
		return true
	}
	if line == "# :" || strings.HasPrefix(line, "# : ") || strings.HasPrefix(line, "# :\t") {
		return true
	}
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, "#"))
	return trimmed == "]" || trimmed == "}" || strings.HasPrefix(trimmed, "],") || strings.HasPrefix(trimmed, "},")
}

func hasArchiveContinuationAhead(lines []string, start int) bool {
	for i := start; i < len(lines); i++ {
		next := strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(next) == "" || !strings.HasPrefix(next, "#") {
			return false
		}
		if isBlankArchiveCommentLine(next) {
			continue
		}
		return isArchiveCommentContinuationLine(next)
	}
	return false
}

func stripArchiveCommentPrefix(line string) string {
	if strings.HasPrefix(line, "# ") {
		return line[2:]
	}
	return strings.TrimPrefix(line, "#")
}

func decodeArchivedYAMLMappingSection(blockText string) (string, bool) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(blockText), &doc); err != nil {
		return "", false
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0] == nil {
		return "", false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode || len(root.Content) != 2 {
		return "", false
	}
	keyNode := root.Content[0]
	if keyNode == nil || keyNode.Kind != yaml.ScalarNode {
		return "", false
	}
	if keyNode.ShortTag() == "!!null" && keyNode.Style == 0 {
		return "", false
	}
	if keyNode.Value == "" {
		return "<empty>", true
	}
	return keyNode.Value, true
}

// UnknownV8ConfigPaths returns the sorted dotted paths of definite active
// unrecognized non-nil configuration entries in a decoded CPA v8 JSON config
// document (including off-table sibling keys nested under named roots such as
// server, plugins, oauth, routing, or observability). Reserved in-memory scan
// carrier keys prefixed with `\x00cpamp_` are ignored.
func UnknownV8ConfigPaths(doc map[string]any) []string {
	if len(doc) == 0 {
		return nil
	}
	index := knownV8ConfigSchema()
	var unknown []string
	var walk func(prefix string, obj map[string]any)
	walk = func(prefix string, obj map[string]any) {
		for key, val := range obj {
			if prefix == "" && strings.HasPrefix(key, archivedV8YAMLScanKeyPrefix) {
				continue
			}
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if index.leaves[path] || index.wholes[path] {
				continue
			}
			if index.parents[path] {
				if val == nil {
					continue
				}
				if child, ok := val.(map[string]any); ok {
					walk(path, child)
				} else {
					unknown = append(unknown, path)
				}
				continue
			}
			if val != nil {
				unknown = append(unknown, path)
			}
		}
	}
	walk("", doc)
	if len(unknown) == 0 {
		return nil
	}
	slices.Sort(unknown)
	return slices.Compact(unknown)
}
