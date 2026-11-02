package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
		if entry.IsDir() {
			entries[filepath.ToSlash(relative)] = "dir " + info.Mode().String()
			return nil
		}
		digest, err := fileContentDigest(path)
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
	sourceDatabaseDigest, err := fileContentDigest(volume.databasePath())
	if err != nil {
		t.Fatalf("digest source database: %v", err)
	}
	sourceKeyDigest, err := fileContentDigest(filepath.Join(volume.root, dataKeyFileName))
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
	manifestFileDigest, err := fileContentDigest(filepath.Join(result.BackupDirectory, "manifest.json"))
	if err != nil {
		t.Fatalf("digest backup manifest: %v", err)
	}
	if result.BackupManifestSHA256 != manifestFileDigest {
		t.Fatalf("recorded manifest digest = %q, want %q", result.BackupManifestSHA256, manifestFileDigest)
	}

	// The v2 layout holds the same bytes for everything that is not migrated.
	if got, err := fileContentDigest(result.TargetDataKey); err != nil || got != sourceKeyDigest {
		t.Fatalf("copied data key digest = %q (%v), want %q", got, err, sourceKeyDigest)
	}
	for _, archived := range volume.archives() {
		relative, err := filepath.Rel(volume.root, archived)
		if err != nil {
			t.Fatalf("resolve archive path: %v", err)
		}
		copied := filepath.Join(targetRoot, relative)
		sourceDigest, err := fileContentDigest(archived)
		if err != nil {
			t.Fatalf("digest source archive: %v", err)
		}
		if got, err := fileContentDigest(copied); err != nil || got != sourceDigest {
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
	// The restore landed but the record did not commit: the v2 layout holds the
	// untranslated 1.x bytes and there is no operation row yet.
	sourceDatabaseDigest, err := fileContentDigest(volume.databasePath())
	if err != nil {
		t.Fatalf("digest source database: %v", err)
	}
	restoredDigest, err := fileContentDigest(filepath.Join(targetRoot, databaseFileName))
	if err != nil || restoredDigest != sourceDatabaseDigest {
		t.Fatalf("restored database digest = %q (%v), want the 1.x bytes %q", restoredDigest, err, sourceDatabaseDigest)
	}
	if _, found, err := ReadRecord(context.Background(), filepath.Join(targetRoot, databaseFileName)); err != nil || found {
		t.Fatalf("interrupted run recorded an operation: found=%t err=%v", found, err)
	}

	observed := map[string]string{}
	options.checkpoint = func(phase string) error {
		if phase == "target-restored" {
			digest, err := fileContentDigest(filepath.Join(targetRoot, databaseFileName))
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

// TestCopyInResumesAfterAnInterruptionInsideTheMigration is the regression for
// the window the copy-in marker exists for: every member was moved and the
// marker was published, and the process died while the incremental migration
// was rewriting the database and its sidecars. Resuming used to compare those
// rewritten bytes with the backup, conclude the target was never materialized,
// delete the marker and then refuse to replace the members it had already
// moved, leaving a store nothing could describe.
func TestCopyInResumesAfterAnInterruptionInsideTheMigration(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	// A real 1.x volume is stopped with a live WAL, so the backup describes the
	// database sidecars and the migration's own WAL is a genuine mismatch.
	sourceWAL, err := sql.Open("sqlite", volume.databasePath())
	if err != nil {
		t.Fatalf("open the 1.x fixture: %v", err)
	}
	defer func() { _ = sourceWAL.Close() }()
	for _, statement := range []string{
		`pragma journal_mode = wal`,
		`pragma wal_autocheckpoint = 0`,
		`insert into settings (key, value, updated_at_ms) values ('wal_frame_probe', '1', 1)`,
	} {
		if _, err := sourceWAL.Exec(statement); err != nil {
			t.Fatalf("prepare a live source WAL: %v", err)
		}
	}
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
	if info, err := os.Lstat(filepath.Join(options.BundleDir, "database-wal")); err != nil || info.Size() == 0 {
		t.Fatalf("the backup holds no database WAL (err=%v)", err)
	}

	// The run that died published its provenance marker before it started the
	// migration, so the resume has to recognize the target from that marker
	// rather than from the operation row.
	if _, found, err := copyinmarker.Read(targetRoot); err != nil || !found {
		t.Fatalf("the interrupted run left no copy-in marker: found=%t err=%v", found, err)
	}

	// The migration was already rewriting the copied store: the database left
	// delete-journal mode and its sidecars hold frames the backup never had.
	databasePath := filepath.Join(targetRoot, databaseFileName)
	migration, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open the copied store: %v", err)
	}
	defer func() { _ = migration.Close() }()
	if _, err := migration.Exec(`pragma journal_mode = wal`); err != nil {
		t.Fatalf("switch the copied store to WAL: %v", err)
	}
	if _, err := migration.Exec(`pragma wal_autocheckpoint = 0; create table if not exists migration_probe (probe text)`); err != nil {
		t.Fatalf("write the migration probe: %v", err)
	}
	if _, err := migration.Exec(`insert into migration_probe (probe) values ('interrupted')`); err != nil {
		t.Fatalf("append the migration probe row: %v", err)
	}
	if info, err := os.Lstat(databasePath + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("the interrupted migration left no WAL (err=%v)", err)
	}

	// Resuming adopts the materialized target: nothing is restored or moved
	// again, the migration finishes and the operation row commits.
	result, err := CopyIn(context.Background(), options)
	if err != nil {
		t.Fatalf("resumed copy in: %v", err)
	}
	if result.Restored {
		t.Fatal("resumed copy in restored bytes over a materialized target")
	}
	if result.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("resumed stage = %q, want %q", result.Stage, model.UpgradeStageCopyIn)
	}
	assertMigratedStore(t, databasePath)
	if _, err := os.Lstat(filepath.Join(targetRoot, copyInWorkDirectoryName)); !os.IsNotExist(err) {
		t.Fatalf("resumed copy in left its work directory behind (err=%v)", err)
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
	strayDigest, err := fileContentDigest(strayPath)
	if err != nil {
		t.Fatalf("digest the stray store: %v", err)
	}

	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetConflict) {
		t.Fatalf("copy in over a stray database = %v, want %s", err, ErrTargetConflict)
	}
	if after, err := fileContentDigest(strayPath); err != nil || after != strayDigest {
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

// materializeMigratedTarget reproduces the state a process death inside the copy
// leaves behind: the backup verified, every member renamed into the v2 layout,
// the migration committed, and the operation row never written. The provenance
// marker is the only description of that state. It returns the backup manifest
// digest and the source fingerprint the marker carries.
func materializeMigratedTarget(t *testing.T, volume legacyVolume, targetRoot string, lock *processlock.Lock) (string, string) {
	t.Helper()
	ctx := context.Background()
	bundleDir := DefaultBundleDirectory(targetRoot)
	created, err := maintenancebundle.Create(ctx, sourceBundleLayout(volume.root), bundleDir, lock)
	if err != nil {
		t.Fatalf("back up the 1.x volume: %v", err)
	}
	verified, err := maintenancebundle.Verify(ctx, bundleDir)
	if err != nil || !reflect.DeepEqual(created, verified) {
		t.Fatalf("verify the fixture backup: %v", err)
	}
	digest, err := manifestDigest(bundleDir)
	if err != nil {
		t.Fatalf("digest the fixture backup manifest: %v", err)
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		t.Fatalf("create the v2 data root: %v", err)
	}
	staging := filepath.Join(targetRoot, copyInWorkDirectoryName, copyInStagingDirectory)
	if err := os.MkdirAll(filepath.Join(targetRoot, copyInWorkDirectoryName), 0o700); err != nil {
		t.Fatalf("create the copy-in work directory: %v", err)
	}
	if _, err := maintenancebundle.RestoreNew(ctx, bundleDir, staging); err != nil {
		t.Fatalf("restore the fixture backup: %v", err)
	}
	for _, member := range copyInMembers {
		entry, ok := verified.Entries[member.bundle]
		if !ok || !entry.Present {
			continue
		}
		if err := os.Rename(filepath.Join(staging, member.bundle), filepath.Join(targetRoot, member.target)); err != nil {
			t.Fatalf("materialize %s: %v", member.target, err)
		}
	}
	// The migration runs and rewrites the copied database; the process dies
	// before the operation row can commit.
	db, err := managersqlite.Open(filepath.Join(targetRoot, databaseFileName))
	if err != nil {
		t.Fatalf("migrate the copied database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the migrated database: %v", err)
	}
	fingerprint, err := fingerprintSource(volume.root)
	if err != nil {
		t.Fatalf("fingerprint the 1.x source: %v", err)
	}
	databasePath := filepath.Join(targetRoot, databaseFileName)
	identity, err := copyinmarker.Identity(databasePath)
	if err != nil {
		t.Fatalf("read the copied database identity: %v", err)
	}
	if err := copyinmarker.Write(targetRoot, copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseMigrating,
		TargetDatabase:          databasePath,
		TargetDatabaseIdentity:  identity,
		SourceKind:              SourceKindUpgradedLegacy,
		SourceFingerprintBefore: fingerprint.Digest,
		SourceFingerprintAfter:  fingerprint.Digest,
		BackupManifestSHA256:    digest,
		BackupDirectory:         bundleDir,
	}); err != nil {
		t.Fatalf("write the provenance marker: %v", err)
	}
	return digest, fingerprint.Digest
}

// TestCopyInResumesWhenTheMigrationCommittedWithoutTheRecord is the crash-window
// regression: a v2 root whose database was migrated but whose operation row was
// never written used to be refused forever, because the migrated database can
// no longer match the backup digest and the copy-in would not replace it.
func TestCopyInResumesWhenTheMigrationCommittedWithoutTheRecord(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	lock := volume.lock(t)
	digest, fingerprint := materializeMigratedTarget(t, volume, targetRoot, lock)
	databasePath := filepath.Join(targetRoot, databaseFileName)
	if _, found, err := ReadRecord(context.Background(), databasePath); err != nil || found {
		t.Fatalf("the constructed state recorded an operation: found=%t err=%v", found, err)
	}
	assertMigratedStore(t, databasePath)
	migrated, err := os.Lstat(databasePath)
	if err != nil {
		t.Fatalf("inspect the migrated database: %v", err)
	}

	options := copyInOptions(volume, targetRoot)
	options.Lock = lock
	result, err := CopyIn(context.Background(), options)
	if err != nil {
		t.Fatalf("resumed copy in: %v", err)
	}
	if result.Restored {
		t.Fatal("resumed copy in restored bytes over the migrated target")
	}
	if result.AlreadyComplete {
		t.Fatal("resumed copy in reported a completed operation that was never recorded")
	}
	if result.Stage != model.UpgradeStageCopyIn || result.BackupManifestSHA256 != digest {
		t.Fatalf("resumed copy in = %+v, want the copy_in stage and the backup manifest", result)
	}
	if result.SourceFingerprintBefore != fingerprint || result.SourceFingerprintAfter != fingerprint {
		t.Fatalf("resumed fingerprints = %q/%q, want %q", result.SourceFingerprintBefore, result.SourceFingerprintAfter, fingerprint)
	}

	// The migration is idempotent and the row it now records describes the copy.
	recorded, found, err := ReadRecord(context.Background(), databasePath)
	if err != nil || !found {
		t.Fatalf("read the recorded operation: found=%t err=%v", found, err)
	}
	if recorded.Stage != model.UpgradeStageCopyIn || recorded.SourceKind != SourceKindUpgradedLegacy {
		t.Fatalf("recorded stage/kind = %q/%q", recorded.Stage, recorded.SourceKind)
	}
	if !recorded.HasDomain(model.UpgradeDomainSourceCopy) {
		t.Fatalf("recorded domains = %v, want %s", recorded.DomainsDone, model.UpgradeDomainSourceCopy)
	}
	if recorded.BackupManifestSHA256 != digest || recorded.SourceFingerprintBefore != fingerprint {
		t.Fatalf("recorded row = %+v, want the backup manifest and the source fingerprint", recorded)
	}
	// The database was adopted, not replaced.
	current, err := os.Lstat(databasePath)
	if err != nil {
		t.Fatalf("inspect the resumed database: %v", err)
	}
	if !os.SameFile(migrated, current) {
		t.Fatal("resumed copy in replaced the migrated database instead of adopting it")
	}
	assertMigratedStore(t, databasePath)
	if _, err := os.Lstat(filepath.Join(targetRoot, copyInWorkDirectoryName)); !os.IsNotExist(err) {
		t.Fatalf("resumed copy in left its work directory behind (err=%v)", err)
	}
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
	if _, found, err := ReadRecord(context.Background(), filepath.Join(targetRoot, databaseFileName)); err != nil || found {
		t.Fatalf("refused copy in recorded an operation: found=%t err=%v", found, err)
	}
}

// TestCopyInReusesAPublishedStagingTree covers the resume branch that adopts a
// staged restore instead of restoring the backup again: the members that reach
// the v2 layout are the staged files themselves.
func TestCopyInReusesAPublishedStagingTree(t *testing.T) {
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	lock := volume.lock(t)
	ctx := context.Background()
	bundleDir := DefaultBundleDirectory(targetRoot)
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		t.Fatalf("create the v2 data root: %v", err)
	}
	if _, err := maintenancebundle.Create(ctx, sourceBundleLayout(volume.root), bundleDir, lock); err != nil {
		t.Fatalf("back up the 1.x volume: %v", err)
	}
	staging := filepath.Join(targetRoot, copyInWorkDirectoryName, copyInStagingDirectory)
	if err := os.MkdirAll(filepath.Join(targetRoot, copyInWorkDirectoryName), 0o700); err != nil {
		t.Fatalf("create the copy-in work directory: %v", err)
	}
	if _, err := maintenancebundle.RestoreNew(ctx, bundleDir, staging); err != nil {
		t.Fatalf("restore into the staging tree: %v", err)
	}
	stagedKey, err := os.Lstat(filepath.Join(staging, "data-key"))
	if err != nil {
		t.Fatalf("inspect the staged data key: %v", err)
	}

	options := copyInOptions(volume, targetRoot)
	options.Lock = lock
	result, err := CopyIn(ctx, options)
	if err != nil {
		t.Fatalf("resumed copy in: %v", err)
	}
	if !result.Restored || result.AlreadyComplete {
		t.Fatalf("resumed copy in = %+v, want a completed copy", result)
	}
	movedKey, err := os.Lstat(result.TargetDataKey)
	if err != nil {
		t.Fatalf("inspect the copied data key: %v", err)
	}
	if !os.SameFile(stagedKey, movedKey) {
		t.Fatal("copy in restored the backup again instead of reusing the published staging tree")
	}
	assertMigratedStore(t, result.TargetDatabase)
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
		digest, err := fileContentDigest(path)
		if err != nil {
			t.Fatalf("digest %s: %v", name, err)
		}
		digests[name] = digest
	}
	return digests
}

// TestCopyInResumeDoesNotRewriteARestoredLiveWALSidecar is the regression for the
// resume window between "every member moved" and "the migration ran". Resuming
// used to read the restored-but-unadopted store with a WAL-sensitive read-only
// open. SQLite rewrote (or created) the target's -shm for that read, the resume
// then compared the sidecar with the backup, and refused to move the very member
// it had just written: a live-WAL source was stuck forever after an interruption
// there. The marker now says how far the run got, so the resume never opens the
// store before the migration that owns it.
func TestCopyInResumeDoesNotRewriteARestoredLiveWALSidecar(t *testing.T) {
	volume := newLegacyVolume(t)
	startLiveWalSource(t, volume)
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
	before := databaseDigests(t, targetRoot)
	if before[databaseFileName+"-shm"] == "" {
		t.Fatalf("the interrupted run restored no WAL index: %v", before)
	}

	observed := map[string]string{}
	options.checkpoint = func(phase string) error {
		if phase == "target-restored" {
			observed = databaseDigests(t, targetRoot)
		}
		return nil
	}
	result, err := CopyIn(context.Background(), options)
	if err != nil {
		t.Fatalf("resumed copy in = %v, want it to complete", err)
	}
	if result.Restored {
		t.Fatal("resumed copy in restored bytes over a materialized target")
	}
	for name, digest := range before {
		if observed[name] != digest {
			t.Fatalf("resume changed the restored %s before the migration (%q, want %q)", name, observed[name], digest)
		}
	}
	if result.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("resumed stage = %q, want %q", result.Stage, model.UpgradeStageCopyIn)
	}
	assertMigratedStore(t, result.TargetDatabase)
	if _, err := os.Lstat(filepath.Join(targetRoot, copyInWorkDirectoryName)); !os.IsNotExist(err) {
		t.Fatalf("resumed copy in left its work directory behind (err=%v)", err)
	}
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
			name: "data key removed",
			change: func(t *testing.T, targetRoot string) {
				t.Helper()
				if err := os.Remove(filepath.Join(targetRoot, dataKeyFileName)); err != nil {
					t.Fatalf("remove the copied data key: %v", err)
				}
			},
		},
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

