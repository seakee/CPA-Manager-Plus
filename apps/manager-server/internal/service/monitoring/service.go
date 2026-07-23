package monitoring

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/pricing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usagehourly"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	defaultEventsLimit         = 100
	defaultDrilldownLimit      = 20
	defaultHeaderSnapshotDays  = 30
	defaultHeaderSnapshotLimit = 1000
	maxEventsLimit             = 50000
	maxDrilldownLimit          = 100
	maxHeaderSnapshotDays      = 365
	maxHeaderSnapshotLimit     = 5000
	maxAccountHistoryTargets   = 200
	accountHistoryCatchUpLimit = 5000
	recentWindowMS             = 30 * 60 * 1000
	// analyticsPrefetchConcurrency limits background analytics reads. The
	// foreground summary/task path may hold one additional SQLite connection.
	analyticsPrefetchConcurrency = 2
)

type Service struct {
	store        *store.Store
	hourlyReader *usagehourly.Reader
}

type analyticsQueryGroup struct {
	ctx       context.Context
	cancel    context.CancelFunc
	semaphore chan struct{}
	waitGroup sync.WaitGroup
	errorOnce sync.Once
	waitOnce  sync.Once
	err       error
}

func newAnalyticsQueryGroup(ctx context.Context, concurrency int) *analyticsQueryGroup {
	if concurrency <= 0 {
		concurrency = 1
	}
	queryCtx, cancel := context.WithCancel(ctx)
	return &analyticsQueryGroup{
		ctx:       queryCtx,
		cancel:    cancel,
		semaphore: make(chan struct{}, concurrency),
	}
}

func (g *analyticsQueryGroup) Go(query func(context.Context) error) {
	g.waitGroup.Add(1)
	go func() {
		defer g.waitGroup.Done()
		select {
		case g.semaphore <- struct{}{}:
			defer func() { <-g.semaphore }()
		case <-g.ctx.Done():
			return
		}
		if err := query(g.ctx); err != nil {
			g.errorOnce.Do(func() {
				g.err = err
				g.cancel()
			})
		}
	}()
}

func (g *analyticsQueryGroup) Wait() error {
	g.waitOnce.Do(func() {
		g.waitGroup.Wait()
		if g.err == nil && g.ctx.Err() != nil {
			g.err = g.ctx.Err()
		}
		g.cancel()
	})
	return g.err
}

func (g *analyticsQueryGroup) Close() {
	g.cancel()
	_ = g.Wait()
}

func New(store *store.Store, hourlyRollupEnabled ...bool) *Service {
	enabled := false
	if len(hourlyRollupEnabled) > 0 {
		enabled = hourlyRollupEnabled[0]
	}
	return &Service{
		store:        store,
		hourlyReader: usagehourly.New(store, enabled, "monitoring-rollup"),
	}
}

type Request struct {
	FromMS           int64   `json:"from_ms"`
	ToMS             int64   `json:"to_ms"`
	NowMS            int64   `json:"now_ms"`
	TimeZone         string  `json:"time_zone"`
	SearchQuery      string  `json:"search_query"`
	SearchAPIKeyHash string  `json:"search_api_key_hash"`
	Filters          Filters `json:"filters"`
	Include          Include `json:"include"`
}

type Filters struct {
	Models           []string `json:"models"`
	Providers        []string `json:"providers"`
	Accounts         []string `json:"accounts"`
	CredentialIDs    []string `json:"credential_ids"`
	AuthFiles        []string `json:"auth_files"`
	AuthIndices      []string `json:"auth_indices"`
	APIKeyHashes     []string `json:"api_key_hashes"`
	SourceHashes     []string `json:"source_hashes"`
	ProjectIDs       []string `json:"project_ids"`
	RequestTypes     []string `json:"request_types"`
	HeaderErrorKinds []string `json:"header_error_kinds"`
	HeaderErrorCodes []string `json:"header_error_codes"`
	HeaderQuotaPlans []string `json:"header_quota_plans"`
	HeaderTraceIDs   []string `json:"header_trace_ids"`
	IncludeFailed    *bool    `json:"include_failed"`
	FailedOnly       bool     `json:"failed_only"`
	MinLatencyMS     int64    `json:"min_latency_ms"`
	CacheStatus      string   `json:"cache_status"`
}

type Include struct {
	Summary            bool              `json:"summary"`
	SummaryProfile     string            `json:"summary_profile"`
	SummaryPercentiles bool              `json:"summary_percentiles"`
	SummaryComparison  bool              `json:"summary_comparison"`
	Timeline           bool              `json:"timeline"`
	HourlyDistribution bool              `json:"hourly_distribution"`
	ModelShare         bool              `json:"model_share"`
	ChannelShare       bool              `json:"channel_share"`
	ModelStats         bool              `json:"model_stats"`
	FailureSources     bool              `json:"failure_sources"`
	AccountStats       bool              `json:"account_stats"`
	CredentialStats    bool              `json:"credential_stats"`
	CredentialTimeline bool              `json:"credential_timeline"`
	APIKeyTimeline     bool              `json:"api_key_timeline"`
	APIKeyStats        bool              `json:"api_key_stats"`
	FilterOptions      bool              `json:"filter_options"`
	FilterSelectors    bool              `json:"filter_selectors"`
	Heatmap            bool              `json:"heatmap"`
	AnomalyPoints      bool              `json:"anomaly_points"`
	TaskBuckets        bool              `json:"task_buckets"`
	RecentFailures     int               `json:"recent_failures"`
	EventsPage         *EventsPage       `json:"events_page"`
	DrilldownPreview   *DrilldownPreview `json:"drilldown_preview"`
	Granularity        string            `json:"granularity"`
}

type EventsPage struct {
	Limit    int    `json:"limit"`
	BeforeMS *int64 `json:"before_ms"`
	BeforeID *int64 `json:"before_id"`
}

type DrilldownPreview struct {
	FromMS int64 `json:"from_ms"`
	ToMS   int64 `json:"to_ms"`
	Limit  int   `json:"limit"`
}

type Response struct {
	GeneratedAtMS      int64                     `json:"generated_at_ms"`
	Granularity        string                    `json:"granularity"`
	Summary            *Summary                  `json:"summary,omitempty"`
	SummaryComparison  *SummaryComparison        `json:"summary_comparison,omitempty"`
	Timeline           []TimelinePoint           `json:"timeline,omitempty"`
	HourlyDistribution []HourlyPoint             `json:"hourly_distribution,omitempty"`
	Heatmap            []HeatmapPoint            `json:"heatmap,omitempty"`
	AnomalyPoints      []AnomalyPoint            `json:"anomaly_points,omitempty"`
	ModelShare         []ModelShareRow           `json:"model_share,omitempty"`
	ModelStats         []ModelStat               `json:"model_stats,omitempty"`
	ChannelShare       []ChannelShareRow         `json:"channel_share,omitempty"`
	FailureSources     []FailureSourceRow        `json:"failure_sources,omitempty"`
	AccountStats       []AccountStatRow          `json:"account_stats,omitempty"`
	CredentialStats    []CredentialStatRow       `json:"credential_stats,omitempty"`
	CredentialTimeline []CredentialTimelinePoint `json:"credential_timeline,omitempty"`
	APIKeyTimeline     []APIKeyTimelinePoint     `json:"api_key_timeline,omitempty"`
	APIKeyStats        []APIKeyStatRow           `json:"api_key_stats,omitempty"`
	FilterOptions      *FilterOptions            `json:"filter_options,omitempty"`
	TaskBuckets        []TaskBucketRow           `json:"task_buckets,omitempty"`
	RecentFailures     []RecentFailure           `json:"recent_failures,omitempty"`
	Events             *EventsResponse           `json:"events,omitempty"`
	DrilldownPreview   *EventsResponse           `json:"drilldown_preview,omitempty"`
}

type HeaderSnapshotsRequest struct {
	Days  int
	Limit int
}

type HeaderSnapshotsResponse struct {
	GeneratedAtMS int64            `json:"generated_at_ms"`
	FromMS        int64            `json:"from_ms"`
	ToMS          int64            `json:"to_ms"`
	Items         []HeaderSnapshot `json:"items"`
}

type AccountHistoryRequest struct {
	Accounts []AccountHistoryTarget `json:"accounts"`
	CatchUp  bool                   `json:"catch_up"`
}

type AccountHistoryTarget struct {
	AccountKey        string `json:"account_key,omitempty"`
	AccountSnapshot   string `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot string `json:"auth_label_snapshot,omitempty"`
	AuthIndex         string `json:"auth_index,omitempty"`
	Source            string `json:"source,omitempty"`
}

type AccountHistoryResponse struct {
	GeneratedAtMS int64                         `json:"generated_at_ms"`
	Checkpoint    AccountHistoryCheckpointState `json:"checkpoint"`
	Items         []AccountHistoryItem          `json:"items"`
}

type AccountHistoryCheckpointState struct {
	LastEventID int64 `json:"last_event_id"`
	LatestID    int64 `json:"latest_id"`
	Pending     bool  `json:"pending"`
	Processed   int   `json:"processed"`
}

type AccountHistoryItem struct {
	AccountKey    string   `json:"account_key"`
	Matched       bool     `json:"matched"`
	TotalRequests int64    `json:"total_requests"`
	SuccessCalls  int64    `json:"success_calls"`
	FailureCalls  int64    `json:"failure_calls"`
	TotalTokens   int64    `json:"total_tokens"`
	TotalCost     float64  `json:"total_cost"`
	SuccessRate   *float64 `json:"success_rate"`
	FirstSeenMS   *int64   `json:"first_seen_ms"`
	LastSeenMS    *int64   `json:"last_seen_ms"`
	SyncStatus    string   `json:"sync_status"`
}

type Summary struct {
	TotalCalls            int64    `json:"total_calls"`
	SuccessCalls          int64    `json:"success_calls"`
	FailureCalls          int64    `json:"failure_calls"`
	SuccessRate           float64  `json:"success_rate"`
	InputTokens           int64    `json:"input_tokens"`
	OutputTokens          int64    `json:"output_tokens"`
	CachedTokens          int64    `json:"cached_tokens"`
	CacheReadTokens       int64    `json:"cache_read_tokens"`
	CacheCreationTokens   int64    `json:"cache_creation_tokens"`
	CacheHitRate          float64  `json:"cache_hit_rate"`
	ReasoningTokens       int64    `json:"reasoning_tokens"`
	TotalTokens           int64    `json:"total_tokens"`
	TotalCost             float64  `json:"total_cost"`
	AverageCostPerCall    float64  `json:"average_cost_per_call"`
	AverageLatencyMS      *float64 `json:"average_latency_ms"`
	P95LatencyMS          *float64 `json:"p95_latency_ms"`
	P95TTFTMS             *float64 `json:"p95_ttft_ms"`
	ZeroTokenCalls        int64    `json:"zero_token_calls"`
	RPM30M                float64  `json:"rpm_30m"`
	TPM30M                float64  `json:"tpm_30m"`
	AvgDailyRequests      float64  `json:"avg_daily_requests"`
	AvgDailyTokens        float64  `json:"avg_daily_tokens"`
	ApproxTasks           int64    `json:"approx_tasks"`
	ApproxTaskFailures    int64    `json:"approx_task_failures"`
	ApproxTaskSuccessRate float64  `json:"approx_task_success_rate"`
	ZeroTokenModels       []string `json:"zero_token_models"`
}

// SummaryComparison holds previous-period aggregates computed with the same
// filter as the current summary, letting the client derive period-over-period
// deltas. It is only populated when Include.SummaryComparison is set, keeping
// the extra queries off other consumers (dashboard/monitoring) that don't need it.
type SummaryComparison struct {
	FromMS       int64   `json:"from_ms"`
	ToMS         int64   `json:"to_ms"`
	TotalCalls   int64   `json:"total_calls"`
	SuccessCalls int64   `json:"success_calls"`
	FailureCalls int64   `json:"failure_calls"`
	SuccessRate  float64 `json:"success_rate"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCost    float64 `json:"total_cost"`
}

type TimelinePoint struct {
	BucketMS            int64    `json:"bucket_ms"`
	Label               string   `json:"label"`
	Calls               int64    `json:"calls"`
	Tokens              int64    `json:"tokens"`
	Success             int64    `json:"success"`
	Failure             int64    `json:"failure"`
	InputTokens         int64    `json:"input_tokens"`
	OutputTokens        int64    `json:"output_tokens"`
	CachedTokens        int64    `json:"cached_tokens"`
	CacheReadTokens     int64    `json:"cache_read_tokens"`
	CacheCreationTokens int64    `json:"cache_creation_tokens"`
	CacheHitRate        float64  `json:"cache_hit_rate"`
	ReasoningTokens     int64    `json:"reasoning_tokens"`
	TotalTokens         int64    `json:"total_tokens"`
	Cost                float64  `json:"cost"`
	AvgLatencyMS        *float64 `json:"average_latency_ms"`
	P95LatencyMS        *float64 `json:"p95_latency_ms"`
	P95TTFTMS           *float64 `json:"p95_ttft_ms"`
	SuccessRate         float64  `json:"success_rate"`
	FailureRate         float64  `json:"failure_rate"`
}

type HourlyPoint struct {
	Hour   int   `json:"hour"`
	Calls  int64 `json:"calls"`
	Tokens int64 `json:"tokens"`
}

type HeatmapPoint struct {
	Weekday              int                  `json:"weekday"`
	Hour                 int                  `json:"hour"`
	Calls                int64                `json:"calls"`
	Success              int64                `json:"success"`
	Failure              int64                `json:"failure"`
	Tokens               int64                `json:"tokens"`
	Cost                 float64              `json:"cost"`
	FailureRate          float64              `json:"failure_rate"`
	ModelContributors    []HeatmapContributor `json:"model_contributors,omitempty"`
	APIKeyContributors   []HeatmapContributor `json:"api_key_contributors,omitempty"`
	ProviderContributors []HeatmapContributor `json:"provider_contributors,omitempty"`
}

type HeatmapContributor struct {
	Key         string  `json:"key"`
	Label       string  `json:"label,omitempty"`
	Calls       int64   `json:"calls"`
	Success     int64   `json:"success"`
	Failure     int64   `json:"failure"`
	Tokens      int64   `json:"tokens"`
	Cost        float64 `json:"cost"`
	FailureRate float64 `json:"failure_rate"`
	Share       float64 `json:"share"`
}

type AnomalyPoint struct {
	BucketMS               int64    `json:"bucket_ms"`
	BucketEndMS            int64    `json:"bucket_end_ms"`
	Label                  string   `json:"label"`
	Severity               string   `json:"severity"`
	MetricKeys             []string `json:"metric_keys"`
	Calls                  int64    `json:"calls"`
	TotalTokens            int64    `json:"total_tokens"`
	Cost                   float64  `json:"cost"`
	FailureRate            float64  `json:"failure_rate"`
	RequestChange          float64  `json:"request_change"`
	CostChange             float64  `json:"cost_change"`
	TokensPerRequestChange float64  `json:"tokens_per_request_change"`
	CacheHitRateChange     float64  `json:"cache_hit_rate_change"`
	FailureRateChange      float64  `json:"failure_rate_change"`
	LatencyP95Change       float64  `json:"latency_p95_change"`
}

type ModelShareRow struct {
	Model  string  `json:"model"`
	Calls  int64   `json:"calls"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

type ModelStat struct {
	Model               string  `json:"model"`
	Calls               int64   `json:"calls"`
	SuccessCalls        int64   `json:"success_calls"`
	FailureCalls        int64   `json:"failure_calls"`
	SuccessRate         float64 `json:"success_rate"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CachedTokens        int64   `json:"cached_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CacheHitTokens      int64   `json:"cache_hit_tokens"`
	CacheHitInputTokens int64   `json:"cache_hit_input_tokens"`
	CacheHitRate        float64 `json:"cache_hit_rate"`
	TotalTokens         int64   `json:"total_tokens"`
	Cost                float64 `json:"cost"`
}

type ChannelShareRow struct {
	AuthIndex            string   `json:"auth_index"`
	Source               string   `json:"source,omitempty"`
	AccountSnapshot      string   `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot    string   `json:"auth_label_snapshot,omitempty"`
	AuthProviderSnapshot string   `json:"auth_provider_snapshot,omitempty"`
	Calls                int64    `json:"calls"`
	Success              int64    `json:"success"`
	Failure              int64    `json:"failure"`
	Tokens               int64    `json:"tokens"`
	Cost                 float64  `json:"cost"`
	AvgLatencyMS         *float64 `json:"average_latency_ms"`
}

