// Package routing contains the read-only route ownership contracts shared by
// the Manager HTTP layer and later Public API consumers. It deliberately does
// not load configuration, persist a prefix, or apply a route change.
package routing

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"unicode"

	bootstrapdomain "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

// BootstrapDiscoveryPath is the only fixed unauthenticated Bootstrap
// discovery carrier. Keep this value in sync with the stdlib-only Ingress
// contract and the checked-in route-owner fixture.
const BootstrapDiscoveryPath = bootstrapdomain.FixedBootstrapDiscoveryPath

// PublicAPIV2Suffix is the only permanent CPAMP Public API namespace. It is a
// child of a verified effective controlBasePath; it is never mounted at root.
const PublicAPIV2Suffix = "/api/v2"

// SnapshotState describes the authority carried by a routing snapshot.
type SnapshotState string

const (
	// SnapshotVerifiedEffective is the only state that can activate a public
	// API mount. The effective path is the currently verified route, even if a
	// different desired path is awaiting a later apply operation.
	SnapshotVerifiedEffective SnapshotState = "verified_effective"
	SnapshotDesiredOnly       SnapshotState = "desired_only"
	SnapshotStale             SnapshotState = "stale"
	SnapshotUnknown           SnapshotState = "unknown"
	SnapshotUnavailable       SnapshotState = "unavailable"
)

func validSnapshotState(state SnapshotState) bool {
	switch state {
	case SnapshotVerifiedEffective, SnapshotDesiredOnly, SnapshotStale,
		SnapshotUnknown, SnapshotUnavailable:
		return true
	default:
		return false
	}
}

// RoutingSnapshot is a Manager-owned, read-only authority projection. Desired
// state and request-derived values are intentionally retained as separate
// fields so consumers cannot accidentally treat either as effective routing.
type RoutingSnapshot struct {
	DesiredControlBasePath           string
	VerifiedEffectiveControlBasePath string
	PreviousVerifiedControlBasePath  string
	Revision                         uint64
	State                            SnapshotState
}

// Validate checks the shape and authority state without consulting any
// external source. It does not decide whether a desired route may be applied.
func (s RoutingSnapshot) Validate() error {
	if !validSnapshotState(s.State) {
		return fmt.Errorf("unknown routing snapshot state %q", s.State)
	}
	if s.DesiredControlBasePath != "" && !ValidControlBasePath(s.DesiredControlBasePath) {
		return errors.New("desired controlBasePath is not canonical")
	}
	if s.VerifiedEffectiveControlBasePath != "" && !ValidControlBasePath(s.VerifiedEffectiveControlBasePath) {
		return errors.New("verified effective controlBasePath is not canonical")
	}
	if s.PreviousVerifiedControlBasePath != "" && !ValidControlBasePath(s.PreviousVerifiedControlBasePath) {
		return errors.New("previous verified controlBasePath is not canonical")
	}
	for _, candidate := range []string{
		s.DesiredControlBasePath,
		s.VerifiedEffectiveControlBasePath,
		s.PreviousVerifiedControlBasePath,
	} {
		if candidate != "" && reservedNamespaceCollision(candidate) {
			return fmt.Errorf("controlBasePath collides with reserved path: %q", candidate)
		}
	}
	switch s.State {
	case SnapshotVerifiedEffective:
		if s.VerifiedEffectiveControlBasePath == "" || s.Revision == 0 {
			return errors.New("verified effective snapshot requires path and revision")
		}
	case SnapshotDesiredOnly:
		if s.DesiredControlBasePath == "" || s.VerifiedEffectiveControlBasePath != "" {
			return errors.New("desired-only snapshot requires desired path and no effective path")
		}
	case SnapshotStale:
		if s.VerifiedEffectiveControlBasePath == "" || s.Revision == 0 {
			return errors.New("stale snapshot requires the last effective path and revision")
		}
	case SnapshotUnknown, SnapshotUnavailable:
		if s.VerifiedEffectiveControlBasePath != "" {
			return errors.New("unknown or unavailable snapshot cannot carry effective authority")
		}
	}
	if err := ValidateNamespaceCollisions(s, nil); err != nil {
		return err
	}
	return nil
}

