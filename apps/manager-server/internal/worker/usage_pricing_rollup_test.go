package worker

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestUsagePricingRollupWorkerCatchUp(t *testing.T) {
	db := newUsagePricingRollupWorkerStore(t)
	ctx := context.Background()
	timestampMS := int64(1_800_000_001_000)
	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-a": {
			Prompt: 1,
			ContextTiers: []store.ModelPriceContextTier{
				{ThresholdTokens: 100, Prompt: 2, PromptConfigured: true},
			},
		},
	}); err != nil {
		t.Fatalf("save model prices: %v", err)
	}
	if _, err := db.InsertEvents(ctx, []usage.Event{usagePricingRollupWorkerEvent(
		"usage-pricing-worker-event",
		timestampMS,
		150,
	)}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	worker := NewUsagePricingRollupWorker(db)
	worker.batchLimit = 10
	worker.maxBatches = 4
	if pending := worker.catchUp(ctx); pending {
		t.Fatal("completed catch-up reported pending work")
	}

	rows, state, available, err := db.UsagePricingHourlyRows(ctx, store.UsagePricingHourlyFilter{
		FromMS:        timestampMS - timestampMS%hourWindowMS,
		ToMS:          timestampMS - timestampMS%hourWindowMS + hourWindowMS,
		IncludeFailed: true,
	})
	if err != nil {
		t.Fatalf("query pricing rollup: %v", err)
	}
	if !available || state.Status != "ready" || state.CoverageEventID != 1 {
		t.Fatalf("pricing state = available:%v state:%#v", available, state)
	}
	if len(rows) != 1 || rows[0].Calls != 1 || rows[0].InputTokens != 150 || rows[0].ContextThresholdTokens != 100 {
		t.Fatalf("pricing rows = %#v", rows)
	}
	for _, rollupName := range []string{"projection_v1", "metadata_v1", "stats_v1"} {
		monitoringState, err := db.UsageMonitoringState(ctx, rollupName)
		if err != nil {
			t.Fatalf("monitoring state %s: %v", rollupName, err)
		}
		if monitoringState.Status != "ready" || monitoringState.CoverageEventID != 1 {
			t.Fatalf("monitoring state %s = %#v", rollupName, monitoringState)
		}
	}
}

func TestUsagePricingRollupWorkerLogsOnlyRebuildResults(t *testing.T) {
	logs := captureWorkerLogs(t)
	worker := NewUsagePricingRollupWorker(nil)
	worker.logTaskProgress(usageDerivedPricingTask, usageDerivedCatchUpResult{
		Processed:       1,
		CoverageEventID: 1,
		TargetEventID:   1,
	})
	if output := logs.String(); strings.Contains(output, "rebuild") {
		t.Fatalf("incremental result emitted rebuild logs:\n%s", output)
	}

	logs.Reset()
	worker.logTaskProgress(usageDerivedPricingTask, usageDerivedCatchUpResult{
		Processed:       1,
		CoverageEventID: 1,
		TargetEventID:   2,
		Pending:         true,
		Rebuilt:         true,
	})
	worker.logTaskProgress(usageDerivedPricingTask, usageDerivedCatchUpResult{
		Processed:       1,
		CoverageEventID: 2,
		TargetEventID:   2,
		Rebuilt:         true,
	})
	output := logs.String()
	for _, fragment := range []string{
		"pricing rebuild started",
		"pricing rebuild progress",
		"pricing rebuild completed",
	} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("rebuild logs missing %q:\n%s", fragment, output)
		}
	}
}

