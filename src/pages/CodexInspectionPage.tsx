import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconChevronDown,
  IconChevronUp,
  IconExternalLink,
  IconRefreshCw,
  IconSettings,
  IconTrash2,
} from '@/components/ui/icons';
import {
  applyCodexInspectionExecutionResult,
  buildCodexInspectionError,
  buildExecutionFailureMessage,
  clearCodexInspectionConfigurableSettings,
  createCodexInspectionSession,
  DEFAULT_CODEX_INSPECTION_SETTINGS,
  executeCodexInspectionActions,
  isCodexInspectionStoppedError,
  isSuggestedAction,
  loadCodexInspectionConfigurableSettings,
  saveCodexInspectionConfigurableSettings,
  type CodexInspectionAction,
  type CodexInspectionConfigurableSettings,
  type CodexInspectionLogLevel,
  type CodexInspectionProgressSnapshot,
  type CodexInspectionResultItem,
  type CodexInspectionRunResult,
  type CodexInspectionSession,
} from '@/features/monitoring/codexInspection';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import styles from './CodexInspectionPage.module.scss';

type RunStatus = 'idle' | 'running' | 'paused' | 'success' | 'error';

type ActionFilter = 'all' | 'delete' | 'disable' | 'enable';

type StatusTone = 'idle' | 'info' | 'good' | 'warn' | 'bad';

type InspectionLogEntry = {
  id: string;
  level: CodexInspectionLogLevel;
  message: string;
  timestamp: number;
};

type ExecutionTriggerSource = 'manual' | 'auto';

type SummaryCard = {
  key: string;
  label: string;
  value: string;
  meta: string;
  tone?: StatusTone;
};

type InspectionSettingsDraft = {
  targetType: string;
  workers: string;
  deleteWorkers: string;
  timeout: string;
  retries: string;
  userAgent: string;
  usedPercentThreshold: string;
  sampleSize: string;
  autoExecuteActions: boolean;
};

type InspectionSettingsDraftField = Exclude<keyof InspectionSettingsDraft, 'autoExecuteActions'>;

type PanelProps = {
  title: string;
  subtitle?: string;
  extra?: ReactNode;
  children: ReactNode;
  className?: string;
};

const ACTION_FILTERS: ActionFilter[] = ['all', 'delete', 'disable', 'enable'];

const actionToneClass: Record<CodexInspectionAction, string> = {
  keep: styles.actionKeep,
  delete: styles.actionDelete,
  disable: styles.actionDisable,
  enable: styles.actionEnable,
};

const levelClassMap: Record<CodexInspectionLogLevel, string> = {
  info: styles.logInfo,
  success: styles.logSuccess,
  warning: styles.logWarning,
  error: styles.logError,
};

const formatTimestamp = (value: number, locale: string) => new Date(value).toLocaleString(locale);
const formatTime = (value: number, locale: string) => new Date(value).toLocaleTimeString(locale);

const formatPercent = (value: number | null) => (value === null ? '--' : `${value.toFixed(1)}%`);

const toSettingsDraft = (settings: CodexInspectionConfigurableSettings): InspectionSettingsDraft => ({
  targetType: settings.targetType,
  workers: String(settings.workers),
  deleteWorkers: String(settings.deleteWorkers),
  timeout: String(settings.timeout),
  retries: String(settings.retries),
  userAgent: settings.userAgent,
  usedPercentThreshold: String(settings.usedPercentThreshold),
  sampleSize: String(settings.sampleSize),
  autoExecuteActions: settings.autoExecuteActions,
});

const formatActionLabel = (action: CodexInspectionAction, t: TFunction) => {
  switch (action) {
    case 'delete':
      return t('monitoring.codex_inspection_action_delete');
    case 'disable':
      return t('monitoring.codex_inspection_action_disable');
    case 'enable':
      return t('monitoring.codex_inspection_action_enable');
    case 'keep':
    default:
      return t('monitoring.codex_inspection_action_keep');
  }
};

const formatCurrentStateLabel = (item: CodexInspectionResultItem, t: TFunction) => {
  if (item.disabled) return t('monitoring.codex_inspection_state_disabled');
  return t('monitoring.codex_inspection_state_enabled');
};

const countActions = (items: CodexInspectionResultItem[]) => {
  const summary = {
    delete: 0,
    disable: 0,
    enable: 0,
  };

  items.forEach((item) => {
    if (item.action === 'delete') summary.delete += 1;
    if (item.action === 'disable') summary.disable += 1;
    if (item.action === 'enable') summary.enable += 1;
  });

  return summary;
};

