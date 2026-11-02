package processlock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireNewCreatesOnceAndPreservesExistingOwnershipMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "usage.sqlite")
	first, err := AcquireNew(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	if lock, err := AcquireNew(path); lock != nil || !errors.Is(err, os.ErrExist) {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("existing held marker accepted as new: lock=%v err=%v", lock, err)
	}
	if lock, err := Acquire(path); lock != nil || !errors.Is(err, ErrLocked) {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("new marker lost ownership fence: lock=%v err=%v", lock, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(first.Path())
	if err != nil {
		t.Fatal(err)
	}
	if lock, err := AcquireNew(path); lock != nil || !errors.Is(err, os.ErrExist) {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("existing released marker accepted as new: lock=%v err=%v", lock, err)
	}
	after, err := os.Lstat(first.Path())
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("exclusive creation changed existing marker")
	}
	// Existing native-v2 owners retain the normal reusable-lock behavior.
	reopened, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("lock acquisition created database")
	}
}

func TestDiscardNewMarkerRejectsStaleOpeners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	owner, err := AcquireNew(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	// A contender opened the old inode before cleanup, but has not locked it.
	stale, err := openLockFile(owner.Path(), os.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	if err := owner.DiscardNew(); err != nil {
		t.Fatal(err)
	}
	if err := lockFile(stale); err == nil {
		staleLock := &Lock{file: stale, databasePath: path, lockPath: owner.Path()}
		if err := staleLock.Validate(); err == nil {
			t.Fatal("unlinked opener retained ownership authority")
		}
		_ = unlockFile(stale)
	}
	// Windows delete-pending ends when the final stale handle is closed.
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	retry, err := AcquireNew(path)
	if err != nil {
		t.Fatal(err)
	}
	defer retry.Close()
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := retry.Validate(); err != nil {
		t.Fatal("repeated old close removed the new owner's marker")
	}
}

func TestAcquireNewAndDiscardSupportLongPaths(t *testing.T) {
	path := t.TempDir()
	for len(path) < 280 {
		path = filepath.Join(path, "long-manager-directory")
	}
	path = filepath.Join(path, "usage.sqlite")
	lock, err := AcquireNew(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.DiscardNew(); err != nil {
		t.Fatal(err)
	}
	retry, err := AcquireNew(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := retry.DiscardNew(); err != nil {
		t.Fatal(err)
	}
}

func TestDiscardNewPreservesUnownedAndChangedMarkers(t *testing.T) {
	for _, kind := range []string{"existing", "closed", "database", "replacement"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "usage.sqlite")
			acquire := AcquireNew
			if kind == "existing" {
				acquire = Acquire
			}
			owner, err := acquire(path)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			switch kind {
			case "closed":
				if err := owner.Close(); err != nil {
					t.Fatal(err)
				}
			case "database":
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				if err := os.Rename(owner.Path(), owner.Path()+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(owner.Path(), []byte("replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(owner.Path())
			if err != nil {
				t.Fatal(err)
			}
			_ = owner.DiscardNew()
			after, err := os.Lstat(owner.Path())
			if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() {
				t.Fatal("cleanup removed or changed unowned source evidence")
			}
		})
	}
}

func TestAcquireExistingNeverCreatesSourceAndRetainsNormalOwnership(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "missing", "usage.sqlite")
	lock, err := AcquireExisting(path)
	if lock != nil || !errors.Is(err, ErrNoExistingLock) {
		t.Fatalf("missing lock=%v err=%v", lock, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("inspection created directory")
	}
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(first.Path())
	if err != nil {
		t.Fatal(err)
	}
	lock, err = AcquireExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := lock.Validate(); err != nil {
		t.Fatal(err)
	}
	if second, err := Acquire(path); second != nil || !errors.Is(err, ErrLocked) {
		t.Fatalf("second=%v err=%v", second, err)
	}
	after, err := os.Stat(first.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(after.ModTime()) || info.Size() != after.Size() {
		t.Fatal("inspection changed persistent lock")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Validate(); err == nil {
		t.Fatal("closed lock retained authority")
	}
}

// TestAcquireExistingReadOnlyTakesAReadOnlyMarkerAndRefusesALiveHolder is the
// Medium finding: the 1.x source is preserved read-only, so a fence that opens the
// marker O_RDWR cannot prove exclusivity there. The read-only descriptor is the
// same exclusive fence — it still excludes a live holder — and it works on a
// marker the caller may read but not write.
func TestAcquireExistingReadOnlyTakesAReadOnlyMarkerAndRefusesALiveHolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens a 0400 marker for writing, so the read-only case is not observable")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	if err := os.WriteFile(path, []byte("sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := path + ".manager.lock"
	if err := os.WriteFile(marker, nil, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(marker, 0o400); err != nil {
		t.Fatal(err)
	}
	// The fixture models the read-only source: the marker is readable and not
	// writable, so the accepted read-write fence cannot open it.
	if writable, err := os.OpenFile(marker, os.O_RDWR, 0); err == nil {
		_ = writable.Close()
		t.Fatal("the fixture marker is writable, so it does not model a read-only source")
	}
	if lock, err := AcquireExisting(path); lock != nil || err == nil {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("the read-write fence opened a 0400 marker: lock=%v err=%v", lock, err)
	}
	lock, err := AcquireExistingReadOnly(path)
	if err != nil {
		t.Fatalf("the read-only fence refused a readable read-only marker: %v", err)
	}
	if err := lock.Validate(); err != nil {
		t.Fatalf("the read-only fence does not validate: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("close the read-only fence: %v", err)
	}
	// A live holder excludes the read-only fence exactly as it excludes the
	// writable one, so the fence is still exclusive.
	holder, err := os.OpenFile(marker, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	if err := lockFile(holder); err != nil {
		t.Fatalf("hold the marker: %v", err)
	}
	defer func() { _ = unlockFile(holder) }()
	if second, err := AcquireExistingReadOnly(path); second != nil || !errors.Is(err, ErrLocked) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("a live holder was not excluded: lock=%v err=%v", second, err)
	}
}

func TestAcquireExistingRejectsLockSymlinksWithoutFollowingThem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	target := filepath.Join(dir, "owned-lock")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".manager.lock"); err != nil {
		t.Skip(err)
	}
	if lock, err := AcquireExisting(path); lock != nil || err == nil {
		t.Fatal("symbolic ownership accepted")
	}
}

func TestAcquireSerializesDatabaseOwnersAndReleasesOnClose(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "data", "usage.sqlite")
	first, err := Acquire(databasePath)
	if err != nil {
		t.Fatalf("acquire first process lock: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	absolutePath, err := resolveDatabasePath(databasePath)
	if err != nil {
		t.Fatalf("resolve expected database path: %v", err)
	}
	if first.DatabasePath() != absolutePath {
		t.Fatalf("database path = %q, want %q", first.DatabasePath(), absolutePath)
	}
	if first.Path() != first.DatabasePath()+".manager.lock" {
		t.Fatalf("lock path = %q", first.Path())
	}
	if _, err := os.Stat(first.Path()); err != nil {
		t.Fatalf("stat persistent lock file: %v", err)
	}

	second, err := Acquire(databasePath)
	if second != nil || !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock = %#v err=%v, want ErrLocked", second, err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("release first process lock: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first process lock twice: %v", err)
	}
	third, err := Acquire(databasePath)
	if err != nil {
		t.Fatalf("reacquire released process lock: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatalf("release reacquired process lock: %v", err)
	}
}

func TestAcquireCanonicalizesSymlinkedDatabasePaths(t *testing.T) {
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.MkdirAll(realDirectory, 0o755); err != nil {
		t.Fatalf("create real database directory: %v", err)
	}
	aliasDirectory := filepath.Join(root, "alias")
	if err := os.Symlink(realDirectory, aliasDirectory); err != nil {
		t.Skipf("create database directory symlink: %v", err)
	}
	realPath := filepath.Join(realDirectory, "usage.sqlite")
	aliasPath := filepath.Join(aliasDirectory, "usage.sqlite")
	first, err := Acquire(realPath)
	if err != nil {
		t.Fatalf("acquire canonical database path: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	canonicalPath, err := resolveDatabasePath(realPath)
	if err != nil {
		t.Fatalf("resolve expected canonical path: %v", err)
	}
	if first.DatabasePath() != canonicalPath {
		t.Fatalf("canonical database path = %q, want %q", first.DatabasePath(), canonicalPath)
	}
	second, err := Acquire(aliasPath)
	if second != nil || !errors.Is(err, ErrLocked) {
		t.Fatalf("symlink alias lock = %#v err=%v, want ErrLocked", second, err)
	}
}

func TestAcquireRejectsDanglingDatabaseSymlink(t *testing.T) {
	root := t.TempDir()
	targetDirectory := filepath.Join(root, "target")
	if err := os.MkdirAll(targetDirectory, 0o755); err != nil {
		t.Fatalf("create target directory: %v", err)
	}
	targetPath := filepath.Join(targetDirectory, "usage.sqlite")
	aliasPath := filepath.Join(root, "usage-alias.sqlite")
	if err := os.Symlink(targetPath, aliasPath); err != nil {
		t.Skipf("create dangling database symlink: %v", err)
	}

	lock, err := Acquire(aliasPath)
	if lock != nil || err == nil || !strings.Contains(err.Error(), "dangling symbolic link") {
		t.Fatalf("dangling symlink lock = %#v err=%v, want explicit rejection", lock, err)
	}
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("dangling symlink target was unexpectedly created: %v", err)
	}
}

func TestAcquireSerializesHardLinkedDatabasePaths(t *testing.T) {
	root := t.TempDir()
	realPath := filepath.Join(root, "usage.sqlite")
	if err := os.WriteFile(realPath, nil, 0o600); err != nil {
		t.Fatalf("create database file: %v", err)
	}
	aliasPath := filepath.Join(root, "usage-alias.sqlite")
	if err := os.Link(realPath, aliasPath); err != nil {
		t.Skipf("create database hard link: %v", err)
	}
	first, err := Acquire(realPath)
	if first != nil || !errors.Is(err, ErrHardLinked) {
		t.Fatalf("hard-linked database lock = %#v err=%v, want ErrHardLinked", first, err)
	}
	second, err := Acquire(aliasPath)
	if second != nil || !errors.Is(err, ErrHardLinked) {
		t.Fatalf("hard-link alias lock = %#v err=%v, want ErrHardLinked", second, err)
	}
}
