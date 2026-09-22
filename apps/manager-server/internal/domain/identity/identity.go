package identity

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Revision is an independent business revision for a canonical identity.
// It starts at 1 and must fit within a SQLite signed 64-bit integer (1..math.MaxInt64).
type Revision uint64

const InitialRevision Revision = 1

// Lifecycle represents the canonical identity lifecycle state.
// Only active, missing, and superseded exist in Phase3-01.
type Lifecycle string

const (
	LifecycleActive     Lifecycle = "active"
	LifecycleMissing    Lifecycle = "missing"
	LifecycleSuperseded Lifecycle = "superseded"
)

func (l Lifecycle) IsValid() bool {
	switch l {
	case LifecycleActive, LifecycleMissing, LifecycleSuperseded:
		return true
	default:
		return false
	}
}

var (
	ErrInvalidLifecycle           = errors.New("invalid canonical identity lifecycle")
	ErrInvalidLifecycleTransition = errors.New("invalid canonical identity lifecycle transition")
	ErrRevisionOutOfRange         = errors.New("canonical identity revision out of range")
	ErrInvalidRuntimeIdentity     = errors.New("invalid runtime identity")
	ErrInvalidAPIKeyHash          = errors.New("invalid API key hash")
	ErrInvalidSourceAuthID        = errors.New("invalid credential source auth ID")
	ErrInvalidTimestamps          = errors.New("invalid identity or binding timestamps")
)

// ValidateTransition checks whether a transition from one lifecycle to another is permitted.
// Permitted transitions:
//   - active -> missing
//   - active -> superseded
//   - missing -> active
//   - missing -> superseded
//   - active -> active (no-op)
//   - missing -> missing (no-op)
//
// Superseded is terminal and cannot transition to active or missing.
func ValidateTransition(from, to Lifecycle) error {
	if !from.IsValid() {
		return fmt.Errorf("%w: unknown source lifecycle %q", ErrInvalidLifecycle, from)
	}
	if !to.IsValid() {
		return fmt.Errorf("%w: unknown target lifecycle %q", ErrInvalidLifecycle, to)
	}
	if from == LifecycleSuperseded {
		return fmt.Errorf("%w: cannot transition from terminal %q to %q", ErrInvalidLifecycleTransition, from, to)
	}
	return nil
}

// APIKeyIdentity is the canonical identity entity for an API key.
type APIKeyIdentity struct {
	ID          APIKeyID
	Revision    Revision
	Lifecycle   Lifecycle
	CreatedAtMS int64
	UpdatedAtMS int64
}

func (e APIKeyIdentity) Validate() error {
	if err := e.ID.Validate(); err != nil {
		return err
	}
	if e.Revision < 1 || e.Revision > math.MaxInt64 {
		return fmt.Errorf("%w: revision %d must be between 1 and math.MaxInt64", ErrRevisionOutOfRange, e.Revision)
	}
	if !e.Lifecycle.IsValid() {
		return fmt.Errorf("%w: %q", ErrInvalidLifecycle, e.Lifecycle)
	}
	if e.CreatedAtMS <= 0 {
		return fmt.Errorf("%w: createdAtMs must be positive", ErrInvalidTimestamps)
	}
	if e.UpdatedAtMS < e.CreatedAtMS {
		return fmt.Errorf("%w: updatedAtMs (%d) must be >= createdAtMs (%d)", ErrInvalidTimestamps, e.UpdatedAtMS, e.CreatedAtMS)
	}
	return nil
}

// CredentialIdentity is the canonical identity entity for a credential.
type CredentialIdentity struct {
	ID          CredentialID
	Revision    Revision
	Lifecycle   Lifecycle
	CreatedAtMS int64
	UpdatedAtMS int64
}

func (e CredentialIdentity) Validate() error {
	if err := e.ID.Validate(); err != nil {
		return err
	}
	if e.Revision < 1 || e.Revision > math.MaxInt64 {
		return fmt.Errorf("%w: revision %d must be between 1 and math.MaxInt64", ErrRevisionOutOfRange, e.Revision)
	}
	if !e.Lifecycle.IsValid() {
		return fmt.Errorf("%w: %q", ErrInvalidLifecycle, e.Lifecycle)
	}
	if e.CreatedAtMS <= 0 {
		return fmt.Errorf("%w: createdAtMs must be positive", ErrInvalidTimestamps)
	}
	if e.UpdatedAtMS < e.CreatedAtMS {
		return fmt.Errorf("%w: updatedAtMs (%d) must be >= createdAtMs (%d)", ErrInvalidTimestamps, e.UpdatedAtMS, e.CreatedAtMS)
	}
	return nil
}

