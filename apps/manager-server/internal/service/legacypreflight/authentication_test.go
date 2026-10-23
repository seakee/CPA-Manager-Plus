package legacypreflight

import (
	"context"
	"errors"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestAuthenticationRetainsReadOnlyFenceAndNoUpgradeGrant(t *testing.T) {
	paths, _, _ := upgradeFixture(t, "v1.14.4")
	for _, key := range []string{"", "CPA-key", upgradeTestKey} {
		called := false
		WithAuthenticatedInspection(context.Background(), paths, key, func(report Report, err error) {
			called = true
			if report.UpgradeAuthorized {
				t.Fatal("authentication granted migration")
			}
			if key == upgradeTestKey {
				if err != nil || report.SourceKind != "legacy-manager" {
					t.Fatalf("%+v %v", report, err)
				}
			} else if !errors.Is(err, sqlite.ErrLegacyAdminUnauthorized) {
				t.Fatalf("wrong key accepted: %v", err)
			}
		})
		if !called {
			t.Fatal("missing observation callback")
		}
	}
}
