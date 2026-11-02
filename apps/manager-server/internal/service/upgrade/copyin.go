package upgrade

import (
	"context"
	"database/sql"
	"encoding/json"
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
	Stage         model.UpgradeStage `json:"stage"`
	DomainsDone   []string           `json:"domainsDone"`
	SourceKind    string             `json:"sourceKind"`
	SourceVersion string             `json:"sourceVersion"`
	// SourceRoot is the 1.x data directory this run read. It is recorded with
	// the row so a later rollback can bind the v1 volume to the exact directory
	// the copy came from instead of asserting "the original volume".
	SourceRoot              string `json:"sourceRoot"`
	SourceFingerprintBefore string `json:"sourceFingerprintBefore"`
	SourceFingerprintAfter  string `json:"sourceFingerprintAfter"`
	BackupManifestSHA256    string `json:"backupManifestSha256"`
	BackupSourceRevision    string `json:"backupSourceRevision"`
	BackupDirectory         string `json:"backupDirectory"`
	TargetDatabase          string `json:"targetDatabase"`
	TargetDataKey           string `json:"targetDataKey"`
	TargetArchives          string `json:"targetArchives"`
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
	sourceRoot = canonicalPath(sourceRoot)
	targetRoot, err := resolveTargetRoot(options.TargetRoot)
	if err != nil {
		return Result{}, err
	}
	targetRoot = canonicalPath(targetRoot)
	bundleDir, err := resolveBundleDir(options.BundleDir)
	if err != nil {
		return Result{}, err
	}
	bundleDir = canonicalPath(bundleDir)
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
	// The v2 data root is written by this run alone. The fence is taken here,
	// in the service, so the CLI and any future caller cannot bypass it, and it
	// covers the whole mutating path: target creation, backup, staging, the
	// member renames, the migration, the row and the marker cleanup. It is
	// taken before the first target or backup write, and the source fence the
	// caller already holds is a different lock on a different directory.
	targetLock, err := acquireTargetFence(targetRoot)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = targetLock.Close() }()
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return Result{}, fmt.Errorf("create the v2 data root: %w", err)
	}
	paths := newTargetPaths(targetRoot)
	result := Result{
		SourceKind:      SourceKindUpgradedLegacy,
		SourceRoot:      sourceRoot,
		BackupDirectory: bundleDir,
		TargetDatabase:  paths.Database,
		TargetDataKey:   paths.DataKey,
		TargetArchives:  paths.Archives,
		DomainsDone:     []string{},
	}
	// A marker means an earlier run of this copy-in was interrupted, and it is
	// read before the operation row on purpose. A marker describes how far the
	// run got without opening the database, so a target that holds restored but
	// not yet adopted members is never read with a WAL-sensitive open: that read
	// rewrites or creates the target's -shm and then blocks the very member the
	// resume has to move. Without a marker the target is either absent or a
	// store this copy-in already finished, and reading the row is safe.
	inFlight, err := copyInInFlight(paths)
	if err != nil {
		return Result{}, err
	}
	if !inFlight {
		// A recorded copy-in is the completion evidence for this function.
		// Reading it must not migrate, restore or otherwise touch the store, and
		// the record is verified against the v2 layout instead of being taken on
		// trust.
		if recorded, found, err := readRecordedCopyIn(ctx, paths.Database); err != nil {
			return Result{}, err
		} else if found {
			return completedResult(ctx, paths, bundleDir, recorded, result)
		}
	}

	before, err := fingerprintSource(sourceRoot)
	if err != nil {
		return Result{}, fmt.Errorf("fingerprint the 1.x source: %w", err)
	}
	result.SourceFingerprintBefore = before.Digest

	manifest, digest, err := ensureBackup(ctx, layout, bundleDir, options.Lock)
	if err != nil {
		return Result{}, err
	}
	result.BackupManifestSHA256 = digest
	result.BackupSourceRevision = manifest.SourceRevision
	// The 1.x release is never inferred from the database: the declaration is
	// the only exact signal, and an omitted one is recorded as unknown.
	result.SourceVersion = declaredVersion(options.DeclaredVersion)
	if err := runCheckpoint(options, "backup-verified"); err != nil {
		return Result{}, err
	}

	restored, moved, err := materializeTarget(ctx, paths, bundleDir, manifest, digest, result)
	if err != nil {
		return Result{}, err
	}
	result.Restored = restored
	if err := runCheckpoint(options, "target-restored"); err != nil {
		return Result{}, err
	}

	// The fence was held for the whole run; a different source fingerprint means
	// the copy has no provenance and nothing may be recorded as complete.
	after, err := fingerprintSource(sourceRoot)
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
	result.SourceFingerprintAfter = after.Digest

	// The marker is republished before the migration opens the copied store:
	// from here on it names the source fingerprint the copy observed and the
	// exact database file this run moved, so a process death between the
	// migration commit and the operation row can neither be mistaken for a
	// finished install nor for a store some other run produced.
	if err := writeCopyInMarker(paths, digest, result, copyinmarker.PhaseMigrating); err != nil {
		return Result{}, err
	}

	recorded, alreadyRecorded, err := migrateAndRecord(ctx, paths, result)
	if err != nil {
		return Result{}, err
	}
	if err := runCheckpoint(options, "recorded"); err != nil {
		return Result{}, err
	}
	// The staging tree, its publication journal and the marker are only removed
	// once the record is committed; before that they are the resume evidence. A
	// marker that outlived its own row — the process died between the record
	// commit and this removal — is cleaned up by the rerun that found it.
	if err := os.RemoveAll(paths.Work); err != nil {
		return Result{}, fmt.Errorf("remove the copy-in work directory: %w", err)
	}
	result.AlreadyComplete = alreadyRecorded
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
	return ReadRecordFrom(ctx, db)
}

