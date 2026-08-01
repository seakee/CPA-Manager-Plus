package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageaggregate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

type blockingUsageAggregateRepository struct {
	usageaggregate.Repository
	afterRead chan struct{}
	resume    <-chan struct{}
}

func (r *blockingUsageAggregateRepository) LoadRowsTx(ctx context.Context, tx *sql.Tx, filter usageaggregate.Filter) ([]usageaggregate.Row, usageaggregate.State, bool, error) {
	rows, state, available, err := r.Repository.LoadRowsTx(ctx, tx, filter)
	close(r.afterRead)
	select {
	case <-r.resume:
	case <-ctx.Done():
		return nil, usageaggregate.State{}, false, ctx.Err()
	}
	return rows, state, available, err
}

func TestLoadUsageHourlyPricingSnapshotIsConsistentDuringConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	db, err := Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	fromMS := int64(1_800_000_000_000)
	toMS := fromMS + 2*int64(time.Hour/time.Millisecond)
	if err := db.SaveModelPrices(ctx, map[string]ModelPrice{
		"model-a": {Prompt: 1, PromptConfigured: true},
	}); err != nil {
		t.Fatalf("save initial prices: %v", err)
	}
	if _, err := db.InsertEvents(ctx, []usage.Event{snapshotTestEvent("snapshot-first", fromMS+1_000, 100_000)}); err != nil {
		t.Fatalf("insert initial event: %v", err)
	}
	catchUpUsagePricingSnapshot(t, ctx, db)

	aggregateFilter := UsageHourlyAggregateFilter{FromMS: fromMS, ToMS: toMS, IncludeFailed: true}
	pricingFilter := UsagePricingHourlyFilter{FromMS: fromMS, ToMS: toMS, IncludeFailed: true}
	originalAggregateRepository := db.UsageAggregates
	afterAggregateRead := make(chan struct{})
	resumeSnapshot := make(chan struct{})
	db.UsageAggregates = &blockingUsageAggregateRepository{
		Repository: originalAggregateRepository,
		afterRead:  afterAggregateRead,
		resume:     resumeSnapshot,
	}

	type snapshotResult struct {
		snapshot UsageHourlyPricingSnapshot
		err      error
	}
	snapshotDone := make(chan snapshotResult, 1)
	go func() {
		snapshot, snapshotErr := db.LoadUsageHourlyPricingSnapshot(ctx, aggregateFilter, pricingFilter)
		snapshotDone <- snapshotResult{snapshot: snapshot, err: snapshotErr}
	}()
	<-afterAggregateRead

	writerDone := make(chan error, 1)
	go func() {
		if saveErr := db.SaveModelPrices(ctx, map[string]ModelPrice{
			"model-a": {
				Prompt: 2, PromptConfigured: true,
				ContextTiers: []ModelPriceContextTier{{
					ThresholdTokens:  200_000,
					Prompt:           4,
					PromptConfigured: true,
				}},
			},
		}); saveErr != nil {
			writerDone <- saveErr
			return
		}
		_, insertErr := db.InsertEvents(ctx, []usage.Event{snapshotTestEvent("snapshot-second", fromMS+2_000, 300_000)})
		writerDone <- insertErr
	}()

	var writerErr error
	writerCompleted := false
	select {
	case writerErr = <-writerDone:
		writerCompleted = true
	case <-time.After(250 * time.Millisecond):
	}
	close(resumeSnapshot)
	result := <-snapshotDone
	if result.err != nil {
		t.Fatalf("load concurrent snapshot: %v", result.err)
	}
	if !writerCompleted {
		writerErr = <-writerDone
	}
	if writerErr != nil {
		t.Fatalf("concurrent writer: %v", writerErr)
	}
	db.UsageAggregates = originalAggregateRepository

	if calls := aggregateSnapshotCalls(result.snapshot.AggregateRows); calls != 1 {
		t.Fatalf("aggregate snapshot calls = %d, want 1", calls)
	}
	if calls := pricingSnapshotCalls(result.snapshot.PricingRows); calls != 1 {
		t.Fatalf("pricing snapshot calls = %d, want 1", calls)
	}
	price := result.snapshot.Prices["model-a"]
	if price.Prompt != 1 || len(price.ContextTiers) != 0 {
		t.Fatalf("snapshot price = %#v, want initial price", price)
	}

	latest, err := db.LoadUsageHourlyPricingSnapshot(ctx, aggregateFilter, pricingFilter)
	if err != nil {
		t.Fatalf("load latest snapshot: %v", err)
	}
	if calls := aggregateSnapshotCalls(latest.AggregateRows); calls != 2 {
		t.Fatalf("latest aggregate calls = %d, want 2", calls)
	}
	if calls := pricingSnapshotCalls(latest.PricingRows); calls != 2 {
		t.Fatalf("latest pricing calls = %d, want 2", calls)
	}
	latestPrice := latest.Prices["model-a"]
	if latestPrice.Prompt != 2 || len(latestPrice.ContextTiers) != 1 || latestPrice.ContextTiers[0].ThresholdTokens != 200_000 {
		t.Fatalf("latest price = %#v, want structural update", latestPrice)
	}
	foundLongBand := false
	for _, row := range latest.PricingRows {
		if row.ContextThresholdTokens == 200_000 {
			foundLongBand = true
		}
	}
	if !foundLongBand {
		t.Fatalf("latest pricing rows did not use updated structure: %#v", latest.PricingRows)
	}
}

