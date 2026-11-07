package cpaupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

const (
	discoverySchemaVersion = 1
	discoveryCooldown      = time.Minute
	discoveryFreshness     = 7 * time.Hour
	maxPersistedErrorBytes = 512
)

type Store interface {
	LoadCPAUpdateCheck(context.Context) ([]byte, error)
	SaveCPAUpdateCheck(context.Context, []byte) error
}

type DiscoveryState struct {
	SchemaVersion int       `json:"schema_version"`
	LastAttemptAt time.Time `json:"last_attempt_at"`
	LastSuccessAt time.Time `json:"last_success_at"`
	LastError     string    `json:"last_error,omitempty"`
	TargetVersion string    `json:"target_version,omitempty"`
}

type StatusState string

const (
	StateNeverChecked      StatusState = "never_checked"
	StateUpToDate          StatusState = "up_to_date"
	StateUpdateAvailable   StatusState = "update_available"
	StateAheadOfStable     StatusState = "ahead_of_stable"
	StateUnknownVersion    StatusState = "unknown_version"
	StateUnsupported       StatusState = "unsupported"
	StateManagedExternally StatusState = "managed_externally"
)

type Status struct {
	Mode              model.RuntimeMode `json:"mode"`
	State             StatusState       `json:"state"`
	CurrentVersion    string            `json:"current_version,omitempty"`
	ActiveArtifactID  string            `json:"active_artifact_id,omitempty"`
	TargetVersion     string            `json:"target_version,omitempty"`
	LastSuccessAt     time.Time         `json:"last_success_at"`
	LastError         string            `json:"last_error,omitempty"`
	Stale             bool              `json:"stale"`
	PrepareSupported  bool              `json:"prepare_supported"`
	ActivateSupported bool              `json:"activate_supported"`
	Actionable        bool              `json:"actionable"`
}

type Service struct {
	mu                sync.Mutex
	store             Store
	runtime           runtimeservice.RuntimeClient
	mode              model.RuntimeMode
	modeResolver      func() model.RuntimeMode
	source            stableReleaseSource
	now               func() time.Time
	loaded            bool
	persistenceFailed bool
	state             DiscoveryState
}

func New(store Store, runtimeClient runtimeservice.RuntimeClient, mode model.RuntimeMode) *Service {
	return &Service{
		store:   store,
		runtime: runtimeClient,
		mode:    mode,
		source:  newOfficialReleaseSource(),
		now:     time.Now,
	}
}

func (s *Service) WithModeResolver(resolve func() model.RuntimeMode) *Service {
	s.modeResolver = resolve
	return s
}

func (s *Service) currentMode() model.RuntimeMode {
	if s.modeResolver != nil {
		return s.modeResolver()
	}
	return s.mode
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx); err != nil {
		return Status{}, err
	}
	if s.persistenceFailed {
		return Status{}, errors.New("CPA update discovery state is not durable")
	}
	return s.project(ctx)
}

func (s *Service) Check(ctx context.Context) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx); err != nil {
		return Status{}, err
	}
	if err := s.checkDiscovery(ctx); err != nil {
		return Status{}, err
	}
	return s.project(ctx)
}

func (s *Service) load(ctx context.Context) error {
	if s.loaded {
		return nil
	}
	if s.store == nil {
		return errors.New("CPA update discovery store is unavailable")
	}
	data, err := s.store.LoadCPAUpdateCheck(ctx)
	if err != nil {
		return fmt.Errorf("load CPA update discovery state: %w", err)
	}
	state := DiscoveryState{SchemaVersion: discoverySchemaVersion}
	if len(data) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&state); err != nil {
			return errors.New("invalid persisted CPA update discovery state")
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return errors.New("invalid trailing persisted CPA update discovery state")
		}
		if err := validateDiscoveryState(state); err != nil {
			return fmt.Errorf("unsupported persisted CPA update discovery state: %w", err)
		}
	}
	s.state = state
	s.loaded = true
	return nil
}

func validateDiscoveryState(state DiscoveryState) error {
	if state.SchemaVersion != discoverySchemaVersion {
		return fmt.Errorf("schema version %d", state.SchemaVersion)
	}
	if state.LastAttemptAt.IsZero() {
		if !state.LastSuccessAt.IsZero() || state.LastError != "" || state.TargetVersion != "" {
			return errors.New("discovery evidence requires last_attempt_at")
		}
	}
	if state.LastSuccessAt.IsZero() != (state.TargetVersion == "") {
		return errors.New("target_version and last_success_at must be present together")
	}
	if state.TargetVersion != "" {
		if _, err := parseStableVersion(state.TargetVersion); err != nil {
			return errors.New("target_version is not canonical stable semver")
		}
	}
	if state.LastError != "" {
		if state.LastError != strings.TrimSpace(state.LastError) ||
			!utf8.ValidString(state.LastError) || len([]byte(state.LastError)) > maxPersistedErrorBytes {
			return errors.New("last_error has invalid shape")
		}
	}
	if !state.LastSuccessAt.IsZero() {
		if state.LastError == "" && state.LastSuccessAt.Before(state.LastAttemptAt) {
			return errors.New("successful discovery timestamp precedes last attempt")
		}
		if state.LastError != "" && state.LastAttemptAt.Before(state.LastSuccessAt) {
			return errors.New("failed discovery timestamp precedes last success")
		}
	}
	return nil
}

