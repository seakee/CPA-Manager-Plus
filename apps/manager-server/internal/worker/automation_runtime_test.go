package worker

import (
	"context"
	"testing"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	automationsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/automation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestAutomationUsageHandlerGatesNewEvents(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	settings := automationsvc.New(config.Config{}, st)
	quota := &recordingQuotaAutomationWorker{}
	account := &recordingAccountAutomationWorker{}
	handler := NewAutomationRuntime(settings, quota, account).handler

	handler.HandleUsageEvents(ctx, collectorpkg.RuntimeConfig{}, []usage.Event{{EventHash: "evt-off"}})
	if quota.handleCount != 0 || account.handleCount != 0 {
		t.Fatalf("disabled handlers were called quota=%d account=%d", quota.handleCount, account.handleCount)
	}

	if _, err := settings.Update(ctx, automationsvc.UpdateRequest{QuotaCooldownEnabled: boolPtr(true)}); err != nil {
		t.Fatalf("enable quota: %v", err)
	}
	handler.HandleUsageEvents(ctx, collectorpkg.RuntimeConfig{}, []usage.Event{{EventHash: "evt-quota"}})
	if quota.handleCount != 1 || account.handleCount != 0 {
		t.Fatalf("quota-only counts quota=%d account=%d", quota.handleCount, account.handleCount)
	}

	if _, err := settings.Update(ctx, automationsvc.UpdateRequest{AccountActionsEnabled: boolPtr(true), AccountActionsAutoDisable: boolPtr(true)}); err != nil {
		t.Fatalf("enable account actions: %v", err)
	}
	handler.HandleUsageEvents(ctx, collectorpkg.RuntimeConfig{}, []usage.Event{{EventHash: "evt-both"}})
	if quota.handleCount != 2 || account.handleCount != 1 || !account.autoDisable {
		t.Fatalf("enabled counts quota=%d account=%d auto=%t", quota.handleCount, account.handleCount, account.autoDisable)
	}
}

func TestAutomationRuntimeReloadUpdatesAutoDisable(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	settings := automationsvc.New(config.Config{}, st)
	account := &recordingAccountAutomationWorker{}
	runtime := NewAutomationRuntime(settings, &recordingQuotaAutomationWorker{}, account)

	if _, err := settings.Update(ctx, automationsvc.UpdateRequest{AccountActionsEnabled: boolPtr(true), AccountActionsAutoDisable: boolPtr(true)}); err != nil {
		t.Fatalf("enable auto-disable: %v", err)
	}
	if err := runtime.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !account.autoDisable {
		t.Fatalf("autoDisable not enabled after reload")
	}
}

func TestAutomationRuntimeStartPreservesCollectorUsageEventFanout(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	settings := automationsvc.New(config.Config{}, st)
	if _, err := settings.Update(ctx, automationsvc.UpdateRequest{
		QuotaCooldownEnabled:      boolPtr(true),
		AccountActionsEnabled:     boolPtr(true),
		AccountActionsAutoDisable: boolPtr(true),
	}); err != nil {
		t.Fatalf("enable automation: %v", err)
	}
	quota := &recordingQuotaAutomationWorker{}
	account := &recordingAccountAutomationWorker{}
	runtime := NewAutomationRuntime(settings, quota, account)

	var projectionEvents int
	fanout := NewUsageEventFanout(
		runtime.UsageEventHandler(),
		collectorpkg.UsageEventHandlerFunc(func(_ context.Context, _ collectorpkg.RuntimeConfig, events []usage.Event) {
			projectionEvents += len(events)
		}),
	)
	manager := collectorpkg.NewManager(config.Config{}, st)
	manager.SetUsageEventHandler(fanout)

	runtime.Start(ctx)
	if quota.startCount != 1 || account.startCount != 1 {
		t.Fatalf("worker start counts quota=%d account=%d, want 1/1", quota.startCount, account.startCount)
	}
	fanout.HandleUsageEvents(ctx, collectorpkg.RuntimeConfig{}, []usage.Event{{EventHash: "evt-fanout"}})
	if quota.handleCount != 1 || account.handleCount != 1 || projectionEvents != 1 {
		t.Fatalf("fanout counts quota=%d account=%d projection=%d, want 1/1/1", quota.handleCount, account.handleCount, projectionEvents)
	}
}

type recordingQuotaAutomationWorker struct {
	startCount   int
	handleCount  int
	runtimeCount int
}

func (w *recordingQuotaAutomationWorker) Start(context.Context) {
	w.startCount++
}

func (w *recordingQuotaAutomationWorker) HandleUsageEvents(context.Context, collectorpkg.RuntimeConfig, []usage.Event) {
	w.handleCount++
}

func (w *recordingQuotaAutomationWorker) UpdateRuntimeConfig(context.Context, collectorpkg.RuntimeConfig) {
	w.runtimeCount++
}

type recordingAccountAutomationWorker struct {
	startCount  int
	handleCount int
	autoDisable bool
}

func (w *recordingAccountAutomationWorker) Start(context.Context) {
	w.startCount++
}

func (w *recordingAccountAutomationWorker) SetAutoDisable(enabled bool) {
	w.autoDisable = enabled
}

func (w *recordingAccountAutomationWorker) HandleUsageEvents(context.Context, collectorpkg.RuntimeConfig, []usage.Event) {
	w.handleCount++
}

func boolPtr(value bool) *bool {
	return &value
}
