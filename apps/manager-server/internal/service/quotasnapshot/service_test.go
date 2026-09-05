package quotasnapshot

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/codexquota"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	quotasnapshotrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func newQuotaSnapshotTestService(t *testing.T, nowMS int64) *Service {
	service, _ := newQuotaSnapshotTestServiceWithPath(t, nowMS)
	return service
}

func newQuotaSnapshotTestServiceWithPath(t *testing.T, nowMS int64) (*Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	service := New(st)
	service.now = func() time.Time { return time.UnixMilli(nowMS) }
	return service, path
}

func quotaSnapshotTestAccount() AccountTarget {
	return AccountTarget{
		AuthFileSnapshot:     "codex.json",
		AuthProviderSnapshot: "codex",
		AuthIndex:            "auth-1",
		AccountSnapshot:      "user@example.com",
	}
}

func writeQuotaLifecycleSnapshotForKey(
	t *testing.T,
	service *Service,
	accountKey, observationID string,
	observedAtMS, cycleStartMS, cycleEndMS, durationSeconds int64,
	usedPercent float64,
	planType string,
) {
	t.Helper()
	entry := WriteEntry{
		Provider: "codex",
		Observation: &ObservationInput{
			Source:              "api_query",
			SourceObservationID: observationID,
			ObservedAtMS:        observedAtMS,
			InventoryScopeKey:   "codex:rate-limits",
			InventoryMode:       "partial",
		},
		Windows: []WindowInput{{
			ProviderWindowID:    "weekly",
			WindowKind:          "weekly",
			WindowMode:          "fixed",
			ModelScopeKind:      "family",
			ModelScopeKey:       "codex_main",
			Source:              "api_query",
			SourceObservationID: observationID,
			ObservedAtMS:        observedAtMS,
			BoundaryAccuracy:    "exact",
			CycleStartMS:        &cycleStartMS,
			CycleEndMS:          &cycleEndMS,
			DurationSeconds:     &durationSeconds,
			UsedPercent:         &usedPercent,
			PlanType:            planType,
		}},
	}
	observation, err := normalizeObservationInput(accountKey, "codex", entry, service.now().UnixMilli())
	if err != nil {
		t.Fatalf("normalize quota observation: %v", err)
	}
	snapshot, err := normalizeWindowInput(accountKey, "codex", entry.Windows[0], service.now().UnixMilli())
	if err != nil {
		t.Fatalf("normalize quota snapshot: %v", err)
	}
	snapshot.InventoryScopeKey = observation.InventoryScopeKey
	write := model.AccountQuotaObservationWrite{
		Observation: observation,
		Snapshots:   []model.AccountQuotaSnapshot{snapshot},
	}
	write.Observation.WindowCount = len(write.Snapshots)
	write.Snapshots[0].ContentHash = snapshotContentHash(write.Snapshots[0])
	write.Observation.ObservationHash = observationHash(write)
	if err := service.store.QuotaSnapshots.InsertObservationWrites(context.Background(), []model.AccountQuotaObservationWrite{write}); err != nil {
		t.Fatalf("write quota lifecycle snapshot: %v", err)
	}
}

func TestWriteQuerySelectsLatestCompleteObservationAndMergesCodexResetCredits(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 20_000)
	cycleStart := int64(10_000)
	cycleEnd := int64(30_000)
	duration := int64(20)
	apiUsed := 20.0
	headerUsed := 35.0
	available := int64(2)

	_, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
		RowKey: "row-1", Provider: "codex", Account: quotaSnapshotTestAccount(),
		Windows: []WindowInput{{
			ProviderWindowID: "rate_limit:five_hour", WindowKind: "five_hour",
			WindowMode: "fixed", ModelScopeKind: "all", Source: "api_query",
			ObservedAtMS: 15_000, BoundaryAccuracy: "exact",
			CycleStartMS: &cycleStart, CycleEndMS: &cycleEnd, DurationSeconds: &duration,
			UsedPercent: &apiUsed, ResetCreditsAvailable: &available,
			ResetCredits: []ResetCredit{{ID: "credit-1", ExpiresAtMS: 100_000}},
		}},
	}}})
	if err != nil {
		t.Fatalf("write api snapshot: %v", err)
	}
	_, err = service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
		RowKey: "row-1", Provider: "codex", Account: quotaSnapshotTestAccount(),
		Windows: []WindowInput{{
			ProviderWindowID: "rate_limit:five_hour", WindowKind: "five_hour",
			WindowMode: "fixed", ModelScopeKind: "all", Source: "response_header",
			ObservedAtMS: 19_000, BoundaryAccuracy: "derived",
			CycleStartMS: &cycleStart, CycleEndMS: &cycleEnd, DurationSeconds: &duration,
			UsedPercent: &headerUsed,
		}},
	}}})
	if err != nil {
		t.Fatalf("write header snapshot: %v", err)
	}

	result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-1", Provider: "codex", Account: quotaSnapshotTestAccount(),
	}}})
	if err != nil {
		t.Fatalf("query snapshots: %v", err)
	}
	if len(result.Items) != 1 || len(result.Items[0].Windows) != 1 {
		t.Fatalf("query result = %#v", result)
	}
	window := result.Items[0].Windows[0]
	if window.Source != "response_header" || window.UsedPercent == nil || *window.UsedPercent != headerUsed {
		t.Fatalf("selected window = %#v", window)
	}
	if window.ResetCreditsAvailable == nil || *window.ResetCreditsAvailable != available || len(window.ResetCredits) != 1 {
		t.Fatalf("reset credits were not merged: %#v", window)
	}
	if got := window.FieldSources["reset_credits"].Source; got != "api_query" {
		t.Fatalf("reset credit source = %q, want api_query", got)
	}
}

func TestCodexQuotaSnapshotsStayMemberScopedAndIgnoreLegacyWorkspaceKey(t *testing.T) {
	service, path := newQuotaSnapshotTestServiceWithPath(t, 50_000)
	ctx := context.Background()
	cycleStart := int64(100)
	cycleEnd := int64(18_100)
	duration := int64(18)
	window := func(used float64, observedAtMS int64) WindowInput {
		return WindowInput{
			ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "fixed",
			ModelScopeKind: "all", Source: "api_query", SourceObservationID: fmt.Sprintf("observation-%d", observedAtMS),
			ObservedAtMS: observedAtMS, BoundaryAccuracy: "exact", UsedPercent: &used,
			CycleStartMS: &cycleStart, CycleEndMS: &cycleEnd, DurationSeconds: &duration,
		}
	}
	account := func(member string) AccountTarget {
		return AccountTarget{
			AuthFileSnapshot:      member + ".json",
			AuthProviderSnapshot:  "codex",
			AuthAccountIDSnapshot: "workspace-1",
			AuthIndex:             "auth-" + member,
			AccountSnapshot:       member + "@example.com",
			Source:                member + ".json",
		}
	}
	for _, entry := range []WriteEntry{
		{RowKey: "alice", Provider: "codex", Account: account("alice"), Observation: &ObservationInput{
			Source: "api_query", SourceObservationID: "observation-1000", ObservedAtMS: 1_000,
			InventoryScopeKey: "codex:rate-limits", InventoryMode: "partial",
		}, Windows: []WindowInput{window(10, 1_000)}},
		{RowKey: "bob", Provider: "codex", Account: account("bob"), Observation: &ObservationInput{
			Source: "api_query", SourceObservationID: "observation-2000", ObservedAtMS: 2_000,
			InventoryScopeKey: "codex:rate-limits", InventoryMode: "partial",
		}, Windows: []WindowInput{window(20, 2_000)}},
	} {
		if _, err := service.Write(ctx, WriteRequest{Entries: []WriteEntry{entry}}); err != nil {
			t.Fatalf("write %s snapshot: %v", entry.RowKey, err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open quota database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// This is the v3 workspace-only key emitted before Codex member identity
	// was introduced. It is intentionally left orphaned rather than fanned out.
	legacyWorkspaceKey := "usage-account-history:3:codex-account:636F646578:776F726B73706163652D31"
	if _, err := db.Exec(`insert into account_quota_snapshots (
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, source, source_observation_id, observed_at_ms,
		boundary_accuracy, used_percent, created_at_ms
	) values (?, 'codex', 'five-hour', 'five_hour', 'fixed', 'all',
		'api_query', 'legacy-workspace', 3_000, 'exact', 99, 3_000)`, legacyWorkspaceKey); err != nil {
		t.Fatalf("insert legacy workspace snapshot: %v", err)
	}

	alice := account("alice")
	bob := account("bob")
	aliceKey, aliceOK := usageidentity.AccountKey(alice.identityFields("codex"))
	bobKey, bobOK := usageidentity.AccountKey(bob.identityFields("codex"))
	if !aliceOK || !bobOK || aliceKey == bobKey {
		t.Fatalf("member account keys = alice:%q/%v bob:%q/%v", aliceKey, aliceOK, bobKey, bobOK)
	}
	for _, test := range []struct {
		name    string
		rowKey  string
		account AccountTarget
		want    float64
	}{
		{name: "Alice", rowKey: "alice", account: alice, want: 10},
		{name: "Bob", rowKey: "bob", account: bob, want: 20},
	} {
		result, err := service.Query(ctx, QueryRequest{Accounts: []QueryAccount{{
			RowKey: test.rowKey, Provider: "codex", Account: test.account,
		}}})
		if err != nil {
			t.Fatalf("query %s snapshot: %v", test.name, err)
		}
		if len(result.Items) != 1 || result.Items[0].AccountKey == legacyWorkspaceKey || len(result.Items[0].Windows) != 1 {
			t.Fatalf("%s query included legacy/fan-out data: %#v", test.name, result)
		}
		if result.Items[0].Windows[0].UsedPercent == nil || *result.Items[0].Windows[0].UsedPercent != test.want {
			t.Fatalf("%s used percent = %#v, want %v", test.name, result.Items[0].Windows[0], test.want)
		}
	}
	var aliceRows, bobRows, legacyRows int
	if err := db.QueryRow(`select count(*) from account_quota_snapshots where account_key = ?`, aliceKey).Scan(&aliceRows); err != nil {
		t.Fatalf("count Alice snapshots: %v", err)
	}
	if err := db.QueryRow(`select count(*) from account_quota_snapshots where account_key = ?`, bobKey).Scan(&bobRows); err != nil {
		t.Fatalf("count Bob snapshots: %v", err)
	}
	if err := db.QueryRow(`select count(*) from account_quota_snapshots where account_key = ?`, legacyWorkspaceKey).Scan(&legacyRows); err != nil {
		t.Fatalf("count legacy snapshots: %v", err)
	}
	if aliceRows != 1 || bobRows != 1 || legacyRows != 1 {
		t.Fatalf("quota snapshot rows were migrated/fanned out: Alice=%d Bob=%d legacy=%d", aliceRows, bobRows, legacyRows)
	}
}

func TestQueryBridgesLegacyCodexPreviousCycleOnlyForConsistentPersonalPlan(t *testing.T) {
	for _, test := range []struct {
		name              string
		previousPlan      string
		legacyCurrentPlan string
		memberCurrentPlan string
		mixedMemberPlan   string
		memberStartOffset int64
		wantBridge        bool
	}{
		{name: "Plus", previousPlan: "plus", legacyCurrentPlan: "plus", memberCurrentPlan: "plus", wantBridge: true},
		{name: "Free", previousPlan: "free", legacyCurrentPlan: "free", memberCurrentPlan: "free", wantBridge: true},
		{name: "Pro", previousPlan: "pro", legacyCurrentPlan: "pro", memberCurrentPlan: "pro", wantBridge: true},
		{name: "Go", previousPlan: "go", legacyCurrentPlan: "go", memberCurrentPlan: "go", wantBridge: true},
		{name: "normalized personal plan", previousPlan: " Plus ", legacyCurrentPlan: "PLUS", memberCurrentPlan: "plus", wantBridge: true},
		{name: "Team", previousPlan: "team", legacyCurrentPlan: "team", memberCurrentPlan: "team"},
		{name: "Business", previousPlan: "business", legacyCurrentPlan: "business", memberCurrentPlan: "business"},
		{name: "Enterprise", previousPlan: "enterprise", legacyCurrentPlan: "enterprise", memberCurrentPlan: "enterprise"},
		{name: "Edu", previousPlan: "edu", legacyCurrentPlan: "edu", memberCurrentPlan: "edu"},
		{name: "unknown plan", previousPlan: "", legacyCurrentPlan: "", memberCurrentPlan: ""},
		{name: "future plan", previousPlan: "starter", legacyCurrentPlan: "starter", memberCurrentPlan: "starter"},
		{name: "plan changed", previousPlan: "free", legacyCurrentPlan: "plus", memberCurrentPlan: "plus"},
		{name: "mixed member cycle", previousPlan: "plus", legacyCurrentPlan: "plus", memberCurrentPlan: "plus", mixedMemberPlan: "team"},
		{name: "current boundary mismatch", previousPlan: "plus", legacyCurrentPlan: "plus", memberCurrentPlan: "plus", memberStartOffset: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, path := newQuotaSnapshotTestServiceWithPath(t, 250_000)
			account := AccountTarget{
				AuthFileSnapshot:      "member.json",
				AuthProviderSnapshot:  "codex",
				AuthAccountIDSnapshot: "workspace-1",
				AuthIndex:             "auth-member",
				AccountSnapshot:       "member@example.com",
				Source:                "member.json",
			}
			memberKey, memberOK := usageidentity.AccountKey(account.identityFields("codex"))
			legacyKey, legacyOK := usageidentity.LegacyCodexWorkspaceAccountKey("codex", "workspace-1")
			if !memberOK || !legacyOK || memberKey == legacyKey {
				t.Fatalf("quota bridge keys = member:%q/%v legacy:%q/%v", memberKey, memberOK, legacyKey, legacyOK)
			}

			const durationSeconds = int64(100)
			const previousStartMS = int64(100_000)
			const currentStartMS = int64(200_000)
			const currentEndMS = int64(300_000)
			writeQuotaLifecycleSnapshotForKey(t, service, legacyKey, "legacy-previous", 150_000,
				previousStartMS, currentStartMS, durationSeconds, 70, test.previousPlan)
			writeQuotaLifecycleSnapshotForKey(t, service, legacyKey, "legacy-current", 220_000,
				currentStartMS, currentEndMS, durationSeconds, 90, test.legacyCurrentPlan)
			memberStartMS := currentStartMS + test.memberStartOffset
			memberEndMS := currentEndMS + test.memberStartOffset
			writeQuotaLifecycleSnapshotForKey(t, service, memberKey, "member-current", 221_000,
				memberStartMS, memberEndMS, durationSeconds, 11, test.memberCurrentPlan)
			if test.mixedMemberPlan != "" {
				writeQuotaLifecycleSnapshotForKey(t, service, memberKey, "member-current-mixed", 222_000,
					memberStartMS, memberEndMS, durationSeconds, 12, test.mixedMemberPlan)
			}

			result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
				RowKey: "member", Provider: "codex", Account: account,
			}}, NowMS: 250_000})
			if err != nil {
				t.Fatalf("query bridged quota lifecycle: %v", err)
			}
			if len(result.Items) != 1 || len(result.Items[0].Windows) != 1 {
				t.Fatalf("quota bridge result = %#v", result)
			}
			window := result.Items[0].Windows[0]
			wantUsedPercent := 11.0
			if test.mixedMemberPlan != "" {
				wantUsedPercent = 12
			}
			if window.UsedPercent == nil || *window.UsedPercent != wantUsedPercent {
				t.Fatalf("member current usage was replaced by legacy workspace data: %#v", window)
			}
			if gotBridge := window.PreviousCycle != nil; gotBridge != test.wantBridge {
				t.Fatalf("previous cycle bridged = %v, want %v: %#v", gotBridge, test.wantBridge, window.PreviousCycle)
			}
			if test.wantBridge {
				if window.CurrentCycle == nil || window.PreviousCycle.ActualStartMS != previousStartMS ||
					window.PreviousCycle.ActualEndMS == nil || *window.PreviousCycle.ActualEndMS != currentStartMS ||
					window.PreviousCycle.EndReason != "scheduled" ||
					window.PreviousCycle.ActivationID == window.CurrentCycle.ActivationID {
					t.Fatalf("bridged previous cycle = %#v current=%#v", window.PreviousCycle, window.CurrentCycle)
				}
			}

			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatalf("open quota bridge database: %v", err)
			}
			defer db.Close()
			var legacySnapshots, memberSnapshots int
			if err := db.QueryRow(`select count(*) from account_quota_snapshots where account_key = ?`, legacyKey).Scan(&legacySnapshots); err != nil {
				t.Fatalf("count legacy quota snapshots: %v", err)
			}
			if err := db.QueryRow(`select count(*) from account_quota_snapshots where account_key = ?`, memberKey).Scan(&memberSnapshots); err != nil {
				t.Fatalf("count member quota snapshots: %v", err)
			}
			wantMemberSnapshots := 1
			if test.mixedMemberPlan != "" {
				wantMemberSnapshots = 2
			}
			if legacySnapshots != 2 || memberSnapshots != wantMemberSnapshots {
				t.Fatalf("quota query rewrote snapshots: legacy=%d member=%d", legacySnapshots, memberSnapshots)
			}
		})
	}
}

func TestQueryKeepsMemberPreviousCycleInsteadOfLegacyWorkspaceCycle(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 250_000)
	account := AccountTarget{
		AuthFileSnapshot:      "member.json",
		AuthProviderSnapshot:  "codex",
		AuthAccountIDSnapshot: "workspace-1",
		AuthIndex:             "auth-member",
		AccountSnapshot:       "member@example.com",
		Source:                "member.json",
	}
	memberKey, _ := usageidentity.AccountKey(account.identityFields("codex"))
	legacyKey, _ := usageidentity.LegacyCodexWorkspaceAccountKey("codex", "workspace-1")
	for _, accountKey := range []string{legacyKey, memberKey} {
		writeQuotaLifecycleSnapshotForKey(t, service, accountKey, accountKey+"-previous", 150_000,
			100_000, 200_000, 100, 70, "plus")
		writeQuotaLifecycleSnapshotForKey(t, service, accountKey, accountKey+"-current", 220_000,
			200_000, 300_000, 100, 10, "plus")
	}

	result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "member", Provider: "codex", Account: account,
	}}, NowMS: 250_000})
	if err != nil {
		t.Fatalf("query member lifecycle: %v", err)
	}
	if len(result.Items) != 1 || len(result.Items[0].Windows) != 1 {
		t.Fatalf("member lifecycle result = %#v", result)
	}
	window := result.Items[0].Windows[0]
	if window.CurrentCycle == nil || window.PreviousCycle == nil ||
		window.PreviousCycle.ActivationID != window.CurrentCycle.ActivationID {
		t.Fatalf("member previous cycle was replaced by legacy workspace cycle: current=%#v previous=%#v", window.CurrentCycle, window.PreviousCycle)
	}
}

func TestLegacyCodexQuotaBridgeAcceptsOnlyReliableClosedCycleReasons(t *testing.T) {
	currentStartMS := int64(200_000)
	currentEndMS := int64(300_000)
	previousEndMS := currentStartMS
	durationSeconds := int64(100)
	cycle := func(id, activationID int64, state, endReason string, startMS int64, endMS *int64) *model.AccountQuotaCycle {
		return &model.AccountQuotaCycle{
			ID:               id,
			ActivationID:     activationID,
			State:            state,
			ScheduledStartMS: &startMS,
			ScheduledEndMS:   endMS,
			ActualStartMS:    startMS,
			ActualEndMS:      endMS,
			DurationSeconds:  &durationSeconds,
			BoundaryAccuracy: "exact",
			EndReason:        endReason,
		}
	}
	for _, test := range []struct {
		endReason string
		want      bool
	}{
		{endReason: "scheduled", want: true},
		{endReason: "provider_reset", want: true},
		{endReason: "early_reset", want: true},
		{endReason: "mode_changed"},
		{endReason: "window_removed"},
		{endReason: ""},
	} {
		t.Run(test.endReason, func(t *testing.T) {
			current := model.AccountQuotaWindowState{
				WindowMode:   "fixed",
				Availability: "active",
				CurrentCycle: cycle(3, 2, "active", "", currentStartMS, nil),
			}
			legacy := model.AccountQuotaWindowState{
				WindowMode:    "fixed",
				Availability:  "active",
				CurrentCycle:  cycle(2, 1, "active", "", currentStartMS, nil),
				PreviousCycle: cycle(1, 1, "closed", test.endReason, 100_000, &previousEndMS),
			}
			current.CurrentCycle.ScheduledEndMS = &currentEndMS
			legacy.CurrentCycle.ScheduledEndMS = &currentEndMS
			if got := legacyCodexQuotaBridgeCyclesMatch(current, legacy); got != test.want {
				t.Fatalf("legacyCodexQuotaBridgeCyclesMatch(%q) = %v, want %v", test.endReason, got, test.want)
			}
		})
	}
}

func TestWriteQueryDoesNotMergeOlderResetCreditsAfterNewZeroCount(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 20_000)
	cycleStart := int64(10_000)
	cycleEnd := int64(30_000)
	duration := int64(20)
	used := 20.0
	one := int64(1)
	zero := int64(0)

	for _, entry := range []WriteEntry{
		{
			Provider: "codex", Account: quotaSnapshotTestAccount(),
			Windows: []WindowInput{{
				ProviderWindowID: "rate_limit:five_hour", WindowKind: "five_hour",
				WindowMode: "fixed", ModelScopeKind: "all", Source: "api_query",
				ObservedAtMS: 15_000, BoundaryAccuracy: "exact",
				CycleStartMS: &cycleStart, CycleEndMS: &cycleEnd, DurationSeconds: &duration,
				UsedPercent: &used, ResetCreditsAvailable: &one,
				ResetCredits: []ResetCredit{{ID: "credit-1", ExpiresAtMS: 100_000}},
			}},
		},
		{
			Provider: "codex", Account: quotaSnapshotTestAccount(),
			Windows: []WindowInput{{
				ProviderWindowID: "rate_limit:five_hour", WindowKind: "five_hour",
				WindowMode: "fixed", ModelScopeKind: "all", Source: "api_query",
				ObservedAtMS: 19_000, BoundaryAccuracy: "exact",
				CycleStartMS: &cycleStart, CycleEndMS: &cycleEnd, DurationSeconds: &duration,
				UsedPercent: &used, ResetCreditsAvailable: &zero,
			}},
		},
	} {
		if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{entry}}); err != nil {
			t.Fatalf("write reset credit observation: %v", err)
		}
	}

	result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-1", Provider: "codex", Account: quotaSnapshotTestAccount(),
	}}})
	if err != nil {
		t.Fatalf("query reset credit snapshots: %v", err)
	}
	window := result.Items[0].Windows[0]
	if window.ResetCreditsAvailable == nil || *window.ResetCreditsAvailable != 0 || len(window.ResetCredits) != 0 {
		t.Fatalf("new zero count retained older reset credits: %#v", window)
	}
	if source := window.FieldSources["reset_credits_available"]; source.ObservedAtMS != 19_000 {
		t.Fatalf("zero count source = %#v", source)
	}
	if _, ok := window.FieldSources["reset_credits"]; ok {
		t.Fatalf("cleared reset credits retained stale field source: %#v", window.FieldSources)
	}
}

func TestQueryPreservesCodexAPIFieldsBeyondRawCandidateLimit(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 5_000_000)
	cycleStart := int64(1_000_000)
	cycleEnd := int64(6_000_000)
	duration := int64(5_000)
	available := int64(1)
	apiUsed := 10.0
	_, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
		Provider: "codex", Account: quotaSnapshotTestAccount(), Windows: []WindowInput{{
			ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "fixed",
			ModelScopeKind: "all", Source: "api_query", ObservedAtMS: 1_000,
			BoundaryAccuracy: "exact", CycleStartMS: &cycleStart, CycleEndMS: &cycleEnd,
			DurationSeconds: &duration, UsedPercent: &apiUsed, ResetCreditsAvailable: &available,
		}},
	}}})
	if err != nil {
		t.Fatalf("write api snapshot: %v", err)
	}

	for batch := 0; batch < 6; batch++ {
		entries := make([]WriteEntry, 400)
		for index := range entries {
			used := 20.0 + float64(batch)
			entries[index] = WriteEntry{
				Provider: "codex", Account: quotaSnapshotTestAccount(), Windows: []WindowInput{{
					ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "fixed",
					ModelScopeKind: "all", Source: "response_header",
					ObservedAtMS: 2_000 + int64(batch*400+index), BoundaryAccuracy: "derived",
					CycleStartMS: &cycleStart, CycleEndMS: &cycleEnd, DurationSeconds: &duration,
					UsedPercent: &used,
				}},
			}
		}
		if _, err := service.Write(context.Background(), WriteRequest{Entries: entries}); err != nil {
			t.Fatalf("write header batch %d: %v", batch, err)
		}
	}

	result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-1", Provider: "codex", Account: quotaSnapshotTestAccount(),
	}}})
	if err != nil {
		t.Fatalf("query snapshots: %v", err)
	}
	window := result.Items[0].Windows[0]
	if window.Source != "response_header" {
		t.Fatalf("latest source = %q, want response_header", window.Source)
	}
	if window.ResetCreditsAvailable == nil || *window.ResetCreditsAvailable != available {
		t.Fatalf("api reset credits were crowded out: %#v", window)
	}
	if got := window.FieldSources["reset_credits_available"].Source; got != "api_query" {
		t.Fatalf("reset credit source = %q, want api_query", got)
	}
}

func TestWriteRejectsWindowsOutsideObservationEnvelope(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 20_000)
	base := WindowInput{
		ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "unknown",
		ModelScopeKind: "all", Source: "api_query", SourceObservationID: "provider-query",
		ObservedAtMS: 10_000, BoundaryAccuracy: "unknown",
	}
	tests := []struct {
		name        string
		mutate      func(*WindowInput)
		wantMessage string
	}{
		{
			name: "source", mutate: func(window *WindowInput) { window.Source = "response_header" },
			wantMessage: "source must match observation source",
		},
		{
			name: "observation time", mutate: func(window *WindowInput) { window.ObservedAtMS++ },
			wantMessage: "observed_at_ms must match observation observed_at_ms",
		},
		{
			name: "source observation id", mutate: func(window *WindowInput) { window.SourceObservationID = "other-query" },
			wantMessage: "source_observation_id must match observation source_observation_id",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			window := base
			testCase.mutate(&window)
			_, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
				Provider: "codex", Account: quotaSnapshotTestAccount(),
				Observation: &ObservationInput{
					Source: "api_query", SourceObservationID: "provider-query",
					ObservedAtMS: 10_000, InventoryScopeKey: "codex:rate-limits", InventoryMode: "partial",
				},
				Windows: []WindowInput{window},
			}}})
			if err == nil || !strings.Contains(err.Error(), testCase.wantMessage) {
				t.Fatalf("write error = %v, want %q", err, testCase.wantMessage)
			}
		})
	}
}

func TestWriteRejectsInvalidQuotaBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		window      func() WindowInput
		wantMessage string
	}{
		{
			name: "zero start",
			window: func() WindowInput {
				start, end, duration := int64(0), int64(11_000), int64(10)
				return WindowInput{ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "fixed", ModelScopeKind: "all", Source: "api_query", ObservedAtMS: 10_000, BoundaryAccuracy: "exact", CycleStartMS: &start, CycleEndMS: &end, DurationSeconds: &duration}
			},
			wantMessage: "cycle_start_ms must be greater than 0",
		},
		{
			name: "zero end",
			window: func() WindowInput {
				start, end, duration := int64(1_000), int64(0), int64(1)
				return WindowInput{ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "fixed", ModelScopeKind: "all", Source: "api_query", ObservedAtMS: 10_000, BoundaryAccuracy: "exact", CycleStartMS: &start, CycleEndMS: &end, DurationSeconds: &duration}
			},
			wantMessage: "cycle_end_ms must be greater than 0",
		},
		{
			name: "duration mismatch",
			window: func() WindowInput {
				start, end, duration := int64(1_000), int64(11_000), int64(9)
				return WindowInput{ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "fixed", ModelScopeKind: "all", Source: "api_query", ObservedAtMS: 10_000, BoundaryAccuracy: "exact", CycleStartMS: &start, CycleEndMS: &end, DurationSeconds: &duration}
			},
			wantMessage: "cycle boundaries must match duration_seconds",
		},
		{
			name: "fractional derived duration",
			window: func() WindowInput {
				start, end := int64(1_000), int64(2_500)
				return WindowInput{ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "fixed", ModelScopeKind: "all", Source: "api_query", ObservedAtMS: 10_000, BoundaryAccuracy: "unknown", CycleStartMS: &start, CycleEndMS: &end}
			},
			wantMessage: "cycle boundary difference must be a whole number of seconds",
		},
		{
			name: "nonpositive derived start",
			window: func() WindowInput {
				end, duration := int64(500), int64(1)
				return WindowInput{ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "fixed", ModelScopeKind: "all", Source: "api_query", ObservedAtMS: 10_000, BoundaryAccuracy: "unknown", CycleEndMS: &end, DurationSeconds: &duration}
			},
			wantMessage: "derived cycle_start_ms must be greater than 0",
		},
		{
			name: "duration overflow",
			window: func() WindowInput {
				duration := int64(math.MaxInt64)
				return WindowInput{ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "rolling", ModelScopeKind: "all", Source: "api_query", ObservedAtMS: 10_000, BoundaryAccuracy: "estimated", DurationSeconds: &duration}
			},
			wantMessage: "duration_seconds is too large",
		},
		{
			name: "derived end overflow",
			window: func() WindowInput {
				start, duration := int64(math.MaxInt64-500), int64(1)
				return WindowInput{ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "rolling", ModelScopeKind: "all", Source: "api_query", ObservedAtMS: 10_000, BoundaryAccuracy: "estimated", CycleStartMS: &start, DurationSeconds: &duration}
			},
			wantMessage: "derived cycle_end_ms is too large",
		},
		{
			name: "rolling expiry overflow",
			window: func() WindowInput {
				duration := int64(1)
				return WindowInput{ProviderWindowID: "rolling", WindowKind: "rolling", WindowMode: "rolling", ModelScopeKind: "all", Source: "api_query", ObservedAtMS: math.MaxInt64 - 500, BoundaryAccuracy: "estimated", DurationSeconds: &duration}
			},
			wantMessage: "rolling window expiry is too large",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			nowMS := int64(20_000)
			observedAtMS := int64(10_000)
			if testCase.name == "rolling expiry overflow" {
				nowMS = math.MaxInt64 - 500
				observedAtMS = nowMS
			}
			service := newQuotaSnapshotTestService(t, nowMS)
			_, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
				Provider: "codex", Account: quotaSnapshotTestAccount(),
				Observation: &ObservationInput{Source: "api_query", SourceObservationID: testCase.name, ObservedAtMS: observedAtMS, InventoryScopeKey: "codex:rate-limits", InventoryMode: "partial"},
				Windows:     []WindowInput{testCase.window()},
			}}})
			if err == nil || !strings.Contains(err.Error(), testCase.wantMessage) {
				t.Fatalf("write error = %v, want %q", err, testCase.wantMessage)
			}
		})
	}
}

