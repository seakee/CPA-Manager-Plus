package setting

import (
	"database/sql"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func readOptionalSetting(t *testing.T, db *sql.DB, key string) (string, bool) {
	t.Helper()
	var val string
	err := db.QueryRow(`select value from settings where key = ?`, key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("read setting %q: %v", key, err)
	}
	return val, true
}

func TestEffectiveMonitoringDisableCommitsGapAndSeqAtomically(t *testing.T) {
	db, repo, _, _ := revisionFixture(t)
	ctx := t.Context()
	enabled := true
	disabled := false

	// Saving with collector enabled (explicit true or nil) must NOT set gap or bump seq.
	if err := repo.SaveManagerConfig(ctx, model.ManagerConfig{
		Collector: model.ManagerCollectorConfig{Enabled: &enabled},
	}); err != nil {
		t.Fatalf("SaveManagerConfig(enabled=true): %v", err)
	}
	if err := repo.SaveManagerConfig(ctx, model.ManagerConfig{}); err != nil {
		t.Fatalf("SaveManagerConfig(enabled=nil): %v", err)
	}
	if _, ok := readOptionalSetting(t, db, sqliterepo.FreshUsageMonitoringDisabledGapKey); ok {
		t.Fatal("disabled gap set when monitoring remained enabled")
	}
	if _, ok := readOptionalSetting(t, db, sqliterepo.FreshUsageMonitoringDisableSeqKey); ok {
		t.Fatal("disable seq incremented when monitoring remained enabled")
	}

	// PatchManagerCollectorEnabled(false) sets gap='1' and increments seq=1 in the same tx without touching revision.
	revBefore, hasRevBefore, err := repo.LoadPersistedCPAConnectionRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PatchManagerCollectorEnabled(ctx, false); err != nil {
		t.Fatalf("PatchManagerCollectorEnabled(false): %v", err)
	}
	if gap, ok := readOptionalSetting(t, db, sqliterepo.FreshUsageMonitoringDisabledGapKey); !ok || gap != "1" {
		t.Fatalf("gap after Patch(false) = (%q, %v), want ('1', true)", gap, ok)
	}
	if seq, ok := readOptionalSetting(t, db, sqliterepo.FreshUsageMonitoringDisableSeqKey); !ok || seq != "1" {
		t.Fatalf("seq after Patch(false) = (%q, %v), want ('1', true)", seq, ok)
	}
	revAfter, hasRevAfter, err := repo.LoadPersistedCPAConnectionRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hasRevAfter != hasRevBefore || revAfter != revBefore {
		t.Fatalf("PatchManagerCollectorEnabled mutated cpa_connection_revision_v1: before=(%q,%v) after=(%q,%v)", revBefore, hasRevBefore, revAfter, hasRevAfter)
	}

	// PatchManagerCollectorEnabled(true) leaves existing gap untouched (only collection advance clears it) and does not increment seq.
	if _, err := repo.PatchManagerCollectorEnabled(ctx, true); err != nil {
		t.Fatalf("PatchManagerCollectorEnabled(true): %v", err)
	}
	if seq, ok := readOptionalSetting(t, db, sqliterepo.FreshUsageMonitoringDisableSeqKey); !ok || seq != "1" {
		t.Fatalf("seq after Patch(true) = (%q, %v), want '1'", seq, ok)
	}

	// SaveManagerConfig and SaveManagerConfigAndSetup with explicit false increment seq to 2 and 3.
	if err := repo.SaveManagerConfig(ctx, model.ManagerConfig{
		Collector: model.ManagerCollectorConfig{Enabled: &disabled},
	}); err != nil {
		t.Fatalf("SaveManagerConfig(enabled=false): %v", err)
	}
	if seq, ok := readOptionalSetting(t, db, sqliterepo.FreshUsageMonitoringDisableSeqKey); !ok || seq != "2" {
		t.Fatalf("seq after SaveManagerConfig(false) = (%q, %v), want '2'", seq, ok)
	}

	cfg, setup := revisionPair("secret-key")
	cfg.Collector.Enabled = &disabled
	if err := repo.SaveManagerConfigAndSetup(ctx, cfg, setup); err != nil {
		t.Fatalf("SaveManagerConfigAndSetup(enabled=false): %v", err)
	}
	if seq, ok := readOptionalSetting(t, db, sqliterepo.FreshUsageMonitoringDisableSeqKey); !ok || seq != "3" {
		t.Fatalf("seq after SaveManagerConfigAndSetup(false) = (%q, %v), want '3'", seq, ok)
	}

	// Turn monitoring ON before testing rollback so we prove manager_config_v1 also rolls back to enabled=true.
	if _, err := repo.PatchManagerCollectorEnabled(ctx, true); err != nil {
		t.Fatalf("PatchManagerCollectorEnabled(true): %v", err)
	}
	if _, err := db.Exec(`create trigger fail_gap_upsert before update on settings when new.key = 'fresh_usage_monitoring_disable_seq' begin select raise(abort, 'abort disable seq'); end`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PatchManagerCollectorEnabled(ctx, false); err == nil {
		t.Fatal("expected PatchManagerCollectorEnabled(false) to fail under trigger")
	}
	if _, err := db.Exec(`drop trigger fail_gap_upsert`); err != nil {
		t.Fatal(err)
	}
	loadedCfg, ok, err := repo.LoadManagerConfig(ctx)
	if err != nil || !ok {
		t.Fatalf("LoadManagerConfig after rollback: ok=%v err=%v", ok, err)
	}
	if loadedCfg.Collector.Enabled == nil || !*loadedCfg.Collector.Enabled {
		t.Fatalf("manager_config_v1 did not roll back to enabled=true: %+v", loadedCfg.Collector.Enabled)
	}
	if seq, ok := readOptionalSetting(t, db, sqliterepo.FreshUsageMonitoringDisableSeqKey); !ok || seq != "3" {
		t.Fatalf("seq after rollback = (%q, %v), want '3'", seq, ok)
	}
}

func TestEffectiveMonitoringDisableFailsClosedOnCorruptedDisableSeq(t *testing.T) {
	db, repo, _, _ := revisionFixture(t)
	ctx := t.Context()
	enabled := true
	disabled := false
	if _, err := repo.PatchManagerCollectorEnabled(ctx, true); err != nil {
		t.Fatalf("PatchManagerCollectorEnabled(true): %v", err)
	}
	if _, err := db.Exec(`insert into settings(key, value, updated_at_ms) values(?, 'corrupted', 1000)
		on conflict(key) do update set value='corrupted', updated_at_ms=1000`,
		sqliterepo.FreshUsageMonitoringDisableSeqKey,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.PatchManagerCollectorEnabled(ctx, false); err == nil {
		t.Fatal("PatchManagerCollectorEnabled(false) succeeded with corrupted disable_seq, want error")
	}
	if err := repo.SaveManagerConfig(ctx, model.ManagerConfig{
		Collector: model.ManagerCollectorConfig{Enabled: &disabled},
	}); err == nil {
		t.Fatal("SaveManagerConfig(false) succeeded with corrupted disable_seq, want error")
	}
	loadedCfg, ok, err := repo.LoadManagerConfig(ctx)
	if err != nil || !ok {
		t.Fatalf("LoadManagerConfig after failed disable: ok=%v err=%v", ok, err)
	}
	if loadedCfg.Collector.Enabled == nil || *loadedCfg.Collector.Enabled != enabled {
		t.Fatalf("manager_config_v1 did not roll back to enabled=true: %+v", loadedCfg.Collector.Enabled)
	}
	if seq, ok := readOptionalSetting(t, db, sqliterepo.FreshUsageMonitoringDisableSeqKey); !ok || seq != "corrupted" {
		t.Fatalf("corrupted disable_seq was overwritten to (%q, %v), want untouched 'corrupted'", seq, ok)
	}
}
