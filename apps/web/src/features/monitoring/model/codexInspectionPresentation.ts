import type { TFunction } from 'i18next';
import {
  type CodexInspectionAction,
  type CodexInspectionAutoActionMode,
  type CodexInspectionConfigurableSettings,
  type CodexInspectionProgressSnapshot,
  type CodexInspectionResultItem,
  type CodexInspectionShortWindowQuotaMode,
  type CodexInspectionRunResult,
  type CodexInspectionStoredActionFilter,
  type CodexInspectionStoredLogEntry,
} from '@/features/monitoring/codexInspection';
import type { CodexInspectionResult } from '@/services/api/usageService';

export type RunStatus = 'idle' | 'running' | 'paused' | 'success' | 'error';

export type ActionFilter = CodexInspectionStoredActionFilter;

export type StatusTone = 'idle' | 'info' | 'good' | 'warn' | 'bad';

export type InspectionLogEntry = CodexInspectionStoredLogEntry;

export type ExecutionTriggerSource = 'manual' | 'auto';

export type CodexInspectionProblemActionMode = 'none' | 'disable' | 'delete';
export type ServerCodexInspectionAction = 'delete' | 'disable' | 'enable';
export type ServerCodexInspectionActionStatus =
  | 'none'
  | 'pending'
  | 'success'
  | 'failed'
  | 'skipped'
  | 'needs_review';

export const CODEX_INSPECTION_PROBLEM_ACTION_MODES: readonly CodexInspectionProblemActionMode[] =
  ['none', 'disable', 'delete'];

export type CodexInspectionSummaryIcon =
  | 'probe'
  | 'sampled'
  | 'delete'
  | 'disable'
  | 'enable'
  | 'reauth';

export type CodexInspectionSummaryAccent =
  | 'blue'
  | 'cyan'
  | 'red'
  | 'amber'
  | 'green'
  | 'violet';

export type SummaryCard = {
  key: string;
  label: string;
  value: string;
  meta: string;
  tone?: StatusTone;
  icon?: CodexInspectionSummaryIcon;
  accent?: CodexInspectionSummaryAccent;
};

export type CodexInspectionPaginationState<T> = {
  currentPage: number;
  totalPages: number;
  pageItems: T[];
  startItem: number;
  endItem: number;
  count: number;
};

export type InspectionSettingsDraft = {
  targetType: string;
  workers: string;
  deleteWorkers: string;
  timeout: string;
  retries: string;
  userAgent: string;
  usedPercentThreshold: string;
  sampleSize: string;
  autoActionMode: CodexInspectionAutoActionMode | string;
  shortWindowQuotaMode: CodexInspectionShortWindowQuotaMode | string;
};

export type InspectionSettingsDraftField = Exclude<
  keyof InspectionSettingsDraft,
  'autoActionMode' | 'shortWindowQuotaMode'
>;

export const ACTION_FILTERS: ActionFilter[] = [
  'all',
  'delete',
  'disable',
  'enable',
  'reauth',
  'http_401',
];

export const CODEX_INSPECTION_RESULT_PAGE_SIZE_OPTIONS = [20, 50, 100] as const;

export const formatTimestamp = (value: number, locale: string) =>
  new Date(value).toLocaleString(locale);

export const formatTime = (value: number, locale: string) =>
  new Date(value).toLocaleTimeString(locale);

export const formatPercent = (value: number | null) =>
  value === null ? '--' : `${value.toFixed(1)}%`;

export const toSettingsDraft = (
  settings: CodexInspectionConfigurableSettings
): InspectionSettingsDraft => ({
  targetType: settings.targetType,
  workers: String(settings.workers),
  deleteWorkers: String(settings.deleteWorkers),
  timeout: String(settings.timeout),
  retries: String(settings.retries),
  userAgent: settings.userAgent,
  usedPercentThreshold: String(settings.usedPercentThreshold),
  sampleSize: String(settings.sampleSize),
  autoActionMode: settings.autoActionMode,
  shortWindowQuotaMode: settings.shortWindowQuotaMode,
});

export const formatActionLabel = (action: CodexInspectionAction, t: TFunction) => {
  switch (action) {
    case 'delete':
      return t('monitoring.codex_inspection_action_delete');
    case 'disable':
      return t('monitoring.codex_inspection_action_disable');
    case 'enable':
      return t('monitoring.codex_inspection_action_enable');
    case 'reauth':
      return t('monitoring.codex_inspection_action_reauth');
    case 'keep':
    default:
      return t('monitoring.codex_inspection_action_keep');
  }
};