func TestWriteRejectsNegativeResetCreditsAvailable(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 20_000)
	available := int64(-1)
	_, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
		Provider: "codex", Account: quotaSnapshotTestAccount(), Windows: []WindowInput{{
			ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "unknown",
			ModelScopeKind: "all", Source: "api_query", ObservedAtMS: 10_000,
			BoundaryAccuracy: "unknown", ResetCreditsAvailable: &available,
		}},
	}}})
	if err == nil || !strings.Contains(err.Error(), "reset_credits_available must be greater than or equal to 0") {
		t.Fatalf("write error = %v, want negative reset credit validation", err)
	}
}

func TestWriteRejectsOverlongObservationID(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 20_000)
	longID := strings.Repeat("x", maxObservationIDLen+1)
	_, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
		Provider: "codex", Account: quotaSnapshotTestAccount(),
		Observation: &ObservationInput{
			Source: "api_query", SourceObservationID: longID, ObservedAtMS: 10_000,
			InventoryScopeKey: "codex:rate-limits", InventoryMode: "partial",
		},
		Windows: []WindowInput{{
			ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "unknown",
			ModelScopeKind: "all", Source: "api_query", ObservedAtMS: 10_000,
			BoundaryAccuracy: "unknown",
		}},
	}}})
	if err == nil || !strings.Contains(err.Error(), "source_observation_id must be less than or equal to") {
		t.Fatalf("write error = %v, want observation id length validation", err)
	}
}

func TestWriteRejectsInvalidWindowRelationships(t *testing.T) {
	base := WindowInput{
		ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "unknown",
		ModelScopeKind: "all", Source: "api_query", ObservedAtMS: 10_000,
		BoundaryAccuracy: "unknown",
	}
	tests := []struct {
		name   string
		mutate func(*WindowInput)
		want   string
	}{
		{
			name: "container without relationship",
			mutate: func(window *WindowInput) {
				window.ContainerWindowID = "weekly"
			},
			want: "relationship_kind is required",
		},
		{
			name: "relationship without container",
			mutate: func(window *WindowInput) {
				window.RelationshipKind = "concurrent_subwindow"
			},
			want: "container_provider_window_id is required",
		},
		{
			name: "unsupported relationship",
			mutate: func(window *WindowInput) {
				window.RelationshipKind = "nested"
				window.ContainerWindowID = "weekly"
			},
			want: "unsupported relationship_kind",
		},
		{
			name: "self relationship",
			mutate: func(window *WindowInput) {
				window.RelationshipKind = "concurrent_subwindow"
				window.ContainerWindowID = "five-hour"
			},
			want: "must differ",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			service := newQuotaSnapshotTestService(t, 20_000)
			window := base
			testCase.mutate(&window)
			_, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
				Provider: "codex", Account: quotaSnapshotTestAccount(), Windows: []WindowInput{window},
			}}})
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("write error = %v, want substring %q", err, testCase.want)
			}
		})
	}
}

func TestNormalizeResetCreditsCanonicalizesOrderAndRejectsConflictingExpiry(t *testing.T) {
	first, err := normalizeResetCredits([]ResetCredit{
		{ID: "Credit-B", ExpiresAtMS: 20},
		{ID: " credit-a ", ExpiresAtMS: 10},
		{ID: "CREDIT-A", ExpiresAtMS: 10},
	})
	if err != nil {
		t.Fatalf("normalize reset credits: %v", err)
	}
	second, err := normalizeResetCredits([]ResetCredit{
		{ID: "CREDIT-A", ExpiresAtMS: 10},
		{ID: "Credit-B", ExpiresAtMS: 20},
	})
	if err != nil || fmt.Sprintf("%v", first) != fmt.Sprintf("%v", second) {
		t.Fatalf("canonical reset credits differ: first=%v second=%v err=%v", first, second, err)
	}
	if len(first) != 2 || first[0].ID != "CREDIT-A" || first[1].ID != "Credit-B" {
		t.Fatalf("canonical reset credits = %#v", first)
	}
	if _, err := normalizeResetCredits([]ResetCredit{
		{ID: "credit-a", ExpiresAtMS: 10},
		{ID: "CREDIT-A", ExpiresAtMS: 11},
	}); err == nil || !strings.Contains(err.Error(), "conflicting expiry") {
		t.Fatalf("conflicting reset credit error = %v", err)
	}
}

func TestWriteRejectsConflictingWindowMutations(t *testing.T) {
	base := quotaLifecycleFixedWindow("five-hour", "five_hour", 1_000, 5*60*60, 20)
	tests := []struct {
		name  string
		entry WriteEntry
		want  string
	}{
		{
			name: "duplicate reported window",
			entry: quotaLifecycleWriteEntryWithObservation(
				"partial", "inspection", "duplicate-reported", "codex:rate-limits", 2_000,
				[]WindowInput{base, base},
			),
			want: "duplicates",
		},
		{
			name: "removed window in complete inventory",
			entry: func() WriteEntry {
				entry := quotaLifecycleWriteEntryWithObservation(
					"complete", "inspection", "complete-removal", "codex:rate-limits", 2_000, nil,
				)
				entry.RemovedWindows = []RemovedWindowInput{{ProviderWindowID: "five-hour", ModelScopeKind: "all"}}
				return entry
			}(),
			want: "require delta inventory_mode",
		},
		{
			name: "duplicate removed window",
			entry: func() WriteEntry {
				entry := quotaLifecycleWriteEntryWithObservation(
					"delta", "inspection", "duplicate-removed", "codex:rate-limits", 2_000, nil,
				)
				entry.RemovedWindows = []RemovedWindowInput{
					{ProviderWindowID: "five-hour", ModelScopeKind: "all"},
					{ProviderWindowID: "five-hour", ModelScopeKind: "all"},
				}
				return entry
			}(),
			want: "duplicates",
		},
		{
			name: "reported and removed overlap",
			entry: func() WriteEntry {
				entry := quotaLifecycleWriteEntryWithObservation(
					"delta", "inspection", "overlap", "codex:rate-limits", 2_000, []WindowInput{base},
				)
				entry.RemovedWindows = []RemovedWindowInput{{ProviderWindowID: "five-hour", ModelScopeKind: "all"}}
				return entry
			}(),
			want: "conflicts",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			service := newQuotaSnapshotTestService(t, 20_000)
			_, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{testCase.entry}})
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("write error = %v, want substring %q", err, testCase.want)
			}
		})
	}
}

func TestNormalizeRemovedWindowRequiresKindForCodexSecondaryAlias(t *testing.T) {
	if _, err := normalizeRemovedWindow("codex", RemovedWindowInput{
		ProviderWindowID: "secondary",
		ModelScopeKind:   "all",
	}); err == nil || !strings.Contains(err.Error(), "window_kind") {
		t.Fatalf("secondary removal without window kind error = %v", err)
	}

	removed, err := normalizeRemovedWindow("codex", RemovedWindowInput{
		ProviderWindowID: "secondary",
		WindowKind:       "monthly",
		ModelScopeKind:   "all",
	})
	if err != nil {
		t.Fatalf("monthly secondary removal: %v", err)
	}
	if removed.ProviderWindowID != "monthly" || removed.ScopeFingerprint == "" {
		t.Fatalf("monthly secondary removal = %#v", removed)
	}
}

func TestWriteRejectsTooManyWindowMutations(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 20_000)
	_, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
		Provider: "codex", Account: quotaSnapshotTestAccount(), Windows: make([]WindowInput, maxWriteEntries+1),
	}}})
	if err == nil || !strings.Contains(err.Error(), "window mutations") {
		t.Fatalf("write error = %v, want mutation limit", err)
	}
}

func TestWriteKeepsCanonicalFieldOnlyObservationChangesIdempotent(t *testing.T) {
	service, path := newQuotaSnapshotTestServiceWithPath(t, 20_000)
	start, end, duration := int64(1_000), int64(11_000), int64(10)
	write := func(available int64) {
		_, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
			Provider: "codex", Account: quotaSnapshotTestAccount(),
			Observation: &ObservationInput{Source: "api_query", SourceObservationID: "same-observation", ObservedAtMS: 10_000, InventoryScopeKey: "codex:rate-limits", InventoryMode: "partial"},
			Windows: []WindowInput{{
				ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "fixed", ModelScopeKind: "all", Source: "api_query", SourceObservationID: "same-observation", ObservedAtMS: 10_000, BoundaryAccuracy: "exact", CycleStartMS: &start, CycleEndMS: &end, DurationSeconds: &duration, ResetCreditsAvailable: &available,
			}},
		}}})
		if err != nil {
			t.Fatalf("write available=%d: %v", available, err)
		}
	}
	write(1)
	write(2)

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open hash test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var observations, snapshots int
	if err := db.QueryRow("select count(*) from account_quota_observations").Scan(&observations); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	if err := db.QueryRow("select count(*) from account_quota_snapshots").Scan(&snapshots); err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	if observations != 2 || snapshots != 2 {
		t.Fatalf("canonical field-only writes were deduplicated: observations=%d snapshots=%d", observations, snapshots)
	}
}

func TestWriteResponseReportsOnlyNewlyPersistedSnapshots(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 20_000)
	entry := WriteEntry{
		Provider: "codex", Account: quotaSnapshotTestAccount(),
		Observation: &ObservationInput{
			Source: "api_query", SourceObservationID: "same-observation",
			ObservedAtMS: 10_000, InventoryScopeKey: "codex:rate-limits", InventoryMode: "partial",
		},
		Windows: []WindowInput{{
			ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "unknown",
			ModelScopeKind: "all", Source: "api_query", SourceObservationID: "same-observation",
			ObservedAtMS: 10_000, BoundaryAccuracy: "unknown",
		}},
	}
	first, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{entry}})
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	second, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{entry}})
	if err != nil {
		t.Fatalf("duplicate write: %v", err)
	}
	if first.Items[0].InsertedCount != 1 || second.Items[0].InsertedCount != 0 {
		t.Fatalf("inserted counts = %d, %d; want 1, 0", first.Items[0].InsertedCount, second.Items[0].InsertedCount)
	}
}

func TestWriteResponseCountsSnapshotPersistedOutsideLifecycleOwnerScope(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 20_000)
	window := WindowInput{
		ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "unknown",
		ModelScopeKind: "all", Source: "api_query", BoundaryAccuracy: "unknown",
	}
	write := func(scopeKey, observationID string, observedAtMS int64) WriteResponse {
		t.Helper()
		entry := quotaLifecycleWriteEntryWithObservation(
			"partial", "api_query", observationID, scopeKey, observedAtMS, []WindowInput{window},
		)
		response, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{entry}})
		if err != nil {
			t.Fatalf("write scope %q: %v", scopeKey, err)
		}
		return response
	}

	owner := write("codex:owner-a", "owner-a", 10_000)
	nonOwner := write("codex:owner-b", "owner-b", 11_000)
	duplicate := write("codex:owner-b", "owner-b", 11_000)
	if owner.Items[0].InsertedCount != 1 || nonOwner.Items[0].InsertedCount != 1 ||
		duplicate.Items[0].InsertedCount != 0 {
		t.Fatalf(
			"inserted counts across lifecycle scopes = %d, %d, %d; want 1, 1, 0",
			owner.Items[0].InsertedCount,
			nonOwner.Items[0].InsertedCount,
			duplicate.Items[0].InsertedCount,
		)
	}
}

func TestQueryPreservesDistinctModelScopesWithSharedProviderWindowID(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 100_000)
	account := AccountTarget{
		AuthFileSnapshot:     "antigravity.json",
		AuthProviderSnapshot: "antigravity",
		AuthIndex:            "ag-1",
	}
	duration := int64(1_000)
	cycleStart := int64(1_000)
	cycleEnd := cycleStart + duration*1000
	write := func(observedAtMS int64, modelID string, usedPercent float64) {
		t.Helper()
		if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
			Provider: "antigravity", Account: account, Windows: []WindowInput{{
				ProviderWindowID: "shared-daily", WindowKind: "daily", WindowMode: "fixed",
				ModelScopeKind: "models", ModelIDs: []string{modelID}, Source: "api_query",
				ObservedAtMS: observedAtMS, BoundaryAccuracy: "exact",
				CycleStartMS: &cycleStart, CycleEndMS: &cycleEnd, DurationSeconds: &duration,
				UsedPercent: &usedPercent,
			}},
		}}}); err != nil {
			t.Fatalf("write %s model scope: %v", modelID, err)
		}
	}
	write(1_000, "model-beta", 70)
	for index := 0; index < 12; index++ {
		write(2_000+int64(index), "model-alpha", 20+float64(index))
	}

	result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-ag", Provider: "antigravity", Account: account,
	}}})
	if err != nil {
		t.Fatalf("query shared model scopes: %v", err)
	}
	if len(result.Items) != 1 || len(result.Items[0].Windows) != 2 {
		t.Fatalf("shared provider window model scopes = %#v", result)
	}
	byModel := make(map[string]Window)
	for _, window := range result.Items[0].Windows {
		if len(window.ModelIDs) == 1 {
			byModel[window.ModelIDs[0]] = window
		}
	}
	if byModel["model-beta"].UsedPercent == nil || *byModel["model-beta"].UsedPercent != 70 {
		t.Fatalf("model-beta snapshot was assigned or crowded out: %#v", byModel)
	}
}

func TestQueryDoesNotPromoteExpiredOrIncompleteFixedWindow(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 50_000)
	cycleStart := int64(10_000)
	cycleEnd := int64(30_000)
	duration := int64(20)
	used := 80.0
	available := int64(2)
	_, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
		Provider: "codex", Account: quotaSnapshotTestAccount(), Windows: []WindowInput{{
			ProviderWindowID: "rate_limit:weekly", WindowKind: "weekly",
			WindowMode: "fixed", ModelScopeKind: "all", Source: "api_query",
			ObservedAtMS: 20_000, BoundaryAccuracy: "exact",
			CycleStartMS: &cycleStart, CycleEndMS: &cycleEnd, DurationSeconds: &duration,
			UsedPercent: &used, ResetCreditsAvailable: &available,
			ResetCredits: []ResetCredit{{ID: "expired-cycle-credit", ExpiresAtMS: 100_000}},
		}},
	}}})
	if err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-1", Provider: "codex", Account: quotaSnapshotTestAccount(),
	}}})
	if err != nil {
		t.Fatalf("query snapshot: %v", err)
	}
	if !result.Items[0].Windows[0].Stale {
		t.Fatalf("expired fixed snapshot must be stale: %#v", result.Items[0].Windows[0])
	}
	if result.Items[0].Windows[0].ResetCreditsAvailable != nil ||
		len(result.Items[0].Windows[0].ResetCredits) != 0 {
		t.Fatalf("expired fixed snapshot exposed current reset credits: %#v", result.Items[0].Windows[0])
	}
}

func TestWriteRejectsReliableFixedWindowWithoutCompleteBoundary(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 20_000)
	_, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
		Provider: "claude", Account: AccountTarget{AuthIndex: "auth-1"},
		Windows: []WindowInput{{
			ProviderWindowID: "five_hour", WindowKind: "five_hour", WindowMode: "fixed",
			ModelScopeKind: "all", Source: "api_query", ObservedAtMS: 10_000,
			BoundaryAccuracy: "exact",
		}},
	}}})
	if err == nil {
		t.Fatal("expected incomplete reliable fixed window to be rejected")
	}
}

func TestWriteUsageEventsPersistsCodexHeaderWindows(t *testing.T) {
	const observedAtMS = int64(1_780_000_000_000)
	service := newQuotaSnapshotTestService(t, observedAtMS+1_000)
	used := 35.0
	resetAfter := 600.0
	minutes := 300.0
	resetAtMS := observedAtMS + int64(resetAfter*1000)
	event := usage.Event{
		TimestampMS:          observedAtMS,
		Provider:             "codex",
		Model:                "gpt-5.6-sol",
		AnalyticsModel:       "gpt-5.6-sol",
		AuthFileSnapshot:     "codex.json",
		AuthProviderSnapshot: "codex",
		AuthIndex:            "auth-1",
		AccountSnapshot:      "user@example.com",
		RequestID:            "req-codex-header",
		ResponseMetadata: &usage.ResponseHeaderMetadata{Quota: &usage.HeaderQuotaMetadata{
			PlanType: "plus",
			Primary: &usage.HeaderQuotaWindow{
				UsedPercent:       &used,
				ResetAtMS:         resetAtMS,
				ResetAfterSeconds: &resetAfter,
				WindowMinutes:     &minutes,
			},
		}},
	}
	if err := service.WriteUsageEvents(context.Background(), []usage.Event{event}); err != nil {
		t.Fatalf("write usage evidence: %v", err)
	}
	result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-1", Provider: "codex", Account: quotaSnapshotTestAccount(),
	}}})
	if err != nil {
		t.Fatalf("query usage evidence: %v", err)
	}
	if len(result.Items) != 1 || len(result.Items[0].Windows) != 1 {
		t.Fatalf("query result = %#v", result)
	}
	window := result.Items[0].Windows[0]
	if window.ProviderWindowID != "five-hour" || window.WindowMode != "fixed" || window.BoundaryAccuracy != "derived" {
		t.Fatalf("codex window = %#v", window)
	}
	if window.CycleEndMS == nil || *window.CycleEndMS != resetAtMS || window.DurationSeconds == nil || *window.DurationSeconds != 18_000 {
		t.Fatalf("codex boundaries = %#v", window)
	}
	if window.Source != "response_header" || window.SourceObservationID != "req-codex-header" {
		t.Fatalf("codex provenance = %#v", window)
	}
}

func TestWriteUsageEventsKeepsCodexMainAndSparkHeaderWindowsIndependent(t *testing.T) {
	const observedAtMS = int64(1_780_000_100_000)
	service := newQuotaSnapshotTestService(t, observedAtMS+2_000)
	mainUsed := 36.0
	sparkUsed := 0.0
	resetAfter := float64(7 * 24 * 60 * 60)
	minutes := float64(7 * 24 * 60)
	mainResetAtMS := observedAtMS + int64(resetAfter*1000)
	sparkResetAtMS := mainResetAtMS + 1_000
	events := []usage.Event{
		{
			TimestampMS:          observedAtMS,
			Provider:             "codex",
			Model:                "gpt-5.6-sol",
			AnalyticsModel:       "gpt-5.6-sol",
			AuthFileSnapshot:     "codex.json",
			AuthProviderSnapshot: "codex",
			AuthIndex:            "auth-1",
			AccountSnapshot:      "user@example.com",
			RequestID:            "req-codex-main-header",
			ResponseMetadata: &usage.ResponseHeaderMetadata{Quota: &usage.HeaderQuotaMetadata{
				PlanType: "plus",
				Primary: &usage.HeaderQuotaWindow{
					UsedPercent: &mainUsed, ResetAtMS: mainResetAtMS,
					ResetAfterSeconds: &resetAfter, WindowMinutes: &minutes,
				},
			}},
		},
		{
			TimestampMS:          observedAtMS + 1_000,
			Provider:             "codex",
			Model:                "my-spark",
			AnalyticsModel:       "my-spark",
			RequestedModel:       "my-spark",
			ResolvedModel:        codexquota.SparkModelID,
			AuthFileSnapshot:     "codex.json",
			AuthProviderSnapshot: "codex",
			AuthIndex:            "auth-1",
			AccountSnapshot:      "user@example.com",
			RequestID:            "req-codex-spark-header",
			ResponseMetadata: &usage.ResponseHeaderMetadata{Quota: &usage.HeaderQuotaMetadata{
				PlanType: "plus",
				Primary: &usage.HeaderQuotaWindow{
					UsedPercent: &sparkUsed, ResetAtMS: sparkResetAtMS,
					ResetAfterSeconds: &resetAfter, WindowMinutes: &minutes,
				},
			}},
		},
	}
	if err := service.WriteUsageEvents(context.Background(), events); err != nil {
		t.Fatalf("write scoped usage evidence: %v", err)
	}
	result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-1", Provider: "codex", Account: quotaSnapshotTestAccount(),
	}}})
	if err != nil {
		t.Fatalf("query scoped usage evidence: %v", err)
	}
	if len(result.Items) != 1 || len(result.Items[0].Windows) != 2 {
		t.Fatalf("scoped header windows = %#v", result)
	}
	byID := make(map[string]Window, len(result.Items[0].Windows))
	for _, window := range result.Items[0].Windows {
		byID[window.ProviderWindowID] = window
	}
	mainWindow := byID["weekly"]
	if mainWindow.ModelScopeKind != "family" || mainWindow.ModelScopeKey != codexquota.MainScopeKey ||
		mainWindow.UsedPercent == nil || *mainWindow.UsedPercent != mainUsed {
		t.Fatalf("main Header window = %#v", mainWindow)
	}
	sparkWindow := byID["spark-weekly-0"]
	if sparkWindow.ModelScopeKind != "models" || len(sparkWindow.ModelIDs) != 1 ||
		sparkWindow.ModelIDs[0] != codexquota.SparkModelID || sparkWindow.UsedPercent == nil ||
		*sparkWindow.UsedPercent != sparkUsed {
		t.Fatalf("Spark Header window = %#v", sparkWindow)
	}
	hasLegacySparkAlias := false
	for _, alias := range sparkWindow.ProviderWindowAliases {
		if alias == "fast-coding-weekly-0" {
			hasLegacySparkAlias = true
			break
		}
	}
	if !hasLegacySparkAlias {
		t.Fatalf("Spark Header aliases = %#v, want legacy fast-coding alias", sparkWindow.ProviderWindowAliases)
	}
}

func TestWriteUsageEventAndFrontendHeaderObservationUseSameDerivedCycle(t *testing.T) {
	const observedAtMS = int64(1_780_000_000_638)
	service, path := newQuotaSnapshotTestServiceWithPath(t, observedAtMS+1_000)
	used := 35.0
	resetAfter := 600.0
	minutes := 300.0
	resetAtMS := int64(1_780_000_600_000)
	event := usage.Event{
		EventHash:            "zz-header-event",
		TimestampMS:          observedAtMS,
		Provider:             "codex",
		Model:                "gpt-5.6-sol",
		AnalyticsModel:       "gpt-5.6-sol",
		AuthFileSnapshot:     "codex.json",
		AuthProviderSnapshot: "codex",
		AuthIndex:            "auth-1",
		AccountSnapshot:      "user@example.com",
		RequestID:            "req-codex-header",
		ResponseMetadata: &usage.ResponseHeaderMetadata{Quota: &usage.HeaderQuotaMetadata{
			PlanType: "plus",
			Primary: &usage.HeaderQuotaWindow{
				UsedPercent:       &used,
				ResetAtMS:         resetAtMS,
				ResetAfterSeconds: &resetAfter,
				WindowMinutes:     &minutes,
			},
		}},
	}
	if err := service.WriteUsageEvents(context.Background(), []usage.Event{event}); err != nil {
		t.Fatalf("write backend usage evidence: %v", err)
	}

	durationSeconds := int64(18_000)
	cycleStartMS := resetAtMS - durationSeconds*1000
	remaining := 65.0
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
		RowKey: "row-1", Provider: "codex", Account: quotaSnapshotTestAccount(),
		Observation: &ObservationInput{
			Source: "response_header", SourceObservationID: event.EventHash,
			ObservedAtMS: observedAtMS, InventoryScopeKey: "codex:rate-limits", InventoryMode: "partial",
		},
		Windows: []WindowInput{{
			ProviderWindowID: "five-hour", WindowKind: "five_hour", WindowMode: "fixed",
			ModelScopeKind: "family", ModelScopeKey: "codex_main",
			Source: "response_header", SourceObservationID: event.EventHash,
			ObservedAtMS: observedAtMS, BoundaryAccuracy: "derived",
			CycleStartMS: &cycleStartMS, CycleEndMS: &resetAtMS, DurationSeconds: &durationSeconds,
			UsedPercent: &used, RemainingPercent: &remaining, PlanType: "plus",
		}},
	}}}); err != nil {
		t.Fatalf("write frontend header observation: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if window.CurrentCycle == nil || window.CurrentCycle.BoundaryAccuracy != "derived" ||
		window.CurrentCycle.ActualStartMS != cycleStartMS ||
		window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != resetAtMS {
		t.Fatalf("dual header ingestion changed the derived cycle = %#v", window)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open dual-ingestion database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var cycleCount int
	if err := db.QueryRow(`select count(*) from account_quota_cycles`).Scan(&cycleCount); err != nil {
		t.Fatalf("count dual-ingestion cycles: %v", err)
	}
	if cycleCount != 1 {
		t.Fatalf("dual header ingestion created %d cycles, want 1", cycleCount)
	}
}

func TestWriteQueryStabilizesDerivedCodexHeaderBoundaryWithinCycle(t *testing.T) {
	const (
		firstObservedAtMS  = int64(1_785_928_574_638)
		secondObservedAtMS = int64(1_785_928_787_294)
	)
	service := newQuotaSnapshotTestService(t, secondObservedAtMS+1_000)
	durationSeconds := int64(30 * 24 * 60 * 60)
	firstCycleEndMS := int64(1_788_520_573_000)
	secondCycleEndMS := int64(1_788_520_580_000)
	firstCycleStartMS := firstCycleEndMS - durationSeconds*1000
	secondCycleStartMS := secondCycleEndMS - durationSeconds*1000
	firstUsed := 0.0
	secondUsed := 1.0

	for _, input := range []WindowInput{
		{
			ProviderWindowID: "monthly", WindowKind: "monthly", WindowMode: "fixed",
			ModelScopeKind: "all", Source: "response_header", SourceObservationID: "req-first",
			ObservedAtMS: firstObservedAtMS, BoundaryAccuracy: "derived",
			CycleStartMS: &firstCycleStartMS, CycleEndMS: &firstCycleEndMS,
			DurationSeconds: &durationSeconds, UsedPercent: &firstUsed,
		},
		{
			ProviderWindowID: "monthly", WindowKind: "monthly", WindowMode: "fixed",
			ModelScopeKind: "all", Source: "response_header", SourceObservationID: "req-second",
			ObservedAtMS: secondObservedAtMS, BoundaryAccuracy: "derived",
			CycleStartMS: &secondCycleStartMS, CycleEndMS: &secondCycleEndMS,
			DurationSeconds: &durationSeconds, UsedPercent: &secondUsed,
		},
	} {
		if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
			Provider: "codex", Account: quotaSnapshotTestAccount(), Windows: []WindowInput{input},
		}}}); err != nil {
			t.Fatalf("write header snapshot: %v", err)
		}
	}

	result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-1", Provider: "codex", Account: quotaSnapshotTestAccount(),
	}}})
	if err != nil {
		t.Fatalf("query header snapshots: %v", err)
	}
	if len(result.Items) != 1 || len(result.Items[0].Windows) != 1 {
		t.Fatalf("query result = %#v", result)
	}
	window := result.Items[0].Windows[0]
	if window.CycleStartMS == nil || *window.CycleStartMS != firstCycleStartMS ||
		window.CycleEndMS == nil || *window.CycleEndMS != firstCycleEndMS {
		t.Fatalf("stabilized boundary = %#v", window)
	}
	if window.SourceObservationID != "req-second" || window.UsedPercent == nil || *window.UsedPercent != secondUsed {
		t.Fatalf("latest quota observation was not preserved: %#v", window)
	}
	if quotaSource := window.FieldSources["quota"]; quotaSource.ObservedAtMS != secondObservedAtMS {
		t.Fatalf("stabilized quota source = %#v", quotaSource)
	}
	if boundarySource := window.FieldSources["boundary"]; boundarySource.ObservedAtMS != firstObservedAtMS {
		t.Fatalf("stabilized boundary source = %#v", boundarySource)
	}
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != firstCycleStartMS || window.PreviousCycle != nil {
		t.Fatalf("boundary jitter split one provider cycle: %#v", window)
	}
}

