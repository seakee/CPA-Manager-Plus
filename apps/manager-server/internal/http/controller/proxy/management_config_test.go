package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	proxysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// 处理层行为：管理密钥验证通过时替换为已保存的 CPA 密钥；其它情况（错误/空密钥）绝不替换；
// 非 GET/PUT/PATCH/DELETE 直接 405。
func TestManagementConfigHandler(t *testing.T) {
	var seenAuth []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		_, _ = io.Copy(io.Discard, r.Body)
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
	credential, err := security.NewAdminCredential("panel-admin", "test")
	if err != nil {
		t.Fatalf("new admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	handler := &Handler{App: &app.Context{
		Config:           config.Config{},
		Store:            st,
		ServiceID:        "test",
		AdminAuthService: adminauthsvc.New(config.Config{}, st),
		ProxyService:     proxysvc.New(managerconfig.New(config.Config{}, st, nil), st),
	}}

	const configPath = "/v8/management/config/plugins/configs/demo/pool/strategy"
	call := func(method, auth, body string) int {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, configPath, reader)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		handler.ManagementConfig(rec, req)
		return rec.Code
	}

	if code := call(http.MethodPut, "Bearer panel-admin", `"headroom-first"`); code != http.StatusOK {
		t.Fatalf("admin call status = %d", code)
	}
	if got := seenAuth[len(seenAuth)-1]; got != "Bearer saved-key" {
		t.Fatalf("admin call forwarded %q, want the saved CPA key", got)
	}

	if code := call(http.MethodPut, "Bearer caller-key", `"round-robin"`); code != http.StatusOK {
		t.Fatalf("caller call status = %d", code)
	}
	if got := seenAuth[len(seenAuth)-1]; got != "Bearer caller-key" {
		t.Fatalf("caller call forwarded %q, want the caller token", got)
	}

	if code := call(http.MethodPut, "Bearer wrong-key", `"round-robin"`); code != http.StatusOK {
		t.Fatalf("wrong-key call status = %d", code)
	}
	if got := seenAuth[len(seenAuth)-1]; got != "Bearer wrong-key" {
		t.Fatalf("wrong key was replaced by %q; the saved key must never be substituted", got)
	}

	if code := call(http.MethodPut, "", `"round-robin"`); code != http.StatusOK {
		t.Fatalf("empty-key call status = %d", code)
	}
	if got := seenAuth[len(seenAuth)-1]; got != "" {
		t.Fatalf("empty key was replaced by %q; the saved key must never be substituted", got)
	}

	forwarded := len(seenAuth)
	if code := call(http.MethodPost, "Bearer panel-admin", `{}`); code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", code)
	}
	if len(seenAuth) != forwarded {
		t.Fatal("a rejected method was forwarded upstream")
	}
}
