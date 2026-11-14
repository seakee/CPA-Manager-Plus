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
