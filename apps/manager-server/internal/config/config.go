package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
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
	DefaultUsageImportChunkBytes        int64 = 4 * 1024 * 1024
	DefaultUsageImportDiskQuotaBytes    int64 = 16 * 1024 * 1024 * 1024
	DefaultUsageImportMaxSessions             = 2
	DefaultUsageImportSessionTTL              = 24 * time.Hour
	DefaultUsageArchiveRetentionEnabled       = false
	DefaultUsageArchiveRetentionDays          = 30
	maxUsageArchiveRetentionDays        int64 = math.MaxInt64 / int64(24*time.Hour)
)

type Config struct {
	// A parsed connection-bearing old config is source evidence when DB is absent.
	ProvisioningConfigOnly       bool
	HTTPAddr                     string
	DataDir                      string
	DBPath                       string
	CPAUpstreamURL               string
	ManagementKey                string
	AdminKey                     string
	DataKey                      string
	DataKeyPath                  string
	RuntimeURL                   string
	RuntimeTokenFile             string
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
	UsageArchiveDir              string
	UsageArchiveRetentionEnabled bool
	UsageArchiveRetentionDays    int
	QuotaCooldownEnvSet          bool
	AccountActionsEnvSet         bool
	AccountActionsAutoEnvSet     bool
}

type LoadOptions struct {
	CreateDefaultConfig   bool
	RequireExplicitConfig bool
}

type fileConfig struct {
	provisioningOnly             bool
	HTTPAddr                     string   `json:"httpAddr,omitempty"`
	DataDir                      string   `json:"dataDir,omitempty"`
	DBPath                       string   `json:"dbPath,omitempty"`
	CPAUpstreamURL               string   `json:"cpaUpstreamUrl,omitempty"`
	ManagementKeyFile            string   `json:"managementKeyFile,omitempty"`
	AdminKeyFile                 string   `json:"adminKeyFile,omitempty"`
	DataKeyFile                  string   `json:"dataKeyFile,omitempty"`
	DataKeyPath                  string   `json:"dataKeyPath,omitempty"`
	CollectorMode                string   `json:"collectorMode,omitempty"`
	Queue                        string   `json:"queue,omitempty"`
	PopSide                      string   `json:"popSide,omitempty"`
	BatchSize                    int      `json:"batchSize,omitempty"`
	PollIntervalMS               int      `json:"pollIntervalMs,omitempty"`
	QueryLimit                   int      `json:"queryLimit,omitempty"`
	PprofAddr                    string   `json:"pprofAddr,omitempty"`
	PanelPath                    string   `json:"panelPath,omitempty"`
	CORSOrigins                  []string `json:"corsOrigins,omitempty"`
	TLSSkipVerify                bool     `json:"tlsSkipVerify,omitempty"`
	QuotaCooldownEnabled         bool     `json:"quotaCooldownEnabled,omitempty"`
	AccountActionsEnabled        bool     `json:"accountActionsEnabled,omitempty"`
	AccountActionsAutoDisable    bool     `json:"accountActionsAutoDisable,omitempty"`
	UsageImportChunkBytes        int64    `json:"usageImportChunkBytes,omitempty"`
	UsageImportDiskQuotaBytes    int64    `json:"usageImportDiskQuotaBytes,omitempty"`
	UsageImportMaxSessions       int      `json:"usageImportMaxSessions,omitempty"`
	UsageImportTTLMinutes        int      `json:"usageImportSessionTTLMinutes,omitempty"`
	UsageArchiveRetentionEnabled bool     `json:"usageArchiveRetentionEnabled,omitempty"`
	UsageArchiveRetentionDays    *int64   `json:"usageArchiveRetentionDays,omitempty"`
}

func Load() (Config, error) {
	return LoadWithOptions(LoadOptions{CreateDefaultConfig: true})
}

func LoadWithoutCreatingDefault() (Config, error) {
	return LoadWithOptions(LoadOptions{})
}

// LoadForInspection preserves normal path/secret precedence without creating
// defaults, and does not silently ignore a missing explicitly selected config.
func LoadForInspection() (Config, error) {
	return LoadWithOptions(LoadOptions{RequireExplicitConfig: true})
}

func InspectionConfigPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv(configEnvKey)); path != "" {
		return path, nil
	}
	return executableConfigPath()
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
	dataDirExplicit := hasEnv("USAGE_DATA_DIR") || strings.TrimSpace(cfgFile.DataDir) != ""

	dbPathFallback := filepath.Join(dataDir, "usage.sqlite")
	if !hasEnv("USAGE_DATA_DIR") && cfgFile.DBPath != "" {
		dbPathFallback = resolveConfigPath(cfgFile.DBPath, cfgDir)
	}
	dbPath := env("USAGE_DB_PATH", dbPathFallback)
	usageArchiveBaseDir := dataDir
	if !dataDirExplicit && (hasEnv("USAGE_DB_PATH") || strings.TrimSpace(cfgFile.DBPath) != "") {
		usageArchiveBaseDir = filepath.Dir(dbPath)
	}
	usageArchiveRetentionDays, err := resolveUsageArchiveRetentionDays(cfgFile.UsageArchiveRetentionDays)
	if err != nil {
		return Config{}, err
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
		ProvisioningConfigOnly:       cfgFile.provisioningOnly,
		HTTPAddr:                     env("HTTP_ADDR", stringFallback(cfgFile.HTTPAddr, "0.0.0.0:18317")),
		DataDir:                      dataDir,
		DBPath:                       dbPath,
		CPAUpstreamURL:               env("CPA_UPSTREAM_URL", cfgFile.CPAUpstreamURL),
		ManagementKey:                readSecret("CPA_MANAGEMENT_KEY", "CPA_MANAGEMENT_KEY_FILE", managementKeyFile),
		AdminKey:                     readSecret("CPA_MANAGER_ADMIN_KEY", "CPA_MANAGER_ADMIN_KEY_FILE", adminKeyFile),
		DataKey:                      readSecret("CPA_MANAGER_DATA_KEY", "CPA_MANAGER_DATA_KEY_FILE", dataKeyFile),
		DataKeyPath:                  env("CPA_MANAGER_DATA_KEY_PATH", dataKeyPath),
		RuntimeURL:                   strings.TrimSpace(os.Getenv("CPAMP_RUNTIME_URL")),
		RuntimeTokenFile:             strings.TrimSpace(os.Getenv("CPAMP_RUNTIME_TOKEN_FILE")),
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
		UsageArchiveDir: filepath.Join(usageArchiveBaseDir, "usage-archives"),
		UsageArchiveRetentionEnabled: envBool(
			"USAGE_ARCHIVE_RETENTION_ENABLED",
			cfgFile.UsageArchiveRetentionEnabled,
		),
		UsageArchiveRetentionDays: usageArchiveRetentionDays,
		QuotaCooldownEnvSet:       hasEnv("USAGE_QUOTA_COOLDOWN_ENABLED"),
		AccountActionsEnvSet:      hasEnv("USAGE_ACCOUNT_ACTIONS_ENABLED"),
		AccountActionsAutoEnvSet:  hasEnv("USAGE_ACCOUNT_ACTIONS_AUTO_DISABLE"),
	}, nil
}

// EmbeddedRuntimeConfigured reports only whether the private deployment
// transport is wired. It does not read the token file or infer product desired
// state from Runtime availability.
func (c Config) EmbeddedRuntimeConfigured() bool {
	return strings.TrimSpace(c.RuntimeURL) != "" && strings.TrimSpace(c.RuntimeTokenFile) != ""
}

func loadFileConfig(options LoadOptions) (fileConfig, string, error) {
	if configPath := strings.TrimSpace(os.Getenv(configEnvKey)); configPath != "" {
		if !options.CreateDefaultConfig {
			cfg, cfgDir, ok, err := readFileConfig(configPath)
			if err != nil || ok {
				return cfg, cfgDir, err
			}
			if options.RequireExplicitConfig {
				return fileConfig{}, cfgDir, fmt.Errorf("explicit config file is missing")
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
	info, err := os.Stat(configPath)
	if os.IsNotExist(err) {
		return fileConfig{}, filepath.Dir(configPath), false, nil
	}
	if err != nil {
		return fileConfig{}, filepath.Dir(configPath), false, fmt.Errorf("stat config %s: %w", configPath, err)
	}
	if !info.Mode().IsRegular() {
		return fileConfig{}, filepath.Dir(configPath), false, fmt.Errorf("config is not a regular file: %s", configPath)
	}
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
	// Unknown old fields cannot turn a missing database into Fresh. Normal
	// decoding and runtime environment precedence are unchanged.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var known fileConfig
	cfg.provisioningOnly = decoder.Decode(&known) == nil && strings.TrimSpace(cfg.CPAUpstreamURL) == "" && strings.TrimSpace(cfg.ManagementKeyFile) == ""
	return cfg, filepath.Dir(configPath), true, nil
}

// ResolveInspectionLocations reads only the explicit source config, without
// runtime environment overrides or reading any credential/secret files.
func ResolveInspectionLocations(configPath, databasePath string) (Config, error) {
	cfg, dir, exists, err := readFileConfig(configPath)
	if err != nil {
		return Config{}, err
	}
	if !exists {
		return Config{}, fmt.Errorf("explicit config file is missing")
	}
	// The default key follows the config's data directory even when dbPath
	// selects a database elsewhere. Archives follow the explicit database unless
	// the config specifies dataDir, matching the normal source path semantics.
	dataDir := resolveConfigPath("./data", dir)
	archiveBaseDir := filepath.Dir(databasePath)
	if strings.TrimSpace(cfg.DataDir) != "" {
		dataDir = resolveConfigPath(cfg.DataDir, dir)
		archiveBaseDir = dataDir
	}
	keyPath := resolveConfigPath(cfg.DataKeyPath, dir)
	if keyPath == "" {
		keyPath = filepath.Join(dataDir, "data.key")
	}
	return Config{DBPath: databasePath, DataKeyPath: keyPath, UsageArchiveDir: filepath.Join(archiveBaseDir, "usage-archives"), ProvisioningConfigOnly: cfg.provisioningOnly}, nil
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

func resolveUsageArchiveRetentionDays(fileValue *int64) (int, error) {
	const envKey = "USAGE_ARCHIVE_RETENTION_DAYS"
	if raw := strings.TrimSpace(os.Getenv(envKey)); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer between 1 and %d: %w", envKey, maxUsageArchiveRetentionDays, err)
		}
		return validateUsageArchiveRetentionDays(envKey, value)
	}
	if fileValue != nil {
		return validateUsageArchiveRetentionDays("usageArchiveRetentionDays", *fileValue)
	}
	return DefaultUsageArchiveRetentionDays, nil
}

func validateUsageArchiveRetentionDays(source string, value int64) (int, error) {
	if value <= 0 || value > maxUsageArchiveRetentionDays {
		return 0, fmt.Errorf("%s must be between 1 and %d days", source, maxUsageArchiveRetentionDays)
	}
	return int(value), nil
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
