package freshkeyapply

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

// rebindRecordFor is a committed Connect External record holding one protected
// credential. It is the durable state a rebind replaces a credential in.
func rebindRecordFor(t *testing.T, protector *security.Protector, management string) (freshstore.Record, ExternalInstall) {
	t.Helper()
	const address = "https://cpa.example"
	protected, err := protector.ProtectString(management)
	if err != nil {
		t.Fatal(err)
	}
	record := freshstore.Record{
		Operation: bootstrap.FreshOperation{
			Strategy: bootstrap.FreshStrategyCheckpoint{
				Strategy: bootstrap.ConnectExternal, Stage: "committed",
				TargetIdentity: ExternalIdentity(address), TargetGeneration: 1,
			},
			ProductComplete: true,
			Revision:        1,
		},
		Connection: &model.ManagerCPAConnectionConfig{CPABaseURL: address, ManagementKey: protected},
	}
	return record, ExternalInstall{Identity: ExternalIdentity(address), Checkpoint: 1, BaseURL: address, ManagementKey: management}
}

// rebindHarness is the durable record plus the External authority over it.
type rebindHarness struct {
	store     *rebindStore
	protector *security.Protector
	service   *Service
}

func newRebindHarness(t *testing.T, management string) *rebindHarness {
	t.Helper()
	protector, err := security.NewProtector([]byte("isolated-rebind-test-key"))
	if err != nil {
		t.Fatal(err)
	}
	record, install := rebindRecordFor(t, protector, management)
	store := &rebindStore{record: record}
	service := NewExternal(nil, nil, func(context.Context) (ExternalInstall, error) { return install, nil })
	service.externalRebindVerifier = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
		return cpa.ClassifyExternalVersion("v8.0.13"), nil
	}
	return &rebindHarness{store: store, protector: protector, service: service}
}

// TestExternalRebindPersistsOnlyAProvenCredential proves a rebind replaces the
// stored envelope and moves the record revision, while leaving the address and
// the installation identity alone so no key-usage history is reattributed.
func TestExternalRebindPersistsOnlyAProvenCredential(t *testing.T) {
	harness := newRebindHarness(t, strings.Repeat("a", 64))
	rotated := strings.Repeat("b", 64)
	result, err := harness.service.Rebind(t.Context(), harness.store, harness.protector, 1, "", rotated)
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if result.Record.Operation.Revision != 2 {
		t.Fatalf("revision = %d, want 2", result.Record.Operation.Revision)
	}
	if result.Record.Operation.Strategy.TargetGeneration != 1 {
		t.Fatalf("the installation generation moved to %d", result.Record.Operation.Strategy.TargetGeneration)
	}
	if result.Record.Operation.Strategy.TargetIdentity != ExternalIdentity("https://cpa.example") {
		t.Fatalf("the identity changed to %q", result.Record.Operation.Strategy.TargetIdentity)
	}
	if result.Record.Connection.CPABaseURL != "https://cpa.example" {
		t.Fatalf("the address changed to %q", result.Record.Connection.CPABaseURL)
	}
	if result.Record.Connection.ManagementKey == rotated {
		t.Fatal("the credential was persisted in plaintext")
	}
	plain, err := harness.protector.UnprotectString(result.Record.Connection.ManagementKey)
	if err != nil || plain != rotated {
		t.Fatalf("the stored envelope does not decrypt to the candidate: %v", err)
	}
	if harness.store.rebounds != 1 {
		t.Fatalf("rebind transactions = %d", harness.store.rebounds)
	}
}

