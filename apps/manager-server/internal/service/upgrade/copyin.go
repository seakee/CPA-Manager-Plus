package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/maintenancebundle"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

// SourceKindUpgradedLegacy is the source kind a copy-in records: the v2 store
// holds data that was copied in from a stopped 1.x volume. It is a durable
// observation, not an authorization; the Upgrade operation row is what admits
// such a store at startup.
const SourceKindUpgradedLegacy = "upgraded-legacy"

const (
	// bundleDirectoryName is the backup location inside the v2 data root. The
	// backup must survive the copy-in that it makes possible.
	bundleDirectoryName = "upgrade-backup"
	// copyInWorkDirectoryName is this operation's reserved scratch area inside
	// the v2 data root. It only ever holds a restored staging tree, the
	// maintenance-bundle publication journal for that exact staging root and the
	// copy-in provenance marker that survives a crash before the record commits.
	copyInWorkDirectoryName = copyinmarker.WorkDirectoryName
	copyInStagingDirectory  = "restored"
	// configMemberName keeps maintenancebundle's layout explicit. The copy-in
	// carries no source config member: the 1.x CPA config is YAML, lives outside
	// the data volume and is imported by the later CPA step. This names a path a
	// 1.x data root never holds.
	configMemberName = ".cpamp-upgrade-no-config-member"
)

// copyMember names the bundle member a copy-in materializes and where the v2
// layout expects it. Order matters only for readability: each rename is
// independent and idempotent.
type copyMember struct{ bundle, target string }

var copyInMembers = []copyMember{
	{"database", databaseFileName},
	{"database-wal", databaseFileName + "-wal"},
	{"database-shm", databaseFileName + "-shm"},
	{"database-journal", databaseFileName + "-journal"},
	{"data-key", dataKeyFileName},
	{"usage-archives", archivesDirName},
}

// SourceDatabaseName is the 1.x Manager database file name. The v2 layout uses
// the same name, so the copy-in never renames the database itself.
const SourceDatabaseName = databaseFileName

// SourceDatabasePath is the 1.x Manager database inside a 1.x data directory.
func SourceDatabasePath(root string) string { return filepath.Join(root, databaseFileName) }

// Result is the observable outcome of a copy-in. It reports what was recorded,
// never an authority: Stage and DomainsDone are the persisted row, the
// fingerprints are the two source observations, and BackupManifestSHA256 names
// the backup that made the copy eligible.
type Result struct {
	Stage                   model.UpgradeStage `json:"stage"`
	DomainsDone             []string           `json:"domainsDone"`
	SourceKind              string             `json:"sourceKind"`
	SourceVersion           string             `json:"sourceVersion"`
	SourceFingerprintBefore string             `json:"sourceFingerprintBefore"`
	SourceFingerprintAfter  string             `json:"sourceFingerprintAfter"`
	BackupManifestSHA256    string             `json:"backupManifestSha256"`
	BackupSourceRevision    string             `json:"backupSourceRevision"`
	BackupDirectory         string             `json:"backupDirectory"`
	TargetDatabase          string             `json:"targetDatabase"`
	TargetDataKey           string             `json:"targetDataKey"`
	TargetArchives          string             `json:"targetArchives"`
	// AlreadyComplete reports that the stored record already contained this
	// copy-in, so nothing was copied, restored or migrated again.
	AlreadyComplete bool `json:"alreadyComplete"`
	// Restored reports that this run moved restored bytes into the v2 layout.
	Restored    bool  `json:"restored"`
	UpdatedAtMS int64 `json:"updatedAtMs"`
}

// DefaultBundleDirectory is the backup destination used when the operator does
// not choose one: inside the v2 data root, so it survives the run.
func DefaultBundleDirectory(targetRoot string) string {
	return filepath.Join(targetRoot, bundleDirectoryName)
}

