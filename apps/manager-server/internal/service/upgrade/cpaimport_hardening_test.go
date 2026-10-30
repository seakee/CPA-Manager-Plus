package upgrade

// These tests pin the four review findings this lane closed: the release claim
// the mapping table derives from upstream, the destination secret a retry needs,
// the source root the import is bound to, the non-ASCII manual item, and the
// codex header-defaults gap.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

// TestCPASourceMappingTableMatchesTheGoldenReleaseClaims pins every row of the
// table, claims included.
//
// The earlier test only proved each release string parses, so a claim could be
// changed to another real release — exactly what happened to
// codex.model-level-cooling — without a failure. This list is the table's
// content: any added, removed or re-worded row changes it, and the change must
// then be made here deliberately.
func TestCPASourceMappingTableMatchesTheGoldenReleaseClaims(t *testing.T) {
	golden := []string{
		"gemini-api-key|api-keys.gemini|v7.0.0|true|true",
		"interactions-api-key|api-keys.interactions|v7.2.51|true|true",
		"vertex-api-key|api-keys.vertex|v7.0.0|true|true",
		"codex-api-key|api-keys.codex|v7.0.0|true|true",
		"claude-api-key|api-keys.claude|v7.0.0|true|true",
		"xai-api-key|api-keys.xai|v7.2.73|true|true",
		"meta-api-key|api-keys.meta|v7.3.4|true|true",
		"openai-compatibility|api-keys.openai-compatibility|v7.0.0|true|true",
		"oauth-model-alias|oauth.model-alias|v7.0.0|true|false",
		"oauth-excluded-models|oauth.excluded-models|v7.0.0|true|false",
		"oauth-request-scoped-errors|oauth.request-scoped-errors|v7.2.138|true|false",
		"payload.default|requests.payload.default|v7.0.0|true|false",
		"payload.default-raw|requests.payload.default-raw|v7.0.0|true|false",
		"payload.override|requests.payload.override|v7.0.0|true|false",
		"payload.override-raw|requests.payload.override-raw|v7.0.0|true|false",
		"payload.filter|requests.payload.filter|v7.0.0|true|false",
		"request-retry|routing.retry.request-retry|v7.0.0|false|false",
		"max-retry-credentials|routing.retry.max-retry-credentials|v7.0.0|false|false",
		"max-retry-interval|routing.retry.max-retry-interval|v7.0.0|false|false",
		"disable-cooling|routing.cooldown.disable-cooling|v7.0.0|false|false",
		"save-cooldown-status|routing.cooldown.save-cooldown-status|v7.2.19|false|false",
		"transient-error-cooldown-seconds|routing.cooldown.transient-error-cooldown-seconds|v7.2.19|false|false",
		"proxy-url|requests.proxy-url|v7.0.0|false|false",
		"codex.disable-codex-cloaking|upstream.codex.disable-codex-cloaking|v7.2.110|false|false",
		"codex.stream-bootstrap-buffering|upstream.codex.stream-bootstrap-buffering|v7.2.138|false|false",
		"codex.stream-bootstrap-timeout|upstream.codex.stream-bootstrap-timeout|v7.3.3|false|false",
		"codex.orphan-delegation-compatibility|upstream.codex.orphan-delegation-compatibility|v7.2.149|false|false",
		"codex.model-level-cooling|upstream.codex.model-level-cooling|v7.2.156|false|false",
		"codex.response-steering|upstream.codex.response-steering|v7.3.9|false|false",
		"codex.optimize-multi-agent-v2|client.codex.optimize-multi-agent-v2|v7.2.98|false|false",
		"claude.model-level-cooling|upstream.claude.model-level-cooling|v7.3.7|false|false",
		"disable-claude-cloak-mode|upstream.claude.disable-claude-cloak-mode|v7.2.8|false|false",
		"claude-header-defaults.user-agent|upstream.claude.header-defaults.user-agent|v7.0.0|false|false",
		"claude-header-defaults.package-version|upstream.claude.header-defaults.package-version|v7.0.0|false|false",
		"claude-header-defaults.runtime-version|upstream.claude.header-defaults.runtime-version|v7.0.0|false|false",
		"claude-header-defaults.os|upstream.claude.header-defaults.os|v7.0.0|false|false",
		"claude-header-defaults.arch|upstream.claude.header-defaults.arch|v7.0.0|false|false",
		"claude-header-defaults.timeout|upstream.claude.header-defaults.timeout|v7.0.0|false|false",
		"claude-header-defaults.timezone|upstream.claude.header-defaults.timezone|v7.2.116|false|false",
		"claude-header-defaults.stabilize-device-profile|upstream.claude.header-defaults.stabilize-device-profile|v7.0.0|false|false",
		"xai.inject-x-search|upstream.xai.inject-x-search|v7.2.102|false|false",
	}
	actual := make([]string, 0, len(cpaConfigPaths))
	for _, path := range cpaConfigPaths {
		actual = append(actual, fmt.Sprintf("%s|%s|%s|%t|%t", path.legacy, path.current, path.introduced, path.whole, path.grouped))
	}
	if len(actual) != len(golden) {
		t.Fatalf("the mapping table has %d rows, the golden list has %d:\n%s", len(actual), len(golden), strings.Join(actual, "\n"))
	}
	for index := range golden {
		if actual[index] != golden[index] {
			t.Fatalf("mapping row %d = %q, want %q", index, actual[index], golden[index])
		}
	}

	removedGolden := []string{"ampcode|amp-|v7.2.0", "codex.identity-confuse||v8.0.4"}
	removedActual := make([]string, 0, len(cpaRemovedConfigPaths))
	for _, removed := range cpaRemovedConfigPaths {
		removedActual = append(removedActual, fmt.Sprintf("%s|%s|%s", removed.legacy, removed.suffix, removed.removed))
	}
	if strings.Join(removedActual, "\n") != strings.Join(removedGolden, "\n") {
		t.Fatalf("the removed-path table = %q, want %q", removedActual, removedGolden)
	}
}

