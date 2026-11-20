//go:build windows

package upgrade

import "testing"

func makeTestFIFO(t *testing.T, _ string) {
	t.Helper()
	t.Skip("FIFO special files require a Unix filesystem")
}
