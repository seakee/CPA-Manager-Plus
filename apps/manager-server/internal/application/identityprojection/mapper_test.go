package identityprojection

import (
	"strings"
	"testing"

	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityprojection"
)

func TestExtractCredentialSourceAuthID(t *testing.T) {
	tests := []struct {
		name       string
		rawJSON    string
		wantAuthID string
		wantState  ports.MappingState
	}{
		{
			name:       "empty raw_json",
			rawJSON:    "",
			wantAuthID: "",
			wantState:  ports.StateUnknown,
		},
		{
			name:       "malformed json",
			rawJSON:    `{not-valid-json`,
			wantAuthID: "",
			wantState:  ports.StateUnknown,
		},
		{
			name:       "json array instead of object",
			rawJSON:    `["auth-1"]`,
			wantAuthID: "",
			wantState:  ports.StateUnknown,
		},
		{
			name:       "no auth_id present",
			rawJSON:    `{"model":"gpt-4","tokens":{"total":100}}`,
			wantAuthID: "",
			wantState:  ports.StateUnknown,
		},
		{
			name:       "only empty auth_id",
			rawJSON:    `{"auth_id":"   ","authId":""}`,
			wantAuthID: "",
			wantState:  ports.StateUnknown,
		},
		{
			name:       "single auth_id alias",
			rawJSON:    `{"auth_id":"cpa-cred-01"}`,
			wantAuthID: "cpa-cred-01",
			wantState:  "",
		},
		{
			name:       "single authId alias with surrounding whitespace",
			rawJSON:    `{"authId":"  cpa-cred-01  "}`,
			wantAuthID: "cpa-cred-01",
			wantState:  "",
		},
		{
			name:       "single AuthID alias",
			rawJSON:    `{"AuthID":"cpa-cred-01"}`,
			wantAuthID: "cpa-cred-01",
			wantState:  "",
		},
		{
			name:       "single AuthId alias",
			rawJSON:    `{"AuthId":"cpa-cred-01"}`,
			wantAuthID: "cpa-cred-01",
			wantState:  "",
		},
		{
			name:       "multiple aliases with identical value",
			rawJSON:    `{"auth_id":"cpa-cred-01","authId":"cpa-cred-01","AuthID":" cpa-cred-01 "}`,
			wantAuthID: "cpa-cred-01",
			wantState:  "",
		},
		{
			name:       "conflicting aliases with different non-empty values",
			rawJSON:    `{"auth_id":"cpa-cred-01","authId":"cpa-cred-02"}`,
			wantAuthID: "",
			wantState:  ports.StateAmbiguous,
		},
		{
			name:       "auth_id inside detail object",
			rawJSON:    `{"model":"claude-3","detail":{"auth_id":"cpa-cred-detail"}}`,
			wantAuthID: "cpa-cred-detail",
			wantState:  "",
		},
		{
			name:       "top level and detail conflicting",
			rawJSON:    `{"auth_id":"top-cred","detail":{"auth_id":"detail-cred"}}`,
			wantAuthID: "",
			wantState:  ports.StateAmbiguous,
		},
		{
			name:       "top level and detail identical",
			rawJSON:    `{"auth_id":"same-cred","detail":{"auth_id":"same-cred"}}`,
			wantAuthID: "same-cred",
			wantState:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAuthID, gotState := ExtractCredentialSourceAuthID(tt.rawJSON)
			if gotAuthID != tt.wantAuthID {
				t.Errorf("ExtractCredentialSourceAuthID() gotAuthID = %q, want %q", gotAuthID, tt.wantAuthID)
			}
			if gotState != tt.wantState {
				t.Errorf("ExtractCredentialSourceAuthID() gotState = %q, want %q", gotState, tt.wantState)
			}
		})
	}
}

