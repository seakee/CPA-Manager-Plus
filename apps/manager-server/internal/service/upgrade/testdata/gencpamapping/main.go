// Command gencpamapping regenerates the independent upstream oracle the CPA
// import's mapping test reads.
//
// The test that consumes the fixture must be able to fail when the mapping table
// is wrong without comparing the table to a second copy of itself. This command
// derives the expected answers from the upstream releases instead:
//
//   - the v8 configuration alias table (internal/config/config_v8.go at the
//     pinned v8 commit): every legacy spelling, the canonical path it loads as,
//     and the eight `*-api-key` provider families;
//   - every v7 release's `Config` struct graph, walked from the yaml tags of the
//     structs the package declares, which gives the exact full path and the
//     release each path first appears in. The same walk over the v8 anchors
//     locates a path that was removed after the last v7 release.
//
// The output is checked in as testdata/cpa_mapping_upstream.json. It is not
// generated at test time: CI has no upstream checkout and no network. To
// regenerate it:
//
//	go run ./internal/service/upgrade/testdata/gencpamapping \
//	  -upstream /path/to/CLIProxyAPI-checkout \
//	  -tags /path/to/tag-commits.txt \
//	  -out internal/service/upgrade/testdata/cpa_mapping_upstream.json
//
// `-tags` is optional; it fills in tags this checkout does not carry (a shallow
// clone may hold the objects without the tag refs). Every tag/commit pair the
// fixture records is provenance, so a reviewer can reproduce the same answer
// from the same commits.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

const (
	// v8AliasRef is the release the alias table is read at. It is the release
	// the destination's own gateway table documents (see
	// internal/service/cpa/config_schema.go), so an alias answer here is the one
	// the destination's surface was verified against.
	v8AliasRef = "v8.0.13"
	// v8AliasFile is the file that declares the alias tables.
	v8AliasFile = "internal/config/config_v8.go"
	// configDirectory is the package whose struct yaml tags define the full
	// configuration paths a release accepts.
	configDirectory = "internal/config"
)

// fixture is the checked-in oracle.
type fixture struct {
	Contract string `json:"contract"`
	Upstream struct {
		Repository   string `json:"repository"`
		ScanCheckout string `json:"scanCheckout"`
		Generator    string `json:"generator"`
		Regenerate   string `json:"regenerate"`
	} `json:"upstream"`
	Sources []source `json:"sources"`
	// Versions is the scan order, oldest first. The presence transitions are
	// only meaningful relative to it.
	Versions []string `json:"versions"`
	// TagCommits pins every tag the scan read to the commit it resolved to.
	TagCommits map[string]string `json:"tagCommits"`
	// Families are the eight `*-api-key` spellings v8 groups into `api-keys.*`.
	Families []aliasEntry `json:"families"`
	// Paths are the exact legacy path aliases and the prefix rewrites v8 applies.
	Paths []aliasEntry `json:"paths"`
	// Presence is the release each full configuration path first appears in and,
	// for a path a later release dropped, the release it disappeared in.
	Presence map[string]presence `json:"presence"`
}

type source struct {
	Role   string `json:"role"`
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
}

type aliasEntry struct {
	Legacy  string `json:"legacy"`
	Current string `json:"current"`
	// Kind is "leaf" for an exact path alias and "prefix" for a rewrite v8
	// applies to the path and everything below it. Families carry their grouped
	// name in Current and no kind.
	Kind string `json:"kind,omitempty"`
}

type presence struct {
	First   string `json:"first"`
	Removed string `json:"removed,omitempty"`
}

func main() {
	upstream := flag.String("upstream", "", "path to the CLIProxyAPI checkout to read")
	tagsFile := flag.String("tags", "", "optional file of `<tag> <commit>` lines for tags the checkout does not carry")
	out := flag.String("out", "", "fixture path to write")
	flag.Parse()
	if strings.TrimSpace(*upstream) == "" || strings.TrimSpace(*out) == "" {
		fmt.Fprintln(os.Stderr, "usage: gencpamapping -upstream <checkout> [-tags <file>] -out <fixture>")
		os.Exit(2)
	}
	if err := run(*upstream, *tagsFile, *out); err != nil {
		fmt.Fprintln(os.Stderr, "gencpamapping:", err)
		os.Exit(1)
	}
}

