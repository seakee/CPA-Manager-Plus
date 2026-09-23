package resourcepolicy

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// PolicyID is a CPAMP-owned, opaque, non-reusable policy identifier.
type PolicyID string

var ErrInvalidPolicyID = errors.New("invalid PolicyID")

func NewPolicyID() (PolicyID, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate PolicyID: %w", err)
	}
	return PolicyID(hex.EncodeToString(bytes[:])), nil
}

func ParsePolicyID(raw string) (PolicyID, error) {
	id := PolicyID(raw)
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

func (id PolicyID) String() string { return string(id) }

func (id PolicyID) Validate() error {
	if len(id) != 32 {
		return fmt.Errorf("%w: expected 32 lowercase hexadecimal characters", ErrInvalidPolicyID)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return fmt.Errorf("%w: expected 32 lowercase hexadecimal characters", ErrInvalidPolicyID)
		}
	}
	return nil
}
