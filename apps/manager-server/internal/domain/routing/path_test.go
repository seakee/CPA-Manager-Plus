package routing

import (
	"net/url"
	"testing"

	bootstrapdomain "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

func TestControlBasePathRequiresStableURLSerialization(t *testing.T) {
	for _, value := range []string{"/控制", "/café", "/😀", "/control[admin]", "/control!", "/control%", "/control\"", "/control`", "/control{admin}"} {
		snapshot := RoutingSnapshot{State: SnapshotVerifiedEffective, VerifiedEffectiveControlBasePath: value, Revision: 1}
		if bootstrapdomain.ValidControlBasePath(value) || ValidControlBasePath(value) || snapshot.Validate() == nil || snapshot.PublicAPIActive() {
			t.Fatalf("non-serializable routing authority accepted: %q", value)
		}
		for _, owner := range []NamespaceOwner{
			{Snapshot: snapshot},
			{Snapshot: RoutingSnapshot{State: SnapshotUnknown}, Retired: []RetiredNamespace{{ControlBasePath: value, Revision: 1}}},
		} {
			if owner.Owner(value+"/api/v2/probe") != OwnerAmbiguousControl || owner.Owner("/v1/models") != OwnerGateway {
				t.Fatalf("invalid declared authority lost deny-only ownership: %q", value)
			}
		}
	}
	for _, value := range []string{"/phase4-test", "/control/sub-path", "/AZaz09-._~$&+,/:;=@"} {
		if !bootstrapdomain.ValidControlBasePath(value) || !ValidControlBasePath(value) || (&url.URL{Path: value}).EscapedPath() != value {
			t.Fatalf("canonical prefix changed during URL serialization: %q", value)
		}
	}
}

func verifiedSnapshot() RoutingSnapshot {
	return RoutingSnapshot{
		DesiredControlBasePath:           "/control-new",
		VerifiedEffectiveControlBasePath: "/control-old",
		Revision:                         7,
		State:                            SnapshotVerifiedEffective,
	}
}

func TestRoutingSnapshotOnlyVerifiedEffectiveActivatesPublicAPI(t *testing.T) {
	tests := []struct {
		name   string
		snap   RoutingSnapshot
		active bool
	}{
		{
			name:   "verified effective",
			snap:   verifiedSnapshot(),
			active: true,
		},
		{
			name: "desired only",
			snap: RoutingSnapshot{
				DesiredControlBasePath: "/control-new",
				Revision:               7,
				State:                  SnapshotDesiredOnly,
			},
		},
		{
			name: "stale effective",
			snap: RoutingSnapshot{
				VerifiedEffectiveControlBasePath: "/control-old",
				Revision:                         7,
				State:                            SnapshotStale,
			},
		},
		{
			name: "unknown",
			snap: RoutingSnapshot{State: SnapshotUnknown},
		},
		{
			name: "unavailable",
			snap: RoutingSnapshot{State: SnapshotUnavailable},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.snap.PublicAPIActive(); got != tt.active {
				t.Fatalf("PublicAPIActive() = %v, want %v", got, tt.active)
			}
			base, ok := tt.snap.CanonicalPublicAPIBase()
			if tt.active {
				if !ok || base != "/control-old/api/v2" {
					t.Fatalf("canonical API base = %q, %v", base, ok)
				}
			} else if ok || base != "" {
				t.Fatalf("inactive snapshot exposed public API base = %q, %v", base, ok)
			}
		})
	}
}

func TestRoutingSnapshotDoesNotTreatDesiredPathAsEffective(t *testing.T) {
	snapshot := RoutingSnapshot{
		DesiredControlBasePath:           "/desired",
		VerifiedEffectiveControlBasePath: "/effective",
		Revision:                         4,
		State:                            SnapshotVerifiedEffective,
	}
	base, ok := snapshot.CanonicalPublicAPIPath("bootstrap/status")
	if !ok || base != "/effective/api/v2/bootstrap/status" {
		t.Fatalf("canonical path = %q, %v", base, ok)
	}
}