func run(upstream, tagsFile, out string) error {
	git := func(args ...string) (string, error) {
		command := exec.Command("git", append([]string{"-C", upstream}, args...)...)
		output, err := command.Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return string(output), nil
	}

	refs, err := resolveTags(git, tagsFile)
	if err != nil {
		return err
	}
	versions := make([]string, 0, len(refs))
	for tag := range refs {
		versions = append(versions, tag)
	}
	sort.Slice(versions, func(i, j int) bool { return lessVersion(versions[i], versions[j]) })

	built := fixture{
		Contract:   "cpamp.cpa-mapping-upstream.v1",
		Versions:   versions,
		TagCommits: map[string]string{},
		Presence:   map[string]presence{},
	}
	built.Upstream.Repository = "https://github.com/router-for-me/CLIProxyAPI"
	built.Upstream.ScanCheckout = upstream
	built.Upstream.Generator = "internal/service/upgrade/testdata/gencpamapping"
	built.Upstream.Regenerate = "cd apps/manager-server && go run ./internal/service/upgrade/testdata/gencpamapping -upstream <checkout> [-tags <tag-commits.txt>] -out internal/service/upgrade/testdata/cpa_mapping_upstream.json"

	aliasSource, err := git("show", refs[v8AliasRef]+":"+v8AliasFile)
	if err != nil {
		return fmt.Errorf("read the alias table at %s: %w", v8AliasRef, err)
	}
	families, paths, err := parseAliasTables(aliasSource)
	if err != nil {
		return err
	}
	built.Families = families
	built.Paths = paths
	digest := sha256.Sum256([]byte(aliasSource))
	built.Sources = append(built.Sources, source{
		Role: "v8-alias-table", Ref: v8AliasRef, Commit: refs[v8AliasRef],
		Path: v8AliasFile, SHA256: hex.EncodeToString(digest[:]),
	})
	built.Sources = append(built.Sources, source{
		Role: "config-struct-yaml-tags", Ref: v7v8Range(versions),
		Path: configDirectory + "/*.go (non-test)",
	})

	// Walk every version in order and record the full path set it accepts.
	seen := map[string]bool{}
	for _, tag := range versions {
		built.TagCommits[tag] = refs[tag]
		pathsPresent, err := configPathsAt(git, refs[tag])
		if err != nil {
			return fmt.Errorf("walk the configuration at %s: %w", tag, err)
		}
		for path := range pathsPresent {
			seen[path] = true
			entry := built.Presence[path]
			if entry.First == "" {
				entry.First = tag
			}
			built.Presence[path] = entry
		}
		for path := range built.Presence {
			if pathsPresent[path] {
				continue
			}
			entry := built.Presence[path]
			if entry.Removed == "" {
				entry.Removed = tag
				built.Presence[path] = entry
			}
		}
	}
	// Only paths that appeared at all belong in the fixture; a path absent from
	// every version never contributes an assertion.
	for path := range built.Presence {
		if !seen[path] {
			delete(built.Presence, path)
		}
	}

	encoded, err := json.MarshalIndent(built, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return os.WriteFile(out, encoded, 0o644)
}

// resolveTags maps the tags the scan reads to commits, using the checkout's own
// refs and then the optional manifest for the tags it does not carry.
func resolveTags(git func(...string) (string, error), tagsFile string) (map[string]string, error) {
	refs := map[string]string{}
	for _, pattern := range []string{"v7.*", "v8.*"} {
		output, err := git("tag", "-l", pattern)
		if err != nil {
			return nil, err
		}
		for _, tag := range strings.Fields(output) {
			commit, err := git("rev-parse", tag+"^{commit}")
			if err != nil {
				return nil, err
			}
			refs[tag] = strings.TrimSpace(commit)
		}
	}
	if strings.TrimSpace(tagsFile) == "" {
		return refs, nil
	}
	data, err := os.ReadFile(tagsFile)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[0], "v") {
			continue
		}
		if _, exists := refs[fields[0]]; exists {
			continue
		}
		// The manifest names a commit; the object must exist in the checkout.
		if _, err := git("cat-file", "-t", fields[1]); err != nil {
			return nil, fmt.Errorf("tag %s names commit %s, which this checkout does not hold", fields[0], fields[1])
		}
		refs[fields[0]] = fields[1]
	}
	return refs, nil
}

