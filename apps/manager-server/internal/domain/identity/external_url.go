package identity

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// ErrExternalURLUnusable reports an address that cannot name an External CPA:
// it is empty, is not an absolute http(s) URL, carries userinfo, or carries no
// host.
var ErrExternalURLUnusable = errors.New("external address is not a usable http(s) endpoint")

// CanonicalExternalURL is the single spelling of an External CPA address, and
// therefore the exact bytes every connection identity is derived from.
//
// The Manager compares two addresses by hashing them, so a spelling difference
// that names the same CPA would read as a different target: `https://cpa.example`,
// `https://CPA.example` and `https://cpa.example:443` are one destination, not
// three. Lowercasing the scheme and the host and dropping the default port
// (`:443` for https, `:80` for http) makes those one value without changing which
// endpoint is reached. The trailing slash goes too, because a path of "/" and no
// path are the same request target.
//
// What canonicalization cannot fix is two different CPAs behind one URL; the
// caller records a server fingerprint beside the identity for that case. It never
// rewrites the path, the query, a non-default port, an IPv6 literal or the case
// of a path segment: those change what the request means.
func CanonicalExternalURL(raw string) (string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return "", ErrExternalURLUnusable
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" {
		return "", ErrExternalURLUnusable
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", ErrExternalURLUnusable
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", ErrExternalURLUnusable
	}
	port := parsed.Port()
	if scheme == "https" && port == "443" || scheme == "http" && port == "80" {
		port = ""
	}
	authority := host
	if strings.Contains(host, ":") {
		// An IPv6 literal keeps its brackets in the authority; Hostname() strips
		// them, so they are restored here rather than emitted as a bare colon.
		authority = "[" + host + "]"
	}
	if port != "" {
		authority = net.JoinHostPort(host, port)
	}
	canonical := url.URL{Scheme: scheme, Host: authority, Path: strings.TrimRight(parsed.Path, "/"), RawQuery: parsed.RawQuery, ForceQuery: parsed.ForceQuery}
	return canonical.String(), nil
}
