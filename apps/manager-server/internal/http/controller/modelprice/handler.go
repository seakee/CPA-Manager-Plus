package modelprice

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	modelpricesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/modelprice"
)

type Handler struct {
	App *app.Context
}

func ensureNoTrailingJSONValue(decoder *json.Decoder) error {
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func decodeSyncRequest(body io.Reader) (modelpricesvc.SyncRequest, error) {
	decoder := json.NewDecoder(body)
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		if errors.Is(err, io.EOF) {
			return modelpricesvc.SyncRequest{}, nil
		}
		return modelpricesvc.SyncRequest{}, err
	}
	if fields == nil {
		return modelpricesvc.SyncRequest{}, errors.New("model price sync request must be a JSON object")
	}
	if err := ensureNoTrailingJSONValue(decoder); err != nil {
		return modelpricesvc.SyncRequest{}, err
	}

	for key := range fields {
		if key != "models" && key != "source" {
			return modelpricesvc.SyncRequest{}, errors.New("unknown model price sync request field: " + key)
		}
	}

	var req modelpricesvc.SyncRequest
	if rawModels, ok := fields["models"]; ok && !bytes.Equal(bytes.TrimSpace(rawModels), []byte("null")) {
		if err := json.Unmarshal(rawModels, &req.Models); err != nil {
			return modelpricesvc.SyncRequest{}, errors.New("invalid model price sync models: " + err.Error())
		}
	}
	if rawSource, ok := fields["source"]; ok {
		if bytes.Equal(bytes.TrimSpace(rawSource), []byte("null")) {
			return modelpricesvc.SyncRequest{}, errors.New("model price sync source must be a string")
		}
		if err := json.Unmarshal(rawSource, &req.Source); err != nil {
			return modelpricesvc.SyncRequest{}, errors.New("model price sync source must be a string")
		}
		normalized, err := modelpricesvc.NormalizeSyncSource(req.Source)
		if err != nil {
			return modelpricesvc.SyncRequest{}, err
		}
		if normalized == "" {
			return modelpricesvc.SyncRequest{}, errors.New("model price sync source must be models.dev, litellm, or openrouter")
		}
		req.Source = normalized
	}
	return req, nil
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}

	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/v0/management/model-prices/usage-summary" && r.Method == http.MethodGet:
		summary, err := h.App.ModelPriceService.UsageSummary(r.Context(), h.App.Config.QueryLimit)
		if err != nil {
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		response.JSON(w, http.StatusOK, summary)
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
		req, err := decodeSyncRequest(r.Body)
		if err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		result, err := h.App.ModelPriceService.Sync(r.Context(), req)
		if err != nil {
			response.Error(w, response.ModelPriceErrorStatus(err), err)
			return
		}
		response.JSON(w, http.StatusOK, result)
	default:
		response.MethodNotAllowed(w)
	}
}
