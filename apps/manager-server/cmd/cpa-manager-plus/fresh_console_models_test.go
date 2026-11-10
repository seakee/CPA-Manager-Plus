package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
)

const (
	consoleModelsManagementKey = "Cpamp_Console_Models_Management_Key_417283"
	consoleModelsClientKey     = "Cpamp_Console_Models_Client_Key_918273"
)

// consoleModelsCPA is the CPA of one installation: it owns a management
// credential, the client keys it accepts, and it records the authorization of
// every request it is asked to make a decision about.
type consoleModelsCPA struct {
	server *httptest.Server
	// listStatus is the answer the gateway gives the model list. A CPA that
	// refuses the client key answers 401 here.
	listStatus int
	clientKeys []string

	mu    sync.Mutex
	calls []consoleModelsCPACall
}

type consoleModelsCPACall struct {
	path          string
	authorization string
}

func newConsoleModelsCPA(t *testing.T, clientKeys ...string) *consoleModelsCPA {
	t.Helper()
	fake := &consoleModelsCPA{listStatus: http.StatusOK, clientKeys: clientKeys}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(fake.server.Close)
	return fake
}

func (c *consoleModelsCPA) handle(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.calls = append(c.calls, consoleModelsCPACall{path: r.URL.Path, authorization: r.Header.Get("Authorization")})
	c.mu.Unlock()
	switch r.URL.Path {
	case "/v8/management/config":
		if r.Header.Get("Authorization") != "Bearer "+consoleModelsManagementKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		keys, err := json.Marshal(c.clientKeys)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access":{"api-keys":` + string(keys) + `},"providers":{}}`))
	case "/v1/models":
		if c.listStatus != http.StatusOK {
			w.WriteHeader(c.listStatus)
			_, _ = w.Write([]byte(`{"error":"Invalid API key"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5"},{"id":"claude-sonnet"}]}`))
	default:
		http.NotFound(w, r)
	}
}

func (c *consoleModelsCPA) recorded() []consoleModelsCPACall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]consoleModelsCPACall(nil), c.calls...)
}

// consoleModelsApplication is the completed installation whose committed
// destination is fake.
func consoleModelsApplication(t *testing.T, fake *consoleModelsCPA) *freshApplication {
	t.Helper()
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
	app.managedSource = func(context.Context) (freshkeyapply.ManagedInstall, error) {
		return freshkeyapply.ManagedInstall{
			BaseURL:       fake.server.URL,
			ManagementKey: consoleModelsManagementKey,
		}, nil
	}
	return app
}

// The console's model list is read with a client key the CPA already holds: the
// Admin credential the console authenticated with never reaches the CPA, and the
// key that does reach it is never returned to the browser.
func TestFreshConsoleModelListUsesCPAHeldClientKey(t *testing.T) {
	fake := newConsoleModelsCPA(t, consoleModelsClientKey)
	app := consoleModelsApplication(t, fake)
	for _, resource := range []string{"cpamp/v1/models", "cpamp/models"} {
		t.Run(resource, func(t *testing.T) {
			response := installationAPI(t, app, http.MethodGet, resource, strategyTestAdmin)
			if response.Code != http.StatusOK {
				t.Fatalf("model list returned %d: %s", response.Code, response.Body.String())
			}
			var payload struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("model list is not JSON: %v", err)
			}
			if len(payload.Data) != 2 || payload.Data[0].ID != "gpt-5" {
				t.Fatalf("model list = %s, want the CPA's own list", response.Body.String())
			}
			if strings.Contains(response.Body.String(), consoleModelsClientKey) {
				t.Fatal("model list returned the client key it was read with")
			}
		})
	}

	listAuthorization := ""
	for _, call := range fake.recorded() {
		if call.authorization == "Bearer "+strategyTestAdmin {
			t.Fatalf("CPA received the console's Admin credential on %s", call.path)
		}
		if call.path == "/v1/models" {
			listAuthorization = call.authorization
		}
	}
	if listAuthorization != "Bearer "+consoleModelsClientKey {
		t.Fatalf("model list read authorization = %q, want the CPA-held client key", listAuthorization)
	}
}

