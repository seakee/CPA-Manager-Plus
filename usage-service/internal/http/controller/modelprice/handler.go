package modelprice

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/app"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/response"
	modelpricesvc "github.com/seakee/cpa-manager-plus/usage-service/internal/service/modelprice"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizeIfConfigured(w, r, h.App.ManagerConfigService) {
		return
	}

	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/v0/management/model-prices" && r.Method == http.MethodGet:
		prices, err := h.App.ModelPriceService.List(r.Context())
		if err != nil {
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"prices": prices})
	case path == "/v0/management/model-prices" && r.Method == http.MethodPut:
		var req modelpricesvc.UpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		prices, err := h.App.ModelPriceService.Replace(r.Context(), req.Prices)
		if err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"prices": prices})
	case path == "/v0/management/model-prices/sync" && r.Method == http.MethodPost:
		var req modelpricesvc.SyncRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		result, err := h.App.ModelPriceService.SyncFromLiteLLM(r.Context(), req)
		if err != nil {
			response.Error(w, response.ModelPriceErrorStatus(err), err)
			return
		}
		response.JSON(w, http.StatusOK, result)
	default:
		response.MethodNotAllowed(w)
	}
}
