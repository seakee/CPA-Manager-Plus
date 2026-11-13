package testutil

import (
	"os"
	"testing"
	"time"
)

// ApplyDetectableRewrite performs write at path and then checks that the file's
// kernel change stamp (ctime / FILE_BASIC_INFO.ChangeTime) has actually moved
// away from before.
//
// The admission recorders reject a rewrite by comparing identity, size, mtime
// and change stamp. A fixture that rewrites a file and restores its mtime keeps
// identity, size and mtime equal by construction, so the change stamp is the
// only evidence left and the fixture proves nothing unless that stamp moves.
// Linux file systems stamp ctime from a coarse clock and only advance it when a
// write crosses a tick boundary, so a single write usually lands inside the tick
// the baseline was taken in: on tmpfs a tick measured near one millisecond and
// 196 of 200 back-to-back rewrites left ctime unchanged. Platforms that can pin
// the stamp into the past make one write enough; the others re-apply the same
// write until the stamp moves, which costs less than a tick and never sleeps
// past a deadline.
//
// It returns an error instead of failing the test so a caller inside a test
// seam can report it through the assertion that follows the seam.
func ApplyDetectableRewrite(t testing.TB, path string, before string, write func() error, stamp func(string) (string, error)) error {
	t.Helper()
	return applyDetectableRewrite(t, path, before, write, stamp)
}

// CanonicalModTime reports the modification time path actually stores after
// mt is restored to it.
//
// File systems differ in the precision they keep: ext4 stores whole seconds,
// APFS and NTFS keep 100 ns ticks. A fixture that restores the mtime it read
// before the rewrite then compares that value against what the file system
// kept, so it has to adopt the stored value instead of the requested one.
func CanonicalModTime(t testing.TB, path string, mt time.Time) time.Time {
	t.Helper()
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatalf("restore the mtime of %s: %v", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s to read its stored mtime: %v", path, err)
	}
	return info.ModTime()
}