// CopyIn backs up a stopped 1.x volume and copies it into the v2 data root.
//
// It is the only Upgrade step that writes: the 1.x source is opened read-only
// and is never renamed, moved or written beside. The order is fixed —
// fingerprint, Create, Verify, RestoreNew into a reserved staging root inside
// the v2 data root, rename each member into the v2 layout, publish the
// provenance marker, migrate the new store, record the outcome — and every step
// is resumable: a verified bundle is reused instead of recreated, an already
// materialized target is not restored again (the marker proves it even after the
// migration rewrote the database), and a recorded copy_in returns the stored
// result without touching anything. A source that changes while the fence is
// held aborts with a typed failure after rolling back this run's own writes.
func CopyIn(ctx context.Context, options Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	sourceRoot, err := resolveLegacyRoot(options.LegacyRoot)
	if err != nil {
		return Result{}, err
	}
	if sourceRoot, err = canonicalPath(sourceRoot); err != nil {
		return Result{}, fmt.Errorf("resolve the 1.x source root: %w", err)
	}
	targetRoot, err := resolveTargetRoot(options.TargetRoot)
	if err != nil {
		return Result{}, err
	}
	if targetRoot, err = canonicalPath(targetRoot); err != nil {
		return Result{}, fmt.Errorf("resolve the v2 data root: %w", err)
	}
	bundleDir, err := resolveBundleDir(options.BundleDir)
	if err != nil {
		return Result{}, err
	}
	if bundleDir, err = canonicalPath(bundleDir); err != nil {
		return Result{}, fmt.Errorf("resolve the backup destination: %w", err)
	}
	if err := rejectOverlap(sourceRoot, targetRoot, bundleDir); err != nil {
		return Result{}, err
	}
	if options.Lock == nil {
		return Result{}, errors.New("copy-in requires the held 1.x ownership lock")
	}
	layout := sourceBundleLayout(sourceRoot)
	if err := requireSourceLock(options.Lock, layout.DatabasePath); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return Result{}, fmt.Errorf("create the v2 data root: %w", err)
	}
	paths := newTargetPaths(targetRoot)
	result := Result{
		SourceKind:      SourceKindUpgradedLegacy,
		BackupDirectory: bundleDir,
		TargetDatabase:  paths.Database,
		TargetDataKey:   paths.DataKey,
		TargetArchives:  paths.Archives,
		DomainsDone:     []string{},
	}
	// A recorded copy-in is the completion evidence for this function. Reading
	// it must not migrate, restore or otherwise touch the store, and the record
	// is verified against the v2 layout instead of being taken on trust.
	if recorded, found, err := readRecordedCopyIn(ctx, paths.Database); err != nil {
		return Result{}, err
	} else if found {
		return completedResult(ctx, paths, bundleDir, recorded, result)
	}

	before, _, err := fingerprintSource(sourceRoot)
	if err != nil {
		return Result{}, fmt.Errorf("fingerprint the 1.x source: %w", err)
	}
	result.SourceFingerprintBefore = before

	manifest, digest, err := ensureBackup(ctx, layout, bundleDir, options.Lock)
	if err != nil {
		return Result{}, err
	}
	result.BackupManifestSHA256 = digest
	result.BackupSourceRevision = manifest.SourceRevision
	result.SourceVersion = bundleSourceRelease(ctx, bundleDir)
	if err := runCheckpoint(options, "backup-verified"); err != nil {
		return Result{}, err
	}

	restored, moved, err := materializeTarget(ctx, paths, bundleDir, manifest, digest)
	if err != nil {
		return Result{}, err
	}
	result.Restored = restored
	if err := runCheckpoint(options, "target-restored"); err != nil {
		return Result{}, err
	}

	// The fence was held for the whole run; a different source fingerprint means
	// the copy has no provenance and nothing may be recorded as complete.
	after, _, err := fingerprintSource(sourceRoot)
	if err != nil {
		return Result{}, fmt.Errorf("fingerprint the 1.x source after the copy: %w", err)
	}
	if after != before {
		aborted := newUpgradeError(UpgradeErrorSourceChanged,
			"the 1.x source changed during the copy-in; the members this run moved were rolled back and nothing was recorded", nil)
		if rollbackErr := rollbackMaterialized(paths, moved); rollbackErr != nil {
			return Result{}, errors.Join(aborted, fmt.Errorf("roll back the copy-in: %w", rollbackErr))
		}
		return Result{}, aborted
	}
	result.SourceFingerprintAfter = after

	// Provenance is published before the migration commits. The migration
	// rewrites the copied database in place, so a process death between that
	// commit and the operation row would otherwise leave a v2 store that nothing
	// describes: the admission gate would read "native-v2, no record" and serve
	// a half-finished install as a normal one.
	if err := writeCopyInMarker(paths, digest, result); err != nil {
		return Result{}, err
	}

	recorded, err := migrateAndRecord(ctx, paths, result)
	if err != nil {
		return Result{}, err
	}
	// The staging tree, its publication journal and the marker are only removed
	// once the record is committed; before that they are the resume evidence.
	if err := os.RemoveAll(paths.Work); err != nil {
		return Result{}, fmt.Errorf("remove the copy-in work directory: %w", err)
	}
	result.Stage = recorded.Stage
	result.DomainsDone = recorded.DomainsDone
	result.UpdatedAtMS = recorded.UpdatedAtMS
	return result, nil
}

