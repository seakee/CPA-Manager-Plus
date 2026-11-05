package upgrade

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	verifyExternalAddress   = "https://cpa.example"
	verifyManagedAddress    = "http://127.0.0.1:28317"
	verifyManagedSecret     = "managed-destination-secret"
	verifyDestinationSource = "1x-managed-source-identity"
)

// verifyFixture is a recorded operation driven through the real steps. Every
// step runs its production entry point — CopyIn, Reconcile, ExternalConnect or
// the CPA import's own recorders — and releases the fences it took, so the
// verification under test acquires the two fences itself exactly as the CLI and
// the wizard action do.
type verifyFixture struct {
	volume legacyVolume
	target string
	paths  targetPaths
}

// newVerifyFixture copies a real 1.x fixture into a fresh v2 data root. The
// copy-in takes the source fence; it is released as soon as the step returns, so
// the fixture never presents the source as a live writer.
func newVerifyFixture(t *testing.T) *verifyFixture {
	t.Helper()
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	lock, err := processlock.AcquireExisting(volume.databasePath())
	if err != nil {
		t.Fatalf("acquire the copy-in source fence: %v", err)
	}
	options.Lock = lock
	_, copyErr := CopyIn(context.Background(), options)
	if err := lock.Close(); err != nil {
		t.Fatalf("release the copy-in source fence: %v", err)
	}
	if copyErr != nil {
		t.Fatalf("copy in: %v", copyErr)
	}
	return &verifyFixture{volume: volume, target: targetRoot, paths: newTargetPaths(targetRoot)}
}

func (f *verifyFixture) databasePath() string { return f.paths.Database }

