package quotasnapshot

import (
	"context"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

func pluginQuotaTestResult(provider string) model.PluginQuotaResult {
	remaining := 1739.5
	checkin := 0.0
	return model.PluginQuotaResult{
		PluginID:        provider,
		Provider:        provider,
		AuthFileName:    provider + "-account.json",
		AuthIndex:       "auth-index-1",
		AccountSnapshot: "account-1",
		ObservedAtMS:    15_000,
		Items: []model.PluginQuotaItem{
			{Key: "credit_remaining", Label: "剩余额度", Value: &remaining, Unit: "credit", Format: "number"},
			{Key: "daily_checkin", Label: "今日可签到", Value: &checkin, Format: "boolean"},
		},
	}
}

func pluginQuotaTestAccount(provider string) AccountTarget {
	return AccountTarget{
		AuthFileSnapshot:     provider + "-account.json",
		AuthProviderSnapshot: provider,
		AuthIndex:            "auth-index-1",
		AccountSnapshot:      "account-1",
	}
}

func TestWritePluginQuotaResultRequiresRegisteredProvider(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 20_000)
	result := pluginQuotaTestResult("example-plugin")

	if err := service.WritePluginQuotaResult(context.Background(), result); err != nil {
		t.Fatalf("write unregistered plugin quota: %v", err)
	}
	// Ignoring an unregistered plugin provider must not weaken the built-in
	// allowlist: the same provider is still rejected by the generic entry point.
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
		Provider: "example-plugin",
		Account:  pluginQuotaTestAccount("example-plugin"),
		Windows: []WindowInput{{
			ProviderWindowID: "credit_remaining", WindowKind: pluginQuotaWindowKind,
			WindowMode: "non_window", ModelScopeKind: "all", Source: "api_query",
			ObservedAtMS: 15_000, BoundaryAccuracy: "unknown",
		}},
	}}}); err == nil {
		t.Fatal("unregistered plugin provider was accepted by Write")
	}

	service.SetPluginQuotaProviders([]string{"example-plugin"})
	if err := service.WritePluginQuotaResult(context.Background(), result); err != nil {
		t.Fatalf("write registered plugin quota: %v", err)
	}
	query, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-1", Provider: "example-plugin", Account: pluginQuotaTestAccount("example-plugin"),
	}}})
	if err != nil {
		t.Fatalf("query registered provider: %v", err)
	}
	if len(query.Items) != 1 || len(query.Items[0].Windows) != 2 {
		t.Fatalf("registered plugin provider windows = %#v", query.Items)
	}
}

func TestWritePluginQuotaResultMapsItemsToNonWindowReadings(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 20_000)
	service.SetPluginQuotaProviders([]string{"example-plugin"})

	if err := service.WritePluginQuotaResult(context.Background(), pluginQuotaTestResult("example-plugin")); err != nil {
		t.Fatalf("write plugin quota: %v", err)
	}
	query, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-1", Provider: "example-plugin", Account: pluginQuotaTestAccount("example-plugin"),
	}}})
	if err != nil {
		t.Fatalf("query plugin quota: %v", err)
	}
	if len(query.Items) != 1 {
		t.Fatalf("query items = %#v", query.Items)
	}
	windows := map[string]Window{}
	for _, window := range query.Items[0].Windows {
		windows[window.ProviderWindowID] = window
	}
	remaining, ok := windows["credit_remaining"]
	if !ok {
		t.Fatalf("credit_remaining window missing: %#v", windows)
	}
	if remaining.WindowKind != pluginQuotaWindowKind || remaining.WindowMode != "non_window" {
		t.Fatalf("credit_remaining window shape = %#v", remaining)
	}
	if remaining.QuotaUnit != "credit" {
		t.Fatalf("credit_remaining unit = %q, want credit", remaining.QuotaUnit)
	}
	if remaining.LimitValue == nil || *remaining.LimitValue != 1739.5 {
		t.Fatalf("credit_remaining value = %#v", remaining.LimitValue)
	}
	if remaining.UsedPercent != nil || remaining.RemainingPercent != nil {
		t.Fatalf("plugin item invented a progress percentage: %#v", remaining)
	}
	if remaining.Source != "api_query" || remaining.BoundaryAccuracy != "unknown" {
		t.Fatalf("plugin item evidence = %#v", remaining)
	}
	checkin, ok := windows["daily_checkin"]
	if !ok {
		t.Fatalf("daily_checkin window missing: %#v", windows)
	}
	if checkin.LimitValue == nil || *checkin.LimitValue != 0 {
		t.Fatalf("daily_checkin value = %#v", checkin.LimitValue)
	}
}

func TestWritePluginQuotaResultIgnoresEmptySummary(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 20_000)
	service.SetPluginQuotaProviders([]string{"example-plugin"})

	empty := pluginQuotaTestResult("example-plugin")
	empty.Items = nil
	if err := service.WritePluginQuotaResult(context.Background(), empty); err != nil {
		t.Fatalf("write empty plugin quota: %v", err)
	}
	query, err := service.Query(context.Background(), QueryRequest{Accounts: []QueryAccount{{
		RowKey: "row-1", Provider: "example-plugin", Account: pluginQuotaTestAccount("example-plugin"),
	}}})
	if err != nil {
		t.Fatalf("query empty plugin quota: %v", err)
	}
	if len(query.Items) != 1 || len(query.Items[0].Windows) != 0 {
		t.Fatalf("empty summary persisted windows: %#v", query.Items)
	}
}

func TestWritePluginQuotaResultRequiresCredentialIdentity(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 20_000)
	service.SetPluginQuotaProviders([]string{"example-plugin"})

	result := pluginQuotaTestResult("example-plugin")
	result.AuthFileName = ""
	result.AuthIndex = ""
	if err := service.WritePluginQuotaResult(context.Background(), result); err != nil {
		t.Fatalf("write identity-less plugin quota: %v", err)
	}
}

func TestSetPluginQuotaProvidersReplacesCatalogue(t *testing.T) {
	service := newQuotaSnapshotTestService(t, 20_000)
	service.SetPluginQuotaProviders([]string{"first-plugin", "second-plugin"})
	if got := service.PluginQuotaProviders(); len(got) != 2 {
		t.Fatalf("provider catalogue = %#v", got)
	}
	service.SetPluginQuotaProviders([]string{"second-plugin"})
	if _, err := service.Write(context.Background(), WriteRequest{Entries: []WriteEntry{{
		Provider: "first-plugin",
		Account:  pluginQuotaTestAccount("first-plugin"),
		Windows: []WindowInput{{
			ProviderWindowID: "item", WindowKind: pluginQuotaWindowKind, WindowMode: "non_window",
			ModelScopeKind: "all", Source: "api_query", ObservedAtMS: 15_000, BoundaryAccuracy: "unknown",
		}},
	}}}); err == nil {
		t.Fatal("removed plugin provider was still accepted")
	}
}
