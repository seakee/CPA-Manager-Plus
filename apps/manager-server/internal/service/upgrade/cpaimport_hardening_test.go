package upgrade

// These tests pin the four review findings this lane closed: the release claim
// the mapping table derives from upstream, the destination secret a retry needs,
// the source root the import is bound to, the non-ASCII manual item, and the
// codex header-defaults gap.

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

// TestImportCPARecordsTheDestinationSecretAndUsesItOnRetry is the Medium finding
// end to end: a run that stages the destination and then fails must leave the
// secret behind, so the next run against a ready destination proceeds without an
// operator input the CLI cannot supply.
func TestImportCPARecordsTheDestinationSecretAndUsesItOnRetry(t *testing.T) {
	fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
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
	if record.BaseURL != destination.url() || !pluginsChoiceEqual(record.Intent.PluginsEnabled, boolPointer(true)) {
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
		t.Fatalf("the retry recorded stage %q with domains %v, want cpa_import and cpa_config", result.Stage, result.DomainsDone)
	}
	if runtime.stageCall != first {
		t.Fatal("the retry staged the destination again")
	}
	// The attempt stays recorded: it is the evidence the completion gate re-reads
	// and it holds the same secret the destination was staged with.
	_, keptKey, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
	if err != nil || !found || keptKey != first.ManagementKey {
		t.Fatalf("the recorded attempt (found %t) no longer holds the staged secret: %v", found, err)
	}
}

