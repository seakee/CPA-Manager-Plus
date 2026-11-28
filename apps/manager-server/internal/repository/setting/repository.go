package setting

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
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
	// PatchManagerCollectorEnabled rewrites only the request-monitoring switch of
	// the stored configuration document, initializing a minimal document when
	// absent. Every other field, including keys this build does not know, is left
	// untouched.
	PatchManagerCollectorEnabled(ctx context.Context, enabled bool) (bool, error)
	PatchManagerCollectorConfig(ctx context.Context, patch model.ManagerCollectorPatch) (bool, error)
	PatchManagerRetentionConfig(ctx context.Context, patch model.ManagerRetentionPatch) (bool, error)
	// LoadPersistedCPAConnectionRevision reports only the persisted DB-owned
	// revision; effective env ownership and CPA instance identity remain separate.
	LoadPersistedCPAConnectionRevision(ctx context.Context) (string, bool, error)
	SetEmbeddedRuntimeDesiredLifecycle(ctx context.Context, desired model.EmbeddedRuntimeDesiredLifecycle) (model.EmbeddedRuntimeDesiredState, error)
	SeedEmbeddedRuntimeDesiredLifecycleIfAbsent(ctx context.Context, desired model.EmbeddedRuntimeDesiredLifecycle) (bool, error)
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
	return r.saveWithConnectionRevision(ctx, false, func(tx *sql.Tx) error {
		return upsertSetting(ctx, tx, "setup", data, time.Now().UnixMilli())
	})
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

	return r.saveWithConnectionRevision(ctx, true, func(tx *sql.Tx) error {
		if err := upsertSetting(ctx, tx, managerConfigKey, cfgData, cfg.UpdatedAtMS); err != nil {
			return err
		}
		if cfg.Collector.Enabled != nil && !*cfg.Collector.Enabled {
			if err := recordMonitoringDisabledInTx(ctx, tx, cfg.UpdatedAtMS); err != nil {
				return err
			}
		}
		return upsertSetting(ctx, tx, "setup", setupData, time.Now().UnixMilli())
	})
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

	// Legacy normalization must not mint a fresh trusted revision for history.
	return r.saveWithConnectionRevision(ctx, false, func(tx *sql.Tx) error {
		if managerPresent {
			if err := upsertSetting(ctx, tx, managerConfigKey, managerData, managerUpdatedAtMS); err != nil {
				return err
			}
		}
		if setupPresent {
			return upsertSetting(ctx, tx, "setup", setupData, time.Now().UnixMilli())
		}
		return nil
	})
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
	return r.saveWithConnectionRevision(ctx, true, func(tx *sql.Tx) error {
		if err := upsertSetting(ctx, tx, managerConfigKey, data, cfg.UpdatedAtMS); err != nil {
			return err
		}
		if cfg.Collector.Enabled != nil && !*cfg.Collector.Enabled {
			return recordMonitoringDisabledInTx(ctx, tx, cfg.UpdatedAtMS)
		}
		return nil
	})
}

