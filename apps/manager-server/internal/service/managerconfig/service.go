package managerconfig

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type Source string

const (
	SourceNone Source = ""
	SourceEnv  Source = "env"
	SourceDB   Source = "db"
)

type Response struct {
	Config   PublicManagerConfig `json:"config"`
	Source   string              `json:"source"`
	CPAUsage *cpa.UsageConfig    `json:"cpaUsage,omitempty"`
}

type PublicManagerConfig struct {
	CPAConnection        PublicManagerCPAConnectionConfig        `json:"cpaConnection"`
	Collector            store.ManagerCollectorConfig            `json:"collector"`
	CodexInspection      store.ManagerCodexInspectionConfig      `json:"codexInspection"`
	ExternalUsageService store.ManagerExternalUsageServiceConfig `json:"externalUsageService"`
	Retention            store.ManagerRetentionConfig            `json:"retention,omitempty"`
	UpdatedAtMS          int64                                   `json:"updatedAtMs,omitempty"`
}

type PublicManagerCPAConnectionConfig struct {
	CPABaseURL              string `json:"cpaBaseUrl"`
	ManagementKeyConfigured bool   `json:"managementKeyConfigured"`
}

type Service struct {
	cfg                        config.Config
	store                      *store.Store
	collector                  *collectorservice.Service
	externalCollectorLifecycle bool
	collectorMu                sync.Mutex
	cpaUsageProbeAllowed       func(context.Context) bool
	retentionChangedNotifier   func()
}

func New(cfg config.Config, store *store.Store, collector *collectorservice.Service) *Service {
	return &Service{
		cfg:       cfg,
		store:     store,
		collector: collector,
	}
}

// UseExternalCollectorLifecycle delegates Collector start/stop to an enclosing
// host (Fresh) so neither Update nor SetCollectorEnabled starts or stops a
// detached background Collector or mutates CPA usage-statistics settings, while
// read-only CPA collector retention validation still runs when monitoring is
// enabled and CPA usage probing is permitted.
func (s *Service) UseExternalCollectorLifecycle() {
	if s != nil {
		s.externalCollectorLifecycle = true
	}
}

// SetCPAUsageProbeAllowed configures whether Get may probe the persisted CPA
// management endpoint for optional live usage-statistics metadata. When allow
// returns false (for example on ConnectExternal, where External usage posture
// is owned by GET external/usage), Get answers purely from Manager's persisted
// configuration without presenting the saved management credential upstream.
func (s *Service) SetCPAUsageProbeAllowed(allow func(context.Context) bool) {
	if s != nil {
		s.cpaUsageProbeAllowed = allow
	}
}

func (s *Service) SetRetentionChangedNotifier(fn func()) {
	if s != nil {
		s.retentionChangedNotifier = fn
	}
}

func (s *Service) Get(ctx context.Context) (Response, error) {
	cfg, source, _, err := s.ResolveManagerConfigWithSource(ctx)
	if err != nil {
		return Response{}, err
	}
	var cpaUsage *cpa.UsageConfig
	if cfg.CPAConnection.CPABaseURL != "" && cfg.CPAConnection.ManagementKey != "" &&
		(s.cpaUsageProbeAllowed == nil || s.cpaUsageProbeAllowed(ctx)) {
		if usageCfg, err := cpa.FetchUsageConfig(
			ctx,
			cfg.CPAConnection.CPABaseURL,
			cfg.CPAConnection.ManagementKey,
		); err == nil {
			cpaUsage = &usageCfg
		}
	}
	return Response{
		Config:   PublicConfig(cfg),
		Source:   string(source),
		CPAUsage: cpaUsage,
	}, nil
}

// CPAConnectionValidation is the result of a strict server-side validation of
// the persisted CPA connection. Unlike Get, it propagates every CPA Management
// API failure so callers (the installer) cannot mistake a tolerant config read
// for a real connection check.
type CPAConnectionValidation struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source,omitempty"`
	CPABaseURL string `json:"cpaBaseUrl,omitempty"`
}

// ValidateCPAConnection resolves the persisted CPA connection Manager Server
// must use after import and performs a strict
// cpa.ValidateManagementAPI call against it. It never accepts a client-supplied
// management key and never swallows upstream auth/network/5xx failures: any
// non-success response from CPA is returned as an error so the caller fails
// closed instead of committing a migration on a false positive.
func (s *Service) ValidateCPAConnection(ctx context.Context) (CPAConnectionValidation, error) {
	connection, source, found, err := s.ResolvePersistedCPAConnection(ctx)
	if err != nil {
		return CPAConnectionValidation{}, err
	}
	baseURL := cpa.NormalizeBaseURL(connection.BaseURL)
	key := strings.TrimSpace(connection.ManagementKey)
	if !found || baseURL == "" || key == "" {
		return CPAConnectionValidation{Configured: false, Source: string(source)}, nil
	}
	if err := cpa.ValidateManagementAPI(ctx, baseURL, key); err != nil {
		return CPAConnectionValidation{Configured: true, Source: string(source), CPABaseURL: baseURL}, err
	}
	return CPAConnectionValidation{Configured: true, Source: string(source), CPABaseURL: baseURL}, nil
}

