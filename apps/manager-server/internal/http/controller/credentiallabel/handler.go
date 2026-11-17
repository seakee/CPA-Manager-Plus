package credentiallabel

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	service "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/credentiallabel"
)

type Handler struct{ App *app.Context }

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	const base = "/v0/management/credential-labels"
	path := strings.TrimRight(r.URL.Path, "/")
	if path == base && r.Method == http.MethodGet {
		items, err := h.App.CredentialLabelService.List(r.Context())
		if err != nil {
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"items": items})
		return
	}
	if !strings.HasPrefix(path, base+"/") || r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		Label *string `json:"label"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		response.Error(w, http.StatusBadRequest, errors.New("valid label body is required"))
		return
	}
	if body.Label == nil {
		response.Error(w, http.StatusBadRequest, errors.New("label must be a string"))
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		response.Error(w, http.StatusBadRequest, errors.New("one JSON object is required"))
		return
	}
	item, err := h.App.CredentialLabelService.Set(r.Context(), strings.TrimPrefix(path, base+"/"), *body.Label)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, service.ErrInvalid) {
			status = http.StatusBadRequest
		}
		response.Error(w, status, err)
		return
	}
	response.JSON(w, http.StatusOK, item)
}
