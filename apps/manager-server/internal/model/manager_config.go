package model

// DefaultCodexQuotaUserAgent matches CLIProxyAPI's reverse-proxy inference
// fallback User-Agent, so quota-lookup requests share a self-consistent client
// fingerprint with real inference requests for the same account, reducing
// anti-fraud risk.
const DefaultCodexQuotaUserAgent = "codex-tui/0.135.0 (Mac OS 26.5.0; arm64) iTerm.app/3.6.10"

type ManagerConfig struct {
	CPAConnection        ManagerCPAConnectionConfig        `json:"cpaConnection"`
	Collector            ManagerCollectorConfig            `json:"collector"`
	Codex                ManagerCodexConfig                `json:"codex"`
	CodexInspection      ManagerCodexInspectionConfig      `json:"codexInspection"`
	ExternalUsageService ManagerExternalUsageServiceConfig `json:"externalUsageService"`
	UpdatedAtMS          int64                             `json:"updatedAtMs,omitempty"`
}

// ManagerCodexConfig holds general Codex settings (independent from the
// inspection UA settings). QuotaUserAgent is the User-Agent sent by all
// frontend quota-lookup paths.
type ManagerCodexConfig struct {
	QuotaUserAgent string `json:"quotaUserAgent,omitempty"`
}

type ManagerCPAConnectionConfig struct {
	CPABaseURL    string `json:"cpaBaseUrl"`
	ManagementKey string `json:"managementKey,omitempty"`
}

type ManagerCollectorConfig struct {
	Enabled        *bool  `json:"enabled,omitempty"`
	CollectorMode  string `json:"collectorMode,omitempty"`
	Queue          string `json:"queue,omitempty"`
	PopSide        string `json:"popSide,omitempty"`
	BatchSize      int    `json:"batchSize,omitempty"`
	PollIntervalMS int    `json:"pollIntervalMs,omitempty"`
	QueryLimit     int    `json:"queryLimit,omitempty"`
	TLSSkipVerify  bool   `json:"tlsSkipVerify,omitempty"`
}

type ManagerExternalUsageServiceConfig struct {
	Enabled     bool   `json:"enabled"`
	ServiceBase string `json:"serviceBase,omitempty"`
}
