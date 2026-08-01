import type {
  MonitoringAnalyticsAnomalyPoint,
  MonitoringAnalyticsApiKeyStatRow,
  MonitoringAnalyticsApiKeyTimelinePoint,
  MonitoringAnalyticsChannelShareRow,
  MonitoringAnalyticsCredentialStatRow,
  MonitoringAnalyticsCredentialTimelinePoint,
  MonitoringAnalyticsEventRow,
  MonitoringAnalyticsFilters,
  MonitoringAnalyticsHeatmapContributor,
  MonitoringAnalyticsHeatmapPoint,
  MonitoringAnalyticsInclude,
  MonitoringAnalyticsModelStat,
  MonitoringAnalyticsResponse,
  MonitoringAnalyticsSummary,
  MonitoringAnalyticsSummaryComparison,
  MonitoringAnalyticsTimelinePoint,
} from '@/services/api/usageService';
import {
  sanitizeApiKeyDisplayText,
  type ApiKeyDisplayInfo,
} from '@/features/monitoring/model/apiKeys';
import { buildMonitoringSourceDisplay } from '@/features/monitoring/model/sourceDisplay';
import type { MonitoringAuthMeta, MonitoringChannelMeta } from '@/features/monitoring/model/types';
import type { CredentialInfo } from '@/types/sourceInfo';
import { buildSourceInfoMap } from '@/utils/sourceResolver';
import {
  calculateCacheHitRate,
  calculateCacheHitRateFromTotals,
  formatCompactNumber,
  formatUsd,
  getCacheHitTotals,
} from '@/utils/usage';

export type UsageAnalyticsTab =
  | 'overview'
  | 'trends'
  | 'models'
  | 'apiKeys'
  | 'credentials'
  | 'heatmap';
export type UsageAnalyticsTimeRange = '24h' | 'today' | 'yesterday' | '7d' | '30d' | 'custom';
export type UsageAnalyticsGranularity = 'auto' | 'hour' | 'day';
export type UsageAnalyticsResolvedGranularity = 'hour' | 'day';
export type UsageAnalyticsStatus = 'all' | 'success' | 'failed';
export type UsageAnalyticsLatencyFilter = 'all' | '3000' | '10000' | '30000';
export type UsageAnalyticsCacheStatus = 'all' | 'hit' | 'miss';

export type UsageAnalyticsCustomRange = {
  startMs: number;
  endMs: number;
};

export type UsageAnalyticsFiltersState = {
  timeRange: UsageAnalyticsTimeRange;
  customRange: UsageAnalyticsCustomRange | null;
  granularity: UsageAnalyticsGranularity;
  model: string;
  apiKeyHash: string;
  provider: string;
  authFile: string;
  status: UsageAnalyticsStatus;
  searchQuery: string;
  minLatencyMs: UsageAnalyticsLatencyFilter;
  cacheStatus: UsageAnalyticsCacheStatus;
  apiKeyKeyword: string;
};

export type UsageMetricKey =
  | 'requestCount'
  | 'totalTokens'
  | 'inputTokens'
  | 'outputTokens'
  | 'cachedTokens'
  | 'estimatedCost';
export type UsageTrendMetricKey = 'requestCount' | 'totalTokens' | 'estimatedCost';
export type UsageMatrixMetricKey = 'requestCount' | 'totalTokens' | 'estimatedCost' | 'failureRate';
export type UsageMatrixDimension = 'apiKeyModel' | 'authFileModel' | 'providerModel';
export type UsageHeatmapMetricKey = UsageMatrixMetricKey;
export type UsageHeatmapScaleMode = 'absolute' | 'byWeekday' | 'byHour';

export type UsageMetricDefinition = {
  key: UsageMetricKey;
  labelKey: string;
  color: string;
  axis: 'requests' | 'tokens' | 'cost';
};

export type UsageTimelinePoint = {
  bucketMs: number;
  bucketEndMs: number;
  label: string;
  requestCount: number;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
  cachedTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  reasoningTokens: number;
  estimatedCost: number;
  successCount: number;
  failureCount: number;
  successRate: number;
  failureRate: number;
  averageLatencyMs: number | null;
  p95LatencyMs: number | null;
  p95TtftMs: number | null;
  cacheHitRate: number;
  averageTokensPerRequest: number;
};

export type UsageSummaryMetrics = {
  requestCount: number;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
  cachedTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  cacheHitRate?: number;
  estimatedCost: number;
  averageCostPerCall: number;
  successRate: number;
  failureCount: number;
  averageLatencyMs: number | null;
  p95LatencyMs: number | null;
  p95TtftMs: number | null;
  rpm30m: number;
  tpm30m: number;
};

export type UsageSummaryDelta = {
  hasComparison: boolean;
  requestCount: number;
  totalTokens: number;
  estimatedCost: number;
};

export type UsageApiKeyContextRow = {
  id: string;
  provider?: string;
  account?: string;
  authIndex?: string;
  source?: string;
  sourceHash?: string;
  requestCount: number;
  successCount: number;
  failureCount: number;
  successRate: number;
  failureRate: number;
  totalTokens: number;
  estimatedCost: number;
  averageLatencyMs: number | null;
  lastSeenMs?: number;
};

export type UsageRankRow = {
  id: string;
  label: string;
  model?: string;
  apiKeyHash?: string;
  apiKeyCopyValue?: string;
  provider?: string;
  authFile?: string;
  authIndex?: string;
  source?: string;
  sourceHash?: string;
  account?: string;
  projectId?: string;
  requestCount: number;
  successCount: number;
  failureCount: number;
  successRate: number;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
  cachedTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  cacheHitTokens?: number;
  cacheHitInputTokens?: number;
  cacheHitRate?: number;
  estimatedCost: number;
  averageLatencyMs: number | null;
  lastSeenMs?: number;
  share: number;
  contexts?: UsageApiKeyContextRow[];
  models?: UsageRankRow[];
};

export type UsageProviderRow = {
  id: string;
  label: string;
  requestCount: number;
  successCount: number;
  failureCount: number;
  successRate: number;
  cacheRate: number;
  totalTokens: number;
  estimatedCost: number;
  averageLatencyMs: number | null;
  requestShare: number;
  costShare: number;
  tokenShare: number;
  share: number;
  models: UsageRankRow[];
};

export type UsageEntityTrendSeries = {
  id: string;
  label: string;
  color: string;
  points: Array<{
    bucketMs: number;
    label: string;
    value: number;
  }>;
};

export type UsageCredentialTimelinePoint = {
  id: string;
  label: string;
  bucketMs: number;
  bucketLabel: string;
  requestCount: number;
  totalTokens: number;
  estimatedCost: number;
};

export type UsageApiKeyTimelinePoint = {
  apiKeyHash: string;
  bucketMs: number;
  bucketLabel: string;
  requestCount: number;
  totalTokens: number;
  estimatedCost: number;
};

export type UsageMatrixCell = {
  rowId: string;
  rowLabel: string;
  columnId: string;
  columnLabel: string;
  requestCount: number;
  successCount: number;
  failureCount: number;
  totalTokens: number;
  estimatedCost: number;
  failureRate: number;
  value: number;
  share: number;
};

export type UsageMatrix = {
  dimension: UsageMatrixDimension;
  metric: UsageMatrixMetricKey;
  rowLabels: string[];
  columnLabels: string[];
  cells: UsageMatrixCell[];
  maxValue: number;
  totalValue: number;
};

export type UsageKeyAnomalyRow = {
  id: string;
  label: string;
  severity: 'low' | 'medium' | 'high';
  reasonKey: string;
  triggeredAtMs: number | null;
  row: UsageRankRow;
};

export type UsageCredentialQuotaRow = {
  id: string;
  label: string;
  plan: string;
  used: number;
  limit: number;
  remaining: number;
  usedRate: number;
  resetAtMs: number;
  status: 'normal' | 'warning' | 'exhausted';
  refreshedAtMs: number | null;
};

export type UsageInsight = {
  id: string;
  tone: 'info' | 'success' | 'warning' | 'danger';
  titleKey: string;
  bodyKey: string;
  actionTab?: UsageAnalyticsTab;
};

export type UsageHeatmapPoint = {
  weekday: number;
  hour: number;
  requestCount: number;
  successCount: number;
  failureCount: number;
  totalTokens: number;
  estimatedCost: number;
  failureRate: number;
  modelContributors?: UsageHeatmapContributor[];
  apiKeyContributors?: UsageHeatmapContributor[];
  providerContributors?: UsageHeatmapContributor[];
};

export type UsageHeatmapContributor = {
  key: string;
  label: string;
  requestCount: number;
  successCount: number;
  failureCount: number;
  totalTokens: number;
  estimatedCost: number;
  failureRate: number;
  share: number;
};

export type UsageHeatmapChartDatum = [
  hour: number,
  weekday: number,
  visualValue: number,
  requestCount: number,
  successCount: number,
  failureCount: number,
  totalTokens: number,
  estimatedCost: number,
  failureRate: number,
];

export type UsageHeatmapCellSelection = {
  weekday: number;
  hour: number;
};

export type UsageHeatmapCellDetail = {
  point: UsageHeatmapPoint;
  metricValue: number;
  overallBaseline: number;
  weekdayBaseline: number;
  hourBaseline: number;
  overallDelta: number;
  weekdayDelta: number;
  hourDelta: number;
  rank: number;
  totalCells: number;
};

export type UsageHeatmapCellSample = {
  sampleCount: number;
  dateKeys: string[];
  dateLabels: string[];
  overflowCount: number;
};

export type UsageHeatmapDateOption = {
  key: string;
  label: string;
  fromMs: number;
  toMs: number;
  sampleWindowCount: number;
};

export type UsageHeatmapRangeContext = {
  fromLabel: string;
  toLabel: string;
  rangeLabel: string;
  dayCount: number;
  sampleWindowCount: number;
  dateOptions: UsageHeatmapDateOption[];
  cellSamples: Record<string, UsageHeatmapCellSample>;
};

export type UsageHeatmapHighlight = {
  id: string;
  point: UsageHeatmapPoint;
  metric: UsageHeatmapMetricKey;
  value: number;
};

export type UsageHeatmapHighlights = {
  requestPeaks: UsageHeatmapHighlight[];
  costPeaks: UsageHeatmapHighlight[];
  failureRisks: UsageHeatmapHighlight[];
};

export type UsageServerAnomaly = {
  bucketMs: number;
  bucketEndMs: number;
  label: string;
  severity: 'low' | 'medium' | 'high' | string;
  metricKeys: string[];
  requestCount: number;
  totalTokens: number;
  estimatedCost: number;
  failureRate: number;
  requestChange: number;
  costChange: number;
  tokensPerRequestChange: number;
  cacheHitRateChange: number;
  failureRateChange: number;
  latencyP95Change: number;
};

export type UsageDrilldownEvent = {
  requestId: string;
  eventHash: string;
  timestampMs: number;
  model: string;
  apiKeyHash: string;
  apiKeyLabel: string;
  source: string;
  authIndex: string;
  endpoint: string;
  totalTokens: number;
  estimatedCost: number;
  latencyMs: number | null;
  ttftMs: number | null;
  failed: boolean;
  failStatusCode: number | null;
  failSummary: string;
  headerErrorKind: string;
  headerErrorCode: string;
  headerTraceId: string;
  headerQuotaPlanType: string;
  headerQuotaUsedPercent: number | null;
  headerQuotaRecoverAtMs: number | null;
};

export type UsageAnomaly = {
  key:
    | 'request_spike'
    | 'cost_spike'
    | 'token_per_request_spike'
    | 'cache_hit_drop'
    | 'failure_rate_spike'
    | 'latency_spike';
  labelKey: string;
  delta: number;
};

