package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

// materializingCredentialTarget copies a real 1.x source with a live WAL into a
// fresh target root and marks the copy as an unfinished copy-in. The target
// members are the state the resumed move compares with its backup: a read that
// opened the store in place would rewrite its WAL index, so any caller that
// authenticates it must stage a private copy somewhere else.
func materializingCredentialTarget(t *testing.T, key string) (string, string) {
	t.Helper()
	fixture := newCredentialFixture(t, key)
	writer, err := sql.Open("sqlite", fixture.dbPath)
	if err != nil {
		t.Fatalf("open the copied source: %v", err)
	}
	writer.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = writer.Close() })
	// Rows committed in the WAL and a writer kept open: the copied target holds
	// a real WAL and a real WAL index rather than a checkpointed database.
	execFixture(t, writer, `pragma wal_autocheckpoint = 0`)
	execFixture(t, writer, `create table snapshot_root_padding (payload blob)`)
	execFixture(t, writer, `insert into snapshot_root_padding values (zeroblob(4194304))`)
	if info, err := os.Lstat(fixture.dbPath + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("the copied source has no WAL: info=%v err=%v", info, err)
	}

	targetRoot := t.TempDir()
	target := filepath.Join(targetRoot, databaseFileName)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(fixture.dbPath + suffix)
		if err != nil {
			t.Fatalf("read the restored member %q: %v", suffix, err)
		}
		if err := os.WriteFile(target+suffix, data, 0o600); err != nil {
			t.Fatalf("write the restored member %q: %v", suffix, err)
		}
	}
	if err := copyinmarker.Write(targetRoot, copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseMaterializing,
		TargetDatabase:          target,
		SourceKind:              "upgraded-legacy",
		SourceFingerprintBefore: "fingerprint-before",
		SourceFingerprintAfter:  "fingerprint-after",
		BackupManifestSHA256:    "manifest",
	}); err != nil {
		t.Fatalf("publish the materializing marker: %v", err)
	}
	return targetRoot, target
}

// snapshotDirsIn lists the private copy directories this package would leave
// under a directory. A read removes its copy on every path, so a lingering entry
// is evidence of an abandoned copy; the witness that a refused call staged
// nothing at all is the typed refusal itself, which is returned before the
// directory is created.
func snapshotDirsIn(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), snapshotDirPrefix) {
			names = append(names, entry.Name())
		}
	}
	return names
}

// TestVerifyManagerCredentialRejectsScratchInsideEitherSnapshotRoot is the two
// roots' contract at the service: a materializing Manager target is authenticated
// from a private copy, and the operating system's temporary directory is refused
// before anything is staged when it points inside the target's own data root or
// the configured 1.x source root.
func TestVerifyManagerCredentialRejectsScratchInsideEitherSnapshotRoot(t *testing.T) {
	for _, inside := range []string{"target", "source"} {
		t.Run(inside, func(t *testing.T) {
			const key = "snapshot-root-credential"
			targetRoot, target := materializingCredentialTarget(t, key)
			sourceRoot := t.TempDir()
			scratchRoot := sourceRoot
			if inside == "target" {
				scratchRoot = targetRoot
			}
			scratch := filepath.Join(scratchRoot, "private-tmp")
			if err := os.Mkdir(scratch, 0o700); err != nil {
				t.Fatalf("create the in-root scratch: %v", err)
			}
			beforeTarget := sourceEntries(t, targetRoot)
			beforeSource := sourceEntries(t, sourceRoot)
			t.Setenv("TMPDIR", scratch)

			err := VerifyManagerCredential(context.Background(), target, CredentialSnapshotRoots{
				Target:      targetRoot,
				Source:      sourceRoot,
				SourceKnown: true,
			}, key)
			if !errors.Is(err, ErrSnapshotFailed) {
				t.Fatalf("TMPDIR inside the %s root = %v, want %s", inside, err, ErrSnapshotFailed)
			}
			if names := snapshotDirsIn(t, scratch); len(names) != 0 {
				t.Fatalf("the refused read staged %v in the %s root", names, inside)
			}
			requireUnchangedSource(t, beforeTarget, sourceEntries(t, targetRoot))
			requireUnchangedSource(t, beforeSource, sourceEntries(t, sourceRoot))
		})
	}
}

// TestVerifyManagerCredentialRefusesSnapshotWithoutAResolvedSourceRoot is the
// fail-closed half of the two roots' contract. A caller that could not resolve
// the configured source root — a mount lookup that outlived its bound — cannot
// promise the copy stays out of it, so the materializing read refuses instead of
// staging a copy in the operating system's temporary directory and hoping.
func TestVerifyManagerCredentialRefusesSnapshotWithoutAResolvedSourceRoot(t *testing.T) {
	const key = "unresolved-root-credential"
	targetRoot, target := materializingCredentialTarget(t, key)
	scratch := t.TempDir()
	before := sourceEntries(t, targetRoot)
	t.Setenv("TMPDIR", scratch)

	// Target is named; the configured source root is not, and the zero value's
	// SourceKnown=false is exactly that state.
	err := VerifyManagerCredential(context.Background(), target, CredentialSnapshotRoots{Target: targetRoot}, key)
	if !errors.Is(err, ErrSnapshotRootUnknown) {
		t.Fatalf("unresolved source root = %v, want %s", err, ErrSnapshotRootUnknown)
	}
	if names := snapshotDirsIn(t, scratch); len(names) != 0 {
		t.Fatalf("the refused read staged %v in the temporary directory", names)
	}
	requireUnchangedSource(t, before, sourceEntries(t, targetRoot))
}

