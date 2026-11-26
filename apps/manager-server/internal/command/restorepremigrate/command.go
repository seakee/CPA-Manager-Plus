package restorepremigrate

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/managerdatasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
)

const defaultRestoreTimeout = 2 * time.Minute

var requiredManifestMembers = []string{
	"database",
	"database-wal",
	"database-shm",
	"database-journal",
	"data-key",
}

// IsHelp reports whether err resulted from `-h` / `--help`.
func IsHelp(err error) bool {
	return errors.Is(err, flag.ErrHelp)
}

type options struct {
	SnapshotDir string
	Latest      bool
	DataDir     string
	DBPath      string
	DataKeyPath string
	Confirm     bool
	Timeout     time.Duration
}

// Run executes `cpa-manager-plus restore-pre-migrate`.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	opts, err := parseArgs(args, stderr)
	if err != nil {
		return err
	}
	if !opts.Confirm {
		return errors.New("refusing to restore without --confirm: post-commit restore discards business writes recorded after the upgrade; stop Manager Server and re-run with --confirm")
	}

	runCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	if err := runCtx.Err(); err != nil {
		return err
	}

	var (
		canonicalDBPath string
		dataKeyPath     string
		snapshotDir     string
	)

	if !opts.Latest {
		snapshotDir = opts.SnapshotDir
		var manifestDBPath, manifestKeyPath string
		if opts.DBPath == "" || opts.DataKeyPath == "" {
			mDB, mKey, mErr := readSnapshotManifestTargetPaths(snapshotDir, opts.DataDir)
			if mErr != nil {
				return mErr
			}
			manifestDBPath = mDB
			manifestKeyPath = mKey
		}
		dbPath := opts.DBPath
		if dbPath == "" && manifestDBPath != "" {
			dbPath = manifestDBPath
		}
		dataKeyPath = opts.DataKeyPath
		if dataKeyPath == "" && manifestKeyPath != "" {
			dataKeyPath = manifestKeyPath
		}
		if dbPath == "" || dataKeyPath == "" {
			fallbackDB, fallbackKey, _, resolveErr := resolveTargetPaths(dbPath, dataKeyPath, opts.DataDir)
			if resolveErr != nil {
				return resolveErr
			}
			if dbPath == "" {
				dbPath = fallbackDB
			}
			if dataKeyPath == "" {
				dataKeyPath = fallbackKey
			}
		}
		precheckLock, lockErr := processlock.Acquire(dbPath)
		if lockErr != nil {
			return fmt.Errorf("acquire restore-pre-migrate lock; stop Manager Server and retry: %w", lockErr)
		}
		canonicalDBPath = precheckLock.DatabasePath()
		if opts.DBPath != "" && opts.DataKeyPath == "" && manifestKeyPath == "" &&
			strings.TrimSpace(os.Getenv("CPA_MANAGER_DATA_KEY_PATH")) == "" &&
			strings.TrimSpace(os.Getenv("USAGE_DATA_DIR")) == "" {
			dataKeyPath = filepath.Join(filepath.Dir(canonicalDBPath), "data.key")
		}
		if closeErr := precheckLock.Close(); closeErr != nil {
			return fmt.Errorf("release precheck database lock: %w", closeErr)
		}
	} else {
		fallbackDBPath, fallbackKeyPath, backupsDir, resolveErr := resolveTargetPaths(opts.DBPath, opts.DataKeyPath, opts.DataDir)
		if resolveErr != nil {
			return resolveErr
		}
		precheckLock, lockErr := processlock.Acquire(fallbackDBPath)
		if lockErr != nil {
			return fmt.Errorf("acquire restore-pre-migrate lock; stop Manager Server and retry: %w", lockErr)
		}
		canonicalFallbackDB := precheckLock.DatabasePath()
		if opts.DBPath != "" && opts.DataKeyPath == "" &&
			strings.TrimSpace(os.Getenv("CPA_MANAGER_DATA_KEY_PATH")) == "" &&
			strings.TrimSpace(os.Getenv("USAGE_DATA_DIR")) == "" {
			fallbackKeyPath = filepath.Join(filepath.Dir(canonicalFallbackDB), "data.key")
		}
		if opts.DBPath != "" && strings.TrimSpace(os.Getenv("USAGE_DATA_DIR")) == "" {
			backupsDir = filepath.Join(filepath.Dir(canonicalFallbackDB), "backups")
		}

		selected, selectErr := selectLatestBackupSnapshotWithFallback(backupsDir, opts.DataDir)
		if selectErr != nil {
			_ = precheckLock.Close()
			return selectErr
		}
		snapshotDir = selected

		var manifestDBPath, manifestKeyPath string
		if opts.DBPath == "" || opts.DataKeyPath == "" {
			mDB, mKey, mErr := readSnapshotManifestTargetPaths(snapshotDir, opts.DataDir)
			if mErr != nil {
				_ = precheckLock.Close()
				return mErr
			}
			manifestDBPath = mDB
			manifestKeyPath = mKey
		}
		if closeErr := precheckLock.Close(); closeErr != nil {
			return fmt.Errorf("release precheck database lock: %w", closeErr)
		}

		targetDB := canonicalFallbackDB
		if opts.DBPath == "" && manifestDBPath != "" {
			targetDB = manifestDBPath
		}
		dataKeyPath = fallbackKeyPath
		if opts.DataKeyPath == "" && manifestKeyPath != "" {
			dataKeyPath = manifestKeyPath
		}
		if filepath.Clean(targetDB) != filepath.Clean(canonicalFallbackDB) {
			targetLock, targetLockErr := processlock.Acquire(targetDB)
			if targetLockErr != nil {
				return fmt.Errorf("acquire restore-pre-migrate lock; stop Manager Server and retry: %w", targetLockErr)
			}
			canonicalDBPath = targetLock.DatabasePath()
			if closeErr := targetLock.Close(); closeErr != nil {
				return fmt.Errorf("release target database lock: %w", closeErr)
			}
		} else {
			canonicalDBPath = canonicalFallbackDB
		}
	}

	restoreArgs := []string{
		"restore",
		"--db-path", canonicalDBPath,
		"--data-key-path", dataKeyPath,
		"--snapshot-dir", snapshotDir,
	}
	return managerdatasnapshot.Run(runCtx, restoreArgs, stdout, stderr)
}

