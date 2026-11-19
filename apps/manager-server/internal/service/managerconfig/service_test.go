package managerconfig

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

func TestResolveLegacyConnectionAuthorityMatrix(t *testing.T) {
	const (
		urlA = "http://cpa-a.local:8317"
		urlB = "http://cpa-b.local:8317"
		key  = "key-a"
	)
	tests := []struct {
		name          string
		managerOK     bool
		managerURL    string
		managerKey    string
		setupOK       bool
		setupURL      string
		setupKey      string
		wantAuthority LegacyConnectionAuthority
		wantURL       string
		wantKey       string
		wantErr       bool
	}{
		{
			name:          "complete manager wins over conflicting setup",
			managerOK:     true,
			managerURL:    urlA,
			managerKey:    key,
			setupOK:       true,
			setupURL:      urlB,
			setupKey:      "key-b",
			wantAuthority: LegacyConnectionAuthorityManager,
			wantURL:       urlA,
			wantKey:       key,
		},
		{
			name:          "matching setup repairs manager URL partial",
			managerOK:     true,
			managerURL:    urlA,
			setupOK:       true,
			setupURL:      urlA,
			setupKey:      key,
			wantAuthority: LegacyConnectionAuthoritySetup,
			wantURL:       urlA,
			wantKey:       key,
		},
		{
			name:       "setup URL conflicts with manager URL partial",
			managerOK:  true,
			managerURL: urlA,
			setupOK:    true,
			setupURL:   urlB,
			setupKey:   key,
			wantErr:    true,
		},
		{
			name:          "matching setup repairs manager key partial",
			managerOK:     true,
			managerKey:    key,
			setupOK:       true,
			setupURL:      urlA,
			setupKey:      key,
			wantAuthority: LegacyConnectionAuthoritySetup,
			wantURL:       urlA,
			wantKey:       key,
		},
		{
			name:       "setup key conflicts with manager key partial",
			managerOK:  true,
			managerKey: "key-manager",
			setupOK:    true,
			setupURL:   urlA,
			setupKey:   "key-setup",
			wantErr:    true,
		},
		{
			name:          "setup is authority when manager is missing",
			setupOK:       true,
			setupURL:      urlA,
			setupKey:      key,
			wantAuthority: LegacyConnectionAuthoritySetup,
			wantURL:       urlA,
			wantKey:       key,
		},
		{
			name:          "partial rows are not combined from manager URL and setup key",
			managerOK:     true,
			managerURL:    urlA,
			setupOK:       true,
			setupKey:      key,
			wantAuthority: LegacyConnectionAuthorityNone,
		},
		{
			name:          "partial rows are not combined from manager key and setup URL",
			managerOK:     true,
			managerKey:    key,
			setupOK:       true,
			setupURL:      urlA,
			wantAuthority: LegacyConnectionAuthorityNone,
		},
		{
			name:       "overlapping partial URLs conflict",
			managerOK:  true,
			managerURL: urlA,
			setupOK:    true,
			setupURL:   urlB,
			wantErr:    true,
		},
		{
			name:       "overlapping partial keys conflict",
			managerOK:  true,
			managerKey: "key-manager",
			setupOK:    true,
			setupKey:   "key-setup",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolution, err := ResolveLegacyConnectionAuthority(
				store.ManagerConfig{CPAConnection: store.ManagerCPAConnectionConfig{
					CPABaseURL:    tt.managerURL,
					ManagementKey: tt.managerKey,
				}},
				tt.managerOK,
				store.Setup{CPAUpstreamURL: tt.setupURL, ManagementKey: tt.setupKey},
				tt.setupOK,
			)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr=%v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if resolution.Authority != tt.wantAuthority {
				t.Fatalf("authority = %q, want %q", resolution.Authority, tt.wantAuthority)
			}
			if resolution.Connection.BaseURL != tt.wantURL || resolution.Connection.ManagementKey != tt.wantKey {
				t.Fatalf("connection = %#v, want URL=%q key=%q", resolution.Connection, tt.wantURL, tt.wantKey)
			}
		})
	}
}

func TestLegacyConnectionResolutionValidatesRequestedInput(t *testing.T) {
	resolution, err := ResolveLegacyConnectionAuthority(
		store.ManagerConfig{CPAConnection: store.ManagerCPAConnectionConfig{CPABaseURL: "http://cpa-a.local:8317"}},
		true,
		store.Setup{ManagementKey: "key-a"},
		true,
	)
	if err != nil {
		t.Fatalf("resolve partial rows: %v", err)
	}
	if err := resolution.ValidateRequestedLegacyConnection(LegacyConnection{
		BaseURL:       "http://cpa-a.local:8317/",
		ManagementKey: "key-a",
	}); err != nil {
		t.Fatalf("matching explicit input rejected: %v", err)
	}
	if err := resolution.ValidateRequestedLegacyConnection(LegacyConnection{
		BaseURL:       "http://cpa-a.local:8317",
		ManagementKey: "key-b",
	}); err == nil {
		t.Fatal("conflicting partial key was accepted")
	}
}

func TestSetCollectorEnabledHonoursEnvironmentSourceAndExternalLifecycle(t *testing.T) {
	t.Run("environment source refuses toggle and keeps env source without persisting secrets", func(t *testing.T) {
		cfg := testutil.NewConfig(t)
		st := testutil.NewStore(t, cfg)
		cpaMock := testutil.NewCPAMock(t)
		cfg.CPAUpstreamURL = cpaMock.URL()
		cfg.ManagementKey = cpaMock.ManagementKey
		collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
		svc := New(cfg, st, collector)

		if _, err := svc.SetCollectorEnabled(t.Context(), false); err == nil || !strings.Contains(err.Error(), "environment variables") {
			t.Fatalf("env toggle error = %v, want environment-managed refusal", err)
		}
		if _, ok, err := st.LoadManagerConfig(t.Context()); err != nil || ok {
			t.Fatalf("env refusal persisted manager_config_v1: ok=%v err=%v", ok, err)
		}

		res, err := svc.SetCollectorEnabled(t.Context(), true)
		if err != nil {
			t.Fatalf("idempotent env SetCollectorEnabled error = %v", err)
		}
		if res.Source != string(SourceEnv) {
			t.Fatalf("source = %q, want %q", res.Source, SourceEnv)
		}
		if _, ok, err := st.LoadManagerConfig(t.Context()); err != nil || ok {
			t.Fatalf("idempotent env call persisted manager_config_v1: ok=%v err=%v", ok, err)
		}
	})

	t.Run("external collector lifecycle updates DB only without starting background collector or touching CPA", func(t *testing.T) {
		cfg := testutil.NewConfig(t)
		st := testutil.NewStore(t, cfg)
		cpaMock := testutil.NewCPAMock(t)
		cpaMock.UsageStatisticsEnabled = false
		collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
		svc := New(cfg, st, collector)
		svc.UseExternalCollectorLifecycle()

		seed := store.ManagerConfig{
			CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL:    cpaMock.URL(),
				ManagementKey: cpaMock.ManagementKey,
			},
			Collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false)},
		}
		if err := st.SaveManagerConfigAndSetup(t.Context(), seed, SetupFromManagerConfig(seed)); err != nil {
			t.Fatal(err)
		}

		res, err := svc.SetCollectorEnabled(t.Context(), true)
		if err != nil {
			t.Fatalf("SetCollectorEnabled(true) error = %v", err)
		}
		if res.Source != string(SourceDB) || res.Config.Collector.Enabled == nil || !*res.Config.Collector.Enabled {
			t.Fatalf("unexpected response = %#v", res)
		}
		if collector.StartCount() != 0 {
			t.Fatalf("collector StartCount = %d, want 0", collector.StartCount())
		}
		if cpaMock.UsageStatisticsEnabled {
			t.Fatal("external lifecycle SetCollectorEnabled mutated CPA usage-statistics-enabled")
		}
	})
}

func TestGetSkipsCPAUsageProbeWhenDisallowed(t *testing.T) {
	var upstreamHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usage-statistics-enabled":true}`))
	}))
	defer upstream.Close()

	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
	svc := New(cfg, st, collector)

	seed := store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    upstream.URL,
			ManagementKey: "secret-key",
		},
		Collector: store.ManagerCollectorConfig{Enabled: BoolPtr(true)},
	}
	if err := st.SaveManagerConfigAndSetup(t.Context(), seed, SetupFromManagerConfig(seed)); err != nil {
		t.Fatal(err)
	}

	allow := false
	svc.SetCPAUsageProbeAllowed(func(context.Context) bool { return allow })

	res, err := svc.Get(t.Context())
	if err != nil {
		t.Fatalf("Get with disallowed probe failed: %v", err)
	}
	if res.CPAUsage != nil {
		t.Fatalf("expected nil CPAUsage when probe disallowed, got %#v", res.CPAUsage)
	}
	if got := upstreamHits.Load(); got != 0 {
		t.Fatalf("disallowed probe hit upstream %d times, want 0", got)
	}

	allow = true
	res, err = svc.Get(t.Context())
	if err != nil {
		t.Fatalf("Get with allowed probe failed: %v", err)
	}
	if res.CPAUsage == nil || !res.CPAUsage.UsageStatisticsEnabled {
		t.Fatalf("expected populated CPAUsage when probe allowed, got %#v", res.CPAUsage)
	}
	if got := upstreamHits.Load(); got != 1 {
		t.Fatalf("allowed probe hit upstream %d times, want 1", got)
	}
}

