package testutil

import "testing"

// PrepareDetectableRewrite prepares path before baseline metadata and change
// stamps are captured for an mtime-restored rewrite test.
//
// On Windows, it pins FILE_BASIC_INFO (CreationTime, LastAccessTime,
// LastWriteTime and ChangeTime) to a fixed past instant and verifies that both
// os.Lstat and stamp report that instant, so origInfo, origStamp and the
// production detector's baseline all observe the pinned timestamp before the
// probe starts.
//
// On Unix platforms, where ctime cannot be backdated via syscall, it normalizes
// path's modification time to the precision stored by the underlying file
// system (for example whole seconds on coarse ext4, nanoseconds on APFS/tmpfs)
// so restoring origInfo.ModTime() during the rewrite leaves the exact same
// stored mtime on disk, and verifies stamp is readable.
func PrepareDetectableRewrite(t testing.TB, path string, stamp func(string) (string, error)) {
	t.Helper()
	if err := prepareDetectableRewrite(t, path, stamp); err != nil {
		t.Fatalf("prepare detectable rewrite for %s: %v", path, err)
	}
}

// ApplyDetectableRewrite performs write at path inside a mid-probe seam and
// verifies that the file's kernel change stamp (ctime /
// FILE_BASIC_INFO.ChangeTime) moved away from before.
//
// The admission and status detectors reject an in-place, same-size,
// mtime-restored rewrite through the change stamp because identity, size and
// mtime stay equal by construction. On Windows, PrepareDetectableRewrite has
// already pinned the baseline stamp into the past, so one write advances
// ChangeTime to the current system clock and ApplyDetectableRewrite verifies
// stamp(path) != before. On Unix, where coarse kernel clocks advance ctime once
// per jiffy, ApplyDetectableRewrite re-applies the idempotent write without
// sleeping until stamp(path) != before, bounded by a safety deadline.
//
// It returns an error instead of calling t.Fatalf directly so a caller inside a
// synchronous test seam can report any failure right after the probe returns.
func ApplyDetectableRewrite(t testing.TB, path string, before string, write func() error, stamp func(string) (string, error)) error {
	t.Helper()
	return applyDetectableRewrite(t, path, before, write, stamp)
}