func parseArgs(args []string, stderr io.Writer) (options, error) {
	opts := options{
		Timeout: defaultRestoreTimeout,
	}
	fs := flag.NewFlagSet("restore-pre-migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.SnapshotDir, "snapshot-dir", "", "explicit pre-migrate or pre-slot snapshot directory")
	fs.BoolVar(&opts.Latest, "latest", false, "auto-select the newest pre-migrate-* or pre-slot-* snapshot under <dataDir>/backups")
	fs.StringVar(&opts.DataDir, "data-dir", "", "Manager data directory fallback when --db-path / USAGE_DATA_DIR are unset")
	fs.StringVar(&opts.DBPath, "db-path", "", "SQLite database path (defaults to <USAGE_DATA_DIR>/usage.sqlite)")
	fs.StringVar(&opts.DataKeyPath, "data-key-path", "", "data.key path (defaults to <USAGE_DATA_DIR>/data.key)")
	fs.BoolVar(&opts.Confirm, "confirm", false, "confirm discarding post-upgrade business writes by restoring the pre-upgrade snapshot")
	fs.DurationVar(&opts.Timeout, "timeout", defaultRestoreTimeout, "maximum restore duration")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: cpa-manager-plus restore-pre-migrate (--snapshot-dir PATH | --latest) [--db-path PATH] [--data-key-path PATH] [--timeout DURATION] --confirm")
		_, _ = fmt.Fprintln(stderr, "Stop Manager Server before running this command. Restoring after an upgrade discards business writes recorded since the snapshot.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	opts.SnapshotDir = strings.TrimSpace(opts.SnapshotDir)
	opts.DataDir = strings.TrimSpace(opts.DataDir)
	opts.DBPath = strings.TrimSpace(opts.DBPath)
	opts.DataKeyPath = strings.TrimSpace(opts.DataKeyPath)
	if opts.SnapshotDir != "" && opts.Latest {
		return options{}, errors.New("--snapshot-dir and --latest cannot be used together")
	}
	if opts.SnapshotDir == "" && !opts.Latest {
		return options{}, errors.New("either --snapshot-dir or --latest is required")
	}
	if opts.Timeout <= 0 {
		return options{}, errors.New("--timeout must be a positive duration")
	}
	return opts, nil
}

func resolveTargetPaths(explicitDBPath, explicitDataKeyPath, explicitDataDir string) (dbPath string, dataKeyPath string, backupsDir string, err error) {
	usageDataDir := strings.TrimSpace(os.Getenv("USAGE_DATA_DIR"))
	effectiveDataDir := usageDataDir
	if effectiveDataDir == "" {
		effectiveDataDir = strings.TrimSpace(explicitDataDir)
	}
	hasExplicitConfig := strings.TrimSpace(os.Getenv("CPA_MANAGER_CONFIG")) != ""
	var loadedCfg config.Config
	needConfig := hasExplicitConfig ||
		(explicitDBPath == "" && effectiveDataDir == "") ||
		(explicitDataKeyPath == "" && explicitDBPath == "" && effectiveDataDir == "" && strings.TrimSpace(os.Getenv("CPA_MANAGER_DATA_KEY_PATH")) == "")
	if needConfig {
		cfg, cfgErr := config.LoadWithoutCreatingDefault()
		if cfgErr != nil {
			return "", "", "", fmt.Errorf("load config: %w", cfgErr)
		}
		loadedCfg = cfg
	}

	switch {
	case explicitDBPath != "":
		dbPath = explicitDBPath
	case strings.TrimSpace(os.Getenv("USAGE_DB_PATH")) != "":
		dbPath = strings.TrimSpace(os.Getenv("USAGE_DB_PATH"))
	case usageDataDir != "":
		dbPath = filepath.Join(usageDataDir, "usage.sqlite")
	case hasExplicitConfig && strings.TrimSpace(loadedCfg.DBPath) != "":
		dbPath = strings.TrimSpace(loadedCfg.DBPath)
	case effectiveDataDir != "":
		dbPath = filepath.Join(effectiveDataDir, "usage.sqlite")
	default:
		dbPath = strings.TrimSpace(loadedCfg.DBPath)
	}
	if dbPath == "" {
		return "", "", "", errors.New("SQLite database path is empty; pass --db-path or set USAGE_DATA_DIR")
	}

	switch {
	case explicitDataKeyPath != "":
		dataKeyPath = explicitDataKeyPath
	case strings.TrimSpace(os.Getenv("CPA_MANAGER_DATA_KEY_PATH")) != "":
		dataKeyPath = strings.TrimSpace(os.Getenv("CPA_MANAGER_DATA_KEY_PATH"))
	case explicitDBPath != "" && effectiveDataDir == "":
		dataKeyPath = filepath.Join(filepath.Dir(dbPath), "data.key")
	case usageDataDir != "":
		dataKeyPath = filepath.Join(usageDataDir, "data.key")
	case hasExplicitConfig && strings.TrimSpace(loadedCfg.DataKeyPath) != "":
		dataKeyPath = strings.TrimSpace(loadedCfg.DataKeyPath)
	case effectiveDataDir != "":
		dataKeyPath = filepath.Join(effectiveDataDir, "data.key")
	default:
		dataKeyPath = strings.TrimSpace(loadedCfg.DataKeyPath)
	}
	if dataKeyPath == "" {
		dataKeyPath = filepath.Join(filepath.Dir(dbPath), "data.key")
	}

	if explicitDBPath == "" && usageDataDir != "" {
		backupsDir = filepath.Join(usageDataDir, "backups")
	} else {
		backupsDir = filepath.Join(filepath.Dir(dbPath), "backups")
	}
	return dbPath, dataKeyPath, backupsDir, nil
}

func selectLatestBackupSnapshot(backupsDir string) (string, error) {
	absBackups, err := filepath.Abs(backupsDir)
	if err != nil {
		return "", fmt.Errorf("resolve backups directory: %w", err)
	}
	info, err := os.Lstat(absBackups)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("no backups directory found at %s; pass --snapshot-dir explicitly", absBackups)
	}
	if err != nil {
		return "", fmt.Errorf("inspect backups directory %s: %w", absBackups, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("backups path %s is not a regular directory", absBackups)
	}

	entries, err := os.ReadDir(absBackups)
	if err != nil {
		return "", fmt.Errorf("read backups directory %s: %w", absBackups, err)
	}

	type snapshotCandidate struct {
		path    string
		name    string
		modTime time.Time
	}
	var candidates []snapshotCandidate
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "pre-migrate-") && !strings.HasPrefix(name, "pre-slot-") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		candidatePath := filepath.Join(absBackups, name)
		manifestInfo, ok := validateBackupCandidate(candidatePath)
		if !ok {
			continue
		}
		candidates = append(candidates, snapshotCandidate{
			path:    candidatePath,
			name:    name,
			modTime: manifestInfo.ModTime(),
		})
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no valid pre-migrate-* or pre-slot-* snapshots found in %s", absBackups)
	}

	slices.SortFunc(candidates, func(a, b snapshotCandidate) int {
		if !a.modTime.Equal(b.modTime) {
			if a.modTime.After(b.modTime) {
				return -1
			}
			return 1
		}
		return strings.Compare(b.name, a.name)
	})
	return candidates[0].path, nil
}