func TestPatchCollectorAndRetentionPreserveUntouchedFields(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
	svc := New(cfg, st, collector)
	svc.UseExternalCollectorLifecycle()

	seed := store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    "http://127.0.0.1:18318",
			ManagementKey: "super-secret-cpa-key",
		},
		Collector: store.ManagerCollectorConfig{
			Enabled:        BoolPtr(true),
			CollectorMode:  "http",
			Queue:          "usage",
			PopSide:        "left",
			PollIntervalMS: 5000,
			BatchSize:      100,
			QueryLimit:     2000,
			TLSSkipVerify:  true,
		},
		Retention: store.ManagerRetentionConfig{
			Enabled:       BoolPtr(false),
			RetentionDays: 30,
		},
	}
	if err := st.SaveManagerConfigAndSetup(t.Context(), seed, SetupFromManagerConfig(seed)); err != nil {
		t.Fatal(err)
	}

	var notified atomic.Int32
	svc.SetRetentionChangedNotifier(func() { notified.Add(1) })

	queue := "cpa_usage_events"
	res, err := svc.PatchCollector(t.Context(), store.ManagerCollectorPatch{
		Queue: &queue,
	})
	if err != nil {
		t.Fatalf("PatchCollector failed: %v", err)
	}
	if res.Config.Collector.Queue != "cpa_usage_events" ||
		!res.Config.Collector.TLSSkipVerify ||
		res.Config.Collector.CollectorMode != "http" ||
		res.Config.Collector.PopSide != "left" ||
		res.Config.Collector.BatchSize != 100 ||
		res.Config.Collector.PollIntervalMS != 5000 ||
		res.Config.Collector.QueryLimit != 2000 ||
		res.Config.Collector.Enabled == nil || !*res.Config.Collector.Enabled {
		t.Fatalf("PatchCollector altered untouched collector fields: %#v", res.Config.Collector)
	}
	if notified.Load() != 0 {
		t.Fatalf("collector patch notified retention worker %d times, want 0", notified.Load())
	}

	days := 45
	res, err = svc.PatchRetention(t.Context(), store.ManagerRetentionPatch{
		Enabled:       BoolPtr(true),
		RetentionDays: &days,
	})
	if err != nil {
		t.Fatalf("PatchRetention failed: %v", err)
	}
	if res.Config.Retention.Enabled == nil || !*res.Config.Retention.Enabled || res.Config.Retention.RetentionDays != 45 {
		t.Fatalf("unexpected retention config: %#v", res.Config.Retention)
	}
	if notified.Load() != 1 {
		t.Fatalf("retention notifier count = %d, want 1", notified.Load())
	}

	stored, ok, err := st.LoadManagerConfig(t.Context())
	if err != nil || !ok {
		t.Fatalf("LoadManagerConfig failed: ok=%v err=%v", ok, err)
	}
	if stored.CPAConnection.CPABaseURL != seed.CPAConnection.CPABaseURL ||
		stored.CPAConnection.ManagementKey != seed.CPAConnection.ManagementKey {
		t.Fatalf("patch altered CPAConnection: got %#v", stored.CPAConnection)
	}

	badDays := 4000
	if _, err := svc.PatchRetention(t.Context(), store.ManagerRetentionPatch{RetentionDays: &badDays}); err == nil {
		t.Fatal("expected out-of-range retentionDays error")
	}
	badMode := "bogus"
	if _, err := svc.PatchCollector(t.Context(), store.ManagerCollectorPatch{CollectorMode: &badMode}); err == nil {
		t.Fatal("expected invalid collectorMode error")
	}

	cfgLocked := testutil.NewConfig(t)
	cfgLocked.UsageArchiveRetentionEnvSet = true
	cfgLocked.UsageArchiveRetentionEnabled = true
	cfgLocked.UsageArchiveRetentionDays = 90
	svcLocked := New(cfgLocked, st, collector)
	if _, err := svcLocked.PatchRetention(t.Context(), store.ManagerRetentionPatch{Enabled: BoolPtr(false)}); err == nil {
		t.Fatal("expected env-locked retention error")
	}
}

func TestPatchCollectorAndRetentionMissingManagerRowDoNotPersistConnectionOrMintRevision(t *testing.T) {
	t.Run("PatchRetention under SourceEnv with empty DB keeps CPAConnection empty in SQLite", func(t *testing.T) {
		cfg := testutil.NewConfig(t)
		cfg.CPAUpstreamURL = "http://env-cpa.local:8317"
		cfg.ManagementKey = "env-secret-key"
		st := testutil.NewStore(t, cfg)
		collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
		svc := New(cfg, st, collector)
		svc.UseExternalCollectorLifecycle()

		days := 45
		res, err := svc.PatchRetention(t.Context(), store.ManagerRetentionPatch{
			Enabled:       BoolPtr(true),
			RetentionDays: &days,
		})
		if err != nil {
			t.Fatalf("PatchRetention under SourceEnv failed: %v", err)
		}
		if res.Source != string(SourceEnv) {
			t.Fatalf("source = %q, want %q", res.Source, SourceEnv)
		}
		if res.Config.Retention.Enabled == nil || !*res.Config.Retention.Enabled || res.Config.Retention.RetentionDays != 45 {
			t.Fatalf("unexpected retention config: %#v", res.Config.Retention)
		}
		stored, ok, err := st.LoadManagerConfig(t.Context())
		if err != nil || !ok {
			t.Fatalf("LoadManagerConfig: ok=%v err=%v", ok, err)
		}
		if stored.CPAConnection.CPABaseURL != "" || stored.CPAConnection.ManagementKey != "" {
			t.Fatalf("PatchRetention persisted SourceEnv CPAConnection into SQLite: %#v", stored.CPAConnection)
		}
		if rev, found, err := st.Settings.LoadPersistedCPAConnectionRevision(t.Context()); err != nil || found || rev != "" {
			t.Fatalf("LoadPersistedCPAConnectionRevision = %q found=%v err=%v, want found=false", rev, found, err)
		}
	})

	t.Run("PatchRetention with lone setup row keeps LoadPersistedCPAConnectionRevision false", func(t *testing.T) {
		cfg := testutil.NewConfig(t)
		st := testutil.NewStore(t, cfg)
		collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
		svc := New(cfg, st, collector)
		svc.UseExternalCollectorLifecycle()

		if err := st.SaveSetup(t.Context(), store.Setup{
			CPAUpstreamURL: "http://legacy-cpa.local:8317",
			ManagementKey:  "legacy-unverified-key",
			Queue:          "legacy-queue",
			PopSide:        "left",
		}); err != nil {
			t.Fatal(err)
		}

		days := 60
		res, err := svc.PatchRetention(t.Context(), store.ManagerRetentionPatch{
			Enabled:       BoolPtr(true),
			RetentionDays: &days,
		})
		if err != nil {
			t.Fatalf("PatchRetention with lone setup row failed: %v", err)
		}
		if res.Config.CPAConnection.CPABaseURL != "http://legacy-cpa.local:8317" || !res.Config.CPAConnection.ManagementKeyConfigured {
			t.Fatalf("resolved connection lost legacy setup fallback: %#v", res.Config.CPAConnection)
		}
		stored, ok, err := st.LoadManagerConfig(t.Context())
		if err != nil || !ok {
			t.Fatalf("LoadManagerConfig: ok=%v err=%v", ok, err)
		}
		if stored.CPAConnection.CPABaseURL != "" || stored.CPAConnection.ManagementKey != "" {
			t.Fatalf("PatchRetention copied legacy setup into manager_config_v1: %#v", stored.CPAConnection)
		}
		if rev, found, err := st.Settings.LoadPersistedCPAConnectionRevision(t.Context()); err != nil || found || rev != "" {
			t.Fatalf("LoadPersistedCPAConnectionRevision = %q found=%v err=%v, want found=false", rev, found, err)
		}
	})

	t.Run("PatchCollector Queue only with lone setup row keeps LoadPersistedCPAConnectionRevision false", func(t *testing.T) {
		cfg := testutil.NewConfig(t)
		st := testutil.NewStore(t, cfg)
		collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
		svc := New(cfg, st, collector)
		svc.UseExternalCollectorLifecycle()

		if err := st.SaveSetup(t.Context(), store.Setup{
			CPAUpstreamURL: "http://legacy-cpa.local:8317",
			ManagementKey:  "legacy-unverified-key",
			Queue:          "legacy-queue",
			PopSide:        "left",
		}); err != nil {
			t.Fatal(err)
		}

		queue := "patched-queue"
		res, err := svc.PatchCollector(t.Context(), store.ManagerCollectorPatch{
			Queue: &queue,
		})
		if err != nil {
			t.Fatalf("PatchCollector with lone setup row failed: %v", err)
		}
		if res.Config.Collector.Queue != "patched-queue" || res.Config.Collector.PopSide != "left" {
			t.Fatalf("unexpected collector config after queue patch: %#v", res.Config.Collector)
		}
		if res.Config.CPAConnection.CPABaseURL != "http://legacy-cpa.local:8317" || !res.Config.CPAConnection.ManagementKeyConfigured {
			t.Fatalf("resolved connection lost legacy setup fallback: %#v", res.Config.CPAConnection)
		}
		stored, ok, err := st.LoadManagerConfig(t.Context())
		if err != nil || !ok {
			t.Fatalf("LoadManagerConfig: ok=%v err=%v", ok, err)
		}
		if stored.CPAConnection.CPABaseURL != "" || stored.CPAConnection.ManagementKey != "" {
			t.Fatalf("PatchCollector copied legacy setup into manager_config_v1: %#v", stored.CPAConnection)
		}
		if stored.Collector.Queue != "patched-queue" {
			t.Fatalf("stored collector queue = %q, want patched-queue", stored.Collector.Queue)
		}
		if rev, found, err := st.Settings.LoadPersistedCPAConnectionRevision(t.Context()); err != nil || found || rev != "" {
			t.Fatalf("LoadPersistedCPAConnectionRevision = %q found=%v err=%v, want found=false", rev, found, err)
		}
	})
}

