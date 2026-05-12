import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconChartLine,
  IconExternalLink,
  IconEye,
  IconFileText,
  IconRefreshCw,
  IconSettings,
  IconTimer,
  IconTrash2,
} from '@/components/ui/icons';
import {
  isUsageServiceId,
  normalizeUsageServiceBase,
  usageServiceApi,
} from '@/services/api/usageService';
import { useAuthStore, useNotificationStore, useUsageServiceStore } from '@/stores';
import type {
  CodexInspectionActionRecord,
  CodexInspectionAutoAction,
  CodexInspectionLogRetentionConfig,
  CodexInspectionNotificationChannel,
  CodexInspectionNotificationConfig,
  CodexInspectionNotificationRecord,
  CodexInspectionNotificationTrigger,
  CodexInspectionRun,
  CodexInspectionRunResponse,
  CodexInspectionScheduleConfig,
  CodexInspectionSchedulerStatus,
  CodexInspectionTargetScope,
  CodexInspectionTask,
  CodexInspectionTaskPayload,
} from '@/types/codexInspectionTask';
import { detectApiBaseFromLocation } from '@/utils/connection';
import styles from './CodexInspectionTasksPage.module.scss';

type TaskDraft = {
  name: string;
  description: string;
  enabled: boolean;
  targetType: CodexInspectionTargetScope['type'];
  fileNames: string;
  authIndices: string;
  query: string;
  noteIncludes: string;
  scheduleType: CodexInspectionScheduleConfig['type'];
  intervalEvery: string;
  intervalUnit: 'minute' | 'hour' | 'day';
  dailyTimes: string;
  timezone: string;
  concurrency: string;
  timeoutMs: string;
  retries: string;
  saveLogs: boolean;
  retentionMode: CodexInspectionLogRetentionConfig['mode'];
  retentionDays: string;
  retentionCount: string;
  dryRun: boolean;
  zeroQuotaAction: Exclude<CodexInspectionAutoAction, 'delete'>;
  fullQuotaAction: Exclude<CodexInspectionAutoAction, 'delete'>;
  invalidAction: CodexInspectionAutoAction;
  allowDelete: boolean;
  requireDeletePreview: boolean;
  notificationEnabled: boolean;
  notificationTrigger: CodexInspectionNotificationTrigger;
  notificationChannels: CodexInspectionNotificationChannel[];
  telegramBotToken: string;
  telegramChatId: string;
  feishuWebhookUrl: string;
  feishuSecret: string;
  wecomWebhookUrl: string;
  webhookUrl: string;
  webhookHeaders: string;
};

type ModalMode = 'create' | 'edit';

const DEFAULT_DRAFT: TaskDraft = {
  name: '',
  description: '',
  enabled: false,
  targetType: 'all_codex',
  fileNames: '',
  authIndices: '',
  query: '',
  noteIncludes: '',
  scheduleType: 'manual',
  intervalEvery: '6',
  intervalUnit: 'hour',
  dailyTimes: '09:00,13:00,23:30',
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || '',
  concurrency: '4',
  timeoutMs: '15000',
  retries: '0',
  saveLogs: true,
  retentionMode: 'days',
  retentionDays: '30',
  retentionCount: '100',
  dryRun: true,
  zeroQuotaAction: 'disable',
  fullQuotaAction: 'disable',
  invalidAction: 'disable',
  allowDelete: false,
  requireDeletePreview: true,
  notificationEnabled: false,
  notificationTrigger: 'auto_action',
  notificationChannels: ['webhook'],
  telegramBotToken: '',
  telegramChatId: '',
  feishuWebhookUrl: '',
  feishuSecret: '',
  wecomWebhookUrl: '',
  webhookUrl: '',
  webhookHeaders: '',
};

const splitList = (value: string) =>
  value
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);

const toNumber = (value: string, fallback: number, min = 0) => {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < min) return fallback;
  return Math.floor(parsed);
};

const formatDateTime = (value?: number) => {
  if (!value) return '--';
  return new Date(value).toLocaleString();
};

const formatDuration = (value?: number) => {
  if (!value) return '--';
  if (value < 1000) return `${value} ms`;
  return `${(value / 1000).toFixed(1)} s`;
};

const summaryNumber = (run: CodexInspectionRun | null | undefined, key: string) => {
  const value = run?.summary?.[key];
  return typeof value === 'number' ? value : 0;
};

const statusTone = (status?: string) => {
  if (status === 'success') return styles.toneGood;
  if (status === 'partial' || status === 'missed') return styles.toneWarn;
  if (status === 'failed' || status === 'interrupted') return styles.toneBad;
  if (status === 'running' || status === 'queued') return styles.toneInfo;
  return styles.toneMuted;
};

const statusLabel = (status?: string) => {
  switch (status) {
    case 'running':
      return '运行中';
    case 'success':
      return '成功';
    case 'partial':
      return '部分异常';
    case 'failed':
      return '失败';
    case 'interrupted':
      return '已中断';
    case 'queued':
      return '排队中';
    case 'idle':
    default:
      return '空闲';
  }
};

const scheduleLabel = (schedule: CodexInspectionScheduleConfig) => {
  if (schedule.type === 'interval') {
    const unitLabel = schedule.unit === 'day' ? '天' : schedule.unit === 'hour' ? '小时' : '分钟';
    return `每 ${schedule.every} ${unitLabel}`;
  }
  if (schedule.type === 'daily_times') {
    return `每天 ${schedule.times.join('、')}`;
  }
  return '手动执行';
};