func TestWriteStabilizesDerivedCodexHeaderBoundariesAcrossBatchEntries(t *testing.T) {
	const (
		firstObservedAtMS  = int64(1_785_928_574_638)
		secondObservedAtMS = int64(1_785_928_787_294)
	)
	service := newQuotaSnapshotTestService(t, secondObservedAtMS+1_000)
	durationSeconds := int64(30 * 24 * 60 * 60)
	firstCycleEndMS := int64(1_788_520_573_000)
	secondCycleEndMS := int64(1_788_520_580_000)
	firstCycleStartMS := firstCycleEndMS - durationSeconds*1000
	secondCycleStartMS := secondCycleEndMS - durationSeconds*1000
	firstUsed := 0.0
	secondUsed := 1.0

	entries := []WriteEntry{
		{
			Provider: "codex", Account: quotaSnapshotTestAccount(), Windows: []WindowInput{{
				ProviderWindowID: "monthly", WindowKind: "monthly", WindowMode: "fixed",
				ModelScopeKind: "all", Source: "response_header", SourceObservationID: "batch-first",
				ObservedAtMS: firstObservedAtMS, BoundaryAccuracy: "derived",
				CycleStartMS: &firstCycleStartMS, CycleEndMS: &firstCycleEndMS,
				DurationSeconds: &durationSeconds, UsedPercent: &firstUsed,
			}},
		},
		{
			Provider: "codex", Account: quotaSnapshotTestAccount(), Windows: []WindowInput{{
				ProviderWindowID: "monthly", WindowKind: "monthly", WindowMode: "fixed",
				ModelScopeKind: "all", Source: "response_header", SourceObservationID: "batch-second",
				ObservedAtMS: secondObservedAtMS, BoundaryAccuracy: "derived",
				CycleStartMS: &secondCycleStartMS, CycleEndMS: &secondCycleEndMS,
				DurationSeconds: &durationSeconds, UsedPercent: &secondUsed,
			}},
		},
	}
	if _, err := service.Write(context.Background(), WriteRequest{Entries: entries}); err != nil {
		t.Fatalf("write batched header snapshots: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["monthly"]
	if window.CycleStartMS == nil || *window.CycleStartMS != firstCycleStartMS ||
		window.CycleEndMS == nil || *window.CycleEndMS != firstCycleEndMS ||
		window.SourceObservationID != "batch-second" || window.UsedPercent == nil ||
		*window.UsedPercent != secondUsed || window.CurrentCycle == nil ||
		window.CurrentCycle.ActualStartMS != firstCycleStartMS || window.PreviousCycle != nil {
		t.Fatalf("batched stabilized boundary = %#v", window)
	}
}

func TestWriteDoesNotStabilizeHeaderBoundaryAcrossModelScopes(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 40_000)
	duration := int64(20)
	firstStart, firstEnd := int64(10_000), int64(30_000)
	secondStart, secondEnd := int64(15_000), int64(35_000)
	used := 10.0

	for _, window := range []WindowInput{
		{
			ProviderWindowID: "shared-window", WindowKind: "five_hour", WindowMode: "fixed",
			ModelScopeKind: "models", ModelScopeKey: "shared", ModelIDs: []string{"model-a"},
			Source: "response_header", SourceObservationID: "model-a", ObservedAtMS: 15_000,
			BoundaryAccuracy: "derived", CycleStartMS: &firstStart, CycleEndMS: &firstEnd,
			DurationSeconds: &duration, UsedPercent: &used,
		},
		{
			ProviderWindowID: "shared-window", WindowKind: "five_hour", WindowMode: "fixed",
			ModelScopeKind: "models", ModelScopeKey: "shared", ModelIDs: []string{"model-b"},
			Source: "response_header", SourceObservationID: "model-b", ObservedAtMS: 20_000,
			BoundaryAccuracy: "derived", CycleStartMS: &secondStart, CycleEndMS: &secondEnd,
			DurationSeconds: &duration, UsedPercent: &used,
		},
	} {
		if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
			Provider: "codex", Account: quotaSnapshotTestAccount(), Windows: []WindowInput{window},
		}}}); err != nil {
			t.Fatalf("write scoped header boundary: %v", err)
		}
	}

	result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-1", Provider: "codex", Account: quotaSnapshotTestAccount(),
	}}})
	if err != nil {
		t.Fatalf("query scoped header boundaries: %v", err)
	}
	for _, window := range result.Items[0].Windows {
		if len(window.ModelIDs) == 1 && window.ModelIDs[0] == "model-b" &&
			(window.CycleStartMS == nil || *window.CycleStartMS != secondStart ||
				window.CycleEndMS == nil || *window.CycleEndMS != secondEnd) {
			t.Fatalf("model-b boundary was copied from another scope: %#v", window)
		}
	}
}

func TestWriteUsageEventsKeepsFirstNonZeroAfterProvisionalBoundaryInCurrentCycle(t *testing.T) {
	const (
		firstObservedAtMS  = int64(1_785_928_574_638)
		secondObservedAtMS = int64(1_785_928_707_112)
		firstResetAtMS     = int64(1_788_520_573_000)
		secondResetAtMS    = int64(1_788_520_706_000)
	)
	service := newQuotaSnapshotTestService(t, secondObservedAtMS+1_000)
	windowMinutes := float64(30 * 24 * 60)
	resetAfter := float64(30 * 24 * 60 * 60)
	usedPercents := []float64{0, 1}
	observedAt := []int64{firstObservedAtMS, secondObservedAtMS}
	resetAt := []int64{firstResetAtMS, secondResetAtMS}
	for index := range observedAt {
		usedPercent := usedPercents[index]
		resetAfterSeconds := resetAfter
		event := usage.Event{
			TimestampMS:          observedAt[index],
			Provider:             "codex",
			Model:                "gpt-5.6-sol",
			AnalyticsModel:       "gpt-5.6-sol",
			AuthFileSnapshot:     "codex.json",
			AuthProviderSnapshot: "codex",
			AuthIndex:            "auth-1",
			AccountSnapshot:      "user@example.com",
			RequestID:            fmt.Sprintf("req-first-non-zero-%d", index+1),
			ResponseMetadata: &usage.ResponseHeaderMetadata{Quota: &usage.HeaderQuotaMetadata{
				PlanType: "free",
				Primary: &usage.HeaderQuotaWindow{
					UsedPercent:       &usedPercent,
					ResetAtMS:         resetAt[index],
					ResetAfterSeconds: &resetAfterSeconds,
					WindowMinutes:     &windowMinutes,
				},
			}},
		}
		if err := service.WriteUsageEvents(context.Background(), []usage.Event{event}); err != nil {
			t.Fatalf("write first non-zero quota evidence: %v", err)
		}
	}

	window := queryQuotaLifecycleWindows(t, service, false)["monthly"]
	expectedStartMS := firstResetAtMS - 30*quotaLifecycleDayMS
	if window.CurrentCycle == nil || window.PreviousCycle != nil ||
		window.CurrentCycle.ActualStartMS != expectedStartMS || window.UsedPercent == nil ||
		*window.UsedPercent != 1 || window.SourceObservationID != "req-first-non-zero-2" {
		t.Fatalf("first non-zero quota lifecycle = %#v", window)
	}
}

func TestWriteUsageEventsKeepsProvisionalZeroCodexBoundaryInOneCycle(t *testing.T) {
	const (
		firstObservedAtMS  = int64(1_785_928_574_638)
		secondObservedAtMS = int64(1_785_928_707_112)
		thirdObservedAtMS  = int64(1_785_928_787_294)
		firstResetAtMS     = int64(1_788_520_573_000)
		secondResetAtMS    = int64(1_788_520_706_000)
		thirdResetAtMS     = int64(1_788_520_580_000)
	)
	service := newQuotaSnapshotTestService(t, thirdObservedAtMS+1_000)
	usedPercent := 0.0
	windowMinutes := float64(30 * 24 * 60)
	resetAfterSeconds := []float64{2_592_000, 2_592_000, 2_591_796}
	observedAt := []int64{firstObservedAtMS, secondObservedAtMS, thirdObservedAtMS}
	resetAt := []int64{firstResetAtMS, secondResetAtMS, thirdResetAtMS}
	events := make([]usage.Event, 0, len(observedAt))
	for index := range observedAt {
		resetAfter := resetAfterSeconds[index]
		events = append(events, usage.Event{
			TimestampMS:          observedAt[index],
			Provider:             "codex",
			Model:                "gpt-5.6-sol",
			AnalyticsModel:       "gpt-5.6-sol",
			AuthFileSnapshot:     "codex.json",
			AuthProviderSnapshot: "codex",
			AuthIndex:            "auth-1",
			AccountSnapshot:      "user@example.com",
			RequestID:            fmt.Sprintf("req-provisional-%d", index+1),
			ResponseMetadata: &usage.ResponseHeaderMetadata{Quota: &usage.HeaderQuotaMetadata{
				PlanType: "free",
				Primary: &usage.HeaderQuotaWindow{
					UsedPercent:       &usedPercent,
					ResetAtMS:         resetAt[index],
					ResetAfterSeconds: &resetAfter,
					WindowMinutes:     &windowMinutes,
				},
			}},
		})
	}

	if err := service.WriteUsageEvents(context.Background(), events); err != nil {
		t.Fatalf("write provisional zero quota evidence: %v", err)
	}
	window := queryQuotaLifecycleWindows(t, service, false)["monthly"]
	expectedStartMS := firstResetAtMS - 30*quotaLifecycleDayMS
	if window.CurrentCycle == nil || window.PreviousCycle != nil ||
		window.CurrentCycle.ActualStartMS != expectedStartMS || window.CycleStartMS == nil ||
		*window.CycleStartMS != expectedStartMS || window.LastSeenAtMS != thirdObservedAtMS ||
		window.SourceObservationID != "req-provisional-3" {
		t.Fatalf("provisional zero quota lifecycle = %#v", window)
	}
}

func TestWriteUsageEventsSkipsZeroOnlyCodexHeaderPlaceholder(t *testing.T) {
	const observedAtMS = int64(1_785_928_574_638)
	service := newQuotaSnapshotTestService(t, observedAtMS+1_000)
	zero := 0.0
	monthlyMinutes := float64(30 * 24 * 60)
	monthlySeconds := float64(30 * 24 * 60 * 60)
	monthlyResetAtMS := observedAtMS + int64(monthlySeconds*1000)
	event := usage.Event{
		TimestampMS:          observedAtMS,
		Provider:             "codex",
		Model:                "gpt-5.6-sol",
		AnalyticsModel:       "gpt-5.6-sol",
		AuthFileSnapshot:     "codex.json",
		AuthProviderSnapshot: "codex",
		AuthIndex:            "auth-1",
		AccountSnapshot:      "user@example.com",
		RequestID:            "req-zero-placeholder",
		ResponseMetadata: &usage.ResponseHeaderMetadata{Quota: &usage.HeaderQuotaMetadata{
			Primary: &usage.HeaderQuotaWindow{
				UsedPercent: &zero, ResetAtMS: monthlyResetAtMS,
				ResetAfterSeconds: &monthlySeconds, WindowMinutes: &monthlyMinutes,
			},
			Secondary: &usage.HeaderQuotaWindow{
				UsedPercent: &zero, ResetAfterSeconds: &zero, WindowMinutes: &zero,
			},
		}},
	}
	if err := service.WriteUsageEvents(context.Background(), []usage.Event{event}); err != nil {
		t.Fatalf("write usage evidence: %v", err)
	}
	result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-1", Provider: "codex", Account: quotaSnapshotTestAccount(),
	}}})
	if err != nil {
		t.Fatalf("query usage evidence: %v", err)
	}
	if len(result.Items) != 1 || len(result.Items[0].Windows) != 1 || result.Items[0].Windows[0].ProviderWindowID != "monthly" {
		t.Fatalf("zero-only secondary placeholder was persisted: %#v", result)
	}

	legacyAccount := quotaSnapshotTestAccount()
	legacyAccount.AuthIndex = "auth-legacy-placeholder"
	remaining := 100.0
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
		Provider: "codex", Account: legacyAccount, Windows: []WindowInput{{
			ProviderWindowID: "secondary", WindowKind: "unknown", WindowMode: "unknown",
			ModelScopeKind: "all", Source: "response_header", ObservedAtMS: observedAtMS,
			BoundaryAccuracy: "unknown", UsedPercent: &zero, RemainingPercent: &remaining,
		}},
	}}}); err != nil {
		t.Fatalf("seed legacy placeholder: %v", err)
	}
	legacyResult, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-legacy", Provider: "codex", Account: legacyAccount,
	}}})
	if err != nil {
		t.Fatalf("query legacy placeholder: %v", err)
	}
	if len(legacyResult.Items) != 1 || len(legacyResult.Items[0].Windows) != 0 {
		t.Fatalf("legacy zero-only placeholder remained visible: %#v", legacyResult)
	}
}

func TestWriteUsageEventsPersistsOnlyExplicitXAIProviderUsage(t *testing.T) {
	const observedAtMS = int64(1_780_000_000_000)
	service := newQuotaSnapshotTestService(t, observedAtMS+1_000)
	actual := int64(1_000_000)
	limit := int64(1_000_000)
	remaining := int64(0)
	event := usage.Event{
		TimestampMS:          observedAtMS,
		Provider:             "xai",
		AuthFileSnapshot:     "xai.json",
		AuthProviderSnapshot: "xai",
		AuthIndex:            "auth-xai",
		RequestID:            "req-xai-body",
		ResponseMetadata: &usage.ResponseHeaderMetadata{
			RateLimit: &usage.HeaderRateLimitMetadata{Requests: &usage.HeaderRateLimitBucket{}},
			ProviderUsage: &usage.ProviderUsageMetadata{
				Provider: "xai", Kind: usage.ProviderUsageKindIncludedFree,
				WindowKind: usage.ProviderUsageWindowRolling24H,
				Source:     usage.ProviderUsageSourceBody, Model: "grok-4.5-build-free",
				ObservedAtMS: observedAtMS, Actual: &actual, Limit: &limit, Remaining: &remaining,
			},
		},
	}
	if err := service.WriteUsageEvents(context.Background(), []usage.Event{event}); err != nil {
		t.Fatalf("write xai evidence: %v", err)
	}
	result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-xai", Provider: "xai", Account: AccountTarget{
			AuthFileSnapshot: "xai.json", AuthProviderSnapshot: "xai", AuthIndex: "auth-xai",
		},
	}}})
	if err != nil {
		t.Fatalf("query xai evidence: %v", err)
	}
	if len(result.Items) != 1 || len(result.Items[0].Windows) != 1 {
		t.Fatalf("query result = %#v", result)
	}
	window := result.Items[0].Windows[0]
	if window.WindowMode != "rolling" || window.ProviderWindowID != "included-free-rolling-24h" || window.DurationSeconds == nil || *window.DurationSeconds != 86_400 {
		t.Fatalf("xai window = %#v", window)
	}
	if window.ModelScopeKind != "models" || len(window.ModelIDs) != 1 || window.ModelIDs[0] != "grok-4.5-build-free" {
		t.Fatalf("xai model scope = %#v", window)
	}

	transportOnly := event
	transportOnly.AuthIndex = "auth-transport-only"
	transportOnly.ResponseMetadata = &usage.ResponseHeaderMetadata{
		RateLimit: &usage.HeaderRateLimitMetadata{Requests: &usage.HeaderRateLimitBucket{}},
	}
	if err := service.WriteUsageEvents(context.Background(), []usage.Event{transportOnly}); err != nil {
		t.Fatalf("write transport-only evidence: %v", err)
	}
	transportResult, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-transport", Provider: "xai", Account: AccountTarget{
			AuthFileSnapshot: "xai.json", AuthProviderSnapshot: "xai", AuthIndex: "auth-transport-only",
		},
	}}})
	if err != nil {
		t.Fatalf("query transport-only evidence: %v", err)
	}
	if len(transportResult.Items[0].Windows) != 0 {
		t.Fatalf("transport rate-limit headers became quota snapshots: %#v", transportResult)
	}
}

func TestWriteCodexInspectionResultRequiresNormalizedResetBoundary(t *testing.T) {
	const observedAtMS = int64(1_780_000_000_000)
	service := newQuotaSnapshotTestService(t, observedAtMS+1_000)
	duration := float64(18_000)
	used := 60.0
	result := model.CodexInspectionResult{
		ID: 7, RunID: 3, Provider: "codex", FileName: "codex.json", AuthIndex: "auth-1",
		AccountSnapshot: "user@example.com", CreatedAtMS: observedAtMS, PlanType: "plus",
		QuotaWindows: []model.CodexInspectionQuotaWindow{
			{ID: "five-hour", UsedPercent: &used, ResetLabel: "08/04 12:00", LimitWindowSeconds: &duration},
			{ID: "weekly", UsedPercent: &used, ResetAtMS: observedAtMS + 604_800_000, ResetAccuracy: "exact", LimitWindowSeconds: float64Pointer(604_800)},
			{ID: "monthly", UsedPercent: &used, ResetAtMS: observedAtMS + 2_592_000_000, ResetAccuracy: "estimated", LimitWindowSeconds: float64Pointer(2_592_000)},
		},
	}
	if err := service.WriteCodexInspectionResult(context.Background(), result); err != nil {
		t.Fatalf("write inspection evidence: %v", err)
	}
	query, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-1", Provider: "codex", Account: quotaSnapshotTestAccount(),
	}}})
	if err != nil {
		t.Fatalf("query inspection evidence: %v", err)
	}
	if len(query.Items[0].Windows) != 3 {
		t.Fatalf("inspection windows = %#v", query)
	}
	byID := map[string]Window{}
	for _, window := range query.Items[0].Windows {
		byID[window.ProviderWindowID] = window
	}
	if byID["five-hour"].WindowMode != "unknown" || byID["five-hour"].BoundaryAccuracy != "unknown" {
		t.Fatalf("label-only boundary was trusted: %#v", byID["five-hour"])
	}
	if byID["weekly"].WindowMode != "fixed" || byID["weekly"].BoundaryAccuracy != "exact" {
		t.Fatalf("normalized boundary was not trusted: %#v", byID["weekly"])
	}
	if byID["monthly"].WindowMode != "fixed" || byID["monthly"].BoundaryAccuracy != "derived" {
		t.Fatalf("estimated reset was not normalized into a derived boundary: %#v", byID["monthly"])
	}
}

func TestWriteCodexInspectionResultReclassifiesLegacyScopedAllScope(t *testing.T) {
	const observedAtMS = int64(1_780_000_000_000)
	service := newQuotaSnapshotTestService(t, observedAtMS+1_000)
	statusCode := 200
	duration := float64(7 * 24 * 60 * 60)
	used := 0.0
	result := model.CodexInspectionResult{
		ID: 8, RunID: 4, Provider: "codex", FileName: "codex.json", AuthIndex: "auth-1",
		AccountSnapshot: "user@example.com", CreatedAtMS: observedAtMS, PlanType: "plus",
		StatusCode: &statusCode, QuotaInventoryObserved: true,
		QuotaWindows: []model.CodexInspectionQuotaWindow{{
			ID: "gpt-5-3-codex-spark-weekly-0", UsedPercent: &used,
			ResetAtMS: observedAtMS + 604_800_000, ResetAccuracy: "exact",
			LimitWindowSeconds: &duration,
			ModelScope:         &model.CodexInspectionQuotaModelScope{Kind: "all", Complete: true},
		}},
	}
	if err := service.WriteCodexInspectionResult(context.Background(), result); err != nil {
		t.Fatalf("write legacy scoped inspection evidence: %v", err)
	}
	query, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-legacy-scoped", Provider: "codex", Account: quotaSnapshotTestAccount(),
	}}})
	if err != nil {
		t.Fatalf("query legacy scoped inspection evidence: %v", err)
	}
	if len(query.Items) != 1 || len(query.Items[0].Windows) != 1 {
		t.Fatalf("legacy scoped inspection windows = %#v", query)
	}
	window := query.Items[0].Windows[0]
	if window.ProviderWindowID != "spark-weekly-0" || window.ModelScopeKind != "models" ||
		len(window.ModelIDs) != 1 || window.ModelIDs[0] != codexquota.SparkModelID {
		t.Fatalf("legacy scoped inspection scope = %#v", window)
	}
}

func TestWriteCodexInspectionResultPersistsCompleteEmptyInventory(t *testing.T) {
	const observedAtMS = int64(1_780_000_000_000)
	service := newQuotaSnapshotTestService(t, observedAtMS+24*60*60*1000)
	duration := float64(18_000)
	used := 25.0
	base := model.CodexInspectionResult{
		Provider: "codex", FileName: "codex.json", AuthIndex: "auth-1",
		AccountSnapshot: "user@example.com", PlanType: "plus",
	}
	okStatus := http.StatusOK
	initial := base
	initial.ID = 1
	initial.RunID = 1
	initial.CreatedAtMS = observedAtMS
	initial.StatusCode = &okStatus
	initial.QuotaWindows = []model.CodexInspectionQuotaWindow{{
		ID: "five-hour", UsedPercent: &used, ResetAtMS: observedAtMS + 18_000_000,
		ResetAccuracy: "exact", LimitWindowSeconds: &duration,
	}}
	if err := service.WriteCodexInspectionResult(context.Background(), initial); err != nil {
		t.Fatalf("write initial inspection inventory: %v", err)
	}

	failedEmpty := base
	failedEmpty.ID = 2
	failedEmpty.RunID = 2
	failedEmpty.CreatedAtMS = observedAtMS + 500
	failedEmpty.Error = "temporary request failure"
	failedEmpty.ErrorKind = "request_error"
	if err := service.WriteCodexInspectionResult(context.Background(), failedEmpty); err != nil {
		t.Fatalf("write failed empty inspection inventory: %v", err)
	}
	if active := queryQuotaLifecycleWindows(t, service, false)["five-hour"]; active.Availability != "active" {
		t.Fatalf("failed empty inspection changed availability = %#v", active)
	}

	firstEmpty := base
	firstEmpty.ID = 3
	firstEmpty.RunID = 3
	firstEmpty.CreatedAtMS = observedAtMS + 1_000
	firstEmpty.StatusCode = &okStatus
	firstEmpty.QuotaWindowsJSON = "[]"
	if err := service.WriteCodexInspectionResult(context.Background(), firstEmpty); err != nil {
		t.Fatalf("write first empty inspection inventory: %v", err)
	}
	pending := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if pending.Availability != "pending_absent" {
		t.Fatalf("first empty inspection availability = %#v", pending)
	}

	secondEmpty := base
	secondEmpty.ID = 4
	secondEmpty.RunID = 4
	secondEmpty.CreatedAtMS = observedAtMS + 2_000
	secondEmpty.QuotaWindowsJSON = "[]"
	secondEmpty.StatusCode = &okStatus
	if err := service.WriteCodexInspectionResult(context.Background(), secondEmpty); err != nil {
		t.Fatalf("write second empty inspection inventory: %v", err)
	}
	if windows := queryQuotaLifecycleWindows(t, service, false); len(windows) != 0 {
		t.Fatalf("confirmed empty inspection inventory = %#v", windows)
	}
}

func TestWriteCodexInspectionResultKeepsFailedPartialInventoryActive(t *testing.T) {
	const observedAtMS = int64(1_780_000_000_000)
	service, path := newQuotaSnapshotTestServiceWithPath(t, observedAtMS+24*60*60*1000)
	fiveHourDuration := float64(5 * 60 * 60)
	weeklyDuration := float64(7 * 24 * 60 * 60)
	fiveHourUsed := 20.0
	weeklyUsed := 30.0
	okStatus := http.StatusOK
	failedStatus := http.StatusInternalServerError

	initial := model.CodexInspectionResult{
		ID: 1, RunID: 1, Provider: "codex", FileName: "codex.json", AuthIndex: "auth-1",
		AccountSnapshot: "user@example.com", CreatedAtMS: observedAtMS, StatusCode: &okStatus,
		QuotaWindows: []model.CodexInspectionQuotaWindow{
			{
				ID: "five-hour", UsedPercent: &fiveHourUsed, ResetAtMS: observedAtMS + int64(fiveHourDuration)*1000,
				ResetAccuracy: "exact", LimitWindowSeconds: &fiveHourDuration,
			},
			{
				ID: "weekly", UsedPercent: &weeklyUsed, ResetAtMS: observedAtMS + int64(weeklyDuration)*1000,
				ResetAccuracy: "exact", LimitWindowSeconds: &weeklyDuration,
			},
		},
	}
	if err := service.WriteCodexInspectionResult(context.Background(), initial); err != nil {
		t.Fatalf("write initial inspection inventory: %v", err)
	}

	for index := 1; index <= 2; index++ {
		failed := initial
		failed.ID = int64(index + 1)
		failed.RunID = int64(index + 1)
		failed.CreatedAtMS = observedAtMS + int64(index)*1_000
		failed.StatusCode = &failedStatus
		failed.ErrorKind = "http_status"
		failed.QuotaWindows = []model.CodexInspectionQuotaWindow{initial.QuotaWindows[1]}
		if err := service.WriteCodexInspectionResult(context.Background(), failed); err != nil {
			t.Fatalf("write failed partial inspection inventory %d: %v", index, err)
		}
	}

	windows := queryQuotaLifecycleWindows(t, service, false)
	if windows["five-hour"].Availability != "active" || windows["weekly"].Availability != "active" {
		t.Fatalf("failed partial inspections changed lifecycle availability = %#v", windows)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open failed-inspection test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var partialCount int
	if err := db.QueryRow(`select count(*) from account_quota_observations
		where source = 'inspection' and inventory_mode = 'partial' and observed_at_ms > ?`,
		observedAtMS,
	).Scan(&partialCount); err != nil {
		t.Fatalf("count failed partial inspection observations: %v", err)
	}
	if partialCount != 2 {
		t.Fatalf("failed partial inspection observation count = %d, want 2", partialCount)
	}
}

func TestWriteCodexInspectionResultKeepsSuccessfulSupplementalInventoryPartial(t *testing.T) {
	const observedAtMS = int64(1_780_000_000_000)
	service := newQuotaSnapshotTestService(t, observedAtMS+24*60*60*1000)
	fiveHourDuration := float64(5 * 60 * 60)
	weeklyDuration := float64(7 * 24 * 60 * 60)
	used := 20.0
	okStatus := http.StatusOK
	initial := model.CodexInspectionResult{
		ID: 1, RunID: 1, Provider: "codex", FileName: "codex.json", AuthIndex: "auth-1",
		AccountSnapshot: "user@example.com", CreatedAtMS: observedAtMS, StatusCode: &okStatus,
		QuotaInventoryObserved: true,
		QuotaWindows: []model.CodexInspectionQuotaWindow{
			{
				ID: "five-hour", UsedPercent: &used,
				ResetAtMS:     observedAtMS + int64(fiveHourDuration)*1000,
				ResetAccuracy: "exact", LimitWindowSeconds: &fiveHourDuration,
			},
			{
				ID: "weekly", UsedPercent: &used,
				ResetAtMS:     observedAtMS + int64(weeklyDuration)*1000,
				ResetAccuracy: "exact", LimitWindowSeconds: &weeklyDuration,
			},
		},
	}
	if err := service.WriteCodexInspectionResult(context.Background(), initial); err != nil {
		t.Fatalf("write complete primary inspection inventory: %v", err)
	}

	for index := 1; index <= 2; index++ {
		partial := model.CodexInspectionResult{
			ID: int64(index + 1), RunID: int64(index + 1), Provider: "codex",
			FileName: "codex.json", AuthIndex: "auth-1", AccountSnapshot: "user@example.com",
			CreatedAtMS: observedAtMS + int64(index)*1_000, StatusCode: &okStatus,
			QuotaWindows: []model.CodexInspectionQuotaWindow{{
				ID: "code-review-five-hour", UsedPercent: &used,
				ResetAtMS:     observedAtMS + int64(fiveHourDuration)*1000,
				ResetAccuracy: "exact", LimitWindowSeconds: &fiveHourDuration,
			}},
		}
		if err := service.WriteCodexInspectionResult(context.Background(), partial); err != nil {
			t.Fatalf("write successful supplemental inspection inventory %d: %v", index, err)
		}
	}

	windows := queryQuotaLifecycleWindows(t, service, false)
	for _, id := range []string{"five-hour", "weekly", "code-review-five-hour"} {
		if window, ok := windows[id]; !ok || window.Availability != "active" {
			t.Fatalf("successful supplemental inspection changed %s lifecycle: %#v", id, windows)
		}
	}
}

func float64Pointer(value float64) *float64 {
	return &value
}

const (
	quotaLifecycleBaseMS = int64(1_800_000_000_000)
	quotaLifecycleHourMS = int64(60 * 60 * 1000)
	quotaLifecycleDayMS  = int64(24 * 60 * 60 * 1000)
)

func TestQuotaLifecycleOrdersBatchAndKeepsReplayedObservationHistorical(t *testing.T) {
	service, path := newQuotaSnapshotTestServiceWithPath(t, quotaLifecycleBaseMS+10*quotaLifecycleDayMS)
	olderStartMS := quotaLifecycleBaseMS - 7*quotaLifecycleDayMS
	older := quotaLifecycleFixedWindow("weekly", "weekly", olderStartMS, 7*24*60*60, 80)
	current := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
	olderObservedAtMS := quotaLifecycleBaseMS + quotaLifecycleHourMS
	currentObservedAtMS := quotaLifecycleBaseMS + 4*quotaLifecycleHourMS

	_, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntry("complete", currentObservedAtMS, []WindowInput{current}),
		quotaLifecycleWriteEntry("complete", olderObservedAtMS, []WindowInput{older}),
	}})
	if err != nil {
		t.Fatalf("write out-of-order quota batch: %v", err)
	}
	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.LastSeenAtMS != currentObservedAtMS || window.CurrentCycle == nil ||
		window.CurrentCycle.ActualStartMS != quotaLifecycleBaseMS || window.UsedPercent == nil ||
		*window.UsedPercent != 20 {
		t.Fatalf("ordered quota lifecycle = %#v", window)
	}

	replayedStartMS := quotaLifecycleBaseMS - 14*quotaLifecycleDayMS
	replayed := quotaLifecycleFixedWindow("weekly", "weekly", replayedStartMS, 7*24*60*60, 95)
	replayedObservedAtMS := quotaLifecycleBaseMS + 2*quotaLifecycleHourMS
	writeQuotaLifecycleObservation(t, service, "complete", replayedObservedAtMS, []WindowInput{replayed})

	window = queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.LastSeenAtMS != currentObservedAtMS || window.CurrentCycle == nil ||
		window.CurrentCycle.ActualStartMS != quotaLifecycleBaseMS || window.UsedPercent == nil ||
		*window.UsedPercent != 20 || window.ActivationGeneration != 1 {
		t.Fatalf("replayed quota observation changed current lifecycle = %#v", window)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open quota lifecycle test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var lifecycleApplied int
	var logicalWindowID sql.NullInt64
	if err := db.QueryRow(`select o.lifecycle_applied, s.logical_window_id
		from account_quota_observations o
		join account_quota_snapshots s on s.observation_id = o.id
		where o.source_observation_id = ?`,
		fmt.Sprintf("observation-%d", replayedObservedAtMS),
	).Scan(&lifecycleApplied, &logicalWindowID); err != nil {
		t.Fatalf("read replayed quota evidence: %v", err)
	}
	if lifecycleApplied != 0 || logicalWindowID.Valid {
		t.Fatalf("replayed quota evidence lifecycle_applied=%d logical_window_id=%#v", lifecycleApplied, logicalWindowID)
	}
}

