package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

const (
	embeddedRuntimeProtocolVersion         = "v1"
	embeddedRuntimeFeaturesHeader          = "X-CPAMP-Runtime-Features"
	embeddedArtifactFeature                = "active-gateway-artifact-v1"
	embeddedRuntimeStatusPath              = "/v1/runtime/status"
	embeddedRuntimeStartPath               = "/v1/runtime/operations/start"
	embeddedRuntimeStopPath                = "/v1/runtime/operations/stop"
	embeddedRuntimeRestartPath             = "/v1/runtime/operations/restart"
	embeddedRuntimePrepareUpdatePath       = "/v1/runtime/operations/prepare-update"
	embeddedRuntimePrepareUpdateUploadPath = "/v1/runtime/operations/prepare-update/upload"
	embeddedRuntimeActivateUpdatePath      = "/v1/runtime/operations/activate-update"
	embeddedRuntimeObserveUpdatePath       = "/v1/runtime/operations/update"
	embeddedRuntimeRequestTimeout          = 30 * time.Second
	// Update mutations have dedicated request budgets so status and ordinary
	// lifecycle calls retain their short timeout semantics. Each remains above
	// its Supervisor route-specific response budget.
	embeddedRuntimePrepareUpdateTimeout  = 13 * time.Minute
	embeddedRuntimeActivateUpdateTimeout = 3 * time.Minute
	embeddedRuntimeMaxResponseBody       = 64 << 10
)

// ErrInvalidRequest indicates local validation failed before invoking embedded Runtime transport.
var ErrInvalidRequest = errors.New("invalid embedded Runtime request")

type RuntimeTokenSource interface {
	Token(context.Context) (string, error)
}

type staticRuntimeTokenSource string

func (s staticRuntimeTokenSource) Token(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return string(s), nil
}

type FileRuntimeTokenSource struct {
	path string
}

func NewFileRuntimeTokenSource(path string) *FileRuntimeTokenSource {
	return &FileRuntimeTokenSource{path: strings.TrimSpace(path)}
}

func (s *FileRuntimeTokenSource) Token(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return "", fmt.Errorf("read Runtime transport token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("Runtime transport token file is empty")
	}
	return token, nil
}

// EmbeddedClient observes CPA through the authenticated Runtime Supervisor
// protocol.
type EmbeddedClient struct {
	baseURL            string
	tokenSource        RuntimeTokenSource
	httpClient         *http.Client
	prepareHTTPClient  *http.Client
	activateHTTPClient *http.Client
}

func NewEmbeddedClient(baseURL string, token string) *EmbeddedClient {
	return NewEmbeddedClientWithTokenSource(baseURL, staticRuntimeTokenSource(token))
}

func NewEmbeddedClientWithTokenSource(baseURL string, tokenSource RuntimeTokenSource) *EmbeddedClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &EmbeddedClient{
		baseURL:            strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		tokenSource:        tokenSource,
		httpClient:         newRuntimeHTTPClient(transport, embeddedRuntimeRequestTimeout),
		prepareHTTPClient:  newRuntimeHTTPClient(transport, embeddedRuntimePrepareUpdateTimeout),
		activateHTTPClient: newRuntimeHTTPClient(transport, embeddedRuntimeActivateUpdateTimeout),
	}
}

