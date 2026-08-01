package model

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	CodexInspectionScheduleModeInterval   = "interval"
	CodexInspectionScheduleModeTimePoints = "time_points"

	CodexInspectionAutoActionNone    = "none"
	CodexInspectionAutoActionEnable  = "enable"
	CodexInspectionAutoActionDisable = "disable"
	CodexInspectionAutoActionDelete  = "delete"

	CodexInspectionStatusRunning     = "running"
	CodexInspectionStatusCompleted   = "completed"
	CodexInspectionStatusFailed      = "failed"
	CodexInspectionStatusCancelling  = "cancelling"
	CodexInspectionStatusCancelled   = "cancelled"
	CodexInspectionStatusInterrupted = "interrupted"

	CodexInspectionTriggerManual    = "manual"
	CodexInspectionTriggerScheduled = "scheduled"

	CodexInspectionActionStatusNone        = "none"
	CodexInspectionActionStatusPending     = "pending"
	CodexInspectionActionStatusSuccess     = "success"
	CodexInspectionActionStatusFailed      = "failed"
	CodexInspectionActionStatusSkipped     = "skipped"
	CodexInspectionActionStatusNeedsReview = "needs_review"

	CodexInspectionTargetCodex = "codex"
	CodexInspectionTargetXAI   = "xai"

	DefaultXAIInspectionModel    = "grok-4.5"
	DefaultXAIInspectionPrompt   = "Reply with exactly OK."
	DefaultXAIInferenceUserAgent = "xai-grok-workspace/0.2.101"
)

type ManagerCodexInspectionConfig struct {
	Enabled  *bool                                `json:"enabled,omitempty"`
	Schedule ManagerCodexInspectionScheduleConfig `json:"schedule"`
	// TargetTypes is the canonical multi-provider selection. TargetType remains
	// available for legacy callers and is always normalized to the first target.
	TargetTypes           []string `json:"targetTypes,omitempty"`
	TargetType            string   `json:"targetType,omitempty"`
	Workers               int      `json:"workers,omitempty"`
	DeleteWorkers         int      `json:"deleteWorkers,omitempty"`
	Timeout               int      `json:"timeout,omitempty"`
	Retries               int      `json:"retries,omitempty"`
	UserAgent             string   `json:"userAgent,omitempty"`
	XAIInferenceUserAgent string   `json:"xaiInferenceUserAgent,omitempty"`
	XAIInferenceEnabled   bool     `json:"xaiInferenceEnabled,omitempty"`
	XAIInferenceModel     string   `json:"xaiInferenceModel,omitempty"`
	XAIInferencePrompt    string   `json:"xaiInferencePrompt,omitempty"`
	UsedPercentThreshold  float64  `json:"usedPercentThreshold,omitempty"`
	SampleSize            int      `json:"sampleSize,omitempty"`
	AutoActionMode        string   `json:"autoActionMode,omitempty"`
	AutoRecoverEnabled    bool     `json:"autoRecoverEnabled,omitempty"`
}

type ManagerCodexInspectionScheduleConfig struct {
	Mode            string   `json:"mode,omitempty"`
	TimePoints      []string `json:"timePoints,omitempty"`
	IntervalMinutes int      `json:"intervalMinutes,omitempty"`
	TimeZone        string   `json:"timeZone,omitempty"`
}

type CodexInspectionRun struct {
	ID            int64                        `json:"id"`
	TriggerType   string                       `json:"triggerType"`
	TriggerKey    string                       `json:"triggerKey,omitempty"`
	Status        string                       `json:"status"`
	StartedAtMS   int64                        `json:"startedAtMs"`
	FinishedAtMS  int64                        `json:"finishedAtMs,omitempty"`
	TotalFiles    int                          `json:"totalFiles"`
	ProbeSetCount int                          `json:"probeSetCount"`
	SampledCount  int                          `json:"sampledCount"`
	DisabledCount int                          `json:"disabledCount"`
	EnabledCount  int                          `json:"enabledCount"`
	DeleteCount   int                          `json:"deleteCount"`
	DisableCount  int                          `json:"disableCount"`
	EnableCount   int                          `json:"enableCount"`
	ReauthCount   int                          `json:"reauthCount"`
	KeepCount     int                          `json:"keepCount"`
	Error         string                       `json:"error,omitempty"`
	Settings      ManagerCodexInspectionConfig `json:"settings"`
	SettingsJSON  string                       `json:"-"`
	CreatedAtMS   int64                        `json:"createdAtMs"`
	UpdatedAtMS   int64                        `json:"updatedAtMs"`
	// Active and Cancellable are derived compatibility fields. They are always
	// emitted so clients can distinguish a truly active run from a stale row
	// whose historical status still says running or cancelling.
	Active      bool `json:"active"`
	Cancellable bool `json:"cancellable"`
}

