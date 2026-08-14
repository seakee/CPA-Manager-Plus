package usagepricing_test

import (
	"context"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
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

	accountRows, _, available, err := st.UsagePricingAccountRows(ctx, []string{pricingAccountKey("team-a.json", "auth-team-a")})
	if err != nil {
		t.Fatalf("load account pricing rows: %v", err)
	}
	if !available || len(accountRows) != 3 {
		t.Fatalf("account pricing rows available=%v rows=%#v", available, accountRows)
	}
}

func TestPricingAccountRollupSeparatesSharedAccountByAuthIndex(t *testing.T) {
	ctx := context.Background()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	first := pricingEvent("shared-pricing-a", 3_600_001, 10)
	first.AccountSnapshot = "same@example.com"
	first.AuthFileSnapshot = "shared.json"
	first.AuthIndex = "auth-a"
	second := pricingEvent("shared-pricing-b", 3_600_002, 20)
	second.AccountSnapshot = "same@example.com"
	second.AuthFileSnapshot = "shared.json"
	second.AuthIndex = "auth-b"
	if _, err := st.UsageEvents.InsertBatch(ctx, []usage.Event{first, second}); err != nil {
		t.Fatalf("insert shared pricing events: %v", err)
	}
	if _, err := st.CatchUpUsagePricing(ctx, 10, 10_000); err != nil {
		t.Fatalf("catch up shared pricing events: %v", err)
	}

	firstKey := pricingAccountKey("shared.json", "auth-a")
	secondKey := pricingAccountKey("shared.json", "auth-b")
	rows, _, available, err := st.UsagePricingAccountRows(ctx, []string{secondKey, firstKey})
	if err != nil {
		t.Fatalf("load shared pricing rows: %v", err)
	}
	if !available || len(rows) != 2 {
		t.Fatalf("shared pricing rows available=%v rows=%#v", available, rows)
	}
	byKey := make(map[string]store.UsagePricingAccountRow, len(rows))
	for _, row := range rows {
		byKey[row.AccountKey] = row
	}
	if byKey[firstKey].Calls != 1 || byKey[firstKey].TotalTokens != 20 {
		t.Fatalf("first shared pricing row = %#v", byKey[firstKey])
	}
	if byKey[secondKey].Calls != 1 || byKey[secondKey].TotalTokens != 30 {
		t.Fatalf("second shared pricing row = %#v", byKey[secondKey])
	}
}