func newRuntimeHTTPClient(transport http.RoundTripper, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type embeddedStatusResponse struct {
	ProtocolVersion       string                         `json:"protocolVersion"`
	RuntimeIdentity       string                         `json:"runtimeIdentity"`
	RuntimeGeneration     uint64                         `json:"runtimeGeneration"`
	State                 string                         `json:"state"`
	CPAObservedVersion    string                         `json:"cpaObservedVersion"`
	ActiveGatewayArtifact *embeddedActiveGatewayArtifact `json:"activeGatewayArtifact"`
	Capabilities          []string                       `json:"capabilities"`
	Recovery              *embeddedRecoveryResponse      `json:"recovery"`
}

type embeddedActiveGatewayArtifact struct {
	Engine     string `json:"engine"`
	ArtifactID string `json:"artifactId"`
	Version    string `json:"version"`
}

type embeddedRecoveryResponse struct {
	State             string `json:"state"`
	AttemptsRemaining int    `json:"attemptsRemaining"`
}

func (c *EmbeddedClient) Status(ctx context.Context) (model.RuntimeObservedStatus, error) {
	token, err := c.runtimeToken(ctx)
	if err != nil {
		return model.RuntimeObservedStatus{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+embeddedRuntimeStatusPath, nil)
	if err != nil {
		return model.RuntimeObservedStatus{}, fmt.Errorf("create embedded Runtime status request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(embeddedRuntimeFeaturesHeader, embeddedArtifactFeature)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return model.RuntimeObservedStatus{}, fmt.Errorf("observe embedded Runtime status: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		if protocolErr := decodeProtocolError(res); protocolErr != nil {
			return model.RuntimeObservedStatus{}, protocolErr
		}
		return model.RuntimeObservedStatus{}, fmt.Errorf("observe embedded Runtime status: unexpected HTTP status %s", res.Status)
	}

	var response embeddedStatusResponse
	if err := decodeStrictJSON(res.Body, &response); err != nil {
		return model.RuntimeObservedStatus{}, fmt.Errorf("decode embedded Runtime status: %w", err)
	}
	if response.ProtocolVersion != embeddedRuntimeProtocolVersion {
		return model.RuntimeObservedStatus{}, fmt.Errorf("observe embedded Runtime status: unsupported protocol version %q", response.ProtocolVersion)
	}

	capabilities := make(model.RuntimeCapabilities, len(response.Capabilities))
	for i, capability := range response.Capabilities {
		capabilities[i] = model.RuntimeCapability(capability)
	}
	status := model.RuntimeObservedStatus{
		Identity:           model.RuntimeIdentity(response.RuntimeIdentity),
		Generation:         model.RuntimeGeneration(response.RuntimeGeneration),
		ProtocolVersion:    model.RuntimeProtocolVersion(response.ProtocolVersion),
		State:              model.RuntimeState(response.State),
		CPAObservedVersion: model.CPAObservedVersion(response.CPAObservedVersion),
		Capabilities:       capabilities,
	}
	if response.ActiveGatewayArtifact != nil {
		status.ActiveGatewayArtifact = &model.ActiveGatewayArtifact{
			Engine:     response.ActiveGatewayArtifact.Engine,
			ArtifactID: model.RuntimeArtifactID(response.ActiveGatewayArtifact.ArtifactID),
			Version:    response.ActiveGatewayArtifact.Version,
		}
	}
	if response.Recovery != nil {
		status.Recovery = &model.RuntimeRecoveryObservation{
			State:             model.RuntimeRecoveryState(response.Recovery.State),
			AttemptsRemaining: response.Recovery.AttemptsRemaining,
		}
	}
	if err := status.Validate(); err != nil {
		return model.RuntimeObservedStatus{}, fmt.Errorf("validate embedded Runtime status: %w", err)
	}
	return status, nil
}

type embeddedMutationRequest struct {
	OperationID               string `json:"operationId"`
	ExpectedRuntimeIdentity   string `json:"expectedRuntimeIdentity"`
	ExpectedRuntimeGeneration uint64 `json:"expectedRuntimeGeneration"`
	ExpectedActiveArtifactID  string `json:"expectedActiveArtifactId,omitempty"`
}

type embeddedPrepareUpdateRequest struct {
	OperationID               string `json:"operationId"`
	ExpectedRuntimeIdentity   string `json:"expectedRuntimeIdentity"`
	ExpectedRuntimeGeneration uint64 `json:"expectedRuntimeGeneration"`
	ExpectedActiveArtifactID  string `json:"expectedActiveArtifactId"`
	TargetVersion             string `json:"targetVersion"`
	SourceMode                string `json:"sourceMode,omitempty"`
	CDNPrefix                 string `json:"cdnPrefix,omitempty"`
}

type embeddedActivateUpdateRequest struct {
	OperationID               string `json:"operationId"`
	ExpectedRuntimeIdentity   string `json:"expectedRuntimeIdentity"`
	ExpectedRuntimeGeneration uint64 `json:"expectedRuntimeGeneration"`
	ExpectedActiveArtifactID  string `json:"expectedActiveArtifactId"`
	TargetVersion             string `json:"targetVersion"`
}

type embeddedOperationResponse struct {
	OperationID       string                 `json:"operationId"`
	OperationType     string                 `json:"operationType"`
	RuntimeIdentity   string                 `json:"runtimeIdentity"`
	RuntimeGeneration uint64                 `json:"runtimeGeneration"`
	State             string                 `json:"state"`
	Error             *embeddedProtocolError `json:"error,omitempty"`
}

type embeddedObservedOperationResponse struct {
	OperationID       string                 `json:"operationId"`
	OperationType     string                 `json:"operationType"`
	RuntimeIdentity   string                 `json:"runtimeIdentity"`
	RuntimeGeneration uint64                 `json:"runtimeGeneration"`
	State             string                 `json:"state"`
	Phase             string                 `json:"phase,omitempty"`
	ReceivedBytes     int64                  `json:"receivedBytes,omitempty"`
	TotalBytes        int64                  `json:"totalBytes,omitempty"`
	Error             *embeddedProtocolError `json:"error,omitempty"`
	CreatedAt         time.Time              `json:"createdAt"`
	UpdatedAt         time.Time              `json:"updatedAt"`
	CompletedAt       *time.Time             `json:"completedAt,omitempty"`
}

type embeddedErrorEnvelope struct {
	Error embeddedProtocolError `json:"error"`
}

type embeddedProtocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *EmbeddedClient) Start(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	return c.mutate(ctx, embeddedRuntimeStartPath, model.RuntimeOperationStart, request)
}

func (c *EmbeddedClient) Stop(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	return c.mutate(ctx, embeddedRuntimeStopPath, model.RuntimeOperationStop, request)
}

func (c *EmbeddedClient) Restart(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	return c.mutate(ctx, embeddedRuntimeRestartPath, model.RuntimeOperationRestart, request)
}

func (c *EmbeddedClient) PrepareUpdate(ctx context.Context, request model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
	if err := request.Validate(); err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("validate embedded Runtime %s request: %w", model.RuntimeOperationPrepareUpdate, err)
	}
	payload, err := json.Marshal(embeddedPrepareUpdateRequest{
		OperationID:               request.OperationID,
		ExpectedRuntimeIdentity:   string(request.ExpectedRuntimeIdentity),
		ExpectedRuntimeGeneration: uint64(request.ExpectedRuntimeGeneration),
		ExpectedActiveArtifactID:  string(request.ExpectedActiveArtifactID),
		TargetVersion:             request.TargetVersion,
		SourceMode:                request.SourceMode,
		CDNPrefix:                 request.CDNPrefix,
	})
	if err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("encode embedded Runtime %s request: %w", model.RuntimeOperationPrepareUpdate, err)
	}
	return c.submitMutationWithClient(
		c.prepareHTTPClient,
		ctx,
		embeddedRuntimePrepareUpdatePath,
		model.RuntimeOperationPrepareUpdate,
		request.OperationID,
		request.ExpectedRuntimeIdentity,
		payload,
	)
}

