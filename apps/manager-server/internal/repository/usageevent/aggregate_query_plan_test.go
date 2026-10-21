package usageevent

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestTopModelsQueryUsesTimestampIndexBeforePricingMaterialization(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqliterepo.RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		t.Fatalf("prepare post-listen indexes: %v", err)
	}
	// Preserve the original timestamp-only planner regression, then verify
	// coexistence without pinning SQLite to one timestamp-leading index.
	if _, err := db.Exec(`drop index ` + latencyCoverIndex); err != nil {
		t.Fatal(err)
	}
	t.Run("timestamp_only", func(t *testing.T) {
		assertTopModelsTimestampPlan(t, db, "idx_usage_events_timestamp")
	})
	if _, err := db.Exec(latencyCoverDDL); err != nil {
		t.Fatal(err)
	}
	t.Run("timestamp_and_latency_cover", func(t *testing.T) {
		assertTopModelsTimestampPlan(t, db, "")
	})
}

func assertTopModelsTimestampPlan(t *testing.T, db *sql.DB, requiredIndex string) {
	t.Helper()
	rows, err := db.Query(`explain query plan `+topModelsSQL, int64(1_000), int64(2_000), 5)
	if err != nil {
		t.Fatalf("explain top models query: %v", err)
	}
	defer rows.Close()

	details := make([]string, 0, 8)
	usesTimestampRange := false
	fullUsageScan := false
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
		if requiredIndex != "" {
			usesTimestampRange = usesTimestampRange || strings.Contains(detail, "SEARCH usage_events USING INDEX "+requiredIndex)
		} else {
			usesTimestampRange = usesTimestampRange || (strings.Contains(detail, "SEARCH usage_events USING ") && strings.Contains(detail, "(timestamp_ms>? AND timestamp_ms<?)"))
		}
		fullUsageScan = fullUsageScan || strings.Contains(detail, "SCAN usage_events")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query plan rows: %v", err)
	}
	if !usesTimestampRange || fullUsageScan {
		t.Fatalf("top models query did not constrain usage_events with a timestamp range index: %v", details)
	}
}
