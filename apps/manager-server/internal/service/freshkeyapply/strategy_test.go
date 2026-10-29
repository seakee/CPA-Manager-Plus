package freshkeyapply

import (
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

// ManagerOwnedStrategy is the single ownership predicate of the install-derived
// authority. It owns exactly the destinations this Fresh lane stages: New Managed,
// and an adoption whose source was read as a network install. `legacy_managed` is
// Upgrade's source class, so it must never reach the authority, and neither may an
// adoption that names no class at all.
func TestManagerOwnedStrategyIsLimitedToTheDestinationsThisLaneStages(t *testing.T) {
	for name, tc := range map[string]struct {
		checkpoint bootstrap.FreshStrategyCheckpoint
		want       bool
	}{
		"new managed":        {bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged}, true},
		"network adoption":   {bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.AdoptExisting, SourceClass: bootstrap.AdoptNetworkExisting}, true},
		"legacy adoption":    {bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.AdoptExisting, SourceClass: bootstrap.AdoptLegacyManaged}, false},
		"classless adoption": {bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.AdoptExisting}, false},
		"external":           {bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.ConnectExternal}, false},
		"unselected":         {bootstrap.FreshStrategyCheckpoint{}, false},
	} {
		if got := ManagerOwnedStrategy(tc.checkpoint); got != tc.want {
			t.Fatalf("%s: ManagerOwnedStrategy = %v, want %v", name, got, tc.want)
		}
	}
	// The prefix a caller picks by strategy follows the same rule, so a legacy
	// adoption cannot be bound to the Manager-owned authority by either half.
	if _, ok := managerOwnedDestinationPrefix(bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.AdoptExisting, SourceClass: bootstrap.AdoptLegacyManaged}); ok {
		t.Fatal("a legacy_managed adoption has a Manager-owned destination prefix")
	}
	if prefix, ok := managerOwnedDestinationPrefix(bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.AdoptExisting, SourceClass: bootstrap.AdoptNetworkExisting}); !ok || prefix != "external:sha256:" {
		t.Fatalf("a network adoption does not carry the external prefix: %q %v", prefix, ok)
	}
}
