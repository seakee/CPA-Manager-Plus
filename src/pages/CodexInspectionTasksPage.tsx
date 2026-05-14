import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { Select } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { usePagination } from '@/hooks/usePagination';
import {
  IconAlertTriangle,
  IconBell,
  IconChartLine,
  IconCheck,
  IconChevronDown,
  IconCircleHelp,
  IconCopy,
  IconCreditCard,
  IconDiamond,
  IconEye,
  IconFileText,
  IconFilter,
  IconHand,
  IconInfo,
  IconLightbulb,
  IconPlayCircle,
  IconPlus,
  IconRefreshCw,
  IconSearch,
  IconSettings,
  IconShield,
  IconSend,
  IconTimer,
  IconTrash2,
  IconUserCheck,
  IconUsers,
  IconWebhook,
  IconWifi,
  IconX,
} from '@/components/ui/icons';
import {
  isUsageServiceId,
  normalizeUsageServiceBase,
  usageServiceApi,
} from '@/services/api/usageService';
import { useAuthStore, useNotificationStore, useUsageServiceStore } from '@/stores';
import type {
  CodexInspectionAutoAction,
  CodexInspectionLogRetentionConfig,
  CodexInspectionNotificationChannel,
  CodexInspectionNotificationConfig,
  CodexInspectionNotificationTrigger,
  CodexInspectionRun,
  CodexInspectionScheduleConfig,
  CodexInspectionSchedulerStatus,
  CodexInspectionTargetScope,
  CodexInspectionTask,
  CodexInspectionTaskPayload,
} from '@/types/codexInspectionTask';
import { detectApiBaseFromLocation } from '@/utils/connection';
import {
  findMockCodexInspectionRunDetail,
  getCodexInspectionMockDataset,
  getCodexInspectionMockSearch,
  getCodexInspectionMockScenario,
  isCodexInspectionMockEnabled,
  MOCK_CODEX_INSPECTION_BASE,
} from './codexInspectionMockData';
import styles from './CodexInspectionTasksPage.module.scss';

type TaskDraft = {
  name: string;
  description: string;
  note: string;
  taskTags: string[];
  tagInput: string;
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
  saveAccountDetails: boolean;
  avoidDuplicateRuns: boolean;
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
  notificationTriggers: NotificationTriggerOption[];
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
type DetailTab = 'overview' | 'schedule' | 'scope' | 'policy' | 'notification' | 'logs';
type TaskStatusFilter = 'all' | 'enabled' | 'disabled' | 'running' | 'warning' | 'failed';
type ScheduleFilter = 'all' | CodexInspectionScheduleConfig['type'];
type ScopeFilter = 'all' | CodexInspectionTargetScope['type'];
type NotificationTriggerOption = CodexInspectionNotificationTrigger | 'action_or_abnormal';

const DETAIL_TABS: Array<{ id: DetailTab; label: string }> = [
  { id: 'overview', label: '概览' },
  { id: 'schedule', label: '执行计划' },
  { id: 'scope', label: '巡检范围' },
  { id: 'policy', label: '自动策略' },
  { id: 'notification', label: '通知策略' },
  { id: 'logs', label: '日志记录' },
];

const DEFAULT_TASK_TAGS = ['security', 'high-risk', 'auto-fix'];
const DEFAULT_SELECTED_AUTH_INDICES = [
  'aliciacassian14+gpt@orton.me',
  'alexliu@orton.me',
  'cpa-team@orton.me',
];
const DEFAULT_NOTIFICATION_CHANNELS: CodexInspectionNotificationChannel[] = ['telegram', 'feishu', 'webhook'];
const DEFAULT_NOTIFICATION_TRIGGERS: NotificationTriggerOption[] = [
  'always',
  'abnormal',
  'manual_required',
  'action_or_abnormal',
];
const PROTOTYPE_SCHEDULE_SUMMARY = '每周日 09:00';
const PROTOTYPE_SCOPE_SUMMARY = '高-risk（36 个标签） >';

const WIZARD_STEPS = [
  {
    label: '基本信息',
    title: '基本信息',
    description: '创建一个新的 Codex 巡检任务，用于定期检测与评估您的云环境安全与配置合规性。',
  },
  {
    label: '巡检范围',
    title: '巡检范围',
    description: '选择需要纳入本次巡检的 Codex 账号范围。',
  },
  {
    label: '执行计划',
    title: '执行计划',
    description: '配置任务的执行计划，设置调度方式、时区与并发控制参数。',
  },
  {
    label: '自动处理策略',
    title: '自动化策略（异常处理）',
    description: '配置任务的自动处理策略，设置系统在巡检发现异常时的处理方式与安全保护机制。',
  },
  {
    label: '通知与日志',
    title: '通知与日志',
    description: '配置任务完成后的通知渠道、触发条件和日志保留策略。',
  },
] as const;

const DEFAULT_DRAFT: TaskDraft = {
  name: '',
  description: '',
  note: '',
  taskTags: DEFAULT_TASK_TAGS,
  tagInput: '',
  enabled: true,
  targetType: 'metadata_filter',
  fileNames: '',
  authIndices: DEFAULT_SELECTED_AUTH_INDICES.join('\n'),
  query: 'high-risk',
  noteIncludes: '',
  scheduleType: 'daily_times',
  intervalEvery: '6',
  intervalUnit: 'hour',
  dailyTimes: '09:00',
  timezone: 'Asia/Shanghai',
  concurrency: '1',
  timeoutMs: '1800',
  retries: '2',
  saveLogs: true,
  saveAccountDetails: true,
  avoidDuplicateRuns: true,
  retentionMode: 'days',
  retentionDays: '90',
  retentionCount: '10000',
  dryRun: true,
  zeroQuotaAction: 'disable',
  fullQuotaAction: 'disable',
  invalidAction: 'disable',
  allowDelete: false,
  requireDeletePreview: true,
  notificationEnabled: true,
  notificationTrigger: 'always',
  notificationTriggers: DEFAULT_NOTIFICATION_TRIGGERS,
  notificationChannels: DEFAULT_NOTIFICATION_CHANNELS,
  telegramBotToken: 'telegram-demo-token',
  telegramChatId: '-10012345678',
  feishuWebhookUrl: 'https://open.feishu.cn/open-apis/bot/v2/hook/demo',
  feishuSecret: '',
  wecomWebhookUrl: '',
  webhookUrl: 'https://hooks.example.com/cpa',
  webhookHeaders: '',
};

const splitList = (value: string) =>
  value
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);

const isPrototypeSummaryDraft = (draft: TaskDraft) =>
  draft.targetType === 'metadata_filter' &&
  draft.query.trim() === 'high-risk' &&
  draft.scheduleType === 'daily_times' &&
  splitList(draft.dailyTimes).length === 1 &&
  splitList(draft.dailyTimes)[0] === '09:00';

const notificationChannelIcon = (channel: CodexInspectionNotificationChannel, size = 18) => {
  if (channel === 'telegram') return <IconSend size={size} />;
  if (channel === 'feishu') return <IconDiamond size={size} />;
  if (channel === 'wecom') return <IconBell size={size} />;
  return <IconWebhook size={size} />;
};

const isNotificationChannelConfigured = (draft: TaskDraft, channel: CodexInspectionNotificationChannel) => {
  if (channel === 'telegram') return Boolean(draft.telegramBotToken.trim() && draft.telegramChatId.trim());
  if (channel === 'feishu') return Boolean(draft.feishuWebhookUrl.trim());
  if (channel === 'wecom') return Boolean(draft.wecomWebhookUrl.trim());
  return Boolean(draft.webhookUrl.trim());
};

const notificationChannelDetail = (
  draft: TaskDraft,
  channel: CodexInspectionNotificationChannel,
  configured: boolean
) => {
  if (channel === 'telegram') return configured ? `***${draft.telegramChatId.slice(-4)}` : '未设置 Chat ID';
  if (channel === 'feishu') return configured ? 'Webhook 已配置' : '未设置 Webhook';
  if (channel === 'wecom') return configured ? 'Webhook 已配置' : '未设置 Webhook';
  return configured ? draft.webhookUrl : '未设置 Webhook URL';
};

const notificationChannelViews = (draft: TaskDraft) => {
  const enabledChannels = draft.notificationEnabled ? new Set(draft.notificationChannels) : new Set<CodexInspectionNotificationChannel>();
  return (['telegram', 'feishu', 'wecom', 'webhook'] as CodexInspectionNotificationChannel[]).map((channel) => {
    const configured = isNotificationChannelConfigured(draft, channel);
    return {
      id: channel,
      label: channelDisplayLabel(channel),
      detail: notificationChannelDetail(draft, channel, configured),
      icon: notificationChannelIcon(channel),
      configured,
      enabled: configured && enabledChannels.has(channel),
    };
  });
};

const toNumber = (value: string, fallback: number, min = 0) => {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < min) return fallback;
  return Math.floor(parsed);
};

const formatDateShort = (value?: number) => {
  if (!value) return '--';
  const date = new Date(value);
  const yyyy = date.getFullYear();
  const mm = String(date.getMonth() + 1).padStart(2, '0');
  const dd = String(date.getDate()).padStart(2, '0');
  const hh = String(date.getHours()).padStart(2, '0');
  const min = String(date.getMinutes()).padStart(2, '0');
  return `${yyyy}/${mm}/${dd} ${hh}:${min}`;
};

const formatTimeOfDay = (value?: number) => {
  if (!value) return '--';
  const date = new Date(value);
  const hours = String(date.getHours()).padStart(2, '0');
  const minutes = String(date.getMinutes()).padStart(2, '0');
  return `${hours}:${minutes}`;
};

