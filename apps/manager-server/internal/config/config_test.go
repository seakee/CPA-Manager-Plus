package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLoadCreatesDefaultConfig(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	t.Setenv(configEnvKey, configPath)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != "0.0.0.0:18317" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if want := filepath.Join(dir, "data", "usage.sqlite"); cfg.DBPath != want {
		t.Fatalf("DBPath = %q, want %q", cfg.DBPath, want)
	}
	if !cfg.DashboardHourlyRollupEnabled {
		t.Fatal("DashboardHourlyRollupEnabled = false by default")
	}
	if want := filepath.Join(dir, "data", "usage-archives"); cfg.UsageArchiveDir != want {
		t.Fatalf("UsageArchiveDir = %q, want %q", cfg.UsageArchiveDir, want)
	}
	if cfg.UsageImportChunkBytes != DefaultUsageImportChunkBytes ||
		cfg.UsageImportDiskQuotaBytes != DefaultUsageImportDiskQuotaBytes ||
		cfg.UsageImportMaxSessions != DefaultUsageImportMaxSessions ||
		cfg.UsageImportSessionTTL != DefaultUsageImportSessionTTL ||
		cfg.UsageArchiveRetentionEnabled || cfg.UsageArchiveRetentionDays != DefaultUsageArchiveRetentionDays {
		t.Fatalf("usage import defaults = %#v", cfg)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	if !strings.Contains(string(data), `"dataDir": "./data"`) {
		t.Fatalf("generated config does not contain relative dataDir: %s", data)
	}
}

func TestLoadWithoutCreatingDefaultDoesNotCreateConfig(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	t.Setenv(configEnvKey, configPath)

	cfg, err := LoadWithoutCreatingDefault()
	if err != nil {
		t.Fatalf("LoadWithoutCreatingDefault() error = %v", err)
	}
	if want := filepath.Join(dir, "data", "usage.sqlite"); cfg.DBPath != want {
		t.Fatalf("DBPath = %q, want %q", cfg.DBPath, want)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config file exists or stat failed: %v", err)
	}
}

func TestLoadReadsConfigAndResolvesRelativePaths(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	secretPath := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("secret-value\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "httpAddr": "127.0.0.1:19000",
  "dataDir": "state",
  "cpaUpstreamUrl": "http://cpa.local:8317",
  "managementKeyFile": "secret.txt",
  "collectorMode": "http",
  "queue": "custom-usage",
  "popSide": "left",
  "batchSize": 7,
	  "pollIntervalMs": 250,
	  "queryLimit": 900,
	  "pprofAddr": "127.0.0.1:6060",
	  "panelPath": "panel.html",
  "corsOrigins": ["http://panel.local"],
  "tlsSkipVerify": true,
  "quotaCooldownEnabled": true,
	  "accountActionsEnabled": true,
	  "accountActionsAutoDisable": true,
	  "usageImportChunkBytes": 1048576,
	  "usageImportDiskQuotaBytes": 1073741824,
	  "usageImportMaxSessions": 3,
	  "usageImportSessionTTLMinutes": 120,
	  "usageArchiveRetentionEnabled": true,
	  "usageArchiveRetentionDays": 45
}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv(configEnvKey, configPath)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:19000" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if want := filepath.Join(dir, "state", "usage.sqlite"); cfg.DBPath != want {
		t.Fatalf("DBPath = %q, want %q", cfg.DBPath, want)
	}
	if cfg.CPAUpstreamURL != "http://cpa.local:8317" {
		t.Fatalf("CPAUpstreamURL = %q", cfg.CPAUpstreamURL)
	}
	if cfg.ManagementKey != "secret-value" {
		t.Fatalf("ManagementKey = %q", cfg.ManagementKey)
	}
	if cfg.CollectorMode != "http" || cfg.Queue != "custom-usage" || cfg.PopSide != "left" {
		t.Fatalf("collector config = %#v", cfg)
	}
	if cfg.BatchSize != 7 || cfg.PollInterval != 250*time.Millisecond || cfg.QueryLimit != 900 {
		t.Fatalf("numeric config = %#v", cfg)
	}
	if cfg.PprofAddr != "127.0.0.1:6060" {
		t.Fatalf("PprofAddr = %q", cfg.PprofAddr)
	}
	if want := filepath.Join(dir, "panel.html"); cfg.PanelPath != want {
		t.Fatalf("PanelPath = %q, want %q", cfg.PanelPath, want)
	}
	if len(cfg.CORSOrigins) != 1 || cfg.CORSOrigins[0] != "http://panel.local" {
		t.Fatalf("CORSOrigins = %#v", cfg.CORSOrigins)
	}
	if !cfg.TLSSkipVerify {
		t.Fatal("TLSSkipVerify = false")
	}
	if !cfg.QuotaCooldownEnabled {
		t.Fatal("QuotaCooldownEnabled = false")
	}
	if !cfg.AccountActionsEnabled {
		t.Fatal("AccountActionsEnabled = false")
	}
	if !cfg.AccountActionsAutoDisable {
		t.Fatal("AccountActionsAutoDisable = false")
	}
	if cfg.UsageImportChunkBytes != 1048576 || cfg.UsageImportDiskQuotaBytes != 1073741824 ||
		cfg.UsageImportMaxSessions != 3 || cfg.UsageImportSessionTTL != 2*time.Hour {
		t.Fatalf("usage import config = %#v", cfg)
	}
	if !cfg.UsageArchiveRetentionEnabled || cfg.UsageArchiveRetentionDays != 45 {
		t.Fatalf("usage archive retention config = %#v", cfg)
	}
	if want := filepath.Join(dir, "state", "usage-archives"); cfg.UsageArchiveDir != want {
		t.Fatalf("UsageArchiveDir = %q, want %q", cfg.UsageArchiveDir, want)
	}
}

