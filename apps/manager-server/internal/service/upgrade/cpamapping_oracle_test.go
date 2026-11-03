package upgrade

// This file is the independent oracle for the CPA import's mapping table.
//
// The earlier golden test compared the table with a second copy of the table: a
// wrong row and its golden line could be changed together and the suite stayed
// green. This test never copies the table. It reads a checked-in fixture that
// `testdata/gencpamapping` generated from the upstream releases:
//
//   - the v8 alias table says what each legacy spelling loads as, so a wrong
//     destination path fails here without any edit to this test;
//   - the per-release struct walk says which release first accepted each full
//     path and which release dropped it, so a wrong release claim fails here.
//
// The fixture records the upstream commits and file digest it was generated
// from, and the command that regenerates it. Regenerating it requires an
// upstream checkout; the test requires neither a checkout nor a network.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const (
	// cpaMappingFixturePath is the generated upstream oracle.
	cpaMappingFixturePath = "testdata/cpa_mapping_upstream.json"
	// cpaMappingFixtureContract is the fixture shape this test reads. A fixture
	// written for another shape is refused instead of read optimistically.
	cpaMappingFixtureContract = "cpamp.cpa-mapping-upstream.v1"
)

type cpaMappingFixture struct {
	Contract string `json:"contract"`
	Upstream struct {
		Repository string `json:"repository"`
		Generator  string `json:"generator"`
		Regenerate string `json:"regenerate"`
	} `json:"upstream"`
	Sources []struct {
		Role   string `json:"role"`
		Ref    string `json:"ref"`
		Commit string `json:"commit"`
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"sources"`
	Versions   []string                  `json:"versions"`
	TagCommits map[string]string         `json:"tagCommits"`
	Families   []cpaMappingAlias         `json:"families"`
	Paths      []cpaMappingAlias         `json:"paths"`
	Presence   map[string]cpaMappingSeen `json:"presence"`
}

type cpaMappingAlias struct {
	Legacy  string `json:"legacy"`
	Current string `json:"current"`
	Kind    string `json:"kind"`
}

type cpaMappingSeen struct {
	First   string `json:"first"`
	Removed string `json:"removed"`
}

// loadCPAMappingFixture reads the oracle and refuses a fixture that does not
// carry the provenance the contract promises.
func loadCPAMappingFixture(t *testing.T) cpaMappingFixture {
	t.Helper()
	data, err := os.ReadFile(cpaMappingFixturePath)
	if err != nil {
		t.Fatalf("read the upstream mapping oracle: %v", err)
	}
	var fixture cpaMappingFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode the upstream mapping oracle: %v", err)
	}
	if fixture.Contract != cpaMappingFixtureContract {
		t.Fatalf("the mapping oracle contract = %q, want %q", fixture.Contract, cpaMappingFixtureContract)
	}
	if strings.TrimSpace(fixture.Upstream.Repository) == "" || strings.TrimSpace(fixture.Upstream.Generator) == "" ||
		!strings.Contains(fixture.Upstream.Regenerate, "gencpamapping") {
		t.Fatalf("the mapping oracle does not name how it was generated: %+v", fixture.Upstream)
	}
	var aliasTable, structWalk bool
	for _, source := range fixture.Sources {
		switch source.Role {
		case "v8-alias-table":
			aliasTable = len(source.Commit) == 40 && len(source.SHA256) == 64
		case "config-struct-yaml-tags":
			structWalk = strings.TrimSpace(source.Ref) != ""
		}
	}
	if !aliasTable || !structWalk {
		t.Fatalf("the mapping oracle does not name both upstream sources: %+v", fixture.Sources)
	}
	if len(fixture.Versions) == 0 || len(fixture.Families) == 0 || len(fixture.Paths) == 0 || len(fixture.Presence) == 0 {
		t.Fatal("the mapping oracle is empty")
	}
	for _, version := range fixture.Versions {
		if len(fixture.TagCommits[version]) != 40 {
			t.Fatalf("the mapping oracle does not pin version %q to a commit", version)
		}
	}
	return fixture
}

// order is the position of one release in the fixture's scan order. A release
// the fixture does not carry sorts after every one it does, which is only used
// by the "presence before removal" guard: an unknown release there is a
// fixture/release mismatch, not an ordering answer.
func (f cpaMappingFixture) order(version string) int {
	for index, candidate := range f.Versions {
		if candidate == version {
			return index
		}
	}
	return len(f.Versions)
}

// upstreamDestinationPath resolves the path upstream v8 loads a legacy spelling
// as. An exact path alias wins over a prefix rewrite, and the eight key families
// name their grouped destination path, exactly as CPA orders them.
func upstreamDestinationPath(fixture cpaMappingFixture, legacy string) (string, bool) {
	for _, alias := range fixture.Paths {
		if alias.Kind == "leaf" && alias.Legacy == legacy {
			return alias.Current, true
		}
	}
	for _, family := range fixture.Families {
		if family.Legacy == legacy {
			return "api-keys." + family.Current, true
		}
	}
	best := ""
	bestCurrent := ""
	for _, alias := range fixture.Paths {
		if alias.Kind != "prefix" {
			continue
		}
		if alias.Legacy != legacy && !strings.HasPrefix(legacy, alias.Legacy+".") {
			continue
		}
		if len(alias.Legacy) <= len(best) {
			continue
		}
		best = alias.Legacy
		bestCurrent = alias.Current
	}
	if best == "" {
		return "", false
	}
	return bestCurrent + strings.TrimPrefix(legacy, best), true
}

// TestCPASourceMappingMatchesTheUpstreamAliasTable proves each row's destination
// path is the path upstream v8 loads that legacy spelling as. A table-only edit
// that points a row at another real destination path fails here; the old golden
// list could be changed in the same commit and stayed green.
func TestCPASourceMappingMatchesTheUpstreamAliasTable(t *testing.T) {
	fixture := loadCPAMappingFixture(t)
	for _, row := range cpaConfigPaths {
		expected, ok := upstreamDestinationPath(fixture, row.legacy)
		if !ok {
			t.Fatalf("mapping row %q -> %q has no upstream alias: v8 does not load that spelling",
				row.legacy, row.current)
		}
		if row.current != expected {
			t.Fatalf("mapping row %q = %q, upstream v8 loads it as %q", row.legacy, row.current, expected)
		}
	}
}

// TestCPASourceMappingMatchesTheUpstreamReleaseTable proves each row's
// `introduced` is the exact release the upstream configuration first accepted
// the path in. This is the claim the review found wrong once already.
func TestCPASourceMappingMatchesTheUpstreamReleaseTable(t *testing.T) {
	fixture := loadCPAMappingFixture(t)
	for _, row := range cpaConfigPaths {
		seen, ok := fixture.Presence[row.legacy]
		if !ok || seen.First == "" {
			t.Fatalf("mapping row %q -> %q claims release %q, but the upstream releases never accept that path",
				row.legacy, row.current, row.introduced)
		}
		if row.introduced != seen.First {
			t.Fatalf("mapping row %q claims release %q, upstream first accepts it in %q",
				row.legacy, row.introduced, seen.First)
		}
	}
}

// TestCPASourceRemovedPathsMatchTheUpstreamReleaseTable proves the removal claims
// the same way: the path is present in the release before and absent in the one
// the table names.
func TestCPASourceRemovedPathsMatchTheUpstreamReleaseTable(t *testing.T) {
	fixture := loadCPAMappingFixture(t)
	for _, removed := range cpaRemovedConfigPaths {
		seen, ok := fixture.Presence[removed.legacy]
		if !ok {
			t.Fatalf("removed-path row %q names release %q, but the upstream releases never accept that path",
				removed.legacy, removed.removed)
		}
		if seen.Removed != removed.removed {
			t.Fatalf("removed-path row %q claims removal in %q, upstream drops it in %q",
				removed.legacy, removed.removed, seen.Removed)
		}
		if seen.First == "" || fixture.order(seen.First) >= fixture.order(seen.Removed) {
			t.Fatalf("removed-path row %q has no presence before its removal: first %q, removed %q",
				removed.legacy, seen.First, seen.Removed)
		}
	}
}

// TestCPASourceMappingCoversEveryUpstreamKeyFamily proves no provider family the
// upstream v8 table groups is missing from the import. A missing family does not
// fail loudly at run time — the executor would report it as a manual item — so
// the completeness is pinned here.
func TestCPASourceMappingCoversEveryUpstreamKeyFamily(t *testing.T) {
	fixture := loadCPAMappingFixture(t)
	for _, family := range fixture.Families {
		expected := "api-keys." + family.Current
		mapped := false
		for _, row := range cpaConfigPaths {
			if row.legacy == family.Legacy && row.current == expected {
				mapped = true
				break
			}
		}
		if !mapped {
			t.Fatalf("the upstream key family %q -> %q is not mapped by the import", family.Legacy, expected)
		}
	}
}

// TestCPASourceMappingRowsDoNotContradictTheOtherTables keeps the three tables
// disjoint: a copied path, a removed path and an excluded section cannot claim
// the same spelling.
func TestCPASourceMappingRowsDoNotContradictTheOtherTables(t *testing.T) {
	mapped := map[string]bool{}
	for _, row := range cpaConfigPaths {
		mapped[row.legacy] = true
	}
	for _, removed := range cpaRemovedConfigPaths {
		if mapped[removed.legacy] {
			t.Fatalf("%q is both mapped and reported as removed", removed.legacy)
		}
	}
	for section := range cpaExcludedSourceSections {
		if mapped[section] {
			t.Fatalf("%q is both mapped and excluded as a Manager-owned section", section)
		}
	}
	for _, clientKeyPath := range []string{cpaClientKeyPathV7, cpaClientKeyPathV8} {
		if mapped[clientKeyPath] {
			t.Fatalf("%q is mapped as configuration, but the client key list is installed by the stage", clientKeyPath)
		}
	}
}
