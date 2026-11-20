package upgrade

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
	_ "modernc.org/sqlite"
)

// snapshotDirPrefix names the transient directories this package owns. They are
// always removed before a preflight or a status command returns.
const snapshotDirPrefix = "cpamp-upgrade-snapshot-"

var (
	// ErrSnapshotFailed reports scratch space this process could not use. It is
	// a blocking condition: without a private copy there is no read-only way to
	// inspect a WAL database. It is also the code for a scratch directory that
	// would land inside a tree the operation must not write to.
	ErrSnapshotFailed = errors.New("upgrade database snapshot could not be created")

	// ErrSnapshotRootUnknown reports a read that would have to stage a private
	// copy but cannot name the configured source root it must stay out of — a
	// mount lookup that outlived its bound before it resolved the root, for
	// example. It fails closed: the unnamed root may hold the temporary
	// directory, and a snapshot abandoned by a killed process would stay in a
	// tree this operation must not write to.
	ErrSnapshotRootUnknown = errors.New("upgrade credential snapshot root is unknown")

	// ErrSnapshotRootTimedOut reports a read that would have to stage a private
	// copy while the configured source root is known but was never inspected:
	// the mount lookup that would have inspected it outlived its bound.
	// Canonicalizing that root for the exclusion check is the filesystem call the
	// bound exists to stop, so the read fails closed instead of re-entering the
	// mount; the caller answers the mount's deadline, which is a retry and never
	// a verdict on the source or the presented key.
	ErrSnapshotRootTimedOut = errors.New("upgrade credential snapshot root timed out")

	errPendingRollbackJournal = errors.New("database has a pending rollback journal")

	// afterSnapshotCopy is the package's synchronous test seam. It runs once
	// between the copy and the source's second digest, so a test can prove that
	// a source which gains a WAL or a rewrite at exactly that moment is refused
	// instead of answering from the state it held before. Production never sets
	// it.
	afterSnapshotCopy func()
)

// SnapshotAttempts bounds how often a snapshot of a database a writer may still
// be serving is retried. A live writer commits in bursts, so a handful of
// attempts across roughly a second normally finds a quiet window. Only
// ErrSourceChanged is retried; a writer that never pauses is reported, never
// read in place.
const (
	SnapshotAttempts  = 8
	SnapshotBaseDelay = 20 * time.Millisecond
	SnapshotMaxDelay  = 200 * time.Millisecond
)

