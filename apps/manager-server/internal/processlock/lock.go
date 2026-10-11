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
)

type Lock struct {
	file         *os.File
	databasePath string
	lockPath     string
	closeOnce    sync.Once
	closeErr     error
}

// AcquireExisting takes the normal Manager ownership lock without creating a
// directory or file. The returned lock can be retained for normal startup;
// releasing it between inspection and startup would lose the ownership fence.
func AcquireExisting(databasePath string) (*Lock, error) {
	absolutePath, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, err
	}
	// Resolve an existing parent without using resolveDatabasePath, which mkdirs.
	directory, err := filepath.EvalSymlinks(filepath.Dir(absolutePath))
	if os.IsNotExist(err) {
		return nil, ErrNoExistingLock
	}
	if err != nil {
		return nil, err
	}
	absolutePath = filepath.Join(directory, filepath.Base(absolutePath))
	if info, err := os.Lstat(absolutePath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("database inspection does not accept a symbolic link")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	lockPath := absolutePath + ".manager.lock"
	if err := rejectHardLinkedDatabase(absolutePath); err != nil {
		return nil, err
	}
	info, err := os.Lstat(lockPath)
	if os.IsNotExist(err) {
		return nil, ErrNoExistingLock
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("existing process lock is not a regular file")
	}
	file, err := os.Open(lockPath)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err == nil && !os.SameFile(info, opened) {
		err = errors.New("existing process lock changed during inspection")
	}
	if err == nil {
		var multiple bool
		multiple, err = hasMultipleLinks(file)
		if multiple {
			err = ErrHardLinked
		}
	}
	if err == nil {
		err = lockFile(file)
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := rejectHardLinkedDatabase(absolutePath); err != nil && !os.IsNotExist(err) {
		_ = errors.Join(unlockFile(file), file.Close())
		return nil, err
	}
	return &Lock{file: file, databasePath: absolutePath, lockPath: lockPath}, nil
}

func Acquire(databasePath string) (*Lock, error) {
	absolutePath, err := resolveDatabasePath(databasePath)
	if err != nil {
		return nil, err
	}
	lockPath := absolutePath + ".manager.lock"
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
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
	if err := rejectHardLinkedDatabase(absolutePath); err != nil {
		_ = errors.Join(unlockFile(file), file.Close())
		return nil, err
	}
	return &Lock{
		file:         file,
		databasePath: absolutePath,
		lockPath:     lockPath,
	}, nil
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
	return rejectHardLinkedDatabase(l.databasePath)
}

func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		l.closeErr = errors.Join(unlockFile(l.file), l.file.Close())
	})
	return l.closeErr
}
