package testutil

import "testing"

// AgeChangeStamp makes a later in-place rewrite of path report a different
// kernel change stamp (ctime / FILE_BASIC_INFO.ChangeTime) than the one path
// has now.
//
// The admission recorders guard a rewrite by comparing identity, size, mtime
// and change stamp. A fixture that rewrites a file back to its own bytes and
// restores its mtime leaves the first three equal by construction, so the
// change stamp is the only evidence left: the fixture proves nothing unless
// that stamp is guaranteed to move. Windows can move ChangeTime backwards, so
// the helper pins it to a fixed instant in the past. Unix file systems cannot
// set ctime, so the helper instead proves the running file system advances it
// across a real rewrite and fails with the reason when it does not.
func AgeChangeStamp(t testing.TB, path string, stamp func(string) (string, error)) {
	t.Helper()
	ageChangeStamp(t, path, stamp)
}

// RequireChangeStampAdvanced fails unless stamp(path) no longer equals before.
// Fixtures call it after applying their rewrite so a mutation that could not
// express the change names the broken precondition instead of surfacing as a
// detector failure.
func RequireChangeStampAdvanced(t testing.TB, path string, before string, stamp func(string) (string, error)) {
	t.Helper()
	after, err := stamp(path)
	if err != nil {
		t.Fatalf("read change stamp after the rewrite: %v", err)
	}
	if after == before {
		t.Fatalf("change stamp did not advance across the rewrite (%q): the fixture cannot express a detectable change on this file system", after)
	}
}
