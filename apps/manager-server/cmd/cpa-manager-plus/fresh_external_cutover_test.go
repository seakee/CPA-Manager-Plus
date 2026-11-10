package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
)

// TestExternalLegacyReplacedWritesAreRefusedLocally is the clean cutover: on
// External the legacy bridge may not be a second, unproven write path onto a CPA
// this Manager does not own. Each write Fresh already owns answers the stable code
// and forwards nothing.
func TestExternalLegacyReplacedWritesAreRefusedLocally(t *testing.T) {
	app, fake := newExternalProbeApp(t)
	for _, scenario := range []struct {
		method   string
		resource string
	}{
		{http.MethodPut, "cpamp/v0/management/api-keys"},
		{http.MethodPatch, "cpamp/v0/management/api-keys"},
		{http.MethodDelete, "cpamp/v0/management/api-keys"},
		{http.MethodPut, "cpamp/v0/management/config.yaml"},
		{http.MethodPut, "cpamp/config"},
		{http.MethodPut, "cpamp/usage-service/config"},
	} {
		t.Run(scenario.method+" "+scenario.resource, func(t *testing.T) {
			before := fake.requests.Load()
			response := gatewayRequest(t, app, scenario.method, scenario.resource, `{}`, "application/json")
			if response.Code != http.StatusConflict {
				t.Fatalf("%s = %d %s", scenario.resource, response.Code, response.Body.String())
			}
			if code := errorCode(t, response); code != externalLegacyWriteReplaced {
				t.Fatalf("%s code = %q", scenario.resource, code)
			}
			if sent := fake.requests.Load() - before; sent != 0 {
				t.Fatalf("%s forwarded %d requests to the user's CPA", scenario.resource, sent)
			}
		})
	}
}

// TestExternalLegacyReadsStayOpenBeforeARejection proves the cutover is scoped to
// the writes Fresh owns: the compatibility reads the Console still uses keep
// working, so the existing pages do not go dark.
func TestExternalLegacyReadsStayOpenBeforeARejection(t *testing.T) {
	app, _ := newExternalProbeApp(t)
	for _, resource := range []string{
		"cpamp/v0/management/api-keys",
		"cpamp/v0/management/config.yaml",
		"cpamp/config",
		"cpamp/status",
	} {
		response := gatewayRequest(t, app, http.MethodGet, resource, "", "")
		if response.Code != http.StatusOK {
			t.Fatalf("read %s = %d %s", resource, response.Code, response.Body.String())
		}
	}
}

// TestExternalLegacyWritesStayOpenOnNewManaged proves the cutover is External's
// alone: a New Managed installation still reaches CPA through the same bridge,
// because there the bridge is the Console's existing contract rather than a
// duplicate of a route Fresh owns.
func TestExternalLegacyWritesStayOpenOnNewManaged(t *testing.T) {
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if record.Operation.Strategy.Strategy == bootstrap.ConnectExternal {
		t.Fatalf("the fixture committed External: %q", record.Operation.Strategy.Strategy)
	}
	response := gatewayRequest(t, app, http.MethodPut, "cpamp/v0/management/api-keys", `{}`, "application/json")
	if code := errorCode(t, response); code == externalLegacyWriteReplaced {
		t.Fatalf("New Managed returned the External cutover code")
	}
	if code := errorCode(t, response); code == freshconfig.ReasonManagementRejected {
		t.Fatalf("New Managed returned the External rejection code")
	}
}