type CodexInspectionLease struct {
	RunID            int64  `json:"runId"`
	OwnerID          string `json:"ownerId"`
	HeartbeatAtMS    int64  `json:"heartbeatAtMs"`
	LeaseExpiresAtMS int64  `json:"leaseExpiresAtMs"`
}

func IsCodexInspectionRunActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case CodexInspectionStatusRunning, CodexInspectionStatusCancelling:
		return true
	default:
		return false
	}
}

func IsCodexInspectionRunCancellable(status string) bool {
	return IsCodexInspectionRunActive(status)
}

func NormalizeCodexInspectionRunStatus(status string) string {
	trimmed := strings.ToLower(strings.TrimSpace(status))
	switch trimmed {
	case CodexInspectionStatusRunning,
		CodexInspectionStatusCompleted,
		CodexInspectionStatusFailed,
		CodexInspectionStatusCancelling,
		CodexInspectionStatusCancelled,
		CodexInspectionStatusInterrupted:
		return trimmed
	default:
		// Preserve unknown values for forward compatibility instead of silently
		// presenting them as a successful or failed run.
		return strings.TrimSpace(status)
	}
}

type CodexInspectionQuotaWindow struct {
	ID                 string         `json:"id"`
	LabelKey           string         `json:"labelKey"`
	LabelParams        map[string]any `json:"labelParams,omitempty"`
	UsedPercent        *float64       `json:"usedPercent,omitempty"`
	ResetLabel         string         `json:"resetLabel"`
	LimitWindowSeconds *float64       `json:"limitWindowSeconds,omitempty"`
}

type CodexInspectionResult struct {
	ID                  int64                        `json:"id"`
	RunID               int64                        `json:"runId"`
	AccountKey          string                       `json:"accountKey"`
	FileName            string                       `json:"fileName"`
	DisplayAccount      string                       `json:"displayAccount"`
	AccountSnapshot     string                       `json:"accountSnapshot,omitempty"`
	AuthIndex           string                       `json:"authIndex,omitempty"`
	AccountID           string                       `json:"accountId,omitempty"`
	Provider            string                       `json:"provider"`
	Disabled            bool                         `json:"disabled"`
	Status              string                       `json:"status,omitempty"`
	State               string                       `json:"state,omitempty"`
	Action              string                       `json:"action"`
	ActionReason        string                       `json:"actionReason"`
	ActionStatus        string                       `json:"actionStatus,omitempty"`
	ExecutedAction      string                       `json:"executedAction,omitempty"`
	ActionError         string                       `json:"actionError,omitempty"`
	StatusCode          *int                         `json:"statusCode,omitempty"`
	UsedPercent         *float64                     `json:"usedPercent,omitempty"`
	IsQuota             bool                         `json:"isQuota"`
	AutoRecoverEligible bool                         `json:"autoRecoverEligible"`
	Error               string                       `json:"error,omitempty"`
	PlanType            string                       `json:"planType,omitempty"`
	QuotaWindows        []CodexInspectionQuotaWindow `json:"quotaWindows,omitempty"`
	QuotaWindowsJSON    string                       `json:"-"`
	ErrorKind           string                       `json:"errorKind,omitempty"`
	ErrorDetail         string                       `json:"errorDetail,omitempty"`
	CreatedAtMS         int64                        `json:"createdAtMs"`
}

type CodexInspectionDisableOwnership struct {
	FileName        string
	Provider        string
	AuthIndex       string
	AccountID       string
	AccountSnapshot string
	DisabledAtMS    int64
	UpdatedAtMS     int64
}

type CodexInspectionDisableOwnershipTarget struct {
	FileName        string
	Provider        *string
	AuthIndex       *string
	AccountID       *string
	AccountSnapshot *string
}

type CodexInspectionLog struct {
	ID          int64  `json:"id"`
	RunID       int64  `json:"runId"`
	Level       string `json:"level"`
	Message     string `json:"message"`
	DetailJSON  string `json:"-"`
	Detail      any    `json:"detail,omitempty"`
	CreatedAtMS int64  `json:"createdAtMs"`
}

