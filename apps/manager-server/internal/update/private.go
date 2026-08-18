package update

import (
	"fmt"
	"os"
	"path/filepath"
)

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if err := restrictPrivatePath(path, true); err != nil {
		return fmt.Errorf("restrict private directory %s: %w", path, err)
	}
	return nil
}

func restrictPrivateFile(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	if err := restrictPrivatePath(path, false); err != nil {
		return fmt.Errorf("restrict private file %s: %w", path, err)
	}
	return nil
}

func writePrivateAtomic(path string, data []byte) error {
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "atomic-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := restrictPrivateFile(temporaryPath); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return restrictPrivateFile(path)
}
