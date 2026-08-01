import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import {
  applyCodexInspectionExecutionResult,
  buildCodexInspectionError,
  clearCodexInspectionConfigurableSettings,
  createCodexInspectionConnectionFingerprint,
  createCodexInspectionSession,
  DEFAULT_CODEX_INSPECTION_SETTINGS,
  executeCodexInspectionActions,
  isCodexInspectionStoppedError,
  isExecutableAction,
  isReauthAction,
  isSuggestedAction,
  loadCodexInspectionLastRun,
  resolveCodexInspectionAutoActionPlan,
  loadCodexInspectionConfigurableSettings,
  saveCodexInspectionLastRun,
  saveCodexInspectionConfigurableSettings,
  toReauthDeleteExecutionItem,
  type CodexInspectionAutoActionMode,
  type CodexInspectionConfigurableSettings,
  type CodexInspectionExecutionOutcome,
  type CodexInspectionLogDetail,
  type CodexInspectionLogLevel,
  type CodexInspectionProgressSnapshot,
  type CodexInspectionResultItem,
  type CodexInspectionRunResult,
  type CodexInspectionSession,
} from '@/features/monitoring/codexInspection';
import { Button } from '@/components/ui/Button';
import { CodexInspectionLogsPanel } from '@/features/monitoring/components/CodexInspectionLogsPanel';
import { CodexInspectionModeTabs } from '@/features/monitoring/components/CodexInspectionModeTabs';
import { CodexInspectionResultsPanel } from '@/features/monitoring/components/CodexInspectionResultsPanel';
import { CodexInspectionStatusPanel } from '@/features/monitoring/components/CodexInspectionStatusPanel';
import { InspectionConfigDrawer } from '@/features/monitoring/components/InspectionConfigDrawer';
import { InspectionConfigFields } from '@/features/monitoring/components/InspectionConfigFields';
import { CodexReauthDialog } from '@/features/oauth/CodexReauthDialog';
import type { CodexReauthTarget } from '@/features/oauth/codexReauthModel';
import {
  CODEX_INSPECTION_RESULT_PAGE_SIZE_OPTIONS,
  buildCodexInspectionPaginationState,
  countActions,
  countHandlingStates,
  createCompletedProgressSnapshot,
  createIdleProgressSnapshot,
  buildConfigOverviewItems,
  filterInspectionResults,
  formatActionLabel,
  formatAutoActionModeLabel,
  formatInspectionLogsForClipboard,
  formatTime,
  getActionFilterCounts,
  isCodexInspectionAutoExecutionEnabled,
  normalizeActionFilter,
  toLocalInspectionLogViewEntry,
  toSettingsDraft,
  validateInspectionConfigDraft,
  validateInspectionConfigFields,
  type ActionFilter,
  type ExecutionTriggerSource,
  type HandlingFilter,
  type InspectionLogEntry,
  type InspectionLogLevelFilter,
  type InspectionSettingsDraft,
  type InspectionSettingsDraftField,
  type RunStatus,
  type StatusTone,
  type SummaryCard,
} from '@/features/monitoring/model/codexInspectionPresentation';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import styles from './CodexInspectionPage.module.scss';