func (f *verifyFixture) sourceLock(t *testing.T) *processlock.Lock {
	t.Helper()
	lock, err := processlock.AcquireExisting(f.volume.databasePath())
	if err != nil {
		t.Fatalf("acquire the 1.x ownership fence: %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return lock
}

func (f *verifyFixture) targetLock(t *testing.T) *processlock.Lock {
	t.Helper()
	lock, err := processlock.Acquire(f.databasePath())
	if err != nil {
		t.Fatalf("acquire the v2 data-root fence: %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return lock
}

// reconcile runs the real comparison under the two fences the step requires and
// releases them, so the verification finds both free.
func (f *verifyFixture) reconcile(t *testing.T) ReconcileReport {
	t.Helper()
	sourceLock := f.sourceLock(t)
	targetLock := f.targetLock(t)
	report, err := Reconcile(context.Background(), Options{
		LegacyRoot: f.volume.root,
		TargetRoot: f.target,
		BundleDir:  DefaultBundleDirectory(f.target),
		Lock:       sourceLock,
		TargetLock: targetLock,
	})
	if closeErr := errors.Join(sourceLock.Close(), targetLock.Close()); closeErr != nil {
		t.Fatalf("release the reconciliation fences: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.Recorded {
		t.Fatalf("reconcile did not record its evidence: %+v", report)
	}
	return report
}

// connectExternal runs the real External connect step against a synthetic v8
// CPA. Every probe is a seam, so no test reaches the network.
func (f *verifyFixture) connectExternal(t *testing.T) ExternalConnectResult {
	t.Helper()
	result, err := ExternalConnect(context.Background(), ExternalConnectOptions{
		TargetRoot:    f.target,
		BaseURL:       verifyExternalAddress,
		ManagementKey: externalTestKey,
		observeExternalVersion: func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
			return cpa.ClassifyExternalVersion("v8.0.13"), nil
		},
		readExternalKeys: func(context.Context, string, string) (cpa.ExternalKeySnapshot, error) {
			return cpa.ExternalKeySnapshot{Keys: []string{"client-key-1"}, SourceSHA256: strings.Repeat("a", 64)}, nil
		},
		readV8Config: func(context.Context, string, string) (cpa.V8ConfigObservation, error) {
			return cpa.V8ConfigObservation{BaseURL: verifyExternalAddress, ClientKeyCount: 1}, nil
		},
		proveModelSurface: func(context.Context, string, string) error { return nil },
	})
	if err != nil {
		t.Fatalf("external connect: %v", err)
	}
	if result.Stage != model.UpgradeStageExternalConnect {
		t.Fatalf("connect stage = %q", result.Stage)
	}
	return result
}

// handManagerConnection stores the CPA the Manager itself resolves, which the
// External verification compares the recorded binding against.
func (f *verifyFixture) handManagerConnection(t *testing.T, address, secret string) {
	t.Helper()
	key, _, err := security.LoadOrCreateDataKey("", f.paths.DataKey)
	if err != nil {
		t.Fatalf("load the Manager data key: %v", err)
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		t.Fatalf("initialize the secret protector: %v", err)
	}
	db, err := managersqlite.Open(f.databasePath())
	if err != nil {
		t.Fatalf("open the v2 database: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := store.New(db, protector).SaveManagerConfig(context.Background(), model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: address, ManagementKey: secret},
	}); err != nil {
		t.Fatalf("store the Manager CPA connection: %v", err)
	}
}

// verifyManagedFingerprint is the server fingerprint the synthetic probe seams
// below report, so the evidence the fixture records is the one the live re-proof
// derives.
func verifyManagedFingerprint() string {
	return freshkeyapply.ExternalServerFingerprint("v8.0.13", strings.Repeat("a", 64))
}

// recordManagedImport records what the CPA import leaves behind for a Managed
// target: the handoff connection, the proved destination attempt with the server
// facts the run observed, the cpa_import stage with its cpa_config domain, and the
// manual items the adoption rules keep manual.
func (f *verifyFixture) recordManagedImport(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := enterCPAImportStage(ctx, f.databasePath()); err != nil {
		t.Fatalf("enter the CPA import stage: %v", err)
	}
	if err := handDestinationConnection(ctx, f.databasePath(), f.paths.DataKey, verifyManagedAddress, verifyManagedSecret); err != nil {
		t.Fatalf("hand the destination connection over: %v", err)
	}
	if err := saveCPAImportDestinationRecord(ctx, f.databasePath(), f.paths.DataKey, cpaImportDestinationRecord{
		BaseURL:           cpa.NormalizeBaseURL(verifyManagedAddress),
		URLProved:         true,
		ServerFingerprint: verifyManagedFingerprint(),
		Intent:            cpaImportDestinationIntent{SourceIdentity: verifyDestinationSource},
		OperationID:       cpaImportDestinationOperationID(verifyDestinationSource, 1),
		Attempt:           1,
	}, verifyManagedSecret); err != nil {
		t.Fatalf("record the destination attempt: %v", err)
	}
	if _, err := recordCPAImportCompletion(ctx, f.databasePath(), CPAImportResult{
		Stage:             model.UpgradeStageCPAImport,
		DestinationURL:    verifyManagedAddress,
		ServerFingerprint: verifyManagedFingerprint(),
		ManualItems: []CPAImportManualItem{{
			Item:        "section:ampcode",
			Reason:      "reconfigure_after_adoption",
			Instruction: "re-create it in the destination CPA",
		}},
	}); err != nil {
		t.Fatalf("record the CPA import completion: %v", err)
	}
}

// verifyExternalFixture is the whole External pipeline, recorded.
func newExternalVerifyFixture(t *testing.T) *verifyFixture {
	t.Helper()
	fixture := newVerifyFixture(t)
	fixture.reconcile(t)
	fixture.handManagerConnection(t, verifyExternalAddress, externalTestKey)
	fixture.connectExternal(t)
	return fixture
}

// verifyManagedFixture is the whole Managed pipeline, recorded.
func newManagedVerifyFixture(t *testing.T) *verifyFixture {
	t.Helper()
	fixture := newVerifyFixture(t)
	fixture.reconcile(t)
	fixture.recordManagedImport(t)
	return fixture
}

// verifyOptions is the gate invocation with the same synthetic probe seams the
// connect step used, so an offline test proves the recorded target without
// dialing its address. Production leaves the seams nil.
func (f *verifyFixture) verifyOptions() VerifyOptions {
	return VerifyOptions{
		LegacyRoot: f.volume.root,
		TargetRoot: f.target,
		observeExternalVersion: func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
			return cpa.ClassifyExternalVersion("v8.0.13"), nil
		},
		readExternalKeys: func(context.Context, string, string) (cpa.ExternalKeySnapshot, error) {
			return cpa.ExternalKeySnapshot{Keys: []string{"client-key-1"}, SourceSHA256: strings.Repeat("a", 64)}, nil
		},
		readV8Config: func(context.Context, string, string) (cpa.V8ConfigObservation, error) {
			return cpa.V8ConfigObservation{BaseURL: verifyExternalAddress, ClientKeyCount: 1}, nil
		},
		proveModelSurface: func(context.Context, string, string) error { return nil },
	}
}

func (f *verifyFixture) verify(t *testing.T) (VerifyResult, error) {
	t.Helper()
	return Verify(context.Background(), f.verifyOptions())
}

func (f *verifyFixture) operation(t *testing.T) model.UpgradeOperation {
	t.Helper()
	return operationRow(t, f.databasePath())
}

// TestVerifyCompletesTheExternalOperation is the External contract end to end:
// the recorded copy-in, reconciliation and connection are re-proved, and the
// verdict and its evidence are recorded in one statement.
func TestVerifyCompletesTheExternalOperation(t *testing.T) {
	fixture := newExternalVerifyFixture(t)
	before := fixture.operation(t)
	fingerprint := before.SourceFingerprintAfter

	result, err := fixture.verify(t)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !result.Complete || result.Stage != model.UpgradeStageComplete {
		t.Fatalf("result = %+v, want a completed operation", result)
	}
	if result.TargetMode != model.UpgradeTargetExternal {
		t.Fatalf("target mode = %q, want external", result.TargetMode)
	}
	if result.ReasonCode != "" || result.Reason != "" {
		t.Fatalf("a completed operation reported a reason: %+v", result)
	}
	wantDomains := []string{
		model.UpgradeDomainSourceCopy,
		model.UpgradeDomainRetainedProjection,
		model.UpgradeDomainPricing,
		model.UpgradeDomainCoverage,
		model.UpgradeDomainIdentity,
		model.UpgradeDomainCPAConfig,
	}
	if !hasDomains(result.DomainsDone, wantDomains...) {
		t.Fatalf("domains = %v, want %v", result.DomainsDone, wantDomains)
	}
	if slicesContain(result.DomainsDone, model.UpgradeDomainUsageEvents) {
		t.Fatalf("domains = %v, want no domain no step recorded", result.DomainsDone)
	}

	recorded := fixture.operation(t)
	if recorded.Stage != model.UpgradeStageComplete {
		t.Fatalf("stage = %q, want complete", recorded.Stage)
	}
	evidence, err := recorded.VerificationEvidence()
	if err != nil {
		t.Fatalf("decode the recorded verification: %v", err)
	}
	if evidence.TargetMode != model.UpgradeTargetExternal || evidence.SourceFingerprint != fingerprint {
		t.Fatalf("evidence = %+v, want the external target on the recorded fingerprint", evidence)
	}
	if evidence.BindingIdentity == "" || evidence.BindingIdentity != evidence.BindingGeneration {
		t.Fatalf("evidence binding = %q/%q, want the connect step's identity", evidence.BindingIdentity, evidence.BindingGeneration)
	}
	// The verdict names every applicable domain and no other: the record's own
	// order is the row's, so the sets are compared rather than the sequences.
	if len(evidence.Domains) != len(wantDomains) || !hasDomains(evidence.Domains, wantDomains...) {
		t.Fatalf("evidence domains = %v, want %v", evidence.Domains, wantDomains)
	}
	if slicesContain(evidence.Domains, model.UpgradeDomainUsageEvents) {
		t.Fatalf("evidence names a domain no step recorded: %v", evidence.Domains)
	}
	// The completion re-proved provenance: the recorded source observations and
	// the copy-in's own facts are untouched.
	if recorded.SourceFingerprintBefore != before.SourceFingerprintBefore ||
		recorded.SourceFingerprintAfter != before.SourceFingerprintAfter ||
		recorded.BackupManifestSHA256 != before.BackupManifestSHA256 {
		t.Fatalf("verification rewrote provenance: %+v -> %+v", before, recorded)
	}
	if len(recorded.ManualItems) != len(before.ManualItems) {
		t.Fatalf("manual items = %v, want the recorded ones %v", recorded.ManualItems, before.ManualItems)
	}

	// A repeat is the same verdict, not a new record: nothing is written, and the
	// source is not observed again.
	repeat, err := fixture.verify(t)
	if err != nil {
		t.Fatalf("repeat verify: %v", err)
	}
	if !repeat.Complete || !repeat.AlreadyVerified {
		t.Fatalf("repeat = %+v, want the recorded verdict reported", repeat)
	}
	after := fixture.operation(t)
	if after.UpdatedAtMS != recorded.UpdatedAtMS || after.Revision != recorded.Revision {
		t.Fatalf("repeat wrote the row: %d/%d -> %d/%d", recorded.UpdatedAtMS, recorded.Revision, after.UpdatedAtMS, after.Revision)
	}
	if !slices.Equal(after.DomainsDone, recorded.DomainsDone) {
		t.Fatalf("repeat changed the domains: %v -> %v", recorded.DomainsDone, after.DomainsDone)
	}
}

// TestVerifyCompletesTheManagedOperation is the Managed contract end to end: the
// recorded copy-in, reconciliation, imported destination and Manager connection
// are re-proved against the live CPA, and the verdict and its evidence are
// recorded as `complete` in one statement.
func TestVerifyCompletesTheManagedOperation(t *testing.T) {
	fixture := newManagedVerifyFixture(t)
	before := fixture.operation(t)
	if before.Stage != model.UpgradeStageCPAImport {
		t.Fatalf("fixture stage = %q, want cpa_import", before.Stage)
	}
	if !before.HasDomain(model.UpgradeDomainCPAConfig) {
		t.Fatal("fixture recorded no cpa_config, so the import never settled its copy")
	}

	result, err := fixture.verify(t)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !result.Complete || result.Stage != model.UpgradeStageComplete {
		t.Fatalf("result = %+v, want a completed operation", result)
	}
	if result.TargetMode != model.UpgradeTargetManaged {
		t.Fatalf("target mode = %q, want managed", result.TargetMode)
	}
	if result.ReasonCode != "" || result.Reason != "" {
		t.Fatalf("a completed operation reported a reason: %+v", result)
	}
	wantDomains := []string{
		model.UpgradeDomainSourceCopy,
		model.UpgradeDomainRetainedProjection,
		model.UpgradeDomainPricing,
		model.UpgradeDomainCoverage,
		model.UpgradeDomainCPAConfig,
	}
	if !hasDomains(result.DomainsDone, wantDomains...) {
		t.Fatalf("domains = %v, want %v", result.DomainsDone, wantDomains)
	}
	if slicesContain(result.DomainsDone, model.UpgradeDomainIdentity) {
		t.Fatalf("a Managed completion claimed the External identity domain: %v", result.DomainsDone)
	}

	recorded := fixture.operation(t)
	if recorded.Stage != model.UpgradeStageComplete {
		t.Fatalf("recorded stage = %q, want complete", recorded.Stage)
	}
	evidence, err := recorded.VerificationEvidence()
	if err != nil {
		t.Fatalf("decode the recorded verification: %v", err)
	}
	if evidence.TargetMode != model.UpgradeTargetManaged {
		t.Fatalf("evidence target mode = %q, want managed", evidence.TargetMode)
	}
	if evidence.BindingIdentity != "" || evidence.BindingGeneration != "" {
		t.Fatalf("a Managed verification named a binding: %+v", evidence)
	}
	// Every manual item the import recorded survives: completion is not a
	// statement that the operator's remaining work disappeared.
	if len(recorded.ManualItems) != len(before.ManualItems) {
		t.Fatalf("manual items = %v, want the import's %v", recorded.ManualItems, before.ManualItems)
	}

	// A repeat is the same verdict, not a new record: nothing is written.
	repeat, err := fixture.verify(t)
	if err != nil {
		t.Fatalf("repeat verify: %v", err)
	}
	if !repeat.Complete || !repeat.AlreadyVerified {
		t.Fatalf("repeat = %+v, want the recorded verdict reported", repeat)
	}
	after := fixture.operation(t)
	if after.Revision != recorded.Revision || after.UpdatedAtMS != recorded.UpdatedAtMS {
		t.Fatalf("repeat wrote the row: %d/%d -> %d/%d", recorded.Revision, recorded.UpdatedAtMS, after.Revision, after.UpdatedAtMS)
	}
}

// TestVerifyRefusesAnUnmountedSource keeps the fingerprint non-negotiable: a
// gate that skipped it would claim the volume was unchanged without looking.
func TestVerifyRefusesAnUnmountedSource(t *testing.T) {
	fixture := newExternalVerifyFixture(t)
	before := fixture.operation(t)
	_, err := Verify(context.Background(), VerifyOptions{TargetRoot: fixture.target})
	if !errors.Is(err, ErrVerifySourceUnavailable) {
		t.Fatalf("verify without a source error = %v, want ErrVerifySourceUnavailable", err)
	}
	assertVerifyRefusalWroteNothing(t, fixture, before)
}

// TestVerifyRefusesAPositionBeforeTheTargetStep refuses a row that never reached
// the step the target mode needs.
func TestVerifyRefusesAPositionBeforeTheTargetStep(t *testing.T) {
	fixture := newVerifyFixture(t)
	before := fixture.operation(t)
	if before.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("fixture stage = %q, want copy_in", before.Stage)
	}
	_, err := fixture.verify(t)
	if !errors.Is(err, ErrVerifyNotApplicable) {
		t.Fatalf("verify from copy_in error = %v, want ErrVerifyNotApplicable", err)
	}
	assertVerifyRefusalWroteNothing(t, fixture, before)
}

// TestVerifyRefusesADomainTheReconciliationEstablished: a comparison that
// established a domain the row does not hold proves no copy for it.
func TestVerifyRefusesADomainTheReconciliationEstablished(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.handManagerConnection(t, verifyExternalAddress, externalTestKey)
	fixture.connectExternal(t)
	// The reconciliation is recorded as evidence only: its domains are never
	// marked on the row, so the row cannot prove the copy it describes.
	recordReconciliationBody(t, fixture, comparedEvidenceBody(t, fixture))
	before := fixture.operation(t)
	if before.HasDomain(model.UpgradeDomainPricing) {
		t.Fatal("fixture recorded pricing, which would make the domain check pass")
	}
	_, err := fixture.verify(t)
	if !errors.Is(err, ErrVerifyDomainsIncomplete) {
		t.Fatalf("verify with a missing domain error = %v, want ErrVerifyDomainsIncomplete", err)
	}
	assertVerifyRefusalWroteNothing(t, fixture, before)
}

// TestVerifyRefusesAnUnreadableReconciliation refuses an evidence body this
// build cannot read as the comparison the step wrote.
func TestVerifyRefusesAnUnreadableReconciliation(t *testing.T) {
	fixture := newManagedVerifyFixture(t)
	recordRawReconciliationBody(t, fixture, json.RawMessage(`{"checked":true}`))
	before := fixture.operation(t)
	_, err := fixture.verify(t)
	if !errors.Is(err, ErrVerifyReconciliationInvalid) {
		t.Fatalf("verify with an unreadable reconciliation error = %v, want ErrVerifyReconciliationInvalid", err)
	}
	assertVerifyRefusalWroteNothing(t, fixture, before)
}

// TestVerifyRefusesAForeignReconciliationFingerprint refuses evidence that names
// another volume than the one that is mounted.
func TestVerifyRefusesAForeignReconciliationFingerprint(t *testing.T) {
	fixture := newManagedVerifyFixture(t)
	body := reconcileEvidenceBody(t, fixture, nil)
	body.SourceFingerprint = "sha256:another-volume"
	recordReconciliationBody(t, fixture, body)
	before := fixture.operation(t)
	_, err := fixture.verify(t)
	if !errors.Is(err, ErrVerifySourceMismatch) {
		t.Fatalf("verify with a foreign reconciliation fingerprint error = %v, want ErrVerifySourceMismatch", err)
	}
	assertVerifyRefusalWroteNothing(t, fixture, before)
}

// TestVerifyRefusesAnUnacknowledgedDay re-derives the reconciliation contract:
// a day the record leaves undecided is a refusal, not a pass hidden behind the
// other domains being clean.
func TestVerifyRefusesAnUnacknowledgedDay(t *testing.T) {
	fixture := newManagedVerifyFixture(t)
	body := reconcileEvidenceBody(t, fixture, nil)
	body.MismatchDays = []string{"2026-02-03"}
	recordReconciliationBody(t, fixture, body)
	before := fixture.operation(t)
	_, err := fixture.verify(t)
	if !errors.Is(err, ErrVerifyReconciliationUnacked) {
		t.Fatalf("verify with an undecided day error = %v, want ErrVerifyReconciliationUnacked", err)
	}
	assertVerifyRefusalWroteNothing(t, fixture, before)
}

// TestVerifyRefusesADroppedCoverageMark refuses an acknowledgement whose durable
// mark no longer exists: the mark is the half of the decision that keeps the
// covered number from reading as exact.
func TestVerifyRefusesADroppedCoverageMark(t *testing.T) {
	day := "2026-02-04"
	// acknowledgedBody is the same decision expressed on each fixture's own
	// volume: the fingerprint belongs to the record, so it cannot be borrowed.
	acknowledgedBody := func(fixture *verifyFixture) ReconcileEvidence {
		body := reconcileEvidenceBody(t, fixture, nil)
		body.ParityGapDays = []string{day}
		body.MismatchDays = []string{day}
		body.AcknowledgedDays = []string{day}
		body.IncompleteDays = []ReconcileMarkedDay{{Day: day, Reason: "projection_lost_before_v1_14_6"}}
		return body
	}

	fixture := newManagedVerifyFixture(t)
	execTarget(t, fixture.databasePath(),
		`insert into usage_coverage_incomplete_days (day, reason, source, recorded_at_ms)
		 values ('`+day+`', 'projection_lost_before_v1_14_6', 'reconcile', 1)`)
	recordReconciliationBody(t, fixture, acknowledgedBody(fixture))
	if _, err := fixture.verify(t); err != nil {
		// The mark is on disk, so this evidence is acceptable: the gate accepts an
		// acknowledged day it can still re-check.
		t.Fatalf("verify with a recorded coverage mark: %v", err)
	}
	// The acceptance above is the control. A second operation, whose store holds
	// no such mark, must be refused instead: the acknowledgement would otherwise
	// present a covered number as exact.
	dropped := newManagedVerifyFixture(t)
	recordReconciliationBody(t, dropped, acknowledgedBody(dropped))
	before := dropped.operation(t)
	_, err := dropped.verify(t)
	if !errors.Is(err, ErrVerifyReconciliationUnacked) {
		t.Fatalf("verify with a dropped coverage mark error = %v, want ErrVerifyReconciliationUnacked", err)
	}
	assertVerifyRefusalWroteNothing(t, dropped, before)
}

// TestVerifyRefusesAMissingBinding refuses a row that reached the connect stage
// without the target binding that stage records.
func TestVerifyRefusesAMissingBinding(t *testing.T) {
	fixture := newExternalVerifyFixture(t)
	execTarget(t, fixture.databasePath(), `delete from upgrade_external_connect`)
	before := fixture.operation(t)
	_, err := fixture.verify(t)
	if !errors.Is(err, ErrVerifyBindingMissing) {
		t.Fatalf("verify without a binding error = %v, want ErrVerifyBindingMissing", err)
	}
	assertVerifyRefusalWroteNothing(t, fixture, before)
}

// TestVerifyRefusesADriftedBinding compares the binding with the CPA the Manager
// itself resolves, never with a caller's claim.
func TestVerifyRefusesADriftedBinding(t *testing.T) {
	fixture := newExternalVerifyFixture(t)
	// The Manager is repointed at another CPA after the connect step recorded
	// its target.
	fixture.handManagerConnection(t, "https://other-cpa.example", externalTestKey)
	before := fixture.operation(t)
	_, err := fixture.verify(t)
	if !errors.Is(err, ErrVerifyBindingDrifted) {
		t.Fatalf("verify against a repointed Manager connection error = %v, want ErrVerifyBindingDrifted", err)
	}
	assertVerifyRefusalWroteNothing(t, fixture, before)
}

// TestVerifyRefusesAManagedConnectionThatDidNotSurvive refuses a handoff the
// Manager no longer carries with the secret the import recorded.
func TestVerifyRefusesAManagedConnectionThatDidNotSurvive(t *testing.T) {
	fixture := newManagedVerifyFixture(t)
	fixture.handManagerConnection(t, verifyManagedAddress, "another-management-secret")
	before := fixture.operation(t)
	_, err := fixture.verify(t)
	if !errors.Is(err, ErrVerifyConnectionUnproven) {
		t.Fatalf("verify with a lost handoff error = %v, want ErrVerifyConnectionUnproven", err)
	}
	assertVerifyRefusalWroteNothing(t, fixture, before)
}

// TestVerifyRefusesASourceThatWasWritten proves the mounted volume is the one
// the copy-in read: a source written after the copy-in no longer matches the
// recorded digest.
func TestVerifyRefusesASourceThatWasWritten(t *testing.T) {
	fixture := newExternalVerifyFixture(t)
	execSource(t, fixture.volume,
		`insert into settings (key, value, updated_at_ms) values ('written_after_the_copy', 'yes', 3)`)
	before := fixture.operation(t)
	_, err := fixture.verify(t)
	if !errors.Is(err, ErrVerifySourceMismatch) {
		t.Fatalf("verify against a written source error = %v, want ErrVerifySourceMismatch", err)
	}
	assertVerifyRefusalWroteNothing(t, fixture, before)
}

// TestVerifyRefusesAHeldTargetFence refuses a store another writer owns instead
// of sharing it with a completion verdict.
func TestVerifyRefusesAHeldTargetFence(t *testing.T) {
	fixture := newExternalVerifyFixture(t)
	before := fixture.operation(t)
	held := fixture.targetLock(t)
	_, err := fixture.verify(t)
	if !errors.Is(err, ErrVerifyV2Running) {
		t.Fatalf("verify with the v2 data root held error = %v, want ErrVerifyV2Running", err)
	}
	if err := held.Close(); err != nil {
		t.Fatalf("release the held fence: %v", err)
	}
	assertVerifyRefusalWroteNothing(t, fixture, before)
}

// TestVerifyRefusesALiveSourceWriter refuses to observe a source a writer holds:
// the fingerprint would describe a volume that is still changing.
func TestVerifyRefusesALiveSourceWriter(t *testing.T) {
	fixture := newExternalVerifyFixture(t)
	before := fixture.operation(t)
	_ = fixture.sourceLock(t)
	_, err := fixture.verify(t)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCode(CodeSourceBusy) {
		t.Fatalf("verify against a live source writer error = %v, want the source-busy refusal", err)
	}
	assertVerifyRefusalWroteNothing(t, fixture, before)
}

// TestVerifyRefusesARolledBackOperation keeps the terminal verdict terminal.
func TestVerifyRefusesARolledBackOperation(t *testing.T) {
	fixture := newVerifyFixture(t)
	db, err := managersqlite.Open(fixture.databasePath())
	if err != nil {
		t.Fatalf("open the v2 database: %v", err)
	}
	if _, err := upgradeoperation.New(db).RecordRollback(context.Background(), model.RollbackEvidence{
		Image:             "seakee/cpa-manager-plus:v1.14.7",
		ImageSource:       "operator",
		ImageForm:         "release-tag",
		StatusURL:         "http://127.0.0.1:18317/health",
		SourceFingerprint: "observed-after-the-v1-restart",
		ObservedAtMS:      time.Now().UnixMilli(),
	}); err != nil {
		_ = db.Close()
		t.Fatalf("record the rollback: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the v2 database: %v", err)
	}
	before := fixture.operation(t)
	if before.Stage != model.UpgradeStageRolledBack {
		t.Fatalf("stage = %q, want rolled_back", before.Stage)
	}
	_, err = fixture.verify(t)
	if !errors.Is(err, ErrVerifyNotApplicable) {
		t.Fatalf("verify on a rolled-back operation error = %v, want ErrVerifyNotApplicable", err)
	}
	assertVerifyRefusalWroteNothing(t, fixture, before)
}

// TestVerifyStoreFaultLeavesNoCompleteWithoutEvidence injects a persistence
// fault into the verdict's own statement. The gate writes the evidence and the
// stage together; a store that refuses the write must leave the operation where
// it was, and the retry after the fault is gone must record the whole verdict.
func TestVerifyStoreFaultLeavesNoCompleteWithoutEvidence(t *testing.T) {
	fixture := newExternalVerifyFixture(t)
	before := fixture.operation(t)
	execTarget(t, fixture.databasePath(), `
		create trigger refuse_completion before update on upgrade_operations
		when new.stage = 'complete'
		begin
			select raise(abort, 'injected store fault');
		end`)
	if _, err := fixture.verify(t); err == nil {
		t.Fatal("verify reported success while the store refused the verdict")
	}
	failed := fixture.operation(t)
	if failed.Stage == model.UpgradeStageComplete {
		t.Fatal("a refused write left the operation complete")
	}
	evidence, _ := failed.VerificationEvidence()
	if evidence.SourceFingerprint != "" || evidence.TargetMode != "" {
		t.Fatalf("a refused write left evidence behind: %+v", evidence)
	}
	if failed.Revision != before.Revision || failed.UpdatedAtMS != before.UpdatedAtMS {
		t.Fatalf("a refused write moved the row: %d/%d -> %d/%d",
			before.Revision, before.UpdatedAtMS, failed.Revision, failed.UpdatedAtMS)
	}
	if !slices.Equal(failed.DomainsDone, before.DomainsDone) {
		t.Fatalf("a refused write changed the domains: %v -> %v", before.DomainsDone, failed.DomainsDone)
	}

	execTarget(t, fixture.databasePath(), `drop trigger refuse_completion`)
	result, err := fixture.verify(t)
	if err != nil {
		t.Fatalf("verify after the fault was removed: %v", err)
	}
	if !result.Complete {
		t.Fatalf("result after the fault was removed = %+v, want complete", result)
	}
	recorded := fixture.operation(t)
	if recorded.UpdatedAtMS < before.UpdatedAtMS {
		t.Fatalf("the verdict moved the timestamp backwards: %d -> %d", before.UpdatedAtMS, recorded.UpdatedAtMS)
	}
}

// TestVerifyConcurrentRunsRecordOneVerdict runs the gate twice at once. The v2
// data-root fence admits one writer, so exactly one verdict is recorded, the
// other is refused, and the row keeps every domain and never moves its
// timestamps backwards.
func TestVerifyConcurrentRunsRecordOneVerdict(t *testing.T) {
	fixture := newExternalVerifyFixture(t)
	before := fixture.operation(t)
	options := fixture.verifyOptions()

	var wait sync.WaitGroup
	results := make([]VerifyResult, 2)
	failures := make([]error, 2)
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], failures[index] = Verify(context.Background(), options)
		}(index)
	}
	wait.Wait()

	succeeded := 0
	for index := range results {
		if failures[index] == nil {
			succeeded++
			continue
		}
		// Both runs take the source fence before the target fence, so the loser is
		// refused by whichever fence it reached second. Either verdict means the
		// same thing: another writer owns the operation.
		code, ok := UpgradeErrorCodeOf(failures[index])
		if !ok || (code != UpgradeErrorVerifyV2Running && code != UpgradeErrorCode(CodeSourceBusy)) {
			t.Fatalf("concurrent verify error = %v, want a held-fence refusal", failures[index])
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent verifies that succeeded = %d, want exactly one", succeeded)
	}
	recorded := fixture.operation(t)
	if recorded.Stage != model.UpgradeStageComplete {
		t.Fatalf("stage = %q, want complete", recorded.Stage)
	}
	if recorded.UpdatedAtMS < before.UpdatedAtMS {
		t.Fatalf("the verdict moved the timestamp backwards: %d -> %d", before.UpdatedAtMS, recorded.UpdatedAtMS)
	}
	for _, domain := range before.DomainsDone {
		if !recorded.HasDomain(domain) {
			t.Fatalf("domain %q was lost: %v", domain, recorded.DomainsDone)
		}
	}
}

