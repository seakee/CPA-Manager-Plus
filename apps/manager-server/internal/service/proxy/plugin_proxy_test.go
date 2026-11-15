package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// pluginProxyFixture wires a completed managed installation whose saved CPA
// management key is known to the test, so every assertion can prove whether the
// saved key did or did not reach the upstream.
type pluginProxyFixture struct {
	service *Service
	// upstreamAuthorizations records the Authorization header of every upstream
	// request in order.
	upstreamAuthorizations *[]string
	upstreamPaths          *[]string
}

func newPluginProxyFixture(t *testing.T, handler http.HandlerFunc) pluginProxyFixture {
	t.Helper()
	const managementKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	authorizations := &[]string{}
	paths := &[]string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*authorizations = append(*authorizations, r.Header.Get("Authorization"))
		*paths = append(*paths, r.URL.Path)
		handler(w, r)
	}))
	t.Cleanup(upstream.Close)
	baseURL := strings.TrimRight(upstream.URL, "/")

	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fresh := freshstore.New(db)
	id := bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "installation", Generation: "generation", OperationID: "operation", DatabasePath: path}
	admin, err := security.NewAdminCredential("Cpamp_Plugin_Isolation_Admin_518204", "host")
	if err != nil {
		t.Fatal(err)
	}
	record, err := fresh.Create(context.Background(), id, &admin)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector([]byte("plugin isolation data key"))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := protector.ProtectString(managementKey)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed", TargetIdentity: "runtime", TargetGeneration: 7, TargetArtifactID: "sha256:" + strings.Repeat("b", 64), DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity(baseURL)}
	pending, err := fresh.Update(context.Background(), id, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = checkpoint
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: ciphertext}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fresh.Update(context.Background(), id, pending.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing.OperationID = id.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/console"
		r.Operation.Routing.InferenceTarget = "/access-api"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		r.Operation.Routing.InferenceCommitted = true
		r.Operation.ProductComplete = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	st := store.New(db, protector)
	return pluginProxyFixture{
		service:                New(managerconfig.New(config.Config{}, st, nil), st),
		upstreamAuthorizations: authorizations,
		upstreamPaths:          paths,
	}
}

func sandboxResourceRequest(method, target string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("Sec-Fetch-Dest", "iframe")
	return request
}

func writeProxyError(w http.ResponseWriter, status int, err error) {
	http.Error(w, err.Error(), status)
}

// The v1.11.11 invariant: an unauthenticated caller never receives the saved CPA
// management key. The caller-auth branch must forward the caller's own (absent)
// authorization rather than substituting the stored credential.
func TestPluginResourceCallerAuthNeverUsesSavedKey(t *testing.T) {
	const savedKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fixture := newPluginProxyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	request := sandboxResourceRequest(http.MethodGet, "/v0/resource/plugins/demo/index.html")
	recorder := httptest.NewRecorder()
	fixture.service.ProxyPluginResourceWithCallerAuth(recorder, request, writeProxyError)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", recorder.Code, recorder.Body.String())
	}
	if len(*fixture.upstreamAuthorizations) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(*fixture.upstreamAuthorizations))
	}
	forwarded := (*fixture.upstreamAuthorizations)[0]
	if strings.Contains(forwarded, savedKey) {
		t.Fatal("caller-auth resource forwarded the SAVED CPA management key")
	}
	if forwarded != "" {
		t.Fatalf("caller-auth resource invented an authorization: %q", forwarded)
	}
}

// The authenticated admin branch must still use the saved key: that is the whole
// point of the Manager holding it, and the contrast proves the test above
// distinguishes the two branches rather than observing an absent header twice.
func TestPluginResourceAdminAuthUsesSavedKey(t *testing.T) {
	const savedKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fixture := newPluginProxyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	request := sandboxResourceRequest(http.MethodGet, "/v0/resource/plugins/demo/index.html")
	recorder := httptest.NewRecorder()
	fixture.service.ProxyPluginResource(recorder, request, writeProxyError)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", recorder.Code, recorder.Body.String())
	}
	if got := (*fixture.upstreamAuthorizations)[0]; got != "Bearer "+savedKey {
		t.Fatalf("admin resource authorization = %q, want saved key", got)
	}
}

