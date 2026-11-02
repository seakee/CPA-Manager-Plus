package upgrade

// These tests pin the four review findings this lane closed: the release claim
// the mapping table derives from upstream, the destination secret a retry needs,
// the source root the import is bound to, the non-ASCII manual item, and the
// codex header-defaults gap.

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

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
	record, recorded, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
	if err != nil || !found {
		t.Fatalf("the staged secret was not recorded (found %t): %v", found, err)
	}
	if recorded != first.ManagementKey {
		t.Fatal("the recorded secret is not the one the destination was staged with")
	}
	if record.BaseURL != destination.url() || record.PluginsEnabled != true {
		t.Fatalf("the recorded attempt = %+v, want the staged destination and plugin choice", record)
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
	// The successful record releases the attempt: it is no longer a retry's to use.
	if _, _, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey); err != nil || found {
		t.Fatalf("the recorded attempt survived the successful import (found %t): %v", found, err)
	}
}

// TestImportCPASupersedesATerminallyFailedAttempt is the High finding end to end:
// the Supervisor replays a journalled operation by ID and never applies the new
// request under the same one, so a retry that regenerated the destination secret
// submitted a secret the destination never received and left the real one
// recorded nowhere. The retry must keep the recorded secret and move to a new
// attempt ID — and only after the recorded attempt is known to be terminal.
func TestImportCPASupersedesATerminallyFailedAttempt(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	runtime.replay = true
	runtime.failStart = true
	options := importOptions(fixture, runtime, destination.url(), boolPointer(true))
	_, err := ImportCPA(context.Background(), options)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationUnready {
		t.Fatalf("a failed start produced %v (code %q), want %s", err, code, UpgradeErrorCPADestinationUnready)
	}
	first := runtime.stageCall
	if first == nil || strings.TrimSpace(first.ManagementKey) == "" {
		t.Fatalf("the first run staged a destination without a management secret: %+v", first)
	}
	paths := newTargetPaths(fixture.targetRoot)
	record, recordedKey, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
	if err != nil || !found {
		t.Fatalf("the failed attempt was not recorded (found %t): %v", found, err)
	}
	if recordedKey != first.ManagementKey {
		t.Fatal("the recorded secret is not the one the destination was staged with")
	}

	// The retry must not mint a second secret: under the Supervisor's replay it
	// would never reach the destination, and the staged one would be lost.
	runtime.mu.Lock()
	runtime.failStart = false
	runtime.mu.Unlock()
	result, err := ImportCPA(context.Background(), options)
	if err != nil {
		t.Fatalf("the retry after a terminal failure was refused: %v", err)
	}
	if record.Attempt != 1 || !record.TerminalFailure || record.OperationID != first.OperationID {
		t.Fatalf("recorded attempt = %+v, want attempt 1, terminal, under %q", record, first.OperationID)
	}
	if result.Stage != model.UpgradeStageCPAImport || !containsValue(result.DomainsDone, model.UpgradeDomainCPAConfig) {
		t.Fatalf("the retry recorded stage %q with domains %v", result.Stage, result.DomainsDone)
	}
	retried := runtime.stageCall
	if retried == nil || retried.OperationID == first.OperationID {
		t.Fatalf("the retry reused operation ID %q, which the Supervisor replays", first.OperationID)
	}
	if !strings.HasSuffix(retried.OperationID, ":attempt:2") {
		t.Fatalf("the retry operation ID = %q, want attempt 2", retried.OperationID)
	}
	if retried.ManagementKey != first.ManagementKey {
		t.Fatal("the retry minted a second destination secret instead of reusing the recorded one")
	}
	destination.mu.Lock()
	installed := destination.management
	destination.mu.Unlock()
	if installed != first.ManagementKey {
		t.Fatal("the destination does not hold the recorded secret, so the Manager could never reach it")
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
	if recorded := readOperation(t, fixture.targetRoot); recorded.Stage != model.UpgradeStageReconcile {
		t.Fatalf("recorded stage = %q, want the untouched %q", recorded.Stage, model.UpgradeStageReconcile)
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