export function CodexInspectionPage() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const config = useConfigStore((state) => state.config);
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const connectionFingerprint = useMemo(
    () => createCodexInspectionConnectionFingerprint(apiBase, managementKey),
    [apiBase, managementKey]
  );
  const initialLastRunRef = useRef<ReturnType<typeof loadCodexInspectionLastRun> | undefined>(
    undefined
  );
  if (initialLastRunRef.current === undefined) {
    initialLastRunRef.current = connectionFingerprint
      ? loadCodexInspectionLastRun(connectionFingerprint)
      : null;
  }
  const initialLastRun = initialLastRunRef.current;

  const [inspectionSettings, setInspectionSettings] = useState<CodexInspectionConfigurableSettings>(
    () => loadCodexInspectionConfigurableSettings(config)
  );
  const [settingsDraft, setSettingsDraft] = useState<InspectionSettingsDraft>(() =>
    toSettingsDraft(loadCodexInspectionConfigurableSettings(config))
  );
  const [isSettingsModalOpen, setIsSettingsModalOpen] = useState(false);
  const [configFocusField, setConfigFocusField] = useState<string | null>(null);
  const [logs, setLogs] = useState<InspectionLogEntry[]>(() => initialLastRun?.logs ?? []);
  const [logsCollapsed, setLogsCollapsed] = useState(() => initialLastRun?.logsCollapsed ?? false);
  const [logLevelFilter, setLogLevelFilter] = useState<InspectionLogLevelFilter>('all');
  const [runStatus, setRunStatus] = useState<RunStatus>(() =>
    initialLastRun?.result ? 'success' : 'idle'
  );
  const [progress, setProgress] = useState<CodexInspectionProgressSnapshot>(() =>
    initialLastRun?.result
      ? createCompletedProgressSnapshot(initialLastRun.result)
      : createIdleProgressSnapshot()
  );
  const [result, setResult] = useState<CodexInspectionRunResult | null>(
    () => initialLastRun?.result ?? null
  );
  const [resultConnectionFingerprint, setResultConnectionFingerprint] = useState<string | null>(
    () => initialLastRun?.connectionFingerprint ?? null
  );
  const [executing, setExecuting] = useState(false);
  const [actionFilter, setActionFilter] = useState<ActionFilter>(() =>
    normalizeActionFilter(initialLastRun?.actionFilter ?? 'all')
  );
  const [handlingFilter, setHandlingFilter] = useState<HandlingFilter>('all');
  const [resultPage, setResultPage] = useState(1);
  const [resultPageSize, setResultPageSize] = useState<number>(
    CODEX_INSPECTION_RESULT_PAGE_SIZE_OPTIONS[0]
  );
  const [codexReauthTarget, setCodexReauthTarget] = useState<CodexReauthTarget | null>(null);
  const logCounterRef = useRef(initialLastRun?.logs.length ?? 0);
  const sessionRef = useRef<CodexInspectionSession | null>(null);
  const activeSessionIdRef = useRef<string | null>(null);
  const restoredConnectionFingerprintRef = useRef<string | null>(connectionFingerprint);
  const logListRef = useRef<HTMLDivElement | null>(null);
  const executeItemsRef = useRef<
    | ((
        items: CodexInspectionResultItem[],
        options?: {
          resultOverride?: CodexInspectionRunResult | null;
          source?: ExecutionTriggerSource;
          connectionFingerprint?: string | null;
          preflightOutcomes?: CodexInspectionExecutionOutcome[];
        }
      ) => Promise<void>)
    | null
  >(null);
  const localLogEntries = useMemo(
    () => logs.map((entry) => toLocalInspectionLogViewEntry(entry, t)),
    [logs, t]
  );

  useEffect(() => {
    if (restoredConnectionFingerprintRef.current === connectionFingerprint) return;
    restoredConnectionFingerprintRef.current = connectionFingerprint;

    activeSessionIdRef.current = null;
    sessionRef.current?.stop();
    sessionRef.current = null;
    setExecuting(false);

    const restored = connectionFingerprint
      ? loadCodexInspectionLastRun(connectionFingerprint)
      : null;

    setLogs(restored?.logs ?? []);
    setLogsCollapsed(restored?.logsCollapsed ?? false);
    setLogLevelFilter('all');
    setRunStatus(restored?.result ? 'success' : 'idle');
    setProgress(
      restored?.result
        ? createCompletedProgressSnapshot(restored.result)
        : createIdleProgressSnapshot()
    );
    setResult(restored?.result ?? null);
    setResultConnectionFingerprint(restored?.connectionFingerprint ?? null);
    setActionFilter(normalizeActionFilter(restored?.actionFilter ?? 'all'));
    setHandlingFilter('all');
    logCounterRef.current = restored?.logs.length ?? 0;
  }, [connectionFingerprint]);

  useEffect(() => {
    const nextSettings = loadCodexInspectionConfigurableSettings(config);
    setInspectionSettings(nextSettings);
    if (!isSettingsModalOpen) {
      setSettingsDraft(toSettingsDraft(nextSettings));
    }
  }, [config, isSettingsModalOpen]);

  useEffect(() => {
    if (!result || result.finishedAt <= 0) return;
    if (runStatus === 'running' || runStatus === 'paused') return;
    if (!connectionFingerprint || resultConnectionFingerprint !== connectionFingerprint) return;
    saveCodexInspectionLastRun({
      result,
      logs,
      logsCollapsed,
      actionFilter,
      connectionFingerprint,
    });
  }, [
    actionFilter,
    connectionFingerprint,
    logs,
    logsCollapsed,
    result,
    resultConnectionFingerprint,
    runStatus,
  ]);

  const appendLog = useCallback(
    (level: CodexInspectionLogLevel, message: string, detail?: CodexInspectionLogDetail) => {
      logCounterRef.current += 1;
      const timestamp = Date.now();
      setLogs((previous) => [
        ...previous,
        {
          id: `${timestamp}-${logCounterRef.current}`,
          level,
          message,
          timestamp,
          ...(detail ? { detail } : {}),
        },
      ]);
    },
    []
  );

  const scrollLogsToBottom = useCallback(() => {
    const element = logListRef.current;
    if (!element) return;
    element.scrollTop = element.scrollHeight;
  }, []);

  const appendInspectionCompletionLog = useCallback(
    (
      completedResult: CodexInspectionRunResult,
      outcomes: CodexInspectionExecutionOutcome[] = [],
      refreshError: string = '',
      executionError: string = ''
    ) => {
      const actionSummary = outcomes.reduce(
        (summary, outcome) => {
          summary[outcome.status] += 1;
          return summary;
        },
        { success: 0, failed: 0, skipped: 0, needs_review: 0 }
      );
      const hasWarning =
        actionSummary.failed > 0 ||
        actionSummary.needs_review > 0 ||
        Boolean(refreshError) ||
        Boolean(executionError);
      appendLog(
        hasWarning ? 'warning' : 'success',
        t('monitoring.codex_inspection_log_completed', {
          delete: completedResult.summary.deleteCount,
          disable: completedResult.summary.disableCount,
          enable: completedResult.summary.enableCount,
          reauth: completedResult.summary.reauthCount,
          keep: completedResult.summary.keepCount,
        }),
        {
          deleteCount: completedResult.summary.deleteCount,
          disableCount: completedResult.summary.disableCount,
          enableCount: completedResult.summary.enableCount,
          reauthCount: completedResult.summary.reauthCount,
          keepCount: completedResult.summary.keepCount,
          actionSuccessCount: actionSummary.success,
          actionFailedCount: actionSummary.failed,
          actionSkippedCount: actionSummary.skipped,
          actionNeedsReviewCount: actionSummary.needs_review,
          actionErrors: outcomes
            .filter((outcome) => !outcome.success)
            .map((outcome) => ({
              fileName: outcome.fileName,
              displayAccount: outcome.displayAccount,
              action: outcome.action,
              error: outcome.error,
            })),
          resultWriteFailedCount: 0,
          ...(refreshError ? { refreshFailed: true, refreshError } : {}),
          ...(executionError ? { executionFailed: true, executionError } : {}),
        }
      );
    },
    [appendLog, t]
  );

  useEffect(() => {
    if (logsCollapsed) return;
    scrollLogsToBottom();
  }, [logs, logsCollapsed, scrollLogsToBottom]);

  useEffect(() => {
    return () => {
      activeSessionIdRef.current = null;
      sessionRef.current?.stop();
      sessionRef.current = null;
    };
  }, []);

  const attachSessionPromise = useCallback(
    (
      session: CodexInspectionSession,
      promise: Promise<CodexInspectionRunResult>,
      autoActionMode: CodexInspectionAutoActionMode,
      autoRecoverEnabled: boolean,
      runConnectionFingerprint: string | null
    ) => {
      const sessionId = session.id;

      void promise
        .then((nextResult) => {
          if (activeSessionIdRef.current !== sessionId) return;
          const nextSuggestedResults = nextResult.results.filter(isSuggestedAction);
          const autoPlan = resolveCodexInspectionAutoActionPlan(
            autoActionMode,
            autoRecoverEnabled,
            nextSuggestedResults
          );
          const autoTargets = autoPlan.items;
          setResult(nextResult);
          setResultConnectionFingerprint(runConnectionFingerprint);
          setProgress(session.getProgress());
          setRunStatus('success');
          if (isCodexInspectionAutoExecutionEnabled(autoActionMode, autoRecoverEnabled)) {
            const autoExecutionLabel =
              autoActionMode === 'none' && autoRecoverEnabled
                ? t('monitoring.codex_inspection_settings_auto_recover_on')
                : formatAutoActionModeLabel(autoActionMode, t);
            if (
              (autoTargets.length > 0 || autoPlan.preflightOutcomes.length > 0) &&
              executeItemsRef.current
            ) {
              const startedMessage = t('monitoring.codex_inspection_auto_execute_started', {
                count: autoTargets.length + autoPlan.preflightOutcomes.length,
                mode: autoExecutionLabel,
              });
              showNotification(startedMessage, 'info');
              void executeItemsRef.current(autoTargets, {
                resultOverride: nextResult,
                source: 'auto',
                connectionFingerprint: runConnectionFingerprint,
                preflightOutcomes: autoPlan.preflightOutcomes,
              });
              return;
            }

            if (nextSuggestedResults.length > 0) {
              const requestedCount = nextSuggestedResults.length;
              appendLog(
                'info',
                t('monitoring.codex_inspection_log_auto_started', {
                  requested: requestedCount,
                  actions: 0,
                }),
                {
                  requestedCount,
                  actionCount: 0,
                }
              );
              appendLog(
                'warning',
                t('monitoring.codex_inspection_log_auto_completed', {
                  success: 0,
                  skipped: 0,
                  review: 0,
                  failed: 0,
                  remaining: requestedCount,
                }),
                {
                  successCount: 0,
                  failedCount: 0,
                  skippedCount: 0,
                  needsReviewCount: 0,
                  remainingCount: requestedCount,
                }
              );
              appendInspectionCompletionLog(nextResult);
              const skippedMessage = t('monitoring.codex_inspection_auto_execute_skipped_by_mode', {
                mode: autoExecutionLabel,
                count: requestedCount,
              });
              showNotification(skippedMessage, 'info');
              return;
            }
          }

          const noActionsMessage =
            nextSuggestedResults.length === 0
              ? t('monitoring.codex_inspection_auto_execute_no_actions')
              : t('monitoring.codex_inspection_run_success');
          appendInspectionCompletionLog(nextResult);
          showNotification(noActionsMessage, 'success');
        })
        .catch((error) => {
          if (activeSessionIdRef.current !== sessionId) return;
          if (isCodexInspectionStoppedError(error)) {
            setRunStatus('idle');
            setProgress(createIdleProgressSnapshot());
            return;
          }

          const message = buildCodexInspectionError(
            error instanceof Error ? error.message : String(error || t('common.unknown_error'))
          );
          setRunStatus('error');
          setLogsCollapsed(false);
          showNotification(message, 'error');
        });
    },
    [appendInspectionCompletionLog, appendLog, showNotification, t]
  );

  const startFreshInspection = useCallback(
    (
      preserveLogs: boolean = false,
      introMessage: string = '',
      options?: {
        autoActionMode?: CodexInspectionAutoActionMode;
      }
    ) => {
      if (connectionStatus !== 'connected') {
        const message = t('notification.connection_required');
        showNotification(message, 'warning');
        return;
      }
      if (!connectionFingerprint) {
        const message = t('notification.connection_required');
        showNotification(message, 'warning');
        return;
      }

      const autoActionMode = options?.autoActionMode ?? inspectionSettings.autoActionMode;
      const runConnectionFingerprint = connectionFingerprint;

      if (!preserveLogs) {
        setLogs([]);
      }
      if (introMessage) {
        appendLog('info', introMessage);
      }

      setResult(null);
      setResultConnectionFingerprint(runConnectionFingerprint);
      setRunStatus('running');
      setLogsCollapsed(false);
      setLogLevelFilter('all');
      setActionFilter('all');
      setHandlingFilter('all');

      const session = createCodexInspectionSession({
        config,
        apiBase,
        managementKey,
        settings: inspectionSettings,
        t,
        deferCompletionLog: true,
        onLog: (level, message, detail) => {
          if (activeSessionIdRef.current !== session.id) return;
          appendLog(level, message, detail);
        },
        onProgress: (snapshot) => {
          if (activeSessionIdRef.current !== session.id) return;
          setProgress(snapshot);
          if (snapshot.status === 'running') {
            setRunStatus('running');
            return;
          }
          if (snapshot.status === 'paused') {
            setRunStatus('paused');
          }
        },
        onResultsChange: (nextResult) => {
          if (activeSessionIdRef.current !== session.id) return;
          setResult(nextResult);
          setResultConnectionFingerprint(runConnectionFingerprint);
        },
      });

      sessionRef.current = session;
      activeSessionIdRef.current = session.id;
      setProgress(session.getProgress());
      attachSessionPromise(
        session,
        session.start(),
        autoActionMode,
        inspectionSettings.autoRecoverEnabled,
        runConnectionFingerprint
      );
    },
    [
      apiBase,
      appendLog,
      attachSessionPromise,
      config,
      connectionFingerprint,
      connectionStatus,
      inspectionSettings,
      managementKey,
      showNotification,
      t,
    ]
  );

  const handleRunInspection = useCallback(() => {
    if (runStatus === 'paused' && sessionRef.current) {
      setLogsCollapsed(false);
      sessionRef.current.resume();
      return;
    }

    startFreshInspection(false);
  }, [runStatus, startFreshInspection]);

  const handlePauseInspection = useCallback(() => {
    if (runStatus !== 'running') return;
    sessionRef.current?.pause();
  }, [runStatus]);

  const handleStopInspection = useCallback(() => {
    const currentSession = sessionRef.current;
    if (!currentSession) return;

    currentSession.stop();
    activeSessionIdRef.current = null;
    sessionRef.current = null;
    setRunStatus('idle');
    setProgress(createIdleProgressSnapshot());
    setResult(null);
    setResultConnectionFingerprint(null);
    setLogsCollapsed(false);
  }, []);

  const executeItems = useCallback(
    async (
      items: CodexInspectionResultItem[],
      options?: {
        resultOverride?: CodexInspectionRunResult | null;
        source?: ExecutionTriggerSource;
        connectionFingerprint?: string | null;
        preflightOutcomes?: CodexInspectionExecutionOutcome[];
      }
    ) => {
      const currentResult = options?.resultOverride ?? result;
      const source = options?.source ?? 'manual';
      if (!currentResult) return;
      const currentResultFingerprint =
        options?.connectionFingerprint ?? resultConnectionFingerprint;
      if (!connectionFingerprint || currentResultFingerprint !== connectionFingerprint) {
        showNotification(t('notification.connection_required'), 'warning');
        return;
      }
      const targets = items.filter(isExecutableAction);
      const preflightOutcomes = options?.preflightOutcomes ?? [];
      if (targets.length === 0 && preflightOutcomes.length === 0) {
        showNotification(t('monitoring.codex_inspection_no_pending_actions'), 'info');
        return;
      }

      setExecuting(true);
      setLogsCollapsed(false);

      try {
        const execution = await executeCodexInspectionActions({
          settings: currentResult.settings,
          items: targets,
          referenceItems: currentResult.results,
          previousFiles: currentResult.files,
          connectionFingerprint: currentResultFingerprint,
          source,
          preflightOutcomes,
          onLog: appendLog,
          t,
        });

        const outcomeSummary = execution.outcomes.reduce(
          (summary, outcome) => {
            summary[outcome.status] += 1;
            return summary;
          },
          { success: 0, failed: 0, skipped: 0, needs_review: 0 }
        );
        const refreshWarning = execution.refreshError
          ? t('monitoring.codex_inspection_log_refresh_failed', {
              message: execution.refreshError,
            })
          : '';
        if (source === 'manual') {
          if (
            outcomeSummary.failed > 0 ||
            outcomeSummary.skipped > 0 ||
            outcomeSummary.needs_review > 0
          ) {
            const failureSummary = t('monitoring.codex_inspection_log_manual_completed', {
              success: outcomeSummary.success,
              skipped: outcomeSummary.skipped,
              review: outcomeSummary.needs_review,
              failed: outcomeSummary.failed,
            });
            showNotification(
              refreshWarning ? `${failureSummary}；${refreshWarning}` : failureSummary,
              'warning'
            );
          } else if (refreshWarning) {
            showNotification(refreshWarning, 'warning');
          } else {
            showNotification(t('monitoring.codex_inspection_execute_success'), 'success');
          }
        }
        const nextResult = applyCodexInspectionExecutionResult(currentResult, execution);
        setResult(nextResult);
        setResultConnectionFingerprint(currentResultFingerprint);

        if (source === 'auto') {
          const successCount = outcomeSummary.success;
          const failedCount = outcomeSummary.failed;
          const remainingCount = nextResult.results.filter(isSuggestedAction).length;
          const baseSummaryMessage = t('monitoring.codex_inspection_log_auto_completed', {
            success: successCount,
            skipped: outcomeSummary.skipped,
            review: outcomeSummary.needs_review,
            failed: failedCount,
            remaining: remainingCount,
          });
          const summaryMessage = refreshWarning
            ? `${baseSummaryMessage}；${refreshWarning}`
            : baseSummaryMessage;
          const hasExecutionWarning =
            failedCount > 0 ||
            outcomeSummary.needs_review > 0 ||
            remainingCount > 0 ||
            Boolean(execution.refreshError);
          appendLog(hasExecutionWarning ? 'warning' : 'success', summaryMessage, {
            successCount,
            failedCount,
            skippedCount: outcomeSummary.skipped,
            needsReviewCount: outcomeSummary.needs_review,
            remainingCount,
            actionErrors: execution.outcomes
              .filter((item) => !item.success)
              .map((item) => ({
                fileName: item.fileName,
                displayAccount: item.displayAccount,
                action: item.action,
                error: item.error,
              })),
            refreshFailed: Boolean(execution.refreshError),
            ...(execution.refreshError ? { refreshError: execution.refreshError } : {}),
          });
          showNotification(summaryMessage, hasExecutionWarning ? 'warning' : 'success');
          appendInspectionCompletionLog(nextResult, execution.outcomes, execution.refreshError);
        }
      } catch (error) {
        const message =
          error instanceof Error ? error.message : String(error || t('common.unknown_error'));
        const requestedCount = targets.length + preflightOutcomes.length;
        const remainingCount = currentResult.results.filter(isSuggestedAction).length;
        const failureMessage = t('monitoring.codex_inspection_log_execution_failed', {
          message,
        });
        appendLog('error', failureMessage, {
          source,
          requestedCount,
          error: message,
        });
        if (source === 'auto') {
          appendLog(
            'warning',
            t('monitoring.codex_inspection_log_auto_completed', {
              success: 0,
              skipped: 0,
              review: 0,
              failed: requestedCount,
              remaining: remainingCount,
            }),
            {
              successCount: 0,
              failedCount: requestedCount,
              skippedCount: 0,
              needsReviewCount: 0,
              remainingCount,
              executionFailed: true,
              executionError: message,
            }
          );
          appendInspectionCompletionLog(currentResult, [], '', message);
        } else {
          appendLog(
            'warning',
            t('monitoring.codex_inspection_log_manual_completed', {
              success: 0,
              skipped: 0,
              review: 0,
              failed: requestedCount,
            }),
            {
              successCount: 0,
              failedCount: requestedCount,
              skippedCount: 0,
              needsReviewCount: 0,
              executionFailed: true,
              executionError: message,
            }
          );
        }
        showNotification(failureMessage, 'error');
      } finally {
        setExecuting(false);
      }
    },
    [
      appendInspectionCompletionLog,
      appendLog,
      connectionFingerprint,
      result,
      resultConnectionFingerprint,
      showNotification,
      t,
    ]
  );

  useEffect(() => {
    executeItemsRef.current = executeItems;
  }, [executeItems]);

  const displayResults = useMemo(() => (result ? result.results : []), [result]);

  const executableResults = useMemo(
    () => (result ? result.results.filter(isExecutableAction) : []),
    [result]
  );

  const reauthResults = useMemo(
    () => (result ? result.results.filter(isReauthAction) : []),
    [result]
  );
  const filteredResults = useMemo(
    () => filterInspectionResults(displayResults, handlingFilter, actionFilter),
    [displayResults, handlingFilter, actionFilter]
  );

  const resultPagination = useMemo(
    () => buildCodexInspectionPaginationState(filteredResults, resultPage, resultPageSize),
    [filteredResults, resultPage, resultPageSize]
  );

  useEffect(() => {
    setResultPage(1);
  }, [actionFilter, handlingFilter, result?.startedAt, result?.finishedAt]);

  useEffect(() => {
    if (resultPage === resultPagination.currentPage) return;
    setResultPage(resultPagination.currentPage);
  }, [resultPage, resultPagination.currentPage]);

  const handleResultPageSizeChange = useCallback((pageSize: number) => {
    setResultPageSize(pageSize);
    setResultPage(1);
  }, []);

  const handleExecutePlanned = useCallback(() => {
    if (!result) return;

    const targets = executableResults;
    const counts = countActions(targets);
    showConfirmation({
      title: t('monitoring.codex_inspection_execute_confirm_title'),
      message: t('monitoring.codex_inspection_execute_confirm_body', {
        total: targets.length,
        delete: counts.delete,
        disable: counts.disable,
        enable: counts.enable,
      }),
      confirmText: t('monitoring.codex_inspection_execute_now'),
      cancelText: t('common.cancel'),
      variant: 'danger',
      onConfirm: () => executeItems(targets),
    });
  }, [executableResults, executeItems, result, showConfirmation, t]);

  const handleExecuteSingle = useCallback(
    (item: CodexInspectionResultItem) => {
      const actionLabel = formatActionLabel(item.action, t);
      showConfirmation({
        title: t('monitoring.codex_inspection_execute_single_title'),
        message: t('monitoring.codex_inspection_execute_single_body', {
          account: item.displayAccount,
          action: actionLabel,
        }),
        confirmText: actionLabel,
        cancelText: t('common.cancel'),
        variant: item.action === 'delete' ? 'danger' : 'primary',
        onConfirm: () => executeItems([item]),
      });
    },
    [executeItems, showConfirmation, t]
  );

  const handleDeleteReauthPlanned = useCallback(() => {
    if (!result) return;

    const targets = reauthResults.map(toReauthDeleteExecutionItem);
    showConfirmation({
      title: t('monitoring.codex_inspection_delete_reauth_confirm_title'),
      message: t('monitoring.codex_inspection_delete_reauth_confirm_body', {
        count: targets.length,
      }),
      confirmText: t('monitoring.codex_inspection_delete_reauth_now'),
      cancelText: t('common.cancel'),
      variant: 'danger',
      onConfirm: () => executeItems(targets),
    });
  }, [executeItems, reauthResults, result, showConfirmation, t]);

  const handleDeleteSingleReauth = useCallback(
    (item: CodexInspectionResultItem) => {
      showConfirmation({
        title: t('monitoring.codex_inspection_delete_reauth_single_title'),
        message: t('monitoring.codex_inspection_delete_reauth_single_body', {
          account: item.displayAccount,
          file: item.fileName,
        }),
        confirmText: t('monitoring.codex_inspection_action_delete'),
        cancelText: t('common.cancel'),
        variant: 'danger',
        onConfirm: () => executeItems([toReauthDeleteExecutionItem(item)]),
      });
    },
    [executeItems, showConfirmation, t]
  );

  const handleOpenCodexReauth = useCallback(
    (item: CodexInspectionResultItem) => {
      if (item.provider === 'xai') {
        navigate('/oauth#oauth-provider-xai');
        return;
      }
      setCodexReauthTarget({
        account: item.displayAccount || item.accountId || item.fileName,
        fileName: item.fileName,
        authIndex: item.authIndex,
        accountId: item.accountId,
      });
    },
    [navigate]
  );

  const handleCodexReauthSuccess = useCallback(() => {
    showNotification(t('codex_reauth.rerun_hint'), 'success');
  }, [showNotification, t]);

  const summaryCards = useMemo<SummaryCard[]>(() => {
    const summarySource =
      runStatus === 'running' || runStatus === 'paused'
        ? progress.summary
        : (result?.summary ?? null);
    const blank = '--';
    const probeSetCount = summarySource ? summarySource.probeSetCount : null;
    const sampledTotal = summarySource ? summarySource.sampledCount : null;
    const sampledCompleted =
      summarySource === null
        ? null
        : runStatus === 'running' || runStatus === 'paused'
          ? progress.completed
          : summarySource.sampledCount;
    const deleteCount = summarySource ? summarySource.deleteCount : null;
    const disableCount = summarySource ? summarySource.disableCount : null;
    const enableCount = summarySource ? summarySource.enableCount : null;
    const reauthCount = summarySource ? summarySource.reauthCount : null;

    const probeMeta = summarySource
      ? t('monitoring.server_codex_inspection_total_files', {
          count: summarySource.totalFiles,
        })
      : t('monitoring.server_codex_inspection_total_files', { count: 0 });

    const sampledMeta = (() => {
      if (sampledTotal === null) {
        return t('monitoring.codex_inspection_sampled_meta_idle');
      }
      if (runStatus === 'running' || runStatus === 'paused') {
        return t('monitoring.codex_inspection_sampled_meta_running', {
          total: sampledTotal,
          percent: progress.percent,
        });
      }
      return t('monitoring.codex_inspection_sampled_meta_done', { total: sampledTotal });
    })();

    return [
      {
        key: 'probe-total',
        label: t('monitoring.codex_inspection_total_accounts'),
        value: probeSetCount === null ? blank : String(probeSetCount),
        meta: probeMeta,
        icon: 'probe',
        accent: 'blue',
      },
      {
        key: 'sampled',
        label: t('monitoring.codex_inspection_sampled_accounts'),
        value: sampledCompleted === null ? blank : String(sampledCompleted),
        meta: sampledMeta,
        icon: 'sampled',
        accent: 'cyan',
      },
      {
        key: 'delete',
        label: t('monitoring.codex_inspection_delete_count'),
        value: deleteCount === null ? blank : String(deleteCount),
        meta: t('monitoring.codex_inspection_delete_meta'),
        tone: deleteCount && deleteCount > 0 ? 'bad' : undefined,
        icon: 'delete',
        accent: 'red',
      },
      {
        key: 'disable',
        label: t('monitoring.codex_inspection_disable_count'),
        value: disableCount === null ? blank : String(disableCount),
        meta: `${t('monitoring.codex_inspection_threshold')} ${inspectionSettings.usedPercentThreshold}%`,
        tone: disableCount && disableCount > 0 ? 'warn' : undefined,
        icon: 'disable',
        accent: 'amber',
      },
      {
        key: 'enable',
        label: t('monitoring.codex_inspection_enable_count'),
        value: enableCount === null ? blank : String(enableCount),
        meta: t('monitoring.codex_inspection_enable_meta'),
        tone: enableCount && enableCount > 0 ? 'good' : undefined,
        icon: 'enable',
        accent: 'green',
      },
      {
        key: 'reauth',
        label: t('monitoring.codex_inspection_reauth_count'),
        value: reauthCount === null ? blank : String(reauthCount),
        meta: t('monitoring.codex_inspection_reauth_meta'),
        tone: reauthCount && reauthCount > 0 ? 'info' : undefined,
        icon: 'reauth',
        accent: 'violet',
      },
    ];
  }, [
    inspectionSettings.usedPercentThreshold,
    progress.completed,
    progress.percent,
    progress.summary,
    result,
    runStatus,
    t,
  ]);

  const pendingActionCount = executableResults.length;
  const progressLabel =
    progress.total > 0
      ? t('monitoring.codex_inspection_progress_status', {
          completed: progress.completed,
          total: progress.total,
          inFlight: progress.inFlight,
          pending: progress.pending,
          percent: progress.percent,
        })
      : t('monitoring.codex_inspection_progress_idle');
  const showProgressBar = runStatus === 'running' || runStatus === 'paused';

  const statusToneMap: Record<RunStatus, StatusTone> = {
    idle: 'idle',
    running: 'info',
    paused: 'warn',
    success: 'good',
    error: 'bad',
  };

  const statusLabelMap: Record<RunStatus, string> = {
    idle: t('monitoring.codex_inspection_status_idle'),
    running: t('monitoring.codex_inspection_status_running'),
    paused: t('monitoring.codex_inspection_status_paused'),
    success: t('monitoring.codex_inspection_status_success'),
    error: t('monitoring.codex_inspection_status_error'),
  };

  const statusTone = statusToneMap[runStatus];
  const statusLabel = statusLabelMap[runStatus];

  const lastFinishedLabel =
    result && result.finishedAt > 0
      ? `${t('monitoring.codex_inspection_last_finished_at')} · ${formatTime(result.finishedAt, i18n.language)}`
      : null;

  const openSettingsModal = useCallback(
    (field?: string) => {
      setSettingsDraft(toSettingsDraft(inspectionSettings));
      setConfigFocusField(field ?? null);
      setIsSettingsModalOpen(true);
    },
    [inspectionSettings]
  );

  const handleSettingsDraftChange = useCallback(
    (field: InspectionSettingsDraftField, value: string) => {
      setSettingsDraft((previous) => ({
        ...previous,
        [field]: value,
      }));
    },
    []
  );

  const handleAutoActionModeChange = useCallback((value: CodexInspectionAutoActionMode) => {
    setSettingsDraft((previous) => ({
      ...previous,
      autoActionMode: value,
    }));
  }, []);

  const handleAutoRecoverEnabledChange = useCallback((value: boolean) => {
    setSettingsDraft((previous) => ({
      ...previous,
      autoRecoverEnabled: value,
    }));
  }, []);

  const handleXaiInferenceEnabledChange = useCallback((value: boolean) => {
    setSettingsDraft((previous) => ({
      ...previous,
      xaiInferenceEnabled: value,
    }));
  }, []);

  const settingsFieldErrors = useMemo(
    () => validateInspectionConfigFields(settingsDraft, t),
    [settingsDraft, t]
  );

  const hasUnsavedSettings = useMemo(() => {
    const baseline = toSettingsDraft(inspectionSettings);
    return (Object.keys(baseline) as (keyof InspectionSettingsDraft)[]).some(
      (key) => baseline[key] !== settingsDraft[key]
    );
  }, [inspectionSettings, settingsDraft]);

  const handleSaveSettings = useCallback(() => {
    const validation = validateInspectionConfigDraft(settingsDraft, t);
    if (!validation.ok) {
      const firstError = Object.values(validation.errors).find(Boolean);
      showNotification(firstError ?? t('common.unknown_error'), 'error');
      return;
    }

    const nextSettings = saveCodexInspectionConfigurableSettings(validation.values);

    setInspectionSettings(nextSettings);
    setSettingsDraft(toSettingsDraft(nextSettings));
    setIsSettingsModalOpen(false);
    showNotification(t('monitoring.codex_inspection_settings_saved'), 'success');
  }, [settingsDraft, showNotification, t]);

  const handleCloseSettingsDrawer = useCallback(() => {
    if (hasUnsavedSettings) {
      showConfirmation({
        title: t('monitoring.server_codex_inspection_close_confirm_title'),
        message: t('monitoring.server_codex_inspection_close_unsaved_hint'),
        confirmText: t('monitoring.server_codex_inspection_discard'),
        cancelText: t('common.cancel'),
        variant: 'danger',
        onConfirm: () => {
          setSettingsDraft(toSettingsDraft(inspectionSettings));
          setIsSettingsModalOpen(false);
        },
      });
      return;
    }
    setIsSettingsModalOpen(false);
  }, [hasUnsavedSettings, inspectionSettings, showConfirmation, t]);

  const handleResetSettings = useCallback(() => {
    clearCodexInspectionConfigurableSettings();
    const nextSettings = saveCodexInspectionConfigurableSettings(DEFAULT_CODEX_INSPECTION_SETTINGS);
    setInspectionSettings(nextSettings);
    setSettingsDraft(toSettingsDraft(nextSettings));
    showNotification(t('monitoring.codex_inspection_settings_reset'), 'success');
  }, [showNotification, t]);

  const handleClearLogs = useCallback(() => {
    setLogs([]);
    setLogLevelFilter('all');
  }, []);

  const handleCopyLogs = useCallback(async () => {
    if (localLogEntries.length === 0) return;
    try {
      await navigator.clipboard.writeText(formatInspectionLogsForClipboard(localLogEntries));
      showNotification(t('monitoring.codex_inspection_logs_copied'), 'success');
    } catch {
      showNotification(t('monitoring.codex_inspection_logs_copy_failed'), 'error');
    }
  }, [localLogEntries, showNotification, t]);

  const handleJumpToLatest = useCallback(() => {
    if (logsCollapsed) {
      setLogsCollapsed(false);
      requestAnimationFrame(scrollLogsToBottom);
      return;
    }
    scrollLogsToBottom();
  }, [logsCollapsed, scrollLogsToBottom]);

  const filterCounts = useMemo(() => {
    return getActionFilterCounts(displayResults);
  }, [displayResults]);

  const handlingFilterCounts = useMemo(() => countHandlingStates(displayResults), [displayResults]);

  const filterLabel = (filter: ActionFilter) => {
    switch (filter) {
      case 'delete':
        return t('monitoring.codex_inspection_filter_delete');
      case 'disable':
        return t('monitoring.codex_inspection_filter_disable');
      case 'enable':
        return t('monitoring.codex_inspection_filter_enable');
      case 'reauth':
        return t('monitoring.codex_inspection_filter_reauth');
      case 'keep':
        return t('monitoring.codex_inspection_action_keep');
      case 'all':
      default:
        return t('monitoring.codex_inspection_filter_all');
    }
  };

  const handlingFilterLabel = (filter: HandlingFilter) => {
    switch (filter) {
      case 'pending':
        return t('monitoring.codex_inspection_handling_filter_pending');
      case 'no_action':
        return t('monitoring.codex_inspection_handling_filter_no_action');
      case 'all':
      default:
        return t('monitoring.codex_inspection_handling_filter_all');
    }
  };

  const isInspectionInFlight = runStatus === 'running' || runStatus === 'paused';
  const runButtonLabel =
    runStatus === 'paused'
      ? t('monitoring.codex_inspection_resume')
      : runStatus === 'running'
        ? t('monitoring.codex_inspection_running')
        : t('monitoring.codex_inspection_run_local');
  const configOverviewItems = buildConfigOverviewItems(inspectionSettings, {
    mode: 'local',
    t,
  });

  return (
    <div className={styles.page}>
      <CodexInspectionModeTabs activeMode="local" />

      <CodexInspectionStatusPanel
        statusTone={statusTone}
        statusLabel={statusLabel}
        lastFinishedLabel={lastFinishedLabel}
        pendingActionCount={pendingActionCount}
        summaryCards={summaryCards}
        progress={progress}
        progressLabel={progressLabel}
        showProgressBar={showProgressBar}
        runStatus={runStatus}
        runButtonLabel={runButtonLabel}
        executing={executing}
        isInspectionInFlight={isInspectionInFlight}
        runDisabled={runStatus === 'running' || executing || connectionStatus !== 'connected'}
        configOverviewItems={configOverviewItems}
        configOverviewTitle={t('monitoring.codex_inspection_config_overview_title')}
        configOverviewEditLabel={t('monitoring.codex_inspection_config_overview_edit')}
        t={t}
        onEditConfig={openSettingsModal}
        onRunInspection={handleRunInspection}
        onPauseInspection={handlePauseInspection}
        onStopInspection={handleStopInspection}
      />

      <CodexInspectionResultsPanel
        result={result}
        filteredResults={resultPagination.pageItems}
        pendingActionCount={pendingActionCount}
        manualActionCount={filterCounts.reauth}
        reauthActionCount={reauthResults.length}
        handlingFilterCounts={handlingFilterCounts}
        filterCounts={filterCounts}
        handlingFilter={handlingFilter}
        actionFilter={actionFilter}
        pagination={resultPagination}
        pageSize={resultPageSize}
        pageSizeOptions={CODEX_INSPECTION_RESULT_PAGE_SIZE_OPTIONS}
        executing={executing}
        isInspectionInFlight={isInspectionInFlight}
        xaiInferenceEnabled={result?.settings.xaiInferenceEnabled ?? false}
        t={t}
        onActionFilterChange={setActionFilter}
        onHandlingFilterChange={setHandlingFilter}
        onPageChange={setResultPage}
        onPageSizeChange={handleResultPageSizeChange}
        onExecutePlanned={handleExecutePlanned}
        onExecuteSingle={handleExecuteSingle}
        onReauthAccount={handleOpenCodexReauth}
        onDeleteReauthPlanned={reauthResults.length > 0 ? handleDeleteReauthPlanned : undefined}
        onDeleteReauthSingle={reauthResults.length > 0 ? handleDeleteSingleReauth : undefined}
        filterLabel={filterLabel}
        handlingFilterLabel={handlingFilterLabel}
      />

      <CodexInspectionLogsPanel
        logs={localLogEntries}
        logsCollapsed={logsCollapsed}
        levelFilter={logLevelFilter}
        logListRef={logListRef}
        locale={i18n.language}
        t={t}
        onLevelFilterChange={setLogLevelFilter}
        onCopyLogs={() => void handleCopyLogs()}
        onJumpToLatest={handleJumpToLatest}
        onClearLogs={handleClearLogs}
        onToggleCollapsed={() => setLogsCollapsed((previous) => !previous)}
      />

      <InspectionConfigDrawer
        open={isSettingsModalOpen}
        title={t('monitoring.codex_inspection_settings_title')}
        description={t('monitoring.codex_inspection_settings_desc')}
        closeLabel={t('common.close')}
        focusField={configFocusField}
        onClose={handleCloseSettingsDrawer}
        footer={
          <>
            <div className={styles.configDrawerStatus}>
              {hasUnsavedSettings ? (
                <span className={styles.serverUnsavedBadge}>
                  {t('monitoring.server_codex_inspection_unsaved')}
                </span>
              ) : (
                <span>{t('monitoring.server_codex_inspection_saved_applied')}</span>
              )}
            </div>
            <div className={styles.configDrawerActions}>
              <Button
                variant="secondary"
                size="sm"
                className={styles.settingsResetButton}
                onClick={handleResetSettings}
              >
                {t('monitoring.codex_inspection_settings_reset_button')}
              </Button>
              <Button size="sm" onClick={handleSaveSettings}>
                {t('common.save')}
              </Button>
            </div>
          </>
        }
      >
        <InspectionConfigFields
          draft={settingsDraft}
          errors={settingsFieldErrors}
          t={t}
          onFieldChange={handleSettingsDraftChange}
          onXaiInferenceEnabledChange={handleXaiInferenceEnabledChange}
          onAutoActionModeChange={handleAutoActionModeChange}
          onAutoRecoverEnabledChange={handleAutoRecoverEnabledChange}
        />
      </InspectionConfigDrawer>

      <CodexReauthDialog
        open={Boolean(codexReauthTarget)}
        target={codexReauthTarget}
        onClose={() => setCodexReauthTarget(null)}
        onSuccess={handleCodexReauthSuccess}
      />
    </div>
  );
}