export const isServerCodexInspectionAction = (
  action: string
): action is ServerCodexInspectionAction =>
  action === 'delete' || action === 'disable' || action === 'enable';

export const normalizeServerCodexInspectionActionStatus = (
  item: Pick<CodexInspectionResult, 'action' | 'actionStatus'>
): ServerCodexInspectionActionStatus => {
  if (
    item.actionStatus === 'none' ||
    item.actionStatus === 'pending' ||
    item.actionStatus === 'success' ||
    item.actionStatus === 'failed' ||
    item.actionStatus === 'skipped' ||
    item.actionStatus === 'needs_review'
  ) {
    return item.actionStatus;
  }
  return isServerCodexInspectionAction(item.action) ? 'pending' : 'none';
};

export const isActionableServerCodexInspectionResult = (
  item: Pick<CodexInspectionResult, 'id' | 'action' | 'actionStatus'>
) => {
  const status = normalizeServerCodexInspectionActionStatus(item);
  return (
    item.id > 0 &&
    isServerCodexInspectionAction(item.action) &&
    (status === 'pending' || status === 'failed')
  );
};

export const getCanonicalServerCodexInspectionActionIds = (
  results: Array<Pick<CodexInspectionResult, 'id' | 'fileName' | 'action' | 'actionStatus'>>
) => {
  const canonicalIds = new Set<number>();
  const fileOrder: string[] = [];
  const groups = new Map<
    string,
    Array<Pick<CodexInspectionResult, 'id' | 'fileName' | 'action' | 'actionStatus'>>
  >();
  for (const item of results) {
    const fileName = item.fileName.trim();
    if (!isServerCodexInspectionAction(item.action) || !fileName) {
      continue;
    }
    if (!groups.has(fileName)) {
      groups.set(fileName, []);
      fileOrder.push(fileName);
    }
    groups.get(fileName)?.push(item);
  }
  for (const fileName of fileOrder) {
    const group = groups.get(fileName) ?? [];
    if (group.length === 0) continue;
    const action = group[0].action;
    if (group.some((item) => item.action !== action)) continue;
    if (isActionableServerCodexInspectionResult(group[0])) {
      canonicalIds.add(group[0].id);
    }
  }
  return canonicalIds;
};

export const getMixedServerCodexInspectionActionIds = (
  results: Array<Pick<CodexInspectionResult, 'id' | 'fileName' | 'action'>>
) => {
  const mixedIds = new Set<number>();
  const groups = new Map<string, Array<Pick<CodexInspectionResult, 'id' | 'fileName' | 'action'>>>();
  for (const item of results) {
    const fileName = item.fileName.trim();
    if (!isServerCodexInspectionAction(item.action) || !fileName) {
      continue;
    }
    if (!groups.has(fileName)) {
      groups.set(fileName, []);
    }
    groups.get(fileName)?.push(item);
  }
  for (const group of groups.values()) {
    if (group.length === 0) continue;
    const action = group[0].action;
    if (!group.some((item) => item.action !== action)) continue;
    group.forEach((item) => mixedIds.add(item.id));
  }
  return mixedIds;
};

export const formatCurrentStateLabel = (item: CodexInspectionResultItem, t: TFunction) => {
  if (item.disabled) return t('monitoring.codex_inspection_state_disabled');
  return t('monitoring.codex_inspection_state_enabled');
};

export const countActions = (items: CodexInspectionResultItem[]) => {
  const summary = {
    delete: 0,
    disable: 0,
    enable: 0,
    reauth: 0,
    http401: 0,
  };

  items.forEach((item) => {
    if (item.action === 'delete') summary.delete += 1;
    if (item.action === 'disable') summary.disable += 1;
    if (item.action === 'enable') summary.enable += 1;
    if (item.action === 'reauth') summary.reauth += 1;
    if (item.statusCode === 401) summary.http401 += 1;
  });

  return summary;
};

export const createIdleProgressSnapshot = (): CodexInspectionProgressSnapshot => ({
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
    reauthCount: 0,
    keepCount: 0,
  },
  startedAt: Date.now(),
  updatedAt: Date.now(),
});

