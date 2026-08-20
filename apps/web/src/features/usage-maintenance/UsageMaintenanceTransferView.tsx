import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
  type DragEvent,
} from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
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
  onBack: () => void;
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

const statusTone = (status: UsageImportSessionStatus) => {
  if (status === 'completed') return styles.success;
  if (status === 'failed') return styles.danger;
  if (status === 'cancelled') return styles.neutral;
  return styles.info;
};

export function UsageMaintenanceTransferView({ serviceBase, managementKey, onBack }: Props) {
  const { t, i18n } = useTranslation();
  const { showConfirmation, showNotification } = useNotificationStore();
  const inputRef = useRef<HTMLInputElement | null>(null);
  const pendingSessionIdRef = useRef<string | undefined>(undefined);
  const operationRef = useRef<AbortController | null>(null);
  const operationIDRef = useRef(0);
  const sessionRequestRef = useRef<AbortController | null>(null);
  const sessionRequestIDRef = useRef(0);
  const [cancelPending, setCancelPending] = useState(false);
  const [sessionList, setSessionList] = useState<UsageImportSessionList | null>(null);
  const [activeTask, setActiveTask] = useState<ActiveTask | null>(null);
  const [selectedSessionId, setSelectedSessionId] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [error, setError] = useState<string | null>(null);

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
        const result = await usageServiceApi.listUsageImportSessions(serviceBase, managementKey, {
          limit: 20,
        }, controller.signal);
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
      operationIDRef.current += 1;
      operationRef.current?.abort();
      operationRef.current = null;
      sessionRequestIDRef.current += 1;
      sessionRequestRef.current?.abort();
      sessionRequestRef.current = null;
    };
  }, [loadSessions]);

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
    setActiveTask((current) => (current?.file === file ? { file, progress } : { file, progress }));
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
          (result.failed ?? 0) > 0 || (result.unsupported ?? 0) > 0 ? 'warning' : 'success'
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
        }
      }
    },
    [loadSessions, managementKey, serviceBase, showNotification, t, updateProgress]
  );

  const selectFile = useCallback(
    (file: File, sessionId?: string) => {
      pendingSessionIdRef.current = sessionId;
      showConfirmation({
        title: sessionId
          ? t('usage_maintenance.transfer_resume_confirm_title', {
              defaultValue: 'Resume this import session?',
            })
          : t('usage_maintenance.transfer_import_confirm_title', {
              defaultValue: 'Import usage data?',
            }),
        message: t('usage_maintenance.transfer_import_confirm_message', {
          defaultValue:
            'The selected file will be uploaded in resumable chunks and deduplicated by the Manager Server identity ledger. Raw diagnostics are not exported or displayed here.',
          name: file.name,
        }),
        confirmText: t('usage_maintenance.transfer_import_confirm_button', {
          defaultValue: sessionId ? 'Resume upload' : 'Start import',
        }),
        variant: 'primary',
        onConfirm: () => {
          pendingSessionIdRef.current = undefined;
          return runImport(file, sessionId);
        },
        onCancel: () => {
          pendingSessionIdRef.current = undefined;
        },
      });
    },
    [runImport, showConfirmation, t]
  );

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
    setExporting(true);
    try {
      const response: UsageExportResponse = await usageServiceApi.exportUsage(
        serviceBase,
        managementKey
      );
      downloadBlob({ filename: response.filename || 'usage-events.jsonl', blob: response.blob });
      showNotification(
        t('usage_maintenance.transfer_export_success', {
          defaultValue: 'Sanitized usage JSONL export downloaded.',
        }),
        'success'
      );
    } catch (cause) {
      showNotification(formatImportError(cause, t), 'error');
    } finally {
      setExporting(false);
    }
  };

  const pauseImport = () => {
    operationRef.current?.abort();
  };

  const cancelImport = async () => {
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
      if (result) {
        setActiveTask((current) =>
          current?.file === task.file
            ? {
                ...current,
                progress: {
                  ...current.progress,
                  sessionId: result.id,
                  filename: result.filename,
                  phase: 'cancelled',
                  status: result.status,
                  uploadedBytes: result.received_bytes,
                  totalBytes: result.size_bytes,
                  percent: progressPercent(result),
                  result: result.result,
                },
              }
            : current
        );
      }
      showNotification(
        t('usage_maintenance.transfer_cancelled', { defaultValue: 'Import session cancelled.' }),
        'success'
      );
      await loadSessions(true);
    } catch (cause) {
      showNotification(formatImportError(cause, t), 'error');
    } finally {
      setCancelPending(false);
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
      void loadSessions(true);
      return;
    }
    if (
      resumableStatuses.has(session.status) ||
      (session.status === 'failed' && session.retryable)
    ) {
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

  if (loading && !sessionList) {
    return (
      <div className={styles.loading}>{t('common.loading', { defaultValue: 'Loading…' })}</div>
    );
  }

  const sessions = sessionList?.sessions ?? [];
  const activeProgress = activeTask?.progress;
  const activeProgressSession = activeProgress?.sessionId
    ? sessions.find((session) => session.id === activeProgress.sessionId)
    : activeSession;

  return (
    <div className={styles.view}>
      <header className={styles.pageHeader}>
        <div>
          <h1>
            {t('usage_maintenance.transfer_page_title', { defaultValue: 'Import / export usage' })}
          </h1>
          <p>
            {t('usage_maintenance.transfer_page_subtitle', {
              defaultValue:
                'Export sanitized JSONL and import compatible usage formats with resumable sessions.',
            })}
          </p>
        </div>
        <div className={styles.headerActions}>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void loadSessions(true)}
            disabled={refreshing}
          >
            {t('common.refresh', { defaultValue: 'Refresh' })}
          </Button>
          <Button variant="secondary" size="sm" onClick={onBack}>
            ← {t('common.back', { defaultValue: 'Back' })}
          </Button>
        </div>
      </header>

      {error ? (
        <div className={styles.error} role="alert">
          {error}
        </div>
      ) : null}

      <div className={styles.layout}>
        <div className={styles.leftColumn}>
          <section className={styles.card}>
            <div className={styles.sectionHeader}>
              <h2>
                {t('usage_maintenance.transfer_import_title', { defaultValue: 'Import usage' })}
              </h2>
              <span className={`${styles.pill} ${styles.info}`}>
                {t('usage_maintenance.transfer_resumable', { defaultValue: 'Resumable' })}
              </span>
            </div>
            <input
              ref={inputRef}
              type="file"
              accept=".json,.jsonl,.ndjson,.txt,application/json,application/x-ndjson,text/plain"
              className={styles.hiddenInput}
              onChange={handleFileChange}
            />
            <div
              className={`${styles.uploadBox} ${dragging ? styles.uploadBoxDragging : ''}`}
              role="button"
              tabIndex={0}
              onClick={openFilePicker}
              onKeyDown={(event) => {
                if (event.key === 'Enter' || event.key === ' ') openFilePicker();
              }}
              onDragEnter={(event) => {
                event.preventDefault();
                setDragging(true);
              }}
              onDragOver={(event) => event.preventDefault()}
              onDragLeave={() => setDragging(false)}
              onDrop={handleDrop}
            >
              <span className={styles.cloud} aria-hidden="true">
                ☁
              </span>
              <strong>
                {t('usage_maintenance.transfer_drop_title', {
                  defaultValue: 'Drop a file here, or choose a file',
                })}
              </strong>
              <span>
                {t('usage_maintenance.transfer_supported_formats', {
                  defaultValue: 'JSONL / JSON array / legacy usage export / legacy payload',
                })}
              </span>
              <small>
                {t('usage_maintenance.transfer_chunk_note', {
                  defaultValue:
                    'Uploads use 4 MiB chunks; total file size is governed by the server disk quota.',
                  chunk: formatFileSize(sessionList?.chunk_size_bytes ?? 0),
                })}
              </small>
            </div>
            <div className={styles.statGrid}>
              <div className={styles.statBox}>
                <span>{t('usage_maintenance.transfer_chunk', { defaultValue: 'Chunk' })}</span>
                <strong>{formatFileSize(sessionList?.chunk_size_bytes ?? 0)}</strong>
              </div>
              <div className={styles.statBox}>
                <span>
                  {t('usage_maintenance.transfer_disk_quota', { defaultValue: 'Disk quota' })}
                </span>
                <strong>{formatFileSize(sessionList?.disk_quota_bytes ?? 0)}</strong>
              </div>
              <div className={styles.statBox}>
                <span>
                  {t('usage_maintenance.transfer_concurrent', { defaultValue: 'Active sessions' })}
                </span>
                <strong>
                  {sessionList
                    ? `${sessionList.active_sessions} / ${sessionList.max_sessions}`
                    : '—'}
                </strong>
              </div>
              <div className={styles.statBox}>
                <span>TTL</span>
                <strong>{formatTTL(sessionList?.ttl_seconds ?? 0, t)}</strong>
              </div>
            </div>
            <p className={styles.infoNote}>
              {t('usage_maintenance.transfer_dedupe_note', {
                defaultValue:
                  'Imports enter the InsertEvents and identity-ledger deduplication path. CPA account snapshots are not fetched for ordinary file imports; matching depends on the file data.',
              })}
            </p>
          </section>

          <section className={styles.card}>
            <h2>
              {t('usage_maintenance.transfer_export_title', { defaultValue: 'Export usage' })}
            </h2>
            <p>
              {t('usage_maintenance.transfer_export_note', {
                defaultValue:
                  'Generate a sanitized JSONL export. Local fail_body, raw_json, paths, and checksums are not exposed in the downloaded content.',
              })}
            </p>
            <Button onClick={() => void handleExport()} loading={exporting}>
              ⇩{' '}
              {t('usage_maintenance.transfer_export_button', {
                defaultValue: 'Export sanitized JSONL',
              })}
            </Button>
          </section>
        </div>

        <section className={styles.card}>
          <div className={styles.sectionHeader}>
            <h2>
              {t('usage_maintenance.transfer_sessions_title', { defaultValue: 'Import sessions' })}
            </h2>
            <span className={styles.muted}>
              {t('usage_maintenance.transfer_session_count', {
                defaultValue: 'Current {{current}} / {{total}}',
                current: sessionList?.active_sessions ?? 0,
                total: sessionList?.max_sessions ?? 0,
              })}
            </span>
          </div>

          {activeProgress &&
          activeProgress.phase !== 'completed' &&
          activeProgress.phase !== 'cancelled' ? (
            <div className={styles.activeCard} aria-live="polite">
              <div className={styles.sessionHeader}>
                <div>
                  <strong>{activeProgress.filename}</strong>
                  <span>
                    {activeProgress.sessionId ||
                      t('usage_maintenance.transfer_preparing', {
                        defaultValue: 'Preparing session…',
                      })}
                  </span>
                </div>
                <span
                  className={`${styles.pill} ${statusTone(activeProgress.status ?? 'uploading')}`}
                >
                  {t(
                    `usage_maintenance.transfer_status_${activeProgress.status ?? activeProgress.phase}`,
                    { defaultValue: activeProgress.phase }
                  )}
                </span>
              </div>
              <div
                className={styles.progressTrack}
                role="progressbar"
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={activeProgress.percent}
              >
                <i style={{ width: `${activeProgress.percent}%` }} />
              </div>
              <div className={styles.progressLabel}>
                <span>
                  {formatFileSize(activeProgress.uploadedBytes)} /{' '}
                  {formatFileSize(activeProgress.totalBytes)}
                </span>
                <strong>{activeProgress.percent}%</strong>
              </div>
              {activeProgress.phase === 'processing' ? (
                <p className={styles.infoNote}>
                  {t('usage_maintenance.transfer_processing_note', {
                    defaultValue:
                      'Upload complete. Manager Server is streaming the file into SQLite; this may take a while for large histories.',
                  })}
                </p>
              ) : null}
              {activeProgressSession ? (
                <dl className={styles.activeFacts}>
                  <div>
                    <dt>{t('usage_maintenance.transfer_chunk', { defaultValue: 'Chunk size' })}</dt>
                    <dd>{formatFileSize(activeProgressSession.chunk_size_bytes)}</dd>
                  </div>
                  <div>
                    <dt>{t('usage_maintenance.transfer_expires', { defaultValue: 'Expires' })}</dt>
                    <dd>
                      {formatDateTime(new Date(activeProgressSession.expires_at_ms), i18n.language)}
                    </dd>
                  </div>
                  <div>
                    <dt>
                      {t('usage_maintenance.transfer_retryable_label', {
                        defaultValue: 'Retryable',
                      })}
                    </dt>
                    <dd>
                      {activeProgressSession.retryable
                        ? t('common.yes', { defaultValue: 'Yes' })
                        : t('common.no', { defaultValue: 'No' })}
                    </dd>
                  </div>
                </dl>
              ) : null}
              {activeProgress.error ? (
                <p className={styles.errorNote}>{activeProgress.error}</p>
              ) : null}
              <div className={styles.footerActions}>
                {activeProgress.sessionId ? (
                  <Button
                    variant="danger"
                    onClick={() => void cancelImport()}
                    disabled={cancelPending}
                  >
                    {t('usage_maintenance.transfer_cancel_session', {
                      defaultValue: 'Cancel session',
                    })}
                  </Button>
                ) : null}
                <Button variant="secondary" onClick={pauseImport}>
                  {t('usage_maintenance.transfer_pause', { defaultValue: 'Pause' })}
                </Button>
              </div>
            </div>
          ) : null}

          {activeProgressSession?.result ? (
            <div className={styles.resultCard}>
              <strong>
                {t('usage_maintenance.transfer_last_result', { defaultValue: 'Latest result' })}
              </strong>
              <span>{resultSummary(activeProgressSession.result, t)}</span>
              {(activeProgressSession.result.unsupported ?? 0) > 0 ? (
                <span>
                  {t('usage_maintenance.transfer_unsupported_count', {
                    defaultValue: '{{count}} unsupported',
                    count: activeProgressSession.result.unsupported,
                  })}
                </span>
              ) : null}
            </div>
          ) : null}

          <div className={styles.tableWrap}>
            <table>
              <thead>
                <tr>
                  <th>{t('usage_maintenance.transfer_file', { defaultValue: 'File' })}</th>
                  <th>{t('usage_maintenance.transfer_status', { defaultValue: 'Status' })}</th>
                  <th>{t('usage_maintenance.transfer_size', { defaultValue: 'Size' })}</th>
                  <th>{t('usage_maintenance.transfer_result', { defaultValue: 'Result' })}</th>
                  <th>{t('usage_maintenance.transfer_updated', { defaultValue: 'Updated' })}</th>
                  <th>{t('common.action', { defaultValue: 'Action' })}</th>
                </tr>
              </thead>
              <tbody>
                {sessions.map((session) => (
                  <tr
                    key={session.id}
                    className={selectedSessionId === session.id ? styles.selectedRow : undefined}
                  >
                    <td>
                      <strong className={styles.filename}>{session.filename}</strong>
                      <small className={styles.sessionID}>{session.id}</small>
                    </td>
                    <td>
                      <span className={`${styles.pill} ${statusTone(session.status)}`}>
                        {statusLabel(session.status)}
                      </span>
                    </td>
                    <td>
                      <span>{formatFileSize(session.size_bytes)}</span>
                      {activeStatuses.has(session.status) ? (
                        <div className={styles.miniProgress}>
                          <i style={{ width: `${progressPercent(session)}%` }} />
                        </div>
                      ) : null}
                    </td>
                    <td>
                      {session.status === 'failed'
                        ? session.retryable
                          ? t('usage_maintenance.transfer_retryable', { defaultValue: 'Retryable' })
                          : t('usage_maintenance.transfer_failed', {
                              defaultValue: 'Needs attention',
                            })
                        : resultSummary(session.result, t)}
                    </td>
                    <td>{formatDateTime(new Date(session.updated_at_ms), i18n.language)}</td>
                    <td>
                      <div className={styles.rowActions}>
                        <Button
                          size="xs"
                          variant="ghost"
                          onClick={() => setSelectedSessionId(session.id)}
                        >
                          {t('usage_maintenance.details', { defaultValue: 'Details' })}
                        </Button>
                        {sessionHasAction(session) ? (
                          <Button
                            size="xs"
                            variant={session.status === 'failed' ? 'primary' : 'secondary'}
                            onClick={() => handleSessionAction(session)}
                          >
                            {sessionActionLabel(session)}
                          </Button>
                        ) : null}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {selectedSessionId
            ? (() => {
                const session = sessions.find((item) => item.id === selectedSessionId);
                if (!session) return null;
                return (
                  <div className={styles.detailCard}>
                    <div>
                      <strong>{session.filename}</strong>
                      <span>{session.id}</span>
                    </div>
                    <dl>
                      <div>
                        <dt>
                          {t('usage_maintenance.transfer_chunk', { defaultValue: 'Chunk size' })}
                        </dt>
                        <dd>{formatFileSize(session.chunk_size_bytes)}</dd>
                      </div>
                      <div>
                        <dt>
                          {t('usage_maintenance.transfer_expires', { defaultValue: 'Expires' })}
                        </dt>
                        <dd>{formatDateTime(new Date(session.expires_at_ms), i18n.language)}</dd>
                      </div>
                      <div>
                        <dt>
                          {t('usage_maintenance.transfer_retryable_label', {
                            defaultValue: 'Retryable',
                          })}
                        </dt>
                        <dd>
                          {session.retryable
                            ? t('common.yes', { defaultValue: 'Yes' })
                            : t('common.no', { defaultValue: 'No' })}
                        </dd>
                      </div>
                    </dl>
                    {session.result ? <p>{resultSummary(session.result, t)}</p> : null}
                  </div>
                );
              })()
            : null}
          {sessions.length === 0 ? (
            <p className={styles.empty}>
              {t('usage_maintenance.transfer_no_sessions', {
                defaultValue: 'No import sessions yet.',
              })}
            </p>
          ) : null}
        </section>
      </div>

      <div className={styles.flowNote}>
        <strong>
          {t('usage_maintenance.transfer_flow_title', { defaultValue: 'Session state flow' })}
        </strong>
        <span className={`${styles.pill} ${styles.info}`}>{statusLabel('uploading')}</span> →
        <span className={`${styles.pill} ${styles.info}`}>{statusLabel('ready')}</span> →
        <span className={`${styles.pill} ${styles.info}`}>{statusLabel('processing')}</span> →
        <span className={`${styles.pill} ${styles.info}`}>
          {statusLabel('completed')} / {statusLabel('failed')} / {statusLabel('cancelled')}
        </span>
        <span>
          {t('usage_maintenance.transfer_flow_note', {
            defaultValue:
              'Completed sessions expose numeric added, skipped, failed, unsupported, and warning counts without raw payloads.',
          })}
        </span>
      </div>
    </div>
  );
}
