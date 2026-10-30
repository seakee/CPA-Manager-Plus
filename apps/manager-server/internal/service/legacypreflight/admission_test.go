package legacypreflight

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
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

func recordFixtureStage(t *testing.T, databasePath, stage string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open admission fixture: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`insert into upgrade_operations (
		id, stage, domains_done, source_kind, created_at_ms, updated_at_ms
	) values (1, ?, '["source_copy"]', 'upgraded-legacy', 1, 1)
	on conflict(id) do update set stage = excluded.stage`, stage); err != nil {
		t.Fatalf("record fixture stage %q: %v", stage, err)
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
		{name: "legacy in progress", kind: SourceKindLegacyManager, stage: UpgradeStageInProgress, expect: true},
		{name: "legacy complete", kind: SourceKindLegacyManager, stage: UpgradeStageComplete, expect: true},
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

func TestLegacySourceIsAdmittedOnlyThroughARecordedUpgrade(t *testing.T) {
	paths := recordedUpgradeFixture(t)
	if report, err := Inspect(context.Background(), paths, Options{SourceStopped: true}); err != nil || report.SourceKind != SourceKindLegacyManager {
		t.Fatalf("fixture classification = %q/%v, want %s", report.SourceKind, err, SourceKindLegacyManager)
	}
	if _, err := AdmitStartup(context.Background(), paths, Options{}); err == nil {
		t.Fatal("a legacy source was admitted without a recorded Upgrade operation")
	}

	for _, test := range []struct {
		recorded string
		stage    UpgradeStage
		surface  StartupSurface
	}{
		{recorded: "copy_in", stage: UpgradeStageInProgress, surface: SurfaceUpgradeOnly},
		{recorded: "failed", stage: UpgradeStageInProgress, surface: SurfaceUpgradeOnly},
		{recorded: "complete", stage: UpgradeStageComplete, surface: SurfaceFull},
	} {
		t.Run(test.recorded, func(t *testing.T) {
			recordFixtureStage(t, paths.Database, test.recorded)
			report, err := Inspect(context.Background(), paths, Options{SourceStopped: true})
			if err != nil {
				t.Fatalf("inspect with a recorded %s operation: %v", test.recorded, err)
			}
			if report.UpgradeAuthorized {
				t.Fatal("the preflight report gained Upgrade authority from a recorded operation")
			}
			admission, err := AdmitStartup(context.Background(), paths, Options{
				UpgradeAdmission: DatabaseUpgradeAdmission{DatabasePath: paths.Database},
			})
			if err != nil {
				t.Fatalf("admission with a recorded %s operation: %v", test.recorded, err)
			}
			defer func() { _ = admission.Close() }()
			if admission.UpgradeStage != test.stage {
				t.Fatalf("admission stage = %q, want %q", admission.UpgradeStage, test.stage)
			}
			if surface := admission.Surface(); surface != test.surface {
				t.Fatalf("admission surface = %q, want %q", surface, test.surface)
			}
		})
	}
}