type FailureSourceRow struct {
	Source               string   `json:"source,omitempty"`
	SourceHash           string   `json:"source_hash"`
	AuthIndex            string   `json:"auth_index"`
	AccountSnapshot      string   `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot    string   `json:"auth_label_snapshot,omitempty"`
	AuthProviderSnapshot string   `json:"auth_provider_snapshot,omitempty"`
	Calls                int64    `json:"calls"`
	Failure              int64    `json:"failure"`
	LastSeenMS           int64    `json:"last_seen_ms"`
	AvgLatencyMS         *float64 `json:"average_latency_ms"`
}

type AccountStatRow struct {
	ID                   string                `json:"id"`
	AccountSnapshot      string                `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot    string                `json:"auth_label_snapshot,omitempty"`
	AuthProviderSnapshot string                `json:"auth_provider_snapshot,omitempty"`
	AuthIndices          []string              `json:"auth_indices,omitempty"`
	Sources              []string              `json:"sources,omitempty"`
	SourceHashes         []string              `json:"source_hashes,omitempty"`
	Calls                int64                 `json:"calls"`
	SuccessCalls         int64                 `json:"success_calls"`
	FailureCalls         int64                 `json:"failure_calls"`
	SuccessRate          float64               `json:"success_rate"`
	InputTokens          int64                 `json:"input_tokens"`
	OutputTokens         int64                 `json:"output_tokens"`
	CachedTokens         int64                 `json:"cached_tokens"`
	CacheReadTokens      int64                 `json:"cache_read_tokens"`
	CacheCreationTokens  int64                 `json:"cache_creation_tokens"`
	TotalTokens          int64                 `json:"total_tokens"`
	Cost                 float64               `json:"cost"`
	AvgLatencyMS         *float64              `json:"average_latency_ms"`
	LastSeenMS           int64                 `json:"last_seen_ms"`
	Models               []AccountModelStatRow `json:"models,omitempty"`
}

type CredentialStatRow struct {
	ID                    string                `json:"id"`
	AuthFileSnapshot      string                `json:"auth_file_snapshot,omitempty"`
	AuthIndex             string                `json:"auth_index,omitempty"`
	Source                string                `json:"source,omitempty"`
	SourceHash            string                `json:"source_hash,omitempty"`
	AccountSnapshot       string                `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot     string                `json:"auth_label_snapshot,omitempty"`
	AuthProviderSnapshot  string                `json:"auth_provider_snapshot,omitempty"`
	AuthProjectIDSnapshot string                `json:"auth_project_id_snapshot,omitempty"`
	Calls                 int64                 `json:"calls"`
	SuccessCalls          int64                 `json:"success_calls"`
	FailureCalls          int64                 `json:"failure_calls"`
	SuccessRate           float64               `json:"success_rate"`
	InputTokens           int64                 `json:"input_tokens"`
	OutputTokens          int64                 `json:"output_tokens"`
	CachedTokens          int64                 `json:"cached_tokens"`
	CacheReadTokens       int64                 `json:"cache_read_tokens"`
	CacheCreationTokens   int64                 `json:"cache_creation_tokens"`
	TotalTokens           int64                 `json:"total_tokens"`
	Cost                  float64               `json:"cost"`
	AvgLatencyMS          *float64              `json:"average_latency_ms"`
	LastSeenMS            int64                 `json:"last_seen_ms"`
	Models                []AccountModelStatRow `json:"models,omitempty"`
}

type CredentialTimelinePoint struct {
	ID                    string   `json:"id"`
	Label                 string   `json:"label"`
	AuthFileSnapshot      string   `json:"auth_file_snapshot,omitempty"`
	AuthIndex             string   `json:"auth_index,omitempty"`
	Source                string   `json:"source,omitempty"`
	SourceHash            string   `json:"source_hash,omitempty"`
	AccountSnapshot       string   `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot     string   `json:"auth_label_snapshot,omitempty"`
	AuthProviderSnapshot  string   `json:"auth_provider_snapshot,omitempty"`
	AuthProjectIDSnapshot string   `json:"auth_project_id_snapshot,omitempty"`
	BucketMS              int64    `json:"bucket_ms"`
	BucketLabel           string   `json:"bucket_label"`
	Calls                 int64    `json:"calls"`
	Tokens                int64    `json:"tokens"`
	Success               int64    `json:"success"`
	Failure               int64    `json:"failure"`
	InputTokens           int64    `json:"input_tokens"`
	OutputTokens          int64    `json:"output_tokens"`
	CachedTokens          int64    `json:"cached_tokens"`
	CacheReadTokens       int64    `json:"cache_read_tokens"`
	CacheCreationTokens   int64    `json:"cache_creation_tokens"`
	ReasoningTokens       int64    `json:"reasoning_tokens"`
	TotalTokens           int64    `json:"total_tokens"`
	Cost                  float64  `json:"cost"`
	AvgLatencyMS          *float64 `json:"average_latency_ms"`
	SuccessRate           float64  `json:"success_rate"`
	FailureRate           float64  `json:"failure_rate"`
}

type APIKeyTimelinePoint struct {
	APIKeyHash          string   `json:"api_key_hash"`
	BucketMS            int64    `json:"bucket_ms"`
	BucketLabel         string   `json:"bucket_label"`
	Calls               int64    `json:"calls"`
	Tokens              int64    `json:"tokens"`
	Success             int64    `json:"success"`
	Failure             int64    `json:"failure"`
	InputTokens         int64    `json:"input_tokens"`
	OutputTokens        int64    `json:"output_tokens"`
	CachedTokens        int64    `json:"cached_tokens"`
	CacheReadTokens     int64    `json:"cache_read_tokens"`
	CacheCreationTokens int64    `json:"cache_creation_tokens"`
	ReasoningTokens     int64    `json:"reasoning_tokens"`
	TotalTokens         int64    `json:"total_tokens"`
	Cost                float64  `json:"cost"`
	AvgLatencyMS        *float64 `json:"average_latency_ms"`
	SuccessRate         float64  `json:"success_rate"`
	FailureRate         float64  `json:"failure_rate"`
}

type AccountModelStatRow struct {
	Model               string  `json:"model"`
	Calls               int64   `json:"calls"`
	SuccessCalls        int64   `json:"success_calls"`
	FailureCalls        int64   `json:"failure_calls"`
	SuccessRate         float64 `json:"success_rate"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CachedTokens        int64   `json:"cached_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CacheHitTokens      int64   `json:"cache_hit_tokens"`
	CacheHitInputTokens int64   `json:"cache_hit_input_tokens"`
	CacheHitRate        float64 `json:"cache_hit_rate"`
	TotalTokens         int64   `json:"total_tokens"`
	Cost                float64 `json:"cost"`
	LastSeenMS          int64   `json:"last_seen_ms"`
}

type APIKeyStatRow struct {
	ID                   string                `json:"id"`
	APIKeyHash           string                `json:"api_key_hash"`
	AccountSnapshot      string                `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot    string                `json:"auth_label_snapshot,omitempty"`
	AuthProviderSnapshot string                `json:"auth_provider_snapshot,omitempty"`
	AuthIndices          []string              `json:"auth_indices,omitempty"`
	Sources              []string              `json:"sources,omitempty"`
	SourceHashes         []string              `json:"source_hashes,omitempty"`
	Calls                int64                 `json:"calls"`
	SuccessCalls         int64                 `json:"success_calls"`
	FailureCalls         int64                 `json:"failure_calls"`
	SuccessRate          float64               `json:"success_rate"`
	InputTokens          int64                 `json:"input_tokens"`
	OutputTokens         int64                 `json:"output_tokens"`
	CachedTokens         int64                 `json:"cached_tokens"`
	CacheReadTokens      int64                 `json:"cache_read_tokens"`
	CacheCreationTokens  int64                 `json:"cache_creation_tokens"`
	TotalTokens          int64                 `json:"total_tokens"`
	Cost                 float64               `json:"cost"`
	AvgLatencyMS         *float64              `json:"average_latency_ms"`
	LastSeenMS           int64                 `json:"last_seen_ms"`
	Models               []AccountModelStatRow `json:"models,omitempty"`
	Contexts             []APIKeyContextRow    `json:"contexts,omitempty"`
}

type APIKeyContextRow struct {
	ID                   string   `json:"id"`
	AccountSnapshot      string   `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot    string   `json:"auth_label_snapshot,omitempty"`
	AuthProviderSnapshot string   `json:"auth_provider_snapshot,omitempty"`
	AuthIndex            string   `json:"auth_index,omitempty"`
	Source               string   `json:"source,omitempty"`
	SourceHash           string   `json:"source_hash,omitempty"`
	Calls                int64    `json:"calls"`
	SuccessCalls         int64    `json:"success_calls"`
	FailureCalls         int64    `json:"failure_calls"`
	SuccessRate          float64  `json:"success_rate"`
	FailureRate          float64  `json:"failure_rate"`
	TotalTokens          int64    `json:"total_tokens"`
	Cost                 float64  `json:"cost"`
	AvgLatencyMS         *float64 `json:"average_latency_ms"`
	LastSeenMS           int64    `json:"last_seen_ms"`
}

type FilterOptions struct {
	AccountStats     []AccountStatRow  `json:"account_stats,omitempty"`
	APIKeyStats      []APIKeyStatRow   `json:"api_key_stats,omitempty"`
	ChannelShare     []ChannelShareRow `json:"channel_share,omitempty"`
	ModelStats       []ModelStat       `json:"model_stats,omitempty"`
	Models           []string          `json:"models,omitempty"`
	APIKeyHashes     []string          `json:"api_key_hashes,omitempty"`
	Providers        []string          `json:"providers,omitempty"`
	AuthFiles        []string          `json:"auth_files,omitempty"`
	Accounts         []string          `json:"accounts,omitempty"`
	AccountCount     int               `json:"account_count,omitempty"`
	APIKeyCount      int               `json:"api_key_count,omitempty"`
	ProjectIDs       []string          `json:"project_ids,omitempty"`
	RequestTypes     []string          `json:"request_types,omitempty"`
	HeaderErrorKinds []string          `json:"header_error_kinds,omitempty"`
	HeaderErrorCodes []string          `json:"header_error_codes,omitempty"`
	HeaderQuotaPlans []string          `json:"header_quota_plans,omitempty"`
	HeaderTraceIDs   []string          `json:"header_trace_ids,omitempty"`
}

type TaskBucketRow struct {
	BucketKey           string   `json:"bucket_key"`
	Total               int64    `json:"total"`
	Success             int64    `json:"success"`
	Failure             int64    `json:"failure"`
	FirstMS             int64    `json:"first_ms"`
	LastMS              int64    `json:"last_ms"`
	Source              string   `json:"source"`
	SourceHash          string   `json:"source_hash"`
	AuthIndex           string   `json:"auth_index"`
	Models              []string `json:"models"`
	Endpoints           []string `json:"endpoints"`
	InputTokens         int64    `json:"input_tokens"`
	OutputTokens        int64    `json:"output_tokens"`
	CachedTokens        int64    `json:"cached_tokens"`
	CacheReadTokens     int64    `json:"cache_read_tokens"`
	CacheCreationTokens int64    `json:"cache_creation_tokens"`
	TotalTokens         int64    `json:"total_tokens"`
	AvgLatencyMS        *float64 `json:"average_latency_ms"`
	MaxLatencyMS        *int64   `json:"max_latency_ms"`
}

type RecentFailure struct {
	TimestampMS            int64                         `json:"timestamp_ms"`
	Model                  string                        `json:"model"`
	APIKeyHash             string                        `json:"api_key_hash"`
	Source                 string                        `json:"source,omitempty"`
	SourceHash             string                        `json:"source_hash"`
	AuthIndex              string                        `json:"auth_index"`
	AccountSnapshot        string                        `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot      string                        `json:"auth_label_snapshot,omitempty"`
	AuthProviderSnapshot   string                        `json:"auth_provider_snapshot,omitempty"`
	AuthProjectIDSnapshot  string                        `json:"auth_project_id_snapshot,omitempty"`
	Endpoint               string                        `json:"endpoint"`
	DurationMS             *int64                        `json:"duration_ms"`
	FailStatusCode         *int64                        `json:"fail_status_code,omitempty"`
	FailSummary            string                        `json:"fail_summary,omitempty"`
	ResponseMetadata       *usage.ResponseHeaderMetadata `json:"response_metadata,omitempty"`
	HeaderQuotaRecoverAtMS *int64                        `json:"header_quota_recover_at_ms,omitempty"`
	HeaderQuotaUsedPercent *float64                      `json:"header_quota_used_percent,omitempty"`
	HeaderQuotaPlanType    string                        `json:"header_quota_plan_type,omitempty"`
	HeaderErrorKind        string                        `json:"header_error_kind,omitempty"`
	HeaderErrorCode        string                        `json:"header_error_code,omitempty"`
	HeaderTraceID          string                        `json:"header_trace_id,omitempty"`
}

type HeaderSnapshot struct {
	EventHash              string                        `json:"event_hash"`
	TimestampMS            int64                         `json:"timestamp_ms"`
	AuthFileSnapshot       string                        `json:"auth_file_snapshot,omitempty"`
	AuthIndex              string                        `json:"auth_index,omitempty"`
	AccountSnapshot        string                        `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot      string                        `json:"auth_label_snapshot,omitempty"`
	AuthProviderSnapshot   string                        `json:"auth_provider_snapshot,omitempty"`
	AuthProjectIDSnapshot  string                        `json:"auth_project_id_snapshot,omitempty"`
	Source                 string                        `json:"source,omitempty"`
	SourceHash             string                        `json:"source_hash,omitempty"`
	ResponseMetadata       *usage.ResponseHeaderMetadata `json:"response_metadata,omitempty"`
	HeaderQuotaRecoverAtMS *int64                        `json:"header_quota_recover_at_ms,omitempty"`
	HeaderQuotaUsedPercent *float64                      `json:"header_quota_used_percent,omitempty"`
	HeaderQuotaPlanType    string                        `json:"header_quota_plan_type,omitempty"`
	HeaderErrorKind        string                        `json:"header_error_kind,omitempty"`
	HeaderErrorCode        string                        `json:"header_error_code,omitempty"`
	HeaderTraceID          string                        `json:"header_trace_id,omitempty"`
}

type EventsResponse struct {
	Items        []EventRow `json:"items"`
	NextBeforeMS int64      `json:"next_before_ms"`
	NextBeforeID int64      `json:"next_before_id"`
	HasMore      bool       `json:"has_more"`
	TotalCount   int64      `json:"total_count"`
}

type EventRow struct {
	RequestID              string                        `json:"request_id,omitempty"`
	EventHash              string                        `json:"event_hash"`
	TimestampMS            int64                         `json:"timestamp_ms"`
	Model                  string                        `json:"model"`
	ResolvedModel          string                        `json:"resolved_model,omitempty"`
	Endpoint               string                        `json:"endpoint"`
	Method                 string                        `json:"method"`
	Path                   string                        `json:"path"`
	AuthIndex              string                        `json:"auth_index"`
	Source                 string                        `json:"source"`
	SourceHash             string                        `json:"source_hash"`
	APIKeyHash             string                        `json:"api_key_hash"`
	AccountSnapshot        string                        `json:"account_snapshot"`
	AuthLabelSnapshot      string                        `json:"auth_label_snapshot"`
	AuthFileSnapshot       string                        `json:"auth_file_snapshot,omitempty"`
	AuthProviderSnapshot   string                        `json:"auth_provider_snapshot"`
	AuthProjectIDSnapshot  string                        `json:"auth_project_id_snapshot,omitempty"`
	ReasoningEffort        string                        `json:"reasoning_effort,omitempty"`
	ServiceTier            string                        `json:"service_tier,omitempty"`
	ExecutorType           string                        `json:"executor_type,omitempty"`
	InputTokens            int64                         `json:"input_tokens"`
	OutputTokens           int64                         `json:"output_tokens"`
	CachedTokens           int64                         `json:"cached_tokens"`
	CacheReadTokens        int64                         `json:"cache_read_tokens"`
	CacheCreationTokens    int64                         `json:"cache_creation_tokens"`
	ReasoningTokens        int64                         `json:"reasoning_tokens"`
	TotalTokens            int64                         `json:"total_tokens"`
	LatencyMS              *int64                        `json:"latency_ms"`
	TTFTMS                 *int64                        `json:"ttft_ms"`
	Failed                 bool                          `json:"failed"`
	FailStatusCode         *int64                        `json:"fail_status_code,omitempty"`
	FailSummary            string                        `json:"fail_summary,omitempty"`
	ResponseMetadata       *usage.ResponseHeaderMetadata `json:"response_metadata,omitempty"`
	HeaderQuotaRecoverAtMS *int64                        `json:"header_quota_recover_at_ms,omitempty"`
	HeaderQuotaUsedPercent *float64                      `json:"header_quota_used_percent,omitempty"`
	HeaderQuotaPlanType    string                        `json:"header_quota_plan_type,omitempty"`
	HeaderErrorKind        string                        `json:"header_error_kind,omitempty"`
	HeaderErrorCode        string                        `json:"header_error_code,omitempty"`
	HeaderTraceID          string                        `json:"header_trace_id,omitempty"`
}

