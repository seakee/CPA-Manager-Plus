package runtime

import (
	"context"
	"encoding/json"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

// DestinationClient is the optional typed capability of the same RuntimeClient.
// External clients cannot stage a Manager-owned process or filesystem resource.
type DestinationClient interface {
	StageDestination(context.Context, model.RuntimeDestinationRequest) (model.RuntimeOperationResult, error)
}

func (c *EmbeddedClient) StageDestination(ctx context.Context, r model.RuntimeDestinationRequest) (model.RuntimeOperationResult, error) {
	if err := r.Validate(); err != nil {
		return model.RuntimeOperationResult{}, err
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	return c.submitMutation(ctx, "/v1/runtime/operations/stage-destination", model.RuntimeOperationStageDestination, r.OperationID, r.ExpectedRuntimeIdentity, payload)
}
