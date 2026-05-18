package setting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/model"
)

const managerConfigKey = "manager_config_v1"

type Repository interface {
	SaveManagerConfig(ctx context.Context, cfg model.ManagerConfig) error
	LoadManagerConfig(ctx context.Context) (model.ManagerConfig, bool, error)
	SaveSetup(ctx context.Context, setup model.Setup) error
	LoadSetup(ctx context.Context) (model.Setup, bool, error)
}

type repository struct {
	db *sql.DB
}

func New(db *sql.DB) Repository {
	return &repository{db: db}
}

func (r *repository) SaveSetup(ctx context.Context, setup model.Setup) error {
	if setup.CPAUpstreamURL == "" || setup.ManagementKey == "" {
		return errors.New("cpaBaseUrl and managementKey are required")
	}
	data, err := json.Marshal(setup)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(
		ctx,
		`insert into settings(key, value, updated_at_ms)
		 values('setup', ?, ?)
		 on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		string(data),
		time.Now().UnixMilli(),
	)
	return err
}

func (r *repository) LoadSetup(ctx context.Context) (model.Setup, bool, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `select value from settings where key = 'setup'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Setup{}, false, nil
	}
	if err != nil {
		return model.Setup{}, false, err
	}
	var setup model.Setup
	if err := json.Unmarshal([]byte(raw), &setup); err != nil {
		return model.Setup{}, false, err
	}
	return setup, true, nil
}

func (r *repository) SaveManagerConfig(ctx context.Context, cfg model.ManagerConfig) error {
	cfg.UpdatedAtMS = time.Now().UnixMilli()
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(
		ctx,
		`insert into settings(key, value, updated_at_ms)
		 values(?, ?, ?)
		 on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		managerConfigKey,
		string(data),
		cfg.UpdatedAtMS,
	)
	return err
}

func (r *repository) LoadManagerConfig(ctx context.Context) (model.ManagerConfig, bool, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `select value from settings where key = ?`, managerConfigKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ManagerConfig{}, false, nil
	}
	if err != nil {
		return model.ManagerConfig{}, false, err
	}
	var cfg model.ManagerConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return model.ManagerConfig{}, false, err
	}
	return cfg, true, nil
}