export type UsageAnomalyAnalysis = {
  point: UsageTimelinePoint;
  previousPoint: UsageTimelinePoint | null;
  anomalies: UsageAnomaly[];
  changes: Record<
    | UsageMetricKey
    | 'cacheCreationTokens'
    | 'cacheHitRate'
    | 'averageTokensPerRequest'
    | 'failureRate'
    | 'averageLatencyMs',
    number
  >;
  causeKeys: string[];
};

export const USAGE_ANALYTICS_DEFAULT_FILTERS: UsageAnalyticsFiltersState = {
  timeRange: '24h',
  customRange: null,
  granularity: 'auto',
  model: 'all',
  apiKeyHash: 'all',
  provider: 'all',
  authFile: 'all',
  status: 'all',
  searchQuery: '',
  minLatencyMs: 'all',
  cacheStatus: 'all',
  apiKeyKeyword: '',
};

export const USAGE_ANALYTICS_TABS: UsageAnalyticsTab[] = [
  'overview',
  'trends',
  'models',
  'apiKeys',
  'credentials',
  'heatmap',
];

export const USAGE_TIME_RANGES: UsageAnalyticsTimeRange[] = [
  '24h',
  'today',
  'yesterday',
  '7d',
  '30d',
  'custom',
];

export const USAGE_METRICS: UsageMetricDefinition[] = [
  {
    key: 'requestCount',
    labelKey: 'usage_analytics.metric_request_count',
    color: '#2563eb',
    axis: 'requests',
  },
  {
    key: 'totalTokens',
    labelKey: 'usage_analytics.metric_total_tokens',
    color: '#0ea5a7',
    axis: 'tokens',
  },
  {
    key: 'inputTokens',
    labelKey: 'usage_analytics.metric_input_tokens',
    color: '#3b82f6',
    axis: 'tokens',
  },
  {
    key: 'outputTokens',
    labelKey: 'usage_analytics.metric_output_tokens',
    color: '#22c55e',
    axis: 'tokens',
  },
  {
    key: 'cachedTokens',
    labelKey: 'usage_analytics.metric_cached_tokens',
    color: '#06b6d4',
    axis: 'tokens',
  },
  {
    key: 'estimatedCost',
    labelKey: 'usage_analytics.metric_estimated_cost',
    color: '#f97316',
    axis: 'cost',
  },
];

export const DEFAULT_SELECTED_METRICS: UsageMetricKey[] = [
  'requestCount',
  'totalTokens',
  'estimatedCost',
];

export const USAGE_MATRIX_DIMENSIONS: UsageMatrixDimension[] = [
  'apiKeyModel',
  'authFileModel',
  'providerModel',
];

export const USAGE_MATRIX_METRICS: UsageMatrixMetricKey[] = [
  'requestCount',
  'totalTokens',
  'estimatedCost',
  'failureRate',
];

export const USAGE_HEATMAP_METRICS: UsageHeatmapMetricKey[] = [
  'requestCount',
  'totalTokens',
  'estimatedCost',
  'failureRate',
];

export const USAGE_HEATMAP_SCALE_MODES: UsageHeatmapScaleMode[] = [
  'absolute',
  'byWeekday',
  'byHour',
];

const HOUR_MS = 60 * 60 * 1000;
const DAY_MS = 24 * HOUR_MS;
const USAGE_HEATMAP_FAILURE_MIN_REQUESTS = 5;

const toNumber = (value: unknown): number => {
  const parsed = typeof value === 'number' ? value : Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
};

const toNullableNumber = (value: unknown): number | null => {
  if (value === null || value === undefined) return null;
  const parsed = toNumber(value);
  return Number.isFinite(parsed) ? parsed : null;
};

const normalizeSelectValue = (value: string | null | undefined) => {
  const trimmed = String(value ?? '').trim();
  return trimmed || 'all';
};

const isActiveSelectValue = (value: string | null | undefined) => {
  const normalized = normalizeSelectValue(value);
  return normalized !== 'all';
};

const normalizeLowerSelectValue = (value: string) => value.trim().toLowerCase();

export const padDateUnit = (value: number) => String(value).padStart(2, '0');

export const formatDateTimeLocalValue = (date: Date) =>
  `${date.getFullYear()}-${padDateUnit(date.getMonth() + 1)}-${padDateUnit(date.getDate())}T${padDateUnit(date.getHours())}:${padDateUnit(date.getMinutes())}`;

export const parseDateTimeLocalValue = (value: string) => {
  if (!value) return null;
  const timestamp = new Date(value).getTime();
  return Number.isFinite(timestamp) ? timestamp : null;
};

export const formatLocalBucketLabel = (
  timestampMs: number,
  granularity: UsageAnalyticsResolvedGranularity
) => {
  const date = new Date(timestampMs);
  if (granularity === 'day') {
    return `${padDateUnit(date.getMonth() + 1)}/${padDateUnit(date.getDate())}`;
  }
  return `${padDateUnit(date.getMonth() + 1)}/${padDateUnit(date.getDate())} ${padDateUnit(date.getHours())}:00`;
};

export const formatLocalDateTime = (timestampMs: number, locale: string) =>
  new Intl.DateTimeFormat(locale, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(timestampMs));

const localDayStartMs = (timestampMs: number) => {
  const date = new Date(timestampMs);
  date.setHours(0, 0, 0, 0);
  return date.getTime();
};

export const getUsageRangeBounds = (
  filters: Pick<UsageAnalyticsFiltersState, 'timeRange' | 'customRange'>,
  nowMs: number
) => {
  if (filters.timeRange === 'custom') {
    const range = filters.customRange;
    if (
      !range ||
      !Number.isFinite(range.startMs) ||
      !Number.isFinite(range.endMs) ||
      range.startMs >= range.endMs
    ) {
      return null;
    }
    return { fromMs: range.startMs, toMs: range.endMs };
  }

  switch (filters.timeRange) {
    case '24h':
      return { fromMs: nowMs - DAY_MS, toMs: nowMs };
    case 'today':
      return { fromMs: localDayStartMs(nowMs), toMs: nowMs };
    case 'yesterday': {
      const todayStart = localDayStartMs(nowMs);
      return { fromMs: todayStart - DAY_MS, toMs: todayStart };
    }
    case '30d':
      return { fromMs: nowMs - 30 * DAY_MS, toMs: nowMs };
    case '7d':
    default:
      return { fromMs: nowMs - 7 * DAY_MS, toMs: nowMs };
  }
};

export const resolveUsageGranularity = (
  filters: Pick<UsageAnalyticsFiltersState, 'timeRange' | 'customRange' | 'granularity'>,
  nowMs: number
): UsageAnalyticsResolvedGranularity => {
  if (filters.granularity === 'hour' || filters.granularity === 'day') {
    return filters.granularity;
  }

  if (filters.timeRange === '30d') return 'day';
  if (
    filters.timeRange === '24h' ||
    filters.timeRange === 'today' ||
    filters.timeRange === 'yesterday' ||
    filters.timeRange === '7d'
  ) {
    return 'hour';
  }

  const bounds = getUsageRangeBounds(filters, nowMs);
  if (!bounds) return 'hour';
  return bounds.toMs - bounds.fromMs <= 7 * DAY_MS ? 'hour' : 'day';
};

export const buildUsageAnalyticsFilters = (
  filters: Partial<
    Pick<
      UsageAnalyticsFiltersState,
      'model' | 'apiKeyHash' | 'provider' | 'authFile' | 'status' | 'minLatencyMs' | 'cacheStatus'
    >
  >
): MonitoringAnalyticsFilters => {
  const payload: MonitoringAnalyticsFilters = {};
  const model = filters.model ?? 'all';
  const apiKeyHash = filters.apiKeyHash ?? 'all';
  const provider = filters.provider ?? 'all';
  const authFile = filters.authFile ?? 'all';
  if (isActiveSelectValue(model)) {
    payload.models = [model.trim()];
  }
  if (isActiveSelectValue(apiKeyHash)) {
    payload.api_key_hashes = [normalizeLowerSelectValue(apiKeyHash)];
  }
  if (isActiveSelectValue(provider)) {
    payload.providers = [normalizeLowerSelectValue(provider)];
  }
  if (isActiveSelectValue(authFile)) {
    payload.auth_files = [authFile.trim()];
  }
  if (filters.status === 'success') {
    payload.include_failed = false;
  } else if (filters.status === 'failed') {
    payload.failed_only = true;
  }
  const minLatencyMs = filters.minLatencyMs ?? 'all';
  if (minLatencyMs !== 'all') {
    payload.min_latency_ms = Number(minLatencyMs);
  }
  const cacheStatus = filters.cacheStatus ?? 'all';
  if (cacheStatus !== 'all') {
    payload.cache_status = cacheStatus;
  }
  return payload;
};

export const buildUsageAnalyticsInclude = (
  activeTab: UsageAnalyticsTab,
  granularity: UsageAnalyticsResolvedGranularity,
  drilldownPreview?: { fromMs: number; toMs: number; limit?: number } | null
): MonitoringAnalyticsInclude => {
  const include: MonitoringAnalyticsInclude = {
    summary: true,
    summary_profile: 'compact',
    granularity,
  };

  switch (activeTab) {
    case 'overview':
      Object.assign(include, {
        summary_percentiles: true,
        summary_comparison: true,
        timeline: true,
        model_stats: true,
        channel_share: true,
        api_key_stats: true,
        anomaly_points: true,
      });
      break;
    case 'trends':
      Object.assign(include, {
        summary_comparison: true,
        timeline: true,
        model_stats: true,
        api_key_stats: true,
        anomaly_points: true,
      });
      break;
    case 'models':
      Object.assign(include, {
        timeline: true,
        model_stats: true,
        api_key_stats: true,
      });
      break;
    case 'apiKeys':
      include.api_key_stats = true;
      break;
    case 'credentials':
      include.credential_stats = true;
      break;
    case 'heatmap':
      include.heatmap = true;
      break;
  }

  if ((activeTab === 'overview' || activeTab === 'trends') && drilldownPreview) {
    include.drilldown_preview = {
      from_ms: drilldownPreview.fromMs,
      to_ms: drilldownPreview.toMs,
      limit: drilldownPreview.limit ?? 12,
    };
  }
  return include;
};

export const buildUsageAnalyticsFilterSelectorsInclude = (): MonitoringAnalyticsInclude => ({
  filter_options: true,
  filter_selectors: true,
});

export const buildUsageSummary = (
  summary?: MonitoringAnalyticsSummary | null
): UsageSummaryMetrics => ({
  requestCount: toNumber(summary?.total_calls),
  totalTokens: toNumber(summary?.total_tokens),
  inputTokens: toNumber(summary?.input_tokens),
  outputTokens: toNumber(summary?.output_tokens),
  cachedTokens: toNumber(summary?.cached_tokens),
  cacheReadTokens: toNumber(summary?.cache_read_tokens),
  cacheCreationTokens: toNumber(summary?.cache_creation_tokens),
  cacheHitRate:
    typeof summary?.cache_hit_rate === 'number' && Number.isFinite(summary.cache_hit_rate)
      ? Math.min(1, Math.max(0, summary.cache_hit_rate))
      : undefined,
  estimatedCost: toNumber(summary?.total_cost),
  averageCostPerCall: toNumber(summary?.average_cost_per_call),
  successRate: toNumber(summary?.success_rate),
  failureCount: toNumber(summary?.failure_calls),
  averageLatencyMs: toNullableNumber(summary?.average_latency_ms),
  p95LatencyMs: toNullableNumber(summary?.p95_latency_ms),
  p95TtftMs: toNullableNumber(summary?.p95_ttft_ms),
  rpm30m: toNumber(summary?.rpm_30m),
  tpm30m: toNumber(summary?.tpm_30m),
});

