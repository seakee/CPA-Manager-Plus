package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
)

// TestManagedInstallSourceResolvesBothManagerOwnedStrategies proves the one
// install-derived authority answers for New Managed and Adopt, and that it
// discriminates by strategy rather than by identity prefix: the two carry
// different prefixes for the same normalized address, and a checkpoint whose
// identity belongs to the other strategy is refused.
func TestManagedInstallSourceResolvesBothManagerOwnedStrategies(t *testing.T) {
	protector, err := security.NewProtector([]byte("managed-install-source-test"))
	if err != nil {
		t.Fatal(err)
	}
	const management = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
	const endpoint = "http://cpa.test:8317"
	protected, err := protector.ProtectString(management)
	if err != nil {
		t.Fatal(err)
	}
	recordOf := func(strategy bootstrap.CPAStrategy, class bootstrap.AdoptSourceClass, identity string) freshstore.Record {
		return freshstore.Record{
			Operation: bootstrap.FreshOperation{Strategy: bootstrap.FreshStrategyCheckpoint{
				Strategy: strategy, SourceClass: class, Stage: "committed",
				TargetIdentity: "embedded-default", TargetGeneration: 7,
				TargetArtifactID:              "sha256:" + strings.Repeat("a", 64),
				DestinationConnectionIdentity: identity,
			}},
			Connection: &model.ManagerCPAConnectionConfig{CPABaseURL: endpoint, ManagementKey: protected},
		}
	}
	managedIdentity := freshstrategy.ManagedDestinationIdentity(endpoint)
	adoptedIdentity := freshstrategy.AdoptedDestinationIdentity(endpoint)
	if managedIdentity == adoptedIdentity || !strings.HasPrefix(adoptedIdentity, "external:sha256:") {
		t.Fatalf("the two Manager-owned identities are not distinct: %s / %s", managedIdentity, adoptedIdentity)
	}
	for _, strategy := range []struct {
		name     string
		strategy bootstrap.CPAStrategy
		class    bootstrap.AdoptSourceClass
		identity string
	}{
		{"new managed", bootstrap.NewManaged, bootstrap.AdoptSourceNone, managedIdentity},
		{"adopt", bootstrap.AdoptExisting, bootstrap.AdoptNetworkExisting, adoptedIdentity},
	} {
		t.Run(strategy.name, func(t *testing.T) {
			store := staticStore{recordOf(strategy.strategy, strategy.class, strategy.identity)}
			install, err := managedInstallSource(store, protector, endpoint)(t.Context())
			if err != nil {
				t.Fatalf("a committed %s installation was refused: %v", strategy.name, err)
			}
			if install.BaseURL != endpoint || install.ManagementKey != management || install.Generation != "7" ||
				install.Identity != "embedded-default" || install.RunID != freshkeyapply.ManagedRunID("") {
				t.Fatalf("resolved %+v", install)
			}
		})
	}
	// The prefix is never the discriminator: each strategy accepts exactly its own
	// identity and refuses the other's.
	crossed := []struct {
		name     string
		strategy bootstrap.CPAStrategy
		class    bootstrap.AdoptSourceClass
		identity string
	}{
		{"adopt bound by the managed identity", bootstrap.AdoptExisting, bootstrap.AdoptNetworkExisting, managedIdentity},
		{"new managed bound by the adopted identity", bootstrap.NewManaged, bootstrap.AdoptSourceNone, adoptedIdentity},
		{"adopt unbound", bootstrap.AdoptExisting, bootstrap.AdoptNetworkExisting, ""},
		{"connect external is outside this authority", bootstrap.ConnectExternal, bootstrap.AdoptSourceNone, freshkeyapply.ExternalIdentity(endpoint)},
	}
	for _, scenario := range crossed {
		t.Run(scenario.name, func(t *testing.T) {
			store := staticStore{recordOf(scenario.strategy, scenario.class, scenario.identity)}
			if _, err := managedInstallSource(store, protector, endpoint)(t.Context()); !errors.Is(err, freshkeyapply.ErrUnavailable) {
				t.Fatalf("%s resolved an install: %v", scenario.name, err)
			}
		})
	}
	// A legacy_managed adoption is Upgrade's source class: its committed
	// destination is not a Fresh staged one, so this authority refuses it even
	// though the strategy and the identity would otherwise match.
	legacy := recordOf(bootstrap.AdoptExisting, bootstrap.AdoptLegacyManaged, adoptedIdentity)
	if _, err := managedInstallSource(staticStore{legacy}, protector, endpoint)(t.Context()); !errors.Is(err, freshkeyapply.ErrUnavailable) {
		t.Fatalf("a legacy_managed adoption resolved the Manager-owned authority: %v", err)
	}
	// A moved destination is a different installation, on both strategies.
	for _, strategy := range []struct {
		name     string
		strategy bootstrap.CPAStrategy
		class    bootstrap.AdoptSourceClass
	}{
		{"new managed", bootstrap.NewManaged, bootstrap.AdoptSourceNone},
		{"adopt", bootstrap.AdoptExisting, bootstrap.AdoptNetworkExisting},
	} {
		t.Run(strategy.name+" at another address", func(t *testing.T) {
			moved := recordOf(strategy.strategy, strategy.class, freshstrategy.ManagedDestinationIdentity("http://moved.test:8317"))
			if strategy.strategy == bootstrap.AdoptExisting {
				moved.Operation.Strategy.DestinationConnectionIdentity = freshstrategy.AdoptedDestinationIdentity("http://moved.test:8317")
			}
			moved.Connection.CPABaseURL = "http://moved.test:8317"
			if _, err := managedInstallSource(staticStore{moved}, protector, endpoint)(t.Context()); !errors.Is(err, freshkeyapply.ErrUnavailable) {
				t.Fatalf("%s followed a moved connection: %v", strategy.name, err)
			}
		})
	}
}
