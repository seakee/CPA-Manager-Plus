import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ChangeEvent,
  type DragEvent,
} from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { EmptyState } from '@/components/ui/EmptyState';
import { Modal } from '@/components/ui/Modal';
import {
  IconArrowUpFromLine,
  IconDownload,
  IconFileText,
  IconRefreshCw,
} from '@/components/ui/icons';
import { SectionActionFooter } from '@/components/common/SectionActionFooter';
import {
  getUsageServiceErrorCode,
  usageServiceApi,
  type UsageImportResponse,
  type UsageImportSession,
  type UsageImportSessionList,
  type UsageImportSessionStatus,
} from '@/services/api/usageService';
import {
  cancelUsageImportFile,
  isUsageImportCancelledError,
  isUsageImportPausedError,
  uploadUsageImportFile,
  UsageImportFailedError,
  type UsageImportProgress,
} from '@/services/usageImportSession';
import { useNotificationStore } from '@/stores';
import { downloadBlob } from '@/utils/download';
import { formatDateTime, formatFileSize } from '@/utils/format';
import { isUsageImportFile } from '@/utils/usageImport';
import {
  hasImportIssues,
  importSessionProgressPercent,
  isActionableImportSession,
  isActiveImportSessionStatus,
  isResumableImportSession,
} from './usageMaintenanceModel';
import styles from './UsageMaintenancePage.module.scss';

type Props = {
  managerBase: string;
  managementKey?: string;
  onUsageChanged: () => void;
};

type ActiveTask = {
  file: File;
  progress: UsageImportProgress;
};

type TransferDrawerMode =
  | { kind: 'session'; sessionId: string }
  | { kind: 'active' }
  | null;

const IMPORT_ERROR_KEY_BY_CODE: Record<string, string> = {
  usage_import_session_too_large: 'transfer_error_too_large',
  usage_import_session_quota_exceeded: 'transfer_error_quota',
  usage_import_session_limit_exceeded: 'transfer_error_limit',
  usage_import_session_conflict: 'transfer_error_conflict',
  usage_import_session_file_mismatch: 'transfer_error_file_mismatch',
  usage_import_session_not_found: 'transfer_error_not_found',
  usage_import_session_invalid_request: 'transfer_error_invalid',
};

function formatTTL(
  seconds: number,
  t: (key: string, options?: Record<string, unknown>) => string
): string {
  const hours = Math.max(0, Math.round(seconds / 3600));
  return t('usage_maintenance.transfer_hours', { defaultValue: '{{hours}}h', hours });
}

function getSessionToneClass(status: UsageImportSessionStatus, result?: UsageImportResponse): string {
  if (status === 'completed') {
    return hasImportIssues(result) ? styles.statusWarning : styles.statusSuccess;
  }
  if (status === 'failed') return styles.statusDanger;
  if (status === 'cancelled') return styles.statusNeutral;
  return styles.statusInfo;
}

