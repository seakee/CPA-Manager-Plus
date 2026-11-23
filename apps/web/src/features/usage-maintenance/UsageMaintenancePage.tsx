import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useSearchParams } from 'react-router-dom';
import { MetricSummaryGrid, type MetricSummaryCardProps } from '@/components/common/MetricSummaryCard';
import { PageControlBar } from '@/components/common/PageControlBar';
import { PaginationFooter } from '@/components/common/PaginationFooter';
import { SplashScreen } from '@/components/common/SplashScreen';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { EmptyState } from '@/components/ui/EmptyState';
import { Modal } from '@/components/ui/Modal';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import { Select } from '@/components/ui/Select';
import {
  IconCheck,
  IconChevronRight,
  IconCircleHelp,
  IconClock,
  IconCopy,
  IconPlus,
  IconRefreshCw,
  IconSearch,
  IconTrash2,
  IconTriangleAlert,
  IconX,
} from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import {
  getUsageServiceErrorCode,
  usageServiceApi,
  type UsageArchivePreview,
  type UsageArchiveRunSummary,
  type UsageArchiveStatusSummary,
  type UsageMaintenanceStatus,
  type UsageServiceStatus,
} from '@/services/api/usageService';
import { useAuthStore, useNotificationStore } from '@/stores';
import { formatDateTime, formatFileSize } from '@/utils/format';
import { UsageMaintenanceCreateDrawerContent } from './UsageMaintenanceCreateDrawerContent';
import { UsageMaintenanceDiagnosticsSection } from './UsageMaintenanceDiagnosticsSection';
import {
  UsageMaintenanceDeleteConfirmationView,
  UsageMaintenanceRunDrawerContent,
} from './UsageMaintenanceRunDrawerContent';
import { UsageMaintenanceTransferSection } from './UsageMaintenanceTransferSection';
import {
  archiveHistoryFilterStatus,
  filterArchiveRunsByQuery,
  getArchiveRunAction,
  getArchiveRunPresentationStage,
  isArchiveRunActiveStatus,
  isArchiveRunCancellable,
  parseUsageMaintenanceTab,
  pickFreshestArchiveStatus,
  recommendRetentionDays,
  resolveArchiveProgressPresentation,
  resolveRawEventRange,
  resolveRetentionCutoff,
  sumStatusCounts,
  toLocalDateTimeValue,
  type ArchiveHistoryFilter,
  type ArchiveHistorySource,
  type MaintenanceIntent,
  type RetentionSelection,
  type UsageMaintenanceTab,
} from './usageMaintenanceModel';
import styles from './UsageMaintenancePage.module.scss';

const ARCHIVE_STATUS_FILTERS: ArchiveHistoryFilter[] = [
  'all',
  'previewed',
  'archiving',
  'archived',
  'verifying',
  'verified',
  'deleting',
  'completed',
  'failed',
  'cancelled',
];

const DEFAULT_PAGE_SIZE = 20;
const PAGE_SIZE_OPTIONS = [10, 20, 50] as const;

function extractHttpStatus(error: unknown): number | undefined {
  if (!error || typeof error !== 'object') return undefined;
  const errObj = error as { status?: unknown; response?: { status?: unknown } };
  if (typeof errObj.status === 'number') return errObj.status;
  if (typeof errObj.response?.status === 'number') return errObj.response.status;
  return undefined;
}

function getRunStatusToneClass(status: string): string {
  if (status === 'completed' || status === 'verified' || status === 'archived') {
    return styles.statusSuccess;
  }
  if (status === 'failed') return styles.statusDanger;
  if (status === 'cancelled') return styles.statusNeutral;
  if (status === 'archiving' || status === 'verifying' || status === 'deleting') {
    return styles.statusWarning;
  }
  return styles.statusNeutral;
}

export function UsageMaintenancePage() {
  const connectionScope = useAuthStore(
    (state) => `${state.sessionMode}:${state.apiBase}:${state.managementKey ? 'auth' : 'anon'}`
  );
  const { managerServiceBase } = usePanelFeatureAvailability();
  return <UsageMaintenancePageContent key={`${connectionScope}:${managerServiceBase}`} />;
}

