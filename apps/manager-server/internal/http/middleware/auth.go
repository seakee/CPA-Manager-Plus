package middleware

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
)

type AdminVerifier interface {
	VerifyHeader(ctx context.Context, authorizationHeader string) (bool, error)
}

type PanelVerifier interface {
	VerifyPanelHeader(ctx context.Context, authorizationHeader string) (bool, error)
	PanelUsesExternalManagementKey(ctx context.Context) (bool, error)
}

func AuthorizeAdmin(w http.ResponseWriter, r *http.Request, verifier AdminVerifier) bool {
	ok, err := verifier.VerifyHeader(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		writeAuthorizationFailure(w, "admin", err)
		return false
	}
	if ok {
		return true
	}
	response.Error(w, http.StatusUnauthorized, errors.New("invalid admin key"))
	return false
}

func AuthorizePanel(w http.ResponseWriter, r *http.Request, verifier PanelVerifier) bool {
	ok, err := verifier.VerifyPanelHeader(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		writeAuthorizationFailure(w, "panel", err)
		return false
	}
	if ok {
		return true
	}
	external, err := verifier.PanelUsesExternalManagementKey(r.Context())
	if err != nil {
		writeAuthorizationFailure(w, "panel mode", err)
		return false
	}
	if external {
		response.Error(w, http.StatusUnauthorized, errors.New("invalid management key"))
		return false
	}
	response.Error(w, http.StatusUnauthorized, errors.New("invalid admin key"))
	return false
}

func writeAuthorizationFailure(w http.ResponseWriter, scope string, err error) {
	log.Printf("%s authorization failed: %v", scope, err)
	response.JSON(w, http.StatusInternalServerError, map[string]any{
		"error": "authorization failed",
		"code":  "request_failed",
	})
}
