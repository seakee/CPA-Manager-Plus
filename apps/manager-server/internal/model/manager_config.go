package model

import (
	"bytes"
	"encoding/json"
)

type ManagerConfig struct {
	CPAConnection        ManagerCPAConnectionConfig        `json:"cpaConnection"`
	Collector            ManagerCollectorConfig            `json:"collector"`
	CodexInspection      ManagerCodexInspectionConfig      `json:"codexInspection"`
	ExternalUsageService ManagerExternalUsageServiceConfig `json:"externalUsageService"`
	Retention            ManagerRetentionConfig            `json:"retention,omitempty"`
	UpdatedAtMS          int64                             `json:"updatedAtMs,omitempty"`
}

type ManagerCPAConnectionConfig struct {
	CPABaseURL    string `json:"cpaBaseUrl"`
	ManagementKey string `json:"managementKey,omitempty"`
}

type ManagerCollectorConfig struct {
	Enabled               *bool  `json:"enabled,omitempty"`
	CollectorMode         string `json:"collectorMode,omitempty"`
	Queue                 string `json:"queue,omitempty"`
	PopSide               string `json:"popSide,omitempty"`
	BatchSize             int    `json:"batchSize,omitempty"`
	PollIntervalMS        int    `json:"pollIntervalMs,omitempty"`
	QueryLimit            int    `json:"queryLimit,omitempty"`
	TLSSkipVerify         bool   `json:"tlsSkipVerify"`
	TLSSkipVerifyExplicit bool   `json:"-"`
}

func (c ManagerCollectorConfig) MarshalJSON() ([]byte, error) {
	if c.TLSSkipVerifyExplicit || c.TLSSkipVerify {
		type explicitCollectorConfig struct {
			Enabled        *bool  `json:"enabled,omitempty"`
			CollectorMode  string `json:"collectorMode,omitempty"`
			Queue          string `json:"queue,omitempty"`
			PopSide        string `json:"popSide,omitempty"`
			BatchSize      int    `json:"batchSize,omitempty"`
			PollIntervalMS int    `json:"pollIntervalMs,omitempty"`
			QueryLimit     int    `json:"queryLimit,omitempty"`
			TLSSkipVerify  bool   `json:"tlsSkipVerify"`
		}
		return json.Marshal(explicitCollectorConfig{
			Enabled:        c.Enabled,
			CollectorMode:  c.CollectorMode,
			Queue:          c.Queue,
			PopSide:        c.PopSide,
			BatchSize:      c.BatchSize,
			PollIntervalMS: c.PollIntervalMS,
			QueryLimit:     c.QueryLimit,
			TLSSkipVerify:  c.TLSSkipVerify,
		})
	}
	type sparseCollectorConfig struct {
		Enabled        *bool  `json:"enabled,omitempty"`
		CollectorMode  string `json:"collectorMode,omitempty"`
		Queue          string `json:"queue,omitempty"`
		PopSide        string `json:"popSide,omitempty"`
		BatchSize      int    `json:"batchSize,omitempty"`
		PollIntervalMS int    `json:"pollIntervalMs,omitempty"`
		QueryLimit     int    `json:"queryLimit,omitempty"`
	}
	return json.Marshal(sparseCollectorConfig{
		Enabled:        c.Enabled,
		CollectorMode:  c.CollectorMode,
		Queue:          c.Queue,
		PopSide:        c.PopSide,
		BatchSize:      c.BatchSize,
		PollIntervalMS: c.PollIntervalMS,
		QueryLimit:     c.QueryLimit,
	})
}

