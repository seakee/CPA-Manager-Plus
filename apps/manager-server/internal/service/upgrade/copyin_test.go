package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/maintenancebundle"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
	_ "modernc.org/sqlite"
)

// legacyVolume is a stopped 1.x data directory. The database carries the
// carried-forward usage schema with rows and none of the v2-only tables, which
// is what the incremental migration has to add.
type legacyVolume struct {
	root string
}

func (v legacyVolume) databasePath() string { return filepath.Join(v.root, databaseFileName) }

func (v legacyVolume) archives() []string {
	return []string{
		filepath.Join(v.root, archivesDirName, "2026-03", "segment-000001.jsonl"),
		filepath.Join(v.root, archivesDirName, "2026-03", "segment-000002.jsonl"),
	}
}

// canonicalTempDir returns a temporary directory with every symlink resolved:
// maintenancebundle refuses any bundle path below a symbolic link.
func canonicalTempDir(t *testing.T) string {
	t.Helper()
	return canonicalPath(t.TempDir())
}

func newLegacyVolume(t *testing.T) legacyVolume {
	t.Helper()
	root := canonicalTempDir(t)
	volume := legacyVolume{root: root}
	databasePath := volume.databasePath()
	db, err := managersqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("create 1.x fixture database: %v", err)
	}
	for _, statement := range []string{
		`insert into usage_events (event_hash, timestamp_ms, timestamp, model, requested_model, created_at_ms)
			values ('legacy-event-1', 1000, '1000', 'gpt-test', 'gpt-test', 1000)`,
		`insert into usage_events (event_hash, timestamp_ms, timestamp, model, requested_model, created_at_ms)
			values ('legacy-event-2', 2000, '2000', 'gpt-test', 'gpt-test', 2000)`,
		`insert into settings (key, value, updated_at_ms) values ('legacy_fixture_marker', 'kept', 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("seed 1.x fixture: %v", err)
		}
	}
	// A 1.x volume has none of the v2-only tables. Dropping them makes the
	// fixture a source the incremental migration must upgrade, and keeps the
	// preflight classification on the 1.x side.
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		t.Fatalf("open fixture connection: %v", err)
	}
	// Foreign keys between the v2-only tables would otherwise order the drops.
	if _, err := conn.ExecContext(context.Background(), `pragma foreign_keys = off`); err != nil {
		_ = conn.Close()
		_ = db.Close()
		t.Fatalf("disable fixture foreign keys: %v", err)
	}
	for _, pattern := range []string{`gateway\_%`, `fresh\_%`} {
		names, err := fixtureTableNames(conn, pattern)
		if err != nil {
			_ = conn.Close()
			_ = db.Close()
			t.Fatalf("list v2-only fixture tables: %v", err)
		}
		for _, name := range names {
			if _, err := conn.ExecContext(context.Background(), `drop table `+name); err != nil {
				_ = conn.Close()
				_ = db.Close()
				t.Fatalf("drop v2-only fixture table %s: %v", name, err)
			}
		}
	}
	if _, err := conn.ExecContext(context.Background(), `drop table if exists upgrade_operations`); err != nil {
		_ = conn.Close()
		_ = db.Close()
		t.Fatalf("drop fixture upgrade table: %v", err)
	}
	if err := conn.Close(); err != nil {
		_ = db.Close()
		t.Fatalf("close fixture connection: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close 1.x fixture database: %v", err)
	}
	if _, _, err := security.LoadOrCreateDataKey("", filepath.Join(root, dataKeyFileName)); err != nil {
		t.Fatalf("create 1.x fixture data key: %v", err)
	}
	for index, path := range volume.archives() {
		writeFixtureTree(t, path, fmt.Sprintf("{\"segment\":%d}\n", index+1))
	}
	if err := os.WriteFile(databasePath+".manager.lock", nil, 0o600); err != nil {
		t.Fatalf("create 1.x fixture ownership marker: %v", err)
	}
	return volume
}

type fixtureQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func fixtureTableNames(db fixtureQueryer, pattern string) ([]string, error) {
	rows, err := db.QueryContext(context.Background(),
		`select name from sqlite_master where type = 'table' and name like ? escape '\'`, pattern)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func writeFixtureTree(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
}

func (v legacyVolume) lock(t *testing.T) *processlock.Lock {
	t.Helper()
	lock, err := processlock.AcquireExisting(v.databasePath())
	if err != nil {
		t.Fatalf("acquire 1.x ownership lock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return lock
}

func copyInOptions(volume legacyVolume, targetRoot string) Options {
	return Options{
		LegacyRoot:      volume.root,
		TargetRoot:      targetRoot,
		BundleDir:       DefaultBundleDirectory(targetRoot),
		DeclaredVersion: pinnedSourceVersion(),
	}
}

// fingerprintTree is a full recursive description of a directory: every entry,
// its size, its content digest and its mode. Two equal fingerprints mean the
// tree was not written, renamed or extended.
func fingerprintTree(t *testing.T, root string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entries[filepath.ToSlash(relative)] = fmt.Sprintf("link %s %s", target, info.Mode())
			return nil
		}
		if entry.IsDir() {
			entries[filepath.ToSlash(relative)] = "dir " + info.Mode().String()
			return nil
		}
		digest, err := fileContentDigest(context.Background(), path)
		if err != nil {
			return err
		}
		entries[filepath.ToSlash(relative)] = fmt.Sprintf("file %d %s %s", info.Size(), digest, info.Mode())
		return nil
	})
	if err != nil {
		t.Fatalf("fingerprint %s: %v", root, err)
	}
	return entries
}

func assertTreeUnchanged(t *testing.T, label string, before, after map[string]string) {
	t.Helper()
	if reflect.DeepEqual(before, after) {
		return
	}
	for name, description := range before {
		if after[name] != description {
			t.Fatalf("%s: %s changed from %q to %q", label, name, description, after[name])
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Fatalf("%s: %s appeared", label, name)
		}
	}
	t.Fatalf("%s tree changed", label)
}

