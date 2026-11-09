package lifecycle

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/selection"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

const (
	ArtifactSchemaVersion = 1

	ArtifactOriginBundled  = "bundled"
	ArtifactOriginAcquired = "acquired"

	ArtifactSourceGitHub = "github"
	ArtifactSourceCDN    = "cdn"
	ArtifactSourceUpload = "upload"

	ArtifactPhaseDownloading = "downloading"
	ArtifactPhaseVerifying   = "verifying"
	ArtifactPhaseInstalling  = "installing"
	ArtifactPhaseSucceeded   = "succeeded"
	ArtifactPhaseFailed      = "failed"

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

	AcquireArtifactExecutionTimeout = 10 * time.Minute
	maxPinnedManifestBytes          = 16 << 10
)

var (
	ErrAcquisitionInProgress = errors.New("artifact acquisition is already in progress")
	ErrAcquisitionFailed     = errors.New("artifact acquisition failed")
	ErrArtifactAlreadyActive = errors.New("pinned artifact is already active")
	ErrRecoveryRequired      = errors.New("artifact selection committed; supervisor recovery is required")
)

// ArtifactError pairs a contract error code with the underlying cause.
type ArtifactError struct {
	Code string
	Err  error
}

func (e *ArtifactError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return e.Code
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Err)
}

func (e *ArtifactError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ArtifactErrorCode returns the canonical contract error code for err.
func ArtifactErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var artifactErr *ArtifactError
	if errors.As(err, &artifactErr) && artifactErr.Code != "" {
		return artifactErr.Code
	}
	switch {
	case errors.Is(err, ErrInvalidRequest), errors.Is(err, runtimeupdate.ErrInvalidCDNPrefix), errors.Is(err, runtimeupdate.ErrInvalidTargetVersion):
		return ArtifactCodeInvalidRequest
	case errors.Is(err, runtimeupdate.ErrUnsupportedPlatform), errors.Is(err, ErrUnsupportedStaging), errors.Is(err, ErrUnsupportedActivation):
		return ArtifactCodeUnsupportedPlatform
	case errors.Is(err, ErrAcquisitionInProgress):
		return ArtifactCodeAcquisitionInProgress
	case errors.Is(err, runtimeupdate.ErrArtifactConflict), errors.Is(err, runtimeupdate.ErrStageConflict), errors.Is(err, ErrActiveArtifactMismatch):
		return ArtifactCodeArtifactConflict
	case errors.Is(err, ErrArtifactAlreadyActive):
		return ArtifactCodeArtifactAlreadyActive
	case errors.Is(err, ErrRecoveryRequired):
		return ArtifactCodeRecoveryRequired
	case errors.Is(err, runtimeupdate.ErrArchiveTooLarge):
		return ArtifactCodeArchiveTooLarge
	case errors.Is(err, runtimeupdate.ErrArtifactMismatch):
		return ArtifactCodeArtifactMismatch
	case errors.Is(err, runtimeupdate.ErrArchiveInvalid):
		return ArtifactCodeArchiveInvalid
	case errors.Is(err, runtimeupdate.ErrSourceUnreachable):
		return ArtifactCodeSourceUnreachable
	case errors.Is(err, runtimeupdate.ErrDownloadFailed):
		return ArtifactCodeDownloadFailed
	default:
		return ArtifactCodeInstallFailed
	}
}

type ArtifactPinnedStatus struct {
	Version    string      `json:"version"`
	ArtifactID artifact.ID `json:"artifactId"`
}

type ArtifactActiveStatus struct {
	ArtifactID artifact.ID `json:"artifactId"`
	Origin     string      `json:"origin"`
}

type ArtifactOperationStatus struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	Phase         string `json:"phase"`
	ReceivedBytes int64  `json:"receivedBytes"`
	TotalBytes    int64  `json:"totalBytes"`
	Code          string `json:"code"`
}

type ArtifactStatusResponse struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Pinned        ArtifactPinnedStatus     `json:"pinned"`
	Active        *ArtifactActiveStatus    `json:"active"`
	Required      bool                     `json:"required"`
	Operation     *ArtifactOperationStatus `json:"operation"`
}

type ArtifactProbeResponse struct {
	Reachable bool   `json:"reachable"`
	Code      string `json:"code"`
}

type ArtifactAcquireRequest struct {
	Source    string `json:"source"`
	CDNPrefix string `json:"cdnPrefix,omitempty"`
}

type ArtifactOperationResponse struct {
	Operation ArtifactOperationStatus `json:"operation"`
}

// OfficialAcquisitionSource abstracts the official GitHub release source for
// reachability probing and archive download.
type OfficialAcquisitionSource interface {
	Probe(ctx context.Context, version string) error
	DownloadArchive(ctx context.Context, version string, destination io.Writer, progress runtimeupdate.ProgressFunc) error
}

// CDNAcquisitionSource abstracts a validated HTTPS CDN release source.
type CDNAcquisitionSource interface {
	DownloadArchive(ctx context.Context, version string, destination io.Writer, progress runtimeupdate.ProgressFunc) error
}