// PublicAPIActive reports whether the canonical public namespace is allowed
// to be mounted. A valid desired path, Host header, browser value, or stale
// effective path never activates it.
func (s RoutingSnapshot) PublicAPIActive() bool {
	return s.Validate() == nil && s.State == SnapshotVerifiedEffective
}

// EffectiveControlBasePath returns the only path that may be used by a public
// route consumer. The empty result is an intentional fail-closed state.
func (s RoutingSnapshot) EffectiveControlBasePath() (string, bool) {
	if !s.PublicAPIActive() {
		return "", false
	}
	return s.VerifiedEffectiveControlBasePath, true
}

// CanonicalPublicAPIBase returns <controlBasePath>/api/v2 only when the
// snapshot is authoritative. It does not create a production mount.
func (s RoutingSnapshot) CanonicalPublicAPIBase() (string, bool) {
	prefix, ok := s.EffectiveControlBasePath()
	if !ok {
		return "", false
	}
	return prefix + PublicAPIV2Suffix, true
}

// CanonicalPublicAPIPath appends one normalized resource path to the canonical
// API base. The resource may be empty for the namespace root.
func (s RoutingSnapshot) CanonicalPublicAPIPath(resource string) (string, bool) {
	base, ok := s.CanonicalPublicAPIBase()
	if !ok {
		return "", false
	}
	if resource == "" {
		return base, true
	}
	if strings.HasPrefix(resource, "/") || !ValidRouteSuffix(resource) {
		return "", false
	}
	return base + "/" + resource, true
}

// RetiredNamespace describes one previous verified Control namespace. It is
// an in-memory deny contract for later route switching; it is not a history
// registry and carries no credential or forwarding behavior.
type RetiredNamespace struct {
	ControlBasePath string
	Revision        uint64
}

func (r RetiredNamespace) Validate() error {
	if !ValidControlBasePath(r.ControlBasePath) {
		return errors.New("retired controlBasePath is not canonical")
	}
	if r.Revision == 0 {
		return errors.New("retired namespace requires a routing revision")
	}
	if reservedNamespaceCollision(r.ControlBasePath) {
		return errors.New("retired controlBasePath collides with a reserved path")
	}
	return nil
}

// IsRetiredPath is segment-boundary aware. It does not clean, decode, or
// reinterpret a request path; callers must reject ambiguous request-targets
// before passing them to this exact-path predicate.
func (r RetiredNamespace) IsRetiredPath(requestPath string) bool {
	return r.Validate() == nil && isSegmentPath(requestPath, r.ControlBasePath)
}

// PathOwner is the owner result for a request after canonical route checks.
// Retired and ambiguous Control candidates remain Manager-owned so they can
// be denied locally; they never fall through to Gateway.
type PathOwner string

const (
	OwnerGateway          PathOwner = "gateway"
	OwnerManager          PathOwner = "manager"
	OwnerRetiredControl   PathOwner = "retired_control"
	OwnerAmbiguousControl PathOwner = "ambiguous_control"
)

// NamespaceOwner applies only the route contract. It intentionally does not
// inspect credentials, Host, forwarding headers, browser state, or desired
// configuration.
type NamespaceOwner struct {
	Snapshot RoutingSnapshot
	Retired  []RetiredNamespace
}

func (o NamespaceOwner) Validate() error {
	if err := o.Snapshot.Validate(); err != nil {
		return err
	}
	for _, retired := range o.Retired {
		if err := retired.Validate(); err != nil {
			return err
		}
		if o.Snapshot.VerifiedEffectiveControlBasePath != "" &&
			retired.ControlBasePath == o.Snapshot.VerifiedEffectiveControlBasePath {
			return errors.New("retired namespace collides with active effective namespace")
		}
	}
	if err := ValidateNamespaceCollisions(o.Snapshot, o.Retired); err != nil {
		return err
	}
	return nil
}

