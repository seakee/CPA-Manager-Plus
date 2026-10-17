package ingress

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// FixedBootstrapDiscoveryPath is the only fixed unauthenticated Bootstrap
// carrier. It is intentionally duplicated at the module boundary because the
// Ingress module must remain stdlib-only; repo contract tests assert the value
// against the Manager routing contract and fixture.
const FixedBootstrapDiscoveryPath = "/.well-known/cpamp-bootstrap"

// Route identifies the internal owner of a public request.
type Route uint8

const (
	RouteGateway Route = iota
	RouteManager
	RouteDeny
)

// ControlMatcher is the single replacement seam for control route ownership.
// Runtime 12 supplies transitional path evidence. A later configuration slice
// can replace it with an effective controlBasePath matcher without changing the
// transport proxies or Compose topology.
type ControlMatcher interface {
	Matches(path string) bool
}

type ControlMatcherFunc func(path string) bool

func (f ControlMatcherFunc) Matches(path string) bool {
	return f(path)
}

type Classifier struct {
	control ControlMatcher
	retired ControlMatcher
}

func NewClassifier(control ControlMatcher) Classifier {
	if control == nil {
		panic("ingress: control matcher is required")
	}
	return Classifier{control: control}
}

// NewOwnershipClassifier adds a deny-only matcher for previous verified
// Control namespaces. Retired paths are rejected in Ingress, before any
// Manager or Gateway proxy can receive Manager/Admin credentials.
func NewOwnershipClassifier(control ControlMatcher, retired ControlMatcher) Classifier {
	if control == nil {
		panic("ingress: control matcher is required")
	}
	return Classifier{control: control, retired: retired}
}

func (c Classifier) Classify(path string) Route {
	candidates := routeCandidates(path)
	if privateRoutingCandidate(candidates) {
		return RouteDeny
	}
	fixed, active, retired := c.candidateOwnership(candidates)
	return resolveOwnership(fixed, active, retired)
}

// ClassifyRequest inspects URL.Path, RawPath/EscapedPath, and RequestURI
// candidates before ownership is selected. Up to two unescapes are considered
// so encoded and double-encoded separators/dot segments cannot turn a
// Control-owned request into Gateway traffic after an intermediary normalizes
// it. Credential headers are deliberately not read here.
func (c Classifier) ClassifyRequest(r *http.Request) Route {
	if r == nil || r.URL == nil {
		return RouteGateway
	}
	if exactBrandResource(r) {
		return RouteManager
	}
	pathValue := r.URL.Path
	if pathValue == "" {
		pathValue = "/"
	}
	values := []string{pathValue, r.URL.RawPath, r.URL.EscapedPath(), r.RequestURI}
	fixed, active, retired := false, false, false
	for _, value := range values {
		if value == "" {
			continue
		}
		if index := strings.IndexByte(value, '?'); index >= 0 {
			value = value[:index]
		}
		candidates := routeCandidates(value)
		if privateRoutingCandidate(candidates) {
			return RouteDeny
		}
		candidateFixed, candidateActive, candidateRetired := c.candidateOwnership(candidates)
		fixed = fixed || candidateFixed
		active = active || candidateActive
		retired = retired || candidateRetired
	}
	return resolveOwnership(fixed, active, retired)
}

// The two brand exceptions are method-aware and intentionally do not use
// routeCandidates: encoded/subpath/lookalike requests keep normal ownership.
// Manager and fixture parity is checked across the stdlib module boundary.
func exactBrandResource(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if r.URL.Path != "/favicon.ico" && r.URL.Path != "/apple-touch-icon.png" {
		return false
	}
	if r.URL.EscapedPath() != r.URL.Path {
		return false
	}
	return r.RequestURI == "" || strings.SplitN(r.RequestURI, "?", 2)[0] == r.URL.Path
}

func (c Classifier) candidateOwnership(candidates []string) (fixed, active, retired bool) {
	for _, candidate := range candidates {
		fixed = fixed || fixedDiscoveryCandidate(candidate)
		active = active || c.control.Matches(candidate)
		retired = retired || c.retired != nil && c.retired.Matches(candidate)
	}
	return fixed, active, retired
}

func resolveOwnership(fixed, active, retired bool) Route {
	if fixed {
		return RouteManager
	}
	if retired {
		return RouteDeny
	}
	if active {
		return RouteManager
	}
	return RouteGateway
}

func routeCandidates(value string) []string {
	if value == "" {
		value = "/"
	}
	seen := make(map[string]struct{}, 10)
	result := make([]string, 0, 10)
	add := func(candidate string) {
		if candidate == "" {
			return
		}
		if _, ok := seen[candidate]; ok {
			return
		}
		seen[candidate] = struct{}{}
		result = append(result, candidate)
		normalized := path.Clean(strings.ReplaceAll(candidate, "\\", "/"))
		if normalized != candidate {
			if _, ok := seen[normalized]; !ok {
				seen[normalized] = struct{}{}
				result = append(result, normalized)
			}
		}
	}
	current := value
	for pass := 0; pass < 3; pass++ {
		add(current)
		decoded, err := url.PathUnescape(current)
		if err != nil || decoded == current {
			break
		}
		current = decoded
	}
	return result
}

func privateRoutingCandidate(candidates []string) bool {
	for _, value := range candidates {
		for _, private := range []string{FreshRoutingControlPath, FreshRoutingAuthorityPath} {
			if value == private || strings.HasPrefix(value, private+"/") {
				return true
			}
		}
	}
	return false
}

func fixedDiscoveryCandidate(value string) bool {
	if value == FixedBootstrapDiscoveryPath || strings.HasPrefix(value, FixedBootstrapDiscoveryPath+"/") || strings.HasPrefix(value, FixedBootstrapDiscoveryPath+"%") {
		return true
	}
	normalized := path.Clean(strings.ReplaceAll(value, "\\", "/"))
	return normalized == FixedBootstrapDiscoveryPath || strings.HasPrefix(normalized, FixedBootstrapDiscoveryPath+"/")
}

// NewTransitionalClassifier reflects only the Manager paths proven by the
// current v2 router. These paths are deployment compatibility evidence, not
// the long-term CPAMP public contract.
func NewTransitionalClassifier() Classifier {
	exact := map[string]struct{}{
		"/":                         {},
		"/health":                   {},
		"/management.html":          {},
		"/setup":                    {},
		"/status":                   {},
		FixedBootstrapDiscoveryPath: {},
	}
	prefixes := []string{
		"/usage-service",
		"/v0/management",
	}
	return NewClassifier(ControlMatcherFunc(func(path string) bool {
		if _, ok := exact[path]; ok {
			return true
		}
		for _, prefix := range prefixes {
			if path == prefix || len(path) > len(prefix) && path[:len(prefix)] == prefix && path[len(prefix)] == '/' {
				return true
			}
		}
		return false
	}))
}
