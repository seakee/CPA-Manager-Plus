package managerdatasnapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
)

const manifestVersion = 1

var snapshotFiles = []snapshotFile{
	{Name: "database", DatabaseSuffix: ""},
	{Name: "database-wal", DatabaseSuffix: "-wal"},
	{Name: "database-shm", DatabaseSuffix: "-shm"},
	{Name: "database-journal", DatabaseSuffix: "-journal"},
	{Name: "data-key", DataKey: true},
}

type snapshotFile struct {
	Name           string
	DatabaseSuffix string
	DataKey        bool
}

type manifest struct {
	Version int                      `json:"version"`
	Files   map[string]manifestEntry `json:"files"`
}

type manifestEntry struct {
	Existed bool   `json:"existed"`
	Mode    uint32 `json:"mode,omitempty"`
	Size    int64  `json:"size,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

type options struct {
	Action      string
	DBPath      string
	DataKeyPath string
	SnapshotDir string
}

func Run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	opts, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	switch opts.Action {
	case "create":
		if err := withDatabaseLock(opts.DBPath, func(dbPath string) error {
			return create(ctx, dbPath, opts.DataKeyPath, opts.SnapshotDir)
		}); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Manager data snapshot created at %s.\n", opts.SnapshotDir)
	case "restore":
		if err := withDatabaseLock(opts.DBPath, func(dbPath string) error {
			return restore(ctx, dbPath, opts.DataKeyPath, opts.SnapshotDir)
		}); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Manager data restored from %s.\n", opts.SnapshotDir)
	case "delete":
		if err := deleteSnapshot(opts.SnapshotDir); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Manager data snapshot deleted at %s.\n", opts.SnapshotDir)
	default:
		return fmt.Errorf("unsupported action %q", opts.Action)
	}
	return nil
}

func parseArgs(args []string, stderr io.Writer) (options, error) {
	if len(args) == 0 {
		return options{}, errors.New("snapshot action is required: create, restore, or delete")
	}
	opts := options{Action: args[0]}
	fs := flag.NewFlagSet("manager-data-snapshot "+opts.Action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.DBPath, "db-path", "", "SQLite database path")
	fs.StringVar(&opts.DataKeyPath, "data-key-path", "", "data.key path")
	fs.StringVar(&opts.SnapshotDir, "snapshot-dir", "", "private snapshot directory")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: cpa-manager-plus manager-data-snapshot <create|restore|delete> --snapshot-dir PATH [--db-path PATH --data-key-path PATH]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args[1:]); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	opts.DBPath = strings.TrimSpace(opts.DBPath)
	opts.DataKeyPath = strings.TrimSpace(opts.DataKeyPath)
	opts.SnapshotDir = strings.TrimSpace(opts.SnapshotDir)
	if opts.SnapshotDir == "" {
		return options{}, errors.New("--snapshot-dir is required")
	}
	if opts.Action == "create" || opts.Action == "restore" {
		if opts.DBPath == "" {
			return options{}, errors.New("--db-path is required")
		}
		if opts.DataKeyPath == "" {
			return options{}, errors.New("--data-key-path is required")
		}
	}
	return opts, nil
}

func withDatabaseLock(dbPath string, fn func(string) error) error {
	databaseLock, err := processlock.Acquire(dbPath)
	if err != nil {
		return fmt.Errorf("acquire Manager data snapshot lock; stop Manager Server and retry: %w", err)
	}
	defer func() { _ = databaseLock.Close() }()
	return fn(databaseLock.DatabasePath())
}

func create(ctx context.Context, dbPath string, dataKeyPath string, snapshotDir string) (returnErr error) {
	absSnapshotDir, err := filepath.Abs(snapshotDir)
	if err != nil {
		return fmt.Errorf("resolve snapshot directory: %w", err)
	}
	if _, err := os.Lstat(absSnapshotDir); err == nil {
		return fmt.Errorf("snapshot directory already exists: %s", absSnapshotDir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect snapshot directory %s: %w", absSnapshotDir, err)
	}
	parent := filepath.Dir(absSnapshotDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create snapshot parent %s: %w", parent, err)
	}
	tempDir, err := os.MkdirTemp(parent, ".cpamp-manager-snapshot-tmp-")
	if err != nil {
		return fmt.Errorf("create temporary snapshot directory: %w", err)
	}
	defer func() {
		if returnErr != nil {
			_ = os.RemoveAll(tempDir)
		}
	}()
	if err := os.Chmod(tempDir, 0o700); err != nil {
		return fmt.Errorf("protect temporary snapshot directory: %w", err)
	}

	m := manifest{Version: manifestVersion, Files: make(map[string]manifestEntry, len(snapshotFiles))}
	for _, item := range snapshotFiles {
		source := sourcePath(item, dbPath, dataKeyPath)
		entry, err := snapshotOne(ctx, source, filepath.Join(tempDir, item.Name))
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", source, err)
		}
		m.Files[item.Name] = entry
	}
	manifestData, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode snapshot manifest: %w", err)
	}
	manifestData = append(manifestData, '\n')
	if err := writeNewFile(filepath.Join(tempDir, "manifest.json"), manifestData, 0o600); err != nil {
		return fmt.Errorf("write snapshot manifest: %w", err)
	}
	if err := os.Rename(tempDir, absSnapshotDir); err != nil {
		return fmt.Errorf("publish snapshot directory %s: %w", absSnapshotDir, err)
	}
	return nil
}

func snapshotOne(ctx context.Context, source string, target string) (manifestEntry, error) {
	info, err := os.Lstat(source)
	if os.IsNotExist(err) {
		return manifestEntry{}, nil
	}
	if err != nil {
		return manifestEntry{}, err
	}
	if !info.Mode().IsRegular() {
		return manifestEntry{}, fmt.Errorf("source is not a regular file")
	}
	digest, size, err := copyFile(ctx, source, target, 0o600)
	if err != nil {
		return manifestEntry{}, err
	}
	return manifestEntry{
		Existed: true,
		Mode:    uint32(info.Mode().Perm()),
		Size:    size,
		SHA256:  digest,
	}, nil
}

// renameFn is a fault-injection seam for tests. Production restore commits
// use os.Rename; the rollback path always calls os.Rename directly so an
// injected forward failure still rolls back with real filesystem calls.
var renameFn = os.Rename

// restore swaps the whole Manager file-set (database, sidecars, data.key) as
// one logical transaction. A usage.sqlite restored without its matching
// data.key is unrecoverable, so the commit phase first moves every live file
// into a rollback slot next to it; any later failure reverses the whole set
// instead of leaving a half-restored state behind.
func restore(ctx context.Context, dbPath string, dataKeyPath string, snapshotDir string) error {
	absSnapshotDir, m, err := loadManifest(snapshotDir)
	if err != nil {
		return err
	}

	type restoreItem struct {
		name    string
		existed bool
		target  string
		staged  string
	}
	items := make([]restoreItem, 0, len(snapshotFiles))
	staged := make(map[string]string)
	defer func() {
		for _, path := range staged {
			_ = os.Remove(path)
		}
	}()
	for _, item := range snapshotFiles {
		entry := m.Files[item.Name]
		target := sourcePath(item, dbPath, dataKeyPath)
		if err := ensureRestorableTarget(target); err != nil {
			return err
		}
		if !entry.Existed {
			items = append(items, restoreItem{name: item.Name, target: target})
			continue
		}
		source := filepath.Join(absSnapshotDir, item.Name)
		stagedPath, err := stageRestoreFile(ctx, source, target, entry)
		if err != nil {
			return err
		}
		staged[target] = stagedPath
		items = append(items, restoreItem{name: item.Name, existed: true, target: target, staged: stagedPath})
	}

	// Commit boundary. Step 1 moves the current live set aside; step 2 switches
	// the staged snapshot files in. Both use renameFn so tests can fail either
	// step, while rollbackRestore below only uses os.Rename.
	type rollbackSlot struct {
		target string
		slot   string
	}
	slots := make([]rollbackSlot, 0, len(items))
	rollbackRestore := func(restored []string) error {
		var problems []string
		for index := len(restored) - 1; index >= 0; index-- {
			if err := os.Remove(restored[index]); err != nil && !os.IsNotExist(err) {
				problems = append(problems, fmt.Sprintf("remove restored %s: %v", restored[index], err))
			}
		}
		for index := len(slots) - 1; index >= 0; index-- {
			if err := os.Rename(slots[index].slot, slots[index].target); err != nil {
				problems = append(problems, fmt.Sprintf("recover live file from %s: %v", slots[index].slot, err))
			}
		}
		if len(problems) > 0 {
			return errors.New(strings.Join(problems, "; "))
		}
		return nil
	}
	for _, item := range items {
		info, err := os.Lstat(item.target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			_ = rollbackRestore(nil)
			return fmt.Errorf("inspect live file %s: %w", item.target, err)
		}
		if !info.Mode().IsRegular() {
			_ = rollbackRestore(nil)
			return fmt.Errorf("live file %s is not a regular file", item.target)
		}
		slot, err := reserveRollbackSlot(item.target)
		if err != nil {
			_ = rollbackRestore(nil)
			return err
		}
		if err := renameFn(item.target, slot); err != nil {
			_ = os.Remove(slot)
			_ = rollbackRestore(nil)
			return fmt.Errorf("move live file %s aside: %w", item.target, err)
		}
		slots = append(slots, rollbackSlot{target: item.target, slot: slot})
	}

	restored := make([]string, 0, len(items))
	for _, item := range items {
		if !item.existed {
			// Files created after the snapshot: phase 1 already moved them into
			// a rollback slot, so the target is absent exactly as the manifest
			// requires.
			continue
		}
		if err := renameFn(item.staged, item.target); err != nil {
			if rollbackErr := rollbackRestore(restored); rollbackErr != nil {
				return fmt.Errorf("restore %s: %v; rollback incomplete, original live files may remain in rollback slots: %v", item.target, err, rollbackErr)
			}
			return fmt.Errorf("restore %s: %w (live files were restored to their pre-restore state)", item.target, err)
		}
		restored = append(restored, item.target)
		delete(staged, item.target)
	}

	for _, slot := range slots {
		if err := os.Remove(slot.slot); err != nil {
			return fmt.Errorf("cleanup rollback slot %s: %w", slot.slot, err)
		}
	}
	targets := make([]string, 0, len(items))
	for _, item := range items {
		targets = append(targets, item.target)
	}
	syncTargetDirs(targets)
	return nil
}

// ensureRestorableTarget rejects symlinked or special restore targets before
// anything is staged, so a restore never renames a link away and replaces it
// with attacker-controlled content.
func ensureRestorableTarget(target string) error {
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect restore target %s: %w", target, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("restore target %s is not a regular file", target)
	}
	return nil
}

func stageRestoreFile(ctx context.Context, source string, target string, entry manifestEntry) (string, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("create restore directory for %s: %w", target, err)
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".cpamp-restore-*")
	if err != nil {
		return "", fmt.Errorf("create restore file for %s: %w", target, err)
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("close restore file for %s: %w", target, err)
	}
	if err := os.Remove(tempPath); err != nil {
		return "", fmt.Errorf("prepare restore file for %s: %w", target, err)
	}
	digest, size, err := copyFile(ctx, source, tempPath, os.FileMode(entry.Mode))
	if err != nil {
		return "", fmt.Errorf("stage restore for %s: %w", target, err)
	}
	if size != entry.Size || digest != entry.SHA256 {
		return "", fmt.Errorf("snapshot file %s failed integrity validation", filepath.Base(source))
	}
	return tempPath, nil
}

func reserveRollbackSlot(target string) (string, error) {
	temp, err := os.CreateTemp(filepath.Dir(target), ".cpamp-restore-rollback-*")
	if err != nil {
		return "", fmt.Errorf("create rollback slot for %s: %w", target, err)
	}
	slot := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(slot)
		return "", fmt.Errorf("close rollback slot for %s: %w", target, err)
	}
	if err := os.Remove(slot); err != nil {
		return "", fmt.Errorf("prepare rollback slot for %s: %w", target, err)
	}
	return slot, nil
}

// syncTargetDirs best-effort fsyncs the parent directories of the restored
// set so the commit survives a crash shortly after restore returns.
func syncTargetDirs(targets []string) {
	seen := make(map[string]bool)
	for _, target := range targets {
		dir := filepath.Dir(target)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		file, err := os.Open(dir)
		if err != nil {
			continue
		}
		_ = file.Sync()
		_ = file.Close()
	}
}

func loadManifest(snapshotDir string) (string, manifest, error) {
	absSnapshotDir, err := filepath.Abs(snapshotDir)
	if err != nil {
		return "", manifest{}, fmt.Errorf("resolve snapshot directory: %w", err)
	}
	info, err := os.Lstat(absSnapshotDir)
	if err != nil {
		return "", manifest{}, fmt.Errorf("inspect snapshot directory %s: %w", absSnapshotDir, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", manifest{}, fmt.Errorf("snapshot path is not a directory: %s", absSnapshotDir)
	}
	data, err := os.ReadFile(filepath.Join(absSnapshotDir, "manifest.json"))
	if err != nil {
		return "", manifest{}, fmt.Errorf("read snapshot manifest: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return "", manifest{}, fmt.Errorf("decode snapshot manifest: %w", err)
	}
	if m.Version != manifestVersion || len(m.Files) != len(snapshotFiles) {
		return "", manifest{}, errors.New("unsupported or incomplete snapshot manifest")
	}
	for _, item := range snapshotFiles {
		entry, ok := m.Files[item.Name]
		if !ok {
			return "", manifest{}, fmt.Errorf("snapshot manifest is missing %s", item.Name)
		}
		if entry.Existed && (entry.SHA256 == "" || entry.Size < 0 || entry.Mode > 0o777) {
			return "", manifest{}, fmt.Errorf("snapshot manifest has invalid metadata for %s", item.Name)
		}
	}
	return absSnapshotDir, m, nil
}

func deleteSnapshot(snapshotDir string) error {
	absSnapshotDir, _, err := loadManifest(snapshotDir)
	if err != nil {
		// A missing manifest with the directory still present usually means an
		// earlier delete removed files but failed before the final rmdir.
		if info, statErr := os.Lstat(absSnapshotDir); statErr == nil && info.IsDir() {
			return fmt.Errorf("%w; snapshot directory %s is incomplete (possibly a partially failed earlier delete); verify it is no longer needed and remove the directory manually", err, absSnapshotDir)
		}
		return err
	}
	allowed := map[string]bool{"manifest.json": true}
	for _, item := range snapshotFiles {
		allowed[item.Name] = true
	}
	entries, err := os.ReadDir(absSnapshotDir)
	if err != nil {
		return fmt.Errorf("inspect snapshot directory %s: %w", absSnapshotDir, err)
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fmt.Errorf("snapshot directory contains unexpected entry %s", entry.Name())
		}
	}
	for _, item := range snapshotFiles {
		if err := os.Remove(filepath.Join(absSnapshotDir, item.Name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete snapshot file %s: %w", item.Name, err)
		}
	}
	if err := os.Remove(filepath.Join(absSnapshotDir, "manifest.json")); err != nil {
		return fmt.Errorf("delete snapshot manifest: %w", err)
	}
	if err := os.Remove(absSnapshotDir); err != nil {
		return fmt.Errorf("delete snapshot directory %s: %w", absSnapshotDir, err)
	}
	return nil
}

func sourcePath(item snapshotFile, dbPath string, dataKeyPath string) string {
	if item.DataKey {
		return dataKeyPath
	}
	return dbPath + item.DatabaseSuffix
}

func copyFile(ctx context.Context, source string, target string, mode os.FileMode) (string, int64, error) {
	input, err := os.Open(source)
	if err != nil {
		return "", 0, err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return "", 0, err
	}
	removeTarget := true
	defer func() {
		_ = output.Close()
		if removeTarget {
			_ = os.Remove(target)
		}
	}()
	hash := sha256.New()
	written, err := copyWithContext(ctx, io.MultiWriter(output, hash), input)
	if err != nil {
		return "", 0, err
	}
	if err := output.Sync(); err != nil {
		return "", 0, err
	}
	if err := output.Close(); err != nil {
		return "", 0, err
	}
	removeTarget = false
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 1024*1024)
	var written int64
	for {
		select {
		case <-ctx.Done():
			return written, ctx.Err()
		default:
		}
		read, readErr := src.Read(buffer)
		if read > 0 {
			count, writeErr := dst.Write(buffer[:read])
			written += int64(count)
			if writeErr != nil {
				return written, writeErr
			}
			if count != read {
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

func writeNewFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
