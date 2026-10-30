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
	// ErrSourceChanged reports a source that was modified while its snapshot was
	// taken. The copy is discarded and the report blocks.
	ErrSourceChanged = errors.New("source changed while it was being snapshotted")
)

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
	before, err := digestDatabaseFiles(databasePath)
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
		var after map[string]databaseFileDigest
		if after, err = digestDatabaseFiles(databasePath); err == nil {
			if !sameDigests(before, after) || !sameDigests(before, copied) {
				err = ErrSourceChanged
			}
		}
	}
	if err != nil {
		_ = snapshot.Close()
		return nil, err
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

// ValidateScratchDir refuses a scratch directory that lies inside a root this
// operation must not write to.
//
// The snapshot copies the whole database, so a scratch directory under the
// legacy source root would write that copy back into the tree preflight promises
// to preserve, and one under the target root would leave upgrade scratch in the
// volume the copy is measured against. Both are refused before a directory is
// created. An empty scratch directory is allowed: it means the OS temporary
// directory, which the caller never shares with either root.
func ValidateScratchDir(scratchDir string, legacyRoot string, targetDir string) error {
	scratch := strings.TrimSpace(scratchDir)
	if scratch == "" {
		return nil
	}
	scratchPath := canonicalPath(scratch)
	for _, root := range []struct {
		label string
		dir   string
	}{
		{label: "the legacy source root", dir: legacyRoot},
		{label: "the target root", dir: targetDir},
	} {
		dir := strings.TrimSpace(root.dir)
		if dir == "" {
			continue
		}
		if pathWithin(scratchPath, canonicalPath(dir)) {
			return fmt.Errorf("%w: scratch directory is inside %s; pass a --scratch-dir on a separate volume",
				ErrSnapshotFailed, root.label)
		}
	}
	return nil
}

// pathWithin reports whether child is parent itself or lies underneath it. Both
// paths must already be canonical.
func pathWithin(child string, parent string) bool {
	if child == parent {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
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

// digestDatabaseFiles hashes the database and every sidecar that exists. A
// missing database is reported as the missing-source condition; missing
// sidecars are normal for a cleanly stopped writer.
func digestDatabaseFiles(databasePath string) (map[string]databaseFileDigest, error) {
	digests := make(map[string]databaseFileDigest, len(databaseSidecars))
	for _, suffix := range databaseSidecars {
		path := databasePath + suffix
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("database file %s is not a regular file", filepath.Base(path))
		}
		digest, err := fileContentDigest(path)
		if err != nil {
			return nil, err
		}
		digests[suffix] = databaseFileDigest{size: info.Size(), digest: digest}
	}
	if _, ok := digests[""]; !ok {
		return nil, errDatabaseMissing
	}
	return digests, nil
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
		info, err := os.Lstat(sourcePath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("database file %s is not a regular file", filepath.Base(sourcePath))
		}
		digest, size, err := copyFileWithDigest(sourcePath, filepath.Join(destination, filepath.Base(sourcePath)))
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrSnapshotFailed, rootCause(err))
		}
		copied[suffix] = databaseFileDigest{size: size, digest: digest}
	}
	if _, ok := copied[""]; !ok {
		return nil, errDatabaseMissing
	}
	return copied, nil
}

func copyFileWithDigest(sourcePath string, destinationPath string) (string, int64, error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = source.Close() }()
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(destination, digest), source)
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
