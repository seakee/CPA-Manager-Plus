package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const configEnvKey = "CPA_MANAGER_CONFIG"

const defaultConfigName = "config.json"

const defaultSecretFile = "/run/secrets/cpa_management_key"
const defaultAdminSecretFile = "/run/secrets/cpa_admin_key"
const defaultDataKeySecretFile = "/run/secrets/cpa_data_key"

const (
	DefaultUsageImportChunkBytes     int64 = 4 * 1024 * 1024
	DefaultUsageImportDiskQuotaBytes int64 = 16 * 1024 * 1024 * 1024
	DefaultUsageImportMaxSessions          = 2
	DefaultUsageImportSessionTTL           = 24 * time.Hour
)

type Config struct {
	HTTPAddr                     string
	DataDir                      string
	DBPath                       string
	CPAUpstreamURL               string
	ManagementKey                string
	AdminKey                     string
	DataKey                      string
	DataKeyPath                  string
	CollectorMode                string
	Queue                        string
	PopSide                      string
	BatchSize                    int
	PollInterval                 time.Duration
	QueryLimit                   int
	PprofAddr                    string
	PanelPath                    string
	CORSOrigins                  []string
	TLSSkipVerify                bool
	QuotaCooldownEnabled         bool
	AccountActionsEnabled        bool
	AccountActionsAutoDisable    bool
	DashboardHourlyRollupEnabled bool
	UsageImportChunkBytes        int64
	UsageImportDiskQuotaBytes    int64
	UsageImportMaxSessions       int
	UsageImportSessionTTL        time.Duration
	QuotaCooldownEnvSet          bool
	AccountActionsEnvSet         bool
	AccountActionsAutoEnvSet     bool
}

type LoadOptions struct {
	CreateDefaultConfig bool
}

type fileConfig struct {
	HTTPAddr                  string   `json:"httpAddr,omitempty"`
	DataDir                   string   `json:"dataDir,omitempty"`
	DBPath                    string   `json:"dbPath,omitempty"`
	CPAUpstreamURL            string   `json:"cpaUpstreamUrl,omitempty"`
	ManagementKeyFile         string   `json:"managementKeyFile,omitempty"`
	AdminKeyFile              string   `json:"adminKeyFile,omitempty"`
	DataKeyFile               string   `json:"dataKeyFile,omitempty"`
	DataKeyPath               string   `json:"dataKeyPath,omitempty"`
	CollectorMode             string   `json:"collectorMode,omitempty"`
	Queue                     string   `json:"queue,omitempty"`
	PopSide                   string   `json:"popSide,omitempty"`
	BatchSize                 int      `json:"batchSize,omitempty"`
	PollIntervalMS            int      `json:"pollIntervalMs,omitempty"`
	QueryLimit                int      `json:"queryLimit,omitempty"`
	PprofAddr                 string   `json:"pprofAddr,omitempty"`
	PanelPath                 string   `json:"panelPath,omitempty"`
	CORSOrigins               []string `json:"corsOrigins,omitempty"`
	TLSSkipVerify             bool     `json:"tlsSkipVerify,omitempty"`
	QuotaCooldownEnabled      bool     `json:"quotaCooldownEnabled,omitempty"`
	AccountActionsEnabled     bool     `json:"accountActionsEnabled,omitempty"`
	AccountActionsAutoDisable bool     `json:"accountActionsAutoDisable,omitempty"`
	UsageImportChunkBytes     int64    `json:"usageImportChunkBytes,omitempty"`
	UsageImportDiskQuotaBytes int64    `json:"usageImportDiskQuotaBytes,omitempty"`
	UsageImportMaxSessions    int      `json:"usageImportMaxSessions,omitempty"`
	UsageImportTTLMinutes     int      `json:"usageImportSessionTTLMinutes,omitempty"`
}

func Load() (Config, error) {
	return LoadWithOptions(LoadOptions{CreateDefaultConfig: true})
}

func LoadWithoutCreatingDefault() (Config, error) {
	return LoadWithOptions(LoadOptions{})
}