func (s *Service) Update(ctx context.Context, submitted store.ManagerConfig) (Response, error) {
	// Probing/mutating CPA is gated below by s.cpaUsageProbeAllowed and
	// s.externalCollectorLifecycle rather than rejecting local config updates.
	current, source, _, err := s.ResolveManagerConfigWithSource(ctx)
	if err != nil {
		return Response{}, err
	}
	saved, savedOK, err := s.store.LoadManagerConfig(ctx)
	if err != nil {
		return Response{}, err
	}
	if err := validateSubmittedCollectorConfig(&submitted.Collector); err != nil {
		return Response{}, err
	}
	if err := model.ValidateCodexInspectionConfig(submitted.CodexInspection); err != nil {
		return Response{}, err
	}
	envDays := PositiveOrDefault(s.cfg.UsageArchiveRetentionDays, 30, 30)
	retentionDaysSubmitted := submitted.Retention.RetentionDaysExplicit || submitted.Retention.RetentionDays != 0
	if submitted.Retention.Enabled != nil && s.retentionEnabledEnvLocked() &&
		*submitted.Retention.Enabled != s.cfg.UsageArchiveRetentionEnabled {
		return Response{}, errors.New("retention settings are managed by environment variables")
	}
	if retentionDaysSubmitted && s.retentionDaysEnvLocked() &&
		submitted.Retention.RetentionDays != envDays {
		return Response{}, errors.New("retention settings are managed by environment variables")
	}
	if retentionDaysSubmitted && !s.retentionDaysEnvLocked() &&
		(submitted.Retention.RetentionDays < 1 || submitted.Retention.RetentionDays > 3650) {
		return Response{}, errors.New("retentionDays must be between 1 and 3650")
	}

	next := s.MergeSubmittedManagerConfig(current, submitted)
	if source == SourceEnv && ManagerConfigConnectionDiffers(current, next) {
		return Response{}, errors.New("connection setup is managed by environment variables")
	}
	if err := validateResolvedCollectorConfig(&next.Collector); err != nil {
		return Response{}, err
	}
	if source != SourceEnv {
		probeAllowed := s.cpaUsageProbeAllowed == nil || s.cpaUsageProbeAllowed(ctx)
		if cpa.NormalizeBaseURL(next.CPAConnection.CPABaseURL) != "" || strings.TrimSpace(next.CPAConnection.ManagementKey) != "" {
			if !ConnectionComplete(next) {
				return Response{}, errors.New("cpaBaseUrl and managementKey are required")
			}
			if probeAllowed {
				if !s.externalCollectorLifecycle {
					if err := cpa.ValidateManagementAPI(
						ctx,
						next.CPAConnection.CPABaseURL,
						next.CPAConnection.ManagementKey,
					); err != nil {
						return Response{}, err
					}
				}
				if ManagerCollectorEnabled(next) {
					if err := cpa.ValidateCollectorConfig(
						ctx,
						next.CPAConnection.CPABaseURL,
						next.CPAConnection.ManagementKey,
						next.Collector.PollIntervalMS,
					); err != nil {
						return Response{}, err
					}
					if !s.externalCollectorLifecycle {
						if err := cpa.SetUsageStatisticsEnabled(
							ctx,
							next.CPAConnection.CPABaseURL,
							next.CPAConnection.ManagementKey,
							true,
						); err != nil {
							return Response{}, err
						}
					}
				}
			}
		} else if ManagerCollectorEnabled(next) {
			return Response{}, errors.New("cpaBaseUrl and managementKey are required when request monitoring is enabled")
		}
	}

	toPersist := next
	if source == SourceEnv {
		if savedOK {
			toPersist.CPAConnection = saved.CPAConnection
			toPersist.Collector = saved.Collector
			toPersist.ExternalUsageService = saved.ExternalUsageService
		} else {
			toPersist.CPAConnection = store.ManagerCPAConnectionConfig{}
			toPersist.Collector = store.ManagerCollectorConfig{}
			toPersist.ExternalUsageService = store.ManagerExternalUsageServiceConfig{}
		}
	} else {
		toPersist.Collector.TLSSkipVerifyExplicit = true
	}
	if s.retentionEnabledEnvLocked() {
		if savedOK {
			toPersist.Retention.Enabled = saved.Retention.Enabled
		} else {
			toPersist.Retention.Enabled = nil
		}
	}
	if s.retentionDaysEnvLocked() {
		if savedOK {
			toPersist.Retention.RetentionDays = saved.Retention.RetentionDays
		} else {
			toPersist.Retention.RetentionDays = 0
		}
	}
	stripRetentionLockMetadata(&toPersist.Retention)

	if source == SourceEnv || toPersist.CPAConnection.CPABaseURL == "" || toPersist.CPAConnection.ManagementKey == "" {
		if err := s.store.SaveManagerConfig(ctx, toPersist); err != nil {
			return Response{}, err
		}
	} else {
		setup := SetupFromManagerConfig(toPersist)
		if err := s.store.SaveManagerConfigAndSetup(ctx, toPersist, setup); err != nil {
			return Response{}, err
		}
	}

	var (
		resolved       store.ManagerConfig
		resolvedSource Source
	)
	if source == SourceEnv {
		resolved, resolvedSource, _, err = s.ResolveManagerConfigWithSource(ctx)
	} else {
		resolved, resolvedSource, err = s.reconcileStandaloneCollector(ctx)
	}
	if err != nil {
		return Response{}, err
	}
	if s.effectiveRetentionDiffers(current.Retention, resolved.Retention) && s.retentionChangedNotifier != nil {
		s.retentionChangedNotifier()
	}
	return Response{
		Config: PublicConfig(resolved),
		Source: string(resolvedSource),
	}, nil
}

