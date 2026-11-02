package upgrade

// These tests pin the four review findings this lane closed: the release claim
// the mapping table derives from upstream, the destination secret a retry needs,
// the source root the import is bound to, the non-ASCII manual item, and the
// codex header-defaults gap.

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
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
	// The stored connection names another CPA, so the run stages and starts this
	// destination and is then refused at the handoff: the attempt has to survive.
	seedManagerConfig(t, fixture.targetRoot, model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: "http://another-cpa.example:8317", ManagementKey: "another-cpa-management-key"},
	})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	options := importOptions(fixture, runtime, destination.url(), boolPointer(true))
	_, err := ImportCPA(context.Background(), options)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAConnectionConflict {
		t.Fatalf("the first run produced %v (code %q), want %s", err, code, UpgradeErrorCPAConnectionConflict)
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
	if record.BaseURL != destination.url() || !record.Intent.PluginsEnabled {
		t.Fatalf("the recorded attempt = %+v, want the staged destination and plugin choice", record)
	}
	if !reflect.DeepEqual(record.Intent.APIKeys, first.APIKeys) {
		t.Fatalf("the recorded client key list = %v, want the staged one %v", record.Intent.APIKeys, first.APIKeys)
	}

	// The retry has no operator input: the destination is ready now, its secret is
	// the recorded one, and the connection is rebound to it.
	destination.mu.Lock()
	destination.management = first.ManagementKey
	destination.mu.Unlock()
	seedManagerConfig(t, fixture.targetRoot, model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: destination.url(), ManagementKey: first.ManagementKey},
	})
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
	if !strings.HasPrefix(retried.OperationID, first.OperationID) {
		t.Fatalf("the retry operation ID = %q, want it derived from the recorded one %q", retried.OperationID, first.OperationID)
	}
	// The supersede contract the Supervisor enforces: the request may differ in
	// its operation ID and nothing else.
	previous := *first
	previous.OperationID = retried.OperationID
	if !reflect.DeepEqual(previous, *retried) {
		t.Fatalf("the retry changed the request, not only the operation ID:\n first=%+v\nretry=%+v", previous, *retried)
	}
	if refusal := runtime.lastRefusal(); refusal != "" {
		t.Fatalf("the Runtime refused the supersede: %s", refusal)
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
	t.Logf("P-H2-supersede attempt=%d operation_id=%q secret_reused=%t intent_unchanged=%t",
		record.Attempt, retried.OperationID, retried.ManagementKey == first.ManagementKey, reflect.DeepEqual(previous, *retried))
}

// TestImportCPARefusesARetryAfterTheSourceChanges is the Medium finding: the
// Supervisor admits a replacement attempt only when it is the published request
// with a new operation ID, and the source identity is part of that request. A
// source edited between a terminal failure and the retry used to produce a new
// attempt the Supervisor refused, leaving the recorded attempt pointing at a
// request it would never accept. The drift is now refused before the attempt is
// touched, and nothing is submitted.
func TestImportCPARefusesARetryAfterTheSourceChanges(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	runtime.failStart = true
	options := importOptions(fixture, runtime, destination.url(), boolPointer(true))
	if _, err := ImportCPA(context.Background(), options); err == nil {
		t.Fatal("the first run should have recorded a terminal destination failure")
	}
	first := runtime.stageCall
	if first == nil {
		t.Fatal("the first run did not stage the destination")
	}
	// A comment is enough: the source identity is the digest of the source file,
	// so any byte changes the request the Supervisor would compare.
	configPath := filepath.Join(fixture.cpaDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read the fixture config: %v", err)
	}
	if err := os.WriteFile(configPath, append(append([]byte(nil), data...), []byte("\n# operator note\n")...), 0o600); err != nil {
		t.Fatalf("edit the fixture config: %v", err)
	}
	runtime.mu.Lock()
	runtime.failStart = false
	runtime.mu.Unlock()
	_, err = ImportCPA(context.Background(), options)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationRequestChanged {
		t.Fatalf("a retry after the source changed produced %v (code %q), want %s", err, code, UpgradeErrorCPADestinationRequestChanged)
	}
	if !strings.Contains(err.Error(), "source identity") {
		t.Fatalf("the refusal does not name the input that changed: %v", err)
	}
	if runtime.stageCall != first {
		t.Fatal("a drifted retry submitted a second destination request")
	}
	paths := newTargetPaths(fixture.targetRoot)
	record, _, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
	if err != nil || !found {
		t.Fatalf("the recorded attempt could not be read (found %t): %v", found, err)
	}
	if record.Attempt != 1 || !record.TerminalFailure || record.OperationID != first.OperationID {
		t.Fatalf("the drifted retry changed the attempt: %+v, want attempt 1 terminal under %q", record, first.OperationID)
	}
	t.Logf("P-H2-drift error_code=%s attempt=%d terminal=%t submitted_again=%t", UpgradeErrorCPADestinationRequestChanged, record.Attempt, record.TerminalFailure, runtime.stageCall != first)
}