// TestVerifyManagerCredentialRefusesSnapshotUnderTheFilesystemSourceRoot is the
// filesystem-root boundary of the same rule. "/" is the canonical parent of every
// absolute path, and every scratch directory — the operating system's temporary
// directory included — therefore lies inside a source root configured as "/".
// The containment check used to build its prefix by appending a separator, which
// turned the root into "//" and reported every descendant as outside it; the
// whole database copy then landed inside the tree the operation promises to
// preserve. The refusal must happen before anything is staged.
func TestVerifyManagerCredentialRefusesSnapshotUnderTheFilesystemSourceRoot(t *testing.T) {
	const key = "filesystem-root-credential"
	targetRoot, target := materializingCredentialTarget(t, key)
	scratch := t.TempDir()
	before := sourceEntries(t, targetRoot)
	t.Setenv("TMPDIR", scratch)

	err := VerifyManagerCredential(context.Background(), target, CredentialSnapshotRoots{
		Target:      targetRoot,
		Source:      string(filepath.Separator),
		SourceKnown: true,
	}, key)
	if !errors.Is(err, ErrSnapshotFailed) {
		t.Fatalf("source root %q = %v, want %s", string(filepath.Separator), err, ErrSnapshotFailed)
	}
	if names := snapshotDirsIn(t, scratch); len(names) != 0 {
		t.Fatalf("the refused read staged %v beneath the filesystem source root", names)
	}
	requireUnchangedSource(t, before, sourceEntries(t, targetRoot))
}

// TestVerifyManagerCredentialFailsClosedWithoutResolvingATimedOutSourceRoot is
// the no-repeat-filesystem-call half of the mount-timeout contract. When the
// lookup that would have inspected the configured root outlived its bound, the
// root was published for the exclusion check but never inspected. A private copy
// must not canonicalize it: that EvalSymlinks is exactly the call the bound
// exists to stop, and on the mount that hung it would never return. The read
// fails closed with the typed deadline instead, and the test's substitute for the
// exclusion resolver counts the calls to prove none were made. The same read
// with an answered root still resolves the root and proves the key, so the count
// is not vacuously zero.
func TestVerifyManagerCredentialFailsClosedWithoutResolvingATimedOutSourceRoot(t *testing.T) {
	const key = "timed-out-root-credential"
	targetRoot, target := materializingCredentialTarget(t, key)
	scratch := t.TempDir()
	before := sourceEntries(t, targetRoot)
	t.Setenv("TMPDIR", scratch)

	original := canonicalExclusionRoot
	t.Cleanup(func() { canonicalExclusionRoot = original })
	var resolutions int32
	canonicalExclusionRoot = func(path string) string {
		atomic.AddInt32(&resolutions, 1)
		return original(path)
	}

	timedOutRoot := t.TempDir()
	err := VerifyManagerCredential(context.Background(), target, CredentialSnapshotRoots{
		Target:         targetRoot,
		Source:         timedOutRoot,
		SourceKnown:    true,
		SourceTimedOut: true,
	}, key)
	if !errors.Is(err, ErrSnapshotRootTimedOut) {
		t.Fatalf("timed-out source root = %v, want %s", err, ErrSnapshotRootTimedOut)
	}
	if got := atomic.LoadInt32(&resolutions); got != 0 {
		t.Fatalf("the timed-out source root was resolved %d times for the exclusion check", got)
	}
	if names := snapshotDirsIn(t, scratch); len(names) != 0 {
		t.Fatalf("the refused read staged %v in the temporary directory", names)
	}
	requireUnchangedSource(t, before, sourceEntries(t, targetRoot))

	// The answered root takes the normal path: the exclusion resolver runs and
	// the key is proved from a private copy.
	if err := VerifyManagerCredential(context.Background(), target, CredentialSnapshotRoots{
		Target:      targetRoot,
		Source:      timedOutRoot,
		SourceKnown: true,
	}, key); err != nil {
		t.Fatalf("answered source root = %v, want a proved credential", err)
	}
	if got := atomic.LoadInt32(&resolutions); got == 0 {
		t.Fatal("the exclusion resolver was never called for an answered source root")
	}
	if names := snapshotDirsIn(t, scratch); len(names) != 0 {
		t.Fatalf("the proved read left %v in the temporary directory", names)
	}
	requireUnchangedSource(t, before, sourceEntries(t, targetRoot))
}
