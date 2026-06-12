package usageevent

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	compatCachedExpr  = "max(max(cached_tokens, cache_tokens) - max(cache_read_tokens, 0) - max(cache_creation_tokens, 0), 0)"
	compatCachedFExpr = "max(max(f.cached_tokens, f.cache_tokens) - max(f.cache_read_tokens, 0) - max(f.cache_creation_tokens, 0), 0)"
)

type AnalyticsFilter struct {
	FromMS           int64
	ToMS             int64
	SearchQuery      string
	SearchAPIKeyHash string
	Models           []string
	Providers        []string
	Accounts         []string
	AuthFiles        []string
	AuthIndices      []string
	APIKeyHashes     []string
	SourceHashes     []string
	ProjectIDs       []string
	RequestTypes     []string
	IncludeFailed    bool
	FailedOnly       bool
	MinLatencyMS     int64
	CacheStatus      string
}

var analyticsSearchTextColumns = []string{
	"request_id",
	"event_hash",
	"model",
	"resolved_model",
	"endpoint",
	"method",
	"path",
	"source",
	"source_hash",
	"api_key_hash",
	"auth_index",
	"account_snapshot",
	"auth_label_snapshot",
	"auth_file_snapshot",
	"auth_provider_snapshot",
	"auth_project_id_snapshot",
	"reasoning_effort",
	"service_tier",
	"executor_type",
	"fail_summary",
}

type LatencyPercentiles struct {
	BucketMS     int64
	P95LatencyMS sql.NullFloat64
	P95TTFTMS    sql.NullFloat64
}

type LatencySummary struct {
	P95LatencyMS sql.NullFloat64
	P95TTFTMS    sql.NullFloat64
}

type FilterOptionValues struct {
	Providers    []string
	AuthFiles    []string
	ProjectIDs   []string
	RequestTypes []string
}

type TimelinePoint struct {
	BucketMS            int64
	Model               string
	BillingModel        string
	ServiceTier         string
	Calls               int64
	Tokens              int64
	Success             int64
	Failure             int64
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	AvgLatencyMS        sql.NullFloat64
	LatencySamples      int64
}

type HourlyPoint struct {
	Hour   int
	Calls  int64
	Tokens int64
}

type HeatmapPoint struct {
	Weekday             int
	Hour                int
	Model               string
	BillingModel        string
	ServiceTier         string
	APIKeyHash          string
	Provider            string
	Calls               int64
	SuccessCalls        int64
	FailureCalls        int64
	InputTokens         int64
	OutputTokens        int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
}

type ChannelModelStat struct {
	AuthIndex            string
	Source               string
	AccountSnapshot      string
	AuthLabelSnapshot    string
	AuthProviderSnapshot string
	Model                string
	BillingModel         string
	ServiceTier          string
	Calls                int64
	SuccessCalls         int64
	FailureCalls         int64
	InputTokens          int64
	OutputTokens         int64
	CachedTokens         int64
	CacheReadTokens      int64
	CacheCreationTokens  int64
	TotalTokens          int64
	AvgLatencyMS         sql.NullFloat64
	LatencySamples       int64
}

type FailureSourceStat struct {
	Source               string
	SourceHash           string
	AuthIndex            string
	AccountSnapshot      string
	AuthLabelSnapshot    string
	AuthProviderSnapshot string
	Calls                int64
	FailureCalls         int64
	LastSeenMS           int64
	AvgLatencyMS         sql.NullFloat64
}

type AccountModelStat struct {
	AccountSnapshot      string
	AuthLabelSnapshot    string
	AuthProviderSnapshot string
	AuthIndex            string
	Source               string
	SourceHash           string
	Model                string
	BillingModel         string
	ServiceTier          string
	Calls                int64
	SuccessCalls         int64
	FailureCalls         int64
	InputTokens          int64
	OutputTokens         int64
	CachedTokens         int64
	CacheReadTokens      int64
	CacheCreationTokens  int64
	TotalTokens          int64
	LastSeenMS           int64
	AvgLatencyMS         sql.NullFloat64
	LatencySamples       int64
}

type CredentialModelStat struct {
	ID                    string
	AuthFileSnapshot      string
	AuthIndex             string
	Source                string
	SourceHash            string
	AccountSnapshot       string
	AuthLabelSnapshot     string
	AuthProviderSnapshot  string
	AuthProjectIDSnapshot string
	Model                 string
	BillingModel          string
	ServiceTier           string
	Calls                 int64
	SuccessCalls          int64
	FailureCalls          int64
	InputTokens           int64
	OutputTokens          int64
	CachedTokens          int64
	CacheReadTokens       int64
	CacheCreationTokens   int64
	TotalTokens           int64
	LastSeenMS            int64
	AvgLatencyMS          sql.NullFloat64
	LatencySamples        int64
}

type APIKeyModelStat struct {
	APIKeyHash           string
	AccountSnapshot      string
	AuthLabelSnapshot    string
	AuthProviderSnapshot string
	AuthIndex            string
	Source               string
	SourceHash           string
	Model                string
	BillingModel         string
	ServiceTier          string
	Calls                int64
	SuccessCalls         int64
	FailureCalls         int64
	InputTokens          int64
	OutputTokens         int64
	CachedTokens         int64
	CacheReadTokens      int64
	CacheCreationTokens  int64
	TotalTokens          int64
	LastSeenMS           int64
	AvgLatencyMS         sql.NullFloat64
	LatencySamples       int64
}

type TaskBucket struct {
	BucketKey           string
	Total               int64
	Success             int64
	Failure             int64
	FirstMS             int64
	LastMS              int64
	Source              string
	SourceHash          string
	AuthIndex           string
	Models              string
	Endpoints           string
	InputTokens         int64
	OutputTokens        int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
	AvgLatencyMS        sql.NullFloat64
	MaxLatencyMS        sql.NullInt64
}