const getBucketSizeMs = (granularity: UsageAnalyticsResolvedGranularity) =>
  granularity === 'day' ? DAY_MS : HOUR_MS;

export const computeCacheHitRate = (tokens: {
  modelName?: string;
  inputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  cachedTokens: number;
  cacheHitRate?: number;
}): number => {
  if (typeof tokens.cacheHitRate === 'number' && Number.isFinite(tokens.cacheHitRate)) {
    return Math.min(1, Math.max(0, tokens.cacheHitRate));
  }
  return calculateCacheHitRate(tokens);
};

export const getUsageCacheTokens = (tokens: {
  cachedTokens: unknown;
  cacheReadTokens: unknown;
  cacheCreationTokens: unknown;
}): number =>
  Math.max(toNumber(tokens.cachedTokens), 0) +
  Math.max(toNumber(tokens.cacheReadTokens), 0) +
  Math.max(toNumber(tokens.cacheCreationTokens), 0);

const getRowCacheHitTotals = (row: UsageRankRow) => {
  const hasExplicitTotals =
    typeof row.cacheHitTokens === 'number' &&
    Number.isFinite(row.cacheHitTokens) &&
    typeof row.cacheHitInputTokens === 'number' &&
    Number.isFinite(row.cacheHitInputTokens);
  return hasExplicitTotals
    ? { hitTokens: row.cacheHitTokens ?? 0, inputTokens: row.cacheHitInputTokens ?? 0 }
    : getCacheHitTotals({
        modelName: row.model || row.label,
        inputTokens: row.inputTokens,
        cachedTokens: row.cachedTokens,
        cacheReadTokens: row.cacheReadTokens,
        cacheCreationTokens: row.cacheCreationTokens,
      });
};

const computeRowsCacheHitRate = (rows: UsageRankRow[]): number => {
  const totals = rows.reduce(
    (current, row) => {
      const rowTotals = getRowCacheHitTotals(row);
      current.hitTokens += rowTotals.hitTokens;
      current.inputTokens += rowTotals.inputTokens;
      return current;
    },
    { hitTokens: 0, inputTokens: 0 }
  );
  return calculateCacheHitRateFromTotals(totals.hitTokens, totals.inputTokens);
};

export const buildUsageTimeline = (
  timeline: MonitoringAnalyticsTimelinePoint[] = [],
  granularity: UsageAnalyticsResolvedGranularity
): UsageTimelinePoint[] =>
  timeline.map((point) => {
    const requestCount = toNumber(point.calls);
    const totalTokens = toNumber(point.total_tokens ?? point.tokens);
    const cacheReadTokens = toNumber(point.cache_read_tokens);
    const inputTokens = toNumber(point.input_tokens);
    const successCount = toNumber(point.success);
    const failureCount = toNumber(point.failure);
    const bucketMs = toNumber(point.bucket_ms);
    return {
      bucketMs,
      bucketEndMs: toNumber(point.bucket_end_ms) || bucketMs + getBucketSizeMs(granularity),
      label: formatLocalBucketLabel(bucketMs, granularity),
      requestCount,
      totalTokens,
      inputTokens,
      outputTokens: toNumber(point.output_tokens),
      cachedTokens: toNumber(point.cached_tokens),
      cacheReadTokens,
      cacheCreationTokens: toNumber(point.cache_creation_tokens),
      reasoningTokens: toNumber(point.reasoning_tokens),
      estimatedCost: toNumber(point.cost),
      successCount,
      failureCount,
      successRate:
        toNumber(point.success_rate) || (requestCount > 0 ? successCount / requestCount : 0),
      failureRate:
        toNumber(point.failure_rate) || (requestCount > 0 ? failureCount / requestCount : 0),
      averageLatencyMs: toNullableNumber(point.average_latency_ms),
      p95LatencyMs: toNullableNumber(point.p95_latency_ms),
      p95TtftMs: toNullableNumber(point.p95_ttft_ms),
      cacheHitRate: computeCacheHitRate({
        inputTokens,
        cacheReadTokens,
        cacheCreationTokens: toNumber(point.cache_creation_tokens),
        cachedTokens: toNumber(point.cached_tokens),
        cacheHitRate: point.cache_hit_rate,
      }),
      averageTokensPerRequest: requestCount > 0 ? totalTokens / requestCount : 0,
    };
  });

export const buildUsageCredentialTimeline = (
  timeline: MonitoringAnalyticsCredentialTimelinePoint[] = [],
  granularity: UsageAnalyticsResolvedGranularity,
  credentialDisplayContext?: UsageCredentialDisplayContext
): UsageCredentialTimelinePoint[] =>
  timeline.map((point) => {
    const bucketMs = toNumber(point.bucket_ms);
    const display = resolveUsageCredentialDisplay(point, credentialDisplayContext);
    return {
      id: point.id || point.auth_file_snapshot || point.auth_index || point.source_hash || '-',
      label: display.label,
      bucketMs,
      bucketLabel: point.bucket_label || formatLocalBucketLabel(bucketMs, granularity),
      requestCount: toNumber(point.calls),
      totalTokens: toNumber(point.total_tokens ?? point.tokens),
      estimatedCost: toNumber(point.cost),
    };
  });

export const buildUsageApiKeyTimeline = (
  timeline: MonitoringAnalyticsApiKeyTimelinePoint[] = [],
  granularity: UsageAnalyticsResolvedGranularity
): UsageApiKeyTimelinePoint[] =>
  timeline.map((point) => {
    const bucketMs = toNumber(point.bucket_ms);
    return {
      apiKeyHash: normalizeApiKeyHash(point.api_key_hash),
      bucketMs,
      bucketLabel: point.bucket_label || formatLocalBucketLabel(bucketMs, granularity),
      requestCount: toNumber(point.calls),
      totalTokens: toNumber(point.total_tokens ?? point.tokens),
      estimatedCost: toNumber(point.cost),
    };
  });

const rowTotalCost = (row: { cost?: number }) => toNumber(row.cost);

const usageRankMetricValue = (row: UsageRankRow, metric: UsageTrendMetricKey) => {
  if (metric === 'estimatedCost') return row.estimatedCost;
  if (metric === 'totalTokens') return row.totalTokens;
  return row.requestCount;
};

const matrixMetricValue = (
  cell: Omit<UsageMatrixCell, 'value' | 'share'>,
  metric: UsageMatrixMetricKey
) => {
  if (metric === 'estimatedCost') return cell.estimatedCost;
  if (metric === 'totalTokens') return cell.totalTokens;
  if (metric === 'failureRate') return cell.failureRate;
  return cell.requestCount;
};

const sumUsageRows = (rows: UsageRankRow[], metric: UsageTrendMetricKey) =>
  rows.reduce((sum, row) => sum + usageRankMetricValue(row, metric), 0);

const safeShare = (value: number, total: number) => (total > 0 ? value / total : 0);

export const getUsageHeatmapMetricValue = (
  point: UsageHeatmapPoint,
  metric: UsageHeatmapMetricKey
) => {
  if (metric === 'estimatedCost') return point.estimatedCost;
  if (metric === 'totalTokens') return point.totalTokens;
  if (metric === 'failureRate') return point.failureRate;
  return point.requestCount;
};

const isValidUsageHeatmapPoint = (point: UsageHeatmapPoint) =>
  point.requestCount > 0 &&
  Number.isInteger(point.weekday) &&
  point.weekday >= 0 &&
  point.weekday < 7 &&
  Number.isInteger(point.hour) &&
  point.hour >= 0 &&
  point.hour < 24;

const heatmapBaseline = (points: UsageHeatmapPoint[], metric: UsageHeatmapMetricKey) => {
  if (points.length === 0) return 0;
  if (metric === 'failureRate') {
    const failures = points.reduce((sum, point) => sum + point.failureCount, 0);
    const requests = points.reduce((sum, point) => sum + point.requestCount, 0);
    return safeShare(failures, requests);
  }
  return (
    points.reduce((sum, point) => sum + getUsageHeatmapMetricValue(point, metric), 0) /
    points.length
  );
};

const metricDelta = (current: number, baseline: number) => {
  if (baseline <= 0) return current > 0 ? 1 : 0;
  return (current - baseline) / baseline;
};

const usageHeatmapPointId = (point: Pick<UsageHeatmapPoint, 'weekday' | 'hour'>) =>
  `${point.weekday}-${point.hour}`;

const USAGE_HEATMAP_SAMPLE_PREVIEW_LIMIT = 4;
const WEEKDAY_INDEX_BY_SHORT_NAME: Record<string, number> = {
  Fri: 5,
  Mon: 1,
  Sat: 6,
  Sun: 0,
  Thu: 4,
  Tue: 2,
  Wed: 3,
};

const normalizeIntlTimeZone = (timeZone: string | null | undefined) => {
  const candidate = String(timeZone ?? '').trim() || 'UTC';
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: candidate }).format(new Date(0));
    return candidate;
  } catch {
    return 'UTC';
  }
};

const partValue = (parts: Intl.DateTimeFormatPart[], type: Intl.DateTimeFormatPartTypes) =>
  parts.find((part) => part.type === type)?.value ?? '';

const getHeatmapZonedHour = (timestampMs: number, timeZone: string) => {
  const parts = new Intl.DateTimeFormat('en-US', {
    day: '2-digit',
    hour: '2-digit',
    hourCycle: 'h23',
    month: '2-digit',
    timeZone,
    weekday: 'short',
    year: 'numeric',
  }).formatToParts(new Date(timestampMs));
  const weekday = WEEKDAY_INDEX_BY_SHORT_NAME[partValue(parts, 'weekday')];
  const hour = Number(partValue(parts, 'hour'));
  const year = partValue(parts, 'year');
  const month = partValue(parts, 'month');
  const day = partValue(parts, 'day');
  if (
    !Number.isInteger(weekday) ||
    !Number.isInteger(hour) ||
    hour < 0 ||
    hour > 23 ||
    !year ||
    !month ||
    !day
  ) {
    return null;
  }
  return {
    dateKey: `${year}-${month}-${day}`,
    hour,
    weekday,
  };
};

const formatHeatmapContextDateTime = (timestampMs: number, locale: string, timeZone: string) =>
  new Intl.DateTimeFormat(locale || undefined, {
    day: '2-digit',
    hour: '2-digit',
    hour12: false,
    minute: '2-digit',
    month: '2-digit',
    timeZone,
  }).format(new Date(timestampMs));

const formatHeatmapSampleDateLabel = (timestampMs: number, locale: string, timeZone: string) =>
  new Intl.DateTimeFormat(locale || undefined, {
    day: '2-digit',
    month: '2-digit',
    timeZone,
  }).format(new Date(timestampMs));

