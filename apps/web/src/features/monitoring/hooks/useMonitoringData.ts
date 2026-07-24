import { useCallback, useEffect, useMemo, useState } from 'react';
import type { MonitoringAnalyticsEventRow } from '@/services/api/usageService';
import type { AuthFileItem } from '@/types/authFile';
import type { CredentialInfo } from '@/types/sourceInfo';
import { buildSourceInfoMap } from '@/utils/sourceResolver';
import { collectUsageDetailsWithEndpoint, normalizeAuthIndex } from '@/utils/usage';
import { readString } from '../model/base';
import { buildApiKeyDisplayMap } from '../model/apiKeys';
import { buildMonitoringAuthMetaMap } from '../model/authMeta';
import { getRangeBounds, shouldUseHourlyTimeline } from '../model/range';
import {
  buildChannelRows,
  buildFailureRows,
  buildFailureSourceRows,
  buildHourlyDistribution,
  buildModelRows,
  buildModelShareRows,
  buildStatusChips,
  buildTaskBuckets,
  buildTimeline,
} from '../model/chartBuilders';
import {
  buildAnalyticsFilters,
  buildAccountRowsFromAnalytics,
  buildApiKeyRowsFromAnalytics,
  buildChannelRowsFromAnalytics,
  buildFailureRowsFromAnalytics,
  buildFailureSourceRowsFromAnalytics,
  buildFilterOptionsFromAnalytics,
  buildHourlyDistributionFromAnalytics,
  buildModelRowsFromAnalytics,
  buildModelShareRowsFromAnalytics,
  buildSummaryFromAnalytics,
  buildTaskBucketsFromAnalytics,
  buildTimelineFromAnalytics,
  buildUsageDetailsFromAnalyticsEvents,
  mergeAnalyticsEventItems,
} from '../model/analyticsAdapters';
import { buildEventRows } from '../model/eventRows';
import {
  buildMonitoringCenterAnalyticsInclude,
  buildMonitoringFilterSelectorsScopeKey,
  buildMonitoringFilterSelectorsInclude,
  mergeMonitoringAccountOptionRows,
} from '../model/monitoringAnalyticsModel';
import {
  buildAccountRows,
  buildApiKeyRows,
  buildMonitoringSummary,
  buildRangeFilteredRows,
  buildScopeFilteredRows,
  shouldIncludeInStats,
} from '../model/rowBuilders';
import type {
  MonitoringAuthMeta,
  MonitoringApiKeyRow,
  MonitoringChannelMeta,
  MonitoringFilterOptions,
  MonitoringMetadata,
  UseMonitoringDataParams,
  UseMonitoringDataReturn,
} from '../model/types';
import { loadMonitoringMetaPayload } from '../services/monitoringMetaService';
import { useMonitoringAnalytics } from './useMonitoringAnalytics';

export type {
  MonitoringAccountModelSpendRow,
  MonitoringAccountRow,
  MonitoringApiKeyModelSpendRow,
  MonitoringApiKeyRow,
  MonitoringChannelMeta,
  MonitoringChannelRow,
  MonitoringCustomTimeRange,
  MonitoringEventRow,
  MonitoringFailureRow,
  MonitoringFailureSourceRow,
  MonitoringKpi,
  MonitoringMetadata,
  MonitoringModelRow,
  MonitoringModelShareRow,
  MonitoringRealtimeRow,
  MonitoringScopeFilters,
  MonitoringStatusChip,
  MonitoringStatusTone,
  MonitoringSummary,
  MonitoringTaskBucketRow,
  MonitoringTimeRange,
  MonitoringTimelinePoint,
  UseMonitoringDataParams,
  UseMonitoringDataReturn,
} from '../model/types';
export { buildApiKeyDisplayMap } from '../model/apiKeys';
export { buildMonitoringAuthMetaMap } from '../model/authMeta';
export { getRangeBounds } from '../model/range';
export {
  buildAccountRows,
  buildApiKeyRows,
  buildMonitoringSummary,
  buildRangeFilteredRows,
  buildScopeFilteredRows,
  buildRealtimeMonitorRows,
} from '../model/rowBuilders';

const MONITORING_EVENTS_PAGE_LIMIT = 500;
export const MONITORING_EVENTS_RETENTION_LIMIT = 2_000;
const MONITORING_PRESENTATION_CACHE_LIMIT = 4;
const EMPTY_MONITORING_ANALYTICS_EVENT_ROWS: MonitoringAnalyticsEventRow[] = [];

interface MonitoringEventsPageState {
  scopeKey: string;
  beforeMs: number | null;
  beforeId: number | null;
  items: MonitoringAnalyticsEventRow[];
  hasMore: boolean;
  loadingMore: boolean;
  lastPageKey: string;
}

