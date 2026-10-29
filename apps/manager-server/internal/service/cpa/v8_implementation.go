package cpa

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
)

//go:embed v8.go v8_adoption.go legacy_adoption.go adoption_ref.go
var safeReadImplementation embed.FS

// SafeReadImplementationSHA256 binds a Fresh consumer acceptance to the actual
// compiled management safe-read/adoption implementation: both the v8 probe, the
// explicit legacy_managed v0 adapter, and the one resolver that decides which
// reference an adoption item is gated under and which references a discovered
// root contributes (`adoption_ref.go`). The resolver is pinned here because it
// decides what an adoption copies: an unpinned classification could change the
// copy gate without invalidating an acceptance.
//
// The resolver's own bytes are not enough, because it classifies against the
// gateway page table in `config_schema.go`, which the gateway page edits often
// and which is not otherwise pinned. The adoption universe digest is folded in
// so that adding a gateway leaf, moving a rule family or changing what an item
// classifies as invalidates an acceptance, without subjecting every gateway-page
// edit to a whole-file hash. It grants no qualification itself.
func SafeReadImplementationSHA256() string {
	hash := sha256.New()
	for _, name := range []string{"v8.go", "v8_adoption.go", "legacy_adoption.go", "adoption_ref.go"} {
		data, _ := safeReadImplementation.ReadFile(name)
		_, _ = hash.Write(data)
	}
	_, _ = hash.Write([]byte(AdoptionUniverseDigest()))
	return hex.EncodeToString(hash.Sum(nil))
}
