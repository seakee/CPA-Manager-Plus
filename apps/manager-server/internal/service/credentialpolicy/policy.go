package credentialpolicy

import "strings"

const (
	ActionDelete = "delete"
	ActionReauth = "reauth"
	ActionReview = "review"

	ReasonAccountDeactivated   = "account_deactivated"
	ReasonWorkspaceDeactivated = "workspace_deactivated"
	ReasonTokenRevoked         = "token_revoked"
	ReasonInvalidCredentials   = "invalid_credentials"
	ReasonAuthenticationReview = "authentication_review"
	ReasonCredentialPermission = "credential_permission_denied"
	ConfidenceHigh             = "high"
	ConfidenceMedium           = "medium"
)

type FailureSignal struct {
	Provider    string
	StatusCode  int
	ErrorCode   string
	ErrorType   string
	Summary     string
	ShouldRetry *bool
}

type Decision struct {
	Action              string
	ReasonCode          string
	Reason              string
	Confidence          string
	AutoDisableEligible bool
}

func EvaluateFailure(signal FailureSignal) (Decision, bool) {
	provider := NormalizeProvider(signal.Provider)
	code := normalizeToken(signal.ErrorCode)
	typ := normalizeToken(signal.ErrorType)
	text := strings.ToLower(strings.Join([]string{signal.Summary, code, typ}, "\n"))

	if signal.StatusCode == 402 && strings.Contains(text, "deactivated_workspace") {
		return Decision{
			Action:              ActionDelete,
			ReasonCode:          ReasonWorkspaceDeactivated,
			Reason:              "Workspace is deactivated; review and delete the stale auth file if appropriate",
			Confidence:          ConfidenceHigh,
			AutoDisableEligible: true,
		}, true
	}
	if signal.StatusCode != 401 && signal.StatusCode != 403 {
		return Decision{}, false
	}

	if strings.Contains(text, "account_deactivated") {
		return Decision{
			Action:              ActionDelete,
			ReasonCode:          ReasonAccountDeactivated,
			Reason:              "Account is deactivated; review and delete the stale auth file if appropriate",
			Confidence:          ConfidenceHigh,
			AutoDisableEligible: true,
		}, true
	}
	if containsAny(text,
		"token_revoked",
		"token_invalidated",
		"invalidated_oauth_token",
		"invalidated oauth token",
		"oauth token revoked",
		"authentication token has been invalidated",
		"token has been invalidated",
	) {
		return Decision{
			Action:              ActionReauth,
			ReasonCode:          ReasonTokenRevoked,
			Reason:              "OAuth token was revoked or invalidated; reauthorize the account",
			Confidence:          ConfidenceHigh,
			AutoDisableEligible: true,
		}, true
	}
	if containsAny(text,
		"invalid_token",
		"invalid or expired credentials",
		"provided authentication token is expired",
		"authentication token is expired",
		"token is expired",
		"no auth context",
		"invalid_grant",
		"auth_unavailable",
		"requires reauthorization",
		"requires re-authentication",
	) {
		return Decision{
			Action:              ActionReauth,
			ReasonCode:          ReasonInvalidCredentials,
			Reason:              "Credentials are invalid or expired; reauthorize the account",
			Confidence:          ConfidenceHigh,
			AutoDisableEligible: true,
		}, true
	}

	if provider == "xai" && code == "permission_denied" && xaiCredentialPermissionDenied(text, signal.ShouldRetry) {
		return Decision{
			Action:              ActionReview,
			ReasonCode:          ReasonCredentialPermission,
			Reason:              "xAI rejected this credential for the chat endpoint; review credential permissions",
			Confidence:          ConfidenceHigh,
			AutoDisableEligible: true,
		}, true
	}

	if typ == "authentication_error" || containsAny(text, "authentication_error", "unauthorized", "forbidden", "permission_denied") {
		return Decision{
			Action:              ActionReview,
			ReasonCode:          ReasonAuthenticationReview,
			Reason:              "Authentication failure requires manual review",
			Confidence:          ConfidenceMedium,
			AutoDisableEligible: false,
		}, true
	}
	return Decision{}, false
}

func NormalizeProvider(value string) string {
	normalized := normalizeToken(value)
	switch normalized {
	case "x_ai", "grok":
		return "xai"
	default:
		return normalized
	}
}

func normalizeToken(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	return normalized
}

func xaiCredentialPermissionDenied(text string, shouldRetry *bool) bool {
	credentialMessage := strings.Contains(text, "access to the chat endpoint is denied") &&
		containsAny(text, "correct credentials", "update the permissions", "contact support")
	if !credentialMessage {
		return false
	}
	return shouldRetry == nil || !*shouldRetry
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}
