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
	connectionSource ExternalConnectionSource
}

// ExternalConnectionSource resolves the current Manager-owned CPA connection.
// Status calls it for every observation so setup and connection changes take
// effect without restarting Manager Server.
type ExternalConnectionSource func(context.Context) (baseURL string, managementKey string, err error)

func NewExternalClient(baseURL string, key string) *ExternalClient {
	return NewExternalClientWithConnectionSource(func(context.Context) (string, string, error) {
		return baseURL, key, nil
	})
}

func NewExternalClientWithConnectionSource(source ExternalConnectionSource) *ExternalClient {
	return &ExternalClient{connectionSource: source}
}

func (c *ExternalClient) Status(ctx context.Context) (model.RuntimeObservedStatus, error) {
	if c == nil || c.connectionSource == nil {
		return model.RuntimeObservedStatus{}, errors.New("observe external CPA: connection source is unavailable")
	}
	baseURL, managementKey, err := c.connectionSource(ctx)
	if err != nil {
		return model.RuntimeObservedStatus{}, fmt.Errorf("resolve external CPA connection: %w", err)
	}
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(managementKey) == "" {
		return model.RuntimeObservedStatus{}, errors.New("observe external CPA: CPA connection is not configured")
	}
	version, err := cpa.ObserveManagementAPI(ctx, baseURL, managementKey)
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

func (c *ExternalClient) PrepareUpdate(context.Context, model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, fmt.Errorf("prepare-update external Runtime: %w", ErrRuntimeMutationUnsupported)
}

func (c *ExternalClient) ActivateUpdate(context.Context, model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, fmt.Errorf("activate-update external Runtime: %w", ErrRuntimeMutationUnsupported)
}

func (c *ExternalClient) ObserveUpdateOperation(context.Context, model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
	return model.RuntimeUpdateOperationObservation{}, fmt.Errorf("observe update operation for external Runtime: %w", ErrRuntimeObservationUnsupported)
}

var _ RuntimeClient = (*ExternalClient)(nil)
