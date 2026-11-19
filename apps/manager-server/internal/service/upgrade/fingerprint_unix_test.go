//go:build !windows

package upgrade

import (
	"syscall"
	"testing"
)

func makeTestFIFO(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo %s: %v", path, err)
	}
}