func TestResolveTemporalBinding(t *testing.T) {
	t.Run("empty bindings returns unknown", func(t *testing.T) {
		state, id := ResolveTemporalBinding(nil, 1000)
		if state != ports.StateUnknown || id != nil {
			t.Fatalf("expected unknown/nil, got %v/%v", state, id)
		}
	})

	t.Run("single active binding matches", func(t *testing.T) {
		bindings := []SourceBindingRecord{
			{CanonicalID: "id-1", RuntimeIdentity: "rt-1", FirstSeenAtMS: 100, RetiredAtMS: 0},
		}
		// Event timestamp >= first seen
		state, id := ResolveTemporalBinding(bindings, 150)
		if state != ports.StateMapped || id == nil || *id != "id-1" {
			t.Fatalf("expected mapped/id-1, got %v/%v", state, id)
		}
		// Boundary check: timestamp == first seen
		state, id = ResolveTemporalBinding(bindings, 100)
		if state != ports.StateMapped || id == nil || *id != "id-1" {
			t.Fatalf("expected mapped/id-1 at exact first_seen, got %v/%v", state, id)
		}
	})

	t.Run("event before first_seen is stale", func(t *testing.T) {
		bindings := []SourceBindingRecord{
			{CanonicalID: "id-1", RuntimeIdentity: "rt-1", FirstSeenAtMS: 500, RetiredAtMS: 0},
		}
		state, id := ResolveTemporalBinding(bindings, 499)
		if state != ports.StateStale || id != nil {
			t.Fatalf("expected stale/nil, got %v/%v", state, id)
		}
	})

	t.Run("retired binding interval is half open [first_seen, retired)", func(t *testing.T) {
		bindings := []SourceBindingRecord{
			{CanonicalID: "id-1", RuntimeIdentity: "rt-1", FirstSeenAtMS: 100, RetiredAtMS: 200},
		}
		// In range
		state, id := ResolveTemporalBinding(bindings, 199)
		if state != ports.StateMapped || id == nil || *id != "id-1" {
			t.Fatalf("expected mapped at 199, got %v/%v", state, id)
		}
		// At retired_at_ms (closed upper bound excluded) -> stale
		state, id = ResolveTemporalBinding(bindings, 200)
		if state != ports.StateStale || id != nil {
			t.Fatalf("expected stale at exact retired_at_ms 200, got %v/%v", state, id)
		}
		// After retired_at_ms -> stale
		state, id = ResolveTemporalBinding(bindings, 250)
		if state != ports.StateStale || id != nil {
			t.Fatalf("expected stale at 250, got %v/%v", state, id)
		}
	})

	t.Run("overlapping candidates within same runtime is ambiguous", func(t *testing.T) {
		bindings := []SourceBindingRecord{
			{CanonicalID: "id-1", RuntimeIdentity: "rt-1", FirstSeenAtMS: 100, RetiredAtMS: 300},
			{CanonicalID: "id-2", RuntimeIdentity: "rt-1", FirstSeenAtMS: 200, RetiredAtMS: 400},
		}
		state, id := ResolveTemporalBinding(bindings, 250)
		if state != ports.StateAmbiguous || id != nil {
			t.Fatalf("expected ambiguous/nil, got %v/%v", state, id)
		}
	})

	t.Run("cross-runtime overlapping candidates is ambiguous", func(t *testing.T) {
		bindings := []SourceBindingRecord{
			{CanonicalID: "id-1", RuntimeIdentity: "rt-A", FirstSeenAtMS: 100, RetiredAtMS: 300},
			{CanonicalID: "id-2", RuntimeIdentity: "rt-B", FirstSeenAtMS: 200, RetiredAtMS: 400},
		}
		state, id := ResolveTemporalBinding(bindings, 250)
		if state != ports.StateAmbiguous || id != nil {
			t.Fatalf("expected ambiguous/nil, got %v/%v", state, id)
		}
	})

	t.Run("explicit B1 rotation maps old and new to same Canonical ID", func(t *testing.T) {
		oldHashBindings := []SourceBindingRecord{
			{CanonicalID: "id-A", RuntimeIdentity: "rt-1", FirstSeenAtMS: 100, RetiredAtMS: 200},
		}
		newHashBindings := []SourceBindingRecord{
			{CanonicalID: "id-A", RuntimeIdentity: "rt-1", FirstSeenAtMS: 200, RetiredAtMS: 0},
		}

		state1, id1 := ResolveTemporalBinding(oldHashBindings, 150)
		if state1 != ports.StateMapped || *id1 != "id-A" {
			t.Fatalf("old hash at 150 want mapped/id-A, got %v/%v", state1, id1)
		}

		state2, id2 := ResolveTemporalBinding(newHashBindings, 250)
		if state2 != ports.StateMapped || *id2 != "id-A" {
			t.Fatalf("new hash at 250 want mapped/id-A, got %v/%v", state2, id2)
		}
	})

	t.Run("passive replacement maps old and new to different Canonical IDs", func(t *testing.T) {
		oldHashBindings := []SourceBindingRecord{
			{CanonicalID: "id-A", RuntimeIdentity: "rt-1", FirstSeenAtMS: 100, RetiredAtMS: 200},
		}
		newHashBindings := []SourceBindingRecord{
			{CanonicalID: "id-B", RuntimeIdentity: "rt-1", FirstSeenAtMS: 200, RetiredAtMS: 0},
		}

		state1, id1 := ResolveTemporalBinding(oldHashBindings, 150)
		if state1 != ports.StateMapped || *id1 != "id-A" {
			t.Fatalf("old hash at 150 want mapped/id-A, got %v/%v", state1, id1)
		}

		state2, id2 := ResolveTemporalBinding(newHashBindings, 250)
		if state2 != ports.StateMapped || *id2 != "id-B" {
			t.Fatalf("new hash at 250 want mapped/id-B, got %v/%v", state2, id2)
		}
	})

	t.Run("superseded source reappearance maps to respective IDs and stale in between", func(t *testing.T) {
		bindings := []SourceBindingRecord{
			{CanonicalID: "id-A", RuntimeIdentity: "rt-1", FirstSeenAtMS: 100, RetiredAtMS: 200},
			{CanonicalID: "id-B", RuntimeIdentity: "rt-1", FirstSeenAtMS: 300, RetiredAtMS: 0},
		}

		// Event before retirement of old ID
		state1, id1 := ResolveTemporalBinding(bindings, 150)
		if state1 != ports.StateMapped || *id1 != "id-A" {
			t.Fatalf("at 150 want mapped/id-A, got %v/%v", state1, id1)
		}

		// Event in the gap [200, 300) is stale
		state2, id2 := ResolveTemporalBinding(bindings, 250)
		if state2 != ports.StateStale || id2 != nil {
			t.Fatalf("at 250 want stale/nil, got %v/%v", state2, id2)
		}

		// Event after new ID appeared
		state3, id3 := ResolveTemporalBinding(bindings, 350)
		if state3 != ports.StateMapped || *id3 != "id-B" {
			t.Fatalf("at 350 want mapped/id-B, got %v/%v", state3, id3)
		}
	})
}

