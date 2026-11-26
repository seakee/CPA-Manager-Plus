//go:build unix

package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

const (
	SlotPreparePhaseDownloading = "downloading"
	SlotPreparePhaseVerifying   = "verifying"
	SlotPreparePhaseStaging     = "staging"
	SlotPreparePhaseSucceeded   = "succeeded"
	SlotPreparePhaseFailed      = "failed"

	maxSlotDownloadRedirects = 5
	slotDownloadTimeout      = 15 * time.Minute
)

// SlotPrepareOperation represents the in-memory prepare_operation state exposed on GET /status.
type SlotPrepareOperation struct {
	ID            string `json:"id"`
	TargetVersion string `json:"target_version"`
	Phase         string `json:"phase"`
	ReceivedBytes int64  `json:"received_bytes"`
	TotalBytes    int64  `json:"total_bytes"`
	ErrorCode     string `json:"error_code"`
}

func (op *SlotPrepareOperation) clone() *SlotPrepareOperation {
	if op == nil {
		return nil
	}
	cp := *op
	return &cp
}

type slotStatusResponse struct {
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
	RollbackBlockedReason    string                `json:"rollback_blocked_reason"`
	State                    string                `json:"state"`
	ActiveAttemptID          string                `json:"active_attempt_id,omitempty"`
	LastAttemptID            string                `json:"last_attempt_id,omitempty"`
	SchemaGeneration         int                   `json:"schema_generation"`
	PreviousSchemaGeneration int                   `json:"previous_schema_generation"`
	StagedSlots              []string              `json:"staged_slots"`
	PrepareOperation         *SlotPrepareOperation `json:"prepare_operation"`
	LastFailureCode          string                `json:"last_failure_code"`
	LastRolledBackSlot       string                `json:"last_rolled_back_slot"`
}

type slotPrepareRequest struct {
	TargetVersion  string `json:"target_version"`
	ArchiveURL     string `json:"archive_url"`
	ExpectedSHA256 string `json:"expected_sha256"`
	ExpectedSize   int64  `json:"expected_size"`
}

type slotActivateRequest struct {
	TargetVersion            string `json:"target_version"`
	SnapshotDir              string `json:"snapshot_dir"`
	PreviousSchemaGeneration int    `json:"previous_schema_generation"`
	AttemptID                string `json:"attempt_id,omitempty"`
}

// SlotActionAcceptedResponse is the 202 Accepted response body for POST /v1/launcher/slot/activate.
type SlotActionAcceptedResponse struct {
	Accepted            bool   `json:"accepted"`
	TargetVersion       string `json:"target_version"`
	TrialTimeoutSeconds int    `json:"trial_timeout_seconds"`
	AttemptID           string `json:"attempt_id,omitempty"`
}

type slotActivateResponse = SlotActionAcceptedResponse

type slotRollbackRequest struct {
	ExpectedActiveSlot      string `json:"expected_active_slot"`
	CurrentSchemaGeneration int    `json:"current_schema_generation"`
}

type slotRollbackResponse struct {
	Accepted      bool   `json:"accepted"`
	TargetVersion string `json:"target_version"`
}

const (
	SlotCodeSlotConflict     = "slot_conflict"
	SlotAbortOutcomeAccepted = "accepted"
	SlotAbortOutcomeAborted  = "aborted"
	maxAbortedSlotAttempts   = 128
)

type slotAbortAttemptRequest struct {
	AttemptID string `json:"attempt_id"`
}

type slotAbortAttemptResponse struct {
	AttemptID string `json:"attempt_id"`
	Outcome   string `json:"outcome"`
}
type slotErrorEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SlotSwitchKind identifies whether a control switch request is an activate or rollback.
type SlotSwitchKind int

const (
	SlotSwitchActivate SlotSwitchKind = iota + 1
	SlotSwitchRollback
)

// SlotSwitchAction describes a validated slot transition queued by the control server.
type SlotSwitchAction struct {
	Kind                     SlotSwitchKind
	AttemptID                string
	TargetVersion            string
	SnapshotDir              string
	TargetSchemaGeneration   int
	PreviousSchemaGeneration int
	ExpectedActiveSlot       string
	RollbackTargetSlot       string
	RollbackSchemaGeneration int
	TargetLauncherPath       string
}

