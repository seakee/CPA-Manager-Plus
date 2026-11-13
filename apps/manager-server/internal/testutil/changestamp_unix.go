//go:build !windows

package testutil

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// rewriteDeadline bounds re-applying one idempotent rewrite while waiting for
// the file system's change stamp to cross a tick. Coarse clocks stamp ctime
// once per jiffy (about 1 ms on tmpfs, a few on ext4), so a handful of attempts
// is enough; the deadline only exists so a file system that never advances the
// stamp fails by name instead of looping.
const rewriteDeadline = 2 * time.Second

// prepareDetectableRewrite normalizes path's modification time to the stored
// precision of the underlying file system before baseline capture.
func prepareDetectableRewrite(t testing.TB, path string, stamp func(string) (string, error)) error {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		return fmt.Errorf("normalize stored mtime of %s: %w", path, err)
	}
	if _, err := stamp(path); err != nil {
		return fmt.Errorf("read baseline change stamp of %s: %w", path, err)
	}
	return nil
}

// applyDetectableRewrite re-applies write until the change stamp leaves before.
//
// No API sets ctime, so each attempt repeats the idempotent mutation (the
// fixture's write plus its mtime restore) without sleeping until a write lands
// in the next kernel ctime tick.
func applyDetectableRewrite(t testing.TB, path string, before string, write func() error, stamp func(string) (string, error)) error {
	t.Helper()
	deadline := time.Now().Add(rewriteDeadline)
	var attempts int
	for {
		attempts++
		if err := write(); err != nil {
			return err
		}
		after, err := stamp(path)
		if err != nil {
			return err
		}
		if after != before {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("change stamp of %s stayed %q across %d mtime-restored rewrites within %s: the file system does not advance ctime for this mutation", path, before, attempts, rewriteDeadline)
		}
	}
}
