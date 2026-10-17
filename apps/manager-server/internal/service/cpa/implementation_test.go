package cpa

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// The compiled digest must cover both safe-read families, including the
// explicit legacy adapter. This mirrors the checked-in destination pin test.
func TestSafeReadImplementationDigestCoversBothSourceGenerations(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", "..", ".."))
	hash := sha256.New()
	for _, name := range []string{"v8.go", "v8_adoption.go", "legacy_adoption.go"} {
		data, err := os.ReadFile(filepath.Join(root, "apps", "manager-server", "internal", "service", "cpa", name))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = hash.Write(data)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != SafeReadImplementationSHA256() {
		t.Fatalf("safe-read implementation digest drift: file=%s compiled=%s", got, SafeReadImplementationSHA256())
	}
	if digest := SafeReadImplementationSHA256(); len(digest) != 64 {
		t.Fatalf("invalid safe-read digest %q", digest)
	}
}
