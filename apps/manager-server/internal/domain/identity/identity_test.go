package identity_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
)

func TestCanonicalIDGenerationAndValidation(t *testing.T) {
	// APIKeyID generation shape
	apiKeyID, err := identity.NewAPIKeyID()
	if err != nil {
		t.Fatalf("unexpected error generating APIKeyID: %v", err)
	}
	if len(apiKeyID) != 32 {
		t.Fatalf("APIKeyID length = %d, want 32", len(apiKeyID))
	}
	if err := apiKeyID.Validate(); err != nil {
		t.Fatalf("generated APIKeyID failed validation: %v", err)
	}

	// CredentialID generation shape
	credID, err := identity.NewCredentialID()
	if err != nil {
		t.Fatalf("unexpected error generating CredentialID: %v", err)
	}
	if len(credID) != 32 {
		t.Fatalf("CredentialID length = %d, want 32", len(credID))
	}
	if err := credID.Validate(); err != nil {
		t.Fatalf("generated CredentialID failed validation: %v", err)
	}

	// Uniqueness across many generated IDs
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id, err := identity.NewAPIKeyID()
		if err != nil {
			t.Fatalf("generate APIKeyID #%d: %v", i, err)
		}
		str := id.String()
		if seen[str] {
			t.Fatalf("duplicate APIKeyID generated: %s", str)
		}
		seen[str] = true
	}
	for i := 0; i < 1000; i++ {
		id, err := identity.NewCredentialID()
		if err != nil {
			t.Fatalf("generate CredentialID #%d: %v", i, err)
		}
		str := id.String()
		if seen[str] {
			t.Fatalf("duplicate CredentialID generated: %s", str)
		}
		seen[str] = true
	}

	// Invalid ID rejection
	testCases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"31 chars", strings.Repeat("a", 31)},
		{"33 chars", strings.Repeat("a", 33)},
		{"uppercase hex", "6F59194528584ABDB0BF21A38A490BF2"},
		{"mixed case", "6f59194528584abdb0bf21a38a490bF2"},
		{"non-hex chars", "6f59194528584abdb0bf21a38a490bgz"},
		{"leading space", " 6f59194528584abdb0bf21a38a490bf2"},
		{"trailing space", "6f59194528584abdb0bf21a38a490bf2 "},
		{"internal newline", "6f59194528584ab\ndb0bf21a38a490bf2"},
	}

	for _, tc := range testCases {
		t.Run("APIKeyID_"+tc.name, func(t *testing.T) {
			parsed, err := identity.ParseAPIKeyID(tc.raw)
			if err == nil {
				t.Fatalf("expected error for %q, got valid parsed %s", tc.raw, parsed)
			}
			if !errors.Is(err, identity.ErrInvalidAPIKeyID) {
				t.Fatalf("expected ErrInvalidAPIKeyID, got %v", err)
			}
			if err := identity.APIKeyID(tc.raw).Validate(); err == nil {
				t.Fatalf("expected Validate error for %q", tc.raw)
			}
		})
		t.Run("CredentialID_"+tc.name, func(t *testing.T) {
			parsed, err := identity.ParseCredentialID(tc.raw)
			if err == nil {
				t.Fatalf("expected error for %q, got valid parsed %s", tc.raw, parsed)
			}
			if !errors.Is(err, identity.ErrInvalidCredentialID) {
				t.Fatalf("expected ErrInvalidCredentialID, got %v", err)
			}
			if err := identity.CredentialID(tc.raw).Validate(); err == nil {
				t.Fatalf("expected Validate error for %q", tc.raw)
			}
		})
	}
}