func TestUsagePricingRollupWorkerContinuesPendingBacklog(t *testing.T) {
	db := newUsagePricingRollupWorkerStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	baseMS := int64(1_800_000_000_000)
	events := make([]usage.Event, 0, 5)
	for index := 0; index < 5; index++ {
		events = append(events, usagePricingRollupWorkerEvent(
			fmt.Sprintf("usage-pricing-worker-backlog-%d", index),
			baseMS+int64(index)*1000,
			int64(index+1),
		))
	}
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	worker := NewUsagePricingRollupWorker(db)
	worker.batchLimit = 1
	worker.maxBatches = 1
	worker.checkInterval = time.Hour
	worker.continuationDelay = time.Millisecond
	worker.Start(ctx)

	deadline := time.Now().Add(10 * time.Second)
	for {
		state, err := db.UsagePricingState(ctx)
		if err != nil {
			t.Fatalf("pricing state: %v", err)
		}
		if state.CoverageEventID == 5 && state.Status == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("backlog did not continue: state=%#v", state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUsagePricingRollupWorkerContinuesRevisionClearing(t *testing.T) {
	db := newUsagePricingRollupWorkerStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	threshold := func(value int64) store.ModelPrice {
		return store.ModelPrice{
			Prompt: 1,
			ContextTiers: []store.ModelPriceContextTier{
				{ThresholdTokens: value, Prompt: 2, PromptConfigured: true},
			},
		}
	}
	pricesA := map[string]store.ModelPrice{"gpt-a": threshold(100)}
	pricesB := map[string]store.ModelPrice{"gpt-a": threshold(200)}
	if err := db.SaveModelPrices(ctx, pricesA); err != nil {
		t.Fatalf("save prices A: %v", err)
	}
	event := usagePricingRollupWorkerEvent("usage-pricing-worker-clearing", 1_800_000_001_000, 150)
	event.AccountSnapshot = "team-a"
	event.AuthFileSnapshot = "team-a.json"
	event.AuthProviderSnapshot = "openai"
	event.AuthIndex = "auth-team-a"
	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := db.CatchUpUsagePricing(ctx, 10, 10_000); err != nil {
		t.Fatalf("build pricing revision A: %v", err)
	}
	revisionA, err := db.UsagePricingState(ctx)
	if err != nil {
		t.Fatalf("read pricing revision A: %v", err)
	}
	if err := db.SaveModelPrices(ctx, pricesB); err != nil {
		t.Fatalf("save prices B: %v", err)
	}
	if _, err := db.CatchUpUsagePricing(ctx, 10, 20_000); err != nil {
		t.Fatalf("build pricing revision B: %v", err)
	}
	if err := db.SaveModelPrices(ctx, pricesA); err != nil {
		t.Fatalf("restore prices A: %v", err)
	}

	worker := NewUsagePricingRollupWorker(db)
	worker.batchLimit = 1
	worker.maxBatches = usageDerivedTaskCount
	worker.checkInterval = time.Hour
	worker.continuationDelay = time.Millisecond
	worker.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for {
		state, err := db.UsagePricingState(ctx)
		if err != nil {
			t.Fatalf("pricing state: %v", err)
		}
		if state.Status == "ready" && state.StructureRevision == revisionA.StructureRevision {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("revision clearing did not continue: state=%#v want_revision=%q", state, revisionA.StructureRevision)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUsagePricingRollupWorkerRecordsFailure(t *testing.T) {
	sqlDB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db := store.New(sqlDB)
	ctx := context.Background()
	baseMS := int64(1_800_000_000_000)
	if _, err := db.InsertEvents(ctx, []usage.Event{
		usagePricingRollupWorkerEvent("usage-pricing-worker-failure", baseMS, 1),
	}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `drop table usage_pricing_hourly_rollups_v1`); err != nil {
		t.Fatalf("drop pricing rollup fixture: %v", err)
	}

	worker := NewUsagePricingRollupWorker(db)
	worker.catchUp(ctx)
	state, err := db.UsagePricingState(ctx)
	if err != nil {
		t.Fatalf("pricing state: %v", err)
	}
	if state.Status != "failed" || state.LastError == "" {
		t.Fatalf("failure state = %#v", state)
	}
	if ctx.Err() != nil {
		t.Fatalf("worker failure unexpectedly canceled context: %v", ctx.Err())
	}
}

func newUsagePricingRollupWorkerStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func usagePricingRollupWorkerEvent(hash string, timestampMS, inputTokens int64) usage.Event {
	return usage.Event{
		EventHash:     hash,
		TimestampMS:   timestampMS,
		Timestamp:     time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Model:         "gpt-a",
		ResolvedModel: "gpt-a",
		Endpoint:      "POST /v1/chat/completions",
		Method:        "POST",
		Path:          "/v1/chat/completions",
		InputTokens:   inputTokens,
		OutputTokens:  5,
		TotalTokens:   inputTokens + 5,
		CreatedAtMS:   timestampMS,
	}
}