// TestExternalLegacyBridgeStopsPresentingARejectedKey is the rejection gate. Once
// the user's CPA refused the stored credential, every legacy request that would
// present it again answers the typed blocker locally: CPA bans the address after
// five failed authentications, so the bridge must not be one more retry loop.
func TestExternalLegacyBridgeStopsPresentingARejectedKey(t *testing.T) {
	app, fake := newExternalProbeApp(t)
	// The typed read is what learns the refusal; the bridge itself never presents
	// the key again once that is recorded.
	fake.management.Store(strings.Repeat("f", 64))
	first := gatewayRequest(t, app, http.MethodGet, "external/usage", "", "")
	if first.Code != http.StatusConflict || errorCode(t, first) != freshconfig.ReasonManagementRejected {
		t.Fatalf("first read = %d %s", first.Code, first.Body.String())
	}
	if !app.externalReadGrant(t.Context()).Refused {
		t.Fatal("the refusal was not recorded")
	}
	before := fake.requests.Load()
	for range 3 {
		for _, scenario := range []struct{ method, resource, body string }{
			{http.MethodGet, "cpamp/v0/management/api-keys", ""},
			{http.MethodGet, "cpamp/v0/management/config.yaml", ""},
			{http.MethodPut, "cpamp/v0/management/config", `{}`},
			{http.MethodDelete, "cpamp/v0/management/logs", ""},
		} {
			response := gatewayRequest(t, app, scenario.method, scenario.resource, scenario.body, "application/json")
			if response.Code != http.StatusConflict {
				t.Fatalf("%s %s after the rejection = %d %s", scenario.method, scenario.resource, response.Code, response.Body.String())
			}
			if code := errorCode(t, response); code != freshconfig.ReasonManagementRejected {
				t.Fatalf("%s %s code = %q", scenario.method, scenario.resource, code)
			}
		}
	}
	if sent := fake.requests.Load() - before; sent != 0 {
		t.Fatalf("the bridge presented the refused key %d more times", sent)
	}
	// The Manager-local families never present the credential, so a refusal must
	// not take them away.
	if response := gatewayRequest(t, app, http.MethodGet, "cpamp/status", "", ""); response.Code != http.StatusOK {
		t.Fatalf("Manager-local read after the rejection = %d %s", response.Code, response.Body.String())
	}
}

// TestExternalLegacyBridgeTurnsAStaleKeyIntoTheBlocker covers the first refusal.
// The CPA answers 401 to a management read the operator rotated out of band, and
// the browser reads any 401 from this namespace as its own Admin session having
// expired: it clears the session and returns to the login form before a page can
// name the cause. The bridge therefore answers the typed blocker, records the
// refusal, and never presents the stale key again.
func TestExternalLegacyBridgeTurnsAStaleKeyIntoTheBlocker(t *testing.T) {
	app, fake := newExternalProbeApp(t)
	fake.management.Store(strings.Repeat("e", 64))
	// No typed read ran first: the refusal is unknown when the bridge forwards.
	if app.externalReadGrant(t.Context()).Refused {
		t.Fatal("the refusal was recorded before any request")
	}
	response := gatewayRequest(t, app, http.MethodGet, "cpamp/v0/management/api-keys", "", "")
	if response.Code != http.StatusConflict {
		t.Fatalf("stale read = %d %s", response.Code, response.Body.String())
	}
	if code := errorCode(t, response); code != freshconfig.ReasonManagementRejected {
		t.Fatalf("stale read code = %q", code)
	}
	if !app.externalReadGrant(t.Context()).Refused {
		t.Fatal("the refusal was not recorded from the CPA's answer")
	}
	// A recorded refusal is local: the CPA is not asked a second time.
	before := fake.requests.Load()
	for range 3 {
		repeated := gatewayRequest(t, app, http.MethodGet, "cpamp/v0/management/api-keys", "", "")
		if repeated.Code != http.StatusConflict {
			t.Fatalf("repeated read = %d %s", repeated.Code, repeated.Body.String())
		}
	}
	if sent := fake.requests.Load() - before; sent != 0 {
		t.Fatalf("the bridge presented the stale key %d more times", sent)
	}
}