// OpenSnapshotWithRetry copies a database with the bounded retry every caller of
// a possibly-live store uses. The open function is a parameter so a caller can
// keep its own seam; production passes OpenSnapshot.
//
// This is the one retry policy for a store that may still be written. A missing
// database, an unusable scratch directory or any other failure does not get
// better by waiting, so only ErrSourceChanged is attempted again.
func OpenSnapshotWithRetry(ctx context.Context, open func(context.Context, string, string) (*Snapshot, error),
	databasePath string, scratchBase string) (*Snapshot, error) {
	delay := SnapshotBaseDelay
	var lastErr error
	for attempt := 1; attempt <= SnapshotAttempts; attempt++ {
		snapshot, err := open(ctx, databasePath, scratchBase)
		if err == nil {
			return snapshot, nil
		}
		if !errors.Is(err, ErrSourceChanged) {
			return nil, err
		}
		lastErr = err
		if attempt == SnapshotAttempts {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if delay < SnapshotMaxDelay {
			delay *= 2
			if delay > SnapshotMaxDelay {
				delay = SnapshotMaxDelay
			}
		}
	}
	return nil, lastErr
}

// databaseSidecars are the files SQLite needs to see every committed row of a
// WAL database: the database, its write-ahead log and the WAL index.
var databaseSidecars = []string{"", "-wal", "-shm"}

type databaseFileDigest struct {
	size   int64
	digest string
}

// Snapshot is a private copy of a SQLite database and its WAL sidecars.
//
// Nothing in this package ever opens a source or Manager database in place. A
// plain read-only open of a WAL database creates -shm/-wal files beside it, so
// preflight would write into a stopped source and `upgrade status` would fail on
// a read-only Manager volume. The copy is opened instead and removed when the
// snapshot is closed.
type Snapshot struct {
	dir   string
	path  string
	bytes int64
	db    *sql.DB
}

// OpenSnapshot copies databasePath plus its -wal/-shm sidecars into a fresh
// directory under scratchBase and opens the copy read-only. An empty
// scratchBase uses the OS temporary directory.
//
// The source files are digested before the copy, again after it, and the copy is
// digested as it is written. Any difference means the source was not stopped, so
// the copy is discarded and ErrSourceChanged is returned instead of a report
// about a mixture of two states.
func OpenSnapshot(ctx context.Context, databasePath string, scratchBase string) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	before, err := digestDatabaseFiles(ctx, databasePath)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(scratchBase, snapshotDirPrefix)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSnapshotFailed, rootCause(err))
	}
	snapshot := &Snapshot{dir: dir, path: filepath.Join(dir, filepath.Base(databasePath))}
	copied, err := copyDatabaseFiles(ctx, databasePath, dir)
	if err == nil {
		// A source that starts writing while its private copy is being taken
		// must be refused, not read as the state it held before the write. The
		// second digest is what proves it, so a test places the write between the
		// copy and that digest through this synchronous seam. Production never
		// sets it.
		if afterSnapshotCopy != nil {
			afterSnapshotCopy()
		}
		var after map[string]databaseFileDigest
		if after, err = digestDatabaseFiles(ctx, databasePath); err == nil {
			if !sameDigests(before, after) || !sameCopiedDigests(before, copied) {
				err = ErrSourceChanged
			}
		}
	}
	if err != nil {
		_ = snapshot.Close()
		return nil, err
	}
	if wal, hasWAL := copied["-wal"]; hasWAL && wal.size > 0 {
		if mainCopy := copied[""]; mainCopy.size == 0 {
			if err := validateZeroByteDatabaseWAL(snapshot.path); err != nil {
				_ = snapshot.Close()
				return nil, err
			}
		} else if err := managersqlite.ValidateWALHeader(snapshot.path); err != nil {
			_ = snapshot.Close()
			return nil, err
		}
	}
	// The WAL index is a derived structure: a copy taken while the writer was
	// active can pair a stale -shm with a newer -wal. Dropping it lets SQLite
	// rebuild the index from the copied WAL inside our own scratch directory.
	if err := os.Remove(snapshot.path + "-shm"); err != nil && !os.IsNotExist(err) {
		_ = snapshot.Close()
		return nil, fmt.Errorf("%w: %s", ErrSnapshotFailed, rootCause(err))
	}
	if mainCopy := copied[""]; mainCopy.size == 0 {
		if wal, hasWAL := copied["-wal"]; hasWAL && wal.size > 0 {
			if err := replayZeroByteSnapshotWAL(ctx, snapshot.path); err != nil {
				_ = snapshot.Close()
				return nil, err
			}
		}
	}
	db, err := sql.Open("sqlite", snapshotDataSourceName(snapshot.path))
	if err != nil {
		_ = snapshot.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		_ = snapshot.Close()
		return nil, err
	}
	snapshot.db = db
	for suffix, file := range copied {
		if suffix == "-shm" {
			continue
		}
		snapshot.bytes += file.size
	}
	snapshot.bytes = snapshot.Bytes()
	return snapshot, nil
}

