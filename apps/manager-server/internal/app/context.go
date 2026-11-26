package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	accountactionsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/accountaction"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	apikeyaliassvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/apikeyalias"
	automationsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/automation"
	bootstrapsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrap"
	codexinspectionsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/codexinspection"
	collectorsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	cpaupdatesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaupdate"
	dashboardsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/dashboard"
	managerconfigsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	modelpricesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/modelprice"
	monitoringsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/monitoring"
	panelsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/panel"
	proxysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxy"
	quotasnapshotsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/quotasnapshot"
	setupsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/setup"
	updatechecksvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/updatecheck"
	usagesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type AutomationRuntimeService interface {
	Reload(ctx context.Context) error
}

type QuotaCooldownRecoveryService interface {
	RecoverOwnedCooldown(ctx context.Context, authFileName string, authIndex string, provider string, cooldownID int64, updatedAtMS int64) (bool, error)
}

type DatabaseMaintenanceStatusProvider interface {
	Snapshot() sqliterepo.WALMaintenanceSnapshot
}

type Context struct {
	UpdateCheckService *updatechecksvc.Service
	CPAUpdateService   *cpaupdatesvc.Service
	Config             config.Config
	Store              *store.Store
	Collector          *collector.Manager

	StartedAt int64
	ServiceID string
	Bootstrap bootstrapsvc.Result

	SetupService                   *setupsvc.Service
	AdminAuthService               *adminauthsvc.Service
	ManagerConfigService           *managerconfigsvc.Service
	CollectorService               *collectorsvc.Service
	UsageService                   *usagesvc.Service
	DashboardService               *dashboardsvc.Service
	CodexInspectionService         *codexinspectionsvc.Service
	MonitoringService              *monitoringsvc.Service
	QuotaSnapshotService           *quotasnapshotsvc.Service
	ModelPriceService              *modelpricesvc.Service
	APIKeyAliasService             *apikeyaliassvc.Service
	AccountActionService           *accountactionsvc.Service
	AccountProcessingPolicyService *automationsvc.Service
	AuthFileMutationCoordinator    *cpaauthfiles.MutationCoordinator
	QuotaCooldownRecoveryService   QuotaCooldownRecoveryService
	ProxyService                   *proxysvc.Service
	PanelService                   *panelsvc.Service
	AutomationRuntimeService       AutomationRuntimeService
	DatabaseMaintenance            DatabaseMaintenanceStatusProvider
	UpgradeAPIKeys                 func(w http.ResponseWriter, r *http.Request, resource string)
}

func FromExisting(
	cfg config.Config,
	st *store.Store,
	collectorManager *collector.Manager,
	startedAt int64,
	embeddedPanel fs.FS,
	modelPriceSyncURL *string,
	openRouterModelPriceSyncURL *string,
	serviceID string,
	automationRuntimeService ...AutomationRuntimeService,
) *Context {
	return fromExisting(
		cfg,
		st,
		collectorManager,
		startedAt,
		embeddedPanel,
		nil,
		modelPriceSyncURL,
		openRouterModelPriceSyncURL,
		serviceID,
		automationRuntimeService...,
	)
}

func FromExistingWithModelsDev(
	cfg config.Config,
	st *store.Store,
	collectorManager *collector.Manager,
	startedAt int64,
	embeddedPanel fs.FS,
	modelsDevModelPriceSyncURL *string,
	modelPriceSyncURL *string,
	openRouterModelPriceSyncURL *string,
	serviceID string,
	automationRuntimeService ...AutomationRuntimeService,
) *Context {
	return fromExisting(
		cfg,
		st,
		collectorManager,
		startedAt,
		embeddedPanel,
		modelsDevModelPriceSyncURL,
		modelPriceSyncURL,
		openRouterModelPriceSyncURL,
		serviceID,
		automationRuntimeService...,
	)
}

