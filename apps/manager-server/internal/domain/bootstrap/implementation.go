package bootstrap

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
)

// The Fresh strategy acceptances bind these semantic source bytes, not a
// self-referential commit SHA. A change to the qualification, adoption decision
// or checkpoint contract requires renewed acceptance.
//
//go:embed qualification.go adoption.go fresh.go
var freshStrategyImplementation embed.FS

// FreshStrategyImplementationSHA256 binds the compiled Fresh strategy domain
// contract. It grants no qualification itself and is independent of the
// original evidence execution HEADs.
func FreshStrategyImplementationSHA256() string {
	hash := sha256.New()
	for _, name := range []string{"qualification.go", "adoption.go", "fresh.go"} {
		data, _ := freshStrategyImplementation.ReadFile(name)
		_, _ = hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
