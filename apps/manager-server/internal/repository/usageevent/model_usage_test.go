package usageevent

import (
	"context"
	"path/filepath"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestModelUsageSummaryAggregatesRecentRequestedAndResolvedModels(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	repo := New(db)
	events := []usage.Event{
		modelUsageTestEvent("old", 100, "old-model", "old-resolved"),
		modelUsageTestEvent("a-resolved", 200, "gpt-a", "gpt-b"),
		modelUsageTestEvent("a-same", 300, "gpt-a", "gpt-a"),
		modelUsageTestEvent("c-requested", 400, "gpt-c", ""),
	}
	if _, err := repo.InsertBatch(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	summary, err := repo.ModelUsageSummary(context.Background(), 3)
	if err != nil {
		t.Fatalf("model usage summary: %v", err)
	}
	if summary.SampledEvents != 3 || summary.TotalEvents != 4 || !summary.Truncated {
		t.Fatalf("summary metadata = %#v", summary)
	}
	want := []struct {
		model                      string
		calls, requested, resolved int64
	}{
		{model: "gpt-a", calls: 2, requested: 2},
		{model: "gpt-b", calls: 1, resolved: 1},
		{model: "gpt-c", calls: 1, requested: 1},
	}
	if len(summary.Models) != len(want) {
		t.Fatalf("models = %#v", summary.Models)
	}
	for index, expected := range want {
		actual := summary.Models[index]
		if actual.Model != expected.model || actual.Calls != expected.calls || actual.RequestedCalls != expected.requested || actual.ResolvedCalls != expected.resolved {
			t.Fatalf("models[%d] = %#v, want %#v", index, actual, expected)
		}
	}
}

func TestModelUsageSummaryReturnsEmptyModels(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	summary, err := New(db).ModelUsageSummary(context.Background(), 10)
	if err != nil {
		t.Fatalf("model usage summary: %v", err)
	}
	if summary.SampledEvents != 0 || summary.TotalEvents != 0 || summary.Truncated {
		t.Fatalf("summary metadata = %#v", summary)
	}
	if summary.Models == nil || len(summary.Models) != 0 {
		t.Fatalf("models = %#v", summary.Models)
	}
}

func modelUsageTestEvent(hash string, timestampMS int64, requestedModel, resolvedModel string) usage.Event {
	return usage.Event{
		EventHash:     hash,
		TimestampMS:   timestampMS,
		Timestamp:     "2026-01-01T00:00:00Z",
		Model:         requestedModel,
		ResolvedModel: resolvedModel,
		CreatedAtMS:   timestampMS,
	}
}
