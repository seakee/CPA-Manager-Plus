//go:build !windows

package copyinmarker

import (
	"errors"
	"os"
)

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func replaceFile(from, to string) error { return os.Rename(from, to) }
