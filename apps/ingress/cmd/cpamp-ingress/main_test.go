package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := loadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != defaultAddr || cfg.managerURL.String() != defaultManagerURL || cfg.gatewayURL.String() != defaultGatewayURL {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestParseUpstreamRejectsAuthorityAndPathAmbiguity(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"https://manager:18317",
		"http://user:pass@manager:18317",
		"http://manager:18317/control",
		"http://manager:18317?route=control",
	} {
		if _, err := parseUpstream("TEST_URL", value, ""); err == nil {
			t.Fatalf("parseUpstream(%q) succeeded", value)
		}
	}
}

func TestLegacyGatewayRequiresExplicitManagerAuthority(t *testing.T) {
	var status atomic.Int32
	var calls atomic.Int32
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/internal/routing/fresh-authority" || r.Method != http.MethodGet {
			t.Errorf("unexpected authority request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(int(status.Load()))
		if status.Load() == http.StatusOK {
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer manager.Close()
	u, _ := url.Parse(manager.URL)
	allowed := managerLegacyGateway(managerFreshAuthority(u))
	for _, code := range []int{http.StatusNotFound, http.StatusServiceUnavailable, http.StatusOK, http.StatusForbidden, http.StatusFound} {
		status.Store(int32(code))
		if allowed(t.Context()) {
			t.Fatal("non-legacy response opened Gateway", code)
		}
	}
	status.Store(http.StatusNoContent)
	if !allowed(t.Context()) {
		t.Fatal("explicit legacy Manager was not accepted")
	}
	before := calls.Load()
	manager.Close()
	if !allowed(context.Background()) || calls.Load() != before {
		t.Fatal("legacy Manager outage broke confirmed mode")
	}
	// A new Ingress process has no legacy acknowledgement to reuse.
	if managerLegacyGateway(managerFreshAuthority(u))(t.Context()) {
		t.Fatal("restart inherited stale legacy mode")
	}
}
