import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { KeyboardEvent, MouseEvent as ReactMouseEvent } from 'react';
import { createPortal } from 'react-dom';
import type { TFunction } from 'i18next';
import { useLocation, useNavigate, type BlockerFunction } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { DropdownMenu, type DropdownMenuItem } from '@/components/ui/DropdownMenu';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { Modal } from '@/components/ui/Modal';
import { Select } from '@/components/ui/Select';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconBinary,
  IconCheck,
  IconArrowDownWideNarrow,
  IconArrowUpNarrowWide,
  IconCopy,
  IconDollarSign,
  IconDownload,
  IconEye,
  IconEyeOff,
  IconFileText,
  IconKey,
  IconMoreVertical,
  IconModelCluster,
  IconPlus,
  IconRefreshCw,
  IconSearch,
  IconSend,
  IconSettings,
  IconShield,
  IconSlidersHorizontal,
  IconTrash2,
  IconX,
} from '@/components/ui/icons';
import {
  ANTIGRAVITY_CONFIG,
  CLAUDE_CONFIG,
  CODEX_CONFIG,
  KIMI_CONFIG,
  XAI_CONFIG,
  buildObservedCodexQuotaState,
  resolveQuotaDisplayState,
  type QuotaConfig,
} from '@/components/quota';
import { buildQuotaFailureState, getScopedQuotaState } from '@/components/quota/quotaConfigs';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useInterval } from '@/hooks/useInterval';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { useUnsavedChangesGuard } from '@/hooks/useUnsavedChangesGuard';
import { getAuthFileIcon } from '@/features/authFiles/constants';
import { useAuthFilesData } from '@/features/authFiles/hooks/useAuthFilesData';
import { useAuthFilesOauth } from '@/features/authFiles/hooks/useAuthFilesOauth';
import { useAuthFilesModels } from '@/features/authFiles/hooks/useAuthFilesModels';
import { useAuthFileConfigurationEditor } from '@/features/authFiles/hooks/useAuthFileConfigurationEditor';
import { useCredentialInspectionSnapshot } from '@/features/accounts/hooks/useCredentialInspectionSnapshot';
import { useAccountsWorkspaceRefresh } from '@/features/accounts/hooks/useAccountsWorkspaceRefresh';
import { useHeaderSnapshotsLoader } from '@/features/monitoring/hooks/useHeaderSnapshotsLoader';
import { PaginationControls } from '@/features/monitoring/components/MonitoringShared';
import { CredentialHealthInspectionWorkspace } from '@/features/monitoring/components/CredentialHealthInspectionWorkspace';
import { AuthJsonPasteModal } from '@/features/authFiles/components/AuthJsonPasteModal';
import { OAuthExcludedCard } from '@/features/authFiles/components/OAuthExcludedCard';
import {
  OAuthExcludedEditorModal,
  OAuthModelAliasEditorModal,
} from '@/features/authFiles/components/OAuthEditorModals';
import { OAuthModelAliasCard } from '@/features/authFiles/components/OAuthModelAliasCard';
import { CodexReauthDialog } from '@/features/oauth/CodexReauthDialog';
import {
  createCodexReauthTargetFromAuthFile,
  type CodexReauthTarget,
} from '@/features/oauth/codexReauthModel';
import {
  ACCOUNT_CODEX_STATUS_FILTERS,
  buildAccountMetrics,
  buildAccountRows,
  findAccountRowForInspectionTarget,
  filterAccountRows,
  getPlanOptions,
  getProviderOptions,
  isAccountCodexStatusFilter,
  normalizeAccountProvider,
  sortAccountRows,
  type AccountQuotaBand,
  type AccountRow,
  type AccountRowSort,
  type AccountStatusFilter,
} from '@/features/accounts/model/accountRows';
import { buildAccountRecommendations } from '@/features/accounts/model/quotaRecommendations';
import {
  buildAccountListItem,
  buildRecommendationBySelectionKey,
  type AccountListHealthStatusKey,
} from '@/features/accounts/model/accountListPresentation';
import {
  buildAccountHistoryByRowKey,
  buildAccountHistoryTargetEntries,
  type AccountHistoryTargetEntry,
} from '@/features/accounts/model/accountHistoryRows';
import {
  buildAccountWindowUsageByKey,
  buildAccountWindowUsageTargetEntries,
  filterAccountWindowUsageByTargetRanges,
} from '@/features/accounts/model/accountWindowUsageRows';
import {
  buildAccountQuotaDisplayWindows,
  getQuotaWindowShortLabel,
  type AccountQuotaDisplayWindow,
} from '@/features/accounts/model/accountQuotaDisplayWindows';
import {
  buildAccountQuotaWindowDefinitions,
  type AccountQuotaWindowDefinition,
} from '@/features/accounts/model/accountQuotaWindowDefinitions';
import {
  buildAccountQuotaSnapshotQueryAccounts,
  buildAccountQuotaSnapshotWriteEntries,
  mergeCodexResetCreditsFromQuotaSnapshots,
  mergeAccountQuotaSnapshotWindows,
} from '@/features/accounts/model/accountQuotaSnapshots';
import {
  ACCOUNT_OVERVIEW_ACTIVITY_RANGE_MS,
  buildAccountDetailViewModel,
} from '@/features/accounts/model/accountDetailViewModel';
import {
  ACCOUNT_SORT_DEFAULT_DIRECTIONS,
  ACCOUNT_SORT_FIELD_OPTIONS,
  DETAIL_EVENTS_LIMIT,
  DETAIL_EVENTS_RANGE_MS,
  PAGE_SIZE_OPTIONS,
  buildAntigravityQuotaMatrix,
  formatCompactNumber,
  formatHistorySuccessRate,
  formatMoney,
  formatPercent,
  formatQuotaResetDisplay,
  formatQuotaResetTooltipParams,
  getAccountHistoryTitle,
  getAccountSortFieldOption,
  getProviderLabel,
  parsePriorityValue,
  toAuthFileCodexInspectionSnapshot,
  type AccountSortFieldValue,
  type AccountsView,
  type DetailTab,
} from '@/features/accounts/model/accountsPagePresentation';
import {
  getAuthFileCodexInspectionKeyForIdentity,
  getAuthFileCodexStatus,
  getAuthFilePatchTarget,
  getAuthFileSelectionKey,
  getAuthFileScopedCodexQuota,
  getFreshAuthFileCodexStatusSources,
  hasPartialSharedAuthFileSelection,
} from '@/features/authFiles/model/authFilesPageModel';
import {
  buildUsageValueRowFromMonitoringSummary,
  buildUsageValueRowsFromRecent,
  type UsageValueRow,
} from '@/features/accounts/model/usageValueRows';
import {
  buildOAuthRulePreviewRows,
  getOAuthRulePreviewProviders,
  partitionOAuthRulePreviewRows,
} from '@/features/accounts/model/oauthRulePreview';
import { resolveAccountReauthAction } from '@/features/accounts/model/accountReauth';
import { beginAccountQuotaRequest } from '@/features/accounts/model/accountQuotaRequestGate';
import { buildAccountOperationalItemsByRowKey } from '@/features/accounts/model/accountOperationalScope';
import {
  DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE,
  readAccountsWorkspaceUiState,
  writeAccountsWorkspaceUiState,
  type AccountOperationalFilter,
  type AccountsWorkspaceUiState,
} from '@/features/accounts/model/accountsWorkspaceUiState';
import {
  readAccountsWorkspaceUrlState,
  writeAccountsWorkspaceUrlSearch,
} from '@/features/accounts/model/accountsWorkspaceUrlState';
import {
  AccountConfigurationTab,
  AccountDiagnosticsTab,
  AccountLatestRequest,
  AccountMetricsGrid,
  AccountModelsTab,
  AccountOverviewTab,
  AccountProviderTabs,
  AccountQuotaMatrix,
  AccountQuotaTab,
  AccountsBatchDeletePreview,
} from '@/features/accounts/components';
import {
  accountQuotaSnapshotApi,
  monitoringAnalyticsApi,
  usageServiceApi,
  type AccountActionCandidate,
  type AccountQuotaSnapshotObservationInput,
  type AccountQuotaSnapshotWriteEntry,
  type AccountQuotaSnapshotWindow,
  type MonitoringAnalyticsAccountStatRow,
  type MonitoringAnalyticsEventRow,
  type MonitoringAnalyticsRecentFailure,
  type MonitoringAnalyticsSummary,
  type MonitoringAccountHistoryItem,
  type MonitoringAccountWindowUsageItem,
  type QuotaCooldownInfo,
  type UsageHeaderSnapshot,
} from '@/services/api';
import type { AuthFileItem, CodexQuotaState, XaiQuotaState } from '@/types';
import type { AuthJsonInputType } from '@/features/authFiles/sessionAuthConverter';
import { type QuotaAccountDisplayMode } from '@/features/quota/quotaPageUiState';
import { maskQuotaAccountText } from '@/components/quota/quotaDisplay';
import {
  captureQuotaCacheGeneration,
  commitIfQuotaCacheCurrent,
  useAuthStore,
  useNotificationStore,
  useQuotaStore,
  useThemeStore,
} from '@/stores';
import { useUsageHeaderSnapshotStore } from '@/stores/useUsageHeaderSnapshotStore';
import { copyToClipboard } from '@/utils/clipboard';
import {
  buildUsageHeaderSnapshotLookup,
  getHighConfidenceUsageHeaderSnapshotForAuthFile,
  hasUsageHeaderQuotaSignal,
  isUsageHeaderQuotaSnapshotExpired,
} from '@/utils/usageHeaderSnapshots';
import {
  getCredentialScopedQuotaState,
  getQuotaCredentialStoreKey,
} from '@/utils/quota/credentialScope';
import {
  buildProviderCredentialTaskPlan,
  runProviderCredentialTaskPlan,
} from '@/utils/quota/providerRefreshScheduler';
import { createCodexInspectionConnectionFingerprint } from '@/features/monitoring/codexInspection';
import type {
  CredentialHealthInspectionMode,
  CredentialInspectionTarget,
} from '@/features/monitoring/model/credentialInspectionSnapshot';
import styles from './AccountsPage.module.scss';

type QuotaUpdater<T> = T | ((prev: T) => T);
type QuotaSetter<T> = (updater: QuotaUpdater<Record<string, T>>) => void;

const MAX_CONCURRENT_QUOTA_REFRESHES_PER_PROVIDER = 1;
const MAX_CONCURRENT_QUOTA_REFRESH_PROVIDERS = 3;
const PASSIVE_HEADER_SNAPSHOT_REFRESH_MS = 60_000;

const readAccountsSearchFromHash = (hash: string): string => {
  const queryIndex = hash.indexOf('?');
  return queryIndex >= 0 ? hash.slice(queryIndex) : '';
};

const isConfigurationDetailTab = (tab: DetailTab): boolean => tab === 'config' || tab === 'models';

const getHealthStatusClass = (status: AccountListHealthStatusKey) => {
  switch (status) {
    case 'available':
      return styles.badgeGood;
    case 'five_hour_cooldown':
    case 'weekly_cooldown':
    case 'monthly_cooldown':
    case 'limited':
    case 'partial':
      return styles.badgeWarn;
    case 'five_hour_exhausted':
    case 'weekly_exhausted':
    case 'monthly_exhausted':
    case 'exception':
    case 'reauth':
      return styles.badgeBad;
    case 'disabled':
      return styles.badgeMuted;
    default:
      return styles.badgeNeutral;
  }
};

const getRemainingBarClass = (row: AccountRow) => {
  if (row.quota.status === 'exhausted' || row.quota.status === 'error') return styles.quotaBarBad;
  if (row.quota.status === 'low') return styles.quotaBarWarn;
  if (row.quota.status === 'ok') return styles.quotaBarGood;
  return styles.quotaBarNeutral;
};

async function refreshQuotaWithConfig<TState, TData>({
  config,
  file,
  setQuota,
  t,
  isCurrent,
}: {
  config: QuotaConfig<TState, TData>;
  file: AuthFileItem;
  setQuota: QuotaSetter<TState>;
  t: TFunction;
  isCurrent: () => boolean;
}) {
  const storeKey = config.getStoreKey?.(file) ?? file.name;
  const cacheGeneration = captureQuotaCacheGeneration();
  try {
    const data = await config.fetchQuota(file, t);
    if (!isCurrent()) return false;
    return commitIfQuotaCacheCurrent(cacheGeneration, () => {
      setQuota((prev) => ({
        ...prev,
        [storeKey]: config.buildSuccessState(data, file),
      }));
    });
  } catch (error: unknown) {
    if (!isCurrent()) return false;
    const message = error instanceof Error ? error.message : t('common.unknown_error');
    const status =
      typeof error === 'object' && error !== null && 'status' in error
        ? Number((error as { status?: unknown }).status)
        : undefined;
    commitIfQuotaCacheCurrent(cacheGeneration, () => {
      setQuota((prev) => {
        const previousState = getScopedQuotaState(config, prev, file);
        return {
          ...prev,
          [storeKey]: buildQuotaFailureState(
            config,
            message,
            Number.isFinite(status) ? status : undefined,
            file,
            previousState
          ),
        };
      });
    });
    return false;
  }
}