const createIdleProgressSnapshot = (): CodexInspectionProgressSnapshot => ({
  total: 0,
  completed: 0,
  inFlight: 0,
  pending: 0,
  percent: 0,
  status: 'idle',
  summary: {
    totalFiles: 0,
    probeSetCount: 0,
    sampledCount: 0,
    deleteCount: 0,
    disableCount: 0,
    enableCount: 0,
    keepCount: 0,
  },
  startedAt: Date.now(),
  updatedAt: Date.now(),
});

const filterByAction = (items: CodexInspectionResultItem[], filter: ActionFilter) => {
  if (filter === 'all') return items;
  return items.filter((item) => item.action === filter);
};

function Panel({ title, subtitle, extra, children, className }: PanelProps) {
  return (
    <Card className={[styles.panel, className].filter(Boolean).join(' ')}>
      <div className={styles.panelHeader}>
        <div className={styles.panelHeading}>
          <h2 className={styles.panelTitle}>{title}</h2>
          {subtitle ? <p className={styles.panelSubtitle}>{subtitle}</p> : null}
        </div>
        {extra ? <div className={styles.panelExtra}>{extra}</div> : null}
      </div>
      {children}
    </Card>
  );
}

export function CodexInspectionPage() {
  const { t, i18n } = useTranslation();
  const config = useConfigStore((state) => state.config);
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);

  const [inspectionSettings, setInspectionSettings] = useState<CodexInspectionConfigurableSettings>(() =>
    loadCodexInspectionConfigurableSettings(config)
  );
  const [settingsDraft, setSettingsDraft] = useState<InspectionSettingsDraft>(() =>
    toSettingsDraft(loadCodexInspectionConfigurableSettings(config))
  );
  const [isSettingsModalOpen, setIsSettingsModalOpen] = useState(false);
  const [logs, setLogs] = useState<InspectionLogEntry[]>([]);
  const [logsCollapsed, setLogsCollapsed] = useState(true);
  const [runStatus, setRunStatus] = useState<RunStatus>('idle');
  const [progress, setProgress] = useState<CodexInspectionProgressSnapshot>(createIdleProgressSnapshot);
  const [result, setResult] = useState<CodexInspectionRunResult | null>(null);
  const [executing, setExecuting] = useState(false);
  const [actionFilter, setActionFilter] = useState<ActionFilter>('all');
  const logCounterRef = useRef(0);
  const sessionRef = useRef<CodexInspectionSession | null>(null);
  const activeSessionIdRef = useRef<string | null>(null);
  const logListRef = useRef<HTMLDivElement | null>(null);
  const executeItemsRef = useRef<
    ((
      items: CodexInspectionResultItem[],
      options?: { resultOverride?: CodexInspectionRunResult | null; source?: ExecutionTriggerSource }
    ) => Promise<void>) | null
  >(null);

  useEffect(() => {
    const nextSettings = loadCodexInspectionConfigurableSettings(config);
    setInspectionSettings(nextSettings);
    if (!isSettingsModalOpen) {
      setSettingsDraft(toSettingsDraft(nextSettings));
    }
  }, [config, isSettingsModalOpen]);

  const appendLog = useCallback((level: CodexInspectionLogLevel, message: string) => {
    logCounterRef.current += 1;
    setLogs((previous) => [
      ...previous,
      {
        id: `${Date.now()}-${logCounterRef.current}`,
        level,
        message,
        timestamp: Date.now(),
      },
    ]);
  }, []);

  const scrollLogsToBottom = useCallback(() => {
    const element = logListRef.current;
    if (!element) return;
    element.scrollTop = element.scrollHeight;
  }, []);

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
    (session: CodexInspectionSession, promise: Promise<CodexInspectionRunResult>, autoExecuteOnComplete: boolean) => {
      const sessionId = session.id;

      void promise
        .then((nextResult) => {
          if (activeSessionIdRef.current !== sessionId) return;
          const nextActionableResults = nextResult.results.filter(isSuggestedAction);
          setResult(nextResult);
          setProgress(session.getProgress());
          setRunStatus('success');
          setLogsCollapsed(true);
          if (autoExecuteOnComplete) {
            if (nextActionableResults.length > 0 && executeItemsRef.current) {
              const startedMessage = t('monitoring.codex_inspection_auto_execute_started', {
                count: nextActionableResults.length,
              });
              appendLog('info', startedMessage);
              showNotification(startedMessage, 'info');
              void executeItemsRef.current(nextActionableResults, {
                resultOverride: nextResult,
                source: 'auto',
              });
              return;
            }

            const noActionsMessage = t('monitoring.codex_inspection_auto_execute_no_actions');
            appendLog('success', noActionsMessage);
            showNotification(noActionsMessage, 'success');
            return;
          }

          showNotification(t('monitoring.codex_inspection_run_success'), 'success');
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
          appendLog('error', message);
          setRunStatus('error');
          setLogsCollapsed(false);
          showNotification(message, 'error');
        });
    },
    [appendLog, showNotification, t]
  );

  const startFreshInspection = useCallback(
    (
      preserveLogs: boolean = false,
      introMessage: string = '',
      options?: {
        autoExecute?: boolean;
      }
    ) => {
      if (connectionStatus !== 'connected') {
        const message = t('notification.connection_required');
        showNotification(message, 'warning');
        return;
      }

      const autoExecuteOnComplete = options?.autoExecute ?? inspectionSettings.autoExecuteActions;

      if (!preserveLogs) {
        setLogs([]);
      }
      if (introMessage) {
        appendLog('info', introMessage);
      }

      setResult(null);
      setRunStatus('running');
      setLogsCollapsed(false);
      setActionFilter('all');

      const session = createCodexInspectionSession({
        config,
        apiBase,
        managementKey,
        settings: inspectionSettings,
        onLog: (level, message) => {
          if (activeSessionIdRef.current !== session.id) return;
          appendLog(level, message);
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
        },
      });

      sessionRef.current = session;
      activeSessionIdRef.current = session.id;
      setProgress(session.getProgress());
      attachSessionPromise(session, session.start(), autoExecuteOnComplete);
    },
    [
      apiBase,
      appendLog,
      attachSessionPromise,
      config,
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

    appendLog('warning', t('monitoring.codex_inspection_stopped'));
    activeSessionIdRef.current = null;
    sessionRef.current = null;
    currentSession.stop();
    setRunStatus('idle');
    setProgress(createIdleProgressSnapshot());
    setResult(null);
    setLogsCollapsed(false);
  }, [appendLog, t]);

  const executeItems = useCallback(
    async (
      items: CodexInspectionResultItem[],
      options?: {
        resultOverride?: CodexInspectionRunResult | null;
        source?: ExecutionTriggerSource;
      }
    ) => {
      const currentResult = options?.resultOverride ?? result;
      const source = options?.source ?? 'manual';
      if (!currentResult) return;
      const targets = items.filter(isSuggestedAction);
      if (targets.length === 0) {
        showNotification(t('monitoring.codex_inspection_no_pending_actions'), 'info');
        return;
      }

      setExecuting(true);
      setLogsCollapsed(false);
      appendLog('info', t('monitoring.codex_inspection_execute_started'));

      try {
        const execution = await executeCodexInspectionActions({
          settings: currentResult.settings,
          items: targets,
          previousFiles: currentResult.files,
          onLog: appendLog,
        });

        const failed = execution.outcomes.filter((item) => !item.success);
        if (failed.length > 0) {
          showNotification(
            `${t('monitoring.codex_inspection_execute_partial')}: ${failed
              .slice(0, 2)
              .map(buildExecutionFailureMessage)
              .join('；')}`,
            'warning'
          );
        } else {
          showNotification(t('monitoring.codex_inspection_execute_success'), 'success');
        }
        const nextResult = applyCodexInspectionExecutionResult(currentResult, execution);
        setResult(nextResult);

        if (source === 'auto') {
          const successCount = execution.outcomes.filter((item) => item.success).length;
          const failedCount = execution.outcomes.length - successCount;
          const remainingCount = nextResult.results.filter(isSuggestedAction).length;
          const summaryMessage =
            failedCount > 0 || remainingCount > 0
              ? t('monitoring.codex_inspection_auto_execute_summary_partial', {
                  total: targets.length,
                  success: successCount,
                  failed: failedCount,
                  remaining: remainingCount,
                })
              : t('monitoring.codex_inspection_auto_execute_summary_success', {
                  total: targets.length,
                  success: successCount,
                });
          appendLog(failedCount > 0 || remainingCount > 0 ? 'warning' : 'success', summaryMessage);
          showNotification(summaryMessage, failedCount > 0 || remainingCount > 0 ? 'warning' : 'success');
        }
      } finally {
        setExecuting(false);
      }
    },
    [appendLog, result, showNotification, t]
  );

  useEffect(() => {
    executeItemsRef.current = executeItems;
  }, [executeItems]);

  const actionableResults = useMemo(
    () => (result ? result.results.filter(isSuggestedAction) : []),
    [result]
  );

  const filteredResults = useMemo(
    () => filterByAction(actionableResults, actionFilter),
    [actionableResults, actionFilter]
  );

  const handleExecutePlanned = useCallback(() => {
    if (!result) return;

    const targets = actionableResults;
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
  }, [actionableResults, executeItems, result, showConfirmation, t]);

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

  const summaryCards = useMemo<SummaryCard[]>(() => {
    const summarySource =
      runStatus === 'running' || runStatus === 'paused' ? progress.summary : result?.summary ?? null;
    const blank = '--';
    const dash = '—';
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
    const totalActions =
      summarySource !== null
        ? summarySource.deleteCount + summarySource.disableCount + summarySource.enableCount
        : null;

    const probeMeta = summarySource
      ? `${t('monitoring.codex_inspection_target_type')} ${inspectionSettings.targetType}`
      : t('monitoring.codex_inspection_progress_idle');

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
        key: 'total-actions',
        label: t('monitoring.codex_inspection_action_total'),
        value: totalActions === null ? blank : String(totalActions),
        meta:
          totalActions !== null && totalActions > 0
            ? t('monitoring.codex_inspection_pending_actions') + ` ${totalActions}`
            : t('monitoring.codex_inspection_no_pending_actions'),
        tone: totalActions && totalActions > 0 ? 'warn' : 'good',
      },
      {
        key: 'probe-total',
        label: t('monitoring.codex_inspection_total_accounts'),
        value: probeSetCount === null ? blank : String(probeSetCount),
        meta: probeMeta,
      },
      {
        key: 'sampled',
        label: t('monitoring.codex_inspection_sampled_accounts'),
        value: sampledCompleted === null ? blank : String(sampledCompleted),
        meta: sampledMeta,
      },
      {
        key: 'delete',
        label: t('monitoring.codex_inspection_delete_count'),
        value: deleteCount === null ? blank : String(deleteCount),
        meta:
          deleteCount && deleteCount > 0
            ? t('monitoring.codex_inspection_action_delete')
            : dash,
        tone: deleteCount && deleteCount > 0 ? 'bad' : undefined,
      },
      {
        key: 'disable',
        label: t('monitoring.codex_inspection_disable_count'),
        value: disableCount === null ? blank : String(disableCount),
        meta:
          disableCount && disableCount > 0
            ? t('monitoring.codex_inspection_action_disable')
            : dash,
        tone: disableCount && disableCount > 0 ? 'warn' : undefined,
      },
      {
        key: 'enable',
        label: t('monitoring.codex_inspection_enable_count'),
        value: enableCount === null ? blank : String(enableCount),
        meta:
          enableCount && enableCount > 0
            ? t('monitoring.codex_inspection_action_enable')
            : dash,
        tone: enableCount && enableCount > 0 ? 'good' : undefined,
      },
    ];
  }, [
    inspectionSettings.targetType,
    progress.completed,
    progress.percent,
    progress.summary,
    result,
    runStatus,
    t,
  ]);

  const pendingActionCount = actionableResults.length;
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

  const lastFinishedLabel = result && result.finishedAt > 0
    ? `${t('monitoring.codex_inspection_last_finished_at')} · ${formatTime(result.finishedAt, i18n.language)}`
    : null;

  const openSettingsModal = useCallback(() => {
    setSettingsDraft(toSettingsDraft(inspectionSettings));
    setIsSettingsModalOpen(true);
  }, [inspectionSettings]);

  const handleSettingsDraftChange = useCallback(
    (field: InspectionSettingsDraftField, value: string) => {
      setSettingsDraft((previous) => ({
        ...previous,
        [field]: value,
      }));
    },
    []
  );

  const handleAutoExecuteChange = useCallback((value: boolean) => {
    setSettingsDraft((previous) => ({
      ...previous,
      autoExecuteActions: value,
    }));
  }, []);

  const parseNonNegativeInteger = useCallback(
    (value: string, label: string, min: number) => {
      const parsed = Number(value.trim());
      if (!Number.isFinite(parsed) || !Number.isInteger(parsed) || parsed < min) {
        throw new Error(t('monitoring.codex_inspection_settings_invalid_integer', { field: label, min }));
      }
      return parsed;
    },
    [t]
  );

  const handleSaveSettings = useCallback(() => {
    const targetType = settingsDraft.targetType.trim().toLowerCase();
    if (!targetType) {
      showNotification(t('monitoring.codex_inspection_settings_target_type_required'), 'error');
      return;
    }

    try {
      const nextSettings = saveCodexInspectionConfigurableSettings({
        targetType,
        workers: parseNonNegativeInteger(
          settingsDraft.workers,
          t('monitoring.codex_inspection_settings_workers_label'),
          1
        ),
        deleteWorkers: parseNonNegativeInteger(
          settingsDraft.deleteWorkers,
          t('monitoring.codex_inspection_settings_delete_workers_label'),
          1
        ),
        timeout: parseNonNegativeInteger(
          settingsDraft.timeout,
          t('monitoring.codex_inspection_settings_timeout_label'),
          1
        ),
        retries: parseNonNegativeInteger(
          settingsDraft.retries,
          t('monitoring.codex_inspection_settings_retries_label'),
          0
        ),
        userAgent: settingsDraft.userAgent.trim(),
        sampleSize: parseNonNegativeInteger(
          settingsDraft.sampleSize,
          t('monitoring.codex_inspection_settings_sample_size_label'),
          0
        ),
        usedPercentThreshold: (() => {
          const parsed = Number(settingsDraft.usedPercentThreshold.trim());
          if (!Number.isFinite(parsed) || parsed < 0 || parsed > 100) {
            throw new Error(
              t('monitoring.codex_inspection_settings_invalid_threshold', {
                field: t('monitoring.codex_inspection_settings_used_percent_threshold_label'),
              })
            );
          }
          return parsed;
        })(),
        autoExecuteActions: settingsDraft.autoExecuteActions,
      });

      setInspectionSettings(nextSettings);
      setSettingsDraft(toSettingsDraft(nextSettings));
      setIsSettingsModalOpen(false);
      showNotification(t('monitoring.codex_inspection_settings_saved'), 'success');
    } catch (error) {
      showNotification(error instanceof Error ? error.message : String(error || t('common.unknown_error')), 'error');
    }
  }, [parseNonNegativeInteger, settingsDraft, showNotification, t]);

  const handleResetSettings = useCallback(() => {
    clearCodexInspectionConfigurableSettings();
    const nextSettings = saveCodexInspectionConfigurableSettings(DEFAULT_CODEX_INSPECTION_SETTINGS);
    setInspectionSettings(nextSettings);
    setSettingsDraft(toSettingsDraft(nextSettings));
    showNotification(t('monitoring.codex_inspection_settings_reset'), 'success');
  }, [showNotification, t]);

  const handleClearLogs = useCallback(() => {
    setLogs([]);
  }, []);

  const handleJumpToLatest = useCallback(() => {
    if (logsCollapsed) {
      setLogsCollapsed(false);
      requestAnimationFrame(scrollLogsToBottom);
      return;
    }
    scrollLogsToBottom();
  }, [logsCollapsed, scrollLogsToBottom]);

  const filterCounts = useMemo(() => {
    const counts = countActions(actionableResults);
    return {
      all: actionableResults.length,
      delete: counts.delete,
      disable: counts.disable,
      enable: counts.enable,
    };
  }, [actionableResults]);

  const filterLabel = (filter: ActionFilter) => {
    switch (filter) {
      case 'delete':
        return t('monitoring.codex_inspection_filter_delete');
      case 'disable':
        return t('monitoring.codex_inspection_filter_disable');
      case 'enable':
        return t('monitoring.codex_inspection_filter_enable');
      case 'all':
      default:
        return t('monitoring.codex_inspection_filter_all');
    }
  };

  const isInspectionInFlight = runStatus === 'running' || runStatus === 'paused';
  const runButtonLabel =
    runStatus === 'paused'
      ? t('monitoring.codex_inspection_resume')
      : runStatus === 'running'
        ? t('monitoring.codex_inspection_running')
        : t('monitoring.codex_inspection_run');

  return (
    <div className={styles.page}>
      <div className={styles.pageHeader}>
        <h1 className={styles.pageTitle}>{t('monitoring.codex_inspection_title')}</h1>
        <p className={styles.description}>{t('monitoring.codex_inspection_desc')}</p>
      </div>

      <Card className={`${styles.panel} ${styles.statusPanel}`}>
        <div className={styles.statusBar}>
          <div className={styles.statusInfo}>
            <span className={`${styles.statusBadge} ${styles[`tone-${statusTone}`]}`}>
              <span className={styles.statusDot} aria-hidden="true" />
              {statusLabel}
            </span>
            <div className={styles.statusMeta}>
              <span>{`${t('monitoring.codex_inspection_target_type')}: ${inspectionSettings.targetType}`}</span>
              <span>{`${t('monitoring.codex_inspection_threshold')}: ${inspectionSettings.usedPercentThreshold}%`}</span>
              <span>{`${t('monitoring.codex_inspection_workers')}: ${inspectionSettings.workers}`}</span>
              <span>{`${t('monitoring.codex_inspection_sample_size')}: ${inspectionSettings.sampleSize || t('common.no')}`}</span>
              {inspectionSettings.autoExecuteActions ? (
                <span className={styles.statusMetaWarn}>
                  {t('monitoring.codex_inspection_settings_auto_execute_actions_label')}
                </span>
              ) : null}
              {lastFinishedLabel ? <span>{lastFinishedLabel}</span> : null}
              {pendingActionCount > 0 ? (
                <span
                  className={styles.statusMetaWarn}
                >{`${t('monitoring.codex_inspection_pending_total')} ${pendingActionCount}`}</span>
              ) : null}
            </div>
          </div>

          <div className={styles.statusActions}>
            <Link to="/monitoring" className={styles.quickLink}>
              <IconExternalLink size={14} />
              <span>{t('monitoring.codex_inspection_back')}</span>
            </Link>
            <Link to="/monitoring/codex-inspection/tasks" className={styles.quickLink}>
              <IconSettings size={14} />
              <span>巡检任务</span>
            </Link>
            <button
              type="button"
              className={styles.iconButton}
              onClick={openSettingsModal}
              disabled={isInspectionInFlight || executing}
              aria-label={t('monitoring.codex_inspection_settings_button')}
              title={t('monitoring.codex_inspection_settings_button')}
            >
              <IconSettings size={16} />
            </button>
            <Button
              variant="primary"
              onClick={handleRunInspection}
              loading={runStatus === 'running'}
              disabled={runStatus === 'running' || executing || connectionStatus !== 'connected'}
            >
              {runButtonLabel}
            </Button>
            {isInspectionInFlight ? (
              <>
                <Button
                  variant="secondary"
                  onClick={handlePauseInspection}
                  disabled={runStatus !== 'running' || executing}
                >
                  {t('monitoring.codex_inspection_pause')}
                </Button>
                <Button variant="danger" onClick={handleStopInspection} disabled={executing}>
                  {t('monitoring.codex_inspection_stop')}
                </Button>
              </>
            ) : null}
          </div>
        </div>

        {showProgressBar ? (
          <div className={styles.progressSection}>
            <div className={styles.progressHeader}>
              <strong>{t('monitoring.codex_inspection_progress_title')}</strong>
              <span>{`${progress.percent}%`}</span>
            </div>
            <div className={styles.progressTrack}>
              <span
                className={styles.progressBar}
                style={{ width: `${Math.max(0, Math.min(100, progress.percent))}%` }}
              />
            </div>
            <div className={styles.progressMeta}>
              <span>{progressLabel}</span>
              {runStatus === 'paused' ? <strong>{t('monitoring.codex_inspection_paused')}</strong> : null}
            </div>
          </div>
        ) : null}
      </Card>

      <section className={styles.summaryGrid}>
        {summaryCards.map((card) => (
          <Card
            key={card.key}
            className={[
              styles.summaryCard,
              card.tone ? styles[`tone-${card.tone}`] : '',
            ]
              .filter(Boolean)
              .join(' ')}
          >
            <span className={styles.summaryLabel}>{card.label}</span>
            <strong className={styles.summaryValue}>{card.value}</strong>
            <span className={styles.summaryMeta}>{card.meta}</span>
          </Card>
        ))}
      </section>

      <Panel
        title={t('monitoring.codex_inspection_results_title')}
        subtitle={t('monitoring.codex_inspection_results_desc')}
        extra={
          <div className={styles.resultsHeaderActions}>
            <Button
              variant={pendingActionCount > 0 ? 'danger' : 'primary'}
              size="sm"
              onClick={handleExecutePlanned}
              loading={executing}
              disabled={!result || isInspectionInFlight || executing || pendingActionCount === 0}
            >
              {executing
                ? t('monitoring.codex_inspection_executing')
                : t('monitoring.codex_inspection_execute_now')}
            </Button>
          </div>
        }
      >
        {result ? (
          <>
            <div className={styles.filterRow}>
              <div className={styles.segmentedControl}>
                {ACTION_FILTERS.map((filter) => {
                  const count = filterCounts[filter];
                  const isActive = actionFilter === filter;
                  return (
                    <button
                      key={filter}
                      type="button"
                      className={`${styles.segmentButton} ${isActive ? styles.segmentButtonActive : ''}`}
                      onClick={() => setActionFilter(filter)}
                    >
                      <span>{filterLabel(filter)}</span>
                      <span className={styles.segmentCount}>{count}</span>
                    </button>
                  );
                })}
              </div>
            </div>

            <div className={styles.tableWrap}>
              <table className={styles.table}>
                <colgroup>
                  <col className={styles.accountColumn} />
                  <col className={styles.stateColumn} />
                  <col className={styles.httpColumn} />
                  <col className={styles.usageColumn} />
                  <col className={styles.actionColumn} />
                  <col className={styles.operationColumn} />
                </colgroup>
                <thead>
                  <tr>
                    <th>{t('monitoring.account_label')}</th>
                    <th>{t('monitoring.codex_inspection_current_state')}</th>
                    <th>{t('monitoring.codex_inspection_http_status')}</th>
                    <th>{t('monitoring.codex_inspection_used_percent')}</th>
                    <th>{t('monitoring.codex_inspection_next_action')}</th>
                    <th>{t('common.action')}</th>
                  </tr>
                </thead>
                <tbody>
                  {filteredResults.length > 0 ? (
                    filteredResults.map((item) => (
                      <tr key={item.key}>
                        <td>
                          <div className={styles.primaryCell}>
                            <span className={styles.primaryAccount}>{item.displayAccount}</span>
                            <small className={styles.primaryFile}>
                              {item.fileName}
                              {item.authIndex ? (
                                <span className={styles.primaryIndex}>{` · #${item.authIndex}`}</span>
                              ) : null}
                            </small>
                            {item.actionReason ? (
                              <small className={styles.primaryReason}>{item.actionReason}</small>
                            ) : null}
                            {item.error ? (
                              <small className={styles.primaryError}>{item.error}</small>
                            ) : null}
                          </div>
                        </td>
                        <td>
                          <span
                            className={`${styles.stateChip} ${
                              item.disabled ? styles.stateDisabled : styles.stateEnabled
                            }`}
                          >
                            {formatCurrentStateLabel(item, t)}
                          </span>
                        </td>
                        <td className={styles.monoCell}>
                          {item.statusCode === null ? '--' : item.statusCode}
                        </td>
                        <td className={styles.monoCell}>{formatPercent(item.usedPercent)}</td>
                        <td>
                          <span className={`${styles.actionBadge} ${actionToneClass[item.action]}`}>
                            {formatActionLabel(item.action, t)}
                          </span>
                        </td>
                        <td>
                          <Button
                            size="sm"
                            variant={item.action === 'delete' ? 'danger' : 'secondary'}
                            onClick={() => handleExecuteSingle(item)}
                            disabled={isInspectionInFlight || executing}
                          >
                            {formatActionLabel(item.action, t)}
                          </Button>
                        </td>
                      </tr>
                    ))
                  ) : (
                    <tr>
                      <td colSpan={6}>
                        <div className={styles.emptyBlockSmall}>
                          {actionableResults.length === 0
                            ? t('monitoring.codex_inspection_no_pending_actions')
                            : t('monitoring.codex_inspection_no_pending_actions')}
                        </div>
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </>
        ) : (
          <div className={styles.emptyBlock}>{t('monitoring.codex_inspection_empty')}</div>
        )}
      </Panel>

      <Panel
        title={t('monitoring.codex_inspection_logs_title')}
        subtitle={t('monitoring.codex_inspection_logs_desc')}
        extra={
          <div className={styles.logActions}>
            <button
              type="button"
              className={styles.iconButton}
              onClick={handleJumpToLatest}
              disabled={logs.length === 0}
              aria-label={t('monitoring.codex_inspection_logs_jump_latest')}
              title={t('monitoring.codex_inspection_logs_jump_latest')}
            >
              <IconRefreshCw size={14} />
            </button>
            <button
              type="button"
              className={styles.iconButton}
              onClick={handleClearLogs}
              disabled={logs.length === 0}
              aria-label={t('monitoring.codex_inspection_logs_clear')}
              title={t('monitoring.codex_inspection_logs_clear')}
            >
              <IconTrash2 size={14} />
            </button>
            <button
              type="button"
              className={styles.foldButton}
              onClick={() => setLogsCollapsed((previous) => !previous)}
              disabled={logs.length === 0}
            >
              {logsCollapsed ? <IconChevronDown size={14} /> : <IconChevronUp size={14} />}
              <span>
                {logsCollapsed
                  ? t('monitoring.codex_inspection_expand_logs')
                  : t('monitoring.codex_inspection_fold_logs')}
              </span>
            </button>
          </div>
        }
      >
        {!logsCollapsed ? (
          <div ref={logListRef} className={styles.logList}>
            {logs.length > 0 ? (
              logs.map((entry) => (
                <div key={entry.id} className={`${styles.logRow} ${levelClassMap[entry.level]}`}>
                  <span className={styles.logTime}>{formatTimestamp(entry.timestamp, i18n.language)}</span>
                  <span className={styles.logMessage}>{entry.message}</span>
                </div>
              ))
            ) : (
              <div className={styles.emptyBlockSmall}>{t('monitoring.codex_inspection_logs_empty')}</div>
            )}
          </div>
        ) : (
          <div className={styles.logCollapsedBar}>
            <span>{t('monitoring.codex_inspection_logs_collapsed', { count: logs.length })}</span>
          </div>
        )}
      </Panel>

      <Modal
        open={isSettingsModalOpen}
        onClose={() => setIsSettingsModalOpen(false)}
        title={t('monitoring.codex_inspection_settings_title')}
        width={920}
        className={styles.settingsModal}
      >
        <div className={styles.settingsBody}>
          <section className={styles.settingsSection}>
            <header className={styles.settingsSectionHeader}>
              <span>{t('monitoring.codex_inspection_settings_group_strategy')}</span>
            </header>
            <div className={styles.settingsGrid}>
              <div className={styles.settingsField}>
                <Input
                  label={t('monitoring.codex_inspection_settings_target_type_label')}
                  value={settingsDraft.targetType}
                  onChange={(event) => handleSettingsDraftChange('targetType', event.target.value)}
                  placeholder={DEFAULT_CODEX_INSPECTION_SETTINGS.targetType}
                />
              </div>
              <div className={styles.settingsField}>
                <Input
                  label={t('monitoring.codex_inspection_settings_used_percent_threshold_label')}
                  hint={t('monitoring.codex_inspection_settings_threshold_hint')}
                  type="number"
                  value={settingsDraft.usedPercentThreshold}
                  onChange={(event) => handleSettingsDraftChange('usedPercentThreshold', event.target.value)}
                  min={0}
                  max={100}
                  step={0.1}
                />
              </div>
              <div className={styles.settingsField}>
                <Input
                  label={t('monitoring.codex_inspection_settings_sample_size_label')}
                  hint={t('monitoring.codex_inspection_settings_sample_size_hint')}
                  type="number"
                  value={settingsDraft.sampleSize}
                  onChange={(event) => handleSettingsDraftChange('sampleSize', event.target.value)}
                  min={0}
                  step={1}
                />
              </div>
            </div>
          </section>

          <section className={styles.settingsSection}>
            <header className={styles.settingsSectionHeader}>
              <span>{t('monitoring.codex_inspection_settings_group_concurrency')}</span>
            </header>
            <div className={styles.settingsGrid}>
              <div className={styles.settingsField}>
                <Input
                  label={t('monitoring.codex_inspection_settings_workers_label')}
                  type="number"
                  value={settingsDraft.workers}
                  onChange={(event) => handleSettingsDraftChange('workers', event.target.value)}
                  min={1}
                  step={1}
                />
              </div>
              <div className={styles.settingsField}>
                <Input
                  label={t('monitoring.codex_inspection_settings_delete_workers_label')}
                  type="number"
                  value={settingsDraft.deleteWorkers}
                  onChange={(event) => handleSettingsDraftChange('deleteWorkers', event.target.value)}
                  min={1}
                  step={1}
                />
              </div>
              <div className={styles.settingsField}>
                <Input
                  label={t('monitoring.codex_inspection_settings_timeout_label')}
                  type="number"
                  value={settingsDraft.timeout}
                  onChange={(event) => handleSettingsDraftChange('timeout', event.target.value)}
                  min={1}
                  step={100}
                />
              </div>
              <div className={styles.settingsField}>
                <Input
                  label={t('monitoring.codex_inspection_settings_retries_label')}
                  type="number"
                  value={settingsDraft.retries}
                  onChange={(event) => handleSettingsDraftChange('retries', event.target.value)}
                  min={0}
                  step={1}
                />
              </div>
              <div className={`${styles.settingsField} ${styles.settingsFieldWide}`}>
                <Input
                  label={t('monitoring.codex_inspection_settings_user_agent_label')}
                  value={settingsDraft.userAgent}
                  onChange={(event) => handleSettingsDraftChange('userAgent', event.target.value)}
                  placeholder={DEFAULT_CODEX_INSPECTION_SETTINGS.userAgent}
                />
              </div>
            </div>
          </section>

          <section className={styles.settingsSection}>
            <header className={styles.settingsSectionHeader}>
              <span>{t('monitoring.codex_inspection_settings_group_auto')}</span>
            </header>
            <div className={styles.settingsAutoCard}>
              <div className={styles.settingsAutoToggle}>
                <ToggleSwitch
                  checked={settingsDraft.autoExecuteActions}
                  onChange={handleAutoExecuteChange}
                  label={t('monitoring.codex_inspection_settings_auto_execute_actions_label')}
                  ariaLabel={t('monitoring.codex_inspection_settings_auto_execute_actions_label')}
                  labelPosition="left"
                />
              </div>
              <p className={styles.settingsAutoHint}>
                {t('monitoring.codex_inspection_settings_auto_execute_actions_hint')}
              </p>
              {settingsDraft.autoExecuteActions ? (
                <p className={styles.settingsAutoWarning}>
                  {t('monitoring.codex_inspection_settings_auto_execute_warning')}
                </p>
              ) : null}
            </div>
          </section>
        </div>

        <div className={styles.settingsActionsBar}>
          <Button variant="secondary" onClick={handleResetSettings}>
            {t('monitoring.codex_inspection_settings_reset_button')}
          </Button>
          <Button variant="secondary" onClick={() => setIsSettingsModalOpen(false)}>
            {t('common.cancel')}
          </Button>
          <Button variant="primary" onClick={handleSaveSettings}>
            {t('common.save')}
          </Button>
        </div>
      </Modal>
    </div>
  );
}