// TestImportCPARecordsTheDestinationSecretAndUsesItOnRetry is the Medium finding
// end to end: a run that stages the destination and then fails must leave the
// secret behind, so the next run against a ready destination proceeds without an
// operator input the CLI cannot supply.
func TestImportCPARecordsTheDestinationSecretAndUsesItOnRetry(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "")
	destination.silentDropPath = "routing.retry.request-retry"
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	options := importOptions(fixture, runtime, destination.url(), boolPointer(true))
	if _, err := ImportCPA(context.Background(), options); err != nil {
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAImportUnproven {
			t.Fatalf("the first run produced %v (code %q), want %s", err, code, UpgradeErrorCPAImportUnproven)
		}
	} else {
		t.Fatal("a dropped configuration write was proved")
	}
	first := runtime.stageCall
	if first == nil || strings.TrimSpace(first.ManagementKey) == "" {
		t.Fatalf("the first run staged a destination without a management secret: %+v", first)
	}
	paths := newTargetPaths(fixture.targetRoot)
	recorded, found, err := loadCPAImportDestinationKey(context.Background(), paths.Database, paths.DataKey, destination.url())
	if err != nil || !found {
		t.Fatalf("the staged secret was not recorded (found %t): %v", found, err)
	}
	if recorded != first.ManagementKey {
		t.Fatal("the recorded secret is not the one the destination was staged with")
	}

	// The retry has no operator input: the destination is ready now, and the only
	// secret that can prove it is the recorded one.
	destination.silentDropPath = ""
	destination.mu.Lock()
	destination.management = first.ManagementKey
	destination.mu.Unlock()
	result, err := ImportCPA(context.Background(), options)
	if err != nil {
		t.Fatalf("the retry against a ready destination was refused: %v", err)
	}
	if result.Stage != model.UpgradeStageCPAImport || !containsValue(result.DomainsDone, model.UpgradeDomainCPAConfig) {
		t.Fatalf("the retry recorded stage %q with domains %v", result.Stage, result.DomainsDone)
	}
	if runtime.stageCall != first {
		t.Fatal("the retry staged the destination again")
	}
	// The successful record releases the secret: it is no longer a retry's to use.
	if _, found, err := loadCPAImportDestinationKey(context.Background(), paths.Database, paths.DataKey, destination.url()); err != nil || found {
		t.Fatalf("the recorded secret survived the successful import (found %t): %v", found, err)
	}
}