// assertVerifyRefusalWroteNothing proves a refusal is a refusal: the row, its
// evidence, its revision and its domains are exactly what they were.
func assertVerifyRefusalWroteNothing(t *testing.T, fixture *verifyFixture, before model.UpgradeOperation) {
	t.Helper()
	after := fixture.operation(t)
	if after.Stage != before.Stage || after.Revision != before.Revision || after.UpdatedAtMS != before.UpdatedAtMS {
		t.Fatalf("a refused verification wrote the row: %q/%d/%d -> %q/%d/%d",
			before.Stage, before.Revision, before.UpdatedAtMS, after.Stage, after.Revision, after.UpdatedAtMS)
	}
	if after.ErrorCode != before.ErrorCode || after.ErrorDetail != before.ErrorDetail {
		t.Fatalf("a refused verification recorded a reason: %q -> %q", before.ErrorCode, after.ErrorCode)
	}
	if !slices.Equal(after.DomainsDone, before.DomainsDone) || !slices.Equal(after.ManualItems, before.ManualItems) {
		t.Fatalf("a refused verification changed the record: %+v -> %+v", before, after)
	}
	if string(after.Verification) != string(before.Verification) {
		t.Fatalf("a refused verification recorded evidence: %s -> %s", before.Verification, after.Verification)
	}
}