func DefaultCodexInspectionConfig() ManagerCodexInspectionConfig {
	return ManagerCodexInspectionConfig{
		Enabled: boolPtr(false),
		Schedule: ManagerCodexInspectionScheduleConfig{
			Mode:            CodexInspectionScheduleModeInterval,
			IntervalMinutes: 60,
		},
		TargetType:            CodexInspectionTargetCodex,
		Workers:               4,
		DeleteWorkers:         4,
		Timeout:               15000,
		Retries:               0,
		UserAgent:             "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal",
		XAIInferenceUserAgent: DefaultXAIInferenceUserAgent,
		XAIInferenceEnabled:   false,
		XAIInferenceModel:     DefaultXAIInspectionModel,
		XAIInferencePrompt:    DefaultXAIInspectionPrompt,
		UsedPercentThreshold:  100,
		SampleSize:            0,
		AutoActionMode:        CodexInspectionAutoActionNone,
		AutoRecoverEnabled:    false,
	}
}

func NormalizeCodexInspectionConfig(input ManagerCodexInspectionConfig, fallback ManagerCodexInspectionConfig) ManagerCodexInspectionConfig {
	base := fallback
	base.TargetTypes = NormalizeCodexInspectionTargetTypes(base.TargetTypes, base.TargetType)
	if len(base.TargetTypes) == 0 {
		base = DefaultCodexInspectionConfig()
	}
	base.TargetType = base.TargetTypes[0]

	next := base
	if input.Enabled != nil {
		next.Enabled = boolPtr(*input.Enabled)
	}
	next.Schedule = NormalizeCodexInspectionSchedule(input.Schedule, base.Schedule)
	if input.TargetTypes != nil {
		if targetTypes := NormalizeCodexInspectionTargetTypes(input.TargetTypes, ""); len(targetTypes) > 0 {
			next.TargetTypes = targetTypes
		}
	} else if targetTypes := NormalizeCodexInspectionTargetTypes(nil, input.TargetType); len(targetTypes) > 0 {
		next.TargetTypes = targetTypes
	}
	next.TargetType = next.TargetTypes[0]
	next.Workers = positiveOr(input.Workers, base.Workers)
	next.DeleteWorkers = positiveOr(input.DeleteWorkers, positiveOr(input.Workers, base.DeleteWorkers))
	next.Timeout = positiveOr(input.Timeout, base.Timeout)
	// Retries and SampleSize intentionally accept zero as an explicit value.
	// Frontend config submissions write complete config objects, so omitted
	// fields are indistinguishable from zero in this non-pointer schema.
	if input.Retries >= 0 {
		next.Retries = input.Retries
	}
	next.UserAgent = valueOr(input.UserAgent, base.UserAgent)
	next.XAIInferenceUserAgent = valueOr(input.XAIInferenceUserAgent, valueOr(base.XAIInferenceUserAgent, DefaultXAIInferenceUserAgent))
	next.XAIInferenceEnabled = input.XAIInferenceEnabled
	next.XAIInferenceModel = valueOr(input.XAIInferenceModel, valueOr(base.XAIInferenceModel, DefaultXAIInspectionModel))
	next.XAIInferencePrompt = valueOr(input.XAIInferencePrompt, valueOr(base.XAIInferencePrompt, DefaultXAIInspectionPrompt))
	next.UsedPercentThreshold = normalizePercent(input.UsedPercentThreshold, base.UsedPercentThreshold)
	if input.SampleSize >= 0 {
		next.SampleSize = input.SampleSize
	}
	next.AutoActionMode = NormalizeCodexInspectionAutoActionMode(input.AutoActionMode, base.AutoActionMode)
	// Frontend and API saves submit the complete inspection config. Keeping this
	// assignment explicit makes the safe false default win for legacy configs.
	next.AutoRecoverEnabled = input.AutoRecoverEnabled
	return next
}

func NormalizeCodexInspectionSchedule(input ManagerCodexInspectionScheduleConfig, fallback ManagerCodexInspectionScheduleConfig) ManagerCodexInspectionScheduleConfig {
	fallbackTimeZone := strings.TrimSpace(fallback.TimeZone)
	base := fallback
	if base.Mode == "" {
		base = DefaultCodexInspectionConfig().Schedule
	}
	next := base

	timePoints := NormalizeCodexInspectionTimePoints(input.TimePoints)
	if len(timePoints) > 0 {
		next.TimePoints = timePoints
	}
	if input.IntervalMinutes > 0 {
		next.IntervalMinutes = input.IntervalMinutes
	}
	next.TimeZone = NormalizeCodexInspectionTimeZone(input.TimeZone, fallbackTimeZone)

	switch strings.ToLower(strings.TrimSpace(input.Mode)) {
	case CodexInspectionScheduleModeTimePoints:
		next.Mode = CodexInspectionScheduleModeTimePoints
	case CodexInspectionScheduleModeInterval:
		next.Mode = CodexInspectionScheduleModeInterval
	case "":
		if len(timePoints) > 0 {
			next.Mode = CodexInspectionScheduleModeTimePoints
		} else if input.IntervalMinutes > 0 {
			next.Mode = CodexInspectionScheduleModeInterval
		}
	}

	if next.Mode == CodexInspectionScheduleModeTimePoints && len(next.TimePoints) == 0 {
		next.Mode = CodexInspectionScheduleModeInterval
	}
	if next.Mode == CodexInspectionScheduleModeInterval && next.IntervalMinutes <= 0 {
		next.IntervalMinutes = 60
	}
	return next
}

