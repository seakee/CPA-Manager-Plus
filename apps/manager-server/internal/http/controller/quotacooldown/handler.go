package quotacooldown

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

type Handler struct {
	App *app.Context
}

// cooldownItem is the minimal, read-only view of an active quota cooldown that
// the panel needs to render a derived hint on the auth file card. The account
// snapshot is included only as the credential discriminator required to avoid
// assigning one shared-file cooldown to a sibling account.
type cooldownItem struct {
	AuthFileName    string                       `json:"authFileName"`
	AuthIndex       string                       `json:"authIndex"`
	AccountSnapshot string                       `json:"accountSnapshot,omitempty"`
	Provider        string                       `json:"provider"`
	Owner           string                       `json:"owner"`
	ReasonCode      string                       `json:"reasonCode,omitempty"`
	WindowKind      string                       `json:"windowKind,omitempty"`
	Evidence        *usage.ProviderUsageMetadata `json:"evidence,omitempty"`
	RecoverAtMs     int64                        `json:"recoverAtMs"`
	DisabledAtMs    int64                        `json:"disabledAtMs"`
	CreatedAtMs     int64                        `json:"createdAtMs"`
}

type listResponse struct {
	Items []cooldownItem `json:"items"`
}

// Handle exposes the currently active quota cooldowns so the panel can show a
// derived "CPAMP cooldown in progress" hint next to the affected auth files.
// It is read-only and never modifies cooldown ownership or the native CPA
// disabled state.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	path := strings.TrimRight(r.URL.Path, "/")
	if path != "/usage-service/quota-cooldowns" &&
		path != "/usage-service/quota-cooldowns/recover" {
		response.MethodNotAllowed(w)
		return
	}
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}

	switch path {
	case "/usage-service/quota-cooldowns":
		if r.Method != http.MethodGet {
			response.MethodNotAllowed(w)
			return
		}
		h.handleList(w, r)
	case "/usage-service/quota-cooldowns/recover":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		h.handleRecover(w, r)
	}
}

func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	cooldowns, err := h.App.Store.QuotaCooldowns.ListActive(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	items := make([]cooldownItem, 0, len(cooldowns))
	for _, c := range cooldowns {
		items = append(items, mapCooldown(c))
	}
	response.JSON(w, http.StatusOK, listResponse{Items: items})
}

type recoverRequest struct {
	AuthFileName string `json:"authFileName"`
	AuthIndex    string `json:"authIndex"`
	Provider     string `json:"provider"`
}

type recoverResponse struct {
	Recovered bool `json:"recovered"`
}

func (h *Handler) handleRecover(w http.ResponseWriter, r *http.Request) {
	var req recoverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	req.AuthFileName = strings.TrimSpace(req.AuthFileName)
	req.AuthIndex = strings.TrimSpace(req.AuthIndex)
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	if req.AuthFileName == "" || req.AuthIndex == "" || req.Provider != "codex" {
		response.JSON(w, http.StatusBadRequest, map[string]string{
			"error": "authFileName, authIndex, and provider=codex are required",
		})
		return
	}
	if h.App.QuotaCooldownRecoveryService == nil {
		response.JSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "quota cooldown recovery service is unavailable",
		})
		return
	}
	recovered, err := h.App.QuotaCooldownRecoveryService.RecoverOwnedCooldown(
		r.Context(),
		req.AuthFileName,
		req.AuthIndex,
		req.Provider,
	)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	response.JSON(w, http.StatusOK, recoverResponse{Recovered: recovered})
}
func mapCooldown(c model.QuotaCooldown) cooldownItem {
	return cooldownItem{
		AuthFileName:    c.AuthFileName,
		AuthIndex:       c.AuthIndex,
		AccountSnapshot: c.AccountSnapshot,
		Provider:        c.Provider,
		Owner:           c.Owner,
		ReasonCode:      c.ReasonCode,
		WindowKind:      c.WindowKind,
		Evidence:        parseCooldownEvidence(c.EvidenceJSON, c.RecoverAtMS),
		RecoverAtMs:     c.RecoverAtMS,
		DisabledAtMs:    c.DisabledAtMS,
		CreatedAtMs:     c.CreatedAtMS,
	}
}

func parseCooldownEvidence(raw string, recoverAtMS int64) *usage.ProviderUsageMetadata {
	if !json.Valid([]byte(raw)) {
		return nil
	}
	var parsed usage.ProviderUsageMetadata
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	evidence := usage.NormalizeProviderUsageMetadata(&parsed)
	if evidence == nil || evidence.Provider != "xai" || evidence.Code != usage.ProviderUsageCodeXAIFree {
		return nil
	}
	if evidence.Kind != "" && evidence.Kind != usage.ProviderUsageKindIncludedFree {
		return nil
	}
	if evidence.State != "" && evidence.State != usage.ProviderUsageStateExhausted {
		return nil
	}
	evidence.Kind = usage.ProviderUsageKindIncludedFree
	evidence.State = usage.ProviderUsageStateExhausted
	if evidence.Unit != "tokens" {
		evidence.Unit = ""
	}
	if evidence.WindowKind != usage.ProviderUsageWindowRolling24H {
		evidence.WindowKind = ""
	}
	if evidence.Source != usage.ProviderUsageSourceBody {
		evidence.Source = ""
	}
	if evidence.RecoverAtMS != recoverAtMS {
		evidence.RecoverAtMS = 0
		evidence.RecoverAtEstimated = false
	}
	return evidence
}
