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
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
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
type DatabaseUpgradeAdmission struct {
	DatabasePath string
	// DataKey is the inline or secret-file Manager data key from configuration
	// (CPA_MANAGER_DATA_KEY / CPA_MANAGER_DATA_KEY_FILE). When non-empty, it takes
	// precedence over DataKeyPath, matching security.LoadOrCreateDataKey.
	DataKey string
	// DataKeyPath is the configured Manager data-key file path
	// (CPA_MANAGER_DATA_KEY_PATH or config dataKeyPath). When empty, it defaults
	// to "data.key" in the database's directory.
	DataKeyPath string
}

func (a DatabaseUpgradeAdmission) Admitted(ctx context.Context) (UpgradeStage, error) {
	path := strings.TrimSpace(a.DatabasePath)
	if path == "" {
		return UpgradeStageNone, errors.New("upgrade admission requires the Manager database path")
	}
	// Staging is private. Until atomic publication readers use only the marker;
	// an absent target or foreign sidecar must never provoke a SQLite open.
	marker, inFlight, err := readCopyInMarker(path)
	if err != nil {
		return UpgradeStageNone, err
	}
	if inFlight && marker.AwaitingCommit(path) {
		return UpgradeStageInProgress, nil
	}
	// A committing marker can outlive its published database. The row then
	// remains the stronger evidence; CopyIn owns verification and cleanup.
	record, found, err := readRecordedUpgrade(ctx, a)
	if err != nil {
		return UpgradeStageNone, err
	}
	if found {
		return upgradeStageFromRecord(record)
	}
	if inFlight {
		// No published row: the copy-in must be resumed.
		return UpgradeStageInProgress, nil
	}
	return UpgradeStageNone, nil
}