func TestPricingRollupExcludesInconsistentCanonicalBuckets(t *testing.T) {
	ctx := context.Background()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	event := pricingEvent("inconsistent", 3_600_001, 100)
	event.AccountingVersion = usage.TokenAccountingSchemaVersion
	event.AccountingValid = true
	event.TokenBreakdown = usage.TokenBreakdown{
		SchemaVersion: usage.TokenAccountingSchemaVersion,
		Quality:       usage.TokenAccountingQualityInconsistent,
		TotalTokens:   110,
		Input: usage.TokenInputBreakdown{
			TotalTokens:    100,
			UncachedTokens: 100,
		},
		Output: usage.TokenOutputBreakdown{
			TotalTokens:        10,
			NonReasoningTokens: 10,
		},
	}
	if !event.TokenBreakdown.Valid() {
		t.Fatalf("test breakdown is invalid: %#v", event.TokenBreakdown)
	}
	if _, err := st.UsageEvents.InsertBatch(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := st.CatchUpUsagePricing(ctx, 10, 10_000); err != nil {
		t.Fatalf("catch up pricing: %v", err)
	}

	rows, _, available, err := st.UsagePricingHourlyRows(ctx, store.UsagePricingHourlyFilter{
		FromMS:        3_600_000,
		ToMS:          7_200_000,
		IncludeFailed: true,
	})
	if err != nil || !available || len(rows) != 1 {
		t.Fatalf("pricing rows available=%v err=%v rows=%#v", available, err, rows)
	}
	row := rows[0]
	if row.InputTokens != 0 || row.OutputTokens != 0 || row.NonReasoningOutputTokens != 0 ||
		row.ReasoningTokens != 0 || row.CacheReadTokens != 0 || row.CacheCreationTokens != 0 ||
		row.UnclassifiedTokens != 110 || row.TotalTokens != 110 || row.IncompleteAccountingCalls != 1 {
		t.Fatalf("inconsistent pricing row = %#v", row)
	}
}

func TestPricingAccountRowsPreserveCanonicalStoredAndRawTailBuckets(t *testing.T) {
	ctx := context.Background()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)

	stored := pricingEvent("canonical-stored", 3_600_001, 999)
	stored.OutputTokens = 999
	stored.ReasoningTokens = 999
	stored.CachedTokens = 999
	stored.CacheReadTokens = 999
	stored.CacheCreationTokens = 999
	stored.TotalTokens = 1_998
	stored.AccountingVersion = usage.TokenAccountingSchemaVersion
	stored.AccountingValid = true
	stored.TokenBreakdown = usage.TokenBreakdown{
		SchemaVersion: usage.TokenAccountingSchemaVersion,
		Quality:       usage.TokenAccountingQualityComplete,
		TotalTokens:   100,
		Input: usage.TokenInputBreakdown{
			TotalTokens:     70,
			UncachedTokens:  50,
			CacheReadTokens: 20,
		},
		Output: usage.TokenOutputBreakdown{
			TotalTokens:        30,
			NonReasoningTokens: 23,
			ReasoningTokens:    7,
		},
	}
	if !stored.TokenBreakdown.Valid() {
		t.Fatalf("stored breakdown is invalid: %#v", stored.TokenBreakdown)
	}
	if _, err := st.UsageEvents.InsertBatch(ctx, []usage.Event{stored}); err != nil {
		t.Fatalf("insert stored event: %v", err)
	}
	if result, err := st.CatchUpUsagePricing(ctx, 1, 10_000); err != nil || result.Processed != 1 || result.Pending {
		t.Fatalf("catch up stored event: result=%#v err=%v", result, err)
	}

	rawTail := pricingEvent("canonical-raw-tail", 3_600_002, 999)
	rawTail.OutputTokens = 999
	rawTail.ReasoningTokens = 999
	rawTail.CachedTokens = 999
	rawTail.CacheReadTokens = 999
	rawTail.CacheCreationTokens = 999
	rawTail.TotalTokens = 1_998
	rawTail.AccountingVersion = usage.TokenAccountingSchemaVersion
	rawTail.AccountingValid = true
	rawTail.TokenBreakdown = usage.TokenBreakdown{
		SchemaVersion:      usage.TokenAccountingSchemaVersion,
		Quality:            usage.TokenAccountingQualityInconsistent,
		TotalTokens:        110,
		UnclassifiedTokens: 110,
	}
	if !rawTail.TokenBreakdown.Valid() {
		t.Fatalf("raw-tail breakdown is invalid: %#v", rawTail.TokenBreakdown)
	}
	if _, err := st.UsageEvents.InsertBatch(ctx, []usage.Event{rawTail}); err != nil {
		t.Fatalf("insert raw-tail event: %v", err)
	}

	rows, _, available, err := st.UsagePricingAccountRows(ctx, []string{pricingAccountKey("team-a.json", "auth-team-a")})
	if err != nil || !available || len(rows) != 1 {
		t.Fatalf("load canonical account rows: available=%v err=%v rows=%#v", available, err, rows)
	}
	row := rows[0]
	if row.Calls != 2 || row.SuccessCalls != 2 || row.FailureCalls != 0 ||
		row.InputTokens != 70 || row.OutputTokens != 30 || row.NonReasoningOutputTokens != 23 ||
		row.ReasoningTokens != 7 || row.UnclassifiedTokens != 110 || row.IncompleteAccountingCalls != 1 ||
		row.CachedTokens != 0 || row.CacheReadTokens != 20 || row.CacheCreationTokens != 0 ||
		row.TotalTokens != 210 {
		t.Fatalf("canonical account row = %#v", row)
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

func TestPricingRollupUsesResolvedThenAnalyticsThenRawModelPrices(t *testing.T) {
	ctx := context.Background()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	if err := st.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"resolved-model":               {Prompt: 1},
		"deepseek-v4-flash":            {Prompt: 2},
		"deepseek-v4-flash(max)":       {Prompt: 3},
		"deepseek-v4-flash(region-us)": {Prompt: 4},
	}); err != nil {
		t.Fatalf("save prices: %v", err)
	}
	events := []usage.Event{
		pricingIdentityEvent("resolved-priority", 3_600_001, "deepseek-v4-flash(max)", "resolved-model"),
		pricingIdentityEvent("analytics-priority", 3_600_002, "deepseek-v4-flash(max)", ""),
		pricingIdentityEvent("raw-fallback", 3_600_003, "deepseek-v4-flash(region-us)", ""),
	}
	if _, err := st.UsageEvents.InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if _, err := st.CatchUpUsagePricing(ctx, 10, 10_000); err != nil {
		t.Fatalf("catch up pricing: %v", err)
	}
	rows, _, available, err := st.UsagePricingHourlyRows(ctx, store.UsagePricingHourlyFilter{
		FromMS:        3_600_000,
		ToMS:          7_200_000,
		IncludeFailed: true,
	})
	if err != nil || !available {
		t.Fatalf("load pricing rows: available=%v err=%v", available, err)
	}
	byPricingModel := make(map[string]store.UsagePricingHourlyRow, len(rows))
	for _, row := range rows {
		byPricingModel[row.PricingModel] = row
	}
	if row := byPricingModel["resolved-model"]; row.Model != "deepseek-v4-flash" || row.BillingModel != "resolved-model" || row.Calls != 1 {
		t.Fatalf("resolved-priority row = %#v", row)
	}
	if row := byPricingModel["deepseek-v4-flash"]; row.Model != "deepseek-v4-flash" || row.BillingModel != "deepseek-v4-flash" || row.Calls != 1 {
		t.Fatalf("analytics-priority row = %#v", row)
	}
	if row := byPricingModel["deepseek-v4-flash(region-us)"]; row.Model != "deepseek-v4-flash(region-us)" || row.Calls != 1 {
		t.Fatalf("raw-fallback row = %#v", row)
	}
	filtered, _, available, err := st.UsagePricingHourlyRows(ctx, store.UsagePricingHourlyFilter{
		FromMS:        3_600_000,
		ToMS:          7_200_000,
		Models:        []string{"deepseek-v4-flash"},
		IncludeFailed: true,
	})
	if err != nil || !available || len(filtered) != 2 {
		t.Fatalf("canonical filtered rows available=%v err=%v rows=%#v", available, err, filtered)
	}
	suffixFiltered, _, available, err := st.UsagePricingHourlyRows(ctx, store.UsagePricingHourlyFilter{
		FromMS:        3_600_000,
		ToMS:          7_200_000,
		Models:        []string{"deepseek-v4-flash(max)"},
		IncludeFailed: true,
	})
	if err != nil || !available || len(suffixFiltered) != 2 {
		t.Fatalf("suffix filtered rows available=%v err=%v rows=%#v", available, err, suffixFiltered)
	}
}

