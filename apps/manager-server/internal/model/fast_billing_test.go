package model

import "testing"

func TestFastBillingSettingsResolveMode(t *testing.T) {
	tests := []struct {
		name     string
		settings FastBillingSettings
		provider string
		want     FastBillingMode
	}{
		{name: "default", settings: FastBillingSettings{}, provider: "codex", want: FastBillingModeAPIPriority},
		{name: "codex credits", settings: FastBillingSettings{Mode: FastBillingModeCodexCredits}, provider: "openai", want: FastBillingModeCodexCredits},
		{name: "automatic codex", settings: FastBillingSettings{Mode: FastBillingModeAutomatic}, provider: "codex", want: FastBillingModeCodexCredits},
		{name: "automatic api", settings: FastBillingSettings{Mode: FastBillingModeAutomatic}, provider: "openai", want: FastBillingModeAPIPriority},
		{name: "provider override", settings: FastBillingSettings{Mode: FastBillingModeAPIPriority, ProviderOverrides: []FastBillingProviderOverride{{Provider: " CODEX ", Mode: FastBillingModeCodexCredits}}}, provider: "codex", want: FastBillingModeCodexCredits},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.settings.ResolveMode(tt.provider); got != tt.want {
				t.Fatalf("ResolveMode(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}
