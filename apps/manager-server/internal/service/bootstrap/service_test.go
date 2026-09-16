package bootstrap

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/cpaconnection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"

	_ "modernc.org/sqlite"
)

func TestRunMigratesLegacySetupAndEncryptsSecrets(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	legacyStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacyStore.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: "http://cpa.local:8317",
		ManagementKey:  "management-key",
		Queue:          "usage",
		PopSide:        "right",
	}); err != nil {
		t.Fatalf("save legacy setup: %v", err)
	}
	if err := legacyStore.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	st, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("open protected store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})

	result, err := Run(context.Background(), config.Config{
		DBPath:        dbPath,
		Queue:         "usage",
		PopSide:       "right",
		BatchSize:     100,
		QueryLimit:    50000,
		CollectorMode: "auto",
	}, st, true)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !result.AdminCreated || result.GeneratedAdminKey == "" {
		t.Fatalf("admin credential result = %#v", result)
	}
	if !result.MigratedLegacy || !result.HasHistoricalData || !result.State.ProjectInitialized {
		t.Fatalf("bootstrap result = %#v", result)
	}

	credential, ok, err := st.LoadAdminCredential(context.Background())
	if err != nil || !ok {
		t.Fatalf("load admin credential ok=%v err=%v", ok, err)
	}
	if !security.VerifyAdminKey(credential, result.GeneratedAdminKey) {
		t.Fatal("generated admin key does not verify")
	}
	if security.VerifyAdminKey(credential, "management-key") {
		t.Fatal("cpa management key should not verify as admin key")
	}

	managerCfg, ok, err := st.LoadManagerConfig(context.Background())
	if err != nil || !ok {
		t.Fatalf("load migrated manager config ok=%v err=%v", ok, err)
	}
	if managerCfg.CPAConnection.CPABaseURL != "http://cpa.local:8317" ||
		managerCfg.CPAConnection.ManagementKey != "management-key" {
		t.Fatalf("migrated manager config = %#v", managerCfg)
	}

	for _, key := range []string{"setup", "manager_config_v1"} {
		raw := rawBootstrapSettingValue(t, dbPath, key)
		if strings.Contains(raw, "management-key") || !strings.Contains(raw, "enc:v1:") {
			t.Fatalf("%s setting was not encrypted: %s", key, raw)
		}
	}
}

func TestInitializeEmbeddedRuntimeDesiredStateFreshAndPreserved(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "manager.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := config.Config{RuntimeURL: "http://cpamp-runtime:9081", RuntimeTokenFile: "/run/cpamp/runtime-secret/token"}

	created, err := initializeEmbeddedRuntimeDesiredState(t.Context(), cfg, st)
	if err != nil || !created {
		t.Fatalf("initialize desired state created=%v err=%v", created, err)
	}
	initial, found, err := st.LoadEmbeddedRuntimeDesiredState(t.Context())
	if err != nil || !found || initial.DesiredLifecycle != model.EmbeddedRuntimeDesiredRunning || initial.Revision != 1 {
		t.Fatalf("initial desired state = %#v found=%v err=%v", initial, found, err)
	}
	stopped, err := st.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredStopped)
	if err != nil {
		t.Fatalf("set stopped desired state: %v", err)
	}
	created, err = initializeEmbeddedRuntimeDesiredState(t.Context(), cfg, st)
	if err != nil || created {
		t.Fatalf("reinitialize desired state created=%v err=%v", created, err)
	}
	preserved, found, err := st.LoadEmbeddedRuntimeDesiredState(t.Context())
	if err != nil || !found || preserved != stopped {
		t.Fatalf("preserved desired state = %#v found=%v err=%v want %#v", preserved, found, err, stopped)
	}
}

