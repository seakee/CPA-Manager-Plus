package upgrade

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

// CopyIn restores a verified backup into private staging, moves only the
// non-database members, then migrates and records the staged database. A synced,
// sidecar-free database is published by one same-filesystem rename. Before that
// commit point the target database must remain absent; a resume discards staging
// and starts again from the verified bundle, never inferring target provenance.
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
	if err := validateCopyInWork(paths, false); err != nil {
		return Result{}, err
	}
	result := Result{
		SourceKind:      SourceKindUpgradedLegacy,
		SourceRoot:      sourceRoot,
		BackupDirectory: bundleDir,
		TargetDatabase:  paths.Database,
		TargetDataKey:   paths.DataKey,
		TargetArchives:  paths.Archives,
		DomainsDone:     []string{},
	}
	// The marker is the first read. Only an atomically published row can prove
	// a target complete; no unrecorded target is migrated or adopted.
	inFlight, err := copyInInFlight(paths)
	if err != nil {
		return Result{}, err
	}
	if inFlight {
		marker, found, err := copyinmarker.Read(paths.Root)
		if err := copyinmarker.DatabaseSidecarsAbsent(paths.Database); err != nil {
			return Result{}, unresumable("the unfinished copy-in target has a database sidecar; preserve the target and marker, move the foreign database and sidecars aside, then run `cpa-manager-plus upgrade run`", err)
		}
		if err != nil || !found {
			return Result{}, unresumable("the copy-in marker changed while resuming; preserve the data root and recover from the verified bundle", err)
		}
		if err := markerDescribesTarget(marker, paths, marker.BackupManifestSHA256); err != nil {
			return Result{}, err
		}
		if _, err := os.Lstat(paths.Database); err == nil {
			if marker.Phase != copyinmarker.PhaseCommitting {
				return Result{}, unresumable("a materializing copy-in cannot have a published target; preserve the target and marker and recover into an empty v2 data root", nil)
			}
			if err := requirePublishedCopyIn(paths); err != nil {
				return Result{}, err
			}
			recorded, found, snapshot, readErr := readCopyInSnapshot(ctx, paths.Database, sourceRoot, targetRoot, options.ScratchDir)
			if snapshot != nil {
				defer func() { _ = snapshot.Close() }()
			}
			if readErr != nil {
				return Result{}, copyInCandidateReadError(ctx, readErr)
			}
			if !found || recorded.BackupManifestSHA256 != marker.BackupManifestSHA256 ||
				recorded.SourceKind != marker.SourceKind || recorded.SourceFingerprintBefore != marker.SourceFingerprintBefore ||
				recorded.SourceFingerprintAfter != marker.SourceFingerprintAfter {
				return Result{}, unresumable("the target database has no committed row for this copy-in; preserve the target and marker, move unexpected database members out of this data root, then run `cpa-manager-plus upgrade run`", nil)
			}
			completed, err := completedResult(ctx, paths, bundleDir, recorded, result, snapshot.DB())
			if err != nil {
				return Result{}, err
			}
			if err := runCheckpoint(options, "completed-verified"); err != nil {
				return Result{}, err
			}
			if err := cleanupPublishedCopyIn(paths); err != nil {
				return Result{}, err
			}
			return completed, nil
		} else if !os.IsNotExist(err) {
			return Result{}, err
		}
	} else {
		recorded, found, snapshot, err := readCopyInCandidate(ctx, paths.Database, sourceRoot, targetRoot, options.ScratchDir)
		if snapshot != nil {
			defer func() { _ = snapshot.Close() }()
		}
		if err != nil {
			return Result{}, copyInCandidateReadError(ctx, err)
		}
		if found {
			completed, err := completedResult(ctx, paths, bundleDir, recorded, result, snapshot.DB())
			if err != nil {
				return Result{}, err
			}
			return completed, nil
		}
	}

	before, err := fingerprintSource(ctx, sourceRoot)
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
	after, err := fingerprintSource(ctx, sourceRoot)
	if err != nil {
		return Result{}, fmt.Errorf("fingerprint the 1.x source after the copy: %w", err)
	}
	if after != before {
		aborted := newUpgradeError(UpgradeErrorSourceChanged,
			"the 1.x source changed during the copy-in; the members this run moved were rolled back and nothing was recorded", nil)
		if rollbackErr := rollbackMaterialized(ctx, paths, moved, manifest); rollbackErr != nil {
			return Result{}, unresumable("the source changed and materialized members could not be safely removed; preserve the target, marker and verified bundle for manual recovery", rollbackErr)
		}
		return Result{}, aborted
	}
	result.SourceFingerprintAfter = after.Digest

	stagedDatabase := filepath.Join(paths.Staging, "database")
	recorded, alreadyRecorded, err := migrateAndRecord(ctx, stagedDatabase, result)
	if err != nil {
		return Result{}, err
	}
	if err := writeCopyInMarker(paths, digest, result, copyinmarker.PhaseCommitting); err != nil {
		return Result{}, err
	}
	if err := runCheckpoint(options, "before-rename"); err != nil {
		return Result{}, err
	}
	if err := refuseTargetDatabase(paths); err != nil {
		return Result{}, err
	}
	if err := runCheckpoint(options, "rename-checked"); err != nil {
		return Result{}, err
	}
	if err := validateStagedDatabase(paths, stagedDatabase, true); err != nil {
		return Result{}, err
	}
	if err := copyinmarker.DatabaseSidecarsAbsent(paths.Database); err != nil {
		return Result{}, unresumable("an unexpected database sidecar arrived before publication; preserve the target and marker for recovery", err)
	}
	if err := publishDatabase(stagedDatabase, paths.Database); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Result{}, unresumable("an unexpected database arrived before publication; preserve the existing target and marker for recovery", err)
		}
		return Result{}, fmt.Errorf("publish the staged database without replacement: %w", err)
	}
	if err := copyinmarker.SyncDirectory(paths.Root); err != nil {
		return Result{}, fmt.Errorf("sync the committed data root: %w", err)
	}
	if err := requirePublishedCopyIn(paths); err != nil {
		return Result{}, err
	}
	if err := runCheckpoint(options, "recorded"); err != nil {
		return Result{}, err
	}
	if err := cleanupPublishedCopyIn(paths); err != nil {
		return Result{}, fmt.Errorf("remove the copy-in work directory: %w", err)
	}
	result.AlreadyComplete = alreadyRecorded
	result.Stage = recorded.Stage
	result.DomainsDone = recorded.DomainsDone
	result.UpdatedAtMS = recorded.UpdatedAtMS
	return result, nil
}