func TestWithModelPriceSnapshotBlocksMutationsUntilReadCompletes(t *testing.T) {
	ctx := context.Background()
	db, err := Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.SaveModelPrices(ctx, map[string]ModelPrice{"model-a": {Prompt: 1}}); err != nil {
		t.Fatalf("save initial price: %v", err)
	}

	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	readDone := make(chan error, 1)
	go func() {
		readDone <- db.WithModelPriceSnapshot(func() error {
			close(readStarted)
			<-releaseRead
			return nil
		})
	}()
	<-readStarted

	writerStarted := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		close(writerStarted)
		writerDone <- db.SaveModelPrices(ctx, map[string]ModelPrice{"model-a": {Prompt: 2}})
	}()
	<-writerStarted
	select {
	case err := <-writerDone:
		t.Fatalf("price mutation completed during read snapshot: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseRead)
	if err := <-readDone; err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if err := <-writerDone; err != nil {
		t.Fatalf("price mutation after read snapshot: %v", err)
	}
	prices, err := db.LoadModelPrices(ctx)
	if err != nil {
		t.Fatalf("load updated price: %v", err)
	}
	if prices["model-a"].Prompt != 2 {
		t.Fatalf("updated price = %#v", prices["model-a"])
	}
}

func catchUpUsagePricingSnapshot(t *testing.T, ctx context.Context, db *Store) {
	t.Helper()
	for {
		result, err := db.CatchUpUsageHourlyAggregate(ctx, 100, time.Now().UnixMilli())
		if err != nil {
			t.Fatalf("catch up hourly aggregate: %v", err)
		}
		if !result.Pending {
			break
		}
	}
	for {
		result, err := db.CatchUpUsagePricing(ctx, 100, time.Now().UnixMilli())
		if err != nil {
			t.Fatalf("catch up pricing aggregate: %v", err)
		}
		if !result.Pending {
			break
		}
	}
}

func snapshotTestEvent(hash string, timestampMS, inputTokens int64) usage.Event {
	return usage.Event{
		EventHash:     hash,
		TimestampMS:   timestampMS,
		Timestamp:     time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Model:         "model-a",
		Endpoint:      "POST /v1/chat/completions",
		Method:        "POST",
		Path:          "/v1/chat/completions",
		InputTokens:   inputTokens,
		TotalTokens:   inputTokens,
		ResolvedModel: "model-a",
	}
}

func aggregateSnapshotCalls(rows []UsageHourlyAggregateRow) int64 {
	var calls int64
	for _, row := range rows {
		calls += row.Calls
	}
	return calls
}

func pricingSnapshotCalls(rows []UsagePricingHourlyRow) int64 {
	var calls int64
	for _, row := range rows {
		calls += row.Calls
	}
	return calls
}
