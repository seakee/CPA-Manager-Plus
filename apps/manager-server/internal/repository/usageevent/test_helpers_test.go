package usageevent

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func canonicalTestHash(hash string) string {
	if usage.IsCanonicalSHA256Hex(hash) {
		return hash
	}
	sum := sha256.Sum256([]byte(hash))
	return hex.EncodeToString(sum[:])
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