func TestQuotaLifecycleKeepsNewerCompleteStateWhenOlderHeaderUsesImplicitObservation(t *testing.T) {
	service, path := newQuotaSnapshotTestServiceWithPath(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	newerObservedAtMS := quotaLifecycleBaseMS + 2*quotaLifecycleHourMS
	olderObservedAtMS := quotaLifecycleBaseMS + quotaLifecycleHourMS
	newer := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
	older := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 80)

	_, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "inspection", "inspection-newer", "codex:rate-limits", newerObservedAtMS, []WindowInput{newer},
		),
	}})
	if err != nil {
		t.Fatalf("write newer complete quota observation: %v", err)
	}
	older.Source = "response_header"
	older.SourceObservationID = "header-older"
	older.ObservedAtMS = olderObservedAtMS
	_, err = service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
		RowKey: "row-lifecycle", Provider: "codex", Account: quotaSnapshotTestAccount(),
		Windows: []WindowInput{older},
	}}})
	if err != nil {
		t.Fatalf("write older implicit header observation: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.LastSeenAtMS != newerObservedAtMS || window.UsedPercent == nil || *window.UsedPercent != 20 {
		t.Fatalf("older implicit header observation changed lifecycle = %#v", window)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open implicit-header test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var inventoryScopeKey string
	var lifecycleApplied int
	if err := db.QueryRow(`select inventory_scope_key, lifecycle_applied
		from account_quota_observations where source_observation_id = 'header-older'`).Scan(
		&inventoryScopeKey,
		&lifecycleApplied,
	); err != nil {
		t.Fatalf("read implicit header observation: %v", err)
	}
	if inventoryScopeKey != "codex:rate-limits" || lifecycleApplied != 0 {
		t.Fatalf("implicit header observation scope=%q lifecycle_applied=%d", inventoryScopeKey, lifecycleApplied)
	}
}

func TestQuotaLifecycleSameTimestampAuthorityIsRequestOrderIndependent(t *testing.T) {
	orders := []struct {
		name    string
		entries []WriteEntry
	}{
		{name: "low-then-high"},
		{name: "high-then-low"},
	}
	for _, testCase := range orders {
		t.Run(testCase.name, func(t *testing.T) {
			service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
			observedAtMS := quotaLifecycleBaseMS + quotaLifecycleHourMS
			low := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 80)
			high := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
			lowEntry := quotaLifecycleWriteEntryWithObservation(
				"partial", "response_header", "header-same-ms", "codex:rate-limits", observedAtMS, []WindowInput{low},
			)
			highEntry := quotaLifecycleWriteEntryWithObservation(
				"complete", "inspection", "inspection-same-ms", "codex:rate-limits", observedAtMS, []WindowInput{high},
			)
			entries := []WriteEntry{lowEntry, highEntry}
			if testCase.name == "high-then-low" {
				entries = []WriteEntry{highEntry, lowEntry}
			}
			for _, entry := range entries {
				if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{entry}}); err != nil {
					t.Fatalf("write same-timestamp observation: %v", err)
				}
			}

			window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
			if window.Source != "inspection" || window.UsedPercent == nil || *window.UsedPercent != 20 {
				t.Fatalf("same-timestamp authority result = %#v", window)
			}
		})
	}
}

func TestQuotaLifecycleSameTimestampHigherAuthorityDoesNotCreateFalseReactivation(t *testing.T) {
	for _, order := range []string{"low-then-high", "high-then-low"} {
		t.Run(order, func(t *testing.T) {
			service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
			weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
			writeQuotaLifecycleObservation(
				t,
				service,
				"complete",
				quotaLifecycleBaseMS+quotaLifecycleHourMS,
				[]WindowInput{weekly},
			)

			observedAtMS := quotaLifecycleBaseMS + 2*quotaLifecycleHourMS
			low := quotaLifecycleWriteEntryWithObservation(
				"delta",
				"response_header",
				"header-remove-same-ms",
				"codex:quota-windows",
				observedAtMS,
				nil,
			)
			low.RemovedWindows = []RemovedWindowInput{{
				ProviderWindowID: "weekly",
				ModelScopeKind:   "all",
			}}
			high := quotaLifecycleWriteEntryWithObservation(
				"complete",
				"inspection",
				"inspection-present-same-ms",
				"codex:quota-windows",
				observedAtMS,
				[]WindowInput{weekly},
			)
			entries := []WriteEntry{low, high}
			if order == "high-then-low" {
				entries = []WriteEntry{high, low}
			}
			for _, entry := range entries {
				if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{entry}}); err != nil {
					t.Fatalf("write same-timestamp lifecycle observation: %v", err)
				}
			}

			window := queryQuotaLifecycleWindows(t, service, true)["weekly"]
			if window.Availability != "active" || window.ActivationGeneration != 1 ||
				window.DeactivatedAtMS != nil || window.CurrentCycle == nil || window.PreviousCycle != nil ||
				window.CurrentCycle.ActualStartMS != quotaLifecycleBaseMS {
				t.Fatalf("same-timestamp authority created a false reactivation: %#v", window)
			}
		})
	}
}

func TestQuotaLifecycleSameTimestampHigherAuthorityDeltaRestoresContainerRelationship(t *testing.T) {
	for _, order := range []string{"low-then-high", "high-then-low"} {
		t.Run(order, func(t *testing.T) {
			service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
			weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
			fiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", quotaLifecycleBaseMS, 5*60*60, 10)
			fiveHour.RelationshipKind = "concurrent_subwindow"
			fiveHour.ContainerWindowID = "weekly"
			writeQuotaLifecycleObservation(
				t,
				service,
				"complete",
				quotaLifecycleBaseMS+quotaLifecycleHourMS,
				[]WindowInput{fiveHour, weekly},
			)

			observedAtMS := quotaLifecycleBaseMS + 2*quotaLifecycleHourMS
			low := quotaLifecycleWriteEntryWithObservation(
				"delta",
				"response_header",
				"header-remove-before-delta-restore",
				"codex:quota-windows",
				observedAtMS,
				nil,
			)
			low.RemovedWindows = []RemovedWindowInput{{
				ProviderWindowID: "weekly",
				ModelScopeKind:   "all",
			}}
			high := quotaLifecycleWriteEntryWithObservation(
				"delta",
				"inspection",
				"inspection-present-delta-same-ms",
				"codex:quota-windows",
				observedAtMS,
				[]WindowInput{weekly},
			)
			entries := []WriteEntry{low, high}
			if order == "high-then-low" {
				entries = []WriteEntry{high, low}
			}
			for _, entry := range entries {
				if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{entry}}); err != nil {
					t.Fatalf("write same-timestamp delta restoration: %v", err)
				}
			}

			windows := queryQuotaLifecycleWindows(t, service, true)
			parent := windows["weekly"]
			child := windows["five-hour"]
			if parent.Availability != "active" || parent.ActivationGeneration != 1 ||
				parent.DeactivatedAtMS != nil || parent.CurrentCycle == nil || parent.PreviousCycle != nil {
				t.Fatalf("same-timestamp delta restoration lifecycle = %#v", parent)
			}
			if child.RelationshipKind != "concurrent_subwindow" || child.ContainerWindowID != "weekly" ||
				child.CurrentCycle == nil || child.CurrentCycle.ParentCycleID == nil ||
				*child.CurrentCycle.ParentCycleID != parent.CurrentCycle.ID {
				t.Fatalf("same-timestamp delta restoration relationship: child=%#v parent=%#v", child, parent)
			}
		})
	}
}

func TestQuotaLifecycleSameTimestampCompleteOmissionSupersedesLowerDeltaRemoval(t *testing.T) {
	for _, order := range []string{"low-then-high", "high-then-low"} {
		t.Run(order, func(t *testing.T) {
			service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
			weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
			fiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", quotaLifecycleBaseMS, 5*60*60, 10)
			fiveHour.RelationshipKind = "concurrent_subwindow"
			fiveHour.ContainerWindowID = "weekly"
			writeQuotaLifecycleObservation(
				t,
				service,
				"complete",
				quotaLifecycleBaseMS+quotaLifecycleHourMS,
				[]WindowInput{fiveHour, weekly},
			)

			observedAtMS := quotaLifecycleBaseMS + 2*quotaLifecycleHourMS
			low := quotaLifecycleWriteEntryWithObservation(
				"delta",
				"response_header",
				"header-remove-before-omission",
				"codex:quota-windows",
				observedAtMS,
				nil,
			)
			low.RemovedWindows = []RemovedWindowInput{{
				ProviderWindowID: "weekly",
				ModelScopeKind:   "all",
			}}
			childWithoutRelationship := fiveHour
			childWithoutRelationship.RelationshipKind = ""
			childWithoutRelationship.ContainerWindowID = ""
			high := quotaLifecycleWriteEntryWithObservation(
				"complete",
				"inspection",
				"inspection-omission-same-ms",
				"codex:quota-windows",
				observedAtMS,
				[]WindowInput{childWithoutRelationship},
			)
			entries := []WriteEntry{low, high}
			if order == "high-then-low" {
				entries = []WriteEntry{high, low}
			}
			for _, entry := range entries {
				if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{entry}}); err != nil {
					t.Fatalf("write same-timestamp omission observation: %v", err)
				}
			}

			windows := queryQuotaLifecycleWindows(t, service, true)
			parent := windows["weekly"]
			child := windows["five-hour"]
			if parent.Availability != "pending_absent" || parent.ActivationGeneration != 1 ||
				parent.MissingSinceMS == nil || *parent.MissingSinceMS != observedAtMS ||
				parent.DeactivatedAtMS != nil || parent.CurrentCycle == nil || parent.PreviousCycle != nil {
				t.Fatalf("same-timestamp complete omission lifecycle = %#v", parent)
			}
			if child.RelationshipKind != "concurrent_subwindow" || child.ContainerWindowID != "weekly" ||
				child.CurrentCycle == nil || parent.CurrentCycle == nil || child.CurrentCycle.ParentCycleID == nil ||
				*child.CurrentCycle.ParentCycleID != parent.CurrentCycle.ID {
				t.Fatalf("same-timestamp complete omission relationship: child=%#v parent=%#v", child, parent)
			}
		})
	}
}

func TestQuotaLifecycleSameTimestampOmissionKeepsEarlierAbsenceConfirmation(t *testing.T) {
	for _, order := range []string{"low-then-high", "high-then-low"} {
		t.Run(order, func(t *testing.T) {
			service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
			weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
			writeQuotaLifecycleObservation(
				t,
				service,
				"complete",
				quotaLifecycleBaseMS+quotaLifecycleHourMS,
				[]WindowInput{weekly},
			)
			firstMissingAtMS := quotaLifecycleBaseMS + 2*quotaLifecycleHourMS
			writeQuotaLifecycleObservation(t, service, "complete", firstMissingAtMS, nil)

			confirmedAtMS := quotaLifecycleBaseMS + 3*quotaLifecycleHourMS
			low := quotaLifecycleWriteEntryWithObservation(
				"delta",
				"response_header",
				"header-remove-after-missing",
				"codex:quota-windows",
				confirmedAtMS,
				nil,
			)
			low.RemovedWindows = []RemovedWindowInput{{
				ProviderWindowID: "weekly",
				ModelScopeKind:   "all",
			}}
			high := quotaLifecycleWriteEntryWithObservation(
				"complete",
				"inspection",
				"inspection-confirm-after-missing",
				"codex:quota-windows",
				confirmedAtMS,
				nil,
			)
			entries := []WriteEntry{low, high}
			if order == "high-then-low" {
				entries = []WriteEntry{high, low}
			}
			for _, entry := range entries {
				if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{entry}}); err != nil {
					t.Fatalf("write same-timestamp confirmed omission: %v", err)
				}
			}

			window := queryQuotaLifecycleWindows(t, service, true)["weekly"]
			if window.Availability != "inactive" || window.ActivationGeneration != 1 ||
				window.MissingSinceMS == nil || *window.MissingSinceMS != firstMissingAtMS ||
				window.DeactivatedAtMS == nil || *window.DeactivatedAtMS != firstMissingAtMS ||
				window.CurrentCycle != nil {
				t.Fatalf("same-timestamp confirmed omission lost prior absence: %#v", window)
			}
		})
	}
}

func TestQuotaLifecycleCountsCompleteOmissionsOncePerTimestamp(t *testing.T) {
	orders := []struct {
		name         string
		firstSource  string
		firstID      string
		secondSource string
		secondID     string
	}{
		{
			name: "low-then-high", firstSource: "api_query", firstID: "api-missing",
			secondSource: "inspection", secondID: "inspection-missing",
		},
		{
			name: "high-then-low", firstSource: "inspection", firstID: "inspection-missing",
			secondSource: "api_query", secondID: "api-missing",
		},
	}
	for _, testCase := range orders {
		t.Run(testCase.name, func(t *testing.T) {
			service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
			weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
			presentAtMS := quotaLifecycleBaseMS + quotaLifecycleHourMS
			missingAtMS := quotaLifecycleBaseMS + 2*quotaLifecycleHourMS
			confirmedAtMS := quotaLifecycleBaseMS + 3*quotaLifecycleHourMS

			entries := []WriteEntry{
				quotaLifecycleWriteEntryWithObservation(
					"complete", "inspection", "inspection-present", "codex:rate-limits", presentAtMS, []WindowInput{weekly},
				),
				quotaLifecycleWriteEntryWithObservation(
					"complete", testCase.firstSource, testCase.firstID, "codex:rate-limits", missingAtMS, nil,
				),
				quotaLifecycleWriteEntryWithObservation(
					"complete", testCase.secondSource, testCase.secondID, "codex:rate-limits", missingAtMS, nil,
				),
			}
			for _, entry := range entries {
				if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{entry}}); err != nil {
					t.Fatalf("write quota lifecycle observation: %v", err)
				}
			}

			pending := queryQuotaLifecycleWindows(t, service, true)["weekly"]
			if pending.Availability != "pending_absent" || pending.MissingSinceMS == nil ||
				*pending.MissingSinceMS != missingAtMS || pending.DeactivatedAtMS != nil {
				t.Fatalf("same-timestamp omissions retired window = %#v", pending)
			}

			if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
				quotaLifecycleWriteEntryWithObservation(
					"complete", "inspection", "inspection-confirmed", "codex:rate-limits", confirmedAtMS, nil,
				),
			}}); err != nil {
				t.Fatalf("confirm quota window omission: %v", err)
			}
			inactive := queryQuotaLifecycleWindows(t, service, true)["weekly"]
			if inactive.Availability != "inactive" || inactive.DeactivatedAtMS == nil ||
				*inactive.DeactivatedAtMS != missingAtMS {
				t.Fatalf("distinct-timestamp omission did not retire window = %#v", inactive)
			}
		})
	}
}

func TestQuotaLifecyclePartialCrossScopeDoesNotMoveInventoryOwnershipOrTime(t *testing.T) {
	service, path := newQuotaSnapshotTestServiceWithPath(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	completeObservedAtMS := quotaLifecycleBaseMS + 2*quotaLifecycleHourMS
	partialObservedAtMS := quotaLifecycleBaseMS + 3*quotaLifecycleHourMS
	complete := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
	partial := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS+quotaLifecycleDayMS, 7*24*60*60, 80)

	for _, entry := range []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "inspection", "inventory-owner", "codex:rate-limits", completeObservedAtMS, []WindowInput{complete},
		),
		quotaLifecycleWriteEntryWithObservation(
			"partial", "response_header", "cross-scope-partial", "codex:legacy-header", partialObservedAtMS, []WindowInput{partial},
		),
	} {
		if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{entry}}); err != nil {
			t.Fatalf("write cross-scope lifecycle observation: %v", err)
		}
	}

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.LastSeenAtMS != completeObservedAtMS || window.UsedPercent == nil || *window.UsedPercent != 20 ||
		window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != quotaLifecycleBaseMS ||
		window.CurrentCycle.ScheduledEndMS == nil ||
		*window.CurrentCycle.ScheduledEndMS != quotaLifecycleBaseMS+7*quotaLifecycleDayMS {
		t.Fatalf("cross-scope partial changed current lifecycle = %#v", window)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open cross-scope test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var inventoryScopeKey string
	var lastSeenAtMS int64
	if err := db.QueryRow(`select inventory_scope_key, last_seen_at_ms
		from account_quota_windows where provider_window_id = 'weekly'`).Scan(
		&inventoryScopeKey,
		&lastSeenAtMS,
	); err != nil {
		t.Fatalf("read cross-scope logical window: %v", err)
	}
	if inventoryScopeKey != "codex:rate-limits" || lastSeenAtMS != completeObservedAtMS {
		t.Fatalf("cross-scope logical window scope=%q last_seen_at_ms=%d", inventoryScopeKey, lastSeenAtMS)
	}
}

func TestQuotaLifecycleCrossScopePartialCannotReactivateInactiveWindow(t *testing.T) {
	service, path := newQuotaSnapshotTestServiceWithPath(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
	ownerScope := "codex:rate-limits"
	write := func(entry WriteEntry) {
		t.Helper()
		if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{entry}}); err != nil {
			t.Fatalf("write lifecycle observation: %v", err)
		}
	}
	write(quotaLifecycleWriteEntryWithObservation(
		"complete", "inspection", "owner-present", ownerScope,
		quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{weekly},
	))
	write(quotaLifecycleWriteEntryWithObservation(
		"complete", "inspection", "owner-missing-first", ownerScope,
		quotaLifecycleBaseMS+2*quotaLifecycleHourMS, nil,
	))
	write(quotaLifecycleWriteEntryWithObservation(
		"complete", "inspection", "owner-missing-confirmed", ownerScope,
		quotaLifecycleBaseMS+3*quotaLifecycleHourMS, nil,
	))

	foreign := quotaLifecycleFixedWindow(
		"weekly", "weekly", quotaLifecycleBaseMS+quotaLifecycleDayMS, 7*24*60*60, 80,
	)
	foreign.Source = "response_header"
	write(quotaLifecycleWriteEntryWithObservation(
		"partial", "response_header", "foreign-reopen", "codex:legacy-header",
		quotaLifecycleBaseMS+4*quotaLifecycleHourMS, []WindowInput{foreign},
	))

	window := queryQuotaLifecycleWindows(t, service, true)["weekly"]
	if window.Availability != "inactive" || window.ActivationGeneration != 1 ||
		window.DeactivatedAtMS == nil ||
		*window.DeactivatedAtMS != quotaLifecycleBaseMS+2*quotaLifecycleHourMS ||
		window.UsedPercent == nil || *window.UsedPercent != 20 {
		t.Fatalf("cross-scope partial reactivated inactive lifecycle = %#v", window)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open inactive cross-scope database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var activeActivations, activeCycles int
	if err := db.QueryRow(`select count(*) from account_quota_window_activations
		where deactivated_at_ms is null`).Scan(&activeActivations); err != nil {
		t.Fatalf("count active activations: %v", err)
	}
	if err := db.QueryRow(`select count(*) from account_quota_cycles
		where actual_end_ms is null`).Scan(&activeCycles); err != nil {
		t.Fatalf("count active cycles: %v", err)
	}
	if activeActivations != 0 || activeCycles != 0 {
		t.Fatalf("cross-scope partial created active lifecycle: activations=%d cycles=%d", activeActivations, activeCycles)
	}
}

func TestQuotaLifecycleCrossScopeRemovalCannotDeactivateOwnerWindow(t *testing.T) {
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "inspection", "owner-present", "codex:rate-limits",
			quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{weekly},
		),
	}}); err != nil {
		t.Fatalf("write owner lifecycle observation: %v", err)
	}

	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
		RowKey: "row-lifecycle", Provider: "codex", Account: quotaSnapshotTestAccount(),
		Observation: &ObservationInput{
			Source: "response_header", SourceObservationID: "foreign-removal",
			ObservedAtMS:      quotaLifecycleBaseMS + 2*quotaLifecycleHourMS,
			InventoryScopeKey: "codex:legacy-header", InventoryMode: "delta",
		},
		Windows: []WindowInput{},
		RemovedWindows: []RemovedWindowInput{{
			ProviderWindowID: "weekly", ModelScopeKind: "all",
		}},
	}}}); err != nil {
		t.Fatalf("write cross-scope removal: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.Availability != "active" || window.ActivationGeneration != 1 ||
		window.DeactivatedAtMS != nil || window.CurrentCycle == nil ||
		window.CurrentCycle.ActualEndMS != nil {
		t.Fatalf("cross-scope removal deactivated owner lifecycle = %#v", window)
	}
}

func TestQuotaLifecycleRepeatedDeltaRemovalPreservesOriginalDeactivationTime(t *testing.T) {
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+quotaLifecycleDayMS)
	weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
	writeQuotaLifecycleObservation(
		t,
		service,
		"complete",
		quotaLifecycleBaseMS+quotaLifecycleHourMS,
		[]WindowInput{weekly},
	)

	remove := func(observationID string, observedAtMS int64) {
		entry := quotaLifecycleWriteEntryWithObservation(
			"delta",
			"inspection",
			observationID,
			"codex:quota-windows",
			observedAtMS,
			nil,
		)
		entry.RemovedWindows = []RemovedWindowInput{{
			ProviderWindowID: "weekly",
			ModelScopeKind:   "all",
		}}
		if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{entry}}); err != nil {
			t.Fatalf("write delta removal %q: %v", observationID, err)
		}
	}

	firstRemovedAtMS := quotaLifecycleBaseMS + 2*quotaLifecycleHourMS
	remove("first-removal", firstRemovedAtMS)
	remove("repeated-removal", quotaLifecycleBaseMS+3*quotaLifecycleHourMS)

	window := queryQuotaLifecycleWindows(t, service, true)["weekly"]
	if window.Availability != "inactive" || window.DeactivatedAtMS == nil ||
		*window.DeactivatedAtMS != firstRemovedAtMS || window.ActivationGeneration != 1 {
		t.Fatalf("repeated delta removal lifecycle = %#v", window)
	}
}

func TestQuotaLifecycleIgnoresOutOfOrderCompleteOmissions(t *testing.T) {
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 10)
	fiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", quotaLifecycleBaseMS, 5*60*60, 20)
	fiveHour.RelationshipKind = "concurrent_subwindow"
	fiveHour.ContainerWindowID = "weekly"
	currentObservedAtMS := quotaLifecycleBaseMS + 6*quotaLifecycleHourMS
	writeQuotaLifecycleObservation(t, service, "complete", currentObservedAtMS, []WindowInput{fiveHour, weekly})

	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+2*quotaLifecycleHourMS, []WindowInput{weekly})
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+3*quotaLifecycleHourMS, []WindowInput{weekly})

	windows := queryQuotaLifecycleWindows(t, service, false)
	if windows["five-hour"].Availability != "active" ||
		windows["five-hour"].LastSeenAtMS != currentObservedAtMS ||
		windows["five-hour"].ActivationGeneration != 1 {
		t.Fatalf("old complete observations changed five-hour lifecycle = %#v", windows["five-hour"])
	}
}

func TestQuotaLifecycleClosesFixedCycleWhenProviderChangesWindowMode(t *testing.T) {
	service, path := newQuotaSnapshotTestServiceWithPath(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 40)
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{weekly})

	durationSeconds := int64(7 * 24 * 60 * 60)
	usedPercent := 45.0
	modeChangedAtMS := quotaLifecycleBaseMS + 2*quotaLifecycleHourMS
	rolling := WindowInput{
		ProviderWindowID: "weekly",
		WindowKind:       "weekly",
		WindowMode:       "rolling",
		ModelScopeKind:   "all",
		Source:           "inspection",
		BoundaryAccuracy: "estimated",
		DurationSeconds:  &durationSeconds,
		UsedPercent:      &usedPercent,
	}
	writeQuotaLifecycleObservation(t, service, "complete", modeChangedAtMS, []WindowInput{rolling})

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.WindowMode != "rolling" || window.CurrentCycle != nil || window.UsedPercent == nil ||
		*window.UsedPercent != usedPercent {
		t.Fatalf("rolling mode lifecycle = %#v", window)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open mode-change test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var state, endReason string
	var actualEndMS int64
	if err := db.QueryRow(`select state, actual_end_ms, end_reason
		from account_quota_cycles order by id desc limit 1`).Scan(&state, &actualEndMS, &endReason); err != nil {
		t.Fatalf("read mode-changed quota cycle: %v", err)
	}
	if state != "closed" || actualEndMS != modeChangedAtMS || endReason != "mode_changed" {
		t.Fatalf("mode-changed cycle state=%q actual_end_ms=%d end_reason=%q", state, actualEndMS, endReason)
	}

	reopenedStartMS := quotaLifecycleBaseMS + 3*quotaLifecycleHourMS
	reopened := quotaLifecycleFixedWindow("weekly", "weekly", reopenedStartMS, 7*24*60*60, 1)
	writeQuotaLifecycleObservation(t, service, "complete", reopenedStartMS+1_000, []WindowInput{reopened})
	window = queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.WindowMode != "fixed" || window.CurrentCycle == nil ||
		window.PreviousCycle != nil {
		t.Fatalf("reopened fixed lifecycle = %#v", window)
	}
}

func TestQuotaLifecycleDoesNotCrossModeChangeWhenFindingHistoricalPrevious(t *testing.T) {
	const durationSeconds = int64(24 * 60 * 60)
	firstStartMS := quotaLifecycleBaseMS
	firstEndMS := firstStartMS + durationSeconds*1000
	secondStartMS := firstEndMS + 8*60*1000
	thirdStartMS := secondStartMS + 3*quotaLifecycleHourMS
	service := newQuotaSnapshotTestService(t, thirdStartMS+quotaLifecycleHourMS)

	first := quotaLifecycleFixedWindow("weekly", "weekly", firstStartMS, durationSeconds, 70)
	writeQuotaLifecycleObservation(t, service, "complete", firstStartMS+quotaLifecycleHourMS, []WindowInput{first})
	second := quotaLifecycleFixedWindow("weekly", "weekly", secondStartMS, durationSeconds, 50)
	writeQuotaLifecycleObservation(t, service, "complete", secondStartMS+quotaLifecycleHourMS, []WindowInput{second})

	duration := durationSeconds
	used := 45.0
	rolling := WindowInput{
		ProviderWindowID: "weekly",
		WindowKind:       "weekly",
		WindowMode:       "rolling",
		ModelScopeKind:   "all",
		Source:           "inspection",
		BoundaryAccuracy: "estimated",
		DurationSeconds:  &duration,
		UsedPercent:      &used,
	}
	writeQuotaLifecycleObservation(t, service, "complete", secondStartMS+2*quotaLifecycleHourMS, []WindowInput{rolling})

	third := quotaLifecycleFixedWindow("weekly", "weekly", thirdStartMS, durationSeconds, 1)
	writeQuotaLifecycleObservation(t, service, "complete", thirdStartMS+1_000, []WindowInput{third})

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != thirdStartMS || window.PreviousCycle != nil {
		t.Fatalf("mode-change barrier leaked historical previous = %#v", window)
	}
}

func TestQuotaLifecycleRestoresClosedProviderCycleWithoutDuplicateInsert(t *testing.T) {
	service, path := newQuotaSnapshotTestServiceWithPath(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	original := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 40)
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{original})
	initial := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if initial.CurrentCycle == nil {
		t.Fatalf("initial fixed lifecycle = %#v", initial)
	}
	initialCycleID := initial.CurrentCycle.ID

	durationSeconds := int64(7 * 24 * 60 * 60)
	usedPercent := 45.0
	rolling := WindowInput{
		ProviderWindowID: "weekly",
		WindowKind:       "weekly",
		WindowMode:       "rolling",
		ModelScopeKind:   "all",
		Source:           "inspection",
		BoundaryAccuracy: "estimated",
		DurationSeconds:  &durationSeconds,
		UsedPercent:      &usedPercent,
	}
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+2*quotaLifecycleHourMS, []WindowInput{rolling})
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+3*quotaLifecycleHourMS, []WindowInput{original})

	restored := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if restored.CurrentCycle == nil || restored.CurrentCycle.ID != initialCycleID ||
		restored.CurrentCycle.ActualEndMS != nil || restored.PreviousCycle != nil {
		t.Fatalf("restored provider cycle = %#v", restored)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open restored-cycle database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var cycleCount int
	if err := db.QueryRow(`select count(*) from account_quota_cycles`).Scan(&cycleCount); err != nil {
		t.Fatalf("count restored cycles: %v", err)
	}
	if cycleCount != 1 {
		t.Fatalf("restored cycle count = %d, want 1", cycleCount)
	}
}

