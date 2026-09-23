package identityprojection

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityprojection"
)

// CurrentSchemaVersion is the schema version for gateway_usage_identity_projection_v1.
const CurrentSchemaVersion = 1

// SourceBindingRecord represents a historical source binding from either
// gateway_api_key_source_bindings or gateway_credential_source_bindings.
type SourceBindingRecord struct {
	CanonicalID     string
	RuntimeIdentity string
	FirstSeenAtMS   int64
	RetiredAtMS     int64 // 0 represents an active (unretired) binding
}

// EventInput represents the minimal evidence extracted from a usage_events row.
type EventInput struct {
	UsageEventID int64
	EventHash    string
	RequestID    string
	TimestampMS  int64
	APIKeyHash   string
	RawJSON      string
}

// ExtractCredentialSourceAuthID parses sanitized usage_events.raw_json and extracts
// the trusted CPA Auth.ID evidence based on supported aliases (auth_id, authId, AuthID, AuthId).
//
// Semantics:
// - 0 non-empty values => unknown
// - 1 unique non-empty value => trusted source_auth_id
// - Multiple aliases with identical value => trusted source_auth_id
// - Multiple different non-empty values => ambiguous
// - malformed JSON or a non-string top-level alias => unknown
// Only top-level aliases are trusted. Only trim normalization is applied.
// No fallback to detail/auth_index/file/provider/account is allowed.
func ExtractCredentialSourceAuthID(rawJSON string) (sourceAuthID string, state ports.MappingState) {
	trimmed := strings.TrimSpace(rawJSON)
	if trimmed == "" {
		return "", ports.StateUnknown
	}

	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", ports.StateUnknown
	}

	record, ok := payload.(map[string]any)
	if !ok {
		return "", ports.StateUnknown
	}

	var foundValues []string
	for _, key := range []string{"auth_id", "authId", "AuthID", "AuthId"} {
		value, exists := record[key]
		if !exists {
			continue
		}
		valueString, ok := value.(string)
		if !ok {
			return "", ports.StateUnknown
		}
		if trimmedValue := strings.TrimSpace(valueString); trimmedValue != "" {
			foundValues = append(foundValues, trimmedValue)
		}
	}

	if len(foundValues) == 0 {
		return "", ports.StateUnknown
	}

	unique := make(map[string]struct{}, len(foundValues))
	for _, v := range foundValues {
		unique[v] = struct{}{}
	}

	if len(unique) > 1 {
		return "", ports.StateAmbiguous
	}

	return foundValues[0], ""
}

// ResolveTemporalBinding evaluates candidate source bindings for a given event timestamp
// using a half-open interval:
//
//	first_seen_at_ms <= timestamp_ms AND (retired_at_ms IS NULL/0 OR timestamp_ms < retired_at_ms)
//
// Semantics:
// - If history has 0 bindings for this source => unknown
// - If history has bindings, but 0 match the temporal interval => stale
// - If exactly 1 binding matches => mapped with canonical ID
// - If >1 bindings match => ambiguous (fail-closed, across same or different runtimes)
func ResolveTemporalBinding(bindings []SourceBindingRecord, eventTimestampMS int64) (ports.MappingState, *string) {
	if len(bindings) == 0 {
		return ports.StateUnknown, nil
	}

	var matched []SourceBindingRecord
	for _, b := range bindings {
		if b.FirstSeenAtMS <= eventTimestampMS && (b.RetiredAtMS == 0 || eventTimestampMS < b.RetiredAtMS) {
			matched = append(matched, b)
		}
	}

	if len(matched) == 0 {
		return ports.StateStale, nil
	}
	if len(matched) > 1 {
		return ports.StateAmbiguous, nil
	}

	canonicalID := matched[0].CanonicalID
	if strings.TrimSpace(canonicalID) == "" {
		return ports.StateUnknown, nil
	}
	return ports.StateMapped, &canonicalID
}

// MapEvent computes a UsageIdentityProjection from an event input and corresponding
// G1 history bindings.
func MapEvent(
	event EventInput,
	apiKeyBindings []SourceBindingRecord,
	credBindings []SourceBindingRecord,
	projectedAtMS int64,
) (ports.UsageIdentityProjection, error) {
	proj := ports.UsageIdentityProjection{
		UsageEventID:        event.UsageEventID,
		EventHash:           event.EventHash,
		RequestID:           event.RequestID,
		EvidenceTimestampMS: event.TimestampMS,
		SchemaVersion:       CurrentSchemaVersion,
		ProjectedAtMS:       projectedAtMS,
	}

	// 1. API Key Mapping
	trimmedKeyHash := strings.TrimSpace(event.APIKeyHash)
	if !identity.IsValidSHA256Hex(trimmedKeyHash) {
		proj.APIKeyState = ports.StateUnknown
		proj.APIKeyID = nil
		proj.APIKeySourceHash = ""
	} else {
		proj.APIKeySourceHash = trimmedKeyHash
		state, id := ResolveTemporalBinding(apiKeyBindings, event.TimestampMS)
		proj.APIKeyState = state
		proj.APIKeyID = id
	}

	// 2. Credential Mapping
	sourceAuthID, credExtractState := ExtractCredentialSourceAuthID(event.RawJSON)
	switch credExtractState {
	case ports.StateAmbiguous:
		proj.CredentialState = ports.StateAmbiguous
		proj.CredentialID = nil
		proj.CredentialSourceAuthID = ""
	case ports.StateUnknown:
		proj.CredentialState = ports.StateUnknown
		proj.CredentialID = nil
		proj.CredentialSourceAuthID = ""
	default:
		proj.CredentialSourceAuthID = sourceAuthID
		state, id := ResolveTemporalBinding(credBindings, event.TimestampMS)
		proj.CredentialState = state
		proj.CredentialID = id
	}

	if err := proj.Validate(); err != nil {
		return ports.UsageIdentityProjection{}, fmt.Errorf("validate projection: %w", err)
	}

	return proj, nil
}