// TestImportCPARefusesARetryAfterTheSupervisorRestarts is the restart half of the
// same finding. A Supervisor restart publishes a new generation, so the recorded
// attempt's generation is no longer the authority the journal validates against.
// The retry is refused as a changed request before the attempt moves; it is never
// submitted under an authority that would answer "stale runtime generation".
func TestImportCPARefusesARetryAfterTheSupervisorRestarts(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	runtime.failStart = true
	options := importOptions(fixture, runtime, destination.url(), boolPointer(true))
	if _, err := ImportCPA(context.Background(), options); err == nil {
		t.Fatal("the first run should have recorded a terminal destination failure")
	}
	first := runtime.stageCall
	runtime.mu.Lock()
	runtime.failStart = false
	runtime.mu.Unlock()
	runtime.restart()
	_, err := ImportCPA(context.Background(), options)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationRequestChanged {
		t.Fatalf("a retry after a Supervisor restart produced %v (code %q), want %s", err, code, UpgradeErrorCPADestinationRequestChanged)
	}
	if !strings.Contains(err.Error(), "Supervisor restarted") {
		t.Fatalf("the refusal does not state that the Supervisor restarted: %v", err)
	}
	if runtime.stageCall != first {
		t.Fatal("a retry after a restart submitted a second destination request")
	}
	paths := newTargetPaths(fixture.targetRoot)
	record, _, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
	if err != nil || !found {
		t.Fatalf("the recorded attempt could not be read (found %t): %v", found, err)
	}
	if record.Attempt != 1 || !record.TerminalFailure {
		t.Fatalf("the restart refusal changed the attempt: %+v", record)
	}
	t.Logf("P-H2-restart error_code=%s attempt=%d submitted_again=%t", UpgradeErrorCPADestinationRequestChanged, record.Attempt, runtime.stageCall != first)
}

// TestImportCPAProvesAReadyDestinationAfterTheSupervisorRestarts is the recovery
// half: a destination that is already answering is proved by its own key list, not
// resubmitted, so a restarted Supervisor does not turn a resumable destination
// into a refusal. Nothing is staged again.
func TestImportCPAProvesAReadyDestinationAfterTheSupervisorRestarts(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	seedManagerConfig(t, fixture.targetRoot, model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: "http://another-cpa.example:8317", ManagementKey: "another-cpa-management-key"},
	})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	options := importOptions(fixture, runtime, destination.url(), boolPointer(true))
	if _, err := ImportCPA(context.Background(), options); err == nil {
		t.Fatal("the first run should have been refused at the connection handoff")
	}
	first := runtime.stageCall
	if first == nil {
		t.Fatal("the first run did not stage the destination")
	}
	destination.mu.Lock()
	destination.management = first.ManagementKey
	destination.mu.Unlock()
	seedManagerConfig(t, fixture.targetRoot, model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: destination.url(), ManagementKey: first.ManagementKey},
	})
	runtime.restart()
	if _, err := ImportCPA(context.Background(), options); err != nil {
		t.Fatalf("a ready destination was refused after a Supervisor restart: %v", err)
	}
	if runtime.stageCall != first {
		t.Fatal("the restart recovery staged the destination again")
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
