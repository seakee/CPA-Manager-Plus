//go:build !windows

package testutil

import (
	"fmt"
	"testing"
	"time"
)

// rewriteDeadline bounds re-applying one idempotent rewrite while waiting for
// the file system's change stamp to cross a tick. Coarse clocks stamp ctime
// once per jiffy (about 1 ms on tmpfs, a few on ext4), so a handful of attempts
// is enough; the deadline only exists so a file system that never advances the
// stamp fails by name instead of looping.
const rewriteDeadline = 2 * time.Second

// applyDetectableRewrite re-applies write until the change stamp leaves before.
//
// No API sets ctime, so this is the only way to make the difference real: each
// attempt repeats the whole idempotent mutation (the fixture's write plus its
// mtime restore), and every attempt is cheap, so the loop simply retries until
// a write lands in the next tick. It never sleeps between attempts, because the
// next attempt is itself the wait and sleeping would only lose ticks.
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