// AcquisitionOption configures optional transport overrides for EnableArtifactAcquisition.
type AcquisitionOption func(*artifactAcquisitionState)

// WithOfficialAcquisitionSource overrides the official GitHub source.
func WithOfficialAcquisitionSource(source OfficialAcquisitionSource) AcquisitionOption {
	return func(s *artifactAcquisitionState) {
		s.officialSource = source
		s.customSource = true
	}
}

// WithCDNAcquisitionSourceFactory overrides how CDN sources are constructed for a prefix.
func WithCDNAcquisitionSourceFactory(factory func(prefix string) (CDNAcquisitionSource, error)) AcquisitionOption {
	return func(s *artifactAcquisitionState) {
		s.cdnFactory = factory
		s.customSource = true
	}
}

// WithAcquisitionHTTPClient configures a custom HTTP client (for example from
// an httptest TLS server) for both the official and CDN transport sources.
func WithAcquisitionHTTPClient(client *http.Client) AcquisitionOption {
	return func(s *artifactAcquisitionState) {
		s.httpClient = client
		s.customSource = true
	}
}

// WithAcquisitionRootCAs configures custom trusted root CAs for both the
// official and CDN transport sources.
func WithAcquisitionRootCAs(roots *x509.CertPool) AcquisitionOption {
	return func(s *artifactAcquisitionState) {
		s.rootCAs = roots
		s.customSource = true
	}
}

// WithAcquisitionPlatform overrides the target OS/architecture used for release
// asset resolution.
func WithAcquisitionPlatform(goos, goarch string) AcquisitionOption {
	return func(s *artifactAcquisitionState) {
		s.goos = goos
		s.goarch = goarch
		s.platformOverridden = true
	}
}

// WithAcquisitionCDNAddrPolicy overrides the IP address policy enforced on CDN
// prefixes, redirects, and dial targets (production default: IsPublicCDNAddr).
func WithAcquisitionCDNAddrPolicy(policy runtimeupdate.CDNAddrPolicy) AcquisitionOption {
	return func(s *artifactAcquisitionState) {
		s.cdnAddrPolicy = policy
		s.customSource = true
	}
}

// WithAcquisitionCDNLookupIP overrides the DNS resolver used at dial time for
// CDN targets.
func WithAcquisitionCDNLookupIP(lookup runtimeupdate.CDNLookupIPAddrFunc) AcquisitionOption {
	return func(s *artifactAcquisitionState) {
		s.cdnLookupIP = lookup
		s.customSource = true
	}
}

// WithAcquisitionPostCommitHooks configures fault-injection callbacks invoked
// after selections.Commit succeeds during observer refresh and destination sync.
func WithAcquisitionPostCommitHooks(refreshHook, destinationSyncHook func() error) AcquisitionOption {
	return func(s *artifactAcquisitionState) {
		s.postCommitRefreshHook = refreshHook
		s.postCommitDestinationHook = destinationSyncHook
	}
}

type stageStoreProvider interface {
	StageStore() *runtimeupdate.Store
}

type artifactAcquisitionState struct {
	mu                        sync.Mutex
	manifestPath              string
	pinned                    *ArtifactPinnedStatus
	goos                      string
	goarch                    string
	platformOverridden        bool
	customSource              bool
	httpClient                *http.Client
	rootCAs                   *x509.CertPool
	officialSource            OfficialAcquisitionSource
	cdnFactory                func(prefix string) (CDNAcquisitionSource, error)
	cdnAddrPolicy             runtimeupdate.CDNAddrPolicy
	cdnLookupIP               runtimeupdate.CDNLookupIPAddrFunc
	postCommitRefreshHook     func() error
	postCommitDestinationHook func() error
	inFlight                  bool
	currentOp                 *ArtifactOperationStatus
	opSeq                     uint64
}

var executorAcquisitions sync.Map // map[*Executor]*artifactAcquisitionState

// EnableArtifactAcquisition configures first-time CPA binary acquisition on the
// Executor, reusing the selection store, stage store, and artifact observer
// already installed on the Executor.
func (e *Executor) EnableArtifactAcquisition(manifestPath string, sources ...AcquisitionOption) error {
	if e == nil {
		return errors.New("executor is required")
	}
	if e.closed.Load() {
		return ErrPersistenceUnavailable
	}
	trimmedManifest := strings.TrimSpace(manifestPath)
	if strings.ContainsRune(trimmedManifest, '\x00') {
		return errors.New("artifact manifest path must not contain NUL")
	}
	state := &artifactAcquisitionState{
		manifestPath: trimmedManifest,
		goos:         runtime.GOOS,
		goarch:       runtime.GOARCH,
	}
	for _, opt := range sources {
		if opt != nil {
			opt(state)
		}
	}
	if state.customSource && !state.platformOverridden && !runtimeupdate.SupportedPlatform(state.goos, state.goarch) {
		state.goos = "linux"
		state.goarch = "amd64"
	}
	if trimmedManifest != "" {
		pinned, err := loadPinnedArtifactManifest(trimmedManifest)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else {
			state.pinned = &pinned
		}
	}
	executorAcquisitions.Store(e, state)
	return nil
}