func TestCopyInRoundTripPreservesBytesAndRecordsTheOperation(t *testing.T) {
	volume := newLegacyVolume(t)
	sourceBefore := fingerprintTree(t, volume.root)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	sourceDatabaseDigest, err := fileContentDigest(context.Background(), volume.databasePath())
	if err != nil {
		t.Fatalf("digest source database: %v", err)
	}
	sourceKeyDigest, err := fileContentDigest(context.Background(), filepath.Join(volume.root, dataKeyFileName))
	if err != nil {
		t.Fatalf("digest source data key: %v", err)
	}

	result, err := CopyIn(context.Background(), func() Options {
		options.Lock = volume.lock(t)
		return options
	}())
	if err != nil {
		t.Fatalf("copy in: %v", err)
	}

	if result.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("stage = %q, want %q", result.Stage, model.UpgradeStageCopyIn)
	}
	if result.SourceKind != SourceKindUpgradedLegacy {
		t.Fatalf("source kind = %q, want %q", result.SourceKind, SourceKindUpgradedLegacy)
	}
	if !slicesContain(result.DomainsDone, model.UpgradeDomainSourceCopy) {
		t.Fatalf("domains = %v, want %s", result.DomainsDone, model.UpgradeDomainSourceCopy)
	}
	if result.SourceFingerprintBefore == "" || result.SourceFingerprintBefore != result.SourceFingerprintAfter {
		t.Fatalf("fingerprints before/after = %q/%q, want one non-empty value",
			result.SourceFingerprintBefore, result.SourceFingerprintAfter)
	}
	if !result.Restored || result.AlreadyComplete {
		t.Fatalf("restored/alreadyComplete = %t/%t, want true/false", result.Restored, result.AlreadyComplete)
	}

	// The backup records exactly the source bytes and names the manifest.
	manifest, err := maintenancebundle.Verify(context.Background(), result.BackupDirectory)
	if err != nil {
		t.Fatalf("verify backup: %v", err)
	}
	if got := manifest.Entries["database"].SHA256; got != sourceDatabaseDigest {
		t.Fatalf("backup database digest = %q, want %q", got, sourceDatabaseDigest)
	}
	if got := manifest.Entries["data-key"].SHA256; got != sourceKeyDigest {
		t.Fatalf("backup data key digest = %q, want %q", got, sourceKeyDigest)
	}
	manifestFileDigest, err := fileContentDigest(context.Background(), filepath.Join(result.BackupDirectory, "manifest.json"))
	if err != nil {
		t.Fatalf("digest backup manifest: %v", err)
	}
	if result.BackupManifestSHA256 != manifestFileDigest {
		t.Fatalf("recorded manifest digest = %q, want %q", result.BackupManifestSHA256, manifestFileDigest)
	}

	// The v2 layout holds the same bytes for everything that is not migrated.
	if got, err := fileContentDigest(context.Background(), result.TargetDataKey); err != nil || got != sourceKeyDigest {
		t.Fatalf("copied data key digest = %q (%v), want %q", got, err, sourceKeyDigest)
	}
	for _, archived := range volume.archives() {
		relative, err := filepath.Rel(volume.root, archived)
		if err != nil {
			t.Fatalf("resolve archive path: %v", err)
		}
		copied := filepath.Join(targetRoot, relative)
		sourceDigest, err := fileContentDigest(context.Background(), archived)
		if err != nil {
			t.Fatalf("digest source archive: %v", err)
		}
		if got, err := fileContentDigest(context.Background(), copied); err != nil || got != sourceDigest {
			t.Fatalf("copied archive %s digest = %q (%v), want %q", relative, got, err, sourceDigest)
		}
	}

	// The copied database is migrated into a v2 store and keeps every source row.
	recorded, found, err := ReadRecord(context.Background(), result.TargetDatabase)
	if err != nil || !found {
		t.Fatalf("read recorded operation: found=%t err=%v", found, err)
	}
	if recorded.Stage != model.UpgradeStageCopyIn || recorded.SourceKind != SourceKindUpgradedLegacy {
		t.Fatalf("recorded stage/kind = %q/%q", recorded.Stage, recorded.SourceKind)
	}
	if recorded.SourceFingerprintBefore != result.SourceFingerprintBefore ||
		recorded.SourceFingerprintAfter != result.SourceFingerprintAfter ||
		recorded.BackupManifestSHA256 != result.BackupManifestSHA256 {
		t.Fatalf("recorded row = %+v, want the reported fingerprints and manifest", recorded)
	}
	if !recorded.HasDomain(model.UpgradeDomainSourceCopy) {
		t.Fatalf("recorded domains = %v, want %s", recorded.DomainsDone, model.UpgradeDomainSourceCopy)
	}
	assertMigratedStore(t, result.TargetDatabase)

	// The 1.x volume is byte-identical, including the absence of WAL, SHM and a
	// new ownership marker.
	assertTreeUnchanged(t, "1.x source", sourceBefore, fingerprintTree(t, volume.root))
	for _, sidecar := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(volume.databasePath() + sidecar); !os.IsNotExist(err) {
			t.Fatalf("1.x source gained %s (err=%v)", sidecar, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(targetRoot, copyInWorkDirectoryName)); !os.IsNotExist(err) {
		t.Fatalf("copy-in work directory was not removed (err=%v)", err)
	}
}

func slicesContain(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func assertMigratedStore(t *testing.T, databasePath string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open copied store: %v", err)
	}
	defer func() { _ = db.Close() }()
	for _, table := range []string{"gateway_api_key_identities", "upgrade_operations"} {
		var present int
		if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, table).Scan(&present); err != nil {
			t.Fatalf("inspect migrated table %s: %v", table, err)
		}
		if present != 1 {
			t.Fatalf("migrated store is missing %s", table)
		}
	}
	var events, marker int
	if err := db.QueryRow(`select count(*) from usage_events`).Scan(&events); err != nil {
		t.Fatalf("count migrated usage events: %v", err)
	}
	if events != 2 {
		t.Fatalf("migrated usage events = %d, want 2", events)
	}
	if err := db.QueryRow(`select count(*) from settings where key = 'legacy_fixture_marker'`).Scan(&marker); err != nil {
		t.Fatalf("read migrated settings marker: %v", err)
	}
	if marker != 1 {
		t.Fatalf("migrated settings marker = %d, want 1", marker)
	}
}

func TestCopyInDoesNotRestoreWhenTheCopyInIsRecorded(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	if _, err := CopyIn(context.Background(), options); err != nil {
		t.Fatalf("first copy in: %v", err)
	}
	targetAfterFirst := fingerprintTree(t, targetRoot)

	phases := []string{}
	options.checkpoint = func(phase string) error {
		phases = append(phases, phase)
		return nil
	}
	result, err := CopyIn(context.Background(), options)
	if err != nil {
		t.Fatalf("second copy in: %v", err)
	}
	if !result.AlreadyComplete {
		t.Fatalf("second copy in = %+v, want already complete", result)
	}
	if result.Restored {
		t.Fatal("second copy in restored bytes again")
	}
	if len(phases) != 0 {
		t.Fatalf("second copy in reached phases %v, want none", phases)
	}
	if result.Stage != model.UpgradeStageCopyIn || result.BackupManifestSHA256 == "" {
		t.Fatalf("second copy in reported %+v, want the recorded stage and manifest", result)
	}
	assertTreeUnchanged(t, "v2 data root", targetAfterFirst, fingerprintTree(t, targetRoot))
}

func TestCopyInResumesAfterTheBackupVerificationIsInterrupted(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	injected := errors.New("injected interruption after the backup was verified")
	options.checkpoint = func(phase string) error {
		if phase == "backup-verified" {
			return injected
		}
		return nil
	}
	if _, err := CopyIn(context.Background(), options); !errors.Is(err, injected) {
		t.Fatalf("interrupted copy in = %v, want the injected error", err)
	}
	if _, err := maintenancebundle.Verify(context.Background(), options.BundleDir); err != nil {
		t.Fatalf("an interrupted run must leave a verified backup behind: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(targetRoot, databaseFileName)); !os.IsNotExist(err) {
		t.Fatalf("an interrupted run wrote the target database (err=%v)", err)
	}

	options.checkpoint = nil
	result, err := CopyIn(context.Background(), options)
	if err != nil {
		t.Fatalf("resumed copy in: %v", err)
	}
	if result.AlreadyComplete || !result.Restored || result.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("resumed copy in = %+v, want a fresh completed copy", result)
	}
	assertMigratedStore(t, result.TargetDatabase)
}

