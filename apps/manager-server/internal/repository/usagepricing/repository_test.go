package usagepricing_test

import (
	"context"
	"errors"
	"strings"
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

func TestPricingRollupDoesNotClassifyIncrementalBacklogAsRebuild(t *testing.T) {
	ctx := context.Background()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	if err := st.SaveModelPrices(ctx, map[string]store.ModelPrice{"resolved-model": {Prompt: 1}}); err != nil {
		t.Fatalf("save prices: %v", err)
	}
	if _, err := st.UsageEvents.InsertBatch(ctx, []usage.Event{pricingEvent("pricing-prime", 3_600_001, 10)}); err != nil {
		t.Fatalf("insert prime event: %v", err)
	}
	if _, err := st.CatchUpUsagePricing(ctx, 10, 10_000); err != nil {
		t.Fatalf("prime pricing revision: %v", err)
	}
	if _, err := st.UsageEvents.InsertBatch(ctx, []usage.Event{
		pricingEvent("pricing-incremental-1", 3_600_002, 20),
		pricingEvent("pricing-incremental-2", 3_600_003, 30),
	}); err != nil {
		t.Fatalf("insert incremental backlog: %v", err)
	}
	first, err := st.CatchUpUsagePricing(ctx, 1, 20_000)
	if err != nil {
		t.Fatalf("first incremental batch: %v", err)
	}
	if first.Rebuilt || !first.Pending || first.CoverageEventID != 2 {
		t.Fatalf("first incremental batch = %#v", first)
	}
	second, err := st.CatchUpUsagePricing(ctx, 1, 21_000)
	if err != nil {
		t.Fatalf("second incremental batch: %v", err)
	}
	if second.Rebuilt || second.Pending || second.CoverageEventID != 3 {
		t.Fatalf("second incremental batch = %#v", second)
	}
}

func TestPricingRollupPreservesRebuildingStatusAcrossFailure(t *testing.T) {
	ctx := context.Background()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	if err := st.SaveModelPrices(ctx, map[string]store.ModelPrice{"resolved-model": {Prompt: 1}}); err != nil {
		t.Fatalf("save prices: %v", err)
	}
	if _, err := st.UsageEvents.InsertBatch(ctx, []usage.Event{
		pricingEvent("pricing-rebuild-resume-1", 3_600_001, 10),
		pricingEvent("pricing-rebuild-resume-2", 3_600_002, 20),
	}); err != nil {
		t.Fatalf("insert rebuild events: %v", err)
	}

	first, err := st.CatchUpUsagePricing(ctx, 1, 10_000)
	if err != nil {
		t.Fatalf("run partial pricing rebuild: %v", err)
	}
	if !first.Rebuilt || !first.Pending || first.CoverageEventID != 1 {
		t.Fatalf("partial pricing rebuild = %#v", first)
	}
	if err := st.RecordUsagePricingFailure(ctx, errors.New("interrupted pricing rebuild"), 11_000); err != nil {
		t.Fatalf("record pricing rebuild failure: %v", err)
	}
	state, err := st.UsagePricingState(ctx)
	if err != nil {
		t.Fatalf("read interrupted pricing state: %v", err)
	}
	if state.Status != "rebuilding" || state.LastError != "interrupted pricing rebuild" {
		t.Fatalf("interrupted pricing state = %#v", state)
	}
	hourlyRows, _, available, err := st.UsagePricingHourlyRows(ctx, store.UsagePricingHourlyFilter{
		FromMS:        3_600_000,
		ToMS:          7_200_000,
		IncludeFailed: true,
	})
	if err != nil || !available || len(hourlyRows) != 1 || hourlyRows[0].Calls != 2 {
		t.Fatalf("interrupted pricing hourly rows = available:%v err:%v rows:%#v", available, err, hourlyRows)
	}
	accountRows, _, available, err := st.UsagePricingAccountRows(ctx, []string{pricingAccountKey("team-a.json", "auth-team-a")})
	if err != nil || !available || len(accountRows) != 1 || accountRows[0].Calls != 2 {
		t.Fatalf("interrupted pricing account rows = available:%v err:%v rows:%#v", available, err, accountRows)
	}

	completed, err := st.CatchUpUsagePricing(ctx, 10, 12_000)
	if err != nil {
		t.Fatalf("resume pricing rebuild: %v", err)
	}
	if !completed.Rebuilt || completed.Pending || completed.CoverageEventID != 2 {
		t.Fatalf("resumed pricing rebuild = %#v", completed)
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

func TestPricingAccountRollupSeparatesCodexMembersSharingWorkspace(t *testing.T) {
	ctx := context.Background()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)

	makeEvent := func(hash, member, file, authIndex string, inputTokens int64) usage.Event {
		event := pricingEvent(hash, 3_600_001+inputTokens, inputTokens)
		event.Provider = "codex"
		event.AccountSnapshot = member
		event.AuthFileSnapshot = file
		event.AuthIndex = authIndex
		event.AuthProviderSnapshot = "codex"
		event.AuthAccountIDSnapshot = "workspace-team"
		event.Source = file
		return event
	}
	if _, err := st.UsageEvents.InsertBatch(ctx, []usage.Event{
		makeEvent("codex-pricing-alice", "alice@example.com", "alice.json", "auth-alice", 100),
		makeEvent("codex-pricing-bob", "bob@example.com", "bob.json", "auth-bob", 200),
	}); err != nil {
		t.Fatalf("insert shared-workspace Codex pricing events: %v", err)
	}
	if _, err := st.CatchUpUsagePricing(ctx, 10, 10_000); err != nil {
		t.Fatalf("catch up shared-workspace Codex pricing events: %v", err)
	}

	accountKey := func(member string) string {
		key, valid := usageidentity.AccountKey(usageidentity.Fields{
			AuthFileSnapshot:      member + ".json",
			AuthIndex:             "auth-" + strings.Split(member, "@")[0],
			AuthProviderSnapshot:  "codex",
			AuthAccountIDSnapshot: "workspace-team",
			AccountSnapshot:       member,
		})
		if !valid {
			t.Fatalf("invalid Codex pricing identity for %s", member)
		}
		return key
	}
	aliceKey := accountKey("alice@example.com")
	bobKey := accountKey("bob@example.com")
	if aliceKey == bobKey {
		t.Fatalf("shared-workspace Codex members merged into %q", aliceKey)
	}

	rows, _, available, err := st.UsagePricingAccountRows(ctx, []string{aliceKey, bobKey})
	if err != nil {
		t.Fatalf("load shared-workspace Codex pricing rows: %v", err)
	}
	if !available || len(rows) != 2 {
		t.Fatalf("shared-workspace Codex pricing rows available=%v rows=%#v", available, rows)
	}
	byKey := make(map[string]store.UsagePricingAccountRow, len(rows))
	for _, row := range rows {
		byKey[row.AccountKey] = row
	}
	if row := byKey[aliceKey]; row.Calls != 1 || row.InputTokens != 100 || row.TotalTokens != 110 {
		t.Fatalf("Alice shared-workspace Codex pricing row = %#v", row)
	}
	if row := byKey[bobKey]; row.Calls != 1 || row.InputTokens != 200 || row.TotalTokens != 210 {
		t.Fatalf("Bob shared-workspace Codex pricing row = %#v", row)
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

// TestPricingRollupRevisionRollbackDoesNotDoubleCount covers a deterministic
// A-to-B-to-A rollback. Target-revision rows are cleared in bounded batches,
// readers use raw events while clearing, and the restored revision is rebuilt
// exactly once.
func TestPricingRollupRevisionRollbackDoesNotDoubleCount(t *testing.T) {
	ctx := context.Background()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	threshold := func(value int64) store.ModelPrice {
		return store.ModelPrice{
			Prompt:       1,
			ContextTiers: []store.ModelPriceContextTier{{ThresholdTokens: value, Prompt: 2, PromptConfigured: true}},
		}
	}
	accountKey := pricingAccountKey("team-a.json", "auth-team-a")
	hourlyFilter := store.UsagePricingHourlyFilter{FromMS: 3_600_000, ToMS: 7_200_000, IncludeFailed: true}
	assertSingleCall := func() {
		t.Helper()
		hourlyRows, _, available, err := st.UsagePricingHourlyRows(ctx, hourlyFilter)
		if err != nil || !available || len(hourlyRows) != 1 || hourlyRows[0].Calls != 1 {
			t.Fatalf("pricing hourly rows after rollback = available:%v err:%v %#v", available, err, hourlyRows)
		}
		accountRows, _, accountAvailable, err := st.UsagePricingAccountRows(ctx, []string{accountKey})
		if err != nil || !accountAvailable || len(accountRows) != 1 || accountRows[0].Calls != 1 {
			t.Fatalf("pricing account rows after rollback = available:%v err:%v %#v", accountAvailable, err, accountRows)
		}
	}

	// Build revision A (threshold 100).
	if err := st.SaveModelPrices(ctx, map[string]store.ModelPrice{"resolved-model": threshold(100)}); err != nil {
		t.Fatalf("save prices A: %v", err)
	}
	if _, err := st.UsageEvents.InsertBatch(ctx, []usage.Event{pricingEvent("rollback", 3_600_001, 150)}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := st.CatchUpUsagePricing(ctx, 10, 10_000); err != nil {
		t.Fatalf("initial catch up: %v", err)
	}
	assertSingleCall()
	// Switch to revision B.
	if err := st.SaveModelPrices(ctx, map[string]store.ModelPrice{"resolved-model": threshold(200)}); err != nil {
		t.Fatalf("save prices B: %v", err)
	}
	if _, err := st.CatchUpUsagePricing(ctx, 10, 20_000); err != nil {
		t.Fatalf("catch up threshold update: %v", err)
	}
	assertSingleCall()
	// Restore revision A. A limit of one clears only the hourly row in the first
	// transaction, leaving the account row for the resumed transaction.
	if err := st.SaveModelPrices(ctx, map[string]store.ModelPrice{"resolved-model": threshold(100)}); err != nil {
		t.Fatalf("rollback to prices A: %v", err)
	}
	result, err := st.CatchUpUsagePricing(ctx, 1, 30_000)
	if err != nil {
		t.Fatalf("start bounded rollback clearing: %v", err)
	}
	if !result.Pending || !result.Rebuilt || result.Processed != 0 || !result.ContinueSoon {
		t.Fatalf("first bounded rollback result = %#v", result)
	}
	state, err := st.UsagePricingState(ctx)
	if err != nil {
		t.Fatalf("read clearing state: %v", err)
	}
	if state.Status != "clearing" || state.CoverageEventID != 0 {
		t.Fatalf("bounded clearing state = %#v", state)
	}
	assertSingleCall()
	if err := st.RecordUsagePricingFailure(ctx, errors.New("interrupted clearing"), 31_000); err != nil {
		t.Fatalf("record clearing interruption: %v", err)
	}
	state, err = st.UsagePricingState(ctx)
	if err != nil {
		t.Fatalf("read interrupted clearing state: %v", err)
	}
	if state.Status != "clearing" || state.LastError != "interrupted clearing" {
		t.Fatalf("interrupted clearing state = %#v", state)
	}
	result, err = st.CatchUpUsagePricing(ctx, 1, 32_000)
	if err != nil {
		t.Fatalf("resume bounded rollback clearing: %v", err)
	}
	if result.Pending || result.Processed != 1 || result.ContinueSoon {
		t.Fatalf("resumed bounded rollback result = %#v", result)
	}
	assertSingleCall()
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
