package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// A consumer acceptance binds the compiled Fresh qualification/adoption/
// checkpoint sources. Recomputing them from the checkout catches a Windows
// CRLF/eol regression and any semantic drift.
func TestFreshStrategyImplementationDigestCoversSemanticSources(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", "..", ".."))
	hash := sha256.New()
	for _, name := range []string{"qualification.go", "adoption.go", "fresh.go"} {
		data, err := os.ReadFile(filepath.Join(root, "apps", "manager-server", "internal", "domain", "bootstrap", name))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = hash.Write(data)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != FreshStrategyImplementationSHA256() {
		t.Fatalf("Fresh strategy implementation digest drift: file=%s compiled=%s", got, FreshStrategyImplementationSHA256())
	}
	if digest := FreshStrategyImplementationSHA256(); len(digest) != 64 {
		t.Fatalf("invalid Fresh strategy digest %q", digest)
	}
}