// APIKeySourceBinding binds a canonical APIKeyID to a trusted runtime source (RuntimeIdentity, SHA-256(raw API key)).
type APIKeySourceBinding struct {
	BindingID                 int64
	APIKeyID                  APIKeyID
	RuntimeIdentity           string
	APIKeyHash                string
	ObservedRuntimeGeneration uint64 // 0 represents unavailable
	FirstSeenAtMS             int64
	LastSeenAtMS              int64
	RetiredAtMS               int64 // 0 represents active binding
}

func (b APIKeySourceBinding) Validate() error {
	if err := b.APIKeyID.Validate(); err != nil {
		return err
	}
	if b.RuntimeIdentity == "" || strings.TrimSpace(b.RuntimeIdentity) != b.RuntimeIdentity {
		return fmt.Errorf("%w: must be non-empty and have no surrounding whitespace", ErrInvalidRuntimeIdentity)
	}
	if !isValidSHA256Hex(b.APIKeyHash) {
		return fmt.Errorf("%w: must be 64 lowercase hexadecimal characters", ErrInvalidAPIKeyHash)
	}
	if b.FirstSeenAtMS <= 0 {
		return fmt.Errorf("%w: firstSeenAtMs must be positive", ErrInvalidTimestamps)
	}
	if b.LastSeenAtMS < b.FirstSeenAtMS {
		return fmt.Errorf("%w: lastSeenAtMs (%d) must be >= firstSeenAtMs (%d)", ErrInvalidTimestamps, b.LastSeenAtMS, b.FirstSeenAtMS)
	}
	if b.RetiredAtMS != 0 && b.RetiredAtMS < b.FirstSeenAtMS {
		return fmt.Errorf("%w: retiredAtMs (%d) must be >= firstSeenAtMs (%d)", ErrInvalidTimestamps, b.RetiredAtMS, b.FirstSeenAtMS)
	}
	return nil
}

// CredentialSourceBinding binds a canonical CredentialID to a trusted runtime source (RuntimeIdentity, CPA Auth.ID).
type CredentialSourceBinding struct {
	BindingID                 int64
	CredentialID              CredentialID
	RuntimeIdentity           string
	SourceAuthID              string
	AuthIndex                 string
	Provider                  string
	PhysicalName              string
	AccountSnapshot           string
	AccountIDSnapshot         string
	ObservedRuntimeGeneration uint64 // 0 represents unavailable
	FirstSeenAtMS             int64
	LastSeenAtMS              int64
	RetiredAtMS               int64 // 0 represents active binding
}

func (b CredentialSourceBinding) Validate() error {
	if err := b.CredentialID.Validate(); err != nil {
		return err
	}
	if b.RuntimeIdentity == "" || strings.TrimSpace(b.RuntimeIdentity) != b.RuntimeIdentity {
		return fmt.Errorf("%w: must be non-empty and have no surrounding whitespace", ErrInvalidRuntimeIdentity)
	}
	if b.SourceAuthID == "" || strings.TrimSpace(b.SourceAuthID) != b.SourceAuthID {
		return fmt.Errorf("%w: must be non-empty and have no surrounding whitespace", ErrInvalidSourceAuthID)
	}
	if b.FirstSeenAtMS <= 0 {
		return fmt.Errorf("%w: firstSeenAtMs must be positive", ErrInvalidTimestamps)
	}
	if b.LastSeenAtMS < b.FirstSeenAtMS {
		return fmt.Errorf("%w: lastSeenAtMs (%d) must be >= firstSeenAtMs (%d)", ErrInvalidTimestamps, b.LastSeenAtMS, b.FirstSeenAtMS)
	}
	if b.RetiredAtMS != 0 && b.RetiredAtMS < b.FirstSeenAtMS {
		return fmt.Errorf("%w: retiredAtMs (%d) must be >= firstSeenAtMs (%d)", ErrInvalidTimestamps, b.RetiredAtMS, b.FirstSeenAtMS)
	}
	return nil
}

// IsValidSHA256Hex checks if a string is exactly 64 lowercase hexadecimal characters.
func IsValidSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}

func isValidSHA256Hex(s string) bool {
	return IsValidSHA256Hex(s)
}