const parseHeaders = (value: string): Record<string, string> => {
  const headers: Record<string, string> = {};
  value
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
    .forEach((line) => {
      const separator = line.indexOf(':');
      if (separator <= 0) return;
      const key = line.slice(0, separator).trim();
      const headerValue = line.slice(separator + 1).trim();
      if (key && headerValue) headers[key] = headerValue;
    });
  return headers;
};

const draftFromTask = (task: CodexInspectionTask): TaskDraft => {
  const schedule = task.schedule;
  const target = task.targetScope;
  const notification = task.notification;
  const webhookConfig =
    notification.channelConfigs?.webhook ??
    notification.channelConfigs?.custom ??
    {};
  const telegramConfig = notification.channelConfigs?.telegram ?? {};
  const feishuConfig = notification.channelConfigs?.feishu ?? {};
  const wecomConfig = notification.channelConfigs?.wecom ?? {};
  const headers = webhookConfig.headers;
  const headerText =
    headers && typeof headers === 'object'
      ? Object.entries(headers as Record<string, unknown>)
          .map(([key, value]) => `${key}: ${String(value)}`)
          .join('\n')
      : '';

  return {
    ...DEFAULT_DRAFT,
    name: task.name,
    description: task.description ?? '',
    enabled: task.enabled,
    targetType: target.type,
    fileNames: target.type === 'files' ? target.fileNames.join('\n') : '',
    authIndices: target.type === 'auth_indices' ? target.authIndices.join('\n') : '',
    query: target.type === 'metadata_filter' ? target.query ?? '' : '',
    noteIncludes: target.type === 'metadata_filter' ? target.noteIncludes ?? '' : '',
    scheduleType: schedule.type,
    intervalEvery: schedule.type === 'interval' ? String(schedule.every) : DEFAULT_DRAFT.intervalEvery,
    intervalUnit: schedule.type === 'interval' ? schedule.unit : DEFAULT_DRAFT.intervalUnit,
    dailyTimes: schedule.type === 'daily_times' ? schedule.times.join(',') : DEFAULT_DRAFT.dailyTimes,
    timezone:
      (schedule.type === 'interval' || schedule.type === 'daily_times' ? schedule.timezone : '') ??
      DEFAULT_DRAFT.timezone,
    concurrency: String(task.execution.concurrency),
    timeoutMs: String(task.execution.timeoutMs),
    retries: String(task.execution.retries),
    saveLogs: task.saveLogs,
    retentionMode: task.logRetention.mode,
    retentionDays: task.logRetention.mode === 'days' ? String(task.logRetention.days) : DEFAULT_DRAFT.retentionDays,
    retentionCount:
      task.logRetention.mode === 'latest' ? String(task.logRetention.count) : DEFAULT_DRAFT.retentionCount,
    dryRun: task.dryRun,
    zeroQuotaAction: task.autoAction.zeroQuotaAction,
    fullQuotaAction: task.autoAction.fullQuotaAction,
    invalidAction: task.autoAction.invalidAction,
    allowDelete: task.autoAction.allowDelete,
    requireDeletePreview: task.autoAction.requireDeletePreview,
    notificationEnabled: notification.enabled,
    notificationTrigger: notification.trigger,
    notificationChannels: notification.channels.length ? notification.channels : ['webhook'],
    telegramBotToken: String(telegramConfig.botToken ?? telegramConfig.token ?? ''),
    telegramChatId: String(telegramConfig.chatId ?? telegramConfig.chatID ?? ''),
    feishuWebhookUrl: String(feishuConfig.webhookUrl ?? feishuConfig.url ?? ''),
    feishuSecret: String(feishuConfig.secret ?? ''),
    wecomWebhookUrl: String(wecomConfig.webhookUrl ?? wecomConfig.url ?? ''),
    webhookUrl: String(webhookConfig.url ?? webhookConfig.webhookUrl ?? ''),
    webhookHeaders: headerText,
  };
};