// reconcileEvidenceBody builds the evidence the recorded reconciliation of this
// fixture already holds, optionally with one field replaced by the caller. It is
// the durable half of a real comparison, so a test that tampers with it tampers
// with evidence the step actually wrote.
func reconcileEvidenceBody(t *testing.T, fixture *verifyFixture, mutate func(*ReconcileEvidence)) ReconcileEvidence {
	t.Helper()
	recorded := fixture.operation(t)
	var body ReconcileEvidence
	if err := json.Unmarshal(recorded.Reconciliation, &body); err != nil {
		t.Fatalf("decode the recorded reconciliation: %v", err)
	}
	if mutate != nil {
		mutate(&body)
	}
	return body
}

// comparedEvidenceBody is a comparison the recorded row never performed: it
// describes the four domains a real run compares, on the fingerprint of the
// mounted volume, while the operation records none of the domains it would
// establish. It is the shape of a store whose evidence and marks disagree.
func comparedEvidenceBody(t *testing.T, fixture *verifyFixture) ReconcileEvidence {
	t.Helper()
	recorded := fixture.operation(t)
	if strings.TrimSpace(recorded.SourceFingerprintAfter) == "" {
		t.Fatal("the fixture recorded no source fingerprint")
	}
	domains := []ReconcileDomainReport{}
	for _, domain := range []string{ReconcileDomainRetained, ReconcileDomainHistory, ReconcileDomainPricing, ReconcileDomainCoverage} {
		domains = append(domains, ReconcileDomainReport{
			Domain:       domain,
			Table:        domain,
			Applicable:   true,
			SourceDays:   1,
			ComparedDays: 1,
			Mismatches:   []ReconcileMismatch{},
		})
	}
	return ReconcileEvidence{
		SourceFingerprint:  recorded.SourceFingerprintAfter,
		Domains:            domains,
		ParityGapDays:      []string{},
		MismatchDays:       []string{},
		AcknowledgedDays:   []string{},
		UnacknowledgedDays: []string{},
		IncompleteDays:     []ReconcileMarkedDay{},
	}
}