// NormalizeCodexInspectionTimeZone validates IANA time zone strings via
// time.LoadLocation. Empty/invalid values fall back to the provided default
// (which may itself be empty, meaning the server's local time zone).
func NormalizeCodexInspectionTimeZone(value string, fallback string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return strings.TrimSpace(fallback)
	}
	if _, err := time.LoadLocation(trimmed); err != nil {
		return strings.TrimSpace(fallback)
	}
	return trimmed
}

func ValidateCodexInspectionConfig(input ManagerCodexInspectionConfig) error {
	targetType := strings.ToLower(strings.TrimSpace(input.TargetType))
	if targetType != "" && !IsCodexInspectionTargetType(targetType) {
		return fmt.Errorf("unsupported inspection target type %q", input.TargetType)
	}
	if input.TargetTypes != nil {
		if len(input.TargetTypes) == 0 {
			return fmt.Errorf("at least one inspection target type is required")
		}
		for _, target := range input.TargetTypes {
			if normalized := strings.ToLower(strings.TrimSpace(target)); !IsCodexInspectionTargetType(normalized) {
				return fmt.Errorf("unsupported inspection target type %q", target)
			}
		}
	}
	return ValidateCodexInspectionSchedule(input.Schedule)
}

func IsCodexInspectionTargetType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case CodexInspectionTargetCodex, CodexInspectionTargetXAI:
		return true
	default:
		return false
	}
}

// NormalizeCodexInspectionTargetTypes returns an ordered, de-duplicated list
// of supported providers. A nil values slice represents a legacy payload, so
// its targetType fallback is read; an explicit empty slice is kept empty for
// validation to reject before persistence.
func NormalizeCodexInspectionTargetTypes(values []string, legacyTargetType string) []string {
	if values == nil {
		values = []string{legacyTargetType}
	}
	selected := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if IsCodexInspectionTargetType(normalized) {
			selected[normalized] = struct{}{}
		}
	}
	result := make([]string, 0, len(selected))
	for _, target := range []string{CodexInspectionTargetCodex, CodexInspectionTargetXAI} {
		if _, ok := selected[target]; ok {
			result = append(result, target)
		}
	}
	return result
}

func (c ManagerCodexInspectionConfig) TargetProviders() []string {
	return NormalizeCodexInspectionTargetTypes(c.TargetTypes, c.TargetType)
}

func (c ManagerCodexInspectionConfig) HasTargetProvider(provider string) bool {
	normalized := strings.ToLower(strings.TrimSpace(provider))
	for _, target := range c.TargetProviders() {
		if target == normalized {
			return true
		}
	}
	return false
}

func ValidateCodexInspectionSchedule(input ManagerCodexInspectionScheduleConfig) error {
	return ValidateCodexInspectionTimeZone(input.TimeZone)
}

func ValidateCodexInspectionTimeZone(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if _, err := time.LoadLocation(trimmed); err != nil {
		return fmt.Errorf("invalid time zone %q: %w", trimmed, err)
	}
	return nil
}

// ResolveCodexInspectionLocation returns the time.Location for the schedule.
// An empty or invalid time zone resolves to time.Local so existing deployments
// keep using the server's local time.
func ResolveCodexInspectionLocation(tz string) *time.Location {
	trimmed := strings.TrimSpace(tz)
	if trimmed == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(trimmed)
	if err != nil {
		return time.Local
	}
	return loc
}

func NormalizeCodexInspectionTimePoints(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized, ok := NormalizeCodexInspectionTimePoint(value)
		if !ok {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result
}

func NormalizeCodexInspectionTimePoint(value string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return "", false
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return "", false
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return "", false
	}
	return fmt.Sprintf("%02d:%02d", hour, minute), true
}