// TestImportCPASupersedesATerminallyFailedAttempt is the High finding end to end:
// the Supervisor replays a journalled operation by ID and never applies the new
// request under the same one, so a retry that regenerated the destination secret
// submitted a secret the destination never received and left the real one
// recorded nowhere. The retry must keep the recorded secret and move to a new
// attempt ID — and only after the recorded attempt is known to be terminal.
func TestImportCPASupersedesATerminallyFailedAttempt(t *testing.T) {
	fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
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
		t.Fatalf("the retry recorded stage %q with domains %v, want cpa_import and cpa_config", result.Stage, result.DomainsDone)
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
	fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
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

func TestImportCPARecoversOfflineRetryAfterTheSupervisorRestarts(t *testing.T) {
	t.Run("start failed before restart", func(t *testing.T) {
		fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
		destination := newFakeDestinationCPA(t, "")
		runtime := newFakeImportRuntime(model.RuntimeStateOffline)
		runtime.staged = destination
		runtime.failStart = true
		options := importOptions(fixture, runtime, destination.url(), boolPointer(true))
		if _, err := ImportCPA(context.Background(), options); err == nil {
			t.Fatal("the first run should have recorded a terminal destination start failure")
		}
		first := runtime.stageCall
		if first == nil {
			t.Fatal("the first run did not stage the destination")
		}
		runtime.mu.Lock()
		runtime.failStart = false
		runtime.mu.Unlock()
		runtime.restart()
		result, err := ImportCPA(context.Background(), options)
		if err != nil {
			t.Fatalf("offline retry after Supervisor restart failed: %v", err)
		}
		if runtime.stageCall != first {
			t.Fatal("cross-generation offline retry after staged destination submitted a second stage request")
		}
		if runtime.startCall == nil || runtime.startCall.ExpectedRuntimeGeneration != 5 {
			t.Fatalf("startCall = %+v, want ExpectedRuntimeGeneration=5", runtime.startCall)
		}
		if !slices.Contains(result.DomainsDone, model.UpgradeDomainCPAConfig) {
			t.Fatalf("DomainsDone = %v, want %s", result.DomainsDone, model.UpgradeDomainCPAConfig)
		}
	})

	t.Run("stage failed before restart", func(t *testing.T) {
		fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
		destination := newFakeDestinationCPA(t, "")
		runtime := newFakeImportRuntime(model.RuntimeStateOffline)
		runtime.staged = destination
		runtime.failStage = true
		options := importOptions(fixture, runtime, destination.url(), boolPointer(true))
		if _, err := ImportCPA(context.Background(), options); err == nil {
			t.Fatal("the first run should have failed StageDestination")
		}
		runtime.mu.Lock()
		runtime.failStage = false
		runtime.mu.Unlock()
		runtime.restart()
		result, err := ImportCPA(context.Background(), options)
		if err != nil {
			t.Fatalf("offline retry after StageDestination failure and Supervisor restart failed: %v", err)
		}
		if runtime.stageCall == nil || runtime.stageCall.ExpectedRuntimeGeneration != 5 {
			t.Fatalf("stageCall = %+v, want ExpectedRuntimeGeneration=5", runtime.stageCall)
		}
		if !slices.Contains(result.DomainsDone, model.UpgradeDomainCPAConfig) {
			t.Fatalf("DomainsDone = %v, want %s", result.DomainsDone, model.UpgradeDomainCPAConfig)
		}
	})

	t.Run("different runtime identity across restart still refuses", func(t *testing.T) {
		fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
		destination := newFakeDestinationCPA(t, "")
		runtime := newFakeImportRuntime(model.RuntimeStateOffline)
		runtime.staged = destination
		runtime.failStart = true
		options := importOptions(fixture, runtime, destination.url(), boolPointer(true))
		if _, err := ImportCPA(context.Background(), options); err == nil {
			t.Fatal("the first run should have failed Start")
		}
		runtime.mu.Lock()
		runtime.failStart = false
		runtime.identity = "runtime-identity-other"
		runtime.mu.Unlock()
		runtime.restart()
		_, err := ImportCPA(context.Background(), options)
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationRequestChanged {
			t.Fatalf("different RuntimeIdentity across restart = %v (code=%q/%t), want %s",
				err, code, ok, UpgradeErrorCPADestinationRequestChanged)
		}
	})
}

// TestImportCPAProvesAReadyDestinationAfterTheSupervisorRestarts is the recovery
// half: a destination that is already answering is proved by its own key list, not
// resubmitted, so a restarted Supervisor does not turn a resumable destination
// into a refusal. Nothing is staged again.
func TestImportCPAProvesAReadyDestinationAfterTheSupervisorRestarts(t *testing.T) {
	fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
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

// TestImportCPAAllowsACorrectedDestinationURLAfterAForeignRefusal is the Medium
// finding: the attempt used to record the caller's URL before the authenticated
// proof ran, so one mistyped address — the CLI's default is CPA_UPSTREAM_URL,
// which usually still names the stopped 1.x CPA — was refused as a foreign target
// and then refused every later run as a changed request. The URL is not part of
// the Supervisor's request, so only a proved URL may bind the attempt: a run that
// proved nothing leaves the URL replaceable, and the retry with the staged
// destination's address resumes the same attempt.
func TestImportCPAAllowsACorrectedDestinationURLAfterAForeignRefusal(t *testing.T) {
	fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	staged := newFakeDestinationCPA(t, "")
	foreign := newFakeDestinationCPA(t, "foreign-management-secret-value-0123456789")
	foreign.clientKeys = []string{"another-installation-client-key"}
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = staged

	_, err := ImportCPA(context.Background(), importOptions(fixture, runtime, foreign.url(), boolPointer(true)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAImportTargetForeign {
		t.Fatalf("a foreign URL produced %v (code %q), want %s", err, code, UpgradeErrorCPAImportTargetForeign)
	}
	if runtime.stageCall == nil {
		t.Fatal("the test did not stage the destination, so it proves nothing about the binding")
	}
	if recorded := readOperation(t, fixture.targetRoot); recorded.Stage != model.UpgradeStageReconcile {
		t.Fatalf("a refused URL moved the recorded stage to %q, want %q", recorded.Stage, model.UpgradeStageReconcile)
	}
	paths := newTargetPaths(fixture.targetRoot)
	attempt, _, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
	if err != nil || !found {
		t.Fatalf("the destination attempt was not recorded (found %t): %v", found, err)
	}
	if attempt.urlProved() {
		t.Fatalf("an unproved URL was recorded as the attempt's destination: %+v", attempt)
	}

	// The corrected URL is the destination the Supervisor staged: the retry proves
	// it, records it and finishes the step.
	result, err := ImportCPA(context.Background(), importOptions(fixture, runtime, staged.url(), boolPointer(true)))
	if err != nil {
		t.Fatalf("the retry with the staged destination's URL was refused: %v", err)
	}
	if result.Stage != model.UpgradeStageCPAImport {
		t.Fatalf("the retry recorded stage %q, want %q", result.Stage, model.UpgradeStageCPAImport)
	}
	attempt, _, found, err = loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
	if err != nil || !found {
		t.Fatalf("the proved attempt could not be read (found %t): %v", found, err)
	}
	if !attempt.urlProved() || attempt.BaseURL != staged.url() {
		t.Fatalf("the proved URL was not recorded: %+v", attempt)
	}

	// The finished step is recorded, so a later run is a replay — and a replay that
	// names another destination URL is refused as a changed request before that
	// address is contacted: the recorded verdict belongs to the URL this operation
	// bound, not to whatever address the caller names next.
	other := newFakeDestinationCPA(t, "")
	_, err = ImportCPA(context.Background(), importOptions(fixture, runtime, other.url(), boolPointer(true)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationRequestChanged {
		t.Fatalf("a swayed URL after the completed import produced %v (code %q), want %s", err, code, UpgradeErrorCPADestinationRequestChanged)
	}
	if requests := other.requests(); len(requests) != 0 {
		t.Fatalf("the other CPA answered %v, want no request", requests)
	}
	// An attempt that proved a URL but never recorded the copy — the row an
	// interrupted run leaves — is still bound to it.
	writeOperationStage(t, fixture.targetRoot, model.UpgradeStageCPAImport, []string{model.UpgradeDomainSourceCopy})
	_, err = ImportCPA(context.Background(), importOptions(fixture, runtime, other.url(), boolPointer(true)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationRequestChanged {
		t.Fatalf("a swapped URL after the proof produced %v (code %q), want %s", err, code, UpgradeErrorCPADestinationRequestChanged)
	}
	if requests := other.requests(); len(requests) != 0 {
		t.Fatalf("the other CPA answered %v, want no request", requests)
	}
}

// TestImportCPAReportsAnUnreachableDestinationAsUnreachable is the Low finding:
// every failure of the authenticated proof used to be reported as
// upgrade_cpa_import_target_foreign with "it is not the destination this operation
// staged", so a connection refused — a destination that is still starting, or the
// stopped 1.x CPA the CLI's default URL usually names — told the operator to edit
// a correct URL. A transport failure is now its own typed outcome, and the URL is
// still replaceable on retry.
func TestImportCPAReportsAnUnreachableDestinationAsUnreachable(t *testing.T) {
	// A reserved and released port refuses the connection.
	refusedURL := func(t *testing.T) string {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve a loopback port: %v", err)
		}
		address := listener.Addr().String()
		if err := listener.Close(); err != nil {
			t.Fatalf("release the loopback port: %v", err)
		}
		return "http://" + address
	}
	// A listener that accepts and never answers is the timeout half.
	silentURL := func(t *testing.T) string {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen on a loopback port: %v", err)
		}
		var mu sync.Mutex
		held := []net.Conn{}
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				mu.Lock()
				held = append(held, conn)
				mu.Unlock()
			}
		}()
		t.Cleanup(func() {
			_ = listener.Close()
			mu.Lock()
			defer mu.Unlock()
			for _, conn := range held {
				_ = conn.Close()
			}
		})
		return "http://" + listener.Addr().String()
	}
	for name, targetURL := range map[string]string{"connection refused": refusedURL(t), "no answer": silentURL(t)} {
		t.Run(name, func(t *testing.T) {
			fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
			staged := newFakeDestinationCPA(t, "")
			runtime := newFakeImportRuntime(model.RuntimeStateOffline)
			runtime.staged = staged
			_, err := ImportCPA(context.Background(), importOptions(fixture, runtime, targetURL, boolPointer(true)))
			code, ok := UpgradeErrorCodeOf(err)
			if !ok || code != UpgradeErrorCPAImportTargetUnreachable {
				t.Fatalf("an unreachable destination produced %v (code %q), want %s", err, code, UpgradeErrorCPAImportTargetUnreachable)
			}
			if strings.Contains(err.Error(), "not the destination this operation staged") {
				t.Fatalf("an unreachable destination was reported as a foreign one: %v", err)
			}
			if !strings.Contains(err.Error(), "nothing answered") {
				t.Fatalf("the refusal does not say that nothing answered: %v", err)
			}
			// The recovery this URL needs is the destination, not the 1.x CPA the
			// URL may name: starting that CPA passes the credential-free probe and is
			// then handed the staged secret, so the operator is sent to
			// --destination-url instead.
			if strings.Contains(err.Error(), "so start that CPA") {
				t.Fatalf("the refusal tells the operator to start the CPA the URL names: %v", err)
			}
			if !strings.Contains(err.Error(), "never start") {
				t.Fatalf("the refusal does not warn against starting the CPA the URL names: %v", err)
			}
			if !strings.Contains(err.Error(), "--destination-url") {
				t.Fatalf("the refusal does not name the destination URL as the recovery: %v", err)
			}
			if !strings.Contains(err.Error(), "still accepted") {
				t.Fatalf("an unproved attempt is told its URL is fixed, but no run recorded one: %v", err)
			}
			if recorded := readOperation(t, fixture.targetRoot); recorded.Stage != model.UpgradeStageReconcile {
				t.Fatalf("an unreachable destination moved the recorded stage to %q, want %q", recorded.Stage, model.UpgradeStageReconcile)
			}
			// The refused URL was never bound, so the retry still names the staged
			// destination and completes.
			result, err := ImportCPA(context.Background(), importOptions(fixture, runtime, staged.url(), boolPointer(true)))
			if err != nil {
				t.Fatalf("the retry with the staged destination's URL was refused: %v", err)
			}
			if result.Stage != model.UpgradeStageCPAImport {
				t.Fatalf("the retry recorded stage %q, want %q", result.Stage, model.UpgradeStageCPAImport)
			}
		})
	}
}

// TestImportCPAUnreachableAfterAProofKeepsTheProvedURL is the operator-wording
// half of the unreachable refusal: once a run proved a URL, the attempt is bound to
// it and requireSameDestinationRequest refuses a different one. The refusal must
// therefore not offer a corrected URL as a recovery the way the unproved case does,
// and it must not send the operator to start the CPA the URL names either.
func TestImportCPAUnreachableAfterAProofKeepsTheProvedURL(t *testing.T) {
	fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	staged := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = staged
	if _, err := ImportCPA(context.Background(), importOptions(fixture, runtime, staged.url(), boolPointer(true))); err != nil {
		t.Fatalf("the run against the staged destination was refused: %v", err)
	}
	paths := newTargetPaths(fixture.targetRoot)
	attempt, _, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
	if err != nil || !found {
		t.Fatalf("the destination attempt was not recorded (found %t): %v", found, err)
	}
	if !attempt.bindsURL(staged.url()) {
		t.Fatalf("the finished run did not record the proved URL: %+v", attempt)
	}

	// An interrupted run leaves the proved attempt without the recorded copy; the
	// destination then stops answering, and the same URL is retried.
	writeOperationStage(t, fixture.targetRoot, model.UpgradeStageCPAImport, []string{model.UpgradeDomainSourceCopy})
	staged.server.Close()
	_, err = ImportCPA(context.Background(), importOptions(fixture, runtime, staged.url(), boolPointer(true)))
	code, ok := UpgradeErrorCodeOf(err)
	if !ok || code != UpgradeErrorCPAImportTargetUnreachable {
		t.Fatalf("a proved URL that stopped answering produced %v (code %q), want %s", err, code, UpgradeErrorCPAImportTargetUnreachable)
	}
	message := err.Error()
	if !strings.Contains(message, "the same URL") || !strings.Contains(message, "changed request") {
		t.Fatalf("the refusal does not say the proved URL is kept and a different one is refused: %v", err)
	}
	if strings.Contains(message, "corrected URL") || strings.Contains(message, "still accepted") {
		t.Fatalf("a proved attempt is told its URL is replaceable: %v", err)
	}
}

// TestImportCPAProbesTheAddressTheProofDials is the Low finding: the probe built
// its own transport with the default dialer, which falls back from a dead first
// address to the second one a dual-stack name resolves to, while
// cpa.VerifyV8Adoption pins the first address of its own resolution. A destination
// that answers only behind the second address therefore passed the probe, failed
// the proof, and was then reported as a foreign target for a URL that was correct.
// The probe now shares cpa.NewDirectClient, so probe, proof and classification
// either reach the same address or report that nothing answered.
func TestImportCPAProbesTheAddressTheProofDials(t *testing.T) {
	ctx := context.Background()
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, "localhost")
	if err != nil {
		t.Fatalf("resolve localhost: %v", err)
	}
	if len(addresses) < 2 {
		t.Skipf("localhost resolves to %d address(es) here, so the dual-stack case cannot be built", len(addresses))
	}
	// The CPA stand-in answers only on an address the direct transport does not
	// dial: a probe that falls back would reach it, the pinned proof would not.
	listener, err := net.Listen("tcp", net.JoinHostPort(addresses[1].IP.String(), "0"))
	if err != nil {
		t.Skipf("listen on the second resolved address %s: %v", addresses[1].IP, err)
	}
	var mu sync.Mutex
	reached := 0
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		reached++
		mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Missing API key"}`))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	staged := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = staged
	targetURL := "http://localhost:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	_, err = ImportCPA(ctx, importOptions(fixture, runtime, targetURL, boolPointer(true)))
	code, ok := UpgradeErrorCodeOf(err)
	if !ok || code != UpgradeErrorCPAImportTargetUnreachable {
		t.Fatalf("a destination reachable only behind the second resolved address produced %v (code %q), want %s",
			err, code, UpgradeErrorCPAImportTargetUnreachable)
	}
	mu.Lock()
	defer mu.Unlock()
	if reached != 0 {
		t.Fatalf("the destination behind %s answered %d request(s), so the probe and the proof resolved localhost differently",
			addresses[1].IP, reached)
	}
}

// TestImportCPARecordsTheProofWhenItReplaysARecordedAttempt is the replay arm's
// half of the URL binding: an attempt that was recorded but never proved is
// replayed on the next run, and that run must write the proof it obtained. Without
// the write the attempt stays unbound, so the "a proved URL is immutable" rule
// silently does not apply to any path that resumes a recorded attempt, and only a
// later run that happens to take the ready arm would repair it.
func TestImportCPARecordsTheProofWhenItReplaysARecordedAttempt(t *testing.T) {
	ctx := context.Background()
	fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	staged := newFakeDestinationCPA(t, "")
	foreign := newFakeDestinationCPA(t, "foreign-management-secret-value-0123456789")
	foreign.clientKeys = []string{"another-installation-client-key"}
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = staged

	// The first run stages and starts the destination, then refuses the URL it was
	// given: the attempt is recorded, unproved and not terminal.
	_, err := ImportCPA(ctx, importOptions(fixture, runtime, foreign.url(), boolPointer(true)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAImportTargetForeign {
		t.Fatalf("a foreign URL produced %v (code %q), want %s", err, code, UpgradeErrorCPAImportTargetForeign)
	}
	paths := newTargetPaths(fixture.targetRoot)
	attempt, recordedKey, found, err := loadCPAImportDestinationRecord(ctx, paths.Database, paths.DataKey)
	if err != nil || !found {
		t.Fatalf("the destination attempt was not recorded (found %t): %v", found, err)
	}
	if attempt.urlProved() || attempt.TerminalFailure {
		t.Fatalf("the refused run left the attempt proved or terminal: %+v", attempt)
	}

	// When the same Runtime is confirmed offline after a journalled start, retry
	// replays StageDestination under the recorded attempt ID and submits a fresh
	// Start operation ID (:start:2) so the offline child is respawned cleanly.
	runtime.mu.Lock()
	runtime.state = model.RuntimeStateOffline
	runtime.startingPollsBeforeReady = 2
	runtime.mu.Unlock()
	stageBefore, startBefore := runtime.callCounts()
	result, err := ImportCPA(ctx, importOptions(fixture, runtime, staged.url(), boolPointer(true)))
	if err != nil {
		t.Fatalf("the confirmed-offline retry with the staged destination's URL was refused: %v", err)
	}
	stageAfter, startAfter := runtime.callCounts()
	if stageAfter != stageBefore+1 || startAfter != startBefore+1 {
		t.Fatalf("confirmed-offline retry submissions = stage %d->%d start %d->%d, want +1/+1",
			stageBefore, stageAfter, startBefore, startAfter)
	}
	if runtime.startCall == nil || runtime.startCall.OperationID != attempt.OperationID+":start:2" {
		t.Fatalf("confirmed-offline retry startCall = %+v, want %q", runtime.startCall, attempt.OperationID+":start:2")
	}
	if result.Stage != model.UpgradeStageCPAImport {
		t.Fatalf("the replay recorded stage %q, want %q", result.Stage, model.UpgradeStageCPAImport)
	}
	replayed, keptKey, found, err := loadCPAImportDestinationRecord(ctx, paths.Database, paths.DataKey)
	if err != nil || !found {
		t.Fatalf("the replayed attempt could not be read (found %t): %v", found, err)
	}
	if !replayed.bindsURL(staged.url()) {
		t.Fatalf("the replay proved the destination but did not record it: %+v", replayed)
	}
	if keptKey != recordedKey {
		t.Fatal("the replay replaced the secret the destination was staged with")
	}
	if gotState := runtime.currentState(); gotState != model.RuntimeStateReady {
		t.Fatalf("runtime state after replay readiness = %q, want %q", gotState, model.RuntimeStateReady)
	}

	// The replay recorded the copy, so a later run is a replay and a swapped URL
	// is refused before that address is contacted; the attempt that proved a URL is
	// still bound to it once the recorded copy is removed.
	other := newFakeDestinationCPA(t, "")
	_, err = ImportCPA(ctx, importOptions(fixture, runtime, other.url(), boolPointer(true)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationRequestChanged {
		t.Fatalf("a swapped URL after the completed replay produced %v (code %q), want %s", err, code, UpgradeErrorCPADestinationRequestChanged)
	}
	if requests := other.requests(); len(requests) != 0 {
		t.Fatalf("the other CPA answered %v, want no request", requests)
	}
}

// TestImportCPAClassifiesAProofTransportFailureAsUnreachable is the other half of
// the unreachable classification: the gate and the proof are separate exchanges, so
// a destination that answers the credential-free probe can still be gone by the
// time the authenticated read runs. That read collapses its transport failure into
// a wrong-key verdict, and the classification probe is what distinguishes the two;
// when the destination stopped answering in between, the run must say unreachable
// rather than tell the operator the URL is foreign.
func TestImportCPAClassifiesAProofTransportFailureAsUnreachable(t *testing.T) {
	ctx := context.Background()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on a loopback port: %v", err)
	}
	var mu sync.Mutex
	paths := []string{}
	server := &http.Server{}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/v1/models" {
			// The gate passes: both an absent and an unknown credential are rejected.
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// The destination dies in the middle of the authenticated read: the
		// connection is dropped and the listener goes away, so the proof fails in
		// transport and the classification probe finds nothing to ask.
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, hijackErr := hijacker.Hijack(); hijackErr == nil {
				_ = conn.Close()
			}
		}
		go func() { _ = server.Close() }()
	})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	staged := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = staged
	_, err = ImportCPA(ctx, importOptions(fixture, runtime, "http://"+listener.Addr().String(), boolPointer(true)))
	code, ok := UpgradeErrorCodeOf(err)
	if !ok || code != UpgradeErrorCPAImportTargetUnreachable {
		t.Fatalf("a destination that stopped answering between the gate and the proof produced %v (code %q), want %s",
			err, code, UpgradeErrorCPAImportTargetUnreachable)
	}
	if strings.Contains(err.Error(), "not the destination this operation staged") {
		t.Fatalf("a transport failure after the gate was reported as a foreign target: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	// The premise: the gate let this endpoint through and the proof read it.
	found := false
	for _, path := range paths {
		if path == "GET /v8/management/config" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the authenticated read never reached the endpoint, so the test proves nothing: %v", paths)
	}
}

// unprovenEndpoint answers the unauthenticated probe without being the staged
// destination. It records every Authorization value it was sent, so a test can
// prove which credentials crossed to it.
type unprovenEndpoint struct {
	mu             sync.Mutex
	requests       int
	authorizations []string
	server         *httptest.Server
}

// newUnprovenEndpoint serves `/v1/models` with one status for an absent
// credential and another for any presented one.
func newUnprovenEndpoint(t *testing.T, anonymous, credential int) *unprovenEndpoint {
	t.Helper()
	endpoint := &unprovenEndpoint{}
	endpoint.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint.mu.Lock()
		endpoint.requests++
		endpoint.authorizations = append(endpoint.authorizations, r.Header.Get("Authorization"))
		endpoint.mu.Unlock()
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(anonymous)
			return
		}
		w.WriteHeader(credential)
	}))
	t.Cleanup(endpoint.server.Close)
	return endpoint
}

// TestImportCPANeverSendsTheStagedSecretToAnUnprovenURL is the Low finding: the
// authenticated proof reads the destination's configuration with the management
// secret this run minted, so running it first handed that secret to whatever
// answered the caller's URL — and the URL is the operator's input. The proof now
// runs only behind an unauthenticated probe that requires the endpoint to reject
// both an absent and an unknown credential; an endpoint that does neither is
// refused as foreign without ever receiving the secret or a client key.
func TestImportCPANeverSendsTheStagedSecretToAnUnprovenURL(t *testing.T) {
	cases := []struct {
		name       string
		anonymous  int
		credential int
	}{
		{name: "does not gate credentials", anonymous: http.StatusOK, credential: http.StatusOK},
		{name: "accepts unknown credentials", anonymous: http.StatusUnauthorized, credential: http.StatusOK},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
			endpoint := newUnprovenEndpoint(t, testCase.anonymous, testCase.credential)
			staged := newFakeDestinationCPA(t, "")
			runtime := newFakeImportRuntime(model.RuntimeStateOffline)
			runtime.staged = staged
			_, err := ImportCPA(context.Background(), importOptions(fixture, runtime, endpoint.server.URL, boolPointer(true)))
			if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAImportTargetForeign {
				t.Fatalf("an endpoint that is not the staged destination produced %v (code %q), want %s", err, code, UpgradeErrorCPAImportTargetForeign)
			}
			if runtime.stageCall == nil || strings.TrimSpace(runtime.stageCall.ManagementKey) == "" {
				t.Fatal("the run did not stage a destination, so it proves nothing about the secret")
			}
			secret := runtime.stageCall.ManagementKey
			clientKeys := append([]string(nil), runtime.stageCall.APIKeys...)
			endpoint.mu.Lock()
			defer endpoint.mu.Unlock()
			if endpoint.requests == 0 {
				t.Fatal("the probe never reached the endpoint, so the test proves nothing")
			}
			for _, authorization := range endpoint.authorizations {
				if strings.Contains(authorization, secret) {
					t.Fatalf("the unproven endpoint received the staged management secret: %q", authorization)
				}
				for _, key := range clientKeys {
					if key != "" && strings.Contains(authorization, key) {
						t.Fatalf("the unproven endpoint received a client key: %q", authorization)
					}
				}
			}
			if recorded := readOperation(t, fixture.targetRoot); recorded.Stage != model.UpgradeStageReconcile {
				t.Fatalf("an unproven endpoint moved the recorded stage to %q, want %q", recorded.Stage, model.UpgradeStageReconcile)
			}
		})
	}
}

