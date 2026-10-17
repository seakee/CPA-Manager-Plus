package cpa

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
)

//go:embed v8.go v8_adoption.go legacy_adoption.go
var safeReadImplementation embed.FS

// SafeReadImplementationSHA256 binds a Fresh consumer acceptance to the actual
// compiled management safe-read/adoption implementation: both the v8 probe and
// the explicit legacy_managed v0 adapter. It grants no qualification itself.
func SafeReadImplementationSHA256() string {
	hash := sha256.New()
	for _, name := range []string{"v8.go", "v8_adoption.go", "legacy_adoption.go"} {
		data, _ := safeReadImplementation.ReadFile(name)
		_, _ = hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