func TestMapEvent(t *testing.T) {
	validHash := strings.Repeat("a", 64)
	apiKeyBindings := []SourceBindingRecord{
		{CanonicalID: "api-key-123", RuntimeIdentity: "rt-1", FirstSeenAtMS: 100, RetiredAtMS: 0},
	}
	credBindings := []SourceBindingRecord{
		{CanonicalID: "cred-456", RuntimeIdentity: "rt-1", FirstSeenAtMS: 100, RetiredAtMS: 0},
	}

	t.Run("both mapped", func(t *testing.T) {
		event := EventInput{
			UsageEventID: 1,
			EventHash:    "hash-1",
			RequestID:    "req-1",
			TimestampMS:  150,
			APIKeyHash:   validHash,
			RawJSON:      `{"auth_id":"auth-cred-1"}`,
		}
		proj, err := MapEvent(event, apiKeyBindings, credBindings, 200)
		if err != nil {
			t.Fatalf("MapEvent failed: %v", err)
		}
		if proj.APIKeyState != ports.StateMapped || proj.APIKeyID == nil || *proj.APIKeyID != "api-key-123" {
			t.Fatalf("unexpected API key state: %v, id: %v", proj.APIKeyState, proj.APIKeyID)
		}
		if proj.APIKeySourceHash != validHash {
			t.Fatalf("unexpected API key source hash: %v", proj.APIKeySourceHash)
		}
		if proj.CredentialState != ports.StateMapped || proj.CredentialID == nil || *proj.CredentialID != "cred-456" {
			t.Fatalf("unexpected Credential state: %v, id: %v", proj.CredentialState, proj.CredentialID)
		}
		if proj.CredentialSourceAuthID != "auth-cred-1" {
			t.Fatalf("unexpected Credential source auth id: %v", proj.CredentialSourceAuthID)
		}
		if proj.RequestID != "req-1" {
			t.Fatalf("unexpected RequestID: %v", proj.RequestID)
		}
	})

	t.Run("raw api key is rejected and never saved in source hash", func(t *testing.T) {
		event := EventInput{
			UsageEventID: 2,
			EventHash:    "hash-2",
			TimestampMS:  150,
			APIKeyHash:   "sk-live-raw-secret-key-12345",
			RawJSON:      `{}`,
		}
		proj, err := MapEvent(event, apiKeyBindings, credBindings, 200)
		if err != nil {
			t.Fatalf("MapEvent failed: %v", err)
		}
		if proj.APIKeyState != ports.StateUnknown || proj.APIKeyID != nil {
			t.Fatalf("expected unknown api key, got %v/%v", proj.APIKeyState, proj.APIKeyID)
		}
		if proj.APIKeySourceHash != "" {
			t.Fatalf("expected empty api_key_source_hash for invalid key, got %q", proj.APIKeySourceHash)
		}
	})

	t.Run("invariant validation fails if mapped without ID", func(t *testing.T) {
		proj := ports.UsageIdentityProjection{
			UsageEventID:        1,
			EventHash:           "h",
			EvidenceTimestampMS: 100,
			APIKeyState:         ports.StateMapped,
			APIKeyID:            nil, // Violates invariant
			CredentialState:     ports.StateUnknown,
			SchemaVersion:       1,
			ProjectedAtMS:       100,
		}
		if err := proj.Validate(); err == nil {
			t.Fatal("expected validation error for mapped state with nil ID")
		}
	})

	t.Run("invariant validation fails if unknown with non-nil ID", func(t *testing.T) {
		id := "some-id"
		proj := ports.UsageIdentityProjection{
			UsageEventID:        1,
			EventHash:           "h",
			EvidenceTimestampMS: 100,
			APIKeyState:         ports.StateUnknown,
			APIKeyID:            &id, // Violates invariant
			CredentialState:     ports.StateUnknown,
			SchemaVersion:       1,
			ProjectedAtMS:       100,
		}
		if err := proj.Validate(); err == nil {
			t.Fatal("expected validation error for unknown state with non-nil ID")
		}
	})
}