func (e *Executor) acquisitionState() (*artifactAcquisitionState, error) {
	if e == nil {
		return nil, &ArtifactError{Code: ArtifactCodeUnsupportedPlatform, Err: runtimeupdate.ErrUnsupportedPlatform}
	}
	raw, ok := executorAcquisitions.Load(e)
	if !ok {
		return nil, &ArtifactError{Code: ArtifactCodeUnsupportedPlatform, Err: runtimeupdate.ErrUnsupportedPlatform}
	}
	return raw.(*artifactAcquisitionState), nil
}

func (s *artifactAcquisitionState) resolvePinnedLocked() (ArtifactPinnedStatus, error) {
	if s.pinned != nil {
		return *s.pinned, nil
	}
	if s.manifestPath == "" {
		return ArtifactPinnedStatus{}, &ArtifactError{
			Code: ArtifactCodeInstallFailed,
			Err:  artifact.ErrManifestUnavailable,
		}
	}
	pinned, err := loadPinnedArtifactManifest(s.manifestPath)
	if err != nil {
		return ArtifactPinnedStatus{}, &ArtifactError{
			Code: ArtifactCodeInstallFailed,
			Err:  err,
		}
	}
	s.pinned = &pinned
	return pinned, nil
}

func (s *artifactAcquisitionState) resolveOfficialSourceLocked() (OfficialAcquisitionSource, error) {
	if !runtimeupdate.SupportedPlatform(s.goos, s.goarch) {
		return nil, &ArtifactError{Code: ArtifactCodeUnsupportedPlatform, Err: runtimeupdate.ErrUnsupportedPlatform}
	}
	if s.officialSource != nil {
		return s.officialSource, nil
	}
	client := s.httpClient
	if client == nil {
		client = runtimeupdate.NewOfficialHTTPClient(s.rootCAs)
	}
	source, err := runtimeupdate.NewSourceForPlatform(s.goos, s.goarch, client)
	if err != nil {
		return nil, &ArtifactError{Code: ArtifactErrorCode(err), Err: err}
	}
	return source, nil
}

func (s *artifactAcquisitionState) resolveCDNSourceLocked(prefix string) (CDNAcquisitionSource, error) {
	if _, err := runtimeupdate.ValidateCDNPrefixWithPolicy(prefix, s.cdnAddrPolicy); err != nil {
		return nil, &ArtifactError{Code: ArtifactCodeInvalidRequest, Err: err}
	}
	if !runtimeupdate.SupportedPlatform(s.goos, s.goarch) {
		return nil, &ArtifactError{Code: ArtifactCodeUnsupportedPlatform, Err: runtimeupdate.ErrUnsupportedPlatform}
	}
	if s.cdnFactory != nil {
		source, err := s.cdnFactory(prefix)
		if err != nil {
			return nil, &ArtifactError{Code: ArtifactErrorCode(err), Err: err}
		}
		return source, nil
	}
	client := s.httpClient
	if client == nil {
		client = runtimeupdate.NewCDNHTTPClient(s.rootCAs)
	}
	source, err := runtimeupdate.NewCDNSourceForPlatform(
		s.goos,
		s.goarch,
		prefix,
		client,
		runtimeupdate.WithCDNAddrPolicy(s.cdnAddrPolicy),
		runtimeupdate.WithCDNLookupIP(s.cdnLookupIP),
	)
	if err != nil {
		return nil, &ArtifactError{Code: ArtifactErrorCode(err), Err: err}
	}
	return source, nil
}

func (e *Executor) resolveAcquisitionStores() (activeSelectionStore, *runtimeupdate.Store, activeArtifactRefresher, error) {
	e.mu.Lock()
	selections := e.selections
	observer := e.artifact
	e.mu.Unlock()
	if selections == nil || observer == nil {
		return nil, nil, nil, &ArtifactError{
			Code: ArtifactCodeUnsupportedPlatform,
			Err:  runtimeupdate.ErrUnsupportedPlatform,
		}
	}
	provider, ok := selections.(stageStoreProvider)
	if !ok || provider.StageStore() == nil {
		return nil, nil, nil, &ArtifactError{
			Code: ArtifactCodeUnsupportedPlatform,
			Err:  runtimeupdate.ErrUnsupportedPlatform,
		}
	}
	return selections, provider.StageStore(), observer, nil
}

