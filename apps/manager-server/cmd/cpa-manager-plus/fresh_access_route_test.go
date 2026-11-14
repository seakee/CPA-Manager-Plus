package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The two checks below are the historical half of the dual-prefix contract:
// an already-committed installation created before the inference prefix
// existed keeps its authenticated Control recovery entry and never acquires a
// public inference root, while a current dual-prefix install publishes both.
func TestLegacyInstallationAccessInfoRequiresAuthenticationAndNoRootFallback(t *testing.T) {
	app := newInstallationTestApplication(t)
	for _, credential := range []string{"", "not-the-admin"} {
		response := installationAPI(t, app, http.MethodGet, "api-keys/access-info", credential)
		if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "/cpamp-admin") || strings.Contains(response.Body.String(), "protocols") {
			t.Fatalf("unauthenticated caller discovered access information: status=%d", response.Code)
		}
	}
	response := installationAPI(t, app, http.MethodGet, "api-keys/access-info", strategyTestAdmin)
	if response.Code != http.StatusOK {
		t.Fatalf("old installation lost authenticated recovery metadata: status=%d", response.Code)
	}
	var info keyAccessInfo
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Ready || len(info.Protocols) != 0 || info.Code != "inference_prefix_required" || info.ManagementBasePath != "/cpamp-admin" || info.InferenceBasePath != "" {
		t.Fatalf("old installation advertised a usable root fallback: %+v", info)
	}
}

func TestCurrentInstallationAccessInfoPublishesBothCommittedPrefixes(t *testing.T) {
	app := newManagedInstallHarness(t).app
	installed, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	control, inference := installed.Operation.Routing.Target, installed.Operation.Routing.InferenceTarget
	response := installationAPI(t, app, http.MethodGet, "api-keys/access-info", strategyTestAdmin)
	if response.Code != http.StatusOK {
		t.Fatalf("committed dual-prefix install lost access information: status=%d", response.Code)
	}
	var info keyAccessInfo
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if !info.Ready || info.Code != "" || info.ManagementBasePath != control || info.InferenceBasePath != inference {
		t.Fatalf("committed dual-prefix install did not publish install facts: %+v", info)
	}
	if len(info.Protocols) == 0 {
		t.Fatal("committed dual-prefix install published no inference protocol")
	}
	for _, protocol := range info.Protocols {
		if !strings.HasPrefix(protocol.BasePath, inference) || !strings.HasPrefix(protocol.ExamplePath, inference) {
			t.Fatalf("protocol %s escaped the committed inference prefix: %+v", protocol.ID, protocol)
		}
	}
	if body := response.Body.String(); strings.Contains(body, strategyTestAdmin) {
		t.Fatal("access information leaked the admin credential")
	}
}