// recordReconciliationBody replaces the stored evidence without touching the
// stage or the domains, which is exactly the shape of a store whose comparison
// record and marks disagree.
func recordReconciliationBody(t *testing.T, fixture *verifyFixture, body ReconcileEvidence) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode the reconciliation: %v", err)
	}
	recordRawReconciliationBody(t, fixture, payload)
}

// recordRawReconciliationBody stores a body verbatim, so a test can present the
// shape a foreign or truncated record would have.
func recordRawReconciliationBody(t *testing.T, fixture *verifyFixture, payload json.RawMessage) {
	t.Helper()
	db, err := managersqlite.Open(fixture.databasePath())
	if err != nil {
		t.Fatalf("open the v2 database: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := upgradeoperation.New(db).RecordReconciliation(context.Background(), payload); err != nil {
		t.Fatalf("record the reconciliation evidence: %v", err)
	}
}

// TestVerifyRefusesAStoreWithoutAnOperation keeps the gate a step that continues
// an operation: a v2 data root with no recorded copy-in has nothing to verify.
func TestVerifyRefusesAStoreWithoutAnOperation(t *testing.T) {
	target := canonicalTempDir(t)
	volume := newLegacyVolume(t)
	_, err := Verify(context.Background(), VerifyOptions{LegacyRoot: volume.root, TargetRoot: target})
	if !errors.Is(err, ErrVerifyNotApplicable) {
		t.Fatalf("verify on an empty v2 data root error = %v, want ErrVerifyNotApplicable", err)
	}
	for _, name := range []string{databaseFileName, dataKeyFileName} {
		if _, statErr := os.Stat(filepath.Join(target, name)); statErr == nil {
			t.Fatalf("a refused verification created %s in the target", name)
		}
	}
}

// TestVerifyRefusesOverlappingRoots keeps the 1.x volume out of the tree the
// verification writes.
func TestVerifyRefusesOverlappingRoots(t *testing.T) {
	fixture := newExternalVerifyFixture(t)
	_, err := Verify(context.Background(), VerifyOptions{LegacyRoot: fixture.target, TargetRoot: fixture.target})
	if err == nil {
		t.Fatal("verify accepted the v2 data root as its own source")
	}
}

// TestVerifyRecordErrorIsTyped keeps the gate's refusals in the shared
// vocabulary: the CLI exit table and the wizard copy read the same code.
func TestVerifyRecordErrorIsTyped(t *testing.T) {
	cases := []struct {
		err  error
		want UpgradeErrorCode
	}{
		{upgradeoperation.ErrStageRegression, UpgradeErrorVerifyNotApplicable},
		{upgradeoperation.ErrConflict, UpgradeErrorVerifyConflict},
	}
	for _, testCase := range cases {
		mapped := mapVerifyRecordError(testCase.err)
		if code, ok := UpgradeErrorCodeOf(mapped); !ok || code != testCase.want {
			t.Fatalf("mapVerifyRecordError(%v) = %v, want code %q", testCase.err, mapped, testCase.want)
		}
	}
}

// TestVerifyTargetModeDerivesFromTheDurableRecord pins the mode decision: the
// binding is the record of an External target, and a connect stage without one is
// refused rather than read as Managed.
func TestVerifyTargetModeDerivesFromTheDurableRecord(t *testing.T) {
	if mode, err := verifyTargetMode(model.UpgradeOperation{Stage: model.UpgradeStageCPAImport}, false); err != nil ||
		mode != model.UpgradeTargetManaged {
		t.Fatalf("managed row = %q/%v, want managed", mode, err)
	}
	if mode, err := verifyTargetMode(model.UpgradeOperation{Stage: model.UpgradeStageExternalConnect}, true); err != nil ||
		mode != model.UpgradeTargetExternal {
		t.Fatalf("external row = %q/%v, want external", mode, err)
	}
	if _, err := verifyTargetMode(model.UpgradeOperation{Stage: model.UpgradeStageExternalConnect}, false); !errors.Is(err, ErrVerifyBindingMissing) {
		t.Fatalf("connect stage without a binding error = %v, want ErrVerifyBindingMissing", err)
	}
}

// TestVerifyRequiredDomainsAreDerived pins the applicable set: never the whole
// model vocabulary, never a caller's list.
func TestVerifyRequiredDomainsAreDerived(t *testing.T) {
	managed := verifyRequiredDomains(model.UpgradeTargetManaged, []string{model.UpgradeDomainPricing})
	if !slices.Equal(managed, []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainPricing, model.UpgradeDomainCPAConfig}) {
		t.Fatalf("managed required domains = %v", managed)
	}
	if slicesContain(managed, model.UpgradeDomainUsageEvents) || slicesContain(managed, model.UpgradeDomainIdentity) {
		t.Fatalf("managed required domains = %v, want no domain no Managed step records", managed)
	}
	external := verifyRequiredDomains(model.UpgradeTargetExternal, nil)
	want := []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainIdentity, model.UpgradeDomainCPAConfig}
	if !slices.Equal(external, want) {
		t.Fatalf("external required domains = %v, want %v", external, want)
	}
}

