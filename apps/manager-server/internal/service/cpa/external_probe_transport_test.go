package cpa

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestExternalProbeIgnoresTheEnvironmentProxy proves the version probe reaches
// the CPA directly. It judges the hop a write would take, so an installed
// HTTP_PROXY must not become the hop that answers for the user's CPA — the
// credential it carries is the user's management key.
func TestExternalProbeIgnoresTheEnvironmentProxy(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("https_proxy", proxy.URL)

	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-CPA-Version", "v8.0.13")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer cpa.Close()

	report, err := ObserveExternalVersion(t.Context(), cpa.URL, "management-key")
	if err != nil {
		t.Fatalf("probe through a direct transport: %v", err)
	}
	if report.Support != VersionSupportSupported {
		t.Fatalf("report = %+v", report)
	}
	if proxied.Load() != 0 {
		t.Fatalf("the probe travelled through the environment proxy %d times", proxied.Load())
	}
}

// TestExternalProbeDoesNotFollowARedirect proves a redirect is an answer this
// Manager refuses to chase: following it would authenticate the management
// credential against a host the committed address never named.
func TestExternalProbeDoesNotFollowARedirect(t *testing.T) {
	var followed atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
		w.Header().Set("X-CPA-Version", "v8.0.13")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer elsewhere.Close()

	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer cpa.Close()

	report, err := ObserveExternalVersion(t.Context(), cpa.URL, "management-key")
	if !errors.Is(err, ErrProofUnavailable) {
		t.Fatalf("probe on a redirect = %v, want ErrProofUnavailable", err)
	}
	if report != (ExternalVersionReport{}) {
		t.Fatalf("report on a redirect = %+v", report)
	}
	if followed.Load() != 0 {
		t.Fatalf("the probe followed the redirect %d times", followed.Load())
	}
}

// TestExternalProbeBoundsItsBudget proves the probe cannot hold a write open:
// every External write runs it while a save holds the Manager's lock, so its
// budget is the same five seconds the write transport allows.
func TestExternalProbeBoundsItsBudget(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	defer slow.Close()

	started := time.Now()
	_, err := ObserveExternalVersion(t.Context(), slow.URL, "management-key")
	if !errors.Is(err, ErrProofUnavailable) {
		t.Fatalf("probe on a silent CPA = %v, want ErrProofUnavailable", err)
	}
	if elapsed := time.Since(started); elapsed > 8*time.Second {
		t.Fatalf("the probe held its caller for %s", elapsed)
	}
}

// TestExternalProbeTreatsAForbiddenAnswerAsARejection is the ban case: CPA v8
// answers 403 while an address is banned for too many failed authentications,
// and 401 to a credential it does not hold. Both are the CPA refusing this
// Manager, so neither may be reported as an unavailable version proof or as a
// version class.
func TestExternalProbeTreatsAForbiddenAnswerAsARejection(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()
			report, err := ObserveExternalVersion(t.Context(), server.URL, "management-key")
			if !errors.Is(err, ErrManagementRejected) {
				t.Fatalf("probe = %v, want ErrManagementRejected", err)
			}
			if errors.Is(err, ErrProofUnavailable) {
				t.Fatal("a refusal was reported as an unavailable proof")
			}
			if report != (ExternalVersionReport{}) {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

// TestExternalUsageStatisticsTreatsAForbiddenAnswerAsARejection proves the
// statistics read and write both report a 403 as the same rejection the probe
// and the rebind verification do: one code for a CPA that refuses this Manager,
// whichever exchange met it.
func TestExternalUsageStatisticsTreatsAForbiddenAnswerAsARejection(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case externalUsageStatisticsPath, "/v0/management/config":
				default:
					t.Fatalf("authenticated read path = %q", r.URL.Path)
				}
				w.WriteHeader(status)
			}))
			defer server.Close()

			enabled, err := FetchExternalUsageStatistics(t.Context(), server.URL, "management-key")
			if !errors.Is(err, ErrManagementRejected) || enabled {
				t.Fatalf("fetch = %v %v, want the typed rejection", enabled, err)
			}
			if err = SetExternalUsageStatistics(t.Context(), server.URL, "management-key", true); !errors.Is(err, ErrManagementRejected) {
				t.Fatalf("write = %v, want the typed rejection", err)
			}
			// The probe over the same CPA reports the same typed blocker.
			if _, err = ObserveExternalVersion(t.Context(), server.URL, "management-key"); !errors.Is(err, ErrManagementRejected) {
				t.Fatalf("probe = %v, want the typed rejection", err)
			}
		})
	}
}