// TestExternalLegacyBridgeBoundsConcurrentRefusals covers the ban window. A
// console page opens several credential-presenting reads at once; the CPA bans an
// address after five failed authentications, so only the first may reach it and
// every concurrent one must answer the recorded refusal locally.
func TestExternalLegacyBridgeBoundsConcurrentRefusals(t *testing.T) {
	app, fake := newExternalProbeApp(t)
	fake.management.Store(strings.Repeat("d", 64))
	before := fake.requests.Load()
	var wg sync.WaitGroup
	responses := make([]int, 8)
	for i := range responses {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			responses[index] = gatewayRequest(t, app, http.MethodGet, "cpamp/v0/management/api-keys", "", "").Code
		}(i)
	}
	wg.Wait()
	if sent := fake.requests.Load() - before; sent != 1 {
		t.Fatalf("the bridge presented the stale key %d times", sent)
	}
	for index, code := range responses {
		if code != http.StatusConflict {
			t.Fatalf("request %d = %d, want the typed blocker", index, code)
		}
	}
}

// TestExternalLegacyBridgeGatesTheV8KeyGroupView proves the External cutover
// covers the one CPA v8 subtree Manager forwards — and only the shape that
// actually proxies. The gate must serialize a canonical GET while the credential
// works and answer the typed blocker locally once the user's CPA refused it, but
// it must not turn a write or a dirty path into a credential failure: those keep
// the router's own 405/404 answers.
func TestExternalLegacyBridgeGatesTheV8KeyGroupView(t *testing.T) {
	t.Run("admitted", func(t *testing.T) {
		app, fake := newExternalProbeApp(t)

		before := fake.requests.Load()
		admitted := gateRequest(t, app, http.MethodGet, "/v8/management/config/api-keys", gateAdmin)
		if admitted.Code != http.StatusOK {
			t.Fatalf("admitted read = %d %s", admitted.Code, admitted.Body.String())
		}
		if sent := fake.requests.Load() - before; sent != 1 {
			t.Fatalf("the admitted read reached the CPA %d times, want 1", sent)
		}

		for _, test := range []struct {
			name   string
			method string
			path   string
			auth   string
			want   int
		}{
			{name: "put", method: http.MethodPut, path: "/v8/management/config/api-keys", auth: gateAdmin, want: http.StatusMethodNotAllowed},
			{name: "options", method: http.MethodOptions, path: "/v8/management/config/api-keys", auth: gateAdmin, want: http.StatusMethodNotAllowed},
			{name: "encoded dot segments", method: http.MethodGet, path: "/v8/management/config/api-keys/%2e%2e/config", auth: gateAdmin, want: http.StatusNotFound},
			{name: "duplicate slash", method: http.MethodGet, path: "/v8/management/config/api-keys//claude", auth: gateAdmin, want: http.StatusNotFound},
			{name: "leading duplicate slash", method: http.MethodGet, path: "//v8/management/config/api-keys", auth: gateAdmin, want: http.StatusNotFound},
			{name: "sibling", method: http.MethodGet, path: "/v8/management/config/api-keys-x", auth: gateAdmin, want: http.StatusNotFound},
		} {
			t.Run(test.name, func(t *testing.T) {
				before := fake.requests.Load()
				response := gateRequest(t, app, test.method, test.path, test.auth)
				if response.Code != test.want {
					t.Fatalf("%s %s = %d %s", test.method, test.path, response.Code, response.Body.String())
				}
				if response.Header().Get("Location") != "" {
					t.Fatalf("%s %s redirected instead of refusing", test.method, test.path)
				}
				if sent := fake.requests.Load() - before; sent != 0 {
					t.Fatalf("%s %s reached the CPA %d times", test.method, test.path, sent)
				}
			})
		}
	})

	t.Run("rejected", func(t *testing.T) {
		app, fake := newExternalProbeApp(t)
		// A refusal learned through a typed read must stop this path too: CPA bans
		// the calling address after five failed authentications.
		fake.management.Store(strings.Repeat("f", 64))
		first := gatewayRequest(t, app, http.MethodGet, "external/usage", "", "")
		if first.Code != http.StatusConflict || errorCode(t, first) != freshconfig.ReasonManagementRejected {
			t.Fatalf("first read = %d %s", first.Code, first.Body.String())
		}

		for _, test := range []struct {
			name   string
			method string
			path   string
			auth   string
			want   int
			code   string
		}{
			{name: "canonical read", method: http.MethodGet, path: "/v8/management/config/api-keys/claude", auth: gateAdmin, want: http.StatusConflict, code: freshconfig.ReasonManagementRejected},
			{name: "bad admin key", method: http.MethodGet, path: "/v8/management/config/api-keys", auth: "Bearer wrong-key", want: http.StatusUnauthorized},
			{name: "put", method: http.MethodPut, path: "/v8/management/config/api-keys", auth: gateAdmin, want: http.StatusMethodNotAllowed},
			{name: "options", method: http.MethodOptions, path: "/v8/management/config/api-keys", auth: gateAdmin, want: http.StatusMethodNotAllowed},
			{name: "encoded dot segments", method: http.MethodGet, path: "/v8/management/config/api-keys/%2e%2e/config", auth: gateAdmin, want: http.StatusNotFound},
			{name: "duplicate slash", method: http.MethodGet, path: "/v8/management/config/api-keys//claude", auth: gateAdmin, want: http.StatusNotFound},
			{name: "leading duplicate slash", method: http.MethodGet, path: "//v8/management/config/api-keys", auth: gateAdmin, want: http.StatusNotFound},
			{name: "sibling", method: http.MethodGet, path: "/v8/management/config/api-keys-x", auth: gateAdmin, want: http.StatusNotFound},
			{name: "client keys", method: http.MethodGet, path: "/v8/management/config/access/api-keys", auth: gateAdmin, want: http.StatusNotFound},
		} {
			t.Run(test.name, func(t *testing.T) {
				before := fake.requests.Load()
				response := gateRequest(t, app, test.method, test.path, test.auth)
				if response.Code != test.want {
					t.Fatalf("%s %s = %d %s", test.method, test.path, response.Code, response.Body.String())
				}
				if test.code != "" {
					if code := errorCode(t, response); code != test.code {
						t.Fatalf("%s %s code = %q, want %q", test.method, test.path, code, test.code)
					}
				}
				if response.Header().Get("Location") != "" {
					t.Fatalf("%s %s redirected instead of refusing", test.method, test.path)
				}
				if sent := fake.requests.Load() - before; sent != 0 {
					t.Fatalf("%s %s reached the CPA %d times", test.method, test.path, sent)
				}
			})
		}
	})
}