func TestPatchRetentionPartialEnvironmentLock(t *testing.T) {
	t.Run("USAGE_ARCHIVE_RETENTION_DAYS set alone locks days but allows toggling Enabled", func(t *testing.T) {
		cfg := testutil.NewConfig(t)
		cfg.UsageArchiveRetentionDays = 60
		cfg.UsageArchiveRetentionDaysEnvSet = true
		st := testutil.NewStore(t, cfg)
		collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
		svc := New(cfg, st, collector)
		svc.UseExternalCollectorLifecycle()

		initial, err := svc.Get(t.Context())
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if initial.Config.Retention.EnvLocked ||
			initial.Config.Retention.EnabledEnvLocked ||
			!initial.Config.Retention.RetentionDaysEnvLocked ||
			initial.Config.Retention.RetentionDays != 60 ||
			initial.Config.Retention.Enabled == nil || *initial.Config.Retention.Enabled {
			t.Fatalf("unexpected initial retention: %#v", initial.Config.Retention)
		}

		res, err := svc.PatchRetention(t.Context(), store.ManagerRetentionPatch{
			Enabled: BoolPtr(true),
		})
		if err != nil {
			t.Fatalf("PatchRetention(Enabled=true) with only days env set failed: %v", err)
		}
		if res.Config.Retention.EnvLocked ||
			res.Config.Retention.EnabledEnvLocked ||
			!res.Config.Retention.RetentionDaysEnvLocked ||
			res.Config.Retention.RetentionDays != 60 ||
			res.Config.Retention.Enabled == nil || !*res.Config.Retention.Enabled {
			t.Fatalf("unexpected retention after enabling: %#v", res.Config.Retention)
		}
		enabled, days, err := svc.ResolveRetention(t.Context())
		if err != nil || !enabled || days != 60 {
			t.Fatalf("ResolveRetention = (%v, %d, %v), want (true, 60, nil)", enabled, days, err)
		}

		overrideDays := 30
		if _, err := svc.PatchRetention(t.Context(), store.ManagerRetentionPatch{
			RetentionDays: &overrideDays,
		}); err == nil || !strings.Contains(err.Error(), "retention settings are managed by environment variables") {
			t.Fatalf("PatchRetention(RetentionDays) error = %v, want env-managed error", err)
		}

		matchingDays := 60
		sameRes, err := svc.PatchRetention(t.Context(), store.ManagerRetentionPatch{
			Enabled:       BoolPtr(false),
			RetentionDays: &matchingDays,
		})
		if err != nil {
			t.Fatalf("PatchRetention with matching locked RetentionDays failed: %v", err)
		}
		if sameRes.Config.Retention.Enabled == nil || *sameRes.Config.Retention.Enabled ||
			sameRes.Config.Retention.RetentionDays != 60 {
			t.Fatalf("unexpected retention after matching days patch: %#v", sameRes.Config.Retention)
		}
		var raw string
		if err := st.SQLDB().QueryRowContext(t.Context(), `select value from settings where key = 'manager_config_v1'`).Scan(&raw); err != nil {
			t.Fatalf("query raw manager_config_v1: %v", err)
		}
		for _, forbidden := range []string{`"retentionDays"`, `"envLocked"`, `"enabledEnvLocked"`, `"retentionDaysEnvLocked"`} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("raw manager_config_v1 persisted locked/metadata field %s: %s", forbidden, raw)
			}
		}
	})

	t.Run("USAGE_ARCHIVE_RETENTION_ENABLED set alone locks Enabled but allows updating RetentionDays", func(t *testing.T) {
		cfg := testutil.NewConfig(t)
		cfg.UsageArchiveRetentionEnabled = true
		cfg.UsageArchiveRetentionEnabledEnvSet = true
		st := testutil.NewStore(t, cfg)
		collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
		svc := New(cfg, st, collector)
		svc.UseExternalCollectorLifecycle()

		days := 90
		res, err := svc.PatchRetention(t.Context(), store.ManagerRetentionPatch{
			RetentionDays: &days,
		})
		if err != nil {
			t.Fatalf("PatchRetention(RetentionDays=90) with only enabled env set failed: %v", err)
		}
		if res.Config.Retention.EnvLocked ||
			!res.Config.Retention.EnabledEnvLocked ||
			res.Config.Retention.RetentionDaysEnvLocked ||
			res.Config.Retention.RetentionDays != 90 ||
			res.Config.Retention.Enabled == nil || !*res.Config.Retention.Enabled {
			t.Fatalf("unexpected retention after updating days: %#v", res.Config.Retention)
		}
		if _, err := svc.PatchRetention(t.Context(), store.ManagerRetentionPatch{
			Enabled: BoolPtr(false),
		}); err == nil || !strings.Contains(err.Error(), "retention settings are managed by environment variables") {
			t.Fatalf("PatchRetention(Enabled=false) error = %v, want env-managed error", err)
		}

		days120 := 120
		sameEnabledRes, err := svc.PatchRetention(t.Context(), store.ManagerRetentionPatch{
			Enabled:       BoolPtr(true),
			RetentionDays: &days120,
		})
		if err != nil {
			t.Fatalf("PatchRetention with matching locked Enabled failed: %v", err)
		}
		if sameEnabledRes.Config.Retention.Enabled == nil || !*sameEnabledRes.Config.Retention.Enabled ||
			sameEnabledRes.Config.Retention.RetentionDays != 120 {
			t.Fatalf("unexpected retention after matching enabled patch: %#v", sameEnabledRes.Config.Retention)
		}
		var raw string
		if err := st.SQLDB().QueryRowContext(t.Context(), `select value from settings where key = 'manager_config_v1'`).Scan(&raw); err != nil {
			t.Fatalf("query raw manager_config_v1: %v", err)
		}
		for _, forbidden := range []string{`"enabled"`, `"envLocked"`, `"enabledEnvLocked"`, `"retentionDaysEnvLocked"`} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("raw manager_config_v1 persisted locked/metadata field %s: %s", forbidden, raw)
			}
		}
	})
}

func TestManagerConfigPreservesProcessTLSSkipVerifyUntilExplicitlyPersisted(t *testing.T) {
	cfg := testutil.NewConfig(t)
	cfg.TLSSkipVerify = true
	st := testutil.NewStore(t, cfg)
	collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
	svc := New(cfg, st, collector)
	svc.UseExternalCollectorLifecycle()

	if def := svc.DefaultManagerConfig(); !def.Collector.TLSSkipVerify {
		t.Fatalf("DefaultManagerConfig().Collector.TLSSkipVerify = false, want true")
	}

	days := 45
	if _, err := svc.PatchRetention(t.Context(), store.ManagerRetentionPatch{
		Enabled:       BoolPtr(true),
		RetentionDays: &days,
	}); err != nil {
		t.Fatalf("PatchRetention failed: %v", err)
	}

	resolvedAfterRetention, _, _, err := svc.ResolveManagerConfigWithSource(t.Context())
	if err != nil {
		t.Fatalf("ResolveManagerConfigWithSource after PatchRetention: %v", err)
	}
	if !resolvedAfterRetention.Collector.TLSSkipVerify {
		t.Fatalf("ResolveManagerConfigWithSource lost cfg.TLSSkipVerify after PatchRetention: %#v", resolvedAfterRetention.Collector)
	}

	queue := "events_v2"
	if _, err := svc.PatchCollector(t.Context(), store.ManagerCollectorPatch{
		Queue: &queue,
	}); err != nil {
		t.Fatalf("PatchCollector(Queue) failed: %v", err)
	}

	loadedSparse, ok, err := st.LoadManagerConfig(t.Context())
	if err != nil || !ok {
		t.Fatalf("LoadManagerConfig after PatchCollector(Queue) = (%v, %v), want ok", ok, err)
	}
	if loadedSparse.Collector.TLSSkipVerifyExplicit {
		t.Fatalf("LoadManagerConfig marked TLSSkipVerifyExplicit on sparse collector patch: %#v", loadedSparse.Collector)
	}
	if rtCfg := collectorservice.RuntimeConfigFromManagerConfigWithFallback(loadedSparse, cfg); !rtCfg.TLSSkipVerify || rtCfg.Queue != queue {
		t.Fatalf("RuntimeConfigFromManagerConfigWithFallback = %#v, want TLSSkipVerify=true Queue=%q", rtCfg, queue)
	}
	resolvedAfterQueue, _, _, err := svc.ResolveManagerConfigWithSource(t.Context())
	if err != nil {
		t.Fatalf("ResolveManagerConfigWithSource after PatchCollector(Queue): %v", err)
	}
	if !resolvedAfterQueue.Collector.TLSSkipVerify || resolvedAfterQueue.Collector.Queue != queue {
		t.Fatalf("ResolveManagerConfigWithSource after PatchCollector(Queue) = %#v, want TLSSkipVerify=true Queue=%q", resolvedAfterQueue.Collector, queue)
	}

	mergedImplicit := svc.MergeSubmittedManagerConfig(svc.DefaultManagerConfig(), store.ManagerConfig{
		Collector: store.ManagerCollectorConfig{Queue: "implicit-q"},
	})
	if !mergedImplicit.Collector.TLSSkipVerify {
		t.Fatalf("MergeSubmittedManagerConfig clobbered TLSSkipVerify when TLSSkipVerifyExplicit=false: %#v", mergedImplicit.Collector)
	}

	explicitFalse := false
	if _, err := svc.PatchCollector(t.Context(), store.ManagerCollectorPatch{
		TLSSkipVerify: &explicitFalse,
	}); err != nil {
		t.Fatalf("PatchCollector(TLSSkipVerify=false) failed: %v", err)
	}
	loadedExplicit, ok, err := st.LoadManagerConfig(t.Context())
	if err != nil || !ok {
		t.Fatalf("LoadManagerConfig after explicit false = (%v, %v)", ok, err)
	}
	if !loadedExplicit.Collector.TLSSkipVerifyExplicit || loadedExplicit.Collector.TLSSkipVerify {
		t.Fatalf("LoadManagerConfig explicit false = %#v, want TLSSkipVerify=false TLSSkipVerifyExplicit=true", loadedExplicit.Collector)
	}
	if rtCfg := collectorservice.RuntimeConfigFromManagerConfigWithFallback(loadedExplicit, cfg); rtCfg.TLSSkipVerify {
		t.Fatalf("RuntimeConfigFromManagerConfigWithFallback = %#v, want TLSSkipVerify=false", rtCfg)
	}
	resolvedExplicit, _, _, err := svc.ResolveManagerConfigWithSource(t.Context())
	if err != nil {
		t.Fatalf("ResolveManagerConfigWithSource after explicit false: %v", err)
	}
	if resolvedExplicit.Collector.TLSSkipVerify {
		t.Fatalf("ResolveManagerConfigWithSource = %#v, want TLSSkipVerify=false", resolvedExplicit.Collector)
	}

	// SaveManagerConfig with TLSSkipVerifyExplicit=false omits "tlsSkipVerify" so
	// sparse/connection-handoff rows preserve process cfg.TLSSkipVerify=true.
	cfg2 := testutil.NewConfig(t)
	cfg2.TLSSkipVerify = true
	st2 := testutil.NewStore(t, cfg2)
	if err := st2.SaveManagerConfig(t.Context(), store.ManagerConfig{
		Collector: store.ManagerCollectorConfig{
			Enabled:       BoolPtr(false),
			CollectorMode: "http",
			TLSSkipVerify: false,
		},
	}); err != nil {
		t.Fatalf("SaveManagerConfig(implicit false): %v", err)
	}
	var sparseSavedRaw string
	if err := st2.SQLDB().QueryRowContext(t.Context(), `select value from settings where key = 'manager_config_v1'`).Scan(&sparseSavedRaw); err != nil {
		t.Fatalf("query sparse saved raw manager_config_v1: %v", err)
	}
	if strings.Contains(sparseSavedRaw, `"tlsSkipVerify"`) {
		t.Fatalf("SaveManagerConfig(TLSSkipVerifyExplicit=false) materialized tlsSkipVerify: %s", sparseSavedRaw)
	}
	loadedSparseSaved, ok, err := st2.LoadManagerConfig(t.Context())
	if err != nil || !ok {
		t.Fatalf("LoadManagerConfig after sparse SaveManagerConfig = (%v, %v)", ok, err)
	}
	if loadedSparseSaved.Collector.TLSSkipVerifyExplicit || loadedSparseSaved.Collector.TLSSkipVerify {
		t.Fatalf("SaveManagerConfig(implicit false) round-trip = %#v, want TLSSkipVerifyExplicit=false TLSSkipVerify=false", loadedSparseSaved.Collector)
	}
	if rtCfg := collectorservice.RuntimeConfigFromManagerConfigWithFallback(loadedSparseSaved, cfg2); !rtCfg.TLSSkipVerify {
		t.Fatalf("RuntimeConfigFromManagerConfigWithFallback after sparse SaveManagerConfig = %#v, want TLSSkipVerify=true", rtCfg)
	}

	// Explicit TLSSkipVerifyExplicit=true (such as whole-config Update PUT) writes
	// "tlsSkipVerify":false so explicit false round-trips across LoadManagerConfig.
	if err := st2.SaveManagerConfig(t.Context(), store.ManagerConfig{
		Collector: store.ManagerCollectorConfig{
			Enabled:               BoolPtr(false),
			CollectorMode:         "http",
			TLSSkipVerify:         false,
			TLSSkipVerifyExplicit: true,
		},
	}); err != nil {
		t.Fatalf("SaveManagerConfig(explicit false): %v", err)
	}
	loadedSaved, ok, err := st2.LoadManagerConfig(t.Context())
	if err != nil || !ok {
		t.Fatalf("LoadManagerConfig after explicit SaveManagerConfig = (%v, %v)", ok, err)
	}
	if !loadedSaved.Collector.TLSSkipVerifyExplicit || loadedSaved.Collector.TLSSkipVerify {
		t.Fatalf("SaveManagerConfig(explicit false) round-trip = %#v, want TLSSkipVerifyExplicit=true TLSSkipVerify=false", loadedSaved.Collector)
	}
}

