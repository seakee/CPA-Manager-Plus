package setup

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type Request struct {
	CPAUpstreamURL               string `json:"cpaBaseUrl"`
	ManagementKey                string `json:"managementKey"`
	CPAManagementKey             string `json:"cpaManagementKey"`
	CollectorMode                string `json:"collectorMode"`
	Queue                        string `json:"queue"`
	PopSide                      string `json:"popSide"`
	BatchSize                    int    `json:"batchSize"`
	PollIntervalMS               int    `json:"pollIntervalMs"`
	QueryLimit                   int    `json:"queryLimit"`
	TLSSkipVerify                bool   `json:"tlsSkipVerify"`
	EnsureUsageStatisticsEnabled *bool  `json:"ensureUsageStatisticsEnabled"`
	RequestMonitoringEnabled     *bool  `json:"requestMonitoringEnabled"`
}

type Result struct {
	OK       bool   `json:"ok"`
	Upstream string `json:"upstream"`
}

type InfoResult struct {
	Service            string `json:"service"`
	Mode               string `json:"mode"`
	StartedAt          int64  `json:"startedAt"`
	Configured         bool   `json:"configured"`
	AdminReady         bool   `json:"adminReady"`
	ProjectInitialized bool   `json:"projectInitialized"`
	SetupRequired      bool   `json:"setupRequired"`
	MigrationStatus    string `json:"migrationStatus,omitempty"`
	DataKeyReady       bool   `json:"dataKeyReady"`
	HasHistoricalData  bool   `json:"hasHistoricalData"`
}

type Service struct {
	cfg                  config.Config
	store                *store.Store
	collector            *collectorservice.Service
	managerConfigService *managerconfig.Service
	startedAt            int64
	serviceID            string
}

func New(cfg config.Config, store *store.Store, collector *collectorservice.Service, managerConfigService *managerconfig.Service, startedAt int64, serviceID string) *Service {
	return &Service{
		cfg:                  cfg,
		store:                store,
		collector:            collector,
		managerConfigService: managerConfigService,
		startedAt:            startedAt,
		serviceID:            serviceID,
	}
}

func (s *Service) Info(ctx context.Context) (InfoResult, error) {
	setup, ok, err := s.managerConfigService.ResolveSetup(ctx)
	if err != nil {
		return InfoResult{}, err
	}
	_, adminReady, err := s.store.LoadAdminCredential(ctx)
	if err != nil {
		return InfoResult{}, err
	}
	bootstrapState, bootstrapStateOK, err := s.store.LoadBootstrapState(ctx)
	if err != nil {
		return InfoResult{}, err
	}
	projectInitialized := ok && setup.CPAUpstreamURL != "" && setup.ManagementKey != ""
	if bootstrapStateOK && !projectInitialized {
		projectInitialized = bootstrapState.ProjectInitialized
	}
	return InfoResult{
		Service:            s.serviceID,
		Mode:               "embedded",
		StartedAt:          s.startedAt,
		Configured:         projectInitialized,
		AdminReady:         adminReady,
		ProjectInitialized: projectInitialized,
		SetupRequired:      adminReady && !projectInitialized,
		MigrationStatus:    bootstrapState.Status,
		DataKeyReady:       bootstrapState.DataKeyReady,
		HasHistoricalData:  bootstrapState.HasHistoricalData,
	}, nil
}