// ReadRecordFromSnapshot reads the recorded operation row from a private copy of
// the Manager database. It exists for the resume guard: a target a copy-in has
// not finished must never be opened in place, because that open creates or
// rewrites the WAL index the resume compares by file identity. A missing
// database is "no record", not a failure.
func ReadRecordFromSnapshot(ctx context.Context, databasePath string, scratchBase string) (model.UpgradeOperation, bool, error) {
	info, err := os.Lstat(databasePath)
	if err != nil {
		if os.IsNotExist(err) {
			if sideErr := copyinmarker.DatabaseSidecarsAbsent(databasePath); sideErr != nil {
				return model.UpgradeOperation{}, false, sideErr
			}
			return model.UpgradeOperation{}, false, nil
		}
		return model.UpgradeOperation{}, false, err
	}
	if info.Mode().IsRegular() && info.Size() == 0 {
		if sideErr := copyinmarker.DatabaseSidecarsAbsent(databasePath); sideErr != nil {
			return model.UpgradeOperation{}, false, sideErr
		}
	}
	snapshot, err := OpenSnapshotWithRetry(ctx, OpenSnapshot, databasePath, scratchBase)
	if err != nil {
		return model.UpgradeOperation{}, false, err
	}
	defer func() { _ = snapshot.Close() }()
	return ReadRecordFrom(ctx, snapshot.DB())
}

// snapshotReadError keeps context and existing Upgrade errors intact while
// giving private-copy failures the shared, actionable snapshot code.
func snapshotReadError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var upgradeErr *UpgradeError
	if errors.As(err, &upgradeErr) {
		return err
	}
	if errors.Is(err, ErrSnapshotFailed) {
		return newUpgradeError(UpgradeErrorSnapshotFailed,
			"the private database snapshot could not be created; choose writable scratch space outside the preserved data roots", err)
	}
	return err
}

// ValidateScratchDir refuses a scratch directory that lies inside a root this
// operation must not write to.
//
// The snapshot copies the whole database, so a scratch directory under the
// legacy source root would write that copy back into the tree preflight promises
// to preserve, and one under the target root would leave upgrade scratch in the
// volume the copy is measured against. Both are refused before a directory is
// created.
//
// An empty scratch directory means the OS temporary directory, and it is
// checked like an explicit one. A host whose TMPDIR points into the legacy root
// or the target volume would otherwise hide a full database copy in a tree this
// operation must not write to, and a snapshot abandoned by a killed process —
// no deferred cleanup runs — would stay on that volume for good.
func ValidateScratchDir(scratchDir string, legacyRoot string, targetDir string) error {
	return ValidateScratchDirWithBackup(scratchDir, legacyRoot, targetDir, "")
}

// ValidateScratchDirWithBackup refuses a scratch directory that lies inside the
// legacy source root, the target root, or a non-empty backup bundle directory.
func ValidateScratchDirWithBackup(scratchDir string, legacyRoot string, targetDir string, bundleDir string) error {
	roots := []ScratchRoot{
		{Label: "the legacy source root", Dir: legacyRoot},
		{Label: "the target root", Dir: targetDir},
		{Label: "the backup directory", Dir: bundleDir},
	}
	for _, cfgLegacy := range ConfiguredLegacyRoots() {
		roots = append(roots, ScratchRoot{Label: "the configured legacy source root", Dir: cfgLegacy})
	}
	for _, cfgBundle := range ConfiguredBundleRoots() {
		roots = append(roots, ScratchRoot{Label: "the configured backup directory", Dir: cfgBundle})
	}
	return validateScratchRoots(scratchDir, roots)
}

// ValidateScratchRoots refuses a scratch directory (or os.TempDir when empty)
// that lies inside any non-empty ScratchRoot in roots.
func ValidateScratchRoots(scratchDir string, roots ...ScratchRoot) error {
	return validateScratchRoots(scratchDir, roots)
}

// ConfiguredLegacyRoots returns non-empty legacy source roots configured in the
// process environment (CPAMP_UPGRADE_LEGACY_ROOT, CPAMP_LEGACY_ROOT,
// CPAMP_UPGRADE_SOURCE_ROOT).
func ConfiguredLegacyRoots() []string {
	var roots []string
	for _, envName := range []string{
		"CPAMP_UPGRADE_LEGACY_ROOT",
		"CPAMP_LEGACY_ROOT",
		"CPAMP_UPGRADE_SOURCE_ROOT",
	} {
		if val := strings.TrimSpace(os.Getenv(envName)); val != "" {
			roots = append(roots, val)
		}
	}
	return roots
}

