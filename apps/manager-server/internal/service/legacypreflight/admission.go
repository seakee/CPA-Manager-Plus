// Package legacypreflight: the Upgrade admission seam.
//
// The startup gate classifies a source by its schema. That classification alone
// can never admit a 1.x source, and it cannot describe a v2 store whose data was
// copied in from 1.x: after the copy-in the schema is an ordinary v2 schema, so
// the schema says "native-v2" while the operation is still unfinished. The
// seam below is the two additional, explicit inputs: a durable Upgrade operation
// row, and the copy-in provenance marker that covers the window before that row
// commits. Both are read through an injectable reader. Nothing here flips
// Report.UpgradeAuthorized or TrustedHopReceipt.
package legacypreflight

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

// Source kinds. Only fresh and native-v2 are admitted on their own; a 1.x
// source and an upgraded one need the Upgrade operation below.
const (
	SourceKindFresh           = "fresh"
	SourceKindNativeV2        = "native-v2"
	SourceKindLegacyManager   = "legacy-manager"
	SourceKindUpgradedLegacy  = "upgraded-legacy"
	SourceKindMissingDatabase = "missing-database"
	SourceKindPartialSource   = "partial-source"
	SourceKindEmptyDatabase   = "empty-database"
	SourceKindUnknown         = "unknown"
)

// UpgradeStage is the admission position of a recorded Upgrade operation.
type UpgradeStage string

const (
	// UpgradeStageNone means no Upgrade operation is recorded. The gate then
	// behaves exactly as it did before this seam existed.
	UpgradeStageNone UpgradeStage = "none"
	// UpgradeStageInProgress means an operation is recorded and unfinished.
	// The source may start, but only the Upgrade surface may be served.
	UpgradeStageInProgress UpgradeStage = "in_progress"
	// UpgradeStageComplete means the recorded operation finished. The source is
	// an ordinary v2 store for every surface.
	UpgradeStageComplete UpgradeStage = "complete"
)

// UpgradeAdmission is the injected, read-only reader of the durable Upgrade
// operation. A nil reader is the historical behavior: no source is ever
// admitted on the strength of an Upgrade record.
type UpgradeAdmission interface {
	Admitted(ctx context.Context) (UpgradeStage, error)
}

func (s UpgradeStage) normalized() UpgradeStage {
	if s == "" {
		return UpgradeStageNone
	}
	return s
}

func (s UpgradeStage) valid() bool {
	switch s.normalized() {
	case UpgradeStageNone, UpgradeStageInProgress, UpgradeStageComplete:
		return true
	default:
		return false
	}
}

// StartupSurface names what an admitted process may serve.
type StartupSurface string

const (
	// SurfaceFull is the complete v2 application.
	SurfaceFull StartupSurface = "full"
	// SurfaceUpgradeOnly is the Upgrade surface: Bootstrap discovery status
	// reads, the read-only shell and the Upgrade authentication leaf. Console,
	// Key workspace, gateway configuration and model traffic stay closed.
	SurfaceUpgradeOnly StartupSurface = "upgrade-only"
)

// UpgradeSurfaceOnly reports whether the recorded stage restricts startup to
// the Upgrade surface. A completed operation does not.
func UpgradeSurfaceOnly(stage UpgradeStage) bool {
	return stage.normalized() == UpgradeStageInProgress
}

// Surface is the surface this admission permits. An absent admission has none.
func (a *Admission) Surface() StartupSurface {
	if a == nil {
		return ""
	}
	if UpgradeSurfaceOnly(a.UpgradeStage) {
		return SurfaceUpgradeOnly
	}
	return SurfaceFull
}

// upgradeAdmissionStage resolves the injected reader once per admission. A
// reader failure fails closed: an unreadable operation record could hide an
// unfinished upgrade, and treating that as "no record" would serve a partially
// copied store as a finished install.
func upgradeAdmissionStage(ctx context.Context, reader UpgradeAdmission) (UpgradeStage, error) {
	if reader == nil {
		return UpgradeStageNone, nil
	}
	stage, err := reader.Admitted(ctx)
	if err != nil {
		return UpgradeStageNone, fmt.Errorf("read the Upgrade operation: %w", err)
	}
	if !stage.valid() {
		return UpgradeStageNone, fmt.Errorf("Upgrade admission %q is not supported", string(stage))
	}
	return stage.normalized(), nil
}

