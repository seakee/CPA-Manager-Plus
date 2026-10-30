package legacypreflight

import (
	"context"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

// WithAuthenticatedInspection verifies the persisted Manager credential on the
// same bounded, read-only source observation used by discovery, except that the
// ownership marker is only probed. Proving a credential must also work on a
// source tree mounted read-only, where the marker can be read but never opened
// for writing, and an authentication leaf excludes no writer: it observes the
// source and writes nothing. This is identity evidence only: it grants neither
// startup admission nor UpgradeAuthorized.
func WithAuthenticatedInspection(ctx context.Context, paths sqlite.InspectionPaths, key string, consume func(Report, error)) {
	if strings.TrimSpace(key) == "" {
		consume(Report{}, sqlite.ErrLegacyAdminUnauthorized)
		return
	}
	WithFencedInspection(ctx, paths, Options{adminKey: key, observeOnly: true}, consume)
}
