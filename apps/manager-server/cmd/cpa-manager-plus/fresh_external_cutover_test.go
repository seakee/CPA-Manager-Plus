package main

import (
	"net/http"
	"strings"
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
