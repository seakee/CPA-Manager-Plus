package app

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	bootstrapsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type Options struct {
	EmbeddedPanel               fs.FS
	ModelsDevModelPriceSyncURL  *string
	ModelPriceSyncURL           *string
	OpenRouterModelPriceSyncURL *string
	ServiceID                   string
	StartedAt                   int64
}

func New(ctx context.Context, cfg config.Config, options Options) (*Context, error) {
	if cfg.DataKey == "" && cfg.DataKeyPath == "" {
		cfg.DataKeyPath = filepath.Join(filepath.Dir(cfg.DBPath), "data.key")
	}
	dataKey, dataKeyCreated, err := security.LoadOrCreateDataKey(cfg.DataKey, cfg.DataKeyPath)
	if err != nil {
		return nil, err
	}
	protector, err := security.NewProtector(dataKey)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(cfg.DBPath, protector)
	if err != nil {
		return nil, err
	}
	bootstrapResult, err := bootstrapsvc.Run(ctx, cfg, st, dataKeyCreated)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	manager := collector.NewManager(cfg, st)
	serviceID := options.ServiceID
	if serviceID == "" {
		serviceID = "cpa-manager-plus"
	}
	startedAt := options.StartedAt
	if startedAt <= 0 {
		startedAt = time.Now().UnixMilli()
	}
	appCtx := FromExistingWithModelsDev(
		cfg,
		st,
		manager,
		startedAt,
		options.EmbeddedPanel,
		options.ModelsDevModelPriceSyncURL,
		options.ModelPriceSyncURL,
		options.OpenRouterModelPriceSyncURL,
		serviceID,
	)
	appCtx.Bootstrap = bootstrapResult
	return appCtx, nil
}
