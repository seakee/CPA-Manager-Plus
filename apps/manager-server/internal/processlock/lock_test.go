package processlock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