// SetCollectorEnabled applies the request-monitoring switch as its own write.
// The switch owns exactly one field, so this path patches the stored document
// instead of submitting a whole configuration through Update: every other field
// the installation carries survives, whether or not this build models it. A
// whole-document write is what used to drop the stored External usage service
// section and the TLS setting of a switch that never meant to touch them.
//
// Turning the switch off never fails on the CPA's account: a manager that could
// not disable collection would be the opposite of a privacy control.
func (s *Service) SetCollectorEnabled(ctx context.Context, enabled bool) (Response, error) {
	current, source, _, err := s.ResolveManagerConfigWithSource(ctx)
	if err != nil {
		return Response{}, err
	}
	if source == SourceEnv && ManagerCollectorEnabled(current) != enabled {
		return Response{}, errors.New("connection setup is managed by environment variables")
	}
	connection := ConnectionComplete(current)
	probeAllowed := s.cpaUsageProbeAllowed == nil || s.cpaUsageProbeAllowed(ctx)
	if enabled {
		if err := validateEnabledCollectorBounds(current.Collector); err != nil {
			return Response{}, err
		}
		if !s.externalCollectorLifecycle {
			if !connection {
				return Response{}, errors.New("cpaBaseUrl and managementKey are required when request monitoring is enabled")
			}
			// Enabling is proven against the CPA before anything is stored: the
			// Collector would otherwise run against a CPA that records nothing.
			if probeAllowed {
				if err := cpa.ValidateManagementAPI(ctx, current.CPAConnection.CPABaseURL, current.CPAConnection.ManagementKey); err != nil {
					return Response{}, err
				}
			}
		}
		if connection && probeAllowed {
			if err := cpa.ValidateCollectorConfig(
				ctx,
				current.CPAConnection.CPABaseURL,
				current.CPAConnection.ManagementKey,
				current.Collector.PollIntervalMS,
			); err != nil {
				return Response{}, err
			}
		}
		if !s.externalCollectorLifecycle && probeAllowed {
			if err := cpa.SetUsageStatisticsEnabled(
				ctx,
				current.CPAConnection.CPABaseURL,
				current.CPAConnection.ManagementKey,
				true,
			); err != nil {
				return Response{}, err
			}
		}
	}
	if source != SourceEnv {
		if _, err := s.store.PatchManagerCollectorEnabled(ctx, enabled); err != nil {
			return Response{}, err
		}
	}
	next, nextSource, err := s.reconcileStandaloneCollector(ctx)
	if err != nil {
		return Response{}, err
	}
	return Response{
		Config: PublicConfig(next),
		Source: string(nextSource),
	}, nil
}

// PatchCollector updates supplied collector parameters without altering
// request-monitoring state (`collector.enabled`), CPAConnection, or untouched
// persisted fields.
func (s *Service) PatchCollector(ctx context.Context, patch store.ManagerCollectorPatch) (Response, error) {
	if patch.IsEmpty() {
		return Response{}, errors.New("collector patch requires at least one field")
	}
	current, source, _, err := s.ResolveManagerConfigWithSource(ctx)
	if err != nil {
		return Response{}, err
	}
	if source == SourceEnv {
		return Response{}, errors.New("connection setup is managed by environment variables")
	}

	normalized := store.ManagerCollectorPatch{}
	nextCollector := current.Collector
	if patch.CollectorMode != nil {
		mode, err := normalizeCollectorModeStrict(*patch.CollectorMode)
		if err != nil {
			return Response{}, err
		}
		normalized.CollectorMode = &mode
		nextCollector.CollectorMode = mode
	}
	if patch.Queue != nil {
		queue, err := normalizeCollectorQueueStrict(*patch.Queue)
		if err != nil {
			return Response{}, err
		}
		normalized.Queue = &queue
		nextCollector.Queue = queue
	}
	if patch.PopSide != nil {
		side, err := normalizePopSideStrict(*patch.PopSide)
		if err != nil {
			return Response{}, err
		}
		normalized.PopSide = &side
		nextCollector.PopSide = side
	}
	if patch.BatchSize != nil {
		if err := validateBatchSize(*patch.BatchSize); err != nil {
			return Response{}, err
		}
		batch := *patch.BatchSize
		normalized.BatchSize = &batch
		nextCollector.BatchSize = batch
	}
	if patch.PollIntervalMS != nil {
		if err := validatePollIntervalMS(*patch.PollIntervalMS); err != nil {
			return Response{}, err
		}
		poll := *patch.PollIntervalMS
		normalized.PollIntervalMS = &poll
		nextCollector.PollIntervalMS = poll
	}
	if patch.QueryLimit != nil {
		if err := validateQueryLimit(*patch.QueryLimit); err != nil {
			return Response{}, err
		}
		limit := *patch.QueryLimit
		normalized.QueryLimit = &limit
		nextCollector.QueryLimit = limit
	}
	if patch.TLSSkipVerify != nil {
		skip := *patch.TLSSkipVerify
		normalized.TLSSkipVerify = &skip
		nextCollector.TLSSkipVerify = skip
	}

	if (s.cpaUsageProbeAllowed == nil || s.cpaUsageProbeAllowed(ctx)) && ConnectionComplete(current) && ManagerCollectorEnabled(current) && patch.PollIntervalMS != nil {
		if err := cpa.ValidateCollectorConfig(
			ctx,
			current.CPAConnection.CPABaseURL,
			current.CPAConnection.ManagementKey,
			nextCollector.PollIntervalMS,
		); err != nil {
			return Response{}, err
		}
	}

	if _, err := s.store.PatchManagerCollectorConfig(ctx, normalized); err != nil {
		return Response{}, err
	}

	next, nextSource, err := s.reconcileStandaloneCollector(ctx)
	if err != nil {
		return Response{}, err
	}
	return Response{
		Config: PublicConfig(next),
		Source: string(nextSource),
	}, nil
}

func (s *Service) reconcileStandaloneCollector(ctx context.Context) (store.ManagerConfig, Source, error) {
	if s.externalCollectorLifecycle || s.collector == nil {
		resolved, resolvedSource, _, err := s.ResolveManagerConfigWithSource(ctx)
		return resolved, resolvedSource, err
	}
	s.collectorMu.Lock()
	defer s.collectorMu.Unlock()
	resolved, resolvedSource, _, err := s.ResolveManagerConfigWithSource(context.WithoutCancel(ctx))
	if err != nil {
		return store.ManagerConfig{}, SourceNone, err
	}
	if ConnectionComplete(resolved) && ManagerCollectorEnabled(resolved) {
		_ = s.collector.Start(context.Background(), resolved)
	} else {
		_ = s.collector.Stop(context.Background())
	}
	return resolved, resolvedSource, nil
}

