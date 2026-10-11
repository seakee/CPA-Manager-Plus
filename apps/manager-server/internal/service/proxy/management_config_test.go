package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestIsCPAManagementConfigPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/v8/management/config", true},
		{"/v8/management/config/", true},
		{"/v8/management/config/plugins/configs/demo/pool/strategy", true},
		{"/v8/management/configx", false},
		{"/v8/management", false},
		{"/v0/management/plugins/demo/accounts", false},
	} {
		if got := IsCPAManagementConfigPath(tc.path); got != tc.want {
			t.Fatalf("IsCPAManagementConfigPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// 覆盖转发语义：管理入口用已保存的 CPA 密钥；调用者入口原样保留其 Authorization；
// 空密钥绝不替换为已保存密钥；标量请求体不被改写；非配置路径不转发。
func TestProxyManagementConfigKeySelectionAndBody(t *testing.T) {
	var seenAuth []string
	var seenBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		seenBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(upstream.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SaveSetup(context.Background(), store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "saved-key"}); err != nil {
		t.Fatalf("save setup: %v", err)
	}
	service := New(managerconfig.New(config.Config{}, st, nil), st)
	writeError := func(w http.ResponseWriter, status int, err error) { http.Error(w, err.Error(), status) }
	const configPath = "/v8/management/config/plugins/configs/demo/pool/strategy"

	run := func(t *testing.T, method, path, auth, body string, callerAuth bool) *httptest.ResponseRecorder {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		if callerAuth {
			service.ProxyManagementConfigWithCallerAuth(rec, req, writeError)
		} else {
			service.ProxyManagementConfig(rec, req, writeError)
		}
		return rec
	}
	lastAuth := func() string { return seenAuth[len(seenAuth)-1] }

	if rec := run(t, http.MethodPut, configPath, "Bearer panel-admin", `"headroom-first"`, false); rec.Code != http.StatusOK {
		t.Fatalf("admin path status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := lastAuth(); got != "Bearer saved-key" {
		t.Fatalf("admin path forwarded %q, want the saved CPA key", got)
	}
	if string(seenBody) != `"headroom-first"` {
		t.Fatalf("scalar body was rewritten: %q", seenBody)
	}

	if rec := run(t, http.MethodPatch, configPath, "Bearer caller-key", `{"strategy":"round-robin"}`, true); rec.Code != http.StatusOK {
		t.Fatalf("caller path status = %d", rec.Code)
	}
	if got := lastAuth(); got != "Bearer caller-key" {
		t.Fatalf("caller path forwarded %q, want the caller token unchanged", got)
	}

	if rec := run(t, http.MethodPut, configPath, "", `"round-robin"`, true); rec.Code != http.StatusOK {
		t.Fatalf("anonymous caller path status = %d", rec.Code)
	}
	if got := lastAuth(); got != "" {
		t.Fatalf("anonymous caller was given %q; the saved key must never be substituted", got)
	}

	forwarded := len(seenAuth)
	if rec := run(t, http.MethodGet, "/v0/management/plugins/demo/accounts", "Bearer caller-key", "", true); rec.Code != http.StatusNotFound || len(seenAuth) != forwarded {
		t.Fatalf("non-config path: status=%d newForwards=%d", rec.Code, len(seenAuth)-forwarded)
	}
}
