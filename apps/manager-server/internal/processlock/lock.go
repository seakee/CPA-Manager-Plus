package processlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var (
	ErrLocked         = errors.New("manager database process lock is already held")
	ErrHardLinked     = errors.New("manager database has multiple hard links")
	ErrNoExistingLock = errors.New("manager database process lock does not exist")
	// ErrSymlinkedDatabase reports that the configured database path is a
	// symbolic link. Every read-only path refuses it: the file that is inspected
	// and the file that is read have to be the same one, and a link can be
	// repointed between the two.
	ErrSymlinkedDatabase = errors.New("database inspection does not accept a symbolic link")
)

type Lock struct {
	file         *os.File
	databasePath string
	lockPath     string
	closeOnce    sync.Once
	closeErr     error
	createdNew   bool
}

// AcquireExisting takes the normal Manager ownership lock without creating a
// directory or file. The returned lock can be retained for normal startup;
// releasing it between inspection and startup would lose the ownership fence.
func AcquireExisting(databasePath string) (*Lock, error) {
	absolutePath, lockPath, marker, err := existingLockTarget(databasePath)
	if err != nil {
		return nil, err
	}
	file, err := openLockFile(lockPath, os.O_RDWR)
	if err != nil {
		return nil, err
	}
	if err := openedMarker(file, marker); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	lock := &Lock{file: file, databasePath: absolutePath, lockPath: lockPath}
	if err := lock.Validate(); err != nil {
		_ = errors.Join(unlockFile(file), file.Close())
		return nil, err
	}
	return lock, nil
}

// ProbeExisting observes the source ownership marker without taking the fence:
// a shared, non-blocking lock proves that no writer holds it and is released
// before returning. A read-only caller must not use AcquireExisting, which needs
// write access to the marker and would exclude the 1.x writer it only observes.
// ErrLocked means a live writer holds the source; ErrNoExistingLock means there
// is no ownership marker to inspect.
func ProbeExisting(databasePath string) error {
	_, lockPath, marker, err := existingLockTarget(databasePath)
	if err != nil {
		return err
	}
	file, err := openLockFile(lockPath, os.O_RDONLY)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err := openedMarker(file, marker); err != nil {
		return err
	}
	return probeLockFile(file)
}

// existingLockTarget validates the source database and its persistent ownership
// marker without opening the marker. It is the shared guard of the callers that
// open it: AcquireExisting takes the fence, ProbeExisting only observes it.
func existingLockTarget(databasePath string) (string, string, os.FileInfo, error) {
	absolutePath, err := filepath.Abs(databasePath)
	if err != nil {
		return "", "", nil, err
	}
	// Resolve an existing parent without using resolveDatabasePath, which mkdirs.
	directory, err := filepath.EvalSymlinks(filepath.Dir(absolutePath))
	if os.IsNotExist(err) {
		return "", "", nil, ErrNoExistingLock
	}
	if err != nil {
		return "", "", nil, err
	}
	absolutePath = filepath.Join(directory, filepath.Base(absolutePath))
	if _, err := InspectDatabaseFile(absolutePath); err != nil && !os.IsNotExist(err) {
		return "", "", nil, err
	}
	lockPath := absolutePath + ".manager.lock"
	marker, err := os.Lstat(lockPath)
	if os.IsNotExist(err) {
		return "", "", nil, ErrNoExistingLock
	}
	if err != nil {
		return "", "", nil, err
	}
	if !marker.Mode().IsRegular() {
		return "", "", nil, errors.New("existing process lock is not a regular file")
	}
	return absolutePath, lockPath, marker, nil
}

// InspectDatabaseFile applies the shared source-database file policy: the path
// must name the database itself (never a symbolic link), a regular file, and a
// file reachable by exactly one link. It returns the inspected identity, which a
// reader compares its own descriptor against. A missing database is reported as
// os.IsNotExist, so a caller that tolerates an absent file can say so.
func InspectDatabaseFile(databasePath string) (os.FileInfo, error) {
	info, err := os.Lstat(databasePath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s", ErrSymlinkedDatabase, databasePath)
	}
	if err := rejectHardLinkedDatabase(databasePath); err != nil {
		return nil, err
	}
	return info, nil
}

// openedMarker re-checks that the opened descriptor is still the marker the
// target named, and that no second hard link reaches it.
func openedMarker(file *os.File, marker os.FileInfo) error {
	return openedIdentity(file, marker, errors.New("existing process lock changed during inspection"))
}

// OpenedDatabase is the reader's openedMarker: it proves the descriptor a
// read-only reader opened is still the file that was inspected, and that no
// second hard link reaches it. A file swapped between the inspection and the
// open is refused instead of read.
func OpenedDatabase(file *os.File, inspected os.FileInfo) error {
	return openedIdentity(file, inspected, errors.New("source database changed during inspection"))
}

// openedIdentity proves an opened descriptor is the file that was inspected and
// that it is reachable by exactly one link.
func openedIdentity(file *os.File, inspected os.FileInfo, replaced error) error {
	opened, err := file.Stat()
	if err == nil && !os.SameFile(inspected, opened) {
		err = replaced
	}
	if err != nil {
		return err
	}
	if multiple, linkErr := hasMultipleLinks(file); linkErr != nil {
		return linkErr
	} else if multiple {
		return ErrHardLinked
	}
	return nil
}

