package usagepricing_test

import (
	"context"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestPricingRollupBandsStrictThresholdsAndMergesRawDelta(t *testing.T) {
	ctx := context.Background()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	if err := st.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"resolved-model": {
			Prompt: 1,
			ContextTiers: []store.ModelPriceContextTier{
				{ThresholdTokens: 100, Prompt: 2, PromptConfigured: true},
				{ThresholdTokens: 200, Prompt: 3, PromptConfigured: true},
			},
		},
	}); err != nil {
		t.Fatalf("save prices: %v", err)
	}
	events := []usage.Event{
		pricingEvent("base", 3_600_001, 100),
		pricingEvent("tier-one", 3_600_002, 101),
		pricingEvent("tier-two", 3_600_003, 201),
	}
	if _, err := st.UsageEvents.InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	result, err := st.CatchUpUsagePricing(ctx, 2, 10_000)
	if err != nil {
		t.Fatalf("catch up pricing: %v", err)
	}
	if result.Processed != 2 || !result.Pending || !result.Rebuilt {
		t.Fatalf("catch-up result = %#v", result)
	}
	rows, state, available, err := st.UsagePricingHourlyRows(ctx, store.UsagePricingHourlyFilter{
		FromMS:        3_600_000,
		ToMS:          7_200_000,
		IncludeFailed: true,
	})
	if err != nil {
		t.Fatalf("load pricing rows: %v", err)
	}
	if !available || state.StructureRevision == "" || len(rows) != 3 {
		t.Fatalf("pricing rows available=%v state=%#v rows=%#v", available, state, rows)
	}
	byThreshold := map[int64]store.UsagePricingHourlyRow{}
	for _, row := range rows {
		byThreshold[row.ContextThresholdTokens] = row
	}
	if byThreshold[model.ModelPriceBaseContextThreshold].Calls != 1 ||
		byThreshold[100].Calls != 1 || byThreshold[200].Calls != 1 {
		t.Fatalf("threshold rows = %#v", byThreshold)
	}
	if byThreshold[100].PricingModel != "resolved-model" || byThreshold[100].InputTokens != 101 {
		t.Fatalf("tier-one row = %#v", byThreshold[100])
	}

	accountRows, _, available, err := st.UsagePricingAccountRows(ctx, []string{"team-a"})
	if err != nil {
		t.Fatalf("load account pricing rows: %v", err)
	}
	if !available || len(accountRows) != 3 {
		t.Fatalf("account pricing rows available=%v rows=%#v", available, accountRows)
	}
}

func TestPricingRollupRateUpdatesKeepRevisionAndThresholdUpdatesRebuild(t *testing.T) {
	ctx := context.Background()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	price := store.ModelPrice{
		Prompt:       1,
		ContextTiers: []store.ModelPriceContextTier{{ThresholdTokens: 100, Prompt: 2, PromptConfigured: true}},
		ServiceTiers: []store.ModelPriceServiceTier{{
			Mode: "fast", ServiceTier: "priority", Prompt: 3, PromptConfigured: true,
		}},
	}
	if err := st.SaveModelPrices(ctx, map[string]store.ModelPrice{"resolved-model": price}); err != nil {
		t.Fatalf("save prices: %v", err)
	}
	if _, err := st.UsageEvents.InsertBatch(ctx, []usage.Event{pricingEvent("event", 3_600_001, 150)}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	first, err := st.CatchUpUsagePricing(ctx, 10, 10_000)
	if err != nil {
		t.Fatalf("initial catch up: %v", err)
	}
	if !first.Rebuilt {
		t.Fatalf("initial catch up did not initialize revision: %#v", first)
	}
	initialState, err := st.UsagePricingState(ctx)
	if err != nil {
		t.Fatalf("initial state: %v", err)
	}

	price.ContextTiers[0].Prompt = 9
	price.ServiceTiers[0].Prompt = 11
	if err := st.SaveModelPrices(ctx, map[string]store.ModelPrice{"resolved-model": price}); err != nil {
		t.Fatalf("save rate update: %v", err)
	}
	rateResult, err := st.CatchUpUsagePricing(ctx, 10, 20_000)
	if err != nil {
		t.Fatalf("catch up rate update: %v", err)
	}
	if rateResult.Rebuilt {
		t.Fatalf("rate-only update rebuilt rollup: %#v", rateResult)
	}
	rateState, err := st.UsagePricingState(ctx)
	if err != nil {
		t.Fatalf("rate state: %v", err)
	}
	if rateState.StructureRevision != initialState.StructureRevision {
		t.Fatalf("rate revision = %q, want %q", rateState.StructureRevision, initialState.StructureRevision)
	}

	price.ContextTiers[0].ThresholdTokens = 200
	if err := st.SaveModelPrices(ctx, map[string]store.ModelPrice{"resolved-model": price}); err != nil {
		t.Fatalf("save threshold update: %v", err)
	}
	thresholdResult, err := st.CatchUpUsagePricing(ctx, 10, 30_000)
	if err != nil {
		t.Fatalf("catch up threshold update: %v", err)
	}
	if !thresholdResult.Rebuilt || thresholdResult.Processed != 1 {
		t.Fatalf("threshold update result = %#v", thresholdResult)
	}
	rows, _, available, err := st.UsagePricingHourlyRows(ctx, store.UsagePricingHourlyFilter{
		FromMS:        3_600_000,
		ToMS:          7_200_000,
		IncludeFailed: true,
	})
	if err != nil || !available || len(rows) != 1 {
		t.Fatalf("rebuilt rows available=%v err=%v rows=%#v", available, err, rows)
	}
	if rows[0].ContextThresholdTokens != model.ModelPriceBaseContextThreshold {
		t.Fatalf("rebuilt threshold = %d", rows[0].ContextThresholdTokens)
	}
}

func pricingEvent(hash string, timestampMS int64, inputTokens int64) usage.Event {
	return usage.Event{
		EventHash:       hash,
		TimestampMS:     timestampMS,
		Timestamp:       "1970-01-01T01:00:00Z",
		Model:           "display-model",
		ResolvedModel:   "resolved-model",
		AccountSnapshot: "team-a",
		InputTokens:     inputTokens,
		OutputTokens:    10,
		TotalTokens:     inputTokens + 10,
		CreatedAtMS:     timestampMS,
	}
}
