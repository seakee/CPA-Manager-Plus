package monitoring

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	monitoringsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/monitoring"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestHandleAccountHistoryRejectsUnknownTargetFields(t *testing.T) {
	st := newHandlerTestStore(t)
	const adminKey = "cpamp_test_key"
	credential, err := security.NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatalf("create admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	handler := &Handler{App: &app.Context{
		AdminAuthService:  adminauthsvc.New(config.Config{}, st),
		MonitoringService: monitoringsvc.New(st),
	}}
	req := httptest.NewRequest(
		http.MethodPost,
		"/v0/management/monitoring/account-history",
		bytes.NewBufferString(`{"accounts":[{"source_hash":"source-only"}]}`),
	)
	req.Header.Set("Authorization", "Bearer "+adminKey)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "source_hash") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

func newHandlerTestStore(t testing.TB) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	return st
}
