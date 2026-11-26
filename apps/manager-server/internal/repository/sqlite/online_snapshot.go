package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"
)

const snapshotManifestVersion = 1

var snapshotMemberNames = []string{
	"database",
	"database-wal",
	"database-shm",
	"database-journal",
	"data-key",
}

type snapshotManifest struct {
	Version           int                              `json:"version"`
	TargetDBPath      string                           `json:"target_db_path,omitempty"`
	TargetDataKeyPath string                           `json:"target_data_key_path,omitempty"`
	Files             map[string]snapshotManifestEntry `json:"files"`
}

type snapshotManifestEntry struct {
	Existed bool   `json:"existed"`
	Mode    uint32 `json:"mode,omitempty"`
	Size    int64  `json:"size,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

// CreateOnlineSnapshot creates an online, self-contained SQLite + data.key
// snapshot compatible with `cpa-manager-plus manager-data-snapshot restore`.
// It uses `VACUUM INTO` so the resulting database image is rollback-free and
// independent of any live WAL/SHM sidecars.
func CreateOnlineSnapshot(ctx context.Context, db *sql.DB, dbPath, dataKeyPath, snapshotDir string) (returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil {
		return errors.New("online snapshot requires a non-nil database handle")
	}
	trimmedDBPath := strings.TrimSpace(dbPath)
	if trimmedDBPath == "" {
		return errors.New("online snapshot requires a non-empty database path")
	}
	trimmedSnapshotDir := strings.TrimSpace(snapshotDir)
	if trimmedSnapshotDir == "" {
		return errors.New("online snapshot requires a non-empty snapshot directory")
	}
	if strings.ContainsRune(trimmedDBPath, '\x00') || strings.ContainsRune(trimmedSnapshotDir, '\x00') || strings.ContainsRune(dataKeyPath, '\x00') {
		return errors.New("online snapshot path contains invalid NUL byte")
	}

	absDBPath, err := filepath.Abs(trimmedDBPath)
	if err != nil {
		return fmt.Errorf("resolve database path: %w", err)
	}
	absDBPath = filepath.Clean(absDBPath)
	if err := ensureNoSymlinkAncestors(filepath.Dir(absDBPath)); err != nil {
		return fmt.Errorf("validate database parent directory: %w", err)
	}
	if dbInfo, err := os.Lstat(absDBPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect database path %s: %w", absDBPath, err)
		}
	} else if dbInfo.Mode()&os.ModeSymlink != 0 || !dbInfo.Mode().IsRegular() {
		return fmt.Errorf("database path %s is not a regular file", absDBPath)
	}

	absSnapshotDir, err := filepath.Abs(trimmedSnapshotDir)
	if err != nil {
		return fmt.Errorf("resolve snapshot directory: %w", err)
	}
	if _, err := os.Lstat(absSnapshotDir); err == nil {
		return fmt.Errorf("snapshot directory already exists: %s", absSnapshotDir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect snapshot directory %s: %w", absSnapshotDir, err)
	}

	parentDir := filepath.Dir(absSnapshotDir)
	if err := ensureSafeSnapshotParentDir(parentDir); err != nil {
		return err
	}

	tempDir, err := os.MkdirTemp(parentDir, ".cpamp-online-snapshot-tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary online snapshot directory: %w", err)
	}
	defer func() {
		if returnErr != nil {
			_ = os.RemoveAll(tempDir)
		}
	}()
	if err := os.Chmod(tempDir, 0o700); err != nil {
		return fmt.Errorf("protect temporary online snapshot directory: %w", err)
	}

	targetDBFile := filepath.Join(tempDir, "database")
	if err := vacuumDatabaseInto(ctx, db, targetDBFile); err != nil {
		return fmt.Errorf("vacuum database into online snapshot: %w", err)
	}
	if err := os.Chmod(targetDBFile, 0o600); err != nil {
		return fmt.Errorf("chmod snapshot database: %w", err)
	}
	dbDigest, dbSize, err := syncAndHashRegularFile(ctx, targetDBFile)
	if err != nil {
		return fmt.Errorf("finalize snapshot database: %w", err)
	}

	var dataKeyEntry snapshotManifestEntry
	effectiveDataKeyPath := strings.TrimSpace(dataKeyPath)
	if effectiveDataKeyPath == "" {
		effectiveDataKeyPath = filepath.Join(filepath.Dir(absDBPath), "data.key")
	}
	absKeyPath, err := filepath.Abs(effectiveDataKeyPath)
	if err != nil {
		return fmt.Errorf("resolve data key path: %w", err)
	}
	absKeyPath = filepath.Clean(absKeyPath)
	if err := ensureNoSymlinkAncestors(filepath.Dir(absKeyPath)); err != nil {
		return fmt.Errorf("validate data key parent directory: %w", err)
	}
	keyInfo, err := os.Lstat(absKeyPath)
	if err == nil {
		if keyInfo.Mode()&os.ModeSymlink != 0 || !keyInfo.Mode().IsRegular() {
			return fmt.Errorf("data key %s is not a regular file", absKeyPath)
		}
		targetKeyFile := filepath.Join(tempDir, "data-key")
		keyDigest, keySize, err := copySnapshotFile(ctx, absKeyPath, targetKeyFile, 0o600)
		if err != nil {
			return fmt.Errorf("snapshot data key %s: %w", absKeyPath, err)
		}
		dataKeyEntry = snapshotManifestEntry{
			Existed: true,
			Mode:    0o600,
			Size:    keySize,
			SHA256:  keyDigest,
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect data key %s: %w", absKeyPath, err)
	}

	manifest := snapshotManifest{
		Version:           snapshotManifestVersion,
		TargetDBPath:      absDBPath,
		TargetDataKeyPath: absKeyPath,
		Files: map[string]snapshotManifestEntry{
			"database": {
				Existed: true,
				Mode:    0o600,
				Size:    dbSize,
				SHA256:  dbDigest,
			},
			"database-wal":     {Existed: false},
			"database-shm":     {Existed: false},
			"database-journal": {Existed: false},
			"data-key":         dataKeyEntry,
		},
	}
	if err := writeSnapshotManifest(tempDir, manifest); err != nil {
		return err
	}
	if err := syncDirectory(tempDir); err != nil {
		return fmt.Errorf("sync temporary snapshot directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("online snapshot canceled before publish: %w", err)
	}
	if err := os.Rename(tempDir, absSnapshotDir); err != nil {
		return fmt.Errorf("publish online snapshot directory %s: %w", absSnapshotDir, err)
	}
	if err := syncDirectory(parentDir); err != nil {
		return fmt.Errorf("sync snapshot parent directory %s: %w", parentDir, err)
	}
	return nil
}

// PrunePreSlotSnapshots retains the newest `keep` `pre-slot-*` snapshot
// directories inside backupsDir while preserving any directory listed in
// protectedDirs and leaving `pre-migrate-*` or unrelated entries untouched.
func PrunePreSlotSnapshots(backupsDir string, keep int, protectedDirs ...string) error {
	trimmedBackups := strings.TrimSpace(backupsDir)
	if trimmedBackups == "" {
		return errors.New("backups directory is required")
	}
	if keep < 0 {
		return errors.New("keep count must be non-negative")
	}
	absBackupsDir, err := filepath.Abs(trimmedBackups)
	if err != nil {
		return fmt.Errorf("resolve backups directory: %w", err)
	}
	info, err := os.Lstat(absBackupsDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect backups directory %s: %w", absBackupsDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("backups path %s is not a regular directory", absBackupsDir)
	}
	if err := ensureNoSymlinkAncestors(absBackupsDir); err != nil {
		return err
	}

	protected := make(map[string]bool, len(protectedDirs)*2)
	for _, raw := range protectedDirs {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if abs, absErr := filepath.Abs(trimmed); absErr == nil {
			protected[filepath.Clean(abs)] = true
			if resolved, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
				protected[filepath.Clean(resolved)] = true
			}
		}
	}

	entries, err := os.ReadDir(absBackupsDir)
	if err != nil {
		return fmt.Errorf("read backups directory %s: %w", absBackupsDir, err)
	}

	type candidate struct {
		path    string
		name    string
		modTime time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "pre-slot-") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		fullPath := filepath.Join(absBackupsDir, name)
		manifestInfo, valid := inspectValidSnapshotManifest(fullPath)
		if !valid {
			continue
		}
		candidates = append(candidates, candidate{
			path:    fullPath,
			name:    name,
			modTime: manifestInfo.ModTime(),
		})
	}

	slices.SortFunc(candidates, func(a, b candidate) int {
		if !a.modTime.Equal(b.modTime) {
			if a.modTime.After(b.modTime) {
				return -1
			}
			return 1
		}
		return strings.Compare(b.name, a.name)
	})

	for idx, item := range candidates {
		if idx < keep {
			continue
		}
		if isProtectedSnapshotPath(item.path, protected) {
			continue
		}
		if err := removeValidatedSnapshotDir(item.path); err != nil {
			return err
		}
	}
	return nil
}

func isProtectedSnapshotPath(candidatePath string, protected map[string]bool) bool {
	if protected[filepath.Clean(candidatePath)] {
		return true
	}
	if resolved, err := filepath.EvalSymlinks(candidatePath); err == nil && protected[filepath.Clean(resolved)] {
		return true
	}
	return false
}

func removeValidatedSnapshotDir(snapshotDir string) error {
	info, err := os.Lstat(snapshotDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect snapshot directory %s: %w", snapshotDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("refusing to prune non-directory snapshot %s", snapshotDir)
	}
	allowed := make(map[string]bool, len(snapshotMemberNames)+1)
	allowed["manifest.json"] = true
	for _, name := range snapshotMemberNames {
		allowed[name] = true
	}
	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		return fmt.Errorf("read snapshot directory %s: %w", snapshotDir, err)
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fmt.Errorf("snapshot directory %s contains unexpected entry %s", snapshotDir, entry.Name())
		}
		memberPath := filepath.Join(snapshotDir, entry.Name())
		memberInfo, err := os.Lstat(memberPath)
		if err != nil {
			return fmt.Errorf("inspect snapshot member %s: %w", memberPath, err)
		}
		if memberInfo.Mode()&os.ModeSymlink != 0 || !memberInfo.Mode().IsRegular() {
			return fmt.Errorf("snapshot member %s is not a regular file", memberPath)
		}
	}
	for _, name := range snapshotMemberNames {
		if err := os.Remove(filepath.Join(snapshotDir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove snapshot file %s: %w", name, err)
		}
	}
	if err := os.Remove(filepath.Join(snapshotDir, "manifest.json")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove snapshot manifest: %w", err)
	}
	if err := os.Remove(snapshotDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove snapshot directory %s: %w", snapshotDir, err)
	}
	return nil
}

func vacuumDatabaseInto(ctx context.Context, db *sql.DB, targetPath string) error {
	quoted := "'" + strings.ReplaceAll(targetPath, "'", "''") + "'"
	_, err := db.ExecContext(ctx, "VACUUM INTO "+quoted)
	return err
}

func syncAndHashRegularFile(ctx context.Context, path string) (string, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("%s is not a regular file", path)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = file.Close() }()

	openedInfo, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !os.SameFile(info, openedInfo) {
		return "", 0, fmt.Errorf("file %s changed during snapshot", path)
	}
	if err := file.Sync(); err != nil {
		return "", 0, err
	}
	digest, size, err := hashOpenFileFromStart(ctx, file)
	if err != nil {
		return "", 0, err
	}
	if size != openedInfo.Size() {
		return "", 0, io.ErrUnexpectedEOF
	}
	return digest, size, nil
}

func writeSnapshotManifest(tempDir string, m snapshotManifest) error {
	manifestData, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode snapshot manifest: %w", err)
	}
	manifestData = append(manifestData, '\n')
	manifestPath := filepath.Join(tempDir, "manifest.json")
	file, err := os.OpenFile(manifestPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create snapshot manifest: %w", err)
	}
	if _, err := file.Write(manifestData); err != nil {
		_ = file.Close()
		return fmt.Errorf("write snapshot manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync snapshot manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close snapshot manifest: %w", err)
	}
	return nil
}

func ensureSafeSnapshotParentDir(parentDir string) error {
	cleanParent := filepath.Clean(parentDir)
	var missing []string
	cur := cleanParent
	for {
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			missing = append(missing, cur)
			next := filepath.Dir(cur)
			if next == cur {
				return fmt.Errorf("snapshot root %s does not exist", cur)
			}
			cur = next
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect snapshot parent %s: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if !isOSRootSystemSymlink(cur) {
				return fmt.Errorf("snapshot path component %s must not be a symbolic link", cur)
			}
		} else if !info.IsDir() {
			return fmt.Errorf("snapshot path component %s is not a directory", cur)
		}
		next := filepath.Dir(cur)
		if next == cur {
			break
		}
		cur = next
	}

	for i := len(missing) - 1; i >= 0; i-- {
		dir := missing[i]
		if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create snapshot parent %s: %w", dir, err)
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return fmt.Errorf("inspect created snapshot parent %s: %w", dir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("snapshot parent %s is not a regular directory", dir)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("chmod snapshot parent %s: %w", dir, err)
		}
	}

	finalInfo, err := os.Lstat(cleanParent)
	if err != nil {
		return fmt.Errorf("inspect snapshot parent %s: %w", cleanParent, err)
	}
	if finalInfo.Mode()&os.ModeSymlink != 0 || !finalInfo.IsDir() {
		return fmt.Errorf("snapshot parent %s is not a regular directory", cleanParent)
	}
	return nil
}

func ensureNoSymlinkAncestors(dir string) error {
	cur := filepath.Clean(dir)
	for {
		info, err := os.Lstat(cur)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				if !isOSRootSystemSymlink(cur) {
					return fmt.Errorf("path component %s must not be a symbolic link", cur)
				}
			} else if !info.IsDir() {
				return fmt.Errorf("path component %s is not a directory", cur)
			}
		}
		next := filepath.Dir(cur)
		if next == cur {
			break
		}
		cur = next
	}
	return nil
}

func isOSRootSystemSymlink(cleanPath string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	return cleanPath == "/var" || cleanPath == "/tmp" || cleanPath == "/etc"
}

func syncDirectory(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil && !errors.Is(syncErr, syscall.EINVAL) && !errors.Is(syncErr, syscall.ENOTSUP) {
		return syncErr
	}
	return closeErr
}

func inspectValidSnapshotManifest(snapshotDir string) (os.FileInfo, bool) {
	m, info, err := readValidatedSnapshotManifest(snapshotDir)
	if err != nil || m.Version != snapshotManifestVersion {
		return nil, false
	}
	return info, true
}

func isValidSnapshotDir(snapshotDir string) bool {
	_, ok := inspectValidSnapshotManifest(snapshotDir)
	return ok
}

func readValidatedSnapshotManifest(snapshotDir string) (snapshotManifest, os.FileInfo, error) {
	dirInfo, err := os.Lstat(snapshotDir)
	if err != nil {
		return snapshotManifest{}, nil, err
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return snapshotManifest{}, nil, fmt.Errorf("snapshot %s is not a regular directory", snapshotDir)
	}
	manifestPath := filepath.Join(snapshotDir, "manifest.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		return snapshotManifest{}, nil, err
	}
	if manifestInfo.Mode()&os.ModeSymlink != 0 || !manifestInfo.Mode().IsRegular() {
		return snapshotManifest{}, nil, fmt.Errorf("manifest %s is not a regular file", manifestPath)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return snapshotManifest{}, nil, err
	}
	var m snapshotManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return snapshotManifest{}, nil, err
	}
	if m.Version != snapshotManifestVersion || len(m.Files) != len(snapshotMemberNames) {
		return snapshotManifest{}, nil, errors.New("invalid snapshot manifest")
	}
	for _, targetPath := range []string{m.TargetDBPath, m.TargetDataKeyPath} {
		if targetPath != "" && (strings.ContainsRune(targetPath, '\x00') || !filepath.IsAbs(targetPath) || filepath.Clean(targetPath) != targetPath) {
			return snapshotManifest{}, nil, errors.New("invalid snapshot target path in manifest")
		}
	}
	for _, name := range snapshotMemberNames {
		entry, ok := m.Files[name]
		if !ok {
			return snapshotManifest{}, nil, fmt.Errorf("manifest missing %s", name)
		}
		if entry.Existed {
			if entry.SHA256 == "" || entry.Size < 0 || entry.Mode > 0o777 {
				return snapshotManifest{}, nil, fmt.Errorf("manifest invalid entry %s", name)
			}
			memberInfo, err := os.Lstat(filepath.Join(snapshotDir, name))
			if err != nil || memberInfo.Mode()&os.ModeSymlink != 0 || !memberInfo.Mode().IsRegular() || memberInfo.Size() != entry.Size {
				return snapshotManifest{}, nil, fmt.Errorf("snapshot member %s missing or mismatched", name)
			}
		}
	}
	return m, manifestInfo, nil
}

func copySnapshotFile(ctx context.Context, source, target string, mode os.FileMode) (string, int64, error) {
	inspected, err := os.Lstat(source)
	if err != nil {
		return "", 0, err
	}
	if inspected.Mode()&os.ModeSymlink != 0 || !inspected.Mode().IsRegular() {
		return "", 0, fmt.Errorf("source %s is not a regular file", source)
	}
	in, err := os.Open(source)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = in.Close() }()

	openedInfo, err := in.Stat()
	if err != nil {
		return "", 0, err
	}
	if !os.SameFile(inspected, openedInfo) {
		return "", 0, fmt.Errorf("source %s changed while opening", source)
	}

	out, err := os.OpenFile(target, os.O_RDWR|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return "", 0, err
	}
	removeOut := true
	defer func() {
		_ = out.Close()
		if removeOut {
			_ = os.Remove(target)
		}
	}()

	digest, written, err := copySnapshotFileData(ctx, out, in)
	if err != nil {
		return "", 0, err
	}
	if err := out.Chmod(mode.Perm()); err != nil {
		return "", 0, err
	}
	if err := out.Sync(); err != nil {
		return "", 0, err
	}
	if err := out.Close(); err != nil {
		return "", 0, err
	}
	removeOut = false
	return digest, written, nil
}

func copyAndHashUserspace(ctx context.Context, dst *os.File, src *os.File) (string, int64, error) {
	hash := sha256.New()
	written, err := copyReaderWithContext(ctx, io.MultiWriter(dst, hash), src)
	if err != nil {
		return "", written, err
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

func finishFallbackAndHash(ctx context.Context, dst *os.File, src *os.File, alreadyCopied int64) (string, int64, error) {
	rem, err := copyReaderWithContext(ctx, dst, src)
	total := alreadyCopied + rem
	if err != nil {
		return "", total, err
	}
	digest, hashedBytes, err := hashOpenFileFromStart(ctx, dst)
	if err != nil {
		return "", total, err
	}
	if hashedBytes != total {
		return "", total, io.ErrUnexpectedEOF
	}
	return digest, total, nil
}

func hashOpenFileFromStart(ctx context.Context, file *os.File) (string, int64, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	written, err := copyReaderWithContext(ctx, hash, file)
	if err != nil {
		return "", written, err
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

func copyReaderWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 1024*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			w, writeErr := dst.Write(buf[:n])
			written += int64(w)
			if writeErr != nil {
				return written, writeErr
			}
			if w != n {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}
