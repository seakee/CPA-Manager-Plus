package freshbootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
)

func TestFixedTransportClaimSessionMethodsAliasesAndOversizedBodies(t *testing.T) {
	p := filepath.Join(t.TempDir(), "manager.sqlite")
	db, e := sqlite.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	st := freshstore.New(db)
	id := bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "install", Generation: "gen", OperationID: "fresh-operation", DatabasePath: p}
	if _, e = st.Create(t.Context(), id, nil); e != nil {
		t.Fatal(e)
	}
	a := fresh.New(st)
	current := func(ctx context.Context) (bootstrap.FreshOperation, error) {
		r, e := st.Load(ctx)
		return r.Operation, e
	}
	fixed := Handler{Authority: a, Current: current}
	gatewayHits := 0
	h := routing.ProtectFixedBootstrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gatewayHits++; w.WriteHeader(200) }), fixed)
	request := func(method, path, auth, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	base := bootstrap.FixedBootstrapDiscoveryPath
	for _, suffix := range []string{"/unknown", "/ui/alias", "/api/v2/unknown", "/api/v2/../claim/exchange", "//ui", "/ui/", "%2fui", "%252fui", "/assets%2fmissing.js", "/ui%3falias"} {
		w := request("POST", base+suffix, "Bearer admin-key", "")
		if w.Code != 404 || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "controlBasePath") {
			t.Fatal(suffix, w.Code, w.Body.String())
		}
	}
	// Malformed encoding cannot be constructed by httptest.NewRequest; model
	// the already-parsed transport target without passing it through a mux.
	r := httptest.NewRequest("GET", base, nil)
	r.URL.Path = base + "%zz/ui"
	r.URL.RawPath = ""
	r.RequestURI = r.URL.Path
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 || gatewayHits != 0 {
		t.Fatal("malformed alias escaped ownership")
	}
	if w := request("OPTIONS", base, "", ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := request("POST", base, "", ""); w.Code != 405 {
		t.Fatal(w.Code)
	}
	handoff, e := a.Issue(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	data, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(handoff.Fragment, "#handoff="))
	if e != nil {
		t.Fatal(e)
	}
	var claim fresh.ExchangeRequest
	if e = json.Unmarshal(data, &claim); e != nil {
		t.Fatal(e)
	}
	exchangeData, _ := json.Marshal(claim)
	if w := request("POST", bootstrap.FixedBootstrapAPIPath+"claim/exchange", "Bearer admin-key", string(exchangeData)); w.Code != 401 {
		t.Fatal("Admin accepted by fixed exchange")
	}
	oversized := append(append([]byte{}, exchangeData...), bytes.Repeat([]byte(" "), 16385)...)
	if w := request("POST", bootstrap.FixedBootstrapAPIPath+"claim/exchange", "", string(oversized)); w.Code != 400 {
		t.Fatal("oversized claim accepted", w.Code)
	}
	w = request("POST", bootstrap.FixedBootstrapAPIPath+"claim/exchange", "", string(exchangeData))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var session fresh.SessionResult
	if json.Unmarshal(w.Body.Bytes(), &session) != nil || session.Token == "" {
		t.Fatal("missing session")
	}
	if w = request("POST", bootstrap.FixedBootstrapAPIPath+"claim/exchange", "", string(exchangeData)); w.Code != 401 || strings.Contains(w.Body.String(), claim.Token) {
		t.Fatal("exchange replay or error leak")
	}
	if w = request("GET", bootstrap.FixedBootstrapAPIPath+"operation", "Fresh "+session.Token, ""); w.Code != 200 {
		t.Fatal("session not accepted")
	}
	if w = request("POST", bootstrap.FixedBootstrapAPIPath+"operation", "Fresh "+session.Token, ""); w.Code != 405 {
		t.Fatal("method bypass")
	}
	if w = request("POST", bootstrap.FixedBootstrapAPIPath+"admin/generate", "Fresh "+session.Token, ""); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("generated secret was not bounded")
	}
	if gatewayHits != 0 {
		t.Fatal("fixed transport fell through to Gateway")
	}
}