func TestInitializeEmbeddedRuntimeDesiredStateDoesNotTakeOverExternalAuthority(t *testing.T) {
	tests := []struct {
		name  string
		cfg   config.Config
		setup func(*testing.T, *store.Store)
	}{
		{
			name: "manager-only deployment",
			cfg:  config.Config{},
		},
		{
			name: "complete environment connection",
			cfg: config.Config{
				RuntimeURL:       "http://cpamp-runtime:9081",
				RuntimeTokenFile: "/run/cpamp/runtime-secret/token",
				CPAUpstreamURL:   "http://external-cpa:8317",
				ManagementKey:    "external-key",
			},
		},
		{
			name: "complete manager authority",
			cfg: config.Config{
				RuntimeURL:       "http://cpamp-runtime:9081",
				RuntimeTokenFile: "/run/cpamp/runtime-secret/token",
			},
			setup: func(t *testing.T, st *store.Store) {
				t.Helper()
				if err := st.SaveManagerConfig(t.Context(), store.ManagerConfig{CPAConnection: store.ManagerCPAConnectionConfig{
					CPABaseURL: "http://external-cpa:8317", ManagementKey: "external-key",
				}}); err != nil {
					t.Fatalf("save manager authority: %v", err)
				}
			},
		},
		{
			name: "complete legacy setup authority",
			cfg: config.Config{
				RuntimeURL:       "http://cpamp-runtime:9081",
				RuntimeTokenFile: "/run/cpamp/runtime-secret/token",
			},
			setup: func(t *testing.T, st *store.Store) {
				t.Helper()
				if err := st.SaveSetup(t.Context(), store.Setup{
					CPAUpstreamURL: "http://external-cpa:8317", ManagementKey: "external-key",
				}); err != nil {
					t.Fatalf("save legacy setup authority: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "manager.sqlite"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if test.setup != nil {
				test.setup(t, st)
			}
			created, err := initializeEmbeddedRuntimeDesiredState(t.Context(), test.cfg, st)
			if err != nil || created {
				t.Fatalf("initialize desired state created=%v err=%v", created, err)
			}
			if state, found, err := st.LoadEmbeddedRuntimeDesiredState(t.Context()); err != nil || found {
				t.Fatalf("unexpected desired state = %#v found=%v err=%v", state, found, err)
			}
		})
	}
}

func TestMigrateLegacySetupRepairsPartialManagerConfig(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	legacyStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacyStore.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{CPABaseURL: "http://cpa.local:8317/"},
		Collector:     store.ManagerCollectorConfig{Queue: "manager-queue"},
	}); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("save partial manager config: %v", err)
	}
	if err := legacyStore.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: "http://cpa.local:8317",
		ManagementKey:  "management-key",
		Queue:          "legacy-queue",
		PopSide:        "left",
	}); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("save legacy setup: %v", err)
	}
	if err := legacyStore.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	st, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("open protected store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	migrated, err := migrateLegacyConfig(context.Background(), config.Config{Queue: "usage", PopSide: "right"}, st)
	if err != nil {
		t.Fatalf("migrate legacy config: %v", err)
	}
	if !migrated {
		t.Fatal("migration was not reported")
	}
	managerCfg, ok, err := st.LoadManagerConfig(context.Background())
	if err != nil || !ok {
		t.Fatalf("load manager config ok=%v err=%v", ok, err)
	}
	if managerCfg.CPAConnection.CPABaseURL != "http://cpa.local:8317" ||
		managerCfg.CPAConnection.ManagementKey != "management-key" {
		t.Fatalf("repaired manager config = %#v", managerCfg.CPAConnection)
	}
	if managerCfg.Collector.Queue != "manager-queue" || managerCfg.Collector.PopSide != "left" {
		t.Fatalf("manager collector settings changed unexpectedly = %#v", managerCfg.Collector)
	}
	for _, key := range []string{"setup", "manager_config_v1"} {
		raw := rawBootstrapSettingValue(t, dbPath, key)
		if strings.Contains(raw, "management-key") || !strings.Contains(raw, "enc:v1:") {
			t.Fatalf("%s setting was not encrypted: %s", key, raw)
		}
	}
}

