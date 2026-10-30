package legacypreflight

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestHistoricalLegacyProfilesAreObservationsNotUpgradeGrants(t *testing.T) {
	for _, fixture := range loadLegacyProfileFixtures(t) {
		t.Run(fixture.Ref, func(t *testing.T) {
			paths := buildLegacyProfileFixture(t, fixture)
			before := sourceState(t, filepath.Dir(paths.Database))
			report, err := Inspect(context.Background(), paths, Options{SourceStopped: true})
			if err != nil {
				t.Fatal(err)
			}
			profile := report.CompatibilityProfile
			if report.SourceKind != "legacy-manager" || profile == nil || profile.Status != "recognized" || profile.Family != fixture.Family {
				t.Fatalf("wrong historical classification: %+v", report)
			}
			if report.ExactSourceRelease.Status != "unknown" || report.BridgeReadiness.Status != "unknown" || report.UpgradeAuthorized || startupAllowed(report, UpgradeStageNone) {
				t.Fatalf("observation granted authority: %+v", report)
			}
			for _, item := range report.Unknown {
				if item == "compatibility-profile" {
					t.Fatal("recognized family still unknown")
				}
			}
			for _, item := range report.BridgeReadiness.Items {
				if item.Status == "ready" || item.Status == "qualified" {
					t.Fatal("unknown bridge claimed ready")
				}
			}
			if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
				t.Fatal("classification mutated source")
			}
			admission, err := AdmitStartup(context.Background(), paths, Options{})
			if err == nil || admission != nil {
				if admission != nil {
					admission.Close()
				}
				t.Fatal("recognized legacy admitted writable startup")
			}
			if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
				t.Fatal("rejected startup changed source")
			}
		})
	}
}

func TestLegacyProfileNegativeShapesNeverBecomeAnotherFamily(t *testing.T) {
	cases := []struct{ name, ref, query, status string }{
		{"missing-common-table", "v1.12.12", "drop table usage_data_migrations", "partial"},
		{"missing-base-column", "v1.12.12", "alter table usage_events drop column request_id", "partial"},
		{"incomplete-metadata", "v1.13.1", "alter table usage_events drop column stream", "partial"},
		{"partial-archive", "v1.14.4", "drop table usage_archive_segments", "partial"},
		{"extra-column", "v1.13.1", "alter table usage_events add column future_owned_column text", "unknown"},
		{"partial-native", "v1.14.4", "create table gateway_api_key_identities(id text primary key)", "conflicting"},
	}
	fixtures := loadLegacyProfileFixtures(t)
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var fixture legacyProfileFixture
			for _, candidate := range fixtures {
				if candidate.Ref == test.ref {
					fixture = candidate
				}
			}
			paths := buildLegacyProfileFixture(t, fixture)
			execLegacyFixture(t, paths.Database, test.query)
			before := sourceState(t, filepath.Dir(paths.Database))
			report, err := Inspect(context.Background(), paths, Options{SourceStopped: true})
			if err != nil {
				t.Fatal(err)
			}
			if report.CompatibilityProfile.Status != test.status || report.CompatibilityProfile.Family != "" || report.UpgradeAuthorized || startupAllowed(report, UpgradeStageNone) {
				t.Fatalf("unsafe profile: %+v", report)
			}
			if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
				t.Fatal("negative classification mutated source")
			}
		})
	}
}

