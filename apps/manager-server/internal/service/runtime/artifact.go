package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

const (
	artifactStatusPath     = "/v1/runtime/artifact"
	artifactProbePath      = "/v1/runtime/artifact/probe"
	artifactAcquirePath    = "/v1/runtime/artifact/acquire"
	artifactUploadPath     = "/v1/runtime/artifact/upload"
	artifactStatusTimeout  = 5 * time.Second
	artifactProbeTimeout   = 10 * time.Second
	artifactAcquireTimeout = 15 * time.Second
	artifactUploadTimeout  = 15 * time.Minute
)

const (
	ArtifactCodeInvalidRequest        = "invalid_request"
	ArtifactCodeUnauthorized          = "unauthorized"
	ArtifactCodeUnsupportedPlatform   = "unsupported_platform"
	ArtifactCodeAcquisitionInProgress = "acquisition_in_progress"
	ArtifactCodeArtifactConflict      = "artifact_conflict"
	ArtifactCodeArtifactAlreadyActive = "artifact_already_active"
	ArtifactCodeArchiveTooLarge       = "archive_too_large"
	ArtifactCodeArchiveInvalid        = "archive_invalid"
	ArtifactCodeArtifactMismatch      = "artifact_mismatch"
	ArtifactCodeSourceUnreachable     = "source_unreachable"
	ArtifactCodeDownloadFailed        = "download_failed"
	ArtifactCodeInstallFailed         = "install_failed"
	ArtifactCodeRecoveryRequired      = "recovery_required"
)

// KnownArtifactErrorStatus returns the canonical HTTP status for a Supervisor
// artifact error code.
func KnownArtifactErrorStatus(code string) (int, bool) {
	switch code {
	case ArtifactCodeInvalidRequest:
		return http.StatusBadRequest, true
	case ArtifactCodeUnauthorized:
		return http.StatusUnauthorized, true
	case ArtifactCodeUnsupportedPlatform,
		ArtifactCodeAcquisitionInProgress,
		ArtifactCodeArtifactConflict,
		ArtifactCodeArtifactAlreadyActive:
		return http.StatusConflict, true
	case ArtifactCodeArchiveTooLarge:
		return http.StatusRequestEntityTooLarge, true
	case ArtifactCodeArchiveInvalid, ArtifactCodeArtifactMismatch:
		return http.StatusUnprocessableEntity, true
	case ArtifactCodeSourceUnreachable, ArtifactCodeDownloadFailed:
		return http.StatusBadGateway, true
	case ArtifactCodeInstallFailed, ArtifactCodeRecoveryRequired:
		return http.StatusInternalServerError, true
	default:
		return 0, false
	}
}

// ArtifactError preserves the Supervisor artifact endpoint's machine-readable
// error code and HTTP status without echoing untrusted remote messages.
type ArtifactError struct {
	Code       string
	HTTPStatus int
}

func (e *ArtifactError) Error() string {
	return fmt.Sprintf("Runtime artifact error %q (HTTP %d)", e.Code, e.HTTPStatus)
}

func ArtifactErrorCode(err error) (string, int, bool) {
	var artifactErr *ArtifactError
	if !errors.As(err, &artifactErr) || artifactErr == nil {
		return "", 0, false
	}
	return artifactErr.Code, artifactErr.HTTPStatus, true
}

type ArtifactPinned struct {
	Version    string `json:"version"`
	ArtifactID string `json:"artifactId"`
}

func (p ArtifactPinned) Validate() error {
	if strings.TrimSpace(p.Version) == "" {
		return errors.New("pinned artifact version is required")
	}
	if !model.RuntimeArtifactID(p.ArtifactID).IsValid() {
		return fmt.Errorf("invalid pinned artifactId %q", p.ArtifactID)
	}
	return nil
}

type ArtifactActive struct {
	ArtifactID string `json:"artifactId"`
	Origin     string `json:"origin"`
}

func (a ArtifactActive) Validate() error {
	if !model.RuntimeArtifactID(a.ArtifactID).IsValid() {
		return fmt.Errorf("invalid active artifactId %q", a.ArtifactID)
	}
	switch a.Origin {
	case "bundled", "acquired":
		return nil
	default:
		return fmt.Errorf("unsupported active artifact origin %q", a.Origin)
	}
}

type ArtifactOperation struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	Phase         string `json:"phase"`
	ReceivedBytes int64  `json:"receivedBytes"`
	TotalBytes    int64  `json:"totalBytes"`
	Code          string `json:"code"`
}

func (o ArtifactOperation) Validate() error {
	if strings.TrimSpace(o.ID) == "" {
		return errors.New("artifact operation id is required")
	}
	switch o.Source {
	case "github", "cdn", "upload":
	default:
		return fmt.Errorf("unsupported artifact operation source %q", o.Source)
	}
	switch o.Phase {
	case "downloading", "verifying", "installing", "succeeded", "failed":
	default:
		return fmt.Errorf("unsupported artifact operation phase %q", o.Phase)
	}
	if o.ReceivedBytes < 0 || o.TotalBytes < 0 {
		return errors.New("artifact operation byte counters must be non-negative")
	}
	return nil
}

