package freshkeyapply

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

func TestExternalApplyAddRequiresLiveModelsAuthAndRecoversWithoutDuplicateWrites(t *testing.T) {
	harness := newExternalHarness(t, "user-existing-key-alpha", "user-existing-key-beta")
	ctx := t.Context()

	reqID := strings.Repeat("a", 32)
	created, wasNew, err := harness.keys.Create(ctx, reqID)
	if err != nil || !wasNew {
		t.Fatalf("stage external key: %v (wasNew=%t)", err, wasNew)
	}

	// Simulate CPA persisting config.yaml immediately while client auth hot-reload
	// has not yet picked up the added secret.
	harness.cpa.mu.Lock()
	harness.cpa.holdHotReload = true
	harness.cpa.mu.Unlock()

	res, err := harness.service.ApplyAdd(ctx, reqID, string(created.Key.ID), 1)
	if !errors.Is(err, cpa.ErrApplyUnknown) {
		t.Fatalf("ApplyAdd during hot-reload delay = %v, want ErrApplyUnknown", err)
	}
	if res.Operation == nil || res.Operation.Outcome != "unknown" || res.RuntimeState != "effect_unknown" {
		t.Fatalf("ApplyAdd result during hot-reload delay = %+v", res)
	}
	harness.cpa.mu.Lock()
	writesAfterFirst := harness.cpa.writes
	persistedKeys := slices.Clone(harness.cpa.keys)
	harness.cpa.mu.Unlock()
	if writesAfterFirst != 1 || len(persistedKeys) != 3 || persistedKeys[0] != "user-existing-key-alpha" || persistedKeys[1] != "user-existing-key-beta" {
		t.Fatalf("external writes=%d keysLen=%d, want 1 write preserving user keys", writesAfterFirst, len(persistedKeys))
	}

	// Retrying ApplyAdd or ContinueOperation while unknown must reconcile read-only
	// and NEVER resend PUT or mint a second key.
	retryRes, retryErr := harness.service.ApplyAdd(ctx, reqID, string(created.Key.ID), 1)
	if !errors.Is(retryErr, cpa.ErrApplyUnknown) || retryRes.Operation == nil || retryRes.Operation.Outcome != "unknown" {
		t.Fatalf("retry ApplyAdd while unknown = %+v, %v", retryRes, retryErr)
	}
	harness.cpa.mu.Lock()
	writesAfterRetry := harness.cpa.writes
	harness.cpa.mu.Unlock()
	if writesAfterRetry != 1 {
		t.Fatalf("unknown operation resent PUT (%d writes, want 1)", writesAfterRetry)
	}

	listDuringDelay, unverified, listErr := harness.service.ObservedKeyList(ctx, 1)
	if listErr != nil || len(listDuringDelay) != 1 || listDuringDelay[0].RuntimeState != "effect_unknown" {
		t.Fatalf("ObservedKeyList during hot-reload delay = %+v (unverified=%t, err=%v)", listDuringDelay, unverified, listErr)
	}

	// Complete hot-reload on External CPA and reconcile read-only.
	harness.cpa.mu.Lock()
	harness.cpa.holdHotReload = false
	harness.cpa.authKeys = slices.Clone(harness.cpa.keys)
	harness.cpa.mu.Unlock()

	settled, checkErr := harness.service.CheckOperation(ctx, reqID, 1)
	if checkErr != nil || settled.Operation == nil || settled.Operation.Outcome != "committed" || settled.RuntimeState != "effective_observed" {
		t.Fatalf("CheckOperation after hot-reload = %+v, %v", settled, checkErr)
	}
	harness.cpa.mu.Lock()
	finalWrites := harness.cpa.writes
	harness.cpa.mu.Unlock()
	if finalWrites != 1 {
		t.Fatalf("read-only reconciliation mutated CPA (%d writes, want 1)", finalWrites)
	}

	observed, obsErr := harness.service.ObserveKey(ctx, created.Key, 1)
	if obsErr != nil || observed.RuntimeState != "effective_observed" || observed.Lifecycle != "active" {
		t.Fatalf("ObserveKey after settle = %+v (%v)", observed, obsErr)
	}
}