// PatchRetention updates supplied archive retention settings without altering
// CPAConnection, Collector, or untouched persisted fields.
func (s *Service) PatchRetention(ctx context.Context, patch store.ManagerRetentionPatch) (Response, error) {
	if patch.IsEmpty() {
		return Response{}, errors.New("retention patch requires at least one field")
	}
	if _, _, _, err := s.ResolveManagerConfigWithSource(ctx); err != nil {
		return Response{}, err
	}
	if patch.Enabled != nil && s.retentionEnabledEnvLocked() && *patch.Enabled != s.cfg.UsageArchiveRetentionEnabled {
		return Response{}, errors.New("retention settings are managed by environment variables")
	}
	envDays := PositiveOrDefault(s.cfg.UsageArchiveRetentionDays, 30, 30)
	if patch.RetentionDays != nil && s.retentionDaysEnvLocked() && *patch.RetentionDays != envDays {
		return Response{}, errors.New("retention settings are managed by environment variables")
	}

	normalized := store.ManagerRetentionPatch{}
	if patch.Enabled != nil && !s.retentionEnabledEnvLocked() {
		enabled := *patch.Enabled
		normalized.Enabled = &enabled
	}
	if patch.RetentionDays != nil && !s.retentionDaysEnvLocked() {
		if *patch.RetentionDays < 1 || *patch.RetentionDays > 3650 {
			return Response{}, errors.New("retentionDays must be between 1 and 3650")
		}
		days := *patch.RetentionDays
		normalized.RetentionDays = &days
	}

	if !normalized.IsEmpty() {
		if _, err := s.store.PatchManagerRetentionConfig(ctx, normalized); err != nil {
			return Response{}, err
		}
		if s.retentionChangedNotifier != nil {
			s.retentionChangedNotifier()
		}
	}
	next, nextSource, _, err := s.ResolveManagerConfigWithSource(ctx)
	if err != nil {
		return Response{}, err
	}
	return Response{
		Config: PublicConfig(next),
		Source: string(nextSource),
	}, nil
}

func (s *Service) ResolveRetention(ctx context.Context) (bool, int, error) {
	cfg, _, _, err := s.ResolveManagerConfigWithSource(ctx)
	if err != nil {
		return false, 0, err
	}
	enabled := cfg.Retention.Enabled != nil && *cfg.Retention.Enabled
	days := cfg.Retention.RetentionDays
	if days <= 0 {
		days = PositiveOrDefault(s.cfg.UsageArchiveRetentionDays, 30, 30)
	}
	return enabled, days, nil
}

func (s *Service) ResolveSetup(ctx context.Context) (store.Setup, bool, error) {
	setup, _, ok, err := s.ResolveSetupWithSource(ctx)
	return setup, ok, err
}

func (s *Service) ResolveSetupWithSource(ctx context.Context) (store.Setup, Source, bool, error) {
	if s.cfg.CPAUpstreamURL != "" && s.cfg.ManagementKey != "" {
		return store.Setup{
			CPAUpstreamURL: cpa.NormalizeBaseURL(s.cfg.CPAUpstreamURL),
			ManagementKey:  s.cfg.ManagementKey,
			Queue:          s.cfg.Queue,
			PopSide:        s.cfg.PopSide,
		}, SourceEnv, true, nil
	}
	if managerCfg, _, ok, err := s.ResolveManagerConfigWithSource(ctx); err != nil {
		return store.Setup{}, SourceNone, false, err
	} else if ok && managerCfg.CPAConnection.CPABaseURL != "" && managerCfg.CPAConnection.ManagementKey != "" {
		return SetupFromManagerConfig(managerCfg), SourceDB, true, nil
	}
	setup, ok, err := s.store.LoadSetup(ctx)
	if !ok || err != nil {
		return setup, SourceNone, ok, err
	}
	return setup, SourceDB, true, nil
}

// ResolvePersistedCPAConnection resolves only the connection persisted in the
// settings rows. Environment variables intentionally do not participate: the
// installer calls this endpoint after importing the final connection and must
// prove that Manager Server can use that persisted value rather than a stale or
// otherwise valid environment override.
func (s *Service) ResolvePersistedCPAConnection(ctx context.Context) (LegacyConnection, Source, bool, error) {
	managerCfg, managerOK, err := s.store.LoadManagerConfig(ctx)
	if err != nil {
		return LegacyConnection{}, SourceNone, false, err
	}
	setup, setupOK, err := s.store.LoadSetup(ctx)
	if err != nil {
		return LegacyConnection{}, SourceNone, false, err
	}
	resolution, err := ResolveLegacyConnectionAuthority(managerCfg, managerOK, setup, setupOK)
	if err != nil {
		return LegacyConnection{}, SourceDB, false, err
	}
	if resolution.Authority == LegacyConnectionAuthorityNone {
		return LegacyConnection{}, SourceDB, false, nil
	}
	return resolution.Connection, SourceDB, true, nil
}

