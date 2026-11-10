package freshkeyapply

import (
	"errors"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

func TestStrategySelectorFailsClosedWhenCheckpointCannotBeRead(t *testing.T) {
	selector := NewStrategySelector(
		func(bootstrap.FreshOperation) *Service {
			t.Fatal("selectFor must not run when loadOp fails")
			return nil
		},
		func() (bootstrap.FreshOperation, bool) {
			return bootstrap.FreshOperation{}, false
		},
	)

	ctx := t.Context()
	if got := selector.Profile(); got != "" {
		t.Fatalf("Profile() = %q, want empty", got)
	}
	if got := selector.ManagedEndpoint(); got != "" {
		t.Fatalf("ManagedEndpoint() = %q, want empty", got)
	}
	if selector.DefaultInstallCPAAuthorityProven(ctx) {
		t.Fatal("DefaultInstallCPAAuthorityProven() = true, want false")
	}
	if selector.KeyOperationsEnabled() {
		t.Fatal("KeyOperationsEnabled() = true, want false")
	}
	if _, err := selector.ConfigTarget(ctx, 1); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ConfigTarget() err = %v, want ErrUnavailable", err)
	}
	if _, err := selector.AdoptedDestinationKeys(ctx, strings.Repeat("a", 64)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("AdoptedDestinationKeys() err = %v, want ErrUnavailable", err)
	}
	if _, err := selector.Admit(ctx, "sk-"+strings.Repeat("a", 70), 1, strings.Repeat("b", 64)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Admit() err = %v, want ErrUnavailable", err)
	}
	if _, _, err := selector.ExternalTarget(ctx, 1); !errors.Is(err, ErrExternalUnavailable) {
		t.Fatalf("ExternalTarget() err = %v, want ErrExternalUnavailable", err)
	}
	keyID := identity.APIKeyID("key_1")
	if _, err := selector.ExternalKeyPresence(ctx, []identity.APIKeyID{keyID}); !errors.Is(err, ErrExternalUnavailable) {
		t.Fatalf("ExternalKeyPresence() err = %v, want ErrExternalUnavailable", err)
	}
	if _, err := selector.ExternalKeyPresenceOf(ctx, keyID); !errors.Is(err, ErrExternalUnavailable) {
		t.Fatalf("ExternalKeyPresenceOf() err = %v, want ErrExternalUnavailable", err)
	}
	seedKeys := []freshkeys.Key{{ID: keyID}}
	if gotKeys, unverified, err := selector.ExternalKeyList(ctx, seedKeys); !errors.Is(err, ErrExternalUnavailable) || !unverified || gotKeys != nil {
		t.Fatalf("ExternalKeyList() = (%v, %v, %v), want nil, true, ErrExternalUnavailable", gotKeys, unverified, err)
	}
	if _, err := selector.Rebind(ctx, nil, nil, 1, "http://127.0.0.1:8317", "secret"); !errors.Is(err, ErrExternalUnavailable) {
		t.Fatalf("Rebind() err = %v, want ErrExternalUnavailable", err)
	}
	if res, err := selector.Prepare(ctx, "req-1", "key-1", "", "revoke", 1); !errors.Is(err, ErrUnavailable) || res.RuntimeState != "effect_unknown" {
		t.Fatalf("Prepare() = (%+v, %v), want effect_unknown + ErrUnavailable", res, err)
	}
	if _, err := selector.CheckOperation(ctx, "req-1", 1); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("CheckOperation() err = %v, want ErrUnavailable", err)
	}
	if res, err := selector.ContinueOperation(ctx, "req-1", 1); !errors.Is(err, ErrUnavailable) || res.RuntimeState != "effect_unknown" {
		t.Fatalf("ContinueOperation() = (%+v, %v), want effect_unknown + ErrUnavailable", res, err)
	}
	if st, err := selector.Apply(ctx, "key-1", 1); !errors.Is(err, ErrUnavailable) || st.RuntimeState != "effect_unknown" {
		t.Fatalf("Apply() = (%+v, %v), want effect_unknown + ErrUnavailable", st, err)
	}
	if st, err := selector.Observe(ctx, "key-1", 1); !errors.Is(err, ErrUnavailable) || st.RuntimeState != "effect_unknown" {
		t.Fatalf("Observe() = (%+v, %v), want effect_unknown + ErrUnavailable", st, err)
	}
	if _, err := selector.EnsureInitialInstall(ctx, "run-1", 1); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("EnsureInitialInstall() err = %v, want ErrUnavailable", err)
	}
	selector.EnableH1(nil)

	nilTargetSelector := NewStrategySelector(
		func(bootstrap.FreshOperation) *Service { return nil },
		func() (bootstrap.FreshOperation, bool) { return bootstrap.FreshOperation{}, true },
	)
	for _, surface := range canonicalSurfaces {
		if nilTargetSelector.Targets(bootstrap.FreshOperation{
			Strategy: bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed"},
		}, surface) {
			t.Fatalf("Targets(%s) = true on nil selected service", surface)
		}
	}
}

