package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// The public model list is the caller's own credential's business: the CPA
// decides about it, so Manager forwards it unchanged and never substitutes a
// credential of its own. These two cases are the boundary the console fix must
// not move.
func TestProxyModelListForwardsCallerAuthorizationUnchanged(t *testing.T) {
	for _, test := range []struct {
		name   string
		header string
	}{
		{name: "anonymous read"},
		{name: "caller key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var forwarded []string
			var forwardedPath string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded = append(forwarded, r.Header.Values("Authorization")...)
				forwardedPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-test"}]}`))
			}))
			t.Cleanup(upstream.Close)

			st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.SaveSetup(context.Background(), store.Setup{
				CPAUpstreamURL: upstream.URL,
				ManagementKey:  "management-key",
			}); err != nil {
				t.Fatalf("save setup: %v", err)
			}
			service := New(managerconfig.New(config.Config{}, st, nil), st)

			request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			if test.header != "" {
				request.Header.Set("Authorization", test.header)
			}
			response := httptest.NewRecorder()
			service.ProxyModelList(response, request,
				func(w http.ResponseWriter, status int, err error) { http.Error(w, err.Error(), status) },
				func(w http.ResponseWriter) { http.Error(w, "method not allowed", http.StatusMethodNotAllowed) },
			)

			if response.Code != http.StatusOK {
				t.Fatalf("model list returned %d: %s", response.Code, response.Body.String())
			}
			if forwardedPath != "/v1/models" {
				t.Fatalf("forwarded path = %q, want /v1/models", forwardedPath)
			}
			if test.header == "" {
				if len(forwarded) != 0 {
					t.Fatalf("anonymous read forwarded Authorization %q", forwarded)
				}
				return
			}
			if len(forwarded) != 1 || forwarded[0] != test.header {
				t.Fatalf("forwarded authorization = %q, want exactly %q", forwarded, test.header)
			}
		})
	}
}
