package credentiallabel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	adminauth "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	service "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/credentiallabel"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

func TestLabelsRequireAdminAndValidateWrites(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	h := &Handler{App: &app.Context{AdminAuthService: adminauth.New(cfg, st), CredentialLabelService: service.New(st.CredentialLabels)}}
	hash := strings.Repeat("a", 64)
	base := "/v0/management/credential-labels"
	request := func(method, path, body string, authorized bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if authorized {
			req.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
		}
		rr := httptest.NewRecorder()
		h.Handle(rr, req)
		return rr
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		path := base
		if method == http.MethodPut {
			path += "/" + hash
		}
		if rr := request(method, path, `{"label":"account@example.com"}`, false); rr.Code != 401 {
			t.Fatalf("%s without auth: %d", method, rr.Code)
		}
	}
	for _, body := range []string{`{`, `{}`, `{"label":null}`, `{"label":"ok","apiKey":"secret"}`, `{"label":"ok"}{}`, `{"label":"line\nbreak"}`, `{"label":123}`, `{"label":"` + strings.Repeat("界", 121) + `"}`, `{"label":"` + strings.Repeat("a", 5000) + `"}`} {
		if rr := request(http.MethodPut, base+"/"+hash, body, true); rr.Code != 400 {
			t.Fatalf("invalid body accepted: %d", rr.Code)
		}
	}
	if rr := request(http.MethodPut, base+"/sk-not-a-hash", `{"label":"ok"}`, true); rr.Code != 400 {
		t.Fatal("raw key accepted")
	}
	if rr := request(http.MethodPut, base+"/"+hash, `{"label":"  account@example.com  "}`, true); rr.Code != 200 {
		t.Fatalf("save: %s", rr.Body)
	}
	rr := request(http.MethodGet, base, "", true)
	if rr.Code != 200 {
		t.Fatal(rr.Code)
	}
	var result struct {
		Items []struct{ KeyHash, Label string }
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Label != "account@example.com" || result.Items[0].KeyHash != hash {
		t.Fatalf("labels: %#v", result)
	}
	if rr = request(http.MethodPut, base+"/"+hash, `{"label":""}`, true); rr.Code != 200 {
		t.Fatalf("clear: %s", rr.Body)
	}
	labels, err := st.CredentialLabels.List(context.Background())
	if err != nil || len(labels) != 0 {
		t.Fatalf("clear did not delete: %v %v", labels, err)
	}
}