const buildTaskPayload = (draft: TaskDraft): CodexInspectionTaskPayload => {
  let targetScope: CodexInspectionTargetScope = { type: 'all_codex' };
  if (draft.targetType === 'files') {
    targetScope = { type: 'files', fileNames: splitList(draft.fileNames) };
  } else if (draft.targetType === 'auth_indices') {
    targetScope = { type: 'auth_indices', authIndices: splitList(draft.authIndices) };
  } else if (draft.targetType === 'metadata_filter') {
    targetScope = {
      type: 'metadata_filter',
      query: draft.query.trim(),
      noteIncludes: draft.noteIncludes.trim(),
    };
  }

  let schedule: CodexInspectionScheduleConfig = { type: 'manual' };
  if (draft.scheduleType === 'interval') {
    schedule = {
      type: 'interval',
      every: toNumber(draft.intervalEvery, 6, 1),
      unit: draft.intervalUnit,
      timezone: draft.timezone.trim() || undefined,
    };
  } else if (draft.scheduleType === 'daily_times') {
    schedule = {
      type: 'daily_times',
      times: splitList(draft.dailyTimes),
      timezone: draft.timezone.trim() || undefined,
    };
  }

  let logRetention: CodexInspectionLogRetentionConfig = { mode: 'none' };
  if (draft.retentionMode === 'days') {
    logRetention = { mode: 'days', days: toNumber(draft.retentionDays, 30, 1) };
  } else if (draft.retentionMode === 'latest') {
    logRetention = { mode: 'latest', count: toNumber(draft.retentionCount, 100, 1) };
  }

  const channelConfigs: CodexInspectionNotificationConfig['channelConfigs'] = {};
  if (draft.telegramBotToken.trim() || draft.telegramChatId.trim()) {
    channelConfigs.telegram = {
      botToken: draft.telegramBotToken.trim(),
      chatId: draft.telegramChatId.trim(),
    };
  }
  if (draft.feishuWebhookUrl.trim()) {
    channelConfigs.feishu = {
      webhookUrl: draft.feishuWebhookUrl.trim(),
      secret: draft.feishuSecret.trim(),
    };
  }
  if (draft.wecomWebhookUrl.trim()) {
    channelConfigs.wecom = {
      webhookUrl: draft.wecomWebhookUrl.trim(),
    };
  }
  if (draft.webhookUrl.trim()) {
    channelConfigs.webhook = {
      url: draft.webhookUrl.trim(),
      headers: parseHeaders(draft.webhookHeaders),
    };
  }

  return {
    name: draft.name.trim(),
    description: draft.description.trim(),
    enabled: draft.enabled,
    targetScope,
    schedule,
    execution: {
      concurrency: toNumber(draft.concurrency, 4, 1),
      timeoutMs: toNumber(draft.timeoutMs, 15000, 1000),
      retries: toNumber(draft.retries, 0, 0),
    },
    saveLogs: draft.saveLogs,
    logRetention,
    dryRun: draft.dryRun,
    autoAction: {
      dryRun: draft.dryRun,
      zeroQuotaAction: draft.zeroQuotaAction,
      fullQuotaAction: draft.fullQuotaAction,
      invalidAction: draft.invalidAction,
      allowDelete: draft.allowDelete,
      requireDeletePreview: draft.requireDeletePreview,
    },
    notification: {
      enabled: draft.notificationEnabled,
      trigger: draft.notificationTrigger,
      channels: draft.notificationEnabled ? draft.notificationChannels : [],
      channelConfigs,
    },
  };
};

