package legacypreflight

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// upgradeAdmissionFixture creates a stopped 1.x source: the carried-forward
// settings and usage tables plus the v2 Upgrade operation table, which is the
// table a copy-in leaves behind. It is deliberately not a native-v2 schema.
func upgradeAdmissionFixture(t *testing.T) sqlite.InspectionPaths {
	t.Helper()
	paths := fixturePaths(t)
	db, err := sql.Open("sqlite", paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
		`create table usage_events (id integer primary key autoincrement, event_hash text not null unique, timestamp_ms integer not null)`,
		`create table upgrade_operations (
			id integer primary key check(id = 1),
			stage text not null default '',
			domains_done text not null default '[]',
			source_kind text,
			source_version text,
			source_fingerprint_before text,
			source_fingerprint_after text,
			backup_manifest_sha256 text,
			reconciliation text not null default '{}',
			manual_items text not null default '[]',
			error_code text,
			error_detail text,
			created_at_ms integer not null,
			updated_at_ms integer not null
		)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("create admission fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close admission fixture: %v", err)
	}
	return paths
}

// recordEvidence is the observation half of an operation row. A finished copy-in
// fills every field; a row that only carries a stage cannot describe one. The
// zero value of the last three fields is the whole record a finished External
// operation holds, so a test that only departs from one of them states just that.
type recordEvidence struct {
	sourceKind        string
	fingerprintBefore string
	fingerprintAfter  string
	backupManifest    string
	// domainsDone is the recorded completed-domain list.
	domainsDone string
	// verification is the observation the completion gate writes beside the
	// stage.
	verification string
	// binding is the External connect row that observation names.
	binding *completedBinding
}

func (e recordEvidence) recordedDomains() string {
	if e.domainsDone != "" {
		return e.domainsDone
	}
	return `["source_copy","cpa_config","identity"]`
}

func (e recordEvidence) recordedVerification() string {
	if e.verification != "" {
		return e.verification
	}
	return completedVerification
}

func (e recordEvidence) recordedBinding() *completedBinding {
	if e.binding != nil {
		return e.binding
	}
	return &completedBinding{
		baseURL:      "https://cpa.example",
		identity:     "https://cpa.example",
		generation:   "https://cpa.example",
		observedAtMS: 1_700_000_000_000,
	}
}

// fullCopyInEvidence is what a completed source copy records.
var fullCopyInEvidence = recordEvidence{
	sourceKind:        SourceKindUpgradedLegacy,
	fingerprintBefore: "source-fingerprint",
	fingerprintAfter:  "source-fingerprint",
	backupManifest:    "backup-manifest-sha256",
}

func recordFixtureStage(t *testing.T, databasePath, stage string) {
	t.Helper()
	recordFixtureOperation(t, databasePath, stage, fullCopyInEvidence)
}

// fixtureManagementKey is the External management key a completed fixture's
// binding proves and the Manager's own connection stores. It has the shape the
// production validator accepts.
const fixtureManagementKey = "fixture-management-key"

// fixtureProtector loads the Manager data key beside the fixture store, creating
// it when the fixture has none. The completed row's secret is sealed with this
// key, exactly as the External connect step seals the one it proved.
func fixtureProtector(t *testing.T, databasePath string) *security.Protector {
	t.Helper()
	key, _, err := security.LoadOrCreateDataKey("", filepath.Join(filepath.Dir(databasePath), "data.key"))
	if err != nil {
		t.Fatalf("load the fixture data key: %v", err)
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		t.Fatalf("initialize the fixture protector: %v", err)
	}
	return protector
}

// recordFixtureConnection stores the CPA connection the Manager itself resolves.
// The admission compares the completed binding against it, so a fixture without
// one describes an install the Manager could not administer.
func recordFixtureConnection(t *testing.T, db *sql.DB, protector *security.Protector, baseURL, managementKey string) {
	t.Helper()
	if err := store.New(db, protector).SaveManagerConfig(context.Background(), model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: managementKey},
	}); err != nil {
		t.Fatalf("store the fixture CPA connection: %v", err)
	}
}

func recordFixtureOperation(t *testing.T, databasePath, stage string, evidence recordEvidence) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open admission fixture: %v", err)
	}
	defer func() { _ = db.Close() }()
	// A store written by a build that predates the completion gate has neither
	// the verification column nor the External connect table, so the fixture
	// writes only the record that schema can hold.
	var hasVerification int
	if err := db.QueryRow(`select count(*) from pragma_table_info('upgrade_operations')
		where name = 'verification'`).Scan(&hasVerification); err != nil {
		t.Fatalf("read the fixture schema: %v", err)
	}
	columns := `id, stage, domains_done, source_kind, source_fingerprint_before,
		source_fingerprint_after, backup_manifest_sha256, created_at_ms, updated_at_ms`
	values := []any{1, stage, evidence.recordedDomains(), evidence.sourceKind,
		evidence.fingerprintBefore, evidence.fingerprintAfter, evidence.backupManifest, 1, 1}
	placeholders := `?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?`
	updates := `stage = excluded.stage, domains_done = excluded.domains_done,
		source_kind = excluded.source_kind,
		source_fingerprint_before = excluded.source_fingerprint_before,
		source_fingerprint_after = excluded.source_fingerprint_after,
		backup_manifest_sha256 = excluded.backup_manifest_sha256`
	if hasVerification > 0 {
		columns += `, verification`
		values = append(values, evidence.recordedVerification())
		placeholders += `, ?`
		updates += `, verification = excluded.verification`
	}
	if _, err := db.Exec(`insert into upgrade_operations (`+columns+`) values (`+placeholders+`)
		on conflict(id) do update set `+updates, values...); err != nil {
		t.Fatalf("record fixture stage %q: %v", stage, err)
	}
	var hasBinding int
	if err := db.QueryRow(`select count(*) from sqlite_master
		where type = 'table' and name = 'upgrade_external_connect'`).Scan(&hasBinding); err != nil {
		t.Fatalf("read the fixture binding table: %v", err)
	}
	if hasBinding == 0 {
		return
	}
	binding := evidence.recordedBinding()
	protector := fixtureProtector(t, databasePath)
	sealedKey, err := protector.ProtectString(fixtureManagementKey)
	if err != nil {
		t.Fatalf("seal the fixture management key: %v", err)
	}
	// The completed row is admitted only against the connection the Manager
	// itself stores, so the fixture records the same credential the binding
	// proved.
	recordFixtureConnection(t, db, protector, binding.baseURL, fixtureManagementKey)
	if _, err := db.Exec(`insert into upgrade_external_connect (
		id, base_url, identity, generation, observed_at_ms, management_key
	) values (1, ?, ?, ?, ?, ?)
	on conflict(id) do update set
		base_url = excluded.base_url, identity = excluded.identity,
		generation = excluded.generation, observed_at_ms = excluded.observed_at_ms,
		management_key = excluded.management_key`,
		binding.baseURL, binding.identity, binding.generation, binding.observedAtMS, sealedKey); err != nil {
		t.Fatalf("record fixture binding: %v", err)
	}
}

// assertNoUpgradeAuthority keeps the admission seam honest: it may decide
// startup, never Upgrade authority.
func assertNoUpgradeAuthority(t *testing.T, report Report) {
	t.Helper()
	if report.UpgradeAuthorized {
		t.Fatal("report carries Upgrade authority")
	}
	if report.UpgradePreconditions != nil && report.UpgradePreconditions.TrustedHopReceipt {
		t.Fatal("report carries a trusted hop receipt")
	}
}

type failingAdmission struct{}

func (failingAdmission) Admitted(context.Context) (UpgradeStage, error) {
	return UpgradeStageNone, errors.New("admission record unreadable")
}

func TestStartupAllowedKeepsEveryExistingRule(t *testing.T) {
	for _, test := range []struct {
		name   string
		kind   string
		stage  UpgradeStage
		block  bool
		expect bool
	}{
		{name: "fresh unchanged", kind: SourceKindFresh, stage: UpgradeStageNone, expect: true},
		{name: "native v2 unchanged", kind: SourceKindNativeV2, stage: UpgradeStageNone, expect: true},
		{name: "native v2 in progress", kind: SourceKindNativeV2, stage: UpgradeStageInProgress, expect: true},
		{name: "native v2 complete", kind: SourceKindNativeV2, stage: UpgradeStageComplete, expect: true},
		{name: "legacy without a record", kind: SourceKindLegacyManager, stage: UpgradeStageNone, expect: false},
		// A copy-in migrates the target, so a 1.x schema can never be the
		// producer of a recorded operation: the classification keeps deciding.
		{name: "legacy in progress", kind: SourceKindLegacyManager, stage: UpgradeStageInProgress, expect: false},
		{name: "legacy complete", kind: SourceKindLegacyManager, stage: UpgradeStageComplete, expect: false},
		{name: "already relabelled in progress", kind: SourceKindUpgradedLegacy, stage: UpgradeStageInProgress, expect: true},
		{name: "already relabelled complete", kind: SourceKindUpgradedLegacy, stage: UpgradeStageComplete, expect: true},
		{name: "already relabelled without a record", kind: SourceKindUpgradedLegacy, stage: UpgradeStageNone, expect: false},
		{name: "blocked legacy with a complete record", kind: SourceKindLegacyManager, stage: UpgradeStageComplete, block: true, expect: false},
		{name: "blocked native v2", kind: SourceKindNativeV2, stage: UpgradeStageNone, block: true, expect: false},
		{name: "partial source with a complete record", kind: SourceKindPartialSource, stage: UpgradeStageComplete, expect: false},
		{name: "unknown with an in-progress record", kind: SourceKindUnknown, stage: UpgradeStageInProgress, expect: false},
		{name: "fresh with an in-progress record", kind: SourceKindFresh, stage: UpgradeStageInProgress, expect: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := Report{SourceKind: test.kind}
			if test.block {
				report.Blocked = []string{"upgrade_source_busy"}
			}
			// The seam never touches the authorization flags.
			assertNoUpgradeAuthority(t, report)
			if allowed := startupAllowed(report, test.stage); allowed != test.expect {
				t.Fatalf("startupAllowed(%s, %s) = %t, want %t", test.kind, test.stage, allowed, test.expect)
			}
			assertNoUpgradeAuthority(t, report)
		})
	}
}

func TestUpgradeSurfaceTable(t *testing.T) {
	for _, test := range []struct {
		stage UpgradeStage
		only  bool
	}{
		{stage: UpgradeStageNone, only: false},
		{stage: "", only: false},
		{stage: UpgradeStageInProgress, only: true},
		{stage: UpgradeStageComplete, only: false},
	} {
		if got := UpgradeSurfaceOnly(test.stage); got != test.only {
			t.Fatalf("UpgradeSurfaceOnly(%q) = %t, want %t", test.stage, got, test.only)
		}
		admission := &Admission{UpgradeStage: test.stage}
		want := SurfaceFull
		if test.only {
			want = SurfaceUpgradeOnly
		}
		if got := admission.Surface(); got != want {
			t.Fatalf("surface for %q = %q, want %q", test.stage, got, want)
		}
	}
	if surface := (*Admission)(nil).Surface(); surface != "" {
		t.Fatalf("nil admission surface = %q, want none", surface)
	}
}

func TestNilUpgradeAdmissionKeepsHistoricalStartup(t *testing.T) {
	stage, err := upgradeAdmissionStage(context.Background(), nil)
	if err != nil || stage != UpgradeStageNone {
		t.Fatalf("nil reader = %q/%v, want none", stage, err)
	}
	paths := upgradeAdmissionFixture(t)
	recordFixtureStage(t, paths.Database, "copy_in")
	// A recorded stage without an injected reader is not evidence: the legacy
	// source stays rejected exactly as it was before this seam.
	if _, err := AdmitStartup(context.Background(), paths, Options{}); err == nil {
		t.Fatal("a legacy source was admitted with a nil admission reader")
	}
}

func TestUpgradeAdmissionFailsClosed(t *testing.T) {
	if _, err := upgradeAdmissionStage(context.Background(), failingAdmission{}); err == nil {
		t.Fatal("an unreadable admission record was treated as no record")
	}
	if _, err := upgradeAdmissionStage(context.Background(), staticAdmission(UpgradeStage("bogus"))); err == nil {
		t.Fatal("an unsupported admission stage was accepted")
	}
	paths := upgradeAdmissionFixture(t)
	if _, err := AdmitStartup(context.Background(), paths, Options{UpgradeAdmission: failingAdmission{}}); err == nil {
		t.Fatal("startup was authorized while the admission record was unreadable")
	}
	if _, err := os.Lstat(paths.Database + ".manager.lock"); !os.IsNotExist(err) {
		t.Fatalf("failed admission created an ownership marker (err=%v)", err)
	}
}

type staticAdmission UpgradeStage

func (s staticAdmission) Admitted(context.Context) (UpgradeStage, error) {
	return UpgradeStage(s), nil
}

func TestDatabaseUpgradeAdmissionReadsTheRecordedStage(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "usage.sqlite")
	if stage, err := (DatabaseUpgradeAdmission{DatabasePath: missing}).Admitted(context.Background()); err != nil || stage != UpgradeStageNone {
		t.Fatalf("missing database = %q/%v, want none", stage, err)
	}

	paths := fixturePaths(t)
	if err := os.WriteFile(paths.Database, []byte{}, 0o600); err != nil {
		t.Fatalf("write empty database: %v", err)
	}
	if stage, err := (DatabaseUpgradeAdmission{DatabasePath: paths.Database}).Admitted(context.Background()); err != nil || stage != UpgradeStageNone {
		t.Fatalf("empty database = %q/%v, want none", stage, err)
	}

	// A store that never ran the incremental migration has no table at all.
	native, err := sqlite.Open(paths.Database)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	if _, err := native.Exec(`delete from upgrade_operations`); err != nil {
		_ = native.Close()
		t.Fatalf("clear recorded operation: %v", err)
	}
	if err := native.Close(); err != nil {
		t.Fatalf("close migrated store: %v", err)
	}
	reader := DatabaseUpgradeAdmission{DatabasePath: paths.Database}
	if stage, err := reader.Admitted(context.Background()); err != nil || stage != UpgradeStageNone {
		t.Fatalf("store without a row = %q/%v, want none", stage, err)
	}

	for _, test := range []struct {
		recorded string
		expect   UpgradeStage
	}{
		{recorded: "preflight", expect: UpgradeStageInProgress},
		{recorded: "copy_in", expect: UpgradeStageInProgress},
		{recorded: "reconcile", expect: UpgradeStageInProgress},
		{recorded: "verify", expect: UpgradeStageInProgress},
		{recorded: "failed", expect: UpgradeStageInProgress},
		{recorded: "complete", expect: UpgradeStageComplete},
	} {
		recordFixtureStage(t, paths.Database, test.recorded)
		stage, err := reader.Admitted(context.Background())
		if err != nil || stage != test.expect {
			t.Fatalf("recorded %q = %q/%v, want %q", test.recorded, stage, err, test.expect)
		}
	}

	// A `complete` row that does not describe a finished copy-in cannot unlock
	// the normal surfaces: each missing observation fails closed.
	for _, test := range []struct {
		name     string
		evidence recordEvidence
	}{
		{name: "no source kind", evidence: recordEvidence{
			fingerprintBefore: "before", fingerprintAfter: "before", backupManifest: "digest",
		}},
		{name: "a 1.x source kind", evidence: recordEvidence{
			sourceKind: SourceKindLegacyManager, fingerprintBefore: "before", fingerprintAfter: "before", backupManifest: "digest",
		}},
		{name: "no fingerprints", evidence: recordEvidence{
			sourceKind: SourceKindUpgradedLegacy, backupManifest: "digest",
		}},
		{name: "one fingerprint", evidence: recordEvidence{
			sourceKind: SourceKindUpgradedLegacy, fingerprintBefore: "before", backupManifest: "digest",
		}},
		{name: "no backup digest", evidence: recordEvidence{
			sourceKind: SourceKindUpgradedLegacy, fingerprintBefore: "before", fingerprintAfter: "before",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recordFixtureOperation(t, paths.Database, "complete", test.evidence)
			if stage, err := reader.Admitted(context.Background()); err == nil {
				t.Fatalf("a complete row with %s was read as %q", test.name, stage)
			}
		})
	}

	// Reading never adds a WAL/shm sidecar beside the store.
	for _, sidecar := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(paths.Database + sidecar); !os.IsNotExist(err) {
			t.Fatalf("admission read created %s (err=%v)", sidecar, err)
		}
	}
}

// recordedUpgradeFixture is a recognized 1.x source (the v1.14.4 profile
// fixture) plus the Upgrade operation table a copy-in leaves behind.
func recordedUpgradeFixture(t *testing.T) sqlite.InspectionPaths {
	t.Helper()
	var chosen legacyProfileFixture
	for _, fixture := range loadLegacyProfileFixtures(t) {
		if fixture.Ref == "v1.14.4" {
			chosen = fixture
		}
	}
	if chosen.Ref == "" {
		t.Fatal("v1.14.4 profile fixture is missing")
	}
	paths := buildLegacyProfileFixture(t, chosen)
	execLegacyFixture(t, paths.Database, `create table upgrade_operations (
		id integer primary key check(id = 1),
		stage text not null default '',
		domains_done text not null default '[]',
		source_kind text,
		source_version text,
		source_fingerprint_before text,
		source_fingerprint_after text,
		backup_manifest_sha256 text,
		reconciliation text not null default '{}',
		manual_items text not null default '[]',
		error_code text,
		error_detail text,
		created_at_ms integer not null,
		updated_at_ms integer not null
	)`)
	return paths
}

// TestLegacySchemaNeverReachesTheFullSurface is the regression for the removed
// `legacy-manager` relabel: a 1.x schema with a complete row (full evidence and
// all) was admitted to the full surface, which no legitimate producer can
// create. The classification must keep deciding.
func TestLegacySchemaNeverReachesTheFullSurface(t *testing.T) {
	paths := recordedUpgradeFixture(t)
	if report, err := Inspect(context.Background(), paths, Options{SourceStopped: true}); err != nil || report.SourceKind != SourceKindLegacyManager {
		t.Fatalf("fixture classification = %q/%v, want %s", report.SourceKind, err, SourceKindLegacyManager)
	}
	if _, err := AdmitStartup(context.Background(), paths, Options{}); err == nil {
		t.Fatal("a legacy source was admitted without a recorded Upgrade operation")
	}

	for _, recorded := range []string{"copy_in", "failed", "complete"} {
		t.Run(recorded, func(t *testing.T) {
			recordFixtureStage(t, paths.Database, recorded)
			report, err := Inspect(context.Background(), paths, Options{SourceStopped: true})
			if err != nil {
				t.Fatalf("inspect with a recorded %s operation: %v", recorded, err)
			}
			if report.UpgradeAuthorized {
				t.Fatal("the preflight report gained Upgrade authority from a recorded operation")
			}
			admission, err := AdmitStartup(context.Background(), paths, Options{
				UpgradeAdmission: DatabaseUpgradeAdmission{DatabasePath: paths.Database},
			})
			if err == nil {
				_ = admission.Close()
				t.Fatalf("a 1.x schema with a recorded %s operation was admitted", recorded)
			}
			if _, statErr := os.Lstat(paths.Database + ".manager.lock"); !os.IsNotExist(statErr) {
				t.Fatalf("a refused %s admission created an ownership marker (err=%v)", recorded, statErr)
			}
		})
	}
}

// TestCopiedInStoreIsAdmittedOnlyWithProperEvidence walks the positive half of
// the same rule on the store a copy-in actually leaves: the migration ran, so
// the schema is native-v2 and only the row decides the surface.
func TestCopiedInStoreIsAdmittedOnlyWithProperEvidence(t *testing.T) {
	paths := fixturePaths(t)
	store, err := sqlite.Open(paths.Database)
	if err != nil {
		t.Fatalf("create the migrated v2 store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close the migrated v2 store: %v", err)
	}
	recordFixtureStage(t, paths.Database, "copy_in")
	assertUpgradeAdmission(t, paths.Database, UpgradeStageInProgress, SurfaceUpgradeOnly)

	recordFixtureStage(t, paths.Database, "complete")
	assertUpgradeAdmission(t, paths.Database, UpgradeStageComplete, SurfaceFull)

	recordFixtureOperation(t, paths.Database, "complete", recordEvidence{sourceKind: SourceKindUpgradedLegacy})
	if stage, err := (DatabaseUpgradeAdmission{DatabasePath: paths.Database}).Admitted(context.Background()); err == nil {
		t.Fatalf("a complete row without fingerprints was read as %q", stage)
	}
}

// TestCopyInMarkerKeepsAnUnfinishedCopyInInProgress is the crash-window
// regression: the copy-in migrated the copied store but died before it could
// record the operation. The schema now says native-v2 and there is no row, so
// without the marker this store is admitted as a finished install.
func TestCopyInMarkerKeepsAnUnfinishedCopyInInProgress(t *testing.T) {
	paths := fixturePaths(t)
	store, err := sqlite.Open(paths.Database)
	if err != nil {
		t.Fatalf("create the migrated v2 store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close the migrated v2 store: %v", err)
	}
	if stage, err := (DatabaseUpgradeAdmission{DatabasePath: paths.Database}).Admitted(context.Background()); err != nil || stage != UpgradeStageNone {
		t.Fatalf("a migrated store without a row = %q/%v, want none", stage, err)
	}

	marker := copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseMaterializing,
		TargetDatabase:          paths.Database,
		SourceKind:              SourceKindUpgradedLegacy,
		SourceFingerprintBefore: "source-fingerprint",
		BackupManifestSHA256:    "backup-manifest-sha256",
		WrittenAtMS:             1,
	}
	if err := copyinmarker.Write(filepath.Dir(paths.Database), marker); err != nil {
		t.Fatalf("write the copy-in marker: %v", err)
	}
	assertUpgradeAdmission(t, paths.Database, UpgradeStageInProgress, SurfaceUpgradeOnly)

	// The marker belongs to the data root it sits in, so a relocated root — the
	// host running the CLI, a container running the server — is still a store
	// with an unfinished copy-in. Reading it as "no record" used to fail open on
	// a store whose migration ran and whose row did not.
	relocated := marker
	relocated.TargetDatabase = filepath.Join(t.TempDir(), "mounted-data", filepath.Base(paths.Database))
	if err := copyinmarker.Write(filepath.Dir(paths.Database), relocated); err != nil {
		t.Fatalf("write a relocated copy-in marker: %v", err)
	}
	assertUpgradeAdmission(t, paths.Database, UpgradeStageInProgress, SurfaceUpgradeOnly)

	// A marker that names a different database file is evidence for some other
	// run, and admitting this store on it would be guessing: it fails closed.
	other := marker
	other.TargetDatabase = filepath.Join(t.TempDir(), "other.sqlite")
	if err := copyinmarker.Write(filepath.Dir(paths.Database), other); err != nil {
		t.Fatalf("write a foreign copy-in marker: %v", err)
	}
	if stage, err := (DatabaseUpgradeAdmission{DatabasePath: paths.Database}).Admitted(context.Background()); err == nil {
		t.Fatalf("marker for another database was read as %q, want a refusal", stage)
	}

	// An unreadable marker fails closed.
	if err := os.WriteFile(copyinmarker.Path(filepath.Dir(paths.Database)), []byte("{"), 0o600); err != nil {
		t.Fatalf("corrupt the copy-in marker: %v", err)
	}
	if _, err := (DatabaseUpgradeAdmission{DatabasePath: paths.Database}).Admitted(context.Background()); err == nil {
		t.Fatal("a corrupt copy-in marker was treated as no marker")
	}
}

// assertUpgradeAdmission resolves the reader and checks both halves of the
// decision: the admission stage and the surface a native-v2 store gets.
func assertUpgradeAdmission(t *testing.T, databasePath string, wantStage UpgradeStage, wantSurface StartupSurface) {
	t.Helper()
	stage, err := upgradeAdmissionStage(context.Background(), DatabaseUpgradeAdmission{DatabasePath: databasePath})
	if err != nil {
		t.Fatalf("admission = %v", err)
	}
	if stage != wantStage {
		t.Fatalf("admission stage = %q, want %q", stage, wantStage)
	}
	if !startupAllowed(Report{SourceKind: SourceKindNativeV2}, stage) {
		t.Fatalf("a native-v2 store was not admitted at stage %q", stage)
	}
	if surface := (&Admission{UpgradeStage: stage}).Surface(); surface != wantSurface {
		t.Fatalf("surface = %q, want %q", surface, wantSurface)
	}
}

// Unpublished targets are never opened, even if a foreign file appears there.
func TestMaterializingMarkerIsDecidedWithoutReadingTheStore(t *testing.T) {
	paths := fixturePaths(t)
	// A target no reader can open stands in for "the store was not read at
	// all": deciding from the marker has to succeed without touching it.
	if err := os.WriteFile(paths.Database, []byte("the copy-in target is not adopted yet"), 0o600); err != nil {
		t.Fatalf("write the unadopted target: %v", err)
	}
	marker := copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseMaterializing,
		TargetDatabase:          paths.Database,
		SourceKind:              SourceKindUpgradedLegacy,
		SourceFingerprintBefore: "source-fingerprint",
		BackupManifestSHA256:    "backup-manifest-sha256",
		WrittenAtMS:             1,
	}
	if err := copyinmarker.Write(filepath.Dir(paths.Database), marker); err != nil {
		t.Fatalf("write the materializing marker: %v", err)
	}
	stage, err := (DatabaseUpgradeAdmission{DatabasePath: paths.Database}).Admitted(context.Background())
	if err != nil {
		t.Fatalf("admission read the unadopted target: %v", err)
	}
	if stage != UpgradeStageInProgress {
		t.Fatalf("admission = %q, want %q", stage, UpgradeStageInProgress)
	}
}

// A committed row outlives the committing marker after atomic publication.
func TestCommittedRowOutlivesTheCommittingMarker(t *testing.T) {
	paths := fixturePaths(t)
	store, err := sqlite.Open(paths.Database)
	if err != nil {
		t.Fatalf("create the migrated v2 store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close the migrated v2 store: %v", err)
	}
	recordFixtureStage(t, paths.Database, "complete")
	marker := copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseCommitting,
		TargetDatabase:          paths.Database,
		SourceKind:              SourceKindUpgradedLegacy,
		SourceFingerprintBefore: "source-fingerprint",
		SourceFingerprintAfter:  "source-fingerprint",
		BackupManifestSHA256:    "backup-manifest-sha256",
		WrittenAtMS:             1,
	}
	if err := copyinmarker.Write(filepath.Dir(paths.Database), marker); err != nil {
		t.Fatalf("write the committing marker: %v", err)
	}
	assertUpgradeAdmission(t, paths.Database, UpgradeStageComplete, SurfaceFull)
}

// completedOperationFixture seeds the row a finished operation holds: the
// copy-in observations, the completed domains, the verification the completion
// gate records and the External binding that verdict names. A test replaces one
// half of it, so the admission has to re-check every half instead of trusting
// the stage.
type completedOperationFixture struct {
	paths sqlite.InspectionPaths
	// verification is the recorded verification block. An empty value writes the
	// schema default `{}`, which is what a row recorded before the gate exists.
	verification string
	domainsDone  string
	// preVerification drops the verification column and the binding table, so the
	// store holds the schema a build that predates the completion gate leaves.
	preVerification bool
	// binding is the External connect binding. nil writes no binding row, which
	// is the shape of a store whose connect step never recorded its target.
	binding *completedBinding
	// managedTarget writes the Managed shape instead: a verdict with no binding,
	// the destination evidence the CPA import records, and the Manager's own
	// sealed connection for it.
	managedTarget *managedCompletion
}

// managedCompletion is the Managed half of a completed fixture.
type managedCompletion struct {
	// omitDestination writes no upgrade_cpa_import_destination_v1 row, which is
	// the shape of a store whose handoff did not survive.
	omitDestination bool
	// destinationSecret is the secret the recorded destination attempt holds. An
	// empty value holds the Manager's own secret, which is what the import writes.
	destinationSecret string
	// mutate adjusts the recorded destination document before it is written.
	mutate func(map[string]any)
	// secret selects how the Manager connection's management secret is written.
	secret bindingSecret
	// managerSecret is the secret the Manager holds. An empty value stores the
	// fixture key.
	managerSecret string
}

// managedFixtureAddress is the endpoint both the recorded destination and the
// Manager connection name in the Managed control.
const managedFixtureAddress = "https://cpa.example"

// managedCompletionVerification is the verdict the gate records for a Managed
// completion: no binding, and the two domains the target steps record.
const managedCompletionVerification = `{"targetMode":"managed","sourceFingerprint":"source-fingerprint",` +
	`"domains":["source_copy","cpa_config"],"observedAtMs":1700000000001}`

// managedFixtureFingerprint is the server fingerprint a completed Managed
// import records.
func managedFixtureFingerprint() string {
	return freshkeyapply.ExternalServerFingerprint("v8.0.13", strings.Repeat("a", 64))
}

// managedFixtureSourceIdentity is the identity the managed source stamps: the
// `legacy-managed:sha256:` prefix and the digest of the configuration bytes the
// destination was staged for.
var managedFixtureSourceIdentity = "legacy-managed:sha256:" + strings.Repeat("c", 64)

// managedFixtureDestination builds the destination evidence a completed Managed
// import writes, sealed with the same protector that seals the Manager
// connection. It carries the whole provenance the admission re-checks: the attempt
// identity derived from the source, and the request the Supervisor published.
func (f completedOperationFixture) managedFixtureDestination(protector *security.Protector, sealedSecret string) (string, error) {
	sealedDestination := sealedSecret
	if f.managedTarget.destinationSecret != "" {
		sealed, err := protector.ProtectString(f.managedTarget.destinationSecret)
		if err != nil {
			return "", err
		}
		sealedDestination = sealed
	}
	qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
	if err != nil {
		return "", err
	}
	document := map[string]any{
		"baseUrl":           managedFixtureAddress,
		"urlProved":         true,
		"serverFingerprint": managedFixtureFingerprint(),
		"managementKey":     sealedDestination,
		"intent": map[string]any{
			"sourceIdentity":            managedFixtureSourceIdentity,
			"sourceClass":               "legacy_managed",
			"expectedRuntimeIdentity":   "runtime-identity-1",
			"expectedRuntimeGeneration": 4,
			"expectedActiveArtifactId":  string(qualifiedArtifact),
			"apiKeys":                   []string{"legacy-client-key-1"},
			"pluginsEnabled":            true,
		},
		"operationId": managedDestinationOperationID(managedFixtureSourceIdentity, 1),
		"attempt":     1,
		"updatedAtMs": 1,
	}
	if f.managedTarget.mutate != nil {
		f.managedTarget.mutate(document)
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

// mutateManagedIntent reaches into the destination document's request, which is
// where the provenance fields the admission re-checks live.
func mutateManagedIntent(mutate func(intent map[string]any)) func(document map[string]any) {
	return func(document map[string]any) {
		intent, ok := document["intent"].(map[string]any)
		if !ok {
			intent = map[string]any{}
			document["intent"] = intent
		}
		mutate(intent)
	}
}

// completedBinding is the External connect row a completed External operation
// names.
type completedBinding struct {
	baseURL      string
	identity     string
	generation   string
	observedAtMS int64
	// secret selects how the binding's management secret is written. The zero
	// value is the record a real connect step leaves.
	secret bindingSecret
	// managerSecret is the secret the Manager's own CPA connection holds. An
	// empty value stores the key the binding proves.
	managerSecret string
}

// bindingSecret is how a completed fixture writes the binding's management
// secret. Each value is a shape a store can really hold, and each is one the
// admission has to refuse unless it is the sealed, decryptable, matching one.
type bindingSecret int

const (
	// bindingSecretSealed seals the proved key with the target data key.
	bindingSecretSealed bindingSecret = iota
	// bindingSecretMissing writes the empty value a row recorded before the
	// column existed carries after the migration backfills it.
	bindingSecretMissing
	// bindingSecretPlaintext writes the key without an envelope, which
	// UnprotectString would otherwise hand back as if it were readable.
	bindingSecretPlaintext
	// bindingSecretColumnAbsent writes the pre-R2 table: no column at all.
	bindingSecretColumnAbsent
	// bindingSecretForeignKey seals the key with a data key this Manager does
	// not hold.
	bindingSecretForeignKey
)

// preR2BindingSchema is the External connect table a build that predates the
// sealed management secret created.
const preR2BindingSchema = `create table upgrade_external_connect (
	id integer primary key check(id = 1),
	base_url text not null,
	identity text not null,
	generation text not null,
	server_fingerprint text not null default '',
	cpa_version text not null default '',
	support text not null default '',
	client_keys integer not null default 0,
	observed_at_ms integer not null
)`

const completedVerification = `{"targetMode":"external","sourceFingerprint":"source-fingerprint",` +
	`"domains":["source_copy","cpa_config","identity"],` +
	`"bindingIdentity":"https://cpa.example","bindingGeneration":"https://cpa.example",` +
	`"bindingObservedAtMs":1700000000000,"observedAtMs":1700000000001}`

// newCompletedOperationFixture creates the store a copy-in leaves behind with a
// complete row and its binding: the positive control every variant departs from.
func newCompletedOperationFixture(t *testing.T) completedOperationFixture {
	t.Helper()
	paths := fixturePaths(t)
	store, err := sqlite.Open(paths.Database)
	if err != nil {
		t.Fatalf("create the migrated v2 store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close the migrated v2 store: %v", err)
	}
	return completedOperationFixture{
		paths:        paths,
		verification: completedVerification,
		domainsDone:  `["source_copy","cpa_config","identity"]`,
		binding: &completedBinding{
			baseURL:      "https://cpa.example",
			identity:     "https://cpa.example",
			generation:   "https://cpa.example",
			observedAtMS: 1_700_000_000_000,
		},
	}
}

// preGateOperationSchema is the operation table a build that predates the
// completion gate created: no verification column.
const preGateOperationSchema = `create table upgrade_operations (
	id integer primary key check(id = 1),
	stage text not null default '',
	domains_done text not null default '[]',
	source_kind text,
	source_version text,
	source_fingerprint_before text,
	source_fingerprint_after text,
	backup_manifest_sha256 text,
	reconciliation text not null default '{}',
	manual_items text not null default '[]',
	error_code text,
	error_detail text,
	created_at_ms integer not null,
	updated_at_ms integer not null
)`

func (f completedOperationFixture) record(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite", f.paths.Database)
	if err != nil {
		t.Fatalf("open the completed fixture store: %v", err)
	}
	defer func() { _ = db.Close() }()
	if f.managedTarget != nil {
		f.recordManaged(t, db)
		return
	}
	if f.preVerification {
		for _, statement := range []string{
			`drop table upgrade_operations`,
			`drop table if exists upgrade_external_connect`,
			preGateOperationSchema,
		} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("write the pre-gate schema: %v", err)
			}
		}
		if _, err := db.Exec(`insert into upgrade_operations (
			id, stage, domains_done, source_kind, source_fingerprint_before,
			source_fingerprint_after, backup_manifest_sha256, created_at_ms, updated_at_ms
		) values (1, 'complete', ?, 'upgraded-legacy', 'source-fingerprint',
			'source-fingerprint', 'backup-manifest-sha256', 1, 1)`, f.domainsDone); err != nil {
			t.Fatalf("record the pre-gate completed row: %v", err)
		}
		return
	}
	if _, err := db.Exec(`insert into upgrade_operations (
		id, stage, last_forward_stage, domains_done, source_kind,
		source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256,
		verification, created_at_ms, updated_at_ms
	) values (1, 'complete', 'complete', ?, 'upgraded-legacy', 'source-fingerprint',
		'source-fingerprint', 'backup-manifest-sha256', ?, 1, 1)`,
		f.domainsDone, f.verification); err != nil {
		t.Fatalf("record the completed fixture row: %v", err)
	}
	f.recordBinding(t, db)
}

// recordBinding writes the External connect row, including the sealed secret
// shape under test. A Managed fixture calls it only to prove an External binding
// on a Managed verdict is refused.
func (f completedOperationFixture) recordBinding(t *testing.T, db *sql.DB) {
	t.Helper()
	if f.binding == nil {
		return
	}
	protector := fixtureProtector(t, f.paths.Database)
	if f.binding.secret == bindingSecretColumnAbsent {
		for _, statement := range []string{`drop table upgrade_external_connect`, preR2BindingSchema} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("write the pre-R2 binding schema: %v", err)
			}
		}
	}
	managerSecret := f.binding.managerSecret
	if managerSecret == "" {
		managerSecret = fixtureManagementKey
	}
	recordFixtureConnection(t, db, protector, f.binding.baseURL, managerSecret)
	if f.binding.secret == bindingSecretColumnAbsent {
		if _, err := db.Exec(`insert into upgrade_external_connect (
			id, base_url, identity, generation, observed_at_ms
		) values (1, ?, ?, ?, ?)`,
			f.binding.baseURL, f.binding.identity, f.binding.generation, f.binding.observedAtMS); err != nil {
			t.Fatalf("record the pre-R2 binding: %v", err)
		}
		return
	}
	stored, err := f.binding.storedSecret(protector)
	if err != nil {
		t.Fatalf("seal the fixture binding secret: %v", err)
	}
	if _, err := db.Exec(`insert into upgrade_external_connect (
		id, base_url, identity, generation, observed_at_ms, management_key
	) values (1, ?, ?, ?, ?, ?)`,
		f.binding.baseURL, f.binding.identity, f.binding.generation, f.binding.observedAtMS, stored); err != nil {
		t.Fatalf("record the completed fixture binding: %v", err)
	}
}

// storedSecret is the value the fixture writes into the binding's
// management_key column, exactly as the shape under test would hold it.
func (b completedBinding) storedSecret(protector *security.Protector) (string, error) {
	switch b.secret {
	case bindingSecretMissing:
		return "", nil
	case bindingSecretPlaintext:
		return fixtureManagementKey, nil
	case bindingSecretForeignKey:
		foreign, err := security.NewProtector([]byte("a-data-key-this-manager-does-not-hold"))
		if err != nil {
			return "", err
		}
		return foreign.ProtectString(fixtureManagementKey)
	default:
		return protector.ProtectString(fixtureManagementKey)
	}
}

// recordManaged writes the Managed completion: the operation row, the Manager's
// own connection with the secret shape under test, and the destination evidence.
func (f completedOperationFixture) recordManaged(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`insert into upgrade_operations (
		id, stage, last_forward_stage, domains_done, source_kind,
		source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256,
		verification, created_at_ms, updated_at_ms
	) values (1, 'complete', 'complete', ?, 'upgraded-legacy', 'source-fingerprint',
		'source-fingerprint', 'backup-manifest-sha256', ?, 1, 1)`,
		f.domainsDone, f.verification); err != nil {
		t.Fatalf("record the managed completed row: %v", err)
	}
	protector := fixtureProtector(t, f.paths.Database)
	secret := f.managedTarget.managerSecret
	if secret == "" {
		secret = fixtureManagementKey
	}
	var stored string
	switch f.managedTarget.secret {
	case bindingSecretPlaintext:
		stored = secret
	case bindingSecretForeignKey:
		foreign, err := security.NewProtector([]byte("a-data-key-this-manager-does-not-hold"))
		if err != nil {
			t.Fatalf("initialize the foreign protector: %v", err)
		}
		stored, err = foreign.ProtectString(secret)
		if err != nil {
			t.Fatalf("seal the connection secret with a foreign key: %v", err)
		}
	default:
		sealed, err := protector.ProtectString(secret)
		if err != nil {
			t.Fatalf("seal the fixture connection secret: %v", err)
		}
		stored = sealed
	}
	payload, err := json.Marshal(model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: managedFixtureAddress, ManagementKey: stored},
	})
	if err != nil {
		t.Fatalf("encode the fixture connection: %v", err)
	}
	if _, err := db.Exec(`insert into settings (key, value, updated_at_ms) values ('manager_config_v1', ?, 1)
		on conflict (key) do update set value = excluded.value`, string(payload)); err != nil {
		t.Fatalf("record the Manager connection: %v", err)
	}
	if !f.managedTarget.omitDestination {
		destination, err := f.managedFixtureDestination(protector, stored)
		if err != nil {
			t.Fatalf("build the CPA import destination: %v", err)
		}
		if _, err := db.Exec(`insert into settings (key, value, updated_at_ms) values (?, ?, 1)
			on conflict (key) do update set value = excluded.value`,
			admissionImportDestinationKeySetting, destination); err != nil {
			t.Fatalf("record the CPA import destination: %v", err)
		}
	}
	// A Managed verdict must carry no binding; a fixture that writes one is the
	// contradiction the admission has to refuse.
	f.recordBinding(t, db)
}

// TestCompletedOperationAdmissionRequiresTheGateEvidence is the R1 regression:
// a `complete` label is not a verdict. The admission must read the verification
// the completion gate records beside the stage, and refuse the row when the
// evidence is missing, names another target, another source volume or another
// binding. Before this check the three copy-in observations alone unlocked the
// full writable product for a row that never passed the gate.
func TestCompletedOperationAdmissionRequiresTheGateEvidence(t *testing.T) {
	ctx := context.Background()

	// The positive control: the record a real gate writes is admitted.
	control := newCompletedOperationFixture(t)
	control.record(t)
	assertUpgradeAdmission(t, control.paths.Database, UpgradeStageComplete, SurfaceFull)
	admission, err := AdmitStartup(ctx, control.paths, Options{
		UpgradeAdmission: DatabaseUpgradeAdmission{DatabasePath: control.paths.Database},
	})
	if err != nil {
		t.Fatalf("admit the recorded completion: %v", err)
	}
	if err := admission.Close(); err != nil {
		t.Fatalf("release the admitted completion: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*completedOperationFixture)
	}{
		{name: "no verification evidence", mutate: func(f *completedOperationFixture) {
			f.verification = "{}"
		}},
		{name: "a verification column the schema never had", mutate: func(f *completedOperationFixture) {
			f.preVerification = true
		}},
		{name: "a foreign source fingerprint", mutate: func(f *completedOperationFixture) {
			f.verification = `{"targetMode":"external","sourceFingerprint":"another-volume",` +
				`"domains":["source_copy","cpa_config","identity"],` +
				`"bindingIdentity":"https://cpa.example","bindingGeneration":"https://cpa.example",` +
				`"bindingObservedAtMs":1700000000000,"observedAtMs":1700000000001}`
		}},
		{name: "a completed domain the row never recorded", mutate: func(f *completedOperationFixture) {
			f.domainsDone = `["source_copy"]`
		}},
		{name: "a verification that names no identity domain", mutate: func(f *completedOperationFixture) {
			f.verification = `{"targetMode":"external","sourceFingerprint":"source-fingerprint",` +
				`"domains":["source_copy","cpa_config"],` +
				`"bindingIdentity":"https://cpa.example","bindingGeneration":"https://cpa.example",` +
				`"bindingObservedAtMs":1700000000000,"observedAtMs":1700000000001}`
		}},
		{name: "no external binding", mutate: func(f *completedOperationFixture) {
			f.binding = nil
		}},
		{name: "a binding generation the verdict did not observe", mutate: func(f *completedOperationFixture) {
			f.binding.observedAtMS = 1_700_000_000_999
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCompletedOperationFixture(t)
			test.mutate(&fixture)
			fixture.record(t)
			if stage, err := (DatabaseUpgradeAdmission{DatabasePath: fixture.paths.Database}).Admitted(ctx); err == nil {
				t.Fatalf("a complete row with %s was read as %q", test.name, stage)
			}
			refused, err := AdmitStartup(ctx, fixture.paths, Options{
				UpgradeAdmission: DatabaseUpgradeAdmission{DatabasePath: fixture.paths.Database},
			})
			if err == nil {
				_ = refused.Close()
				t.Fatalf("a complete row with %s was admitted to startup", test.name)
			}
		})
	}
}

// TestCompletedOperationAdmissionAdmitsAManagedCompletion is the positive control
// for the Managed verdict: the recorded destination evidence, the Manager's own
// sealed connection and the gate's verdict together unlock the full surface, with
// no External binding anywhere in the store.
func TestCompletedOperationAdmissionAdmitsAManagedCompletion(t *testing.T) {
	ctx := context.Background()
	fixture := newCompletedOperationFixture(t)
	fixture.binding = nil
	fixture.verification = managedCompletionVerification
	fixture.domainsDone = `["source_copy","cpa_config"]`
	fixture.managedTarget = &managedCompletion{}
	fixture.record(t)

	assertUpgradeAdmission(t, fixture.paths.Database, UpgradeStageComplete, SurfaceFull)
	admission, err := AdmitStartup(ctx, fixture.paths, Options{
		UpgradeAdmission: DatabaseUpgradeAdmission{DatabasePath: fixture.paths.Database},
	})
	if err != nil {
		t.Fatalf("admit the recorded Managed completion: %v", err)
	}
	if err := admission.Close(); err != nil {
		t.Fatalf("release the admitted Managed completion: %v", err)
	}
}

// TestCompletedOperationAdmissionRequiresTheManagedHandoff is the Managed half of
// the completion check. A `complete` row proves a destination only while the
// store still holds that destination's evidence and the Manager's own connection
// carries the sealed credential for it; each shape below is one a real store can
// hold after a failed or partial handoff, and each must be refused before startup.
func TestCompletedOperationAdmissionRequiresTheManagedHandoff(t *testing.T) {
	ctx := context.Background()
	managedFixture := func(t *testing.T) completedOperationFixture {
		fixture := newCompletedOperationFixture(t)
		fixture.binding = nil
		fixture.verification = managedCompletionVerification
		fixture.domainsDone = `["source_copy","cpa_config"]`
		fixture.managedTarget = &managedCompletion{}
		return fixture
	}

	for _, test := range []struct {
		name   string
		mutate func(*completedOperationFixture)
	}{
		{name: "no recorded destination", mutate: func(f *completedOperationFixture) {
			f.managedTarget.omitDestination = true
		}},
		{name: "a destination no run proved", mutate: func(f *completedOperationFixture) {
			f.managedTarget.mutate = func(document map[string]any) { document["urlProved"] = false }
		}},
		{name: "a destination with no server fingerprint", mutate: func(f *completedOperationFixture) {
			f.managedTarget.mutate = func(document map[string]any) { document["serverFingerprint"] = "" }
		}},
		{name: "a destination with no source identity", mutate: func(f *completedOperationFixture) {
			f.managedTarget.mutate = func(document map[string]any) { document["intent"] = map[string]any{} }
		}},
		{name: "a destination whose source identity is not this import's", mutate: func(f *completedOperationFixture) {
			f.managedTarget.mutate = mutateManagedIntent(func(intent map[string]any) {
				intent["sourceIdentity"] = "external:sha256:" + strings.Repeat("d", 64)
			})
		}},
		{name: "a destination with no source class", mutate: func(f *completedOperationFixture) {
			f.managedTarget.mutate = mutateManagedIntent(func(intent map[string]any) { delete(intent, "sourceClass") })
		}},
		{name: "a destination with another source class", mutate: func(f *completedOperationFixture) {
			f.managedTarget.mutate = mutateManagedIntent(func(intent map[string]any) { intent["sourceClass"] = "network_existing" })
		}},
		{name: "a destination with no expected runtime identity", mutate: func(f *completedOperationFixture) {
			f.managedTarget.mutate = mutateManagedIntent(func(intent map[string]any) { delete(intent, "expectedRuntimeIdentity") })
		}},
		{name: "a destination with no expected runtime generation", mutate: func(f *completedOperationFixture) {
			f.managedTarget.mutate = mutateManagedIntent(func(intent map[string]any) { intent["expectedRuntimeGeneration"] = 0 })
		}},
		{name: "a destination with an unqualified artifact", mutate: func(f *completedOperationFixture) {
			f.managedTarget.mutate = mutateManagedIntent(func(intent map[string]any) {
				intent["expectedActiveArtifactId"] = "sha256:" + strings.Repeat("b", 64)
			})
		}},
		{name: "a destination with no client keys", mutate: func(f *completedOperationFixture) {
			f.managedTarget.mutate = mutateManagedIntent(func(intent map[string]any) { intent["apiKeys"] = []string{} })
		}},
		{name: "a destination with no plugin choice", mutate: func(f *completedOperationFixture) {
			f.managedTarget.mutate = mutateManagedIntent(func(intent map[string]any) { delete(intent, "pluginsEnabled") })
		}},
		{name: "a destination with no operation ID", mutate: func(f *completedOperationFixture) {
			f.managedTarget.mutate = func(document map[string]any) { delete(document, "operationId") }
		}},
		{name: "a destination whose operation ID names another attempt", mutate: func(f *completedOperationFixture) {
			f.managedTarget.mutate = func(document map[string]any) {
				document["operationId"] = managedDestinationOperationID(managedFixtureSourceIdentity, 2)
			}
		}},
		{name: "a destination holding a secret the Manager does not", mutate: func(f *completedOperationFixture) {
			f.managedTarget.destinationSecret = "another-management-key"
		}},
		{name: "a destination the connection does not name", mutate: func(f *completedOperationFixture) {
			f.managedTarget.mutate = func(document map[string]any) {
				document["baseUrl"] = "https://another-cpa.example:8317"
			}
		}},
		{name: "a plaintext management secret", mutate: func(f *completedOperationFixture) {
			f.managedTarget.secret = bindingSecretPlaintext
		}},
		{name: "a secret sealed with another data key", mutate: func(f *completedOperationFixture) {
			f.managedTarget.secret = bindingSecretForeignKey
		}},
		{name: "an External binding on a Managed verdict", mutate: func(f *completedOperationFixture) {
			f.binding = &completedBinding{
				baseURL: "https://cpa.example", identity: "https://cpa.example",
				generation: "https://cpa.example", observedAtMS: 1_700_000_000_000,
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := managedFixture(t)
			test.mutate(&fixture)
			fixture.record(t)
			if stage, err := (DatabaseUpgradeAdmission{DatabasePath: fixture.paths.Database}).Admitted(ctx); err == nil {
				t.Fatalf("a complete Managed row with %s was read as %q", test.name, stage)
			}
			refused, err := AdmitStartup(ctx, fixture.paths, Options{
				UpgradeAdmission: DatabaseUpgradeAdmission{DatabasePath: fixture.paths.Database},
			})
			if err == nil {
				_ = refused.Close()
				t.Fatalf("a complete Managed row with %s was admitted to startup", test.name)
			}
		})
	}
}

// TestCompletedOperationAdmissionRequiresTheProvedCredential is the F1
// regression: the R1 evidence check proves the binding, never the credential the
// connect step proved against it.
//
// A store that reached `complete` before the connect step sealed its secret
// carries a full verification and an empty `management_key`, and the admission
// used to unlock the writable product on it. The restarted Manager then met 401
// against the CPA the operation called complete. Every shape below is one such a
// real store can hold, and each has to be refused before startup.
func TestCompletedOperationAdmissionRequiresTheProvedCredential(t *testing.T) {
	ctx := context.Background()

	for _, test := range []struct {
		name   string
		mutate func(*completedOperationFixture)
	}{
		{name: "no sealed management secret", mutate: func(f *completedOperationFixture) {
			f.binding.secret = bindingSecretMissing
		}},
		{name: "a binding table without the secret column", mutate: func(f *completedOperationFixture) {
			f.binding.secret = bindingSecretColumnAbsent
		}},
		{name: "a plaintext management secret", mutate: func(f *completedOperationFixture) {
			f.binding.secret = bindingSecretPlaintext
		}},
		{name: "a secret sealed with another data key", mutate: func(f *completedOperationFixture) {
			f.binding.secret = bindingSecretForeignKey
		}},
		{name: "a Manager connection holding another key", mutate: func(f *completedOperationFixture) {
			f.binding.managerSecret = "another-management-key"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCompletedOperationFixture(t)
			test.mutate(&fixture)
			fixture.record(t)
			if stage, err := (DatabaseUpgradeAdmission{DatabasePath: fixture.paths.Database}).Admitted(ctx); err == nil {
				t.Fatalf("a complete row with %s was read as %q", test.name, stage)
			}
			refused, err := AdmitStartup(ctx, fixture.paths, Options{
				UpgradeAdmission: DatabaseUpgradeAdmission{DatabasePath: fixture.paths.Database},
			})
			if err == nil {
				_ = refused.Close()
				t.Fatalf("a complete row with %s was admitted to startup", test.name)
			}
		})
	}

	// A completed store with no data key beside it cannot decrypt the secret it
	// carries, so the row is refused rather than admitted on the envelope shape.
	noKey := newCompletedOperationFixture(t)
	noKey.record(t)
	missingKeyPath := filepath.Join(filepath.Dir(noKey.paths.Database), "data.key")
	if err := os.Remove(missingKeyPath); err != nil {
		t.Fatalf("remove the fixture data key: %v", err)
	}
	if stage, err := (DatabaseUpgradeAdmission{DatabasePath: noKey.paths.Database}).Admitted(ctx); err == nil {
		t.Fatalf("a complete row without its data key was read as %q", stage)
	}
	if _, err := os.Lstat(missingKeyPath); !os.IsNotExist(err) {
		t.Fatalf("read-only admission created %s on disk: err=%v", missingKeyPath, err)
	}

	// A deployment that configures its data key outside the database directory
	// (CPA_MANAGER_DATA_KEY_PATH or config dataKeyPath) still carries the key
	// that sealed the binding, so AdmitStartup must resolve that configured path
	// instead of demanding a sibling data.key beside usage.sqlite.
	relocatedKey := newCompletedOperationFixture(t)
	relocatedKey.record(t)
	customKeyPath := filepath.Join(t.TempDir(), "secrets", "custom-data.key")
	if err := os.MkdirAll(filepath.Dir(customKeyPath), 0o700); err != nil {
		t.Fatalf("create custom key dir: %v", err)
	}
	if err := os.Rename(relocatedKey.paths.DataKey, customKeyPath); err != nil {
		t.Fatalf("relocate fixture data key: %v", err)
	}
	relocatedKey.paths.DataKey = customKeyPath
	admittedRelocated, err := AdmitStartup(ctx, relocatedKey.paths, Options{
		UpgradeAdmission: DatabaseUpgradeAdmission{DatabasePath: relocatedKey.paths.Database},
	})
	if err != nil {
		t.Fatalf("admit completed store with relocated dataKeyPath: %v", err)
	}
	surfaceRelocated := admittedRelocated.Surface()
	_ = admittedRelocated.Close()
	if surfaceRelocated != SurfaceFull {
		t.Fatalf("relocated dataKeyPath surface = %q, want %q", surfaceRelocated, SurfaceFull)
	}
	if stage, err := (DatabaseUpgradeAdmission{
		DatabasePath: relocatedKey.paths.Database,
		DataKeyPath:  customKeyPath,
	}).Admitted(ctx); err != nil || stage != UpgradeStageComplete {
		t.Fatalf("DatabaseUpgradeAdmission with DataKeyPath = %q/%v, want complete", stage, err)
	}
	rawKey, err := os.ReadFile(customKeyPath)
	if err != nil {
		t.Fatalf("read relocated data key: %v", err)
	}
	if err := os.Remove(customKeyPath); err != nil {
		t.Fatalf("remove relocated data key: %v", err)
	}
	if stage, err := (DatabaseUpgradeAdmission{
		DatabasePath: relocatedKey.paths.Database,
		DataKey:      string(rawKey),
	}).Admitted(ctx); err != nil || stage != UpgradeStageComplete {
		t.Fatalf("DatabaseUpgradeAdmission with inline DataKey = %q/%v, want complete", stage, err)
	}
	if _, err := os.Lstat(customKeyPath); !os.IsNotExist(err) {
		t.Fatalf("inline DataKey admission created %s on disk: err=%v", customKeyPath, err)
	}
}

func TestCopyInAdmissionRejectsOlderAndUnknownMarkerContracts(t *testing.T) {
	for index, content := range []string{
		`{"version":2,"phase":"migrating","targetDatabase":"/data/usage.sqlite","targetDatabaseIdentity":{"device":1,"inode":2},"sourceKind":"upgraded-legacy","sourceFingerprintBefore":"before","backupManifestSha256":"backup","writtenAtMs":1}`,
		`{"version":3,"phase":"future-phase","targetDatabase":"/data/usage.sqlite","sourceKind":"upgraded-legacy","sourceFingerprintBefore":"before","backupManifestSha256":"backup","writtenAtMs":1}`,
	} {
		t.Run([]string{"version-two", "unknown-phase"}[index], func(t *testing.T) {
			root := t.TempDir()
			database := filepath.Join(root, "usage.sqlite")
			if err := os.MkdirAll(copyinmarker.Directory(root), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(copyinmarker.Path(root), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			admission := DatabaseUpgradeAdmission{DatabasePath: database}
			if _, err := admission.Admitted(context.Background()); err == nil || !strings.Contains(err.Error(), "empty v2 root") || !strings.Contains(err.Error(), "upgrade run") {
				t.Errorf("unsupported-marker admission lacks recovery: %v", err)
			}
			if _, _, err := admission.AdmittedReason(context.Background()); err == nil || !strings.Contains(err.Error(), "upgrade run") {
				t.Errorf("unsupported-marker reason admission lacks recovery: %v", err)
			}
			if _, err := os.Lstat(database); !os.IsNotExist(err) {
				t.Fatalf("admission created a target: %v", err)
			}
			if got, err := os.ReadFile(copyinmarker.Path(root)); err != nil || string(got) != content {
				t.Fatalf("admission changed marker: %q/%v", got, err)
			}
		})
	}
}