// SlotControlConfig configures the Launcher loopback HTTP control server.
type SlotControlConfig struct {
	ListenAddr        string
	Mode              string
	LauncherToken     string
	Base              BaseInstallationInfo
	RunningSlot       string
	DataDir           string
	ManagerUID        int
	TrialTimeout      time.Duration
	Store             *SlotStore
	ArchiveAddrPolicy func(netip.Addr) bool
	ArchiveTLSRootCAs *x509.CertPool
	ArchiveLookupIP   func(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// ValidateLoopbackListenAddr validates that addr is a literal loopback IP:port
// (e.g. 127.0.0.1:9083 or [::1]:9083; port 0 allowed for ephemeral test listeners).
func ValidateLoopbackListenAddr(addr string) (string, error) {
	trimmed := strings.TrimSpace(addr)
	host, portStr, err := net.SplitHostPort(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid loopback control address %q: %w", addr, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
		return "", fmt.Errorf("launcher control address %q must bind a literal loopback IP", addr)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return "", fmt.Errorf("launcher control address %q has invalid port", addr)
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
}

// SlotControlServer serves /v1/launcher/slot/* over an authenticated loopback listener.
type SlotControlServer struct {
	cfg        SlotControlConfig
	listener   net.Listener
	httpServer *http.Server
	boundAddr  string
	switchCh   chan SlotSwitchAction

	mu                   sync.Mutex
	busy                 bool
	switching            bool
	pendingSwitch        *SlotSwitchAction
	abortedAttempts      map[string]struct{}
	abortedAttemptsOrder []string
	prepOp               *SlotPrepareOperation
	prepReceived         atomic.Int64
	dlCancel             context.CancelFunc
	dlWG                 sync.WaitGroup

	beforeActivateReserveHook func()
	afterActivateReserveHook  func(http.ResponseWriter) bool
}

// StartSlotControlServer binds the loopback control listener and starts serving in the background.
func StartSlotControlServer(cfg SlotControlConfig) (*SlotControlServer, error) {
	if cfg.Store == nil {
		return nil, errors.New("slot store is required for control server")
	}
	if len(cfg.LauncherToken) < 32 {
		return nil, errors.New("launcher control token must be at least 32 characters")
	}
	listenAddr := cfg.ListenAddr
	if listenAddr == "" {
		listenAddr = DefaultLauncherControlAddr
	}
	normAddr, err := ValidateLoopbackListenAddr(listenAddr)
	if err != nil {
		return nil, err
	}
	if cfg.Mode == "" {
		cfg.Mode = LauncherModeDocker
	}
	if cfg.TrialTimeout <= 0 {
		cfg.TrialTimeout = DefaultTrialWatchdogTimeout
	}
	if cfg.ArchiveAddrPolicy == nil {
		cfg.ArchiveAddrPolicy = update.IsPublicCDNAddr
	}
	if cfg.ArchiveLookupIP == nil {
		cfg.ArchiveLookupIP = net.DefaultResolver.LookupNetIP
	}

	ln, err := listenLoopbackWithRetry(normAddr, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to bind launcher control listener %s: %w", normAddr, err)
	}

	s := &SlotControlServer{
		cfg:       cfg,
		listener:  ln,
		boundAddr: ln.Addr().String(),
		switchCh:  make(chan SlotSwitchAction, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/launcher/slot/status", s.withAuth(s.handleStatus))
	mux.HandleFunc("/v1/launcher/slot/prepare", s.withAuth(s.handlePrepare))
	mux.HandleFunc("/v1/launcher/slot/prepare-upload", s.withAuth(s.handlePrepareUpload))
	mux.HandleFunc("/v1/launcher/slot/activate", s.withAuth(s.handleActivate))
	mux.HandleFunc("/v1/launcher/slot/abort-attempt", s.withAuth(s.handleAbortAttempt))
	mux.HandleFunc("/v1/launcher/slot/rollback", s.withAuth(s.handleRollback))

	s.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = s.httpServer.Serve(ln)
	}()
	return s, nil
}
func listenLoopbackWithRetry(addr string, maxWait time.Duration) (net.Listener, error) {
	deadline := time.Now().Add(maxWait)
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) || !time.Now().Before(deadline) {
			return nil, err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// BoundAddr returns the bound loopback host:port of the control listener.
func (s *SlotControlServer) BoundAddr() string {
	return s.boundAddr
}

// SwitchChan delivers at most one validated activate/rollback switch action.
func (s *SlotControlServer) SwitchChan() <-chan SlotSwitchAction {
	return s.switchCh
}

// Shutdown cancels any background prepare download, drains in-flight HTTP handlers,
// and closes the loopback listener so no open FD leaks across unix.Exec.
func (s *SlotControlServer) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	cancel := s.dlCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	err := s.httpServer.Shutdown(ctx)
	_ = s.listener.Close()
	s.dlWG.Wait()
	return err
}

func (s *SlotControlServer) runningVersion() string {
	if s.cfg.RunningSlot != "" {
		return s.cfg.RunningSlot
	}
	return s.cfg.Base.BaseVersion
}

func (s *SlotControlServer) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackPeerAddr(r.RemoteAddr) {
			writeSlotJSONError(w, http.StatusForbidden, SlotCodeUnauthorized, "non-loopback peer forbidden")
			return
		}
		authHeader := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(authHeader, prefix) {
			writeSlotJSONError(w, http.StatusUnauthorized, SlotCodeUnauthorized, "missing bearer token")
			return
		}
		provided := authHeader[len(prefix):]
		if len(provided) != len(s.cfg.LauncherToken) ||
			subtle.ConstantTimeCompare([]byte(provided), []byte(s.cfg.LauncherToken)) != 1 {
			writeSlotJSONError(w, http.StatusUnauthorized, SlotCodeUnauthorized, "invalid bearer token")
			return
		}
		next(w, r)
	}
}

func isLoopbackPeerAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func writeSlotJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeSlotJSONError(w http.ResponseWriter, status int, code, msg string) {
	writeSlotJSON(w, status, slotErrorEnvelope{
		Code:    code,
		Message: msg,
	})
}

func writeSlotErr(w http.ResponseWriter, err error, fallbackStatus int, fallbackCode string) {
	var se *SlotError
	if errors.As(err, &se) {
		msg := se.Message
		if msg == "" {
			msg = se.Error()
		}
		status := se.HTTPStatus
		if status == 0 {
			status = fallbackStatus
		}
		writeSlotJSONError(w, status, se.Code, msg)
		return
	}
	writeSlotJSONError(w, fallbackStatus, fallbackCode, err.Error())
}

func (s *SlotControlServer) snapshotPrepOpLocked() *SlotPrepareOperation {
	if s.prepOp == nil {
		return nil
	}
	cp := s.prepOp.clone()
	if cp.Phase == SlotPreparePhaseDownloading || cp.Phase == SlotPreparePhaseVerifying || cp.Phase == SlotPreparePhaseStaging {
		if recv := s.prepReceived.Load(); recv > cp.ReceivedBytes {
			cp.ReceivedBytes = recv
		}
	}
	return cp
}

func (s *SlotControlServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, "method must be GET")
		return
	}

	state, _, err := s.cfg.Store.ReadSlotStateIfExists()
	if err != nil {
		writeSlotJSONError(w, http.StatusInternalServerError, SlotCodeInternalError, err.Error())
		return
	}
	staged, err := s.cfg.Store.ListStagedSlots()
	if err != nil {
		writeSlotJSONError(w, http.StatusInternalServerError, SlotCodeInternalError, err.Error())
		return
	}
	if staged == nil {
		staged = []string{}
	}

	s.mu.Lock()
	prepOp := s.snapshotPrepOpLocked()
	var pending SlotSwitchAction
	hasPending := s.pendingSwitch != nil
	if hasPending {
		pending = *s.pendingSwitch
	}
	s.mu.Unlock()

	if hasPending && pending.Kind == SlotSwitchActivate && state.State != SlotStateTrial {
		state.State = SlotStateTrial
		state.ActiveAttemptID = pending.AttemptID
		state.LastAttemptID = pending.AttemptID
	}
	curSchema := state.SchemaGeneration
	if curSchema <= 0 {
		curSchema = 1
	}
	prevSchema := state.PreviousSchemaGeneration
	if prevSchema <= 0 {
		prevSchema = 1
	}

	var rollbackTarget string
	if state.ActiveSlot != "" {
		if state.PreviousSlot != "" {
			rollbackTarget = state.PreviousSlot
		} else {
			rollbackTarget = s.cfg.Base.BaseVersion
		}
	}

	sameSchema := curSchema == prevSchema
	rollbackAvailable := false
	rollbackBlockedReason := ""
	if (state.State == SlotStateCommitted || state.State == SlotStateRolledBack) && state.ActiveSlot != "" {
		if !sameSchema {
			rollbackBlockedReason = SlotCodeSchemaRollbackForbidden
		} else if state.PreviousSlot != "" {
			if _, statErr := os.Lstat(filepath.Join(s.cfg.Store.SlotsDir, state.PreviousSlot)); statErr == nil {
				rollbackAvailable = true
			}
		} else if fileExistsRegular(s.cfg.Base.LauncherExecutable) {
			rollbackAvailable = true
		}
	}

	resp := slotStatusResponse{
		Supported:                true,
		Mode:                     s.cfg.Mode,
		BaseVersion:              s.cfg.Base.BaseVersion,
		BaseEpoch:                s.cfg.Base.BaseEpoch,
		RunningVersion:           s.runningVersion(),
		ActiveSlot:               state.ActiveSlot,
		PreviousSlot:             state.PreviousSlot,
		RollbackTargetVersion:    rollbackTarget,
		RollbackAvailable:        rollbackAvailable,
		RollbackSameSchema:       sameSchema,
		RollbackBlockedReason:    rollbackBlockedReason,
		State:                    state.State,
		ActiveAttemptID:          state.ActiveAttemptID,
		LastAttemptID:            state.LastAttemptID,
		SchemaGeneration:         curSchema,
		PreviousSchemaGeneration: prevSchema,
		StagedSlots:              staged,
		PrepareOperation:         prepOp,
		LastFailureCode:          state.LastFailureCode,
		LastRolledBackSlot:       state.LastRolledBackSlot,
	}
	writeSlotJSON(w, http.StatusOK, resp)
}

