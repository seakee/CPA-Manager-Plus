package managerconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/requestbody"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const maxManagerConfigPutBodyBytes int64 = 64 << 10

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !h.authorizeRead(w, r) {
			return
		}
		result, err := h.App.ManagerConfigService.Get(r.Context())
		if err != nil {
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		response.JSON(w, http.StatusOK, result)
	case http.MethodPut:
		ok, err := h.App.AdminAuthService.VerifySubmittedExternalConfigHeader(
			r.Context(),
			r.Header.Get("Authorization"),
			store.ManagerConfig{},
		)
		if err != nil {
			closeUnreadManagerConfigBody(w, r)
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			closeUnreadManagerConfigBody(w, r)
			response.Error(w, http.StatusUnauthorized, errors.New("invalid admin key"))
			return
		}
		var req struct {
			Config store.ManagerConfig `json:"config"`
		}
		body, readErr := io.ReadAll(http.MaxBytesReader(requestbody.UnwrapRootResponseWriter(w), r.Body, maxManagerConfigPutBodyBytes))
		if readErr != nil || !utf8.Valid(body) {
			if readErr != nil {
				closeUnreadManagerConfigBody(w, r)
			} else {
				readErr = errors.New("invalid request body")
			}
			response.Error(w, http.StatusBadRequest, readErr)
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		if err := decoder.Decode(&req); err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		if _, err := decoder.Token(); err != io.EOF {
			response.Error(w, http.StatusBadRequest, errors.New("unexpected trailing request data"))
			return
		}
		result, err := h.App.ManagerConfigService.Update(r.Context(), req.Config)
		if err != nil {
			response.Error(w, response.ManagerConfigErrorStatus(err), err)
			return
		}
		response.JSON(w, http.StatusOK, result)
	case http.MethodPatch:
		// Each PATCH applies one narrow configuration action — request-monitoring
		// switch, collector tuning, or retention policy — so callers never mix
		// independent side effects or overwrite fields they do not own.
		ok, err := h.App.AdminAuthService.VerifyHeader(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			closeUnreadManagerConfigBody(w, r)
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			closeUnreadManagerConfigBody(w, r)
			response.Error(w, http.StatusUnauthorized, errors.New("invalid admin key"))
			return
		}
		req, err := decodeManagerConfigPatchRequest(w, r)
		if err != nil {
			response.Error(w, http.StatusBadRequest, errors.New("exactly one of enabled, collector, or retention is required"))
			return
		}
		var result any
		switch {
		case req.Enabled != nil:
			result, err = h.App.ManagerConfigService.SetCollectorEnabled(r.Context(), *req.Enabled)
		case req.Collector != nil:
			result, err = h.App.ManagerConfigService.PatchCollector(r.Context(), *req.Collector)
		default:
			result, err = h.App.ManagerConfigService.PatchRetention(r.Context(), *req.Retention)
		}
		if err != nil {
			response.Error(w, response.ManagerConfigErrorStatus(err), err)
			return
		}
		response.JSON(w, http.StatusOK, result)
	default:
		closeUnreadManagerConfigBody(w, r)
		response.MethodNotAllowed(w)
	}
}

type managerConfigPatchRequest struct {
	Enabled   *bool
	Collector *store.ManagerCollectorPatch
	Retention *store.ManagerRetentionPatch
}

func closeUnreadManagerConfigBody(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return
	}
	w.Header().Set("Connection", "close")
	_ = http.NewResponseController(w).SetReadDeadline(time.Unix(1, 0))
}

