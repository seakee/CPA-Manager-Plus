package worker

import (
	"context"
	"log"

	collectorpkg "github.com/seakee/cpa-manager-plus/usage-service/internal/collector"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/config"
	collectorservice "github.com/seakee/cpa-manager-plus/usage-service/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/store"
)

type CollectorWorker struct {
	cfg              config.Config
	store            *store.Store
	collectorService *collectorservice.Service
}

func NewCollectorWorker(cfg config.Config, store *store.Store, collectorService *collectorservice.Service) *CollectorWorker {
	return &CollectorWorker{
		cfg:              cfg,
		store:            store,
		collectorService: collectorService,
	}
}

func (w *CollectorWorker) Start(ctx context.Context) {
	if w.cfg.CPAUpstreamURL != "" && w.cfg.ManagementKey != "" {
		_ = w.collectorService.StartRuntime(ctx, collectorpkg.RuntimeConfig{
			CPAUpstreamURL: w.cfg.CPAUpstreamURL,
			ManagementKey:  w.cfg.ManagementKey,
			CollectorMode:  w.cfg.CollectorMode,
			Queue:          w.cfg.Queue,
			PopSide:        w.cfg.PopSide,
			BatchSize:      w.cfg.BatchSize,
			PollInterval:   w.cfg.PollInterval,
			TLSSkipVerify:  w.cfg.TLSSkipVerify,
		})
		return
	}

	if managerCfg, ok, err := w.store.LoadManagerConfig(ctx); err == nil && ok &&
		managerCfg.CPAConnection.CPABaseURL != "" && managerCfg.CPAConnection.ManagementKey != "" {
		if collectorservice.ManagerCollectorEnabled(managerCfg) {
			_ = w.collectorService.StartRuntime(ctx, collectorservice.RuntimeConfigFromManagerConfigWithFallback(managerCfg, w.cfg))
		}
		return
	} else if err != nil {
		log.Printf("load manager config: %v", err)
		return
	}

	if setup, ok, err := w.store.LoadSetup(ctx); err == nil && ok {
		_ = w.collectorService.StartRuntime(ctx, collectorpkg.RuntimeConfig{
			CPAUpstreamURL: setup.CPAUpstreamURL,
			ManagementKey:  setup.ManagementKey,
			CollectorMode:  w.cfg.CollectorMode,
			Queue:          setup.Queue,
			PopSide:        setup.PopSide,
			BatchSize:      w.cfg.BatchSize,
			PollInterval:   w.cfg.PollInterval,
			TLSSkipVerify:  w.cfg.TLSSkipVerify,
		})
	} else if err != nil {
		log.Printf("load setup: %v", err)
	}
}

func (w *CollectorWorker) Stop(ctx context.Context) {
	if err := w.collectorService.Stop(ctx); err != nil {
		log.Printf("stop collector: %v", err)
	}
}