// ReadRecord reads the single Upgrade operation through a private snapshot.
// It never opens the target in place, joins its WAL, migrates it, or creates a
// target sidecar. A missing operation table is reported as found=false.
func ReadRecord(ctx context.Context, databasePath string) (model.UpgradeOperation, bool, error) {
	if err := ctx.Err(); err != nil {
		return model.UpgradeOperation{}, false, err
	}
	if _, err := os.Lstat(databasePath); err != nil {
		return model.UpgradeOperation{}, false, err
	}
	if err := ValidateScratchDir("", "", filepath.Dir(databasePath)); err != nil {
		return model.UpgradeOperation{}, false, snapshotReadError(ctx, err)
	}
	recorded, found, err := ReadRecordFromSnapshot(ctx, databasePath, "")
	return recorded, found, snapshotReadError(ctx, err)
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
	if err := requireRecoveredCopyIn(databasePath); err != nil {
		return err
	}
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

// StoreSchemaOutdatedError reports a Manager store whose upgrade table predates
// the columns this build selects. A read-only reader cannot migrate it, so the
// operator has to open it once with this build instead of meeting raw SQL.
func StoreSchemaOutdatedError(missing []string) error {
	return newUpgradeError(UpgradeErrorStoreSchemaOutdated,
		fmt.Sprintf("this Manager store predates the current build's upgrade table (missing %s), so its operation cannot be read without a writable open; open it once with this build before reading it read-only",
			strings.Join(missing, ", ")), nil)
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

// RefuseOverlappingRoots refuses a 1.x source, a v2 data root and a backup
// destination where one contains another. It resolves the three the way
// Reconcile does but reads no content and creates nothing, so a caller can
// refuse a mistaken target before it takes a fence or materializes a directory
// and marker inside the source tree it promises not to touch.
func RefuseOverlappingRoots(sourceRoot, targetRoot, bundleDir string) error {
	absoluteSource, err := filepath.Abs(sourceRoot)
	if err != nil {
		return fmt.Errorf("resolve the 1.x source root: %w", err)
	}
	absoluteTarget, err := filepath.Abs(targetRoot)
	if err != nil {
		return fmt.Errorf("resolve the v2 data root: %w", err)
	}
	absoluteBundle, err := filepath.Abs(bundleDir)
	if err != nil {
		return fmt.Errorf("resolve the backup destination: %w", err)
	}
	return rejectOverlap(canonicalPath(absoluteSource), canonicalPath(absoluteTarget), canonicalPath(absoluteBundle))
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
	return requireLock(lock, databasePath, "1.x source database")
}

// requireLock proves the caller's held fence belongs to this database and is
// still valid, so a lock taken for another path can never authorize a write.
// Every offline writer holds a processlock on the store it writes; the role
// names which one, because the same fence guards the 1.x source and the v2 data
// root in different steps.
func requireLock(lock *processlock.Lock, databasePath, role string) error {
	resolved := databasePath
	if dir, err := filepath.EvalSymlinks(filepath.Dir(databasePath)); err == nil {
		resolved = filepath.Join(dir, filepath.Base(databasePath))
	}
	if filepath.Clean(resolved) != filepath.Clean(lock.DatabasePath()) {
		return fmt.Errorf("the held ownership lock does not belong to the %s", role)
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
	if err := requireRecoveredCopyIn(databasePath); err != nil {
		return model.UpgradeOperation{}, false, err
	}
	if _, err := os.Lstat(databasePath); os.IsNotExist(err) {
		return model.UpgradeOperation{}, false, nil
	} else if err != nil {
		return model.UpgradeOperation{}, false, err
	}
	recorded, found, err := ReadRecord(ctx, databasePath)
	if err != nil || !found {
		return model.UpgradeOperation{}, false, err
	}
	if !copyInRecorded(recorded) {
		return model.UpgradeOperation{}, false, nil
	}
	return recorded, true, nil
}

// readCopyInCandidate opens only a private snapshot outside both roots, even
// for a sidecar-free candidate. On a recorded copy the caller owns the snapshot
// and uses that same database for schema verification before closing it.
func readCopyInCandidate(ctx context.Context, databasePath, sourceRoot, targetRoot, scratchDir string) (model.UpgradeOperation, bool, *Snapshot, error) {
	if err := requireRecoveredCopyIn(databasePath); err != nil {
		return model.UpgradeOperation{}, false, nil, err
	}
	return readCopyInSnapshot(ctx, databasePath, sourceRoot, targetRoot, scratchDir)
}

// CopyIn alone may inspect a surviving committing marker's published database,
// after its stricter marker binding and sidecar-free publication checks.
func readCopyInSnapshot(ctx context.Context, databasePath, sourceRoot, targetRoot, scratchDir string) (model.UpgradeOperation, bool, *Snapshot, error) {
	if err := requireCandidateSidecars(databasePath); err != nil {
		return model.UpgradeOperation{}, false, nil, err
	}
	info, err := os.Lstat(databasePath)
	if os.IsNotExist(err) {
		return model.UpgradeOperation{}, false, nil, nil
	}
	if err != nil {
		return model.UpgradeOperation{}, false, nil, err
	}
	if !info.Mode().IsRegular() {
		return model.UpgradeOperation{}, false, nil, unresumable("the target database is not a regular file; preserve it for manual recovery", nil)
	}
	if err := ValidateScratchDir(scratchDir, sourceRoot, targetRoot); err != nil {
		return model.UpgradeOperation{}, false, nil, err
	}
	snapshot, err := OpenSnapshot(ctx, databasePath, scratchDir)
	if err != nil {
		return model.UpgradeOperation{}, false, nil, err
	}
	recorded, found, err := ReadRecordFrom(ctx, snapshot.DB())
	if err != nil || !found || !copyInRecorded(recorded) {
		return recorded, false, nil, errors.Join(err, snapshot.Close())
	}
	return recorded, true, snapshot, nil
}

func copyInCandidateReadError(ctx context.Context, err error) error {
	err = snapshotReadError(ctx, err)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var upgradeErr *UpgradeError
	if errors.As(err, &upgradeErr) {
		return err
	}
	return unresumable("the target database could not be inspected; preserve the target and marker and recover from the verified bundle into an empty v2 data root", err)
}

// copyInRecorded reports whether the row already holds this copy-in. A failed
// or preflight-only stage is not a completed copy, so a retry resumes it.
func copyInRecorded(recorded model.UpgradeOperation) bool {
	if !recorded.HasDomain(model.UpgradeDomainSourceCopy) {
		return false
	}
	position := recorded.Stage
	if position == model.UpgradeStageFailed {
		position = recorded.HighWater()
	}
	switch position {
	case "", model.UpgradeStagePreflight, model.UpgradeStageFailed:
		return false
	}
	return model.UpgradeStageCopyIn.CanAdvanceTo(position)
}

// completedResult returns the stored outcome of a recorded copy-in, but only
// after proving the v2 layout still holds what the record says was copied. A
// recorded row is not evidence by itself: the bytes the operator will keep
// using are what has to match.
func completedResult(ctx context.Context, paths targetPaths, bundleDir string, recorded model.UpgradeOperation, result Result, database *sql.DB) (Result, error) {
	if err := verifyCompletedMembers(ctx, paths, bundleDir, recorded); err != nil {
		return Result{}, err
	}
	if _, err := copiedDatabaseSchema(ctx, database); err != nil {
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

// copiedDatabaseSchema qualifies the database already opened from a private
// snapshot. The row, schema and subsequent observations share that one copy.
func copiedDatabaseSchema(ctx context.Context, database *sql.DB) (sourceSchema, error) {
	if err := quickCheckSource(ctx, database); err != nil {
		return sourceSchema{}, err
	}
	schema, err := readSourceSchema(ctx, database)
	if err != nil {
		return sourceSchema{}, err
	}
	if schema.sourceKind() != SourceKindNativeV2 {
		return sourceSchema{}, newUpgradeError(UpgradeErrorTargetModified,
			"the v2 database is missing or is not the migrated store the copy-in recorded", nil)
	}
	return schema, nil
}

// verifyCompletedMembers checks the backup binding and non-database members.
// Completed recovery supplies its one private snapshot separately, so the row
// and schema always come from the same observed database bytes.
func verifyCompletedMembers(ctx context.Context, paths targetPaths, bundleDir string, recorded model.UpgradeOperation) error {
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
	if err := requireTargetMember(ctx, paths.Root, manifest, "data-key", "the v2 data key does not match the backup"); err != nil {
		return err
	}
	if err := requireTargetMember(ctx, paths.Root, manifest, archivesDirName, "the v2 archive tree does not match the backup"); err != nil {
		return err
	}
	return refuseExtraTargetEntries(paths, manifest)
}

// verifyRecordedBackup re-verifies the backup and returns its published
// manifest digest. The backup is the only reference for every target digest, so
// an unverifiable one leaves nothing to compare against.
func verifyRecordedBackup(ctx context.Context, bundleDir string) (maintenancebundle.Manifest, string, error) {
	manifest, err := maintenancebundle.Verify(ctx, bundleDir)
	if err != nil {
		return maintenancebundle.Manifest{}, "", fmt.Errorf("the backup that made the copy eligible does not verify: %w", err)
	}
	digest, err := manifestDigest(ctx, bundleDir)
	if err != nil {
		return maintenancebundle.Manifest{}, "", fmt.Errorf("read the backup manifest digest: %w", err)
	}
	return manifest, digest, nil
}

// requireTargetMember refuses a member the backup describes but the v2 layout
// does not hold, or holds with different bytes.
func requireTargetMember(ctx context.Context, root string, manifest maintenancebundle.Manifest, member, detail string) error {
	matching, err := targetMemberMatches(ctx, root, manifest, member)
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
		digest, err := manifestDigest(ctx, bundleDir)
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
	digest, err := manifestDigest(ctx, bundleDir)
	return verified, digest, err
}

func manifestDigest(ctx context.Context, bundleDir string) (string, error) {
	return fileContentDigest(ctx, filepath.Join(bundleDir, "manifest.json"))
}

// copyInInFlight reports whether this v2 data root carries the provenance
// marker of an interrupted copy-in. An unreadable marker is refused rather than
// ignored: it is the only evidence that the store beside it is unfinished.
func copyInInFlight(paths targetPaths) (bool, error) {
	_, found, err := copyinmarker.Read(paths.Root)
	if err != nil {
		return false, unresumable("the copy-in marker is unreadable or uses an older version or unknown phase; preserve the data root and verified bundle, recover into an empty v2 data root with `cpa-manager-plus upgrade run`", err)
	}
	return found, nil
}

// materializeTarget always restores fresh staging from the verified bundle.
// Only non-database members move before the database commit point.
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
	} else {
		if err := refuseTargetDatabase(paths); err != nil {
			return false, nil, err
		}
		if err := refuseMemberConflicts(ctx, paths, manifest); err != nil {
			return false, nil, err
		}
		// The marker is published before the first member is moved: from here on
		// a process death must be recognizable without opening the store.
		if err := writeCopyInMarker(paths, digest, provenance, copyinmarker.PhaseMaterializing); err != nil {
			return false, nil, err
		}
	}
	if err := refuseTargetDatabase(paths); err != nil {
		return false, nil, err
	}
	if err := refuseMemberConflicts(ctx, paths, manifest); err != nil {
		return false, nil, err
	}
	if err := refuseExtraTargetEntries(paths, manifest); err != nil {
		return false, nil, err
	}
	if err := prepareStaging(ctx, paths, bundleDir); err != nil {
		return false, nil, err
	}
	moved, err := moveMembers(ctx, paths, paths.Staging, manifest)
	return len(moved) > 0, moved, err
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

// refuseTargetDatabase checks all four paths, including sidecars absent from
// the backup. No unrecorded target database is eligible for adoption.
func refuseTargetDatabase(paths targetPaths) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(paths.Database + suffix); err == nil {
			return unresumable("the v2 data root already holds a database or sidecar without this copy-in's committed row; preserve the marker and move the foreign database and sidecars aside before running `cpa-manager-plus upgrade run`", nil)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// refuseMemberConflicts checks non-database members before changing staging.
// Missing members can be moved from fresh staging; existing ones must match.
func refuseMemberConflicts(ctx context.Context, paths targetPaths, manifest maintenancebundle.Manifest) error {
	for _, member := range copyInMembers {
		entry, described := manifest.Entries[member.bundle]
		if _, err := os.Lstat(filepath.Join(paths.Root, member.target)); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return fmt.Errorf("inspect the v2 data root %s: %w", member.target, err)
		}
		if !described || !entry.Present {
			return unresumable(fmt.Sprintf("the v2 data root holds %s, absent from the backup", member.target), nil)
		}
		matching, err := targetMemberMatches(ctx, paths.Root, manifest, member.bundle)
		if err != nil {
			return err
		}
		if !matching {
			return unresumable(fmt.Sprintf("the v2 data root already holds %s, which this backup does not describe", member.target), nil)
		}
	}
	return nil
}

// prepareStaging never trusts a previous migration's staged bytes or sidecars.
func prepareStaging(ctx context.Context, paths targetPaths, bundleDir string) error {
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

// validateCopyInWork checks every ancestor owned by copy-in before staging
// access or cleanup. A linked directory is evidence to preserve, not follow.
func validateCopyInWork(paths targetPaths, requireStaging bool) error {
	if !within(paths.Root, paths.Work) || !within(paths.Work, paths.Staging) {
		return unresumable("the copy-in work paths escape the v2 data root", nil)
	}
	if err := copyinmarker.ValidateDirectory(paths.Root); err != nil {
		return unresumable("the copy-in work directory is not safe; preserve it and recover into an empty v2 data root", err)
	}
	info, err := os.Lstat(paths.Staging)
	if os.IsNotExist(err) && !requireStaging {
		return nil
	}
	if err != nil {
		return unresumable("inspect the copy-in staging directory", err)
	}
	if !info.IsDir() {
		return unresumable("the copy-in staging path must be a real directory, not a symbolic link or special file", nil)
	}
	return nil
}

func validateStagedDatabase(paths targetPaths, databasePath string, closed bool) error {
	if err := validateCopyInWork(paths, true); err != nil {
		return err
	}
	if databasePath != filepath.Join(paths.Staging, "database") {
		return unresumable("the staged database is outside the copy-in staging directory", nil)
	}
	info, err := os.Lstat(databasePath)
	if err != nil {
		return unresumable("inspect the staged database", err)
	}
	if !info.Mode().IsRegular() {
		return unresumable("the staged database must be a regular file, not a symbolic link or special file", nil)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		info, err := os.Lstat(databasePath + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return unresumable("inspect the staged database sidecars", err)
		}
		if closed || !info.Mode().IsRegular() {
			return unresumable("the staged database has an unexpected or linked sidecar", nil)
		}
	}
	return nil
}

func requirePublishedCopyIn(paths targetPaths) error {
	if err := copyinmarker.RequirePublishedDatabase(paths.Database); err != nil {
		return unresumable("the published copy-in target is not a regular, sidecar-free database; preserve the target and marker, move foreign database members aside, then run `cpa-manager-plus upgrade run`", err)
	}
	return nil
}

// clearWorkDirectory validates all children before deleting any of them. It
// never follows a work/staging link or removes an unclassified child.
func clearWorkDirectory(paths targetPaths) error {
	if err := validateCopyInWork(paths, false); err != nil {
		return err
	}
	entries, err := os.ReadDir(paths.Work)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(paths.Work, entry.Name())
		if !within(paths.Root, path) || !within(paths.Work, path) {
			return unresumable("a copy-in work child escapes its data root", nil)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return unresumable("the copy-in work directory contains a symbolic link or special file; preserve it and recover into an empty v2 data root", nil)
		}
	}
	for _, entry := range entries {
		if entry.Name() == copyinmarker.FileName {
			continue
		}
		if err := os.RemoveAll(filepath.Join(paths.Work, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func removeCopyInWork(paths targetPaths) error {
	if err := clearWorkDirectory(paths); err != nil {
		return err
	}
	if err := os.Remove(copyinmarker.Path(paths.Root)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(paths.Work); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func cleanupPublishedCopyIn(paths targetPaths) error {
	if err := requirePublishedCopyIn(paths); err != nil {
		return err
	}
	return removeCopyInWork(paths)
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
		matching, err := targetMemberMatches(ctx, paths.Root, manifest, member.bundle)
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

// rollbackMaterialized validates every moved member before removing any of
// them. A replaced member or additional archive entry is preserved for recovery.
func rollbackMaterialized(ctx context.Context, paths targetPaths, moved []string, manifest maintenancebundle.Manifest) error {
	if len(moved) == 0 {
		return nil
	}
	if err := validateCopyInWork(paths, false); err != nil {
		return err
	}
	for _, path := range moved {
		member := ""
		for _, candidate := range copyInMembers {
			if path == filepath.Join(paths.Root, candidate.target) {
				member = candidate.bundle
				break
			}
		}
		if member == "" {
			return unresumable("a materialized member is outside the expected target layout; preserve it for manual recovery", nil)
		}
		matching, err := targetMemberMatches(ctx, paths.Root, manifest, member)
		if err != nil || !matching {
			return unresumable("a materialized member changed after restoration; preserve the target and marker for manual recovery", err)
		}
		if member == archivesDirName {
			if err := refuseExtraTargetEntries(paths, manifest); err != nil {
				return unresumable("the materialized archive tree changed after restoration; preserve it for manual recovery", err)
			}
		}
	}
	var err error
	for index := len(moved) - 1; index >= 0; index-- {
		if !within(paths.Root, moved[index]) {
			return unresumable("a materialized member escapes the v2 data root", nil)
		}
		if removeErr := os.RemoveAll(moved[index]); removeErr != nil {
			err = errors.Join(err, removeErr)
		}
	}
	return errors.Join(err, removeCopyInWork(paths))
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

// writeCopyInMarker publishes the backup binding before moving non-DB members
// and again once staging is ready for its atomic database commit.
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
	if err := copyinmarker.Write(paths.Root, marker); err != nil {
		return fmt.Errorf("write the copy-in provenance marker: %w", err)
	}
	return nil
}

// targetMemberMatches compares one bundle member, including every nested
// archive entry, with the file the v2 layout holds for it.
func targetMemberMatches(ctx context.Context, root string, manifest maintenancebundle.Manifest, member string) (bool, error) {
	for name, entry := range manifest.Entries {
		if name != member && !strings.HasPrefix(name, member+"/") {
			continue
		}
		path, ok := targetMemberPath(root, name)
		if !ok {
			return false, nil
		}
		if !entry.Present {
			_, err := os.Lstat(path)
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}
		matching, err := entryMatches(ctx, path, entry)
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

func entryMatches(ctx context.Context, path string, entry maintenancebundle.Entry) (bool, error) {
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
	digest, err := fileContentDigest(ctx, path)
	if err != nil {
		return false, err
	}
	return digest == entry.SHA256, nil
}

// migrateAndRecord migrates and records only private staging, checkpoints and
// closes it, requires no sidecars, then fsyncs the database before publication.
func migrateAndRecord(ctx context.Context, databasePath string, result Result) (model.UpgradeOperation, bool, error) {
	paths := newTargetPaths(filepath.Dir(result.TargetDatabase))
	if err := validateStagedDatabase(paths, databasePath, false); err != nil {
		return model.UpgradeOperation{}, false, err
	}
	db, err := managersqlite.Open(databasePath)
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
	var busy, logFrames, checkpointed int
	if err := db.QueryRowContext(ctx, `pragma wal_checkpoint(truncate)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return model.UpgradeOperation{}, false, fmt.Errorf("checkpoint the staged database: %w", err)
	}
	if busy != 0 {
		return model.UpgradeOperation{}, false, errors.New("the staged database checkpoint is busy")
	}
	if err := db.Close(); err != nil {
		return model.UpgradeOperation{}, false, err
	}
	if err := validateStagedDatabase(paths, databasePath, true); err != nil {
		return model.UpgradeOperation{}, false, err
	}
	file, err := os.Open(databasePath)
	if err != nil {
		return model.UpgradeOperation{}, false, err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return model.UpgradeOperation{}, false, err
	}
	return persisted, alreadyRecorded, nil
}

func tablePresent(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master
		where type = 'table' and name = ?`, name).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}