// TestExternalKeyPreflightAnswersARefusedCredentialLocally extends the rejection
// gate to the key drawer. The preflight is a management read like any other, so on
// External it must not become the retry loop the gate exists to prevent: once the
// user's CPA refused the stored credential, the read-only answer names the rebind
// where the operator is choosing, and the CPA is not asked again.
func TestExternalKeyPreflightAnswersARefusedCredentialLocally(t *testing.T) {
	app, fake := newExternalProbeApp(t)
	fake.management.Store(strings.Repeat("f", 64))
	first := gatewayRequest(t, app, http.MethodGet, "external/usage", "", "")
	if first.Code != http.StatusConflict || errorCode(t, first) != freshconfig.ReasonManagementRejected {
		t.Fatalf("first read = %d %s", first.Code, first.Body.String())
	}
	before := fake.requests.Load()
	for range 3 {
		response := gatewayRequest(t, app, http.MethodGet,
			"api-keys/"+strings.Repeat("a", 32)+"/revocation/"+strings.Repeat("b", 32)+"/replace", "", "")
		if response.Code != http.StatusOK {
			t.Fatalf("preflight = %d %s", response.Code, response.Body.String())
		}
		var reply struct {
			Ready  bool   `json:"ready"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
			t.Fatalf("preflight body = %s (%v)", response.Body.String(), err)
		}
		if reply.Ready || reply.Reason != freshconfig.ReasonManagementRejected {
			t.Fatalf("preflight after the refusal = %v %q", reply.Ready, reply.Reason)
		}
	}
	if sent := fake.requests.Load() - before; sent != 0 {
		t.Fatalf("the preflight presented the refused key %d more times", sent)
	}
}
