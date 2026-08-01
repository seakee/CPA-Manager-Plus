package usageevent

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestCredentialTimelineHourlyPreaggregationMatchesRaw(t *testing.T) {
	repo := newAnalyticsPreaggregationRepo(t)
	ctx := context.Background()
	base := time.Date(2026, time.March, 8, 5, 0, 0, 0, time.UTC)
	insertAnalyticsPreaggregationEvents(t, ctx, repo, base)
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	filter := AnalyticsFilter{
		FromMS:        base.Add(15 * time.Minute).UnixMilli(),
		ToMS:          base.Add(4*time.Hour + 45*time.Minute).UnixMilli(),
		IncludeFailed: true,
	}

	for _, granularity := range []string{"hour", "day"} {
		raw, err := repo.credentialTimelineRawWithFilter(ctx, filter, granularity, location)
		if err != nil {
			t.Fatalf("raw %s: %v", granularity, err)
		}
		got, err := repo.CredentialTimelineWithFilter(ctx, filter, granularity, location)
		if err != nil {
			t.Fatalf("preaggregate %s: %v", granularity, err)
		}
		sortCredentialTimelinePoints(raw)
		sortCredentialTimelinePoints(got)
		if !reflect.DeepEqual(got, raw) {
			t.Fatalf("%s mismatch\npreaggregate=%#v\nraw=%#v", granularity, got, raw)
		}
	}
}

func TestCredentialTimelinePreaggregationFallsBackForFractionalOffset(t *testing.T) {
	repo := newAnalyticsPreaggregationRepo(t)
	ctx := context.Background()
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	insertAnalyticsPreaggregationEvents(t, ctx, repo, base)
	filter := AnalyticsFilter{FromMS: base.UnixMilli(), ToMS: base.Add(5 * time.Hour).UnixMilli(), IncludeFailed: true}

	for _, name := range []string{"Asia/Kolkata", "Australia/Eucla"} {
		location, err := time.LoadLocation(name)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		rawCredentials, err := repo.credentialTimelineRawWithFilter(ctx, filter, "hour", location)
		if err != nil {
			t.Fatalf("raw credentials %s: %v", name, err)
		}
		gotCredentials, err := repo.CredentialTimelineWithFilter(ctx, filter, "hour", location)
		if err != nil {
			t.Fatalf("credentials %s: %v", name, err)
		}
		if !reflect.DeepEqual(gotCredentials, rawCredentials) {
			t.Fatalf("credentials %s did not preserve raw fallback", name)
		}
	}
}

func TestCredentialIDFilterMatchesAllIdentityFallbacks(t *testing.T) {
	repo := newAnalyticsPreaggregationRepo(t)
	ctx := context.Background()
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	identities := []struct {
		name       string
		id         string
		authFile   string
		authIndex  string
		sourceHash string
		source     string
	}{
		{name: "auth file", id: "credential.json", authFile: "credential.json", authIndex: "auth-file", sourceHash: "hash-file", source: "source-file"},
		{name: "auth index", id: "auth-index", authIndex: "auth-index", sourceHash: "hash-index", source: "source-index"},
		{name: "source hash", id: "hash-only", sourceHash: "hash-only", source: "source-hash"},
		{name: "source", id: "source-only", source: "source-only"},
	}
	events := make([]usage.Event, 0, len(identities))
	for index, identity := range identities {
		timestamp := base.Add(time.Duration(index) * time.Hour)
		events = append(events, usage.Event{
			EventHash:        "credential-filter-" + identity.name,
			TimestampMS:      timestamp.UnixMilli(),
			Timestamp:        timestamp.Format(time.RFC3339Nano),
			Model:            "gpt-test",
			AuthFileSnapshot: identity.authFile,
			AuthIndex:        identity.authIndex,
			SourceHash:       identity.sourceHash,
			Source:           identity.source,
			InputTokens:      10,
			OutputTokens:     5,
			TotalTokens:      15,
			CreatedAtMS:      timestamp.UnixMilli(),
		})
	}
	if _, err := repo.InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	for _, identity := range identities {
		t.Run(identity.name, func(t *testing.T) {
			filter := AnalyticsFilter{
				FromMS:        base.UnixMilli(),
				ToMS:          base.Add(5 * time.Hour).UnixMilli(),
				CredentialIDs: []string{identity.id},
				IncludeFailed: true,
			}
			stats, err := repo.CredentialModelStatsWithFilter(ctx, filter)
			if err != nil {
				t.Fatalf("credential stats: %v", err)
			}
			if len(stats) != 1 || stats[0].ID != identity.id {
				t.Fatalf("credential stats = %#v", stats)
			}
			points, err := repo.CredentialTimelineWithFilter(ctx, filter, "hour", time.UTC)
			if err != nil {
				t.Fatalf("credential timeline: %v", err)
			}
			if len(points) != 1 || points[0].ID != identity.id {
				t.Fatalf("credential timeline = %#v", points)
			}
		})
	}
}

func newAnalyticsPreaggregationRepo(t *testing.T) *repository {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &repository{db: db}
}

func insertAnalyticsPreaggregationEvents(t *testing.T, ctx context.Context, repo *repository, base time.Time) {
	t.Helper()
	latencies := []int64{100, 0, -200, 500, 700, 900}
	events := make([]usage.Event, 0, len(latencies))
	for index, latency := range latencies {
		timestamp := base.Add(time.Duration(index)*time.Hour + 20*time.Minute)
		event := usage.Event{
			EventHash:             "analytics-preaggregate-" + timestamp.Format("20060102T150405Z"),
			TimestampMS:           timestamp.UnixMilli(),
			Timestamp:             timestamp.Format(time.RFC3339Nano),
			Provider:              "fallback-provider",
			Model:                 "gpt-test",
			ResolvedModel:         "gpt-test-billing",
			ServiceTier:           "priority",
			AuthIndex:             "auth-1",
			Source:                "source-a",
			SourceHash:            "source-hash-a",
			APIKeyHash:            "api-key-a",
			AuthFileSnapshot:      "credential-a.json",
			AccountSnapshot:       "account-a",
			AuthLabelSnapshot:     "label-a",
			AuthProviderSnapshot:  "openai",
			AuthProjectIDSnapshot: "project-a",
			InputTokens:           int64(100_000 + index*100_000),
			OutputTokens:          int64(10 + index),
			ReasoningTokens:       int64(index),
			CachedTokens:          int64(index * 3),
			CacheReadTokens:       int64(index),
			CacheCreationTokens:   int64(index),
			TotalTokens:           int64(100_010 + index*100_001),
			Failed:                index%3 == 1,
			CreatedAtMS:           timestamp.UnixMilli(),
		}
		if latency != 0 {
			event.LatencyMS = &latencies[index]
		}
		if index == 3 {
			event.AccountSnapshot = ""
			event.AuthLabelSnapshot = ""
		}
		events = append(events, event)
	}
	if _, err := repo.InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}
}

func sortCredentialTimelinePoints(points []CredentialTimelinePoint) {
	sort.Slice(points, func(i, j int) bool {
		if points[i].BucketMS != points[j].BucketMS {
			return points[i].BucketMS < points[j].BucketMS
		}
		if points[i].ID != points[j].ID {
			return points[i].ID < points[j].ID
		}
		return points[i].Model < points[j].Model
	})
}