func TestCanonicalLifecycleTransitions(t *testing.T) {
	// Permitted transitions
	permitted := [][2]identity.Lifecycle{
		{identity.LifecycleActive, identity.LifecycleMissing},
		{identity.LifecycleActive, identity.LifecycleSuperseded},
		{identity.LifecycleMissing, identity.LifecycleActive},
		{identity.LifecycleMissing, identity.LifecycleSuperseded},
		{identity.LifecycleActive, identity.LifecycleActive},
		{identity.LifecycleMissing, identity.LifecycleMissing},
	}
	for _, pair := range permitted {
		from, to := pair[0], pair[1]
		if err := identity.ValidateTransition(from, to); err != nil {
			t.Errorf("transition %s -> %s should be permitted, got error: %v", from, to, err)
		}
	}

	// Forbidden transitions from terminal superseded
	forbidden := [][2]identity.Lifecycle{
		{identity.LifecycleSuperseded, identity.LifecycleActive},
		{identity.LifecycleSuperseded, identity.LifecycleMissing},
		{identity.LifecycleSuperseded, identity.LifecycleSuperseded},
	}
	for _, pair := range forbidden {
		from, to := pair[0], pair[1]
		err := identity.ValidateTransition(from, to)
		if err == nil {
			t.Errorf("transition %s -> %s should be forbidden, got nil", from, to)
		}
		if !errors.Is(err, identity.ErrInvalidLifecycleTransition) {
			t.Errorf("expected ErrInvalidLifecycleTransition, got %v", err)
		}
	}

	// Unknown or invalid lifecycle values
	invalidLifecycles := []identity.Lifecycle{
		"",
		"disabled",
		"orphaned",
		"deleted",
		"ACTIVE",
	}
	for _, lc := range invalidLifecycles {
		if lc.IsValid() {
			t.Errorf("expected lifecycle %q to be invalid", lc)
		}
		if err := identity.ValidateTransition(lc, identity.LifecycleActive); err == nil {
			t.Errorf("expected error transitioning from invalid lifecycle %q", lc)
		}
		if err := identity.ValidateTransition(identity.LifecycleActive, lc); err == nil {
			t.Errorf("expected error transitioning to invalid lifecycle %q", lc)
		}
	}
}

func TestAPIKeyIdentityValidation(t *testing.T) {
	validID := identity.APIKeyID("0123456789abcdef0123456789abcdef")

	validEntity := identity.APIKeyIdentity{
		ID:          validID,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 1000,
		UpdatedAtMS: 1000,
	}
	if err := validEntity.Validate(); err != nil {
		t.Fatalf("valid entity validation failed: %v", err)
	}

	// Revision out of range
	invalidRevZero := validEntity
	invalidRevZero.Revision = 0
	if err := invalidRevZero.Validate(); !errors.Is(err, identity.ErrRevisionOutOfRange) {
		t.Errorf("expected ErrRevisionOutOfRange for rev 0, got %v", err)
	}

	invalidRevOverflow := validEntity
	invalidRevOverflow.Revision = math.MaxInt64 + 1
	if err := invalidRevOverflow.Validate(); !errors.Is(err, identity.ErrRevisionOutOfRange) {
		t.Errorf("expected ErrRevisionOutOfRange for rev > MaxInt64, got %v", err)
	}

	// Timestamps
	invalidCreatedAt := validEntity
	invalidCreatedAt.CreatedAtMS = 0
	if err := invalidCreatedAt.Validate(); !errors.Is(err, identity.ErrInvalidTimestamps) {
		t.Errorf("expected ErrInvalidTimestamps for createdAt <= 0, got %v", err)
	}

	invalidUpdatedAt := validEntity
	invalidUpdatedAt.UpdatedAtMS = 999
	if err := invalidUpdatedAt.Validate(); !errors.Is(err, identity.ErrInvalidTimestamps) {
		t.Errorf("expected ErrInvalidTimestamps for updatedAt < createdAt, got %v", err)
	}
}

func TestCredentialIdentityValidation(t *testing.T) {
	validID := identity.CredentialID("0123456789abcdef0123456789abcdef")

	validEntity := identity.CredentialIdentity{
		ID:          validID,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 1000,
		UpdatedAtMS: 1000,
	}
	if err := validEntity.Validate(); err != nil {
		t.Fatalf("valid entity validation failed: %v", err)
	}

	invalidRevZero := validEntity
	invalidRevZero.Revision = 0
	if err := invalidRevZero.Validate(); !errors.Is(err, identity.ErrRevisionOutOfRange) {
		t.Errorf("expected ErrRevisionOutOfRange for rev 0, got %v", err)
	}

	invalidRevOverflow := validEntity
	invalidRevOverflow.Revision = math.MaxInt64 + 1
	if err := invalidRevOverflow.Validate(); !errors.Is(err, identity.ErrRevisionOutOfRange) {
		t.Errorf("expected ErrRevisionOutOfRange for rev > MaxInt64, got %v", err)
	}
}

