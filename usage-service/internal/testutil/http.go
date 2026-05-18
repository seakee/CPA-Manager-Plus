package testutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func Request(t testing.TB, handler http.Handler, method string, target string, body string, managementKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if managementKey != "" {
		req.Header.Set("Authorization", "Bearer "+managementKey)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func DecodeJSON(t testing.TB, rr *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response body %q: %v", rr.Body.String(), err)
	}
}

func RequireStatus(t testing.TB, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("status = %d, want %d, body = %s", rr.Code, want, rr.Body.String())
	}
}