func TestLoadEnvOverridesConfig(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
  "httpAddr": "127.0.0.1:19000",
  "dataDir": "state",
  "managementKeyFile": "secret.txt",
  "batchSize": 7
}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv(configEnvKey, configPath)
	t.Setenv("HTTP_ADDR", "127.0.0.1:19001")
	t.Setenv("USAGE_DATA_DIR", filepath.Join(dir, "env-data"))
	t.Setenv("CPA_MANAGEMENT_KEY", "env-secret")
	t.Setenv("USAGE_BATCH_SIZE", "12")
	t.Setenv("CPA_MANAGER_PPROF_ADDR", "[::1]:6061")
	t.Setenv("USAGE_DASHBOARD_HOURLY_ROLLUP_ENABLED", "false")
	t.Setenv("USAGE_IMPORT_CHUNK_BYTES", "2097152")
	t.Setenv("USAGE_IMPORT_DISK_QUOTA_BYTES", "2147483648")
	t.Setenv("USAGE_IMPORT_MAX_SESSIONS", "4")
	t.Setenv("USAGE_IMPORT_SESSION_TTL_MINUTES", "30")
	t.Setenv("USAGE_ARCHIVE_RETENTION_ENABLED", "true")
	t.Setenv("USAGE_ARCHIVE_RETENTION_DAYS", "60")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:19001" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if want := filepath.Join(dir, "env-data", "usage.sqlite"); cfg.DBPath != want {
		t.Fatalf("DBPath = %q, want %q", cfg.DBPath, want)
	}
	if cfg.ManagementKey != "env-secret" {
		t.Fatalf("ManagementKey = %q", cfg.ManagementKey)
	}
	if cfg.BatchSize != 12 {
		t.Fatalf("BatchSize = %d", cfg.BatchSize)
	}
	if cfg.PprofAddr != "[::1]:6061" {
		t.Fatalf("PprofAddr = %q", cfg.PprofAddr)
	}
	if cfg.DashboardHourlyRollupEnabled {
		t.Fatal("DashboardHourlyRollupEnabled = true, want false")
	}
	if cfg.UsageImportChunkBytes != 2097152 || cfg.UsageImportDiskQuotaBytes != 2147483648 ||
		cfg.UsageImportMaxSessions != 4 || cfg.UsageImportSessionTTL != 30*time.Minute {
		t.Fatalf("usage import env config = %#v", cfg)
	}
	if !cfg.UsageArchiveRetentionEnabled || cfg.UsageArchiveRetentionDays != 60 {
		t.Fatalf("usage archive retention env config = %#v", cfg)
	}
	if want := filepath.Join(dir, "env-data", "usage-archives"); cfg.UsageArchiveDir != want {
		t.Fatalf("UsageArchiveDir = %q, want %q", cfg.UsageArchiveDir, want)
	}
}

func TestLoadRejectsInvalidUsageArchiveRetentionDays(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "zero", value: "0"},
		{name: "negative", value: "-1"},
		{name: "not an integer", value: "thirty"},
		{name: "integer overflow", value: "9223372036854775808"},
		{name: "duration overflow", value: strconv.FormatInt(maxUsageArchiveRetentionDays+1, 10)},
	} {
		t.Run("environment/"+test.name, func(t *testing.T) {
			clearConfigEnv(t)
			dir := t.TempDir()
			t.Setenv(configEnvKey, filepath.Join(dir, "missing-config.json"))
			t.Setenv("USAGE_ARCHIVE_RETENTION_ENABLED", "true")
			t.Setenv("USAGE_ARCHIVE_RETENTION_DAYS", test.value)
			if _, err := LoadWithoutCreatingDefault(); err == nil {
				t.Fatalf("retention days %q unexpectedly loaded", test.value)
			}
		})
	}

	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "zero", value: "0"},
		{name: "negative", value: "-1"},
		{name: "duration overflow", value: strconv.FormatInt(maxUsageArchiveRetentionDays+1, 10)},
	} {
		t.Run("config/"+test.name, func(t *testing.T) {
			clearConfigEnv(t)
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.json")
			contents := `{"usageArchiveRetentionEnabled":true,"usageArchiveRetentionDays":` + test.value + `}`
			if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			t.Setenv(configEnvKey, configPath)
			if _, err := Load(); err == nil {
				t.Fatalf("retention days %q unexpectedly loaded", test.value)
			}
		})
	}
}