func (c *EmbeddedClient) PrepareUpdateUpload(
	ctx context.Context,
	request model.RuntimePrepareUpdateRequest,
	archive io.Reader,
	contentLength int64,
) (model.RuntimeOperationResult, error) {
	if archive == nil {
		return model.RuntimeOperationResult{}, fmt.Errorf(
			"%w: uploaded archive stream is required: %w",
			ErrInvalidRequest,
			&ProtocolError{Code: ProtocolErrorInvalidRequest, HTTPStatus: http.StatusBadRequest},
		)
	}
	if contentLength == 0 || contentLength < -1 {
		return model.RuntimeOperationResult{}, fmt.Errorf(
			"%w: invalid upload content length %d: %w",
			ErrInvalidRequest,
			contentLength,
			&ProtocolError{Code: ProtocolErrorInvalidRequest, HTTPStatus: http.StatusBadRequest},
		)
	}
	if err := request.Validate(); err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("validate embedded Runtime %s upload request: %w", model.RuntimeOperationPrepareUpdate, err)
	}
	if c.prepareHTTPClient == nil {
		return model.RuntimeOperationResult{}, errors.New("embedded Runtime HTTP client is unavailable")
	}
	token, err := c.runtimeToken(ctx)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	endpoint, err := url.Parse(c.baseURL + embeddedRuntimePrepareUpdateUploadPath)
	if err != nil {
		return model.RuntimeOperationResult{}, errors.New("embedded Runtime prepare-update upload URL is invalid")
	}
	query := endpoint.Query()
	query.Set("operationId", request.OperationID)
	query.Set("expectedRuntimeIdentity", string(request.ExpectedRuntimeIdentity))
	query.Set("expectedRuntimeGeneration", fmt.Sprintf("%d", request.ExpectedRuntimeGeneration))
	query.Set("expectedActiveArtifactId", string(request.ExpectedActiveArtifactID))
	query.Set("targetVersion", request.TargetVersion)
	endpoint.RawQuery = query.Encode()

	uploadCtx, cancel := context.WithCancel(ctx)
	pipeReader, pipeWriter := io.Pipe()
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		buf := make([]byte, 32*1024)
		for {
			if err := uploadCtx.Err(); err != nil {
				_ = pipeWriter.CloseWithError(err)
				return
			}
			n, readErr := archive.Read(buf)
			if n > 0 {
				if _, writeErr := pipeWriter.Write(buf[:n]); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					_ = pipeWriter.Close()
				} else {
					_ = pipeWriter.CloseWithError(readErr)
				}
				return
			}
		}
	}()
	var cleanupOnce sync.Once
	cleanupUpload := func() {
		cleanupOnce.Do(func() {
			cancel()
			_ = pipeReader.CloseWithError(context.Canceled)
			if closer, ok := archive.(io.Closer); ok {
				_ = closer.Close()
			}
			if interruptible, ok := archive.(interface{ Interruptible() bool }); ok && interruptible.Interruptible() {
				<-pumpDone
				return
			}
			select {
			case <-pumpDone:
			default:
			}
		})
	}
	defer cleanupUpload()

	httpRequest, err := http.NewRequestWithContext(uploadCtx, http.MethodPost, endpoint.String(), pipeReader)
	if err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("create embedded Runtime %s upload request: %w", model.RuntimeOperationPrepareUpdate, err)
	}
	httpRequest.ContentLength = contentLength
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/octet-stream")
	httpRequest.Header.Set("Authorization", "Bearer "+token)

	response, err := c.prepareHTTPClient.Do(httpRequest)
	if err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("submit embedded Runtime %s upload: %w", model.RuntimeOperationPrepareUpdate, err)
	}
	result, decodeErr := decodeMutationResponse(response, model.RuntimeOperationPrepareUpdate, request.OperationID, request.ExpectedRuntimeIdentity)
	_ = response.Body.Close()
	cleanupUpload()
	return result, decodeErr
}