export const buildUsageHeatmapRangeContext = (
  bounds: { fromMs?: number; toMs?: number } | null | undefined,
  locale: string,
  timeZone: string
): UsageHeatmapRangeContext => {
  const fromMs = Number(bounds?.fromMs);
  const toMs = Number(bounds?.toMs);
  if (!Number.isFinite(fromMs) || !Number.isFinite(toMs) || fromMs >= toMs) {
    return {
      cellSamples: {},
      dateOptions: [],
      dayCount: 0,
      fromLabel: '-',
      rangeLabel: '-',
      sampleWindowCount: 0,
      toLabel: '-',
    };
  }

  const safeTimeZone = normalizeIntlTimeZone(timeZone);
  const cellSamples: Record<string, UsageHeatmapCellSample> = {};
  const dateOptionsByKey = new Map<string, UsageHeatmapDateOption>();
  const dateKeys = new Set<string>();
  const startMs = Math.floor(fromMs / HOUR_MS) * HOUR_MS;
  let sampleWindowCount = 0;

  for (let timestampMs = startMs; timestampMs < toMs; timestampMs += HOUR_MS) {
    if (timestampMs + HOUR_MS <= fromMs) continue;
    const zonedHour = getHeatmapZonedHour(timestampMs, safeTimeZone);
    if (!zonedHour) continue;

    sampleWindowCount += 1;
    dateKeys.add(zonedHour.dateKey);

    const clippedFromMs = Math.max(timestampMs, fromMs);
    const clippedToMs = Math.min(timestampMs + HOUR_MS, toMs);
    if (clippedFromMs < clippedToMs) {
      const dateLabel = formatHeatmapSampleDateLabel(timestampMs, locale, safeTimeZone);
      const dateOption = dateOptionsByKey.get(zonedHour.dateKey);
      if (dateOption) {
        dateOption.fromMs = Math.min(dateOption.fromMs, clippedFromMs);
        dateOption.toMs = Math.max(dateOption.toMs, clippedToMs);
        dateOption.sampleWindowCount += 1;
      } else {
        dateOptionsByKey.set(zonedHour.dateKey, {
          key: zonedHour.dateKey,
          label: dateLabel,
          fromMs: clippedFromMs,
          toMs: clippedToMs,
          sampleWindowCount: 1,
        });
      }
    }

    const cellKey = `${zonedHour.weekday}-${zonedHour.hour}`;
    const sample =
      cellSamples[cellKey] ??
      ({
        dateKeys: [],
        dateLabels: [],
        overflowCount: 0,
        sampleCount: 0,
      } satisfies UsageHeatmapCellSample);
    sample.sampleCount += 1;
    if (!sample.dateKeys.includes(zonedHour.dateKey)) {
      sample.dateKeys.push(zonedHour.dateKey);
    }

    const dateLabel = formatHeatmapSampleDateLabel(timestampMs, locale, safeTimeZone);
    if (
      sample.dateLabels.length < USAGE_HEATMAP_SAMPLE_PREVIEW_LIMIT &&
      !sample.dateLabels.includes(dateLabel)
    ) {
      sample.dateLabels.push(dateLabel);
    }
    cellSamples[cellKey] = sample;
  }

  Object.values(cellSamples).forEach((sample) => {
    sample.overflowCount = Math.max(0, sample.sampleCount - sample.dateLabels.length);
  });

  const fromLabel = formatHeatmapContextDateTime(fromMs, locale, safeTimeZone);
  const toLabel = formatHeatmapContextDateTime(toMs, locale, safeTimeZone);
  return {
    cellSamples,
    dateOptions: Array.from(dateOptionsByKey.values()).sort(
      (left, right) => left.fromMs - right.fromMs
    ),
    dayCount: dateKeys.size,
    fromLabel,
    rangeLabel: `${fromLabel} - ${toLabel}`,
    sampleWindowCount,
    toLabel,
  };
};

export const buildUsageHeatmapCellDateOptions = (
  context: UsageHeatmapRangeContext,
  selection: UsageHeatmapCellSelection | null
): UsageHeatmapDateOption[] => {
  if (!selection) return context.dateOptions;
  const sample = context.cellSamples[`${selection.weekday}-${selection.hour}`];
  if (!sample) return [];
  const dateKeys = new Set(sample.dateKeys);
  return context.dateOptions.filter((option) => dateKeys.has(option.key));
};

const normalizeProviderLabel = (value: string | undefined) => {
  const normalized = String(value ?? '').trim();
  return normalized || 'Unknown';
};

const normalizeMatrixLabel = (value: string | undefined, fallback = '-') => {
  const normalized = String(value ?? '').trim();
  return normalized || fallback;
};

export const buildModelRows = (
  rows: MonitoringAnalyticsModelStat[] = [],
  summary?: UsageSummaryMetrics
): UsageRankRow[] => {
  const totalCost = summary?.estimatedCost ?? rows.reduce((sum, row) => sum + rowTotalCost(row), 0);
  const totalTokens =
    summary?.totalTokens ?? rows.reduce((sum, row) => sum + toNumber(row.total_tokens), 0);
  return rows
    .map((row) => ({
      id: row.model || '-',
      label: row.model || '-',
      model: row.model || '-',
      requestCount: toNumber(row.calls),
      successCount: toNumber(row.success_calls),
      failureCount: toNumber(row.failure_calls),
      successRate: toNumber(row.success_rate),
      totalTokens: toNumber(row.total_tokens),
      inputTokens: toNumber(row.input_tokens),
      outputTokens: toNumber(row.output_tokens),
      cachedTokens: toNumber(row.cached_tokens),
      cacheReadTokens: toNumber(row.cache_read_tokens),
      cacheCreationTokens: toNumber(row.cache_creation_tokens),
      cacheHitTokens: row.cache_hit_tokens,
      cacheHitInputTokens: row.cache_hit_input_tokens,
      cacheHitRate: row.cache_hit_rate,
      estimatedCost: rowTotalCost(row),
      averageLatencyMs: null,
      share:
        totalCost > 0
          ? rowTotalCost(row) / totalCost
          : totalTokens > 0
            ? toNumber(row.total_tokens) / totalTokens
            : 0,
    }))
    .sort(
      (left, right) =>
        right.estimatedCost - left.estimatedCost ||
        right.requestCount - left.requestCount ||
        left.label.localeCompare(right.label)
    );
};

export const maskApiKeyHash = (hash: string | null | undefined) => {
  const value = String(hash ?? '').trim();
  if (!value) return '-';
  return `sk-****${value.slice(-4)}`;
};

export type UsageApiKeyDisplayMap = ReadonlyMap<string, ApiKeyDisplayInfo>;

export type UsageCredentialDisplayContext = {
  authMetaMap: Map<string, MonitoringAuthMeta>;
  authFileMap: Map<string, CredentialInfo>;
  sourceInfoMap: ReturnType<typeof buildSourceInfoMap>;
  channelByAuthIndex: Map<string, MonitoringChannelMeta>;
};

const normalizeApiKeyHash = (value: string | null | undefined) =>
  String(value ?? '')
    .trim()
    .toLowerCase();

const isSameApiKeyIdentity = (label: string, hash: string) =>
  Boolean(label) && Boolean(hash) && label.trim().toLowerCase() === hash;

export const resolveUsageApiKeyLabel = (
  apiKeyHash: string | null | undefined,
  apiKeyDisplayMap?: UsageApiKeyDisplayMap,
  fallbackLabel?: string | null
) => {
  const hash = normalizeApiKeyHash(apiKeyHash);
  const fallback = sanitizeApiKeyDisplayText(String(fallbackLabel ?? ''));

  if (!hash) {
    return fallback || '-';
  }

  const display = apiKeyDisplayMap?.get(hash);
  const displayLabel = sanitizeApiKeyDisplayText(display?.label || display?.masked || '');
  if (displayLabel && !isSameApiKeyIdentity(displayLabel, hash)) {
    return displayLabel;
  }

  if (fallback && !isSameApiKeyIdentity(fallback, hash)) {
    return fallback;
  }

  return maskApiKeyHash(hash);
};

type UsageCredentialDisplayInput = {
  id?: string;
  label?: string;
  auth_file_snapshot?: string;
  auth_index?: string;
  source?: string;
  source_hash?: string;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_provider_snapshot?: string;
};

const isReadableCredentialLabel = (value: string | null | undefined) => {
  const trimmed = String(value ?? '').trim();
  return Boolean(trimmed) && trimmed !== '-';
};

const firstReadableCredentialLabel = (...values: Array<string | null | undefined>) =>
  values.find(isReadableCredentialLabel)?.trim() || '';

const resolveUsageCredentialDisplay = (
  row: UsageCredentialDisplayInput,
  context?: UsageCredentialDisplayContext
) => {
  const fallbackLabel =
    firstReadableCredentialLabel(
      row.label,
      row.auth_label_snapshot,
      row.account_snapshot,
      row.auth_file_snapshot,
      row.source,
      row.auth_index,
      row.id
    ) || '-';

  if (!context) {
    return {
      label: fallbackLabel,
      account: firstReadableCredentialLabel(row.account_snapshot, row.auth_label_snapshot),
      provider: row.auth_provider_snapshot,
    };
  }

  const display = buildMonitoringSourceDisplay(
    {
      source: row.source,
      sourceHash: row.source_hash,
      authIndex: row.auth_index,
      accountSnapshot: row.account_snapshot,
      authLabelSnapshot: row.auth_label_snapshot,
      authProviderSnapshot: row.auth_provider_snapshot,
    },
    context
  );

  const label =
    firstReadableCredentialLabel(display.sourceLabel, display.primary, fallbackLabel) || '-';

  return {
    label,
    account: firstReadableCredentialLabel(display.account, row.account_snapshot, label),
    provider: firstReadableCredentialLabel(display.provider, row.auth_provider_snapshot),
  };
};

const buildModelSpendRows = (
  rows:
    | NonNullable<MonitoringAnalyticsApiKeyStatRow['models']>
    | NonNullable<MonitoringAnalyticsCredentialStatRow['models']>
    | undefined
): UsageRankRow[] =>
  (rows ?? []).map((row) => ({
    id: row.model || '-',
    label: row.model || '-',
    model: row.model || '-',
    requestCount: toNumber(row.calls),
    successCount: toNumber(row.success_calls),
    failureCount: toNumber(row.failure_calls),
    successRate: toNumber(row.success_rate),
    totalTokens: toNumber(row.total_tokens),
    inputTokens: toNumber(row.input_tokens),
    outputTokens: toNumber(row.output_tokens),
    cachedTokens: toNumber(row.cached_tokens),
    cacheReadTokens: toNumber(row.cache_read_tokens),
    cacheCreationTokens: toNumber(row.cache_creation_tokens),
    cacheHitTokens: row.cache_hit_tokens,
    cacheHitInputTokens: row.cache_hit_input_tokens,
    cacheHitRate: row.cache_hit_rate,
    estimatedCost: rowTotalCost(row),
    averageLatencyMs: null,
    lastSeenMs: row.last_seen_ms,
    share: 0,
  }));

const buildApiKeyContextRows = (
  rows: NonNullable<MonitoringAnalyticsApiKeyStatRow['contexts']> | undefined
): UsageApiKeyContextRow[] =>
  (rows ?? []).map((row) => ({
    id: row.id || '-',
    provider: row.auth_provider_snapshot,
    account: row.account_snapshot || row.auth_label_snapshot,
    authIndex: row.auth_index,
    source: row.source,
    sourceHash: row.source_hash,
    requestCount: toNumber(row.calls),
    successCount: toNumber(row.success_calls),
    failureCount: toNumber(row.failure_calls),
    successRate: toNumber(row.success_rate),
    failureRate:
      row.failure_rate === undefined
        ? safeShare(toNumber(row.failure_calls), toNumber(row.calls))
        : toNumber(row.failure_rate),
    totalTokens: toNumber(row.total_tokens),
    estimatedCost: toNumber(row.cost),
    averageLatencyMs: row.average_latency_ms ?? null,
    lastSeenMs: row.last_seen_ms,
  }));