// v7v8Range names the release range the struct scan covers.
func v7v8Range(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	return versions[0] + ".." + versions[len(versions)-1]
}

// lessVersion orders the scanned tags: v7 before v8, then numerically by the
// release parts. A pre-release suffix is ignored for ordering.
func lessVersion(left, right string) bool {
	a := versionParts(left)
	b := versionParts(right)
	for index := range 3 {
		if a[index] != b[index] {
			return a[index] < b[index]
		}
	}
	return left < right
}

func versionParts(tag string) [3]int {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(tag, "v"), "V")
	trimmed, _, _ = strings.Cut(trimmed, "-")
	trimmed, _, _ = strings.Cut(trimmed, "+")
	parts := strings.Split(trimmed, ".")
	out := [3]int{}
	for index := 0; index < len(parts) && index < 3; index++ {
		out[index], _ = strconv.Atoi(parts[index])
	}
	return out
}

// parseAliasTables reads the four alias declarations of config_v8.go. They are
// literal tables of {old, current} pairs, so a shape-aware read of the literal
// is all the derivation needs.
func parseAliasTables(source string) ([]aliasEntry, []aliasEntry, error) {
	families, err := literalPairs(source, "var v8KeyFamilies")
	if err != nil {
		return nil, nil, err
	}
	entries := make([]aliasEntry, 0, len(families))
	for _, pair := range families {
		entries = append(entries, aliasEntry{Legacy: pair[0], Current: pair[1]})
	}
	paths := []aliasEntry{}
	for _, block := range []string{"var v8ClientPaths", "var v8SharedPaths"} {
		pairs, err := literalPairs(source, block)
		if err != nil {
			return nil, nil, err
		}
		for _, pair := range pairs {
			paths = append(paths, aliasEntry{Legacy: pair[0], Current: pair[1], Kind: "leaf"})
		}
	}
	prefixes, err := literalPairs(source, "prefixes := []configPath")
	if err != nil {
		return nil, nil, err
	}
	for _, pair := range prefixes {
		paths = append(paths, aliasEntry{Legacy: pair[0], Current: pair[1], Kind: "prefix"})
	}
	if len(entries) != 8 || len(paths) == 0 {
		return nil, nil, fmt.Errorf("the alias table read %d families and %d paths, which is not the v8 table", len(entries), len(paths))
	}
	return entries, paths, nil
}

// literalPairs extracts the string pairs of one literal slice declaration.
func literalPairs(source, marker string) ([][2]string, error) {
	start := strings.Index(source, marker)
	if start < 0 {
		return nil, fmt.Errorf("the alias table has no %q declaration", marker)
	}
	// The declaration closes at the first line that is only a brace, whatever
	// its indentation: a package-level table and a function-local one differ
	// there, and cutting on a fixed indentation would read past a table.
	lines := strings.Split(source[start:], "\n")
	body := ""
	for _, line := range lines {
		if strings.TrimSpace(line) == "}" {
			break
		}
		body += line + "\n"
	}
	pairs := [][2]string{}
	for _, chunk := range strings.Split(body, "{")[1:] {
		chunk, _, _ = strings.Cut(chunk, "}")
		quoted := strings.Split(chunk, "\"")
		if len(quoted) < 4 {
			continue
		}
		pairs = append(pairs, [2]string{strings.TrimSpace(quoted[1]), strings.TrimSpace(quoted[3])})
	}
	return pairs, nil
}

