package usageevent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestRecentFailuresHideHistoricalCodexProjectMarker(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	ctx := context.Background()
	timestamp := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	if _, err := repo.InsertBatch(ctx, []usage.Event{{
		EventHash:             canonicalTestHash("recent-failure-legacy-codex-marker"),
		TimestampMS:           timestamp.UnixMilli(),
		Timestamp:             timestamp.Format(time.RFC3339Nano),
		Provider:              "codex",
		Model:                 "gpt-5",
		Failed:                true,
		AuthFileSnapshot:      "codex.json",
		AuthIndex:             "codex-auth",
		AuthProviderSnapshot:  "codex",
		AuthProjectIDSnapshot: "codex-account-id:v1:historical-account",
		CreatedAtMS:           timestamp.UnixMilli(),
	}}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	failures, err := repo.RecentFailuresBetween(ctx, timestamp.Add(-time.Hour).UnixMilli(), timestamp.Add(time.Hour).UnixMilli(), 5)
	if err != nil {
		t.Fatalf("recent failures: %v", err)
	}
	if len(failures) != 1 || failures[0].AuthProjectIDSnapshot != "" {
		t.Fatalf("recent failure project snapshot = %#v, want empty", failures)
	}
}