func (s *Service) Analytics(ctx context.Context, req Request) (Response, error) {
	if req.FromMS <= 0 || req.ToMS <= 0 || req.FromMS >= req.ToMS {
		return Response{}, errors.New("from_ms and to_ms are required and from_ms must be less than to_ms")
	}
	nowMS := req.NowMS
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	granularity := normalizeGranularity(req.Include.Granularity, req.FromMS, req.ToMS)
	location, err := resolveAnalyticsLocation(req.TimeZone)
	if err != nil {
		return Response{}, err
	}
	filter := buildFilter(req)
	prices, err := s.store.LoadModelPrices(ctx)
	if err != nil {
		return Response{}, err
	}

	response := Response{
		GeneratedAtMS: time.Now().UnixMilli(),
		Granularity:   granularity,
	}

	// summaryTotalCalls caches the count(*) computed for the summary so the
	// events page can reuse it as total_count without a second table scan
	// (summary and events use the exact same filter).
	var summaryTotalCalls int64
	summaryComputed := false
	compactSummary := req.Include.Summary && req.Include.SummaryProfile == "compact"
	rollupEligible := analyticsHourlyRollupEligible(filter)
	needsHourlyAggregates := req.Include.Summary || req.Include.ModelShare || req.Include.ModelStats
	needsHourlyTimeline := req.Include.Timeline || req.Include.AnomalyPoints
	hourlyTimelineRepresentable := rollupEligible && needsHourlyTimeline && s.hourlyReader.CanRepresentAnalyticsTimeline(req.FromMS, req.ToMS, granularity, location)
	needsHourlyCore := needsHourlyAggregates || hourlyTimelineRepresentable
	var hourlySnapshot usagehourly.Snapshot
	hourlySnapshotAvailable := false
	if rollupEligible && needsHourlyCore {
		hourlySnapshot, hourlySnapshotAvailable = s.hourlyReader.LoadAnalytics(
			ctx,
			filter,
			granularity,
			location,
			hourlyTimelineRepresentable,
		)
	}

	var modelStats []store.ModelStat
	var channelStats []store.ChannelModelStat
	var accountStats []store.AccountModelStat
	var apiKeyStats []store.APIKeyModelStat
	needsModelStats := req.Include.Summary || req.Include.ModelShare || req.Include.ModelStats
	if needsModelStats {
		if hourlySnapshotAvailable {
			modelStats = hourlySnapshot.ModelStats
		} else {
			modelStats, err = s.store.ModelStatsWithFilter(ctx, filter, 0)
			if err != nil {
				return Response{}, err
			}
		}
	}

	queries := newAnalyticsQueryGroup(ctx, analyticsPrefetchConcurrency)
	defer queries.Close()

	var latencyPercentiles []store.LatencyPercentiles
	if req.Include.Timeline || req.Include.AnomalyPoints {
		queries.Go(func(queryCtx context.Context) error {
			var queryErr error
			latencyPercentiles, queryErr = s.store.LatencyPercentilesWithFilter(queryCtx, filter, granularity, location)
			return queryErr
		})
	}

	var hourlyDistributionPoints []store.HourlyPoint
	if req.Include.HourlyDistribution {
		queries.Go(func(queryCtx context.Context) error {
			var queryErr error
			hourlyDistributionPoints, queryErr = s.store.HourlyDistributionWithFilter(queryCtx, filter, location)
			return queryErr
		})
	}

	var heatmapPoints []store.HeatmapPoint
	if req.Include.Heatmap {
		queries.Go(func(queryCtx context.Context) error {
			var queryErr error
			heatmapPoints, queryErr = s.store.HeatmapWithFilter(queryCtx, filter, location)
			return queryErr
		})
	}

	if req.Include.ChannelShare {
		queries.Go(func(queryCtx context.Context) error {
			var queryErr error
			channelStats, queryErr = s.store.ChannelModelStatsWithFilter(queryCtx, filter)
			return queryErr
		})
	}

	var failureSourceStats []store.FailureSourceStat
	if req.Include.FailureSources {
		queries.Go(func(queryCtx context.Context) error {
			var queryErr error
			failureSourceStats, queryErr = s.store.FailureSourcesWithFilter(queryCtx, filter)
			return queryErr
		})
	}

	if req.Include.AccountStats {
		queries.Go(func(queryCtx context.Context) error {
			var queryErr error
			accountStats, queryErr = s.store.AccountModelStatsWithFilter(queryCtx, filter)
			return queryErr
		})
	}

	var credentialStats []store.CredentialModelStat
	if req.Include.CredentialStats {
		queries.Go(func(queryCtx context.Context) error {
			var queryErr error
			credentialStats, queryErr = s.store.CredentialModelStatsWithFilter(queryCtx, filter)
			return queryErr
		})
	}

	var credentialTimelinePoints []store.CredentialTimelinePoint
	if req.Include.CredentialTimeline {
		queries.Go(func(queryCtx context.Context) error {
			var queryErr error
			credentialTimelinePoints, queryErr = s.store.CredentialTimelineWithFilter(queryCtx, filter, granularity, location)
			return queryErr
		})
	}

	var apiKeyTimelinePoints []store.APIKeyTimelinePoint
	if req.Include.APIKeyTimeline {
		queries.Go(func(queryCtx context.Context) error {
			var queryErr error
			apiKeyTimelinePoints, queryErr = s.store.APIKeyTimelineWithFilter(queryCtx, filter, granularity, location)
			return queryErr
		})
	}

	if req.Include.APIKeyStats {
		queries.Go(func(queryCtx context.Context) error {
			var queryErr error
			apiKeyStats, queryErr = s.store.APIKeyModelStatsWithFilter(queryCtx, filter)
			return queryErr
		})
	}

	var selectorOptions *FilterOptions
	var prebuiltFilterOptions *FilterOptions
	var filterOptionValues store.FilterOptionValues
	var filterOptionValuesAvailable bool
	if req.Include.FilterSelectors {
		queries.Go(func(queryCtx context.Context) error {
			var queryErr error
			selectorOptions, queryErr = s.filterSelectors(queryCtx, filter)
			return queryErr
		})
	} else if req.Include.FilterOptions {
		if filterOptionsMatchMainScope(filter) {
			queries.Go(func(queryCtx context.Context) error {
				var queryErr error
				filterOptionValues, queryErr = s.store.FilterOptionValuesWithFilter(queryCtx, filterOptionsBaseFilter(filter))
				filterOptionValuesAvailable = queryErr == nil
				return queryErr
			})
		} else {
			queries.Go(func(queryCtx context.Context) error {
				var queryErr error
				prebuiltFilterOptions, queryErr = s.filterOptions(queryCtx, filter, prices, filterOptionStats{})
				return queryErr
			})
		}
	}

	var recentFailureRows []store.RecentFailure
	if req.Include.RecentFailures > 0 {
		queries.Go(func(queryCtx context.Context) error {
			var queryErr error
			recentFailureRows, queryErr = s.store.RecentFailuresWithFilter(queryCtx, filter, req.Include.RecentFailures)
			return queryErr
		})
	}

	var eventsPage store.EventsPage
	if req.Include.EventsPage != nil {
		limit := req.Include.EventsPage.Limit
		if limit <= 0 {
			limit = defaultEventsLimit
		}
		if limit > maxEventsLimit {
			limit = maxEventsLimit
		}
		beforeMS := int64(0)
		if req.Include.EventsPage.BeforeMS != nil {
			beforeMS = *req.Include.EventsPage.BeforeMS
		}
		beforeID := int64(0)
		if req.Include.EventsPage.BeforeID != nil {
			beforeID = *req.Include.EventsPage.BeforeID
		}
		queries.Go(func(queryCtx context.Context) error {
			var queryErr error
			eventsPage, queryErr = s.store.EventsPageWithFilter(queryCtx, filter, beforeMS, beforeID, limit)
			return queryErr
		})
	}

	var taskBuckets []store.TaskBucket
	if req.Include.TaskBuckets || (req.Include.Summary && !compactSummary) {
		taskBuckets, err = s.store.TaskBucketsWithFilter(ctx, filter)
		if err != nil {
			return Response{}, err
		}
	}

	if req.Include.Summary {
		var agg store.Aggregate
		if hourlySnapshotAvailable {
			agg = hourlySnapshot.Aggregate
		} else {
			agg, err = s.store.AggregateWithFilter(ctx, filter)
			if err != nil {
				return Response{}, err
			}
		}
		var latencySummary store.LatencySummary
		if !compactSummary || req.Include.SummaryPercentiles {
			latencySummary, err = s.store.LatencySummaryWithFilter(ctx, filter)
			if err != nil {
				return Response{}, err
			}
		}
		var rollingAgg store.Aggregate
		var activeDays int64
		var zeroTokenModels []string
		if !compactSummary {
			rollingFilter := filter
			rollingFilter.FromMS = nowMS - recentWindowMS
			rollingFilter.ToMS = nowMS
			rollingAgg, err = s.store.AggregateWithFilter(ctx, rollingFilter)
			if err != nil {
				return Response{}, err
			}
			activeDays, err = s.store.ActiveDaysWithFilter(ctx, filter, location)
			if err != nil {
				return Response{}, err
			}
			zeroTokenModels, err = s.store.ZeroTokenModelsWithFilter(ctx, filter)
			if err != nil {
				return Response{}, err
			}
		}
		summaryTaskBuckets := taskBuckets
		if compactSummary {
			summaryTaskBuckets = nil
		}
		response.Summary = buildSummary(agg, latencySummary, rollingAgg, activeDays, modelStats, summaryTaskBuckets, prices, zeroTokenModels)
		if compactSummary {
			clearFullSummaryMetrics(response.Summary)
		}
		summaryTotalCalls = agg.TotalCalls
		summaryComputed = true

		// Period-over-period comparison reuses the same filter over the
		// immediately preceding window [FromMS-window, FromMS). Gated behind an
		// explicit flag so other analytics consumers avoid the extra queries.
		if req.Include.SummaryComparison {
			windowMS := req.ToMS - req.FromMS
			if prevFrom := req.FromMS - windowMS; prevFrom > 0 {
				prevFilter := filter
				prevFilter.FromMS = prevFrom
				prevFilter.ToMS = req.FromMS
				var prevAgg store.Aggregate
				var prevModelStats []store.ModelStat
				var prevSnapshot usagehourly.Snapshot
				prevSnapshotAvailable := false
				if rollupEligible {
					prevSnapshot, prevSnapshotAvailable = s.hourlyReader.LoadAnalytics(
						ctx,
						prevFilter,
						granularity,
						location,
						false,
					)
				}
				if prevSnapshotAvailable {
					prevAgg = prevSnapshot.Aggregate
					prevModelStats = prevSnapshot.ModelStats
				} else {
					prevAgg, err = s.store.AggregateWithFilter(ctx, prevFilter)
					if err != nil {
						return Response{}, err
					}
					prevModelStats, err = s.store.ModelStatsWithFilter(ctx, prevFilter, 0)
					if err != nil {
						return Response{}, err
					}
				}
				response.SummaryComparison = &SummaryComparison{
					FromMS:       prevFrom,
					ToMS:         req.FromMS,
					TotalCalls:   prevAgg.TotalCalls,
					SuccessCalls: prevAgg.SuccessCalls,
					FailureCalls: prevAgg.FailureCalls,
					SuccessRate:  ratio(prevAgg.SuccessCalls, prevAgg.TotalCalls),
					TotalTokens:  prevAgg.TotalTokens,
					TotalCost:    sumCost(prevModelStats, prices),
				}
			}
		}
	}
	if err := queries.Wait(); err != nil {
		return Response{}, err
	}
	var timeline []TimelinePoint
	if req.Include.Timeline || req.Include.AnomalyPoints {
		var points []store.TimelinePoint
		pointsAvailable := false
		if hourlySnapshotAvailable && hourlyTimelineRepresentable {
			points, pointsAvailable = s.hourlyReader.AnalyticsTimeline(ctx, hourlySnapshot, granularity, location)
		}
		if !pointsAvailable {
			points, err = s.store.TimelineWithFilter(ctx, filter, granularity, location)
			if err != nil {
				return Response{}, err
			}
		}
		timeline = buildTimeline(points, latencyPercentiles, granularity, location, prices)
		if req.Include.Timeline {
			response.Timeline = timeline
		}
		if req.Include.AnomalyPoints {
			response.AnomalyPoints = buildAnomalyPoints(timeline, granularity)
		}
	}
	if req.Include.HourlyDistribution {
		response.HourlyDistribution = buildHourly(hourlyDistributionPoints)
	}
	if req.Include.Heatmap {
		response.Heatmap = buildHeatmap(heatmapPoints, prices)
	}
	if req.Include.ModelShare {
		response.ModelShare = buildModelShare(modelStats, prices)
	}
	if req.Include.ModelStats {
		response.ModelStats = buildModelStats(modelStats, prices)
	}
	if req.Include.ChannelShare {
		response.ChannelShare = buildChannelShare(channelStats, prices)
	}
	if req.Include.FailureSources {
		response.FailureSources = buildFailureSources(failureSourceStats)
	}
	if req.Include.AccountStats {
		response.AccountStats = buildAccountStats(accountStats, prices)
	}
	if req.Include.CredentialStats {
		response.CredentialStats = buildCredentialStats(credentialStats, prices)
	}
	if req.Include.CredentialTimeline {
		response.CredentialTimeline = buildCredentialTimeline(credentialTimelinePoints, granularity, location, prices)
	}
	if req.Include.APIKeyTimeline {
		response.APIKeyTimeline = buildAPIKeyTimeline(apiKeyTimelinePoints, granularity, location, prices)
	}
	if req.Include.APIKeyStats {
		response.APIKeyStats = buildAPIKeyStats(apiKeyStats, prices)
	}
	if req.Include.FilterSelectors {
		response.FilterOptions = selectorOptions
	} else if req.Include.FilterOptions {
		if prebuiltFilterOptions != nil {
			response.FilterOptions = prebuiltFilterOptions
		} else {
			reuse := filterOptionStats{
				accountStats:          accountStats,
				accountStatsAvailable: req.Include.AccountStats,
				apiKeyStats:           apiKeyStats,
				apiKeyStatsAvailable:  req.Include.APIKeyStats,
				channelStats:          channelStats,
				channelStatsAvailable: req.Include.ChannelShare,
				modelStats:            modelStats,
				modelStatsAvailable:   needsModelStats,
				optionValues:          filterOptionValues,
				optionValuesAvailable: filterOptionValuesAvailable,
			}
			options, err := s.filterOptions(ctx, filter, prices, reuse)
			if err != nil {
				return Response{}, err
			}
			if filterOptionsMatchMainScope(filter) {
				if req.Include.AccountStats {
					options.AccountStats = response.AccountStats
				}
				if req.Include.APIKeyStats {
					options.APIKeyStats = response.APIKeyStats
				}
				if req.Include.ChannelShare {
					options.ChannelShare = response.ChannelShare
				}
				if req.Include.ModelStats {
					options.ModelStats = response.ModelStats
				}
			}
			response.FilterOptions = options
		}
	}
	if req.Include.TaskBuckets {
		response.TaskBuckets = buildTaskBuckets(taskBuckets)
	}
	if req.Include.RecentFailures > 0 {
		response.RecentFailures = buildRecentFailures(recentFailureRows)
	}
	if req.Include.EventsPage != nil {
		// total_count is the real number of events matching the current filter
		// (time range + scope filters + search), independent of the pagination
		// cursor. Reuse the summary aggregate count when it was already computed
		// for this same filter to avoid a second scan; otherwise run a
		// lightweight count(*).
		total := summaryTotalCalls
		if !summaryComputed {
			total, err = s.store.EventsCountWithFilter(ctx, filter)
			if err != nil {
				return Response{}, err
			}
		}
		response.Events = buildEvents(eventsPage, total)
	}
	if req.Include.DrilldownPreview != nil {
		preview := req.Include.DrilldownPreview
		if preview.FromMS > 0 && preview.ToMS > preview.FromMS {
			previewFilter := filter
			previewFilter.FromMS = preview.FromMS
			previewFilter.ToMS = preview.ToMS
			limit := preview.Limit
			if limit <= 0 {
				limit = defaultDrilldownLimit
			}
			if limit > maxDrilldownLimit {
				limit = maxDrilldownLimit
			}
			page, err := s.store.EventsPageWithFilter(ctx, previewFilter, 0, 0, limit)
			if err != nil {
				return Response{}, err
			}
			response.DrilldownPreview = buildEvents(page, int64(len(page.Items)))
		}
	}

	return response, nil
}

