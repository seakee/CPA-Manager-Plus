package managerconfig

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