func (s *SlotControlServer) validatePrepareTargetVersion(targetVersion string) error {
	if err := ValidateCanonicalSlotVersion(targetVersion); err != nil {
		return newSlotError(SlotCodeInvalidRequest, http.StatusBadRequest, err.Error(), nil)
	}
	runningVer := s.runningVersion()
	if _, ok := ParseSemVerV2(runningVer); ok {
		cmp, err := CompareSemVerV2(targetVersion, runningVer)
		if err != nil {
			return newSlotError(SlotCodeInvalidRequest, http.StatusBadRequest, err.Error(), nil)
		}
		if cmp <= 0 {
			return newSlotError(
				SlotCodeVersionConflict,
				http.StatusConflict,
				fmt.Sprintf("target_version %s must be newer than running version %s", targetVersion, runningVer),
				nil,
			)
		}
	} else if targetVersion == runningVer {
		return newSlotError(SlotCodeVersionConflict, http.StatusConflict, fmt.Sprintf("target_version %s is already running", targetVersion), nil)
	}
	if _, ok := ParseSemVerV2(s.cfg.Base.BaseVersion); ok {
		cmp, err := CompareSemVerV2(targetVersion, s.cfg.Base.BaseVersion)
		if err != nil {
			return newSlotError(SlotCodeInvalidRequest, http.StatusBadRequest, err.Error(), nil)
		}
		if cmp <= 0 {
			return newSlotError(
				SlotCodeVersionConflict,
				http.StatusConflict,
				fmt.Sprintf("target_version %s must be newer than base %s", targetVersion, s.cfg.Base.BaseVersion),
				nil,
			)
		}
	}
	return nil
}

