import {
  useCallback,
  useDeferredValue,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
} from 'react';
import { useLocation } from 'react-router-dom';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import {
  buildRealtimeMonitorRows,
  getRangeBounds,
  type MonitoringAccountRow,
  type MonitoringCustomTimeRange,
  type MonitoringStatusTone,
  type MonitoringTimeRange,
  useMonitoringData,
} from '@/features/monitoring/hooks/useMonitoringData';
import {
  ACCOUNT_OVERVIEW_CARD_PAGE_SIZE_OPTIONS,
  ACCOUNT_OVERVIEW_TABLE_PAGE_SIZE_OPTIONS,
  buildEmptyMonitoringStatusData,
  buildMonitoringAccountAuthStateMap,
  buildMonitoringAccountStatusDataMap,
  normalizeAccountOverviewPageSize,
  resolveMonitoringStatusRangeBounds,
  shouldClampAccountOverviewPage,
  shouldResetAccountOverviewPage,
  sortAccountRows,
  readAccountOverviewUiState,
  writeAccountOverviewUiState,
  type AccountDisplayMode,
  type AccountOverviewPageResetState,
  type AccountSortKey,
  type AccountSortState,
  type MonitoringAccountOverviewMode,
} from '@/features/monitoring/accountOverviewState';
import { buildMonitoringAccountQuotaTargetsByAccount } from '@/features/monitoring/accountOverviewQuotaTargets';
import {
  AccountExpandedDetails,
  AccountOverviewCard,
} from '@/features/monitoring/components/AccountOverviewCard';
import {
  AccountOverviewPanel,
  AccountOverviewPanelActions,
} from '@/features/monitoring/components/AccountOverviewPanel';
import {
  ApiKeySummaryPanel,
  ApiKeySummaryPanelActions,
} from '@/features/monitoring/components/ApiKeySummaryPanel';
import { MonitoringDataPanel } from '@/features/monitoring/components/MonitoringDataPanel';
import { MonitoringActionBar } from '@/features/monitoring/components/MonitoringActionBar';
import { MonitoringCustomRangeModal } from '@/features/monitoring/components/MonitoringCustomRangeModal';
import { MonitoringFiltersPanel } from '@/features/monitoring/components/MonitoringFiltersPanel';
import { UsageImportProgressModal } from '@/features/monitoring/components/UsageImportProgressModal';
import { usePageTransitionLayer } from '@/components/common/PageTransitionLayer';
import { IconInbox } from '@/components/ui/icons';
import {
  MonitoringStatusHeader,
  MonitoringStatusSummary,
} from '@/features/monitoring/components/MonitoringStatusHeader';
import { MonitoringSummarySection } from '@/features/monitoring/components/MonitoringSummarySection';
import type { MonitoringTab } from '@/features/monitoring/components/MonitoringTabsBar';
import {
  RealtimeEventsPanel,
  RealtimeEventsPanelActions,
} from '@/features/monitoring/components/RealtimeEventsPanel';
import { type AccountQuotaState } from '@/features/monitoring/components/accountOverviewPresentation';
import {
  buildAccountOptions,
  buildAccountOverviewColumns,
  buildAccountSortOptions,
  buildApiKeyOptionsFromRows,
  buildApiKeyOverviewColumns,
  buildAuthFilesByAuthIndex,
  buildAccountQuotaRefreshFailureEntry,
  buildObservedCodexAccountQuotaEntry,
  buildChannelOptionsFromValues,
  buildMonitoringInitialStateFromQuery,
  buildModelOptionsFromValues,
  buildPaginationState,
  buildPrimarySummaryCards,
  buildProviderOptionsFromValues,
  buildRealtimeLogRows,
  buildSecondarySummaryCards,
  buildStatusOptions,
  formatAccountOverviewScopeText,
  getCurrentInputValue,
  getTodayStartInputValue,
  isUsageImportFile,
  mergeObservedAccountQuotaState,
  parseDateTimeLocalValue,
  requestAccountQuota,
  type FocusSnapshot,
  type StatusFilter,
} from '@/features/monitoring/model/monitoringCenterPageModel';
import { resolveMonitoringDimensionCounts } from '@/features/monitoring/model/monitoringAnalyticsModel';
import { useUsageData } from '@/features/monitoring/hooks/useUsageData';
import {
  isUsageImportCancelledError,
  isUsageImportPausedError,
  type UsageImportProgress,
} from '@/features/monitoring/services/usageImportSession';
import { monitoringAnalyticsApi, type UsageHeaderSnapshot } from '@/services/api/usageService';
import {
  readMonitoringCenterUiState,
  writeMonitoringCenterUiState,
  type MonitoringDataTab,
} from '@/features/monitoring/monitoringCenterUiState';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useInterval } from '@/hooks/useInterval';
import { useRequestMonitoringAvailability } from '@/hooks/useRequestMonitoringAvailability';
import { isFileLogsAvailable } from '@/features/logs/logFeatureAvailability';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import type { StatusBarData } from '@/utils/recentRequests';
import { downloadBlob } from '@/utils/download';
import { sha256Hex } from '@/utils/apiKeyHash';
import { formatCompactNumber } from '@/utils/usage';
import {
  buildUsageHeaderSnapshotLookup,
  getHighConfidenceUsageHeaderSnapshotForAuthFile,
} from '@/utils/usageHeaderSnapshots';
import { buildSourceInfoMap, buildSourceProviderStateMap } from '@/utils/sourceResolver';
import styles from './MonitoringCenterPage.module.scss';

export { AccountExpandedDetails, AccountOverviewCard };

const DEFAULT_ACCOUNT_PAGE_SIZE = ACCOUNT_OVERVIEW_TABLE_PAGE_SIZE_OPTIONS[0];
const EMPTY_STATUS_BAR_DATA: StatusBarData = {
  blocks: [],
  blockDetails: [],
  successRate: 100,
  totalSuccess: 0,
  totalFailure: 0,
};

const shortLabel = (t: TFunction, shortKey: string, fallbackKey: string) => {
  const fallback = t(fallbackKey);
  const label = t(shortKey, { defaultValue: fallback });
  return label === shortKey ? fallback : label;
};