func TestMigrateLegacySetupKeepsCompleteManagerConfigAsAuthority(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	legacyStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacyStore.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    "http://manager-cpa.local:8317",
			ManagementKey: "manager-key",
		},
	}); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("save manager config: %v", err)
	}
	if err := legacyStore.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: "http://legacy-cpa.local:8317",
		ManagementKey:  "legacy-key",
	}); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("save legacy setup: %v", err)
	}
	if err := legacyStore.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	st, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("open protected store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if migrated, err := migrateLegacyConfig(context.Background(), config.Config{}, st); err != nil || !migrated {
		t.Fatalf("migration result migrated=%v err=%v", migrated, err)
	}
	managerCfg, ok, err := st.LoadManagerConfig(context.Background())
	if err != nil || !ok {
		t.Fatalf("load manager config ok=%v err=%v", ok, err)
	}
	if managerCfg.CPAConnection.CPABaseURL != "http://manager-cpa.local:8317" ||
		managerCfg.CPAConnection.ManagementKey != "manager-key" {
		t.Fatalf("manager config authority changed = %#v", managerCfg.CPAConnection)
	}
	managerRaw := rawBootstrapSettingValue(t, dbPath, "manager_config_v1")
	setupRaw := rawBootstrapSettingValue(t, dbPath, "setup")
	if !strings.Contains(managerRaw, "enc:v1:") || !strings.Contains(setupRaw, "enc:v1:") {
		t.Fatal("manager and legacy settings were not normalized to encrypted storage")
	}
	canonicalSetup, ok, err := st.LoadSetup(context.Background())
	if err != nil || !ok {
		t.Fatalf("load canonical setup ok=%v err=%v", ok, err)
	}
	if canonicalSetup.CPAUpstreamURL != managerCfg.CPAConnection.CPABaseURL ||
		canonicalSetup.ManagementKey != managerCfg.CPAConnection.ManagementKey {
		t.Fatalf("legacy setup did not follow manager config authority = %#v", canonicalSetup)
	}
}

func TestMigrateLegacyPartialConnectionRowsNormalizeSecretsWithoutCombiningRows(t *testing.T) {
	const (
		urlA = "http://cpa-a.local:8317"
		keyA = "partial-key-a"
		keyB = "partial-key-b"
	)
	tests := []struct {
		name           string
		manager        *store.ManagerConfig
		setup          *store.Setup
		wantManager    bool
		wantSetup      bool
		wantManagerURL string
		wantManagerKey string
		wantSetupURL   string
		wantSetupKey   string
	}{
		{
			name: "manager URL only and setup key only stay separate",
			manager: &store.ManagerConfig{CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL: urlA,
			}},
			setup:          &store.Setup{ManagementKey: keyA},
			wantManager:    true,
			wantSetup:      true,
			wantManagerURL: urlA,
			wantSetupKey:   keyA,
		},
		{
			name:         "setup key only is encrypted without creating manager connection",
			setup:        &store.Setup{ManagementKey: keyA},
			wantSetup:    true,
			wantSetupKey: keyA,
		},
		{
			name: "manager key only is encrypted without creating setup connection",
			manager: &store.ManagerConfig{CPAConnection: store.ManagerCPAConnectionConfig{
				ManagementKey: keyA,
			}},
			wantManager:    true,
			wantManagerKey: keyA,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
			legacyStore, err := store.Open(dbPath)
			if err != nil {
				t.Fatalf("open legacy store: %v", err)
			}
			if tt.manager != nil {
				if err := legacyStore.SaveManagerConfig(context.Background(), *tt.manager); err != nil {
					_ = legacyStore.Close()
					t.Fatalf("save manager config: %v", err)
				}
			}
			if err := legacyStore.Close(); err != nil {
				t.Fatalf("close legacy store: %v", err)
			}
			if tt.setup != nil {
				data, err := json.Marshal(tt.setup)
				if err != nil {
					t.Fatalf("marshal setup: %v", err)
				}
				if err := writeRawSetting(dbPath, "setup", string(data)); err != nil {
					t.Fatalf("write partial setup: %v", err)
				}
			}

			protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
			if err != nil {
				t.Fatalf("create protector: %v", err)
			}
			st, err := store.Open(dbPath, protector)
			if err != nil {
				t.Fatalf("open protected store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			migrated, err := migrateLegacyConfig(context.Background(), config.Config{}, st)
			if err != nil || migrated != (tt.wantManager || tt.wantSetup) {
				t.Fatalf("migration result migrated=%v err=%v", migrated, err)
			}

			managerCfg, managerOK, err := st.LoadManagerConfig(context.Background())
			if err != nil {
				t.Fatalf("load manager config: %v", err)
			}
			if managerOK != tt.wantManager {
				t.Fatalf("manager presence=%v want %v", managerOK, tt.wantManager)
			}
			if managerOK && (managerCfg.CPAConnection.CPABaseURL != tt.wantManagerURL ||
				managerCfg.CPAConnection.ManagementKey != tt.wantManagerKey) {
				t.Fatalf("manager connection=%#v want URL=%q key=%q",
					managerCfg.CPAConnection, tt.wantManagerURL, tt.wantManagerKey)
			}
			setup, setupOK, err := st.LoadSetup(context.Background())
			if err != nil {
				t.Fatalf("load setup: %v", err)
			}
			if setupOK != tt.wantSetup {
				t.Fatalf("setup presence=%v want %v", setupOK, tt.wantSetup)
			}
			if setupOK && (setup.CPAUpstreamURL != tt.wantSetupURL ||
				setup.ManagementKey != tt.wantSetupKey) {
				t.Fatalf("setup=%#v want URL=%q key=%q",
					setup, tt.wantSetupURL, tt.wantSetupKey)
			}
			for _, key := range []string{"setup", "manager_config_v1"} {
				raw, err := rawSettingValueIfPresent(dbPath, key)
				if err != nil {
					t.Fatalf("read raw %s: %v", key, err)
				}
				if strings.Contains(raw, keyA) || strings.Contains(raw, keyB) {
					t.Fatalf("%s retained plaintext secret: %s", key, raw)
				}
			}
			if tt.wantSetup {
				raw := rawBootstrapSettingValue(t, dbPath, "setup")
				if !strings.Contains(raw, "enc:v1:") {
					t.Fatalf("setup secret is not encrypted: %s", raw)
				}
			}
			if tt.wantManager && tt.wantManagerKey != "" {
				raw := rawBootstrapSettingValue(t, dbPath, "manager_config_v1")
				if !strings.Contains(raw, "enc:v1:") {
					t.Fatalf("manager secret is not encrypted: %s", raw)
				}
			}
		})
	}
}

