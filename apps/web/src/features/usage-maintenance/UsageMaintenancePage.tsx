import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { Drawer } from '@/components/ui/Drawer';
import { DropdownMenu } from '@/components/ui/DropdownMenu';
import {
  IconMoreVertical,
  IconPlus,
  IconRefreshCw,
  IconArrowUpFromLine,
  IconDownload,
} from '@/components/ui/icons';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import {
  readUsageMaintenanceNavigation,
  writeUsageMaintenanceNavigation,
  type UsageMaintenanceNavigation,
} from './usageMaintenanceNavigation';
import { Button } from '@/components/ui/Button';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import {
  getUsageServiceErrorCode,
  usageServiceApi,
  type UsageArchiveList,
  type UsageArchivePreview,
  type UsageArchiveResumeStage,
  type UsageArchiveRunSummary,
  type UsageArchiveSegmentSummary,
  type UsageArchiveStatus,
  type UsageMaintenanceStatus,
} from '@/services/api/usageService';
import { useAuthStore, useNotificationStore } from '@/stores';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { formatDateTime, formatFileSize } from '@/utils/format';
import {
  archiveHistoryFilterStatus,
  recommendRetentionDays,
  resolveRawEventRange,
  resolveRetentionCutoff,
  type ArchiveHistoryFilter,
  type ArchiveHistorySource,
  type ArchiveRunAction,
  type RetentionSelection,
  type UsageMaintenanceView,
} from './usageMaintenanceModel';
import {
  UsageArchiveHistoryView,
  UsageArchiveRunView,
  UsageArchiveRunActions,
  UsageMaintenanceOverviewView,
} from './UsageMaintenanceArchiveViews';
import { UsageMaintenanceCreateView, type GuidedArchiveStage } from './UsageMaintenanceCreateView';
import { UsageMaintenanceDeleteConfirmation } from './UsageMaintenanceDeleteConfirmation';
import { UsageMaintenanceTransferView } from './UsageMaintenanceTransferView';
import {
  COMPACT_USAGE_COMMAND,
  UsageMaintenanceAdvancedView,
  UsageMaintenanceDiagnosticsView,
} from './UsageMaintenanceCapabilityViews';
import styles from './UsageMaintenancePage.module.scss';

const isUnsupportedError = (error: unknown) => {
  const candidate = error as { status?: number } | null;
  return candidate?.status === 404 || candidate?.status === 405;
};

const formatArchiveActionError = (
  error: unknown,
  t: (key: string, options?: Record<string, unknown>) => string
) => {
  const code = getUsageServiceErrorCode(error);
  if (code === 'usage_archive_cancel_unsafe') {
    return t('usage_maintenance.cancel_unsafe', {
      defaultValue:
        'This task has started raw deletion and cannot be abandoned. Continue or recover the deletion stage.',
    });
  }
  if (code === 'usage_archive_cancel_cleanup_failed') {
    return t('usage_maintenance.cancel_cleanup_failed', {
      defaultValue:
        'The task could not be abandoned because temporary-file cleanup failed. Retry the abandon action or inspect the server logs.',
    });
  }
  if (code === 'usage_archive_cancel_published') {
    return t('usage_maintenance.cancel_published', {
      defaultValue:
        'This published archive cannot be abandoned in its current state. Continue the existing archive workflow or leave the archive in place.',
    });
  }
  return error instanceof Error ? error.message : String(error);
};

const activeRefreshIntervalMs = 5_000;
const archiveProgressStatuses = new Set(['archiving', 'verifying', 'deleting']);
type OperationToken = {
  generation: number;
  controller: AbortController;
  serviceBase: string;
  managementKey?: string;
};

type ConfirmationToken = {
  generation: number;
  serviceBase: string;
  managementKey?: string;
};

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const hasString = (value: Record<string, unknown>, key: string) => typeof value[key] === 'string';

const hasNumber = (value: Record<string, unknown>, key: string) => {
  const candidate = value[key];
  return typeof candidate === 'number' && Number.isFinite(candidate);
};

const hasBoolean = (value: Record<string, unknown>, key: string) => typeof value[key] === 'boolean';

const hasOptionalNumber = (value: Record<string, unknown>, key: string) =>
  value[key] === undefined || hasNumber(value, key);

const hasOptionalString = (value: Record<string, unknown>, key: string) =>
  value[key] === undefined || hasString(value, key);

const isNumberRecord = (value: unknown) =>
  isRecord(value) &&
  Object.values(value).every(
    (candidate) => typeof candidate === 'number' && Number.isFinite(candidate)
  );

const isUsageArchivePreview = (value: unknown): value is UsageArchivePreview =>
  isRecord(value) &&
  hasNumber(value, 'cutoff_timestamp_ms') &&
  hasNumber(value, 'target_event_id') &&
  hasNumber(value, 'event_count') &&
  hasNumber(value, 'estimated_bytes') &&
  hasOptionalNumber(value, 'min_timestamp_ms') &&
  hasOptionalNumber(value, 'max_timestamp_ms');

const isUsageArchiveRunSummary = (value: unknown): value is UsageArchiveRunSummary => {
  if (!isRecord(value)) return false;
  return (
    hasString(value, 'id') &&
    hasString(value, 'mode') &&
    hasString(value, 'status') &&
    hasOptionalString(value, 'resume_status') &&
    hasOptionalString(value, 'requested_stage') &&
    hasNumber(value, 'cutoff_timestamp_ms') &&
    hasNumber(value, 'target_event_id') &&
    hasNumber(value, 'event_count') &&
    hasNumber(value, 'estimated_bytes') &&
    hasNumber(value, 'last_archived_event_id') &&
    hasNumber(value, 'archived_event_count') &&
    hasNumber(value, 'archived_uncompressed_bytes') &&
    hasNumber(value, 'archived_compressed_bytes') &&
    hasNumber(value, 'last_deleted_event_id') &&
    hasNumber(value, 'deleted_event_count') &&
    hasNumber(value, 'created_at_ms') &&
    hasNumber(value, 'updated_at_ms') &&
    hasOptionalNumber(value, 'started_at_ms') &&
    hasOptionalNumber(value, 'archived_at_ms') &&
    hasOptionalNumber(value, 'verified_at_ms') &&
    hasOptionalNumber(value, 'delete_started_at_ms') &&
    hasOptionalNumber(value, 'completed_at_ms') &&
    hasBoolean(value, 'has_error')
  );
};

const isUsageArchiveList = (value: unknown): value is UsageArchiveList =>
  isRecord(value) &&
  Array.isArray(value.runs) &&
  value.runs.every(isUsageArchiveRunSummary) &&
  hasOptionalNumber(value, 'total') &&
  (value.status_counts === undefined || isNumberRecord(value.status_counts)) &&
  hasOptionalString(value, 'next_cursor');

const isUsageArchiveSegmentSummary = (value: unknown): value is UsageArchiveSegmentSummary => {
  if (!isRecord(value)) return false;
  return (
    hasString(value, 'run_id') &&
    hasNumber(value, 'sequence') &&
    hasString(value, 'status') &&
    hasNumber(value, 'first_event_id') &&
    hasNumber(value, 'last_event_id') &&
    hasNumber(value, 'min_timestamp_ms') &&
    hasNumber(value, 'max_timestamp_ms') &&
    hasNumber(value, 'event_count') &&
    hasNumber(value, 'uncompressed_bytes') &&
    hasNumber(value, 'compressed_bytes') &&
    hasNumber(value, 'created_at_ms') &&
    hasOptionalNumber(value, 'verified_at_ms')
  );
};

const isUsageArchiveStatus = (value: unknown): value is UsageArchiveStatus =>
  isRecord(value) &&
  isUsageArchiveRunSummary(value.run) &&
  Array.isArray(value.segments) &&
  value.segments.every(isUsageArchiveSegmentSummary);

const isUsageMaintenanceLock = (value: unknown) =>
  isRecord(value) &&
  hasString(value, 'run_id') &&
  hasString(value, 'operation') &&
  hasNumber(value, 'acquired_at_ms') &&
  hasNumber(value, 'updated_at_ms');

const isUsageMaintenanceCoverage = (value: unknown) =>
  isRecord(value) &&
  hasString(value, 'status') &&
  hasNumber(value, 'watermark_event_id') &&
  hasNumber(value, 'target_event_id') &&
  hasBoolean(value, 'complete');