export const createCompletedProgressSnapshot = (
  result: CodexInspectionRunResult
): CodexInspectionProgressSnapshot => {
  const total = Math.max(0, result.summary.sampledCount || result.results.length);
  return {
    total,
    completed: total,
    inFlight: 0,
    pending: 0,
    percent: total > 0 ? 100 : 0,
    status: 'completed',
    summary: {
      totalFiles: result.summary.totalFiles,
      probeSetCount: result.summary.probeSetCount,
      sampledCount: result.summary.sampledCount,
      deleteCount: result.summary.deleteCount,
      disableCount: result.summary.disableCount,
      enableCount: result.summary.enableCount,
      reauthCount: result.summary.reauthCount,
      keepCount: result.summary.keepCount,
    },
    startedAt: result.startedAt,
    updatedAt: result.finishedAt || Date.now(),
  };
};

export const filterByAction = (items: CodexInspectionResultItem[], filter: ActionFilter) => {
  if (filter === 'all') return items;
  if (filter === 'http_401') return items.filter((item) => item.statusCode === 401);
  return items.filter((item) => item.action === filter);
};

export const buildCodexInspectionPaginationState = <T>(
  items: readonly T[],
  page: number,
  pageSize: number
): CodexInspectionPaginationState<T> => {
  const safePageSize = Math.max(1, pageSize);
  const totalPages = Math.max(1, Math.ceil(items.length / safePageSize));
  const currentPage = Math.min(Math.max(1, Number.isFinite(page) ? page : 1), totalPages);
  const startIndex = (currentPage - 1) * safePageSize;
  const endIndex = Math.min(startIndex + safePageSize, items.length);

  return {
    currentPage,
    totalPages,
    pageItems: items.slice(startIndex, endIndex),
    startItem: items.length > 0 ? startIndex + 1 : 0,
    endItem: endIndex,
    count: items.length,
  };
};

export const isCodexInspectionAutoExecutionEnabled = (
  mode: CodexInspectionAutoActionMode
) => mode !== 'none';

export const getCodexInspectionProblemActionMode = (
  mode: CodexInspectionAutoActionMode
): CodexInspectionProblemActionMode => {
  if (mode === 'disable' || mode === 'delete') return mode;
  return 'none';
};

export const composeCodexInspectionAutoActionMode = (
  enabled: boolean,
  problemActionMode: CodexInspectionProblemActionMode
): CodexInspectionAutoActionMode => {
  if (!enabled) return 'none';
  if (problemActionMode === 'disable' || problemActionMode === 'delete') {
    return problemActionMode;
  }
  return 'enable';
};

export const formatAutoActionModeLabel = (
  mode: CodexInspectionAutoActionMode,
  t: TFunction
) => {
  switch (mode) {
    case 'delete':
      return t('monitoring.codex_inspection_settings_auto_action_mode_delete');
    case 'disable':
      return t('monitoring.codex_inspection_settings_auto_action_mode_disable');
    case 'enable':
      return t('monitoring.codex_inspection_settings_auto_action_mode_enable');
    case 'none':
    default:
      return t('monitoring.codex_inspection_settings_auto_action_mode_none');
  }
};

// ─── 共享配置：字段级校验 + 概览卡数据 ───────────────────────────────
// 本地与服务端共有的可校验文本字段（autoActionMode 走卡片选择,无需文本校验）。
export type SharedInspectionConfigField =
  | 'targetType'
  | 'usedPercentThreshold'
  | 'sampleSize'
  | 'workers'
  | 'deleteWorkers'
  | 'timeout'
  | 'retries'
  | 'userAgent';

export type SharedInspectionConfigDraft = {
  [K in SharedInspectionConfigField]: string;
} & {
  autoActionMode: CodexInspectionAutoActionMode | string;
  shortWindowQuotaMode: CodexInspectionShortWindowQuotaMode | string;
};

export type InspectionConfigFieldErrors = Partial<
  Record<SharedInspectionConfigField, string>
>;

export type ValidatedInspectionConfigValues = {
  targetType: string;
  workers: number;
  deleteWorkers: number;
  timeout: number;
  retries: number;
  userAgent: string;
  usedPercentThreshold: number;
  sampleSize: number;
  autoActionMode: CodexInspectionAutoActionMode;
  shortWindowQuotaMode: CodexInspectionShortWindowQuotaMode;
};

type InspectionConfigDraftValidation =
  | {
      ok: true;
      errors: InspectionConfigFieldErrors;
      values: ValidatedInspectionConfigValues;
    }
  | {
      ok: false;
      errors: InspectionConfigFieldErrors;
      values: null;
    };

