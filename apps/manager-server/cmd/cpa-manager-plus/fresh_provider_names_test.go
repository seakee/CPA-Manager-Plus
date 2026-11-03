package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
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
	for _, invalid := range []string{strings.Replace(body, `"provider":"codex"`, `"provider":"openai-compatibility"`, 1), strings.Replace(body, `"name":"new@example.com"`, `"name":null`, 1), strings.TrimSuffix(body, "}") + `,"api-key":"injected"}`, `{"generation":"7","etag":"a","provider":"codex","index":0,"name":"one","name":"two"}`} {
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

// After admission succeeds, the actual config request may still be rejected.
// Its refusal must fence both subsequent reads and writes, scoped to the exact
// connection/credential envelope which met it, without exposing upstream bodies.
func TestProviderNamesExternalRefusalStopsReadsAndWrites(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			t.Run(method+http.StatusText(status), func(t *testing.T) {
				app, fake := externalAuthorityApp(t, "")
				app.gatewayConfig = gatewayFor(app)
				original := fake.server.Config.Handler
				var denied atomic.Bool
				var requests atomic.Int32
				var expectedKey atomic.Value
				expectedKey.Store(externalAuthorityManagement)
				fake.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.URL.Path == "/v8/management/config" || r.URL.Path == "/v8/management/config/api-keys/codex" {
						if r.Header.Get("Authorization") != "Bearer "+expectedKey.Load().(string) {
							t.Error("wrong protected key")
						}
						if denied.Load() && r.Method == method {
							w.WriteHeader(status)
							_, _ = w.Write([]byte(`{"error":"synthetic-secret-body"}`))
							return
						}
						w.Header().Set("ETag", `"r1"`)
						_, _ = w.Write([]byte(`{"api-keys":{"codex":[{"name":"before"}]}}`))
						return
					}
					original.ServeHTTP(w, r)
				})
				initial := gatewayRequest(t, app, http.MethodGet, "providers/names", "", "")
				if initial.Code != http.StatusOK {
					t.Fatalf("initial read %d %s", initial.Code, initial.Body.String())
				}
				var view struct {
					Generation string `json:"generation"`
					ETag       string `json:"etag"`
				}
				if err := json.Unmarshal(initial.Body.Bytes(), &view); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(map[string]any{"generation": view.Generation, "etag": view.ETag, "provider": "codex", "index": 0, "name": "after"})
				grant := app.externalReadGrant(t.Context())
				denied.Store(true)
				response := gatewayRequest(t, app, method, "providers/names", string(raw), "application/json")
				if response.Code != http.StatusConflict || errorCode(t, response) != freshconfig.ReasonManagementRejected || strings.Contains(response.Body.String(), "synthetic-secret") {
					t.Fatalf("refusal %d %s", response.Code, response.Body.String())
				}
				if !app.rejection.refused(t.Context(), grant.Identity, grant.Credential) {
					t.Fatal("refusal not filed against request envelope")
				}
				before := requests.Load()
				for _, retry := range []string{http.MethodGet, http.MethodPut} {
					response = gatewayRequest(t, app, retry, "providers/names", string(raw), "application/json")
					if response.Code != http.StatusConflict || errorCode(t, response) != freshconfig.ReasonManagementRejected {
						t.Fatalf("retry %d %s", response.Code, response.Body.String())
					}
				}
				if requests.Load() != before {
					t.Fatal("refused key presented again")
				}
				// Simulate a successful rebind replacing the protected envelope. The old
				// refusal must not be inherited by the new credential on this connection.
				record, err := app.store.Load(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				protected, err := app.protector.ProtectString("new-synthetic-management")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = app.store.(freshstore.ConnectionRebinder).RebindConnection(t.Context(), record.Operation.Identity, record.Operation.Revision, model.ManagerCPAConnectionConfig{CPABaseURL: record.Connection.CPABaseURL, ManagementKey: protected}); err != nil {
					t.Fatal(err)
				}
				if app.externalReadGrant(t.Context()).Refused {
					t.Fatal("rebound envelope inherited rejection")
				}
				denied.Store(false)
				expectedKey.Store("new-synthetic-management")
				response = gatewayRequest(t, app, http.MethodGet, "providers/names", "", "")
				if response.Code != http.StatusOK {
					t.Fatalf("rebound read %d %s", response.Code, response.Body.String())
				}
			})
		}
	}
}