// TestExternalRebindRefusesAnUnprovenCredential proves nothing is written when the
// candidate cannot be proven: a refused credential, an unsupported version, an
// unusable shape and another address each keep the installation exactly as it was.
func TestExternalRebindRefusesAnUnprovenCredential(t *testing.T) {
	rotated := strings.Repeat("b", 64)
	for _, scenario := range []struct {
		name  string
		key   string
		url   string
		probe func() (cpa.ExternalVersionReport, error)
		want  error
	}{
		{
			name: "rejected credential", key: rotated,
			probe: func() (cpa.ExternalVersionReport, error) {
				return cpa.ExternalVersionReport{}, cpa.ErrManagementRejected
			},
			want: cpa.ErrManagementRejected,
		},
		{
			name: "unreachable CPA", key: rotated,
			probe: func() (cpa.ExternalVersionReport, error) { return cpa.ExternalVersionReport{}, cpa.ErrProofUnavailable },
			want:  cpa.ErrProofUnavailable,
		},
		{
			name: "unsupported version", key: rotated,
			probe: func() (cpa.ExternalVersionReport, error) {
				return cpa.ClassifyExternalVersion("v7.2.130"), nil
			},
			want: cpa.ErrExternalVersionUnsupported,
		},
		{
			name: "unusable shape", key: " padded ",
			want: ErrRebindRefused,
		},
		{
			name: "another address", key: rotated, url: "https://other.example",
			want: ErrRebindRefused,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			harness := newRebindHarness(t, strings.Repeat("a", 64))
			before := *harness.store.record.Connection
			verifications := 0
			harness.service.externalRebindVerifier = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
				verifications++
				if scenario.probe == nil {
					return cpa.ClassifyExternalVersion("v8.0.13"), nil
				}
				return scenario.probe()
			}
			result, err := harness.service.Rebind(t.Context(), harness.store, harness.protector, 1, scenario.url, scenario.key)
			if !errors.Is(err, scenario.want) {
				t.Fatalf("rebind = %v, want %v", err, scenario.want)
			}
			if result.Record.Operation.Revision != 0 {
				t.Fatal("a refused rebind answered a record")
			}
			if harness.store.rebounds != 0 {
				t.Fatal("a refused rebind wrote the durable record")
			}
			if *harness.store.record.Connection != before {
				t.Fatal("a refused rebind changed the stored connection")
			}
			// A shape or address refusal never reaches the CPA at all.
			if scenario.probe == nil && verifications != 0 {
				t.Fatalf("a pre-read refusal still probed the CPA %d times", verifications)
			}
		})
	}
}

// TestExternalRebindReportsAStaleRecord proves a record that moved while the
// candidate was being proven is a conflict, not a rebind: the page re-reads and
// asks again rather than this Manager guessing which write won.
func TestExternalRebindReportsAStaleRecord(t *testing.T) {
	harness := newRebindHarness(t, strings.Repeat("a", 64))
	harness.store.conflict = true
	_, err := harness.service.Rebind(t.Context(), harness.store, harness.protector, 1, "", strings.Repeat("b", 64))
	if !errors.Is(err, ErrGeneration) {
		t.Fatalf("stale record = %v, want a generation mismatch", err)
	}
	if harness.store.rebounds != 0 {
		t.Fatal("a stale rebind wrote the durable record")
	}
}

// TestExternalRebindDetectsARevisionChangeDuringVerification proves the revision
// the write is conditional on was read before the candidate was proven. A record
// edited during the verification — the window a real CPA round trip opens — must
// conflict: reading the revision afterwards would compare the CAS against the
// state this rebind itself last saw and overwrite the concurrent edit.
func TestExternalRebindDetectsARevisionChangeDuringVerification(t *testing.T) {
	harness := newRebindHarness(t, strings.Repeat("a", 64))
	harness.service.externalRebindVerifier = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
		// Another save lands while the candidate is being verified.
		harness.store.record.Operation.Revision++
		return cpa.ClassifyExternalVersion("v8.0.13"), nil
	}
	_, err := harness.service.Rebind(t.Context(), harness.store, harness.protector, 1, "", strings.Repeat("b", 64))
	if !errors.Is(err, ErrGeneration) {
		t.Fatalf("revision change during verification = %v, want a generation mismatch", err)
	}
	if harness.store.rebounds != 0 {
		t.Fatal("a rebind decided against a stale revision wrote the record")
	}
}

