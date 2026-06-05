import {
  useCallback,
  useDeferredValue,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
} from 'react';
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
  buildAccountQuotaErrorEntry,
  buildChannelOptionsFromValues,
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
  parseDateTimeLocalValue,
  requestAccountQuota,
  type FocusSnapshot,
  type StatusFilter,
} from '@/features/monitoring/model/monitoringCenterPageModel';
import { useUsageData } from '@/features/monitoring/hooks/useUsageData';
import {
  readMonitoringCenterUiState,
  writeMonitoringCenterUiState,
  type MonitoringDataTab,
} from '@/features/monitoring/monitoringCenterUiState';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useInterval } from '@/hooks/useInterval';
import { useRequestMonitoringAvailability } from '@/hooks/useRequestMonitoringAvailability';
import { isFileLogsAvailable } from '@/features/logs/logFeatureAvailability';
import { authFilesApi } from '@/services/api';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import { formatFileSize } from '@/utils/format';
import type { StatusBarData } from '@/utils/recentRequests';
import { downloadBlob } from '@/utils/download';
import { sha256Hex } from '@/utils/apiKeyHash';
import { formatCompactNumber } from '@/utils/usage';
import styles from './MonitoringCenterPage.module.scss';

export { AccountExpandedDetails, AccountOverviewCard };

const DEFAULT_ACCOUNT_PAGE_SIZE = ACCOUNT_OVERVIEW_TABLE_PAGE_SIZE_OPTIONS[0];
const MAX_USAGE_IMPORT_FILE_SIZE = 64 * 1024 * 1024;
const EMPTY_STATUS_BAR_DATA: StatusBarData = {
  blocks: [],
  blockDetails: [],
  successRate: 100,
  totalSuccess: 0,
  totalFailure: 0,
};