// ConfiguredBundleRoots returns non-empty external backup directories
// configured in the process environment.
func ConfiguredBundleRoots() []string {
	var roots []string
	for _, envName := range []string{
		"CPAMP_UPGRADE_BUNDLE_DIR",
		"CPAMP_UPGRADE_BACKUP_DIR",
	} {
		if val := strings.TrimSpace(os.Getenv(envName)); val != "" {
			roots = append(roots, val)
		}
	}
	return roots
}

// ScratchRoot is one directory a private copy must stay out of, named the way the
// refusal names it to the operator.
//
// The label travels with the directory rather than being inferred from its
// position: the same call excludes differently rooted trees — the Manager's data
// root and data-key directory on one path, the configured 1.x source root on
// another — and a refusal that named the wrong tree would send the operator to
// the wrong directory.
type ScratchRoot struct {
	Label string
	Dir   string
}

// PreservedSource names the files of a source that a read must never write
// beside: the database and the Manager data key its secrets are sealed with.
//
// The data key is part of the source even though it is a separate file, and a
// configuration may place it outside the database's directory — a data root
// mounted at /data with the key at /keys/data.key, for example. A private copy
// that landed in the key's directory would leave a full database copy in a tree
// this read promises to preserve, so the key's directory is excluded exactly like
// the database's own.
type PreservedSource struct {
	// Database is the database file being read. Only its directory is used.
	Database string
	// DataKey is the Manager data key that unlocks the source's secrets. Empty
	// means the caller does not know it, which only a source with no sealed
	// secret (and no caller that resolves one) can honestly report.
	DataKey string
}

// ValidatePreservedScratchDir refuses a scratch directory inside any tree a
// preserved-source read must not write to: the source database's directory, the
// directory that holds the source's data key, and every extra root the caller
// resolved (the target data root, the configured 1.x mount). It is the single
// check every read that stages a private copy beside a preserved source uses, so
// no entry point can exclude a different set of trees than another.
func ValidatePreservedScratchDir(scratchDir string, source PreservedSource, extraRoots ...ScratchRoot) error {
	roots := make([]ScratchRoot, 0, 2+len(extraRoots))
	if database := strings.TrimSpace(source.Database); database != "" {
		roots = append(roots, ScratchRoot{Label: "the source database directory", Dir: filepath.Dir(database)})
	}
	if dataKey := strings.TrimSpace(source.DataKey); dataKey != "" {
		roots = append(roots, ScratchRoot{Label: "the Manager data key directory", Dir: filepath.Dir(dataKey)})
	}
	return validateScratchRoots(scratchDir, append(roots, extraRoots...))
}

