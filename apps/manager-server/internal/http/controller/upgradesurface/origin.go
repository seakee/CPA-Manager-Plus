// Package upgradesurface holds the request policy every leaf of the Upgrade
// surface shares. The surface has four leaves — the authentication entry and the
// two wizard reads, mounted while the operation is unfinished, plus the same two
// reads on the canonical continuation — and they must answer one origin rule,
// one rejection code and one credential source. Anything only one leaf knows is
// a way for the leaves to disagree about who may read them.
package upgradesurface

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// CodeOriginRejected is the one answer a cross-site browser request gets. Both
// leaves report it, so the entry page and the reads cannot drift apart.
const CodeOriginRejected = "origin_rejected"

// NormalizeOrigin validates and canonicalizes the configured public origin. An
// empty value means the deployment configured none. Any other value that is not
// a bare http(s) origin is an error: a deployment that names an unusable origin
// must not silently fall back to trusting the request's Host header.
func NormalizeOrigin(raw string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	if value == "" {
		return "", nil
	}
	origin, err := url.Parse(value)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" ||
		origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return "", errors.New("invalid Upgrade public origin")
	}
	return value, nil
}

// CrossSite reports whether a request carries browser provenance from a site
// other than this surface. A caller with neither Origin nor Sec-Fetch-Site is a
// CLI client and passes. publicOrigin is the configured value: when it names an
// unusable origin no browser request is trusted, and when it is empty the
// request's own scheme and Host are the expected origin. Forwarded headers
// cannot establish this trust.
func CrossSite(r *http.Request, publicOrigin string) bool {
	if r == nil {
		return false
	}
	origin := r.Header.Get("Origin")
	site := r.Header.Get("Sec-Fetch-Site")
	if origin == "" && site == "" {
		return false
	}
	if site != "" && site != "same-origin" && site != "none" {
		return true
	}
	if origin == "" {
		return false
	}
	expected, err := NormalizeOrigin(publicOrigin)
	if err != nil {
		return true
	}
	if expected == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		expected = scheme + "://" + r.Host
	}
	return origin != expected
}