func TestVersionedAuthProofCacheEliminatesRepeatedModelSweepsOn130Keys(t *testing.T) {
	harness := newExternalHarness(t, "external-owner-seed")
	ctx := t.Context()

	var secrets []string
	secrets = append(secrets, "external-owner-seed")
	for i := range 130 {
		reqHex := strconv.FormatInt(int64(i+1), 16)
		reqID := strings.Repeat("0", 32-len(reqHex)) + reqHex
		res, _, err := harness.keys.Create(ctx, reqID)
		if err != nil {
			t.Fatalf("create key %d: %v", i, err)
		}
		secrets = append(secrets, res.CreatedSecret)
	}
	harness.cpa.mu.Lock()
	harness.cpa.keys = slices.Clone(secrets)
	harness.cpa.authKeys = slices.Clone(secrets)
	harness.cpa.modelHits = 0
	harness.cpa.mu.Unlock()

	firstList, unverified, err := harness.service.ObservedKeyList(ctx, 1)
	if err != nil || unverified || len(firstList) != 130 {
		t.Fatalf("first 130-key ObservedKeyList: len=%d unverified=%t err=%v", len(firstList), unverified, err)
	}
	for i, k := range firstList {
		if k.RuntimeState != "effective_observed" {
			t.Fatalf("key %d runtimeState = %q, want effective_observed", i, k.RuntimeState)
		}
	}
	harness.cpa.mu.Lock()
	hitsAfterFirst := harness.cpa.modelHits
	harness.cpa.mu.Unlock()
	if hitsAfterFirst == 0 {
		t.Fatal("cold cache did not verify client authentication on /v1/models")
	}

	// Second list poll + single-key detail poll against unchanged config version
	// must hit /v1/models zero times.
	secondList, unverified, err := harness.service.ObservedKeyList(ctx, 1)
	if err != nil || unverified || len(secondList) != 130 {
		t.Fatalf("second ObservedKeyList: len=%d unverified=%t err=%v", len(secondList), unverified, err)
	}
	single, err := harness.service.ObserveKey(ctx, secondList[64], 1)
	if err != nil || single.RuntimeState != "effective_observed" {
		t.Fatalf("single ObserveKey on cached version = %+v (%v)", single, err)
	}
	harness.cpa.mu.Lock()
	hitsAfterCached := harness.cpa.modelHits
	harness.cpa.mu.Unlock()
	if hitsAfterCached != hitsAfterFirst {
		t.Fatalf("cached polls triggered /v1/models hits: %d -> %d", hitsAfterFirst, hitsAfterCached)
	}

	// Out-of-band config change invalidates the versioned proof cache: removing
	// key 0 from CPA must immediately surface as not_applied rather than cached effective.
	harness.cpa.mu.Lock()
	removedSecret := secrets[1]
	harness.cpa.keys = slices.DeleteFunc(slices.Clone(harness.cpa.keys), func(v string) bool { return v == removedSecret })
	harness.cpa.authKeys = slices.Clone(harness.cpa.keys)
	harness.cpa.mu.Unlock()

	afterRemoval, err := harness.service.ObserveKey(ctx, secondList[0], 1)
	if err != nil {
		t.Fatal(err)
	}
	// Find whichever workspace key corresponded to removedSecret and verify it is not_applied.
	thirdList, _, err := harness.service.ObservedKeyList(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	notAppliedCount := 0
	for _, k := range thirdList {
		if k.RuntimeState == "not_applied" {
			notAppliedCount++
		}
	}
	_ = afterRemoval
	if notAppliedCount != 1 {
		t.Fatalf("not_applied count after removing one key from CPA = %d, want 1", notAppliedCount)
	}
}

func TestSourceVersionChangeDoesNotPromoteOtherKeysFromAnOlderProof(t *testing.T) {
	h := newExternalHarness(t, "external-owner-seed")
	ctx := t.Context()
	a, _, err := h.keys.Create(ctx, strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := h.keys.Create(ctx, strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	h.cpa.mu.Lock()
	h.cpa.keys = []string{"external-owner-seed", a.CreatedSecret, b.CreatedSecret}
	h.cpa.authKeys = slices.Clone(h.cpa.keys)
	h.cpa.mu.Unlock()
	for _, key := range []freshkeys.Key{a.Key, b.Key} {
		observed, err := h.service.ObserveKey(ctx, key, 1)
		if err != nil || observed.RuntimeState != "effective_observed" {
			t.Fatalf("initial client proof = %s, %v", observed.RuntimeState, err)
		}
	}
	// An out-of-band config reload changes the source version while one key's
	// live authentication is still stale. Proving A must not re-certify B.
	h.cpa.mu.Lock()
	h.cpa.keys = []string{"external-owner-seed", b.CreatedSecret, a.CreatedSecret}
	h.cpa.authKeys = []string{"external-owner-seed", a.CreatedSecret}
	h.cpa.holdHotReload = true
	h.cpa.mu.Unlock()
	if observed, err := h.service.ObserveKey(ctx, a.Key, 1); err != nil || observed.RuntimeState != "effective_observed" {
		t.Fatalf("current A proof = %s, %v", observed.RuntimeState, err)
	}
	if observed, err := h.service.ObserveKey(ctx, b.Key, 1); err != nil || observed.RuntimeState != "effect_unknown" {
		t.Fatalf("B inherited stale authentication proof: %s, %v", observed.RuntimeState, err)
	}
}

func TestManagedRuntimeRestartInvalidatesCachedClientAuthentication(t *testing.T) {
	s, destination, _, id := managedFixture(t)
	ctx := t.Context()
	if _, err := s.Apply(ctx, id, 9); err != nil {
		t.Fatal(err)
	}
	key, err := s.keys.Detail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if observed, err := s.ObserveKey(ctx, key, 9); err != nil || observed.RuntimeState != "effective_observed" {
		t.Fatalf("initial client authentication = %s, %v", observed.RuntimeState, err)
	}
	s.runtime.(*observedRuntime).status.Generation++
	destination.mu.Lock()
	destination.authOpen = true
	destination.mu.Unlock()
	if observed, err := s.ObserveKey(ctx, key, 9); err != nil || observed.RuntimeState != "effect_unknown" {
		t.Fatalf("new Runtime inherited old client proof: %s, %v", observed.RuntimeState, err)
	}
}

func TestPreparedAddAlreadyLoadedCommitsWithoutResending(t *testing.T) {
	s, destination, _, firstID := managedFixture(t)
	ctx := t.Context()
	if _, err := s.Apply(ctx, firstID, 9); err != nil { t.Fatal(err) }
	created, _, err := s.keys.Create(ctx, strings.Repeat("a", 32))
	if err != nil { t.Fatal(err) }
	requestID := strings.Repeat("d", 32)
	prepared, err := s.Prepare(ctx, requestID, string(created.Key.ID), "", "add", 9)
	if err != nil || prepared.Operation == nil || prepared.Operation.Outcome != "pending" { t.Fatalf("prepare add: %v", err) }
	destination.mu.Lock()
	writesBefore := destination.keyWrites
	destination.keys = append(destination.keys, created.CreatedSecret)
	destination.mu.Unlock()
	settled, err := s.CheckOperation(ctx, requestID, 9)
	if err != nil || settled.Operation == nil || settled.Operation.Outcome != "committed" || settled.RuntimeState != "effective_observed" {
		t.Fatalf("loaded add failed to settle its journal: state=%s, operation=%+v, err=%v", settled.RuntimeState, settled.Operation, err)
	}
	destination.mu.Lock()
	writesAfter := destination.keyWrites
	destination.mu.Unlock()
	if writesAfter != writesBefore { t.Fatal("read-only add recovery resent a CPA write") }
	next, _, err := s.keys.Create(ctx, strings.Repeat("b", 32))
	if err != nil { t.Fatal(err) }
	if _, err := s.Prepare(ctx, strings.Repeat("e", 32), string(next.Key.ID), "", "add", 9); err != nil {
		t.Fatalf("settled add still blocks the next key operation: %v", err)
	}
}