// ArtifactStatus returns the current pinned manifest, active artifact observation
// with origin, whether acquisition is required, and the latest operation state.
func (e *Executor) ArtifactStatus() (ArtifactStatusResponse, error) {
	state, err := e.acquisitionState()
	if err != nil {
		return ArtifactStatusResponse{}, err
	}
	state.mu.Lock()
	pinned, err := state.resolvePinnedLocked()
	var opCopy *ArtifactOperationStatus
	if state.currentOp != nil {
		copied := *state.currentOp
		opCopy = &copied
	}
	state.mu.Unlock()
	if err != nil {
		return ArtifactStatusResponse{}, err
	}

	var active *ArtifactActiveStatus
	e.mu.Lock()
	observer := e.artifact
	descriptor := e.active
	e.mu.Unlock()
	if observer != nil {
		if observed := observer.Observation(); observed != nil && observed.ArtifactID.IsValid() {
			origin := ArtifactOriginBundled
			if descriptor.IsFinalizedStage() {
				origin = ArtifactOriginAcquired
			}
			active = &ArtifactActiveStatus{
				ArtifactID: observed.ArtifactID,
				Origin:     origin,
			}
		}
	}
	return ArtifactStatusResponse{
		SchemaVersion: ArtifactSchemaVersion,
		Pinned:        pinned,
		Active:        active,
		Required:      active == nil,
		Operation:     opCopy,
	}, nil
}

// ProbeArtifactSource probes reachability of the official GitHub release asset
// within a 5s timeout bound.
func (e *Executor) ProbeArtifactSource(ctx context.Context) (ArtifactProbeResponse, error) {
	state, err := e.acquisitionState()
	if err != nil {
		return ArtifactProbeResponse{}, err
	}
	state.mu.Lock()
	pinned, err := state.resolvePinnedLocked()
	if err != nil {
		state.mu.Unlock()
		return ArtifactProbeResponse{}, err
	}
	source, err := state.resolveOfficialSourceLocked()
	state.mu.Unlock()
	if err != nil {
		return ArtifactProbeResponse{}, err
	}
	if err := source.Probe(ctx, pinned.Version); err != nil {
		return ArtifactProbeResponse{
			Reachable: false,
			Code:      ArtifactCodeSourceUnreachable,
		}, nil
	}
	return ArtifactProbeResponse{
		Reachable: true,
		Code:      "",
	}, nil
}

// Validate checks that the acquire request names a supported transport and
// valid CDN prefix when source == "cdn".
func (r ArtifactAcquireRequest) Validate() error {
	return r.ValidateWithPolicy(runtimeupdate.IsPublicCDNAddr)
}

// ValidateWithPolicy validates the acquire request using the given CDN IP policy.
func (r ArtifactAcquireRequest) ValidateWithPolicy(policy runtimeupdate.CDNAddrPolicy) error {
	switch r.Source {
	case ArtifactSourceGitHub:
		if r.CDNPrefix != "" {
			return &ArtifactError{Code: ArtifactCodeInvalidRequest, Err: ErrInvalidRequest}
		}
		return nil
	case ArtifactSourceCDN:
		if _, err := runtimeupdate.ValidateCDNPrefixWithPolicy(r.CDNPrefix, policy); err != nil {
			return &ArtifactError{Code: ArtifactCodeInvalidRequest, Err: err}
		}
		return nil
	default:
		return &ArtifactError{Code: ArtifactCodeInvalidRequest, Err: ErrInvalidRequest}
	}
}

