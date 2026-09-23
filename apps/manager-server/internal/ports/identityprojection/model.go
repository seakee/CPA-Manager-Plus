package identityprojection

import (
	"errors"
	"fmt"
	"strings"
)

// MappingState represents the four canonical shadow mapping states: mapped, unknown, ambiguous, stale.
type MappingState string

const (
	StateMapped    MappingState = "mapped"
	StateUnknown   MappingState = "unknown"
	StateAmbiguous MappingState = "ambiguous"
	StateStale     MappingState = "stale"
)

func (s MappingState) IsValid() bool {
	switch s {
	case StateMapped, StateUnknown, StateAmbiguous, StateStale:
		return true
	default:
		return false
	}
}

// UsageIdentityProjection represents a purpose-built derived projection row from usage_events.
type UsageIdentityProjection struct {
	UsageEventID           int64        `json:"usage_event_id"`
	EventHash              string       `json:"event_hash"`
	RequestID              string       `json:"request_id"`
	EvidenceTimestampMS    int64        `json:"evidence_timestamp_ms"`
	APIKeyState            MappingState `json:"api_key_state"`
	APIKeyID               *string      `json:"api_key_id,omitempty"`
	APIKeySourceHash       string       `json:"api_key_source_hash"`
	CredentialState        MappingState `json:"credential_state"`
	CredentialID           *string      `json:"credential_id,omitempty"`
	CredentialSourceAuthID string       `json:"credential_source_auth_id"`
	SchemaVersion          int          `json:"schema_version"`
	ProjectedAtMS          int64        `json:"projected_at_ms"`
}

// Validate checks internal invariants for UsageIdentityProjection:
// - state == mapped => canonical ID MUST NOT be empty
// - state != mapped => canonical ID MUST be empty (nil)
func (p UsageIdentityProjection) Validate() error {
	if p.UsageEventID <= 0 {
		return errors.New("usage_event_id must be positive")
	}
	if p.EventHash == "" {
		return errors.New("event_hash must not be empty")
	}
	if p.EvidenceTimestampMS <= 0 {
		return errors.New("evidence_timestamp_ms must be positive")
	}
	if !p.APIKeyState.IsValid() {
		return fmt.Errorf("invalid api_key_state: %q", p.APIKeyState)
	}
	if p.APIKeyState == StateMapped {
		if p.APIKeyID == nil || strings.TrimSpace(*p.APIKeyID) == "" {
			return errors.New("api_key_id must not be empty when api_key_state is mapped")
		}
	} else {
		if p.APIKeyID != nil {
			return errors.New("api_key_id must be nil when api_key_state is not mapped")
		}
	}
	if !p.CredentialState.IsValid() {
		return fmt.Errorf("invalid credential_state: %q", p.CredentialState)
	}
	if p.CredentialState == StateMapped {
		if p.CredentialID == nil || strings.TrimSpace(*p.CredentialID) == "" {
			return errors.New("credential_id must not be empty when credential_state is mapped")
		}
	} else {
		if p.CredentialID != nil {
			return errors.New("credential_id must be nil when credential_state is not mapped")
		}
	}
	if p.SchemaVersion <= 0 {
		return errors.New("schema_version must be positive")
	}
	if p.ProjectedAtMS <= 0 {
		return errors.New("projected_at_ms must be positive")
	}
	return nil
}

// State represents the persistent checkpoint / catch-up state.
type State struct {
	StateName            string `json:"state_name"`
	SchemaVersion        int    `json:"schema_version"`
	Status               string `json:"status"`
	LastProcessedEventID int64  `json:"last_processed_event_id"`
	TargetEventID        int64  `json:"target_event_id"`
	ProcessedEvents      int64  `json:"processed_events"`
	LastRunStartedAtMS   *int64 `json:"last_run_started_at_ms,omitempty"`
	UpdatedAtMS          int64  `json:"updated_at_ms"`
	FinishedAtMS         *int64 `json:"finished_at_ms,omitempty"`
	LastError            string `json:"last_error,omitempty"`
}

// CatchUpResult summarizes a bounded batch catch-up iteration.
type CatchUpResult struct {
	Processed            int   `json:"processed"`
	LastProcessedEventID int64 `json:"last_processed_event_id"`
	TargetEventID        int64 `json:"target_event_id"`
	Pending              bool  `json:"pending"`
	Rebuilt              bool  `json:"rebuilt"`
}
