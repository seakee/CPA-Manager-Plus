package freshkeyapply

import (
	"errors"
	"slices"
	"strings"
	"testing"

	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

func TestManagedSingleAndMultiKeyRotateAndAlreadyHeldReconciliation(t *testing.T) {
	ctx := t.Context()
	s, dest, _, installedID := managedFixture(t)
	state, err := s.Apply(ctx, installedID, 9)
	if err != nil || state.RuntimeState != "effective_observed" {
		t.Fatalf("initial managed apply: %+v %v", state, err)
	}

	// Single-key rotation MUST NOT trigger ErrLastKey even when len(snap.Keys) == 1.
	rot1ID := strings.Repeat("1", 32)
	staged1, created1, err := s.keys.StageRotate(ctx, rot1ID, installedID)
	if err != nil || !created1 {
		t.Fatalf("StageRotate single key: %+v %v %v", staged1, created1, err)
	}
	res1, err := s.ApplyRotate(ctx, rot1ID, installedID, 9)
	if err != nil || res1.RuntimeState != "effective_observed" || res1.Operation == nil || res1.Operation.Outcome != repo.OutcomeCommitted {
		t.Fatalf("ApplyRotate single key: %+v %v", res1, err)
	}
	if got := dest.snapshot(); !slices.Equal(got, []string{staged1.NewSecret}) {
		t.Fatalf("CPA keys after single-key rotate = %v, want [%s]", got, staged1.NewSecret)
	}
	detail1, err := s.keys.Detail(ctx, installedID)
	if err != nil || string(detail1.ID) != installedID || detail1.Origin != "workspace" || detail1.LastRotatedAtMS <= 0 {
		t.Fatalf("rotated single-key detail = %+v, err=%v", detail1, err)
	}

	// Add two workspace keys and rotate the middle key in place; order of surrounding keys must be preserved.
	second, _, err := s.keys.CreateNamed(ctx, strings.Repeat("2", 32), "Middle Key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyAdd(ctx, strings.Repeat("2", 32), string(second.Key.ID), 9); err != nil {
		t.Fatal(err)
	}
	third, _, err := s.keys.CreateNamed(ctx, strings.Repeat("3", 32), "Tail Key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyAdd(ctx, strings.Repeat("3", 32), string(third.Key.ID), 9); err != nil {
		t.Fatal(err)
	}

	rotMidID := strings.Repeat("4", 32)
	stagedMid, _, err := s.keys.StageRotate(ctx, rotMidID, string(second.Key.ID))
	if err != nil {
		t.Fatal(err)
	}
	resMid, err := s.ApplyRotate(ctx, rotMidID, string(second.Key.ID), 9)
	if err != nil || resMid.RuntimeState != "effective_observed" {
		t.Fatalf("ApplyRotate middle key: %+v %v", resMid, err)
	}
	wantOrder := []string{staged1.NewSecret, stagedMid.NewSecret, third.CreatedSecret}
	if got := dest.snapshot(); !slices.Equal(got, wantOrder) {
		t.Fatalf("CPA keys order = %v, want %v", got, wantOrder)
	}

	// If old is already absent and next is already active on CPA before rotate is attempted, Prepare commits cleanly without extra PUT.
	rotPreHeldID := strings.Repeat("5", 32)
	stagedPreHeld, _, err := s.keys.StageRotate(ctx, rotPreHeldID, string(second.Key.ID))
	if err != nil {
		t.Fatal(err)
	}
	dest.mu.Lock()
	dest.keys = []string{staged1.NewSecret, stagedPreHeld.NewSecret, third.CreatedSecret}
	writesBefore := dest.keyWrites
	dest.mu.Unlock()

	resPreHeld, err := s.ApplyRotate(ctx, rotPreHeldID, string(second.Key.ID), 9)
	if err != nil || resPreHeld.RuntimeState != "effective_observed" || resPreHeld.Operation == nil || resPreHeld.Operation.Outcome != repo.OutcomeCommitted {
		t.Fatalf("ApplyRotate pre-held = %+v %v", resPreHeld, err)
	}
	dest.mu.Lock()
	writesAfter := dest.keyWrites
	dest.mu.Unlock()
	if writesAfter != writesBefore {
		t.Fatalf("pre-held rotate issued %d writes, want 0", writesAfter-writesBefore)
	}

	// If old is absent and next is NOT present on CPA, ApplyRotate fails closed with ErrSourceConflict and cleans up unstarted candidate.
	rotMissingOldID := strings.Repeat("6", 32)
	if _, _, err := s.keys.StageRotate(ctx, rotMissingOldID, string(second.Key.ID)); err != nil {
		t.Fatal(err)
	}
	dest.mu.Lock()
	dest.keys = []string{staged1.NewSecret, third.CreatedSecret}
	dest.mu.Unlock()

	if _, err := s.ApplyRotate(ctx, rotMissingOldID, string(second.Key.ID), 9); !errors.Is(err, cpa.ErrSourceConflict) {
		t.Fatalf("ApplyRotate when old missing without next = %v, want ErrSourceConflict", err)
	}
	if lingering, err := s.repo.RotationByRequestID(ctx, rotMissingOldID); err != nil || lingering != nil {
		t.Fatalf("unstarted failed rotation candidate was not cleaned up: %+v err=%v", lingering, err)
	}
}

func TestExternalRotateSingleKeyRecoveryAndRebindAbortCleanup(t *testing.T) {
	ctx := t.Context()
	h := newExternalHarness(t)
	keyID, initialSecret := h.staged(t, strings.Repeat("a", 32))
	h.cpa.mu.Lock()
	h.cpa.keys = []string{initialSecret}
	h.cpa.authKeys = []string{initialSecret}
	h.cpa.mu.Unlock()

	// Single-key rotation on External CPA works without ErrLastKey.
	rot1 := strings.Repeat("b", 32)
	staged1, created1, err := h.keys.StageRotate(ctx, rot1, keyID)
	if err != nil || !created1 {
		t.Fatalf("External StageRotate: %+v %v %v", staged1, created1, err)
	}
	res1, err := h.service.ApplyRotate(ctx, rot1, keyID, 1)
	if err != nil || res1.RuntimeState != "effective_observed" || res1.Operation == nil || res1.Operation.Outcome != repo.OutcomeCommitted {
		t.Fatalf("External ApplyRotate: %+v %v", res1, err)
	}
	h.cpa.mu.Lock()
	gotKeys1 := slices.Clone(h.cpa.keys)
	h.cpa.mu.Unlock()
	if !slices.Equal(gotKeys1, []string{staged1.NewSecret}) {
		t.Fatalf("External CPA keys = %v, want [%s]", gotKeys1, staged1.NewSecret)
	}

	// Prepare an unattempted rotation, then abort via AbortUnattemptedLatestForRebind; candidate row must be cleaned up so next rotation succeeds.
	rotAbort := strings.Repeat("c", 32)
	if _, _, err := h.keys.StageRotate(ctx, rotAbort, keyID); err != nil {
		t.Fatal(err)
	}
	prepAbort, err := h.service.Prepare(ctx, rotAbort, keyID, "", "rotate", 1)
	if err != nil || prepAbort.Operation == nil || prepAbort.Operation.Outcome != repo.OutcomePending {
		t.Fatalf("External Prepare rotate: %+v %v", prepAbort, err)
	}
	extID := ExternalIdentity(h.cpa.server.URL)
	if err := h.service.AbortUnattemptedLatestForRebind(ctx, extID, extID); err != nil {
		t.Fatalf("AbortUnattemptedLatestForRebind = %v", err)
	}
	if cand, err := h.service.repo.RotationByRequestID(ctx, rotAbort); err != nil || cand != nil {
		t.Fatalf("aborted External rotation candidate lingered: %+v %v", cand, err)
	}

	// Subsequent rotation with new request ID succeeds cleanly and reconciles via CheckOperation / ContinueOperation when stepped manually.
	rot2 := strings.Repeat("d", 32)
	staged2, created2, err := h.keys.StageRotate(ctx, rot2, keyID)
	if err != nil || !created2 {
		t.Fatalf("StageRotate after abort: %+v %v %v", staged2, created2, err)
	}
	prep2, err := h.service.Prepare(ctx, rot2, keyID, "", "rotate", 1)
	if err != nil || prep2.NextStep != "rotate" {
		t.Fatalf("Prepare rot2: %+v %v", prep2, err)
	}
	latestCheck, err := h.service.CheckOperation(ctx, "latest", 1)
	if err != nil || latestCheck.Operation == nil || latestCheck.Operation.ID != rot2 || latestCheck.NextStep != "rotate" {
		t.Fatalf("CheckOperation(latest) = %+v %v", latestCheck, err)
	}
	cont2, err := h.service.ContinueOperation(ctx, rot2, 1)
	if err != nil || cont2.RuntimeState != "effective_observed" || cont2.Operation.Outcome != repo.OutcomeCommitted {
		t.Fatalf("ContinueOperation rot2 = %+v %v", cont2, err)
	}
	h.cpa.mu.Lock()
	gotKeys2 := slices.Clone(h.cpa.keys)
	h.cpa.mu.Unlock()
	if !slices.Equal(gotKeys2, []string{staged2.NewSecret}) {
		t.Fatalf("External CPA keys after ContinueOperation = %v, want [%s]", gotKeys2, staged2.NewSecret)
	}
}

func TestOrphanedUnjournaledRotationSweptBeforeSubsequentRotateOrRevoke(t *testing.T) {
	ctx := t.Context()
	s, dest, _, installedID := managedFixture(t)
	if state, err := s.Apply(ctx, installedID, 9); err != nil || state.RuntimeState != "effective_observed" {
		t.Fatalf("initial apply: %+v %v", state, err)
	}
	second, _, err := s.keys.CreateNamed(ctx, strings.Repeat("2", 32), "Second Key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyAdd(ctx, strings.Repeat("2", 32), string(second.Key.ID), 9); err != nil {
		t.Fatal(err)
	}

	// Simulate crash after StageRotate(R1) before BeginOperation wrote settings('fresh_key_operation_v1:' || R1).
	orphanR1 := strings.Repeat("a", 32)
	if _, created, err := s.keys.StageRotate(ctx, orphanR1, installedID); err != nil || !created {
		t.Fatalf("StageRotate orphan R1: created=%v err=%v", created, err)
	}

	// Subsequent ApplyStagedRotate with R2 != R1 sweeps orphan R1 and commits cleanly without ErrConflict.
	r2 := strings.Repeat("b", 32)
	staged2, created2, res2, err := s.ApplyStagedRotate(ctx, r2, installedID, 9)
	if err != nil || !created2 || res2.RuntimeState != "effective_observed" || res2.Operation == nil || res2.Operation.Outcome != repo.OutcomeCommitted {
		t.Fatalf("ApplyStagedRotate(R2) after unjournaled R1: created=%v res=%+v err=%v", created2, res2, err)
	}
	if !slices.Contains(dest.snapshot(), staged2.NewSecret) {
		t.Fatalf("CPA snapshot %v missing rotated secret %s", dest.snapshot(), staged2.NewSecret)
	}

	// Stage an unjournaled rotation R0 on second.Key.ID, verify Preflight sweeps R0 cleanly!
	orphanR0 := strings.Repeat("e", 32)
	if _, created, err := s.keys.StageRotate(ctx, orphanR0, string(second.Key.ID)); err != nil || !created {
		t.Fatalf("StageRotate orphan R0: created=%v err=%v", created, err)
	}
	if err := s.Preflight(ctx, string(second.Key.ID), "", "revoke", 9); err != nil {
		t.Fatalf("Preflight revoke after unjournaled R0: %v", err)
	}
	if has, err := s.keys.HasStagedRotate(ctx, string(second.Key.ID)); err != nil || has {
		t.Fatalf("HasStagedRotate after Preflight sweep = %v, %v, want false", has, err)
	}

	// Stage another unjournaled rotation R3 on second.Key.ID, then Prepare a keeperless revoke — Prepare sweeps R3 cleanly.
	orphanR3 := strings.Repeat("c", 32)
	if _, created, err := s.keys.StageRotate(ctx, orphanR3, string(second.Key.ID)); err != nil || !created {
		t.Fatalf("StageRotate orphan R3: created=%v err=%v", created, err)
	}
	revID := strings.Repeat("d", 32)
	prepRev, err := s.Prepare(ctx, revID, string(second.Key.ID), "", "revoke", 9)
	if err != nil || prepRev.Operation == nil || prepRev.NextStep != "revoke" {
		t.Fatalf("Prepare revoke after unjournaled R3: %+v %v", prepRev, err)
	}
	if has, err := s.keys.HasStagedRotate(ctx, string(second.Key.ID)); err != nil || has {
		t.Fatalf("HasStagedRotate after Prepare sweep = %v, %v, want false", has, err)
	}
}
