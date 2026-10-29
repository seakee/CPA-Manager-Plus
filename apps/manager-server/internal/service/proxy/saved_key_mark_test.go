package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSavedKeyMarkFollowsTheCredentialActuallySent pins the one signal a gate above
// this proxy has: the path does not say whether the saved key went out, because the
// same management prefix is proxied with the caller's own header for a caller-auth
// plugin request, and the model list forwards the caller's header on a CPA path.
func TestSavedKeyMarkFollowsTheCredentialActuallySent(t *testing.T) {
	fixture := newPluginProxyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	for _, scenario := range []struct {
		name string
		path string
		call func(http.ResponseWriter, *http.Request)
		want bool
	}{
		{
			name: "management read",
			path: "/v0/management/api-keys",
			call: func(w http.ResponseWriter, r *http.Request) { fixture.service.ProxyManagement(w, r, writeProxyError) },
			want: true,
		},
		{
			name: "plugin management",
			path: "/v0/management/codex-invite/invite",
			call: func(w http.ResponseWriter, r *http.Request) { fixture.service.ProxyPluginManagement(w, r, writeProxyError) },
			want: true,
		},
		{
			name: "plugin management with caller auth",
			path: "/v0/management/codex-invite/invite",
			call: func(w http.ResponseWriter, r *http.Request) {
				fixture.service.ProxyPluginManagementWithCallerAuth(w, r, writeProxyError)
			},
			want: false,
		},
		{
			name: "model list",
			path: "/v1/models",
			call: func(w http.ResponseWriter, r *http.Request) {
				fixture.service.ProxyModelList(w, r, writeProxyError, func(w http.ResponseWriter) {
					w.WriteHeader(http.StatusMethodNotAllowed)
				})
			},
			want: false,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, mark := WithSavedKeyMark(t.Context())
			request := httptest.NewRequest(http.MethodGet, scenario.path, nil).WithContext(ctx)
			scenario.call(httptest.NewRecorder(), request)
			if mark.Attached() != scenario.want {
				t.Fatalf("saved key attached = %v, want %v", mark.Attached(), scenario.want)
			}
		})
	}
}