// AcquireArtifact starts an asynchronous acquisition from GitHub or a CDN
// prefix, or returns started=false with a succeeded operation when the pinned
// artifact is already active.
func (e *Executor) AcquireArtifact(ctx context.Context, req ArtifactAcquireRequest) (ArtifactOperationStatus, bool, error) {
	if e == nil || e.closed.Load() {
		return ArtifactOperationStatus{}, false, &ArtifactError{Code: ArtifactCodeInstallFailed, Err: ErrPersistenceUnavailable}
	}
	state, err := e.acquisitionState()
	if err != nil {
		return ArtifactOperationStatus{}, false, err
	}

	state.mu.Lock()
	if err := req.ValidateWithPolicy(state.cdnAddrPolicy); err != nil {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, false, err
	}
	if !runtimeupdate.SupportedPlatform(state.goos, state.goarch) {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, false, &ArtifactError{
			Code: ArtifactCodeUnsupportedPlatform,
			Err:  runtimeupdate.ErrUnsupportedPlatform,
		}
	}
	pinned, err := state.resolvePinnedLocked()
	if err != nil {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, false, err
	}
	var downloadFn runtimeupdate.AcquireDownloadFunc
	switch req.Source {
	case ArtifactSourceGitHub:
		source, sourceErr := state.resolveOfficialSourceLocked()
		if sourceErr != nil {
			state.mu.Unlock()
			return ArtifactOperationStatus{}, false, sourceErr
		}
		downloadFn = func(dlCtx context.Context, dst io.Writer, progress runtimeupdate.ProgressFunc) error {
			return source.DownloadArchive(dlCtx, pinned.Version, dst, progress)
		}
	case ArtifactSourceCDN:
		source, sourceErr := state.resolveCDNSourceLocked(req.CDNPrefix)
		if sourceErr != nil {
			state.mu.Unlock()
			return ArtifactOperationStatus{}, false, sourceErr
		}
		downloadFn = func(dlCtx context.Context, dst io.Writer, progress runtimeupdate.ProgressFunc) error {
			return source.DownloadArchive(dlCtx, pinned.Version, dst, progress)
		}
	}
	if state.inFlight {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, false, &ArtifactError{
			Code: ArtifactCodeAcquisitionInProgress,
			Err:  ErrAcquisitionInProgress,
		}
	}
	selections, stageStore, observer, storeErr := e.resolveAcquisitionStores()
	if storeErr != nil {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, false, storeErr
	}

	alreadyActive, conflictErr := e.evaluateAcquisitionAdmission(state.manifestPath, pinned, selections, stageStore, observer)
	if conflictErr != nil {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, false, conflictErr
	}
	if alreadyActive {
		if state.currentOp == nil || state.currentOp.Phase != ArtifactPhaseSucceeded {
			state.opSeq++
			state.currentOp = &ArtifactOperationStatus{
				ID:     nextArtifactOperationID(state.opSeq),
				Source: req.Source,
				Phase:  ArtifactPhaseSucceeded,
				Code:   "",
			}
		}
		opCopy := *state.currentOp
		state.mu.Unlock()
		return opCopy, false, nil
	}

	if finalized, resolveErr := stageStore.ResolveFinalized(pinned.Version); resolveErr == nil {
		if finalized.Metadata.ArtifactID != pinned.ArtifactID {
			state.mu.Unlock()
			return ArtifactOperationStatus{}, false, &ArtifactError{
				Code: ArtifactCodeArtifactConflict,
				Err:  runtimeupdate.ErrArtifactConflict,
			}
		}
	} else if errors.Is(resolveErr, runtimeupdate.ErrStageConflict) {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, false, &ArtifactError{
			Code: ArtifactCodeArtifactConflict,
			Err:  resolveErr,
		}
	}

	if !e.admitPrepareWorker() {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, false, &ArtifactError{
			Code: ArtifactCodeInstallFailed,
			Err:  ErrPersistenceUnavailable,
		}
	}
	state.opSeq++
	opID := nextArtifactOperationID(state.opSeq)
	state.inFlight = true
	state.currentOp = &ArtifactOperationStatus{
		ID:            opID,
		Source:        req.Source,
		Phase:         ArtifactPhaseDownloading,
		ReceivedBytes: 0,
		TotalBytes:    0,
		Code:          "",
	}
	opCopy := *state.currentOp
	state.mu.Unlock()

	go func() {
		defer e.prepareWorkers.Done()
		execCtx, cancel := context.WithTimeout(e.recoveryContext, AcquireArtifactExecutionTimeout)
		defer cancel()
		_, _ = e.executeArtifactAcquisition(execCtx, state, opID, pinned, selections, stageStore, observer, downloadFn)
	}()

	return opCopy, true, nil
}

// UploadArtifact synchronously stages and commits the pinned artifact from an
// uploaded .tar.gz stream.
func (e *Executor) UploadArtifact(ctx context.Context, body io.Reader, contentLength int64) (ArtifactOperationStatus, error) {
	if e == nil || e.closed.Load() {
		return ArtifactOperationStatus{}, &ArtifactError{Code: ArtifactCodeInstallFailed, Err: ErrPersistenceUnavailable}
	}
	state, err := e.acquisitionState()
	if err != nil {
		return ArtifactOperationStatus{}, err
	}

	state.mu.Lock()
	if !runtimeupdate.SupportedPlatform(state.goos, state.goarch) {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, &ArtifactError{
			Code: ArtifactCodeUnsupportedPlatform,
			Err:  runtimeupdate.ErrUnsupportedPlatform,
		}
	}
	pinned, err := state.resolvePinnedLocked()
	if err != nil {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, err
	}
	if state.inFlight {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, &ArtifactError{
			Code: ArtifactCodeAcquisitionInProgress,
			Err:  ErrAcquisitionInProgress,
		}
	}
	selections, stageStore, observer, storeErr := e.resolveAcquisitionStores()
	if storeErr != nil {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, storeErr
	}
	alreadyActive, conflictErr := e.evaluateAcquisitionAdmission(state.manifestPath, pinned, selections, stageStore, observer)
	if conflictErr != nil {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, conflictErr
	}
	if alreadyActive {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, &ArtifactError{
			Code: ArtifactCodeArtifactAlreadyActive,
			Err:  ErrArtifactAlreadyActive,
		}
	}
	if finalized, resolveErr := stageStore.ResolveFinalized(pinned.Version); resolveErr == nil {
		if finalized.Metadata.ArtifactID != pinned.ArtifactID {
			state.mu.Unlock()
			return ArtifactOperationStatus{}, &ArtifactError{
				Code: ArtifactCodeArtifactConflict,
				Err:  runtimeupdate.ErrArtifactConflict,
			}
		}
	} else if errors.Is(resolveErr, runtimeupdate.ErrStageConflict) {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, &ArtifactError{
			Code: ArtifactCodeArtifactConflict,
			Err:  resolveErr,
		}
	}
	if contentLength > runtimeupdate.MaxAcquisitionArchiveBytes {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, &ArtifactError{
			Code: ArtifactCodeArchiveTooLarge,
			Err:  runtimeupdate.ErrArchiveTooLarge,
		}
	}

	if !e.admitPrepareWorker() {
		state.mu.Unlock()
		return ArtifactOperationStatus{}, &ArtifactError{
			Code: ArtifactCodeInstallFailed,
			Err:  ErrPersistenceUnavailable,
		}
	}
	defer e.prepareWorkers.Done()

	var totalBytes int64
	if contentLength > 0 {
		totalBytes = contentLength
	}
	state.opSeq++
	opID := nextArtifactOperationID(state.opSeq)
	state.inFlight = true
	state.currentOp = &ArtifactOperationStatus{
		ID:            opID,
		Source:        ArtifactSourceUpload,
		Phase:         ArtifactPhaseDownloading,
		ReceivedBytes: 0,
		TotalBytes:    totalBytes,
		Code:          "",
	}
	state.mu.Unlock()

	execCtx, cancel := context.WithTimeout(ctx, AcquireArtifactExecutionTimeout)
	defer cancel()
	return e.executeArtifactAcquisition(
		execCtx,
		state,
		opID,
		pinned,
		selections,
		stageStore,
		observer,
		runtimeupdate.CopyUploadArchive(body, contentLength),
	)
}

