import { useCallback, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import type {
  DataZoomComponentOption,
  GridComponentOption,
  LegendComponentOption,
  TooltipComponentOption,
  VisualMapComponentOption,
} from 'echarts/components';
import type { BarSeriesOption, HeatmapSeriesOption, LineSeriesOption } from 'echarts/charts';
import type { ComposeOption, ECElementEvent } from 'echarts/core';
import { EChartsView } from '@/components/charts/EChartsView';
import { Button } from '@/components/ui/Button';
import { Select, type SelectOption } from '@/components/ui/Select';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import {
  IconBinary,
  IconCopy,
  IconDollarSign,
  IconExternalLink,
  IconFileText,
  IconInbox,
  IconKey,
  IconModelCluster,
  IconRefreshCw,
  IconSearch,
  IconShield,
  IconTrendingUp,
  IconX,
} from '@/components/ui/icons';
import { useNotificationStore, useThemeStore } from '@/stores';
import { copyToClipboard } from '@/utils/clipboard';
import {
  buildUsageHeatmapChartData,
  buildModelKeyDistribution,
  buildMonitoringDetailUrl,
  buildOptionValues,
  computeRowAverageCostPerCall,
  computeRowCacheHitRate,
  summarizeAnomalies,
  anomalyMetricLabelKey,
  DEFAULT_SELECTED_METRICS,
  formatDateTimeLocalValue,
  formatHeatmapMetricValue,
  formatLocalDateTime,
  formatMetricValue,
  getUsageCacheTokens,
  hasUsageData,
  maskApiKeyHash,
  parseDateTimeLocalValue,
  resolveUsageApiKeyLabel,
  USAGE_ANALYTICS_TABS,
  USAGE_HEATMAP_METRICS,
  USAGE_HEATMAP_SCALE_MODES,
  USAGE_METRICS,
  USAGE_SUCCESS_RATE_WATCH_THRESHOLD,
  USAGE_TIME_RANGES,
  type UsageAnalyticsTab,
  type UsageEntityTrendSeries,
  type UsageAnalyticsGranularity,
  type UsageAnalyticsCacheStatus,
  type UsageAnalyticsLatencyFilter,
  type UsageAnalyticsStatus,
  type UsageApiKeyContextRow,
  type UsageDrilldownEvent,
  type UsageHeatmapCellDetail,
  type UsageHeatmapCellSelection,
  type UsageHeatmapContributor,
  type UsageHeatmapDateOption,
  type UsageHeatmapHighlight,
  type UsageHeatmapHighlights,
  type UsageHeatmapMetricKey,
  type UsageHeatmapPoint,
  type UsageHeatmapScaleMode,
  type UsageKeyAnomalyRow,
  type UsageMetricKey,
  type UsageModelKeyDistributionRow,
  type UsageProviderRow,
  type UsageRankRow,
  type UsageServerAnomaly,
  type UsageTimelinePoint,
  type UsageTrendMetricKey,
} from './usageAnalyticsModel';
import { useUsageAnalytics } from './useUsageAnalytics';
import { UsageSummarySection } from './components/UsageSummaryCards';
import {
  buildUsageEntitySummaryCards,
  buildUsageApiKeySummaryCards,
  buildUsageModelSummaryCards,
  buildUsageHeatmapSummaryCards,
  buildUsageOverviewSummaryCards,
  buildUsageTrendSummaryCards,
  buildCredentialDetailCards,
  formatUsageDurationMs,
} from './usageAnalyticsPresentation';
import styles from './UsageAnalyticsPage.module.scss';

const trendMetricOptions: Array<{ value: UsageTrendMetricKey; labelKey: string }> = [
  { value: 'requestCount', labelKey: 'usage_analytics.trend_metric_requestCount' },
  { value: 'totalTokens', labelKey: 'usage_analytics.trend_metric_totalTokens' },
  { value: 'estimatedCost', labelKey: 'usage_analytics.trend_metric_estimatedCost' },
];

const heatmapMetricOptions: Array<{ value: UsageHeatmapMetricKey; labelKey: string }> =
  USAGE_HEATMAP_METRICS.map((metric) => ({
    value: metric,
    labelKey: `usage_analytics.heatmap_metric_${metric}`,
  }));

const heatmapScaleOptions: Array<{ value: UsageHeatmapScaleMode; labelKey: string }> =
  USAGE_HEATMAP_SCALE_MODES.map((mode) => ({
    value: mode,
    labelKey: `usage_analytics.heatmap_scale_${mode}`,
  }));

const chartHeight = 360;
const compactChartHeight = 220;

type UsageTrendChartOption = ComposeOption<
  | DataZoomComponentOption
  | GridComponentOption
  | LegendComponentOption
  | LineSeriesOption
  | TooltipComponentOption
>;

type CostRankChartOption = ComposeOption<
  BarSeriesOption | GridComponentOption | TooltipComponentOption
>;

type HealthChartOption = ComposeOption<
  | BarSeriesOption
  | GridComponentOption
  | LegendComponentOption
  | LineSeriesOption
  | TooltipComponentOption
>;

type TokenStructureChartOption = ComposeOption<
  BarSeriesOption | GridComponentOption | LegendComponentOption | TooltipComponentOption
>;

type EntityTrendChartOption = ComposeOption<
  GridComponentOption | LegendComponentOption | LineSeriesOption | TooltipComponentOption
>;

type HeatmapChartOption = ComposeOption<
  GridComponentOption | HeatmapSeriesOption | TooltipComponentOption | VisualMapComponentOption
>;

type CostShareRankStyle = CSSProperties & Record<'--rank-color' | '--rank-share', string | number>;

const usageChartAxisKeys = {
  requests: 0,
  tokens: 1,
  cost: 2,
} as const;

type UsageChartTheme = {
  axisColors: Record<'requests' | 'tokens' | 'cost', string>;
  categoryPalette: string[];
  heatmapColors: string[];
  healthColors: {
    failure: string;
    latency: string;
    success: string;
  };
  metricColors: Record<UsageMetricKey, string>;
  surface: {
    axisLabel: string;
    axisLine: string;
    axisPointer: string;
    barBackground: string;
    heatmapCellBorder: string;
    heatmapEmphasisBorder: string;
    pieBorder: string;
    pieShadow: string;
    selectedLine: string;
    splitLine: string;
    tooltipBackground: string;
    tooltipBorder: string;
    tooltipMuted: string;
    tooltipShadow: string;
    tooltipText: string;
  };
  tokenStructureColors: string[];
};

const lightUsageChartTheme: UsageChartTheme = {
  axisColors: {
    requests: '#409eff',
    tokens: '#14b8a6',
    cost: '#f59e0b',
  },
  categoryPalette: ['#409eff', '#14b8a6', '#f59e0b', '#f56c6c', '#94a3b8'],
  heatmapColors: ['#eff6ff', '#93c5fd', '#409eff', '#0f766e'],
  healthColors: {
    failure: '#f56c6c',
    latency: '#0ea5e9',
    success: '#67c23a',
  },
  metricColors: {
    cachedTokens: '#06b6d4',
    estimatedCost: '#f59e0b',
    inputTokens: '#60a5fa',
    outputTokens: '#22c55e',
    requestCount: '#409eff',
    totalTokens: '#14b8a6',
  },
  surface: {
    axisLabel: '#5f6c7b',
    axisLine: '#d8e5f2',
    axisPointer: '#8b95a6',
    barBackground: 'rgba(139, 149, 166, 0.14)',
    heatmapCellBorder: '#ffffff',
    heatmapEmphasisBorder: '#2c3e50',
    pieBorder: '#ffffff',
    pieShadow: 'rgba(15, 23, 42, 0.18)',
    selectedLine: '#8b95a6',
    splitLine: '#d3e1ef',
    tooltipBackground: 'rgba(255, 255, 255, 0.96)',
    tooltipBorder: '#d8e5f2',
    tooltipMuted: '#5f6c7b',
    tooltipShadow: 'box-shadow: 0 16px 36px rgba(15, 23, 42, 0.14);',
    tooltipText: '#2c3e50',
  },
  tokenStructureColors: ['#60a5fa', '#22c55e', '#06b6d4', '#f59e0b'],
};

const darkUsageChartTheme: UsageChartTheme = {
  axisColors: {
    requests: '#79bbff',
    tokens: '#2dd4bf',
    cost: '#fbbf24',
  },
  categoryPalette: ['#79bbff', '#2dd4bf', '#fbbf24', '#fab6b6', '#a3a6ad'],
  heatmapColors: ['#102f4f', '#1d5f98', '#409eff', '#79bbff'],
  healthColors: {
    failure: '#fab6b6',
    latency: '#7dd3fc',
    success: '#95d475',
  },
  metricColors: {
    cachedTokens: '#22d3ee',
    estimatedCost: '#fbbf24',
    inputTokens: '#60a5fa',
    outputTokens: '#95d475',
    requestCount: '#79bbff',
    totalTokens: '#2dd4bf',
  },
  surface: {
    axisLabel: '#a3a3a3',
    axisLine: 'rgba(255, 255, 255, 0.12)',
    axisPointer: '#7a7a7a',
    barBackground: 'rgba(255, 255, 255, 0.08)',
    heatmapCellBorder: '#1b1f2a',
    heatmapEmphasisBorder: '#e5e5e5',
    pieBorder: '#1b1f2a',
    pieShadow: 'rgba(0, 0, 0, 0.36)',
    selectedLine: '#7a7a7a',
    splitLine: 'rgba(255, 255, 255, 0.1)',
    tooltipBackground: 'rgba(24, 28, 40, 0.96)',
    tooltipBorder: 'rgba(255, 255, 255, 0.12)',
    tooltipMuted: '#a3a3a3',
    tooltipShadow: 'box-shadow: 0 16px 36px rgba(0, 0, 0, 0.38);',
    tooltipText: '#e5e5e5',
  },
  tokenStructureColors: ['#60a5fa', '#95d475', '#22d3ee', '#fbbf24'],
};

const getUsageChartTheme = (resolvedTheme: 'light' | 'dark'): UsageChartTheme =>
  resolvedTheme === 'dark' ? darkUsageChartTheme : lightUsageChartTheme;

const useUsageChartTheme = () => {
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  return getUsageChartTheme(resolvedTheme);
};

const appendHexAlpha = (color: string, alphaHex: string) =>
  /^#[\da-f]{6}$/i.test(color) ? `${color}${alphaHex}` : color;

const getThemedUsageMetrics = (chartTheme: UsageChartTheme): typeof USAGE_METRICS =>
  USAGE_METRICS.map((metric) => ({
    ...metric,
    color: chartTheme.metricColors[metric.key],
  })) as typeof USAGE_METRICS;

const compactNumber = (value: number) => {
  if (!Number.isFinite(value)) return '0';
  if (Math.abs(value) >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(2)}B`;
  if (Math.abs(value) >= 1_000_000) return `${(value / 1_000_000).toFixed(2)}M`;
  if (Math.abs(value) >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
  return String(Math.round(value));
};

const formatPercent = (value: number) =>
  `${(Number.isFinite(value) ? value * 100 : 0).toFixed(2)}%`;

const formatDrilldownDiagnostics = (row: UsageDrilldownEvent, locale: string, t: TFunction) => {
  const parts: string[] = [];
  if (row.headerErrorCode || row.headerErrorKind) {
    parts.push(
      `${t('monitoring.header_error')}: ${[row.headerErrorKind, row.headerErrorCode]
        .filter(Boolean)
        .join(' / ')}`
    );
  }
  if (row.headerTraceId) {
    parts.push(`${t('monitoring.header_trace')}: ${row.headerTraceId}`);
  }
  const quotaParts = [
    row.headerQuotaPlanType,
    typeof row.headerQuotaUsedPercent === 'number'
      ? formatPercent(row.headerQuotaUsedPercent / 100)
      : '',
    row.headerQuotaRecoverAtMs
      ? `${t('monitoring.header_recover_at')} ${formatLocalDateTime(row.headerQuotaRecoverAtMs, locale)}`
      : '',
  ].filter(Boolean);
  if (quotaParts.length > 0) {
    parts.push(`${t('monitoring.header_quota')}: ${quotaParts.join(' · ')}`);
  }
  return parts.join('\n');
};

const formatDelta = (value: number) => {
  const sign = value > 0 ? '+' : '';
  return `${sign}${(value * 100).toFixed(1)}%`;
};

const formatHeatmapMetricDifference = (metric: UsageHeatmapMetricKey, value: number) => {
  if (!Number.isFinite(value) || value === 0) return formatHeatmapMetricValue(metric, 0);
  const sign = value > 0 ? '+' : '-';
  return `${sign}${formatHeatmapMetricValue(metric, Math.abs(value))}`;
};

const formatHeatmapVisualValue = (
  metric: UsageHeatmapMetricKey,
  scaleMode: UsageHeatmapScaleMode,
  value: number
) =>
  scaleMode === 'absolute'
    ? formatHeatmapMetricValue(metric, value)
    : `${Math.round((Number.isFinite(value) ? value : 0) * 100)}%`;

const heatmapMetricValueFromDatum = (
  metric: UsageHeatmapMetricKey,
  calls = 0,
  tokens = 0,
  cost = 0,
  failureRate = 0
) => {
  if (metric === 'estimatedCost') return cost;
  if (metric === 'totalTokens') return tokens;
  if (metric === 'failureRate') return failureRate;
  return calls;
};

const getHeatmapEventSelection = (event: ECElementEvent): UsageHeatmapCellSelection | null => {
  const value = event.value;
  if (!Array.isArray(value)) return null;
  const [hour, weekday] = value;
  if (typeof hour !== 'number' || typeof weekday !== 'number') return null;
  return { weekday, hour };
};

const escapeHtml = (value: string | number | null | undefined) =>
  String(value ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');

const tooltipTitleHtml = (chartTheme: UsageChartTheme, titleHtml: string) =>
  `<b style="color:${chartTheme.surface.tooltipText}">${titleHtml}</b>`;

const tooltipRowHtml = (chartTheme: UsageChartTheme, labelHtml: string, valueHtml: string) =>
  `<div class="${styles.echartsTooltipRow}" style="color:${chartTheme.surface.tooltipMuted}"><span>${labelHtml}</span><strong style="color:${chartTheme.surface.tooltipText}">${valueHtml}</strong></div>`;

const tooltipHtml = (chartTheme: UsageChartTheme, rowsHtml: string, titleHtml?: string | null) =>
  `<div class="${styles.echartsTooltip}" style="color:${chartTheme.surface.tooltipMuted}">${
    titleHtml ? tooltipTitleHtml(chartTheme, titleHtml) : ''
  }${rowsHtml}</div>`;

const getTooltipOption = (chartTheme: UsageChartTheme) => ({
  backgroundColor: chartTheme.surface.tooltipBackground,
  borderColor: chartTheme.surface.tooltipBorder,
  extraCssText: chartTheme.surface.tooltipShadow,
  textStyle: {
    color: chartTheme.surface.tooltipMuted,
  },
});

const getMetricLabel = (key: UsageMetricKey, t: ReturnType<typeof useTranslation>['t']) => {
  const metric = USAGE_METRICS.find((item) => item.key === key);
  return metric ? t(metric.labelKey) : key;
};

const formatTrendMetricValue = (key: UsageTrendMetricKey, value: number) => {
  if (key === 'estimatedCost') return formatMetricValue('estimatedCost', value);
  if (key === 'totalTokens') return formatMetricValue('totalTokens', value);
  return formatMetricValue('requestCount', value);
};

const buildStableSelectOptions = (
  allLabel: string,
  values: Array<string | null | undefined>,
  currentValue?: string,
  currentLabel?: string
): SelectOption[] => {
  const normalizedCurrent = currentValue?.trim();
  const options = buildOptionValues([
    ...values,
    normalizedCurrent && normalizedCurrent !== 'all' ? normalizedCurrent : undefined,
  ]).map((value) => ({
    value,
    label: value === normalizedCurrent && currentLabel ? currentLabel : value,
  }));
  return [{ value: 'all', label: allLabel }, ...options];
};

type StableUsageOptionCache = {
  models: string[];
  providers: string[];
  authFiles: string[];
  apiKeys: SelectOption[];
};

const emptyStableOptionCache = (): StableUsageOptionCache => ({
  models: [],
  providers: [],
  authFiles: [],
  apiKeys: [],
});

const mergeSelectOptions = (options: SelectOption[]) =>
  Array.from(
    new Map(
      options
        .filter((option) => option.value.trim())
        .map((option) => [option.value, option] as const)
    ).values()
  ).sort((left, right) => left.label.localeCompare(right.label));

const mergeStableOptionCache = (
  current: StableUsageOptionCache,
  incoming: StableUsageOptionCache
): StableUsageOptionCache => ({
  models: buildOptionValues([...current.models, ...incoming.models]),
  providers: buildOptionValues([...current.providers, ...incoming.providers]),
  authFiles: buildOptionValues([...current.authFiles, ...incoming.authFiles]),
  apiKeys: mergeSelectOptions([...current.apiKeys, ...incoming.apiKeys]),
});

const stableOptionCachesEqual = (left: StableUsageOptionCache, right: StableUsageOptionCache) =>
  left.models.join('\n') === right.models.join('\n') &&
  left.providers.join('\n') === right.providers.join('\n') &&
  left.authFiles.join('\n') === right.authFiles.join('\n') &&
  left.apiKeys.map((option) => `${option.value}:${option.label}`).join('\n') ===
    right.apiKeys.map((option) => `${option.value}:${option.label}`).join('\n');

const getApiKeyRowDisplayLabel = (row: Pick<UsageRankRow, 'apiKeyHash' | 'id' | 'label'>) => {
  const hash = row.apiKeyHash || row.id || '';
  const label = row.label?.trim();
  return label && label.toLowerCase() !== hash.toLowerCase() ? label : maskApiKeyHash(hash);
};

const getApiKeyContributorDisplayLabel = (row: UsageHeatmapContributor) => {
  const label = row.label?.trim();
  return label && label.toLowerCase() !== row.key.toLowerCase() ? label : maskApiKeyHash(row.key);
};

const metricValue = (point: UsageTimelinePoint, key: UsageMetricKey) =>
  key === 'cachedTokens' ? getUsageCacheTokens(point) : point[key];

const getMetricAxisIndex = (axis: (typeof USAGE_METRICS)[number]['axis']) =>
  usageChartAxisKeys[axis];

const getAxisValueFormatter = (axis: (typeof USAGE_METRICS)[number]['axis']) => {
  if (axis === 'cost') return (value: number) => formatMetricValue('estimatedCost', value);
  if (axis === 'requests') return (value: number) => compactNumber(value);
  return (value: number) => compactNumber(value);
};

const getUsageChartTooltipFormatter =
  (
    timeline: UsageTimelinePoint[],
    locale: string,
    t: ReturnType<typeof useTranslation>['t'],
    chartTheme: UsageChartTheme
  ) =>
  (params: unknown) => {
    const items = Array.isArray(params) ? params : [params];
    const first = items[0] as { dataIndex?: number } | undefined;
    const point = typeof first?.dataIndex === 'number' ? timeline[first.dataIndex] : undefined;
    const rows = items
      .map((item) => {
        const entry = item as {
          color?: string;
          data?: number;
          marker?: string;
          seriesName?: string;
        };
        const metric = USAGE_METRICS.find(
          (candidate) => t(candidate.labelKey) === entry.seriesName
        );
        const value =
          metric && typeof entry.data === 'number'
            ? formatMetricValue(metric.key, entry.data)
            : String(entry.data ?? '-');
        return tooltipRowHtml(
          chartTheme,
          `${entry.marker ?? ''}${escapeHtml(entry.seriesName)}`,
          escapeHtml(value)
        );
      })
      .join('');

    return tooltipHtml(
      chartTheme,
      rows,
      escapeHtml(point ? formatLocalDateTime(point.bucketMs, locale) : '')
    );
  };

const buildUsageTrendChartOption = ({
  compact,
  locale,
  metrics,
  selectedBucket,
  t,
  chartTheme,
  timeline,
}: {
  compact: boolean;
  locale: string;
  metrics: typeof USAGE_METRICS;
  selectedBucket?: UsageTimelinePoint | null;
  t: ReturnType<typeof useTranslation>['t'];
  chartTheme: UsageChartTheme;
  timeline: UsageTimelinePoint[];
}): UsageTrendChartOption => {
  const selectedLabel = selectedBucket?.label;
  const visibleAxisSet = new Set(metrics.map((metric) => metric.axis));
  const requestsVisible = visibleAxisSet.has('requests');
  const tokensVisible = visibleAxisSet.has('tokens');
  const costVisible = visibleAxisSet.has('cost');
  const tokensOnRight = tokensVisible && requestsVisible;
  const costOnRight = costVisible && (requestsVisible || tokensVisible);
  const rightAxisCount = Number(tokensOnRight) + Number(costOnRight);
  const splitLineAxis = requestsVisible ? 'requests' : tokensVisible ? 'tokens' : 'cost';
  const selectedLine =
    selectedLabel && metrics.length > 0
      ? {
          symbol: ['none', 'none'],
          label: { show: false },
          lineStyle: {
            color: chartTheme.surface.selectedLine,
            type: 'dashed' as const,
            width: 1.5,
          },
          data: [{ xAxis: selectedLabel }],
          silent: true,
        }
      : undefined;

  return {
    animationDuration: compact ? 180 : 320,
    backgroundColor: 'transparent',
    color: metrics.map((metric) => metric.color),
    dataZoom:
      timeline.length > 12
        ? [
            {
              type: 'inside',
              xAxisIndex: 0,
              filterMode: 'none',
              minSpan: Math.min(100, Math.max(10, (6 / timeline.length) * 100)),
              zoomOnMouseWheel: true,
              moveOnMouseMove: true,
              moveOnMouseWheel: false,
            },
          ]
        : [],
    grid: {
      bottom: compact ? 34 : 44,
      containLabel: true,
      left: 10,
      right: rightAxisCount > 1 ? 104 : rightAxisCount === 1 ? 72 : 28,
      top: compact ? 16 : 28,
    },
    legend: {
      bottom: 0,
      icon: 'circle',
      itemGap: 16,
      itemHeight: 8,
      itemWidth: 8,
      selectedMode: false,
      textStyle: {
        color: chartTheme.surface.axisLabel,
        fontSize: 12,
        fontWeight: 700,
      },
    },
    tooltip: {
      appendToBody: true,
      axisPointer: {
        lineStyle: {
          color: chartTheme.surface.axisPointer,
          type: 'dashed',
          width: 1,
        },
        snap: true,
        type: 'line',
      },
      ...getTooltipOption(chartTheme),
      borderRadius: 10,
      borderWidth: 1,
      className: styles.echartsTooltipWrapper,
      confine: true,
      formatter: getUsageChartTooltipFormatter(timeline, locale, t, chartTheme),
      padding: 0,
      trigger: 'axis',
    },
    xAxis: {
      axisLabel: {
        color: chartTheme.surface.axisLabel,
        fontSize: 11,
        fontWeight: 700,
        hideOverlap: true,
        margin: 14,
      },
      axisLine: {
        lineStyle: {
          color: chartTheme.surface.axisLine,
        },
      },
      axisTick: { show: false },
      boundaryGap: false,
      data: timeline.map((point) => point.label),
      type: 'category',
    },
    yAxis: [
      {
        axisLabel: {
          color: chartTheme.axisColors.requests,
          formatter: getAxisValueFormatter('requests'),
          fontWeight: 700,
        },
        nameTextStyle: { color: chartTheme.axisColors.requests },
        position: 'left',
        scale: true,
        show: requestsVisible,
        splitLine: {
          show: splitLineAxis === 'requests',
          lineStyle: {
            color: chartTheme.surface.splitLine,
            type: 'dashed',
          },
        },
        type: 'value',
      },
      {
        axisLabel: {
          color: chartTheme.axisColors.tokens,
          formatter: getAxisValueFormatter('tokens'),
          fontWeight: 700,
        },
        offset: tokensOnRight && costOnRight ? 46 : 0,
        position: tokensOnRight ? 'right' : 'left',
        scale: true,
        show: tokensVisible,
        splitLine: {
          show: splitLineAxis === 'tokens',
          lineStyle: {
            color: chartTheme.surface.splitLine,
            type: 'dashed',
          },
        },
        type: 'value',
      },
      {
        axisLabel: {
          color: chartTheme.axisColors.cost,
          formatter: getAxisValueFormatter('cost'),
          fontWeight: 700,
        },
        position: costOnRight ? 'right' : 'left',
        scale: true,
        show: costVisible,
        splitLine: {
          show: splitLineAxis === 'cost',
          lineStyle: {
            color: chartTheme.surface.splitLine,
            type: 'dashed',
          },
        },
        type: 'value',
      },
    ],
    series: metrics.map((metric, index) => ({
      areaStyle:
        compact || index > 1
          ? undefined
          : {
              color: {
                colorStops: [
                  { color: appendHexAlpha(metric.color, compact ? '1f' : '2e'), offset: 0 },
                  { color: appendHexAlpha(metric.color, '00'), offset: 1 },
                ],
                x: 0,
                x2: 0,
                y: 0,
                y2: 1,
                type: 'linear',
              },
            },
      connectNulls: true,
      data: timeline.map((point) => metricValue(point, metric.key)),
      emphasis: {
        focus: 'series',
        lineStyle: { width: compact ? 2.6 : 3.2 },
      },
      lineStyle: {
        color: metric.color,
        width: compact ? 2 : 2.5,
      },
      markLine: index === 0 && selectedLine ? selectedLine : undefined,
      name: t(metric.labelKey),
      showSymbol: timeline.length <= 36,
      smooth: 0.25,
      symbol: 'circle',
      symbolSize: compact ? 5 : 6,
      type: 'line',
      yAxisIndex: getMetricAxisIndex(metric.axis),
    })),
  };
};

function UsageLineChart({
  timeline,
  selectedMetrics,
  selectedBucket,
  onSelectBucket,
  compact = false,
}: {
  timeline: UsageTimelinePoint[];
  selectedMetrics: UsageMetricKey[];
  selectedBucket?: UsageTimelinePoint | null;
  onSelectBucket?: (point: UsageTimelinePoint) => void;
  compact?: boolean;
}) {
  const { t, i18n } = useTranslation();
  const chartTheme = useUsageChartTheme();
  const metrics = useMemo(
    () =>
      getThemedUsageMetrics(chartTheme).filter((metric) => selectedMetrics.includes(metric.key)),
    [chartTheme, selectedMetrics]
  );
  const option = useMemo(
    () =>
      buildUsageTrendChartOption({
        chartTheme,
        compact,
        locale: i18n.language,
        metrics,
        selectedBucket,
        t,
        timeline,
      }),
    [chartTheme, compact, i18n.language, metrics, selectedBucket, t, timeline]
  );
  const handleClick = useCallback(
    (event: ECElementEvent) => {
      const dataIndex = typeof event.dataIndex === 'number' ? event.dataIndex : -1;
      const point = dataIndex >= 0 ? timeline[dataIndex] : undefined;
      if (point) onSelectBucket?.(point);
    },
    [onSelectBucket, timeline]
  );

  if (timeline.length === 0 || metrics.length === 0) {
    return (
      <div className={styles.chartEmptyInline}>
        <IconInbox size={24} />
        <span>{t('usage_analytics.empty_title')}</span>
      </div>
    );
  }

  const height = compact ? compactChartHeight : chartHeight;

  return (
    <div className={styles.localChart}>
      <EChartsView
        option={option}
        className={styles.echartsCanvas}
        style={{ height }}
        role={onSelectBucket ? 'button' : 'img'}
        ariaLabel={t('usage_analytics.trend_title')}
        onClick={onSelectBucket ? handleClick : undefined}
      />
    </div>
  );
}

function CostShareChart({ rows }: { rows: UsageRankRow[] }) {
  const { t } = useTranslation();
  const chartTheme = useUsageChartTheme();
  const totalCost = rows.reduce((sum, row) => sum + row.estimatedCost, 0);
  const chartRows = [...rows]
    .filter((row) => row.estimatedCost > 0)
    .sort((left, right) => right.estimatedCost - left.estimatedCost)
    .slice(0, 5);
  const maxCost = Math.max(...chartRows.map((row) => row.estimatedCost), 0);

  if (totalCost <= 0 || chartRows.length === 0) {
    return (
      <div className={styles.chartEmptyInline}>
        <IconInbox size={24} />
        <span>{formatMetricValue('estimatedCost', 0)}</span>
      </div>
    );
  }

  return (
    <div className={styles.costShareChart}>
      <div className={styles.costShareSummary}>
        <span>{t('usage_analytics.total_cost')}</span>
        <strong>{formatMetricValue('estimatedCost', totalCost)}</strong>
      </div>
      <div className={styles.costShareRankList}>
        {chartRows.map((row, index) => (
          <div
            key={row.id}
            className={styles.costShareRankRow}
            title={`${row.label} ${formatMetricValue('estimatedCost', row.estimatedCost)} ${formatPercent(
              row.estimatedCost / totalCost
            )}`}
          >
            <span className={styles.costShareRankHeader}>
              <span className={styles.costShareRankIdentity}>
                <i
                  className={styles.costShareRankSwatch}
                  style={{
                    background:
                      chartTheme.categoryPalette[index % chartTheme.categoryPalette.length],
                  }}
                  aria-hidden="true"
                />
                <span>{row.label}</span>
              </span>
              <span className={styles.costShareRankMeta}>
                <strong>{formatMetricValue('estimatedCost', row.estimatedCost)}</strong>
                <span>{formatPercent(row.estimatedCost / totalCost)}</span>
              </span>
            </span>
            <span className={styles.costShareRankTrack} aria-hidden="true">
              <span
                className={styles.costShareRankBar}
                style={
                  {
                    '--rank-color':
                      chartTheme.categoryPalette[index % chartTheme.categoryPalette.length],
                    '--rank-share': maxCost > 0 ? row.estimatedCost / maxCost : 0,
                  } as CostShareRankStyle
                }
              />
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

function CostRankChart({ rows, title }: { rows: UsageRankRow[]; title: string }) {
  const { t } = useTranslation();
  const chartTheme = useUsageChartTheme();
  const chartRows = useMemo(
    () =>
      [...rows]
        .filter((row) => row.estimatedCost > 0)
        .sort((left, right) => right.estimatedCost - left.estimatedCost)
        .slice(0, 5),
    [rows]
  );
  const maxCost = Math.max(...chartRows.map((row) => row.estimatedCost), 0);

  const option = useMemo<CostRankChartOption>(
    () => ({
      animationDuration: 260,
      backgroundColor: 'transparent',
      grid: { bottom: 8, containLabel: true, left: 8, right: 74, top: 8 },
      tooltip: {
        appendToBody: true,
        ...getTooltipOption(chartTheme),
        borderRadius: 10,
        borderWidth: 1,
        className: styles.echartsTooltipWrapper,
        confine: true,
        formatter: (params: unknown) => {
          const item = params as {
            data?: { share?: number; value?: number };
            marker?: string;
            name?: string;
          };
          const value = Number(item.data?.value ?? 0);
          const share = Number(item.data?.share ?? 0);
          const titleHtml = escapeHtml(
            item.name // user-controlled tooltip label
          );
          return tooltipHtml(
            chartTheme,
            `${tooltipRowHtml(
              chartTheme,
              `${item.marker ?? ''}${escapeHtml(t('usage_analytics.total_cost'))}`,
              escapeHtml(formatMetricValue('estimatedCost', value))
            )}${tooltipRowHtml(
              chartTheme,
              escapeHtml(t('usage_analytics.share')),
              escapeHtml(formatPercent(share))
            )}`,
            titleHtml
          );
        },
        padding: 0,
        trigger: 'item',
      },
      xAxis: {
        axisLabel: { show: false },
        axisLine: { show: false },
        axisTick: { show: false },
        max: Math.max(maxCost * 1.18, 1),
        splitLine: { show: false },
        type: 'value',
      },
      yAxis: {
        axisLabel: {
          color: chartTheme.surface.tooltipText,
          fontSize: 12,
          fontWeight: 700,
          overflow: 'truncate',
          width: 138,
        },
        axisLine: { show: false },
        axisTick: { show: false },
        data: chartRows.map((row) => row.label),
        inverse: true,
        type: 'category',
      },
      series: [
        {
          barMaxWidth: 16,
          barWidth: 14,
          data: chartRows.map((row, index) => ({
            itemStyle: {
              color: chartTheme.categoryPalette[index % chartTheme.categoryPalette.length],
            },
            share: row.share,
            value: row.estimatedCost,
          })),
          itemStyle: {
            borderRadius: [0, 8, 8, 0],
          },
          label: {
            color: chartTheme.surface.tooltipText,
            fontSize: 12,
            fontWeight: 800,
            formatter: (params: unknown) =>
              formatMetricValue('estimatedCost', Number((params as { value?: number }).value ?? 0)),
            position: 'right',
            show: true,
          },
          showBackground: true,
          backgroundStyle: {
            borderRadius: [0, 8, 8, 0],
            color: chartTheme.surface.barBackground,
          },
          type: 'bar',
        },
      ],
    }),
    [chartRows, chartTheme, maxCost, t]
  );

  if (chartRows.length === 0) {
    return (
      <div className={styles.chartEmptyInline}>
        <IconInbox size={24} />
        <span>{formatMetricValue('estimatedCost', 0)}</span>
      </div>
    );
  }

  return (
    <EChartsView
      option={option}
      className={styles.echartsCanvas}
      style={{ height: Math.max(180, chartRows.length * 36 + 26) }}
      ariaLabel={title}
    />
  );
}

const buildHealthChartOption = (
  timeline: UsageTimelinePoint[],
  t: ReturnType<typeof useTranslation>['t'],
  chartTheme: UsageChartTheme
): HealthChartOption => ({
  animationDuration: 260,
  backgroundColor: 'transparent',
  color: [
    chartTheme.healthColors.success,
    chartTheme.healthColors.failure,
    chartTheme.healthColors.latency,
  ],
  grid: { bottom: 34, containLabel: true, left: 8, right: 58, top: 20 },
  legend: {
    bottom: 0,
    icon: 'circle',
    itemHeight: 8,
    itemWidth: 8,
    textStyle: { color: chartTheme.surface.axisLabel, fontSize: 12, fontWeight: 700 },
  },
  tooltip: {
    appendToBody: true,
    axisPointer: { type: 'cross' },
    ...getTooltipOption(chartTheme),
    borderRadius: 10,
    borderWidth: 1,
    confine: true,
    formatter: (params: unknown) => {
      const items = Array.isArray(params) ? params : [params];
      const first = items[0] as { dataIndex?: number } | undefined;
      const point = typeof first?.dataIndex === 'number' ? timeline[first.dataIndex] : undefined;
      const rows = items
        .map((item) => {
          const entry = item as { marker?: string; seriesName?: string; data?: number };
          const value =
            entry.seriesName === t('usage_analytics.metric_average_latency')
              ? formatUsageDurationMs(Number(entry.data ?? 0))
              : formatPercent(Number(entry.data ?? 0));
          return tooltipRowHtml(
            chartTheme,
            `${entry.marker ?? ''}${escapeHtml(entry.seriesName)}`,
            escapeHtml(value)
          );
        })
        .join('');
      return tooltipHtml(chartTheme, rows, escapeHtml(point?.label));
    },
    padding: 0,
    trigger: 'axis',
  },
  xAxis: {
    axisLabel: {
      color: chartTheme.surface.axisLabel,
      fontSize: 11,
      fontWeight: 700,
      hideOverlap: true,
    },
    axisLine: { lineStyle: { color: chartTheme.surface.axisLine } },
    axisTick: { show: false },
    data: timeline.map((point) => point.label),
    type: 'category',
  },
  yAxis: [
    {
      axisLabel: {
        color: chartTheme.surface.axisLabel,
        formatter: (value: number) => `${Math.round(value * 100)}%`,
      },
      max: 1,
      min: 0,
      splitLine: { lineStyle: { color: chartTheme.surface.splitLine, type: 'dashed' } },
      type: 'value',
    },
    {
      axisLabel: {
        color: chartTheme.healthColors.latency,
        formatter: (value: number) => formatUsageDurationMs(value),
      },
      position: 'right',
      scale: true,
      splitLine: { show: false },
      type: 'value',
    },
  ],
  series: [
    {
      data: timeline.map((point) => point.successRate),
      lineStyle: { width: 2.5 },
      name: t('usage_analytics.success_rate'),
      showSymbol: timeline.length <= 36,
      smooth: 0.25,
      type: 'line',
      yAxisIndex: 0,
    },
    {
      data: timeline.map((point) => point.failureRate),
      lineStyle: { width: 2.5 },
      name: t('usage_analytics.failure_rate'),
      showSymbol: timeline.length <= 36,
      smooth: 0.25,
      type: 'line',
      yAxisIndex: 0,
    },
    {
      barMaxWidth: 16,
      data: timeline.map((point) => point.averageLatencyMs ?? 0),
      name: t('usage_analytics.metric_average_latency'),
      type: 'bar',
      yAxisIndex: 1,
    },
  ],
});

function HealthTrendChart({ timeline }: { timeline: UsageTimelinePoint[] }) {
  const { t } = useTranslation();
  const chartTheme = useUsageChartTheme();
  const option = useMemo(
    () => buildHealthChartOption(timeline, t, chartTheme),
    [chartTheme, timeline, t]
  );
  if (timeline.length === 0) {
    return (
      <div className={styles.chartEmptyInline}>
        <IconInbox size={24} />
        <span>{t('usage_analytics.empty_title')}</span>
      </div>
    );
  }
  return (
    <EChartsView
      option={option}
      className={styles.echartsCanvas}
      style={{ height: 260 }}
      ariaLabel={t('usage_analytics.health_trend_title')}
    />
  );
}

const weekdayLabelKeys = [
  'usage_analytics.weekday_sun',
  'usage_analytics.weekday_mon',
  'usage_analytics.weekday_tue',
  'usage_analytics.weekday_wed',
  'usage_analytics.weekday_thu',
  'usage_analytics.weekday_fri',
  'usage_analytics.weekday_sat',
] as const;

function UsageHeatmapChart({
  loading = false,
  metric,
  onSelect,
  points,
  scaleMode,
  selectedCell,
}: {
  loading?: boolean;
  metric: UsageHeatmapMetricKey;
  onSelect: (cell: UsageHeatmapCellSelection | null) => void;
  points: UsageHeatmapPoint[];
  scaleMode: UsageHeatmapScaleMode;
  selectedCell: UsageHeatmapCellSelection | null;
}) {
  const { t } = useTranslation();
  const chartTheme = useUsageChartTheme();
  const hours = Array.from({ length: 24 }, (_, hour) => `${String(hour).padStart(2, '0')}:00`);
  const weekdays = weekdayLabelKeys.map((key) => t(key));
  const data = buildUsageHeatmapChartData(points, metric, scaleMode);
  const maxVisualValue = Math.max(0, ...data.map((point) => point[2]));
  const maxValue = maxVisualValue > 0 ? maxVisualValue : 1;
  const minLegendLabel = formatHeatmapVisualValue(metric, scaleMode, 0);
  const maxLegendLabel = formatHeatmapVisualValue(metric, scaleMode, maxValue);
  const chartData = data.map((point) => {
    const selected = selectedCell?.hour === point[0] && selectedCell.weekday === point[1];
    return selected
      ? {
          value: point,
          itemStyle: {
            borderColor: chartTheme.axisColors.requests,
            borderWidth: 1,
            shadowBlur: 8,
            shadowColor: appendHexAlpha(chartTheme.axisColors.requests, '66'),
          },
        }
      : point;
  });
  const option: HeatmapChartOption = {
    animationDuration: 260,
    backgroundColor: 'transparent',
    grid: { bottom: 78, containLabel: true, left: 8, right: 14, top: 10 },
    tooltip: {
      appendToBody: true,
      ...getTooltipOption(chartTheme),
      borderRadius: 10,
      borderWidth: 1,
      confine: true,
      formatter: (params: unknown) => {
        const item = params as { value?: number[] };
        const [hour, weekday, visualValue, calls, success, failure, tokens, cost, failureRate] =
          item.value ?? [];
        const metricValue = heatmapMetricValueFromDatum(metric, calls, tokens, cost, failureRate);
        return tooltipHtml(
          chartTheme,
          `${tooltipRowHtml(
            chartTheme,
            escapeHtml(t(`usage_analytics.heatmap_metric_${metric}`)),
            escapeHtml(formatHeatmapMetricValue(metric, metricValue))
          )}${tooltipRowHtml(
            chartTheme,
            escapeHtml(t('usage_analytics.heatmap_color_value')),
            escapeHtml(formatHeatmapVisualValue(metric, scaleMode, visualValue ?? 0))
          )}${tooltipRowHtml(
            chartTheme,
            escapeHtml(t('usage_analytics.metric_request_count')),
            escapeHtml(compactNumber(calls ?? 0))
          )}${tooltipRowHtml(
            chartTheme,
            escapeHtml(t('usage_analytics.metric_total_tokens')),
            escapeHtml(compactNumber(tokens ?? 0))
          )}${tooltipRowHtml(
            chartTheme,
            escapeHtml(t('usage_analytics.metric_estimated_cost')),
            escapeHtml(formatMetricValue('estimatedCost', cost ?? 0))
          )}${tooltipRowHtml(
            chartTheme,
            escapeHtml(t('usage_analytics.metric_failure_count')),
            escapeHtml(compactNumber(failure ?? 0))
          )}${tooltipRowHtml(
            chartTheme,
            escapeHtml(t('usage_analytics.failure_rate')),
            escapeHtml(formatPercent(failureRate ?? 0))
          )}${tooltipRowHtml(
            chartTheme,
            escapeHtml(t('usage_analytics.status_success')),
            escapeHtml(compactNumber(success ?? 0))
          )}`,
          escapeHtml(`${weekdays[weekday] ?? ''} ${hours[hour] ?? ''}`)
        );
      },
      padding: 0,
    },
    visualMap: {
      bottom: 28,
      calculable: true,
      dimension: 2,
      formatter: () => '',
      inRange: { color: chartTheme.heatmapColors },
      itemHeight: 148,
      itemWidth: 16,
      left: 'center',
      max: maxValue,
      min: 0,
      orient: 'horizontal',
      textStyle: { color: chartTheme.surface.axisLabel, fontSize: 11 },
    },
    xAxis: {
      axisLabel: { color: chartTheme.surface.axisLabel, fontSize: 10 },
      axisTick: { show: false },
      data: hours,
      splitArea: { show: true },
      type: 'category',
    },
    yAxis: {
      axisLabel: { color: chartTheme.surface.axisLabel, fontSize: 11, fontWeight: 700 },
      axisTick: { show: false },
      data: weekdays,
      splitArea: { show: true },
      type: 'category',
    },
    series: [
      {
        data: chartData,
        encode: { x: 0, y: 1, value: 2, tooltip: [2, 3, 4] },
        emphasis: {
          itemStyle: {
            borderColor: chartTheme.axisColors.requests,
            borderWidth: 1,
            shadowBlur: 6,
            shadowColor: appendHexAlpha(chartTheme.axisColors.requests, '4D'),
          },
        },
        itemStyle: { borderColor: chartTheme.surface.heatmapCellBorder, borderWidth: 1 },
        label: { show: false },
        name: t('usage_analytics.heatmap_title'),
        type: 'heatmap',
      },
    ],
  };

  if (loading) {
    return (
      <div className={styles.chartEmptyInline}>
        <IconRefreshCw size={24} />
        <span>{t('common.loading')}</span>
      </div>
    );
  }

  if (data.length === 0) {
    return (
      <div className={styles.chartEmptyInline}>
        <IconInbox size={24} />
        <span>{t('usage_analytics.empty_title')}</span>
      </div>
    );
  }

  return (
    <div className={styles.heatmapChartFrame}>
      <EChartsView
        option={option}
        className={styles.echartsCanvas}
        style={{ height: 380 }}
        ariaLabel={t('usage_analytics.heatmap_title')}
        onClick={(event) => onSelect(getHeatmapEventSelection(event))}
      />
      <div className={styles.heatmapLegendLabels} aria-hidden="true">
        <span>{minLegendLabel}</span>
        <span>{maxLegendLabel}</span>
      </div>
    </div>
  );
}

const formatHeatmapWindowLabel = (
  point: Pick<UsageHeatmapPoint, 'hour' | 'weekday'>,
  weekdays: string[]
) => `${weekdays[point.weekday] ?? ''} ${String(point.hour).padStart(2, '0')}:00`;

function HeatmapDateTabs({
  dateOptions,
  onSelect,
  selectedKey,
}: {
  dateOptions: UsageHeatmapDateOption[];
  onSelect: (key: string) => void;
  selectedKey: string;
}) {
  const { t } = useTranslation();
  return (
    <div className={styles.heatmapDateTabsRow}>
      <div
        className={`${styles.segmentedControl} ${styles.heatmapDateTabs}`}
        aria-label={t('usage_analytics.heatmap_date_tabs_label')}
      >
        <button
          type="button"
          aria-pressed={selectedKey === 'all'}
          className={`${styles.segmentButton} ${
            selectedKey === 'all' ? styles.segmentButtonActive : ''
          }`}
          onClick={() => onSelect('all')}
        >
          {t('usage_analytics.heatmap_date_all')}
        </button>
        {dateOptions.map((option) => (
          <button
            key={option.key}
            type="button"
            aria-pressed={selectedKey === option.key}
            className={`${styles.segmentButton} ${
              selectedKey === option.key ? styles.segmentButtonActive : ''
            }`}
            onClick={() => onSelect(option.key)}
          >
            {option.label}
          </button>
        ))}
      </div>
    </div>
  );
}

function HeatmapContributorGroup({
  emptyLabel,
  kind,
  rows,
  title,
}: {
  emptyLabel: string;
  kind: 'model' | 'apiKey' | 'provider';
  rows: UsageHeatmapContributor[];
  title: string;
}) {
  const { t } = useTranslation();
  return (
    <div className={styles.heatmapContributorGroup}>
      <h3>{title}</h3>
      {rows.length === 0 ? (
        <span className={styles.shortcutEmpty}>{emptyLabel}</span>
      ) : (
        rows.map((row) => {
          const label =
            kind === 'apiKey' ? getApiKeyContributorDisplayLabel(row) : row.label || row.key;
          return (
            <div key={`${kind}-${row.key}`} className={styles.heatmapContributorRow}>
              <div className={styles.heatmapContributorMain}>
                <span>
                  <strong>{label}</strong>
                  <b>{compactNumber(row.requestCount)}</b>
                </span>
                <em>
                  {t('usage_analytics.heatmap_contributor_meta', {
                    cost: formatMetricValue('estimatedCost', row.estimatedCost),
                    share: formatPercent(row.share),
                    tokens: compactNumber(row.totalTokens),
                  })}
                </em>
              </div>
              <div className={styles.heatmapContributorProgress}>
                <span style={{ width: `${Math.min(100, Math.max(0, row.share * 100))}%` }} />
              </div>
            </div>
          );
        })
      )}
    </div>
  );
}

function HeatmapDetailPanel({
  dateError,
  dateLoading,
  dateOptions,
  detail,
  metric,
  onClear,
  onSelectDate,
  selectedCell,
  selectedDateKey,
  timeZone,
}: {
  dateError: string;
  dateLoading: boolean;
  dateOptions: UsageHeatmapDateOption[];
  detail: UsageHeatmapCellDetail | null;
  metric: UsageHeatmapMetricKey;
  onClear: () => void;
  onSelectDate: (key: string) => void;
  selectedCell: UsageHeatmapCellSelection;
  selectedDateKey: string;
  timeZone: string;
}) {
  const { i18n, t } = useTranslation();
  const detailNumberFormatter = useMemo(
    () => new Intl.NumberFormat(i18n.language),
    [i18n.language]
  );
  const weekdays = weekdayLabelKeys.map((key) => t(key));
  const selectedWindowLabel = formatHeatmapWindowLabel(detail?.point ?? selectedCell, weekdays);
  const metrics = detail
    ? [
        {
          accentClass: styles.heatmapDetailMetricBlue,
          icon: <IconInbox size={18} />,
          label: t('usage_analytics.metric_request_count'),
          title: detailNumberFormatter.format(detail.point.requestCount),
          value: compactNumber(detail.point.requestCount),
        },
        {
          accentClass: styles.heatmapDetailMetricTeal,
          icon: <IconBinary size={18} />,
          label: t('usage_analytics.metric_total_tokens'),
          title: detailNumberFormatter.format(detail.point.totalTokens),
          value: compactNumber(detail.point.totalTokens),
        },
        {
          accentClass: styles.heatmapDetailMetricAmber,
          icon: <IconDollarSign size={18} />,
          label: t('usage_analytics.metric_estimated_cost'),
          value: formatMetricValue('estimatedCost', detail.point.estimatedCost),
        },
        {
          accentClass: styles.heatmapDetailMetricRed,
          icon: <IconX size={18} />,
          label: t('usage_analytics.metric_failure_count'),
          title: detailNumberFormatter.format(detail.point.failureCount),
          toneClass:
            detail.point.failureCount > 0
              ? styles.heatmapDetailMetricDanger
              : styles.heatmapDetailMetricGood,
          value: compactNumber(detail.point.failureCount),
        },
        {
          accentClass: styles.heatmapDetailMetricRed,
          icon: <IconShield size={18} />,
          label: t('usage_analytics.failure_rate'),
          toneClass:
            detail.point.failureRate > 0
              ? styles.heatmapDetailMetricDanger
              : styles.heatmapDetailMetricGood,
          value: formatPercent(detail.point.failureRate),
        },
      ]
    : [];
  const comparisons = detail
    ? [
        {
          label: t('usage_analytics.heatmap_compare_overall'),
          average: detail.overallBaseline,
          delta: detail.overallDelta,
        },
        {
          label: t('usage_analytics.heatmap_compare_weekday'),
          average: detail.weekdayBaseline,
          delta: detail.weekdayDelta,
        },
        {
          label: t('usage_analytics.heatmap_compare_hour'),
          average: detail.hourBaseline,
          delta: detail.hourDelta,
        },
      ]
    : [];
  const contributorGroups = detail
    ? [
        {
          kind: 'model' as const,
          rows: detail.point.modelContributors ?? [],
          title: t('usage_analytics.heatmap_contributor_models'),
        },
        {
          kind: 'apiKey' as const,
          rows: detail.point.apiKeyContributors ?? [],
          title: t('usage_analytics.heatmap_contributor_api_keys'),
        },
        {
          kind: 'provider' as const,
          rows: detail.point.providerContributors ?? [],
          title: t('usage_analytics.heatmap_contributor_providers'),
        },
      ]
    : [];
  const dateCount = dateOptions.length;
  let content: ReactNode;

  if (dateLoading && !detail) {
    content = (
      <div className={styles.chartEmptyInline}>
        <IconRefreshCw size={24} />
        <span>{t('common.loading')}</span>
      </div>
    );
  } else if (dateError) {
    content = (
      <div className={styles.chartEmptyInline}>
        <IconX size={24} />
        <span>{t('usage_analytics.error_title')}</span>
        <span>{dateError}</span>
      </div>
    );
  } else if (!detail) {
    content = (
      <div className={styles.chartEmptyInline}>
        <IconInbox size={24} />
        <span>{t('usage_analytics.heatmap_date_empty')}</span>
      </div>
    );
  } else {
    content = (
      <div
        className={`${styles.heatmapDetailBody} ${
          dateLoading ? styles.heatmapDetailBodyRefreshing : ''
        }`}
        aria-busy={dateLoading}
      >
        {dateLoading ? (
          <div className={styles.heatmapDetailRefreshBadge} role="status">
            <IconRefreshCw size={14} />
            <span>{t('common.loading')}</span>
          </div>
        ) : null}
        <div className={styles.heatmapDetailSummary}>
          <div className={styles.heatmapDetailMetrics}>
            {metrics.map((item) => (
              <div
                key={item.label}
                className={`${styles.heatmapDetailMetricCard} ${item.accentClass}`}
              >
                <div className={styles.heatmapDetailMetricHeader}>
                  <span className={styles.heatmapDetailMetricIcon}>{item.icon}</span>
                  <span className={styles.heatmapDetailMetricLabel}>{item.label}</span>
                </div>
                <strong
                  className={`${styles.heatmapDetailMetricValue} ${item.toneClass ?? ''}`}
                  title={item.title ?? item.value}
                >
                  {item.value}
                </strong>
                <div className={styles.heatmapDetailMetricChart} aria-hidden="true">
                  <svg viewBox="0 0 100 30" preserveAspectRatio="none">
                    <path d="M0,24 C18,8 28,22 42,16 S67,4 82,13 S94,22 100,10" />
                  </svg>
                </div>
              </div>
            ))}
          </div>
          <div className={styles.heatmapComparisonPanel}>
            <div className={styles.heatmapComparisonHeader}>
              <span>{t(`usage_analytics.heatmap_metric_${metric}`)}</span>
              <strong>{formatHeatmapMetricValue(metric, detail.metricValue)}</strong>
            </div>
            <div className={styles.heatmapComparisonList}>
              {comparisons.map((item) => {
                const direction = item.delta > 0 ? 'above' : item.delta < 0 ? 'below' : 'even';
                const difference = detail.metricValue - item.average;
                const directionClass =
                  direction === 'even'
                    ? styles.heatmapComparisonEven
                    : metric === 'failureRate'
                      ? direction === 'above'
                        ? styles.heatmapComparisonRisk
                        : styles.heatmapComparisonGood
                      : metric === 'estimatedCost'
                        ? direction === 'above'
                          ? styles.heatmapComparisonWarn
                          : styles.heatmapComparisonGood
                        : direction === 'above'
                          ? styles.heatmapComparisonAbove
                          : styles.heatmapComparisonBelow;
                return (
                  <div
                    key={item.label}
                    className={`${styles.heatmapComparisonCard} ${directionClass}`}
                  >
                    <span className={styles.heatmapComparisonLabel}>{item.label}</span>
                    <div className={styles.heatmapComparisonValues}>
                      <strong>
                        {t('usage_analytics.heatmap_compare_average_value', {
                          value: formatHeatmapMetricValue(metric, item.average),
                        })}
                      </strong>
                      <b>{formatHeatmapMetricDifference(metric, difference)}</b>
                    </div>
                    <em>
                      {t(`usage_analytics.heatmap_compare_${direction}`)} {formatDelta(item.delta)}
                    </em>
                  </div>
                );
              })}
            </div>
          </div>
        </div>
        <div className={styles.heatmapContributorSection}>
          <h3>{t('usage_analytics.heatmap_contributors_title')}</h3>
          <div className={styles.heatmapContributorGrid}>
            {contributorGroups.map((group) => (
              <HeatmapContributorGroup
                key={group.kind}
                emptyLabel={t('usage_analytics.heatmap_contributor_empty')}
                kind={group.kind}
                rows={group.rows}
                title={group.title}
              />
            ))}
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className={`${styles.panel} ${styles.heatmapDetailPanel}`}>
      <div className={styles.heatmapDetailHero}>
        <div>
          <span>{t('usage_analytics.heatmap_detail_title')}</span>
          <h2>{selectedWindowLabel}</h2>
          <p>
            {t('usage_analytics.heatmap_detail_summary_meta', {
              count: dateCount,
              timeZone,
            })}
          </p>
        </div>
        <div className={styles.heatmapDetailHeroActions}>
          {detail ? (
            <div className={styles.heatmapRankBadge}>
              <span>{t('usage_analytics.heatmap_rank')}</span>
              <strong>
                {detail.rank}/{detail.totalCells}
              </strong>
            </div>
          ) : null}
          <button type="button" onClick={onClear}>
            {t('usage_analytics.heatmap_clear_selection')}
          </button>
        </div>
      </div>
      <HeatmapDateTabs
        dateOptions={dateOptions}
        selectedKey={selectedDateKey}
        onSelect={onSelectDate}
      />
      {content}
    </div>
  );
}

function HeatmapHighlightGroup({
  emptyLabel,
  onSelect,
  rows,
  selectedCell,
  title,
  tone,
}: {
  emptyLabel: string;
  onSelect: (cell: UsageHeatmapCellSelection) => void;
  rows: UsageHeatmapHighlight[];
  selectedCell: UsageHeatmapCellSelection | null;
  title: string;
  tone: 'request' | 'cost' | 'failure';
}) {
  const { t } = useTranslation();
  const weekdays = weekdayLabelKeys.map((key) => t(key));
  const toneClass = {
    cost: styles.heatmapHighlightCardCost,
    failure: styles.heatmapHighlightCardFailure,
    request: styles.heatmapHighlightCardRequest,
  }[tone];
  return (
    <div className={styles.heatmapHighlightGroup}>
      <h3>{title}</h3>
      {rows.length === 0 ? (
        <span className={styles.shortcutEmpty}>{emptyLabel}</span>
      ) : (
        rows.map((row, index) => {
          const selected =
            selectedCell?.weekday === row.point.weekday && selectedCell.hour === row.point.hour;
          return (
            <button
              key={row.id}
              type="button"
              className={`${styles.heatmapHighlightCard} ${toneClass} ${
                selected ? styles.heatmapHighlightCardSelected : ''
              }`}
              onClick={() => onSelect({ weekday: row.point.weekday, hour: row.point.hour })}
            >
              <span className={styles.heatmapHighlightRank}>#{index + 1}</span>
              <span className={styles.heatmapHighlightMain}>
                <strong>{formatHeatmapWindowLabel(row.point, weekdays)}</strong>
                <em>
                  {t('usage_analytics.heatmap_highlight_meta', {
                    requests: compactNumber(row.point.requestCount),
                    failures: compactNumber(row.point.failureCount),
                  })}
                </em>
              </span>
              <b>{formatHeatmapMetricValue(row.metric, row.value)}</b>
            </button>
          );
        })
      )}
    </div>
  );
}

function HeatmapHighlightsPanel({
  highlights,
  onSelect,
  selectedCell,
  timeZone,
}: {
  highlights: UsageHeatmapHighlights;
  onSelect: (cell: UsageHeatmapCellSelection) => void;
  selectedCell: UsageHeatmapCellSelection | null;
  timeZone: string;
}) {
  const { t } = useTranslation();
  return (
    <div className={styles.panel}>
      <div className={styles.panelHeader}>
        <div>
          <h2>{t('usage_analytics.heatmap_focus_title')}</h2>
          <p>{t('usage_analytics.heatmap_focus_hint', { timeZone })}</p>
        </div>
      </div>
      <div className={styles.heatmapHighlightGrid}>
        <HeatmapHighlightGroup
          title={t('usage_analytics.heatmap_peak_requests')}
          rows={highlights.requestPeaks}
          emptyLabel={t('usage_analytics.empty_title')}
          onSelect={onSelect}
          selectedCell={selectedCell}
          tone="request"
        />
        <HeatmapHighlightGroup
          title={t('usage_analytics.heatmap_peak_cost')}
          rows={highlights.costPeaks}
          emptyLabel={t('usage_analytics.empty_title')}
          onSelect={onSelect}
          selectedCell={selectedCell}
          tone="cost"
        />
        <HeatmapHighlightGroup
          title={t('usage_analytics.heatmap_peak_failure')}
          rows={highlights.failureRisks}
          emptyLabel={t('usage_analytics.heatmap_failure_sample_empty')}
          onSelect={onSelect}
          selectedCell={selectedCell}
          tone="failure"
        />
      </div>
    </div>
  );
}

function TokenStructureChart({ timeline }: { timeline: UsageTimelinePoint[] }) {
  const { t } = useTranslation();
  const chartTheme = useUsageChartTheme();
  const tokenBarItemStyle = useMemo(
    () => ({
      borderColor: chartTheme.surface.pieBorder,
      borderWidth: 1,
    }),
    [chartTheme]
  );
  const option = useMemo<TokenStructureChartOption>(
    () => ({
      animationDuration: 260,
      backgroundColor: 'transparent',
      color: chartTheme.tokenStructureColors,
      grid: { bottom: 34, containLabel: true, left: 8, right: 18, top: 18 },
      legend: {
        bottom: 0,
        icon: 'circle',
        itemHeight: 8,
        itemWidth: 8,
        textStyle: { color: chartTheme.surface.axisLabel, fontSize: 12, fontWeight: 700 },
      },
      tooltip: {
        appendToBody: true,
        axisPointer: { type: 'shadow' },
        ...getTooltipOption(chartTheme),
        borderRadius: 10,
        borderWidth: 1,
        confine: true,
        formatter: (params: unknown) => {
          const items = Array.isArray(params) ? params : [params];
          const first = items[0] as { dataIndex?: number } | undefined;
          const point =
            typeof first?.dataIndex === 'number' ? timeline[first.dataIndex] : undefined;
          const rows = items
            .map((item) => {
              const entry = item as { marker?: string; seriesName?: string; data?: number };
              return tooltipRowHtml(
                chartTheme,
                `${entry.marker ?? ''}${escapeHtml(entry.seriesName)}`,
                escapeHtml(compactNumber(Number(entry.data ?? 0)))
              );
            })
            .join('');
          return tooltipHtml(chartTheme, rows, escapeHtml(point?.label));
        },
        padding: 0,
        trigger: 'axis',
      },
      xAxis: {
        axisLabel: {
          color: chartTheme.surface.axisLabel,
          fontSize: 11,
          fontWeight: 700,
          hideOverlap: true,
        },
        axisLine: { lineStyle: { color: chartTheme.surface.axisLine } },
        axisTick: { show: false },
        data: timeline.map((point) => point.label),
        type: 'category',
      },
      yAxis: {
        axisLabel: { color: chartTheme.surface.axisLabel, formatter: compactNumber },
        splitLine: { lineStyle: { color: chartTheme.surface.splitLine, type: 'dashed' } },
        type: 'value',
      },
      series: [
        {
          barMaxWidth: 22,
          data: timeline.map((point) => point.inputTokens),
          itemStyle: tokenBarItemStyle,
          name: t('usage_analytics.metric_input_tokens'),
          stack: 'tokens',
          type: 'bar',
        },
        {
          barMaxWidth: 22,
          data: timeline.map((point) => point.outputTokens),
          itemStyle: tokenBarItemStyle,
          name: t('usage_analytics.metric_output_tokens'),
          stack: 'tokens',
          type: 'bar',
        },
        {
          barMaxWidth: 22,
          data: timeline.map((point) => getUsageCacheTokens(point)),
          itemStyle: tokenBarItemStyle,
          name: t('usage_analytics.metric_cached_tokens'),
          stack: 'tokens',
          type: 'bar',
        },
        {
          barMaxWidth: 22,
          data: timeline.map((point) => point.reasoningTokens),
          itemStyle: tokenBarItemStyle,
          name: t('usage_analytics.metric_reasoning_tokens'),
          stack: 'tokens',
          type: 'bar',
        },
      ],
    }),
    [chartTheme, t, timeline, tokenBarItemStyle]
  );

  if (timeline.length === 0) {
    return (
      <div className={styles.chartEmptyInline}>
        <IconInbox size={24} />
        <span>{t('usage_analytics.empty_title')}</span>
      </div>
    );
  }

  return (
    <EChartsView
      option={option}
      className={styles.echartsCanvas}
      style={{ height: 260 }}
      ariaLabel={t('usage_analytics.token_structure_title')}
    />
  );
}

function EntityTrendChart({
  metric,
  series,
  highlightId,
  loading = false,
  error = '',
}: {
  metric: UsageTrendMetricKey;
  series: UsageEntityTrendSeries[];
  highlightId?: string;
  loading?: boolean;
  error?: string;
}) {
  const { t } = useTranslation();
  const chartTheme = useUsageChartTheme();
  const hasHighlight = Boolean(highlightId && series.some((item) => item.id === highlightId));
  const option = useMemo<EntityTrendChartOption>(
    () => ({
      animationDuration: 260,
      backgroundColor: 'transparent',
      color: series.map(
        (_, index) => chartTheme.categoryPalette[index % chartTheme.categoryPalette.length]
      ),
      grid: { bottom: 34, containLabel: true, left: 8, right: 18, top: 18 },
      legend: {
        bottom: 0,
        icon: 'circle',
        itemHeight: 8,
        itemWidth: 8,
        textStyle: { color: chartTheme.surface.axisLabel, fontSize: 12, fontWeight: 700 },
      },
      tooltip: {
        appendToBody: true,
        axisPointer: { type: 'line' },
        ...getTooltipOption(chartTheme),
        borderRadius: 10,
        borderWidth: 1,
        confine: true,
        formatter: (params: unknown) => {
          const items = Array.isArray(params) ? params : [params];
          const rows = items
            .map((item) => {
              const entry = item as { marker?: string; seriesName?: string; data?: number };
              return tooltipRowHtml(
                chartTheme,
                `${entry.marker ?? ''}${escapeHtml(entry.seriesName)}`,
                escapeHtml(formatTrendMetricValue(metric, Number(entry.data ?? 0)))
              );
            })
            .join('');
          return tooltipHtml(chartTheme, rows);
        },
        padding: 0,
        trigger: 'axis',
      },
      xAxis: {
        axisLabel: {
          color: chartTheme.surface.axisLabel,
          fontSize: 11,
          fontWeight: 700,
          hideOverlap: true,
        },
        axisLine: { lineStyle: { color: chartTheme.surface.axisLine } },
        axisTick: { show: false },
        boundaryGap: false,
        data: series[0]?.points.map((point) => point.label) ?? [],
        type: 'category',
      },
      yAxis: {
        axisLabel: {
          color: chartTheme.surface.axisLabel,
          formatter: (value: number) => formatTrendMetricValue(metric, value),
        },
        splitLine: { lineStyle: { color: chartTheme.surface.splitLine, type: 'dashed' } },
        type: 'value',
      },
      series: series.map((item, index) => {
        const highlighted = hasHighlight && item.id === highlightId;
        return {
          data: item.points.map((point) => point.value),
          lineStyle: {
            color: chartTheme.categoryPalette[index % chartTheme.categoryPalette.length],
            opacity: hasHighlight && !highlighted ? 0.28 : 1,
            width: highlighted ? 3.4 : 2.3,
          },
          name: item.label,
          showSymbol: item.points.length <= 36,
          smooth: 0.25,
          type: 'line',
        };
      }),
    }),
    [chartTheme, hasHighlight, highlightId, metric, series]
  );

  if (loading) {
    return (
      <div className={styles.chartEmptyInline}>
        <IconRefreshCw size={24} />
        <span>{t('common.loading')}</span>
      </div>
    );
  }

  if (error) {
    return (
      <div className={styles.chartEmptyInline}>
        <IconX size={24} />
        <span>{t('usage_analytics.error_title')}</span>
        <span>{error}</span>
      </div>
    );
  }

  if (series.length === 0) {
    return (
      <div className={styles.chartEmptyInline}>
        <IconInbox size={24} />
        <span>{t('usage_analytics.empty_title')}</span>
      </div>
    );
  }

  return (
    <EChartsView
      option={option}
      className={styles.echartsCanvas}
      style={{ height: 260 }}
      ariaLabel={t('usage_analytics.entity_trend_title')}
    />
  );
}

function KeyAnomalyTable({
  locale,
  rows,
  onOpen,
  type = 'apiKey',
}: {
  locale: string;
  rows: UsageKeyAnomalyRow[];
  onOpen?: (row: UsageKeyAnomalyRow) => void;
  type?: 'apiKey' | 'credential';
}) {
  const { t } = useTranslation();
  return (
    <div className={styles.tableWrap}>
      <table className={styles.compactTable}>
        <thead>
          <tr>
            <th>
              {t(
                type === 'credential'
                  ? 'usage_analytics.col_credential'
                  : 'usage_analytics.col_api_key'
              )}
            </th>
            <th>{t('usage_analytics.col_reason')}</th>
            <th>{t('usage_analytics.col_severity')}</th>
            <th>{t('usage_analytics.col_triggered_at')}</th>
            <th>{t('usage_analytics.metric_estimated_cost')}</th>
            <th>{t('usage_analytics.failure_rate')}</th>
            {onOpen ? <th>{t('usage_analytics.col_action')}</th> : null}
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? (
            <tr>
              <td colSpan={onOpen ? 7 : 6}>{t('usage_analytics.anomaly_none')}</td>
            </tr>
          ) : (
            rows.slice(0, 8).map((row) => (
              <tr key={row.id}>
                <td>{type === 'credential' ? row.label : getApiKeyRowDisplayLabel(row.row)}</td>
                <td>{t(row.reasonKey)}</td>
                <td>
                  <span className={`${styles.severityBadge} ${styles[`severity${row.severity}`]}`}>
                    {t(`usage_analytics.severity_${row.severity}`)}
                  </span>
                </td>
                <td>{row.triggeredAtMs ? formatLocalDateTime(row.triggeredAtMs, locale) : '-'}</td>
                <td>{formatMetricValue('estimatedCost', row.row.estimatedCost)}</td>
                <td>{formatPercent(row.row.failureCount / Math.max(row.row.requestCount, 1))}</td>
                {onOpen ? (
                  <td>
                    <button type="button" className={styles.linkButton} onClick={() => onOpen(row)}>
                      {t('usage_analytics.view_request_details')}
                    </button>
                  </td>
                ) : null}
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}

function ApiKeyContextTable({ locale, rows }: { locale: string; rows: UsageApiKeyContextRow[] }) {
  const { t } = useTranslation();
  return (
    <div className={styles.contextSection}>
      <div className={styles.panelSubHeader}>
        <h3>{t('usage_analytics.api_key_context_title')}</h3>
      </div>
      {rows.length === 0 ? (
        <div className={styles.inlineEmpty}>
          <IconInbox size={22} />
          <span>{t('usage_analytics.api_key_context_empty')}</span>
        </div>
      ) : (
        <div className={styles.tableWrap}>
          <table className={styles.apiKeyContextTable}>
            <thead>
              <tr>
                <th>{t('usage_analytics.credential_identity_provider')}</th>
                <th>{t('usage_analytics.credential_identity_account')}</th>
                <th>{t('usage_analytics.credential_identity_auth_index')}</th>
                <th>{t('usage_analytics.api_key_context_source')}</th>
                <th>{t('usage_analytics.api_key_context_source_hash')}</th>
                <th>{t('usage_analytics.metric_request_count')}</th>
                <th>{t('usage_analytics.metric_estimated_cost')}</th>
                <th>{t('usage_analytics.failure_rate')}</th>
                <th>{t('usage_analytics.api_key_context_last_seen')}</th>
              </tr>
            </thead>
            <tbody>
              {rows.slice(0, 8).map((row) => (
                <tr key={row.id}>
                  <td>{row.provider || '-'}</td>
                  <td title={row.account || '-'}>{row.account || '-'}</td>
                  <td>{row.authIndex || '-'}</td>
                  <td title={row.source || '-'}>{row.source || '-'}</td>
                  <td className={styles.monoCell} title={row.sourceHash || '-'}>
                    {row.sourceHash || '-'}
                  </td>
                  <td>{compactNumber(row.requestCount)}</td>
                  <td>{formatMetricValue('estimatedCost', row.estimatedCost)}</td>
                  <td
                    className={
                      row.requestCount > 0 &&
                      row.failureRate > 1 - USAGE_SUCCESS_RATE_WATCH_THRESHOLD
                        ? styles.tonebad
                        : ''
                    }
                  >
                    {formatPercent(row.failureRate)}
                  </td>
                  <td>{row.lastSeenMs ? formatLocalDateTime(row.lastSeenMs, locale) : '-'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function ProviderOverviewPanel({
  rows,
  onOpen,
}: {
  rows: UsageProviderRow[];
  onOpen: (row: UsageProviderRow) => void;
}) {
  const { t } = useTranslation();
  const visibleRows = useMemo(
    () =>
      [...rows]
        .sort(
          (left, right) =>
            right.estimatedCost - left.estimatedCost ||
            right.requestCount - left.requestCount ||
            left.label.localeCompare(right.label)
        )
        .slice(0, 8),
    [rows]
  );

  return (
    <div className={styles.tableWrap}>
      {visibleRows.length === 0 ? (
        <div className={styles.inlineEmpty}>
          <IconInbox size={22} />
          <span>{t('usage_analytics.empty_title')}</span>
        </div>
      ) : (
        <table className={`${styles.compactTable} ${styles.providerOverviewTable}`}>
          <thead>
            <tr>
              <th>{t('usage_analytics.credential_identity_provider')}</th>
              <th>{t('usage_analytics.metric_request_count')}</th>
              <th>{t('usage_analytics.provider_request_share')}</th>
              <th>{t('usage_analytics.metric_estimated_cost')}</th>
              <th>{t('usage_analytics.provider_cost_share')}</th>
              <th>{t('usage_analytics.metric_total_tokens')}</th>
              <th>{t('usage_analytics.success_rate')}</th>
              <th>{t('usage_analytics.cache_read_rate')}</th>
              <th>{t('usage_analytics.provider_top_model')}</th>
              <th>{t('usage_analytics.col_action')}</th>
            </tr>
          </thead>
          <tbody>
            {visibleRows.map((row) => {
              const topModel = row.models[0];
              return (
                <tr key={row.id}>
                  <td>
                    <strong>{row.label}</strong>
                  </td>
                  <td>{formatMetricValue('requestCount', row.requestCount)}</td>
                  <td>{formatPercent(row.requestShare)}</td>
                  <td>{formatMetricValue('estimatedCost', row.estimatedCost)}</td>
                  <td>{formatPercent(row.costShare)}</td>
                  <td>{formatMetricValue('totalTokens', row.totalTokens)}</td>
                  <td
                    className={
                      row.requestCount > 0 && row.successRate < USAGE_SUCCESS_RATE_WATCH_THRESHOLD
                        ? styles.tonebad
                        : ''
                    }
                  >
                    {formatPercent(row.successRate)}
                  </td>
                  <td>{formatPercent(row.cacheRate)}</td>
                  <td title={topModel?.label ?? '-'}>
                    {topModel
                      ? `${topModel.label} · ${formatMetricValue(
                          'estimatedCost',
                          topModel.estimatedCost
                        )}`
                      : '-'}
                  </td>
                  <td>
                    <button type="button" className={styles.linkButton} onClick={() => onOpen(row)}>
                      {t('usage_analytics.view_request_details')}
                    </button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </div>
  );
}

function EmptyState({ title, body }: { title: string; body: string }) {
  return (
    <div className={styles.emptyState}>
      <IconInbox size={28} />
      <strong>{title}</strong>
      <span>{body}</span>
    </div>
  );
}

function UsageAnalyticsPageInner() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const usage = useUsageAnalytics();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const chartTheme = useUsageChartTheme();
  const themedUsageMetrics = useMemo(() => getThemedUsageMetrics(chartTheme), [chartTheme]);
  const [selectedMetrics, setSelectedMetrics] =
    useState<UsageMetricKey[]>(DEFAULT_SELECTED_METRICS);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [showAllModels, setShowAllModels] = useState(false);
  const [credentialWarningsForSelectionOnly, setCredentialWarningsForSelectionOnly] =
    useState(false);
  const [customRangeFallbackEndMs] = useState(() => Date.now());
  const customStartInputRef = useRef<HTMLInputElement | null>(null);
  const customEndInputRef = useRef<HTMLInputElement | null>(null);
  const [stableOptionCache, setStableOptionCache] = useState<StableUsageOptionCache>(() =>
    emptyStableOptionCache()
  );
  const customStartInputValue = formatDateTimeLocalValue(
    new Date(usage.filters.customRange?.startMs ?? customRangeFallbackEndMs - 24 * 60 * 60 * 1000)
  );
  const customEndInputValue = formatDateTimeLocalValue(
    new Date(usage.filters.customRange?.endMs ?? customRangeFallbackEndMs)
  );
  const customRangeInputKey = `${usage.filters.customRange?.startMs ?? 'default'}:${
    usage.filters.customRange?.endMs ?? 'default'
  }`;
  const allModelOptionLabel = t('monitoring.filter_all_models');
  const allApiKeyOptionLabel = t('monitoring.filter_all_api_keys');
  const allProviderOptionLabel = t('monitoring.filter_all_providers');
  const allStatusOptionLabel = t('monitoring.filter_all_statuses');
  const allAuthFileOptionLabel = t('usage_analytics.filter_all_auth_files');

  const incomingOptionCache = useMemo<StableUsageOptionCache>(() => {
    const apiKeys = mergeSelectOptions([
      ...(usage.filterOptions?.api_key_hashes ?? []).map((hash) => ({
        value: hash,
        label: resolveUsageApiKeyLabel(hash, usage.apiKeyDisplayMap),
      })),
      ...(usage.filterOptions?.api_key_stats ?? []).map((row) => {
        const hash = row.api_key_hash || row.id;
        return {
          value: hash,
          label: resolveUsageApiKeyLabel(hash, usage.apiKeyDisplayMap),
        };
      }),
      ...usage.apiKeyRows.map((row) => {
        const hash = row.apiKeyHash || row.id;
        return { value: hash, label: row.label };
      }),
    ]);

    return {
      models: buildOptionValues([
        ...(usage.filterOptions?.models ?? []),
        ...(usage.filterOptions?.model_stats ?? []).map((row) => row.model),
        ...usage.modelRows.map((row) => row.model || row.label),
      ]),
      providers: buildOptionValues([
        ...(usage.filterOptions?.providers ?? []),
        ...usage.modelRows.map((row) => row.provider),
        ...usage.apiKeyRows.map((row) => row.provider),
        ...usage.credentialRows.map((row) => row.provider),
      ]),
      authFiles: buildOptionValues([
        ...(usage.filterOptions?.auth_files ?? []),
        ...usage.credentialRows.map((row) => row.authFile),
      ]),
      apiKeys,
    };
  }, [
    usage.apiKeyDisplayMap,
    usage.apiKeyRows,
    usage.credentialRows,
    usage.filterOptions?.api_key_hashes,
    usage.filterOptions?.api_key_stats,
    usage.filterOptions?.auth_files,
    usage.filterOptions?.models,
    usage.filterOptions?.model_stats,
    usage.filterOptions?.providers,
    usage.modelRows,
  ]);

  const displayOptionCache = useMemo(
    () => mergeStableOptionCache(stableOptionCache, incomingOptionCache),
    [incomingOptionCache, stableOptionCache]
  );

  const rememberVisibleOptions = () => {
    setStableOptionCache((current) => {
      const next = mergeStableOptionCache(current, incomingOptionCache);
      return stableOptionCachesEqual(current, next) ? current : next;
    });
  };

  const updateFilters = (patch: Partial<typeof usage.filters>) => {
    rememberVisibleOptions();
    usage.setFilters(patch);
  };
  const handleCopyApiKey = useCallback(
    async (copyValue: string) => {
      const copied = await copyToClipboard(copyValue);
      showNotification(
        t(copied ? 'notification.link_copied' : 'notification.copy_failed'),
        copied ? 'success' : 'error'
      );
    },
    [showNotification, t]
  );
  const buildApiKeyMonitoringUrl = (apiKeyHash: string, status?: UsageAnalyticsStatus) =>
    usage.bounds
      ? buildMonitoringDetailUrl(
          { bucketMs: usage.bounds.fromMs, bucketEndMs: usage.bounds.toMs },
          {
            ...usage.filters,
            apiKeyHash,
            status: status ?? usage.filters.status,
          }
        )
      : `/monitoring?api_key_hash=${encodeURIComponent(apiKeyHash)}`;
  const buildCredentialMonitoringUrl = (
    row: Pick<UsageRankRow, 'authFile' | 'projectId'> | null | undefined,
    status?: UsageAnalyticsStatus
  ) =>
    usage.bounds
      ? buildMonitoringDetailUrl(
          { bucketMs: usage.bounds.fromMs, bucketEndMs: usage.bounds.toMs },
          {
            ...usage.filters,
            authFile: row?.authFile || usage.filters.authFile,
            projectId: row?.projectId || undefined,
            status: status ?? usage.filters.status,
          }
        )
      : `/monitoring?auth_file=${encodeURIComponent(row?.authFile || '')}&project_id=${encodeURIComponent(
          row?.projectId || ''
        )}`;
  const openApiKeyCombinationHeatmap = () => {
    const apiKeyHash = usage.selectedApiKey?.apiKeyHash || usage.selectedApiKey?.id || '';
    if (apiKeyHash) {
      updateFilters({ apiKeyHash });
    }
    usage.setActiveTab('heatmap');
  };

  const usageTabItems = useMemo<ReadonlyArray<SegmentedTabItem<UsageAnalyticsTab>>>(
    () =>
      USAGE_ANALYTICS_TABS.map((tab) => ({
        id: tab,
        label: t(`usage_analytics.tab_${tab}`),
      })),
    [t]
  );

  const modelOptions = useMemo<SelectOption[]>(
    () =>
      buildStableSelectOptions(allModelOptionLabel, displayOptionCache.models, usage.filters.model),
    [allModelOptionLabel, displayOptionCache.models, usage.filters.model]
  );
  const apiKeyOptions = useMemo<SelectOption[]>(
    () => [
      { value: 'all', label: allApiKeyOptionLabel },
      ...mergeSelectOptions(
        [
          ...displayOptionCache.apiKeys,
          usage.filters.apiKeyHash !== 'all'
            ? {
                value: usage.filters.apiKeyHash,
                label: resolveUsageApiKeyLabel(usage.filters.apiKeyHash, usage.apiKeyDisplayMap),
              }
            : null,
        ].filter((option): option is SelectOption => Boolean(option?.value))
      ),
    ],
    [
      allApiKeyOptionLabel,
      displayOptionCache.apiKeys,
      usage.apiKeyDisplayMap,
      usage.filters.apiKeyHash,
    ]
  );
  const providerOptions = useMemo<SelectOption[]>(
    () =>
      buildStableSelectOptions(
        allProviderOptionLabel,
        displayOptionCache.providers,
        usage.filters.provider
      ),
    [allProviderOptionLabel, displayOptionCache.providers, usage.filters.provider]
  );
  const authFileOptions = useMemo<SelectOption[]>(
    () =>
      buildStableSelectOptions(
        allAuthFileOptionLabel,
        displayOptionCache.authFiles,
        usage.filters.authFile
      ),
    [allAuthFileOptionLabel, displayOptionCache.authFiles, usage.filters.authFile]
  );
  const statusOptions: SelectOption[] = [
    { value: 'all', label: allStatusOptionLabel },
    { value: 'success', label: t('usage_analytics.status_success') },
    { value: 'failed', label: t('usage_analytics.status_failed') },
  ];
  const latencyOptions: SelectOption[] = [
    { value: 'all', label: t('usage_analytics.latency_all') },
    { value: '3000', label: t('usage_analytics.latency_over_3000') },
    { value: '10000', label: t('usage_analytics.latency_over_10000') },
    { value: '30000', label: t('usage_analytics.latency_over_30000') },
  ];
  const cacheStatusOptions: SelectOption[] = [
    { value: 'all', label: t('usage_analytics.cache_status_all') },
    { value: 'hit', label: t('usage_analytics.cache_status_hit') },
    { value: 'miss', label: t('usage_analytics.cache_status_miss') },
  ];
  const noData = !usage.loading && !usage.error && !hasUsageData(usage.summary, usage.timeline);
  const rankRowLimit = 8;
  const credentialRankRowLimit = 10;
  const visibleModelRows = showAllModels ? usage.modelRows : usage.modelRows.slice(0, rankRowLimit);
  const visibleApiKeyRows = usage.apiKeyRows.slice(0, 8);
  const visibleCredentialRows = usage.credentialRows.slice(0, credentialRankRowLimit);
  const selectedModelKeyDistribution = useMemo(
    () =>
      usage.selectedModel
        ? buildModelKeyDistribution(usage.selectedModel.id, usage.apiKeyRows)
        : [],
    [usage.apiKeyRows, usage.selectedModel]
  );
  const abnormalCredentialCount = usage.credentialAnomalies.length;
  const visibleCredentialAnomalies = useMemo(
    () =>
      credentialWarningsForSelectionOnly && usage.selectedCredential
        ? usage.credentialAnomalies.filter((row) => row.id === usage.selectedCredential?.id)
        : usage.credentialAnomalies,
    [credentialWarningsForSelectionOnly, usage.credentialAnomalies, usage.selectedCredential]
  );
  const highRiskCredentialCount = useMemo(
    () => usage.credentialAnomalies.filter((row) => row.severity === 'high').length,
    [usage.credentialAnomalies]
  );
  const lowestSuccessCredential = useMemo(
    () =>
      usage.credentialRows.reduce<UsageRankRow | null>(
        (current, row) =>
          row.requestCount > 0 && (!current || row.successRate < current.successRate)
            ? row
            : current,
        null
      ),
    [usage.credentialRows]
  );
  const anomalyUrl = usage.anomalyAnalysis
    ? buildMonitoringDetailUrl(usage.anomalyAnalysis.point, usage.filters)
    : '';
  const overviewReasoningTokens = useMemo(
    () => usage.timeline.reduce((sum, point) => sum + point.reasoningTokens, 0),
    [usage.timeline]
  );

  const overviewAnomalySummary = useMemo(
    () => summarizeAnomalies(usage.anomalyPoints, { minRequests: 10, limit: 5 }),
    [usage.anomalyPoints]
  );
  const overviewSummaryCards = useMemo(
    () =>
      buildUsageOverviewSummaryCards({
        anomalyCount: usage.anomalyPoints.length,
        locale: i18n.language,
        reasoningTokens: overviewReasoningTokens,
        summary: usage.summary,
        summaryDelta: usage.summaryDelta,
        t,
      }),
    [
      i18n.language,
      overviewReasoningTokens,
      t,
      usage.anomalyPoints.length,
      usage.summary,
      usage.summaryDelta,
    ]
  );
  const trendSummaryCards = useMemo(
    () =>
      buildUsageTrendSummaryCards({
        locale: i18n.language,
        summaryDelta: usage.summaryDelta,
        timeline: usage.timeline,
        t,
      }),
    [i18n.language, t, usage.summaryDelta, usage.timeline]
  );
  const modelSummaryCards = useMemo(
    () =>
      buildUsageModelSummaryCards({
        locale: i18n.language,
        modelRows: usage.modelRows,
        summary: usage.summary,
        t,
      }),
    [i18n.language, t, usage.modelRows, usage.summary]
  );
  const apiKeySummaryCards = useMemo(
    () =>
      buildUsageApiKeySummaryCards({
        apiKeyRows: usage.apiKeyRows,
        keyAnomalyCount: usage.keyAnomalies.length,
        locale: i18n.language,
        summary: usage.summary,
        t,
      }),
    [i18n.language, t, usage.apiKeyRows, usage.keyAnomalies.length, usage.summary]
  );
  const credentialSummaryCards = useMemo(
    () =>
      buildUsageEntitySummaryCards({
        activeAccent: 'cyan',
        activeCount: usage.credentialRows.length,
        activeIcon: 'credential',
        activeLabel: t('usage_analytics.active_credentials'),
        activeMeta: t('usage_analytics.active_credential_hint', {
          active: usage.credentialRows.length,
          total: usage.allCredentialRows.length,
        }),
        anomalyCount: abnormalCredentialCount,
        anomalyLabel: t('usage_analytics.anomaly_credentials'),
        locale: i18n.language,
        summary: usage.summary,
        t,
      }),
    [
      abnormalCredentialCount,
      i18n.language,
      t,
      usage.allCredentialRows.length,
      usage.credentialRows.length,
      usage.summary,
    ]
  );
  const heatmapSummaryCards = useMemo(
    () =>
      buildUsageHeatmapSummaryCards({
        locale: i18n.language,
        summary: usage.summary,
        t,
      }),
    [i18n.language, t, usage.summary]
  );
  const toggleMetric = (key: UsageMetricKey) => {
    setSelectedMetrics((current) =>
      current.includes(key) ? current.filter((item) => item !== key) : [...current, key]
    );
  };

  const applyCustomRange = () => {
    const startMs = parseDateTimeLocalValue(
      customStartInputRef.current?.value ?? customStartInputValue
    );
    const endMs = parseDateTimeLocalValue(customEndInputRef.current?.value ?? customEndInputValue);
    if (startMs === null || endMs === null || startMs >= endMs) return;
    updateFilters({
      timeRange: 'custom',
      customRange: { startMs, endMs },
    });
  };
  const openProviderDetails = (row: UsageProviderRow) => {
    if (!usage.bounds) {
      navigate(`/monitoring?provider=${encodeURIComponent(row.id.toLowerCase())}`);
      return;
    }
    navigate(
      buildMonitoringDetailUrl(
        { bucketMs: usage.bounds.fromMs, bucketEndMs: usage.bounds.toMs },
        { ...usage.filters, provider: row.id }
      )
    );
  };

  return (
    <div className={styles.page}>
      <section className={styles.controlsPanel}>
        <div className={styles.controlsTabsRow}>
          <SegmentedTabs
            items={usageTabItems}
            activeTab={usage.activeTab}
            onChange={usage.setActiveTab}
            ariaLabel={t('usage_analytics.tabs_label')}
            idBase="usage-analytics-tab"
            className={styles.tabs}
          />
        </div>

        <div className={styles.controlsFilterSection}>
          <div className={styles.controlBar}>
            <div
              className={styles.segmentedControl}
              aria-label={t('usage_analytics.filter_time_range')}
            >
              {USAGE_TIME_RANGES.map((range) => (
                <button
                  key={range}
                  type="button"
                  className={`${styles.segmentButton} ${
                    usage.filters.timeRange === range ? styles.segmentButtonActive : ''
                  }`}
                  onClick={() => updateFilters({ timeRange: range })}
                >
                  {t(`usage_analytics.range_${range}`)}
                </button>
              ))}
            </div>

            <div
              className={styles.segmentedControl}
              aria-label={t('usage_analytics.filter_granularity')}
            >
              {(['auto', 'hour', 'day'] as UsageAnalyticsGranularity[]).map((granularity) => (
                <button
                  key={granularity}
                  type="button"
                  className={`${styles.segmentButton} ${
                    usage.filters.granularity === granularity ? styles.segmentButtonActive : ''
                  }`}
                  onClick={() => updateFilters({ granularity })}
                >
                  {t(`usage_analytics.granularity_${granularity}`)}
                </button>
              ))}
            </div>

            <div className={styles.refreshControls}>
              <span className={styles.filterMeta}>
                {t('usage_analytics.resolved_granularity', {
                  granularity: usage.resolvedGranularity,
                })}
              </span>
              <button
                type="button"
                className={styles.filterActionButton}
                onClick={usage.resetFilters}
              >
                {t('usage_analytics.clear_all')}
              </button>
              <button
                type="button"
                className={styles.filterActionButton}
                onClick={() => setAdvancedOpen((open) => !open)}
              >
                {advancedOpen
                  ? t('usage_analytics.hide_advanced_filters')
                  : t('usage_analytics.show_advanced_filters')}
              </button>
              <Button
                variant="secondary"
                size="sm"
                onClick={usage.refresh}
                disabled={usage.loading}
              >
                <IconRefreshCw size={15} />
                {t('common.refresh')}
              </Button>
            </div>
          </div>

          <div className={styles.filterBar}>
            <div className={styles.scopeSearchBar}>
              <IconSearch size={16} />
              <input
                value={usage.filters.searchQuery}
                onChange={(event) => updateFilters({ searchQuery: event.target.value })}
                aria-label={t('usage_analytics.filter_search')}
                placeholder={t('usage_analytics.filter_search_placeholder')}
              />
              {usage.filters.searchQuery.trim() ? (
                <button
                  type="button"
                  className={styles.scopeSearchClear}
                  onClick={() => updateFilters({ searchQuery: '' })}
                  aria-label={t('usage_analytics.filter_search_clear')}
                >
                  <IconX size={14} />
                </button>
              ) : null}
            </div>
            <div className={styles.filterGrid}>
              <Select
                value={usage.filters.model}
                options={modelOptions}
                onChange={(model) => updateFilters({ model })}
                ariaLabel={t('usage_analytics.filter_model')}
                triggerClassName={styles.filterSelectTrigger}
              />
              <Select
                value={usage.filters.apiKeyHash}
                options={apiKeyOptions}
                onChange={(apiKeyHash) => updateFilters({ apiKeyHash })}
                ariaLabel={t('usage_analytics.filter_api_key')}
                triggerClassName={styles.filterSelectTrigger}
              />
              <Select
                value={usage.filters.provider}
                options={providerOptions}
                onChange={(provider) => updateFilters({ provider })}
                ariaLabel={t('usage_analytics.filter_provider')}
                triggerClassName={styles.filterSelectTrigger}
              />
              <Select
                value={usage.filters.status}
                options={statusOptions}
                onChange={(status) => updateFilters({ status: status as UsageAnalyticsStatus })}
                ariaLabel={t('usage_analytics.filter_status')}
                triggerClassName={styles.filterSelectTrigger}
              />
            </div>
          </div>

          {usage.filters.timeRange === 'custom' ? (
            <div className={styles.customRangeRow}>
              <input
                key={`start-${customRangeInputKey}`}
                ref={customStartInputRef}
                type="datetime-local"
                defaultValue={customStartInputValue}
                aria-label={t('usage_analytics.custom_start')}
              />
              <input
                key={`end-${customRangeInputKey}`}
                ref={customEndInputRef}
                type="datetime-local"
                defaultValue={customEndInputValue}
                aria-label={t('usage_analytics.custom_end')}
              />
              <Button variant="secondary" size="sm" onClick={applyCustomRange}>
                {t('usage_analytics.apply_custom_range')}
              </Button>
            </div>
          ) : null}

          {advancedOpen ? (
            <div className={styles.advancedPanel}>
              <div className={styles.advancedGrid}>
                <label className={styles.filterGroup}>
                  <Select
                    value={usage.filters.authFile}
                    options={authFileOptions}
                    onChange={(authFile) => updateFilters({ authFile })}
                    ariaLabel={t('usage_analytics.filter_auth_file')}
                  />
                </label>
                <label className={styles.filterGroup}>
                  <Select
                    value={usage.filters.minLatencyMs}
                    options={latencyOptions}
                    onChange={(minLatencyMs) =>
                      updateFilters({
                        minLatencyMs: minLatencyMs as UsageAnalyticsLatencyFilter,
                      })
                    }
                    ariaLabel={t('usage_analytics.filter_latency')}
                  />
                </label>
                <label className={styles.filterGroup}>
                  <Select
                    value={usage.filters.cacheStatus}
                    options={cacheStatusOptions}
                    onChange={(cacheStatus) =>
                      updateFilters({ cacheStatus: cacheStatus as UsageAnalyticsCacheStatus })
                    }
                    ariaLabel={t('usage_analytics.filter_cache_status')}
                  />
                </label>
              </div>
            </div>
          ) : null}
        </div>
      </section>

      {usage.error ? (
        <section className={styles.alertPanel}>
          <IconShield size={22} />
          <div>
            <strong>{t('usage_analytics.error_title')}</strong>
            <span>{usage.error}</span>
          </div>
        </section>
      ) : null}

      {noData ? (
        <EmptyState
          title={t('usage_analytics.empty_title')}
          body={t('usage_analytics.empty_body')}
        />
      ) : null}

      {usage.activeTab === 'overview' ? (
        <>
          <UsageSummarySection cards={overviewSummaryCards} />

          <section className={styles.chartPanel}>
            <div className={styles.panelHeader}>
              <div>
                <h2>{t('usage_analytics.overview_trend_title')}</h2>
                <p>{t('usage_analytics.overview_trend_hint')}</p>
              </div>
            </div>
            <UsageLineChart
              timeline={usage.timeline}
              selectedMetrics={DEFAULT_SELECTED_METRICS}
              selectedBucket={usage.selectedBucket}
              onSelectBucket={usage.selectBucket}
            />
          </section>

          <AnomalyPointsPanel
            rows={overviewAnomalySummary}
            onOpen={(row) => navigate(buildMonitoringDetailUrl(row, usage.filters))}
            onViewAll={() => usage.setActiveTab('trends')}
          />

          <section className={styles.overviewCards}>
            <OverviewCard
              title={t('usage_analytics.model_overview_title')}
              rows={usage.modelRows}
              onViewAll={() => usage.setActiveTab('models')}
            />
            <OverviewCard
              title={t('usage_analytics.api_key_overview_title')}
              rows={usage.apiKeyRows}
              onViewAll={() => usage.setActiveTab('apiKeys')}
            />
          </section>

          <section className={styles.tablePanel}>
            <div className={styles.panelHeader}>
              <div>
                <h2>{t('usage_analytics.provider_overview_title')}</h2>
                <p>{t('usage_analytics.provider_overview_hint')}</p>
              </div>
            </div>
            <ProviderOverviewPanel rows={usage.providerRows} onOpen={openProviderDetails} />
          </section>

          {usage.selectedBucket ? (
            <DrilldownPreviewPanel rows={usage.drilldownPreview} locale={i18n.language} />
          ) : null}
        </>
      ) : null}

      {usage.activeTab === 'trends' ? (
        <>
          <UsageSummarySection cards={trendSummaryCards} />

          <section className={styles.chartPanel}>
            <div className={styles.panelHeader}>
              <div>
                <h2>{t('usage_analytics.trend_title')}</h2>
                <p>{t('usage_analytics.trend_hint')}</p>
              </div>
            </div>
            <div className={styles.metricChips}>
              {themedUsageMetrics.map((metric) => (
                <button
                  key={metric.key}
                  type="button"
                  className={selectedMetrics.includes(metric.key) ? styles.metricChipActive : ''}
                  style={{ '--metric-color': metric.color } as CSSProperties}
                  onClick={() => toggleMetric(metric.key)}
                >
                  <span />
                  {t(metric.labelKey)}
                </button>
              ))}
            </div>
            <div className={styles.chartCanvas}>
              <UsageLineChart
                timeline={usage.timeline}
                selectedMetrics={selectedMetrics}
                selectedBucket={usage.selectedBucket}
                onSelectBucket={usage.selectBucket}
              />
            </div>
          </section>

          {usage.anomalyAnalysis ? (
            <section className={styles.anomalyPanel}>
              <div className={styles.anomalyMain}>
                <IconTrendingUp size={32} />
                <div>
                  <h2>{t('usage_analytics.anomaly_title')}</h2>
                  <p>{formatLocalDateTime(usage.anomalyAnalysis.point.bucketMs, i18n.language)}</p>
                  <div className={styles.anomalyTags}>
                    {usage.anomalyAnalysis.anomalies.length > 0 ? (
                      usage.anomalyAnalysis.anomalies.map((item) => (
                        <span key={item.key}>{t(item.labelKey)}</span>
                      ))
                    ) : (
                      <span>{t('usage_analytics.anomaly_none')}</span>
                    )}
                  </div>
                </div>
              </div>
              <div className={styles.anomalyMetrics}>
                {(['requestCount', 'totalTokens', 'estimatedCost'] as UsageMetricKey[]).map(
                  (key) => (
                    <div key={key}>
                      <span>{getMetricLabel(key, t)}</span>
                      <strong>{formatMetricValue(key, usage.anomalyAnalysis!.point[key])}</strong>
                      <em>{formatDelta(usage.anomalyAnalysis!.changes[key])}</em>
                    </div>
                  )
                )}
              </div>
              <div className={styles.possibleCauses}>
                <h3>{t('usage_analytics.possible_causes')}</h3>
                <ul>
                  {usage.anomalyAnalysis.causeKeys.map((causeKey) => (
                    <li key={causeKey}>{t(causeKey)}</li>
                  ))}
                </ul>
              </div>
              <Button onClick={() => navigate(anomalyUrl)}>
                {t('usage_analytics.view_monitoring_details')}
              </Button>
            </section>
          ) : null}

          {usage.selectedBucket ? (
            <DrilldownPreviewPanel rows={usage.drilldownPreview} locale={i18n.language} />
          ) : null}

          <section className={styles.dualChartGrid}>
            <div className={styles.panel}>
              <div className={styles.panelHeader}>
                <div>
                  <h2>{t('usage_analytics.health_trend_title')}</h2>
                </div>
              </div>
              <HealthTrendChart timeline={usage.timeline} />
            </div>
            <div className={styles.panel}>
              <div className={styles.panelHeader}>
                <div>
                  <h2>{t('usage_analytics.token_structure_title')}</h2>
                  <p>{t('usage_analytics.token_structure_hint')}</p>
                </div>
              </div>
              <TokenStructureChart timeline={usage.timeline} />
            </div>
          </section>

          <section className={styles.panel}>
            <div className={`${styles.panelHeader} ${styles.trendEntityHeader}`}>
              <h2>{t('usage_analytics.trend_entity_compare_title')}</h2>
              <div
                className={`${styles.segmentedControl} ${styles.trendMetricTabs}`}
                aria-label={t('usage_analytics.filter_metric')}
              >
                {trendMetricOptions.map((option) => (
                  <button
                    key={option.value}
                    type="button"
                    className={`${styles.segmentButton} ${
                      usage.trendMetric === option.value ? styles.segmentButtonActive : ''
                    }`}
                    onClick={() => usage.setTrendMetric(option.value)}
                    aria-pressed={usage.trendMetric === option.value}
                  >
                    {t(option.labelKey)}
                  </button>
                ))}
              </div>
            </div>
            <div className={styles.trendEntityGrid}>
              <div className={styles.trendEntityChart}>
                <h3>{t('usage_analytics.model_compare_title')}</h3>
                <EntityTrendChart series={usage.modelTrendSeries} metric={usage.trendMetric} />
              </div>
              <div className={styles.trendEntityChart}>
                <h3>{t('usage_analytics.api_key_compare_title')}</h3>
                <EntityTrendChart series={usage.apiKeyTrendSeries} metric={usage.trendMetric} />
              </div>
            </div>
          </section>

          <AnomalyPointsPanel
            rows={usage.anomalyPoints}
            onOpen={(row) => navigate(buildMonitoringDetailUrl(row, usage.filters))}
          />
        </>
      ) : null}

      {usage.activeTab === 'models' ? (
        <>
          <UsageSummarySection cards={modelSummaryCards} />
          <section className={styles.tablePanel}>
            <div className={styles.panelHeader}>
              <h2>{t('usage_analytics.model_rank_title')}</h2>
              {usage.modelRows.length > rankRowLimit ? (
                <button
                  type="button"
                  className={styles.filterActionButton}
                  onClick={() => setShowAllModels((open) => !open)}
                >
                  {showAllModels
                    ? t('usage_analytics.rank_collapse')
                    : t('usage_analytics.rank_show_all', { count: usage.modelRows.length })}
                </button>
              ) : null}
            </div>
            <RankTable
              rows={visibleModelRows}
              type="model"
              selectedId={usage.selectedModel?.id}
              onSelect={(row) => usage.setSelectedModelId(row.id)}
            />
          </section>
          <section className={styles.dualChartGrid}>
            <div className={styles.panel}>
              <div className={`${styles.panelHeader} ${styles.trendEntityHeader}`}>
                <div>
                  <h2>{t('usage_analytics.model_compare_title')}</h2>
                  <p>{t('usage_analytics.entity_trend_hint')}</p>
                </div>
                <div
                  className={`${styles.segmentedControl} ${styles.trendMetricTabs}`}
                  aria-label={t('usage_analytics.filter_metric')}
                >
                  {trendMetricOptions.map((option) => (
                    <button
                      key={option.value}
                      type="button"
                      className={`${styles.segmentButton} ${
                        usage.trendMetric === option.value ? styles.segmentButtonActive : ''
                      }`}
                      onClick={() => usage.setTrendMetric(option.value)}
                      aria-pressed={usage.trendMetric === option.value}
                    >
                      {t(option.labelKey)}
                    </button>
                  ))}
                </div>
              </div>
              <EntityTrendChart
                series={usage.modelTrendSeries}
                metric={usage.trendMetric}
                highlightId={usage.selectedModel?.id}
              />
            </div>
            <div className={styles.panel}>
              <h2>{t('usage_analytics.cost_share_title')}</h2>
              <CostShareChart rows={usage.modelRows} />
            </div>
          </section>
          {usage.selectedModel ? (
            <DetailPanel
              row={usage.selectedModel}
              type="model"
              keyDistribution={selectedModelKeyDistribution}
              action={
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => {
                    const model = usage.selectedModel?.label ?? '';
                    navigate(
                      usage.bounds
                        ? buildMonitoringDetailUrl(
                            { bucketMs: usage.bounds.fromMs, bucketEndMs: usage.bounds.toMs },
                            { ...usage.filters, model }
                          )
                        : `/monitoring?model=${encodeURIComponent(model)}`
                    );
                  }}
                >
                  <IconExternalLink size={14} />
                  {t('usage_analytics.view_request_details')}
                </Button>
              }
            />
          ) : null}
        </>
      ) : null}

      {usage.activeTab === 'apiKeys' ? (
        <>
          <UsageSummarySection cards={apiKeySummaryCards} />
          <section className={styles.apiKeyAnalysisGrid}>
            <div className={styles.tablePanel}>
              <div className={styles.panelHeader}>
                <h2>{t('usage_analytics.api_key_rank_title')}</h2>
                <div className={styles.apiSearchBar}>
                  <IconSearch size={16} />
                  <input
                    value={usage.filters.apiKeyKeyword}
                    onChange={(event) => updateFilters({ apiKeyKeyword: event.target.value })}
                    placeholder={t('usage_analytics.api_key_keyword_placeholder')}
                  />
                </div>
              </div>
              <RankTable
                rows={visibleApiKeyRows}
                type="apiKey"
                selectedId={usage.selectedApiKey?.apiKeyHash}
                onSelect={(row) => usage.setSelectedApiKeyHash(row.apiKeyHash || row.id)}
                onCopyApiKey={(row) => {
                  if (row.apiKeyCopyValue) void handleCopyApiKey(row.apiKeyCopyValue);
                }}
              />
            </div>
            <div className={styles.panel}>
              <div className={`${styles.panelHeader} ${styles.trendEntityHeader}`}>
                <h2>{t('usage_analytics.api_key_compare_title')}</h2>
                <div
                  className={`${styles.segmentedControl} ${styles.trendMetricTabs}`}
                  aria-label={t('usage_analytics.filter_metric')}
                >
                  {trendMetricOptions.map((option) => (
                    <button
                      key={option.value}
                      type="button"
                      className={`${styles.segmentButton} ${
                        usage.trendMetric === option.value ? styles.segmentButtonActive : ''
                      }`}
                      onClick={() => usage.setTrendMetric(option.value)}
                      aria-pressed={usage.trendMetric === option.value}
                    >
                      {t(option.labelKey)}
                    </button>
                  ))}
                </div>
              </div>
              <EntityTrendChart
                series={usage.selectedApiKeyTrendSeries}
                metric={usage.trendMetric}
                highlightId={usage.selectedApiKey?.apiKeyHash || usage.selectedApiKey?.id}
              />
            </div>
            {usage.selectedApiKey ? (
              <DetailPanel
                row={usage.selectedApiKey}
                type="apiKey"
                action={
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() =>
                      navigate(
                        buildApiKeyMonitoringUrl(
                          usage.selectedApiKey?.apiKeyHash || usage.selectedApiKey?.id || ''
                        )
                      )
                    }
                  >
                    <IconExternalLink size={14} />
                    {t('usage_analytics.view_request_details')}
                  </Button>
                }
              />
            ) : null}
            <div className={`${styles.warningPanel} ${styles.fullWidthPanel}`}>
              <div className={styles.panelHeader}>
                <h2>{t('usage_analytics.api_key_warning_title')}</h2>
                <button type="button" onClick={openApiKeyCombinationHeatmap}>
                  {t('usage_analytics.view_exception_combinations')}
                </button>
              </div>
              <KeyAnomalyTable
                rows={usage.keyAnomalies}
                locale={i18n.language}
                onOpen={(row) =>
                  navigate(
                    buildApiKeyMonitoringUrl(
                      row.row.apiKeyHash || row.id,
                      row.reasonKey === 'usage_analytics.anomaly_reason_error_rate'
                        ? 'failed'
                        : usage.filters.status
                    )
                  )
                }
              />
            </div>
          </section>
        </>
      ) : null}

      {usage.activeTab === 'credentials' ? (
        <>
          <UsageSummarySection cards={credentialSummaryCards} />
          <section className={styles.credentialAnalysisGrid}>
            <div className={styles.tablePanel}>
              <div className={styles.panelHeader}>
                <div>
                  <h2>{t('usage_analytics.credential_rank_title')}</h2>
                  <p>
                    {t('usage_analytics.active_credential_hint', {
                      active: usage.credentialRows.length,
                      total: usage.allCredentialRows.length,
                    })}
                  </p>
                </div>
              </div>
              <RankTable
                rows={visibleCredentialRows}
                type="credential"
                selectedId={usage.selectedCredential?.id}
                onSelect={(row) => usage.setSelectedCredentialId(row.id)}
              />
            </div>
            <div className={styles.panel}>
              <div className={`${styles.panelHeader} ${styles.trendEntityHeader}`}>
                <h2>{t('usage_analytics.selected_credential_trend_title')}</h2>
                <div
                  className={`${styles.segmentedControl} ${styles.trendMetricTabs}`}
                  aria-label={t('usage_analytics.filter_metric')}
                >
                  {trendMetricOptions.map((option) => (
                    <button
                      key={option.value}
                      type="button"
                      className={`${styles.segmentButton} ${
                        usage.trendMetric === option.value ? styles.segmentButtonActive : ''
                      }`}
                      onClick={() => usage.setTrendMetric(option.value)}
                      aria-pressed={usage.trendMetric === option.value}
                    >
                      {t(option.labelKey)}
                    </button>
                  ))}
                </div>
              </div>
              <EntityTrendChart
                series={usage.credentialTrendSeries}
                metric={usage.trendMetric}
                loading={usage.credentialTrendLoading}
                error={usage.credentialTrendError}
              />
            </div>
            {usage.selectedCredential ? (
              <DetailPanel
                className={`${styles.credentialDetailPanel} ${styles.fullWidthPanel}`}
                row={usage.selectedCredential}
                type="credential"
                action={
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => navigate(buildCredentialMonitoringUrl(usage.selectedCredential))}
                  >
                    <IconExternalLink size={14} />
                    {t('usage_analytics.view_request_details')}
                  </Button>
                }
              />
            ) : null}
            <div className={`${styles.warningPanel} ${styles.fullWidthPanel}`}>
              <div className={styles.panelHeader}>
                <div>
                  <h2>{t('usage_analytics.credential_warning_title')}</h2>
                  <p>
                    {t('usage_analytics.credential_warning_summary', {
                      count: abnormalCredentialCount,
                      high: highRiskCredentialCount,
                      lowest: lowestSuccessCredential
                        ? formatPercent(lowestSuccessCredential.successRate)
                        : '-',
                    })}
                  </p>
                </div>
                <label className={styles.toggleControl}>
                  <input
                    type="checkbox"
                    checked={credentialWarningsForSelectionOnly}
                    onChange={(event) =>
                      setCredentialWarningsForSelectionOnly(event.target.checked)
                    }
                    disabled={!usage.selectedCredential}
                  />
                  <span>{t('usage_analytics.credential_warning_selected_only')}</span>
                </label>
              </div>
              <KeyAnomalyTable
                rows={visibleCredentialAnomalies}
                locale={i18n.language}
                type="credential"
                onOpen={(row) =>
                  navigate(
                    buildCredentialMonitoringUrl(
                      row.row,
                      row.reasonKey === 'usage_analytics.anomaly_reason_error_rate'
                        ? 'failed'
                        : usage.filters.status
                    )
                  )
                }
              />
            </div>
          </section>
        </>
      ) : null}

      {usage.activeTab === 'heatmap' ? (
        <>
          <UsageSummarySection cards={heatmapSummaryCards} />
          <section className={styles.chartPanel}>
            <div className={`${styles.panelHeader} ${styles.heatmapPanelHeader}`}>
              <div>
                <h2>{t('usage_analytics.heatmap_title')}</h2>
                <p>
                  {t('usage_analytics.heatmap_hint')}{' '}
                  {t('usage_analytics.heatmap_timezone_hint', {
                    timeZone: usage.browserTimeZone,
                  })}
                </p>
              </div>
              <div className={styles.heatmapToolbar}>
                <div
                  className={`${styles.segmentedControl} ${styles.heatmapSegmented}`}
                  aria-label={t('usage_analytics.filter_metric')}
                >
                  {heatmapMetricOptions.map((option) => (
                    <button
                      key={option.value}
                      type="button"
                      aria-pressed={usage.heatmapMetric === option.value}
                      className={`${styles.segmentButton} ${
                        usage.heatmapMetric === option.value ? styles.segmentButtonActive : ''
                      }`}
                      onClick={() => usage.setHeatmapMetric(option.value)}
                    >
                      {t(option.labelKey)}
                    </button>
                  ))}
                </div>
                <div
                  className={`${styles.segmentedControl} ${styles.heatmapSegmented}`}
                  aria-label={t('usage_analytics.heatmap_scale_label')}
                >
                  {heatmapScaleOptions.map((option) => (
                    <button
                      key={option.value}
                      type="button"
                      aria-pressed={usage.heatmapScaleMode === option.value}
                      className={`${styles.segmentButton} ${
                        usage.heatmapScaleMode === option.value ? styles.segmentButtonActive : ''
                      }`}
                      onClick={() => usage.setHeatmapScaleMode(option.value)}
                    >
                      {t(option.labelKey)}
                    </button>
                  ))}
                </div>
              </div>
            </div>
            <UsageHeatmapChart
              metric={usage.heatmapMetric}
              points={usage.heatmap}
              scaleMode={usage.heatmapScaleMode}
              selectedCell={usage.selectedHeatmapCell}
              onSelect={usage.selectHeatmapCell}
            />
          </section>
          <section className={styles.heatmapWorkspace}>
            {usage.selectedHeatmapCell ? (
              <HeatmapDetailPanel
                dateError={usage.heatmapDateError}
                dateLoading={usage.heatmapDateLoading}
                dateOptions={usage.heatmapDateOptions}
                detail={usage.heatmapDetail}
                metric={usage.heatmapMetric}
                selectedCell={usage.selectedHeatmapCell}
                selectedDateKey={usage.selectedHeatmapDateKey}
                timeZone={usage.browserTimeZone}
                onClear={() => usage.selectHeatmapCell(null)}
                onSelectDate={usage.selectHeatmapDate}
              />
            ) : (
              <HeatmapHighlightsPanel
                highlights={usage.heatmapHighlights}
                selectedCell={usage.selectedHeatmapCell}
                timeZone={usage.browserTimeZone}
                onSelect={usage.selectHeatmapCell}
              />
            )}
          </section>
        </>
      ) : null}
    </div>
  );
}

function OverviewCard({
  title,
  rows,
  onViewAll,
}: {
  title: string;
  rows: UsageRankRow[];
  onViewAll: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div className={styles.overviewCard}>
      <div className={styles.panelHeader}>
        <h2>{title}</h2>
        <button type="button" onClick={onViewAll}>
          {t('usage_analytics.view_all')}
        </button>
      </div>
      <CostRankChart rows={rows} title={title} />
    </div>
  );
}

function AnomalyPointsPanel({
  rows,
  onOpen,
  onViewAll,
}: {
  rows: UsageServerAnomaly[];
  onOpen: (row: UsageServerAnomaly) => void;
  onViewAll?: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div className={styles.tablePanel}>
      <div className={styles.panelHeader}>
        <div>
          <h2>{t('usage_analytics.anomaly_points_title')}</h2>
          <p>{t('usage_analytics.anomaly_points_hint')}</p>
        </div>
        {onViewAll ? (
          <button type="button" onClick={onViewAll}>
            {t('usage_analytics.view_all')}
          </button>
        ) : null}
      </div>
      {rows.length === 0 ? (
        <div className={styles.inlineEmpty}>
          <IconInbox size={22} />
          <span>{t('usage_analytics.anomaly_none')}</span>
        </div>
      ) : (
        <div className={styles.tableWrap}>
          <table className={styles.anomalyTable}>
            <thead>
              <tr>
                <th>{t('usage_analytics.col_time')}</th>
                <th>{t('usage_analytics.col_severity')}</th>
                <th>{t('usage_analytics.metric_request_count')}</th>
                <th>{t('usage_analytics.metric_total_tokens')}</th>
                <th>{t('usage_analytics.metric_estimated_cost')}</th>
                <th>{t('usage_analytics.col_anomaly_type')}</th>
                <th>{t('usage_analytics.col_action')}</th>
              </tr>
            </thead>
            <tbody>
              {rows.slice(0, 8).map((row) => (
                <tr key={`${row.bucketMs}-${row.metricKeys.join('-')}`}>
                  <td>{row.label}</td>
                  <td>
                    <span
                      className={`${styles.severityBadge} ${styles[`severity${row.severity}`]}`}
                    >
                      {t(`usage_analytics.severity_${row.severity}`, row.severity)}
                    </span>
                  </td>
                  <td>{compactNumber(row.requestCount)}</td>
                  <td>{compactNumber(row.totalTokens)}</td>
                  <td>{formatMetricValue('estimatedCost', row.estimatedCost)}</td>
                  <td>
                    <span className={styles.anomalyTypeList}>
                      {row.metricKeys.slice(0, 3).map((key) => (
                        <em key={key}>{t(anomalyMetricLabelKey(key))}</em>
                      ))}
                    </span>
                  </td>
                  <td>
                    <button type="button" className={styles.linkButton} onClick={() => onOpen(row)}>
                      {t('usage_analytics.view_monitoring_details')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function DrilldownPreviewPanel({ rows, locale }: { rows: UsageDrilldownEvent[]; locale: string }) {
  const { t } = useTranslation();
  return (
    <div className={styles.tablePanel}>
      <div className={styles.panelHeader}>
        <div>
          <h2>{t('usage_analytics.drilldown_preview_title')}</h2>
          <p>{t('usage_analytics.drilldown_preview_hint')}</p>
        </div>
      </div>
      {rows.length === 0 ? (
        <div className={styles.inlineEmpty}>
          <IconInbox size={22} />
          <span>{t('usage_analytics.drilldown_preview_empty')}</span>
        </div>
      ) : (
        <div className={styles.tableWrap}>
          <table className={styles.drilldownTable}>
            <thead>
              <tr>
                <th>{t('usage_analytics.col_time')}</th>
                <th>{t('usage_analytics.filter_request_id')}</th>
                <th>{t('usage_analytics.col_model')}</th>
                <th>{t('usage_analytics.col_api_key')}</th>
                <th>{t('usage_analytics.metric_total_tokens')}</th>
                <th>{t('usage_analytics.metric_average_latency')}</th>
                <th>{t('usage_analytics.filter_status')}</th>
                <th>{t('usage_analytics.col_diagnostics')}</th>
              </tr>
            </thead>
            <tbody>
              {rows.slice(0, 12).map((row) => (
                <tr key={row.eventHash}>
                  <td>{formatLocalDateTime(row.timestampMs, locale)}</td>
                  <td className={styles.monoCell}>{row.requestId || row.eventHash.slice(0, 10)}</td>
                  <td>{row.model}</td>
                  <td>{row.apiKeyLabel || maskApiKeyHash(row.apiKeyHash)}</td>
                  <td>{compactNumber(row.totalTokens)}</td>
                  <td>{formatUsageDurationMs(row.latencyMs)}</td>
                  <td>
                    <span className={row.failed ? styles.statusFailed : styles.statusSuccess}>
                      {row.failed
                        ? t('usage_analytics.status_failed')
                        : t('usage_analytics.status_success')}
                    </span>
                  </td>
                  <td className={styles.monoCell}>
                    {formatDrilldownDiagnostics(row, locale, t) || '-'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function RankTable({
  rows,
  type,
  selectedId,
  onSelect,
  onCopyApiKey,
}: {
  rows: UsageRankRow[];
  type: 'model' | 'apiKey' | 'credential';
  selectedId?: string;
  onSelect: (row: UsageRankRow) => void;
  onCopyApiKey?: (row: UsageRankRow) => void;
}) {
  const { t } = useTranslation();
  const entityHeader =
    type === 'model'
      ? t('usage_analytics.col_model')
      : type === 'apiKey'
        ? t('usage_analytics.col_api_key')
        : t('usage_analytics.col_credential');
  return (
    <div className={styles.tableWrap}>
      <table
        className={
          type === 'apiKey'
            ? styles.apiKeyTable
            : type === 'model'
              ? styles.modelRankTable
              : styles.modelTable
        }
      >
        <thead>
          <tr>
            <th>{t('usage_analytics.col_rank')}</th>
            <th>{entityHeader}</th>
            <th>{t('usage_analytics.metric_request_count')}</th>
            <th>{t('usage_analytics.metric_total_tokens')}</th>
            <th>{t('usage_analytics.metric_input_tokens')}</th>
            <th>{t('usage_analytics.metric_output_tokens')}</th>
            <th>{t('usage_analytics.metric_cached_tokens')}</th>
            {type !== 'credential' ? <th>{t('usage_analytics.cache_read_rate')}</th> : null}
            <th>{t('usage_analytics.metric_estimated_cost')}</th>
            {type !== 'credential' ? (
              <th>{t('usage_analytics.metric_average_cost_per_call')}</th>
            ) : null}
            {type !== 'credential' ? <th>{t('usage_analytics.metric_failure_count')}</th> : null}
            <th>{t('usage_analytics.success_rate')}</th>
            <th>{t('usage_analytics.cost_share')}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row, index) => {
            const active = selectedId === (type === 'apiKey' ? row.apiKeyHash : row.id);
            return (
              <tr
                key={row.id}
                className={active ? styles.selectedRow : ''}
                onClick={() => onSelect(row)}
              >
                <td>{index + 1}</td>
                <td>
                  <span className={styles.entityCell}>
                    {type === 'apiKey' ? (
                      <IconKey size={16} />
                    ) : type === 'credential' ? (
                      <IconFileText size={16} />
                    ) : (
                      <IconModelCluster size={16} />
                    )}
                    {type === 'apiKey' ? getApiKeyRowDisplayLabel(row) : row.label}
                    {type === 'apiKey' && row.apiKeyCopyValue && onCopyApiKey ? (
                      <button
                        type="button"
                        className={styles.entityCopyButton}
                        onClick={(event) => {
                          event.stopPropagation();
                          onCopyApiKey(row);
                        }}
                        title={t('common.copy')}
                        aria-label={t('common.copy')}
                      >
                        <IconCopy size={13} />
                      </button>
                    ) : null}
                  </span>
                </td>
                <td>{compactNumber(row.requestCount)}</td>
                <td>{compactNumber(row.totalTokens)}</td>
                <td>{compactNumber(row.inputTokens)}</td>
                <td>{compactNumber(row.outputTokens)}</td>
                <td>{compactNumber(getUsageCacheTokens(row))}</td>
                {type !== 'credential' ? (
                  <td>{formatPercent(computeRowCacheHitRate(row))}</td>
                ) : null}
                <td>{formatMetricValue('estimatedCost', row.estimatedCost)}</td>
                {type !== 'credential' ? (
                  <td>{formatMetricValue('estimatedCost', computeRowAverageCostPerCall(row))}</td>
                ) : null}
                {type !== 'credential' ? (
                  <td className={row.failureCount > 0 ? styles.tonebad : ''}>
                    {compactNumber(row.failureCount)}
                  </td>
                ) : null}
                <td
                  className={
                    row.requestCount > 0 && row.successRate < USAGE_SUCCESS_RATE_WATCH_THRESHOLD
                      ? styles.tonebad
                      : ''
                  }
                >
                  {formatPercent(row.successRate)}
                </td>
                <td>{formatPercent(row.share)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function DetailPanel({
  className = '',
  row,
  type,
  action,
  keyDistribution,
}: {
  className?: string;
  row: UsageRankRow;
  type: 'model' | 'apiKey' | 'credential';
  action?: ReactNode;
  keyDistribution?: UsageModelKeyDistributionRow[];
}) {
  const { t, i18n } = useTranslation();
  const title =
    type === 'model'
      ? t('usage_analytics.model_detail_title', { model: row.label })
      : type === 'apiKey'
        ? t('usage_analytics.api_key_detail_title', { key: row.label })
        : t('usage_analytics.credential_detail_title', { credential: row.label });
  return (
    <div className={`${styles.detailPanel} ${className}`}>
      <div className={styles.panelHeader}>
        <h2>{title}</h2>
        {action}
      </div>
      {type === 'model' ? (
        // Unit economics + health only: absolute volumes already live in the rank table row.
        <div className={styles.detailMetrics}>
          <div>
            <span>{t('usage_analytics.average_tokens_per_request')}</span>
            <strong>
              {compactNumber(row.requestCount > 0 ? row.totalTokens / row.requestCount : 0)}
            </strong>
          </div>
          <div>
            <span>{t('usage_analytics.average_cost')}</span>
            <strong>{formatMetricValue('estimatedCost', computeRowAverageCostPerCall(row))}</strong>
          </div>
          <div>
            <span>{t('usage_analytics.cache_read_rate')}</span>
            <strong>{formatPercent(computeRowCacheHitRate(row))}</strong>
          </div>
          <div>
            <span>{t('usage_analytics.metric_failure_count')}</span>
            <strong className={row.failureCount > 0 ? styles.tonebad : ''}>
              {compactNumber(row.failureCount)}
            </strong>
          </div>
          <div>
            <span>{t('usage_analytics.success_rate')}</span>
            <strong
              className={
                row.requestCount > 0 && row.successRate < USAGE_SUCCESS_RATE_WATCH_THRESHOLD
                  ? styles.tonebad
                  : ''
              }
            >
              {formatPercent(row.successRate)}
            </strong>
          </div>
        </div>
      ) : type === 'apiKey' ? (
        <ApiKeyContextTable locale={i18n.language} rows={row.contexts ?? []} />
      ) : (
        <>
          <div className={styles.entityIdentityGrid}>
            {[
              ['credential_identity_provider', row.provider],
              ['credential_identity_account', row.account],
              ['credential_identity_auth_file', row.authFile],
              ['credential_identity_auth_index', row.authIndex],
              ['credential_identity_project_id', row.projectId],
            ].map(([key, value]) => (
              <div key={key}>
                <span>{t(`usage_analytics.${key}`)}</span>
                <strong title={String(value || '-')}>{value || '-'}</strong>
              </div>
            ))}
          </div>
          <UsageSummarySection
            cards={buildCredentialDetailCards({ locale: i18n.language, row, t })}
          />
        </>
      )}
      {type === 'model' && keyDistribution && keyDistribution.length > 0 ? (
        <div className={styles.modelDistribution}>
          <h3>{t('usage_analytics.model_caller_distribution')}</h3>
          <div>
            {keyDistribution.map((entry) => (
              <span key={entry.id}>
                <i
                  style={{
                    width: `${Math.max(8, Math.min(100, entry.share * 100))}%`,
                  }}
                />
                <b>{entry.label}</b>
                <em>{formatPercent(entry.share)}</em>
              </span>
            ))}
          </div>
        </div>
      ) : null}
      {(type === 'apiKey' || type === 'credential') && row.models && row.models.length > 0 ? (
        <div className={styles.modelDistribution}>
          <h3>{t('usage_analytics.related_model_distribution')}</h3>
          <div>
            {row.models.slice(0, 4).map((model) => (
              <span key={model.id}>
                <i
                  style={{
                    width: `${Math.max(8, Math.min(100, (model.totalTokens / Math.max(row.totalTokens, 1)) * 100))}%`,
                  }}
                />
                <b>{model.label}</b>
                <em>{formatPercent(model.totalTokens / Math.max(row.totalTokens, 1))}</em>
              </span>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  );
}

export function UsageAnalyticsPage() {
  return <UsageAnalyticsPageInner />;
}