// validateScratchRoots refuses a scratch directory that lies inside any named
// root. The canonicalization and the operator-facing message are shared, so a
// caller cannot weaken the check by naming a tree through another wrapper.
func validateScratchRoots(scratchDir string, roots []ScratchRoot) error {
	scratch := strings.TrimSpace(scratchDir)
	defaulted := scratch == ""
	if defaulted {
		scratch = os.TempDir()
	}
	scratchPath, err := canonicalPathStrict(scratch)
	if err != nil {
		if defaulted {
			return fmt.Errorf("%w: default scratch directory (the OS temporary directory) cannot be resolved: %v",
				ErrSnapshotFailed, err)
		}
		return fmt.Errorf("%w: scratch directory cannot be resolved: %v", ErrSnapshotFailed, err)
	}
	for _, root := range roots {
		dir := strings.TrimSpace(root.Dir)
		if dir == "" {
			continue
		}
		exclRoot := canonicalExclusionRoot(dir)
		if _, err := canonicalPathStrict(dir); err != nil {
			return fmt.Errorf("%w: %s cannot be resolved: %v", ErrSnapshotFailed, root.Label, err)
		}
		if pathWithin(scratchPath, exclRoot) {
			// The default is named as such: an operator who never passed
			// --scratch-dir has to learn that the OS temporary directory is the
			// one that landed inside the root.
			origin := "scratch directory"
			// The hint names only the control that every caller has: rollback
			// and migrate take no --scratch-dir flag, so naming one here would
			// send the operator to an argument the command cannot accept.
			hint := "choose a scratch directory on a separate volume"
			if defaulted {
				origin = "default scratch directory (the OS temporary directory)"
				hint = "point TMPDIR at a directory on a separate volume"
			}
			return fmt.Errorf("%w: %s is inside %s; %s",
				ErrSnapshotFailed, origin, root.Label, hint)
		}
	}
	if !defaulted {
		info, statErr := os.Stat(scratch)
		if statErr != nil {
			return fmt.Errorf("%w: scratch directory is not accessible: %v", ErrSnapshotFailed, statErr)
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: scratch directory is not a directory", ErrSnapshotFailed)
		}
	} else if info, statErr := os.Stat(scratch); statErr == nil && !info.IsDir() {
		return fmt.Errorf("%w: default scratch directory (the OS temporary directory) is not a directory", ErrSnapshotFailed)
	}
	return nil
}

// pathWithin reports whether child is parent itself or lies underneath it. Both
// paths must already be canonical.
//
// The separator is added only when the parent does not already end in one. The
// filesystem root is a parent of every absolute path, and appending a separator
// to "/" would build the "//" prefix that no cleaned path starts with — so an
// operator who set the source root to "/" (or to any aliased spelling of it)
// would pass the exclusion check and stage the whole database copy inside that
// root. A parent with a trailing separator, such as "/a/", names the same tree
// as "/a" and is handled by cleaning both sides first.
func pathWithin(child string, parent string) bool {
	child = filepath.Clean(child)
	parent = filepath.Clean(parent)
	if child == parent {
		return true
	}
	prefix := parent
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(child, prefix)
}

// canonicalPath makes a path absolute and resolves the symlinks of the deepest
// existing ancestor. A directory that does not exist yet still belongs to a real
// parent, and on some hosts (macOS /var, for example) comparing unresolved paths
// with resolved ones reports a false miss.
func canonicalPathStrict(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := absolute
	var missing []string
	for {
		resolved, evalErr := filepath.EvalSymlinks(current)
		if evalErr == nil {
			if len(missing) > 0 {
				info, statErr := os.Stat(resolved)
				if statErr != nil {
					return "", statErr
				}
				if !info.IsDir() {
					return "", fmt.Errorf("%q is not a directory", current)
				}
			}
			return filepath.Join(append([]string{resolved}, missing...)...), nil
		}
		if info, lstatErr := os.Lstat(current); lstatErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("dangling or unresolvable symlink %q: %w", current, evalErr)
			}
			return "", evalErr
		} else if !errors.Is(lstatErr, os.ErrNotExist) {
			return "", lstatErr
		}
		if !errors.Is(evalErr, os.ErrNotExist) {
			return "", evalErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", evalErr
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = parent
	}
}

func canonicalDirectoryCandidate(path, label string) (string, error) {
	resolved, err := canonicalPathStrict(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	if info, statErr := os.Stat(resolved); statErr == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("%s %q is not a directory", label, path)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect %s: %w", label, statErr)
	}
	return resolved, nil
}

