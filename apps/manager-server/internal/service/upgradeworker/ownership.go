package upgradeworker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const maxTargetOwnershipDepth = 16

var (
	errTargetOwnershipRootInvalid   = errors.New("upgrade_target_ownership_root_invalid")
	errTargetOwnershipSymlink       = errors.New("upgrade_target_ownership_symlink")
	errTargetOwnershipHardlink      = errors.New("upgrade_target_ownership_hardlink")
	errTargetOwnershipCrossMount    = errors.New("upgrade_target_ownership_cross_mount")
	errTargetOwnershipUnsupported   = errors.New("upgrade_target_ownership_entry_unsupported")
	errTargetOwnershipRuntimeDenied = errors.New("upgrade_target_ownership_runtime_excluded")
)

// targetOwnedMembers lists the exact Manager-owned entries inside the target
// data root (/data) that root-executed upgrade steps may create or update.
//
// Excluded by construction:
//   - targetRoot itself (/data is 0:10002 sticky 01771 to protect runtime-secret)
//   - runtime-secret (0:0 mode 0600 supervisor bootstrap secret)
//   - /runtime or any runtime subdirectory
var targetOwnedMembers = [...]string{
	"usage.sqlite",
	"usage.sqlite-wal",
	"usage.sqlite-shm",
	"usage.sqlite-journal",
	"usage.sqlite.manager.lock",
	"data.key",
	"usage-archives",
	".upgrade-copy-in",
}

func isExcludedTargetMember(name string) bool {
	trimmed := strings.TrimSpace(name)
	return trimmed == "" || trimmed == "." || trimmed == ".." ||
		trimmed == "runtime" || trimmed == "runtime-secret" ||
		strings.HasPrefix(trimmed, "runtime.")
}

func validateTargetRootNoSymlinks(targetRoot string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(targetRoot))
	if cleaned == "" || !filepath.IsAbs(cleaned) || cleaned == "/" {
		return "", errTargetOwnershipRootInvalid
	}
	if cleaned == "/runtime" || strings.HasPrefix(cleaned, "/runtime/") {
		return "", errTargetOwnershipRuntimeDenied
	}
	info, err := os.Lstat(cleaned)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errTargetOwnershipSymlink
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", err
	}
	if filepath.Clean(resolved) != cleaned {
		return "", errTargetOwnershipSymlink
	}
	return cleaned, nil
}

// walkTargetOwnership validates targetRoot and traverses only targetOwnedMembers
// without following symlinks, invoking visit(relPath, fullPath, info) for each
// existing Manager-owned file or directory. It never visits targetRoot itself,
// runtime-secret, or any runtime directory.
func walkTargetOwnership(targetRoot string, visit func(relPath, fullPath string, info os.FileInfo) error) error {
	root, err := validateTargetRootNoSymlinks(targetRoot)
	if err != nil {
		return err
	}
	for _, name := range targetOwnedMembers {
		if isExcludedTargetMember(name) {
			return errTargetOwnershipRuntimeDenied
		}
		if err := walkTargetMember(root, name, 1, visit); err != nil {
			return err
		}
	}
	return nil
}

func walkTargetMember(root, relPath string, depth int, visit func(relPath, fullPath string, info os.FileInfo) error) error {
	if depth > maxTargetOwnershipDepth {
		return errTargetOwnershipUnsupported
	}
	fullPath := filepath.Join(root, relPath)
	info, err := os.Lstat(fullPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errTargetOwnershipSymlink
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return errTargetOwnershipUnsupported
	}
	if info.Mode().IsRegular() && fileLinkCount(info) > 1 {
		return errTargetOwnershipHardlink
	}
	if visit != nil {
		if err := visit(relPath, fullPath, info); err != nil {
			return err
		}
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		child := entry.Name()
		if isExcludedTargetMember(child) {
			return errTargetOwnershipRuntimeDenied
		}
		if err := walkTargetMember(root, filepath.Join(relPath, child), depth+1, visit); err != nil {
			return err
		}
	}
	return nil
}