const formatDurationClock = (value?: number) => {
  if (!value) return '--';
  const totalSeconds = Math.max(0, Math.round(value / 1000));
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`;
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

const scheduleTypeLabel = (schedule: CodexInspectionScheduleConfig) => {
  if (schedule.type === 'interval') return '固定频率';
  if (schedule.type === 'daily_times') return '指定时间';
  return '手动';
};

const scopeLabel = (scope: CodexInspectionTargetScope) => {
  if (scope.type === 'all_codex') return '全部 Codex 账号';
  if (scope.type === 'files') return `指定文件 ${scope.fileNames.length}`;
  if (scope.type === 'auth_indices') return `指定账号 ${scope.authIndices.length}`;
  return '标签/关键字筛选';
};

const actionLabel = (action: CodexInspectionAutoAction) => {
  switch (action) {
    case 'disable':
      return '禁用';
    case 'enable':
      return '启用';
    case 'delete':
      return '删除';
    case 'none':
    default:
      return '不处理';
  }
};

const notificationTriggerLabel = (trigger: CodexInspectionNotificationTrigger) => {
  switch (trigger) {
    case 'always':
      return '每次巡检';
    case 'abnormal':
      return '仅异常';
    case 'auto_action':
      return '仅有自动操作';
    case 'manual_required':
      return '仅需人工处理';
    default:
      return trigger;
  }
};

const targetSearchText = (task: CodexInspectionTask) =>
  [task.id, task.name, task.description, scheduleLabel(task.schedule), scopeLabel(task.targetScope)]
    .filter(Boolean)
    .join(' ')
    .toLowerCase();

const taskResultItems = (run: CodexInspectionRun | null | undefined) => [
  { label: '正常', value: summaryNumber(run, 'healthy'), tone: 'good' },
  { label: '满额', value: summaryNumber(run, 'fullQuota'), tone: 'info' },
  { label: '零额', value: summaryNumber(run, 'zeroQuota'), tone: 'warn' },
  { label: '失效', value: summaryNumber(run, 'invalid'), tone: 'bad' },
  { label: '失败', value: summaryNumber(run, 'probeFailed'), tone: 'bad' },
  {
    label: '动作',
    value:
      summaryNumber(run, 'disableCount') +
      summaryNumber(run, 'enableCount') +
      summaryNumber(run, 'deleteCount'),
    tone: 'action',
  },
];

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

const timeoutSecondsFromMs = (value: number) => String(Math.max(1, Math.round(value / 1000)));

const timeoutMsFromSeconds = (value: string) => toNumber(value, 1800, 1) * 1000;

const notificationTriggersFromSingle = (
  trigger: CodexInspectionNotificationTrigger
): NotificationTriggerOption[] => {
  if (trigger === 'always') return ['always', 'abnormal', 'manual_required', 'action_or_abnormal'];
  if (trigger === 'abnormal') return ['abnormal', 'action_or_abnormal'];
  if (trigger === 'manual_required') return ['manual_required'];
  return ['auto_action'];
};

const backendNotificationTrigger = (
  triggers: NotificationTriggerOption[],
  fallback: CodexInspectionNotificationTrigger
): CodexInspectionNotificationTrigger => {
  const selected = new Set(triggers);
  if (selected.has('always')) return 'always';
  if (selected.has('action_or_abnormal') || selected.has('abnormal')) return 'abnormal';
  if (selected.has('auto_action')) return 'auto_action';
  if (selected.has('manual_required')) return 'manual_required';
  return fallback;
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
    note: task.note ?? '',
    taskTags:
      target.type === 'metadata_filter' && target.query
        ? Array.from(new Set([...DEFAULT_TASK_TAGS, ...splitList(target.query)]))
        : DEFAULT_TASK_TAGS,
    tagInput: '',
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
    timeoutMs: timeoutSecondsFromMs(task.execution.timeoutMs),
    retries: String(task.execution.retries),
    saveLogs: task.saveLogs,
    saveAccountDetails: task.saveLogs,
    avoidDuplicateRuns: DEFAULT_DRAFT.avoidDuplicateRuns,
    retentionMode: task.logRetention.mode,
    retentionDays: task.logRetention.mode === 'days' ? String(task.logRetention.days) : DEFAULT_DRAFT.retentionDays,
    retentionCount:
      task.logRetention.mode === 'latest' ? String(task.logRetention.count) : DEFAULT_DRAFT.retentionCount,
    dryRun: task.dryRun ?? task.autoAction.dryRun ?? DEFAULT_DRAFT.dryRun,
    zeroQuotaAction: task.autoAction.zeroQuotaAction,
    fullQuotaAction: task.autoAction.fullQuotaAction,
    invalidAction: task.autoAction.invalidAction,
    allowDelete: task.autoAction.allowDelete,
    requireDeletePreview: task.autoAction.requireDeletePreview,
    notificationEnabled: notification.enabled,
    notificationTrigger: notification.trigger,
    notificationTriggers: notificationTriggersFromSingle(notification.trigger),
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
      concurrency: toNumber(draft.concurrency, 1, 1),
      timeoutMs: timeoutMsFromSeconds(draft.timeoutMs),
      retries: toNumber(draft.retries, 2, 0),
    },
    saveLogs: draft.saveLogs,
    logRetention,
    dryRun: draft.dryRun,
    autoAction: {
      zeroQuotaAction: draft.zeroQuotaAction,
      fullQuotaAction: draft.fullQuotaAction,
      invalidAction: draft.invalidAction,
      allowDelete: draft.allowDelete,
      requireDeletePreview: draft.requireDeletePreview,
    },
    notification: {
      enabled: draft.notificationEnabled,
      trigger: backendNotificationTrigger(draft.notificationTriggers, draft.notificationTrigger),
      channels: draft.notificationEnabled ? draft.notificationChannels : [],
      channelConfigs,
    },
  };
};

export function CodexInspectionTasksPage() {
  const location = useLocation();
  const navigate = useNavigate();
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const usageServiceEnabled = useUsageServiceStore((state) => state.enabled);
  const usageServiceBase = useUsageServiceStore((state) => state.serviceBase);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const mockModeEnabled = isCodexInspectionMockEnabled(location.search);
  const mockScenario = getCodexInspectionMockScenario(location.search);
  const mockSearch = getCodexInspectionMockSearch(location.search);
  const mockDataset = useMemo(() => getCodexInspectionMockDataset(mockScenario), [mockScenario]);
  const initialSelectedTaskId = mockModeEnabled ? mockDataset.tasks[0]?.id || '' : '';
  const initialRecentRuns = mockModeEnabled
    ? mockDataset.runs
        .filter((run) => run.taskId === initialSelectedTaskId)
        .sort((left, right) => {
          const leftTime = left.startedAtMs ?? left.createdAtMs ?? 0;
          const rightTime = right.startedAtMs ?? right.createdAtMs ?? 0;
          return rightTime - leftTime;
        })
        .slice(0, 10)
    : [];
  const initialRecentRunsTotal = mockModeEnabled
    ? mockDataset.runs.filter((run) => run.taskId === initialSelectedTaskId).length
    : 0;

  const copyToClipboard = useCallback(
    async (text: string, successMessage: string) => {
      try {
        if (navigator.clipboard?.writeText) {
          await navigator.clipboard.writeText(text);
        } else {
          const textarea = document.createElement('textarea');
          textarea.value = text;
          textarea.setAttribute('readonly', '');
          textarea.style.position = 'absolute';
          textarea.style.left = '-9999px';
          document.body.appendChild(textarea);
          textarea.select();
          document.execCommand('copy');
          document.body.removeChild(textarea);
        }
        showNotification(successMessage, 'success');
      } catch (err) {
        showNotification(err instanceof Error ? err.message : '复制失败', 'error');
      }
    },
    [showNotification]
  );

  const [serviceBase, setServiceBase] = useState(() => (mockModeEnabled ? MOCK_CODEX_INSPECTION_BASE : ''));
  const [tasks, setTasks] = useState<CodexInspectionTask[]>(() => (mockModeEnabled ? mockDataset.tasks : []));
  const [runs, setRuns] = useState<CodexInspectionRun[]>(() => (mockModeEnabled ? mockDataset.runs : []));
  const [recentRuns, setRecentRuns] = useState<CodexInspectionRun[]>(() => initialRecentRuns);
  const [recentRunsTotal, setRecentRunsTotal] = useState(initialRecentRunsTotal);
  const [recentRunsPage, setRecentRunsPage] = useState(1);
  const [recentRunsPageSize, setRecentRunsPageSize] = useState(5);
  const [schedulerStatus, setSchedulerStatus] = useState<CodexInspectionSchedulerStatus | null>(() =>
    mockModeEnabled ? mockDataset.schedulerStatus : null
  );
  const [selectedTaskId, setSelectedTaskId] = useState(initialSelectedTaskId);
  const [loading, setLoading] = useState(!mockModeEnabled);
  const [saving, setSaving] = useState(false);
  const [runningTaskIds, setRunningTaskIds] = useState<Set<string>>(() => new Set());
  const [modalMode, setModalMode] = useState<ModalMode>('create');
  const [taskModalOpen, setTaskModalOpen] = useState(false);
  const [wizardStep, setWizardStep] = useState(0);
  const [draft, setDraft] = useState<TaskDraft>(DEFAULT_DRAFT);
  const [detailTab, setDetailTab] = useState<DetailTab>('overview');
  const [keywordFilter, setKeywordFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState<TaskStatusFilter>('all');
  const [scheduleFilter, setScheduleFilter] = useState<ScheduleFilter>('all');
  const [scopeFilter, setScopeFilter] = useState<ScopeFilter>('all');
  const [menuTaskId, setMenuTaskId] = useState('');
  const openMenuRef = useRef<HTMLDivElement | null>(null);
  const [notificationModalOpen, setNotificationModalOpen] = useState(false);

  useEffect(() => {
    if (!menuTaskId) return;
    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Node | null;
      if (!target) return;
      if (openMenuRef.current && openMenuRef.current.contains(target)) return;
      setMenuTaskId('');
    };
    const handleKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setMenuTaskId('');
    };
    document.addEventListener('mousedown', handleClickOutside);
    document.addEventListener('keydown', handleKey);
    return () => {
      document.removeEventListener('mousedown', handleClickOutside);
      document.removeEventListener('keydown', handleKey);
    };
  }, [menuTaskId]);

  const resolveServiceBase = useCallback(async () => {
    if (mockModeEnabled) {
      return MOCK_CODEX_INSPECTION_BASE;
    }
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
  }, [apiBase, mockModeEnabled, usageServiceBase, usageServiceEnabled]);

  const loadData = useCallback(async () => {
    setLoading(true);
    try {
      const base = await resolveServiceBase();
      setServiceBase(base);
      if (mockModeEnabled) {
        setTasks(mockDataset.tasks);
        setRuns(mockDataset.runs);
        setSchedulerStatus(mockDataset.schedulerStatus);
        setSelectedTaskId((current) => current || mockDataset.tasks[0]?.id || '');
        return;
      }
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
  }, [managementKey, mockDataset, mockModeEnabled, resolveServiceBase, showNotification]);

  useEffect(() => {
    void loadData();
  }, [loadData]);

  const selectedTask = useMemo(
    () => tasks.find((task) => task.id === selectedTaskId) ?? tasks[0] ?? null,
    [selectedTaskId, tasks]
  );

  const loadRecentRuns = useCallback(
    async (taskId: string, page: number, pageSize: number) => {
      if (!serviceBase || !taskId) {
        setRecentRuns([]);
        setRecentRunsTotal(0);
        return;
      }
      if (mockModeEnabled) {
        const filtered = mockDataset.runs
          .filter((run) => run.taskId === taskId)
          .sort((left, right) => {
            const leftTime = left.startedAtMs ?? left.createdAtMs ?? 0;
            const rightTime = right.startedAtMs ?? right.createdAtMs ?? 0;
            return rightTime - leftTime;
          });
        const start = Math.max(0, (page - 1) * pageSize);
        setRecentRuns(filtered.slice(start, start + pageSize));
        setRecentRunsTotal(filtered.length);
        return;
      }
      try {
        const response = await usageServiceApi.getCodexInspectionRuns(
          serviceBase,
          { taskId, page, pageSize },
          managementKey
        );
        setRecentRuns(response.runs ?? []);
        setRecentRunsTotal(response.total ?? response.runs?.length ?? 0);
      } catch (err) {
        showNotification(err instanceof Error ? err.message : String(err), 'error');
      }
    },
    [managementKey, mockDataset, mockModeEnabled, serviceBase, showNotification]
  );

  useEffect(() => {
    if (!selectedTask) {
      setRecentRuns([]);
      setRecentRunsTotal(0);
      return;
    }
    void loadRecentRuns(selectedTask.id, recentRunsPage, recentRunsPageSize);
  }, [loadRecentRuns, recentRunsPage, recentRunsPageSize, selectedTask]);

  useEffect(() => {
    setRecentRunsPage(1);
  }, [selectedTask?.id]);

  const selectedTaskRuns = useMemo(
    () => (selectedTask ? recentRuns : []),
    [recentRuns, selectedTask]
  );

  const recentRunsTotalPages = Math.max(1, Math.ceil(recentRunsTotal / recentRunsPageSize));

  const latestRunByTask = useMemo(() => {
    const map = new Map<string, CodexInspectionRun>();
    for (const run of runs) {
      const previous = map.get(run.taskId);
      const runTime = run.startedAtMs ?? run.createdAtMs ?? 0;
      const previousTime = previous?.startedAtMs ?? previous?.createdAtMs ?? 0;
      if (!previous || runTime > previousTime) {
        map.set(run.taskId, run);
      }
    }
    return map;
  }, [runs]);

  const selectedTaskLastRun = selectedTask ? latestRunByTask.get(selectedTask.id) ?? null : null;

  const isHighRiskPolicy = useMemo(() => {
    if (!selectedTask) return false;
    const { allowDelete, invalidAction } = selectedTask.autoAction;
    return Boolean(allowDelete) || invalidAction === 'delete' || !selectedTask.dryRun;
  }, [selectedTask]);

  const filteredTasks = useMemo(() => {
    const normalizedKeyword = keywordFilter.trim().toLowerCase();
    return tasks.filter((task) => {
      const currentStatus = task.lastRunStatus ?? task.status;
      if (normalizedKeyword && !targetSearchText(task).includes(normalizedKeyword)) return false;
      if (statusFilter === 'enabled' && !task.enabled) return false;
      if (statusFilter === 'disabled' && task.enabled) return false;
      if (statusFilter === 'running' && currentStatus !== 'running' && !runningTaskIds.has(task.id)) return false;
      if (statusFilter === 'failed' && currentStatus !== 'failed' && currentStatus !== 'interrupted') return false;
      if (statusFilter === 'warning' && currentStatus !== 'partial' && currentStatus !== 'missed') return false;
      if (scheduleFilter !== 'all' && task.schedule.type !== scheduleFilter) return false;
      if (scopeFilter !== 'all' && task.targetScope.type !== scopeFilter) return false;
      return true;
    });
  }, [keywordFilter, runningTaskIds, scheduleFilter, scopeFilter, statusFilter, tasks]);

  const {
    currentItems: pagedTasks,
    currentPage: taskPage,
    totalPages: taskTotalPages,
    pageSize: taskPageSize,
    goToPage: goToTaskPage,
    setPageSize: setTaskPageSize,
  } = usePagination(filteredTasks, 10);

  const channelLabel = (channel: CodexInspectionNotificationChannel) => {
    switch (channel) {
      case 'telegram':
        return 'Telegram';
      case 'feishu':
        return '飞书';
      case 'wecom':
        return '企业微信';
      case 'webhook':
        return 'Webhook';
      default:
        return channel;
    }
  };

  const stats = useMemo(() => {
    const enabledChannels = new Set<CodexInspectionNotificationChannel>();
    tasks.forEach((task) => {
      if (!task.notification.enabled) return;
      task.notification.channels.forEach((channel) => enabledChannels.add(channel));
    });
    const lastTriggerMs = tasks.reduce(
      (max, task) => Math.max(max, task.lastRunAtMs ?? 0),
      0
    );
    const highRiskCount = runs.reduce(
      (total, run) =>
        total + summaryNumber(run, 'invalid') + summaryNumber(run, 'probeFailed'),
      0
    );
    return {
      total: tasks.length,
      enabled: tasks.filter((task) => task.enabled).length,
      running: tasks.filter((task) => task.status === 'running').length + runningTaskIds.size,
      needAttention: runs.reduce(
        (total, run) =>
          total +
          summaryNumber(run, 'zeroQuota') +
          summaryNumber(run, 'fullQuota') +
          summaryNumber(run, 'invalid') +
          summaryNumber(run, 'probeFailed'),
        0
      ),
      highRiskCount,
      lastTriggerMs,
      notificationChannelList: Array.from(enabledChannels),
      notificationChannels: enabledChannels.size,
    };
  }, [runningTaskIds, runs, tasks]);

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
    if (mockModeEnabled) {
      setTaskModalOpen(false);
      showNotification('Mock 模式下不会写入后端，当前操作仅用于界面验证', 'info');
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
    if (mockModeEnabled) {
      setTasks((previous) =>
        previous.map((item) =>
          item.id === task.id
            ? {
                ...item,
                enabled,
                updatedAtMs: Date.now(),
              }
            : item
        )
      );
      showNotification(enabled ? '任务已启用' : '任务已停用', 'success');
      return;
    }
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
    if (mockModeEnabled) {
      const detail = findMockCodexInspectionRunDetail(task.lastRunId || '', mockScenario);
      if (!detail) {
        showNotification('Mock 模式下该任务暂无可查看的执行日志', 'warning');
        return;
      }
      showNotification('Mock 模式：已打开最近一次执行日志', 'info');
      navigate({
        pathname: `/monitoring/codex-inspection-tasks/runs/${encodeURIComponent(detail.run.id)}`,
        search: mockSearch,
      });
      return;
    }
    setRunningTaskIds((previous) => new Set(previous).add(task.id));
    try {
      const detail = await usageServiceApi.runCodexInspectionTask(serviceBase, task.id, {}, managementKey);
      await loadData();
      showNotification('巡检执行完成', detail.run.status === 'success' ? 'success' : 'warning');
      if (detail.run?.id) {
        navigate({
          pathname: `/monitoring/codex-inspection-tasks/runs/${encodeURIComponent(detail.run.id)}`,
          search: mockSearch,
        });
      }
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
        if (mockModeEnabled) {
          setTasks((previous) => previous.filter((item) => item.id !== task.id));
          setRuns((previous) => previous.filter((item) => item.taskId !== task.id));
          if (selectedTaskId === task.id) {
            setSelectedTaskId('');
            setRecentRuns([]);
            setRecentRunsTotal(0);
          }
          showNotification('巡检任务已删除', 'success');
          return;
        }
        await usageServiceApi.deleteCodexInspectionTask(serviceBase, task.id, managementKey);
        if (selectedTaskId === task.id) setSelectedTaskId('');
        await loadData();
        showNotification('巡检任务已删除', 'success');
      },
    });
  };

  const openRunDetail = (run: CodexInspectionRun) => {
    navigate({
      pathname: `/monitoring/codex-inspection-tasks/runs/${encodeURIComponent(run.id)}`,
      search: mockSearch,
    });
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

  if (taskModalOpen) {
    return (
      <TaskWizard
        mode={modalMode}
        draft={draft}
        wizardStep={wizardStep}
        saving={saving}
        onDraftChange={updateDraft}
        onToggleChannel={toggleNotificationChannel}
        onStepChange={setWizardStep}
        onSave={saveTask}
      />
    );
  }

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1>Codex 巡检任务</h1>
          <p>按计划自动巡检 Codex 账号状态，自动处理异常并通过多渠道通知，同时保留审计日志以便追溯。</p>
        </div>
        <div className={styles.headerActions}>
          <Button variant="secondary" onClick={loadData} loading={loading}>
            <IconRefreshCw size={16} />
            刷新
          </Button>
          <Link to="/monitoring/codex-inspection" className={styles.secondaryLink}>
            <IconChartLine size={16} />
            <span>查看旧版手动巡检</span>
          </Link>
          <Button onClick={openCreateModal}>
            <IconPlus size={16} />
            新建任务
          </Button>
        </div>
      </header>

      {mockModeEnabled ? (
        <div className={styles.mockTip}>
          <IconLightbulb size={14} />
          <span>Mock 数据模式已启用：当前页面使用本地巡检任务假数据，不会向 Usage Service 发起真实请求。</span>
        </div>
      ) : null}

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
        <MetricCard
          icon={<IconFileText size={22} />}
          label="启用任务"
          value={String(stats.enabled)}
          meta={`共 ${stats.total} 个任务`}
          tone="info"
        />
        <MetricCard
          icon={<IconPlayCircle size={22} />}
          label="运行中任务"
          value={String(stats.running)}
          meta={
            schedulerStatus?.running
              ? `最近启动: ${formatTimeOfDay(stats.lastTriggerMs) || '--'}`
              : '调度器未启动'
          }
          tone="good"
        />
        <MetricCard
          icon={<IconAlertTriangle size={22} />}
          label="待处理账号"
          value={String(stats.needAttention)}
          meta={`高风险: ${stats.highRiskCount}`}
          tone="warn"
        />
        <MetricCard
          icon={<IconBell size={22} />}
          label="通知渠道"
          value={String(stats.notificationChannels)}
          meta={
            stats.notificationChannelList.length
              ? stats.notificationChannelList.map(channelLabel).join(' / ')
              : '尚未启用通知'
          }
          tone="purple"
        />
      </section>

      <section className={styles.workspace}>
        <Card className={styles.taskListPanel}>
          <div className={styles.panelHeader}>
            <div>
              <h2>任务列表</h2>
            </div>
          </div>
          <div className={styles.taskFilters}>
            <Input
              value={keywordFilter}
              onChange={(event) => setKeywordFilter(event.target.value)}
              placeholder="搜索任务名 / ID / 描述"
              rightElement={<IconSearch size={14} />}
              className={styles.filterInput}
            />
            <Select
              value={statusFilter}
              onChange={(value) => setStatusFilter(value as TaskStatusFilter)}
              triggerClassName={styles.filterSelectTrigger}
              options={[
                { value: 'all', label: '全部状态' },
                { value: 'enabled', label: '启用' },
                { value: 'disabled', label: '停用' },
                { value: 'running', label: '运行中' },
                { value: 'warning', label: '需处理' },
                { value: 'failed', label: '失败' },
              ]}
              ariaLabel="状态筛选"
            />
            <Select
              value={scheduleFilter}
              onChange={(value) => setScheduleFilter(value as ScheduleFilter)}
              triggerClassName={styles.filterSelectTrigger}
              options={[
                { value: 'all', label: '全部频率' },
                { value: 'manual', label: '手动' },
                { value: 'interval', label: '固定频率' },
                { value: 'daily_times', label: '指定时间' },
              ]}
              ariaLabel="频率筛选"
            />
            <Select
              value={scopeFilter}
              onChange={(value) => setScopeFilter(value as ScopeFilter)}
              triggerClassName={styles.filterSelectTrigger}
              options={[
                { value: 'all', label: '全部范围' },
                { value: 'all_codex', label: '全部账号' },
                { value: 'files', label: '认证文件' },
                { value: 'auth_indices', label: '指定账号' },
                { value: 'metadata_filter', label: '标签/关键字' },
              ]}
              ariaLabel="范围筛选"
            />
          </div>
          <div className={styles.taskList}>
            {pagedTasks.map((task) => {
              const lastRun = latestRunByTask.get(task.id);
              return (
                <div
                  key={task.id}
                  role="button"
                  tabIndex={0}
                  className={`${styles.taskRow} ${selectedTask?.id === task.id ? styles.taskRowActive : ''}`}
                  onClick={() => {
                    setSelectedTaskId(task.id);
                    setDetailTab('overview');
                    setMenuTaskId('');
                  }}
                  onKeyDown={(event) => {
                    if (event.key === 'Enter' || event.key === ' ') {
                      event.preventDefault();
                      setSelectedTaskId(task.id);
                      setDetailTab('overview');
                      setMenuTaskId('');
                    }
                  }}
                >
                  <div className={styles.taskCardMain}>
                    <div className={styles.taskTitleLine}>
                      <strong>{task.name}</strong>
                      <span className={`${styles.statusPill} ${task.enabled ? styles.pillGood : styles.pillMuted}`}>
                        {task.enabled ? '启用' : '停用'}
                      </span>
                      {task.dryRun ? <span className={`${styles.statusPill} ${styles.pillInfo}`}>干运行</span> : null}
                    </div>
                    <div className={styles.taskMetaRow}>
                      <div className={styles.taskMetaLine}>
                        <span>频率：{scheduleLabel(task.schedule)}</span>
                        <span>范围：{scopeLabel(task.targetScope)}</span>
                      </div>
                      <div className={styles.taskRunMeta}>
                        <div>
                          <span>上次运行</span>
                          <strong>{formatDateShort(task.lastRunAtMs)}</strong>
                        </div>
                        <div>
                          <span>下次运行</span>
                          <strong>{formatDateShort(task.nextRunAtMs)}</strong>
                        </div>
                      </div>
                    </div>
                    <TaskResultChips run={lastRun} />
                  </div>

                  <div
                    className={styles.taskRowActions}
                    ref={menuTaskId === task.id ? openMenuRef : undefined}
                  >
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={runningTaskIds.has(task.id)}
                      onClick={(event) => {
                        event.stopPropagation();
                        void runTask(task);
                      }}
                    >
                      立即运行
                    </Button>
                    <button
                      type="button"
                      className={styles.rowMenuButton}
                      onClick={(event) => {
                        event.stopPropagation();
                        setMenuTaskId((current) => (current === task.id ? '' : task.id));
                      }}
                    >
                      更多
                      <IconChevronDown size={12} />
                    </button>
                    {menuTaskId === task.id ? (
                      <span
                        className={styles.moreMenu}
                        onMouseDown={(event) => event.stopPropagation()}
                      >
                        <button type="button" onClick={(event) => { event.stopPropagation(); openEditModal(task); setMenuTaskId(''); }}>编辑任务</button>
                        <button type="button" onClick={(event) => { event.stopPropagation(); void setTaskEnabled(task, !task.enabled); setMenuTaskId(''); }}>
                          {task.enabled ? '停用任务' : '启用任务'}
                        </button>
                        <button type="button" onClick={(event) => { event.stopPropagation(); setDetailTab('logs'); setSelectedTaskId(task.id); setMenuTaskId(''); }}>查看日志</button>
                        <button type="button" className={styles.dangerMenuItem} onClick={(event) => { event.stopPropagation(); deleteTask(task); setMenuTaskId(''); }}>删除任务</button>
                      </span>
                    ) : null}
                  </div>
                </div>
              );
            })}
            {filteredTasks.length === 0 ? (
              <div className={styles.emptyState}>
                <IconFilter size={24} />
                <p>{tasks.length === 0 ? '还没有巡检任务。' : '没有匹配的巡检任务。'}</p>
                {tasks.length === 0 ? <Button size="sm" onClick={openCreateModal}>新建任务</Button> : null}
              </div>
            ) : null}
          </div>
          <PaginationBar
            total={filteredTasks.length}
            currentPage={taskPage}
            totalPages={taskTotalPages}
            pageSize={taskPageSize}
            onPageChange={goToTaskPage}
            onPageSizeChange={setTaskPageSize}
          />
        </Card>

        <aside className={styles.detailPanel}>
          {selectedTask ? (
            <>
              <div className={styles.detailHeader}>
                <div>
                  <div className={styles.detailTitleLine}>
                    <h2>{selectedTask.name}</h2>
                    <span className={`${styles.statusPill} ${selectedTask.enabled ? styles.pillGood : styles.pillMuted}`}>
                      {selectedTask.enabled ? '已启用' : '已停用'}
                    </span>
                  </div>
                  <p>{selectedTask.description || '未填写描述'}</p>
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

              <div className={styles.detailTabs}>
                {DETAIL_TABS.map((tab) => (
                  <button
                    key={tab.id}
                    type="button"
                    className={detailTab === tab.id ? styles.detailTabActive : ''}
                    onClick={() => setDetailTab(tab.id)}
                  >
                    {tab.label}
                  </button>
                ))}
              </div>

              {detailTab === 'overview' ? (
                <>
                  <div className={styles.detailOverviewGrid}>
                    <section className={styles.detailSubCard}>
                      <h3>任务信息</h3>
                      <div className={styles.detailInfoList}>
                        <InfoItem label="任务名称" value={selectedTask.name} />
                        <InfoItem
                          label="任务 ID"
                          value={selectedTask.id}
                          onCopy={() => void copyToClipboard(selectedTask.id, '任务 ID 已复制')}
                        />
                        <InfoItem label="状态" value={selectedTask.enabled ? '启用' : '停用'} />
                        <InfoItem label="频率" value={scheduleLabel(selectedTask.schedule)} />
                        <InfoItem
                          label="时区"
                          value={
                            selectedTask.schedule.type === 'interval' ||
                            selectedTask.schedule.type === 'daily_times'
                              ? selectedTask.schedule.timezone || '服务端默认'
                              : '服务端默认'
                          }
                        />
                        <InfoItem label="创建时间" value={formatDateShort(selectedTask.createdAtMs)} />
                        <InfoItem label="创建者" value={selectedTask.createdBy || 'system'} />
                        <InfoItem label="备注" value={selectedTask.note || selectedTask.description || '--'} />
                      </div>
                    </section>

                    <section className={styles.detailSubCard}>
                      <div className={styles.subCardHeader}>
                        <h3>最后一次巡检结果</h3>
                        <span>{selectedTaskLastRun ? formatDateShort(selectedTaskLastRun.startedAtMs) : '--'}</span>
                      </div>
                      <ResultDistribution run={selectedTaskLastRun} />
                    </section>

                    <section className={`${styles.detailSubCard} ${styles.riskPanel} ${isHighRiskPolicy ? styles.riskPanelHigh : ''}`}>
                      <div className={styles.riskPanelHeader}>
                        <h3>策略风险提醒</h3>
                        <span className={isHighRiskPolicy ? styles.riskHigh : styles.riskLow}>
                          {isHighRiskPolicy ? '高风险' : '低风险'}
                        </span>
                      </div>
                      <p>
                        {isHighRiskPolicy
                          ? '当前自动处理策略为 高风险，可能直接导致账号被禁用或丢失。'
                          : '当前策略安全，仅在 dry-run 下生成建议和审计记录。'}
                      </p>
                      <ul className={styles.riskActionList}>
                        <li>
                          <span>满额度账号</span>
                          <strong>{actionLabel(selectedTask.autoAction.fullQuotaAction)}</strong>
                        </li>
                        <li>
                          <span>零额度账号</span>
                          <strong>{actionLabel(selectedTask.autoAction.zeroQuotaAction)}</strong>
                        </li>
                        <li>
                          <span>失效账号</span>
                          <strong>{actionLabel(selectedTask.autoAction.invalidAction)}</strong>
                        </li>
                      </ul>
                      <button type="button" onClick={() => setDetailTab('policy')}>
                        查看自动处理策略
                      </button>
                    </section>
                  </div>

                  <div className={styles.detailRecentRuns}>
                    <div className={styles.subCardHeader}>
                      <h3>最近执行记录</h3>
                    </div>
                    <div className={styles.detailRunTable}>
                      <div className={styles.detailRunHeader}>
                        <span>执行时间</span>
                        <span>状态</span>
                        <span>巡检账号数</span>
                        <span>结果汇总（正常 / 满额度 / 零额度 / 失效 / 失败）</span>
                        <span>动作执行</span>
                        <span>耗时</span>
                        <span>操作</span>
                      </div>
                      {selectedTaskRuns.map((run) => (
                        <div key={run.id} className={styles.detailRunRow}>
                          <span>{formatDateShort(run.startedAtMs)}</span>
                          <span className={`${styles.runStatus} ${statusTone(run.status)}`}>{statusLabel(run.status)}</span>
                          <span>{summaryNumber(run, 'total')}</span>
                          <span>
                            {summaryNumber(run, 'healthy')} / {summaryNumber(run, 'fullQuota')} /{' '}
                            {summaryNumber(run, 'zeroQuota')} / {summaryNumber(run, 'invalid')} /{' '}
                            {summaryNumber(run, 'probeFailed')}
                          </span>
                          <span>
                            {summaryNumber(run, 'disableCount') +
                              summaryNumber(run, 'enableCount') +
                              summaryNumber(run, 'deleteCount')}
                          </span>
                          <span>{formatDurationClock(run.durationMs)}</span>
                          <span>
                            <Button size="sm" variant="secondary" onClick={() => openRunDetail(run)}>
                              查看详情
                            </Button>
                          </span>
                        </div>
                      ))}
                      {selectedTaskRuns.length === 0 ? <div className={styles.emptyRow}>暂无执行记录</div> : null}
                    </div>
                    <PaginationBar
                      total={recentRunsTotal}
                      currentPage={recentRunsPage}
                      totalPages={recentRunsTotalPages}
                      pageSize={recentRunsPageSize}
                      onPageChange={setRecentRunsPage}
                      onPageSizeChange={(size) => {
                        setRecentRunsPageSize(size);
                        setRecentRunsPage(1);
                      }}
                    />
                  </div>
                </>
              ) : null}

              {detailTab === 'schedule' ? (
                <div className={styles.detailGrid}>
                  <InfoItem label="执行方式" value={scheduleTypeLabel(selectedTask.schedule)} />
                  <InfoItem label="计划" value={scheduleLabel(selectedTask.schedule)} />
                  <InfoItem
                    label="时区"
                    value={
                      selectedTask.schedule.type === 'interval' || selectedTask.schedule.type === 'daily_times'
                        ? selectedTask.schedule.timezone || '服务端默认'
                        : '服务端默认'
                    }
                  />
                  <InfoItem label="失败重试" value={`${selectedTask.execution.retries} 次`} />
                  <InfoItem label="并发数" value={String(selectedTask.execution.concurrency)} />
                  <InfoItem label="超时时间" value={`${selectedTask.execution.timeoutMs} ms`} />
                </div>
              ) : null}

              {detailTab === 'scope' ? (
                <div className={styles.scopePreview}>
                  <InfoItem label="范围类型" value={scopeLabel(selectedTask.targetScope)} />
                  {selectedTask.targetScope.type === 'files' ? (
                    <pre>{selectedTask.targetScope.fileNames.join('\n') || '--'}</pre>
                  ) : null}
                  {selectedTask.targetScope.type === 'auth_indices' ? (
                    <pre>{selectedTask.targetScope.authIndices.join('\n') || '--'}</pre>
                  ) : null}
                  {selectedTask.targetScope.type === 'metadata_filter' ? (
                    <div className={styles.detailGrid}>
                      <InfoItem label="关键词" value={selectedTask.targetScope.query || '--'} />
                      <InfoItem label="备注包含" value={selectedTask.targetScope.noteIncludes || '--'} />
                    </div>
                  ) : null}
                  {selectedTask.targetScope.type === 'all_codex' ? (
                    <p className={styles.mutedText}>将巡检 auth pool 中所有 Codex 账号。</p>
                  ) : null}
                </div>
              ) : null}

              {detailTab === 'policy' ? (
                <>
                  <div className={styles.strategyGrid}>
                    <PolicyBadge label="零额度" value={actionLabel(selectedTask.autoAction.zeroQuotaAction)} />
                    <PolicyBadge label="满额度" value={actionLabel(selectedTask.autoAction.fullQuotaAction)} />
                    <PolicyBadge
                      label="失效账号"
                      value={actionLabel(selectedTask.autoAction.invalidAction)}
                      danger={selectedTask.autoAction.invalidAction === 'delete'}
                    />
                    <PolicyBadge
                      label="自动删除"
                      value={selectedTask.autoAction.allowDelete ? '允许' : '关闭'}
                      danger={selectedTask.autoAction.allowDelete}
                    />
                    <PolicyBadge label="删除预览" value={selectedTask.autoAction.requireDeletePreview ? '必须' : '关闭'} />
                    <PolicyBadge label="Dry-run" value={selectedTask.dryRun ? '开启' : '关闭'} />
                  </div>
                  {(selectedTask.autoAction.invalidAction === 'delete' || selectedTask.autoAction.allowDelete) ? (
                    <div className={styles.dangerNotice}>
                      <IconTrash2 size={18} />
                      <span>自动删除属于高风险操作，默认不会对 unknown、网络异常或巡检失败结果执行。</span>
                    </div>
                  ) : null}
                </>
              ) : null}

              {detailTab === 'notification' ? (
                <div className={styles.notificationPreview}>
                  <div className={styles.detailGrid}>
                    <InfoItem label="通知状态" value={selectedTask.notification.enabled ? '启用' : '停用'} />
                    <InfoItem label="触发条件" value={notificationTriggerLabel(selectedTask.notification.trigger)} />
                    <InfoItem label="渠道" value={selectedTask.notification.channels.join('、') || '--'} />
                    <InfoItem label="仅有操作/异常通知" value={selectedTask.notification.trigger === 'auto_action' || selectedTask.notification.trigger === 'abnormal' ? '是' : '否'} />
                  </div>
                  <div className={styles.channelPreviewGrid}>
                    {(['telegram', 'feishu', 'wecom', 'webhook'] as CodexInspectionNotificationChannel[]).map((channel) => (
                      <div key={channel} className={styles.channelPreviewCard}>
                        <strong>{channel}</strong>
                        <span>{selectedTask.notification.channels.includes(channel) ? '已选择' : '未选择'}</span>
                      </div>
                    ))}
                  </div>
                  <Button size="sm" variant="secondary" onClick={() => setNotificationModalOpen(true)}>
                    <IconSettings size={15} />
                    配置通知渠道
                  </Button>
                </div>
              ) : null}

              {detailTab === 'logs' ? (
                <div className={styles.miniRunTable}>
                  {selectedTaskRuns.map((run) => (
                    <button key={run.id} type="button" onClick={() => openRunDetail(run)}>
                      <span className={`${styles.runStatus} ${statusTone(run.status)}`}>{statusLabel(run.status)}</span>
                      <strong>{formatDateShort(run.startedAtMs)}</strong>
                      <small>账号 {summaryNumber(run, 'total')} / 耗时 {formatDurationClock(run.durationMs)}</small>
                    </button>
                  ))}
                  {selectedTaskRuns.length === 0 ? <div className={styles.emptyRow}>暂无执行日志</div> : null}
                </div>
              ) : null}
            </>
          ) : (
            <div className={styles.emptyState}>
              <IconEye size={24} />
              <p>选择一个任务查看详情。</p>
            </div>
          )}
        </aside>
      </section>

      <NotificationChannelModal
        open={notificationModalOpen}
        serviceBase={serviceBase}
        managementKey={managementKey}
        mockModeEnabled={mockModeEnabled}
        onClose={() => setNotificationModalOpen(false)}
        onNotify={showNotification}
      />
    </div>
  );
}

function MetricCard({
  icon,
  label,
  value,
  meta,
  tone,
}: {
  icon: ReactNode;
  label: string;
  value: string;
  meta: string;
  tone?: 'good' | 'info' | 'warn' | 'purple';
}) {
  return (
    <Card className={`${styles.metricCard} ${tone ? styles[`metric-${tone}`] : ''}`}>
      <div className={styles.metricIcon}>{icon}</div>
      <div>
        <span>{label}</span>
        <strong>{value}</strong>
        <small>{meta}</small>
      </div>
    </Card>
  );
}

function TaskResultChips({ run }: { run: CodexInspectionRun | null | undefined }) {
  if (!run) {
    return (
      <div className={styles.resultChips}>
        <span className={`${styles.resultChip} ${styles['result-muted']}`}>暂无结果</span>
      </div>
    );
  }
  return (
    <div className={styles.resultChips}>
      {taskResultItems(run).map((item) => (
        <span key={item.label} className={`${styles.resultChip} ${styles[`result-${item.tone}`]}`}>
          {item.label} {item.value}
        </span>
      ))}
    </div>
  );
}

function ResultDistribution({ run }: { run: CodexInspectionRun | null | undefined }) {
  const total = summaryNumber(run, 'total');
  const healthy = summaryNumber(run, 'healthy');
  const fullQuota = summaryNumber(run, 'fullQuota');
  const zeroQuota = summaryNumber(run, 'zeroQuota');
  const invalid = summaryNumber(run, 'invalid');
  const failed = summaryNumber(run, 'probeFailed');
  const safeTotal = Math.max(total, healthy + fullQuota + zeroQuota + invalid + failed, 1);
  const healthyEnd = (healthy / safeTotal) * 100;
  const fullEnd = healthyEnd + (fullQuota / safeTotal) * 100;
  const zeroEnd = fullEnd + (zeroQuota / safeTotal) * 100;
  const invalidEnd = zeroEnd + (invalid / safeTotal) * 100;
  const donutStyle = {
    background: `conic-gradient(
      var(--task-green) 0 ${healthyEnd}%,
      var(--task-cyan) ${healthyEnd}% ${fullEnd}%,
      var(--task-amber) ${fullEnd}% ${zeroEnd}%,
      #a855f7 ${zeroEnd}% ${invalidEnd}%,
      var(--task-red) ${invalidEnd}% 100%
    )`,
  };
  const percent = (value: number) => {
    if (safeTotal <= 0) return '0.0%';
    return `${((value / safeTotal) * 100).toFixed(1)}%`;
  };

  return (
    <div className={styles.resultDistribution}>
      <div className={styles.resultDonut} style={donutStyle}>
        <div>
          <strong>{total}</strong>
          <span>账号总数</span>
        </div>
      </div>
      <div className={styles.resultLegend}>
        <span><i className={styles.legendGood} />正常 {healthy} ({percent(healthy)})</span>
        <span><i className={styles.legendInfo} />满额度 {fullQuota} ({percent(fullQuota)})</span>
        <span><i className={styles.legendWarn} />零额度 {zeroQuota} ({percent(zeroQuota)})</span>
        <span><i className={styles.legendPurple} />失效 {invalid} ({percent(invalid)})</span>
        <span><i className={styles.legendBad} />失败 {failed} ({percent(failed)})</span>
      </div>
    </div>
  );
}function InfoItem({
  label,
  value,
  onCopy,
}: {
  label: string;
  value: string;
  onCopy?: () => void;
}) {
  return (
    <div className={styles.infoItem}>
      <span>{label}</span>
      <div className={styles.infoItemValue}>
        <strong>{value}</strong>
        {onCopy ? (
          <button
            type="button"
            className={styles.copyButton}
            onClick={onCopy}
            title="复制"
            aria-label={`复制${label}`}
          >
            <IconCopy size={13} />
          </button>
        ) : null}
      </div>
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

function PaginationBar({
  total,
  currentPage,
  totalPages,
  pageSize,
  pageSizeOptions = [10, 20, 50],
  onPageChange,
  onPageSizeChange,
}: {
  total: number;
  currentPage: number;
  totalPages: number;
  pageSize: number;
  pageSizeOptions?: readonly number[];
  onPageChange: (page: number) => void;
  onPageSizeChange: (size: number) => void;
}) {
  if (total === 0) return null;
  const pages = Array.from({ length: totalPages }, (_, index) => index + 1);
  return (
    <div className={styles.paginationBar}>
      <span className={styles.paginationInfo}>共 {total} 条</span>
      <div className={styles.paginationPages}>
        <button
          type="button"
          className={styles.pageNavButton}
          disabled={currentPage <= 1}
          onClick={() => onPageChange(Math.max(1, currentPage - 1))}
          aria-label="上一页"
        >
          ‹
        </button>
        {pages.map((page) => (
          <button
            key={page}
            type="button"
            className={`${styles.pageNumber} ${page === currentPage ? styles.pageNumberActive : ''}`}
            onClick={() => onPageChange(page)}
          >
            {page}
          </button>
        ))}
        <button
          type="button"
          className={styles.pageNavButton}
          disabled={currentPage >= totalPages}
          onClick={() => onPageChange(Math.min(totalPages, currentPage + 1))}
          aria-label="下一页"
        >
          ›
        </button>
      </div>
      <div className={styles.pageSizeField}>
        <Select
          value={String(pageSize)}
          onChange={(value) => onPageSizeChange(Number(value))}
          options={pageSizeOptions.map((size) => ({ value: String(size), label: `${size} 条/页` }))}
          ariaLabel="每页条数"
          fullWidth={false}
        />
      </div>
    </div>
  );
}

function TaskWizard({
  mode,
  draft,
  wizardStep,
  saving,
  onDraftChange,
  onToggleChannel,
  onStepChange,
  onSave,
}: {
  mode: ModalMode;
  draft: TaskDraft;
  wizardStep: number;
  saving: boolean;
  onDraftChange: <K extends keyof TaskDraft>(key: K, value: TaskDraft[K]) => void;
  onToggleChannel: (channel: CodexInspectionNotificationChannel) => void;
  onStepChange: (step: number) => void;
  onSave: () => void;
}) {
  const isLastStep = wizardStep >= WIZARD_STEPS.length - 1;
  const safeStep = Math.min(Math.max(wizardStep, 0), WIZARD_STEPS.length - 1);
  const initializedCreateExecutionStepRef = useRef(false);

  useEffect(() => {
    if (mode !== 'create' || safeStep !== 2 || initializedCreateExecutionStepRef.current) return;
    initializedCreateExecutionStepRef.current = true;
    if (draft.scheduleType === 'manual') {
      onDraftChange('scheduleType', 'daily_times');
    }
  }, [draft.scheduleType, mode, onDraftChange, safeStep]);

  return (
    <div className={styles.wizardPage}>
      <header className={styles.wizardHeader}>
        <div>
          <h1>{mode === 'edit' ? '编辑任务' : '新建任务'}</h1>
          <p>{WIZARD_STEPS[safeStep].description}</p>
        </div>
      </header>

      <WizardStepper currentStep={safeStep} onStepChange={onStepChange} />

      <main className={styles.wizardContent}>
        {safeStep === 0 ? <BasicInfoStep draft={draft} onDraftChange={onDraftChange} /> : null}
        {safeStep === 1 ? <ScopeStep draft={draft} onDraftChange={onDraftChange} /> : null}
        {safeStep === 2 ? <ExecutionStep draft={draft} onDraftChange={onDraftChange} /> : null}
        {safeStep === 3 ? <AutomationStep draft={draft} onDraftChange={onDraftChange} /> : null}
        {safeStep === 4 ? (
          <NotificationLogStep
            draft={draft}
            onDraftChange={onDraftChange}
            onToggleChannel={onToggleChannel}
          />
        ) : null}
      </main>

      <footer className={styles.wizardFooterBar}>
        <div>
          {safeStep > 0 ? (
            <Button variant="secondary" onClick={() => onStepChange(safeStep - 1)} disabled={saving}>
              上一步
            </Button>
          ) : null}
        </div>
        <div className={styles.wizardFooterActions}>
          {isLastStep ? (
            <Button onClick={onSave} loading={saving}>
              {mode === 'edit' ? '保存任务' : '创建任务'}
            </Button>
          ) : (
            <Button onClick={() => onStepChange(safeStep + 1)} disabled={saving}>
              下一步
            </Button>
          )}
        </div>
      </footer>
    </div>
  );
}

type DraftChangeHandler = <K extends keyof TaskDraft>(key: K, value: TaskDraft[K]) => void;

function WizardStepper({
  currentStep,
  onStepChange,
}: {
  currentStep: number;
  onStepChange: (step: number) => void;
}) {
  return (
    <nav className={styles.fullWizardSteps} aria-label="任务创建步骤">
      {WIZARD_STEPS.map((step, index) => {
        const done = index < currentStep;
        const active = index === currentStep;
        return (
          <div key={step.label} className={styles.stepperSegment}>
            <button
              type="button"
              className={[
                styles.fullWizardStep,
                active ? styles.fullWizardStepActive : '',
                done ? styles.fullWizardStepDone : '',
              ]
                .filter(Boolean)
                .join(' ')}
              onClick={() => onStepChange(index)}
            >
              <span>{done ? <IconCheck size={18} /> : index + 1}</span>
              <strong>{step.label}</strong>
            </button>
            {index < WIZARD_STEPS.length - 1 ? (
              <i
                className={[
                  styles.stepperConnector,
                  index < currentStep ? styles.stepperConnectorActive : '',
                ]
                  .filter(Boolean)
                  .join(' ')}
              />
            ) : null}
          </div>
        );
      })}
    </nav>
  );
}

function WizardPanel({
  title,
  subtitle,
  eyebrow,
  children,
  side,
  layoutClassName,
}: {
  title: string;
  subtitle?: string;
  eyebrow?: ReactNode;
  children: ReactNode;
  side?: ReactNode;
  layoutClassName?: string;
}) {
  const hasSide = Boolean(side);

  return (
    <div className={[styles.wizardPanelGrid, hasSide ? '' : styles.wizardPanelGridFull, layoutClassName].filter(Boolean).join(' ')}>
      <section className={styles.wizardMainPanel}>
        <div className={styles.wizardPanelTitle}>
          {eyebrow}
          <h2>{title}</h2>
          {subtitle ? <p>{subtitle}</p> : null}
        </div>
        {children}
      </section>
      {hasSide ? <aside className={styles.wizardSidePanel}>{side}</aside> : null}
    </div>
  );
}

function WizardHelp({ title, items, footer }: { title: string; items: Array<{ icon: ReactNode; title: string; body: string }>; footer?: ReactNode }) {
  return (
    <>
      <div className={styles.helpTitle}>
        <IconFileText size={18} />
        <strong>{title}</strong>
      </div>
      <div className={styles.helpList}>
        {items.map((item) => (
          <div key={item.title} className={styles.helpItem}>
            <span className={styles.helpIcon}>{item.icon}</span>
            <div>
              <strong>{item.title}</strong>
              <p>{item.body}</p>
            </div>
          </div>
        ))}
      </div>
      {footer ? <div className={styles.helpFooter}>{footer}</div> : null}
    </>
  );
}

function CountedTextInput({
  label,
  value,
  maxLength,
  placeholder,
  hint,
  onChange,
}: {
  label: string;
  value: string;
  maxLength: number;
  placeholder?: string;
  hint?: string;
  onChange: (value: string) => void;
}) {
  return (
    <label className={styles.countedField}>
      <span>{label}</span>
      <div className={styles.countedInputShell}>
        <input
          value={value}
          maxLength={maxLength}
          onChange={(event) => onChange(event.target.value)}
          placeholder={placeholder}
        />
        <em>{value.length} / {maxLength}</em>
      </div>
      {hint ? <small>{hint}</small> : null}
    </label>
  );
}

function CountedTextarea({
  label,
  value,
  maxLength,
  placeholder,
  hint,
  onChange,
}: {
  label: string;
  value: string;
  maxLength: number;
  placeholder?: string;
  hint?: string;
  onChange: (value: string) => void;
}) {
  return (
    <label className={styles.countedField}>
      <span>{label}</span>
      <div className={styles.countedTextareaShell}>
        <textarea
          value={value}
          maxLength={maxLength}
          onChange={(event) => onChange(event.target.value)}
          placeholder={placeholder}
        />
        <em>{value.length} / {maxLength}</em>
      </div>
      {hint ? <small>{hint}</small> : null}
    </label>
  );
}

function BasicInfoStep({ draft, onDraftChange }: { draft: TaskDraft; onDraftChange: DraftChangeHandler }) {
  const addTag = () => {
    const nextTag = draft.tagInput.trim();
    if (!nextTag || draft.taskTags.includes(nextTag)) {
      onDraftChange('tagInput', '');
      return;
    }
    onDraftChange('taskTags', [...draft.taskTags, nextTag]);
    onDraftChange('tagInput', '');
  };

  const removeTag = (tag: string) => {
    onDraftChange(
      'taskTags',
      draft.taskTags.filter((item) => item !== tag)
    );
  };

  return (
    <WizardPanel
      title="基本信息"
      side={
        <WizardHelp
          title="填写说明"
          items={[
            {
              icon: <IconTimer size={22} />,
              title: '任务名称',
              body: '建议包含业务线、环境、区域或频率等关键信息，例如：生产环境-高风险巡检-每日。',
            },
            {
              icon: <IconDiamond size={22} />,
              title: '任务标签',
              body: '使用标签便于后续筛选与统计，例如：high-risk、security、auto-fix、定期巡检等。',
            },
            {
              icon: <IconShield size={22} />,
              title: '安全默认',
              body: '新建任务默认以模拟执行模式运行，确保诊断安全可靠。',
            },
            {
              icon: <IconBell size={22} />,
              title: '后续步骤',
              body: '完成基本信息后，您将配置巡检范围、执行计划、自动处理策略与通知方式。',
            },
          ]}
        />
      }
    >
      <div className={styles.basicForm}>
        <CountedTextInput
          label="任务名称 *"
          value={draft.name}
          maxLength={100}
          onChange={(value) => onDraftChange('name', value)}
          placeholder="请输入任务名称"
          hint="建议使用清晰、简洁的名称，便于识别与管理。"
        />
        <CountedTextarea
          label="任务描述"
          value={draft.description}
          maxLength={500}
          onChange={(value) => onDraftChange('description', value)}
          placeholder="请输入任务描述（可选）"
          hint="需要说明任务目的、范围、重点关注的安全项或配置要求。"
        />
        <div className={styles.tagField}>
          <span>任务标签</span>
          <div className={styles.tagInputBox}>
            <input
              value={draft.tagInput}
              onChange={(event) => onDraftChange('tagInput', event.target.value)}
              onKeyDown={(event) => {
                if (event.key !== 'Enter') return;
                event.preventDefault();
                addTag();
              }}
              placeholder="选择或输入标签，按回车添加"
            />
            <IconChevronDown size={16} />
          </div>
          <div className={styles.tagChipRow}>
            {draft.taskTags.map((tag) => (
              <button key={tag} type="button" className={styles.tagChip} onClick={() => removeTag(tag)}>
                {tag}
                <IconX size={13} />
              </button>
            ))}
            <button type="button" className={styles.addTagButton} onClick={addTag}>
              <IconPlus size={14} />
              添加标签
            </button>
          </div>
          <small>使用标签对任务进行分类与筛选，支持多选。</small>
        </div>
        <div className={styles.enabledLine}>
          <span>是否启用</span>
          <ToggleSwitch
            checked={draft.enabled}
            onChange={(value) => onDraftChange('enabled', value)}
            label="启用后将按照执行计划自动运行"
          />
        </div>
        <CountedTextarea
          label="任务备注（可选）"
          value={draft.note}
          maxLength={1000}
          onChange={(value) => onDraftChange('note', value)}
          placeholder="补充说明或备注信息（可选）"
          hint="可记录相关负责人、业务信息或其他补充说明，便于日后查询。"
        />
        <div className={styles.infoNotice}>
          <IconInfo size={16} />
          <span>提示：任务创建完成后，默认以「模拟执行（Dry-run）」模式运行，不会对您的资源进行任何变更。</span>
        </div>
      </div>
    </WizardPanel>
  );
}

function ScopeStep({ draft, onDraftChange }: { draft: TaskDraft; onDraftChange: DraftChangeHandler }) {
  const accountItems = splitList(draft.authIndices);
  const visibleAccounts = accountItems;

  return (
    <WizardPanel
      title="巡检范围"
      side={
        <WizardHelp
          title="选择范围提示"
          items={[
            {
              icon: <IconUsers size={23} />,
              title: '全部 Codex 账号',
              body: '对所有账号执行巡检，适用于全面排查或首次巡检。',
            },
            {
              icon: <IconDiamond size={23} />,
              title: '按标签筛选',
              body: '通过标签快速定位高风险或重点关注的账号，推荐使用。',
            },
            {
              icon: <IconUserCheck size={23} />,
              title: '指定账号',
              body: '针对特定账号进行精准巡检，适用于问题排查或专项检查。',
            },
          ]}
          footer={
            <>
              <IconInfo size={16} />
              <span>标签由系统或自定义标签提供，确保标签体系完善以提高筛选效率。</span>
            </>
          }
        />
      }
    >
      <div className={styles.scopeWizard}>
        <span className={styles.sectionCaption}>选择范围模式</span>
        <div className={styles.scopeModeGrid}>
          <ChoiceCard
            checked={draft.targetType === 'all_codex'}
            icon={<IconUsers size={30} />}
            title="全部 Codex 账号"
            body="对所有 Codex 账号执行巡检"
            onClick={() => onDraftChange('targetType', 'all_codex')}
          />
          <ChoiceCard
            checked={draft.targetType === 'metadata_filter'}
            icon={<IconDiamond size={30} />}
            title="按标签筛选"
            body="按标签筛选符合条件的账号"
            onClick={() => onDraftChange('targetType', 'metadata_filter')}
          />
          <ChoiceCard
            checked={draft.targetType === 'auth_indices'}
            icon={<IconUserCheck size={30} />}
            title="指定账号"
            body="手动选择需要巡检的账号"
            onClick={() => onDraftChange('targetType', 'auth_indices')}
          />
          <ChoiceCard
            checked={draft.targetType === 'files'}
            icon={<IconFileText size={30} />}
            title="指定认证文件"
            body="按认证文件名限定巡检范围"
            onClick={() => onDraftChange('targetType', 'files')}
          />
        </div>

        <div className={styles.scopeColumns}>
          <section>
            <span className={styles.sectionCaption}>标签筛选（多选）</span>
            <div className={styles.tagInputBox}>
              {draft.taskTags.slice(0, 3).map((tag) => (
                <span key={tag} className={styles.staticTagChip}>
                  {tag}
                  <IconX size={13} />
                </span>
              ))}
              <input
                value={draft.query}
                onChange={(event) => onDraftChange('query', event.target.value)}
                onFocus={() => onDraftChange('targetType', 'metadata_filter')}
                placeholder="选择或输入标签"
              />
              <IconChevronDown size={16} />
            </div>
            <p className={styles.fieldHint}>选择一个或多个标签，匹配任一标签的账号将被纳入巡检范围。</p>
            <div className={styles.matchEstimate}>
              <IconUsers size={30} />
              <div>
                <span>预计匹配结果</span>
                <strong>45 <small>个账号</small><b>已匹配</b></strong>
                <p>基于当前筛选条件的预估匹配数量，实际结果以执行时为准。</p>
              </div>
            </div>
          </section>
          <section>
            <span className={styles.sectionCaption}>选择账号（多选）</span>
            <div className={styles.accountSelectBox}>
              <div className={styles.accountSearchLine}>
                <IconSearch size={16} />
                <span>搜索账号名称 / 邮箱 / ID</span>
              </div>
              <div className={styles.accountChips}>
                {visibleAccounts.map((account) => (
                  <span key={account}>
                    {account}
                    <IconX size={13} />
                  </span>
                ))}
              </div>
            </div>
            <div className={styles.selectionFooter}>
              <span>已选择 {accountItems.length} 个账号</span>
              <button type="button" onClick={() => onDraftChange('authIndices', '')}>
                清空全部
              </button>
            </div>
          </section>
        </div>

        {draft.targetType === 'files' ? (
          <label className={styles.wizardFieldWide}>
            <span>认证文件名</span>
            <textarea
              value={draft.fileNames}
              onChange={(event) => onDraftChange('fileNames', event.target.value)}
              placeholder="每行一个文件名，或用逗号分隔"
            />
            <small>指定一个或多个认证文件，仅巡检这些文件中的 Codex 账号。</small>
          </label>
        ) : null}

        <section className={styles.optionalFilters}>
          <span className={styles.sectionCaption}>可选筛选条件（可选）</span>
          <div className={styles.optionalFilterGrid}>
            <label className={styles.wizardField}>
              <span>禁用状态</span>
              <Select
                value="all"
                onChange={() => undefined}
                options={[{ value: 'all', label: '全部状态' }]}
                ariaLabel="禁用状态"
              />
              <small>筛选是否禁用的账号</small>
            </label>
            <label className={styles.wizardField}>
              <span>提供商</span>
              <Select
                value="all"
                onChange={() => undefined}
                options={[{ value: 'all', label: '全部提供商' }]}
                ariaLabel="提供商"
              />
              <small>筛选账号所属的 AI 提供商</small>
            </label>
            <Input
              label="关键词搜索"
              value={draft.noteIncludes}
              onChange={(event) => {
                onDraftChange('noteIncludes', event.target.value);
                onDraftChange('targetType', 'metadata_filter');
              }}
              placeholder="搜索账号名称 / 邮箱 / ID"
              hint="支持模糊搜索账号名称、邮箱或 ID"
            />
          </div>
        </section>
      </div>
    </WizardPanel>
  );
}

function ExecutionStep({ draft, onDraftChange }: { draft: TaskDraft; onDraftChange: DraftChangeHandler }) {
  const dailyTimes = splitList(draft.dailyTimes);
  const timezoneOptions = Array.from(new Set(['Asia/Shanghai', 'UTC', 'America/Los_Angeles', 'Europe/London', draft.timezone || 'Asia/Shanghai']));
  const removeDailyTime = (targetIndex: number) => {
    onDraftChange('dailyTimes', dailyTimes.filter((_, index) => index !== targetIndex).join(','));
  };

  return (
    <>
      <WizardPanel
        title="执行计划"
        subtitle="配置任务的执行时间、频率与并发控制参数。"
        eyebrow={<span className={styles.stepNumberPill}>3</span>}
        side={null}
      >
        <div className={styles.executionGrid}>
          <section className={styles.executionLeft}>
            <span className={styles.sectionCaption}>调度方式</span>
            <div className={styles.scheduleChoiceRow}>
              <RadioChoice
                checked={draft.scheduleType === 'manual'}
                label="手动执行"
                onClick={() => onDraftChange('scheduleType', 'manual')}
              />
              <RadioChoice
                checked={draft.scheduleType === 'interval'}
                label="固定时间间隔"
                onClick={() => onDraftChange('scheduleType', 'interval')}
              />
              <RadioChoice
                checked={draft.scheduleType === 'daily_times'}
                label="多个每日执行时间点"
                onClick={() => onDraftChange('scheduleType', 'daily_times')}
              />
              <RadioChoice checked={false} label="Cron 表达式" disabled onClick={() => undefined} />
            </div>
            {draft.scheduleType === 'daily_times' ? (
              <div className={styles.timePointGroup}>
                <span className={styles.sectionCaption}>每日执行时间点</span>
                <div className={styles.timeChips}>
                  {dailyTimes.map((time, index) => (
                    <span key={`${time}-${index}`} className={styles.timeChip}>
                      {time}
                      <button
                        type="button"
                        className={styles.timeChipRemove}
                        onClick={() => removeDailyTime(index)}
                        aria-label={`删除时间点 ${time}`}
                      >
                        <IconX size={13} />
                      </button>
                    </span>
                  ))}
                  <button
                    type="button"
                    className={styles.timeChipAdd}
                    onClick={() => onDraftChange('dailyTimes', `${draft.dailyTimes},09:00`)}
                  >
                    <IconPlus size={16} />
                    添加时间点
                  </button>
                </div>
                <p className={styles.fieldHint}>系统会在所选时区，按设置的时间点分别触发任务执行。</p>
              </div>
            ) : null}
            {draft.scheduleType === 'interval' ? (
              <div className={styles.intervalGrid}>
                <Input
                  label="间隔数值"
                  type="number"
                  min={1}
                  value={draft.intervalEvery}
                  onChange={(event) => onDraftChange('intervalEvery', event.target.value)}
                />
                <label className={styles.wizardField}>
                  <span>间隔单位</span>
                  <Select
                    value={draft.intervalUnit}
                    onChange={(value) => onDraftChange('intervalUnit', value as TaskDraft['intervalUnit'])}
                    options={[
                      { value: 'minute', label: '分钟' },
                      { value: 'hour', label: '小时' },
                      { value: 'day', label: '天' },
                    ]}
                    ariaLabel="间隔单位"
                  />
                </label>
              </div>
            ) : null}
            {draft.scheduleType === 'manual' ? (
              <p className={styles.fieldHint}>手动执行不会参与自动调度，仅当您手动触发时执行一次。</p>
            ) : null}
          </section>

          <section className={styles.executionRight}>
            <span className={styles.sectionCaption}>执行设置</span>
            <div className={styles.executionSettingsGrid}>
              <label className={styles.wizardField}>
                <span>时区</span>
                <Select
                  value={draft.timezone || 'Asia/Shanghai'}
                  onChange={(value) => onDraftChange('timezone', value)}
                  options={timezoneOptions.map((timezone) => ({ value: timezone, label: `(UTC+08:00) ${timezone}` }))}
                  ariaLabel="时区"
                />
              </label>
              <Input
                label="并发数（同时运行实例数）"
                type="number"
                min={1}
                max={20}
                value={draft.concurrency}
                onChange={(event) => onDraftChange('concurrency', event.target.value)}
                hint="建议 1-5"
              />
              <Input
                label="任务超时时间（秒）"
                type="number"
                min={1}
                value={draft.timeoutMs}
                onChange={(event) => onDraftChange('timeoutMs', event.target.value)}
                hint="任务超过该时间将被强制终止"
              />
              <Input
                label="重试次数"
                type="number"
                min={0}
                value={draft.retries}
                onChange={(event) => onDraftChange('retries', event.target.value)}
                hint="任务失败后的重试次数"
              />
            </div>
            <label className={styles.checkboxLine}>
              <input
                type="checkbox"
                checked={draft.avoidDuplicateRuns}
                onChange={(event) => onDraftChange('avoidDuplicateRuns', event.target.checked)}
              />
              <span>避免重复并发执行</span>
            </label>
            <p className={styles.fieldHint}>若上次任务未完成，则跳过本次计划执行，避免重复与冲突。</p>
          </section>
        </div>
      </WizardPanel>
      <section className={styles.executionTipsSection}>
        <div className={styles.executionTipsHeader}>
          <span>
            <IconLightbulb size={20} />
          </span>
          <strong>执行提示</strong>
        </div>
        <div className={styles.executionTips}>
          {[
            { icon: <IconHand size={25} />, title: '手动执行不会参与自动调度', body: '仅当手动触发时执行一次，不会影响自动计划任务。' },
            { icon: <IconTimer size={25} />, title: '多个时间点模式适合固定巡检时段', body: '可配置多个每日时间点，适合早/中/晚等固定时段巡检。' },
            { icon: <IconUsers size={25} />, title: '并发建议 1-5', body: '根据任务耗时与系统负载合理设置，过高可能造成资源竞争。' },
            { icon: <IconShield size={25} />, title: '开启避免重复并发可防止任务重叠', body: '当前任务未完成时将跳过下一次执行，保障任务稳定性。' },
          ].map((item) => (
            <div key={item.title} className={styles.executionTipCard}>
              <span>
                {item.icon}
              </span>
              <div>
                <strong>{item.title}</strong>
                <p>{item.body}</p>
              </div>
            </div>
          ))}
        </div>
      </section>
    </>
  );
}

function AutomationStep({ draft, onDraftChange }: { draft: TaskDraft; onDraftChange: DraftChangeHandler }) {
  return (
    <WizardPanel
      title="自动化策略（异常处理）"
      side={
        <section className={styles.riskSettingsPanel}>
          <div className={styles.riskSettingsTitle}>
            <IconShield size={22} />
            <strong>危险设置（默认安全保护）</strong>
          </div>
          <ToggleRow
            title="自动删除失效账号（默认关闭）"
            body="启用后，系统将自动删除被判定为失效的账号。"
            checked={draft.allowDelete}
            onChange={(value) => onDraftChange('allowDelete', value)}
          />
          <ToggleRow
            title="仅连续两次判定失效后才允许删除"
            body="账号需在连续两次巡检中均被判定为失效，才允许删除。"
            checked={draft.requireDeletePreview}
            onChange={(value) => onDraftChange('requireDeletePreview', value)}
          />
          <ToggleRow
            title="未知状态不自动处理"
            body="未知状态的账号将仅记录，不执行禁用/删除等操作。"
            checked
            disabled
            onChange={() => undefined}
          />
          <ToggleRow
            title="网络异常不自动处理"
            body="网络异常时不执行任何自动处理操作。"
            checked
            disabled
            onChange={() => undefined}
          />
          <div className={styles.warningNotice}>
            <IconAlertTriangle size={20} />
            <span>删除操作属于高风险行为，建议开启干跑验证并进行二次确认。实际删除前，系统将在日志中记录并提供待删除列表预览。</span>
          </div>
        </section>
      }
    >
      <div className={styles.autoStrategy}>
        <div className={styles.dryRunBanner}>
          <IconShield size={20} />
          <span>开启干跑模式（dry-run）为默认安全选项，系统将模拟执行所有操作，不会对账号产生任何实际变更。</span>
          <ToggleSwitch checked={draft.dryRun} onChange={(value) => onDraftChange('dryRun', value)} ariaLabel="Dry-run 模式" />
        </div>
        <p className={styles.fieldHint}>当巡检发现对应异常状态时，执行以下处理动作：</p>
        <div className={styles.policyRows}>
          <PolicyActionRow
            tone="warning"
            title="满额度账号"
            body="账号达到最大发电额度或使用额度上限。"
            value={draft.fullQuotaAction}
            onChange={(value) => onDraftChange('fullQuotaAction', value as TaskDraft['fullQuotaAction'])}
            options={[
              { value: 'none', label: '仅记录（dry-run）' },
              { value: 'disable', label: '自动禁用' },
              { value: 'enable', label: '自动启用' },
            ]}
          />
          <PolicyActionRow
            tone="info"
            title="零额度账号"
            body="账号可用额度为 0 或无法正常分配额度。"
            value={draft.zeroQuotaAction}
            onChange={(value) => onDraftChange('zeroQuotaAction', value as TaskDraft['zeroQuotaAction'])}
            options={[
              { value: 'none', label: '不处理' },
              { value: 'disable', label: '自动禁用' },
              { value: 'enable', label: '自动启用' },
            ]}
          />
          <PolicyActionRow
            tone="danger"
            title="失效账号"
            body="账号已失效、被封禁或认证失败。"
            value={draft.invalidAction}
            onChange={(value) => onDraftChange('invalidAction', value as CodexInspectionAutoAction)}
            options={[
              { value: 'none', label: '不处理' },
              { value: 'disable', label: '自动禁用' },
              { value: 'enable', label: '自动启用' },
              { value: 'delete', label: '自动删除' },
            ]}
          />
          <PolicyActionRow tone="neutral" title="未知状态" body="账号状态无法判断或返回结果不明确。" value="none" disabled />
          <PolicyActionRow tone="network" title="网络异常" body="请求超时、网络错误或服务不可达。" value="none" disabled />
        </div>
        <p className={styles.auditHint}>所有操作执行前将记录到巡检日志，便于审计与回溯。</p>
      </div>
    </WizardPanel>
  );
}

function NotificationLogStep({
  draft,
  onDraftChange,
  onToggleChannel,
}: {
  draft: TaskDraft;
  onDraftChange: DraftChangeHandler;
  onToggleChannel: (channel: CodexInspectionNotificationChannel) => void;
}) {
  const toggleTrigger = (trigger: NotificationTriggerOption) => {
    const exists = draft.notificationTriggers.includes(trigger);
    const next = exists
      ? draft.notificationTriggers.filter((item) => item !== trigger)
      : [...draft.notificationTriggers, trigger];
    onDraftChange('notificationEnabled', true);
    onDraftChange('notificationTriggers', next);
    onDraftChange('notificationTrigger', backendNotificationTrigger(next, draft.notificationTrigger));
  };

  const channels = notificationChannelViews(draft);

  return (
    <WizardPanel
      title="通知与日志"
      layoutClassName={styles.notificationWizardPanel}
      side={<SummaryPanel draft={draft} />}
    >
      <div className={styles.notificationLogGrid}>
        <section className={styles.notificationBlock}>
          <h3>通知渠道</h3>
          <div className={styles.channelRows}>
            {channels.map((channel) => (
              <div key={channel.id} className={styles.channelRow}>
                <span className={`${styles.channelIcon} ${styles[`channelIcon-${channel.id}`]}`}>{channel.icon}</span>
                <strong>{channel.label}</strong>
                <span className={channel.configured ? styles.configuredBadge : styles.unconfiguredBadge}>
                  {channel.configured ? '已配置' : '未配置'}
                </span>
                <small>{channel.detail}</small>
                <Button size="sm" variant="secondary" disabled={!channel.configured}>
                  测试
                </Button>
                <ToggleSwitch
                  checked={channel.enabled}
                  disabled={!channel.configured}
                  onChange={() => {
                    if (!channel.configured) return;
                    if (!draft.notificationEnabled) {
                      onDraftChange('notificationEnabled', true);
                      if (!draft.notificationChannels.includes(channel.id)) {
                        onToggleChannel(channel.id);
                      }
                      return;
                    }
                    onToggleChannel(channel.id);
                  }}
                  ariaLabel={`${channel.label} 通知`}
                />
              </div>
            ))}
          </div>
        </section>

        <section className={styles.notificationBlock}>
          <h3>通知触发条件</h3>
          <div className={styles.triggerChecks}>
            {[
              { value: 'always', label: '每次巡检都通知', body: '无论是否有异常，巡检完成后都会发送通知' },
              { value: 'abnormal', label: '仅异常通知', body: '仅当发现异常账号或异常状态时发送通知' },
              { value: 'auto_action', label: '仅有自动操作时通知', body: '仅当执行了自动处理操作时发送通知' },
              { value: 'manual_required', label: '仅有需要人工处理的账号时通知', body: '仅当存在需人工介入账号时通知' },
              { value: 'action_or_abnormal', label: '仅在有操作或异常时通知', body: '当存在异常或执行了任一操作（自动或人工）时发送通知' },
            ].map((item) => (
              <label key={item.value} className={styles.notificationCheckLine}>
                <input
                  type="checkbox"
                  checked={draft.notificationTriggers.includes(item.value as NotificationTriggerOption)}
                  onChange={() => toggleTrigger(item.value as NotificationTriggerOption)}
                />
                <span>{item.label}</span>
                <small>{item.body}</small>
              </label>
            ))}
          </div>
        </section>

        <section className={styles.logSettingsBlock}>
          <h3>日志设置</h3>
          <div className={styles.logSettingsSplit}>
            <div className={styles.logToggleList}>
              <div className={styles.logToggleRow}>
                <ToggleSwitch checked={draft.saveLogs} onChange={(value) => onDraftChange('saveLogs', value)} ariaLabel="保存执行日志" />
                <div className={styles.logToggleText}>
                  <strong>保存执行日志</strong>
                  <p>保存每次巡检的执行日志和结果摘要</p>
                </div>
              </div>
              <div className={styles.logToggleRow}>
                <ToggleSwitch checked={draft.saveAccountDetails} onChange={(value) => onDraftChange('saveAccountDetails', value)} ariaLabel="保存账号明细" />
                <div className={styles.logToggleText}>
                  <strong>保存账号明细</strong>
                  <p>保存影响账号的明细数据，便于追溯与分析</p>
                </div>
              </div>
            </div>
            <span className={styles.logSettingsDivider} aria-hidden="true" />
            <div className={styles.logRetentionGrid}>
              <Input
                label="日志保留天数"
                type="number"
                min={1}
                className={styles.inputWithSuffix}
                rightElement={<span className={styles.inputSuffix}>天</span>}
                value={draft.retentionDays}
                onChange={(event) => {
                  onDraftChange('retentionMode', 'days');
                  onDraftChange('retentionDays', event.target.value);
                }}
              />
              <Input
                label="保留最近 N 条"
                type="number"
                min={1}
                className={styles.inputWithSuffix}
                rightElement={<span className={styles.inputSuffix}>条</span>}
                value={draft.retentionCount}
                onChange={(event) => onDraftChange('retentionCount', event.target.value)}
              />
              <label className={styles.wizardField}>
                <span>自动清理周期</span>
                <Select value="daily" onChange={() => undefined} options={[{ value: 'daily', label: '每天' }]} ariaLabel="自动清理周期" />
              </label>
            </div>
          </div>
          <p className={styles.fieldHint}>系统将根据所选策略自动清理过期日志，确保储存空间可控。</p>
        </section>
      </div>
    </WizardPanel>
  );
}

function ChoiceCard({
  checked,
  icon,
  title,
  body,
  onClick,
}: {
  checked: boolean;
  icon: ReactNode;
  title: string;
  body: string;
  onClick: () => void;
}) {
  return (
    <button type="button" className={`${styles.choiceCard} ${checked ? styles.choiceCardActive : ''}`} onClick={onClick}>
      <span className={styles.choiceRadio}>{checked ? <i /> : null}</span>
      <span className={styles.choiceIcon}>{icon}</span>
      <strong>{title}</strong>
      <small>{body}</small>
    </button>
  );
}

function RadioChoice({
  checked,
  label,
  disabled,
  onClick,
}: {
  checked: boolean;
  label: string;
  disabled?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      disabled={disabled}
      className={`${styles.radioChoice} ${checked ? styles.radioChoiceActive : ''}`}
      onClick={onClick}
    >
      <span>{checked ? <i /> : null}</span>
      {label}
    </button>
  );
}

function PolicyActionRow({
  tone,
  title,
  body,
  value,
  options,
  disabled,
  onChange,
}: {
  tone: 'warning' | 'info' | 'danger' | 'neutral' | 'network';
  title: string;
  body: string;
  value: string;
  options?: Array<{ value: string; label: string }>;
  disabled?: boolean;
  onChange?: (value: string) => void;
}) {
  const icon =
    tone === 'warning' ? (
      <IconCreditCard size={20} />
    ) : tone === 'info' ? (
      <IconCreditCard size={20} />
    ) : tone === 'danger' ? (
      <IconX size={20} />
    ) : tone === 'neutral' ? (
      <IconCircleHelp size={20} />
    ) : (
      <IconWifi size={20} />
    );

  return (
    <div className={styles.policyActionRow}>
      <span className={`${styles.policyIcon} ${styles[`policyIcon-${tone}`]}`}>
        {icon}
      </span>
      <div>
        <strong>{title}</strong>
        <p>{body}</p>
      </div>
      <Select
        value={value}
        onChange={(nextValue) => onChange?.(nextValue)}
        disabled={disabled}
        options={options ?? [{ value: 'none', label: '不处理' }]}
        ariaLabel={`${title}处理策略`}
      />
    </div>
  );
}

function ToggleRow({
  title,
  body,
  checked,
  disabled,
  onChange,
}: {
  title: string;
  body: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (value: boolean) => void;
}) {
  return (
    <div className={styles.toggleRow}>
      <div>
        <strong>{title}</strong>
        <p>{body}</p>
      </div>
      <ToggleSwitch checked={checked} disabled={disabled} onChange={onChange} ariaLabel={title} />
    </div>
  );
}

function SummaryPanel({ draft }: { draft: TaskDraft }) {
  const channels = notificationChannelViews(draft);
  return (
    <section className={styles.summaryPanel}>
      <h2>任务配置汇总</h2>
      <div className={styles.summaryPanelCard}>
        <div className={styles.summaryList}>
          <SummaryItem icon={<IconFileText size={16} />} label="任务名称" value={draft.name || '高风险账号巡检任务'} />
          <SummaryItem icon={<IconTimer size={16} />} label="执行计划" value={draftScheduleSummary(draft)} />
          <SummaryItem icon={<IconUsers size={16} />} label="巡检范围" value={draftScopeSummary(draft)} />
          <SummaryItem icon={<IconShield size={16} />} label="执行模式" value={draft.taskTags.join(' + ') || 'security + high-risk + auto-fix'} />
          <SummaryItem icon={<IconShield size={16} />} label="自动处理策略" value={`启用（${draftPolicyCount(draft)} 条规则）`} />
          <SummaryItem
            icon={<IconBell size={16} />}
            label="通知渠道"
            value=""
          >
            <div className={styles.summaryChannelList}>
              {channels.map((channel) => (
                <span key={channel.id}>
                  <i className={`${styles.summaryChannelIcon} ${styles[`channelIcon-${channel.id}`]}`}>
                    {notificationChannelIcon(channel.id, 13)}
                  </i>
                  <strong>{channel.label}</strong>
                  <em className={channel.enabled ? styles.summaryEnabledBadge : styles.summaryDisabledBadge}>
                    {channel.enabled ? '已启用' : '未启用'}
                  </em>
                </span>
              ))}
            </div>
          </SummaryItem>
          <SummaryItem icon={<IconCheck size={16} />} label="通知触发条件" value={`${draft.notificationTriggers.length} 项条件`} />
          <SummaryItem icon={<IconFileText size={16} />} label="日志保留" value={draftRetentionSummary(draft)} />
        </div>
      </div>
    </section>
  );
}

function SummaryItem({
  icon,
  label,
  value,
  badge,
  children,
}: {
  icon: ReactNode;
  label: string;
  value: string;
  badge?: string;
  children?: ReactNode;
}) {
  return (
    <div className={styles.summaryItem}>
      <i>{icon}</i>
      <span>{label}</span>
      {children ?? <strong>{value}</strong>}
      {badge ? <em>{badge}</em> : null}
    </div>
  );
}

function channelDisplayLabel(channel: CodexInspectionNotificationChannel) {
  if (channel === 'telegram') return 'Telegram';
  if (channel === 'feishu') return '飞书';
  if (channel === 'wecom') return '企业微信';
  return '自定义 Webhook';
}

function draftScheduleSummary(draft: TaskDraft) {
  if (draft.scheduleType === 'interval') {
    const unit = draft.intervalUnit === 'day' ? '天' : draft.intervalUnit === 'hour' ? '小时' : '分钟';
    return `每 ${draft.intervalEvery || 1} ${unit}`;
  }
  if (draft.scheduleType === 'daily_times') {
    if (isPrototypeSummaryDraft(draft)) return PROTOTYPE_SCHEDULE_SUMMARY;
    return `每日 ${splitList(draft.dailyTimes)[0] ?? '09:00'}`;
  }
  return '手动执行';
}

function draftScopeSummary(draft: TaskDraft) {
  if (draft.targetType === 'all_codex') return '全部 Codex 账号';
  if (draft.targetType === 'auth_indices') return `指定账号（${splitList(draft.authIndices).length || 3} 个）`;
  if (draft.targetType === 'files') return `认证文件（${splitList(draft.fileNames).length} 个）`;
  if (draft.query.trim() === 'high-risk') return PROTOTYPE_SCOPE_SUMMARY;
  return draft.query.trim() ? `标签筛选：${draft.query.trim()}` : '标签筛选';
}

function draftPolicyCount(draft: TaskDraft) {
  return [draft.zeroQuotaAction, draft.fullQuotaAction, draft.invalidAction].filter((action) => action !== 'none').length;
}

function draftRetentionSummary(draft: TaskDraft) {
  if (draft.retentionMode === 'days') return `保留 ${draft.retentionDays || 90} 天 / 最近 ${draft.retentionCount || 10000} 条\n自动清理：每天`;
  if (draft.retentionMode === 'latest') return `最近 ${draft.retentionCount || 10000} 条`;
  return '不自动清理';
}

function NotificationChannelModal({
  open,
  serviceBase,
  managementKey,
  mockModeEnabled,
  onClose,
  onNotify,
}: {
  open: boolean;
  serviceBase: string;
  managementKey?: string;
  mockModeEnabled: boolean;
  onClose: () => void;
  onNotify: (message: string, type?: 'success' | 'error' | 'warning' | 'info') => void;
}) {
  const [channel, setChannel] = useState<CodexInspectionNotificationChannel>('telegram');
  const [enabled, setEnabled] = useState(true);
  const [botToken, setBotToken] = useState('');
  const [chatId, setChatId] = useState('');
  const [webhookUrl, setWebhookUrl] = useState('');
  const [secret, setSecret] = useState('');
  const [headers, setHeaders] = useState('Content-Type: application/json');
  const [template, setTemplate] = useState(
    'Codex 巡检任务：{{taskName}}\n状态：{{status}}\n账号总数：{{total}}\n日志 ID：{{logId}}'
  );
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState('');

  const channelConfig = useMemo(() => {
    if (channel === 'telegram') {
      return { botToken, chatId, template };
    }
    if (channel === 'feishu') {
      return { webhookUrl, secret, template };
    }
    if (channel === 'wecom') {
      return { webhookUrl, template };
    }
    return { url: webhookUrl, headers: parseHeaders(headers), template };
  }, [botToken, channel, chatId, headers, secret, template, webhookUrl]);

  const payload = useMemo(
    () => ({
      enabled,
      channels: enabled ? [channel] : [],
      trigger: 'always',
      channelConfigs: {
        [channel]: channelConfig,
      },
    }),
    [channel, channelConfig, enabled]
  );

  const previewText = `Codex 巡检任务：Codex 巡检通知测试
状态：success
账号总数：1
正常：1，零额度：0，满额度：0，失效：0，失败：0
日志 ID：test`;

  const testNotification = async () => {
    if (!serviceBase) {
      onNotify('Usage Service 未连接，无法测试通知', 'error');
      return;
    }
    if (mockModeEnabled) {
      const response = {
        ok: true,
        mock: true,
        channel,
        preview: previewText,
      };
      setTestResult(JSON.stringify(response, null, 2));
      onNotify('Mock 通知测试成功', 'success');
      return;
    }
    setTesting(true);
    setTestResult('');
    try {
      const response = await usageServiceApi.testCodexInspectionNotification(
        serviceBase,
        { notification: payload },
        managementKey
      );
      setTestResult(JSON.stringify(response, null, 2));
      onNotify(response.ok ? '测试通知发送成功' : '测试通知发送失败', response.ok ? 'success' : 'warning');
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setTestResult(message);
      onNotify(message, 'error');
    } finally {
      setTesting(false);
    }
  };

  return (
    <Modal open={open} title="通知渠道配置" onClose={onClose} width={920}>
      <div className={styles.notificationModalGrid}>
        <section className={styles.notificationForm}>
          <ToggleSwitch checked={enabled} onChange={setEnabled} label="启用通知渠道" />
          <label className={styles.field}>
            <span>渠道</span>
            <Select
              value={channel}
              onChange={(value) => setChannel(value as CodexInspectionNotificationChannel)}
              options={[
                { value: 'telegram', label: 'Telegram Bot' },
                { value: 'feishu', label: '飞书机器人' },
                { value: 'wecom', label: '企业微信机器人' },
                { value: 'webhook', label: '自定义 Webhook' },
              ]}
              ariaLabel="通知渠道"
            />
          </label>

          {channel === 'telegram' ? (
            <>
              <Input
                label="Bot Token"
                value={botToken}
                onChange={(event) => setBotToken(event.target.value)}
                placeholder="保存后后端应脱敏返回"
              />
              <Input label="Chat ID" value={chatId} onChange={(event) => setChatId(event.target.value)} />
            </>
          ) : null}

          {channel === 'feishu' || channel === 'wecom' || channel === 'webhook' ? (
            <Input
              label={channel === 'webhook' ? 'Webhook URL' : '机器人 Webhook'}
              value={webhookUrl}
              onChange={(event) => setWebhookUrl(event.target.value)}
              placeholder="保存后不应明文回显"
            />
          ) : null}

          {channel === 'feishu' ? (
            <Input label="Secret" value={secret} onChange={(event) => setSecret(event.target.value)} />
          ) : null}

          {channel === 'webhook' ? (
            <label className={styles.fieldWide}>
              <span>Header</span>
              <textarea value={headers} onChange={(event) => setHeaders(event.target.value)} />
            </label>
          ) : null}

          <label className={styles.fieldWide}>
            <span>消息模板</span>
            <textarea value={template} onChange={(event) => setTemplate(event.target.value)} />
          </label>

          <div className={styles.safeNotice}>
            <IconShield size={18} />
            <span>Token、Secret、Webhook URL 保存后需要由后端脱敏显示；测试通知失败不会阻止任务配置保存。</span>
          </div>

          <div className={styles.modalFooter}>
            <Button variant="secondary" onClick={onClose}>关闭</Button>
            <Button onClick={() => void testNotification()} loading={testing}>测试通知</Button>
          </div>
        </section>

        <aside className={styles.notificationPreviewPanel}>
          <div>
            <h3>消息预览</h3>
            <pre>{previewText}</pre>
          </div>
          <div>
            <h3>JSON Payload</h3>
            <pre>{JSON.stringify(payload, null, 2)}</pre>
          </div>
          {testResult ? (
            <div>
              <h3>测试结果</h3>
              <pre>{testResult}</pre>
            </div>
          ) : null}
        </aside>
      </div>
    </Modal>
  );
}