// TestVerifyExternalIdentityMatchesTheConnectStep pins the comparison's spelling
// rule: the verification canonicalizes the Manager's address with the same two
// functions the connect step used.
func TestVerifyExternalIdentityMatchesTheConnectStep(t *testing.T) {
	fixture := newExternalVerifyFixture(t)
	db, err := managersqlite.Open(fixture.databasePath())
	if err != nil {
		t.Fatalf("open the v2 database: %v", err)
	}
	defer func() { _ = db.Close() }()
	binding, found, err := upgradeoperation.New(db).ExternalBinding(context.Background())
	if err != nil || !found {
		t.Fatalf("read the binding: found=%t err=%v", found, err)
	}
	canonical, err := canonicalVerifyAddress(binding.BaseURL)
	if err != nil {
		t.Fatalf("canonicalize the bound address: %v", err)
	}
	if canonical != binding.BaseURL {
		t.Fatalf("canonical address = %q, binding = %q", canonical, binding.BaseURL)
	}
	// The Manager's own stored connection is normalized the same way, so the
	// comparison is between two spellings of one endpoint.
	resolution, err := managerconfig.ResolveLegacyConnectionAuthority(
		model.ManagerConfig{CPAConnection: model.ManagerCPAConnectionConfig{
			CPABaseURL: binding.BaseURL, ManagementKey: externalTestKey,
		}}, true, model.Setup{}, false)
	if err != nil {
		t.Fatalf("resolve the Manager connection: %v", err)
	}
	if resolution.Connection.BaseURL != binding.BaseURL {
		t.Fatalf("Manager connection = %q, binding = %q", resolution.Connection.BaseURL, binding.BaseURL)
	}
}

// snapshotCompletedTarget renders the operation row and the External binding
// exactly as stored, so a refused re-verification of a completed row can prove
// that it wrote neither.
func snapshotCompletedTarget(t *testing.T, databasePath string) (row, binding string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open the v2 database: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.QueryRow(`select quote(stage) || '|' || quote(domains_done) || '|' || quote(reconciliation) ||
		'|' || quote(verification) || '|' || quote(manual_items) || '|' || quote(error_code) ||
		'|' || quote(error_detail) || '|' || quote(revision) || '|' || quote(updated_at_ms)
		from upgrade_operations where id = 1`).Scan(&row); err != nil {
		t.Fatalf("read the stored operation: %v", err)
	}
	err = db.QueryRow(`select quote(base_url) || '|' || quote(identity) || '|' || quote(generation) ||
		'|' || quote(server_fingerprint) || '|' || quote(cpa_version) || '|' || quote(support) ||
		'|' || quote(client_keys) || '|' || quote(observed_at_ms)
		from upgrade_external_connect where id = 1`).Scan(&binding)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read the stored binding: %v", err)
	}
	return row, binding
}

// storeVerification overwrites the stored verification block, which is the shape
// of a row written by a build whose gate proved something else.
func storeVerification(t *testing.T, databasePath string, evidence model.UpgradeVerification) {
	t.Helper()
	payload, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("encode the verification: %v", err)
	}
	execTarget(t, databasePath, `update upgrade_operations set verification = '`+string(payload)+`' where id = 1`)
}