// TestImportCPADestinationAttemptNeverMovesItsTimestampBackwards is the Low
// finding: the attempt's JSON updatedAtMs and its settings column were both
// stamped from the wall clock on every write, so a store restored from a backup
// written later, or a host clock that moved back, rewrote the attempt as older
// than what was already recorded. Both representations now move forward from the
// stored value and are written from one value, so they cannot disagree.
func TestImportCPADestinationAttemptNeverMovesItsTimestampBackwards(t *testing.T) {
	fixture := newImportFixture(t, v8FixtureConfig, nil)
	paths := newTargetPaths(fixture.targetRoot)
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	record := cpaImportDestinationRecord{
		BaseURL: "http://127.0.0.1:8317",
		Intent: cpaImportDestinationIntent{
			SourceIdentity: "external:sha256:fixture", SourceClass: managedDestinationSourceClass,
			APIKeys: []string{"legacy-client-key-1"}, PluginsEnabled: boolPointer(true),
		},
		OperationID: "upgrade-cpa-import:fixture", Attempt: 1,
	}
	if err := saveCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey, record, secret); err != nil {
		t.Fatalf("record the attempt: %v", err)
	}
	future := time.Now().Add(time.Hour).UnixMilli()
	for _, testCase := range []struct {
		name   string
		column int64
		json   int64
	}{
		{name: "column ahead of the clock", column: future},
		{name: "json ahead of the clock", json: future},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			seedDestinationUpdatedAt(t, paths.Database, testCase.column, testCase.json)
			stored, key, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
			if err != nil || !found {
				t.Fatalf("read the seeded attempt (found %t): %v", found, err)
			}
			if err := saveCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey, stored, key); err != nil {
				t.Fatalf("rewrite the attempt: %v", err)
			}
			reloaded, _, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
			if err != nil || !found {
				t.Fatalf("read the rewritten attempt (found %t): %v", found, err)
			}
			column := destinationUpdatedAtColumn(t, paths.Database)
			if reloaded.UpdatedAtMS < future {
				t.Fatalf("attempt updatedAtMs = %d, want at least the seeded %d", reloaded.UpdatedAtMS, future)
			}
			if column < future {
				t.Fatalf("settings updated_at_ms = %d, want at least the seeded %d", column, future)
			}
			if column != reloaded.UpdatedAtMS {
				t.Fatalf("settings updated_at_ms = %d and attempt updatedAtMs = %d disagree", column, reloaded.UpdatedAtMS)
			}
		})
	}
}