func (c *EmbeddedClient) ActivateUpdate(ctx context.Context, request model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
	if err := request.Validate(); err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("validate embedded Runtime %s request: %w", model.RuntimeOperationActivateUpdate, err)
	}
	payload, err := json.Marshal(embeddedActivateUpdateRequest{
		OperationID:               request.OperationID,
		ExpectedRuntimeIdentity:   string(request.ExpectedRuntimeIdentity),
		ExpectedRuntimeGeneration: uint64(request.ExpectedRuntimeGeneration),
		ExpectedActiveArtifactID:  string(request.ExpectedActiveArtifactID),
		TargetVersion:             request.TargetVersion,
	})
	if err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("encode embedded Runtime %s request: %w", model.RuntimeOperationActivateUpdate, err)
	}
	return c.submitMutationWithClient(
		c.activateHTTPClient,
		ctx,
		embeddedRuntimeActivateUpdatePath,
		model.RuntimeOperationActivateUpdate,
		request.OperationID,
		request.ExpectedRuntimeIdentity,
		payload,
	)
}

func (c *EmbeddedClient) ObserveUpdateOperation(ctx context.Context, request model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
	if err := request.Validate(); err != nil {
		return model.RuntimeUpdateOperationObservation{}, fmt.Errorf("validate embedded Runtime update operation observation: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return model.RuntimeUpdateOperationObservation{}, fmt.Errorf("observe embedded Runtime update operation: %w", err)
	}
	token, err := c.runtimeToken(ctx)
	if err != nil {
		return model.RuntimeUpdateOperationObservation{}, errors.New("embedded Runtime transport token is unavailable")
	}
	endpoint, err := url.Parse(c.baseURL + embeddedRuntimeObserveUpdatePath)
	if err != nil {
		return model.RuntimeUpdateOperationObservation{}, errors.New("embedded Runtime update observation URL is invalid")
	}
	query := endpoint.Query()
	query.Set("operationId", request.OperationID)
	query.Set("expectedRuntimeIdentity", string(request.ExpectedRuntimeIdentity))
	query.Set("expectedRuntimeGeneration", fmt.Sprintf("%d", request.ExpectedRuntimeGeneration))
	query.Set("operationType", string(request.OperationType))
	query.Set("targetVersion", request.TargetVersion)
	endpoint.RawQuery = query.Encode()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return model.RuntimeUpdateOperationObservation{}, errors.New("embedded Runtime update observation request is invalid")
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	httpRequest.Header.Set("Cache-Control", "no-store")
	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return model.RuntimeUpdateOperationObservation{}, fmt.Errorf("observe embedded Runtime update operation: %w", ctx.Err())
		}
		return model.RuntimeUpdateOperationObservation{}, errors.New("embedded Runtime update observation transport is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if protocolErr := decodeProtocolError(response); protocolErr != nil {
			code, _ := ErrorCode(protocolErr)
			switch code {
			case ProtocolErrorInvalidRequest, ProtocolErrorUnsupportedOperation, ProtocolErrorOperationNotFound,
				ProtocolErrorRuntimeIdentityMismatch, ProtocolErrorStaleRuntimeGeneration, ProtocolErrorOperationIDConflict,
				ProtocolErrorOperationPersistenceUnavailable, ProtocolErrorInternal:
				return model.RuntimeUpdateOperationObservation{}, protocolErr
			}
		}
		return model.RuntimeUpdateOperationObservation{}, fmt.Errorf(
			"observe embedded Runtime update operation: unexpected HTTP status %d",
			response.StatusCode,
		)
	}
	var wire embeddedObservedOperationResponse
	if err := decodeStrictJSON(response.Body, &wire); err != nil {
		return model.RuntimeUpdateOperationObservation{}, errors.New("embedded Runtime update operation response is invalid")
	}
	result := model.RuntimeUpdateOperationObservation{
		OperationID:       wire.OperationID,
		OperationType:     model.RuntimeOperationType(wire.OperationType),
		RuntimeIdentity:   model.RuntimeIdentity(wire.RuntimeIdentity),
		RuntimeGeneration: model.RuntimeGeneration(wire.RuntimeGeneration),
		State:             model.RuntimeOperationState(wire.State),
		Phase:             wire.Phase,
		ReceivedBytes:     wire.ReceivedBytes,
		TotalBytes:        wire.TotalBytes,
		CreatedAt:         wire.CreatedAt,
		UpdatedAt:         wire.UpdatedAt,
		CompletedAt:       wire.CompletedAt,
	}
	if wire.Error != nil {
		if result.State != model.RuntimeOperationFailed {
			return model.RuntimeUpdateOperationObservation{}, errors.New("embedded Runtime update operation has inconsistent failure evidence")
		}
		result.FailureCode = wire.Error.Code
	}
	if err := result.Validate(); err != nil {
		return model.RuntimeUpdateOperationObservation{}, errors.New("embedded Runtime update operation evidence is invalid")
	}
	if result.OperationID != request.OperationID || result.OperationType != request.OperationType ||
		result.RuntimeIdentity != request.ExpectedRuntimeIdentity {
		return model.RuntimeUpdateOperationObservation{}, errors.New("validate embedded Runtime update operation observation: response does not match request")
	}
	return result, nil
}

