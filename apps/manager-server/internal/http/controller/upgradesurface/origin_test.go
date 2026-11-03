package upgradesurface

import (
	"net/http/httptest"
	"testing"
)

func TestNormalizeOriginAcceptsOnlyABareHTTPOrigin(t *testing.T) {
	for _, testCase := range []struct {
		raw, want string
	}{
		{raw: "", want: ""},
		{raw: "https://manager.test", want: "https://manager.test"},
		{raw: " https://manager.test/ ", want: "https://manager.test"},
		{raw: "http://127.0.0.1:18317", want: "http://127.0.0.1:18317"},
	} {
		got, err := NormalizeOrigin(testCase.raw)
		if err != nil || got != testCase.want {
			t.Fatalf("NormalizeOrigin(%q) = %q err=%v, want %q", testCase.raw, got, err, testCase.want)
		}
	}
	for _, invalid := range []string{
		"null", "file:///tmp/source", "https://user:pass@manager.test",
		"https://manager.test/path", "https://manager.test?key=test", "ftp://manager.test",
	} {
		if got, err := NormalizeOrigin(invalid); err == nil {
			t.Fatalf("accepted invalid origin %q as %q", invalid, got)
		}
	}
}

func TestCrossSiteSharesOneRule(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		configured string
		origin     string
		site       string
		want       bool
	}{
		{name: "cli caller", want: false},
		{name: "same origin", configured: "https://manager.test", origin: "https://manager.test", want: false},
		{name: "other origin", configured: "https://manager.test", origin: "https://attacker.test", want: true},
		{name: "scheme differs from the configured origin", configured: "https://manager.test", origin: "http://manager.test", want: true},
		{name: "cross-site fetch", configured: "https://manager.test", origin: "https://manager.test", site: "cross-site", want: true},
		{name: "same-site fetch is not same-origin", configured: "https://manager.test", origin: "https://manager.test", site: "same-site", want: true},
		{name: "same-origin fetch", configured: "https://manager.test", origin: "https://manager.test", site: "same-origin", want: false},
		{name: "request host without a configured origin", origin: "https://manager.test", want: false},
		{name: "other origin without a configured origin", origin: "https://attacker.test", want: true},
		{name: "unusable configured origin trusts no browser", configured: "null", origin: "https://manager.test", want: true},
		{name: "unusable configured origin still admits a CLI caller", configured: "null", want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://manager.test", nil)
			if testCase.origin != "" {
				r.Header.Set("Origin", testCase.origin)
			}
			if testCase.site != "" {
				r.Header.Set("Sec-Fetch-Site", testCase.site)
			}
			if got := CrossSite(r, testCase.configured); got != testCase.want {
				t.Fatalf("CrossSite = %t, want %t", got, testCase.want)
			}
		})
	}
}