func TestMigrateLegacyConfigRejectsOverlappingPartialConflicts(t *testing.T) {
	tests := []struct {
		name          string
		manager       store.ManagerConfig
		setup         store.Setup
		managerSecret string
		setupSecret   string
	}{
		{
			name: "URL conflict",
			manager: store.ManagerConfig{CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL: "http://manager-cpa.local:8317",
			}},
			setup: store.Setup{
				CPAUpstreamURL: "http://setup-cpa.local:8317",
			},
		},
		{
			name: "Management Key conflict",
			manager: store.ManagerConfig{CPAConnection: store.ManagerCPAConnectionConfig{
				ManagementKey: "manager-key",
			}},
			setup: store.Setup{
				ManagementKey: "setup-key",
			},
			managerSecret: "manager-key",
			setupSecret:   "setup-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
			legacyStore, err := store.Open(dbPath)
			if err != nil {
				t.Fatalf("open legacy store: %v", err)
			}
			if err := legacyStore.SaveManagerConfig(context.Background(), tt.manager); err != nil {
				_ = legacyStore.Close()
				t.Fatalf("save partial manager config: %v", err)
			}
			if err := legacyStore.Close(); err != nil {
				t.Fatalf("close legacy store: %v", err)
			}
			setupData, err := json.Marshal(tt.setup)
			if err != nil {
				t.Fatalf("marshal partial setup: %v", err)
			}
			if err := writeRawSetting(dbPath, "setup", string(setupData)); err != nil {
				t.Fatalf("write partial setup: %v", err)
			}

			protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
			if err != nil {
				t.Fatalf("create protector: %v", err)
			}
			st, err := store.Open(dbPath, protector)
			if err != nil {
				t.Fatalf("open protected store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })

			if _, err := migrateLegacyConfig(context.Background(), config.Config{}, st); err == nil || !strings.Contains(err.Error(), "conflict") {
				t.Fatalf("overlapping partial migration error = %v", err)
			}

			// Even though migration refuses to choose an authority, it still
			// normalizes both historical keys before returning the conflict.
			for _, key := range []string{"setup", "manager_config_v1"} {
				raw := rawBootstrapSettingValue(t, dbPath, key)
				if tt.managerSecret != "" || tt.setupSecret != "" {
					if !strings.Contains(raw, "enc:v1:") {
						t.Fatalf("%s was not normalized to an encrypted value: %s", key, raw)
					}
				}
				if tt.managerSecret != "" && strings.Contains(raw, tt.managerSecret) {
					t.Fatalf("%s retained manager plaintext key: %s", key, raw)
				}
				if tt.setupSecret != "" && strings.Contains(raw, tt.setupSecret) {
					t.Fatalf("%s retained setup plaintext key: %s", key, raw)
				}
			}
		})
	}
}

