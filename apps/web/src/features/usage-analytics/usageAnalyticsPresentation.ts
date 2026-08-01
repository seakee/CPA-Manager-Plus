import type { TFunction } from 'i18next';
import {
  computeCacheHitRate,
  computeRowCacheHitRate,
  formatMetricValue,
  getUsageCacheTokens,
  USAGE_MODEL_LONG_TAIL_SHARE,
  USAGE_MODEL_TOP_SHARE_THRESHOLD,
  USAGE_SUCCESS_RATE_WATCH_THRESHOLD,
  type UsageRankRow,
  type UsageSummaryDelta,
  type UsageSummaryMetrics,
  type UsageTimelinePoint,
} from './usageAnalyticsModel';

export type UsageSummaryCardIcon =
  | 'anomaly'
  | 'cache'
  | 'calls'
  | 'cost'
  | 'credential'
  | 'failure'
  | 'input'
  | 'key'
  | 'latency'
  | 'model'
  | 'output'
  | 'success'
  | 'tokens'
  | 'trend';

export type UsageSummaryCardAccent = 'amber' | 'blue' | 'cyan' | 'green' | 'red' | 'teal';

export type UsageSummaryCardTone = 'bad' | 'good' | 'warn';

export type UsageSummaryCard = {
  accent?: UsageSummaryCardAccent;
  fullLabel?: string;
  icon?: UsageSummaryCardIcon;
  label: string;
  meta: string;
  tone?: UsageSummaryCardTone;
  value: string;
  valueTitle?: string;
  variant?: 'primary' | 'secondary';
};

type CommonSummaryContext = {
  locale: string;
  t: TFunction;
};

type OverviewSummaryCardsInput = CommonSummaryContext & {
  anomalyCount: number;
  reasoningTokens: number;
  summary: UsageSummaryMetrics;
  summaryDelta: UsageSummaryDelta;
};

type TrendSummaryCardsInput = CommonSummaryContext & {
  summaryDelta: UsageSummaryDelta;
  timeline: UsageTimelinePoint[];
};

type EntitySummaryCardsInput = CommonSummaryContext & {
  activeCount: number;
  activeLabel: string;
  activeMeta: string;
  activeIcon: UsageSummaryCardIcon;
  activeAccent: UsageSummaryCardAccent;
  anomalyCount?: number;
  anomalyLabel?: string;
  summary: UsageSummaryMetrics;
};

type ModelSummaryCardsInput = CommonSummaryContext & {
  modelRows: UsageRankRow[];
  summary: UsageSummaryMetrics;
};

type ApiKeySummaryCardsInput = CommonSummaryContext & {
  apiKeyRows: UsageRankRow[];
  keyAnomalyCount: number;
  summary: UsageSummaryMetrics;
};

type HeatmapSummaryCardsInput = CommonSummaryContext & {
  summary: UsageSummaryMetrics;
};

type CredentialDetailCardsInput = CommonSummaryContext & {
  row: UsageRankRow;
};