func selectLatestBackupSnapshotWithFallback(primaryBackupsDir, explicitDataDir string) (string, error) {
	selected, err := selectLatestBackupSnapshot(primaryBackupsDir)
	if err == nil {
		return selected, nil
	}
	trimmedDataDir := strings.TrimSpace(explicitDataDir)
	if trimmedDataDir == "" {
		return "", err
	}
	altBackupsDir := filepath.Join(trimmedDataDir, "backups")
	if filepath.Clean(altBackupsDir) == filepath.Clean(primaryBackupsDir) {
		return "", err
	}
	if altSelected, altErr := selectLatestBackupSnapshot(altBackupsDir); altErr == nil {
		return altSelected, nil
	}
	return "", err
}

type manifestHeader struct {
	Version           int                       `json:"version"`
	TargetDBPath      string                    `json:"target_db_path,omitempty"`
	TargetDataKeyPath string                    `json:"target_data_key_path,omitempty"`
	Files             map[string]map[string]any `json:"files"`
}

func readSnapshotManifestTargetPaths(snapshotDir, explicitDataDir string) (string, string, error) {
	trimmedSnap := strings.TrimSpace(snapshotDir)
	if trimmedSnap == "" {
		return "", "", nil
	}
	absSnap, err := filepath.Abs(trimmedSnap)
	if err != nil {
		return "", "", fmt.Errorf("resolve snapshot directory: %w", err)
	}
	dirInfo, err := os.Lstat(absSnap)
	if os.IsNotExist(err) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("inspect snapshot directory %s: %w", absSnap, err)
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return "", "", fmt.Errorf("snapshot directory %s must be a regular directory", absSnap)
	}
	manifestPath := filepath.Join(absSnap, "manifest.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if os.IsNotExist(err) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("inspect snapshot manifest %s: %w", manifestPath, err)
	}
	if manifestInfo.Mode()&os.ModeSymlink != 0 || !manifestInfo.Mode().IsRegular() {
		return "", "", fmt.Errorf("snapshot manifest %s must be a regular file", manifestPath)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", "", fmt.Errorf("read snapshot manifest %s: %w", manifestPath, err)
	}
	var parsed manifestHeader
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", "", fmt.Errorf("decode snapshot manifest %s: %w", manifestPath, err)
	}
	targetDB, err := validateManifestTargetPath(parsed.TargetDBPath, "target_db_path", explicitDataDir)
	if err != nil {
		return "", "", err
	}
	targetKey, err := validateManifestTargetPath(parsed.TargetDataKeyPath, "target_data_key_path", explicitDataDir)
	if err != nil {
		return "", "", err
	}
	return targetDB, targetKey, nil
}