// seedDestinationUpdatedAt moves one representation of the attempt's timestamp
// ahead of the wall clock, which is the state a restored store or a rolled-back
// clock leaves behind.
func seedDestinationUpdatedAt(t *testing.T, databasePath string, columnMS, jsonMS int64) {
	t.Helper()
	db, err := managersqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("open the store to seed the attempt timestamp: %v", err)
	}
	defer func() { _ = db.Close() }()
	var raw string
	var column int64
	if err := db.QueryRow(`select value, updated_at_ms from settings where key = ?`, cpaImportDestinationKeySetting).Scan(&raw, &column); err != nil {
		t.Fatalf("read the seeded attempt: %v", err)
	}
	var record cpaImportDestinationRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatalf("decode the seeded attempt: %v", err)
	}
	if jsonMS != 0 {
		record.UpdatedAtMS = jsonMS
	}
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("encode the seeded attempt: %v", err)
	}
	if columnMS == 0 {
		columnMS = column
	}
	if _, err := db.Exec(`update settings set value = ?, updated_at_ms = ? where key = ?`,
		string(payload), columnMS, cpaImportDestinationKeySetting); err != nil {
		t.Fatalf("seed the attempt timestamp: %v", err)
	}
}

// destinationUpdatedAtColumn reads the settings column the attempt was written to.
func destinationUpdatedAtColumn(t *testing.T, databasePath string) int64 {
	t.Helper()
	db, err := managersqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("open the store to read the attempt timestamp: %v", err)
	}
	defer func() { _ = db.Close() }()
	var column int64
	if err := db.QueryRow(`select updated_at_ms from settings where key = ?`, cpaImportDestinationKeySetting).Scan(&column); err != nil {
		t.Fatalf("read the attempt timestamp: %v", err)
	}
	return column
}

// TestImportCPARequiresTheDestinationSecretWhenNoneWasRecorded pins the refusal
// that replaces the old dead end: a ready destination with no recorded secret and
// no file names the flag that supplies one.
func TestImportCPARequiresTheDestinationSecretWhenNoneWasRecorded(t *testing.T) {
	fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
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
	fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
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

// TestBoundedManualItemTruncatesOnARuneBoundary pins the non-ASCII case and the
// half that must survive it: a long instruction must stay valid UTF-8 when it is
// bounded, and the item name and typed reason are never what gets cut. The reason
// is the rule's own manual action, so a bounded line is not a prefix of the full
// line — it is the full head and tail with the instruction shortened between them.
func TestBoundedManualItemTruncatesOnARuneBoundary(t *testing.T) {
	instruction := strings.Repeat("配置路径", 120)
	line := boundedManualItem(CPAImportManualItem{Item: "section:café", Reason: "not_in_import_scope", Instruction: instruction})
	if !utf8.ValidString(line) {
		t.Fatal("the bounded manual item is not valid UTF-8")
	}
	if len(line) > cpaImportManualItemLimit {
		t.Fatalf("the bounded manual item is %d bytes, want at most %d", len(line), cpaImportManualItemLimit)
	}
	if !strings.HasPrefix(line, "section:café: ") {
		t.Fatalf("the bounded manual item lost its item name: %q", line)
	}
	if !strings.HasSuffix(line, " (not_in_import_scope)") {
		t.Fatalf("the bounded manual item lost its reason: %q", line)
	}
	kept := strings.TrimSuffix(strings.TrimPrefix(line, "section:café: "), " (not_in_import_scope)")
	if !strings.HasPrefix(instruction, kept) {
		t.Fatalf("the bounded instruction %q is not a prefix of the full one", kept)
	}
	// A rule's own manual action is preserved for a full-size instruction, which
	// is what tells the operator what the item still needs.
	manual := boundedManualItem(CPAImportManualItem{
		Item: "section:ampcode", Reason: "reconfigure_after_adoption", Instruction: instruction,
	})
	if !strings.HasSuffix(manual, "(reconfigure_after_adoption)") {
		t.Fatalf("the bounded manual item lost its reason: %q", manual)
	}
}

// TestCPASourceMigratesCodexHeaderDefaultsToOAuthProvidersCodex verifies both
// v7 `codex-header-defaults.*` and v8 `oauth.providers.codex.header-defaults.*`
// map onto `oauth.providers.codex.header-defaults.*` with zero manual items,
// while an unknown sibling key inside `codex-header-defaults` is still reported.
func TestCPASourceMigratesCodexHeaderDefaultsToOAuthProvidersCodex(t *testing.T) {
	config := "api-keys:\n  - \"legacy-client-key-1\"\ncodex-header-defaults:\n  user-agent: \"codex_cli_rs/0.20.0\"\n  beta-features: \"responses=v1\"\n  unexpected-header: \"keep-reported\"\n"
	legacyRoot, _ := writeCPAFixture(t, config, nil)
	source, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "8.0.55"})
	if err != nil {
		t.Fatalf("open a source carrying codex header defaults: %v", err)
	}
	if got, presence := lookupDocumentPath(source.document, "oauth.providers.codex.header-defaults.user-agent"); presence != documentPresent || got != "codex_cli_rs/0.20.0" {
		t.Fatalf("user-agent = %#v (presence=%v), want codex_cli_rs/0.20.0", got, presence)
	}
	if got, presence := lookupDocumentPath(source.document, "oauth.providers.codex.header-defaults.beta-features"); presence != documentPresent || got != "responses=v1" {
		t.Fatalf("beta-features = %#v (presence=%v), want responses=v1", got, presence)
	}
	if len(source.manual) != 1 || source.manual[0].item != "config:codex-header-defaults.unexpected-header" || source.manual[0].reason != "not_in_import_scope" {
		t.Fatalf("manual items = %+v, want only config:codex-header-defaults.unexpected-header (not_in_import_scope)", source.manual)
	}
}