// TestExternalRebindNeedsTheCommittedGeneration proves the authority's own
// generation check runs first: a rebind against another generation never probes
// the CPA.
func TestExternalRebindNeedsTheCommittedGeneration(t *testing.T) {
	harness := newRebindHarness(t, strings.Repeat("a", 64))
	probed := false
	harness.service.externalRebindVerifier = func(context.Context, string, string) (cpa.ExternalVersionReport, error) {
		probed = true
		return cpa.ClassifyExternalVersion("v8.0.13"), nil
	}
	if _, err := harness.service.Rebind(t.Context(), harness.store, harness.protector, 2, "", strings.Repeat("b", 64)); !errors.Is(err, ErrGeneration) {
		t.Fatalf("another generation = %v", err)
	}
	if probed {
		t.Fatal("a foreign generation was probed against the CPA")
	}
}

// TestExternalRefusalRebindAbortsUnattemptedOperationAllowsRecoveryAndFencesAttemptedWrites
// verifies the end-to-end refusal -> rebind -> recovery contract:
//  1. A management-refused, entirely unattempted operation is truthfully aborted on
//     real credential rotation without marking target identities superseded or allowing
//     the old destructive intent to execute against the rebound CPA, unblocking recovery.
//  2. Attempted/unknown or partially-applied operations remain fenced against rotation,
//     while same-credential re-verify stays available to recover from temporary bans.
func TestExternalRefusalRebindAbortsUnattemptedOperationAllowsRecoveryAndFencesAttemptedWrites(t *testing.T) {
	ctx := t.Context()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "rebind-recovery.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlite.Migrate(db); err != nil {
		t.Fatal(err)
	}
	keyRepo := repo.New(db)
	protector, err := security.NewProtector([]byte("isolated-rebind-recovery-key"))
	if err != nil {
		t.Fatal(err)
	}
	keys := freshkeys.New(keyRepo, protector)

	initialManagement := strings.Repeat("a", 64)
	rotatedManagement := strings.Repeat("b", 64)
	thirdManagement := strings.Repeat("c", 64)

	fakeCPA := newFakeExternalCPA(t)
	fakeCPA.management = initialManagement
	protectedInitial, err := protector.ProtectString(initialManagement)
	if err != nil {
		t.Fatal(err)
	}
	store := &rebindStore{
		record: freshstore.Record{
			Operation: bootstrap.FreshOperation{
				Strategy: bootstrap.FreshStrategyCheckpoint{
					Strategy:         bootstrap.ConnectExternal,
					Stage:            "committed",
					TargetIdentity:   ExternalIdentity(fakeCPA.server.URL),
					TargetGeneration: 1,
				},
				ProductComplete: true,
				Revision:        1,
			},
			Connection: &model.ManagerCPAConnectionConfig{
				CPABaseURL:    fakeCPA.server.URL,
				ManagementKey: protectedInitial,
			},
		},
	}
	source := func(context.Context) (ExternalInstall, error) {
		rec, loadErr := store.Load(ctx)
		if loadErr != nil || rec.Connection == nil {
			return ExternalInstall{}, ErrExternalUnavailable
		}
		mgmt, openErr := protector.UnprotectString(rec.Connection.ManagementKey)
		if openErr != nil {
			return ExternalInstall{}, ErrExternalUnavailable
		}
		return ExternalInstall{
			Identity:      rec.Operation.Strategy.TargetIdentity,
			Checkpoint:    rec.Operation.Strategy.TargetGeneration,
			BaseURL:       rec.Connection.CPABaseURL,
			ManagementKey: mgmt,
		}, nil
	}
	service := NewExternal(keyRepo, keys, source)
	service.externalProbe = supportedExternalVersion
	service.externalRebindVerifier = func(_ context.Context, _ string, candidate string) (cpa.ExternalVersionReport, error) {
		fakeCPA.mu.Lock()
		currentMgmt := fakeCPA.management
		fakeCPA.mu.Unlock()
		if candidate != currentMgmt {
			return cpa.ExternalVersionReport{}, cpa.ErrManagementRejected
		}
		return cpa.ClassifyExternalVersion("v8.0.13"), nil
	}

	stageKey := func(reqID string) (string, string) {
		created, _, createErr := keys.Create(ctx, reqID)
		if createErr != nil {
			t.Fatal(createErr)
		}
		return string(created.Key.ID), created.CreatedSecret
	}

	targetID, targetSecret := stageKey(strings.Repeat("1", 32))
	keeperID, keeperSecret := stageKey(strings.Repeat("2", 32))
	fakeCPA.mu.Lock()
	fakeCPA.keys = []string{targetSecret, keeperSecret}
	fakeCPA.authKeys = []string{targetSecret, keeperSecret}
	fakeCPA.mu.Unlock()

	// Prepare a destructive revoke of targetID, then rotate CPA's management key before ContinueOperation.
	revokeOpID := strings.Repeat("3", 32)
	prepared, err := service.Prepare(ctx, revokeOpID, targetID, keeperID, "revoke", 1)
	if err != nil {
		t.Fatalf("Prepare(revoke): %v", err)
	}
	fakeCPA.mu.Lock()
	fakeCPA.management = rotatedManagement
	fakeCPA.mu.Unlock()

	if _, err := service.ContinueOperation(ctx, prepared.Operation.ID, 1); !errors.Is(err, cpa.ErrManagementRejected) {
		t.Fatalf("ContinueOperation with rotated management key = %v, want ErrManagementRejected", err)
	}
	refusedOp, err := keyRepo.LatestOperation(ctx)
	if err != nil || refusedOp == nil || refusedOp.Outcome != repo.OutcomePending || !refusedOp.AllStepsUnattempted() {
		t.Fatalf("operation after management refusal = %+v (%v), want pending + all unattempted", refusedOp, err)
	}

	// Real credential rebind aborts the unattempted revoke without lying committed.
	rebound, err := service.Rebind(ctx, store, protector, 1, "", rotatedManagement)
	if err != nil {
		t.Fatalf("Rebind with unattempted operation: %v", err)
	}
	if rebound.Reverified || store.rebounds != 1 {
		t.Fatalf("rebound = %+v rebounds=%d, want rotation commit", rebound, store.rebounds)
	}

	abortedOp, err := keyRepo.Operation(ctx, revokeOpID)
	if err != nil || abortedOp == nil || abortedOp.Outcome != repo.OutcomeAborted || !abortedOp.IsTerminal() || !abortedOp.AllStepsUnattempted() {
		t.Fatalf("abortedOp after Rebind = %+v (%v), want terminal OutcomeAborted", abortedOp, err)
	}
	targetDetail, err := keys.Detail(ctx, targetID)
	if err != nil || targetDetail.Lifecycle != identity.LifecycleActive {
		t.Fatalf("target lifecycle after aborted revoke = %+v (%v), want active (never falsely superseded)", targetDetail, err)
	}

	checked, err := service.CheckOperation(ctx, revokeOpID, 1)
	if err != nil || checked.Operation == nil || checked.Operation.Outcome != repo.OutcomeAborted || checked.RuntimeState != "not_applied" || checked.NextStep != "" {
		t.Fatalf("CheckOperation(aborted) = %+v (%v), want aborted not_applied empty nextStep", checked, err)
	}
	if _, err := service.ContinueOperation(ctx, revokeOpID, 1); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("ContinueOperation(aborted) = %v, want repo.ErrConflict (never retargets old revoke)", err)
	}
	if !slices.Contains(fakeCPA.keys, targetSecret) {
		t.Fatalf("rebound CPA lost targetSecret from aborted pre-rebind revoke: %v", fakeCPA.keys)
	}

	// Recovery with SAME canonical key and SAME original requestId after an aborted
	// ApplyAdd: initial ApplyAdd(createReqID) fails with ErrManagementRejected -> Rebind
	// aborts that unattempted add -> retrying ApplyAdd(createReqID) with the SAME
	// requestId allocates one fresh add intent, coalesces idempotent retries while
	// pending/unknown without resending attempted PUTs, and commits the preserved key.
	fourthManagement := strings.Repeat("d", 64)
	createReqID := strings.Repeat("4", 32)
	recoveredKeyID, recoveredSecret := stageKey(createReqID)
	if _, err := service.Prepare(ctx, createReqID, recoveredKeyID, "", "add", 1); err != nil {
		t.Fatalf("prepare add before credential rotation: %v", err)
	}
	fakeCPA.mu.Lock()
	fakeCPA.management = thirdManagement
	fakeCPA.mu.Unlock()

	if _, err := service.ApplyAdd(ctx, createReqID, recoveredKeyID, 1); !errors.Is(err, cpa.ErrManagementRejected) {
		t.Fatalf("ApplyAdd before second Rebind = %v, want ErrManagementRejected", err)
	}
	if _, err := service.Rebind(ctx, store, protector, 1, "", thirdManagement); err != nil {
		t.Fatalf("second Rebind after refused ApplyAdd: %v", err)
	}
	origAddOp, err := keyRepo.Operation(ctx, createReqID)
	if err != nil || origAddOp == nil || origAddOp.Outcome != repo.OutcomeAborted {
		t.Fatalf("original add op after Rebind = %+v (%v), want OutcomeAborted", origAddOp, err)
	}

	// Hold hot-reload so the first post-rebind retry writes once and stays unknown;
	// a second retry with the SAME createReqID must resolve the newest unknown op
	// without minting another ID or sending a duplicate PUT.
	fakeCPA.mu.Lock()
	fakeCPA.holdHotReload = true
	fakeCPA.authKeys = slices.Clone(fakeCPA.keys)
	writesBeforeAdd := fakeCPA.writes
	fakeCPA.mu.Unlock()

	unknownRes, err := service.ApplyAdd(ctx, createReqID, recoveredKeyID, 1)
	if !errors.Is(err, cpa.ErrApplyUnknown) || unknownRes.Operation == nil || unknownRes.Operation.ID == createReqID {
		t.Fatalf("post-rebind ApplyAdd during hot-reload lag = %+v (%v), want fresh op ID + ErrApplyUnknown", unknownRes, err)
	}
	postRebindOpID := unknownRes.Operation.ID
	if fakeCPA.writes != writesBeforeAdd+1 {
		t.Fatalf("writes after first post-rebind ApplyAdd = %d, want +1", fakeCPA.writes-writesBeforeAdd)
	}

	retryUnknown, err := service.ApplyAdd(ctx, createReqID, recoveredKeyID, 1)
	if !errors.Is(err, cpa.ErrApplyUnknown) || retryUnknown.Operation == nil || retryUnknown.Operation.ID != postRebindOpID {
		t.Fatalf("idempotent retry while unknown = %+v (%v), want same op %s + ErrApplyUnknown", retryUnknown, err, postRebindOpID)
	}
	if fakeCPA.writes != writesBeforeAdd+1 {
		t.Fatalf("writes after idempotent unknown retry = %d, want 0 duplicate PUTs", fakeCPA.writes-(writesBeforeAdd+1))
	}

	fakeCPA.mu.Lock()
	fakeCPA.holdHotReload = false
	fakeCPA.authKeys = slices.Clone(fakeCPA.keys)
	fakeCPA.mu.Unlock()

	recoveredRes, err := service.ApplyAdd(ctx, createReqID, recoveredKeyID, 1)
	if err != nil {
		t.Fatalf("ApplyAdd recovery settle with original requestId: %v", err)
	}
	if recoveredRes.Operation == nil || recoveredRes.Operation.ID != postRebindOpID ||
		recoveredRes.Operation.Outcome != repo.OutcomeCommitted || !slices.Contains(fakeCPA.keys, recoveredSecret) {
		t.Fatalf("recovered ApplyAdd = %+v, keys=%v", recoveredRes, fakeCPA.keys)
	}
	if fakeCPA.writes != writesBeforeAdd+1 {
		t.Fatalf("writes after read-only settle = %d, want still 1 total PUT", fakeCPA.writes-writesBeforeAdd)
	}

	// Replaying ApplyAdd(createReqID) after commit resolves the committed op read-only.
	replayedRes, err := service.ApplyAdd(ctx, createReqID, recoveredKeyID, 1)
	if err != nil || replayedRes.Operation == nil || replayedRes.Operation.ID != postRebindOpID || replayedRes.Operation.Outcome != repo.OutcomeCommitted {
		t.Fatalf("replayed ApplyAdd(createReqID) = %+v (%v), want committed op %s", replayedRes, err, postRebindOpID)
	}
	if fakeCPA.writes != writesBeforeAdd+1 {
		t.Fatalf("writes after committed replay = %d, want 0 additional PUTs", fakeCPA.writes-(writesBeforeAdd+1))
	}

	// Attempted/unknown write remains fenced against credential rotation, while same-key reverify stays open.
	fencedKeyID, _ := stageKey(strings.Repeat("6", 32))
	attemptedOp := repo.Operation{
		ID: strings.Repeat("7", 32), TargetID: fencedKeyID, Mode: "add",
		RunID: ExternalIdentity(fakeCPA.server.URL), Generation: ExternalIdentity(fakeCPA.server.URL),
		Revision: 1, Outcome: repo.OutcomeUnknown,
		Steps:       []repo.KeyStep{{Name: "add", State: "attempted", Returned: true}},
		CreatedAtMS: 5_000,
	}
	if err := keyRepo.BeginOperation(ctx, attemptedOp); err != nil {
		t.Fatalf("BeginOperation(attempted): %v", err)
	}
	if _, err := service.Rebind(ctx, store, protector, 1, "", fourthManagement); !errors.Is(err, ErrRebindRefused) || !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("Rebind during attempted/unknown operation = %v, want ErrRebindRefused + repo.ErrConflict", err)
	}
	stillUnknown, err := keyRepo.LatestOperation(ctx)
	if err != nil || stillUnknown == nil || stillUnknown.Outcome != repo.OutcomeUnknown {
		t.Fatalf("LatestOperation after fenced Rebind = %+v (%v), want unchanged OutcomeUnknown", stillUnknown, err)
	}
	reverified, err := service.Rebind(ctx, store, protector, 1, "", thirdManagement)
	if err != nil || !reverified.Reverified {
		t.Fatalf("same-credential Rebind reverify with unknown op = %+v (%v), want Reverified=true", reverified, err)
	}
}

// rebindStore is the durable record a rebind needs, in memory.
type rebindStore struct {
	record   freshstore.Record
	rebounds int
	conflict bool
}

func (s *rebindStore) Load(context.Context) (freshstore.Record, error) { return s.record, nil }

func (s *rebindStore) Create(context.Context, bootstrap.FreshIdentity, *model.AdminCredential) (freshstore.Record, error) {
	return freshstore.Record{}, errors.New("unsupported")
}

func (s *rebindStore) Update(context.Context, bootstrap.FreshIdentity, uint64, func(*freshstore.Record) error) (freshstore.Record, error) {
	return freshstore.Record{}, errors.New("unsupported")
}

func (s *rebindStore) RebindConnection(_ context.Context, _ bootstrap.FreshIdentity, revision uint64, next model.ManagerCPAConnectionConfig) (freshstore.Record, error) {
	if s.conflict {
		return freshstore.Record{}, freshstore.ErrConflict
	}
	// The real store compares the revision it is given, so a rebind decided
	// against a record that has since moved conflicts here too.
	if revision != s.record.Operation.Revision {
		return freshstore.Record{}, freshstore.ErrConflict
	}
	s.rebounds++
	s.record.Connection = &next
	s.record.Operation.Revision++
	return s.record, nil
}