// PatchManagerCollectorEnabled writes the request-monitoring switch as one
// field of the stored document. A whole-document save would re-encode every
// field through this build's model and silently drop whatever it does not
// model, so the switch gets a narrow write instead: the document is patched as
// JSON, the stored credential envelope is carried through untouched because it
// is never decrypted here, and the connection revision is left alone because
// the connection itself did not change.
func (r *repository) PatchManagerCollectorEnabled(ctx context.Context, enabled bool) (bool, error) {
	updatedAtMS := time.Now().UnixMilli()
	patched := false
	err := r.saveWithConnectionRevision(ctx, false, func(tx *sql.Tx) error {
		var raw string
		switch err := tx.QueryRowContext(ctx, `select value from settings where key = ?`, managerConfigKey).Scan(&raw); {
		case errors.Is(err, sql.ErrNoRows):
			raw = "{}"
		case err != nil:
			return err
		}
		data, err := patchCollectorEnabled([]byte(raw), enabled, updatedAtMS)
		if err != nil {
			return err
		}
		patched = true
		if err := upsertSetting(ctx, tx, managerConfigKey, data, updatedAtMS); err != nil {
			return err
		}
		if !enabled {
			return recordMonitoringDisabledInTx(ctx, tx, updatedAtMS)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return patched, nil
}

func recordMonitoringDisabledInTx(ctx context.Context, tx *sql.Tx, nowMS int64) error {
	if err := upsertSetting(ctx, tx, sqliterepo.FreshUsageMonitoringDisabledGapKey, []byte("1"), nowMS); err != nil {
		return err
	}
	var rawSeq string
	var seq int64
	switch err := tx.QueryRowContext(ctx, `select value from settings where key = ?`, sqliterepo.FreshUsageMonitoringDisableSeqKey).Scan(&rawSeq); {
	case errors.Is(err, sql.ErrNoRows):
		seq = 0
	case err != nil:
		return err
	default:
		parsed, parseErr := strconv.ParseInt(strings.TrimSpace(rawSeq), 10, 64)
		if parseErr != nil || parsed < 0 || parsed == math.MaxInt64 {
			return fmt.Errorf("invalid %s setting value", sqliterepo.FreshUsageMonitoringDisableSeqKey)
		}
		seq = parsed
	}
	seq++
	return upsertSetting(ctx, tx, sqliterepo.FreshUsageMonitoringDisableSeqKey, []byte(strconv.FormatInt(seq, 10)), nowMS)
}

// patchCollectorEnabled replaces the `collector.enabled` leaf of a stored
// configuration document and nothing else. Values are kept as raw JSON so a
// field written by another build survives the write byte for byte.
func patchCollectorEnabled(raw []byte, enabled bool, updatedAtMS int64) ([]byte, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	if document == nil {
		return nil, errors.New("manager_config_v1 must be a JSON object")
	}
	var collector map[string]json.RawMessage
	if existing, ok := document["collector"]; ok && len(existing) > 0 && !bytes.Equal(bytes.TrimSpace(existing), []byte("null")) {
		if err := json.Unmarshal(existing, &collector); err != nil {
			return nil, err
		}
	}
	if collector == nil {
		collector = map[string]json.RawMessage{}
	}
	collector["enabled"] = json.RawMessage(strconv.FormatBool(enabled))
	encoded, err := json.Marshal(collector)
	if err != nil {
		return nil, err
	}
	document["collector"] = encoded
	document["updatedAtMs"] = json.RawMessage(strconv.FormatInt(updatedAtMS, 10))
	return json.Marshal(document)
}

func (r *repository) PatchManagerCollectorConfig(ctx context.Context, patch model.ManagerCollectorPatch) (bool, error) {
	updatedAtMS := time.Now().UnixMilli()
	patched := false
	err := r.saveWithConnectionRevision(ctx, false, func(tx *sql.Tx) error {
		var raw string
		switch err := tx.QueryRowContext(ctx, `select value from settings where key = ?`, managerConfigKey).Scan(&raw); {
		case errors.Is(err, sql.ErrNoRows):
			raw = "{}"
		case err != nil:
			return err
		}
		data, err := patchCollectorDocument([]byte(raw), patch, updatedAtMS)
		if err != nil {
			return err
		}
		patched = true
		return upsertSetting(ctx, tx, managerConfigKey, data, updatedAtMS)
	})
	if err != nil {
		return false, err
	}
	return patched, nil
}

func (r *repository) PatchManagerRetentionConfig(ctx context.Context, patch model.ManagerRetentionPatch) (bool, error) {
	updatedAtMS := time.Now().UnixMilli()
	patched := false
	err := r.saveWithConnectionRevision(ctx, false, func(tx *sql.Tx) error {
		var raw string
		switch err := tx.QueryRowContext(ctx, `select value from settings where key = ?`, managerConfigKey).Scan(&raw); {
		case errors.Is(err, sql.ErrNoRows):
			raw = "{}"
		case err != nil:
			return err
		}
		data, err := patchRetentionDocument([]byte(raw), patch, updatedAtMS)
		if err != nil {
			return err
		}
		patched = true
		return upsertSetting(ctx, tx, managerConfigKey, data, updatedAtMS)
	})
	if err != nil {
		return false, err
	}
	return patched, nil
}

func patchCollectorDocument(raw []byte, patch model.ManagerCollectorPatch, updatedAtMS int64) ([]byte, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	if document == nil {
		return nil, errors.New("manager_config_v1 must be a JSON object")
	}
	var collector map[string]json.RawMessage
	if existing, ok := document["collector"]; ok && len(existing) > 0 && !bytes.Equal(bytes.TrimSpace(existing), []byte("null")) {
		if err := json.Unmarshal(existing, &collector); err != nil {
			return nil, err
		}
	}
	if collector == nil {
		collector = map[string]json.RawMessage{}
	}
	if patch.CollectorMode != nil {
		encoded, err := json.Marshal(*patch.CollectorMode)
		if err != nil {
			return nil, err
		}
		collector["collectorMode"] = encoded
	}
	if patch.Queue != nil {
		encoded, err := json.Marshal(*patch.Queue)
		if err != nil {
			return nil, err
		}
		collector["queue"] = encoded
	}
	if patch.PopSide != nil {
		encoded, err := json.Marshal(*patch.PopSide)
		if err != nil {
			return nil, err
		}
		collector["popSide"] = encoded
	}
	if patch.BatchSize != nil {
		collector["batchSize"] = json.RawMessage(strconv.Itoa(*patch.BatchSize))
	}
	if patch.PollIntervalMS != nil {
		collector["pollIntervalMs"] = json.RawMessage(strconv.Itoa(*patch.PollIntervalMS))
	}
	if patch.QueryLimit != nil {
		collector["queryLimit"] = json.RawMessage(strconv.Itoa(*patch.QueryLimit))
	}
	if patch.TLSSkipVerify != nil {
		collector["tlsSkipVerify"] = json.RawMessage(strconv.FormatBool(*patch.TLSSkipVerify))
	}
	encoded, err := json.Marshal(collector)
	if err != nil {
		return nil, err
	}
	document["collector"] = encoded
	document["updatedAtMs"] = json.RawMessage(strconv.FormatInt(updatedAtMS, 10))
	return json.Marshal(document)
}

func patchRetentionDocument(raw []byte, patch model.ManagerRetentionPatch, updatedAtMS int64) ([]byte, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	if document == nil {
		return nil, errors.New("manager_config_v1 must be a JSON object")
	}
	var retention map[string]json.RawMessage
	if existing, ok := document["retention"]; ok && len(existing) > 0 && !bytes.Equal(bytes.TrimSpace(existing), []byte("null")) {
		if err := json.Unmarshal(existing, &retention); err != nil {
			return nil, err
		}
	}
	if retention == nil {
		retention = map[string]json.RawMessage{}
	}
	if patch.Enabled != nil {
		retention["enabled"] = json.RawMessage(strconv.FormatBool(*patch.Enabled))
	}
	if patch.RetentionDays != nil {
		retention["retentionDays"] = json.RawMessage(strconv.Itoa(*patch.RetentionDays))
	}
	delete(retention, "envLocked")
	delete(retention, "enabledEnvLocked")
	delete(retention, "retentionDaysEnvLocked")
	encoded, err := json.Marshal(retention)
	if err != nil {
		return nil, err
	}
	document["retention"] = encoded
	document["updatedAtMs"] = json.RawMessage(strconv.FormatInt(updatedAtMS, 10))
	return json.Marshal(document)
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
	cfg.Retention.EnvLocked = false
	cfg.Retention.EnabledEnvLocked = false
	cfg.Retention.RetentionDaysEnvLocked = false
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
	cfg.Retention.EnvLocked = false
	cfg.Retention.EnabledEnvLocked = false
	cfg.Retention.RetentionDaysEnvLocked = false
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

// SeedEmbeddedRuntimeDesiredLifecycleIfAbsent atomically inserts an initial
// revision-1 desired lifecycle row only when runtime_desired_v1 does not yet
// exist, never overwriting an existing operator Stop or unreadable row.
func (r *repository) SeedEmbeddedRuntimeDesiredLifecycleIfAbsent(
	ctx context.Context,
	desired model.EmbeddedRuntimeDesiredLifecycle,
) (bool, error) {
	if !desired.IsValid() {
		return false, fmt.Errorf("invalid embedded Runtime desired lifecycle %q", desired)
	}
	initial := model.EmbeddedRuntimeDesiredState{
		DesiredLifecycle: desired,
		Revision:         1,
		UpdatedAtMS:      time.Now().UnixMilli(),
	}
	data, err := json.Marshal(initial)
	if err != nil {
		return false, err
	}
	result, err := r.db.ExecContext(
		ctx,
		`insert into settings(key, value, updated_at_ms)
		 values(?, ?, ?)
		 on conflict(key) do nothing`,
		embeddedRuntimeDesiredKey,
		string(data),
		initial.UpdatedAtMS,
	)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
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