func TestProviderNameSaveThenLegacyWritesPreserveNativeGroups(t *testing.T) {
	document, err := cpa.DecodeConfigDocument([]byte(`{"api-keys":{"codex":[{"name":"first","future":9007199254740993,"keys":[{"api-key":"synthetic-one","websockets":true},{"api-key":"synthetic-two","disabled":true}]},{"name":"second","keys":[{"api-key":"synthetic-three"}]}],"claude":[{"name":"other-family","unknown":true}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(document)
	fixture := newGatewayConfigFixture(t, `{}`, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"r1"`)
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(document)
			return
		}
		if r.Method != http.MethodPut || r.URL.Path != "/v8/management/config/api-keys/codex" || r.Header.Get("If-Match") != `"r1"` {
			t.Error("unexpected native write")
		}
		var groups []any
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&groups); err != nil {
			t.Error(err)
		}
		document["api-keys"].(map[string]any)["codex"] = groups
	}))
	defer server.Close()
	fixture.app.gatewayConfig.EnableManaged(server.URL)
	legacyWrites := 0
	fixture.app.legacy = guardFreshLegacyWrites(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyWrites++
		// A legacy writer reconstructs flat key groups; the guard must prevent this.
		document["api-keys"] = map[string]any{"codex": []any{map[string]any{"name": "codex-1"}}}
	}), nil)
	response := gatewayRequest(t, fixture.app, http.MethodGet, "providers/names", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("read %d", response.Code)
	}
	var view struct {
		Generation string `json:"generation"`
		ETag       string `json:"etag"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"generation": view.Generation, "etag": view.ETag, "provider": "codex", "index": 0, "name": "saved@example.com"})
	response = gatewayRequest(t, fixture.app, http.MethodPut, "providers/names", string(raw), "application/json")
	if response.Code != http.StatusOK {
		t.Fatalf("save %d %s", response.Code, response.Body.String())
	}
	saved, _ := json.Marshal(document)
	expected := strings.Replace(string(original), `"name":"first"`, `"name":"saved@example.com"`, 1)
	if string(saved) != expected {
		t.Fatal("native save changed grouping, flags, keys or unknown fields")
	}
	if !strings.Contains(string(saved), `"name":"saved@example.com"`) {
		t.Fatal("name was not saved")
	}
	for _, section := range []string{"codex-api-key", "claude-api-key", "gemini-api-key", "interactions-api-key", "xai-api-key", "vertex-api-key", "openai-compatibility", "meta-api-key"} {
		// Normal edit, enable/disable, batch health write and deletion, including
		// query-bearing and trailing-slash requests from stale clients.
		for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodPost, http.MethodDelete} {
			response = httptest.NewRecorder()
			fixture.app.legacy.ServeHTTP(response, httptest.NewRequest(method, "/v0/management/"+section+"/?index=0", strings.NewReader(`[]`)))
			if response.Code != http.StatusConflict || errorCode(t, response) != "legacy_provider_write_paused" {
				t.Fatalf("%s %s: %d %s", method, section, response.Code, response.Body.String())
			}
		}
	}
	// The supported canonical compatibility calls must hit the same guard too.
	for _, section := range []string{"codex-api-key", "claude-api-key", "gemini-api-key", "interactions-api-key", "xai-api-key", "vertex-api-key", "openai-compatibility"} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			response = gatewayRequest(t, fixture.app, method, "cpamp/v0/management/"+section+"?index=0", `[]`, "application/json")
			if response.Code != http.StatusConflict || errorCode(t, response) != "legacy_provider_write_paused" {
				t.Fatalf("canonical %s %s: %d %s", method, section, response.Code, response.Body.String())
			}
		}
	}
	after, _ := json.Marshal(document)
	if legacyWrites != 0 || string(saved) != string(after) {
		t.Fatal("legacy writes changed names, grouping, keys, flags or unknown fields")
	}
}