func TestRunDoesNotAdvanceMigrationVersionForPartialAuthorityConflict(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	legacyStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacyStore.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{CPABaseURL: "http://manager-a.local:8317"},
	}); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("save partial manager config: %v", err)
	}
	if err := writeRawSetting(dbPath, "setup",
		"{\"cpaBaseUrl\":\"http://manager-b.local:8317\",\"managementKey\":\"setup-key\"}"); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("write conflicting setup: %v", err)
	}
	if err := upsertRawBootstrapState(t, dbPath,
		"{\"version\":1,\"migratedLegacy\":true,\"connectionStorageMigrationVersion\":1}"); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("write old bootstrap state: %v", err)
	}
	if err := legacyStore.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	st, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("open protected store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := Run(context.Background(), config.Config{}, st, false); err == nil {
		t.Fatal("bootstrap accepted conflicting partial authority")
	}
	state, ok, err := st.LoadBootstrapState(context.Background())
	if err != nil {
		t.Fatalf("load bootstrap state: %v", err)
	}
	if !ok || state.ConnectionStorageMigrationVersion != 1 {
		t.Fatalf("migration version advanced after conflict: ok=%v state=%#v", ok, state)
	}
	setupRaw := rawBootstrapSettingValue(t, dbPath, "setup")
	if strings.Contains(setupRaw, "setup-key") || !strings.Contains(setupRaw, "enc:v1:") {
		t.Fatalf("setup was not normalized before conflict abort: %s", setupRaw)
	}
	managerRaw := rawBootstrapSettingValue(t, dbPath, "manager_config_v1")
	if strings.Contains(managerRaw, "setup-key") {
		t.Fatalf("manager retained setup plaintext secret: %s", managerRaw)
	}
}

func rawBootstrapSettingValue(t testing.TB, dbPath string, key string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer db.Close()

	var raw string
	if err := db.QueryRow(`select value from settings where key = ?`, key).Scan(&raw); err != nil {
		t.Fatalf("load raw setting %s: %v", key, err)
	}
	return raw
}

func rawSettingValueIfPresent(dbPath string, key string) (string, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var raw string
	err = db.QueryRow("select value from settings where key = ?", key).Scan(&raw)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return raw, err
}

func writeRawSetting(dbPath string, key string, value string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec("insert into settings (key, value, updated_at_ms) values (?, ?, 1) "+
		"on conflict(key) do update set value = excluded.value", key, value)
	return err
}

