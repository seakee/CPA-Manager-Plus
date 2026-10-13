package usageevent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const latencyCoverIndex = "idx_usage_events_ts_latency_ttft_cover"
const latencyCoverDDL = `create index if not exists idx_usage_events_ts_latency_ttft_cover on usage_events(timestamp_ms, latency_ms, ttft_ms)`
const latencyPerfBase int64 = 1767225600000
const latencyPerfDay int64 = 86400000

// Opt-in integration experiment: creates synthetic fixtures; resume requires
// a matching synthetic marker before opening for writes. Set
// CPAMP_LATENCY_PERF=1 and optionally CPAMP_LATENCY_PERF_EVENTS / _RAW_BYTES.
// The generated fixture is retained for TestLatencyCoveringIndexAPIExperiment.
func TestLatencyCoveringIndexPerformanceExperiment(t *testing.T) {
	if os.Getenv("CPAMP_LATENCY_PERF") != "1" {
		t.Skip("opt-in synthetic performance experiment")
	}
	count := latencyPerfEnvInt(t, "CPAMP_LATENCY_PERF_EVENTS", 100000)
	padding := latencyPerfEnvInt(t, "CPAMP_LATENCY_PERF_RAW_BYTES", 850)
	if count > 4000000 || padding > 2048 {
		t.Fatal("synthetic fixture size exceeds experiment limit")
	}
	path := os.Getenv("CPAMP_LATENCY_PERF_RESUME")
	resume := path != ""
	if !resume {
		dir, err := os.MkdirTemp("", "cpamp-latency-perf-")
		if err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(dir, "synthetic.sqlite")
	} else {
		// Validate before Open can run any migration or create a missing file.
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatal("resume fixture missing")
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
		uriPath := filepath.ToSlash(absolute)
		if !strings.HasPrefix(uriPath, "/") {
			uriPath = "/" + uriPath
		}
		dsn := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro"}).String()
		probe, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		var marker int
		err = probe.QueryRow(`select event_count from cpamp_latency_benchmark_fixture`).Scan(&marker)
		_ = probe.Close()
		if err != nil || marker != count {
			t.Fatal("resume requires matching synthetic fixture marker")
		}
	}
	lock, err := processlock.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	db, err := sqliterepo.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if !resume {
		latencyPerfExec(t, db, `create table cpamp_latency_benchmark_fixture(event_count integer not null)`)
		latencyPerfExec(t, db, `insert into cpamp_latency_benchmark_fixture values (?)`, count)
	}
	if err := sqliterepo.RunDerivedStartupMaintenance(ctx, db); err != nil {
		t.Fatal(err)
	}
	latencyPerfExec(t, db, `drop index if exists `+latencyCoverIndex)
	// Fixture generation is not a query/write benchmark. A bounded cache avoids
	// unnecessary random seed IO; restore CPAMP's default before measuring.
	db.SetMaxOpenConns(1)
	latencyPerfExec(t, db, `pragma cache_size=-131072`)
	seedStart := time.Now()
	seeded := 0
	if resume {
		var maxID int
		if err := db.QueryRow(`select count(*),coalesce(max(id),0) from usage_events`).Scan(&seeded, &maxID); err != nil {
			t.Fatal(err)
		}
		if seeded != maxID || seeded > count {
			t.Fatal("resume fixture is not an interrupted seed")
		}
		t.Logf("RESUME path=%s seeded=%d target=%d", path, seeded, count)
	}
	// Timestamps deliberately differ from insertion order, like concurrent
	// collectors/imports. All payload text is synthetic and contains no keys.
	for start := seeded; start < count; start += 10000 {
		latencyPerfExec(t, db, `with recursive ids(i) as (select ? union all select i+1 from ids where i+1 < ?)
insert into usage_events(event_hash,timestamp_ms,timestamp,model,requested_model,provider,auth_provider_snapshot,
auth_file_snapshot,auth_index,account_snapshot,source,api_key_hash,input_tokens,output_tokens,total_tokens,
latency_ms,ttft_ms,failed,raw_json,created_at_ms)
select printf('%064x',i+1), ? + ((i*7919) % ?)*? / ?, '2026-01-01T00:00:00Z',
printf('gpt-%02d',i%12), '', case i%3 when 0 then 'codex' when 1 then 'claude' else 'gemini' end,
case i%3 when 0 then 'codex' when 1 then 'claude' else 'gemini' end,
printf('account-%03d.json',i%100),printf('auth-%03d',i%100),printf('account-%03d@example.test',i%100),
printf('account-%03d.json',i%100),printf('key-%02d',i%10),1000,200,1200,
case when i%17=0 then null when i%19=0 then 0 else 50+(i*37)%20000 end,
case when i%23=0 then null when i%29=0 then 0 else 10+(i*13)%5000 end,i%20=0,?,?
from ids`, start, min(start+10000, count), latencyPerfBase, count, 120*latencyPerfDay, count,
			`{"synthetic":"`+strings.Repeat("x", padding)+`"}`, latencyPerfBase)
		if (start/10000)%25 == 0 {
			t.Logf("SEED rows=%d/%d", min(start+10000, count), count)
		}
	}
	latencyPerfExec(t, db, `pragma wal_checkpoint(truncate)`)
	latencyPerfExec(t, db, `pragma cache_size=-2000`)
	db.SetMaxOpenConns(4)
	var version string
	if err := db.QueryRow(`select sqlite_version()`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("FIXTURE path=%s events=%d bytes=%d sqlite=%s go=%s cpus=%d seed_ms=%.1f", path, count, latencyPerfFileSize(path), version, runtime.Version(), runtime.NumCPU(), float64(time.Since(seedStart).Microseconds())/1000)
	build := time.Now()
	latencyPerfExec(t, db, latencyCoverDDL)
	buildMS := float64(time.Since(build).Microseconds()) / 1000
	latencyPerfExec(t, db, `pragma wal_checkpoint(truncate)`)
	var indexBytes int64
	if err := db.QueryRow(`select coalesce(sum(pgsize),0) from dbstat where name=?`, latencyCoverIndex).Scan(&indexBytes); err != nil {
		t.Fatal(err)
	}
	t.Logf("INDEX build_ms=%.1f bytes=%d database_bytes=%d", buildMS, indexBytes, latencyPerfFileSize(path))
	if os.Getenv("CPAMP_LATENCY_PERF_MODE") == "writes" {
		latencyPerfWrites(t, db, path)
		return
	}
	if os.Getenv("CPAMP_LATENCY_PERF_MODE") == "engine" {
		latencyPerfEngineWindows(t, db)
		return
	}
	repo := New(db)
	timelineTemplate := latencyPerfSQL(t, "LatencyPercentilesWithFilter")
	summaryTemplate := latencyPerfSQL(t, "LatencySummaryWithFilter")
	filters := []struct {
		name   string
		change func(*AnalyticsFilter)
	}{
		{"all", func(f *AnalyticsFilter) {}},
		{"success", func(f *AnalyticsFilter) { f.IncludeFailed = false }},
		{"provider", func(f *AnalyticsFilter) { f.Providers = []string{"codex"} }},
		{"model", func(f *AnalyticsFilter) { f.Models = []string{"gpt-00"} }},
		{"account", func(f *AnalyticsFilter) { f.Accounts = []string{"auth-000"} }},
		{"threshold", func(f *AnalyticsFilter) { f.MinLatencyMS = 10000 }},
		{"combined", func(f *AnalyticsFilter) {
			f.IncludeFailed = false
			f.Providers = []string{"claude"}
			f.Models = []string{"gpt-01"}
			f.Accounts = []string{"auth-001"}
			f.MinLatencyMS = 1000
		}},
	}
	for _, days := range []int{7, 30, 60, 90, 120} {
		for _, scenario := range filters {
			// Every window has an unfiltered comparison. Filtered comparisons use
			// 30 days so the large experiment remains bounded.
			if scenario.name != "all" && (days != 30 || os.Getenv("CPAMP_LATENCY_PERF_MODE") == "windows") {
				continue
			}
			filter := AnalyticsFilter{FromMS: latencyPerfBase + (120-int64(days))*latencyPerfDay, ToMS: latencyPerfBase + 120*latencyPerfDay, IncludeFailed: true}
			scenario.change(&filter)
			where, args := analyticsWhere(filter)
			for _, kind := range []string{"timeline", "summary"} {
				if kind == "summary" && days == 60 {
					continue
				}
				template := timelineTemplate
				queryArgs := args
				if kind == "summary" {
					template = summaryTemplate
					queryArgs = append(append([]any{}, args...), args...)
				}
				query := fmt.Sprintf(template, where)
				if kind == "summary" {
					query = fmt.Sprintf(template, where, where)
				}
				name := fmt.Sprintf("%s/%dd/%s", kind, days, scenario.name)
				var reference any
				forcedResults := map[string]any{}
				for _, index := range []string{"idx_usage_events_timestamp", latencyCoverIndex} {
					forced := strings.ReplaceAll(query, "from usage_events ", "from usage_events indexed by "+index+" ")
					forcedResults[index] = latencyPerfScan(t, db, forced, queryArgs)
				}
				if !reflect.DeepEqual(forcedResults["idx_usage_events_timestamp"], forcedResults[latencyCoverIndex]) {
					t.Fatalf("forced results differ: %s", name)
				}
				reference = forcedResults[latencyCoverIndex]
				timings := map[string][]float64{}
				for round := -1; round < 5; round++ {
					order := []string{"idx_usage_events_timestamp", latencyCoverIndex}
					if round%2 != 0 {
						order[0], order[1] = order[1], order[0]
					}
					for _, index := range order {
						forced := strings.ReplaceAll(query, "from usage_events ", "from usage_events indexed by "+index+" ")
						start := time.Now()
						got := latencyPerfScan(t, db, forced, queryArgs)
						elapsed := float64(time.Since(start).Microseconds()) / 1000
						if !reflect.DeepEqual(got, reference) {
							t.Fatalf("results changed: %s", name)
						}
						if round >= 0 {
							timings[index] = append(timings[index], elapsed)
						}
					}
				}
				for _, index := range []string{"idx_usage_events_timestamp", latencyCoverIndex} {
					forced := strings.ReplaceAll(query, "from usage_events ", "from usage_events indexed by "+index+" ")
					latencyPerfReport(t, name, index, timings[index], len(reference.([][3]int64)), latencyPerfPlan(t, db, forced, queryArgs))
				}
				// Real repository uses the natural planner; this includes grouping,
				// sorting and bucket conversion for timeline queries.
				var repoResult any
				var natural []float64
				for round := -1; round < 5; round++ {
					start := time.Now()
					var got any
					var err error
					if kind == "timeline" {
						got, err = repo.LatencyPercentilesWithFilter(ctx, filter, "day", time.UTC)
					} else {
						got, err = repo.LatencySummaryWithFilter(ctx, filter)
					}
					elapsed := float64(time.Since(start).Microseconds()) / 1000
					if err != nil {
						t.Fatal(err)
					}
					if round == -1 {
						repoResult = got
					} else if !reflect.DeepEqual(got, repoResult) {
						t.Fatalf("natural result changed: %s", name)
					}
					if round >= 0 {
						natural = append(natural, elapsed)
					}
				}
				latencyPerfReport(t, name, "repository_natural", natural, len(reference.([][3]int64)), latencyPerfPlan(t, db, query, queryArgs))
				if kind == "summary" {
					var expected LatencySummary
					for _, row := range reference.([][3]int64) {
						value := sql.NullFloat64{Float64: float64(row[1]), Valid: true}
						if row[0] == 1 {
							expected.P95TTFTMS = value
						} else {
							expected.P95LatencyMS = value
						}
					}
					if repoResult != expected {
						t.Fatalf("natural summary differs from forced samples: %s", name)
					}
				}
				if kind == "timeline" && scenario.name == "all" {
					var grouping []float64
					for round := -1; round < 5; round++ {
						start := time.Now()
						got := latencyPerfGroup(reference.([][3]int64))
						elapsed := float64(time.Since(start).Microseconds()) / 1000
						if !reflect.DeepEqual(got, repoResult) {
							t.Fatalf("Go grouping result mismatch %s", name)
						}
						if round >= 0 {
							grouping = append(grouping, elapsed)
						}
					}
					latencyPerfReport(t, name, "go_group_sort", grouping, len(reference.([][3]int64)), nil)
				}
			}
		}
	}
	if count == 100000 {
		latencyPerfWrites(t, db, path)
	}
	latencyPerfEngineWindows(t, db)
	latencyPerfExec(t, db, `pragma wal_checkpoint(truncate)`)
	t.Logf("RETAINED synthetic fixture %s", path)
}

// A scalar checksum forces all three selected columns to be evaluated inside
// SQLite while returning only two integers to Go. The extra SUM is disclosed
// separately; this is not treated as the exact original query's VM-only time.
func latencyPerfEngineWindows(t *testing.T, db *sql.DB) {
	for _, days := range []int64{7, 30, 60, 90, 120} {
		filter := AnalyticsFilter{FromMS: latencyPerfBase + (120-days)*latencyPerfDay, ToMS: latencyPerfBase + 120*latencyPerfDay, IncludeFailed: true}
		where, args := analyticsWhere(filter)
		query := fmt.Sprintf(latencyPerfSQL(t, "LatencyPercentilesWithFilter"), where)
		query = strings.Replace(query, "coalesce(latency_ms, 0)", "coalesce(latency_ms, 0) as latency_sample_ms", 1)
		query = strings.Replace(query, "coalesce(ttft_ms, 0)", "coalesce(ttft_ms, 0) as ttft_sample_ms", 1)
		query = `select count(*),coalesce(sum(timestamp_ms+latency_sample_ms+ttft_sample_ms),0) from (` + query + `)`
		measure := map[string][]float64{}
		var reference [2]int64
		for round := -1; round < 5; round++ {
			order := []string{"idx_usage_events_timestamp", latencyCoverIndex}
			if round%2 != 0 {
				order[0], order[1] = order[1], order[0]
			}
			for _, index := range order {
				forced := strings.ReplaceAll(query, "from usage_events ", "from usage_events indexed by "+index+" ")
				var result [2]int64
				start := time.Now()
				if err := db.QueryRow(forced, args...).Scan(&result[0], &result[1]); err != nil {
					t.Fatal(err)
				}
				elapsed := float64(time.Since(start).Microseconds()) / 1000
				if round == -1 && index == order[0] {
					reference = result
				} else if result != reference {
					t.Fatal("engine checksum changed")
				}
				if round >= 0 {
					measure[index] = append(measure[index], elapsed)
				}
			}
		}
		for _, index := range []string{"idx_usage_events_timestamp", latencyCoverIndex} {
			latencyPerfReport(t, fmt.Sprintf("engine_checksum/%dd/all", days), index, measure[index], int(reference[0]), nil)
		}
	}
}

// Independently time Go bucketing and the production nearest-rank helper on
// already-scanned samples; no subtraction of SQL timings is used as evidence.
func latencyPerfGroup(samples [][3]int64) []LatencyPercentiles {
	result := make([]LatencyPercentiles, 0)
	var bucket int64
	hasBucket := false
	var latency, ttft []float64
	flush := func() {
		if !hasBucket {
			return
		}
		point := LatencyPercentiles{BucketMS: bucket}
		if value, ok := percentile95(latency); ok {
			point.P95LatencyMS = sql.NullFloat64{Float64: value, Valid: true}
		}
		if value, ok := percentile95(ttft); ok {
			point.P95TTFTMS = sql.NullFloat64{Float64: value, Valid: true}
		}
		result = append(result, point)
		latency = latency[:0]
		ttft = ttft[:0]
	}
	for _, sample := range samples {
		next := usage.AnalyticsBucketMS(sample[0], "day", time.UTC)
		if !hasBucket || next != bucket {
			flush()
			bucket = next
			hasBucket = true
		}
		if sample[1] > 0 {
			latency = append(latency, float64(sample[1]))
		}
		if sample[2] > 0 {
			ttft = append(ttft, float64(sample[2]))
		}
	}
	flush()
	return result
}

func TestLatencyCoveringIndexPreservesPercentilesAndUsesCoveringPlan(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := sqliterepo.RunDerivedStartupMaintenance(ctx, db); err != nil {
		t.Fatal(err)
	}
	latencyPerfExec(t, db, `drop index if exists `+latencyCoverIndex)
	latencyPerfExec(t, db, `with recursive ids(i) as (select 1 union all select i+1 from ids where i<240)
insert into usage_events(event_hash,timestamp_ms,timestamp,model,provider,auth_provider_snapshot,auth_index,auth_file_snapshot,latency_ms,ttft_ms,failed,created_at_ms)
select printf('%064x',i),?+i*3600000,'synthetic','model-'||(i%2),'codex','codex','auth-'||(i%3),'account-'||(i%3)||'.json',case when i%17=0 then null else i%31 end,case when i%19=0 then null else i%11 end,i%5=0,1 from ids`, latencyPerfBase)
	filters := []AnalyticsFilter{
		{IncludeFailed: true}, {IncludeFailed: false}, {IncludeFailed: true, Providers: []string{"codex"}},
		{IncludeFailed: true, Models: []string{"model-1"}}, {IncludeFailed: true, Accounts: []string{"auth-1"}},
		{IncludeFailed: true, MinLatencyMS: 20}, {IncludeFailed: false, Models: []string{"model-1"}, MinLatencyMS: 10},
	}
	for i := range filters {
		filters[i].FromMS = latencyPerfBase
		filters[i].ToMS = latencyPerfBase + 11*latencyPerfDay
	}
	repo := New(db)
	var timelineBefore [][]LatencyPercentiles
	var summaryBefore []LatencySummary
	for _, filter := range filters {
		points, err := repo.LatencyPercentilesWithFilter(ctx, filter, "day", time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		summary, err := repo.LatencySummaryWithFilter(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		timelineBefore = append(timelineBefore, points)
		summaryBefore = append(summaryBefore, summary)
	}
	latencyPerfExec(t, db, latencyCoverDDL)
	for i, filter := range filters {
		points, err := repo.LatencyPercentilesWithFilter(ctx, filter, "day", time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		summary, err := repo.LatencySummaryWithFilter(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(points, timelineBefore[i]) || summary != summaryBefore[i] {
			t.Fatalf("percentile result changed for filter %d", i)
		}
		if i == 0 || i == 1 {
			where, args := analyticsWhere(filter)
			for _, fn := range []string{"LatencyPercentilesWithFilter", "LatencySummaryWithFilter"} {
				template := latencyPerfSQL(t, fn)
				query := fmt.Sprintf(template, where)
				queryArgs := args
				if fn == "LatencySummaryWithFilter" {
					query = fmt.Sprintf(template, where, where)
					queryArgs = append(append([]any{}, args...), args...)
				}
				plan := strings.Join(latencyPerfPlan(t, db, query, queryArgs), "\n")
				covered := strings.Contains(plan, "USING COVERING INDEX "+latencyCoverIndex)
				if covered != (i == 0) {
					t.Fatalf("unexpected covering plan for filter %d: %s", i, plan)
				}
			}
		}
	}
}

func latencyPerfEnvInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	if os.Getenv(key) == "" {
		return fallback
	}
	n, err := strconv.Atoi(os.Getenv(key))
	if err != nil || n <= 0 {
		t.Fatalf("invalid %s", key)
	}
	return n
}
func latencyPerfExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
func latencyPerfFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// Extract the exact SQL literal so this opt-in test cannot silently benchmark a
// different query after a production change. Only INDEXED BY is added below.
func latencyPerfSQL(t *testing.T, function string) string {
	t.Helper()
	source, err := os.ReadFile("analytics.go")
	if err != nil {
		t.Fatal(err)
	}
	_, tail, ok := strings.Cut(string(source), "func (r *repository) "+function+"(")
	if !ok {
		t.Fatal("SQL function missing")
	}
	_, tail, ok = strings.Cut(tail, "query := fmt.Sprintf(`")
	if !ok {
		t.Fatal("SQL literal missing")
	}
	query, _, ok := strings.Cut(tail, "`, where")
	if !ok {
		t.Fatal("SQL end missing")
	}
	return query
}
func latencyPerfScan(t *testing.T, db *sql.DB, query string, args []any) [][3]int64 {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := make([][3]int64, 0)
	columnCount := len(rowsColumns(t, rows))
	for rows.Next() {
		var row [3]int64
		if columnCount == 3 {
			err = rows.Scan(&row[0], &row[1], &row[2])
		} else {
			var kind string
			err = rows.Scan(&kind, &row[1])
			if kind == "ttft" {
				row[0] = 1
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
func rowsColumns(t *testing.T, rows *sql.Rows) []string {
	t.Helper()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	return columns
}
func latencyPerfPlan(t *testing.T, db *sql.DB, query string, args []any) []string {
	t.Helper()
	rows, err := db.Query("explain query plan "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var a, b, c int
		var detail string
		if err := rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan
}
func latencyPerfReport(t *testing.T, name, index string, values []float64, rows int, plan []string) {
	t.Helper()
	sort.Float64s(values)
	report := map[string]any{"name": name, "index": index, "median_ms": values[len(values)/2], "p95_ms": values[len(values)-1], "min_ms": values[0], "max_ms": values[len(values)-1], "runs": len(values), "rows": rows, "plan": plan}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("PERF " + string(data))
}

func latencyPerfWrites(t *testing.T, db *sql.DB, path string) {
	t.Helper()
	ctx := context.Background()
	// Identical independent copies avoid measuring writes immediately after
	// repeated index rebuilds, which biases the cache and changes WAL state.
	dir := t.TempDir()
	var databases [2]*sql.DB
	var repositories [2]Repository
	var paths [2]string
	var sequence [2]int
	for variant := range databases {
		paths[variant] = filepath.Join(dir, fmt.Sprintf("write-%d.sqlite", variant))
		latencyPerfExec(t, db, `vacuum into ?`, paths[variant])
		clone, err := sqliterepo.Open(paths[variant])
		if err != nil {
			t.Fatal(err)
		}
		databases[variant] = clone
		repositories[variant] = New(clone)
		t.Cleanup(func() { _ = clone.Close() })
		latencyPerfExec(t, clone, `drop index if exists `+latencyCoverIndex)
		if variant == 1 {
			latencyPerfExec(t, clone, latencyCoverDDL)
		}
		latencyPerfExec(t, clone, `pragma wal_checkpoint(truncate)`)
	}
	makeBatch := func(variant, size int) []usage.Event {
		batch := make([]usage.Event, size)
		for i := range batch {
			sequence[variant]++
			latency, ttft := int64(200+i), int64(50+i)
			batch[i] = usage.Event{EventHash: canonicalTestHash(fmt.Sprintf("stable-write-%d", sequence[variant])), TimestampMS: latencyPerfBase + 120*latencyPerfDay + int64(sequence[variant]), Timestamp: "2026-05-01T00:00:00Z", Model: "gpt-00", Provider: "codex", AuthFileSnapshot: "synthetic-write.json", InputTokens: 1000, OutputTokens: 200, TotalTokens: 1200, LatencyMS: &latency, TTFTMS: &ttft, RawJSON: `{"synthetic":true}`, CreatedAtMS: latencyPerfBase}
		}
		return batch
	}
	for _, size := range []int{1, 100, 1000} {
		measure := map[string][]float64{}
		for round := -1; round < 5; round++ {
			order := []bool{false, true}
			if round%2 != 0 {
				order[0], order[1] = order[1], order[0]
			}
			for _, cover := range order {
				variant := 0
				if cover {
					variant = 1
				}
				db, repo, path := databases[variant], repositories[variant], paths[variant]
				batch := makeBatch(variant, size)
				latencyPerfExec(t, db, `pragma wal_checkpoint(truncate)`)
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				cpuBefore := latencyPerfProcessCPU()
				start := time.Now()
				result, err := repo.InsertBatch(ctx, batch)
				elapsed := float64(time.Since(start).Microseconds()) / 1000
				cpuMS := latencyPerfProcessCPU() - cpuBefore
				if cpuBefore < 0 {
					cpuMS = -1
				}
				runtime.ReadMemStats(&after)
				if err != nil {
					t.Fatal(err)
				}
				if result.Inserted != size {
					t.Fatalf("insert result %+v", result)
				}
				duplicate, err := repo.InsertBatch(ctx, batch)
				if err != nil || duplicate.Inserted != 0 || duplicate.Skipped != size {
					t.Fatalf("duplicate result %+v err=%v", duplicate, err)
				}
				key := fmt.Sprintf("cover=%t", cover)
				if round >= 0 {
					measure[key] = append(measure[key], elapsed)
				}
				t.Logf("WRITE size=%d cover=%t round=%d ms=%.3f cpu_ms=%.3f rows_per_s=%.1f allocated_bytes=%d wal_bytes=%d", size, cover, round, elapsed, cpuMS, float64(size)*1000/elapsed, after.TotalAlloc-before.TotalAlloc, latencyPerfFileSize(path+"-wal"))
			}
		}
		for _, key := range []string{"cover=false", "cover=true"} {
			latencyPerfReport(t, fmt.Sprintf("write/%d", size), key, measure[key], size, nil)
		}
	}
	for _, concurrent := range []bool{false, true} {
		measure := map[string][]float64{}
		for round := -1; round < 5; round++ {
			for step := 0; step < 2; step++ {
				variant := (round + 2 + step) % 2
				db, repo, path := databases[variant], repositories[variant], paths[variant]
				latencyPerfExec(t, db, `pragma wal_checkpoint(truncate)`)
				var wg sync.WaitGroup
				stop := make(chan struct{})
				readErr := make(chan error, 1)
				if concurrent {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for {
							_, err := repo.LatencyPercentilesWithFilter(ctx, AnalyticsFilter{FromMS: latencyPerfBase, ToMS: latencyPerfBase + 120*latencyPerfDay, IncludeFailed: true}, "day", time.UTC)
							if err != nil {
								readErr <- err
								return
							}
							select {
							case <-stop:
								return
							default:
							}
						}
					}()
				}
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				cpuBefore := latencyPerfProcessCPU()
				start := time.Now()
				for j := 0; j < 20; j++ {
					result, err := repo.InsertBatch(ctx, makeBatch(variant, 1000))
					if err != nil || result.Inserted != 1000 {
						close(stop)
						wg.Wait()
						t.Fatalf("sustained %+v %v", result, err)
					}
				}
				writeMS := float64(time.Since(start).Microseconds()) / 1000
				close(stop)
				wg.Wait()
				cpuMS := latencyPerfProcessCPU() - cpuBefore
				if cpuBefore < 0 {
					cpuMS = -1
				}
				runtime.ReadMemStats(&after)
				select {
				case err := <-readErr:
					t.Fatal(err)
				default:
				}
				key := fmt.Sprintf("cover=%t", variant == 1)
				if round >= 0 {
					measure[key] = append(measure[key], writeMS)
				}
				t.Logf("SUSTAINED concurrent=%t cover=%t round=%d written=20000 write_ms=%.3f cpu_ms=%.3f allocated_bytes=%d wal_bytes=%d", concurrent, variant == 1, round, writeMS, cpuMS, after.TotalAlloc-before.TotalAlloc, latencyPerfFileSize(path+"-wal"))
			}
		}
		for _, key := range []string{"cover=false", "cover=true"} {
			latencyPerfReport(t, fmt.Sprintf("sustained/concurrent=%t", concurrent), key, measure[key], 20000, nil)
		}
	}
	var snapshots [2][4]int64
	for variant, db := range databases {
		v := &snapshots[variant]
		if err := db.QueryRow(`select count(*),sum(timestamp_ms),sum(coalesce(latency_ms,0)),sum(coalesce(ttft_ms,0)) from usage_events`).Scan(&v[0], &v[1], &v[2], &v[3]); err != nil {
			t.Fatal(err)
		}
	}
	if snapshots[0] != snapshots[1] {
		t.Fatalf("write history mismatch: %v", snapshots)
	}
}