func Acquire(databasePath string) (*Lock, error) {
	return acquire(databasePath, false)
}

// AcquireNew proves that this caller created the persistent ownership marker.
// An existing marker is rejected atomically, even when its owner released it.
func AcquireNew(databasePath string) (*Lock, error) {
	return acquire(databasePath, true)
}

func acquire(databasePath string, exclusive bool) (*Lock, error) {
	absolutePath, err := resolveDatabasePath(databasePath)
	if err != nil {
		return nil, err
	}
	lockPath := absolutePath + ".manager.lock"
	flags := os.O_CREATE | os.O_RDWR
	if exclusive {
		flags |= os.O_EXCL
	}
	file, err := openLockFile(lockPath, flags)
	if err != nil {
		return nil, fmt.Errorf("open manager database process lock %s: %w", lockPath, err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		if errors.Is(err, ErrLocked) {
			return nil, fmt.Errorf("%w for %s", ErrLocked, absolutePath)
		}
		return nil, fmt.Errorf("acquire manager database process lock %s: %w", lockPath, err)
	}
	lock := &Lock{file: file, databasePath: absolutePath, lockPath: lockPath, createdNew: exclusive}
	if err := lock.Validate(); err != nil {
		_ = errors.Join(unlockFile(file), file.Close())
		return nil, err
	}
	return lock, nil
}

func rejectHardLinkedDatabase(databasePath string) error {
	info, err := os.Stat(databasePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect manager database type %s: %w", databasePath, err)
	}
	// Opening a FIFO can block before preflight gets to its context or inventory.
	if !info.Mode().IsRegular() {
		return fmt.Errorf("manager database is not a regular file: %s", databasePath)
	}
	file, err := os.Open(databasePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect manager database links %s: %w", databasePath, err)
	}
	multiple, inspectErr := hasMultipleLinks(file)
	closeErr := file.Close()
	if inspectErr != nil || closeErr != nil {
		return fmt.Errorf("inspect manager database links %s: %w", databasePath, errors.Join(inspectErr, closeErr))
	}
	if multiple {
		return fmt.Errorf("%w for %s; replace hard-link aliases with one canonical database path", ErrHardLinked, databasePath)
	}
	return nil
}

func resolveDatabasePath(databasePath string) (string, error) {
	absolutePath, err := filepath.Abs(databasePath)
	if err != nil {
		return "", fmt.Errorf("resolve manager database path: %w", err)
	}
	absolutePath = filepath.Clean(absolutePath)
	directory := filepath.Dir(absolutePath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("create manager database directory: %w", err)
	}
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", fmt.Errorf("resolve manager database directory: %w", err)
	}
	resolvedPath := filepath.Join(resolvedDirectory, filepath.Base(absolutePath))
	if existingPath, err := filepath.EvalSymlinks(resolvedPath); err == nil {
		resolvedPath = existingPath
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve manager database file: %w", err)
	} else if info, lstatErr := os.Lstat(resolvedPath); lstatErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("resolve manager database file: %s is a dangling symbolic link", resolvedPath)
		}
	} else if !os.IsNotExist(lstatErr) {
		return "", fmt.Errorf("inspect manager database file: %w", lstatErr)
	}
	return filepath.Clean(resolvedPath), nil
}

func (l *Lock) DatabasePath() string {
	if l == nil {
		return ""
	}
	return l.databasePath
}

func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.lockPath
}

// Validate detects a closed fence or replacement of its persistent lock file.
func (l *Lock) Validate() error {
	if l == nil || l.file == nil {
		return errors.New("manager ownership lock is unavailable")
	}
	held, err := l.file.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(l.lockPath)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(held, current) {
		return errors.New("manager ownership lock was replaced")
	}
	if multiple, err := hasMultipleLinks(l.file); err != nil {
		return err
	} else if multiple {
		return ErrHardLinked
	}
	return rejectHardLinkedDatabase(l.databasePath)
}

func (l *Lock) Close() error {
	return l.close(false)
}

// DiscardNew removes only this caller's exclusively created marker, while its
// ownership fence is still held. Callers must first prove that no source assets
// were written. Closing an ordinary or already closed lock never removes it.
func (l *Lock) DiscardNew() error {
	return l.close(true)
}

func (l *Lock) close(discard bool) error {
	if l == nil || l.file == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		if discard {
			if !l.createdNew {
				l.closeErr = errors.New("cannot discard a pre-existing ownership marker")
			} else if err := l.Validate(); err != nil {
				l.closeErr = err
			} else if _, err := os.Lstat(l.databasePath); !os.IsNotExist(err) {
				l.closeErr = errors.New("cannot discard ownership marker with database evidence")
			} else if multiple, err := hasMultipleLinks(l.file); err != nil || multiple {
				l.closeErr = errors.Join(err, ErrHardLinked)
			} else {
				l.closeErr = os.Remove(l.lockPath)
			}
		}
		l.closeErr = errors.Join(l.closeErr, unlockFile(l.file), l.file.Close())
	})
	return l.closeErr
}
