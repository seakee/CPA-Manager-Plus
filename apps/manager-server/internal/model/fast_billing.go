package model

import "strings"

// FastBillingMode selects how Fast/Priority usage is represented in local cost estimates.
type FastBillingMode string

const (
	FastBillingModeAPIPriority  FastBillingMode = "api_priority"
	FastBillingModeCodexCredits FastBillingMode = "codex_credits"
	FastBillingModeAutomatic    FastBillingMode = "automatic"
)

// FastBillingProviderOverride applies a billing mode to one CPA provider/channel.
type FastBillingProviderOverride struct {
	Provider string          `json:"provider"`
	Mode     FastBillingMode `json:"mode"`
}

// FastBillingSettings controls the local estimated-cost multiplier for requests
// whose effective service tier is fast or priority.
type FastBillingSettings struct {
	Mode              FastBillingMode               `json:"mode"`
	ProviderOverrides []FastBillingProviderOverride `json:"providerOverrides,omitempty"`
	UpdatedAtMS       int64                         `json:"updatedAtMs,omitempty"`
}

func DefaultFastBillingSettings() FastBillingSettings {
	return FastBillingSettings{Mode: FastBillingModeAPIPriority}
}

func NormalizeFastBillingMode(mode FastBillingMode) FastBillingMode {
	switch FastBillingMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case FastBillingModeAPIPriority:
		return FastBillingModeAPIPriority
	case FastBillingModeCodexCredits:
		return FastBillingModeCodexCredits
	case FastBillingModeAutomatic:
		return FastBillingModeAutomatic
	default:
		return ""
	}
}

func NormalizeFastBillingSettings(settings FastBillingSettings) FastBillingSettings {
	mode := NormalizeFastBillingMode(settings.Mode)
	if mode == "" {
		mode = FastBillingModeAPIPriority
	}
	result := FastBillingSettings{Mode: mode, UpdatedAtMS: settings.UpdatedAtMS}
	seen := map[string]bool{}
	for _, override := range settings.ProviderOverrides {
		provider := strings.ToLower(strings.TrimSpace(override.Provider))
		overrideMode := NormalizeFastBillingMode(override.Mode)
		if provider == "" || (overrideMode != FastBillingModeAPIPriority && overrideMode != FastBillingModeCodexCredits) || seen[provider] {
			continue
		}
		seen[provider] = true
		result.ProviderOverrides = append(result.ProviderOverrides, FastBillingProviderOverride{
			Provider: provider,
			Mode:     overrideMode,
		})
	}
	return result
}

func (settings FastBillingSettings) ProviderAware() bool {
	normalized := NormalizeFastBillingSettings(settings)
	return normalized.Mode == FastBillingModeAutomatic || len(normalized.ProviderOverrides) > 0
}

func (settings FastBillingSettings) ResolveMode(provider string) FastBillingMode {
	normalized := NormalizeFastBillingSettings(settings)
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, override := range normalized.ProviderOverrides {
		if override.Provider == provider {
			return override.Mode
		}
	}
	if normalized.Mode == FastBillingModeAutomatic {
		if provider == "codex" {
			return FastBillingModeCodexCredits
		}
		return FastBillingModeAPIPriority
	}
	return normalized.Mode
}
