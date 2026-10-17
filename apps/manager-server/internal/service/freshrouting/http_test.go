package freshrouting

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/fullweb"
)

func TestHTTPDriverVerifiesHTMLGeneratedAssetsForAmpersandPrefix(t *testing.T) {
	state := State{InstallationID: "installation", Generation: "generation", OperationID: "fresh-operation", Revision: 1, Target: "/operator&team"}
	dist := fstest.MapFS{"index.html": {Data: []byte(`<html><div id="root"></div><script src="./assets/index-AbCd1234.js"></script><script>__CPAMP_ROUTING_CONTEXT__</script></html>`)}, "assets/index-AbCd1234.js": {Data: []byte(`console.log('same Full Web build')`)}}
	var assets atomic.Int32
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != state.Target+"/api/v2/bootstrap/identity" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
	})
	manager := fullweb.InitialHandler{Target: state.Target, Dist: dist, API: api}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != state.Target && !strings.HasPrefix(r.URL.Path, state.Target+"/") {
			w.Header().Set("X-CPAMP-Route-Owner", "gateway")
			w.WriteHeader(503)
			return
		}
		if strings.Contains(r.URL.Path, "/assets/") {
			assets.Add(1)
		}
		w.Header().Set("X-CPAMP-Route-Owner", "manager")
		manager.ServeHTTP(w, r)
	}))
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+state.Target, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "text/html")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || !strings.Contains(string(body), "/operator&amp;team/assets/") {
		t.Fatalf("expected actual escaped Manager HTML: %s %v", body, err)
	}
	driver, err := NewHTTPDriver(HTTPConfig{IngressControlURL: server.URL, PublicOrigin: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err = driver.Verify(t.Context(), state); err != nil || assets.Load() != 1 {
		t.Fatalf("HTML entity leaked into asset URL: requests=%d err=%v", assets.Load(), err)
	}
}

func TestHTTPDriverSafePublicIdentityVerificationAndRedirectRejection(t *testing.T) {
	s := State{InstallationID: "installation", Generation: "gen", OperationID: "fresh-operation", Revision: 1, Target: "/private"}
	for _, kind := range []string{"valid", "redirect", "wrong-api", "asset-html", "gateway-open", "gateway-manager"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-CPAMP-Route-Owner", "manager")
				if r.Header.Get("Authorization") != "" {
					t.Error("internal credential leaked into public probe")
				}
				switch r.URL.Path {
				case "/private":
					if kind == "redirect" {
						http.Redirect(w, r, "/somewhere", 302)
						return
					}
					w.Header().Set("Content-Type", "text/html")
					io.WriteString(w, `<div id="root"></div><script src="/private/assets/index-AbCd1234.js"></script>`)
				case "/private/assets/index-AbCd1234.js":
					w.Header().Set("Content-Type", "application/javascript")
					if kind == "asset-html" {
						w.Header().Set("Content-Type", "text/html")
					}
					io.WriteString(w, "console.log('same build')")
				case "/private/api/v2/bootstrap/identity":
					actual := s
					if kind == "wrong-api" {
						actual.Generation = "foreign"
					}
					json.NewEncoder(w).Encode(actual)
				default:
					w.Header().Set("X-CPAMP-Route-Owner", "gateway")
					if kind == "gateway-manager" {
						w.Header().Set("X-CPAMP-Route-Owner", "manager")
					}
					if kind == "gateway-open" {
						w.WriteHeader(200)
					} else {
						w.WriteHeader(503)
					}
				}
			}))
			defer server.Close()
			d, e := NewHTTPDriver(HTTPConfig{IngressControlURL: server.URL, PublicOrigin: server.URL})
			if e != nil {
				t.Fatal(e)
			}
			e = d.Verify(t.Context(), s)
			if (kind == "valid") != (e == nil) {
				t.Fatal(kind, e)
			}
		})
	}
	for _, origin := range []string{"", "http://user:secret@example.com", "http://example.com/path", "http://example.com?query", "ftp://example.com"} {
		if _, e := NewHTTPDriver(HTTPConfig{IngressControlURL: "http://localhost:9082", PublicOrigin: origin}); e == nil {
			t.Fatal("unsafe public authority", origin)
		}
	}
}
func TestHTTPDriverInternalControlCarriesNoCredentialAndKeepsErrorsOpaque(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("internal control unexpectedly carried a credential")
		}
		w.WriteHeader(500)
		io.WriteString(w, "private-internal-detail")
	}))
	defer server.Close()
	d, _ := NewHTTPDriver(HTTPConfig{IngressControlURL: server.URL, PublicOrigin: server.URL})
	_, e := d.Inspect(t.Context())
	if e == nil || strings.Contains(e.Error(), "private-internal-detail") {
		t.Fatal(e)
	}
}