func TestCopyInResumesAfterARestoreWithoutRestoringTwice(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	injected := errors.New("injected interruption after the restore")
	options.checkpoint = func(phase string) error {
		if phase == "target-restored" {
			return injected
		}
		return nil
	}
	if _, err := CopyIn(context.Background(), options); !errors.Is(err, injected) {
		t.Fatalf("interrupted copy in = %v, want the injected error", err)
	}
	// Only staging holds the legacy DB before the atomic commit.
	staged := filepath.Join(targetRoot, copyInWorkDirectoryName, copyInStagingDirectory, "database")
	sourceDatabaseDigest, err := fileContentDigest(context.Background(), volume.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	if digest, err := fileContentDigest(context.Background(), staged); err != nil || digest != sourceDatabaseDigest {
		t.Fatalf("staged database digest = %q/%v", digest, err)
	}
	if _, err := os.Lstat(filepath.Join(targetRoot, databaseFileName)); !os.IsNotExist(err) {
		t.Fatalf("uncommitted target database exists: %v", err)
	}

	observed := map[string]string{}
	options.checkpoint = func(phase string) error {
		if phase == "target-restored" {
			digest, err := fileContentDigest(context.Background(), staged)
			if err != nil {
				return err
			}
			observed[phase] = digest
		}
		return nil
	}
	result, err := CopyIn(context.Background(), options)
	if err != nil {
		t.Fatalf("resumed copy in: %v", err)
	}
	if result.Restored {
		t.Fatal("resumed copy in restored bytes again")
	}
	if observed["target-restored"] != sourceDatabaseDigest {
		t.Fatalf("resumed copy in reached migration with database %q, want the restored 1.x bytes %q",
			observed["target-restored"], sourceDatabaseDigest)
	}
	assertMigratedStore(t, result.TargetDatabase)
	completedTree := fingerprintTree(t, targetRoot)

	// A third run sees the committed record and changes nothing at all.
	options.checkpoint = nil
	again, err := CopyIn(context.Background(), options)
	if err != nil {
		t.Fatalf("third copy in: %v", err)
	}
	if !again.AlreadyComplete {
		t.Fatalf("third copy in = %+v, want already complete", again)
	}
	assertTreeUnchanged(t, "v2 data root", completedTree, fingerprintTree(t, targetRoot))
}

// An interrupted migration changes only disposable staging, never the target.
func TestCopyInResumesAfterAnInterruptionInsideTheMigration(t *testing.T) {
	volume := newLegacyVolume(t)
	startLiveWalSource(t, volume)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	interruptCopyIn(t, options, "target-restored")
	staged := filepath.Join(newTargetPaths(targetRoot).Staging, "database")
	db, err := sql.Open("sqlite", staged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`pragma journal_mode=wal; create table migration_probe (probe text); insert into migration_probe values ('tampered'); update usage_events set model='foreign'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(targetRoot, databaseFileName)); !os.IsNotExist(err) {
		t.Fatalf("migration published a target: %v", err)
	}
	result, err := CopyIn(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	assertMigratedStore(t, result.TargetDatabase)
	db, err = sql.Open("sqlite", readOnlyDataSourceName(result.TargetDatabase, true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`select count(*) from sqlite_master where name='migration_probe'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("staged tampering survived: %d/%v", count, err)
	}
	if err := db.QueryRow(`select count(*) from usage_events where model='foreign'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("foreign events survived: %d/%v", count, err)
	}
	assertNoCopyInWork(t, targetRoot)
}

func interruptCopyIn(t *testing.T, options Options, checkpoint string) {
	t.Helper()
	interruption := errors.New("copy-in interruption")
	options.checkpoint = func(phase string) error {
		if phase == checkpoint {
			return interruption
		}
		return nil
	}
	if _, err := CopyIn(context.Background(), options); !errors.Is(err, interruption) {
		t.Fatalf("interrupt at %s = %v", checkpoint, err)
	}
}

func assertNoCopyInWork(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Lstat(newTargetPaths(root).Work); !os.IsNotExist(err) {
		t.Fatalf("copy-in work survives: %v", err)
	}
}

// TestCopyInRefusesAStrayDatabaseInTheV2Root proves the resume path cannot be
// widened into a license to overwrite a v2 data root this copy-in never wrote:
// without its marker the materialized target is not recognized, and the move
// still refuses to replace a database it did not place.
func TestCopyInRefusesAStrayDatabaseInTheV2Root(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	strayPath := filepath.Join(targetRoot, databaseFileName)
	stray, err := sql.Open("sqlite", strayPath)
	if err != nil {
		t.Fatalf("open the stray store: %v", err)
	}
	if _, err := stray.Exec(`create table foreign_probe (probe text); insert into foreign_probe (probe) values ('kept')`); err != nil {
		_ = stray.Close()
		t.Fatalf("seed the stray store: %v", err)
	}
	if err := stray.Close(); err != nil {
		t.Fatalf("close the stray store: %v", err)
	}
	strayDigest, err := fileContentDigest(context.Background(), strayPath)
	if err != nil {
		t.Fatalf("digest the stray store: %v", err)
	}

	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetConflict) {
		t.Fatalf("copy in over a stray database = %v, want %s", err, ErrTargetConflict)
	}
	if after, err := fileContentDigest(context.Background(), strayPath); err != nil || after != strayDigest {
		t.Fatalf("refused copy in rewrote the stray database (digest %q, err=%v)", after, err)
	}
	if _, found, err := ReadRecord(context.Background(), strayPath); err != nil || found {
		t.Fatalf("refused copy in recorded an operation: found=%t err=%v", found, err)
	}
}

// TestCopyInRefusesToResumeOverAMemberItDidNotWrite pins the other side of the
// same edge: a marker only vouches for the members this copy-in moved. When a
// member the migration never touches no longer matches the backup, resuming must
// refuse instead of overwriting the tree it found, and the marker — the only
// evidence that the store is unfinished — must survive the refusal.
func TestCopyInRefusesToResumeOverAMemberItDidNotWrite(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	injected := errors.New("injected interruption after the restore")
	options.checkpoint = func(phase string) error {
		if phase == "target-restored" {
			return injected
		}
		return nil
	}
	if _, err := CopyIn(context.Background(), options); !errors.Is(err, injected) {
		t.Fatalf("interrupted copy in = %v, want the injected error", err)
	}
	options.checkpoint = nil

	markerBefore, found, err := copyinmarker.Read(targetRoot)
	if err != nil || !found {
		t.Fatalf("the interrupted run left no copy-in marker: found=%t err=%v", found, err)
	}
	// The archive tree is not rewritten by the migration, so bytes that differ
	// from the backup were written by someone else after this copy-in moved it.
	foreign := []byte("{\"segment\":\"someone-else\"}\n")
	replaced := filepath.Join(targetRoot, archivesDirName, "2026-03", "segment-000001.jsonl")
	if err := os.WriteFile(replaced, foreign, 0o600); err != nil {
		t.Fatalf("replace a copied archive segment: %v", err)
	}
	targetBefore := fingerprintTree(t, targetRoot)

	if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetConflict) {
		t.Fatalf("resumed copy in over a replaced member = %v, want %s", err, ErrTargetConflict)
	}
	if after, err := os.ReadFile(replaced); err != nil || string(after) != string(foreign) {
		t.Fatalf("refused resume rewrote the replaced archive segment (%q, err=%v)", after, err)
	}
	if !reflect.DeepEqual(targetBefore, fingerprintTree(t, targetRoot)) {
		t.Fatal("refused resume changed the v2 data root it refused to resume")
	}
	if markerAfter, found, err := copyinmarker.Read(targetRoot); err != nil || !found || markerAfter != markerBefore {
		t.Fatalf("refused resume changed the copy-in marker: found=%t err=%v", found, err)
	}
}

func TestCopyInRefusesAnExistingBackupThatDescribesAnotherSource(t *testing.T) {
	first := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(first, targetRoot)
	options.Lock = first.lock(t)
	if _, err := CopyIn(context.Background(), options); err != nil {
		t.Fatalf("first copy in: %v", err)
	}

	second := newLegacyVolume(t)
	otherTarget := canonicalTempDir(t)
	otherOptions := Options{
		LegacyRoot: second.root,
		TargetRoot: otherTarget,
		BundleDir:  options.BundleDir,
		Lock:       second.lock(t),
	}
	if _, err := CopyIn(context.Background(), otherOptions); err == nil {
		t.Fatal("copy in reused a backup of a different source")
	}
	if _, err := os.Lstat(filepath.Join(otherTarget, databaseFileName)); !os.IsNotExist(err) {
		t.Fatalf("refused copy in wrote the target database (err=%v)", err)
	}
}

func TestCopyInRequiresTheSourceFenceAndDisjointRoots(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)

	missingLockOptions := copyInOptions(volume, targetRoot)
	if _, err := CopyIn(context.Background(), missingLockOptions); err == nil {
		t.Fatal("copy in ran without the 1.x ownership lock")
	}

	insideSource := copyInOptions(volume, filepath.Join(volume.root, "v2"))
	insideSource.Lock = volume.lock(t)
	if _, err := CopyIn(context.Background(), insideSource); err == nil {
		t.Fatal("copy in accepted a v2 data root inside the 1.x source root")
	}

	foreignLockOptions := copyInOptions(volume, targetRoot)
	other := newLegacyVolume(t)
	foreignLockOptions.Lock = other.lock(t)
	if _, err := CopyIn(context.Background(), foreignLockOptions); err == nil {
		t.Fatal("copy in accepted a lock for another database")
	}
}

// A completed migration without its row is disposable staging, not a target
// whose provenance needs to be inferred.
func TestCopyInResumesWhenTheMigrationCommittedWithoutTheRecord(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	interruptCopyIn(t, options, "target-restored")
	staged := filepath.Join(newTargetPaths(targetRoot).Staging, "database")
	db, err := managersqlite.Open(staged)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := ReadRecord(context.Background(), staged); err != nil || found {
		t.Fatalf("staged record = %t/%v", found, err)
	}
	result, err := CopyIn(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.AlreadyComplete || result.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("resumed result = %+v", result)
	}
	assertMigratedStore(t, result.TargetDatabase)
	assertNoCopyInWork(t, targetRoot)
}

// TestCopyInRefusesToReportACompletedCopyInWhoseTargetChanged is the regression
// for the false success: `alreadyComplete` used to trust the row alone, so a
// modified data key or an archive tree that gained entries was reported as a
// finished copy-in.
func TestCopyInRefusesToReportACompletedCopyInWhoseTargetChanged(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(t *testing.T, targetRoot string)
	}{
		{
			name: "data key digest",
			change: func(t *testing.T, targetRoot string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(targetRoot, dataKeyFileName), []byte("replaced-key\n"), 0o600); err != nil {
					t.Fatalf("replace the data key: %v", err)
				}
			},
		},
		{
			name: "extra archive entry",
			change: func(t *testing.T, targetRoot string) {
				t.Helper()
				writeFixtureTree(t, filepath.Join(targetRoot, archivesDirName, "2026-05", "segment-000009.jsonl"), "{}\n")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			volume := newLegacyVolume(t)
			targetRoot := canonicalTempDir(t)
			options := copyInOptions(volume, targetRoot)
			options.Lock = volume.lock(t)
			if _, err := CopyIn(context.Background(), options); err != nil {
				t.Fatalf("copy in: %v", err)
			}
			test.change(t, targetRoot)
			changed := fingerprintTree(t, targetRoot)

			if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetModified) {
				t.Fatalf("copy in over a modified target = %v, want %s", err, ErrTargetModified)
			} else if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorTargetModified {
				t.Fatalf("copy in error code = %q/%t, want %s", code, ok, UpgradeErrorTargetModified)
			}
			if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetModified) {
				t.Fatalf("repeated copy in over a modified target = %v, want %s", err, ErrTargetModified)
			}
			// The refusal neither repaired nor replaced anything.
			assertTreeUnchanged(t, "v2 data root", changed, fingerprintTree(t, targetRoot))
		})
	}
}

// TestCopyInRollsBackWhenTheSourceChangesMidRun is the regression for the abort
// path: a source that changes while the copy runs must leave neither a
// materialized target nor a staged copy, and must report a typed failure.
func TestCopyInRollsBackWhenTheSourceChangesMidRun(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	options.checkpoint = func(phase string) error {
		if phase != "target-restored" {
			return nil
		}
		// Another writer touches the source while the copy is in flight.
		return os.WriteFile(volume.archives()[0], []byte("{\"segment\":\"changed\"}\n"), 0o600)
	}

	if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("copy in with a changing source = %v, want %s", err, ErrSourceChanged)
	} else if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorSourceChanged {
		t.Fatalf("copy in error code = %q/%t, want %s", code, ok, UpgradeErrorSourceChanged)
	}
	for _, name := range []string{databaseFileName, dataKeyFileName, archivesDirName, copyInWorkDirectoryName} {
		if _, err := os.Lstat(filepath.Join(targetRoot, name)); !os.IsNotExist(err) {
			t.Fatalf("aborted copy in left %s in the v2 data root (err=%v)", name, err)
		}
	}
	// The backup stays: it is the evidence of the refused attempt, and the next
	// run reuses it instead of backing up the changed source again.
	if _, err := maintenancebundle.Verify(context.Background(), options.BundleDir); err != nil {
		t.Fatalf("aborted copy in lost its verified backup: %v", err)
	}
}

// TestCopyInRefusesExtraArchiveEntriesNextToAMatchingTarget is the regression
// for the target match: a target that already holds the backup bytes is not
// restored again, but it must not silently adopt content the backup does not
// describe.
func TestCopyInRefusesExtraArchiveEntriesNextToAMatchingTarget(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	injected := errors.New("injected interruption after the restore")
	options.checkpoint = func(phase string) error {
		if phase == "target-restored" {
			return injected
		}
		return nil
	}
	if _, err := CopyIn(context.Background(), options); !errors.Is(err, injected) {
		t.Fatalf("interrupted copy in = %v, want the injected error", err)
	}
	// The target holds the backup bytes and no record; another entry appears in
	// the archive tree before the resume.
	writeFixtureTree(t, filepath.Join(targetRoot, archivesDirName, "2026-05", "segment-000009.jsonl"), "{}\n")
	options.checkpoint = nil
	before := fingerprintTree(t, targetRoot)

	if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetModified) {
		t.Fatalf("copy in next to an extra archive entry = %v, want %s", err, ErrTargetModified)
	}
	// The refusal wrote nothing: the tree is exactly as the extra entry left it.
	assertTreeUnchanged(t, "v2 data root", before, fingerprintTree(t, targetRoot))
	if _, err := os.Lstat(filepath.Join(targetRoot, databaseFileName)); !os.IsNotExist(err) {
		t.Fatalf("refusal published a target: %v", err)
	}
}

// Even a published staging tree is disposable: restore the verified bundle,
// rather than trusting staging's manifest or accepting tampered bytes.
func TestCopyInRestoresAPublishedStagingTreeFromTheVerifiedBundle(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	interruptCopyIn(t, options, "before-rename")
	staged := filepath.Join(newTargetPaths(targetRoot).Staging, "database")
	if err := os.WriteFile(staged, []byte("tampered staged database"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.WriteFile(staged+suffix, []byte("foreign sidecar"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := CopyIn(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	assertMigratedStore(t, result.TargetDatabase)
	assertNoCopyInWork(t, targetRoot)
}

// startLiveWalSource leaves the 1.x fixture in the state a killed 1.x writer
// leaves behind: the database is in WAL mode, its -wal holds frames that were
// never checkpointed, and the -shm index is on disk. The writer handle stays open
// so SQLite cannot checkpoint those frames away, and the volume is still
// stopped from the upgrade's point of view: nothing writes to it.
func startLiveWalSource(t *testing.T, volume legacyVolume) {
	t.Helper()
	db, err := sql.Open("sqlite", volume.databasePath())
	if err != nil {
		t.Fatalf("open the 1.x fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`pragma journal_mode = wal`,
		`pragma wal_autocheckpoint = 0`,
		`insert into settings (key, value, updated_at_ms) values ('wal_frame_probe', '1', 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("prepare a live source WAL: %v", err)
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if info, err := os.Lstat(volume.databasePath() + suffix); err != nil || info.Size() == 0 {
			t.Fatalf("the live source holds no %s (err=%v)", suffix, err)
		}
	}
}

// databaseDigests is the byte identity of one database and the sidecars that
// exist beside it.
func databaseDigests(t *testing.T, root string) map[string]string {
	t.Helper()
	digests := map[string]string{}
	for _, name := range []string{databaseFileName, databaseFileName + "-wal", databaseFileName + "-shm"} {
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		}
		digest, err := fileContentDigest(context.Background(), path)
		if err != nil {
			t.Fatalf("digest %s: %v", name, err)
		}
		digests[name] = digest
	}
	return digests
}

// A live source WAL is restored afresh in staging and never moved to the target.
func TestCopyInResumeDoesNotRewriteARestoredLiveWALSidecar(t *testing.T) {
	volume := newLegacyVolume(t)
	startLiveWalSource(t, volume)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	interruptCopyIn(t, options, "target-restored")
	before := fingerprintTree(t, options.BundleDir)
	options.checkpoint = func(phase string) error {
		if phase == "target-restored" {
			if len(databaseDigests(t, targetRoot)) != 0 {
				t.Fatal("database members moved before commit")
			}
			for _, suffix := range []string{"", "-wal", "-shm"} {
				want, err := fileContentDigest(context.Background(), filepath.Join(options.BundleDir, "database"+suffix))
				if err != nil {
					return err
				}
				got, err := fileContentDigest(context.Background(), filepath.Join(newTargetPaths(targetRoot).Staging, "database"+suffix))
				if err != nil || got != want {
					t.Fatalf("restored %s = %s/%v, want %s", suffix, got, err, want)
				}
			}
		}
		return nil
	}
	result, err := CopyIn(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	assertMigratedStore(t, result.TargetDatabase)
	assertTreeUnchanged(t, "verified bundle", before, fingerprintTree(t, options.BundleDir))
	assertNoCopyInWork(t, targetRoot)
}

// TestCopyInRefusesToResumeAMarkerThatDoesNotVouchForTheTarget is the H1
// regression. The marker used to be read as a hint: a marker that did not
// describe the current target was ignored, the work directory was cleared
// (taking the marker with it), the restore ran again and the move then refused
// to replace the members already in place. The result was a migrated store with
// no row and no marker — a full writable surface for an unfinished upgrade.
// A marker that cannot vouch for the target has to fail closed instead, and the
// marker has to survive the refusal.
func TestCopyInRefusesToResumeAMarkerThatDoesNotVouchForTheTarget(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(t *testing.T, targetRoot string)
	}{
		{
			name: "data key replaced",
			change: func(t *testing.T, targetRoot string) {
				t.Helper()
				writeFixtureTree(t, filepath.Join(targetRoot, dataKeyFileName), "someone-else-key\n")
			},
		},
		{
			name: "archive segment replaced",
			change: func(t *testing.T, targetRoot string) {
				t.Helper()
				writeFixtureTree(t, filepath.Join(targetRoot, archivesDirName, "2026-03", "segment-000002.jsonl"), "{}\n")
			},
		},
		{
			name: "archive segment removed",
			change: func(t *testing.T, targetRoot string) {
				t.Helper()
				if err := os.Remove(filepath.Join(targetRoot, archivesDirName, "2026-03", "segment-000002.jsonl")); err != nil {
					t.Fatalf("remove a copied archive segment: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			volume := newLegacyVolume(t)
			targetRoot := canonicalTempDir(t)
			options := copyInOptions(volume, targetRoot)
			options.Lock = volume.lock(t)
			injected := errors.New("injected interruption after the restore")
			options.checkpoint = func(phase string) error {
				if phase == "before-rename" {
					return injected
				}
				return nil
			}
			if _, err := CopyIn(context.Background(), options); !errors.Is(err, injected) {
				t.Fatalf("interrupted copy in = %v, want the injected error", err)
			}
			options.checkpoint = nil
			markerBefore, found, err := copyinmarker.Read(targetRoot)
			if err != nil || !found {
				t.Fatalf("the interrupted run left no marker: found=%t err=%v", found, err)
			}

			test.change(t, targetRoot)
			targetBefore := fingerprintTree(t, targetRoot)
			if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetConflict) {
				t.Fatalf("resumed copy in over a changed member = %v, want %s", err, ErrTargetConflict)
			} else if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorTargetConflict {
				t.Fatalf("resume error code = %q/%t, want %s", code, ok, UpgradeErrorTargetConflict)
			}
			if !reflect.DeepEqual(targetBefore, fingerprintTree(t, targetRoot)) {
				t.Fatal("the refused resume changed the v2 data root")
			}
			markerAfter, found, err := copyinmarker.Read(targetRoot)
			if err != nil || !found || markerAfter != markerBefore {
				t.Fatalf("the refused resume changed the marker: found=%t err=%v", found, err)
			}

			// The startup gate must still see an unfinished upgrade: without the
			// marker this store reads as a native-v2 install.
			stage, err := (legacypreflight.DatabaseUpgradeAdmission{
				DatabasePath: filepath.Join(targetRoot, databaseFileName),
			}).Admitted(context.Background())
			if err != nil || stage != legacypreflight.UpgradeStageInProgress {
				t.Fatalf("admission after the refusal = %q/%v, want %q", stage, err, legacypreflight.UpgradeStageInProgress)
			}
		})
	}
}

// Target admission is independent of inode allocation and content heuristics.
// These foreign stores preserve settings and event count/max id while changing
// existing rows, or inserting a replacement event. WAL edits leave the main DB unchanged.
func TestCopyInRefusesAForeignDatabaseInTheCrashWindow(t *testing.T) {
	for _, wal := range []bool{false, true} {
		for _, edit := range []bool{false, true} {
			t.Run(fmt.Sprintf("wal=%t/edit=%t", wal, edit), func(t *testing.T) {
				volume := newLegacyVolume(t)
				targetRoot := canonicalTempDir(t)
				options := copyInOptions(volume, targetRoot)
				options.Lock = volume.lock(t)
				interruptCopyIn(t, options, "before-rename")
				databasePath := filepath.Join(targetRoot, databaseFileName)
				bytes, err := os.ReadFile(filepath.Join(options.BundleDir, "database"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(databasePath, bytes, 0o600); err != nil {
					t.Fatal(err)
				}
				foreign, err := sql.Open("sqlite", databasePath)
				if err != nil {
					t.Fatal(err)
				}
				defer foreign.Close()
				if wal {
					if _, err := foreign.Exec(`pragma journal_mode=wal; pragma wal_checkpoint(truncate); pragma wal_autocheckpoint=0`); err != nil {
						t.Fatal(err)
					}
				}
				mainBefore, err := fileContentDigest(context.Background(), databasePath)
				if err != nil {
					t.Fatal(err)
				}
				var countBefore, maxBefore int
				if err := foreign.QueryRow(`select count(*), coalesce(max(id),0) from usage_events`).Scan(&countBefore, &maxBefore); err != nil {
					t.Fatal(err)
				}
				statement := `delete from usage_events where id=1; insert into usage_events (id, event_hash, timestamp_ms, timestamp, model, created_at_ms) values (1, 'foreign-event', 99999, '99999', 'foreign', 99999)`
				if edit {
					statement = `update usage_events set model='foreign'`
				}
				if _, err := foreign.Exec(statement); err != nil {
					t.Fatal(err)
				}
				var countAfter, maxAfter int
				if err := foreign.QueryRow(`select count(*), coalesce(max(id),0) from usage_events`).Scan(&countAfter, &maxAfter); err != nil || countAfter != countBefore || maxAfter != maxBefore {
					t.Fatalf("event shape changed: %d/%d -> %d/%d: %v", countBefore, maxBefore, countAfter, maxAfter, err)
				}
				if wal {
					mainAfter, err := fileContentDigest(context.Background(), databasePath)
					if err != nil || mainBefore != mainAfter {
						t.Fatalf("foreign WAL changed main DB: %s -> %s/%v", mainBefore, mainAfter, err)
					}
				}
				if !wal {
					if err := foreign.Close(); err != nil {
						t.Fatal(err)
					}
				}
				before := fingerprintTree(t, targetRoot)
				if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetConflict) {
					t.Fatalf("resume adopted foreign target: %v", err)
				}
				assertTreeUnchanged(t, "foreign target and marker", before, fingerprintTree(t, targetRoot))
				if _, found, err := ReadRecord(context.Background(), databasePath); err != nil || found {
					t.Fatalf("foreign provenance = %t/%v", found, err)
				}
			})
		}
	}
}

// TestCopyInCleansUpASurvivingMarkerOnceTheRecordIsCommitted is the L1
// regression: a process death between the operation-row commit and the removal of
// the work directory left a marker that the AlreadyComplete rerun never cleaned
// up. The rerun recognizes its own recorded outcome and removes the evidence.
func TestCopyInCleansUpASurvivingMarkerOnceTheRecordIsCommitted(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	injected := errors.New("injected interruption after the record committed")
	options.checkpoint = func(phase string) error {
		if phase == "recorded" {
			return injected
		}
		return nil
	}
	if _, err := CopyIn(context.Background(), options); !errors.Is(err, injected) {
		t.Fatalf("interrupted copy in = %v, want the injected error", err)
	}

	databasePath := filepath.Join(targetRoot, databaseFileName)
	beforeRead := fingerprintTree(t, targetRoot)
	recorded, found, err := ReadRecordFromSnapshot(context.Background(), databasePath, "")
	if err != nil || !found || !copyInRecorded(recorded) {
		t.Fatalf("the interrupted run did not commit the record: found=%t err=%v", found, err)
	}
	if _, found, err := copyinmarker.Read(targetRoot); err != nil || !found {
		t.Fatalf("the interrupted run left no marker to clean: found=%t err=%v", found, err)
	}
	admission := legacypreflight.DatabaseUpgradeAdmission{DatabasePath: databasePath}
	if stage, err := admission.Admitted(context.Background()); err != nil || stage != legacypreflight.UpgradeStageInProgress {
		t.Fatalf("published copy_in admission = %q/%v", stage, err)
	}
	assertTreeUnchanged(t, "committed row observations", beforeRead, fingerprintTree(t, targetRoot))
	migrated, err := os.Lstat(databasePath)
	if err != nil {
		t.Fatalf("inspect the committed database: %v", err)
	}

	options.checkpoint = nil
	result, err := CopyIn(context.Background(), options)
	if err != nil {
		t.Fatalf("rerun over a committed record = %v", err)
	}
	if !result.AlreadyComplete || result.Restored {
		t.Fatalf("rerun = %+v, want an already complete copy-in", result)
	}
	if result.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("rerun stage = %q, want %q", result.Stage, model.UpgradeStageCopyIn)
	}
	if _, err := os.Lstat(filepath.Join(targetRoot, copyInWorkDirectoryName)); !os.IsNotExist(err) {
		t.Fatalf("the rerun left the marker behind (err=%v)", err)
	}
	// The committed store was adopted, not replaced.
	current, err := os.Lstat(databasePath)
	if err != nil {
		t.Fatalf("inspect the adopted database: %v", err)
	}
	if !os.SameFile(migrated, current) {
		t.Fatal("the rerun replaced the committed database instead of adopting it")
	}
}

// Admission never opens the disposable staging database or creates a target.
func TestCopyInAdmissionBeforeResumeKeepsTheLiveWALTargetResumable(t *testing.T) {
	for _, checkpoint := range []string{"target-restored", "before-rename"} {
		t.Run(checkpoint, func(t *testing.T) {
			volume := newLegacyVolume(t)
			startLiveWalSource(t, volume)
			targetRoot := canonicalTempDir(t)
			options := copyInOptions(volume, targetRoot)
			options.Lock = volume.lock(t)
			interruptCopyIn(t, options, checkpoint)
			before := fingerprintTree(t, targetRoot)
			admission := legacypreflight.DatabaseUpgradeAdmission{DatabasePath: filepath.Join(targetRoot, databaseFileName)}
			stage, err := admission.Admitted(context.Background())
			if err != nil || stage != legacypreflight.UpgradeStageInProgress {
				t.Fatalf("admission = %q/%v", stage, err)
			}
			if stage, _, err := admission.AdmittedReason(context.Background()); err != nil || stage != legacypreflight.UpgradeStageInProgress {
				t.Fatalf("reason admission = %q/%v", stage, err)
			}
			assertTreeUnchanged(t, "admission target", before, fingerprintTree(t, targetRoot))
			result, err := CopyIn(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			assertMigratedStore(t, result.TargetDatabase)
		})
	}
}

// TestCopyInRefusesAHeldV2DataRoot is the shared-target regression. The copy-in
// used to take only the 1.x source fence, so it created, restored, migrated and
// recorded inside a v2 data root that another Manager process owned; the real
// CLI did exactly that under a held target lock. The service now takes the
// target's own process lock before its first write, so two writers cannot share
// the root and the refusal is typed, leaves the root untouched and reports exit
// 1 with a code instead of a generic failure.
func TestCopyInRefusesAHeldV2DataRoot(t *testing.T) {
	targetRoot := canonicalTempDir(t)
	owner, err := processlock.Acquire(filepath.Join(targetRoot, databaseFileName))
	if err != nil {
		t.Fatalf("take the v2 data root fence: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })

	first := newLegacyVolume(t)
	second := newLegacyVolume(t)
	if reflect.DeepEqual(fingerprintTree(t, first.root), fingerprintTree(t, second.root)) {
		t.Fatal("the two sources are the same volume, so the shared-target case proves nothing")
	}
	firstLock := first.lock(t)
	secondLock := second.lock(t)
	for _, attempt := range []struct {
		volume legacyVolume
		lock   *processlock.Lock
	}{{first, firstLock}, {second, secondLock}} {
		options := copyInOptions(attempt.volume, targetRoot)
		options.Lock = attempt.lock
		before := fingerprintTree(t, targetRoot)
		_, err := CopyIn(context.Background(), options)
		if !errors.Is(err, ErrV2Running) {
			t.Fatalf("copy-in into a held v2 data root = %v, want %s", err, ErrV2Running)
		}
		if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorV2Running {
			t.Fatalf("copy-in error code = %q/%t, want %s", code, ok, UpgradeErrorV2Running)
		}
		assertTreeUnchanged(t, "held v2 data root", before, fingerprintTree(t, targetRoot))
		for _, absent := range []string{databaseFileName, DefaultBundleDirectory(targetRoot)} {
			if _, statErr := os.Lstat(filepath.Join(targetRoot, absent)); !os.IsNotExist(statErr) {
				t.Fatalf("a refused copy-in wrote %s (err=%v)", absent, statErr)
			}
		}
	}

	// The same run succeeds once the other owner releases the fence: the refusal
	// is the lock, not a copy-in that can never write this root.
	options := copyInOptions(second, targetRoot)
	options.Lock = secondLock
	if err := owner.Close(); err != nil {
		t.Fatalf("release the v2 data root fence: %v", err)
	}
	if _, err := CopyIn(context.Background(), options); err != nil {
		t.Fatalf("copy-in after the fence was released = %v", err)
	}
}

// TestCopyInReportsTheDurableTimestamp is the result-timestamp regression. The
// repository owns updated_at_ms and discards the caller's proposal, so building
// the result from the local row after Put reported updatedAtMs=0 on the first
// run while the durable row held the write time. The result is now the persisted
// row, on a first run and on a rerun that still carries its marker.
func TestCopyInReportsTheDurableTimestamp(t *testing.T) {
	for _, test := range []struct {
		name      string
		interrupt func(options Options, err error) Options
		wantRerun bool
	}{
		{name: "first run"},
		{
			name: "rerun after a surviving marker",
			interrupt: func(options Options, interrupted error) Options {
				options.checkpoint = func(phase string) error {
					if phase == "recorded" {
						return interrupted
					}
					return nil
				}
				return options
			},
			wantRerun: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			volume := newLegacyVolume(t)
			targetRoot := canonicalTempDir(t)
			options := copyInOptions(volume, targetRoot)
			options.Lock = volume.lock(t)
			if test.interrupt != nil {
				interrupted := errors.New("injected interruption after the record committed")
				options = test.interrupt(options, interrupted)
				if _, err := CopyIn(context.Background(), options); !errors.Is(err, interrupted) {
					t.Fatalf("interrupted copy in = %v, want the injected error", err)
				}
				options.checkpoint = nil
			}
			result, err := CopyIn(context.Background(), options)
			if err != nil {
				t.Fatalf("copy in: %v", err)
			}
			if test.wantRerun && !result.AlreadyComplete {
				t.Fatalf("rerun = %+v, want it to clean up the surviving marker", result)
			}
			recorded, found, err := ReadRecord(context.Background(), result.TargetDatabase)
			if err != nil || !found {
				t.Fatalf("read the persisted operation: found=%t err=%v", found, err)
			}
			if recorded.UpdatedAtMS == 0 {
				t.Fatal("the persisted row holds no updated_at_ms")
			}
			if result.UpdatedAtMS != recorded.UpdatedAtMS {
				t.Fatalf("result updatedAtMs = %d, want the durable row's %d", result.UpdatedAtMS, recorded.UpdatedAtMS)
			}
		})
	}
}

func TestCopyInRefusesOldAndUnknownMarkerContracts(t *testing.T) {
	for _, test := range []struct{ name, old, replacement string }{
		{"old version", `"version":3`, `"version":2`},
		{"unknown phase", `"phase":"committing"`, `"phase":"migrating"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			volume := newLegacyVolume(t)
			targetRoot := canonicalTempDir(t)
			options := copyInOptions(volume, targetRoot)
			options.Lock = volume.lock(t)
			interruptCopyIn(t, options, "before-rename")
			path := copyinmarker.Path(targetRoot)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := strings.Replace(string(data), test.old, test.replacement, 1)
			if changed == string(data) {
				t.Fatal("marker mutation did not apply")
			}
			if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
				t.Fatal(err)
			}
			before := fingerprintTree(t, targetRoot)
			if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetConflict) || !strings.Contains(err.Error(), "empty v2 data root") {
				t.Fatalf("unsupported marker = %v, want typed recovery", err)
			}
			assertTreeUnchanged(t, "unsupported marker", before, fingerprintTree(t, targetRoot))
		})
	}
}

func TestCopyInRefusesTargetSidecarsBeforeTheRename(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			volume := newLegacyVolume(t)
			targetRoot := canonicalTempDir(t)
			options := copyInOptions(volume, targetRoot)
			options.Lock = volume.lock(t)
			interruptCopyIn(t, options, "before-rename")
			path := filepath.Join(targetRoot, databaseFileName+suffix)
			if err := os.WriteFile(path, []byte("foreign sidecar"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := fingerprintTree(t, targetRoot)
			if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetConflict) {
				t.Fatalf("foreign sidecar resume = %v", err)
			}
			assertTreeUnchanged(t, "foreign sidecar", before, fingerprintTree(t, targetRoot))
		})
	}
}

func TestCopyInRechecksTheTargetImmediatelyBeforeTheRename(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	var before map[string]string
	options.checkpoint = func(phase string) error {
		if phase == "before-rename" {
			if err := os.WriteFile(filepath.Join(targetRoot, databaseFileName), []byte("foreign"), 0o600); err != nil {
				return err
			}
			before = fingerprintTree(t, targetRoot)
		}
		return nil
	}
	if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetConflict) {
		t.Fatalf("publication over an arriving target = %v", err)
	}
	assertTreeUnchanged(t, "pre-rename target", before, fingerprintTree(t, targetRoot))
}

func TestCopyInRefusesAnUnmarkedForeignWALWithoutTouchingItsMembers(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	databasePath := filepath.Join(targetRoot, databaseFileName)
	foreign, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if _, err := foreign.Exec(`pragma journal_mode=wal; pragma wal_autocheckpoint=0; create table foreign_store (value text); insert into foreign_store values ('kept')`); err != nil {
		t.Fatal(err)
	}
	before := databaseDigests(t, targetRoot)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetConflict) {
		t.Fatalf("foreign WAL copy-in = %v", err)
	}
	if after := databaseDigests(t, targetRoot); !reflect.DeepEqual(before, after) {
		t.Fatalf("foreign database members changed: %v -> %v", before, after)
	}
}

func TestCopyInRestoresMissingNonDatabaseMembersBeforeTheCommit(t *testing.T) {
	for _, member := range []string{dataKeyFileName, archivesDirName} {
		t.Run(member, func(t *testing.T) {
			volume := newLegacyVolume(t)
			targetRoot := canonicalTempDir(t)
			options := copyInOptions(volume, targetRoot)
			options.Lock = volume.lock(t)
			interruptCopyIn(t, options, "before-rename")
			if err := os.RemoveAll(filepath.Join(targetRoot, member)); err != nil {
				t.Fatal(err)
			}
			result, err := CopyIn(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Restored {
				t.Fatal("resume did not report moving the missing member")
			}
			assertMigratedStore(t, result.TargetDatabase)
			assertNoCopyInWork(t, targetRoot)
		})
	}
}

func TestCopyInRefusesStagedDatabaseSymlinks(t *testing.T) {
	for _, phase := range []string{"target-restored", "before-rename"} {
		t.Run(phase, func(t *testing.T) {
			volume := newLegacyVolume(t)
			target := canonicalTempDir(t)
			options := copyInOptions(volume, target)
			options.Lock = volume.lock(t)
			sourceBefore := fingerprintTree(t, volume.root)
			staged := filepath.Join(newTargetPaths(target).Staging, "database")
			options.checkpoint = func(current string) error {
				if current != phase {
					return nil
				}
				if err := os.Remove(staged); err != nil {
					return err
				}
				return os.Symlink(volume.databasePath(), staged)
			}
			if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetConflict) {
				t.Errorf("staged source symlink = %v, want typed conflict", err)
			}
			assertTreeUnchanged(t, "protected source", sourceBefore, fingerprintTree(t, volume.root))
			if _, err := os.Lstat(filepath.Join(target, databaseFileName)); !os.IsNotExist(err) {
				t.Fatalf("symlink published as target: %v", err)
			}
			if _, found, err := copyinmarker.Read(target); err != nil || !found {
				t.Fatalf("refusal lost marker: %t/%v", found, err)
			}
		})
	}
}

func TestCopyInRefusesSymlinkedWorkDirectories(t *testing.T) {
	for _, name := range []string{copyInWorkDirectoryName, filepath.Join(copyInWorkDirectoryName, copyInStagingDirectory), filepath.Join(copyInWorkDirectoryName, "untrusted-entry")} {
		t.Run(filepath.Base(name), func(t *testing.T) {
			volume := newLegacyVolume(t)
			target := canonicalTempDir(t)
			unrelatedRoot := canonicalTempDir(t)
			writeFixtureTree(t, filepath.Join(unrelatedRoot, "unrelated-user-data"), "must survive\n")
			before := fingerprintTree(t, unrelatedRoot)
			link := filepath.Join(target, name)
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(unrelatedRoot, link); err != nil {
				t.Fatal(err)
			}
			options := copyInOptions(volume, target)
			options.Lock = volume.lock(t)
			if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetConflict) {
				t.Errorf("symlinked work path = %v, want typed conflict", err)
			}
			assertTreeUnchanged(t, "unrelated directory", before, fingerprintTree(t, unrelatedRoot))
			if got, err := os.Readlink(link); err != nil || got != unrelatedRoot {
				t.Fatalf("refusal changed symlink: %q/%v", got, err)
			}
		})
	}
}

func TestCopyInRefusesSidecarsBesideTheCommittedMarker(t *testing.T) {
	for _, sidecar := range []string{"-wal", "-shm", "-journal", "journal-symlink"} {
		t.Run(sidecar, func(t *testing.T) {
			volume := newLegacyVolume(t)
			target := canonicalTempDir(t)
			options := copyInOptions(volume, target)
			options.Lock = volume.lock(t)
			interruptCopyIn(t, options, "recorded")
			database := filepath.Join(target, databaseFileName)
			if sidecar == "-wal" {
				donorPath := filepath.Join(canonicalTempDir(t), databaseFileName)
				data, err := os.ReadFile(database)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(donorPath, data, 0o600); err != nil {
					t.Fatal(err)
				}
				donor, err := sql.Open("sqlite", donorPath)
				if err != nil {
					t.Fatal(err)
				}
				defer donor.Close()
				if _, err := donor.Exec(`pragma journal_mode=wal; pragma wal_checkpoint(truncate); pragma wal_autocheckpoint=0; update usage_events set model='foreign-committed-wal'`); err != nil {
					t.Fatal(err)
				}
				wal, err := os.ReadFile(donorPath + "-wal")
				if err != nil || len(wal) == 0 {
					t.Fatalf("donor WAL: %v", err)
				}
				if err := os.WriteFile(database+"-wal", wal, 0o600); err != nil {
					t.Fatal(err)
				}
				snapshot, err := OpenSnapshot(context.Background(), database, "")
				if err != nil {
					t.Fatal(err)
				}
				var count int
				queryErr := snapshot.DB().QueryRow(`select count(*) from usage_events where model='foreign-committed-wal'`).Scan(&count)
				closeErr := snapshot.Close()
				if queryErr != nil || closeErr != nil || count != 2 {
					t.Fatalf("foreign WAL is not logically active: %d/%v/%v", count, queryErr, closeErr)
				}
			} else if sidecar == "journal-symlink" {
				referencedFile := filepath.Join(canonicalTempDir(t), "journal-reference")
				writeFixtureTree(t, referencedFile, "must not be followed")
				if err := os.Symlink(referencedFile, database+"-journal"); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(database+sidecar, []byte("foreign sidecar"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := fingerprintTree(t, target)
			admission := legacypreflight.DatabaseUpgradeAdmission{DatabasePath: database}
			if stage, err := admission.Admitted(context.Background()); err != nil || stage != legacypreflight.UpgradeStageInProgress {
				t.Fatalf("pre-resume admission = %q/%v", stage, err)
			}
			if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetConflict) {
				t.Errorf("committed target sidecar = %v, want typed conflict", err)
			}
			assertTreeUnchanged(t, "committed target and marker", before, fingerprintTree(t, target))
			if stage, err := admission.Admitted(context.Background()); err != nil || stage != legacypreflight.UpgradeStageInProgress {
				t.Fatalf("post-resume admission = %q/%v", stage, err)
			}
		})
	}
}

func TestCopyInRefusesSidecarsArrivingAfterTheRenameCheck(t *testing.T) {
	for _, phase := range []string{"rename-checked", "recorded"} {
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			t.Run(phase+suffix, func(t *testing.T) {
				volume := newLegacyVolume(t)
				target := canonicalTempDir(t)
				options := copyInOptions(volume, target)
				options.Lock = volume.lock(t)
				planted := false
				path := filepath.Join(target, databaseFileName+suffix)
				options.checkpoint = func(current string) error {
					if current != phase {
						return nil
					}
					planted = true
					return os.WriteFile(path, []byte("late foreign sidecar"), 0o600)
				}
				if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetConflict) {
					t.Errorf("late sidecar = %v, want typed conflict", err)
				}
				if !planted {
					t.Fatal("publication checkpoint was not exercised")
				}
				if data, err := os.ReadFile(path); err != nil || string(data) != "late foreign sidecar" {
					t.Fatalf("late sidecar changed: %q/%v", data, err)
				}
				if _, found, err := copyinmarker.Read(target); err != nil || !found {
					t.Fatalf("late conflict lost marker: %t/%v", found, err)
				}
				stage, err := (legacypreflight.DatabaseUpgradeAdmission{DatabasePath: filepath.Join(target, databaseFileName)}).Admitted(context.Background())
				if err != nil || stage != legacypreflight.UpgradeStageInProgress {
					t.Fatalf("late-sidecar admission = %q/%v", stage, err)
				}
			})
		}
	}
}

func TestCopyInCompletedRecoveryUsesOnePrivateSnapshotOutsideBothRoots(t *testing.T) {
	volume := newLegacyVolume(t)
	target := canonicalTempDir(t)
	options := copyInOptions(volume, target)
	options.Lock = volume.lock(t)
	interruptCopyIn(t, options, "recorded")
	options.ScratchDir = canonicalTempDir(t)
	sourceBefore := fingerprintTree(t, volume.root)
	databaseBefore := databaseDigests(t, target)
	observed := false
	options.checkpoint = func(phase string) error {
		if phase != "completed-verified" {
			return nil
		}
		observed = true
		entries, err := os.ReadDir(options.ScratchDir)
		if err != nil || len(entries) != 1 || !entries[0].IsDir() {
			t.Fatalf("completed recovery snapshots = %v/%v, want one private directory", entries, err)
		}
		privateRoot := filepath.Join(options.ScratchDir, entries[0].Name())
		if within(volume.root, privateRoot) || within(target, privateRoot) {
			t.Fatalf("snapshot is inside a preserved root: %s", privateRoot)
		}
		return nil
	}
	result, err := CopyIn(context.Background(), options)
	if err != nil || !result.AlreadyComplete {
		t.Fatalf("completed recovery = %+v/%v", result, err)
	}
	if !observed {
		t.Fatal("completed recovery did not verify through an active private snapshot")
	}
	if entries, err := os.ReadDir(options.ScratchDir); err != nil || len(entries) != 0 {
		t.Fatalf("completed recovery left private scratch: %v/%v", entries, err)
	}
	if after := databaseDigests(t, target); !reflect.DeepEqual(databaseBefore, after) {
		t.Fatalf("completed database changed: %v -> %v", databaseBefore, after)
	}
	assertTreeUnchanged(t, "completed recovery source", sourceBefore, fingerprintTree(t, volume.root))
	assertNoCopyInWork(t, target)
}

func TestCopyInCompletedRecoveryRefusesScratchInsideEitherRoot(t *testing.T) {
	for _, root := range []string{"source", "target"} {
		t.Run(root, func(t *testing.T) {
			volume := newLegacyVolume(t)
			target := canonicalTempDir(t)
			options := copyInOptions(volume, target)
			options.Lock = volume.lock(t)
			interruptCopyIn(t, options, "recorded")
			options.ScratchDir = target
			if root == "source" {
				options.ScratchDir = volume.root
			}
			sourceBefore, targetBefore := fingerprintTree(t, volume.root), fingerprintTree(t, target)
			_, err := CopyIn(context.Background(), options)
			if !errors.Is(err, ErrSnapshotFailed) {
				t.Fatalf("completed recovery scratch = %v, want snapshot refusal", err)
			}
			if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorSnapshotFailed || !errors.Is(err, ErrSnapshotScratch) {
				t.Fatalf("completed recovery scratch code = %q/%t (%v), want typed snapshot refusal", code, ok, err)
			}
			assertTreeUnchanged(t, "scratch-refused source", sourceBefore, fingerprintTree(t, volume.root))
			assertTreeUnchanged(t, "scratch-refused target and marker", targetBefore, fingerprintTree(t, target))
		})
	}
}

func TestCopyInCompletedRecoveryRemovesItsSnapshotOnEveryRefusal(t *testing.T) {
	for _, reason := range []string{"member-mismatch", "marker-mismatch", "verification-stopped"} {
		t.Run(reason, func(t *testing.T) {
			volume := newLegacyVolume(t)
			target := canonicalTempDir(t)
			options := copyInOptions(volume, target)
			options.Lock = volume.lock(t)
			interruptCopyIn(t, options, "recorded")
			options.ScratchDir = canonicalTempDir(t)
			stopped := errors.New("completed recovery stopped")
			var expected error
			switch reason {
			case "member-mismatch":
				if err := os.WriteFile(filepath.Join(target, dataKeyFileName), []byte("unexpected key bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
				expected = ErrTargetModified
			case "marker-mismatch":
				marker, _, err := copyinmarker.Read(target)
				if err != nil {
					t.Fatal(err)
				}
				marker.SourceFingerprintBefore = "different-source-observation"
				if err := copyinmarker.Write(target, marker); err != nil {
					t.Fatal(err)
				}
				expected = ErrTargetConflict
			case "verification-stopped":
				options.checkpoint = func(phase string) error {
					if phase == "completed-verified" {
						return stopped
					}
					return nil
				}
				expected = stopped
			}
			sourceBefore, targetBefore := fingerprintTree(t, volume.root), fingerprintTree(t, target)
			if _, err := CopyIn(context.Background(), options); !errors.Is(err, expected) {
				t.Fatalf("completed recovery refusal = %v, want %v", err, expected)
			}
			if entries, err := os.ReadDir(options.ScratchDir); err != nil || len(entries) != 0 {
				t.Fatalf("refusal left private scratch: %v/%v", entries, err)
			}
			assertTreeUnchanged(t, "refused source", sourceBefore, fingerprintTree(t, volume.root))
			assertTreeUnchanged(t, "refused target and marker", targetBefore, fingerprintTree(t, target))
		})
	}
}

func TestCopyInPreservesCandidateReadErrorTypes(t *testing.T) {
	for _, markerPresent := range []bool{false, true} {
		for _, failure := range []string{"outdated-schema", "canceled"} {
			t.Run(fmt.Sprintf("marker=%t/%s", markerPresent, failure), func(t *testing.T) {
				volume := newLegacyVolume(t)
				target := canonicalTempDir(t)
				options := copyInOptions(volume, target)
				options.Lock = volume.lock(t)
				interruptCopyIn(t, options, "recorded")
				if !markerPresent {
					if err := os.RemoveAll(newTargetPaths(target).Work); err != nil {
						t.Fatal(err)
					}
				}
				options.ScratchDir = canonicalTempDir(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if failure == "outdated-schema" {
					db, err := sql.Open("sqlite", filepath.Join(target, databaseFileName))
					if err != nil {
						t.Fatal(err)
					}
					_, alterErr := db.Exec(`alter table upgrade_operations rename to prior_upgrade_operations; create table upgrade_operations (id integer primary key, stage text); insert into upgrade_operations values (1, 'copy_in')`)
					closeErr := db.Close()
					if alterErr != nil || closeErr != nil {
						t.Fatalf("old schema = %v/%v", alterErr, closeErr)
					}
				} else {
					old := afterSnapshotCopy
					afterSnapshotCopy = cancel
					defer func() { afterSnapshotCopy = old }()
				}
				before := fingerprintTree(t, target)
				_, err := CopyIn(ctx, options)
				if failure == "outdated-schema" {
					if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorStoreSchemaOutdated || !errors.Is(err, ErrStoreSchemaOutdated) {
						t.Errorf("schema read = %q/%t/%v", code, ok, err)
					}
				} else if err != ctx.Err() {
					t.Errorf("canceled read = %v, want context error unchanged", err)
				}
				assertTreeUnchanged(t, "read-refused target", before, fingerprintTree(t, target))
				if entries, err := os.ReadDir(options.ScratchDir); err != nil || len(entries) != 0 {
					t.Fatalf("read refusal left private scratch: %v/%v", entries, err)
				}
			})
		}
	}
}
