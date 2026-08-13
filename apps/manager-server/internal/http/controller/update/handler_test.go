package update

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

func TestUpdateEndpointsRequirePanelAuthorization(t *testing.T) {
	cfg := testutil.NewConfig(t)
	store := testutil.NewStore(t, cfg)
	handler := &Handler{App: &app.Context{
		Config:           cfg,
		AdminAuthService: adminauthsvc.New(cfg, store),
	}}
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, httptest.NewRequest(http.MethodGet, "/v0/management/update/capability", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestApplyFailsBeforeServiceMutationWithoutShutdownCoordinator(t *testing.T) {
	cfg := testutil.NewConfig(t)
	store := testutil.NewStore(t, cfg)
	handler := &Handler{App: &app.Context{
		Config:           cfg,
		AdminAuthService: adminauthsvc.New(cfg, store),
	}}
	request := httptest.NewRequest(http.MethodPost, "/v0/management/update/apply", nil)
	request.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusConflict || recorder.Body.String() == "" {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}