func (c *EmbeddedClient) mutate(
	ctx context.Context,
	requestPath string,
	operationType model.RuntimeOperationType,
	request model.RuntimeMutationRequest,
) (model.RuntimeOperationResult, error) {
	if operationType != model.RuntimeOperationStart && request.ExpectedActiveArtifactID != "" {
		return model.RuntimeOperationResult{}, errors.New("active artifact fencing is only supported for Start")
	}
	if err := request.Validate(); err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("validate embedded Runtime %s request: %w", operationType, err)
	}
	payload, err := json.Marshal(embeddedMutationRequest{
		OperationID:               request.OperationID,
		ExpectedRuntimeIdentity:   string(request.ExpectedRuntimeIdentity),
		ExpectedRuntimeGeneration: uint64(request.ExpectedRuntimeGeneration),
		ExpectedActiveArtifactID:  string(request.ExpectedActiveArtifactID),
	})
	if err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("encode embedded Runtime %s request: %w", operationType, err)
	}
	return c.submitMutation(ctx, requestPath, operationType, request.OperationID, request.ExpectedRuntimeIdentity, payload)
}

func (c *EmbeddedClient) submitMutation(
	ctx context.Context,
	requestPath string,
	operationType model.RuntimeOperationType,
	operationID string,
	expectedRuntimeIdentity model.RuntimeIdentity,
	payload []byte,
) (model.RuntimeOperationResult, error) {
	return c.submitMutationWithClient(c.httpClient, ctx, requestPath, operationType, operationID, expectedRuntimeIdentity, payload)
}