// Owner classifies already-normalized paths. It returns a Control owner for
// the fixed discovery carrier even when no effective prefix authority exists.
func (o NamespaceOwner) Owner(requestPath string) PathOwner {
	if !CanonicalRequestPath(requestPath) {
		return OwnerAmbiguousControl
	}
	// The fixed discovery carrier has an owner independent of dynamic routing
	// authority and must not be hidden by an unrelated snapshot conflict.
	if requestPath == BootstrapDiscoveryPath {
		return OwnerManager
	}
	if strings.HasPrefix(requestPath, BootstrapDiscoveryPath+"/") {
		return OwnerAmbiguousControl
	}
	if o.Validate() != nil {
		// Invalid/conflicting routing authority fails closed only inside the
		// namespaces it actually claims. Explicitly unrelated Data Plane paths
		// keep the existing Gateway catch-all.
		if o.matchesDeclaredControlNamespace(requestPath) {
			return OwnerAmbiguousControl
		}
		return OwnerGateway
	}
	if o.Snapshot.VerifiedEffectiveControlBasePath != "" &&
		isSegmentPath(requestPath, o.Snapshot.VerifiedEffectiveControlBasePath) {
		if o.Snapshot.State != SnapshotVerifiedEffective {
			return OwnerAmbiguousControl
		}
		return OwnerManager
	}
	if o.Snapshot.DesiredControlBasePath != "" &&
		o.Snapshot.DesiredControlBasePath != o.Snapshot.VerifiedEffectiveControlBasePath &&
		isSegmentPath(requestPath, o.Snapshot.DesiredControlBasePath) {
		// A desired route is not authority for activation, but treating its
		// candidate namespace as ambiguous prevents a credential-bearing
		// request from falling into the Gateway while an apply is pending.
		return OwnerAmbiguousControl
	}
	if o.Snapshot.PreviousVerifiedControlBasePath != "" &&
		isSegmentPath(requestPath, o.Snapshot.PreviousVerifiedControlBasePath) {
		// Previous verified authority is inherently deny-only. Callers need not
		// duplicate it in Retired to keep its namespace out of the Gateway.
		return OwnerRetiredControl
	}
	for _, retired := range o.Retired {
		if retired.IsRetiredPath(requestPath) {
			return OwnerRetiredControl
		}
	}
	return OwnerGateway
}

func (o NamespaceOwner) matchesDeclaredControlNamespace(requestPath string) bool {
	// Invalid authority cannot activate, but its literally claimed namespace
	// remains deny-only even if it no longer meets the activation grammar.
	for _, candidate := range []string{
		o.Snapshot.DesiredControlBasePath,
		o.Snapshot.VerifiedEffectiveControlBasePath,
		o.Snapshot.PreviousVerifiedControlBasePath,
	} {
		if candidate != "" && validDeclaredControlBasePath(candidate) && isSegmentPath(requestPath, candidate) {
			return true
		}
	}
	for _, retired := range o.Retired {
		if retired.ControlBasePath != "" && validDeclaredControlBasePath(retired.ControlBasePath) &&
			isSegmentPath(requestPath, retired.ControlBasePath) {
			return true
		}
	}
	return false
}