const isUsageMaintenanceStatus = (value: unknown): value is UsageMaintenanceStatus => {
  if (!isRecord(value)) return false;
  const migration = value.migration;
  const aggregate = value.hourly_aggregate;
  const readiness = value.readiness;
  const storage = value.storage;
  if (!isRecord(migration) || !isRecord(aggregate) || !isRecord(readiness) || !isRecord(storage)) {
    return false;
  }
  if (value.active_run !== undefined && !isUsageArchiveRunSummary(value.active_run)) return false;
  if (value.active_lock !== undefined && !isUsageMaintenanceLock(value.active_lock)) return false;
  if (
    value.migration_coverage !== undefined &&
    !isUsageMaintenanceCoverage(value.migration_coverage)
  ) {
    return false;
  }
  if (
    value.hourly_aggregate_coverage !== undefined &&
    !isUsageMaintenanceCoverage(value.hourly_aggregate_coverage)
  ) {
    return false;
  }
  return (
    hasNumber(value, 'raw_event_count') &&
    hasOptionalNumber(value, 'raw_min_timestamp_ms') &&
    hasOptionalNumber(value, 'raw_max_timestamp_ms') &&
    hasOptionalNumber(value, 'raw_archived_event_count') &&
    hasNumber(value, 'raw_deleted_event_count') &&
    hasString(migration, 'name') &&
    hasString(migration, 'status') &&
    hasNumber(migration, 'last_event_id') &&
    hasNumber(migration, 'target_event_id') &&
    hasNumber(migration, 'processed_rows') &&
    hasNumber(migration, 'changed_rows') &&
    hasNumber(migration, 'updated_at_ms') &&
    hasString(aggregate, 'name') &&
    hasString(aggregate, 'status') &&
    hasNumber(aggregate, 'schema_version') &&
    hasNumber(aggregate, 'coverage_event_id') &&
    hasNumber(aggregate, 'target_event_id') &&
    hasNumber(aggregate, 'updated_at_ms') &&
    hasBoolean(readiness, 'migration_ready') &&
    hasBoolean(readiness, 'hourly_aggregate_ready') &&
    hasBoolean(readiness, 'archive_delete_enabled') &&
    hasNumber(storage, 'page_size') &&
    hasNumber(storage, 'page_count') &&
    hasNumber(storage, 'freelist_count') &&
    hasNumber(storage, 'reclaimable_bytes') &&
    hasNumber(storage, 'database_bytes') &&
    hasNumber(storage, 'wal_bytes') &&
    hasNumber(storage, 'shm_bytes') &&
    hasNumber(storage, 'total_bytes') &&
    value.compact_requires_stopped_server === true
  );
};

const actionIsDestructive = (
  run: UsageArchiveRunSummary,
  action: 'resume' | 'verify' | 'delete' | 'cancel'
) =>
  action === 'delete' ||
  (action === 'resume' &&
    (run.status === 'deleting' || (run.status === 'failed' && run.resume_status === 'deleting')));

const actionRequiresMigrationReady = (
  run: UsageArchiveRunSummary,
  action: 'resume' | 'verify' | 'delete' | 'cancel'
) => {
  if (action !== 'resume') return false;
  const resumeStage = run.status === 'failed' ? run.resume_status : run.status;
  return resumeStage === 'previewed' || resumeStage === 'archiving';
};

const resumeExpectedStage = (run: UsageArchiveRunSummary): UsageArchiveResumeStage | null => {
  const resumeStage = run.status === 'failed' ? run.resume_status : run.status;
  if (resumeStage === 'previewed' || resumeStage === 'archiving') return 'archiving';
  if (resumeStage === 'verifying') return 'verifying';
  if (resumeStage === 'deleting') return 'deleting';
  return null;
};

const expectedActionStatuses = (
  run: UsageArchiveRunSummary,
  action: 'resume' | 'verify' | 'delete' | 'cancel'
): ReadonlySet<string> => {
  if (action === 'cancel') return new Set(['cancelled']);
  if (action === 'delete') return new Set(['completed']);
  if (action === 'verify') return new Set(['verified', 'deleting', 'completed']);
  const resumeStage = run.status === 'failed' ? run.resume_status : run.status;
  if (resumeStage === 'previewed' || resumeStage === 'archiving') {
    return new Set(['archived', 'verifying', 'verified', 'deleting', 'completed']);
  }
  if (resumeStage === 'verifying') return new Set(['verified', 'deleting', 'completed']);
  if (resumeStage === 'deleting') return new Set(['completed']);
  return new Set();
};

const findScrollContainer = (element: HTMLElement | null) => {
  let container = element?.parentElement ?? null;
  while (container) {
    if (
      /(auto|scroll)/.test(window.getComputedStyle(container).overflowY) &&
      container.scrollHeight > container.clientHeight
    )
      return container;
    container = container.parentElement;
  }
  return null;
};

type DrawerConfirmation = {
  title: string;
  message: ReactNode;
  confirmText: string;
  cancelText: string;
  variant: 'primary' | 'danger';
  width?: number;
  onConfirm: () => void;
};