func TestRunNormalizesLegacyConflictDespiteOldMigratedLegacyFlag(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	legacyStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacyStore.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    "http://manager-cpa.local:8317",
			ManagementKey: "manager-key",
		},
		Collector: store.ManagerCollectorConfig{Queue: "manager-queue"},
	}); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("save manager config: %v", err)
	}
	if err := legacyStore.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: "http://legacy-cpa.local:8317",
		ManagementKey:  "legacy-key",
	}); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("save legacy setup: %v", err)
	}
	// Simulate a database migrated by an older release: MigratedLegacy is
	// already true and no connection-storage migration version exists.
	if err := upsertRawBootstrapState(t, dbPath, `{"version":1,"status":"migrated","adminReady":true,"projectInitialized":true,"dataKeyReady":true,"migratedLegacy":true,"hasHistoricalData":true,"updatedAtMs":1}`); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("write legacy bootstrap state: %v", err)
	}
	if err := legacyStore.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	st, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("open protected store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := config.Config{Queue: "usage", PopSide: "right"}
	result, err := Run(context.Background(), cfg, st, false)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !result.MigratedLegacy {
		t.Fatal("MigratedLegacy was downgraded by the versioned migration")
	}
	if result.State.ConnectionStorageMigrationVersion != currentConnectionStorageMigrationVersion {
		t.Fatalf("connection storage migration version = %d, want %d", result.State.ConnectionStorageMigrationVersion, currentConnectionStorageMigrationVersion)
	}

	// The normalization must actually run: the stale legacy setup follows the
	// manager config authority and both rows are stored encrypted.
	managerCfg, ok, err := st.LoadManagerConfig(context.Background())
	if err != nil || !ok {
		t.Fatalf("load manager config ok=%v err=%v", ok, err)
	}
	if managerCfg.CPAConnection.ManagementKey != "manager-key" ||
		managerCfg.Collector.Queue != "manager-queue" {
		t.Fatalf("manager config authority changed = %#v", managerCfg)
	}
	canonicalSetup, ok, err := st.LoadSetup(context.Background())
	if err != nil || !ok {
		t.Fatalf("load canonical setup ok=%v err=%v", ok, err)
	}
	if canonicalSetup.CPAUpstreamURL != "http://manager-cpa.local:8317" ||
		canonicalSetup.ManagementKey != "manager-key" {
		t.Fatalf("legacy setup did not follow manager config authority = %#v", canonicalSetup)
	}
	for _, key := range []string{"setup", "manager_config_v1"} {
		raw := rawBootstrapSettingValue(t, dbPath, key)
		if strings.Contains(raw, "manager-key") || strings.Contains(raw, "legacy-key") || !strings.Contains(raw, "enc:v1:") {
			t.Fatalf("%s setting was not encrypted: %s", key, raw)
		}
	}

	// A second run must be a no-op that keeps the normalized state.
	before := rawBootstrapSettingValue(t, dbPath, "setup")
	if _, err := Run(context.Background(), cfg, st, false); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	if after := rawBootstrapSettingValue(t, dbPath, "setup"); after != before {
		t.Fatal("second bootstrap rewrote the normalized setup row")
	}
}

func TestRunDoesNotMarkMigrationVersionWhenNormalizationFails(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	legacyStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacyStore.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    "http://manager-cpa.local:8317",
			ManagementKey: "manager-key",
		},
	}); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("save manager config: %v", err)
	}
	if err := legacyStore.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: "http://legacy-cpa.local:8317",
		ManagementKey:  "legacy-key",
	}); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("save legacy setup: %v", err)
	}
	if err := legacyStore.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	st, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("open protected store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	blockSetupWrites(t, dbPath)
	if _, err := Run(context.Background(), config.Config{}, st, false); err == nil {
		t.Fatal("bootstrap succeeded although the setup rewrite was blocked")
	}
	state, ok, err := st.LoadBootstrapState(context.Background())
	if err != nil {
		t.Fatalf("load bootstrap state: %v", err)
	}
	if ok && state.ConnectionStorageMigrationVersion != 0 {
		t.Fatalf("migration version = %d, want 0 after failed normalization", state.ConnectionStorageMigrationVersion)
	}

	// After the blocker is gone the migration must retry and complete.
	unblockSetupWrites(t, dbPath)
	result, err := Run(context.Background(), config.Config{}, st, false)
	if err != nil {
		t.Fatalf("bootstrap after unblock: %v", err)
	}
	if result.State.ConnectionStorageMigrationVersion != currentConnectionStorageMigrationVersion {
		t.Fatalf("migration version = %d, want %d after retry", result.State.ConnectionStorageMigrationVersion, currentConnectionStorageMigrationVersion)
	}
}

func upsertRawBootstrapState(t testing.TB, dbPath string, value string) error {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`insert into settings (key, value, updated_at_ms) values ('bootstrap_state_v1', ?, 1)
		on conflict(key) do update set value = excluded.value`, value)
	return err
}