// ReadRecord reads the single recorded Upgrade operation from an existing v2
// Manager database. It opens the store read-only, applies no pragma that
// persists state and never migrates. found is false when the operation table
// has not been created yet, which is the normal state before any Upgrade step.
func ReadRecord(ctx context.Context, databasePath string) (model.UpgradeOperation, bool, error) {
	return recordedOperation(ctx, databasePath, false)
}

// recordedOperation reads the single row. With immutable set it reads a store
// this process alone may touch (the server is down), which also keeps the read
// from creating a WAL index beside it.
func recordedOperation(ctx context.Context, databasePath string, immutable bool) (model.UpgradeOperation, bool, error) {
	if _, err := os.Lstat(databasePath); err != nil {
		return model.UpgradeOperation{}, false, err
	}
	dsn := readOnlyDataSourceName(databasePath, false)
	if immutable {
		dsn = readOnlySnapshotName(databasePath)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return model.UpgradeOperation{}, false, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { _ = db.Close() }()
	present, err := tablePresent(ctx, db, "upgrade_operations")
	if err != nil || !present {
		return model.UpgradeOperation{}, false, err
	}
	return upgradeoperation.New(db).Get(ctx)
}

type targetPaths struct {
	Root     string
	Database string
	DataKey  string
	Archives string
	Work     string
	Staging  string
}

func newTargetPaths(root string) targetPaths {
	work := filepath.Join(root, copyInWorkDirectoryName)
	return targetPaths{
		Root:     root,
		Database: filepath.Join(root, databaseFileName),
		DataKey:  filepath.Join(root, dataKeyFileName),
		Archives: filepath.Join(root, archivesDirName),
		Work:     work,
		Staging:  filepath.Join(work, copyInStagingDirectory),
	}
}

func resolveTargetRoot(root string) (string, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return "", errors.New("the v2 data root is required")
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve the v2 data root: %w", err)
	}
	return absolute, nil
}

func resolveBundleDir(dir string) (string, error) {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return "", errors.New("the backup destination is required")
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve the backup destination: %w", err)
	}
	return absolute, nil
}

// canonicalPath resolves symlinks in the existing part of a path, so a
// destination that does not exist yet still compares canonically with its
// neighbours.
func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return absolute, nil
	}
	resolvedParent, err := canonicalPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(absolute)), nil
}