type ArtifactStatus struct {
	SchemaVersion int                `json:"schemaVersion"`
	Pinned        ArtifactPinned     `json:"pinned"`
	Active        *ArtifactActive    `json:"active"`
	Required      bool               `json:"required"`
	Operation     *ArtifactOperation `json:"operation"`
}

func (s ArtifactStatus) Validate() error {
	if s.SchemaVersion != 1 {
		return fmt.Errorf("unsupported artifact status schemaVersion %d", s.SchemaVersion)
	}
	if err := s.Pinned.Validate(); err != nil {
		return err
	}
	if s.Active != nil {
		if err := s.Active.Validate(); err != nil {
			return err
		}
		if s.Required {
			return errors.New("artifact status required must be false when active artifact is present")
		}
	} else if !s.Required {
		return errors.New("artifact status required must be true when active artifact is absent")
	}
	if s.Operation != nil {
		if err := s.Operation.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Ready mirrors the Fresh wizard's isArtifactReady predicate (#1166): the
// pinned engine artifact is active, not required, and not awaiting Supervisor
// recovery.
func (s ArtifactStatus) Ready() bool {
	if s.Validate() != nil || s.Required || s.Active == nil || s.Active.ArtifactID != s.Pinned.ArtifactID {
		return false
	}
	return !(s.Operation != nil && s.Operation.Phase == "failed" && s.Operation.Code == ArtifactCodeRecoveryRequired)
}

type ArtifactProbeResult struct {
	Reachable bool   `json:"reachable"`
	Code      string `json:"code"`
}

func (p ArtifactProbeResult) Validate() error {
	if p.Reachable {
		if p.Code != "" {
			return fmt.Errorf("reachable artifact probe must not report error code %q", p.Code)
		}
		return nil
	}
	if p.Code != "source_unreachable" {
		return fmt.Errorf("unreachable artifact probe must report source_unreachable, got %q", p.Code)
	}
	return nil
}

type ArtifactOperationEnvelope struct {
	Operation  ArtifactOperation `json:"operation"`
	HTTPStatus int               `json:"-"`
}

type artifactAcquireRequest struct {
	Source    string `json:"source"`
	CDNPrefix string `json:"cdnPrefix,omitempty"`
}

type artifactErrorEnvelope struct {
	Code string `json:"code"`
}

// ArtifactClient communicates with the Runtime Supervisor's artifact
// observation and acquisition endpoints over the authenticated local transport.
type ArtifactClient struct {
	baseURL           string
	tokenSource       RuntimeTokenSource
	statusHTTPClient  *http.Client
	probeHTTPClient   *http.Client
	acquireHTTPClient *http.Client
	uploadHTTPClient  *http.Client
}

func NewArtifactClient(baseURL string, token string) *ArtifactClient {
	return NewArtifactClientWithTokenSource(baseURL, staticRuntimeTokenSource(token))
}

func NewArtifactClientWithTokenSource(baseURL string, tokenSource RuntimeTokenSource) *ArtifactClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &ArtifactClient{
		baseURL:           strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		tokenSource:       tokenSource,
		statusHTTPClient:  newRuntimeHTTPClient(transport, artifactStatusTimeout),
		probeHTTPClient:   newRuntimeHTTPClient(transport, artifactProbeTimeout),
		acquireHTTPClient: newRuntimeHTTPClient(transport, artifactAcquireTimeout),
		uploadHTTPClient:  newRuntimeHTTPClient(transport, artifactUploadTimeout),
	}
}

func (c *ArtifactClient) Status(ctx context.Context) (ArtifactStatus, error) {
	token, err := c.runtimeToken(ctx)
	if err != nil {
		return ArtifactStatus{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+artifactStatusPath, nil)
	if err != nil {
		return ArtifactStatus{}, fmt.Errorf("create Runtime artifact status request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := c.statusHTTPClient.Do(req)
	if err != nil {
		return ArtifactStatus{}, fmt.Errorf("observe Runtime artifact status: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		if artifactErr := decodeArtifactError(res); artifactErr != nil {
			return ArtifactStatus{}, artifactErr
		}
		return ArtifactStatus{}, fmt.Errorf("observe Runtime artifact status: unexpected HTTP status %s", res.Status)
	}

	var status ArtifactStatus
	if err := decodeStrictJSON(res.Body, &status); err != nil {
		return ArtifactStatus{}, fmt.Errorf("decode Runtime artifact status: %w", err)
	}
	if err := status.Validate(); err != nil {
		return ArtifactStatus{}, fmt.Errorf("validate Runtime artifact status: %w", err)
	}
	return status, nil
}

func (c *ArtifactClient) Probe(ctx context.Context) (ArtifactProbeResult, error) {
	token, err := c.runtimeToken(ctx)
	if err != nil {
		return ArtifactProbeResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+artifactProbePath, bytes.NewReader([]byte("{}")))
	if err != nil {
		return ArtifactProbeResult{}, fmt.Errorf("create Runtime artifact probe request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := c.probeHTTPClient.Do(req)
	if err != nil {
		return ArtifactProbeResult{}, fmt.Errorf("probe Runtime artifact source: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		if artifactErr := decodeArtifactError(res); artifactErr != nil {
			return ArtifactProbeResult{}, artifactErr
		}
		return ArtifactProbeResult{}, fmt.Errorf("probe Runtime artifact source: unexpected HTTP status %s", res.Status)
	}

	var result ArtifactProbeResult
	if err := decodeStrictJSON(res.Body, &result); err != nil {
		return ArtifactProbeResult{}, fmt.Errorf("decode Runtime artifact probe: %w", err)
	}
	if err := result.Validate(); err != nil {
		return ArtifactProbeResult{}, fmt.Errorf("validate Runtime artifact probe: %w", err)
	}
	return result, nil
}

func (c *ArtifactClient) Acquire(ctx context.Context, source, cdnPrefix string) (ArtifactOperationEnvelope, error) {
	token, err := c.runtimeToken(ctx)
	if err != nil {
		return ArtifactOperationEnvelope{}, err
	}
	encoded, err := json.Marshal(artifactAcquireRequest{
		Source:    source,
		CDNPrefix: cdnPrefix,
	})
	if err != nil {
		return ArtifactOperationEnvelope{}, fmt.Errorf("encode Runtime artifact acquire request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+artifactAcquirePath, bytes.NewReader(encoded))
	if err != nil {
		return ArtifactOperationEnvelope{}, fmt.Errorf("create Runtime artifact acquire request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := c.acquireHTTPClient.Do(req)
	if err != nil {
		return ArtifactOperationEnvelope{}, fmt.Errorf("submit Runtime artifact acquire: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusAccepted {
		if artifactErr := decodeArtifactError(res); artifactErr != nil {
			return ArtifactOperationEnvelope{}, artifactErr
		}
		return ArtifactOperationEnvelope{}, fmt.Errorf("submit Runtime artifact acquire: unexpected HTTP status %s", res.Status)
	}

	var envelope ArtifactOperationEnvelope
	if err := decodeStrictJSON(res.Body, &envelope); err != nil {
		return ArtifactOperationEnvelope{}, fmt.Errorf("decode Runtime artifact acquire response: %w", err)
	}
	if err := envelope.Operation.Validate(); err != nil {
		return ArtifactOperationEnvelope{}, fmt.Errorf("validate Runtime artifact acquire response: %w", err)
	}
	if res.StatusCode == http.StatusOK && envelope.Operation.Phase != "succeeded" {
		return ArtifactOperationEnvelope{}, fmt.Errorf("validate Runtime artifact acquire response: HTTP 200 requires succeeded phase, got %q", envelope.Operation.Phase)
	}
	envelope.HTTPStatus = res.StatusCode
	return envelope, nil
}

func (c *ArtifactClient) Upload(ctx context.Context, body io.Reader, contentLength int64) (ArtifactOperationEnvelope, error) {
	if body == nil {
		return ArtifactOperationEnvelope{}, errors.New("Runtime artifact upload body is nil")
	}
	token, err := c.runtimeToken(ctx)
	if err != nil {
		return ArtifactOperationEnvelope{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+artifactUploadPath, body)
	if err != nil {
		return ArtifactOperationEnvelope{}, fmt.Errorf("create Runtime artifact upload request: %w", err)
	}
	if contentLength >= 0 {
		req.ContentLength = contentLength
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := c.uploadHTTPClient.Do(req)
	if err != nil {
		return ArtifactOperationEnvelope{}, fmt.Errorf("upload Runtime artifact: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		if artifactErr := decodeArtifactError(res); artifactErr != nil {
			return ArtifactOperationEnvelope{}, artifactErr
		}
		return ArtifactOperationEnvelope{}, fmt.Errorf("upload Runtime artifact: unexpected HTTP status %s", res.Status)
	}

	var envelope ArtifactOperationEnvelope
	if err := decodeStrictJSON(res.Body, &envelope); err != nil {
		return ArtifactOperationEnvelope{}, fmt.Errorf("decode Runtime artifact upload response: %w", err)
	}
	if err := envelope.Operation.Validate(); err != nil {
		return ArtifactOperationEnvelope{}, fmt.Errorf("validate Runtime artifact upload response: %w", err)
	}
	if envelope.Operation.Phase != "succeeded" {
		return ArtifactOperationEnvelope{}, fmt.Errorf("validate Runtime artifact upload response: HTTP 200 requires succeeded phase, got %q", envelope.Operation.Phase)
	}
	envelope.HTTPStatus = res.StatusCode
	return envelope, nil
}

func (c *ArtifactClient) runtimeToken(ctx context.Context) (string, error) {
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

func decodeArtifactError(response *http.Response) error {
	var envelope artifactErrorEnvelope
	if err := decodeStrictJSON(response.Body, &envelope); err != nil || strings.TrimSpace(envelope.Code) == "" {
		return nil
	}
	code := strings.TrimSpace(envelope.Code)
	status := response.StatusCode
	if knownStatus, ok := KnownArtifactErrorStatus(code); ok {
		status = knownStatus
	}
	return &ArtifactError{Code: code, HTTPStatus: status}
}