func TestAPIKeySourceBindingValidation(t *testing.T) {
	validID := identity.APIKeyID("0123456789abcdef0123456789abcdef")
	validHash := strings.Repeat("a", 64)

	validBinding := identity.APIKeySourceBinding{
		APIKeyID:                  validID,
		RuntimeIdentity:           "rt-embedded-1",
		APIKeyHash:                validHash,
		ObservedRuntimeGeneration: 12345,
		FirstSeenAtMS:             1000,
		LastSeenAtMS:              1000,
		RetiredAtMS:               0,
	}
	if err := validBinding.Validate(); err != nil {
		t.Fatalf("valid binding validation failed: %v", err)
	}

	// Generation = 0 is valid (unavailable)
	withZeroGen := validBinding
	withZeroGen.ObservedRuntimeGeneration = 0
	if err := withZeroGen.Validate(); err != nil {
		t.Fatalf("generation = 0 should be valid, got: %v", err)
	}

	// Generation > MaxInt64 is valid
	withLargeGen := validBinding
	withLargeGen.ObservedRuntimeGeneration = math.MaxUint64
	if err := withLargeGen.Validate(); err != nil {
		t.Fatalf("generation = MaxUint64 should be valid, got: %v", err)
	}

	// Empty RuntimeIdentity rejected
	emptyRT := validBinding
	emptyRT.RuntimeIdentity = ""
	if err := emptyRT.Validate(); !errors.Is(err, identity.ErrInvalidRuntimeIdentity) {
		t.Errorf("expected ErrInvalidRuntimeIdentity, got %v", err)
	}

	// Untrimmed RuntimeIdentity rejected
	untrimmedRT := validBinding
	untrimmedRT.RuntimeIdentity = " rt-embedded-1 "
	if err := untrimmedRT.Validate(); !errors.Is(err, identity.ErrInvalidRuntimeIdentity) {
		t.Errorf("expected ErrInvalidRuntimeIdentity, got %v", err)
	}

	// Invalid APIKeyHash
	invalidHashes := []string{
		"",
		strings.Repeat("a", 63),
		strings.Repeat("a", 65),
		strings.Repeat("A", 64),
		strings.Repeat("g", 64),
		" " + validHash,
	}
	for _, h := range invalidHashes {
		b := validBinding
		b.APIKeyHash = h
		if err := b.Validate(); !errors.Is(err, identity.ErrInvalidAPIKeyHash) {
			t.Errorf("expected ErrInvalidAPIKeyHash for hash %q, got %v", h, err)
		}
	}

	// Invalid timestamps
	invalidTimestamps := validBinding
	invalidTimestamps.LastSeenAtMS = 999
	if err := invalidTimestamps.Validate(); !errors.Is(err, identity.ErrInvalidTimestamps) {
		t.Errorf("expected ErrInvalidTimestamps when lastSeen < firstSeen, got %v", err)
	}

	invalidRetired := validBinding
	invalidRetired.RetiredAtMS = 999
	if err := invalidRetired.Validate(); !errors.Is(err, identity.ErrInvalidTimestamps) {
		t.Errorf("expected ErrInvalidTimestamps when retired < firstSeen, got %v", err)
	}
}

func TestCredentialSourceBindingValidation(t *testing.T) {
	validID := identity.CredentialID("0123456789abcdef0123456789abcdef")

	validBinding := identity.CredentialSourceBinding{
		CredentialID:              validID,
		RuntimeIdentity:           "rt-embedded-1",
		SourceAuthID:              "auth-12345",
		AuthIndex:                 "openai-1",
		Provider:                  "openai",
		PhysicalName:              "openai.json",
		AccountSnapshot:           "user@example.com",
		AccountIDSnapshot:         "acc-001",
		ObservedRuntimeGeneration: 98765,
		FirstSeenAtMS:             1000,
		LastSeenAtMS:              1000,
		RetiredAtMS:               0,
	}
	if err := validBinding.Validate(); err != nil {
		t.Fatalf("valid credential binding validation failed: %v", err)
	}

	// Empty source auth ID rejected
	emptyAuth := validBinding
	emptyAuth.SourceAuthID = ""
	if err := emptyAuth.Validate(); !errors.Is(err, identity.ErrInvalidSourceAuthID) {
		t.Errorf("expected ErrInvalidSourceAuthID, got %v", err)
	}

	// Untrimmed source auth ID rejected
	untrimmedAuth := validBinding
	untrimmedAuth.SourceAuthID = " auth-12345 "
	if err := untrimmedAuth.Validate(); !errors.Is(err, identity.ErrInvalidSourceAuthID) {
		t.Errorf("expected ErrInvalidSourceAuthID, got %v", err)
	}
}