func canonicalPath(path string) string {
	if resolved, err := canonicalPathStrict(path); err == nil {
		return resolved
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return absolute
}

// canonicalExclusionRoot resolves one root whose subtree the scratch directory
// must stay out of. It is the package's synchronous seam for that single
// filesystem call: a test substitutes it to prove that a root whose mount lookup
// already timed out is never resolved again. Production never sets it.
var canonicalExclusionRoot = canonicalPath

// DB returns the read-only handle on the private copy.
func (s *Snapshot) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Dir returns the temporary directory holding this snapshot copy, or empty once
// the snapshot has been closed.
func (s *Snapshot) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Bytes returns the total regular-file bytes currently held inside this
// snapshot's temporary directory, or zero once closed.
func (s *Snapshot) Bytes() int64 {
	if s == nil || s.dir == "" {
		return 0
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return s.bytes
	}
	var total int64
	for _, entry := range entries {
		info, statErr := entry.Info()
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		total += info.Size()
	}
	if total < s.bytes {
		return s.bytes
	}
	return total
}

// Close releases the copy and deletes the scratch directory.
func (s *Snapshot) Close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.db != nil {
		err = s.db.Close()
		s.db = nil
	}
	if s.dir != "" {
		err = errors.Join(err, os.RemoveAll(s.dir))
		s.dir = ""
	}
	s.bytes = 0
	return err
}

