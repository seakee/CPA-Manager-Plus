package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

const persistedConnectionStorageMigrationVersion = 2

// RequireExistingDataKeyForEncryptedCPAConnection prevents startup from
// generating a replacement data.key when the existing Manager database already
// contains an encrypted CPA connection. The raw storage invariant is checked
// first, even when a data key is present or supplied through the environment, so
// post-v2 malformed values cannot be reinterpreted as legacy plaintext.
func RequireExistingDataKeyForEncryptedCPAConnection(ctx context.Context, databasePath, rawDataKey, dataKeyPath string) error {
	if strings.TrimSpace(databasePath) == "" {
		return nil
	}
	inspection, err := InspectPersistedCPAConnectionStorage(ctx, databasePath)
	if err != nil {
		return err
	}
	return RequireDataKeyForInspectedConnection(inspection, rawDataKey, dataKeyPath)
}

// RequireDataKeyForInspectedConnection consumes the fenced preflight result;
// it does not reopen the source or create SQLite sidecars.
func RequireDataKeyForInspectedConnection(inspection PersistedCPAConnectionStorageInspection, rawDataKey, dataKeyPath string) error {
	if strings.TrimSpace(rawDataKey) != "" {
		return nil
	}
	if _, err := os.Stat(dataKeyPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat data key %s: %w", dataKeyPath, err)
	}
	if inspection.HasEncryptedConnection {
		return fmt.Errorf("encrypted CPA connection exists but data key is missing at %s", dataKeyPath)
	}
	return nil
}

// PersistedCPAConnectionStorageInspection is the result of a raw, pre-Store
// inspection of the bootstrap and connection settings.
type PersistedCPAConnectionStorageInspection struct {
	MigrationVersion       int  `json:"migrationVersion"`
	HasEncryptedConnection bool `json:"hasEncryptedConnection"`
}

// InspectPersistedCPAConnectionStorage reads the bootstrap marker and both
// persisted CPA connection rows without opening the normal protected Store.
// Before migration v2, non-envelope values remain legacy plaintext for
// compatibility. From v2 onward, every non-empty managementKey in either row
// must be a structurally valid encrypted envelope.
func InspectPersistedCPAConnectionStorage(ctx context.Context, databasePath string) (PersistedCPAConnectionStorageInspection, error) {
	inspection := PersistedCPAConnectionStorageInspection{}
	info, err := os.Stat(databasePath)
	if os.IsNotExist(err) {
		return inspection, nil
	}
	if err != nil {
		return inspection, fmt.Errorf("stat sqlite %s: %w", databasePath, err)
	}
	if !info.Mode().IsRegular() {
		return inspection, fmt.Errorf("SQLite database path is not a regular file: %s", databasePath)
	}
	if info.Size() == 0 {
		return inspection, nil
	}

	dsn, err := readOnlyDataSourceName(databasePath)
	if err != nil {
		return inspection, fmt.Errorf("prepare read-only sqlite inspection %s: %w", databasePath, err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return inspection, fmt.Errorf("open sqlite %s for data-key inspection: %w", databasePath, err)
	}
	defer db.Close()
	return inspectConnectionStorage(ctx, db, databasePath)
}

func inspectConnectionStorage(ctx context.Context, db *sql.DB, databasePath string) (PersistedCPAConnectionStorageInspection, error) {
	inspection := PersistedCPAConnectionStorageInspection{}

	var tableExists int
	if err := db.QueryRowContext(ctx, `select exists(select 1 from sqlite_schema where type = 'table' and name = 'settings')`).Scan(&tableExists); err != nil {
		return inspection, fmt.Errorf("inspect sqlite settings table %s: %w", databasePath, err)
	}
	if tableExists == 0 {
		return inspection, nil
	}

	rows, err := db.QueryContext(ctx, `select key, value from settings where key in ('bootstrap_state_v1', 'setup', 'manager_config_v1')`)
	if err != nil {
		return inspection, fmt.Errorf("inspect encrypted CPA connection in %s: %w", databasePath, err)
	}
	defer rows.Close()
	type persistedSetting struct {
		key           string
		managementKey string
	}
	settings := make([]persistedSetting, 0, 2)
	for rows.Next() {
		var key string
		var raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return inspection, fmt.Errorf("scan encrypted CPA connection in %s: %w", databasePath, err)
		}
		if key == "bootstrap_state_v1" {
			var state struct {
				ConnectionStorageMigrationVersion int `json:"connectionStorageMigrationVersion"`
			}
			if err := json.Unmarshal([]byte(raw), &state); err != nil {
				return inspection, fmt.Errorf("corrupted persisted CPA connection storage: invalid bootstrap state: %w", err)
			}
			inspection.MigrationVersion = state.ConnectionStorageMigrationVersion
			continue
		}
		managementKey, err := persistedManagementKey(key, raw)
		if err != nil {
			return inspection, fmt.Errorf("corrupted persisted CPA connection: %s: %w", key, err)
		}
		settings = append(settings, persistedSetting{key: key, managementKey: managementKey})
	}
	if err := rows.Err(); err != nil {
		return inspection, fmt.Errorf("read encrypted CPA connection in %s: %w", databasePath, err)
	}
	if inspection.MigrationVersion >= persistedConnectionStorageMigrationVersion {
		for _, setting := range settings {
			if setting.managementKey == "" {
				continue
			}
			if !security.IsValidProtectedEnvelope(setting.managementKey) {
				return inspection, fmt.Errorf("corrupted persisted CPA connection: %s managementKey is not a valid encrypted envelope", setting.key)
			}
		}
	}
	for _, setting := range settings {
		if security.IsValidProtectedEnvelope(setting.managementKey) {
			inspection.HasEncryptedConnection = true
			break
		}
	}
	return inspection, nil
}

// HasPersistedEncryptedCPAConnection keeps the original boolean inspection API
// for callers that only need to decide whether a data key is required.
func HasPersistedEncryptedCPAConnection(ctx context.Context, databasePath string) (bool, error) {
	inspection, err := InspectPersistedCPAConnectionStorage(ctx, databasePath)
	if err != nil {
		return false, err
	}
	return inspection.HasEncryptedConnection, nil
}

func readOnlyDataSourceName(databasePath string) (string, error) {
	dsn := dataSourceName(databasePath)
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("mode", "ro")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func persistedManagementKey(key, raw string) (string, error) {
	switch key {
	case "setup":
		var setup struct {
			ManagementKey string `json:"managementKey"`
		}
		if err := json.Unmarshal([]byte(raw), &setup); err != nil {
			return "", fmt.Errorf("invalid setup JSON: %w", err)
		}
		return setup.ManagementKey, nil
	case "manager_config_v1":
		var cfg struct {
			CPAConnection struct {
				ManagementKey string `json:"managementKey"`
			} `json:"cpaConnection"`
		}
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return "", fmt.Errorf("invalid manager_config_v1 JSON: %w", err)
		}
		return cfg.CPAConnection.ManagementKey, nil
	default:
		return "", nil
	}
}