func fromExisting(
	cfg config.Config,
	st *store.Store,
	collectorManager *collector.Manager,
	startedAt int64,
	embeddedPanel fs.FS,
	modelsDevModelPriceSyncURL *string,
	modelPriceSyncURL *string,
	openRouterModelPriceSyncURL *string,
	serviceID string,
	automationRuntimeService ...AutomationRuntimeService,
) *Context {
	var runtimeService AutomationRuntimeService
	if len(automationRuntimeService) > 0 {
		runtimeService = automationRuntimeService[0]
	}
	collectorService := collectorsvc.New(collectorManager)
	managerConfigService := managerconfigsvc.New(cfg, st, collectorService)
	accountProcessingPolicyService := automationsvc.New(cfg, st)
	usageImportBaseDir := strings.TrimSpace(cfg.DataDir)
	if usageImportBaseDir == "" {
		usageImportBaseDir = filepath.Dir(cfg.DBPath)
	}
	usageArchiveDir := strings.TrimSpace(cfg.UsageArchiveDir)
	if usageArchiveDir == "" {
		usageArchiveDir = filepath.Join(usageImportBaseDir, "usage-archives")
	}
	usageService := usagesvc.New(st, usagesvc.WithImportSessions(usagesvc.ImportSessionConfig{
		Directory:      filepath.Join(usageImportBaseDir, "usage-imports"),
		ChunkSizeBytes: cfg.UsageImportChunkBytes,
		DiskQuotaBytes: cfg.UsageImportDiskQuotaBytes,
		MaxSessions:    cfg.UsageImportMaxSessions,
		TTL:            cfg.UsageImportSessionTTL,
	}), usagesvc.WithArchive(usagesvc.ArchiveConfig{
		Directory:             usageArchiveDir,
		AggregateReadsEnabled: cfg.DashboardHourlyRollupEnabled,
	}))
	authFileMutationCoordinator := cpaauthfiles.NewMutationCoordinator()
	updateCheckService := updatechecksvc.New(st, buildinfo.Version, buildinfo.SourceCommit, os.Getenv("CPAMP_UPDATE_CHECK_ENABLED") != "false")
	updateCheckService.SetCurrentSchemaGeneration(sqliterepo.CurrentSchemaGeneration)
	updateCheckService.SetPreActivateFence(func(ctx context.Context) (func(), error) {
		middleware.EngageProcessTrialHold()
		release := func() {
			middleware.ReleaseProcessTrialHold()
		}
		if collectorManager != nil {
			if err := collectorManager.QuiesceForTrialHold(ctx); err != nil {
				release()
				return nil, err
			}
		}
		if err := middleware.WaitOtherInflightMutations(ctx); err != nil {
			release()
			return nil, err
		}
		return release, nil
	})
	updateCheckService.SetPreSlotSnapshot(func(ctx context.Context, targetVersion string) (string, int, error) {
		if st == nil || st.SQLDB() == nil {
			return "", 0, errors.New("database store is unavailable")
		}
		dbPath := strings.TrimSpace(cfg.DBPath)
		lowerDB := strings.ToLower(dbPath)
		if dbPath == "" || dbPath == ":memory:" || strings.HasPrefix(lowerDB, "file::memory:") || strings.Contains(lowerDB, "mode=memory") {
			return "", 0, errors.New("on-disk database path is unavailable")
		}
		absDBPath, err := filepath.Abs(dbPath)
		if err != nil {
			return "", 0, fmt.Errorf("resolve database path: %w", err)
		}
		absDBPath = filepath.Clean(absDBPath)
		dataKeyPath := strings.TrimSpace(cfg.DataKeyPath)
		if dataKeyPath == "" {
			dataKeyPath = filepath.Join(filepath.Dir(absDBPath), "data.key")
		}
		backupsDir := filepath.Join(filepath.Dir(absDBPath), "backups")
		snapshotDir := filepath.Join(
			backupsDir,
			fmt.Sprintf(
				"pre-slot-%s-to-%s-%s",
				sanitizeSnapshotVersionSegment(buildinfo.Version),
				sanitizeSnapshotVersionSegment(targetVersion),
				time.Now().UTC().Format("20060102T150405.000000000Z"),
			),
		)
		if err := sqliterepo.CreateOnlineSnapshot(ctx, st.SQLDB(), absDBPath, dataKeyPath, snapshotDir); err != nil {
			return "", 0, err
		}
		if err := sqliterepo.PrunePreSlotSnapshots(backupsDir, 2, snapshotDir); err != nil {
			return "", 0, err
		}
		return snapshotDir, sqliterepo.CurrentSchemaGeneration, nil
	})
	return &Context{
		UpdateCheckService:   updateCheckService,
		Config:               cfg,
		Store:                st,
		Collector:            collectorManager,
		StartedAt:            startedAt,
		ServiceID:            serviceID,
		AdminAuthService:     adminauthsvc.New(cfg, st),
		SetupService:         setupsvc.New(cfg, st, collectorService, managerConfigService, startedAt, serviceID),
		ManagerConfigService: managerConfigService,
		CollectorService:     collectorService,
		UsageService:         usageService,
		DashboardService:     dashboardsvc.New(st, cfg.DashboardHourlyRollupEnabled),
		CodexInspectionService: codexinspectionsvc.NewWithOptions(
			st,
			managerConfigService,
			codexinspectionsvc.ServiceOptions{AuthFileMutationCoordinator: authFileMutationCoordinator},
		),
		MonitoringService:    monitoringsvc.New(st, cfg.DashboardHourlyRollupEnabled),
		QuotaSnapshotService: quotasnapshotsvc.New(st),
		ModelPriceService:    modelpricesvc.NewMultiSourceWithModelsDev(st, modelsDevModelPriceSyncURL, modelPriceSyncURL, openRouterModelPriceSyncURL, managerConfigService),
		APIKeyAliasService:   apikeyaliassvc.New(st),
		AccountActionService: accountactionsvc.NewWithMutationCoordinator(
			st,
			managerConfigService,
			authFileMutationCoordinator,
		),
		AccountProcessingPolicyService: accountProcessingPolicyService,
		AuthFileMutationCoordinator:    authFileMutationCoordinator,
		ProxyService: proxysvc.NewWithMutationCoordinator(
			managerConfigService,
			authFileMutationCoordinator,
			st,
		),
		PanelService:             panelsvc.New(cfg.PanelPath, embeddedPanel),
		AutomationRuntimeService: runtimeService,
	}
}

func sanitizeSnapshotVersionSegment(raw string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), ".-")
	if out == "" {
		return "unknown"
	}
	return out
}
