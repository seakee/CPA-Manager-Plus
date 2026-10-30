package identity

import (
	"errors"
	"testing"
)

// TestCanonicalExternalURL proves the spellings that name one CPA become one
// value, and that everything which changes what a request means is preserved.
func TestCanonicalExternalURL(t *testing.T) {
	for name, testCase := range map[string]struct {
		raw  string
		want string
	}{
		"already canonical":      {raw: "https://cpa.example", want: "https://cpa.example"},
		"capitalized scheme":     {raw: "HTTPS://cpa.example", want: "https://cpa.example"},
		"capitalized host":       {raw: "https://CPA.Example", want: "https://cpa.example"},
		"explicit https port":    {raw: "https://cpa.example:443", want: "https://cpa.example"},
		"explicit http port":     {raw: "http://cpa.example:80", want: "http://cpa.example"},
		"trailing slash":         {raw: "https://cpa.example/", want: "https://cpa.example"},
		"path":                   {raw: "https://cpa.example/cpa/", want: "https://cpa.example/cpa"},
		"path case is preserved": {raw: "https://cpa.example/CPA", want: "https://cpa.example/CPA"},
		"non-default port":       {raw: "https://cpa.example:8443", want: "https://cpa.example:8443"},
		"query":                  {raw: "https://cpa.example?x=1", want: "https://cpa.example?x=1"},
		"ipv4":                   {raw: "http://10.0.0.4:8317", want: "http://10.0.0.4:8317"},
		"ipv6":                   {raw: "http://[::1]:8317", want: "http://[::1]:8317"},
		"ipv6 without port":      {raw: "http://[::1]", want: "http://[::1]"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := CanonicalExternalURL(testCase.raw)
			if err != nil {
				t.Fatalf("CanonicalExternalURL(%q) = %v", testCase.raw, err)
			}
			if got != testCase.want {
				t.Fatalf("CanonicalExternalURL(%q) = %q, want %q", testCase.raw, got, testCase.want)
			}
		})
	}
	for name, raw := range map[string]string{
		"empty":       "",
		"padded":      " https://cpa.example",
		"no scheme":   "cpa.example",
		"unsupported": "ftp://cpa.example",
		"userinfo":    "https://user:secret@cpa.example",
		"no host":     "https:///cpa",
		"relative":    "/cpa",
	} {
		t.Run("refused "+name, func(t *testing.T) {
			if _, err := CanonicalExternalURL(raw); !errors.Is(err, ErrExternalURLUnusable) {
				t.Fatalf("CanonicalExternalURL(%q) accepted an unusable address", raw)
			}
		})
	}
}