func (c *ManagerCollectorConfig) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		*c = ManagerCollectorConfig{}
		return nil
	}
	type plainCollectorConfig struct {
		Enabled        *bool  `json:"enabled,omitempty"`
		CollectorMode  string `json:"collectorMode,omitempty"`
		Queue          string `json:"queue,omitempty"`
		PopSide        string `json:"popSide,omitempty"`
		BatchSize      int    `json:"batchSize,omitempty"`
		PollIntervalMS int    `json:"pollIntervalMs,omitempty"`
		QueryLimit     int    `json:"queryLimit,omitempty"`
		TLSSkipVerify  bool   `json:"tlsSkipVerify"`
	}
	var decoded plainCollectorConfig
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return err
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &rawFields); err != nil {
		return err
	}
	explicit := false
	if rawTLS, ok := rawFields["tlsSkipVerify"]; ok {
		rawTLS = bytes.TrimSpace(rawTLS)
		if len(rawTLS) > 0 && !bytes.Equal(rawTLS, []byte("null")) {
			explicit = true
		}
	}
	*c = ManagerCollectorConfig{
		Enabled:               decoded.Enabled,
		CollectorMode:         decoded.CollectorMode,
		Queue:                 decoded.Queue,
		PopSide:               decoded.PopSide,
		BatchSize:             decoded.BatchSize,
		PollIntervalMS:        decoded.PollIntervalMS,
		QueryLimit:            decoded.QueryLimit,
		TLSSkipVerify:         decoded.TLSSkipVerify,
		TLSSkipVerifyExplicit: explicit,
	}
	return nil
}

// ManagerCollectorPatch carries partial updates to collector parameters.
// Request monitoring (`collector.enabled`) is controlled separately through
// SetCollectorEnabled so collector tuning cannot bypass switch validation.
type ManagerCollectorPatch struct {
	CollectorMode  *string `json:"collectorMode,omitempty"`
	Queue          *string `json:"queue,omitempty"`
	PopSide        *string `json:"popSide,omitempty"`
	BatchSize      *int    `json:"batchSize,omitempty"`
	PollIntervalMS *int    `json:"pollIntervalMs,omitempty"`
	QueryLimit     *int    `json:"queryLimit,omitempty"`
	TLSSkipVerify  *bool   `json:"tlsSkipVerify,omitempty"`
}

func (p ManagerCollectorPatch) IsEmpty() bool {
	return p.CollectorMode == nil && p.Queue == nil && p.PopSide == nil &&
		p.BatchSize == nil && p.PollIntervalMS == nil && p.QueryLimit == nil &&
		p.TLSSkipVerify == nil
}

type ManagerExternalUsageServiceConfig struct {
	Enabled     bool   `json:"enabled"`
	ServiceBase string `json:"serviceBase,omitempty"`
}

type ManagerRetentionConfig struct {
	Enabled                *bool `json:"enabled,omitempty"`
	RetentionDays          int   `json:"retentionDays,omitempty"`
	EnvLocked              bool  `json:"envLocked,omitempty"`
	EnabledEnvLocked       bool  `json:"enabledEnvLocked,omitempty"`
	RetentionDaysEnvLocked bool  `json:"retentionDaysEnvLocked,omitempty"`
	RetentionDaysExplicit  bool  `json:"-"`
}

func (r *ManagerRetentionConfig) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		*r = ManagerRetentionConfig{}
		return nil
	}
	type plainRetentionConfig struct {
		Enabled                *bool `json:"enabled,omitempty"`
		RetentionDays          int   `json:"retentionDays,omitempty"`
		EnvLocked              bool  `json:"envLocked,omitempty"`
		EnabledEnvLocked       bool  `json:"enabledEnvLocked,omitempty"`
		RetentionDaysEnvLocked bool  `json:"retentionDaysEnvLocked,omitempty"`
	}
	var decoded plainRetentionConfig
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return err
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &rawFields); err != nil {
		return err
	}
	daysExplicit := false
	if rawDays, ok := rawFields["retentionDays"]; ok {
		rawDays = bytes.TrimSpace(rawDays)
		if len(rawDays) > 0 && !bytes.Equal(rawDays, []byte("null")) {
			daysExplicit = true
		}
	}
	*r = ManagerRetentionConfig{
		Enabled:                decoded.Enabled,
		RetentionDays:          decoded.RetentionDays,
		EnvLocked:              decoded.EnvLocked,
		EnabledEnvLocked:       decoded.EnabledEnvLocked,
		RetentionDaysEnvLocked: decoded.RetentionDaysEnvLocked,
		RetentionDaysExplicit:  daysExplicit,
	}
	return nil
}

type ManagerRetentionPatch struct {
	Enabled       *bool `json:"enabled,omitempty"`
	RetentionDays *int  `json:"retentionDays,omitempty"`
}

func (p ManagerRetentionPatch) IsEmpty() bool {
	return p.Enabled == nil && p.RetentionDays == nil
}