const formatCompactNumber = (value: number) => {
  if (!Number.isFinite(value)) return '0';
  if (Math.abs(value) >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(2)}B`;
  if (Math.abs(value) >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (Math.abs(value) >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
  return String(Math.round(value));
};

const formatFullNumber = (value: number, locale: string) =>
  new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }).format(
    Number.isFinite(value) ? value : 0
  );

const formatPercent = (value: number) =>
  `${((Number.isFinite(value) ? value : 0) * 100).toFixed(1)}%`;

const formatDelta = (value: number) => {
  const sign = value > 0 ? '+' : '';
  return `${sign}${(value * 100).toFixed(1)}%`;
};

const formatSecondValue = (seconds: number) => {
  const fixed = seconds < 10 ? seconds.toFixed(2) : seconds.toFixed(1);
  return fixed.replace(/\.0+$/, '').replace(/(\.\d*[1-9])0$/, '$1');
};

export const formatUsageDurationMs = (value: number | null | undefined) => {
  if (value === null || value === undefined) return '-';

  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < 0) return '-';
  if (parsed < 1000) return `${Math.round(parsed)}ms`;

  return `${formatSecondValue(parsed / 1000)}s`;
};

const getMaxTimelinePoint = (
  timeline: UsageTimelinePoint[],
  valueOf: (point: UsageTimelinePoint) => number
) =>
  timeline.reduce<UsageTimelinePoint | null>((current, point) => {
    if (!current) return point;
    return valueOf(point) > valueOf(current) ? point : current;
  }, null);

const getMaxTimelineMs = (
  timeline: UsageTimelinePoint[],
  valueOf: (point: UsageTimelinePoint) => number | null
) =>
  timeline.reduce<number | null>((current, point) => {
    const value = valueOf(point);
    if (value === null || !Number.isFinite(value)) return current;
    return current === null || value > current ? value : current;
  }, null);

const deltaMeta = (
  summaryDelta: UsageSummaryDelta,
  key: 'estimatedCost' | 'requestCount' | 'totalTokens',
  t: TFunction,
  fallback: string
) =>
  summaryDelta.hasComparison
    ? `${formatDelta(summaryDelta[key])} ${t('usage_analytics.summary_vs_previous')}`
    : fallback;

export const buildUsageOverviewSummaryCards = ({
  anomalyCount,
  locale,
  reasoningTokens,
  summary,
  summaryDelta,
  t,
}: OverviewSummaryCardsInput): UsageSummaryCard[] => {
  const cacheTokens = getUsageCacheTokens(summary);
  const totalTokens = Math.max(summary.totalTokens, 0);
  const p95LatencyLabel =
    summary.p95LatencyMs === null && summary.p95TtftMs !== null
      ? t('usage_analytics.metric_p95_ttft')
      : t('usage_analytics.metric_p95_latency');
  const p95LatencyValue = summary.p95LatencyMs ?? summary.p95TtftMs;

  return [
    {
      accent: 'blue',
      fullLabel: t('usage_analytics.metric_request_count'),
      icon: 'calls',
      label: t('usage_analytics.metric_request_count'),
      meta: deltaMeta(summaryDelta, 'requestCount', t, t('usage_analytics.summary_meta')),
      value: formatMetricValue('requestCount', summary.requestCount),
      valueTitle: formatFullNumber(summary.requestCount, locale),
    },
    {
      accent: 'green',
      fullLabel: t('usage_analytics.success_rate'),
      icon: 'success',
      label: t('usage_analytics.success_rate'),
      meta: `${p95LatencyLabel} ${formatUsageDurationMs(p95LatencyValue)}`,
      tone: summary.successRate >= 0.95 ? 'good' : summary.successRate >= 0.85 ? 'warn' : 'bad',
      value: formatPercent(summary.successRate),
    },
    {
      accent: 'red',
      fullLabel: t('usage_analytics.metric_failure_count'),
      icon: 'failure',
      label: t('usage_analytics.metric_failure_count'),
      meta: `${anomalyCount} ${t('usage_analytics.anomaly_points_title')}`,
      tone: summary.failureCount > 0 ? 'bad' : 'good',
      value: formatMetricValue('requestCount', summary.failureCount),
      valueTitle: formatFullNumber(summary.failureCount, locale),
    },
    {
      accent: 'amber',
      fullLabel: t('usage_analytics.metric_estimated_cost'),
      icon: 'cost',
      label: t('usage_analytics.metric_estimated_cost'),
      meta: deltaMeta(summaryDelta, 'estimatedCost', t, t('usage_analytics.summary_cost_meta')),
      value: formatMetricValue('estimatedCost', summary.estimatedCost),
    },
    {
      accent: 'teal',
      fullLabel: t('usage_analytics.metric_total_tokens'),
      icon: 'tokens',
      label: t('usage_analytics.metric_total_tokens'),
      meta: `${t('usage_analytics.metric_reasoning_tokens')} ${formatCompactNumber(reasoningTokens)}`,
      value: formatMetricValue('totalTokens', summary.totalTokens),
      valueTitle: formatFullNumber(summary.totalTokens, locale),
      variant: 'secondary',
    },
    {
      accent: 'cyan',
      fullLabel: t('usage_analytics.metric_input_tokens'),
      icon: 'input',
      label: t('usage_analytics.metric_input_tokens'),
      meta: `${t('usage_analytics.share')} ${formatPercent(
        totalTokens > 0 ? summary.inputTokens / totalTokens : 0
      )}`,
      value: formatMetricValue('totalTokens', summary.inputTokens),
      valueTitle: formatFullNumber(summary.inputTokens, locale),
      variant: 'secondary',
    },
    {
      accent: 'blue',
      fullLabel: t('usage_analytics.metric_output_tokens'),
      icon: 'output',
      label: t('usage_analytics.metric_output_tokens'),
      meta: `${t('usage_analytics.share')} ${formatPercent(
        totalTokens > 0 ? summary.outputTokens / totalTokens : 0
      )}`,
      value: formatMetricValue('totalTokens', summary.outputTokens),
      valueTitle: formatFullNumber(summary.outputTokens, locale),
      variant: 'secondary',
    },
    {
      accent: 'teal',
      fullLabel: t('usage_analytics.metric_cached_tokens'),
      icon: 'cache',
      label: t('usage_analytics.metric_cached_tokens'),
      meta: `${t('usage_analytics.cache_read_rate')} ${formatPercent(computeCacheHitRate(summary))}`,
      value: formatMetricValue('totalTokens', cacheTokens),
      valueTitle: formatFullNumber(cacheTokens, locale),
      variant: 'secondary',
    },
  ];
};

export const buildUsageTrendSummaryCards = ({
  locale,
  summaryDelta,
  timeline,
  t,
}: TrendSummaryCardsInput): UsageSummaryCard[] => {
  const peakRequestPoint = getMaxTimelinePoint(timeline, (point) => point.requestCount);
  const peakFailurePoint = getMaxTimelinePoint(timeline, (point) => point.failureRate);
  const peakP95Ms = getMaxTimelineMs(timeline, (point) => point.p95LatencyMs);
  const averageBucketRequests =
    timeline.length > 0
      ? timeline.reduce((sum, point) => sum + point.requestCount, 0) / timeline.length
      : 0;

  return [
    {
      accent: 'blue',
      icon: 'latency',
      label: t('usage_analytics.trend_peak_request_bucket'),
      meta: peakRequestPoint
        ? `${formatCompactNumber(peakRequestPoint.requestCount)} ${t('usage_analytics.metric_request_count')}`
        : '-',
      value: peakRequestPoint?.label ?? '-',
      valueTitle: peakRequestPoint
        ? formatFullNumber(peakRequestPoint.requestCount, locale)
        : undefined,
    },
    {
      accent: 'blue',
      icon: 'calls',
      label: t('usage_analytics.trend_average_bucket_requests'),
      meta: t('usage_analytics.summary_meta'),
      value: formatCompactNumber(averageBucketRequests),
    },
    {
      accent: 'blue',
      icon: 'trend',
      label: t('usage_analytics.trend_request_change'),
      meta: t('usage_analytics.summary_vs_previous'),
      value: summaryDelta.hasComparison ? formatDelta(summaryDelta.requestCount) : '-',
    },
    {
      accent: 'teal',
      icon: 'tokens',
      label: t('usage_analytics.trend_token_change'),
      meta: t('usage_analytics.summary_vs_previous'),
      value: summaryDelta.hasComparison ? formatDelta(summaryDelta.totalTokens) : '-',
    },
    {
      accent: 'amber',
      icon: 'cost',
      label: t('usage_analytics.trend_cost_change'),
      meta: t('usage_analytics.summary_vs_previous'),
      value: summaryDelta.hasComparison ? formatDelta(summaryDelta.estimatedCost) : '-',
    },
    {
      accent: 'red',
      icon: 'failure',
      label: t('usage_analytics.trend_failure_peak'),
      meta: peakFailurePoint?.label ?? '-',
      tone: peakFailurePoint && peakFailurePoint.failureRate > 0 ? 'bad' : 'good',
      value: peakFailurePoint ? formatPercent(peakFailurePoint.failureRate) : '-',
    },
    {
      accent: 'amber',
      icon: 'latency',
      label: t('usage_analytics.trend_p95_peak'),
      meta: t('usage_analytics.metric_p95_latency'),
      value: formatUsageDurationMs(peakP95Ms),
    },
  ];
};

export const buildUsageEntitySummaryCards = ({
  activeAccent,
  activeCount,
  activeIcon,
  activeLabel,
  activeMeta,
  anomalyCount,
  anomalyLabel,
  locale,
  summary,
  t,
}: EntitySummaryCardsInput): UsageSummaryCard[] => [
  {
    accent: activeAccent,
    icon: activeIcon,
    label: activeLabel,
    meta: activeMeta,
    value: formatCompactNumber(activeCount),
    valueTitle: formatFullNumber(activeCount, locale),
  },
  {
    accent: 'blue',
    icon: 'calls',
    label: t('usage_analytics.metric_request_count'),
    meta: t('usage_analytics.summary_meta'),
    value: formatMetricValue('requestCount', summary.requestCount),
    valueTitle: formatFullNumber(summary.requestCount, locale),
  },
  {
    accent: 'teal',
    icon: 'tokens',
    label: t('usage_analytics.metric_total_tokens'),
    meta: t('usage_analytics.summary_meta'),
    value: formatMetricValue('totalTokens', summary.totalTokens),
    valueTitle: formatFullNumber(summary.totalTokens, locale),
  },
  {
    accent: 'amber',
    icon: 'cost',
    label: t('usage_analytics.metric_estimated_cost'),
    meta: t('usage_analytics.summary_cost_meta'),
    value: formatMetricValue('estimatedCost', summary.estimatedCost),
  },
  {
    accent: anomalyCount === undefined ? 'amber' : 'red',
    icon: anomalyCount === undefined ? 'cost' : 'anomaly',
    label:
      anomalyCount === undefined
        ? t('usage_analytics.metric_average_cost_per_call')
        : (anomalyLabel ?? t('usage_analytics.anomaly_points_title')),
    meta: t('usage_analytics.summary_meta'),
    tone: anomalyCount && anomalyCount > 0 ? 'bad' : undefined,
    value:
      anomalyCount === undefined
        ? formatMetricValue('estimatedCost', summary.averageCostPerCall)
        : formatCompactNumber(anomalyCount),
  },
];

export const buildUsageModelSummaryCards = ({
  locale,
  modelRows,
  summary,
  t,
}: ModelSummaryCardsInput): UsageSummaryCard[] => {
  const topModel = modelRows[0];
  // With a single costed model, a 100% top share is trivially true — not a concentration signal.
  const costedModelCount = modelRows.filter((row) => row.estimatedCost > 0).length;
  const lowestSuccessModel = modelRows
    .filter((row) => row.requestCount > 0)
    .reduce<UsageRankRow | null>(
      (current, row) => (!current || row.successRate < current.successRate ? row : current),
      null
    );
  const longTailShare = modelRows
    .filter((row) => row.share < USAGE_MODEL_LONG_TAIL_SHARE)
    .reduce((sum, row) => sum + row.share, 0);
  return [
    {
      accent: 'teal',
      icon: 'model',
      label: t('usage_analytics.active_models'),
      meta: t('usage_analytics.summary_meta'),
      value: formatCompactNumber(modelRows.length),
      valueTitle: formatFullNumber(modelRows.length, locale),
    },
    {
      accent: 'amber',
      icon: 'cost',
      label: t('usage_analytics.model_top_cost_share'),
      meta: topModel ? topModel.label : t('usage_analytics.summary_meta'),
      tone:
        topModel && costedModelCount >= 2 && topModel.share >= USAGE_MODEL_TOP_SHARE_THRESHOLD
          ? 'warn'
          : undefined,
      value: topModel ? formatPercent(topModel.share) : '-',
    },
    {
      accent: 'red',
      icon: 'failure',
      label: t('usage_analytics.model_lowest_success'),
      meta: lowestSuccessModel ? lowestSuccessModel.label : t('usage_analytics.summary_meta'),
      tone: lowestSuccessModel
        ? lowestSuccessModel.successRate < USAGE_SUCCESS_RATE_WATCH_THRESHOLD
          ? 'bad'
          : 'good'
        : undefined,
      value: lowestSuccessModel ? formatPercent(lowestSuccessModel.successRate) : '-',
    },
    {
      accent: 'blue',
      icon: 'trend',
      label: t('usage_analytics.model_long_tail_share'),
      meta: t('usage_analytics.model_long_tail_meta'),
      value: formatPercent(longTailShare),
    },
    {
      accent: 'amber',
      icon: 'cost',
      label: t('usage_analytics.metric_estimated_cost'),
      meta: t('usage_analytics.summary_cost_meta'),
      value: formatMetricValue('estimatedCost', summary.estimatedCost),
    },
  ];
};

// Key-dimension digest only: global totals already live in the overview tab,
// and the anomaly count must match the warning table (buildKeyAnomalies).
export const buildUsageApiKeySummaryCards = ({
  apiKeyRows,
  keyAnomalyCount,
  locale,
  summary,
  t,
}: ApiKeySummaryCardsInput): UsageSummaryCard[] => {
  const topKey = apiKeyRows[0];
  // With a single costed key, a 100% top share is trivially true — not a concentration signal.
  const costedKeyCount = apiKeyRows.filter((row) => row.estimatedCost > 0).length;
  const lowestSuccessKey = apiKeyRows
    .filter((row) => row.requestCount > 0)
    .reduce<UsageRankRow | null>(
      (current, row) => (!current || row.successRate < current.successRate ? row : current),
      null
    );
  return [
    {
      accent: 'blue',
      icon: 'key',
      label: t('usage_analytics.active_api_keys'),
      meta: t('usage_analytics.summary_meta'),
      value: formatCompactNumber(apiKeyRows.length),
      valueTitle: formatFullNumber(apiKeyRows.length, locale),
    },
    {
      accent: 'amber',
      icon: 'cost',
      label: t('usage_analytics.api_key_top_cost_share'),
      meta: topKey ? topKey.label : t('usage_analytics.summary_meta'),
      tone:
        topKey && costedKeyCount >= 2 && topKey.share >= USAGE_MODEL_TOP_SHARE_THRESHOLD
          ? 'warn'
          : undefined,
      value: topKey ? formatPercent(topKey.share) : '-',
    },
    {
      accent: 'red',
      icon: 'failure',
      label: t('usage_analytics.api_key_lowest_success'),
      meta: lowestSuccessKey ? lowestSuccessKey.label : t('usage_analytics.summary_meta'),
      tone: lowestSuccessKey
        ? lowestSuccessKey.successRate < USAGE_SUCCESS_RATE_WATCH_THRESHOLD
          ? 'bad'
          : 'good'
        : undefined,
      value: lowestSuccessKey ? formatPercent(lowestSuccessKey.successRate) : '-',
    },
    {
      accent: 'cyan',
      icon: 'cost',
      label: t('usage_analytics.metric_average_cost_per_call'),
      meta: t('usage_analytics.summary_cost_meta'),
      value: formatMetricValue('estimatedCost', summary.averageCostPerCall),
    },
    {
      accent: 'red',
      icon: 'anomaly',
      label: t('usage_analytics.anomaly_keys'),
      meta: t('usage_analytics.summary_meta'),
      tone: keyAnomalyCount > 0 ? 'bad' : undefined,
      value: formatCompactNumber(keyAnomalyCount),
    },
  ];
};

export const buildUsageHeatmapSummaryCards = ({
  locale,
  summary,
  t,
}: HeatmapSummaryCardsInput): UsageSummaryCard[] => [
  {
    accent: 'blue',
    icon: 'calls',
    label: t('usage_analytics.metric_request_count'),
    meta: t('usage_analytics.summary_meta'),
    value: formatMetricValue('requestCount', summary.requestCount),
    valueTitle: formatFullNumber(summary.requestCount, locale),
  },
  {
    accent: 'teal',
    icon: 'tokens',
    label: t('usage_analytics.metric_total_tokens'),
    meta: t('usage_analytics.summary_meta'),
    value: formatMetricValue('totalTokens', summary.totalTokens),
    valueTitle: formatFullNumber(summary.totalTokens, locale),
  },
  {
    accent: 'amber',
    icon: 'cost',
    label: t('usage_analytics.metric_estimated_cost'),
    meta: t('usage_analytics.summary_cost_meta'),
    value: formatMetricValue('estimatedCost', summary.estimatedCost),
  },
  {
    accent: 'red',
    icon: 'failure',
    label: t('usage_analytics.failure_rate'),
    meta: t('usage_analytics.metric_failure_count'),
    tone: summary.failureCount > 0 ? 'bad' : 'good',
    value: formatPercent(
      summary.requestCount > 0 ? summary.failureCount / summary.requestCount : 0
    ),
  },
];

export const buildCredentialDetailCards = ({
  locale,
  row,
  t,
}: CredentialDetailCardsInput): UsageSummaryCard[] => {
  const averageCost = row.requestCount > 0 ? row.estimatedCost / row.requestCount : 0;
  const averageTokens = row.requestCount > 0 ? row.totalTokens / row.requestCount : 0;
  const failureRate = row.requestCount > 0 ? row.failureCount / row.requestCount : 0;
  const cacheRate = computeRowCacheHitRate(row);
  const lastSeenLabel = row.lastSeenMs
    ? new Intl.DateTimeFormat(locale, {
        day: '2-digit',
        hour: '2-digit',
        hour12: false,
        minute: '2-digit',
        month: '2-digit',
      }).format(new Date(row.lastSeenMs))
    : '-';

  return [
    {
      accent: 'blue',
      icon: 'calls',
      label: t('usage_analytics.metric_request_count'),
      meta: `${t('usage_analytics.metric_total_tokens')} ${formatCompactNumber(row.totalTokens)}`,
      value: formatCompactNumber(row.requestCount),
      valueTitle: formatFullNumber(row.requestCount, locale),
    },
    {
      accent: 'amber',
      icon: 'cost',
      label: t('usage_analytics.average_cost'),
      meta: `${t('usage_analytics.metric_estimated_cost')} ${formatMetricValue('estimatedCost', row.estimatedCost)}`,
      value: formatMetricValue('estimatedCost', averageCost),
    },
    {
      accent: 'teal',
      icon: 'tokens',
      label: t('usage_analytics.average_tokens_per_request'),
      meta: `${t('usage_analytics.cache_read_rate')} ${formatPercent(cacheRate)}`,
      value: formatCompactNumber(averageTokens),
      valueTitle: formatFullNumber(averageTokens, locale),
    },
    {
      accent: 'cyan',
      icon: 'cache',
      label: t('usage_analytics.cache_read_rate'),
      meta: `${t('usage_analytics.metric_cached_tokens')} ${formatCompactNumber(getUsageCacheTokens(row))}`,
      value: formatPercent(cacheRate),
    },
    {
      accent: 'green',
      icon: 'success',
      label: t('usage_analytics.success_rate'),
      meta: `${t('usage_analytics.metric_failure_count')} ${formatCompactNumber(row.failureCount)}`,
      tone:
        row.requestCount > 0 && row.successRate < USAGE_SUCCESS_RATE_WATCH_THRESHOLD
          ? 'bad'
          : 'good',
      value: formatPercent(row.successRate),
    },
    {
      accent: 'red',
      icon: 'failure',
      label: t('usage_analytics.metric_failure_count'),
      meta: `${t('usage_analytics.failure_rate')} ${formatPercent(failureRate)}`,
      tone: row.failureCount > 0 ? 'bad' : 'good',
      value: formatCompactNumber(row.failureCount),
    },
    {
      accent: 'blue',
      icon: 'latency',
      label: t('usage_analytics.metric_average_latency'),
      meta:
        row.averageLatencyMs === null || row.averageLatencyMs === undefined
          ? t('usage_analytics.summary_meta')
          : `${t('usage_analytics.summary_meta')}`,
      value: formatUsageDurationMs(row.averageLatencyMs),
    },
    {
      accent: 'cyan',
      icon: 'trend',
      label: t('usage_analytics.credential_last_seen'),
      meta: t('usage_analytics.credential_last_seen_meta'),
      value: lastSeenLabel,
      valueTitle: row.lastSeenMs
        ? new Intl.DateTimeFormat(locale, {
            day: '2-digit',
            hour: '2-digit',
            hour12: false,
            minute: '2-digit',
            month: '2-digit',
            year: 'numeric',
          }).format(new Date(row.lastSeenMs))
        : '-',
    },
  ];
};
