package updatecheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	LauncherControlURLEnv = "CPAMP_LAUNCHER_CONTROL_URL"
	LauncherTokenFileEnv  = "CPAMP_LAUNCHER_TOKEN_FILE"
	BaseVersionEnv        = "CPAMP_BASE_VERSION"
	RunningSlotEnv        = "CPAMP_RUNNING_SLOT"

	MaxSlotArchiveBytes int64 = 192 << 20 // 192 MiB

	launcherStatusPath        = "/v1/launcher/slot/status"
	launcherPreparePath       = "/v1/launcher/slot/prepare"
	launcherPrepareUploadPath = "/v1/launcher/slot/prepare-upload"
	launcherActivatePath      = "/v1/launcher/slot/activate"
	launcherAbortAttemptPath  = "/v1/launcher/slot/abort-attempt"
	launcherRollbackPath      = "/v1/launcher/slot/rollback"

	LauncherAbortOutcomeAccepted = "accepted"
	LauncherAbortOutcomeAborted  = "aborted"

	launcherStatusTimeout   = 10 * time.Second
	launcherPrepareTimeout  = 15 * time.Minute
	launcherActivateTimeout = 3 * time.Minute
	launcherActionTimeout   = 30 * time.Second
	launcherMaxErrorBody    = 64 << 10
	launcherMaxJSONBody     = 256 << 10
)

var DefaultSlotCDNPresets = []string{
	"https://ghfast.top/",
	"https://gh-proxy.com/",
}

// SelfUpdateError represents a typed self-update or Launcher error suitable for
// structured HTTP JSON error responses.
type SelfUpdateError struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	HTTPStatus        int    `json:"-"`
	DefiniteRejection bool   `json:"-"`
}

func (e *SelfUpdateError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}

type SlotPrepareOperation struct {
	ID            string `json:"id"`
	TargetVersion string `json:"target_version"`
	Phase         string `json:"phase"`
	ReceivedBytes int64  `json:"received_bytes"`
	TotalBytes    int64  `json:"total_bytes"`
	ErrorCode     string `json:"error_code"`
}

type LauncherSlotStatus struct {
	Supported                bool                  `json:"supported"`
	Mode                     string                `json:"mode"`
	BaseVersion              string                `json:"base_version"`
	BaseEpoch                int                   `json:"base_epoch"`
	RunningVersion           string                `json:"running_version"`
	ActiveSlot               string                `json:"active_slot"`
	PreviousSlot             string                `json:"previous_slot"`
	RollbackTargetVersion    string                `json:"rollback_target_version"`
	RollbackAvailable        bool                  `json:"rollback_available"`
	RollbackSameSchema       bool                  `json:"rollback_same_schema"`
	RollbackBlockedReason    string                `json:"rollback_blocked_reason,omitempty"`
	SchemaGeneration         int                   `json:"schema_generation,omitempty"`
	PreviousSchemaGeneration int                   `json:"previous_schema_generation,omitempty"`
	State                    string                `json:"state"`
	ActiveAttemptID          string                `json:"active_attempt_id,omitempty"`
	LastAttemptID            string                `json:"last_attempt_id,omitempty"`
	StagedSlots              []string              `json:"staged_slots"`
	PrepareOperation         *SlotPrepareOperation `json:"prepare_operation"`
	LastFailureCode          string                `json:"last_failure_code"`
	LastRolledBackSlot       string                `json:"last_rolled_back_slot"`
}

type LauncherPrepareRequest struct {
	TargetVersion  string `json:"target_version"`
	ArchiveURL     string `json:"archive_url"`
	ExpectedSHA256 string `json:"expected_sha256"`
	ExpectedSize   int64  `json:"expected_size"`
}

type LauncherActivateRequest struct {
	TargetVersion            string `json:"target_version"`
	SnapshotDir              string `json:"snapshot_dir"`
	PreviousSchemaGeneration int    `json:"previous_schema_generation"`
	AttemptID                string `json:"attempt_id,omitempty"`
}

type LauncherRollbackRequest struct {
	ExpectedActiveSlot      string `json:"expected_active_slot"`
	CurrentSchemaGeneration int    `json:"current_schema_generation"`
}

type LauncherAbortAttemptRequest struct {
	AttemptID string `json:"attempt_id"`
}

type LauncherAbortAttemptResponse struct {
	AttemptID string `json:"attempt_id"`
	Outcome   string `json:"outcome"`
}