export function CodexInspectionTasksPage() {
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const usageServiceEnabled = useUsageServiceStore((state) => state.enabled);
  const usageServiceBase = useUsageServiceStore((state) => state.serviceBase);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);

  const [serviceBase, setServiceBase] = useState('');
  const [tasks, setTasks] = useState<CodexInspectionTask[]>([]);
  const [runs, setRuns] = useState<CodexInspectionRun[]>([]);
  const [schedulerStatus, setSchedulerStatus] = useState<CodexInspectionSchedulerStatus | null>(null);
  const [selectedTaskId, setSelectedTaskId] = useState('');
  const [selectedRunDetail, setSelectedRunDetail] = useState<CodexInspectionRunResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [runningTaskIds, setRunningTaskIds] = useState<Set<string>>(() => new Set());
  const [modalMode, setModalMode] = useState<ModalMode>('create');
  const [taskModalOpen, setTaskModalOpen] = useState(false);
  const [wizardStep, setWizardStep] = useState(0);
  const [draft, setDraft] = useState<TaskDraft>(DEFAULT_DRAFT);
  const [runDetailOpen, setRunDetailOpen] = useState(false);

  const resolveServiceBase = useCallback(async () => {
    if (usageServiceEnabled && usageServiceBase) {
      return usageServiceBase;
    }
    const candidates = Array.from(
      new Set(
        [apiBase, detectApiBaseFromLocation()]
          .map((value) => normalizeUsageServiceBase(value || ''))
          .filter(Boolean)
      )
    );
    for (const candidate of candidates) {
      try {
        const info = await usageServiceApi.getInfo(candidate);
        if (isUsageServiceId(info.service)) return candidate;
      } catch {
        // CPA 主服务未启用 Usage Service 时会走到这里。
      }
    }
    return '';
  }, [apiBase, usageServiceBase, usageServiceEnabled]);

  const loadData = useCallback(async () => {
    setLoading(true);
    try {
      const base = await resolveServiceBase();
      setServiceBase(base);
      if (!base) {
        setTasks([]);
        setRuns([]);
        setSchedulerStatus(null);
        return;
      }
      const [taskResponse, runResponse, schedulerResponse] = await Promise.all([
        usageServiceApi.getCodexInspectionTasks(base, managementKey),
        usageServiceApi.getCodexInspectionRuns(base, { page: 1, pageSize: 20 }, managementKey),
        usageServiceApi.getCodexInspectionSchedulerStatus(base, managementKey),
      ]);
      setTasks(taskResponse.tasks ?? []);
      setRuns(runResponse.runs ?? []);
      setSchedulerStatus(schedulerResponse);
      setSelectedTaskId((current) => current || taskResponse.tasks?.[0]?.id || '');
    } catch (err) {
      showNotification(err instanceof Error ? err.message : String(err), 'error');
    } finally {
      setLoading(false);
    }
  }, [managementKey, resolveServiceBase, showNotification]);

  useEffect(() => {
    void loadData();
  }, [loadData]);

  const selectedTask = useMemo(
    () => tasks.find((task) => task.id === selectedTaskId) ?? tasks[0] ?? null,
    [selectedTaskId, tasks]
  );

  const selectedTaskRuns = useMemo(
    () => runs.filter((run) => !selectedTask || run.taskId === selectedTask.id),
    [runs, selectedTask]
  );

  const stats = useMemo(
    () => ({
      total: tasks.length,
      enabled: tasks.filter((task) => task.enabled).length,
      running: tasks.filter((task) => task.status === 'running').length + runningTaskIds.size,
      dryRun: tasks.filter((task) => task.dryRun).length,
    }),
    [runningTaskIds.size, tasks]
  );

  const openCreateModal = () => {
    setModalMode('create');
    setDraft({ ...DEFAULT_DRAFT });
    setWizardStep(0);
    setTaskModalOpen(true);
  };

  const openEditModal = (task: CodexInspectionTask) => {
    setModalMode('edit');
    setDraft(draftFromTask(task));
    setWizardStep(0);
    setSelectedTaskId(task.id);
    setTaskModalOpen(true);
  };

  const saveTask = async () => {
    if (!serviceBase) return;
    const payload = buildTaskPayload(draft);
    if (!payload.name) {
      showNotification('任务名称不能为空', 'error');
      return;
    }
    setSaving(true);
    try {
      if (modalMode === 'edit' && selectedTask) {
        await usageServiceApi.updateCodexInspectionTask(serviceBase, selectedTask.id, payload, managementKey);
      } else {
        const response = await usageServiceApi.createCodexInspectionTask(serviceBase, payload, managementKey);
        setSelectedTaskId(response.task.id);
      }
      setTaskModalOpen(false);
      await loadData();
      showNotification('巡检任务已保存', 'success');
    } catch (err) {
      showNotification(err instanceof Error ? err.message : String(err), 'error');
    } finally {
      setSaving(false);
    }
  };

  const setTaskEnabled = async (task: CodexInspectionTask, enabled: boolean) => {
    if (!serviceBase) return;
    try {
      await usageServiceApi.setCodexInspectionTaskEnabled(serviceBase, task.id, enabled, managementKey);
      await loadData();
      showNotification(enabled ? '任务已启用' : '任务已停用', 'success');
    } catch (err) {
      showNotification(err instanceof Error ? err.message : String(err), 'error');
    }
  };

  const runTask = async (task: CodexInspectionTask) => {
    if (!serviceBase || runningTaskIds.has(task.id)) return;
    setRunningTaskIds((previous) => new Set(previous).add(task.id));
    try {
      const detail = await usageServiceApi.runCodexInspectionTask(serviceBase, task.id, {}, managementKey);
      setSelectedRunDetail(detail);
      setRunDetailOpen(true);
      await loadData();
      showNotification('巡检执行完成', detail.run.status === 'success' ? 'success' : 'warning');
    } catch (err) {
      showNotification(err instanceof Error ? err.message : String(err), 'error');
    } finally {
      setRunningTaskIds((previous) => {
        const next = new Set(previous);
        next.delete(task.id);
        return next;
      });
    }
  };

  const deleteTask = (task: CodexInspectionTask) => {
    if (!serviceBase) return;
    showConfirmation({
      title: '删除巡检任务',
      message: `确认删除「${task.name}」？任务历史日志会保留到清理策略处理。`,
      confirmText: '删除',
      variant: 'danger',
      onConfirm: async () => {
        await usageServiceApi.deleteCodexInspectionTask(serviceBase, task.id, managementKey);
        if (selectedTaskId === task.id) setSelectedTaskId('');
        await loadData();
        showNotification('巡检任务已删除', 'success');
      },
    });
  };

  const openRunDetail = async (run: CodexInspectionRun) => {
    if (!serviceBase) return;
    try {
      const detail = await usageServiceApi.getCodexInspectionRun(serviceBase, run.id, managementKey);
      setSelectedRunDetail(detail);
      setRunDetailOpen(true);
    } catch (err) {
      showNotification(err instanceof Error ? err.message : String(err), 'error');
    }
  };

  const updateDraft = <K extends keyof TaskDraft>(key: K, value: TaskDraft[K]) => {
    setDraft((previous) => ({ ...previous, [key]: value }));
  };

  const toggleNotificationChannel = (channel: CodexInspectionNotificationChannel) => {
    setDraft((previous) => {
      const exists = previous.notificationChannels.includes(channel);
      return {
        ...previous,
        notificationChannels: exists
          ? previous.notificationChannels.filter((item) => item !== channel)
          : [...previous.notificationChannels, channel],
      };
    });
  };

  const selectedRunActions = selectedRunDetail?.actions ?? [];
  const selectedRunNotifications = selectedRunDetail?.notifications ?? [];

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <p className={styles.eyebrow}>Codex Account Inspection</p>
          <h1>Codex 巡检任务</h1>
          <p>创建、调度、自动处理并审计 Codex 账号巡检结果。</p>
        </div>
        <div className={styles.headerActions}>
          <Link to="/monitoring/codex-inspection" className={styles.secondaryLink}>
            <IconChartLine size={16} />
            <span>手动巡检</span>
            <IconExternalLink size={14} />
          </Link>
          <Button variant="secondary" onClick={loadData} loading={loading}>
            <IconRefreshCw size={16} />
            刷新
          </Button>
          <Button onClick={openCreateModal}>
            <IconTimer size={16} />
            新建任务
          </Button>
        </div>
      </header>

      {!serviceBase && !loading ? (
        <Card className={styles.notice}>
          <IconFileText size={20} />
          <div>
            <strong>Usage Service 未连接</strong>
            <p>Codex 巡检任务由 Usage Service 调度执行。请先在请求监控中启用 Usage Service。</p>
          </div>
        </Card>
      ) : null}

      <section className={styles.statsGrid}>
        <MetricCard label="任务总数" value={String(stats.total)} meta="全部 Codex 巡检任务" />
        <MetricCard label="已启用" value={String(stats.enabled)} meta="会被服务端调度器扫描" tone="good" />
        <MetricCard label="运行中" value={String(stats.running)} meta="包含手动触发中的任务" tone="info" />
        <MetricCard
          label="调度器"
          value={schedulerStatus?.running ? '运行中' : '未启动'}
          meta={`tick ${schedulerStatus?.tickIntervalMs ?? 0} ms`}
          tone={schedulerStatus?.running ? 'good' : 'warn'}
        />
      </section>

      <section className={styles.workspace}>
        <Card className={styles.taskListPanel}>
          <div className={styles.panelHeader}>
            <div>
              <h2>任务列表</h2>
              <p>按更新时间排序，选择任务查看最近执行和策略。</p>
            </div>
          </div>
          <div className={styles.taskList}>
            {tasks.map((task) => (
              <button
                key={task.id}
                type="button"
                className={`${styles.taskRow} ${selectedTask?.id === task.id ? styles.taskRowActive : ''}`}
                onClick={() => setSelectedTaskId(task.id)}
              >
                <span className={`${styles.statusPill} ${task.enabled ? styles.pillGood : styles.pillMuted}`}>
                  {task.enabled ? '启用' : '停用'}
                </span>
                <span className={styles.taskRowMain}>
                  <strong>{task.name}</strong>
                  <small>{scheduleLabel(task.schedule)}</small>
                </span>
                <span className={`${styles.runStatus} ${statusTone(task.lastRunStatus ?? task.status)}`}>
                  {statusLabel(task.lastRunStatus ?? task.status)}
                </span>
              </button>
            ))}
            {tasks.length === 0 ? (
              <div className={styles.emptyState}>
                <IconTimer size={24} />
                <p>还没有巡检任务。</p>
                <Button size="sm" onClick={openCreateModal}>新建任务</Button>
              </div>
            ) : null}
          </div>
        </Card>

        <aside className={styles.detailPanel}>
          {selectedTask ? (
            <>
              <div className={styles.detailHeader}>
                <div>
                  <span className={`${styles.statusPill} ${selectedTask.enabled ? styles.pillGood : styles.pillMuted}`}>
                    {selectedTask.enabled ? '已启用' : '已停用'}
                  </span>
                  <h2>{selectedTask.name}</h2>
                  <p>{selectedTask.description || '未填写描述'}</p>
                </div>
                <div className={styles.detailActions}>
                  <button type="button" title="编辑" onClick={() => openEditModal(selectedTask)}>
                    <IconSettings size={16} />
                  </button>
                  <button type="button" title="手动运行" onClick={() => runTask(selectedTask)}>
                    <IconTimer size={16} />
                  </button>
                  <button type="button" title="删除" onClick={() => deleteTask(selectedTask)}>
                    <IconTrash2 size={16} />
                  </button>
                </div>
              </div>

              {runningTaskIds.has(selectedTask.id) ? (
                <div className={styles.runningCard}>
                  <span className={styles.spinner} />
                  <div>
                    <strong>巡检执行中</strong>
                    <p>正在调用 CPA Management API 探测 Codex 账号。</p>
                  </div>
                </div>
              ) : null}

              <div className={styles.detailGrid}>
                <InfoItem label="下次执行" value={formatDateTime(selectedTask.nextRunAtMs)} />
                <InfoItem label="最近执行" value={formatDateTime(selectedTask.lastRunAtMs)} />
                <InfoItem label="并发/超时" value={`${selectedTask.execution.concurrency} / ${selectedTask.execution.timeoutMs}ms`} />
                <InfoItem label="Dry-run" value={selectedTask.dryRun ? '开启' : '关闭'} />
                <InfoItem label="范围" value={selectedTask.targetScope.type} />
                <InfoItem label="日志保留" value={retentionLabel(selectedTask.logRetention)} />
              </div>

              <div className={styles.strategyGrid}>
                <PolicyBadge label="零额度" value={selectedTask.autoAction.zeroQuotaAction} />
                <PolicyBadge label="满额度" value={selectedTask.autoAction.fullQuotaAction} />
                <PolicyBadge label="失效账号" value={selectedTask.autoAction.invalidAction} danger={selectedTask.autoAction.invalidAction === 'delete'} />
                <PolicyBadge label="自动删除" value={selectedTask.autoAction.allowDelete ? '允许' : '关闭'} danger={selectedTask.autoAction.allowDelete} />
              </div>

              <div className={styles.detailFooter}>
                <ToggleSwitch
                  checked={selectedTask.enabled}
                  onChange={(checked) => void setTaskEnabled(selectedTask, checked)}
                  label={selectedTask.enabled ? '启用中' : '已停用'}
                />
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => runTask(selectedTask)}
                  loading={runningTaskIds.has(selectedTask.id)}
                >
                  <IconTimer size={15} />
                  手动运行
                </Button>
              </div>
            </>
          ) : (
            <div className={styles.emptyState}>
              <IconEye size={24} />
              <p>选择一个任务查看详情。</p>
            </div>
          )}
        </aside>
      </section>

      <Card className={styles.logsPanel}>
        <div className={styles.panelHeader}>
          <div>
            <h2>最近执行日志</h2>
            <p>点击一条执行记录查看账号结果、自动操作和通知发送记录。</p>
          </div>
        </div>
        <div className={styles.runTable}>
          <div className={styles.runTableHeader}>
            <span>任务</span>
            <span>状态</span>
            <span>触发</span>
            <span>账号</span>
            <span>操作</span>
            <span>开始时间</span>
            <span>耗时</span>
            <span />
          </div>
          {selectedTaskRuns.map((run) => (
            <button key={run.id} type="button" className={styles.runRow} onClick={() => openRunDetail(run)}>
              <span>{tasks.find((task) => task.id === run.taskId)?.name ?? run.taskId}</span>
              <span className={`${styles.runStatus} ${statusTone(run.status)}`}>{statusLabel(run.status)}</span>
              <span>{run.trigger}</span>
              <span>{summaryNumber(run, 'total')}</span>
              <span>
                禁用 {summaryNumber(run, 'disableCount')} / 启用 {summaryNumber(run, 'enableCount')} / 删除{' '}
                {summaryNumber(run, 'deleteCount')}
              </span>
              <span>{formatDateTime(run.startedAtMs)}</span>
              <span>{formatDuration(run.durationMs)}</span>
              <span className={styles.rowIcon}><IconEye size={16} /></span>
            </button>
          ))}
          {selectedTaskRuns.length === 0 ? <div className={styles.emptyRow}>暂无执行日志</div> : null}
        </div>
      </Card>

      <TaskModal
        open={taskModalOpen}
        mode={modalMode}
        draft={draft}
        wizardStep={wizardStep}
        saving={saving}
        onDraftChange={updateDraft}
        onToggleChannel={toggleNotificationChannel}
        onStepChange={setWizardStep}
        onClose={() => setTaskModalOpen(false)}
        onSave={saveTask}
      />

      <RunDetailModal
        open={runDetailOpen}
        detail={selectedRunDetail}
        actions={selectedRunActions}
        notifications={selectedRunNotifications}
        onClose={() => setRunDetailOpen(false)}
      />
    </div>
  );
}

