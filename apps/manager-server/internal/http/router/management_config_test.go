package router

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

// 覆盖实际踩过的失败模式：路由漏分发 `/v8/management/config` 时请求在面板层 404，
// 页面的配置写入无法持久化。本测试经真实 router.New(appCtx) 驱动整条链路。
func TestRouterDispatchesV8ManagementConfig(t *testing.T) {
	var seenPaths, seenAuth []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPaths = append(seenPaths, r.URL.Path)
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
	handler := New(&app.Context{
		Config:           config.Config{},
		Store:            st,
		ServiceID:        "test",
		AdminAuthService: adminauthsvc.New(config.Config{}, st),
		ProxyService:     proxysvc.New(managerconfig.New(config.Config{}, st, nil), st),
	})

	const configPath = "/v8/management/config/plugins/configs/demo/pool/strategy"
	send := func(t *testing.T, method, path, auth, body string) *httptest.ResponseRecorder {
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
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := send(t, http.MethodPut, configPath, "Bearer panel-admin", `"headroom-first"`); rec.Code != http.StatusOK {
		t.Fatalf("admin PUT status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(seenPaths) != 1 || seenPaths[0] != configPath {
		t.Fatalf("upstream saw %v, want %s", seenPaths, configPath)
	}
	if seenAuth[0] != "Bearer saved-key" {
		t.Fatalf("admin PUT forwarded %q, want the saved CPA key", seenAuth[0])
	}

	if rec := send(t, http.MethodPatch, configPath, "Bearer caller-key", `{"strategy":"round-robin"}`); rec.Code != http.StatusOK {
		t.Fatalf("caller PATCH status = %d", rec.Code)
	}
	if seenAuth[1] != "Bearer caller-key" {
		t.Fatalf("caller PATCH forwarded %q, want the caller token", seenAuth[1])
	}

	before := len(seenPaths)
	if rec := send(t, http.MethodGet, "/v8/management/other", "Bearer panel-admin", ""); rec.Code != http.StatusNotFound || len(seenPaths) != before {
		t.Fatalf("non-config /v8 path: status=%d forwarded=%d; the config passthrough must stay narrow", rec.Code, len(seenPaths)-before)
	}
}