function UsageMaintenancePageContent() {
  const { t, i18n } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  const managementKey = useAuthStore((state) => state.managementKey);
  const { showNotification } = useNotificationStore();
  const featureAvailability = usePanelFeatureAvailability();
  const managerBase = featureAvailability.managerServiceBase;

  const activeTab = parseUsageMaintenanceTab(searchParams.get('tab'));
  const selectedRunId = searchParams.get('run');
  const formatTimestamp = useCallback(
    (value?: number) => (value ? formatDateTime(new Date(value), i18n.language) : '—'),
    [i18n.language]
  );

  const [maintenance, setMaintenance] = useState<UsageMaintenanceStatus | null>(null);
  const [serviceStatus, setServiceStatus] = useState<UsageServiceStatus | null>(null);
  const [runs, setRuns] = useState<UsageArchiveRunSummary[]>([]);
  const [runsTotal, setRunsTotal] = useState(0);
  const [statusCounts, setStatusCounts] = useState<Record<string, number> | undefined>(undefined);
  const [nextCursor, setNextCursor] = useState<string | undefined>(undefined);
  const [loaded, setLoaded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [unsupported, setUnsupported] = useState(false);
  const [loadError, setLoadError] = useState('');

  // Create archive drawer state
  const [createDrawerOpen, setCreateDrawerOpen] = useState(false);
  const [createConfirmStep, setCreateConfirmStep] = useState(false);
  const [createIntent, setCreateIntent] = useState<MaintenanceIntent>('archive');
  const [retentionSelection, setRetentionSelection] = useState<RetentionSelection>(30);
  const [referenceNowMS, setReferenceNowMS] = useState(() => Date.now());
  const [customCutoff, setCustomCutoff] = useState(() =>
    toLocalDateTimeValue(Date.now() - 30 * 24 * 60 * 60 * 1000)
  );
  const [preview, setPreview] = useState<UsageArchivePreview | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [previewRefreshToken, setPreviewRefreshToken] = useState(0);
  const [createWorking, setCreateWorking] = useState(false);
  const createAbortRef = useRef<AbortController | null>(null);
  const previewAbortRef = useRef<AbortController | null>(null);

  // Archive history filters & cursor pagination
  const [statusFilter, setStatusFilter] = useState<ArchiveHistoryFilter>('all');
  const [sourceFilter, setSourceFilter] = useState<ArchiveHistorySource>('all');
  const [searchQuery, setSearchQuery] = useState('');
  const [pageSize, setPageSize] = useState<number>(DEFAULT_PAGE_SIZE);
  const [currentCursor, setCurrentCursor] = useState<string | undefined>(undefined);
  const [cursorStack, setCursorStack] = useState<string[]>([]);

  // Run actions, detail drawer, delete & cancel confirmations
  const [actingRunId, setActingRunId] = useState('');
  const [detailStatus, setDetailStatus] = useState<UsageArchiveStatusSummary | null>(null);
  const [operationArchive, setOperationArchive] = useState<UsageArchiveStatusSummary | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState('');
  const [deleteTargetRun, setDeleteTargetRun] = useState<UsageArchiveRunSummary | null>(null);
  const [deleteAcknowledged, setDeleteAcknowledged] = useState(false);
  const [cancelTargetRun, setCancelTargetRun] = useState<UsageArchiveRunSummary | null>(null);
  const [postDeleteNoticeVisible, setPostDeleteNoticeVisible] = useState(false);

  const mountedRef = useRef(true);
  const requestSeqRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      createAbortRef.current?.abort();
      previewAbortRef.current?.abort();
    };
  }, []);

  const resolveApiError = useCallback(
    (error: unknown, fallbackKey = 'usage_service_errors.request_failed') => {
      const code = getUsageServiceErrorCode(error);
      if (code) {
        return t(`usage_service_errors.${code}`, {
          defaultValue: t(fallbackKey, { defaultValue: 'Request failed.' }),
        });
      }
      if (error instanceof Error && error.message) return error.message;
      return t(fallbackKey, { defaultValue: 'Request failed.' });
    },
    [t]
  );

  const loadWorkspace = useCallback(
    async (isBackground = false) => {
      if (!managerBase || !managementKey) return;
      const reqId = ++requestSeqRef.current;
      if (isBackground) setRefreshing(true);
      else setLoading(true);
      setLoadError('');

      try {
        const [maintRes, listRes, statusRes] = await Promise.all([
          usageServiceApi.getMaintenanceStatus(managerBase, managementKey),
          usageServiceApi.listArchives(managerBase, managementKey, {
            limit: pageSize,
            status: archiveHistoryFilterStatus(statusFilter),
            mode: sourceFilter === 'all' ? undefined : sourceFilter,
            ...(currentCursor ? { cursor: currentCursor } : {}),
          }),
          usageServiceApi.getStatus(managerBase, managementKey).catch(() => null),
        ]);
        if (!mountedRef.current || reqId !== requestSeqRef.current) return;
        setUnsupported(false);
        setMaintenance(maintRes);
        setRuns(listRes.runs);
        setRunsTotal(listRes.total);
        setStatusCounts(listRes.status_counts);
        setNextCursor(listRes.next_cursor);
        if (statusRes) setServiceStatus(statusRes);
        setLoaded(true);
      } catch (err) {
        if (!mountedRef.current || reqId !== requestSeqRef.current) return;
        if (extractHttpStatus(err) === 404) {
          setUnsupported(true);
        } else {
          setLoadError(resolveApiError(err));
        }
        setLoaded(true);
      } finally {
        if (mountedRef.current && reqId === requestSeqRef.current) {
          setLoading(false);
          setRefreshing(false);
        }
      }
    },
    [currentCursor, managementKey, managerBase, pageSize, resolveApiError, sourceFilter, statusFilter]
  );

  useEffect(() => {
    if (featureAvailability.checking || !featureAvailability.managerServiceAvailable) return;
    void loadWorkspace(false);
  }, [
    featureAvailability.checking,
    featureAvailability.managerServiceAvailable,
    loadWorkspace,
  ]);

  useHeaderRefresh(
    () => loadWorkspace(true),
    !featureAvailability.checking && featureAvailability.managerServiceAvailable
  );

  const refreshRunDetail = useCallback(
    async (runId: string) => {
      if (!managerBase || !managementKey || !runId) return;
      setDetailLoading(true);
      setDetailError('');
      try {
        const res = await usageServiceApi.getArchiveStatus(managerBase, managementKey, runId);
        if (!mountedRef.current) return;
        setDetailStatus((prev) => pickFreshestArchiveStatus(res, prev));
      } catch (err) {
        if (!mountedRef.current) return;
        const message = resolveApiError(err);
        setDetailError(message);
        showNotification(message, 'error');
      } finally {
        if (mountedRef.current) setDetailLoading(false);
      }
    },
    [managementKey, managerBase, resolveApiError, showNotification]
  );

  useEffect(() => {
    if (!selectedRunId) {
      setDetailStatus(null);
      setDetailError('');
      return;
    }
    void refreshRunDetail(selectedRunId);
  }, [refreshRunDetail, selectedRunId]);

  const activeBackgroundRun = useMemo(
    () =>
      pickFreshestArchiveStatus(
        maintenance?.active_run && isArchiveRunActiveStatus(maintenance.active_run.status)
          ? { run: maintenance.active_run, segments: [] }
          : null,
        operationArchive?.run && isArchiveRunActiveStatus(operationArchive.run.status)
          ? operationArchive
          : null
      )?.run ?? null,
    [maintenance?.active_run, operationArchive]
  );

  const shouldPollActive = Boolean(
    maintenance?.active_lock ||
      activeBackgroundRun ||
      (selectedRunId && detailStatus && isArchiveRunActiveStatus(detailStatus.run.status))
  );

  useEffect(() => {
    if (!shouldPollActive) return;
    const timer = window.setInterval(() => {
      if (selectedRunId) void refreshRunDetail(selectedRunId);
      void loadWorkspace(true);
    }, 2000);
    return () => window.clearInterval(timer);
  }, [loadWorkspace, refreshRunDetail, selectedRunId, shouldPollActive]);

  const setActiveTab = (tab: UsageMaintenanceTab) => {
    setSearchParams((prev: URLSearchParams) => {
      const next = new URLSearchParams(prev);
      if (tab === 'archives') next.delete('tab');
      else next.set('tab', tab);
      return next;
    });
  };

  const openRunDetail = (runId: string) => {
    setDeleteTargetRun(null);
    setDeleteAcknowledged(false);
    setCreateDrawerOpen(false);
    setCreateConfirmStep(false);
    setSearchParams((prev: URLSearchParams) => {
      const next = new URLSearchParams(prev);
      next.set('run', runId);
      return next;
    });
  };

  const closeRunDrawer = () => {
    setDeleteTargetRun(null);
    setDeleteAcknowledged(false);
    setSearchParams((prev: URLSearchParams) => {
      const next = new URLSearchParams(prev);
      next.delete('run');
      return next;
    });
  };

  const rawRange = useMemo(
    () =>
      maintenance
        ? resolveRawEventRange(maintenance)
        : ({ kind: 'empty' } as ReturnType<typeof resolveRawEventRange>),
    [maintenance]
  );

  const recommendedDays = useMemo(() => {
    if (!maintenance) return null;
    return recommendRetentionDays(rawRange, referenceNowMS);
  }, [maintenance, rawRange, referenceNowMS]);

  const resolvedCutoffMS = resolveRetentionCutoff(
    retentionSelection,
    customCutoff,
    referenceNowMS
  );
  const hasActiveMutex =
    Boolean(maintenance?.active_run && isArchiveRunActiveStatus(maintenance.active_run.status)) ||
    Boolean(maintenance?.active_lock);
  const archiveReadinessBlocked = Boolean(maintenance && !maintenance.readiness.migration_ready);

  // Auto-preview whenever Create Drawer is open and cutoff changes
  useEffect(() => {
    if (
      !createDrawerOpen ||
      createConfirmStep ||
      createWorking ||
      !loaded ||
      unsupported ||
      !managerBase ||
      !managementKey
    ) {
      return;
    }

    previewAbortRef.current?.abort();
    previewAbortRef.current = null;
    setPreview(null);
    setPreviewError(null);

    if (!resolvedCutoffMS) {
      setPreviewLoading(false);
      setPreviewError(t('usage_maintenance.invalid_cutoff'));
      return;
    }

    const controller = new AbortController();
    previewAbortRef.current = controller;
    setPreviewLoading(true);

    const runPreview = async () => {
      try {
        const res = await usageServiceApi.previewArchive(
          managerBase,
          managementKey,
          resolvedCutoffMS,
          controller.signal
        );
        if (!mountedRef.current || controller.signal.aborted) return;
        setPreview(res);
      } catch (err) {
        if (!mountedRef.current || controller.signal.aborted) return;
        const message = resolveApiError(err);
        setPreviewError(message);
        showNotification(message, 'error');
      } finally {
        if (mountedRef.current && previewAbortRef.current === controller) {
          previewAbortRef.current = null;
          setPreviewLoading(false);
        }
      }
    };

    let timer: ReturnType<typeof setTimeout> | undefined;
    if (retentionSelection === 'custom' && previewRefreshToken === 0) {
      timer = setTimeout(() => void runPreview(), 250);
    } else {
      void runPreview();
    }

    return () => {
      if (timer) clearTimeout(timer);
      controller.abort();
      if (previewAbortRef.current === controller) previewAbortRef.current = null;
    };
  }, [
    createConfirmStep,
    createDrawerOpen,
    createWorking,
    loaded,
    managementKey,
    managerBase,
    previewRefreshToken,
    resolveApiError,
    resolvedCutoffMS,
    retentionSelection,
    showNotification,
    t,
    unsupported,
  ]);

  const openCreateDrawer = () => {
    setReferenceNowMS(Date.now());
    setCreateConfirmStep(false);
    setPreview(null);
    setPreviewError(null);
    setCreateDrawerOpen(true);
  };

  const closeCreateDrawer = () => {
    previewAbortRef.current?.abort();
    setCreateDrawerOpen(false);
    setCreateConfirmStep(false);
  };

  const handleStopGuidedCreate = () => {
    createAbortRef.current?.abort();
    createAbortRef.current = null;
    setCreateWorking(false);
    showNotification(
      t('usage_maintenance.archive_prepare_cancelled', {
        defaultValue:
          'The request was stopped. If an archive task was created, it remains recoverable in history.',
      }),
      'warning'
    );
    void loadWorkspace(true);
  };

  const handleExecuteCreateArchive = async () => {
    if (
      !managerBase ||
      !managementKey ||
      createWorking ||
      hasActiveMutex ||
      archiveReadinessBlocked
    ) {
      return;
    }
    const cutoff = preview?.cutoff_timestamp_ms ?? resolvedCutoffMS;
    if (!cutoff) {
      showNotification(t('usage_maintenance.invalid_cutoff'), 'warning');
      return;
    }

    createAbortRef.current?.abort();
    const controller = new AbortController();
    createAbortRef.current = controller;
    setCreateWorking(true);

    try {
      const created = await usageServiceApi.createArchive(
        managerBase,
        managementKey,
        cutoff,
        controller.signal
      );
      if (!mountedRef.current || controller.signal.aborted) return;

      const runId = created.run.id;
      let latest: UsageArchiveStatusSummary = created;
      setOperationArchive(latest);
      setDetailStatus(latest);
      setCreateDrawerOpen(false);
      setCreateConfirmStep(false);
      openRunDetail(runId);

      if (latest.run.status === 'previewed') {
        latest = await usageServiceApi.resumeArchive(
          managerBase,
          managementKey,
          runId,
          true,
          controller.signal,
          'archiving'
        );
        if (!mountedRef.current || controller.signal.aborted) return;
        setOperationArchive(latest);
        setDetailStatus((prev) => pickFreshestArchiveStatus(latest, prev));
      }

      if (latest.run.status === 'archived' || latest.run.status === 'verifying') {
        latest = await usageServiceApi.verifyArchive(
          managerBase,
          managementKey,
          runId,
          true,
          controller.signal
        );
        if (!mountedRef.current || controller.signal.aborted) return;
        setOperationArchive(latest);
        setDetailStatus((prev) => pickFreshestArchiveStatus(latest, prev));
      }

      setPreview(null);
      showNotification(
        t('usage_maintenance.archive_prepare_success', {
          defaultValue: t('usage_maintenance.create_success'),
        }),
        'success'
      );
      await loadWorkspace(true);
    } catch (err) {
      if (!mountedRef.current || controller.signal.aborted) return;
      showNotification(resolveApiError(err), 'error');
      await loadWorkspace(true);
    } finally {
      if (createAbortRef.current === controller) createAbortRef.current = null;
      if (mountedRef.current) {
        setCreateWorking(false);
        setOperationArchive(null);
      }
    }
  };

  const executeRunAction = async (
    runId: string,
    action: 'verify' | 'delete' | 'resume' | 'cancel'
  ) => {
    if (!managerBase || !managementKey || actingRunId) return;
    setActingRunId(runId);
    if (action === 'delete') setPostDeleteNoticeVisible(false);
    try {
      let updated: UsageArchiveStatusSummary | null = null;
      if (action === 'verify') {
        updated = await usageServiceApi.verifyArchive(managerBase, managementKey, runId, true);
        showNotification(t('usage_maintenance.verify_success'), 'success');
      } else if (action === 'delete') {
        updated = await usageServiceApi.deleteArchive(managerBase, managementKey, runId, true);
        showNotification(t('usage_maintenance.delete_success'), 'success');
        if (updated?.run.status === 'completed') {
          setPostDeleteNoticeVisible(true);
        }
      } else if (action === 'resume') {
        updated = await usageServiceApi.resumeArchive(managerBase, managementKey, runId, true);
        showNotification(t('usage_maintenance.resume_success'), 'success');
      } else {
        updated = await usageServiceApi.cancelArchive(managerBase, managementKey, runId);
        showNotification(t('usage_maintenance.cancel_success'), 'success');
      }
      if (!mountedRef.current) return;
      if (updated) {
        setOperationArchive(updated);
        if (selectedRunId === runId) {
          setDetailStatus((prev) => pickFreshestArchiveStatus(updated, prev));
        }
      }
      await loadWorkspace(true);
    } catch (err) {
      if (!mountedRef.current) return;
      showNotification(resolveApiError(err), 'error');
    } finally {
      if (mountedRef.current) {
        setActingRunId('');
        setOperationArchive(null);
      }
    }
  };

  const openDeleteConfirmation = (run: UsageArchiveRunSummary) => {
    setDeleteAcknowledged(false);
    setDeleteTargetRun(run);
    setSearchParams((prev: URLSearchParams) => {
      const next = new URLSearchParams(prev);
      next.set('run', run.id);
      return next;
    });
  };

  const handleConfirmDeleteRun = async () => {
    if (!deleteTargetRun || !deleteAcknowledged) return;
    const runId = deleteTargetRun.id;
    setDeleteTargetRun(null);
    setDeleteAcknowledged(false);
    await executeRunAction(runId, 'delete');
  };

  const handleConfirmCancelRun = async () => {
    if (!cancelTargetRun) return;
    const runId = cancelTargetRun.id;
    setCancelTargetRun(null);
    await executeRunAction(runId, 'cancel');
  };

  const handleCopyText = async (text: string, message: string) => {
    try {
      if (!navigator.clipboard?.writeText) return;
      await navigator.clipboard.writeText(text);
      showNotification(message, 'success');
    } catch {
      // Clipboard unavailable in restricted contexts
    }
  };

  const filteredRuns = useMemo(
    () => filterArchiveRunsByQuery(runs, searchQuery),
    [runs, searchQuery]
  );

  const currentPage = cursorStack.length + 1;
  const totalFromCounts = sumStatusCounts(statusCounts);
  const totalCount = totalFromCounts ?? runsTotal ?? runs.length;
  const totalPages = nextCursor ? currentPage + 1 : currentPage;
  const startItem = filteredRuns.length === 0 ? 0 : (currentPage - 1) * pageSize + 1;
  const endItem = (currentPage - 1) * pageSize + filteredRuns.length;

  const handleCursorPageChange = (nextPage: number) => {
    if (nextPage > currentPage && nextCursor) {
      setCursorStack((prev) => [...prev, currentCursor ?? '']);
      setCurrentCursor(nextCursor);
    } else if (nextPage < currentPage && cursorStack.length > 0) {
      const previous = cursorStack[cursorStack.length - 1];
      setCursorStack((prev) => prev.slice(0, -1));
      setCurrentCursor(previous || undefined);
    }
  };

  if (featureAvailability.checking) {
    return <SplashScreen variant="content" />;
  }

  if (!featureAvailability.managerServiceAvailable || unsupported) {
    return (
      <div
        className={styles.page}
        data-testid={
          unsupported ? 'usage-maintenance-api-unsupported' : 'usage-maintenance-unsupported'
        }
      >
        <section className={styles.panel}>
          <EmptyState
            variant="canvas"
            title={t('usage_maintenance.title')}
            description={t('usage_maintenance.unsupported')}
            action={
              <Link to="/system" className="btn btn-primary btn-sm">
                {t('nav.system_config', { defaultValue: 'System Config' })}
              </Link>
            }
          />
        </section>
      </div>
    );
  }

  const storageTotalBytes =
    maintenance?.storage?.total_bytes ?? serviceStatus?.database?.totalBytes ?? 0;
  const reclaimableBytes = maintenance?.storage?.reclaimable_bytes ?? 0;

  const summaryCards: MetricSummaryCardProps[] = [
    {
      label: t('usage_maintenance.raw_events'),
      value: maintenance ? maintenance.raw_event_count.toLocaleString(i18n.language) : '—',
      icon: 'calls',
      accent: 'blue',
      meta: t('usage_maintenance.workspace_archived_subset', {
        count: maintenance
          ? maintenance.raw_archived_event_count.toLocaleString(i18n.language)
          : '—',
      }),
    },
    {
      label: t('usage_maintenance.deleted_events'),
      value: maintenance ? maintenance.raw_deleted_event_count.toLocaleString(i18n.language) : '—',
      icon: 'delete',
      accent: 'amber',
      meta: t('usage_maintenance.workspace_deleted_hint'),
    },
    {
      label: t('usage_maintenance.sqlite_total'),
      value: storageTotalBytes > 0 ? formatFileSize(storageTotalBytes) : '—',
      icon: 'model',
      accent: 'indigo',
      meta: t('usage_maintenance.sqlite_total_hint'),
    },
    {
      label: t('usage_maintenance.reclaimable'),
      value: maintenance ? formatFileSize(reclaimableBytes) : '—',
      icon: 'cache',
      accent: 'teal',
      meta:
        reclaimableBytes > 0
          ? t('usage_maintenance.sqlite_reclaimable_hint', {
              size: formatFileSize(reclaimableBytes),
            })
          : t('usage_maintenance.advanced_sqlite_note_short', {
              defaultValue: 'Offline compaction reclaims free pages',
            }),
    },
  ];

  const mainTabs: SegmentedTabItem<UsageMaintenanceTab>[] = [
    { id: 'archives', label: t('usage_maintenance.archive_management') },
    { id: 'transfer', label: t('usage_maintenance.workspace_transfer') },
    { id: 'diagnostics', label: t('usage_maintenance.diagnostics_title') },
  ];

  const sourceTabs: SegmentedTabItem<ArchiveHistorySource>[] = [
    { id: 'all', label: t('usage_maintenance.source_all') },
    { id: 'manual', label: t('usage_maintenance.run_mode_manual') },
    { id: 'retention', label: t('usage_maintenance.run_mode_retention') },
  ];

  const statusOptions = ARCHIVE_STATUS_FILTERS.map((status) => {
    const baseLabel =
      status === 'all'
        ? t('usage_maintenance.history_filter_all')
        : t(`usage_maintenance.run_status_${status}`, { defaultValue: status });
    const countLabel =
      statusCounts !== undefined
        ? ` (${status === 'all' ? (totalFromCounts ?? 0) : (statusCounts[status] ?? 0)})`
        : '';
    return {
      value: status,
      label: `${baseLabel}${countLabel}`,
    };
  });

  const renderRunActions = (run: UsageArchiveRunSummary, includeDetails = true) => {
    const primaryAction = getArchiveRunAction(run.status);
    const cancellable = isArchiveRunCancellable(run);
    const rowBusy = actingRunId === run.id;
    const deleteBlocked =
      primaryAction === 'delete' && !maintenance?.readiness.archive_delete_enabled;

    return (
      <div className={styles.rowActions}>
        {primaryAction === 'verify' ? (
          <Button
            variant="secondary"
            size="xs"
            loading={rowBusy}
            disabled={Boolean(actingRunId)}
            onClick={() => void executeRunAction(run.id, 'verify')}
          >
            {t('usage_maintenance.guided_step_verify')}
          </Button>
        ) : null}
        {primaryAction === 'delete' ? (
          <Button
            variant="danger"
            size="xs"
            loading={rowBusy}
            disabled={Boolean(actingRunId) || deleteBlocked}
            onClick={() => openDeleteConfirmation(run)}
          >
            <IconTrash2 size={14} aria-hidden="true" />
            <span>{t('usage_maintenance.guided_step_delete')}</span>
          </Button>
        ) : null}
        {primaryAction === 'resume' ? (
          <Button
            variant="secondary"
            size="xs"
            loading={rowBusy}
            disabled={Boolean(actingRunId)}
            onClick={() => void executeRunAction(run.id, 'resume')}
          >
            {t('usage_maintenance.action_resume')}
          </Button>
        ) : null}
        {cancellable ? (
          <Button
            variant="ghost"
            size="xs"
            loading={rowBusy}
            disabled={Boolean(actingRunId)}
            onClick={() => setCancelTargetRun(run)}
          >
            {t('common.cancel')}
          </Button>
        ) : null}
        {includeDetails ? (
          <Button variant="ghost" size="xs" onClick={() => openRunDetail(run.id)}>
            {t('usage_maintenance.details')}
          </Button>
        ) : null}
      </div>
    );
  };

  const pendingContinuationRun = [maintenance?.active_run, ...runs].find(
    (run): run is UsageArchiveRunSummary =>
      Boolean(run && run.status !== 'verified' && getArchiveRunAction(run.status))
  );

  const freshestDetail = pickFreshestArchiveStatus(
    detailStatus?.run.id === selectedRunId ? detailStatus : null,
    operationArchive?.run.id === selectedRunId ? operationArchive : null
  );

  const showFloatingProgress = Boolean(
    activeBackgroundRun && (!selectedRunId || selectedRunId !== activeBackgroundRun.id)
  );
  const activeRunProgress = activeBackgroundRun
    ? resolveArchiveProgressPresentation(activeBackgroundRun)
    : null;

  const deleteBlocked = !maintenance?.readiness.archive_delete_enabled;

  return (
    <div className={styles.page} data-testid="usage-maintenance-page">
      <PageControlBar
        ariaLabel={t('usage_maintenance.title')}
        leading={
          <div className={styles.leadingRow}>
            <SegmentedTabs<UsageMaintenanceTab>
              activeTab={activeTab}
              onChange={setActiveTab}
              items={mainTabs}
              ariaLabel={t('usage_maintenance.title')}
              idBase="usage-maintenance-tabs"
            />
            {maintenance?.active_lock ? (
              <span className={`${styles.statusPill} ${styles.statusWarning}`}>
                <span className={styles.statusDot} aria-hidden="true" />
                <span>
                  {t('usage_maintenance.diagnostics_lock_title')}:{' '}
                  {maintenance.active_lock.operation}
                </span>
              </span>
            ) : archiveReadinessBlocked ? (
              <span className={`${styles.statusPill} ${styles.statusWarning}`}>
                <span className={styles.statusDot} aria-hidden="true" />
                <span>{t('usage_maintenance.migration_status_pending')}</span>
              </span>
            ) : null}
          </div>
        }
        actions={
          <div className={styles.controlBarActions}>
            <Button
              variant="secondary"
              size="sm"
              loading={refreshing || loading}
              onClick={() => void loadWorkspace(true)}
              data-testid="usage-maintenance-refresh-btn"
            >
              <IconRefreshCw size={15} aria-hidden="true" />
              <span>{t('common.refresh')}</span>
            </Button>
            <Button
              variant="primary"
              size="sm"
              disabled={loading || createWorking}
              onClick={openCreateDrawer}
              data-testid="usage-maintenance-open-create-btn"
            >
              <IconPlus size={15} aria-hidden="true" />
              <span>{t('usage_maintenance.new_archive')}</span>
            </Button>
          </div>
        }
      />

      {loadError ? (
        <div className={`${styles.alertBanner} ${styles.alertDanger}`} role="alert">
          <span>{loadError}</span>
          <Button variant="secondary" size="xs" onClick={() => void loadWorkspace(true)}>
            {t('common.refresh')}
          </Button>
        </div>
      ) : null}

      <MetricSummaryGrid cards={summaryCards} columns={4} />

      {postDeleteNoticeVisible ? (
        <div
          className={`${styles.alertBanner} ${styles.alertInfo}`}
          role="status"
          data-testid="usage-post-delete-notice"
        >
          <div className={styles.alertStack}>
            <strong className={styles.alertHeading}>
              <IconCheck size={15} aria-hidden="true" />
              <span>{t('usage_maintenance.cleanup_complete_title')}</span>
            </strong>
            <span>
              {reclaimableBytes > 0
                ? t('usage_maintenance.cleanup_complete_reclaimable', {
                    size: formatFileSize(reclaimableBytes),
                  })
                : t('usage_maintenance.cleanup_complete_no_reclaimable')}
            </span>
          </div>
          <div className={styles.rowActions}>
            <Button size="xs" variant="secondary" onClick={() => setActiveTab('diagnostics')}>
              {t('usage_maintenance.cleanup_complete_open_advanced')}
            </Button>
            <Button
              size="xs"
              variant="ghost"
              onClick={() => setPostDeleteNoticeVisible(false)}
            >
              {t('common.close')}
            </Button>
          </div>
        </div>
      ) : null}

      {activeTab === 'archives' ? (
        <>
          {pendingContinuationRun && selectedRunId !== pendingContinuationRun.id ? (
            <div
              className={`${styles.alertBanner} ${
                pendingContinuationRun.status === 'failed' || pendingContinuationRun.has_error
                  ? styles.alertDanger
                  : styles.alertWarn
              }`}
              data-testid="usage-maintenance-continuation-banner"
            >
              <div className={styles.alertHeading}>
                {pendingContinuationRun.status === 'failed' || pendingContinuationRun.has_error ? (
                  <IconTriangleAlert size={15} aria-hidden="true" />
                ) : (
                  <IconClock size={15} aria-hidden="true" />
                )}
                <span>
                  {t(`usage_maintenance.run_status_${pendingContinuationRun.status}`, {
                    defaultValue: pendingContinuationRun.status,
                  })}{' '}
                  · #{pendingContinuationRun.id.slice(0, 8)} ·{' '}
                  {formatTimestamp(pendingContinuationRun.created_at_ms)}
                </span>
              </div>
              <Button
                size="xs"
                variant={
                  pendingContinuationRun.status === 'failed' || pendingContinuationRun.has_error
                    ? 'danger'
                    : 'secondary'
                }
                onClick={() => openRunDetail(pendingContinuationRun.id)}
              >
                {t('usage_maintenance.workspace_continue_record')}
              </Button>
            </div>
          ) : null}

          <section className={styles.panel} data-testid="usage-maintenance-runs-card">
            <div className={styles.panelHeader}>
              <div className={styles.panelHeaderCopy}>
                <h2 className={styles.panelTitle}>{t('usage_maintenance.archive_records')}</h2>
              </div>
              <div className={styles.panelHeaderActions}>
                <SegmentedTabs<ArchiveHistorySource>
                  variant="joined"
                  responsiveFullWidth={false}
                  activeTab={sourceFilter}
                  onChange={(next) => {
                    setSourceFilter(next);
                    setCurrentCursor(undefined);
                    setCursorStack([]);
                  }}
                  items={sourceTabs}
                  ariaLabel={t('usage_maintenance.source_filter')}
                  idBase="usage-maintenance-source-tabs"
                />
                <div className={styles.statusSelectWrap}>
                  <Select
                    value={statusFilter}
                    options={statusOptions}
                    onChange={(value) => {
                      setStatusFilter(value as ArchiveHistoryFilter);
                      setCurrentCursor(undefined);
                      setCursorStack([]);
                    }}
                    ariaLabel={t('usage_maintenance.status_title')}
                    fullWidth={false}
                  />
                </div>
                <div className={styles.searchWrap}>
                  <IconSearch size={14} className={styles.searchIcon} aria-hidden="true" />
                  <input
                    type="search"
                    className={styles.searchInput}
                    placeholder={t('usage_maintenance.search_runs_placeholder', {
                      defaultValue: 'Search run ID or status…',
                    })}
                    value={searchQuery}
                    onChange={(event) => setSearchQuery(event.target.value)}
                    aria-label={t('usage_maintenance.search_runs_placeholder', {
                      defaultValue: 'Search run ID or status…',
                    })}
                    data-testid="usage-maintenance-search-input"
                  />
                  {searchQuery ? (
                    <button
                      type="button"
                      className={styles.searchClearBtn}
                      onClick={() => setSearchQuery('')}
                      aria-label={t('common.clear', { defaultValue: 'Clear' })}
                    >
                      <IconX size={12} />
                    </button>
                  ) : null}
                </div>
              </div>
            </div>

            {filteredRuns.length === 0 ? (
              <EmptyState
                variant="canvas"
                title={
                  runs.length > 0
                    ? t('usage_maintenance.no_matching_runs', {
                        defaultValue: 'No matching archive records found',
                      })
                    : t('usage_maintenance.no_archive_records')
                }
                description={t('usage_maintenance.workspace_cleanup_hint')}
                action={
                  runs.length > 0 ? (
                    <Button variant="secondary" size="sm" onClick={() => setSearchQuery('')}>
                      {t('common.clear', { defaultValue: 'Clear filter' })}
                    </Button>
                  ) : (
                    <Button variant="primary" size="sm" onClick={openCreateDrawer}>
                      <IconPlus size={14} aria-hidden="true" />
                      <span>{t('usage_maintenance.new_archive')}</span>
                    </Button>
                  )
                }
              />
            ) : (
              <>
                <div className={styles.tableWrap}>
                  <table className={styles.table}>
                    <thead>
                      <tr>
                        <th>{t('usage_maintenance.created_at')}</th>
                        <th>{t('usage_maintenance.technical_mode')}</th>
                        <th>
                          <span className={styles.thWithHelp}>
                            <span>{t('usage_maintenance.cutoff_short')}</span>
                            <span
                              className={styles.helpIcon}
                              title={t('usage_maintenance.resolved_cutoff_hint')}
                              aria-label={t('usage_maintenance.resolved_cutoff_hint')}
                            >
                              <IconCircleHelp size={13} />
                            </span>
                          </span>
                        </th>
                        <th>{t('usage_maintenance.workspace_record_count')}</th>
                        <th>{t('usage_maintenance.progress_title')}</th>
                        <th>{t('usage_maintenance.technical_status')}</th>
                        <th className={styles.actionsCol}>
                          {t('usage_maintenance.record_actions')}
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {filteredRuns.map((run) => {
                        const progress = resolveArchiveProgressPresentation(run);
                        const stage = getArchiveRunPresentationStage(run);
                        const barWidth =
                          progress && progress.percent === null
                            ? undefined
                            : progress?.percent !== undefined && progress?.percent !== null
                              ? `${progress.percent}%`
                              : run.status === 'completed' ||
                                  run.status === 'verified' ||
                                  run.status === 'archived'
                                ? '100%'
                                : run.event_count > 0 && run.archived_event_count > 0
                                  ? `${Math.min(
                                      100,
                                      Math.round((run.archived_event_count / run.event_count) * 100)
                                    )}%`
                                  : '0%';
                        const fillToneClass =
                          run.status === 'completed' || run.status === 'verified'
                            ? styles.progressFillSuccess
                            : run.status === 'failed' || run.status === 'cancelled'
                              ? styles.progressFillDanger
                              : '';
                        return (
                          <tr
                            key={run.id}
                            data-testid={`usage-maintenance-run-row-${run.id}`}
                            className={styles.clickableRow}
                            onClick={(event) => {
                              const target = event.target as HTMLElement | null;
                              if (
                                typeof target?.closest === 'function' &&
                                target.closest('button, a, input')
                              ) {
                                return;
                              }
                              openRunDetail(run.id);
                            }}
                          >
                            <td>
                              <div className={styles.identityCell}>
                                <button
                                  type="button"
                                  className={styles.runIdBtn}
                                  title={run.id}
                                  onClick={() => openRunDetail(run.id)}
                                >
                                  {formatTimestamp(run.created_at_ms)}
                                </button>
                                <span className={styles.identitySubRow} title={run.id}>
                                  <span>#{run.id.slice(0, 8)}</span>
                                  <button
                                    type="button"
                                    className={styles.iconActionBtn}
                                    title={`${t('common.copy')} ID`}
                                    aria-label={`${t('common.copy')} ${run.id}`}
                                    onClick={(event) => {
                                      event.stopPropagation();
                                      void handleCopyText(
                                        run.id,
                                        t('usage_maintenance.run_id_copied', {
                                          defaultValue: 'Task ID copied to clipboard',
                                        })
                                      );
                                    }}
                                  >
                                    <IconCopy size={11} />
                                  </button>
                                </span>
                              </div>
                            </td>
                            <td>
                              <span className={styles.modeChip}>
                                {t(`usage_maintenance.run_mode_${run.mode}`, {
                                  defaultValue: run.mode,
                                })}
                              </span>
                            </td>
                            <td className={styles.mono}>{formatTimestamp(run.cutoff_timestamp_ms)}</td>
                            <td>
                              <div className={styles.identityCell}>
                                <strong className={styles.mono}>
                                  {run.event_count.toLocaleString(i18n.language)}{' '}
                                  {t('usage_maintenance.events_suffix')}
                                </strong>
                                {run.deleted_event_count >= run.event_count && run.event_count > 0 ? (
                                  <span
                                    className={`${styles.semanticMeta} ${styles.semanticMetaSuccess}`}
                                  >
                                    {t('usage_maintenance.meta_cleaned_all')}
                                  </span>
                                ) : run.deleted_event_count === 0 ? (
                                  <span className={styles.semanticMeta}>
                                    {t('usage_maintenance.meta_archived_only')}
                                  </span>
                                ) : (
                                  <span
                                    className={`${styles.semanticMeta} ${styles.semanticMetaWarning}`}
                                  >
                                    {t('usage_maintenance.meta_partially_cleaned', {
                                      deleted: run.deleted_event_count.toLocaleString(i18n.language),
                                      total: run.event_count.toLocaleString(i18n.language),
                                    })}
                                  </span>
                                )}
                              </div>
                            </td>
                            <td>
                              <div className={styles.progressBlock}>
                                <div className={styles.progressTrack}>
                                  <div
                                    className={`${styles.progressFill} ${fillToneClass} ${
                                      progress && progress.percent === null
                                        ? styles.progressFillIndeterminate
                                        : ''
                                    }`}
                                    style={barWidth !== undefined ? { width: barWidth } : undefined}
                                  />
                                </div>
                                <span className={styles.progressLabel}>
                                  {progress
                                    ? t(progress.labelKey)
                                    : `${run.archived_event_count.toLocaleString()} / ${run.event_count.toLocaleString()}`}
                                </span>
                              </div>
                            </td>
                            <td>
                              <div className={styles.statusStack}>
                                <span
                                  className={`${styles.statusPill} ${getRunStatusToneClass(run.status)}`}
                                  title={stage}
                                >
                                  <span className={styles.statusDot} aria-hidden="true" />
                                  {t(`usage_maintenance.run_status_${run.status}`, {
                                    defaultValue: run.status,
                                  })}
                                </span>
                                {run.status === 'verified' || run.status === 'completed' ? (
                                  <span className={styles.statusSub}>
                                    {t(
                                      run.status === 'verified'
                                        ? 'usage_maintenance.online_retained'
                                        : 'usage_maintenance.archive_retained'
                                    )}
                                  </span>
                                ) : null}
                              </div>
                            </td>
                            <td className={styles.actionsCol}>{renderRunActions(run)}</td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>

                <div className={styles.mobileCardList}>
                  {filteredRuns.map((run) => (
                    <div key={run.id} className={styles.mobileRunRow}>
                      <div className={styles.mobileRunHeader}>
                        <button
                          type="button"
                          className={styles.runIdBtn}
                          onClick={() => openRunDetail(run.id)}
                        >
                          {formatTimestamp(run.created_at_ms)} · #{run.id.slice(0, 8)}
                        </button>
                        <span
                          className={`${styles.statusPill} ${getRunStatusToneClass(run.status)}`}
                        >
                          <span className={styles.statusDot} aria-hidden="true" />
                          {t(`usage_maintenance.run_status_${run.status}`, {
                            defaultValue: run.status,
                          })}
                        </span>
                      </div>
                      <div className={styles.mobileRunMeta}>
                        <span>{formatTimestamp(run.cutoff_timestamp_ms)}</span>
                        <span>
                          {run.archived_event_count.toLocaleString()} /{' '}
                          {run.event_count.toLocaleString()}
                        </span>
                      </div>
                      {renderRunActions(run)}
                    </div>
                  ))}
                </div>
              </>
            )}

            <PaginationFooter
              variant="flush"
              currentPage={currentPage}
              totalPages={totalPages}
              disabled={!loaded || loading}
              infoText={
                totalCount > 0
                  ? `${startItem}-${endItem} / ${totalCount}`
                  : t('usage_maintenance.records_on_page', { count: filteredRuns.length })
              }
              pageSize={pageSize}
              pageSizeOptions={PAGE_SIZE_OPTIONS}
              pageSizeLabel={t('monitoring.page_size_label')}
              pageSizeTitle={t('monitoring.page_size_label')}
              pageSizeAriaLabel={t('monitoring.page_size_label')}
              formatPageSizeOption={(size) => t('monitoring.page_size_option', { count: size })}
              prevLabel={t('usage_maintenance.pagination_prev')}
              nextLabel={t('usage_maintenance.pagination_next')}
              onPageChange={handleCursorPageChange}
              onPageSizeChange={(nextSize) => {
                setPageSize(nextSize);
                setCurrentCursor(undefined);
                setCursorStack([]);
              }}
            />
          </section>
        </>
      ) : null}

      {activeTab === 'transfer' ? (
        <UsageMaintenanceTransferSection
          managerBase={managerBase}
          managementKey={managementKey}
          onUsageChanged={() => void loadWorkspace(true)}
        />
      ) : null}

      {activeTab === 'diagnostics' ? (
        <UsageMaintenanceDiagnosticsSection
          maintenance={maintenance}
          serviceStatus={serviceStatus}
          onOpenRun={openRunDetail}
          onCopyText={(text, message) => void handleCopyText(text, message)}
        />
      ) : null}

      <Drawer
        open={createDrawerOpen}
        onClose={closeCreateDrawer}
        width="min(640px, 100vw)"
        title={
          createConfirmStep
            ? t('usage_maintenance.archive_prepare_confirm_title')
            : t('usage_maintenance.new_archive')
        }
        footer={
          <div className={styles.rowActions}>
            {createConfirmStep ? (
              <>
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={createWorking}
                  onClick={() => setCreateConfirmStep(false)}
                >
                  {t('common.back')}
                </Button>
                <Button
                  variant="primary"
                  size="sm"
                  loading={createWorking}
                  disabled={hasActiveMutex || archiveReadinessBlocked}
                  onClick={() => void handleExecuteCreateArchive()}
                  data-testid="usage-maintenance-create-confirm-btn"
                >
                  {t('usage_maintenance.archive_prepare_confirm_button')}
                </Button>
              </>
            ) : (
              <>
                <Button variant="secondary" size="sm" onClick={closeCreateDrawer}>
                  {t('common.cancel')}
                </Button>
                {createWorking ? (
                  <Button variant="secondary" size="sm" onClick={handleStopGuidedCreate}>
                    {t('usage_maintenance.archive_prepare_stop')}
                  </Button>
                ) : null}
                <Button
                  variant="primary"
                  size="sm"
                  disabled={
                    createWorking ||
                    !preview ||
                    preview.event_count <= 0 ||
                    previewLoading ||
                    Boolean(previewError) ||
                    hasActiveMutex ||
                    archiveReadinessBlocked
                  }
                  onClick={() => setCreateConfirmStep(true)}
                  data-testid="usage-maintenance-create-btn"
                >
                  {t('common.next')}
                </Button>
              </>
            )}
          </div>
        }
      >
        {maintenance ? (
          <UsageMaintenanceCreateDrawerContent
            maintenance={maintenance}
            intent={createIntent}
            retentionSelection={retentionSelection}
            customCutoff={customCutoff}
            referenceNowMS={referenceNowMS}
            resolvedCutoffMS={resolvedCutoffMS}
            recommendedDays={recommendedDays}
            rawRange={rawRange}
            preview={preview}
            previewLoading={previewLoading}
            previewError={previewError}
            working={createWorking}
            hasActiveMutex={hasActiveMutex}
            archiveReadinessBlocked={archiveReadinessBlocked}
            confirmStep={createConfirmStep}
            onChangeIntent={setCreateIntent}
            onSelectRetention={(next) => {
              setReferenceNowMS(Date.now());
              setRetentionSelection(next);
            }}
            onChangeCustomCutoff={(value) => {
              setReferenceNowMS(Date.now());
              setCustomCutoff(value);
            }}
            onRetryPreview={() => setPreviewRefreshToken((value) => value + 1)}
            onViewHistory={closeCreateDrawer}
          />
        ) : (
          <SplashScreen variant="content" />
        )}
      </Drawer>

      <Drawer
        open={Boolean(selectedRunId)}
        onClose={closeRunDrawer}
        width={deleteTargetRun ? 'min(680px, 100vw)' : 'min(600px, 100vw)'}
        title={
          deleteTargetRun
            ? t('usage_maintenance.delete_confirm_title')
            : t('usage_maintenance.run_detail_title', {
                defaultValue: selectedRunId
                  ? t('usage_maintenance.archive_task_label', { runId: selectedRunId })
                  : undefined,
              })
        }
        footer={
          deleteTargetRun ? (
            <div className={styles.rowActions}>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => {
                  setDeleteTargetRun(null);
                  setDeleteAcknowledged(false);
                }}
              >
                {t('common.back')}
              </Button>
              <Button
                variant="danger"
                size="sm"
                loading={actingRunId === deleteTargetRun.id}
                disabled={deleteBlocked || !deleteAcknowledged || Boolean(actingRunId)}
                onClick={() => void handleConfirmDeleteRun()}
                data-testid="usage-maintenance-confirm-delete-btn"
              >
                {t('usage_maintenance.delete_confirm_button', {
                  count: Math.max(
                    0,
                    deleteTargetRun.event_count - deleteTargetRun.deleted_event_count
                  ).toLocaleString(i18n.language),
                })}
              </Button>
            </div>
          ) : freshestDetail ? (
            <div className={styles.rowActions}>
              <Button
                variant="ghost"
                size="sm"
                loading={detailLoading}
                onClick={() => {
                  if (selectedRunId) void refreshRunDetail(selectedRunId);
                }}
              >
                <IconRefreshCw size={14} aria-hidden="true" />
                <span>{t('common.refresh')}</span>
              </Button>
              {renderRunActions(freshestDetail.run, false)}
              <Button variant="secondary" size="sm" onClick={closeRunDrawer}>
                {isArchiveRunActiveStatus(freshestDetail.run.status)
                  ? t('usage_maintenance.minimize')
                  : freshestDetail.run.status === 'completed' ||
                      freshestDetail.run.status === 'verified' ||
                      freshestDetail.run.status === 'cancelled'
                    ? t('usage_maintenance.done')
                    : t('common.close')}
              </Button>
            </div>
          ) : (
            <div className={styles.rowActions}>
              <Button variant="secondary" size="sm" onClick={closeRunDrawer}>
                {t('common.close')}
              </Button>
            </div>
          )
        }
      >
        {deleteTargetRun ? (
          <UsageMaintenanceDeleteConfirmationView
            run={deleteTargetRun}
            deletionEnabled={Boolean(maintenance?.readiness.archive_delete_enabled)}
            acknowledged={deleteAcknowledged}
            onToggleAcknowledged={setDeleteAcknowledged}
          />
        ) : detailLoading && !freshestDetail ? (
          <SplashScreen variant="content" />
        ) : detailError && !freshestDetail ? (
          <EmptyState
            variant="inline"
            title={t('usage_maintenance.error_title', { defaultValue: 'Failed to load details' })}
            description={detailError}
          />
        ) : freshestDetail ? (
          <UsageMaintenanceRunDrawerContent
            archive={freshestDetail}
            maintenance={maintenance}
            intent={createIntent}
            working={actingRunId === freshestDetail.run.id || createWorking}
            actionError={detailError || null}
            statusToneClass={getRunStatusToneClass}
            renderActions={(run) => renderRunActions(run, false)}
            onCopyText={(text, message) => void handleCopyText(text, message)}
          />
        ) : null}
      </Drawer>

      <Modal
        open={Boolean(cancelTargetRun)}
        title={t('usage_maintenance.cancel_confirm_title')}
        onClose={() => setCancelTargetRun(null)}
        footer={
          <div className={styles.rowActions}>
            <Button variant="secondary" size="sm" onClick={() => setCancelTargetRun(null)}>
              {t('common.back')}
            </Button>
            <Button
              variant="danger"
              size="sm"
              loading={Boolean(cancelTargetRun && actingRunId === cancelTargetRun.id)}
              onClick={() => void handleConfirmCancelRun()}
              data-testid="usage-maintenance-confirm-cancel-btn"
            >
              {t('usage_maintenance.cancel_confirm_button')}
            </Button>
          </div>
        }
      >
        <div className={styles.modalBody}>
          <p>{t('usage_maintenance.cancel_confirm_message')}</p>
        </div>
      </Modal>

      {showFloatingProgress && activeBackgroundRun ? (
        <button
          type="button"
          className={styles.floatingProgress}
          onClick={() => openRunDetail(activeBackgroundRun.id)}
          title={t('usage_maintenance.view_active_progress')}
          data-testid="usage-maintenance-floating-progress"
        >
          <span className={styles.floatingProgressDot} aria-hidden="true" />
          <span className={styles.floatingProgressContent}>
            <span>
              {activeRunProgress
                ? t(activeRunProgress.labelKey)
                : t(`usage_maintenance.run_status_${activeBackgroundRun.status}`, {
                    defaultValue: activeBackgroundRun.status,
                  })}
            </span>
            {activeRunProgress?.percent !== null && activeRunProgress?.percent !== undefined ? (
              <span className={styles.mono}>{activeRunProgress.percent.toFixed(1)}%</span>
            ) : null}
          </span>
          <IconChevronRight size={14} aria-hidden="true" />
        </button>
      ) : null}
    </div>
  );
}
