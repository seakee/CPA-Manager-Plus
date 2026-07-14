import {
  useCallback,
  type CSSProperties,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
} from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { animate } from 'motion/mini';
import type { AnimationPlaybackControlsWithThen } from 'motion-dom';
import { useInterval } from '@/hooks/useInterval';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { usePageTransitionLayer } from '@/components/common/PageTransitionLayer';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { Select } from '@/components/ui/Select';
import { IconFilterAll, IconSearch } from '@/components/ui/icons';
import { EmptyState } from '@/components/ui/EmptyState';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  CODEX_CONFIG,
  buildObservedCodexQuotaState,
  getQuotaStoreKey,
  resolveQuotaDisplayState,
} from '@/components/quota';
import { copyToClipboard } from '@/utils/clipboard';
import { getStatusFromError, resolveAuthProvider } from '@/utils/quota';
import {
  MAX_CARD_PAGE_SIZE,
  MIN_CARD_PAGE_SIZE,
  QUOTA_PROVIDER_TYPES,
  clampCardPageSize,
  getAuthFileIcon,
  getTypeColor,
  getTypeLabel,
  hasAuthFileStatusMessage,
  isHealthyAuthFile,
  isRuntimeOnlyAuthFile,
  normalizeProviderKey,
  parsePriorityValue,
  supportsAuthFileWebsockets,
  type QuotaProviderType,
  type ResolvedTheme,
} from '@/features/authFiles/constants';
import { AuthFileCard } from '@/features/authFiles/components/AuthFileCard';
import { AuthJsonPasteModal } from '@/features/authFiles/components/AuthJsonPasteModal';
import { AuthFileModelsModal } from '@/features/authFiles/components/AuthFileModelsModal';
import { AuthFilesPrefixProxyEditorModal } from '@/features/authFiles/components/AuthFilesPrefixProxyEditorModal';
import { OAuthExcludedCard } from '@/features/authFiles/components/OAuthExcludedCard';
import { OAuthModelAliasCard } from '@/features/authFiles/components/OAuthModelAliasCard';
import { CodexReauthDialog } from '@/features/oauth/CodexReauthDialog';
import {
  createCodexReauthTargetFromAuthFile,
  type CodexReauthTarget,
} from '@/features/oauth/codexReauthModel';
import {
  monitoringAnalyticsApi,
  usageServiceApi,
  type QuotaCooldownInfo,
  type UsageHeaderSnapshot,
} from '@/services/api/usageService';
import {
  buildUsageHeaderSnapshotLookup,
  getHighConfidenceUsageHeaderSnapshotForAuthFile,
  isUsageHeaderQuotaSnapshotExpired,
} from '@/utils/usageHeaderSnapshots';
import { useAuthFilesData } from '@/features/authFiles/hooks/useAuthFilesData';
import { useAuthFilesModels } from '@/features/authFiles/hooks/useAuthFilesModels';
import { useAuthFilesOauth } from '@/features/authFiles/hooks/useAuthFilesOauth';
import { useAuthFilesPrefixProxyEditor } from '@/features/authFiles/hooks/useAuthFilesPrefixProxyEditor';
import { useAuthFilesStatusBarCache } from '@/features/authFiles/hooks/useAuthFilesStatusBarCache';
import { useAntigravitySubscriptions } from '@/features/authFiles/hooks/useAntigravitySubscriptions';
import {
  BATCH_BAR_BASE_TRANSFORM,
  BATCH_BAR_HIDDEN_TRANSFORM,
  DEFAULT_COMPACT_PAGE_SIZE,
  DEFAULT_REGULAR_PAGE_SIZE,
  authFileMatchesCodexPlanFilter,
  authFileMatchesCodexStatusFilter,
  buildAuthFileCodexInspectionMap,
  buildWildcardSearch,
  compareAuthFileName,
  compareAuthFileNote,
  compareAuthFilePriority,
  easePower2In,
  easePower3Out,
  getAuthFileCodexInspectionKey,
  getAuthFileCodexInspectionKeyForFile,
  getAuthFileCodexStatus,
  getAuthFilePatchTarget,
  getAuthFilePlanSortRank,
  getAuthFileScopedCodexQuota,
  getAuthFileSearchValues,
  getAuthFileSelectionKey,
  getAuthFileNameFromSelectionKey,
  getFreshAuthFileCodexStatusSources,
  hasPartialSharedAuthFileSelection,
  normalizeAuthFilesCodexPlanFilter,
  normalizeAuthFilesCodexStatusFilter,
  stringifySearchValue,
  type AuthFileCodexInspectionSnapshot,
  type AuthFilesCodexPlanFilter,
  type AuthFilesCodexStatusFilter,
} from '@/features/authFiles/model/authFilesPageModel';
import {
  createCodexInspectionConnectionFingerprint,
  loadCodexInspectionLastRun,
} from '@/features/monitoring/codexInspection';
import {
  normalizeAuthFilesSortMode,
  normalizeAuthFilesViewMode,
  readAuthFilesUiState,
  readPersistedAuthFilesCompactMode,
  writeAuthFilesUiState,
  writePersistedAuthFilesCompactMode,
  type AuthFilesSortMode,
} from '@/features/authFiles/uiState';
import type { AuthJsonInputType } from '@/features/authFiles/sessionAuthConverter';
import type { AuthFileItem, CodexQuotaState } from '@/types';
import {
  captureQuotaCacheGeneration,
  commitIfQuotaCacheCurrent,
  useAuthStore,
  useNotificationStore,
  useQuotaStore,
  useThemeStore,
} from '@/stores';
import styles from './AuthFilesPage.module.scss';

const hasInlineQuotaLayout = (file: AuthFileItem): boolean => {
  if (isRuntimeOnlyAuthFile(file)) return false;
  const provider = resolveAuthProvider(file);
  return QUOTA_PROVIDER_TYPES.has(provider as QuotaProviderType);
};

type CodexInspectionSnapshotSource = {
  fileName: string;
  authIndex?: string | number | null;
  statusCode?: number | string | null;
  action?: string | null;
  usedPercent?: number | string | null;
  isQuota?: boolean | null;
};

const readCodexInspectionRunAtMs = (run: {
  finishedAtMs?: number;
  updatedAtMs?: number;
  startedAtMs?: number;
}): number | null =>
  run.finishedAtMs && Number.isFinite(run.finishedAtMs)
    ? run.finishedAtMs
    : run.updatedAtMs && Number.isFinite(run.updatedAtMs)
      ? run.updatedAtMs
      : run.startedAtMs && Number.isFinite(run.startedAtMs)
        ? run.startedAtMs
        : null;

const toAuthFileCodexInspectionSnapshots = (
  results: CodexInspectionSnapshotSource[],
  inspectionAtMs?: number | null
): AuthFileCodexInspectionSnapshot[] =>
  results.map((item) => ({
    fileName: item.fileName,
    authIndex: item.authIndex ?? null,
    statusCode: item.statusCode ?? null,
    action: item.action ?? null,
    usedPercent: item.usedPercent ?? null,
    isQuota: item.isQuota ?? null,
    inspectionAtMs: inspectionAtMs ?? null,
  }));

const isStaleCodexReauthSnapshot = (item: AuthFileCodexInspectionSnapshot): boolean => {
  const action = typeof item.action === 'string' ? item.action.trim().toLowerCase() : '';
  const statusCode =
    typeof item.statusCode === 'number'
      ? item.statusCode
      : typeof item.statusCode === 'string'
        ? Number(item.statusCode)
        : null;
  return action === 'reauth' || statusCode === 401;
};

type QuotaCooldownState = {
  contextKey: string;
  items: Map<string, QuotaCooldownInfo>;
};

const getQuotaCooldownContextKey = (managerServiceBase: string, managementKey: string): string =>
  `${managerServiceBase}\u0000${managementKey}`;