func (c *EmbeddedClient) submitMutationWithClient(
	client *http.Client,
	ctx context.Context,
	requestPath string,
	operationType model.RuntimeOperationType,
	operationID string,
	expectedRuntimeIdentity model.RuntimeIdentity,
	payload []byte,
) (model.RuntimeOperationResult, error) {
	if client == nil {
		return model.RuntimeOperationResult{}, errors.New("embedded Runtime HTTP client is unavailable")
	}
	token, err := c.runtimeToken(ctx)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+requestPath, bytes.NewReader(payload))
	if err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("create embedded Runtime %s request: %w", operationType, err)
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+token)

	response, err := client.Do(httpRequest)
	if err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("submit embedded Runtime %s: %w", operationType, err)
	}
	defer response.Body.Close()
	return decodeMutationResponse(response, operationType, operationID, expectedRuntimeIdentity)
}

func decodeMutationResponse(
	response *http.Response,
	operationType model.RuntimeOperationType,
	operationID string,
	expectedRuntimeIdentity model.RuntimeIdentity,
) (model.RuntimeOperationResult, error) {
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if protocolErr := decodeProtocolError(response); protocolErr != nil {
			return model.RuntimeOperationResult{}, protocolErr
		}
		return model.RuntimeOperationResult{}, fmt.Errorf(
			"submit embedded Runtime %s: unexpected HTTP status %s",
			operationType,
			response.Status,
		)
	}
	var wire embeddedOperationResponse
	if err := decodeStrictJSON(response.Body, &wire); err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("decode embedded Runtime %s result: %w", operationType, err)
	}
	result := model.RuntimeOperationResult{
		OperationID:       wire.OperationID,
		OperationType:     model.RuntimeOperationType(wire.OperationType),
		RuntimeIdentity:   model.RuntimeIdentity(wire.RuntimeIdentity),
		RuntimeGeneration: model.RuntimeGeneration(wire.RuntimeGeneration),
		State:             model.RuntimeOperationState(wire.State),
	}
	if wire.Error != nil {
		result.Failure = &model.RuntimeOperationFailure{Code: wire.Error.Code, Message: wire.Error.Message}
	}
	if err := result.Validate(); err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("validate embedded Runtime %s result: %w", operationType, err)
	}
	if result.OperationID != operationID || result.OperationType != operationType ||
		result.RuntimeIdentity != expectedRuntimeIdentity {
		return model.RuntimeOperationResult{}, fmt.Errorf("validate embedded Runtime %s result: response does not match request", operationType)
	}
	return result, nil
}

func (c *EmbeddedClient) runtimeToken(ctx context.Context) (string, error) {
	if c.tokenSource == nil {
		return "", errors.New("Runtime transport token source is unavailable")
	}
	token, err := c.tokenSource.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("load Runtime transport token: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("Runtime transport token is empty")
	}
	return token, nil
}

func decodeProtocolError(response *http.Response) error {
	var envelope embeddedErrorEnvelope
	if err := decodeStrictJSON(response.Body, &envelope); err != nil || strings.TrimSpace(envelope.Error.Code) == "" {
		return nil
	}
	return &ProtocolError{Code: ProtocolErrorCode(envelope.Error.Code), HTTPStatus: response.StatusCode}
}

func decodeStrictJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, embeddedRuntimeMaxResponseBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

var _ RuntimeClient = (*EmbeddedClient)(nil)