export function UsageMaintenancePage() {
  const { t, i18n } = useTranslation();
  const availability = usePanelFeatureAvailability();
  const navigate = useNavigate();
  const navigateRef = useRef(navigate);
  const [navigation, setNavigation] = useState(readUsageMaintenanceNavigation);
  const [drawerConfirmation, setDrawerConfirmation] = useState<DrawerConfirmation | null>(null);
  const drawerConfirmationRef = useRef<DrawerConfirmation | null>(null);
  const setPendingConfirmation = useCallback((value: DrawerConfirmation | null) => {
    drawerConfirmationRef.current = value;
    setDrawerConfirmation(value);
  }, []);
  const [transferRefreshToken, setTransferRefreshToken] = useState(0);
  const [transferVisited, setTransferVisited] = useState(navigation.tab === 'transfer');
  const navigationRef = useRef(navigation);
  const pageRef = useRef<HTMLDivElement | null>(null);
  const panelRef = useRef<HTMLDivElement | null>(null);
  const drawerBodyRef = useRef<HTMLDivElement | null>(null);
  const [drawerVisible, setDrawerVisible] = useState(false);
  const setDrawerBodyRef = useCallback((node: HTMLDivElement | null) => {
    drawerBodyRef.current = node;
    setDrawerVisible(node !== null);
  }, []);
  const drawerScrollRef = useRef<{
    tab: UsageMaintenanceNavigation['tab'];
    window: number;
    container: HTMLElement | null;
    containerTop: number;
  } | null>(null);
  const scrollPositionsRef = useRef(new Map<string, { window: number; container: number }>());
  const view =
    navigation.panel === 'create'
      ? 'create'
      : navigation.tab === 'organize'
        ? 'history'
        : 'transfer';
  const historyFilter = navigation.filter;
  const historySource = navigation.source;
  const selectedRunId = navigation.runId;
  const retentionSelection = navigation.retention;
  const customCutoff = navigation.customCutoff;
  const referenceNowMS = navigation.referenceNowMS;
  useLayoutEffect(() => {
    navigateRef.current = navigate;
  }, [navigate]);

  const updateNavigation = useCallback(
    (patch: Partial<UsageMaintenanceNavigation>, replace = false) => {
      const next = { ...navigationRef.current, ...patch };
      navigationRef.current = next;
      setNavigation(next);
      navigateRef.current(
        { pathname: '/usage-maintenance', search: writeUsageMaintenanceNavigation(next) },
        { replace }
      );
    },
    []
  );
  const closeDrawer = useCallback(() => {
    setPendingConfirmation(null);
    updateNavigation({ panel: null, runId: null, sessionId: null });
  }, [setPendingConfirmation, updateNavigation]);

  useEffect(() => {
    if (navigation.tab === 'transfer') setTransferVisited(true);
  }, [navigation.tab]);

  useEffect(() => {
    if (typeof window === 'undefined') return;
    const syncFromHash = () => {
      const pathname = window.location.hash.split('?')[0];
      if (
        pathname !== '#/usage-maintenance' &&
        !(__DEMO_SITE__ && pathname === '#/demo/usage-maintenance')
      )
        return;
      const next = readUsageMaintenanceNavigation();
      if (
        next.filter !== navigationRef.current.filter ||
        next.source !== navigationRef.current.source
      ) {
        setHistoryCursor(undefined);
        setHistoryCursorStack([]);
        setHistoryList({ runs: [] });
        setHistoryError(null);
      }
      if (
        writeUsageMaintenanceNavigation(next) !==
        writeUsageMaintenanceNavigation(navigationRef.current)
      ) {
        setPendingConfirmation(null);
      }
      navigationRef.current = next;
      setNavigation(next);
    };
    window.addEventListener('hashchange', syncFromHash);
    return () => window.removeEventListener('hashchange', syncFromHash);
  }, [setPendingConfirmation]);

  const managementKey = useAuthStore((state) => state.managementKey);
  const { showConfirmation, showNotification } = useNotificationStore();
  const serviceBase = availability.managerServiceBase;
  const [maintenance, setMaintenance] = useState<UsageMaintenanceStatus | null>(null);
  const maintenanceLoaded = maintenance !== null;
  const [archiveList, setArchiveList] = useState<UsageArchiveList>({ runs: [] });
  const archives = archiveList.runs;
  const [historyCursor, setHistoryCursor] = useState<string | undefined>();
  const [historyCursorStack, setHistoryCursorStack] = useState<string[]>([]);
  const [historyList, setHistoryList] = useState<UsageArchiveList>({ runs: [] });
  const [historyLoading, setHistoryLoading] = useState(false);
  const [historyError, setHistoryError] = useState<string | null>(null);
  const showingRecentArchives =
    historyFilter === 'all' && historySource === 'all' && !historyCursor;
  const displayedHistory = showingRecentArchives ? archiveList : historyList;
  const [selectedArchive, setSelectedArchive] = useState<UsageArchiveStatus | null>(null);
  const [selectedArchiveLoading, setSelectedArchiveLoading] = useState(false);
  const [selectedArchiveRefreshToken, setSelectedArchiveRefreshToken] = useState(0);
  const [preview, setPreview] = useState<UsageArchivePreview | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [guidedArchiveStage, setGuidedArchiveStage] = useState<GuidedArchiveStage>('idle');
  const [guidedArchiveRunId, setGuidedArchiveRunId] = useState<string | null>(null);
  const [previewRefreshToken, setPreviewRefreshToken] = useState(0);
  const [loading, setLoading] = useState(true);
  const [working, setWorking] = useState(false);
  const [operationArchive, setOperationArchive] = useState<UsageArchiveStatus | null>(null);
  const activeOperationRunIdRef = useRef<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [unsupported, setUnsupported] = useState(false);
  const [postDeleteNoticeVisible, setPostDeleteNoticeVisible] = useState(false);
  const [postDeleteRefreshFailed, setPostDeleteRefreshFailed] = useState(false);
  const mountedRef = useRef(false);
  const loadControllerRef = useRef<AbortController | null>(null);
  const loadGenerationRef = useRef(0);
  const capabilityContextRef = useRef<{ serviceBase: string; managementKey?: string } | null>(null);
  const hasLoadedMaintenanceRef = useRef(false);
  const operationControllerRef = useRef<AbortController | null>(null);
  const operationGenerationRef = useRef(0);
  const previewControllerRef = useRef<AbortController | null>(null);
  const previewGenerationRef = useRef(0);
  const operationContextRef = useRef({ serviceBase, managementKey });
  const contextGenerationRef = useRef(0);
  const historyControllerRef = useRef<AbortController | null>(null);
  const historyGenerationRef = useRef(0);
  const selectedArchiveControllerRef = useRef<AbortController | null>(null);
  const selectedArchiveGenerationRef = useRef(0);

  const invalidatePreview = useCallback((resetState = false) => {
    previewGenerationRef.current += 1;
    previewControllerRef.current?.abort();
    previewControllerRef.current = null;
    if (!mountedRef.current) return;
    setPreviewLoading(false);
    if (resetState) {
      setPreview(null);
      setPreviewError(null);
    }
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      drawerConfirmationRef.current = null;
      loadGenerationRef.current += 1;
      loadControllerRef.current?.abort();
      loadControllerRef.current = null;
      operationGenerationRef.current += 1;
      operationControllerRef.current?.abort();
      operationControllerRef.current = null;
      previewGenerationRef.current += 1;
      previewControllerRef.current?.abort();
      previewControllerRef.current = null;
      historyGenerationRef.current += 1;
      historyControllerRef.current?.abort();
      historyControllerRef.current = null;
      selectedArchiveGenerationRef.current += 1;
      selectedArchiveControllerRef.current?.abort();
      selectedArchiveControllerRef.current = null;
      contextGenerationRef.current += 1;
    };
  }, []);

  const invalidateOperation = useCallback((resetWorking = false) => {
    operationGenerationRef.current += 1;
    operationControllerRef.current?.abort();
    operationControllerRef.current = null;
    activeOperationRunIdRef.current = null;
    if (resetWorking && mountedRef.current) setWorking(false);
  }, []);

  const beginWorking = useCallback(
    (capturedServiceBase: string, capturedManagementKey?: string): OperationToken | null => {
      if (!mountedRef.current || operationControllerRef.current) return null;
      const currentContext = operationContextRef.current;
      if (
        currentContext.serviceBase !== capturedServiceBase ||
        currentContext.managementKey !== capturedManagementKey
      ) {
        return null;
      }
      const controller = new AbortController();
      const generation = ++operationGenerationRef.current;
      operationControllerRef.current = controller;
      activeOperationRunIdRef.current = null;
      setOperationArchive(null);
      loadGenerationRef.current += 1;
      loadControllerRef.current?.abort();
      loadControllerRef.current = null;
      setWorking(true);
      return {
        generation,
        controller,
        serviceBase: capturedServiceBase,
        managementKey: capturedManagementKey,
      };
    },
    []
  );

  const publishOperationArchive = useCallback((archive: UsageArchiveStatus) => {
    activeOperationRunIdRef.current = archive.run.id;
    setOperationArchive(archive);
    if (
      navigationRef.current.runId === archive.run.id ||
      navigationRef.current.panel === 'create'
    ) {
      setSelectedArchive(archive);
    }
  }, []);

  const confirmationIsCurrent = useCallback((confirmation: ConfirmationToken) => {
    const currentContext = operationContextRef.current;
    return (
      mountedRef.current &&
      confirmation.generation === contextGenerationRef.current &&
      confirmation.serviceBase === currentContext.serviceBase &&
      confirmation.managementKey === currentContext.managementKey
    );
  }, []);

  const operationIsCurrent = useCallback((operation: OperationToken) => {
    const currentContext = operationContextRef.current;
    return (
      mountedRef.current &&
      !operation.controller.signal.aborted &&
      operation.generation === operationGenerationRef.current &&
      operation.controller === operationControllerRef.current &&
      operation.serviceBase === currentContext.serviceBase &&
      operation.managementKey === currentContext.managementKey
    );
  }, []);

  const finishOperation = useCallback(
    (operation: OperationToken) => {
      if (!operationIsCurrent(operation)) return;
      operationControllerRef.current = null;
      activeOperationRunIdRef.current = null;
      setWorking(false);
    },
    [operationIsCurrent]
  );

  useLayoutEffect(() => {
    const contextChanged =
      operationContextRef.current.serviceBase !== serviceBase ||
      operationContextRef.current.managementKey !== managementKey;
    contextGenerationRef.current += 1;
    operationContextRef.current = { serviceBase, managementKey };
    capabilityContextRef.current = null;
    invalidateOperation(true);
    invalidatePreview(false);
    if (contextChanged && hasLoadedMaintenanceRef.current) {
      const next = { ...readUsageMaintenanceNavigation(''), tab: navigationRef.current.tab };
      updateNavigation(next, true);
    }
    setPendingConfirmation(null);
    setMaintenance(null);
    setArchiveList({ runs: [] });
    setHistoryCursor(undefined);
    setHistoryCursorStack([]);
    setHistoryList({ runs: [] });
    setHistoryError(null);
    setHistoryLoading(false);
    setSelectedArchive(null);
    setOperationArchive(null);
    setSelectedArchiveLoading(false);
    setSelectedArchiveRefreshToken(0);
    setPreview(null);
    setPreviewLoading(false);
    setPreviewError(null);
    setGuidedArchiveStage('idle');
    setGuidedArchiveRunId(null);
    setPostDeleteNoticeVisible(false);
    setPostDeleteRefreshFailed(false);
    setError(null);
    setUnsupported(false);
    setLoading(Boolean(serviceBase));
    return () => {
      invalidateOperation(false);
      invalidatePreview(false);
    };
  }, [
    invalidateOperation,
    invalidatePreview,
    managementKey,
    serviceBase,
    setPendingConfirmation,
    updateNavigation,
  ]);

  const loadHistory = useCallback(
    async ({
      clearErrorOnSuccess = true,
    }: {
      clearErrorOnSuccess?: boolean;
    } = {}) => {
      if (
        !mountedRef.current ||
        !serviceBase ||
        navigationRef.current.tab !== 'organize' ||
        showingRecentArchives
      )
        return;
      const generation = ++historyGenerationRef.current;
      historyControllerRef.current?.abort();
      const controller = new AbortController();
      historyControllerRef.current = controller;
      setHistoryLoading(true);
      try {
        const result = await usageServiceApi.listUsageArchives(
          serviceBase,
          managementKey,
          {
            status: archiveHistoryFilterStatus(historyFilter),
            mode: historySource === 'all' ? undefined : historySource,
            limit: 20,
            cursor: historyCursor,
          },
          controller.signal
        );
        if (controller.signal.aborted || generation !== historyGenerationRef.current) return;
        if (!isUsageArchiveList(result)) {
          setHistoryError(
            t('usage_maintenance.archive_response_invalid', {
              defaultValue: 'The server returned an invalid archive task response.',
            })
          );
          return;
        }
        setHistoryList(result);
        if (clearErrorOnSuccess) {
          setHistoryError(null);
        }
      } catch (cause) {
        if (controller.signal.aborted || generation !== historyGenerationRef.current) return;
        setHistoryError(cause instanceof Error ? cause.message : String(cause));
      } finally {
        if (generation === historyGenerationRef.current) {
          historyControllerRef.current = null;
          setHistoryLoading(false);
        }
      }
    },
    [
      historyCursor,
      historyFilter,
      historySource,
      showingRecentArchives,
      managementKey,
      serviceBase,
      t,
    ]
  );

  useEffect(() => {
    if (navigation.tab !== 'organize' || !maintenanceLoaded || unsupported) return;
    void loadHistory();
    return () => {
      historyGenerationRef.current += 1;
      historyControllerRef.current?.abort();
      historyControllerRef.current = null;
    };
  }, [loadHistory, navigation.tab, maintenanceLoaded, unsupported]);

  useEffect(() => {
    if (
      !selectedRunId ||
      !serviceBase ||
      drawerConfirmation ||
      (operationControllerRef.current && activeOperationRunIdRef.current === selectedRunId)
    )
      return;
    const generation = ++selectedArchiveGenerationRef.current;
    selectedArchiveControllerRef.current?.abort();
    const controller = new AbortController();
    selectedArchiveControllerRef.current = controller;
    setSelectedArchiveLoading(true);
    setSelectedArchive((current) => (current?.run.id === selectedRunId ? current : null));
    const loadSelectedArchive = async () => {
      try {
        const result = await usageServiceApi.getUsageArchive(
          serviceBase,
          selectedRunId,
          managementKey,
          controller.signal
        );
        if (controller.signal.aborted || generation !== selectedArchiveGenerationRef.current)
          return;
        if (!isUsageArchiveStatus(result) || result.run.id !== selectedRunId) {
          setError(
            t('usage_maintenance.archive_response_invalid', {
              defaultValue: 'The server returned an invalid archive task response.',
            })
          );
          return;
        }
        setSelectedArchive(result);
        setError(null);
      } catch (cause) {
        if (controller.signal.aborted || generation !== selectedArchiveGenerationRef.current)
          return;
        setError(cause instanceof Error ? cause.message : String(cause));
      } finally {
        if (generation === selectedArchiveGenerationRef.current) {
          selectedArchiveControllerRef.current = null;
          setSelectedArchiveLoading(false);
        }
      }
    };
    void loadSelectedArchive();
    return () => {
      controller.abort();
      if (selectedArchiveControllerRef.current === controller) {
        selectedArchiveControllerRef.current = null;
      }
    };
  }, [
    managementKey,
    selectedArchiveRefreshToken,
    selectedRunId,
    serviceBase,
    t,
    drawerConfirmation,
  ]);

  const load = useCallback(
    async ({
      background = false,
    }: { background?: boolean } = {}): Promise<UsageMaintenanceStatus | null> => {
      if (!mountedRef.current) return null;
      if (!serviceBase) {
        if (!background) setLoading(false);
        return null;
      }

      const generation = ++loadGenerationRef.current;
      loadControllerRef.current?.abort();
      const controller = new AbortController();
      loadControllerRef.current = controller;
      if (!background) {
        setLoading(true);
        setError(null);
      }
      try {
        const capabilityContext = capabilityContextRef.current;
        if (
          capabilityContext?.serviceBase !== serviceBase ||
          capabilityContext.managementKey !== managementKey
        ) {
          await usageServiceApi.probeUsageMaintenance(
            serviceBase,
            managementKey,
            controller.signal
          );
          if (controller.signal.aborted || generation !== loadGenerationRef.current) return null;
          capabilityContextRef.current = { serviceBase, managementKey };
        }
        const [maintenanceResult, archiveResult] = await Promise.all([
          usageServiceApi.getUsageMaintenance(serviceBase, managementKey, controller.signal),
          usageServiceApi.listUsageArchives(serviceBase, managementKey, 20, controller.signal),
        ]);
        if (controller.signal.aborted || generation !== loadGenerationRef.current) return null;
        if (!isUsageMaintenanceStatus(maintenanceResult) || !isUsageArchiveList(archiveResult)) {
          setUnsupported(true);
          setMaintenance(null);
          setArchiveList({ runs: [] });
          setError(null);
          return null;
        }
        hasLoadedMaintenanceRef.current = true;
        setMaintenance(maintenanceResult);
        setPostDeleteRefreshFailed(false);
        setArchiveList(archiveResult);
        setUnsupported(false);
        setError(null);
        return maintenanceResult;
      } catch (cause) {
        if (generation !== loadGenerationRef.current || controller.signal.aborted) return null;
        controller.abort();
        if (isUnsupportedError(cause)) {
          setUnsupported(true);
          setMaintenance(null);
          setArchiveList({ runs: [] });
        } else {
          setUnsupported(false);
          setError(cause instanceof Error ? cause.message : String(cause));
        }
        return null;
      } finally {
        if (generation === loadGenerationRef.current) {
          loadControllerRef.current = null;
          if (!background) setLoading(false);
        }
      }
    },
    [managementKey, serviceBase]
  );

  useEffect(() => {
    setPreview(null);
    void load();
    return () => {
      loadGenerationRef.current += 1;
      loadControllerRef.current?.abort();
      loadControllerRef.current = null;
    };
  }, [load]);

  const handleUsageChanged = useCallback(() => {
    setPreviewRefreshToken((value) => value + 1);
    if (!operationControllerRef.current) void load({ background: true });
  }, [load]);

  const shouldPollMaintenance = Boolean(
    maintenance?.active_lock ||
    (maintenance?.active_run &&
      (maintenance.active_run.mode === 'retention' ||
        archiveProgressStatuses.has(maintenance.active_run.status)))
  );
  useEffect(() => {
    if (!shouldPollMaintenance || working || !serviceBase) return;
    const timer = setInterval(() => {
      if (!loadControllerRef.current) {
        void load({ background: true }).then(() => {
          if (mountedRef.current && selectedRunId) {
            setSelectedArchiveRefreshToken((value) => value + 1);
          }
        });
      }
    }, activeRefreshIntervalMs);
    return () => clearInterval(timer);
  }, [load, selectedRunId, serviceBase, shouldPollMaintenance, working]);

  const cutoffTimestamp = useMemo(
    () => resolveRetentionCutoff(retentionSelection, customCutoff, referenceNowMS),
    [customCutoff, referenceNowMS, retentionSelection]
  );
  const rawEventRange = useMemo(
    () => (maintenance ? resolveRawEventRange(maintenance) : null),
    [maintenance]
  );
  const recommendedRetentionDays = useMemo(
    () => (rawEventRange ? recommendRetentionDays(rawEventRange, referenceNowMS) : null),
    [rawEventRange, referenceNowMS]
  );

  useEffect(() => {
    if (
      !maintenanceLoaded ||
      !serviceBase ||
      unsupported ||
      view !== 'create' ||
      selectedRunId ||
      working
    )
      return;
    previewGenerationRef.current += 1;
    previewControllerRef.current?.abort();
    previewControllerRef.current = null;
    setPreview(null);
    setPreviewError(null);

    if (!cutoffTimestamp) {
      setPreviewLoading(false);
      setPreviewError(
        t('usage_maintenance.invalid_cutoff', {
          defaultValue: 'Choose a valid cutoff time that is not in the future.',
        })
      );
      return;
    }

    const controller = new AbortController();
    const generation = ++previewGenerationRef.current;
    previewControllerRef.current = controller;
    setPreviewLoading(true);

    const previewIsCurrent = () => {
      const currentContext = operationContextRef.current;
      return (
        mountedRef.current &&
        !controller.signal.aborted &&
        generation === previewGenerationRef.current &&
        controller === previewControllerRef.current &&
        currentContext.serviceBase === serviceBase &&
        currentContext.managementKey === managementKey
      );
    };
    const requestPreview = async () => {
      try {
        const result = await usageServiceApi.previewUsageArchive(
          serviceBase,
          cutoffTimestamp,
          managementKey,
          controller.signal
        );
        if (!previewIsCurrent()) return;
        if (!isUsageArchivePreview(result)) {
          setUnsupported(true);
          return;
        }
        setPreview(result);
      } catch (cause) {
        if (!previewIsCurrent()) return;
        if (isUnsupportedError(cause)) setUnsupported(true);
        else setPreviewError(cause instanceof Error ? cause.message : String(cause));
      } finally {
        if (previewIsCurrent()) {
          previewControllerRef.current = null;
          setPreviewLoading(false);
        }
      }
    };

    let timer: ReturnType<typeof setTimeout> | undefined;
    if (retentionSelection === 'custom') {
      timer = setTimeout(() => void requestPreview(), 250);
    } else {
      void requestPreview();
    }
    return () => {
      if (timer) clearTimeout(timer);
      controller.abort();
      if (controller === previewControllerRef.current) previewControllerRef.current = null;
    };
  }, [
    cutoffTimestamp,
    maintenanceLoaded,
    managementKey,
    previewRefreshToken,
    retentionSelection,
    serviceBase,
    t,
    unsupported,
    view,
    selectedRunId,
    working,
  ]);

  const selectRetention = (selection: RetentionSelection) => {
    if (working || selection === retentionSelection) return;
    invalidatePreview(true);
    setGuidedArchiveStage('idle');
    setGuidedArchiveRunId(null);
    updateNavigation({ retention: selection, referenceNowMS: Date.now() }, true);
  };

  const updateCustomCutoff = (value: string) => {
    if (working) return;
    invalidatePreview(true);
    setGuidedArchiveStage('idle');
    setGuidedArchiveRunId(null);
    updateNavigation({ customCutoff: value, referenceNowMS: Date.now() }, true);
  };

  const refreshMaintenance = () => {
    setTransferRefreshToken((value) => value + 1);
    void loadHistory();
    setPreviewRefreshToken((value) => value + 1);
    void load();
    if (selectedRunId) setSelectedArchiveRefreshToken((value) => value + 1);
  };

  const requireArchiveResponse = (
    value: unknown,
    expectedRunID?: string,
    expectedStatuses?: ReadonlySet<string>
  ) => {
    if (
      !isUsageArchiveStatus(value) ||
      (expectedRunID !== undefined && value.run.id !== expectedRunID) ||
      (expectedStatuses !== undefined && !expectedStatuses.has(value.run.status))
    ) {
      throw new Error(
        t('usage_maintenance.archive_response_invalid', {
          defaultValue: 'The server returned an invalid archive task response.',
        })
      );
    }
    return value;
  };

  const showConcurrentDeleteWarning = () =>
    showNotification(
      t('usage_maintenance.archive_concurrent_delete', {
        defaultValue:
          'This task advanced to raw-data deletion in another session. Its latest state is shown in archive history.',
      }),
      'warning'
    );

  const cancelGuidedArchive = () => {
    const guidedOperation = guidedArchiveStage !== 'idle' && guidedArchiveStage !== 'complete';
    if (!guidedOperation && !working) return;
    invalidateOperation(true);
    if (guidedOperation) {
      setGuidedArchiveStage('idle');
      setGuidedArchiveRunId(null);
    }
    showNotification(
      t('usage_maintenance.archive_prepare_cancelled', {
        defaultValue:
          'The request was stopped. If an archive task was created, it remains recoverable in history.',
      }),
      'warning'
    );
    setSelectedArchiveRefreshToken((value) => value + 1);
    void load({ background: true });
  };

  const createArchive = async (
    previewCutoffTimestamp: number,
    confirmation?: ConfirmationToken
  ) => {
    if (confirmation && !confirmationIsCurrent(confirmation)) return;
    invalidatePreview(false);
    const operation = beginWorking(serviceBase, managementKey);
    if (!operation) return;
    setGuidedArchiveStage('creating');
    setGuidedArchiveRunId(null);
    try {
      const createResponse = await usageServiceApi.createUsageArchive(
        serviceBase,
        previewCutoffTimestamp,
        managementKey,
        operation.controller.signal
      );
      if (!operationIsCurrent(operation)) return;
      const created = requireArchiveResponse(createResponse, undefined, new Set(['previewed']));
      const runID = created.run.id;
      publishOperationArchive(created);
      if (navigationRef.current.panel === 'create') {
        updateNavigation({ runId: runID, panel: 'run' }, true);
      }
      setGuidedArchiveRunId(runID);
      setGuidedArchiveStage('archiving');
      const archiveResponse = await usageServiceApi.resumeUsageArchive(
        serviceBase,
        runID,
        managementKey,
        operation.controller.signal,
        'archiving'
      );
      if (!operationIsCurrent(operation)) return;
      const archived = requireArchiveResponse(
        archiveResponse,
        runID,
        new Set(['archived', 'verifying', 'verified', 'deleting', 'completed'])
      );
      publishOperationArchive(archived);
      let finalStatus = archived.run.status;
      if (finalStatus === 'archived' || finalStatus === 'verifying') {
        setGuidedArchiveStage('verifying');
        const verifyResponse = await usageServiceApi.verifyUsageArchive(
          serviceBase,
          runID,
          managementKey,
          operation.controller.signal
        );
        if (!operationIsCurrent(operation)) return;
        const verified = requireArchiveResponse(
          verifyResponse,
          runID,
          new Set(['verified', 'deleting', 'completed'])
        );
        publishOperationArchive(verified);
        finalStatus = verified.run.status;
      }
      if (finalStatus === 'deleting' || finalStatus === 'completed') {
        setGuidedArchiveStage('idle');
        setGuidedArchiveRunId(null);
        setPreview(null);
        showConcurrentDeleteWarning();
        await load({ background: true });
        return;
      }
      setGuidedArchiveStage('complete');
      setPreview(null);
      if (navigationRef.current.panel !== 'run' || navigationRef.current.runId !== runID) {
        showNotification(
          t('usage_maintenance.archive_prepare_success', {
            defaultValue: 'Archive created and verified. Raw data was not deleted.',
          }),
          'success'
        );
      }
      await load({ background: true });
      if (operationIsCurrent(operation)) await loadHistory({ clearErrorOnSuccess: false });
    } catch (cause) {
      if (operationIsCurrent(operation)) {
        setGuidedArchiveStage('attention');
        showNotification(formatArchiveActionError(cause, t), 'error');
        await load({ background: true });
        if (operationIsCurrent(operation)) setSelectedArchiveRefreshToken((value) => value + 1);
      }
    } finally {
      finishOperation(operation);
    }
  };

  const confirmCreate = () => {
    if (!preview) return;
    const previewCutoffTimestamp = preview.cutoff_timestamp_ms;
    const confirmation = {
      generation: contextGenerationRef.current,
      serviceBase,
      managementKey,
    };
    setPendingConfirmation({
      title: t('usage_maintenance.archive_prepare_confirm_title', {
        defaultValue: 'Archive and verify this data?',
      }),
      message: (
        <div className={styles.confirmSummary}>
          <p>
            {t('usage_maintenance.archive_prepare_confirm_message', {
              defaultValue:
                'The archive will be prepared and verified. Online details remain available until you separately confirm cleanup.',
            })}
          </p>
          <dl>
            <div>
              <dt>
                {t('usage_maintenance.preview_events', { defaultValue: 'New events to archive' })}
              </dt>
              <dd>{preview.event_count.toLocaleString(i18n.language)}</dd>
            </div>
            <div>
              <dt>{t('usage_maintenance.cutoff', { defaultValue: 'Archive events before' })}</dt>
              <dd>{formatTime(previewCutoffTimestamp)}</dd>
            </div>
            <div>
              <dt>{t('usage_maintenance.preview_range', { defaultValue: 'Timestamp range' })}</dt>
              <dd>
                {formatTime(preview.min_timestamp_ms)} – {formatTime(preview.max_timestamp_ms)}
              </dd>
            </div>
            <div>
              <dt>
                {t('usage_maintenance.preview_source_bytes', {
                  defaultValue: 'Estimated source size',
                })}
              </dt>
              <dd>{formatFileSize(preview.estimated_bytes)}</dd>
            </div>
          </dl>
          <p>
            {t('usage_maintenance.workspace_recalculate', {
              defaultValue:
                'The server recalculates eligible data when the operation starts. This estimate does not include existing archives or promise a disk-space saving.',
            })}
          </p>
        </div>
      ),
      confirmText: t('usage_maintenance.archive_prepare_confirm_button', {
        defaultValue: 'Archive and verify',
      }),
      cancelText: t('common.cancel'),
      variant: 'primary',
      onConfirm: () => {
        void createArchive(previewCutoffTimestamp, confirmation);
      },
    });
  };

  const runAction = async (
    run: UsageArchiveRunSummary,
    action: 'resume' | 'verify' | 'delete' | 'cancel',
    confirmation?: ConfirmationToken
  ) => {
    if (confirmation && !confirmationIsCurrent(confirmation)) return;
    invalidatePreview(false);
    const expectedResumeStage = action === 'resume' ? resumeExpectedStage(run) : null;
    if (action === 'resume' && !expectedResumeStage) {
      showNotification(
        t('usage_maintenance.archive_response_invalid', {
          defaultValue: 'The server returned an invalid archive task response.',
        }),
        'error'
      );
      return;
    }
    const destructive = actionIsDestructive(run, action);
    if (destructive) {
      setPostDeleteNoticeVisible(false);
    }
    const operation = beginWorking(serviceBase, managementKey);
    if (!operation) return;
    publishOperationArchive({
      run,
      segments: selectedArchive?.run.id === run.id ? selectedArchive.segments : [],
    });
    try {
      const response =
        action === 'resume'
          ? await usageServiceApi.resumeUsageArchive(
              serviceBase,
              run.id,
              managementKey,
              operation.controller.signal,
              expectedResumeStage ?? undefined
            )
          : action === 'verify'
            ? await usageServiceApi.verifyUsageArchive(
                serviceBase,
                run.id,
                managementKey,
                operation.controller.signal
              )
            : action === 'delete'
              ? await usageServiceApi.deleteUsageArchive(
                  serviceBase,
                  run.id,
                  managementKey,
                  operation.controller.signal
                )
              : await usageServiceApi.cancelUsageArchive(
                  serviceBase,
                  run.id,
                  managementKey,
                  operation.controller.signal
                );
      if (!operationIsCurrent(operation)) return;
      const updated = requireArchiveResponse(response, run.id, expectedActionStatuses(run, action));
      publishOperationArchive(updated);
      if (
        guidedArchiveRunId === run.id ||
        (guidedArchiveStage === 'attention' && guidedArchiveRunId === null)
      ) {
        setGuidedArchiveStage('idle');
        setGuidedArchiveRunId(null);
      }
      if (
        !destructive &&
        (updated.run.status === 'deleting' || updated.run.status === 'completed')
      ) {
        showConcurrentDeleteWarning();
      } else {
        showNotification(
          t(`usage_maintenance.${destructive ? 'delete' : action}_success`, {
            defaultValue: destructive
              ? 'Logical deletion completed.'
              : action === 'cancel'
                ? 'Archive task abandoned; raw usage data was not deleted.'
                : 'Archive run updated.',
          }),
          'success'
        );
      }
      const destructiveCompleted = destructive && updated.run.status === 'completed';
      if (destructiveCompleted) {
        setPostDeleteNoticeVisible(true);
        setPostDeleteRefreshFailed(true);
      }
      const refreshedMaintenance = await load({ background: true });
      if (!operationIsCurrent(operation)) return;
      if (destructiveCompleted) {
        setPostDeleteRefreshFailed(refreshedMaintenance === null);
        // The mutation already succeeded. Keep its authoritative result even if a read fails.
        if (navigationRef.current.runId === run.id) setSelectedArchive(updated);
      }
      if (navigationRef.current.tab === 'organize')
        await loadHistory({ clearErrorOnSuccess: refreshedMaintenance !== null });
      if (operationIsCurrent(operation)) {
        if (destructiveCompleted) {
          // A completed deletion is authoritative even when a subsequent list read is stale.
          setArchiveList((list) => ({
            ...list,
            runs: list.runs.map((item) => (item.id === run.id ? updated.run : item)),
          }));
          setHistoryList((list) => ({
            ...list,
            runs: list.runs.map((item) => (item.id === run.id ? updated.run : item)),
          }));
        }
        setPreviewRefreshToken((value) => value + 1);
        if (!destructiveCompleted && navigationRef.current.runId === run.id) {
          setSelectedArchiveRefreshToken((value) => value + 1);
        }
      }
    } catch (cause) {
      if (operationIsCurrent(operation)) {
        showNotification(formatArchiveActionError(cause, t), 'error');
        await load({ background: true });
        if (operationIsCurrent(operation)) {
          setPreviewRefreshToken((value) => value + 1);
          if (navigationRef.current.runId === run.id) {
            setSelectedArchiveRefreshToken((value) => value + 1);
          }
        }
      }
    } finally {
      finishOperation(operation);
    }
  };

  const formatTime = (value?: number) =>
    value ? formatDateTime(new Date(value), i18n.language) : '-';
  const confirmAction = (
    run: UsageArchiveRunSummary,
    action: 'resume' | 'verify' | 'delete' | 'cancel'
  ) => {
    if (action === 'cancel') {
      const confirmation = {
        generation: contextGenerationRef.current,
        serviceBase,
        managementKey,
      };
      showConfirmation({
        title: t('usage_maintenance.cancel_confirm_title', {
          defaultValue: 'Abandon this archive task?',
        }),
        message: t('usage_maintenance.cancel_confirm_message', {
          defaultValue:
            'This will release the maintenance task without deleting raw usage data. Any published archive files remain available.',
        }),
        confirmText: t('usage_maintenance.cancel_confirm_button', {
          defaultValue: 'Abandon task',
        }),
        cancelText: t('common.cancel'),
        variant: 'primary',
        onConfirm: () => {
          void runAction(run, action, confirmation);
        },
      });
      return;
    }
    if (!actionIsDestructive(run, action)) {
      openRun(run);
      void runAction(run, action);
      return;
    }
    const confirmation = {
      generation: contextGenerationRef.current,
      serviceBase,
      managementKey,
    };
    openRun(run);
    setPendingConfirmation({
      title: t('usage_maintenance.delete_confirm_title', {
        defaultValue: 'Delete online raw data?',
      }),
      width: 720,
      message: (
        <UsageMaintenanceDeleteConfirmation
          run={run}
          deletionEnabled={maintenance?.readiness.archive_delete_enabled === true}
        />
      ),
      confirmText: t('usage_maintenance.delete_confirm_button', {
        defaultValue: 'Delete {{count}} raw rows',
        count: Math.max(0, run.event_count - run.deleted_event_count).toLocaleString(i18n.language),
      }),
      cancelText: t('common.cancel'),
      variant: 'danger',
      onConfirm: () => {
        void runAction(run, action, confirmation);
      },
    });
  };

  const actionLabel = (run: UsageArchiveRunSummary, action: ArchiveRunAction | null) => {
    if (!action) return '';
    if (action === 'resume') {
      const resumeStage = run.status === 'failed' ? run.resume_status : run.status;
      if (resumeStage === 'verifying') {
        return t('usage_maintenance.action_resume_verify', {
          defaultValue: 'Continue verification',
        });
      }
      if (resumeStage === 'deleting') {
        return t('usage_maintenance.action_resume_delete', {
          defaultValue: 'Continue deletion',
        });
      }
    }
    if (action === 'cancel') {
      return t('usage_maintenance.action_cancel', { defaultValue: 'Abandon task' });
    }
    const fallback =
      action === 'resume'
        ? 'Continue archive'
        : action === 'verify'
          ? 'Verify archive'
          : 'Delete raw data';
    return t(`usage_maintenance.action_${action}`, { defaultValue: fallback });
  };
  const deleteDisabled = maintenance?.readiness.archive_delete_enabled === false;
  const deleteReadinessHint = deleteDisabled
    ? t('usage_maintenance.delete_disabled', {
        defaultValue: 'Raw deletion is disabled until the server enables archive deletion.',
      })
    : maintenance &&
        (!maintenance.readiness.migration_ready || !maintenance.readiness.hourly_aggregate_ready)
      ? t('usage_maintenance.delete_readiness_pending', {
          defaultValue:
            'Global catch-up is still pending. The server will verify this run’s exact coverage before deletion.',
        })
      : '';
  const resolvedCutoffTimestamp = preview?.cutoff_timestamp_ms ?? cutoffTimestamp ?? undefined;
  const createBlockedByMaintenance = Boolean(maintenance?.active_run || maintenance?.active_lock);
  const archiveReadinessPending = maintenance?.readiness.migration_ready === false;
  const archiveReadinessHint = archiveReadinessPending
    ? t('usage_maintenance.archive_readiness_pending', {
        defaultValue:
          'Archiving becomes available after usage accounting preparation completes. Previewing is still safe.',
      })
    : '';
  const navigateTo = (nextView: UsageMaintenanceView) => {
    setError(null);
    setPendingConfirmation(null);
    if (nextView === 'advanced' || nextView === 'diagnostics' || nextView === 'overview') {
      updateNavigation({ panel: nextView });
      return;
    }
    const tab = nextView === 'transfer' ? 'transfer' : 'organize';
    updateNavigation({
      tab,
      panel: nextView === 'create' ? 'create' : null,
      runId: null,
      sessionId: null,
    });
    if (nextView === 'create') {
      setSelectedArchive(null);
      setGuidedArchiveStage('idle');
      setGuidedArchiveRunId(null);
      setPreviewRefreshToken((value) => value + 1);
    }
  };

  const copyCompactCommand = useCallback(async () => {
    try {
      if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(COMPACT_USAGE_COMMAND);
      showNotification(
        t('usage_maintenance.advanced_copy_success', {
          defaultValue: 'Offline compact command copied.',
        }),
        'success'
      );
    } catch {
      showNotification(
        t('usage_maintenance.advanced_copy_failed', {
          defaultValue: 'The command could not be copied. Select it manually from the code block.',
        }),
        'warning'
      );
    }
  }, [showNotification, t]);

  const openRun = (run: UsageArchiveRunSummary) => {
    setPendingConfirmation(null);
    setSelectedArchive((current) => (current?.run.id === run.id ? current : null));
    setSelectedArchiveRefreshToken((value) => value + 1);
    setError(null);
    updateNavigation({ runId: run.id, panel: 'run' });
  };

  const updateHistoryFilter = (filter: ArchiveHistoryFilter) => {
    setHistoryList({ runs: [] });
    setHistoryCursor(undefined);
    setHistoryCursorStack([]);
    updateNavigation({ filter }, true);
  };

  const updateHistorySource = (source: ArchiveHistorySource) => {
    setHistoryList({ runs: [] });
    setHistoryCursor(undefined);
    setHistoryCursorStack([]);
    updateNavigation({ source }, true);
  };

  const nextHistoryPage = () => {
    if (!displayedHistory.next_cursor) return;
    setHistoryList({ runs: [] });
    setHistoryCursorStack((stack) => [...stack, historyCursor ?? '']);
    setHistoryCursor(displayedHistory.next_cursor);
  };

  const previousHistoryPage = () => {
    setHistoryList({ runs: [] });
    setHistoryCursorStack((stack) => {
      if (stack.length === 0) return stack;
      const previous = stack[stack.length - 1];
      setHistoryCursor(previous || undefined);
      return stack.slice(0, -1);
    });
  };

  const archiveActionDisabled = (run: UsageArchiveRunSummary, action: ArchiveRunAction) => {
    const waitingForMigration =
      archiveReadinessPending && actionRequiresMigrationReady(run, action);
    return waitingForMigration || (actionIsDestructive(run, action) && deleteDisabled);
  };

  const archiveActionTitle = (run: UsageArchiveRunSummary, action: ArchiveRunAction) => {
    if (archiveReadinessPending && actionRequiresMigrationReady(run, action)) {
      return archiveReadinessHint;
    }
    if (actionIsDestructive(run, action)) return deleteReadinessHint || undefined;
    return undefined;
  };

  useLayoutEffect(() => {
    if (typeof window === 'undefined' || !pageRef.current) return;
    const container = findScrollContainer(pageRef.current);
    const scrollPositions = scrollPositionsRef.current;
    const saved = scrollPositions.get(navigation.tab);
    window.scrollTo({ top: saved?.window ?? 0, behavior: 'instant' });
    if (container) container.scrollTop = saved?.container ?? 0;
    if (!document.activeElement?.closest('[role="tablist"]'))
      panelRef.current?.focus({ preventScroll: true });
    return () => {
      scrollPositions.set(navigation.tab, {
        window: window.scrollY,
        container: container?.scrollTop ?? 0,
      });
    };
  }, [navigation.tab, maintenanceLoaded]);

  useLayoutEffect(() => {
    if (
      typeof window === 'undefined' ||
      !pageRef.current ||
      !navigation.panel ||
      drawerScrollRef.current
    )
      return;
    const container = findScrollContainer(pageRef.current);
    drawerScrollRef.current = {
      tab: navigation.tab,
      window: window.scrollY,
      container,
      containerTop: container?.scrollTop ?? 0,
    };
  }, [navigation.panel, navigation.tab, maintenanceLoaded]);

  useEffect(() => {
    // Wait for the drawer's exit animation and scroll unlock before restoring the page.
    if (drawerVisible || navigation.panel || !drawerScrollRef.current) return;
    const saved = drawerScrollRef.current;
    drawerScrollRef.current = null;
    if (saved.tab !== navigation.tab) return;
    window.scrollTo({ top: saved.window, behavior: 'instant' });
    if (saved.container?.isConnected) saved.container.scrollTop = saved.containerTop;
  }, [drawerVisible, navigation.panel, navigation.tab]);

  useLayoutEffect(() => {
    if (drawerBodyRef.current) drawerBodyRef.current.scrollTop = 0;
  }, [navigation.panel, selectedRunId, drawerConfirmation]);

  const runActions = {
    working,
    onAction: confirmAction,
    actionDisabled: archiveActionDisabled,
    actionTitle: archiveActionTitle,
    actionLabel: (run: UsageArchiveRunSummary, action: ArchiveRunAction) =>
      actionLabel(run, action),
  };
  const currentRunWorking = working && activeOperationRunIdRef.current === selectedRunId;
  const currentArchive =
    currentRunWorking && operationArchive?.run.id === selectedRunId
      ? operationArchive
      : selectedArchive?.run.id === selectedRunId
        ? selectedArchive
        : null;
  const renderRun = () =>
    currentArchive && maintenance ? (
      <UsageArchiveRunView
        archive={currentArchive}
        maintenance={maintenance}
        active={archiveProgressStatuses.has(currentArchive.run.status)}
        intent={navigation.intent}
        {...runActions}
        working={currentRunWorking}
      />
    ) : selectedArchiveLoading ? (
      <LoadingSpinner />
    ) : (
      <div className={styles.error} role="alert">
        {error ??
          t('usage_maintenance.archive_response_invalid', {
            defaultValue: 'The archive record could not be loaded.',
          })}
        <Button
          variant="secondary"
          size="sm"
          onClick={() => setSelectedArchiveRefreshToken((value) => value + 1)}
        >
          {t('common.retry')}
        </Button>
      </div>
    );

  if (availability.checking || (loading && !maintenance)) return <LoadingSpinner />;
  if (unsupported)
    return (
      <div className={styles.page}>
        <p>
          {t('usage_maintenance.unsupported', {
            defaultValue:
              'This Manager Server is older than the usage maintenance API. Upgrade the server to manage archives here.',
          })}
        </p>
        <Button variant="secondary" onClick={refreshMaintenance}>
          {t('common.refresh')}
        </Button>
      </div>
    );
  if (!maintenance)
    return (
      <div className={styles.page}>
        {error ? (
          <p className={styles.error} role="alert">
            {error}
          </p>
        ) : null}
        <Button variant="secondary" onClick={refreshMaintenance}>
          {t('common.retry')}
        </Button>
      </div>
    );

  return (
    <div className={styles.page} ref={pageRef}>
      <div className={styles.controlsPanel}>
        <SegmentedTabs
          idBase="usage-maintenance"
          className={styles.workspaceTabs}
          equalWidth
          activeTab={navigation.tab}
          ariaLabel={t('usage_maintenance.title', { defaultValue: 'Usage maintenance' })}
          items={[
            {
              id: 'organize',
              label: t('usage_maintenance.archive_management'),
            },
            {
              id: 'transfer',
              label: t('usage_maintenance.workspace_transfer', { defaultValue: 'Import / export' }),
            },
          ]}
          onChange={(tab) => {
            setPendingConfirmation(null);
            updateNavigation({ tab, panel: null, runId: null, sessionId: null });
          }}
        />
        <div className={styles.actions}>
          <Button
            size="sm"
            variant="secondary"
            onClick={refreshMaintenance}
            disabled={working || loading}
            aria-label={t('common.refresh')}
            title={t('common.refresh')}
          >
            <IconRefreshCw size={16} />
          </Button>
          <DropdownMenu
            ariaLabel={t('usage_maintenance.more_maintenance')}
            triggerLabel={
              <span className={styles.toolbarLabel}>{t('usage_maintenance.more_maintenance')}</span>
            }
            triggerIcon={<IconMoreVertical size={16} />}
            items={[
              {
                key: 'storage',
                label: t('usage_maintenance.storage_recovery'),
                onClick: () => navigateTo('advanced'),
              },
              {
                key: 'diagnostics',
                label: t('usage_maintenance.diagnostics_title'),
                onClick: () => navigateTo('diagnostics'),
              },
            ]}
          />
          {navigation.tab === 'transfer' ? (
            <>
              <Button
                size="sm"
                variant="secondary"
                onClick={() => updateNavigation({ panel: 'export', sessionId: null })}
              >
                <IconDownload size={16} />
                <span className={styles.toolbarLabel}>{t('usage_maintenance.export_online')}</span>
                <span className={styles.srOnlyMobile}>{t('usage_maintenance.export_online')}</span>
              </Button>
              <Button
                size="sm"
                onClick={() => updateNavigation({ panel: 'import', sessionId: null })}
              >
                <IconArrowUpFromLine size={16} />
                {t('usage_maintenance.import_file')}
              </Button>
            </>
          ) : (
            <Button size="sm" onClick={() => navigateTo('create')} disabled={working || loading}>
              <IconPlus size={16} />
              {t('usage_maintenance.new_archive')}
            </Button>
          )}
        </div>
      </div>
      {error || (navigation.tab === 'organize' && historyError) ? (
        <div className={styles.error} role="alert">
          {error || historyError}
        </div>
      ) : null}
      {postDeleteNoticeVisible ? (
        <section
          className={styles.postDeleteNotice}
          aria-live="polite"
          data-testid="usage-post-delete-notice"
        >
          <strong>
            {t('usage_maintenance.cleanup_complete_title', {
              defaultValue: 'Online detail cleanup complete',
            })}
          </strong>
          <p>
            {postDeleteRefreshFailed
              ? t('usage_maintenance.workspace_cleanup_refresh_failed', {
                  defaultValue:
                    'Cleanup succeeded. Current storage statistics are unavailable; refresh to read them again.',
                })
              : maintenance.storage.reclaimable_bytes > 0
                ? t('usage_maintenance.cleanup_complete_reclaimable', {
                    size: formatFileSize(maintenance.storage.reclaimable_bytes),
                    defaultValue:
                      'The SQLite file does not shrink immediately. About {{size}} can be reclaimed by offline compaction.',
                  })
                : t('usage_maintenance.cleanup_complete_no_reclaimable', {
                    defaultValue:
                      'No significant reclaimable free pages were detected. Physical compaction is not immediately necessary.',
                  })}
          </p>
          <div className={styles.actions}>
            {postDeleteRefreshFailed ? (
              <Button size="sm" variant="secondary" onClick={refreshMaintenance}>
                {t('common.refresh')}
              </Button>
            ) : null}
            <Button size="sm" variant="secondary" onClick={() => navigateTo('advanced')}>
              {t('usage_maintenance.cleanup_complete_open_advanced', {
                defaultValue: 'View storage and compaction',
              })}
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setPostDeleteNoticeVisible(false)}>
              {t('common.close')}
            </Button>
          </div>
        </section>
      ) : null}
      <div
        role="tabpanel"
        aria-labelledby={`usage-maintenance-${navigation.tab}`}
        ref={panelRef}
        tabIndex={-1}
        className={styles.workspace}
      >
        {navigation.tab === 'organize' ? (
          <>
            <UsageMaintenanceOverviewView
              maintenance={maintenance}
              archives={
                working && operationArchive ? [operationArchive.run, ...archives] : archives
              }
              stale={postDeleteRefreshFailed}
              onNavigate={navigateTo}
              onOpenRun={openRun}
            />
            {working && !operationArchive ? (
              <div className={styles.preparingNotice} role="status">
                <span>{t('usage_maintenance.archive_prepare_creating')}</span>
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => updateNavigation({ panel: 'create' })}
                >
                  {t('usage_maintenance.details')}
                </Button>
              </div>
            ) : null}
            <UsageArchiveHistoryView
              archiveList={displayedHistory}
              filter={historyFilter}
              source={historySource}
              loading={showingRecentArchives ? loading : historyLoading}
              canGoBack={historyCursorStack.length > 0}
              onFilter={updateHistoryFilter}
              onSource={updateHistorySource}
              onNextPage={nextHistoryPage}
              onPreviousPage={previousHistoryPage}
              onOpenRun={openRun}
              {...runActions}
            />
          </>
        ) : null}
        {transferVisited ? (
          <div hidden={view !== 'transfer'} className={styles.transferPanel}>
            <UsageMaintenanceTransferView
              key={`${serviceBase}\u0000${managementKey}`}
              serviceBase={serviceBase}
              managementKey={managementKey}
              panel={
                navigation.tab === 'transfer' &&
                (navigation.panel === 'import' ||
                  navigation.panel === 'export' ||
                  navigation.panel === 'import-session')
                  ? navigation.panel
                  : null
              }
              sessionId={navigation.sessionId}
              refreshToken={transferRefreshToken}
              onUsageChanged={handleUsageChanged}
              onOpenPanel={(panel, sessionId) =>
                updateNavigation({ panel, sessionId: sessionId ?? null })
              }
              onClosePanel={closeDrawer}
              drawerClassName={styles.drawer}
              drawerBodyRef={setDrawerBodyRef}
            />
          </div>
        ) : null}
      </div>
      <Drawer
        open={
          navigation.panel !== null &&
          !['import', 'import-session', 'export'].includes(navigation.panel)
        }
        onClose={closeDrawer}
        width="min(600px, 100vw)"
        className={styles.drawer}
        bodyRef={setDrawerBodyRef}
        title={
          drawerConfirmation?.title ??
          (navigation.panel === 'create'
            ? t('usage_maintenance.new_archive')
            : navigation.panel === 'run'
              ? t('usage_maintenance.run_detail_title')
              : navigation.panel === 'overview'
                ? t('usage_maintenance.online_range_title')
                : navigation.panel === 'advanced'
                  ? t('usage_maintenance.storage_recovery')
                  : t('usage_maintenance.diagnostics_title'))
        }
        footer={
          <div className={styles.drawerActions}>
            {drawerConfirmation ? (
              <>
                <Button variant="secondary" onClick={() => setPendingConfirmation(null)}>
                  {t('common.back')}
                </Button>
                <Button
                  variant={drawerConfirmation.variant}
                  disabled={working || (drawerConfirmation.variant === 'danger' && deleteDisabled)}
                  onClick={() => {
                    if (!mountedRef.current || drawerConfirmationRef.current !== drawerConfirmation)
                      return;
                    setPendingConfirmation(null);
                    drawerConfirmation.onConfirm();
                  }}
                >
                  {drawerConfirmation.confirmText}
                </Button>
              </>
            ) : navigation.panel === 'create' ? (
              <>
                <Button variant="secondary" onClick={closeDrawer}>
                  {t('common.cancel')}
                </Button>
                {working ? (
                  <Button variant="secondary" onClick={cancelGuidedArchive}>
                    {t('usage_maintenance.archive_prepare_stop')}
                  </Button>
                ) : null}
                <Button
                  onClick={confirmCreate}
                  disabled={
                    working ||
                    !preview ||
                    preview.event_count <= 0 ||
                    previewLoading ||
                    Boolean(previewError) ||
                    createBlockedByMaintenance ||
                    archiveReadinessPending
                  }
                >
                  {working
                    ? t(`usage_maintenance.archive_prepare_${guidedArchiveStage}`)
                    : t('common.next')}
                </Button>
              </>
            ) : navigation.panel === 'run' && currentArchive ? (
              <>
                {currentRunWorking ? (
                  <>
                    <Button variant="secondary" onClick={cancelGuidedArchive}>
                      {t('usage_maintenance.archive_prepare_stop')}
                    </Button>
                    <Button onClick={closeDrawer}>{t('usage_maintenance.minimize')}</Button>
                  </>
                ) : (
                  <>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={refreshMaintenance}
                      aria-label={t('common.refresh')}
                      title={t('common.refresh')}
                    >
                      <IconRefreshCw size={16} />
                    </Button>
                    <UsageArchiveRunActions
                      run={currentArchive.run}
                      intent={navigation.intent}
                      {...runActions}
                    />
                    <Button
                      variant={
                        currentArchive.run.status === 'completed' ||
                        (currentArchive.run.status === 'verified' &&
                          navigation.intent === 'archive')
                          ? 'primary'
                          : 'secondary'
                      }
                      onClick={closeDrawer}
                    >
                      {currentArchive.run.status === 'completed' ||
                      currentArchive.run.status === 'verified' ||
                      currentArchive.run.status === 'cancelled'
                        ? t('usage_maintenance.done')
                        : t('common.close')}
                    </Button>
                  </>
                )}
              </>
            ) : (
              <>
                <Button
                  variant="secondary"
                  onClick={refreshMaintenance}
                  disabled={working || loading}
                >
                  <IconRefreshCw size={16} />
                  {t('common.refresh')}
                </Button>
                <Button variant="secondary" onClick={closeDrawer}>
                  {t('common.close')}
                </Button>
              </>
            )}
          </div>
        }
      >
        {drawerConfirmation ? (
          drawerConfirmation.message
        ) : (
          <>
            {navigation.panel === 'create' ? (
              <UsageMaintenanceCreateView
                maintenance={maintenance}
                preview={preview}
                previewLoading={previewLoading}
                previewError={previewError}
                retentionSelection={retentionSelection}
                customCutoff={customCutoff}
                referenceNowMS={referenceNowMS}
                resolvedCutoffTimestamp={resolvedCutoffTimestamp}
                recommendedRetentionDays={recommendedRetentionDays}
                intent={navigation.intent}
                onIntent={(intent) => updateNavigation({ intent }, true)}
                working={working}
                createBlockedByMaintenance={createBlockedByMaintenance}
                archiveReadinessPending={archiveReadinessPending}
                archiveReadinessHint={archiveReadinessHint}
                onHistory={() => navigateTo('history')}
                onSelectRetention={selectRetention}
                onUpdateCustomCutoff={updateCustomCutoff}
                onRetryPreview={() => setPreviewRefreshToken((value) => value + 1)}
              />
            ) : null}
            {navigation.panel === 'run' ? renderRun() : null}
            {navigation.panel === 'overview' ? (
              <div className={styles.confirmSummary}>
                <p>
                  {postDeleteRefreshFailed ? (
                    t('usage_maintenance.workspace_cleanup_refresh_failed')
                  ) : rawEventRange?.kind === 'empty' ? (
                    t('usage_maintenance.raw_range_empty')
                  ) : rawEventRange?.kind === 'available' ? (
                    <>
                      {formatTime(rawEventRange.minTimestampMS)}
                      <br />
                      {formatTime(rawEventRange.maxTimestampMS)}
                    </>
                  ) : (
                    t('usage_maintenance.raw_range_unavailable')
                  )}
                </p>
                <p>{t('usage_maintenance.preview_excludes_existing')}</p>
              </div>
            ) : null}
            {navigation.panel === 'advanced' ? (
              <UsageMaintenanceAdvancedView
                maintenance={maintenance}
                stale={postDeleteRefreshFailed}
                onCopyCommand={() => void copyCompactCommand()}
              />
            ) : null}
            {navigation.panel === 'diagnostics' ? (
              <UsageMaintenanceDiagnosticsView
                maintenance={maintenance}
                onOpenActive={() => {
                  if (maintenance.active_run) openRun(maintenance.active_run);
                }}
              />
            ) : null}
          </>
        )}
      </Drawer>
    </div>
  );
}