// configPathsAt returns every configuration path one release accepts: each leaf
// reached from `Config` through the package's struct declarations, plus every
// intermediate struct path so a section-level claim can be checked too.
func configPathsAt(git func(...string) (string, error), commit string) (map[string]bool, error) {
	listing, err := git("ls-tree", "-r", "--name-only", commit, configDirectory+"/")
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, name := range strings.Split(listing, "\n") {
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := git("show", commit+":"+name)
		if err != nil {
			return nil, err
		}
		files[name] = []byte(body)
	}
	return walkConfigPaths(files)
}

// walkConfigPaths builds the struct graph of one package snapshot and walks it
// from the root `Config` type.
func walkConfigPaths(files map[string][]byte) (map[string]bool, error) {
	structs := map[string]*ast.StructType{}
	set := token.NewFileSet()
	for name, body := range files {
		parsed, err := parser.ParseFile(set, name, body, 0)
		if err != nil {
			return nil, err
		}
		for _, declaration := range parsed.Decls {
			generic, ok := declaration.(*ast.GenDecl)
			if !ok || generic.Tok != token.TYPE {
				continue
			}
			for _, spec := range generic.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if structure, ok := typeSpec.Type.(*ast.StructType); ok {
					structs[typeSpec.Name.Name] = structure
				}
			}
		}
	}
	root, ok := structs["Config"]
	if !ok {
		return nil, fmt.Errorf("the release declares no Config struct in %s", configDirectory)
	}
	out := map[string]bool{}
	var walk func(structure *ast.StructType, prefix string, depth int)
	walk = func(structure *ast.StructType, prefix string, depth int) {
		if structure == nil || depth > 12 {
			return
		}
		if prefix != "" {
			out[prefix] = true
		}
		for _, field := range structure.Fields.List {
			name, inline, ok := yamlField(field)
			if !ok {
				continue
			}
			if inline {
				// An embedded struct contributes its fields at this prefix, which
				// is what `yaml:",inline"` means to the decoder.
				if nested, ok := structs[typeName(field.Type)]; ok {
					walk(nested, prefix, depth+1)
				}
				continue
			}
			next := name
			if prefix != "" {
				next = prefix + "." + name
			}
			out[next] = true
			if nested, ok := structs[typeName(field.Type)]; ok {
				walk(nested, next, depth+1)
			}
		}
	}
	walk(root, "", 0)
	return out, nil
}

// yamlField returns the yaml name of one field. inline reports an embedded
// struct whose fields belong at the parent's level (`yaml:",inline"` or no tag
// at all), and ok is false for a field the format does not read.
func yamlField(field *ast.Field) (name string, inline bool, ok bool) {
	if field.Tag == nil {
		return "", len(field.Names) == 0, len(field.Names) == 0
	}
	raw, err := strconv.Unquote(field.Tag.Value)
	if err != nil {
		return "", false, false
	}
	for _, part := range strings.Fields(raw) {
		value, found := strings.CutPrefix(part, `yaml:"`)
		if !found {
			continue
		}
		value, _, _ = strings.Cut(value, `"`)
		name, options, _ := strings.Cut(value, ",")
		if name == "-" {
			return "", false, false
		}
		if name == "" {
			return "", strings.Contains(options, "inline"), true
		}
		return name, false, true
	}
	return "", len(field.Names) == 0, len(field.Names) == 0
}

// typeName resolves the local type name a field is declared as, including a
// pointer, slice or map element that carries the struct.
func typeName(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return typeName(typed.X)
	case *ast.ArrayType:
		return typeName(typed.Elt)
	case *ast.MapType:
		return typeName(typed.Value)
	default:
		return ""
	}
}