func LoadWithOptions(options LoadOptions) (Config, error) {
	cfgFile, cfgDir, err := loadFileConfig(options)
	if err != nil {
		return Config{}, err
	}

	dataDirFallback := "/data"
	if cfgFile.DataDir != "" {
		dataDirFallback = resolveConfigPath(cfgFile.DataDir, cfgDir)
	} else if cfgDir != "" {
		dataDirFallback = resolveConfigPath("./data", cfgDir)
	}
	dataDir := env("USAGE_DATA_DIR", dataDirFallback)

	dbPathFallback := filepath.Join(dataDir, "usage.sqlite")
	if !hasEnv("USAGE_DATA_DIR") && cfgFile.DBPath != "" {
		dbPathFallback = resolveConfigPath(cfgFile.DBPath, cfgDir)
	}

	managementKeyFile := defaultSecretFile
	if cfgFile.ManagementKeyFile != "" {
		managementKeyFile = resolveConfigPath(cfgFile.ManagementKeyFile, cfgDir)
	}

	adminKeyFile := defaultAdminSecretFile
	if cfgFile.AdminKeyFile != "" {
		adminKeyFile = resolveConfigPath(cfgFile.AdminKeyFile, cfgDir)
	}

	dataKeyFile := defaultDataKeySecretFile
	if cfgFile.DataKeyFile != "" {
		dataKeyFile = resolveConfigPath(cfgFile.DataKeyFile, cfgDir)
	}
	dataKeyPath := resolveConfigPath(cfgFile.DataKeyPath, cfgDir)
	if dataKeyPath == "" {
		dataKeyPath = filepath.Join(dataDir, "data.key")
	}

	return Config{
		HTTPAddr:                     env("HTTP_ADDR", stringFallback(cfgFile.HTTPAddr, "0.0.0.0:18317")),
		DataDir:                      dataDir,
		DBPath:                       env("USAGE_DB_PATH", dbPathFallback),
		CPAUpstreamURL:               env("CPA_UPSTREAM_URL", cfgFile.CPAUpstreamURL),
		ManagementKey:                readSecret("CPA_MANAGEMENT_KEY", "CPA_MANAGEMENT_KEY_FILE", managementKeyFile),
		AdminKey:                     readSecret("CPA_MANAGER_ADMIN_KEY", "CPA_MANAGER_ADMIN_KEY_FILE", adminKeyFile),
		DataKey:                      readSecret("CPA_MANAGER_DATA_KEY", "CPA_MANAGER_DATA_KEY_FILE", dataKeyFile),
		DataKeyPath:                  env("CPA_MANAGER_DATA_KEY_PATH", dataKeyPath),
		CollectorMode:                normalizeCollectorMode(env("USAGE_COLLECTOR_MODE", stringFallback(cfgFile.CollectorMode, "auto"))),
		Queue:                        env("USAGE_RESP_QUEUE", stringFallback(cfgFile.Queue, "usage")),
		PopSide:                      env("USAGE_RESP_POP_SIDE", stringFallback(cfgFile.PopSide, "right")),
		BatchSize:                    envInt("USAGE_BATCH_SIZE", intFallback(cfgFile.BatchSize, 100)),
		PollInterval:                 time.Duration(envInt("USAGE_POLL_INTERVAL_MS", intFallback(cfgFile.PollIntervalMS, 500))) * time.Millisecond,
		QueryLimit:                   envInt("USAGE_QUERY_LIMIT", intFallback(cfgFile.QueryLimit, 50000)),
		PprofAddr:                    env("CPA_MANAGER_PPROF_ADDR", cfgFile.PprofAddr),
		PanelPath:                    env("PANEL_PATH", resolveConfigPath(cfgFile.PanelPath, cfgDir)),
		CORSOrigins:                  splitCSV(env("USAGE_CORS_ORIGINS", strings.Join(sliceFallback(cfgFile.CORSOrigins, []string{"*"}), ","))),
		TLSSkipVerify:                envBool("USAGE_RESP_TLS_SKIP_VERIFY", cfgFile.TLSSkipVerify),
		QuotaCooldownEnabled:         envBool("USAGE_QUOTA_COOLDOWN_ENABLED", cfgFile.QuotaCooldownEnabled),
		AccountActionsEnabled:        envBool("USAGE_ACCOUNT_ACTIONS_ENABLED", cfgFile.AccountActionsEnabled),
		AccountActionsAutoDisable:    envBool("USAGE_ACCOUNT_ACTIONS_AUTO_DISABLE", cfgFile.AccountActionsAutoDisable),
		DashboardHourlyRollupEnabled: envBool("USAGE_DASHBOARD_HOURLY_ROLLUP_ENABLED", true),
		UsageImportChunkBytes: envInt64(
			"USAGE_IMPORT_CHUNK_BYTES",
			int64Fallback(cfgFile.UsageImportChunkBytes, DefaultUsageImportChunkBytes),
		),
		UsageImportDiskQuotaBytes: envInt64(
			"USAGE_IMPORT_DISK_QUOTA_BYTES",
			int64Fallback(cfgFile.UsageImportDiskQuotaBytes, DefaultUsageImportDiskQuotaBytes),
		),
		UsageImportMaxSessions: envInt(
			"USAGE_IMPORT_MAX_SESSIONS",
			intFallback(cfgFile.UsageImportMaxSessions, DefaultUsageImportMaxSessions),
		),
		UsageImportSessionTTL: time.Duration(envInt(
			"USAGE_IMPORT_SESSION_TTL_MINUTES",
			intFallback(cfgFile.UsageImportTTLMinutes, int(DefaultUsageImportSessionTTL/time.Minute)),
		)) * time.Minute,
		QuotaCooldownEnvSet:      hasEnv("USAGE_QUOTA_COOLDOWN_ENABLED"),
		AccountActionsEnvSet:     hasEnv("USAGE_ACCOUNT_ACTIONS_ENABLED"),
		AccountActionsAutoEnvSet: hasEnv("USAGE_ACCOUNT_ACTIONS_AUTO_DISABLE"),
	}, nil
}