func TestLegacyProfileTypesAndPrimaryKeysMustMatch(t *testing.T) {
	fixture := loadLegacyProfileFixtures(t)[4]
	paths := buildLegacyProfileFixture(t, fixture)
	inspected, err := sqlite.InspectLegacySource(context.Background(), paths, sqlite.InspectionOptions{SourceStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name          string
		table, column string
		change        func(*sqlite.InspectionColumn)
	}{
		{"wrong-type", "usage_events", "stream", func(c *sqlite.InspectionColumn) { c.Type = "text" }},
		{"wrong-base-pk", "usage_events", "id", func(c *sqlite.InspectionColumn) { c.PrimaryKey = 0 }},
		{"wrong-composite-pk", "usage_archive_segments", "sequence", func(c *sqlite.InspectionColumn) { c.PrimaryKey = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, _ := json.Marshal(inspected)
			var source sqlite.LegacyInspection
			json.Unmarshal(data, &source)
			for ti := range source.Tables {
				for ci := range source.Tables[ti].Columns {
					if source.Tables[ti].Name == test.table && source.Tables[ti].Columns[ci].Name == test.column {
						test.change(&source.Tables[ti].Columns[ci])
					}
				}
			}
			result := classifyLegacyProfile(source)
			if result.Status != "conflicting" || result.Family != "" {
				t.Fatalf("accepted wrong type/PK: %+v", result)
			}
		})
	}
	// C archive tables combined with A metadata must not be called an older A.
	data, _ := json.Marshal(inspected)
	var mixed sqlite.LegacyInspection
	json.Unmarshal(data, &mixed)
	for i := range mixed.Tables {
		if mixed.Tables[i].Name == "usage_events" {
			kept := mixed.Tables[i].Columns[:0]
			for _, c := range mixed.Tables[i].Columns {
				isMetadata := false
				for _, spec := range legacyMetadataColumns {
					if strings.SplitN(spec, ":", 2)[0] == c.Name {
						isMetadata = true
					}
				}
				if !isMetadata {
					kept = append(kept, c)
				}
			}
			mixed.Tables[i].Columns = kept
		}
	}
	if result := classifyLegacyProfile(mixed); result.Status != "conflicting" || result.Family != "" {
		t.Fatalf("archive/A mixture accepted: %+v", result)
	}
}

func TestBridgeObservationsNeverClaimReadinessOrLeakSecrets(t *testing.T) {
	fixture := loadLegacyProfileFixtures(t)[6]
	for _, test := range []struct{ name, query, item, status string }{
		{"completed", `insert into usage_data_migrations(name,status) values('usage_cache_accounting_v2','completed')`, "cache-accounting-v2", "observed"},
		{"pending", `insert into usage_data_migrations(name,status) values('usage_cache_accounting_v2','pending')`, "cache-accounting-v2", "blocked"},
		{"failed", `insert into usage_data_migrations(name,status) values('usage_cache_accounting_v2','failed')`, "cache-accounting-v2", "blocked"},
		{"applying", `insert into usage_data_migrations(name,status) values('usage_cache_accounting_v2','applying')`, "cache-accounting-v2", "blocked"},
		{"clearing", `insert into usage_data_migrations(name,status) values('usage_cache_accounting_v2','clearing')`, "cache-accounting-v2", "blocked"},
		{"quota-completed", `insert into usage_data_migrations(name,status) values('usage_cache_accounting_v2','completed'),('quota_snapshot_lifecycle_v1','completed')`, "quota-snapshot-lifecycle-v1", "observed"},
		{"quota-pending", `insert into usage_data_migrations(name,status) values('usage_cache_accounting_v2','completed'),('quota_snapshot_lifecycle_v1','pending')`, "quota-snapshot-lifecycle-v1", "blocked"},
		{"quota-running", `insert into usage_data_migrations(name,status) values('usage_cache_accounting_v2','completed'),('quota_snapshot_lifecycle_v1','running')`, "quota-snapshot-lifecycle-v1", "blocked"},
		{"quota-failed", `insert into usage_data_migrations(name,status) values('usage_cache_accounting_v2','completed'),('quota_snapshot_lifecycle_v1','failed')`, "quota-snapshot-lifecycle-v1", "blocked"},
		{"quota-offline-required", `insert into usage_data_migrations(name,status) values('usage_cache_accounting_v2','completed'),('quota_snapshot_lifecycle_v1','offline_required')`, "quota-snapshot-lifecycle-v1", "blocked"},
		{"quota-unknown", `insert into usage_data_migrations(name,status) values('usage_cache_accounting_v2','completed'),('quota_snapshot_lifecycle_v1','OWNED_SECRET_STATUS')`, "quota-snapshot-lifecycle-v1", "unknown"},
		{"quota-over-budget", `with recursive owned(n) as (values(1) union all select n+1 from owned where n<1001) insert into usage_data_migrations(name,status) select 'owned_'||n,'completed' from owned; insert into usage_data_migrations(name,status) values('usage_cache_accounting_v2','completed'),('quota_snapshot_lifecycle_v1','offline_required')`, "quota-snapshot-lifecycle-v1", "blocked"},
		{"unknown", `insert into usage_data_migrations(name,status) values('usage_cache_accounting_v2','OWNED_SECRET_STATUS')`, "cache-accounting-v2", "unknown"},
		{"deleted-history", `insert into usage_archive_deleted_coverage_daily values(20261012,1,1,1)`, "archive-coverage", "blocked"},
		{"maintenance", `insert into usage_maintenance_locks values('owned','owned-run','archive',1,1)`, "maintenance", "blocked"},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := buildLegacyProfileFixture(t, fixture)
			execLegacyFixture(t, paths.Database, test.query)
			execLegacyFixture(t, paths.Database, `insert into settings values('untrusted-version','v1.14.4 OWNED_SECRET_VERSION',0)`)
			before := sourceState(t, filepath.Dir(paths.Database))
			report, err := Inspect(context.Background(), paths, Options{SourceStopped: true})
			if err != nil {
				t.Fatal(err)
			}
			if report.BridgeReadiness.Items[test.item].Status != test.status || report.BridgeReadiness.Status == "ready" || report.UpgradeAuthorized || report.ExactSourceRelease.Status != "unknown" {
				t.Fatalf("bridge/profile state: %+v", report)
			}
			if report.CompatibilityProfile.Status != "recognized" || report.CompatibilityProfile.Family != "LegacySchemaC" {
				t.Fatal("runtime migration state changed capability classification")
			}
			if test.status == "blocked" && (report.BridgeReadiness.Status != "blocked" || !strings.Contains(strings.Join(report.Blocked, ","), "legacy-bridge-blocked")) {
				t.Fatal("blocker absent from report")
			}
			encoded, _ := json.Marshal(report)
			if strings.Contains(string(encoded), "OWNED_SECRET") {
				t.Fatal("raw evidence escaped")
			}
			if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
				t.Fatal("bridge query changed source")
			}
		})
	}
}

func TestProfilesRemainUnknownOnUnsafeInspectionAndCancellation(t *testing.T) {
	paths := buildLegacyProfileFixture(t, loadLegacyProfileFixtures(t)[6])
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := sourceState(t, filepath.Dir(paths.Database))
	report, err := Inspect(ctx, paths, Options{SourceStopped: true})
	if !errors.Is(err, context.Canceled) || report.CompatibilityProfile.Status != "unknown" || report.UpgradeAuthorized {
		t.Fatalf("cancelled profile=%+v err=%v", report, err)
	}
	report, err = Inspect(context.Background(), paths, Options{})
	if err != nil || report.CompatibilityProfile.Status != "unknown" || report.UpgradeAuthorized {
		t.Fatalf("unfenced profile=%+v err=%v", report, err)
	}
	if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
		t.Fatal("unsafe inspection changed source")
	}
}