func TestQuotaLifecycleExactEvidenceReplacesDerivedCycleBoundary(t *testing.T) {
	service, path := newQuotaSnapshotTestServiceWithPath(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	durationSeconds := int64(7 * 24 * 60 * 60)
	derivedStartMS := quotaLifecycleBaseMS
	derivedEndMS := derivedStartMS + durationSeconds*1000
	exactStartMS := derivedStartMS + 2*60*1000
	exactEndMS := derivedEndMS + 2*60*1000
	derivedUsedPercent := 4.0
	exactUsedPercent := 0.0

	derived := WindowInput{
		ProviderWindowID: "weekly", WindowKind: "weekly", WindowMode: "fixed",
		ModelScopeKind: "all", Source: "response_header", BoundaryAccuracy: "derived",
		CycleStartMS: &derivedStartMS, CycleEndMS: &derivedEndMS,
		DurationSeconds: &durationSeconds, UsedPercent: &derivedUsedPercent,
	}
	writeQuotaLifecycleObservation(t, service, "partial", quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{derived})

	exact := derived
	exact.Source = "inspection"
	exact.BoundaryAccuracy = "exact"
	exact.CycleStartMS = &exactStartMS
	exact.CycleEndMS = &exactEndMS
	exact.UsedPercent = &exactUsedPercent
	writeQuotaLifecycleObservation(t, service, "partial", quotaLifecycleBaseMS+2*quotaLifecycleHourMS, []WindowInput{exact})

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != exactStartMS ||
		window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != exactStartMS ||
		window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != exactEndMS ||
		window.CurrentCycle.BoundaryAccuracy != "exact" || window.CycleStartMS == nil ||
		*window.CycleStartMS != exactStartMS || window.CycleEndMS == nil || *window.CycleEndMS != exactEndMS {
		t.Fatalf("exact cycle boundary = %#v", window)
	}
	if window.PreviousCycle != nil {
		t.Fatalf("precision upgrade created previous cycle = %#v", window.PreviousCycle)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open precision-upgrade database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var cycleCount int
	if err := db.QueryRow(`select count(*) from account_quota_cycles`).Scan(&cycleCount); err != nil {
		t.Fatalf("count precision-upgrade cycles: %v", err)
	}
	if cycleCount != 1 {
		t.Fatalf("precision-upgrade cycle count = %d, want 1", cycleCount)
	}
}

func TestQuotaLifecycleExposesSameActivationHistoricalCycleAsPreviousAfterGap(t *testing.T) {
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+4*quotaLifecycleDayMS)
	first := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 24*60*60, 70)
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{first})

	firstEndMS := quotaLifecycleBaseMS + 24*60*60*1000
	secondStartMS := firstEndMS + 477*1000
	second := quotaLifecycleFixedWindow("weekly", "weekly", secondStartMS, 24*60*60, 10)
	writeQuotaLifecycleObservation(t, service, "complete", secondStartMS+quotaLifecycleHourMS, []WindowInput{second})

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != secondStartMS ||
		window.PreviousCycle == nil || window.PreviousCycle.ActualStartMS != quotaLifecycleBaseMS ||
		window.PreviousCycle.ActualEndMS == nil || *window.PreviousCycle.ActualEndMS != firstEndMS ||
		window.PreviousCycle.EndReason != "scheduled" ||
		window.PreviousCycle.ActivationID != window.CurrentCycle.ActivationID {
		t.Fatalf("scheduled gap previous lifecycle = %#v", window)
	}
}

func TestQuotaLifecycleDoesNotReexposeCollapsedFragmentAsPrevious(t *testing.T) {
	const durationSeconds = int64(24 * 60 * 60)
	service, path := newQuotaSnapshotTestServiceWithPath(t, quotaLifecycleBaseMS+3*quotaLifecycleDayMS)
	initial := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, durationSeconds, 70)
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{initial})

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open collapsed-fragment database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var activationID, originalCycleID, originalSnapshotID int64
	if err := db.QueryRow(`select activation_id, logical_window_id, cycle_id, id
		from account_quota_snapshots order by id limit 1`).Scan(
		&activationID, new(int64), &originalCycleID, &originalSnapshotID,
	); err != nil {
		t.Fatalf("read initial collapsed-fragment lifecycle: %v", err)
	}

	firstEndMS := quotaLifecycleBaseMS + durationSeconds*1000
	fragmentStartMS := firstEndMS + 5*quotaLifecycleHourMS
	currentStartMS := fragmentStartMS + 30*1000
	invalidEndMS := fragmentStartMS + 2*60*1000
	if _, err := db.Exec(`update account_quota_cycles set
		state = 'closed', actual_end_ms = ?, end_reason = 'scheduled' where id = ?`,
		firstEndMS, originalCycleID); err != nil {
		t.Fatalf("close initial collapsed-fragment cycle: %v", err)
	}

	insertCycle := func(key, state string, startMS int64, endMS *int64, endReason string) int64 {
		t.Helper()
		result, insertErr := db.Exec(`insert into account_quota_cycles (
			activation_id, provider_cycle_key, state, scheduled_start_ms, scheduled_end_ms,
			actual_start_ms, actual_end_ms, duration_seconds, boundary_accuracy, end_reason,
			created_at_ms, updated_at_ms
		) values (?, ?, ?, ?, ?, ?, ?, ?, 'exact', ?, ?, ?)`,
			activationID, key, state, startMS, startMS+durationSeconds*1000,
			startMS, endMS, durationSeconds, endReason, startMS, startMS,
		)
		if insertErr != nil {
			t.Fatalf("insert collapsed-fragment cycle %s: %v", key, insertErr)
		}
		id, insertErr := result.LastInsertId()
		if insertErr != nil {
			t.Fatalf("read collapsed-fragment cycle %s ID: %v", key, insertErr)
		}
		return id
	}
	fragmentEnd := currentStartMS
	fragmentCycleID := insertCycle("weekly:fragment", "closed", fragmentStartMS, &fragmentEnd, "early_reset")
	currentCycleID := insertCycle("weekly:current", "active", currentStartMS, nil, "")
	_ = insertCycle("weekly:overlap", "closed", fragmentStartMS-2*quotaLifecycleHourMS, &invalidEndMS, "scheduled")

	insertSnapshot := func(cycleID int64, sourceID string, observedAtMS, startMS int64, usedPercent float64) {
		t.Helper()
		_, insertErr := db.Exec(`insert into account_quota_snapshots (
			observation_id, logical_window_id, activation_id, cycle_id, account_key, provider,
			provider_window_id, window_kind, window_mode, model_scope_kind, model_scope_key,
			model_ids_json, scope_fingerprint, content_hash, source, source_observation_id,
			observed_at_ms, boundary_accuracy, cycle_start_ms, cycle_end_ms,
			duration_seconds, used_percent, created_at_ms
		) select observation_id, logical_window_id, activation_id, ?, account_key, provider,
			provider_window_id, window_kind, window_mode, model_scope_kind, model_scope_key,
			model_ids_json, scope_fingerprint, ?, 'inspection', ?, ?, 'exact', ?, ?, ?, ?, ?
			from account_quota_snapshots where id = ?`,
			cycleID, sourceID, sourceID, observedAtMS,
			startMS, startMS+durationSeconds*1000, durationSeconds, usedPercent, observedAtMS,
			originalSnapshotID,
		)
		if insertErr != nil {
			t.Fatalf("insert collapsed-fragment snapshot %s: %v", sourceID, insertErr)
		}
	}
	insertSnapshot(fragmentCycleID, "fragment-observation", fragmentStartMS+1_000, fragmentStartMS, 40)
	insertSnapshot(currentCycleID, "current-observation", currentStartMS+1_000, currentStartMS, 38)

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ID != fragmentCycleID ||
		window.PreviousCycle == nil || window.PreviousCycle.ID != originalCycleID ||
		window.PreviousCycle.ID == fragmentCycleID || window.PreviousCycle.ActualEndMS == nil ||
		*window.PreviousCycle.ActualEndMS != firstEndMS {
		t.Fatalf("collapsed fragment previous lifecycle = %#v", window)
	}
}

func TestQuotaLifecyclePartialFiveHourInfersAndPreservesWeeklyRelationship(t *testing.T) {
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 10)
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{weekly})

	fiveHourStartMS := quotaLifecycleBaseMS + 2*quotaLifecycleHourMS
	fiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", fiveHourStartMS, 5*60*60, 20)
	writeQuotaLifecycleObservation(t, service, "partial", fiveHourStartMS+1_000, []WindowInput{fiveHour})
	initial := queryQuotaLifecycleWindows(t, service, false)
	if initial["five-hour"].RelationshipKind != "concurrent_subwindow" ||
		initial["five-hour"].ContainerWindowID != "weekly" ||
		initial["five-hour"].CurrentCycle == nil || initial["weekly"].CurrentCycle == nil ||
		initial["five-hour"].CurrentCycle.ParentCycleID == nil ||
		*initial["five-hour"].CurrentCycle.ParentCycleID != initial["weekly"].CurrentCycle.ID {
		t.Fatalf("inferred five-hour relationship: five-hour=%#v weekly=%#v", initial["five-hour"], initial["weekly"])
	}

	unknownUsedPercent := 30.0
	unknown := WindowInput{
		ProviderWindowID: "five-hour",
		WindowKind:       "unknown",
		WindowMode:       "unknown",
		ModelScopeKind:   "all",
		Source:           "response_header",
		BoundaryAccuracy: "unknown",
		UsedPercent:      &unknownUsedPercent,
	}
	writeQuotaLifecycleObservation(t, service, "partial", fiveHourStartMS+2_000, []WindowInput{unknown})
	preserved := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if preserved.WindowKind != "five_hour" || preserved.WindowMode != "fixed" ||
		preserved.RelationshipKind != "concurrent_subwindow" || preserved.ContainerWindowID != "weekly" ||
		preserved.CurrentCycle == nil || preserved.UsedPercent == nil || *preserved.UsedPercent != unknownUsedPercent {
		t.Fatalf("partial unknown five-hour lifecycle = %#v", preserved)
	}
}

func TestQuotaLifecyclePartialSupplementalFiveHourInfersExistingFamilyContainer(t *testing.T) {
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	weekly := quotaLifecycleFixedWindow("code-review-weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 10)
	writeQuotaLifecycleObservation(t, service, "partial", quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{weekly})

	fiveHourStartMS := quotaLifecycleBaseMS + 2*quotaLifecycleHourMS
	fiveHour := quotaLifecycleFixedWindow("code-review-five-hour", "five_hour", fiveHourStartMS, 5*60*60, 20)
	writeQuotaLifecycleObservation(t, service, "partial", fiveHourStartMS+1_000, []WindowInput{fiveHour})

	result, err := service.Query(context.Background(), QueryRequest{
		Accounts: []QueryAccount{{
			RowKey: "row-lifecycle", Provider: "codex", Account: quotaSnapshotTestAccount(),
		}},
	})
	if err != nil {
		t.Fatalf("query supplemental family inference: %v", err)
	}
	byID := make(map[string]Window, len(result.Items[0].Windows))
	for _, window := range result.Items[0].Windows {
		byID[window.ProviderWindowID] = window
	}
	child := byID["code-review-five-hour"]
	parent := byID["code-review-weekly"]
	if child.RelationshipKind != "concurrent_subwindow" || child.ContainerWindowID != "code-review-weekly" ||
		child.CurrentCycle == nil || parent.CurrentCycle == nil || child.CurrentCycle.ParentCycleID == nil ||
		*child.CurrentCycle.ParentCycleID != parent.CurrentCycle.ID {
		t.Fatalf("supplemental family inference: child=%#v parent=%#v", child, parent)
	}
}

func TestQuotaLifecycleLinksConcurrentCyclesWithinMatchingModelScope(t *testing.T) {
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	makeScoped := func(kind, scopeKey string, startMS, durationSeconds int64) WindowInput {
		window := quotaLifecycleFixedWindow(
			map[string]string{"five_hour": "five-hour", "weekly": "weekly"}[kind],
			kind,
			startMS,
			durationSeconds,
			20,
		)
		window.ModelScopeKind = "family"
		window.ModelScopeKey = scopeKey
		if kind == "five_hour" {
			window.RelationshipKind = "concurrent_subwindow"
			window.ContainerWindowID = "weekly"
		}
		return window
	}
	windows := []WindowInput{
		makeScoped("five_hour", "gemini", quotaLifecycleBaseMS, 5*60*60),
		makeScoped("weekly", "gemini", quotaLifecycleBaseMS, 7*24*60*60),
		makeScoped("five_hour", "claude_gpt", quotaLifecycleBaseMS, 5*60*60),
		makeScoped("weekly", "claude_gpt", quotaLifecycleBaseMS, 7*24*60*60),
	}
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+quotaLifecycleHourMS, windows)

	result, err := service.Query(context.Background(), QueryRequest{
		Accounts: []QueryAccount{{
			RowKey: "row-lifecycle", Provider: "codex", Account: quotaSnapshotTestAccount(),
		}},
	})
	if err != nil {
		t.Fatalf("query scoped parent cycles: %v", err)
	}
	byScopeAndKind := make(map[string]Window)
	for _, window := range result.Items[0].Windows {
		byScopeAndKind[window.ModelScopeKey+"\x00"+window.WindowKind] = window
	}
	for _, scopeKey := range []string{"gemini", "claude_gpt"} {
		fiveHour := byScopeAndKind[scopeKey+"\x00five_hour"]
		weekly := byScopeAndKind[scopeKey+"\x00weekly"]
		if fiveHour.CurrentCycle == nil || weekly.CurrentCycle == nil ||
			fiveHour.CurrentCycle.ParentCycleID == nil ||
			*fiveHour.CurrentCycle.ParentCycleID != weekly.CurrentCycle.ID {
			t.Fatalf("scoped cycle relationship %s: five-hour=%#v weekly=%#v", scopeKey, fiveHour, weekly)
		}
	}
}

func TestQuotaLifecycleLinksEachCodexQuotaFamilyWithinSharedScope(t *testing.T) {
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	windows := []WindowInput{
		quotaLifecycleFixedWindow("five-hour", "five_hour", quotaLifecycleBaseMS, 5*60*60, 10),
		quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20),
		quotaLifecycleFixedWindow("code-review-five-hour", "five_hour", quotaLifecycleBaseMS, 5*60*60, 30),
		quotaLifecycleFixedWindow("code-review-weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 40),
		quotaLifecycleFixedWindow("credits-five-hour-0", "five_hour", quotaLifecycleBaseMS, 5*60*60, 50),
		quotaLifecycleFixedWindow("credits-weekly-0", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 60),
	}
	applyCodexWindowRelationships(windows)
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+quotaLifecycleHourMS, windows)

	result, err := service.Query(context.Background(), QueryRequest{
		Accounts: []QueryAccount{{
			RowKey: "row-lifecycle", Provider: "codex", Account: quotaSnapshotTestAccount(),
		}},
	})
	if err != nil {
		t.Fatalf("query quota family relationships: %v", err)
	}
	byID := make(map[string]Window, len(result.Items[0].Windows))
	for _, window := range result.Items[0].Windows {
		byID[window.ProviderWindowID] = window
	}
	for childID, parentID := range map[string]string{
		"five-hour":             "weekly",
		"code-review-five-hour": "code-review-weekly",
		"credits-five-hour-0":   "credits-weekly-0",
	} {
		child := byID[childID]
		parent := byID[parentID]
		if child.RelationshipKind != "concurrent_subwindow" || child.ContainerWindowID != parentID ||
			child.CurrentCycle == nil || parent.CurrentCycle == nil || child.CurrentCycle.ParentCycleID == nil ||
			*child.CurrentCycle.ParentCycleID != parent.CurrentCycle.ID {
			t.Fatalf("quota family relationship %s -> %s: child=%#v parent=%#v", childID, parentID, child, parent)
		}
	}
}

func TestQuotaLifecycleDoesNotLinkConcurrentCyclesAcrossModelScopes(t *testing.T) {
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	fiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", quotaLifecycleBaseMS, 5*60*60, 20)
	fiveHour.ModelScopeKind = "family"
	fiveHour.ModelScopeKey = "gemini"
	fiveHour.RelationshipKind = "concurrent_subwindow"
	fiveHour.ContainerWindowID = "weekly"
	weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
	weekly.ModelScopeKind = "family"
	weekly.ModelScopeKey = "claude_gpt"

	writeQuotaLifecycleObservation(
		t,
		service,
		"complete",
		quotaLifecycleBaseMS+quotaLifecycleHourMS,
		[]WindowInput{fiveHour, weekly},
	)

	windows := queryQuotaLifecycleWindows(t, service, false)
	if windows["five-hour"].CurrentCycle == nil ||
		windows["five-hour"].CurrentCycle.ParentCycleID != nil {
		t.Fatalf("mismatched scoped cycles were linked: %#v", windows)
	}
}

func TestQuotaLifecycleDoesNotLinkConcurrentCyclesAcrossInventoryScopes(t *testing.T) {
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
	fiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", quotaLifecycleBaseMS, 5*60*60, 20)
	fiveHour.RelationshipKind = "concurrent_subwindow"
	fiveHour.ContainerWindowID = "weekly"

	for _, entry := range []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "inspection", "weekly-owner", "codex:weekly-owner",
			quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{weekly},
		),
		quotaLifecycleWriteEntryWithObservation(
			"complete", "inspection", "five-hour-owner", "codex:five-hour-owner",
			quotaLifecycleBaseMS+2*quotaLifecycleHourMS, []WindowInput{fiveHour},
		),
	} {
		if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{entry}}); err != nil {
			t.Fatalf("write cross-inventory relationship observation: %v", err)
		}
	}

	windows := queryQuotaLifecycleWindows(t, service, false)
	if windows["five-hour"].CurrentCycle == nil ||
		windows["five-hour"].CurrentCycle.ParentCycleID != nil {
		t.Fatalf("cross-inventory cycles were linked: %#v", windows)
	}
}

func TestQuotaLifecycleResetsWeeklyWithoutSplittingFiveHour(t *testing.T) {
	resetAtMS := quotaLifecycleBaseMS + 3*quotaLifecycleDayMS
	service := newQuotaSnapshotTestService(t, resetAtMS+2*quotaLifecycleDayMS)
	weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 40)
	fiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", resetAtMS-2*quotaLifecycleHourMS, 5*60*60, 25)
	fiveHour.RelationshipKind = "concurrent_subwindow"
	fiveHour.ContainerWindowID = "weekly"
	writeQuotaLifecycleObservation(t, service, "complete", resetAtMS-quotaLifecycleHourMS, []WindowInput{fiveHour, weekly})

	resetWeekly := quotaLifecycleFixedWindow("weekly", "weekly", resetAtMS, 7*24*60*60, 1)
	writeQuotaLifecycleObservation(t, service, "complete", resetAtMS+1_000, []WindowInput{fiveHour, resetWeekly})

	windows := queryQuotaLifecycleWindows(t, service, false)
	weeklyState := windows["weekly"]
	if weeklyState.PreviousCycle == nil || weeklyState.PreviousCycle.ActualEndMS == nil ||
		*weeklyState.PreviousCycle.ActualEndMS != resetAtMS || weeklyState.PreviousCycle.EndReason != "early_reset" ||
		weeklyState.PreviousCycle.ForecastEligible {
		t.Fatalf("weekly early reset = %#v", weeklyState)
	}
	fiveHourState := windows["five-hour"]
	if fiveHourState.CurrentCycle == nil || fiveHourState.PreviousCycle != nil ||
		weeklyState.PreviousCycle == nil || fiveHourState.CurrentCycle.ParentCycleID == nil ||
		*fiveHourState.CurrentCycle.ParentCycleID != weeklyState.PreviousCycle.ID {
		t.Fatalf("weekly-only reset split five-hour cycle: %#v", fiveHourState)
	}
}

func TestQuotaLifecycleUsesFirstObservedRequestForEarlyResetBoundary(t *testing.T) {
	const durationSeconds = int64(7 * 24 * 60 * 60)
	firstRequestAtMS := quotaLifecycleBaseMS + 3*quotaLifecycleDayMS
	providerStartMS := firstRequestAtMS + 30_000
	service := newQuotaSnapshotTestService(t, firstRequestAtMS+quotaLifecycleDayMS)
	oldCycle := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, durationSeconds, 4)
	writeQuotaLifecycleObservation(t, service, "complete", firstRequestAtMS-quotaLifecycleHourMS, []WindowInput{oldCycle})

	reset := quotaLifecycleFixedWindow("weekly", "weekly", providerStartMS, durationSeconds, 0)
	writeQuotaLifecycleObservation(t, service, "complete", firstRequestAtMS, []WindowInput{reset})

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != firstRequestAtMS ||
		window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != providerStartMS ||
		window.PreviousCycle == nil || window.PreviousCycle.ActualEndMS == nil ||
		*window.PreviousCycle.ActualEndMS != firstRequestAtMS || window.PreviousCycle.EndReason != "early_reset" {
		t.Fatalf("early reset request boundary = %#v", window)
	}
}

func TestQuotaLifecycleNormalizesStoredEarlyResetBoundaryToFirstConfirmedObservation(t *testing.T) {
	const durationSeconds = int64(7 * 24 * 60 * 60)
	firstRequestAtMS := quotaLifecycleBaseMS + 3*quotaLifecycleDayMS
	providerStartMS := firstRequestAtMS + 30_000
	service, path := newQuotaSnapshotTestServiceWithPath(t, firstRequestAtMS+quotaLifecycleDayMS)
	oldCycle := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, durationSeconds, 40)
	writeQuotaLifecycleObservation(t, service, "complete", firstRequestAtMS-quotaLifecycleHourMS, []WindowInput{oldCycle})

	reset := quotaLifecycleFixedWindow("weekly", "weekly", providerStartMS, durationSeconds, 1)
	writeQuotaLifecycleObservation(t, service, "complete", firstRequestAtMS, []WindowInput{reset})

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open lifecycle database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`update account_quota_cycles set actual_end_ms = ?
		where end_reason = 'early_reset'`, providerStartMS); err != nil {
		t.Fatalf("restore legacy previous boundary: %v", err)
	}
	if _, err := db.Exec(`update account_quota_cycles set actual_start_ms = ?
		where actual_end_ms is null`, providerStartMS); err != nil {
		t.Fatalf("restore legacy current boundary: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != firstRequestAtMS ||
		window.PreviousCycle == nil || window.PreviousCycle.ActualEndMS == nil ||
		*window.PreviousCycle.ActualEndMS != firstRequestAtMS {
		t.Fatalf("normalized stored early reset boundary = %#v", window)
	}
}

func TestQuotaLifecycleDoesNotCollapseEarlyResetFragmentAcrossObservationGap(t *testing.T) {
	const durationSeconds = int64(7 * 24 * 60 * 60)
	firstRequestAtMS := quotaLifecycleBaseMS + 3*quotaLifecycleDayMS
	providerStartMS := firstRequestAtMS + 30*1000
	currentStartMS := providerStartMS + 2*60*1000
	service, path := newQuotaSnapshotTestServiceWithPath(t, currentStartMS+quotaLifecycleDayMS)
	oldCycle := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, durationSeconds, 40)
	writeQuotaLifecycleObservation(t, service, "complete", firstRequestAtMS-quotaLifecycleHourMS, []WindowInput{oldCycle})

	reset := quotaLifecycleFixedWindow("weekly", "weekly", providerStartMS, durationSeconds, 1)
	writeQuotaLifecycleObservation(t, service, "complete", firstRequestAtMS, []WindowInput{reset})

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open lifecycle database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var oldCycleID, currentCycleID int64
	if err := db.QueryRow(`select id from account_quota_cycles
		where end_reason = 'early_reset' limit 1`).Scan(&oldCycleID); err != nil {
		t.Fatalf("read early-reset fragment cycle: %v", err)
	}
	if err := db.QueryRow(`select id from account_quota_cycles
		where actual_end_ms is null limit 1`).Scan(&currentCycleID); err != nil {
		t.Fatalf("read active cycle: %v", err)
	}
	if _, err := db.Exec(`update account_quota_cycles set actual_end_ms = ? where id = ?`, providerStartMS, oldCycleID); err != nil {
		t.Fatalf("restore early-reset fragment end: %v", err)
	}
	if _, err := db.Exec(`update account_quota_cycles set actual_start_ms = ? where id = ?`, currentStartMS, currentCycleID); err != nil {
		t.Fatalf("introduce early-reset observation gap: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ID != currentCycleID ||
		window.CurrentCycle.ActualStartMS != currentStartMS || window.PreviousCycle == nil ||
		window.PreviousCycle.ID != oldCycleID || window.PreviousCycle.EndReason != "early_reset" ||
		window.PreviousCycle.ActualEndMS == nil || *window.PreviousCycle.ActualEndMS != providerStartMS {
		t.Fatalf("early-reset gap lifecycle = %#v", window)
	}
}

func TestQuotaLifecycleMarksConcurrentFiveHourAndWeeklyResetAsProviderReset(t *testing.T) {
	resetAtMS := quotaLifecycleBaseMS + 3*quotaLifecycleDayMS
	service := newQuotaSnapshotTestService(t, resetAtMS+2*quotaLifecycleDayMS)
	weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 40)
	fiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", resetAtMS-2*quotaLifecycleHourMS, 5*60*60, 25)
	fiveHour.RelationshipKind = "concurrent_subwindow"
	fiveHour.ContainerWindowID = "weekly"
	writeQuotaLifecycleObservation(t, service, "complete", resetAtMS-quotaLifecycleHourMS, []WindowInput{fiveHour, weekly})

	resetWeekly := quotaLifecycleFixedWindow("weekly", "weekly", resetAtMS, 7*24*60*60, 1)
	resetFiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", resetAtMS, 5*60*60, 2)
	resetFiveHour.RelationshipKind = "concurrent_subwindow"
	resetFiveHour.ContainerWindowID = "weekly"
	writeQuotaLifecycleObservation(t, service, "complete", resetAtMS+1_000, []WindowInput{resetFiveHour, resetWeekly})

	windows := queryQuotaLifecycleWindows(t, service, false)
	for _, id := range []string{"five-hour", "weekly"} {
		window := windows[id]
		if window.PreviousCycle == nil || window.PreviousCycle.ActualEndMS == nil ||
			*window.PreviousCycle.ActualEndMS != resetAtMS || window.PreviousCycle.EndReason != "provider_reset" ||
			window.PreviousCycle.ForecastEligible {
			t.Fatalf("%s provider reset = %#v", id, window)
		}
	}
	fiveHourState := windows["five-hour"]
	weeklyState := windows["weekly"]
	if fiveHourState.RelationshipKind != "concurrent_subwindow" || fiveHourState.ContainerWindowID != "weekly" {
		t.Fatalf("five-hour relationship = %#v", fiveHourState)
	}
	if fiveHourState.CurrentCycle == nil || weeklyState.CurrentCycle == nil ||
		fiveHourState.CurrentCycle.ParentCycleID == nil ||
		*fiveHourState.CurrentCycle.ParentCycleID != weeklyState.CurrentCycle.ID {
		t.Fatalf("five-hour parent cycle was not linked: five-hour=%#v weekly=%#v", fiveHourState, weeklyState)
	}
}

func TestQuotaLifecycleDetectsProviderResetWhenBoundariesDoNotChange(t *testing.T) {
	resetAtMS := quotaLifecycleBaseMS + 3*quotaLifecycleHourMS
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+quotaLifecycleDayMS)
	weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 65)
	fiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", quotaLifecycleBaseMS, 5*60*60, 75)
	fiveHour.RelationshipKind = "concurrent_subwindow"
	fiveHour.ContainerWindowID = "weekly"
	writeQuotaLifecycleObservation(t, service, "complete", resetAtMS-quotaLifecycleHourMS, []WindowInput{fiveHour, weekly})

	resetWeekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 1)
	resetFiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", quotaLifecycleBaseMS, 5*60*60, 1)
	resetFiveHour.RelationshipKind = "concurrent_subwindow"
	resetFiveHour.ContainerWindowID = "weekly"
	writeQuotaLifecycleObservation(t, service, "complete", resetAtMS, []WindowInput{resetFiveHour, resetWeekly})

	windows := queryQuotaLifecycleWindows(t, service, false)
	for _, id := range []string{"five-hour", "weekly"} {
		window := windows[id]
		if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != resetAtMS ||
			window.PreviousCycle == nil || window.PreviousCycle.ActualEndMS == nil ||
			*window.PreviousCycle.ActualEndMS != resetAtMS || window.PreviousCycle.EndReason != "provider_reset" ||
			window.PreviousCycle.ForecastEligible {
			t.Fatalf("same-boundary provider reset %s = %#v", id, window)
		}
	}
	if windows["five-hour"].CurrentCycle.ParentCycleID == nil ||
		*windows["five-hour"].CurrentCycle.ParentCycleID != windows["weekly"].CurrentCycle.ID {
		t.Fatalf("same-boundary reset parent cycles = %#v", windows)
	}
}

func TestQuotaLifecycleDetectsCounterResetAcrossObservationSources(t *testing.T) {
	resetAtMS := quotaLifecycleBaseMS + 3*quotaLifecycleHourMS
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+quotaLifecycleDayMS)
	beforeReset := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 65)
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete",
			"api_query",
			"api-before-reset",
			"codex:rate-limits",
			resetAtMS-quotaLifecycleHourMS,
			[]WindowInput{beforeReset},
		),
	}}); err != nil {
		t.Fatalf("write API quota before reset: %v", err)
	}

	afterReset := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 1)
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"partial",
			"response_header",
			"header-after-reset",
			"codex:rate-limits",
			resetAtMS,
			[]WindowInput{afterReset},
		),
	}}); err != nil {
		t.Fatalf("write Header quota after reset: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != resetAtMS ||
		window.PreviousCycle == nil || window.PreviousCycle.ActualEndMS == nil ||
		*window.PreviousCycle.ActualEndMS != resetAtMS ||
		window.PreviousCycle.EndReason != "early_reset" || window.PreviousCycle.ForecastEligible {
		t.Fatalf("cross-source counter reset lifecycle = %#v", window)
	}
}

func TestQuotaLifecycleDoesNotSplitCycleForSmallQuotaCorrection(t *testing.T) {
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+quotaLifecycleDayMS)
	first := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 40)
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{first})

	corrected := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 38)
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+2*quotaLifecycleHourMS, []WindowInput{corrected})

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != quotaLifecycleBaseMS ||
		window.PreviousCycle != nil {
		t.Fatalf("small quota correction split lifecycle = %#v", window)
	}
}

func TestQuotaLifecycleDetectsLargeQuotaDropReset(t *testing.T) {
	resetAtMS := quotaLifecycleBaseMS + 3*quotaLifecycleHourMS
	service := newQuotaSnapshotTestService(t, resetAtMS+quotaLifecycleDayMS)
	first := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 60)
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{first})

	reset := quotaLifecycleFixedWindow("weekly", "weekly", resetAtMS, 7*24*60*60, 25)
	writeQuotaLifecycleObservation(t, service, "complete", resetAtMS+1_000, []WindowInput{reset})

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != resetAtMS ||
		window.PreviousCycle == nil || window.PreviousCycle.ActualEndMS == nil ||
		*window.PreviousCycle.ActualEndMS != resetAtMS || window.PreviousCycle.EndReason != "early_reset" ||
		window.PreviousCycle.ForecastEligible {
		t.Fatalf("large quota drop reset lifecycle = %#v", window)
	}
}

