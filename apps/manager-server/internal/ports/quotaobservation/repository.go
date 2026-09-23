package quotaobservation

import (
	"context"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/resourcepolicy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityprojection"
)

// Source contains only immutable provenance needed by the shadow evaluator.
type Source struct {
	ID          int64
	EventHash   string
	TimestampMS int64
}

type ProjectionState struct {
	SchemaVersion        int
	Status               string
	LastProcessedEventID int64
	BindingRevision      int64
}

type Observation struct {
	Count           int64
	TokenSum        *int64
	TokenIncomplete bool
	Overflow        bool
}

// View's methods all read the same SQLite snapshot. The callback ends before
// any decision append, so retries never hold a read transaction while writing.
type View interface {
	Source(context.Context, int64) (Source, error)
	State(context.Context) (ProjectionState, error)
	Projection(context.Context, int64) (*identityprojection.UsageIdentityProjection, error)
	Binding(context.Context, identity.APIKeyID) (*resourcepolicy.PolicyBinding, error)
	Policy(context.Context, resourcepolicy.PolicyID) (resourcepolicy.QuotaPolicy, error)
	Observe(context.Context, identity.APIKeyID, resourcepolicy.Metric, int64, int64, int64) (Observation, error)
}

type Repository interface {
	WithSnapshot(context.Context, func(View) error) error
}