func blockSetupWrites(t testing.TB, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer db.Close()
	for _, statement := range []string{
		`create trigger block_bootstrap_setup_insert before insert on settings
		 when new.key = 'setup' begin select raise(abort, 'setup write blocked'); end`,
		`create trigger block_bootstrap_setup_update before update on settings
		 when new.key = 'setup' begin select raise(abort, 'setup write blocked'); end`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("install setup write blocker: %v", err)
		}
	}
}

func unblockSetupWrites(t testing.TB, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer db.Close()
	for _, statement := range []string{
		`drop trigger if exists block_bootstrap_setup_insert`,
		`drop trigger if exists block_bootstrap_setup_update`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("remove setup write blocker: %v", err)
		}
	}
}

func TestRunDoesNotPersistAdminCredentialWhenMigrationFails(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	legacyStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacyStore.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{CPABaseURL: "http://manager-a.local:8317"},
	}); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("save partial manager config: %v", err)
	}
	if err := writeRawSetting(dbPath, "setup",
		"{\"cpaBaseUrl\":\"http://manager-b.local:8317\",\"managementKey\":\"setup-key\"}"); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("write conflicting setup: %v", err)
	}
	if err := legacyStore.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	st, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("open protected store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if _, err := Run(context.Background(), config.Config{}, st, false); err == nil {
		t.Fatal("bootstrap accepted conflicting partial authority")
	}
	// A generated admin credential must never be persisted when bootstrap
	// fails before disclosing it; the next boot must be free to generate and
	// disclose a fresh key instead of locking the operator out.
	if _, ok, err := st.LoadAdminCredential(context.Background()); err != nil || ok {
		t.Fatalf("failed bootstrap persisted admin credential: ok=%v err=%v", ok, err)
	}
}