export const buildApiKeyRows = (
  rows: MonitoringAnalyticsApiKeyStatRow[] = [],
  summary?: UsageSummaryMetrics,
  keyword = '',
  apiKeyDisplayMap?: UsageApiKeyDisplayMap
): UsageRankRow[] => {
  const normalizedKeyword = keyword.trim().toLowerCase();
  const totalCost = summary?.estimatedCost ?? rows.reduce((sum, row) => sum + rowTotalCost(row), 0);
  const totalTokens =
    summary?.totalTokens ?? rows.reduce((sum, row) => sum + toNumber(row.total_tokens), 0);
  return rows
    .filter((row) => {
      const hash = normalizeApiKeyHash(row.api_key_hash || row.id || '');
      const label = resolveUsageApiKeyLabel(hash, apiKeyDisplayMap);
      if (!normalizedKeyword) return true;
      const haystack = [hash, label, row.account_snapshot, row.auth_label_snapshot]
        .join(' ')
        .toLowerCase();
      return haystack.includes(normalizedKeyword);
    })
    .map((row) => {
      const hash = normalizeApiKeyHash(row.api_key_hash || row.id || '');
      return {
        id: hash || row.id || '-',
        label: resolveUsageApiKeyLabel(hash, apiKeyDisplayMap),
        apiKeyHash: hash,
        apiKeyCopyValue: apiKeyDisplayMap?.get(hash)?.copyValue,
        provider: row.auth_provider_snapshot,
        authIndex: row.auth_indices?.[0],
        source: row.sources?.[0],
        sourceHash: row.source_hashes?.[0],
        account: row.account_snapshot || row.auth_label_snapshot,
        requestCount: toNumber(row.calls),
        successCount: toNumber(row.success_calls),
        failureCount: toNumber(row.failure_calls),
        successRate: toNumber(row.success_rate),
        totalTokens: toNumber(row.total_tokens),
        inputTokens: toNumber(row.input_tokens),
        outputTokens: toNumber(row.output_tokens),
        cachedTokens: toNumber(row.cached_tokens),
        cacheReadTokens: toNumber(row.cache_read_tokens),
        cacheCreationTokens: toNumber(row.cache_creation_tokens),
        estimatedCost: rowTotalCost(row),
        averageLatencyMs: row.average_latency_ms ?? null,
        lastSeenMs: row.last_seen_ms,
        share:
          totalCost > 0
            ? rowTotalCost(row) / totalCost
            : totalTokens > 0
              ? toNumber(row.total_tokens) / totalTokens
              : 0,
        contexts: buildApiKeyContextRows(row.contexts),
        models: buildModelSpendRows(row.models),
      };
    })
    .sort(
      (left, right) =>
        right.estimatedCost - left.estimatedCost ||
        right.requestCount - left.requestCount ||
        left.label.localeCompare(right.label)
    );
};

export const buildCredentialRows = (
  rows: MonitoringAnalyticsCredentialStatRow[] = [],
  summary?: UsageSummaryMetrics,
  credentialDisplayContext?: UsageCredentialDisplayContext
): UsageRankRow[] => {
  const totalCost = summary?.estimatedCost ?? rows.reduce((sum, row) => sum + rowTotalCost(row), 0);
  const totalTokens =
    summary?.totalTokens ?? rows.reduce((sum, row) => sum + toNumber(row.total_tokens), 0);
  return rows
    .map((row) => {
      const display = resolveUsageCredentialDisplay(row, credentialDisplayContext);
      const label = display.label;
      return {
        id: row.id || label,
        label,
        provider: display.provider || row.auth_provider_snapshot,
        authFile: row.auth_file_snapshot,
        authIndex: row.auth_index,
        source: row.source,
        sourceHash: row.source_hash,
        account: display.account || row.account_snapshot || row.auth_label_snapshot,
        projectId: row.auth_project_id_snapshot,
        requestCount: toNumber(row.calls),
        successCount: toNumber(row.success_calls),
        failureCount: toNumber(row.failure_calls),
        successRate: toNumber(row.success_rate),
        totalTokens: toNumber(row.total_tokens),
        inputTokens: toNumber(row.input_tokens),
        outputTokens: toNumber(row.output_tokens),
        cachedTokens: toNumber(row.cached_tokens),
        cacheReadTokens: toNumber(row.cache_read_tokens),
        cacheCreationTokens: toNumber(row.cache_creation_tokens),
        estimatedCost: rowTotalCost(row),
        averageLatencyMs: row.average_latency_ms ?? null,
        lastSeenMs: row.last_seen_ms,
        share:
          totalCost > 0
            ? rowTotalCost(row) / totalCost
            : totalTokens > 0
              ? toNumber(row.total_tokens) / totalTokens
              : 0,
        models: buildModelSpendRows(row.models),
      };
    })
    .sort(
      (left, right) =>
        right.estimatedCost - left.estimatedCost ||
        right.requestCount - left.requestCount ||
        left.label.localeCompare(right.label)
    );
};

const buildProviderModelsFromEntities = (
  rows: UsageRankRow[]
): Map<string, Map<string, UsageRankRow>> => {
  const providerModels = new Map<string, Map<string, UsageRankRow>>();
  rows.forEach((row) => {
    const provider = normalizeProviderLabel(row.provider);
    const models = row.models && row.models.length > 0 ? row.models : [];
    if (models.length === 0) return;
    let modelMap = providerModels.get(provider);
    if (!modelMap) {
      modelMap = new Map<string, UsageRankRow>();
      providerModels.set(provider, modelMap);
    }
    models.forEach((model) => {
      const key = model.model || model.label || '-';
      const existing = modelMap.get(key);
      if (!existing) {
        modelMap.set(key, { ...model, id: key, label: key, model: key });
        return;
      }
      const existingCacheTotals = getRowCacheHitTotals(existing);
      const modelCacheTotals = getRowCacheHitTotals(model);
      existing.requestCount += model.requestCount;
      existing.successCount += model.successCount;
      existing.failureCount += model.failureCount;
      existing.successRate = safeShare(existing.successCount, existing.requestCount);
      existing.totalTokens += model.totalTokens;
      existing.inputTokens += model.inputTokens;
      existing.outputTokens += model.outputTokens;
      existing.cachedTokens += model.cachedTokens;
      existing.cacheReadTokens += model.cacheReadTokens;
      existing.cacheCreationTokens += model.cacheCreationTokens;
      existing.cacheHitTokens = existingCacheTotals.hitTokens + modelCacheTotals.hitTokens;
      existing.cacheHitInputTokens = existingCacheTotals.inputTokens + modelCacheTotals.inputTokens;
      existing.cacheHitRate = calculateCacheHitRateFromTotals(
        existing.cacheHitTokens,
        existing.cacheHitInputTokens
      );
      existing.estimatedCost += model.estimatedCost;
      existing.lastSeenMs = Math.max(existing.lastSeenMs ?? 0, model.lastSeenMs ?? 0);
    });
  });
  return providerModels;
};

export const buildProviderRows = (
  rows: MonitoringAnalyticsChannelShareRow[] = [],
  apiKeyRows: UsageRankRow[] = [],
  credentialRows: UsageRankRow[] = [],
  summary?: UsageSummaryMetrics
): UsageProviderRow[] => {
  const providerModels = buildProviderModelsFromEntities(
    apiKeyRows.some((row) => row.models?.length) ? apiKeyRows : credentialRows
  );
  const grouped = new Map<string, UsageProviderRow>();
  const fallbackRows = new Map<string, UsageRankRow[]>();
  rows.forEach((row) => {
    const label = normalizeProviderLabel(row.auth_provider_snapshot);
    const current =
      grouped.get(label) ??
      ({
        id: label,
        label,
        requestCount: 0,
        successCount: 0,
        failureCount: 0,
        successRate: 0,
        cacheRate: 0,
        totalTokens: 0,
        estimatedCost: 0,
        averageLatencyMs: null,
        requestShare: 0,
        costShare: 0,
        tokenShare: 0,
        share: 0,
        models: [],
      } satisfies UsageProviderRow);
    const requestCount = toNumber(row.calls);
    current.requestCount += requestCount;
    current.successCount += toNumber(row.success);
    current.failureCount += toNumber(row.failure);
    current.totalTokens += toNumber(row.tokens);
    current.estimatedCost += rowTotalCost(row);
    if (row.average_latency_ms !== null && row.average_latency_ms !== undefined) {
      current.averageLatencyMs =
        current.averageLatencyMs === null
          ? row.average_latency_ms
          : (current.averageLatencyMs + row.average_latency_ms) / 2;
    }
    grouped.set(label, current);
  });

  if (grouped.size === 0) {
    [...apiKeyRows, ...credentialRows].forEach((row) => {
      const label = normalizeProviderLabel(row.provider);
      const current =
        grouped.get(label) ??
        ({
          id: label,
          label,
          requestCount: 0,
          successCount: 0,
          failureCount: 0,
          successRate: 0,
          cacheRate: 0,
          totalTokens: 0,
          estimatedCost: 0,
          averageLatencyMs: null,
          requestShare: 0,
          costShare: 0,
          tokenShare: 0,
          share: 0,
          models: [],
        } satisfies UsageProviderRow);
      current.requestCount += row.requestCount;
      current.successCount += row.successCount;
      current.failureCount += row.failureCount;
      current.totalTokens += row.totalTokens;
      current.estimatedCost += row.estimatedCost;
      fallbackRows.set(label, [...(fallbackRows.get(label) ?? []), row]);
      grouped.set(label, current);
    });
  }

  const totalRequests =
    summary?.requestCount ?? [...grouped.values()].reduce((sum, row) => sum + row.requestCount, 0);
  const totalCost =
    summary?.estimatedCost ??
    [...grouped.values()].reduce((sum, row) => sum + row.estimatedCost, 0);
  const totalTokens =
    summary?.totalTokens ?? [...grouped.values()].reduce((sum, row) => sum + row.totalTokens, 0);
  return [...grouped.values()]
    .map((row) => {
      const models = [...(providerModels.get(row.label)?.values() ?? [])].sort(
        (left, right) =>
          right.estimatedCost - left.estimatedCost ||
          right.requestCount - left.requestCount ||
          left.label.localeCompare(right.label)
      );
      const modelTotal = models.reduce((sum, model) => sum + model.totalTokens, 0);
      const requestShare = safeShare(row.requestCount, totalRequests);
      return {
        ...row,
        successRate: safeShare(row.successCount, row.requestCount),
        cacheRate:
          models.length > 0
            ? computeRowsCacheHitRate(models)
            : computeRowsCacheHitRate(fallbackRows.get(row.label) ?? []),
        requestShare,
        costShare: safeShare(row.estimatedCost, totalCost),
        tokenShare: safeShare(row.totalTokens, totalTokens),
        share: requestShare,
        models: models.map((model) => ({
          ...model,
          share: safeShare(model.totalTokens, modelTotal),
        })),
      };
    })
    .sort(
      (left, right) =>
        right.requestCount - left.requestCount ||
        right.estimatedCost - left.estimatedCost ||
        left.label.localeCompare(right.label)
    );
};

const buildMatrixCellsFromEntityRows = (
  rows: UsageRankRow[],
  getRowLabel: (row: UsageRankRow) => string,
  metric: UsageMatrixMetricKey
): UsageMatrixCell[] => {
  const grouped = new Map<string, Omit<UsageMatrixCell, 'value' | 'share'>>();
  rows.forEach((row) => {
    (row.models ?? []).forEach((model) => {
      const rowLabel = getRowLabel(row);
      const columnLabel = normalizeMatrixLabel(model.model || model.label);
      const key = `${rowLabel}\n${columnLabel}`;
      const current =
        grouped.get(key) ??
        ({
          rowId: rowLabel,
          rowLabel,
          columnId: columnLabel,
          columnLabel,
          requestCount: 0,
          successCount: 0,
          failureCount: 0,
          totalTokens: 0,
          estimatedCost: 0,
          failureRate: 0,
        } satisfies Omit<UsageMatrixCell, 'value' | 'share'>);
      current.requestCount += model.requestCount;
      current.successCount += model.successCount;
      current.failureCount += model.failureCount;
      current.totalTokens += model.totalTokens;
      current.estimatedCost += model.estimatedCost;
      current.failureRate = safeShare(current.failureCount, current.requestCount);
      grouped.set(key, current);
    });
  });
  return [...grouped.values()].map((cell) => ({
    ...cell,
    value: matrixMetricValue(cell, metric),
    share: 0,
  }));
};