// within reports whether child is parent itself or sits below it.
func within(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if relative == "." {
		return true
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// rejectOverlap keeps every write inside the v2 data root. The 1.x volume is a
// source, never a destination.
func rejectOverlap(sourceRoot, targetRoot, bundleDir string) error {
	if within(sourceRoot, targetRoot) || within(targetRoot, sourceRoot) {
		return errors.New("the v2 data root and the 1.x source root must not contain each other")
	}
	if within(sourceRoot, bundleDir) {
		return errors.New("the backup destination must not sit inside the 1.x source root")
	}
	if within(bundleDir, targetRoot) {
		return errors.New("the v2 data root must not sit inside the backup destination")
	}
	return nil
}

// sourceBundleLayout describes the 1.x data volume for maintenancebundle. The
// archive root is required: a 1.x volume without it is not a complete source.
func sourceBundleLayout(root string) maintenancebundle.Layout {
	return maintenancebundle.Layout{
		DatabasePath:    filepath.Join(root, databaseFileName),
		DataKeyPath:     filepath.Join(root, dataKeyFileName),
		ConfigPath:      filepath.Join(root, configMemberName),
		ArchivesPath:    filepath.Join(root, archivesDirName),
		RequireConfig:   false,
		RequireArchives: true,
	}
}

// requireSourceLock proves the caller's fence is the one for this database, so
// a lock taken for another path can never authorize this copy.
func requireSourceLock(lock *processlock.Lock, databasePath string) error {
	resolved := databasePath
	if dir, err := filepath.EvalSymlinks(filepath.Dir(databasePath)); err == nil {
		resolved = filepath.Join(dir, filepath.Base(databasePath))
	}
	if filepath.Clean(resolved) != filepath.Clean(lock.DatabasePath()) {
		return errors.New("the held ownership lock does not belong to the 1.x source database")
	}
	return lock.Validate()
}

func readRecordedCopyIn(ctx context.Context, databasePath string) (model.UpgradeOperation, bool, error) {
	if _, err := os.Lstat(databasePath); os.IsNotExist(err) {
		return model.UpgradeOperation{}, false, nil
	} else if err != nil {
		return model.UpgradeOperation{}, false, err
	}
	recorded, found, err := recordedOperation(ctx, databasePath, true)
	if err != nil || !found {
		return model.UpgradeOperation{}, false, err
	}
	if !copyInRecorded(recorded) {
		return model.UpgradeOperation{}, false, nil
	}
	return recorded, true, nil
}

// copyInRecorded reports whether the row already holds this copy-in. A failed
// or preflight-only stage is not a completed copy, so a retry resumes it.
func copyInRecorded(recorded model.UpgradeOperation) bool {
	if !recorded.HasDomain(model.UpgradeDomainSourceCopy) {
		return false
	}
	switch recorded.Stage {
	case "", model.UpgradeStagePreflight, model.UpgradeStageFailed:
		return false
	}
	return model.UpgradeStageCopyIn.CanAdvanceTo(recorded.Stage)
}

// completedResult returns the stored outcome of a recorded copy-in, but only
// after proving the v2 layout still holds what the record says was copied. A
// recorded row is not evidence by itself: the bytes the operator will keep
// using are what has to match.
func completedResult(ctx context.Context, paths targetPaths, bundleDir string, recorded model.UpgradeOperation, result Result) (Result, error) {
	if err := verifyCompletedTarget(ctx, paths, bundleDir, recorded); err != nil {
		return Result{}, err
	}
	result.AlreadyComplete = true
	result.Stage = recorded.Stage
	result.DomainsDone = recorded.DomainsDone
	result.SourceVersion = recorded.SourceVersion
	result.SourceFingerprintBefore = recorded.SourceFingerprintBefore
	result.SourceFingerprintAfter = recorded.SourceFingerprintAfter
	result.BackupManifestSHA256 = recorded.BackupManifestSHA256
	result.UpdatedAtMS = recorded.UpdatedAtMS
	return result, nil
}

// verifyCompletedTarget compares the v2 data root with the backup that made the
// copy eligible. The database is the migrated store, so its bytes can never
// match the backup again; it must exist and carry the schema the migration
// produced. Everything else the copy-in placed must still match byte for byte.
func verifyCompletedTarget(ctx context.Context, paths targetPaths, bundleDir string, recorded model.UpgradeOperation) error {
	if strings.TrimSpace(recorded.BackupManifestSHA256) == "" {
		return newUpgradeError(UpgradeErrorTargetModified,
			"the recorded copy-in names no backup manifest, so the v2 data root cannot be verified", nil)
	}
	manifest, digest, err := verifyRecordedBackup(ctx, bundleDir)
	if err != nil {
		return err
	}
	if digest != recorded.BackupManifestSHA256 {
		return newUpgradeError(UpgradeErrorTargetModified,
			"the backup destination holds a different manifest than the one the copy-in recorded", nil)
	}
	if err := requireTargetMember(paths.Root, manifest, "data-key", "the v2 data key does not match the backup"); err != nil {
		return err
	}
	if err := requireTargetMember(paths.Root, manifest, archivesDirName, "the v2 archive tree does not match the backup"); err != nil {
		return err
	}
	if err := refuseExtraTargetEntries(paths, manifest); err != nil {
		return err
	}
	migrated, err := databaseCarriesV2Schema(ctx, paths.Database)
	if err != nil {
		return err
	}
	if !migrated {
		return newUpgradeError(UpgradeErrorTargetModified,
			"the v2 database is missing or is not the migrated store the copy-in recorded", nil)
	}
	return nil
}

// verifyRecordedBackup re-verifies the backup and returns its published
// manifest digest. The backup is the only reference for every target digest, so
// an unverifiable one leaves nothing to compare against.
func verifyRecordedBackup(ctx context.Context, bundleDir string) (maintenancebundle.Manifest, string, error) {
	manifest, err := maintenancebundle.Verify(ctx, bundleDir)
	if err != nil {
		return maintenancebundle.Manifest{}, "", fmt.Errorf("the backup that made the copy eligible does not verify: %w", err)
	}
	digest, err := manifestDigest(bundleDir)
	if err != nil {
		return maintenancebundle.Manifest{}, "", fmt.Errorf("read the backup manifest digest: %w", err)
	}
	return manifest, digest, nil
}

// requireTargetMember refuses a member the backup describes but the v2 layout
// does not hold, or holds with different bytes.
func requireTargetMember(root string, manifest maintenancebundle.Manifest, member, detail string) error {
	matching, err := targetMemberMatches(root, manifest, member)
	if err != nil {
		return err
	}
	if !matching {
		return newUpgradeError(UpgradeErrorTargetModified, detail, nil)
	}
	return nil
}

func runCheckpoint(options Options, phase string) error {
	if options.checkpoint == nil {
		return nil
	}
	return options.checkpoint(phase)
}

// ensureBackup reuses a verified bundle, or creates and verifies a new one. The
// digest is over the published manifest bytes: it names exactly one backup
// description, so a later step can refuse a different one.
func ensureBackup(ctx context.Context, layout maintenancebundle.Layout, bundleDir string, lock *processlock.Lock) (maintenancebundle.Manifest, string, error) {
	info, err := os.Lstat(bundleDir)
	if err == nil {
		if !info.IsDir() {
			return maintenancebundle.Manifest{}, "", errors.New("the backup destination is not a directory")
		}
		manifest, verifyErr := maintenancebundle.Verify(ctx, bundleDir)
		if verifyErr != nil {
			return maintenancebundle.Manifest{}, "", fmt.Errorf("the existing backup does not verify: %w", verifyErr)
		}
		if err := maintenancebundle.RevalidateSource(ctx, layout, manifest); err != nil {
			return maintenancebundle.Manifest{}, "", fmt.Errorf("the existing backup does not describe this 1.x source: %w", err)
		}
		digest, err := manifestDigest(bundleDir)
		return manifest, digest, err
	}
	if !os.IsNotExist(err) {
		return maintenancebundle.Manifest{}, "", fmt.Errorf("inspect the backup destination: %w", err)
	}
	created, err := maintenancebundle.Create(ctx, layout, bundleDir, lock)
	if err != nil {
		return maintenancebundle.Manifest{}, "", fmt.Errorf("back up the 1.x volume: %w", err)
	}
	verified, err := maintenancebundle.Verify(ctx, bundleDir)
	if err != nil {
		return maintenancebundle.Manifest{}, "", fmt.Errorf("verify the new backup: %w", err)
	}
	if !reflect.DeepEqual(created, verified) {
		return maintenancebundle.Manifest{}, "", errors.New("the published backup differs from the created manifest")
	}
	digest, err := manifestDigest(bundleDir)
	return verified, digest, err
}

func manifestDigest(bundleDir string) (string, error) {
	return fileContentDigest(filepath.Join(bundleDir, "manifest.json"))
}

// bundleSourceRelease reads the 1.x release floor from the backed-up database
// copy, never from the source volume. An unreadable copy leaves the recorded
// version explicitly unknown rather than guessed.
func bundleSourceRelease(ctx context.Context, bundleDir string) string {
	path := maintenancebundle.BundleLayout(bundleDir).DatabasePath
	db, schema, err := openImmutableSource(ctx, path)
	if db != nil {
		defer func() { _ = db.Close() }()
	}
	if err != nil {
		return VersionUnknown
	}
	return detectSourceVersion(schema, pinnedSourceVersion()).Release
}

// materializeTarget moves the restored members into the v2 layout. It reports
// whether this run had to write anything and exactly which target paths it
// moved, so an abort can roll back only its own work.
//
// An already materialized target is never restored a second time. The marker
// makes that safe across the crash window: it names the backup this target was
// materialized from, so a target whose migration already ran is recognized even
// though the migration changed the database bytes.
func materializeTarget(ctx context.Context, paths targetPaths, bundleDir string, manifest maintenancebundle.Manifest, digest string) (bool, []string, error) {
	if entry, ok := manifest.Entries["config"]; ok && entry.Present {
		return false, nil, errors.New("the backup carries a source config member the v2 data root does not accept")
	}
	if done, err := targetDescribedByMarker(ctx, paths, manifest, digest); err != nil {
		return false, nil, err
	} else if done {
		return false, nil, nil
	}
	matching, err := targetMatches(paths, manifest)
	if err != nil {
		return false, nil, err
	}
	if matching {
		// A target that already holds the backup is not restored again, but it
		// must hold nothing else: an extra archive entry is not part of the copy.
		if err := refuseExtraTargetEntries(paths, manifest); err != nil {
			return false, nil, err
		}
		return false, nil, nil
	}
	if err := os.MkdirAll(paths.Work, 0o700); err != nil {
		return false, nil, fmt.Errorf("create the copy-in work directory: %w", err)
	}
	// A published staging root that still describes this backup is the resume
	// evidence of an interrupted run; anything else in the reserved work
	// directory belongs to a restore that this run must not adopt.
	reused := false
	if info, err := os.Lstat(paths.Staging); err == nil && info.IsDir() {
		if staged, verifyErr := maintenancebundle.Verify(ctx, paths.Staging); verifyErr == nil {
			reused = reflect.DeepEqual(staged.Entries, manifest.Entries)
		}
	}
	if !reused {
		if err := os.RemoveAll(paths.Work); err != nil {
			return false, nil, fmt.Errorf("clear the copy-in work directory: %w", err)
		}
		if err := os.MkdirAll(paths.Work, 0o700); err != nil {
			return false, nil, fmt.Errorf("create the copy-in work directory: %w", err)
		}
		if _, err := maintenancebundle.RestoreNew(ctx, bundleDir, paths.Staging); err != nil {
			return false, nil, fmt.Errorf("restore the backup into the v2 data root: %w", err)
		}
	}
	moved, err := moveMembers(ctx, paths, paths.Staging, manifest)
	if err != nil {
		return false, moved, err
	}
	return true, moved, nil
}

// targetDescribedByMarker reports whether an earlier run of this exact copy-in
// already materialized the target. The marker is the only durable witness of
// that, because the migration rewrites the copied database and its digest can
// never be compared with the backup again.
func targetDescribedByMarker(ctx context.Context, paths targetPaths, manifest maintenancebundle.Manifest, digest string) (bool, error) {
	marker, found, err := copyinmarker.Read(paths.Root)
	if err != nil {
		return false, fmt.Errorf("read the copy-in provenance marker: %w", err)
	}
	if !found || !sameDatabasePath(marker.TargetDatabase, paths.Database) || marker.BackupManifestSHA256 != digest {
		return false, nil
	}
	return targetMaterialized(paths, manifest)
}

// targetMaterialized reports whether the v2 layout holds the members an
// interrupted run moved into place. The marker is published only after every
// member has been moved, so its presence plus a copied database is the
// materialization evidence.
//
// The database and its sidecars are deliberately not compared with the backup:
// the incremental migration rewrites them in place, and a process killed inside
// that rewrite leaves neither the backup bytes nor the finished v2 schema. A
// byte comparison here made exactly that window unresumable — the run wiped the
// marker, restored again and then refused to replace the members already in
// place. The members the migration never touches must still match the backup,
// so a data key or archive tree that moved is still refused.
func targetMaterialized(paths targetPaths, manifest maintenancebundle.Manifest) (bool, error) {
	entry, ok := manifest.Entries["database"]
	if !ok || !entry.Present {
		return false, nil
	}
	info, err := os.Lstat(paths.Database)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return false, nil
	}
	header := make([]byte, 16)
	file, err := os.Open(paths.Database)
	if err != nil {
		return false, err
	}
	_, readErr := io.ReadFull(file, header)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return false, errors.Join(readErr, closeErr)
	}
	if string(header) != "SQLite format 3\x00" {
		return false, nil
	}
	for _, member := range copyInMembers {
		switch member.bundle {
		case "database", "database-wal", "database-shm", "database-journal":
			// Owned by the migration: their bytes are not comparable once it has
			// run, and a live WAL is its own evidence that a run got this far.
			continue
		}
		matching, err := targetMemberMatches(paths.Root, manifest, member.bundle)
		if err != nil || !matching {
			return false, err
		}
	}
	if err := refuseExtraTargetEntries(paths, manifest); err != nil {
		return false, err
	}
	return true, nil
}