func TestNamespaceOwnerKeepsRetiredControlOutOfGateway(t *testing.T) {
	snapshot := verifiedSnapshot()
	owner := NamespaceOwner{
		Snapshot: snapshot,
		Retired:  []RetiredNamespace{{ControlBasePath: "/retired", Revision: 6}},
	}
	if err := owner.Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		want PathOwner
	}{
		{BootstrapDiscoveryPath, OwnerManager},
		{BootstrapDiscoveryPath + "/extra", OwnerAmbiguousControl},
		{"/control-old", OwnerManager},
		{"/control-old/api/v2/models", OwnerManager},
		{"/control-new/api/v2/models", OwnerAmbiguousControl},
		{"/retired", OwnerRetiredControl},
		{"/retired/api/v2/models", OwnerRetiredControl},
		{"/retiredish/api/v2/models", OwnerGateway},
		{"/v1/responses", OwnerGateway},
		{"/control-old//api/v2", OwnerAmbiguousControl},
		{"/control-old/../v1/responses", OwnerAmbiguousControl},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := owner.Owner(tt.path); got != tt.want {
				t.Fatalf("Owner(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestNamespaceOwnerRejectsRetiredReuseAndNestedCollision(t *testing.T) {
	tests := []struct {
		name    string
		retired []RetiredNamespace
	}{
		{
			name:    "active reuse",
			retired: []RetiredNamespace{{ControlBasePath: "/control-old", Revision: 8}},
		},
		{
			name:    "desired nested",
			retired: []RetiredNamespace{{ControlBasePath: "/control-new/child", Revision: 8}},
		},
		{
			name: "retired nested",
			retired: []RetiredNamespace{
				{ControlBasePath: "/retired", Revision: 8},
				{ControlBasePath: "/retired/child", Revision: 9},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner := NamespaceOwner{Snapshot: verifiedSnapshot(), Retired: tt.retired}
			if err := owner.Validate(); err == nil {
				t.Fatal("namespace collision accepted")
			}
		})
	}
}

func TestPreviousVerifiedPathMayBecomeOneRetiredNamespace(t *testing.T) {
	snapshot := RoutingSnapshot{
		DesiredControlBasePath:           "/control-new",
		VerifiedEffectiveControlBasePath: "/control-new",
		PreviousVerifiedControlBasePath:  "/control-old",
		Revision:                         8,
		State:                            SnapshotVerifiedEffective,
	}
	owner := NamespaceOwner{
		Snapshot: snapshot,
		Retired:  []RetiredNamespace{{ControlBasePath: "/control-old", Revision: 7}},
	}
	if err := owner.Validate(); err != nil {
		t.Fatalf("previous path handoff rejected: %v", err)
	}
}

func TestPreviousVerifiedNamespaceIsDenyOnlyWithoutRetiredEntry(t *testing.T) {
	for _, state := range []SnapshotState{SnapshotVerifiedEffective, SnapshotDesiredOnly, SnapshotStale, SnapshotUnknown, SnapshotUnavailable} {
		t.Run(string(state), func(t *testing.T) {
			snapshot := RoutingSnapshot{State: state, PreviousVerifiedControlBasePath: "/previous", Revision: 8}
			switch state {
			case SnapshotVerifiedEffective, SnapshotStale:
				snapshot.VerifiedEffectiveControlBasePath = "/current"
			case SnapshotDesiredOnly:
				snapshot.DesiredControlBasePath = "/desired"
			}
			owner := NamespaceOwner{Snapshot: snapshot}
			if err := owner.Validate(); err != nil {
				t.Fatal(err)
			}
			for _, item := range []struct {
				path string
				want PathOwner
			}{
				{"/previous", OwnerRetiredControl},
				{"/previous/api/v2/models", OwnerRetiredControl},
				{"/previous/control-plane", OwnerRetiredControl},
				{"/previousish/api/v2/models", OwnerGateway},
				{"/v1/responses", OwnerGateway},
				{"/previous%2fapi", OwnerAmbiguousControl},
				{"/previous/../v1/responses", OwnerAmbiguousControl},
			} {
				if got := owner.Owner(item.path); got != item.want {
					t.Errorf("Owner(%q) = %q, want %q", item.path, got, item.want)
				}
			}
			if state != SnapshotVerifiedEffective && snapshot.PublicAPIActive() {
				t.Fatal("previous namespace granted public API activation")
			}
		})
	}
}

func TestPreviousVerifiedNamespaceCollisionFailsClosedWithoutRetiredEntry(t *testing.T) {
	for _, previous := range []string{"/current", "/current/child", "/current%2fchild"} {
		owner := NamespaceOwner{Snapshot: RoutingSnapshot{
			State: SnapshotVerifiedEffective, Revision: 8,
			VerifiedEffectiveControlBasePath: "/current", PreviousVerifiedControlBasePath: previous,
		}}
		if owner.Validate() == nil || owner.Owner(previous) != OwnerAmbiguousControl || owner.Snapshot.PublicAPIActive() {
			t.Errorf("invalid previous namespace %q did not fail closed", previous)
		}
	}
}

func TestInvalidRoutingAuthorityDoesNotCaptureUnrelatedPaths(t *testing.T) {
	owner := NamespaceOwner{
		Snapshot: RoutingSnapshot{
			DesiredControlBasePath:           "/control",
			VerifiedEffectiveControlBasePath: "/control/child",
			Revision:                         9,
			State:                            SnapshotVerifiedEffective,
		},
		Retired: []RetiredNamespace{{ControlBasePath: "/retired", Revision: 0}},
	}
	if owner.Validate() == nil {
		t.Fatal("invalid routing authority unexpectedly validated")
	}
	for _, item := range []struct {
		path string
		want PathOwner
	}{
		{BootstrapDiscoveryPath, OwnerManager},
		{"/control", OwnerAmbiguousControl},
		{"/control/child/api/v2", OwnerAmbiguousControl},
		{"/retired/settings", OwnerAmbiguousControl},
		{"/v1/responses", OwnerGateway},
		{"/models", OwnerGateway},
	} {
		if got := owner.Owner(item.path); got != item.want {
			t.Errorf("Owner(%q) = %q, want %q", item.path, got, item.want)
		}
	}
}

func TestReservedControlCollisionFailsClosedOnlyForClaimedNamespace(t *testing.T) {
	owner := NamespaceOwner{Snapshot: RoutingSnapshot{
		DesiredControlBasePath:           "/v1",
		VerifiedEffectiveControlBasePath: "/v1",
		Revision:                         1,
		State:                            SnapshotVerifiedEffective,
	}}
	if owner.Validate() == nil {
		t.Fatal("reserved control namespace unexpectedly validated")
	}
	if got := owner.Owner("/v1/responses"); got != OwnerAmbiguousControl {
		t.Fatalf("reserved claimed namespace owner = %q, want ambiguous control", got)
	}
	if got := owner.Owner("/models"); got != OwnerGateway {
		t.Fatalf("unrelated Data Plane owner = %q, want gateway", got)
	}
	if got := owner.Owner(BootstrapDiscoveryPath); got != OwnerManager {
		t.Fatalf("fixed discovery owner = %q, want manager", got)
	}
}

func TestCanonicalPathRejectsAmbiguousForms(t *testing.T) {
	for _, value := range []string{
		"",
		"control",
		"/",
		"/control/",
		"/control//api",
		"/control/./api",
		"/control/../api",
		"/control%2fapi",
		"/control\\api",
		"/control?x=1",
	} {
		if CanonicalRequestPath(value) {
			t.Fatalf("ambiguous request path accepted: %q", value)
		}
	}
	for _, value := range []string{"/control", "/control/api/v2", "/.well-known/cpamp-bootstrap"} {
		if !CanonicalRequestPath(value) {
			t.Fatalf("canonical request path rejected: %q", value)
		}
	}
}

func TestCanonicalPublicAPIPathRejectsParallelNamespaceAndTraversal(t *testing.T) {
	snapshot := verifiedSnapshot()
	for _, value := range []string{"/api/v2/models", "models//list", "models/../admin", "models%2fadmin", "models?x=1"} {
		if path, ok := snapshot.CanonicalPublicAPIPath(value); ok || path != "" {
			t.Fatalf("unsafe API suffix accepted: %q -> %q, %v", value, path, ok)
		}
	}
}

func TestRoutingSnapshotRejectsReservedNamespaceCollision(t *testing.T) {
	for _, value := range []string{
		"/v1",
		"/v1/control",
		"/models",
		"/health/private",
		BootstrapDiscoveryPath,
		"/usage-service",
		"/internal",
		"/internal/routing",
		"/internal/routing/fresh",
		"/internal/routing/fresh-authority",
	} {
		snapshot := RoutingSnapshot{
			DesiredControlBasePath:           value,
			VerifiedEffectiveControlBasePath: value,
			Revision:                         1,
			State:                            SnapshotVerifiedEffective,
		}
		if err := snapshot.Validate(); err == nil {
			t.Fatalf("reserved namespace accepted: %q", value)
		}
	}
}