// A CPA that holds no client key has nothing this console may present. The
// answer is the empty list the System page already renders, not a 401 the user
// could never fix from there.
func TestFreshConsoleModelListIsEmptyWithoutCPAClientKey(t *testing.T) {
	fake := newConsoleModelsCPA(t)
	app := consoleModelsApplication(t, fake)
	response := installationAPI(t, app, http.MethodGet, "cpamp/v1/models", strategyTestAdmin)
	if response.Code != http.StatusOK {
		t.Fatalf("model list returned %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"data":[]`) {
		t.Fatalf("model list = %s, want an empty list", response.Body.String())
	}
	for _, call := range fake.recorded() {
		if call.path == "/v1/models" {
			t.Fatalf("empty installation still asked CPA for its model list with %q", call.authorization)
		}
	}
}

// A CPA that refuses the key this Manager holds is an upstream failure. Relaying
// its 401 would tell the browser its console session expired, which is false.
func TestFreshConsoleModelListDoesNotRelayCPAAuthenticationFailure(t *testing.T) {
	fake := newConsoleModelsCPA(t, consoleModelsClientKey)
	fake.listStatus = http.StatusUnauthorized
	app := consoleModelsApplication(t, fake)
	response := installationAPI(t, app, http.MethodGet, "cpamp/v1/models", strategyTestAdmin)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("model list returned %d: %s, want 502", response.Code, response.Body.String())
	}
}

// The console route is admin-protected, and the CPA is not consulted at all
// before that check passes.
func TestFreshConsoleModelListRejectsUnauthenticatedCaller(t *testing.T) {
	fake := newConsoleModelsCPA(t, consoleModelsClientKey)
	app := consoleModelsApplication(t, fake)
	for _, token := range []string{"", "not-the-admin-key"} {
		response := installationAPI(t, app, http.MethodGet, "cpamp/v1/models", token)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("token %q reached the model list: %d %s", token, response.Code, response.Body.String())
		}
	}
	if calls := fake.recorded(); len(calls) != 0 {
		t.Fatalf("unauthenticated model list reached the CPA: %#v", calls)
	}
}

// Without a committed installation authority there is no CPA this Manager may
// read, and the answer says so instead of presenting a credential anywhere.
func TestFreshConsoleModelListReportsUnavailableWithoutCPAAuthority(t *testing.T) {
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
	response := installationAPI(t, app, http.MethodGet, "cpamp/v1/models", strategyTestAdmin)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("model list returned %d: %s, want 503", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), consoleModelListUnavailable) {
		t.Fatalf("model list = %s, want the %q code", response.Body.String(), consoleModelListUnavailable)
	}
}

func TestFirstCPAClientKeyProjectsOnlyGatewayClientList(t *testing.T) {
	for _, test := range []struct {
		name     string
		document map[string]any
		want     string
	}{
		{name: "absent access", document: map[string]any{}},
		{name: "absent list", document: map[string]any{"access": map[string]any{}}},
		{name: "not a list", document: map[string]any{"access": map[string]any{"api-keys": "key"}}},
		{name: "empty list", document: map[string]any{"access": map[string]any{"api-keys": []any{}}}},
		{name: "blank entry", document: map[string]any{"access": map[string]any{"api-keys": []any{"  "}}}},
		{
			name: "control character entry",
			document: map[string]any{"access": map[string]any{
				"api-keys": []any{"key\nInjected: 1"},
			}},
		},
		{
			name: "first usable entry",
			document: map[string]any{"access": map[string]any{
				"api-keys": []any{7, " ", "key-one", "key-two"},
			}},
			want: "key-one",
		},
		{
			name: "provider secrets stay behind",
			document: map[string]any{
				"access":    map[string]any{"api-keys": []any{"key-one"}},
				"providers": map[string]any{"openai": map[string]any{"api-key": "provider-secret"}},
			},
			want: "key-one",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := firstCPAClientKey(test.document); got != test.want {
				t.Fatalf("firstCPAClientKey() = %q, want %q", got, test.want)
			}
		})
	}
}