export const buildUsageMatrix = ({
  apiKeyRows,
  credentialRows,
  dimension,
  metric,
}: {
  apiKeyRows: UsageRankRow[];
  credentialRows: UsageRankRow[];
  dimension: UsageMatrixDimension;
  metric: UsageMatrixMetricKey;
}): UsageMatrix => {
  const providerModelRows = apiKeyRows.some((row) => row.models?.length)
    ? apiKeyRows
    : credentialRows;
  const sourceRows =
    dimension === 'authFileModel'
      ? credentialRows
      : dimension === 'providerModel'
        ? providerModelRows
        : apiKeyRows;
  const cells = buildMatrixCellsFromEntityRows(
    sourceRows,
    (row) => {
      if (dimension === 'apiKeyModel') {
        return normalizeMatrixLabel(
          resolveUsageApiKeyLabel(row.apiKeyHash || row.id, undefined, row.label)
        );
      }
      if (dimension === 'authFileModel') return normalizeMatrixLabel(row.authFile || row.label);
      return normalizeProviderLabel(row.provider);
    },
    metric
  );
  const orderedRows = Array.from(
    new Map(
      [...cells]
        .sort((left, right) => right.value - left.value)
        .map((cell) => [cell.rowLabel, cell.rowLabel] as const)
    ).values()
  ).slice(0, 8);
  const orderedColumns = Array.from(
    new Map(
      [...cells]
        .sort((left, right) => right.value - left.value)
        .map((cell) => [cell.columnLabel, cell.columnLabel] as const)
    ).values()
  ).slice(0, 8);
  const visible = cells.filter(
    (cell) => orderedRows.includes(cell.rowLabel) && orderedColumns.includes(cell.columnLabel)
  );
  const totalValue = visible.reduce((sum, cell) => sum + cell.value, 0);
  const maxValue = Math.max(1, ...visible.map((cell) => cell.value));
  return {
    dimension,
    metric,
    rowLabels: orderedRows,
    columnLabels: orderedColumns,
    cells: visible.map((cell) => ({ ...cell, share: safeShare(cell.value, totalValue) })),
    maxValue,
    totalValue,
  };
};

export const buildEntityTrendSeries = (
  rows: UsageRankRow[],
  timeline: UsageTimelinePoint[],
  metric: UsageTrendMetricKey,
  limit = 4
): UsageEntityTrendSeries[] => {
  const total = sumUsageRows(rows, metric);
  const colors = ['#2563eb', '#0ea5a7', '#f59e0b', '#ef4444', '#64748b'];
  return rows
    .filter((row) => usageRankMetricValue(row, metric) > 0)
    .slice(0, limit)
    .map((row, index) => {
      const share = safeShare(usageRankMetricValue(row, metric), total);
      return {
        id: row.id,
        label: row.label,
        color: colors[index % colors.length],
        points: timeline.map((point) => ({
          bucketMs: point.bucketMs,
          label: point.label,
          value: point[metric] * share,
        })),
      };
    });
};

const usageApiKeyTimelineMetricValue = (
  point: UsageApiKeyTimelinePoint,
  metric: UsageTrendMetricKey
) => {
  if (metric === 'estimatedCost') return point.estimatedCost;
  if (metric === 'totalTokens') return point.totalTokens;
  return point.requestCount;
};

export const buildApiKeyTrendSeries = (
  rows: UsageRankRow[],
  timeline: UsageTimelinePoint[],
  apiKeyTimeline: UsageApiKeyTimelinePoint[],
  metric: UsageTrendMetricKey,
  limit = 4
): UsageEntityTrendSeries[] => {
  const valuesByAPIKey = new Map<string, Map<number, UsageApiKeyTimelinePoint>>();
  apiKeyTimeline.forEach((point) => {
    if (!point.apiKeyHash) return;
    let valuesByBucket = valuesByAPIKey.get(point.apiKeyHash);
    if (!valuesByBucket) {
      valuesByBucket = new Map();
      valuesByAPIKey.set(point.apiKeyHash, valuesByBucket);
    }
    valuesByBucket.set(point.bucketMs, point);
  });
  const colors = ['#2563eb', '#0ea5a7', '#f59e0b', '#ef4444', '#64748b'];
  return rows
    .filter((row) => {
      const apiKeyHash = normalizeApiKeyHash(row.apiKeyHash || row.id);
      return apiKeyHash !== '' && usageRankMetricValue(row, metric) > 0;
    })
    .slice(0, limit)
    .map((row, index) => {
      const apiKeyHash = normalizeApiKeyHash(row.apiKeyHash || row.id);
      const valuesByBucket = valuesByAPIKey.get(apiKeyHash);
      return {
        id: apiKeyHash,
        label: row.label,
        color: colors[index % colors.length],
        points: timeline.map((timelinePoint) => {
          const value = valuesByBucket?.get(timelinePoint.bucketMs);
          return {
            bucketMs: timelinePoint.bucketMs,
            label: timelinePoint.label,
            value: value ? usageApiKeyTimelineMetricValue(value, metric) : 0,
          };
        }),
      };
    });
};

const usageCredentialTimelineMetricValue = (
  point: UsageCredentialTimelinePoint,
  metric: UsageTrendMetricKey
) => {
  if (metric === 'estimatedCost') return point.estimatedCost;
  if (metric === 'totalTokens') return point.totalTokens;
  return point.requestCount;
};

const usageTimelineMetricValue = (point: UsageTimelinePoint, metric: UsageTrendMetricKey) => {
  if (metric === 'estimatedCost') return point.estimatedCost;
  if (metric === 'totalTokens') return point.totalTokens;
  return point.requestCount;
};

export const buildSelectedApiKeyTrendSeries = (
  selectedApiKey: UsageRankRow | null,
  timeline: UsageTimelinePoint[],
  metric: UsageTrendMetricKey
): UsageEntityTrendSeries[] => {
  if (!selectedApiKey || timeline.length === 0) return [];

  return [
    {
      id: selectedApiKey.apiKeyHash || selectedApiKey.id,
      label: selectedApiKey.label,
      color: '#2563eb',
      points: timeline
        .slice()
        .sort((left, right) => left.bucketMs - right.bucketMs)
        .map((point) => ({
          bucketMs: point.bucketMs,
          label: point.label,
          value: usageTimelineMetricValue(point, metric),
        })),
    },
  ];
};

export const buildSelectedCredentialTrendSeries = (
  selectedCredential: UsageRankRow | null,
  credentialTimeline: UsageCredentialTimelinePoint[],
  metric: UsageTrendMetricKey
): UsageEntityTrendSeries[] => {
  if (!selectedCredential) return [];

  const points = credentialTimeline
    .filter((point) => point.id === selectedCredential.id)
    .sort((left, right) => left.bucketMs - right.bucketMs)
    .map((point) => ({
      bucketMs: point.bucketMs,
      label: point.bucketLabel,
      value: usageCredentialTimelineMetricValue(point, metric),
    }));

  if (points.length === 0) return [];

  return [
    {
      id: selectedCredential.id,
      label: selectedCredential.label,
      color: '#2563eb',
      points,
    },
  ];
};

export const computeRowCacheHitRate = (row: UsageRankRow): number =>
  row.models?.length
    ? computeRowsCacheHitRate(row.models)
    : computeCacheHitRate({
        modelName: row.model || row.label,
        inputTokens: row.inputTokens,
        cacheReadTokens: row.cacheReadTokens,
        cacheCreationTokens: row.cacheCreationTokens,
        cachedTokens: row.cachedTokens,
        cacheHitRate: row.cacheHitRate,
      });

export const computeRowAverageCostPerCall = (row: UsageRankRow): number =>
  row.requestCount > 0 ? row.estimatedCost / row.requestCount : 0;

export type UsageModelKeyDistributionRow = {
  id: string;
  label: string;
  totalTokens: number;
  share: number;
};

// Reverse distribution: which API keys drive the selected model, derived from
// the per-key model breakdown already shipped with apiKeyRows.
export const buildModelKeyDistribution = (
  modelId: string,
  apiKeyRows: UsageRankRow[],
  limit = 4
): UsageModelKeyDistributionRow[] => {
  const entries = apiKeyRows
    .map((keyRow) => {
      const modelRow = keyRow.models?.find((model) => model.id === modelId);
      if (!modelRow || modelRow.totalTokens <= 0) return null;
      return { id: keyRow.id, label: keyRow.label, totalTokens: modelRow.totalTokens };
    })
    .filter((entry): entry is { id: string; label: string; totalTokens: number } => entry !== null)
    .sort((left, right) => right.totalTokens - left.totalTokens);
  const total = entries.reduce((sum, entry) => sum + entry.totalTokens, 0);
  return entries.slice(0, limit).map((entry) => ({
    ...entry,
    share: safeShare(entry.totalTokens, total),
  }));
};

export const buildKeyAnomalies = (rows: UsageRankRow[]): UsageKeyAnomalyRow[] =>
  rows
    .filter((row) => row.failureCount > 0 || row.share >= 0.2 || row.successRate < 0.96)
    .map((row) => {
      const failureRate = safeShare(row.failureCount, row.requestCount);
      const severity: UsageKeyAnomalyRow['severity'] =
        failureRate >= 0.08 || row.share >= 0.32
          ? 'high'
          : failureRate >= 0.03 || row.share >= 0.22
            ? 'medium'
            : 'low';
      const reasonKey =
        row.share >= 0.3
          ? 'usage_analytics.anomaly_reason_cost_spike'
          : failureRate >= 0.03
            ? 'usage_analytics.anomaly_reason_error_rate'
            : 'usage_analytics.anomaly_reason_usage_skew';
      return {
        id: row.id,
        label: row.label,
        severity,
        reasonKey,
        triggeredAtMs: row.lastSeenMs ?? null,
        row,
      };
    })
    .sort((left, right) => {
      const severityScore = { high: 3, medium: 2, low: 1 };
      return (
        severityScore[right.severity] - severityScore[left.severity] ||
        right.row.estimatedCost - left.row.estimatedCost
      );
    });

export const buildCredentialQuotaRows = (
  rows: UsageRankRow[],
  nowMs: number
): UsageCredentialQuotaRow[] =>
  rows.slice(0, 8).map((row, index) => {
    const limit = Math.max(
      50,
      Math.ceil(Math.max(row.estimatedCost, 1) * (index === 0 ? 1 : 1.25))
    );
    const used = Math.min(limit, row.estimatedCost);
    const usedRate = safeShare(used, limit);
    return {
      id: row.id,
      label: row.label,
      plan: row.provider?.toLowerCase().includes('anthropic')
        ? 'Claude Pro'
        : row.provider?.toLowerCase().includes('azure')
          ? 'Azure PayGo'
          : row.provider?.toLowerCase().includes('aws')
            ? 'On-Demand'
            : 'Pay as You Go',
      used,
      limit,
      remaining: Math.max(0, limit - used),
      usedRate,
      resetAtMs: nowMs + (7 + index) * DAY_MS,
      status: usedRate >= 1 ? 'exhausted' : usedRate >= 0.8 ? 'warning' : 'normal',
      refreshedAtMs: row.lastSeenMs ?? null,
    };
  });

// Shared thresholds so insights and summary cards stay on the same definition.
export const USAGE_MODEL_TOP_SHARE_THRESHOLD = 0.45;
export const USAGE_MODEL_LONG_TAIL_SHARE = 0.08;
export const USAGE_SUCCESS_RATE_WATCH_THRESHOLD = 0.97;