func (s *Service) AccountHistory(ctx context.Context, req AccountHistoryRequest) (AccountHistoryResponse, error) {
	if len(req.Accounts) == 0 {
		return AccountHistoryResponse{}, errors.New("accounts are required")
	}
	if len(req.Accounts) > maxAccountHistoryTargets {
		return AccountHistoryResponse{}, fmt.Errorf("accounts must be less than or equal to %d", maxAccountHistoryTargets)
	}
	generatedAtMS := time.Now().UnixMilli()
	processed := 0
	if req.CatchUp {
		result, err := s.store.CatchUpAccountHistoryRollups(ctx, accountHistoryCatchUpLimit, generatedAtMS)
		if err != nil {
			return AccountHistoryResponse{}, err
		}
		processed = result.Processed
	}
	checkpoint, err := s.store.AccountHistoryRollupCheckpoint(ctx)
	if err != nil {
		return AccountHistoryResponse{}, err
	}
	latestID, err := s.store.LatestUsageEventID(ctx)
	if err != nil {
		return AccountHistoryResponse{}, err
	}

	keys := make([]string, 0, len(req.Accounts))
	targetKeys := make([]string, len(req.Accounts))
	validTargets := make([]bool, len(req.Accounts))
	for index, account := range req.Accounts {
		key, valid := accountHistoryTargetKey(account)
		targetKeys[index] = key
		validTargets[index] = valid
		if valid {
			keys = append(keys, key)
		}
	}
	rows, err := s.store.AccountHistoryRollupRows(ctx, keys)
	if err != nil {
		return AccountHistoryResponse{}, err
	}
	prices, err := s.store.LoadModelPrices(ctx)
	if err != nil {
		return AccountHistoryResponse{}, err
	}
	totals := buildAccountHistoryTotals(rows, prices)
	pending := latestID > checkpoint.LastEventID
	items := make([]AccountHistoryItem, 0, len(req.Accounts))
	for index := range req.Accounts {
		key := targetKeys[index]
		if !validTargets[index] {
			items = append(items, AccountHistoryItem{
				AccountKey: key,
				Matched:    false,
				SyncStatus: accountHistorySyncStatus(false, false),
			})
			continue
		}
		total := totals[key]
		if total == nil {
			items = append(items, AccountHistoryItem{
				AccountKey: key,
				Matched:    false,
				SyncStatus: accountHistorySyncStatus(false, pending),
			})
			continue
		}
		var successRate *float64
		if total.requests > 0 {
			value := ratio(total.successCalls, total.requests)
			successRate = &value
		}
		items = append(items, AccountHistoryItem{
			AccountKey:    key,
			Matched:       true,
			TotalRequests: total.requests,
			SuccessCalls:  total.successCalls,
			FailureCalls:  total.failureCalls,
			TotalTokens:   total.totalTokens,
			TotalCost:     total.cost,
			SuccessRate:   successRate,
			FirstSeenMS:   nullableMSPointer(total.firstSeenMS),
			LastSeenMS:    nullableMSPointer(total.lastSeenMS),
			SyncStatus:    accountHistorySyncStatus(true, pending),
		})
	}

	return AccountHistoryResponse{
		GeneratedAtMS: generatedAtMS,
		Checkpoint: AccountHistoryCheckpointState{
			LastEventID: checkpoint.LastEventID,
			LatestID:    latestID,
			Pending:     pending,
			Processed:   processed,
		},
		Items: items,
	}, nil
}

func (s *Service) HeaderSnapshots(ctx context.Context, req HeaderSnapshotsRequest) (HeaderSnapshotsResponse, error) {
	days := req.Days
	if days <= 0 {
		days = defaultHeaderSnapshotDays
	}
	if days > maxHeaderSnapshotDays {
		days = maxHeaderSnapshotDays
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultHeaderSnapshotLimit
	}
	if limit > maxHeaderSnapshotLimit {
		limit = maxHeaderSnapshotLimit
	}
	nowMS := time.Now().UnixMilli()
	fromMS := nowMS - int64(days)*24*60*60*1000
	items, err := s.store.LatestHeaderSnapshots(ctx, fromMS, limit)
	if err != nil {
		return HeaderSnapshotsResponse{}, err
	}
	return HeaderSnapshotsResponse{
		GeneratedAtMS: nowMS,
		FromMS:        fromMS,
		ToMS:          nowMS,
		Items:         buildHeaderSnapshots(items),
	}, nil
}

func buildFilter(req Request) store.AnalyticsFilter {
	includeFailed := true
	if req.Filters.IncludeFailed != nil {
		includeFailed = *req.Filters.IncludeFailed
	}
	return store.AnalyticsFilter{
		FromMS:           req.FromMS,
		ToMS:             req.ToMS,
		SearchQuery:      req.SearchQuery,
		SearchAPIKeyHash: req.SearchAPIKeyHash,
		Models:           req.Filters.Models,
		Providers:        req.Filters.Providers,
		Accounts:         req.Filters.Accounts,
		CredentialIDs:    req.Filters.CredentialIDs,
		AuthFiles:        req.Filters.AuthFiles,
		AuthIndices:      req.Filters.AuthIndices,
		APIKeyHashes:     req.Filters.APIKeyHashes,
		SourceHashes:     req.Filters.SourceHashes,
		ProjectIDs:       req.Filters.ProjectIDs,
		RequestTypes:     req.Filters.RequestTypes,
		HeaderErrorKinds: req.Filters.HeaderErrorKinds,
		HeaderErrorCodes: req.Filters.HeaderErrorCodes,
		HeaderQuotaPlans: req.Filters.HeaderQuotaPlans,
		HeaderTraceIDs:   req.Filters.HeaderTraceIDs,
		IncludeFailed:    includeFailed,
		FailedOnly:       req.Filters.FailedOnly,
		MinLatencyMS:     req.Filters.MinLatencyMS,
		CacheStatus:      req.Filters.CacheStatus,
	}
}

func analyticsHourlyRollupEligible(filter store.AnalyticsFilter) bool {
	return usagehourly.SupportsAnalyticsFilter(filter)
}

type filterOptionStats struct {
	accountStats          []store.AccountModelStat
	accountStatsAvailable bool
	apiKeyStats           []store.APIKeyModelStat
	apiKeyStatsAvailable  bool
	channelStats          []store.ChannelModelStat
	channelStatsAvailable bool
	modelStats            []store.ModelStat
	modelStatsAvailable   bool
	optionValues          store.FilterOptionValues
	optionValuesAvailable bool
}

func (s *Service) filterOptions(
	ctx context.Context,
	filter store.AnalyticsFilter,
	prices map[string]store.ModelPrice,
	reuse filterOptionStats,
) (*FilterOptions, error) {
	optionFilter := filterOptionsBaseFilter(filter)

	accountStats := reuse.accountStats
	if !reuse.accountStatsAvailable {
		var err error
		accountStats, err = s.store.AccountModelStatsWithFilter(ctx, optionFilter)
		if err != nil {
			return nil, err
		}
	}
	apiKeyStats := reuse.apiKeyStats
	if !reuse.apiKeyStatsAvailable {
		var err error
		apiKeyStats, err = s.store.APIKeyModelStatsWithFilter(ctx, optionFilter)
		if err != nil {
			return nil, err
		}
	}
	channelStats := reuse.channelStats
	if !reuse.channelStatsAvailable {
		var err error
		channelStats, err = s.store.ChannelModelStatsWithFilter(ctx, optionFilter)
		if err != nil {
			return nil, err
		}
	}
	modelStats := reuse.modelStats
	if !reuse.modelStatsAvailable {
		var err error
		modelStats, err = s.store.ModelStatsWithFilter(ctx, optionFilter, 0)
		if err != nil {
			return nil, err
		}
	}
	optionValues := reuse.optionValues
	if !reuse.optionValuesAvailable {
		var err error
		optionValues, err = s.store.FilterOptionValuesWithFilter(ctx, optionFilter)
		if err != nil {
			return nil, err
		}
	}

	return &FilterOptions{
		AccountStats:     buildAccountStats(accountStats, prices),
		APIKeyStats:      buildAPIKeyStats(apiKeyStats, prices),
		ChannelShare:     buildChannelShare(channelStats, prices),
		ModelStats:       buildModelStats(modelStats, prices),
		Providers:        optionValues.Providers,
		AuthFiles:        optionValues.AuthFiles,
		ProjectIDs:       optionValues.ProjectIDs,
		RequestTypes:     optionValues.RequestTypes,
		HeaderErrorKinds: optionValues.HeaderErrorKinds,
		HeaderErrorCodes: optionValues.HeaderErrorCodes,
		HeaderQuotaPlans: optionValues.HeaderQuotaPlans,
		HeaderTraceIDs:   optionValues.HeaderTraceIDs,
	}, nil
}

func filterOptionsMatchMainScope(filter store.AnalyticsFilter) bool {
	return len(filter.Models) == 0 &&
		len(filter.Providers) == 0 &&
		len(filter.Accounts) == 0 &&
		len(filter.CredentialIDs) == 0 &&
		len(filter.AuthFiles) == 0 &&
		len(filter.AuthIndices) == 0 &&
		len(filter.APIKeyHashes) == 0 &&
		len(filter.SourceHashes) == 0 &&
		len(filter.ProjectIDs) == 0 &&
		len(filter.RequestTypes) == 0 &&
		len(filter.HeaderErrorKinds) == 0 &&
		len(filter.HeaderErrorCodes) == 0 &&
		len(filter.HeaderQuotaPlans) == 0 &&
		len(filter.HeaderTraceIDs) == 0 &&
		filter.IncludeFailed &&
		!filter.FailedOnly &&
		filter.MinLatencyMS == 0 &&
		strings.TrimSpace(filter.CacheStatus) == ""
}

func (s *Service) filterSelectors(ctx context.Context, filter store.AnalyticsFilter) (*FilterOptions, error) {
	values, err := s.store.FilterSelectorValuesWithFilter(ctx, filterOptionsBaseFilter(filter))
	if err != nil {
		return nil, err
	}
	accountStats := buildAccountSelectorStats(values)
	return &FilterOptions{
		Models:       values.Models,
		APIKeyHashes: values.APIKeyHashes,
		Providers:    values.Providers,
		AuthFiles:    values.AuthFiles,
		Accounts:     values.Accounts,
		AccountStats: accountStats,
		AccountCount: len(accountStats),
		APIKeyCount:  countAPIKeySelectors(values),
	}, nil
}

func countAPIKeySelectors(values store.FilterSelectorValues) int {
	groups := make(map[string]struct{}, len(values.APIKeySelectors))
	for _, selector := range values.APIKeySelectors {
		groups[apiKeyGroupKey(
			selector.APIKeyHash,
			selector.SourceHash,
			selector.AuthIndex,
			selector.Source,
			selector.AuthProviderSnapshot,
		)] = struct{}{}
	}
	return len(groups)
}

func buildAccountSelectorStats(values store.FilterSelectorValues) []AccountStatRow {
	grouped := map[string]*accountStatAccumulator{}
	for _, selector := range values.AccountSelectors {
		id := accountGroupKey(
			selector.AccountSnapshot,
			selector.AuthLabelSnapshot,
			selector.Source,
			selector.AuthIndex,
		)
		if id == "-" && strings.TrimSpace(selector.SourceHash) == "" {
			continue
		}
		entry := grouped[id]
		if entry == nil {
			entry = &accountStatAccumulator{
				row: AccountStatRow{
					ID:                   id,
					AccountSnapshot:      selector.AccountSnapshot,
					AuthLabelSnapshot:    selector.AuthLabelSnapshot,
					AuthProviderSnapshot: selector.AuthProviderSnapshot,
					SuccessRate:          1,
				},
				authIndices:  map[string]struct{}{},
				sources:      map[string]struct{}{},
				sourceHashes: map[string]struct{}{},
			}
			grouped[id] = entry
		}
		fillAccountStatSnapshots(
			&entry.row,
			selector.AccountSnapshot,
			selector.AuthLabelSnapshot,
			selector.AuthProviderSnapshot,
		)
		addSetValue(entry.authIndices, selector.AuthIndex)
		addSetValue(entry.sources, selector.Source)
		addSetValue(entry.sourceHashes, selector.SourceHash)
	}

	result := make([]AccountStatRow, 0, len(grouped))
	for _, entry := range grouped {
		entry.row.AuthIndices = sortedSetValues(entry.authIndices)
		entry.row.Sources = sortedSetValues(entry.sources)
		entry.row.SourceHashes = sortedSetValues(entry.sourceHashes)
		result = append(result, entry.row)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})
	return result
}

func filterOptionsBaseFilter(filter store.AnalyticsFilter) store.AnalyticsFilter {
	optionFilter := filter
	optionFilter.Models = nil
	optionFilter.Providers = nil
	optionFilter.Accounts = nil
	optionFilter.CredentialIDs = nil
	optionFilter.AuthFiles = nil
	optionFilter.AuthIndices = nil
	optionFilter.APIKeyHashes = nil
	optionFilter.SourceHashes = nil
	optionFilter.ProjectIDs = nil
	optionFilter.RequestTypes = nil
	optionFilter.HeaderErrorKinds = nil
	optionFilter.HeaderErrorCodes = nil
	optionFilter.HeaderQuotaPlans = nil
	optionFilter.HeaderTraceIDs = nil
	optionFilter.IncludeFailed = true
	optionFilter.FailedOnly = false
	optionFilter.MinLatencyMS = 0
	optionFilter.CacheStatus = ""
	return optionFilter
}

func normalizeGranularity(input string, fromMS int64, toMS int64) string {
	if input == "day" || input == "hour" {
		return input
	}
	if toMS-fromMS <= 24*60*60*1000 {
		return "hour"
	}
	return "day"
}

func resolveAnalyticsLocation(timeZone string) (*time.Location, error) {
	trimmed := strings.TrimSpace(timeZone)
	if trimmed == "" {
		return time.UTC, nil
	}
	location, err := time.LoadLocation(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid time zone: %s", trimmed)
	}
	return location, nil
}

func buildSummary(agg store.Aggregate, latencySummary store.LatencySummary, rolling store.Aggregate, activeDays int64, modelStats []store.ModelStat, taskBuckets []store.TaskBucket, prices map[string]store.ModelPrice, zeroTokenModels []string) *Summary {
	dayCount := activeDays
	if dayCount <= 0 {
		dayCount = 1
	}
	taskFailures := int64(0)
	for _, bucket := range taskBuckets {
		if bucket.Failure > 0 {
			taskFailures++
		}
	}
	approxTasks := int64(len(taskBuckets))
	totalCost := sumCost(modelStats, prices)
	return &Summary{
		TotalCalls:            agg.TotalCalls,
		SuccessCalls:          agg.SuccessCalls,
		FailureCalls:          agg.FailureCalls,
		SuccessRate:           ratio(agg.SuccessCalls, agg.TotalCalls),
		InputTokens:           agg.InputTokens,
		OutputTokens:          agg.OutputTokens,
		CachedTokens:          agg.CachedTokens,
		CacheReadTokens:       agg.CacheReadTokens,
		CacheCreationTokens:   agg.CacheCreationTokens,
		CacheHitRate:          cacheHitRateForModelStats(modelStats),
		ReasoningTokens:       agg.ReasoningTokens,
		TotalTokens:           agg.TotalTokens,
		TotalCost:             totalCost,
		AverageCostPerCall:    ratioFloat(totalCost, agg.TotalCalls),
		AverageLatencyMS:      nullableFloat(agg.AvgLatencyMS.Valid, agg.AvgLatencyMS.Float64),
		P95LatencyMS:          nullableFloat(latencySummary.P95LatencyMS.Valid, latencySummary.P95LatencyMS.Float64),
		P95TTFTMS:             nullableFloat(latencySummary.P95TTFTMS.Valid, latencySummary.P95TTFTMS.Float64),
		ZeroTokenCalls:        agg.ZeroTokenCalls,
		RPM30M:                float64(rolling.TotalCalls) / 30,
		TPM30M:                float64(rolling.TotalTokens) / 30,
		AvgDailyRequests:      float64(agg.TotalCalls) / float64(dayCount),
		AvgDailyTokens:        float64(agg.TotalTokens) / float64(dayCount),
		ApproxTasks:           approxTasks,
		ApproxTaskFailures:    taskFailures,
		ApproxTaskSuccessRate: ratio(approxTasks-taskFailures, approxTasks),
		ZeroTokenModels:       zeroTokenModels,
	}
}