func NormalizeCodexInspectionAutoActionMode(value string, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case CodexInspectionAutoActionEnable:
		return CodexInspectionAutoActionEnable
	case CodexInspectionAutoActionDisable:
		return CodexInspectionAutoActionDisable
	case CodexInspectionAutoActionDelete:
		return CodexInspectionAutoActionDelete
	case CodexInspectionAutoActionNone:
		return CodexInspectionAutoActionNone
	default:
		if fallback == CodexInspectionAutoActionEnable ||
			fallback == CodexInspectionAutoActionDisable ||
			fallback == CodexInspectionAutoActionDelete {
			return fallback
		}
		return CodexInspectionAutoActionNone
	}
}

func NormalizeCodexInspectionActionStatus(value string, action string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case CodexInspectionActionStatusNone:
		return CodexInspectionActionStatusNone
	case CodexInspectionActionStatusSuccess:
		return CodexInspectionActionStatusSuccess
	case CodexInspectionActionStatusFailed:
		return CodexInspectionActionStatusFailed
	case CodexInspectionActionStatusSkipped:
		return CodexInspectionActionStatusSkipped
	case CodexInspectionActionStatusNeedsReview:
		return CodexInspectionActionStatusNeedsReview
	case CodexInspectionActionStatusPending:
		return CodexInspectionActionStatusPending
	default:
		switch strings.ToLower(strings.TrimSpace(action)) {
		case CodexInspectionAutoActionDelete, CodexInspectionAutoActionDisable, CodexInspectionAutoActionEnable:
			return CodexInspectionActionStatusPending
		default:
			return CodexInspectionActionStatusNone
		}
	}
}

func MarshalCodexInspectionSettings(settings ManagerCodexInspectionConfig) string {
	data, err := json.Marshal(settings)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func UnmarshalCodexInspectionSettings(raw string) ManagerCodexInspectionConfig {
	settings := DefaultCodexInspectionConfig()
	if strings.TrimSpace(raw) == "" {
		return settings
	}
	var parsed ManagerCodexInspectionConfig
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return settings
	}
	return NormalizeCodexInspectionConfig(parsed, settings)
}

func MarshalCodexInspectionQuotaWindows(windows []CodexInspectionQuotaWindow) string {
	if len(windows) == 0 {
		return ""
	}
	data, err := json.Marshal(windows)
	if err != nil {
		return ""
	}
	return string(data)
}

func UnmarshalCodexInspectionQuotaWindows(raw string) []CodexInspectionQuotaWindow {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var windows []CodexInspectionQuotaWindow
	if err := json.Unmarshal([]byte(raw), &windows); err != nil {
		return nil
	}
	return windows
}

func CodexInspectionTriggerKey(now time.Time, cfg ManagerCodexInspectionConfig) string {
	schedule := cfg.Schedule
	switch schedule.Mode {
	case CodexInspectionScheduleModeTimePoints:
		return now.In(ResolveCodexInspectionLocation(schedule.TimeZone)).Format("2006-01-02 15:04")
	case CodexInspectionScheduleModeInterval:
		if schedule.IntervalMinutes <= 0 {
			return now.Format("2006-01-02T15:04")
		}
		bucket := now.Unix() / int64(schedule.IntervalMinutes*60)
		return fmt.Sprintf("interval:%d:%d", schedule.IntervalMinutes, bucket)
	default:
		return now.Format("2006-01-02T15:04")
	}
}

func CodexInspectionScheduleDue(now time.Time, lastRun time.Time, cfg ManagerCodexInspectionConfig) bool {
	if cfg.Enabled == nil || !*cfg.Enabled {
		return false
	}
	switch cfg.Schedule.Mode {
	case CodexInspectionScheduleModeTimePoints:
		current := now.In(ResolveCodexInspectionLocation(cfg.Schedule.TimeZone)).Format("15:04")
		for _, point := range cfg.Schedule.TimePoints {
			if point == current {
				return true
			}
		}
		return false
	case CodexInspectionScheduleModeInterval:
		if cfg.Schedule.IntervalMinutes <= 0 {
			return false
		}
		if lastRun.IsZero() {
			return true
		}
		return now.Sub(lastRun) >= time.Duration(cfg.Schedule.IntervalMinutes)*time.Minute
	default:
		return false
	}
}

func valueOr(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func valueOrLower(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func positiveOr(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func normalizePercent(value float64, fallback float64) float64 {
	if value == 0 {
		return fallback
	}
	if value > 0 && value <= 1 {
		value *= 100
	}
	if value < 0 || value > 100 {
		return fallback
	}
	return value
}

func boolPtr(value bool) *bool {
	return &value
}
