package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestAPIKeysManagementRequiresCPAMPAdminBeforeProxy(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "admin.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	credential, err := security.NewAdminCredential("cpamp-admin-test", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
	handler := &Handler{App: &app.Context{AdminAuthService: adminauth.New(config.Config{}, st)}}
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete, http.MethodPut} {
		req := httptest.NewRequest(method, "/v0/management/api-keys", nil)
		req.Header.Set("Authorization", "Bearer caller-cpa-key")
		response := httptest.NewRecorder()
		handler.Management(response, req)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d", method, response.Code)
		}
	}
}
