package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

const (
	embeddedRuntimeProtocolVersion = "v1"
	embeddedRuntimeStatusPath      = "/v1/runtime/status"
	embeddedRuntimeStartPath       = "/v1/runtime/operations/start"
	embeddedRuntimeStopPath        = "/v1/runtime/operations/stop"
	embeddedRuntimeRestartPath     = "/v1/runtime/operations/restart"
	embeddedRuntimeRequestTimeout  = 30 * time.Second
	embeddedRuntimeMaxResponseBody = 64 << 10
)

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
	baseURL     string
	tokenSource RuntimeTokenSource
	httpClient  *http.Client
}

func NewEmbeddedClient(baseURL string, token string) *EmbeddedClient {
	return NewEmbeddedClientWithTokenSource(baseURL, staticRuntimeTokenSource(token))
}

func NewEmbeddedClientWithTokenSource(baseURL string, tokenSource RuntimeTokenSource) *EmbeddedClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &EmbeddedClient{
		baseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		tokenSource: tokenSource,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   embeddedRuntimeRequestTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

type embeddedStatusResponse struct {
	ProtocolVersion    string                    `json:"protocolVersion"`
	RuntimeIdentity    string                    `json:"runtimeIdentity"`
	RuntimeGeneration  uint64                    `json:"runtimeGeneration"`
	State              string                    `json:"state"`
	CPAObservedVersion string                    `json:"cpaObservedVersion"`
	Capabilities       []string                  `json:"capabilities"`
	Recovery           *embeddedRecoveryResponse `json:"recovery"`
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
}

type embeddedOperationResponse struct {
	OperationID       string                 `json:"operationId"`
	OperationType     string                 `json:"operationType"`
	RuntimeIdentity   string                 `json:"runtimeIdentity"`
	RuntimeGeneration uint64                 `json:"runtimeGeneration"`
	State             string                 `json:"state"`
	Error             *embeddedProtocolError `json:"error,omitempty"`
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

func (c *EmbeddedClient) mutate(
	ctx context.Context,
	requestPath string,
	operationType model.RuntimeOperationType,
	request model.RuntimeMutationRequest,
) (model.RuntimeOperationResult, error) {
	if err := request.Validate(); err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("validate embedded Runtime %s request: %w", operationType, err)
	}
	payload, err := json.Marshal(embeddedMutationRequest{
		OperationID:               request.OperationID,
		ExpectedRuntimeIdentity:   string(request.ExpectedRuntimeIdentity),
		ExpectedRuntimeGeneration: uint64(request.ExpectedRuntimeGeneration),
	})
	if err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("encode embedded Runtime %s request: %w", operationType, err)
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

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return model.RuntimeOperationResult{}, fmt.Errorf("submit embedded Runtime %s: %w", operationType, err)
	}
	defer response.Body.Close()
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
	if result.OperationID != request.OperationID || result.OperationType != operationType ||
		result.RuntimeIdentity != request.ExpectedRuntimeIdentity {
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
