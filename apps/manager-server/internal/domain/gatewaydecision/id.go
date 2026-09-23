package gatewaydecision

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// DecisionID identifies one immutable quota decision audit record.
type DecisionID string

// DedupeKey is a producer-supplied SHA-256 of canonical non-secret inputs.
// The producer's canonical input recipe belongs to the evaluator, not this domain.
type DedupeKey string

var (
	ErrInvalidDecisionID = errors.New("invalid DecisionID")
	ErrInvalidDedupeKey  = errors.New("invalid DedupeKey")
)

func NewDecisionID() (DecisionID, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate DecisionID: %w", err)
	}
	return DecisionID(hex.EncodeToString(bytes[:])), nil
}

func ParseDecisionID(raw string) (DecisionID, error) {
	id := DecisionID(raw)
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

func (id DecisionID) String() string { return string(id) }

func (id DecisionID) Validate() error {
	if !isLowerHex(string(id), 32) {
		return fmt.Errorf("%w: expected 32 lowercase hexadecimal characters", ErrInvalidDecisionID)
	}
	return nil
}

func ParseDedupeKey(raw string) (DedupeKey, error) {
	key := DedupeKey(raw)
	if err := key.Validate(); err != nil {
		return "", err
	}
	return key, nil
}

func (key DedupeKey) String() string { return string(key) }

func (key DedupeKey) Validate() error {
	if !isLowerHex(string(key), 64) {
		return fmt.Errorf("%w: expected 64 lowercase hexadecimal characters", ErrInvalidDedupeKey)
	}
	return nil
}

func isLowerHex(s string, size int) bool {
	if len(s) != size {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