// databaseCarriesV2Schema reports whether the target database holds the store
// the migration produces. It joins a live WAL, because a process death between
// the migration commit and the operation row can leave the migrated schema in
// the WAL only.
func databaseCarriesV2Schema(ctx context.Context, path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return false, nil
	}
	db, schema, err := openTargetSnapshot(ctx, path)
	if db != nil {
		defer func() { _ = db.Close() }()
	}
	if err != nil {
		return false, fmt.Errorf("read the copied v2 database: %w", err)
	}
	return schema.sourceKind() == SourceKindNativeV2, nil
}

// openTargetSnapshot reads the copied store of a stopped process: immutable
// only when no live WAL exists, so a WAL that still holds the migration is not
// silently skipped.
func openTargetSnapshot(ctx context.Context, path string) (*sql.DB, sourceSchema, error) {
	immutable := true
	if info, err := os.Lstat(path + "-wal"); err == nil && info.Size() > 0 {
		immutable = false
	}
	return openSource(ctx, path, immutable)
}

// moveMembers renames each restored member into the v2 layout. A member that is
// already in place is skipped, so a run interrupted between two renames
// completes instead of restarting; a different file with the same name is never
// replaced. The returned paths are exactly what this run moved.
func moveMembers(ctx context.Context, paths targetPaths, staging string, manifest maintenancebundle.Manifest) ([]string, error) {
	moved := []string{}
	for _, member := range copyInMembers {
		if err := ctx.Err(); err != nil {
			return moved, err
		}
		entry, ok := manifest.Entries[member.bundle]
		if !ok || !entry.Present {
			continue
		}
		matching, err := targetMemberMatches(paths.Root, manifest, member.bundle)
		if err != nil {
			return moved, err
		}
		if matching {
			continue
		}
		target := filepath.Join(paths.Root, member.target)
		if _, err := os.Lstat(target); err == nil {
			return moved, fmt.Errorf("the v2 data root already holds %s; refusing to replace it", member.target)
		} else if !os.IsNotExist(err) {
			return moved, fmt.Errorf("inspect the v2 data root %s: %w", member.target, err)
		}
		if err := os.Rename(filepath.Join(staging, member.bundle), target); err != nil {
			return moved, fmt.Errorf("move %s into the v2 data root: %w", member.target, err)
		}
		moved = append(moved, target)
	}
	return moved, nil
}