export type MonitoringPresentationSnapshot = Pick<
  UseMonitoringDataReturn,
  | 'summary'
  | 'timeline'
  | 'timelineGranularity'
  | 'hourlyDistribution'
  | 'modelShareRows'
  | 'channelRows'
  | 'modelRows'
  | 'failureSourceRows'
  | 'taskBuckets'
  | 'recentFailures'
  | 'accountRows'
  | 'apiKeyRows'
  | 'filterOptions'
  | 'filteredRows'
  | 'eventsHasMore'
  | 'eventsLoadingMore'
  | 'eventsRetentionLimited'
  | 'eventsTotalCount'
  | 'eventsLoadedCount'
  | 'lastRefreshedAt'
>;

export interface MonitoringPresentationSnapshotResolution {
  snapshot: MonitoringPresentationSnapshot;
  hasPresentationSnapshot: boolean;
  usingSnapshotFallback: boolean;
}

interface MonitoringPresentationSnapshotStore {
  cachedSnapshots: Map<string, MonitoringPresentationSnapshot>;
  lastStableSnapshot: MonitoringPresentationSnapshot | null;
}

const createEventsPageState = (scopeKey = ''): MonitoringEventsPageState => ({
  scopeKey,
  beforeMs: null,
  beforeId: null,
  items: [],
  hasMore: false,
  loadingMore: false,
  lastPageKey: '',
});

const buildEventsPageKey = (
  scopeKey: string,
  beforeMs: number | null,
  pageItems: MonitoringAnalyticsEventRow[],
  nextBeforeMs: number
) =>
  [
    scopeKey,
    beforeMs ?? 'root',
    nextBeforeMs,
    pageItems.length,
    pageItems[0]?.event_hash ?? '',
    pageItems[pageItems.length - 1]?.event_hash ?? '',
  ].join(':');

export const buildMonitoringEventsScopeKey = (
  timeRange: UseMonitoringDataParams['timeRange'],
  analyticsBounds: { startMs: number; endMs: number } | null,
  searchQuery: string,
  searchApiKeyHash: string | undefined,
  filters: unknown,
  granularity: string
) =>
  JSON.stringify({
    range: timeRange,
    bounds:
      timeRange === 'custom'
        ? analyticsBounds
        : analyticsBounds
          ? { startMs: analyticsBounds.startMs }
          : null,
    searchQuery,
    searchApiKeyHash,
    filters,
    granularity,
  });

export const mergeMonitoringEventsPageItems = (
  previousItems: MonitoringAnalyticsEventRow[],
  pageItems: MonitoringAnalyticsEventRow[],
  requestBeforeMs: number | null
) => {
  if (requestBeforeMs) {
    return mergeAnalyticsEventItems(previousItems, pageItems).slice(
      0,
      MONITORING_EVENTS_RETENTION_LIMIT
    );
  }
  if (previousItems.length === 0) {
    return pageItems.slice(0, MONITORING_EVENTS_RETENTION_LIMIT);
  }
  return mergeAnalyticsEventItems(pageItems, previousItems).slice(
    0,
    MONITORING_EVENTS_RETENTION_LIMIT
  );
};

export const withoutMonitoringSnapshotEvents = (
  snapshot: MonitoringPresentationSnapshot
): MonitoringPresentationSnapshot => ({
  ...snapshot,
  filteredRows: [],
  eventsHasMore: false,
  eventsLoadingMore: false,
  eventsRetentionLimited: false,
  eventsTotalCount: 0,
  eventsLoadedCount: 0,
});

const uniqueOptionValues = (values: Array<string | null | undefined>) =>
  Array.from(new Set(values.map((value) => String(value || '').trim()).filter(Boolean))).sort(
    (left, right) => left.localeCompare(right)
  );

const mergeMonitoringApiKeyOptionRows = (
  ...collections: ReadonlyArray<readonly MonitoringApiKeyRow[]>
) => {
  const rows = new Map<string, MonitoringApiKeyRow>();
  collections.flat().forEach((row) => {
    const key = row.apiKeyHash.trim().toLowerCase() || row.id;
    if (!rows.has(key)) {
      rows.set(key, row);
    }
  });
  return Array.from(rows.values());
};

