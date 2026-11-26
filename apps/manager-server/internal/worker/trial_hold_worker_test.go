package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	codexinspectioncontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/codexinspection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	usagearchive "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagearchive"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	usageservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestMutatingWorkersDeferUnderTrialHoldAndResumeAfterRelease(t *testing.T) {
	t.Cleanup(func() {
		middleware.SetProcessTrialHoldGate(nil)
		middleware.ReleaseProcessTrialHold()
	})

	t.Run("UsageArchiveRetentionWorker skips runOnce under trial hold and requests retry", func(t *testing.T) {
		middleware.EngageProcessTrialHold()
		defer middleware.ReleaseProcessTrialHold()

		service := &fakeUsageArchiveRetentionService{}
		retentionWorker := NewUsageArchiveRetentionWorker(service, 30)
		retrySoon := retentionWorker.runOnce(context.Background())
		if !retrySoon {
			t.Fatal("expected runOnce under trial hold to request retry interval")
		}
		if len(service.calls) != 0 {
			t.Fatalf("expected zero retention mutations under trial hold, got %v", service.calls)
		}

		middleware.ReleaseProcessTrialHold()
		retentionWorker.now = func() time.Time { return time.UnixMilli(4_000_000_000) }
		retentionWorker.runOnce(context.Background())
		if len(service.calls) == 0 {
			t.Fatal("expected retention worker to execute after trial hold release")
		}
	})

	t.Run("AccountActionCandidateWorker and RateLimitAutoDisableWorker defer queued jobs under trial hold", func(t *testing.T) {
		middleware.EngageProcessTrialHold()
		defer middleware.ReleaseProcessTrialHold()

		var cpaHits atomic.Int32
		cpaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			cpaHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"files":[]}`)
		}))
		t.Cleanup(cpaSrv.Close)

		cfg := testutil.NewConfig(t)
		st := testutil.NewStore(t, cfg)
		rtCfg := collectorpkg.RuntimeConfig{
			CPAUpstreamURL: cpaSrv.URL,
			ManagementKey:  "mgmt-key",
		}

		accountWorker := NewAccountActionCandidateWorker(st)
		rateWorker := NewRateLimitAutoDisableWorker(st, rtCfg)
		rateWorker.enableCheckInterval = 25 * time.Millisecond

		ctx, cancel := context.WithCancel(context.Background())
		accountWorker.Start(ctx)
		rateWorker.Start(ctx)
		t.Cleanup(func() {
			cancel()
			accountWorker.Wait()
			rateWorker.Wait()
		})

		accountWorker.HandleUsageEvents(ctx, rtCfg, []usage.Event{
			{
				Failed:                true,
				FailStatusCode:        401,
				EventHash:             "evt-trial-hold-401",
				RequestID:             "req-trial-hold-401",
				Provider:              "codex",
				AuthFileSnapshot:      "codex-expired.json",
				AuthIndex:             "1",
				AccountSnapshot:       "user@example.com",
				AuthAccountIDSnapshot: "acct-123",
				FailSummary:           "authentication_error: invalidated OAuth token",
				FailBody:              `{"error":{"type":"authentication_error","code":"token_revoked"}}`,
			},
		})
		rateWorker.HandleUsageEvents(ctx, rtCfg, []usage.Event{
			{
				Failed:                true,
				FailStatusCode:        429,
				EventHash:             "evt-trial-hold-429",
				RequestID:             "req-trial-hold-429",
				Provider:              "codex",
				AuthFileSnapshot:      "codex-quota.json",
				AuthIndex:             "2",
				AccountSnapshot:       "user2@example.com",
				AuthAccountIDSnapshot: "acct-456",
				FailBody:              `{"error":{"type":"usage_limit_reached","resets_in_seconds":120}}`,
			},
		})

		time.Sleep(220 * time.Millisecond)

		items, err := st.ListAccountActionCandidates(ctx, model.AccountActionStatusPending, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 0 {
			t.Fatalf("expected 0 persisted account action candidates under trial hold, got %d", len(items))
		}
		if got := cpaHits.Load(); got != 0 {
			t.Fatalf("expected 0 CPA auth-file calls from RateLimitAutoDisableWorker under trial hold, got %d", got)
		}

		// Release trial hold; both workers process their deferred work.
		middleware.ReleaseProcessTrialHold()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			items, err = st.ListAccountActionCandidates(ctx, model.AccountActionStatusPending, 10)
			if err == nil && len(items) == 1 && cpaHits.Load() >= 1 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if len(items) != 1 || items[0].AuthFileName != "codex-expired.json" {
			t.Fatalf("expected deferred candidate to persist after release, got %+v", items)
		}
		if cpaHits.Load() < 1 {
			t.Fatal("expected RateLimitAutoDisableWorker to resume processing after trial hold release")
		}
	})
}

type trialHoldBlockingRetentionService struct {
	entered   chan struct{}
	allowExit chan struct{}
	finished  atomic.Bool
}

func (b *trialHoldBlockingRetentionService) ActiveArchiveRun(context.Context) (store.UsageArchiveRun, bool, error) {
	close(b.entered)
	<-b.allowExit
	b.finished.Store(true)
	return store.UsageArchiveRun{}, false, usagearchive.ErrNoEvents
}

func (b *trialHoldBlockingRetentionService) CreateRetentionArchive(context.Context, int64) (usageservice.ArchiveStatus, error) {
	return usageservice.ArchiveStatus{}, usagearchive.ErrNoEvents
}

func (b *trialHoldBlockingRetentionService) ResumeArchive(context.Context, string) (usageservice.ArchiveStatus, error) {
	return usageservice.ArchiveStatus{}, nil
}

func (b *trialHoldBlockingRetentionService) VerifyArchive(context.Context, string) (usageservice.ArchiveStatus, error) {
	return usageservice.ArchiveStatus{}, nil
}

func (b *trialHoldBlockingRetentionService) DeleteArchive(context.Context, string) (usageservice.ArchiveStatus, error) {
	return usageservice.ArchiveStatus{}, nil
}

func TestWaitOtherInflightMutationsDrainsRunningBackgroundWorkers(t *testing.T) {
	resetTrialHold := func() {
		middleware.SetProcessTrialHoldGate(nil)
		middleware.ReleaseProcessTrialHold()
	}
	t.Cleanup(resetTrialHold)

	t.Run("UsageArchiveRetentionWorker mid-runOnce blocks WaitOtherInflightMutations until finished", func(t *testing.T) {
		resetTrialHold()
		gate := middleware.NewTrialHoldGate(false, "")
		middleware.SetProcessTrialHoldGate(gate)

		svc := &trialHoldBlockingRetentionService{
			entered:   make(chan struct{}),
			allowExit: make(chan struct{}),
		}
		retentionWorker := NewUsageArchiveRetentionWorker(svc, 30)
		retentionWorker.now = func() time.Time { return time.UnixMilli(4_000_000_000) }

		workerDone := make(chan struct{})
		go func() {
			defer close(workerDone)
			retentionWorker.runOnce(context.Background())
		}()
		<-svc.entered

		middleware.EngageProcessTrialHold()
		drainDone := make(chan error, 1)
		go func() {
			drainDone <- middleware.WaitOtherInflightMutations(context.Background())
		}()

		select {
		case err := <-drainDone:
			t.Fatalf("WaitOtherInflightMutations returned before UsageArchiveRetentionWorker finished: %v", err)
		case <-time.After(40 * time.Millisecond):
		}

		close(svc.allowExit)
		<-workerDone

		select {
		case err := <-drainDone:
			if err != nil {
				t.Fatalf("WaitOtherInflightMutations returned error: %v", err)
			}
			if !svc.finished.Load() {
				t.Fatal("expected worker run to have completed before WaitOtherInflightMutations unblocked")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for WaitOtherInflightMutations after retention worker finished")
		}
	})

	t.Run("RateLimitAutoDisableWorker mid-execution blocks WaitOtherInflightMutations until finished", func(t *testing.T) {
		resetTrialHold()
		gate := middleware.NewTrialHoldGate(false, "")
		middleware.SetProcessTrialHoldGate(gate)

		var enterOnce sync.Once
		enteredCPA := make(chan struct{})
		allowCPAExit := make(chan struct{})
		var workerCompleted atomic.Bool

		cpaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			enterOnce.Do(func() { close(enteredCPA) })
			<-allowCPAExit
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"files":[]}`)
		}))
		t.Cleanup(cpaSrv.Close)

		cfg := testutil.NewConfig(t)
		st := testutil.NewStore(t, cfg)
		rtCfg := collectorpkg.RuntimeConfig{
			CPAUpstreamURL: cpaSrv.URL,
			ManagementKey:  "mgmt-key",
		}
		rateWorker := NewRateLimitAutoDisableWorker(st, rtCfg)
		workerDone := make(chan struct{})
		go func() {
			defer close(workerDone)
			rateWorker.handleCandidate(context.Background(), quotaAutoDisableCandidate{
				BaseURL:         cpaSrv.URL,
				ManagementKey:   "mgmt-key",
				FileName:        "codex-inflight.json",
				AuthIndex:       "1",
				AccountSnapshot: "inflight@example.com",
				AccountID:       "acct-inflight",
				Provider:        "codex",
				ReasonCode:      quotaReasonCodexUsageLimit,
				WindowKind:      quotaWindowRolling24H,
				ResetAt:         time.Now().Add(5 * time.Minute),
				EventHash:       "evt-inflight",
			})
			workerCompleted.Store(true)
		}()
		<-enteredCPA

		middleware.EngageProcessTrialHold()
		drainDone := make(chan error, 1)
		go func() {
			drainDone <- middleware.WaitOtherInflightMutations(context.Background())
		}()

		select {
		case err := <-drainDone:
			t.Fatalf("WaitOtherInflightMutations returned while RateLimitAutoDisableWorker was mid-execution: %v", err)
		case <-time.After(40 * time.Millisecond):
		}

		close(allowCPAExit)
		<-workerDone
		select {
		case err := <-drainDone:
			if err != nil || !workerCompleted.Load() {
				t.Fatalf("WaitOtherInflightMutations err=%v workerCompleted=%v", err, workerCompleted.Load())
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for RateLimitAutoDisableWorker drain")
		}
	})

	t.Run("CodexInspectionWorker scheduled run blocks WaitOtherInflightMutations until run finishes", func(t *testing.T) {
		resetTrialHold()
		gate := middleware.NewTrialHoldGate(false, "")
		middleware.SetProcessTrialHoldGate(gate)

		var enterOnce sync.Once
		enteredInspect := make(chan struct{})
		allowInspectExit := make(chan struct{})
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v0/management/auth-files" {
				enterOnce.Do(func() { close(enteredInspect) })
				<-allowInspectExit
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"files":[]}`)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(upstream.Close)

		st := newWorkerInspectionStore(t, filepath.Join(t.TempDir(), "usage.sqlite"))
		if err := st.SaveManagerConfig(context.Background(), newWorkerManagerConfig(upstream.URL, true)); err != nil {
			t.Fatalf("save manager config: %v", err)
		}
		service := newWorkerInspectionService(t, st)
		inspectionWorker := NewCodexInspectionWorker(st, service)
		t.Cleanup(func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = inspectionWorker.StopAndWait(stopCtx)
		})

		inspectionWorker.processNext(context.Background())
		<-enteredInspect

		middleware.EngageProcessTrialHold()
		drainDone := make(chan error, 1)
		go func() {
			drainDone <- middleware.WaitOtherInflightMutations(context.Background())
		}()

		select {
		case err := <-drainDone:
			t.Fatalf("WaitOtherInflightMutations returned while CodexInspectionWorker scheduled run was active: %v", err)
		case <-time.After(40 * time.Millisecond):
		}

		close(allowInspectExit)
		select {
		case err := <-drainDone:
			if err != nil {
				t.Fatalf("WaitOtherInflightMutations returned error: %v", err)
			}
			if _, active := service.ActiveRunID(); active {
				t.Fatal("expected CodexInspectionService active run to have finished before drain completed")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for CodexInspectionWorker scheduled run drain")
		}
	})
}

func TestCodexInspectionHandlerMapsTrialHoldActiveTo503UpgradeTrialVerifying(t *testing.T) {
	t.Cleanup(func() {
		middleware.SetProcessTrialHoldGate(nil)
	})
	gate := middleware.NewTrialHoldGate(true, "")
	middleware.SetProcessTrialHoldGate(gate)

	st := newWorkerInspectionStore(t, filepath.Join(t.TempDir(), "usage.sqlite"))
	testutil.EnsureAdminCredential(t, st)
	if err := st.SaveManagerConfig(context.Background(), newWorkerManagerConfig("http://127.0.0.1:18317", true)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	service := newWorkerInspectionService(t, st)
	handler := &codexinspectioncontroller.Handler{
		App: &app.Context{
			AdminAuthService:       adminauthsvc.New(config.Config{}, st),
			CodexInspectionService: service,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/codex-inspection/run", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
	req.Header.Set("Content-Type", "application/json")
	handler.Handle(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable from direct handler call under TrialHold, got %d (%s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("expected Retry-After=5, got %q", rec.Header().Get("Retry-After"))
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 503 body: %v", err)
	}
	if body["code"] != middleware.TrialHoldErrorCode {
		t.Fatalf("expected code=%q, got %#v", middleware.TrialHoldErrorCode, body)
	}
}