// TestVerifyRefusesACompletedRowWithoutTheGateEvidence is the R1 regression on
// the read side: the completed path must cross-check the row against the
// evidence it carries. A verdict naming another target, another source volume,
// another binding or a domain the row never marked is refused, and the row is
// left exactly as the corrupted store holds it.
func TestVerifyRefusesACompletedRowWithoutTheGateEvidence(t *testing.T) {
	for _, test := range []struct {
		name string
		// mutate corrupts the stored record; true means it replaced the
		// verification block instead of writing raw SQL.
		mutate func(t *testing.T, fixture *verifyFixture, evidence *model.UpgradeVerification) bool
	}{
		{name: "no verification evidence", mutate: func(t *testing.T, fixture *verifyFixture, _ *model.UpgradeVerification) bool {
			execTarget(t, fixture.databasePath(), `update upgrade_operations set verification = '{}' where id = 1`)
			return false
		}},
		{name: "a managed verdict", mutate: func(_ *testing.T, _ *verifyFixture, evidence *model.UpgradeVerification) bool {
			evidence.TargetMode = model.UpgradeTargetManaged
			evidence.BindingIdentity = ""
			evidence.BindingGeneration = ""
			evidence.BindingObservedAtMS = 0
			evidence.ObservedAtMS = 1_700_000_000_002
			return true
		}},
		{name: "a foreign source fingerprint", mutate: func(_ *testing.T, _ *verifyFixture, evidence *model.UpgradeVerification) bool {
			evidence.SourceFingerprint = "sha256:another-volume"
			return true
		}},
		{name: "a completed domain the row never recorded", mutate: func(t *testing.T, fixture *verifyFixture, _ *model.UpgradeVerification) bool {
			execTarget(t, fixture.databasePath(), `update upgrade_operations set domains_done = '["source_copy"]' where id = 1`)
			return false
		}},
		{name: "a binding the row never recorded", mutate: func(t *testing.T, fixture *verifyFixture, _ *model.UpgradeVerification) bool {
			execTarget(t, fixture.databasePath(), `delete from upgrade_external_connect`)
			return false
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExternalVerifyFixture(t)
			if _, err := fixture.verify(t); err != nil {
				t.Fatalf("complete the fixture operation: %v", err)
			}
			recorded := fixture.operation(t)
			evidence, err := recorded.VerificationEvidence()
			if err != nil {
				t.Fatalf("read the recorded verification: %v", err)
			}
			if test.mutate(t, fixture, &evidence) {
				storeVerification(t, fixture.databasePath(), evidence)
			}
			before, beforeBinding := snapshotCompletedTarget(t, fixture.databasePath())
			if _, err := fixture.verify(t); !errors.Is(err, ErrVerifyNotApplicable) {
				t.Fatalf("verify on a completed row with %s error = %v, want ErrVerifyNotApplicable", test.name, err)
			}
			after, afterBinding := snapshotCompletedTarget(t, fixture.databasePath())
			if after != before || afterBinding != beforeBinding {
				t.Fatalf("a refused re-verification wrote the store: %q/%q -> %q/%q", before, beforeBinding, after, afterBinding)
			}
		})
	}
}

// verifyRotatingManagementKey is the key the synthetic External CPA accepts until
// a test rotates it. It has the shape the production validator accepts.
const verifyRotatingManagementKey = "rotating-management-key"

// externalVerifyCPAServer is a synthetic External CPA serving the v8 management
// reads the connect step and the completion gate make: the version header, the
// configuration document, the client-key list and the gateway model list. It
// authenticates with verifyRotatingManagementKey and can start refusing that key,
// which is what a CPA whose owner rotated its credential does.
type externalVerifyCPAServer struct {
	*httptest.Server
	version  string
	rejected atomic.Bool
}

const externalVerifyConfigDocument = `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/auth"},` +
	`"access":{"api-keys":["client-key-1"]}}`

func newExternalVerifyCPAServer(t *testing.T, version string) *externalVerifyCPAServer {
	t.Helper()
	server := &externalVerifyCPAServer{version: version}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-test"}]}`))
			return
		}
		if server.rejected.Load() || r.Header.Get("Authorization") != "Bearer "+verifyRotatingManagementKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v0/management/config":
			if server.version != "" {
				w.Header().Set("X-CPA-Version", server.version)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case "/v8/management/config":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(externalVerifyConfigDocument))
		case "/v8/management/config/access/api-keys":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`["client-key-1"]`))
		case "/v8/management/config.yaml":
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write([]byte("port: 8317\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestVerifyRefusesAManagerKeyTheConnectStepDidNotProve is the R2 regression for
// the stored credential: the External connect step proved K_new, while the
// Manager's own connection still carries K_old for the same address. Comparing
// only the address recorded a complete verdict, and the restarted Manager then
// met 401 against the CPA.
func TestVerifyRefusesAManagerKeyTheConnectStepDidNotProve(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.reconcile(t)
	// The Manager keeps an older credential for the same CPA.
	fixture.handManagerConnection(t, verifyExternalAddress, "stale-management-key")
	// The connect step proves the current one.
	fixture.connectExternal(t)
	before := fixture.operation(t)
	_, err := fixture.verify(t)
	if !errors.Is(err, ErrVerifyConnectionUnproven) {
		t.Fatalf("verify with a stale Manager credential error = %v, want ErrVerifyConnectionUnproven", err)
	}
	if stage := fixture.operation(t).Stage; stage != model.UpgradeStageExternalConnect {
		t.Fatalf("stage after the refusal = %q, want external_connect", stage)
	}
	assertVerifyRefusalWroteNothing(t, fixture, before)
}

// TestVerifyRefusesAnExternalKeyTheCPANoLongerAccepts proves the capability half
// of the same gate against a real CPA server: the connect step proved the key,
// the Manager stores it, and the CPA then rotates it. The stored-key equality is
// satisfied and the live authenticated read is not, so the gate must refuse
// instead of recording a completion the restarted Manager cannot use.
func TestVerifyRefusesAnExternalKeyTheCPANoLongerAccepts(t *testing.T) {
	server := newExternalVerifyCPAServer(t, "v8.0.13")
	fixture := newVerifyFixture(t)
	fixture.reconcile(t)
	// The connect step runs against the real CPA, so the binding records the
	// server fingerprint and the target address it answered from.
	if _, err := ExternalConnect(context.Background(), ExternalConnectOptions{
		TargetRoot:    fixture.target,
		BaseURL:       server.URL,
		ManagementKey: verifyRotatingManagementKey,
	}); err != nil {
		t.Fatalf("external connect: %v", err)
	}
	fixture.handManagerConnection(t, server.URL, verifyRotatingManagementKey)
	// The CPA rotates its management key after the connect step committed.
	server.rejected.Store(true)
	before := fixture.operation(t)
	// No seam is supplied, so the gate's own probes run against the CPA server:
	// this is the production read that has to fail.
	_, err := Verify(context.Background(), VerifyOptions{LegacyRoot: fixture.volume.root, TargetRoot: fixture.target})
	if !errors.Is(err, ErrVerifyConnectionUnproven) {
		t.Fatalf("verify after the CPA rotated its key error = %v, want ErrVerifyConnectionUnproven", err)
	}
	if stage := fixture.operation(t).Stage; stage != model.UpgradeStageExternalConnect {
		t.Fatalf("stage after the refusal = %q, want external_connect", stage)
	}
	assertVerifyRefusalWroteNothing(t, fixture, before)
}

// TestVerifyRefusesACompletedRowWithoutTheProvedCredential is the F1 regression
// on the read side: the completed path re-checked the verification against the
// row, but never against the credential the Manager itself holds.
//
// A store that reached `complete` before the connect step sealed its secret
// carries a full verdict and an empty management_key, and a repeat of the gate
// reported it as a finished install. Every shape below is one such a real store
// can hold, and each is refused without writing.
func TestVerifyRefusesACompletedRowWithoutTheProvedCredential(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, fixture *verifyFixture)
	}{
		{name: "no sealed management secret", mutate: func(t *testing.T, fixture *verifyFixture) {
			execTarget(t, fixture.databasePath(), `update upgrade_external_connect set management_key = '' where id = 1`)
		}},
		{name: "a plaintext management secret", mutate: func(t *testing.T, fixture *verifyFixture) {
			execTarget(t, fixture.databasePath(), `update upgrade_external_connect set management_key = '`+externalTestKey+`' where id = 1`)
		}},
		{name: "a secret sealed with another data key", mutate: func(t *testing.T, fixture *verifyFixture) {
			foreign, err := security.NewProtector([]byte("a-data-key-this-manager-does-not-hold"))
			if err != nil {
				t.Fatalf("initialize the foreign protector: %v", err)
			}
			sealed, err := foreign.ProtectString(externalTestKey)
			if err != nil {
				t.Fatalf("seal the secret with another data key: %v", err)
			}
			execTarget(t, fixture.databasePath(), `update upgrade_external_connect set management_key = '`+sealed+`' where id = 1`)
		}},
		{name: "a Manager connection holding another key", mutate: func(t *testing.T, fixture *verifyFixture) {
			fixture.handManagerConnection(t, verifyExternalAddress, "stale-management-key")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExternalVerifyFixture(t)
			if _, err := fixture.verify(t); err != nil {
				t.Fatalf("complete the fixture operation: %v", err)
			}
			test.mutate(t, fixture)
			before, beforeBinding := snapshotCompletedTarget(t, fixture.databasePath())
			if _, err := fixture.verify(t); !errors.Is(err, ErrVerifyNotApplicable) {
				t.Fatalf("verify on a completed row with %s error = %v, want ErrVerifyNotApplicable", test.name, err)
			}
			after, afterBinding := snapshotCompletedTarget(t, fixture.databasePath())
			if after != before || afterBinding != beforeBinding {
				t.Fatalf("a refused re-verification wrote the store: %q/%q -> %q/%q", before, beforeBinding, after, afterBinding)
			}
		})
	}
}