func TestPricingAccountRollupSeparatesAnalyticsModelsSharingBillingModel(t *testing.T) {
	ctx := context.Background()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	if err := st.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"resolved-x": {Prompt: 1},
	}); err != nil {
		t.Fatalf("save prices: %v", err)
	}
	first := pricingIdentityEvent("pricing-shared-billing-a", 3_600_001, "model-a", "resolved-x")
	second := pricingIdentityEvent("pricing-shared-billing-b", 3_600_002, "model-b", "resolved-x")
	second.InputTokens = 20
	second.TotalTokens = 30
	if _, err := st.UsageEvents.InsertBatch(ctx, []usage.Event{first, second}); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if _, err := st.CatchUpUsagePricing(ctx, 10, 10_000); err != nil {
		t.Fatalf("catch up pricing: %v", err)
	}
	rows, _, available, err := st.UsagePricingAccountRows(ctx, []string{pricingAccountKey("team-a.json", "auth-team-a")})
	if err != nil || !available {
		t.Fatalf("load account rows: available=%v err=%v", available, err)
	}
	if len(rows) != 2 {
		t.Fatalf("account rows = %#v, want one row per analytics model", rows)
	}
	byModel := make(map[string]store.UsagePricingAccountRow, len(rows))
	for _, row := range rows {
		byModel[row.Model] = row
	}
	if row := byModel["model-a"]; row.BillingModel != "resolved-x" || row.PricingModel != "resolved-x" || row.Calls != 1 || row.TotalTokens != 20 {
		t.Fatalf("model-a row = %#v", row)
	}
	if row := byModel["model-b"]; row.BillingModel != "resolved-x" || row.PricingModel != "resolved-x" || row.Calls != 1 || row.TotalTokens != 30 {
		t.Fatalf("model-b row = %#v", row)
	}
}

func pricingEvent(hash string, timestampMS int64, inputTokens int64) usage.Event {
	return usage.Event{
		EventHash:            hash,
		TimestampMS:          timestampMS,
		Timestamp:            "1970-01-01T01:00:00Z",
		Provider:             "openai",
		Model:                "display-model",
		ResolvedModel:        "resolved-model",
		AccountSnapshot:      "team-a",
		AuthFileSnapshot:     "team-a.json",
		AuthProviderSnapshot: "openai",
		AuthIndex:            "auth-team-a",
		InputTokens:          inputTokens,
		OutputTokens:         10,
		TotalTokens:          inputTokens + 10,
		CreatedAtMS:          timestampMS,
	}
}

func pricingIdentityEvent(hash string, timestampMS int64, requestedModel, resolvedModel string) usage.Event {
	event := pricingEvent(hash, timestampMS, 10)
	event.Model = requestedModel
	event.ResolvedModel = resolvedModel
	return event
}

func pricingAccountKey(authFileSnapshot, authIndex string) string {
	key, valid := usageidentity.AccountKey(usageidentity.Fields{
		AuthFileSnapshot:     authFileSnapshot,
		AuthIndex:            authIndex,
		AuthProviderSnapshot: "openai",
	})
	if !valid {
		panic("invalid pricing test identity")
	}
	return key
}