type SlotActionAccepted struct {
	Accepted            bool   `json:"accepted"`
	TargetVersion       string `json:"target_version"`
	TrialTimeoutSeconds int    `json:"trial_timeout_seconds,omitempty"`
	AttemptID           string `json:"attempt_id,omitempty"`
}

type launcherErrorEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// LauncherClient communicates with the local cpamp-launcher control listener
// over loopback HTTP using a bearer token read from disk on every call.
type LauncherClient struct {
	baseURL        string
	tokenFile      string
	statusClient   *http.Client
	prepareClient  *http.Client
	activateClient *http.Client
	actionClient   *http.Client
}

// NewLauncherClientFromEnv constructs a LauncherClient from
// CPAMP_LAUNCHER_CONTROL_URL and CPAMP_LAUNCHER_TOKEN_FILE, returning (nil, nil)
// when self-update launcher control is not configured.
func NewLauncherClientFromEnv() (*LauncherClient, error) {
	controlURL := strings.TrimSpace(os.Getenv(LauncherControlURLEnv))
	tokenFile := strings.TrimSpace(os.Getenv(LauncherTokenFileEnv))
	if controlURL == "" && tokenFile == "" {
		return nil, nil
	}
	if controlURL == "" || tokenFile == "" {
		return nil, errors.New("both CPAMP_LAUNCHER_CONTROL_URL and CPAMP_LAUNCHER_TOKEN_FILE must be configured together")
	}
	return NewLauncherClient(controlURL, tokenFile)
}

// NewLauncherClient creates an authenticated loopback client for cpamp-launcher.
func NewLauncherClient(controlURL, tokenFile string) (*LauncherClient, error) {
	cleanURL, err := validateLauncherControlURL(controlURL)
	if err != nil {
		return nil, err
	}
	cleanTokenFile := strings.TrimSpace(tokenFile)
	if cleanTokenFile == "" {
		return nil, errors.New("launcher token file path is required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &LauncherClient{
		baseURL:        cleanURL,
		tokenFile:      cleanTokenFile,
		statusClient:   newLauncherHTTPClient(transport, launcherStatusTimeout),
		prepareClient:  newLauncherHTTPClient(transport, launcherPrepareTimeout),
		activateClient: newLauncherHTTPClient(transport, launcherActivateTimeout),
		actionClient:   newLauncherHTTPClient(transport, launcherActionTimeout),
	}, nil
}

func newLauncherHTTPClient(transport http.RoundTripper, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func validateLauncherControlURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("launcher control URL is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return "", fmt.Errorf("invalid launcher control URL: %w", err)
	}
	if parsed.Scheme != "http" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return "", errors.New("launcher control URL must be a plain http loopback URL")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("launcher control URL must not include a path")
	}
	host := parsed.Hostname()
	if !isLoopbackHost(host) {
		return "", fmt.Errorf("launcher control URL host %q must be loopback", host)
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return "", errors.New("launcher control URL has invalid port")
		}
	}
	return strings.TrimRight(trimmed, "/"), nil
}

func isLoopbackHost(host string) bool {
	trimmed := strings.TrimSuffix(strings.TrimSpace(host), ".")
	if trimmed == "" {
		return false
	}
	if strings.EqualFold(trimmed, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(trimmed)
	if err != nil || addr.Zone() != "" {
		return false
	}
	return addr.Unmap().IsLoopback()
}

func (c *LauncherClient) readToken() (string, error) {
	if c == nil || strings.TrimSpace(c.tokenFile) == "" {
		return "", &SelfUpdateError{
			Code:       "self_update_unsupported",
			Message:    "launcher control client is not configured",
			HTTPStatus: http.StatusServiceUnavailable,
		}
	}
	data, err := os.ReadFile(c.tokenFile)
	if err != nil {
		return "", &SelfUpdateError{
			Code:       "launcher_unavailable",
			Message:    "failed to read launcher authentication token",
			HTTPStatus: http.StatusServiceUnavailable,
		}
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", &SelfUpdateError{
			Code:       "launcher_unavailable",
			Message:    "launcher authentication token is empty",
			HTTPStatus: http.StatusServiceUnavailable,
		}
	}
	return token, nil
}