func TestQuotaLifecycleDoesNotSplitLowUsageCycleWithoutBoundaryShift(t *testing.T) {
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+quotaLifecycleDayMS)
	first := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 4)
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{first})

	nearZero := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 0)
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+2*quotaLifecycleHourMS, []WindowInput{nearZero})

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != quotaLifecycleBaseMS ||
		window.PreviousCycle != nil {
		t.Fatalf("same-boundary low usage split lifecycle = %#v", window)
	}
}

func TestQuotaLifecycleDetectsCalendarCounterReset(t *testing.T) {
	resetAtMS := quotaLifecycleBaseMS + 3*quotaLifecycleHourMS
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+quotaLifecycleDayMS)
	beforeReset := quotaLifecycleFixedWindow("calendar-week", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 65)
	beforeReset.WindowMode = "calendar"
	writeQuotaLifecycleObservation(
		t,
		service,
		"complete",
		resetAtMS-quotaLifecycleHourMS,
		[]WindowInput{beforeReset},
	)

	afterReset := quotaLifecycleFixedWindow("calendar-week", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 1)
	afterReset.WindowMode = "calendar"
	writeQuotaLifecycleObservation(t, service, "complete", resetAtMS, []WindowInput{afterReset})

	window := queryQuotaLifecycleWindows(t, service, false)["calendar-week"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != resetAtMS ||
		window.PreviousCycle == nil || window.PreviousCycle.ActualEndMS == nil ||
		*window.PreviousCycle.ActualEndMS != resetAtMS ||
		window.PreviousCycle.EndReason != "early_reset" || window.PreviousCycle.ForecastEligible {
		t.Fatalf("calendar counter reset lifecycle = %#v", window)
	}
}

func TestQuotaLifecycleActivatesCodexCycleFromConfirmedCallAfterProvisionalAPI(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	at := func(day, hour, minute int) int64 {
		return time.Date(2026, time.August, day, hour, minute, 0, 0, location).UnixMilli()
	}
	const durationSeconds = int64(7 * 24 * 60 * 60)
	oldStartMS := at(9, 8, 28)
	oldEndMS := at(16, 8, 28)
	provisionalAtMS := at(11, 9, 24)
	confirmedAtMS := at(11, 9, 27)
	confirmedStartMS := at(11, 8, 0)
	confirmedEndMS := confirmedStartMS + durationSeconds*1000
	driftAtMS := at(11, 9, 30)
	service, path := newQuotaSnapshotTestServiceWithPath(t, at(12, 8, 0))

	oldCycle := quotaLifecycleFixedWindow("weekly", "weekly", oldStartMS, durationSeconds, 65)
	writeQuotaLifecycleObservation(t, service, "complete", at(10, 8, 30), []WindowInput{oldCycle})

	provisional := quotaLifecycleFixedWindow("weekly", "weekly", provisionalAtMS, durationSeconds, 0)
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "api_query", "api-provisional", "codex:quota-windows",
			provisionalAtMS, []WindowInput{provisional},
		),
	}}); err != nil {
		t.Fatalf("write provisional API boundary: %v", err)
	}

	beforeCall := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if beforeCall.CurrentCycle == nil || beforeCall.CurrentCycle.ActualStartMS != oldStartMS ||
		beforeCall.PreviousCycle != nil {
		t.Fatalf("provisional API boundary changed lifecycle = %#v", beforeCall)
	}

	confirmed := quotaLifecycleFixedWindow("weekly", "weekly", confirmedStartMS, durationSeconds, 0)
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"partial", "response_header", "header-first-call", "codex:quota-windows",
			confirmedAtMS, []WindowInput{confirmed},
		),
	}}); err != nil {
		t.Fatalf("write confirmed Header boundary: %v", err)
	}

	drift := quotaLifecycleFixedWindow("weekly", "weekly", driftAtMS, durationSeconds, 0)
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "api_query", "api-after-confirmation", "codex:quota-windows",
			driftAtMS, []WindowInput{drift},
		),
	}}); err != nil {
		t.Fatalf("write API drift after confirmation: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != confirmedStartMS ||
		window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != confirmedStartMS ||
		window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != confirmedEndMS ||
		window.PreviousCycle == nil || window.PreviousCycle.ScheduledStartMS == nil ||
		*window.PreviousCycle.ScheduledStartMS != oldStartMS || window.PreviousCycle.ScheduledEndMS == nil ||
		*window.PreviousCycle.ScheduledEndMS != oldEndMS || window.PreviousCycle.ActualEndMS == nil ||
		*window.PreviousCycle.ActualEndMS != confirmedStartMS {
		t.Fatalf("confirmed Codex lifecycle = %#v", window)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open lifecycle database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var cycleCount, unassignedProvisionalCount int
	if err := db.QueryRow(`select count(*) from account_quota_cycles`).Scan(&cycleCount); err != nil {
		t.Fatalf("count confirmed lifecycle cycles: %v", err)
	}
	if err := db.QueryRow(`select count(*) from account_quota_snapshots
		where source = 'api_query' and source_observation_id in ('api-provisional', 'api-after-confirmation')
			and cycle_id is null and boundary_accuracy = 'unknown'`).Scan(&unassignedProvisionalCount); err != nil {
		t.Fatalf("count unassigned provisional snapshots: %v", err)
	}
	if cycleCount != 2 || unassignedProvisionalCount != 2 {
		t.Fatalf("cycle_count=%d unassigned_provisional_count=%d, want 2 and 2", cycleCount, unassignedProvisionalCount)
	}
}

func TestQuotaLifecycleTreatsZeroUseResponseHeaderAsConfirmedCallEvidence(t *testing.T) {
	const durationSeconds = int64(7 * 24 * 60 * 60)
	oldStartMS := quotaLifecycleBaseMS
	firstCallAtMS := oldStartMS + 2*quotaLifecycleDayMS
	service := newQuotaSnapshotTestService(t, firstCallAtMS+quotaLifecycleDayMS)
	oldCycle := quotaLifecycleFixedWindow("weekly", "weekly", oldStartMS, durationSeconds, 65)
	writeQuotaLifecycleObservation(t, service, "complete", oldStartMS+quotaLifecycleHourMS, []WindowInput{oldCycle})

	firstCall := quotaLifecycleFixedWindow("weekly", "weekly", firstCallAtMS, durationSeconds, 0)
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"partial", "response_header", "header-zero-first-call", "codex:quota-windows",
			firstCallAtMS, []WindowInput{firstCall},
		),
	}}); err != nil {
		t.Fatalf("write zero-use Header call evidence: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != firstCallAtMS ||
		window.CurrentCycle.State != "active" || window.PreviousCycle == nil ||
		window.PreviousCycle.ActualEndMS == nil || *window.PreviousCycle.ActualEndMS != firstCallAtMS {
		t.Fatalf("zero-use Header call lifecycle = %#v", window)
	}
}

func TestQuotaLifecycleNormalizesStoredProvisionalDriftChainWithoutDeletingRows(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	at := func(day, hour, minute int) int64 {
		return time.Date(2026, time.August, day, hour, minute, 0, 0, location).UnixMilli()
	}
	const durationSeconds = int64(7 * 24 * 60 * 60)
	oldStartMS := at(9, 8, 28)
	oldEndMS := at(16, 8, 28)
	provisionalStartMS := at(11, 9, 24)
	confirmedStartMS := at(11, 9, 27)
	service, path := newQuotaSnapshotTestServiceWithPath(t, at(12, 8, 0))
	oldCycle := quotaLifecycleFixedWindow("weekly", "weekly", oldStartMS, durationSeconds, 65)
	writeQuotaLifecycleObservation(t, service, "complete", at(10, 8, 30), []WindowInput{oldCycle})

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy lifecycle database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var activationID, oldCycleID int64
	if err := db.QueryRow(`select activation_id, id from account_quota_cycles
		where actual_end_ms is null limit 1`).Scan(&activationID, &oldCycleID); err != nil {
		t.Fatalf("read active lifecycle IDs: %v", err)
	}
	if _, err := db.Exec(`update account_quota_cycles set
		state = 'closed', actual_end_ms = ?, end_reason = 'early_reset' where id = ?`,
		provisionalStartMS, oldCycleID); err != nil {
		t.Fatalf("close original legacy cycle: %v", err)
	}
	insertCycle := func(key, state string, startMS int64, endMS *int64, endReason string) int64 {
		t.Helper()
		result, insertErr := db.Exec(`insert into account_quota_cycles (
			activation_id, provider_cycle_key, state, scheduled_start_ms, scheduled_end_ms,
			actual_start_ms, actual_end_ms, duration_seconds, boundary_accuracy, end_reason,
			created_at_ms, updated_at_ms
		) values (?, ?, ?, ?, ?, ?, ?, ?, 'exact', ?, ?, ?)`,
			activationID, key, state, startMS, startMS+durationSeconds*1000,
			startMS, endMS, durationSeconds, endReason, startMS, startMS,
		)
		if insertErr != nil {
			t.Fatalf("insert legacy cycle %s: %v", key, insertErr)
		}
		id, insertErr := result.LastInsertId()
		if insertErr != nil {
			t.Fatalf("read legacy cycle %s ID: %v", key, insertErr)
		}
		return id
	}
	provisionalEndMS := confirmedStartMS
	confirmedCycleID := insertCycle("legacy-confirmed", "closed", provisionalStartMS, &provisionalEndMS, "early_reset")
	provisionalCycleID := insertCycle("legacy-provisional", "active", confirmedStartMS, nil, "")

	insertSnapshot := func(cycleID int64, source, sourceID string, observedAtMS, startMS int64, usedPercent float64) {
		t.Helper()
		_, insertErr := db.Exec(`insert into account_quota_snapshots (
			logical_window_id, activation_id, cycle_id, account_key, provider,
			provider_window_id, window_kind, window_mode, model_scope_kind,
			scope_fingerprint, content_hash, source, source_observation_id,
			observed_at_ms, boundary_accuracy, cycle_start_ms, cycle_end_ms,
			duration_seconds, used_percent, created_at_ms
		) select logical_window_id, activation_id, ?, account_key, provider,
			provider_window_id, window_kind, window_mode, model_scope_kind,
			scope_fingerprint, ?, ?, ?, ?, 'exact', ?, ?, ?, ?, ?
			from account_quota_snapshots where cycle_id = ? limit 1`,
			cycleID, sourceID, source, sourceID, observedAtMS,
			startMS, startMS+durationSeconds*1000, durationSeconds, usedPercent, observedAtMS,
			oldCycleID,
		)
		if insertErr != nil {
			t.Fatalf("insert legacy snapshot %s: %v", sourceID, insertErr)
		}
	}
	insertSnapshot(confirmedCycleID, "response_header", "legacy-header-0924", provisionalStartMS, provisionalStartMS, 0)
	insertSnapshot(provisionalCycleID, "api_query", "legacy-api-0927", confirmedStartMS, confirmedStartMS, 0)
	child := quotaLifecycleFixedWindow("five-hour", "five_hour", confirmedStartMS, 5*60*60, 0)
	child.RelationshipKind = "concurrent_subwindow"
	child.ContainerWindowID = "weekly"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"partial", "response_header", "legacy-child-header", "codex:quota-windows",
			at(11, 9, 28), []WindowInput{child},
		),
	}}); err != nil {
		t.Fatalf("write legacy child cycle: %v", err)
	}
	var storedParentCycleID int64
	if err := db.QueryRow(`select c.parent_cycle_id
		from account_quota_cycles c
		join account_quota_window_activations a on a.id = c.activation_id
		join account_quota_windows w on w.id = a.window_id
		where w.provider_window_id = 'five-hour' and c.actual_end_ms is null`).Scan(&storedParentCycleID); err != nil {
		t.Fatalf("read stored legacy child parent: %v", err)
	}
	if storedParentCycleID != provisionalCycleID {
		t.Fatalf("stored child parent cycle = %d, want provisional cycle %d", storedParentCycleID, provisionalCycleID)
	}

	windows := queryQuotaLifecycleWindows(t, service, false)
	window := windows["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != provisionalStartMS ||
		window.CurrentCycle.ID != confirmedCycleID || window.CurrentCycle.State != "active" ||
		window.CurrentCycle.ScheduledStartMS == nil ||
		*window.CurrentCycle.ScheduledStartMS != provisionalStartMS ||
		window.PreviousCycle == nil || window.PreviousCycle.ID != oldCycleID ||
		window.PreviousCycle.ScheduledStartMS == nil || *window.PreviousCycle.ScheduledStartMS != oldStartMS ||
		window.PreviousCycle.ScheduledEndMS == nil || *window.PreviousCycle.ScheduledEndMS != oldEndMS ||
		window.PreviousCycle.ActualEndMS == nil || *window.PreviousCycle.ActualEndMS != provisionalStartMS {
		t.Fatalf("normalized legacy lifecycle = %#v", window)
	}
	childWindow := windows["five-hour"]
	if childWindow.CurrentCycle == nil || childWindow.CurrentCycle.ParentCycleID == nil ||
		*childWindow.CurrentCycle.ParentCycleID != window.CurrentCycle.ID {
		t.Fatalf("normalized child parent cycle: child=%#v weekly=%#v", childWindow, window)
	}
	var cycleCount int
	if err := db.QueryRow(`select count(*) from account_quota_cycles where activation_id = ?`, activationID).Scan(&cycleCount); err != nil {
		t.Fatalf("count stored legacy cycles: %v", err)
	}
	if cycleCount != 3 {
		t.Fatalf("stored legacy cycle count = %d, want 3", cycleCount)
	}
}

func TestQuotaLifecycleRequiresConfirmedAbsenceAndReopensNewGeneration(t *testing.T) {
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 10)
	fiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", quotaLifecycleBaseMS, 5*60*60, 20)
	fiveHour.RelationshipKind = "concurrent_subwindow"
	fiveHour.ContainerWindowID = "weekly"
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{fiveHour, weekly})

	writeQuotaLifecycleObservation(t, service, "partial", quotaLifecycleBaseMS+2*quotaLifecycleHourMS, []WindowInput{weekly})
	if got := queryQuotaLifecycleWindows(t, service, false)["five-hour"].Availability; got != "active" {
		t.Fatalf("partial omission availability = %q, want active", got)
	}

	firstMissingAtMS := quotaLifecycleBaseMS + 3*quotaLifecycleHourMS
	writeQuotaLifecycleObservation(t, service, "complete", firstMissingAtMS, []WindowInput{weekly})
	if got := queryQuotaLifecycleWindows(t, service, false)["five-hour"].Availability; got != "pending_absent" {
		t.Fatalf("first complete omission availability = %q, want pending_absent", got)
	}
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+4*quotaLifecycleHourMS, []WindowInput{weekly})
	if _, ok := queryQuotaLifecycleWindows(t, service, false)["five-hour"]; ok {
		t.Fatal("confirmed inactive five-hour window remained in default query")
	}
	inactive := queryQuotaLifecycleWindows(t, service, true)["five-hour"]
	if inactive.Availability != "inactive" || inactive.DeactivatedAtMS == nil ||
		*inactive.DeactivatedAtMS != firstMissingAtMS || inactive.ActivationGeneration != 1 {
		t.Fatalf("inactive lifecycle = %#v", inactive)
	}

	reopenedFiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", quotaLifecycleBaseMS+5*quotaLifecycleHourMS, 5*60*60, 1)
	writeQuotaLifecycleObservation(t, service, "partial", quotaLifecycleBaseMS+5*quotaLifecycleHourMS+1_000, []WindowInput{reopenedFiveHour})
	reopened := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if reopened.Availability != "active" || reopened.ActivationGeneration != 2 || reopened.DeactivatedAtMS != nil ||
		reopened.CurrentCycle == nil || reopened.PreviousCycle != nil ||
		reopened.RelationshipKind != "concurrent_subwindow" || reopened.ContainerWindowID != "weekly" ||
		reopened.CurrentCycle.ParentCycleID == nil {
		t.Fatalf("reopened lifecycle = %#v", reopened)
	}
}

func TestQueryPrefersNewerLegacySnapshotDuringPartialLifecycleMigration(t *testing.T) {
	nowMS := quotaLifecycleBaseMS + 2*quotaLifecycleDayMS
	service, path := newQuotaSnapshotTestServiceWithPath(t, nowMS)
	olderObservedAtMS := quotaLifecycleBaseMS + quotaLifecycleHourMS
	newerObservedAtMS := olderObservedAtMS + quotaLifecycleHourMS
	older := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
	writeQuotaLifecycleObservation(t, service, "partial", olderObservedAtMS, []WindowInput{older})

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open partial migration database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var accountKey string
	if err := db.QueryRow(`select account_key from account_quota_snapshots limit 1`).Scan(&accountKey); err != nil {
		t.Fatalf("read quota account key: %v", err)
	}
	newCycleStartMS := quotaLifecycleBaseMS + quotaLifecycleDayMS
	newCycleEndMS := newCycleStartMS + 7*quotaLifecycleDayMS
	if _, err := db.Exec(`insert into account_quota_snapshots (
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, model_ids_json, source, source_observation_id,
		observed_at_ms, boundary_accuracy, cycle_start_ms, cycle_end_ms,
		duration_seconds, used_percent, remaining_percent, created_at_ms
	) values (?, 'codex', 'weekly', 'weekly', 'fixed', 'all', '[]',
		'inspection', 'legacy-newer', ?, 'exact', ?, ?, ?, 80, 20, ?)`,
		accountKey,
		newerObservedAtMS,
		newCycleStartMS,
		newCycleEndMS,
		int64(7*24*60*60),
		newerObservedAtMS,
	); err != nil {
		t.Fatalf("insert newer unmigrated snapshot: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.ObservedAtMS != newerObservedAtMS || window.SourceObservationID != "legacy-newer" ||
		window.UsedPercent == nil || *window.UsedPercent != 80 {
		t.Fatalf("partially migrated quota selection = %#v", window)
	}
}

func TestQueryKeepsNewerLifecycleSnapshotAheadOfOlderLegacyEvidence(t *testing.T) {
	nowMS := quotaLifecycleBaseMS + 2*quotaLifecycleDayMS
	service, path := newQuotaSnapshotTestServiceWithPath(t, nowMS)
	olderObservedAtMS := quotaLifecycleBaseMS + quotaLifecycleHourMS
	newerObservedAtMS := olderObservedAtMS + quotaLifecycleHourMS
	current := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 20)
	current.BoundaryAccuracy = "estimated"
	writeQuotaLifecycleObservation(t, service, "partial", newerObservedAtMS, []WindowInput{current})

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open partial migration database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var accountKey string
	if err := db.QueryRow(`select account_key from account_quota_snapshots limit 1`).Scan(&accountKey); err != nil {
		t.Fatalf("read quota account key: %v", err)
	}
	legacyCycleStartMS := quotaLifecycleBaseMS + quotaLifecycleDayMS
	legacyCycleEndMS := legacyCycleStartMS + 7*quotaLifecycleDayMS
	if _, err := db.Exec(`insert into account_quota_snapshots (
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, model_ids_json, source, source_observation_id,
		observed_at_ms, boundary_accuracy, cycle_start_ms, cycle_end_ms,
		duration_seconds, used_percent, remaining_percent, created_at_ms
	) values (?, 'codex', 'weekly', 'weekly', 'fixed', 'all', '[]',
		'inspection', 'legacy-older', ?, 'exact', ?, ?, ?, 80, 20, ?)`,
		accountKey,
		olderObservedAtMS,
		legacyCycleStartMS,
		legacyCycleEndMS,
		int64(7*24*60*60),
		olderObservedAtMS,
	); err != nil {
		t.Fatalf("insert older unmigrated snapshot: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.ObservedAtMS != newerObservedAtMS || window.SourceObservationID == "legacy-older" ||
		window.UsedPercent == nil || *window.UsedPercent != 20 {
		t.Fatalf("partially migrated quota selection = %#v", window)
	}
}

func TestQuotaLifecyclePreservesSubwindowRelationshipUntilContainerAbsenceIsConfirmed(t *testing.T) {
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+2*quotaLifecycleDayMS)
	weekly := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 10)
	fiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", quotaLifecycleBaseMS, 5*60*60, 20)
	fiveHour.RelationshipKind = "concurrent_subwindow"
	fiveHour.ContainerWindowID = "weekly"
	writeQuotaLifecycleObservation(
		t,
		service,
		"complete",
		quotaLifecycleBaseMS+quotaLifecycleHourMS,
		[]WindowInput{fiveHour, weekly},
	)

	childWithoutRelationship := fiveHour
	childWithoutRelationship.RelationshipKind = ""
	childWithoutRelationship.ContainerWindowID = ""
	firstMissingAtMS := quotaLifecycleBaseMS + 2*quotaLifecycleHourMS
	writeQuotaLifecycleObservation(
		t,
		service,
		"complete",
		firstMissingAtMS,
		[]WindowInput{childWithoutRelationship},
	)
	pending := queryQuotaLifecycleWindows(t, service, true)
	if pending["weekly"].Availability != "pending_absent" ||
		pending["five-hour"].RelationshipKind != "concurrent_subwindow" ||
		pending["five-hour"].ContainerWindowID != "weekly" ||
		pending["five-hour"].CurrentCycle == nil ||
		pending["five-hour"].CurrentCycle.ParentCycleID == nil ||
		pending["weekly"].CurrentCycle == nil ||
		*pending["five-hour"].CurrentCycle.ParentCycleID != pending["weekly"].CurrentCycle.ID {
		t.Fatalf("pending container relationship: five-hour=%#v weekly=%#v", pending["five-hour"], pending["weekly"])
	}

	writeQuotaLifecycleObservation(
		t,
		service,
		"complete",
		quotaLifecycleBaseMS+3*quotaLifecycleHourMS,
		[]WindowInput{childWithoutRelationship},
	)
	inactive := queryQuotaLifecycleWindows(t, service, true)
	if inactive["weekly"].Availability != "inactive" ||
		inactive["five-hour"].RelationshipKind != "" ||
		inactive["five-hour"].ContainerWindowID != "" ||
		inactive["five-hour"].CurrentCycle == nil ||
		inactive["five-hour"].CurrentCycle.ParentCycleID != nil {
		t.Fatalf("inactive container relationship: five-hour=%#v weekly=%#v", inactive["five-hour"], inactive["weekly"])
	}
}

func TestQuotaLifecycleAcceptsCompleteEmptyInventory(t *testing.T) {
	service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+quotaLifecycleDayMS)
	fiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", quotaLifecycleBaseMS, 5*60*60, 20)
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{fiveHour})
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+2*quotaLifecycleHourMS, nil)
	if got := queryQuotaLifecycleWindows(t, service, false)["five-hour"].Availability; got != "pending_absent" {
		t.Fatalf("first empty inventory availability = %q, want pending_absent", got)
	}
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+3*quotaLifecycleHourMS, []WindowInput{})
	if windows := queryQuotaLifecycleWindows(t, service, false); len(windows) != 0 {
		t.Fatalf("confirmed empty inventory windows = %#v", windows)
	}
}

func TestQuotaCycleResponseRequiresReliableCurrentBoundaryForForecast(t *testing.T) {
	scheduledEndMS := quotaLifecycleBaseMS + quotaLifecycleHourMS
	durationSeconds := int64(60 * 60)
	base := model.AccountQuotaCycle{
		State:            "active",
		ActualStartMS:    quotaLifecycleBaseMS,
		ScheduledEndMS:   &scheduledEndMS,
		BoundaryAccuracy: "exact",
		DurationSeconds:  &durationSeconds,
	}

	if response := quotaCycleResponse(&base, true); response == nil || !response.ForecastEligible {
		t.Fatalf("reliable current cycle = %#v", response)
	}
	estimated := base
	estimated.BoundaryAccuracy = "estimated"
	if response := quotaCycleResponse(&estimated, true); response == nil || response.ForecastEligible {
		t.Fatalf("estimated current cycle = %#v", response)
	}
	missingEnd := base
	missingEnd.ScheduledEndMS = nil
	if response := quotaCycleResponse(&missingEnd, true); response == nil || response.ForecastEligible {
		t.Fatalf("current cycle without scheduled end = %#v", response)
	}
}

func TestQuotaLifecycleScheduledRolloverUsesScheduledBoundaryAfterIdleGap(t *testing.T) {
	rolloverAtMS := quotaLifecycleBaseMS + 7*quotaLifecycleDayMS
	firstUseAtMS := rolloverAtMS + 2*quotaLifecycleHourMS
	service := newQuotaSnapshotTestService(t, rolloverAtMS+quotaLifecycleDayMS)
	first := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, 7*24*60*60, 80)
	writeQuotaLifecycleObservation(t, service, "complete", rolloverAtMS-quotaLifecycleHourMS, []WindowInput{first})
	second := quotaLifecycleFixedWindow("weekly", "weekly", rolloverAtMS, 7*24*60*60, 1)
	writeQuotaLifecycleObservation(t, service, "complete", firstUseAtMS, []WindowInput{second})

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != rolloverAtMS ||
		window.PreviousCycle == nil || window.PreviousCycle.EndReason != "scheduled" ||
		!window.PreviousCycle.ForecastEligible || window.PreviousCycle.ActualEndMS == nil ||
		*window.PreviousCycle.ActualEndMS != rolloverAtMS {
		t.Fatalf("scheduled rollover after idle gap = %#v", window)
	}
}

func TestQuotaLifecycleTreatsNegativeBoundaryJitterAsScheduledRollover(t *testing.T) {
	const durationSeconds = int64(7 * 24 * 60 * 60)
	oldStartMS := quotaLifecycleBaseMS
	oldEndMS := oldStartMS + durationSeconds*1000
	nextStartMS := oldEndMS - 5*1000
	nextEndMS := nextStartMS + durationSeconds*1000
	observedAtMS := oldEndMS + 5*60*1000
	service := newQuotaSnapshotTestService(t, observedAtMS+quotaLifecycleHourMS)

	first := quotaLifecycleFixedWindow("weekly", "weekly", oldStartMS, durationSeconds, 20)
	writeQuotaLifecycleObservation(t, service, "complete", oldStartMS+quotaLifecycleHourMS, []WindowInput{first})

	next := quotaLifecycleFixedWindow("weekly", "weekly", nextStartMS, durationSeconds, 8)
	next.Source = "api_query"
	next.BoundaryAccuracy = "derived"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "api_query", "api-negative-jitter", "codex:quota-windows",
			observedAtMS, []WindowInput{next},
		),
	}}); err != nil {
		t.Fatalf("write negative-jitter observation: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.PreviousCycle == nil {
		t.Fatalf("negative-jitter rollover did not expose both cycles: %#v", window)
	}
	if window.CurrentCycle.ID == window.PreviousCycle.ID {
		t.Fatalf("negative-jitter rollover reused the active cycle: %#v", window)
	}
	if window.PreviousCycle.State != "closed" || window.PreviousCycle.EndReason != "scheduled" ||
		window.PreviousCycle.ActualEndMS == nil || *window.PreviousCycle.ActualEndMS != oldEndMS {
		t.Fatalf("negative-jitter previous cycle = %#v", window.PreviousCycle)
	}
	if window.CurrentCycle.ActualStartMS != oldEndMS ||
		window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != nextStartMS ||
		window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != nextEndMS {
		t.Fatalf("negative-jitter current cycle = %#v", window.CurrentCycle)
	}
	if window.CurrentCycle.ActualStartMS != *window.PreviousCycle.ActualEndMS {
		t.Fatalf("negative-jitter transition has overlap or gap: %#v", window)
	}
}

func TestQuotaLifecycleNegativeBoundaryJitterCanonicalizesIndependentActualTransition(t *testing.T) {
	const durationSeconds = int64(5 * 60 * 60)
	oldStartMS := quotaLifecycleBaseMS
	oldEndMS := oldStartMS + durationSeconds*1000
	actualTransitionMS := oldStartMS + 4*quotaLifecycleHourMS
	nextStartMS := oldEndMS - 5*1000
	nextEndMS := nextStartMS + durationSeconds*1000
	observedAtMS := oldEndMS + 5*60*1000
	service := newQuotaSnapshotTestService(t, observedAtMS+quotaLifecycleHourMS)

	initial := quotaLifecycleFixedWindow("five-hour", "five_hour", oldStartMS, durationSeconds, 75)
	writeQuotaLifecycleObservation(t, service, "complete", oldStartMS+quotaLifecycleHourMS, []WindowInput{initial})

	reset := quotaLifecycleFixedWindow("five-hour", "five_hour", oldStartMS, durationSeconds, 1)
	reset.Source = "response_header"
	reset.BoundaryAccuracy = "derived"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "response_header", "header-early-reset", "codex:quota-windows",
			actualTransitionMS, []WindowInput{reset},
		),
	}}); err != nil {
		t.Fatalf("write independent actual transition: %v", err)
	}

	resetted := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if resetted.CurrentCycle == nil || resetted.CurrentCycle.ActualStartMS != actualTransitionMS ||
		resetted.CurrentCycle.ScheduledStartMS == nil || *resetted.CurrentCycle.ScheduledStartMS != oldStartMS {
		t.Fatalf("independent actual transition was not established: %#v", resetted)
	}
	oldCycleID := resetted.CurrentCycle.ID

	next := quotaLifecycleFixedWindow("five-hour", "five_hour", nextStartMS, durationSeconds, 8)
	next.Source = "api_query"
	next.BoundaryAccuracy = "derived"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "api_query", "api-negative-jitter-after-reset", "codex:quota-windows",
			observedAtMS, []WindowInput{next},
		),
	}}); err != nil {
		t.Fatalf("write negative-jitter observation after actual transition: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if window.CurrentCycle == nil || window.PreviousCycle == nil || window.PreviousCycle.ID != oldCycleID {
		t.Fatalf("negative-jitter rollover lost the independent transition cycle: %#v", window)
	}
	if window.PreviousCycle.ActualStartMS != actualTransitionMS ||
		window.PreviousCycle.ActualEndMS == nil || *window.PreviousCycle.ActualEndMS != oldEndMS ||
		window.PreviousCycle.EndReason != "scheduled" {
		t.Fatalf("negative-jitter closed cycle crossed the provider reset: %#v", window.PreviousCycle)
	}
	if window.CurrentCycle.ActualStartMS != oldEndMS ||
		window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != nextStartMS ||
		window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != nextEndMS {
		t.Fatalf("negative-jitter next cycle did not use the canonical transition: %#v", window.CurrentCycle)
	}
	if window.CurrentCycle.ActualStartMS != *window.PreviousCycle.ActualEndMS {
		t.Fatalf("negative-jitter transition has overlap or gap after actual reset: %#v", window)
	}
}

