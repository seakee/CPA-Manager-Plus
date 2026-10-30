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
	resolved, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temporary directory: %v", err)
	}
	return resolved
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
		LegacyRoot: volume.root,
		TargetRoot: targetRoot,
		BundleDir:  DefaultBundleDirectory(targetRoot),
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
	fingerprint, _, err := fingerprintSource(volume.root)
	if err != nil {
		t.Fatalf("fingerprint the 1.x source: %v", err)
	}
	if err := copyinmarker.Write(targetRoot, copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		TargetDatabase:          filepath.Join(targetRoot, databaseFileName),
		SourceKind:              SourceKindUpgradedLegacy,
		SourceFingerprintBefore: fingerprint,
		SourceFingerprintAfter:  fingerprint,
		BackupManifestSHA256:    digest,
		BackupDirectory:         bundleDir,
	}); err != nil {
		t.Fatalf("write the provenance marker: %v", err)
	}
	return digest, fingerprint
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
