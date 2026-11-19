package upgrade

import (
	"context"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestHandDestinationConnectionPreservesImplicitTLSSkipVerifyOnLegacyAndSparseRows(t *testing.T) {
	openFixtureStore := func(t *testing.T, fixture *verifyFixture) *store.Store {
		t.Helper()
		key, _, err := security.LoadOrCreateDataKey("", fixture.paths.DataKey)
		if err != nil {
			t.Fatalf("load data key: %v", err)
		}
		protector, err := security.NewProtector(key)
		if err != nil {
			t.Fatalf("new protector: %v", err)
		}
		st, err := store.Open(fixture.databasePath(), protector)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		return st
	}

	readRawManagerConfig := func(t *testing.T, dbPath string) string {
		t.Helper()
		db, err := managersqlite.Open(dbPath)
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		defer func() { _ = db.Close() }()
		var raw string
		if err := db.QueryRowContext(context.Background(), `select value from settings where key = 'manager_config_v1'`).Scan(&raw); err != nil {
			t.Fatalf("query manager_config_v1: %v", err)
		}
		return raw
	}

	t.Run("1.x setup-only DB handed off keeps tlsSkipVerify omitted so process TLSSkipVerify=true applies", func(t *testing.T) {
		fixture := newVerifyFixture(t)
		fixture.reconcile(t)

		st := openFixtureStore(t, fixture)
		if err := st.SaveSetup(context.Background(), model.Setup{
			CPAUpstreamURL: verifyManagedAddress,
			ManagementKey:  "stale-1x-secret",
			Queue:          "legacy-queue",
			PopSide:        "left",
		}); err != nil {
			_ = st.Close()
			t.Fatalf("seed legacy setup: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}

		if err := handDestinationConnection(
			context.Background(),
			fixture.databasePath(),
			fixture.paths.DataKey,
			verifyManagedAddress,
			verifyManagedSecret,
		); err != nil {
			t.Fatalf("handDestinationConnection: %v", err)
		}

		raw := readRawManagerConfig(t, fixture.databasePath())
		if strings.Contains(raw, `"tlsSkipVerify"`) {
			t.Fatalf("handDestinationConnection materialized tlsSkipVerify on 1.x setup-only DB: %s", raw)
		}

		reopened := openFixtureStore(t, fixture)
		defer func() { _ = reopened.Close() }()
		loaded, ok, err := reopened.LoadManagerConfig(context.Background())
		if err != nil || !ok {
			t.Fatalf("LoadManagerConfig = (%v, %v)", ok, err)
		}
		if loaded.Collector.TLSSkipVerifyExplicit {
			t.Fatalf("loaded Collector.TLSSkipVerifyExplicit = true on 1.x handoff: %#v", loaded.Collector)
		}
		processCfg := config.Config{TLSSkipVerify: true}
		if rtCfg := collectorservice.RuntimeConfigFromManagerConfigWithFallback(loaded, processCfg); !rtCfg.TLSSkipVerify || rtCfg.Queue != "legacy-queue" || rtCfg.PopSide != "left" {
			t.Fatalf("RuntimeConfigFromManagerConfigWithFallback = %#v, want TLSSkipVerify=true Queue=legacy-queue PopSide=left", rtCfg)
		}
		resolved, _, _, err := managerconfig.New(processCfg, reopened, nil).ResolveManagerConfigWithSource(context.Background())
		if err != nil {
			t.Fatalf("ResolveManagerConfigWithSource: %v", err)
		}
		if !resolved.Collector.TLSSkipVerify || resolved.Collector.Queue != "legacy-queue" || resolved.Collector.PopSide != "left" {
			t.Fatalf("resolved Collector = %#v, want TLSSkipVerify=true Queue=legacy-queue PopSide=left", resolved.Collector)
		}
	})

	t.Run("sparse manager_config_v1 without tlsSkipVerify stays sparse across handoff", func(t *testing.T) {
		fixture := newVerifyFixture(t)
		fixture.reconcile(t)

		st := openFixtureStore(t, fixture)
		queue := "custom-sparse-queue"
		if _, err := st.PatchManagerCollectorConfig(context.Background(), model.ManagerCollectorPatch{
			Queue: &queue,
		}); err != nil {
			_ = st.Close()
			t.Fatalf("seed sparse manager_config_v1: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}

		if err := handDestinationConnection(
			context.Background(),
			fixture.databasePath(),
			fixture.paths.DataKey,
			verifyManagedAddress,
			verifyManagedSecret,
		); err != nil {
			t.Fatalf("handDestinationConnection: %v", err)
		}

		raw := readRawManagerConfig(t, fixture.databasePath())
		if strings.Contains(raw, `"tlsSkipVerify"`) {
			t.Fatalf("handDestinationConnection materialized tlsSkipVerify on sparse row: %s", raw)
		}
		reopened := openFixtureStore(t, fixture)
		defer func() { _ = reopened.Close() }()
		loaded, ok, err := reopened.LoadManagerConfig(context.Background())
		if err != nil || !ok {
			t.Fatalf("LoadManagerConfig = (%v, %v)", ok, err)
		}
		if loaded.Collector.TLSSkipVerifyExplicit || loaded.Collector.Queue != queue {
			t.Fatalf("loaded Collector = %#v, want TLSSkipVerifyExplicit=false Queue=%q", loaded.Collector, queue)
		}
		if rtCfg := collectorservice.RuntimeConfigFromManagerConfigWithFallback(loaded, config.Config{TLSSkipVerify: true}); !rtCfg.TLSSkipVerify {
			t.Fatalf("RuntimeConfigFromManagerConfigWithFallback = %#v, want TLSSkipVerify=true", rtCfg)
		}
	})

	t.Run("explicit tlsSkipVerify false is preserved across handoff", func(t *testing.T) {
		fixture := newVerifyFixture(t)
		fixture.reconcile(t)

		st := openFixtureStore(t, fixture)
		explicitFalse := false
		if _, err := st.PatchManagerCollectorConfig(context.Background(), model.ManagerCollectorPatch{
			TLSSkipVerify: &explicitFalse,
		}); err != nil {
			_ = st.Close()
			t.Fatalf("seed explicit tlsSkipVerify=false: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}

		if err := handDestinationConnection(
			context.Background(),
			fixture.databasePath(),
			fixture.paths.DataKey,
			verifyManagedAddress,
			verifyManagedSecret,
		); err != nil {
			t.Fatalf("handDestinationConnection: %v", err)
		}

		raw := readRawManagerConfig(t, fixture.databasePath())
		if !strings.Contains(raw, `"tlsSkipVerify":false`) {
			t.Fatalf("handDestinationConnection dropped explicit tlsSkipVerify=false: %s", raw)
		}
		reopened := openFixtureStore(t, fixture)
		defer func() { _ = reopened.Close() }()
		loaded, ok, err := reopened.LoadManagerConfig(context.Background())
		if err != nil || !ok {
			t.Fatalf("LoadManagerConfig = (%v, %v)", ok, err)
		}
		if !loaded.Collector.TLSSkipVerifyExplicit || loaded.Collector.TLSSkipVerify {
			t.Fatalf("loaded Collector = %#v, want TLSSkipVerifyExplicit=true TLSSkipVerify=false", loaded.Collector)
		}
		if rtCfg := collectorservice.RuntimeConfigFromManagerConfigWithFallback(loaded, config.Config{TLSSkipVerify: true}); rtCfg.TLSSkipVerify {
			t.Fatalf("RuntimeConfigFromManagerConfigWithFallback = %#v, want TLSSkipVerify=false", rtCfg)
		}
	})
	t.Run("1.x setup row with empty PopSide keeps Collector.PopSide blank for process fallback", func(t *testing.T) {
		fixture := newVerifyFixture(t)
		fixture.reconcile(t)

		st := openFixtureStore(t, fixture)
		if err := st.SaveSetup(context.Background(), model.Setup{
			CPAUpstreamURL: verifyManagedAddress,
			ManagementKey:  "stale-1x-secret",
			Queue:          "legacy-queue",
			PopSide:        "",
		}); err != nil {
			_ = st.Close()
			t.Fatalf("seed legacy setup: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}

		if err := handDestinationConnection(
			context.Background(),
			fixture.databasePath(),
			fixture.paths.DataKey,
			verifyManagedAddress,
			verifyManagedSecret,
		); err != nil {
			t.Fatalf("handDestinationConnection: %v", err)
		}

		reopened := openFixtureStore(t, fixture)
		defer func() { _ = reopened.Close() }()
		loaded, ok, err := reopened.LoadManagerConfig(context.Background())
		if err != nil || !ok {
			t.Fatalf("LoadManagerConfig = (%v, %v)", ok, err)
		}
		if loaded.Collector.Queue != "legacy-queue" || loaded.Collector.PopSide != "" {
			t.Fatalf("loaded Collector = %#v, want Queue=legacy-queue and empty PopSide", loaded.Collector)
		}
		if rtCfg := collectorservice.RuntimeConfigFromManagerConfigWithFallback(loaded, config.Config{PopSide: "left"}); rtCfg.Queue != "legacy-queue" || rtCfg.PopSide != "left" {
			t.Fatalf("RuntimeConfigFromManagerConfigWithFallback = %#v, want Queue=legacy-queue PopSide=left", rtCfg)
		}
	})
}
