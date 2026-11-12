//go:build !windows

package testutil

import (
	"os"
	"testing"
)

// ageChangeStamp handles the platforms whose change stamp is the POSIX ctime.
// No API sets ctime, so the helper cannot pin it into the past. It instead
// proves the running file system advances ctime across a real in-place write
// and fails with the reason when it cannot, so a fixture never relies on a
// stamp that might not move.
//
// The proof writes the file's own bytes back at offset 0 and restores its
// mtime, which is exactly the mutation the admission fixtures apply: if ctime
// survives that unchanged, the fixture cannot express a detectable change here
// and the test says so instead of passing or failing on timing.
func ageChangeStamp(t testing.TB, path string, stamp func(string) (string, error)) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s to age its change stamp: %v", path, err)
	}
	if len(content) == 0 {
		t.Fatalf("%s is empty; a rewrite cannot be expressed", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s to age its change stamp: %v", path, err)
	}
	before, err := stamp(path)
	if err != nil {
		t.Fatalf("read the change stamp of %s: %v", path, err)
	}

	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s to age its change stamp: %v", path, err)
	}
	if _, err := file.WriteAt(content[:min(len(content), 16)], 0); err != nil {
		_ = file.Close()
		t.Fatalf("rewrite %s to age its change stamp: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s to age its change stamp: %v", path, err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("restore the mtime of %s: %v", path, err)
	}

	RequireChangeStampAdvanced(t, path, before, stamp)
}