func (e *Executor) evaluateAcquisitionAdmission(
	manifestPath string,
	pinned ArtifactPinnedStatus,
	selections activeSelectionStore,
	stageStore *runtimeupdate.Store,
	observer activeArtifactRefresher,
) (alreadyActive bool, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.evaluateAcquisitionAdmissionLocked(manifestPath, pinned, selections, stageStore, observer)
}

func (e *Executor) evaluateAcquisitionAdmissionLocked(
	manifestPath string,
	pinned ArtifactPinnedStatus,
	selections activeSelectionStore,
	stageStore *runtimeupdate.Store,
	observer activeArtifactRefresher,
) (alreadyActive bool, err error) {
	if e.closed.Load() {
		return false, &ArtifactError{Code: ArtifactCodeInstallFailed, Err: ErrPersistenceUnavailable}
	}
	if e.activeSelectionAmbiguous {
		return false, &ArtifactError{Code: ArtifactCodeRecoveryRequired, Err: ErrRecoveryRequired}
	}
	descriptor := e.currentDescriptorLocked()
	procState := e.process.Observe().State
	processRunning := procState != cpaprocess.StateNotStarted && procState != cpaprocess.StateExited
	if descriptor.IsFinalizedStage() {
		if descriptor.ArtifactID != pinned.ArtifactID || (descriptor.Version != "" && descriptor.Version != pinned.Version) {
			return false, &ArtifactError{
				Code: ArtifactCodeArtifactConflict,
				Err:  runtimeupdate.ErrArtifactConflict,
			}
		}
		if observer == nil {
			return false, &ArtifactError{
				Code: ArtifactCodeArtifactConflict,
				Err:  runtimeupdate.ErrArtifactConflict,
			}
		}
		observed := observer.Observation()
		if observed == nil || !observed.ArtifactID.IsValid() || observed.ArtifactID != pinned.ArtifactID || (observed.Version != "" && observed.Version != pinned.Version) {
			return false, &ArtifactError{
				Code: ArtifactCodeArtifactConflict,
				Err:  runtimeupdate.ErrArtifactConflict,
			}
		}
		if selections != nil {
			revalidated, revalidateErr := selections.Revalidate(descriptor)
			if revalidateErr != nil || revalidated.ArtifactID != pinned.ArtifactID {
				return false, &ArtifactError{
					Code: ArtifactCodeArtifactConflict,
					Err:  runtimeupdate.ErrArtifactConflict,
				}
			}
		}
		if stageStore != nil && stageStore.HasAcquiredStage(pinned.Version, pinned.ArtifactID) {
			return true, nil
		}
		if processRunning {
			return false, &ArtifactError{
				Code: ArtifactCodeArtifactConflict,
				Err:  runtimeupdate.ErrArtifactConflict,
			}
		}
		return false, nil
	}

	execPath := strings.TrimSpace(descriptor.ExecutablePath)
	if execPath == "" {
		return false, &ArtifactError{
			Code: ArtifactCodeArtifactConflict,
			Err:  runtimeupdate.ErrArtifactConflict,
		}
	}
	info, statErr := os.Lstat(execPath)
	if errors.Is(statErr, os.ErrNotExist) {
		if processRunning {
			return false, &ArtifactError{
				Code: ArtifactCodeArtifactConflict,
				Err:  runtimeupdate.ErrArtifactConflict,
			}
		}
		if observer != nil {
			if observed := observer.Observation(); observed != nil && observed.ArtifactID.IsValid() {
				return false, &ArtifactError{
					Code: ArtifactCodeArtifactConflict,
					Err:  runtimeupdate.ErrArtifactConflict,
				}
			}
		}
		return false, nil
	}
	if statErr != nil || !info.Mode().IsRegular() || observer == nil {
		return false, &ArtifactError{
			Code: ArtifactCodeArtifactConflict,
			Err:  runtimeupdate.ErrArtifactConflict,
		}
	}
	observed := observer.Observation()
	if observed == nil || !observed.ArtifactID.IsValid() || observed.ArtifactID != pinned.ArtifactID || (observed.Version != "" && observed.Version != pinned.Version) {
		return false, &ArtifactError{
			Code: ArtifactCodeArtifactConflict,
			Err:  runtimeupdate.ErrArtifactConflict,
		}
	}
	verifyManifest := strings.TrimSpace(descriptor.MetadataPath)
	if verifyManifest == "" {
		verifyManifest = strings.TrimSpace(manifestPath)
	}
	diskObserver := artifact.NewObserver(execPath, verifyManifest)
	refreshErr := diskObserver.Refresh()
	diskObs := diskObserver.Observation()
	if refreshErr != nil || diskObs == nil || diskObs.ArtifactID != pinned.ArtifactID || (diskObs.Version != "" && diskObs.Version != pinned.Version) {
		return false, &ArtifactError{
			Code: ArtifactCodeArtifactConflict,
			Err:  runtimeupdate.ErrArtifactConflict,
		}
	}
	return true, nil
}

