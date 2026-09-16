package setting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

const managerConfigKey = "manager_config_v1"
const embeddedRuntimeDesiredKey = "runtime_desired_v1"
const automationSettingsKey = "automation_settings_v1"
const adminCredentialKey = "admin_credential_v1"
const bootstrapStateKey = "bootstrap_state_v1"

type Repository interface {
	SaveManagerConfig(ctx context.Context, cfg model.ManagerConfig) error
	SaveManagerConfigAndSetup(ctx context.Context, cfg model.ManagerConfig, setup model.Setup) error
	NormalizeLegacyConnectionStorage(ctx context.Context, cfg model.ManagerConfig, managerPresent bool, setup model.Setup, setupPresent bool) error
	LoadManagerConfig(ctx context.Context) (model.ManagerConfig, bool, error)
	SetEmbeddedRuntimeDesiredLifecycle(ctx context.Context, desired model.EmbeddedRuntimeDesiredLifecycle) (model.EmbeddedRuntimeDesiredState, error)
	LoadEmbeddedRuntimeDesiredState(ctx context.Context) (model.EmbeddedRuntimeDesiredState, bool, error)
	SaveAutomationSettings(ctx context.Context, settings model.AutomationSettings) (model.AutomationSettings, error)
	LoadAutomationSettings(ctx context.Context) (model.AutomationSettings, bool, error)
	SaveSetup(ctx context.Context, setup model.Setup) error
	LoadSetup(ctx context.Context) (model.Setup, bool, error)
	SaveAdminCredential(ctx context.Context, credential model.AdminCredential) error
	LoadAdminCredential(ctx context.Context) (model.AdminCredential, bool, error)
	SaveBootstrapState(ctx context.Context, state model.BootstrapState) error
	LoadBootstrapState(ctx context.Context) (model.BootstrapState, bool, error)
	HasHistoricalData(ctx context.Context) (bool, error)
}

type repository struct {
	db        *sql.DB
	protector *security.Protector
}

func New(db *sql.DB, protector ...*security.Protector) Repository {
	var p *security.Protector
	if len(protector) > 0 {
		p = protector[0]
	}
	return &repository{db: db, protector: p}
}

