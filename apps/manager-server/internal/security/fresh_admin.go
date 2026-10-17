package security

import (
	"errors"
	"strings"
	"unicode"
)

// ValidateFreshAdminKey freezes the bounded product key contract. The legacy
// verifier remains unchanged for existing installations.
func ValidateFreshAdminKey(key string) error {
	if len(key) < 24 || len(key) > 256 || strings.TrimSpace(key) != key {
		return errors.New("admin key must contain 24 to 256 non-whitespace characters")
	}
	classes := map[int]bool{}
	unique := map[rune]bool{}
	for _, r := range key {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("admin key contains whitespace or control characters")
		}
		unique[r] = true
		switch {
		case unicode.IsLower(r):
			classes[0] = true
		case unicode.IsUpper(r):
			classes[1] = true
		case unicode.IsDigit(r):
			classes[2] = true
		default:
			classes[3] = true
		}
	}
	if len(classes) < 3 || len(unique) < 12 {
		return errors.New("admin key requires at least three character classes and twelve distinct characters")
	}
	return nil
}
