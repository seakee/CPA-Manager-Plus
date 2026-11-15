package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

func TestMissingInferencePrefixNeverPublishesRootAPI(t *testing.T) {
	// Old completed installations have a Control route but no inference field.
	// Formatting that field as an empty string must not advertise /v1 as a
	// working fallback, even when the caller has an old completion checkpoint.
	for _, missing := range []string{"", "/", "/../gateway", "/gateway%2Fprivate"} {
		t.Run(missing, func(t *testing.T) {
			value := makeKeyAccessInfo(bootstrap.NewManaged, currentInstallControlPrefix, missing, true, "")
			if value.Ready || len(value.Protocols) != 0 || value.Code != "inference_prefix_required" {
				t.Fatalf("unsafe inference path advertised: ready=%v protocols=%d code=%s", value.Ready, len(value.Protocols), value.Code)
			}
		})
	}
}

func TestExternalAccessInfoNeverPublishesURLCredentials(t *testing.T) {
	for _, external := range []string{
		"https://operator:management-secret@external.example/custom",
		"https://external.example/custom?key=management-secret",
		"https://external.example/custom#management-secret",
	} {
		value := makeKeyAccessInfo(bootstrap.ConnectExternal, currentInstallControlPrefix, "", true, external)
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "management-secret") || value.Ready || len(value.Protocols) != 0 || value.ExternalBaseURL != "" {
			t.Fatal("invalid external connection exposed sensitive address or usable protocol endpoints")
		}
	}
}
