package identity

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// APIKeyID is a CPAMP-owned, opaque, non-reusable canonical identifier for an API key.
// It is exactly 32 lowercase hexadecimal characters (16 cryptographically random bytes).
type APIKeyID string

// CredentialID is a CPAMP-owned, opaque, non-reusable canonical identifier for a credential.
// It is exactly 32 lowercase hexadecimal characters (16 cryptographically random bytes).
type CredentialID string

const canonicalIDHexLength = 32

var (
	ErrInvalidAPIKeyID     = errors.New("invalid canonical APIKeyID")
	ErrInvalidCredentialID = errors.New("invalid canonical CredentialID")
)

// NewAPIKeyID generates a new random Canonical APIKeyID from 16 crypto/rand bytes.
func NewAPIKeyID() (APIKeyID, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate random APIKeyID: %w", err)
	}
	return APIKeyID(hex.EncodeToString(bytes)), nil
}

// NewCredentialID generates a new random Canonical CredentialID from 16 crypto/rand bytes.
func NewCredentialID() (CredentialID, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate random CredentialID: %w", err)
	}
	return CredentialID(hex.EncodeToString(bytes)), nil
}

// ParseAPIKeyID validates and parses a string into an APIKeyID.
func ParseAPIKeyID(s string) (APIKeyID, error) {
	id := APIKeyID(s)
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

// ParseCredentialID validates and parses a string into a CredentialID.
func ParseCredentialID(s string) (CredentialID, error) {
	id := CredentialID(s)
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

func (id APIKeyID) String() string {
	return string(id)
}

func (id APIKeyID) Validate() error {
	if !isValidCanonicalID(string(id)) {
		return fmt.Errorf("%w: must be exactly 32 lowercase hexadecimal characters", ErrInvalidAPIKeyID)
	}
	return nil
}

func (id CredentialID) String() string {
	return string(id)
}

func (id CredentialID) Validate() error {
	if !isValidCanonicalID(string(id)) {
		return fmt.Errorf("%w: must be exactly 32 lowercase hexadecimal characters", ErrInvalidCredentialID)
	}
	return nil
}

func isValidCanonicalID(s string) bool {
	if len(s) != canonicalIDHexLength {
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
