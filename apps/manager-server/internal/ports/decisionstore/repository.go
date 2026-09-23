package decisionstore

import (
	"context"
	"errors"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/gatewaydecision"
)

var (
	ErrNotFound       = errors.New("quota decision event not found")
	ErrDedupeConflict = errors.New("quota decision dedupe key conflicts with the first event")
)

// Repository appends immutable audit events. Idempotent Append does not imply
// exactly-once upstream delivery.
type Repository interface {
	Append(context.Context, gatewaydecision.QuotaDecisionEvent) (gatewaydecision.QuotaDecisionEvent, bool, error)
	LoadByID(context.Context, gatewaydecision.DecisionID) (gatewaydecision.QuotaDecisionEvent, error)
}
