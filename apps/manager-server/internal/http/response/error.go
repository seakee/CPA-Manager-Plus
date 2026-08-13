package response

import (
	"errors"
	"net/http"
	"strings"
)

func Error(w http.ResponseWriter, status int, err error) {
	code := UsageServiceErrorCode(err)
	JSON(w, status, map[string]any{"error": PublicErrorMessage(code, err), "code": code})
}

func PublicErrorMessage(code string, err error) string {
	switch code {
	case "update_staged_version_changed":
		return "the running version changed after this update was prepared; prepare the update again"
	case "update_target_not_newer":
		return "the prepared update is no longer newer than the running version"
	case "update_manual_recovery_required":
		return "manual update recovery is required before another update can be prepared"
	case "update_target_unstable":
		return "the target version did not remain healthy; the previous version was restored"
	case "update_private_storage_unavailable":
		return "private update storage could not be secured; no program files were replaced"
	case "request_failed":
		if managedUpdateError(err) {
			return "managed update failed; review the update status and Manager Server logs"
		}
	}
	return err.Error()
}

func managedUpdateError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "managed update") ||
		strings.Contains(message, "update transaction") ||
		strings.Contains(message, "staged update") ||
		strings.Contains(message, "rollback snapshot") ||
		strings.Contains(message, "target version") ||
		strings.Contains(message, "control script") ||
		strings.Contains(message, "private directory") ||
		strings.Contains(message, "private file")
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
		strings.Contains(message, "pollIntervalMs must be less than or equal"),
		strings.Contains(message, "invalid time zone"):
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
	case strings.Contains(message, "connection setup is managed by environment variables"),
		strings.Contains(message, "locked by environment variable"):
		return http.StatusConflict
	case strings.Contains(message, "CPA connection is already bound"):
		return http.StatusConflict
	case strings.Contains(message, "cpaBaseUrl and managementKey are required"),
		strings.Contains(message, "CPA redis-usage-queue-retention-seconds"),
		strings.Contains(message, "pollIntervalMs must be less than or equal"),
		strings.Contains(message, "invalid time zone"):
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
	case strings.Contains(message, "locked by environment variable"):
		return "account_processing_policy_env_locked"
	case strings.Contains(message, "CPA connection is already bound"):
		return "cpa_connection_already_bound"
	case strings.Contains(message, "cpaBaseUrl and managementKey are required when request monitoring is enabled"):
		return "cpa_connection_required_for_monitoring"
	case strings.Contains(message, "cpaBaseUrl and managementKey are required"):
		return "cpa_connection_required"
	case strings.Contains(message, "setup is managed by environment variables"):
		return "setup_env_managed"
	case strings.Contains(message, "invalid management key for existing setup"):
		return "invalid_existing_management_key"
	case strings.Contains(message, "invalid admin key"):
		return "invalid_admin_key"
	case strings.Contains(message, "invalid management key"):
		return "invalid_management_key"
	case strings.Contains(message, "usage service is not configured"):
		return "usage_service_not_configured"
	case strings.Contains(message, "CPA redis-usage-queue-retention-seconds must be greater than 0"):
		return "cpa_usage_retention_invalid"
	case strings.Contains(message, "pollIntervalMs must be less than or equal"):
		return "poll_interval_exceeds_retention"
	case strings.Contains(message, "invalid time zone"):
		return "invalid_time_zone"
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
	case strings.Contains(message, "managed native updates are not supported"):
		return "managed_updates_not_supported"
	case strings.Contains(message, "an update transaction is already active"):
		return "update_already_active"
	case strings.Contains(message, "no staged update is ready to apply"):
		return "update_not_staged"
	case strings.Contains(message, "managed shutdown is unavailable"):
		return "managed_shutdown_unavailable"
	case strings.Contains(message, "no newer stable release is available"):
		return "no_update_available"
	case strings.Contains(message, "not ready for managed updates"):
		return "release_not_update_ready"
	case strings.Contains(message, "backup preflight failed"):
		return "update_backup_preflight_failed"
	case strings.Contains(message, "managed update is still active"):
		return "update_still_active"
	case strings.Contains(message, "staged update no longer matches"):
		return "update_staged_version_changed"
	case strings.Contains(message, "staged update target is not newer"):
		return "update_target_not_newer"
	case strings.Contains(message, "manual update recovery is required"):
		return "update_manual_recovery_required"
	case strings.Contains(message, "did not remain healthy"):
		return "update_target_unstable"
	case strings.Contains(message, "restrict private"):
		return "update_private_storage_unavailable"
	case strings.Contains(message, "method not allowed"):
		return "method_not_allowed"
	default:
		return "request_failed"
	}
}