export function AccountsPage() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const featureAvailability = usePanelFeatureAvailability();
  const initialWorkspaceUiState = useRef(readAccountsWorkspaceUiState());
  const initialWorkspaceUrlState = useRef(
    readAccountsWorkspaceUrlState(location.search, initialWorkspaceUiState.current)
  );
  const modelsLoadKeyRef = useRef('');
  const modelRulesLoadKeyRef = useRef('');
  const connectionFingerprint = useMemo(
    () => createCodexInspectionConnectionFingerprint(apiBase, managementKey),
    [apiBase, managementKey]
  );
  const managerStorageAvailable =
    !featureAvailability.checking &&
    Boolean(featureAvailability.managerServiceBase) &&
    Boolean(managementKey);
  const requestHistoryAvailable =
    managerStorageAvailable && featureAvailability.requestMonitoringAvailable;

  const {
    files,
    selectedFiles,
    selectionCount,
    loading,
    error,
    uploading,
    authJsonPasteSaving,
    deleting,
    credentialRefreshing = {},
    batchFieldsUpdating,
    fileInputRef,
    loadFiles,
    handleUploadClick,
    handleFileChange,
    savePastedAuthJson,
    handleDelete,
    handleDownload,
    handleCredentialRefresh,
    toggleSelect,
    selectAllVisible,
    invertVisibleSelection,
    deselectAll,
    batchDownload,
    batchSetStatus,
    batchPatchFields,
    batchDelete,
  } = useAuthFilesData({ connectionFingerprint });

  const [oauthViewMode, setOauthViewMode] = useState<'diagram' | 'list'>('list');
  const oauthState = useAuthFilesOauth({
    viewMode: oauthViewMode,
    files,
    connectionKey: connectionFingerprint,
    requestScope: { apiBase, managementKey },
  });
  const {
    modelsLoading,
    modelsRefreshing,
    modelsList,
    modelDefinitions,
    modelDefinitionsLoading,
    modelDefinitionsError,
    modelsFileName,
    modelsFileType,
    modelsSelectionKey,
    modelsError,
    showModels,
    refreshModels,
    invalidateModels,
  } = useAuthFilesModels({ connectionKey: connectionFingerprint });
  const antigravityQuota = useQuotaStore((state) => state.antigravityQuota);
  const claudeQuota = useQuotaStore((state) => state.claudeQuota);
  const codexQuota = useQuotaStore((state) => state.codexQuota);
  const kimiQuota = useQuotaStore((state) => state.kimiQuota);
  const xaiQuota = useQuotaStore((state) => state.xaiQuota);
  const baseQuotaStores = useMemo(
    () => ({
      antigravityQuota,
      claudeQuota,
      codexQuota,
      kimiQuota,
      xaiQuota,
    }),
    [antigravityQuota, claudeQuota, codexQuota, kimiQuota, xaiQuota]
  );
  const setAntigravityQuota = useQuotaStore((state) => state.setAntigravityQuota);
  const setClaudeQuota = useQuotaStore((state) => state.setClaudeQuota);
  const setCodexQuota = useQuotaStore((state) => state.setCodexQuota);
  const setKimiQuota = useQuotaStore((state) => state.setKimiQuota);
  const setXaiQuota = useQuotaStore((state) => state.setXaiQuota);

  const [activeView, setActiveView] = useState<AccountsView>(
    () => initialWorkspaceUrlState.current.view
  );
  const [documentVisible, setDocumentVisible] = useState(
    () => typeof document === 'undefined' || document.visibilityState !== 'hidden'
  );
  const previousDocumentVisibleRef = useRef(documentVisible);
  const [healthMode, setHealthMode] = useState<CredentialHealthInspectionMode>(
    () => initialWorkspaceUrlState.current.healthMode
  );
  const {
    results: inspectionResults,
    loading: inspectionLoading,
    refresh: loadInspectionSummary,
    applySnapshot: applyInspectionSnapshot,
  } = useCredentialInspectionSnapshot({
    connectionFingerprint,
    checking: featureAvailability.checking,
    serverAvailable: featureAvailability.serverCodexInspectionAvailable,
    managerServiceBase: featureAvailability.managerServiceBase ?? '',
    managementKey,
  });
  const [quotaRefreshing, setQuotaRefreshing] = useState(false);
  const [historyRefreshing, setHistoryRefreshing] = useState(false);
  const [accountHistoryRefreshRevision, setAccountHistoryRefreshRevision] = useState(0);
  const [accountQuotaRefreshRevision, setAccountQuotaRefreshRevision] = useState(0);
  const [statusUpdating, setStatusUpdating] = useState(false);
  const [selectedRowKey, setSelectedRowKey] = useState<string | null>(
    () => initialWorkspaceUrlState.current.account
  );
  const [isSelectionMode, setIsSelectionMode] = useState(false);
  const [detailTab, setDetailTab] = useState<DetailTab>(
    () => initialWorkspaceUrlState.current.detailTab
  );
  const [providerFilter, setProviderFilter] = useState(
    () => initialWorkspaceUrlState.current.providerFilter
  );
  const [statusFilter, setStatusFilter] = useState<AccountStatusFilter>(
    () => initialWorkspaceUrlState.current.statusFilter
  );
  const [planFilter, setPlanFilter] = useState(() => initialWorkspaceUrlState.current.planFilter);
  const [quotaBandFilter, setQuotaBandFilter] = useState<AccountQuotaBand>(
    () => initialWorkspaceUrlState.current.quotaBandFilter
  );
  const [operationalFilter, setOperationalFilter] = useState<AccountOperationalFilter>(
    () => initialWorkspaceUrlState.current.operationalFilter
  );
  const effectiveOperationalFilter =
    !managerStorageAvailable &&
    (operationalFilter === 'cooldown' || operationalFilter === 'automation')
      ? 'all'
      : operationalFilter;
  const [search, setSearch] = useState(() => initialWorkspaceUrlState.current.search);
  const [accountSort, setAccountSort] = useState<AccountRowSort>(
    () => initialWorkspaceUrlState.current.accountSort
  );
  const [isMobileFiltersOpen, setIsMobileFiltersOpen] = useState(false);
  const [isAccountSortDropdownOpen, setIsAccountSortDropdownOpen] = useState(false);
  const [highlightedAccountSortIndex, setHighlightedAccountSortIndex] = useState(-1);
  const [batchPriorityOpen, setBatchPriorityOpen] = useState(false);
  const [batchPriorityValue, setBatchPriorityValue] = useState('');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(() => initialWorkspaceUrlState.current.pageSize);
  const [usageRows, setUsageRows] = useState<UsageValueRow[]>([]);
  const [accountHistoryByRowKey, setAccountHistoryByRowKey] = useState<
    Map<string, MonitoringAccountHistoryItem>
  >(() => new Map());
  const [accountHistoryLoading, setAccountHistoryLoading] = useState(false);
  const [accountHistoryErrorsByRowKey, setAccountHistoryErrorsByRowKey] = useState<
    Map<string, string>
  >(() => new Map());
  const [accountWindowUsageByKey, setAccountWindowUsageByKey] = useState<
    Map<string, MonitoringAccountWindowUsageItem>
  >(() => new Map());
  const [accountWindowUsageQueryContext, setAccountWindowUsageQueryContext] = useState<{
    rowKey: string;
    asOfMs: number;
  } | null>(null);
  const [accountWindowUsageError, setAccountWindowUsageError] = useState('');
  const [quotaSnapshotWindowsByRowKey, setQuotaSnapshotWindowsByRowKey] = useState<
    Map<string, AccountQuotaSnapshotWindow[]>
  >(() => new Map());
  const [accountActionCandidates, setAccountActionCandidates] = useState<AccountActionCandidate[]>(
    []
  );
  const [accountActionCandidatesLoading, setAccountActionCandidatesLoading] = useState(false);
  const [accountActionCandidatesError, setAccountActionCandidatesError] = useState('');
  const [oauthPreviewModel, setOauthPreviewModel] = useState('');
  const [oauthPreviewProvider, setOauthPreviewProvider] = useState('');
  const [oauthDirectExpanded, setOauthDirectExpanded] = useState(false);
  const [oauthExcludedEditorProvider, setOauthExcludedEditorProvider] = useState<string | null>(
    () =>
      initialWorkspaceUrlState.current.editor === 'excluded'
        ? initialWorkspaceUrlState.current.editorProvider
        : null
  );
  const [oauthModelAliasEditorProvider, setOauthModelAliasEditorProvider] = useState<string | null>(
    () =>
      initialWorkspaceUrlState.current.editor === 'alias'
        ? initialWorkspaceUrlState.current.editorProvider
        : null
  );
  const [authJsonPasteOpen, setAuthJsonPasteOpen] = useState(false);
  const [codexReauthTarget, setCodexReauthTarget] = useState<CodexReauthTarget | null>(null);
  const [detailEventsRowKey, setDetailEventsRowKey] = useState<string | null>(null);
  const [detailEvents, setDetailEvents] = useState<MonitoringAnalyticsEventRow[]>([]);
  const [detailEventsSummary, setDetailEventsSummary] = useState<MonitoringAnalyticsSummary | null>(
    null
  );
  const [detailEventsRecentFailure, setDetailEventsRecentFailure] =
    useState<MonitoringAnalyticsRecentFailure | null>(null);
  const [detailEventsTotalCount, setDetailEventsTotalCount] = useState(0);
  const [detailEventsHasMore, setDetailEventsHasMore] = useState(false);
  const [detailEventsNextBeforeMs, setDetailEventsNextBeforeMs] = useState<number | null>(null);
  const [detailEventsNextBeforeId, setDetailEventsNextBeforeId] = useState<number | null>(null);
  const [detailEventsLoading, setDetailEventsLoading] = useState(false);
  const [detailEventsRefreshing, setDetailEventsRefreshing] = useState(false);
  const [detailEventsAppending, setDetailEventsAppending] = useState(false);
  const [detailEventsError, setDetailEventsError] = useState('');
  const [quotaCooldowns, setQuotaCooldowns] = useState<Map<string, QuotaCooldownInfo>>(
    () => new Map()
  );
  const headerSnapshotLoadKey = useMemo(
    () =>
      JSON.stringify({
        connectionFingerprint,
        managerServiceBase: featureAvailability.managerServiceBase,
        managementKey,
        requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
      }),
    [
      connectionFingerprint,
      featureAvailability.managerServiceBase,
      featureAvailability.requestMonitoringAvailable,
      managementKey,
    ]
  );
  const headerSnapshots = useUsageHeaderSnapshotStore((state) => state.items);
  const headerSnapshotGeneratedAtMs = useUsageHeaderSnapshotStore((state) => state.generatedAtMs);
  const [headerSnapshotLoadedKey, setHeaderSnapshotLoadedKey] = useState('');
  const headerSnapshotLoadKeyRef = useRef(headerSnapshotLoadKey);
  headerSnapshotLoadKeyRef.current = headerSnapshotLoadKey;
  const [accountDisplayMode, setAccountDisplayMode] = useState<QuotaAccountDisplayMode>(
    () => initialWorkspaceUrlState.current.accountDisplayMode
  );
  const [copiedIdentityKey, setCopiedIdentityKey] = useState<string | null>(null);
  const detailEventsRequestIdRef = useRef(0);
  const detailEventsAutoLoadKeyRef = useRef<string | null>(null);
  const quotaCooldownRequestIdRef = useRef(0);
  const accountHistoryAutoAbortRef = useRef<AbortController | null>(null);
  const accountHistoryTargetAbortRef = useRef<AbortController | null>(null);
  const accountHistoryRequestVersionsRef = useRef<Map<string, number>>(new Map());
  const accountHistoryPendingRequestsRef = useRef<Set<symbol>>(new Set());
  const accountHistoryAutoLoadKeyRef = useRef<string | null>(null);
  const accountWindowUsageReqIdRef = useRef(0);
  const accountWindowUsageAbortRef = useRef<AbortController | null>(null);
  const accountWindowUsageAutoLoadKeyRef = useRef<string | null>(null);
  const quotaRefreshBatchPromiseRef = useRef<Promise<void> | null>(null);
  const accountHistoryRefreshRequestIdRef = useRef(0);
  const accountHistoryRefreshPromiseRef = useRef<{
    key: string;
    promise: Promise<void>;
  } | null>(null);
  const usageValuesRequestIdRef = useRef(0);
  const usageValuesAutoLoadKeyRef = useRef<string | null>(null);
  const accountActionCandidatesReqIdRef = useRef(0);
  const accountActionCandidatesRef = useRef<AccountActionCandidate[]>([]);
  const lastWorkspaceNavigationRef = useRef<string | null>(null);
  const syncingWorkspaceLocationRef = useRef(false);
  const hasProcessedInitialWorkspaceLocationRef = useRef(false);
  const currentHashHistoryStateRef = useRef<unknown>(
    typeof window === 'undefined' ? null : window.history?.state
  );
  const pendingExternalHashNavigationRef = useRef('');
  const quotaRequestVersionsRef = useRef<Map<string, number>>(new Map());
  const identityCopyTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const accountSortDropdownRef = useRef<HTMLDivElement | null>(null);
  const accountSortTriggerRef = useRef<HTMLButtonElement | null>(null);
  const accountSortOptionRefs = useRef<Map<AccountSortFieldValue, HTMLButtonElement | null>>(
    new Map()
  );
  const detailDrawerBodyRef = useRef<HTMLDivElement | null>(null);
  const selectedRowKeyRef = useRef(selectedRowKey);
  const headerSnapshotContextRef = useRef({
    managerServiceBase: featureAvailability.managerServiceBase,
    managementKey,
    connectionFingerprint,
    checking: featureAvailability.checking,
    requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
  });

  useLayoutEffect(() => {
    if (
      featureAvailability.checking ||
      managerStorageAvailable ||
      (operationalFilter !== 'cooldown' && operationalFilter !== 'automation')
    ) {
      return;
    }
    setOperationalFilter('all');
  }, [featureAvailability.checking, managerStorageAvailable, operationalFilter]);

  const loadQuotaCooldowns = useCallback(async () => {
    const requestId = quotaCooldownRequestIdRef.current + 1;
    quotaCooldownRequestIdRef.current = requestId;
    if (!featureAvailability.managerServiceBase) {
      setQuotaCooldowns((current) => (current.size === 0 ? current : new Map()));
      return;
    }

    try {
      const items = await usageServiceApi.getActiveQuotaCooldowns(
        featureAvailability.managerServiceBase,
        managementKey
      );
      const next = new Map<string, QuotaCooldownInfo>();
      for (const item of items) {
        if (!item.authFileName) continue;
        const key = getAuthFileCodexInspectionKeyForIdentity({
          fileName: item.authFileName,
          provider: item.provider,
          authIndex: item.authIndex ?? null,
          accountSnapshot: item.accountSnapshot,
        });
        const existing = next.get(key);
        if (!existing || (item.recoverAtMs ?? 0) > (existing.recoverAtMs ?? 0)) {
          next.set(key, item);
        }
      }
      if (quotaCooldownRequestIdRef.current !== requestId) return;
      setQuotaCooldowns(next);
    } catch {
      // Cooldown badges are a derived hint; keep the last known state on transient failures.
    }
  }, [featureAvailability.managerServiceBase, managementKey]);

  const loadSharedHeaderSnapshots = useHeaderSnapshotsLoader({
    serviceBase:
      !featureAvailability.checking && featureAvailability.requestMonitoringAvailable
        ? featureAvailability.managerServiceBase
        : '',
    managementKey,
  });
  const loadHeaderSnapshots = useCallback(async () => {
    if (featureAvailability.checking) return;
    try {
      await loadSharedHeaderSnapshots();
    } finally {
      if (headerSnapshotLoadKeyRef.current === headerSnapshotLoadKey) {
        setHeaderSnapshotLoadedKey(headerSnapshotLoadKey);
      }
    }
  }, [featureAvailability.checking, headerSnapshotLoadKey, loadSharedHeaderSnapshots]);

  const loadAccountActionCandidates = useCallback(async () => {
    const requestId = accountActionCandidatesReqIdRef.current + 1;
    accountActionCandidatesReqIdRef.current = requestId;

    if (featureAvailability.checking || !featureAvailability.managerServiceBase) {
      accountActionCandidatesRef.current = [];
      setAccountActionCandidates([]);
      setAccountActionCandidatesLoading(false);
      setAccountActionCandidatesError('');
      return;
    }

    setAccountActionCandidatesLoading(true);
    setAccountActionCandidatesError('');
    try {
      const response = await usageServiceApi.listAccountActionCandidates(
        featureAvailability.managerServiceBase,
        managementKey,
        'pending',
        500
      );
      if (accountActionCandidatesReqIdRef.current !== requestId) return;
      const items = response.items ?? [];
      accountActionCandidatesRef.current = items;
      setAccountActionCandidates(items);
    } catch (err: unknown) {
      if (accountActionCandidatesReqIdRef.current !== requestId) return;
      setAccountActionCandidatesError(
        err instanceof Error ? err.message : t('notification.load_failed')
      );
    } finally {
      if (accountActionCandidatesReqIdRef.current === requestId) {
        setAccountActionCandidatesLoading(false);
      }
    }
  }, [featureAvailability.checking, featureAvailability.managerServiceBase, managementKey, t]);

  useLayoutEffect(() => {
    const prev = headerSnapshotContextRef.current;
    if (
      prev.managerServiceBase === featureAvailability.managerServiceBase &&
      prev.managementKey === managementKey &&
      prev.connectionFingerprint === connectionFingerprint &&
      prev.checking === featureAvailability.checking &&
      prev.requestMonitoringAvailable === featureAvailability.requestMonitoringAvailable
    ) {
      return;
    }
    headerSnapshotContextRef.current = {
      managerServiceBase: featureAvailability.managerServiceBase,
      managementKey,
      connectionFingerprint,
      checking: featureAvailability.checking,
      requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
    };
    quotaCooldownRequestIdRef.current += 1;
    accountHistoryAutoAbortRef.current?.abort();
    accountHistoryAutoAbortRef.current = null;
    accountHistoryTargetAbortRef.current?.abort();
    accountHistoryTargetAbortRef.current = null;
    accountHistoryRefreshRequestIdRef.current += 1;
    accountHistoryRefreshPromiseRef.current = null;
    accountHistoryRequestVersionsRef.current.clear();
    accountHistoryPendingRequestsRef.current.clear();
    accountHistoryAutoLoadKeyRef.current = null;
    accountActionCandidatesReqIdRef.current += 1;
    accountWindowUsageReqIdRef.current += 1;
    accountWindowUsageAbortRef.current?.abort();
    accountWindowUsageAbortRef.current = null;
    accountActionCandidatesRef.current = [];
    accountWindowUsageAutoLoadKeyRef.current = null;
    usageValuesRequestIdRef.current += 1;
    usageValuesAutoLoadKeyRef.current = null;
    detailEventsRequestIdRef.current += 1;
    detailEventsAutoLoadKeyRef.current = null;
    setQuotaCooldowns(new Map());
    setAccountActionCandidates([]);
    setHeaderSnapshotLoadedKey('');
    setAccountHistoryByRowKey((current) => (current.size === 0 ? current : new Map()));
    setAccountHistoryLoading(false);
    setAccountHistoryErrorsByRowKey((current) => (current.size === 0 ? current : new Map()));
    setHistoryRefreshing(false);
    setQuotaSnapshotWindowsByRowKey((current) => (current.size === 0 ? current : new Map()));
    setAccountWindowUsageByKey((current) => (current.size === 0 ? current : new Map()));
    setAccountWindowUsageQueryContext(null);
    setAccountWindowUsageError('');
    setUsageRows([]);
    setDetailEventsRowKey(null);
    setDetailEvents([]);
    setDetailEventsSummary(null);
    setDetailEventsRecentFailure(null);
    setDetailEventsTotalCount(0);
    setDetailEventsHasMore(false);
    setDetailEventsNextBeforeMs(null);
    setDetailEventsNextBeforeId(null);
    setDetailEventsLoading(false);
    setDetailEventsRefreshing(false);
    setDetailEventsAppending(false);
    setDetailEventsError('');
  }, [
    connectionFingerprint,
    featureAvailability.checking,
    featureAvailability.managerServiceBase,
    featureAvailability.requestMonitoringAvailable,
    managementKey,
  ]);

  useEffect(
    () => () => {
      accountHistoryAutoAbortRef.current?.abort();
      accountHistoryTargetAbortRef.current?.abort();
      accountWindowUsageAbortRef.current?.abort();
    },
    []
  );

  const loadOauthExcluded = oauthState.loadExcluded;
  const loadOauthModelAlias = oauthState.loadModelAlias;
  const refreshOauthWorkspace = useCallback(async () => {
    await Promise.all([loadOauthExcluded(), loadOauthModelAlias()]);
  }, [loadOauthExcluded, loadOauthModelAlias]);
  const refreshAccountsWorkspace = useCallback(async () => {
    await Promise.all([loadFiles(), loadHeaderSnapshots()]);
  }, [loadFiles, loadHeaderSnapshots]);
  const refreshActiveWorkspace = useAccountsWorkspaceRefresh(activeView, {
    refreshAccounts: refreshAccountsWorkspace,
    refreshHealth: loadInspectionSummary,
    refreshOauth: refreshOauthWorkspace,
  });

  useHeaderRefresh(refreshActiveWorkspace);

  useEffect(() => {
    void loadFiles();
  }, [connectionFingerprint, loadFiles]);

  useEffect(() => {
    if (activeView === 'accounts') return;
    void refreshActiveWorkspace();
  }, [activeView, connectionFingerprint, refreshActiveWorkspace]);

  useEffect(() => {
    if (typeof document === 'undefined') return;
    const updateVisibility = () => setDocumentVisible(document.visibilityState !== 'hidden');
    document.addEventListener('visibilitychange', updateVisibility);
    return () => document.removeEventListener('visibilitychange', updateVisibility);
  }, []);

  useEffect(() => {
    const regainedVisibility = documentVisible && !previousDocumentVisibleRef.current;
    previousDocumentVisibleRef.current = documentVisible;
    if (
      !regainedVisibility ||
      activeView !== 'accounts' ||
      featureAvailability.checking ||
      !featureAvailability.requestMonitoringAvailable ||
      !featureAvailability.managerServiceBase
    ) {
      return;
    }
    void loadHeaderSnapshots();
  }, [
    activeView,
    documentVisible,
    featureAvailability.checking,
    featureAvailability.managerServiceBase,
    featureAvailability.requestMonitoringAvailable,
    loadHeaderSnapshots,
  ]);

  useInterval(
    () => {
      void loadHeaderSnapshots();
    },
    activeView === 'accounts' &&
      documentVisible &&
      !featureAvailability.checking &&
      featureAvailability.requestMonitoringAvailable &&
      Boolean(featureAvailability.managerServiceBase)
      ? PASSIVE_HEADER_SNAPSHOT_REFRESH_MS
      : null
  );

  useEffect(
    () => () => {
      if (identityCopyTimerRef.current !== null) {
        clearTimeout(identityCopyTimerRef.current);
      }
    },
    []
  );

  useEffect(() => {
    if (!isAccountSortDropdownOpen || typeof document === 'undefined') return;

    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Node;
      if (!accountSortDropdownRef.current?.contains(target)) {
        setIsAccountSortDropdownOpen(false);
      }
    };

    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [isAccountSortDropdownOpen]);

  useEffect(() => {
    if (!isAccountSortDropdownOpen || highlightedAccountSortIndex < 0) return;
    const highlightedOption = ACCOUNT_SORT_FIELD_OPTIONS[highlightedAccountSortIndex];
    if (!highlightedOption) return;
    accountSortOptionRefs.current.get(highlightedOption.value)?.focus();
  }, [highlightedAccountSortIndex, isAccountSortDropdownOpen]);

  const handleSavePastedAuthJson = useCallback(
    async (type: AuthJsonInputType, fileName: string, jsonText: string) => {
      await savePastedAuthJson(type, fileName, jsonText);
      setAuthJsonPasteOpen(false);
    },
    [savePastedAuthJson]
  );

  const handleCodexReauthSuccess = useCallback(async () => {
    await loadFiles();
    setCodexReauthTarget(null);
  }, [loadFiles]);

  const handleReauthAccount = useCallback(
    (file: AuthFileItem) => {
      const action = resolveAccountReauthAction(file);
      if (action.kind === 'codex-dialog') {
        setCodexReauthTarget(createCodexReauthTargetFromAuthFile(file));
        return;
      }
      if (action.kind === 'navigate') {
        navigate(action.path);
        return;
      }
      showNotification(
        t('accounts.reauth_unsupported', {
          defaultValue: '该 Provider 暂不支持从凭证工作区重新认证：{{provider}}',
          provider: action.provider,
        }),
        'info'
      );
    },
    [navigate, showNotification, t]
  );

  const headerSnapshotLookup = useMemo(
    () => buildUsageHeaderSnapshotLookup(headerSnapshots),
    [headerSnapshots]
  );
  const getActiveCodexQuota = useCallback(
    (file: AuthFileItem): CodexQuotaState | undefined => {
      if (normalizeAccountProvider(file) !== CODEX_CONFIG.type) return undefined;
      const storeKey = CODEX_CONFIG.getStoreKey?.(file) ?? file.name;
      return (
        getAuthFileScopedCodexQuota(file, codexQuota[storeKey]) ??
        getAuthFileScopedCodexQuota(file, codexQuota[file.name])
      );
    },
    [codexQuota]
  );
  const getFreshCodexHeaderSnapshot = useCallback(
    (file: AuthFileItem): UsageHeaderSnapshot | undefined => {
      const headerSnapshot = getHighConfidenceUsageHeaderSnapshotForAuthFile(
        headerSnapshotLookup,
        file
      );
      return isUsageHeaderQuotaSnapshotExpired(headerSnapshot, headerSnapshotGeneratedAtMs)
        ? undefined
        : headerSnapshot;
    },
    [headerSnapshotGeneratedAtMs, headerSnapshotLookup]
  );
  const getDisplayCodexQuota = useCallback(
    (file: AuthFileItem): CodexQuotaState | undefined => {
      if (normalizeAccountProvider(file) !== CODEX_CONFIG.type) return undefined;
      const activeQuota = getActiveCodexQuota(file);
      const observedQuota = buildObservedCodexQuotaState(
        file,
        getFreshCodexHeaderSnapshot(file),
        t,
        headerSnapshotGeneratedAtMs
      );
      if (activeQuota?.status === 'error' && activeQuota.errorStatus === 401) {
        return activeQuota;
      }
      return resolveQuotaDisplayState(activeQuota, observedQuota);
    },
    [getActiveCodexQuota, getFreshCodexHeaderSnapshot, headerSnapshotGeneratedAtMs, t]
  );
  const getQuotaSnapshotObservation = useCallback(
    (row: AccountRow): AccountQuotaSnapshotObservationInput | undefined => {
      let fetchedAtMs: number | undefined;
      let inventoryMode: AccountQuotaSnapshotObservationInput['inventory_mode'] = 'complete';
      switch (row.provider) {
        case CODEX_CONFIG.type: {
          const state = getActiveCodexQuota(row.raw);
          if (
            state?.status === 'success' &&
            (state.quotaInventoryObserved === true || state.windows.length > 0)
          ) {
            fetchedAtMs = state.fetchedAtMs;
            if (state.quotaInventoryObserved !== true) inventoryMode = 'partial';
            break;
          }
          break;
        }
        case CLAUDE_CONFIG.type: {
          const state = getCredentialScopedQuotaState(baseQuotaStores.claudeQuota, row.raw);
          if (
            state?.status === 'success' &&
            (state.quotaInventoryObserved === true || state.windows.length > 0)
          ) {
            fetchedAtMs = state.fetchedAtMs;
            if (state.quotaInventoryObserved !== true) inventoryMode = 'partial';
          }
          break;
        }
        case ANTIGRAVITY_CONFIG.type: {
          const state = getCredentialScopedQuotaState(baseQuotaStores.antigravityQuota, row.raw);
          if (
            state?.status === 'success' &&
            (state.quotaInventoryObserved === true || state.groups.length > 0)
          ) {
            fetchedAtMs = state.fetchedAtMs;
            if (state.quotaInventoryObserved !== true) inventoryMode = 'partial';
          }
          break;
        }
        case KIMI_CONFIG.type: {
          const state = getCredentialScopedQuotaState(baseQuotaStores.kimiQuota, row.raw);
          if (
            state?.status === 'success' &&
            (state.quotaInventoryObserved === true || state.rows.length > 0)
          ) {
            fetchedAtMs = state.fetchedAtMs;
            if (state.quotaInventoryObserved !== true) inventoryMode = 'partial';
          }
          break;
        }
        case XAI_CONFIG.type: {
          const state = getCredentialScopedQuotaState(baseQuotaStores.xaiQuota, row.raw);
          if (state?.status === 'success' && state.billing && !state.billing.officialApiHealth) {
            fetchedAtMs = state.fetchedAtMs;
            if (state.billing.partial !== false) inventoryMode = 'partial';
          }
          break;
        }
        default:
          return undefined;
      }
      if (typeof fetchedAtMs !== 'number' || !Number.isFinite(fetchedAtMs) || fetchedAtMs <= 0) {
        return undefined;
      }
      return {
        source: 'api_query',
        source_observation_id: `accounts-provider-query:${Math.trunc(fetchedAtMs)}`,
        observed_at_ms: Math.trunc(fetchedAtMs),
        inventory_scope_key:
          row.provider === CODEX_CONFIG.type
            ? 'codex:rate-limits'
            : `${row.provider}:quota-windows`,
        inventory_mode: inventoryMode,
      };
    },
    [baseQuotaStores, getActiveCodexQuota]
  );
  const getCodexHeaderQuotaSnapshotObservation = useCallback(
    (row: AccountRow): AccountQuotaSnapshotObservationInput | undefined => {
      if (row.provider !== CODEX_CONFIG.type) return undefined;
      const activeQuota = getActiveCodexQuota(row.raw);
      if (activeQuota?.status === 'error' && activeQuota.errorStatus === 401) return undefined;
      const headerSnapshot = getFreshCodexHeaderSnapshot(row.raw);
      if (
        !headerSnapshot ||
        !hasUsageHeaderQuotaSignal(headerSnapshot) ||
        !Number.isFinite(headerSnapshot.timestamp_ms) ||
        headerSnapshot.timestamp_ms <= 0
      ) {
        return undefined;
      }
      if (
        activeQuota?.status === 'success' &&
        activeQuota.quotaInventoryObserved === true &&
        typeof activeQuota.fetchedAtMs === 'number' &&
        Number.isFinite(activeQuota.fetchedAtMs) &&
        headerSnapshot.timestamp_ms <= activeQuota.fetchedAtMs
      ) {
        return undefined;
      }
      return {
        source: 'response_header',
        source_observation_id: headerSnapshot.event_hash,
        observed_at_ms: Math.trunc(headerSnapshot.timestamp_ms),
        inventory_scope_key: 'codex:rate-limits',
        inventory_mode: 'partial',
      };
    },
    [getActiveCodexQuota, getFreshCodexHeaderSnapshot]
  );
  const accountQuotaOverrides = useMemo(() => {
    const codexQuotaBySelectionKey = new Map<string, CodexQuotaState>();
    const codexHeaderSnapshotBySelectionKey = new Map<string, UsageHeaderSnapshot>();
    files.forEach((file) => {
      const selectionKey = getAuthFileSelectionKey(file);
      const headerSnapshot = getFreshCodexHeaderSnapshot(file);
      if (headerSnapshot) {
        codexHeaderSnapshotBySelectionKey.set(selectionKey, headerSnapshot);
      }
      const quota = getDisplayCodexQuota(file);
      if (quota) {
        codexQuotaBySelectionKey.set(selectionKey, quota);
      }
    });
    return { codexQuotaBySelectionKey, codexHeaderSnapshotBySelectionKey };
  }, [files, getDisplayCodexQuota, getFreshCodexHeaderSnapshot]);

  const rows = useMemo(
    () => buildAccountRows(files, baseQuotaStores, inspectionResults, accountQuotaOverrides),
    [accountQuotaOverrides, baseQuotaStores, files, inspectionResults]
  );
  const codexStatusBySelectionKey = useMemo(() => {
    const statusMap = new Map<string, ReturnType<typeof getAuthFileCodexStatus>>();
    rows.forEach((row) => {
      if (row.provider !== CODEX_CONFIG.type && row.provider !== XAI_CONFIG.type) return;
      const activeQuota =
        row.provider === CODEX_CONFIG.type ? getActiveCodexQuota(row.raw) : undefined;
      const freshHeaderSnapshot = getFreshCodexHeaderSnapshot(row.raw);
      const sources = getFreshAuthFileCodexStatusSources(
        row.raw,
        activeQuota,
        toAuthFileCodexInspectionSnapshot(row),
        freshHeaderSnapshot
      );
      const statusQuota =
        row.provider === CODEX_CONFIG.type
          ? resolveQuotaDisplayState(
              activeQuota,
              buildObservedCodexQuotaState(
                row.raw,
                sources.headerSnapshot,
                t,
                headerSnapshotGeneratedAtMs
              )
            )
          : undefined;
      statusMap.set(
        row.selectionKey,
        getAuthFileCodexStatus(row.raw, statusQuota, sources.inspection, sources.headerSnapshot)
      );
    });
    return statusMap;
  }, [getActiveCodexQuota, getFreshCodexHeaderSnapshot, headerSnapshotGeneratedAtMs, rows, t]);
  const actionCandidatesByRowKey = useMemo(
    () =>
      buildAccountOperationalItemsByRowKey(
        rows,
        accountActionCandidates.filter((candidate) => candidate.status === 'pending')
      ),
    [accountActionCandidates, rows]
  );
  const quotaCooldownsByRowKey = useMemo(
    () =>
      buildAccountOperationalItemsByRowKey(
        rows.filter(
          (row) => row.provider === CODEX_CONFIG.type || row.provider === XAI_CONFIG.type
        ),
        Array.from(quotaCooldowns.values())
      ),
    [quotaCooldowns, rows]
  );
  const metrics = useMemo(
    () =>
      buildAccountMetrics(rows, {
        pendingActionsByRowKey: actionCandidatesByRowKey,
        quotaCooldownsByRowKey,
      }),
    [actionCandidatesByRowKey, quotaCooldownsByRowKey, rows]
  );
  const providerOptions = useMemo(() => getProviderOptions(rows), [rows]);
  const planOptions = useMemo(() => getPlanOptions(rows), [rows]);
  const recommendations = useMemo(() => buildAccountRecommendations(rows), [rows]);
  const recommendationBySelectionKey = useMemo(
    () => buildRecommendationBySelectionKey(recommendations),
    [recommendations]
  );
  const baseFilteredRows = useMemo(
    () =>
      filterAccountRows(rows, {
        provider: providerFilter,
        status: statusFilter,
        plan: planFilter,
        quotaBand: quotaBandFilter,
        search,
        codexStatusBySelectionKey,
      }),
    [
      codexStatusBySelectionKey,
      planFilter,
      providerFilter,
      quotaBandFilter,
      rows,
      search,
      statusFilter,
    ]
  );
  const filteredRows = useMemo(() => {
    const operationalRows = baseFilteredRows.filter((row) => {
      if (effectiveOperationalFilter === 'all') return true;
      if (effectiveOperationalFilter === 'reauth') {
        const recommendation = recommendationBySelectionKey.get(row.selectionKey);
        const statusMessage = row.statusMessage.trim().toLowerCase();
        return (
          recommendation?.action === 'reauth' ||
          row.inspection?.action === 'reauth' ||
          row.inspection?.statusCode === 401 ||
          ['unauthorized', 'unauthenticated', 'expired', 'token_expired'].includes(statusMessage)
        );
      }
      if (effectiveOperationalFilter === 'cooldown') {
        return (quotaCooldownsByRowKey.get(row.selectionKey)?.length ?? 0) > 0;
      }
      if (effectiveOperationalFilter === 'automation') {
        return (actionCandidatesByRowKey.get(row.selectionKey)?.length ?? 0) > 0;
      }
      return row.disabled && row.quota.status === 'ok' && !row.quota.error;
    });
    return sortAccountRows(operationalRows, accountSort);
  }, [
    accountSort,
    actionCandidatesByRowKey,
    baseFilteredRows,
    effectiveOperationalFilter,
    quotaCooldownsByRowKey,
    recommendationBySelectionKey,
  ]);

  const totalPages = Math.max(1, Math.ceil(filteredRows.length / pageSize));
  const currentPage = Math.min(page, totalPages);
  const pageRows = useMemo(
    () => filteredRows.slice((currentPage - 1) * pageSize, currentPage * pageSize),
    [currentPage, filteredRows, pageSize]
  );
  const paginationStartItem = filteredRows.length === 0 ? 0 : (currentPage - 1) * pageSize + 1;
  const paginationEndItem = Math.min(filteredRows.length, currentPage * pageSize);
  const pageAuthFiles = useMemo(() => pageRows.map((row) => row.raw), [pageRows]);
  const filteredAuthFiles = useMemo(() => filteredRows.map((row) => row.raw), [filteredRows]);
  const selectablePageRows = useMemo(() => pageRows.filter((row) => !row.runtimeOnly), [pageRows]);
  const selectableFilteredRows = useMemo(
    () => filteredRows.filter((row) => !row.runtimeOnly),
    [filteredRows]
  );
  const selectedRows = useMemo(
    () => rows.filter((row) => selectedFiles.has(row.selectionKey)),
    [rows, selectedFiles]
  );
  const selectedTargetFiles = useMemo(
    () => selectedRows.filter((row) => !row.runtimeOnly).map((row) => row.raw),
    [selectedRows]
  );
  const selectedFileNames = useMemo(
    () => Array.from(new Set(selectedTargetFiles.map((file) => file.name))),
    [selectedTargetFiles]
  );
  const selectedHasPartialSharedAuthFile = useMemo(
    () => hasPartialSharedAuthFileSelection(files, selectedFiles),
    [files, selectedFiles]
  );
  const selectedCodexRows = useMemo(
    () => selectedRows.filter((row) => !row.runtimeOnly && row.provider === CODEX_CONFIG.type),
    [selectedRows]
  );
  const selectedRow = useMemo(
    () => rows.find((row) => row.selectionKey === selectedRowKey) ?? null,
    [rows, selectedRowKey]
  );
  const accountHistoryTargets = useMemo(() => {
    const targetRows = [...pageRows];
    if (selectedRow && !targetRows.some((row) => row.selectionKey === selectedRow.selectionKey)) {
      targetRows.push(selectedRow);
    }
    return buildAccountHistoryTargetEntries(targetRows);
  }, [pageRows, selectedRow]);
  const accountHistoryAutoLoadKey = useMemo(
    () =>
      JSON.stringify({
        checking: featureAvailability.checking,
        managerServiceBase: featureAvailability.managerServiceBase,
        managementKey,
        requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
        targets: accountHistoryTargets.map((entry) => entry.target),
      }),
    [
      accountHistoryTargets,
      featureAvailability.checking,
      featureAvailability.managerServiceBase,
      featureAvailability.requestMonitoringAvailable,
      managementKey,
    ]
  );
  const usageValuesAutoLoadKey = useMemo(
    () =>
      JSON.stringify({
        checking: featureAvailability.checking,
        connectionFingerprint,
        historyRevision: accountHistoryRefreshRevision,
        managerServiceBase: featureAvailability.managerServiceBase,
        managementKey,
        requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
        selectedRowKey: selectedRow?.selectionKey ?? selectedRowKey,
      }),
    [
      accountHistoryRefreshRevision,
      connectionFingerprint,
      featureAvailability.checking,
      featureAvailability.managerServiceBase,
      featureAvailability.requestMonitoringAvailable,
      managementKey,
      selectedRow,
      selectedRowKey,
    ]
  );
  const detailEventsAutoLoadKey = useMemo(
    () =>
      JSON.stringify({
        checking: featureAvailability.checking,
        connectionFingerprint,
        managerServiceBase: featureAvailability.managerServiceBase,
        managementKey,
        requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
        selectedRowKey: selectedRow?.selectionKey ?? selectedRowKey,
      }),
    [
      connectionFingerprint,
      featureAvailability.checking,
      featureAvailability.managerServiceBase,
      featureAvailability.requestMonitoringAvailable,
      managementKey,
      selectedRow,
      selectedRowKey,
    ]
  );
  const selectedSourceMemberCount = useMemo(() => {
    const fileName = selectedRow?.fileName.trim();
    if (!fileName) return 0;
    return files.filter((file) => String(file.name ?? '').trim() === fileName).length;
  }, [files, selectedRow?.fileName]);
  const disableControls = connectionStatus !== 'connected';
  const handleConfigurationSaved = useCallback(() => {
    if (!selectedRow) return;
    invalidateModels(selectedRow.raw);
    if (detailTab === 'models') {
      void refreshModels(selectedRow.raw);
    }
  }, [detailTab, invalidateModels, refreshModels, selectedRow]);
  const configurationEditor = useAuthFileConfigurationEditor({
    file: selectedRow && !selectedRow.runtimeOnly ? selectedRow.raw : null,
    enabled:
      activeView === 'accounts' &&
      (detailTab === 'config' || detailTab === 'models') &&
      Boolean(selectedRow),
    disableControls,
    sourceMemberCount: selectedSourceMemberCount,
    connectionKey: connectionFingerprint,
    loadFiles,
    onSaved: handleConfigurationSaved,
  });
  const configurationDirty = configurationEditor.dirty;
  const configurationReload = configurationEditor.reload;
  const configurationSaving = configurationEditor.state?.saving === true;
  const handleAccountModelsRefresh = useCallback(async () => {
    if (!selectedRow) return;
    await Promise.all([
      refreshModels(selectedRow.raw),
      loadOauthExcluded(),
      loadOauthModelAlias(),
      ...(!configurationDirty && !configurationSaving && !selectedRow.runtimeOnly
        ? [configurationReload()]
        : []),
    ]);
  }, [
    configurationDirty,
    configurationReload,
    configurationSaving,
    loadOauthExcluded,
    loadOauthModelAlias,
    refreshModels,
    selectedRow,
  ]);
  const shouldBlockConfigurationNavigation = useCallback<BlockerFunction>(
    ({ currentLocation, nextLocation }) => {
      if (!configurationEditor.dirty && !configurationSaving) return false;
      if (currentLocation.pathname !== nextLocation.pathname) return true;

      const current = readAccountsWorkspaceUrlState(
        currentLocation.search,
        DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      );
      const next = readAccountsWorkspaceUrlState(
        nextLocation.search,
        DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      );
      return !(
        current.view === 'accounts' &&
        next.view === 'accounts' &&
        Boolean(current.account) &&
        current.account === next.account &&
        isConfigurationDetailTab(current.detailTab) &&
        isConfigurationDetailTab(next.detailTab)
      );
    },
    [configurationEditor.dirty, configurationSaving]
  );
  const handleRouterNavigationConfirm = useCallback((): boolean => {
    if (configurationSaving) {
      showNotification(t('accounts.config_save_in_progress'), 'info');
      return false;
    }
    configurationEditor.reset();
    return true;
  }, [configurationEditor, configurationSaving, showNotification, t]);
  const { allowNextNavigation, allowNavigationTo } = useUnsavedChangesGuard({
    enabled: activeView === 'accounts' && (detailTab === 'config' || detailTab === 'models'),
    shouldBlock: shouldBlockConfigurationNavigation,
    onConfirmNavigation: handleRouterNavigationConfirm,
    dialog: {
      title: t('common.unsaved_changes_title'),
      message: t('common.unsaved_changes_message'),
      confirmText: t('common.leave'),
      cancelText: t('common.stay'),
      variant: 'danger',
    },
  });
  const discardConfigurationRequestRef = useRef<Promise<boolean> | null>(null);
  const confirmConfigurationDiscard = useCallback(
    (resetDraftOnConfirm = true): Promise<boolean> => {
      if (configurationEditor.state?.saving) {
        showNotification(t('accounts.config_save_in_progress'), 'info');
        return Promise.resolve(false);
      }
      if (!configurationEditor.dirty) return Promise.resolve(true);
      if (discardConfigurationRequestRef.current) {
        return Promise.resolve(false);
      }

      const request = new Promise<boolean>((resolve) => {
        showConfirmation({
          title: t('common.unsaved_changes_title'),
          message: t('common.unsaved_changes_message'),
          confirmText: t('common.leave'),
          cancelText: t('common.stay'),
          variant: 'danger',
          onConfirm: () => {
            if (resetDraftOnConfirm) configurationEditor.reset();
            resolve(true);
          },
          onCancel: () => resolve(false),
        });
      });
      discardConfigurationRequestRef.current = request;
      void request.finally(() => {
        if (discardConfigurationRequestRef.current === request) {
          discardConfigurationRequestRef.current = null;
        }
      });
      return request;
    },
    [configurationEditor, showConfirmation, showNotification, t]
  );
  const handleAccountDelete = useCallback(
    async (item: AuthFileItem) => {
      const deletesOpenCredential = getAuthFileSelectionKey(item) === selectedRowKey;
      if (deletesOpenCredential && !(await confirmConfigurationDiscard(false))) return;
      handleDelete(item);
    },
    [confirmConfigurationDiscard, handleDelete, selectedRowKey]
  );
  const hasSelectedAccountDetail = activeView === 'accounts' && Boolean(selectedRowKey);
  const needsCodexStatusEvidence =
    activeView === 'accounts' && isAccountCodexStatusFilter(statusFilter);
  const needsQuotaCooldowns =
    managerStorageAvailable &&
    activeView === 'accounts' &&
    (effectiveOperationalFilter === 'cooldown' ||
      (hasSelectedAccountDetail && (detailTab === 'overview' || detailTab === 'quota')));
  const needsActionCandidates =
    managerStorageAvailable &&
    activeView === 'accounts' &&
    (effectiveOperationalFilter === 'automation' ||
      (hasSelectedAccountDetail && (detailTab === 'overview' || detailTab === 'diagnostics')));
  const canLoadHeaderSnapshots =
    activeView === 'accounts' &&
    !featureAvailability.checking &&
    featureAvailability.requestMonitoringAvailable &&
    Boolean(featureAvailability.managerServiceBase);
  const needsHeaderSnapshots =
    canLoadHeaderSnapshots && headerSnapshotLoadedKey !== headerSnapshotLoadKey;

  useEffect(() => {
    if (!needsQuotaCooldowns) return;
    void loadQuotaCooldowns();
  }, [loadQuotaCooldowns, needsQuotaCooldowns]);

  useEffect(() => {
    if (!needsActionCandidates) return;
    void loadAccountActionCandidates();
  }, [loadAccountActionCandidates, needsActionCandidates]);

  useEffect(() => {
    if (!needsHeaderSnapshots) return;
    void loadHeaderSnapshots();
  }, [loadHeaderSnapshots, needsHeaderSnapshots]);

  useEffect(() => {
    if (!needsCodexStatusEvidence || featureAvailability.checking) return;
    void loadInspectionSummary();
  }, [featureAvailability.checking, loadInspectionSummary, needsCodexStatusEvidence]);

  useEffect(() => {
    if (activeView !== 'accounts' || detailTab !== 'models' || !selectedRow) {
      modelsLoadKeyRef.current = '';
      return;
    }

    const loadKey = `${connectionFingerprint ?? ''}\u0000${selectedRow.selectionKey}`;
    if (modelsLoadKeyRef.current === loadKey) return;
    modelsLoadKeyRef.current = loadKey;
    void showModels(selectedRow.raw);
  }, [activeView, connectionFingerprint, detailTab, selectedRow, showModels]);

  useEffect(() => {
    if (activeView !== 'accounts' || detailTab !== 'models' || !selectedRow) {
      modelRulesLoadKeyRef.current = '';
      return;
    }

    const loadKey = `${connectionFingerprint}\u0000models`;
    if (modelRulesLoadKeyRef.current === loadKey) return;
    modelRulesLoadKeyRef.current = loadKey;
    void Promise.all([loadOauthExcluded(), loadOauthModelAlias()]);
  }, [
    activeView,
    connectionFingerprint,
    detailTab,
    loadOauthExcluded,
    loadOauthModelAlias,
    selectedRow,
  ]);

  const handleAccountCardClick = useCallback(
    (row: AccountRow) => {
      if (isSelectionMode) {
        if (!row.runtimeOnly) {
          toggleSelect(row.selectionKey);
        }
      }
    },
    [isSelectionMode, toggleSelect]
  );
  const cancelSelectionMode = useCallback(() => {
    deselectAll();
    setIsSelectionMode(false);
  }, [deselectAll]);
  const getDisplayText = useCallback(
    (value: string) => (accountDisplayMode === 'full' ? value : maskQuotaAccountText(value)),
    [accountDisplayMode]
  );
  const getDisplayAccount = useCallback(
    (row: AccountRow) => getDisplayText(row.accountLabel),
    [getDisplayText]
  );
  const getDisplayFileName = useCallback(
    (fileName: string) => getDisplayText(fileName),
    [getDisplayText]
  );
  const translateQuotaWindowLabel = useCallback(
    (
      label: string | undefined,
      labelKey?: string,
      labelParams?: Record<string, string | number>
    ) => {
      if (!labelKey) return label || t('accounts.col_quota');
      return t(labelKey, {
        defaultValue: label || labelKey,
        ...labelParams,
      });
    },
    [t]
  );
  const buildQuotaDisplayWindows = useCallback(
    (row: AccountRow): AccountQuotaDisplayWindow[] => {
      return buildAccountQuotaDisplayWindows(row, {
        stores: baseQuotaStores,
        getDisplayCodexQuota,
        translateQuotaWindowLabel,
        t,
      });
    },
    [baseQuotaStores, getDisplayCodexQuota, t, translateQuotaWindowLabel]
  );
  const buildCodexSnapshotDefinitions = useCallback(
    (row: AccountRow, quota: CodexQuotaState | undefined): AccountQuotaWindowDefinition[] =>
      buildAccountQuotaWindowDefinitions(
        buildAccountQuotaDisplayWindows(row, {
          stores: baseQuotaStores,
          getDisplayCodexQuota: () => quota,
          translateQuotaWindowLabel,
          t,
        })
      ).filter((definition) => definition.provider === CODEX_CONFIG.type),
    [baseQuotaStores, t, translateQuotaWindowLabel]
  );
  const quotaDisplayWindowsByRowKey = useMemo(() => {
    const result = new Map<string, AccountQuotaDisplayWindow[]>();
    pageRows.forEach((row) => {
      result.set(row.selectionKey, buildQuotaDisplayWindows(row));
    });
    if (selectedRow && !result.has(selectedRow.selectionKey)) {
      result.set(selectedRow.selectionKey, buildQuotaDisplayWindows(selectedRow));
    }
    return result;
  }, [buildQuotaDisplayWindows, pageRows, selectedRow]);
  const quotaWindowDefinitionsByRowKey = useMemo(() => {
    const result = new Map<string, AccountQuotaWindowDefinition[]>();
    quotaDisplayWindowsByRowKey.forEach((windows, rowKey) => {
      result.set(rowKey, buildAccountQuotaWindowDefinitions(windows));
    });
    return result;
  }, [quotaDisplayWindowsByRowKey]);
  const effectiveQuotaWindowDefinitionsByRowKey = useMemo(() => {
    const result = new Map<string, AccountQuotaWindowDefinition[]>();
    quotaWindowDefinitionsByRowKey.forEach((definitions, rowKey) => {
      const provider = selectedRow?.selectionKey === rowKey ? selectedRow.provider : undefined;
      result.set(
        rowKey,
        mergeAccountQuotaSnapshotWindows(
          definitions,
          quotaSnapshotWindowsByRowKey.get(rowKey) ?? [],
          {
            provider,
            getLabel: (snapshot) => {
              const kind = snapshot.window_kind;
              if (kind === 'rolling_24h') {
                return t('accounts.detail_snapshot_window_rolling_24h');
              }
              if (kind === 'five_hour') return t('accounts.detail_snapshot_window_five_hour');
              if (kind === 'daily') return t('accounts.detail_snapshot_window_daily');
              if (kind === 'weekly') return t('accounts.detail_snapshot_window_weekly');
              if (kind === 'monthly') return t('accounts.detail_snapshot_window_monthly');
              return snapshot.provider_window_id;
            },
          }
        )
      );
    });
    return result;
  }, [quotaSnapshotWindowsByRowKey, quotaWindowDefinitionsByRowKey, selectedRow, t]);
  const accountWindowUsageTargets = useMemo(() => {
    if (!selectedRow) return [];
    const windowsByRowKey = new Map<string, AccountQuotaWindowDefinition[]>();
    windowsByRowKey.set(
      selectedRow.selectionKey,
      effectiveQuotaWindowDefinitionsByRowKey.get(selectedRow.selectionKey) ??
        buildAccountQuotaWindowDefinitions(buildQuotaDisplayWindows(selectedRow))
    );
    const asOfMs =
      accountWindowUsageQueryContext?.rowKey === selectedRow.selectionKey
        ? accountWindowUsageQueryContext.asOfMs
        : undefined;
    return buildAccountWindowUsageTargetEntries([selectedRow], windowsByRowKey, asOfMs);
  }, [
    accountWindowUsageQueryContext,
    buildQuotaDisplayWindows,
    effectiveQuotaWindowDefinitionsByRowKey,
    selectedRow,
  ]);
  const matchingAccountWindowUsageByKey = useMemo(
    () =>
      filterAccountWindowUsageByTargetRanges(accountWindowUsageTargets, accountWindowUsageByKey),
    [accountWindowUsageByKey, accountWindowUsageTargets]
  );
  const selectedHeaderSnapshotRevision = useMemo(() => {
    if (selectedRow?.provider !== CODEX_CONFIG.type) return '';
    const snapshot = getFreshCodexHeaderSnapshot(selectedRow.raw);
    return snapshot ? `${snapshot.event_hash}\u0000${snapshot.timestamp_ms}` : '';
  }, [getFreshCodexHeaderSnapshot, selectedRow]);
  const accountWindowUsageAutoLoadKey = useMemo(
    () =>
      JSON.stringify({
        checking: featureAvailability.checking,
        managerServiceBase: featureAvailability.managerServiceBase,
        managementKey,
        requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
        selectedRowKey: selectedRow?.selectionKey ?? selectedRowKey,
        selectedHeaderSnapshotRevision,
        historyRevision: accountHistoryRefreshRevision,
        quotaRevision: accountQuotaRefreshRevision,
      }),
    [
      accountHistoryRefreshRevision,
      accountQuotaRefreshRevision,
      featureAvailability.checking,
      featureAvailability.managerServiceBase,
      featureAvailability.requestMonitoringAvailable,
      managementKey,
      selectedRow,
      selectedHeaderSnapshotRevision,
      selectedRowKey,
    ]
  );
  const accountDisplayHint = t(
    accountDisplayMode === 'masked'
      ? 'quota_management.show_full_credentials_hint'
      : 'quota_management.show_masked_credentials_hint'
  );
  const AccountDisplayIcon = accountDisplayMode === 'masked' ? IconEyeOff : IconEye;
  const oauthPreviewProviders = useMemo(
    () =>
      getOAuthRulePreviewProviders({
        providers: providerOptions,
        excluded: oauthState.excluded,
        aliases: oauthState.modelAlias,
      }),
    [oauthState.excluded, oauthState.modelAlias, providerOptions]
  );
  const oauthPreviewProviderOptions = useMemo(
    () => [
      { value: '', label: t('accounts.oauth_preview_provider_all') },
      ...oauthPreviewProviders.map((provider) => ({
        value: provider,
        label: getProviderLabel(provider, t),
      })),
    ],
    [oauthPreviewProviders, t]
  );
  const oauthPreviewRows = useMemo(
    () =>
      buildOAuthRulePreviewRows({
        providers: providerOptions,
        excluded: oauthState.excluded,
        aliases: oauthState.modelAlias,
        inputModel: oauthPreviewModel,
      }),
    [oauthPreviewModel, oauthState.excluded, oauthState.modelAlias, providerOptions]
  );
  const { affectedRows: oauthAffectedPreviewRows, directRows: oauthDirectPreviewRows } = useMemo(
    () => partitionOAuthRulePreviewRows(oauthPreviewRows, oauthPreviewProvider),
    [oauthPreviewProvider, oauthPreviewRows]
  );

  useEffect(() => {
    setPage(1);
  }, [
    operationalFilter,
    pageSize,
    planFilter,
    providerFilter,
    quotaBandFilter,
    search,
    statusFilter,
  ]);

  useEffect(() => {
    writeAccountsWorkspaceUiState({
      search,
      providerFilter,
      statusFilter,
      planFilter,
      quotaBandFilter,
      operationalFilter,
      accountSort,
      pageSize,
      accountDisplayMode,
    });
  }, [
    accountDisplayMode,
    accountSort,
    operationalFilter,
    pageSize,
    planFilter,
    providerFilter,
    quotaBandFilter,
    search,
    statusFilter,
  ]);

  const workspaceUrlState = useMemo(
    () => ({
      search,
      providerFilter,
      statusFilter,
      planFilter,
      quotaBandFilter,
      operationalFilter,
      accountSort,
      pageSize,
      accountDisplayMode,
      view: activeView,
      healthMode,
      account: selectedRowKey,
      detailTab,
      editor:
        oauthExcludedEditorProvider !== null
          ? ('excluded' as const)
          : oauthModelAliasEditorProvider !== null
            ? ('alias' as const)
            : null,
      editorProvider: oauthExcludedEditorProvider ?? oauthModelAliasEditorProvider ?? '',
    }),
    [
      accountDisplayMode,
      accountSort,
      activeView,
      detailTab,
      healthMode,
      oauthExcludedEditorProvider,
      oauthModelAliasEditorProvider,
      operationalFilter,
      pageSize,
      planFilter,
      providerFilter,
      quotaBandFilter,
      search,
      selectedRowKey,
      statusFilter,
    ]
  );

  const openAccountDetail = useCallback(
    async (row: AccountRow, tab: DetailTab = 'overview') => {
      const preservesConfigurationDraft =
        row.selectionKey === selectedRowKey &&
        (detailTab === 'config' || detailTab === 'models') &&
        (tab === 'config' || tab === 'models');
      const hadDirtyConfiguration = configurationEditor.dirty;
      if (!preservesConfigurationDraft && !(await confirmConfigurationDiscard())) return;
      if (
        hadDirtyConfiguration &&
        (!preservesConfigurationDraft || row.selectionKey !== selectedRowKey || tab !== detailTab)
      ) {
        allowNextNavigation();
      }

      const searchValue = writeAccountsWorkspaceUrlSearch(
        location.search,
        {
          ...workspaceUrlState,
          view: 'accounts',
          account: row.selectionKey,
          detailTab: tab,
        },
        DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      );
      setSelectedRowKey(row.selectionKey);
      setDetailTab(tab);
      if (searchValue !== location.search) {
        lastWorkspaceNavigationRef.current = searchValue;
        navigate({ pathname: location.pathname, search: searchValue }, { replace: true });
      }
    },
    [
      allowNextNavigation,
      configurationEditor.dirty,
      confirmConfigurationDiscard,
      detailTab,
      location.pathname,
      location.search,
      navigate,
      selectedRowKey,
      workspaceUrlState,
    ]
  );

  const closeAccountDetail = useCallback(() => {
    setSelectedRowKey(null);
    setDetailTab('overview');
    const searchValue = writeAccountsWorkspaceUrlSearch(
      location.search,
      { ...workspaceUrlState, account: null, detailTab: 'overview' },
      DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
    );
    lastWorkspaceNavigationRef.current = searchValue;
    allowNextNavigation();
    navigate({ pathname: location.pathname, search: searchValue }, { replace: true });
  }, [allowNextNavigation, location.pathname, location.search, navigate, workspaceUrlState]);

  const applyWorkspaceUrlState = useCallback(
    (searchValue: string, fallback: AccountsWorkspaceUiState) => {
      const next = readAccountsWorkspaceUrlState(searchValue, fallback);
      const requestedView = new URLSearchParams(searchValue).get('view');
      syncingWorkspaceLocationRef.current = !requestedView || requestedView === next.view;
      lastWorkspaceNavigationRef.current = null;
      setActiveView(next.view);
      setHealthMode(next.healthMode);
      setSearch(next.search);
      setProviderFilter(next.providerFilter);
      setStatusFilter(next.statusFilter);
      setPlanFilter(next.planFilter);
      setQuotaBandFilter(next.quotaBandFilter);
      setOperationalFilter(next.operationalFilter);
      setAccountSort(next.accountSort);
      setPageSize(next.pageSize);
      setAccountDisplayMode(next.accountDisplayMode);
      setSelectedRowKey(next.account);
      setDetailTab(next.detailTab);
      setOauthExcludedEditorProvider(next.editor === 'excluded' ? next.editorProvider : null);
      setOauthModelAliasEditorProvider(next.editor === 'alias' ? next.editorProvider : null);
    },
    []
  );

  useEffect(() => {
    const fallback = hasProcessedInitialWorkspaceLocationRef.current
      ? DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      : initialWorkspaceUiState.current;
    hasProcessedInitialWorkspaceLocationRef.current = true;
    applyWorkspaceUrlState(location.search, fallback);
  }, [applyWorkspaceUrlState, location.search]);

  useEffect(() => {
    if (typeof window === 'undefined') return;
    currentHashHistoryStateRef.current = window.history?.state;
  }, [location.hash, location.pathname, location.search]);

  useEffect(() => {
    if (syncingWorkspaceLocationRef.current) {
      syncingWorkspaceLocationRef.current = false;
      return;
    }
    const nextSearch = writeAccountsWorkspaceUrlSearch(
      location.search,
      workspaceUrlState,
      DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
    );
    if (nextSearch === location.search) {
      lastWorkspaceNavigationRef.current = null;
      return;
    }
    if (lastWorkspaceNavigationRef.current === nextSearch) return;
    lastWorkspaceNavigationRef.current = nextSearch;
    navigate({ pathname: location.pathname, search: nextSearch }, { replace: true });
  }, [location.pathname, location.search, navigate, workspaceUrlState]);

  useEffect(() => {
    if (typeof window === 'undefined') return undefined;
    const syncWorkspaceFromHash = () => {
      if (configurationEditor.dirty || configurationEditor.state?.saving) return;
      applyWorkspaceUrlState(
        readAccountsSearchFromHash(window.location.hash),
        DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      );
    };
    window.addEventListener('hashchange', syncWorkspaceFromHash);
    return () => window.removeEventListener('hashchange', syncWorkspaceFromHash);
  }, [applyWorkspaceUrlState, configurationEditor.dirty, configurationEditor.state?.saving]);

  useEffect(() => {
    if (
      typeof window === 'undefined' ||
      !window.history ||
      typeof window.history.replaceState !== 'function'
    ) {
      return undefined;
    }

    const interceptExternalHashNavigation = (event: Event) => {
      const historyState = window.history.state as { idx?: unknown } | null;
      if (typeof historyState?.idx === 'number') return;

      const targetHash = window.location.hash;
      const currentHash = `#${location.pathname}${location.search}${location.hash || ''}`;
      if (!targetHash || targetHash === currentHash) return;

      event.stopImmediatePropagation();
      window.history.replaceState(currentHashHistoryStateRef.current, '', currentHash);

      const target = targetHash.startsWith('#') ? targetHash.slice(1) || '/' : targetHash;
      if (pendingExternalHashNavigationRef.current) return;
      pendingExternalHashNavigationRef.current = target;
      const finish = () => {
        if (pendingExternalHashNavigationRef.current === target) {
          pendingExternalHashNavigationRef.current = '';
        }
      };
      const navigateToTarget = () => {
        allowNavigationTo(target);
        navigate(target, { replace: true });
      };

      if (!configurationEditor.dirty && !configurationSaving) {
        navigateToTarget();
        finish();
        return;
      }

      void confirmConfigurationDiscard()
        .then((allowed) => {
          if (allowed) navigateToTarget();
        })
        .finally(finish);
    };

    window.addEventListener('popstate', interceptExternalHashNavigation, true);
    window.addEventListener('hashchange', interceptExternalHashNavigation, true);
    return () => {
      window.removeEventListener('popstate', interceptExternalHashNavigation, true);
      window.removeEventListener('hashchange', interceptExternalHashNavigation, true);
    };
  }, [
    allowNavigationTo,
    configurationEditor.dirty,
    configurationSaving,
    confirmConfigurationDiscard,
    location.hash,
    location.pathname,
    location.search,
    navigate,
  ]);

  const changeActiveView = useCallback(
    async (view: AccountsView) => {
      const hadDirtyConfiguration = configurationEditor.dirty;
      if (!(await confirmConfigurationDiscard())) return;
      setActiveView(view);
      const searchValue = writeAccountsWorkspaceUrlSearch(
        location.search,
        { ...workspaceUrlState, view },
        DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      );
      lastWorkspaceNavigationRef.current = searchValue;
      if (hadDirtyConfiguration) allowNextNavigation();
      navigate(
        {
          pathname: location.pathname,
          search: searchValue,
        },
        { replace: false }
      );
    },
    [
      allowNextNavigation,
      configurationEditor.dirty,
      confirmConfigurationDiscard,
      location.pathname,
      location.search,
      navigate,
      workspaceUrlState,
    ]
  );

  const changeHealthMode = useCallback(
    (mode: CredentialHealthInspectionMode) => {
      setHealthMode(mode);
      const searchValue = writeAccountsWorkspaceUrlSearch(
        location.search,
        { ...workspaceUrlState, healthMode: mode },
        DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      );
      lastWorkspaceNavigationRef.current = searchValue;
      navigate({ pathname: location.pathname, search: searchValue }, { replace: true });
    },
    [location.pathname, location.search, navigate, workspaceUrlState]
  );

  const handleInspectionCredentialsChanged = useCallback(async () => {
    await loadFiles();
  }, [loadFiles]);

  const handleOpenInspectionCredential = useCallback(
    async (target: CredentialInspectionTarget) => {
      const targetRow = findAccountRowForInspectionTarget(rows, target);
      if (!targetRow) {
        showNotification(t('accounts.inspection_credential_not_found'), 'warning');
        return;
      }
      const hadDirtyConfiguration = configurationEditor.dirty;
      if (!(await confirmConfigurationDiscard())) return;

      setActiveView('accounts');
      setSelectedRowKey(targetRow.selectionKey);
      setDetailTab('diagnostics');
      const searchValue = writeAccountsWorkspaceUrlSearch(
        location.search,
        {
          ...workspaceUrlState,
          view: 'accounts',
          account: targetRow.selectionKey,
          detailTab: 'diagnostics',
        },
        DEFAULT_ACCOUNTS_WORKSPACE_UI_STATE
      );
      lastWorkspaceNavigationRef.current = searchValue;
      if (hadDirtyConfiguration) allowNextNavigation();
      navigate({ pathname: location.pathname, search: searchValue }, { replace: false });
    },
    [
      allowNextNavigation,
      configurationEditor.dirty,
      confirmConfigurationDiscard,
      location.pathname,
      location.search,
      navigate,
      rows,
      showNotification,
      t,
      workspaceUrlState,
    ]
  );

  useEffect(() => {
    if (!selectedRowKey) {
      setDetailTab('overview');
    }
  }, [selectedRowKey]);

  useLayoutEffect(() => {
    selectedRowKeyRef.current = selectedRowKey;
    accountHistoryTargetAbortRef.current?.abort();
    accountHistoryTargetAbortRef.current = null;
    accountHistoryRefreshRequestIdRef.current += 1;
    accountHistoryRefreshPromiseRef.current = null;
    setHistoryRefreshing(false);
    usageValuesRequestIdRef.current += 1;
    usageValuesAutoLoadKeyRef.current = null;
    accountWindowUsageAutoLoadKeyRef.current = null;
    setAccountWindowUsageQueryContext(null);
    detailEventsRequestIdRef.current += 1;
    detailEventsAutoLoadKeyRef.current = null;
    setDetailEventsLoading(false);
    setDetailEventsAppending(false);
  }, [selectedRowKey]);

  useLayoutEffect(() => {
    if (detailDrawerBodyRef.current) {
      detailDrawerBodyRef.current.scrollTop = 0;
    }
  }, [detailTab, selectedRowKey]);

  useEffect(() => {
    if (loading || error || !selectedRowKey || selectedRow) return;
    setSelectedRowKey(null);
    setDetailTab('overview');
  }, [error, loading, selectedRow, selectedRowKey]);

  const loadAccountWindowUsage = useCallback(async () => {
    const requestId = accountWindowUsageReqIdRef.current + 1;
    accountWindowUsageReqIdRef.current = requestId;
    accountWindowUsageAbortRef.current?.abort();
    accountWindowUsageAbortRef.current = null;

    if (featureAvailability.checking || !managerStorageAvailable || !selectedRow) {
      setAccountWindowUsageByKey(new Map());
      setAccountWindowUsageQueryContext(null);
      setAccountWindowUsageError('');
      return;
    }

    const controller = new AbortController();
    accountWindowUsageAbortRef.current = controller;
    const queryAsOfMs = Date.now();
    const initialDefinitions =
      effectiveQuotaWindowDefinitionsByRowKey.get(selectedRow.selectionKey) ??
      buildAccountQuotaWindowDefinitions(buildQuotaDisplayWindows(selectedRow));
    let entries = buildAccountWindowUsageTargetEntries(
      [selectedRow],
      new Map([[selectedRow.selectionKey, initialDefinitions]]),
      queryAsOfMs
    );
    const isCurrentRequest = () =>
      accountWindowUsageReqIdRef.current === requestId && !controller.signal.aborted;
    setAccountWindowUsageError('');
    try {
      const localDefinitions = quotaWindowDefinitionsByRowKey.get(selectedRow.selectionKey) ?? [];
      let writeEntries: AccountQuotaSnapshotWriteEntry[] = [];
      if (selectedRow.provider === CODEX_CONFIG.type) {
        const activeQuota = getActiveCodexQuota(selectedRow.raw);
        const apiObservation = getQuotaSnapshotObservation(selectedRow);
        if (apiObservation) {
          const apiDefinitions = buildCodexSnapshotDefinitions(selectedRow, activeQuota);
          writeEntries.push(
            ...buildAccountQuotaSnapshotWriteEntries(
              [selectedRow],
              new Map([[selectedRow.selectionKey, apiDefinitions]]),
              {
                getCodexQuota: () => activeQuota,
                getObservation: () => apiObservation,
              }
            )
          );
        }

        const headerObservation = getCodexHeaderQuotaSnapshotObservation(selectedRow);
        if (headerObservation) {
          const headerQuota = buildObservedCodexQuotaState(
            selectedRow.raw,
            getFreshCodexHeaderSnapshot(selectedRow.raw),
            t,
            headerSnapshotGeneratedAtMs
          );
          const headerDefinitions = buildCodexSnapshotDefinitions(selectedRow, headerQuota);
          writeEntries.push(
            ...buildAccountQuotaSnapshotWriteEntries(
              [selectedRow],
              new Map([[selectedRow.selectionKey, headerDefinitions]]),
              {
                getCodexQuota: () => headerQuota,
                getObservation: () => headerObservation,
              }
            )
          );
        }
      } else {
        writeEntries = buildAccountQuotaSnapshotWriteEntries(
          [selectedRow],
          new Map([[selectedRow.selectionKey, localDefinitions]]),
          { getObservation: getQuotaSnapshotObservation }
        );
      }
      if (writeEntries.length > 0) {
        try {
          await accountQuotaSnapshotApi.write(
            featureAvailability.managerServiceBase,
            managementKey,
            writeEntries,
            controller.signal
          );
        } catch {
          if (controller.signal.aborted) return;
          // Snapshot persistence is additive. A write failure must not block
          // reading previously persisted lifecycle evidence.
        }
      }
      const queryAccounts = buildAccountQuotaSnapshotQueryAccounts([selectedRow]);
      if (queryAccounts.length > 0) {
        try {
          const snapshotResponse = await accountQuotaSnapshotApi.query(
            featureAvailability.managerServiceBase,
            managementKey,
            queryAccounts,
            { includeInactive: true },
            controller.signal
          );
          if (!isCurrentRequest()) return;
          const snapshotWindows = snapshotResponse.items.find(
            (item) => item.row_key === selectedRow.selectionKey
          )?.windows;
          if (snapshotWindows) {
            setQuotaSnapshotWindowsByRowKey((current) => {
              const next = new Map(current);
              next.set(selectedRow.selectionKey, snapshotWindows);
              return next;
            });
            const mergedDefinitions = mergeAccountQuotaSnapshotWindows(
              localDefinitions,
              snapshotWindows,
              { provider: selectedRow.provider }
            );
            entries = buildAccountWindowUsageTargetEntries(
              [selectedRow],
              new Map([[selectedRow.selectionKey, mergedDefinitions]]),
              queryAsOfMs
            );
          }
        } catch {
          if (controller.signal.aborted) return;
          // Older Manager Server versions or transient query failures must not
          // block the existing usage query path.
        }
      }
      if (!featureAvailability.requestMonitoringAvailable) {
        if (!isCurrentRequest()) return;
        setAccountWindowUsageByKey(new Map());
        setAccountWindowUsageQueryContext(null);
        setAccountWindowUsageError('');
        return;
      }
      if (entries.length === 0) {
        if (!isCurrentRequest()) return;
        setAccountWindowUsageByKey(new Map());
        return;
      }
      const response = await monitoringAnalyticsApi.getAccountWindowUsage(
        featureAvailability.managerServiceBase,
        managementKey,
        {
          windows: entries.map((entry) => entry.target),
        },
        controller.signal
      );
      if (!isCurrentRequest()) return;
      setAccountWindowUsageQueryContext({
        rowKey: selectedRow.selectionKey,
        asOfMs: queryAsOfMs,
      });
      setAccountWindowUsageByKey(buildAccountWindowUsageByKey(entries, response.items ?? []));
    } catch (err: unknown) {
      if (!isCurrentRequest()) return;
      setAccountWindowUsageError(
        err instanceof Error ? err.message : t('notification.load_failed')
      );
    } finally {
      if (accountWindowUsageReqIdRef.current === requestId) {
        if (accountWindowUsageAbortRef.current === controller) {
          accountWindowUsageAbortRef.current = null;
        }
      }
    }
  }, [
    buildCodexSnapshotDefinitions,
    buildQuotaDisplayWindows,
    effectiveQuotaWindowDefinitionsByRowKey,
    featureAvailability.checking,
    featureAvailability.managerServiceBase,
    featureAvailability.requestMonitoringAvailable,
    getActiveCodexQuota,
    getCodexHeaderQuotaSnapshotObservation,
    getFreshCodexHeaderSnapshot,
    getQuotaSnapshotObservation,
    headerSnapshotGeneratedAtMs,
    managementKey,
    managerStorageAvailable,
    quotaWindowDefinitionsByRowKey,
    selectedRow,
    t,
  ]);

  const loadUsageValues = useCallback(async () => {
    if (!selectedRow) return;
    const requestId = usageValuesRequestIdRef.current + 1;
    usageValuesRequestIdRef.current = requestId;
    const commitRows = (rows: UsageValueRow[]) => {
      if (usageValuesRequestIdRef.current !== requestId) return;
      setUsageRows(rows);
    };
    const fallback = () => {
      commitRows(buildUsageValueRowsFromRecent([selectedRow]));
    };

    if (
      featureAvailability.checking ||
      !featureAvailability.requestMonitoringAvailable ||
      !featureAvailability.managerServiceBase ||
      !managementKey
    ) {
      fallback();
      return;
    }

    try {
      const toMs = Date.now();
      const authIndex = selectedRow.authIndex ? String(selectedRow.authIndex) : '';
      const response = await monitoringAnalyticsApi.getAnalytics(
        featureAvailability.managerServiceBase,
        managementKey,
        {
          from_ms: toMs - ACCOUNT_OVERVIEW_ACTIVITY_RANGE_MS,
          to_ms: toMs,
          now_ms: toMs,
          filters: {
            auth_files: [selectedRow.fileName],
            ...(authIndex ? { auth_indices: [authIndex] } : {}),
          },
          include: {
            summary: true,
            account_stats: true,
          },
        }
      );
      const stats: MonitoringAnalyticsAccountStatRow[] = response.account_stats ?? [];
      const monitoringRow = buildUsageValueRowFromMonitoringSummary(
        selectedRow,
        response.summary,
        stats
      );
      commitRows([monitoringRow]);
    } catch {
      fallback();
    }
  }, [
    featureAvailability.checking,
    featureAvailability.managerServiceBase,
    featureAvailability.requestMonitoringAvailable,
    managementKey,
    selectedRow,
  ]);

  useEffect(() => {
    if (activeView !== 'accounts' || detailTab !== 'overview' || !selectedRowKey) return;
    if (usageValuesAutoLoadKeyRef.current === usageValuesAutoLoadKey) return;
    usageValuesAutoLoadKeyRef.current = usageValuesAutoLoadKey;
    void loadUsageValues();
  }, [activeView, detailTab, loadUsageValues, selectedRowKey, usageValuesAutoLoadKey]);

  const loadAccountHistory = useCallback(
    async (targetEntries?: AccountHistoryTargetEntry[]) => {
      const entries = targetEntries ?? accountHistoryTargets;
      const mergeResult = targetEntries !== undefined;
      const controllerRef = mergeResult ? accountHistoryTargetAbortRef : accountHistoryAutoAbortRef;
      controllerRef.current?.abort();
      controllerRef.current = null;

      if (
        featureAvailability.checking ||
        !featureAvailability.requestMonitoringAvailable ||
        !featureAvailability.managerServiceBase ||
        !managementKey ||
        entries.length === 0
      ) {
        if (!mergeResult) {
          setAccountHistoryByRowKey(new Map());
        }
        setAccountHistoryLoading(accountHistoryPendingRequestsRef.current.size > 0);
        setAccountHistoryErrorsByRowKey((current) => {
          if (!mergeResult) return current.size === 0 ? current : new Map();
          const next = new Map(current);
          entries.forEach((entry) => next.delete(entry.rowKey));
          return next.size === current.size ? current : next;
        });
        return;
      }

      const controller = new AbortController();
      controllerRef.current = controller;
      const requestToken = Symbol('account-history-request');
      accountHistoryPendingRequestsRef.current.add(requestToken);
      const requestVersions = new Map<string, number>();
      entries.forEach((entry) => {
        const version = (accountHistoryRequestVersionsRef.current.get(entry.rowKey) ?? 0) + 1;
        accountHistoryRequestVersionsRef.current.set(entry.rowKey, version);
        requestVersions.set(entry.rowKey, version);
      });
      setAccountHistoryLoading(true);
      setAccountHistoryErrorsByRowKey((current) => {
        const next = new Map(current);
        entries.forEach((entry) => next.delete(entry.rowKey));
        return next.size === current.size ? current : next;
      });
      try {
        const response = await monitoringAnalyticsApi.getAccountHistory(
          featureAvailability.managerServiceBase,
          managementKey,
          {
            accounts: entries.map((entry) => entry.target),
          },
          controller.signal
        );
        if (controller.signal.aborted) return;
        const nextHistory = buildAccountHistoryByRowKey(
          entries,
          response.items,
          response.generated_at_ms
        );
        setAccountHistoryByRowKey((current) => {
          const merged = mergeResult
            ? new Map(current)
            : new Map<string, MonitoringAccountHistoryItem>();
          entries.forEach((entry) => {
            const requestVersion = requestVersions.get(entry.rowKey);
            if (accountHistoryRequestVersionsRef.current.get(entry.rowKey) !== requestVersion) {
              if (!mergeResult) {
                const currentItem = current.get(entry.rowKey);
                if (currentItem) merged.set(entry.rowKey, currentItem);
              }
              return;
            }
            merged.delete(entry.rowKey);
            const nextItem = nextHistory.get(entry.rowKey);
            if (nextItem) merged.set(entry.rowKey, nextItem);
          });
          return merged;
        });
      } catch (err: unknown) {
        if (controller.signal.aborted) return;
        const message = err instanceof Error ? err.message : t('notification.load_failed');
        setAccountHistoryErrorsByRowKey((current) => {
          const next = new Map(current);
          entries.forEach((entry) => {
            const requestVersion = requestVersions.get(entry.rowKey);
            if (accountHistoryRequestVersionsRef.current.get(entry.rowKey) === requestVersion) {
              next.set(entry.rowKey, message);
            }
          });
          return next;
        });
      } finally {
        if (controllerRef.current === controller) {
          controllerRef.current = null;
        }
        accountHistoryPendingRequestsRef.current.delete(requestToken);
        setAccountHistoryLoading(accountHistoryPendingRequestsRef.current.size > 0);
      }
    },
    [
      accountHistoryTargets,
      featureAvailability.checking,
      featureAvailability.managerServiceBase,
      featureAvailability.requestMonitoringAvailable,
      managementKey,
      t,
    ]
  );

  useEffect(() => {
    if (activeView !== 'accounts') {
      accountHistoryAutoLoadKeyRef.current = null;
      accountHistoryAutoAbortRef.current?.abort();
      accountHistoryAutoAbortRef.current = null;
      return;
    }
    if (accountHistoryAutoLoadKeyRef.current === accountHistoryAutoLoadKey) return;
    accountHistoryAutoLoadKeyRef.current = accountHistoryAutoLoadKey;
    void loadAccountHistory();
  }, [accountHistoryAutoLoadKey, activeView, loadAccountHistory]);

  const loadDetailEvents = useCallback(
    async (
      row: AccountRow,
      options: { append?: boolean; beforeMs?: number | null; beforeId?: number | null } = {}
    ) => {
      const append = options.append === true;
      const requestId = detailEventsRequestIdRef.current + 1;
      detailEventsRequestIdRef.current = requestId;
      const shouldCommit = () => detailEventsRequestIdRef.current === requestId;

      setDetailEventsRowKey(row.selectionKey);
      if (!append) {
        setDetailEvents([]);
        setDetailEventsSummary(null);
        setDetailEventsRecentFailure(null);
        setDetailEventsTotalCount(0);
        setDetailEventsHasMore(false);
        setDetailEventsNextBeforeMs(null);
        setDetailEventsNextBeforeId(null);
      }
      setDetailEventsError('');

      if (
        featureAvailability.checking ||
        !featureAvailability.requestMonitoringAvailable ||
        !featureAvailability.managerServiceBase ||
        !managementKey
      ) {
        setDetailEventsLoading(false);
        return;
      }

      if (append) setDetailEventsAppending(true);
      else setDetailEventsLoading(true);
      try {
        const toMs = Date.now();
        const authIndex = row.authIndex ? String(row.authIndex) : '';
        const response = await monitoringAnalyticsApi.getAnalytics(
          featureAvailability.managerServiceBase,
          managementKey,
          {
            from_ms: toMs - DETAIL_EVENTS_RANGE_MS,
            to_ms: toMs,
            now_ms: toMs,
            filters: {
              auth_files: [row.fileName],
              ...(authIndex ? { auth_indices: [authIndex] } : {}),
            },
            include: {
              summary: true,
              summary_profile: 'compact',
              summary_percentiles: true,
              recent_failures: 1,
              events_page: {
                limit: DETAIL_EVENTS_LIMIT,
                before_ms: options.beforeMs ?? null,
                before_id: options.beforeId ?? null,
              },
              granularity: 'day',
            },
          }
        );
        if (!shouldCommit()) return;
        const eventsPage = response.events;
        const nextItems = eventsPage?.items ?? [];
        setDetailEvents((current) => (append ? [...current, ...nextItems] : nextItems));
        setDetailEventsSummary((current) => response.summary ?? (append ? current : null));
        setDetailEventsRecentFailure(
          (current) => response.recent_failures?.[0] ?? (append ? current : null)
        );
        setDetailEventsTotalCount(
          (current) =>
            eventsPage?.total_count ?? (append ? current + nextItems.length : nextItems.length)
        );
        setDetailEventsHasMore(eventsPage?.has_more === true);
        setDetailEventsNextBeforeMs(
          typeof eventsPage?.next_before_ms === 'number' && eventsPage.next_before_ms > 0
            ? eventsPage.next_before_ms
            : null
        );
        setDetailEventsNextBeforeId(
          typeof eventsPage?.next_before_id === 'number' && eventsPage.next_before_id > 0
            ? eventsPage.next_before_id
            : null
        );
      } catch (err: unknown) {
        if (!shouldCommit()) return;
        setDetailEventsError(err instanceof Error ? err.message : t('notification.load_failed'));
      } finally {
        if (shouldCommit()) {
          if (append) setDetailEventsAppending(false);
          else setDetailEventsLoading(false);
        }
      }
    },
    [
      featureAvailability.checking,
      featureAvailability.managerServiceBase,
      featureAvailability.requestMonitoringAvailable,
      managementKey,
      t,
    ]
  );

  const refreshDetailEvents = useCallback(
    async (row: AccountRow) => {
      setDetailEventsRefreshing(true);
      try {
        await loadDetailEvents(row);
      } finally {
        setDetailEventsRefreshing(false);
      }
    },
    [loadDetailEvents]
  );

  useEffect(() => {
    if (detailTab !== 'diagnostics' || !selectedRow) return;
    if (detailEventsAutoLoadKeyRef.current === detailEventsAutoLoadKey) return;
    detailEventsAutoLoadKeyRef.current = detailEventsAutoLoadKey;
    void loadDetailEvents(selectedRow);
  }, [detailEventsAutoLoadKey, detailTab, loadDetailEvents, selectedRow]);

  const refreshQuotaForRow = useCallback(
    async (row: AccountRow) => {
      if (row.disabled || row.runtimeOnly) return false;
      const refreshWithConfig = <TState, TData>(
        config: QuotaConfig<TState, TData>,
        setQuota: QuotaSetter<TState>
      ) => {
        const storeKey = config.getStoreKey?.(row.raw) ?? row.fileName;
        return refreshQuotaWithConfig({
          config,
          file: row.raw,
          setQuota,
          t,
          isCurrent: beginAccountQuotaRequest(
            quotaRequestVersionsRef.current,
            `${config.type}:${storeKey}`
          ),
        });
      };
      switch (row.provider) {
        case CODEX_CONFIG.type:
          return refreshWithConfig(CODEX_CONFIG, setCodexQuota);
        case CLAUDE_CONFIG.type:
          return refreshWithConfig(CLAUDE_CONFIG, setClaudeQuota);
        case ANTIGRAVITY_CONFIG.type:
          return refreshWithConfig(ANTIGRAVITY_CONFIG, setAntigravityQuota);
        case KIMI_CONFIG.type:
          return refreshWithConfig(KIMI_CONFIG, setKimiQuota);
        case XAI_CONFIG.type:
          return refreshWithConfig<XaiQuotaState, NonNullable<XaiQuotaState['billing']>>(
            XAI_CONFIG,
            setXaiQuota
          );
        default:
          return false;
      }
    },
    [setAntigravityQuota, setClaudeQuota, setCodexQuota, setKimiQuota, setXaiQuota, t]
  );

  const refreshQuotaRows = useCallback(
    (targets: AccountRow[]): Promise<void> => {
      if (quotaRefreshBatchPromiseRef.current) {
        return quotaRefreshBatchPromiseRef.current;
      }
      const refreshable = targets.filter((row) => !row.disabled && !row.runtimeOnly);
      if (refreshable.length === 0) {
        showNotification(t('accounts.no_refreshable_accounts'), 'warning');
        return Promise.resolve();
      }
      const taskPlan = buildProviderCredentialTaskPlan(refreshable, {
        getProviderKey: (row) => row.provider,
        getCredentialKey: (row) => getQuotaCredentialStoreKey(row.raw),
      });
      const batchPromise = (async () => {
        setQuotaRefreshing(true);
        try {
          const results = await runProviderCredentialTaskPlan(
            taskPlan,
            {
              perProviderConcurrency: MAX_CONCURRENT_QUOTA_REFRESHES_PER_PROVIDER,
              maxConcurrentProviders: MAX_CONCURRENT_QUOTA_REFRESH_PROVIDERS,
            },
            ({ item }) => refreshQuotaForRow(item)
          );
          const successCount = results.filter(Boolean).length;
          showNotification(
            t('accounts.quota_refresh_result', {
              success: successCount,
              total: taskPlan.length,
            }),
            successCount === taskPlan.length ? 'success' : 'warning'
          );
        } finally {
          quotaRefreshBatchPromiseRef.current = null;
          setQuotaRefreshing(false);
        }
      })();
      quotaRefreshBatchPromiseRef.current = batchPromise;
      return batchPromise;
    },
    [refreshQuotaForRow, showNotification, t]
  );

  const refreshAccountQuota = useCallback(
    async (row: AccountRow): Promise<void> => {
      await refreshQuotaRows([row]);
      if (selectedRowKeyRef.current === row.selectionKey) {
        setAccountQuotaRefreshRevision((current) => current + 1);
      }
    },
    [refreshQuotaRows]
  );

  const refreshAccountHistory = useCallback(
    (row: AccountRow): Promise<void> => {
      if (!requestHistoryAvailable) return Promise.resolve();
      const refreshKey = JSON.stringify({
        checking: featureAvailability.checking,
        connectionFingerprint,
        managerServiceBase: featureAvailability.managerServiceBase,
        managementKey,
        requestMonitoringAvailable: featureAvailability.requestMonitoringAvailable,
        rowKey: row.selectionKey,
      });
      const currentRefresh = accountHistoryRefreshPromiseRef.current;
      if (currentRefresh?.key === refreshKey) {
        return currentRefresh.promise;
      }
      const requestId = accountHistoryRefreshRequestIdRef.current + 1;
      accountHistoryRefreshRequestIdRef.current = requestId;
      const refreshPromise = (async () => {
        const isCurrentContext = () => {
          const context = headerSnapshotContextRef.current;
          return (
            accountHistoryRefreshRequestIdRef.current === requestId &&
            context.managerServiceBase === featureAvailability.managerServiceBase &&
            context.managementKey === managementKey &&
            context.connectionFingerprint === connectionFingerprint &&
            context.checking === featureAvailability.checking &&
            context.requestMonitoringAvailable === featureAvailability.requestMonitoringAvailable &&
            selectedRowKeyRef.current === row.selectionKey
          );
        };
        setHistoryRefreshing(true);
        try {
          await loadAccountHistory(buildAccountHistoryTargetEntries([row]));
          if (!isCurrentContext()) return;
          if (row.provider === CODEX_CONFIG.type) {
            await loadHeaderSnapshots();
            if (!isCurrentContext()) return;
          }
          setAccountHistoryRefreshRevision((current) => current + 1);
        } finally {
          if (accountHistoryRefreshRequestIdRef.current === requestId) {
            accountHistoryRefreshPromiseRef.current = null;
            setHistoryRefreshing(false);
          }
        }
      })();
      accountHistoryRefreshPromiseRef.current = { key: refreshKey, promise: refreshPromise };
      return refreshPromise;
    },
    [
      connectionFingerprint,
      featureAvailability.checking,
      featureAvailability.managerServiceBase,
      featureAvailability.requestMonitoringAvailable,
      loadAccountHistory,
      loadHeaderSnapshots,
      managementKey,
      requestHistoryAvailable,
    ]
  );

  useEffect(() => {
    if (activeView !== 'accounts' || detailTab !== 'quota' || !selectedRowKey || !selectedRow) {
      const hadInFlightRequest = accountWindowUsageAbortRef.current !== null;
      accountWindowUsageReqIdRef.current += 1;
      accountWindowUsageAbortRef.current?.abort();
      accountWindowUsageAbortRef.current = null;
      if (hadInFlightRequest || !selectedRow) {
        accountWindowUsageAutoLoadKeyRef.current = null;
      }
      return;
    }
    if (
      canLoadHeaderSnapshots &&
      selectedRow?.provider === CODEX_CONFIG.type &&
      headerSnapshotLoadedKey !== headerSnapshotLoadKey
    ) {
      return;
    }
    if (accountWindowUsageAutoLoadKeyRef.current === accountWindowUsageAutoLoadKey) return;
    accountWindowUsageAutoLoadKeyRef.current = accountWindowUsageAutoLoadKey;
    void loadAccountWindowUsage();
  }, [
    accountWindowUsageAutoLoadKey,
    activeView,
    canLoadHeaderSnapshots,
    detailTab,
    headerSnapshotLoadKey,
    headerSnapshotLoadedKey,
    loadAccountWindowUsage,
    selectedRow,
    selectedRowKey,
  ]);

  const canResetCodexQuota = useCallback(
    (row: AccountRow) => {
      if (row.provider !== CODEX_CONFIG.type || row.disabled || row.runtimeOnly) return false;
      return CODEX_CONFIG.canResetQuota?.(row.raw, getDisplayCodexQuota(row.raw)) === true;
    },
    [getDisplayCodexQuota]
  );

  const resetCodexQuotaForRow = useCallback(
    (row: AccountRow) => {
      if (!canResetCodexQuota(row) || !CODEX_CONFIG.resetQuota) return;
      const quota = getDisplayCodexQuota(row.raw);
      const storeKey = CODEX_CONFIG.getStoreKey?.(row.raw) ?? row.fileName;
      const resetCount = quota?.rateLimitResetCreditsAvailableCount ?? 0;
      const displayName = getDisplayAccount(row);

      showConfirmation({
        title: t('codex_quota.reset_confirm_title'),
        message: t('codex_quota.reset_confirm_message', {
          name: displayName,
          count: resetCount,
        }),
        confirmText: t('codex_quota.reset_button', { count: resetCount }),
        cancelText: t('common.cancel'),
        variant: 'primary',
        onConfirm: async () => {
          const cacheGeneration = captureQuotaCacheGeneration();
          const isCurrent = beginAccountQuotaRequest(
            quotaRequestVersionsRef.current,
            `${CODEX_CONFIG.type}:${storeKey}`
          );
          try {
            const data = await CODEX_CONFIG.resetQuota?.(row.raw, t);
            if (data === undefined) {
              throw new Error(t('common.unknown_error'));
            }
            if (!isCurrent()) return;
            const committed = commitIfQuotaCacheCurrent(cacheGeneration, () => {
              setCodexQuota((prev) => ({
                ...prev,
                [storeKey]: CODEX_CONFIG.buildSuccessState(data, row.raw),
              }));
            });
            if (!committed) return;
            showNotification(t('codex_quota.reset_success', { name: displayName }), 'success');
          } catch (err: unknown) {
            if (!isCurrent()) return;
            const message = err instanceof Error ? err.message : t('common.unknown_error');
            const status =
              typeof err === 'object' && err !== null && 'status' in err
                ? Number((err as { status?: unknown }).status)
                : undefined;
            const committed = commitIfQuotaCacheCurrent(cacheGeneration, () => {
              setCodexQuota((prev) => {
                const previousState = getScopedQuotaState(CODEX_CONFIG, prev, row.raw);
                return {
                  ...prev,
                  [storeKey]: buildQuotaFailureState(
                    CODEX_CONFIG,
                    message,
                    Number.isFinite(status) ? status : undefined,
                    row.raw,
                    previousState
                  ),
                };
              });
            });
            if (!committed) return;
            showNotification(
              t('codex_quota.reset_failed', { name: displayName, message }),
              'error'
            );
          }
        },
      });
    },
    [
      canResetCodexQuota,
      getDisplayAccount,
      getDisplayCodexQuota,
      setCodexQuota,
      showConfirmation,
      showNotification,
      t,
    ]
  );

  const handleBatchStatus = useCallback(
    async (enabled: boolean, targets = selectedRows) => {
      const patchTargets = targets
        .filter((row) => !row.runtimeOnly)
        .map((row) => getAuthFilePatchTarget(row.raw));
      if (patchTargets.length === 0) return;
      setStatusUpdating(true);
      try {
        await batchSetStatus(patchTargets, enabled);
        await loadFiles();
        deselectAll();
      } finally {
        setStatusUpdating(false);
      }
    },
    [batchSetStatus, deselectAll, loadFiles, selectedRows]
  );

  const patchPriorityRows = useCallback(
    async (targets: AccountRow[], priority: number) => {
      const patchTargets = targets
        .filter((row) => !row.runtimeOnly)
        .map((row) => getAuthFilePatchTarget(row.raw));
      await batchPatchFields(patchTargets, { priority });
    },
    [batchPatchFields]
  );

  const handleBatchPrioritySave = useCallback(async () => {
    const priority = parsePriorityValue(batchPriorityValue);
    if (priority === null) {
      showNotification(t('accounts.priority_invalid'), 'error');
      return;
    }
    await patchPriorityRows(selectedRows, priority);
    setBatchPriorityOpen(false);
    setBatchPriorityValue('');
    setIsSelectionMode(false);
  }, [batchPriorityValue, patchPriorityRows, selectedRows, showNotification, t]);

  const patchWebsocketsRows = useCallback(
    async (targets: AccountRow[], websockets: boolean) => {
      const patchTargets = targets
        .filter((row) => !row.runtimeOnly && row.provider === CODEX_CONFIG.type)
        .map((row) => getAuthFilePatchTarget(row.raw));
      if (patchTargets.length === 0) {
        showNotification(t('accounts.no_codex_accounts_selected'), 'info');
        return;
      }
      await batchPatchFields(patchTargets, { websockets });
    },
    [batchPatchFields, showNotification, t]
  );

  const copyTextWithNotification = useCallback(
    async (text: string) => {
      const copied = await copyToClipboard(text);
      showNotification(
        copied
          ? t('notification.link_copied', { defaultValue: 'Copied to clipboard' })
          : t('notification.copy_failed', { defaultValue: 'Copy failed' }),
        copied ? 'success' : 'error'
      );
    },
    [showNotification, t]
  );

  const handleCopyIdentityText = useCallback(
    async (event: ReactMouseEvent<HTMLButtonElement>, text: string, feedbackKey: string) => {
      event.stopPropagation();
      const value = text.trim();
      if (!value) return;

      const copied = await copyToClipboard(value);
      if (!copied) {
        showNotification(t('notification.copy_failed', { defaultValue: 'Copy failed' }), 'error');
        return;
      }

      setCopiedIdentityKey(feedbackKey);
      if (identityCopyTimerRef.current !== null) {
        clearTimeout(identityCopyTimerRef.current);
      }
      identityCopyTimerRef.current = setTimeout(() => {
        setCopiedIdentityKey((current) => (current === feedbackKey ? null : current));
        identityCopyTimerRef.current = null;
      }, 1800);
    },
    [showNotification, t]
  );

  const openOauthExcludedEditor = useCallback(
    (provider?: string) => {
      const providerValue = (provider || (providerFilter !== 'all' ? providerFilter : '')).trim();
      setOauthExcludedEditorProvider(providerValue);
    },
    [providerFilter]
  );

  const openOauthModelAliasEditor = useCallback(
    (provider?: string) => {
      const providerValue = (provider || (providerFilter !== 'all' ? providerFilter : '')).trim();
      setOauthModelAliasEditorProvider(providerValue);
    },
    [providerFilter]
  );

  const reloadOauthRules = useCallback(async () => {
    await Promise.all([loadOauthExcluded(), loadOauthModelAlias()]);
  }, [loadOauthExcluded, loadOauthModelAlias]);

  const selectedAccountSortIndex = ACCOUNT_SORT_FIELD_OPTIONS.findIndex(
    (option) => option.value === accountSort.key
  );
  const selectedAccountSortOption = getAccountSortFieldOption(accountSort.key);
  const selectedAccountSortLabel = t(selectedAccountSortOption.labelKey);
  const selectedStatusFilterLabel =
    statusFilter === 'all'
      ? t('accounts.status_all')
      : isAccountCodexStatusFilter(statusFilter)
        ? t(`auth_files.codex_status_filter_${statusFilter}`)
        : t(`accounts.status_${statusFilter}`);
  const selectedPlanFilterLabel =
    planFilter === 'all'
      ? t('accounts.plan_all')
      : planFilter === 'unknown'
        ? t('auth_files.codex_plan_filter_unknown')
        : planFilter;
  const selectedQuotaFilterLabel =
    quotaBandFilter === 'all' ? t('accounts.quota_all') : t(`accounts.quota_${quotaBandFilter}`);
  const selectedOperationalFilterLabel = t(`accounts.operational_${effectiveOperationalFilter}`);
  const activeMobileFilterCount = [
    statusFilter !== 'all',
    effectiveOperationalFilter !== 'all',
    planFilter !== 'all',
    quotaBandFilter !== 'all',
    accountSort.key !== 'default',
  ].filter(Boolean).length;
  const mobileFilterSummary =
    activeMobileFilterCount === 0
      ? t('accounts.mobile_filters_default')
      : [
          statusFilter !== 'all' ? selectedStatusFilterLabel : null,
          effectiveOperationalFilter !== 'all' ? selectedOperationalFilterLabel : null,
          planFilter !== 'all' ? selectedPlanFilterLabel : null,
          quotaBandFilter !== 'all' ? selectedQuotaFilterLabel : null,
          accountSort.key !== 'default' ? selectedAccountSortLabel : null,
        ]
          .filter(Boolean)
          .join(' · ');

  const openAccountSortDropdown = () => {
    setHighlightedAccountSortIndex(selectedAccountSortIndex >= 0 ? selectedAccountSortIndex : 0);
    setIsAccountSortDropdownOpen(true);
  };

  const toggleAccountSortDropdown = () => {
    if (isAccountSortDropdownOpen) {
      setIsAccountSortDropdownOpen(false);
      return;
    }
    openAccountSortDropdown();
  };

  const closeAccountSortDropdown = () => {
    setIsAccountSortDropdownOpen(false);
    accountSortTriggerRef.current?.focus();
  };

  const closeMobileFilters = () => {
    setIsMobileFiltersOpen(false);
    setIsAccountSortDropdownOpen(false);
  };

  const resetAccountFilters = () => {
    setStatusFilter('all');
    setOperationalFilter('all');
    setPlanFilter('all');
    setQuotaBandFilter('all');
    setAccountSort({ key: 'default', direction: 'desc' });
    setPage(1);
    setIsAccountSortDropdownOpen(false);
  };

  const moveAccountSortHighlight = (nextIndex: number) => {
    const normalizedIndex =
      (nextIndex + ACCOUNT_SORT_FIELD_OPTIONS.length) % ACCOUNT_SORT_FIELD_OPTIONS.length;
    setHighlightedAccountSortIndex(normalizedIndex);
  };

  const commitAccountSortField = (value: AccountSortFieldValue) => {
    setAccountSort((prev) => {
      if (value === 'default') {
        return { key: 'default', direction: 'desc' };
      }
      return {
        key: value,
        direction: prev.key === value ? prev.direction : ACCOUNT_SORT_DEFAULT_DIRECTIONS[value],
      };
    });
    setPage(1);
    setIsAccountSortDropdownOpen(false);
    accountSortTriggerRef.current?.focus();
  };

  const handleAccountSortDirectionToggle = () => {
    setAccountSort((prev) => {
      if (prev.key === 'default') return prev;
      return {
        key: prev.key,
        direction: prev.direction === 'asc' ? 'desc' : 'asc',
      };
    });
    setPage(1);
  };

  const handleAccountSortTriggerKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    switch (event.key) {
      case 'Enter':
      case ' ':
        event.preventDefault();
        toggleAccountSortDropdown();
        return;
      case 'ArrowDown':
        event.preventDefault();
        if (!isAccountSortDropdownOpen) {
          openAccountSortDropdown();
          return;
        }
        moveAccountSortHighlight(highlightedAccountSortIndex + 1);
        return;
      case 'ArrowUp':
        event.preventDefault();
        if (!isAccountSortDropdownOpen) {
          openAccountSortDropdown();
          return;
        }
        moveAccountSortHighlight(highlightedAccountSortIndex - 1);
        return;
      case 'Escape':
        if (!isAccountSortDropdownOpen) return;
        event.preventDefault();
        closeAccountSortDropdown();
        return;
      default:
        return;
    }
  };

  const handleAccountSortOptionKeyDown = (
    event: KeyboardEvent<HTMLButtonElement>,
    optionIndex: number
  ) => {
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        moveAccountSortHighlight(optionIndex + 1);
        return;
      case 'ArrowUp':
        event.preventDefault();
        moveAccountSortHighlight(optionIndex - 1);
        return;
      case 'Home':
        event.preventDefault();
        moveAccountSortHighlight(0);
        return;
      case 'End':
        event.preventDefault();
        moveAccountSortHighlight(ACCOUNT_SORT_FIELD_OPTIONS.length - 1);
        return;
      case 'Escape':
        event.preventDefault();
        closeAccountSortDropdown();
        return;
      default:
        return;
    }
  };

  const renderViewTabs = () => {
    const tabs: Array<SegmentedTabItem<AccountsView> & { badge?: number }> = [
      { id: 'accounts', label: t('accounts.tab_accounts') },
      {
        id: 'health',
        label: t('accounts.tab_health'),
        badge: metrics.needsInspectionAction,
      },
      { id: 'oauth', label: t('accounts.tab_oauth') },
    ];
    return (
      <SegmentedTabs
        items={tabs.map((tab) => ({
          id: tab.id,
          label: (
            <span className={styles.tabLabel}>
              <span>{tab.label}</span>
              {tab.badge ? <small className={styles.tabBadge}>{tab.badge}</small> : null}
            </span>
          ),
        }))}
        activeTab={activeView}
        onChange={changeActiveView}
        ariaLabel={t('accounts.tabs_label')}
        idBase="accounts-tab"
        className={styles.tabs}
      />
    );
  };

  const renderAccountSortControls = () => {
    const selectedField: AccountSortFieldValue = accountSort.key;
    const directionLabel =
      accountSort.direction === 'asc'
        ? t('accounts.sort_ascending')
        : t('accounts.sort_descending');

    return (
      <div className={styles.accountSortControls} ref={accountSortDropdownRef}>
        <button
          ref={accountSortTriggerRef}
          type="button"
          className={styles.accountSortTrigger}
          onClick={toggleAccountSortDropdown}
          onKeyDown={handleAccountSortTriggerKeyDown}
          title={`${t('accounts.sort_label')}: ${selectedAccountSortLabel}`}
          aria-label={`${t('accounts.sort_label')}: ${selectedAccountSortLabel}`}
          aria-haspopup="listbox"
          aria-expanded={isAccountSortDropdownOpen}
        >
          <span className={styles.accountSortLabel}>{selectedAccountSortLabel}</span>
        </button>
        <button
          type="button"
          className={styles.accountSortDirectionButton}
          onClick={handleAccountSortDirectionToggle}
          disabled={accountSort.key === 'default'}
          title={directionLabel}
          aria-label={directionLabel}
        >
          <span className={styles.accountSortDirectionIcon} aria-hidden="true">
            {accountSort.direction === 'asc' ? (
              <IconArrowUpNarrowWide size={14} />
            ) : (
              <IconArrowDownWideNarrow size={14} />
            )}
          </span>
        </button>
        {isAccountSortDropdownOpen ? (
          <div className={styles.accountSortDropdownList} role="listbox">
            {ACCOUNT_SORT_FIELD_OPTIONS.map((option, optionIndex) => {
              const isSelected = option.value === selectedField;
              const isHighlighted = optionIndex === highlightedAccountSortIndex;
              const optionClassName = [
                styles.accountSortDropdownItem,
                isSelected ? styles.accountSortDropdownItemSelected : '',
                isHighlighted ? styles.accountSortDropdownItemHighlighted : '',
              ]
                .filter(Boolean)
                .join(' ');
              return (
                <button
                  key={option.value}
                  ref={(node) => {
                    accountSortOptionRefs.current.set(option.value, node);
                  }}
                  type="button"
                  role="option"
                  aria-selected={isSelected}
                  className={optionClassName}
                  onClick={() => commitAccountSortField(option.value)}
                  onKeyDown={(event) => handleAccountSortOptionKeyDown(event, optionIndex)}
                  onMouseEnter={() => setHighlightedAccountSortIndex(optionIndex)}
                >
                  {t(option.labelKey)}
                </button>
              );
            })}
          </div>
        ) : null}
      </div>
    );
  };

  const renderAccountFilterFields = () => (
    <>
      <div className={styles.filterField}>
        <Select
          value={statusFilter}
          options={[
            { value: 'all', label: t('accounts.status_all') },
            { value: 'available', label: t('accounts.status_available') },
            { value: 'low', label: t('accounts.status_low') },
            { value: 'exhausted', label: t('accounts.status_exhausted') },
            { value: 'disabled', label: t('accounts.status_disabled') },
            { value: 'problem', label: t('accounts.status_problem') },
            { value: 'inspection', label: t('accounts.status_inspection') },
            ...ACCOUNT_CODEX_STATUS_FILTERS.map((value) => ({
              value,
              label: t(`auth_files.codex_status_filter_${value}`),
            })),
          ]}
          onChange={(value) => setStatusFilter(value as AccountStatusFilter)}
          ariaLabel={t('accounts.status_filter')}
          triggerClassName={styles.toolbarSelectTrigger}
        />
      </div>
      <div className={styles.filterField}>
        <Select
          value={effectiveOperationalFilter}
          options={[
            { value: 'all', label: t('accounts.operational_all') },
            { value: 'reauth', label: t('accounts.operational_reauth') },
            ...(managerStorageAvailable
              ? [
                  { value: 'cooldown', label: t('accounts.operational_cooldown') },
                  { value: 'automation', label: t('accounts.operational_automation') },
                ]
              : []),
            { value: 'recovered', label: t('accounts.operational_recovered') },
          ]}
          onChange={(value) => setOperationalFilter(value as AccountOperationalFilter)}
          ariaLabel={t('accounts.operational_filter')}
          triggerClassName={styles.toolbarSelectTrigger}
        />
      </div>
      <div className={styles.filterField}>
        <Select
          value={planFilter}
          options={[
            { value: 'all', label: t('accounts.plan_all') },
            ...planOptions.map((plan) => ({
              value: plan,
              label: plan === 'unknown' ? t('auth_files.codex_plan_filter_unknown') : plan,
            })),
          ]}
          onChange={setPlanFilter}
          ariaLabel={t('accounts.plan_filter')}
          triggerClassName={styles.toolbarSelectTrigger}
        />
      </div>
      <div className={styles.filterField}>
        <Select
          value={quotaBandFilter}
          options={[
            { value: 'all', label: t('accounts.quota_all') },
            { value: 'ge50', label: t('accounts.quota_ge50') },
            { value: 'between20and50', label: t('accounts.quota_between20and50') },
            { value: 'lt20', label: t('accounts.quota_lt20') },
            { value: 'spent', label: t('accounts.quota_spent') },
          ]}
          onChange={(value) => setQuotaBandFilter(value as AccountQuotaBand)}
          ariaLabel={t('accounts.quota_filter')}
          triggerClassName={styles.toolbarSelectTrigger}
        />
      </div>
    </>
  );

  const renderSelectionControls = () => (
    <div className={styles.selectionControls}>
      <Button
        variant="secondary"
        size="sm"
        onClick={() => selectAllVisible(pageAuthFiles)}
        disabled={selectablePageRows.length === 0}
      >
        {t('auth_files.batch_select_page')}
      </Button>
      <Button
        variant="secondary"
        size="sm"
        onClick={() => selectAllVisible(filteredAuthFiles)}
        disabled={selectableFilteredRows.length === 0}
      >
        {t('auth_files.batch_select_filtered')}
      </Button>
      <Button
        variant="secondary"
        size="sm"
        onClick={() => invertVisibleSelection(pageAuthFiles)}
        disabled={selectablePageRows.length === 0}
      >
        {t('auth_files.batch_invert_page')}
      </Button>
      <Button
        variant="secondary"
        size="sm"
        onClick={cancelSelectionMode}
        disabled={!isSelectionMode && selectionCount === 0}
      >
        <IconX size={15} />
        {t('auth_files.batch_deselect')}
      </Button>
    </div>
  );

  const renderToolbar = () => (
    <>
      <AccountProviderTabs
        rows={rows}
        value={providerFilter}
        onChange={setProviderFilter}
        resolvedTheme={resolvedTheme}
      />
      <section className={styles.toolbar}>
        <div className={styles.searchField}>
          <Input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t('accounts.search_placeholder')}
            rightElement={<IconSearch size={16} />}
            aria-label={t('accounts.search_label')}
            className={styles.toolbarSearchInput}
          />
        </div>
        <div className={styles.mobileToolbarActions}>
          <Button
            variant="secondary"
            size="sm"
            className={styles.mobileFilterButton}
            onClick={() => {
              setIsMobileFiltersOpen(true);
              setIsAccountSortDropdownOpen(false);
            }}
            aria-label={t('accounts.mobile_filters_button')}
          >
            <IconSlidersHorizontal size={15} />
            {t('accounts.mobile_filters_button')}
            {activeMobileFilterCount > 0 ? (
              <span className={styles.mobileFilterCount}>{activeMobileFilterCount}</span>
            ) : null}
          </Button>
          <span className={styles.mobileFilterSummary} title={mobileFilterSummary}>
            {mobileFilterSummary}
          </span>
        </div>
        {renderAccountFilterFields()}
        {renderAccountSortControls()}
      </section>
    </>
  );

  const renderAccountDisplayToggle = () => (
    <Button
      variant="secondary"
      size="sm"
      className={[
        styles.accountDisplayButton,
        accountDisplayMode === 'full' ? styles.accountDisplayButtonActive : '',
      ]
        .filter(Boolean)
        .join(' ')}
      onClick={() => setAccountDisplayMode((mode) => (mode === 'masked' ? 'full' : 'masked'))}
      title={accountDisplayHint}
      aria-label={accountDisplayHint}
    >
      <AccountDisplayIcon size={15} />
      {t(
        accountDisplayMode === 'masked'
          ? 'quota_management.account_display_masked'
          : 'quota_management.account_display_full'
      )}
    </Button>
  );

  const renderMobileFilterPanel = () => {
    if (!isMobileFiltersOpen || typeof document === 'undefined') return null;

    return createPortal(
      <div className={styles.mobileFilterLayer}>
        <button
          type="button"
          className={styles.mobileFilterBackdrop}
          aria-label={t('common.close')}
          onClick={closeMobileFilters}
        />
        <section
          className={styles.mobileFilterPanel}
          role="dialog"
          aria-modal="true"
          aria-labelledby="accounts-mobile-filter-title"
        >
          <header className={styles.mobileFilterHeader}>
            <div>
              <h2 id="accounts-mobile-filter-title">{t('accounts.mobile_filters_title')}</h2>
              <span>{mobileFilterSummary}</span>
            </div>
            <Button
              variant="ghost"
              size="sm"
              iconOnly
              onClick={closeMobileFilters}
              aria-label={t('common.close')}
            >
              <IconX size={17} />
            </Button>
          </header>
          <div className={styles.mobileFilterBody}>
            <div className={styles.mobileFilterDisplayMode}>{renderAccountDisplayToggle()}</div>
            {renderAccountFilterFields()}
            <div className={styles.mobileSortField}>{renderAccountSortControls()}</div>
          </div>
          <footer className={styles.mobileFilterFooter}>
            <Button variant="secondary" size="sm" onClick={resetAccountFilters}>
              {t('common.reset')}
            </Button>
            <Button variant="primary" size="sm" onClick={closeMobileFilters}>
              {t('common.confirm')}
            </Button>
          </footer>
        </section>
      </div>,
      document.body
    );
  };

  const renderBatchBar = () => {
    const hasSelection = selectionCount > 0;
    const refreshTargets = hasSelection ? selectedRows : pageRows;
    const showSelectionControls = isSelectionMode || hasSelection;

    return (
      <section
        className={[
          styles.batchBar,
          hasSelection || isSelectionMode ? styles.batchBarActive : styles.batchBarIdle,
        ]
          .filter(Boolean)
          .join(' ')}
      >
        <div className={styles.batchSummary}>
          <span>
            {t('accounts.selected_count', {
              count: selectionCount,
            })}
          </span>
          <small>
            {isSelectionMode || hasSelection
              ? t('accounts.selection_mode_hint')
              : t('accounts.batch_hint')}
          </small>
        </div>
        <div className={styles.batchActions}>
          {!isSelectionMode && !hasSelection ? (
            <Button
              variant="secondary"
              size="sm"
              className={styles.selectionModeButton}
              onClick={() => setIsSelectionMode(true)}
              aria-pressed={isSelectionMode}
            >
              <IconCheck size={15} />
              {t('accounts.selection_mode_enter')}
            </Button>
          ) : null}
          {showSelectionControls ? renderSelectionControls() : null}
          <div className={styles.batchDisplayToggle}>{renderAccountDisplayToggle()}</div>
          {!hasSelection && !isSelectionMode ? (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => refreshQuotaRows(refreshTargets)}
              disabled={quotaRefreshing || refreshTargets.length === 0}
              loading={quotaRefreshing}
              title={t('accounts.refresh_quota')}
            >
              {!quotaRefreshing ? <IconRefreshCw size={15} /> : null}
              {t('accounts.refresh_quota')}
            </Button>
          ) : null}
        </div>
      </section>
    );
  };

  const renderFloatingBatchActions = () => {
    if (selectionCount === 0) return null;
    const selectedProviderSummary = Array.from(
      selectedRows.reduce((summary, row) => {
        summary.set(row.provider, (summary.get(row.provider) ?? 0) + 1);
        return summary;
      }, new Map<string, number>())
    )
      .sort((left, right) => right[1] - left[1])
      .map(([provider, count]) => `${getProviderLabel(provider, t)} × ${count}`)
      .join(', ');
    const deletePreviewNames = selectedFileNames.slice(0, 6);
    const moreItems: DropdownMenuItem[] = [
      {
        key: 'download',
        label: t('auth_files.batch_download'),
        icon: <IconDownload size={15} />,
        onClick: () => void batchDownload(selectedFileNames),
        disabled: disableControls || selectedFileNames.length === 0,
      },
      {
        key: 'websockets-enable',
        label: t('auth_files.batch_websockets_enable'),
        icon: <IconSettings size={15} />,
        onClick: () => void patchWebsocketsRows(selectedRows, true),
        disabled: disableControls || selectedCodexRows.length === 0 || batchFieldsUpdating,
      },
      {
        key: 'websockets-disable',
        label: t('auth_files.batch_websockets_disable'),
        icon: <IconSettings size={15} />,
        onClick: () => void patchWebsocketsRows(selectedRows, false),
        disabled: disableControls || selectedCodexRows.length === 0 || batchFieldsUpdating,
      },
      { key: 'batch-more-divider', type: 'divider' },
      {
        key: 'restore-default',
        label: t('accounts.restore_default_priority'),
        icon: <IconRefreshCw size={15} />,
        onClick: () => {
          void patchPriorityRows(selectedRows, 0).then(() => setIsSelectionMode(false));
        },
        disabled: disableControls || batchFieldsUpdating,
      },
      { key: 'danger-divider', type: 'divider' },
      {
        key: 'delete',
        label: t('common.delete'),
        icon: <IconTrash2 size={15} />,
        onClick: () => {
          if (selectedHasPartialSharedAuthFile) return;
          batchDelete(selectedTargetFiles, {
            title: t('auth_files.batch_delete_title'),
            confirmText: t('common.delete'),
            message: (
              <AccountsBatchDeletePreview
                summary={t('accounts.batch_delete_preview_summary', {
                  rows: selectionCount,
                  files: selectedFileNames.length,
                })}
                warning={t('accounts.batch_delete_preview_file_scope')}
                providers={
                  selectedProviderSummary
                    ? t('accounts.batch_delete_preview_providers', {
                        providers: selectedProviderSummary,
                      })
                    : undefined
                }
                fileNames={deletePreviewNames.map(getDisplayFileName)}
                moreLabel={
                  selectedFileNames.length > deletePreviewNames.length
                    ? t('accounts.batch_delete_preview_more', {
                        count: selectedFileNames.length - deletePreviewNames.length,
                      })
                    : undefined
                }
              />
            ),
          });
        },
        disabled:
          disableControls || selectedFileNames.length === 0 || selectedHasPartialSharedAuthFile,
        tone: 'danger',
      },
    ];

    const content = (
      <div className={styles.floatingBatchActionContainer}>
        <div className={styles.floatingBatchActionBar}>
          <div className={styles.floatingBatchActionLeft}>
            <span className={styles.batchSelectionText}>
              {t('accounts.selected_count', {
                count: selectionCount,
              })}
            </span>
          </div>
          <div className={styles.floatingBatchActionRight}>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => refreshQuotaRows(selectedRows)}
              disabled={quotaRefreshing || selectedRows.length === 0}
              loading={quotaRefreshing}
              title={t('accounts.refresh_quota')}
            >
              {!quotaRefreshing ? <IconRefreshCw size={15} /> : null}
              {t('accounts.refresh_quota')}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => handleBatchStatus(true)}
              disabled={disableControls || statusUpdating}
              title={t('accounts.enable')}
            >
              <IconCheck size={15} />
              {t('accounts.enable')}
            </Button>
            <Button
              variant="danger"
              size="sm"
              onClick={() => handleBatchStatus(false)}
              disabled={disableControls || statusUpdating}
              title={t('accounts.disable')}
            >
              <IconX size={15} />
              {t('accounts.disable')}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              disabled={disableControls || selectedRows.length === 0 || batchFieldsUpdating}
              onClick={() => {
                setBatchPriorityValue('');
                setBatchPriorityOpen(true);
              }}
              title={t('accounts.set_priority')}
            >
              {t('accounts.set_priority')}
            </Button>
            <DropdownMenu
              items={moreItems}
              ariaLabel={t('accounts.batch_more')}
              triggerLabel={t('accounts.batch_more')}
              triggerIcon={<IconMoreVertical size={16} />}
              triggerClassName={styles.floatingBatchMore}
            />
          </div>
        </div>
      </div>
    );

    return typeof document === 'undefined' ? content : createPortal(content, document.body);
  };

  const renderRowActions = (row: AccountRow, needsReauth = false) => (
    <div className={styles.rowActions} onClick={(event) => event.stopPropagation()}>
      <div className={styles.accountQuickActionsGrid}>
        {needsReauth ? (
          <Button
            variant="secondary"
            size="sm"
            iconOnly
            className={`${styles.accountIconButton} ${styles.accountIconButtonRefresh}`}
            onClick={() => handleReauthAccount(row.raw)}
            disabled={disableControls || row.runtimeOnly}
            title={t('accounts.recommend_action_reauth')}
            aria-label={t('accounts.recommend_action_reauth')}
          >
            <IconShield size={15} />
          </Button>
        ) : null}
        <Button
          variant="secondary"
          size="sm"
          iconOnly
          className={`${styles.accountIconButton} ${styles.accountIconButtonRefresh}`}
          onClick={() => void refreshAccountQuota(row)}
          disabled={quotaRefreshing || row.disabled || row.runtimeOnly}
          title={t('accounts.refresh_quota')}
          aria-label={t('accounts.refresh_quota')}
        >
          <IconRefreshCw size={15} />
        </Button>
        <Button
          variant="secondary"
          size="sm"
          iconOnly
          className={`${styles.accountIconButton} ${styles.accountIconButtonModels}`}
          onClick={() => void openAccountDetail(row, 'models')}
          disabled={row.runtimeOnly && row.provider !== 'aistudio'}
          title={t('auth_files.models_button')}
          aria-label={t('auth_files.models_button')}
        >
          <IconModelCluster size={15} />
        </Button>
        <Button
          variant="secondary"
          size="sm"
          iconOnly
          className={`${styles.accountIconButton} ${styles.accountIconButtonDownload}`}
          onClick={() => void handleDownload(row.fileName)}
          disabled={row.runtimeOnly}
          title={t('auth_files.download_button')}
          aria-label={t('auth_files.download_button')}
        >
          <IconDownload size={15} />
        </Button>
        <Button
          variant="danger"
          size="sm"
          iconOnly
          className={`${styles.accountIconButton} ${styles.accountIconButtonDelete}`}
          onClick={() => void handleAccountDelete(row.raw)}
          disabled={disableControls || row.runtimeOnly || deleting === row.fileName}
          title={t('auth_files.delete_button')}
          aria-label={t('auth_files.delete_button')}
        >
          {deleting === row.fileName ? <LoadingSpinner size={14} /> : <IconTrash2 size={15} />}
        </Button>
      </div>
      <span className={styles.accountActionsDivider} aria-hidden="true" />
      <div className={styles.accountSideActions}>
        <div className={styles.accountStatusSwitch}>
          <ToggleSwitch
            checked={!row.disabled}
            onChange={(enabled) => void handleBatchStatus(enabled, [row])}
            disabled={disableControls || statusUpdating || row.runtimeOnly}
            ariaLabel={t('auth_files.status_toggle_label')}
          />
        </div>
        <Button
          variant="ghost"
          size="xs"
          className={styles.rowDetailButton}
          onClick={() => void openAccountDetail(row)}
          title={t('accounts.open_detail', { name: row.fileName })}
          aria-label={t('accounts.open_detail', { name: row.fileName })}
        >
          {t('accounts.open_detail_short')}
        </Button>
      </div>
    </div>
  );

  const renderPagination = () => (
    <div className={styles.accountsPagination}>
      <PaginationControls
        count={filteredRows.length}
        currentPage={currentPage}
        totalPages={totalPages}
        startItem={paginationStartItem}
        endItem={paginationEndItem}
        pageSize={pageSize}
        pageSizeOptions={PAGE_SIZE_OPTIONS.map((option) => Number(option.value))}
        onPageChange={setPage}
        onPageSizeChange={(nextPageSize) => {
          setPageSize(nextPageSize);
          setPage(1);
        }}
        t={t}
      />
    </div>
  );

  const renderAccountEmptyState = () => (
    <EmptyState
      title={t('accounts.empty_title')}
      description={t('accounts.empty_desc')}
      action={
        <Button variant="secondary" onClick={() => void refreshAccountsWorkspace()}>
          {t('common.refresh')}
        </Button>
      }
    />
  );

  const renderAccountCards = (rowsToRender = pageRows, paged = true) => (
    <section className={styles.tablePanel}>
      {paged ? renderBatchBar() : null}
      {rowsToRender.length > 0 ? (
        <div className={styles.accountCardList}>
          <div className={styles.accountCardHeader} data-account-list-header="true">
            <span>{t('accounts.list_header_credential')}</span>
            <span>{t('accounts.list_header_availability')}</span>
            <span>{t('accounts.list_header_recent_requests')}</span>
            <span>{t('accounts.list_header_historical_usage')}</span>
            <span>{t('accounts.list_header_quota')}</span>
            <span>{t('accounts.list_header_actions')}</span>
          </div>
          {rowsToRender.map((row) => {
            const recommendation = recommendationBySelectionKey.get(row.selectionKey) ?? null;
            const quotaWindows =
              quotaDisplayWindowsByRowKey.get(row.selectionKey) ?? buildQuotaDisplayWindows(row);
            const quotaCooldown = quotaCooldownsByRowKey.get(row.selectionKey)?.[0] ?? null;
            const codexStatus = codexStatusBySelectionKey.get(row.selectionKey) ?? null;
            const item = buildAccountListItem(row, {
              recommendation,
              quotaCooldown,
              codexStatus,
              quotaWindows,
            });
            const antigravityQuotaMatrix = buildAntigravityQuotaMatrix(row, quotaWindows);
            const displayQuotaWindows = antigravityQuotaMatrix ? [] : quotaWindows.slice(0, 2);
            const displayedQuotaWindowCount = antigravityQuotaMatrix
              ? antigravityQuotaMatrix.windowKeys.size
              : displayQuotaWindows.length;
            const hiddenQuotaWindowCount = Math.max(
              0,
              quotaWindows.length - displayedQuotaWindowCount
            );
            const quotaWindowTitle =
              quotaWindows
                .map((window) => {
                  const label = window.groupLabel
                    ? `${window.groupLabel} ${window.label}`
                    : window.label;
                  return `${label}: ${formatPercent(window.remainingPercent)}`;
                })
                .join('\n') || t('accounts.quota_source_none');
            const healthTitle = t(
              item.health.tooltipKey,
              formatQuotaResetTooltipParams(
                item.health.tooltipParams,
                item.health.resetAtMs,
                i18n.language,
                item.health.cooldown?.recoverAtMs
              )
            );
            const accountHistory = accountHistoryByRowKey.get(row.selectionKey) ?? null;
            const accountHistoryError = accountHistoryErrorsByRowKey.get(row.selectionKey) ?? '';
            const accountHistoryMatched = accountHistory?.matched === true;
            const accountHistoryTitle = getAccountHistoryTitle(
              t,
              accountHistory,
              accountHistoryLoading,
              accountHistoryError
            );
            const accountHistoryFootnote = accountHistoryError
              ? row.usage.success + row.usage.failure > 0
                ? t('accounts.history_recent_fallback')
                : t('accounts.history_unavailable')
              : accountHistoryLoading && !accountHistory
                ? t('accounts.history_loading')
                : accountHistory?.sync_status === 'pending'
                  ? t('accounts.history_syncing')
                  : null;
            const recentRequestCount = row.usage.success + row.usage.failure;
            const accountHistoryRequestValue = accountHistoryMatched
              ? formatCompactNumber(accountHistory.total_requests)
              : recentRequestCount > 0
                ? formatCompactNumber(recentRequestCount)
                : '-';
            const accountHistoryTokenValue = accountHistoryMatched
              ? formatCompactNumber(accountHistory.total_tokens)
              : '-';
            const accountHistoryCostValue = accountHistoryMatched
              ? formatMoney(accountHistory.total_cost)
              : '-';
            const accountHistorySuccessValue = accountHistoryMatched
              ? formatHistorySuccessRate(accountHistory.success_rate)
              : row.usage.successRate !== null
                ? formatPercent(row.usage.successRate, 1)
                : '-';
            return (
              <article
                key={row.selectionKey}
                data-account-card={row.selectionKey}
                aria-selected={selectedFiles.has(row.selectionKey)}
                className={[
                  styles.accountCard,
                  selectedRowKey === row.selectionKey ? styles.accountCardSelected : '',
                  selectedFiles.has(row.selectionKey) ? styles.accountCardBulkSelected : '',
                  isSelectionMode ? styles.accountCardSelectionMode : '',
                ]
                  .filter(Boolean)
                  .join(' ')}
                onClick={isSelectionMode ? () => handleAccountCardClick(row) : undefined}
              >
                <div className={styles.accountCardIdentity}>
                  <div className={styles.accountIdentityBadgeRow}>
                    <span className={styles.providerPill}>
                      {getProviderLabel(item.identity.provider, t)}
                    </span>
                    {item.identity.planType ? (
                      <span className={styles.accountMetaPill}>{item.identity.planType}</span>
                    ) : null}
                  </div>
                  <div className={styles.accountIdentityCopyLine}>
                    <button
                      type="button"
                      className={styles.accountIdentityCopyTarget}
                      title={row.accountLabel}
                      aria-label={`${t('common.copy')} ${row.accountLabel}`}
                      onClick={(event) =>
                        void handleCopyIdentityText(
                          event,
                          row.accountLabel,
                          `${row.selectionKey}:account`
                        )
                      }
                    >
                      <strong className={styles.accountIdentityTitle}>
                        {getDisplayAccount(row)}
                      </strong>
                    </button>
                    {copiedIdentityKey === `${row.selectionKey}:account` ? (
                      <span className={styles.accountIdentityCopyHint}>
                        {t('accounts.copy_feedback_copied')}
                      </span>
                    ) : null}
                  </div>
                  <div className={styles.accountIdentityCopyLine}>
                    <button
                      type="button"
                      className={styles.accountIdentityCopyTarget}
                      title={row.fileName}
                      aria-label={`${t('common.copy')} ${row.fileName}`}
                      onClick={(event) =>
                        void handleCopyIdentityText(event, row.fileName, `${row.selectionKey}:file`)
                      }
                    >
                      <span className={styles.accountCardFile}>
                        {getDisplayFileName(row.fileName)}
                      </span>
                    </button>
                    {copiedIdentityKey === `${row.selectionKey}:file` ? (
                      <span className={styles.accountIdentityCopyHint}>
                        {t('accounts.copy_feedback_copied')}
                      </span>
                    ) : null}
                  </div>
                </div>

                <div className={styles.accountCardHealth}>
                  <div className={styles.accountCardLine}>
                    <span
                      className={`${styles.badge} ${getHealthStatusClass(item.health.status)}`}
                      title={healthTitle}
                    >
                      {t(item.health.labelKey)}
                    </span>
                  </div>
                  <div className={styles.accountHealthMetaRow}>
                    <span
                      className={
                        item.identity.priorityIsNegative
                          ? styles.accountPriorityMetaDanger
                          : styles.accountPriorityMeta
                      }
                      title={t('accounts.col_priority')}
                    >
                      {t('accounts.col_priority')} {item.identity.priority}
                    </span>
                  </div>
                </div>

                <div className={styles.accountCardLatestRequest}>
                  <AccountLatestRequest
                    latestRequest={accountHistory?.latest_request}
                    recentRequests={accountHistory?.recent_requests}
                    loading={accountHistoryLoading && !accountHistory}
                    unavailable={Boolean(accountHistoryError)}
                    locale={i18n.language}
                    onCopy={copyTextWithNotification}
                  />
                </div>

                <div className={styles.accountCardEvidence} title={accountHistoryTitle}>
                  <div className={styles.accountHistoryGrid}>
                    <div
                      className={`${styles.accountHistoryMetric} ${styles.accountHistoryMetricRequests}`}
                      aria-label={`${t('accounts.history_requests')} ${accountHistoryRequestValue}`}
                      title={t('accounts.history_requests')}
                    >
                      <span className={styles.accountHistoryIcon}>
                        <IconSend size={13} />
                      </span>
                      <strong>{accountHistoryRequestValue}</strong>
                    </div>
                    <div
                      className={`${styles.accountHistoryMetric} ${styles.accountHistoryMetricTokens}`}
                      aria-label={`${t('accounts.history_tokens')} ${accountHistoryTokenValue}`}
                      title={t('accounts.history_tokens')}
                    >
                      <span className={styles.accountHistoryIcon}>
                        <IconBinary size={13} />
                      </span>
                      <strong>{accountHistoryTokenValue}</strong>
                    </div>
                    <div
                      className={`${styles.accountHistoryMetric} ${styles.accountHistoryMetricCost}`}
                      aria-label={`${t('accounts.history_cost')} ${accountHistoryCostValue}`}
                      title={t('accounts.history_cost')}
                    >
                      <span className={styles.accountHistoryIcon}>
                        <IconDollarSign size={13} />
                      </span>
                      <strong>{accountHistoryCostValue}</strong>
                    </div>
                    <div
                      className={`${styles.accountHistoryMetric} ${styles.accountHistoryMetricSuccess}`}
                      aria-label={`${t('accounts.history_success')} ${accountHistorySuccessValue}`}
                      title={t('accounts.history_success')}
                    >
                      <span className={styles.accountHistoryIcon}>
                        <IconCheck size={13} />
                      </span>
                      <strong>{accountHistorySuccessValue}</strong>
                    </div>
                  </div>
                  {accountHistoryFootnote ? (
                    <span className={styles.accountHistoryFootnote}>{accountHistoryFootnote}</span>
                  ) : null}
                </div>

                <div className={styles.accountCardBusiness}>
                  <div
                    className={[
                      styles.quotaWindowGrid,
                      hiddenQuotaWindowCount > 0 ? styles.quotaWindowGridHasMore : '',
                    ]
                      .filter(Boolean)
                      .join(' ')}
                    title={quotaWindowTitle}
                  >
                    {antigravityQuotaMatrix ? (
                      <AccountQuotaMatrix
                        accountKey={row.selectionKey}
                        matrix={antigravityQuotaMatrix}
                      />
                    ) : displayQuotaWindows.length > 0 ? (
                      displayQuotaWindows.map((window) => {
                        const windowRemaining = window.remainingPercent;
                        const windowWidth = Math.max(0, Math.min(100, windowRemaining ?? 0));
                        const resetLabel =
                          window.resetLabel && window.resetLabel !== '-' ? window.resetLabel : '';
                        const resetDisplayLabel = formatQuotaResetDisplay(
                          window.resetAtMs,
                          resetLabel,
                          i18n.language
                        );
                        const shortLabel = getQuotaWindowShortLabel(window);
                        return (
                          <div
                            key={window.key}
                            className={styles.quotaWindowCard}
                            title={`${window.label}: ${formatPercent(windowRemaining)}`}
                          >
                            <div className={styles.quotaWindowPrimaryLine}>
                              <span className={styles.quotaWindowSummary} title={window.label}>
                                {shortLabel}
                              </span>
                              <div className={styles.quotaTrack} aria-hidden="true">
                                <span
                                  className={`${styles.quotaBar} ${getRemainingBarClass(row)}`}
                                  style={{ width: `${windowWidth}%` }}
                                />
                              </div>
                              <strong className={styles.quotaWindowPercent}>
                                {windowRemaining !== null ? formatPercent(windowRemaining) : '-'}
                              </strong>
                              <span
                                className={styles.quotaResetMeta}
                                title={
                                  resetDisplayLabel !== '-'
                                    ? `${t('accounts.col_reset')}: ${resetDisplayLabel}`
                                    : ''
                                }
                              >
                                {resetDisplayLabel}
                              </span>
                            </div>
                          </div>
                        );
                      })
                    ) : (
                      <span className={styles.quotaEmptyState} data-account-quota-empty="true">
                        {t('accounts.quota_source_none')}
                      </span>
                    )}
                    {hiddenQuotaWindowCount > 0 ? (
                      <button
                        type="button"
                        className={styles.quotaMoreButton}
                        title={t('accounts.quota_more_windows_title', {
                          count: hiddenQuotaWindowCount,
                        })}
                        onClick={(event) => {
                          event.stopPropagation();
                          void openAccountDetail(row, 'quota');
                        }}
                      >
                        {t('accounts.quota_more_windows', {
                          count: hiddenQuotaWindowCount,
                        })}
                      </button>
                    ) : null}
                  </div>
                </div>

                <div className={styles.accountCardRecommendation}>
                  {renderRowActions(row, item.health.status === 'reauth')}
                </div>
              </article>
            );
          })}
        </div>
      ) : (
        renderAccountEmptyState()
      )}
      {paged ? renderPagination() : null}
    </section>
  );

  const renderDetailDrawer = () => {
    if (!selectedRow) {
      return (
        <Drawer
          open={false}
          onClose={() => setSelectedRowKey(null)}
          width="clamp(540px, 45vw, 720px)"
          className={styles.accountDetailDrawer}
        />
      );
    }

    const providerIcon = getAuthFileIcon(selectedRow.provider, resolvedTheme);

    const detailTabs: Array<{ id: DetailTab; label: string }> = [
      { id: 'overview', label: t('accounts.detail_tab_overview') },
      { id: 'quota', label: t('accounts.detail_tab_quota') },
      { id: 'config', label: t('accounts.detail_tab_config') },
      { id: 'models', label: t('accounts.detail_tab_models') },
      { id: 'diagnostics', label: t('accounts.detail_tab_diagnostics') },
    ];
    const valueRow =
      usageRows.find((row) => row.row?.selectionKey === selectedRow.selectionKey) ??
      usageRows.find((row) => !row.row && row.fileName === selectedRow.fileName);
    const selectedQuotaWindows = (
      effectiveQuotaWindowDefinitionsByRowKey.get(selectedRow.selectionKey) ??
      buildAccountQuotaWindowDefinitions(buildQuotaDisplayWindows(selectedRow))
    ).map((definition) => ({
      ...definition.display,
      providerWindowId: definition.providerWindowId,
      resetAtMs: definition.cycleEndMs,
      resetAccuracy:
        definition.boundaryAccuracy === 'exact'
          ? ('exact' as const)
          : definition.boundaryAccuracy === 'derived' || definition.boundaryAccuracy === 'estimated'
            ? ('estimated' as const)
            : ('unknown' as const),
      fromMs: definition.cycleStartMs,
      toMs: definition.cycleEndMs,
      observationSource: definition.observationSource,
      observedAtMs: definition.observedAtMs,
      windowMode: definition.windowMode,
      cycleStartMs: definition.cycleStartMs,
      cycleEndMs: definition.cycleEndMs,
      limitWindowSeconds: definition.durationSeconds,
      modelScope: definition.modelScope,
      boundaryAccuracy: definition.boundaryAccuracy,
      stale: definition.stale,
      logicalWindowId: definition.logicalWindowId,
      activationGeneration: definition.activationGeneration,
      availability: definition.availability,
      relationshipKind: definition.relationshipKind,
      containerProviderWindowId: definition.containerProviderWindowId,
      firstSeenAtMs: definition.firstSeenAtMs,
      lastSeenAtMs: definition.lastSeenAtMs,
      missingSinceMs: definition.missingSinceMs,
      deactivatedAtMs: definition.deactivatedAtMs,
      currentCycle: definition.currentCycle,
      previousCycle: definition.previousCycle,
    }));
    const selectedQuotaCooldown = quotaCooldownsByRowKey.get(selectedRow.selectionKey)?.[0] ?? null;
    const selectedCodexQuota =
      selectedRow.provider === CODEX_CONFIG.type
        ? mergeCodexResetCreditsFromQuotaSnapshots(
            getDisplayCodexQuota(selectedRow.raw),
            quotaSnapshotWindowsByRowKey.get(selectedRow.selectionKey) ?? []
          )
        : undefined;
    const selectedCodexStatus = codexStatusBySelectionKey.get(selectedRow.selectionKey) ?? null;
    const hasMatchingDetailEvents = detailEventsRowKey === selectedRow.selectionKey;
    const rowEvents = hasMatchingDetailEvents ? detailEvents : [];
    const rowEventsSummary = hasMatchingDetailEvents ? detailEventsSummary : null;
    const rowEventsRecentFailure = hasMatchingDetailEvents ? detailEventsRecentFailure : null;
    const rowEventsTotalCount = hasMatchingDetailEvents ? detailEventsTotalCount : 0;
    const detailView = buildAccountDetailViewModel(selectedRow, {
      recommendation: recommendationBySelectionKey.get(selectedRow.selectionKey) ?? null,
      quotaCooldown: selectedQuotaCooldown,
      codexStatus: selectedCodexStatus,
      quotaWindows: selectedQuotaWindows,
      windowUsageByKey: matchingAccountWindowUsageByKey,
      actionCandidates: accountActionCandidates,
      matchedActionCandidates: actionCandidatesByRowKey.get(selectedRow.selectionKey) ?? [],
      history: accountHistoryByRowKey.get(selectedRow.selectionKey) ?? null,
      valueRow,
      codexQuota: selectedCodexQuota,
      xaiQuota:
        selectedRow.provider === XAI_CONFIG.type
          ? getCredentialScopedQuotaState(xaiQuota, selectedRow.raw)
          : undefined,
      diagnosticsSummary: rowEventsSummary,
      diagnosticsRecentFailure: rowEventsRecentFailure,
      diagnosticsEvents: rowEvents,
      diagnosticsTotalCount: rowEventsTotalCount,
    });
    const eventsUnavailable =
      !featureAvailability.requestMonitoringAvailable ||
      !featureAvailability.managerServiceBase ||
      !managementKey;
    const modelsTargetMatches = modelsSelectionKey === selectedRow.selectionKey;

    const renderActiveDetail = () => {
      if (detailTab === 'quota') {
        return (
          <AccountQuotaTab
            detailView={detailView}
            windowUsageError={accountWindowUsageError}
            historyAvailable={requestHistoryAvailable}
            historyRefreshing={historyRefreshing}
            onRefreshHistory={() => void refreshAccountHistory(selectedRow)}
            onResetQuota={() => resetCodexQuotaForRow(selectedRow)}
            resetQuotaDisabled={!canResetCodexQuota(selectedRow) || configurationSaving}
          />
        );
      }
      if (detailTab === 'config') {
        return (
          <AccountConfigurationTab
            row={selectedRow}
            disableControls={disableControls}
            editor={configurationEditor}
            onCopyText={copyTextWithNotification}
          />
        );
      }
      if (detailTab === 'models') {
        return (
          <AccountModelsTab
            key={`${selectedRow.selectionKey}:models`}
            row={selectedRow}
            disableControls={disableControls}
            fileName={getDisplayFileName(
              modelsTargetMatches ? modelsFileName || selectedRow.fileName : selectedRow.fileName
            )}
            fileType={
              modelsTargetMatches ? modelsFileType || selectedRow.provider : selectedRow.provider
            }
            loading={modelsTargetMatches ? modelsLoading : true}
            refreshing={modelsTargetMatches ? modelsRefreshing : false}
            error={modelsTargetMatches ? modelsError : null}
            models={modelsTargetMatches ? modelsList : []}
            modelDefinitions={modelsTargetMatches ? modelDefinitions : []}
            modelDefinitionsLoading={modelsTargetMatches ? modelDefinitionsLoading : true}
            modelDefinitionsError={modelsTargetMatches ? modelDefinitionsError : null}
            globalExcluded={oauthState.excluded}
            globalExcludedState={oauthState.excludedError}
            aliases={oauthState.modelAlias}
            editor={configurationEditor}
            onRefresh={() => void handleAccountModelsRefresh()}
            onManageGlobalRules={() => void changeActiveView('oauth')}
            onOpenAdvancedRules={() => void openAccountDetail(selectedRow, 'config')}
            onCopyText={copyTextWithNotification}
          />
        );
      }
      if (detailTab === 'diagnostics') {
        return (
          <AccountDiagnosticsTab
            row={selectedRow}
            detailView={detailView}
            inspectionLoading={inspectionLoading}
            candidatesLoading={accountActionCandidatesLoading}
            candidatesError={accountActionCandidatesError}
            events={rowEvents}
            eventsTotalCount={rowEventsTotalCount}
            eventsHasMore={detailEventsHasMore}
            eventsLoading={detailEventsLoading}
            eventsRefreshing={detailEventsRefreshing}
            eventsAppending={detailEventsAppending}
            eventsError={detailEventsError}
            eventsUnavailable={eventsUnavailable}
            nextBeforeMs={detailEventsNextBeforeMs}
            nextBeforeId={detailEventsNextBeforeId}
            onRefreshEvents={() => void refreshDetailEvents(selectedRow)}
            onLoadMoreEvents={(beforeMs, beforeId) =>
              void loadDetailEvents(selectedRow, {
                append: true,
                beforeMs,
                beforeId,
              })
            }
          />
        );
      }
      return (
        <AccountOverviewTab
          detailView={detailView}
          getHealthStatusClass={getHealthStatusClass}
          onSelectTab={(tab) => void openAccountDetail(selectedRow, tab)}
        />
      );
    };
    const selectedCredentialRefreshing =
      credentialRefreshing[getAuthFileSelectionKey(selectedRow.raw)] === true;
    const drawerMoreItems: DropdownMenuItem[] = [
      {
        key: 'models',
        label: t('auth_files.models_button'),
        icon: <IconModelCluster size={15} />,
        onClick: () => {
          void openAccountDetail(selectedRow, 'models');
        },
        disabled: selectedRow.runtimeOnly && selectedRow.provider !== 'aistudio',
      },
      ...(selectedRow.provider === CODEX_CONFIG.type && !selectedRow.runtimeOnly
        ? [
            {
              key: 'refresh-credential',
              label: t('auth_files.credential_refresh_button'),
              icon: <IconRefreshCw size={15} />,
              onClick: () => {
                void handleCredentialRefresh(selectedRow.raw);
              },
              disabled: disableControls || configurationSaving || selectedCredentialRefreshing,
            } satisfies DropdownMenuItem,
          ]
        : []),
      {
        key: 'download',
        label: t('auth_files.download_button'),
        icon: <IconDownload size={15} />,
        onClick: () => void handleDownload(selectedRow.fileName),
        disabled: selectedRow.runtimeOnly,
      },
      ...(canResetCodexQuota(selectedRow)
        ? [
            {
              key: 'reset-codex-quota',
              label: t('codex_quota.reset_action_button'),
              icon: <IconRefreshCw size={15} />,
              onClick: () => resetCodexQuotaForRow(selectedRow),
              disabled: configurationSaving,
            } satisfies DropdownMenuItem,
          ]
        : []),
      { key: 'drawer-danger-divider', type: 'divider' },
      {
        key: 'delete',
        label: t('auth_files.delete_button'),
        icon: <IconTrash2 size={15} />,
        onClick: () => void handleAccountDelete(selectedRow.raw),
        disabled:
          disableControls ||
          configurationSaving ||
          selectedRow.runtimeOnly ||
          deleting === selectedRow.fileName,
        tone: 'danger',
      },
    ];

    return (
      <Drawer
        key={selectedRow.selectionKey}
        open
        onClose={closeAccountDetail}
        onBeforeClose={confirmConfigurationDiscard}
        width="clamp(540px, 45vw, 720px)"
        className={styles.accountDetailDrawer}
        bodyRef={detailDrawerBodyRef}
        title={
          <div className={styles.drawerTitleIdentity}>
            <span
              className={styles.drawerProviderIcon}
              data-account-provider-icon={selectedRow.provider}
              aria-hidden="true"
            >
              {providerIcon ? <img src={providerIcon} alt="" /> : <IconKey size={24} />}
            </span>
            <div className={styles.drawerTitleStack}>
              <strong className={styles.drawerTitlePrimary} title={selectedRow.accountLabel}>
                {getDisplayAccount(selectedRow)}
              </strong>
              <span className={styles.drawerTitleMeta}>
                {getProviderLabel(selectedRow.provider, t)} · {selectedRow.planType ?? '-'} ·{' '}
                <button
                  type="button"
                  className={styles.drawerFileNameCopy}
                  onClick={() => copyTextWithNotification(selectedRow.fileName)}
                  title={t('common.copy', { defaultValue: '点击复制' })}
                  aria-label={`${t('common.copy', { defaultValue: '点击复制' })} ${getDisplayFileName(selectedRow.fileName)}`}
                >
                  {getDisplayFileName(selectedRow.fileName)}
                  <IconCopy size={12} />
                </button>
              </span>
            </div>
          </div>
        }
        footer={
          <div className={styles.drawerActions}>
            {detailView.health.status === 'reauth' ? (
              <Button
                variant="primary"
                onClick={() => handleReauthAccount(selectedRow.raw)}
                disabled={disableControls || configurationSaving || selectedRow.runtimeOnly}
              >
                <IconShield size={16} />
                {t('accounts.recommend_action_reauth')}
              </Button>
            ) : null}
            <Button
              variant="secondary"
              onClick={() => void refreshAccountQuota(selectedRow)}
              loading={quotaRefreshing}
            >
              {!quotaRefreshing ? <IconRefreshCw size={16} /> : null}
              {t('accounts.refresh_quota')}
            </Button>
            <Button
              variant={selectedRow.disabled ? 'secondary' : 'danger'}
              onClick={() => handleBatchStatus(selectedRow.disabled, [selectedRow])}
              disabled={
                disableControls || configurationSaving || statusUpdating || selectedRow.runtimeOnly
              }
            >
              {selectedRow.disabled ? t('accounts.enable') : t('accounts.disable')}
            </Button>
            <DropdownMenu
              items={drawerMoreItems}
              ariaLabel={t('accounts.drawer_more_actions')}
              triggerTitle={t('accounts.drawer_more_actions')}
              triggerLabel={t('accounts.batch_more')}
              triggerIcon={<IconMoreVertical size={16} />}
              triggerClassName={styles.drawerMoreActions}
            />
          </div>
        }
      >
        <div className={styles.drawerBodyShell} data-detail-tab={detailTab}>
          {selectedRow.disabled ? (
            <div className={styles.drawerDisabledNotice} role="status">
              <span>
                {t('accounts.detail_disabled_notice_title', { defaultValue: '账号已禁用' })}
              </span>
              <p>
                {t('accounts.detail_disabled_notice_desc', {
                  defaultValue:
                    '此账号当前不接收请求,各 Tab 仅展示只读摘要。点击底部"启用"按钮可恢复完整功能。',
                })}
              </p>
            </div>
          ) : null}
          <div
            className={styles.drawerTabs}
            role="tablist"
            aria-label={t('accounts.detail_tablist_label')}
          >
            {detailTabs.map((tab) => (
              <button
                key={tab.id}
                type="button"
                role="tab"
                id={`accounts-detail-tab-${tab.id}`}
                aria-selected={detailTab === tab.id}
                aria-controls="accounts-detail-tab-panel"
                className={detailTab === tab.id ? styles.drawerTabActive : ''}
                onClick={() => void openAccountDetail(selectedRow, tab.id)}
              >
                {tab.label}
              </button>
            ))}
          </div>
          <div
            id="accounts-detail-tab-panel"
            className={styles.drawerTabPanel}
            role="tabpanel"
            aria-labelledby={`accounts-detail-tab-${detailTab}`}
          >
            {renderActiveDetail()}
          </div>
        </div>
      </Drawer>
    );
  };

  const renderAccountsOverview = () => (
    <>
      <AccountMetricsGrid metrics={metrics} />
      {error ? <div className={styles.errorBox}>{error}</div> : null}
      {loading ? (
        <div className={styles.loadingPanel}>
          <LoadingSpinner />
        </div>
      ) : (
        renderAccountCards()
      )}
      {renderDetailDrawer()}
    </>
  );

  const renderHealthView = () => (
    <CredentialHealthInspectionWorkspace
      mode={healthMode}
      onModeChange={changeHealthMode}
      onSnapshotChange={applyInspectionSnapshot}
      onCredentialsChanged={handleInspectionCredentialsChanged}
      onOpenCredential={handleOpenInspectionCredential}
    />
  );

  const renderOAuthPreviewRow = (row: (typeof oauthPreviewRows)[number]) => {
    const catalogLabel = row.catalogModels.length
      ? row.catalogModels
          .map((model) => (model.displayName ? `${model.id} (${model.displayName})` : model.id))
          .join(' · ')
      : t('accounts.oauth_preview_catalog_hidden');

    return (
      <div
        key={row.provider}
        className={styles.previewRow}
        data-oauth-preview-provider={row.provider}
        data-oauth-preview-status={row.effectiveStatus}
      >
        <div className={styles.previewRowHeader}>
          <strong>{getProviderLabel(row.provider, t)}</strong>
          <span className={styles.previewStatus}>
            {t(`accounts.oauth_preview_status_${row.effectiveStatus}`)}
          </span>
        </div>
        <dl className={styles.previewDetails}>
          <div>
            <dt>{t('accounts.oauth_preview_route_label')}</dt>
            <dd>
              {row.inputModel || '-'} → {row.upstreamModel || '-'}
            </dd>
          </div>
          <div>
            <dt>{t('accounts.oauth_preview_catalog_label')}</dt>
            <dd>{catalogLabel}</dd>
          </div>
          <div>
            <dt>{t('accounts.oauth_preview_response_label')}</dt>
            <dd>
              {row.responseModel || '-'} ·{' '}
              {t(
                row.forceMapping
                  ? 'accounts.oauth_preview_response_forced'
                  : 'accounts.oauth_preview_response_passthrough'
              )}
            </dd>
          </div>
        </dl>
        <small>
          {t(row.explanationKey, {
            model: row.upstreamModel || '-',
            pattern: row.matchedExclude || '-',
          })}
        </small>
      </div>
    );
  };

  const renderOAuthView = () => (
    <section className={styles.oauthGrid}>
      <div className={styles.oauthCardStack}>
        <OAuthExcludedCard
          disableControls={disableControls}
          loadState={oauthState.excludedError}
          excluded={oauthState.excluded}
          onRetry={loadOauthExcluded}
          onAdd={() => openOauthExcludedEditor()}
          onEdit={openOauthExcludedEditor}
          onDelete={oauthState.deleteExcluded}
        />
        <OAuthModelAliasCard
          disableControls={disableControls}
          viewMode={oauthViewMode}
          onViewModeChange={setOauthViewMode}
          onAdd={() => openOauthModelAliasEditor()}
          onEditProvider={openOauthModelAliasEditor}
          onDeleteProvider={oauthState.deleteModelAlias}
          loadState={oauthState.modelAliasError}
          modelAlias={oauthState.modelAlias}
          onRetry={loadOauthModelAlias}
          allProviderModels={oauthState.allProviderModels}
          onUpdate={oauthState.handleMappingUpdate}
          onDeleteLink={oauthState.handleDeleteLink}
          onToggleFork={oauthState.handleToggleFork}
          onRenameAlias={oauthState.handleRenameAlias}
          onDeleteAlias={oauthState.handleDeleteAlias}
        />
      </div>
      <aside className={styles.rulePanel}>
        <div className={styles.previewPanelHeader}>
          <h2>{t('accounts.oauth_preview_title')}</h2>
          <p className={styles.previewScope}>{t('accounts.oauth_preview_scope')}</p>
        </div>
        <div className={styles.previewControls}>
          <Input
            label={t('accounts.oauth_preview_input_label')}
            value={oauthPreviewModel}
            onChange={(event) => {
              setOauthPreviewModel(event.target.value);
              setOauthDirectExpanded(false);
            }}
            placeholder={t('accounts.oauth_preview_placeholder')}
            aria-label={t('accounts.oauth_preview_input_label')}
          />
          <div className={styles.previewProviderFilter}>
            <label id="oauth-preview-provider-label">
              {t('accounts.oauth_preview_provider_label')}
            </label>
            <Select
              id="oauth-preview-provider"
              value={oauthPreviewProvider}
              options={oauthPreviewProviderOptions}
              onChange={(value) => {
                setOauthPreviewProvider(value);
                setOauthDirectExpanded(false);
              }}
              ariaLabelledBy="oauth-preview-provider-label"
            />
          </div>
        </div>
        <div className={styles.previewList}>
          {oauthPreviewRows.length === 0 ? (
            <p className={styles.previewEmpty}>{t('accounts.oauth_preview_empty')}</p>
          ) : oauthAffectedPreviewRows.length === 0 && oauthDirectPreviewRows.length === 0 ? (
            <p className={styles.previewEmpty}>{t('accounts.oauth_preview_provider_empty')}</p>
          ) : (
            <>
              {oauthAffectedPreviewRows.map(renderOAuthPreviewRow)}
              {!oauthPreviewProvider && oauthDirectPreviewRows.length > 0 ? (
                <button
                  type="button"
                  className={styles.previewDirectSummary}
                  data-oauth-preview-direct-summary={oauthDirectPreviewRows.length}
                  aria-expanded={oauthDirectExpanded}
                  onClick={() => setOauthDirectExpanded((current) => !current)}
                >
                  <span>
                    {t('accounts.oauth_preview_direct_summary', {
                      count: oauthDirectPreviewRows.length,
                    })}
                  </span>
                  <span>
                    {t(
                      oauthDirectExpanded
                        ? 'accounts.oauth_preview_direct_collapse'
                        : 'accounts.oauth_preview_direct_expand'
                    )}
                  </span>
                </button>
              ) : null}
              {oauthPreviewProvider || oauthDirectExpanded
                ? oauthDirectPreviewRows.map(renderOAuthPreviewRow)
                : null}
            </>
          )}
        </div>
      </aside>
    </section>
  );

  const renderPageActions = () => (
    <div className={styles.headerActions}>
      <Button
        variant="secondary"
        size="sm"
        onClick={() => void refreshActiveWorkspace()}
        disabled={loading}
      >
        <IconRefreshCw size={15} />
        {t('common.refresh')}
      </Button>
      <Button
        variant="secondary"
        size="sm"
        onClick={() => setAuthJsonPasteOpen(true)}
        disabled={disableControls || authJsonPasteSaving}
        loading={authJsonPasteSaving}
      >
        {!authJsonPasteSaving ? <IconFileText size={15} /> : null}
        {t('auth_files.paste_button')}
      </Button>
      <Button
        variant="primary"
        size="sm"
        onClick={handleUploadClick}
        disabled={disableControls || uploading}
        loading={uploading}
      >
        {!uploading ? <IconPlus size={15} /> : null}
        {t('auth_files.upload_button')}
      </Button>
      <input
        ref={fileInputRef}
        type="file"
        accept=".json,application/json"
        multiple
        hidden
        onChange={(event) => void handleFileChange(event)}
      />
    </div>
  );

  const renderActiveView = () => {
    if (activeView === 'health') return renderHealthView();
    if (activeView === 'oauth') return renderOAuthView();
    return renderAccountsOverview();
  };

  return (
    <div className={styles.container} lang={i18n.language}>
      <section className={styles.controlsPanel}>
        <div className={styles.controlsTabsRow}>
          {renderViewTabs()}
          {renderPageActions()}
        </div>
      </section>
      {activeView === 'accounts' ? (
        <section className={styles.controlsFilterPanel}>{renderToolbar()}</section>
      ) : null}
      {renderActiveView()}
      {renderMobileFilterPanel()}
      {renderFloatingBatchActions()}
      <AuthJsonPasteModal
        open={authJsonPasteOpen}
        saving={authJsonPasteSaving}
        disabled={disableControls}
        onClose={() => {
          if (!authJsonPasteSaving) setAuthJsonPasteOpen(false);
        }}
        onSave={handleSavePastedAuthJson}
      />
      <OAuthExcludedEditorModal
        open={oauthExcludedEditorProvider !== null}
        provider={oauthExcludedEditorProvider ?? ''}
        files={files}
        excluded={oauthState.excluded}
        modelAlias={oauthState.modelAlias}
        disabled={disableControls}
        unsupported={oauthState.excludedError === 'unsupported'}
        onClose={() => setOauthExcludedEditorProvider(null)}
        onSaved={reloadOauthRules}
      />
      <OAuthModelAliasEditorModal
        open={oauthModelAliasEditorProvider !== null}
        provider={oauthModelAliasEditorProvider ?? ''}
        files={files}
        excluded={oauthState.excluded}
        modelAlias={oauthState.modelAlias}
        disabled={disableControls}
        unsupported={oauthState.modelAliasError === 'unsupported'}
        onClose={() => setOauthModelAliasEditorProvider(null)}
        onSaved={reloadOauthRules}
      />
      <Modal
        open={batchPriorityOpen}
        onClose={() => {
          if (!batchFieldsUpdating) setBatchPriorityOpen(false);
        }}
        closeDisabled={batchFieldsUpdating}
        title={t('accounts.batch_priority_title')}
        width={420}
        footer={
          <div className={styles.batchPriorityFooter}>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setBatchPriorityOpen(false)}
              disabled={batchFieldsUpdating}
            >
              {t('common.cancel')}
            </Button>
            <Button
              size="sm"
              onClick={() => void handleBatchPrioritySave()}
              disabled={disableControls || selectedRows.length === 0 || batchFieldsUpdating}
              loading={batchFieldsUpdating}
            >
              {t('common.confirm')}
            </Button>
          </div>
        }
      >
        <div className={styles.batchPriorityModal}>
          <Input
            label={t('accounts.priority_label')}
            placeholder={t('accounts.priority_placeholder')}
            hint={t('accounts.priority_hint')}
            value={batchPriorityValue}
            onChange={(event) => setBatchPriorityValue(event.target.value)}
            disabled={disableControls || batchFieldsUpdating}
            inputMode="numeric"
            autoFocus
            onKeyDown={(event) => {
              if (event.key !== 'Enter' || disableControls || batchFieldsUpdating) return;
              void handleBatchPrioritySave();
            }}
          />
        </div>
      </Modal>
      <CodexReauthDialog
        open={Boolean(codexReauthTarget)}
        target={codexReauthTarget}
        onClose={() => setCodexReauthTarget(null)}
        onSuccess={handleCodexReauthSuccess}
      />
    </div>
  );
}