// ValidateNamespaceCollisions rejects reuse or prefix nesting between the
// active/desired/previous namespace and retired namespaces. A collision is a
// fail-closed contract error rather than an instruction to choose a winner.
func ValidateNamespaceCollisions(snapshot RoutingSnapshot, retired []RetiredNamespace) error {
	if snapshot.DesiredControlBasePath != "" && snapshot.VerifiedEffectiveControlBasePath != "" &&
		snapshot.DesiredControlBasePath != snapshot.VerifiedEffectiveControlBasePath &&
		sameOrNestedPrefix(snapshot.DesiredControlBasePath, snapshot.VerifiedEffectiveControlBasePath) {
		return fmt.Errorf("routing namespace collision between desired %q and effective %q", snapshot.DesiredControlBasePath, snapshot.VerifiedEffectiveControlBasePath)
	}
	if snapshot.PreviousVerifiedControlBasePath != "" {
		for _, current := range []string{snapshot.DesiredControlBasePath, snapshot.VerifiedEffectiveControlBasePath} {
			if current != "" && sameOrNestedPrefix(current, snapshot.PreviousVerifiedControlBasePath) {
				return fmt.Errorf("routing namespace collision between current %q and previous %q", current, snapshot.PreviousVerifiedControlBasePath)
			}
		}
	}
	paths := make([]string, 0, len(retired)+3)
	for _, candidate := range []string{
		snapshot.DesiredControlBasePath,
		snapshot.VerifiedEffectiveControlBasePath,
		snapshot.PreviousVerifiedControlBasePath,
	} {
		if candidate != "" {
			paths = append(paths, candidate)
		}
	}
	for _, item := range retired {
		if item.ControlBasePath == "" {
			continue
		}
		for _, existing := range paths {
			// The previous verified path is expected to become a retired
			// namespace during a later successful switch. That exact handoff is
			// representation, not a collision; nested or reused paths remain
			// fail-closed.
			if existing == snapshot.PreviousVerifiedControlBasePath && existing == item.ControlBasePath {
				continue
			}
			if sameOrNestedPrefix(existing, item.ControlBasePath) {
				return fmt.Errorf("routing namespace collision between %q and %q", existing, item.ControlBasePath)
			}
		}
		paths = append(paths, item.ControlBasePath)
	}
	return nil
}

// ValidControlBasePath delegates to the same grammar as Bootstrap prefix and
// preview contracts. Routing does not define another canonical prefix grammar.
func ValidControlBasePath(value string) bool {
	return bootstrapdomain.ValidControlBasePath(value)
}

// Preserve Phase4-01's decoded namespace shape for deny-only ownership of an
// invalid snapshot; narrowing activation must not release old claimed routes.
func validDeclaredControlBasePath(value string) bool {
	if value == "" || value == "/" || !strings.HasPrefix(value, "/") ||
		strings.TrimSpace(value) != value || strings.Contains(value, "//") ||
		strings.ContainsAny(value, "?#\\") || containsPercentEncoding(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return path.Clean(value) == value
}

// CanonicalRequestPath accepts only the already-decoded, canonical path form
// used by the Manager owner. HTTP request handlers must separately inspect
// RawPath/EscapedPath and reject ambiguous encodings before calling it.
func CanonicalRequestPath(value string) bool {
	if value == "" || !strings.HasPrefix(value, "/") || strings.Contains(value, "%") ||
		strings.ContainsAny(value, "\\?#") || strings.Contains(value, "//") {
		return false
	}
	if path.Clean(value) != value {
		return false
	}
	segments := strings.Split(value, "/")
	for index, segment := range segments {
		if index == 0 && segment == "" {
			continue
		}
		if segment == "." || segment == ".." || segment == "" {
			return false
		}
	}
	return true
}

// ValidRouteSuffix only permits path segments, not query, fragment, escaping,
// or dot traversal. It is used for constructing API paths, not request routing.
func ValidRouteSuffix(value string) bool {
	if value == "" || strings.ContainsAny(value, "?#\\%") || strings.Contains(value, "//") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func isSegmentPath(requestPath, prefix string) bool {
	return requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/")
}

func sameOrNestedPrefix(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func reservedNamespaceCollision(candidate string) bool {
	for _, reserved := range []string{
		BootstrapDiscoveryPath,
		"/favicon.ico",
		"/apple-touch-icon.png",
		"/health",
		"/management.html",
		"/models",
		"/setup",
		"/status",
		"/v0",
		"/v1",
		"/usage-service",
		"/internal/routing/fresh",
		"/internal/routing/fresh-authority",
	} {
		if sameOrNestedPrefix(candidate, reserved) {
			return true
		}
	}
	return false
}

func containsPercentEncoding(value string) bool {
	for i := 0; i+2 < len(value); i++ {
		if value[i] == '%' {
			if _, err := url.PathUnescape(value[i : i+3]); err == nil {
				return true
			}
		}
	}
	return false
}
