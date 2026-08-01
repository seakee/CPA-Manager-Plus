package credentialpolicy

import "testing"

func TestEvaluateFailure(t *testing.T) {
	shouldNotRetry := false
	shouldRetry := true
	tests := []struct {
		name            string
		signal          FailureSignal
		wantAction      string
		wantReason      string
		wantAutoDisable bool
		wantDecision    bool
	}{
		{
			name: "xai expired credentials",
			signal: FailureSignal{
				Provider:   "xai",
				StatusCode: 401,
				Summary:    `{"error":"Invalid or expired credentials (auth_kind=bearer, upstream=PermissionDenied, reason=no auth context)"}`,
			},
			wantAction:      ActionReauth,
			wantReason:      ReasonInvalidCredentials,
			wantAutoDisable: true,
			wantDecision:    true,
		},
		{
			name: "xai credential permission denied",
			signal: FailureSignal{
				Provider:    "grok",
				StatusCode:  403,
				ErrorCode:   "permission-denied",
				Summary:     `{"code":"permission-denied","error":"Access to the chat endpoint is denied. Please ensure you’re using the correct credentials. If you believe this is a mistake, please log into console.x.ai and update the permissions, or contact support."}`,
				ShouldRetry: &shouldNotRetry,
			},
			wantAction:      ActionReview,
			wantReason:      ReasonCredentialPermission,
			wantAutoDisable: true,
			wantDecision:    true,
		},
		{
			name: "xai regional permission denied is review only",
			signal: FailureSignal{
				Provider:   "xai",
				StatusCode: 403,
				ErrorCode:  "permission-denied",
				Summary:    `{"code":"permission-denied","error":"The model is not available in your region."}`,
			},
			wantAction:      ActionReview,
			wantReason:      ReasonAuthenticationReview,
			wantAutoDisable: false,
			wantDecision:    true,
		},
		{
			name: "xai retryable credential permission denied is review only",
			signal: FailureSignal{
				Provider:    "xai",
				StatusCode:  403,
				ErrorCode:   "permission-denied",
				Summary:     `{"code":"permission-denied","error":"Access to the chat endpoint is denied. Please ensure you're using the correct credentials and update the permissions."}`,
				ShouldRetry: &shouldRetry,
			},
			wantAction:      ActionReview,
			wantReason:      ReasonAuthenticationReview,
			wantAutoDisable: false,
			wantDecision:    true,
		},
		{
			name:         "status alone does not disable",
			signal:       FailureSignal{Provider: "codex", StatusCode: 401, Summary: "request failed"},
			wantDecision: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := EvaluateFailure(tt.signal)
			if ok != tt.wantDecision {
				t.Fatalf("EvaluateFailure() ok = %v, want %v; decision=%#v", ok, tt.wantDecision, got)
			}
			if !ok {
				return
			}
			if got.Action != tt.wantAction || got.ReasonCode != tt.wantReason || got.AutoDisableEligible != tt.wantAutoDisable {
				t.Fatalf("EvaluateFailure() = %#v", got)
			}
		})
	}
}