func (c *LauncherClient) Status(ctx context.Context) (LauncherSlotStatus, error) {
	var out LauncherSlotStatus
	if err := c.doJSON(ctx, c.statusClient, http.MethodGet, launcherStatusPath, nil, &out, http.StatusOK); err != nil {
		return LauncherSlotStatus{}, err
	}
	if out.StagedSlots == nil {
		out.StagedSlots = []string{}
	}
	return out, nil
}

func (c *LauncherClient) Prepare(ctx context.Context, req LauncherPrepareRequest) (SlotPrepareOperation, error) {
	var out SlotPrepareOperation
	err := c.doJSON(ctx, c.prepareClient, http.MethodPost, launcherPreparePath, req, &out, http.StatusOK, http.StatusAccepted)
	return out, err
}

func (c *LauncherClient) PrepareUpload(ctx context.Context, targetVersion, expectedSHA256 string, body io.Reader, contentLength int64) (SlotPrepareOperation, error) {
	if body == nil {
		return SlotPrepareOperation{}, &SelfUpdateError{
			Code:       "invalid_request",
			Message:    "slot archive body is required",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	token, err := c.readToken()
	if err != nil {
		return SlotPrepareOperation{}, err
	}
	query := url.Values{}
	query.Set("target_version", strings.TrimSpace(targetVersion))
	if sha := strings.TrimSpace(expectedSHA256); sha != "" {
		query.Set("expected_sha256", sha)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+launcherPrepareUploadPath+"?"+query.Encode(), body)
	if err != nil {
		return SlotPrepareOperation{}, &SelfUpdateError{
			Code:       "invalid_request",
			Message:    "failed to construct upload request",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Accept", "application/json")
	if contentLength > 0 {
		req.ContentLength = contentLength
	}
	res, err := c.prepareClient.Do(req)
	if err != nil {
		return SlotPrepareOperation{}, mapTransportError(err)
	}
	defer res.Body.Close()
	var out SlotPrepareOperation
	if err := decodeLauncherResponse(res, &out, http.StatusOK, http.StatusAccepted); err != nil {
		return SlotPrepareOperation{}, err
	}
	return out, nil
}

func (c *LauncherClient) Activate(ctx context.Context, req LauncherActivateRequest) (SlotActionAccepted, error) {
	var out SlotActionAccepted
	err := c.doJSON(ctx, c.activateClient, http.MethodPost, launcherActivatePath, req, &out, http.StatusAccepted, http.StatusOK)
	return out, err
}

func (c *LauncherClient) AbortAttempt(ctx context.Context, attemptID string) (LauncherAbortAttemptResponse, error) {
	var out LauncherAbortAttemptResponse
	err := c.doJSON(ctx, c.statusClient, http.MethodPost, launcherAbortAttemptPath, LauncherAbortAttemptRequest{
		AttemptID: strings.TrimSpace(attemptID),
	}, &out, http.StatusOK)
	return out, err
}

func (c *LauncherClient) Rollback(ctx context.Context, req LauncherRollbackRequest) (SlotActionAccepted, error) {
	var out SlotActionAccepted
	err := c.doJSON(ctx, c.actionClient, http.MethodPost, launcherRollbackPath, req, &out, http.StatusAccepted, http.StatusOK)
	return out, err
}

func (c *LauncherClient) doJSON(
	ctx context.Context,
	client *http.Client,
	method string,
	endpoint string,
	payload any,
	dst any,
	acceptedStatuses ...int,
) error {
	token, err := c.readToken()
	if err != nil {
		return err
	}
	var bodyReader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return &SelfUpdateError{
				Code:       "invalid_request",
				Message:    "failed to encode launcher request",
				HTTPStatus: http.StatusBadRequest,
			}
		}
		bodyReader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, bodyReader)
	if err != nil {
		return &SelfUpdateError{
			Code:       "invalid_request",
			Message:    "failed to build launcher request",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return mapTransportError(err)
	}
	defer res.Body.Close()
	return decodeLauncherResponse(res, dst, acceptedStatuses...)
}

func decodeLauncherResponse(res *http.Response, dst any, acceptedStatuses ...int) error {
	if !statusMatches(res.StatusCode, acceptedStatuses...) {
		return decodeLauncherError(res)
	}
	if dst == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, launcherMaxJSONBody+1))
	if err != nil {
		return &SelfUpdateError{
			Code:       "launcher_unavailable",
			Message:    "failed reading launcher response",
			HTTPStatus: http.StatusBadGateway,
		}
	}
	if len(data) > launcherMaxJSONBody {
		return &SelfUpdateError{
			Code:       "launcher_unavailable",
			Message:    "launcher response exceeded size limit",
			HTTPStatus: http.StatusBadGateway,
		}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return &SelfUpdateError{
			Code:       "launcher_unavailable",
			Message:    "launcher returned empty response",
			HTTPStatus: http.StatusBadGateway,
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(dst); err != nil {
		return &SelfUpdateError{
			Code:       "launcher_unavailable",
			Message:    "launcher returned invalid JSON",
			HTTPStatus: http.StatusBadGateway,
		}
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return &SelfUpdateError{
			Code:       "launcher_unavailable",
			Message:    "launcher returned malformed JSON",
			HTTPStatus: http.StatusBadGateway,
		}
	}
	return nil
}

func statusMatches(got int, expected ...int) bool {
	for _, code := range expected {
		if got == code {
			return true
		}
	}
	return false
}

func decodeLauncherError(res *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(res.Body, launcherMaxErrorBody))
	var env launcherErrorEnvelope
	_ = json.Unmarshal(data, &env)
	code := strings.TrimSpace(env.Code)
	msg := strings.TrimSpace(env.Message)
	return normalizeLauncherError(res.StatusCode, code, msg)
}

func normalizeLauncherError(statusCode int, code, message string) *SelfUpdateError {
	definiteRejection := statusCode >= 400 && statusCode <= 599
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		if message == "" {
			message = "launcher rejected authentication credentials"
		}
		return &SelfUpdateError{
			Code:              "launcher_auth_failed",
			Message:           message,
			HTTPStatus:        http.StatusBadGateway,
			DefiniteRejection: definiteRejection,
		}
	}
	if code == "" {
		code = defaultCodeForStatus(statusCode)
	}
	mappedStatus := statusCode
	if known, ok := knownLauncherErrorHTTPStatus(code); ok {
		mappedStatus = known
	} else if mappedStatus < 400 || mappedStatus > 599 {
		mappedStatus = http.StatusBadGateway
	}
	if message == "" {
		message = code
	}
	return &SelfUpdateError{
		Code:              code,
		Message:           message,
		HTTPStatus:        mappedStatus,
		DefiniteRejection: definiteRejection,
	}
}

func knownLauncherErrorHTTPStatus(code string) (int, bool) {
	switch code {
	case "invalid_request":
		return http.StatusBadRequest, true
	case "slot_busy", "slot_conflict", "slot_not_staged", "version_conflict", "schema_rollback_forbidden", "recovery_required":
		return http.StatusConflict, true
	case "archive_too_large":
		return http.StatusRequestEntityTooLarge, true
	case "base_epoch_incompatible", "signature_invalid", "archive_invalid", "snapshot_invalid", "migration_required", "slot_asset_unavailable":
		return http.StatusUnprocessableEntity, true
	case "download_failed", "launcher_auth_failed":
		return http.StatusBadGateway, true
	case "self_update_unsupported", "launcher_unavailable":
		return http.StatusServiceUnavailable, true
	case "internal_error", "snapshot_failed", "snapshot_restore_failed":
		return http.StatusInternalServerError, true
	default:
		return 0, false
	}
}

func defaultCodeForStatus(statusCode int) string {
	switch statusCode {
	case http.StatusBadRequest:
		return "invalid_request"
	case http.StatusConflict:
		return "version_conflict"
	case http.StatusRequestEntityTooLarge:
		return "archive_too_large"
	case http.StatusUnprocessableEntity:
		return "archive_invalid"
	case http.StatusBadGateway:
		return "download_failed"
	case http.StatusServiceUnavailable:
		return "launcher_unavailable"
	default:
		return "internal_error"
	}
}

func mapTransportError(err error) error {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return &SelfUpdateError{
			Code:       "archive_too_large",
			Message:    "slot archive exceeds maximum allowed size",
			HTTPStatus: http.StatusRequestEntityTooLarge,
		}
	}
	var suErr *SelfUpdateError
	if errors.As(err, &suErr) && suErr != nil {
		return suErr
	}
	return &SelfUpdateError{
		Code:       "launcher_unavailable",
		Message:    "launcher control endpoint is unavailable",
		HTTPStatus: http.StatusServiceUnavailable,
	}
}
