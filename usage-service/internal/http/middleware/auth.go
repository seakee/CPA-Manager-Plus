package middleware

import (
	"errors"
	"net/http"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/response"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/service/managerconfig"
)

func AuthorizeIfConfigured(w http.ResponseWriter, r *http.Request, managerConfigService *managerconfig.Service) bool {
	setup, ok, err := managerConfigService.ResolveSetup(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return false
	}
	if !ok || setup.ManagementKey == "" {
		return true
	}
	if managerconfig.AuthHeaderMatches(r.Header.Get("Authorization"), setup.ManagementKey) {
		return true
	}
	response.Error(w, http.StatusUnauthorized, errors.New("invalid management key"))
	return false
}
