package freshbootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/fullweb"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := l.Addr().String()
	l.Close()
	return address
}

// This test builds and runs the actual stdlib-only Ingress process against
// real Manager handlers and the actual Manager SQLite Fresh checkpoint.
func TestRealIngressInitialRoutingAuthenticationCutoverAndRestart(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	ingressDir := filepath.Clean(filepath.Join(filepath.Dir(source), "../../../../..", "ingress"))
	task := t.TempDir()
	binary := filepath.Join(task, "ingress")
	build := exec.Command("go", "build", "-o", binary, "./cmd/cpamp-ingress")
	build.Dir = ingressDir
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build real Ingress %s: %v %s", ingressDir, e, out)
	}
	dbPath := filepath.Join(task, "manager.sqlite")
	db, e := sqlite.Open(dbPath)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	st := freshstore.New(db)
	id := bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "installation", Generation: "generation", OperationID: "fresh-operation", DatabasePath: dbPath}
	credential, e := security.NewAdminCredential("cpamp_RoutingIntegration0123456789!", "fresh")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.Create(t.Context(), id, &credential); e != nil {
		t.Fatal(e)
	}
	current := func(ctx context.Context) (bootstrap.FreshOperation, error) {
		r, e := st.Load(ctx)
		return r.Operation, e
	}
	authority := fresh.New(st)
	handoff, e := authority.Issue(t.Context())
	if e != nil || !handoff.Resume {
		t.Fatal(e)
	}
	payloadBytes, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(handoff.Fragment, "#handoff="))
	if e != nil {
		t.Fatal(e)
	}
	var payload struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(payloadBytes, &payload) != nil {
		t.Fatal("invalid host resume")
	}
	dist := fstest.MapFS{"index.html": {Data: []byte(`<html><div id="root"></div><script src="./assets/index-AbCd1234.js"></script><script>__CPAMP_ROUTING_CONTEXT__</script></html>`)}, "assets/index-AbCd1234.js": {Data: []byte(`console.log('same Full Web technical carrier')`)}}
	fixed := &Handler{Authority: authority, Current: current, Shell: fullweb.BootstrapHandler{Dist: dist}}
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op, e := current(r.Context())
		if e != nil || r.URL.Path != op.Routing.Target+"/api/v2/bootstrap/identity" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(freshrouting.StateFor(op))
	})
	manager := httptest.NewServer(routing.ProtectFixedBootstrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == freshrouting.AuthorityPath && r.URL.EscapedPath() == r.URL.Path && r.URL.RawQuery == "" {
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			op, e := current(r.Context())
			if e != nil || op.Routing.Target == "" || op.Routing.OperationID == "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(freshrouting.StateFor(op))
			return
		}
		op, e := current(r.Context())
		if e != nil || op.Routing.Target == "" {
			http.NotFound(w, r)
			return
		}
		if op.Routing.Committed {
			fullweb.Handler{Snapshot: fixed.Routing.Snapshot(r.Context()), Dist: dist, API: api}.ServeHTTP(w, r)
		} else {
			fullweb.InitialHandler{Target: op.Routing.Target, Dist: dist, API: api}.ServeHTTP(w, r)
		}
	}), fixed))
	defer manager.Close()
	gatewayHits := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gatewayHits++; w.WriteHeader(200) }))
	defer gateway.Close()
	publicAddr, controlAddr := freeAddr(t), freeAddr(t)
	origin := "http://" + publicAddr
	var process *exec.Cmd
	start := func() {
		process = exec.Command(binary)
		process.Env = append(os.Environ(), "CPAMP_INGRESS_ADDR="+publicAddr, "CPAMP_INGRESS_CONTROL_ADDR="+controlAddr, "CPAMP_MANAGER_URL="+manager.URL, "CPAMP_GATEWAY_URL="+gateway.URL)
		process.Stdout = io.Discard
		process.Stderr = io.Discard
		if e := process.Start(); e != nil {
			t.Fatal(e)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			response, e := http.Get(origin + bootstrap.FixedBootstrapDiscoveryPath)
			if e == nil {
				response.Body.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("Ingress did not start")
	}
	stop := func() {
		if process != nil && process.Process != nil {
			_ = process.Process.Kill()
			_ = process.Wait()
			process = nil
		}
	}
	defer stop()
	start()
	driver, e := freshrouting.NewHTTPDriver(freshrouting.HTTPConfig{IngressControlURL: "http://" + controlAddr, PublicOrigin: origin})
	if e != nil {
		t.Fatal(e)
	}
	fixed.Routing = freshrouting.New(st, driver)
	request := func(method, path, auth string, body any) (int, string, http.Header) {
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		r, e := http.NewRequest(method, origin+path, bytes.NewReader(data))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Accept", "text/html")
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		response, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		b, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(b), response.Header
	}
	base := bootstrap.FixedBootstrapAPIPath
	if code, _, _ := request("GET", "/v1/models", "Bearer client-key", nil); code != 503 || gatewayHits != 0 {
		t.Fatal("Gateway forwarded before routing prepare/Apply")
	}
	for _, auth := range []string{"Bearer cpamp_RoutingIntegration0123456789!", "Bearer cpa-management-key", "", "Fresh wrong-session"} {
		code, body, _ := request("GET", base+"operation", auth, nil)
		if code != 401 || strings.Contains(body, "secret") {
			t.Fatal("fixed accepted non-Fresh authority", code, body)
		}
	}
	if code, _, _ := request("GET", base+"operation", "Fresh "+payload.Token, nil); code != 200 {
		t.Fatal("current resume rejected")
	}
	if code, _, _ := request("GET", bootstrap.FixedBootstrapUIPath, "", nil); code != 200 {
		t.Fatal("same-build shell unavailable")
	}
	if code, body, _ := request("GET", bootstrap.FixedBootstrapDiscoveryPath, "", nil); code != http.StatusServiceUnavailable || strings.Contains(body, "admin_authentication_required") || !strings.Contains(body, "unavailable") {
		t.Fatal("initialized Fresh discovery", code, body)
	}
	if code, body, _ := request("POST", base+"routing/prepare", "Fresh "+payload.Token, map[string]any{"identity": id, "target": "/private-control"}); code != 200 {
		t.Fatal("prepare", code, body)
	}
	if code, _, _ := request("GET", "/v1/models", "Bearer client-key", nil); code != 503 || gatewayHits != 0 {
		t.Fatal("Gateway forwarded with prepared authority before Apply")
	}
	if code, body, _ := request("POST", base+"routing/activate", "Fresh "+payload.Token, map[string]any{"identity": id}); code != 200 {
		t.Fatal("activate real Ingress", code, body)
	}
	op, e := current(t.Context())
	if e != nil || !op.Routing.Committed || op.Routing.OperationID != id.OperationID {
		t.Fatal(op, e)
	}
	for _, suffix := range []string{"", "/ui", "/assets/index-AbCd1234.js", "/api/v2/operation", "/unknown", "%2fui", "/api/v2/claim/exchange"} {
		code, body, header := request("GET", bootstrap.FixedBootstrapDiscoveryPath+suffix, "Fresh "+payload.Token, nil)
		if code != 404 || header.Get("Location") != "" || strings.Contains(body, "private-control") {
			t.Fatal("fixed namespace did not retire", suffix, code, body)
		}
	}
	if code, _, _ := request("GET", "/private-control", "", nil); code != 200 {
		t.Fatal("canonical route unavailable")
	}
	if code, _, _ := request("GET", "/v1/models", "Bearer client-key", nil); code != 503 || gatewayHits != 0 {
		t.Fatal("Gateway activated during Fresh")
	}
	// Ingress alone restarts; Manager republishes the original committed
	// checkpoint rather than creating another operation or reopening claim.
	before := op.Revision
	stop()
	start()
	if code, _, _ := request("GET", "/v1/models", "Bearer client-key", nil); code != 503 || gatewayHits != 0 {
		t.Fatal("Gateway forwarded after Ingress restart before Restore")
	}
	if e = fixed.Routing.Restore(t.Context()); e != nil {
		t.Fatal(e)
	}
	if code, _, _ := request("GET", "/private-control", "", nil); code != 200 {
		t.Fatal("committed route not restored")
	}
	if code, _, _ := request("GET", "/v1/models", "Bearer client-key", nil); code != 503 || gatewayHits != 0 {
		t.Fatal("Restore activated Gateway")
	}
	op, e = current(t.Context())
	if e != nil || op.Revision != before || !op.Routing.Committed {
		t.Fatal("restore changed authority")
	}
}