func clearFullSummaryMetrics(summary *Summary) {
	if summary == nil {
		return
	}
	summary.RPM30M = 0
	summary.TPM30M = 0
	summary.AvgDailyRequests = 0
	summary.AvgDailyTokens = 0
	summary.ApproxTasks = 0
	summary.ApproxTaskFailures = 0
	summary.ApproxTaskSuccessRate = 0
	summary.ZeroTokenModels = nil
}

func buildTimeline(points []store.TimelinePoint, percentiles []store.LatencyPercentiles, granularity string, location *time.Location, prices map[string]store.ModelPrice) []TimelinePoint {
	type bucketAccumulator struct {
		point               TimelinePoint
		cacheHitTokens      int64
		cacheHitInputTokens int64
		latencyTotal        float64
		latencySample       int64
	}
	orderedPoints := append([]store.TimelinePoint(nil), points...)
	sort.SliceStable(orderedPoints, func(i, j int) bool {
		if orderedPoints[i].BucketMS != orderedPoints[j].BucketMS {
			return orderedPoints[i].BucketMS < orderedPoints[j].BucketMS
		}
		if orderedPoints[i].Model != orderedPoints[j].Model {
			return orderedPoints[i].Model < orderedPoints[j].Model
		}
		if orderedPoints[i].BillingModel != orderedPoints[j].BillingModel {
			return orderedPoints[i].BillingModel < orderedPoints[j].BillingModel
		}
		return orderedPoints[i].ServiceTier < orderedPoints[j].ServiceTier
	})

	buckets := make(map[int64]*bucketAccumulator, len(orderedPoints))
	order := make([]int64, 0, len(points))
	for _, point := range orderedPoints {
		bucket := buckets[point.BucketMS]
		if bucket == nil {
			bucket = &bucketAccumulator{
				point: TimelinePoint{
					BucketMS: point.BucketMS,
					Label:    timelineLabel(point.BucketMS, granularity, location),
				},
			}
			buckets[point.BucketMS] = bucket
			order = append(order, point.BucketMS)
		}
		bucket.point.Calls += point.Calls
		bucket.point.Tokens += point.Tokens
		bucket.point.TotalTokens += point.Tokens
		bucket.point.Success += point.Success
		bucket.point.Failure += point.Failure
		bucket.point.InputTokens += point.InputTokens
		bucket.point.OutputTokens += point.OutputTokens
		bucket.point.CachedTokens += point.CachedTokens
		bucket.point.CacheReadTokens += point.CacheReadTokens
		bucket.point.CacheCreationTokens += point.CacheCreationTokens
		bucket.point.ReasoningTokens += point.ReasoningTokens
		bucket.point.Cost += costForTimelinePoint(point, prices)
		behaviorModel := point.BillingModel
		if strings.TrimSpace(behaviorModel) == "" {
			behaviorModel = point.Model
		}
		cacheHitTokens, cacheHitInputTokens := usage.CacheHitTotals(
			behaviorModel,
			point.InputTokens,
			point.CachedTokens,
			point.CacheReadTokens,
			point.CacheCreationTokens,
		)
		bucket.cacheHitTokens += cacheHitTokens
		bucket.cacheHitInputTokens += cacheHitInputTokens
		if point.AvgLatencyMS.Valid && point.LatencySamples > 0 {
			bucket.latencyTotal += point.AvgLatencyMS.Float64 * float64(point.LatencySamples)
			bucket.latencySample += point.LatencySamples
		}
	}
	result := make([]TimelinePoint, 0, len(order))
	for _, bucketMS := range order {
		bucket := buckets[bucketMS]
		if bucket.latencySample > 0 {
			value := bucket.latencyTotal / float64(bucket.latencySample)
			bucket.point.AvgLatencyMS = &value
		}
		bucket.point.SuccessRate = ratio(bucket.point.Success, bucket.point.Calls)
		bucket.point.FailureRate = ratio(bucket.point.Failure, bucket.point.Calls)
		bucket.point.CacheHitRate = usage.CacheHitRateFromTotals(bucket.cacheHitTokens, bucket.cacheHitInputTokens)
		result = append(result, bucket.point)
	}
	percentilesByBucket := make(map[int64]store.LatencyPercentiles, len(percentiles))
	for _, point := range percentiles {
		percentilesByBucket[point.BucketMS] = point
	}
	for index := range result {
		if point, ok := percentilesByBucket[result[index].BucketMS]; ok {
			result[index].P95LatencyMS = nullableFloat(point.P95LatencyMS.Valid, point.P95LatencyMS.Float64)
			result[index].P95TTFTMS = nullableFloat(point.P95TTFTMS.Valid, point.P95TTFTMS.Float64)
		}
	}
	return result
}

func buildHourly(points []store.HourlyPoint) []HourlyPoint {
	result := make([]HourlyPoint, 0, len(points))
	for _, point := range points {
		result = append(result, HourlyPoint(point))
	}
	return result
}

const heatmapContributorLimit = 5

type heatmapAccumulator struct {
	point     *HeatmapPoint
	models    map[string]*HeatmapContributor
	apiKeys   map[string]*HeatmapContributor
	providers map[string]*HeatmapContributor
}

func newHeatmapAccumulator(point store.HeatmapPoint) *heatmapAccumulator {
	return &heatmapAccumulator{
		point: &HeatmapPoint{
			Weekday: point.Weekday,
			Hour:    point.Hour,
		},
		models:    map[string]*HeatmapContributor{},
		apiKeys:   map[string]*HeatmapContributor{},
		providers: map[string]*HeatmapContributor{},
	}
}

func buildHeatmap(points []store.HeatmapPoint, prices map[string]store.ModelPrice) []HeatmapPoint {
	type key struct {
		weekday int
		hour    int
	}
	grouped := map[key]*heatmapAccumulator{}
	order := make([]key, 0)
	for _, point := range points {
		mapKey := key{weekday: point.Weekday, hour: point.Hour}
		entry := grouped[mapKey]
		if entry == nil {
			entry = newHeatmapAccumulator(point)
			grouped[mapKey] = entry
			order = append(order, mapKey)
		}
		cost := costForHeatmapPoint(point, prices)
		entry.point.Calls += point.Calls
		entry.point.Success += point.SuccessCalls
		entry.point.Failure += point.FailureCalls
		entry.point.Tokens += point.TotalTokens
		entry.point.Cost += cost
		addHeatmapContributor(entry.models, heatmapContributorKey(point.Model), point.Model, point, cost)
		addHeatmapContributor(entry.apiKeys, strings.TrimSpace(point.APIKeyHash), point.APIKeyHash, point, cost)
		addHeatmapContributor(entry.providers, heatmapProviderKey(point.Provider), point.Provider, point, cost)
	}
	result := make([]HeatmapPoint, 0, len(order))
	for _, mapKey := range order {
		entry := grouped[mapKey]
		entry.point.FailureRate = ratio(entry.point.Failure, entry.point.Calls)
		entry.point.ModelContributors = topHeatmapContributors(entry.models, entry.point.Calls)
		entry.point.APIKeyContributors = topHeatmapContributors(entry.apiKeys, entry.point.Calls)
		entry.point.ProviderContributors = topHeatmapContributors(entry.providers, entry.point.Calls)
		result = append(result, *entry.point)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Weekday < result[j].Weekday ||
			(result[i].Weekday == result[j].Weekday && result[i].Hour < result[j].Hour)
	})
	return result
}

func addHeatmapContributor(group map[string]*HeatmapContributor, key string, label string, point store.HeatmapPoint, cost float64) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = key
	}
	entry := group[key]
	if entry == nil {
		entry = &HeatmapContributor{Key: key, Label: label}
		group[key] = entry
	}
	entry.Calls += point.Calls
	entry.Success += point.SuccessCalls
	entry.Failure += point.FailureCalls
	entry.Tokens += point.TotalTokens
	entry.Cost += cost
}

func heatmapContributorKey(value string) string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "Unknown"
	}
	return normalized
}

func heatmapProviderKey(value string) string {
	return heatmapContributorKey(value)
}

func topHeatmapContributors(group map[string]*HeatmapContributor, totalCalls int64) []HeatmapContributor {
	if len(group) == 0 {
		return nil
	}
	result := make([]HeatmapContributor, 0, len(group))
	for _, contributor := range group {
		next := *contributor
		next.FailureRate = ratio(next.Failure, next.Calls)
		next.Share = ratio(next.Calls, totalCalls)
		result = append(result, next)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Calls > result[j].Calls ||
			(result[i].Calls == result[j].Calls && result[i].Cost > result[j].Cost) ||
			(result[i].Calls == result[j].Calls && result[i].Cost == result[j].Cost && result[i].Key < result[j].Key)
	})
	if len(result) > heatmapContributorLimit {
		result = result[:heatmapContributorLimit]
	}
	return result
}

func buildAnomalyPoints(timeline []TimelinePoint, granularity string) []AnomalyPoint {
	if len(timeline) < 2 {
		return nil
	}
	result := make([]AnomalyPoint, 0)
	for index := 1; index < len(timeline); index++ {
		previous := timeline[index-1]
		current := timeline[index]
		metricKeys := make([]string, 0, 6)
		requestChange := percentChange(float64(current.Calls), float64(previous.Calls))
		costChange := percentChange(current.Cost, previous.Cost)
		tokensPerRequestChange := percentChange(averageTokensPerRequest(current), averageTokensPerRequest(previous))
		cacheHitRateChange := cacheHitRate(current) - cacheHitRate(previous)
		failureRateChange := current.FailureRate - previous.FailureRate
		latencyP95Change := percentChange(floatValueOrZero(current.P95LatencyMS), floatValueOrZero(previous.P95LatencyMS))
		if requestChange > 1 {
			metricKeys = append(metricKeys, "request_spike")
		}
		if costChange > 1 {
			metricKeys = append(metricKeys, "cost_spike")
		}
		if tokensPerRequestChange > 0.5 {
			metricKeys = append(metricKeys, "tokens_per_request_spike")
		}
		if cacheHitRateChange < -0.2 {
			metricKeys = append(metricKeys, "cache_hit_drop")
		}
		if failureRateChange > 0.2 {
			metricKeys = append(metricKeys, "failure_rate_spike")
		}
		if latencyP95Change > 0.5 {
			metricKeys = append(metricKeys, "latency_spike")
		}
		if len(metricKeys) == 0 {
			continue
		}
		result = append(result, AnomalyPoint{
			BucketMS:               current.BucketMS,
			BucketEndMS:            current.BucketMS + bucketSizeMS(granularity),
			Label:                  current.Label,
			Severity:               anomalySeverity(len(metricKeys)),
			MetricKeys:             metricKeys,
			Calls:                  current.Calls,
			TotalTokens:            current.TotalTokens,
			Cost:                   current.Cost,
			FailureRate:            current.FailureRate,
			RequestChange:          requestChange,
			CostChange:             costChange,
			TokensPerRequestChange: tokensPerRequestChange,
			CacheHitRateChange:     cacheHitRateChange,
			FailureRateChange:      failureRateChange,
			LatencyP95Change:       latencyP95Change,
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		iScore := anomalyScore(result[i])
		jScore := anomalyScore(result[j])
		return iScore > jScore || (iScore == jScore && result[i].BucketMS > result[j].BucketMS)
	})
	if len(result) > 50 {
		result = result[:50]
	}
	return result
}

func buildModelShare(stats []store.ModelStat, prices map[string]store.ModelPrice) []ModelShareRow {
	aggregated := aggregateModelStats(stats, prices)
	result := make([]ModelShareRow, 0, len(aggregated))
	for _, stat := range aggregated {
		result = append(result, ModelShareRow{
			Model:  stat.Model,
			Calls:  stat.Calls,
			Tokens: stat.TotalTokens,
			Cost:   stat.Cost,
		})
	}
	return result
}

func buildModelStats(stats []store.ModelStat, prices map[string]store.ModelPrice) []ModelStat {
	aggregated := aggregateModelStats(stats, prices)
	result := make([]ModelStat, 0, len(aggregated))
	for _, stat := range aggregated {
		result = append(result, ModelStat{
			Model:               stat.Model,
			Calls:               stat.Calls,
			SuccessCalls:        stat.SuccessCalls,
			FailureCalls:        stat.Calls - stat.SuccessCalls,
			SuccessRate:         ratio(stat.SuccessCalls, stat.Calls),
			InputTokens:         stat.InputTokens,
			OutputTokens:        stat.OutputTokens,
			CachedTokens:        stat.CachedTokens,
			CacheReadTokens:     stat.CacheReadTokens,
			CacheCreationTokens: stat.CacheCreationTokens,
			CacheHitTokens:      stat.CacheHitTokens,
			CacheHitInputTokens: stat.CacheHitInputTokens,
			CacheHitRate:        usage.CacheHitRateFromTotals(stat.CacheHitTokens, stat.CacheHitInputTokens),
			TotalTokens:         stat.TotalTokens,
			Cost:                stat.Cost,
		})
	}
	return result
}

type aggregatedModelStat struct {
	Model               string
	Calls               int64
	SuccessCalls        int64
	InputTokens         int64
	OutputTokens        int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	CacheHitTokens      int64
	CacheHitInputTokens int64
	TotalTokens         int64
	Cost                float64
}

func aggregateModelStats(stats []store.ModelStat, prices map[string]store.ModelPrice) []aggregatedModelStat {
	grouped := make(map[string]*aggregatedModelStat, len(stats))
	order := make([]string, 0, len(stats))
	for _, stat := range stats {
		entry := grouped[stat.Model]
		if entry == nil {
			entry = &aggregatedModelStat{Model: stat.Model}
			grouped[stat.Model] = entry
			order = append(order, stat.Model)
		}
		entry.Calls += stat.Calls
		entry.SuccessCalls += stat.SuccessCalls
		entry.InputTokens += stat.InputTokens
		entry.OutputTokens += stat.OutputTokens
		entry.CachedTokens += stat.CachedTokens
		entry.CacheReadTokens += stat.CacheReadTokens
		entry.CacheCreationTokens += stat.CacheCreationTokens
		behaviorModel := stat.BillingModel
		if strings.TrimSpace(behaviorModel) == "" {
			behaviorModel = stat.Model
		}
		cacheHitTokens, cacheHitInputTokens := usage.CacheHitTotals(
			behaviorModel,
			stat.InputTokens,
			stat.CachedTokens,
			stat.CacheReadTokens,
			stat.CacheCreationTokens,
		)
		entry.CacheHitTokens += cacheHitTokens
		entry.CacheHitInputTokens += cacheHitInputTokens
		entry.TotalTokens += stat.TotalTokens
		entry.Cost += costForStat(stat, prices)
	}
	result := make([]aggregatedModelStat, 0, len(order))
	for _, model := range order {
		result = append(result, *grouped[model])
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Calls > result[j].Calls
	})
	return result
}

func buildChannelShare(stats []store.ChannelModelStat, prices map[string]store.ModelPrice) []ChannelShareRow {
	type accumulator struct {
		row        ChannelShareRow
		latencySum float64
		latencyN   int64
	}
	grouped := map[string]*accumulator{}
	for _, stat := range stats {
		authIndex := stat.AuthIndex
		if authIndex == "" {
			authIndex = "-"
		}
		entry := grouped[authIndex]
		if entry == nil {
			entry = &accumulator{row: ChannelShareRow{
				AuthIndex:            authIndex,
				Source:               stat.Source,
				AccountSnapshot:      stat.AccountSnapshot,
				AuthLabelSnapshot:    stat.AuthLabelSnapshot,
				AuthProviderSnapshot: stat.AuthProviderSnapshot,
			}}
			grouped[authIndex] = entry
		}
		fillChannelShareSnapshots(&entry.row, stat)
		entry.row.Calls += stat.Calls
		entry.row.Success += stat.SuccessCalls
		entry.row.Failure += stat.FailureCalls
		entry.row.Tokens += stat.TotalTokens
		entry.row.Cost += costForChannelStat(stat, prices)
		if stat.AvgLatencyMS.Valid && stat.LatencySamples > 0 {
			entry.latencySum += stat.AvgLatencyMS.Float64 * float64(stat.LatencySamples)
			entry.latencyN += stat.LatencySamples
		}
	}
	result := make([]ChannelShareRow, 0, len(grouped))
	for _, entry := range grouped {
		if entry.latencyN > 0 {
			value := entry.latencySum / float64(entry.latencyN)
			entry.row.AvgLatencyMS = &value
		}
		result = append(result, entry.row)
	}
	return result
}