func (s *SlotControlServer) tryAcquireBusy() error {
	state, _, err := s.cfg.Store.ReadSlotStateIfExists()
	if err != nil {
		return newSlotError(SlotCodeInternalError, http.StatusInternalServerError, err.Error(), nil)
	}
	if state.State == SlotStateTrial {
		return newSlotError(SlotCodeSlotBusy, http.StatusConflict, "slot trial is currently in progress", nil)
	}
	if state.State == SlotStateRecoveryRequired {
		return newSlotError(SlotCodeSlotBusy, http.StatusConflict, "slot recovery is required before new slot operations", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy || s.switching {
		return newSlotError(SlotCodeSlotBusy, http.StatusConflict, "another slot operation is already in progress", nil)
	}
	s.busy = true
	return nil
}

func (s *SlotControlServer) releaseBusy() {
	s.mu.Lock()
	s.busy = false
	s.dlCancel = nil
	s.mu.Unlock()
}

func decodeRequestJSONBody(r *http.Request, dst any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		return newSlotError(SlotCodeInvalidRequest, http.StatusBadRequest, "failed reading request body", err)
	}
	if err := strictUnmarshalJSON(raw, dst); err != nil {
		return newSlotError(SlotCodeInvalidRequest, http.StatusBadRequest, "malformed request JSON", err)
	}
	return nil
}

func (s *SlotControlServer) handlePrepare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, "method must be POST")
		return
	}
	var req slotPrepareRequest
	if err := decodeRequestJSONBody(r, &req); err != nil {
		writeSlotErr(w, err, http.StatusBadRequest, SlotCodeInvalidRequest)
		return
	}
	if err := s.validatePrepareTargetVersion(req.TargetVersion); err != nil {
		writeSlotErr(w, err, http.StatusBadRequest, SlotCodeInvalidRequest)
		return
	}
	if !isValidPrefixedSHA256(req.ExpectedSHA256) {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, "expected_sha256 must be sha256:<64-lowercase-hex>")
		return
	}
	if req.ExpectedSize <= 0 {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, "expected_size must be a positive integer")
		return
	}
	if req.ExpectedSize > MaxSlotArchiveBytes {
		writeSlotJSONError(w, http.StatusRequestEntityTooLarge, SlotCodeArchiveTooLarge, "expected_size exceeds 192 MiB limit")
		return
	}
	parsedURL, err := validateSlotArchiveHTTPSURL(req.ArchiveURL, s.cfg.ArchiveAddrPolicy)
	if err != nil {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, err.Error())
		return
	}

	if err := s.tryAcquireBusy(); err != nil {
		writeSlotErr(w, err, http.StatusConflict, SlotCodeSlotBusy)
		return
	}

	opSuffix, err := randomHexToken(6)
	if err != nil {
		s.releaseBusy()
		writeSlotJSONError(w, http.StatusInternalServerError, SlotCodeInternalError, "failed generating operation id")
		return
	}

	dlCtx, dlCancel := context.WithTimeout(context.Background(), slotDownloadTimeout)
	op := &SlotPrepareOperation{
		ID:            "slot-prep-" + opSuffix,
		TargetVersion: req.TargetVersion,
		Phase:         SlotPreparePhaseDownloading,
		ReceivedBytes: 0,
		TotalBytes:    req.ExpectedSize,
		ErrorCode:     "",
	}

	s.mu.Lock()
	s.prepReceived.Store(0)
	s.prepOp = op.clone()
	s.dlCancel = dlCancel
	s.dlWG.Add(1)
	respSnapshot := s.prepOp.clone()
	s.mu.Unlock()

	go func() {
		defer s.dlWG.Done()
		defer dlCancel()
		defer s.releaseBusy()
		s.runAsyncDownloadAndStage(dlCtx, parsedURL.String(), req)
	}()

	writeSlotJSON(w, http.StatusAccepted, respSnapshot)
}

