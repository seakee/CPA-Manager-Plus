import { useCallback, useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useMonitoringAnalytics } from '@/features/monitoring/hooks/useMonitoringAnalytics';
import { useUsageData } from '@/features/monitoring/hooks/useUsageData';
import { buildApiKeyDisplayMap } from '@/features/monitoring/model/apiKeys';
import { buildMonitoringAuthMetaMap } from '@/features/monitoring/model/authMeta';
import { readString } from '@/features/monitoring/model/base';
import type { MonitoringChannelMeta } from '@/features/monitoring/model/types';
import { loadMonitoringMetaPayload } from '@/features/monitoring/services/monitoringMetaService';
import { useConfigStore } from '@/stores';
import type { AuthFileItem } from '@/types/authFile';
import type { CredentialInfo } from '@/types/sourceInfo';
import { buildSourceInfoMap } from '@/utils/sourceResolver';
import { normalizeAuthIndex } from '@/utils/usage';
import {
  adaptUsageAnalyticsData,
  analyzeUsageBucket,
  buildSelectedApiKeyTrendSeries,
  buildSelectedCredentialTrendSeries,
  buildCredentialQuotaRows,
  buildEntityTrendSeries,
  buildKeyAnomalies,
  buildUsageInsights,
  buildUsageHeatmap,
  buildUsageHeatmapCellDetail,
  buildUsageHeatmapCellDateOptions,
  buildUsageHeatmapHighlights,
  buildUsageHeatmapRangeContext,
  buildUsageMatrix,
  buildUsageSummaryDelta,
  buildUsageAnalyticsFilters,
  buildUsageAnalyticsInclude,
  buildUsageTimeline,
  getUsageRangeBounds,
  resolveUsageGranularity,
  USAGE_ANALYTICS_DEFAULT_FILTERS,
  type UsageMatrixDimension,
  type UsageMatrixMetricKey,
  type UsageTrendMetricKey,
  type UsageAnalyticsFiltersState,
  type UsageAnalyticsTab,
  type UsageSelectedFilterKey,
  type UsageAnomalyAnalysis,
  type UsageHeatmapCellSelection,
  type UsageHeatmapDateOption,
  type UsageHeatmapMetricKey,
  type UsageHeatmapScaleMode,
  type UsagePerformanceHeatmapMetricKey,
  type UsageTimelinePoint,
  type UsageCredentialDisplayContext,
} from './usageAnalyticsModel';
import {
  buildUsageAnalyticsSearchParams,
  buildUsageAnalyticsUiStateFromSearchParams,
  readUsageAnalyticsUiState,
  writeUsageAnalyticsUiState,
  type UsageAnalyticsUiState,
} from './usageAnalyticsUiState';

const USAGE_SEARCH_DEBOUNCE_MS = 350;
const USAGE_HEATMAP_ALL_DATES_KEY = 'all';

type UsageAnalyticsMonitoringMeta = {
  authFiles: AuthFileItem[];
  channels: MonitoringChannelMeta[];
};

const EMPTY_USAGE_ANALYTICS_MONITORING_META: UsageAnalyticsMonitoringMeta = {
  authFiles: [],
  channels: [],
};

const getBrowserTimeZone = () => {
  if (typeof Intl === 'undefined') return 'UTC';
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
};

function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debouncedValue, setDebouncedValue] = useState(value);

  useEffect(() => {
    const timer = setTimeout(() => setDebouncedValue(value), delayMs);
    return () => clearTimeout(timer);
  }, [delayMs, value]);

  return debouncedValue;
}