func TestLoadRetentionDaysEnvironmentOverridesInvalidFileValue(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"usageArchiveRetentionEnabled":true,"usageArchiveRetentionDays":0}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv(configEnvKey, configPath)
	t.Setenv("USAGE_ARCHIVE_RETENTION_DAYS", "45")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load overridden retention days: %v", err)
	}
	if !cfg.UsageArchiveRetentionEnabled || cfg.UsageArchiveRetentionDays != 45 {
		t.Fatalf("overridden retention config = %#v", cfg)
	}
}

func TestLoadCustomDBPathSuppliesArchiveDirectoryWithoutMovingDataKey(t *testing.T) {
	t.Run("environment", func(t *testing.T) {
		clearConfigEnv(t)
		dir := t.TempDir()
		t.Setenv(configEnvKey, filepath.Join(dir, "missing-config.json"))
		customDB := filepath.Join(dir, "custom", "history.sqlite")
		t.Setenv("USAGE_DB_PATH", customDB)

		cfg, err := LoadWithoutCreatingDefault()
		if err != nil {
			t.Fatalf("load environment DB path: %v", err)
		}
		wantDataDir := filepath.Join(dir, "data")
		if cfg.DBPath != customDB || cfg.DataDir != wantDataDir {
			t.Fatalf("custom environment DB config = %#v", cfg)
		}
		if want := filepath.Join(wantDataDir, "data.key"); cfg.DataKeyPath != want {
			t.Fatalf("DataKeyPath = %q, want %q", cfg.DataKeyPath, want)
		}
		if want := filepath.Join(filepath.Dir(customDB), "usage-archives"); cfg.UsageArchiveDir != want {
			t.Fatalf("UsageArchiveDir = %q, want %q", cfg.UsageArchiveDir, want)
		}
	})

	t.Run("config file", func(t *testing.T) {
		clearConfigEnv(t)
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.json")
		if err := os.WriteFile(configPath, []byte(`{"dbPath":"state/history.sqlite"}`), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		t.Setenv(configEnvKey, configPath)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("load config DB path: %v", err)
		}
		wantDB := filepath.Join(dir, "state", "history.sqlite")
		wantDataDir := filepath.Join(dir, "data")
		if cfg.DBPath != wantDB || cfg.DataDir != wantDataDir {
			t.Fatalf("custom config DB config = %#v", cfg)
		}
		if want := filepath.Join(wantDataDir, "data.key"); cfg.DataKeyPath != want {
			t.Fatalf("DataKeyPath = %q, want %q", cfg.DataKeyPath, want)
		}
		if want := filepath.Join(filepath.Dir(wantDB), "usage-archives"); cfg.UsageArchiveDir != want {
			t.Fatalf("UsageArchiveDir = %q, want %q", cfg.UsageArchiveDir, want)
		}
	})
}

func TestNormalizeCollectorMode(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", "auto"},
		{"AUTO", "auto"},
		{"http", "http"},
		{"HTTP", "http"},
		{"resp", "resp"},
		{"subscribe", "subscribe"},
		{" Subscribe ", "subscribe"},
		{"unknown", "auto"},
	}
	for _, tc := range cases {
		if got := normalizeCollectorMode(tc.input); got != tc.want {
			t.Errorf("normalizeCollectorMode(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		configEnvKey,
		"HTTP_ADDR",
		"USAGE_DATA_DIR",
		"USAGE_DB_PATH",
		"CPA_UPSTREAM_URL",
		"CPA_MANAGEMENT_KEY",
		"CPA_MANAGEMENT_KEY_FILE",
		"USAGE_COLLECTOR_MODE",
		"USAGE_RESP_QUEUE",
		"USAGE_RESP_POP_SIDE",
		"USAGE_BATCH_SIZE",
		"USAGE_POLL_INTERVAL_MS",
		"USAGE_QUERY_LIMIT",
		"CPA_MANAGER_PPROF_ADDR",
		"USAGE_CORS_ORIGINS",
		"USAGE_RESP_TLS_SKIP_VERIFY",
		"USAGE_QUOTA_COOLDOWN_ENABLED",
		"USAGE_ACCOUNT_ACTIONS_ENABLED",
		"USAGE_ACCOUNT_ACTIONS_AUTO_DISABLE",
		"USAGE_DASHBOARD_HOURLY_ROLLUP_ENABLED",
		"USAGE_IMPORT_CHUNK_BYTES",
		"USAGE_IMPORT_DISK_QUOTA_BYTES",
		"USAGE_IMPORT_MAX_SESSIONS",
		"USAGE_IMPORT_SESSION_TTL_MINUTES",
		"USAGE_ARCHIVE_RETENTION_ENABLED",
		"USAGE_ARCHIVE_RETENTION_DAYS",
		"PANEL_PATH",
	} {
		t.Setenv(key, "")
	}
}
