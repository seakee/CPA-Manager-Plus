package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestLoopbackManagementDoesNotForwardBrowserIP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer management-key" {
			t.Error("missing management credential")
		}
		for _, key := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded"} {
			if r.Header.Get(key) != "" {
				t.Errorf("forwarded browser identity in %s", key)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSetup(context.Background(), store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"}); err != nil {
		t.Fatal(err)
	}
	service := New(managerconfig.New(config.Config{}, st, nil), st)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/api-keys", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.10")
	req.Header.Set("X-Real-IP", "198.51.100.10")
	req.Header.Set("Forwarded", "for=198.51.100.10")
	recorder := httptest.NewRecorder()
	service.ProxyManagement(recorder, req, func(w http.ResponseWriter, status int, err error) { http.Error(w, err.Error(), status) })
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestLoopbackCallerAuthPreservesBrowserIP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer caller-key" {
			t.Error("caller credential was replaced")
		}
		if got := r.Header.Get("X-Forwarded-For"); got != "198.51.100.10, 192.0.2.10" {
			t.Errorf("X-Forwarded-For = %q", got)
		}
		if r.Header.Get("X-Real-IP") != "198.51.100.10" || r.Header.Get("Forwarded") != "for=198.51.100.10" {
			t.Error("caller forwarding headers were removed")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSetup(context.Background(), store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"}); err != nil {
		t.Fatal(err)
	}
	service := New(managerconfig.New(config.Config{}, st, nil), st)
	req := httptest.NewRequest(http.MethodGet, "/v0/resource/plugins/example/status", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("Authorization", "Bearer caller-key")
	req.Header.Set("X-Forwarded-For", "198.51.100.10")
	req.Header.Set("X-Real-IP", "198.51.100.10")
	req.Header.Set("Forwarded", "for=198.51.100.10")
	recorder := httptest.NewRecorder()
	service.ProxyPluginResourceWithCallerAuth(recorder, req, func(w http.ResponseWriter, status int, err error) { http.Error(w, err.Error(), status) })
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
}