// TestCopyInRefusesAForeignDatabaseInTheCrashWindow is the M2 regression. The
// marker used to vouch for "a SQLite file with the right name plus matching
// non-database members", which a v2 store some other run produced also satisfies
// — and the resume then migrated it and recorded upgraded-legacy provenance for
// data that was never copied. The marker now names the file it moved, so a
// swapped-in store is refused instead of adopted.
func TestCopyInRefusesAForeignDatabaseInTheCrashWindow(t *testing.T) {
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

	// The crash window: the marker was published and the operation row was not.
	// Someone then swaps the copied database for a store this copy-in never wrote.
	databasePath := filepath.Join(targetRoot, databaseFileName)
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.Remove(databasePath + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove the copied %s sidecar: %v", suffix, err)
		}
	}
	if err := os.Remove(databasePath); err != nil {
		t.Fatalf("remove the copied database: %v", err)
	}
	foreign, err := managersqlite.Open(databasePath)
	if err != nil {
		t.Fatalf("create the foreign store: %v", err)
	}
	if _, err := foreign.Exec(`insert into settings (key, value, updated_at_ms) values ('foreign_store', '1', 1)`); err != nil {
		_ = foreign.Close()
		t.Fatalf("seed the foreign store: %v", err)
	}
	if err := foreign.Close(); err != nil {
		t.Fatalf("close the foreign store: %v", err)
	}
	foreignDigest, err := fileContentDigest(databasePath)
	if err != nil {
		t.Fatalf("digest the foreign store: %v", err)
	}

	if _, err := CopyIn(context.Background(), options); !errors.Is(err, ErrTargetConflict) {
		t.Fatalf("resumed copy in over a foreign database = %v, want %s", err, ErrTargetConflict)
	}
	if after, err := fileContentDigest(databasePath); err != nil || after != foreignDigest {
		t.Fatalf("the refused resume rewrote the foreign store (%q, err=%v)", after, err)
	}
	if _, found, err := ReadRecord(context.Background(), databasePath); err != nil || found {
		t.Fatalf("the refused resume recorded provenance in the foreign store: found=%t err=%v", found, err)
	}
	if _, found, err := copyinmarker.Read(targetRoot); err != nil || !found {
		t.Fatalf("the refused resume removed the marker: found=%t err=%v", found, err)
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
	recorded, found, err := ReadRecord(context.Background(), databasePath)
	if err != nil || !found || !copyInRecorded(recorded) {
		t.Fatalf("the interrupted run did not commit the record: found=%t err=%v", found, err)
	}
	if _, found, err := copyinmarker.Read(targetRoot); err != nil || !found {
		t.Fatalf("the interrupted run left no marker to clean: found=%t err=%v", found, err)
	}
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

// TestCopyInAdmissionBeforeResumeKeepsTheLiveWALTargetResumable is the H2
// regression. Startup admission used to read the operation row from the copied
// store before it looked at the provenance marker. In the crash window this
// test reproduces — every member renamed into the v2 layout, the migrating
// marker not yet published — that read joined the copied WAL and rewrote
// usage.sqlite-shm, so the resumed move no longer found the sidecar it had
// restored and refused to continue. Admission now decides the materializing
// phase from the marker alone and never opens the unadopted target.
func TestCopyInAdmissionBeforeResumeKeepsTheLiveWALTargetResumable(t *testing.T) {
	for _, phase := range []string{copyinmarker.PhaseMaterializing, copyinmarker.PhaseMigrating} {
		t.Run(phase, func(t *testing.T) {
			volume := newLegacyVolume(t)
			startLiveWalSource(t, volume)
			targetRoot := canonicalTempDir(t)
			options := copyInOptions(volume, targetRoot)
			options.Lock = volume.lock(t)
			injected := errors.New("injected interruption after the members were moved")
			options.checkpoint = func(current string) error {
				if current == "target-restored" {
					return injected
				}
				return nil
			}
			if _, err := CopyIn(context.Background(), options); !errors.Is(err, injected) {
				t.Fatalf("interrupted copy in = %v, want the injected error", err)
			}
			// The bytes are exactly what the interrupted run moved. The
			// materializing case sets only the phase back to the state the last
			// rename left behind, because no checkpoint runs between that
			// rename and the migrating publication.
			marker, found, err := copyinmarker.Read(targetRoot)
			if err != nil || !found {
				t.Fatalf("the interrupted run left no marker: found=%t err=%v", found, err)
			}
			if marker.Phase != copyinmarker.PhaseMigrating {
				t.Fatalf("interrupted marker phase = %q, want %q", marker.Phase, copyinmarker.PhaseMigrating)
			}
			if phase == copyinmarker.PhaseMaterializing {
				marker.Phase = phase
				marker.TargetDatabaseIdentity = copyinmarker.FileIdentity{}
				if err := copyinmarker.Write(targetRoot, marker); err != nil {
					t.Fatalf("write the materializing marker: %v", err)
				}
			}

			before := databaseDigests(t, targetRoot)
			if before[databaseFileName+"-shm"] == "" {
				t.Fatalf("the interrupted run restored no WAL index: %v", before)
			}
			stage, admissionErr := (legacypreflight.DatabaseUpgradeAdmission{
				DatabasePath: filepath.Join(targetRoot, databaseFileName),
			}).Admitted(context.Background())
			if admissionErr != nil || stage != legacypreflight.UpgradeStageInProgress {
				t.Fatalf("admission before the resume = %q/%v, want %q", stage, admissionErr, legacypreflight.UpgradeStageInProgress)
			}
			after := databaseDigests(t, targetRoot)
			if after[databaseFileName] != before[databaseFileName] {
				t.Fatalf("admission rewrote the copied database: %q -> %q", before[databaseFileName], after[databaseFileName])
			}
			admissionRewroteTarget := phase == copyinmarker.PhaseMaterializing && !reflect.DeepEqual(before, after)

			options.checkpoint = nil
			result, err := CopyIn(context.Background(), options)
			if err != nil {
				// The recorded symptom, reported together with its cause so the
				// regression is recognizable: admission's WAL-sensitive read
				// rewrote the sidecar the resumed move compares with the backup.
				t.Fatalf("resume after admission = %v (admission rewrote the unadopted target: %t)", err, admissionRewroteTarget)
			}
			if admissionRewroteTarget {
				t.Fatalf("admission rewrote the unadopted target: %v -> %v", before, after)
			}
			if result.Stage != model.UpgradeStageCopyIn {
				t.Fatalf("resumed stage = %q, want %q", result.Stage, model.UpgradeStageCopyIn)
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