func (r *repository) SaveSetup(ctx context.Context, setup model.Setup) error {
	data, err := r.marshalSetup(setup)
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

func (r *repository) SaveManagerConfigAndSetup(ctx context.Context, cfg model.ManagerConfig, setup model.Setup) error {
	cfg.UpdatedAtMS = time.Now().UnixMilli()
	cfgData, err := r.marshalManagerConfig(cfg)
	if err != nil {
		return err
	}
	setupData, err := r.marshalSetup(setup)
	if err != nil {
		return err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	updatedAtMS := cfg.UpdatedAtMS
	if updatedAtMS == 0 {
		updatedAtMS = time.Now().UnixMilli()
	}
	if err := upsertSetting(ctx, tx, managerConfigKey, cfgData, updatedAtMS); err != nil {
		return err
	}
	if err := upsertSetting(ctx, tx, "setup", setupData, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// NormalizeLegacyConnectionStorage is migration-only persistence. Unlike the
// public SaveSetup path it accepts incomplete historical rows, but it never
// permits a non-empty management key to be written without a protector. Both
// rows are written in one transaction so a failed normalization cannot leave
// only one side rewritten.
func (r *repository) NormalizeLegacyConnectionStorage(
	ctx context.Context,
	cfg model.ManagerConfig,
	managerPresent bool,
	setup model.Setup,
	setupPresent bool,
) error {
	if !managerPresent && !setupPresent {
		return nil
	}
	if r.protector == nil && ((managerPresent && strings.TrimSpace(cfg.CPAConnection.ManagementKey) != "") ||
		(setupPresent && strings.TrimSpace(setup.ManagementKey) != "")) {
		return errors.New("secret protector is required to normalize legacy CPA connection storage")
	}

	var managerData []byte
	managerUpdatedAtMS := time.Now().UnixMilli()
	if managerPresent {
		var err error
		managerData, err = r.marshalManagerConfig(cfg)
		if err != nil {
			return err
		}
		if cfg.UpdatedAtMS > 0 {
			managerUpdatedAtMS = cfg.UpdatedAtMS
		}
	}
	var setupData []byte
	if setupPresent {
		var err error
		setupData, err = r.marshalLegacySetup(setup)
		if err != nil {
			return err
		}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if managerPresent {
		if err := upsertSetting(ctx, tx, managerConfigKey, managerData, managerUpdatedAtMS); err != nil {
			return err
		}
	}
	if setupPresent {
		if err := upsertSetting(ctx, tx, "setup", setupData, time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	setup, err = r.unprotectSetup(setup)
	if err != nil {
		return model.Setup{}, false, err
	}
	return setup, true, nil
}

func (r *repository) SaveManagerConfig(ctx context.Context, cfg model.ManagerConfig) error {
	cfg.UpdatedAtMS = time.Now().UnixMilli()
	data, err := r.marshalManagerConfig(cfg)
	if err != nil {
		return err
	}
	return upsertSetting(ctx, r.db, managerConfigKey, data, cfg.UpdatedAtMS)
}

type settingWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func upsertSetting(ctx context.Context, writer settingWriter, key string, data []byte, updatedAtMS int64) error {
	_, err := writer.ExecContext(
		ctx,
		`insert into settings(key, value, updated_at_ms)
		 values(?, ?, ?)
		 on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		key,
		string(data),
		updatedAtMS,
	)
	return err
}

func (r *repository) marshalSetup(setup model.Setup) ([]byte, error) {
	if setup.CPAUpstreamURL == "" || setup.ManagementKey == "" {
		return nil, errors.New("cpaBaseUrl and managementKey are required")
	}
	protected, err := r.protectSetup(setup)
	if err != nil {
		return nil, err
	}
	return json.Marshal(protected)
}

func (r *repository) marshalLegacySetup(setup model.Setup) ([]byte, error) {
	protected, err := r.protectSetup(setup)
	if err != nil {
		return nil, err
	}
	return json.Marshal(protected)
}

func (r *repository) marshalManagerConfig(cfg model.ManagerConfig) ([]byte, error) {
	protected, err := r.protectManagerConfig(cfg)
	if err != nil {
		return nil, err
	}
	return json.Marshal(protected)
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
	cfg, err = r.unprotectManagerConfig(cfg)
	if err != nil {
		return model.ManagerConfig{}, false, err
	}
	return cfg, true, nil
}

func (r *repository) SetEmbeddedRuntimeDesiredLifecycle(
	ctx context.Context,
	desired model.EmbeddedRuntimeDesiredLifecycle,
) (model.EmbeddedRuntimeDesiredState, error) {
	if !desired.IsValid() {
		return model.EmbeddedRuntimeDesiredState{}, fmt.Errorf("invalid embedded Runtime desired lifecycle %q", desired)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.EmbeddedRuntimeDesiredState{}, err
	}
	defer func() { _ = tx.Rollback() }()

	current, found, err := loadEmbeddedRuntimeDesiredState(ctx, tx)
	if err != nil {
		return model.EmbeddedRuntimeDesiredState{}, err
	}
	if found && current.DesiredLifecycle == desired {
		if err := tx.Commit(); err != nil {
			return model.EmbeddedRuntimeDesiredState{}, err
		}
		return current, nil
	}

	revision := uint64(1)
	updatedAtMS := time.Now().UnixMilli()
	if found {
		if current.Revision == math.MaxUint64 {
			return model.EmbeddedRuntimeDesiredState{}, errors.New("embedded Runtime desired revision overflow")
		}
		revision = current.Revision + 1
		if updatedAtMS <= current.UpdatedAtMS {
			if current.UpdatedAtMS == math.MaxInt64 {
				return model.EmbeddedRuntimeDesiredState{}, errors.New("embedded Runtime desired timestamp overflow")
			}
			updatedAtMS = current.UpdatedAtMS + 1
		}
	}
	next := model.EmbeddedRuntimeDesiredState{
		DesiredLifecycle: desired,
		Revision:         revision,
		UpdatedAtMS:      updatedAtMS,
	}
	data, err := json.Marshal(next)
	if err != nil {
		return model.EmbeddedRuntimeDesiredState{}, err
	}
	if err := upsertSetting(ctx, tx, embeddedRuntimeDesiredKey, data, next.UpdatedAtMS); err != nil {
		return model.EmbeddedRuntimeDesiredState{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.EmbeddedRuntimeDesiredState{}, err
	}
	return next, nil
}

func (r *repository) LoadEmbeddedRuntimeDesiredState(ctx context.Context) (model.EmbeddedRuntimeDesiredState, bool, error) {
	return loadEmbeddedRuntimeDesiredState(ctx, r.db)
}

type settingQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadEmbeddedRuntimeDesiredState(
	ctx context.Context,
	queryer settingQueryer,
) (model.EmbeddedRuntimeDesiredState, bool, error) {
	var raw string
	err := queryer.QueryRowContext(ctx, `select value from settings where key = ?`, embeddedRuntimeDesiredKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.EmbeddedRuntimeDesiredState{}, false, nil
	}
	if err != nil {
		return model.EmbeddedRuntimeDesiredState{}, false, err
	}
	var state model.EmbeddedRuntimeDesiredState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return model.EmbeddedRuntimeDesiredState{}, false, err
	}
	if err := state.Validate(); err != nil {
		return model.EmbeddedRuntimeDesiredState{}, false, err
	}
	return state, true, nil
}

func (r *repository) SaveAutomationSettings(ctx context.Context, settings model.AutomationSettings) (model.AutomationSettings, error) {
	settings.UpdatedAtMS = time.Now().UnixMilli()
	data, err := json.Marshal(settings)
	if err != nil {
		return model.AutomationSettings{}, err
	}
	_, err = r.db.ExecContext(
		ctx,
		`insert into settings(key, value, updated_at_ms)
		 values(?, ?, ?)
		 on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		automationSettingsKey,
		string(data),
		settings.UpdatedAtMS,
	)
	if err != nil {
		return model.AutomationSettings{}, err
	}
	return settings, nil
}

func (r *repository) LoadAutomationSettings(ctx context.Context) (model.AutomationSettings, bool, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `select value from settings where key = ?`, automationSettingsKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.AutomationSettings{}, false, nil
	}
	if err != nil {
		return model.AutomationSettings{}, false, err
	}
	var settings model.AutomationSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return model.AutomationSettings{}, false, err
	}
	return settings, true, nil
}

func (r *repository) SaveAdminCredential(ctx context.Context, credential model.AdminCredential) error {
	if credential.KeyHash == "" || credential.Salt == "" {
		return errors.New("admin credential is required")
	}
	data, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(
		ctx,
		`insert into settings(key, value, updated_at_ms)
		 values(?, ?, ?)
		 on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		adminCredentialKey,
		string(data),
		time.Now().UnixMilli(),
	)
	return err
}

func (r *repository) LoadAdminCredential(ctx context.Context) (model.AdminCredential, bool, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `select value from settings where key = ?`, adminCredentialKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.AdminCredential{}, false, nil
	}
	if err != nil {
		return model.AdminCredential{}, false, err
	}
	var credential model.AdminCredential
	if err := json.Unmarshal([]byte(raw), &credential); err != nil {
		return model.AdminCredential{}, false, err
	}
	return credential, true, nil
}

func (r *repository) SaveBootstrapState(ctx context.Context, state model.BootstrapState) error {
	state.UpdatedAtMS = time.Now().UnixMilli()
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(
		ctx,
		`insert into settings(key, value, updated_at_ms)
		 values(?, ?, ?)
		 on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		bootstrapStateKey,
		string(data),
		state.UpdatedAtMS,
	)
	return err
}

func (r *repository) LoadBootstrapState(ctx context.Context) (model.BootstrapState, bool, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `select value from settings where key = ?`, bootstrapStateKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.BootstrapState{}, false, nil
	}
	if err != nil {
		return model.BootstrapState{}, false, err
	}
	var state model.BootstrapState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return model.BootstrapState{}, false, err
	}
	return state, true, nil
}

func (r *repository) HasHistoricalData(ctx context.Context) (bool, error) {
	tables := []string{"usage_events", "dead_letter_events", "model_prices", "api_key_aliases"}
	for _, table := range tables {
		var count int64
		if err := r.db.QueryRowContext(ctx, `select count(*) from `+table).Scan(&count); err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	var settingsCount int64
	if err := r.db.QueryRowContext(
		ctx,
		`select count(*) from settings where key in ('setup', ?)`,
		managerConfigKey,
	).Scan(&settingsCount); err != nil {
		return false, err
	}
	return settingsCount > 0, nil
}

func (r *repository) protectSetup(setup model.Setup) (model.Setup, error) {
	if r.protector == nil {
		return setup, nil
	}
	value, err := r.protector.ProtectString(setup.ManagementKey)
	if err != nil {
		return model.Setup{}, err
	}
	setup.ManagementKey = value
	return setup, nil
}

func (r *repository) unprotectSetup(setup model.Setup) (model.Setup, error) {
	if r.protector == nil {
		return setup, nil
	}
	value, err := r.protector.UnprotectString(setup.ManagementKey)
	if err != nil {
		return model.Setup{}, err
	}
	setup.ManagementKey = value
	return setup, nil
}

func (r *repository) protectManagerConfig(cfg model.ManagerConfig) (model.ManagerConfig, error) {
	if r.protector == nil {
		return cfg, nil
	}
	value, err := r.protector.ProtectString(cfg.CPAConnection.ManagementKey)
	if err != nil {
		return model.ManagerConfig{}, err
	}
	cfg.CPAConnection.ManagementKey = value
	return cfg, nil
}

func (r *repository) unprotectManagerConfig(cfg model.ManagerConfig) (model.ManagerConfig, error) {
	if r.protector == nil {
		return cfg, nil
	}
	value, err := r.protector.UnprotectString(cfg.CPAConnection.ManagementKey)
	if err != nil {
		return model.ManagerConfig{}, err
	}
	cfg.CPAConnection.ManagementKey = value
	return cfg, nil
}
