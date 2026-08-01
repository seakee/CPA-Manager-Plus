package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	codexinspectionrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/codexinspection"
	codexinspectionservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/codexinspection"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	managerconfigservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type latestScheduledRunErrorRepository struct {
	codexinspectionrepo.Repository
	err error
}

func (r *latestScheduledRunErrorRepository) GetLatestRunByTriggerType(context.Context, string) (model.CodexInspectionRun, bool, error) {
	return model.CodexInspectionRun{}, false, r.err
}

func TestCodexInspectionWorkerDisabledConfigDoesNotRun(t *testing.T) {
	upstream := newWorkerInspectionServer(t, false)
	st := newWorkerInspectionStore(t, filepath.Join(t.TempDir(), "usage.sqlite"))
	cfg := newWorkerManagerConfig(upstream.URL, false)
	if err := st.SaveManagerConfig(context.Background(), cfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	service := newWorkerInspectionService(t, st)
	NewCodexInspectionWorker(st, service).tick(context.Background())
	time.Sleep(50 * time.Millisecond)
	runs, err := st.ListCodexInspectionRuns(context.Background(), 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("disabled runs=%#v err=%v", runs, err)
	}
}

func TestCodexInspectionWorkerRecoversExpiredRunWhileDisabled(t *testing.T) {
	upstream := newWorkerInspectionServer(t, false)
	st := newWorkerInspectionStore(t, filepath.Join(t.TempDir(), "usage.sqlite"))
	cfg := newWorkerManagerConfig(upstream.URL, false)
	if err := st.SaveManagerConfig(context.Background(), cfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	acquired, err := st.AcquireCodexInspectionRun(context.Background(), model.CodexInspectionRun{
		TriggerType:  model.CodexInspectionTriggerManual,
		TriggerKey:   "stale",
		Status:       model.CodexInspectionStatusRunning,
		Settings:     cfg.CodexInspection,
		SettingsJSON: model.MarshalCodexInspectionSettings(cfg.CodexInspection),
	}, "stale-owner", time.Millisecond)
	if err != nil {
		t.Fatalf("acquire stale run: %v", err)
	}
	stale := acquired.Run
	waitForWorkerInspectionLeaseExpiry(t, st)
	NewCodexInspectionWorker(st, newWorkerInspectionService(t, st)).tick(context.Background())
	run, found, err := st.GetCodexInspectionRun(context.Background(), stale.ID)
	if err != nil || !found || run.Status != model.CodexInspectionStatusInterrupted || run.FinishedAtMS == 0 {
		t.Fatalf("recovered stale run = %#v found=%v err=%v", run, found, err)
	}
	runs, err := st.ListCodexInspectionRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs after disabled recovery = %#v err=%v", runs, err)
	}
}

func waitForWorkerInspectionLeaseExpiry(t *testing.T, st *store.Store) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, active, err := st.GetActiveCodexInspectionLease(context.Background(), time.Now().UnixMilli())
		if err != nil {
			t.Fatalf("get active inspection lease: %v", err)
		}
		if !active {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("inspection lease did not expire")
}

func TestCodexInspectionWorkerSkipsActiveLease(t *testing.T) {
	upstream := newWorkerInspectionServer(t, false)
	st := newWorkerInspectionStore(t, filepath.Join(t.TempDir(), "usage.sqlite"))
	if err := st.SaveManagerConfig(context.Background(), newWorkerManagerConfig(upstream.URL, true)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	settings := model.DefaultCodexInspectionConfig()
	if _, err := st.AcquireCodexInspectionRun(context.Background(), model.CodexInspectionRun{
		TriggerType:  model.CodexInspectionTriggerManual,
		TriggerKey:   "active",
		Status:       model.CodexInspectionStatusRunning,
		Settings:     settings,
		SettingsJSON: model.MarshalCodexInspectionSettings(settings),
	}, "external-owner", time.Minute); err != nil {
		t.Fatalf("acquire active lease: %v", err)
	}
	NewCodexInspectionWorker(st, newWorkerInspectionService(t, st)).tick(context.Background())
	time.Sleep(50 * time.Millisecond)
	runs, err := st.ListCodexInspectionRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs while active=%#v err=%v", runs, err)
	}
}

func TestCodexInspectionWorkerDoesNotRunWhenScheduleHistoryReadFails(t *testing.T) {
	upstream := newWorkerInspectionServer(t, false)
	st := newWorkerInspectionStore(t, filepath.Join(t.TempDir(), "usage.sqlite"))
	if err := st.SaveManagerConfig(context.Background(), newWorkerManagerConfig(upstream.URL, true)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	st.CodexInspections = &latestScheduledRunErrorRepository{
		Repository: st.CodexInspections,
		err:        context.DeadlineExceeded,
	}
	NewCodexInspectionWorker(st, newWorkerInspectionService(t, st)).tick(context.Background())
	runs, err := st.ListCodexInspectionRuns(context.Background(), 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("runs after history read failure = %#v err=%v", runs, err)
	}
}

func TestCodexInspectionWorkersConcurrentTickCreateOneRun(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			once.Do(func() { close(started) })
			<-r.Context().Done()
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	firstStore := newWorkerInspectionStore(t, dbPath)
	secondStore := newWorkerInspectionStore(t, dbPath)
	if err := firstStore.SaveManagerConfig(context.Background(), newWorkerManagerConfig(upstream.URL, true)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	firstService := newWorkerInspectionService(t, firstStore)
	secondService := newWorkerInspectionService(t, secondStore)
	workers := []*CodexInspectionWorker{
		NewCodexInspectionWorker(firstStore, firstService),
		NewCodexInspectionWorker(secondStore, secondService),
	}
	var wg sync.WaitGroup
	for _, inspectionWorker := range workers {
		wg.Add(1)
		go func(inspectionWorker *CodexInspectionWorker) {
			defer wg.Done()
			inspectionWorker.tick(context.Background())
		}(inspectionWorker)
	}
	wg.Wait()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled inspection did not start")
	}
	time.Sleep(50 * time.Millisecond)
	runs, err := firstStore.ListCodexInspectionRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("concurrent runs=%#v err=%v", runs, err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := firstService.StopAndWait(shutdownCtx); err != nil {
		t.Fatalf("stop first service: %v", err)
	}
	if err := secondService.StopAndWait(shutdownCtx); err != nil {
		t.Fatalf("stop second service: %v", err)
	}
}

func TestCodexInspectionWorkerRunsNextIntervalAfterCompletion(t *testing.T) {
	upstream := newWorkerInspectionServer(t, false)
	st := newWorkerInspectionStore(t, filepath.Join(t.TempDir(), "usage.sqlite"))
	if err := st.SaveManagerConfig(context.Background(), newWorkerManagerConfig(upstream.URL, true)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	settings := model.DefaultCodexInspectionConfig()
	finishedAt := time.Now().Add(-2 * time.Minute).UnixMilli()
	if _, err := st.CreateCodexInspectionRun(context.Background(), model.CodexInspectionRun{
		TriggerType:  model.CodexInspectionTriggerScheduled,
		TriggerKey:   "interval:1:old",
		Status:       model.CodexInspectionStatusCompleted,
		StartedAtMS:  finishedAt - 1000,
		FinishedAtMS: finishedAt,
		Settings:     settings,
		SettingsJSON: model.MarshalCodexInspectionSettings(settings),
	}); err != nil {
		t.Fatalf("create previous run: %v", err)
	}
	service := newWorkerInspectionService(t, st)
	NewCodexInspectionWorker(st, service).tick(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := st.ListCodexInspectionRuns(context.Background(), 10)
		if err != nil {
			t.Fatalf("list runs: %v", err)
		}
		if len(runs) == 2 && runs[0].Status == model.CodexInspectionStatusCompleted {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	runs, _ := st.ListCodexInspectionRuns(context.Background(), 10)
	t.Fatalf("next interval runs = %#v", runs)
}

func TestCodexInspectionWorkerUsesLatestScheduledRunBeyondHistoryPage(t *testing.T) {
	upstream := newWorkerInspectionServer(t, false)
	st := newWorkerInspectionStore(t, filepath.Join(t.TempDir(), "usage.sqlite"))
	cfg := newWorkerManagerConfig(upstream.URL, true)
	cfg.CodexInspection.Schedule.IntervalMinutes = 60
	if err := st.SaveManagerConfig(context.Background(), cfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	settings := cfg.CodexInspection
	finishedAt := time.Now().Add(-time.Minute).UnixMilli()
	if _, err := st.CreateCodexInspectionRun(context.Background(), model.CodexInspectionRun{
		TriggerType:  model.CodexInspectionTriggerScheduled,
		TriggerKey:   "interval:60:previous",
		Status:       model.CodexInspectionStatusCompleted,
		StartedAtMS:  finishedAt - 1000,
		FinishedAtMS: finishedAt,
		Settings:     settings,
		SettingsJSON: model.MarshalCodexInspectionSettings(settings),
	}); err != nil {
		t.Fatalf("create scheduled run: %v", err)
	}
	for index := 0; index < 25; index++ {
		if _, err := st.CreateCodexInspectionRun(context.Background(), model.CodexInspectionRun{
			TriggerType:  model.CodexInspectionTriggerManual,
			TriggerKey:   "manual",
			Status:       model.CodexInspectionStatusCompleted,
			StartedAtMS:  finishedAt + int64(index+1),
			FinishedAtMS: finishedAt + int64(index+1),
			Settings:     settings,
			SettingsJSON: model.MarshalCodexInspectionSettings(settings),
		}); err != nil {
			t.Fatalf("create manual run %d: %v", index, err)
		}
	}
	NewCodexInspectionWorker(st, newWorkerInspectionService(t, st)).tick(context.Background())
	time.Sleep(50 * time.Millisecond)
	runs, err := st.ListCodexInspectionRuns(context.Background(), 100)
	if err != nil || len(runs) != 26 {
		t.Fatalf("runs after not-due tick = %d err=%v", len(runs), err)
	}
}

func newWorkerInspectionStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newWorkerInspectionService(t *testing.T, st *store.Store) *codexinspectionservice.Service {
	t.Helper()
	cfg := config.Config{
		DBPath:        filepath.Join(t.TempDir(), "usage.sqlite"),
		Queue:         "usage",
		PopSide:       "right",
		BatchSize:     100,
		QueryLimit:    50000,
		CORSOrigins:   []string{"*"},
		CollectorMode: "auto",
	}
	manager := collectorpkg.NewManager(cfg, st)
	collector := collectorservice.New(manager)
	managerConfig := managerconfigservice.New(cfg, st, collector)
	return codexinspectionservice.NewWithOptions(st, managerConfig, codexinspectionservice.ServiceOptions{
		LeaseDuration:     time.Second,
		HeartbeatInterval: 100 * time.Millisecond,
	}, &http.Client{})
}

func newWorkerManagerConfig(upstreamURL string, enabled bool) store.ManagerConfig {
	cfg := store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    upstreamURL,
			ManagementKey: "management-key",
		},
		Collector: store.ManagerCollectorConfig{
			CollectorMode:  "auto",
			Queue:          "usage",
			PopSide:        "right",
			BatchSize:      100,
			PollIntervalMS: 500,
			QueryLimit:     50000,
		},
		CodexInspection: model.DefaultCodexInspectionConfig(),
	}
	cfg.CodexInspection.Enabled = &enabled
	cfg.CodexInspection.Schedule.Mode = model.CodexInspectionScheduleModeInterval
	cfg.CodexInspection.Schedule.IntervalMinutes = 1
	cfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	cfg.CodexInspection.Workers = 1
	return cfg
}

func newWorkerInspectionServer(t *testing.T, block bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			if block {
				<-r.Context().Done()
				return
			}
			_, _ = w.Write([]byte(`{"files":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}
