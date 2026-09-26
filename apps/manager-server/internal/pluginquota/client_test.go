package pluginquota

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientProvidersNormalizesCatalogue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != providersPath {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer key" {
			t.Errorf("authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"providers":[
			{"plugin_id":"Example_Plugin","provider":"Example_Plugin","display_name":"Example quota",
			 "supported_providers":["Example_Plugin","other"],"supports_reset":true},
			{"plugin_id":"","provider":"","display_name":"nameless"},
			{"plugin_id":"second","display_name":"Second quota"}
		]}`))
	}))
	defer server.Close()

	providers, err := New(server.Client()).Providers(context.Background(), server.URL, "key")
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("providers = %#v", providers)
	}
	first := providers[0]
	if first.Provider != "example-plugin" || first.PluginID != "Example_Plugin" {
		t.Fatalf("first provider = %#v", first)
	}
	if first.PluginLocator() != "Example_Plugin" {
		t.Fatalf("plugin locator = %q", first.PluginLocator())
	}
	if len(first.SupportedProviders) != 2 || first.SupportedProviders[0] != "example-plugin" {
		t.Fatalf("supported providers = %#v", first.SupportedProviders)
	}
	if !first.SupportsReset || first.DisplayName != "Example quota" {
		t.Fatalf("first provider metadata = %#v", first)
	}
	if providers[1].Provider != "second" || len(providers[1].SupportedProviders) != 1 {
		t.Fatalf("second provider = %#v", providers[1])
	}
}

func TestClientCredentialQuotaPreservesValueShapes(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/plugins/example-plugin/quota" {
			t.Errorf("path = %q", r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"summary":[
			{"key":"credit_remaining","label":"剩余额度","value":1739.5,"unit":"credit","format":"number"},
			{"key":"daily_checkin","label":"今日可签到","value":0,"format":"boolean"},
			{"key":"balance","label":"Balance","value":5,"unit":"USD","format":"currency","currency":"usd"},
			{"key":"note","label":"Note","value":"not-a-number"},
			{"key":"","label":"Missing key","value":1}
		]}`))
	}))
	defer server.Close()

	items, err := New(server.Client()).CredentialQuota(
		context.Background(), server.URL, "key", "example-plugin", "auth index/1",
	)
	if err != nil {
		t.Fatalf("credential quota: %v", err)
	}
	if gotQuery != "auth_index=auth+index%2F1" {
		t.Fatalf("query = %q", gotQuery)
	}
	if len(items) != 4 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].Value.Number == nil || *items[0].Value.Number != 1739.5 {
		t.Fatalf("numeric reading = %#v", items[0].Value)
	}
	if items[1].Value.Number == nil || *items[1].Value.Number != 0 {
		t.Fatalf("boolean reading = %#v", items[1].Value)
	}
	if items[2].Currency != "USD" {
		t.Fatalf("currency = %q", items[2].Currency)
	}
	if items[3].Value.Number != nil || items[3].Value.Text != "not-a-number" {
		t.Fatalf("text reading = %#v", items[3].Value)
	}
}

func TestClientCredentialQuotaRequiresLocators(t *testing.T) {
	client := New(nil)
	if _, err := client.CredentialQuota(context.Background(), "http://127.0.0.1:1", "key", "", "index"); !errors.Is(err, ErrProviderRequired) {
		t.Fatalf("missing provider error = %v", err)
	}
	if _, err := client.CredentialQuota(context.Background(), "http://127.0.0.1:1", "key", "plugin", ""); !errors.Is(err, ErrAuthIndexRequired) {
		t.Fatalf("missing auth index error = %v", err)
	}
}

func TestClientCredentialQuotaSurfacesStatusAndBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream unavailable"}`))
	}))
	defer server.Close()

	_, err := New(server.Client()).CredentialQuota(context.Background(), server.URL, "key", "plugin", "index")
	if err == nil || !contains(err.Error(), "HTTP 502") || !contains(err.Error(), "upstream unavailable") {
		t.Fatalf("status error = %v", err)
	}
}

func TestClientFetchAllSendsOnlyProvidedAuthIndex(t *testing.T) {
	var body map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != fetchPath || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		body = map[string]string{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(server.Client())
	if err := client.FetchAll(context.Background(), server.URL, "key", "index-1"); err != nil {
		t.Fatalf("fetch all: %v", err)
	}
	if body["auth_index"] != "index-1" || len(body) != 1 {
		t.Fatalf("body = %#v", body)
	}
	if err := client.FetchAll(context.Background(), server.URL, "key", ""); err != nil {
		t.Fatalf("fetch all without index: %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("body without index = %#v", body)
	}
}

func TestClientResetCredentialQuotaUsesResetEndpoint(t *testing.T) {
	var path string
	var body map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := New(server.Client()).ResetCredentialQuota(
		context.Background(), server.URL, "key", "example-plugin", "index-1",
	); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if path != "/v0/management/plugins/example-plugin/quota/reset" {
		t.Fatalf("path = %q", path)
	}
	if body["auth_index"] != "index-1" {
		t.Fatalf("body = %#v", body)
	}
}

func TestSupportedCredentialProvidersKeepsFirstBinding(t *testing.T) {
	providers := []Provider{
		{Provider: "first-plugin", SupportedProviders: []string{"shared"}},
		{Provider: "second-plugin", SupportedProviders: []string{"shared", "own"}},
	}
	supported := SupportedCredentialProviders(providers)
	if supported["shared"] != "first-plugin" {
		t.Fatalf("shared binding = %q", supported["shared"])
	}
	if supported["own"] != "second-plugin" {
		t.Fatalf("own binding = %q", supported["own"])
	}
	if names := ProviderNames(providers); len(names) != 2 || names[1] != "second-plugin" {
		t.Fatalf("provider names = %#v", names)
	}
}

func TestReadingUnmarshalRejectsInvalidNumbers(t *testing.T) {
	var reading Reading
	if err := json.Unmarshal([]byte(`null`), &reading); err != nil {
		t.Fatalf("null reading: %v", err)
	}
	if reading.Number != nil || reading.Text != "" {
		t.Fatalf("null reading = %#v", reading)
	}
	if err := json.Unmarshal([]byte(`true`), &reading); err != nil {
		t.Fatalf("boolean reading: %v", err)
	}
	if reading.Text != "true" {
		t.Fatalf("boolean reading = %#v", reading)
	}
}

func TestClientUsesConfiguredTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"providers":[]}`))
	}))
	defer server.Close()

	client := New(server.Client(), 20*time.Millisecond)
	if _, err := client.Providers(context.Background(), server.URL, "key"); err == nil {
		t.Fatal("expected timeout error")
	}
}

func contains(value, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}
