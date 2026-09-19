import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
  type DragEvent,
  type Ref,
} from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { IconArrowUpFromLine, IconDownload, IconRefreshCw } from '@/components/ui/icons';
import {
  UsageImportProgressActions,
  UsageImportProgressView,
} from '@/components/usage/UsageImportProgressView';
import {
  getUsageServiceErrorCode,
  usageServiceApi,
  type UsageExportResponse,
  type UsageImportResponse,
  type UsageImportSession,
  type UsageImportSessionList,
  type UsageImportSessionStatus,
} from '@/services/api/usageService';
import { useNotificationStore } from '@/stores';
import { downloadBlob } from '@/utils/download';
import { formatDateTime, formatFileSize } from '@/utils/format';
import {
  cancelUsageImportFile,
  isUsageImportCancelledError,
  isUsageImportPausedError,
  uploadUsageImportFile,
  UsageImportFailedError,
  type UsageImportProgress,
} from '@/features/monitoring/services/usageImportSession';
import { isUsageImportFile } from '@/utils/usageImport';
import styles from './UsageMaintenanceTransferView.module.scss';

type Props = {
  serviceBase: string;
  managementKey?: string;
  panel: 'import' | 'import-session' | 'export' | null;
  sessionId: string | null;
  refreshToken: number;
  onOpenPanel: (panel: 'import' | 'import-session' | 'export', sessionId?: string) => void;
  onClosePanel: () => void;
  onUsageChanged?: () => void;
  drawerClassName?: string;
  drawerBodyRef?: Ref<HTMLDivElement>;
};

type ActiveTask = {
  file: File;
  progress: UsageImportProgress;
};

const activeStatuses = new Set<UsageImportSessionStatus>(['uploading', 'ready', 'processing']);
const resumableStatuses = new Set<UsageImportSessionStatus>(['uploading', 'ready']);

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const isFiniteNumber = (value: unknown): value is number =>
  typeof value === 'number' && Number.isFinite(value);

const isImportSession = (value: unknown): value is UsageImportSession => {
  if (!isRecord(value)) return false;
  return (
    typeof value.id === 'string' &&
    typeof value.filename === 'string' &&
    typeof value.status === 'string' &&
    isFiniteNumber(value.size_bytes) &&
    isFiniteNumber(value.received_bytes) &&
    isFiniteNumber(value.chunk_size_bytes) &&
    isFiniteNumber(value.created_at_ms) &&
    isFiniteNumber(value.updated_at_ms) &&
    isFiniteNumber(value.expires_at_ms)
  );
};

const isImportSessionList = (value: unknown): value is UsageImportSessionList => {
  if (!isRecord(value) || !Array.isArray(value.sessions)) return false;
  return (
    value.sessions.every(isImportSession) &&
    isFiniteNumber(value.total) &&
    isFiniteNumber(value.active_sessions) &&
    isFiniteNumber(value.max_sessions) &&
    isFiniteNumber(value.chunk_size_bytes) &&
    isFiniteNumber(value.disk_quota_bytes) &&
    isFiniteNumber(value.ttl_seconds)
  );
};

const progressPercent = (session: UsageImportSession) => {
  if (session.size_bytes <= 0) return 0;
  return Math.min(
    100,
    Math.max(0, Math.floor((session.received_bytes / session.size_bytes) * 100))
  );
};

const formatTTL = (
  seconds: number,
  t: (key: string, options?: Record<string, unknown>) => string
) => {
  const hours = Math.max(0, Math.round(seconds / 3600));
  return t('usage_maintenance.transfer_hours', { defaultValue: '{{hours}}h', hours });
};

const formatImportError = (
  error: unknown,
  t: (key: string, options?: Record<string, unknown>) => string
) => {
  const code = getUsageServiceErrorCode(error);
  const keyByCode: Record<string, string> = {
    usage_import_session_too_large: 'transfer_error_too_large',
    usage_import_session_quota_exceeded: 'transfer_error_quota',
    usage_import_session_limit_exceeded: 'transfer_error_limit',
    usage_import_session_conflict: 'transfer_error_conflict',
    usage_import_session_file_mismatch: 'transfer_error_file_mismatch',
    usage_import_session_not_found: 'transfer_error_not_found',
    usage_import_session_invalid_request: 'transfer_error_invalid',
  };
  return t(`usage_maintenance.${keyByCode[code] ?? 'transfer_error_generic'}`, {
    defaultValue:
      keyByCode[code] === 'transfer_error_too_large'
        ? 'The selected file exceeds the Manager Server disk quota.'
        : keyByCode[code] === 'transfer_error_quota'
          ? 'The Manager Server import disk quota is currently reserved by other sessions.'
          : keyByCode[code] === 'transfer_error_limit'
            ? 'The maximum number of active import sessions has been reached.'
            : keyByCode[code] === 'transfer_error_conflict'
              ? 'The selected file does not match the resumable session.'
              : keyByCode[code] === 'transfer_error_file_mismatch'
                ? 'The selected file does not match the uploaded session prefix. Choose the original file or start a new import.'
                : keyByCode[code] === 'transfer_error_not_found'
                  ? 'The resumable session has expired or no longer exists.'
                  : keyByCode[code] === 'transfer_error_invalid'
                    ? 'The import request is invalid.'
                    : 'The import request could not be completed.',
  });
};