func (s *Service) ResolveManagerConfigWithSource(ctx context.Context) (store.ManagerConfig, Source, bool, error) {
	cfg := s.DefaultManagerConfig()
	source := SourceNone
	found := false
	var saved store.ManagerConfig
	managerOK := false

	if loaded, ok, err := s.store.LoadManagerConfig(ctx); err != nil {
		return cfg, source, false, err
	} else if ok {
		saved = loaded
		managerOK = true
		cfg = s.MergeSubmittedManagerConfig(cfg, saved)
		source = SourceDB
		found = true
	}

	if setup, ok, err := s.store.LoadSetup(ctx); err != nil {
		return cfg, source, false, err
	} else if ok {
		resolution, err := ResolveLegacyConnectionAuthority(saved, managerOK, setup, true)
		if err != nil {
			return cfg, source, false, err
		}
		if !managerOK || (strings.TrimSpace(saved.CPAConnection.CPABaseURL) == "" && strings.TrimSpace(saved.CPAConnection.ManagementKey) == "") {
			// A lone legacy setup row remains the fallback even when it is
			// partial; the setup page needs to see that state and report
			// needs_setup rather than silently replacing it with defaults.
			cfg.CPAConnection.CPABaseURL = cpa.NormalizeBaseURL(setup.CPAUpstreamURL)
			cfg.CPAConnection.ManagementKey = strings.TrimSpace(setup.ManagementKey)
		} else if resolution.Authority == LegacyConnectionAuthoritySetup {
			cfg.CPAConnection.CPABaseURL = resolution.Connection.BaseURL
			cfg.CPAConnection.ManagementKey = resolution.Connection.ManagementKey
		}
		if !managerOK || strings.TrimSpace(saved.Collector.Queue) == "" {
			cfg.Collector.Queue = ValueOr(setup.Queue, cfg.Collector.Queue)
		}
		if !managerOK || strings.TrimSpace(saved.Collector.PopSide) == "" {
			cfg.Collector.PopSide = NormalizePopSide(setup.PopSide, cfg.Collector.PopSide)
		}
		source = SourceDB
		found = true
	}

	if s.cfg.CPAUpstreamURL != "" && s.cfg.ManagementKey != "" {
		cfg.CPAConnection.CPABaseURL = cpa.NormalizeBaseURL(s.cfg.CPAUpstreamURL)
		cfg.CPAConnection.ManagementKey = s.cfg.ManagementKey
		cfg.Collector.CollectorMode = CollectorMode(s.cfg.CollectorMode)
		cfg.Collector.Queue = ValueOr(s.cfg.Queue, cfg.Collector.Queue)
		cfg.Collector.PopSide = NormalizePopSide(s.cfg.PopSide, cfg.Collector.PopSide)
		cfg.Collector.BatchSize = PositiveOrDefault(s.cfg.BatchSize, cfg.Collector.BatchSize, 100)
		cfg.Collector.PollIntervalMS = PositiveOrDefault(int(s.cfg.PollInterval/time.Millisecond), cfg.Collector.PollIntervalMS, 500)
		cfg.Collector.QueryLimit = PositiveOrDefault(s.cfg.QueryLimit, cfg.Collector.QueryLimit, 50000)
		cfg.Collector.TLSSkipVerify = s.cfg.TLSSkipVerify
		cfg.Collector.TLSSkipVerifyExplicit = true
		source = SourceEnv
		found = true
	}

	if s.retentionEnabledEnvLocked() {
		cfg.Retention.Enabled = BoolPtr(s.cfg.UsageArchiveRetentionEnabled)
	}
	if s.retentionDaysEnvLocked() {
		cfg.Retention.RetentionDays = PositiveOrDefault(s.cfg.UsageArchiveRetentionDays, 30, 30)
	}
	cfg.Retention.EnvLocked = s.retentionEnvLocked()
	cfg.Retention.EnabledEnvLocked = s.retentionEnabledEnvLocked()
	cfg.Retention.RetentionDaysEnvLocked = s.retentionDaysEnvLocked()
	return cfg, source, found, nil
}

func (s *Service) DefaultManagerConfig() store.ManagerConfig {
	pollIntervalMS := int(s.cfg.PollInterval / time.Millisecond)
	return store.ManagerConfig{
		Collector: store.ManagerCollectorConfig{
			Enabled:        BoolPtr(true),
			CollectorMode:  CollectorMode(s.cfg.CollectorMode),
			Queue:          ValueOr(s.cfg.Queue, "usage"),
			PopSide:        NormalizePopSide(s.cfg.PopSide, "right"),
			BatchSize:      PositiveOrDefault(s.cfg.BatchSize, 100, 100),
			PollIntervalMS: PositiveOrDefault(pollIntervalMS, 500, 500),
			QueryLimit:     PositiveOrDefault(s.cfg.QueryLimit, 50000, 50000),
			TLSSkipVerify:  s.cfg.TLSSkipVerify,
		},
		CodexInspection: store.DefaultCodexInspectionConfig(),
		Retention: store.ManagerRetentionConfig{
			Enabled:                BoolPtr(s.cfg.UsageArchiveRetentionEnabled),
			RetentionDays:          PositiveOrDefault(s.cfg.UsageArchiveRetentionDays, 30, 30),
			EnvLocked:              s.retentionEnvLocked(),
			EnabledEnvLocked:       s.retentionEnabledEnvLocked(),
			RetentionDaysEnvLocked: s.retentionDaysEnvLocked(),
		},
	}
}

func (s *Service) retentionEnabledEnvLocked() bool {
	return s.cfg.UsageArchiveRetentionEnvSet || s.cfg.UsageArchiveRetentionEnabledEnvSet
}

func (s *Service) retentionDaysEnvLocked() bool {
	return s.cfg.UsageArchiveRetentionEnvSet || s.cfg.UsageArchiveRetentionDaysEnvSet
}

func (s *Service) retentionEnvLocked() bool {
	return s.retentionEnabledEnvLocked() && s.retentionDaysEnvLocked()
}

func (s *Service) effectiveRetentionDiffers(left, right store.ManagerRetentionConfig) bool {
	leftEnabled := left.Enabled != nil && *left.Enabled
	rightEnabled := right.Enabled != nil && *right.Enabled
	defaultDays := PositiveOrDefault(s.cfg.UsageArchiveRetentionDays, 30, 30)
	leftDays := PositiveOrDefault(left.RetentionDays, defaultDays, 30)
	rightDays := PositiveOrDefault(right.RetentionDays, defaultDays, 30)
	return leftEnabled != rightEnabled || leftDays != rightDays
}

func stripRetentionLockMetadata(r *store.ManagerRetentionConfig) {
	if r == nil {
		return
	}
	r.EnvLocked = false
	r.EnabledEnvLocked = false
	r.RetentionDaysEnvLocked = false
}

