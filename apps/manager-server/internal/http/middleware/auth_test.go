package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthorizeAdminSanitizesVerifierErrors(t *testing.T) {
	const internalDetail = "/private/usage.sqlite: admin credential query failed"
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v0/management/usage/maintenance", nil)
	if AuthorizeAdmin(recorder, request, failingAdminVerifier{err: errors.New(internalDetail)}) {
		t.Fatal("failed admin verification unexpectedly authorized")
	}
	if recorder.Code != http.StatusInternalServerError ||
		!strings.Contains(recorder.Body.String(), `"error":"authorization failed"`) ||
		!strings.Contains(recorder.Body.String(), `"code":"request_failed"`) ||
		strings.Contains(recorder.Body.String(), internalDetail) {
		t.Fatalf("admin authorization response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestAuthorizePanelSanitizesVerifierErrors(t *testing.T) {
	const internalDetail = "/private/usage.sqlite: panel auth query failed"
	for _, test := range []struct {
		name     string
		verifier failingPanelVerifier
	}{
		{name: "verify", verifier: failingPanelVerifier{verifyErr: errors.New(internalDetail)}},
		{name: "mode", verifier: failingPanelVerifier{externalErr: errors.New(internalDetail)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)
			if AuthorizePanel(recorder, request, test.verifier) {
				t.Fatal("failed panel verification unexpectedly authorized")
			}
			if recorder.Code != http.StatusInternalServerError ||
				!strings.Contains(recorder.Body.String(), `"error":"authorization failed"`) ||
				!strings.Contains(recorder.Body.String(), `"code":"request_failed"`) ||
				strings.Contains(recorder.Body.String(), internalDetail) {
				t.Fatalf("panel authorization response = %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

type failingAdminVerifier struct {
	err error
}

func (v failingAdminVerifier) VerifyHeader(context.Context, string) (bool, error) {
	return false, v.err
}

type failingPanelVerifier struct {
	verifyErr   error
	externalErr error
}

func (v failingPanelVerifier) VerifyPanelHeader(context.Context, string) (bool, error) {
	return false, v.verifyErr
}

func (v failingPanelVerifier) PanelUsesExternalManagementKey(context.Context) (bool, error) {
	return false, v.externalErr
}
