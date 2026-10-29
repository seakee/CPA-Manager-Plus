package freshkeyapply

import (
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// The canonical surfaces in the order Targets is asked about them.
var canonicalSurfaces = []Surface{SurfaceKeyInstall, SurfaceKeyOperation, SurfaceChatAdmission, SurfaceGatewayConfig}

// keySurfaces are the surfaces that only ever acted on the managed strategy, so
// they keep that rule inside the one discriminator.
var keySurfaces = []Surface{SurfaceKeyInstall, SurfaceKeyOperation, SurfaceChatAdmission}

// operation wraps one checkpoint in the Fresh operation the surfaces ask about.
func operation(checkpoint bootstrap.FreshStrategyCheckpoint) bootstrap.FreshOperation {
	return bootstrap.FreshOperation{Strategy: checkpoint}
}

func sameAnswer(t *testing.T, service *Service, op bootstrap.FreshOperation, want bool, surfaces ...Surface) {
	t.Helper()
	for _, surface := range surfaces {
		if got := service.Targets(op, surface); got != want {
			t.Fatalf("Targets(%s) = %v, want %v", surface, got, want)
		}
	}
}

// Targets is the one profile discriminator the key install, the key operation,
// the chat consumer and the gateway configuration page ask. A managed
// installation owns exactly its committed managed destination on every surface;
// the private profile owns exactly the attested private target it has always
// owned, and only its own page shipped without the managed strategy.
func TestTargetsSelectsTheProfileOfTheService(t *testing.T) {
	managedDestination := "managed:sha256:" + strings.Repeat("c", 64)
	managedCheckpoint := bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.NewManaged, Stage: "committed",
		TargetIdentity: managedRuntimeIdentity, TargetGeneration: 9, TargetArtifactID: managedArtifact(),
		DestinationConnectionIdentity: managedDestination,
	}
	privateCheckpoint := bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.NewManaged, Stage: "committed",
		TargetIdentity: cpa.PrivateKeyRuntime, TargetGeneration: 9, TargetArtifactID: cpa.PrivateKeyArtifact,
	}
	// The private H1/direct page and field journal predate the managed
	// checkpoint and select the attested target alone, so they still accept an
	// adopted checkpoint naming it. The key surfaces never did.
	adoptedPrivate := privateCheckpoint
	adoptedPrivate.Strategy = bootstrap.AdoptExisting
	externalCheckpoint := bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.ConnectExternal, Stage: "committed",
		TargetIdentity: "external:sha256:" + strings.Repeat("d", 64), TargetGeneration: 1,
	}
	stagedPrivate := privateCheckpoint
	stagedPrivate.Stage = "staged"
	wrongArtifact := privateCheckpoint
	wrongArtifact.TargetArtifactID = "sha256:" + strings.Repeat("e", 64)
	unboundManaged := managedCheckpoint
	unboundManaged.DestinationConnectionIdentity = ""
	stagedManaged := managedCheckpoint
	stagedManaged.Stage = "staged"

	private := New(nil, nil, nil)
	managed := NewManaged(nil, nil, nil, "http://127.0.0.1:8317", nil)
	external := NewExternal(nil, nil, nil)
	if private.profile != ProfilePrivate {
		t.Fatalf("New profile = %q", private.profile)
	}
	if managed.profile != ProfileManaged {
		t.Fatalf("NewManaged profile = %q", managed.profile)
	}
	if external.profile != ProfileExternal {
		t.Fatalf("NewExternal profile = %q", external.profile)
	}
	var nilService *Service
	t.Run("nil", func(t *testing.T) {
		for _, surface := range canonicalSurfaces {
			if nilService.Targets(operation(managedCheckpoint), surface) {
				t.Fatalf("nil service authorized %s", surface)
			}
		}
	})
	t.Run("managed owns the managed destination on every surface", func(t *testing.T) {
		sameAnswer(t, managed, operation(managedCheckpoint), true, canonicalSurfaces...)
	})
	t.Run("managed refuses the private target", func(t *testing.T) {
		sameAnswer(t, managed, operation(privateCheckpoint), false, canonicalSurfaces...)
	})
	t.Run("managed refuses an unbound checkpoint", func(t *testing.T) {
		sameAnswer(t, managed, operation(unboundManaged), false, canonicalSurfaces...)
	})
	t.Run("managed owns an adopted destination on every surface", func(t *testing.T) {
		// Adopt is Manager-owned: this Manager staged the destination, started it
		// and holds the install-derived credential. Its connection identity is the
		// `external:` digest the adoption recorded, so the prefix comes from the
		// strategy and is never inferred from the value.
		adopted := bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.AdoptExisting, SourceClass: bootstrap.AdoptNetworkExisting, Stage: "committed",
			TargetIdentity: managedRuntimeIdentity, TargetGeneration: 9, TargetArtifactID: managedArtifact(),
			DestinationConnectionIdentity: "external:sha256:" + strings.Repeat("f", 64),
		}
		sameAnswer(t, managed, operation(adopted), true, canonicalSurfaces...)
	})
	t.Run("managed refuses an adopted checkpoint bound by the managed prefix", func(t *testing.T) {
		// The two identities are distinct values. An adopted checkpoint carrying
		// the New Managed prefix is a record this Manager never wrote.
		adopted := bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.AdoptExisting, SourceClass: bootstrap.AdoptNetworkExisting, Stage: "committed",
			TargetIdentity: managedRuntimeIdentity, TargetGeneration: 9, TargetArtifactID: managedArtifact(),
			DestinationConnectionIdentity: managedDestination,
		}
		sameAnswer(t, managed, operation(adopted), false, canonicalSurfaces...)
	})
	t.Run("managed refuses a legacy_managed adoption on every surface", func(t *testing.T) {
		// `legacy_managed` is Upgrade's source class, not this lane's authority: no
		// Fresh adopt path staged that destination, so no key, admission or gateway
		// write may be granted on it.
		legacy := bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.AdoptExisting, SourceClass: bootstrap.AdoptLegacyManaged, Stage: "committed",
			TargetIdentity: managedRuntimeIdentity, TargetGeneration: 9, TargetArtifactID: managedArtifact(),
			DestinationConnectionIdentity: "external:sha256:" + strings.Repeat("f", 64),
		}
		sameAnswer(t, managed, operation(legacy), false, canonicalSurfaces...)
	})
	t.Run("managed refuses an unbound adopted checkpoint", func(t *testing.T) {
		sameAnswer(t, managed, operation(adoptedPrivate), false, canonicalSurfaces...)
	})
	t.Run("managed never answers Connect External", func(t *testing.T) {
		// The plugin-opt-in and authority generalizations stop at the two
		// Manager-owned strategies: the user's own CPA stays outside this
		// authority on every surface, including the gateway page.
		sameAnswer(t, managed, operation(externalCheckpoint), false, canonicalSurfaces...)
	})
	t.Run("managed refuses a staged checkpoint", func(t *testing.T) {
		sameAnswer(t, managed, operation(stagedManaged), false, canonicalSurfaces...)
	})
	t.Run("managed refuses an external checkpoint", func(t *testing.T) {
		sameAnswer(t, managed, operation(externalCheckpoint), false, canonicalSurfaces...)
	})
	t.Run("private owns the attested target on every surface", func(t *testing.T) {
		sameAnswer(t, private, operation(privateCheckpoint), true, canonicalSurfaces...)
	})
	t.Run("private keeps its adopted target only on the gateway page", func(t *testing.T) {
		sameAnswer(t, private, operation(adoptedPrivate), false, keySurfaces...)
		sameAnswer(t, private, operation(adoptedPrivate), true, SurfaceGatewayConfig)
	})
	t.Run("private refuses a staged target", func(t *testing.T) {
		sameAnswer(t, private, operation(stagedPrivate), false, canonicalSurfaces...)
	})
	t.Run("private refuses a foreign artifact", func(t *testing.T) {
		sameAnswer(t, private, operation(wrongArtifact), false, canonicalSurfaces...)
	})
	t.Run("private refuses the managed destination", func(t *testing.T) {
		sameAnswer(t, private, operation(managedCheckpoint), false, canonicalSurfaces...)
	})
	t.Run("private refuses an external checkpoint", func(t *testing.T) {
		sameAnswer(t, private, operation(externalCheckpoint), false, canonicalSurfaces...)
	})
	t.Run("external owns the external target on the managed surfaces", func(t *testing.T) {
		sameAnswer(t, external, operation(externalCheckpoint), true, SurfaceKeyInstall, SurfaceKeyOperation, SurfaceGatewayConfig)
	})
	t.Run("external never answers chat admission", func(t *testing.T) {
		sameAnswer(t, external, operation(externalCheckpoint), false, SurfaceChatAdmission)
	})
	t.Run("external refuses an unknown surface", func(t *testing.T) {
		// The External surfaces are an allowlist: a name this Manager does not
		// define is refused instead of inheriting the loose half of the rule.
		sameAnswer(t, external, operation(externalCheckpoint), false, Surface("unrecognized"), Surface("gateway_admission"), Surface(""))
	})
	t.Run("external refuses a managed destination", func(t *testing.T) {
		sameAnswer(t, external, operation(managedCheckpoint), false, canonicalSurfaces...)
	})
	t.Run("external refuses the private target", func(t *testing.T) {
		sameAnswer(t, external, operation(privateCheckpoint), false, canonicalSurfaces...)
	})
	t.Run("external refuses an adopted checkpoint", func(t *testing.T) {
		sameAnswer(t, external, operation(adoptedPrivate), false, canonicalSurfaces...)
	})
	t.Run("external refuses a staged checkpoint", func(t *testing.T) {
		staged := externalCheckpoint
		staged.Stage = "validated"
		sameAnswer(t, external, operation(staged), false, canonicalSurfaces...)
	})
	t.Run("external refuses an unbound checkpoint", func(t *testing.T) {
		unbound := externalCheckpoint
		unbound.TargetIdentity = ""
		sameAnswer(t, external, operation(unbound), false, canonicalSurfaces...)
	})
	t.Run("external refuses a foreign connection identity", func(t *testing.T) {
		foreign := externalCheckpoint
		foreign.TargetIdentity = "managed:sha256:" + strings.Repeat("d", 64)
		sameAnswer(t, external, operation(foreign), false, canonicalSurfaces...)
	})
	t.Run("external profiles stay mutually exclusive", func(t *testing.T) {
		for _, service := range []*Service{managed, private} {
			if service.Targets(operation(externalCheckpoint), SurfaceKeyInstall) {
				t.Fatal("a non-External profile accepted the External checkpoint")
			}
		}
	})
	t.Run("unknown surface is answered by the strictest rule", func(t *testing.T) {
		if private.Targets(operation(adoptedPrivate), Surface("unrecognized")) {
			t.Fatal("an unknown surface accepted an adopted private checkpoint")
		}
		sameAnswer(t, private, operation(privateCheckpoint), true, Surface("unrecognized"))
	})
}