export const resolveMonitoringDisplayEventItems = ({
  analyticsData,
  currentPageItems,
  eventsPageItems,
  eventsBeforeMs,
  dataStale,
}: {
  analyticsData: { events?: { items: MonitoringAnalyticsEventRow[] } } | null;
  currentPageItems: MonitoringAnalyticsEventRow[] | null;
  eventsPageItems: MonitoringAnalyticsEventRow[];
  eventsBeforeMs: number | null;
  dataStale: boolean;
}): MonitoringAnalyticsEventRow[] => {
  if (dataStale) {
    return eventsPageItems.length > 0
      ? eventsPageItems
      : (analyticsData?.events?.items ?? EMPTY_MONITORING_ANALYTICS_EVENT_ROWS);
  }

  if (!currentPageItems) {
    return eventsPageItems;
  }

  const existingEventHashes = new Set(eventsPageItems.map((item) => item.event_hash));
  if (currentPageItems.every((item) => existingEventHashes.has(item.event_hash))) {
    return eventsPageItems;
  }

  return mergeMonitoringEventsPageItems(eventsPageItems, currentPageItems, eventsBeforeMs);
};

export const resolveMonitoringPresentationSnapshot = ({
  computedSnapshot,
  scopeKey,
  dataStale,
  cachedSnapshots,
  lastStableSnapshot,
}: {
  computedSnapshot: MonitoringPresentationSnapshot;
  scopeKey: string;
  dataStale: boolean;
  cachedSnapshots: ReadonlyMap<string, MonitoringPresentationSnapshot>;
  lastStableSnapshot: MonitoringPresentationSnapshot | null;
}): MonitoringPresentationSnapshotResolution => {
  if (!dataStale) {
    return {
      snapshot: computedSnapshot,
      hasPresentationSnapshot: true,
      usingSnapshotFallback: false,
    };
  }

  const snapshot = cachedSnapshots.get(scopeKey) ?? lastStableSnapshot;
  return {
    snapshot: snapshot ?? computedSnapshot,
    hasPresentationSnapshot: Boolean(snapshot),
    usingSnapshotFallback: Boolean(snapshot),
  };
};