export function MonitoringCenterPage() {
  const { t, i18n } = useTranslation();
  const location = useLocation();
  const config = useConfigStore((state) => state.config);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const managementKey = useAuthStore((state) => state.managementKey);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const requestMonitoringAvailability = useRequestMonitoringAvailability();
  const pageTransitionLayer = usePageTransitionLayer();
  const isCurrentLayer = pageTransitionLayer ? pageTransitionLayer.status === 'current' : true;
  const initialAccountOverviewUiState = useRef(readAccountOverviewUiState());
  const initialMonitoringCenterUiState = useRef(
    buildMonitoringInitialStateFromQuery(location.search, readMonitoringCenterUiState())
  );
  const initialMonitoringDrilldownFilters = useRef(
    (() => {
      const params = new URLSearchParams(location.search);
      const minLatencyMs = Number(params.get('min_latency_ms'));
      return {
        authFile: params.get('auth_file')?.trim() || '',
        projectId: params.get('project_id')?.trim() || '',
        requestType: params.get('request_type')?.trim() || '',
        minLatencyMs: Number.isFinite(minLatencyMs) && minLatencyMs > 0 ? minLatencyMs : undefined,
        cacheStatus: params.get('cache_status')?.trim() || '',
      };
    })()
  );
  const [timeRange, setTimeRange] = useState<MonitoringTimeRange>(
    initialMonitoringCenterUiState.current.timeRange
  );
  const [customStartInput, setCustomStartInput] = useState(
    () => initialMonitoringCenterUiState.current.customStartInput || getTodayStartInputValue()
  );
  const [customEndInput, setCustomEndInput] = useState(
    () => initialMonitoringCenterUiState.current.customEndInput || getCurrentInputValue()
  );
  const [customDraftStartInput, setCustomDraftStartInput] = useState(
    () => initialMonitoringCenterUiState.current.customStartInput || getTodayStartInputValue()
  );
  const [customDraftEndInput, setCustomDraftEndInput] = useState(
    () => initialMonitoringCenterUiState.current.customEndInput || getCurrentInputValue()
  );
  const [searchInput, setSearchInput] = useState(
    () => initialMonitoringCenterUiState.current.searchInput
  );
  const [autoRefreshMs, setAutoRefreshMs] = useState(
    () => initialMonitoringCenterUiState.current.autoRefreshMs
  );
  const [documentVisible, setDocumentVisible] = useState(
    () => typeof document === 'undefined' || document.visibilityState !== 'hidden'
  );
  const [headerSnapshots, setHeaderSnapshots] = useState<UsageHeaderSnapshot[]>([]);
  const [selectedAccount, setSelectedAccount] = useState(
    () => initialMonitoringCenterUiState.current.selectedAccount
  );
  const [selectedProvider, setSelectedProvider] = useState(
    () => initialMonitoringCenterUiState.current.selectedProvider
  );
  const [selectedModel, setSelectedModel] = useState(
    () => initialMonitoringCenterUiState.current.selectedModel
  );
  const [selectedChannel, setSelectedChannel] = useState(
    () => initialMonitoringCenterUiState.current.selectedChannel
  );
  const [selectedApiKeyHash, setSelectedApiKeyHash] = useState(
    () => initialMonitoringCenterUiState.current.selectedApiKeyHash
  );
  const [selectedHeaderTraceId, setSelectedHeaderTraceId] = useState(
    () => initialMonitoringCenterUiState.current.selectedHeaderTraceId
  );
  const [selectedStatus, setSelectedStatus] = useState<StatusFilter>(
    () => initialMonitoringCenterUiState.current.selectedStatus
  );
  const [drilldownAuthFile, setDrilldownAuthFile] = useState(
    () => initialMonitoringDrilldownFilters.current.authFile
  );
  const [drilldownProjectId, setDrilldownProjectId] = useState(
    () => initialMonitoringDrilldownFilters.current.projectId
  );
  const [drilldownRequestType, setDrilldownRequestType] = useState(
    () => initialMonitoringDrilldownFilters.current.requestType
  );
  const [drilldownMinLatencyMs, setDrilldownMinLatencyMs] = useState(
    () => initialMonitoringDrilldownFilters.current.minLatencyMs
  );
  const [drilldownCacheStatus, setDrilldownCacheStatus] = useState(
    () => initialMonitoringDrilldownFilters.current.cacheStatus
  );
  const [expandedAccounts, setExpandedAccounts] = useState<Record<string, boolean>>({});
  const [expandedApiKeys, setExpandedApiKeys] = useState<Record<string, boolean>>({});
  const [focusedAccount, setFocusedAccount] = useState<string | null>(null);
  const [isCustomRangeModalOpen, setIsCustomRangeModalOpen] = useState(false);
  const [usageExporting, setUsageExporting] = useState(false);
  const [usageImporting, setUsageImporting] = useState(false);
  const [usageImportCancelling, setUsageImportCancelling] = useState(false);
  const [usageImportTask, setUsageImportTask] = useState<{
    file: File;
    progress: UsageImportProgress;
  } | null>(null);
  const [accountQuotaStates, setAccountQuotaStates] = useState<Record<string, AccountQuotaState>>(
    {}
  );
  const [activeDataTab, setActiveDataTab] = useState<MonitoringDataTab>(
    initialMonitoringCenterUiState.current.activeDataTab
  );
  const [accountOverviewMode, setAccountOverviewMode] = useState<MonitoringAccountOverviewMode>(
    initialAccountOverviewUiState.current.mode
  );
  const [accountDisplayMode, setAccountDisplayMode] = useState<AccountDisplayMode>(
    initialAccountOverviewUiState.current.accountDisplayMode
  );
  const [accountSort, setAccountSort] = useState<AccountSortState>(
    initialAccountOverviewUiState.current.sort
  );
  const [accountPageByMode, setAccountPageByMode] = useState(() => ({
    table: 1,
    card: initialAccountOverviewUiState.current.cardPagination.page,
  }));
  const [accountPageSizeByMode, setAccountPageSizeByMode] = useState(() => ({
    table: DEFAULT_ACCOUNT_PAGE_SIZE,
    card: initialAccountOverviewUiState.current.cardPagination.pageSize,
  }));
  const [apiKeyPage, setApiKeyPage] = useState(1);
  const [apiKeyPageSize, setApiKeyPageSize] = useState<number>(
    initialMonitoringCenterUiState.current.apiKeyPageSize
  );
  const [realtimePage, setRealtimePage] = useState(1);
  const [realtimePageSize, setRealtimePageSize] = useState(
    initialMonitoringCenterUiState.current.realtimePageSize
  );
  const focusSnapshotRef = useRef<FocusSnapshot | null>(null);
  const previousAccountPageResetStateRef = useRef<AccountOverviewPageResetState | null>(null);
  const accountQuotaStatesRef = useRef<Record<string, AccountQuotaState>>({});
  const accountQuotaRequestIdsRef = useRef<Record<string, number>>({});
  const usageImportInputRef = useRef<HTMLInputElement | null>(null);
  const usageImportAbortRef = useRef<AbortController | null>(null);
  const usageImportCancelPendingRef = useRef(false);
  const usageImportRunIDRef = useRef(0);
  const deferredSearch = useDeferredValue(searchInput);
  const deferredSearchApiKeyHash = useMemo(() => sha256Hex(deferredSearch), [deferredSearch]);
  const accountPage =
    accountOverviewMode === 'card' ? accountPageByMode.card : accountPageByMode.table;
  const accountPageSize =
    accountOverviewMode === 'card' ? accountPageSizeByMode.card : accountPageSizeByMode.table;
  const customStartMs = useMemo(
    () => parseDateTimeLocalValue(customStartInput),
    [customStartInput]
  );
  const customEndMs = useMemo(() => parseDateTimeLocalValue(customEndInput), [customEndInput]);
  const customDraftStartMs = useMemo(
    () => parseDateTimeLocalValue(customDraftStartInput),
    [customDraftStartInput]
  );
  const customDraftEndMs = useMemo(
    () => parseDateTimeLocalValue(customDraftEndInput),
    [customDraftEndInput]
  );
  const customTimeRangeError = useMemo(() => {
    if (timeRange !== 'custom') return '';
    if (customStartMs === null || customEndMs === null) {
      return t('monitoring.custom_range_required');
    }
    if (customStartMs > customEndMs) {
      return t('monitoring.custom_range_invalid');
    }
    return '';
  }, [customEndMs, customStartMs, t, timeRange]);
  const customTimeRange = useMemo<MonitoringCustomTimeRange | null>(() => {
    if (
      timeRange !== 'custom' ||
      customTimeRangeError ||
      customStartMs === null ||
      customEndMs === null
    ) {
      return null;
    }
    return {
      startMs: customStartMs,
      endMs: customEndMs,
    };
  }, [customEndMs, customStartMs, customTimeRangeError, timeRange]);
  const customDraftTimeRangeError = useMemo(() => {
    if (customDraftStartMs === null || customDraftEndMs === null) {
      return t('monitoring.custom_range_required');
    }
    if (customDraftStartMs > customDraftEndMs) {
      return t('monitoring.custom_range_invalid');
    }
    return '';
  }, [customDraftEndMs, customDraftStartMs, t]);

  const {
    loading: usageLoading,
    error: usageError,
    modelPrices,
    apiKeyAliases,
    loadApiKeyAliases,
    exportUsage,
    importUsage,
    cancelUsageImport,
  } = useUsageData({ loadUsageEvents: false });

  const monitoringScopeFilters = useMemo(
    () => ({
      account: selectedAccount,
      provider: selectedProvider,
      authFile: drilldownAuthFile || undefined,
      projectId: drilldownProjectId || undefined,
      requestType: drilldownRequestType || undefined,
      minLatencyMs: drilldownMinLatencyMs,
      cacheStatus: drilldownCacheStatus || undefined,
      model: selectedModel,
      channel: selectedChannel,
      apiKeyHash: selectedApiKeyHash,
      headerTraceId: selectedHeaderTraceId,
      status: selectedStatus,
    }),
    [
      drilldownAuthFile,
      drilldownCacheStatus,
      drilldownMinLatencyMs,
      drilldownProjectId,
      drilldownRequestType,
      selectedAccount,
      selectedApiKeyHash,
      selectedChannel,
      selectedHeaderTraceId,
      selectedModel,
      selectedProvider,
      selectedStatus,
    ]
  );

  const {
    loading: monitoringLoading,
    error: monitoringError,
    authFiles,
    summary: monitoringSummary,
    accountRows: monitoringAccountRows,
    apiKeyRows: monitoringApiKeyRows,
    filterOptions: monitoringFilterOptions,
    filteredRows,
    eventsHasMore,
    eventsLoadingMore,
    eventsRetentionLimited,
    eventsTotalCount,
    eventsLoadedCount,
    lastRefreshedAt: monitoringLastRefreshedAt,
    isTransitioningScope: monitoringScopeTransitioning,
    hasPresentationSnapshot: hasMonitoringPresentationSnapshot,
    refreshMeta,
    loadMoreEvents,
  } = useMonitoringData({
    config,
    modelPrices,
    apiKeyAliases,
    timeRange,
    customTimeRange,
    searchQuery: deferredSearch,
    searchApiKeyHash: deferredSearchApiKeyHash,
    scopeFilters: monitoringScopeFilters,
    activeDataTab,
  });

  const loadHeaderSnapshots = useCallback(async () => {
    if (!requestMonitoringAvailability.serviceBase) {
      setHeaderSnapshots([]);
      return;
    }
    try {
      const response = await monitoringAnalyticsApi.getHeaderSnapshots(
        requestMonitoringAvailability.serviceBase,
        managementKey,
        { days: 30, limit: 1000 }
      );
      setHeaderSnapshots(response.items ?? []);
    } catch {
      setHeaderSnapshots((current) => current);
    }
  }, [managementKey, requestMonitoringAvailability.serviceBase]);

  const refreshAll = useCallback(async () => {
    await Promise.all([loadApiKeyAliases(), refreshMeta(false), loadHeaderSnapshots()]);
  }, [loadApiKeyAliases, loadHeaderSnapshots, refreshMeta]);

  const setCurrentAccountPage = useCallback(
    (page: number) => {
      setAccountPageByMode((previous) => ({
        ...previous,
        [accountOverviewMode]: page,
      }));
    },
    [accountOverviewMode]
  );

  const resetCurrentAccountPage = useCallback(() => {
    setCurrentAccountPage(1);
  }, [setCurrentAccountPage]);

  useHeaderRefresh(refreshAll, isCurrentLayer);
  useEffect(() => {
    if (typeof document === 'undefined') return;
    const updateVisibility = () => setDocumentVisible(document.visibilityState !== 'hidden');
    document.addEventListener('visibilitychange', updateVisibility);
    return () => document.removeEventListener('visibilitychange', updateVisibility);
  }, []);
  useInterval(
    () => {
      void refreshAll().catch(() => {});
    },
    isCurrentLayer &&
      documentVisible &&
      connectionStatus === 'connected' &&
      Number(autoRefreshMs) > 0
      ? Number(autoRefreshMs)
      : null
  );

  useEffect(() => {
    if (!isCurrentLayer || !requestMonitoringAvailability.serviceBase) return;
    void loadHeaderSnapshots();
  }, [isCurrentLayer, loadHeaderSnapshots, requestMonitoringAvailability.serviceBase]);

  const monitoringUnavailable =
    !requestMonitoringAvailability.checking && !requestMonitoringAvailability.available;
  const usageTransferAvailable = requestMonitoringAvailability.available;
  const monitoringUnavailableTitle =
    requestMonitoringAvailability.reason === 'monitoring_disabled'
      ? t('monitoring.request_monitoring_disabled_title')
      : t('monitoring.request_monitoring_unavailable_title');
  const monitoringUnavailableBody =
    requestMonitoringAvailability.reason === 'monitoring_disabled'
      ? t('monitoring.request_monitoring_disabled_body')
      : requestMonitoringAvailability.reason === 'service_unavailable'
        ? t('monitoring.request_monitoring_service_unavailable_body')
        : t('monitoring.request_monitoring_not_configured_body');
  const monitoringBlockingLoading =
    monitoringLoading && (!monitoringScopeTransitioning || !hasMonitoringPresentationSnapshot);
  const overallLoading =
    usageLoading || monitoringBlockingLoading || requestMonitoringAvailability.checking;
  const combinedError = monitoringUnavailable
    ? monitoringError
    : [usageError, monitoringError].filter(Boolean).join('；');
  const hasPrices = Object.keys(modelPrices).length > 0;

  useEffect(() => {
    accountQuotaStatesRef.current = accountQuotaStates;
  }, [accountQuotaStates]);

  useEffect(() => {
    writeAccountOverviewUiState({
      mode: accountOverviewMode,
      accountDisplayMode,
      sort: accountSort,
      cardPagination: {
        page: accountPageByMode.card,
        pageSize: accountPageSizeByMode.card,
      },
    });
  }, [
    accountDisplayMode,
    accountOverviewMode,
    accountPageByMode.card,
    accountPageSizeByMode.card,
    accountSort,
  ]);

  useEffect(() => {
    writeMonitoringCenterUiState({
      activeDataTab,
      timeRange,
      customStartInput,
      customEndInput,
      searchInput,
      autoRefreshMs,
      selectedAccount,
      selectedProvider,
      selectedModel,
      selectedChannel,
      selectedApiKeyHash,
      selectedHeaderTraceId,
      selectedStatus,
      apiKeyPageSize,
      realtimePageSize,
    });
  }, [
    activeDataTab,
    apiKeyPageSize,
    autoRefreshMs,
    customEndInput,
    customStartInput,
    realtimePageSize,
    searchInput,
    selectedAccount,
    selectedApiKeyHash,
    selectedChannel,
    selectedHeaderTraceId,
    selectedModel,
    selectedProvider,
    selectedStatus,
    timeRange,
  ]);

  const providerOptions = useMemo(
    () => buildProviderOptionsFromValues(monitoringFilterOptions.providers, selectedProvider, t),
    [monitoringFilterOptions.providers, selectedProvider, t]
  );

  const accountOptions = useMemo(
    () =>
      buildAccountOptions(
        monitoringFilterOptions.accountRows,
        selectedAccount,
        t,
        accountDisplayMode
      ),
    [accountDisplayMode, monitoringFilterOptions.accountRows, selectedAccount, t]
  );

  const modelOptions = useMemo(
    () => buildModelOptionsFromValues(monitoringFilterOptions.models, selectedModel, t),
    [monitoringFilterOptions.models, selectedModel, t]
  );

  const channelOptions = useMemo(
    () => buildChannelOptionsFromValues(monitoringFilterOptions.channels, selectedChannel, t),
    [monitoringFilterOptions.channels, selectedChannel, t]
  );

  const apiKeyOptions = useMemo(
    () => buildApiKeyOptionsFromRows(monitoringFilterOptions.apiKeyRows, selectedApiKeyHash, t),
    [monitoringFilterOptions.apiKeyRows, selectedApiKeyHash, t]
  );

  const statusOptions = useMemo(() => buildStatusOptions(t), [t]);

  const authFilesByAuthIndex = useMemo(() => buildAuthFilesByAuthIndex(authFiles), [authFiles]);
  const accountSourceProviderStateBySourceKey = useMemo(
    () =>
      buildSourceProviderStateMap(
        buildSourceInfoMap({
          geminiApiKeys: config?.geminiApiKeys || [],
          claudeApiKeys: config?.claudeApiKeys || [],
          codexApiKeys: config?.codexApiKeys || [],
          xaiApiKeys: config?.xaiApiKeys || [],
          vertexApiKeys: config?.vertexApiKeys || [],
          openaiCompatibility: config?.openaiCompatibility || [],
        })
      ),
    [config]
  );

  const scopedRows = filteredRows;
  const scopedStatsRows = useMemo(
    () => scopedRows.filter((row) => row.statsIncluded),
    [scopedRows]
  );
  const accountStatusNowMs = monitoringLastRefreshedAt?.getTime() ?? Date.now();
  const accountStatusBounds = useMemo(
    () => getRangeBounds(timeRange, accountStatusNowMs, customTimeRange),
    [accountStatusNowMs, customTimeRange, timeRange]
  );
  const accountOverviewScopeText = useMemo(
    () => formatAccountOverviewScopeText(accountStatusBounds, i18n.language, t),
    [accountStatusBounds, i18n.language, t]
  );

  const scopedSummary = monitoringSummary;
  const accountRows = monitoringAccountRows;
  const apiKeyRows = monitoringApiKeyRows;
  const { accountCount, apiKeyCount } = useMemo(
    () =>
      resolveMonitoringDimensionCounts({
        activeDataTab,
        accountRowCount: accountRows.length,
        apiKeyRowCount: apiKeyRows.length,
        accountSelectorCount:
          monitoringFilterOptions.accountCount ?? monitoringFilterOptions.accountRows.length,
        apiKeySelectorCount:
          monitoringFilterOptions.apiKeyCount ?? monitoringFilterOptions.apiKeyRows.length,
      }),
    [
      accountRows.length,
      activeDataTab,
      apiKeyRows.length,
      monitoringFilterOptions.accountCount,
      monitoringFilterOptions.accountRows.length,
      monitoringFilterOptions.apiKeyCount,
      monitoringFilterOptions.apiKeyRows.length,
    ]
  );
  const accountStatusDataByRowId = useMemo(
    () => buildMonitoringAccountStatusDataMap(scopedRows, accountStatusBounds),
    [accountStatusBounds, scopedRows]
  );
  const emptyAccountStatusData = useMemo(() => {
    const resolvedBounds = resolveMonitoringStatusRangeBounds(scopedRows, accountStatusBounds);
    return resolvedBounds ? buildEmptyMonitoringStatusData(resolvedBounds) : EMPTY_STATUS_BAR_DATA;
  }, [accountStatusBounds, scopedRows]);
  const accountAuthStateByRowId = useMemo(
    () =>
      buildMonitoringAccountAuthStateMap(
        accountRows,
        authFilesByAuthIndex,
        accountSourceProviderStateBySourceKey
      ),
    [accountRows, accountSourceProviderStateBySourceKey, authFilesByAuthIndex]
  );
  const sortedAccountRows = useMemo(
    () => sortAccountRows(accountRows, accountSort),
    [accountRows, accountSort]
  );
  const groupedRealtimeRows = useMemo(
    () => buildRealtimeMonitorRows(scopedStatsRows),
    [scopedStatsRows]
  );
  const realtimeLogRows = useMemo(() => buildRealtimeLogRows(scopedRows), [scopedRows]);
  const accountPagination = useMemo(
    () => buildPaginationState(sortedAccountRows, accountPage, accountPageSize),
    [accountPage, accountPageSize, sortedAccountRows]
  );
  const apiKeyPagination = useMemo(
    () => buildPaginationState(apiKeyRows, apiKeyPage, apiKeyPageSize),
    [apiKeyPage, apiKeyPageSize, apiKeyRows]
  );
  const realtimePagination = useMemo(
    () => buildPaginationState(realtimeLogRows, realtimePage, realtimePageSize),
    [realtimeLogRows, realtimePage, realtimePageSize]
  );
  const accountPageResetState = useMemo<AccountOverviewPageResetState>(
    () => ({
      customEndInput,
      customStartInput,
      deferredSearch,
      selectedAccount,
      selectedApiKeyHash,
      selectedChannel,
      selectedHeaderTraceId,
      selectedModel,
      selectedProvider,
      selectedStatus,
      timeRange,
    }),
    [
      customEndInput,
      customStartInput,
      deferredSearch,
      selectedAccount,
      selectedApiKeyHash,
      selectedChannel,
      selectedHeaderTraceId,
      selectedModel,
      selectedProvider,
      selectedStatus,
      timeRange,
    ]
  );

  useEffect(() => {
    if (
      shouldResetAccountOverviewPage(
        previousAccountPageResetStateRef.current,
        accountPageResetState
      )
    ) {
      if (monitoringScopeTransitioning && hasMonitoringPresentationSnapshot) {
        return;
      }
      resetCurrentAccountPage();
      setApiKeyPage(1);
      setRealtimePage(1);
    }

    previousAccountPageResetStateRef.current = accountPageResetState;
  }, [
    accountPageResetState,
    hasMonitoringPresentationSnapshot,
    monitoringScopeTransitioning,
    resetCurrentAccountPage,
  ]);

  useEffect(() => {
    if (
      !shouldClampAccountOverviewPage(overallLoading, accountPage, accountPagination.currentPage)
    ) {
      return;
    }

    setCurrentAccountPage(accountPagination.currentPage);
  }, [accountPage, accountPagination.currentPage, overallLoading, setCurrentAccountPage]);

  const accountQuotaTargetsByAccount = useMemo(
    () => buildMonitoringAccountQuotaTargetsByAccount(accountRows, accountAuthStateByRowId),
    [accountAuthStateByRowId, accountRows]
  );
  const headerSnapshotLookup = useMemo(
    () => buildUsageHeaderSnapshotLookup(headerSnapshots),
    [headerSnapshots]
  );
  const scopedFailureCount = scopedSummary.failureCalls;
  const accountQuotaStatesWithObservedHeaders = useMemo(() => {
    let changed = false;
    const nextStates = Object.fromEntries(
      Object.entries(accountQuotaStates).map(([account, state]) => {
        const targets = accountQuotaTargetsByAccount.get(account) ?? [];
        const observedEntries = targets
          .map((target) =>
            buildObservedCodexAccountQuotaEntry(
              target,
              getHighConfidenceUsageHeaderSnapshotForAuthFile(headerSnapshotLookup, target.file),
              t
            )
          )
          .filter((entry): entry is NonNullable<typeof entry> => Boolean(entry));
        const nextState = mergeObservedAccountQuotaState(state, targets, observedEntries) ?? state;
        changed = changed || nextState !== state;
        return [account, nextState] as const;
      })
    );
    return changed ? nextStates : accountQuotaStates;
  }, [accountQuotaStates, accountQuotaTargetsByAccount, headerSnapshotLookup, t]);

  const hasSearchFilter = Boolean(deferredSearch.trim());
  const hasScopeFilter =
    selectedAccount !== 'all' ||
    selectedProvider !== 'all' ||
    selectedModel !== 'all' ||
    selectedChannel !== 'all' ||
    selectedApiKeyHash !== 'all' ||
    selectedHeaderTraceId !== 'all' ||
    selectedStatus !== 'all' ||
    Boolean(drilldownAuthFile) ||
    Boolean(drilldownProjectId) ||
    Boolean(drilldownRequestType) ||
    Boolean(drilldownMinLatencyMs) ||
    Boolean(drilldownCacheStatus);
  const hasActiveDataFilter = hasSearchFilter || hasScopeFilter;
  const failedGroupCount = groupedRealtimeRows.filter((row) => row.failureCalls > 0).length;
  const failedOnlyActive = selectedStatus === 'failed';
  const hasMonitoringDisplayData =
    scopedSummary.totalCalls > 0 ||
    accountRows.length > 0 ||
    apiKeyRows.length > 0 ||
    filteredRows.length > 0;
  const connectionTone: MonitoringStatusTone =
    connectionStatus === 'connected' ? 'good' : connectionStatus === 'connecting' ? 'warn' : 'bad';
  const connectionLabel =
    connectionStatus === 'connected'
      ? t('common.connected_status')
      : connectionStatus === 'connecting'
        ? t('common.connecting_status')
        : connectionStatus === 'error'
          ? t('common.error')
          : t('common.disconnected_status');

  const accountOverviewColumns = useMemo(() => buildAccountOverviewColumns(t), [t]);

  const apiKeyOverviewColumns = useMemo(() => buildApiKeyOverviewColumns(t), [t]);

  const accountSortOptions = useMemo(
    () => buildAccountSortOptions(accountOverviewColumns, t),
    [accountOverviewColumns, t]
  );

  const accountPageSizeOptions =
    accountOverviewMode === 'card'
      ? ACCOUNT_OVERVIEW_CARD_PAGE_SIZE_OPTIONS
      : ACCOUNT_OVERVIEW_TABLE_PAGE_SIZE_OPTIONS;

  const primarySummaryCards = useMemo(
    () =>
      buildPrimarySummaryCards({
        summary: scopedSummary,
        accountCount,
        failedGroupCount,
        hasPrices,
        locale: i18n.language,
        t,
      }),
    [accountCount, failedGroupCount, hasPrices, i18n.language, scopedSummary, t]
  );

  const secondarySummaryCards = useMemo(
    () => buildSecondarySummaryCards(scopedSummary, i18n.language, t),
    [i18n.language, scopedSummary, t]
  );

  const dataTabs = useMemo<MonitoringTab<MonitoringDataTab>[]>(() => {
    const totalCalls = scopedSummary.totalCalls;
    const failureCount = scopedFailureCount;
    const realtimeHasFailure = failureCount > 0;
    const realtimeBadge = realtimeHasFailure ? failureCount : formatCompactNumber(totalCalls);
    return [
      {
        id: 'accounts',
        label: shortLabel(t, 'monitoring.data_tab_accounts_short', 'monitoring.data_tab_accounts'),
        fullLabel: t('monitoring.data_tab_accounts'),
        icon: 'accounts',
        badge: accountCount,
        badgeTitle: t('monitoring.data_tab_accounts_badge_title', { count: accountCount }),
      },
      {
        id: 'apiKeys',
        label: shortLabel(t, 'monitoring.data_tab_api_keys_short', 'monitoring.data_tab_api_keys'),
        fullLabel: t('monitoring.data_tab_api_keys'),
        icon: 'apiKeys',
        badge: apiKeyCount,
        badgeTitle: t('monitoring.data_tab_api_keys_badge_title', { count: apiKeyCount }),
      },
      {
        id: 'realtime',
        label: shortLabel(t, 'monitoring.data_tab_realtime_short', 'monitoring.data_tab_realtime'),
        fullLabel: t('monitoring.data_tab_realtime'),
        icon: 'realtime',
        badge: realtimeBadge,
        badgeTone: realtimeHasFailure ? 'failure' : 'default',
        badgeTitle: t('monitoring.data_tab_realtime_badge_title', {
          failed: failureCount,
          total: totalCalls,
        }),
      },
    ];
  }, [accountCount, apiKeyCount, scopedFailureCount, scopedSummary.totalCalls, t]);

  const handleDataTabChange = useCallback((tab: MonitoringDataTab) => {
    setActiveDataTab(tab);
  }, []);

  const restoreFocusSnapshot = useCallback(() => {
    const snapshot = focusSnapshotRef.current;
    focusSnapshotRef.current = null;
    setFocusedAccount(null);

    if (!snapshot) {
      setSelectedAccount('all');
      return;
    }

    setSearchInput(snapshot.searchInput);
    setSelectedAccount(snapshot.selectedAccount);
    setSelectedProvider(snapshot.selectedProvider);
    setSelectedModel(snapshot.selectedModel);
    setSelectedChannel(snapshot.selectedChannel);
    setSelectedApiKeyHash(snapshot.selectedApiKeyHash);
    setSelectedHeaderTraceId(snapshot.selectedHeaderTraceId);
    setSelectedStatus(snapshot.selectedStatus);
  }, []);

  const clearFilters = useCallback(() => {
    focusSnapshotRef.current = null;
    setFocusedAccount(null);
    setSearchInput('');
    setSelectedAccount('all');
    setSelectedProvider('all');
    setSelectedModel('all');
    setSelectedChannel('all');
    setSelectedApiKeyHash('all');
    setSelectedHeaderTraceId('all');
    setSelectedStatus('all');
    setDrilldownAuthFile('');
    setDrilldownProjectId('');
    setDrilldownRequestType('');
    setDrilldownMinLatencyMs(undefined);
    setDrilldownCacheStatus('');
  }, []);

  const renderMonitoringEmptyState = () => (
    <div className={styles.emptyState}>
      <IconInbox size={48} className={styles.emptyStateIcon} aria-hidden="true" />
      <strong className={styles.emptyStateTitle}>
        {hasActiveDataFilter ? t('monitoring.no_filtered_data') : t('monitoring.no_data')}
      </strong>
      {!hasActiveDataFilter ? (
        <details className={styles.emptyStateDetails}>
          <summary className={styles.emptyStateSummary}>
            {t('monitoring.empty_diagnostics_link')}
          </summary>
          <span className={styles.emptyStateBody}>{t('monitoring.empty_diagnostics_body')}</span>
        </details>
      ) : null}
    </div>
  );

  const openCustomRangeModal = useCallback(() => {
    setCustomDraftStartInput(customStartInput || getTodayStartInputValue());
    setCustomDraftEndInput(customEndInput || getCurrentInputValue());
    setIsCustomRangeModalOpen(true);
  }, [customEndInput, customStartInput]);

  const handleTimeRangeChange = useCallback(
    (range: MonitoringTimeRange) => {
      if (range === 'custom') {
        openCustomRangeModal();
        return;
      }
      setIsCustomRangeModalOpen(false);
      setTimeRange(range);
    },
    [openCustomRangeModal]
  );

  const handleCustomDraftStartChange = useCallback((event: ChangeEvent<HTMLInputElement>) => {
    setCustomDraftStartInput(event.target.value);
  }, []);

  const handleCustomDraftEndChange = useCallback((event: ChangeEvent<HTMLInputElement>) => {
    setCustomDraftEndInput(event.target.value);
  }, []);

  const applyCustomTimeRange = useCallback(() => {
    if (customDraftTimeRangeError) return;
    setCustomStartInput(customDraftStartInput);
    setCustomEndInput(customDraftEndInput);
    setTimeRange('custom');
    setIsCustomRangeModalOpen(false);
  }, [customDraftEndInput, customDraftStartInput, customDraftTimeRangeError]);

  const toggleFailedOnly = useCallback(() => {
    setSelectedStatus((previous) => (previous === 'failed' ? 'all' : 'failed'));
  }, []);

  const toggleApiKeyExpanded = useCallback((apiKeyId: string) => {
    setExpandedApiKeys((previous) => ({
      ...previous,
      [apiKeyId]: !previous[apiKeyId],
    }));
  }, []);

  const loadAccountQuota = useCallback(
    async (account: string, force: boolean = false) => {
      const currentState = accountQuotaStatesRef.current[account];
      const targets = accountQuotaTargetsByAccount.get(account) ?? [];
      const targetKey = targets.map((target) => target.key).join('|');
      const previousEntriesByKey =
        currentState?.targetKey === targetKey
          ? new Map(currentState.entries.map((entry) => [entry.key, entry]))
          : new Map();
      const observedEntries = targets
        .map((target) =>
          buildObservedCodexAccountQuotaEntry(
            target,
            getHighConfidenceUsageHeaderSnapshotForAuthFile(headerSnapshotLookup, target.file),
            t
          )
        )
        .filter((entry): entry is NonNullable<typeof entry> => Boolean(entry));
      if (
        !force &&
        currentState &&
        currentState.status !== 'idle' &&
        currentState.targetKey === targetKey
      ) {
        return;
      }

      const requestId = (accountQuotaRequestIdsRef.current[account] ?? 0) + 1;
      accountQuotaRequestIdsRef.current[account] = requestId;

      setAccountQuotaStates((previous) => ({
        ...previous,
        [account]: {
          status: 'loading',
          targetKey,
          entries:
            previous[account]?.targetKey === targetKey
              ? (previous[account]?.entries ?? observedEntries)
              : observedEntries,
          lastRefreshedAt: previous[account]?.lastRefreshedAt,
        },
      }));

      if (targets.length === 0) {
        if (accountQuotaRequestIdsRef.current[account] !== requestId) return;
        setAccountQuotaStates((previous) => ({
          ...previous,
          [account]: {
            status: 'success',
            targetKey,
            entries: [],
            lastRefreshedAt: Date.now(),
          },
        }));
        return;
      }

      const settled = await Promise.allSettled(
        targets.map((target) => requestAccountQuota(target, t))
      );
      if (accountQuotaRequestIdsRef.current[account] !== requestId) return;

      const hasFailure = settled.some((result) => result.status === 'rejected');
      const completedAtMs = Date.now();
      const entries = settled.map((result, index) => {
        const fallback = targets[index];
        if (result.status === 'fulfilled') {
          return result.value;
        }

        const error =
          result.reason instanceof Error
            ? result.reason.message
            : String(result.reason || t('common.unknown_error'));
        const observedEntry = buildObservedCodexAccountQuotaEntry(
          fallback,
          getHighConfidenceUsageHeaderSnapshotForAuthFile(headerSnapshotLookup, fallback.file),
          t
        );
        return buildAccountQuotaRefreshFailureEntry(
          fallback,
          error,
          t,
          previousEntriesByKey.get(fallback.key),
          observedEntry,
          completedAtMs
        );
      });

      const hasSuccess = entries.some((entry) => !entry.error);
      const firstError = entries.find((entry) => entry.error)?.error;
      setAccountQuotaStates((previous) => ({
        ...previous,
        [account]: {
          status: hasFailure ? 'error' : hasSuccess ? 'success' : 'error',
          targetKey,
          entries,
          error: hasFailure ? firstError || t('common.unknown_error') : '',
          failedAtMs: hasFailure ? completedAtMs : undefined,
          lastRefreshedAt: hasFailure ? previous[account]?.lastRefreshedAt : completedAtMs,
        },
      }));
    },
    [accountQuotaTargetsByAccount, headerSnapshotLookup, t]
  );

  const toggleAccountExpanded = useCallback(
    (accountId: string, account: string) => {
      if (!expandedAccounts[accountId]) {
        void loadAccountQuota(account);
      }
      setExpandedAccounts((previous) => ({
        ...previous,
        [accountId]: !previous[accountId],
      }));
    },
    [expandedAccounts, loadAccountQuota]
  );

  const focusAccount = useCallback(
    (row: MonitoringAccountRow) => {
      const account = row.account;
      const accountFilterValue = row.filterValue || row.account;
      if (focusedAccount === account) {
        restoreFocusSnapshot();
        return;
      }

      if (!focusSnapshotRef.current) {
        focusSnapshotRef.current = {
          searchInput,
          selectedAccount,
          selectedProvider,
          selectedModel,
          selectedChannel,
          selectedApiKeyHash,
          selectedHeaderTraceId,
          selectedStatus,
        };
      }

      setFocusedAccount(account);
      setSelectedAccount(accountFilterValue);
    },
    [
      focusedAccount,
      restoreFocusSnapshot,
      searchInput,
      selectedAccount,
      selectedApiKeyHash,
      selectedChannel,
      selectedHeaderTraceId,
      selectedModel,
      selectedProvider,
      selectedStatus,
    ]
  );

  const handleAccountFilterChange = useCallback(
    (value: string) => {
      setSelectedAccount(value);

      if (focusedAccount && value !== focusedAccount) {
        focusSnapshotRef.current = null;
        setFocusedAccount(null);
      }
    },
    [focusedAccount]
  );

  const handleAccountPageSizeChange = useCallback(
    (pageSize: number) => {
      setAccountPageSizeByMode((previous) => ({
        ...previous,
        [accountOverviewMode]: normalizeAccountOverviewPageSize(pageSize, accountOverviewMode),
      }));
      resetCurrentAccountPage();
    },
    [accountOverviewMode, resetCurrentAccountPage]
  );

  const handleApiKeyPageSizeChange = useCallback((pageSize: number) => {
    setApiKeyPageSize(normalizeAccountOverviewPageSize(pageSize, 'table'));
    setApiKeyPage(1);
  }, []);

  const handleRealtimePageSizeChange = useCallback((pageSize: number) => {
    setRealtimePageSize(pageSize);
    setRealtimePage(1);
  }, []);

  const handleAccountSortKeyChange = useCallback(
    (key: AccountSortKey) => {
      resetCurrentAccountPage();
      setAccountSort((previous) =>
        previous.key === key
          ? previous
          : {
              key,
              direction: 'desc',
            }
      );
    },
    [resetCurrentAccountPage]
  );

  const handleAccountSort = useCallback(
    (key: AccountSortKey) => {
      resetCurrentAccountPage();
      setAccountSort((previous) =>
        previous.key === key
          ? {
              key,
              direction: previous.direction === 'desc' ? 'asc' : 'desc',
            }
          : {
              key,
              direction: 'desc',
            }
      );
    },
    [resetCurrentAccountPage]
  );

  const dataPanelActions = useMemo(() => {
    if (activeDataTab === 'accounts') {
      return (
        <AccountOverviewPanelActions
          mode={accountOverviewMode}
          accountDisplayMode={accountDisplayMode}
          searchInput={searchInput}
          accountSort={accountSort}
          accountSortOptions={accountSortOptions}
          overallLoading={overallLoading}
          t={t}
          onSearchChange={setSearchInput}
          onRefreshAll={refreshAll}
          onAccountSortKeyChange={handleAccountSortKeyChange}
          onModeChange={setAccountOverviewMode}
          onAccountDisplayModeChange={setAccountDisplayMode}
        />
      );
    }

    if (activeDataTab === 'apiKeys') {
      return <ApiKeySummaryPanelActions rowCount={apiKeyRows.length} t={t} />;
    }

    return (
      <RealtimeEventsPanelActions
        rowCount={realtimeLogRows.length}
        scopedFailureCount={scopedFailureCount}
        failedOnlyActive={failedOnlyActive}
        accountDisplayMode={accountDisplayMode}
        t={t}
        onToggleFailedOnly={toggleFailedOnly}
        onAccountDisplayModeChange={setAccountDisplayMode}
      />
    );
  }, [
    accountOverviewMode,
    accountDisplayMode,
    accountSort,
    accountSortOptions,
    activeDataTab,
    apiKeyRows.length,
    failedOnlyActive,
    handleAccountSortKeyChange,
    overallLoading,
    realtimeLogRows.length,
    refreshAll,
    scopedFailureCount,
    searchInput,
    t,
    toggleFailedOnly,
  ]);

  const handleAccountPageChange = useCallback(
    (page: number) => {
      setCurrentAccountPage(page);
    },
    [setCurrentAccountPage]
  );

  const handleApiKeyPageChange = useCallback((page: number) => {
    setApiKeyPage(page);
  }, []);

  const resolveUsageTransferError = useCallback(
    (error: unknown) => {
      const rawMessage =
        error instanceof Error ? error.message : String(error || t('common.unknown_error'));
      return rawMessage === 'usage_import_export_requires_usage_service'
        ? t('usage_stats.import_export_requires_usage_service')
        : rawMessage;
    },
    [t]
  );

  const handleUsageExport = useCallback(async () => {
    setUsageExporting(true);
    try {
      const response = await exportUsage();
      const timestamp = new Date().toISOString().replace(/[:.]/g, '-');
      downloadBlob({
        filename: response.filename || `usage-events-${timestamp}.jsonl`,
        blob: response.blob,
      });
      showNotification(t('usage_stats.export_success'), 'success');
    } catch (error: unknown) {
      const message = resolveUsageTransferError(error);
      showNotification(
        `${t('notification.download_failed')}${message ? `: ${message}` : ''}`,
        'error'
      );
    } finally {
      setUsageExporting(false);
    }
  }, [exportUsage, resolveUsageTransferError, showNotification, t]);

  const runUsageImport = useCallback(
    async (file: File) => {
      const runID = usageImportRunIDRef.current + 1;
      usageImportRunIDRef.current = runID;
      const controller = new AbortController();
      usageImportAbortRef.current = controller;
      setUsageImporting(true);
      setUsageImportTask((current) => ({
        file,
        progress:
          current?.file === file
            ? { ...current.progress, phase: 'preparing', error: undefined }
            : {
                sessionId: '',
                filename: file.name,
                phase: 'preparing',
                uploadedBytes: 0,
                totalBytes: file.size,
                percent: 0,
              },
      }));
      try {
        const result = await importUsage(file, {
          signal: controller.signal,
          onProgress: (progress) => {
            if (usageImportRunIDRef.current !== runID) return;
            setUsageImportTask({ file, progress });
          },
        });
        if (usageImportRunIDRef.current !== runID) return;
        const unsupported = result.unsupported ?? 0;
        showNotification(
          `${t('usage_stats.import_success', {
            added: result.added ?? 0,
            skipped: result.skipped ?? 0,
            total: result.total ?? 0,
            failed: result.failed ?? 0,
          })}${unsupported > 0 ? `, ${t('usage_stats.import_unsupported', { count: unsupported })}` : ''}`,
          (result.failed ?? 0) > 0 || unsupported > 0 ? 'warning' : 'success'
        );
        if (result.format?.startsWith('legacy') || (result.warnings ?? []).length > 0) {
          showNotification(t('usage_stats.import_legacy_warning'), 'warning');
        }
        await refreshAll();
        if (usageImportRunIDRef.current === runID) {
          setUsageImportTask(null);
        }
      } catch (error: unknown) {
        if (usageImportRunIDRef.current !== runID) return;
        if (isUsageImportPausedError(error)) {
          setUsageImportTask((current) =>
            current?.file === file
              ? { ...current, progress: { ...current.progress, phase: 'paused', error: undefined } }
              : current
          );
          return;
        }
        if (isUsageImportCancelledError(error)) {
          setUsageImportTask((current) =>
            current?.file === file
              ? {
                  ...current,
                  progress: { ...current.progress, phase: 'cancelled', error: undefined },
                }
              : current
          );
          showNotification(t('usage_stats.import_cancelled'), 'success');
          return;
        }
        const message = resolveUsageTransferError(error);
        setUsageImportTask((current) =>
          current?.file === file
            ? { ...current, progress: { ...current.progress, phase: 'failed', error: message } }
            : current
        );
        showNotification(
          `${t('notification.upload_failed')}${message ? `: ${message}` : ''}`,
          'error'
        );
      } finally {
        if (usageImportRunIDRef.current === runID) {
          usageImportAbortRef.current = null;
          setUsageImporting(false);
        }
      }
    },
    [importUsage, refreshAll, resolveUsageTransferError, showNotification, t]
  );

  const handleUsageImportPause = useCallback(() => {
    usageImportAbortRef.current?.abort();
  }, []);

  const handleUsageImportResume = useCallback(() => {
    if (!usageImportTask || usageImporting) return;
    void runUsageImport(usageImportTask.file);
  }, [runUsageImport, usageImportTask, usageImporting]);

  const handleUsageImportCancel = useCallback(async () => {
    const task = usageImportTask;
    if (!task || usageImportCancelPendingRef.current) return;
    usageImportCancelPendingRef.current = true;
    const runID = usageImportRunIDRef.current + 1;
    usageImportRunIDRef.current = runID;
    usageImportAbortRef.current?.abort();
    usageImportAbortRef.current = null;
    setUsageImporting(true);
    setUsageImportCancelling(true);
    let closeTask = false;
    try {
      if (task.progress.sessionId) {
        const cancelled = await cancelUsageImport(task.progress.sessionId, task.file);
        if (cancelled?.status === 'completed') {
          showNotification(
            t('usage_stats.import_completed_before_cancel', {
              added: cancelled.result?.added ?? 0,
              skipped: cancelled.result?.skipped ?? 0,
            }),
            'warning'
          );
          await refreshAll();
          closeTask = true;
        } else if (cancelled?.status === 'cancelled') {
          const uploadedBytes = cancelled.received_bytes;
          const totalBytes = cancelled.size_bytes || task.progress.totalBytes;
          setUsageImportTask((current) =>
            current?.file === task.file
              ? {
                  ...current,
                  progress: {
                    sessionId: cancelled.id,
                    filename: cancelled.filename || task.file.name,
                    phase: 'cancelled',
                    status: cancelled.status,
                    uploadedBytes,
                    totalBytes,
                    percent:
                      totalBytes > 0
                        ? uploadedBytes >= totalBytes
                          ? 100
                          : Math.max(0, Math.floor((uploadedBytes / totalBytes) * 100))
                        : 0,
                    retryable: cancelled.retryable,
                    result: cancelled.result,
                  },
                }
              : current
          );
          showNotification(t('usage_stats.import_cancelled'), 'success');
        } else {
          showNotification(t('usage_stats.import_cancelled'), 'success');
          closeTask = true;
        }
      } else {
        showNotification(t('usage_stats.import_cancelled'), 'success');
        closeTask = true;
      }
    } catch (error: unknown) {
      const message = resolveUsageTransferError(error);
      setUsageImportTask((current) =>
        current?.file === task.file
          ? { ...current, progress: { ...current.progress, phase: 'paused', error: message } }
          : current
      );
      showNotification(
        `${t('usage_stats.import_cancel_failed')}${message ? `: ${message}` : ''}`,
        'error'
      );
    } finally {
      usageImportCancelPendingRef.current = false;
      if (usageImportRunIDRef.current === runID) {
        setUsageImporting(false);
        setUsageImportCancelling(false);
        if (closeTask) {
          setUsageImportTask(null);
        }
      }
    }
  }, [
    cancelUsageImport,
    refreshAll,
    resolveUsageTransferError,
    showNotification,
    t,
    usageImportTask,
  ]);

  const handleUsageImportModalClose = useCallback(() => {
    if (!usageImporting && !usageImportCancelling) {
      setUsageImportTask(null);
    }
  }, [usageImportCancelling, usageImporting]);

  useEffect(
    () => () => {
      usageImportRunIDRef.current += 1;
      usageImportAbortRef.current?.abort();
    },
    []
  );

  const handleUsageImportClick = useCallback(() => {
    if (!requestMonitoringAvailability.available) {
      showNotification(t('usage_stats.import_export_requires_usage_service'), 'warning');
      return;
    }
    usageImportInputRef.current?.click();
  }, [requestMonitoringAvailability.available, showNotification, t]);

  const handleUsageImportChange = useCallback(
    (event: ChangeEvent<HTMLInputElement>) => {
      const file = event.target.files?.[0];
      event.target.value = '';
      if (!file) return;

      if (!isUsageImportFile(file)) {
        showNotification(t('usage_stats.import_invalid'), 'error');
        return;
      }
      showConfirmation({
        title: t('usage_stats.import_confirm_title'),
        message: t('usage_stats.import_confirm_body', { name: file.name }),
        confirmText: t('usage_stats.import'),
        variant: 'primary',
        onConfirm: () => runUsageImport(file),
      });
    },
    [runUsageImport, showConfirmation, showNotification, t]
  );

  if (monitoringUnavailable) {
    return (
      <div className={styles.page}>
        <MonitoringStatusHeader
          showLoadingOverlay={false}
          monitoringUnavailable={monitoringUnavailable}
          monitoringUnavailableTitle={monitoringUnavailableTitle}
          monitoringUnavailableBody={monitoringUnavailableBody}
          t={t}
        />
      </div>
    );
  }

  return (
    <div className={styles.page}>
      <MonitoringStatusHeader
        showLoadingOverlay={
          overallLoading &&
          !hasMonitoringDisplayData &&
          (!monitoringScopeTransitioning || !hasMonitoringPresentationSnapshot)
        }
        monitoringUnavailable={monitoringUnavailable}
        monitoringUnavailableTitle={monitoringUnavailableTitle}
        monitoringUnavailableBody={monitoringUnavailableBody}
        t={t}
      />

      <MonitoringActionBar
        usageTransferAvailable={usageTransferAvailable}
        usageExporting={usageExporting}
        usageImporting={usageImporting}
        loggingToFile={isFileLogsAvailable(config)}
        modelPricesAvailable={requestMonitoringAvailability.modelPricesAvailable}
        usageImportInputRef={usageImportInputRef}
        t={t}
        onUsageExport={handleUsageExport}
        onUsageImportClick={handleUsageImportClick}
        onUsageImportChange={handleUsageImportChange}
        statusSummary={
          <MonitoringStatusSummary
            connectionTone={connectionTone}
            connectionLabel={connectionLabel}
            lastRefreshedAt={monitoringLastRefreshedAt}
            locale={i18n.language}
            scopedFailureCount={scopedFailureCount}
            totalCalls={scopedSummary.totalCalls}
            t={t}
          />
        }
      />

      <MonitoringFiltersPanel
        timeRange={timeRange}
        autoRefreshMs={autoRefreshMs}
        selectedAccount={selectedAccount}
        selectedProvider={selectedProvider}
        selectedModel={selectedModel}
        selectedChannel={selectedChannel}
        selectedApiKeyHash={selectedApiKeyHash}
        selectedStatus={selectedStatus}
        searchInput={searchInput}
        accountOptions={accountOptions}
        providerOptions={providerOptions}
        modelOptions={modelOptions}
        channelOptions={channelOptions}
        apiKeyOptions={apiKeyOptions}
        statusOptions={statusOptions}
        combinedError={combinedError}
        usageStatisticsEnabled={Boolean(config?.usageStatisticsEnabled)}
        overallLoading={overallLoading}
        t={t}
        onTimeRangeChange={handleTimeRangeChange}
        onAutoRefreshChange={setAutoRefreshMs}
        onRefreshAll={refreshAll}
        onAccountFilterChange={handleAccountFilterChange}
        onProviderChange={setSelectedProvider}
        onModelChange={setSelectedModel}
        onChannelChange={setSelectedChannel}
        onApiKeyChange={setSelectedApiKeyHash}
        onStatusChange={(value) => setSelectedStatus(value as StatusFilter)}
        onSearchChange={setSearchInput}
        onClearFilters={clearFilters}
      />

      <MonitoringSummarySection
        primaryCards={primarySummaryCards}
        secondaryCards={secondarySummaryCards}
      />

      <MonitoringDataPanel
        tabs={dataTabs}
        activeTab={activeDataTab}
        onTabChange={handleDataTabChange}
        ariaLabel={t('monitoring.data_tabs_aria_label')}
        actions={dataPanelActions}
        renderContent={(tab) => {
          if (tab === 'accounts') {
            return (
              <AccountOverviewPanel
                embedded
                mode={accountOverviewMode}
                accountDisplayMode={accountDisplayMode}
                searchInput={searchInput}
                columns={accountOverviewColumns}
                rows={sortedAccountRows}
                pagination={accountPagination}
                accountSort={accountSort}
                accountSortOptions={accountSortOptions}
                expandedAccounts={expandedAccounts}
                focusedAccount={focusedAccount}
                accountAuthStateByRowId={accountAuthStateByRowId}
                accountStatusDataByRowId={accountStatusDataByRowId}
                emptyAccountStatusData={emptyAccountStatusData}
                accountQuotaStates={accountQuotaStatesWithObservedHeaders}
                accountPageSize={accountPageSize}
                accountPageSizeOptions={accountPageSizeOptions}
                accountOverviewScopeText={accountOverviewScopeText}
                hasPrices={hasPrices}
                overallLoading={overallLoading}
                locale={i18n.language}
                emptyState={renderMonitoringEmptyState()}
                t={t}
                onSearchChange={setSearchInput}
                onRefreshAll={refreshAll}
                onAccountSortKeyChange={handleAccountSortKeyChange}
                onModeChange={setAccountOverviewMode}
                onAccountDisplayModeChange={setAccountDisplayMode}
                onAccountSort={handleAccountSort}
                onLoadAccountQuota={loadAccountQuota}
                onToggleExpanded={toggleAccountExpanded}
                onFocusAccount={focusAccount}
                onPageChange={handleAccountPageChange}
                onPageSizeChange={handleAccountPageSizeChange}
              />
            );
          }

          if (tab === 'apiKeys') {
            return (
              <ApiKeySummaryPanel
                embedded
                rows={apiKeyRows}
                columns={apiKeyOverviewColumns}
                pagination={apiKeyPagination}
                expandedApiKeys={expandedApiKeys}
                hasPrices={hasPrices}
                locale={i18n.language}
                pageSize={apiKeyPageSize}
                pageSizeOptions={ACCOUNT_OVERVIEW_TABLE_PAGE_SIZE_OPTIONS}
                emptyState={renderMonitoringEmptyState()}
                t={t}
                onToggleApiKey={toggleApiKeyExpanded}
                onPageChange={handleApiKeyPageChange}
                onPageSizeChange={handleApiKeyPageSizeChange}
              />
            );
          }

          return (
            <RealtimeEventsPanel
              embedded
              rows={realtimeLogRows}
              pagination={realtimePagination}
              pageSize={realtimePageSize}
              scopedFailureCount={scopedFailureCount}
              failedOnlyActive={failedOnlyActive}
              eventsHasMore={eventsHasMore}
              eventsLoadingMore={eventsLoadingMore}
              eventsRetentionLimited={eventsRetentionLimited}
              eventsTotalCount={eventsTotalCount}
              eventsLoadedCount={eventsLoadedCount}
              overallLoading={overallLoading}
              hasPrices={hasPrices}
              accountDisplayMode={accountDisplayMode}
              locale={i18n.language}
              emptyState={renderMonitoringEmptyState()}
              t={t}
              onToggleFailedOnly={toggleFailedOnly}
              onAccountDisplayModeChange={setAccountDisplayMode}
              onPageChange={setRealtimePage}
              onPageSizeChange={handleRealtimePageSizeChange}
              onLoadMoreEvents={loadMoreEvents}
            />
          );
        }}
      />

      <MonitoringCustomRangeModal
        open={isCustomRangeModalOpen}
        onClose={() => setIsCustomRangeModalOpen(false)}
        startInput={customDraftStartInput}
        endInput={customDraftEndInput}
        error={customDraftTimeRangeError}
        t={t}
        onApply={applyCustomTimeRange}
        onStartChange={handleCustomDraftStartChange}
        onEndChange={handleCustomDraftEndChange}
      />

      <UsageImportProgressModal
        open={Boolean(usageImportTask)}
        progress={usageImportTask?.progress ?? null}
        busy={usageImportCancelling}
        onPause={handleUsageImportPause}
        onResume={handleUsageImportResume}
        onCancel={() => void handleUsageImportCancel()}
        onClose={handleUsageImportModalClose}
      />
    </div>
  );
}