func (s *SlotControlServer) runAsyncDownloadAndStage(ctx context.Context, archiveURL string, req slotPrepareRequest) {
	if err := s.cfg.Store.EnsureStoreDirs(); err != nil {
		s.failPrepareOp(SlotCodeInternalError)
		return
	}

	suffix, err := randomHexToken(8)
	if err != nil {
		s.failPrepareOp(SlotCodeInternalError)
		return
	}
	tmpArchivePath := filepath.Join(s.cfg.Store.SlotsDir, ".slot-dl-"+suffix+".tar.gz")
	defer func() { _ = os.Remove(tmpArchivePath) }()

	client := s.newSSRFProtectedHTTPClient()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		s.failPrepareOp(SlotCodeInvalidRequest)
		return
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		s.failPrepareOp(SlotCodeDownloadFailed)
		return
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		s.failPrepareOp(SlotCodeDownloadFailed)
		return
	}
	if resp.ContentLength > MaxSlotArchiveBytes {
		_ = resp.Body.Close()
		s.failPrepareOp(SlotCodeArchiveTooLarge)
		return
	}

	tmpFile, err := os.OpenFile(tmpArchivePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		_ = resp.Body.Close()
		s.failPrepareOp(SlotCodeInternalError)
		return
	}
	defer func() { _ = tmpFile.Close() }()

	dlMeter := &archiveMeterReader{
		r:        resp.Body,
		maxBytes: MaxSlotArchiveBytes,
		hasher:   ioDiscardHash{},
		onProgress: func(n int64) {
			s.prepReceived.Store(n)
		},
	}
	written, copyErr := io.Copy(tmpFile, dlMeter)
	_ = resp.Body.Close()
	if copyErr != nil {
		if dlMeter.overflowed {
			s.failPrepareOp(SlotCodeArchiveTooLarge)
			return
		}
		s.failPrepareOp(SlotCodeDownloadFailed)
		return
	}
	if written != req.ExpectedSize {
		s.failPrepareOp(SlotCodeArchiveInvalid)
		return
	}
	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		s.failPrepareOp(SlotCodeInternalError)
		return
	}

	s.mu.Lock()
	if s.prepOp != nil {
		s.prepOp.Phase = SlotPreparePhaseVerifying
		s.prepOp.ReceivedBytes = written
	}
	s.mu.Unlock()

	if _, _, stageErr := s.cfg.Store.StageSlotArchive(tmpFile, req.TargetVersion, req.ExpectedSHA256, req.ExpectedSize, nil); stageErr != nil {
		s.failPrepareOp(slotErrorCode(stageErr, SlotCodeArchiveInvalid))
		return
	}

	s.mu.Lock()
	if s.prepOp != nil {
		s.prepOp.Phase = SlotPreparePhaseSucceeded
		s.prepOp.ReceivedBytes = written
		s.prepOp.TotalBytes = written
		s.prepOp.ErrorCode = ""
	}
	s.mu.Unlock()
}

type ioDiscardHash struct{}

func (ioDiscardHash) Write(p []byte) (int, error) { return len(p), nil }
func (ioDiscardHash) Sum(b []byte) []byte         { return b }
func (ioDiscardHash) Reset()                      {}
func (ioDiscardHash) Size() int                   { return 0 }
func (ioDiscardHash) BlockSize() int              { return 64 }

func (s *SlotControlServer) failPrepareOp(code string) {
	s.mu.Lock()
	if s.prepOp != nil {
		if recv := s.prepReceived.Load(); recv > s.prepOp.ReceivedBytes {
			s.prepOp.ReceivedBytes = recv
		}
		s.prepOp.Phase = SlotPreparePhaseFailed
		s.prepOp.ErrorCode = code
	}
	s.mu.Unlock()
}

func (s *SlotControlServer) handlePrepareUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, "method must be PUT")
		return
	}
	q := r.URL.Query()
	targetVersion := strings.TrimSpace(q.Get("target_version"))
	expectedSHA256 := strings.TrimSpace(q.Get("expected_sha256"))

	if err := s.validatePrepareTargetVersion(targetVersion); err != nil {
		writeSlotErr(w, err, http.StatusBadRequest, SlotCodeInvalidRequest)
		return
	}
	if expectedSHA256 != "" && !isValidPrefixedSHA256(expectedSHA256) {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, "invalid expected_sha256 query parameter")
		return
	}
	ct := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if ct != "" && ct != "application/gzip" && ct != "application/x-gzip" && ct != "application/octet-stream" {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, "Content-Type must be application/gzip or application/octet-stream")
		return
	}
	if r.ContentLength > MaxSlotArchiveBytes {
		writeSlotJSONError(w, http.StatusRequestEntityTooLarge, SlotCodeArchiveTooLarge, "upload Content-Length exceeds 192 MiB limit")
		return
	}

	if err := s.tryAcquireBusy(); err != nil {
		writeSlotErr(w, err, http.StatusConflict, SlotCodeSlotBusy)
		return
	}
	defer s.releaseBusy()

	opSuffix, err := randomHexToken(6)
	if err != nil {
		writeSlotJSONError(w, http.StatusInternalServerError, SlotCodeInternalError, "failed generating operation id")
		return
	}

	var totalBytes int64
	if r.ContentLength > 0 {
		totalBytes = r.ContentLength
	}
	s.mu.Lock()
	s.prepReceived.Store(0)
	s.prepOp = &SlotPrepareOperation{
		ID:            "slot-upload-" + opSuffix,
		TargetVersion: targetVersion,
		Phase:         SlotPreparePhaseStaging,
		ReceivedBytes: 0,
		TotalBytes:    totalBytes,
		ErrorCode:     "",
	}
	s.mu.Unlock()

	_, written, stageErr := s.cfg.Store.StageSlotArchive(
		r.Body,
		targetVersion,
		expectedSHA256,
		0,
		func(n int64) {
			s.prepReceived.Store(n)
		},
	)
	if stageErr != nil {
		code := slotErrorCode(stageErr, SlotCodeArchiveInvalid)
		s.failPrepareOp(code)
		writeSlotErr(w, stageErr, slotErrorStatus(stageErr, http.StatusUnprocessableEntity), code)
		return
	}

	s.mu.Lock()
	s.prepOp.Phase = SlotPreparePhaseSucceeded
	s.prepOp.ReceivedBytes = written
	s.prepOp.TotalBytes = written
	s.prepOp.ErrorCode = ""
	respOp := s.prepOp.clone()
	s.mu.Unlock()

	writeSlotJSON(w, http.StatusOK, respOp)
}

