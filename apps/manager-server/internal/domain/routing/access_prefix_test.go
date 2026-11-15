package routing

import (
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

func TestValidateAccessPrefixPairEnforcesCanonicalAndReservedInvariants(t *testing.T) {
	if err := ValidateAccessPrefixPair("/cpamp-admin", "/cpamp-api", false, false); err != nil {
		t.Fatalf("distinct valid prefixes rejected: %v", err)
	}
	if err := ValidateAccessPrefixPair("/cpamp-admin", "", false, true); err != nil {
		t.Fatalf("valid external control prefix rejected: %v", err)
	}

	expectCode := func(control, inference string, shared, external bool, want bootstrap.AccessPrefixCode) {
		t.Helper()
		err := ValidateAccessPrefixPair(control, inference, shared, external)
		if err == nil {
			t.Fatalf("ValidateAccessPrefixPair(%q, %q, shared=%t, ext=%t) succeeded, want %s", control, inference, shared, external, want)
		}
		got, ok := bootstrap.AccessPrefixCodeOf(err)
		if !ok || got != want {
			t.Fatalf("ValidateAccessPrefixPair(%q, %q) code = %q (ok=%t), want %s (err=%v)", control, inference, got, ok, want, err)
		}
	}

	expectCode("", "/cpamp-api", false, false, bootstrap.CodeInvalidAccessPrefix)
	expectCode("/", "/cpamp-api", false, false, bootstrap.CodeInvalidAccessPrefix)
	expectCode("/cpamp-admin/", "/cpamp-api", false, false, bootstrap.CodeInvalidAccessPrefix)
	expectCode("/cpamp-admin", "", false, false, bootstrap.CodeInferencePrefixRequired)
	expectCode("/cpamp-admin", "relative/path", false, false, bootstrap.CodeInvalidAccessPrefix)

	for _, reserved := range []string{
		"/v0", "/v0/management", "/v8", "/v8/management",
		"/v1", "/v1/models", "/v1beta", "/openai", "/openai/v1",
		"/backend-api", "/backend-api/codex", "/api/provider", "/api/v2",
		"/callback", "/codex/callback", "/anthropic/callback", "/antigravity/callback", "/devin/callback",
		"/health", "/healthz", "/keepalive", "/management.html", "/assets",
		"/.well-known", "/.well-known/cpamp-bootstrap", "/internal", "/usage-service",
	} {
		expectCode(reserved, "/cpamp-api", false, false, bootstrap.CodeReservedPrefixCollision)
		expectCode("/cpamp-admin", reserved, false, false, bootstrap.CodeReservedPrefixCollision)
	}

	// Identical prefixes require explicit sharedEntry confirmation.
	expectCode("/shared-entry", "/shared-entry", false, false, bootstrap.CodeSharedPrefixConfirmationRequired)
	if err := ValidateAccessPrefixPair("/shared-entry", "/shared-entry", true, false); err != nil {
		t.Fatalf("explicitly confirmed sharedEntry rejected: %v", err)
	}
	expectCode("/cpamp-admin", "/cpamp-api", true, false, bootstrap.CodeSharedPrefixMismatch)

	// Nested prefixes are strictly forbidden even if sharedEntry is true.
	expectCode("/cpamp", "/cpamp/ai", false, false, bootstrap.CodePrefixOverlapForbidden)
	expectCode("/cpamp/admin", "/cpamp", false, false, bootstrap.CodePrefixOverlapForbidden)
	expectCode("/cpamp", "/cpamp/ai", true, false, bootstrap.CodeSharedPrefixMismatch)

	// External mode forbids managed inference prefixes and sharedEntry.
	expectCode("/cpamp-admin", "/cpamp-api", false, true, bootstrap.CodeExternalInferencePrefixForbidden)
	expectCode("/cpamp-admin", "", true, true, bootstrap.CodeExternalInferencePrefixForbidden)
}

func TestRoutingSnapshotTracksEffectiveInferenceBasePathAndSharedEntry(t *testing.T) {
	snap := RoutingSnapshot{
		State:                              SnapshotVerifiedEffective,
		DesiredControlBasePath:             "/cpamp-admin",
		VerifiedEffectiveControlBasePath:   "/cpamp-admin",
		DesiredInferenceBasePath:           "/cpamp-api",
		VerifiedEffectiveInferenceBasePath: "/cpamp-api",
		InferenceState:                     SnapshotVerifiedEffective,
		Revision:                           3,
	}
	if ctrl, ok := snap.EffectiveControlBasePath(); !ok || ctrl != "/cpamp-admin" {
		t.Fatalf("EffectiveControlBasePath = (%q, %t), want (/cpamp-admin, true)", ctrl, ok)
	}
	if inf, ok := snap.EffectiveInferenceBasePath(); !ok || inf != "/cpamp-api" {
		t.Fatalf("EffectiveInferenceBasePath = (%q, %t), want (/cpamp-api, true)", inf, ok)
	}

	// Pre-upgrade v2 installation missing inferenceBasePath keeps control open and inference closed.
	missingInf := RoutingSnapshot{
		State:                              SnapshotVerifiedEffective,
		DesiredControlBasePath:             "/cpamp-admin",
		VerifiedEffectiveControlBasePath:   "/cpamp-admin",
		InferenceState:                     SnapshotUnavailable,
		MissingInferenceCompletionRequired: true,
		Revision:                           1,
	}
	if ctrl, ok := missingInf.EffectiveControlBasePath(); !ok || ctrl != "/cpamp-admin" {
		t.Fatalf("missing-inference control = (%q, %t), want (/cpamp-admin, true)", ctrl, ok)
	}
	if inf, ok := missingInf.EffectiveInferenceBasePath(); ok || inf != "" {
		t.Fatalf("missing-inference EffectiveInferenceBasePath = (%q, %t), want (\"\", false)", inf, ok)
	}

	// Shared-entry snapshot validates only when SharedEntry is true.
	shared := RoutingSnapshot{
		State:                              SnapshotVerifiedEffective,
		DesiredControlBasePath:             "/cpamp",
		VerifiedEffectiveControlBasePath:   "/cpamp",
		DesiredInferenceBasePath:           "/cpamp",
		VerifiedEffectiveInferenceBasePath: "/cpamp",
		InferenceState:                     SnapshotVerifiedEffective,
		SharedEntry:                        false,
		Revision:                           2,
	}
	if err := shared.Validate(); err == nil {
		t.Fatal("shared prefix without SharedEntry passed validation")
	}
	shared.SharedEntry = true
	if inf, ok := shared.EffectiveInferenceBasePath(); !ok || inf != "/cpamp" {
		t.Fatalf("shared EffectiveInferenceBasePath = (%q, %t), want (/cpamp, true)", inf, ok)
	}
}

func TestGenerateSuggestedPrefixPairProducesDistinctNonReservedCanonicalPrefixes(t *testing.T) {
	for range 16 {
		ctrl, inf, err := bootstrap.GenerateSuggestedPrefixPair(ReservedNamespaceCollision)
		if err != nil {
			t.Fatalf("GenerateSuggestedPrefixPair failed: %v", err)
		}
		if ctrl == inf || len(ctrl) != 17 || len(inf) != 17 {
			t.Fatalf("unexpected generated pair (%q, %q)", ctrl, inf)
		}
		if err := ValidateAccessPrefixPair(ctrl, inf, false, false); err != nil {
			t.Fatalf("generated pair (%q, %q) failed validation: %v", ctrl, inf, err)
		}
	}
}
