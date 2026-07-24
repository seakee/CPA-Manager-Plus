package usageevent

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

var (
	longContextThresholdSQL = strconv.FormatInt(usage.LongContextInputTokenThreshold, 10)
	compatCachedExpr        = "max(max(cached_tokens, cache_tokens) - max(cache_read_tokens, 0) - max(cache_creation_tokens, 0), 0)"
	compatCachedFExpr       = "max(max(f.cached_tokens, f.cache_tokens) - max(f.cache_read_tokens, 0) - max(f.cache_creation_tokens, 0), 0)"
	normalizedInputExpr     = "coalesce(normalized_total_input_tokens, input_tokens)"
	normalizedInputFExpr    = "coalesce(f.normalized_total_input_tokens, f.input_tokens)"
	longInputExpr           = "case when " + normalizedInputExpr + " > " + longContextThresholdSQL + " then " + normalizedInputExpr + " else 0 end"
	longOutputExpr          = "case when " + normalizedInputExpr + " > " + longContextThresholdSQL + " then output_tokens else 0 end"
	longCachedExpr          = "case when " + normalizedInputExpr + " > " + longContextThresholdSQL + " then " + compatCachedExpr + " else 0 end"
	longCacheReadExpr       = "case when " + normalizedInputExpr + " > " + longContextThresholdSQL + " then cache_read_tokens else 0 end"
	longCacheCreationExpr   = "case when " + normalizedInputExpr + " > " + longContextThresholdSQL + " then cache_creation_tokens else 0 end"
	longInputFExpr          = "case when " + normalizedInputFExpr + " > " + longContextThresholdSQL + " then " + normalizedInputFExpr + " else 0 end"
	longOutputFExpr         = "case when " + normalizedInputFExpr + " > " + longContextThresholdSQL + " then f.output_tokens else 0 end"
	longCachedFExpr         = "case when " + normalizedInputFExpr + " > " + longContextThresholdSQL + " then " + compatCachedFExpr + " else 0 end"
	longCacheReadFExpr      = "case when " + normalizedInputFExpr + " > " + longContextThresholdSQL + " then f.cache_read_tokens else 0 end"
	longCacheCreationFExpr  = "case when " + normalizedInputFExpr + " > " + longContextThresholdSQL + " then f.cache_creation_tokens else 0 end"
	credentialIDExpr        = "coalesce(nullif(auth_file_snapshot, ''), nullif(auth_index, ''), nullif(source_hash, ''), nullif(source, ''), '-')"
)

type AnalyticsFilter struct {
	FromMS           int64
	ToMS             int64
	SearchQuery      string
	SearchAPIKeyHash string
	Models           []string
	Providers        []string
	Accounts         []string
	CredentialIDs    []string
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
	HeaderErrorKinds []string
	HeaderErrorCodes []string
	HeaderQuotaPlans []string
	HeaderTraceIDs   []string
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
	"header_quota_plan_type",
	"header_error_kind",
	"header_error_code",
	"header_trace_id",
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
	Providers        []string
	AuthFiles        []string
	ProjectIDs       []string
	RequestTypes     []string
	HeaderErrorKinds []string
	HeaderErrorCodes []string
	HeaderQuotaPlans []string
	HeaderTraceIDs   []string
}

type FilterSelectorValues struct {
	Models           []string
	APIKeyHashes     []string
	Providers        []string
	AuthFiles        []string
	Accounts         []string
	AccountSelectors []AccountSelectorValue
	APIKeySelectors  []APIKeySelectorValue
}

type AccountSelectorValue struct {
	AccountSnapshot      string
	AuthLabelSnapshot    string
	AuthProviderSnapshot string
	AuthIndex            string
	Source               string
	SourceHash           string
}

type APIKeySelectorValue struct {
	APIKeyHash           string
	AuthProviderSnapshot string
	AuthIndex            string
	Source               string
	SourceHash           string
}

