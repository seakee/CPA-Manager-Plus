// Package runtime defines the Manager application boundary for observing and
// submitting typed lifecycle operations to a CPA Runtime.
package runtime

import (
	"context"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

// RuntimeClient reports observed Runtime state without exposing an adapter or
// transport to application code. Status returns an error when the adapter
// cannot obtain reliable observed state. RuntimeStateOffline is reserved for
// authoritative observation that CPA is unavailable; transport and
// authentication failures are errors.
type RuntimeClient interface {
	Status(ctx context.Context) (model.RuntimeObservedStatus, error)
	Start(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error)
	Stop(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error)
	Restart(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error)
}