export const normalizeInspectionAutoActionMode = (
  mode: CodexInspectionAutoActionMode | string
): CodexInspectionAutoActionMode => {
  if (mode === 'enable' || mode === 'disable' || mode === 'delete') return mode;
  return 'none';
};

export const normalizeInspectionShortWindowQuotaMode = (
  mode: CodexInspectionShortWindowQuotaMode | string
): CodexInspectionShortWindowQuotaMode => (mode === 'disable' ? 'disable' : 'keep');

export const formatShortWindowQuotaModeLabel = (
  mode: CodexInspectionShortWindowQuotaMode | string,
  t: TFunction
) => {
  switch (normalizeInspectionShortWindowQuotaMode(mode)) {
    case 'disable':
      return t('monitoring.codex_inspection_settings_short_window_quota_mode_disable');
    case 'keep':
    default:
      return t('monitoring.codex_inspection_settings_short_window_quota_mode_keep');
  }
};

// 字段级即时校验,边界与 normalizeConfigurableSettings 保持一致,作为两模式单一校验源。
export const validateInspectionConfigFields = (
  draft: SharedInspectionConfigDraft,
  t: TFunction
): InspectionConfigFieldErrors => {
  const errors: InspectionConfigFieldErrors = {};

  if (!draft.targetType.trim()) {
    errors.targetType = t('monitoring.codex_inspection_settings_target_type_required');
  }

  const checkInteger = (field: SharedInspectionConfigField, min: number, labelKey: string) => {
    const parsed = Number(draft[field].trim());
    if (!Number.isFinite(parsed) || !Number.isInteger(parsed) || parsed < min) {
      errors[field] = t('monitoring.codex_inspection_settings_invalid_integer', {
        field: t(labelKey),
        min,
      });
    }
  };

  checkInteger('workers', 1, 'monitoring.codex_inspection_settings_workers_label');
  checkInteger('deleteWorkers', 1, 'monitoring.codex_inspection_settings_delete_workers_label');
  checkInteger('timeout', 1, 'monitoring.codex_inspection_settings_timeout_label');
  checkInteger('retries', 0, 'monitoring.codex_inspection_settings_retries_label');
  checkInteger('sampleSize', 0, 'monitoring.codex_inspection_settings_sample_size_label');

  const threshold = Number(draft.usedPercentThreshold.trim());
  if (!Number.isFinite(threshold) || threshold < 0 || threshold > 100) {
    errors.usedPercentThreshold = t('monitoring.codex_inspection_settings_invalid_threshold', {
      field: t('monitoring.codex_inspection_settings_used_percent_threshold_label'),
    });
  }

  return errors;
};

export const hasInspectionConfigFieldErrors = (errors: InspectionConfigFieldErrors): boolean =>
  Object.values(errors).some(Boolean);

export const validateInspectionConfigDraft = (
  draft: SharedInspectionConfigDraft,
  t: TFunction
): InspectionConfigDraftValidation => {
  const errors = validateInspectionConfigFields(draft, t);
  if (hasInspectionConfigFieldErrors(errors)) {
    return { ok: false, errors, values: null };
  }

  return {
    ok: true,
    errors,
    values: {
      targetType: draft.targetType.trim(),
      workers: Number(draft.workers.trim()),
      deleteWorkers: Number(draft.deleteWorkers.trim()),
      timeout: Number(draft.timeout.trim()),
      retries: Number(draft.retries.trim()),
      userAgent: draft.userAgent.trim(),
      usedPercentThreshold: Number(draft.usedPercentThreshold.trim()),
      sampleSize: Number(draft.sampleSize.trim()),
      autoActionMode: normalizeInspectionAutoActionMode(draft.autoActionMode),
      shortWindowQuotaMode: normalizeInspectionShortWindowQuotaMode(draft.shortWindowQuotaMode),
    },
  };
};

// 自动处置模式 → 概览卡语气色,危险动作更醒目。
export const getAutoActionTone = (mode: CodexInspectionAutoActionMode | string): StatusTone => {
  switch (normalizeInspectionAutoActionMode(mode)) {
    case 'delete':
      return 'bad';
    case 'disable':
      return 'warn';
    case 'enable':
      return 'good';
    case 'none':
    default:
      return 'idle';
  }
};