func buildFailureSources(stats []store.FailureSourceStat) []FailureSourceRow {
	result := make([]FailureSourceRow, 0, len(stats))
	for _, stat := range stats {
		result = append(result, FailureSourceRow{
			Source:               stat.Source,
			SourceHash:           stat.SourceHash,
			AuthIndex:            stat.AuthIndex,
			AccountSnapshot:      stat.AccountSnapshot,
			AuthLabelSnapshot:    stat.AuthLabelSnapshot,
			AuthProviderSnapshot: stat.AuthProviderSnapshot,
			Calls:                stat.Calls,
			Failure:              stat.FailureCalls,
			LastSeenMS:           stat.LastSeenMS,
			AvgLatencyMS:         nullableFloat(stat.AvgLatencyMS.Valid, stat.AvgLatencyMS.Float64),
		})
	}
	return result
}

type accountStatAccumulator struct {
	row            AccountStatRow
	authIndices    map[string]struct{}
	sources        map[string]struct{}
	sourceHashes   map[string]struct{}
	models         map[string]*AccountModelStatRow
	latencySum     float64
	latencySamples int64
}

type apiKeyStatAccumulator struct {
	row            APIKeyStatRow
	authIndices    map[string]struct{}
	sources        map[string]struct{}
	sourceHashes   map[string]struct{}
	models         map[string]*AccountModelStatRow
	contexts       map[string]*apiKeyContextAccumulator
	latencySum     float64
	latencySamples int64
}

type apiKeyContextAccumulator struct {
	row            APIKeyContextRow
	latencySum     float64
	latencySamples int64
}

type credentialStatAccumulator struct {
	row            CredentialStatRow
	models         map[string]*AccountModelStatRow
	latencySum     float64
	latencySamples int64
}

func buildAccountStats(stats []store.AccountModelStat, prices map[string]store.ModelPrice) []AccountStatRow {
	grouped := map[string]*accountStatAccumulator{}
	for _, stat := range stats {
		id := accountGroupKey(stat.AccountSnapshot, stat.AuthLabelSnapshot, stat.Source, stat.AuthIndex)
		entry := grouped[id]
		if entry == nil {
			entry = &accountStatAccumulator{
				row: AccountStatRow{
					ID:                   id,
					AccountSnapshot:      stat.AccountSnapshot,
					AuthLabelSnapshot:    stat.AuthLabelSnapshot,
					AuthProviderSnapshot: stat.AuthProviderSnapshot,
				},
				authIndices:  map[string]struct{}{},
				sources:      map[string]struct{}{},
				sourceHashes: map[string]struct{}{},
				models:       map[string]*AccountModelStatRow{},
			}
			grouped[id] = entry
		}
		fillAccountStatSnapshots(&entry.row, stat.AccountSnapshot, stat.AuthLabelSnapshot, stat.AuthProviderSnapshot)
		addSetValue(entry.authIndices, stat.AuthIndex)
		addSetValue(entry.sources, stat.Source)
		addSetValue(entry.sourceHashes, stat.SourceHash)
		cost := costForAccountModelStat(stat, prices)
		addAccountTotals(
			&entry.row.Calls,
			&entry.row.SuccessCalls,
			&entry.row.FailureCalls,
			&entry.row.InputTokens,
			&entry.row.OutputTokens,
			&entry.row.CachedTokens,
			&entry.row.CacheReadTokens,
			&entry.row.CacheCreationTokens,
			&entry.row.TotalTokens,
			&entry.row.Cost,
			stat.Calls,
			stat.SuccessCalls,
			stat.FailureCalls,
			stat.InputTokens,
			stat.OutputTokens,
			stat.CachedTokens,
			stat.CacheReadTokens,
			stat.CacheCreationTokens,
			stat.TotalTokens,
			cost,
		)
		if stat.LastSeenMS > entry.row.LastSeenMS {
			entry.row.LastSeenMS = stat.LastSeenMS
		}
		if stat.AvgLatencyMS.Valid && stat.LatencySamples > 0 {
			entry.latencySum += stat.AvgLatencyMS.Float64 * float64(stat.LatencySamples)
			entry.latencySamples += stat.LatencySamples
		}
		addAccountModelStat(entry.models, stat.Model, stat.BillingModel, stat.Calls, stat.SuccessCalls, stat.FailureCalls, stat.InputTokens, stat.OutputTokens, stat.CachedTokens, stat.CacheReadTokens, stat.CacheCreationTokens, stat.TotalTokens, cost, stat.LastSeenMS)
	}

	result := make([]AccountStatRow, 0, len(grouped))
	for _, entry := range grouped {
		entry.row.SuccessRate = ratio(entry.row.SuccessCalls, entry.row.Calls)
		entry.row.AuthIndices = sortedSetValues(entry.authIndices)
		entry.row.Sources = sortedSetValues(entry.sources)
		entry.row.SourceHashes = sortedSetValues(entry.sourceHashes)
		entry.row.Models = sortedAccountModelStats(entry.models)
		if entry.latencySamples > 0 {
			value := entry.latencySum / float64(entry.latencySamples)
			entry.row.AvgLatencyMS = &value
		}
		result = append(result, entry.row)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].LastSeenMS > result[j].LastSeenMS ||
			(result[i].LastSeenMS == result[j].LastSeenMS && result[i].Calls > result[j].Calls) ||
			(result[i].LastSeenMS == result[j].LastSeenMS && result[i].Calls == result[j].Calls && result[i].Cost > result[j].Cost)
	})
	return result
}

func buildCredentialStats(stats []store.CredentialModelStat, prices map[string]store.ModelPrice) []CredentialStatRow {
	grouped := map[string]*credentialStatAccumulator{}
	for _, stat := range stats {
		id := credentialGroupKey(stat)
		entry := grouped[id]
		if entry == nil {
			entry = &credentialStatAccumulator{
				row: CredentialStatRow{
					ID:                    id,
					AuthFileSnapshot:      stat.AuthFileSnapshot,
					AuthIndex:             stat.AuthIndex,
					Source:                stat.Source,
					SourceHash:            stat.SourceHash,
					AccountSnapshot:       stat.AccountSnapshot,
					AuthLabelSnapshot:     stat.AuthLabelSnapshot,
					AuthProviderSnapshot:  stat.AuthProviderSnapshot,
					AuthProjectIDSnapshot: stat.AuthProjectIDSnapshot,
				},
				models: map[string]*AccountModelStatRow{},
			}
			grouped[id] = entry
		}
		fillCredentialStatSnapshots(&entry.row, stat)
		cost := costForCredentialModelStat(stat, prices)
		addAccountTotals(
			&entry.row.Calls,
			&entry.row.SuccessCalls,
			&entry.row.FailureCalls,
			&entry.row.InputTokens,
			&entry.row.OutputTokens,
			&entry.row.CachedTokens,
			&entry.row.CacheReadTokens,
			&entry.row.CacheCreationTokens,
			&entry.row.TotalTokens,
			&entry.row.Cost,
			stat.Calls,
			stat.SuccessCalls,
			stat.FailureCalls,
			stat.InputTokens,
			stat.OutputTokens,
			stat.CachedTokens,
			stat.CacheReadTokens,
			stat.CacheCreationTokens,
			stat.TotalTokens,
			cost,
		)
		if stat.LastSeenMS > entry.row.LastSeenMS {
			entry.row.LastSeenMS = stat.LastSeenMS
		}
		if stat.AvgLatencyMS.Valid && stat.LatencySamples > 0 {
			entry.latencySum += stat.AvgLatencyMS.Float64 * float64(stat.LatencySamples)
			entry.latencySamples += stat.LatencySamples
		}
		addAccountModelStat(entry.models, stat.Model, stat.BillingModel, stat.Calls, stat.SuccessCalls, stat.FailureCalls, stat.InputTokens, stat.OutputTokens, stat.CachedTokens, stat.CacheReadTokens, stat.CacheCreationTokens, stat.TotalTokens, cost, stat.LastSeenMS)
	}

	result := make([]CredentialStatRow, 0, len(grouped))
	for _, entry := range grouped {
		entry.row.SuccessRate = ratio(entry.row.SuccessCalls, entry.row.Calls)
		entry.row.Models = sortedAccountModelStats(entry.models)
		if entry.latencySamples > 0 {
			value := entry.latencySum / float64(entry.latencySamples)
			entry.row.AvgLatencyMS = &value
		}
		result = append(result, entry.row)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Cost > result[j].Cost ||
			(result[i].Cost == result[j].Cost && result[i].Calls > result[j].Calls) ||
			(result[i].Cost == result[j].Cost && result[i].Calls == result[j].Calls && result[i].LastSeenMS > result[j].LastSeenMS)
	})
	return result
}

type credentialTimelineAccumulator struct {
	point          CredentialTimelinePoint
	latencySum     float64
	latencySamples int64
}

func buildCredentialTimeline(points []store.CredentialTimelinePoint, granularity string, location *time.Location, prices map[string]store.ModelPrice) []CredentialTimelinePoint {
	type key struct {
		id       string
		bucketMS int64
	}
	grouped := map[key]*credentialTimelineAccumulator{}
	order := make([]key, 0, len(points))
	for _, point := range points {
		id := credentialTimelineGroupKey(point)
		mapKey := key{id: id, bucketMS: point.BucketMS}
		entry := grouped[mapKey]
		if entry == nil {
			entry = &credentialTimelineAccumulator{
				point: CredentialTimelinePoint{
					ID:                    id,
					Label:                 credentialTimelineLabel(point),
					AuthFileSnapshot:      point.AuthFileSnapshot,
					AuthIndex:             point.AuthIndex,
					Source:                point.Source,
					SourceHash:            point.SourceHash,
					AccountSnapshot:       point.AccountSnapshot,
					AuthLabelSnapshot:     point.AuthLabelSnapshot,
					AuthProviderSnapshot:  point.AuthProviderSnapshot,
					AuthProjectIDSnapshot: point.AuthProjectIDSnapshot,
					BucketMS:              point.BucketMS,
					BucketLabel:           timelineLabel(point.BucketMS, granularity, location),
				},
			}
			grouped[mapKey] = entry
			order = append(order, mapKey)
		}
		fillCredentialTimelineSnapshots(&entry.point, point)
		entry.point.Calls += point.Calls
		entry.point.Tokens += point.Tokens
		entry.point.TotalTokens += point.Tokens
		entry.point.Success += point.Success
		entry.point.Failure += point.Failure
		entry.point.InputTokens += point.InputTokens
		entry.point.OutputTokens += point.OutputTokens
		entry.point.CachedTokens += point.CachedTokens
		entry.point.CacheReadTokens += point.CacheReadTokens
		entry.point.CacheCreationTokens += point.CacheCreationTokens
		entry.point.ReasoningTokens += point.ReasoningTokens
		entry.point.Cost += costForCredentialTimelinePoint(point, prices)
		if point.AvgLatencyMS.Valid && point.LatencySamples > 0 {
			entry.latencySum += point.AvgLatencyMS.Float64 * float64(point.LatencySamples)
			entry.latencySamples += point.LatencySamples
		}
	}

	result := make([]CredentialTimelinePoint, 0, len(order))
	for _, mapKey := range order {
		entry := grouped[mapKey]
		if entry.latencySamples > 0 {
			value := entry.latencySum / float64(entry.latencySamples)
			entry.point.AvgLatencyMS = &value
		}
		entry.point.SuccessRate = ratio(entry.point.Success, entry.point.Calls)
		entry.point.FailureRate = ratio(entry.point.Failure, entry.point.Calls)
		result = append(result, entry.point)
	}
	return result
}

type apiKeyTimelineAccumulator struct {
	point          APIKeyTimelinePoint
	latencySum     float64
	latencySamples int64
}

func buildAPIKeyTimeline(points []store.APIKeyTimelinePoint, granularity string, location *time.Location, prices map[string]store.ModelPrice) []APIKeyTimelinePoint {
	type key struct {
		apiKeyHash string
		bucketMS   int64
	}
	grouped := map[key]*apiKeyTimelineAccumulator{}
	order := make([]key, 0, len(points))
	for _, point := range points {
		apiKeyHash := strings.TrimSpace(point.APIKeyHash)
		if apiKeyHash == "" {
			continue
		}
		mapKey := key{apiKeyHash: apiKeyHash, bucketMS: point.BucketMS}
		entry := grouped[mapKey]
		if entry == nil {
			entry = &apiKeyTimelineAccumulator{
				point: APIKeyTimelinePoint{
					APIKeyHash:  apiKeyHash,
					BucketMS:    point.BucketMS,
					BucketLabel: timelineLabel(point.BucketMS, granularity, location),
				},
			}
			grouped[mapKey] = entry
			order = append(order, mapKey)
		}
		entry.point.Calls += point.Calls
		entry.point.Tokens += point.Tokens
		entry.point.TotalTokens += point.Tokens
		entry.point.Success += point.Success
		entry.point.Failure += point.Failure
		entry.point.InputTokens += point.InputTokens
		entry.point.OutputTokens += point.OutputTokens
		entry.point.CachedTokens += point.CachedTokens
		entry.point.CacheReadTokens += point.CacheReadTokens
		entry.point.CacheCreationTokens += point.CacheCreationTokens
		entry.point.ReasoningTokens += point.ReasoningTokens
		entry.point.Cost += costForAPIKeyTimelinePoint(point, prices)
		if point.AvgLatencyMS.Valid && point.LatencySamples > 0 {
			entry.latencySum += point.AvgLatencyMS.Float64 * float64(point.LatencySamples)
			entry.latencySamples += point.LatencySamples
		}
	}

	result := make([]APIKeyTimelinePoint, 0, len(order))
	for _, mapKey := range order {
		entry := grouped[mapKey]
		if entry.latencySamples > 0 {
			value := entry.latencySum / float64(entry.latencySamples)
			entry.point.AvgLatencyMS = &value
		}
		entry.point.SuccessRate = ratio(entry.point.Success, entry.point.Calls)
		entry.point.FailureRate = ratio(entry.point.Failure, entry.point.Calls)
		result = append(result, entry.point)
	}
	return result
}

func buildAPIKeyStats(stats []store.APIKeyModelStat, prices map[string]store.ModelPrice) []APIKeyStatRow {
	grouped := map[string]*apiKeyStatAccumulator{}
	for _, stat := range stats {
		id := apiKeyGroupKey(stat.APIKeyHash, stat.SourceHash, stat.AuthIndex, stat.Source, stat.AuthProviderSnapshot)
		entry := grouped[id]
		if entry == nil {
			entry = &apiKeyStatAccumulator{
				row: APIKeyStatRow{
					ID:                   id,
					APIKeyHash:           stat.APIKeyHash,
					AccountSnapshot:      stat.AccountSnapshot,
					AuthLabelSnapshot:    stat.AuthLabelSnapshot,
					AuthProviderSnapshot: stat.AuthProviderSnapshot,
				},
				authIndices:  map[string]struct{}{},
				sources:      map[string]struct{}{},
				sourceHashes: map[string]struct{}{},
				models:       map[string]*AccountModelStatRow{},
				contexts:     map[string]*apiKeyContextAccumulator{},
			}
			grouped[id] = entry
		}
		fillAPIKeyStatSnapshots(&entry.row, stat.APIKeyHash, stat.AccountSnapshot, stat.AuthLabelSnapshot, stat.AuthProviderSnapshot)
		addSetValue(entry.authIndices, stat.AuthIndex)
		addSetValue(entry.sources, stat.Source)
		addSetValue(entry.sourceHashes, stat.SourceHash)
		cost := costForAPIKeyModelStat(stat, prices)
		addAccountTotals(
			&entry.row.Calls,
			&entry.row.SuccessCalls,
			&entry.row.FailureCalls,
			&entry.row.InputTokens,
			&entry.row.OutputTokens,
			&entry.row.CachedTokens,
			&entry.row.CacheReadTokens,
			&entry.row.CacheCreationTokens,
			&entry.row.TotalTokens,
			&entry.row.Cost,
			stat.Calls,
			stat.SuccessCalls,
			stat.FailureCalls,
			stat.InputTokens,
			stat.OutputTokens,
			stat.CachedTokens,
			stat.CacheReadTokens,
			stat.CacheCreationTokens,
			stat.TotalTokens,
			cost,
		)
		if stat.LastSeenMS > entry.row.LastSeenMS {
			entry.row.LastSeenMS = stat.LastSeenMS
		}
		if stat.AvgLatencyMS.Valid && stat.LatencySamples > 0 {
			entry.latencySum += stat.AvgLatencyMS.Float64 * float64(stat.LatencySamples)
			entry.latencySamples += stat.LatencySamples
		}
		addAPIKeyContextStat(entry.contexts, stat, cost)
		addAccountModelStat(entry.models, stat.Model, stat.BillingModel, stat.Calls, stat.SuccessCalls, stat.FailureCalls, stat.InputTokens, stat.OutputTokens, stat.CachedTokens, stat.CacheReadTokens, stat.CacheCreationTokens, stat.TotalTokens, cost, stat.LastSeenMS)
	}

	result := make([]APIKeyStatRow, 0, len(grouped))
	for _, entry := range grouped {
		entry.row.SuccessRate = ratio(entry.row.SuccessCalls, entry.row.Calls)
		entry.row.AuthIndices = sortedSetValues(entry.authIndices)
		entry.row.Sources = sortedSetValues(entry.sources)
		entry.row.SourceHashes = sortedSetValues(entry.sourceHashes)
		entry.row.Models = sortedAccountModelStats(entry.models)
		entry.row.Contexts = sortedAPIKeyContextStats(entry.contexts)
		if entry.latencySamples > 0 {
			value := entry.latencySum / float64(entry.latencySamples)
			entry.row.AvgLatencyMS = &value
		}
		result = append(result, entry.row)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].LastSeenMS > result[j].LastSeenMS ||
			(result[i].LastSeenMS == result[j].LastSeenMS && result[i].Calls > result[j].Calls) ||
			(result[i].LastSeenMS == result[j].LastSeenMS && result[i].Calls == result[j].Calls && result[i].Cost > result[j].Cost)
	})
	return result
}