// TestImportCPARequiresTheDestinationSecretWhenNoneWasRecorded pins the refusal
// that replaces the old dead end: a ready destination with no recorded secret and
// no file names the flag that supplies one.
func TestImportCPARequiresTheDestinationSecretWhenNoneWasRecorded(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "destination-management-secret-value-0123456789abcdef")
	runtime := newFakeImportRuntime(model.RuntimeStateReady)
	runtime.staged = destination
	_, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true)))
	code, ok := UpgradeErrorCodeOf(err)
	if !ok || code != UpgradeErrorCPADestinationKeyRequired {
		t.Fatalf("a ready destination without a secret produced %v (code %q), want %s", err, code, UpgradeErrorCPADestinationKeyRequired)
	}
	if !strings.Contains(err.Error(), "--destination-management-key-file") {
		t.Fatalf("the refusal does not name the flag that supplies the secret: %v", err)
	}
}

// TestImportCPARefusesASourceRootOtherThanTheRecordedOne binds the import to the
// tree the copy-in read. The impostor root has no CPA directory at all, so a read
// would report a missing source; the mismatch code proves the order.
func TestImportCPARefusesASourceRootOtherThanTheRecordedOne(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	impostor := newLegacyVolume(t)
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	options := importOptions(fixture, runtime, destination.url(), boolPointer(true))
	options.LegacyRoot = impostor.root
	_, err := ImportCPA(context.Background(), options)
	code, ok := UpgradeErrorCodeOf(err)
	if !ok || code != UpgradeErrorCPASourceRootMismatch {
		t.Fatalf("a different 1.x tree produced %v (code %q), want %s", err, code, UpgradeErrorCPASourceRootMismatch)
	}
	if runtime.stageCall != nil || len(destination.requests()) != 0 {
		t.Fatal("a run bound to another source root still wrote to the destination")
	}
	if recorded := readOperation(t, fixture.targetRoot); recorded.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("recorded stage = %q, want the untouched %q", recorded.Stage, model.UpgradeStageCopyIn)
	}
}

// TestBoundedManualItemTruncatesOnARuneBoundary pins the non-ASCII case: a long
// item must stay valid UTF-8 when it is bounded.
func TestBoundedManualItemTruncatesOnARuneBoundary(t *testing.T) {
	instruction := strings.Repeat("配置路径", 120)
	line := boundedManualItem(CPAImportManualItem{Item: "section:café", Reason: "not_in_import_scope", Instruction: instruction})
	if !utf8.ValidString(line) {
		t.Fatal("the bounded manual item is not valid UTF-8")
	}
	if len(line) > cpaImportManualItemLimit {
		t.Fatalf("the bounded manual item is %d bytes, want at most %d", len(line), cpaImportManualItemLimit)
	}
	if !strings.HasPrefix("section:café: "+instruction+" (not_in_import_scope)", line) {
		t.Fatal("the bounded manual item is not a prefix of the full line")
	}
	if !strings.HasPrefix(line, "section:café: ") {
		t.Fatalf("the bounded manual item lost its item name: %q", line)
	}
}

// TestCPASourceKeepsCodexHeaderDefaultsAsAStatedGap pins the Low finding: the
// section is not silently dropped and not invented onto a destination path that
// does not exist, and the operator is told where CPA v8 keeps it.
func TestCPASourceKeepsCodexHeaderDefaultsAsAStatedGap(t *testing.T) {
	config := "api-keys:\n  - \"legacy-client-key-1\"\ncodex-header-defaults:\n  user-agent: \"codex_cli_rs/0.20.0\"\n  beta-features: \"responses=v1\"\n"
	legacyRoot, _ := writeCPAFixture(t, config, nil)
	source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "7.3.4"})
	if err != nil {
		t.Fatalf("open a source carrying codex header defaults: %v", err)
	}
	for _, item := range source.manual {
		if item.item != "section:codex-header-defaults" {
			continue
		}
		if item.reason != "destination_surface_missing" {
			t.Fatalf("codex header defaults reason = %q, want destination_surface_missing", item.reason)
		}
		if !strings.Contains(item.instruction, "oauth.providers.codex.header-defaults") {
			t.Fatalf("the instruction does not name where CPA v8 keeps the field: %q", item.instruction)
		}
		if _, mapped := lookupDocumentPath(source.document, "oauth.providers.codex.header-defaults"); mapped == documentPresent {
			t.Fatal("the import invented a destination leaf for codex header defaults")
		}
		return
	}
	t.Fatalf("codex header defaults were dropped; manual items = %+v", source.manual)
}
