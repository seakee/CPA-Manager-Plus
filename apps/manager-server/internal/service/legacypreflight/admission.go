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
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
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
func readRecordedUpgrade(ctx context.Context, a DatabaseUpgradeAdmission) (upgradeRecord, bool, error) {
	path := strings.TrimSpace(a.DatabasePath)
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
// proved: an External target, the source volume the copy-in observed, every
// domain the verdict names, and the External binding that verdict bound —
// including the sealed credential the connect step proved against it. A row
// written before the gate existed carries none of that, and it is refused here
// instead of admitting a store the gate never verified.
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
	if evidence.TargetMode != model.UpgradeTargetExternal {
		// A Managed operation stops at verify until the trusted U1-1b re-pin
		// promotes its authority, so this build can never have recorded one as
		// complete.
		return fmt.Errorf("a complete Upgrade record must name an external target, not %q", evidence.TargetMode)
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
	for _, required := range []string{
		model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig, model.UpgradeDomainIdentity,
	} {
		if !containsRecordedDomain(evidence.Domains, required) {
			return fmt.Errorf("a complete Upgrade record's verification does not name the %q domain", required)
		}
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
	dataKeyPath := strings.TrimSpace(a.DataKeyPath)
	if dataKeyPath == "" {
		dataKeyPath = filepath.Join(filepath.Dir(strings.TrimSpace(a.DatabasePath)), admissionDataKeyFileName)
	}
	key, err := security.LoadExistingDataKey(a.DataKey, dataKeyPath)
	if err != nil {
		return fmt.Errorf("a complete Upgrade record's sealed management secret cannot be read because the Manager data key is missing: %w", err)
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		return fmt.Errorf("initialize the Manager secret protector: %w", err)
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