func fillChannelShareSnapshots(row *ChannelShareRow, stat store.ChannelModelStat) {
	if row.Source == "" {
		row.Source = stat.Source
	}
	if row.AccountSnapshot == "" {
		row.AccountSnapshot = stat.AccountSnapshot
	}
	if row.AuthLabelSnapshot == "" {
		row.AuthLabelSnapshot = stat.AuthLabelSnapshot
	}
	if row.AuthProviderSnapshot == "" {
		row.AuthProviderSnapshot = stat.AuthProviderSnapshot
	}
}

func accountGroupKey(accountSnapshot, authLabelSnapshot, source, authIndex string) string {
	if strings.TrimSpace(accountSnapshot) != "" {
		return accountSnapshot
	}
	if strings.TrimSpace(authLabelSnapshot) != "" {
		return authLabelSnapshot
	}
	if strings.TrimSpace(source) != "" {
		return source
	}
	if strings.TrimSpace(authIndex) != "" {
		return authIndex
	}
	return "-"
}

func apiKeyGroupKey(apiKeyHash, sourceHash, authIndex, source, provider string) string {
	if strings.TrimSpace(apiKeyHash) != "" {
		return strings.ToLower(strings.TrimSpace(apiKeyHash))
	}
	parts := []string{"unknown-client-api-key"}
	for _, value := range []string{sourceHash, authIndex, source, provider} {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			trimmed = "-"
		}
		parts = append(parts, trimmed)
	}
	return strings.Join(parts, ":")
}

func fillAccountStatSnapshots(row *AccountStatRow, accountSnapshot, authLabelSnapshot, authProviderSnapshot string) {
	if row.AccountSnapshot == "" {
		row.AccountSnapshot = accountSnapshot
	}
	if row.AuthLabelSnapshot == "" {
		row.AuthLabelSnapshot = authLabelSnapshot
	}
	if row.AuthProviderSnapshot == "" {
		row.AuthProviderSnapshot = authProviderSnapshot
	}
}

func fillAPIKeyStatSnapshots(row *APIKeyStatRow, apiKeyHash, accountSnapshot, authLabelSnapshot, authProviderSnapshot string) {
	if row.APIKeyHash == "" {
		row.APIKeyHash = apiKeyHash
	}
	if row.AccountSnapshot == "" {
		row.AccountSnapshot = accountSnapshot
	}
	if row.AuthLabelSnapshot == "" {
		row.AuthLabelSnapshot = authLabelSnapshot
	}
	if row.AuthProviderSnapshot == "" {
		row.AuthProviderSnapshot = authProviderSnapshot
	}
}

func credentialGroupKey(stat store.CredentialModelStat) string {
	for _, value := range []string{stat.ID, stat.AuthFileSnapshot, stat.AuthIndex, stat.SourceHash, stat.Source} {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return "-"
}

func credentialTimelineGroupKey(point store.CredentialTimelinePoint) string {
	for _, value := range []string{point.ID, point.AuthFileSnapshot, point.AuthIndex, point.SourceHash, point.Source} {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return "-"
}

func credentialTimelineLabel(point store.CredentialTimelinePoint) string {
	for _, value := range []string{
		point.AuthLabelSnapshot,
		point.AccountSnapshot,
		point.AuthFileSnapshot,
		point.Source,
		point.AuthIndex,
		point.ID,
	} {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return "-"
}

func fillCredentialStatSnapshots(row *CredentialStatRow, stat store.CredentialModelStat) {
	if row.AuthFileSnapshot == "" {
		row.AuthFileSnapshot = stat.AuthFileSnapshot
	}
	if row.AuthIndex == "" {
		row.AuthIndex = stat.AuthIndex
	}
	if row.Source == "" {
		row.Source = stat.Source
	}
	if row.SourceHash == "" {
		row.SourceHash = stat.SourceHash
	}
	if row.AccountSnapshot == "" {
		row.AccountSnapshot = stat.AccountSnapshot
	}
	if row.AuthLabelSnapshot == "" {
		row.AuthLabelSnapshot = stat.AuthLabelSnapshot
	}
	if row.AuthProviderSnapshot == "" {
		row.AuthProviderSnapshot = stat.AuthProviderSnapshot
	}
	if row.AuthProjectIDSnapshot == "" {
		row.AuthProjectIDSnapshot = stat.AuthProjectIDSnapshot
	}
}

func fillCredentialTimelineSnapshots(row *CredentialTimelinePoint, point store.CredentialTimelinePoint) {
	if row.Label == "" || row.Label == "-" {
		row.Label = credentialTimelineLabel(point)
	}
	if row.AuthFileSnapshot == "" {
		row.AuthFileSnapshot = point.AuthFileSnapshot
	}
	if row.AuthIndex == "" {
		row.AuthIndex = point.AuthIndex
	}
	if row.Source == "" {
		row.Source = point.Source
	}
	if row.SourceHash == "" {
		row.SourceHash = point.SourceHash
	}
	if row.AccountSnapshot == "" {
		row.AccountSnapshot = point.AccountSnapshot
	}
	if row.AuthLabelSnapshot == "" {
		row.AuthLabelSnapshot = point.AuthLabelSnapshot
	}
	if row.AuthProviderSnapshot == "" {
		row.AuthProviderSnapshot = point.AuthProviderSnapshot
	}
	if row.AuthProjectIDSnapshot == "" {
		row.AuthProjectIDSnapshot = point.AuthProjectIDSnapshot
	}
}

func addSetValue(values map[string]struct{}, value string) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return
	}
	values[trimmed] = struct{}{}
}

func sortedSetValues(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func addAccountTotals(
	calls *int64,
	successCalls *int64,
	failureCalls *int64,
	inputTokens *int64,
	outputTokens *int64,
	cachedTokens *int64,
	cacheReadTokens *int64,
	cacheCreationTokens *int64,
	totalTokens *int64,
	cost *float64,
	addCalls int64,
	addSuccessCalls int64,
	addFailureCalls int64,
	addInputTokens int64,
	addOutputTokens int64,
	addCachedTokens int64,
	addCacheReadTokens int64,
	addCacheCreationTokens int64,
	addTotalTokens int64,
	addCost float64,
) {
	*calls += addCalls
	*successCalls += addSuccessCalls
	*failureCalls += addFailureCalls
	*inputTokens += addInputTokens
	*outputTokens += addOutputTokens
	*cachedTokens += addCachedTokens
	*cacheReadTokens += addCacheReadTokens
	*cacheCreationTokens += addCacheCreationTokens
	*totalTokens += addTotalTokens
	*cost += addCost
}

func addAccountModelStat(
	models map[string]*AccountModelStatRow,
	model string,
	billingModel string,
	calls int64,
	successCalls int64,
	failureCalls int64,
	inputTokens int64,
	outputTokens int64,
	cachedTokens int64,
	cacheReadTokens int64,
	cacheCreationTokens int64,
	totalTokens int64,
	cost float64,
	lastSeenMS int64,
) {
	modelKey := model
	if strings.TrimSpace(modelKey) == "" {
		modelKey = "-"
	}
	entry := models[modelKey]
	if entry == nil {
		entry = &AccountModelStatRow{Model: modelKey}
		models[modelKey] = entry
	}
	entry.Calls += calls
	entry.SuccessCalls += successCalls
	entry.FailureCalls += failureCalls
	entry.InputTokens += inputTokens
	entry.OutputTokens += outputTokens
	entry.CachedTokens += cachedTokens
	entry.CacheReadTokens += cacheReadTokens
	entry.CacheCreationTokens += cacheCreationTokens
	behaviorModel := billingModel
	if strings.TrimSpace(behaviorModel) == "" {
		behaviorModel = modelKey
	}
	cacheHitTokens, cacheHitInputTokens := usage.CacheHitTotals(
		behaviorModel,
		inputTokens,
		cachedTokens,
		cacheReadTokens,
		cacheCreationTokens,
	)
	entry.CacheHitTokens += cacheHitTokens
	entry.CacheHitInputTokens += cacheHitInputTokens
	entry.CacheHitRate = usage.CacheHitRateFromTotals(entry.CacheHitTokens, entry.CacheHitInputTokens)
	entry.TotalTokens += totalTokens
	entry.Cost += cost
	if lastSeenMS > entry.LastSeenMS {
		entry.LastSeenMS = lastSeenMS
	}
	entry.SuccessRate = ratio(entry.SuccessCalls, entry.Calls)
}

func apiKeyContextKey(stat store.APIKeyModelStat) string {
	parts := []string{
		stat.AuthProviderSnapshot,
		stat.AccountSnapshot,
		stat.AuthLabelSnapshot,
		stat.AuthIndex,
		stat.SourceHash,
		stat.Source,
	}
	normalized := make([]string, 0, len(parts))
	for _, value := range parts {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			trimmed = "-"
		}
		normalized = append(normalized, trimmed)
	}
	return strings.Join(normalized, ":")
}

func addAPIKeyContextStat(contexts map[string]*apiKeyContextAccumulator, stat store.APIKeyModelStat, cost float64) {
	key := apiKeyContextKey(stat)
	entry := contexts[key]
	if entry == nil {
		entry = &apiKeyContextAccumulator{
			row: APIKeyContextRow{
				ID:                   key,
				AccountSnapshot:      stat.AccountSnapshot,
				AuthLabelSnapshot:    stat.AuthLabelSnapshot,
				AuthProviderSnapshot: stat.AuthProviderSnapshot,
				AuthIndex:            stat.AuthIndex,
				Source:               stat.Source,
				SourceHash:           stat.SourceHash,
			},
		}
		contexts[key] = entry
	}
	entry.row.Calls += stat.Calls
	entry.row.SuccessCalls += stat.SuccessCalls
	entry.row.FailureCalls += stat.FailureCalls
	entry.row.TotalTokens += stat.TotalTokens
	entry.row.Cost += cost
	if stat.LastSeenMS > entry.row.LastSeenMS {
		entry.row.LastSeenMS = stat.LastSeenMS
	}
	if stat.AvgLatencyMS.Valid && stat.LatencySamples > 0 {
		entry.latencySum += stat.AvgLatencyMS.Float64 * float64(stat.LatencySamples)
		entry.latencySamples += stat.LatencySamples
	}
	entry.row.SuccessRate = ratio(entry.row.SuccessCalls, entry.row.Calls)
	entry.row.FailureRate = ratio(entry.row.FailureCalls, entry.row.Calls)
}

func sortedAPIKeyContextStats(contexts map[string]*apiKeyContextAccumulator) []APIKeyContextRow {
	result := make([]APIKeyContextRow, 0, len(contexts))
	for _, context := range contexts {
		if context.latencySamples > 0 {
			value := context.latencySum / float64(context.latencySamples)
			context.row.AvgLatencyMS = &value
		}
		result = append(result, context.row)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Cost > result[j].Cost ||
			(result[i].Cost == result[j].Cost && result[i].Calls > result[j].Calls) ||
			(result[i].Cost == result[j].Cost && result[i].Calls == result[j].Calls && result[i].LastSeenMS > result[j].LastSeenMS) ||
			(result[i].Cost == result[j].Cost && result[i].Calls == result[j].Calls && result[i].LastSeenMS == result[j].LastSeenMS && result[i].ID < result[j].ID)
	})
	return result
}

func sortedAccountModelStats(models map[string]*AccountModelStatRow) []AccountModelStatRow {
	result := make([]AccountModelStatRow, 0, len(models))
	for _, model := range models {
		result = append(result, *model)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Cost > result[j].Cost ||
			(result[i].Cost == result[j].Cost && result[i].Calls > result[j].Calls) ||
			(result[i].Cost == result[j].Cost && result[i].Calls == result[j].Calls && result[i].LastSeenMS > result[j].LastSeenMS)
	})
	return result
}

func buildTaskBuckets(buckets []store.TaskBucket) []TaskBucketRow {
	result := make([]TaskBucketRow, 0, len(buckets))
	for _, bucket := range buckets {
		result = append(result, TaskBucketRow{
			BucketKey:           bucket.BucketKey,
			Total:               bucket.Total,
			Success:             bucket.Success,
			Failure:             bucket.Failure,
			FirstMS:             bucket.FirstMS,
			LastMS:              bucket.LastMS,
			Source:              bucket.Source,
			SourceHash:          bucket.SourceHash,
			AuthIndex:           bucket.AuthIndex,
			Models:              splitCSV(bucket.Models),
			Endpoints:           splitCSV(bucket.Endpoints),
			InputTokens:         bucket.InputTokens,
			OutputTokens:        bucket.OutputTokens,
			CachedTokens:        bucket.CachedTokens,
			CacheReadTokens:     bucket.CacheReadTokens,
			CacheCreationTokens: bucket.CacheCreationTokens,
			TotalTokens:         bucket.TotalTokens,
			AvgLatencyMS:        nullableFloat(bucket.AvgLatencyMS.Valid, bucket.AvgLatencyMS.Float64),
			MaxLatencyMS:        nullableInt(bucket.MaxLatencyMS.Valid, bucket.MaxLatencyMS.Int64),
		})
	}
	return result
}

func buildRecentFailures(failures []store.RecentFailure) []RecentFailure {
	result := make([]RecentFailure, 0, len(failures))
	for _, failure := range failures {
		result = append(result, RecentFailure{
			TimestampMS:            failure.TimestampMS,
			Model:                  failure.Model,
			APIKeyHash:             failure.APIKeyHash,
			Source:                 failure.Source,
			SourceHash:             failure.SourceHash,
			AuthIndex:              failure.AuthIndex,
			AccountSnapshot:        failure.AccountSnapshot,
			AuthLabelSnapshot:      failure.AuthLabelSnapshot,
			AuthProviderSnapshot:   failure.AuthProviderSnapshot,
			AuthProjectIDSnapshot:  failure.AuthProjectIDSnapshot,
			Endpoint:               failure.Endpoint,
			DurationMS:             nullableInt(failure.LatencyMS.Valid, failure.LatencyMS.Int64),
			FailStatusCode:         nullableInt(failure.FailStatusCode.Valid, failure.FailStatusCode.Int64),
			FailSummary:            failure.FailSummary,
			ResponseMetadata:       failure.ResponseMetadata,
			HeaderQuotaRecoverAtMS: nullableInt(failure.HeaderQuotaRecoverAtMS.Valid, failure.HeaderQuotaRecoverAtMS.Int64),
			HeaderQuotaUsedPercent: nullableFloat(failure.HeaderQuotaUsedPercent.Valid, failure.HeaderQuotaUsedPercent.Float64),
			HeaderQuotaPlanType:    failure.HeaderQuotaPlanType,
			HeaderErrorKind:        failure.HeaderErrorKind,
			HeaderErrorCode:        failure.HeaderErrorCode,
			HeaderTraceID:          failure.HeaderTraceID,
		})
	}
	return result
}