func (s *Service) Setup(ctx context.Context, req Request, _ string) (Result, error) {
	req.CPAUpstreamURL = cpa.NormalizeBaseURL(req.CPAUpstreamURL)
	req.CPAManagementKey = strings.TrimSpace(req.CPAManagementKey)
	if req.CPAManagementKey == "" {
		req.CPAManagementKey = strings.TrimSpace(req.ManagementKey)
	}
	req.ManagementKey = req.CPAManagementKey
	req.CollectorMode = managerconfig.CollectorMode(req.CollectorMode)
	if req.Queue == "" {
		req.Queue = s.cfg.Queue
	}
	if req.PopSide == "" {
		req.PopSide = s.cfg.PopSide
	}
	req.PopSide = managerconfig.NormalizePopSide(req.PopSide, s.cfg.PopSide)
	req.BatchSize = managerconfig.PositiveOrDefault(req.BatchSize, s.cfg.BatchSize, 100)
	req.PollIntervalMS = managerconfig.PositiveOrDefault(req.PollIntervalMS, int(s.cfg.PollInterval/time.Millisecond), 500)
	req.QueryLimit = managerconfig.PositiveOrDefault(req.QueryLimit, s.cfg.QueryLimit, 50000)
	requestMonitoringEnabled := requestMonitoringEnabled(req)
	if req.CPAUpstreamURL == "" || req.ManagementKey == "" {
		return Result{}, errors.New("cpaBaseUrl and managementKey are required")
	}
	managementAPIValidated := false
	if existing, source, ok, err := s.managerConfigService.ResolveSetupWithSource(ctx); err != nil {
		return Result{}, err
	} else if source == managerconfig.SourceEnv && setupDiffers(existing, req) {
		return Result{}, errors.New("setup is managed by environment variables")
	} else if ok && existing.ManagementKey != "" && req.ManagementKey != existing.ManagementKey {
		if cpa.NormalizeBaseURL(existing.CPAUpstreamURL) != req.CPAUpstreamURL {
			return Result{}, errors.New("invalid management key for existing setup")
		}
		if err := cpa.ValidateManagementAPI(ctx, req.CPAUpstreamURL, req.ManagementKey); err != nil {
			return Result{}, err
		}
		managementAPIValidated = true
	}
	if !managementAPIValidated {
		if err := cpa.ValidateManagementAPI(ctx, req.CPAUpstreamURL, req.ManagementKey); err != nil {
			return Result{}, err
		}
	}
	managerCfg := s.managerConfigService.DefaultManagerConfig()
	if existingManagerCfg, _, ok, err := s.managerConfigService.ResolveManagerConfigWithSource(ctx); err != nil {
		return Result{}, err
	} else if ok {
		managerCfg = existingManagerCfg
	}
	managerCfg.CPAConnection.CPABaseURL = req.CPAUpstreamURL
	managerCfg.CPAConnection.ManagementKey = req.ManagementKey
	managerCfg.Collector.Enabled = managerconfig.BoolPtr(requestMonitoringEnabled)
	managerCfg.Collector.CollectorMode = req.CollectorMode
	managerCfg.Collector.Queue = req.Queue
	managerCfg.Collector.PopSide = req.PopSide
	managerCfg.Collector.BatchSize = req.BatchSize
	managerCfg.Collector.PollIntervalMS = req.PollIntervalMS
	managerCfg.Collector.QueryLimit = req.QueryLimit
	managerCfg.Collector.TLSSkipVerify = req.TLSSkipVerify
	if requestMonitoringEnabled {
		if err := cpa.ValidateCollectorConfig(
			ctx,
			managerCfg.CPAConnection.CPABaseURL,
			managerCfg.CPAConnection.ManagementKey,
			managerCfg.Collector.PollIntervalMS,
		); err != nil {
			return Result{}, err
		}
	}
	ensureUsageStatisticsEnabled := requestMonitoringEnabled
	if req.EnsureUsageStatisticsEnabled != nil {
		ensureUsageStatisticsEnabled = requestMonitoringEnabled && *req.EnsureUsageStatisticsEnabled
	}
	if ensureUsageStatisticsEnabled {
		if err := cpa.SetUsageStatisticsEnabled(ctx, req.CPAUpstreamURL, req.ManagementKey, true); err != nil {
			return Result{}, err
		}
	}
	setup := store.Setup{
		CPAUpstreamURL: req.CPAUpstreamURL,
		ManagementKey:  req.ManagementKey,
		Queue:          req.Queue,
		PopSide:        req.PopSide,
	}
	if err := s.store.SaveManagerConfigAndSetup(ctx, managerCfg, setup); err != nil {
		return Result{}, err
	}
	if err := s.markBootstrapReady(ctx); err != nil {
		return Result{}, err
	}
	if requestMonitoringEnabled {
		_ = s.collector.Start(context.Background(), managerCfg)
	} else {
		_ = s.collector.Stop(context.Background())
	}
	return Result{OK: true, Upstream: setup.CPAUpstreamURL}, nil
}

func (s *Service) markBootstrapReady(ctx context.Context) error {
	state, ok, err := s.store.LoadBootstrapState(ctx)
	if err != nil {
		return err
	}
	if !ok {
		state = store.BootstrapState{Version: 1}
	} else if state.Version == 0 {
		state.Version = 1
	}
	state.Status = "ready"
	state.AdminReady = true
	state.ProjectInitialized = true
	state.DataKeyReady = true
	return s.store.SaveBootstrapState(ctx, state)
}

func setupDiffers(existing store.Setup, req Request) bool {
	return cpa.NormalizeBaseURL(existing.CPAUpstreamURL) != req.CPAUpstreamURL ||
		existing.ManagementKey != req.ManagementKey ||
		existing.Queue != req.Queue ||
		existing.PopSide != req.PopSide
}

func requestMonitoringEnabled(req Request) bool {
	if req.RequestMonitoringEnabled == nil {
		return true
	}
	return *req.RequestMonitoringEnabled
}