func TestServiceUpdateRetentionEnvLocksBoundsAndNotifier(t *testing.T) {
	cpaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/config":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":true,"redis-usage-queue-retention-seconds":60}`))
		case r.Method == http.MethodPut && r.URL.Path == "/v0/management/usage-statistics-enabled":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cpaServer.Close)

	t.Run("bounds validation and notifier firing on unlocked Update", func(t *testing.T) {
		cfg := testutil.NewConfig(t)
		st := testutil.NewStore(t, cfg)
		collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
		svc := New(cfg, st, collector)
		svc.UseExternalCollectorLifecycle()
		var notified atomic.Int32
		svc.SetRetentionChangedNotifier(func() { notified.Add(1) })

		for _, invalid := range []store.ManagerRetentionConfig{
			{Enabled: BoolPtr(true), RetentionDays: -5},
			{Enabled: BoolPtr(true), RetentionDays: 3651},
			{Enabled: BoolPtr(true), RetentionDays: 0, RetentionDaysExplicit: true},
		} {
			if _, err := svc.Update(t.Context(), store.ManagerConfig{
				Collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false)},
				Retention: invalid,
			}); err == nil || !strings.Contains(err.Error(), "retentionDays must be between 1 and 3650") {
				t.Fatalf("Update(%#v) error = %v, want retentionDays bounds error", invalid, err)
			}
		}
		if got := notified.Load(); got != 0 {
			t.Fatalf("notifier fired %d times on rejected Update, want 0", got)
		}

		var decodedFromJSON store.ManagerConfig
		if err := json.Unmarshal([]byte(`{"collector":{"enabled":false},"retention":{"retentionDays":0}}`), &decodedFromJSON); err != nil {
			t.Fatalf("unmarshal explicit zero retentionDays: %v", err)
		}
		if _, err := svc.Update(t.Context(), decodedFromJSON); err == nil || !strings.Contains(err.Error(), "retentionDays must be between 1 and 3650") {
			t.Fatalf("Update(JSON retentionDays=0) error = %v, want bounds error", err)
		}

		res, err := svc.Update(t.Context(), store.ManagerConfig{
			CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL:    cpaServer.URL,
				ManagementKey: "secret-key",
			},
			Collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false)},
			Retention: store.ManagerRetentionConfig{
				Enabled:                BoolPtr(true),
				RetentionDays:          45,
				EnvLocked:              true,
				EnabledEnvLocked:       true,
				RetentionDaysEnvLocked: true,
			},
		})
		if err != nil {
			t.Fatalf("valid Update failed: %v", err)
		}
		if got := notified.Load(); got != 1 {
			t.Fatalf("notifier count = %d, want 1", got)
		}
		if res.Config.Retention.EnvLocked || res.Config.Retention.EnabledEnvLocked || res.Config.Retention.RetentionDaysEnvLocked ||
			res.Config.Retention.Enabled == nil || !*res.Config.Retention.Enabled || res.Config.Retention.RetentionDays != 45 {
			t.Fatalf("Update response retention = %#v", res.Config.Retention)
		}

		// Repeating the same retention values must not re-fire notifier.
		if _, err := svc.Update(t.Context(), store.ManagerConfig{
			CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL:    cpaServer.URL,
				ManagementKey: "secret-key",
			},
			Collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false), Queue: "usage_v2"},
			Retention: store.ManagerRetentionConfig{
				Enabled:       BoolPtr(true),
				RetentionDays: 45,
			},
		}); err != nil {
			t.Fatalf("second Update failed: %v", err)
		}
		if got := notified.Load(); got != 1 {
			t.Fatalf("notifier count after unchanged retention = %d, want 1", got)
		}

		var raw string
		if err := st.SQLDB().QueryRowContext(t.Context(), `select value from settings where key = 'manager_config_v1'`).Scan(&raw); err != nil {
			t.Fatalf("query raw manager_config_v1: %v", err)
		}
		for _, forbidden := range []string{`"envLocked"`, `"enabledEnvLocked"`, `"retentionDaysEnvLocked"`} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("raw manager_config_v1 persisted lock metadata %s: %s", forbidden, raw)
			}
		}
	})

	t.Run("full and partial retention env locks on Update and SetCollectorEnabled fallback", func(t *testing.T) {
		// Full lock
		fullCfg := testutil.NewConfig(t)
		fullCfg.UsageArchiveRetentionEnabled = true
		fullCfg.UsageArchiveRetentionDays = 60
		fullCfg.UsageArchiveRetentionEnvSet = true
		fullStore := testutil.NewStore(t, fullCfg)
		fullSvc := New(fullCfg, fullStore, collectorservice.New(collectorpkg.NewManager(fullCfg, fullStore)))
		fullSvc.UseExternalCollectorLifecycle()

		// SetCollectorEnabled fallback save on empty DB strips env-lock metadata.
		if _, err := fullSvc.SetCollectorEnabled(t.Context(), false); err != nil {
			t.Fatalf("SetCollectorEnabled(false) on empty DB failed: %v", err)
		}
		var fallbackRaw string
		if err := fullStore.SQLDB().QueryRowContext(t.Context(), `select value from settings where key = 'manager_config_v1'`).Scan(&fallbackRaw); err != nil {
			t.Fatalf("query fallback raw manager_config_v1: %v", err)
		}
		for _, forbidden := range []string{`"envLocked"`, `"enabledEnvLocked"`, `"retentionDaysEnvLocked"`} {
			if strings.Contains(fallbackRaw, forbidden) {
				t.Fatalf("SetCollectorEnabled fallback persisted %s: %s", forbidden, fallbackRaw)
			}
		}

		if _, err := fullSvc.Update(t.Context(), store.ManagerConfig{
			Collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false)},
			Retention: store.ManagerRetentionConfig{Enabled: BoolPtr(false)},
		}); err == nil || !strings.Contains(err.Error(), "retention settings are managed by environment variables") {
			t.Fatalf("Update changing full-locked Enabled error = %v, want env-managed", err)
		}
		if _, err := fullSvc.Update(t.Context(), store.ManagerConfig{
			Collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false)},
			Retention: store.ManagerRetentionConfig{RetentionDays: 15},
		}); err == nil || !strings.Contains(err.Error(), "retention settings are managed by environment variables") {
			t.Fatalf("Update changing full-locked RetentionDays error = %v, want env-managed", err)
		}
		fullRes, err := fullSvc.Update(t.Context(), store.ManagerConfig{
			Collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false), Queue: "full-q"},
			Retention: store.ManagerRetentionConfig{Enabled: BoolPtr(true), RetentionDays: 60},
		})
		if err != nil {
			t.Fatalf("Update matching full-locked retention failed: %v", err)
		}
		if !fullRes.Config.Retention.EnvLocked || !fullRes.Config.Retention.EnabledEnvLocked || !fullRes.Config.Retention.RetentionDaysEnvLocked {
			t.Fatalf("Update under full lock lost projected lock flags: %#v", fullRes.Config.Retention)
		}

		// Partial lock: Enabled locked to true, Days unlocked.
		partCfg := testutil.NewConfig(t)
		partCfg.UsageArchiveRetentionEnabled = true
		partCfg.UsageArchiveRetentionEnabledEnvSet = true
		partStore := testutil.NewStore(t, partCfg)
		partSvc := New(partCfg, partStore, collectorservice.New(collectorpkg.NewManager(partCfg, partStore)))
		partSvc.UseExternalCollectorLifecycle()
		var partNotified atomic.Int32
		partSvc.SetRetentionChangedNotifier(func() { partNotified.Add(1) })

		if _, err := partSvc.Update(t.Context(), store.ManagerConfig{
			Collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false)},
			Retention: store.ManagerRetentionConfig{Enabled: BoolPtr(false), RetentionDays: 90},
		}); err == nil || !strings.Contains(err.Error(), "retention settings are managed by environment variables") {
			t.Fatalf("Update changing locked Enabled error = %v, want env-managed", err)
		}
		partRes, err := partSvc.Update(t.Context(), store.ManagerConfig{
			Collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false)},
			Retention: store.ManagerRetentionConfig{Enabled: BoolPtr(true), RetentionDays: 90},
		})
		if err != nil {
			t.Fatalf("Update changing unlocked RetentionDays under partial lock failed: %v", err)
		}
		if partNotified.Load() != 1 ||
			partRes.Config.Retention.EnvLocked ||
			!partRes.Config.Retention.EnabledEnvLocked ||
			partRes.Config.Retention.RetentionDaysEnvLocked ||
			partRes.Config.Retention.RetentionDays != 90 {
			t.Fatalf("unexpected partial-lock Update result (notified=%d): %#v", partNotified.Load(), partRes.Config.Retention)
		}
	})
}

func TestPatchCollectorValidatesPollIntervalWhenCPAUsageProbeAllowed(t *testing.T) {
	var cpaProbeCount atomic.Int32
	cpaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v0/management/config" {
			cpaProbeCount.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":true,"redis-usage-queue-retention-seconds":2}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(cpaServer.Close)

	seedActiveConnection := func(t *testing.T) (*store.Store, *Service) {
		t.Helper()
		cfg := testutil.NewConfig(t)
		st := testutil.NewStore(t, cfg)
		t.Cleanup(func() { _ = st.Close() })
		if err := st.SaveManagerConfig(t.Context(), store.ManagerConfig{
			CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL:    cpaServer.URL,
				ManagementKey: "secret-key",
			},
			Collector: store.ManagerCollectorConfig{
				Enabled:        BoolPtr(true),
				CollectorMode:  "http",
				Queue:          "usage",
				PopSide:        "right",
				BatchSize:      100,
				PollIntervalMS: 500,
				QueryLimit:     50000,
			},
		}); err != nil {
			t.Fatalf("seed manager config: %v", err)
		}
		mgr := collectorpkg.NewManager(cfg, st)
		t.Cleanup(func() { _ = mgr.StopAndWait(context.Background()) })
		svc := New(cfg, st, collectorservice.New(mgr))
		return st, svc
	}

	t.Run("standalone !externalCollectorLifecycle rejects excessive pollIntervalMs", func(t *testing.T) {
		_, svc := seedActiveConnection(t)
		tooSlow := 2500
		if _, err := svc.PatchCollector(t.Context(), store.ManagerCollectorPatch{
			PollIntervalMS: &tooSlow,
		}); err == nil || !strings.Contains(err.Error(), "redis-usage-queue-retention-seconds") {
			t.Fatalf("standalone PatchCollector(2500ms) error = %v, want queue retention error", err)
		}
	})

	t.Run("v2 Managed externalCollectorLifecycle with probe allowed rejects excessive pollIntervalMs", func(t *testing.T) {
		_, svc := seedActiveConnection(t)
		svc.UseExternalCollectorLifecycle()
		svc.SetCPAUsageProbeAllowed(func(context.Context) bool { return true })

		tooSlow := 2500
		if _, err := svc.PatchCollector(t.Context(), store.ManagerCollectorPatch{
			PollIntervalMS: &tooSlow,
		}); err == nil || !strings.Contains(err.Error(), "redis-usage-queue-retention-seconds") {
			t.Fatalf("Managed PatchCollector(2500ms) error = %v, want queue retention error", err)
		}

		valid := 1500
		res, err := svc.PatchCollector(t.Context(), store.ManagerCollectorPatch{
			PollIntervalMS: &valid,
		})
		if err != nil || res.Config.Collector.PollIntervalMS != 1500 {
			t.Fatalf("Managed PatchCollector(1500ms) = (%#v, %v), want 1500ms", res, err)
		}
	})

	t.Run("v2 External probe disallowed skips CPA validation", func(t *testing.T) {
		_, svc := seedActiveConnection(t)
		svc.UseExternalCollectorLifecycle()
		svc.SetCPAUsageProbeAllowed(func(context.Context) bool { return false })

		before := cpaProbeCount.Load()
		slow := 5000
		res, err := svc.PatchCollector(t.Context(), store.ManagerCollectorPatch{
			PollIntervalMS: &slow,
		})
		if err != nil || res.Config.Collector.PollIntervalMS != 5000 {
			t.Fatalf("External PatchCollector(5000ms) = (%#v, %v), want 5000ms", res, err)
		}
		if got := cpaProbeCount.Load() - before; got != 0 {
			t.Fatalf("External PatchCollector probed CPA %d times, want 0", got)
		}
	})
}

func TestSetCollectorEnabledOnSetupOnlyAndEmptyDBKeepsNarrowDocument(t *testing.T) {
	t.Run("empty DB writes minimal collector switch without tlsSkipVerify or defaults", func(t *testing.T) {
		cfg := testutil.NewConfig(t)
		cfg.TLSSkipVerify = true
		st := testutil.NewStore(t, cfg)
		collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
		svc := New(cfg, st, collector)
		svc.UseExternalCollectorLifecycle()

		res, err := svc.SetCollectorEnabled(t.Context(), false)
		if err != nil {
			t.Fatalf("SetCollectorEnabled(false) on empty DB failed: %v", err)
		}
		if res.Config.Collector.Enabled == nil || *res.Config.Collector.Enabled || !res.Config.Collector.TLSSkipVerify {
			t.Fatalf("SetCollectorEnabled(false) response collector = %#v, want enabled=false tlsSkipVerify=true", res.Config.Collector)
		}
		var raw string
		if err := st.SQLDB().QueryRowContext(t.Context(), `select value from settings where key = 'manager_config_v1'`).Scan(&raw); err != nil {
			t.Fatalf("query raw manager_config_v1: %v", err)
		}
		if strings.Contains(raw, `"tlsSkipVerify"`) || strings.Contains(raw, `"cpaConnection"`) || strings.Contains(raw, `"codexInspection"`) {
			t.Fatalf("empty DB SetCollectorEnabled(false) materialized whole config: %s", raw)
		}
		if _, found, err := st.Settings.LoadPersistedCPAConnectionRevision(t.Context()); err != nil || found {
			t.Fatalf("empty DB SetCollectorEnabled(false) minted revision: found=%v err=%v", found, err)
		}
	})

	t.Run("setup-only DB does not copy setup credentials or materialize tlsSkipVerify false", func(t *testing.T) {
		cfg := testutil.NewConfig(t)
		cfg.TLSSkipVerify = true
		st := testutil.NewStore(t, cfg)
		collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
		svc := New(cfg, st, collector)
		svc.UseExternalCollectorLifecycle()

		if err := st.SaveSetup(t.Context(), store.Setup{
			CPAUpstreamURL: "https://legacy-cpa.local:8317",
			ManagementKey:  "legacy-setup-only-secret",
			Queue:          "legacy-queue",
			PopSide:        "left",
		}); err != nil {
			t.Fatalf("SaveSetup: %v", err)
		}

		res, err := svc.SetCollectorEnabled(t.Context(), false)
		if err != nil {
			t.Fatalf("SetCollectorEnabled(false) on setup-only DB failed: %v", err)
		}
		if res.Config.CPAConnection.CPABaseURL != "https://legacy-cpa.local:8317" || !res.Config.CPAConnection.ManagementKeyConfigured {
			t.Fatalf("resolved connection lost legacy setup fallback: %#v", res.Config.CPAConnection)
		}
		if !res.Config.Collector.TLSSkipVerify {
			t.Fatalf("SetCollectorEnabled(false) clobbered cfg.TLSSkipVerify=true: %#v", res.Config.Collector)
		}
		var raw string
		if err := st.SQLDB().QueryRowContext(t.Context(), `select value from settings where key = 'manager_config_v1'`).Scan(&raw); err != nil {
			t.Fatalf("query raw manager_config_v1: %v", err)
		}
		if strings.Contains(raw, `"tlsSkipVerify"`) || strings.Contains(raw, `"cpaConnection"`) || strings.Contains(raw, "legacy-cpa.local") {
			t.Fatalf("setup-only SetCollectorEnabled(false) leaked setup fields or tlsSkipVerify into manager_config_v1: %s", raw)
		}
		stored, ok, err := st.LoadManagerConfig(t.Context())
		if err != nil || !ok {
			t.Fatalf("LoadManagerConfig = (%v, %v)", ok, err)
		}
		if stored.CPAConnection.CPABaseURL != "" || stored.CPAConnection.ManagementKey != "" || stored.Collector.TLSSkipVerifyExplicit {
			t.Fatalf("stored manager_config_v1 = %#v, want empty connection and TLSSkipVerifyExplicit=false", stored)
		}
		if _, found, err := st.Settings.LoadPersistedCPAConnectionRevision(t.Context()); err != nil || found {
			t.Fatalf("setup-only SetCollectorEnabled(false) minted revision: found=%v err=%v", found, err)
		}
	})
}

func TestPatchRetentionDualEnvLockedAllowsIdempotentSameValuePatch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Service)
	}{
		{
			name: "legacy combined retention env lock",
			setup: func(svc *Service) {
				svc.cfg.UsageArchiveRetentionEnvSet = true
			},
		},
		{
			name: "explicit dual enabled and days env locks",
			setup: func(svc *Service) {
				svc.cfg.UsageArchiveRetentionEnabledEnvSet = true
				svc.cfg.UsageArchiveRetentionDaysEnvSet = true
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testutil.NewConfig(t)
			cfg.UsageArchiveRetentionEnabled = true
			cfg.UsageArchiveRetentionDays = 45
			st := testutil.NewStore(t, cfg)
			collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
			svc := New(cfg, st, collector)
			svc.UseExternalCollectorLifecycle()
			tc.setup(svc)
			var notified atomic.Int32
			svc.SetRetentionChangedNotifier(func() { notified.Add(1) })

			sameDays := 45
			for _, samePatch := range []store.ManagerRetentionPatch{
				{Enabled: BoolPtr(true)},
				{RetentionDays: &sameDays},
				{Enabled: BoolPtr(true), RetentionDays: &sameDays},
			} {
				res, err := svc.PatchRetention(t.Context(), samePatch)
				if err != nil {
					t.Fatalf("idempotent PatchRetention(%#v) under dual lock failed: %v", samePatch, err)
				}
				if !res.Config.Retention.EnvLocked || !res.Config.Retention.EnabledEnvLocked || !res.Config.Retention.RetentionDaysEnvLocked ||
					res.Config.Retention.Enabled == nil || !*res.Config.Retention.Enabled || res.Config.Retention.RetentionDays != 45 {
					t.Fatalf("unexpected retention response = %#v", res.Config.Retention)
				}
			}
			if got := notified.Load(); got != 0 {
				t.Fatalf("retention notifier fired %d times on no-op env-locked patches, want 0", got)
			}
			if _, ok, err := st.LoadManagerConfig(t.Context()); err != nil || ok {
				t.Fatalf("idempotent dual-locked PatchRetention wrote manager_config_v1: ok=%v err=%v", ok, err)
			}

			diffDays := 30
			for _, diffPatch := range []store.ManagerRetentionPatch{
				{Enabled: BoolPtr(false)},
				{RetentionDays: &diffDays},
				{Enabled: BoolPtr(false), RetentionDays: &sameDays},
				{Enabled: BoolPtr(true), RetentionDays: &diffDays},
			} {
				if _, err := svc.PatchRetention(t.Context(), diffPatch); err == nil || !strings.Contains(err.Error(), "retention settings are managed by environment variables") {
					t.Fatalf("differing PatchRetention(%#v) error = %v, want env-managed error", diffPatch, err)
				}
			}
		})
	}
}

func TestPatchCollectorDoesNotStartCollectorWhenConnectionIncomplete(t *testing.T) {
	t.Run("empty DB with internal collector lifecycle keeps collector stopped", func(t *testing.T) {
		cfg := testutil.NewConfig(t)
		st := testutil.NewStore(t, cfg)
		collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
		t.Cleanup(func() { _ = collector.StopAndWait(context.Background()) })
		svc := New(cfg, st, collector)

		queue := "patched-before-setup"
		res, err := svc.PatchCollector(t.Context(), store.ManagerCollectorPatch{
			Queue: &queue,
		})
		if err != nil {
			t.Fatalf("PatchCollector failed: %v", err)
		}
		if res.Config.Collector.Queue != queue {
			t.Fatalf("Queue = %q, want %q", res.Config.Collector.Queue, queue)
		}
		if got := collector.StartCount(); got != 0 {
			t.Fatalf("collector StartCount = %d, want 0", got)
		}
		if _, active := collector.ActiveRuntimeConfig(); active {
			t.Fatal("collector became active with empty CPA connection")
		}
	})

	t.Run("partial CPA connection stops active collector instead of restarting", func(t *testing.T) {
		cfg := testutil.NewConfig(t)
		st := testutil.NewStore(t, cfg)
		cpaMock := testutil.NewCPAMock(t)
		collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
		t.Cleanup(func() { _ = collector.StopAndWait(context.Background()) })
		svc := New(cfg, st, collector)

		if err := collector.Start(context.Background(), store.ManagerConfig{
			CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL:    cpaMock.URL(),
				ManagementKey: cpaMock.ManagementKey,
			},
			Collector: store.ManagerCollectorConfig{
				Enabled:        BoolPtr(true),
				CollectorMode:  "http",
				Queue:          "usage",
				PopSide:        "right",
				BatchSize:      100,
				PollIntervalMS: 1000,
			},
		}); err != nil {
			t.Fatalf("seed collector Start: %v", err)
		}
		startsBefore := collector.StartCount()
		if _, active := collector.ActiveRuntimeConfig(); !active {
			t.Fatal("expected seeded collector to be active before PatchCollector")
		}

		if err := st.SaveManagerConfig(t.Context(), store.ManagerConfig{
			CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL:    cpaMock.URL(),
				ManagementKey: "",
			},
			Collector: store.ManagerCollectorConfig{
				Enabled: BoolPtr(true),
			},
		}); err != nil {
			t.Fatalf("seed partial manager_config_v1: %v", err)
		}

		batch := 250
		if _, err := svc.PatchCollector(t.Context(), store.ManagerCollectorPatch{
			BatchSize: &batch,
		}); err != nil {
			t.Fatalf("PatchCollector with partial connection failed: %v", err)
		}
		if got := collector.StartCount(); got != startsBefore {
			t.Fatalf("collector StartCount = %d, want %d (must not start on incomplete connection)", got, startsBefore)
		}
		if _, active := collector.ActiveRuntimeConfig(); active {
			t.Fatal("collector remained active after PatchCollector with incomplete CPA connection")
		}
	})
}

func TestManagerConfigRegressionGuards(t *testing.T) {
	t.Run("Managed mode disable -> PatchCollector pollIntervalMs above CPA retention -> SetCollectorEnabled(true) rejected without persisting", func(t *testing.T) {
		cfg := testutil.NewConfig(t)
		st := testutil.NewStore(t, cfg)
		cpaMock := testutil.NewCPAMock(t)
		cpaMock.UsageStatisticsEnabled = false
		cpaMock.RetentionSeconds = 2
		svc := New(cfg, st, collectorservice.New(collectorpkg.NewManager(cfg, st)))
		svc.UseExternalCollectorLifecycle()
		svc.SetCPAUsageProbeAllowed(func(context.Context) bool { return true })

		if err := st.SaveManagerConfig(t.Context(), store.ManagerConfig{
			CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL:    cpaMock.URL(),
				ManagementKey: cpaMock.ManagementKey,
			},
			Collector: store.ManagerCollectorConfig{
				Enabled:        BoolPtr(true),
				CollectorMode:  "http",
				Queue:          "usage",
				PopSide:        "right",
				BatchSize:      100,
				PollIntervalMS: 500,
				QueryLimit:     50000,
			},
			Retention: store.ManagerRetentionConfig{Enabled: BoolPtr(true), RetentionDays: 30},
		}); err != nil {
			t.Fatalf("SaveManagerConfig: %v", err)
		}

		// 1. Disable collector first.
		if _, err := svc.SetCollectorEnabled(t.Context(), false); err != nil {
			t.Fatalf("SetCollectorEnabled(false): %v", err)
		}

		// 2. While disabled, PatchCollector raises pollIntervalMs past CPA queue retention (2500ms > 2000ms).
		tooSlow := 2500
		if _, err := svc.PatchCollector(t.Context(), store.ManagerCollectorPatch{PollIntervalMS: &tooSlow}); err != nil {
			t.Fatalf("PatchCollector while disabled: %v", err)
		}

		// 3. Re-enabling collector in Managed mode must run read-only ValidateCollectorConfig, fail closed, and not persist enabled=true.
		if _, err := svc.SetCollectorEnabled(t.Context(), true); err == nil || !strings.Contains(err.Error(), "redis-usage-queue-retention-seconds") {
			t.Fatalf("expected SetCollectorEnabled(true) to reject excessive pollIntervalMs, got %v", err)
		}
		stored, ok, err := st.LoadManagerConfig(t.Context())
		if err != nil || !ok {
			t.Fatalf("LoadManagerConfig = (%t, %v)", ok, err)
		}
		if stored.Collector.Enabled == nil || *stored.Collector.Enabled {
			t.Fatalf("SetCollectorEnabled failure persisted enabled=%v, want false", stored.Collector.Enabled)
		}
		if stored.Collector.PollIntervalMS != tooSlow {
			t.Fatalf("stored PollIntervalMS = %d, want %d", stored.Collector.PollIntervalMS, tooSlow)
		}
		for _, req := range cpaMock.Requests() {
			if req.Path == "/v0/management/usage-statistics-enabled" {
				t.Fatalf("SetCollectorEnabled under externalCollectorLifecycle mutated CPA usage-statistics-enabled: %+v", req)
			}
		}
	})

	t.Run("Update enforces collector field bounds and strict mode/popSide/queue validation", func(t *testing.T) {
		cfg := testutil.NewConfig(t)
		st := testutil.NewStore(t, cfg)
		svc := New(cfg, st, collectorservice.New(collectorpkg.NewManager(cfg, st)))

		for _, tc := range []struct {
			name      string
			collector store.ManagerCollectorConfig
			wantErr   string
		}{
			{
				name:      "invalid collectorMode",
				collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false), CollectorMode: "bogus"},
				wantErr:   "collectorMode must be",
			},
			{
				name:      "whitespace collectorMode",
				collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false), CollectorMode: "   "},
				wantErr:   "collectorMode must be",
			},
			{
				name:      "invalid popSide",
				collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false), PopSide: "middle"},
				wantErr:   "popSide must be",
			},
			{
				name:      "whitespace popSide",
				collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false), PopSide: "   "},
				wantErr:   "popSide must be",
			},
			{
				name:      "whitespace queue",
				collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false), Queue: "   "},
				wantErr:   "queue must not be empty",
			},
			{
				name:      "control char queue",
				collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false), Queue: "usage\r\nextra"},
				wantErr:   "queue must not be empty",
			},
			{
				name:      "negative batchSize",
				collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false), BatchSize: -1},
				wantErr:   "batchSize must be between 1 and 5000",
			},
			{
				name:      "oversize batchSize",
				collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false), BatchSize: 5001},
				wantErr:   "batchSize must be between 1 and 5000",
			},
			{
				name:      "too small pollIntervalMs",
				collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false), PollIntervalMS: 9},
				wantErr:   "pollIntervalMs must be between 10 and 60000",
			},
			{
				name:      "too large pollIntervalMs",
				collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false), PollIntervalMS: 60001},
				wantErr:   "pollIntervalMs must be between 10 and 60000",
			},
			{
				name:      "negative queryLimit",
				collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false), QueryLimit: -5},
				wantErr:   "queryLimit must be between 1 and 100000",
			},
			{
				name:      "oversize queryLimit",
				collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false), QueryLimit: 100001},
				wantErr:   "queryLimit must be between 1 and 100000",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := svc.Update(t.Context(), store.ManagerConfig{Collector: tc.collector})
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Update(%s) err = %v, want substring %q", tc.name, err, tc.wantErr)
				}
			})
		}
	})

	t.Run("Update respects cpaUsageProbeAllowed=false and externalCollectorLifecycle=true", func(t *testing.T) {
		cfg := testutil.NewConfig(t)
		st := testutil.NewStore(t, cfg)
		cpaMock := testutil.NewCPAMock(t)
		cpaMock.UsageStatisticsEnabled = false
		cpaMock.RetentionSeconds = 2
		svc := New(cfg, st, collectorservice.New(collectorpkg.NewManager(cfg, st)))

		// 1. When cpaUsageProbeAllowed returns false, Update skips all CPA validation and mutation calls.
		svc.SetCPAUsageProbeAllowed(func(context.Context) bool { return false })
		resp, err := svc.Update(t.Context(), store.ManagerConfig{
			CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL:    "http://127.0.0.1:1",
				ManagementKey: "unprobed-external-key",
			},
			Collector: store.ManagerCollectorConfig{
				Enabled:        BoolPtr(true),
				CollectorMode:  "http",
				Queue:          "usage",
				PopSide:        "right",
				BatchSize:      100,
				PollIntervalMS: 5000,
				QueryLimit:     50000,
			},
		})
		if err != nil {
			t.Fatalf("Update with cpaUsageProbeAllowed=false failed: %v", err)
		}
		if resp.Config.Collector.Enabled == nil || !*resp.Config.Collector.Enabled || resp.Config.Collector.PollIntervalMS != 5000 {
			t.Fatalf("unexpected saved config with probe disabled: %+v", resp.Config.Collector)
		}

		// 2. When externalCollectorLifecycle=true and probe is allowed:
		//    - enabled=true runs read-only ValidateCollectorConfig (rejecting 2500ms > 2s retention, accepting 500ms)
		//    - never calls SetUsageStatisticsEnabled or starts internal collector
		svc.UseExternalCollectorLifecycle()
		svc.SetCPAUsageProbeAllowed(func(context.Context) bool { return true })

		_, err = svc.Update(t.Context(), store.ManagerConfig{
			CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL:    cpaMock.URL(),
				ManagementKey: cpaMock.ManagementKey,
			},
			Collector: store.ManagerCollectorConfig{
				Enabled:        BoolPtr(true),
				CollectorMode:  "http",
				Queue:          "usage",
				PopSide:        "right",
				BatchSize:      100,
				PollIntervalMS: 2500,
				QueryLimit:     50000,
			},
		})
		if err == nil || !strings.Contains(err.Error(), "redis-usage-queue-retention-seconds") {
			t.Fatalf("Update(enabled=true, poll=2500) under externalCollectorLifecycle = %v, want retention error", err)
		}

		_, err = svc.Update(t.Context(), store.ManagerConfig{
			CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL:    cpaMock.URL(),
				ManagementKey: cpaMock.ManagementKey,
			},
			Collector: store.ManagerCollectorConfig{
				Enabled:        BoolPtr(true),
				CollectorMode:  "http",
				Queue:          "usage",
				PopSide:        "right",
				BatchSize:      100,
				PollIntervalMS: 500,
				QueryLimit:     50000,
			},
		})
		if err != nil {
			t.Fatalf("Update(enabled=true, poll=500) under externalCollectorLifecycle: %v", err)
		}
		if cpaMock.UsageStatisticsEnabled {
			t.Fatal("Update under externalCollectorLifecycle mutated CPA usage-statistics-enabled")
		}
		for _, req := range cpaMock.Requests() {
			if req.Path == "/v0/management/usage-statistics-enabled" {
				t.Fatalf("unexpected PUT /v0/management/usage-statistics-enabled under externalCollectorLifecycle: %+v", req)
			}
		}
	})

	t.Run("ManagerConfigConnectionDiffers detects QueryLimit changes and blocks SourceEnv Update", func(t *testing.T) {
		left := store.ManagerConfig{Collector: store.ManagerCollectorConfig{QueryLimit: 50000}}
		right := store.ManagerConfig{Collector: store.ManagerCollectorConfig{QueryLimit: 10000}}
		if !ManagerConfigConnectionDiffers(left, right) {
			t.Fatal("ManagerConfigConnectionDiffers returned false for distinct QueryLimit")
		}

		cfg := testutil.NewConfig(t)
		cfg.CPAUpstreamURL = "http://127.0.0.1:8317"
		cfg.ManagementKey = "env-managed-key"
		st := testutil.NewStore(t, cfg)
		svc := New(cfg, st, collectorservice.New(collectorpkg.NewManager(cfg, st)))

		current, _, _, err := svc.ResolveManagerConfigWithSource(t.Context())
		if err != nil {
			t.Fatalf("ResolveManagerConfigWithSource: %v", err)
		}
		modified := current
		modified.Collector.QueryLimit = 12345
		if _, err := svc.Update(t.Context(), modified); err == nil || !strings.Contains(err.Error(), "managed by environment variables") {
			t.Fatalf("Update changing QueryLimit under SourceEnv = %v, want env-managed error", err)
		}
	})
}

func TestUpdateRejectsTargetURLChangeWithEmptyManagementKey(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	cpaA := testutil.NewCPAMock(t)

	var urlBHits atomic.Int32
	cpaB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		urlBHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usage-statistics-enabled":true}`))
	}))
	t.Cleanup(cpaB.Close)

	collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
	t.Cleanup(func() { _ = collector.StopAndWait(context.Background()) })
	svc := New(cfg, st, collector)

	urlA := cpaA.URL()
	keyA := cpaA.ManagementKey
	if _, err := svc.Update(t.Context(), store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    urlA,
			ManagementKey: keyA,
		},
		Collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false)},
	}); err != nil {
		t.Fatalf("seed Update(urlA, keyA): %v", err)
	}

	// Same normalized URL with empty managementKey continues to retain keyA.
	base := svc.DefaultManagerConfig()
	base.CPAConnection = store.ManagerCPAConnectionConfig{
		CPABaseURL:    urlA,
		ManagementKey: keyA,
	}
	mergedSame := svc.MergeSubmittedManagerConfig(base, store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    urlA + "/",
			ManagementKey: "",
		},
	})
	if mergedSame.CPAConnection.ManagementKey != keyA {
		t.Fatalf("same-URL MergeSubmittedManagerConfig ManagementKey = %q, want %q", mergedSame.CPAConnection.ManagementKey, keyA)
	}
	mergedChanged := svc.MergeSubmittedManagerConfig(base, store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    cpaB.URL,
			ManagementKey: "",
		},
	})
	if mergedChanged.CPAConnection.ManagementKey != "" {
		t.Fatalf("changed-URL MergeSubmittedManagerConfig ManagementKey = %q, want empty", mergedChanged.CPAConnection.ManagementKey)
	}

	for _, blankKey := range []string{"", "   "} {
		_, err := svc.Update(t.Context(), store.ManagerConfig{
			CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL:    cpaB.URL,
				ManagementKey: blankKey,
			},
			Collector: store.ManagerCollectorConfig{Enabled: BoolPtr(false)},
		})
		if err == nil || err.Error() != "cpaBaseUrl and managementKey are required" {
			t.Fatalf("Update(urlB, %q) error = %v, want %q", blankKey, err, "cpaBaseUrl and managementKey are required")
		}
	}
	if got := urlBHits.Load(); got != 0 {
		t.Fatalf("new target URL received %d HTTP requests, want 0", got)
	}

	storedCfg, ok, err := st.LoadManagerConfig(t.Context())
	if err != nil || !ok {
		t.Fatalf("LoadManagerConfig = (%t, %v), want ok", ok, err)
	}
	if storedCfg.CPAConnection.CPABaseURL != urlA || storedCfg.CPAConnection.ManagementKey != keyA {
		t.Fatalf("stored manager_config_v1 connection = %#v, want (%q, %q)", storedCfg.CPAConnection, urlA, keyA)
	}
	storedSetup, setupOK, err := st.LoadSetup(t.Context())
	if err != nil || !setupOK {
		t.Fatalf("LoadSetup = (%t, %v), want ok", setupOK, err)
	}
	if storedSetup.CPAUpstreamURL != urlA || storedSetup.ManagementKey != keyA {
		t.Fatalf("stored setup connection = %#v, want (%q, %q)", storedSetup, urlA, keyA)
	}
}