func (s *SlotControlServer) handleActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, "method must be POST")
		return
	}
	var req slotActivateRequest
	if err := decodeRequestJSONBody(r, &req); err != nil {
		writeSlotErr(w, err, http.StatusBadRequest, SlotCodeInvalidRequest)
		return
	}
	if err := s.validatePrepareTargetVersion(req.TargetVersion); err != nil {
		writeSlotErr(w, err, http.StatusBadRequest, SlotCodeInvalidRequest)
		return
	}
	if req.AttemptID != "" && !isValidSlotAttemptID(req.AttemptID) {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, "attempt_id must match ^attempt-[0-9a-f]{16}$")
		return
	}
	if req.PreviousSchemaGeneration < 0 {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, "previous_schema_generation must be >= 1")
		return
	}
	prevSchemaGen := req.PreviousSchemaGeneration
	if prevSchemaGen == 0 {
		prevSchemaGen = 1
	}
	snapshotDir := strings.TrimSpace(req.SnapshotDir)
	if snapshotDir == "" {
		writeSlotJSONError(w, http.StatusUnprocessableEntity, SlotCodeSnapshotInvalid, "snapshot_dir is required for slot activation")
		return
	}

	if err := s.tryAcquireBusy(); err != nil {
		writeSlotErr(w, err, http.StatusConflict, SlotCodeSlotBusy)
		return
	}
	committedSwitch := false
	defer func() {
		if !committedSwitch {
			s.releaseBusy()
		}
	}()

	curState, curExists, stateErr := s.cfg.Store.ReadSlotStateIfExists()
	if stateErr != nil {
		writeSlotJSONError(w, http.StatusInternalServerError, SlotCodeInternalError, stateErr.Error())
		return
	}
	if curExists && curState.State != SlotStateNone && curState.SchemaGeneration > prevSchemaGen {
		prevSchemaGen = curState.SchemaGeneration
	}

	manifest, err := s.cfg.Store.VerifyInstalledSlot(req.TargetVersion)
	if err != nil {
		writeSlotErr(w, err, http.StatusUnprocessableEntity, SlotCodeArchiveInvalid)
		return
	}
	if manifest.SchemaGeneration < prevSchemaGen {
		writeSlotJSONError(
			w,
			http.StatusConflict,
			SlotCodeSchemaRollbackForbidden,
			fmt.Sprintf("target slot schema_generation %d is older than active schema_generation %d", manifest.SchemaGeneration, prevSchemaGen),
		)
		return
	}

	if err := ValidateManagerDataSnapshotDir(snapshotDir, s.cfg.DataDir, s.cfg.ManagerUID); err != nil {
		writeSlotErr(w, err, http.StatusUnprocessableEntity, SlotCodeSnapshotInvalid)
		return
	}
	s.mu.Lock()
	beforeHook := s.beforeActivateReserveHook
	s.mu.Unlock()
	if beforeHook != nil {
		beforeHook()
	}

	attemptID := req.AttemptID
	if attemptID == "" {
		attemptSuffix, genErr := randomHexToken(8)
		if genErr != nil {
			writeSlotJSONError(w, http.StatusInternalServerError, SlotCodeInternalError, "failed generating attempt id")
			return
		}
		attemptID = "attempt-" + attemptSuffix
	}
	targetLauncher := filepath.Join(s.cfg.Store.SlotsDir, req.TargetVersion, "bin", "cpamp-launcher")
	action := SlotSwitchAction{
		Kind:                     SlotSwitchActivate,
		AttemptID:                attemptID,
		TargetVersion:            req.TargetVersion,
		SnapshotDir:              snapshotDir,
		TargetSchemaGeneration:   manifest.SchemaGeneration,
		PreviousSchemaGeneration: prevSchemaGen,
		TargetLauncherPath:       targetLauncher,
	}

	s.mu.Lock()
	_, aborted := s.abortedAttempts[attemptID]
	if r.Context().Err() != nil || aborted {
		s.mu.Unlock()
		writeSlotJSONError(w, http.StatusConflict, SlotCodeSlotConflict, "activation attempt was canceled or aborted")
		return
	}
	s.switching = true
	reserved := action
	s.pendingSwitch = &reserved
	hook := s.afterActivateReserveHook
	s.mu.Unlock()
	committedSwitch = true
	if hook != nil && hook(w) {
		select {
		case s.switchCh <- action:
		default:
		}
		return
	}

	timeoutSec := int(math.Ceil(s.cfg.TrialTimeout.Seconds()))
	if timeoutSec < 1 {
		timeoutSec = 1
	}
	w.Header().Set("Connection", "close")
	writeSlotJSON(w, http.StatusAccepted, SlotActionAcceptedResponse{
		Accepted:            true,
		TargetVersion:       req.TargetVersion,
		TrialTimeoutSeconds: timeoutSec,
		AttemptID:           attemptID,
	})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	select {
	case s.switchCh <- action:
	default:
	}
}

