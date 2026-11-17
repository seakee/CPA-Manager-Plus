package usage

import (
	"strings"
	"testing"
)

func TestNormalizeRawPrefersCPAEffectiveServiceTier(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantTier     string
		wantRequest  string
		wantResponse string
		wantRawKeeps []string
	}{
		{
			name: "codex fast injected by payload override bills priority",
			raw: `{"request_id":"r1","timestamp":"2026-11-07T00:00:00Z","provider":"codex","executor_type":"codex",
				"model":"gpt-6-astra","alias":"gpt-6-astra-fast",
				"service_tier":"auto","effective_service_tier":"priority","response_service_tier":"default",
				"tokens":{"input_tokens":10,"output_tokens":5}}`,
			wantTier:     "priority",
			wantRequest:  "priority",
			wantResponse: "default",
			wantRawKeeps: []string{`"service_tier":"auto"`, `"response_service_tier":"default"`},
		},
		{
			name: "camelCase effective tier is accepted",
			raw: `{"request_id":"r2","timestamp":"2026-11-07T00:00:00Z","provider":"codex",
				"model":"gpt-6-astra","serviceTier":"auto","effectiveServiceTier":"priority",
				"tokens":{"input_tokens":10,"output_tokens":5}}`,
			wantTier:    "priority",
			wantRequest: "priority",
		},
		{
			name: "older CPA without effective tier keeps request tier",
			raw: `{"request_id":"r3","timestamp":"2026-11-07T00:00:00Z","provider":"codex",
				"model":"gpt-6-astra","service_tier":"auto","response_service_tier":"default",
				"tokens":{"input_tokens":10,"output_tokens":5}}`,
			wantTier:     "auto",
			wantRequest:  "auto",
			wantResponse: "default",
		},
		{
			name: "empty effective tier falls back to request tier",
			raw: `{"request_id":"r4","timestamp":"2026-11-07T00:00:00Z","provider":"codex",
				"model":"gpt-6-astra","request_service_tier":"priority","effective_service_tier":"",
				"tokens":{"input_tokens":10,"output_tokens":5}}`,
			wantTier:    "priority",
			wantRequest: "priority",
		},
		{
			name: "null effective tier falls back to client tier",
			raw: `{"request_id":"r6","timestamp":"2026-11-07T00:00:00Z","provider":"codex",
				"model":"gpt-6-astra","service_tier":"priority","effective_service_tier":null,
				"tokens":{"input_tokens":10,"output_tokens":5}}`,
			wantTier:    "priority",
			wantRequest: "priority",
		},
		{
			name: "non-codex providers keep reported tier precedence",
			raw: `{"request_id":"r5","timestamp":"2026-11-07T00:00:00Z","provider":"openai",
				"model":"gpt-5","service_tier":"auto","effective_service_tier":"priority","response_service_tier":"default",
				"tokens":{"input_tokens":10,"output_tokens":5}}`,
			wantTier:     "default",
			wantRequest:  "priority",
			wantResponse: "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := NormalizeRaw([]byte(tt.raw))
			if err != nil {
				t.Fatalf("NormalizeRaw: %v", err)
			}
			if event.ServiceTier != tt.wantTier {
				t.Fatalf("ServiceTier = %q, want %q", event.ServiceTier, tt.wantTier)
			}
			if event.RequestServiceTier != tt.wantRequest {
				t.Fatalf("RequestServiceTier = %q, want %q", event.RequestServiceTier, tt.wantRequest)
			}
			if event.ResponseServiceTier != tt.wantResponse {
				t.Fatalf("ResponseServiceTier = %q, want %q", event.ResponseServiceTier, tt.wantResponse)
			}
			for _, fragment := range tt.wantRawKeeps {
				if !strings.Contains(event.RawJSON, fragment) {
					t.Fatalf("RawJSON lost client-side diagnostic %s: %s", fragment, event.RawJSON)
				}
			}
		})
	}
}