func TestUpdateUnderEnvironmentSourcePersistsOnlyNonConnectionSettings(t *testing.T) {
	t.Run("empty_db_keeps_env_secret_unpersisted_and_mints_no_connection_revision", func(t *testing.T) {
		var envCPAHits atomic.Int32
		envCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			envCPAHits.Add(1)
			http.Error(w, "unexpected upstream write", http.StatusInternalServerError)
		}))
		t.Cleanup(envCPA.Close)

		cfg := testutil.NewConfig(t)
		cfg.CPAUpstreamURL = envCPA.URL
		cfg.ManagementKey = "env-only-management-secret"
		st := testutil.NewStore(t, cfg)
		collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
		t.Cleanup(func() { _ = collector.StopAndWait(context.Background()) })
		svc := New(cfg, st, collector)

		res, err := svc.Update(t.Context(), store.ManagerConfig{
			CodexInspection: store.ManagerCodexInspectionConfig{
				Enabled: BoolPtr(true),
				Workers: 7,
			},
			Retention: store.ManagerRetentionConfig{
				Enabled:       BoolPtr(true),
				RetentionDays: 14,
			},
		})
		if err != nil {
			t.Fatalf("Update(CodexInspection/Retention) under SourceEnv failed: %v", err)
		}
		if res.Source != string(SourceEnv) {
			t.Fatalf("Source = %q, want %q", res.Source, SourceEnv)
		}
		if res.Config.CodexInspection.Workers != 7 || res.Config.Retention.RetentionDays != 14 {
			t.Fatalf("unexpected Update response = %#v", res.Config)
		}
		if got := envCPAHits.Load(); got != 0 {
			t.Fatalf("SourceEnv Update hit CPA %d times, want 0", got)
		}

		if _, setupOK, err := st.LoadSetup(t.Context()); err != nil || setupOK {
			t.Fatalf("LoadSetup = (%t, %v), want setup unpersisted", setupOK, err)
		}
		if rev, found, err := st.Settings.LoadPersistedCPAConnectionRevision(t.Context()); err != nil || found || rev != "" {
			t.Fatalf("LoadPersistedCPAConnectionRevision = (%q, %t, %v), want empty/false/nil", rev, found, err)
		}
		savedCfg, ok, err := st.LoadManagerConfig(t.Context())
		if err != nil || !ok {
			t.Fatalf("LoadManagerConfig = (%t, %v), want ok", ok, err)
		}
		if savedCfg.CPAConnection != (store.ManagerCPAConnectionConfig{}) {
			t.Fatalf("saved CPAConnection = %#v, want zero value", savedCfg.CPAConnection)
		}
		if savedCfg.Collector != (store.ManagerCollectorConfig{}) {
			t.Fatalf("saved Collector = %#v, want zero value", savedCfg.Collector)
		}
		if savedCfg.CodexInspection.Workers != 7 || savedCfg.Retention.RetentionDays != 14 {
			t.Fatalf("saved non-connection settings = (%#v, %#v)", savedCfg.CodexInspection, savedCfg.Retention)
		}

		rows, err := st.SQLDB().QueryContext(t.Context(), `select key, value from settings`)
		if err != nil {
			t.Fatalf("query settings rows: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				t.Fatalf("scan setting row: %v", err)
			}
			if key == "setup" || key == "cpa_connection_revision_v1" {
				t.Fatalf("unexpected persisted setting key %q under empty-DB SourceEnv Update", key)
			}
			if strings.Contains(value, cfg.ManagementKey) || strings.Contains(value, envCPA.URL) || strings.Contains(value, "cpamp_") {
				t.Fatalf("setting %q persisted environment connection material: %s", key, value)
			}
		}
	})

	t.Run("preset_db_connection_and_collector_remain_intact_under_env_override", func(t *testing.T) {
		var envCPAHits atomic.Int32
		envCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			envCPAHits.Add(1)
			http.Error(w, "unexpected upstream write", http.StatusInternalServerError)
		}))
		t.Cleanup(envCPA.Close)

		cfg := testutil.NewConfig(t)
		st := testutil.NewStore(t, cfg)
		oldSeed := store.ManagerConfig{
			CPAConnection: store.ManagerCPAConnectionConfig{
				CPABaseURL:    "http://db-persisted.local:8317",
				ManagementKey: "db-persisted-secret-key",
			},
			Collector: store.ManagerCollectorConfig{
				Enabled:               BoolPtr(false),
				CollectorMode:         "http",
				Queue:                 "db-custom-queue",
				PopSide:               "left",
				BatchSize:             250,
				PollIntervalMS:        1500,
				QueryLimit:            12000,
				TLSSkipVerify:         true,
				TLSSkipVerifyExplicit: true,
			},
			Retention: store.ManagerRetentionConfig{
				Enabled:       BoolPtr(false),
				RetentionDays: 30,
			},
		}
		if err := st.SaveManagerConfigAndSetup(t.Context(), oldSeed, SetupFromManagerConfig(oldSeed)); err != nil {
			t.Fatalf("seed old DB connection: %v", err)
		}
		initialRev, revFound, err := st.Settings.LoadPersistedCPAConnectionRevision(t.Context())
		if err != nil || !revFound {
			t.Fatalf("LoadPersistedCPAConnectionRevision before env Update = (%q, %t, %v)", initialRev, revFound, err)
		}

		cfg.CPAUpstreamURL = envCPA.URL
		cfg.ManagementKey = "env-override-management-secret"
		collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
		t.Cleanup(func() { _ = collector.StopAndWait(context.Background()) })
		svc := New(cfg, st, collector)

		res, err := svc.Update(t.Context(), store.ManagerConfig{
			CodexInspection: store.ManagerCodexInspectionConfig{
				Enabled: BoolPtr(true),
				Workers: 9,
			},
		})
		if err != nil {
			t.Fatalf("Update(CodexInspection) with preset DB under SourceEnv failed: %v", err)
		}
		if res.Source != string(SourceEnv) || res.Config.CodexInspection.Workers != 9 {
			t.Fatalf("unexpected Update response = %#v", res)
		}
		if got := envCPAHits.Load(); got != 0 {
			t.Fatalf("SourceEnv Update hit CPA %d times, want 0", got)
		}

		savedCfg, ok, err := st.LoadManagerConfig(t.Context())
		if err != nil || !ok {
			t.Fatalf("LoadManagerConfig = (%t, %v), want ok", ok, err)
		}
		if savedCfg.CPAConnection != oldSeed.CPAConnection {
			t.Fatalf("saved CPAConnection = %#v, want %#v", savedCfg.CPAConnection, oldSeed.CPAConnection)
		}
		wantCollector := oldSeed.Collector
		wantCollector.Enabled = savedCfg.Collector.Enabled
		if savedCfg.Collector.Enabled == nil || *savedCfg.Collector.Enabled || savedCfg.Collector != wantCollector {
			t.Fatalf("saved Collector = %#v, want %#v", savedCfg.Collector, oldSeed.Collector)
		}
		if savedCfg.CodexInspection.Workers != 9 {
			t.Fatalf("saved CodexInspection.Workers = %d, want 9", savedCfg.CodexInspection.Workers)
		}
		savedSetup, setupOK, err := st.LoadSetup(t.Context())
		if err != nil || !setupOK {
			t.Fatalf("LoadSetup = (%t, %v), want ok", setupOK, err)
		}
		if savedSetup != SetupFromManagerConfig(oldSeed) {
			t.Fatalf("saved Setup = %#v, want %#v", savedSetup, SetupFromManagerConfig(oldSeed))
		}
		afterRev, afterFound, err := st.Settings.LoadPersistedCPAConnectionRevision(t.Context())
		if err != nil || !afterFound || afterRev != initialRev {
			t.Fatalf("LoadPersistedCPAConnectionRevision after env Update = (%q, %t, %v), want (%q, true, nil)", afterRev, afterFound, err, initialRev)
		}

		rows, err := st.SQLDB().QueryContext(t.Context(), `select key, value from settings`)
		if err != nil {
			t.Fatalf("query settings rows: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				t.Fatalf("scan setting row: %v", err)
			}
			if strings.Contains(value, cfg.ManagementKey) || strings.Contains(value, envCPA.URL) {
				t.Fatalf("setting %q leaked env connection: %s", key, value)
			}
		}
	})
}