func (s *SlotControlServer) recordAbortedAttemptLocked(attemptID string) {
	if s.abortedAttempts == nil {
		s.abortedAttempts = make(map[string]struct{})
	}
	if _, exists := s.abortedAttempts[attemptID]; exists {
		return
	}
	if len(s.abortedAttemptsOrder) >= maxAbortedSlotAttempts {
		evict := s.abortedAttemptsOrder[0]
		copy(s.abortedAttemptsOrder, s.abortedAttemptsOrder[1:])
		s.abortedAttemptsOrder = s.abortedAttemptsOrder[:maxAbortedSlotAttempts-1]
		delete(s.abortedAttempts, evict)
	}
	s.abortedAttempts[attemptID] = struct{}{}
	s.abortedAttemptsOrder = append(s.abortedAttemptsOrder, attemptID)
}

func (s *SlotControlServer) handleAbortAttempt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, "method must be POST")
		return
	}
	var req slotAbortAttemptRequest
	if err := decodeRequestJSONBody(r, &req); err != nil {
		writeSlotErr(w, err, http.StatusBadRequest, SlotCodeInvalidRequest)
		return
	}
	attemptID := strings.TrimSpace(req.AttemptID)
	if !isValidSlotAttemptID(attemptID) {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, "attempt_id must match ^attempt-[0-9a-f]{16}$")
		return
	}
	s.mu.Lock()
	state, _, err := s.cfg.Store.ReadSlotStateIfExists()
	if err != nil {
		s.mu.Unlock()
		writeSlotJSONError(w, http.StatusInternalServerError, SlotCodeInternalError, err.Error())
		return
	}
	accepted := (s.pendingSwitch != nil && s.pendingSwitch.AttemptID == attemptID) ||
		state.ActiveAttemptID == attemptID ||
		state.LastAttemptID == attemptID
	outcome := SlotAbortOutcomeAccepted
	if !accepted {
		s.recordAbortedAttemptLocked(attemptID)
		outcome = SlotAbortOutcomeAborted
	}
	s.mu.Unlock()

	writeSlotJSON(w, http.StatusOK, slotAbortAttemptResponse{
		AttemptID: attemptID,
		Outcome:   outcome,
	})
}

func (s *SlotControlServer) handleRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, "method must be POST")
		return
	}
	var req slotRollbackRequest
	if err := decodeRequestJSONBody(r, &req); err != nil {
		writeSlotErr(w, err, http.StatusBadRequest, SlotCodeInvalidRequest)
		return
	}
	if err := ValidateCanonicalSlotVersion(req.ExpectedActiveSlot); err != nil {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, err.Error())
		return
	}
	if req.CurrentSchemaGeneration < 0 {
		writeSlotJSONError(w, http.StatusBadRequest, SlotCodeInvalidRequest, "current_schema_generation must be >= 1")
		return
	}

	if err := s.tryAcquireBusy(); err != nil {
		writeSlotErr(w, err, http.StatusConflict, SlotCodeSlotBusy)
		return
	}
	committedSwitch := false
	defer func() {
		if !committedSwitch {
			s.releaseBusy()
		}
	}()

	state, exists, err := s.cfg.Store.ReadSlotStateIfExists()
	if err != nil {
		writeSlotJSONError(w, http.StatusInternalServerError, SlotCodeInternalError, err.Error())
		return
	}
	if !exists || (state.State != SlotStateCommitted && state.State != SlotStateRolledBack) || state.ActiveSlot == "" || state.ActiveSlot != req.ExpectedActiveSlot {
		writeSlotJSONError(w, http.StatusConflict, SlotCodeVersionConflict, "active committed slot does not match expected_active_slot")
		return
	}

	prevGen := state.PreviousSchemaGeneration
	if prevGen <= 0 {
		prevGen = 1
	}
	curStateGen := state.SchemaGeneration
	if curStateGen <= 0 {
		curStateGen = 1
	}
	curReqGen := req.CurrentSchemaGeneration
	if curReqGen == 0 {
		curReqGen = curStateGen
	}
	if curStateGen != prevGen || curReqGen != prevGen {
		writeSlotJSONError(
			w,
			http.StatusConflict,
			SlotCodeSchemaRollbackForbidden,
			"cross-schema online rollback is forbidden after trial commit; use cpa-manager-plus restore-pre-migrate",
		)
		return
	}

	var (
		targetLauncher string
		targetVersion  string
	)
	if state.PreviousSlot != "" {
		if _, err := s.cfg.Store.VerifyInstalledSlot(state.PreviousSlot); err != nil {
			writeSlotErr(w, err, http.StatusUnprocessableEntity, SlotCodeArchiveInvalid)
			return
		}
		targetLauncher = filepath.Join(s.cfg.Store.SlotsDir, state.PreviousSlot, "bin", "cpamp-launcher")
		targetVersion = state.PreviousSlot
	} else {
		if !fileExistsRegular(s.cfg.Base.LauncherExecutable) {
			writeSlotJSONError(w, http.StatusInternalServerError, SlotCodeInternalError, "base launcher executable is unavailable")
			return
		}
		targetLauncher = s.cfg.Base.LauncherExecutable
		targetVersion = s.cfg.Base.BaseVersion
	}

	action := SlotSwitchAction{
		Kind:                     SlotSwitchRollback,
		ExpectedActiveSlot:       req.ExpectedActiveSlot,
		RollbackTargetSlot:       state.PreviousSlot,
		RollbackSchemaGeneration: prevGen,
		TargetLauncherPath:       targetLauncher,
	}

	s.mu.Lock()
	s.switching = true
	reserved := action
	s.pendingSwitch = &reserved
	s.mu.Unlock()
	committedSwitch = true

	w.Header().Set("Connection", "close")
	writeSlotJSON(w, http.StatusAccepted, slotRollbackResponse{
		Accepted:      true,
		TargetVersion: targetVersion,
	})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	select {
	case s.switchCh <- action:
	default:
	}
}

