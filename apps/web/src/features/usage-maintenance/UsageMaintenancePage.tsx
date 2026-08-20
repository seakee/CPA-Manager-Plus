import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import {
  usageServiceApi,
  type UsageArchiveList,
  type UsageArchivePreview,
  type UsageArchiveResumeStage,
  type UsageArchiveRunStatus,
  type UsageArchiveRunSummary,
  type UsageArchiveSegmentSummary,
  type UsageArchiveStatus,
  type UsageMaintenanceStatus,
} from '@/services/api/usageService';
import { useAuthStore, useNotificationStore } from '@/stores';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { formatDateTime, formatFileSize } from '@/utils/format';
import {
  getArchiveRunPresentationStage,
  archiveHistoryFilterStatus,
  recommendRetentionDays,
  resolveRawEventRange,
  resolveRetentionCutoff,
  retentionPresetDays,
  toLocalDateTimeValue,
  type ArchiveHistoryFilter,
  type ArchiveRunAction,
  type RetentionSelection,
  type UsageMaintenanceView,
} from './usageMaintenanceModel';
import {
  UsageArchiveHistoryView,
  UsageArchiveRunView,
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

const activeRefreshIntervalMs = 5_000;
const defaultRetentionDays = 30 as const;
const archiveProgressStatuses = new Set(['archiving', 'verifying', 'deleting']);
const archiveStatusTranslationValues = new Set([
  'previewed',
  'archiving',
  'archived',
  'verifying',
  'verified',
  'deleting',
  'completed',
  'failed',
  'cancelled',
]);
const archiveModeTranslationValues = new Set(['manual', 'retention']);
const migrationStatusTranslationValues = new Set([
  'discovering',
  'pending',
  'running',
  'applying',
  'clearing',
  'completed',
  'failed',
]);
const aggregateStatusTranslationValues = new Set([
  'pending',
  'backfilling',
  'catching_up',
  'clearing',
  'ready',
  'failed',
]);

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

const statusAction = (status: UsageArchiveRunStatus): 'resume' | 'verify' | 'delete' | null => {
  if (
    status === 'previewed' ||
    status === 'archiving' ||
    status === 'verifying' ||
    status === 'deleting' ||
    status === 'failed'
  ) {
    return 'resume';
  }
  if (status === 'archived') return 'verify';
  if (status === 'verified') return 'delete';
  return null;
};

const actionIsDestructive = (run: UsageArchiveRunSummary, action: 'resume' | 'verify' | 'delete') =>
  action === 'delete' ||
  (action === 'resume' &&
    (run.status === 'deleting' || (run.status === 'failed' && run.resume_status === 'deleting')));

const actionRequiresMigrationReady = (
  run: UsageArchiveRunSummary,
  action: 'resume' | 'verify' | 'delete'
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
  action: 'resume' | 'verify' | 'delete'
): ReadonlySet<string> => {
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

export function UsageMaintenancePage() {
  const { t, i18n } = useTranslation();
  const availability = usePanelFeatureAvailability();
  const managementKey = useAuthStore((state) => state.managementKey);
  const { showConfirmation, showNotification } = useNotificationStore();
  const serviceBase = availability.managerServiceBase;
  const [maintenance, setMaintenance] = useState<UsageMaintenanceStatus | null>(null);
  const [archives, setArchives] = useState<UsageArchiveRunSummary[]>([]);
  const [view, setView] = useState<UsageMaintenanceView>('overview');
  const [historyFilter, setHistoryFilter] = useState<ArchiveHistoryFilter>('all');
  const [historyCursor, setHistoryCursor] = useState<string | undefined>();
  const [historyCursorStack, setHistoryCursorStack] = useState<string[]>([]);
  const [historyList, setHistoryList] = useState<UsageArchiveList>({ runs: [] });
  const [historyLoading, setHistoryLoading] = useState(false);
  const [selectedRunId, setSelectedRunId] = useState<string | null>(null);
  const [selectedArchive, setSelectedArchive] = useState<UsageArchiveStatus | null>(null);
  const [selectedArchiveLoading, setSelectedArchiveLoading] = useState(false);
  const [selectedArchiveRefreshToken, setSelectedArchiveRefreshToken] = useState(0);
  const [preview, setPreview] = useState<UsageArchivePreview | null>(null);
  const [retentionSelection, setRetentionSelection] =
    useState<RetentionSelection>(defaultRetentionDays);
  const [referenceNowMS, setReferenceNowMS] = useState(() => Date.now());
  const [customCutoff, setCustomCutoff] = useState(() =>
    toLocalDateTimeValue(Date.now() - defaultRetentionDays * 24 * 60 * 60 * 1000)
  );
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [guidedArchiveStage, setGuidedArchiveStage] = useState<GuidedArchiveStage>('idle');
  const [guidedArchiveRunId, setGuidedArchiveRunId] = useState<string | null>(null);
  const [previewRefreshToken, setPreviewRefreshToken] = useState(0);
  const [loading, setLoading] = useState(true);
  const [working, setWorking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [unsupported, setUnsupported] = useState(false);
  const mountedRef = useRef(false);
  const loadControllerRef = useRef<AbortController | null>(null);
  const loadGenerationRef = useRef(0);
  const capabilityContextRef = useRef<{ serviceBase: string; managementKey?: string } | null>(null);
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
    if (resetWorking && mountedRef.current) setWorking(false);
  }, []);

  const beginWorking = useCallback(
    (capturedServiceBase: string, capturedManagementKey?: string): OperationToken | null => {
      if (!mountedRef.current) return null;
      const currentContext = operationContextRef.current;
      if (
        currentContext.serviceBase !== capturedServiceBase ||
        currentContext.managementKey !== capturedManagementKey
      ) {
        return null;
      }
      operationControllerRef.current?.abort();
      const controller = new AbortController();
      const generation = ++operationGenerationRef.current;
      operationControllerRef.current = controller;
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
      setWorking(false);
    },
    [operationIsCurrent]
  );

  useLayoutEffect(() => {
    contextGenerationRef.current += 1;
    operationContextRef.current = { serviceBase, managementKey };
    capabilityContextRef.current = null;
    invalidateOperation(true);
    invalidatePreview(false);
    const nextReferenceNowMS = Date.now();
    setReferenceNowMS(nextReferenceNowMS);
    setRetentionSelection(defaultRetentionDays);
    setCustomCutoff(
      toLocalDateTimeValue(nextReferenceNowMS - defaultRetentionDays * 24 * 60 * 60 * 1000)
    );
    setMaintenance(null);
    setArchives([]);
    setView('overview');
    setHistoryFilter('all');
    setHistoryCursor(undefined);
    setHistoryCursorStack([]);
    setHistoryList({ runs: [] });
    setHistoryLoading(false);
    setSelectedRunId(null);
    setSelectedArchive(null);
    setSelectedArchiveLoading(false);
    setSelectedArchiveRefreshToken(0);
    setPreview(null);
    setPreviewLoading(false);
    setPreviewError(null);
    setGuidedArchiveStage('idle');
    setGuidedArchiveRunId(null);
    setError(null);
    setUnsupported(false);
    setLoading(Boolean(serviceBase));
    return () => {
      invalidateOperation(false);
      invalidatePreview(false);
    };
  }, [invalidateOperation, invalidatePreview, managementKey, serviceBase]);

  const loadHistory = useCallback(async () => {
    if (!mountedRef.current || !serviceBase || view !== 'history') return;
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
          limit: 20,
          cursor: historyCursor,
        },
        controller.signal
      );
      if (controller.signal.aborted || generation !== historyGenerationRef.current) return;
      if (!isUsageArchiveList(result)) {
        setError(
          t('usage_maintenance.archive_response_invalid', {
            defaultValue: 'The server returned an invalid archive task response.',
          })
        );
        return;
      }
      setHistoryList(result);
      setError(null);
    } catch (cause) {
      if (controller.signal.aborted || generation !== historyGenerationRef.current) return;
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (generation === historyGenerationRef.current) {
        historyControllerRef.current = null;
        setHistoryLoading(false);
      }
    }
  }, [historyCursor, historyFilter, managementKey, serviceBase, t, view]);

  useEffect(() => {
    if (view !== 'history') return;
    void loadHistory();
    return () => {
      historyGenerationRef.current += 1;
      historyControllerRef.current?.abort();
      historyControllerRef.current = null;
    };
  }, [loadHistory, view]);

  useEffect(() => {
    if (!selectedRunId || (view !== 'detail' && view !== 'active') || !serviceBase) return;
    const generation = ++selectedArchiveGenerationRef.current;
    selectedArchiveControllerRef.current?.abort();
    const controller = new AbortController();
    selectedArchiveControllerRef.current = controller;
    setSelectedArchiveLoading(true);
    setSelectedArchive(null);
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
  }, [managementKey, selectedArchiveRefreshToken, selectedRunId, serviceBase, t, view]);

  const load = useCallback(
    async ({ background = false }: { background?: boolean } = {}) => {
      if (!mountedRef.current) return;
      if (!serviceBase) {
        if (!background) setLoading(false);
        return;
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
          if (controller.signal.aborted || generation !== loadGenerationRef.current) return;
          capabilityContextRef.current = { serviceBase, managementKey };
        }
        const [maintenanceResult, archiveResult] = await Promise.all([
          usageServiceApi.getUsageMaintenance(serviceBase, managementKey, controller.signal),
          usageServiceApi.listUsageArchives(serviceBase, managementKey, 20, controller.signal),
        ]);
        if (controller.signal.aborted || generation !== loadGenerationRef.current) return;
        if (!isUsageMaintenanceStatus(maintenanceResult) || !isUsageArchiveList(archiveResult)) {
          setUnsupported(true);
          setMaintenance(null);
          setArchives([]);
          setError(null);
          return;
        }
        setMaintenance(maintenanceResult);
        setArchives(archiveResult.runs ?? []);
        setUnsupported(false);
        setError(null);
      } catch (cause) {
        if (generation !== loadGenerationRef.current || controller.signal.aborted) return;
        controller.abort();
        if (isUnsupportedError(cause)) {
          setUnsupported(true);
          setMaintenance(null);
          setArchives([]);
        } else {
          setUnsupported(false);
          setError(cause instanceof Error ? cause.message : String(cause));
        }
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
          if (mountedRef.current && view === 'active' && selectedRunId) {
            setSelectedArchiveRefreshToken((value) => value + 1);
          }
        });
      }
    }, activeRefreshIntervalMs);
    return () => clearInterval(timer);
  }, [load, selectedRunId, serviceBase, shouldPollMaintenance, view, working]);

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
  const maintenanceLoaded = maintenance !== null;

  useEffect(() => {
    if (!maintenanceLoaded || !serviceBase || unsupported || view !== 'create') return;
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
  ]);

  const selectRetention = (selection: RetentionSelection) => {
    if (working || selection === retentionSelection) return;
    invalidatePreview(true);
    setGuidedArchiveStage('idle');
    setGuidedArchiveRunId(null);
    setRetentionSelection(selection);
  };

  const updateCustomCutoff = (value: string) => {
    if (working) return;
    invalidatePreview(true);
    setGuidedArchiveStage('idle');
    setGuidedArchiveRunId(null);
    setCustomCutoff(value);
  };

  const refreshMaintenance = () => {
    setReferenceNowMS(Date.now());
    setPreviewRefreshToken((value) => value + 1);
    void load();
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
        finalStatus = requireArchiveResponse(
          verifyResponse,
          runID,
          new Set(['verified', 'deleting', 'completed'])
        ).run.status;
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
      showNotification(
        t('usage_maintenance.archive_prepare_success', {
          defaultValue: 'Archive created and verified. Raw data was not deleted.',
        }),
        'success'
      );
      await load({ background: true });
    } catch (cause) {
      if (operationIsCurrent(operation)) {
        setGuidedArchiveStage('attention');
        showNotification(cause instanceof Error ? cause.message : String(cause), 'error');
        await load({ background: true });
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
    showConfirmation({
      title: t('usage_maintenance.archive_prepare_confirm_title', {
        defaultValue: 'Archive and verify this data?',
      }),
      message: t('usage_maintenance.archive_prepare_confirm_message', {
        defaultValue:
          'The server will create the archive, write its files, and verify them in one guided operation. Raw data will not be deleted.',
      }),
      confirmText: t('usage_maintenance.archive_prepare_confirm_button', {
        defaultValue: 'Archive and verify',
      }),
      cancelText: t('common.cancel'),
      variant: 'primary',
      onConfirm: () => createArchive(previewCutoffTimestamp, confirmation),
    });
  };

  const runAction = async (
    run: UsageArchiveRunSummary,
    action: 'resume' | 'verify' | 'delete',
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
    const operation = beginWorking(serviceBase, managementKey);
    if (!operation) return;
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
            : await usageServiceApi.deleteUsageArchive(
                serviceBase,
                run.id,
                managementKey,
                operation.controller.signal
              );
      if (!operationIsCurrent(operation)) return;
      const updated = requireArchiveResponse(response, run.id, expectedActionStatuses(run, action));
      if (
        guidedArchiveRunId === run.id ||
        (guidedArchiveStage === 'attention' && guidedArchiveRunId === null)
      ) {
        setGuidedArchiveStage('idle');
        setGuidedArchiveRunId(null);
      }
      const destructive = actionIsDestructive(run, action);
      if (
        !destructive &&
        (updated.run.status === 'deleting' || updated.run.status === 'completed')
      ) {
        showConcurrentDeleteWarning();
      } else {
        showNotification(
          t(`usage_maintenance.${destructive ? 'delete' : action}_success`, {
            defaultValue: destructive ? 'Logical deletion completed.' : 'Archive run updated.',
          }),
          'success'
        );
      }
      await load({ background: true });
      if (view === 'history') await loadHistory();
      if (operationIsCurrent(operation)) {
        setPreviewRefreshToken((value) => value + 1);
        if (selectedRunId === run.id) {
          setSelectedArchiveRefreshToken((value) => value + 1);
        }
      }
    } catch (cause) {
      if (operationIsCurrent(operation)) {
        showNotification(cause instanceof Error ? cause.message : String(cause), 'error');
        await load({ background: true });
        if (operationIsCurrent(operation)) {
          setPreviewRefreshToken((value) => value + 1);
          if (selectedRunId === run.id) {
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
  const knownValueLabel = (prefix: string, value: string, knownValues: ReadonlySet<string>) =>
    knownValues.has(value)
      ? t(`usage_maintenance.${prefix}_${value}`, { defaultValue: value })
      : value;
  const archiveStatusLabel = (value: string) =>
    knownValueLabel('run_status', value, archiveStatusTranslationValues);
  const archiveModeLabel = (value: string) =>
    knownValueLabel('run_mode', value, archiveModeTranslationValues);
  const migrationStatusLabel = (value: string) =>
    knownValueLabel('migration_status', value, migrationStatusTranslationValues);
  const aggregateStatusLabel = (value: string) =>
    knownValueLabel('aggregate_status', value, aggregateStatusTranslationValues);
  const guidedStageLabel = (stage: GuidedArchiveStage) =>
    t(`usage_maintenance.archive_prepare_${stage}`, {
      defaultValue:
        stage === 'creating'
          ? 'Creating archive task'
          : stage === 'archiving'
            ? 'Writing archive'
            : stage === 'verifying'
              ? 'Verifying archive'
              : stage === 'complete'
                ? 'Archive verified'
                : stage === 'attention'
                  ? 'Needs attention'
                  : '',
    });
  const archiveStageLabel = (stage: ReturnType<typeof getArchiveRunPresentationStage>) =>
    t(`usage_maintenance.run_stage_${stage}`, {
      defaultValue:
        stage === 'archiving'
          ? 'Archive in progress'
          : stage === 'verifying'
            ? 'Ready for verification'
            : stage === 'delete_ready'
              ? 'Archive verified'
              : stage === 'deleting'
                ? 'Removing raw data'
                : stage === 'completed'
                  ? 'Complete'
                  : 'Needs attention',
    });
  const archiveStepState = (
    stage: ReturnType<typeof getArchiveRunPresentationStage>,
    step: 'archive' | 'verify' | 'delete',
    resumeStatus?: UsageArchiveRunStatus
  ) => {
    const order = { archive: 1, verify: 2, delete: 3 } as const;
    const stageOrder =
      stage === 'archiving'
        ? 1
        : stage === 'verifying'
          ? 2
          : stage === 'delete_ready'
            ? 3
            : stage === 'deleting'
              ? 3
              : stage === 'completed'
                ? 4
                : 0;
    if (stage === 'attention') {
      const failedOrder =
        resumeStatus === 'deleting'
          ? 3
          : resumeStatus === 'verifying'
            ? 2
            : resumeStatus === 'archiving'
              ? 1
              : 0;
      if (failedOrder > order[step]) return 'complete';
      if (failedOrder === order[step]) return 'current';
      return 'pending';
    }
    if (stageOrder > order[step]) return 'complete';
    if (stageOrder === order[step]) return 'current';
    return 'pending';
  };
  const confirmAction = (run: UsageArchiveRunSummary, action: 'resume' | 'verify' | 'delete') => {
    if (!actionIsDestructive(run, action)) {
      void runAction(run, action);
      return;
    }
    const confirmation = {
      generation: contextGenerationRef.current,
      serviceBase,
      managementKey,
    };
    showConfirmation({
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
      onConfirm: () => runAction(run, action, confirmation),
    });
  };

  const actionLabel = (run: UsageArchiveRunSummary, action: ReturnType<typeof statusAction>) => {
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
  const rawRangeSummary =
    rawEventRange?.kind === 'available'
      ? `${formatTime(rawEventRange.minTimestampMS)} – ${formatTime(rawEventRange.maxTimestampMS)}`
      : rawEventRange?.kind === 'empty'
        ? t('usage_maintenance.raw_range_empty', { defaultValue: 'No raw usage data yet' })
        : rawEventRange?.kind === 'unavailable'
          ? t('usage_maintenance.raw_range_unavailable', {
              defaultValue: 'Time range unavailable on this server version',
            })
          : '-';
  const createBlockedByMaintenance = Boolean(maintenance?.active_run || maintenance?.active_lock);
  const archiveReadinessPending = maintenance?.readiness.migration_ready === false;
  const archiveReadinessHint = archiveReadinessPending
    ? t('usage_maintenance.archive_readiness_pending', {
        defaultValue:
          'Archiving becomes available after usage accounting preparation completes. Previewing is still safe.',
      })
    : '';
  const hasArchivedRawEvents = (maintenance?.raw_archived_event_count ?? 0) > 0;
  const recommendedPresetAvailable =
    !hasArchivedRawEvents &&
    recommendedRetentionDays !== null &&
    recommendedRetentionDays !== retentionSelection;
  const navigateTo = (nextView: UsageMaintenanceView) => {
    if (nextView !== 'detail' && nextView !== 'active') {
      setSelectedRunId(null);
      setSelectedArchive(null);
    }
    setError(null);
    setView(nextView);
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
    const isActive =
      maintenance?.active_run?.id === run.id || archiveProgressStatuses.has(run.status);
    setSelectedRunId(run.id);
    setSelectedArchive(null);
    setError(null);
    setView(isActive ? 'active' : 'detail');
  };

  const updateHistoryFilter = (filter: ArchiveHistoryFilter) => {
    setHistoryList({ runs: [] });
    setHistoryFilter(filter);
    setHistoryCursor(undefined);
    setHistoryCursorStack([]);
  };

  const nextHistoryPage = () => {
    if (!historyList.next_cursor) return;
    setHistoryList({ runs: [] });
    setHistoryCursorStack((stack) => [...stack, historyCursor ?? '']);
    setHistoryCursor(historyList.next_cursor);
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

  if (availability.checking || loading) return <LoadingSpinner />;
  if (unsupported) {
    return (
      <div className={styles.page}>
        <section className={styles.unsupported}>
          <h1 className={styles.title}>
            {t('usage_maintenance.title', { defaultValue: 'Usage maintenance' })}
          </h1>
          <p className={styles.description}>
            {t('usage_maintenance.unsupported', {
              defaultValue:
                'This Manager Server is older than the usage maintenance API. Upgrade the server to manage archives here.',
            })}
          </p>
        </section>
      </div>
    );
  }

  if (maintenance && view === 'overview') {
    return (
      <div className={styles.page}>
        {error ? <div className={styles.error}>{error}</div> : null}
        <UsageMaintenanceOverviewView
          maintenance={maintenance}
          archives={archives}
          working={working}
          onRefresh={refreshMaintenance}
          onNavigate={navigateTo}
          onOpenRun={openRun}
        />
      </div>
    );
  }

  if (maintenance && view === 'create') {
    return (
      <div className={styles.page}>
        {error ? <div className={styles.error}>{error}</div> : null}
        <UsageMaintenanceCreateView
          maintenance={maintenance}
          preview={preview}
          previewLoading={previewLoading}
          previewError={previewError}
          retentionSelection={retentionSelection}
          customCutoff={customCutoff}
          referenceNowMS={referenceNowMS}
          resolvedCutoffTimestamp={resolvedCutoffTimestamp}
          rawEventRange={rawEventRange ?? { kind: 'empty' }}
          recommendedRetentionDays={recommendedRetentionDays}
          guidedArchiveStage={guidedArchiveStage}
          guidedArchiveRunId={guidedArchiveRunId}
          working={working}
          createBlockedByMaintenance={createBlockedByMaintenance}
          archiveReadinessPending={archiveReadinessPending}
          archiveReadinessHint={archiveReadinessHint}
          onBack={() => navigateTo('overview')}
          onRefresh={refreshMaintenance}
          onSelectRetention={selectRetention}
          onUpdateCustomCutoff={updateCustomCutoff}
          onRetryPreview={() => setPreviewRefreshToken((value) => value + 1)}
          onCreate={confirmCreate}
          onStopWaiting={cancelGuidedArchive}
        />
      </div>
    );
  }

  if (maintenance && view === 'history') {
    return (
      <div className={styles.page}>
        {error ? <div className={styles.error}>{error}</div> : null}
        <UsageArchiveHistoryView
          archiveList={historyList}
          filter={historyFilter}
          loading={historyLoading}
          working={working}
          canGoBack={historyCursorStack.length > 0}
          onFilter={updateHistoryFilter}
          onNextPage={nextHistoryPage}
          onPreviousPage={previousHistoryPage}
          onRefresh={() => void loadHistory()}
          onNavigate={navigateTo}
          onOpenRun={openRun}
          onAction={confirmAction}
          actionDisabled={archiveActionDisabled}
          actionTitle={archiveActionTitle}
          actionLabel={(run, action) => actionLabel(run, action)}
        />
      </div>
    );
  }

  if (maintenance && view === 'transfer') {
    return (
      <div className={styles.page}>
        <UsageMaintenanceTransferView
          serviceBase={serviceBase}
          managementKey={managementKey}
          onBack={() => navigateTo('overview')}
        />
      </div>
    );
  }

  if (maintenance && view === 'advanced') {
    return (
      <div className={styles.page}>
        {error ? <div className={styles.error}>{error}</div> : null}
        <UsageMaintenanceAdvancedView
          maintenance={maintenance}
          working={working}
          onBack={() => navigateTo('overview')}
          onRefresh={refreshMaintenance}
          onCopyCommand={() => void copyCompactCommand()}
        />
      </div>
    );
  }

  if (maintenance && view === 'diagnostics') {
    return (
      <div className={styles.page}>
        {error ? <div className={styles.error}>{error}</div> : null}
        <UsageMaintenanceDiagnosticsView
          maintenance={maintenance}
          working={working}
          onBack={() => navigateTo('overview')}
          onRefresh={refreshMaintenance}
          onOpenActive={() => {
            if (maintenance.active_run) openRun(maintenance.active_run);
          }}
        />
      </div>
    );
  }

  if (maintenance && (view === 'detail' || view === 'active')) {
    if (selectedArchiveLoading) return <LoadingSpinner />;
    if (!selectedArchive) {
      return (
        <div className={styles.page}>
          <div className={styles.error}>
            {error ??
              t('usage_maintenance.archive_response_invalid', {
                defaultValue: 'The server returned an invalid archive task response.',
              })}
          </div>
          <Button variant="secondary" onClick={() => navigateTo('history')}>
            {t('common.back')}
          </Button>
        </div>
      );
    }
    return (
      <div className={styles.page}>
        {error ? <div className={styles.error}>{error}</div> : null}
        <UsageArchiveRunView
          archive={selectedArchive}
          active={view === 'active'}
          maintenance={maintenance}
          working={working}
          onBack={() => navigateTo('history')}
          onRefresh={() => {
            setSelectedArchiveRefreshToken((value) => value + 1);
            void load({ background: true });
          }}
          onStopWaiting={cancelGuidedArchive}
          onAction={confirmAction}
          actionDisabled={archiveActionDisabled}
          actionTitle={archiveActionTitle}
          actionLabel={(run, action) => actionLabel(run, action)}
        />
      </div>
    );
  }

  return (
    <div className={styles.page}>
      <section className={styles.hero}>
        <div>
          <p className={styles.eyebrow}>
            {t('usage_maintenance.eyebrow', { defaultValue: 'Data lifecycle' })}
          </p>
          <h1 className={styles.title}>
            {t('usage_maintenance.title', { defaultValue: 'Usage maintenance' })}
          </h1>
          <p className={styles.description}>
            {t('usage_maintenance.subtitle', {
              defaultValue:
                'Review archive runs and reclaim SQLite space without mixing logical deletion with physical compaction.',
            })}
          </p>
        </div>
        <div className={styles.actions}>
          {view !== 'overview' ? (
            <Button variant="ghost" size="sm" onClick={() => navigateTo('overview')}>
              {t('common.back')}
            </Button>
          ) : null}
          <Button variant="secondary" size="sm" onClick={refreshMaintenance} disabled={working}>
            {t('common.refresh')}
          </Button>
        </div>
      </section>

      {error ? <div className={styles.error}>{error}</div> : null}

      {maintenance ? (
        <section
          className={styles.stats}
          aria-label={t('usage_maintenance.status_title', { defaultValue: 'Maintenance status' })}
        >
          <div className={styles.stat}>
            <span className={styles.statValue}>{maintenance.raw_event_count.toLocaleString()}</span>
            <span className={styles.statLabel}>
              {t('usage_maintenance.raw_events', { defaultValue: 'Raw events' })}
            </span>
          </div>
          <div className={styles.stat}>
            <span className={styles.statValue}>
              {maintenance.raw_deleted_event_count.toLocaleString()}
            </span>
            <span className={styles.statLabel}>
              {t('usage_maintenance.deleted_events', { defaultValue: 'Logically deleted' })}
            </span>
          </div>
          <div className={styles.stat}>
            <span className={styles.statValue}>
              {formatFileSize(maintenance.storage.reclaimable_bytes)}
            </span>
            <span className={styles.statLabel}>
              {t('usage_maintenance.reclaimable', { defaultValue: 'Reclaimable' })}
            </span>
          </div>
          <div className={styles.stat}>
            <span className={styles.statValue}>
              {migrationStatusLabel(maintenance.migration.status)}
            </span>
            <span className={styles.statLabel}>
              {t('usage_maintenance.migration', { defaultValue: 'Migration' })}
            </span>
          </div>
          <div className={styles.stat}>
            <span className={styles.statValue}>
              {aggregateStatusLabel(maintenance.hourly_aggregate.status)}
            </span>
            <span className={styles.statLabel}>
              {t('usage_maintenance.hourly_aggregate', { defaultValue: 'Hourly aggregate' })}
            </span>
          </div>
        </section>
      ) : null}

      <section className={styles.panel}>
        <div className={styles.panelHeader}>
          <h2 className={styles.panelTitle}>
            {t('usage_maintenance.policy_title', { defaultValue: 'Choose a retention policy' })}
          </h2>
          <span className={styles.muted}>
            {t('usage_maintenance.policy_hint', {
              defaultValue: 'Archiving is non-destructive. Raw deletion remains a separate step.',
            })}
          </span>
        </div>
        <div className={styles.policyGrid}>
          <div className={styles.policyControls}>
            <div className={styles.sectionIntro}>
              <h3>
                {t('usage_maintenance.retention_title', {
                  defaultValue: 'How much recent raw data should stay online?',
                })}
              </h3>
              <p>
                {t('usage_maintenance.retention_description', {
                  defaultValue:
                    'Events older than the selected period are included in the archive preview. Recent events remain in SQLite.',
                })}
              </p>
            </div>
            <div
              className={styles.retentionOptions}
              role="group"
              aria-label={t('usage_maintenance.retention_group_label', {
                defaultValue: 'Raw data retention period',
              })}
            >
              {retentionPresetDays.map((days) => (
                <button
                  key={days}
                  type="button"
                  className={`${styles.retentionOption} ${
                    retentionSelection === days ? styles.retentionOptionActive : ''
                  }`}
                  aria-pressed={retentionSelection === days}
                  disabled={working}
                  onClick={() => selectRetention(days)}
                >
                  <strong>
                    {t(`usage_maintenance.retention_${days}_label`, {
                      defaultValue: `Keep ${days} days`,
                    })}
                  </strong>
                  <span>
                    {t(`usage_maintenance.retention_${days}_hint`, {
                      defaultValue:
                        days === 7
                          ? 'Lowest storage use'
                          : days === 30
                            ? 'Recommended default'
                            : 'More local history',
                    })}
                  </span>
                </button>
              ))}
              <button
                type="button"
                className={`${styles.retentionOption} ${
                  retentionSelection === 'custom' ? styles.retentionOptionActive : ''
                }`}
                aria-pressed={retentionSelection === 'custom'}
                disabled={working}
                onClick={() => selectRetention('custom')}
              >
                <strong>
                  {t('usage_maintenance.retention_custom_label', { defaultValue: 'Custom date' })}
                </strong>
                <span>
                  {t('usage_maintenance.retention_custom_hint', {
                    defaultValue: 'Choose an exact cutoff',
                  })}
                </span>
              </button>
            </div>
            {retentionSelection === 'custom' ? (
              <div className={styles.dateField}>
                <Input
                  type="datetime-local"
                  label={t('usage_maintenance.cutoff', { defaultValue: 'Archive events before' })}
                  hint={t('usage_maintenance.custom_cutoff_hint', {
                    defaultValue: 'The cutoff must be in the past.',
                  })}
                  max={toLocalDateTimeValue(referenceNowMS)}
                  value={customCutoff}
                  disabled={working}
                  error={!cutoffTimestamp ? (previewError ?? undefined) : undefined}
                  onChange={(event) => updateCustomCutoff(event.target.value)}
                />
              </div>
            ) : null}
            <div className={styles.policyFacts}>
              <div className={styles.policyFact}>
                <span>
                  {t('usage_maintenance.raw_range_label', {
                    defaultValue: 'Current raw data range',
                  })}
                </span>
                <strong>{rawRangeSummary}</strong>
                {maintenance ? (
                  <small>
                    {t('usage_maintenance.raw_range_count', {
                      defaultValue: '{{count}} raw events',
                      count: maintenance.raw_event_count.toLocaleString(i18n.language),
                    })}
                  </small>
                ) : null}
              </div>
              <div className={styles.policyFact}>
                <span>
                  {t('usage_maintenance.resolved_cutoff_label', {
                    defaultValue: 'Resolved archive cutoff',
                  })}
                </span>
                <strong>{formatTime(resolvedCutoffTimestamp)}</strong>
                <small>
                  {t('usage_maintenance.resolved_cutoff_hint', {
                    defaultValue: 'Older events are archived; newer events stay online.',
                  })}
                </small>
              </div>
            </div>
          </div>
          <div className={styles.impactCard} aria-live="polite">
            <div className={styles.impactHeader}>
              <div>
                <span className={styles.impactEyebrow}>
                  {t('usage_maintenance.preview', { defaultValue: 'Impact preview' })}
                </span>
                <h3>
                  {t('usage_maintenance.preview_title', {
                    defaultValue: 'What will be archived',
                  })}
                </h3>
              </div>
              {previewLoading ? (
                <span className={styles.previewLoading}>
                  <span className="loading-spinner" aria-hidden="true" />
                  {t('usage_maintenance.preview_loading', { defaultValue: 'Calculating…' })}
                </span>
              ) : null}
            </div>
            {guidedArchiveStage !== 'idle' ? (
              <div className={styles.guidedProgress} aria-live="polite">
                <div className={styles.guidedProgressHeader}>
                  <strong>{guidedStageLabel(guidedArchiveStage)}</strong>
                  {guidedArchiveRunId ? (
                    <span className={styles.muted}>
                      {t('usage_maintenance.archive_task_label', {
                        defaultValue: 'Task {{runId}}',
                        runId: guidedArchiveRunId,
                      })}
                    </span>
                  ) : null}
                </div>
                <div className={styles.guidedSteps}>
                  {(['archive', 'verify', 'delete'] as const).map((step) => {
                    const state =
                      guidedArchiveStage === 'complete'
                        ? step === 'delete'
                          ? 'current'
                          : 'complete'
                        : guidedArchiveStage === 'attention'
                          ? 'pending'
                          : step === 'archive'
                            ? guidedArchiveStage === 'creating' ||
                              guidedArchiveStage === 'archiving'
                              ? 'current'
                              : 'complete'
                            : step === 'verify'
                              ? guidedArchiveStage === 'verifying'
                                ? 'current'
                                : 'pending'
                              : 'pending';
                    return (
                      <div
                        key={step}
                        className={`${styles.guidedStep} ${styles[`guidedStep_${state}`]}`}
                      >
                        <span className={styles.guidedStepMarker}>
                          {state === 'complete' ? '✓' : ''}
                        </span>
                        <span>
                          {t(`usage_maintenance.guided_step_${step}`, {
                            defaultValue:
                              step === 'archive'
                                ? 'Archive'
                                : step === 'verify'
                                  ? 'Verify'
                                  : 'Optional delete',
                          })}
                        </span>
                      </div>
                    );
                  })}
                </div>
                {guidedArchiveStage !== 'complete' ? (
                  <Button
                    size="xs"
                    variant="ghost"
                    onClick={cancelGuidedArchive}
                    disabled={guidedArchiveStage === 'attention'}
                  >
                    {t('usage_maintenance.archive_prepare_stop', { defaultValue: 'Stop waiting' })}
                  </Button>
                ) : (
                  <p className={styles.guidedNote}>
                    {t('usage_maintenance.archive_prepare_no_delete', {
                      defaultValue:
                        'Raw events are unchanged. Delete them only from the separate action.',
                    })}
                  </p>
                )}
              </div>
            ) : null}
            {previewError && cutoffTimestamp ? (
              <div className={styles.previewError}>
                <p>{previewError}</p>
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => setPreviewRefreshToken((value) => value + 1)}
                  disabled={working}
                >
                  {t('usage_maintenance.preview_retry', { defaultValue: 'Retry calculation' })}
                </Button>
              </div>
            ) : null}
            {!previewLoading && !previewError && preview?.event_count === 0 ? (
              <div className={styles.emptyImpact}>
                <strong>
                  {t('usage_maintenance.preview_empty_title', {
                    defaultValue: 'No events match this retention policy',
                  })}
                </strong>
                <p>
                  {rawEventRange?.kind === 'empty'
                    ? t('usage_maintenance.preview_empty_no_data', {
                        defaultValue:
                          'There is no raw usage data to archive yet. Import or collect events first.',
                      })
                    : hasArchivedRawEvents
                      ? t('usage_maintenance.preview_empty_archived', {
                          defaultValue:
                            'Some older raw events are already protected by an archive. Review archive history before changing the retention period.',
                        })
                      : recommendedPresetAvailable
                        ? t('usage_maintenance.preview_empty_recommendation', {
                            defaultValue:
                              'The oldest raw event is {{oldest}}. Keep {{days}} days to include older events while preserving recent data.',
                            oldest:
                              rawEventRange?.kind === 'available'
                                ? formatTime(rawEventRange.minTimestampMS)
                                : '-',
                            days: recommendedRetentionDays,
                          })
                        : rawEventRange?.kind === 'available'
                          ? t('usage_maintenance.preview_empty_recent', {
                              defaultValue:
                                'All raw events are newer than the standard retention presets. Use a custom date only if you intentionally want to archive recent data.',
                            })
                          : t('usage_maintenance.preview_empty_generic', {
                              defaultValue:
                                'Try a shorter retention period or choose a custom cutoff after the oldest event.',
                            })}
                </p>
                {recommendedPresetAvailable ? (
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => selectRetention(recommendedRetentionDays)}
                    disabled={working}
                  >
                    {t('usage_maintenance.use_recommended_retention', {
                      defaultValue: 'Keep {{days}} days',
                      days: recommendedRetentionDays,
                    })}
                  </Button>
                ) : null}
              </div>
            ) : null}
            {!previewLoading && !previewError && preview && preview.event_count > 0 ? (
              <>
                <div className={styles.previewGrid}>
                  <div className={styles.metric}>
                    <span className={styles.metricLabel}>
                      {t('usage_maintenance.preview_events', { defaultValue: 'Eligible events' })}
                    </span>
                    <span className={styles.metricValue}>
                      {preview.event_count.toLocaleString()}
                    </span>
                  </div>
                  <div className={styles.metric}>
                    <span className={styles.metricLabel}>
                      {t('usage_maintenance.preview_bytes', { defaultValue: 'Estimated size' })}
                    </span>
                    <span className={styles.metricValue}>
                      {formatFileSize(preview.estimated_bytes)}
                    </span>
                  </div>
                  <div className={styles.metric}>
                    <span className={styles.metricLabel}>
                      {t('usage_maintenance.preview_range', { defaultValue: 'Timestamp range' })}
                    </span>
                    <span className={styles.metricValue}>
                      {formatTime(preview.min_timestamp_ms)} –{' '}
                      {formatTime(preview.max_timestamp_ms)}
                    </span>
                  </div>
                </div>
                {createBlockedByMaintenance ? (
                  <p className={styles.busyHint}>
                    {t('usage_maintenance.create_blocked_active', {
                      defaultValue:
                        'Finish or recover the active maintenance task before starting another archive.',
                    })}
                  </p>
                ) : archiveReadinessPending ? (
                  <p className={styles.busyHint}>{archiveReadinessHint}</p>
                ) : null}
                <Button
                  onClick={confirmCreate}
                  disabled={working || createBlockedByMaintenance || archiveReadinessPending}
                  fullWidth
                >
                  {t('usage_maintenance.create', { defaultValue: 'Archive and verify' })}
                </Button>
              </>
            ) : null}
          </div>
        </div>
        {deleteReadinessHint ? <p className={styles.readinessHint}>{deleteReadinessHint}</p> : null}
        <div className={styles.historyHeader}>
          <div>
            <h3>{t('usage_maintenance.archive_title', { defaultValue: 'Archive history' })}</h3>
            <p>
              {t('usage_maintenance.archive_hint', {
                defaultValue: 'Archive first, delete only after verification.',
              })}
            </p>
          </div>
        </div>
        <div className={styles.runList}>
          {archives.length === 0 ? (
            <p className={styles.muted}>
              {t('usage_maintenance.no_runs', { defaultValue: 'No archive runs yet.' })}
            </p>
          ) : null}
          {archives.map((run) => {
            const action = statusAction(run.status);
            const destructive = action ? actionIsDestructive(run, action) : false;
            const waitingForMigration = Boolean(
              action && archiveReadinessPending && actionRequiresMigrationReady(run, action)
            );
            const presentationStage = getArchiveRunPresentationStage(run);
            return (
              <div className={styles.run} key={run.id}>
                <div className={styles.runMain}>
                  <div className={styles.runTitle}>
                    <span
                      className={`${styles.badge} ${
                        presentationStage !== 'completed' ? styles.badgeActive : ''
                      } ${presentationStage === 'attention' ? styles.badgeAttention : ''}`}
                    >
                      {archiveStageLabel(presentationStage)}
                    </span>
                    <strong>
                      {t('usage_maintenance.run_title', {
                        defaultValue: 'Archive before {{cutoff}}',
                        cutoff: formatTime(run.cutoff_timestamp_ms),
                      })}
                    </strong>
                  </div>
                  <div className={styles.runMeta}>
                    <span>
                      {run.event_count.toLocaleString()}{' '}
                      {t('usage_maintenance.events_suffix', { defaultValue: 'events' })}
                    </span>
                    <span>{formatFileSize(run.estimated_bytes)}</span>
                    <span>{formatTime(run.created_at_ms)}</span>
                  </div>
                  <div
                    className={styles.runSteps}
                    aria-label={t('usage_maintenance.run_steps_label', {
                      defaultValue: 'Archive workflow progress',
                    })}
                  >
                    {(['archive', 'verify', 'delete'] as const).map((step) => {
                      const state = archiveStepState(presentationStage, step, run.resume_status);
                      return (
                        <span
                          key={step}
                          className={`${styles.runStep} ${styles[`runStep_${state}`]}`}
                        >
                          <span className={styles.runStepMarker}>
                            {state === 'complete' ? '✓' : ''}
                          </span>
                          {t(`usage_maintenance.guided_step_${step}`, {
                            defaultValue:
                              step === 'archive'
                                ? 'Archive'
                                : step === 'verify'
                                  ? 'Verify'
                                  : 'Optional delete',
                          })}
                        </span>
                      );
                    })}
                  </div>
                  <details className={styles.technicalDetails}>
                    <summary>
                      {t('usage_maintenance.technical_details', {
                        defaultValue: 'Technical details',
                      })}
                    </summary>
                    <div className={styles.technicalGrid}>
                      <span>
                        {t('usage_maintenance.technical_run_id', { defaultValue: 'Task ID' })}
                      </span>
                      <strong>{run.id}</strong>
                      <span>
                        {t('usage_maintenance.technical_status', { defaultValue: 'Server status' })}
                      </span>
                      <strong>{archiveStatusLabel(run.status)}</strong>
                      <span>{t('usage_maintenance.technical_mode', { defaultValue: 'Mode' })}</span>
                      <strong>{archiveModeLabel(run.mode)}</strong>
                      {run.resume_status ? (
                        <>
                          <span>
                            {t('usage_maintenance.technical_resume_status', {
                              defaultValue: 'Resume stage',
                            })}
                          </span>
                          <strong>{archiveStatusLabel(run.resume_status)}</strong>
                        </>
                      ) : null}
                    </div>
                  </details>
                </div>
                <div className={styles.runActions}>
                  {action ? (
                    <Button
                      size="xs"
                      variant={destructive ? 'danger' : 'secondary'}
                      disabled={working || waitingForMigration || (destructive && deleteDisabled)}
                      title={
                        waitingForMigration
                          ? archiveReadinessHint
                          : destructive
                            ? deleteReadinessHint || undefined
                            : undefined
                      }
                      onClick={() => confirmAction(run, action)}
                    >
                      {actionLabel(run, action)}
                    </Button>
                  ) : null}
                </div>
              </div>
            );
          })}
        </div>
      </section>

      <details className={`${styles.notice} ${styles.advancedSection}`}>
        <summary>
          <span>
            {t('usage_maintenance.compact_advanced_summary', {
              defaultValue: 'Advanced: reclaim physical SQLite space',
            })}
          </span>
          <span className={styles.badge}>
            {t('usage_maintenance.compact_offline_badge', {
              defaultValue: 'Requires stopped server',
            })}
          </span>
        </summary>
        <div className={styles.advancedContent}>
          <p>
            {t('usage_maintenance.compact_description', {
              defaultValue:
                'Logical deletion does not shrink the SQLite file. Stop Manager Server before running the offline compaction command.',
            })}
          </p>
          {maintenance ? (
            <div className={styles.compactFacts}>
              <span>
                {t('usage_maintenance.compact_reclaimable', {
                  defaultValue: 'Currently reclaimable',
                })}
              </span>
              <strong>{formatFileSize(maintenance.storage.reclaimable_bytes)}</strong>
              <span>
                {t('usage_maintenance.compact_total_size', { defaultValue: 'Database set size' })}
              </span>
              <strong>{formatFileSize(maintenance.storage.total_bytes)}</strong>
            </div>
          ) : null}
          <ul>
            <li>
              {t('usage_maintenance.compact_backup', {
                defaultValue:
                  'Back up usage.sqlite, usage.sqlite-wal, usage.sqlite-shm, data.key, and the usage-archives directory together.',
              })}
            </li>
            <li>
              {t('usage_maintenance.compact_command', {
                defaultValue: 'Run: cpa-manager-plus compact-usage --db-path /path/to/usage.sqlite',
              })}
            </li>
            <li>
              {t('usage_maintenance.compact_restore', {
                defaultValue:
                  'Restore the complete backup set before troubleshooting a failed checkpoint or integrity check.',
              })}
            </li>
          </ul>
        </div>
      </details>
    </div>
  );
}
