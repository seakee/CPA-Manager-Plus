package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// ExternalClient observes a separately managed CPA through its Management API.
type ExternalClient struct {
	baseURL string
	key     string
}

func NewExternalClient(baseURL string, key string) *ExternalClient {
	return &ExternalClient{baseURL: baseURL, key: key}
}

func (c *ExternalClient) Status(ctx context.Context) (model.RuntimeObservedStatus, error) {
	version, err := cpa.ObserveManagementAPI(ctx, c.baseURL, c.key)
	if err != nil {
		return model.RuntimeObservedStatus{}, fmt.Errorf("observe external CPA: %w", err)
	}
	version = strings.TrimSpace(version)
	if version == "" {
		return model.RuntimeObservedStatus{}, errors.New("observe external CPA: CPA version header is missing")
	}
	status := model.RuntimeObservedStatus{
		State:              model.RuntimeStateReady,
		CPAObservedVersion: model.CPAObservedVersion(version),
	}
	if err := status.Validate(); err != nil {
		return model.RuntimeObservedStatus{}, fmt.Errorf("validate external CPA status: %w", err)
	}
	return status, nil
}

func (c *ExternalClient) Start(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, fmt.Errorf("start external Runtime: %w", ErrRuntimeMutationUnsupported)
}

func (c *ExternalClient) Stop(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, fmt.Errorf("stop external Runtime: %w", ErrRuntimeMutationUnsupported)
}

func (c *ExternalClient) Restart(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, fmt.Errorf("restart external Runtime: %w", ErrRuntimeMutationUnsupported)
}

var _ RuntimeClient = (*ExternalClient)(nil)