// MigrateOperationStore brings an outdated Manager store up to this build's
// upgrade table. It is the remedy the read paths name: a read-only reader cannot
// add a column, and a Manager admitted only for the Upgrade surface never opens
// the store writable, so without this command an operation recorded by an older
// build could not be read at all.
//
// The command only serves a store the Upgrade copy-in recorded. The writable
// open also runs the full schema migration, so opening any other store would
// turn a 1.x or native store into one this build serves as native-v2, without
// the copy evidence the Upgrade admission requires. That store is refused from a
// private snapshot before any writable open, so its bytes do not change.
//
// The migration is a write, so it holds the v2 data-root process lock for the
// whole writable open. A held lock means the writable Manager is still serving
// the store and the command refuses with upgrade_store_locked.
func MigrateOperationStore(ctx context.Context, databasePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Lstat(databasePath); err != nil {
		return fmt.Errorf("the Manager store cannot be migrated: %w", err)
	}
	// The lock is taken before the proof. A live writer holding it means the
	// store is in use, and that is the answer regardless of what a snapshot of a
	// moving store would have said: proving first could report the source as
	// changed while the real condition is upgrade_store_locked.
	lock, err := processlock.Acquire(databasePath)
	if err != nil {
		if errors.Is(err, processlock.ErrLocked) {
			return newUpgradeError(UpgradeErrorStoreLocked,
				"the v2 Manager still holds its database process lock, so it has not been stopped; stop the v2 stack and run `upgrade migrate` again", err)
		}
		return fmt.Errorf("take the v2 database process lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := requireCopiedInStore(ctx, databasePath); err != nil {
		return err
	}
	db, err := managersqlite.Open(databasePath)
	if err != nil {
		return fmt.Errorf("open the Manager store writable to migrate it: %w", err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close the migrated Manager store: %w", err)
	}
	return nil
}

// requireCopiedInStore proves from a private snapshot that this store is the
// product of an Upgrade copy-in. The snapshot is a copy, so a store this build
// must not open still answers the question with its bytes unchanged.
func requireCopiedInStore(ctx context.Context, databasePath string) error {
	// The proof reads a private copy of the database, so it must not write that
	// copy back into the Manager data root it is measuring. A default TMPDIR
	// inside that volume is refused before a directory is made.
	if err := ValidateScratchDir("", "", filepath.Dir(databasePath)); err != nil {
		return newUpgradeError(UpgradeErrorSnapshotFailed,
			"the private snapshot of the Manager store would be created inside the Manager data root; point TMPDIR at a directory on a separate volume and run `upgrade migrate` again", err)
	}
	snapshot, err := OpenSnapshot(ctx, databasePath, "")
	if err != nil {
		return fmt.Errorf("read the Manager store before migrating it: %w", err)
	}
	defer func() { _ = snapshot.Close() }()
	copiedIn, err := recordsCopyIn(ctx, snapshot.DB())
	if err != nil {
		return fmt.Errorf("read the recorded Upgrade operation: %w", err)
	}
	if !copiedIn {
		return newUpgradeError(UpgradeErrorMigrateNotCopiedIn,
			"this Manager store carries no recorded Upgrade copy-in, so it is not the store this command serves: opening it writable would migrate a 1.x or native store into one this build serves as native without the Upgrade admission. Copy the 1.x source in with `upgrade run`, or keep using the Manager build that wrote this store", nil)
	}
	return nil
}

// recordsCopyIn reports whether the store holds the row the copy-in records: a
// copied-in source kind, the source-copy domain, both fingerprints and the
// backup manifest. Only columns every version of the table has are read, so the
// proof also works on the older schema this command exists to migrate.
func recordsCopyIn(ctx context.Context, db *sql.DB) (bool, error) {
	present, err := tablePresent(ctx, db, "upgrade_operations")
	if err != nil || !present {
		return false, err
	}
	var stage, sourceKind, domains, before, after, manifest sql.NullString
	err = db.QueryRowContext(ctx, `select stage, source_kind, domains_done,
		source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256
		from upgrade_operations where id = 1`).
		Scan(&stage, &sourceKind, &domains, &before, &after, &manifest)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	recordedStage := model.UpgradeStage(stage.String)
	if !recordedStage.RollbackApplicable() {
		return false, nil
	}
	if sourceKind.String != SourceKindUpgradedLegacy {
		return false, nil
	}
	if strings.TrimSpace(before.String) == "" || strings.TrimSpace(after.String) == "" ||
		strings.TrimSpace(manifest.String) == "" {
		return false, nil
	}
	var completed []string
	if err := json.Unmarshal([]byte(domains.String), &completed); err != nil {
		return false, err
	}
	for _, domain := range completed {
		if domain == model.UpgradeDomainSourceCopy {
			return true, nil
		}
	}
	return false, nil
}

// ReadRecordFrom reads the single recorded operation from an already-open
// read-only handle. It maps the two shapes a read-only reader cannot repair to
// the shared store-schema code, so every reader reports the same thing instead
// of surfacing raw SQL or a malformed-row error.
func ReadRecordFrom(ctx context.Context, db *sql.DB) (model.UpgradeOperation, bool, error) {
	present, err := tablePresent(ctx, db, "upgrade_operations")
	if err != nil || !present {
		return model.UpgradeOperation{}, false, err
	}
	repository := upgradeoperation.New(db)
	// A read-only open cannot migrate, and the columns a rollback records were
	// added by a later build. Saying so is the operator's next step; letting the
	// select fail would surface raw SQL instead.
	missing, err := repository.MissingColumns(ctx)
	if err != nil {
		return model.UpgradeOperation{}, false, err
	}
	if len(missing) > 0 {
		return model.UpgradeOperation{}, false, newUpgradeError(UpgradeErrorStoreSchemaOutdated,
			fmt.Sprintf("this Manager store predates the current build's upgrade table (missing %s), so its operation cannot be read read-only; stop the v2 Manager and run `cpa-manager-plus upgrade migrate`, which opens the store writable under the v2 process lock and adds them (it refuses a store that carries no recorded copy-in)",
				strings.Join(missing, ", ")), nil)
	}
	operation, found, err := repository.Get(ctx)
	// A rolled_back row whose observation did not survive was written by a build
	// that did not record the two together. No path of this build writes one and
	// no open repairs one, so the store is the outdated thing here: the operator
	// is told the record cannot be read and which record they can decide against.
	if errors.Is(err, upgradeoperation.ErrRollbackEvidenceMissing) {
		return model.UpgradeOperation{}, false, newUpgradeError(UpgradeErrorStoreSchemaOutdated,
			"this Manager store records a rolled_back operation without the observation that justified it, which this build refuses on every write path, so the operation cannot be read as recorded and opening the store does not repair it; copy the 1.x source into a new v2 data root with `upgrade run` and decide against that record", nil)
	}
	return operation, found, err
}

type targetPaths struct {
	Root     string
	Database string
	DataKey  string
	Archives string
	Work     string
	Staging  string
}

// TargetLayout is the v2 data root's member layout: the exact paths the copy-in
// writes and the Manager service reads. It is exported so the CLI can refuse a
// configured layout that would put a copied member where nothing reads it.
type TargetLayout struct {
	Root     string
	Database string
	DataKey  string
	Archives string
	Work     string
}

// LayoutForRoot is the canonical v2 layout of a data root.
func LayoutForRoot(root string) TargetLayout {
	paths := newTargetPaths(root)
	return TargetLayout{
		Root:     paths.Root,
		Database: paths.Database,
		DataKey:  paths.DataKey,
		Archives: paths.Archives,
		Work:     paths.Work,
	}
}

// SamePath reports whether two paths name the same location. Symlinks in the
// deepest existing ancestor are resolved, so a mounted path and its canonical
// alias agree.
func SamePath(left, right string) bool {
	return canonicalPath(left) == canonicalPath(right)
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
	if rootsOverlap(sourceRoot, targetRoot) {
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

// acquireTargetFence takes the v2 data root's ownership lock, the same marker a
// running Manager holds. It creates the marker (and the data root) when they do
// not exist yet, which is the copy-in's own first write to the target.
//
// A held lock is a typed refusal, not a generic environment failure: another
// Manager process is serving or writing this store, and copying into it would
// make two writers disagree about the same bytes. The message names no host
// path; the code and exit status are what an operator acts on.
func acquireTargetFence(targetRoot string) (*processlock.Lock, error) {
	lock, err := processlock.Acquire(filepath.Join(targetRoot, databaseFileName))
	if err == nil {
		return lock, nil
	}
	if errors.Is(err, processlock.ErrLocked) {
		return nil, newUpgradeError(UpgradeErrorV2Running,
			"the v2 data root is owned by a running Manager process; stop it before the copy-in", nil)
	}
	return nil, fmt.Errorf("take the v2 data root ownership lock: %w", err)
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
	result.SourceRoot = recorded.SourceRoot
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

// copyInInFlight reports whether this v2 data root carries the provenance
// marker of an interrupted copy-in. An unreadable marker is refused rather than
// ignored: it is the only evidence that the store beside it is unfinished.
func copyInInFlight(paths targetPaths) (bool, error) {
	_, found, err := copyinmarker.Read(paths.Root)
	if err != nil {
		return false, unresumable("the copy-in marker in this v2 data root is unreadable", err)
	}
	return found, nil
}

// materializeTarget moves the restored members into the v2 layout. It reports
// whether this run moved bytes into the layout and exactly which target paths it
// moved, so an abort can roll back only its own work.
//
// Nothing is deleted, restored or moved before the target has been checked, and
// nothing is checked by opening the database: the marker phase says how far an
// earlier run got, and every member comparison is a file digest. An already
// materialized target is therefore never restored a second time and never read
// with a WAL-sensitive open.
func materializeTarget(ctx context.Context, paths targetPaths, bundleDir string, manifest maintenancebundle.Manifest, digest string, provenance Result) (bool, []string, error) {
	if entry, ok := manifest.Entries["config"]; ok && entry.Present {
		return false, nil, errors.New("the backup carries a source config member the v2 data root does not accept")
	}
	marker, found, err := copyinmarker.Read(paths.Root)
	if err != nil {
		return false, nil, unresumable("the copy-in marker in this v2 data root is unreadable", err)
	}
	if found {
		if err := markerDescribesTarget(marker, paths, digest); err != nil {
			return false, nil, err
		}
		switch marker.Phase {
		case copyinmarker.PhaseMigrating:
			materialized, err := targetMaterialized(paths, manifest, marker)
			if err != nil {
				return false, nil, err
			}
			if !materialized {
				// The marker vouches for this target and says every member was
				// moved, but a member the migration never touches is gone or
				// changed. Restoring over it would adopt evidence of a copy that
				// never happened, and clearing the work directory would delete
				// the only proof the store is unfinished.
				return false, nil, unresumable("the copy-in marker describes a materialized target, but the v2 data root no longer holds the members it moved; refusing to restore over it", nil)
			}
			return false, nil, nil
		case copyinmarker.PhaseMaterializing:
			// The run died while it was moving members in. The staging tree it
			// restored is the source of the rest; the move itself is idempotent
			// and refuses anything already in place.
		default:
			return false, nil, unresumable(fmt.Sprintf("the copy-in marker phase %q is not supported", marker.Phase), nil)
		}
	} else {
		matching, err := targetMatches(paths, manifest)
		if err != nil {
			return false, nil, err
		}
		if matching {
			// A target that already holds the backup is not restored again, but
			// it must hold nothing else: an extra archive entry is not part of
			// the copy.
			if err := refuseExtraTargetEntries(paths, manifest); err != nil {
				return false, nil, err
			}
			return false, nil, nil
		}
		// Every member this run would replace must be absent first. The check
		// runs before the marker is published and before any byte is written, so
		// a conflict can never delete the evidence of an earlier run.
		if err := refuseMemberConflicts(paths, manifest); err != nil {
			return false, nil, err
		}
		// The marker is published before the first member is moved: from here on
		// a process death must be recognizable without opening the store.
		if err := writeCopyInMarker(paths, digest, provenance, copyinmarker.PhaseMaterializing); err != nil {
			return false, nil, err
		}
	}
	if err := prepareStaging(ctx, paths, bundleDir, manifest); err != nil {
		return false, nil, err
	}
	moved, err := moveMembers(ctx, paths, paths.Staging, manifest)
	if err != nil {
		return false, moved, err
	}
	// Every member is in place. The marker moves to its migrating phase before
	// the migration rewrites the database, so a resume adopts this target
	// instead of restoring over it.
	if err := writeCopyInMarker(paths, digest, provenance, copyinmarker.PhaseMigrating); err != nil {
		return false, moved, err
	}
	return len(moved) > 0, moved, nil
}

// markerDescribesTarget refuses a marker that does not describe this copy-in.
//
// The marker lives in this data root's reserved work directory, so it belongs to
// this root: a relocated mount changes the absolute path it recorded, not the
// store it describes. What it may not do is name a different database file or a
// different backup — that marker is evidence for some other run, and treating
// its absence as permission to restore would be guessing.
func markerDescribesTarget(marker copyinmarker.Marker, paths targetPaths, digest string) error {
	if filepath.Base(marker.TargetDatabase) != filepath.Base(paths.Database) {
		return unresumable("the copy-in marker in this v2 data root describes another database", nil)
	}
	if marker.BackupManifestSHA256 != digest {
		return unresumable("the copy-in marker in this v2 data root describes a different backup", nil)
	}
	return nil
}

// refuseMemberConflicts refuses to materialize over a file this copy-in did not
// place. A member the backup describes must be absent or already byte-identical;
// the database sidecars are compared too, because before the marker exists no
// run of this copy-in could have rewritten them.
func refuseMemberConflicts(paths targetPaths, manifest maintenancebundle.Manifest) error {
	for _, member := range copyInMembers {
		entry, ok := manifest.Entries[member.bundle]
		if !ok || !entry.Present {
			continue
		}
		if _, err := os.Lstat(filepath.Join(paths.Root, member.target)); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return fmt.Errorf("inspect the v2 data root %s: %w", member.target, err)
		}
		matching, err := targetMemberMatches(paths.Root, manifest, member.bundle)
		if err != nil {
			return err
		}
		if !matching {
			return unresumable(fmt.Sprintf("the v2 data root already holds %s, which this backup does not describe", member.target), nil)
		}
	}
	return nil
}

// prepareStaging makes a verified staging tree available for the move. A
// published staging tree that still describes this backup is the resume
// evidence of an interrupted run and is reused; anything else in the work
// directory is this run's own scratch and is replaced. The provenance marker is
// never one of them.
func prepareStaging(ctx context.Context, paths targetPaths, bundleDir string, manifest maintenancebundle.Manifest) error {
	if info, err := os.Lstat(paths.Staging); err == nil && info.IsDir() {
		if staged, verifyErr := maintenancebundle.Verify(ctx, paths.Staging); verifyErr == nil &&
			reflect.DeepEqual(staged.Entries, manifest.Entries) {
			return nil
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect the copy-in staging tree: %w", err)
	}
	if err := clearWorkDirectory(paths); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.Work, 0o700); err != nil {
		return fmt.Errorf("create the copy-in work directory: %w", err)
	}
	if _, err := maintenancebundle.RestoreNew(ctx, bundleDir, paths.Staging); err != nil {
		return fmt.Errorf("restore the backup into the v2 data root: %w", err)
	}
	return nil
}

// clearWorkDirectory removes everything in the reserved work directory except
// the provenance marker.
func clearWorkDirectory(paths targetPaths) error {
	listing, err := os.ReadDir(paths.Work)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect the copy-in work directory: %w", err)
	}
	for _, entry := range listing {
		if entry.Name() == copyinmarker.FileName {
			continue
		}
		if err := os.RemoveAll(filepath.Join(paths.Work, entry.Name())); err != nil {
			return fmt.Errorf("clear the copy-in work directory: %w", err)
		}
	}
	return nil
}

// targetMaterialized reports whether the v2 layout holds the members an
// interrupted run moved into place.
//
// The database and its sidecars are deliberately not compared with the backup:
// the incremental migration rewrites them in place, and a process killed inside
// that rewrite leaves neither the backup bytes nor the finished v2 schema. The
// database file identity, by contrast, is comparable: the migration rewrites the
// file it found and the rename that placed it kept its inode, so a different
// identity means the target holds a database this copy-in never moved. The
// members the migration never touches must still match the backup, so a data key
// or archive tree that moved is still refused.
func targetMaterialized(paths targetPaths, manifest maintenancebundle.Manifest, marker copyinmarker.Marker) (bool, error) {
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
	identity, err := copyinmarker.Identity(paths.Database)
	if err != nil {
		return false, err
	}
	if identity != marker.TargetDatabaseIdentity {
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

// writeCopyInMarker publishes the provenance of this copy-in inside the work
// directory. It is written before the first target member is moved, republished
// with the identity of the database it moved and the source fingerprint the copy
// kept, and removed only after the operation row commits.
func writeCopyInMarker(paths targetPaths, digest string, result Result, phase string) error {
	marker := copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   phase,
		TargetDatabase:          paths.Database,
		SourceKind:              result.SourceKind,
		SourceVersion:           result.SourceVersion,
		SourceFingerprintBefore: result.SourceFingerprintBefore,
		SourceFingerprintAfter:  result.SourceFingerprintAfter,
		BackupManifestSHA256:    digest,
		BackupDirectory:         result.BackupDirectory,
		WrittenAtMS:             time.Now().UnixMilli(),
	}
	if phase == copyinmarker.PhaseMigrating {
		identity, err := copyinmarker.Identity(paths.Database)
		if err != nil {
			return fmt.Errorf("read the identity of the copied database: %w", err)
		}
		marker.TargetDatabaseIdentity = identity
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
// stage and only re-records the observations. alreadyRecorded reports that the
// row already held this copy-in before this call, which is how a rerun that
// found its own surviving marker recognizes the recorded outcome.
func migrateAndRecord(ctx context.Context, paths targetPaths, result Result) (model.UpgradeOperation, bool, error) {
	db, err := managersqlite.Open(paths.Database)
	if err != nil {
		return model.UpgradeOperation{}, false, fmt.Errorf("open the copied v2 database: %w", err)
	}
	defer func() { _ = db.Close() }()
	repository := upgradeoperation.New(db)
	recorded, found, err := repository.Get(ctx)
	if err != nil {
		return model.UpgradeOperation{}, false, err
	}
	alreadyRecorded := found && copyInRecorded(recorded)
	if !found {
		recorded = model.UpgradeOperation{}
	}
	recorded.SourceKind = SourceKindUpgradedLegacy
	recorded.SourceVersion = result.SourceVersion
	recorded.SourceRoot = result.SourceRoot
	recorded.SourceFingerprintBefore = result.SourceFingerprintBefore
	recorded.SourceFingerprintAfter = result.SourceFingerprintAfter
	recorded.BackupManifestSHA256 = result.BackupManifestSHA256
	switch recorded.Stage {
	case "", model.UpgradeStagePreflight, model.UpgradeStageFailed, model.UpgradeStageCopyIn:
		recorded.Stage = model.UpgradeStageCopyIn
	}
	recorded.MarkDomain(model.UpgradeDomainSourceCopy)
	if err := repository.Put(ctx, recorded); err != nil {
		return model.UpgradeOperation{}, false, fmt.Errorf("record the copy_in stage: %w", err)
	}
	// The repository owns created_at_ms and updated_at_ms: it discards the
	// caller's proposal, and on the first write the returned struct still holds
	// no timestamp at all. Reporting this run's result from the local struct
	// would then show updated_at_ms=0 while the durable row holds the time the
	// write happened, so the persisted row is read back and is the result.
	persisted, found, err := repository.Get(ctx)
	if err != nil {
		return model.UpgradeOperation{}, false, fmt.Errorf("read the recorded copy_in stage: %w", err)
	}
	if !found {
		return model.UpgradeOperation{}, false, errors.New("the copy_in stage is missing from the v2 database after it was recorded")
	}
	return persisted, alreadyRecorded, nil
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
