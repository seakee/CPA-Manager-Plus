import {
  Suspense,
  lazy,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { useTranslation } from 'react-i18next';
import { createPortal } from 'react-dom';
import type { ReactCodeMirrorRef } from '@uiw/react-codemirror';
import { parse as parseYaml, parseDocument } from 'yaml';
import { usePageTransitionLayer } from '@/components/common/PageTransitionLayer';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import {
  IconCheck,
  IconChevronDown,
  IconChevronUp,
  IconRefreshCw,
  IconSearch,
} from '@/components/ui/icons';
import { VisualConfigEditor } from '@/components/config/VisualConfigEditor';
import type { ApiKeyMutation } from '@/components/config/ApiKeysCardEditor';
import { DiffModal } from '@/components/config/DiffModal';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import { useVisualConfig } from '@/hooks/useVisualConfig';
import {
  useNotificationStore,
  useAuthStore,
  useThemeStore,
  useConfigStore,
  useUsageServiceStore,
} from '@/stores';
import { configFileApi } from '@/services/api/configFile';
import { apiKeysApi } from '@/services/api/apiKeys';
import {
  getUsageServiceErrorCode,
  isUsageServiceId,
  normalizeUsageServiceBase,
  usageServiceApi,
  type CPAUsageConfig,
  type ManagerConfig,
  type ManagerConfigResponse,
} from '@/services/api/usageService';
import { detectApiBaseFromLocation } from '@/utils/connection';
import { ManagerConfigPanel } from './components/ManagerConfigPanel';
import styles from './ConfigPage.module.scss';

type ConfigEditorTab = 'visual' | 'source' | 'manager';
export type ManagerBindingStatus = 'unknown' | 'unconfigured' | 'matched';

const MANAGER_COLLECTOR_DEFAULT = {
  enabled: true,
  collectorMode: 'auto',
  queue: 'usage',
  popSide: 'right',
  batchSize: 100,
  pollIntervalMs: 500,
  queryLimit: 50000,
  tlsSkipVerify: false,
};

const CONFIG_TAB_STORAGE_KEY = 'config-management:tab';

// eslint-disable-next-line react-refresh/only-export-components
export function resolveManagerRequestAuthKey({
  panelHostedByUsageService,
  managementKey,
}: {
  panelHostedByUsageService: boolean | null;
  managementKey: string;
}): string {
  if (panelHostedByUsageService === true) {
    return managementKey.trim();
  }
  return '';
}

// eslint-disable-next-line react-refresh/only-export-components
export function resolveManagerBindingStatus({
  panelHostedByUsageService,
}: {
  panelHostedByUsageService: boolean | null;
}): ManagerBindingStatus {
  if (panelHostedByUsageService === null) return 'unknown';
  if (panelHostedByUsageService === true) return 'matched';
  return 'unconfigured';
}

// eslint-disable-next-line react-refresh/only-export-components
export function resolveManagerSaveState({
  panelHostedByUsageService,
  managerDirty,
}: {
  panelHostedByUsageService: boolean | null;
  managerDirty: boolean;
}): {
  adminKeyLoadPending: boolean;
  adminKeyOnlyPending: boolean;
  hasPendingSave: boolean;
  canSave: boolean;
} {
  const hasPendingSave = panelHostedByUsageService === true && managerDirty;

  return {
    adminKeyLoadPending: false,
    adminKeyOnlyPending: false,
    hasPendingSave,
    canSave: hasPendingSave,
  };
}

// eslint-disable-next-line react-refresh/only-export-components
export function resolveManagerCPAConnection({
  panelHostedByUsageService,
  managerConfig,
  cpaBaseUrlInput,
  managementKeyInput = '',
}: {
  panelHostedByUsageService: boolean | null;
  managerConfig: ManagerConfig | null;
  cpaBaseUrlInput?: string;
  managementKeyInput?: string;
}): ManagerConfig['cpaConnection'] {
  const savedConnection = managerConfig?.cpaConnection;
  const nextCPABaseUrl =
    cpaBaseUrlInput === undefined ? savedConnection?.cpaBaseUrl || '' : cpaBaseUrlInput.trim();
  const nextManagementKey = managementKeyInput.trim();
  const managementKeyConfigured = Boolean(
    savedConnection?.managementKeyConfigured || savedConnection?.managementKey
  );
  const sanitizedConnection: ManagerConfig['cpaConnection'] = {
    cpaBaseUrl: nextCPABaseUrl,
    managementKeyConfigured,
  };

  if (panelHostedByUsageService === true) {
    return {
      ...sanitizedConnection,
      ...(nextManagementKey ? { managementKey: nextManagementKey } : {}),
    };
  }

  return sanitizedConnection;
}

function parseManagerPositiveIntegerInput(value: string): number | null {
  const trimmed = value.trim();
  if (!/^\d+$/.test(trimmed)) return null;
  const parsed = Number(trimmed);
  if (!Number.isFinite(parsed) || parsed <= 0) return null;
  return Math.floor(parsed);
}

function resolveManagerPositiveIntegerBaseline(
  value: number | undefined,
  fallback: number
): number {
  return Number.isFinite(value) && value && value > 0 ? Math.floor(value) : fallback;
}

function managerPositiveIntegerInputChanged(
  input: string,
  savedValue: number | undefined,
  fallback: number
): boolean {
  const parsed = parseManagerPositiveIntegerInput(input);
  if (parsed === null) return true;
  return parsed !== resolveManagerPositiveIntegerBaseline(savedValue, fallback);
}

// eslint-disable-next-line react-refresh/only-export-components
export function resolveManagerFormDirty({
  managerConfig,
  cpaBaseUrlInput,
  managementKeyInput = '',
  requestMonitoringEnabled,
  collectorMode,
  pollIntervalMs,
  batchSize,
  queryLimit,
}: {
  managerConfig: ManagerConfig | null;
  cpaBaseUrlInput: string;
  managementKeyInput?: string;
  requestMonitoringEnabled: boolean;
  collectorMode: string;
  pollIntervalMs: string;
  batchSize: string;
  queryLimit: string;
}): boolean {
  if (!managerConfig) return false;

  const savedConnection = managerConfig.cpaConnection;
  const savedCollector = managerConfig.collector ?? MANAGER_COLLECTOR_DEFAULT;
  const savedCPABase = normalizeUsageServiceBase(savedConnection?.cpaBaseUrl || '');
  const nextCPABase = normalizeUsageServiceBase(cpaBaseUrlInput || '');
  if (savedCPABase !== nextCPABase) return true;

  const nextManagementKey = managementKeyInput.trim();
  if (nextManagementKey) return true;

  if (requestMonitoringEnabled !== (savedCollector.enabled !== false)) return true;
  const savedCollectorMode =
    savedCollector.collectorMode || MANAGER_COLLECTOR_DEFAULT.collectorMode;
  if ((collectorMode || MANAGER_COLLECTOR_DEFAULT.collectorMode) !== savedCollectorMode) {
    return true;
  }

  return (
    managerPositiveIntegerInputChanged(
      pollIntervalMs,
      savedCollector.pollIntervalMs,
      MANAGER_COLLECTOR_DEFAULT.pollIntervalMs
    ) ||
    managerPositiveIntegerInputChanged(
      batchSize,
      savedCollector.batchSize,
      MANAGER_COLLECTOR_DEFAULT.batchSize
    ) ||
    managerPositiveIntegerInputChanged(
      queryLimit,
      savedCollector.queryLimit,
      MANAGER_COLLECTOR_DEFAULT.queryLimit
    )
  );
}

function isManagerAuthErrorCode(code: string): boolean {
  return code === 'invalid_admin_key' || code === 'invalid_management_key';
}

// API-key persistence is allowed alongside ordinary Visual drafts, but not alongside
// an unsaved Source draft or another operation that could write the same config state.
// eslint-disable-next-line react-refresh/only-export-components
export function resolveApiKeyOperationBlockReason({
  sourceDirty,
  saving,
  managerSaving,
  apiKeyMutationInFlight,
  diffModalOpen,
}: {
  sourceDirty: boolean;
  saving: boolean;
  managerSaving: boolean;
  apiKeyMutationInFlight: boolean;
  diffModalOpen: boolean;
}): 'source_config_dirty' | 'api_key_operation_busy' | null {
  if (saving || managerSaving || apiKeyMutationInFlight || diffModalOpen) {
    return 'api_key_operation_busy';
  }
  if (sourceDirty) return 'source_config_dirty';
  return null;
}

// CPA appends the replacement value when the old value is missing. Keep replace
// semantics explicit at the UI boundary so a stale edit cannot become a create.
export type ApiKeyReplacePreflightResult =
  | {
      ok: true;
      canonicalOldApiKey: string;
    }
  | {
      ok: false;
      reason: 'api_key_stale' | 'api_key_duplicate' | 'api_key_ambiguous';
    };

// eslint-disable-next-line react-refresh/only-export-components
export function resolveApiKeyReplacePreflight({
  currentKeys,
  oldApiKey,
  newApiKey,
}: {
  currentKeys: string[];
  oldApiKey: string;
  newApiKey: string;
}): ApiKeyReplacePreflightResult {
  const normalizedOldApiKey = oldApiKey.trim();
  const normalizedNewApiKey = newApiKey.trim();
  const matchingOldKeys = currentKeys.filter((key) => key.trim() === normalizedOldApiKey);
  if (matchingOldKeys.length === 0) {
    return { ok: false, reason: 'api_key_stale' };
  }
  if (matchingOldKeys.length > 1) {
    return { ok: false, reason: 'api_key_ambiguous' };
  }

  if (
    normalizedOldApiKey !== normalizedNewApiKey &&
    currentKeys.some((key) => key.trim() === normalizedNewApiKey)
  ) {
    return { ok: false, reason: 'api_key_duplicate' };
  }

  return { ok: true, canonicalOldApiKey: matchingOldKeys[0] };
}

// A stale source buffer must never be used as the payload for a config save.
// eslint-disable-next-line react-refresh/only-export-components
export function shouldBlockStaleSourceSave({
  activeTab,
  sourceSnapshotStale,
}: {
  activeTab: ConfigEditorTab;
  sourceSnapshotStale: boolean;
}): boolean {
  return activeTab === 'source' && sourceSnapshotStale;
}

const LazyConfigSourceEditor = lazy(() => import('@/components/config/ConfigSourceEditor'));

function readCommercialModeFromYaml(yamlContent: string): boolean {
  try {
    const parsed = parseYaml(yamlContent);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return false;
    return Boolean((parsed as Record<string, unknown>)['commercial-mode']);
  } catch {
    return false;
  }
}

function normalizeYamlForVisualDiff(yamlContent: string): string {
  try {
    const doc = parseDocument(yamlContent);
    return doc.toString({ indent: 2, lineWidth: 120, minContentWidth: 0 });
  } catch {
    return yamlContent;
  }
}

export function ConfigPage() {
  const { t } = useTranslation();
  const pageTransitionLayer = usePageTransitionLayer();
  const isCurrentLayer = pageTransitionLayer ? pageTransitionLayer.isCurrentLayer : true;
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const managementKey = useAuthStore((state) => state.managementKey);
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const setUsageServiceConfig = useUsageServiceStore((state) => state.setUsageServiceConfig);
  const isMobile = useMediaQuery('(max-width: 768px)');

  const {
    visualValues,
    visualDirty,
    visualParseError,
    visualValidationErrors,
    visualHasPayloadValidationErrors,
    loadVisualValuesFromYaml,
    applyVisualChangesToYaml,
    setVisualValues,
    commitApiKeysText,
  } = useVisualConfig();

  const [activeTab, setActiveTab] = useState<ConfigEditorTab>(() => {
    const saved = localStorage.getItem(CONFIG_TAB_STORAGE_KEY);
    if (saved === 'visual' || saved === 'source' || saved === 'manager') return saved;
    return 'visual';
  });

  const [content, setContent] = useState('');
  const [sourceConfigLoaded, setSourceConfigLoaded] = useState(false);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [apiKeyMutationInFlight, setApiKeyMutationInFlight] = useState(false);
  const [sourceSnapshotStale, setSourceSnapshotStale] = useState(false);
  const [error, setError] = useState('');
  const [dirty, setDirty] = useState(false);
  const [diffModalOpen, setDiffModalOpen] = useState(false);
  const [serverYaml, setServerYaml] = useState('');
  const [mergedYaml, setMergedYaml] = useState('');
  const [previewServerYaml, setPreviewServerYaml] = useState('');
  const [previewTab, setPreviewTab] = useState<ConfigEditorTab>('visual');
  const [managerConfig, setManagerConfig] = useState<ManagerConfig | null>(null);
  const [managerConfigSource, setManagerConfigSource] = useState('');
  const [managerCPAUsage, setManagerCPAUsage] = useState<CPAUsageConfig | null>(null);
  const [managerLoading, setManagerLoading] = useState(false);
  const [managerSaving, setManagerSaving] = useState(false);
  const [managerError, setManagerError] = useState('');
  const [managerRequestMonitoringEnabled, setManagerRequestMonitoringEnabled] = useState(true);
  const [managerCPABaseInput, setManagerCPABaseInput] = useState('');
  const [managerCPAManagementKeyInput, setManagerCPAManagementKeyInput] = useState('');
  const [managerCPAManagementKeyVisible, setManagerCPAManagementKeyVisible] = useState(false);
  const [panelHostedByUsageService, setPanelHostedByUsageService] = useState<boolean | null>(null);
  const [managerCollectorMode, setManagerCollectorMode] = useState(
    MANAGER_COLLECTOR_DEFAULT.collectorMode
  );
  const [managerPollIntervalMs, setManagerPollIntervalMs] = useState(
    String(MANAGER_COLLECTOR_DEFAULT.pollIntervalMs)
  );
  const [managerBatchSize, setManagerBatchSize] = useState(
    String(MANAGER_COLLECTOR_DEFAULT.batchSize)
  );
  const [managerQueryLimit, setManagerQueryLimit] = useState(
    String(MANAGER_COLLECTOR_DEFAULT.queryLimit)
  );

  // Search state
  const [searchQuery, setSearchQuery] = useState('');
  const [searchResults, setSearchResults] = useState<{ current: number; total: number }>({
    current: 0,
    total: 0,
  });
  const [lastSearchedQuery, setLastSearchedQuery] = useState('');
  const editorRef = useRef<ReactCodeMirrorRef | null>(null);
  const floatingActionsRef = useRef<HTMLDivElement>(null);
  const savingRef = useRef(false);
  const managerSavingRef = useRef(false);
  const apiKeyMutationInFlightRef = useRef(false);
  const sourceSnapshotStaleRef = useRef(false);

  const updateSourceSnapshotStale = useCallback((stale: boolean) => {
    sourceSnapshotStaleRef.current = stale;
    setSourceSnapshotStale(stale);
  }, []);

  const disableControls = connectionStatus !== 'connected';
  const showManagerTab = panelHostedByUsageService === true;
  const isManagerTab = activeTab === 'manager' && showManagerTab;
  const sourceDirty = dirty || visualDirty;
  const shouldRenderFloatingActions = isCurrentLayer;
  const hasVisualModeError = !!visualParseError;
  const hasVisualValidationErrors =
    activeTab === 'visual' &&
    (Object.values(visualValidationErrors).some(Boolean) || visualHasPayloadValidationErrors);
  const managerRetentionSeconds =
    managerCPAUsage?.redisUsageQueueRetentionSeconds ||
    Number(visualValues.redisUsageQueueRetentionSeconds) ||
    60;
  const detectedPanelBase = useMemo(() => detectApiBaseFromLocation(), []);
  const managerCollectorModeOptions = useMemo(
    () => [
      { value: 'auto', label: t('config_management.manager.collector_mode_auto') },
      { value: 'http', label: t('config_management.manager.collector_mode_http') },
      { value: 'resp', label: t('config_management.manager.collector_mode_resp') },
      { value: 'subscribe', label: t('config_management.manager.collector_mode_subscribe') },
    ],
    [t]
  );
  const getUsageServiceDisplayError = useCallback(
    (error: unknown, fallbackKey: string) => {
      const code = getUsageServiceErrorCode(error);
      if (code) {
        return t(`usage_service_errors.${code}`, {
          defaultValue: t('usage_service_errors.request_failed'),
        });
      }
      if (error instanceof Error && error.name !== 'UsageServiceApiError' && error.message) {
        return error.message;
      }
      return t(fallbackKey);
    },
    [t]
  );
  const managerConfigSourceLabel = useMemo(() => {
    switch (managerConfigSource) {
      case 'env':
        return t('config_management.manager.config_source_env');
      case 'db':
        return t('config_management.manager.config_source_db');
      default:
        return t('config_management.manager.config_source_none');
    }
  }, [managerConfigSource, t]);
  const managerBoundCPABase = normalizeUsageServiceBase(
    managerConfig?.cpaConnection?.cpaBaseUrl || ''
  );

  const loadConfig = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await configFileApi.fetchConfigYaml();
      setContent(data);
      setDirty(false);
      setDiffModalOpen(false);
      setServerYaml(data);
      setMergedYaml(data);
      setPreviewServerYaml(data);
      updateSourceSnapshotStale(false);
      setSourceConfigLoaded(true);
      loadVisualValuesFromYaml(data);
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : t('notification.refresh_failed');
      setError(message);
    } finally {
      setLoading(false);
    }
  }, [loadVisualValuesFromYaml, t, updateSourceSnapshotStale]);

  useEffect(() => {
    if (activeTab === 'manager') {
      setLoading(false);
      return;
    }
    if (sourceConfigLoaded) return;
    void loadConfig();
  }, [activeTab, loadConfig, sourceConfigLoaded]);

  useEffect(() => {
    let cancelled = false;
    const detectUsageServiceHost = async () => {
      try {
        const info = await usageServiceApi.getInfo(detectedPanelBase);
        if (!cancelled) {
          setPanelHostedByUsageService(isUsageServiceId(info.service));
        }
      } catch {
        if (!cancelled) {
          setPanelHostedByUsageService(false);
        }
      }
    };
    void detectUsageServiceHost();
    return () => {
      cancelled = true;
    };
  }, [detectedPanelBase]);

  useEffect(() => {
    if (panelHostedByUsageService !== false || activeTab !== 'manager') return;
    setActiveTab('visual');
    localStorage.setItem(CONFIG_TAB_STORAGE_KEY, 'visual');
  }, [activeTab, panelHostedByUsageService]);

  const resolveManagerServiceBase = useCallback(() => {
    if (panelHostedByUsageService) {
      return normalizeUsageServiceBase(detectedPanelBase);
    }
    return '';
  }, [detectedPanelBase, panelHostedByUsageService]);

  const managerServiceTarget = resolveManagerServiceBase();
  const managerDirty = useMemo(
    () =>
      resolveManagerFormDirty({
        managerConfig,
        cpaBaseUrlInput: managerCPABaseInput,
        managementKeyInput: managerCPAManagementKeyInput,
        requestMonitoringEnabled: managerRequestMonitoringEnabled,
        collectorMode: managerCollectorMode,
        pollIntervalMs: managerPollIntervalMs,
        batchSize: managerBatchSize,
        queryLimit: managerQueryLimit,
      }),
    [
      managerBatchSize,
      managerCPABaseInput,
      managerCPAManagementKeyInput,
      managerCollectorMode,
      managerConfig,
      managerPollIntervalMs,
      managerQueryLimit,
      managerRequestMonitoringEnabled,
    ]
  );
  const managerSaveState = resolveManagerSaveState({
    panelHostedByUsageService,
    managerDirty,
  });
  const managerHasPendingSave = managerSaveState.hasPendingSave;
  const managerCanSave = managerSaveState.canSave;
  const isDirty = isManagerTab ? managerHasPendingSave : sourceDirty;

  const beginApiKeyOperation = useCallback(() => {
    const blockReason = resolveApiKeyOperationBlockReason({
      sourceDirty: dirty,
      saving: savingRef.current || saving,
      managerSaving: managerSavingRef.current || managerSaving,
      apiKeyMutationInFlight: apiKeyMutationInFlightRef.current,
      diffModalOpen,
    });
    if (blockReason) {
      const error = new Error(
        t(
          blockReason === 'source_config_dirty'
            ? 'config_management.visual.api_keys.source_dirty_guard'
            : 'config_management.visual.api_keys.operation_busy'
        )
      ) as Error & { code?: string };
      error.code = blockReason;
      throw error;
    }

    apiKeyMutationInFlightRef.current = true;
    setApiKeyMutationInFlight(true);
  }, [diffModalOpen, dirty, managerSaving, saving, t]);

  const endApiKeyOperation = useCallback(() => {
    apiKeyMutationInFlightRef.current = false;
    setApiKeyMutationInFlight(false);
  }, []);

  const refreshCleanSourceSnapshot = useCallback(async () => {
    // A clean source buffer is a server snapshot, so refresh it after CPA updates.
    // Never rewrite a source draft that the user has already changed.
    if (dirty) {
      updateSourceSnapshotStale(true);
      return false;
    }
    try {
      const latestYaml = await configFileApi.fetchConfigYaml();
      if (dirty) {
        updateSourceSnapshotStale(true);
        return false;
      }
      setContent(latestYaml);
      setServerYaml(latestYaml);
      setMergedYaml(latestYaml);
      setPreviewServerYaml(latestYaml);
      updateSourceSnapshotStale(false);
      return true;
    } catch {
      // The canonical API-key list has already been obtained. Keep the successful
      // API-key mutation, but prevent a stale source buffer from being saved later.
      updateSourceSnapshotStale(true);
      return false;
    }
  }, [dirty, updateSourceSnapshotStale]);

  const persistApiKeyMutation = useCallback(
    async (mutation: ApiKeyMutation): Promise<string[]> => {
      if (!apiKeyMutationInFlightRef.current) {
        const error = new Error(t('config_management.visual.api_keys.operation_busy')) as Error & {
          code?: string;
        };
        error.code = 'api_key_operation_busy';
        throw error;
      }
      if (dirty) {
        const error = new Error(
          t('config_management.visual.api_keys.source_dirty_guard')
        ) as Error & { code?: string };
        error.code = 'source_config_dirty';
        throw error;
      }

      if (mutation.type === 'create') {
        const normalizedApiKey = mutation.apiKey.trim();
        const currentKeys = await apiKeysApi.list();
        if (currentKeys.some((key) => key.trim() === normalizedApiKey)) {
          commitApiKeysText(currentKeys.join('\n'));
          await refreshCleanSourceSnapshot();
          const error = new Error(
            t('config_management.visual.api_keys.error_duplicate')
          ) as Error & { code?: string };
          error.code = 'api_key_duplicate';
          throw error;
        }
        updateSourceSnapshotStale(true);
        try {
          await apiKeysApi.replace([...currentKeys, normalizedApiKey]);
        } catch (cause) {
          const error = new Error(
            t('config_management.visual.api_keys.mutation_outcome_unknown')
          ) as Error & { cause?: unknown; code?: string };
          error.code = 'api_key_mutation_outcome_unknown';
          error.cause = cause;
          throw error;
        }
      } else if (mutation.type === 'replace') {
        const normalizedOldApiKey = mutation.oldApiKey.trim();
        const normalizedNewApiKey = mutation.newApiKey.trim();
        const currentKeys = await apiKeysApi.list();
        const preflightError = resolveApiKeyReplacePreflight({
          currentKeys,
          oldApiKey: normalizedOldApiKey,
          newApiKey: normalizedNewApiKey,
        });
        if (!preflightError.ok) {
          commitApiKeysText(currentKeys.join('\n'));
          await refreshCleanSourceSnapshot();
          const messageKey =
            preflightError.reason === 'api_key_stale'
              ? 'config_management.visual.api_keys.stale_key_refreshed'
              : preflightError.reason === 'api_key_duplicate'
                ? 'config_management.visual.api_keys.error_duplicate'
                : 'config_management.visual.api_keys.state_refresh_failed';
          const error = new Error(t(messageKey)) as Error & { code?: string };
          error.code = preflightError.reason;
          throw error;
        }
        updateSourceSnapshotStale(true);
        try {
          await apiKeysApi.replaceValue(preflightError.canonicalOldApiKey, normalizedNewApiKey);
        } catch (cause) {
          const error = new Error(
            t('config_management.visual.api_keys.mutation_outcome_unknown')
          ) as Error & { cause?: unknown; code?: string };
          error.code = 'api_key_mutation_outcome_unknown';
          error.cause = cause;
          throw error;
        }
      } else {
        updateSourceSnapshotStale(true);
        try {
          await apiKeysApi.deleteValue(mutation.apiKey.trim());
        } catch (cause) {
          const error = new Error(
            t('config_management.visual.api_keys.mutation_outcome_unknown')
          ) as Error & { cause?: unknown; code?: string };
          error.code = 'api_key_mutation_outcome_unknown';
          error.cause = cause;
          throw error;
        }
      }

      let canonicalKeys: string[];
      try {
        canonicalKeys = await apiKeysApi.list();
      } catch (error) {
        const refreshError = new Error(
          t('config_management.visual.api_keys.state_refresh_failed')
        ) as Error & { cause?: unknown; code?: string };
        refreshError.code = 'api_key_state_refresh_failed';
        refreshError.cause = error;
        throw refreshError;
      }
      commitApiKeysText(canonicalKeys.join('\n'));
      await refreshCleanSourceSnapshot();
      return canonicalKeys;
    },
    [commitApiKeysText, dirty, refreshCleanSourceSnapshot, t, updateSourceSnapshotStale]
  );

  const refreshApiKeys = useCallback(async (): Promise<string[]> => {
    if (!apiKeyMutationInFlightRef.current) {
      const error = new Error(t('config_management.visual.api_keys.operation_busy')) as Error & {
        code?: string;
      };
      error.code = 'api_key_operation_busy';
      throw error;
    }
    if (dirty) {
      const error = new Error(
        t('config_management.visual.api_keys.source_dirty_guard')
      ) as Error & { code?: string };
      error.code = 'source_config_dirty';
      throw error;
    }
    const canonicalKeys = await apiKeysApi.list();
    commitApiKeysText(canonicalKeys.join('\n'));
    await refreshCleanSourceSnapshot();
    return canonicalKeys;
  }, [commitApiKeysText, dirty, refreshCleanSourceSnapshot, t]);

  const syncEmbeddedManagerBootstrap = useCallback(
    (serviceBase: string) => {
      if (panelHostedByUsageService !== true) return;
      const normalized = normalizeUsageServiceBase(serviceBase);
      if (!normalized) return;
      setUsageServiceConfig(
        { enabled: true, serviceBase: normalized },
        { panelBase: detectedPanelBase, panelHostMode: 'manager_embedded' }
      );
    },
    [detectedPanelBase, panelHostedByUsageService, setUsageServiceConfig]
  );

  const applyManagerConfigResponse = useCallback((response: ManagerConfigResponse) => {
    const receivedConnection = response.config.cpaConnection;
    const nextConfig: ManagerConfig = {
      ...response.config,
      cpaConnection: {
        cpaBaseUrl: receivedConnection?.cpaBaseUrl || '',
        managementKeyConfigured: Boolean(
          receivedConnection?.managementKeyConfigured || receivedConnection?.managementKey
        ),
      },
    };
    const collector = nextConfig.collector ?? MANAGER_COLLECTOR_DEFAULT;

    setManagerConfig(nextConfig);
    setManagerConfigSource(response.source || '');
    setManagerCPAUsage(response.cpaUsage ?? null);
    setManagerRequestMonitoringEnabled(collector.enabled !== false);
    setManagerCPABaseInput(nextConfig.cpaConnection?.cpaBaseUrl || '');
    setManagerCollectorMode(collector.collectorMode || MANAGER_COLLECTOR_DEFAULT.collectorMode);
    setManagerPollIntervalMs(
      String(collector.pollIntervalMs || MANAGER_COLLECTOR_DEFAULT.pollIntervalMs)
    );
    setManagerBatchSize(String(collector.batchSize || MANAGER_COLLECTOR_DEFAULT.batchSize));
    setManagerQueryLimit(String(collector.queryLimit || MANAGER_COLLECTOR_DEFAULT.queryLimit));
    setManagerCPAManagementKeyInput('');
    setManagerCPAManagementKeyVisible(false);
  }, []);

  const loadManagerConfig = useCallback(async () => {
    const serviceBase = resolveManagerServiceBase();
    const requestAuthKey = resolveManagerRequestAuthKey({
      panelHostedByUsageService,
      managementKey,
    });
    if (!serviceBase) {
      setManagerError('');
      setManagerConfig(null);
      setManagerCPAUsage(null);
      setManagerConfigSource('');
      setManagerCPABaseInput('');
      return;
    }
    if (!requestAuthKey) {
      setManagerError(t('config_management.manager.admin_key_required'));
      return;
    }
    setManagerLoading(true);
    setManagerError('');
    try {
      const response = await usageServiceApi.getManagerConfig(serviceBase, requestAuthKey);
      applyManagerConfigResponse(response);
      syncEmbeddedManagerBootstrap(serviceBase);
    } catch (error: unknown) {
      const code = getUsageServiceErrorCode(error);
      if (isManagerAuthErrorCode(code)) {
        setManagerError(t('config_management.manager.admin_key_required'));
      } else {
        setManagerError(
          getUsageServiceDisplayError(error, 'config_management.manager.load_failed')
        );
      }
    } finally {
      setManagerLoading(false);
    }
  }, [
    applyManagerConfigResponse,
    getUsageServiceDisplayError,
    managementKey,
    panelHostedByUsageService,
    resolveManagerServiceBase,
    syncEmbeddedManagerBootstrap,
    t,
  ]);

  const readManagerPositiveInteger = useCallback(
    (value: string, label: string) => {
      const trimmed = value.trim();
      if (!/^\d+$/.test(trimmed)) {
        throw new Error(
          t('config_management.manager.number_invalid', {
            label,
          })
        );
      }
      const parsed = Number(trimmed);
      if (!Number.isFinite(parsed) || parsed <= 0) {
        throw new Error(
          t('config_management.manager.number_invalid', {
            label,
          })
        );
      }
      return Math.floor(parsed);
    },
    [t]
  );

  useEffect(() => {
    if (activeTab !== 'visual' || !visualParseError) return;

    setActiveTab('source');
    localStorage.setItem(CONFIG_TAB_STORAGE_KEY, 'source');
    showNotification(
      t('config_management.visual_mode_unavailable_detail', { message: visualParseError }),
      'error'
    );
  }, [activeTab, showNotification, t, visualParseError]);

  useEffect(() => {
    if (activeTab !== 'manager') return;
    void loadManagerConfig();
  }, [activeTab, loadManagerConfig]);

  const handleConfirmSave = async () => {
    if (
      shouldBlockStaleSourceSave({
        activeTab,
        sourceSnapshotStale: sourceSnapshotStale || sourceSnapshotStaleRef.current,
      })
    ) {
      showNotification(t('notification.refresh_failed'), 'error');
      return;
    }
    if (savingRef.current || managerSavingRef.current || apiKeyMutationInFlightRef.current) {
      return;
    }
    savingRef.current = true;
    setSaving(true);
    try {
      const latestServerYaml = await configFileApi.fetchConfigYaml();
      if (latestServerYaml !== previewServerYaml) {
        const nextMergedYaml =
          previewTab === 'visual' ? applyVisualChangesToYaml(latestServerYaml) : mergedYaml;
        const nextServerYaml =
          previewTab === 'visual' ? normalizeYamlForVisualDiff(latestServerYaml) : latestServerYaml;

        setPreviewServerYaml(latestServerYaml);
        setServerYaml(nextServerYaml);
        setMergedYaml(nextMergedYaml);

        if (nextServerYaml === nextMergedYaml) {
          setDirty(false);
          setDiffModalOpen(false);
          setContent(latestServerYaml);
          loadVisualValuesFromYaml(latestServerYaml);
          showNotification(t('config_management.diff.no_changes'), 'info');
        }
        return;
      }

      const previousCommercialMode = readCommercialModeFromYaml(latestServerYaml);
      const nextCommercialMode = readCommercialModeFromYaml(mergedYaml);
      const commercialModeChanged = previousCommercialMode !== nextCommercialMode;

      await configFileApi.saveConfigYaml(mergedYaml);
      const latestContent = await configFileApi.fetchConfigYaml();
      setDirty(false);
      setDiffModalOpen(false);
      setContent(latestContent);
      setServerYaml(latestContent);
      setMergedYaml(latestContent);
      setPreviewServerYaml(latestContent);
      updateSourceSnapshotStale(false);
      loadVisualValuesFromYaml(latestContent);

      // Keep the global config store in sync so sidebar / other pages reflect YAML changes immediately.
      try {
        useConfigStore.getState().clearCache();
        await useConfigStore.getState().fetchConfig(undefined, true);
      } catch (refreshError: unknown) {
        const message =
          refreshError instanceof Error
            ? refreshError.message
            : typeof refreshError === 'string'
              ? refreshError
              : '';
        showNotification(
          `${t('notification.refresh_failed')}${message ? `: ${message}` : ''}`,
          'error'
        );
      }

      showNotification(t('config_management.save_success'), 'success');
      if (commercialModeChanged) {
        showNotification(t('notification.commercial_mode_restart_required'), 'warning');
      }
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : '';
      showNotification(`${t('notification.save_failed')}: ${message}`, 'error');
    } finally {
      savingRef.current = false;
      setSaving(false);
    }
  };

  const saveManagerConfigPayload = useCallback(
    async (serviceBase: string, nextConfig: ManagerConfig, requestAuthKey: string) => {
      const response = await usageServiceApi.saveManagerConfig(
        serviceBase,
        nextConfig,
        requestAuthKey
      );
      applyManagerConfigResponse(response);
      setUsageServiceConfig(
        {
          enabled: true,
          serviceBase,
        },
        {
          panelBase: detectedPanelBase,
          panelHostMode: 'manager_embedded',
        }
      );
      showNotification(t('config_management.manager.save_success'), 'success');
    },
    [applyManagerConfigResponse, detectedPanelBase, setUsageServiceConfig, showNotification, t]
  );

  const handleManagerSave = async () => {
    if (managerSavingRef.current || apiKeyMutationInFlightRef.current) return;
    if (disableControls) return;
    if (panelHostedByUsageService !== true) return;
    const serviceBase = resolveManagerServiceBase();
    if (!serviceBase) {
      showNotification(t('config_management.manager.service_base_required'), 'warning');
      return;
    }
    const requestAuthKey = resolveManagerRequestAuthKey({
      panelHostedByUsageService,
      managementKey,
    });
    if (!requestAuthKey) {
      showNotification(t('config_management.manager.admin_key_required'), 'warning');
      return;
    }
    try {
      const pollIntervalMs = managerRequestMonitoringEnabled
        ? readManagerPositiveInteger(
            managerPollIntervalMs,
            t('config_management.manager.poll_interval_ms')
          )
        : MANAGER_COLLECTOR_DEFAULT.pollIntervalMs;
      const batchSize = managerRequestMonitoringEnabled
        ? readManagerPositiveInteger(managerBatchSize, t('config_management.manager.batch_size'))
        : MANAGER_COLLECTOR_DEFAULT.batchSize;
      const queryLimit = managerRequestMonitoringEnabled
        ? readManagerPositiveInteger(managerQueryLimit, t('config_management.manager.query_limit'))
        : MANAGER_COLLECTOR_DEFAULT.queryLimit;
      if (managerRequestMonitoringEnabled && pollIntervalMs > managerRetentionSeconds * 1000) {
        showNotification(t('config_management.manager.poll_interval_retention_error'), 'error');
        return;
      }
      const cpaConnection = resolveManagerCPAConnection({
        panelHostedByUsageService,
        managerConfig,
        cpaBaseUrlInput: managerCPABaseInput,
        managementKeyInput: managerCPAManagementKeyInput,
      });
      const nextConfig: ManagerConfig = {
        ...(managerConfig ?? {
          cpaConnection,
          collector: MANAGER_COLLECTOR_DEFAULT,
          externalUsageService: {
            enabled: false,
            serviceBase: '',
          },
        }),
        cpaConnection,
        collector: {
          ...(managerConfig?.collector ?? MANAGER_COLLECTOR_DEFAULT),
          enabled: managerRequestMonitoringEnabled,
          collectorMode: managerCollectorMode,
          pollIntervalMs,
          batchSize,
          queryLimit,
        },
        externalUsageService: {
          enabled: false,
          serviceBase: '',
        },
      };
      const savedCPABase = normalizeUsageServiceBase(
        managerConfig?.cpaConnection?.cpaBaseUrl || ''
      );
      const nextCPABase = normalizeUsageServiceBase(cpaConnection.cpaBaseUrl || '');
      const cpaBaseChanged = savedCPABase !== nextCPABase;
      const managementKeyChanged = managerCPAManagementKeyInput.trim() !== '';
      const cpaConnectionChanged = cpaBaseChanged || managementKeyChanged;

      if (cpaBaseChanged && sourceDirty) {
        showNotification(t('config_management.manager.cpa_switch_unsaved_config'), 'warning');
        return;
      }

      const runSave = async (notifyOnError: boolean) => {
        if (managerSavingRef.current || apiKeyMutationInFlightRef.current) return;
        managerSavingRef.current = true;
        setManagerSaving(true);
        let requestStarted = false;
        try {
          requestStarted = true;
          await saveManagerConfigPayload(serviceBase, nextConfig, requestAuthKey);
          if (cpaBaseChanged) {
            window.location.reload();
          }
        } catch (error: unknown) {
          if (cpaBaseChanged && requestStarted) {
            if (notifyOnError) {
              const message = getUsageServiceDisplayError(
                error,
                'usage_service_errors.request_failed'
              );
              showNotification(
                `${t('notification.save_failed')}${message ? `: ${message}` : ''}`,
                'error'
              );
            }
            window.location.reload();
            return;
          }
          if (notifyOnError) {
            const message = getUsageServiceDisplayError(
              error,
              'usage_service_errors.request_failed'
            );
            showNotification(
              `${t('notification.save_failed')}${message ? `: ${message}` : ''}`,
              'error'
            );
          }
          throw error;
        } finally {
          managerSavingRef.current = false;
          setManagerSaving(false);
        }
      };

      if (cpaConnectionChanged) {
        showConfirmation({
          title: t('config_management.manager.cpa_connection_risk_title'),
          message: t('config_management.manager.cpa_connection_risk_message', {
            currentBase: savedCPABase || t('config_management.manager.not_bound'),
            nextBase: nextCPABase || t('config_management.manager.not_bound'),
          }),
          confirmText: t('config_management.manager.cpa_connection_risk_confirm'),
          cancelText: t('common.cancel'),
          variant: 'danger',
          onConfirm: () => runSave(true),
        });
        return;
      }

      await runSave(false);
    } catch (error: unknown) {
      setManagerSaving(false);
      const message = getUsageServiceDisplayError(error, 'usage_service_errors.request_failed');
      showNotification(`${t('notification.save_failed')}${message ? `: ${message}` : ''}`, 'error');
    }
  };

  const handleSave = async () => {
    if (isManagerTab) {
      await handleManagerSave();
      return;
    }

    if (savingRef.current || managerSavingRef.current || apiKeyMutationInFlightRef.current) {
      return;
    }

    if (
      shouldBlockStaleSourceSave({
        activeTab,
        sourceSnapshotStale: sourceSnapshotStale || sourceSnapshotStaleRef.current,
      })
    ) {
      showNotification(t('notification.refresh_failed'), 'error');
      return;
    }

    if (activeTab === 'visual' && visualParseError) {
      showNotification(t('config_management.visual_mode_save_blocked'), 'error');
      return;
    }

    savingRef.current = true;
    setSaving(true);
    try {
      const latestServerYaml = await configFileApi.fetchConfigYaml();
      const visualBaseYaml = dirty ? content : latestServerYaml;

      if (activeTab !== 'source') {
        const latestDocument = parseDocument(latestServerYaml);
        if (latestDocument.errors.length > 0) {
          showNotification(
            t('config_management.visual_mode_latest_yaml_invalid', {
              message:
                latestDocument.errors[0]?.message ??
                t('config_management.visual_mode_save_blocked'),
            }),
            'error'
          );
          return;
        }

        if (visualBaseYaml !== latestServerYaml) {
          const visualBaseDocument = parseDocument(visualBaseYaml);
          if (visualBaseDocument.errors.length > 0) {
            showNotification(
              t('config_management.visual_mode_latest_yaml_invalid', {
                message:
                  visualBaseDocument.errors[0]?.message ??
                  t('config_management.visual_mode_save_blocked'),
              }),
              'error'
            );
            return;
          }
        }
      }

      // In source mode, save exactly what the user edited. In visual mode, preserve
      // unsaved source edits as the visual patch base so backend-only fields survive.
      const nextMergedYaml =
        activeTab === 'source' ? content : applyVisualChangesToYaml(visualBaseYaml);

      // In visual mode, applyVisualChangesToYaml re-serializes YAML via parseDocument → toString,
      // which may reformat comments/whitespace. Normalize the server YAML through the same pipeline
      // so the diff only shows actual value changes, not cosmetic reformatting.
      let diffOriginal = latestServerYaml;
      if (activeTab !== 'source') {
        diffOriginal = normalizeYamlForVisualDiff(latestServerYaml);
      }

      if (diffOriginal === nextMergedYaml) {
        setDirty(false);
        setContent(latestServerYaml);
        setServerYaml(latestServerYaml);
        setMergedYaml(nextMergedYaml);
        setPreviewServerYaml(latestServerYaml);
        updateSourceSnapshotStale(false);
        loadVisualValuesFromYaml(latestServerYaml);
        showNotification(t('config_management.diff.no_changes'), 'info');
        return;
      }

      setServerYaml(diffOriginal);
      setMergedYaml(nextMergedYaml);
      setPreviewServerYaml(latestServerYaml);
      setPreviewTab(activeTab);
      setDiffModalOpen(true);
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : '';
      showNotification(`${t('notification.save_failed')}: ${message}`, 'error');
    } finally {
      savingRef.current = false;
      setSaving(false);
    }
  };

  const handleChange = useCallback((value: string) => {
    setContent(value);
    setDirty(true);
  }, []);

  const handleTabChange = useCallback(
    async (tab: ConfigEditorTab) => {
      if (tab === activeTab) return;
      if (apiKeyMutationInFlightRef.current || managerSavingRef.current) return;

      if (tab === 'manager') {
        setActiveTab(tab);
        localStorage.setItem(CONFIG_TAB_STORAGE_KEY, tab);
        return;
      }

      if (!sourceConfigLoaded) {
        setActiveTab(tab);
        localStorage.setItem(CONFIG_TAB_STORAGE_KEY, tab);
        return;
      }

      if (tab === 'source') {
        if (sourceSnapshotStaleRef.current) {
          try {
            const latestYaml = await configFileApi.fetchConfigYaml();
            if (dirty) {
              updateSourceSnapshotStale(true);
              showNotification(t('notification.refresh_failed'), 'error');
              return;
            }
            setContent(latestYaml);
            setServerYaml(latestYaml);
            setMergedYaml(latestYaml);
            setPreviewServerYaml(latestYaml);
            updateSourceSnapshotStale(false);

            if (visualDirty) {
              const nextContent = applyVisualChangesToYaml(latestYaml);
              if (nextContent !== latestYaml) {
                setContent(nextContent);
                setDirty(true);
              }
            }
          } catch {
            updateSourceSnapshotStale(true);
            showNotification(t('notification.refresh_failed'), 'error');
            return;
          }
        } else if (visualDirty) {
          // Only rewrite YAML when there are pending visual changes; otherwise preserve raw YAML + comments.
          const nextContent = applyVisualChangesToYaml(content);
          if (nextContent !== content) {
            setContent(nextContent);
            setDirty(true);
          }
        }
      } else {
        const result = loadVisualValuesFromYaml(content);
        if (!result.ok) {
          showNotification(
            t('config_management.visual_mode_unavailable_detail', { message: result.error }),
            'error'
          );
          return;
        }
      }

      setActiveTab(tab);
      localStorage.setItem(CONFIG_TAB_STORAGE_KEY, tab);
    },
    [
      activeTab,
      applyVisualChangesToYaml,
      content,
      dirty,
      loadVisualValuesFromYaml,
      showNotification,
      sourceConfigLoaded,
      t,
      updateSourceSnapshotStale,
      visualDirty,
    ]
  );

  // Search functionality
  const performSearch = useCallback((query: string, direction: 'next' | 'prev' = 'next') => {
    if (!query || !editorRef.current?.view) return;

    const view = editorRef.current.view;
    const doc = view.state.doc.toString();
    const matches: number[] = [];
    const lowerQuery = query.toLowerCase();
    const lowerDoc = doc.toLowerCase();

    let pos = 0;
    while (pos < lowerDoc.length) {
      const index = lowerDoc.indexOf(lowerQuery, pos);
      if (index === -1) break;
      matches.push(index);
      pos = index + 1;
    }

    if (matches.length === 0) {
      setSearchResults({ current: 0, total: 0 });
      return;
    }

    // Find current match based on cursor position
    const selection = view.state.selection.main;
    const cursorPos = direction === 'prev' ? selection.from : selection.to;
    let currentIndex = 0;

    if (direction === 'next') {
      // Find next match after cursor
      for (let i = 0; i < matches.length; i++) {
        if (matches[i] > cursorPos) {
          currentIndex = i;
          break;
        }
        // If no match after cursor, wrap to first
        if (i === matches.length - 1) {
          currentIndex = 0;
        }
      }
    } else {
      // Find previous match before cursor
      for (let i = matches.length - 1; i >= 0; i--) {
        if (matches[i] < cursorPos) {
          currentIndex = i;
          break;
        }
        // If no match before cursor, wrap to last
        if (i === 0) {
          currentIndex = matches.length - 1;
        }
      }
    }

    const matchPos = matches[currentIndex];
    setSearchResults({ current: currentIndex + 1, total: matches.length });

    // Scroll to and select the match
    view.dispatch({
      selection: { anchor: matchPos, head: matchPos + query.length },
      scrollIntoView: true,
    });
    view.focus();
  }, []);

  const handleSearchChange = useCallback((value: string) => {
    setSearchQuery(value);
    // Do not auto-search on each keystroke. Clear previous results when query changes.
    if (!value) {
      setSearchResults({ current: 0, total: 0 });
      setLastSearchedQuery('');
    } else {
      setSearchResults({ current: 0, total: 0 });
    }
  }, []);

  const executeSearch = useCallback(
    (direction: 'next' | 'prev' = 'next') => {
      if (!searchQuery) return;
      setLastSearchedQuery(searchQuery);
      performSearch(searchQuery, direction);
    },
    [searchQuery, performSearch]
  );

  const handleSearchKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        executeSearch(e.shiftKey ? 'prev' : 'next');
      }
    },
    [executeSearch]
  );

  const handlePrevMatch = useCallback(() => {
    if (!lastSearchedQuery) return;
    performSearch(lastSearchedQuery, 'prev');
  }, [lastSearchedQuery, performSearch]);

  const handleNextMatch = useCallback(() => {
    if (!lastSearchedQuery) return;
    performSearch(lastSearchedQuery, 'next');
  }, [lastSearchedQuery, performSearch]);

  // Keep bottom floating actions from covering page content by syncing its height to a CSS variable.
  useLayoutEffect(() => {
    if (typeof window === 'undefined' || !shouldRenderFloatingActions) return;

    const actionsEl = floatingActionsRef.current;
    if (!actionsEl) return;

    const updatePadding = () => {
      const height = actionsEl.getBoundingClientRect().height;
      document.documentElement.style.setProperty('--config-action-bar-height', `${height}px`);
    };

    updatePadding();
    window.addEventListener('resize', updatePadding);

    const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(updatePadding);
    ro?.observe(actionsEl);

    return () => {
      ro?.disconnect();
      window.removeEventListener('resize', updatePadding);
      document.documentElement.style.removeProperty('--config-action-bar-height');
    };
  }, [shouldRenderFloatingActions]);

  // Status text
  const getStatusText = () => {
    if (isManagerTab) {
      if (disableControls) return t('config_management.status_disconnected');
      if (managerLoading) return t('config_management.status_loading');
      if (managerError) return t('config_management.status_load_failed');
      if (managerSaving) return t('config_management.status_saving');
      if (managerDirty) return t('config_management.status_dirty');
      return t('config_management.status_loaded');
    }
    if (disableControls) return t('config_management.status_disconnected');
    if (loading) return t('config_management.status_loading');
    if (error) return t('config_management.status_load_failed');
    if (hasVisualModeError) return t('config_management.visual_mode_unavailable');
    if (hasVisualValidationErrors)
      return t('config_management.visual.validation.validation_blocked');
    if (saving) return t('config_management.status_saving');
    if (isDirty) return t('config_management.status_dirty');
    return t('config_management.status_loaded');
  };

  const getStatusClass = () => {
    if (isManagerTab) {
      if (managerError) return styles.error;
      if (managerDirty) return styles.modified;
      if (!managerLoading && !managerSaving) return styles.saved;
      return '';
    }
    if (error || hasVisualModeError || hasVisualValidationErrors) return styles.error;
    if (isDirty) return styles.modified;
    if (!loading && !saving) return styles.saved;
    return '';
  };

  const getFloatingStatusText = () => {
    if (isManagerTab) {
      if (!isMobile) return getStatusText();
      if (disableControls)
        return t('config_management.status_disconnected_short', { defaultValue: 'Disconnected' });
      if (managerLoading)
        return t('config_management.status_loading_short', { defaultValue: 'Loading' });
      if (managerError)
        return t('config_management.status_load_failed_short', { defaultValue: 'Failed' });
      if (managerSaving)
        return t('config_management.status_saving_short', { defaultValue: 'Saving' });
      if (managerDirty)
        return t('config_management.status_dirty_short', { defaultValue: 'Unsaved' });
      return t('config_management.status_loaded_short', { defaultValue: 'Loaded' });
    }
    if (!isMobile) return getStatusText();
    if (disableControls)
      return t('config_management.status_disconnected_short', { defaultValue: 'Disconnected' });
    if (loading) return t('config_management.status_loading_short', { defaultValue: 'Loading' });
    if (error) return t('config_management.status_load_failed_short', { defaultValue: 'Failed' });
    if (hasVisualModeError)
      return t('config_management.visual_mode_unavailable_short', { defaultValue: 'YAML issue' });
    if (hasVisualValidationErrors) return t('config_management.visual.validation_blocked_short');
    if (saving) return t('config_management.status_saving_short', { defaultValue: 'Saving' });
    if (isDirty) return t('config_management.status_dirty_short', { defaultValue: 'Unsaved' });
    return t('config_management.status_loaded_short', { defaultValue: 'Loaded' });
  };

  const handleReload = useCallback(() => {
    if (apiKeyMutationInFlightRef.current || savingRef.current || managerSavingRef.current) {
      return;
    }
    if (isManagerTab) {
      if (!managerDirty) {
        void loadManagerConfig();
        return;
      }
      showConfirmation({
        title: t('common.unsaved_changes_title'),
        message: t('config_management.reload_confirm_message'),
        confirmText: t('config_management.reload'),
        cancelText: t('common.cancel'),
        variant: 'danger',
        onConfirm: async () => {
          await loadManagerConfig();
        },
      });
      return;
    }

    if (!isDirty) {
      void loadConfig();
      return;
    }

    showConfirmation({
      title: t('common.unsaved_changes_title'),
      message: t('config_management.reload_confirm_message'),
      confirmText: t('config_management.reload'),
      cancelText: t('common.cancel'),
      variant: 'danger',
      onConfirm: async () => {
        await loadConfig();
      },
    });
  }, [isManagerTab, isDirty, loadConfig, loadManagerConfig, managerDirty, showConfirmation, t]);

  const floatingActions = (
    <div className={styles.floatingActionContainer} ref={floatingActionsRef}>
      <div className={styles.floatingActionList}>
        <div
          className={`${styles.floatingStatus} ${
            isMobile ? styles.floatingStatusCompact : ''
          } ${getStatusClass()}`}
        >
          {getFloatingStatusText()}
        </div>
        <button
          type="button"
          className={styles.floatingActionButton}
          onClick={handleReload}
          disabled={loading || saving || managerSaving || apiKeyMutationInFlight}
          title={t('config_management.reload')}
          aria-label={t('config_management.reload')}
        >
          <IconRefreshCw size={16} />
        </button>
        <button
          type="button"
          className={styles.floatingActionButton}
          onClick={handleSave}
          disabled={
            isManagerTab
              ? disableControls ||
                managerLoading ||
                managerSaving ||
                apiKeyMutationInFlight ||
                !managerCanSave
              : disableControls ||
                loading ||
                saving ||
                managerSaving ||
                apiKeyMutationInFlight ||
                !isDirty ||
                diffModalOpen ||
                hasVisualModeError ||
                hasVisualValidationErrors
          }
          title={t('config_management.save')}
          aria-label={t('config_management.save')}
        >
          <IconCheck size={16} />
          {isDirty && <span className={styles.dirtyDot} aria-hidden="true" />}
        </button>
      </div>
    </div>
  );

  const canConfigureRequestMonitoring =
    panelHostedByUsageService === true &&
    Boolean(
      managerServiceTarget &&
      managerCPABaseInput.trim() &&
      (managerCPAManagementKeyInput.trim() || managerConfig?.cpaConnection?.managementKeyConfigured)
    );
  const managerRuntimeModeLabel =
    panelHostedByUsageService === true
      ? t('config_management.manager.runtime_embedded')
      : t('config_management.manager.runtime_external');
  const configEditorTabs = useMemo<ReadonlyArray<SegmentedTabItem<ConfigEditorTab>>>(
    () => [
      {
        id: 'visual',
        label: t('config_management.tabs.visual'),
        disabled: saving || loading || managerSaving || apiKeyMutationInFlight,
      },
      {
        id: 'source',
        label: t('config_management.tabs.source'),
        disabled: saving || loading || managerSaving || apiKeyMutationInFlight,
      },
      ...(showManagerTab
        ? [
            {
              id: 'manager' as const,
              label: t('config_management.tabs.manager'),
              disabled: managerSaving || managerLoading || apiKeyMutationInFlight,
            },
          ]
        : []),
    ],
    [apiKeyMutationInFlight, loading, managerLoading, managerSaving, saving, showManagerTab, t]
  );

  return (
    <div className={styles.container}>
      <div className={styles.workspaceShell}>
        <div className={styles.pageMeta}>
          <SegmentedTabs
            items={configEditorTabs}
            activeTab={activeTab}
            onChange={handleTabChange}
            ariaLabel={t('config_management.title')}
          />
          <div className={`${styles.statusBadge} ${getStatusClass()}`}>{getStatusText()}</div>
        </div>

        <div className={styles.content}>
          {!isManagerTab && error && <div className="error-box">{error}</div>}
          {isManagerTab && managerError && <div className="error-box">{managerError}</div>}
          {!isManagerTab && !error && visualParseError && (
            <div className="error-box">
              {t('config_management.visual_mode_unavailable_detail', { message: visualParseError })}
            </div>
          )}

          {isManagerTab ? (
            <ManagerConfigPanel
              managerLoading={managerLoading}
              managerSaving={managerSaving}
              panelHostedByUsageService={panelHostedByUsageService}
              detectedPanelBase={detectedPanelBase}
              managerRuntimeModeLabel={managerRuntimeModeLabel}
              managerHasBoundCPAManagementKey={Boolean(
                managerConfig?.cpaConnection?.managementKeyConfigured
              )}
              managerCPABaseInput={managerCPABaseInput}
              managerCPAManagementKeyInput={managerCPAManagementKeyInput}
              managerCPAManagementKeyVisible={managerCPAManagementKeyVisible}
              managerBoundCPABase={managerBoundCPABase}
              disableControls={disableControls}
              canConfigureRequestMonitoring={canConfigureRequestMonitoring}
              managerRequestMonitoringEnabled={managerRequestMonitoringEnabled}
              managerCollectorMode={managerCollectorMode}
              managerCollectorModeOptions={managerCollectorModeOptions}
              managerPollIntervalMs={managerPollIntervalMs}
              managerBatchSize={managerBatchSize}
              managerQueryLimit={managerQueryLimit}
              managerRetentionSeconds={managerRetentionSeconds}
              managerConfigSourceLabel={managerConfigSourceLabel}
              managerUsageStatisticsEnabled={Boolean(managerCPAUsage?.usageStatisticsEnabled)}
              onRefresh={() => void loadManagerConfig()}
              onRequestMonitoringChange={(value) => {
                setManagerRequestMonitoringEnabled(value);
              }}
              onCPABaseInputChange={(value) => {
                setManagerCPABaseInput(value);
              }}
              onCPAManagementKeyInputChange={(value) => {
                setManagerCPAManagementKeyInput(value);
              }}
              onCPAManagementKeyClear={() => {
                setManagerCPAManagementKeyInput('');
                setManagerCPAManagementKeyVisible(false);
              }}
              onCPAManagementKeyVisibilityToggle={() => {
                setManagerCPAManagementKeyVisible((visible) => !visible);
              }}
              onCollectorModeChange={(value) => {
                setManagerCollectorMode(value);
              }}
              onPollIntervalMsChange={(value) => {
                setManagerPollIntervalMs(value);
              }}
              onBatchSizeChange={(value) => {
                setManagerBatchSize(value);
              }}
              onQueryLimitChange={(value) => {
                setManagerQueryLimit(value);
              }}
            />
          ) : activeTab === 'visual' ? (
            <VisualConfigEditor
              values={visualValues}
              validationErrors={visualValidationErrors}
              hasPayloadValidationErrors={visualHasPayloadValidationErrors}
              disabled={
                disableControls ||
                loading ||
                saving ||
                managerSaving ||
                diffModalOpen ||
                apiKeyMutationInFlight
              }
              onChange={setVisualValues}
              onPersistApiKeyMutation={persistApiKeyMutation}
              onRefreshApiKeys={refreshApiKeys}
              onApiKeyOperationStart={beginApiKeyOperation}
              onApiKeyOperationEnd={endApiKeyOperation}
            />
          ) : (
            <div className={styles.sourceWorkspace}>
              <div className={styles.sourceToolbar}>
                <div className={styles.searchInputWrapper}>
                  <Input
                    value={searchQuery}
                    onChange={(e) => handleSearchChange(e.target.value)}
                    onKeyDown={handleSearchKeyDown}
                    placeholder={t('config_management.search_placeholder')}
                    disabled={disableControls || loading}
                    className={styles.searchInput}
                    rightElement={
                      <div className={styles.searchRight}>
                        {searchQuery && lastSearchedQuery === searchQuery && (
                          <span className={styles.searchCount}>
                            {searchResults.total > 0
                              ? `${searchResults.current} / ${searchResults.total}`
                              : t('config_management.search_no_results')}
                          </span>
                        )}
                        <button
                          type="button"
                          className={styles.searchButton}
                          onClick={() => executeSearch('next')}
                          disabled={!searchQuery || disableControls || loading}
                          title={t('config_management.search_button')}
                        >
                          <IconSearch size={16} />
                        </button>
                      </div>
                    }
                  />
                </div>

                <div className={styles.searchActions}>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={handlePrevMatch}
                    disabled={
                      !searchQuery || lastSearchedQuery !== searchQuery || searchResults.total === 0
                    }
                    title={t('config_management.search_prev')}
                  >
                    <IconChevronUp size={16} />
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={handleNextMatch}
                    disabled={
                      !searchQuery || lastSearchedQuery !== searchQuery || searchResults.total === 0
                    }
                    title={t('config_management.search_next')}
                  >
                    <IconChevronDown size={16} />
                  </Button>
                </div>
              </div>

              <div className={styles.editorWrapper}>
                <Suspense fallback={null}>
                  <LazyConfigSourceEditor
                    editorRef={editorRef}
                    value={content}
                    onChange={handleChange}
                    theme={resolvedTheme}
                    editable={
                      !disableControls &&
                      !loading &&
                      !saving &&
                      !managerSaving &&
                      !apiKeyMutationInFlight &&
                      !diffModalOpen
                    }
                    placeholder={t('config_management.editor_placeholder')}
                  />
                </Suspense>
              </div>
            </div>
          )}
        </div>
      </div>

      {shouldRenderFloatingActions && typeof document !== 'undefined'
        ? createPortal(floatingActions, document.body)
        : null}
      <DiffModal
        open={diffModalOpen}
        original={serverYaml}
        modified={mergedYaml}
        onConfirm={handleConfirmSave}
        onCancel={() => setDiffModalOpen(false)}
        loading={saving}
      />
    </div>
  );
}