// externalConnectOptionsFor drives the connect step with the same synthetic probe
// seams the fixture's verification uses, so an offline test re-proves the
// recorded target without dialing its address.
func externalConnectOptionsFor(fixture *verifyFixture, managementKey string, reprove bool) ExternalConnectOptions {
	verifyOptions := fixture.verifyOptions()
	return ExternalConnectOptions{
		TargetRoot:             fixture.target,
		BaseURL:                verifyExternalAddress,
		ManagementKey:          managementKey,
		Reprove:                reprove,
		observeExternalVersion: verifyOptions.observeExternalVersion,
		readExternalKeys:       verifyOptions.readExternalKeys,
		proveModelSurface:      verifyOptions.proveModelSurface,
		readV8Config:           verifyOptions.readV8Config,
	}
}

// TestVerifyCompletesAfterAnExternalConnectBackfill is the F2 regression on the
// gate's side: the refusal an unsealed binding earns tells the operator to run
// the External connect step again, so that instruction has to be executable.
func TestVerifyCompletesAfterAnExternalConnectBackfill(t *testing.T) {
	fixture := newExternalVerifyFixture(t)
	// The shape 1040edcc and 081fcc53 left: the stage and the binding, no secret.
	execTarget(t, fixture.databasePath(), `update upgrade_external_connect set management_key = '' where id = 1`)
	if _, err := fixture.verify(t); !errors.Is(err, ErrVerifyConnectionUnproven) {
		t.Fatalf("verify on an unsealed binding error = %v, want ErrVerifyConnectionUnproven", err)
	}
	// No --reprove: the operator is recovering a binding that never carried a
	// secret, which is exactly the case the default path now repairs.
	result, err := ExternalConnect(context.Background(), externalConnectOptionsFor(fixture, externalTestKey, false))
	if err != nil {
		t.Fatalf("backfill the unsealed binding: %v", err)
	}
	if result.AlreadyComplete {
		t.Fatalf("the backfill answered from the record it had to repair: %+v", result)
	}
	verified, err := fixture.verify(t)
	if err != nil {
		t.Fatalf("verify after the backfill: %v", err)
	}
	if !verified.Complete || verified.Stage != model.UpgradeStageComplete {
		t.Fatalf("result = %+v, want a completed operation", verified)
	}
}

// TestVerifyCompletesAfterAReproofedRotatedKey is the F2 rotation path end to
// end: the connect step proved K1, the CPA rotated to K2, the Manager stores K2.
// The gate refuses, the default repeat refuses, and --reprove seals K2 so the
// next verification completes.
func TestVerifyCompletesAfterAReproofedRotatedKey(t *testing.T) {
	const rotated = "rotated-management-key"
	fixture := newExternalVerifyFixture(t)
	rotatedConfigSHA := strings.Repeat("b", 64)
	rotatedKeys := []string{"client-key-1", "client-key-2"}
	verifyWithRotatedConfig := func() (VerifyResult, error) {
		opts := fixture.verifyOptions()
		opts.readExternalKeys = func(context.Context, string, string) (cpa.ExternalKeySnapshot, error) {
			return cpa.ExternalKeySnapshot{Keys: rotatedKeys, SourceSHA256: rotatedConfigSHA}, nil
		}
		opts.readV8Config = func(context.Context, string, string) (cpa.V8ConfigObservation, error) {
			return cpa.V8ConfigObservation{BaseURL: verifyExternalAddress, ClientKeyCount: len(rotatedKeys)}, nil
		}
		return Verify(context.Background(), opts)
	}
	// Before key rotation, if config.yaml changed on the External CPA without
	// --reprove, Verify refuses with binding drift and names --reprove.
	if _, driftErr := verifyWithRotatedConfig(); !errors.Is(driftErr, ErrVerifyBindingDrifted) || !strings.Contains(driftErr.Error(), "--reprove") {
		t.Fatalf("verify on unacknowledged config.yaml drift = %v, want ErrVerifyBindingDrifted naming --reprove", driftErr)
	}
	// The Manager follows the CPA's rotation; the binding still seals K1.
	fixture.handManagerConnection(t, verifyExternalAddress, rotated)
	_, verifyErr := verifyWithRotatedConfig()
	if !errors.Is(verifyErr, ErrVerifyConnectionUnproven) {
		t.Fatalf("verify after the rotation error = %v, want ErrVerifyConnectionUnproven", verifyErr)
	}
	if !strings.Contains(verifyErr.Error(), "--reprove") {
		t.Fatalf("the refusal does not name the recovery: %v", verifyErr)
	}
	connectOpts := func(reprove bool) ExternalConnectOptions {
		opts := externalConnectOptionsFor(fixture, rotated, reprove)
		opts.readExternalKeys = func(context.Context, string, string) (cpa.ExternalKeySnapshot, error) {
			return cpa.ExternalKeySnapshot{Keys: rotatedKeys, SourceSHA256: rotatedConfigSHA}, nil
		}
		opts.readV8Config = func(context.Context, string, string) (cpa.V8ConfigObservation, error) {
			return cpa.V8ConfigObservation{BaseURL: verifyExternalAddress, ClientKeyCount: len(rotatedKeys)}, nil
		}
		return opts
	}
	// The default repeat refuses a credential the operation did not connect with.
	if _, err := ExternalConnect(context.Background(), connectOpts(false)); !errors.Is(err, ErrExternalManagementRejected) {
		t.Fatalf("the default repeat of a rotated key refused with %v", err)
	}
	// The explicit re-proof seals K2 and refreshes the config.yaml digest on the
	// same CPA build so the next verification completes.
	reproved, err := ExternalConnect(context.Background(), connectOpts(true))
	if err != nil {
		t.Fatalf("reprove the rotated key and updated config.yaml: %v", err)
	}
	if reproved.AlreadyComplete {
		t.Fatalf("the reproof answered from the recorded connection: %+v", reproved)
	}
	verified, err := verifyWithRotatedConfig()
	if err != nil {
		t.Fatalf("verify after the reproof: %v", err)
	}
	if !verified.Complete || verified.Stage != model.UpgradeStageComplete {
		t.Fatalf("result = %+v, want a completed operation", verified)
	}
}
