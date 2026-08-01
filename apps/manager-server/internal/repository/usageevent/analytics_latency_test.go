package usageevent

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestLatencyPercentilesUseNearestRankAcrossSummaryAndBuckets(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	repo := New(db)
	base := time.Date(2026, time.January, 1, 10, 0, 0, 0, time.UTC)
	events := make([]usage.Event, 0, 40)
	for hour := 0; hour < 2; hour++ {
		for sample := int64(1); sample <= 20; sample++ {
			latency := sample + int64(hour*100)
			ttft := latency * 2
			timestamp := base.Add(time.Duration(hour) * time.Hour).Add(time.Duration(sample) * time.Second)
			events = append(events, usage.Event{
				EventHash:   fmt.Sprintf("%d-%d", hour, sample),
				TimestampMS: timestamp.UnixMilli(),
				Timestamp:   timestamp.Format(time.RFC3339Nano),
				Model:       "gpt-test",
				LatencyMS:   &latency,
				TTFTMS:      &ttft,
				CreatedAtMS: timestamp.UnixMilli(),
			})
		}
	}
	if _, err := repo.InsertBatch(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	filter := AnalyticsFilter{
		FromMS: base.UnixMilli(),
		ToMS:   base.Add(2 * time.Hour).UnixMilli(),
	}
	summary, err := repo.LatencySummaryWithFilter(context.Background(), filter)
	if err != nil {
		t.Fatalf("latency summary: %v", err)
	}
	if !summary.P95LatencyMS.Valid || summary.P95LatencyMS.Float64 != 118 {
		t.Fatalf("summary latency p95 = %#v, want 118", summary.P95LatencyMS)
	}
	if !summary.P95TTFTMS.Valid || summary.P95TTFTMS.Float64 != 236 {
		t.Fatalf("summary ttft p95 = %#v, want 236", summary.P95TTFTMS)
	}

	points, err := repo.LatencyPercentilesWithFilter(context.Background(), filter, "hour", time.UTC)
	if err != nil {
		t.Fatalf("latency percentiles: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("points = %#v", points)
	}
	if points[0].P95LatencyMS.Float64 != 19 || points[0].P95TTFTMS.Float64 != 38 {
		t.Fatalf("first bucket = %#v", points[0])
	}
	if points[1].P95LatencyMS.Float64 != 119 || points[1].P95TTFTMS.Float64 != 238 {
		t.Fatalf("second bucket = %#v", points[1])
	}
}

func TestLatencySummaryReturnsInvalidPercentilesWithoutSamples(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	summary, err := New(db).LatencySummaryWithFilter(context.Background(), AnalyticsFilter{})
	if err != nil {
		t.Fatalf("latency summary: %v", err)
	}
	if summary.P95LatencyMS.Valid || summary.P95TTFTMS.Valid {
		t.Fatalf("summary = %#v", summary)
	}
	points, err := New(db).LatencyPercentilesWithFilter(context.Background(), AnalyticsFilter{}, "day", time.UTC)
	if err != nil {
		t.Fatalf("latency percentiles: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("points = %#v", points)
	}
}

func TestLatencyPercentilesMatchRawAcrossTimeZonesAndDST(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	newSample := func(hash string, timestamp time.Time, latencyMS, ttftMS *int64) usage.Event {
		return usage.Event{
			EventHash:   hash,
			TimestampMS: timestamp.UnixMilli(),
			Timestamp:   timestamp.Format(time.RFC3339Nano),
			Model:       "gpt-test",
			LatencyMS:   latencyMS,
			TTFTMS:      ttftMS,
			CreatedAtMS: timestamp.UnixMilli(),
		}
	}
	value := func(number int64) *int64 { return &number }
	events := []usage.Event{
		newSample("spring-before", time.Date(2026, time.March, 8, 6, 55, 0, 0, time.UTC), value(10), value(4)),
		newSample("spring-after", time.Date(2026, time.March, 8, 7, 5, 0, 0, time.UTC), value(20), value(8)),
		newSample("fall-first", time.Date(2026, time.November, 1, 5, 30, 0, 0, time.UTC), value(30), nil),
		newSample("fall-second", time.Date(2026, time.November, 1, 6, 30, 0, 0, time.UTC), nil, value(12)),
		newSample("zero-null", time.Date(2026, time.November, 1, 7, 30, 0, 0, time.UTC), value(0), nil),
	}
	if _, err := New(db).InsertBatch(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	fromMS := events[0].TimestampMS
	toMS := events[len(events)-1].TimestampMS + 1
	for _, zone := range []string{"America/New_York", "Asia/Kathmandu"} {
		location, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatalf("load location %s: %v", zone, err)
		}
		for _, granularity := range []string{"hour", "day"} {
			t.Run(zone+"/"+granularity, func(t *testing.T) {
				filter := AnalyticsFilter{FromMS: fromMS, ToMS: toMS, IncludeFailed: true}
				got, err := New(db).LatencyPercentilesWithFilter(context.Background(), filter, granularity, location)
				if err != nil {
					t.Fatalf("latency percentiles: %v", err)
				}
				want := rawLatencyPercentiles(events, filter, granularity, location)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("percentiles mismatch\ngot=%#v\nwant=%#v", got, want)
				}
			})
		}
	}
}

func TestLatencyPercentilesApplyFiltersAndIgnoreMissingValues(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	latency10, latency20, latency30 := int64(10), int64(20), int64(30)
	ttft5, ttft15 := int64(5), int64(15)
	events := []usage.Event{
		{EventHash: "match", TimestampMS: base.UnixMilli(), Timestamp: base.Format(time.RFC3339Nano), Model: "gpt-a", Provider: "codex", AuthFileSnapshot: "a.json", LatencyMS: &latency20, TTFTMS: &ttft15, CreatedAtMS: base.UnixMilli()},
		{EventHash: "low-latency", TimestampMS: base.Add(time.Minute).UnixMilli(), Timestamp: base.Add(time.Minute).Format(time.RFC3339Nano), Model: "gpt-a", Provider: "codex", AuthFileSnapshot: "a.json", LatencyMS: &latency10, TTFTMS: &ttft5, CreatedAtMS: base.Add(time.Minute).UnixMilli()},
		{EventHash: "failed", TimestampMS: base.Add(2 * time.Minute).UnixMilli(), Timestamp: base.Add(2 * time.Minute).Format(time.RFC3339Nano), Model: "gpt-a", Provider: "codex", AuthFileSnapshot: "a.json", LatencyMS: &latency30, Failed: true, CreatedAtMS: base.Add(2 * time.Minute).UnixMilli()},
		{EventHash: "other", TimestampMS: base.Add(3 * time.Minute).UnixMilli(), Timestamp: base.Add(3 * time.Minute).Format(time.RFC3339Nano), Model: "gpt-b", Provider: "claude", AuthFileSnapshot: "b.json", TTFTMS: &ttft15, CreatedAtMS: base.Add(3 * time.Minute).UnixMilli()},
	}
	if _, err := New(db).InsertBatch(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	filter := AnalyticsFilter{
		FromMS:        base.UnixMilli(),
		ToMS:          base.Add(time.Hour).UnixMilli(),
		Models:        []string{"gpt-a"},
		Providers:     []string{"CODEX"},
		AuthFiles:     []string{"a.json"},
		MinLatencyMS:  15,
		IncludeFailed: false,
	}
	got, err := New(db).LatencyPercentilesWithFilter(context.Background(), filter, "hour", time.UTC)
	if err != nil {
		t.Fatalf("latency percentiles: %v", err)
	}
	want := rawLatencyPercentiles(events, filter, "hour", time.UTC)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("percentiles mismatch\ngot=%#v\nwant=%#v", got, want)
	}
}

func BenchmarkLatencyPercentilesWithFilter(b *testing.B) {
	for _, count := range []int{10_000, 100_000} {
		b.Run(fmt.Sprintf("events_%dk", count/1_000), func(b *testing.B) {
			db, err := sqliterepo.Open(filepath.Join(b.TempDir(), "usage.sqlite"))
			if err != nil {
				b.Fatalf("open database: %v", err)
			}
			b.Cleanup(func() { _ = db.Close() })

			ctx := context.Background()
			base := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
			events := make([]usage.Event, 0, 1_000)
			for start := 0; start < count; start += 1_000 {
				events = events[:0]
				end := min(start+1_000, count)
				for index := start; index < end; index++ {
					timestamp := base.Add(time.Duration(index) * time.Second)
					latencyMS := int64(50 + index%2_000)
					ttftMS := int64(10 + index%500)
					events = append(events, usage.Event{
						EventHash:   fmt.Sprintf("latency-benchmark-%06d", index),
						TimestampMS: timestamp.UnixMilli(),
						Timestamp:   timestamp.Format(time.RFC3339Nano),
						Model:       fmt.Sprintf("gpt-%02d", index%12),
						Provider:    []string{"codex", "claude", "gemini"}[index%3],
						LatencyMS:   &latencyMS,
						TTFTMS:      &ttftMS,
						CreatedAtMS: timestamp.UnixMilli(),
					})
				}
				if _, err := New(db).InsertBatch(ctx, events); err != nil {
					b.Fatalf("insert events: %v", err)
				}
			}

			repo := New(db)
			filter := AnalyticsFilter{
				FromMS:        base.UnixMilli(),
				ToMS:          base.Add(time.Duration(count) * time.Second).UnixMilli(),
				IncludeFailed: true,
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := repo.LatencyPercentilesWithFilter(ctx, filter, "day", time.UTC); err != nil {
					b.Fatalf("latency percentiles: %v", err)
				}
			}
		})
	}
}

func rawLatencyPercentiles(events []usage.Event, filter AnalyticsFilter, granularity string, location *time.Location) []LatencyPercentiles {
	type samples struct {
		latencies []float64
		ttfts     []float64
	}
	grouped := map[int64]*samples{}
	order := make([]int64, 0)
	for _, event := range events {
		if event.TimestampMS < filter.FromMS || event.TimestampMS >= filter.ToMS || (!filter.IncludeFailed && event.Failed) {
			continue
		}
		if len(filter.Models) > 0 && event.Model != filter.Models[0] {
			continue
		}
		if len(filter.Providers) > 0 && event.Provider != "codex" {
			continue
		}
		if len(filter.AuthFiles) > 0 && event.AuthFileSnapshot != filter.AuthFiles[0] {
			continue
		}
		if filter.MinLatencyMS > 0 && (event.LatencyMS == nil || *event.LatencyMS < filter.MinLatencyMS) {
			continue
		}
		if (event.LatencyMS == nil || *event.LatencyMS <= 0) && (event.TTFTMS == nil || *event.TTFTMS <= 0) {
			continue
		}
		bucketMS := usage.AnalyticsBucketMS(event.TimestampMS, granularity, location)
		bucket := grouped[bucketMS]
		if bucket == nil {
			bucket = &samples{}
			grouped[bucketMS] = bucket
			order = append(order, bucketMS)
		}
		if event.LatencyMS != nil && *event.LatencyMS > 0 {
			bucket.latencies = append(bucket.latencies, float64(*event.LatencyMS))
		}
		if event.TTFTMS != nil && *event.TTFTMS > 0 {
			bucket.ttfts = append(bucket.ttfts, float64(*event.TTFTMS))
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	result := make([]LatencyPercentiles, 0, len(order))
	for _, bucketMS := range order {
		point := LatencyPercentiles{BucketMS: bucketMS}
		bucket := grouped[bucketMS]
		if value, ok := percentile95(bucket.latencies); ok {
			point.P95LatencyMS = sql.NullFloat64{Float64: value, Valid: true}
		}
		if value, ok := percentile95(bucket.ttfts); ok {
			point.P95TTFTMS = sql.NullFloat64{Float64: value, Valid: true}
		}
		result = append(result, point)
	}
	return result
}