func TestConcurrentStandaloneCollectorReconciliationMatchesPersistedState(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	cpaMock := testutil.NewCPAMock(t)
	collector := collectorservice.New(collectorpkg.NewManager(cfg, st))
	t.Cleanup(func() { _ = collector.StopAndWait(context.Background()) })
	svc := New(cfg, st, collector)

	seed := store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    cpaMock.URL(),
			ManagementKey: cpaMock.ManagementKey,
		},
		Collector: store.ManagerCollectorConfig{
			Enabled:        BoolPtr(true),
			CollectorMode:  "http",
			Queue:          "usage",
			PopSide:        "right",
			BatchSize:      100,
			PollIntervalMS: 500,
			QueryLimit:     50000,
		},
	}
	if err := st.SaveManagerConfigAndSetup(t.Context(), seed, SetupFromManagerConfig(seed)); err != nil {
		t.Fatalf("seed SaveManagerConfigAndSetup: %v", err)
	}

	for round := range 25 {
		startGate := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(4)
		go func() {
			defer wg.Done()
			<-startGate
			_, _ = svc.SetCollectorEnabled(t.Context(), true)
		}()
		go func() {
			defer wg.Done()
			<-startGate
			_, _ = svc.SetCollectorEnabled(t.Context(), false)
		}()
		go func(iter int) {
			defer wg.Done()
			<-startGate
			batch := 100 + iter
			_, _ = svc.PatchCollector(t.Context(), store.ManagerCollectorPatch{BatchSize: &batch})
		}(round)
		go func(iter int) {
			defer wg.Done()
			<-startGate
			enabled := iter%2 == 1
			_, _ = svc.Update(t.Context(), store.ManagerConfig{
				CPAConnection: store.ManagerCPAConnectionConfig{
					CPABaseURL:    cpaMock.URL(),
					ManagementKey: cpaMock.ManagementKey,
				},
				Collector: store.ManagerCollectorConfig{
					Enabled:        BoolPtr(enabled),
					CollectorMode:  "http",
					Queue:          "usage",
					PopSide:        "right",
					BatchSize:      120 + iter,
					PollIntervalMS: 500,
					QueryLimit:     50000,
				},
			})
		}(round)
		close(startGate)
		wg.Wait()

		resolved, _, _, err := svc.ResolveManagerConfigWithSource(t.Context())
		if err != nil {
			t.Fatalf("round %d ResolveManagerConfigWithSource: %v", round, err)
		}
		_, active := collector.ActiveRuntimeConfig()
		wantActive := ManagerCollectorEnabled(resolved)
		if active != wantActive {
			t.Fatalf("round %d collector active=%t, persisted enabled=%t", round, active, wantActive)
		}
	}
}