// rollbackMaterialized removes exactly what this run moved into the v2 layout
// and the work directory that carried it. Paths that existed before this run
// are never in the list, so nothing older is touched.
func rollbackMaterialized(paths targetPaths, moved []string) error {
	if len(moved) == 0 {
		return nil
	}
	var err error
	for index := len(moved) - 1; index >= 0; index-- {
		if removeErr := os.RemoveAll(moved[index]); removeErr != nil {
			err = errors.Join(err, removeErr)
		}
	}
	return errors.Join(err, os.RemoveAll(paths.Work))
}

// refuseExtraTargetEntries refuses target content the backup does not describe.
// The archive tree is the only directory the copy-in owns, so an additional
// entry there means the v2 data root was modified after the copy and is no
// longer the tree the backup explains.
func refuseExtraTargetEntries(paths targetPaths, manifest maintenancebundle.Manifest) error {
	info, err := os.Lstat(paths.Archives)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return newUpgradeError(UpgradeErrorTargetModified, "the v2 archive path is not a directory", nil)
	}
	unexpected := ""
	if err := filepath.WalkDir(paths.Archives, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(paths.Root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if _, ok := manifest.Entries[name]; !ok {
			unexpected = name
			return filepath.SkipAll
		}
		return nil
	}); err != nil {
		return err
	}
	if unexpected != "" {
		return newUpgradeError(UpgradeErrorTargetModified,
			fmt.Sprintf("the v2 archive tree holds %s, which the backup does not describe", unexpected), nil)
	}
	return nil
}