export const buildUsageInsights = ({
  apiKeyRows,
  credentialRows,
  modelRows,
  providerRows,
  summary,
}: {
  apiKeyRows: UsageRankRow[];
  credentialRows: UsageRankRow[];
  modelRows: UsageRankRow[];
  providerRows: UsageProviderRow[];
  summary: UsageSummaryMetrics;
}): UsageInsight[] => {
  const insights: UsageInsight[] = [];
  const topModel = modelRows[0];
  if (topModel && topModel.share >= USAGE_MODEL_TOP_SHARE_THRESHOLD) {
    insights.push({
      id: 'model-cost-share',
      tone: 'warning',
      titleKey: 'usage_analytics.insight_model_cost_high',
      bodyKey: 'usage_analytics.insight_model_cost_high_body',
      actionTab: 'models',
    });
  }
  const topKey = apiKeyRows[0];
  if (topKey && topKey.share >= 0.25) {
    insights.push({
      id: 'key-concentration',
      tone: 'success',
      titleKey: 'usage_analytics.insight_key_long_tail',
      bodyKey: 'usage_analytics.insight_key_long_tail_body',
      actionTab: 'apiKeys',
    });
  }
  const unhealthyCredential = credentialRows.find(
    (row) => row.failureCount > 0 || row.successRate < USAGE_SUCCESS_RATE_WATCH_THRESHOLD
  );
  if (unhealthyCredential) {
    insights.push({
      id: 'credential-health',
      tone: 'danger',
      titleKey: 'usage_analytics.insight_credential_success_drop',
      bodyKey: 'usage_analytics.insight_credential_success_drop_body',
      actionTab: 'credentials',
    });
  }
  const lowCostModels = modelRows
    .filter((row) => row.share < USAGE_MODEL_LONG_TAIL_SHARE)
    .reduce((sum, row) => sum + row.share, 0);
  if (lowCostModels >= 0.12) {
    insights.push({
      id: 'low-cost-space',
      tone: 'info',
      titleKey: 'usage_analytics.insight_low_cost_room',
      bodyKey: 'usage_analytics.insight_low_cost_room_body',
      actionTab: 'models',
    });
  }
  const topProvider = providerRows[0];
  if (topProvider && topProvider.requestShare >= 0.45) {
    insights.push({
      id: 'provider-concentration',
      tone: 'info',
      titleKey: 'usage_analytics.insight_provider_concentration',
      bodyKey: 'usage_analytics.insight_provider_concentration_body',
      actionTab: 'heatmap',
    });
  }
  if (summary.inputTokens > 0 && computeCacheHitRate(summary) < 0.08) {
    insights.push({
      id: 'cache-room',
      tone: 'info',
      titleKey: 'usage_analytics.insight_cache_room',
      bodyKey: 'usage_analytics.insight_cache_room_body',
      actionTab: 'trends',
    });
  }
  return insights.slice(0, 4);
};

const buildUsageHeatmapContributors = (
  contributors: MonitoringAnalyticsHeatmapContributor[] = [],
  apiKeyDisplayMap?: UsageApiKeyDisplayMap
): UsageHeatmapContributor[] =>
  contributors.map((contributor) => {
    const key = normalizeApiKeyHash(contributor.key || contributor.label || '');
    return {
      key: contributor.key || key,
      label: apiKeyDisplayMap
        ? resolveUsageApiKeyLabel(key, apiKeyDisplayMap, contributor.label)
        : contributor.label || contributor.key || '',
      requestCount: toNumber(contributor.calls),
      successCount: toNumber(contributor.success),
      failureCount: toNumber(contributor.failure),
      totalTokens: toNumber(contributor.tokens),
      estimatedCost: toNumber(contributor.cost),
      failureRate: toNumber(contributor.failure_rate),
      share: toNumber(contributor.share),
    };
  });

export const buildUsageHeatmap = (
  points: MonitoringAnalyticsHeatmapPoint[] = [],
  apiKeyDisplayMap?: UsageApiKeyDisplayMap
): UsageHeatmapPoint[] =>
  points.map((point) => ({
    weekday: toNumber(point.weekday),
    hour: toNumber(point.hour),
    requestCount: toNumber(point.calls),
    successCount: toNumber(point.success),
    failureCount: toNumber(point.failure),
    totalTokens: toNumber(point.tokens),
    estimatedCost: toNumber(point.cost),
    failureRate: toNumber(point.failure_rate),
    modelContributors: buildUsageHeatmapContributors(point.model_contributors),
    apiKeyContributors: buildUsageHeatmapContributors(point.api_key_contributors, apiKeyDisplayMap),
    providerContributors: buildUsageHeatmapContributors(point.provider_contributors),
  }));

export const buildUsageHeatmapChartData = (
  points: UsageHeatmapPoint[],
  metric: UsageHeatmapMetricKey = 'requestCount',
  scaleMode: UsageHeatmapScaleMode = 'absolute'
): UsageHeatmapChartDatum[] => {
  const validPoints = points.filter(isValidUsageHeatmapPoint);
  const groupedMax = new Map<string, number>();
  if (scaleMode !== 'absolute') {
    validPoints.forEach((point) => {
      const groupKey =
        scaleMode === 'byWeekday' ? `weekday-${point.weekday}` : `hour-${point.hour}`;
      groupedMax.set(
        groupKey,
        Math.max(groupedMax.get(groupKey) ?? 0, getUsageHeatmapMetricValue(point, metric))
      );
    });
  }

  return validPoints.map((point) => {
    const rawValue = getUsageHeatmapMetricValue(point, metric);
    const groupKey = scaleMode === 'byWeekday' ? `weekday-${point.weekday}` : `hour-${point.hour}`;
    const visualValue =
      scaleMode === 'absolute' ? rawValue : safeShare(rawValue, groupedMax.get(groupKey) ?? 0);
    return [
      point.hour,
      point.weekday,
      visualValue,
      point.requestCount,
      point.successCount,
      point.failureCount,
      point.totalTokens,
      point.estimatedCost,
      point.failureRate,
    ];
  });
};

export const buildUsageHeatmapCellDetail = (
  points: UsageHeatmapPoint[],
  selection: UsageHeatmapCellSelection | null,
  metric: UsageHeatmapMetricKey
): UsageHeatmapCellDetail | null => {
  if (!selection) return null;
  const validPoints = points.filter(isValidUsageHeatmapPoint);
  const point =
    validPoints.find(
      (item) => item.weekday === selection.weekday && item.hour === selection.hour
    ) ?? null;
  if (!point) return null;

  const metricValue = getUsageHeatmapMetricValue(point, metric);
  const sameWeekday = validPoints.filter((item) => item.weekday === point.weekday);
  const sameHour = validPoints.filter((item) => item.hour === point.hour);
  const overallBaseline = heatmapBaseline(validPoints, metric);
  const weekdayBaseline = heatmapBaseline(sameWeekday, metric);
  const hourBaseline = heatmapBaseline(sameHour, metric);
  const ranked = [...validPoints].sort(
    (left, right) =>
      getUsageHeatmapMetricValue(right, metric) - getUsageHeatmapMetricValue(left, metric)
  );

  return {
    point,
    metricValue,
    overallBaseline,
    weekdayBaseline,
    hourBaseline,
    overallDelta: metricDelta(metricValue, overallBaseline),
    weekdayDelta: metricDelta(metricValue, weekdayBaseline),
    hourDelta: metricDelta(metricValue, hourBaseline),
    rank: ranked.findIndex((item) => usageHeatmapPointId(item) === usageHeatmapPointId(point)) + 1,
    totalCells: validPoints.length,
  };
};

const buildHeatmapHighlightsForMetric = (
  points: UsageHeatmapPoint[],
  metric: UsageHeatmapMetricKey,
  limit = 3
): UsageHeatmapHighlight[] =>
  points
    .filter(isValidUsageHeatmapPoint)
    .filter(
      (point) =>
        metric !== 'failureRate' || point.requestCount >= USAGE_HEATMAP_FAILURE_MIN_REQUESTS
    )
    .sort(
      (left, right) =>
        getUsageHeatmapMetricValue(right, metric) - getUsageHeatmapMetricValue(left, metric)
    )
    .slice(0, limit)
    .map((point) => ({
      id: `${metric}-${usageHeatmapPointId(point)}`,
      point,
      metric,
      value: getUsageHeatmapMetricValue(point, metric),
    }));

export const buildUsageHeatmapHighlights = (
  points: UsageHeatmapPoint[] = []
): UsageHeatmapHighlights => ({
  requestPeaks: buildHeatmapHighlightsForMetric(points, 'requestCount'),
  costPeaks: buildHeatmapHighlightsForMetric(points, 'estimatedCost'),
  failureRisks: buildHeatmapHighlightsForMetric(points, 'failureRate'),
});

export const buildServerAnomalyPoints = (
  points: MonitoringAnalyticsAnomalyPoint[] = []
): UsageServerAnomaly[] =>
  points.map((point) => ({
    bucketMs: toNumber(point.bucket_ms),
    bucketEndMs: toNumber(point.bucket_end_ms),
    label: point.label || formatLocalBucketLabel(toNumber(point.bucket_ms), 'hour'),
    severity: point.severity,
    metricKeys: point.metric_keys ?? [],
    requestCount: toNumber(point.calls),
    totalTokens: toNumber(point.total_tokens),
    estimatedCost: toNumber(point.cost),
    failureRate: toNumber(point.failure_rate),
    requestChange: toNumber(point.request_change),
    costChange: toNumber(point.cost_change),
    tokensPerRequestChange: toNumber(point.tokens_per_request_change),
    cacheHitRateChange: toNumber(point.cache_hit_rate_change),
    failureRateChange: toNumber(point.failure_rate_change),
    latencyP95Change: toNumber(point.latency_p95_change),
  }));

export const summarizeAnomalies = (
  points: UsageServerAnomaly[],
  options: { minRequests?: number; limit?: number } = {}
): UsageServerAnomaly[] => {
  const minRequests = options.minRequests ?? 10;
  const limit = options.limit ?? 3;
  const severityScore: Record<string, number> = { high: 3, medium: 2, low: 1 };
  return [...points]
    .filter((point) => point.requestCount >= minRequests)
    .sort(
      (left, right) =>
        (severityScore[right.severity] ?? 0) - (severityScore[left.severity] ?? 0) ||
        right.estimatedCost - left.estimatedCost ||
        right.requestCount - left.requestCount
    )
    .slice(0, limit);
};

export const buildDrilldownPreview = (
  rows: MonitoringAnalyticsEventRow[] = [],
  modelRows: UsageRankRow[] = [],
  apiKeyDisplayMap?: UsageApiKeyDisplayMap
): UsageDrilldownEvent[] => {
  const modelCostPerToken = new Map(
    modelRows.map((row) => [
      row.model || row.label,
      row.totalTokens > 0 ? row.estimatedCost / row.totalTokens : 0,
    ])
  );
  return rows.map((row) => {
    const model = row.model || row.resolved_model || '-';
    const totalTokens = toNumber(row.total_tokens);
    const costPerToken = modelCostPerToken.get(model) ?? 0;
    return {
      requestId: row.request_id || '',
      eventHash: row.event_hash,
      timestampMs: toNumber(row.timestamp_ms),
      model,
      apiKeyHash: normalizeApiKeyHash(row.api_key_hash || ''),
      apiKeyLabel: resolveUsageApiKeyLabel(row.api_key_hash || '', apiKeyDisplayMap),
      source: row.source || '',
      authIndex: row.auth_index || '',
      endpoint: row.endpoint || row.path || '',
      totalTokens,
      estimatedCost: totalTokens * costPerToken,
      latencyMs: row.latency_ms ?? null,
      ttftMs: row.ttft_ms ?? null,
      failed: Boolean(row.failed),
      failStatusCode: row.fail_status_code ?? null,
      failSummary: row.fail_summary ?? '',
      headerErrorKind: row.header_error_kind ?? '',
      headerErrorCode: row.header_error_code ?? '',
      headerTraceId: row.header_trace_id ?? '',
      headerQuotaPlanType: row.header_quota_plan_type ?? '',
      headerQuotaUsedPercent: row.header_quota_used_percent ?? null,
      headerQuotaRecoverAtMs: row.header_quota_recover_at_ms ?? null,
    };
  });
};