// startupAllowed admits a source for writable startup. A recorded Upgrade
// operation may relabel a native-v2 source as `upgraded-legacy` — the copy-in
// already ran the incremental v2 migration, so the schema alone can no longer
// describe it. Every other classification keeps its existing rule, and a 1.x
// schema is never admitted whatever the record says.
func startupAllowed(report Report, stage UpgradeStage) bool {
	if len(report.Blocked) != 0 {
		return false
	}
	switch admittedKind(report.SourceKind, stage) {
	case SourceKindFresh, SourceKindNativeV2:
		return true
	case SourceKindUpgradedLegacy:
		// Only a recorded operation may admit an upgraded 1.x source; a source
		// that merely claims the label is not evidence.
		return stage.normalized() == UpgradeStageInProgress || stage.normalized() == UpgradeStageComplete
	default:
		return false
	}
}

// admittedKind relabels a source only when a copy-in could have produced it. A
// copy-in materializes the v2 layout and runs the incremental migration before
// it records anything, so the classified kind of a copied-in store is always
// native-v2; `legacy-manager` with a record therefore has no legitimate
// producer, and keeping that branch would admit a 1.x database on the strength
// of a row it must never have.
func admittedKind(classified string, stage UpgradeStage) string {
	switch stage.normalized() {
	case UpgradeStageInProgress, UpgradeStageComplete:
		switch classified {
		case SourceKindNativeV2, SourceKindUpgradedLegacy:
			return SourceKindUpgradedLegacy
		}
	}
	return classified
}

// DatabaseUpgradeAdmission reads the single upgrade_operations row from the
// Manager database and, when the row is missing, the copy-in provenance marker
// the copy-in published beside it. It opens the store read-only, never creates
// it, applies no pragma that persists state and never migrates.
type DatabaseUpgradeAdmission struct{ DatabasePath string }

func (a DatabaseUpgradeAdmission) Admitted(ctx context.Context) (UpgradeStage, error) {
	path := strings.TrimSpace(a.DatabasePath)
	if path == "" {
		return UpgradeStageNone, errors.New("upgrade admission requires the Manager database path")
	}
	record, found, err := readRecordedUpgrade(ctx, path)
	if err != nil {
		return UpgradeStageNone, err
	}
	if found {
		return upgradeStageFromRecord(record)
	}
	// No row: an unfinished copy-in may still be described by its marker.
	return copyInMarkerStage(path)
}

