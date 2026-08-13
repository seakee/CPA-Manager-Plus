package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const backupCapacityReserve = uint64(64 * 1024 * 1024)

func CheckBackupCapacity(manifest InstallManifest, dataPaths []string) error {
	var required uint64
	managed := RuntimeManagedFiles()
	for _, path := range []string{
		filepath.Join(manifest.InstallRoot, managed.Binary),
		filepath.Join(manifest.InstallRoot, managed.Updater),
		filepath.Join(manifest.InstallRoot, managed.Control),
		filepath.Join(manifest.InstallRoot, "config.json"),
	} {
		size, err := pathSize(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		required += size
	}
	for _, path := range dataPaths {
		size, err := pathSize(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		required += size
	}
	available, err := diskFreeBytes(manifest.BackupRoot)
	if err != nil {
		return fmt.Errorf("read backup volume capacity: %w", err)
	}
	if available < required+backupCapacityReserve {
		return fmt.Errorf("insufficient free space for rollback backup: need at least %d bytes, have %d bytes", required+backupCapacityReserve, available)
	}
	return nil
}

func pathSize(path string) (uint64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return 0, errors.New("backup preflight refuses symbolic links")
	}
	if info.Mode().IsRegular() {
		return uint64(info.Size()), nil
	}
	if !info.IsDir() {
		return 0, errors.New("backup preflight only supports regular files and directories")
	}
	var total uint64
	err = filepath.WalkDir(path, func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		candidateInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if candidateInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("backup preflight refuses symbolic links")
		}
		if candidateInfo.Mode().IsRegular() {
			total += uint64(candidateInfo.Size())
			return nil
		}
		if !candidateInfo.IsDir() {
			return errors.New("backup preflight only supports regular files and directories")
		}
		return nil
	})
	return total, err
}
