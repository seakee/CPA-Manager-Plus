package ingress

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTransitionalClassifier(t *testing.T) {
	t.Parallel()
	classifier := NewTransitionalClassifier()
	tests := []struct {
		path string
		want Route
	}{
		{path: "/", want: RouteManager},
		{path: "/management.html", want: RouteManager},
		{path: "/health", want: RouteManager},
		{path: "/status", want: RouteManager},
		{path: "/setup", want: RouteManager},
		{path: FixedBootstrapDiscoveryPath, want: RouteManager},
		{path: FixedBootstrapDiscoveryPath + "/extra", want: RouteManager},
		{path: "/usage-service", want: RouteManager},
		{path: "/usage-service/info", want: RouteManager},
		{path: "/v0/management", want: RouteManager},
		{path: "/v0/management/config", want: RouteManager},
		{path: FreshRoutingControlPath, want: RouteDeny},
		{path: FreshRoutingAuthorityPath, want: RouteDeny},
		{path: "/v1/models", want: RouteGateway},
		{path: "/v1/chat/completions", want: RouteGateway},
		{path: "/v1/responses", want: RouteGateway},
		{path: "/v1/messages", want: RouteGateway},
		{path: "/future-gateway-protocol", want: RouteGateway},
		{path: "/usage-services", want: RouteGateway},
		{path: "/v0/management-other", want: RouteGateway},
		{path: "/v8/management", want: RouteDeny},
		{path: "/v8/management/config", want: RouteDeny},
		{path: "/v8/management/config/access/api-keys", want: RouteDeny},
		{path: "/v8/management/config/api-keys", want: RouteDeny},
		{path: "/v8/management/config/api-keys/claude", want: RouteDeny},
		{path: "/v8/management-other", want: RouteGateway},
		{path: "/v8/management-lookalike/v1", want: RouteGateway},
	}
	for _, test := range tests {
		test := test
		t.Run(test.path, func(t *testing.T) {
			t.Parallel()
			if got := classifier.Classify(test.path); got != test.want {
				t.Fatalf("Classify(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}

func TestClassifierRequestKeepsAmbiguousActiveCandidatesWithManager(t *testing.T) {
	control := ControlMatcherFunc(func(value string) bool {
		return value == "/control" || value == "/control/" || len(value) > len("/control/") && strings.HasPrefix(value, "/control/")
	})
	retired := ControlMatcherFunc(func(value string) bool {
		return value == "/retired" || strings.HasPrefix(value, "/retired/")
	})
	classifier := NewOwnershipClassifier(control, retired)
	for _, target := range []string{
		"/control/%2e%2e/control",
		"/control/%252e%252e/control",
		"/control%2fsettings",
		"/control%252fsettings",
		"/control\\settings",
		"/control//settings",
		FixedBootstrapDiscoveryPath + "/",
		"/.well-known%2Fcpamp-bootstrap",
		"/.well-known%252Fcpamp-bootstrap",
	} {
		t.Run(target, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, "http://example.test"+target, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer manager-looking-secret")
			if got := classifier.ClassifyRequest(req); got != RouteManager {
				t.Fatalf("ClassifyRequest(%q) = %v, want Manager", target, got)
			}
		})
	}
}

func TestClassifierRequestDeniesRetiredCandidates(t *testing.T) {
	classifier := NewOwnershipClassifier(
		ControlMatcherFunc(func(value string) bool { return value == "/control" || strings.HasPrefix(value, "/control/") }),
		ControlMatcherFunc(func(value string) bool { return value == "/retired" || strings.HasPrefix(value, "/retired/") }),
	)
	for _, target := range []string{"/retired/settings", "/retired%2fsettings", "/retired%252fsettings", "/internal%2frouting%2ffresh", "/internal%252frouting%252ffresh-authority"} {
		t.Run(target, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, "http://example.test"+target, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer manager-looking-secret")
			if got := classifier.ClassifyRequest(req); got != RouteDeny {
				t.Fatalf("ClassifyRequest(%q) = %v, want local deny", target, got)
			}
		})
	}
}

func TestOwnershipClassifierDeniesActiveRetiredOverlap(t *testing.T) {
	matcher := ControlMatcherFunc(func(value string) bool {
		return value == "/overlap" || strings.HasPrefix(value, "/overlap/")
	})
	classifier := NewOwnershipClassifier(matcher, matcher)
	for _, target := range []string{"/overlap", "/overlap/settings", "/overlap%2fsettings"} {
		request := httptest.NewRequest(http.MethodPost, "http://example.test"+target, nil)
		if got := classifier.ClassifyRequest(request); got != RouteDeny {
			t.Fatalf("ClassifyRequest(%q) = %v, want local deny", target, got)
		}
	}
	if got := classifier.Classify("/overlap/settings"); got != RouteDeny {
		t.Fatalf("Classify overlap = %v, want local deny", got)
	}
}

func TestFixedDiscoveryOwnershipWinsOverDynamicMatcherOverlap(t *testing.T) {
	matchEverything := ControlMatcherFunc(func(string) bool { return true })
	classifier := NewOwnershipClassifier(matchEverything, matchEverything)
	for _, target := range []string{
		FixedBootstrapDiscoveryPath,
		FixedBootstrapDiscoveryPath + "/extra",
		"/.well-known%2Fcpamp-bootstrap",
	} {
		request := httptest.NewRequest(http.MethodPost, "http://example.test"+target, nil)
		if got := classifier.ClassifyRequest(request); got != RouteManager {
			t.Fatalf("ClassifyRequest(%q) = %v, want fixed Manager ownership", target, got)
		}
	}
}

func TestClassifierRequestPreservesNormalGatewayOwnership(t *testing.T) {
	classifier := NewOwnershipClassifier(
		ControlMatcherFunc(func(value string) bool { return value == "/control" || strings.HasPrefix(value, "/control/") }),
		ControlMatcherFunc(func(value string) bool { return value == "/retired" || strings.HasPrefix(value, "/retired/") }),
	)
	for _, target := range []string{"/v1/responses", "/v1/chat/completions", "/models", "/control-plane", "/retiredish/models"} {
		req := httptest.NewRequest(http.MethodPost, "http://example.test"+target, nil)
		req.Header.Set("Authorization", "Bearer provider-secret")
		if got := classifier.ClassifyRequest(req); got != RouteGateway {
			t.Fatalf("ClassifyRequest(%q) = %v, want Gateway", target, got)
		}
	}
}

func TestClassifierControlMatcherIsReplaceable(t *testing.T) {
	t.Parallel()
	classifier := NewClassifier(ControlMatcherFunc(func(path string) bool {
		return path == "/future-control" || len(path) > len("/future-control/") && path[:len("/future-control/")] == "/future-control/"
	}))
	if got := classifier.Classify("/future-control/settings"); got != RouteManager {
		t.Fatalf("future control path classified as %v", got)
	}
	if got := classifier.Classify("/health"); got != RouteGateway {
		t.Fatalf("transitional route leaked into replacement matcher: %v", got)
	}
}

func TestFreshPrivateStagedKeyProbeNeverRoutesToGateway(t *testing.T) {
	classifier := NewTransitionalClassifier()
	for _, target := range []string{
		"/internal/fresh-client-key-stage/probe",
		"/internal%2ffresh-client-key-stage%2fprobe",
		"/internal%252ffresh-client-key-stage%252fprobe",
		"/internal/fresh-client-key-stage/probe/extra",
	} {
		request := httptest.NewRequest(http.MethodPost, target, nil)
		request.Header.Set("Authorization", "Bearer staged-secret")
		if got := classifier.ClassifyRequest(request); got != RouteDeny {
			t.Fatalf("private key proof leaked to Gateway: %s => %v", target, got)
		}
	}
}

// Exercise actual HTTP transport routing, not only classifier decisions.
// Neither mode may send the private probe to Manager or the raw CPA proxy.
func TestPrivateKeyConsumersAreDeniedOnPublicIngressTCP(t *testing.T) {
	var managerHits, gatewayHits atomic.Int32
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		managerHits.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer manager.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gatewayHits.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer gateway.Close()
	managerURL, _ := url.Parse(manager.URL)
	gatewayURL, _ := url.Parse(gateway.URL)
	logger := log.New(io.Discard, "", 0)
	for _, mode := range []struct {
		name    string
		handler http.Handler
	}{
		{"legacy", NewHandler(NewTransitionalClassifier(), managerURL, gatewayURL, logger)},
		{"fresh", NewFreshHandler(&FreshRoutes{}, managerURL, gatewayURL, logger, nil)},
	} {
		t.Run(mode.name, func(t *testing.T) {
			ingress := httptest.NewServer(mode.handler)
			defer ingress.Close()
			for _, path := range []string{
				"/internal/fresh-client-key-stage/probe",
				"/internal%2ffresh-client-key-stage%2fprobe",
				"/internal%252ffresh-client-key-stage%252fprobe",
				"/internal/fresh-client-key-stage/probe/extra",
				PrivateChatAdmissionPath,
				"/internal%2ffresh-chat-admission",
				"/internal%252ffresh-chat-admission",
				"/x/../internal/fresh-chat-admission",
				"/internal/fresh-chat-admission%3fignored",
				"/internal/fresh-chat-admission%3bignored",
				"/internal/fresh-chat-admission%25252fignored",
			} {
				req, err := http.NewRequest(http.MethodPost, ingress.URL+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer synthetic-staged-key")
				response, err := ingress.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusNotFound {
					t.Fatalf("public ingress %s returned %d, want deny 404", path, response.StatusCode)
				}
			}
			if managerHits.Load() != 0 || gatewayHits.Load() != 0 {
				t.Fatalf("private probe forwarded in %s mode: manager=%d gateway=%d", mode.name, managerHits.Load(), gatewayHits.Load())
			}
		})
	}
}

// The real public HTTP proxy must hand the entire fixed Bootstrap namespace to
// Manager, including denied mutations. No such request may reach CPA Gateway.
func TestFixedBootstrapPreflightPublicProxyOwner(t *testing.T) {
	var managerHits, gatewayHits atomic.Int32
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		managerHits.Add(1)
		if r.URL.Path == FixedBootstrapDiscoveryPath+"/ui" ||
			r.URL.Path == FixedBootstrapDiscoveryPath ||
			r.URL.Path == FixedBootstrapDiscoveryPath+"/assets/index-AbCd1234.js" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer manager.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gatewayHits.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer gateway.Close()
	managerURL, _ := url.Parse(manager.URL)
	gatewayURL, _ := url.Parse(gateway.URL)
	ingress := httptest.NewServer(NewFreshHandler(&FreshRoutes{}, managerURL, gatewayURL, log.New(io.Discard, "", 0), nil))
	defer ingress.Close()
	for _, item := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, FixedBootstrapDiscoveryPath, http.StatusOK},
		{http.MethodGet, FixedBootstrapDiscoveryPath + "/ui", http.StatusOK},
		{http.MethodHead, FixedBootstrapDiscoveryPath + "/assets/index-AbCd1234.js", http.StatusOK},
		{http.MethodPost, FixedBootstrapDiscoveryPath + "/api/v2/claim/exchange", http.StatusNotFound},
		{http.MethodGet, FixedBootstrapDiscoveryPath + "/ui/extra", http.StatusNotFound},
	} {
		req, err := http.NewRequest(item.method, ingress.URL+item.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := ingress.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != item.want {
			t.Fatalf("%s %s: status=%d want=%d", item.method, item.path, resp.StatusCode, item.want)
		}
		if resp.Header.Get("X-Cpamp-Route-Owner") != "manager" {
			t.Fatalf("fixed Bootstrap namespace owner lost: %s", item.path)
		}
	}
	if gatewayHits.Load() != 0 || managerHits.Load() != 5 {
		t.Fatalf("fixed Bootstrap routing: manager hits=%d gateway hits=%d", managerHits.Load(), gatewayHits.Load())
	}
}