func TestImportCPAReplayRestartsOfflineManagedRuntime(t *testing.T) {
	fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	options := importOptions(fixture, runtime, destination.url(), boolPointer(true))

	first, err := ImportCPA(context.Background(), options)
	if err != nil {
		t.Fatalf("initial ImportCPA: %v", err)
	}
	if first.AlreadyComplete || !slices.Contains(first.DomainsDone, model.UpgradeDomainCPAConfig) {
		t.Fatalf("unexpected initial result: %+v", first)
	}

	// Simulate Supervisor restart leaving the staged Managed CPA offline.
	runtime.mu.Lock()
	runtime.state = model.RuntimeStateOffline
	runtime.mu.Unlock()
	runtime.restart()

	replayed, err := ImportCPA(context.Background(), options)
	if err != nil {
		t.Fatalf("replay ImportCPA while runtime offline failed: %v", err)
	}
	if !replayed.AlreadyComplete {
		t.Fatalf("expected AlreadyComplete=true on replay, got %+v", replayed)
	}
	status, err := runtime.Status(context.Background())
	if err != nil || status.State != model.RuntimeStateReady {
		t.Fatalf("runtime status after offline replay = %+v, err=%v, want ready", status, err)
	}
	if runtime.startCall == nil || runtime.startCall.ExpectedRuntimeGeneration != status.Generation {
		t.Fatalf("startCall = %+v, want ExpectedRuntimeGeneration=%d", runtime.startCall, status.Generation)
	}
}