func (e *Executor) executeArtifactAcquisition(
	ctx context.Context,
	state *artifactAcquisitionState,
	opID string,
	pinned ArtifactPinnedStatus,
	selections activeSelectionStore,
	stageStore *runtimeupdate.Store,
	observer activeArtifactRefresher,
	download runtimeupdate.AcquireDownloadFunc,
) (ArtifactOperationStatus, error) {
	updateProgress := func(receivedBytes, totalBytes int64) {
		state.mu.Lock()
		if state.currentOp != nil && state.currentOp.ID == opID {
			state.currentOp.ReceivedBytes = receivedBytes
			state.currentOp.TotalBytes = totalBytes
		}
		state.mu.Unlock()
	}
	updatePhase := func(phase string) {
		state.mu.Lock()
		if state.currentOp != nil && state.currentOp.ID == opID {
			state.currentOp.Phase = phase
		}
		state.mu.Unlock()
	}
	failOperation := func(err error) (ArtifactOperationStatus, error) {
		code := ArtifactErrorCode(err)
		state.mu.Lock()
		var opCopy ArtifactOperationStatus
		if state.currentOp != nil && state.currentOp.ID == opID {
			state.currentOp.Phase = ArtifactPhaseFailed
			state.currentOp.Code = code
			opCopy = *state.currentOp
		}
		state.inFlight = false
		state.mu.Unlock()
		return opCopy, &ArtifactError{Code: code, Err: err}
	}

	stageRes, err := stageStore.AcquireStage(ctx, pinned.Version, pinned.ArtifactID, download, updateProgress, updatePhase)
	if err != nil {
		return failOperation(err)
	}

	updatePhase(ArtifactPhaseInstalling)
	if err := e.commitAcquiredStage(state, pinned, selections, stageStore, observer, stageRes.CreatedStage); err != nil {
		return failOperation(err)
	}

	state.mu.Lock()
	var opCopy ArtifactOperationStatus
	if state.currentOp != nil && state.currentOp.ID == opID {
		state.currentOp.Phase = ArtifactPhaseSucceeded
		state.currentOp.Code = ""
		opCopy = *state.currentOp
	}
	state.inFlight = false
	state.mu.Unlock()
	return opCopy, nil
}

func (e *Executor) markAcquisitionRecoveryRequiredLocked() {
	e.activeSelectionAmbiguous = true
	e.disableRecovery(RecoveryStateManualIntervention)
}

func (e *Executor) preverifyDestinationSyncLocked(artifactID artifact.ID) error {
	if e.destination == nil {
		return nil
	}
	staged, err := e.destination.load()
	if err != nil || staged == nil {
		return err
	}
	if !artifactID.IsValid() {
		return ErrActiveArtifactMismatch
	}
	if _, err := e.loadOriginStageRequestLocked(); err != nil {
		return err
	}
	updated := *staged
	updated.ExpectedActiveArtifactID = artifactID
	return updated.Validate()
}