// snapshotDataSourceName opens the private copy without write access to the
// database itself. query_only is a second fence: even a writer-capable handle
// refuses every statement that would change the copy.
func snapshotDataSourceName(path string) string {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := &url.URL{Scheme: "file", Path: uriPath}
	query := dsn.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

// auditedSidecarSuffixes is the single journal sidecar list shared across every
// reader and classifier of a preserved source.
var auditedSidecarSuffixes = []string{"-wal", "-shm", "-journal"}

// AuditSourceSidecars applies the shared source-file policy to every journal
// member beside databasePath and fails closed when a non-empty rollback journal
// is present.
var errInvalidSourceSidecar = errors.New("invalid database sidecar")

func AuditSourceSidecars(databasePath string) error {
	for _, suffix := range auditedSidecarSuffixes {
		sidePath := databasePath + suffix
		info, err := inspectSnapshotSourceFile(sidePath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			if errors.Is(err, processlock.ErrSymlinkedDatabase) {
				return &symlinkedSourceFile{name: filepath.Base(sidePath)}
			}
			if errors.Is(err, processlock.ErrHardLinked) {
				return err
			}
			return errors.Join(errInvalidSourceSidecar, err)
		}
		if suffix == "-journal" && info.Size() > 0 {
			return errors.Join(errInvalidSourceSidecar, errPendingRollbackJournal)
		}
	}
	return nil
}

func auditSidecarsRegularSingleLink(databasePath string) error {
	for _, suffix := range auditedSidecarSuffixes {
		info, err := processlock.InspectRegularSingleLink(databasePath + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if suffix == "-journal" && info.Size() > 0 {
			return errPendingRollbackJournal
		}
	}
	return nil
}

// validateZeroByteDatabaseWAL validates a populated SQLite WAL paired with an
// uncheckpointed 0-byte main database file. It checks the 32-byte WAL header,
// active-cycle frame checksums, and requires at least one committed active
// frame carrying a valid page-1 SQLite header with matching page size.
func validateZeroByteDatabaseWAL(databasePath string) error {
	_, err := extractCommittedPage1FromWAL(databasePath + "-wal")
	return err
}

func extractCommittedPage1FromWAL(walPath string) ([]byte, error) {
	wal, err := os.Open(walPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = wal.Close() }()
	var header [32]byte
	if _, err := io.ReadFull(wal, header[:]); err != nil {
		return nil, err
	}
	magic := binary.BigEndian.Uint32(header[:4])
	if magic != 0x377f0682 && magic != 0x377f0683 {
		return nil, errors.New("invalid WAL header magic")
	}
	if binary.BigEndian.Uint32(header[4:8]) != 3007000 {
		return nil, errors.New("unsupported WAL file format version")
	}
	pageSize := binary.BigEndian.Uint32(header[8:12])
	if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 {
		return nil, fmt.Errorf("invalid WAL page size %d", pageSize)
	}
	var order binary.ByteOrder = binary.LittleEndian
	if magic&1 != 0 {
		order = binary.BigEndian
	}
	checksum := func(buf []byte, s1, s2 uint32) (uint32, uint32) {
		for i := 0; i < len(buf); i += 8 {
			s1 += order.Uint32(buf[i:i+4]) + s2
			s2 += order.Uint32(buf[i+4:i+8]) + s1
		}
		return s1, s2
	}
	s1, s2 := checksum(header[:24], 0, 0)
	if s1 != binary.BigEndian.Uint32(header[24:28]) || s2 != binary.BigEndian.Uint32(header[28:32]) {
		return nil, errors.New("invalid WAL header checksum")
	}
	salt1 := binary.BigEndian.Uint32(header[16:20])
	salt2 := binary.BigEndian.Uint32(header[20:24])
	frame := make([]byte, 24+int(pageSize))
	var pendingPage1 []byte
	var committedPage1 []byte
	for {
		_, err := io.ReadFull(wal, frame)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if binary.BigEndian.Uint32(frame[8:12]) != salt1 || binary.BigEndian.Uint32(frame[12:16]) != salt2 {
			break
		}
		s1, s2 = checksum(frame[:8], s1, s2)
		s1, s2 = checksum(frame[24:], s1, s2)
		if s1 != binary.BigEndian.Uint32(frame[16:20]) || s2 != binary.BigEndian.Uint32(frame[20:24]) {
			break
		}
		pageNumber := binary.BigEndian.Uint32(frame[:4])
		if pageNumber == 0 {
			return nil, errors.New("invalid WAL frame page number")
		}
		if pageNumber == 1 {
			if pendingPage1 == nil {
				pendingPage1 = make([]byte, int(pageSize))
			}
			copy(pendingPage1, frame[24:])
		}
		if binary.BigEndian.Uint32(frame[4:8]) > 0 && pendingPage1 != nil {
			if committedPage1 == nil {
				committedPage1 = make([]byte, int(pageSize))
			}
			copy(committedPage1, pendingPage1)
		}
	}
	if committedPage1 == nil {
		return nil, errors.New("zero-byte database WAL has no committed page 1")
	}
	if string(committedPage1[:16]) != "SQLite format 3\x00" {
		return nil, errors.New("zero-byte database WAL page 1 is not a valid SQLite header")
	}
	dbPageSize := uint32(binary.BigEndian.Uint16(committedPage1[16:18]))
	if dbPageSize == 1 {
		dbPageSize = 65536
	}
	if dbPageSize != pageSize {
		return nil, fmt.Errorf("WAL page size %d does not match page-1 database page size %d", pageSize, dbPageSize)
	}
	return committedPage1, nil
}

func replayZeroByteSnapshotWAL(ctx context.Context, snapshotDBPath string) error {
	page1, err := extractCommittedPage1FromWAL(snapshotDBPath + "-wal")
	if err != nil {
		return err
	}
	if err := os.Remove(snapshotDBPath + "-shm"); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(snapshotDBPath, page1, 0o600); err != nil {
		return err
	}
	rawDB, err := sql.Open("sqlite", snapshotDBPath)
	if err != nil {
		return err
	}
	rawDB.SetMaxOpenConns(1)
	_, checkpointErr := rawDB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	closeErr := rawDB.Close()
	if checkpointErr != nil {
		return checkpointErr
	}
	if closeErr != nil {
		return closeErr
	}
	info, err := os.Lstat(snapshotDBPath)
	if err != nil {
		return err
	}
	if info.Size() < 100 {
		return errors.New("zero-byte database WAL produced no replayed database pages")
	}
	return nil
}

// inspectSnapshotSourceFile is the snapshot's use of the one shared source-file
// policy: the database and every sidecar the copy reads are audited the same way
// by every reader of a preserved source.
func inspectSnapshotSourceFile(path string) (os.FileInfo, error) {
	return processlock.InspectSourceFile(path)
}

func digestDatabaseFiles(ctx context.Context, databasePath string) (map[string]databaseFileDigest, error) {
	digests := make(map[string]databaseFileDigest, len(databaseSidecars)+1)
	for _, suffix := range databaseSidecars {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := databasePath + suffix
		info, err := inspectSnapshotSourceFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		digest, err := digestInspectedFile(ctx, path, info)
		if err != nil {
			return nil, err
		}
		digests[suffix] = databaseFileDigest{size: info.Size(), digest: digest}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	journalPath := databasePath + "-journal"
	if info, err := inspectSnapshotSourceFile(journalPath); err == nil {
		if info.Size() > 0 {
			return nil, errPendingRollbackJournal
		}
		digest, digestErr := digestInspectedFile(ctx, journalPath, info)
		if digestErr != nil {
			return nil, digestErr
		}
		digests["-journal"] = databaseFileDigest{size: info.Size(), digest: digest}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if _, ok := digests[""]; !ok {
		return nil, errDatabaseMissing
	}
	return digests, nil
}

func digestInspectedFile(ctx context.Context, path string, inspected os.FileInfo) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	if err := processlock.OpenedDatabase(file, inspected); err != nil {
		return "", err
	}
	digest := sha256.New()
	if _, err := copyChunks(ctx, digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// copyDatabaseFiles writes the database and its sidecars into destination and
// returns the digest of every byte it copied.
func copyDatabaseFiles(ctx context.Context, databasePath string, destination string) (map[string]databaseFileDigest, error) {
	copied := make(map[string]databaseFileDigest, len(databaseSidecars))
	for _, suffix := range databaseSidecars {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sourcePath := databasePath + suffix
		info, err := inspectSnapshotSourceFile(sourcePath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		digest, size, err := copyFileWithDigest(ctx, sourcePath, info, filepath.Join(destination, filepath.Base(sourcePath)))
		if err != nil {
			return nil, snapshotCopyFailure(err)
		}
		copied[suffix] = databaseFileDigest{size: size, digest: digest}
	}
	if _, ok := copied[""]; !ok {
		return nil, errDatabaseMissing
	}
	return copied, nil
}

// snapshotCopyFailure labels a failed copy as a snapshot failure without
// destroying the reason. Cancellation keeps its identity: stringifying it would
// turn an interrupted run into a suspicious source, and the caller could no
// longer tell the two apart. Every other cause stays a short, path-free
// description for the report.
func snapshotCopyFailure(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(ErrSnapshotFailed, err)
	}
	return fmt.Errorf("%w: %s", ErrSnapshotFailed, rootCause(err))
}

func copyFileWithDigest(ctx context.Context, sourcePath string, inspected os.FileInfo, destinationPath string) (string, int64, error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = source.Close() }()
	if err := processlock.OpenedDatabase(source, inspected); err != nil {
		return "", 0, err
	}
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	digest := sha256.New()
	written, copyErr := copyChunks(ctx, io.MultiWriter(destination, digest), source)
	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil {
		return "", 0, errors.Join(copyErr, closeErr)
	}
	return hex.EncodeToString(digest.Sum(nil)), written, nil
}

func sameDigests(left, right map[string]databaseFileDigest) bool {
	if len(left) != len(right) {
		return false
	}
	for suffix, expected := range left {
		actual, ok := right[suffix]
		if !ok || actual != expected {
			return false
		}
	}
	return true
}

func sameCopiedDigests(source, copied map[string]databaseFileDigest) bool {
	wantCount := 0
	for suffix, expected := range source {
		if suffix == "-journal" {
			continue
		}
		wantCount++
		actual, ok := copied[suffix]
		if !ok || actual != expected {
			return false
		}
	}
	return len(copied) == wantCount
}
