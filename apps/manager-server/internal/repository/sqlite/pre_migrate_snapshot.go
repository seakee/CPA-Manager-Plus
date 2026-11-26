package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
)

const skipAutoPreMigrateSnapshotEnv = "CPAMP_SKIP_AUTO_PRE_MIGRATE_SNAPSHOT"

// ensurePreMigrateFallbackSnapshot creates a consistent pre-migration fallback
// snapshot under `<filepath.Dir(dbPath)>/backups/pre-migrate-gen<old>-to-gen<new>`
// before Migrate mutates tables or advances `schema_generation`.
//
// It skips in-memory databases, empty/missing database files, explicit
// `CPAMP_SKIP_AUTO_PRE_MIGRATE_SNAPSHOT=1`, and already-created valid target
// snapshots. When a verified `pre-slot-*` snapshot already matches the
// checkpointed live database and `data.key` digests, it materializes the durable
// `pre-migrate-gen<old>-to-gen<new>` directory by hardlinking (with copy
// fallback) so subsequent `PrunePreSlotSnapshots` runs never delete it.
func ensurePreMigrateFallbackSnapshot(ctx context.Context, db *sql.DB, oldGen, newGen int64) (returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil || oldGen < 0 || newGen <= oldGen {
		return nil
	}
	if strings.TrimSpace(os.Getenv(skipAutoPreMigrateSnapshotEnv)) == "1" {
		return nil
	}

	mainDBPath, err := lookupMainDatabasePath(ctx, db)
	if err != nil {
		return err
	}
	if isEphemeralSQLitePath(mainDBPath) {
		return nil
	}

	absDBPath, err := filepath.Abs(mainDBPath)
	if err != nil {
		return fmt.Errorf("resolve pre-migrate database path: %w", err)
	}
	absDBPath = filepath.Clean(absDBPath)
	if err := ensureNoSymlinkAncestors(filepath.Dir(absDBPath)); err != nil {
		return fmt.Errorf("validate pre-migrate database parent directory: %w", err)
	}
	dbInfo, err := os.Lstat(absDBPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect pre-migrate database %s: %w", absDBPath, err)
	}
	if dbInfo.Mode()&os.ModeSymlink != 0 || !dbInfo.Mode().IsRegular() {
		return fmt.Errorf("pre-migrate database %s is not a regular file", absDBPath)
	}
	if dbInfo.Size() == 0 && !nonEmptySidecarExists(absDBPath+"-wal") && !nonEmptySidecarExists(absDBPath+"-journal") {
		return nil
	}

	backupsDir := filepath.Join(filepath.Dir(absDBPath), "backups")
	snapshotDir := filepath.Join(backupsDir, fmt.Sprintf("pre-migrate-gen%d-to-gen%d", oldGen, newGen))
	if existingInfo, err := os.Lstat(snapshotDir); err == nil {
		if existingInfo.Mode()&os.ModeSymlink != 0 || !existingInfo.IsDir() {
			return fmt.Errorf("existing pre-migrate snapshot path %s is not a regular directory", snapshotDir)
		}
		if isValidSnapshotDir(snapshotDir) {
			return nil
		}
		return fmt.Errorf("existing pre-migrate snapshot directory %s is incomplete", snapshotDir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect pre-migrate snapshot directory %s: %w", snapshotDir, err)
	}

	rawDataKeyPath := resolveSnapshotDataKeyPath(absDBPath)
	absDataKeyPath, err := filepath.Abs(strings.TrimSpace(rawDataKeyPath))
	if err != nil {
		return fmt.Errorf("resolve pre-migrate data key path: %w", err)
	}
	absDataKeyPath = filepath.Clean(absDataKeyPath)
	if err := ensureNoSymlinkAncestors(filepath.Dir(absDataKeyPath)); err != nil {
		return fmt.Errorf("validate pre-migrate data key parent directory: %w", err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for pre-migrate snapshot: %w", err)
	}
	defer func() { _ = conn.Close() }()

	var busy, logFrames, checkpointedFrames int
	if err := conn.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return fmt.Errorf("checkpoint WAL before pre-migrate snapshot: %w", err)
	}

	// Verify `-shm` is not a symlink/special file if present, while always
	// recording Existed: false so restore cleans up any live shared-memory index.
	if shmInfo, err := os.Lstat(absDBPath + "-shm"); err == nil {
		if shmInfo.Mode()&os.ModeSymlink != 0 || !shmInfo.Mode().IsRegular() {
			return fmt.Errorf("database sidecar %s is not a regular file", absDBPath+"-shm")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect database sidecar %s: %w", absDBPath+"-shm", err)
	}

	matchedPreSlotDir, matchedPreSlotManifest, err := findVerifiedPreSlotSnapshot(ctx, absDBPath, absDataKeyPath, backupsDir)
	if err != nil {
		return err
	}

	if err := ensureSafeSnapshotParentDir(backupsDir); err != nil {
		return err
	}

	tempDir, err := os.MkdirTemp(backupsDir, ".cpamp-pre-migrate-tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary pre-migrate snapshot directory: %w", err)
	}
	defer func() {
		if returnErr != nil {
			_ = os.RemoveAll(tempDir)
		}
	}()
	if err := os.Chmod(tempDir, 0o700); err != nil {
		return fmt.Errorf("protect temporary pre-migrate snapshot directory: %w", err)
	}

	var manifest snapshotManifest
	if matchedPreSlotDir != "" {
		dbEntry := matchedPreSlotManifest.Files["database"]
		if err := linkOrCopyVerifiedSnapshotFile(
			ctx,
			filepath.Join(matchedPreSlotDir, "database"),
			filepath.Join(tempDir, "database"),
			dbEntry.SHA256,
			dbEntry.Size,
			0o600,
		); err != nil {
			return fmt.Errorf("materialize pre-migrate database from verified pre-slot snapshot: %w", err)
		}
		keyEntry := matchedPreSlotManifest.Files["data-key"]
		var materializedKeyEntry snapshotManifestEntry
		if keyEntry.Existed {
			if err := linkOrCopyVerifiedSnapshotFile(
				ctx,
				filepath.Join(matchedPreSlotDir, "data-key"),
				filepath.Join(tempDir, "data-key"),
				keyEntry.SHA256,
				keyEntry.Size,
				0o600,
			); err != nil {
				return fmt.Errorf("materialize pre-migrate data-key from verified pre-slot snapshot: %w", err)
			}
			materializedKeyEntry = snapshotManifestEntry{
				Existed: true,
				Mode:    0o600,
				Size:    keyEntry.Size,
				SHA256:  keyEntry.SHA256,
			}
		}
		manifest = snapshotManifest{
			Version:           snapshotManifestVersion,
			TargetDBPath:      absDBPath,
			TargetDataKeyPath: absDataKeyPath,
			Files: map[string]snapshotManifestEntry{
				"database": {
					Existed: true,
					Mode:    0o600,
					Size:    dbEntry.Size,
					SHA256:  dbEntry.SHA256,
				},
				"database-wal":     {Existed: false},
				"database-shm":     {Existed: false},
				"database-journal": {Existed: false},
				"data-key":         materializedKeyEntry,
			},
		}
	} else {
		dbDigest, dbSize, err := copySnapshotFile(ctx, absDBPath, filepath.Join(tempDir, "database"), 0o600)
		if err != nil {
			return fmt.Errorf("copy database for pre-migrate snapshot: %w", err)
		}
		walEntry, err := snapshotOptionalNonEmptySidecar(ctx, absDBPath+"-wal", filepath.Join(tempDir, "database-wal"))
		if err != nil {
			return err
		}
		journalEntry, err := snapshotOptionalNonEmptySidecar(ctx, absDBPath+"-journal", filepath.Join(tempDir, "database-journal"))
		if err != nil {
			return err
		}
		dataKeyEntry, err := snapshotOptionalDataKey(ctx, absDataKeyPath, filepath.Join(tempDir, "data-key"))
		if err != nil {
			return err
		}
		manifest = snapshotManifest{
			Version:           snapshotManifestVersion,
			TargetDBPath:      absDBPath,
			TargetDataKeyPath: absDataKeyPath,
			Files: map[string]snapshotManifestEntry{
				"database": {
					Existed: true,
					Mode:    0o600,
					Size:    dbSize,
					SHA256:  dbDigest,
				},
				"database-wal":     walEntry,
				"database-shm":     {Existed: false},
				"database-journal": journalEntry,
				"data-key":         dataKeyEntry,
			},
		}
	}
	if err := writeSnapshotManifest(tempDir, manifest); err != nil {
		return err
	}
	if err := syncDirectory(tempDir); err != nil {
		return fmt.Errorf("sync temporary pre-migrate snapshot directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("pre-migrate snapshot canceled before publish: %w", err)
	}
	if err := os.Rename(tempDir, snapshotDir); err != nil {
		return fmt.Errorf("publish pre-migrate snapshot directory %s: %w", snapshotDir, err)
	}
	if err := syncDirectory(backupsDir); err != nil {
		return fmt.Errorf("sync backups directory %s: %w", backupsDir, err)
	}
	return nil
}

func lookupMainDatabasePath(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA database_list`)
	if err != nil {
		return "", fmt.Errorf("query sqlite database_list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return "", fmt.Errorf("scan sqlite database_list: %w", err)
		}
		if name == "main" {
			return strings.TrimSpace(file), nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate sqlite database_list: %w", err)
	}
	return "", nil
}

func isEphemeralSQLitePath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || trimmed == ":memory:" {
		return true
	}
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "file::memory:") || strings.Contains(lower, "mode=memory")
}

func nonEmptySidecarExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func resolveSnapshotDataKeyPath(absDBPath string) string {
	if explicit := strings.TrimSpace(os.Getenv("CPA_MANAGER_DATA_KEY_PATH")); explicit != "" {
		return explicit
	}
	if cfg, err := config.LoadWithoutCreatingDefault(); err == nil {
		if cfgDB, absErr := filepath.Abs(strings.TrimSpace(cfg.DBPath)); absErr == nil && pathsReferToSameDB(cfgDB, absDBPath) {
			if cfgKey := strings.TrimSpace(cfg.DataKeyPath); cfgKey != "" {
				return cfgKey
			}
		}
	}
	return filepath.Join(filepath.Dir(absDBPath), "data.key")
}

func pathsReferToSameDB(pathA, pathB string) bool {
	if filepath.Clean(pathA) == filepath.Clean(pathB) {
		return true
	}
	infoA, errA := os.Stat(pathA)
	infoB, errB := os.Stat(pathB)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

func snapshotOptionalNonEmptySidecar(ctx context.Context, sourcePath, targetPath string) (snapshotManifestEntry, error) {
	info, err := os.Lstat(sourcePath)
	if os.IsNotExist(err) {
		return snapshotManifestEntry{Existed: false}, nil
	}
	if err != nil {
		return snapshotManifestEntry{}, fmt.Errorf("inspect database sidecar %s: %w", sourcePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return snapshotManifestEntry{}, fmt.Errorf("database sidecar %s is not a regular file", sourcePath)
	}
	if info.Size() == 0 {
		return snapshotManifestEntry{Existed: false}, nil
	}
	digest, size, err := copySnapshotFile(ctx, sourcePath, targetPath, 0o600)
	if err != nil {
		return snapshotManifestEntry{}, fmt.Errorf("copy database sidecar %s: %w", sourcePath, err)
	}
	return snapshotManifestEntry{
		Existed: true,
		Mode:    0o600,
		Size:    size,
		SHA256:  digest,
	}, nil
}

func snapshotOptionalDataKey(ctx context.Context, dataKeyPath, targetPath string) (snapshotManifestEntry, error) {
	trimmed := strings.TrimSpace(dataKeyPath)
	if trimmed == "" {
		return snapshotManifestEntry{Existed: false}, nil
	}
	absKeyPath, err := filepath.Abs(trimmed)
	if err != nil {
		return snapshotManifestEntry{}, fmt.Errorf("resolve data key path: %w", err)
	}
	if err := ensureNoSymlinkAncestors(filepath.Dir(absKeyPath)); err != nil {
		return snapshotManifestEntry{}, fmt.Errorf("validate data key parent directory: %w", err)
	}
	info, err := os.Lstat(absKeyPath)
	if os.IsNotExist(err) {
		return snapshotManifestEntry{Existed: false}, nil
	}
	if err != nil {
		return snapshotManifestEntry{}, fmt.Errorf("inspect data key %s: %w", absKeyPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return snapshotManifestEntry{}, fmt.Errorf("data key %s is not a regular file", absKeyPath)
	}
	digest, size, err := copySnapshotFile(ctx, absKeyPath, targetPath, 0o600)
	if err != nil {
		return snapshotManifestEntry{}, fmt.Errorf("copy data key %s: %w", absKeyPath, err)
	}
	return snapshotManifestEntry{
		Existed: true,
		Mode:    0o600,
		Size:    size,
		SHA256:  digest,
	}, nil
}

func findVerifiedPreSlotSnapshot(ctx context.Context, absDBPath, absDataKeyPath, backupsDir string) (string, snapshotManifest, error) {
	info, err := os.Lstat(backupsDir)
	if os.IsNotExist(err) {
		return "", snapshotManifest{}, nil
	}
	if err != nil {
		return "", snapshotManifest{}, fmt.Errorf("inspect backups directory %s: %w", backupsDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", snapshotManifest{}, fmt.Errorf("backups path %s is not a regular directory", backupsDir)
	}
	if nonEmptySidecarExists(absDBPath+"-wal") || nonEmptySidecarExists(absDBPath+"-journal") {
		return "", snapshotManifest{}, nil
	}

	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		return "", snapshotManifest{}, fmt.Errorf("read backups directory %s: %w", backupsDir, err)
	}

	type preSlotCandidate struct {
		dir      string
		manifest snapshotManifest
	}
	var candidates []preSlotCandidate
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "pre-slot-") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		candidateDir := filepath.Join(backupsDir, name)
		m, _, err := readValidatedSnapshotManifest(candidateDir)
		if err != nil {
			continue
		}
		candidates = append(candidates, preSlotCandidate{dir: candidateDir, manifest: m})
	}
	if len(candidates) == 0 {
		return "", snapshotManifest{}, nil
	}

	dbDigest, dbSize, err := hashRegularFilePath(ctx, absDBPath)
	if err != nil {
		return "", snapshotManifest{}, nil
	}

	var keyExisted bool
	var keyDigest string
	var keySize int64
	if keyInfo, statErr := os.Lstat(absDataKeyPath); statErr == nil {
		if keyInfo.Mode()&os.ModeSymlink != 0 || !keyInfo.Mode().IsRegular() {
			return "", snapshotManifest{}, fmt.Errorf("data key %s is not a regular file", absDataKeyPath)
		}
		d, s, keyErr := hashRegularFilePath(ctx, absDataKeyPath)
		if keyErr != nil {
			return "", snapshotManifest{}, nil
		}
		keyExisted = true
		keyDigest = d
		keySize = s
	} else if !os.IsNotExist(statErr) {
		return "", snapshotManifest{}, fmt.Errorf("inspect data key %s: %w", absDataKeyPath, statErr)
	}

	for _, cand := range candidates {
		m := cand.manifest
		dbEntry := m.Files["database"]
		walEntry := m.Files["database-wal"]
		journalEntry := m.Files["database-journal"]
		keyEntry := m.Files["data-key"]
		if !dbEntry.Existed || walEntry.Existed || journalEntry.Existed {
			continue
		}
		if dbEntry.Size != dbSize || dbEntry.SHA256 != dbDigest {
			continue
		}
		if keyEntry.Existed != keyExisted {
			continue
		}
		if keyExisted && (keyEntry.Size != keySize || keyEntry.SHA256 != keyDigest) {
			continue
		}
		candDBDigest, candDBSize, err := hashRegularFilePath(ctx, filepath.Join(cand.dir, "database"))
		if err != nil || candDBDigest != dbDigest || candDBSize != dbSize {
			continue
		}
		if keyExisted {
			candKeyDigest, candKeySize, err := hashRegularFilePath(ctx, filepath.Join(cand.dir, "data-key"))
			if err != nil || candKeyDigest != keyDigest || candKeySize != keySize {
				continue
			}
		}
		return cand.dir, m, nil
	}
	return "", snapshotManifest{}, nil
}

func linkOrCopyVerifiedSnapshotFile(ctx context.Context, sourcePath, targetPath, expectedSHA256 string, expectedSize int64, mode os.FileMode) error {
	srcInfo, err := os.Lstat(sourcePath)
	if err == nil && srcInfo.Mode()&os.ModeSymlink == 0 && srcInfo.Mode().IsRegular() && (runtime.GOOS == "windows" || srcInfo.Mode().Perm() == mode.Perm()) && srcInfo.Size() == expectedSize {
		if linkErr := os.Link(sourcePath, targetPath); linkErr == nil {
			dstInfo, statErr := os.Lstat(targetPath)
			if statErr == nil && dstInfo.Mode()&os.ModeSymlink == 0 && dstInfo.Mode().IsRegular() && os.SameFile(srcInfo, dstInfo) && dstInfo.Size() == expectedSize {
				if f, openErr := os.Open(targetPath); openErr == nil {
					_ = f.Sync()
					_ = f.Close()
					return nil
				}
			}
			_ = os.Remove(targetPath)
		}
	}
	digest, size, err := copySnapshotFile(ctx, sourcePath, targetPath, mode)
	if err != nil {
		return err
	}
	if digest != expectedSHA256 || size != expectedSize {
		_ = os.Remove(targetPath)
		return fmt.Errorf("verified snapshot copy %s mismatched expected digest or size", sourcePath)
	}
	return nil
}

func hashRegularFilePath(ctx context.Context, path string) (string, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	written, err := copyReaderWithContext(ctx, hash, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}
