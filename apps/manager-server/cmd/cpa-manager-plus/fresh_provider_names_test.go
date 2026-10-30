package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderNamesCanonicalSurface(t *testing.T) {
	writes := 0
	document := `{"api-keys":{"codex":[{"name":"old@example.com","keys":[{"api-key":"synthetic-provider-secret","websockets":true,"excluded-models":["*"]}]}]},"client-keys":["synthetic-client-secret"]}`
	fixture := newGatewayConfigFixture(t, document, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer management-key" {
			t.Error("wrong upstream credential")
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v8/management/config" {
			w.Header().Set("ETag", `"revision-1"`)
			_, _ = w.Write([]byte(document))
			return
		}
		if r.Method == http.MethodPut && r.URL.Path == "/v8/management/config/api-keys/codex" {
			writes++
			if r.Header.Get("If-Match") != `"revision-1"` {
				t.Error("missing guard")
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"name":"new@example.com"`) || !strings.Contains(string(body), `"websockets":true`) || !strings.Contains(string(body), `"excluded-models":["*"]`) {
				t.Error("wrong name-only mutation")
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	fixture.app.gatewayConfig.EnableManaged(server.URL)
	response := gatewayRequest(t, fixture.app, http.MethodGet, "providers/names", "", "")
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("unsafe read %d %s", response.Code, response.Body.String())
	}
	var view struct {
		Generation string `json:"generation"`
		ETag       string `json:"etag"`
	}
	if json.Unmarshal(response.Body.Bytes(), &view) != nil {
		t.Fatal("bad view")
	}
	// Marshal the opaque revision rather than embedding JSON quoting by hand.
	bodyBytes, _ := json.Marshal(map[string]any{"generation": view.Generation, "etag": view.ETag, "provider": "codex", "index": 0, "name": "new@example.com"})
	body := string(bodyBytes)
	response = gatewayRequest(t, fixture.app, http.MethodPut, "providers/names", body, "application/json")
	if response.Code != http.StatusOK || writes != 1 {
		t.Fatalf("save %d %s", response.Code, response.Body.String())
	}
	for _, invalid := range []string{strings.Replace(body, `"name":"new@example.com"`, `"name":null`, 1), strings.TrimSuffix(body, "}") + `,"api-key":"injected"}`, `{"generation":"7","etag":"a","provider":"codex","index":0,"name":"one","name":"two"}`} {
		response = gatewayRequest(t, fixture.app, http.MethodPut, "providers/names", invalid, "application/json")
		if response.Code != http.StatusBadRequest || writes != 1 {
			t.Fatalf("bad shape reached CPA: %d %s", response.Code, response.Body.String())
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		r := httptest.NewRequest(method, "/control/api/v2/providers/names", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer wrong")
		response = httptest.NewRecorder()
		fixture.app.canonicalAPI(response, r)
		if response.Code != http.StatusUnauthorized || writes != 1 {
			t.Fatal("unauthed access")
		}
	}
}