func (s *Service) MergeSubmittedManagerConfig(base store.ManagerConfig, submitted store.ManagerConfig) store.ManagerConfig {
	next := base

	if strings.TrimSpace(submitted.CPAConnection.CPABaseURL) != "" {
		submittedURL := cpa.NormalizeBaseURL(submitted.CPAConnection.CPABaseURL)
		if submittedURL != cpa.NormalizeBaseURL(base.CPAConnection.CPABaseURL) &&
			strings.TrimSpace(submitted.CPAConnection.ManagementKey) == "" {
			next.CPAConnection.ManagementKey = ""
		}
		next.CPAConnection.CPABaseURL = submittedURL
	}
	if strings.TrimSpace(submitted.CPAConnection.ManagementKey) != "" {
		next.CPAConnection.ManagementKey = strings.TrimSpace(submitted.CPAConnection.ManagementKey)
	}

	if submitted.Collector.Enabled != nil {
		next.Collector.Enabled = BoolPtr(*submitted.Collector.Enabled)
	}
	next.Collector.CollectorMode = CollectorMode(ValueOr(submitted.Collector.CollectorMode, next.Collector.CollectorMode))
	next.Collector.Queue = ValueOr(strings.TrimSpace(submitted.Collector.Queue), next.Collector.Queue)
	next.Collector.PopSide = NormalizePopSide(submitted.Collector.PopSide, next.Collector.PopSide)
	next.Collector.BatchSize = PositiveOrDefault(submitted.Collector.BatchSize, next.Collector.BatchSize, 100)
	next.Collector.PollIntervalMS = PositiveOrDefault(submitted.Collector.PollIntervalMS, next.Collector.PollIntervalMS, 500)
	next.Collector.QueryLimit = PositiveOrDefault(submitted.Collector.QueryLimit, next.Collector.QueryLimit, 50000)
	if submitted.Collector.TLSSkipVerifyExplicit || submitted.Collector.TLSSkipVerify {
		next.Collector.TLSSkipVerify = submitted.Collector.TLSSkipVerify
		next.Collector.TLSSkipVerifyExplicit = true
	}

	next.CodexInspection = store.NormalizeCodexInspectionConfig(submitted.CodexInspection, next.CodexInspection)
	if submitted.Retention.Enabled != nil {
		next.Retention.Enabled = BoolPtr(*submitted.Retention.Enabled)
	}
	if submitted.Retention.RetentionDays > 0 {
		next.Retention.RetentionDays = submitted.Retention.RetentionDays
	}

	next.ExternalUsageService.Enabled = false
	next.ExternalUsageService.ServiceBase = ""

	return next
}

func PublicConfig(cfg store.ManagerConfig) PublicManagerConfig {
	collector := cfg.Collector
	collector.TLSSkipVerifyExplicit = true
	return PublicManagerConfig{
		CPAConnection: PublicManagerCPAConnectionConfig{
			CPABaseURL:              cfg.CPAConnection.CPABaseURL,
			ManagementKeyConfigured: strings.TrimSpace(cfg.CPAConnection.ManagementKey) != "",
		},
		Collector:            collector,
		CodexInspection:      cfg.CodexInspection,
		ExternalUsageService: cfg.ExternalUsageService,
		Retention: store.ManagerRetentionConfig{
			Enabled:                cfg.Retention.Enabled,
			RetentionDays:          cfg.Retention.RetentionDays,
			EnvLocked:              cfg.Retention.EnvLocked,
			EnabledEnvLocked:       cfg.Retention.EnabledEnvLocked,
			RetentionDaysEnvLocked: cfg.Retention.RetentionDaysEnvLocked,
		},
		UpdatedAtMS: cfg.UpdatedAtMS,
	}
}

func SetupFromManagerConfig(cfg store.ManagerConfig) store.Setup {
	return store.Setup{
		CPAUpstreamURL: cfg.CPAConnection.CPABaseURL,
		ManagementKey:  cfg.CPAConnection.ManagementKey,
		Queue:          cfg.Collector.Queue,
		PopSide:        cfg.Collector.PopSide,
	}
}

// LegacyConnectionAuthority identifies which historical row, if any, is
// authoritative for a persisted CPA connection. It deliberately does not
// perform secret storage or fill a connection from two partial rows.
type LegacyConnectionAuthority string

const (
	LegacyConnectionAuthorityNone    LegacyConnectionAuthority = ""
	LegacyConnectionAuthorityManager LegacyConnectionAuthority = "manager_config_v1"
	LegacyConnectionAuthoritySetup   LegacyConnectionAuthority = "legacy setup"
)

// LegacyConnectionConflictRepairHint is appended to persisted-state conflict
// errors so operators learn the explicit recovery path instead of having to
// edit SQLite by hand. Repairing is always an explicit, offline action: the
// shared resolver itself never picks a side.
const LegacyConnectionConflictRepairHint = `

The persisted manager_config_v1 and legacy setup rows contain conflicting CPA connection data: neither side was chosen and nothing was overwritten. To repair the stored connection explicitly, stop Manager Server and re-run:

  cpa-manager-plus store-cpa-connection --repair-conflict --cpa-base-url <url> --management-key-file <key-file> [--db-path <db>] [--data-key-path <key>]`

// LegacyConnectionCompleteAuthorityNote is appended when --repair-conflict
// cannot apply because the persisted authority is complete and consistent.
// A healthy stored connection must not be rebound through the offline repair
// path; it is changed through the management panel or an identical import.
const LegacyConnectionCompleteAuthorityNote = `

--repair-conflict only repairs persisted rows that conflict with each other; this stored CPA connection is complete and consistent. Provide the matching connection, or change it through the management panel while the server is running.`

type LegacyConnection struct {
	BaseURL       string
	ManagementKey string
}

type LegacyConnectionResolution struct {
	Authority  LegacyConnectionAuthority
	Connection LegacyConnection

	manager LegacyConnection
	setup   LegacyConnection
}