func TestBootstrapSucceedsAfterExplicitConflictRepair(t *testing.T) {
	t.Setenv("CPA_UPSTREAM_URL", "")
	t.Setenv("CPA_MANAGEMENT_KEY", "")
	t.Setenv("CPA_MANAGER_CONFIG", filepath.Join(t.TempDir(), "missing-config.json"))
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	dataKeyPath := filepath.Join(dir, "data.key")
	legacyStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacyStore.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{CPABaseURL: "http://manager-a.local:8317"},
	}); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("save partial manager config: %v", err)
	}
	if err := writeRawSetting(dbPath, "setup",
		"{\"cpaBaseUrl\":\"http://manager-b.local:8317\",\"managementKey\":\"setup-key\"}"); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("write conflicting setup: %v", err)
	}
	if err := legacyStore.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	managementKeyPath := filepath.Join(dir, "cpa-management-key")
	if err := os.WriteFile(managementKeyPath, []byte("repaired-key\n"), 0o600); err != nil {
		t.Fatalf("write management key: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if err := cpaconnection.Run(context.Background(), []string{
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--cpa-base-url", "http://cpa-c.local:8317",
		"--management-key-file", managementKeyPath,
		"--repair-conflict",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("explicit repair: %v stderr=%s", err, stderr.String())
	}

	dataKey, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
	if err != nil {
		t.Fatalf("load data key: %v", err)
	}
	protector, err := security.NewProtector(dataKey)
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	st, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("open protected store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	result, err := Run(context.Background(), config.Config{}, st, false)
	if err != nil {
		t.Fatalf("bootstrap after repair: %v", err)
	}
	if !result.State.ProjectInitialized || result.State.ConnectionStorageMigrationVersion != 2 {
		t.Fatalf("bootstrap state after repair = %#v", result.State)
	}
	credential, ok, err := st.LoadAdminCredential(context.Background())
	if err != nil || !ok {
		t.Fatalf("load admin credential ok=%v err=%v", ok, err)
	}
	if result.GeneratedAdminKey == "" || !security.VerifyAdminKey(credential, result.GeneratedAdminKey) {
		t.Fatalf("generated admin key was not disclosed or does not verify: %#v", result)
	}
	managerCfg, ok, err := st.LoadManagerConfig(context.Background())
	if err != nil || !ok {
		t.Fatalf("load manager config ok=%v err=%v", ok, err)
	}
	if managerCfg.CPAConnection.CPABaseURL != "http://cpa-c.local:8317" ||
		managerCfg.CPAConnection.ManagementKey != "repaired-key" {
		t.Fatalf("manager config after bootstrap = %#v", managerCfg.CPAConnection)
	}
}

// TestRunReEncryptsLegacyPlaintextPrefixedWithEnvelopePrefix proves that a
// historical database whose plaintext CPA Management Key legitimately starts
// with "enc:v1:" (and therefore collides with the encrypted-value prefix) is
// migrated correctly: bootstrap reads it as legacy plaintext, re-encrypts it
// with the real data key, and the persisted raw setting no longer contains the
// original plaintext. See P0-2 in PR #585.
func TestRunReEncryptsLegacyPlaintextPrefixedWithEnvelopePrefix(t *testing.T) {
	const legacyPlaintextKey = "enc:v1:legacy-real-cpa-key"

	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	legacyStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacyStore.SaveManagerConfig(context.Background(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    "http://cpa-legacy.local:8317",
			ManagementKey: legacyPlaintextKey,
		},
	}); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("write prefix-colliding legacy manager config: %v", err)
	}
	// Write the legacy setup directly as a raw plaintext JSON row so the
	// migration boundary sees the same prefix-colliding plaintext exactly as an
	// older release would have persisted it in both connection rows.
	if err := writeRawSetting(dbPath, "setup",
		`{"cpaBaseUrl":"http://cpa-legacy.local:8317","managementKey":"`+legacyPlaintextKey+`","queue":"usage","popSide":"right"}`); err != nil {
		_ = legacyStore.Close()
		t.Fatalf("write prefix-colliding legacy setup: %v", err)
	}
	if err := legacyStore.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	st, err := store.Open(dbPath, protector)
	if err != nil {
		t.Fatalf("open protected store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := config.Config{Queue: "usage", PopSide: "right"}
	result, err := Run(context.Background(), cfg, st, false)
	if err != nil {
		t.Fatalf("bootstrap with prefix-colliding legacy key: %v", err)
	}
	if result.State.ConnectionStorageMigrationVersion != currentConnectionStorageMigrationVersion {
		t.Fatalf("connection storage migration version = %d, want %d", result.State.ConnectionStorageMigrationVersion, currentConnectionStorageMigrationVersion)
	}

	// The loader must return the original plaintext value, proving the
	// prefix-colliding key survived the migration instead of being misread as
	// ciphertext.
	setup, ok, err := st.LoadSetup(context.Background())
	if err != nil || !ok {
		t.Fatalf("load migrated setup ok=%v err=%v", ok, err)
	}
	if setup.CPAUpstreamURL != "http://cpa-legacy.local:8317" || setup.ManagementKey != legacyPlaintextKey {
		t.Fatalf("migrated setup = %#v", setup)
	}
	managerCfg, ok, err := st.LoadManagerConfig(context.Background())
	if err != nil || !ok {
		t.Fatalf("load migrated manager config ok=%v err=%v", ok, err)
	}
	if managerCfg.CPAConnection.ManagementKey != legacyPlaintextKey {
		t.Fatalf("migrated manager config key = %q", managerCfg.CPAConnection.ManagementKey)
	}

	// The raw persisted rows must not contain the original plaintext key and
	// must contain a real encrypted envelope.
	for _, key := range []string{"setup", "manager_config_v1"} {
		raw := rawBootstrapSettingValue(t, dbPath, key)
		if strings.Contains(raw, legacyPlaintextKey) {
			t.Fatalf("%s raw setting retained the prefix-colliding plaintext: %s", key, raw)
		}
		if !strings.Contains(raw, "enc:v1:") {
			t.Fatalf("%s raw setting is not encrypted: %s", key, raw)
		}
	}

	// A second bootstrap must still read the re-encrypted value correctly.
	if _, err := Run(context.Background(), cfg, st, false); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	managerCfg2, ok, err := st.LoadManagerConfig(context.Background())
	if err != nil || !ok || managerCfg2.CPAConnection.ManagementKey != legacyPlaintextKey {
		t.Fatalf("second bootstrap manager config = %#v ok=%v err=%v", managerCfg2, ok, err)
	}
}