// readRecordedUpgrade reads the row the admission decides on. Before the
// incremental migration has ever run the table is absent, which is not an error:
// no Upgrade operation has been recorded.
//
// The database file and every journal member beside it are audited by the shared
// source-file policy first: a linked, special or multi-linked member, an
// unreadable sidecar or a non-empty rollback journal is a state no file-only
// answer can describe, so it is reported instead of being read as "no record".
func readRecordedUpgrade(ctx context.Context, a DatabaseUpgradeAdmission) (upgradeRecord, bool, error) {
	path := strings.TrimSpace(a.DatabasePath)
	info, err := processlock.InspectSourceFile(path)
	if os.IsNotExist(err) {
		if sideErr := copyinmarker.DatabaseSidecarsAbsent(path); sideErr != nil {
			return upgradeRecord{}, false, sideErr
		}
		return upgradeRecord{}, false, nil
	}
	if err != nil {
		return upgradeRecord{}, false, err
	}
	if info.Size() == 0 {
		if sideErr := copyinmarker.DatabaseSidecarsAbsent(path); sideErr != nil {
			return upgradeRecord{}, false, sideErr
		}
		return upgradeRecord{}, false, nil
	}
	// The operation table is looked up in the database file alone first when no
	// journal is pending, bracketed by before/after file and sidecar identity. An
	// immutable handle opens no journal and creates no -shm, so a cold source that
	// records no operations — every clean 1.x volume — is answered without writing
	// anything beside the tree v2 must leave untouched.
	//
	// A pending journal makes a file-only lookup non-conclusive, never negative:
	// SQLite keeps both the table and its row in the journal until a checkpoint
	// copies them into the file, so a pending journal (whether present at baseline
	// or created during the immutable probe) forces the private copy below.
	inFile, walPending, baseline, err := recordedOperationEvidenceWithBaseline(ctx, path)
	if err != nil {
		return upgradeRecord{}, false, err
	}
	if !inFile && !walPending {
		return upgradeRecord{}, false, nil
	}
	readPath := path
	if walPending {
		copied, cleanup, copyErr := sqlite.CopySourceForRead(ctx, sqlite.InspectionPaths{
			Database: path,
			DataKey:  a.DataKeyPath,
		}, "-wal")
		if copyErr != nil {
			return upgradeRecord{}, false, copyErr
		}
		defer cleanup()
		readPath = copied
	}
	db, err := sql.Open("sqlite", admissionDataSourceName(readPath, !walPending))
	if err != nil {
		return upgradeRecord{}, false, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { _ = db.Close() }()
	// The copy answers with the journal applied. A journal that held no schema of
	// its own leaves the store exactly as the file described it: no operation, no
	// evidence, and the ordinary admission answers.
	if !inFile {
		present, probeErr := recordedTablePresent(ctx, db)
		if probeErr != nil {
			return upgradeRecord{}, false, probeErr
		}
		if !present {
			return upgradeRecord{}, false, nil
		}
	}
	// The columns a reader may select depend on the build that wrote the table:
	// a store that predates the completion gate has neither the verification
	// block nor the External connect table, and reading it must say "no evidence"
	// instead of failing on SQL.
	columns, err := recordedUpgradeColumns(ctx, db)
	if err != nil {
		return upgradeRecord{}, false, err
	}
	var (
		stage                                                           string
		domainsDone, verification                                       sql.NullString
		sourceKind, fingerprintBefore, fingerprintAfter, backupManifest sql.NullString
	)
	selected := []string{"stage"}
	destinations := []any{&stage}
	addColumn := func(name string, destination any) {
		if columns[name] {
			selected = append(selected, name)
			destinations = append(destinations, destination)
		}
	}
	addColumn("source_kind", &sourceKind)
	addColumn("source_fingerprint_before", &fingerprintBefore)
	addColumn("source_fingerprint_after", &fingerprintAfter)
	addColumn("backup_manifest_sha256", &backupManifest)
	addColumn("domains_done", &domainsDone)
	addColumn("verification", &verification)
	err = db.QueryRowContext(ctx, `select `+strings.Join(selected, ", ")+
		` from upgrade_operations where id = 1`).Scan(destinations...)
	if errors.Is(err, sql.ErrNoRows) {
		if !walPending {
			if after, stateErr := inspectOperationEvidenceState(path); stateErr != nil || after != baseline {
				if stateErr != nil {
					return upgradeRecord{}, false, stateErr
				}
				return upgradeRecord{}, false, errors.New("database changed during recorded operation read")
			}
		}
		return upgradeRecord{}, false, nil
	}
	if err != nil {
		return upgradeRecord{}, false, err
	}
	record := upgradeRecord{
		Stage:                   model.UpgradeStage(stage),
		SourceKind:              sourceKind.String,
		SourceFingerprintBefore: fingerprintBefore.String,
		SourceFingerprintAfter:  fingerprintAfter.String,
		BackupManifestSHA256:    backupManifest.String,
		DomainsDone:             domainsDone.String,
		Verification:            verification.String,
	}
	if record.Stage == model.UpgradeStageComplete {
		binding, err := readRecordedExternalBinding(ctx, db)
		if err != nil {
			return upgradeRecord{}, false, err
		}
		record.Binding = binding
		// The verification the gate writes names a binding, but it never carries
		// the credential that binding proved. That credential is the sealed
		// secret on the row, and a completed store is admitted only when this
		// Manager can still read it and still holds it. A row recorded before
		// that column existed carries none, and it is refused here instead of
		// unlocking a product whose next start would meet 401.
		if err := requireSealedBindingAuthority(ctx, db, a, record); err != nil {
			return upgradeRecord{}, false, err
		}
		// A Managed completion has no External binding; the handoff it rests on is
		// the import's recorded destination and the Manager's own sealed connection
		// for it. Both are read here, before the row can unlock the product.
		if err := requireManagedDestinationAuthority(ctx, db, a, record); err != nil {
			return upgradeRecord{}, false, err
		}
	}
	if !walPending {
		if after, stateErr := inspectOperationEvidenceState(path); stateErr != nil || after != baseline {
			if stateErr != nil {
				return upgradeRecord{}, false, stateErr
			}
			return upgradeRecord{}, false, errors.New("database changed during recorded operation read")
		}
	}
	return record, true, nil
}

// recordedUpgradeColumns reports the columns the stored operation table has. A
// pre-gate schema is not an error: the missing evidence is what the admission
// then refuses, not a read failure a process could mistake for "no record".
func recordedUpgradeColumns(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	return recordedTableColumns(ctx, db, "upgrade_operations")
}

// recordedTableColumns reports the columns a stored table has. A reader selects
// only what the writing build created, so a store that predates a column answers
// "the column is absent" instead of failing on SQL. The table name is an
// internal constant at every call site, never caller input.
func recordedTableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `pragma table_info("`+table+`")`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns := map[string]bool{}
	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue any
			pk           int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

// readRecordedExternalBinding reads the External connect row the completion
// evidence has to name. A store whose connect step never ran, or one written
// before the table existed, has no binding at all.
//
// The management secret is read only when the writing build created its column:
// a binding recorded before that column existed carries no sealed credential,
// and the admission refuses it instead of treating the empty value as proof.
func readRecordedExternalBinding(ctx context.Context, db *sql.DB) (upgradeRecordBinding, error) {
	var present int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master
		where type = 'table' and name = 'upgrade_external_connect'`).Scan(&present); err != nil {
		return upgradeRecordBinding{}, err
	}
	if present == 0 {
		return upgradeRecordBinding{}, nil
	}
	columns, err := recordedTableColumns(ctx, db, "upgrade_external_connect")
	if err != nil {
		return upgradeRecordBinding{}, err
	}
	var (
		baseURL, bindingIdentity, generation, managementKey sql.NullString
		observedAtMS                                        sql.NullInt64
	)
	selected := []string{"base_url", "identity", "generation", "observed_at_ms"}
	destinations := []any{&baseURL, &bindingIdentity, &generation, &observedAtMS}
	if columns["management_key"] {
		selected = append(selected, "management_key")
		destinations = append(destinations, &managementKey)
	}
	err = db.QueryRowContext(ctx, `select `+strings.Join(selected, ", ")+
		` from upgrade_external_connect where id = 1`).Scan(destinations...)
	if errors.Is(err, sql.ErrNoRows) {
		return upgradeRecordBinding{}, nil
	}
	if err != nil {
		return upgradeRecordBinding{}, err
	}
	return upgradeRecordBinding{
		Found:         true,
		BaseURL:       baseURL.String,
		Identity:      bindingIdentity.String,
		Generation:    generation.String,
		ManagementKey: managementKey.String,
		ObservedAtMS:  observedAtMS.Int64,
	}, nil
}

// readCopyInMarker reads preparation evidence before any database open. A row
// and migrated schema become visible together; only cleanup may remain pending.
func readCopyInMarker(databasePath string) (copyinmarker.Marker, bool, error) {
	marker, found, err := copyinmarker.Read(filepath.Dir(databasePath))
	if err != nil {
		return copyinmarker.Marker{}, false, fmt.Errorf("read the copy-in provenance marker: %w", err)
	}
	if !found {
		return copyinmarker.Marker{}, false, nil
	}
	// The marker sits in this data root's own reserved work directory, so it
	// describes this root even when the root is mounted at another absolute
	// path: a host that runs the CLI and a container that runs the server agree
	// on the bytes, not on the path. Reporting "no record" there would fail open
	// on an unfinished store, so a relocated root stays in progress. A marker
	// naming a different database file is evidence for some other run, and
	// admitting this store on it would be guessing: it fails closed instead.
	// SameTarget is the one rule the wizard's status projection applies too.
	if !copyinmarker.SameTarget(marker.TargetDatabase, databasePath) {
		return copyinmarker.Marker{}, false, errors.New("the copy-in marker in this data root describes another database")
	}
	return marker, true, nil
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
	// DomainsDone is the recorded completed-domain list exactly as stored. The
	// completion gate's evidence is checked against it, so an unreadable or
	// shortened list is a refusal instead of an empty set.
	DomainsDone string
	// Verification is the recorded verification block. It stays empty when the
	// store predates the column, which is exactly the state a build without the
	// completion gate wrote: there is no evidence such a row could carry.
	Verification string
	// Binding is the External connect record. The verification the gate writes
	// names the binding it observed, so a complete row is only admissible with
	// the same binding on disk.
	Binding upgradeRecordBinding
}

// upgradeRecordBinding is the External connect row a completed External
// operation's verification names. Found is false before the connect step
// recorded one, which is also the state of a store that predates the table.
type upgradeRecordBinding struct {
	Found      bool
	BaseURL    string
	Identity   string
	Generation string
	// ManagementKey is the sealed credential the connect step proved, exactly as
	// stored. It is empty for a binding recorded before the column existed, and
	// the admission refuses such a completed row: nothing on disk says which
	// secret the Manager would present to the CPA it just verified.
	ManagementKey string
	ObservedAtMS  int64
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
		// A rolled-back operation is unfinished by admission's terms: the v2
		// root was abandoned, so serving the normal surfaces would present an
		// abandoned install as a working one. It stays on the Upgrade surface.
		model.UpgradeStageRolledBack, model.UpgradeStageFailed:
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
//
// The stage alone is not a verdict. The completion gate writes the observation
// it rests on beside the stage, so this reader proves the same facts the gate
// proved: the source volume the copy-in observed, every domain the verdict names,
// and the target binding that verdict proved — the External connect binding with
// its sealed credential, or the Managed import's destination. A row written
// before the gate existed carries none of that, and it is refused here instead of
// admitting a store the gate never verified.
func requireCompleteEvidence(record upgradeRecord) error {
	switch {
	case record.SourceKind != SourceKindUpgradedLegacy:
		return errors.New("a complete Upgrade record must name the upgraded-legacy source kind")
	case record.SourceFingerprintBefore == "" || record.SourceFingerprintAfter == "":
		return errors.New("a complete Upgrade record must carry both source fingerprints")
	case record.BackupManifestSHA256 == "":
		return errors.New("a complete Upgrade record must carry the backup manifest digest")
	}
	evidence, err := (model.UpgradeOperation{Verification: json.RawMessage(record.Verification)}).VerificationEvidence()
	if err != nil {
		return fmt.Errorf("a complete Upgrade record must carry the verification the completion gate wrote: %w", err)
	}
	if err := evidence.Validate(); err != nil {
		return fmt.Errorf("a complete Upgrade record carries verification this build cannot accept: %w", err)
	}
	if strings.TrimSpace(evidence.SourceFingerprint) != strings.TrimSpace(record.SourceFingerprintAfter) {
		return errors.New("a complete Upgrade record's verification names a different source volume than the recorded copy-in")
	}
	recorded, err := decodeRecordedDomains(record.DomainsDone)
	if err != nil {
		return fmt.Errorf("a complete Upgrade record must carry its completed domains: %w", err)
	}
	for _, domain := range evidence.Domains {
		if !containsRecordedDomain(recorded, domain) {
			return fmt.Errorf("a complete Upgrade record's verification names the %q domain the row never recorded", domain)
		}
	}
	// `cpa_config` is the domain both target steps record; `identity` is the
	// External connect step's own observation and has no Managed producer.
	requiredDomains := []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig}
	switch evidence.TargetMode {
	case model.UpgradeTargetExternal:
		requiredDomains = append(requiredDomains, model.UpgradeDomainIdentity)
	case model.UpgradeTargetManaged:
		if record.Binding.Found {
			// The Managed verdict names the import's destination. A stored
			// External binding contradicts it, so the row describes two targets.
			return errors.New("a complete Managed Upgrade record carries an External connect binding")
		}
	default:
		return fmt.Errorf("a complete Upgrade record names an unsupported target mode %q", evidence.TargetMode)
	}
	for _, required := range requiredDomains {
		if !containsRecordedDomain(evidence.Domains, required) {
			return fmt.Errorf("a complete Upgrade record's verification does not name the %q domain", required)
		}
	}
	if evidence.TargetMode != model.UpgradeTargetExternal {
		return nil
	}
	binding := record.Binding
	switch {
	case !binding.Found:
		return errors.New("a complete Upgrade record must name the External connect binding its verification observed")
	case strings.TrimSpace(binding.BaseURL) == "":
		return errors.New("a complete Upgrade record's External binding names no target address")
	case strings.TrimSpace(binding.Identity) == "" || strings.TrimSpace(binding.Generation) == "":
		return errors.New("a complete Upgrade record's External binding names no identity")
	case binding.Identity != binding.Generation:
		return errors.New("a complete Upgrade record's External binding identity is not its generation")
	case binding.ObservedAtMS <= 0:
		return errors.New("a complete Upgrade record's External binding carries no observation time")
	}
	if evidence.BindingIdentity != binding.Identity ||
		evidence.BindingGeneration != binding.Generation ||
		evidence.BindingObservedAtMS != binding.ObservedAtMS {
		return errors.New("a complete Upgrade record's verification names a different External binding than the recorded one")
	}
	if !security.IsValidProtectedEnvelope(strings.TrimSpace(binding.ManagementKey)) {
		// The verification proves the binding, not the credential the connect step
		// proved against it. Without a sealed secret on the row the Manager's own
		// key is not known to be the one that worked, so a restart could meet 401
		// against a CPA this operation called complete.
		return errors.New("a complete Upgrade record's External binding carries no sealed management secret, so the credential the Manager would present was never proved")
	}
	return nil
}

// admissionDataKeyFileName names the Manager data key beside the database. The
// admission only ever reads it: a store a completed operation left behind always
// holds the key its secrets were sealed with, and minting one here would leave a
// second key that cannot decrypt the record it just admitted.
const admissionDataKeyFileName = "data.key"

// requireSealedBindingAuthority proves the completed row's External binding
// carries a credential this Manager can read and still holds.
//
// The sealed envelope alone is structural: a valid envelope can be written with
// another data key, and a decryptable secret can name a key the Manager's own
// connection does not. Both are the same failure at startup — the Manager would
// present a credential the CPA never accepted — so both are refused here, before
// the row can unlock the writable product.
func requireSealedBindingAuthority(ctx context.Context, db *sql.DB, a DatabaseUpgradeAdmission, record upgradeRecord) error {
	binding := record.Binding
	if !binding.Found {
		// The evidence check reports the missing binding; there is no secret to
		// compare.
		return nil
	}
	sealed := strings.TrimSpace(binding.ManagementKey)
	if !security.IsValidProtectedEnvelope(sealed) {
		return errors.New("a complete Upgrade record's External binding carries no sealed management secret, so the credential the Manager would present was never proved")
	}
	protector, err := admissionProtector(a)
	if err != nil {
		return fmt.Errorf("a complete Upgrade record's sealed management secret cannot be read because the Manager data key is missing: %w", err)
	}
	secret, err := protector.UnprotectString(sealed)
	if err != nil {
		return fmt.Errorf("decrypt the External binding's sealed management secret: %w", err)
	}
	secret = strings.TrimSpace(secret)
	if !cpa.ValidV8ManagementKey(secret) {
		return errors.New("a complete Upgrade record's External binding carries no usable management secret")
	}
	st := store.New(db, protector)
	managerCfg, managerOK, err := st.LoadManagerConfig(ctx)
	if err != nil {
		return fmt.Errorf("read the Manager configuration: %w", err)
	}
	setup, setupOK, err := st.LoadSetup(ctx)
	if err != nil {
		return fmt.Errorf("read the Manager setup: %w", err)
	}
	resolution, err := managerconfig.ResolveLegacyConnectionAuthority(managerCfg, managerOK, setup, setupOK)
	if err != nil {
		return fmt.Errorf("resolve the Manager's stored CPA connection: %w", err)
	}
	if resolution.Authority == managerconfig.LegacyConnectionAuthorityNone {
		return errors.New("a complete Upgrade record's External binding names a CPA the Manager stores no connection for")
	}
	canonical, err := canonicalAdmissionAddress(resolution.Connection.BaseURL)
	if err != nil {
		return fmt.Errorf("the CPA address the Manager stores is not a usable endpoint: %w", err)
	}
	if canonical != strings.TrimSpace(binding.BaseURL) {
		return errors.New("a complete Upgrade record's External binding names a CPA address the Manager's own connection does not")
	}
	if !security.EqualHMAC(strings.TrimSpace(resolution.Connection.ManagementKey), secret) {
		return errors.New("a complete Upgrade record's External binding proved a management secret the Manager's own connection does not carry")
	}
	return nil
}

// admissionImportDestinationKeySetting names the settings row the Managed CPA
// import writes for its destination attempt and completion evidence.
// `upgrade.cpaImportDestinationKeySetting` is the producer; the admission reads
// the same bytes and must not depend on the writer's package.
const admissionImportDestinationKeySetting = "upgrade_cpa_import_destination_v1"

// managedImportDestination is the part of that row the admission re-checks: the
// attempt identity the import submitted, the whole request it published, the
// address a run proved and the server fingerprint that run observed. The
// management secret the row also carries is not this check's business — the
// Manager's own connection is.
//
// The request is re-read field by field because the handoff is the claim that this
// operation staged this destination: the attempt's operation ID is derived from
// the source it was staged for, and the Supervisor replays the request against the
// authority, artifact, key list and plugin choice recorded here. A row missing any
// of them describes a stage the import never submitted, and admitting it would
// start a Manager on an operation whose target nothing proved.
type managedImportDestination struct {
	BaseURL           string `json:"baseUrl"`
	URLProved         bool   `json:"urlProved"`
	ServerFingerprint string `json:"serverFingerprint"`
	ManagementKey     string `json:"managementKey"`
	OperationID       string `json:"operationId"`
	Attempt           int    `json:"attempt"`
	Intent            struct {
		SourceIdentity            string   `json:"sourceIdentity"`
		SourceClass               string   `json:"sourceClass"`
		ExpectedRuntimeIdentity   string   `json:"expectedRuntimeIdentity"`
		ExpectedRuntimeGeneration uint64   `json:"expectedRuntimeGeneration"`
		ExpectedActiveArtifactID  string   `json:"expectedActiveArtifactId"`
		APIKeys                   []string `json:"apiKeys"`
		PluginsEnabled            *bool    `json:"pluginsEnabled"`
	} `json:"intent"`
}

// requireManagedDestinationProvenance proves the recorded row describes a stage
// this operation submitted, before any of it is compared with the Manager's own
// connection.
//
// Every field is the import's own: the operation ID is the Supervisor's replay
// identity for one attempt of one source, the source identity is the digest the
// managed source stamps, the class selects the Supervisor's 1.x arm, and the
// authority, artifact, key list and plugin choice are the request the destination
// was staged with. The artifact is compared with the compiled adoption record this
// build carries, so a store completed on another build's artifact is not admitted
// by this one.
func requireManagedDestinationProvenance(destination managedImportDestination) error {
	switch {
	case strings.TrimSpace(destination.BaseURL) == "":
		return errors.New("a complete Managed Upgrade record's CPA import destination names no address")
	case !destination.URLProved:
		return errors.New("a complete Managed Upgrade record's CPA import destination names no address a run proved")
	case !validManagedServerFingerprint(destination.ServerFingerprint):
		return errors.New("a complete Managed Upgrade record's CPA import destination names no proved server fingerprint")
	case !validManagedSourceIdentity(destination.Intent.SourceIdentity):
		return errors.New("a complete Managed Upgrade record's CPA import destination names no source identity this import stamps")
	case destination.Intent.SourceClass != managedDestinationSourceClass:
		return errors.New("a complete Managed Upgrade record's CPA import destination does not name the legacy managed source class the import stages")
	case destination.Attempt < 1:
		return errors.New("a complete Managed Upgrade record's CPA import destination names no attempt identity")
	case destination.OperationID != managedDestinationOperationID(destination.Intent.SourceIdentity, destination.Attempt):
		return errors.New("a complete Managed Upgrade record's CPA import destination operation ID does not resolve to its own source and attempt")
	case strings.TrimSpace(destination.Intent.ExpectedRuntimeIdentity) == "" || destination.Intent.ExpectedRuntimeGeneration == 0:
		return errors.New("a complete Managed Upgrade record's CPA import destination names no Supervisor authority the stage request addressed")
	case len(destination.Intent.APIKeys) == 0:
		return errors.New("a complete Managed Upgrade record's CPA import destination names no client key list the stage request published")
	case destination.Intent.PluginsEnabled == nil:
		return errors.New("a complete Managed Upgrade record's CPA import destination names no plugin choice the stage request published")
	}
	qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
	if err != nil {
		return fmt.Errorf("this build carries no accepted CPA adoption artifact to compare the recorded destination against: %w", err)
	}
	if model.RuntimeArtifactID(destination.Intent.ExpectedActiveArtifactID) != qualifiedArtifact {
		return errors.New("a complete Managed Upgrade record's CPA import destination was staged on an artifact this build's adoption record is not qualified against")
	}
	return nil
}

// managedDestinationSourceClass is the source class every Managed destination
// stage publishes; the import copies a 1.x stack's own CPA state.
const managedDestinationSourceClass = "legacy_managed"

// managedDestinationOperationID mirrors the import's derivation of the Supervisor
// operation ID. The admission reads the same bytes the writer wrote and must not
// depend on the writer's package, so the derivation is repeated here.
func managedDestinationOperationID(sourceIdentity string, attempt int) string {
	sum := sha256.Sum256([]byte(sourceIdentity))
	base := "upgrade-cpa-import:" + hex.EncodeToString(sum[:])[:32]
	if attempt <= 1 {
		return base
	}
	return fmt.Sprintf("%s:attempt:%d", base, attempt)
}

// validManagedSourceIdentity accepts the identity the managed source stamps and
// the Runtime Supervisor validates for `legacy_managed`: the `external:sha256:`
// prefix and a lowercase SHA-256 digest.
func validManagedSourceIdentity(value string) bool {
	digest, ok := strings.CutPrefix(strings.TrimSpace(value), "external:sha256:")
	return ok && identity.IsValidSHA256Hex(digest)
}

// admissionProtector loads the Manager data key the store's secrets are sealed
// with. It never creates one.
func admissionProtector(a DatabaseUpgradeAdmission) (*security.Protector, error) {
	dataKeyPath := strings.TrimSpace(a.DataKeyPath)
	if dataKeyPath == "" {
		dataKeyPath = filepath.Join(filepath.Dir(strings.TrimSpace(a.DatabasePath)), admissionDataKeyFileName)
	}
	key, err := security.LoadExistingDataKey(a.DataKey, dataKeyPath)
	if err != nil {
		return nil, err
	}
	return security.NewProtector(key)
}

// requireManagedDestinationAuthority proves the completed Managed row's handoff
// survives a restart: the store holds the destination evidence the import wrote,
// and the Manager's own connection carries the sealed credential for exactly that
// address.
//
// The sealed envelope is checked, not merely the decrypted value: a plaintext
// secret left on the row is not a credential this Manager sealed, and the next
// start must read the same bytes this check read. Nothing here creates a key or
// repairs a row — a completed store is read-only.
func requireManagedDestinationAuthority(ctx context.Context, db *sql.DB, a DatabaseUpgradeAdmission, record upgradeRecord) error {
	evidence, err := (model.UpgradeOperation{Verification: json.RawMessage(record.Verification)}).VerificationEvidence()
	if err != nil || evidence.TargetMode != model.UpgradeTargetManaged {
		// requireCompleteEvidence reports an unreadable block, and only a Managed
		// verdict has a destination row to prove.
		return nil
	}
	var raw string
	err = db.QueryRowContext(ctx,
		`select value from settings where key = ?`, admissionImportDestinationKeySetting).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("a complete Managed Upgrade record names no CPA import destination the store holds")
	}
	if err != nil {
		return fmt.Errorf("read the recorded CPA import destination: %w", err)
	}
	var destination managedImportDestination
	if err := json.Unmarshal([]byte(raw), &destination); err != nil {
		return fmt.Errorf("decode the recorded CPA import destination: %w", err)
	}
	if err := requireManagedDestinationProvenance(destination); err != nil {
		return err
	}
	destinationAddress, err := canonicalAdmissionAddress(destination.BaseURL)
	if err != nil {
		return fmt.Errorf("the recorded CPA import destination names an address that is not a usable endpoint: %w", err)
	}
	protector, err := admissionProtector(a)
	if err != nil {
		return fmt.Errorf("a complete Managed Upgrade record's sealed connection cannot be read with this Manager data key: %w", err)
	}
	var managerRaw string
	err = db.QueryRowContext(ctx, `select value from settings where key = 'manager_config_v1'`).Scan(&managerRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("a complete Managed Upgrade record names a CPA the Manager stores no connection for")
	}
	if err != nil {
		return fmt.Errorf("read the Manager's stored CPA connection: %w", err)
	}
	var stored model.ManagerConfig
	if err := json.Unmarshal([]byte(managerRaw), &stored); err != nil {
		return fmt.Errorf("decode the Manager's stored CPA connection: %w", err)
	}
	sealed := strings.TrimSpace(stored.CPAConnection.ManagementKey)
	if !security.IsValidProtectedEnvelope(sealed) {
		return errors.New("a complete Managed Upgrade record's Manager connection carries no sealed management secret")
	}
	secret, err := protector.UnprotectString(sealed)
	if err != nil {
		return fmt.Errorf("decrypt the Manager's sealed management secret: %w", err)
	}
	secret = strings.TrimSpace(secret)
	if !cpa.ValidV8ManagementKey(secret) {
		return errors.New("a complete Managed Upgrade record's Manager connection carries no usable management secret")
	}
	// The destination was handed a secret by the import, and the Manager holds the
	// one it will present after a restart. If the two differ, the store describes a
	// handoff between them that never happened.
	destinationSealed := strings.TrimSpace(destination.ManagementKey)
	if !security.IsValidProtectedEnvelope(destinationSealed) {
		return errors.New("a complete Managed Upgrade record's CPA import destination carries no sealed management secret")
	}
	destinationSecret, err := protector.UnprotectString(destinationSealed)
	if err != nil {
		return fmt.Errorf("decrypt the recorded destination's sealed management secret: %w", err)
	}
	if !security.EqualHMAC(strings.TrimSpace(destinationSecret), secret) {
		return errors.New("a complete Managed Upgrade record's CPA import destination holds a management secret the Manager does not")
	}
	st := store.New(db, protector)
	managerCfg, managerOK, err := st.LoadManagerConfig(ctx)
	if err != nil {
		return fmt.Errorf("read the Manager configuration: %w", err)
	}
	setup, setupOK, err := st.LoadSetup(ctx)
	if err != nil {
		return fmt.Errorf("read the Manager setup: %w", err)
	}
	resolution, err := managerconfig.ResolveLegacyConnectionAuthority(managerCfg, managerOK, setup, setupOK)
	if err != nil {
		return fmt.Errorf("resolve the Manager's stored CPA connection: %w", err)
	}
	if resolution.Authority == managerconfig.LegacyConnectionAuthorityNone {
		return errors.New("a complete Managed Upgrade record names a CPA the Manager stores no connection for")
	}
	connectionAddress, err := canonicalAdmissionAddress(resolution.Connection.BaseURL)
	if err != nil {
		return fmt.Errorf("the CPA address the Manager stores is not a usable endpoint: %w", err)
	}
	if connectionAddress != destinationAddress {
		return errors.New("a complete Managed Upgrade record's CPA import destination names a CPA address the Manager's own connection does not")
	}
	if !security.EqualHMAC(strings.TrimSpace(resolution.Connection.ManagementKey), secret) {
		return errors.New("a complete Managed Upgrade record's Manager connection does not carry the sealed management secret this store holds")
	}
	return nil
}

// validManagedServerFingerprint accepts exactly the shape
// freshkeyapply.ExternalServerFingerprint produces: the shared identity prefix
// and a lowercase SHA-256 digest.
func validManagedServerFingerprint(value string) bool {
	digest, ok := strings.CutPrefix(strings.TrimSpace(value), "external:sha256:")
	return ok && identity.IsValidSHA256Hex(digest)
}

// canonicalAdmissionAddress applies the two functions the External connect step
// applies to a target address, so the admission compares spellings of one
// endpoint rather than an operator's spelling against a stored one.
func canonicalAdmissionAddress(address string) (string, error) {
	normalized, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(address))
	if err != nil {
		return "", err
	}
	return identity.CanonicalExternalURL(normalized)
}

// decodeRecordedDomains parses the stored completed-domain list. The column is a
// JSON string array on disk, so anything else is corruption the admission must
// refuse rather than read as "no domains".
func decodeRecordedDomains(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("the completed-domain list is empty")
	}
	var domains []string
	if err := json.Unmarshal([]byte(raw), &domains); err != nil {
		return nil, err
	}
	for _, domain := range domains {
		if !model.ValidUpgradeDomain(domain) {
			return nil, fmt.Errorf("the completed-domain list names the unsupported domain %q", domain)
		}
	}
	return domains, nil
}

func containsRecordedDomain(domains []string, want string) bool {
	for _, domain := range domains {
		if domain == want {
			return true
		}
	}
	return false
}

// RecordedOperationTablePresent reports whether the database file names the
// Upgrade operation table. The handle is immutable, so the answer describes the
// file as written and never the journal beside it: a source that records no
// operation — every clean 1.x volume — is answered without copying it or
// creating anything beside it.
//
// A false answer is only conclusive when no journal is pending; record the two
// facts together with RecordedOperationEvidence before deciding that a store
// never recorded an operation.
func RecordedOperationTablePresent(ctx context.Context, databasePath string) (bool, error) {
	db, err := sql.Open("sqlite", admissionDataSourceName(databasePath, true))
	if err != nil {
		return false, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { _ = db.Close() }()
	return recordedTablePresent(ctx, db)
}

// beforeOperationEvidenceProbe is a package-private synchronous test seam for
// exercising concurrent journal creation between baseline capture and the
// immutable table probe. Production leaves it nil.
var beforeOperationEvidenceProbe func(databasePath string)

type evidenceMemberIdentity struct {
	present     bool
	device      uint64
	inode       uint64
	size        int64
	modifiedNS  int64
	changeStamp string
}

type operationEvidenceState struct {
	database evidenceMemberIdentity
	wal      evidenceMemberIdentity
	shm      evidenceMemberIdentity
	journal  evidenceMemberIdentity
}

func inspectEvidenceMember(path string) (evidenceMemberIdentity, error) {
	info, err := processlock.InspectSourceFile(path)
	if os.IsNotExist(err) {
		return evidenceMemberIdentity{}, nil
	}
	if err != nil {
		return evidenceMemberIdentity{}, err
	}
	id, err := copyinmarker.Identity(path)
	if err != nil {
		return evidenceMemberIdentity{}, err
	}
	stamp, err := copyinmarker.ChangeStamp(path)
	if err != nil {
		return evidenceMemberIdentity{}, err
	}
	return evidenceMemberIdentity{
		present:     true,
		device:      id.Device,
		inode:       id.Inode,
		size:        info.Size(),
		modifiedNS:  info.ModTime().UnixNano(),
		changeStamp: stamp,
	}, nil
}

func inspectOperationEvidenceState(databasePath string) (operationEvidenceState, error) {
	if err := auditAdmissionSidecars(databasePath); err != nil {
		return operationEvidenceState{}, err
	}
	dbID, err := inspectEvidenceMember(databasePath)
	if err != nil {
		return operationEvidenceState{}, err
	}
	if !dbID.present {
		return operationEvidenceState{}, os.ErrNotExist
	}
	walID, err := inspectEvidenceMember(databasePath + "-wal")
	if err != nil {
		return operationEvidenceState{}, err
	}
	shmID, err := inspectEvidenceMember(databasePath + "-shm")
	if err != nil {
		return operationEvidenceState{}, err
	}
	journalID, err := inspectEvidenceMember(databasePath + "-journal")
	if err != nil {
		return operationEvidenceState{}, err
	}
	return operationEvidenceState{
		database: dbID,
		wal:      walID,
		shm:      shmID,
		journal:  journalID,
	}, nil
}

func recordedOperationEvidenceWithBaseline(ctx context.Context, databasePath string) (bool, bool, operationEvidenceState, error) {
	before, err := inspectOperationEvidenceState(databasePath)
	if err != nil {
		return false, false, operationEvidenceState{}, err
	}
	if before.wal.present && before.wal.size > 0 {
		return false, true, before, nil
	}
	if beforeOperationEvidenceProbe != nil {
		beforeOperationEvidenceProbe(databasePath)
	}
	inFile, err := RecordedOperationTablePresent(ctx, databasePath)
	if err != nil {
		return false, false, operationEvidenceState{}, err
	}
	after, err := inspectOperationEvidenceState(databasePath)
	if err != nil {
		return false, false, operationEvidenceState{}, err
	}
	if after != before {
		if after.wal.present && after.wal.size > 0 {
			return false, true, after, nil
		}
		return false, false, operationEvidenceState{}, errors.New("database changed during operation evidence probe")
	}
	return inFile, false, after, nil
}

// RecordedOperationEvidence is the pair of facts a reader needs before it may
// answer "no operation recorded": whether the database file names the table, and
// whether a journal beside it may still hold the transaction that creates it.
// Baseline and post-read file + sidecar identities (including ChangeStamp) are
// checked so a concurrent WAL write during the immutable probe routes to a
// private snapshot or fails closed rather than returning a stale negative answer.
func RecordedOperationEvidence(ctx context.Context, databasePath string) (inFile bool, journalPending bool, err error) {
	inFile, journalPending, _, err = recordedOperationEvidenceWithBaseline(ctx, databasePath)
	return inFile, journalPending, err
}

// auditAdmissionSidecars enforces the single-link regular-file policy across
// every journal member beside databasePath and fails closed on a non-empty
// rollback journal, which a read-only admission check cannot safely roll back.
func auditAdmissionSidecars(databasePath string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		info, err := processlock.InspectSourceFile(databasePath + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if suffix == "-journal" && info.Size() > 0 {
			return errors.New("database has a pending rollback journal")
		}
	}
	return nil
}

// recordedTablePresent asks one open handle whether the operation table exists.
func recordedTablePresent(ctx context.Context, db *sql.DB) (bool, error) {
	var present int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master
		where type = 'table' and name = 'upgrade_operations'`).Scan(&present); err != nil {
		return false, err
	}
	return present != 0, nil
}

// admissionDataSourceName is the read-only handle for the operation record.
// `immutable` must be false only for a copy that carries its journal, because an
// immutable handle ignores the journal; it must be true for any path inside the
// source, because a plain read-only open of a WAL database still creates or
// joins `-shm` beside it.
func admissionDataSourceName(path string, immutable bool) string {
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