func buildEvents(page store.EventsPage, totalCount int64) *EventsResponse {
	items := make([]EventRow, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, EventRow{
			RequestID:              item.RequestID,
			EventHash:              item.EventHash,
			TimestampMS:            item.TimestampMS,
			Model:                  item.Model,
			ResolvedModel:          item.ResolvedModel,
			Endpoint:               item.Endpoint,
			Method:                 item.Method,
			Path:                   item.Path,
			AuthIndex:              item.AuthIndex,
			Source:                 item.Source,
			SourceHash:             item.SourceHash,
			APIKeyHash:             item.APIKeyHash,
			AccountSnapshot:        item.AccountSnapshot,
			AuthLabelSnapshot:      item.AuthLabelSnapshot,
			AuthFileSnapshot:       item.AuthFileSnapshot,
			AuthProviderSnapshot:   item.AuthProviderSnapshot,
			AuthProjectIDSnapshot:  item.AuthProjectIDSnapshot,
			ReasoningEffort:        item.ReasoningEffort,
			ServiceTier:            item.ServiceTier,
			ExecutorType:           item.ExecutorType,
			InputTokens:            item.InputTokens,
			OutputTokens:           item.OutputTokens,
			CachedTokens:           item.CachedTokens,
			CacheReadTokens:        item.CacheReadTokens,
			CacheCreationTokens:    item.CacheCreationTokens,
			ReasoningTokens:        item.ReasoningTokens,
			TotalTokens:            item.TotalTokens,
			LatencyMS:              nullableInt(item.LatencyMS.Valid, item.LatencyMS.Int64),
			TTFTMS:                 nullableInt(item.TTFTMS.Valid, item.TTFTMS.Int64),
			Failed:                 item.Failed,
			FailStatusCode:         nullableInt(item.FailStatusCode.Valid, item.FailStatusCode.Int64),
			FailSummary:            item.FailSummary,
			ResponseMetadata:       item.ResponseMetadata,
			HeaderQuotaRecoverAtMS: nullableInt(item.HeaderQuotaRecoverAtMS.Valid, item.HeaderQuotaRecoverAtMS.Int64),
			HeaderQuotaUsedPercent: nullableFloat(item.HeaderQuotaUsedPercent.Valid, item.HeaderQuotaUsedPercent.Float64),
			HeaderQuotaPlanType:    item.HeaderQuotaPlanType,
			HeaderErrorKind:        item.HeaderErrorKind,
			HeaderErrorCode:        item.HeaderErrorCode,
			HeaderTraceID:          item.HeaderTraceID,
		})
	}
	return &EventsResponse{Items: items, NextBeforeMS: page.NextBeforeMS, NextBeforeID: page.NextBeforeID, HasMore: page.HasMore, TotalCount: totalCount}
}

func buildHeaderSnapshots(items []store.HeaderSnapshot) []HeaderSnapshot {
	result := make([]HeaderSnapshot, 0, len(items))
	for _, item := range items {
		result = append(result, HeaderSnapshot{
			EventHash:              item.EventHash,
			TimestampMS:            item.TimestampMS,
			AuthFileSnapshot:       item.AuthFileSnapshot,
			AuthIndex:              item.AuthIndex,
			AccountSnapshot:        item.AccountSnapshot,
			AuthLabelSnapshot:      item.AuthLabelSnapshot,
			AuthProviderSnapshot:   item.AuthProviderSnapshot,
			AuthProjectIDSnapshot:  item.AuthProjectIDSnapshot,
			Source:                 item.Source,
			SourceHash:             item.SourceHash,
			ResponseMetadata:       item.ResponseMetadata,
			HeaderQuotaRecoverAtMS: nullableInt(item.HeaderQuotaRecoverAtMS.Valid, item.HeaderQuotaRecoverAtMS.Int64),
			HeaderQuotaUsedPercent: nullableFloat(item.HeaderQuotaUsedPercent.Valid, item.HeaderQuotaUsedPercent.Float64),
			HeaderQuotaPlanType:    item.HeaderQuotaPlanType,
			HeaderErrorKind:        item.HeaderErrorKind,
			HeaderErrorCode:        item.HeaderErrorCode,
			HeaderTraceID:          item.HeaderTraceID,
		})
	}
	return result
}

type accountHistoryTotal struct {
	requests     int64
	successCalls int64
	failureCalls int64
	totalTokens  int64
	cost         float64
	firstSeenMS  int64
	lastSeenMS   int64
}

func accountHistoryTargetKey(target AccountHistoryTarget) (string, bool) {
	if key := strings.TrimSpace(target.AccountKey); key != "" {
		return key, true
	}
	if strings.TrimSpace(target.AccountSnapshot) == "" &&
		strings.TrimSpace(target.AuthLabelSnapshot) == "" &&
		strings.TrimSpace(target.Source) == "" &&
		strings.TrimSpace(target.AuthIndex) == "" {
		return "", false
	}
	return store.AccountHistoryKey(
		target.AccountSnapshot,
		target.AuthLabelSnapshot,
		target.Source,
		target.AuthIndex,
	), true
}

func buildAccountHistoryTotals(rows []store.AccountHistoryRollupRow, prices map[string]store.ModelPrice) map[string]*accountHistoryTotal {
	totals := map[string]*accountHistoryTotal{}
	for _, row := range rows {
		total := totals[row.AccountKey]
		if total == nil {
			total = &accountHistoryTotal{}
			totals[row.AccountKey] = total
		}
		total.requests += row.Calls
		total.successCalls += row.SuccessCalls
		total.failureCalls += row.FailureCalls
		total.totalTokens += row.TotalTokens
		total.cost += pricing.CostForModelCandidatesWithServiceTier(
			[]string{row.BillingModel, row.Model},
			row.ServiceTier,
			pricing.ModelTokens{
				InputTokens:             row.InputTokens,
				OutputTokens:            row.OutputTokens,
				CachedTokens:            row.CachedTokens,
				CacheReadTokens:         row.CacheReadTokens,
				CacheCreationTokens:     row.CacheCreationTokens,
				LongInputTokens:         row.LongInputTokens,
				LongOutputTokens:        row.LongOutputTokens,
				LongCachedTokens:        row.LongCachedTokens,
				LongCacheReadTokens:     row.LongCacheReadTokens,
				LongCacheCreationTokens: row.LongCacheCreationTokens,
			},
			prices,
		)
		if total.firstSeenMS == 0 || (row.FirstSeenMS > 0 && row.FirstSeenMS < total.firstSeenMS) {
			total.firstSeenMS = row.FirstSeenMS
		}
		if row.LastSeenMS > total.lastSeenMS {
			total.lastSeenMS = row.LastSeenMS
		}
	}
	return totals
}

func accountHistorySyncStatus(matched bool, pending bool) string {
	if pending {
		return "pending"
	}
	if matched {
		return "ready"
	}
	return "empty"
}

func nullableMSPointer(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func sumCost(stats []store.ModelStat, prices map[string]store.ModelPrice) float64 {
	total := 0.0
	for _, stat := range stats {
		total += costForStat(stat, prices)
	}
	return total
}

func costForStat(stat store.ModelStat, prices map[string]store.ModelPrice) float64 {
	return pricing.CostForModelCandidatesWithServiceTier([]string{stat.BillingModel, stat.Model}, stat.ServiceTier, pricing.ModelTokens{
		InputTokens:             stat.InputTokens,
		OutputTokens:            stat.OutputTokens,
		CachedTokens:            stat.CachedTokens,
		CacheReadTokens:         stat.CacheReadTokens,
		CacheCreationTokens:     stat.CacheCreationTokens,
		LongInputTokens:         stat.LongInputTokens,
		LongOutputTokens:        stat.LongOutputTokens,
		LongCachedTokens:        stat.LongCachedTokens,
		LongCacheReadTokens:     stat.LongCacheReadTokens,
		LongCacheCreationTokens: stat.LongCacheCreationTokens,
	}, prices)
}

func costForTimelinePoint(point store.TimelinePoint, prices map[string]store.ModelPrice) float64 {
	return pricing.CostForModelCandidatesWithServiceTier([]string{point.BillingModel, point.Model}, point.ServiceTier, pricing.ModelTokens{
		InputTokens:             point.InputTokens,
		OutputTokens:            point.OutputTokens,
		CachedTokens:            point.CachedTokens,
		CacheReadTokens:         point.CacheReadTokens,
		CacheCreationTokens:     point.CacheCreationTokens,
		LongInputTokens:         point.LongInputTokens,
		LongOutputTokens:        point.LongOutputTokens,
		LongCachedTokens:        point.LongCachedTokens,
		LongCacheReadTokens:     point.LongCacheReadTokens,
		LongCacheCreationTokens: point.LongCacheCreationTokens,
	}, prices)
}

func costForHeatmapPoint(point store.HeatmapPoint, prices map[string]store.ModelPrice) float64 {
	return pricing.CostForModelCandidatesWithServiceTier([]string{point.BillingModel, point.Model}, point.ServiceTier, pricing.ModelTokens{
		InputTokens:             point.InputTokens,
		OutputTokens:            point.OutputTokens,
		CachedTokens:            point.CachedTokens,
		CacheReadTokens:         point.CacheReadTokens,
		CacheCreationTokens:     point.CacheCreationTokens,
		LongInputTokens:         point.LongInputTokens,
		LongOutputTokens:        point.LongOutputTokens,
		LongCachedTokens:        point.LongCachedTokens,
		LongCacheReadTokens:     point.LongCacheReadTokens,
		LongCacheCreationTokens: point.LongCacheCreationTokens,
	}, prices)
}

func costForChannelStat(stat store.ChannelModelStat, prices map[string]store.ModelPrice) float64 {
	return pricing.CostForModelCandidatesWithServiceTier([]string{stat.BillingModel, stat.Model}, stat.ServiceTier, pricing.ModelTokens{
		InputTokens:             stat.InputTokens,
		OutputTokens:            stat.OutputTokens,
		CachedTokens:            stat.CachedTokens,
		CacheReadTokens:         stat.CacheReadTokens,
		CacheCreationTokens:     stat.CacheCreationTokens,
		LongInputTokens:         stat.LongInputTokens,
		LongOutputTokens:        stat.LongOutputTokens,
		LongCachedTokens:        stat.LongCachedTokens,
		LongCacheReadTokens:     stat.LongCacheReadTokens,
		LongCacheCreationTokens: stat.LongCacheCreationTokens,
	}, prices)
}

func costForAccountModelStat(stat store.AccountModelStat, prices map[string]store.ModelPrice) float64 {
	return pricing.CostForModelCandidatesWithServiceTier([]string{stat.BillingModel, stat.Model}, stat.ServiceTier, pricing.ModelTokens{
		InputTokens:             stat.InputTokens,
		OutputTokens:            stat.OutputTokens,
		CachedTokens:            stat.CachedTokens,
		CacheReadTokens:         stat.CacheReadTokens,
		CacheCreationTokens:     stat.CacheCreationTokens,
		LongInputTokens:         stat.LongInputTokens,
		LongOutputTokens:        stat.LongOutputTokens,
		LongCachedTokens:        stat.LongCachedTokens,
		LongCacheReadTokens:     stat.LongCacheReadTokens,
		LongCacheCreationTokens: stat.LongCacheCreationTokens,
	}, prices)
}

func costForAPIKeyModelStat(stat store.APIKeyModelStat, prices map[string]store.ModelPrice) float64 {
	return pricing.CostForModelCandidatesWithServiceTier([]string{stat.BillingModel, stat.Model}, stat.ServiceTier, pricing.ModelTokens{
		InputTokens:             stat.InputTokens,
		OutputTokens:            stat.OutputTokens,
		CachedTokens:            stat.CachedTokens,
		CacheReadTokens:         stat.CacheReadTokens,
		CacheCreationTokens:     stat.CacheCreationTokens,
		LongInputTokens:         stat.LongInputTokens,
		LongOutputTokens:        stat.LongOutputTokens,
		LongCachedTokens:        stat.LongCachedTokens,
		LongCacheReadTokens:     stat.LongCacheReadTokens,
		LongCacheCreationTokens: stat.LongCacheCreationTokens,
	}, prices)
}

func costForCredentialModelStat(stat store.CredentialModelStat, prices map[string]store.ModelPrice) float64 {
	return pricing.CostForModelCandidatesWithServiceTier([]string{stat.BillingModel, stat.Model}, stat.ServiceTier, pricing.ModelTokens{
		InputTokens:             stat.InputTokens,
		OutputTokens:            stat.OutputTokens,
		CachedTokens:            stat.CachedTokens,
		CacheReadTokens:         stat.CacheReadTokens,
		CacheCreationTokens:     stat.CacheCreationTokens,
		LongInputTokens:         stat.LongInputTokens,
		LongOutputTokens:        stat.LongOutputTokens,
		LongCachedTokens:        stat.LongCachedTokens,
		LongCacheReadTokens:     stat.LongCacheReadTokens,
		LongCacheCreationTokens: stat.LongCacheCreationTokens,
	}, prices)
}

func costForCredentialTimelinePoint(point store.CredentialTimelinePoint, prices map[string]store.ModelPrice) float64 {
	return pricing.CostForModelCandidatesWithServiceTier([]string{point.BillingModel, point.Model}, point.ServiceTier, pricing.ModelTokens{
		InputTokens:             point.InputTokens,
		OutputTokens:            point.OutputTokens,
		CachedTokens:            point.CachedTokens,
		CacheReadTokens:         point.CacheReadTokens,
		CacheCreationTokens:     point.CacheCreationTokens,
		LongInputTokens:         point.LongInputTokens,
		LongOutputTokens:        point.LongOutputTokens,
		LongCachedTokens:        point.LongCachedTokens,
		LongCacheReadTokens:     point.LongCacheReadTokens,
		LongCacheCreationTokens: point.LongCacheCreationTokens,
	}, prices)
}

func costForAPIKeyTimelinePoint(point store.APIKeyTimelinePoint, prices map[string]store.ModelPrice) float64 {
	return pricing.CostForModelCandidatesWithServiceTier([]string{point.BillingModel, point.Model}, point.ServiceTier, pricing.ModelTokens{
		InputTokens:             point.InputTokens,
		OutputTokens:            point.OutputTokens,
		CachedTokens:            point.CachedTokens,
		CacheReadTokens:         point.CacheReadTokens,
		CacheCreationTokens:     point.CacheCreationTokens,
		LongInputTokens:         point.LongInputTokens,
		LongOutputTokens:        point.LongOutputTokens,
		LongCachedTokens:        point.LongCachedTokens,
		LongCacheReadTokens:     point.LongCacheReadTokens,
		LongCacheCreationTokens: point.LongCacheCreationTokens,
	}, prices)
}

func ratio(part int64, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total)
}

func ratioFloat(part float64, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return part / float64(total)
}

func percentChange(current float64, previous float64) float64 {
	if previous <= 0 {
		if current > 0 {
			return 1
		}
		return 0
	}
	return (current - previous) / previous
}

func averageTokensPerRequest(point TimelinePoint) float64 {
	if point.Calls <= 0 {
		return 0
	}
	return float64(point.TotalTokens) / float64(point.Calls)
}

func cacheHitRate(point TimelinePoint) float64 {
	rate := point.CacheHitRate
	if rate <= 0 {
		rate = usage.CacheHitRate("", point.InputTokens, point.CachedTokens, point.CacheReadTokens, point.CacheCreationTokens)
	}
	if rate > 1 {
		return 1
	}
	return rate
}

func cacheHitRateForModelStats(stats []store.ModelStat) float64 {
	var hitTokens int64
	var inputTokens int64
	for _, stat := range stats {
		behaviorModel := stat.BillingModel
		if strings.TrimSpace(behaviorModel) == "" {
			behaviorModel = stat.Model
		}
		statHitTokens, statInputTokens := usage.CacheHitTotals(
			behaviorModel,
			stat.InputTokens,
			stat.CachedTokens,
			stat.CacheReadTokens,
			stat.CacheCreationTokens,
		)
		hitTokens += statHitTokens
		inputTokens += statInputTokens
	}
	return usage.CacheHitRateFromTotals(hitTokens, inputTokens)
}

func floatValueOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func bucketSizeMS(granularity string) int64 {
	if granularity == "day" {
		return 24 * 60 * 60 * 1000
	}
	return 60 * 60 * 1000
}

func anomalySeverity(metricCount int) string {
	if metricCount >= 3 {
		return "high"
	}
	if metricCount >= 2 {
		return "medium"
	}
	return "low"
}

func anomalyScore(point AnomalyPoint) float64 {
	score := float64(len(point.MetricKeys)) * 10
	score += positive(point.RequestChange)
	score += positive(point.CostChange)
	score += positive(point.TokensPerRequestChange)
	score += positive(-point.CacheHitRateChange)
	score += positive(point.FailureRateChange)
	score += positive(point.LatencyP95Change)
	return score
}

func positive(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

func nullableFloat(valid bool, value float64) *float64 {
	if !valid {
		return nil
	}
	return &value
}

func nullableInt(valid bool, value int64) *int64 {
	if !valid {
		return nil
	}
	return &value
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func timelineLabel(bucketMS int64, granularity string, location *time.Location) string {
	if location == nil {
		location = time.UTC
	}
	tm := time.UnixMilli(bucketMS).In(location)
	if granularity == "day" {
		return tm.Format("01/02")
	}
	return tm.Format("15:04")
}