export function useUsageAnalytics() {
  const config = useConfigStore((state) => state.config);
  const { apiKeyAliases, loadApiKeyAliases } = useUsageData({ loadUsageEvents: false });
  const [monitoringMeta, setMonitoringMeta] = useState<UsageAnalyticsMonitoringMeta>(
    EMPTY_USAGE_ANALYTICS_MONITORING_META
  );
  const [searchParams, setSearchParams] = useSearchParams();
  const [initialUiState] = useState<UsageAnalyticsUiState>(() =>
    buildUsageAnalyticsUiStateFromSearchParams(searchParams, readUsageAnalyticsUiState())
  );
  const [filters, setFiltersState] = useState<UsageAnalyticsFiltersState>(
    () => initialUiState.filters
  );
  const [activeTabState, setActiveTabState] = useState<UsageAnalyticsTab>(
    () => initialUiState.activeTab
  );
  const [nowMs, setNowMs] = useState(() => Date.now());
  const [selectedBucketMs, setSelectedBucketMs] = useState<number | null>(null);
  const [selectedModelId, setSelectedModelId] = useState('');
  const [selectedApiKeyHash, setSelectedApiKeyHash] = useState('');
  const [selectedCredentialId, setSelectedCredentialId] = useState('');
  const [trendMetric, setTrendMetric] = useState<UsageTrendMetricKey>('requestCount');
  const [matrixDimension, setMatrixDimension] = useState<UsageMatrixDimension>('apiKeyModel');
  const [matrixMetric, setMatrixMetric] = useState<UsageMatrixMetricKey>('requestCount');
  const [heatmapMetric, setHeatmapMetric] = useState<UsageHeatmapMetricKey>('requestCount');
  const [heatmapScaleMode, setHeatmapScaleMode] = useState<UsageHeatmapScaleMode>('absolute');
  const [performanceHeatmapMetric, setPerformanceHeatmapMetric] =
    useState<UsagePerformanceHeatmapMetricKey>('weightedDecodeTps');
  const [selectedHeatmapDateKey, setSelectedHeatmapDateKey] = useState(USAGE_HEATMAP_ALL_DATES_KEY);
  const [selectedHeatmapCell, setSelectedHeatmapCell] = useState<UsageHeatmapCellSelection | null>(
    null
  );
  const browserTimeZone = useMemo(() => getBrowserTimeZone(), []);
  const apiKeyDisplayMap = useMemo(
    () => buildApiKeyDisplayMap(config?.apiKeys || [], apiKeyAliases || []),
    [apiKeyAliases, config?.apiKeys]
  );
  const loadMonitoringMeta = useCallback(async () => {
    const payload = await loadMonitoringMetaPayload(config);
    setMonitoringMeta({
      authFiles: payload.authFiles,
      channels: payload.channels,
    });
  }, [config]);
  useEffect(() => {
    let cancelled = false;
    loadMonitoringMetaPayload(config)
      .then((payload) => {
        if (cancelled) return;
        setMonitoringMeta({
          authFiles: payload.authFiles,
          channels: payload.channels,
        });
      })
      .catch(() => {
        if (!cancelled) {
          setMonitoringMeta(EMPTY_USAGE_ANALYTICS_MONITORING_META);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [config]);
  const authMetaMap = useMemo(
    () => buildMonitoringAuthMetaMap(monitoringMeta.authFiles),
    [monitoringMeta.authFiles]
  );
  const authFileMap = useMemo(() => {
    const map = new Map<string, CredentialInfo>();
    monitoringMeta.authFiles.forEach((entry) => {
      const authIndex = normalizeAuthIndex(entry['auth_index'] ?? entry.authIndex);
      if (!authIndex) return;
      map.set(authIndex, {
        name:
          readString(entry.label) ||
          readString(entry.name) ||
          readString(entry.email) ||
          readString(entry.account) ||
          authIndex,
        type: readString(entry.provider) || readString(entry.type),
      });
    });
    return map;
  }, [monitoringMeta.authFiles]);
  const sourceInfoMap = useMemo(
    () =>
      buildSourceInfoMap({
        geminiApiKeys: config?.geminiApiKeys || [],
        claudeApiKeys: config?.claudeApiKeys || [],
        codexApiKeys: config?.codexApiKeys || [],
        vertexApiKeys: config?.vertexApiKeys || [],
        openaiCompatibility: config?.openaiCompatibility || [],
      }),
    [config]
  );
  const channelByAuthIndex = useMemo(() => {
    const map = new Map<string, MonitoringChannelMeta>();
    monitoringMeta.channels.forEach((channel) => {
      channel.authIndices.forEach((authIndex) => {
        map.set(authIndex, channel);
      });
    });
    return map;
  }, [monitoringMeta.channels]);
  const credentialDisplayContext = useMemo<UsageCredentialDisplayContext>(
    () => ({
      authMetaMap,
      authFileMap,
      sourceInfoMap,
      channelByAuthIndex,
    }),
    [authFileMap, authMetaMap, channelByAuthIndex, sourceInfoMap]
  );
  const setActiveTab = useCallback((tab: UsageAnalyticsTab) => {
    setActiveTabState(tab);
    writeUsageAnalyticsUiState({ activeTab: tab });
  }, []);
  const debouncedSearchQuery = useDebouncedValue(
    filters.searchQuery.trim(),
    USAGE_SEARCH_DEBOUNCE_MS
  );

  const bounds = useMemo(() => getUsageRangeBounds(filters, nowMs), [filters, nowMs]);
  const heatmapRangeContext = useMemo(
    () => buildUsageHeatmapRangeContext(bounds, 'en-US', browserTimeZone),
    [bounds, browserTimeZone]
  );
  const heatmapDateOptions = useMemo(
    () => buildUsageHeatmapCellDateOptions(heatmapRangeContext, selectedHeatmapCell),
    [heatmapRangeContext, selectedHeatmapCell]
  );
  const selectedHeatmapDate = useMemo<UsageHeatmapDateOption | null>(
    () =>
      selectedHeatmapDateKey === USAGE_HEATMAP_ALL_DATES_KEY
        ? null
        : (heatmapDateOptions.find((option) => option.key === selectedHeatmapDateKey) ?? null),
    [heatmapDateOptions, selectedHeatmapDateKey]
  );
  const activeHeatmapDateKey = selectedHeatmapDate
    ? selectedHeatmapDateKey
    : USAGE_HEATMAP_ALL_DATES_KEY;
  const resolvedGranularity = useMemo(
    () => resolveUsageGranularity(filters, nowMs),
    [filters, nowMs]
  );
  const analyticsFilters = useMemo(() => buildUsageAnalyticsFilters(filters), [filters]);

  useEffect(() => {
    const nextState = { activeTab: activeTabState, filters };
    writeUsageAnalyticsUiState(nextState);
    const nextParams = buildUsageAnalyticsSearchParams(nextState);
    if (nextParams.toString() !== searchParams.toString()) {
      setSearchParams(nextParams, { replace: true });
    }
  }, [activeTabState, filters, searchParams, setSearchParams]);
  const drilldownPreview = useMemo(() => {
    if (selectedBucketMs === null) return null;
    return {
      fromMs: selectedBucketMs,
      toMs:
        selectedBucketMs + (resolvedGranularity === 'day' ? 24 * 60 * 60 * 1000 : 60 * 60 * 1000),
      limit: 12,
    };
  }, [resolvedGranularity, selectedBucketMs]);
  const include = useMemo(
    () => buildUsageAnalyticsInclude(resolvedGranularity, drilldownPreview),
    [drilldownPreview, resolvedGranularity]
  );
  const dataScopeKey = useMemo(
    () =>
      JSON.stringify({
        bounds,
        drilldownPreview,
        filters: analyticsFilters,
        granularity: resolvedGranularity,
        searchQuery: debouncedSearchQuery,
      }),
    [analyticsFilters, bounds, debouncedSearchQuery, drilldownPreview, resolvedGranularity]
  );

  const analytics = useMonitoringAnalytics({
    fromMs: bounds?.fromMs,
    toMs: bounds?.toMs,
    nowMs,
    dataScopeKey,
    searchQuery: debouncedSearchQuery,
    filters: analyticsFilters,
    include,
    throttleMs: 0,
  });

  const heatmapDateInclude = useMemo(
    () => ({
      granularity: 'hour' as const,
      heatmap: true,
    }),
    []
  );
  const heatmapDateDataScopeKey = useMemo(
    () =>
      JSON.stringify({
        date: selectedHeatmapDate
          ? {
              fromMs: selectedHeatmapDate.fromMs,
              key: selectedHeatmapDate.key,
              toMs: selectedHeatmapDate.toMs,
            }
          : null,
        filters: analyticsFilters,
        searchQuery: debouncedSearchQuery,
      }),
    [analyticsFilters, debouncedSearchQuery, selectedHeatmapDate]
  );
  const heatmapDateAnalytics = useMonitoringAnalytics({
    fromMs: selectedHeatmapDate?.fromMs,
    toMs: selectedHeatmapDate?.toMs,
    nowMs,
    dataScopeKey: heatmapDateDataScopeKey,
    searchQuery: debouncedSearchQuery,
    filters: analyticsFilters,
    include: heatmapDateInclude,
    throttleMs: 0,
  });

  const analyticsData = analytics.dataStale ? null : analytics.data;
  const adapted = useMemo(
    () =>
      adaptUsageAnalyticsData(
        analyticsData,
        resolvedGranularity,
        filters.apiKeyKeyword,
        apiKeyDisplayMap,
        credentialDisplayContext
      ),
    [
      analyticsData,
      apiKeyDisplayMap,
      credentialDisplayContext,
      filters.apiKeyKeyword,
      resolvedGranularity,
    ]
  );
  const heatmapDateData = heatmapDateAnalytics.dataStale ? null : heatmapDateAnalytics.data;
  const heatmapDateRows = useMemo(
    () => buildUsageHeatmap(heatmapDateData?.heatmap ?? [], apiKeyDisplayMap),
    [apiKeyDisplayMap, heatmapDateData]
  );
  const heatmapDetailSource = selectedHeatmapDate ? heatmapDateRows : adapted.heatmap;
  const heatmapDateRefreshing = Boolean(
    selectedHeatmapDate &&
    (heatmapDateAnalytics.loading ||
      heatmapDateAnalytics.dataStale ||
      (!heatmapDateAnalytics.data && !heatmapDateAnalytics.error))
  );
  const summaryDelta = useMemo(
    () => buildUsageSummaryDelta(adapted.summary, adapted.summaryComparison),
    [adapted.summary, adapted.summaryComparison]
  );

  const selectedBucket = useMemo(
    () =>
      selectedBucketMs === null
        ? null
        : (adapted.timeline.find((point) => point.bucketMs === selectedBucketMs) ?? null),
    [adapted.timeline, selectedBucketMs]
  );

  const anomalyAnalysis = useMemo<UsageAnomalyAnalysis | null>(
    () =>
      selectedBucketMs === null ? null : analyzeUsageBucket(adapted.timeline, selectedBucketMs),
    [adapted.timeline, selectedBucketMs]
  );

  const selectedModel =
    adapted.modelRows.find((row) => row.id === selectedModelId) ?? adapted.modelRows[0] ?? null;
  const selectedApiKey =
    adapted.apiKeyRows.find((row) => row.apiKeyHash === selectedApiKeyHash) ??
    adapted.apiKeyRows[0] ??
    null;
  const selectedCredential =
    adapted.credentialRows.find((row) => row.id === selectedCredentialId) ??
    adapted.credentialRows[0] ??
    null;

  const modelTrendSeries = useMemo(
    () => buildEntityTrendSeries(adapted.modelRows, adapted.timeline, trendMetric, 4),
    [adapted.modelRows, adapted.timeline, trendMetric]
  );
  const apiKeyTrendSeries = useMemo(
    () => buildEntityTrendSeries(adapted.apiKeyRows, adapted.timeline, trendMetric, 4),
    [adapted.apiKeyRows, adapted.timeline, trendMetric]
  );
  const selectedApiKeyFilterHash = selectedApiKey?.apiKeyHash || selectedApiKey?.id || '';
  const selectedApiKeyTimelineFilters = useMemo(
    () =>
      selectedApiKeyFilterHash
        ? buildUsageAnalyticsFilters({ ...filters, apiKeyHash: selectedApiKeyFilterHash })
        : {},
    [filters, selectedApiKeyFilterHash]
  );
  const selectedApiKeyTimelineInclude = useMemo(
    () => ({
      granularity: resolvedGranularity,
      timeline: true,
    }),
    [resolvedGranularity]
  );
  const selectedApiKeyTimelineDataScopeKey = useMemo(
    () =>
      JSON.stringify({
        activeTab: activeTabState,
        bounds,
        filters: selectedApiKeyTimelineFilters,
        granularity: resolvedGranularity,
        searchQuery: debouncedSearchQuery,
        selectedApiKeyHash: selectedApiKeyFilterHash,
      }),
    [
      activeTabState,
      bounds,
      debouncedSearchQuery,
      resolvedGranularity,
      selectedApiKeyFilterHash,
      selectedApiKeyTimelineFilters,
    ]
  );
  const selectedApiKeyTimelineAnalytics = useMonitoringAnalytics({
    fromMs: activeTabState === 'apiKeys' && selectedApiKeyFilterHash ? bounds?.fromMs : undefined,
    toMs: activeTabState === 'apiKeys' && selectedApiKeyFilterHash ? bounds?.toMs : undefined,
    nowMs,
    dataScopeKey: selectedApiKeyTimelineDataScopeKey,
    searchQuery: debouncedSearchQuery,
    filters: selectedApiKeyTimelineFilters,
    include: selectedApiKeyTimelineInclude,
    throttleMs: 0,
  });
  const selectedApiKeyTimelineData = selectedApiKeyTimelineAnalytics.dataStale
    ? null
    : selectedApiKeyTimelineAnalytics.data;
  const selectedApiKeyTimeline = useMemo(
    () => buildUsageTimeline(selectedApiKeyTimelineData?.timeline ?? [], resolvedGranularity),
    [resolvedGranularity, selectedApiKeyTimelineData]
  );
  const selectedApiKeyTrendSeries = useMemo(
    () => buildSelectedApiKeyTrendSeries(selectedApiKey, selectedApiKeyTimeline, trendMetric),
    [selectedApiKey, selectedApiKeyTimeline, trendMetric]
  );
  const credentialTrendSeries = useMemo(
    () =>
      buildSelectedCredentialTrendSeries(
        selectedCredential,
        adapted.credentialTimeline,
        trendMetric
      ),
    [adapted.credentialTimeline, selectedCredential, trendMetric]
  );
  const heatmapDetail = useMemo(
    () => buildUsageHeatmapCellDetail(heatmapDetailSource, selectedHeatmapCell, heatmapMetric),
    [heatmapDetailSource, heatmapMetric, selectedHeatmapCell]
  );
  const heatmapHighlights = useMemo(
    () => buildUsageHeatmapHighlights(adapted.heatmap),
    [adapted.heatmap]
  );
  const matrix = useMemo(
    () =>
      buildUsageMatrix({
        apiKeyRows: adapted.apiKeyRows,
        credentialRows: adapted.credentialRows,
        dimension: matrixDimension,
        metric: matrixMetric,
      }),
    [adapted.apiKeyRows, adapted.credentialRows, matrixDimension, matrixMetric]
  );
  const keyAnomalies = useMemo(() => buildKeyAnomalies(adapted.apiKeyRows), [adapted.apiKeyRows]);
  const credentialAnomalies = useMemo(
    () => buildKeyAnomalies(adapted.credentialRows),
    [adapted.credentialRows]
  );
  const credentialQuotaRows = useMemo(
    () => buildCredentialQuotaRows(adapted.credentialRows, nowMs),
    [adapted.credentialRows, nowMs]
  );
  const insights = useMemo(
    () =>
      buildUsageInsights({
        apiKeyRows: adapted.apiKeyRows,
        credentialRows: adapted.credentialRows,
        modelRows: adapted.modelRows,
        providerRows: adapted.providerRows,
        summary: adapted.summary,
      }),
    [
      adapted.apiKeyRows,
      adapted.credentialRows,
      adapted.modelRows,
      adapted.providerRows,
      adapted.summary,
    ]
  );
  const setFilters = useCallback((patch: Partial<UsageAnalyticsFiltersState>) => {
    setFiltersState((current) => {
      const next = { ...current, ...patch };
      writeUsageAnalyticsUiState({ filters: next });
      return next;
    });
    setSelectedBucketMs(null);
    setSelectedHeatmapDateKey(USAGE_HEATMAP_ALL_DATES_KEY);
    setSelectedHeatmapCell(null);
  }, []);

  const resetFilters = useCallback(() => {
    setFiltersState(USAGE_ANALYTICS_DEFAULT_FILTERS);
    writeUsageAnalyticsUiState({ filters: USAGE_ANALYTICS_DEFAULT_FILTERS });
    setSelectedBucketMs(null);
    setSelectedHeatmapDateKey(USAGE_HEATMAP_ALL_DATES_KEY);
    setSelectedHeatmapCell(null);
  }, []);

  const clearFilter = useCallback((key: UsageSelectedFilterKey) => {
    setFiltersState((current) => {
      const next = {
        ...current,
        [key]: 'all',
      };
      writeUsageAnalyticsUiState({ filters: next });
      return next;
    });
    setSelectedBucketMs(null);
    setSelectedHeatmapDateKey(USAGE_HEATMAP_ALL_DATES_KEY);
    setSelectedHeatmapCell(null);
  }, []);

  const selectBucket = useCallback((point: UsageTimelinePoint | null) => {
    setSelectedBucketMs(point?.bucketMs ?? null);
  }, []);

  const selectHeatmapCell = useCallback((cell: UsageHeatmapCellSelection | null) => {
    setSelectedHeatmapCell(cell);
    setSelectedHeatmapDateKey(USAGE_HEATMAP_ALL_DATES_KEY);
  }, []);

  const selectHeatmapDate = useCallback((key: string) => {
    setSelectedHeatmapDateKey(key || USAGE_HEATMAP_ALL_DATES_KEY);
  }, []);

  const refresh = useCallback(() => {
    setNowMs(Date.now());
    void loadApiKeyAliases();
    void loadMonitoringMeta();
    void analytics.refresh({ force: true });
    if (selectedApiKeyTimelineAnalytics.enabled) {
      void selectedApiKeyTimelineAnalytics.refresh({ force: true });
    }
    if (selectedHeatmapDate) {
      void heatmapDateAnalytics.refresh({ force: true });
    }
  }, [
    analytics,
    heatmapDateAnalytics,
    loadApiKeyAliases,
    loadMonitoringMeta,
    selectedApiKeyTimelineAnalytics,
    selectedHeatmapDate,
  ]);

  return {
    filters,
    setFilters,
    resetFilters,
    clearFilter,
    activeTab: activeTabState,
    setActiveTab,
    bounds,
    resolvedGranularity,
    loading: analytics.loading,
    error: analytics.error,
    enabled: analytics.enabled,
    apiKeyDisplayMap,
    unavailableReason: analytics.unavailableReason,
    lastRefreshedAt: analytics.lastRefreshedAt,
    refresh,
    summary: adapted.summary,
    summaryDelta,
    timeline: adapted.timeline,
    modelRows: adapted.modelRows,
    apiKeyRows: adapted.apiKeyRows,
    credentialRows: adapted.credentialRows,
    allCredentialRows: adapted.credentialRows,
    providerRows: adapted.providerRows,
    heatmap: adapted.heatmap,
    performanceHeatmap: adapted.performanceHeatmap,
    performanceHeatmapMetric,
    setPerformanceHeatmapMetric,
    heatmapMetric,
    setHeatmapMetric,
    heatmapScaleMode,
    setHeatmapScaleMode,
    heatmapDateOptions,
    selectedHeatmapDateKey: activeHeatmapDateKey,
    selectHeatmapDate,
    heatmapDateLoading: heatmapDateRefreshing,
    heatmapDateError: selectedHeatmapDate ? heatmapDateAnalytics.error : '',
    selectedHeatmapCell,
    selectHeatmapCell,
    heatmapDetail,
    heatmapHighlights,
    browserTimeZone,
    matrix,
    matrixDimension,
    setMatrixDimension,
    matrixMetric,
    setMatrixMetric,
    trendMetric,
    setTrendMetric,
    modelTrendSeries,
    apiKeyTrendSeries,
    selectedApiKeyTrendSeries,
    credentialTrendSeries,
    keyAnomalies,
    credentialAnomalies,
    credentialQuotaRows,
    insights,
    anomalyPoints: adapted.anomalyPoints,
    drilldownPreview: adapted.drilldownPreview,
    filterOptions: adapted.filterOptions,
    selectedBucket,
    selectBucket,
    anomalyAnalysis,
    selectedModel,
    setSelectedModelId,
    selectedApiKey,
    setSelectedApiKeyHash,
    selectedCredential,
    setSelectedCredentialId,
  };
}
