package freshkeyapply

import (
	"context"
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
	if gotKeys, unverified, err := selector.ExternalKeyList(ctx, seedKeys); err != nil || unverified || len(gotKeys) != 1 {
		t.Fatalf("ExternalKeyList() = (%v, %v, %v), want unchanged keys, false, nil", gotKeys, unverified, err)
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

func TestStrategySelectorSwitchesBetweenManagedAdoptAndExternal(t *testing.T) {
	managedDestination := "managed:sha256:" + strings.Repeat("c", 64)
	adoptedDestination := "external:sha256:" + strings.Repeat("d", 64)
	externalTarget := "external:sha256:" + strings.Repeat("e", 64)
	managed := NewManaged(nil, nil, nil, "http://127.0.0.1:8317", nil)
	external := NewExternal(nil, nil, nil)

	var current bootstrap.FreshOperation
	selector := NewStrategySelector(
		func(op bootstrap.FreshOperation) *Service {
			if op.Strategy.Strategy == bootstrap.ConnectExternal {
				return external
			}
			return managed
		},
		func() (bootstrap.FreshOperation, bool) {
			return current, true
		},
	)

	// 1. Uncommitted defaults to Managed profile while refusing uncommitted Targets.
	if selector.Profile() != ProfileManaged {
		t.Fatalf("uncommitted Profile() = %q, want %q", selector.Profile(), ProfileManaged)
	}
	sameAnswer(t, selector, current, false, canonicalSurfaces...)

	// 2. Committed NewManaged selects ProfileManaged across all four surfaces.
	current = operation(bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.NewManaged, Stage: "committed",
		TargetIdentity: managedRuntimeIdentity, TargetGeneration: 1, TargetArtifactID: managedArtifact(),
		DestinationConnectionIdentity: managedDestination,
	})
	if selector.Profile() != ProfileManaged || selector.ManagedEndpoint() != "http://127.0.0.1:8317" {
		t.Fatalf("NewManaged Profile=%q Endpoint=%q", selector.Profile(), selector.ManagedEndpoint())
	}
	sameAnswer(t, selector, current, true, canonicalSurfaces...)

	// 3. Committed AdoptExisting (network-existing) also selects ProfileManaged.
	current = operation(bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.AdoptExisting, SourceClass: bootstrap.AdoptNetworkExisting, Stage: "committed",
		TargetIdentity: managedRuntimeIdentity, TargetGeneration: 2, TargetArtifactID: managedArtifact(),
		DestinationConnectionIdentity: adoptedDestination,
	})
	if selector.Profile() != ProfileManaged {
		t.Fatalf("AdoptExisting Profile() = %q, want %q", selector.Profile(), ProfileManaged)
	}
	sameAnswer(t, selector, current, true, canonicalSurfaces...)

	// 4. Committed ConnectExternal selects ProfileExternal: key/config surfaces enabled, chat admission closed.
	current = operation(bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.ConnectExternal, Stage: "committed",
		TargetIdentity: externalTarget, TargetGeneration: 3,
	})
	if selector.Profile() != ProfileExternal || selector.ManagedEndpoint() != "" {
		t.Fatalf("ConnectExternal Profile=%q Endpoint=%q", selector.Profile(), selector.ManagedEndpoint())
	}
	if selector.DefaultInstallCPAAuthorityProven(context.Background()) {
		t.Fatal("ConnectExternal proved DefaultInstallCPAAuthorityProven")
	}
	sameAnswer(t, selector, current, true, SurfaceKeyInstall, SurfaceKeyOperation, SurfaceGatewayConfig)
	sameAnswer(t, selector, current, false, SurfaceChatAdmission)
}