type EventPageItem struct {
	ID                    int64
	RequestID             string
	EventHash             string
	TimestampMS           int64
	Timestamp             string
	Model                 string
	ResolvedModel         string
	Endpoint              string
	Method                string
	Path                  string
	AuthIndex             string
	Source                string
	SourceHash            string
	APIKeyHash            string
	AccountSnapshot       string
	AuthLabelSnapshot     string
	AuthFileSnapshot      string
	AuthProviderSnapshot  string
	AuthProjectIDSnapshot string
	ReasoningEffort       string
	ServiceTier           string
	ExecutorType          string
	InputTokens           int64
	OutputTokens          int64
	CachedTokens          int64
	CacheReadTokens       int64
	CacheCreationTokens   int64
	ReasoningTokens       int64
	TotalTokens           int64
	LatencyMS             sql.NullInt64
	TTFTMS                sql.NullInt64
	Failed                bool
	FailStatusCode        sql.NullInt64
	FailSummary           string
}

type EventsPage struct {
	Items        []EventPageItem
	NextBeforeMS int64
	NextBeforeID int64
	HasMore      bool
}

func (r *repository) AggregateWithFilter(ctx context.Context, filter AnalyticsFilter) (Aggregate, error) {
	where, args := analyticsWhere(filter)
	row := r.db.QueryRowContext(ctx, `select
	count(*) as calls,
	sum(case when failed = 0 then 1 else 0 end),
	sum(case when failed = 1 then 1 else 0 end),
	coalesce(sum(input_tokens), 0),
	coalesce(sum(output_tokens), 0),
	coalesce(sum(reasoning_tokens), 0),
	coalesce(sum(`+compatCachedExpr+`), 0),
	coalesce(sum(cache_read_tokens), 0),
	coalesce(sum(cache_creation_tokens), 0),
	coalesce(sum(total_tokens), 0),
	avg(nullif(latency_ms, 0)),
	coalesce(sum(case when total_tokens = 0 and failed = 0 then 1 else 0 end), 0)
from usage_events `+where, args...)

	var agg Aggregate
	var success, failure sql.NullInt64
	if err := row.Scan(
		&agg.TotalCalls,
		&success,
		&failure,
		&agg.InputTokens,
		&agg.OutputTokens,
		&agg.ReasoningTokens,
		&agg.CachedTokens,
		&agg.CacheReadTokens,
		&agg.CacheCreationTokens,
		&agg.TotalTokens,
		&agg.AvgLatencyMS,
		&agg.ZeroTokenCalls,
	); err != nil {
		return Aggregate{}, err
	}
	agg.SuccessCalls = success.Int64
	agg.FailureCalls = failure.Int64
	return agg, nil
}