// ResolveLegacyConnectionAuthority is the single authority decision used by
// bootstrap normalization and the offline connection importer. A complete
// manager row wins over legacy setup, while a complete setup can repair a
// partial manager row only when every manager side that exists matches it.
// Two partial rows never get combined implicitly.
func ResolveLegacyConnectionAuthority(
	managerCfg store.ManagerConfig,
	managerOK bool,
	setup store.Setup,
	setupOK bool,
) (LegacyConnectionResolution, error) {
	manager := LegacyConnection{}
	if managerOK {
		manager = LegacyConnection{
			BaseURL:       cpa.NormalizeBaseURL(managerCfg.CPAConnection.CPABaseURL),
			ManagementKey: strings.TrimSpace(managerCfg.CPAConnection.ManagementKey),
		}
	}
	legacySetup := LegacyConnection{}
	if setupOK {
		legacySetup = LegacyConnection{
			BaseURL:       cpa.NormalizeBaseURL(setup.CPAUpstreamURL),
			ManagementKey: strings.TrimSpace(setup.ManagementKey),
		}
	}

	resolution := LegacyConnectionResolution{
		manager: manager,
		setup:   legacySetup,
	}
	managerComplete := manager.BaseURL != "" && manager.ManagementKey != ""
	setupComplete := legacySetup.BaseURL != "" && legacySetup.ManagementKey != ""
	if managerComplete {
		resolution.Authority = LegacyConnectionAuthorityManager
		resolution.Connection = manager
		return resolution, nil
	}
	if !setupComplete {
		// Two partial rows do not establish an authority, but overlapping
		// values still have to agree. Otherwise the normal setup flow could
		// silently replace one side of an explicitly conflicting history.
		if manager.BaseURL != "" && legacySetup.BaseURL != "" && manager.BaseURL != legacySetup.BaseURL {
			return LegacyConnectionResolution{}, errors.New(
				"manager_config_v1 contains a partial CPA connection whose URL conflicts with the legacy setup",
			)
		}
		if manager.ManagementKey != "" && legacySetup.ManagementKey != "" &&
			!security.EqualHMAC(manager.ManagementKey, legacySetup.ManagementKey) {
			return LegacyConnectionResolution{}, errors.New(
				"manager_config_v1 contains a partial CPA connection whose key conflicts with the legacy setup",
			)
		}
		return resolution, nil
	}
	if manager.BaseURL != "" && manager.BaseURL != legacySetup.BaseURL {
		return LegacyConnectionResolution{}, errors.New(
			"legacy setup CPA connection conflicts with the present manager_config_v1 URL",
		)
	}
	if manager.ManagementKey != "" && !security.EqualHMAC(manager.ManagementKey, legacySetup.ManagementKey) {
		return LegacyConnectionResolution{}, errors.New(
			"legacy setup CPA connection conflicts with the present manager_config_v1 key",
		)
	}
	resolution.Authority = LegacyConnectionAuthoritySetup
	resolution.Connection = legacySetup
	return resolution, nil
}

// ValidateRequestedLegacyConnection compares an explicit import request with
// the resolved persisted authority. When both historical rows are partial and
// there is no authority, every present side is checked independently; the
// helper never treats those sides as one inferred connection.
func (r LegacyConnectionResolution) ValidateRequestedLegacyConnection(input LegacyConnection) error {
	input.BaseURL = cpa.NormalizeBaseURL(input.BaseURL)
	input.ManagementKey = strings.TrimSpace(input.ManagementKey)
	if r.Authority != LegacyConnectionAuthorityNone {
		if !legacyConnectionsEqual(r.Connection, input) {
			return errors.New(string(r.Authority) + " CPA connection conflicts with the requested connection")
		}
		return nil
	}
	if r.manager.BaseURL != "" && r.manager.BaseURL != input.BaseURL {
		return errors.New("manager_config_v1 contains a partial CPA connection whose URL conflicts with the requested connection")
	}
	if r.manager.ManagementKey != "" && !security.EqualHMAC(r.manager.ManagementKey, input.ManagementKey) {
		return errors.New("manager_config_v1 contains a partial CPA connection whose key conflicts with the requested connection")
	}
	if r.setup.BaseURL != "" && r.setup.BaseURL != input.BaseURL {
		return errors.New("legacy setup contains a partial CPA connection whose URL conflicts with the requested connection")
	}
	if r.setup.ManagementKey != "" && !security.EqualHMAC(r.setup.ManagementKey, input.ManagementKey) {
		return errors.New("legacy setup contains a partial CPA connection whose key conflicts with the requested connection")
	}
	return nil
}

func legacyConnectionsEqual(left LegacyConnection, right LegacyConnection) bool {
	return cpa.NormalizeBaseURL(left.BaseURL) == cpa.NormalizeBaseURL(right.BaseURL) &&
		security.EqualHMAC(strings.TrimSpace(left.ManagementKey), strings.TrimSpace(right.ManagementKey))
}

// LegacyConnectionsEqual compares a resolved historical connection with an
// explicit connection supplied by an offline importer. Keep this comparison
// next to the authority resolver so callers cannot drift into a second set of
// URL/key equality rules.
func LegacyConnectionsEqual(left LegacyConnection, right LegacyConnection) bool {
	return legacyConnectionsEqual(left, right)
}

// CanonicalSetupFromManagerConfig rebuilds the connection sides from the
// manager authority while retaining non-connection collector fields already
// present in the legacy row when the manager row did not have them.
func CanonicalSetupFromManagerConfig(cfg store.ManagerConfig, legacySetup store.Setup, legacySetupPresent bool) store.Setup {
	next := SetupFromManagerConfig(cfg)
	if !legacySetupPresent {
		return next
	}
	if strings.TrimSpace(next.Queue) == "" {
		next.Queue = legacySetup.Queue
	}
	if strings.TrimSpace(next.PopSide) == "" {
		next.PopSide = NormalizePopSide(legacySetup.PopSide, next.PopSide)
	}
	return next
}

// ConnectionComplete reports whether manager_config_v1 carries both sides of
// the CPA connection. A complete manager config is the authority for the
// active connection; bootstrap normalization and the offline importer must
// agree on this definition.
func ConnectionComplete(cfg store.ManagerConfig) bool {
	return cpa.NormalizeBaseURL(cfg.CPAConnection.CPABaseURL) != "" &&
		strings.TrimSpace(cfg.CPAConnection.ManagementKey) != ""
}

// SetupConnectionComplete mirrors ConnectionComplete for the legacy setup row.
func SetupConnectionComplete(setup store.Setup) bool {
	return cpa.NormalizeBaseURL(setup.CPAUpstreamURL) != "" &&
		strings.TrimSpace(setup.ManagementKey) != ""
}

