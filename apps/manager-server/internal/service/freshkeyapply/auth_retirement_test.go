package freshkeyapply

import (
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
)

func TestRetiredExternalKeyRequiresLiveClosedAuthRejection(t *testing.T) {
	for _, scenario := range []string{"closed-and-rejected", "old-key-still-authenticates", "empty-keyset"} {
		t.Run(scenario, func(t *testing.T) {
			h := newExternalHarness(t, "owner-seed")
			id, secret := h.staged(t, strings.Repeat("a", 32))
			if _, err := h.service.ApplyAdd(t.Context(), strings.Repeat("a", 32), id, 1); err != nil {
				t.Fatal(err)
			}
			prepared, err := h.service.Prepare(t.Context(), strings.Repeat("b", 32), id, "", "revoke", 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.service.ContinueOperation(t.Context(), prepared.Operation.ID, 1); err != nil {
				t.Fatal(err)
			}
			key, err := h.keys.Detail(t.Context(), id)
			if err != nil || key.Lifecycle != identity.LifecycleSuperseded {
				t.Fatalf("retired canonical key = %+v, %v", key, err)
			}
			h.service.authCache = authProofCache{}
			h.cpa.mu.Lock()
			switch scenario {
			case "old-key-still-authenticates":
				h.cpa.holdHotReload = true
				h.cpa.authKeys = []string{"owner-seed", secret}
			case "empty-keyset":
				h.cpa.keys = []string{}
			}
			h.cpa.mu.Unlock()
			want := "effect_unknown"
			if scenario == "closed-and-rejected" {
				want = "revoked_observed"
			}
			observed, err := h.service.ObserveKey(t.Context(), key, 0)
			if err != nil || observed.RuntimeState != want {
				t.Fatalf("detail = %s, %v; want %s", observed.RuntimeState, err, want)
			}
			rows, unverified, err := h.service.ObservedKeyList(t.Context(), 0)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range rows {
				if row.ID == key.ID {
					found = true
					if row.RuntimeState != want {
						t.Fatalf("list = %s; want %s", row.RuntimeState, want)
					}
				}
			}
			if !found || unverified != (want == "effect_unknown") {
				t.Fatalf("list found=%t, unverified=%t for %s", found, unverified, scenario)
			}
		})
	}
}

func TestObservedKeyListStillRunsTrailingFenceWhenSupersededKeyHitsCache(t *testing.T) {
	h := newExternalHarness(t, "owner-seed")
	retiredID, _ := h.staged(t, strings.Repeat("c", 32))
	if _, err := h.service.ApplyAdd(t.Context(), strings.Repeat("c", 32), retiredID, 1); err != nil {
		t.Fatal(err)
	}
	prepared, err := h.service.Prepare(t.Context(), strings.Repeat("d", 32), retiredID, "", "revoke", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.ContinueOperation(t.Context(), prepared.Operation.ID, 1); err != nil {
		t.Fatal(err)
	}

	activeID, _ := h.staged(t, strings.Repeat("e", 32))
	if _, err := h.service.ApplyAdd(t.Context(), strings.Repeat("e", 32), activeID, 1); err != nil {
		t.Fatal(err)
	}

	// Warm only the rejection cache for retiredID at the current snapshot version
	// while leaving activeID unverified so ObservedKeyList positively samples
	// activeID (fencing once) and then hits the rejection cache for retiredID.
	h.service.authCache = authProofCache{}
	retiredKey, err := h.keys.Detail(t.Context(), retiredID)
	if err != nil {
		t.Fatal(err)
	}
	if obs, err := h.service.ObserveKey(t.Context(), retiredKey, 0); err != nil || obs.RuntimeState != "revoked_observed" {
		t.Fatalf("warm retired cache = %s, %v", obs.RuntimeState, err)
	}

	// Rotate CPA's live keyset right before key-read #3 (the trailing fence after
	// observeRetirements) so only the trailing fence can catch the concurrent edit.
	reads := 0
	h.cpa.mu.Lock()
	origKeys := append([]string(nil), h.cpa.keys...)
	h.cpa.onKeyRead = func(f *fakeExternalCPA) {
		reads++
		if reads == 3 {
			f.keys = []string{"owner-seed"}
		}
	}
	h.cpa.mu.Unlock()
	defer func() {
		h.cpa.mu.Lock()
		h.cpa.onKeyRead = nil
		h.cpa.keys = origKeys
		h.cpa.mu.Unlock()
	}()

	rows, unverified, err := h.service.ObservedKeyList(t.Context(), 0)
	if reads < 3 {
		t.Fatalf("expected trailing ObservationHeld read #3 after superseded DB read, got %d reads", reads)
	}
	if err == nil || !unverified {
		t.Fatalf("expected trailing fence to detect rotation during superseded read, got rows=%+v unverified=%t err=%v", rows, unverified, err)
	}
	for _, row := range rows {
		if row.RuntimeState != "effect_unknown" {
			t.Fatalf("expected row %s downgraded to effect_unknown, got %s", row.ID, row.RuntimeState)
		}
	}
}