func TestCPASourceAndPreflightRefuseSymbolicLinks(t *testing.T) {
	outsideDir := t.TempDir()
	outsideConfig := filepath.Join(outsideDir, "config.yaml")
	if err := os.WriteFile(outsideConfig, []byte(v8FixtureConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideAuthDir := filepath.Join(outsideDir, "auths")
	if err := os.MkdirAll(outsideAuthDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideAuthDir, "outside.json"), []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("symlinked sibling legacy-cpa directory", func(t *testing.T) {
		parent := t.TempDir()
		src := newSourceFixture(t, nil)
		legacyRoot := filepath.Join(parent, "data")
		if err := os.Rename(src.root, legacyRoot); err != nil {
			t.Skipf("rename temp dir unsupported: %v", err)
		}
		siblingLink := filepath.Join(parent, "legacy-cpa")
		if err := os.Symlink(outsideDir, siblingLink); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}

		report, pfErr := Preflight(context.Background(), Options{LegacyRoot: legacyRoot})
		if pfErr != nil {
			t.Fatalf("Preflight error: %v", pfErr)
		}
		if report.Ready || !slices.Contains(report.Blocked, CodeSourceSymlinked) {
			t.Fatalf("Preflight with symlinked ../legacy-cpa ready=%t blocked=%v, want %s",
				report.Ready, report.Blocked, CodeSourceSymlinked)
		}

		_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "8.0.55"})
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPASourceMissing || !errors.Is(err, ErrSourceSymlinked) {
			t.Fatalf("openManagedCPASource with symlinked ../legacy-cpa = %v (code=%q/%t), want %s wrapping ErrSourceSymlinked",
				err, code, ok, UpgradeErrorCPASourceMissing)
		}
	})

	t.Run("symlinked config.yaml", func(t *testing.T) {
		parent := t.TempDir()
		src := newSourceFixture(t, nil)
		legacyRoot := filepath.Join(parent, "data")
		if err := os.Rename(src.root, legacyRoot); err != nil {
			t.Skipf("rename temp dir unsupported: %v", err)
		}
		cpaDir := filepath.Join(parent, "legacy-cpa")
		if err := os.MkdirAll(cpaDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideConfig, filepath.Join(cpaDir, "config.yaml")); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}

		report, pfErr := Preflight(context.Background(), Options{LegacyRoot: legacyRoot})
		if pfErr != nil {
			t.Fatalf("Preflight error: %v", pfErr)
		}
		if report.Ready || !slices.Contains(report.Blocked, CodeSourceSymlinked) {
			t.Fatalf("Preflight with symlinked config.yaml ready=%t blocked=%v, want %s",
				report.Ready, report.Blocked, CodeSourceSymlinked)
		}

		_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "8.0.55"})
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPASourceMissing || !errors.Is(err, ErrSourceSymlinked) {
			t.Fatalf("openManagedCPASource with symlinked config.yaml = %v (code=%q/%t), want %s wrapping ErrSourceSymlinked",
				err, code, ok, UpgradeErrorCPASourceMissing)
		}
	})

	t.Run("symlinked explicit CPAAuthDirectory", func(t *testing.T) {
		legacyRoot, _ := writeCPAFixture(t, v8FixtureConfig, nil)
		authLink := filepath.Join(t.TempDir(), "auth-link")
		if err := os.Symlink(outsideAuthDir, authLink); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
		_, err := openManagedCPASource(cpaSourceOptions{
			LegacyRoot:       legacyRoot,
			CPAAuthDirectory: authLink,
			CPAVersion:       "8.0.55",
		})
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPASourceMissing || !errors.Is(err, ErrSourceSymlinked) {
			t.Fatalf("openManagedCPASource with symlinked CPAAuthDirectory = %v (code=%q/%t), want %s wrapping ErrSourceSymlinked",
				err, code, ok, UpgradeErrorCPASourceMissing)
		}
	})

	t.Run("symlinked cpaDir auths directory", func(t *testing.T) {
		legacyRoot, cpaDir := writeCPAFixture(t, v8FixtureConfig, nil)
		authsPath := filepath.Join(cpaDir, "auths")
		_ = os.RemoveAll(authsPath)
		if err := os.Symlink(outsideAuthDir, authsPath); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
		_, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "8.0.55"})
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPASourceMissing || !errors.Is(err, ErrSourceSymlinked) {
			t.Fatalf("openManagedCPASource with symlinked auths = %v (code=%q/%t), want %s wrapping ErrSourceSymlinked",
				err, code, ok, UpgradeErrorCPASourceMissing)
		}
	})

	t.Run("symlinked relative configuredAuthDir segment inside cpaDir is rejected", func(t *testing.T) {
		cfg := "config-version: 8\noauth:\n  auth-dir: nested/linked\naccess:\n  api-keys:\n    - \"legacy-client-key-1\"\n"
		legacyRoot, cpaDir := writeCPAFixture(t, cfg, map[string]string{"fallback.json": `{"type":"codex"}`})
		inTreeTarget := filepath.Join(cpaDir, "hidden-target")
		if err := os.MkdirAll(inTreeTarget, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(inTreeTarget, "linked-cred.json"), []byte(`{"type":"codex"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		nestedDir := filepath.Join(cpaDir, "nested")
		if err := os.MkdirAll(nestedDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(inTreeTarget, filepath.Join(nestedDir, "linked")); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
		src, err := openManagedCPASource(cpaSourceOptions{LegacyRoot: legacyRoot, CPAVersion: "8.0.55"})
		if err != nil {
			t.Fatalf("openManagedCPASource: %v", err)
		}
		if _, ok := src.credentialData["linked-cred.json"]; ok {
			t.Fatal("configuredAuthDir followed an internal symlink segment")
		}
	})
}

// crossGenStageRuntime mirrors the pinned runtime-supervisor lifecycle.Executor
// destination & requireStagedDestination contract verbatim while allowing tests
// to interrupt StageDestination or Start before or after reaching the Supervisor.
type crossGenStageRuntime struct {
	*fakeImportRuntime
	failAfterStagePersist     bool
	failBeforeSupervisor      bool
	failStartBeforeSupervisor bool
}

func (r *crossGenStageRuntime) StageDestination(ctx context.Context, request model.RuntimeDestinationRequest) (model.RuntimeOperationResult, error) {
	r.mu.Lock()
	if r.failBeforeSupervisor {
		r.mu.Unlock()
		return model.RuntimeOperationResult{}, errors.New("transient transport failure before reaching supervisor")
	}
	r.mu.Unlock()
	result, err := r.fakeImportRuntime.StageDestination(ctx, request)
	if err != nil {
		return result, err
	}
	r.mu.Lock()
	interrupt := r.failAfterStagePersist
	r.mu.Unlock()
	if interrupt {
		return model.RuntimeOperationResult{}, errors.New("interrupted after stage.json persisted before StartDispatched")
	}
	return result, nil
}

func (r *crossGenStageRuntime) Start(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	r.mu.Lock()
	if r.failStartBeforeSupervisor {
		r.mu.Unlock()
		return model.RuntimeOperationResult{}, errors.New("transient transport failure before Start reached supervisor")
	}
	if r.published != nil {
		stage, ok := r.journal[r.published.OperationID]
		if !ok || stage.OperationType != model.RuntimeOperationStageDestination || stage.State != model.RuntimeOperationSucceeded {
			r.mu.Unlock()
			return model.RuntimeOperationResult{}, &runtimeservice.ProtocolError{Code: runtimeservice.ProtocolErrorOperationStateConflict, HTTPStatus: 409}
		}
	}
	r.mu.Unlock()
	return r.fakeImportRuntime.Start(ctx, request)
}

func TestImportCPARecoversCrossGenerationAfterStagePersistedBeforeStartDispatched(t *testing.T) {
	t.Run("stage succeeded on generation G before StartDispatched then Supervisor restarted to G+1", func(t *testing.T) {
		fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
		destination := newFakeDestinationCPA(t, "")
		rt := &crossGenStageRuntime{
			fakeImportRuntime:     newFakeImportRuntime(model.RuntimeStateOffline),
			failAfterStagePersist: true,
		}
		rt.staged = destination
		options := importOptions(fixture, rt, destination.url(), boolPointer(true))

		_, err := ImportCPA(context.Background(), options)
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationUnready {
			t.Fatalf("interrupted initial run = %v (code=%q/%t), want %s", err, code, ok, UpgradeErrorCPADestinationUnready)
		}
		if rt.published == nil || rt.published.ExpectedRuntimeGeneration != 4 {
			t.Fatalf("expected generation-4 stage.json persisted on Supervisor, got %+v", rt.published)
		}
		firstPublished := *rt.published
		paths := newTargetPaths(fixture.targetRoot)
		record, firstKey, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
		if err != nil || !found {
			t.Fatalf("loadCPAImportDestinationRecord: found=%t err=%v", found, err)
		}
		if record.Attempt != 1 || record.StartDispatched || record.Intent.ExpectedRuntimeGeneration != 4 || firstKey != firstPublished.ManagementKey {
			t.Fatalf("recorded attempt after interrupted stage = %+v, want Attempt=1 StartDispatched=false Gen=4", record)
		}

		rt.mu.Lock()
		rt.failAfterStagePersist = false
		rt.mu.Unlock()
		rt.restart()

		result, err := ImportCPA(context.Background(), options)
		if err != nil {
			t.Fatalf("cross-generation retry after persisted stage.json failed: %v", err)
		}
		if !slices.Contains(result.DomainsDone, model.UpgradeDomainCPAConfig) {
			t.Fatalf("DomainsDone = %v, want %s", result.DomainsDone, model.UpgradeDomainCPAConfig)
		}
		if rt.startCall == nil || rt.startCall.OperationID != record.OperationID+":start" || rt.startCall.ExpectedRuntimeGeneration != 5 {
			t.Fatalf("startCall = %+v, want %q on generation 5", rt.startCall, record.OperationID+":start")
		}
		afterRecord, afterKey, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
		if err != nil || !found || afterRecord.Attempt != 1 || !afterRecord.StartDispatched || afterRecord.StartAttempt != 1 || afterKey != firstKey {
			t.Fatalf("final record = %+v (found=%t err=%v), want Attempt=1 StartAttempt=1 StartDispatched=true same key", afterRecord, found, err)
		}
	})

	t.Run("transient StageDestination failure on G+1 before reaching Supervisor retries cleanly on G+1", func(t *testing.T) {
		fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
		destination := newFakeDestinationCPA(t, "")
		rt := &crossGenStageRuntime{
			fakeImportRuntime:     newFakeImportRuntime(model.RuntimeStateOffline),
			failAfterStagePersist: true,
		}
		rt.staged = destination
		options := importOptions(fixture, rt, destination.url(), boolPointer(true))

		if _, err := ImportCPA(context.Background(), options); err == nil {
			t.Fatal("expected initial run to fail after persisting generation-4 stage.json")
		}
		paths := newTargetPaths(fixture.targetRoot)
		initialRecord, firstKey, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
		if err != nil || !found || initialRecord.Attempt != 1 || initialRecord.StartDispatched {
			t.Fatalf("initial record = %+v (found=%t err=%v)", initialRecord, found, err)
		}

		rt.mu.Lock()
		rt.failAfterStagePersist = false
		rt.failBeforeSupervisor = true
		rt.mu.Unlock()
		rt.restart()

		_, err = ImportCPA(context.Background(), options)
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationUnready {
			t.Fatalf("transient G+1 failure = %v (code=%q/%t), want %s", err, code, ok, UpgradeErrorCPADestinationUnready)
		}
		midRecord, midKey, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
		if err != nil || !found || midRecord.Attempt != 1 || midRecord.OperationID != initialRecord.OperationID || midRecord.StartDispatched || midKey != firstKey {
			t.Fatalf("mid record = %+v, want unchanged Attempt=1 op=%q StartDispatched=false", midRecord, initialRecord.OperationID)
		}

		rt.mu.Lock()
		rt.failBeforeSupervisor = false
		rt.mu.Unlock()

		result, err := ImportCPA(context.Background(), options)
		if err != nil {
			t.Fatalf("second retry on generation 5 failed: %v", err)
		}
		if !slices.Contains(result.DomainsDone, model.UpgradeDomainCPAConfig) {
			t.Fatalf("DomainsDone = %v, want %s", result.DomainsDone, model.UpgradeDomainCPAConfig)
		}
		finalRecord, finalKey, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
		if err != nil || !found || finalRecord.Attempt != 1 || !finalRecord.StartDispatched || finalKey != firstKey {
			t.Fatalf("final record = %+v (found=%t err=%v), want Attempt=1 StartDispatched=true", finalRecord, found, err)
		}
	})
}

func TestImportCPARecoversCrossGenerationWhenRestartedChildIsStarting(t *testing.T) {
	t.Run("cross-generation Start leaves child Starting on G+1 and retry replays start:2 to completion", func(t *testing.T) {
		fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
		destination := newFakeDestinationCPA(t, "")
		rt := &crossGenStageRuntime{
			fakeImportRuntime: newFakeImportRuntime(model.RuntimeStateOffline),
		}
		rt.staged = destination
		rt.stayStartingForever = true
		plugins := false
		options := importOptions(fixture, rt, destination.server.URL, &plugins)
		paths := newTargetPaths(fixture.targetRoot)

		// Generation 4: StageDestination + initial :start succeed, child stays in
		// Starting until caller deadline expires.
		gen4Ctx, gen4Cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
		defer gen4Cancel()
		_, err := ImportCPA(gen4Ctx, options)
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationUnready {
			t.Fatalf("gen 4 initial start timeout = %v (code=%q/%t), want %s", err, code, ok, UpgradeErrorCPADestinationUnready)
		}
		recAfterGen4, firstKey, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
		if err != nil || !found || recAfterGen4.Attempt != 1 || recAfterGen4.StartAttempt != 1 ||
			recAfterGen4.StartRuntimeGeneration != 4 {
			t.Fatalf("record after gen 4 = %+v (found=%t err=%v), want Attempt=1 StartAttempt=1 StartRuntimeGeneration=4",
				recAfterGen4, found, err)
		}

		// Supervisor restarts to Generation 5 with child Offline.
		rt.mu.Lock()
		rt.state = model.RuntimeStateOffline
		rt.mu.Unlock()
		rt.restart()

		// First retry on Generation 5 mints :start:2, spawns the child into
		// RuntimeStateStarting, and times out before ready.
		gen5FirstCtx, gen5FirstCancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
		defer gen5FirstCancel()
		_, err = ImportCPA(gen5FirstCtx, options)
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationUnready {
			t.Fatalf("gen 5 first retry timeout = %v (code=%q/%t), want %s", err, code, ok, UpgradeErrorCPADestinationUnready)
		}
		if gotState := rt.currentState(); gotState != model.RuntimeStateStarting {
			t.Fatalf("runtime state after gen 5 first retry = %q, want %q", gotState, model.RuntimeStateStarting)
		}
		stageCallsMid, startCallsMid := rt.callCounts()
		if stageCallsMid != 1 || startCallsMid != 2 {
			t.Fatalf("mid callCounts = (stage=%d, start=%d), want (stage=1, start=2)", stageCallsMid, startCallsMid)
		}
		recMidGen5, midKey, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
		if err != nil || !found || recMidGen5.Attempt != 1 || recMidGen5.StartAttempt != 2 ||
			recMidGen5.StartRuntimeGeneration != 5 || midKey != firstKey {
			t.Fatalf("record mid gen 5 = %+v (found=%t err=%v), want Attempt=1 StartAttempt=2 StartRuntimeGeneration=5",
				recMidGen5, found, err)
		}

		// Second retry on Generation 5 while child is STILL in RuntimeStateStarting
		// must NOT fail with destination_request_changed, must NOT call
		// StageDestination, and must replay :start:2 without minting :start:3.
		rt.mu.Lock()
		rt.stayStartingForever = false
		rt.startingPollsBeforeReady = 1
		rt.mu.Unlock()

		result, err := ImportCPA(context.Background(), options)
		if err != nil {
			t.Fatalf("gen 5 second retry while Starting failed: %v", err)
		}
		if !slices.Contains(result.DomainsDone, model.UpgradeDomainCPAConfig) {
			t.Fatalf("DomainsDone = %v, want %s", result.DomainsDone, model.UpgradeDomainCPAConfig)
		}
		stageCallsFinal, startCallsFinal := rt.callCounts()
		if stageCallsFinal != 1 || startCallsFinal != 3 {
			t.Fatalf("final callCounts = (stage=%d, start=%d), want (stage=1, start=3)", stageCallsFinal, startCallsFinal)
		}
		wantStart2 := recAfterGen4.OperationID + ":start:2"
		if rt.startCall == nil || rt.startCall.OperationID != wantStart2 {
			t.Fatalf("last Start call = %+v, want OperationID=%q", rt.startCall, wantStart2)
		}
		recFinal, finalKey, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
		if err != nil || !found || recFinal.Attempt != 1 || recFinal.StartAttempt != 2 ||
			recFinal.StartRuntimeGeneration != 5 || finalKey != firstKey {
			t.Fatalf("final record = %+v (found=%t err=%v), want Attempt=1 StartAttempt=2 StartRuntimeGeneration=5",
				recFinal, found, err)
		}
	})

	t.Run("unowned Starting process on a newer generation G+2 fails closed", func(t *testing.T) {
		fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
		destination := newFakeDestinationCPA(t, "")
		rt := &crossGenStageRuntime{
			fakeImportRuntime: newFakeImportRuntime(model.RuntimeStateOffline),
		}
		rt.staged = destination
		rt.stayStartingForever = true
		plugins := false
		options := importOptions(fixture, rt, destination.server.URL, &plugins)

		shortCtx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
		defer cancel()
		_, _ = ImportCPA(shortCtx, options)

		// Restart to generation 5 in Starting without ImportCPA ever dispatching a
		// start on generation 5 (StartRuntimeGeneration is still 4).
		rt.restart()
		rt.mu.Lock()
		rt.state = model.RuntimeStateStarting
		rt.mu.Unlock()

		_, err := ImportCPA(context.Background(), options)
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationRequestChanged {
			t.Fatalf("unowned Starting on G+1 = %v (code=%q/%t), want %s", err, code, ok, UpgradeErrorCPADestinationRequestChanged)
		}
	})

	t.Run("transport error before Supervisor on cross-generation start:2 replays start:2 without minting start:3", func(t *testing.T) {
		fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
		destination := newFakeDestinationCPA(t, "")
		rt := &crossGenStageRuntime{
			fakeImportRuntime: newFakeImportRuntime(model.RuntimeStateOffline),
		}
		rt.staged = destination
		rt.stayStartingForever = true
		plugins := false
		options := importOptions(fixture, rt, destination.server.URL, &plugins)
		paths := newTargetPaths(fixture.targetRoot)

		shortCtx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
		defer cancel()
		_, _ = ImportCPA(shortCtx, options)

		rt.mu.Lock()
		rt.state = model.RuntimeStateOffline
		rt.stayStartingForever = false
		rt.failStartBeforeSupervisor = true
		rt.mu.Unlock()
		rt.restart()

		_, err := ImportCPA(context.Background(), options)
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationUnready {
			t.Fatalf("transport failure on cross-gen start:2 = %v (code=%q/%t), want %s", err, code, ok, UpgradeErrorCPADestinationUnready)
		}

		rt.mu.Lock()
		rt.failStartBeforeSupervisor = false
		rt.mu.Unlock()

		result, err := ImportCPA(context.Background(), options)
		if err != nil {
			t.Fatalf("replay of cross-gen start:2 failed: %v", err)
		}
		if !slices.Contains(result.DomainsDone, model.UpgradeDomainCPAConfig) {
			t.Fatalf("DomainsDone = %v, want %s", result.DomainsDone, model.UpgradeDomainCPAConfig)
		}
		recFinal, _, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
		if err != nil || !found || recFinal.Attempt != 1 || recFinal.StartAttempt != 2 || recFinal.StartRuntimeGeneration != 5 {
			t.Fatalf("final record = %+v (found=%t err=%v), want Attempt=1 StartAttempt=2 StartRuntimeGeneration=5",
				recFinal, found, err)
		}
	})
}

// r5CrossGenRuntime wraps crossGenStageRuntime so tests can drive exact
// cross-generation Supervisor responses:
//   - Gen 4 persistence followed by transport loss -> Gen 5 operation_id_conflict
//     from StageDestination -> Start -> Starting timeout -> Gen 6 offline retry
//   - Gen 4 failed stage.json -> Gen 5 Start operation_state_conflict -> pinned
//     supersede rejection vs mock attempt:2 supersede completion
type r5CrossGenRuntime struct {
	*crossGenStageRuntime
	injectIDConflictOnGen     model.RuntimeGeneration
	allowCrossGenSupersedeApp bool
	allStartAttempts          int
}

func (r *r5CrossGenRuntime) Start(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	r.mu.Lock()
	r.allStartAttempts++
	r.mu.Unlock()
	return r.crossGenStageRuntime.Start(ctx, request)
}

func (r *r5CrossGenRuntime) StageDestination(ctx context.Context, request model.RuntimeDestinationRequest) (model.RuntimeOperationResult, error) {
	r.mu.Lock()
	gen := r.generation
	shouldConflict := r.injectIDConflictOnGen != 0 && gen == r.injectIDConflictOnGen && request.OperationID == cpaImportDestinationOperationID(request.SourceIdentity, 1)
	allowSupersede := r.allowCrossGenSupersedeApp && strings.HasSuffix(request.OperationID, ":attempt:2")
	r.mu.Unlock()

	if shouldConflict {
		r.mu.Lock()
		r.stageCalls++
		r.stageCall = &request
		r.mu.Unlock()
		return model.RuntimeOperationResult{}, &runtimeservice.ProtocolError{
			Code:       runtimeservice.ProtocolErrorOperationIDConflict,
			HTTPStatus: 409,
		}
	}
	if allowSupersede {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.stageCalls++
		r.stageCall = &request
		if err := r.validateSubmissionLocked(request.ExpectedRuntimeIdentity, request.ExpectedRuntimeGeneration); err != nil {
			return model.RuntimeOperationResult{}, err
		}
		if r.staged != nil {
			r.staged.mu.Lock()
			r.staged.management = request.ManagementKey
			r.staged.clientKeys = slices.Clone(request.APIKeys)
			r.staged.mu.Unlock()
		}
		res := model.RuntimeOperationResult{
			OperationID:       request.OperationID,
			OperationType:     model.RuntimeOperationStageDestination,
			RuntimeIdentity:   request.ExpectedRuntimeIdentity,
			RuntimeGeneration: request.ExpectedRuntimeGeneration,
			State:             model.RuntimeOperationSucceeded,
		}
		r.published = &request
		r.submitted[request.OperationID] = request
		r.journal[request.OperationID] = res
		return res, nil
	}
	return r.crossGenStageRuntime.StageDestination(ctx, request)
}

func TestImportCPARecoversCrossGenerationIDConflictStartTimeoutToCompletion(t *testing.T) {
	fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "")
	base := &crossGenStageRuntime{
		fakeImportRuntime:     newFakeImportRuntime(model.RuntimeStateOffline),
		failAfterStagePersist: true,
	}
	base.staged = destination
	rt := &r5CrossGenRuntime{
		crossGenStageRuntime:  base,
		injectIDConflictOnGen: 5,
	}
	plugins := false
	options := importOptions(fixture, rt, destination.server.URL, &plugins)
	paths := newTargetPaths(fixture.targetRoot)

	// Gen 4: StageDestination persists stage.json (Succeeded) on the Supervisor,
	// then drops the reply before Manager records StartDispatched.
	_, err := ImportCPA(context.Background(), options)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationUnready {
		t.Fatalf("gen 4 stage drop = %v (code=%q/%t), want %s", err, code, ok, UpgradeErrorCPADestinationUnready)
	}
	recAfterGen4, firstKey, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
	if err != nil || !found || recAfterGen4.Attempt != 1 || recAfterGen4.StartDispatched {
		t.Fatalf("record after gen 4 = %+v (found=%t err=%v), want Attempt=1 StartDispatched=false", recAfterGen4, found, err)
	}
	if _, ok := rt.journal[recAfterGen4.OperationID]; !ok {
		// Ensure the simulated Supervisor journal holds the persisted succeeded stage from Gen 4.
		req := model.RuntimeDestinationRequest{
			OperationID:               recAfterGen4.OperationID,
			ExpectedRuntimeIdentity:   rt.identity,
			ExpectedRuntimeGeneration: 4,
			ExpectedActiveArtifactID:  rt.artifact,
			SourceIdentity:            recAfterGen4.Intent.SourceIdentity,
			SourceClass:               managedDestinationSourceClass,
			APIKeys:                   slices.Clone(recAfterGen4.Intent.APIKeys),
			PluginsEnabled:            plugins,
			ManagementKey:             firstKey,
		}
		rt.mu.Lock()
		rt.published = &req
		rt.submitted[req.OperationID] = req
		rt.journal[req.OperationID] = model.RuntimeOperationResult{
			OperationID:       req.OperationID,
			OperationType:     model.RuntimeOperationStageDestination,
			RuntimeIdentity:   rt.identity,
			RuntimeGeneration: 4,
			State:             model.RuntimeOperationSucceeded,
		}
		rt.mu.Unlock()
	}

	// Restart Supervisor to Gen 5: probe sees operation_id_conflict -> dispatches
	// Start -> child remains Starting past the deadline.
	rt.mu.Lock()
	rt.failAfterStagePersist = false
	if rt.generation == 4 {
		rt.generation = 5
	}
	rt.stayStartingForever = true
	rt.state = model.RuntimeStateOffline
	rt.mu.Unlock()

	gen5Ctx, gen5Cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer gen5Cancel()
	_, err = ImportCPA(gen5Ctx, options)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationUnready {
		t.Fatalf("gen 5 id-conflict start timeout = %v (code=%q/%t), want %s", err, code, ok, UpgradeErrorCPADestinationUnready)
	}
	recAfterGen5, keyAfterGen5, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
	if err != nil || !found || recAfterGen5.Attempt != 1 || recAfterGen5.StartAttempt != 1 || !recAfterGen5.StartDispatched || keyAfterGen5 != firstKey {
		t.Fatalf("record after gen 5 = %+v (found=%t err=%v), want Attempt=1 StartAttempt=1 StartDispatched=true", recAfterGen5, found, err)
	}
	stageCallsGen5, startCallsGen5 := rt.callCounts()

	// Restart Supervisor to Gen 6 (G_2) with offline child: retry must skip
	// StageDestination completely, increment StartAttempt to 2, and succeed.
	rt.mu.Lock()
	rt.stayStartingForever = false
	rt.startingPollsBeforeReady = 1
	rt.generation = 6
	rt.state = model.RuntimeStateOffline
	rt.mu.Unlock()

	result, err := ImportCPA(context.Background(), options)
	if err != nil {
		t.Fatalf("gen 6 retry after id-conflict Start timeout failed: %v", err)
	}
	if !slices.Contains(result.DomainsDone, model.UpgradeDomainCPAConfig) {
		t.Fatalf("DomainsDone = %v, want %s", result.DomainsDone, model.UpgradeDomainCPAConfig)
	}
	stageCallsGen6, startCallsGen6 := rt.callCounts()
	if stageCallsGen6 != stageCallsGen5 {
		t.Fatalf("gen 6 called StageDestination (%d -> %d), want zero extra StageDestination calls", stageCallsGen5, stageCallsGen6)
	}
	if startCallsGen6 <= startCallsGen5 {
		t.Fatalf("gen 6 did not dispatch Start (%d -> %d)", startCallsGen5, startCallsGen6)
	}
	finalRec, finalKey, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
	if err != nil || !found || finalRec.Attempt != 1 || finalRec.StartAttempt != 2 || !finalRec.StartDispatched || finalKey != firstKey {
		t.Fatalf("final record = %+v (found=%t err=%v), want Attempt=1 StartAttempt=2 StartDispatched=true", finalRec, found, err)
	}
}

func TestImportCPACrossGenerationFailedStageStateConflictHandling(t *testing.T) {
	setupFailedGen4 := func(t *testing.T, allowSupersede bool) (importFixture, *fakeDestinationCPA, *r5CrossGenRuntime, CPAImportOptions, string) {
		t.Helper()
		fixture := newImportFixture(t, v8FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
		destination := newFakeDestinationCPA(t, "")
		base := &crossGenStageRuntime{
			fakeImportRuntime:     newFakeImportRuntime(model.RuntimeStateOffline),
			failAfterStagePersist: true,
		}
		base.staged = destination
		rt := &r5CrossGenRuntime{
			crossGenStageRuntime:      base,
			injectIDConflictOnGen:     5,
			allowCrossGenSupersedeApp: allowSupersede,
		}
		plugins := false
		options := importOptions(fixture, rt, destination.server.URL, &plugins)
		if _, err := ImportCPA(context.Background(), options); err == nil {
			t.Fatal("expected gen 4 injected drop to fail")
		}
		paths := newTargetPaths(fixture.targetRoot)
		rec, key, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
		if err != nil || !found {
			t.Fatalf("load gen 4 record: found=%t err=%v", found, err)
		}
		// Simulate Gen 4 stage.json persisted while its journal entry holds StateFailed
		// on the Supervisor before restart to Gen 5.
		rt.mu.Lock()
		rt.failAfterStagePersist = false
		rt.journal[rec.OperationID] = model.RuntimeOperationResult{
			OperationID:       rec.OperationID,
			OperationType:     model.RuntimeOperationStageDestination,
			RuntimeIdentity:   rt.identity,
			RuntimeGeneration: 4,
			State:             model.RuntimeOperationFailed,
			Failure:           &model.RuntimeOperationFailure{Code: "stage_failed"},
		}
		rt.generation = 5
		rt.state = model.RuntimeStateOffline
		rt.mu.Unlock()
		return fixture, destination, rt, options, key
	}

	t.Run("pinned crossGenStageRuntime restores previousRecord StartDispatched=false after operation_state_conflict", func(t *testing.T) {
		fixture, _, rt, options, firstKey := setupFailedGen4(t, false)
		paths := newTargetPaths(fixture.targetRoot)

		_, err := ImportCPA(context.Background(), options)
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationUnready {
			t.Fatalf("pinned cross-gen failed stage retry = %v (code=%q/%t), want %s", err, code, ok, UpgradeErrorCPADestinationUnready)
		}
		rt.mu.Lock()
		allStarts := rt.allStartAttempts
		rt.mu.Unlock()
		if allStarts == 0 {
			t.Fatal("expected probe Start attempt before operation_state_conflict fallback")
		}
		recAfter, keyAfter, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
		if err != nil || !found {
			t.Fatalf("load record after rollback restore: found=%t err=%v", found, err)
		}
		if recAfter.Attempt != 1 || recAfter.StartDispatched || keyAfter != firstKey {
			t.Fatalf("restored record = %+v, want Attempt=1 StartDispatched=false preserving initial secret", recAfter)
		}
	})

	t.Run("mock runtime supporting cross-gen supersede succeeds on attempt:2 with same secret", func(t *testing.T) {
		fixture, _, rt, options, firstKey := setupFailedGen4(t, true)
		paths := newTargetPaths(fixture.targetRoot)

		result, err := ImportCPA(context.Background(), options)
		if err != nil {
			t.Fatalf("cross-gen supersede attempt:2 failed: %v", err)
		}
		if !slices.Contains(result.DomainsDone, model.UpgradeDomainCPAConfig) {
			t.Fatalf("DomainsDone = %v, want %s", result.DomainsDone, model.UpgradeDomainCPAConfig)
		}
		recAfter, keyAfter, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
		if err != nil || !found {
			t.Fatalf("load record after supersede completion: found=%t err=%v", found, err)
		}
		if recAfter.Attempt != 2 || !strings.HasSuffix(recAfter.OperationID, ":attempt:2") || keyAfter != firstKey {
			t.Fatalf("record = %+v (key match=%t), want Attempt=2 with preserved secret", recAfter, keyAfter == firstKey)
		}
		rt.mu.Lock()
		allStarts := rt.allStartAttempts
		rt.mu.Unlock()
		if allStarts < 2 {
			t.Fatalf("allStartAttempts = %d, want >= 2 (rejected start + attempt:2 start)", allStarts)
		}
	})
}
