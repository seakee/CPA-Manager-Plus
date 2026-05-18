package response

import (
	"errors"
	"net/http"
	"strings"
)

func Error(w http.ResponseWriter, status int, err error) {
	JSON(w, status, map[string]any{"error": err.Error(), "code": UsageServiceErrorCode(err)})
}

func MethodNotAllowed(w http.ResponseWriter) {
	Error(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}

func SetupErrorStatus(err error) int {
	message := err.Error()
	switch {
	case strings.Contains(message, "setup is managed by environment variables"):
		return http.StatusConflict
	case strings.Contains(message, "invalid management key for existing setup"):
		return http.StatusUnauthorized
	case strings.Contains(message, "cpaBaseUrl and managementKey are required"),
		strings.Contains(message, "CPA redis-usage-queue-retention-seconds"),
		strings.Contains(message, "pollIntervalMs must be less than or equal"):
		return http.StatusBadRequest
	case strings.Contains(message, "management API validation failed"),
		strings.Contains(message, "enable CPA usage statistics failed"):
		return http.StatusBadGateway
	default:
		return http.StatusBadGateway
	}
}

func ManagerConfigErrorStatus(err error) int {
	message := err.Error()
	switch {
	case strings.Contains(message, "connection setup is managed by environment variables"):
		return http.StatusConflict
	case strings.Contains(message, "cpaBaseUrl and managementKey are required"),
		strings.Contains(message, "CPA redis-usage-queue-retention-seconds"),
		strings.Contains(message, "pollIntervalMs must be less than or equal"):
		return http.StatusBadRequest
	case strings.Contains(message, "management API validation failed"),
		strings.Contains(message, "management API config request failed"),
		strings.Contains(message, "enable CPA usage statistics failed"):
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

func ModelPriceErrorStatus(err error) int {
	if strings.Contains(err.Error(), "model price sync failed") {
		return http.StatusBadGateway
	}
	return http.StatusInternalServerError
}

func UsageServiceErrorCode(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "connection setup is managed by environment variables"):
		return "connection_env_managed"
	case strings.Contains(message, "cpaBaseUrl and managementKey are required when request monitoring is enabled"):
		return "cpa_connection_required_for_monitoring"
	case strings.Contains(message, "cpaBaseUrl and managementKey are required"):
		return "cpa_connection_required"
	case strings.Contains(message, "setup is managed by environment variables"):
		return "setup_env_managed"
	case strings.Contains(message, "invalid management key for existing setup"):
		return "invalid_existing_management_key"
	case strings.Contains(message, "invalid management key"):
		return "invalid_management_key"
	case strings.Contains(message, "usage service is not configured"):
		return "usage_service_not_configured"
	case strings.Contains(message, "CPA redis-usage-queue-retention-seconds must be greater than 0"):
		return "cpa_usage_retention_invalid"
	case strings.Contains(message, "pollIntervalMs must be less than or equal"):
		return "poll_interval_exceeds_retention"
	case strings.Contains(message, "management API validation failed"):
		return "management_api_validation_failed"
	case strings.Contains(message, "management API config request failed"):
		return "management_api_config_failed"
	case strings.Contains(message, "enable CPA usage statistics failed"):
		return "enable_cpa_usage_statistics_failed"
	case strings.Contains(message, "prices are required"):
		return "prices_required"
	case strings.Contains(message, "api key aliases are required"):
		return "api_key_aliases_required"
	case strings.Contains(message, "api key alias already exists"):
		return "api_key_alias_duplicate"
	case strings.Contains(message, "model price sync failed"):
		return "model_price_sync_failed"
	case strings.Contains(message, "method not allowed"):
		return "method_not_allowed"
	default:
		return "request_failed"
	}
}