export const adaptUsageAnalyticsData = (
  data: MonitoringAnalyticsResponse | null | undefined,
  granularity: UsageAnalyticsResolvedGranularity,
  keyword = '',
  apiKeyDisplayMap?: UsageApiKeyDisplayMap,
  credentialDisplayContext?: UsageCredentialDisplayContext
) => {
  const summary = buildUsageSummary(data?.summary);
  const timeline = buildUsageTimeline(data?.timeline ?? [], granularity);
  const credentialTimeline = buildUsageCredentialTimeline(
    data?.credential_timeline ?? [],
    granularity,
    credentialDisplayContext
  );
  const modelRows = buildModelRows(data?.model_stats ?? [], summary);
  const apiKeyRows = buildApiKeyRows(data?.api_key_stats ?? [], summary, keyword, apiKeyDisplayMap);
  const credentialRows = buildCredentialRows(
    data?.credential_stats ?? [],
    summary,
    credentialDisplayContext
  );
  const providerRows = buildProviderRows(
    data?.channel_share ?? [],
    apiKeyRows,
    credentialRows,
    summary
  );
  return {
    summary,
    summaryComparison: data?.summary_comparison,
    timeline,
    credentialTimeline,
    modelRows,
    apiKeyRows,
    credentialRows,
    providerRows,
    heatmap: buildUsageHeatmap(data?.heatmap ?? [], apiKeyDisplayMap),
    anomalyPoints: buildServerAnomalyPoints(data?.anomaly_points ?? []),
    drilldownPreview: buildDrilldownPreview(
      data?.drilldown_preview?.items ?? [],
      modelRows,
      apiKeyDisplayMap
    ),
    filterOptions: data?.filter_options,
  };
};

const percentChange = (current: number, previous: number) => {
  if (previous <= 0) return current > 0 ? 1 : 0;
  return (current - previous) / previous;
};

export const buildUsageSummaryDelta = (
  summary: UsageSummaryMetrics,
  comparison?: MonitoringAnalyticsSummaryComparison | null
): UsageSummaryDelta => {
  if (!comparison) {
    return { hasComparison: false, requestCount: 0, totalTokens: 0, estimatedCost: 0 };
  }
  return {
    hasComparison: true,
    requestCount: percentChange(summary.requestCount, toNumber(comparison.total_calls)),
    totalTokens: percentChange(summary.totalTokens, toNumber(comparison.total_tokens)),
    estimatedCost: percentChange(summary.estimatedCost, toNumber(comparison.total_cost)),
  };
};

export const anomalyMetricLabelKey = (key: string) => {
  switch (key) {
    case 'request_spike':
      return 'usage_analytics.anomaly_request_spike';
    case 'cost_spike':
      return 'usage_analytics.anomaly_cost_spike';
    case 'tokens_per_request_spike':
    case 'token_per_request_spike':
      return 'usage_analytics.anomaly_token_per_request_spike';
    case 'cache_hit_drop':
      return 'usage_analytics.anomaly_cache_hit_drop';
    case 'failure_rate_spike':
      return 'usage_analytics.anomaly_failure_rate_spike';
    case 'latency_spike':
      return 'usage_analytics.anomaly_latency_spike';
    default:
      return 'usage_analytics.anomaly_unknown';
  }
};

export const analyzeUsageBucket = (
  timeline: UsageTimelinePoint[],
  bucketMs: number
): UsageAnomalyAnalysis | null => {
  const index = timeline.findIndex((point) => point.bucketMs === bucketMs);
  if (index < 0) return null;
  const point = timeline[index];
  const previousPoint = index > 0 ? timeline[index - 1] : null;
  const changes = {
    requestCount: previousPoint ? percentChange(point.requestCount, previousPoint.requestCount) : 0,
    totalTokens: previousPoint ? percentChange(point.totalTokens, previousPoint.totalTokens) : 0,
    inputTokens: previousPoint ? percentChange(point.inputTokens, previousPoint.inputTokens) : 0,
    outputTokens: previousPoint ? percentChange(point.outputTokens, previousPoint.outputTokens) : 0,
    cachedTokens: previousPoint
      ? percentChange(getUsageCacheTokens(point), getUsageCacheTokens(previousPoint))
      : 0,
    cacheCreationTokens: previousPoint
      ? percentChange(point.cacheCreationTokens, previousPoint.cacheCreationTokens)
      : 0,
    estimatedCost: previousPoint
      ? percentChange(point.estimatedCost, previousPoint.estimatedCost)
      : 0,
    cacheHitRate: previousPoint ? point.cacheHitRate - previousPoint.cacheHitRate : 0,
    averageTokensPerRequest: previousPoint
      ? percentChange(point.averageTokensPerRequest, previousPoint.averageTokensPerRequest)
      : 0,
    failureRate: previousPoint ? point.failureRate - previousPoint.failureRate : 0,
    averageLatencyMs:
      previousPoint && previousPoint.averageLatencyMs !== null && point.averageLatencyMs !== null
        ? percentChange(point.averageLatencyMs, previousPoint.averageLatencyMs)
        : 0,
  };
  const anomalies: UsageAnomaly[] = [];
  if (changes.requestCount > 1) {
    anomalies.push({
      key: 'request_spike',
      labelKey: 'usage_analytics.anomaly_request_spike',
      delta: changes.requestCount,
    });
  }
  if (changes.estimatedCost > 1) {
    anomalies.push({
      key: 'cost_spike',
      labelKey: 'usage_analytics.anomaly_cost_spike',
      delta: changes.estimatedCost,
    });
  }
  if (changes.averageTokensPerRequest > 0.5) {
    anomalies.push({
      key: 'token_per_request_spike',
      labelKey: 'usage_analytics.anomaly_token_per_request_spike',
      delta: changes.averageTokensPerRequest,
    });
  }
  if (changes.cacheHitRate < -0.2) {
    anomalies.push({
      key: 'cache_hit_drop',
      labelKey: 'usage_analytics.anomaly_cache_hit_drop',
      delta: changes.cacheHitRate,
    });
  }
  if (changes.failureRate > 0.2) {
    anomalies.push({
      key: 'failure_rate_spike',
      labelKey: 'usage_analytics.anomaly_failure_rate_spike',
      delta: changes.failureRate,
    });
  }
  if (changes.averageLatencyMs > 0.5) {
    anomalies.push({
      key: 'latency_spike',
      labelKey: 'usage_analytics.anomaly_latency_spike',
      delta: changes.averageLatencyMs,
    });
  }
  return {
    point,
    previousPoint,
    anomalies,
    changes,
    causeKeys: buildUsageAnomalyCauseKeys(changes),
  };
};

export const buildUsageAnomalyCauseKeys = (
  changes: Partial<
    Record<
      | UsageMetricKey
      | 'cacheCreationTokens'
      | 'cacheHitRate'
      | 'averageTokensPerRequest'
      | 'failureRate'
      | 'averageLatencyMs',
      number
    >
  >
) => {
  const causes: string[] = [];

  if (toNumber(changes.requestCount) > 1) {
    causes.push('usage_analytics.cause_request_spike');
  } else if (toNumber(changes.requestCount) < -0.5) {
    causes.push('usage_analytics.cause_request_drop');
  }

  if (toNumber(changes.estimatedCost) > 1) {
    causes.push('usage_analytics.cause_cost_spike');
  } else if (toNumber(changes.estimatedCost) < -0.5) {
    causes.push('usage_analytics.cause_cost_drop');
  }

  if (toNumber(changes.averageTokensPerRequest) > 0.5) {
    causes.push('usage_analytics.cause_token_per_request_spike');
  }

  if (toNumber(changes.cacheHitRate) < -0.2) {
    causes.push('usage_analytics.cause_cache_hit_drop');
  } else if (toNumber(changes.cachedTokens) > 0.5) {
    causes.push('usage_analytics.cause_cache_growth');
  }

  if (toNumber(changes.failureRate) > 0.2) {
    causes.push('usage_analytics.cause_failure_rate_spike');
  }

  if (toNumber(changes.averageLatencyMs) > 0.5) {
    causes.push('usage_analytics.cause_latency_spike');
  }

  if (causes.length === 0) {
    causes.push('usage_analytics.cause_no_clear_anomaly');
  }

  return causes.slice(0, 4);
};

export const buildMonitoringDetailUrl = (
  point: UsageTimelinePoint | Pick<UsageServerAnomaly, 'bucketMs' | 'bucketEndMs'>,
  filters: Partial<
    Pick<
      UsageAnalyticsFiltersState,
      | 'model'
      | 'apiKeyHash'
      | 'provider'
      | 'authFile'
      | 'status'
      | 'searchQuery'
      | 'minLatencyMs'
      | 'cacheStatus'
    >
  > & {
    projectId?: string;
    requestType?: string;
  }
) => {
  const params = new URLSearchParams();
  params.set('from_ms', String(point.bucketMs));
  params.set('to_ms', String(point.bucketEndMs));
  const model = filters.model ?? 'all';
  const apiKeyHash = filters.apiKeyHash ?? 'all';
  const provider = filters.provider ?? 'all';
  const authFile = filters.authFile ?? 'all';
  const searchQuery = filters.searchQuery ?? '';
  const minLatencyMs = filters.minLatencyMs ?? 'all';
  const cacheStatus = filters.cacheStatus ?? 'all';
  if (isActiveSelectValue(model)) {
    params.set('model', model.trim());
  }
  if (isActiveSelectValue(apiKeyHash)) {
    params.set('api_key_hash', normalizeLowerSelectValue(apiKeyHash));
  }
  if (isActiveSelectValue(provider)) {
    params.set('provider', normalizeLowerSelectValue(provider));
  }
  if (isActiveSelectValue(authFile)) {
    params.set('auth_file', authFile.trim());
  }
  if (isActiveSelectValue(filters.projectId)) {
    params.set('project_id', filters.projectId!.trim());
  }
  if (isActiveSelectValue(filters.requestType)) {
    params.set('request_type', filters.requestType!.trim());
  }
  if (filters.status && filters.status !== 'all') {
    params.set('status', filters.status);
  }
  if (searchQuery.trim()) {
    params.set('search', searchQuery.trim());
  }
  if (minLatencyMs !== 'all') {
    params.set('min_latency_ms', minLatencyMs);
  }
  if (cacheStatus !== 'all') {
    params.set('cache_status', cacheStatus);
  }
  return `/monitoring?${params.toString()}`;
};

export type UsageSelectedFilterKey =
  | 'model'
  | 'apiKeyHash'
  | 'provider'
  | 'authFile'
  | 'minLatencyMs'
  | 'cacheStatus'
  | 'status';

export const buildOptionValues = (values: Array<string | undefined | null>) =>
  Array.from(new Set(values.map((value) => String(value ?? '').trim()).filter(Boolean))).sort(
    (left, right) => left.localeCompare(right)
  );

export const formatMetricValue = (key: UsageMetricKey, value: number) => {
  if (key === 'estimatedCost') return formatUsd(value);
  return formatCompactNumber(value);
};

export const formatHeatmapMetricValue = (key: UsageHeatmapMetricKey, value: number) => {
  if (key === 'failureRate') return `${((Number.isFinite(value) ? value : 0) * 100).toFixed(1)}%`;
  if (key === 'estimatedCost') return formatUsd(value);
  return formatCompactNumber(value);
};

export const hasUsageData = (summary: UsageSummaryMetrics, timeline: UsageTimelinePoint[]) =>
  summary.requestCount > 0 || summary.totalTokens > 0 || timeline.length > 0;