function MetricCard({ label, value, meta, tone }: { label: string; value: string; meta: string; tone?: 'good' | 'info' | 'warn' }) {
  return (
    <Card className={`${styles.metricCard} ${tone ? styles[`metric-${tone}`] : ''}`}>
      <span>{label}</span>
      <strong>{value}</strong>
      <small>{meta}</small>
    </Card>
  );
}

function InfoItem({ label, value }: { label: string; value: string }) {
  return (
    <div className={styles.infoItem}>
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function PolicyBadge({ label, value, danger }: { label: string; value: string; danger?: boolean }) {
  return (
    <div className={`${styles.policyBadge} ${danger ? styles.policyDanger : ''}`}>
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

function retentionLabel(config: CodexInspectionLogRetentionConfig) {
  if (config.mode === 'days') return `${config.days} 天`;
  if (config.mode === 'latest') return `最近 ${config.count} 条`;
  return '不自动清理';
}

function TaskModal({
  open,
  mode,
  draft,
  wizardStep,
  saving,
  onDraftChange,
  onToggleChannel,
  onStepChange,
  onClose,
  onSave,
}: {
  open: boolean;
  mode: ModalMode;
  draft: TaskDraft;
  wizardStep: number;
  saving: boolean;
  onDraftChange: <K extends keyof TaskDraft>(key: K, value: TaskDraft[K]) => void;
  onToggleChannel: (channel: CodexInspectionNotificationChannel) => void;
  onStepChange: (step: number) => void;
  onClose: () => void;
  onSave: () => void;
}) {
  const footer = (
    <div className={styles.modalFooter}>
      <Button variant="secondary" onClick={onClose} disabled={saving}>取消</Button>
      {wizardStep > 0 ? (
        <Button variant="secondary" onClick={() => onStepChange(wizardStep - 1)} disabled={saving}>上一步</Button>
      ) : null}
      {wizardStep < 2 ? (
        <Button onClick={() => onStepChange(wizardStep + 1)} disabled={saving}>下一步</Button>
      ) : (
        <Button onClick={onSave} loading={saving}>{mode === 'edit' ? '保存任务' : '创建任务'}</Button>
      )}
    </div>
  );

  return (
    <Modal open={open} title={mode === 'edit' ? '编辑巡检任务' : '新建巡检任务'} onClose={onClose} footer={footer} width={860}>
      <div className={styles.wizardSteps}>
        {['基础配置', '范围与调度', '处理与通知'].map((label, index) => (
          <button
            key={label}
            type="button"
            className={index === wizardStep ? styles.stepActive : ''}
            onClick={() => onStepChange(index)}
          >
            <span>{index + 1}</span>
            {label}
          </button>
        ))}
      </div>

      {wizardStep === 0 ? (
        <div className={styles.formGrid}>
          <Input label="任务名称" value={draft.name} onChange={(event) => onDraftChange('name', event.target.value)} />
          <Input label="描述" value={draft.description} onChange={(event) => onDraftChange('description', event.target.value)} />
          <ToggleSwitch checked={draft.enabled} onChange={(value) => onDraftChange('enabled', value)} label="启用任务" />
          <ToggleSwitch checked={draft.dryRun} onChange={(value) => onDraftChange('dryRun', value)} label="Dry-run 模式" />
          <Input label="并发数" type="number" min={1} value={draft.concurrency} onChange={(event) => onDraftChange('concurrency', event.target.value)} />
          <Input label="超时时间 ms" type="number" min={1000} value={draft.timeoutMs} onChange={(event) => onDraftChange('timeoutMs', event.target.value)} />
          <Input label="失败重试次数" type="number" min={0} value={draft.retries} onChange={(event) => onDraftChange('retries', event.target.value)} />
          <ToggleSwitch checked={draft.saveLogs} onChange={(value) => onDraftChange('saveLogs', value)} label="保存任务日志" />
        </div>
      ) : null}

      {wizardStep === 1 ? (
        <div className={styles.formGrid}>
          <label className={styles.field}>
            <span>巡检范围</span>
            <select value={draft.targetType} onChange={(event) => onDraftChange('targetType', event.target.value as TaskDraft['targetType'])}>
              <option value="all_codex">全部 Codex 账号</option>
              <option value="files">指定认证文件</option>
              <option value="auth_indices">指定 auth_index</option>
              <option value="metadata_filter">元数据筛选</option>
            </select>
          </label>
          {draft.targetType === 'files' ? (
            <label className={styles.fieldWide}>
              <span>认证文件名</span>
              <textarea value={draft.fileNames} onChange={(event) => onDraftChange('fileNames', event.target.value)} placeholder="每行一个文件名，或用逗号分隔" />
            </label>
          ) : null}
          {draft.targetType === 'auth_indices' ? (
            <label className={styles.fieldWide}>
              <span>auth_index</span>
              <textarea value={draft.authIndices} onChange={(event) => onDraftChange('authIndices', event.target.value)} placeholder="每行一个 auth_index，或用逗号分隔" />
            </label>
          ) : null}
          {draft.targetType === 'metadata_filter' ? (
            <>
              <Input label="关键词" value={draft.query} onChange={(event) => onDraftChange('query', event.target.value)} />
              <Input label="备注包含" value={draft.noteIncludes} onChange={(event) => onDraftChange('noteIncludes', event.target.value)} />
            </>
          ) : null}
          <label className={styles.field}>
            <span>执行方式</span>
            <select value={draft.scheduleType} onChange={(event) => onDraftChange('scheduleType', event.target.value as TaskDraft['scheduleType'])}>
              <option value="manual">手动执行</option>
              <option value="interval">固定频率</option>
              <option value="daily_times">多个指定时间点</option>
            </select>
          </label>
          {draft.scheduleType === 'interval' ? (
            <>
              <Input label="每 N" type="number" min={1} value={draft.intervalEvery} onChange={(event) => onDraftChange('intervalEvery', event.target.value)} />
              <label className={styles.field}>
                <span>单位</span>
                <select value={draft.intervalUnit} onChange={(event) => onDraftChange('intervalUnit', event.target.value as TaskDraft['intervalUnit'])}>
                  <option value="minute">分钟</option>
                  <option value="hour">小时</option>
                  <option value="day">天</option>
                </select>
              </label>
            </>
          ) : null}
          {draft.scheduleType === 'daily_times' ? (
            <Input label="执行时间点" value={draft.dailyTimes} onChange={(event) => onDraftChange('dailyTimes', event.target.value)} hint="示例：09:00,13:00,23:30" />
          ) : null}
          <Input label="时区" value={draft.timezone} onChange={(event) => onDraftChange('timezone', event.target.value)} placeholder="Asia/Shanghai" />
          <label className={styles.field}>
            <span>日志保留</span>
            <select value={draft.retentionMode} onChange={(event) => onDraftChange('retentionMode', event.target.value as TaskDraft['retentionMode'])}>
              <option value="days">按天数</option>
              <option value="latest">保留最近 N 条</option>
              <option value="none">不自动清理</option>
            </select>
          </label>
          {draft.retentionMode === 'days' ? (
            <Input label="保留天数" type="number" min={1} value={draft.retentionDays} onChange={(event) => onDraftChange('retentionDays', event.target.value)} />
          ) : null}
          {draft.retentionMode === 'latest' ? (
            <Input label="保留最近条数" type="number" min={1} value={draft.retentionCount} onChange={(event) => onDraftChange('retentionCount', event.target.value)} />
          ) : null}
        </div>
      ) : null}

      {wizardStep === 2 ? (
        <div className={styles.formGrid}>
          <label className={styles.field}>
            <span>零额度账号</span>
            <select value={draft.zeroQuotaAction} onChange={(event) => onDraftChange('zeroQuotaAction', event.target.value as TaskDraft['zeroQuotaAction'])}>
              <option value="none">不处理</option>
              <option value="disable">自动禁用</option>
              <option value="enable">自动启用</option>
            </select>
          </label>
          <label className={styles.field}>
            <span>满额度账号</span>
            <select value={draft.fullQuotaAction} onChange={(event) => onDraftChange('fullQuotaAction', event.target.value as TaskDraft['fullQuotaAction'])}>
              <option value="none">不处理</option>
              <option value="disable">自动禁用</option>
              <option value="enable">自动启用</option>
            </select>
          </label>
          <label className={styles.field}>
            <span>失效账号</span>
            <select value={draft.invalidAction} onChange={(event) => onDraftChange('invalidAction', event.target.value as CodexInspectionAutoAction)}>
              <option value="none">不处理</option>
              <option value="disable">自动禁用</option>
              <option value="enable">自动启用</option>
              <option value="delete">自动删除</option>
            </select>
          </label>
          <ToggleSwitch checked={draft.allowDelete} onChange={(value) => onDraftChange('allowDelete', value)} label="允许自动删除" />
          <ToggleSwitch checked={draft.requireDeletePreview} onChange={(value) => onDraftChange('requireDeletePreview', value)} label="删除前必须预览" />
          {(draft.invalidAction === 'delete' || draft.allowDelete) ? (
            <div className={styles.dangerNotice}>
              <IconTrash2 size={18} />
              <span>自动删除默认关闭。实际删除需要关闭 dry-run、开启允许自动删除，并关闭预览保护。</span>
            </div>
          ) : null}
          <ToggleSwitch checked={draft.notificationEnabled} onChange={(value) => onDraftChange('notificationEnabled', value)} label="启用通知" />
          <label className={styles.field}>
            <span>通知触发条件</span>
            <select value={draft.notificationTrigger} onChange={(event) => onDraftChange('notificationTrigger', event.target.value as CodexInspectionNotificationTrigger)}>
              <option value="always">每次巡检</option>
              <option value="abnormal">仅异常</option>
              <option value="auto_action">仅有自动操作</option>
              <option value="manual_required">仅需人工处理</option>
            </select>
          </label>
          <div className={styles.channelGroup}>
            {(['telegram', 'feishu', 'wecom', 'webhook'] as CodexInspectionNotificationChannel[]).map((channel) => (
              <label key={channel} className={styles.checkboxPill}>
                <input
                  type="checkbox"
                  checked={draft.notificationChannels.includes(channel)}
                  onChange={() => onToggleChannel(channel)}
                />
                <span>{channel}</span>
              </label>
            ))}
          </div>
          {draft.notificationChannels.includes('telegram') ? (
            <>
              <Input label="Telegram Bot Token" value={draft.telegramBotToken} onChange={(event) => onDraftChange('telegramBotToken', event.target.value)} />
              <Input label="Telegram Chat ID" value={draft.telegramChatId} onChange={(event) => onDraftChange('telegramChatId', event.target.value)} />
            </>
          ) : null}
          {draft.notificationChannels.includes('feishu') ? (
            <>
              <Input label="飞书机器人 Webhook" value={draft.feishuWebhookUrl} onChange={(event) => onDraftChange('feishuWebhookUrl', event.target.value)} />
              <Input label="飞书 Secret" value={draft.feishuSecret} onChange={(event) => onDraftChange('feishuSecret', event.target.value)} />
            </>
          ) : null}
          {draft.notificationChannels.includes('wecom') ? (
            <Input label="企业微信机器人 Webhook" value={draft.wecomWebhookUrl} onChange={(event) => onDraftChange('wecomWebhookUrl', event.target.value)} />
          ) : null}
          <Input label="自定义 Webhook URL" value={draft.webhookUrl} onChange={(event) => onDraftChange('webhookUrl', event.target.value)} />
          <label className={styles.fieldWide}>
            <span>Webhook Header</span>
            <textarea value={draft.webhookHeaders} onChange={(event) => onDraftChange('webhookHeaders', event.target.value)} placeholder="Authorization: Bearer xxx" />
          </label>
        </div>
      ) : null}
    </Modal>
  );
}

function RunDetailModal({
  open,
  detail,
  actions,
  notifications,
  onClose,
}: {
  open: boolean;
  detail: CodexInspectionRunResponse | null;
  actions: CodexInspectionActionRecord[];
  notifications: CodexInspectionNotificationRecord[];
  onClose: () => void;
}) {
  const run = detail?.run;
  return (
    <Modal open={open} title="执行日志详情" onClose={onClose} width={900}>
      {run ? (
        <div className={styles.runDetail}>
          <div className={styles.detailGrid}>
            <InfoItem label="日志 ID" value={run.id} />
            <InfoItem label="批次 ID" value={run.batchId} />
            <InfoItem label="状态" value={statusLabel(run.status)} />
            <InfoItem label="触发方式" value={run.trigger} />
            <InfoItem label="开始时间" value={formatDateTime(run.startedAtMs)} />
            <InfoItem label="耗时" value={formatDuration(run.durationMs)} />
          </div>
          <div className={styles.resultSummary}>
            <PolicyBadge label="账号总数" value={String(summaryNumber(run, 'total'))} />
            <PolicyBadge label="正常" value={String(summaryNumber(run, 'healthy'))} />
            <PolicyBadge label="零额度" value={String(summaryNumber(run, 'zeroQuota'))} />
            <PolicyBadge label="满额度" value={String(summaryNumber(run, 'fullQuota'))} />
            <PolicyBadge label="失效" value={String(summaryNumber(run, 'invalid'))} />
            <PolicyBadge label="探测失败" value={String(summaryNumber(run, 'probeFailed'))} />
          </div>
          <section>
            <h3>账号结果</h3>
            <div className={styles.accountRows}>
              {(detail.accounts ?? []).map((account) => (
                <div key={account.id ?? `${account.runId}-${account.fileName}`} className={styles.accountRow}>
                  <strong>{account.displayAccount || account.fileName}</strong>
                  <span>{account.fileName}</span>
                  <span>{account.classification}</span>
                  <span>{account.recommendedAction}</span>
                  <small>{account.actionReason}</small>
                </div>
              ))}
            </div>
          </section>
          <section>
            <h3>自动操作</h3>
            <div className={styles.auditRows}>
              {actions.map((action) => (
                <div key={action.id ?? `${action.runId}-${action.fileName}-${action.action}`} className={styles.auditRow}>
                  <span>{action.action}</span>
                  <strong>{action.fileName}</strong>
                  <span>{action.dryRun ? 'dry-run' : 'real'}</span>
                  <span className={action.success ? styles.toneGood : styles.toneBad}>
                    {action.success ? '成功' : '失败'}
                  </span>
                  <small>{action.error || action.triggerReason}</small>
                </div>
              ))}
              {actions.length === 0 ? <div className={styles.emptyRow}>无自动操作</div> : null}
            </div>
          </section>
          <section>
            <h3>通知结果</h3>
            <div className={styles.auditRows}>
              {notifications.map((record) => (
                <div key={record.id ?? `${record.runId}-${record.channel}`} className={styles.auditRow}>
                  <span>{record.channel}</span>
                  <strong className={record.status === 'success' ? styles.toneGood : styles.toneBad}>
                    {record.status}
                  </strong>
                  <small>{record.error || record.responseSummary}</small>
                </div>
              ))}
              {notifications.length === 0 ? <div className={styles.emptyRow}>无通知记录</div> : null}
            </div>
          </section>
        </div>
      ) : null}
    </Modal>
  );
}