// sameDatabasePath compares a marker's target with the database this run owns,
// resolving symlinks in the parent so a canonical path and its alias agree.
func sameDatabasePath(recorded, actual string) bool {
	canonical := func(path string) string {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return filepath.Clean(path)
		}
		if resolved, err := filepath.EvalSymlinks(filepath.Dir(absolute)); err == nil {
			absolute = filepath.Join(resolved, filepath.Base(absolute))
		}
		return filepath.Clean(absolute)
	}
	return canonical(recorded) == canonical(actual)
}

// writeCopyInMarker publishes the provenance of this copy-in inside the work
// directory. It is written before the migration starts and removed only after
// the operation row commits.
func writeCopyInMarker(paths targetPaths, digest string, result Result) error {
	marker := copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		TargetDatabase:          paths.Database,
		SourceKind:              result.SourceKind,
		SourceVersion:           result.SourceVersion,
		SourceFingerprintBefore: result.SourceFingerprintBefore,
		SourceFingerprintAfter:  result.SourceFingerprintAfter,
		BackupManifestSHA256:    digest,
		BackupDirectory:         result.BackupDirectory,
		WrittenAtMS:             time.Now().UnixMilli(),
	}
	if err := copyinmarker.Write(paths.Root, marker); err != nil {
		return fmt.Errorf("write the copy-in provenance marker: %w", err)
	}
	return nil
}