func (e *Executor) commitAcquiredStage(
	state *artifactAcquisitionState,
	pinned ArtifactPinnedStatus,
	selections activeSelectionStore,
	stageStore *runtimeupdate.Store,
	observer activeArtifactRefresher,
	createdStage bool,
) error {
	cleanupCreatedStage := func() {
		if createdStage && stageStore != nil {
			_ = stageStore.RemoveCreatedStage(pinned.Version)
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	alreadyActive, conflictErr := e.evaluateAcquisitionAdmissionLocked(state.manifestPath, pinned, selections, stageStore, observer)
	if conflictErr != nil {
		cleanupCreatedStage()
		return conflictErr
	}
	if alreadyActive {
		if !e.currentDescriptorLocked().IsFinalizedStage() {
			cleanupCreatedStage()
			return &ArtifactError{Code: ArtifactCodeArtifactConflict, Err: runtimeupdate.ErrArtifactConflict}
		}
		return nil
	}

	candidate, err := selections.ResolveFinalized(pinned.Version)
	if err != nil {
		cleanupCreatedStage()
		if errors.Is(err, runtimeupdate.ErrStageConflict) || errors.Is(err, runtimeupdate.ErrArtifactConflict) {
			return &ArtifactError{Code: ArtifactCodeArtifactConflict, Err: err}
		}
		return &ArtifactError{Code: ArtifactCodeInstallFailed, Err: err}
	}
	if candidate.ArtifactID != pinned.ArtifactID {
		cleanupCreatedStage()
		return &ArtifactError{Code: ArtifactCodeArtifactConflict, Err: runtimeupdate.ErrArtifactConflict}
	}
	if observer == nil {
		cleanupCreatedStage()
		return &ArtifactError{Code: ArtifactCodeInstallFailed, Err: ErrPersistenceUnavailable}
	}
	candidateObserver := artifact.NewObserver(candidate.ExecutablePath, candidate.MetadataPath)
	err = candidateObserver.Refresh()
	candidateObs := candidateObserver.Observation()
	if err != nil || candidateObs == nil || candidateObs.ArtifactID != pinned.ArtifactID || (candidateObs.Version != "" && candidateObs.Version != pinned.Version) {
		cleanupCreatedStage()
		if err == nil {
			err = runtimeupdate.ErrArtifactConflict
		}
		return &ArtifactError{Code: ArtifactCodeInstallFailed, Err: err}
	}
	if err := e.preverifyDestinationSyncLocked(candidate.ArtifactID); err != nil {
		cleanupCreatedStage()
		return &ArtifactError{Code: ArtifactCodeInstallFailed, Err: err}
	}

	if err := selections.Commit(candidate); err != nil {
		if errors.Is(err, selection.ErrSelectionPublicationAmbiguous) {
			e.markAcquisitionRecoveryRequiredLocked()
			return &ArtifactError{
				Code: ArtifactCodeRecoveryRequired,
				Err:  errors.Join(ErrRecoveryRequired, err),
			}
		}
		cleanupCreatedStage()
		return &ArtifactError{Code: ArtifactCodeInstallFailed, Err: err}
	}

	e.active = candidate
	e.executable = candidate.ExecutablePath

	refreshErr := refreshSelectedDescriptor(observer, &candidate)
	if refreshErr == nil && state.postCommitRefreshHook != nil {
		refreshErr = state.postCommitRefreshHook()
	}
	destErr := e.syncDestinationExpectedArtifactLocked(candidate.ArtifactID)
	if destErr == nil && state.postCommitDestinationHook != nil {
		destErr = state.postCommitDestinationHook()
	}
	if postCommitErr := errors.Join(refreshErr, destErr); postCommitErr != nil {
		e.markAcquisitionRecoveryRequiredLocked()
		return &ArtifactError{
			Code: ArtifactCodeRecoveryRequired,
			Err:  errors.Join(ErrRecoveryRequired, postCommitErr),
		}
	}
	return nil
}

type pinnedArtifactManifest struct {
	SchemaVersion       int         `json:"schemaVersion"`
	Engine              string      `json:"engine"`
	Version             string      `json:"version"`
	ArtifactID          artifact.ID `json:"artifactId"`
	SourceArchiveDigest string      `json:"sourceArchiveDigest,omitempty"`
}

func loadPinnedArtifactManifest(manifestPath string) (ArtifactPinnedStatus, error) {
	if strings.TrimSpace(manifestPath) == "" {
		return ArtifactPinnedStatus{}, artifact.ErrManifestUnavailable
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return ArtifactPinnedStatus{}, fmt.Errorf("%w: %v", artifact.ErrManifestUnavailable, err)
	}
	encoded, readErr := io.ReadAll(io.LimitReader(file, maxPinnedManifestBytes+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return ArtifactPinnedStatus{}, fmt.Errorf("%w: %v", artifact.ErrManifestUnavailable, err)
	}
	if len(encoded) > maxPinnedManifestBytes {
		return ArtifactPinnedStatus{}, fmt.Errorf("%w: exceeds %d bytes", artifact.ErrManifestInvalid, maxPinnedManifestBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var parsed pinnedArtifactManifest
	if err := decoder.Decode(&parsed); err != nil {
		return ArtifactPinnedStatus{}, fmt.Errorf("%w: %v", artifact.ErrManifestInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ArtifactPinnedStatus{}, fmt.Errorf("%w: multiple JSON values", artifact.ErrManifestInvalid)
	}
	if parsed.SchemaVersion != artifact.ManifestSchemaVersion ||
		parsed.Engine != artifact.EngineCPA ||
		runtimeupdate.ValidateVersion(parsed.Version) != nil ||
		!parsed.ArtifactID.IsValid() {
		return ArtifactPinnedStatus{}, fmt.Errorf("%w: noncanonical manifest fields", artifact.ErrManifestInvalid)
	}
	return ArtifactPinnedStatus{
		Version:    parsed.Version,
		ArtifactID: parsed.ArtifactID,
	}, nil
}

func nextArtifactOperationID(seq uint64) string {
	var randomBytes [8]byte
	if _, err := rand.Read(randomBytes[:]); err == nil {
		return "acquire-" + hex.EncodeToString(randomBytes[:])
	}
	return fmt.Sprintf("acquire-%d", seq)
}