func (r *repository) ModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter, limit int) ([]ModelStat, error) {
	where, args := analyticsWhere(filter)
	query := `select
	model,
	coalesce(nullif(resolved_model, ''), model) as billing_model,
	coalesce(service_tier, '') as service_tier,
	count(*) as calls,
	sum(case when failed = 0 then 1 else 0 end) as success,
	coalesce(sum(input_tokens), 0),
	coalesce(sum(output_tokens), 0),
	coalesce(sum(reasoning_tokens), 0),
	coalesce(sum(` + compatCachedExpr + `), 0),
	coalesce(sum(cache_read_tokens), 0),
	coalesce(sum(cache_creation_tokens), 0),
	coalesce(sum(total_tokens), 0)
from usage_events ` + where + `
group by model, billing_model, coalesce(service_tier, '')
order by calls desc`
	if limit > 0 {
		query = `with filtered as (
	select * from usage_events ` + where + `
),
top_models as (
	select model, count(*) as model_calls
	from filtered
	group by model
	order by model_calls desc
	limit ?
)
select
	f.model,
	coalesce(nullif(f.resolved_model, ''), f.model) as billing_model,
	coalesce(f.service_tier, '') as service_tier,
	count(*) as calls,
	sum(case when f.failed = 0 then 1 else 0 end) as success,
	coalesce(sum(f.input_tokens), 0),
	coalesce(sum(f.output_tokens), 0),
	coalesce(sum(f.reasoning_tokens), 0),
	coalesce(sum(` + compatCachedFExpr + `), 0),
	coalesce(sum(f.cache_read_tokens), 0),
	coalesce(sum(f.cache_creation_tokens), 0),
	coalesce(sum(f.total_tokens), 0)
from filtered f
join top_models t on t.model = f.model
group by f.model, billing_model, coalesce(f.service_tier, '')
order by max(t.model_calls) desc, f.model, calls desc`
		args = append(args, limit)
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make([]ModelStat, 0)
	for rows.Next() {
		var stat ModelStat
		if err := rows.Scan(
			&stat.Model,
			&stat.BillingModel,
			&stat.ServiceTier,
			&stat.Calls,
			&stat.SuccessCalls,
			&stat.InputTokens,
			&stat.OutputTokens,
			&stat.ReasoningTokens,
			&stat.CachedTokens,
			&stat.CacheReadTokens,
			&stat.CacheCreationTokens,
			&stat.TotalTokens,
		); err != nil {
			return nil, err
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}

func resolveBucketMS(timestampMS int64, granularity string, location *time.Location) int64 {
	if location == nil {
		location = time.UTC
	}
	tm := time.UnixMilli(timestampMS).In(location)
	if granularity == "day" {
		return time.Date(tm.Year(), tm.Month(), tm.Day(), 0, 0, 0, 0, location).UnixMilli()
	}
	return time.Date(tm.Year(), tm.Month(), tm.Day(), tm.Hour(), 0, 0, 0, location).UnixMilli()
}

func (r *repository) TimelineWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]TimelinePoint, error) {
	where, args := analyticsWhere(filter)
	query := fmt.Sprintf(`select
	timestamp_ms,
	model,
	coalesce(nullif(resolved_model, ''), model) as billing_model,
	coalesce(service_tier, '') as service_tier,
	failed,
	input_tokens,
	output_tokens,
	reasoning_tokens,
	`+compatCachedExpr+`,
	cache_read_tokens,
	cache_creation_tokens,
	total_tokens,
	latency_ms
from usage_events %s
order by timestamp_ms, model`, where)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct {
		bucketMS     int64
		model        string
		billingModel string
		serviceTier  string
	}
	grouped := map[key]*TimelinePoint{}
	order := make([]key, 0)
	for rows.Next() {
		var timestampMS int64
		var model string
		var billingModel string
		var serviceTier string
		var failed int
		var latency sql.NullFloat64
		var inputTokens int64
		var outputTokens int64
		var reasoningTokens int64
		var cachedTokens int64
		var cacheReadTokens int64
		var cacheCreationTokens int64
		var totalTokens int64
		if err := rows.Scan(
			&timestampMS,
			&model,
			&billingModel,
			&serviceTier,
			&failed,
			&inputTokens,
			&outputTokens,
			&reasoningTokens,
			&cachedTokens,
			&cacheReadTokens,
			&cacheCreationTokens,
			&totalTokens,
			&latency,
		); err != nil {
			return nil, err
		}
		mapKey := key{
			bucketMS:     resolveBucketMS(timestampMS, granularity, location),
			model:        model,
			billingModel: billingModel,
			serviceTier:  serviceTier,
		}
		point := grouped[mapKey]
		if point == nil {
			point = &TimelinePoint{
				BucketMS:     mapKey.bucketMS,
				Model:        model,
				BillingModel: billingModel,
				ServiceTier:  serviceTier,
			}
			grouped[mapKey] = point
			order = append(order, mapKey)
		}
		point.Calls += 1
		point.Tokens += totalTokens
		if failed != 0 {
			point.Failure += 1
		} else {
			point.Success += 1
		}
		point.InputTokens += inputTokens
		point.OutputTokens += outputTokens
		point.ReasoningTokens += reasoningTokens
		point.CachedTokens += cachedTokens
		point.CacheReadTokens += cacheReadTokens
		point.CacheCreationTokens += cacheCreationTokens
		if latency.Valid && latency.Float64 > 0 {
			point.AvgLatencyMS.Float64 += latency.Float64
			point.LatencySamples += 1
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	points := make([]TimelinePoint, 0, len(order))
	for _, mapKey := range order {
		point := grouped[mapKey]
		if point.LatencySamples > 0 {
			point.AvgLatencyMS.Float64 = point.AvgLatencyMS.Float64 / float64(point.LatencySamples)
			point.AvgLatencyMS.Valid = true
		}
		points = append(points, *point)
	}
	return points, nil
}

func (r *repository) LatencyPercentilesWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]LatencyPercentiles, error) {
	where, args := analyticsWhere(filter)
	query := fmt.Sprintf(`select
	timestamp_ms,
	latency_ms,
	ttft_ms
from usage_events %s
and (latency_ms > 0 or ttft_ms > 0)
order by timestamp_ms`, where)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type samples struct {
		latencies []float64
		ttfts     []float64
	}
	grouped := map[int64]*samples{}
	order := make([]int64, 0)
	for rows.Next() {
		var timestampMS int64
		var latency sql.NullFloat64
		var ttft sql.NullFloat64
		if err := rows.Scan(&timestampMS, &latency, &ttft); err != nil {
			return nil, err
		}
		bucketMS := resolveBucketMS(timestampMS, granularity, location)
		entry := grouped[bucketMS]
		if entry == nil {
			entry = &samples{}
			grouped[bucketMS] = entry
			order = append(order, bucketMS)
		}
		if latency.Valid && latency.Float64 > 0 {
			entry.latencies = append(entry.latencies, latency.Float64)
		}
		if ttft.Valid && ttft.Float64 > 0 {
			entry.ttfts = append(entry.ttfts, ttft.Float64)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]LatencyPercentiles, 0, len(order))
	for _, bucketMS := range order {
		entry := grouped[bucketMS]
		point := LatencyPercentiles{BucketMS: bucketMS}
		if value, ok := percentile95(entry.latencies); ok {
			point.P95LatencyMS = sql.NullFloat64{Float64: value, Valid: true}
		}
		if value, ok := percentile95(entry.ttfts); ok {
			point.P95TTFTMS = sql.NullFloat64{Float64: value, Valid: true}
		}
		result = append(result, point)
	}
	return result, nil
}

func (r *repository) LatencySummaryWithFilter(ctx context.Context, filter AnalyticsFilter) (LatencySummary, error) {
	where, args := analyticsWhere(filter)
	rows, err := r.db.QueryContext(ctx, `select latency_ms, ttft_ms
from usage_events `+where+`
and (latency_ms > 0 or ttft_ms > 0)`, args...)
	if err != nil {
		return LatencySummary{}, err
	}
	defer rows.Close()

	latencies := make([]float64, 0)
	ttfts := make([]float64, 0)
	for rows.Next() {
		var latency sql.NullFloat64
		var ttft sql.NullFloat64
		if err := rows.Scan(&latency, &ttft); err != nil {
			return LatencySummary{}, err
		}
		if latency.Valid && latency.Float64 > 0 {
			latencies = append(latencies, latency.Float64)
		}
		if ttft.Valid && ttft.Float64 > 0 {
			ttfts = append(ttfts, ttft.Float64)
		}
	}
	if err := rows.Err(); err != nil {
		return LatencySummary{}, err
	}

	var summary LatencySummary
	if value, ok := percentile95(latencies); ok {
		summary.P95LatencyMS = sql.NullFloat64{Float64: value, Valid: true}
	}
	if value, ok := percentile95(ttfts); ok {
		summary.P95TTFTMS = sql.NullFloat64{Float64: value, Valid: true}
	}
	return summary, nil
}

func percentile95(values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	sort.Float64s(values)
	index := int(float64(len(values))*0.95+0.999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index], true
}

func (r *repository) HourlyDistributionWithFilter(ctx context.Context, filter AnalyticsFilter, location *time.Location) ([]HourlyPoint, error) {
	if location == nil {
		location = time.UTC
	}
	where, args := analyticsWhere(filter)
	rows, err := r.db.QueryContext(ctx, `select timestamp_ms, total_tokens
from usage_events `+where+`
order by timestamp_ms`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pointsByHour := map[int]*HourlyPoint{}
	for rows.Next() {
		var timestampMS int64
		var totalTokens int64
		if err := rows.Scan(&timestampMS, &totalTokens); err != nil {
			return nil, err
		}
		hour := time.UnixMilli(timestampMS).In(location).Hour()
		point := pointsByHour[hour]
		if point == nil {
			point = &HourlyPoint{Hour: hour}
			pointsByHour[hour] = point
		}
		point.Calls += 1
		point.Tokens += totalTokens
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	hours := make([]int, 0, len(pointsByHour))
	for hour := range pointsByHour {
		hours = append(hours, hour)
	}
	sort.Ints(hours)
	points := make([]HourlyPoint, 0, len(hours))
	for _, hour := range hours {
		point := pointsByHour[hour]
		points = append(points, *point)
	}
	return points, nil
}

func (r *repository) FilterOptionValuesWithFilter(ctx context.Context, filter AnalyticsFilter) (FilterOptionValues, error) {
	providers, err := r.distinctFilterValues(ctx, filter, "coalesce(nullif(auth_provider_snapshot, ''), nullif(provider, ''), '')")
	if err != nil {
		return FilterOptionValues{}, err
	}
	authFiles, err := r.distinctFilterValues(ctx, filter, "coalesce(auth_file_snapshot, '')")
	if err != nil {
		return FilterOptionValues{}, err
	}
	projectIDs, err := r.distinctFilterValues(ctx, filter, "coalesce(auth_project_id_snapshot, '')")
	if err != nil {
		return FilterOptionValues{}, err
	}
	requestTypes, err := r.distinctFilterValues(ctx, filter, "coalesce(executor_type, '')")
	if err != nil {
		return FilterOptionValues{}, err
	}
	return FilterOptionValues{
		Providers:    providers,
		AuthFiles:    authFiles,
		ProjectIDs:   projectIDs,
		RequestTypes: requestTypes,
	}, nil
}

func (r *repository) distinctFilterValues(ctx context.Context, filter AnalyticsFilter, expression string) ([]string, error) {
	where, args := analyticsWhere(filter)
	rows, err := r.db.QueryContext(ctx, `select distinct `+expression+` as value
from usage_events `+where+`
and `+expression+` <> ''
order by value`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *repository) HeatmapWithFilter(ctx context.Context, filter AnalyticsFilter, location *time.Location) ([]HeatmapPoint, error) {
	where, args := analyticsWhere(filter)
	rows, err := r.db.QueryContext(ctx, `select
	timestamp_ms,
	model,
	coalesce(nullif(resolved_model, ''), model) as billing_model,
	coalesce(service_tier, '') as service_tier,
	coalesce(api_key_hash, ''),
	coalesce(nullif(auth_provider_snapshot, ''), provider, ''),
	failed,
	input_tokens,
	output_tokens,
	`+compatCachedExpr+`,
	cache_read_tokens,
	cache_creation_tokens,
	total_tokens
from usage_events `+where+`
order by timestamp_ms, model`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if location == nil {
		location = time.UTC
	}
	type key struct {
		weekday      int
		hour         int
		model        string
		billingModel string
		serviceTier  string
		apiKeyHash   string
		provider     string
	}
	grouped := map[key]*HeatmapPoint{}
	order := make([]key, 0)
	for rows.Next() {
		var timestampMS int64
		var model string
		var billingModel string
		var serviceTier string
		var apiKeyHash string
		var provider string
		var failed int
		var inputTokens int64
		var outputTokens int64
		var cachedTokens int64
		var cacheReadTokens int64
		var cacheCreationTokens int64
		var totalTokens int64
		if err := rows.Scan(
			&timestampMS,
			&model,
			&billingModel,
			&serviceTier,
			&apiKeyHash,
			&provider,
			&failed,
			&inputTokens,
			&outputTokens,
			&cachedTokens,
			&cacheReadTokens,
			&cacheCreationTokens,
			&totalTokens,
		); err != nil {
			return nil, err
		}
		tm := time.UnixMilli(timestampMS).In(location)
		mapKey := key{
			weekday:      int(tm.Weekday()),
			hour:         tm.Hour(),
			model:        model,
			billingModel: billingModel,
			serviceTier:  serviceTier,
			apiKeyHash:   apiKeyHash,
			provider:     provider,
		}
		point := grouped[mapKey]
		if point == nil {
			point = &HeatmapPoint{
				Weekday:      mapKey.weekday,
				Hour:         mapKey.hour,
				Model:        model,
				BillingModel: billingModel,
				ServiceTier:  serviceTier,
				APIKeyHash:   apiKeyHash,
				Provider:     provider,
			}
			grouped[mapKey] = point
			order = append(order, mapKey)
		}
		point.Calls += 1
		if failed != 0 {
			point.FailureCalls += 1
		} else {
			point.SuccessCalls += 1
		}
		point.InputTokens += inputTokens
		point.OutputTokens += outputTokens
		point.CachedTokens += cachedTokens
		point.CacheReadTokens += cacheReadTokens
		point.CacheCreationTokens += cacheCreationTokens
		point.TotalTokens += totalTokens
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	points := make([]HeatmapPoint, 0, len(order))
	for _, mapKey := range order {
		points = append(points, *grouped[mapKey])
	}
	return points, nil
}

func (r *repository) ChannelModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]ChannelModelStat, error) {
	where, args := analyticsWhere(filter)
	rows, err := r.db.QueryContext(ctx, `select
	coalesce(auth_index, ''),
	coalesce(max(source), ''),
	coalesce(max(account_snapshot), ''),
	coalesce(max(auth_label_snapshot), ''),
	coalesce(nullif(max(auth_provider_snapshot), ''), max(provider), ''),
	model,
	coalesce(nullif(resolved_model, ''), model) as billing_model,
	coalesce(service_tier, '') as service_tier,
	count(*),
	sum(case when failed = 0 then 1 else 0 end),
	sum(case when failed = 1 then 1 else 0 end),
	coalesce(sum(input_tokens), 0),
	coalesce(sum(output_tokens), 0),
	coalesce(sum(`+compatCachedExpr+`), 0),
	coalesce(sum(cache_read_tokens), 0),
	coalesce(sum(cache_creation_tokens), 0),
	coalesce(sum(total_tokens), 0),
	avg(nullif(latency_ms, 0)),
	count(nullif(latency_ms, 0))
from usage_events `+where+`
group by auth_index, model, billing_model, coalesce(service_tier, '')
order by count(*) desc`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make([]ChannelModelStat, 0)
	for rows.Next() {
		var stat ChannelModelStat
		if err := rows.Scan(
			&stat.AuthIndex,
			&stat.Source,
			&stat.AccountSnapshot,
			&stat.AuthLabelSnapshot,
			&stat.AuthProviderSnapshot,
			&stat.Model,
			&stat.BillingModel,
			&stat.ServiceTier,
			&stat.Calls,
			&stat.SuccessCalls,
			&stat.FailureCalls,
			&stat.InputTokens,
			&stat.OutputTokens,
			&stat.CachedTokens,
			&stat.CacheReadTokens,
			&stat.CacheCreationTokens,
			&stat.TotalTokens,
			&stat.AvgLatencyMS,
			&stat.LatencySamples,
		); err != nil {
			return nil, err
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}

func (r *repository) FailureSourcesWithFilter(ctx context.Context, filter AnalyticsFilter) ([]FailureSourceStat, error) {
	where, args := analyticsWhere(filter)
	rows, err := r.db.QueryContext(ctx, `select
	coalesce(max(source), ''),
	coalesce(source_hash, ''),
	coalesce(auth_index, ''),
	coalesce(max(account_snapshot), ''),
	coalesce(max(auth_label_snapshot), ''),
	coalesce(nullif(max(auth_provider_snapshot), ''), max(provider), ''),
	count(*),
	sum(case when failed = 1 then 1 else 0 end),
	max(timestamp_ms),
	avg(nullif(latency_ms, 0))
from usage_events `+where+`
group by source_hash, auth_index
having sum(case when failed = 1 then 1 else 0 end) > 0
order by sum(case when failed = 1 then 1 else 0 end) desc, max(timestamp_ms) desc`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make([]FailureSourceStat, 0)
	for rows.Next() {
		var stat FailureSourceStat
		if err := rows.Scan(
			&stat.Source,
			&stat.SourceHash,
			&stat.AuthIndex,
			&stat.AccountSnapshot,
			&stat.AuthLabelSnapshot,
			&stat.AuthProviderSnapshot,
			&stat.Calls,
			&stat.FailureCalls,
			&stat.LastSeenMS,
			&stat.AvgLatencyMS,
		); err != nil {
			return nil, err
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}

func (r *repository) AccountModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]AccountModelStat, error) {
	where, args := analyticsWhere(filter)
	rows, err := r.db.QueryContext(ctx, `select
	coalesce(account_snapshot, ''),
	coalesce(auth_label_snapshot, ''),
	coalesce(nullif(auth_provider_snapshot, ''), provider, ''),
	coalesce(auth_index, ''),
	coalesce(max(source), ''),
	coalesce(source_hash, ''),
	model,
	coalesce(nullif(resolved_model, ''), model) as billing_model,
	coalesce(service_tier, '') as service_tier,
	count(*),
	sum(case when failed = 0 then 1 else 0 end),
	sum(case when failed = 1 then 1 else 0 end),
	coalesce(sum(input_tokens), 0),
	coalesce(sum(output_tokens), 0),
	coalesce(sum(`+compatCachedExpr+`), 0),
	coalesce(sum(cache_read_tokens), 0),
	coalesce(sum(cache_creation_tokens), 0),
	coalesce(sum(total_tokens), 0),
	max(timestamp_ms),
	avg(nullif(latency_ms, 0)),
	count(nullif(latency_ms, 0))
from usage_events `+where+`
group by account_snapshot, auth_label_snapshot, coalesce(nullif(auth_provider_snapshot, ''), provider, ''), auth_index, source_hash, model, billing_model, coalesce(service_tier, '')
order by max(timestamp_ms) desc, count(*) desc`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make([]AccountModelStat, 0)
	for rows.Next() {
		var stat AccountModelStat
		if err := rows.Scan(
			&stat.AccountSnapshot,
			&stat.AuthLabelSnapshot,
			&stat.AuthProviderSnapshot,
			&stat.AuthIndex,
			&stat.Source,
			&stat.SourceHash,
			&stat.Model,
			&stat.BillingModel,
			&stat.ServiceTier,
			&stat.Calls,
			&stat.SuccessCalls,
			&stat.FailureCalls,
			&stat.InputTokens,
			&stat.OutputTokens,
			&stat.CachedTokens,
			&stat.CacheReadTokens,
			&stat.CacheCreationTokens,
			&stat.TotalTokens,
			&stat.LastSeenMS,
			&stat.AvgLatencyMS,
			&stat.LatencySamples,
		); err != nil {
			return nil, err
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}

func (r *repository) CredentialModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]CredentialModelStat, error) {
	where, args := analyticsWhere(filter)
	rows, err := r.db.QueryContext(ctx, `select
	coalesce(nullif(auth_file_snapshot, ''), nullif(auth_index, ''), nullif(source_hash, ''), nullif(source, ''), '-') as credential_id,
	coalesce(auth_file_snapshot, ''),
	coalesce(auth_index, ''),
	coalesce(max(source), ''),
	coalesce(source_hash, ''),
	coalesce(max(account_snapshot), ''),
	coalesce(max(auth_label_snapshot), ''),
	coalesce(nullif(max(auth_provider_snapshot), ''), max(provider), ''),
	coalesce(max(auth_project_id_snapshot), ''),
	model,
	coalesce(nullif(resolved_model, ''), model) as billing_model,
	coalesce(service_tier, '') as service_tier,
	count(*),
	sum(case when failed = 0 then 1 else 0 end),
	sum(case when failed = 1 then 1 else 0 end),
	coalesce(sum(input_tokens), 0),
	coalesce(sum(output_tokens), 0),
	coalesce(sum(`+compatCachedExpr+`), 0),
	coalesce(sum(cache_read_tokens), 0),
	coalesce(sum(cache_creation_tokens), 0),
	coalesce(sum(total_tokens), 0),
	max(timestamp_ms),
	avg(nullif(latency_ms, 0)),
	count(nullif(latency_ms, 0))
from usage_events `+where+`
group by credential_id, auth_file_snapshot, auth_index, source_hash, model, billing_model, coalesce(service_tier, '')
order by max(timestamp_ms) desc, count(*) desc`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make([]CredentialModelStat, 0)
	for rows.Next() {
		var stat CredentialModelStat
		if err := rows.Scan(
			&stat.ID,
			&stat.AuthFileSnapshot,
			&stat.AuthIndex,
			&stat.Source,
			&stat.SourceHash,
			&stat.AccountSnapshot,
			&stat.AuthLabelSnapshot,
			&stat.AuthProviderSnapshot,
			&stat.AuthProjectIDSnapshot,
			&stat.Model,
			&stat.BillingModel,
			&stat.ServiceTier,
			&stat.Calls,
			&stat.SuccessCalls,
			&stat.FailureCalls,
			&stat.InputTokens,
			&stat.OutputTokens,
			&stat.CachedTokens,
			&stat.CacheReadTokens,
			&stat.CacheCreationTokens,
			&stat.TotalTokens,
			&stat.LastSeenMS,
			&stat.AvgLatencyMS,
			&stat.LatencySamples,
		); err != nil {
			return nil, err
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}

func (r *repository) APIKeyModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]APIKeyModelStat, error) {
	where, args := analyticsWhere(filter)
	rows, err := r.db.QueryContext(ctx, `select
	coalesce(api_key_hash, ''),
	coalesce(account_snapshot, ''),
	coalesce(auth_label_snapshot, ''),
	coalesce(nullif(auth_provider_snapshot, ''), provider, ''),
	coalesce(auth_index, ''),
	coalesce(max(source), ''),
	coalesce(source_hash, ''),
	model,
	coalesce(nullif(resolved_model, ''), model) as billing_model,
	coalesce(service_tier, '') as service_tier,
	count(*),
	sum(case when failed = 0 then 1 else 0 end),
	sum(case when failed = 1 then 1 else 0 end),
	coalesce(sum(input_tokens), 0),
	coalesce(sum(output_tokens), 0),
	coalesce(sum(`+compatCachedExpr+`), 0),
	coalesce(sum(cache_read_tokens), 0),
	coalesce(sum(cache_creation_tokens), 0),
	coalesce(sum(total_tokens), 0),
	max(timestamp_ms),
	avg(nullif(latency_ms, 0)),
	count(nullif(latency_ms, 0))
from usage_events `+where+`
group by api_key_hash, account_snapshot, auth_label_snapshot, coalesce(nullif(auth_provider_snapshot, ''), provider, ''), auth_index, source_hash, model, billing_model, coalesce(service_tier, '')
order by max(timestamp_ms) desc, count(*) desc`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make([]APIKeyModelStat, 0)
	for rows.Next() {
		var stat APIKeyModelStat
		if err := rows.Scan(
			&stat.APIKeyHash,
			&stat.AccountSnapshot,
			&stat.AuthLabelSnapshot,
			&stat.AuthProviderSnapshot,
			&stat.AuthIndex,
			&stat.Source,
			&stat.SourceHash,
			&stat.Model,
			&stat.BillingModel,
			&stat.ServiceTier,
			&stat.Calls,
			&stat.SuccessCalls,
			&stat.FailureCalls,
			&stat.InputTokens,
			&stat.OutputTokens,
			&stat.CachedTokens,
			&stat.CacheReadTokens,
			&stat.CacheCreationTokens,
			&stat.TotalTokens,
			&stat.LastSeenMS,
			&stat.AvgLatencyMS,
			&stat.LatencySamples,
		); err != nil {
			return nil, err
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}

func (r *repository) TaskBucketsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]TaskBucket, error) {
	where, args := analyticsWhere(filter)
	rows, err := r.db.QueryContext(ctx, `select
	coalesce(timestamp, '') || '|' || coalesce(source_hash, '') || '|' || coalesce(auth_index, '') as bucket_key,
	count(*),
	sum(case when failed = 0 then 1 else 0 end),
	sum(case when failed = 1 then 1 else 0 end),
	min(timestamp_ms),
	max(timestamp_ms),
	coalesce(max(source), ''),
	coalesce(source_hash, ''),
	coalesce(auth_index, ''),
	coalesce(group_concat(distinct model), ''),
	coalesce(group_concat(distinct endpoint), ''),
	coalesce(sum(input_tokens), 0),
	coalesce(sum(output_tokens), 0),
	coalesce(sum(`+compatCachedExpr+`), 0),
	coalesce(sum(cache_read_tokens), 0),
	coalesce(sum(cache_creation_tokens), 0),
	coalesce(sum(total_tokens), 0),
	avg(nullif(latency_ms, 0)),
	max(latency_ms)
from usage_events `+where+`
group by bucket_key, source_hash, auth_index
order by max(timestamp_ms) desc
limit 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buckets := make([]TaskBucket, 0)
	for rows.Next() {
		var bucket TaskBucket
		if err := rows.Scan(
			&bucket.BucketKey,
			&bucket.Total,
			&bucket.Success,
			&bucket.Failure,
			&bucket.FirstMS,
			&bucket.LastMS,
			&bucket.Source,
			&bucket.SourceHash,
			&bucket.AuthIndex,
			&bucket.Models,
			&bucket.Endpoints,
			&bucket.InputTokens,
			&bucket.OutputTokens,
			&bucket.CachedTokens,
			&bucket.CacheReadTokens,
			&bucket.CacheCreationTokens,
			&bucket.TotalTokens,
			&bucket.AvgLatencyMS,
			&bucket.MaxLatencyMS,
		); err != nil {
			return nil, err
		}
		buckets = append(buckets, bucket)
	}
	return buckets, rows.Err()
}

func (r *repository) RecentFailuresWithFilter(ctx context.Context, filter AnalyticsFilter, limit int) ([]RecentFailure, error) {
	if limit <= 0 {
		return nil, nil
	}
	filter.IncludeFailed = true
	where, args := analyticsWhere(filter)
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, `select
	timestamp_ms,
	model,
	coalesce(api_key_hash, ''),
	coalesce(source, ''),
	coalesce(source_hash, ''),
	coalesce(auth_index, ''),
	coalesce(endpoint, ''),
	latency_ms,
	coalesce(account_snapshot, ''),
	coalesce(auth_label_snapshot, ''),
	coalesce(nullif(auth_provider_snapshot, ''), provider, ''),
	coalesce(auth_project_id_snapshot, ''),
	fail_status_code,
	coalesce(fail_summary, '')
from usage_events `+where+`
and failed = 1
order by timestamp_ms desc, id desc
limit ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	failures := make([]RecentFailure, 0, limit)
	for rows.Next() {
		var failure RecentFailure
		if err := rows.Scan(
			&failure.TimestampMS,
			&failure.Model,
			&failure.APIKeyHash,
			&failure.Source,
			&failure.SourceHash,
			&failure.AuthIndex,
			&failure.Endpoint,
			&failure.LatencyMS,
			&failure.AccountSnapshot,
			&failure.AuthLabelSnapshot,
			&failure.AuthProviderSnapshot,
			&failure.AuthProjectIDSnapshot,
			&failure.FailStatusCode,
			&failure.FailSummary,
		); err != nil {
			return nil, err
		}
		failures = append(failures, failure)
	}
	return failures, rows.Err()
}

func (r *repository) EventsCountWithFilter(ctx context.Context, filter AnalyticsFilter) (int64, error) {
	where, args := analyticsWhere(filter)
	var total int64
	if err := r.db.QueryRowContext(ctx, `select count(*) from usage_events `+where, args...).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (r *repository) EventsPageWithFilter(ctx context.Context, filter AnalyticsFilter, beforeMS int64, beforeID int64, limit int) (EventsPage, error) {
	if limit <= 0 {
		return EventsPage{}, nil
	}
	queryLimit := limit + 1
	where, args := analyticsWhere(filter)
	// Keyset pagination cursor. The non-unique timestamp index implicitly
	// carries the rowid (id is "integer primary key"), so ordering by
	// (timestamp_ms desc, id desc) stays index-backed. Using the compound
	// (timestamp_ms, id) cursor instead of only timestamp_ms guarantees that
	// many rows sharing one timestamp_ms are never skipped across pages.
	// beforeID <= 0 falls back to the legacy timestamp-only cursor for old
	// clients that do not send before_id yet.
	if beforeMS > 0 {
		if beforeID > 0 {
			where += " and (timestamp_ms < ? or (timestamp_ms = ? and id < ?))"
			args = append(args, beforeMS, beforeMS, beforeID)
		} else {
			where += " and timestamp_ms < ?"
			args = append(args, beforeMS)
		}
	}
	args = append(args, queryLimit)
	rows, err := r.db.QueryContext(ctx, `select
	id,
	coalesce(request_id, ''),
	event_hash,
	timestamp_ms,
	timestamp,
	model,
	coalesce(resolved_model, ''),
	coalesce(endpoint, ''),
	coalesce(method, ''),
	coalesce(path, ''),
	coalesce(auth_index, ''),
	coalesce(source, ''),
	coalesce(source_hash, ''),
	coalesce(api_key_hash, ''),
	coalesce(account_snapshot, ''),
	coalesce(auth_label_snapshot, ''),
	coalesce(auth_file_snapshot, ''),
	coalesce(nullif(auth_provider_snapshot, ''), provider, ''),
	coalesce(auth_project_id_snapshot, ''),
	coalesce(reasoning_effort, ''),
	coalesce(service_tier, ''),
	coalesce(executor_type, ''),
	input_tokens,
	output_tokens,
	`+compatCachedExpr+`,
	cache_read_tokens,
	cache_creation_tokens,
	reasoning_tokens,
	total_tokens,
	latency_ms,
	ttft_ms,
	failed,
	fail_status_code,
	coalesce(fail_summary, '')
from usage_events `+where+`
order by timestamp_ms desc, id desc
limit ?`, args...)
	if err != nil {
		return EventsPage{}, err
	}
	defer rows.Close()

	items := make([]EventPageItem, 0, limit)
	for rows.Next() {
		var item EventPageItem
		var failed int
		if err := rows.Scan(
			&item.ID,
			&item.RequestID,
			&item.EventHash,
			&item.TimestampMS,
			&item.Timestamp,
			&item.Model,
			&item.ResolvedModel,
			&item.Endpoint,
			&item.Method,
			&item.Path,
			&item.AuthIndex,
			&item.Source,
			&item.SourceHash,
			&item.APIKeyHash,
			&item.AccountSnapshot,
			&item.AuthLabelSnapshot,
			&item.AuthFileSnapshot,
			&item.AuthProviderSnapshot,
			&item.AuthProjectIDSnapshot,
			&item.ReasoningEffort,
			&item.ServiceTier,
			&item.ExecutorType,
			&item.InputTokens,
			&item.OutputTokens,
			&item.CachedTokens,
			&item.CacheReadTokens,
			&item.CacheCreationTokens,
			&item.ReasoningTokens,
			&item.TotalTokens,
			&item.LatencyMS,
			&item.TTFTMS,
			&failed,
			&item.FailStatusCode,
			&item.FailSummary,
		); err != nil {
			return EventsPage{}, err
		}
		item.Failed = failed != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return EventsPage{}, err
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	nextBeforeMS := int64(0)
	nextBeforeID := int64(0)
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		nextBeforeMS = last.TimestampMS
		nextBeforeID = last.ID
	}
	return EventsPage{Items: items, NextBeforeMS: nextBeforeMS, NextBeforeID: nextBeforeID, HasMore: hasMore}, nil
}

func (r *repository) ActiveDaysWithFilter(ctx context.Context, filter AnalyticsFilter, location *time.Location) (int64, error) {
	if location == nil {
		location = time.UTC
	}
	where, args := analyticsWhere(filter)
	rows, err := r.db.QueryContext(ctx, `select timestamp_ms from usage_events `+where, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	activeDays := map[string]struct{}{}
	for rows.Next() {
		var timestampMS int64
		if err := rows.Scan(&timestampMS); err != nil {
			return 0, err
		}
		activeDays[time.UnixMilli(timestampMS).In(location).Format("2006-01-02")] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return int64(len(activeDays)), nil
}

func (r *repository) ZeroTokenModelsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]string, error) {
	where, args := analyticsWhere(filter)
	rows, err := r.db.QueryContext(ctx, `select distinct coalesce(model, '')
from usage_events `+where+`
and total_tokens = 0
and failed = 0
order by model`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	models := make([]string, 0)
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, err
		}
		if strings.TrimSpace(model) == "" {
			continue
		}
		models = append(models, model)
	}
	return models, rows.Err()
}

func analyticsWhere(filter AnalyticsFilter) (string, []any) {
	conditions := []string{"timestamp_ms >= ?", "timestamp_ms < ?"}
	args := []any{filter.FromMS, filter.ToMS}

	query := strings.TrimSpace(strings.ToLower(filter.SearchQuery))
	hash := strings.TrimSpace(strings.ToLower(filter.SearchAPIKeyHash))
	if query != "" {
		like := "%" + query + "%"
		searchConditions := make([]string, 0, len(analyticsSearchTextColumns)+1)
		for _, column := range analyticsSearchTextColumns {
			searchConditions = append(searchConditions, fmt.Sprintf("lower(coalesce(%s, '')) like ?", column))
			args = append(args, like)
		}
		if hash != "" {
			searchConditions = append(searchConditions, "lower(coalesce(api_key_hash, '')) = ?")
			args = append(args, hash)
		}
		conditions = append(conditions, "("+strings.Join(searchConditions, " or ")+")")
	} else if hash != "" {
		conditions = append(conditions, "lower(coalesce(api_key_hash, '')) = ?")
		args = append(args, hash)
	}
	addInCondition := func(column string, values []string) {
		normalized := normalizeFilterValues(values)
		if len(normalized) == 0 {
			return
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(normalized)), ",")
		conditions = append(conditions, fmt.Sprintf("coalesce(%s, '') in (%s)", column, placeholders))
		for _, value := range normalized {
			args = append(args, value)
		}
	}
	addInCondition("model", filter.Models)
	addProviderCondition(filter.Providers, &conditions, &args)
	addAccountCondition(filter.Accounts, &conditions, &args)
	addInCondition("auth_file_snapshot", filter.AuthFiles)
	addInCondition("auth_index", filter.AuthIndices)
	addInCondition("api_key_hash", filter.APIKeyHashes)
	addInCondition("source_hash", filter.SourceHashes)
	addInCondition("auth_project_id_snapshot", filter.ProjectIDs)
	addInCondition("executor_type", filter.RequestTypes)
	if !filter.IncludeFailed {
		conditions = append(conditions, "failed = 0")
	}
	if filter.FailedOnly {
		conditions = append(conditions, "failed = 1")
	}
	if filter.MinLatencyMS > 0 {
		conditions = append(conditions, "latency_ms >= ?")
		args = append(args, filter.MinLatencyMS)
	}
	cacheHitCondition := strings.Join([]string{
		"(coalesce(cached_tokens, 0) > 0",
		"or coalesce(cache_tokens, 0) > 0",
		"or coalesce(cache_read_tokens, 0) > 0",
		"or coalesce(cache_creation_tokens, 0) > 0)",
	}, " ")
	switch strings.TrimSpace(strings.ToLower(filter.CacheStatus)) {
	case "hit":
		conditions = append(conditions, cacheHitCondition)
	case "miss":
		conditions = append(conditions, "not "+cacheHitCondition)
	case "read":
		conditions = append(conditions, "coalesce(cache_read_tokens, 0) > 0")
	case "creation":
		conditions = append(conditions, "coalesce(cache_creation_tokens, 0) > 0")
	}

	return "where " + strings.Join(conditions, " and "), args
}

func addProviderCondition(values []string, conditions *[]string, args *[]any) {
	normalized := normalizeLowerFilterValues(values)
	if len(normalized) == 0 {
		return
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(normalized)), ",")
	providerConditions := []string{
		fmt.Sprintf("lower(coalesce(provider, '')) in (%s)", placeholders),
		fmt.Sprintf("lower(coalesce(auth_provider_snapshot, '')) in (%s)", placeholders),
	}
	*conditions = append(*conditions, "("+strings.Join(providerConditions, " or ")+")")
	for range providerConditions {
		for _, value := range normalized {
			*args = append(*args, value)
		}
	}
}

func addAccountCondition(values []string, conditions *[]string, args *[]any) {
	normalized := normalizeLowerFilterValues(values)
	if len(normalized) == 0 {
		return
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(normalized)), ",")
	accountConditions := []string{
		fmt.Sprintf("lower(coalesce(account_snapshot, '')) in (%s)", placeholders),
		fmt.Sprintf("lower(coalesce(auth_label_snapshot, '')) in (%s)", placeholders),
		fmt.Sprintf("lower(coalesce(source, '')) in (%s)", placeholders),
		fmt.Sprintf("lower(coalesce(auth_index, '')) in (%s)", placeholders),
	}
	*conditions = append(*conditions, "("+strings.Join(accountConditions, " or ")+")")
	for range accountConditions {
		for _, value := range normalized {
			*args = append(*args, value)
		}
	}
}

func normalizeFilterValues(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func normalizeLowerFilterValues(values []string) []string {
	normalized := normalizeFilterValues(values)
	for index, value := range normalized {
		normalized[index] = strings.ToLower(value)
	}
	return normalized
}