func loadFileConfig(options LoadOptions) (fileConfig, string, error) {
	if configPath := strings.TrimSpace(os.Getenv(configEnvKey)); configPath != "" {
		if !options.CreateDefaultConfig {
			cfg, cfgDir, ok, err := readFileConfig(configPath)
			if err != nil || ok {
				return cfg, cfgDir, err
			}
			return fileConfig{}, filepath.Dir(configPath), nil
		}
		return readOrCreateFileConfig(configPath)
	}

	configPath, err := executableConfigPath()
	if err != nil {
		return fileConfig{}, "", err
	}
	cfg, cfgDir, ok, err := readFileConfig(configPath)
	if err != nil || ok {
		return cfg, cfgDir, err
	}
	if hasEnv("USAGE_DATA_DIR") || hasEnv("USAGE_DB_PATH") {
		return fileConfig{}, "", nil
	}
	if !options.CreateDefaultConfig {
		return fileConfig{}, filepath.Dir(configPath), nil
	}
	return createDefaultFileConfig(configPath)
}

func readOrCreateFileConfig(configPath string) (fileConfig, string, error) {
	cfg, cfgDir, ok, err := readFileConfig(configPath)
	if err != nil || ok {
		return cfg, cfgDir, err
	}
	return createDefaultFileConfig(configPath)
}

func readFileConfig(configPath string) (fileConfig, string, bool, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fileConfig{}, filepath.Dir(configPath), false, nil
		}
		return fileConfig{}, filepath.Dir(configPath), false, fmt.Errorf("read config %s: %w", configPath, err)
	}
	var cfg fileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fileConfig{}, filepath.Dir(configPath), false, fmt.Errorf("parse config %s: %w", configPath, err)
	}
	return cfg, filepath.Dir(configPath), true, nil
}

func createDefaultFileConfig(configPath string) (fileConfig, string, error) {
	cfg := fileConfig{
		HTTPAddr: "0.0.0.0:18317",
		DataDir:  "./data",
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fileConfig{}, "", err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fileConfig{}, "", fmt.Errorf("create config directory %s: %w", filepath.Dir(configPath), err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		return fileConfig{}, "", fmt.Errorf("create default config %s: %w", configPath, err)
	}
	return cfg, filepath.Dir(configPath), nil
}

func executableConfigPath() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	return filepath.Join(filepath.Dir(executable), defaultConfigName), nil
}

func normalizeCollectorMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "http", "resp", "subscribe":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "auto"
	}
}

func hasEnv(key string) bool {
	return strings.TrimSpace(os.Getenv(key)) != ""
}

func env(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envInt64(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func stringFallback(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func intFallback(value int, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func int64Fallback(value int64, fallback int64) int64 {
	if value <= 0 {
		return fallback
	}
	return value
}

func sliceFallback(value []string, fallback []string) []string {
	if len(value) == 0 {
		return fallback
	}
	return value
}

func resolveConfigPath(path string, baseDir string) string {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) || baseDir == "" {
		return path
	}
	return filepath.Join(baseDir, path)
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func readSecret(envKey string, fileEnvKey string, defaultFile string) string {
	if value := strings.TrimSpace(os.Getenv(envKey)); value != "" {
		return value
	}

	path := strings.TrimSpace(os.Getenv(fileEnvKey))
	if path == "" {
		path = defaultFile
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
