//go:build !windows

package usage

import "os"

func replaceArchiveFile(from, to string) error {
	return os.Rename(from, to)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func archiveFilePermissionsPrivate(info os.FileInfo) bool {
	return info.Mode().Perm()&0o077 == 0
}