func TestQuotaLifecycleDoesNotUseProvisionalZeroAPINegativeJitterForScheduledRollover(t *testing.T) {
	const durationSeconds = int64(7 * 24 * 60 * 60)
	oldStartMS := quotaLifecycleBaseMS
	oldEndMS := oldStartMS + durationSeconds*1000
	provisionalStartMS := oldEndMS - 5*1000
	observedAtMS := oldEndMS + 30*1000
	service, path := newQuotaSnapshotTestServiceWithPath(t, observedAtMS+quotaLifecycleHourMS)

	oldCycle := quotaLifecycleFixedWindow("weekly", "weekly", oldStartMS, durationSeconds, 65)
	writeQuotaLifecycleObservation(t, service, "complete", oldStartMS+quotaLifecycleHourMS, []WindowInput{oldCycle})
	initial := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if initial.CurrentCycle == nil {
		t.Fatalf("initial provisional-zero lifecycle = %#v", initial)
	}
	oldCycleID := initial.CurrentCycle.ID

	provisional := quotaLifecycleFixedWindow("weekly", "weekly", provisionalStartMS, durationSeconds, 0)
	provisional.BoundaryAccuracy = "derived"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "api_query", "api-negative-jitter-provisional", "codex:quota-windows",
			observedAtMS, []WindowInput{provisional},
		),
	}}); err != nil {
		t.Fatalf("write provisional negative-jitter API boundary: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ID != oldCycleID ||
		window.CurrentCycle.State != "active" || window.PreviousCycle != nil {
		t.Fatalf("provisional negative-jitter API boundary changed lifecycle = %#v", window)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open provisional lifecycle database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var cycleCount, provisionalSnapshotCount int
	if err := db.QueryRow(`select count(*) from account_quota_cycles`).Scan(&cycleCount); err != nil {
		t.Fatalf("count provisional lifecycle cycles: %v", err)
	}
	if err := db.QueryRow(`select count(*) from account_quota_snapshots
		where source_observation_id = 'api-negative-jitter-provisional'
			and cycle_id is null and boundary_accuracy = 'unknown'`).Scan(&provisionalSnapshotCount); err != nil {
		t.Fatalf("count unassigned provisional snapshot: %v", err)
	}
	if cycleCount != 1 || provisionalSnapshotCount != 1 {
		t.Fatalf("cycle_count=%d provisional_snapshot_count=%d, want 1 and 1", cycleCount, provisionalSnapshotCount)
	}
}

func TestQuotaLifecycleExposesFiveHourAndWeeklyScheduledGapsAsPrevious(t *testing.T) {
	const gapMS = int64(8 * 60 * 1000)
	weeklyStartMS := quotaLifecycleBaseMS
	fiveHourStartMS := quotaLifecycleBaseMS
	weeklyEndMS := weeklyStartMS + 7*24*60*60*1000
	fiveHourEndMS := fiveHourStartMS + 5*60*60*1000
	weeklyNextStartMS := weeklyEndMS + gapMS
	fiveHourNextStartMS := fiveHourEndMS + gapMS
	service := newQuotaSnapshotTestService(t, weeklyNextStartMS+quotaLifecycleHourMS)

	firstWeekly := quotaLifecycleFixedWindow("weekly", "weekly", weeklyStartMS, 7*24*60*60, 80)
	firstFiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", fiveHourStartMS, 5*60*60, 70)
	writeQuotaLifecycleObservation(t, service, "complete", quotaLifecycleBaseMS+quotaLifecycleHourMS, []WindowInput{
		firstFiveHour,
		firstWeekly,
	})

	secondWeekly := quotaLifecycleFixedWindow("weekly", "weekly", weeklyNextStartMS, 7*24*60*60, 1)
	secondFiveHour := quotaLifecycleFixedWindow("five-hour", "five_hour", fiveHourNextStartMS, 5*60*60, 1)
	writeQuotaLifecycleObservation(t, service, "complete", weeklyNextStartMS+quotaLifecycleHourMS, []WindowInput{
		secondFiveHour,
		secondWeekly,
	})

	windows := queryQuotaLifecycleWindows(t, service, false)
	for id, bounds := range map[string][3]int64{
		"five-hour": {fiveHourStartMS, fiveHourEndMS, fiveHourNextStartMS},
		"weekly":    {weeklyStartMS, weeklyEndMS, weeklyNextStartMS},
	} {
		wantStart, wantEnd, wantCurrentStart := bounds[0], bounds[1], bounds[2]
		window := windows[id]
		if window.CurrentCycle == nil || window.PreviousCycle == nil ||
			window.PreviousCycle.ActualStartMS != wantStart || window.PreviousCycle.ActualEndMS == nil ||
			*window.PreviousCycle.ActualEndMS != wantEnd || window.PreviousCycle.EndReason != "scheduled" ||
			window.PreviousCycle.ActivationID != window.CurrentCycle.ActivationID ||
			window.CurrentCycle.ActualStartMS != wantCurrentStart {
			t.Fatalf("%s scheduled gap previous lifecycle = %#v", id, window)
		}
	}
}

func TestQuotaLifecycleAcceptsZeroUseAPIBoundaryAtScheduledRollover(t *testing.T) {
	const durationSeconds = int64(7 * 24 * 60 * 60)
	rolloverAtMS := quotaLifecycleBaseMS + durationSeconds*1000
	service := newQuotaSnapshotTestService(t, rolloverAtMS+quotaLifecycleDayMS)
	first := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, durationSeconds, 80)
	writeQuotaLifecycleObservation(t, service, "complete", rolloverAtMS-quotaLifecycleHourMS, []WindowInput{first})

	second := quotaLifecycleFixedWindow("weekly", "weekly", rolloverAtMS, durationSeconds, 0)
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "api_query", "api-scheduled-rollover", "codex:quota-windows",
			rolloverAtMS+1_000, []WindowInput{second},
		),
	}}); err != nil {
		t.Fatalf("write zero-use API scheduled rollover: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != rolloverAtMS ||
		window.CurrentCycle.State != "active" || window.PreviousCycle == nil ||
		window.PreviousCycle.ActualEndMS == nil || *window.PreviousCycle.ActualEndMS != rolloverAtMS ||
		window.PreviousCycle.EndReason != "scheduled" {
		t.Fatalf("zero-use API scheduled rollover lifecycle = %#v", window)
	}
}

func TestQuotaLifecycleRejectsUnreliableScheduledRolloverBoundary(t *testing.T) {
	const durationSeconds = int64(7 * 24 * 60 * 60)
	rolloverAtMS := quotaLifecycleBaseMS + durationSeconds*1000
	service := newQuotaSnapshotTestService(t, rolloverAtMS+quotaLifecycleDayMS)
	first := quotaLifecycleFixedWindow("weekly", "weekly", quotaLifecycleBaseMS, durationSeconds, 80)
	writeQuotaLifecycleObservation(t, service, "complete", rolloverAtMS-quotaLifecycleHourMS, []WindowInput{first})

	unreliable := quotaLifecycleFixedWindow("weekly", "weekly", rolloverAtMS, durationSeconds, 10)
	unreliable.BoundaryAccuracy = "estimated"
	writeQuotaLifecycleObservation(t, service, "complete", rolloverAtMS+1_000, []WindowInput{unreliable})

	window := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != quotaLifecycleBaseMS ||
		window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != rolloverAtMS ||
		window.PreviousCycle != nil {
		t.Fatalf("unreliable scheduled rollover changed lifecycle = %#v", window)
	}
}

func TestQuotaLifecycleRefreshesExpiredBoundaryFromFreshOverlappingObservation(t *testing.T) {
	tests := []struct {
		name                string
		source              string
		sourceObservationID string
		freshAccuracy       string
	}{
		{name: "exact api boundary", source: "api_query", sourceObservationID: "api-expired-refresh", freshAccuracy: "exact"},
		{name: "derived header boundary", source: "response_header", sourceObservationID: "header-expired-refresh", freshAccuracy: "derived"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			const durationSeconds = int64(5 * 60 * 60)
			firstStartMS := quotaLifecycleBaseMS
			firstEndMS := firstStartMS + durationSeconds*1000
			freshStartMS := firstStartMS + 3*quotaLifecycleHourMS
			freshEndMS := freshStartMS + durationSeconds*1000
			refreshAtMS := firstEndMS + quotaLifecycleHourMS
			service, path := newQuotaSnapshotTestServiceWithPath(t, refreshAtMS+quotaLifecycleHourMS)

			first := quotaLifecycleFixedWindow("five-hour", "five_hour", firstStartMS, durationSeconds, 40)
			writeQuotaLifecycleObservation(t, service, "complete", firstStartMS+quotaLifecycleHourMS, []WindowInput{first})
			initial := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
			if initial.CurrentCycle == nil {
				t.Fatalf("initial fixed lifecycle = %#v", initial)
			}
			initialCycleID := initial.CurrentCycle.ID

			fresh := quotaLifecycleFixedWindow("five-hour", "five_hour", freshStartMS, durationSeconds, 45)
			fresh.Source = testCase.source
			fresh.BoundaryAccuracy = testCase.freshAccuracy
			if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
				quotaLifecycleWriteEntryWithObservation(
					"complete", testCase.source, testCase.sourceObservationID, "codex:quota-windows",
					refreshAtMS, []WindowInput{fresh},
				),
			}}); err != nil {
				t.Fatalf("write fresh overlapping observation: %v", err)
			}

			window := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
			if window.CurrentCycle == nil || window.CurrentCycle.ID != initialCycleID || window.PreviousCycle != nil {
				t.Fatalf("expired boundary refresh split the cycle: %#v", window)
			}
			if window.CurrentCycle.ActualStartMS != freshStartMS ||
				window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != freshStartMS ||
				window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != freshEndMS ||
				window.CurrentCycle.DurationSeconds == nil || *window.CurrentCycle.DurationSeconds != durationSeconds ||
				window.CurrentCycle.BoundaryAccuracy != testCase.freshAccuracy {
				t.Fatalf("expired cycle boundary was not refreshed: %#v", window.CurrentCycle)
			}
			if window.CycleStartMS == nil || *window.CycleStartMS != freshStartMS ||
				window.CycleEndMS == nil || *window.CycleEndMS != freshEndMS {
				t.Fatalf("window boundary did not follow refreshed cycle: %#v", window)
			}
			if window.Stale {
				t.Fatalf("refreshed window must not be stale: %#v", window)
			}

			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatalf("open expired-refresh database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			var cycleCount int
			if err := db.QueryRow(`select count(*) from account_quota_cycles`).Scan(&cycleCount); err != nil {
				t.Fatalf("count expired-refresh cycles: %v", err)
			}
			if cycleCount != 1 {
				t.Fatalf("expired-refresh cycle count = %d, want 1", cycleCount)
			}
		})
	}
}

func TestQuotaLifecycleRefreshesExpiredCalendarBoundaryFromFreshOverlappingObservation(t *testing.T) {
	const durationSeconds = int64(5 * 60 * 60)
	firstStartMS := quotaLifecycleBaseMS
	firstEndMS := firstStartMS + durationSeconds*1000
	freshStartMS := firstStartMS + 3*quotaLifecycleHourMS
	freshEndMS := freshStartMS + durationSeconds*1000
	refreshAtMS := firstEndMS + quotaLifecycleHourMS
	service, path := newQuotaSnapshotTestServiceWithPath(t, refreshAtMS+quotaLifecycleHourMS)

	first := quotaLifecycleFixedWindow("calendar-week", "weekly", firstStartMS, durationSeconds, 40)
	first.WindowMode = "calendar"
	writeQuotaLifecycleObservation(t, service, "complete", firstStartMS+quotaLifecycleHourMS, []WindowInput{first})
	initial := queryQuotaLifecycleWindows(t, service, false)["calendar-week"]
	if initial.CurrentCycle == nil {
		t.Fatalf("initial calendar lifecycle = %#v", initial)
	}
	initialCycleID := initial.CurrentCycle.ID

	fresh := quotaLifecycleFixedWindow("calendar-week", "weekly", freshStartMS, durationSeconds, 45)
	fresh.WindowMode = "calendar"
	fresh.Source = "response_header"
	fresh.BoundaryAccuracy = "derived"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "response_header", "header-calendar-refresh", "codex:quota-windows",
			refreshAtMS, []WindowInput{fresh},
		),
	}}); err != nil {
		t.Fatalf("write fresh overlapping calendar observation: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["calendar-week"]
	if window.CurrentCycle == nil || window.CurrentCycle.ID != initialCycleID || window.PreviousCycle != nil {
		t.Fatalf("calendar expired refresh split the cycle: %#v", window)
	}
	if window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != freshStartMS ||
		window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != freshEndMS ||
		window.CurrentCycle.DurationSeconds == nil || *window.CurrentCycle.DurationSeconds != durationSeconds ||
		window.CurrentCycle.BoundaryAccuracy != "derived" {
		t.Fatalf("calendar cycle boundary was not refreshed: %#v", window.CurrentCycle)
	}
	if window.CycleStartMS == nil || *window.CycleStartMS != freshStartMS ||
		window.CycleEndMS == nil || *window.CycleEndMS != freshEndMS {
		t.Fatalf("window boundary did not follow refreshed calendar cycle: %#v", window)
	}
	if window.Stale {
		t.Fatalf("refreshed calendar window must not be stale: %#v", window)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open calendar-refresh database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var cycleCount int
	if err := db.QueryRow(`select count(*) from account_quota_cycles`).Scan(&cycleCount); err != nil {
		t.Fatalf("count calendar-refresh cycles: %v", err)
	}
	if cycleCount != 1 {
		t.Fatalf("calendar-refresh cycle count = %d, want 1", cycleCount)
	}
}

func TestQuotaLifecycleKeepsExpiredBoundaryGuardBeforeScheduledEnd(t *testing.T) {
	const durationSeconds = int64(5 * 60 * 60)
	firstStartMS := quotaLifecycleBaseMS
	firstEndMS := firstStartMS + durationSeconds*1000
	candidateStartMS := firstStartMS + 2*quotaLifecycleHourMS
	service := newQuotaSnapshotTestService(t, firstEndMS-quotaLifecycleHourMS)

	first := quotaLifecycleFixedWindow("five-hour", "five_hour", firstStartMS, durationSeconds, 40)
	writeQuotaLifecycleObservation(t, service, "complete", firstStartMS+quotaLifecycleHourMS, []WindowInput{first})

	candidate := quotaLifecycleFixedWindow("five-hour", "five_hour", candidateStartMS, durationSeconds, 45)
	writeQuotaLifecycleObservation(t, service, "complete", firstStartMS+4*quotaLifecycleHourMS, []WindowInput{candidate})

	window := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if window.CurrentCycle == nil || window.CurrentCycle.ActualStartMS != firstStartMS ||
		window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != firstEndMS ||
		window.PreviousCycle != nil {
		t.Fatalf("pre-expiry candidate changed lifecycle = %#v", window)
	}
	if window.CycleStartMS == nil || *window.CycleStartMS != firstStartMS ||
		window.CycleEndMS == nil || *window.CycleEndMS != firstEndMS {
		t.Fatalf("pre-expiry candidate adopted fresh boundary = %#v", window)
	}
}

func TestQuotaLifecycleDoesNotReplaceFreshCurrentBoundaryWithLaterExpiredExactEvidence(t *testing.T) {
	const durationSeconds = int64(5 * 60 * 60)
	firstStartMS := quotaLifecycleBaseMS
	firstEndMS := firstStartMS + durationSeconds*1000
	freshStartMS := firstStartMS + 3*quotaLifecycleHourMS
	freshEndMS := freshStartMS + durationSeconds*1000
	refreshAtMS := firstEndMS + quotaLifecycleHourMS
	expiredAtMS := refreshAtMS + 10*60*1000
	service, path := newQuotaSnapshotTestServiceWithPath(t, expiredAtMS+50*60*1000)

	first := quotaLifecycleFixedWindow("five-hour", "five_hour", firstStartMS, durationSeconds, 40)
	writeQuotaLifecycleObservation(t, service, "complete", firstStartMS+quotaLifecycleHourMS, []WindowInput{first})

	fresh := quotaLifecycleFixedWindow("five-hour", "five_hour", freshStartMS, durationSeconds, 45)
	fresh.Source = "response_header"
	fresh.BoundaryAccuracy = "derived"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "response_header", "header-expired-refresh", "codex:quota-windows",
			refreshAtMS, []WindowInput{fresh},
		),
	}}); err != nil {
		t.Fatalf("write fresh overlapping observation: %v", err)
	}
	refreshed := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if refreshed.CurrentCycle == nil || refreshed.CurrentCycle.ScheduledStartMS == nil ||
		*refreshed.CurrentCycle.ScheduledStartMS != freshStartMS ||
		refreshed.CurrentCycle.ScheduledEndMS == nil || *refreshed.CurrentCycle.ScheduledEndMS != freshEndMS ||
		refreshed.CurrentCycle.BoundaryAccuracy != "derived" || refreshed.Stale {
		t.Fatalf("fresh derived boundary was not refreshed: %#v", refreshed)
	}
	currentCycleID := refreshed.CurrentCycle.ID

	expired := quotaLifecycleFixedWindow("five-hour", "five_hour", firstStartMS, durationSeconds, 46)
	writeQuotaLifecycleObservation(t, service, "complete", expiredAtMS, []WindowInput{expired})

	window := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if window.CurrentCycle == nil || window.CurrentCycle.ID != currentCycleID || window.PreviousCycle != nil {
		t.Fatalf("expired exact evidence split or replaced the cycle: %#v", window)
	}
	if window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != freshStartMS ||
		window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != freshEndMS ||
		window.CurrentCycle.BoundaryAccuracy != "derived" {
		t.Fatalf("expired exact evidence replaced the current boundary: %#v", window.CurrentCycle)
	}
	if window.CycleStartMS == nil || *window.CycleStartMS != freshStartMS ||
		window.CycleEndMS == nil || *window.CycleEndMS != freshEndMS || window.Stale {
		t.Fatalf("window boundary did not keep the fresh current cycle: %#v", window)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open expired-evidence database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var cycleCount int
	if err := db.QueryRow(`select count(*) from account_quota_cycles`).Scan(&cycleCount); err != nil {
		t.Fatalf("count expired-evidence cycles: %v", err)
	}
	if cycleCount != 1 {
		t.Fatalf("expired-evidence cycle count = %d, want 1", cycleCount)
	}
}

func TestQuotaLifecycleExpiredBoundaryRefreshPreservesConfirmedResetTransition(t *testing.T) {
	const durationSeconds = int64(5 * 60 * 60)
	firstStartMS := quotaLifecycleBaseMS
	firstEndMS := firstStartMS + durationSeconds*1000
	resetAtMS := firstStartMS + 4*quotaLifecycleHourMS
	freshStartMS := firstStartMS + 3*quotaLifecycleHourMS
	freshEndMS := freshStartMS + durationSeconds*1000
	refreshAtMS := firstEndMS + quotaLifecycleHourMS
	service, path := newQuotaSnapshotTestServiceWithPath(t, refreshAtMS+quotaLifecycleHourMS)

	initial := quotaLifecycleFixedWindow("five-hour", "five_hour", firstStartMS, durationSeconds, 75)
	writeQuotaLifecycleObservation(t, service, "complete", firstStartMS+3*quotaLifecycleHourMS, []WindowInput{initial})

	reset := quotaLifecycleFixedWindow("five-hour", "five_hour", firstStartMS, durationSeconds, 1)
	writeQuotaLifecycleObservation(t, service, "complete", resetAtMS, []WindowInput{reset})

	resetted := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if resetted.PreviousCycle == nil || resetted.PreviousCycle.ActualEndMS == nil ||
		*resetted.PreviousCycle.ActualEndMS != resetAtMS ||
		resetted.CurrentCycle == nil || resetted.CurrentCycle.ActualStartMS != resetAtMS ||
		resetted.CurrentCycle.ScheduledStartMS == nil || *resetted.CurrentCycle.ScheduledStartMS != firstStartMS ||
		resetted.CurrentCycle.ScheduledEndMS == nil || *resetted.CurrentCycle.ScheduledEndMS != firstEndMS {
		t.Fatalf("same-boundary counter reset = %#v", resetted)
	}
	currentCycleID := resetted.CurrentCycle.ID
	previousCycleID := resetted.PreviousCycle.ID

	fresh := quotaLifecycleFixedWindow("five-hour", "five_hour", freshStartMS, durationSeconds, 10)
	writeQuotaLifecycleObservation(t, service, "complete", refreshAtMS, []WindowInput{fresh})

	window := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if window.CurrentCycle == nil || window.CurrentCycle.ID != currentCycleID ||
		window.PreviousCycle == nil || window.PreviousCycle.ID != previousCycleID {
		t.Fatalf("expired refresh changed cycle identity: %#v", window)
	}
	if window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != freshStartMS ||
		window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != freshEndMS {
		t.Fatalf("expired refresh did not update scheduled boundary: %#v", window.CurrentCycle)
	}
	if window.CurrentCycle.ActualStartMS != resetAtMS {
		t.Fatalf("confirmed reset transition was overwritten: %#v", window.CurrentCycle)
	}
	if window.PreviousCycle.ActualEndMS == nil || *window.PreviousCycle.ActualEndMS != resetAtMS ||
		window.CurrentCycle.ActualStartMS != *window.PreviousCycle.ActualEndMS {
		t.Fatalf("previous cycle no longer meets the confirmed transition: %#v", window)
	}
	if window.CycleStartMS == nil || *window.CycleStartMS != resetAtMS ||
		window.CycleEndMS == nil || *window.CycleEndMS != freshEndMS || window.Stale {
		t.Fatalf("window boundary = %#v", window)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open reset-transition database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var cycleCount int
	if err := db.QueryRow(`select count(*) from account_quota_cycles`).Scan(&cycleCount); err != nil {
		t.Fatalf("count reset-transition cycles: %v", err)
	}
	if cycleCount != 2 {
		t.Fatalf("reset-transition cycle count = %d, want 2", cycleCount)
	}
}

func TestQuotaLifecycleAccuracyUpgradeAfterExpiredRefreshPreservesConfirmedResetTransition(t *testing.T) {
	const durationSeconds = int64(5 * 60 * 60)
	firstStartMS := quotaLifecycleBaseMS
	firstEndMS := firstStartMS + durationSeconds*1000
	resetAtMS := firstStartMS + 4*quotaLifecycleHourMS
	freshStartMS := firstStartMS + 3*quotaLifecycleHourMS
	freshEndMS := freshStartMS + durationSeconds*1000
	refreshAtMS := firstEndMS + quotaLifecycleHourMS
	upgradeAtMS := refreshAtMS + 10*60*1000
	service, path := newQuotaSnapshotTestServiceWithPath(t, upgradeAtMS+50*60*1000)

	initial := quotaLifecycleFixedWindow("five-hour", "five_hour", firstStartMS, durationSeconds, 75)
	writeQuotaLifecycleObservation(t, service, "complete", firstStartMS+3*quotaLifecycleHourMS, []WindowInput{initial})

	reset := quotaLifecycleFixedWindow("five-hour", "five_hour", firstStartMS, durationSeconds, 1)
	writeQuotaLifecycleObservation(t, service, "complete", resetAtMS, []WindowInput{reset})

	resetted := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if resetted.PreviousCycle == nil || resetted.PreviousCycle.ActualEndMS == nil ||
		*resetted.PreviousCycle.ActualEndMS != resetAtMS ||
		resetted.CurrentCycle == nil || resetted.CurrentCycle.ActualStartMS != resetAtMS ||
		resetted.CurrentCycle.ScheduledStartMS == nil || *resetted.CurrentCycle.ScheduledStartMS != firstStartMS ||
		resetted.CurrentCycle.ScheduledEndMS == nil || *resetted.CurrentCycle.ScheduledEndMS != firstEndMS {
		t.Fatalf("same-boundary counter reset = %#v", resetted)
	}
	currentCycleID := resetted.CurrentCycle.ID
	previousCycleID := resetted.PreviousCycle.ID

	fresh := quotaLifecycleFixedWindow("five-hour", "five_hour", freshStartMS, durationSeconds, 10)
	fresh.Source = "response_header"
	fresh.BoundaryAccuracy = "derived"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "response_header", "header-expired-refresh", "codex:quota-windows",
			refreshAtMS, []WindowInput{fresh},
		),
	}}); err != nil {
		t.Fatalf("write expired refresh observation: %v", err)
	}
	refreshed := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if refreshed.CurrentCycle == nil || refreshed.CurrentCycle.ID != currentCycleID ||
		refreshed.CurrentCycle.ScheduledStartMS == nil || *refreshed.CurrentCycle.ScheduledStartMS != freshStartMS ||
		refreshed.CurrentCycle.ScheduledEndMS == nil || *refreshed.CurrentCycle.ScheduledEndMS != freshEndMS ||
		refreshed.CurrentCycle.BoundaryAccuracy != "derived" || refreshed.CurrentCycle.ActualStartMS != resetAtMS ||
		refreshed.PreviousCycle == nil || refreshed.PreviousCycle.ID != previousCycleID ||
		refreshed.PreviousCycle.ActualEndMS == nil || *refreshed.PreviousCycle.ActualEndMS != resetAtMS {
		t.Fatalf("expired refresh lost the confirmed transition: %#v", refreshed)
	}

	upgrade := quotaLifecycleFixedWindow("five-hour", "five_hour", freshStartMS, durationSeconds, 11)
	upgrade.Source = "api_query"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "api_query", "api-boundary-upgrade", "codex:quota-windows",
			upgradeAtMS, []WindowInput{upgrade},
		),
	}}); err != nil {
		t.Fatalf("write accuracy upgrade observation: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if window.CurrentCycle == nil || window.CurrentCycle.ID != currentCycleID ||
		window.PreviousCycle == nil || window.PreviousCycle.ID != previousCycleID {
		t.Fatalf("accuracy upgrade changed cycle identity: %#v", window)
	}
	if window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != freshStartMS ||
		window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != freshEndMS ||
		window.CurrentCycle.BoundaryAccuracy != "exact" || window.CurrentCycle.ActualStartMS != resetAtMS {
		t.Fatalf("accuracy upgrade overwrote the confirmed transition: %#v", window.CurrentCycle)
	}
	if window.PreviousCycle.ActualEndMS == nil || *window.PreviousCycle.ActualEndMS != resetAtMS ||
		window.CurrentCycle.ActualStartMS != *window.PreviousCycle.ActualEndMS {
		t.Fatalf("previous cycle no longer meets the confirmed transition: %#v", window)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open accuracy-upgrade database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var cycleCount int
	if err := db.QueryRow(`select count(*) from account_quota_cycles`).Scan(&cycleCount); err != nil {
		t.Fatalf("count accuracy-upgrade cycles: %v", err)
	}
	if cycleCount != 2 {
		t.Fatalf("accuracy-upgrade cycle count = %d, want 2", cycleCount)
	}
}

func TestQuotaLifecycleExpiredBoundaryEvidenceCannotCreateStaleResetCycle(t *testing.T) {
	const durationSeconds = int64(5 * 60 * 60)
	firstStartMS := quotaLifecycleBaseMS
	firstEndMS := firstStartMS + durationSeconds*1000
	freshStartMS := firstStartMS + 3*quotaLifecycleHourMS
	freshEndMS := freshStartMS + durationSeconds*1000
	refreshAtMS := firstEndMS + quotaLifecycleHourMS
	staleAtMS := refreshAtMS + 10*60*1000
	service, path := newQuotaSnapshotTestServiceWithPath(t, staleAtMS+50*60*1000)

	first := quotaLifecycleFixedWindow("five-hour", "five_hour", firstStartMS, durationSeconds, 40)
	writeQuotaLifecycleObservation(t, service, "complete", firstStartMS+quotaLifecycleHourMS, []WindowInput{first})

	fresh := quotaLifecycleFixedWindow("five-hour", "five_hour", freshStartMS, durationSeconds, 45)
	fresh.Source = "response_header"
	fresh.BoundaryAccuracy = "derived"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "response_header", "header-expired-refresh", "codex:quota-windows",
			refreshAtMS, []WindowInput{fresh},
		),
	}}); err != nil {
		t.Fatalf("write fresh overlapping observation: %v", err)
	}
	refreshed := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if refreshed.CurrentCycle == nil || refreshed.CurrentCycle.ScheduledStartMS == nil ||
		*refreshed.CurrentCycle.ScheduledStartMS != freshStartMS ||
		refreshed.CurrentCycle.ScheduledEndMS == nil || *refreshed.CurrentCycle.ScheduledEndMS != freshEndMS ||
		refreshed.CurrentCycle.BoundaryAccuracy != "derived" || refreshed.Stale {
		t.Fatalf("fresh derived boundary was not refreshed: %#v", refreshed)
	}

	stale := quotaLifecycleFixedWindow("five-hour", "five_hour", firstStartMS, durationSeconds, 1)
	stale.Source = "api_query"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "api_query", "api-stale-reset-evidence", "codex:quota-windows",
			staleAtMS, []WindowInput{stale},
		),
	}}); err != nil {
		t.Fatalf("write stale expired observation: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if window.CurrentCycle == nil || window.PreviousCycle == nil {
		t.Fatalf("stale boundary evidence did not reconcile a reset: %#v", window)
	}
	if window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != freshStartMS ||
		window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != freshEndMS {
		t.Fatalf("stale boundary drove the reset cycle timing: %#v", window.CurrentCycle)
	}
	if window.PreviousCycle.ActualEndMS == nil || *window.PreviousCycle.ActualEndMS != staleAtMS ||
		window.CurrentCycle.ActualStartMS != staleAtMS {
		t.Fatalf("canonicalized counter reset transition = %#v", window)
	}
	if window.PreviousCycle.ActualStartMS >= *window.PreviousCycle.ActualEndMS {
		t.Fatalf("zero-length reset fragment = %#v", window.PreviousCycle)
	}
	if window.Stale {
		t.Fatalf("current cycle is stale after canonicalized reset: %#v", window)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open stale-reset database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var cycleCount int
	if err := db.QueryRow(`select count(*) from account_quota_cycles`).Scan(&cycleCount); err != nil {
		t.Fatalf("count stale-reset cycles: %v", err)
	}
	if cycleCount != 2 {
		t.Fatalf("stale-reset cycle count = %d, want 2", cycleCount)
	}
}

func TestQuotaLifecycleUnreliableExpiredBoundaryCannotGainResetAuthority(t *testing.T) {
	const durationSeconds = int64(5 * 60 * 60)
	firstStartMS := quotaLifecycleBaseMS
	firstEndMS := firstStartMS + durationSeconds*1000
	freshStartMS := firstStartMS + 3*quotaLifecycleHourMS
	freshEndMS := freshStartMS + durationSeconds*1000
	refreshAtMS := firstEndMS + quotaLifecycleHourMS
	staleAtMS := refreshAtMS + 10*60*1000
	service, path := newQuotaSnapshotTestServiceWithPath(t, staleAtMS+50*60*1000)

	first := quotaLifecycleFixedWindow("five-hour", "five_hour", firstStartMS, durationSeconds, 40)
	writeQuotaLifecycleObservation(t, service, "complete", firstStartMS+quotaLifecycleHourMS, []WindowInput{first})

	fresh := quotaLifecycleFixedWindow("five-hour", "five_hour", freshStartMS, durationSeconds, 45)
	fresh.Source = "response_header"
	fresh.BoundaryAccuracy = "derived"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "response_header", "header-expired-refresh", "codex:quota-windows",
			refreshAtMS, []WindowInput{fresh},
		),
	}}); err != nil {
		t.Fatalf("write fresh overlapping observation: %v", err)
	}
	refreshed := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if refreshed.CurrentCycle == nil || refreshed.CurrentCycle.ScheduledStartMS == nil ||
		*refreshed.CurrentCycle.ScheduledStartMS != freshStartMS ||
		refreshed.CurrentCycle.ScheduledEndMS == nil || *refreshed.CurrentCycle.ScheduledEndMS != freshEndMS ||
		refreshed.CurrentCycle.BoundaryAccuracy != "derived" || refreshed.PreviousCycle != nil || refreshed.Stale {
		t.Fatalf("fresh derived boundary was not refreshed: %#v", refreshed)
	}
	currentCycleID := refreshed.CurrentCycle.ID

	stale := quotaLifecycleFixedWindow("five-hour", "five_hour", firstStartMS, durationSeconds, 1)
	stale.Source = "api_query"
	stale.BoundaryAccuracy = "estimated"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "api_query", "api-stale-unreliable", "codex:quota-windows",
			staleAtMS, []WindowInput{stale},
		),
	}}); err != nil {
		t.Fatalf("write stale unreliable observation: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if window.CurrentCycle == nil || window.CurrentCycle.ID != currentCycleID || window.PreviousCycle != nil {
		t.Fatalf("unreliable expired boundary gained reset authority: %#v", window)
	}
	if window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != freshStartMS ||
		window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != freshEndMS {
		t.Fatalf("unreliable boundary changed the current boundary: %#v", window.CurrentCycle)
	}
	if window.Stale {
		t.Fatalf("current cycle became stale after unreliable evidence: %#v", window)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open unreliable-evidence database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var cycleCount int
	if err := db.QueryRow(`select count(*) from account_quota_cycles`).Scan(&cycleCount); err != nil {
		t.Fatalf("count unreliable-evidence cycles: %v", err)
	}
	if cycleCount != 1 {
		t.Fatalf("unreliable-evidence cycle count = %d, want 1", cycleCount)
	}
}

func TestQuotaLifecycleStaleBoundaryAfterActiveExpiryCannotResetLifecycle(t *testing.T) {
	const durationSeconds = int64(5 * 60 * 60)
	firstStartMS := quotaLifecycleBaseMS
	firstEndMS := firstStartMS + durationSeconds*1000
	freshStartMS := firstStartMS + 3*quotaLifecycleHourMS
	freshEndMS := freshStartMS + durationSeconds*1000
	refreshAtMS := firstEndMS + quotaLifecycleHourMS
	staleAtMS := freshEndMS + 10*60*1000
	nextRefreshAtMS := staleAtMS + 10*60*1000
	nextStartMS := freshEndMS - quotaLifecycleHourMS
	nextEndMS := nextStartMS + durationSeconds*1000
	service, path := newQuotaSnapshotTestServiceWithPath(t, nextRefreshAtMS+40*60*1000)

	first := quotaLifecycleFixedWindow("five-hour", "five_hour", firstStartMS, durationSeconds, 40)
	writeQuotaLifecycleObservation(t, service, "complete", firstStartMS+quotaLifecycleHourMS, []WindowInput{first})

	fresh := quotaLifecycleFixedWindow("five-hour", "five_hour", freshStartMS, durationSeconds, 45)
	fresh.Source = "response_header"
	fresh.BoundaryAccuracy = "derived"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "response_header", "header-expired-refresh", "codex:quota-windows",
			refreshAtMS, []WindowInput{fresh},
		),
	}}); err != nil {
		t.Fatalf("write fresh overlapping observation: %v", err)
	}
	refreshed := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if refreshed.CurrentCycle == nil || refreshed.CurrentCycle.ScheduledStartMS == nil ||
		*refreshed.CurrentCycle.ScheduledStartMS != freshStartMS ||
		refreshed.CurrentCycle.ScheduledEndMS == nil || *refreshed.CurrentCycle.ScheduledEndMS != freshEndMS ||
		refreshed.PreviousCycle != nil {
		t.Fatalf("fresh derived boundary was not refreshed: %#v", refreshed)
	}
	currentCycleID := refreshed.CurrentCycle.ID

	stale := quotaLifecycleFixedWindow("five-hour", "five_hour", firstStartMS, durationSeconds, 1)
	stale.Source = "api_query"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "api_query", "api-stale-after-expiry", "codex:quota-windows",
			staleAtMS, []WindowInput{stale},
		),
	}}); err != nil {
		t.Fatalf("write stale observation after active expiry: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if window.CurrentCycle == nil || window.CurrentCycle.ID != currentCycleID || window.PreviousCycle != nil {
		t.Fatalf("stale boundary after active expiry split the lifecycle: %#v", window)
	}
	if window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != freshStartMS ||
		window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != freshEndMS ||
		window.CurrentCycle.ActualStartMS != freshStartMS ||
		window.CurrentCycle.BoundaryAccuracy != "derived" {
		t.Fatalf("stale exact evidence rewound the active boundary: %#v", window.CurrentCycle)
	}
	if !window.Stale {
		t.Fatalf("expired preserved boundary must report stale rather than roll back: %#v", window)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open post-expiry database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var staleFragments, cycleCount int
	if err := db.QueryRow(`select count(*) from account_quota_cycles
		where actual_end_ms is not null and actual_end_ms <= actual_start_ms`).Scan(&staleFragments); err != nil {
		t.Fatalf("count post-expiry fragments: %v", err)
	}
	if staleFragments != 0 {
		t.Fatalf("post-expiry zero-length fragments = %d, want 0", staleFragments)
	}
	if err := db.QueryRow(`select count(*) from account_quota_cycles`).Scan(&cycleCount); err != nil {
		t.Fatalf("count post-expiry cycles: %v", err)
	}
	if cycleCount != 1 {
		t.Fatalf("post-expiry cycle count = %d, want 1", cycleCount)
	}

	nextFresh := quotaLifecycleFixedWindow("five-hour", "five_hour", nextStartMS, durationSeconds, 2)
	nextFresh.Source = "response_header"
	nextFresh.BoundaryAccuracy = "derived"
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "response_header", "header-next-fresh", "codex:quota-windows",
			nextRefreshAtMS, []WindowInput{nextFresh},
		),
	}}); err != nil {
		t.Fatalf("write next fresh observation: %v", err)
	}

	window = queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if window.CurrentCycle == nil || window.CurrentCycle.ID != currentCycleID ||
		window.PreviousCycle != nil || window.Stale {
		t.Fatalf("next fresh boundary misrouted after stale evidence: %#v", window)
	}
	if window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != nextStartMS ||
		window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != nextEndMS ||
		window.CurrentCycle.ActualStartMS != nextStartMS ||
		window.CurrentCycle.BoundaryAccuracy != "derived" {
		t.Fatalf("next fresh boundary did not refresh the same cycle: %#v", window.CurrentCycle)
	}
	if err := db.QueryRow(`select count(*) from account_quota_cycles`).Scan(&cycleCount); err != nil {
		t.Fatalf("count post-refresh cycles: %v", err)
	}
	if cycleCount != 1 {
		t.Fatalf("post-refresh cycle count = %d, want 1", cycleCount)
	}
}