export function MonitoringCenterPage() {
  const { t, i18n } = useTranslation();
  const config = useConfigStore((state) => state.config);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const requestMonitoringAvailability = useRequestMonitoringAvailability();
  const initialAccountOverviewUiState = useRef(readAccountOverviewUiState());
  const initialMonitoringCenterUiState = useRef(readMonitoringCenterUiState());
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
  const [selectedStatus, setSelectedStatus] = useState<StatusFilter>(
    () => initialMonitoringCenterUiState.current.selectedStatus
  );
  const [expandedAccounts, setExpandedAccounts] = useState<Record<string, boolean>>({});
  const [expandedApiKeys, setExpandedApiKeys] = useState<Record<string, boolean>>({});
  const [focusedAccount, setFocusedAccount] = useState<string | null>(null);
  const [isCustomRangeModalOpen, setIsCustomRangeModalOpen] = useState(false);
  const [usageExporting, setUsageExporting] = useState(false);
  const [usageImporting, setUsageImporting] = useState(false);
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
  const [accountStatusUpdating, setAccountStatusUpdating] = useState<Record<string, boolean>>({});
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
  } = useUsageData({ loadUsageEvents: false });

  const monitoringScopeFilters = useMemo(
    () => ({
      account: selectedAccount,
      provider: selectedProvider,
      model: selectedModel,
      channel: selectedChannel,
      apiKeyHash: selectedApiKeyHash,
      status: selectedStatus,
    }),
    [
      selectedAccount,
      selectedApiKeyHash,
      selectedChannel,
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
  });

  const refreshAll = useCallback(async () => {
    await Promise.all([loadApiKeyAliases(), refreshMeta(false)]);
  }, [loadApiKeyAliases, refreshMeta]);

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

  useHeaderRefresh(refreshAll);
  useInterval(
    () => {
      void refreshAll().catch(() => {});
    },
    connectionStatus === 'connected' && Number(autoRefreshMs) > 0 ? Number(autoRefreshMs) : null
  );

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
  const accountStatusDataByRowId = useMemo(
    () => buildMonitoringAccountStatusDataMap(scopedRows, accountStatusBounds),
    [accountStatusBounds, scopedRows]
  );
  const emptyAccountStatusData = useMemo(() => {
    const resolvedBounds = resolveMonitoringStatusRangeBounds(scopedRows, accountStatusBounds);
    return resolvedBounds ? buildEmptyMonitoringStatusData(resolvedBounds) : EMPTY_STATUS_BAR_DATA;
  }, [accountStatusBounds, scopedRows]);
  const accountAuthStateByRowId = useMemo(
    () => buildMonitoringAccountAuthStateMap(accountRows, authFilesByAuthIndex),
    [accountRows, authFilesByAuthIndex]
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
  const scopedFailureCount = scopedSummary.failureCalls;

  const hasSearchFilter = Boolean(deferredSearch.trim());
  const hasScopeFilter =
    selectedAccount !== 'all' ||
    selectedProvider !== 'all' ||
    selectedModel !== 'all' ||
    selectedChannel !== 'all' ||
    selectedApiKeyHash !== 'all' ||
    selectedStatus !== 'all';
  const hasActiveDataFilter = hasSearchFilter || hasScopeFilter;
  const failedGroupCount = groupedRealtimeRows.filter((row) => row.failureCalls > 0).length;
  const failedOnlyActive = selectedStatus === 'failed';
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
        accountCount: accountRows.length,
        failedGroupCount,
        hasPrices,
        locale: i18n.language,
        t,
      }),
    [accountRows.length, failedGroupCount, hasPrices, i18n.language, scopedSummary, t]
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
        label: t('monitoring.data_tab_accounts'),
        icon: 'accounts',
        badge: accountRows.length,
        badgeTitle: t('monitoring.data_tab_accounts_badge_title', { count: accountRows.length }),
      },
      {
        id: 'apiKeys',
        label: t('monitoring.data_tab_api_keys'),
        icon: 'apiKeys',
        badge: apiKeyRows.length,
        badgeTitle: t('monitoring.data_tab_api_keys_badge_title', { count: apiKeyRows.length }),
      },
      {
        id: 'realtime',
        label: t('monitoring.data_tab_realtime'),
        icon: 'realtime',
        badge: realtimeBadge,
        badgeTone: realtimeHasFailure ? 'failure' : 'default',
        badgeTitle: t('monitoring.data_tab_realtime_badge_title', {
          failed: failureCount,
          total: totalCalls,
        }),
      },
    ];
  }, [accountRows.length, apiKeyRows.length, scopedFailureCount, scopedSummary.totalCalls, t]);

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
    setSelectedStatus('all');
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
            previous[account]?.targetKey === targetKey ? (previous[account]?.entries ?? []) : [],
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

      const entries = settled.map((result, index) => {
        const fallback = targets[index];
        if (result.status === 'fulfilled') {
          return result.value;
        }

        const error =
          result.reason instanceof Error
            ? result.reason.message
            : String(result.reason || t('common.unknown_error'));
        return buildAccountQuotaErrorEntry(fallback, error, t);
      });

      const hasSuccess = entries.some((entry) => !entry.error);
      setAccountQuotaStates((previous) => ({
        ...previous,
        [account]: {
          status: hasSuccess ? 'success' : 'error',
          targetKey,
          entries,
          error: hasSuccess ? '' : entries[0]?.error || t('common.unknown_error'),
          lastRefreshedAt: Date.now(),
        },
      }));
    },
    [accountQuotaTargetsByAccount, t]
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

  const handleAccountStatusToggle = useCallback(
    async (row: MonitoringAccountRow, enabled: boolean) => {
      const authState = accountAuthStateByRowId.get(row.id);
      const fileNames = authState?.toggleableFileNames ?? [];
      if (fileNames.length === 0) return;

      setAccountStatusUpdating((previous) => ({ ...previous, [row.id]: true }));

      const results = await Promise.allSettled(
        fileNames.map((fileName) => authFilesApi.setStatusWithFallback(fileName, !enabled))
      );

      const successCount = results.filter((result) => result.status === 'fulfilled').length;
      const failureCount = results.length - successCount;

      try {
        await refreshMeta(false);
      } finally {
        setAccountStatusUpdating((previous) => {
          const next = { ...previous };
          delete next[row.id];
          return next;
        });
      }

      if (failureCount === 0) {
        showNotification(
          enabled
            ? t('monitoring.account_overview_status_enabled_success', { count: successCount })
            : t('monitoring.account_overview_status_disabled_success', { count: successCount }),
          'success'
        );
        return;
      }

      showNotification(
        t('monitoring.account_overview_status_partial', {
          success: successCount,
          failed: failureCount,
        }),
        successCount > 0 ? 'warning' : 'error'
      );
    },
    [accountAuthStateByRowId, refreshMeta, showNotification, t]
  );

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

  const importUsageFile = useCallback(
    async (file: File) => {
      setUsageImporting(true);
      try {
        const result = await importUsage(file);
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
      } catch (error: unknown) {
        const message = resolveUsageTransferError(error);
        showNotification(
          `${t('notification.upload_failed')}${message ? `: ${message}` : ''}`,
          'error'
        );
      } finally {
        setUsageImporting(false);
      }
    },
    [importUsage, refreshAll, resolveUsageTransferError, showNotification, t]
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
      if (file.size > MAX_USAGE_IMPORT_FILE_SIZE) {
        showNotification(
          t('usage_stats.import_file_too_large', {
            maxSize: formatFileSize(MAX_USAGE_IMPORT_FILE_SIZE),
          }),
          'error'
        );
        return;
      }

      showConfirmation({
        title: t('usage_stats.import_confirm_title'),
        message: t('usage_stats.import_confirm_body', { name: file.name }),
        confirmText: t('usage_stats.import'),
        variant: 'primary',
        onConfirm: () => importUsageFile(file),
      });
    },
    [importUsageFile, showConfirmation, showNotification, t]
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
          filteredRows.length === 0 &&
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
                accountQuotaStates={accountQuotaStates}
                accountStatusUpdating={accountStatusUpdating}
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
                onAccountStatusToggle={handleAccountStatusToggle}
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
    </div>
  );
}
