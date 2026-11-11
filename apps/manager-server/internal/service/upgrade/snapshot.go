package upgrade

import (
	"context"
	"crypto/sha256"
	"database/sql"
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
	dir  string
	path string
	db   *sql.DB
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
		if err := managersqlite.ValidateWALHeader(snapshot.path); err != nil {
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
	return snapshot, nil
}

// ReadRecordFromSnapshot reads the recorded operation row from a private copy of
// the Manager database. It exists for the resume guard: a target a copy-in has
// not finished must never be opened in place, because that open creates or
// rewrites the WAL index the resume compares by file identity. A missing
// database is "no record", not a failure.
func ReadRecordFromSnapshot(ctx context.Context, databasePath string, scratchBase string) (model.UpgradeOperation, bool, error) {
	if _, err := os.Lstat(databasePath); err != nil {
		if os.IsNotExist(err) {
			if sideErr := copyinmarker.DatabaseSidecarsAbsent(databasePath); sideErr != nil {
				return model.UpgradeOperation{}, false, sideErr
			}
			return model.UpgradeOperation{}, false, nil
		}
		return model.UpgradeOperation{}, false, err
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
	return validateScratchRoots(scratchDir, []ScratchRoot{
		{Label: "the legacy source root", Dir: legacyRoot},
		{Label: "the target root", Dir: targetDir},
	})
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
	scratchPath := canonicalPath(scratch)
	for _, root := range roots {
		dir := strings.TrimSpace(root.Dir)
		if dir == "" {
			continue
		}
		if pathWithin(scratchPath, canonicalExclusionRoot(dir)) {
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
func canonicalPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	current := absolute
	missing := []string{}
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(append([]string{resolved}, missing...)...)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return absolute
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = parent
	}
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
func AuditSourceSidecars(databasePath string) error {
	for _, suffix := range auditedSidecarSuffixes {
		info, err := inspectSnapshotSourceFile(databasePath + suffix)
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