type TimelinePoint struct {
	usage.LongContextTokens
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
	usage.LongContextTokens
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
	usage.LongContextTokens
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
	usage.LongContextTokens
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
	usage.LongContextTokens
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

type CredentialTimelinePoint struct {
	usage.LongContextTokens
	ID                    string
	AuthFileSnapshot      string
	AuthIndex             string
	Source                string
	SourceHash            string
	AccountSnapshot       string
	AuthLabelSnapshot     string
	AuthProviderSnapshot  string
	AuthProjectIDSnapshot string
	BucketMS              int64
	Model                 string
	BillingModel          string
	ServiceTier           string
	Calls                 int64
	Tokens                int64
	Success               int64
	Failure               int64
	InputTokens           int64
	OutputTokens          int64
	ReasoningTokens       int64
	CachedTokens          int64
	CacheReadTokens       int64
	CacheCreationTokens   int64
	AvgLatencyMS          sql.NullFloat64
	LatencySamples        int64
}

type APIKeyTimelinePoint struct {
	usage.LongContextTokens
	APIKeyHash          string
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

type APIKeyModelStat struct {
	usage.LongContextTokens
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
	ID                     int64
	RequestID              string
	EventHash              string
	TimestampMS            int64
	Timestamp              string
	Model                  string
	ResolvedModel          string
	Endpoint               string
	Method                 string
	Path                   string
	AuthIndex              string
	Source                 string
	SourceHash             string
	APIKeyHash             string
	AccountSnapshot        string
	AuthLabelSnapshot      string
	AuthFileSnapshot       string
	AuthProviderSnapshot   string
	AuthProjectIDSnapshot  string
	ReasoningEffort        string
	ServiceTier            string
	ExecutorType           string
	InputTokens            int64
	OutputTokens           int64
	CachedTokens           int64
	CacheReadTokens        int64
	CacheCreationTokens    int64
	ReasoningTokens        int64
	TotalTokens            int64
	LatencyMS              sql.NullInt64
	TTFTMS                 sql.NullInt64
	Failed                 bool
	FailStatusCode         sql.NullInt64
	FailSummary            string
	ResponseMetadata       *usage.ResponseHeaderMetadata
	HeaderQuotaRecoverAtMS sql.NullInt64
	HeaderQuotaUsedPercent sql.NullFloat64
	HeaderQuotaPlanType    string
	HeaderErrorKind        string
	HeaderErrorCode        string
	HeaderTraceID          string
}

type EventsPage struct {
	Items        []EventPageItem
	NextBeforeMS int64
	NextBeforeID int64
	HasMore      bool
}

type HeaderSnapshot struct {
	ID                     int64
	EventHash              string
	TimestampMS            int64
	AuthFileSnapshot       string
	AuthIndex              string
	AccountSnapshot        string
	AuthLabelSnapshot      string
	AuthProviderSnapshot   string
	AuthProjectIDSnapshot  string
	Source                 string
	SourceHash             string
	ResponseMetadata       *usage.ResponseHeaderMetadata
	HeaderQuotaRecoverAtMS sql.NullInt64
	HeaderQuotaUsedPercent sql.NullFloat64
	HeaderQuotaPlanType    string
	HeaderErrorKind        string
	HeaderErrorCode        string
	HeaderTraceID          string
}

func (r *repository) AggregateWithFilter(ctx context.Context, filter AnalyticsFilter) (Aggregate, error) {
	where, args := analyticsWhere(filter)
	row := r.db.QueryRowContext(ctx, `select
	count(*) as calls,
	sum(case when failed = 0 then 1 else 0 end),
	sum(case when failed = 1 then 1 else 0 end),
	coalesce(sum(`+normalizedInputExpr+`), 0),
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
	coalesce(sum(` + normalizedInputExpr + `), 0),
	coalesce(sum(output_tokens), 0),
	coalesce(sum(reasoning_tokens), 0),
	coalesce(sum(` + compatCachedExpr + `), 0),
	coalesce(sum(cache_read_tokens), 0),
	coalesce(sum(cache_creation_tokens), 0),
	coalesce(sum(` + longInputExpr + `), 0),
	coalesce(sum(` + longOutputExpr + `), 0),
	coalesce(sum(` + longCachedExpr + `), 0),
	coalesce(sum(` + longCacheReadExpr + `), 0),
	coalesce(sum(` + longCacheCreationExpr + `), 0),
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
	coalesce(sum(` + longInputFExpr + `), 0),
	coalesce(sum(` + longOutputFExpr + `), 0),
	coalesce(sum(` + longCachedFExpr + `), 0),
	coalesce(sum(` + longCacheReadFExpr + `), 0),
	coalesce(sum(` + longCacheCreationFExpr + `), 0),
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
			&stat.LongInputTokens,
			&stat.LongOutputTokens,
			&stat.LongCachedTokens,
			&stat.LongCacheReadTokens,
			&stat.LongCacheCreationTokens,
			&stat.TotalTokens,
		); err != nil {
			return nil, err
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}

func (r *repository) TimelineWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]TimelinePoint, error) {
	where, args := analyticsWhere(filter)
	query := fmt.Sprintf(`select
	timestamp_ms,
	model,
	coalesce(nullif(resolved_model, ''), model) as billing_model,
		coalesce(service_tier, '') as service_tier,
		failed,
		`+normalizedInputExpr+`,
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
			bucketMS:     usage.AnalyticsBucketMS(timestampMS, granularity, location),
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
		point.AddIfLongContext(inputTokens, outputTokens, cachedTokens, cacheReadTokens, cacheCreationTokens)
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

func (r *repository) APIKeyTimelineWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]APIKeyTimelinePoint, error) {
	if len(normalizeFilterValues(filter.APIKeyHashes)) == 0 && strings.TrimSpace(filter.SearchAPIKeyHash) == "" {
		return nil, nil
	}
	where, args := analyticsWhere(filter)
	query := fmt.Sprintf(`select
	timestamp_ms,
	coalesce(api_key_hash, ''),
	model,
	coalesce(nullif(resolved_model, ''), model) as billing_model,
	coalesce(service_tier, '') as service_tier,
	failed,
	`+normalizedInputExpr+`,
	output_tokens,
	reasoning_tokens,
	`+compatCachedExpr+`,
	cache_read_tokens,
	cache_creation_tokens,
	total_tokens,
	latency_ms
from usage_events %s
order by timestamp_ms, api_key_hash, model`, where)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct {
		apiKeyHash   string
		bucketMS     int64
		model        string
		billingModel string
		serviceTier  string
	}
	grouped := map[key]*APIKeyTimelinePoint{}
	order := make([]key, 0)
	for rows.Next() {
		var timestampMS int64
		var point APIKeyTimelinePoint
		var failed int
		var latency sql.NullFloat64
		var totalTokens int64
		if err := rows.Scan(
			&timestampMS,
			&point.APIKeyHash,
			&point.Model,
			&point.BillingModel,
			&point.ServiceTier,
			&failed,
			&point.InputTokens,
			&point.OutputTokens,
			&point.ReasoningTokens,
			&point.CachedTokens,
			&point.CacheReadTokens,
			&point.CacheCreationTokens,
			&totalTokens,
			&latency,
		); err != nil {
			return nil, err
		}
		mapKey := key{
			apiKeyHash:   point.APIKeyHash,
			bucketMS:     usage.AnalyticsBucketMS(timestampMS, granularity, location),
			model:        point.Model,
			billingModel: point.BillingModel,
			serviceTier:  point.ServiceTier,
		}
		entry := grouped[mapKey]
		if entry == nil {
			entry = &APIKeyTimelinePoint{
				APIKeyHash:   point.APIKeyHash,
				BucketMS:     mapKey.bucketMS,
				Model:        point.Model,
				BillingModel: point.BillingModel,
				ServiceTier:  point.ServiceTier,
			}
			grouped[mapKey] = entry
			order = append(order, mapKey)
		}
		entry.Calls += 1
		entry.Tokens += totalTokens
		if failed != 0 {
			entry.Failure += 1
		} else {
			entry.Success += 1
		}
		entry.InputTokens += point.InputTokens
		entry.OutputTokens += point.OutputTokens
		entry.ReasoningTokens += point.ReasoningTokens
		entry.CachedTokens += point.CachedTokens
		entry.CacheReadTokens += point.CacheReadTokens
		entry.CacheCreationTokens += point.CacheCreationTokens
		entry.AddIfLongContext(point.InputTokens, point.OutputTokens, point.CachedTokens, point.CacheReadTokens, point.CacheCreationTokens)
		if latency.Valid && latency.Float64 > 0 {
			entry.AvgLatencyMS.Float64 += latency.Float64
			entry.LatencySamples += 1
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	points := make([]APIKeyTimelinePoint, 0, len(order))
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
	coalesce(latency_ms, 0),
	coalesce(ttft_ms, 0)
from usage_events %s
and (latency_ms > 0 or ttft_ms > 0)
order by timestamp_ms`, where)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]LatencyPercentiles, 0)
	var currentBucketMS int64
	hasCurrentBucket := false
	latencies := make([]float64, 0)
	ttfts := make([]float64, 0)
	flushBucket := func() {
		if !hasCurrentBucket {
			return
		}
		point := LatencyPercentiles{BucketMS: currentBucketMS}
		if value, ok := percentile95(latencies); ok {
			point.P95LatencyMS = sql.NullFloat64{Float64: value, Valid: true}
		}
		if value, ok := percentile95(ttfts); ok {
			point.P95TTFTMS = sql.NullFloat64{Float64: value, Valid: true}
		}
		result = append(result, point)
		latencies = latencies[:0]
		ttfts = ttfts[:0]
	}
	for rows.Next() {
		var timestampMS int64
		var latencyMS int64
		var ttftMS int64
		if err := rows.Scan(&timestampMS, &latencyMS, &ttftMS); err != nil {
			return nil, err
		}
		bucketMS := usage.AnalyticsBucketMS(timestampMS, granularity, location)
		if !hasCurrentBucket || bucketMS != currentBucketMS {
			flushBucket()
			currentBucketMS = bucketMS
			hasCurrentBucket = true
		}
		if latencyMS > 0 {
			latencies = append(latencies, float64(latencyMS))
		}
		if ttftMS > 0 {
			ttfts = append(ttfts, float64(ttftMS))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	flushBucket()
	return result, nil
}

func (r *repository) LatencySummaryWithFilter(ctx context.Context, filter AnalyticsFilter) (LatencySummary, error) {
	where, args := analyticsWhere(filter)
	query := fmt.Sprintf(`with samples(kind, value) as (
	select 'latency', latency_ms from usage_events %s and latency_ms > 0
	union all
	select 'ttft', ttft_ms from usage_events %s and ttft_ms > 0
), ranked as (
	select
		kind,
		value,
		row_number() over (partition by kind order by value) as sample_number,
		count(*) over (partition by kind) as sample_count
	from samples
)
select kind, value
from ranked
where sample_number = ((sample_count * 95) + 99) / 100`, where, where)
	queryArgs := make([]any, 0, len(args)*2)
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, args...)
	rows, err := r.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return LatencySummary{}, err
	}
	defer rows.Close()

	var summary LatencySummary
	for rows.Next() {
		var kind string
		var value float64
		if err := rows.Scan(&kind, &value); err != nil {
			return LatencySummary{}, err
		}
		switch kind {
		case "latency":
			summary.P95LatencyMS = sql.NullFloat64{Float64: value, Valid: true}
		case "ttft":
			summary.P95TTFTMS = sql.NullFloat64{Float64: value, Valid: true}
		}
	}
	if err := rows.Err(); err != nil {
		return LatencySummary{}, err
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
	headerErrorKinds, err := r.distinctFilterValues(ctx, filter, "coalesce(header_error_kind, '')")
	if err != nil {
		return FilterOptionValues{}, err
	}
	headerErrorCodes, err := r.distinctFilterValues(ctx, filter, "coalesce(header_error_code, '')")
	if err != nil {
		return FilterOptionValues{}, err
	}
	headerQuotaPlans, err := r.distinctFilterValues(ctx, filter, "coalesce(header_quota_plan_type, '')")
	if err != nil {
		return FilterOptionValues{}, err
	}
	headerTraceIDs, err := r.distinctFilterValues(ctx, filter, "coalesce(header_trace_id, '')")
	if err != nil {
		return FilterOptionValues{}, err
	}
	return FilterOptionValues{
		Providers:        providers,
		AuthFiles:        authFiles,
		ProjectIDs:       projectIDs,
		RequestTypes:     requestTypes,
		HeaderErrorKinds: headerErrorKinds,
		HeaderErrorCodes: headerErrorCodes,
		HeaderQuotaPlans: headerQuotaPlans,
		HeaderTraceIDs:   headerTraceIDs,
	}, nil
}

func (r *repository) FilterSelectorValuesWithFilter(ctx context.Context, filter AnalyticsFilter) (FilterSelectorValues, error) {
	models, err := r.distinctFilterValues(ctx, filter, "coalesce(nullif(model, ''), '')")
	if err != nil {
		return FilterSelectorValues{}, err
	}
	apiKeySelectors, err := r.apiKeyFilterSelectorValuesWithFilter(ctx, filter)
	if err != nil {
		return FilterSelectorValues{}, err
	}
	apiKeyHashSet := make(map[string]struct{}, len(apiKeySelectors))
	for _, selector := range apiKeySelectors {
		apiKeyHash := strings.ToLower(strings.TrimSpace(selector.APIKeyHash))
		if apiKeyHash != "" {
			apiKeyHashSet[apiKeyHash] = struct{}{}
		}
	}
	apiKeyHashes := make([]string, 0, len(apiKeyHashSet))
	for apiKeyHash := range apiKeyHashSet {
		apiKeyHashes = append(apiKeyHashes, apiKeyHash)
	}
	sort.Strings(apiKeyHashes)
	providers, err := r.distinctFilterValues(ctx, filter, "coalesce(nullif(auth_provider_snapshot, ''), nullif(provider, ''), '')")
	if err != nil {
		return FilterSelectorValues{}, err
	}
	authFiles, err := r.distinctFilterValues(ctx, filter, "coalesce(auth_file_snapshot, '')")
	if err != nil {
		return FilterSelectorValues{}, err
	}
	accountSelectors, err := r.accountFilterSelectorValuesWithFilter(ctx, filter)
	if err != nil {
		return FilterSelectorValues{}, err
	}
	accountSet := make(map[string]struct{}, len(accountSelectors))
	for _, selector := range accountSelectors {
		account := strings.TrimSpace(selector.AccountSnapshot)
		if account != "" {
			accountSet[account] = struct{}{}
		}
	}
	accounts := make([]string, 0, len(accountSet))
	for account := range accountSet {
		accounts = append(accounts, account)
	}
	sort.Strings(accounts)
	return FilterSelectorValues{
		Models:           models,
		APIKeyHashes:     apiKeyHashes,
		Providers:        providers,
		AuthFiles:        authFiles,
		Accounts:         accounts,
		AccountSelectors: accountSelectors,
		APIKeySelectors:  apiKeySelectors,
	}, nil
}

func (r *repository) apiKeyFilterSelectorValuesWithFilter(ctx context.Context, filter AnalyticsFilter) ([]APIKeySelectorValue, error) {
	where, args := analyticsWhere(filter)
	rows, err := r.db.QueryContext(ctx, `select
	coalesce(api_key_hash, ''),
	coalesce(nullif(auth_provider_snapshot, ''), nullif(provider, ''), ''),
	coalesce(auth_index, ''),
	coalesce(max(source), ''),
	coalesce(source_hash, '')
from usage_events `+where+`
group by
	coalesce(api_key_hash, ''),
	coalesce(nullif(auth_provider_snapshot, ''), nullif(provider, ''), ''),
	coalesce(auth_index, ''),
	coalesce(source_hash, '')
order by 1, 5, 3, 4, 2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	values := make([]APIKeySelectorValue, 0)
	for rows.Next() {
		var value APIKeySelectorValue
		if err := rows.Scan(
			&value.APIKeyHash,
			&value.AuthProviderSnapshot,
			&value.AuthIndex,
			&value.Source,
			&value.SourceHash,
		); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func (r *repository) accountFilterSelectorValuesWithFilter(ctx context.Context, filter AnalyticsFilter) ([]AccountSelectorValue, error) {
	where, args := analyticsWhere(filter)
	rows, err := r.db.QueryContext(ctx, `select
	coalesce(account_snapshot, ''),
	coalesce(auth_label_snapshot, ''),
	coalesce(nullif(auth_provider_snapshot, ''), nullif(provider, ''), ''),
	coalesce(auth_index, ''),
	coalesce(max(source), ''),
	coalesce(source_hash, '')
from usage_events `+where+`
and (
	coalesce(account_snapshot, '') <> '' or
	coalesce(auth_label_snapshot, '') <> '' or
	coalesce(auth_index, '') <> '' or
	coalesce(source, '') <> '' or
	coalesce(source_hash, '') <> ''
)
group by
	coalesce(account_snapshot, ''),
	coalesce(auth_label_snapshot, ''),
	coalesce(nullif(auth_provider_snapshot, ''), nullif(provider, ''), ''),
	coalesce(auth_index, ''),
	coalesce(source_hash, '')
order by 1, 2, 5, 4, 6`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	values := make([]AccountSelectorValue, 0)
	for rows.Next() {
		var value AccountSelectorValue
		if err := rows.Scan(
			&value.AccountSnapshot,
			&value.AuthLabelSnapshot,
			&value.AuthProviderSnapshot,
			&value.AuthIndex,
			&value.Source,
			&value.SourceHash,
		); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
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
		`+normalizedInputExpr+`,
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
		point.AddIfLongContext(inputTokens, outputTokens, cachedTokens, cacheReadTokens, cacheCreationTokens)
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
		coalesce(sum(`+normalizedInputExpr+`), 0),
	coalesce(sum(output_tokens), 0),
	coalesce(sum(`+compatCachedExpr+`), 0),
	coalesce(sum(cache_read_tokens), 0),
	coalesce(sum(cache_creation_tokens), 0),
	coalesce(sum(`+longInputExpr+`), 0),
	coalesce(sum(`+longOutputExpr+`), 0),
	coalesce(sum(`+longCachedExpr+`), 0),
	coalesce(sum(`+longCacheReadExpr+`), 0),
	coalesce(sum(`+longCacheCreationExpr+`), 0),
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
			&stat.LongInputTokens,
			&stat.LongOutputTokens,
			&stat.LongCachedTokens,
			&stat.LongCacheReadTokens,
			&stat.LongCacheCreationTokens,
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
		coalesce(sum(`+normalizedInputExpr+`), 0),
	coalesce(sum(output_tokens), 0),
	coalesce(sum(`+compatCachedExpr+`), 0),
	coalesce(sum(cache_read_tokens), 0),
	coalesce(sum(cache_creation_tokens), 0),
	coalesce(sum(`+longInputExpr+`), 0),
	coalesce(sum(`+longOutputExpr+`), 0),
	coalesce(sum(`+longCachedExpr+`), 0),
	coalesce(sum(`+longCacheReadExpr+`), 0),
	coalesce(sum(`+longCacheCreationExpr+`), 0),
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
			&stat.LongInputTokens,
			&stat.LongOutputTokens,
			&stat.LongCachedTokens,
			&stat.LongCacheReadTokens,
			&stat.LongCacheCreationTokens,
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
	`+credentialIDExpr+` as credential_id,
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
		coalesce(sum(`+normalizedInputExpr+`), 0),
	coalesce(sum(output_tokens), 0),
	coalesce(sum(`+compatCachedExpr+`), 0),
	coalesce(sum(cache_read_tokens), 0),
	coalesce(sum(cache_creation_tokens), 0),
	coalesce(sum(`+longInputExpr+`), 0),
	coalesce(sum(`+longOutputExpr+`), 0),
	coalesce(sum(`+longCachedExpr+`), 0),
	coalesce(sum(`+longCacheReadExpr+`), 0),
	coalesce(sum(`+longCacheCreationExpr+`), 0),
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
			&stat.LongInputTokens,
			&stat.LongOutputTokens,
			&stat.LongCachedTokens,
			&stat.LongCacheReadTokens,
			&stat.LongCacheCreationTokens,
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

func (r *repository) CredentialTimelineWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]CredentialTimelinePoint, error) {
	coreFromMS, coreToMS := usage.AnalyticsFullUTCHourRange(filter.FromMS, filter.ToMS)
	if coreFromMS >= coreToMS || !usage.CanMapUTCWholeHours(coreFromMS, coreToMS, granularity, location) {
		return r.credentialTimelineRawWithFilter(ctx, filter, granularity, location)
	}

	parts := make([][]CredentialTimelinePoint, 0, 3)
	if filter.FromMS < coreFromMS {
		edgeFilter := filter
		edgeFilter.ToMS = coreFromMS
		points, err := r.credentialTimelineRawWithFilter(ctx, edgeFilter, granularity, location)
		if err != nil {
			return nil, err
		}
		parts = append(parts, points)
	}
	coreFilter := filter
	coreFilter.FromMS = coreFromMS
	coreFilter.ToMS = coreToMS
	core, err := r.credentialTimelineHourlyWithFilter(ctx, coreFilter, granularity, location)
	if err != nil {
		return nil, err
	}
	parts = append(parts, core)
	if coreToMS < filter.ToMS {
		edgeFilter := filter
		edgeFilter.FromMS = coreToMS
		points, err := r.credentialTimelineRawWithFilter(ctx, edgeFilter, granularity, location)
		if err != nil {
			return nil, err
		}
		parts = append(parts, points)
	}
	return mergeCredentialTimelineParts(parts), nil
}

func (r *repository) credentialTimelineRawWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]CredentialTimelinePoint, error) {
	where, args := analyticsWhere(filter)
	query := fmt.Sprintf(`select
	timestamp_ms,
	`+credentialIDExpr+` as credential_id,
	coalesce(auth_file_snapshot, ''),
	coalesce(auth_index, ''),
	coalesce(source, ''),
	coalesce(source_hash, ''),
	coalesce(account_snapshot, ''),
	coalesce(auth_label_snapshot, ''),
	coalesce(nullif(auth_provider_snapshot, ''), provider, ''),
	coalesce(auth_project_id_snapshot, ''),
	model,
	coalesce(nullif(resolved_model, ''), model) as billing_model,
		coalesce(service_tier, '') as service_tier,
		failed,
		`+normalizedInputExpr+`,
	output_tokens,
	reasoning_tokens,
	`+compatCachedExpr+`,
	cache_read_tokens,
	cache_creation_tokens,
	total_tokens,
	latency_ms
from usage_events %s
order by timestamp_ms, credential_id, model`, where)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct {
		id               string
		authFileSnapshot string
		authIndex        string
		sourceHash       string
		bucketMS         int64
		model            string
		billingModel     string
		serviceTier      string
	}
	grouped := map[key]*CredentialTimelinePoint{}
	order := make([]key, 0)
	for rows.Next() {
		var timestampMS int64
		var point CredentialTimelinePoint
		var failed int
		var latency sql.NullFloat64
		var totalTokens int64
		if err := rows.Scan(
			&timestampMS,
			&point.ID,
			&point.AuthFileSnapshot,
			&point.AuthIndex,
			&point.Source,
			&point.SourceHash,
			&point.AccountSnapshot,
			&point.AuthLabelSnapshot,
			&point.AuthProviderSnapshot,
			&point.AuthProjectIDSnapshot,
			&point.Model,
			&point.BillingModel,
			&point.ServiceTier,
			&failed,
			&point.InputTokens,
			&point.OutputTokens,
			&point.ReasoningTokens,
			&point.CachedTokens,
			&point.CacheReadTokens,
			&point.CacheCreationTokens,
			&totalTokens,
			&latency,
		); err != nil {
			return nil, err
		}
		bucketMS := usage.AnalyticsBucketMS(timestampMS, granularity, location)
		mapKey := key{
			id:               point.ID,
			authFileSnapshot: point.AuthFileSnapshot,
			authIndex:        point.AuthIndex,
			sourceHash:       point.SourceHash,
			bucketMS:         bucketMS,
			model:            point.Model,
			billingModel:     point.BillingModel,
			serviceTier:      point.ServiceTier,
		}
		entry := grouped[mapKey]
		if entry == nil {
			entry = &CredentialTimelinePoint{
				ID:                    point.ID,
				AuthFileSnapshot:      point.AuthFileSnapshot,
				AuthIndex:             point.AuthIndex,
				Source:                point.Source,
				SourceHash:            point.SourceHash,
				AccountSnapshot:       point.AccountSnapshot,
				AuthLabelSnapshot:     point.AuthLabelSnapshot,
				AuthProviderSnapshot:  point.AuthProviderSnapshot,
				AuthProjectIDSnapshot: point.AuthProjectIDSnapshot,
				BucketMS:              bucketMS,
				Model:                 point.Model,
				BillingModel:          point.BillingModel,
				ServiceTier:           point.ServiceTier,
			}
			grouped[mapKey] = entry
			order = append(order, mapKey)
		}
		entry.Calls += 1
		entry.Tokens += totalTokens
		if failed != 0 {
			entry.Failure += 1
		} else {
			entry.Success += 1
		}
		entry.InputTokens += point.InputTokens
		entry.OutputTokens += point.OutputTokens
		entry.ReasoningTokens += point.ReasoningTokens
		entry.CachedTokens += point.CachedTokens
		entry.CacheReadTokens += point.CacheReadTokens
		entry.CacheCreationTokens += point.CacheCreationTokens
		entry.AddIfLongContext(point.InputTokens, point.OutputTokens, point.CachedTokens, point.CacheReadTokens, point.CacheCreationTokens)
		if latency.Valid && latency.Float64 > 0 {
			entry.AvgLatencyMS.Float64 += latency.Float64
			entry.LatencySamples += 1
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	points := make([]CredentialTimelinePoint, 0, len(order))
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

func (r *repository) credentialTimelineHourlyWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]CredentialTimelinePoint, error) {
	where, args := analyticsWhere(filter)
	const hourBucketExpr = "(timestamp_ms / 3600000) * 3600000"
	queryPrefix := ""
	queryFrom := "from usage_events\n"
	bucketExpr := "bucket_map.bucket_ms"
	queryArgs := args
	if offsetMS, ok := analyticsConstantOffsetMS(filter.FromMS, filter.ToMS, location); ok {
		bucketSizeMS := int64(time.Hour / time.Millisecond)
		if granularity == "day" {
			bucketSizeMS = int64(24 * time.Hour / time.Millisecond)
		}
		bucketExpr = fmt.Sprintf("((timestamp_ms + %d) / %d) * %d - %d", offsetMS, bucketSizeMS, bucketSizeMS, offsetMS)
	} else {
		mapSQL, mapArgs, ok := credentialBucketMapSQL(filter.FromMS, filter.ToMS, granularity, location)
		if !ok {
			return r.credentialTimelineRawWithFilter(ctx, filter, granularity, location)
		}
		queryPrefix = "with bucket_map(hour_bucket, bucket_ms) as (values " + mapSQL + ")\n"
		queryFrom += "join bucket_map on " + hourBucketExpr + " = bucket_map.hour_bucket\n"
		queryArgs = append(mapArgs, args...)
	}
	query := queryPrefix + `select
	` + bucketExpr + `,
	` + credentialIDExpr + ` as credential_id,
	coalesce(auth_file_snapshot, ''),
	coalesce(auth_index, ''),
	coalesce(source, ''),
	coalesce(source_hash, ''),
	coalesce(account_snapshot, ''),
	coalesce(auth_label_snapshot, ''),
	coalesce(nullif(auth_provider_snapshot, ''), provider, ''),
	coalesce(auth_project_id_snapshot, ''),
	model,
	coalesce(nullif(resolved_model, ''), model) as billing_model,
	coalesce(service_tier, '') as service_tier,
	count(*),
	coalesce(sum(total_tokens), 0),
	sum(case when failed = 0 then 1 else 0 end),
	sum(case when failed = 1 then 1 else 0 end),
	coalesce(sum(` + normalizedInputExpr + `), 0),
	coalesce(sum(output_tokens), 0),
	coalesce(sum(reasoning_tokens), 0),
	coalesce(sum(` + compatCachedExpr + `), 0),
	coalesce(sum(cache_read_tokens), 0),
	coalesce(sum(cache_creation_tokens), 0),
	coalesce(sum(` + longInputExpr + `), 0),
	coalesce(sum(` + longOutputExpr + `), 0),
	coalesce(sum(` + longCachedExpr + `), 0),
	coalesce(sum(` + longCacheReadExpr + `), 0),
	coalesce(sum(` + longCacheCreationExpr + `), 0),
	avg(case when latency_ms > 0 then latency_ms end),
	count(case when latency_ms > 0 then 1 end)
` + queryFrom + where + `
group by ` + bucketExpr + `, credential_id,
	coalesce(auth_file_snapshot, ''), coalesce(auth_index, ''), coalesce(source, ''), coalesce(source_hash, ''),
	coalesce(account_snapshot, ''), coalesce(auth_label_snapshot, ''),
	coalesce(nullif(auth_provider_snapshot, ''), provider, ''), coalesce(auth_project_id_snapshot, ''),
	model, billing_model, service_tier
order by min(timestamp_ms), credential_id, model`
	rows, err := r.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	points := make([]CredentialTimelinePoint, 0)
	for rows.Next() {
		var point CredentialTimelinePoint
		if err := rows.Scan(
			&point.BucketMS,
			&point.ID,
			&point.AuthFileSnapshot,
			&point.AuthIndex,
			&point.Source,
			&point.SourceHash,
			&point.AccountSnapshot,
			&point.AuthLabelSnapshot,
			&point.AuthProviderSnapshot,
			&point.AuthProjectIDSnapshot,
			&point.Model,
			&point.BillingModel,
			&point.ServiceTier,
			&point.Calls,
			&point.Tokens,
			&point.Success,
			&point.Failure,
			&point.InputTokens,
			&point.OutputTokens,
			&point.ReasoningTokens,
			&point.CachedTokens,
			&point.CacheReadTokens,
			&point.CacheCreationTokens,
			&point.LongInputTokens,
			&point.LongOutputTokens,
			&point.LongCachedTokens,
			&point.LongCacheReadTokens,
			&point.LongCacheCreationTokens,
			&point.AvgLatencyMS,
			&point.LatencySamples,
		); err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

const maxAnalyticsBucketMapHours = 4_000

func analyticsConstantOffsetMS(fromMS, toMS int64, location *time.Location) (int64, bool) {
	const hourMS = int64(time.Hour / time.Millisecond)
	if fromMS >= toMS {
		return 0, false
	}
	if location == nil {
		location = time.UTC
	}
	_, firstOffsetSeconds := time.UnixMilli(fromMS).In(location).Zone()
	for timestampMS := fromMS; timestampMS < toMS; timestampMS += hourMS {
		_, offsetSeconds := time.UnixMilli(timestampMS).In(location).Zone()
		if offsetSeconds != firstOffsetSeconds {
			return 0, false
		}
	}
	_, lastOffsetSeconds := time.UnixMilli(toMS - 1).In(location).Zone()
	if lastOffsetSeconds != firstOffsetSeconds {
		return 0, false
	}
	return int64(firstOffsetSeconds) * 1000, true
}

func credentialBucketMapSQL(fromMS, toMS int64, granularity string, location *time.Location) (string, []any, bool) {
	const hourMS = int64(time.Hour / time.Millisecond)
	hours := (toMS - fromMS) / hourMS
	if hours <= 0 || hours > maxAnalyticsBucketMapHours {
		return "", nil, false
	}
	placeholders := make([]string, 0, hours)
	args := make([]any, 0, hours*2)
	for hourBucketMS := fromMS; hourBucketMS < toMS; hourBucketMS += hourMS {
		placeholders = append(placeholders, "(?, ?)")
		args = append(args, hourBucketMS, usage.AnalyticsBucketMS(hourBucketMS, granularity, location))
	}
	return strings.Join(placeholders, ", "), args, true
}

func mergeCredentialTimelineParts(parts [][]CredentialTimelinePoint) []CredentialTimelinePoint {
	type key struct {
		id               string
		authFileSnapshot string
		authIndex        string
		sourceHash       string
		bucketMS         int64
		model            string
		billingModel     string
		serviceTier      string
	}
	grouped := make(map[key]*CredentialTimelinePoint)
	order := make([]key, 0)
	for _, points := range parts {
		for _, point := range points {
			mapKey := key{point.ID, point.AuthFileSnapshot, point.AuthIndex, point.SourceHash, point.BucketMS, point.Model, point.BillingModel, point.ServiceTier}
			entry := grouped[mapKey]
			if entry == nil {
				next := point
				grouped[mapKey] = &next
				order = append(order, mapKey)
				continue
			}
			if entry.Source == "" {
				entry.Source = point.Source
			}
			if entry.AccountSnapshot == "" {
				entry.AccountSnapshot = point.AccountSnapshot
			}
			if entry.AuthLabelSnapshot == "" {
				entry.AuthLabelSnapshot = point.AuthLabelSnapshot
			}
			if entry.AuthProviderSnapshot == "" {
				entry.AuthProviderSnapshot = point.AuthProviderSnapshot
			}
			if entry.AuthProjectIDSnapshot == "" {
				entry.AuthProjectIDSnapshot = point.AuthProjectIDSnapshot
			}
			latencySum := entry.AvgLatencyMS.Float64*float64(entry.LatencySamples) + point.AvgLatencyMS.Float64*float64(point.LatencySamples)
			entry.Calls += point.Calls
			entry.Tokens += point.Tokens
			entry.Success += point.Success
			entry.Failure += point.Failure
			entry.InputTokens += point.InputTokens
			entry.OutputTokens += point.OutputTokens
			entry.ReasoningTokens += point.ReasoningTokens
			entry.CachedTokens += point.CachedTokens
			entry.CacheReadTokens += point.CacheReadTokens
			entry.CacheCreationTokens += point.CacheCreationTokens
			entry.LongInputTokens += point.LongInputTokens
			entry.LongOutputTokens += point.LongOutputTokens
			entry.LongCachedTokens += point.LongCachedTokens
			entry.LongCacheReadTokens += point.LongCacheReadTokens
			entry.LongCacheCreationTokens += point.LongCacheCreationTokens
			entry.LatencySamples += point.LatencySamples
			entry.AvgLatencyMS.Valid = entry.LatencySamples > 0
			if entry.AvgLatencyMS.Valid {
				entry.AvgLatencyMS.Float64 = latencySum / float64(entry.LatencySamples)
			}
		}
	}
	result := make([]CredentialTimelinePoint, 0, len(order))
	for _, mapKey := range order {
		result = append(result, *grouped[mapKey])
	}
	return result
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
		coalesce(sum(`+normalizedInputExpr+`), 0),
	coalesce(sum(output_tokens), 0),
	coalesce(sum(`+compatCachedExpr+`), 0),
	coalesce(sum(cache_read_tokens), 0),
	coalesce(sum(cache_creation_tokens), 0),
	coalesce(sum(`+longInputExpr+`), 0),
	coalesce(sum(`+longOutputExpr+`), 0),
	coalesce(sum(`+longCachedExpr+`), 0),
	coalesce(sum(`+longCacheReadExpr+`), 0),
	coalesce(sum(`+longCacheCreationExpr+`), 0),
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
			&stat.LongInputTokens,
			&stat.LongOutputTokens,
			&stat.LongCachedTokens,
			&stat.LongCacheReadTokens,
			&stat.LongCacheCreationTokens,
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
		coalesce(sum(`+normalizedInputExpr+`), 0),
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
	coalesce(fail_summary, ''),
	coalesce(response_metadata_json, ''),
	header_quota_recover_at_ms,
	header_quota_used_percent,
	coalesce(header_quota_plan_type, ''),
	coalesce(header_error_kind, ''),
	coalesce(header_error_code, ''),
	coalesce(header_trace_id, '')
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
		var responseMetadataJSON string
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
			&responseMetadataJSON,
			&failure.HeaderQuotaRecoverAtMS,
			&failure.HeaderQuotaUsedPercent,
			&failure.HeaderQuotaPlanType,
			&failure.HeaderErrorKind,
			&failure.HeaderErrorCode,
			&failure.HeaderTraceID,
		); err != nil {
			return nil, err
		}
		failure.ResponseMetadata = usage.ResponseHeaderMetadataFromJSON(responseMetadataJSON)
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
	`+normalizedInputExpr+`,
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
	coalesce(fail_summary, ''),
	coalesce(response_metadata_json, ''),
	header_quota_recover_at_ms,
	header_quota_used_percent,
	coalesce(header_quota_plan_type, ''),
	coalesce(header_error_kind, ''),
	coalesce(header_error_code, ''),
	coalesce(header_trace_id, '')
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
		var responseMetadataJSON string
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
			&responseMetadataJSON,
			&item.HeaderQuotaRecoverAtMS,
			&item.HeaderQuotaUsedPercent,
			&item.HeaderQuotaPlanType,
			&item.HeaderErrorKind,
			&item.HeaderErrorCode,
			&item.HeaderTraceID,
		); err != nil {
			return EventsPage{}, err
		}
		item.Failed = failed != 0
		item.ResponseMetadata = usage.ResponseHeaderMetadataFromJSON(responseMetadataJSON)
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

func (r *repository) LatestHeaderSnapshots(ctx context.Context, sinceMS int64, limit int) ([]HeaderSnapshot, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `with candidates as (
	select
		id,
		event_hash,
		timestamp_ms,
		coalesce(auth_file_snapshot, '') as auth_file_snapshot,
		coalesce(auth_index, '') as auth_index,
		coalesce(account_snapshot, '') as account_snapshot,
		coalesce(auth_label_snapshot, '') as auth_label_snapshot,
		coalesce(nullif(auth_provider_snapshot, ''), provider, '') as auth_provider_snapshot,
		coalesce(auth_project_id_snapshot, '') as auth_project_id_snapshot,
		coalesce(source, '') as source,
		coalesce(source_hash, '') as source_hash,
		coalesce(response_metadata_json, '') as response_metadata_json,
		header_quota_recover_at_ms,
		header_quota_used_percent,
		coalesce(header_quota_plan_type, '') as header_quota_plan_type,
		coalesce(header_error_kind, '') as header_error_kind,
		coalesce(header_error_code, '') as header_error_code,
		coalesce(header_trace_id, '') as header_trace_id,
		case
			when coalesce(auth_file_snapshot, '') <> '' and coalesce(auth_index, '') <> '' then coalesce(auth_file_snapshot, '') || '::' || coalesce(auth_index, '')
			when coalesce(auth_file_snapshot, '') <> '' then 'file::' || coalesce(auth_file_snapshot, '')
			when coalesce(auth_index, '') <> '' then 'auth::' || coalesce(auth_index, '')
			when coalesce(account_snapshot, '') <> '' then 'account::' || lower(coalesce(account_snapshot, ''))
			when coalesce(source_hash, '') <> '' then 'source::' || coalesce(source_hash, '')
			else 'event::' || event_hash
		end as snapshot_key
	from usage_events
	where timestamp_ms >= ?
	and (
		coalesce(response_metadata_json, '') <> ''
		or header_quota_recover_at_ms is not null
		or header_quota_used_percent is not null
		or coalesce(header_quota_plan_type, '') <> ''
		or coalesce(header_error_kind, '') <> ''
		or coalesce(header_error_code, '') <> ''
		or coalesce(header_trace_id, '') <> ''
	)
	and (
		coalesce(auth_file_snapshot, '') <> ''
		or coalesce(auth_index, '') <> ''
		or coalesce(account_snapshot, '') <> ''
		or coalesce(source_hash, '') <> ''
	)
), ranked as (
	select *, row_number() over (partition by snapshot_key order by timestamp_ms desc, id desc) as rn
	from candidates
)
select
	id,
	event_hash,
	timestamp_ms,
	auth_file_snapshot,
	auth_index,
	account_snapshot,
	auth_label_snapshot,
	auth_provider_snapshot,
	auth_project_id_snapshot,
	source,
	source_hash,
	response_metadata_json,
	header_quota_recover_at_ms,
	header_quota_used_percent,
	header_quota_plan_type,
	header_error_kind,
	header_error_code,
	header_trace_id
from ranked
where rn = 1
order by timestamp_ms desc, id desc
limit ?`, sinceMS, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]HeaderSnapshot, 0, limit)
	for rows.Next() {
		var item HeaderSnapshot
		var responseMetadataJSON string
		if err := rows.Scan(
			&item.ID,
			&item.EventHash,
			&item.TimestampMS,
			&item.AuthFileSnapshot,
			&item.AuthIndex,
			&item.AccountSnapshot,
			&item.AuthLabelSnapshot,
			&item.AuthProviderSnapshot,
			&item.AuthProjectIDSnapshot,
			&item.Source,
			&item.SourceHash,
			&responseMetadataJSON,
			&item.HeaderQuotaRecoverAtMS,
			&item.HeaderQuotaUsedPercent,
			&item.HeaderQuotaPlanType,
			&item.HeaderErrorKind,
			&item.HeaderErrorCode,
			&item.HeaderTraceID,
		); err != nil {
			return nil, err
		}
		item.ResponseMetadata = usage.ResponseHeaderMetadataFromJSON(responseMetadataJSON)
		items = append(items, item)
	}
	return items, rows.Err()
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
	addInCondition(credentialIDExpr, filter.CredentialIDs)
	addInCondition("auth_file_snapshot", filter.AuthFiles)
	addInCondition("auth_index", filter.AuthIndices)
	addInCondition("api_key_hash", filter.APIKeyHashes)
	addInCondition("source_hash", filter.SourceHashes)
	addInCondition("auth_project_id_snapshot", filter.ProjectIDs)
	addInCondition("executor_type", filter.RequestTypes)
	addInCondition("header_error_kind", filter.HeaderErrorKinds)
	addInCondition("header_error_code", filter.HeaderErrorCodes)
	addInCondition("header_quota_plan_type", filter.HeaderQuotaPlans)
	addInCondition("header_trace_id", filter.HeaderTraceIDs)
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