func validateManifestTargetPath(rawTarget, fieldName, explicitDataDir string) (string, error) {
	if rawTarget == "" {
		return "", nil
	}
	if strings.ContainsRune(rawTarget, '\x00') || strings.TrimSpace(rawTarget) != rawTarget {
		return "", fmt.Errorf("snapshot manifest %s %q is invalid", fieldName, rawTarget)
	}
	if !filepath.IsAbs(rawTarget) || filepath.Clean(rawTarget) != rawTarget {
		return "", fmt.Errorf("snapshot manifest %s %q must be a clean absolute path", fieldName, rawTarget)
	}
	if err := ensureNoSymlinkPathAncestors(filepath.Dir(rawTarget)); err != nil {
		return "", fmt.Errorf("snapshot manifest %s rejected: %w", fieldName, err)
	}
	if leafInfo, err := os.Lstat(rawTarget); err == nil {
		if leafInfo.Mode()&os.ModeSymlink != 0 || !leafInfo.Mode().IsRegular() {
			return "", fmt.Errorf("snapshot manifest %s %q is not a regular file", fieldName, rawTarget)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect snapshot manifest %s %q: %w", fieldName, rawTarget, err)
	}

	trimmedDataDir := strings.TrimSpace(explicitDataDir)
	if trimmedDataDir != "" {
		absDataDir, err := filepath.Abs(trimmedDataDir)
		if err != nil {
			return "", fmt.Errorf("resolve --data-dir: %w", err)
		}
		cleanDataDir := filepath.Clean(absDataDir)
		if err := ensureNoSymlinkPathAncestors(cleanDataDir); err != nil {
			return "", fmt.Errorf("--data-dir rejected: %w", err)
		}
		if dataDirInfo, err := os.Lstat(cleanDataDir); err == nil {
			if dataDirInfo.Mode()&os.ModeSymlink != 0 || !dataDirInfo.IsDir() {
				return "", fmt.Errorf("--data-dir %q is not a regular directory", cleanDataDir)
			}
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect --data-dir %q: %w", cleanDataDir, err)
		}
		baseDir, rel, ok := relWithinDataDir(cleanDataDir, rawTarget)
		if !ok {
			return "", fmt.Errorf("snapshot manifest %s %q escapes --data-dir %q", fieldName, rawTarget, cleanDataDir)
		}
		if err := ensureNoSymlinkRelativeSegments(baseDir, rel); err != nil {
			return "", fmt.Errorf("snapshot manifest %s %q symlink escape inside --data-dir: %w", fieldName, rawTarget, err)
		}
	}
	return rawTarget, nil
}

func relWithinDataDir(cleanDataDir, cleanTarget string) (string, string, bool) {
	candidates := []string{cleanDataDir}
	if runtime.GOOS == "darwin" {
		for _, sysPrefix := range []string{"/var/", "/tmp/", "/etc/"} {
			switch {
			case strings.HasPrefix(cleanDataDir, sysPrefix):
				candidates = append(candidates, "/private"+cleanDataDir)
			case strings.HasPrefix(cleanDataDir, "/private"+sysPrefix):
				candidates = append(candidates, strings.TrimPrefix(cleanDataDir, "/private"))
			}
		}
	}
	for _, base := range candidates {
		rel, err := filepath.Rel(base, cleanTarget)
		if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) {
			continue
		}
		if strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return base, rel, true
	}
	return "", "", false
}

