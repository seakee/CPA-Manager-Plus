package quotasnapshot

import (
	"context"
	"testing"
	"time"
)

// CommandCode quota windows come from a CLIProxyAPI provider plugin through the
// native quota API. Snapshots must persist per credential like built-in providers.
func TestCommandCodeQuotaPersistenceAndCredentialIsolation(t *testing.T) {
	now := time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC).UnixMilli()
	service := newQuotaSnapshotTestService(t, now)
	ctx := context.Background()
	start := time.Date(2026, 10, 2, 6, 34, 24, 0, time.UTC).UnixMilli()
	end := time.Date(2026, 11, 2, 6, 34, 24, 0, time.UTC).UnixMilli()
	duration := (end - start) / 1000
	weeklyDuration := int64(604800)
	weeklyStart := now - 86400000
	weeklyEnd := weeklyStart + weeklyDuration*1000
	used := 40.0
	account := AccountTarget{AuthFileSnapshot: "commandcode-fixture.json", AuthProviderSnapshot: "commandcode", AuthIndex: "fixture-index", Source: "commandcode-fixture.json"}
	entry := WriteEntry{
		RowKey: "fixture", Provider: "commandcode", Account: account,
		Observation: &ObservationInput{Source: "api_query", SourceObservationID: "fixture-query", ObservedAtMS: now, InventoryScopeKey: "commandcode:quota-windows", InventoryMode: "partial"},
		Windows: []WindowInput{{
			ProviderWindowID: "commandcode-monthly", WindowKind: "monthly", WindowMode: "calendar", ModelScopeKind: "all",
			Source: "api_query", SourceObservationID: "fixture-query", ObservedAtMS: now, BoundaryAccuracy: "exact",
			CycleStartMS: &start, CycleEndMS: &end, DurationSeconds: &duration,
		}, {
			ProviderWindowID: "commandcode-weekly", WindowKind: "weekly", WindowMode: "fixed", ModelScopeKind: "all",
			Source: "api_query", SourceObservationID: "fixture-query", ObservedAtMS: now, BoundaryAccuracy: "exact", UsedPercent: &used,
			CycleStartMS: &weeklyStart, CycleEndMS: &weeklyEnd, DurationSeconds: &weeklyDuration,
		}},
	}
	for i := 0; i < 2; i++ {
		if _, err := service.Write(ctx, WriteRequest{Entries: []WriteEntry{entry}}); err != nil {
			t.Fatalf("write CommandCode snapshot: %v", err)
		}
	}
	query := func(target AccountTarget) QueryResponse {
		t.Helper()
		result, err := service.Query(ctx, QueryRequest{Accounts: []QueryAccount{{RowKey: "fixture", Provider: "commandcode", Account: target}}})
		if err != nil {
			t.Fatalf("query CommandCode snapshots: %v", err)
		}
		return result
	}
	result := query(account)
	if len(result.Items) != 1 || len(result.Items[0].Windows) != 2 {
		t.Fatalf("persisted windows = %#v", result)
	}
	for _, window := range result.Items[0].Windows {
		switch window.ProviderWindowID {
		case "commandcode-monthly":
			if window.UsedPercent != nil || window.RemainingPercent != nil {
				t.Fatal("a subscription boundary must not invent monthly quota percentages")
			}
			if window.CurrentCycle == nil || window.CurrentCycle.ScheduledStartMS == nil || *window.CurrentCycle.ScheduledStartMS != start || window.CurrentCycle.ScheduledEndMS == nil || *window.CurrentCycle.ScheduledEndMS != end {
				t.Fatalf("subscription cycle = %#v", window.CurrentCycle)
			}
		case "commandcode-weekly":
			if window.UsedPercent == nil || *window.UsedPercent != used {
				t.Fatalf("weekly quota = %#v", window)
			}
		default:
			t.Fatalf("unexpected window %q", window.ProviderWindowID)
		}
	}
	other := account
	other.AuthIndex = "other-index"
	other.AuthFileSnapshot = "other-commandcode.json"
	other.Source = "other-commandcode.json"
	if isolated := query(other); len(isolated.Items) != 1 || len(isolated.Items[0].Windows) != 0 {
		t.Fatalf("another credential borrowed persisted windows: %#v", isolated)
	}
	entry.Provider = "unregistered-provider"
	if _, err := service.Write(ctx, WriteRequest{Entries: []WriteEntry{entry}}); err == nil {
		t.Fatal("unregistered providers should still be rejected")
	}
}
