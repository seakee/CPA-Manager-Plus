package buildinfo

import "testing"

func TestRuntimeVersionFallback(t *testing.T) {
	previous := Version
	t.Cleanup(func() { Version = previous })

	Version = "  "
	if got := RuntimeVersion(); got != "dev" {
		t.Fatalf("RuntimeVersion() = %q, want dev", got)
	}

	Version = " v1.2.3 "
	if got := RuntimeVersion(); got != "v1.2.3" {
		t.Fatalf("RuntimeVersion() = %q, want v1.2.3", got)
	}
}