const resultSummary = (
  result: UsageImportResponse | undefined,
  t: (key: string, options?: Record<string, unknown>) => string
) => {
  if (!result) return t('usage_maintenance.transfer_result_pending', { defaultValue: '—' });
  return t('usage_maintenance.transfer_result_summary', {
    defaultValue:
      'added {{added}} · skipped {{skipped}} · failed {{failed}} · unsupported {{unsupported}} · warnings {{warnings}}',
    added: result.added ?? 0,
    skipped: result.skipped ?? 0,
    failed: result.failed ?? 0,
    unsupported: result.unsupported ?? 0,
    warnings: result.warnings?.length ?? 0,
  });
};

const hasImportIssues = (result?: UsageImportResponse) =>
  Boolean((result?.failed ?? 0) > 0 || (result?.unsupported ?? 0) > 0 || result?.warnings?.length);

const statusTone = (status: UsageImportSessionStatus, result?: UsageImportResponse) => {
  if (status === 'completed') return hasImportIssues(result) ? styles.warning : styles.success;
  if (status === 'failed') return styles.danger;
  if (status === 'cancelled') return styles.neutral;
  return styles.info;
};

export function UsageMaintenanceTransferView({
  serviceBase,
  managementKey,
  panel,
  sessionId: selectedSessionId,
  refreshToken,
  onOpenPanel,
  onClosePanel,
  onUsageChanged,
  drawerClassName,
  drawerBodyRef,
}: Props) {
  const { t, i18n } = useTranslation();
  const { showConfirmation, showNotification } = useNotificationStore();
  const inputRef = useRef<HTMLInputElement | null>(null);
  const pendingSessionIdRef = useRef<string | undefined>(undefined);
  const operationRef = useRef<AbortController | null>(null);
  const operationIDRef = useRef(0);
  const mountedRef = useRef(false);
  const contextGenerationRef = useRef(0);
  const sessionRequestRef = useRef<AbortController | null>(null);
  const sessionRequestIDRef = useRef(0);
  const [cancelPending, setCancelPending] = useState(false);
  const [sessionList, setSessionList] = useState<UsageImportSessionList | null>(null);
  const [activeTask, setActiveTask] = useState<ActiveTask | null>(null);
  const [pendingImport, setPendingImport] = useState<{
    file: File;
    sessionId?: string;
    generation: number;
  } | null>(null);
  const pendingImportRef = useRef(pendingImport);
  const panelRef = useRef(panel);
  const [detailSession, setDetailSession] = useState<UsageImportSession | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  useLayoutEffect(() => {
    panelRef.current = panel;
  }, [panel]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useLayoutEffect(() => {
    mountedRef.current = true;
    contextGenerationRef.current += 1;
    setActiveTask(null);
    setPendingImport(null);
    pendingImportRef.current = null;
    pendingSessionIdRef.current = undefined;
    setDetailSession(null);
    setDetailError(null);
    setSessionList(null);
    setLoading(true);
    setCancelPending(false);
    setExporting(false);
    return () => {
      mountedRef.current = false;
      contextGenerationRef.current += 1;
      operationIDRef.current += 1;
      operationRef.current?.abort();
      operationRef.current = null;
    };
  }, [managementKey, serviceBase]);

  const loadSessions = useCallback(
    async (background = false) => {
      if (!serviceBase) return;
      const requestID = ++sessionRequestIDRef.current;
      sessionRequestRef.current?.abort();
      const controller = new AbortController();
      sessionRequestRef.current = controller;
      if (background) setRefreshing(true);
      else setLoading(true);
      try {
        const result = await usageServiceApi.listUsageImportSessions(
          serviceBase,
          managementKey,
          {
            limit: 20,
          },
          controller.signal
        );
        if (requestID !== sessionRequestIDRef.current) return;
        if (!isImportSessionList(result)) {
          throw new Error('invalid import session response');
        }
        setSessionList(result);
        setError(null);
      } catch (cause) {
        if (controller.signal.aborted || requestID !== sessionRequestIDRef.current) return;
        setError(formatImportError(cause, t));
      } finally {
        if (requestID === sessionRequestIDRef.current) {
          if (background) setRefreshing(false);
          else setLoading(false);
          sessionRequestRef.current = null;
        }
      }
    },
    [managementKey, serviceBase, t]
  );

  useEffect(() => {
    void loadSessions();
    return () => {
      sessionRequestIDRef.current += 1;
      sessionRequestRef.current?.abort();
      sessionRequestRef.current = null;
    };
  }, [loadSessions]);

  useEffect(() => {
    if (refreshToken > 0) void loadSessions(true);
  }, [loadSessions, refreshToken]);

  useEffect(() => {
    setDetailSession(null);
    setDetailError(null);
    if (
      !selectedSessionId ||
      !sessionList ||
      sessionList.sessions.some((item) => item.id === selectedSessionId)
    )
      return;
    const controller = new AbortController();
    void usageServiceApi
      .getUsageImportSession(serviceBase, selectedSessionId, managementKey, controller.signal)
      .then((session) => {
        if (controller.signal.aborted) return;
        if (!isImportSession(session) || session.id !== selectedSessionId)
          throw new Error('invalid import session response');
        setDetailSession(session);
      })
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) setDetailError(formatImportError(cause, t));
      });
    return () => controller.abort();
  }, [selectedSessionId, sessionList, serviceBase, managementKey, t]);

  const shouldPoll = Boolean(
    activeTask?.progress.phase === 'processing' ||
    sessionList?.sessions.some((session) => activeStatuses.has(session.status))
  );

  useEffect(() => {
    if (!shouldPoll) return;
    const timer = globalThis.setInterval(() => void loadSessions(true), 5_000);
    return () => globalThis.clearInterval(timer);
  }, [loadSessions, shouldPoll]);

  const updateProgress = useCallback((file: File, progress: UsageImportProgress) => {
    setActiveTask((current) =>
      current?.file === file && current.progress.phase === 'cancelled'
        ? current
        : { file, progress }
    );
  }, []);

  const runImport = useCallback(
    async (file: File, sessionId?: string) => {
      if (!isUsageImportFile(file)) {
        showNotification(
          t('usage_maintenance.transfer_invalid_file', {
            defaultValue: 'Choose a JSONL, JSON, NDJSON, or text usage export.',
          }),
          'error'
        );
        return;
      }
      operationRef.current?.abort();
      const controller = new AbortController();
      operationRef.current = controller;
      const operationID = ++operationIDRef.current;
      setActiveTask({
        file,
        progress: {
          sessionId: sessionId ?? '',
          filename: file.name,
          phase: 'preparing',
          uploadedBytes: 0,
          totalBytes: file.size,
          percent: 0,
        },
      });
      try {
        const result = await uploadUsageImportFile({
          base: serviceBase,
          managementKey,
          file,
          sessionId,
          signal: controller.signal,
          onProgress: (progress) => {
            if (operationID === operationIDRef.current) updateProgress(file, progress);
          },
        });
        if (operationID !== operationIDRef.current) return;
        showNotification(
          t('usage_maintenance.transfer_import_success', {
            defaultValue:
              'Import complete: added {{added}}, skipped {{skipped}}, failed {{failed}}.',
            added: result.added ?? 0,
            skipped: result.skipped ?? 0,
            failed: result.failed ?? 0,
          }),
          hasImportIssues(result) ? 'warning' : 'success'
        );
        await loadSessions(true);
      } catch (cause) {
        if (operationID !== operationIDRef.current) return;
        if (!isUsageImportPausedError(cause) && !isUsageImportCancelledError(cause)) {
          const retryable = cause instanceof UsageImportFailedError ? cause.retryable : true;
          setActiveTask((current) =>
            current?.file === file
              ? {
                  ...current,
                  progress: {
                    ...current.progress,
                    phase: 'failed',
                    error: formatImportError(cause, t),
                    retryable,
                  },
                }
              : current
          );
          showNotification(formatImportError(cause, t), 'error');
        }
        await loadSessions(true);
      } finally {
        if (operationID === operationIDRef.current) {
          operationRef.current = null;
          onUsageChanged?.();
        }
      }
    },
    [loadSessions, managementKey, onUsageChanged, serviceBase, showNotification, t, updateProgress]
  );

  const selectFile = (file: File, sessionId?: string) => {
    if (operationRef.current || cancelPending) return;
    if (!isUsageImportFile(file)) {
      showNotification(t('usage_maintenance.transfer_invalid_file'), 'error');
      return;
    }
    const pending = { file, sessionId, generation: contextGenerationRef.current };
    pendingSessionIdRef.current = undefined;
    pendingImportRef.current = pending;
    setPendingImport(pending);
    onOpenPanel('import');
  };

  const confirmImport = () => {
    const pending = pendingImport;
    if (
      !pending ||
      pendingImportRef.current !== pending ||
      panelRef.current !== 'import' ||
      !mountedRef.current ||
      pending.generation !== contextGenerationRef.current
    )
      return;
    pendingImportRef.current = null;
    setPendingImport(null);
    void runImport(pending.file, pending.sessionId);
  };

  const closePanel = () => {
    pendingImportRef.current = null;
    setPendingImport(null);
    pendingSessionIdRef.current = undefined;
    onClosePanel();
  };

  const handleFileChange = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    const sessionId = pendingSessionIdRef.current;
    pendingSessionIdRef.current = undefined;
    event.target.value = '';
    if (file) selectFile(file, sessionId);
  };

  const handleDrop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    setDragging(false);
    pendingSessionIdRef.current = undefined;
    const file = event.dataTransfer.files?.[0];
    if (file) selectFile(file);
  };

  const openFilePicker = () => {
    pendingSessionIdRef.current = undefined;
    inputRef.current?.click();
  };

  const handleExport = async () => {
    const generation = contextGenerationRef.current;
    setExporting(true);
    try {
      const response: UsageExportResponse = await usageServiceApi.exportUsage(
        serviceBase,
        managementKey
      );
      if (!mountedRef.current || generation !== contextGenerationRef.current) return;
      downloadBlob({ filename: response.filename || 'usage-events.jsonl', blob: response.blob });
      showNotification(
        t('usage_maintenance.transfer_export_success', {
          defaultValue: 'Sanitized usage JSONL export downloaded.',
        }),
        'success'
      );
    } catch {
      if (!mountedRef.current || generation !== contextGenerationRef.current) return;
      showNotification(
        t('usage_maintenance.transfer_export_error', {
          defaultValue: 'The export request could not be completed. Please retry.',
        }),
        'error'
      );
    } finally {
      if (mountedRef.current && generation === contextGenerationRef.current) setExporting(false);
    }
  };

  const pauseImport = () => {
    operationRef.current?.abort();
  };

  const cancelImport = async () => {
    const generation = contextGenerationRef.current;
    const task = activeTask;
    if (!task?.progress.sessionId || cancelPending) return;
    setCancelPending(true);
    operationRef.current?.abort();
    try {
      const result = await cancelUsageImportFile({
        base: serviceBase,
        managementKey,
        sessionId: task.progress.sessionId,
        file: task.file,
      });
      if (!mountedRef.current || generation !== contextGenerationRef.current) return;
      if (result && result.status !== 'completed' && result.status !== 'cancelled') {
        throw new Error('usage import cancellation did not reach a terminal state');
      }
      // A settled server result wins over late progress or errors from the aborted upload.
      operationIDRef.current += 1;
      operationRef.current = null;
      const completed = result?.status === 'completed';
      setActiveTask((current) =>
        current?.file === task.file
          ? {
              ...current,
              progress: {
                ...current.progress,
                sessionId: result?.id ?? current.progress.sessionId,
                filename: result?.filename ?? current.progress.filename,
                phase: completed ? 'completed' : 'cancelled',
                status: result?.status ?? 'cancelled',
                uploadedBytes: result?.received_bytes ?? current.progress.uploadedBytes,
                totalBytes: result?.size_bytes ?? current.progress.totalBytes,
                percent: result ? progressPercent(result) : current.progress.percent,
                result: result?.result ?? current.progress.result,
                error: undefined,
                retryable: false,
              },
            }
          : current
      );
      showNotification(
        completed
          ? t('usage_stats.import_completed_before_cancel', {
              defaultValue:
                'The import completed before cancellation took effect ({{added}} added, {{skipped}} skipped).',
              added: result.result?.added ?? 0,
              skipped: result.result?.skipped ?? 0,
            })
          : result
            ? t('usage_maintenance.transfer_cancelled', {
                defaultValue: 'Import session cancelled.',
              })
            : t('usage_maintenance.transfer_error_not_found'),
        !result || completed ? 'warning' : 'success'
      );
      await loadSessions(true);
    } catch (cause) {
      if (!mountedRef.current || generation !== contextGenerationRef.current) return;
      showNotification(formatImportError(cause, t), 'error');
    } finally {
      if (mountedRef.current && generation === contextGenerationRef.current) {
        setCancelPending(false);
        onUsageChanged?.();
      }
    }
  };

  const activeSession = useMemo(
    () =>
      activeTask?.progress.sessionId
        ? sessionList?.sessions.find((session) => session.id === activeTask.progress.sessionId)
        : undefined,
    [activeTask?.progress.sessionId, sessionList?.sessions]
  );

  const handleSessionAction = (session: UsageImportSession) => {
    const task = activeTask;
    const current = activeTask?.progress;
    if (current?.sessionId === session.id) {
      onOpenPanel('import-session', session.id);
      if (
        current.phase === 'paused' ||
        (current.phase === 'failed' && current.retryable !== false)
      ) {
        if (task) void runImport(task.file, session.id);
      } else if (current.phase === 'uploading' || current.phase === 'processing') {
        pauseImport();
      }
      return;
    }
    if (session.status === 'processing') {
      onOpenPanel('import-session', session.id);
      void loadSessions(true);
      return;
    }
    if (
      resumableStatuses.has(session.status) ||
      (session.status === 'failed' && session.retryable)
    ) {
      if (operationRef.current || cancelPending) return;
      onOpenPanel('import-session', session.id);
      pendingSessionIdRef.current = session.id;
      inputRef.current?.click();
    }
  };

  const statusLabel = (status: UsageImportSessionStatus) =>
    t(`usage_maintenance.transfer_status_${status}`, { defaultValue: status });

  const sessionActionLabel = (session: UsageImportSession) => {
    if (activeTask?.progress.sessionId === session.id) {
      if (
        activeTask.progress.phase === 'paused' ||
        (activeTask.progress.phase === 'failed' && activeTask.progress.retryable !== false)
      ) {
        return t('usage_maintenance.transfer_resume', { defaultValue: 'Resume upload' });
      }
      if (activeTask.progress.phase === 'uploading' || activeTask.progress.phase === 'processing') {
        return t('usage_maintenance.transfer_pause', { defaultValue: 'Pause' });
      }
    }
    if (session.status === 'processing') {
      return t('common.refresh', { defaultValue: 'Refresh' });
    }
    if (session.status === 'failed' && session.retryable) {
      return t('common.retry', { defaultValue: 'Retry' });
    }
    if (
      resumableStatuses.has(session.status) ||
      (session.status === 'failed' && session.retryable)
    ) {
      return t('usage_maintenance.transfer_resume', { defaultValue: 'Continue upload' });
    }
    return t('usage_maintenance.details', { defaultValue: 'Details' });
  };

  const sessionHasAction = (session: UsageImportSession) =>
    session.status === 'processing' ||
    resumableStatuses.has(session.status) ||
    (session.status === 'failed' && session.retryable === true);

  const sessions = sessionList?.sessions ?? [];
  const selectedSession =
    sessions.find((session) => session.id === selectedSessionId) ?? detailSession;
  const showingActiveTask = Boolean(
    activeTask &&
    !pendingImport &&
    (panel === 'import' ||
      (panel === 'import-session' && activeTask.progress.sessionId === selectedSessionId))
  );
  const activeProgress = showingActiveTask ? activeTask?.progress : undefined;
  const displayedResult =
    activeProgress?.result ?? (showingActiveTask ? activeSession?.result : selectedSession?.result);
  const operationBusy = Boolean(
    activeTask && ['preparing', 'uploading', 'processing'].includes(activeTask.progress.phase)
  );
  const displayStatus = (session: UsageImportSession) =>
    session.status === 'completed' && hasImportIssues(session.result)
      ? t('usage_maintenance.import_partial')
      : statusLabel(session.status);
  const confirmCancelImport = () => {
    const generation = contextGenerationRef.current;
    const cancellationOperationID = operationIDRef.current;
    showConfirmation({
      title: t('usage_stats.import_cancel'),
      message: t('usage_maintenance.import_cancel_note'),
      confirmText: t('usage_stats.import_cancel'),
      cancelText: t('common.back'),
      variant: 'danger',
      onConfirm: () => {
        if (
          mountedRef.current &&
          generation === contextGenerationRef.current &&
          cancellationOperationID === operationIDRef.current
        ) {
          void cancelImport();
        }
      },
    });
  };

  return (
    <div className={styles.view}>
      <input
        ref={inputRef}
        type="file"
        accept=".json,.jsonl,.ndjson,.txt,application/json,application/x-ndjson,text/plain"
        className={styles.hiddenInput}
        onChange={handleFileChange}
      />
      {error ? (
        <div className={styles.error} role="alert">
          {error}
          <Button
            size="sm"
            variant="secondary"
            disabled={loading || refreshing}
            onClick={() => void loadSessions()}
          >
            {t('common.retry')}
          </Button>
        </div>
      ) : null}
      {activeTask && !['completed', 'cancelled'].includes(activeTask.progress.phase) ? (
        <div className={styles.activity}>
          <div>
            <strong>{activeTask.file.name}</strong>
            <span>{t(`usage_stats.import_phase_${activeTask.progress.phase}`)}</span>
          </div>
          <Button size="sm" variant="secondary" onClick={() => onOpenPanel('import')}>
            {t('usage_maintenance.details')}
          </Button>
        </div>
      ) : null}
      <section className={styles.sessions} aria-busy={loading || refreshing}>
        <div className={styles.sectionHeader}>
          <h2>{t('usage_maintenance.transfer_sessions_title')}</h2>
          <span>
            {sessionList && !error
              ? t('usage_maintenance.transfer_session_count', {
                  current: sessionList.active_sessions,
                  total: sessionList.max_sessions,
                })
              : '—'}
          </span>
        </div>
        <table className={styles.sessionTable}>
          <thead>
            <tr>
              <th>{t('usage_maintenance.import_filename')}</th>
              <th>{t('usage_maintenance.upload_progress')}</th>
              <th>{t('usage_maintenance.transfer_last_result')}</th>
              <th>{t('usage_maintenance.technical_status')}</th>
              <th>
                <span className={styles.srOnly}>{t('usage_maintenance.record_actions')}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {sessions.map((session) => (
              <tr key={session.id} data-session-id={session.id}>
                <td data-label={t('usage_maintenance.import_filename')}>
                  <button
                    type="button"
                    className={styles.filename}
                    onClick={() => onOpenPanel('import-session', session.id)}
                  >
                    {session.filename}
                  </button>
                  <small>{formatDateTime(new Date(session.updated_at_ms), i18n.language)}</small>
                </td>
                <td data-label={t('usage_maintenance.upload_progress')}>
                  <span>{progressPercent(session)}%</span>
                  <div
                    className={styles.miniProgress}
                    role="progressbar"
                    aria-label={t('usage_maintenance.upload_progress')}
                    aria-valuemin={0}
                    aria-valuemax={100}
                    aria-valuenow={progressPercent(session)}
                  >
                    <i style={{ width: `${progressPercent(session)}%` }} />
                  </div>
                  <small>
                    {formatFileSize(session.received_bytes)} / {formatFileSize(session.size_bytes)}
                  </small>
                </td>
                <td data-label={t('usage_maintenance.transfer_last_result')}>
                  <span>{resultSummary(session.result, t)}</span>
                </td>
                <td data-label={t('usage_maintenance.technical_status')}>
                  <span className={`${styles.pill} ${statusTone(session.status, session.result)}`}>
                    {displayStatus(session)}
                  </span>
                </td>
                <td className={styles.rowActions}>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => onOpenPanel('import-session', session.id)}
                  >
                    {t('usage_maintenance.details')}
                  </Button>
                  {sessionHasAction(session) ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={
                        cancelPending ||
                        (operationBusy &&
                          activeTask?.progress.sessionId !== session.id &&
                          session.status !== 'processing')
                      }
                      onClick={() => handleSessionAction(session)}
                    >
                      {sessionActionLabel(session)}
                    </Button>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {sessions.length === 0 && loading ? (
          <div className={styles.empty}>{t('common.loading')}</div>
        ) : null}
        {sessions.length === 0 && !loading && !error && sessionList ? (
          <div className={styles.empty}>
            {t('usage_maintenance.transfer_no_sessions')}
            <Button size="sm" onClick={() => onOpenPanel('import')}>
              {t('usage_maintenance.import_file')}
            </Button>
          </div>
        ) : null}
        <p className={styles.listNote}>{t('usage_maintenance.recent_imports_note')}</p>
      </section>
      <Drawer
        open={panel !== null}
        onClose={closePanel}
        width="min(600px, 100vw)"
        className={drawerClassName}
        bodyRef={drawerBodyRef}
        title={
          panel === 'export'
            ? t('usage_maintenance.export_online')
            : pendingImport
              ? t(
                  pendingImport.sessionId
                    ? 'usage_maintenance.transfer_resume_confirm_title'
                    : 'usage_maintenance.transfer_import_confirm_title'
                )
              : panel === 'import-session'
                ? t('usage_maintenance.import_details')
                : t('usage_maintenance.import_file')
        }
        footer={
          <div className={styles.drawerActions}>
            {panel === 'export' ? (
              <>
                <Button variant="secondary" onClick={closePanel}>
                  {t('common.close')}
                </Button>
                <Button loading={exporting} onClick={() => void handleExport()}>
                  <IconDownload size={16} />
                  {t('usage_maintenance.transfer_export_button')}
                </Button>
              </>
            ) : pendingImport ? (
              <>
                <Button
                  variant="secondary"
                  onClick={() => {
                    pendingImportRef.current = null;
                    setPendingImport(null);
                  }}
                >
                  {t('common.back')}
                </Button>
                <Button onClick={confirmImport}>
                  {t('usage_maintenance.transfer_import_confirm_button')}
                </Button>
              </>
            ) : activeProgress ? (
              <>
                <UsageImportProgressActions
                  progress={activeProgress}
                  busy={cancelPending}
                  onPause={pauseImport}
                  onCancel={confirmCancelImport}
                  onResume={() => {
                    if (activeTask)
                      void runImport(activeTask.file, activeProgress.sessionId || undefined);
                  }}
                />
                <Button
                  variant={
                    activeProgress.phase === 'completed' || activeProgress.phase === 'cancelled'
                      ? 'primary'
                      : 'secondary'
                  }
                  onClick={closePanel}
                >
                  {activeProgress.phase === 'completed' || activeProgress.phase === 'cancelled'
                    ? t('usage_maintenance.done')
                    : t('usage_maintenance.minimize')}
                </Button>
              </>
            ) : (
              <>
                {panel === 'import-session' &&
                selectedSession &&
                sessionHasAction(selectedSession) ? (
                  <Button
                    onClick={() => handleSessionAction(selectedSession)}
                    disabled={cancelPending || operationBusy}
                  >
                    {sessionActionLabel(selectedSession)}
                  </Button>
                ) : null}
                <Button variant="secondary" onClick={closePanel}>
                  {t('common.close')}
                </Button>
              </>
            )}
          </div>
        }
      >
        {panel === 'export' ? (
          <div className={styles.drawerBody}>
            <IconDownload size={26} />
            <h3>{t('usage_maintenance.transfer_export_title')}</h3>
            <p>{t('usage_maintenance.transfer_export_note')}</p>
          </div>
        ) : pendingImport ? (
          <div className={styles.drawerBody}>
            <div className={styles.fileSummary}>
              <IconArrowUpFromLine size={22} />
              <strong>{pendingImport.file.name}</strong>
              <span>{formatFileSize(pendingImport.file.size)}</span>
            </div>
            <p>
              {t('usage_maintenance.transfer_import_confirm_message', {
                name: pendingImport.file.name,
              })}
            </p>
            <p className={styles.muted}>{t('usage_maintenance.transfer_dedupe_note')}</p>
          </div>
        ) : activeProgress ? (
          <div className={styles.drawerBody}>
            {activeProgress.phase === 'completed' ? (
              <div className={hasImportIssues(displayedResult) ? styles.warning : styles.success}>
                <h3>
                  {t(
                    hasImportIssues(displayedResult)
                      ? 'usage_maintenance.import_partial'
                      : 'usage_maintenance.import_complete'
                  )}
                </h3>
              </div>
            ) : null}
            <UsageImportProgressView progress={activeProgress} />
            {displayedResult ? <p>{resultSummary(displayedResult, t)}</p> : null}
            {displayedResult?.warnings?.length ? (
              <details>
                <summary>{t('usage_maintenance.import_warnings')}</summary>
                <ul>
                  {displayedResult.warnings.map((warning, index) => (
                    <li key={index}>{warning}</li>
                  ))}
                </ul>
              </details>
            ) : null}
            <p className={styles.muted}>{t('usage_maintenance.import_cancel_note')}</p>
            {!operationBusy && !cancelPending ? (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setActiveTask(null);
                  openFilePicker();
                }}
              >
                {t('usage_maintenance.import_another')}
              </Button>
            ) : null}
          </div>
        ) : panel === 'import-session' ? (
          selectedSession ? (
            <div className={styles.drawerBody}>
              <span
                className={`${styles.pill} ${statusTone(selectedSession.status, selectedSession.result)}`}
              >
                {displayStatus(selectedSession)}
              </span>
              <h3 className={styles.fileTitle}>{selectedSession.filename}</h3>
              <div>
                <p>
                  {t('usage_maintenance.upload_progress')} · {progressPercent(selectedSession)}%
                </p>
                <div
                  className={styles.miniProgress}
                  role="progressbar"
                  aria-label={t('usage_maintenance.upload_progress')}
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={progressPercent(selectedSession)}
                >
                  <i style={{ width: `${progressPercent(selectedSession)}%` }} />
                </div>
                <p className={styles.muted}>
                  {formatFileSize(selectedSession.received_bytes)} /{' '}
                  {formatFileSize(selectedSession.size_bytes)}
                </p>
              </div>
              {selectedSession.status === 'processing' ? (
                <p className={styles.muted}>{t('usage_stats.import_processing_hint')}</p>
              ) : null}
              <p>{resultSummary(selectedSession.result, t)}</p>
              {selectedSession.result?.warnings?.length ? (
                <details>
                  <summary>{t('usage_maintenance.import_warnings')}</summary>
                  <ul>
                    {selectedSession.result.warnings.map((warning, index) => (
                      <li key={index}>{warning}</li>
                    ))}
                  </ul>
                </details>
              ) : null}
              {selectedSession.status === 'failed' ? (
                <p className={styles.warning}>
                  {t(
                    selectedSession.retryable
                      ? 'usage_maintenance.transfer_retryable'
                      : 'usage_maintenance.transfer_failed'
                  )}
                </p>
              ) : null}
              {selectedSession.status === 'cancelled' ? (
                <p className={styles.muted}>{t('usage_maintenance.import_cancel_note')}</p>
              ) : null}
              <details className={styles.technical}>
                <summary>{t('usage_maintenance.workspace_technical')}</summary>
                <dl>
                  <div>
                    <dt>{t('usage_maintenance.technical_run_id')}</dt>
                    <dd>{selectedSession.id}</dd>
                  </div>
                  <div>
                    <dt>{t('usage_maintenance.transfer_chunk')}</dt>
                    <dd>{formatFileSize(selectedSession.chunk_size_bytes)}</dd>
                  </div>
                  <div>
                    <dt>{t('usage_maintenance.transfer_expires')}</dt>
                    <dd>
                      {formatDateTime(new Date(selectedSession.expires_at_ms), i18n.language)}
                    </dd>
                  </div>
                  <div>
                    <dt>{t('usage_maintenance.transfer_retryable_label')}</dt>
                    <dd>{t(selectedSession.retryable ? 'common.yes' : 'common.no')}</dd>
                  </div>
                </dl>
              </details>
            </div>
          ) : (
            <div className={styles.drawerBody}>
              <p>{detailError ?? t('common.loading')}</p>
              {detailError ? (
                <Button variant="secondary" onClick={() => void loadSessions(true)}>
                  <IconRefreshCw size={16} />
                  {t('common.retry')}
                </Button>
              ) : null}
            </div>
          )
        ) : (
          <div className={styles.drawerBody}>
            <div
              className={`${styles.uploadBox} ${dragging ? styles.uploadBoxDragging : ''}`}
              role="button"
              tabIndex={0}
              onClick={openFilePicker}
              onKeyDown={(event) => {
                if (event.key === 'Enter' || event.key === ' ') {
                  event.preventDefault();
                  openFilePicker();
                }
              }}
              onDragEnter={(event) => {
                event.preventDefault();
                setDragging(true);
              }}
              onDragOver={(event) => event.preventDefault()}
              onDragLeave={() => setDragging(false)}
              onDrop={handleDrop}
            >
              <IconArrowUpFromLine size={28} />
              <strong>{t('usage_maintenance.transfer_drop_title')}</strong>
              <span>{t('usage_maintenance.transfer_supported_formats')}</span>
            </div>
            <p className={styles.muted}>
              {t('usage_maintenance.transfer_chunk_note', {
                quota: sessionList ? formatFileSize(sessionList.disk_quota_bytes) : '—',
              })}
            </p>
            <p className={styles.muted}>{t('usage_maintenance.transfer_dedupe_note')}</p>
            <details className={styles.technical}>
              <summary>{t('usage_maintenance.transfer_upload_details')}</summary>
              <dl>
                <div>
                  <dt>{t('usage_maintenance.transfer_chunk')}</dt>
                  <dd>{sessionList ? formatFileSize(sessionList.chunk_size_bytes) : '—'}</dd>
                </div>
                <div>
                  <dt>{t('usage_maintenance.transfer_disk_quota')}</dt>
                  <dd>{sessionList ? formatFileSize(sessionList.disk_quota_bytes) : '—'}</dd>
                </div>
                <div>
                  <dt>{t('usage_maintenance.transfer_concurrent')}</dt>
                  <dd>
                    {sessionList
                      ? `${sessionList.active_sessions} / ${sessionList.max_sessions}`
                      : '—'}
                  </dd>
                </div>
                <div>
                  <dt>{t('usage_maintenance.transfer_expires')}</dt>
                  <dd>{sessionList ? formatTTL(sessionList.ttl_seconds, t) : '—'}</dd>
                </div>
              </dl>
            </details>
          </div>
        )}
      </Drawer>
    </div>
  );
}