func TestQuotaLifecycleAllowsHigherAccuracyCorrectionWithinBoundaryJitter(t *testing.T) {
	const durationSeconds = int64(5 * 60 * 60)
	activeStartMS := quotaLifecycleBaseMS + 3*quotaLifecycleHourMS + 30*1000
	correctedStartMS := quotaLifecycleBaseMS + 3*quotaLifecycleHourMS
	upgradeAtMS := activeStartMS + 2*quotaLifecycleHourMS
	service, path := newQuotaSnapshotTestServiceWithPath(t, upgradeAtMS+quotaLifecycleHourMS)

	active := quotaLifecycleFixedWindow("five-hour", "five_hour", activeStartMS, durationSeconds, 45)
	active.Source = "response_header"
	active.BoundaryAccuracy = "derived"
	writeQuotaLifecycleObservation(t, service, "complete", activeStartMS+quotaLifecycleHourMS, []WindowInput{active})
	initial := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if initial.CurrentCycle == nil {
		t.Fatalf("initial derived lifecycle = %#v", initial)
	}
	cycleID := initial.CurrentCycle.ID

	corrected := quotaLifecycleFixedWindow("five-hour", "five_hour", correctedStartMS, durationSeconds, 46)
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntryWithObservation(
			"complete", "api_query", "api-jitter-upgrade", "codex:quota-windows",
			upgradeAtMS, []WindowInput{corrected},
		),
	}}); err != nil {
		t.Fatalf("write jitter upgrade observation: %v", err)
	}

	window := queryQuotaLifecycleWindows(t, service, false)["five-hour"]
	if window.CurrentCycle == nil || window.CurrentCycle.ID != cycleID || window.PreviousCycle != nil ||
		window.CurrentCycle.BoundaryAccuracy != "exact" {
		t.Fatalf("jitter-range accuracy upgrade was rejected: %#v", window)
	}
	if window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != correctedStartMS ||
		window.CurrentCycle.ActualStartMS != correctedStartMS {
		t.Fatalf("jitter upgrade boundary = %#v", window.CurrentCycle)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open jitter-upgrade database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var cycleCount int
	if err := db.QueryRow(`select count(*) from account_quota_cycles`).Scan(&cycleCount); err != nil {
		t.Fatalf("count jitter-upgrade cycles: %v", err)
	}
	if cycleCount != 1 {
		t.Fatalf("jitter-upgrade cycle count = %d, want 1", cycleCount)
	}
}

func TestQuotaLifecycleReclassifiesLegacyCodexSparkAllScope(t *testing.T) {
	service, path := newQuotaSnapshotTestServiceWithPath(t, quotaLifecycleBaseMS+quotaLifecycleDayMS)
	legacy := quotaLifecycleFixedWindow(
		"legacy-codex-window",
		"weekly",
		quotaLifecycleBaseMS,
		7*24*60*60,
		0,
	)
	writeQuotaLifecycleObservation(
		t,
		service,
		"complete",
		quotaLifecycleBaseMS+quotaLifecycleHourMS,
		[]WindowInput{legacy},
	)
	rewriteQuotaLifecycleProviderWindowID(t, path, "legacy-codex-window", "fast-coding-weekly-0")

	scoped := quotaLifecycleFixedWindow(
		"spark-weekly-0",
		"weekly",
		quotaLifecycleBaseMS,
		7*24*60*60,
		0,
	)
	scoped.ModelScopeKind = "models"
	scoped.ModelIDs = []string{codexquota.SparkModelID}
	scoped.ProviderWindowAliases = []string{"fast-coding-weekly-0"}
	writeQuotaLifecycleObservation(
		t,
		service,
		"complete",
		quotaLifecycleBaseMS+2*quotaLifecycleHourMS,
		[]WindowInput{scoped},
	)

	result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-lifecycle", Provider: "codex", Account: quotaSnapshotTestAccount(),
	}}})
	if err != nil {
		t.Fatalf("query active reclassified Spark window: %v", err)
	}
	if len(result.Items) != 1 || len(result.Items[0].Windows) != 1 {
		t.Fatalf("active reclassified Spark windows = %#v", result)
	}
	active := result.Items[0].Windows[0]
	if active.ProviderWindowID != "spark-weekly-0" || active.ModelScopeKind != "models" ||
		len(active.ModelIDs) != 1 || active.ModelIDs[0] != codexquota.SparkModelID || active.Availability != "active" {
		t.Fatalf("active reclassified Spark window = %#v", active)
	}

	all, err := service.Query(context.Background(), QueryRequest{
		Accounts: []QueryAccount{{
			RowKey: "row-lifecycle", Provider: "codex", Account: quotaSnapshotTestAccount(),
		}},
		IncludeInactive: true,
	})
	if err != nil {
		t.Fatalf("query inactive reclassified Spark window: %v", err)
	}
	if len(all.Items) != 1 || len(all.Items[0].Windows) != 2 {
		t.Fatalf("all reclassified Spark windows = %#v", all)
	}
	var legacyInactive bool
	for _, window := range all.Items[0].Windows {
		if window.ModelScopeKind == "all" && window.Availability == "inactive" {
			legacyInactive = true
		}
	}
	if !legacyInactive {
		t.Fatalf("legacy Spark all-scope window was not retained as inactive: %#v", all.Items[0].Windows)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open reclassification database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var activationReason, cycleReason string
	if err := db.QueryRow(`select a.deactivation_reason
		from account_quota_window_activations a
		join account_quota_windows w on w.id = a.window_id
		where w.provider_window_id = 'fast-coding-weekly-0' and lower(trim(w.model_scope_kind)) = 'all'
		order by a.id desc limit 1`).Scan(&activationReason); err != nil {
		t.Fatalf("read Spark reclassification activation reason: %v", err)
	}
	if err := db.QueryRow(`select c.end_reason
		from account_quota_cycles c
		join account_quota_window_activations a on a.id = c.activation_id
		join account_quota_windows w on w.id = a.window_id
		where w.provider_window_id = 'fast-coding-weekly-0' and lower(trim(w.model_scope_kind)) = 'all'
		order by c.id desc limit 1`).Scan(&cycleReason); err != nil {
		t.Fatalf("read Spark reclassification cycle reason: %v", err)
	}
	if activationReason != "scope_reclassified" || cycleReason != "scope_reclassified" {
		t.Fatalf("Spark reclassification reasons = activation:%q cycle:%q", activationReason, cycleReason)
	}
}

func TestQuotaLifecycleReclassifiesLegacyCodexIncompleteFeatureAllScope(t *testing.T) {
	tests := []struct {
		name             string
		providerWindowID string
		scopeKey         string
	}{
		{name: "code review", providerWindowID: "code-review-weekly-0", scopeKey: codexquota.CodeReviewScopeKey},
		{name: "unknown additional feature", providerWindowID: "future-feature-weekly-0", scopeKey: "future_feature"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, path := newQuotaSnapshotTestServiceWithPath(t, quotaLifecycleBaseMS+quotaLifecycleDayMS)
			legacy := quotaLifecycleFixedWindow(
				"legacy-codex-window",
				"weekly",
				quotaLifecycleBaseMS,
				7*24*60*60,
				50,
			)
			unrelated := quotaLifecycleFixedWindow(
				"unrelated-feature-weekly-0",
				"weekly",
				quotaLifecycleBaseMS,
				7*24*60*60,
				25,
			)
			writeQuotaLifecycleObservation(
				t,
				service,
				"complete",
				quotaLifecycleBaseMS+quotaLifecycleHourMS,
				[]WindowInput{legacy, unrelated},
			)
			rewriteQuotaLifecycleProviderWindowID(t, path, "legacy-codex-window", test.providerWindowID)

			scoped := quotaLifecycleFixedWindow(
				test.providerWindowID,
				"weekly",
				quotaLifecycleBaseMS,
				7*24*60*60,
				0,
			)
			scoped.ModelScopeKind = "feature"
			scoped.ModelScopeKey = test.scopeKey
			writeQuotaLifecycleObservation(
				t,
				service,
				"partial",
				quotaLifecycleBaseMS+2*quotaLifecycleHourMS,
				[]WindowInput{scoped},
			)

			result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
				RowKey: "row-lifecycle", Provider: "codex", Account: quotaSnapshotTestAccount(),
			}}})
			if err != nil {
				t.Fatalf("query active reclassified feature window: %v", err)
			}
			if len(result.Items) != 1 || len(result.Items[0].Windows) != 2 {
				t.Fatalf("active reclassified feature windows = %#v", result)
			}
			var scopedActive, unrelatedActive bool
			for _, window := range result.Items[0].Windows {
				switch window.ProviderWindowID {
				case test.providerWindowID:
					scopedActive = window.ModelScopeKind == "feature" &&
						window.ModelScopeKey == test.scopeKey && window.Availability == "active"
				case "unrelated-feature-weekly-0":
					unrelatedActive = window.ModelScopeKind == "feature" &&
						window.ModelScopeKey == "unrelated_feature" && window.Availability == "active"
				}
			}
			if !scopedActive || !unrelatedActive {
				t.Fatalf("scoped/unrelated active windows = %#v", result.Items[0].Windows)
			}

			all, err := service.Query(context.Background(), QueryRequest{
				Accounts: []QueryAccount{{
					RowKey: "row-lifecycle", Provider: "codex", Account: quotaSnapshotTestAccount(),
				}},
				IncludeInactive: true,
			})
			if err != nil {
				t.Fatalf("query inactive reclassified feature window: %v", err)
			}
			if len(all.Items) != 1 || len(all.Items[0].Windows) != 3 {
				t.Fatalf("all reclassified feature windows = %#v", all)
			}
			var legacyInactive bool
			for _, window := range all.Items[0].Windows {
				if window.ProviderWindowID == test.providerWindowID &&
					window.ModelScopeKind == "all" && window.Availability == "inactive" {
					legacyInactive = true
				}
			}
			if !legacyInactive {
				t.Fatalf("legacy feature all-scope window was not retained as inactive: %#v", all.Items[0].Windows)
			}
		})
	}
}

func TestQuerySuppressesUnmigratedLegacyCodexSparkAllScope(t *testing.T) {
	service, path := newQuotaSnapshotTestServiceWithPath(t, 40_000)
	scoped := quotaLifecycleFixedWindow(
		"spark-weekly-0",
		"weekly",
		10_000,
		7*24*60*60,
		0,
	)
	scoped.ModelScopeKind = "models"
	scoped.ModelIDs = []string{codexquota.SparkModelID}
	writeQuotaLifecycleObservation(t, service, "partial", 20_000, []WindowInput{scoped})

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open unmigrated quota database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var accountKey string
	if err := db.QueryRow(`select account_key from account_quota_snapshots limit 1`).Scan(&accountKey); err != nil {
		t.Fatalf("read quota account key: %v", err)
	}
	if _, err := db.Exec(`insert into account_quota_snapshots (
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, source, source_observation_id, observed_at_ms,
		boundary_accuracy, used_percent, remaining_percent, created_at_ms
	) values (?, 'codex', 'fast-coding-weekly-0', 'weekly', 'unknown', 'all',
		'inspection', 'legacy-unmigrated', 30_000, 'unknown', 99, 1, 30_000)`, accountKey); err != nil {
		t.Fatalf("insert unmigrated Spark snapshot: %v", err)
	}

	result, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-unmigrated", Provider: "codex", Account: quotaSnapshotTestAccount(),
	}}})
	if err != nil {
		t.Fatalf("query unmigrated Spark snapshots: %v", err)
	}
	if len(result.Items) != 1 || len(result.Items[0].Windows) != 1 {
		t.Fatalf("unmigrated Spark snapshots were duplicated: %#v", result)
	}
	window := result.Items[0].Windows[0]
	if window.ProviderWindowID != "spark-weekly-0" || window.ModelScopeKind != "models" ||
		len(window.ModelIDs) != 1 || window.ModelIDs[0] != codexquota.SparkModelID ||
		window.UsedPercent == nil || *window.UsedPercent != 0 {
		t.Fatalf("unmigrated Spark snapshot replaced scoped evidence: %#v", window)
	}
}

func TestQuotaLifecycleMigratesLegacyCodexMainAllScopeInPlace(t *testing.T) {
	service, path := newQuotaSnapshotTestServiceWithPath(t, quotaLifecycleBaseMS+quotaLifecycleDayMS)
	legacy := quotaLifecycleFixedWindow(
		"weekly",
		"weekly",
		quotaLifecycleBaseMS,
		7*24*60*60,
		36,
	)
	writeQuotaLifecycleObservation(
		t,
		service,
		"complete",
		quotaLifecycleBaseMS+quotaLifecycleHourMS,
		[]WindowInput{legacy},
	)
	legacyWindow := queryQuotaLifecycleWindows(t, service, false)["weekly"]
	if legacyWindow.LogicalWindowID == 0 {
		t.Fatalf("legacy main logical window = %#v", legacyWindow)
	}

	scoped := quotaLifecycleFixedWindow(
		"weekly",
		"weekly",
		quotaLifecycleBaseMS,
		7*24*60*60,
		36,
	)
	scoped.ModelScopeKind = "family"
	scoped.ModelScopeKey = codexquota.MainScopeKey
	writeQuotaLifecycleObservation(
		t,
		service,
		"complete",
		quotaLifecycleBaseMS+2*quotaLifecycleHourMS,
		[]WindowInput{scoped},
	)

	windows := queryQuotaLifecycleWindows(t, service, false)
	window, ok := windows["weekly"]
	if !ok || window.ModelScopeKind != "family" || window.ModelScopeKey != codexquota.MainScopeKey ||
		window.Availability != "active" || window.LogicalWindowID == 0 || window.CurrentCycle == nil {
		t.Fatalf("main window was not migrated in place: %#v", windows)
	}
	if window.LogicalWindowID != legacyWindow.LogicalWindowID {
		t.Fatalf("main logical window changed from %d to %d", legacyWindow.LogicalWindowID, window.LogicalWindowID)
	}
	if window.CurrentCycle.ActualStartMS != quotaLifecycleBaseMS {
		t.Fatalf("main cycle start changed during migration: %#v", window.CurrentCycle)
	}

	all := queryQuotaLifecycleWindows(t, service, true)
	if len(all) != 1 {
		t.Fatalf("main migration created duplicate logical windows: %#v", all)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open main migration database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var storedKind, storedKey string
	if err := db.QueryRow(`select model_scope_kind, coalesce(model_scope_key, '')
		from account_quota_windows where id = ?`, window.LogicalWindowID).Scan(&storedKind, &storedKey); err != nil {
		t.Fatalf("read stored main lifecycle scope: %v", err)
	}
	if storedKind != "all" || storedKey != "" {
		t.Fatalf("stored main lifecycle scope = %q/%q, want legacy all identity", storedKind, storedKey)
	}
}

func TestQuotaLifecycleReconcilesLegacyCodexPrimaryAndSecondaryAliases(t *testing.T) {
	tests := []struct {
		name       string
		legacyID   string
		currentID  string
		windowKind string
		duration   int64
	}{
		{name: "primary to five hour", legacyID: "primary", currentID: "five-hour", windowKind: "five_hour", duration: 5 * 60 * 60},
		{name: "secondary to weekly", legacyID: "secondary", currentID: "weekly", windowKind: "weekly", duration: 7 * 24 * 60 * 60},
		{name: "secondary to team monthly", legacyID: "secondary", currentID: "monthly", windowKind: "monthly", duration: 30 * 24 * 60 * 60},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newQuotaSnapshotTestService(t, quotaLifecycleBaseMS+quotaLifecycleDayMS)
			legacy := quotaLifecycleFixedWindow(
				test.legacyID,
				test.windowKind,
				quotaLifecycleBaseMS,
				test.duration,
				40,
			)
			writeQuotaLifecycleObservation(
				t,
				service,
				"complete",
				quotaLifecycleBaseMS+quotaLifecycleHourMS,
				[]WindowInput{legacy},
			)

			scoped := quotaLifecycleFixedWindow(
				test.currentID,
				test.windowKind,
				quotaLifecycleBaseMS,
				test.duration,
				35,
			)
			scoped.ModelScopeKind = "family"
			scoped.ModelScopeKey = codexquota.MainScopeKey
			writeQuotaLifecycleObservation(
				t,
				service,
				"complete",
				quotaLifecycleBaseMS+2*quotaLifecycleHourMS,
				[]WindowInput{scoped},
			)

			active := queryQuotaLifecycleWindows(t, service, false)
			if len(active) != 1 {
				t.Fatalf("legacy %s produced duplicate active windows: %#v", test.legacyID, active)
			}
			for _, window := range active {
				if window.ModelScopeKind != "family" || window.ModelScopeKey != codexquota.MainScopeKey {
					t.Fatalf("legacy %s active scope = %#v", test.legacyID, window)
				}
			}

			all := queryQuotaLifecycleWindows(t, service, true)
			if len(all) != 1 {
				t.Fatalf("legacy %s produced duplicate lifecycle windows: %#v", test.legacyID, all)
			}
		})
	}
}

func quotaLifecycleFixedWindow(id, kind string, startMS, durationSeconds int64, usedPercent float64) WindowInput {
	endMS := startMS + durationSeconds*1000
	return WindowInput{
		ProviderWindowID: id,
		WindowKind:       kind,
		WindowMode:       "fixed",
		ModelScopeKind:   "all",
		Source:           "inspection",
		BoundaryAccuracy: "exact",
		CycleStartMS:     &startMS,
		CycleEndMS:       &endMS,
		DurationSeconds:  &durationSeconds,
		UsedPercent:      &usedPercent,
	}
}

func writeQuotaLifecycleObservation(t *testing.T, service *Service, inventoryMode string, observedAtMS int64, windows []WindowInput) {
	t.Helper()
	_, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{
		quotaLifecycleWriteEntry(inventoryMode, observedAtMS, windows),
	}})
	if err != nil {
		t.Fatalf("write %s quota lifecycle observation at %d: %v", inventoryMode, observedAtMS, err)
	}
}

func rewriteQuotaLifecycleProviderWindowID(t *testing.T, path, from, to string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy quota database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	legacyScopeFingerprint := quotasnapshotrepo.ScopeFingerprint("all", "", nil)
	if _, err := db.Exec(`update account_quota_windows set
		provider_window_id = ?, model_scope_kind = 'all', model_scope_key = '',
		model_ids_json = '', scope_fingerprint = ? where provider_window_id = ?`, to, legacyScopeFingerprint, from); err != nil {
		t.Fatalf("rewrite legacy quota window state: %v", err)
	}
	if _, err := db.Exec(`update account_quota_snapshots set
		provider_window_id = ?, model_scope_kind = 'all', model_scope_key = '',
		model_ids_json = '', scope_fingerprint = ? where provider_window_id = ?`, to, legacyScopeFingerprint, from); err != nil {
		t.Fatalf("rewrite legacy quota snapshot: %v", err)
	}
}

func quotaLifecycleWriteEntry(inventoryMode string, observedAtMS int64, windows []WindowInput) WriteEntry {
	windows = append([]WindowInput(nil), windows...)
	source := "inspection"
	if len(windows) > 0 && strings.TrimSpace(windows[0].Source) != "" {
		source = windows[0].Source
	}
	sourceObservationID := fmt.Sprintf("observation-%d", observedAtMS)
	for index := range windows {
		windows[index].ObservedAtMS = observedAtMS
		windows[index].SourceObservationID = sourceObservationID
	}
	return WriteEntry{
		RowKey: "row-lifecycle", Provider: "codex", Account: quotaSnapshotTestAccount(),
		Observation: &ObservationInput{
			Source: source, SourceObservationID: sourceObservationID,
			ObservedAtMS: observedAtMS, InventoryScopeKey: "codex:quota-windows", InventoryMode: inventoryMode,
		},
		Windows: windows,
	}
}

func quotaLifecycleWriteEntryWithObservation(
	inventoryMode string,
	source string,
	sourceObservationID string,
	inventoryScopeKey string,
	observedAtMS int64,
	windows []WindowInput,
) WriteEntry {
	windows = append([]WindowInput(nil), windows...)
	for index := range windows {
		windows[index].Source = source
		windows[index].SourceObservationID = sourceObservationID
		windows[index].ObservedAtMS = observedAtMS
	}
	return WriteEntry{
		RowKey: "row-lifecycle", Provider: "codex", Account: quotaSnapshotTestAccount(),
		Observation: &ObservationInput{
			Source: source, SourceObservationID: sourceObservationID,
			ObservedAtMS: observedAtMS, InventoryScopeKey: inventoryScopeKey, InventoryMode: inventoryMode,
		},
		Windows: windows,
	}
}

func queryQuotaLifecycleWindows(t *testing.T, service *Service, includeInactive bool) map[string]Window {
	t.Helper()
	result, err := service.Query(context.Background(), QueryRequest{
		Accounts:        []QueryAccount{{RowKey: "row-lifecycle", Provider: "codex", Account: quotaSnapshotTestAccount()}},
		IncludeInactive: includeInactive,
	})
	if err != nil {
		t.Fatalf("query quota lifecycle: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("quota lifecycle items = %#v", result.Items)
	}
	windows := make(map[string]Window, len(result.Items[0].Windows))
	for _, window := range result.Items[0].Windows {
		windows[window.ProviderWindowID] = window
	}
	return windows
}