export function AuthFilesPage() {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const resolvedTheme: ResolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const codexQuota = useQuotaStore((state) => state.codexQuota);
  const setCodexQuota = useQuotaStore((state) => state.setCodexQuota);
  const featureAvailability = usePanelFeatureAvailability();
  const managerServiceBase = featureAvailability.managerServiceBase;
  const pageTransitionLayer = usePageTransitionLayer();
  const isCurrentLayer = pageTransitionLayer ? pageTransitionLayer.status === 'current' : true;
  const navigate = useNavigate();
  const connectionFingerprint = useMemo(
    () => createCodexInspectionConnectionFingerprint(apiBase, managementKey),
    [apiBase, managementKey]
  );

  const [filter, setFilter] = useState<'all' | string>('all');
  const [problemOnly, setProblemOnly] = useState(false);
  const [disabledOnly, setDisabledOnly] = useState(false);
  const [healthyOnly, setHealthyOnly] = useState(false);
  const [codexStatusFilter, setCodexStatusFilter] = useState<AuthFilesCodexStatusFilter>('all');
  const [codexPlanFilter, setCodexPlanFilter] = useState<AuthFilesCodexPlanFilter>('all');
  const [compactMode, setCompactMode] = useState(false);
  const [search, setSearch] = useState('');
  const [page, setPage] = useState(1);
  const [pageSizeByMode, setPageSizeByMode] = useState({
    regular: DEFAULT_REGULAR_PAGE_SIZE,
    compact: DEFAULT_COMPACT_PAGE_SIZE,
  });
  const [pageSizeInput, setPageSizeInput] = useState('9');
  const [viewMode, setViewMode] = useState<'diagram' | 'list'>('list');
  const [sortMode, setSortMode] = useState<AuthFilesSortMode>('default');
  const [batchActionBarVisible, setBatchActionBarVisible] = useState(false);
  const [uiStateHydrated, setUiStateHydrated] = useState(false);
  const [authJsonPasteOpen, setAuthJsonPasteOpen] = useState(false);
  const [batchPriorityOpen, setBatchPriorityOpen] = useState(false);
  const [batchPriorityValue, setBatchPriorityValue] = useState('');
  const [codexReauthTarget, setCodexReauthTarget] = useState<CodexReauthTarget | null>(null);
  const [lastCodexInspectionResults, setLastCodexInspectionResults] = useState<
    AuthFileCodexInspectionSnapshot[]
  >([]);
  const [quotaCooldownState, setQuotaCooldownState] = useState<QuotaCooldownState>(() => ({
    contextKey: getQuotaCooldownContextKey(managerServiceBase, managementKey),
    items: new Map(),
  }));
  const quotaCooldowns = quotaCooldownState.items;
  const [headerSnapshots, setHeaderSnapshots] = useState<UsageHeaderSnapshot[]>([]);
  const [headerSnapshotGeneratedAtMs, setHeaderSnapshotGeneratedAtMs] = useState(0);
  const floatingBatchActionsRef = useRef<HTMLDivElement>(null);
  const batchActionAnimationRef = useRef<AnimationPlaybackControlsWithThen | null>(null);
  const previousSelectionCountRef = useRef(0);
  const selectionCountRef = useRef(0);
  const quotaCooldownsRef = useRef<QuotaCooldownState>({
    contextKey: getQuotaCooldownContextKey(managerServiceBase, managementKey),
    items: new Map(),
  });
  const pendingRecoveredCodexQuotaRefreshRef = useRef<Set<string>>(new Set());
  const autoRefreshingCodexQuotaRef = useRef<Set<string>>(new Set());
  const expiredHeaderCodexQuotaRefreshRef = useRef<Set<string>>(new Set());
  const skipNextRecoveredCooldownRefreshRef = useRef(false);
  // Generation token for in-flight cooldown fetches. Every fetch and every
  // context identity change bump it, so a slow, superseded response can be
  // detected and dropped; otherwise it would re-introduce stale badges after
  // the old context was invalidated.
  const cooldownReqId = useRef(0);
  const headerSnapshotReqId = useRef(0);
  // Tracks the context identity so the layout effect can detect cross-context
  // transitions synchronously (before passive effects fire) and invalidate any
  // in-flight request that belongs to the old context.
  const cooldownContextRef = useRef({ managerServiceBase, managementKey });
  const cooldownRecoveryContextRef = useRef({ managerServiceBase, managementKey });
  const headerSnapshotContextRef = useRef({ managerServiceBase, managementKey });

  const {
    files,
    selectedFiles,
    selectionCount,
    loading,
    error,
    uploading,
    authJsonPasteSaving,
    deleting,
    deletingAll,
    statusUpdating,
    batchStatusUpdating,
    batchFieldsUpdating,
    fileInputRef,
    loadFiles,
    handleUploadClick,
    handleFileChange,
    savePastedAuthJson,
    handleDelete,
    handleDeleteAll,
    handleDownload,
    handleStatusToggle,
    toggleSelect,
    selectAllVisible,
    invertVisibleSelection,
    deselectAll,
    batchDownload,
    batchSetStatus,
    batchPatchFields,
    batchDelete,
  } = useAuthFilesData({ connectionFingerprint });

  const statusBarCache = useAuthFilesStatusBarCache(files);
  const uniqueAuthFileKeyByFallbackCooldownKey = useMemo(() => {
    const fallbackEntries = new Map<string, { authFileKey: string; count: number }>();
    files.forEach((file) => {
      const provider = resolveAuthProvider(file);
      if (isRuntimeOnlyAuthFile(file) || (provider !== 'codex' && provider !== 'xai')) return;
      const fallbackKey = getAuthFileCodexInspectionKey(file.name, null);
      const authFileKey = getAuthFileCodexInspectionKeyForFile(file);
      const existing = fallbackEntries.get(fallbackKey);
      if (existing) {
        existing.count += 1;
        return;
      }
      fallbackEntries.set(fallbackKey, { authFileKey, count: 1 });
    });

    const uniqueKeys = new Map<string, string>();
    fallbackEntries.forEach((entry, fallbackKey) => {
      if (entry.count === 1) uniqueKeys.set(fallbackKey, entry.authFileKey);
    });
    return uniqueKeys;
  }, [files]);
  const getQuotaCooldownForFile = useCallback(
    (file: AuthFileItem): QuotaCooldownInfo | undefined => {
      const provider = resolveAuthProvider(file);
      if (isRuntimeOnlyAuthFile(file) || (provider !== 'codex' && provider !== 'xai')) {
        return undefined;
      }
      const authFileKey = getAuthFileCodexInspectionKeyForFile(file);
      const exactCooldown = quotaCooldowns.get(authFileKey);
      if (exactCooldown) return exactCooldown;

      const fallbackKey = getAuthFileCodexInspectionKey(file.name, null);
      if (uniqueAuthFileKeyByFallbackCooldownKey.get(fallbackKey) !== authFileKey) {
        return undefined;
      }
      return quotaCooldowns.get(fallbackKey);
    },
    [quotaCooldowns, uniqueAuthFileKeyByFallbackCooldownKey]
  );

  const {
    excluded,
    excludedError,
    modelAlias,
    modelAliasError,
    allProviderModels,
    loadExcluded,
    loadModelAlias,
    deleteExcluded,
    deleteModelAlias,
    handleMappingUpdate,
    handleDeleteLink,
    handleToggleFork,
    handleRenameAlias,
    handleDeleteAlias,
  } = useAuthFilesOauth({ viewMode, files });

  const {
    modelsModalOpen,
    modelsLoading,
    modelsList,
    modelsFileName,
    modelsFileType,
    modelsError,
    showModels,
    closeModelsModal,
  } = useAuthFilesModels();

  const {
    prefixProxyEditor,
    prefixProxyUpdatedText,
    prefixProxyDirty,
    openPrefixProxyEditor,
    closePrefixProxyEditor,
    handlePrefixProxyChange,
    handlePrefixProxySave,
  } = useAuthFilesPrefixProxyEditor({
    disableControls: connectionStatus !== 'connected',
    loadFiles,
  });

  const disableControls = connectionStatus !== 'connected';
  const normalizedFilter = normalizeProviderKey(String(filter));
  const pageSize = compactMode ? pageSizeByMode.compact : pageSizeByMode.regular;
  useEffect(() => {
    const persistedCompactMode = readPersistedAuthFilesCompactMode();
    if (typeof persistedCompactMode === 'boolean') {
      setCompactMode(persistedCompactMode);
    }

    const persisted = readAuthFilesUiState();
    if (persisted) {
      if (typeof persisted.filter === 'string' && persisted.filter.trim()) {
        setFilter(normalizeProviderKey(persisted.filter));
      }
      if (typeof persisted.problemOnly === 'boolean') {
        setProblemOnly(persisted.problemOnly);
      }
      if (typeof persisted.disabledOnly === 'boolean') {
        setDisabledOnly(persisted.disabledOnly);
      }
      if (typeof persisted.healthyOnly === 'boolean') {
        setHealthyOnly(persisted.healthyOnly);
      }
      const persistedCodexStatusFilter = normalizeAuthFilesCodexStatusFilter(
        persisted.codexStatusFilter
      );
      if (persistedCodexStatusFilter) {
        setCodexStatusFilter(persistedCodexStatusFilter);
      }
      const persistedCodexPlanFilter = normalizeAuthFilesCodexPlanFilter(persisted.codexPlanFilter);
      if (persistedCodexPlanFilter) {
        setCodexPlanFilter(persistedCodexPlanFilter);
      }
      if (typeof persistedCompactMode !== 'boolean' && typeof persisted.compactMode === 'boolean') {
        setCompactMode(persisted.compactMode);
      }
      if (typeof persisted.search === 'string') {
        setSearch(persisted.search);
      }
      if (typeof persisted.page === 'number' && Number.isFinite(persisted.page)) {
        setPage(Math.max(1, Math.round(persisted.page)));
      }
      const legacyPageSize =
        typeof persisted.pageSize === 'number' && Number.isFinite(persisted.pageSize)
          ? clampCardPageSize(persisted.pageSize)
          : null;
      const regularPageSize =
        typeof persisted.regularPageSize === 'number' && Number.isFinite(persisted.regularPageSize)
          ? clampCardPageSize(persisted.regularPageSize)
          : (legacyPageSize ?? DEFAULT_REGULAR_PAGE_SIZE);
      const compactPageSize =
        typeof persisted.compactPageSize === 'number' && Number.isFinite(persisted.compactPageSize)
          ? clampCardPageSize(persisted.compactPageSize)
          : (legacyPageSize ?? DEFAULT_COMPACT_PAGE_SIZE);
      setPageSizeByMode({
        regular: regularPageSize,
        compact: compactPageSize,
      });
      const persistedSortMode = normalizeAuthFilesSortMode(persisted.sortMode);
      if (persistedSortMode) {
        setSortMode(persistedSortMode);
      }
      const persistedViewMode = normalizeAuthFilesViewMode(persisted.viewMode);
      if (persistedViewMode) {
        setViewMode(persistedViewMode);
      }
    }

    setUiStateHydrated(true);
  }, []);

  useEffect(() => {
    if (!uiStateHydrated) return;

    writeAuthFilesUiState({
      filter,
      problemOnly,
      disabledOnly,
      healthyOnly,
      codexStatusFilter,
      codexPlanFilter,
      compactMode,
      search,
      page,
      pageSize,
      regularPageSize: pageSizeByMode.regular,
      compactPageSize: pageSizeByMode.compact,
      sortMode,
      viewMode,
    });
    writePersistedAuthFilesCompactMode(compactMode);
  }, [
    codexPlanFilter,
    codexStatusFilter,
    compactMode,
    disabledOnly,
    filter,
    healthyOnly,
    page,
    pageSize,
    pageSizeByMode,
    problemOnly,
    search,
    sortMode,
    uiStateHydrated,
    viewMode,
  ]);

  useEffect(() => {
    setPageSizeInput(String(pageSize));
  }, [pageSize]);

  const loadCodexInspectionSnapshots = useCallback(async () => {
    const lastRun = connectionFingerprint
      ? loadCodexInspectionLastRun(connectionFingerprint)
      : null;

    const managerServiceBase = featureAvailability.managerServiceBase;
    if (
      !featureAvailability.checking &&
      featureAvailability.serverCodexInspectionAvailable &&
      managerServiceBase
    ) {
      try {
        const runs = await usageServiceApi.listCodexInspectionRuns(
          managerServiceBase,
          managementKey,
          1
        );
        const latestRun = runs.items[0];
        if (latestRun) {
          const detail = await usageServiceApi.getCodexInspectionRun(
            managerServiceBase,
            managementKey,
            latestRun.id
          );
          setLastCodexInspectionResults(
            toAuthFileCodexInspectionSnapshots(
              detail.results,
              readCodexInspectionRunAtMs(detail.run)
            )
          );
          return;
        }
      } catch {
        // Fall back to the browser-side cache when the manager service is unavailable.
      }
    }

    setLastCodexInspectionResults(
      lastRun
        ? toAuthFileCodexInspectionSnapshots(
            lastRun.result.results,
            lastRun.result.finishedAt || lastRun.result.startedAt || null
          )
        : []
    );
  }, [
    connectionFingerprint,
    featureAvailability.checking,
    featureAvailability.managerServiceBase,
    featureAvailability.serverCodexInspectionAvailable,
    managementKey,
  ]);

  useEffect(() => {
    if (!isCurrentLayer) return;
    void loadCodexInspectionSnapshots();
  }, [isCurrentLayer, loadCodexInspectionSnapshots]);

  const setCurrentModePageSize = useCallback(
    (next: number) => {
      setPageSizeByMode((current) =>
        compactMode ? { ...current, compact: next } : { ...current, regular: next }
      );
    },
    [compactMode]
  );

  const commitPageSizeInput = (rawValue: string) => {
    const trimmed = rawValue.trim();
    if (!trimmed) {
      setPageSizeInput(String(pageSize));
      return;
    }

    const value = Number(trimmed);
    if (!Number.isFinite(value)) {
      setPageSizeInput(String(pageSize));
      return;
    }

    const next = clampCardPageSize(value);
    setCurrentModePageSize(next);
    setPageSizeInput(String(next));
    setPage(1);
  };

  const handlePageSizeChange = (event: ChangeEvent<HTMLInputElement>) => {
    const rawValue = event.currentTarget.value;
    setPageSizeInput(rawValue);

    const trimmed = rawValue.trim();
    if (!trimmed) return;

    const parsed = Number(trimmed);
    if (!Number.isFinite(parsed)) return;

    const rounded = Math.round(parsed);
    if (rounded < MIN_CARD_PAGE_SIZE || rounded > MAX_CARD_PAGE_SIZE) return;

    setCurrentModePageSize(rounded);
    setPage(1);
  };

  const handleSortModeChange = useCallback(
    (value: string) => {
      const nextSortMode = normalizeAuthFilesSortMode(value);
      if (!nextSortMode || nextSortMode === sortMode) return;
      setSortMode(nextSortMode);
      setPage(1);
      void loadFiles().catch(() => {});
    },
    [loadFiles, sortMode]
  );

  const handleSavePastedAuthJson = useCallback(
    async (type: AuthJsonInputType, fileName: string, jsonText: string) => {
      await savePastedAuthJson(type, fileName, jsonText);
      setAuthJsonPasteOpen(false);
    },
    [savePastedAuthJson]
  );

  const handleHeaderRefresh = useCallback(async () => {
    await Promise.all([
      loadFiles(),
      loadExcluded(),
      loadModelAlias(),
      loadCodexInspectionSnapshots(),
    ]);
  }, [loadFiles, loadExcluded, loadModelAlias, loadCodexInspectionSnapshots]);

  useHeaderRefresh(handleHeaderRefresh);

  useEffect(() => {
    if (!isCurrentLayer) return;
    loadFiles();
    loadExcluded();
    loadModelAlias();
  }, [isCurrentLayer, loadFiles, loadExcluded, loadModelAlias]);

  useInterval(
    () => {
      void loadFiles().catch(() => {});
    },
    isCurrentLayer ? 240_000 : null
  );

  const loadQuotaCooldowns = useCallback(async () => {
    // Stamp this fetch with a fresh id so a later fetch or context identity
    // invalidation can supersede it. If the generation has changed by the time
    // we land, we drop the result instead of writing stale badges back.
    const id = ++cooldownReqId.current;
    const contextKey = getQuotaCooldownContextKey(managerServiceBase, managementKey);
    try {
      const items = await usageServiceApi.getActiveQuotaCooldowns(
        managerServiceBase,
        managementKey
      );
      if (id !== cooldownReqId.current) return;
      const next = new Map<string, QuotaCooldownInfo>();
      for (const item of items) {
        if (!item.authFileName) continue;
        const cooldownKey = getAuthFileCodexInspectionKey(
          item.authFileName,
          item.authIndex ?? null
        );
        const existing = next.get(cooldownKey);
        if (!existing || (item.recoverAtMs ?? 0) > (existing.recoverAtMs ?? 0)) {
          next.set(cooldownKey, item);
        }
      }
      setQuotaCooldownState({ contextKey, items: next });
    } catch {
      // The cooldown badge is a derived hint; fail silently and keep the last known state.
    }
  }, [managerServiceBase, managementKey]);

  const loadHeaderSnapshots = useCallback(async () => {
    if (!managerServiceBase) {
      setHeaderSnapshots([]);
      setHeaderSnapshotGeneratedAtMs(0);
      return;
    }
    const id = ++headerSnapshotReqId.current;
    try {
      const response = await monitoringAnalyticsApi.getHeaderSnapshots(
        managerServiceBase,
        managementKey,
        {
          days: 30,
          limit: 1000,
        }
      );
      if (id !== headerSnapshotReqId.current) return;
      const generatedAtMs =
        response.generated_at_ms ??
        (response as { generatedAtMs?: number }).generatedAtMs ??
        Date.now();
      expiredHeaderCodexQuotaRefreshRef.current.clear();
      setHeaderSnapshots(response.items ?? []);
      setHeaderSnapshotGeneratedAtMs(generatedAtMs);
    } catch {
      // Header snapshots are passive hints; keep the current page usable if Manager data is unavailable.
    }
  }, [managementKey, managerServiceBase]);

  const refreshRecoveredCodexQuotaForFile = useCallback(
    async (file: AuthFileItem) => {
      if (resolveAuthProvider(file) !== 'codex') return false;
      if (isRuntimeOnlyAuthFile(file) || file.disabled) return false;

      const authFileKey = getAuthFileCodexInspectionKeyForFile(file);
      if (autoRefreshingCodexQuotaRef.current.has(authFileKey)) return false;
      autoRefreshingCodexQuotaRef.current.add(authFileKey);
      const cacheGeneration = captureQuotaCacheGeneration();

      const storeKey = getQuotaStoreKey(CODEX_CONFIG, file);
      const previousQuota =
        (codexQuota[storeKey] as CodexQuotaState | undefined) ??
        (codexQuota[file.name] as CodexQuotaState | undefined);
      setCodexQuota((prev) => ({
        ...prev,
        [storeKey]: CODEX_CONFIG.buildLoadingState(file),
      }));

      try {
        const data = await CODEX_CONFIG.fetchQuota(file, t);
        return commitIfQuotaCacheCurrent(cacheGeneration, () => {
          setCodexQuota((prev) => ({
            ...prev,
            [storeKey]: CODEX_CONFIG.buildSuccessState(data, file),
          }));
        });
      } catch (err: unknown) {
        const message = err instanceof Error ? err.message : t('common.unknown_error');
        const status = getStatusFromError(err);
        commitIfQuotaCacheCurrent(cacheGeneration, () => {
          setCodexQuota((prev) => ({
            ...prev,
            [storeKey]: CODEX_CONFIG.buildFailureState
              ? CODEX_CONFIG.buildFailureState(message, status, file, previousQuota, Date.now())
              : CODEX_CONFIG.buildErrorState(message, status, file),
          }));
        });
        return false;
      } finally {
        autoRefreshingCodexQuotaRef.current.delete(authFileKey);
      }
    },
    [codexQuota, setCodexQuota, t]
  );

  const refreshPendingRecoveredCodexQuotas = useCallback(() => {
    if (pendingRecoveredCodexQuotaRefreshRef.current.size === 0) return;

    for (const file of files) {
      const authFileKey = getAuthFileCodexInspectionKeyForFile(file);
      const fallbackKey = getAuthFileCodexInspectionKey(file.name, null);
      const hasExactPending = pendingRecoveredCodexQuotaRefreshRef.current.has(authFileKey);
      const hasFallbackPending =
        fallbackKey !== authFileKey &&
        pendingRecoveredCodexQuotaRefreshRef.current.has(fallbackKey) &&
        uniqueAuthFileKeyByFallbackCooldownKey.get(fallbackKey) === authFileKey;
      if (!hasExactPending && !hasFallbackPending) continue;
      if (file.disabled || resolveAuthProvider(file) !== 'codex' || isRuntimeOnlyAuthFile(file)) {
        continue;
      }
      if (hasExactPending) pendingRecoveredCodexQuotaRefreshRef.current.delete(authFileKey);
      if (hasFallbackPending) pendingRecoveredCodexQuotaRefreshRef.current.delete(fallbackKey);
      void refreshRecoveredCodexQuotaForFile(file);
    }
  }, [files, refreshRecoveredCodexQuotaForFile, uniqueAuthFileKeyByFallbackCooldownKey]);

  // Synchronously invalidate in-flight cooldown requests when the context
  // (managerServiceBase or managementKey) changes, regardless of direction
  // (A to B, A to empty, empty to A). This runs in the layout phase, before any
  // passive effect that might fire a new loadQuotaCooldowns, so a stale
  // response that resolves between renders or inside the gap between a
  // re-render and its passive effects will find its generation token already
  // invalidated.
  useLayoutEffect(() => {
    const prev = cooldownContextRef.current;
    if (prev.managerServiceBase === managerServiceBase && prev.managementKey === managementKey) {
      return;
    }
    cooldownContextRef.current = { managerServiceBase, managementKey };
    cooldownReqId.current += 1;
    const contextKey = getQuotaCooldownContextKey(managerServiceBase, managementKey);
    quotaCooldownsRef.current = { contextKey, items: new Map() };
    pendingRecoveredCodexQuotaRefreshRef.current.clear();
    skipNextRecoveredCooldownRefreshRef.current = true;
    setQuotaCooldownState((current) =>
      current.contextKey === contextKey && current.items.size === 0
        ? current
        : { contextKey, items: new Map() }
    );
  }, [managerServiceBase, managementKey]);

  useLayoutEffect(() => {
    const prev = headerSnapshotContextRef.current;
    if (prev.managerServiceBase === managerServiceBase && prev.managementKey === managementKey) {
      return;
    }
    headerSnapshotContextRef.current = { managerServiceBase, managementKey };
    headerSnapshotReqId.current += 1;
    expiredHeaderCodexQuotaRefreshRef.current.clear();
    setHeaderSnapshots((current) => (current.length === 0 ? current : []));
    setHeaderSnapshotGeneratedAtMs(0);
  }, [managerServiceBase, managementKey]);

  useEffect(() => {
    if (!isCurrentLayer || !managerServiceBase) return;
    void loadQuotaCooldowns();
    void loadHeaderSnapshots();
  }, [isCurrentLayer, managerServiceBase, loadHeaderSnapshots, loadQuotaCooldowns]);

  useInterval(
    () => {
      void loadQuotaCooldowns();
      void loadHeaderSnapshots();
    },
    isCurrentLayer && managerServiceBase ? 60_000 : null
  );

  useEffect(() => {
    const previous = quotaCooldownsRef.current;
    const currentContextKey = getQuotaCooldownContextKey(managerServiceBase, managementKey);
    const previousContext = cooldownRecoveryContextRef.current;
    const contextChanged =
      previousContext.managerServiceBase !== managerServiceBase ||
      previousContext.managementKey !== managementKey;
    cooldownRecoveryContextRef.current = { managerServiceBase, managementKey };
    quotaCooldownsRef.current = quotaCooldownState;
    if (
      contextChanged ||
      previous.contextKey !== quotaCooldownState.contextKey ||
      quotaCooldownState.contextKey !== currentContextKey ||
      skipNextRecoveredCooldownRefreshRef.current
    ) {
      skipNextRecoveredCooldownRefreshRef.current = false;
      pendingRecoveredCodexQuotaRefreshRef.current.clear();
      return;
    }
    if (!isCurrentLayer || !managerServiceBase) return;

    const nowMs = Date.now();
    let hasRecovered = false;
    previous.items.forEach((item, authFileKey) => {
      if (quotaCooldowns.has(authFileKey)) return;
      if (item.owner && item.owner !== 'cpamp_usage_429') return;
      if (item.recoverAtMs > nowMs + 60_000) return;
      pendingRecoveredCodexQuotaRefreshRef.current.add(authFileKey);
      hasRecovered = true;
    });

    if (!hasRecovered) return;
    void loadFiles().catch(() => {});
    refreshPendingRecoveredCodexQuotas();
  }, [
    isCurrentLayer,
    loadFiles,
    managerServiceBase,
    managementKey,
    quotaCooldowns,
    quotaCooldownState,
    refreshPendingRecoveredCodexQuotas,
  ]);

  useEffect(() => {
    refreshPendingRecoveredCodexQuotas();
  }, [files, refreshPendingRecoveredCodexQuotas]);

  const existingTypes = useMemo(() => {
    const types = new Set<string>(['all']);
    files.forEach((file) => {
      const type = normalizeProviderKey(String(file.type ?? file.provider ?? ''));
      if (type) types.add(type);
    });
    return Array.from(types);
  }, [files]);

  const codexInspectionByAuthFile = useMemo(
    () => buildAuthFileCodexInspectionMap(lastCodexInspectionResults),
    [lastCodexInspectionResults]
  );

  const headerSnapshotLookup = useMemo(
    () => buildUsageHeaderSnapshotLookup(headerSnapshots),
    [headerSnapshots]
  );

  const getActiveCodexQuota = useCallback(
    (file: AuthFileItem): CodexQuotaState | undefined => {
      if (resolveAuthProvider(file) !== 'codex') return undefined;
      const storeKey = getAuthFileCodexInspectionKeyForFile(file);
      return getAuthFileScopedCodexQuota(file, codexQuota[storeKey] ?? codexQuota[file.name]);
    },
    [codexQuota]
  );

  useEffect(() => {
    if (!isCurrentLayer || !managerServiceBase) return;
    const nowMs = headerSnapshotGeneratedAtMs || Date.now();
    for (const file of files) {
      if (resolveAuthProvider(file) !== 'codex' || isRuntimeOnlyAuthFile(file) || file.disabled) {
        continue;
      }
      const headerSnapshot = getHighConfidenceUsageHeaderSnapshotForAuthFile(
        headerSnapshotLookup,
        file
      );
      if (!isUsageHeaderQuotaSnapshotExpired(headerSnapshot, nowMs)) continue;

      const activeQuota = getActiveCodexQuota(file);
      const fetchedAtMs =
        activeQuota?.status === 'success' && typeof activeQuota.fetchedAtMs === 'number'
          ? activeQuota.fetchedAtMs
          : 0;
      const headerAtMs =
        typeof headerSnapshot?.timestamp_ms === 'number' ? headerSnapshot.timestamp_ms : 0;
      if (fetchedAtMs > headerAtMs) continue;

      const authFileKey = getAuthFileCodexInspectionKeyForFile(file);
      const marker = `${authFileKey}:${headerSnapshot?.event_hash ?? ''}:${headerAtMs}`;
      if (expiredHeaderCodexQuotaRefreshRef.current.has(marker)) continue;
      expiredHeaderCodexQuotaRefreshRef.current.add(marker);
      void refreshRecoveredCodexQuotaForFile(file);
    }
  }, [
    files,
    getActiveCodexQuota,
    headerSnapshotGeneratedAtMs,
    headerSnapshotLookup,
    isCurrentLayer,
    managerServiceBase,
    refreshRecoveredCodexQuotaForFile,
  ]);

  const codexStatusSourcesByAuthFileKey = useMemo(() => {
    const sourcesMap = new Map<string, ReturnType<typeof getFreshAuthFileCodexStatusSources>>();
    files.forEach((file) => {
      const statusKey = getAuthFileCodexInspectionKeyForFile(file);
      const headerSnapshot = getHighConfidenceUsageHeaderSnapshotForAuthFile(
        headerSnapshotLookup,
        file
      );
      const freshHeaderSnapshot = isUsageHeaderQuotaSnapshotExpired(
        headerSnapshot,
        headerSnapshotGeneratedAtMs || Date.now()
      )
        ? undefined
        : headerSnapshot;
      sourcesMap.set(
        statusKey,
        getFreshAuthFileCodexStatusSources(
          file,
          getActiveCodexQuota(file),
          codexInspectionByAuthFile.get(statusKey),
          freshHeaderSnapshot
        )
      );
    });
    return sourcesMap;
  }, [
    codexInspectionByAuthFile,
    files,
    getActiveCodexQuota,
    headerSnapshotGeneratedAtMs,
    headerSnapshotLookup,
  ]);

  const getDisplayCodexQuota = useCallback(
    (file: AuthFileItem): CodexQuotaState | undefined => {
      if (resolveAuthProvider(file) !== 'codex') return undefined;
      const statusKey = getAuthFileCodexInspectionKeyForFile(file);
      const activeQuota = getActiveCodexQuota(file);
      const observedQuota = buildObservedCodexQuotaState(
        file,
        codexStatusSourcesByAuthFileKey.get(statusKey)?.headerSnapshot,
        t
      );
      return resolveQuotaDisplayState(activeQuota, observedQuota);
    },
    [codexStatusSourcesByAuthFileKey, getActiveCodexQuota, t]
  );

  const codexStatusByAuthFileKey = useMemo(() => {
    const statusMap = new Map<string, ReturnType<typeof getAuthFileCodexStatus>>();
    files.forEach((file) => {
      const statusKey = getAuthFileCodexInspectionKeyForFile(file);
      const sources = codexStatusSourcesByAuthFileKey.get(statusKey);
      statusMap.set(
        statusKey,
        getAuthFileCodexStatus(
          file,
          getDisplayCodexQuota(file),
          sources?.inspection,
          sources?.headerSnapshot
        )
      );
    });
    return statusMap;
  }, [codexStatusSourcesByAuthFileKey, files, getDisplayCodexQuota]);

  const filesMatchingStatusFilters = useMemo(
    () =>
      files.filter((file) => {
        if (disabledOnly && file.disabled !== true) return false;
        if (healthyOnly && !isHealthyAuthFile(file)) return false;
        const codexStatus = codexStatusByAuthFileKey.get(
          getAuthFileCodexInspectionKeyForFile(file)
        );
        if (problemOnly && !hasAuthFileStatusMessage(file) && !codexStatus?.badges.length) {
          return false;
        }
        if (codexStatus && !authFileMatchesCodexStatusFilter(codexStatus, codexStatusFilter)) {
          return false;
        }
        if (
          !authFileMatchesCodexPlanFilter(
            file,
            getDisplayCodexQuota(file),
            codexPlanFilter,
            getHighConfidenceUsageHeaderSnapshotForAuthFile(headerSnapshotLookup, file)
          )
        ) {
          return false;
        }
        return true;
      }),
    [
      codexPlanFilter,
      codexStatusByAuthFileKey,
      codexStatusFilter,
      disabledOnly,
      files,
      getDisplayCodexQuota,
      headerSnapshotLookup,
      healthyOnly,
      problemOnly,
    ]
  );

  const sortOptions = useMemo(
    () => [
      { value: 'default', label: t('auth_files.sort_default') },
      { value: 'name-asc', label: t('auth_files.sort_name_asc') },
      { value: 'note-asc', label: t('auth_files.sort_note_asc') },
      { value: 'note-desc', label: t('auth_files.sort_note_desc') },
      { value: 'priority-desc', label: t('auth_files.sort_priority_desc') },
      { value: 'priority-asc', label: t('auth_files.sort_priority_asc') },
      { value: 'plan-desc', label: t('auth_files.sort_plan_desc') },
      { value: 'plan-asc', label: t('auth_files.sort_plan_asc') },
    ],
    [t]
  );

  const codexStatusFilterOptions = useMemo(
    () => [
      { value: 'all', label: t('auth_files.codex_status_filter_all') },
      { value: 'reauth', label: t('auth_files.codex_status_filter_reauth') },
      { value: 'quota_limited', label: t('auth_files.codex_status_filter_quota_limited') },
      {
        value: 'five_hour_limited',
        label: t('auth_files.codex_status_filter_five_hour_limited'),
      },
      { value: 'weekly_limited', label: t('auth_files.codex_status_filter_weekly_limited') },
      { value: 'monthly_limited', label: t('auth_files.codex_status_filter_monthly_limited') },
      {
        value: 'disabled_with_reset',
        label: t('auth_files.codex_status_filter_disabled_with_reset'),
      },
    ],
    [t]
  );

  const codexPlanFilterOptions = useMemo(
    () => [
      { value: 'all', label: t('auth_files.codex_plan_filter_all') },
      { value: 'free', label: t('codex_quota.plan_free') },
      { value: 'plus', label: t('codex_quota.plan_plus') },
      { value: 'team', label: t('codex_quota.plan_team') },
      { value: 'prolite', label: t('codex_quota.plan_prolite') },
      { value: 'pro', label: t('codex_quota.plan_pro') },
      { value: 'unknown', label: t('auth_files.codex_plan_filter_unknown') },
    ],
    [t]
  );

  const typeCounts = useMemo(() => {
    const counts: Record<string, number> = { all: filesMatchingStatusFilters.length };
    filesMatchingStatusFilters.forEach((file) => {
      const type = normalizeProviderKey(String(file.type ?? file.provider ?? ''));
      if (!type) return;
      counts[type] = (counts[type] || 0) + 1;
    });
    return counts;
  }, [filesMatchingStatusFilters]);

  const normalizedSearch = search.trim();
  const wildcardSearch = useMemo(() => buildWildcardSearch(normalizedSearch), [normalizedSearch]);

  const filtered = useMemo(() => {
    const normalizedTerm = normalizedSearch.toLowerCase();

    return filesMatchingStatusFilters.filter((item) => {
      const type = normalizeProviderKey(String(item.type ?? item.provider ?? ''));
      const matchType = normalizedFilter === 'all' || type === normalizedFilter;
      const authFileKey = getAuthFileCodexInspectionKeyForFile(item);
      const planHeaderSnapshot = getHighConfidenceUsageHeaderSnapshotForAuthFile(
        headerSnapshotLookup,
        item
      );
      const statusHeaderSnapshot = codexStatusSourcesByAuthFileKey.get(authFileKey)?.headerSnapshot;
      const matchSearch =
        !normalizedSearch ||
        stringifySearchValue(
          getAuthFileSearchValues(
            item,
            t,
            getDisplayCodexQuota(item),
            codexStatusByAuthFileKey.get(authFileKey),
            statusHeaderSnapshot,
            planHeaderSnapshot
          )
        ).some((value) => {
          const content = value.toString();
          return wildcardSearch
            ? wildcardSearch.test(content)
            : content.toLowerCase().includes(normalizedTerm);
        });
      return matchType && matchSearch;
    });
  }, [
    codexStatusByAuthFileKey,
    codexStatusSourcesByAuthFileKey,
    filesMatchingStatusFilters,
    getDisplayCodexQuota,
    headerSnapshotLookup,
    normalizedFilter,
    normalizedSearch,
    t,
    wildcardSearch,
  ]);

  const sorted = useMemo(() => {
    const copy = [...filtered];
    if (sortMode === 'default') {
      copy.sort((a, b) => {
        const providerA = normalizeProviderKey(String(a.provider ?? a.type ?? 'unknown'));
        const providerB = normalizeProviderKey(String(b.provider ?? b.type ?? 'unknown'));
        const providerCompare = providerA.localeCompare(providerB);
        if (providerCompare !== 0) return providerCompare;
        return compareAuthFileName(a, b);
      });
    } else if (sortMode === 'name-asc') {
      copy.sort(compareAuthFileName);
    } else if (sortMode === 'note-asc' || sortMode === 'note-desc') {
      copy.sort((a, b) => compareAuthFileNote(a, b, sortMode === 'note-desc' ? 'desc' : 'asc'));
    } else if (sortMode === 'priority-asc' || sortMode === 'priority-desc') {
      copy.sort((a, b) =>
        compareAuthFilePriority(a, b, sortMode === 'priority-desc' ? 'desc' : 'asc')
      );
    } else if (sortMode === 'plan-asc' || sortMode === 'plan-desc') {
      copy.sort((a, b) => {
        const leftRank = getAuthFilePlanSortRank(
          a,
          getDisplayCodexQuota(a),
          getHighConfidenceUsageHeaderSnapshotForAuthFile(headerSnapshotLookup, a)
        );
        const rightRank = getAuthFilePlanSortRank(
          b,
          getDisplayCodexQuota(b),
          getHighConfidenceUsageHeaderSnapshotForAuthFile(headerSnapshotLookup, b)
        );
        const leftKnown = leftRank !== null && leftRank !== undefined;
        const rightKnown = rightRank !== null && rightRank !== undefined;

        if (leftKnown || rightKnown) {
          if (!leftKnown) return 1;
          if (!rightKnown) return -1;
          const rankDiff = sortMode === 'plan-desc' ? rightRank - leftRank : leftRank - rightRank;
          if (rankDiff !== 0) return rankDiff;
        }

        return compareAuthFileName(a, b);
      });
    }
    return copy;
  }, [filtered, getDisplayCodexQuota, headerSnapshotLookup, sortMode]);

  const totalPages = Math.max(1, Math.ceil(sorted.length / pageSize));
  const currentPage = Math.min(page, totalPages);
  const start = (currentPage - 1) * pageSize;
  const pageItems = useMemo(() => sorted.slice(start, start + pageSize), [pageSize, sorted, start]);
  const { subscriptions: antigravitySubscriptions, refreshSubscription } =
    useAntigravitySubscriptions();
  const pageHasInlineQuotaCards = !compactMode && pageItems.some(hasInlineQuotaLayout);
  const selectablePageItems = useMemo(
    () => pageItems.filter((file) => !isRuntimeOnlyAuthFile(file)),
    [pageItems]
  );
  const selectableFilteredItems = useMemo(
    () => sorted.filter((file) => !isRuntimeOnlyAuthFile(file)),
    [sorted]
  );
  const fileBySelectionKey = useMemo(() => {
    const map = new Map<string, AuthFileItem>();
    files.forEach((file) => {
      map.set(getAuthFileSelectionKey(file), file);
    });
    return map;
  }, [files]);
  const selectedKeys = useMemo(() => Array.from(selectedFiles), [selectedFiles]);
  const selectedFileNames = useMemo(
    () =>
      Array.from(
        new Set(selectedKeys.map(getAuthFileNameFromSelectionKey).filter((name) => name.trim()))
      ),
    [selectedKeys]
  );
  const selectedTargetFiles = useMemo(
    () =>
      selectedKeys
        .map((key) => fileBySelectionKey.get(key))
        .filter((file): file is AuthFileItem => Boolean(file)),
    [fileBySelectionKey, selectedKeys]
  );
  const selectedPatchTargets = useMemo(
    () => selectedTargetFiles.map(getAuthFilePatchTarget),
    [selectedTargetFiles]
  );
  const selectedWebsocketPatchTargets = useMemo(
    () =>
      selectedTargetFiles
        .filter((file) => supportsAuthFileWebsockets(String(file.type ?? file.provider ?? '')))
        .map(getAuthFilePatchTarget),
    [selectedTargetFiles]
  );
  const selectedHasStatusUpdating = useMemo(
    () => selectedFileNames.some((name) => statusUpdating[name] === true),
    [selectedFileNames, statusUpdating]
  );
  const selectedHasPartialSharedAuthFile = useMemo(
    () => hasPartialSharedAuthFileSelection(files, selectedKeys),
    [files, selectedKeys]
  );
  const batchStatusButtonsDisabled =
    disableControls ||
    selectedFileNames.length === 0 ||
    batchStatusUpdating ||
    selectedHasStatusUpdating;
  const batchFieldsButtonsDisabled =
    disableControls || selectedPatchTargets.length === 0 || batchFieldsUpdating;
  const batchCodexFieldsButtonsDisabled =
    disableControls || selectedWebsocketPatchTargets.length === 0 || batchFieldsUpdating;
  const batchDeleteButtonsDisabled =
    disableControls || selectedFileNames.length === 0 || selectedHasPartialSharedAuthFile;

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

  const handleOpenBatchPriority = useCallback(() => {
    setBatchPriorityValue('');
    setBatchPriorityOpen(true);
  }, []);

  const handleBatchPrioritySave = useCallback(async () => {
    const parsedPriority = parsePriorityValue(batchPriorityValue);
    if (parsedPriority === undefined) {
      showNotification(t('auth_files.batch_priority_invalid'), 'error');
      return;
    }

    const result = await batchPatchFields(selectedPatchTargets, { priority: parsedPriority });
    if (result) {
      setBatchPriorityOpen(false);
    }
  }, [batchPatchFields, batchPriorityValue, selectedPatchTargets, showNotification, t]);

  const handleBatchCodexWebsockets = useCallback(
    (websockets: boolean) => {
      void batchPatchFields(selectedWebsocketPatchTargets, { websockets });
    },
    [batchPatchFields, selectedWebsocketPatchTargets]
  );

  const handleCodexReauthSuccess = useCallback(async () => {
    const target = codexReauthTarget;
    await loadFiles();
    await loadCodexInspectionSnapshots();
    if (!target?.fileName) return;

    const targetKey = getAuthFileCodexInspectionKey(target.fileName, target.authIndex ?? null);
    setLastCodexInspectionResults((current) =>
      current.filter((item) => {
        const itemKey = getAuthFileCodexInspectionKey(item.fileName, item.authIndex ?? null);
        return itemKey !== targetKey || !isStaleCodexReauthSnapshot(item);
      })
    );
  }, [codexReauthTarget, loadCodexInspectionSnapshots, loadFiles]);

  const openExcludedEditor = useCallback(
    (provider?: string) => {
      const providerValue = (provider || (filter !== 'all' ? String(filter) : '')).trim();
      const params = new URLSearchParams();
      if (providerValue) {
        params.set('provider', providerValue);
      }
      const nextSearch = params.toString();
      navigate(`/auth-files/oauth-excluded${nextSearch ? `?${nextSearch}` : ''}`, {
        state: { fromAuthFiles: true },
      });
    },
    [filter, navigate]
  );

  const openModelAliasEditor = useCallback(
    (provider?: string) => {
      const providerValue = (provider || (filter !== 'all' ? String(filter) : '')).trim();
      const params = new URLSearchParams();
      if (providerValue) {
        params.set('provider', providerValue);
      }
      const nextSearch = params.toString();
      navigate(`/auth-files/oauth-model-alias${nextSearch ? `?${nextSearch}` : ''}`, {
        state: { fromAuthFiles: true },
      });
    },
    [filter, navigate]
  );

  useLayoutEffect(() => {
    if (typeof window === 'undefined') return;

    const actionsEl = floatingBatchActionsRef.current;
    if (!actionsEl) {
      document.documentElement.style.removeProperty('--auth-files-action-bar-height');
      return;
    }

    const updatePadding = () => {
      const height = actionsEl.getBoundingClientRect().height;
      document.documentElement.style.setProperty('--auth-files-action-bar-height', `${height}px`);
    };

    updatePadding();
    window.addEventListener('resize', updatePadding);

    const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(updatePadding);
    ro?.observe(actionsEl);

    return () => {
      ro?.disconnect();
      window.removeEventListener('resize', updatePadding);
      document.documentElement.style.removeProperty('--auth-files-action-bar-height');
    };
  }, [batchActionBarVisible, selectionCount]);

  useEffect(() => {
    selectionCountRef.current = selectionCount;
    if (selectionCount > 0) {
      setBatchActionBarVisible(true);
    }
  }, [selectionCount]);

  useLayoutEffect(() => {
    if (!batchActionBarVisible) return;
    const currentCount = selectionCount;
    const previousCount = previousSelectionCountRef.current;
    const actionsEl = floatingBatchActionsRef.current;
    if (!actionsEl) return;

    batchActionAnimationRef.current?.stop();
    batchActionAnimationRef.current = null;

    if (currentCount > 0 && previousCount === 0) {
      batchActionAnimationRef.current = animate(
        actionsEl,
        {
          transform: [BATCH_BAR_HIDDEN_TRANSFORM, BATCH_BAR_BASE_TRANSFORM],
          opacity: [0, 1],
        },
        {
          duration: 0.28,
          ease: easePower3Out,
          onComplete: () => {
            actionsEl.style.transform = BATCH_BAR_BASE_TRANSFORM;
            actionsEl.style.opacity = '1';
          },
        }
      );
    } else if (currentCount === 0 && previousCount > 0) {
      batchActionAnimationRef.current = animate(
        actionsEl,
        {
          transform: [BATCH_BAR_BASE_TRANSFORM, BATCH_BAR_HIDDEN_TRANSFORM],
          opacity: [1, 0],
        },
        {
          duration: 0.22,
          ease: easePower2In,
          onComplete: () => {
            if (selectionCountRef.current === 0) {
              setBatchActionBarVisible(false);
            }
          },
        }
      );
    }

    previousSelectionCountRef.current = currentCount;
  }, [batchActionBarVisible, selectionCount]);

  useEffect(
    () => () => {
      batchActionAnimationRef.current?.stop();
      batchActionAnimationRef.current = null;
    },
    []
  );

  const renderFilterTags = () => (
    <div className={styles.filterTags}>
      {existingTypes.map((type) => {
        const isActive = normalizedFilter === type;
        const iconSrc = getAuthFileIcon(type, resolvedTheme);
        const color =
          type === 'all'
            ? { bg: 'var(--color-primary-light-9)', text: 'var(--primary-color)' }
            : getTypeColor(type, resolvedTheme);
        const buttonStyle = {
          '--filter-color': color.text,
          '--filter-surface': color.bg,
          '--filter-active-text': resolvedTheme === 'dark' ? '#111827' : '#ffffff',
        } as CSSProperties;

        return (
          <button
            key={type}
            className={`${styles.filterTag} ${isActive ? styles.filterTagActive : ''}`}
            style={buttonStyle}
            onClick={() => {
              setFilter(type);
              setPage(1);
            }}
          >
            <span className={styles.filterTagLabel}>
              {type === 'all' ? (
                <span className={`${styles.filterTagIconWrap} ${styles.filterAllIconWrap}`}>
                  <IconFilterAll className={styles.filterAllIcon} size={16} />
                </span>
              ) : (
                <span className={styles.filterTagIconWrap}>
                  {iconSrc ? (
                    <img src={iconSrc} alt="" className={styles.filterTagIcon} />
                  ) : (
                    <span className={styles.filterTagIconFallback}>
                      {getTypeLabel(t, type).slice(0, 1).toUpperCase()}
                    </span>
                  )}
                </span>
              )}
              <span className={styles.filterTagText}>{getTypeLabel(t, type)}</span>
            </span>
            <span className={styles.filterTagCount}>{typeCounts[type] ?? 0}</span>
          </button>
        );
      })}
    </div>
  );

  const codexResultFilterActive = codexStatusFilter !== 'all' || codexPlanFilter !== 'all';
  const deleteAllButtonLabel = (() => {
    if (disabledOnly || healthyOnly || codexResultFilterActive) {
      return t('auth_files.delete_filtered_result_button');
    }
    if (problemOnly) {
      return normalizedFilter === 'all'
        ? t('auth_files.delete_problem_button')
        : t('auth_files.delete_problem_button_with_type', {
            type: getTypeLabel(t, normalizedFilter),
          });
    }
    return normalizedFilter === 'all'
      ? t('auth_files.delete_all_button')
      : `${t('common.delete')} ${getTypeLabel(t, normalizedFilter)}`;
  })();

  return (
    <div className={styles.container}>
      <section className={styles.authFilesShell}>
        {error && <div className={styles.errorBox}>{error}</div>}

        <div className={styles.filterSection}>
          <div className={styles.filterPanel}>
            <div className={styles.filterPanelHeader}>
              <div className={styles.filterPanelTags}>{renderFilterTags()}</div>
              <div className={styles.headerActions}>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={handleHeaderRefresh}
                  disabled={loading}
                >
                  {t('common.refresh')}
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => setAuthJsonPasteOpen(true)}
                  disabled={disableControls || authJsonPasteSaving}
                  loading={authJsonPasteSaving}
                >
                  {t('auth_files.paste_button')}
                </Button>
                <Button
                  size="sm"
                  onClick={handleUploadClick}
                  disabled={disableControls || uploading}
                  loading={uploading}
                >
                  {t('auth_files.upload_button')}
                </Button>
                <Button
                  variant="danger"
                  size="sm"
                  onClick={() =>
                    handleDeleteAll({
                      filter: normalizedFilter,
                      problemOnly,
                      disabledOnly,
                      healthyOnly,
                      filteredFiles: codexResultFilterActive ? filtered : undefined,
                      onResetFilterToAll: () => setFilter('all'),
                      onResetProblemOnly: () => setProblemOnly(false),
                      onResetDisabledOnly: () => setDisabledOnly(false),
                      onResetHealthyOnly: () => setHealthyOnly(false),
                      onResetResultFilters: () => {
                        setCodexStatusFilter('all');
                        setCodexPlanFilter('all');
                      },
                    })
                  }
                  disabled={disableControls || loading || deletingAll}
                  loading={deletingAll}
                >
                  {deleteAllButtonLabel}
                </Button>
                <input
                  ref={fileInputRef}
                  type="file"
                  accept=".json,application/json"
                  multiple
                  style={{ display: 'none' }}
                  onChange={handleFileChange}
                />
              </div>
            </div>
            <div className={styles.filterControlsPanel}>
              <div className={styles.filterControls}>
                <div className={styles.filterItem}>
                  <label>{t('auth_files.search_label')}</label>
                  <Input
                    value={search}
                    onChange={(e) => {
                      setSearch(e.target.value);
                      setPage(1);
                    }}
                    placeholder={t('auth_files.search_placeholder')}
                    rightElement={<IconSearch size={16} />}
                    aria-label={t('auth_files.search_label')}
                  />
                </div>
                <div className={styles.filterItem}>
                  <label>{t('auth_files.page_size_label')}</label>
                  <input
                    className={styles.pageSizeSelect}
                    type="number"
                    min={MIN_CARD_PAGE_SIZE}
                    max={MAX_CARD_PAGE_SIZE}
                    step={1}
                    value={pageSizeInput}
                    onChange={handlePageSizeChange}
                    onBlur={(e) => commitPageSizeInput(e.currentTarget.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        e.currentTarget.blur();
                      }
                    }}
                  />
                </div>
                <div className={styles.filterItem}>
                  <label>{t('auth_files.sort_label')}</label>
                  <Select
                    className={styles.sortSelect}
                    value={sortMode}
                    options={sortOptions}
                    onChange={handleSortModeChange}
                    ariaLabel={t('auth_files.sort_label')}
                    fullWidth
                  />
                </div>
                <div className={styles.filterItem}>
                  <label>{t('auth_files.codex_status_filter_label')}</label>
                  <Select
                    className={styles.sortSelect}
                    value={codexStatusFilter}
                    options={codexStatusFilterOptions}
                    onChange={(value) => {
                      const next = normalizeAuthFilesCodexStatusFilter(value);
                      if (!next || next === codexStatusFilter) return;
                      setCodexStatusFilter(next);
                      setPage(1);
                    }}
                    ariaLabel={t('auth_files.codex_status_filter_label')}
                    fullWidth
                  />
                </div>
                <div className={styles.filterItem}>
                  <label>{t('auth_files.codex_plan_filter_label')}</label>
                  <Select
                    className={styles.sortSelect}
                    value={codexPlanFilter}
                    options={codexPlanFilterOptions}
                    onChange={(value) => {
                      const next = normalizeAuthFilesCodexPlanFilter(value);
                      if (!next || next === codexPlanFilter) return;
                      setCodexPlanFilter(next);
                      setPage(1);
                    }}
                    ariaLabel={t('auth_files.codex_plan_filter_label')}
                    fullWidth
                  />
                </div>
                <div className={`${styles.filterItem} ${styles.filterToggleItem}`}>
                  <label>{t('auth_files.display_options_label')}</label>
                  <div className={styles.filterToggleGroup}>
                    <div className={styles.filterToggleCard}>
                      <ToggleSwitch
                        checked={problemOnly}
                        onChange={(value) => {
                          setProblemOnly(value);
                          if (value) setHealthyOnly(false);
                          setPage(1);
                        }}
                        ariaLabel={t('auth_files.problem_filter_only')}
                        label={
                          <span className={styles.filterToggleLabel}>
                            {t('auth_files.problem_filter_only')}
                          </span>
                        }
                      />
                    </div>
                    <div className={styles.filterToggleCard}>
                      <ToggleSwitch
                        checked={disabledOnly}
                        onChange={(value) => {
                          setDisabledOnly(value);
                          if (value) setHealthyOnly(false);
                          setPage(1);
                        }}
                        ariaLabel={t('auth_files.disabled_filter_only')}
                        label={
                          <span className={styles.filterToggleLabel}>
                            {t('auth_files.disabled_filter_only')}
                          </span>
                        }
                      />
                    </div>
                    <div className={styles.filterToggleCard}>
                      <ToggleSwitch
                        checked={healthyOnly}
                        onChange={(value) => {
                          setHealthyOnly(value);
                          if (value) {
                            setProblemOnly(false);
                            setDisabledOnly(false);
                          }
                          setPage(1);
                        }}
                        ariaLabel={t('auth_files.healthy_filter_only')}
                        label={
                          <span className={styles.filterToggleLabel}>
                            {t('auth_files.healthy_filter_only')}
                          </span>
                        }
                      />
                    </div>
                    <div className={styles.filterToggleCard}>
                      <ToggleSwitch
                        checked={compactMode}
                        onChange={(value) => setCompactMode(value)}
                        ariaLabel={t('auth_files.compact_mode_label')}
                        label={
                          <span className={styles.filterToggleLabel}>
                            {t('auth_files.compact_mode_label')}
                          </span>
                        }
                      />
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>

          <div className={styles.filterContent}>
            {loading ? (
              <div className={styles.hint}>{t('common.loading')}</div>
            ) : pageItems.length === 0 ? (
              <EmptyState
                title={t('auth_files.search_empty_title')}
                description={t('auth_files.search_empty_desc')}
              />
            ) : (
              <div
                className={`${styles.fileGrid} ${pageHasInlineQuotaCards ? styles.fileGridQuotaManaged : ''} ${compactMode ? styles.fileGridCompact : ''}`}
              >
                {pageItems.map((file) => {
                  const authFileKey = getAuthFileCodexInspectionKeyForFile(file);
                  const codexStatus = codexStatusByAuthFileKey.get(authFileKey);
                  return (
                    <AuthFileCard
                      key={authFileKey}
                      file={file}
                      compact={compactMode}
                      selected={selectedFiles.has(getAuthFileSelectionKey(file))}
                      resolvedTheme={resolvedTheme}
                      disableControls={disableControls}
                      deleting={deleting}
                      statusUpdating={statusUpdating}
                      statusBarCache={statusBarCache}
                      codexStatusBadges={codexStatus?.badges ?? []}
                      codexNeedsReauth={codexStatus?.needsReauth ?? false}
                      codexDisplayQuota={getDisplayCodexQuota(file)}
                      antigravitySubscription={antigravitySubscriptions[file.name]}
                      onRefreshAntigravitySubscription={refreshSubscription}
                      quotaCooldown={getQuotaCooldownForFile(file)}
                      onShowModels={showModels}
                      onReauth={(targetFile) =>
                        setCodexReauthTarget(createCodexReauthTargetFromAuthFile(targetFile))
                      }
                      onDownload={handleDownload}
                      onOpenPrefixProxyEditor={openPrefixProxyEditor}
                      onDelete={handleDelete}
                      onToggleStatus={handleStatusToggle}
                      onToggleSelect={() => toggleSelect(getAuthFileSelectionKey(file))}
                    />
                  );
                })}
              </div>
            )}

            {!loading && sorted.length > pageSize && (
              <div className={styles.pagination}>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => setPage(Math.max(1, currentPage - 1))}
                  disabled={currentPage <= 1}
                >
                  {t('auth_files.pagination_prev')}
                </Button>
                <div className={styles.pageInfo}>
                  {t('auth_files.pagination_info', {
                    current: currentPage,
                    total: totalPages,
                    count: sorted.length,
                  })}
                </div>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => setPage(Math.min(totalPages, currentPage + 1))}
                  disabled={currentPage >= totalPages}
                >
                  {t('auth_files.pagination_next')}
                </Button>
              </div>
            )}
          </div>
        </div>
      </section>

      <OAuthExcludedCard
        disableControls={disableControls}
        excludedError={excludedError}
        excluded={excluded}
        onAdd={() => openExcludedEditor()}
        onEdit={openExcludedEditor}
        onDelete={deleteExcluded}
      />

      <OAuthModelAliasCard
        disableControls={disableControls}
        viewMode={viewMode}
        onViewModeChange={setViewMode}
        onAdd={() => openModelAliasEditor()}
        onEditProvider={openModelAliasEditor}
        onDeleteProvider={deleteModelAlias}
        modelAliasError={modelAliasError}
        modelAlias={modelAlias}
        allProviderModels={allProviderModels}
        onUpdate={handleMappingUpdate}
        onDeleteLink={handleDeleteLink}
        onToggleFork={handleToggleFork}
        onRenameAlias={handleRenameAlias}
        onDeleteAlias={handleDeleteAlias}
      />

      <AuthFileModelsModal
        open={modelsModalOpen}
        fileName={modelsFileName}
        fileType={modelsFileType}
        loading={modelsLoading}
        error={modelsError}
        models={modelsList}
        excluded={excluded}
        onClose={closeModelsModal}
        onCopyText={copyTextWithNotification}
      />

      <AuthFilesPrefixProxyEditorModal
        disableControls={disableControls}
        editor={prefixProxyEditor}
        updatedText={prefixProxyUpdatedText}
        dirty={prefixProxyDirty}
        onClose={closePrefixProxyEditor}
        onCopyText={copyTextWithNotification}
        onSave={handlePrefixProxySave}
        onChange={handlePrefixProxyChange}
      />

      <AuthJsonPasteModal
        open={authJsonPasteOpen}
        saving={authJsonPasteSaving}
        disabled={disableControls}
        onClose={() => {
          if (!authJsonPasteSaving) setAuthJsonPasteOpen(false);
        }}
        onSave={handleSavePastedAuthJson}
      />

      <CodexReauthDialog
        open={Boolean(codexReauthTarget)}
        target={codexReauthTarget}
        onClose={() => setCodexReauthTarget(null)}
        onSuccess={handleCodexReauthSuccess}
      />

      <Modal
        open={batchPriorityOpen}
        onClose={() => {
          if (!batchFieldsUpdating) setBatchPriorityOpen(false);
        }}
        closeDisabled={batchFieldsUpdating}
        title={t('auth_files.batch_priority_title')}
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
              disabled={batchFieldsButtonsDisabled}
              loading={batchFieldsUpdating}
            >
              {t('common.confirm')}
            </Button>
          </div>
        }
      >
        <div className={styles.batchPriorityModal}>
          <Input
            label={t('auth_files.priority_label')}
            placeholder={t('auth_files.priority_placeholder')}
            hint={t('auth_files.priority_hint')}
            value={batchPriorityValue}
            onChange={(event) => setBatchPriorityValue(event.target.value)}
            disabled={disableControls || batchFieldsUpdating}
            inputMode="numeric"
            autoFocus
            onKeyDown={(event) => {
              if (event.key !== 'Enter' || batchFieldsButtonsDisabled) return;
              void handleBatchPrioritySave();
            }}
          />
        </div>
      </Modal>

      {batchActionBarVisible && typeof document !== 'undefined'
        ? createPortal(
            <div className={styles.batchActionContainer} ref={floatingBatchActionsRef}>
              <div className={styles.batchActionBar}>
                <div className={styles.batchActionLeft}>
                  <span className={styles.batchSelectionText}>
                    {t('auth_files.batch_selected', { count: selectionCount })}
                  </span>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => selectAllVisible(pageItems)}
                    disabled={selectablePageItems.length === 0}
                  >
                    {t('auth_files.batch_select_page')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => selectAllVisible(sorted)}
                    disabled={selectableFilteredItems.length === 0}
                  >
                    {t('auth_files.batch_select_filtered')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => invertVisibleSelection(pageItems)}
                    disabled={selectablePageItems.length === 0}
                  >
                    {t('auth_files.batch_invert_page')}
                  </Button>
                  <Button variant="ghost" size="sm" onClick={deselectAll}>
                    {t('auth_files.batch_deselect')}
                  </Button>
                </div>
                <div className={styles.batchActionRight}>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => void batchDownload(selectedFileNames)}
                    disabled={disableControls || selectedFileNames.length === 0}
                  >
                    {t('auth_files.batch_download')}
                  </Button>
                  <Button
                    size="sm"
                    onClick={() => void batchSetStatus(selectedFileNames, true)}
                    disabled={batchStatusButtonsDisabled}
                  >
                    {t('auth_files.batch_enable')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => void batchSetStatus(selectedFileNames, false)}
                    disabled={batchStatusButtonsDisabled}
                  >
                    {t('auth_files.batch_disable')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={handleOpenBatchPriority}
                    disabled={batchFieldsButtonsDisabled}
                    loading={batchFieldsUpdating}
                  >
                    {t('auth_files.batch_priority_button')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => handleBatchCodexWebsockets(true)}
                    disabled={batchCodexFieldsButtonsDisabled}
                    loading={batchFieldsUpdating}
                  >
                    {t('auth_files.batch_websockets_enable')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => handleBatchCodexWebsockets(false)}
                    disabled={batchCodexFieldsButtonsDisabled}
                    loading={batchFieldsUpdating}
                  >
                    {t('auth_files.batch_websockets_disable')}
                  </Button>
                  <Button
                    variant="danger"
                    size="sm"
                    onClick={() => batchDelete(selectedFileNames)}
                    disabled={batchDeleteButtonsDisabled}
                  >
                    {t('common.delete')}
                  </Button>
                </div>
              </div>
            </div>,
            document.body
          )
        : null}
    </div>
  );
}