func targetMatches(paths targetPaths, manifest maintenancebundle.Manifest) (bool, error) {
	for _, member := range copyInMembers {
		matching, err := targetMemberMatches(paths.Root, manifest, member.bundle)
		if err != nil || !matching {
			return false, err
		}
	}
	return true, nil
}

// targetMemberMatches compares one bundle member, including every nested
// archive entry, with the file the v2 layout holds for it.
func targetMemberMatches(root string, manifest maintenancebundle.Manifest, member string) (bool, error) {
	for name, entry := range manifest.Entries {
		if name != member && !strings.HasPrefix(name, member+"/") {
			continue
		}
		if !entry.Present {
			continue
		}
		path, ok := targetMemberPath(root, name)
		if !ok {
			return false, nil
		}
		matching, err := entryMatches(path, entry)
		if err != nil || !matching {
			return false, err
		}
	}
	return true, nil
}

// targetMemberPath maps a bundle member name onto the v2 data layout. Members
// the v2 layout has no place for are reported as unmapped so nothing is
// silently dropped.
func targetMemberPath(root string, name string) (string, bool) {
	switch {
	case name == "database":
		return filepath.Join(root, databaseFileName), true
	case strings.HasPrefix(name, "database-"):
		return filepath.Join(root, databaseFileName+strings.TrimPrefix(name, "database")), true
	case name == "data-key":
		return filepath.Join(root, dataKeyFileName), true
	case name == archivesDirName:
		return filepath.Join(root, archivesDirName), true
	case strings.HasPrefix(name, archivesDirName+"/"):
		return filepath.Join(root, filepath.FromSlash(name)), true
	default:
		return "", false
	}
}

func entryMatches(path string, entry maintenancebundle.Entry) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return !entry.Present, nil
	}
	if err != nil {
		return false, err
	}
	if entry.Directory {
		return info.IsDir(), nil
	}
	if !info.Mode().IsRegular() || info.Size() != entry.Size {
		return false, nil
	}
	digest, err := fileContentDigest(path)
	if err != nil {
		return false, err
	}
	return digest == entry.SHA256, nil
}

// migrateAndRecord opens the copied store, runs the incremental v2 schema
// migration and commits the copy_in stage in the same database. The stage only
// moves forward: a retry after a later stage was recorded keeps that later
// stage and only re-records the observations.
func migrateAndRecord(ctx context.Context, paths targetPaths, result Result) (model.UpgradeOperation, error) {
	db, err := managersqlite.Open(paths.Database)
	if err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("open the copied v2 database: %w", err)
	}
	defer func() { _ = db.Close() }()
	repository := upgradeoperation.New(db)
	recorded, found, err := repository.Get(ctx)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if !found {
		recorded = model.UpgradeOperation{}
	}
	recorded.SourceKind = SourceKindUpgradedLegacy
	recorded.SourceVersion = result.SourceVersion
	recorded.SourceFingerprintBefore = result.SourceFingerprintBefore
	recorded.SourceFingerprintAfter = result.SourceFingerprintAfter
	recorded.BackupManifestSHA256 = result.BackupManifestSHA256
	switch recorded.Stage {
	case "", model.UpgradeStagePreflight, model.UpgradeStageFailed, model.UpgradeStageCopyIn:
		recorded.Stage = model.UpgradeStageCopyIn
	}
	recorded.MarkDomain(model.UpgradeDomainSourceCopy)
	if err := repository.Put(ctx, recorded); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("record the copy_in stage: %w", err)
	}
	return recorded, nil
}

// readOnlySnapshotName reads a store nobody else writes. It joins a live WAL
// when one exists and is immutable only when it cannot miss one.
func readOnlySnapshotName(path string) string {
	immutable := true
	if info, err := os.Lstat(path + "-wal"); err == nil && info.Size() > 0 {
		immutable = false
	}
	return readOnlyDataSourceName(path, immutable)
}

func tablePresent(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master
		where type = 'table' and name = ?`, name).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}