func isValidSlotAttemptID(raw string) bool {
	const prefix = "attempt-"
	if len(raw) != len(prefix)+16 || !strings.HasPrefix(raw, prefix) {
		return false
	}
	for i := len(prefix); i < len(raw); i++ {
		c := raw[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validateSlotArchiveHTTPSURL(rawURL string, addrPolicy func(netip.Addr) bool) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" || len(trimmed) > 4096 {
		return nil, errors.New("archive_url must be a non-empty HTTPS URL")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid archive_url: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return nil, errors.New("archive_url must use https scheme")
	}
	if parsed.User != nil {
		return nil, errors.New("archive_url must not contain user credentials")
	}
	if parsed.Fragment != "" {
		return nil, errors.New("archive_url must not contain a fragment")
	}
	host := parsed.Hostname()
	if host == "" || strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return nil, errors.New("archive_url host is not permitted")
	}
	if addrPolicy == nil {
		addrPolicy = update.IsPublicCDNAddr
	}
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		if ip.Zone() != "" || !addrPolicy(ip.Unmap()) {
			return nil, fmt.Errorf("archive_url literal IP %s is disallowed by address policy", ip)
		}
	}
	if portStr := parsed.Port(); portStr != "" {
		port, convErr := strconv.Atoi(portStr)
		if convErr != nil || port < 1 || port > 65535 {
			return nil, errors.New("archive_url port is invalid")
		}
	}
	return parsed, nil
}

func (s *SlotControlServer) newSSRFProtectedHTTPClient() *http.Client {
	policy := s.cfg.ArchiveAddrPolicy
	if policy == nil {
		policy = update.IsPublicCDNAddr
	}
	lookupIP := s.cfg.ArchiveLookupIP
	if lookupIP == nil {
		lookupIP = net.DefaultResolver.LookupNetIP
	}

	dialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || ip.Zone() != "" || !policy(ip.Unmap()) {
				return fmt.Errorf("blocked dial to disallowed IP %q", host)
			}
			return nil
		},
	}

	var tlsCfg *tls.Config
	if s.cfg.ArchiveTLSRootCAs != nil {
		tlsCfg = &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    s.cfg.ArchiveTLSRootCAs,
		}
	} else {
		tlsCfg = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}

	transport := &http.Transport{
		Proxy:           nil,
		TLSClientConfig: tlsCfg,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			var candidates []netip.Addr
			if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
				if ip.Zone() != "" || !policy(ip.Unmap()) {
					return nil, fmt.Errorf("disallowed archive IP %s", ip)
				}
				candidates = []netip.Addr{ip.Unmap()}
			} else {
				addrs, dnsErr := lookupIP(ctx, "ip", host)
				if dnsErr != nil || len(addrs) == 0 {
					return nil, fmt.Errorf("failed resolving archive host %q: %w", host, dnsErr)
				}
				for _, resolved := range addrs {
					unmapped := resolved.Unmap()
					if resolved.Zone() != "" || !policy(unmapped) {
						return nil, fmt.Errorf("archive host %q resolved to disallowed IP %s", host, resolved)
					}
					candidates = append(candidates, unmapped)
				}
			}

			var lastErr error
			for _, candidate := range candidates {
				target := net.JoinHostPort(candidate.String(), port)
				conn, dialErr := dialer.DialContext(ctx, network, target)
				if dialErr != nil {
					lastErr = dialErr
					continue
				}
				if tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
					if remoteIP, ok := netip.AddrFromSlice(tcpAddr.IP); !ok || !policy(remoteIP.Unmap()) {
						_ = conn.Close()
						return nil, fmt.Errorf("disallowed remote peer %v", conn.RemoteAddr())
					}
				}
				return conn, nil
			}
			if lastErr == nil {
				lastErr = errors.New("no valid IP addresses to dial")
			}
			return nil, lastErr
		},
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableKeepAlives:     true,
	}

	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxSlotDownloadRedirects {
				return fmt.Errorf("stopped after %d redirects", maxSlotDownloadRedirects)
			}
			if _, err := validateSlotArchiveHTTPSURL(req.URL.String(), policy); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			return nil
		},
	}
}