export function useMonitoringData({
  usage,
  config,
  modelPrices,
  apiKeyAliases,
  timeRange,
  customTimeRange,
  searchQuery,
  searchApiKeyHash,
  scopeFilters,
  activeDataTab = 'accounts',
}: UseMonitoringDataParams): UseMonitoringDataReturn {
  const [authFiles, setAuthFiles] = useState<AuthFileItem[]>([]);
  const [channels, setChannels] = useState<MonitoringChannelMeta[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [analyticsNowMs, setAnalyticsNowMs] = useState(() => Date.now());
  const [eventsPageState, setEventsPageState] = useState<MonitoringEventsPageState>(() =>
    createEventsPageState()
  );
  const [presentationSnapshotStore, setPresentationSnapshotStore] =
    useState<MonitoringPresentationSnapshotStore>(() => ({
      cachedSnapshots: new Map(),
      lastStableSnapshot: null,
    }));

  const analyticsBounds = useMemo(() => {
    const bounds = getRangeBounds(timeRange, analyticsNowMs, customTimeRange);
    if (!bounds) return null;
    return {
      startMs: Number.isFinite(bounds.startMs) && bounds.startMs > 0 ? bounds.startMs : 1,
      endMs: Math.max(bounds.endMs, 1),
    };
  }, [analyticsNowMs, customTimeRange, timeRange]);

  const refreshMeta = useCallback(
    async (showLoading: boolean = true) => {
      if (showLoading) {
        setLoading(true);
        setError('');
      }

      const payload = await loadMonitoringMetaPayload(config);
      setAuthFiles(payload.authFiles);
      setChannels(payload.channels);
      setError(payload.error);
      setLoading(false);
      setEventsPageState((previous) =>
        previous.beforeMs === null && previous.beforeId === null && !previous.loadingMore
          ? previous
          : { ...previous, beforeMs: null, beforeId: null, loadingMore: false }
      );
      setAnalyticsNowMs(Date.now());
    },
    [config]
  );

  useEffect(() => {
    let cancelled = false;

    loadMonitoringMetaPayload(config).then((payload) => {
      if (cancelled) return;
      setAuthFiles(payload.authFiles);
      setChannels(payload.channels);
      setError(payload.error);
      setLoading(false);
    });

    return () => {
      cancelled = true;
    };
  }, [config]);

  const authMetaMap = useMemo(() => buildMonitoringAuthMetaMap(authFiles), [authFiles]);

  const uniqueAuthMeta = useMemo(() => {
    const map = new Map<string, MonitoringAuthMeta>();
    authMetaMap.forEach((item) => {
      map.set(item.authIndex, item);
    });
    return Array.from(map.values());
  }, [authMetaMap]);

  const authFileMap = useMemo(() => {
    const map = new Map<string, CredentialInfo>();
    authFiles.forEach((entry) => {
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
  }, [authFiles]);

  const sourceInfoMap = useMemo(
    () =>
      buildSourceInfoMap({
        geminiApiKeys: config?.geminiApiKeys || [],
        claudeApiKeys: config?.claudeApiKeys || [],
        codexApiKeys: config?.codexApiKeys || [],
        xaiApiKeys: config?.xaiApiKeys || [],
        vertexApiKeys: config?.vertexApiKeys || [],
        openaiCompatibility: config?.openaiCompatibility || [],
      }),
    [config]
  );

  const channelByAuthIndex = useMemo(() => {
    const map = new Map<string, MonitoringChannelMeta>();
    channels.forEach((channel) => {
      channel.authIndices.forEach((authIndex) => {
        map.set(authIndex, channel);
      });
    });
    return map;
  }, [channels]);

  const apiKeyDisplayMap = useMemo(() => {
    return buildApiKeyDisplayMap(config?.apiKeys || [], apiKeyAliases || []);
  }, [apiKeyAliases, config?.apiKeys]);

  const analyticsFilters = useMemo(
    () => buildAnalyticsFilters(scopeFilters, authMetaMap, channels),
    [authMetaMap, channels, scopeFilters]
  );

  const analyticsGranularity = useMemo(
    () => (shouldUseHourlyTimeline(timeRange, customTimeRange) ? 'hour' : 'day'),
    [customTimeRange, timeRange]
  );

  const eventsScopeKey = useMemo(
    () =>
      buildMonitoringEventsScopeKey(
        timeRange,
        analyticsBounds,
        searchQuery,
        searchApiKeyHash,
        analyticsFilters,
        analyticsGranularity
      ),
    [
      analyticsBounds,
      analyticsFilters,
      analyticsGranularity,
      searchApiKeyHash,
      searchQuery,
      timeRange,
    ]
  );

  const activeEventsPageState = useMemo(
    () =>
      eventsPageState.scopeKey === eventsScopeKey
        ? eventsPageState
        : createEventsPageState(eventsScopeKey),
    [eventsPageState, eventsScopeKey]
  );
  const eventsBeforeMs = activeEventsPageState.beforeMs;
  const eventsBeforeId = activeEventsPageState.beforeId;
  const eventItems = activeEventsPageState.items;
  const eventsHasMore = activeEventsPageState.hasMore;
  const eventsLoadingMore = activeEventsPageState.loadingMore;

  const analyticsInclude = useMemo(
    () =>
      buildMonitoringCenterAnalyticsInclude(activeDataTab, analyticsGranularity, {
        limit: MONITORING_EVENTS_PAGE_LIMIT,
        before_ms: eventsBeforeMs,
        before_id: eventsBeforeId,
      }),
    [activeDataTab, analyticsGranularity, eventsBeforeId, eventsBeforeMs]
  );
  const requestedEventsBeforeMs = analyticsInclude.events_page?.before_ms ?? null;
  const requestedEventsBeforeId = analyticsInclude.events_page?.before_id ?? null;
  const analyticsScopeKey = useMemo(
    () => JSON.stringify({ activeDataTab, eventsScopeKey }),
    [activeDataTab, eventsScopeKey]
  );

  const analytics = useMonitoringAnalytics({
    fromMs: analyticsBounds?.startMs,
    toMs: analyticsBounds?.endMs,
    nowMs: analyticsNowMs,
    dataScopeKey: analyticsScopeKey,
    searchQuery,
    searchApiKeyHash,
    filters: analyticsFilters,
    include: analyticsInclude,
    throttleMs: 1_000,
  });
  const filterSelectorsInclude = useMemo(() => buildMonitoringFilterSelectorsInclude(), []);
  const filterSelectorsScopeKey = useMemo(
    () =>
      buildMonitoringFilterSelectorsScopeKey(
        timeRange,
        analyticsBounds,
        searchQuery,
        searchApiKeyHash
      ),
    [analyticsBounds, searchApiKeyHash, searchQuery, timeRange]
  );
  const filterSelectorsAnalytics = useMonitoringAnalytics({
    fromMs: analyticsBounds?.startMs,
    toMs: analyticsBounds?.endMs,
    nowMs: analyticsNowMs,
    dataScopeKey: filterSelectorsScopeKey,
    searchQuery,
    searchApiKeyHash,
    include: filterSelectorsInclude,
    throttleMs: 1_000,
  });
  const analyticsData = analytics.data;
  const currentAnalyticsData = analytics.dataStale ? null : analyticsData;
  const currentFilterSelectorsData = filterSelectorsAnalytics.dataStale
    ? null
    : filterSelectorsAnalytics.data;
  const displayEventItems = useMemo(
    () =>
      resolveMonitoringDisplayEventItems({
        analyticsData,
        currentPageItems: currentAnalyticsData?.events?.items ?? null,
        eventsPageItems: eventItems,
        eventsBeforeMs: requestedEventsBeforeMs,
        dataStale: analytics.dataStale,
      }),
    [
      analytics.dataStale,
      analyticsData,
      currentAnalyticsData?.events?.items,
      eventItems,
      requestedEventsBeforeMs,
    ]
  );
  const eventsLoadedCount = displayEventItems.length;
  const displayEventsTotalCount = currentAnalyticsData?.events?.total_count ?? eventsLoadedCount;
  const eventsRetentionLimited =
    eventsLoadedCount >= MONITORING_EVENTS_RETENTION_LIMIT &&
    (Boolean(currentAnalyticsData?.events?.has_more) ||
      eventsHasMore ||
      displayEventsTotalCount > MONITORING_EVENTS_RETENTION_LIMIT);
  const displayEventsHasMore =
    !eventsRetentionLimited && (currentAnalyticsData?.events?.has_more ?? eventsHasMore);

  useEffect(() => {
    const page = currentAnalyticsData?.events;
    if (!page) return;
    const requestBeforeMs = requestedEventsBeforeMs;
    const requestBeforeId = requestedEventsBeforeId;
    const pageKey = buildEventsPageKey(
      eventsScopeKey,
      requestBeforeMs,
      page.items,
      page.next_before_ms
    );
    let cancelled = false;
    queueMicrotask(() => {
      if (cancelled) return;
      setEventsPageState((previous) => {
        const base =
          previous.scopeKey === eventsScopeKey ? previous : createEventsPageState(eventsScopeKey);
        if (base.lastPageKey === pageKey) return base;
        const items = mergeMonitoringEventsPageItems(base.items, page.items, requestBeforeMs);
        return {
          scopeKey: eventsScopeKey,
          beforeMs: requestBeforeMs,
          beforeId: requestBeforeId,
          items,
          hasMore: page.has_more && items.length < MONITORING_EVENTS_RETENTION_LIMIT,
          loadingMore: false,
          lastPageKey: pageKey,
        };
      });
    });
    return () => {
      cancelled = true;
    };
  }, [
    currentAnalyticsData?.events,
    eventsScopeKey,
    requestedEventsBeforeId,
    requestedEventsBeforeMs,
  ]);

  useEffect(() => {
    if (analytics.error) {
      let cancelled = false;
      queueMicrotask(() => {
        if (cancelled) return;
        setEventsPageState((previous) =>
          previous.loadingMore ? { ...previous, loadingMore: false } : previous
        );
      });
      return () => {
        cancelled = true;
      };
    }
  }, [analytics.error]);

  const loadMoreEvents = useCallback(() => {
    if (
      activeDataTab !== 'realtime' ||
      analytics.loading ||
      eventsLoadingMore ||
      !eventsHasMore ||
      eventItems.length >= MONITORING_EVENTS_RETENTION_LIMIT
    )
      return;
    const nextBeforeMs = currentAnalyticsData?.events?.next_before_ms;
    if (!nextBeforeMs) return;
    const nextBeforeId = currentAnalyticsData?.events?.next_before_id ?? null;
    setEventsPageState((previous) => {
      const base =
        previous.scopeKey === eventsScopeKey ? previous : createEventsPageState(eventsScopeKey);
      if (base.loadingMore) return base;
      return { ...base, beforeMs: nextBeforeMs, beforeId: nextBeforeId, loadingMore: true };
    });
  }, [
    activeDataTab,
    currentAnalyticsData?.events?.next_before_ms,
    currentAnalyticsData?.events?.next_before_id,
    analytics.loading,
    eventItems.length,
    eventsScopeKey,
    eventsHasMore,
    eventsLoadingMore,
  ]);

  const allRows = useMemo(() => {
    const details = analyticsData
      ? buildUsageDetailsFromAnalyticsEvents(displayEventItems)
      : collectUsageDetailsWithEndpoint(usage);
    return buildEventRows(
      details,
      authMetaMap,
      authFileMap,
      sourceInfoMap,
      channelByAuthIndex,
      modelPrices,
      apiKeyDisplayMap
    ).sort((left, right) => right.timestampMs - left.timestampMs);
  }, [
    apiKeyDisplayMap,
    authFileMap,
    authMetaMap,
    channelByAuthIndex,
    analyticsData,
    displayEventItems,
    modelPrices,
    sourceInfoMap,
    usage,
  ]);

  const rangeFilteredRows = useMemo(
    () =>
      buildRangeFilteredRows(allRows, timeRange, customTimeRange, searchQuery, searchApiKeyHash),
    [allRows, customTimeRange, searchApiKeyHash, searchQuery, timeRange]
  );
  const filteredRows = useMemo(
    () => buildScopeFilteredRows(rangeFilteredRows, scopeFilters),
    [rangeFilteredRows, scopeFilters]
  );
  const statsRows = useMemo(() => filteredRows.filter(shouldIncludeInStats), [filteredRows]);

  const summary = useMemo(
    () =>
      currentAnalyticsData?.summary
        ? buildSummaryFromAnalytics(currentAnalyticsData.summary)
        : buildMonitoringSummary(statsRows),
    [currentAnalyticsData, statsRows]
  );
  const timelineData = useMemo(
    () =>
      currentAnalyticsData?.timeline
        ? {
            granularity:
              currentAnalyticsData.granularity === 'hour' ? ('hour' as const) : ('day' as const),
            points: buildTimelineFromAnalytics(
              currentAnalyticsData.timeline,
              currentAnalyticsData.granularity
            ),
          }
        : buildTimeline(statsRows, timeRange, customTimeRange),
    [currentAnalyticsData, customTimeRange, statsRows, timeRange]
  );
  const hourlyDistribution = useMemo(
    () =>
      currentAnalyticsData?.hourly_distribution
        ? buildHourlyDistributionFromAnalytics(currentAnalyticsData.hourly_distribution)
        : buildHourlyDistribution(statsRows),
    [currentAnalyticsData, statsRows]
  );
  const modelShareRows = useMemo(
    () =>
      currentAnalyticsData?.model_share
        ? buildModelShareRowsFromAnalytics(
            currentAnalyticsData.model_share,
            currentAnalyticsData.model_stats
          )
        : buildModelShareRows(statsRows),
    [currentAnalyticsData, statsRows]
  );
  const channelRows = useMemo(
    () =>
      currentAnalyticsData?.channel_share
        ? buildChannelRowsFromAnalytics(
            currentAnalyticsData.channel_share,
            authMetaMap,
            authFileMap,
            sourceInfoMap,
            channelByAuthIndex
          )
        : buildChannelRows(statsRows),
    [currentAnalyticsData, authFileMap, authMetaMap, channelByAuthIndex, sourceInfoMap, statsRows]
  );
  const modelRows = useMemo(
    () =>
      currentAnalyticsData?.model_stats
        ? buildModelRowsFromAnalytics(currentAnalyticsData.model_stats)
        : buildModelRows(statsRows),
    [currentAnalyticsData, statsRows]
  );
  const failureSourceRows = useMemo(
    () =>
      currentAnalyticsData?.failure_sources
        ? buildFailureSourceRowsFromAnalytics(
            currentAnalyticsData.failure_sources,
            authMetaMap,
            authFileMap,
            sourceInfoMap,
            channelByAuthIndex
          )
        : buildFailureSourceRows(statsRows),
    [currentAnalyticsData, authFileMap, authMetaMap, channelByAuthIndex, sourceInfoMap, statsRows]
  );
  const taskBuckets = useMemo(
    () =>
      currentAnalyticsData?.task_buckets
        ? buildTaskBucketsFromAnalytics(
            currentAnalyticsData.task_buckets,
            authMetaMap,
            authFileMap,
            sourceInfoMap,
            channelByAuthIndex
          )
        : buildTaskBuckets(statsRows),
    [currentAnalyticsData, authFileMap, authMetaMap, channelByAuthIndex, sourceInfoMap, statsRows]
  );
  const recentFailures = useMemo(
    () =>
      currentAnalyticsData?.recent_failures
        ? buildFailureRowsFromAnalytics(
            currentAnalyticsData.recent_failures,
            authMetaMap,
            authFileMap,
            sourceInfoMap,
            channelByAuthIndex
          )
        : buildFailureRows(statsRows),
    [currentAnalyticsData, authFileMap, authMetaMap, channelByAuthIndex, sourceInfoMap, statsRows]
  );
  const accountRows = useMemo(
    () =>
      currentAnalyticsData?.account_stats
        ? buildAccountRowsFromAnalytics(
            currentAnalyticsData.account_stats,
            authMetaMap,
            authFileMap,
            sourceInfoMap,
            channelByAuthIndex
          )
        : buildAccountRows(filteredRows),
    [
      currentAnalyticsData,
      authFileMap,
      authMetaMap,
      channelByAuthIndex,
      filteredRows,
      sourceInfoMap,
    ]
  );
  const apiKeyRows = useMemo(
    () =>
      currentAnalyticsData?.api_key_stats
        ? buildApiKeyRowsFromAnalytics(
            currentAnalyticsData.api_key_stats,
            authMetaMap,
            authFileMap,
            sourceInfoMap,
            channelByAuthIndex,
            apiKeyDisplayMap
          )
        : buildApiKeyRows(filteredRows, apiKeyDisplayMap),
    [
      apiKeyDisplayMap,
      currentAnalyticsData,
      authFileMap,
      authMetaMap,
      channelByAuthIndex,
      filteredRows,
      sourceInfoMap,
    ]
  );
  const fallbackFilterOptions = useMemo<MonitoringFilterOptions>(() => {
    const fallbackAccountRows = buildAccountRows(rangeFilteredRows);
    const fallbackApiKeyRows = buildApiKeyRows(rangeFilteredRows, apiKeyDisplayMap);
    return {
      accountRows: fallbackAccountRows,
      apiKeyRows: fallbackApiKeyRows,
      accountCount: fallbackAccountRows.length,
      apiKeyCount: fallbackApiKeyRows.length,
      providers: uniqueOptionValues(rangeFilteredRows.map((row) => row.provider)),
      models: uniqueOptionValues(rangeFilteredRows.map((row) => row.model)),
      channels: uniqueOptionValues(rangeFilteredRows.map((row) => row.channel)),
      headerTraceIds: uniqueOptionValues(rangeFilteredRows.map((row) => row.headerTraceId)),
    };
  },
    [apiKeyDisplayMap, rangeFilteredRows]
  );
  const analyticsFilterOptions =
    currentFilterSelectorsData?.filter_options ?? currentAnalyticsData?.filter_options;
  const filterOptions = useMemo(() => {
    const selectorOptions = analyticsFilterOptions
      ? buildFilterOptionsFromAnalytics(
          analyticsFilterOptions,
          authMetaMap,
          authFileMap,
          sourceInfoMap,
          channelByAuthIndex,
          apiKeyDisplayMap
        )
      : null;
    const mergedAccountRows = mergeMonitoringAccountOptionRows(
      accountRows,
      selectorOptions?.accountRows ?? [],
      fallbackFilterOptions.accountRows
    );
    const mergedApiKeyRows = mergeMonitoringApiKeyOptionRows(
      apiKeyRows,
      selectorOptions?.apiKeyRows ?? [],
      fallbackFilterOptions.apiKeyRows
    );
    return {
      accountRows: mergedAccountRows,
      apiKeyRows: mergedApiKeyRows,
      accountCount: analyticsFilterOptions
        ? (analyticsFilterOptions.account_count ?? selectorOptions?.accountRows.length ?? 0)
        : mergedAccountRows.length,
      apiKeyCount: analyticsFilterOptions
        ? (analyticsFilterOptions.api_key_count ?? selectorOptions?.apiKeyRows.length ?? 0)
        : mergedApiKeyRows.length,
      providers: uniqueOptionValues([
        ...(selectorOptions?.providers ?? []),
        ...fallbackFilterOptions.providers,
      ]),
      models: uniqueOptionValues([
        ...(selectorOptions?.models ?? []),
        ...fallbackFilterOptions.models,
      ]),
      channels: uniqueOptionValues([
        ...(selectorOptions?.channels ?? []),
        ...fallbackFilterOptions.channels,
      ]),
      headerTraceIds: uniqueOptionValues([
        ...(selectorOptions?.headerTraceIds ?? []),
        ...fallbackFilterOptions.headerTraceIds,
      ]),
    };
  }, [
    apiKeyDisplayMap,
    authFileMap,
    authMetaMap,
    accountRows,
    apiKeyRows,
    channelByAuthIndex,
    analyticsFilterOptions,
    fallbackFilterOptions,
    sourceInfoMap,
  ]);

  const computedPresentationSnapshot = useMemo<MonitoringPresentationSnapshot>(
    () => ({
      summary,
      timeline: timelineData.points,
      timelineGranularity: timelineData.granularity,
      hourlyDistribution,
      modelShareRows,
      channelRows,
      modelRows,
      failureSourceRows,
      taskBuckets,
      recentFailures,
      accountRows,
      apiKeyRows,
      filterOptions,
      filteredRows,
      eventsHasMore: displayEventsHasMore,
      eventsLoadingMore,
      eventsRetentionLimited,
      eventsTotalCount: displayEventsTotalCount,
      eventsLoadedCount,
      lastRefreshedAt: analytics.lastRefreshedAt,
    }),
    [
      analytics.lastRefreshedAt,
      accountRows,
      apiKeyRows,
      channelRows,
      displayEventsHasMore,
      displayEventsTotalCount,
      eventsLoadedCount,
      eventsLoadingMore,
      eventsRetentionLimited,
      failureSourceRows,
      filterOptions,
      filteredRows,
      hourlyDistribution,
      modelRows,
      modelShareRows,
      recentFailures,
      summary,
      taskBuckets,
      timelineData.granularity,
      timelineData.points,
    ]
  );

  useEffect(() => {
    if (analytics.dataStale) return;

    let cancelled = false;
    queueMicrotask(() => {
      if (cancelled) return;
      setPresentationSnapshotStore((previous) => {
        if (
          previous.lastStableSnapshot === computedPresentationSnapshot &&
          previous.cachedSnapshots.get(analyticsScopeKey) === computedPresentationSnapshot
        ) {
          return previous;
        }

        const cachedSnapshot = withoutMonitoringSnapshotEvents(computedPresentationSnapshot);
        const cachedSnapshots = new Map(previous.cachedSnapshots);
        cachedSnapshots.delete(analyticsScopeKey);
        cachedSnapshots.set(analyticsScopeKey, cachedSnapshot);
        while (cachedSnapshots.size > MONITORING_PRESENTATION_CACHE_LIMIT) {
          const oldestKey = cachedSnapshots.keys().next().value;
          if (oldestKey === undefined) break;
          cachedSnapshots.delete(oldestKey);
        }
        return {
          cachedSnapshots,
          lastStableSnapshot: cachedSnapshot,
        };
      });
    });
    return () => {
      cancelled = true;
    };
  }, [analytics.dataStale, analyticsScopeKey, computedPresentationSnapshot]);

  const presentationResolution = useMemo(
    () =>
      resolveMonitoringPresentationSnapshot({
        computedSnapshot: computedPresentationSnapshot,
        scopeKey: analyticsScopeKey,
        dataStale: analytics.dataStale,
        cachedSnapshots: presentationSnapshotStore.cachedSnapshots,
        lastStableSnapshot: presentationSnapshotStore.lastStableSnapshot,
      }),
    [
      analytics.dataStale,
      computedPresentationSnapshot,
      analyticsScopeKey,
      presentationSnapshotStore.cachedSnapshots,
      presentationSnapshotStore.lastStableSnapshot,
    ]
  );
  const presentationSnapshot = presentationResolution.snapshot;

  const metadata = useMemo<MonitoringMetadata>(() => {
    const planTypes = Array.from(
      new Set(uniqueAuthMeta.map((item) => item.planType).filter((item) => item && item !== '-'))
    ).sort();

    return {
      totalAuthFiles: authFiles.length,
      activeAuthFiles: uniqueAuthMeta.filter(
        (item) => !item.disabled && !item.unavailable && item.status === 'active'
      ).length,
      unavailableAuthFiles: uniqueAuthMeta.filter((item) => item.unavailable).length,
      runtimeOnlyAuthFiles: uniqueAuthMeta.filter((item) => item.runtimeOnly).length,
      totalChannels: channels.length,
      enabledChannels: channels.filter((item) => !item.disabled).length,
      configuredModels: Array.from(new Set(channels.flatMap((item) => item.modelNames))).length,
      planTypes,
    };
  }, [authFiles.length, channels, uniqueAuthMeta]);

  const statusChips = useMemo(() => buildStatusChips(metadata), [metadata]);

  return {
    loading: loading || analytics.loading,
    error: [error, analytics.error, filterSelectorsAnalytics.error].filter(Boolean).join('；'),
    authFiles,
    channels,
    summary: presentationSnapshot.summary,
    metadata,
    statusChips,
    timeline: presentationSnapshot.timeline,
    timelineGranularity: presentationSnapshot.timelineGranularity,
    hourlyDistribution: presentationSnapshot.hourlyDistribution,
    modelShareRows: presentationSnapshot.modelShareRows,
    channelRows: presentationSnapshot.channelRows,
    modelRows: presentationSnapshot.modelRows,
    failureSourceRows: presentationSnapshot.failureSourceRows,
    taskBuckets: presentationSnapshot.taskBuckets,
    recentFailures: presentationSnapshot.recentFailures,
    accountRows: presentationSnapshot.accountRows,
    apiKeyRows: presentationSnapshot.apiKeyRows,
    filterOptions: presentationSnapshot.filterOptions,
    filteredRows: presentationSnapshot.filteredRows,
    eventsHasMore: presentationSnapshot.eventsHasMore,
    eventsLoadingMore: presentationSnapshot.eventsLoadingMore,
    eventsRetentionLimited: presentationSnapshot.eventsRetentionLimited,
    eventsTotalCount: presentationSnapshot.eventsTotalCount,
    eventsLoadedCount: presentationSnapshot.eventsLoadedCount,
    lastRefreshedAt: presentationSnapshot.lastRefreshedAt,
    isTransitioningScope: analytics.dataStale,
    hasPresentationSnapshot: presentationResolution.hasPresentationSnapshot,
    refreshMeta,
    loadMoreEvents,
  };
}