func ensureNoSymlinkRelativeSegments(baseDir, rel string) error {
	parts := strings.Split(rel, string(filepath.Separator))
	cur := baseDir
	for idx, part := range parts {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component %s is a symbolic link", cur)
		}
		if idx < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("path component %s is not a directory", cur)
		}
		if idx == len(parts)-1 && !info.Mode().IsRegular() {
			return fmt.Errorf("target %s is not a regular file", cur)
		}
	}
	return nil
}

func ensureNoSymlinkPathAncestors(dir string) error {
	clean := filepath.Clean(dir)
	vol := filepath.VolumeName(clean)
	rest := strings.TrimPrefix(clean, vol)
	cur := vol + string(filepath.Separator)
	for _, part := range strings.Split(rest, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if runtime.GOOS == "darwin" && (cur == "/var" || cur == "/tmp" || cur == "/etc") {
				continue
			}
			return fmt.Errorf("path component %s is a symbolic link", cur)
		}
	}
	return nil
}

func validateBackupCandidate(snapshotDir string) (os.FileInfo, bool) {
	dirInfo, err := os.Lstat(snapshotDir)
	if err != nil || dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return nil, false
	}
	manifestPath := filepath.Join(snapshotDir, "manifest.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || manifestInfo.Mode()&os.ModeSymlink != 0 || !manifestInfo.Mode().IsRegular() {
		return nil, false
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, false
	}
	var parsed manifestHeader
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, false
	}
	if parsed.Version != 1 || len(parsed.Files) != len(requiredManifestMembers) {
		return nil, false
	}
	for _, member := range requiredManifestMembers {
		if _, ok := parsed.Files[member]; !ok {
			return nil, false
		}
	}
	return manifestInfo, true
}