// 概览卡单项:label/value 结构,可选语气色、次要说明与点击聚焦的目标字段。
export type ConfigOverviewItem = {
  key: string;
  label: string;
  value: string;
  hint?: string;
  tone?: StatusTone;
  field?: string;
};

type ConfigOverviewSettings = Pick<
  CodexInspectionConfigurableSettings,
  'targetType' | 'workers' | 'timeout' | 'usedPercentThreshold' | 'sampleSize' | 'shortWindowQuotaMode'
> & {
  autoActionMode: CodexInspectionAutoActionMode | string;
};

type BuildConfigOverviewItemsOptions =
  | {
      mode: 'local';
      t: TFunction;
    }
  | {
      mode: 'server';
      t: TFunction;
      scheduleEnabled: boolean;
      scheduleLabel: string;
    };

export const buildConfigOverviewItems = (
  settings: ConfigOverviewSettings,
  options: BuildConfigOverviewItemsOptions
): ConfigOverviewItem[] => {
  const { t } = options;
  const autoActionMode = normalizeInspectionAutoActionMode(settings.autoActionMode);
  const autoActionLabel = formatAutoActionModeLabel(autoActionMode, t);
  const shortWindowQuotaMode = normalizeInspectionShortWindowQuotaMode(settings.shortWindowQuotaMode);
  const shortWindowQuotaLabel = formatShortWindowQuotaModeLabel(shortWindowQuotaMode, t);
  const sampleSizeLabel =
    settings.sampleSize > 0
      ? String(settings.sampleSize)
      : t('monitoring.server_codex_inspection_sample_all');

  if (options.mode === 'server') {
    return [
      {
        key: 'schedule',
        label: t('monitoring.server_codex_inspection_config_summary_schedule'),
        value: options.scheduleEnabled
          ? t('monitoring.server_codex_inspection_schedule_enabled')
          : t('monitoring.server_codex_inspection_schedule_disabled'),
        tone: options.scheduleEnabled ? 'good' : 'idle',
        field: 'schedule',
      },
      {
        key: 'trigger',
        label: t('monitoring.server_codex_inspection_config_summary_trigger'),
        value: options.scheduleLabel,
        field: 'schedule',
      },
      {
        key: 'threshold',
        label: t('monitoring.server_codex_inspection_config_summary_threshold'),
        value: `${settings.usedPercentThreshold}%`,
        field: 'usedPercentThreshold',
      },
      {
        key: 'sample',
        label: t('monitoring.server_codex_inspection_config_summary_sample'),
        value: sampleSizeLabel,
        field: 'sampleSize',
      },
      {
        key: 'short-window',
        label: t('monitoring.codex_inspection_settings_short_window_quota_mode_label'),
        value: shortWindowQuotaLabel,
        tone: shortWindowQuotaMode === 'disable' ? 'warn' : 'idle',
        field: 'shortWindowQuotaMode',
      },
      {
        key: 'auto',
        label: t('monitoring.server_codex_inspection_config_summary_auto'),
        value: autoActionLabel,
        tone: getAutoActionTone(autoActionMode),
        field: 'autoActionMode',
      },
    ];
  }

  return [
    {
      key: 'threshold',
      label: t('monitoring.codex_inspection_threshold'),
      value: `${settings.usedPercentThreshold}%`,
      field: 'usedPercentThreshold',
    },
    {
      key: 'sample',
      label: t('monitoring.codex_inspection_sample_size'),
      value: sampleSizeLabel,
      field: 'sampleSize',
    },
    {
      key: 'short-window',
      label: t('monitoring.codex_inspection_settings_short_window_quota_mode_label'),
      value: shortWindowQuotaLabel,
      tone: shortWindowQuotaMode === 'disable' ? 'warn' : 'idle',
      field: 'shortWindowQuotaMode',
    },
    {
      key: 'auto',
      label: t('monitoring.codex_inspection_settings_auto_action_mode_label'),
      value: autoActionLabel,
      tone: getAutoActionTone(autoActionMode),
      field: 'autoActionMode',
    },
    {
      key: 'concurrency',
      label: t('monitoring.codex_inspection_workers'),
      value: String(settings.workers),
      hint: `${t('monitoring.codex_inspection_settings_timeout_label')}: ${settings.timeout}`,
      field: 'workers',
    },
    {
      key: 'target',
      label: t('monitoring.codex_inspection_target_type'),
      value: settings.targetType,
      field: 'targetType',
    },
  ];
};