// MergeLegacyCollectorSettings fills empty manager collector fields from a
// persisted legacy setup row. Connection completeness is intentionally not a
// prerequisite: partial historical rows still contain non-connection fields
// that must survive storage normalization.
func MergeLegacyCollectorSettings(managerCfg *store.ManagerConfig, setup store.Setup, setupPresent bool) {
	if managerCfg == nil || !setupPresent {
		return
	}
	if strings.TrimSpace(managerCfg.Collector.Queue) == "" {
		managerCfg.Collector.Queue = ValueOr(setup.Queue, managerCfg.Collector.Queue)
	}
	if strings.TrimSpace(managerCfg.Collector.PopSide) == "" {
		managerCfg.Collector.PopSide = NormalizePopSide(setup.PopSide, managerCfg.Collector.PopSide)
	}
}

func normalizeCollectorModeStrict(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "auto", "http", "resp", "subscribe":
		return mode, nil
	default:
		return "", errors.New("collectorMode must be auto, http, resp, or subscribe")
	}
}

func normalizePopSideStrict(raw string) (string, error) {
	side := strings.ToLower(strings.TrimSpace(raw))
	switch side {
	case "left", "right":
		return side, nil
	default:
		return "", errors.New("popSide must be left or right")
	}
}

func validateBatchSize(value int) error {
	if value < 1 || value > 5000 {
		return errors.New("batchSize must be between 1 and 5000")
	}
	return nil
}

func validatePollIntervalMS(value int) error {
	if value < 10 || value > 60000 {
		return errors.New("pollIntervalMs must be between 10 and 60000")
	}
	return nil
}

func validateQueryLimit(value int) error {
	if value < 1 || value > 100000 {
		return errors.New("queryLimit must be between 1 and 100000")
	}
	return nil
}

func normalizeCollectorQueueStrict(raw string) (string, error) {
	queue := strings.TrimSpace(raw)
	if queue == "" {
		return "", errors.New("queue must not be empty")
	}
	for _, r := range queue {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("queue must not be empty or contain control characters")
		}
	}
	return queue, nil
}

func validateSubmittedCollectorConfig(c *store.ManagerCollectorConfig) error {
	if c == nil {
		return nil
	}
	if c.CollectorMode != "" {
		mode, err := normalizeCollectorModeStrict(c.CollectorMode)
		if err != nil {
			return err
		}
		c.CollectorMode = mode
	}
	if c.Queue != "" {
		queue, err := normalizeCollectorQueueStrict(c.Queue)
		if err != nil {
			return err
		}
		c.Queue = queue
	}
	if c.PopSide != "" {
		side, err := normalizePopSideStrict(c.PopSide)
		if err != nil {
			return err
		}
		c.PopSide = side
	}
	if c.BatchSize != 0 {
		if err := validateBatchSize(c.BatchSize); err != nil {
			return err
		}
	}
	if c.PollIntervalMS != 0 {
		if err := validatePollIntervalMS(c.PollIntervalMS); err != nil {
			return err
		}
	}
	if c.QueryLimit != 0 {
		if err := validateQueryLimit(c.QueryLimit); err != nil {
			return err
		}
	}
	return nil
}

func validateResolvedCollectorConfig(c *store.ManagerCollectorConfig) error {
	if c == nil {
		return nil
	}
	mode, err := normalizeCollectorModeStrict(c.CollectorMode)
	if err != nil {
		return err
	}
	queue, err := normalizeCollectorQueueStrict(c.Queue)
	if err != nil {
		return err
	}
	side, err := normalizePopSideStrict(c.PopSide)
	if err != nil {
		return err
	}
	if err := validateBatchSize(c.BatchSize); err != nil {
		return err
	}
	if err := validatePollIntervalMS(c.PollIntervalMS); err != nil {
		return err
	}
	if err := validateQueryLimit(c.QueryLimit); err != nil {
		return err
	}
	c.CollectorMode = mode
	c.Queue = queue
	c.PopSide = side
	return nil
}

func validateEnabledCollectorBounds(c store.ManagerCollectorConfig) error {
	return validateResolvedCollectorConfig(&c)
}

func ManagerConfigConnectionDiffers(left store.ManagerConfig, right store.ManagerConfig) bool {
	return cpa.NormalizeBaseURL(left.CPAConnection.CPABaseURL) != cpa.NormalizeBaseURL(right.CPAConnection.CPABaseURL) ||
		left.CPAConnection.ManagementKey != right.CPAConnection.ManagementKey ||
		ManagerCollectorEnabled(left) != ManagerCollectorEnabled(right) ||
		left.Collector.CollectorMode != right.Collector.CollectorMode ||
		left.Collector.Queue != right.Collector.Queue ||
		left.Collector.PopSide != right.Collector.PopSide ||
		left.Collector.BatchSize != right.Collector.BatchSize ||
		left.Collector.PollIntervalMS != right.Collector.PollIntervalMS ||
		left.Collector.QueryLimit != right.Collector.QueryLimit ||
		left.Collector.TLSSkipVerify != right.Collector.TLSSkipVerify
}

func ManagerConfigCPABindingDiffers(left store.ManagerConfig, right store.ManagerConfig) bool {
	leftBase := cpa.NormalizeBaseURL(left.CPAConnection.CPABaseURL)
	rightBase := cpa.NormalizeBaseURL(right.CPAConnection.CPABaseURL)
	if leftBase == "" || left.CPAConnection.ManagementKey == "" {
		return false
	}
	return leftBase != rightBase
}

func PositiveOrDefault(value int, fallback int, hardDefault int) int {
	if value > 0 {
		return value
	}
	if fallback > 0 {
		return fallback
	}
	return hardDefault
}

func ValueOr(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func NormalizePopSide(value string, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "left", "right":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		if strings.ToLower(strings.TrimSpace(fallback)) == "left" {
			return "left"
		}
		return "right"
	}
}

func CollectorMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "http", "resp", "subscribe":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "auto"
	}
}

func BoolPtr(value bool) *bool {
	return &value
}

func ManagerCollectorEnabled(cfg store.ManagerConfig) bool {
	return cfg.Collector.Enabled == nil || *cfg.Collector.Enabled
}

func AuthHeaderMatches(header string, managementKey string) bool {
	header = strings.TrimSpace(header)
	if header == "" || managementKey == "" {
		return false
	}
	const prefix = "Bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	return strings.TrimSpace(header[len(prefix):]) == managementKey
}