func (s *Service) checkDiscovery(ctx context.Context) error {
	now := s.now().UTC()
	if !s.state.LastAttemptAt.IsZero() {
		elapsed := now.Sub(s.state.LastAttemptAt)
		if elapsed >= 0 && elapsed < discoveryCooldown {
			if !s.persistenceFailed {
				return nil
			}
			return s.save(ctx)
		}
	}
	s.state.LastAttemptAt = now
	if s.source == nil {
		s.state.LastError = "official CPA release source is unavailable"
		return s.save(ctx)
	}
	target, err := s.source.LatestStable(ctx)
	if err != nil {
		s.state.LastError = boundedError(err)
	} else {
		if _, parseErr := parseStableVersion(target); parseErr != nil {
			s.state.LastError = "official CPA release source returned an invalid stable version"
		} else {
			s.state.TargetVersion = target
			s.state.LastSuccessAt = now
			s.state.LastError = ""
		}
	}
	return s.save(ctx)
}

func (s *Service) save(ctx context.Context) error {
	if err := validateDiscoveryState(s.state); err != nil {
		return fmt.Errorf("validate CPA update discovery state: %w", err)
	}
	data, err := json.Marshal(s.state)
	if err != nil {
		return fmt.Errorf("encode CPA update discovery state: %w", err)
	}
	if err := s.store.SaveCPAUpdateCheck(ctx, data); err != nil {
		s.persistenceFailed = true
		return fmt.Errorf("save CPA update discovery state: %w", err)
	}
	s.persistenceFailed = false
	return nil
}

func boundedError(err error) string {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "CPA update discovery failed"
	}
	if len([]byte(message)) <= maxPersistedErrorBytes {
		return message
	}
	for len([]byte(message)) > maxPersistedErrorBytes {
		_, size := utf8.DecodeLastRuneInString(message)
		message = message[:len(message)-size]
	}
	return strings.TrimSpace(message)
}

func (s *Service) project(ctx context.Context) (Status, error) {
	mode := s.currentMode()
	if !mode.IsValid() {
		return Status{}, errors.New("CPA update Runtime mode is invalid")
	}
	if s.runtime == nil {
		return Status{}, errors.New("CPA update Runtime client is unavailable")
	}
	observed, err := s.runtime.Status(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("observe CPA Runtime for update status: %w", err)
	}
	status := Status{
		Mode:          mode,
		State:         StateNeverChecked,
		TargetVersion: s.state.TargetVersion,
		LastSuccessAt: s.state.LastSuccessAt,
		LastError:     s.state.LastError,
		Stale:         discoveryIsStale(s.state, s.now()),
	}
	if mode == model.RuntimeModeExternal {
		if err := observed.Validate(); err != nil {
			return Status{}, fmt.Errorf("validate CPA Runtime update observation: %w", err)
		}
		status.State = StateManagedExternally
		status.CurrentVersion = string(observed.CPAObservedVersion)
		return status, nil
	}

	status.PrepareSupported = observed.Capabilities.Supports(model.RuntimeCapabilityPrepareUpdate)
	status.ActivateSupported = observed.Capabilities.Supports(model.RuntimeCapabilityActivateUpdate)
	artifact := observed.ActiveGatewayArtifact
	if artifact == nil {
		status.State = StateUnsupported
		return status, nil
	}
	if err := observed.Validate(); err != nil {
		return Status{}, fmt.Errorf("validate CPA Runtime update observation: %w", err)
	}
	status.CurrentVersion = artifact.Version
	status.ActiveArtifactID = string(artifact.ArtifactID)
	current, err := parseStableVersion(artifact.Version)
	if err != nil {
		status.State = StateUnknownVersion
		return status, nil
	}
	if s.state.TargetVersion == "" {
		return status, nil
	}
	target, err := parseStableVersion(s.state.TargetVersion)
	if err != nil {
		return Status{}, errors.New("persisted CPA update target is invalid")
	}
	switch compareStableVersions(target, current) {
	case -1:
		status.State = StateAheadOfStable
	case 0:
		status.State = StateUpToDate
	case 1:
		status.State = StateUpdateAvailable
		status.Actionable = !status.Stale && status.PrepareSupported && status.ActivateSupported
	}
	return status, nil
}

func discoveryIsStale(state DiscoveryState, now time.Time) bool {
	if state.LastSuccessAt.IsZero() || state.LastError != "" || now.Before(state.LastSuccessAt) {
		return true
	}
	return now.Sub(state.LastSuccessAt) > discoveryFreshness
}