export function UsageMaintenanceTransferSection({
  managerBase,
  managementKey,
  onUsageChanged,
}: Props) {
  const { t, i18n } = useTranslation();
  const { showNotification } = useNotificationStore();

  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const pendingSessionIdRef = useRef<string | undefined>(undefined);
  const operationRef = useRef<AbortController | null>(null);
  const operationSeqRef = useRef(0);
  const sessionListSeqRef = useRef(0);
  const sessionListAbortRef = useRef<AbortController | null>(null);
  const mountedRef = useRef(true);

  const [sessionList, setSessionList] = useState<UsageImportSessionList | null>(null);
  const [sessionsLoading, setSessionsLoading] = useState(true);
  const [sessionsRefreshing, setSessionsRefreshing] = useState(false);
  const [sessionsError, setSessionsError] = useState('');

  const [exportBusy, setExportBusy] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [pendingImport, setPendingImport] = useState<{ file: File; sessionId?: string } | null>(
    null
  );
  const [activeTask, setActiveTask] = useState<ActiveTask | null>(null);
  const [cancelConfirmOpen, setCancelConfirmOpen] = useState(false);
  const [cancelBusy, setCancelBusy] = useState(false);
  const [drawerMode, setDrawerMode] = useState<TransferDrawerMode>(null);
  const [detailSession, setDetailSession] = useState<UsageImportSession | null>(null);
  const [detailError, setDetailError] = useState('');

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      operationSeqRef.current += 1;
      operationRef.current?.abort();
      operationRef.current = null;
      sessionListSeqRef.current += 1;
      sessionListAbortRef.current?.abort();
      sessionListAbortRef.current = null;
    };
  }, []);

  const formatImportError = useCallback(
    (error: unknown) => {
      const code = getUsageServiceErrorCode(error);
      const key = IMPORT_ERROR_KEY_BY_CODE[code] ?? 'transfer_error_generic';
      return t(`usage_maintenance.${key}`, {
        defaultValue: 'The import request could not be completed.',
      });
    },
    [t]
  );

  const formatResultSummary = useCallback(
    (result?: UsageImportResponse) => {
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
    },
    [t]
  );

  const loadSessions = useCallback(
    async (background = false) => {
      if (!managerBase || !managementKey) return;
      const reqId = ++sessionListSeqRef.current;
      sessionListAbortRef.current?.abort();
      const controller = new AbortController();
      sessionListAbortRef.current = controller;
      if (background) setSessionsRefreshing(true);
      else setSessionsLoading(true);

      try {
        const res = await usageServiceApi.listUsageImportSessions(
          managerBase,
          managementKey,
          { limit: 20 },
          controller.signal
        );
        if (!mountedRef.current || reqId !== sessionListSeqRef.current) return;
        if (res && Array.isArray(res.sessions)) {
          setSessionList(res);
          setSessionsError('');
        }
      } catch (err) {
        if (!mountedRef.current || controller.signal.aborted || reqId !== sessionListSeqRef.current) {
          return;
        }
        setSessionsError(formatImportError(err));
      } finally {
        if (mountedRef.current && reqId === sessionListSeqRef.current) {
          setSessionsLoading(false);
          setSessionsRefreshing(false);
          sessionListAbortRef.current = null;
        }
      }
    },
    [formatImportError, managementKey, managerBase]
  );

  useEffect(() => {
    void loadSessions(false);
  }, [loadSessions]);

  const shouldPoll = Boolean(
    activeTask?.progress.phase === 'processing' ||
      sessionList?.sessions.some((session) => isActiveImportSessionStatus(session.status))
  );

  useEffect(() => {
    if (!shouldPoll) return;
    const timer = window.setInterval(() => {
      void loadSessions(true);
    }, 5_000);
    return () => window.clearInterval(timer);
  }, [loadSessions, shouldPoll]);

  const selectedSessionId = drawerMode?.kind === 'session' ? drawerMode.sessionId : null;
  const sessions = sessionList?.sessions ?? [];
  const selectedSession =
    sessions.find((session) => session.id === selectedSessionId) ?? detailSession;

  useEffect(() => {
    setDetailSession(null);
    setDetailError('');
    if (!selectedSessionId || sessions.some((item) => item.id === selectedSessionId)) return;
    if (!managerBase || !managementKey) return;
    const controller = new AbortController();
    void usageServiceApi
      .getUsageImportSession(managerBase, selectedSessionId, managementKey, controller.signal)
      .then((session) => {
        if (!mountedRef.current || controller.signal.aborted) return;
        setDetailSession(session);
      })
      .catch((err) => {
        if (!mountedRef.current || controller.signal.aborted) return;
        setDetailError(formatImportError(err));
      });
    return () => controller.abort();
  }, [formatImportError, managementKey, managerBase, selectedSessionId, sessions]);

  const handleExportUsage = async () => {
    if (!managerBase || !managementKey || exportBusy) return;
    setExportBusy(true);
    try {
      const exported = await usageServiceApi.exportUsage(managerBase, managementKey);
      if (!mountedRef.current) return;
      downloadBlob({
        filename: exported.filename || `usage-events-${new Date().toISOString().slice(0, 10)}.jsonl`,
        blob: exported.blob,
      });
      showNotification(t('usage_maintenance.transfer_export_success'), 'success');
    } catch {
      if (!mountedRef.current) return;
      showNotification(t('usage_maintenance.transfer_export_error'), 'error');
    } finally {
      if (mountedRef.current) setExportBusy(false);
    }
  };

  const runImport = useCallback(
    async (file: File, sessionId?: string) => {
      if (!managerBase || !managementKey) return;
      if (!isUsageImportFile(file)) {
        showNotification(t('usage_maintenance.transfer_invalid_file'), 'warning');
        return;
      }
      operationRef.current?.abort();
      const controller = new AbortController();
      operationRef.current = controller;
      const opSeq = ++operationSeqRef.current;

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
      setDrawerMode({ kind: 'active' });

      try {
        const result = await uploadUsageImportFile({
          base: managerBase,
          managementKey,
          file,
          sessionId,
          signal: controller.signal,
          onProgress: (progress) => {
            if (!mountedRef.current || opSeq !== operationSeqRef.current) return;
            setActiveTask((prev) =>
              prev?.file === file && prev.progress.phase === 'cancelled'
                ? prev
                : { file, progress }
            );
          },
        });
        if (!mountedRef.current || opSeq !== operationSeqRef.current) return;
        showNotification(
          t('usage_maintenance.transfer_import_success', {
            added: result.added ?? 0,
            skipped: result.skipped ?? 0,
            failed: result.failed ?? 0,
          }),
          hasImportIssues(result) ? 'warning' : 'success'
        );
        await loadSessions(true);
      } catch (err) {
        if (!mountedRef.current || opSeq !== operationSeqRef.current) return;
        if (!isUsageImportPausedError(err) && !isUsageImportCancelledError(err)) {
          const retryable = err instanceof UsageImportFailedError ? err.retryable : true;
          const message = formatImportError(err);
          setActiveTask((prev) =>
            prev?.file === file
              ? {
                  ...prev,
                  progress: {
                    ...prev.progress,
                    phase: 'failed',
                    error: message,
                    retryable,
                  },
                }
              : prev
          );
          showNotification(message, 'error');
        }
        await loadSessions(true);
      } finally {
        if (opSeq === operationSeqRef.current) {
          operationRef.current = null;
          onUsageChanged();
        }
      }
    },
    [formatImportError, loadSessions, managementKey, managerBase, onUsageChanged, showNotification, t]
  );

  const selectFile = (file: File, sessionId?: string) => {
    if (operationRef.current || cancelBusy) return;
    if (!isUsageImportFile(file)) {
      showNotification(t('usage_maintenance.transfer_invalid_file'), 'warning');
      return;
    }
    setPendingImport({ file, sessionId });
  };

  const handleFileInputChange = (event: ChangeEvent<HTMLInputElement>) => {
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

  const openFilePicker = (resumeSessionId?: string) => {
    if (operationRef.current || cancelBusy) return;
    pendingSessionIdRef.current = resumeSessionId;
    fileInputRef.current?.click();
  };

  const handlePauseImport = () => {
    operationRef.current?.abort();
  };

  const handleConfirmCancelImport = async () => {
    setCancelConfirmOpen(false);
    const task = activeTask;
    if (!managerBase || !managementKey || !task?.progress.sessionId || cancelBusy) return;
    setCancelBusy(true);
    operationRef.current?.abort();
    try {
      const result = await cancelUsageImportFile({
        base: managerBase,
        managementKey,
        sessionId: task.progress.sessionId,
        file: task.file,
      });
      if (!mountedRef.current) return;
      operationSeqRef.current += 1;
      operationRef.current = null;
      const completed = result?.status === 'completed';
      setActiveTask((prev) =>
        prev?.file === task.file
          ? {
              ...prev,
              progress: {
                ...prev.progress,
                sessionId: result?.id ?? prev.progress.sessionId,
                filename: result?.filename ?? prev.progress.filename,
                phase: completed ? 'completed' : 'cancelled',
                status: result?.status ?? 'cancelled',
                uploadedBytes: result?.received_bytes ?? prev.progress.uploadedBytes,
                totalBytes: result?.size_bytes ?? prev.progress.totalBytes,
                percent: result ? importSessionProgressPercent(result) : prev.progress.percent,
                result: result?.result ?? prev.progress.result,
                error: undefined,
                retryable: false,
              },
            }
          : prev
      );
      showNotification(
        completed
          ? t('usage_stats.import_completed_before_cancel', {
              added: result.result?.added ?? 0,
              skipped: result.result?.skipped ?? 0,
            })
          : result
            ? t('usage_maintenance.transfer_cancelled')
            : t('usage_maintenance.transfer_error_not_found'),
        !result || completed ? 'warning' : 'success'
      );
      await loadSessions(true);
    } catch (err) {
      if (!mountedRef.current) return;
      showNotification(formatImportError(err), 'error');
    } finally {
      if (mountedRef.current) {
        setCancelBusy(false);
        onUsageChanged();
      }
    }
  };

  const handleSessionAction = (session: UsageImportSession) => {
    if (activeTask?.progress.sessionId === session.id) {
      setDrawerMode({ kind: 'active' });
      const phase = activeTask.progress.phase;
      if (phase === 'paused' || (phase === 'failed' && activeTask.progress.retryable !== false)) {
        void runImport(activeTask.file, session.id);
      } else if (phase === 'uploading' || phase === 'processing') {
        handlePauseImport();
      }
      return;
    }
    if (session.status === 'processing') {
      setDrawerMode({ kind: 'session', sessionId: session.id });
      void loadSessions(true);
      return;
    }
    if (isResumableImportSession(session)) {
      openFilePicker(session.id);
    }
  };

  const getSessionActionLabel = (session: UsageImportSession) => {
    if (activeTask?.progress.sessionId === session.id) {
      const phase = activeTask.progress.phase;
      if (phase === 'paused' || (phase === 'failed' && activeTask.progress.retryable !== false)) {
        return t('usage_maintenance.transfer_resume');
      }
      if (phase === 'uploading' || phase === 'processing') {
        return t('usage_maintenance.transfer_pause');
      }
    }
    if (session.status === 'processing') return t('common.refresh');
    if (session.status === 'failed' && session.retryable) return t('common.retry');
    if (isResumableImportSession(session)) return t('usage_maintenance.transfer_resume');
    return t('usage_maintenance.details');
  };

  const getSessionDisplayStatus = (session: UsageImportSession) =>
    session.status === 'completed' && hasImportIssues(session.result)
      ? t('usage_maintenance.import_partial')
      : t(`usage_maintenance.transfer_status_${session.status}`, { defaultValue: session.status });

  const operationBusy = Boolean(
    activeTask && ['preparing', 'uploading', 'processing'].includes(activeTask.progress.phase)
  );
  const activeProgress = activeTask?.progress ?? null;
  const drawerOpen = drawerMode !== null;

  return (
    <div className={styles.transferLayout} data-testid="usage-maintenance-transfer-tab">
      <input
        ref={fileInputRef}
        type="file"
        accept=".jsonl,.json,.ndjson,.txt,application/json,application/x-ndjson,text/plain"
        className={styles.hiddenFileInput}
        onChange={handleFileInputChange}
        data-testid="usage-maintenance-import-input"
      />

      {sessionsError ? (
        <div className={`${styles.alertBanner} ${styles.alertDanger}`} role="alert">
          <span>{sessionsError}</span>
          <Button
            variant="secondary"
            size="xs"
            disabled={sessionsLoading || sessionsRefreshing}
            onClick={() => void loadSessions(false)}
          >
            {t('common.retry')}
          </Button>
        </div>
      ) : null}

      {activeTask && !['completed', 'cancelled'].includes(activeTask.progress.phase) ? (
        <div className={`${styles.alertBanner} ${styles.alertInfo}`}>
          <span>
            <strong>{activeTask.file.name}</strong> ·{' '}
            {t(`usage_stats.import_phase_${activeTask.progress.phase}`, {
              defaultValue: activeTask.progress.phase,
            })}
          </span>
          <Button size="xs" variant="secondary" onClick={() => setDrawerMode({ kind: 'active' })}>
            {t('usage_maintenance.details')}
          </Button>
        </div>
      ) : null}

      <div className={styles.transferGrid}>
        <section className={styles.panel}>
          <div className={styles.panelHeader}>
            <div className={styles.panelHeaderCopy}>
              <h2 className={styles.panelTitle}>{t('usage_maintenance.transfer_export_title')}</h2>
              <p className={styles.panelDescription}>
                {t('usage_maintenance.transfer_export_note')}
              </p>
            </div>
          </div>
          <div className={styles.panelBody}>
            <div className={styles.flatRowCopy}>
              <span className={styles.flatRowLabel}>
                {t('usage_maintenance.advanced_sanitized_only')}
              </span>
              <span className={styles.flatRowMeta}>
                {t('usage_maintenance.transfer_flow_note')}
              </span>
            </div>
          </div>
          <div className={styles.panelFooter}>
            <SectionActionFooter>
              <Button
                variant="secondary"
                size="sm"
                loading={exportBusy}
                onClick={() => void handleExportUsage()}
                data-testid="usage-maintenance-export-btn"
              >
                <IconDownload size={15} aria-hidden="true" />
                <span>{t('usage_maintenance.transfer_export_button')}</span>
              </Button>
            </SectionActionFooter>
          </div>
        </section>

        <section className={styles.panel}>
          <div className={styles.panelHeader}>
            <div className={styles.panelHeaderCopy}>
              <h2 className={styles.panelTitle}>{t('usage_maintenance.transfer_import_title')}</h2>
              <p className={styles.panelDescription}>
                {t('usage_maintenance.transfer_dedupe_note')}
              </p>
            </div>
          </div>
          <div className={styles.panelBody}>
            <div
              className={`${styles.dropZone} ${dragging ? styles.dropZoneActive : ''}`}
              role="button"
              tabIndex={0}
              onClick={() => openFilePicker()}
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
              onDragOver={(event) => {
                event.preventDefault();
                if (!dragging) setDragging(true);
              }}
              onDragLeave={() => setDragging(false)}
              onDrop={handleDrop}
              data-testid="usage-maintenance-import-dropzone"
            >
              <IconArrowUpFromLine size={20} aria-hidden="true" />
              <div className={styles.dropZoneCopy}>
                <strong>{t('usage_maintenance.transfer_drop_title')}</strong>
                <span>{t('usage_maintenance.transfer_supported_formats')}</span>
              </div>
            </div>

            {sessionList ? (
              <details className={styles.detailsCollapse}>
                <summary>{t('usage_maintenance.transfer_upload_details')}</summary>
                <div className={styles.detailsCollapseBody}>
                  <dl className={styles.drawerKvGrid}>
                    <div className={styles.inlineMetric}>
                      <dt className={styles.inlineMetricLabel}>
                        {t('usage_maintenance.transfer_chunk')}
                      </dt>
                      <dd className={styles.inlineMetricValue}>
                        {formatFileSize(sessionList.chunk_size_bytes)}
                      </dd>
                    </div>
                    <div className={styles.inlineMetric}>
                      <dt className={styles.inlineMetricLabel}>
                        {t('usage_maintenance.transfer_quota')}
                      </dt>
                      <dd className={styles.inlineMetricValue}>
                        {formatFileSize(sessionList.disk_quota_bytes)}
                      </dd>
                    </div>
                    <div className={styles.inlineMetric}>
                      <dt className={styles.inlineMetricLabel}>
                        {t('usage_maintenance.transfer_limit')}
                      </dt>
                      <dd className={styles.inlineMetricValue}>
                        {t('usage_maintenance.transfer_session_count', {
                          current: sessionList.active_sessions,
                          total: sessionList.max_sessions,
                        })}
                      </dd>
                    </div>
                    <div className={styles.inlineMetric}>
                      <dt className={styles.inlineMetricLabel}>
                        {t('usage_maintenance.transfer_expires')}
                      </dt>
                      <dd className={styles.inlineMetricValue}>
                        {formatTTL(sessionList.ttl_seconds, t)}
                      </dd>
                    </div>
                  </dl>
                </div>
              </details>
            ) : null}

            {activeProgress ? (
              <div className={styles.importProgressRow} data-testid="usage-maintenance-import-progress">
                <div className={styles.mobileRunHeader}>
                  <strong>{activeProgress.filename}</strong>
                  <span className={styles.mono}>{activeProgress.percent}%</span>
                </div>
                <div className={styles.progressTrack}>
                  <div
                    className={styles.progressFill}
                    style={{ width: `${activeProgress.percent}%` }}
                  />
                </div>
                <span className={styles.progressLabel}>
                  {formatFileSize(activeProgress.uploadedBytes)} /{' '}
                  {formatFileSize(activeProgress.totalBytes)} ·{' '}
                  {t(`usage_stats.import_phase_${activeProgress.phase}`, {
                    defaultValue: activeProgress.phase,
                  })}
                </span>
                {activeProgress.result ? (
                  <span className={styles.flatRowMeta}>
                    {formatResultSummary(activeProgress.result)}
                  </span>
                ) : null}
              </div>
            ) : null}
          </div>
          <div className={styles.panelFooter}>
            <SectionActionFooter>
              {activeProgress?.sessionId &&
              activeProgress.phase !== 'completed' &&
              activeProgress.phase !== 'cancelled' ? (
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={cancelBusy}
                  onClick={() => setCancelConfirmOpen(true)}
                >
                  {t('usage_maintenance.transfer_cancel_session')}
                </Button>
              ) : null}
              {operationBusy ? (
                <Button variant="secondary" size="sm" onClick={handlePauseImport}>
                  {t('usage_maintenance.transfer_pause')}
                </Button>
              ) : null}
              {activeTask &&
              (activeProgress?.phase === 'paused' ||
                (activeProgress?.phase === 'failed' && activeProgress.retryable !== false)) ? (
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() =>
                    void runImport(activeTask.file, activeProgress.sessionId || undefined)
                  }
                >
                  {t('usage_maintenance.transfer_resume')}
                </Button>
              ) : null}
              {activeProgress ? (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setDrawerMode({ kind: 'active' })}
                >
                  {t('usage_maintenance.details')}
                </Button>
              ) : null}
              <Button
                variant="primary"
                size="sm"
                disabled={operationBusy || cancelBusy}
                onClick={() => openFilePicker()}
                data-testid="usage-maintenance-import-pick-btn"
              >
                {t('usage_maintenance.import_file')}
              </Button>
            </SectionActionFooter>
          </div>
        </section>
      </div>

      <section className={styles.panel} data-testid="usage-maintenance-import-sessions-card">
        <div className={styles.panelHeader}>
          <div className={styles.panelHeaderCopy}>
            <h2 className={styles.panelTitle}>{t('usage_maintenance.transfer_sessions_title')}</h2>
            <p className={styles.panelDescription}>
              {t('usage_maintenance.recent_imports_note', {
                defaultValue: t('usage_maintenance.transfer_flow_note'),
              })}
            </p>
          </div>
          <div className={styles.panelHeaderActions}>
            {sessionList && !sessionsError ? (
              <span className={`${styles.statusPill} ${styles.statusNeutral}`}>
                {t('usage_maintenance.transfer_session_count', {
                  current: sessionList.active_sessions,
                  total: sessionList.max_sessions,
                })}
              </span>
            ) : null}
            <Button
              variant="secondary"
              size="xs"
              loading={sessionsRefreshing}
              onClick={() => void loadSessions(true)}
            >
              <IconRefreshCw size={13} aria-hidden="true" />
              <span>{t('common.refresh')}</span>
            </Button>
          </div>
        </div>

        {sessions.length === 0 ? (
          <EmptyState
            variant="canvas"
            title={
              sessionsLoading ? t('common.loading') : t('usage_maintenance.transfer_no_sessions')
            }
            description={t('usage_maintenance.transfer_flow_note')}
            action={
              !sessionsLoading ? (
                <Button size="sm" variant="secondary" onClick={() => openFilePicker()}>
                  {t('usage_maintenance.import_file')}
                </Button>
              ) : undefined
            }
          />
        ) : (
          <>
            <div className={styles.tableWrap}>
              <table className={styles.table}>
                <thead>
                  <tr>
                    <th>{t('usage_maintenance.import_filename')}</th>
                    <th>{t('usage_maintenance.upload_progress')}</th>
                    <th>{t('usage_maintenance.transfer_last_result')}</th>
                    <th>{t('usage_maintenance.technical_status')}</th>
                    <th className={styles.actionsCol}>{t('usage_maintenance.record_actions')}</th>
                  </tr>
                </thead>
                <tbody>
                  {sessions.map((session) => {
                    const pct = importSessionProgressPercent(session);
                    return (
                      <tr key={session.id} data-testid={`usage-import-session-row-${session.id}`}>
                        <td>
                          <div className={styles.sessionIdentity}>
                            <IconFileText size={15} aria-hidden="true" />
                            <div className={styles.flatRowCopy}>
                              <button
                                type="button"
                                className={styles.runIdBtn}
                                onClick={() =>
                                  setDrawerMode({ kind: 'session', sessionId: session.id })
                                }
                              >
                                {session.filename}
                              </button>
                              <span className={styles.flatRowMeta}>
                                {formatDateTime(new Date(session.updated_at_ms), i18n.language)}
                              </span>
                            </div>
                          </div>
                        </td>
                        <td>
                          <div className={styles.progressBlock}>
                            <div className={styles.progressTrack}>
                              <div className={styles.progressFill} style={{ width: `${pct}%` }} />
                            </div>
                            <span className={styles.progressLabel}>
                              {pct}% · {formatFileSize(session.received_bytes)} /{' '}
                              {formatFileSize(session.size_bytes)}
                            </span>
                          </div>
                        </td>
                        <td className={styles.flatRowMeta}>
                          {formatResultSummary(session.result)}
                        </td>
                        <td>
                          <span
                            className={`${styles.statusPill} ${getSessionToneClass(
                              session.status,
                              session.result
                            )}`}
                          >
                            <span className={styles.statusDot} aria-hidden="true" />
                            {getSessionDisplayStatus(session)}
                          </span>
                        </td>
                        <td className={styles.actionsCol}>
                          <div className={styles.rowActions}>
                            <Button
                              size="xs"
                              variant="ghost"
                              onClick={() =>
                                setDrawerMode({ kind: 'session', sessionId: session.id })
                              }
                            >
                              {t('usage_maintenance.details')}
                            </Button>
                            {isActionableImportSession(session) ? (
                              <Button
                                size="xs"
                                variant="secondary"
                                disabled={
                                  cancelBusy ||
                                  (operationBusy &&
                                    activeTask?.progress.sessionId !== session.id &&
                                    session.status !== 'processing')
                                }
                                onClick={() => handleSessionAction(session)}
                              >
                                {getSessionActionLabel(session)}
                              </Button>
                            ) : null}
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>

            <div className={styles.mobileCardList}>
              {sessions.map((session) => {
                const pct = importSessionProgressPercent(session);
                return (
                  <div key={session.id} className={styles.mobileRunRow}>
                    <div className={styles.mobileRunHeader}>
                      <button
                        type="button"
                        className={styles.runIdBtn}
                        onClick={() => setDrawerMode({ kind: 'session', sessionId: session.id })}
                      >
                        {session.filename}
                      </button>
                      <span
                        className={`${styles.statusPill} ${getSessionToneClass(
                          session.status,
                          session.result
                        )}`}
                      >
                        <span className={styles.statusDot} aria-hidden="true" />
                        {getSessionDisplayStatus(session)}
                      </span>
                    </div>
                    <div className={styles.mobileRunMeta}>
                      <span>
                        {pct}% · {formatFileSize(session.received_bytes)} /{' '}
                        {formatFileSize(session.size_bytes)}
                      </span>
                      <span>{formatResultSummary(session.result)}</span>
                    </div>
                    <div className={styles.rowActions}>
                      <Button
                        size="xs"
                        variant="ghost"
                        onClick={() => setDrawerMode({ kind: 'session', sessionId: session.id })}
                      >
                        {t('usage_maintenance.details')}
                      </Button>
                      {isActionableImportSession(session) ? (
                        <Button
                          size="xs"
                          variant="secondary"
                          onClick={() => handleSessionAction(session)}
                        >
                          {getSessionActionLabel(session)}
                        </Button>
                      ) : null}
                    </div>
                  </div>
                );
              })}
            </div>
          </>
        )}
      </section>

      <Drawer
        open={drawerOpen}
        onClose={() => setDrawerMode(null)}
        width="min(560px, 100vw)"
        title={
          drawerMode?.kind === 'active'
            ? t('usage_maintenance.import_file')
            : t('usage_maintenance.import_details')
        }
        footer={
          <div className={styles.rowActions}>
            {drawerMode?.kind === 'active' && activeProgress ? (
              <>
                {activeProgress.sessionId &&
                activeProgress.phase !== 'completed' &&
                activeProgress.phase !== 'cancelled' ? (
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={cancelBusy}
                    onClick={() => setCancelConfirmOpen(true)}
                  >
                    {t('usage_maintenance.transfer_cancel_session')}
                  </Button>
                ) : null}
                {operationBusy ? (
                  <Button variant="secondary" size="sm" onClick={handlePauseImport}>
                    {t('usage_maintenance.transfer_pause')}
                  </Button>
                ) : null}
                {activeTask &&
                (activeProgress.phase === 'paused' ||
                  (activeProgress.phase === 'failed' && activeProgress.retryable !== false)) ? (
                  <Button
                    variant="primary"
                    size="sm"
                    onClick={() =>
                      void runImport(activeTask.file, activeProgress.sessionId || undefined)
                    }
                  >
                    {t('usage_maintenance.transfer_resume')}
                  </Button>
                ) : null}
                <Button variant="secondary" size="sm" onClick={() => setDrawerMode(null)}>
                  {activeProgress.phase === 'completed' || activeProgress.phase === 'cancelled'
                    ? t('usage_maintenance.done')
                    : t('usage_maintenance.minimize')}
                </Button>
              </>
            ) : (
              <>
                {selectedSession && isActionableImportSession(selectedSession) ? (
                  <Button
                    size="sm"
                    variant="primary"
                    disabled={cancelBusy || operationBusy}
                    onClick={() => handleSessionAction(selectedSession)}
                  >
                    {getSessionActionLabel(selectedSession)}
                  </Button>
                ) : null}
                <Button variant="secondary" size="sm" onClick={() => setDrawerMode(null)}>
                  {t('common.close')}
                </Button>
              </>
            )}
          </div>
        }
      >
        {drawerMode?.kind === 'active' && activeProgress ? (
          <div className={styles.drawerBody}>
            <div className={styles.mobileRunHeader}>
              <strong>{activeProgress.filename}</strong>
              <span className={styles.mono}>{activeProgress.percent}%</span>
            </div>
            <div className={styles.progressTrack}>
              <div
                className={styles.progressFill}
                style={{ width: `${activeProgress.percent}%` }}
              />
            </div>
            <p className={styles.flatRowMeta}>
              {formatFileSize(activeProgress.uploadedBytes)} /{' '}
              {formatFileSize(activeProgress.totalBytes)} ·{' '}
              {t(`usage_stats.import_phase_${activeProgress.phase}`, {
                defaultValue: activeProgress.phase,
              })}
            </p>
            {activeProgress.error ? (
              <div className={`${styles.alertBanner} ${styles.alertDanger}`} role="alert">
                {activeProgress.error}
              </div>
            ) : null}
            {activeProgress.result ? (
              <p className={styles.flatRowMeta}>{formatResultSummary(activeProgress.result)}</p>
            ) : null}
            {activeProgress.result?.warnings?.length ? (
              <details className={styles.detailsCollapse}>
                <summary>{t('usage_maintenance.import_warnings')}</summary>
                <div className={styles.detailsCollapseBody}>
                  <ul>
                    {activeProgress.result.warnings.map((warning, idx) => (
                      <li key={idx}>{warning}</li>
                    ))}
                  </ul>
                </div>
              </details>
            ) : null}
            <p className={styles.flatRowMeta}>{t('usage_maintenance.import_cancel_note')}</p>
          </div>
        ) : selectedSession ? (
          <div className={styles.drawerBody} data-testid="usage-maintenance-import-session-drawer">
            <div className={styles.mobileRunHeader}>
              <span
                className={`${styles.statusPill} ${getSessionToneClass(
                  selectedSession.status,
                  selectedSession.result
                )}`}
              >
                <span className={styles.statusDot} aria-hidden="true" />
                {getSessionDisplayStatus(selectedSession)}
              </span>
              <span className={styles.mono}>{importSessionProgressPercent(selectedSession)}%</span>
            </div>
            <strong className={styles.flatRowLabel}>{selectedSession.filename}</strong>
            <div className={styles.progressTrack}>
              <div
                className={styles.progressFill}
                style={{ width: `${importSessionProgressPercent(selectedSession)}%` }}
              />
            </div>
            <p className={styles.flatRowMeta}>
              {formatFileSize(selectedSession.received_bytes)} /{' '}
              {formatFileSize(selectedSession.size_bytes)}
            </p>
            {selectedSession.status === 'processing' ? (
              <p className={styles.flatRowMeta}>{t('usage_stats.import_processing_hint')}</p>
            ) : null}
            <p className={styles.flatRowMeta}>{formatResultSummary(selectedSession.result)}</p>
            {selectedSession.result?.warnings?.length ? (
              <details className={styles.detailsCollapse}>
                <summary>{t('usage_maintenance.import_warnings')}</summary>
                <div className={styles.detailsCollapseBody}>
                  <ul>
                    {selectedSession.result.warnings.map((warning, idx) => (
                      <li key={idx}>{warning}</li>
                    ))}
                  </ul>
                </div>
              </details>
            ) : null}
            {selectedSession.status === 'failed' ? (
              <div className={`${styles.alertBanner} ${styles.alertWarn}`}>
                {t(
                  selectedSession.retryable
                    ? 'usage_maintenance.transfer_retryable'
                    : 'usage_maintenance.transfer_failed'
                )}
              </div>
            ) : null}
            <details className={styles.detailsCollapse}>
              <summary>{t('usage_maintenance.workspace_technical')}</summary>
              <div className={styles.detailsCollapseBody}>
                <dl className={styles.drawerKvGrid}>
                  <div className={`${styles.inlineMetric} ${styles.inlineMetricFull}`}>
                    <dt className={styles.inlineMetricLabel}>
                      {t('usage_maintenance.technical_run_id')}
                    </dt>
                    <dd className={styles.mono}>{selectedSession.id}</dd>
                  </div>
                  <div className={styles.inlineMetric}>
                    <dt className={styles.inlineMetricLabel}>
                      {t('usage_maintenance.transfer_chunk')}
                    </dt>
                    <dd className={styles.inlineMetricValue}>
                      {formatFileSize(selectedSession.chunk_size_bytes)}
                    </dd>
                  </div>
                  <div className={styles.inlineMetric}>
                    <dt className={styles.inlineMetricLabel}>
                      {t('usage_maintenance.transfer_expires')}
                    </dt>
                    <dd className={styles.inlineMetricValue}>
                      {formatDateTime(new Date(selectedSession.expires_at_ms), i18n.language)}
                    </dd>
                  </div>
                  <div className={styles.inlineMetric}>
                    <dt className={styles.inlineMetricLabel}>
                      {t('usage_maintenance.transfer_retryable_label')}
                    </dt>
                    <dd className={styles.inlineMetricValue}>
                      {t(selectedSession.retryable ? 'common.yes' : 'common.no')}
                    </dd>
                  </div>
                  {sessionList ? (
                    <div className={styles.inlineMetric}>
                      <dt className={styles.inlineMetricLabel}>
                        {t('usage_maintenance.transfer_expires')} TTL
                      </dt>
                      <dd className={styles.inlineMetricValue}>
                        {formatTTL(sessionList.ttl_seconds, t)}
                      </dd>
                    </div>
                  ) : null}
                </dl>
              </div>
            </details>
          </div>
        ) : (
          <div className={styles.drawerBody}>
            {detailError ? (
              <div className={`${styles.alertBanner} ${styles.alertDanger}`} role="alert">
                {detailError}
              </div>
            ) : (
              <p className={styles.flatRowMeta}>{t('common.loading')}</p>
            )}
          </div>
        )}
      </Drawer>

      <Modal
        open={Boolean(pendingImport)}
        title={t(
          pendingImport?.sessionId
            ? 'usage_maintenance.transfer_resume_confirm_title'
            : 'usage_maintenance.transfer_import_confirm_title'
        )}
        onClose={() => setPendingImport(null)}
        footer={
          <div className={styles.rowActions}>
            <Button variant="secondary" size="sm" onClick={() => setPendingImport(null)}>
              {t('common.cancel')}
            </Button>
            <Button
              variant="primary"
              size="sm"
              onClick={() => {
                if (!pendingImport) return;
                const { file, sessionId } = pendingImport;
                setPendingImport(null);
                void runImport(file, sessionId);
              }}
              data-testid="usage-maintenance-confirm-import-btn"
            >
              {t('usage_maintenance.transfer_import_confirm_button')}
            </Button>
          </div>
        }
      >
        <div className={styles.modalBody}>
          <p>
            {t('usage_maintenance.transfer_import_confirm_message', {
              name: pendingImport?.file.name ?? '',
            })}
          </p>
          {pendingImport ? (
            <p className={styles.mono}>
              {pendingImport.file.name} · {formatFileSize(pendingImport.file.size)}
            </p>
          ) : null}
        </div>
      </Modal>

      <Modal
        open={cancelConfirmOpen}
        title={t('usage_stats.import_cancel')}
        onClose={() => setCancelConfirmOpen(false)}
        footer={
          <div className={styles.rowActions}>
            <Button variant="secondary" size="sm" onClick={() => setCancelConfirmOpen(false)}>
              {t('common.back')}
            </Button>
            <Button
              variant="danger"
              size="sm"
              loading={cancelBusy}
              onClick={() => void handleConfirmCancelImport()}
            >
              {t('usage_stats.import_cancel')}
            </Button>
          </div>
        }
      >
        <div className={styles.modalBody}>
          <p>{t('usage_maintenance.import_cancel_note')}</p>
        </div>
      </Modal>
    </div>
  );
}