func TestPluginResourceAdmissionRejects(t *testing.T) {
	fixture := newPluginProxyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	cases := []struct {
		name   string
		method string
		target string
		header map[string]string
		want   int
	}{
		{"no fetch metadata", http.MethodGet, "/v0/resource/plugins/demo/index.html", nil, http.StatusForbidden},
		{"admin script load", http.MethodGet, "/v0/resource/plugins/demo/index.html", map[string]string{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Dest": "script"}, http.StatusForbidden},
		{"worker load", http.MethodGet, "/v0/resource/plugins/demo/page.js", map[string]string{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Dest": "worker"}, http.StatusForbidden},
		{"outside namespace", http.MethodGet, "/v0/management/config", map[string]string{"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Dest": "iframe"}, http.StatusNotFound},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(testCase.method, testCase.target, nil)
			for name, value := range testCase.header {
				request.Header.Set(name, value)
			}
			recorder := httptest.NewRecorder()
			fixture.service.ProxyPluginResourceWithCallerAuth(recorder, request, writeProxyError)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.want)
			}
			if len(*fixture.upstreamAuthorizations) != 0 {
				t.Fatalf("rejected request reached upstream %d times", len(*fixture.upstreamAuthorizations))
			}
		})
	}
}

// A query string is part of a plugin's own resource contract and must be
// preserved, while the response still carries the sandbox contract.
func TestPluginResourcePreservesQueryAndSandboxHeaders(t *testing.T) {
	fixture := newPluginProxyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "v=7" {
			t.Errorf("upstream query = %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Security-Policy", "sandbox allow-same-origin")
		w.Header().Set("Set-Cookie", "planted=1; Path=/")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("body"))
	})
	request := sandboxResourceRequest(http.MethodGet, "/v0/resource/plugins/demo/index.html?v=7")
	recorder := httptest.NewRecorder()
	fixture.service.ProxyPluginResourceWithCallerAuth(recorder, request, writeProxyError)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", recorder.Code, recorder.Body.String())
	}
	header := recorder.Header()
	if got := header.Get("Content-Security-Policy"); got != pluginResourceSandboxCSP {
		t.Fatalf("policy = %q", got)
	}
	if cookies := header.Values("Set-Cookie"); len(cookies) != 0 {
		t.Fatalf("plugin Set-Cookie survived: %v", cookies)
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff = %q", got)
	}
	if got := header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("allow-origin = %q", got)
	}
}

// A redirect that leaves the plugin's own prefix must fail the request instead
// of handing the sandbox a Location pointing at the Console or another plugin.
func TestPluginResourceRejectsRedirectOutsidePrefix(t *testing.T) {
	fixture := newPluginProxyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/console/")
		w.WriteHeader(http.StatusFound)
	})
	request := sandboxResourceRequest(http.MethodGet, "/v0/resource/plugins/demo/index.html")
	recorder := httptest.NewRecorder()
	fixture.service.ProxyPluginResourceWithCallerAuth(recorder, request, writeProxyError)
	if recorder.Code == http.StatusFound {
		t.Fatalf("redirect escaped the plugin prefix: %d", recorder.Code)
	}
	if got := recorder.Header().Get("Location"); got != "" {
		t.Fatalf("escaped Location leaked: %q", got)
	}
}

func TestPluginResourceAllowsRedirectWithinPlugin(t *testing.T) {
	fixture := newPluginProxyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/v0/resource/plugins/demo/next.html")
		w.WriteHeader(http.StatusFound)
	})
	request := sandboxResourceRequest(http.MethodGet, "/v0/resource/plugins/demo/index.html")
	recorder := httptest.NewRecorder()
	fixture.service.ProxyPluginResourceWithCallerAuth(recorder, request, writeProxyError)
	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Location"); got != "/v0/resource/plugins/demo/next.html" {
		t.Fatalf("location = %q", got)
	}
}