func decodeManagerConfigPatchRequest(w http.ResponseWriter, r *http.Request) (managerConfigPatchRequest, error) {
	invalidErr := errors.New("exactly one of enabled, collector, or retention is required")
	body, err := io.ReadAll(http.MaxBytesReader(requestbody.UnwrapRootResponseWriter(w), r.Body, 4096))
	if err != nil || !utf8.Valid(body) {
		if err != nil {
			closeUnreadManagerConfigBody(w, r)
		}
		return managerConfigPatchRequest{}, invalidErr
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return managerConfigPatchRequest{}, invalidErr
	}
	seen := make(map[string]bool, 3)
	var req managerConfigPatchRequest
	for decoder.More() {
		keyToken, err := decoder.Token()
		field, ok := keyToken.(string)
		if err != nil || !ok || seen[field] {
			return managerConfigPatchRequest{}, invalidErr
		}
		seen[field] = true
		switch field {
		case "enabled":
			val, ok := decodeJSONBool(decoder)
			if !ok {
				return managerConfigPatchRequest{}, invalidErr
			}
			req.Enabled = &val
		case "collector":
			patch, ok := decodeCollectorPatchObject(decoder)
			if !ok || patch.IsEmpty() {
				return managerConfigPatchRequest{}, invalidErr
			}
			req.Collector = &patch
		case "retention":
			patch, ok := decodeRetentionPatchObject(decoder)
			if !ok || patch.IsEmpty() {
				return managerConfigPatchRequest{}, invalidErr
			}
			req.Retention = &patch
		default:
			return managerConfigPatchRequest{}, invalidErr
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return managerConfigPatchRequest{}, invalidErr
	}
	if _, err := decoder.Token(); err != io.EOF {
		return managerConfigPatchRequest{}, invalidErr
	}
	if len(seen) != 1 {
		return managerConfigPatchRequest{}, invalidErr
	}
	return req, nil
}

func decodeCollectorPatchObject(decoder *json.Decoder) (store.ManagerCollectorPatch, bool) {
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return store.ManagerCollectorPatch{}, false
	}
	seen := make(map[string]bool, 7)
	var patch store.ManagerCollectorPatch
	for decoder.More() {
		keyToken, err := decoder.Token()
		field, ok := keyToken.(string)
		if err != nil || !ok || seen[field] {
			return store.ManagerCollectorPatch{}, false
		}
		seen[field] = true
		switch field {
		case "collectorMode":
			val, ok := decodeJSONString(decoder)
			if !ok {
				return store.ManagerCollectorPatch{}, false
			}
			patch.CollectorMode = &val
		case "queue":
			val, ok := decodeJSONString(decoder)
			if !ok {
				return store.ManagerCollectorPatch{}, false
			}
			patch.Queue = &val
		case "popSide":
			val, ok := decodeJSONString(decoder)
			if !ok {
				return store.ManagerCollectorPatch{}, false
			}
			patch.PopSide = &val
		case "batchSize":
			val, ok := decodeJSONInt(decoder)
			if !ok {
				return store.ManagerCollectorPatch{}, false
			}
			patch.BatchSize = &val
		case "pollIntervalMs":
			val, ok := decodeJSONInt(decoder)
			if !ok {
				return store.ManagerCollectorPatch{}, false
			}
			patch.PollIntervalMS = &val
		case "queryLimit":
			val, ok := decodeJSONInt(decoder)
			if !ok {
				return store.ManagerCollectorPatch{}, false
			}
			patch.QueryLimit = &val
		case "tlsSkipVerify":
			val, ok := decodeJSONBool(decoder)
			if !ok {
				return store.ManagerCollectorPatch{}, false
			}
			patch.TLSSkipVerify = &val
		default:
			return store.ManagerCollectorPatch{}, false
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return store.ManagerCollectorPatch{}, false
	}
	return patch, len(seen) > 0
}

func decodeRetentionPatchObject(decoder *json.Decoder) (store.ManagerRetentionPatch, bool) {
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return store.ManagerRetentionPatch{}, false
	}
	seen := make(map[string]bool, 2)
	var patch store.ManagerRetentionPatch
	for decoder.More() {
		keyToken, err := decoder.Token()
		field, ok := keyToken.(string)
		if err != nil || !ok || seen[field] {
			return store.ManagerRetentionPatch{}, false
		}
		seen[field] = true
		switch field {
		case "enabled":
			val, ok := decodeJSONBool(decoder)
			if !ok {
				return store.ManagerRetentionPatch{}, false
			}
			patch.Enabled = &val
		case "retentionDays":
			val, ok := decodeJSONInt(decoder)
			if !ok {
				return store.ManagerRetentionPatch{}, false
			}
			patch.RetentionDays = &val
		default:
			return store.ManagerRetentionPatch{}, false
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return store.ManagerRetentionPatch{}, false
	}
	return patch, len(seen) > 0
}

func decodeJSONBool(decoder *json.Decoder) (bool, bool) {
	token, err := decoder.Token()
	if err != nil {
		return false, false
	}
	val, ok := token.(bool)
	return val, ok
}

func decodeJSONString(decoder *json.Decoder) (string, bool) {
	token, err := decoder.Token()
	if err != nil {
		return "", false
	}
	val, ok := token.(string)
	return val, ok
}

func decodeJSONInt(decoder *json.Decoder) (int, bool) {
	token, err := decoder.Token()
	if err != nil {
		return 0, false
	}
	num, ok := token.(json.Number)
	if !ok {
		return 0, false
	}
	val, err := strconv.Atoi(num.String())
	if err != nil {
		return 0, false
	}
	return val, true
}

func (h *Handler) authorizeRead(w http.ResponseWriter, r *http.Request) bool {
	ok, err := h.App.AdminAuthService.VerifyPanelHeader(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return false
	}
	if ok {
		return true
	}
	setup, setupOK, err := h.App.ManagerConfigService.ResolveSetup(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return false
	}
	if !setupOK || setup.ManagementKey == "" {
		return true
	}
	response.Error(w, http.StatusUnauthorized, errors.New("invalid admin key"))
	return false
}

// ValidateCPAConnection is the strict server-side CPA connection validation
// endpoint used by the installer after a connection import. It requires CPAMP
// admin auth, ignores any client-supplied CPA Management Key, resolves the
// connection Manager Server actually uses, and propagates every CPA upstream
// failure (auth, network, 5xx) as a non-2xx response so the installer fails
// closed instead of committing a migration on a false positive.
func (h *Handler) ValidateCPAConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		closeUnreadManagerConfigBody(w, r)
		response.MethodNotAllowed(w)
		return
	}
	ok, err := h.App.AdminAuthService.VerifyHeader(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		closeUnreadManagerConfigBody(w, r)
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		closeUnreadManagerConfigBody(w, r)
		response.Error(w, http.StatusUnauthorized, errors.New("invalid admin key"))
		return
	}
	result, err := h.App.ManagerConfigService.ValidateCPAConnection(r.Context())
	if err != nil {
		response.Error(w, http.StatusBadGateway, err)
		return
	}
	if !result.Configured {
		response.Error(w, http.StatusConflict, errors.New("CPA connection is not configured"))
		return
	}
	response.JSON(w, http.StatusOK, result)
}