// readRecordedUpgrade reads the row the admission decides on. Before the
// incremental migration has ever run the table is absent, which is not an error:
// no Upgrade operation has been recorded.
func readRecordedUpgrade(ctx context.Context, path string) (upgradeRecord, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) || info != nil && (!info.Mode().IsRegular() || info.Size() == 0) {
		return upgradeRecord{}, false, nil
	}
	if err != nil {
		return upgradeRecord{}, false, err
	}
	db, err := sql.Open("sqlite", admissionDataSourceName(path))
	if err != nil {
		return upgradeRecord{}, false, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { _ = db.Close() }()
	var present int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master
		where type = 'table' and name = 'upgrade_operations'`).Scan(&present); err != nil {
		return upgradeRecord{}, false, err
	}
	if present == 0 {
		return upgradeRecord{}, false, nil
	}
	var (
		stage                                     string
		sourceKind, fingerprintBefore, fingerprintAfter, backupManifest sql.NullString
	)
	err = db.QueryRowContext(ctx, `select stage, source_kind, source_fingerprint_before,
		source_fingerprint_after, backup_manifest_sha256 from upgrade_operations where id = 1`).
		Scan(&stage, &sourceKind, &fingerprintBefore, &fingerprintAfter, &backupManifest)
	if errors.Is(err, sql.ErrNoRows) {
		return upgradeRecord{}, false, nil
	}
	if err != nil {
		return upgradeRecord{}, false, err
	}
	return upgradeRecord{
		Stage:                   model.UpgradeStage(stage),
		SourceKind:              sourceKind.String,
		SourceFingerprintBefore: fingerprintBefore.String,
		SourceFingerprintAfter:  fingerprintAfter.String,
		BackupManifestSHA256:    backupManifest.String,
	}, true, nil
}

// copyInMarkerStage reads the provenance marker of an in-flight copy-in. The
// copy-in writes it before the migration commits and removes it only after the
// operation row commits, so its presence means the v2 store is unfinished: the
// schema already says "native-v2" while no row describes the copy.
func copyInMarkerStage(databasePath string) (UpgradeStage, error) {
	marker, found, err := copyinmarker.Read(filepath.Dir(databasePath))
	if err != nil {
		return UpgradeStageNone, fmt.Errorf("read the copy-in provenance marker: %w", err)
	}
	if !found || !sameDatabaseFile(marker.TargetDatabase, databasePath) {
		return UpgradeStageNone, nil
	}
	return UpgradeStageInProgress, nil
}

// sameDatabaseFile compares a marker's target with the database being admitted,
// resolving symlinks in the parent so a canonical path and its alias agree.
func sameDatabaseFile(recorded, actual string) bool {
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

// upgradeRecord is the row half of the admission decision. The admission never
// trusts the stage alone: a `complete` that does not describe a finished
// copy-in is not a finished install.
type upgradeRecord struct {
	Stage                   model.UpgradeStage
	SourceKind              string
	SourceFingerprintBefore string
	SourceFingerprintAfter  string
	BackupManifestSHA256    string
}

// upgradeStageFromRecord maps the durable row onto the two admission stages.
// A failed operation is unfinished, never complete: it must not serve the
// normal surfaces.
func upgradeStageFromRecord(record upgradeRecord) (UpgradeStage, error) {
	switch record.Stage {
	case model.UpgradeStageComplete:
		if err := requireCompleteEvidence(record); err != nil {
			return UpgradeStageNone, err
		}
		return UpgradeStageComplete, nil
	case "", model.UpgradeStagePreflight, model.UpgradeStageCopyIn, model.UpgradeStageReconcile,
		model.UpgradeStageCPAImport, model.UpgradeStageExternalConnect, model.UpgradeStageVerify,
		model.UpgradeStageFailed:
		return UpgradeStageInProgress, nil
	default:
		return UpgradeStageNone, fmt.Errorf("recorded Upgrade stage %q is not supported", string(record.Stage))
	}
}

// requireCompleteEvidence refuses a `complete` row that cannot describe a
// finished copy-in: without the upgraded-legacy label, both source fingerprints
// and the backup manifest digest the row cannot distinguish a copied-in store
// from any other database that happens to carry a row, so it never unlocks the
// normal surfaces.
func requireCompleteEvidence(record upgradeRecord) error {
	switch {
	case record.SourceKind != SourceKindUpgradedLegacy:
		return errors.New("a complete Upgrade record must name the upgraded-legacy source kind")
	case record.SourceFingerprintBefore == "" || record.SourceFingerprintAfter == "":
		return errors.New("a complete Upgrade record must carry both source fingerprints")
	case record.BackupManifestSHA256 == "":
		return errors.New("a complete Upgrade record must carry the backup manifest digest")
	}
	return nil
}

// admissionDataSourceName mirrors the bounded inspection DSN: read-only, no
// persistence, and immutable only when there is no live WAL to join.
func admissionDataSourceName(path string) string {
	immutable := true
	if info, err := os.Lstat(path + "-wal"); err == nil && info.Size() > 0 {
		immutable = false
	}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := &url.URL{Scheme: "file", Path: uriPath}
	query := dsn.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	if immutable {
		query.Set("immutable", "1")
	}
	dsn.RawQuery = query.Encode()
	return dsn.String()
}
