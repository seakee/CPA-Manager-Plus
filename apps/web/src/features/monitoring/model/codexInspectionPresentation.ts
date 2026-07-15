import type { TFunction } from 'i18next';
import {
  type CodexInspectionAction,
  type CodexInspectionAutoActionMode,
  type CodexInspectionConfigurableSettings,
  type CodexInspectionProgressSnapshot,
  type CodexInspectionResultItem,
  type CodexInspectionRunResult,
  type CodexInspectionStoredActionFilter,
  type CodexInspectionStoredLogEntry,
} from '@/features/monitoring/codexInspection';
import type { CodexInspectionResult } from '@/services/api/usageService';

export type RunStatus = 'idle' | 'running' | 'paused' | 'success' | 'error';

export type ActionFilter = CodexInspectionStoredActionFilter;
export type HandlingFilter = 'all' | 'pending' | 'no_action';

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

export const CODEX_INSPECTION_PROBLEM_ACTION_MODES: readonly CodexInspectionProblemActionMode[] = [
  'none',
  'disable',
  'delete',
];

export type CodexInspectionSummaryIcon =
  | 'probe'
  | 'sampled'
  | 'delete'
  | 'disable'
  | 'enable'
  | 'reauth';

export type CodexInspectionSummaryAccent = 'blue' | 'cyan' | 'red' | 'amber' | 'green' | 'violet';

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
  autoActionMode: CodexInspectionAutoActionMode;
  autoRecoverEnabled: boolean;
};

export type InspectionSettingsDraftField = Exclude<
  keyof InspectionSettingsDraft,
  'autoActionMode' | 'autoRecoverEnabled'
>;

export const ACTION_FILTERS: ActionFilter[] = [
  'all',
  'reauth',
  'delete',
  'disable',
  'enable',
  'keep',
];

export const HANDLING_FILTERS: HandlingFilter[] = ['all', 'pending', 'no_action'];

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
  autoRecoverEnabled: settings.autoRecoverEnabled,
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

export const isPendingServerReauthResult = (
  item: Pick<CodexInspectionResult, 'action' | 'actionStatus' | 'executedAction'>
) => {
  if (item.action !== 'reauth' || item.executedAction === 'delete') return false;
  const status = normalizeServerCodexInspectionActionStatus(item);
  return status === 'none' || status === 'pending' || status === 'failed';
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
  const groups = new Map<
    string,
    Array<Pick<CodexInspectionResult, 'id' | 'fileName' | 'action'>>
  >();
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
    keep: 0,
  };

  items.forEach((item) => {
    if (item.action === 'delete') summary.delete += 1;
    if (item.action === 'disable') summary.disable += 1;
    if (item.action === 'enable') summary.enable += 1;
    if (item.action === 'reauth') summary.reauth += 1;
    if (item.action === 'keep') summary.keep += 1;
    if (item.statusCode === 401) summary.http401 += 1;
  });

  return summary;
};

export const normalizeActionFilter = (value: unknown): ActionFilter => {
  if (value === 'http_401') return 'reauth';
  if (
    value === 'all' ||
    value === 'delete' ||
    value === 'disable' ||
    value === 'enable' ||
    value === 'reauth' ||
    value === 'keep'
  ) {
    return value;
  }
  return 'all';
};

export const isNeedsHandling = (
  item: Pick<CodexInspectionResultItem, 'action' | 'statusCode' | 'actionHandled'>
) => !item.actionHandled && (item.action !== 'keep' || item.statusCode === 401);

export const countHandlingStates = (items: CodexInspectionResultItem[]) => {
  const pending = items.filter(isNeedsHandling).length;
  return {
    all: items.length,
    pending,
    no_action: items.length - pending,
  } satisfies Record<HandlingFilter, number>;
};

export const getActionFilterCounts = (items: CodexInspectionResultItem[]) => {
  const counts = countActions(items);
  return {
    all: items.length,
    reauth: counts.reauth,
    delete: counts.delete,
    disable: counts.disable,
    enable: counts.enable,
    keep: counts.keep,
  } satisfies Record<ActionFilter, number>;
};

export const filterByHandling = (items: CodexInspectionResultItem[], filter: HandlingFilter) => {
  if (filter === 'pending') return items.filter(isNeedsHandling);
  if (filter === 'no_action') return items.filter((item) => !isNeedsHandling(item));
  return items;
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
  return items.filter((item) => item.action === filter);
};

export const filterInspectionResults = (
  items: CodexInspectionResultItem[],
  handlingFilter: HandlingFilter,
  actionFilter: ActionFilter
) => filterByAction(filterByHandling(items, handlingFilter), actionFilter);

export const summarizeInspectionError = (
  item: Pick<
    CodexInspectionResultItem,
    'action' | 'statusCode' | 'errorKind' | 'error' | 'errorDetail'
  >,
  t: TFunction
) => {
  if (item.action === 'reauth' || item.statusCode === 401) {
    return t('monitoring.codex_inspection_error_summary_reauth');
  }
  if (item.errorKind) {
    return t('monitoring.codex_inspection_error_summary_kind', { kind: item.errorKind });
  }
  const raw = item.error || item.errorDetail;
  if (!raw) return '';
  const trimmed = raw.trim();
  if (!trimmed) return '';
  if (trimmed.length <= 120 && !trimmed.startsWith('{') && !trimmed.startsWith('[')) {
    return trimmed;
  }
  return t('monitoring.codex_inspection_error_summary_response');
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
  mode: CodexInspectionAutoActionMode,
  autoRecoverEnabled = false
) => mode === 'disable' || mode === 'delete' || autoRecoverEnabled;

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

export const formatAutoActionModeLabel = (mode: CodexInspectionAutoActionMode, t: TFunction) => {
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
  autoRecoverEnabled: boolean;
};

export type InspectionConfigFieldErrors = Partial<Record<SharedInspectionConfigField, string>>;

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
  autoRecoverEnabled: boolean;
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

// 字段级即时校验,边界与 normalizeConfigurableSettings 保持一致,作为两模式单一校验源。
export const validateInspectionConfigFields = (
  draft: SharedInspectionConfigDraft,
  t: TFunction
): InspectionConfigFieldErrors => {
  const errors: InspectionConfigFieldErrors = {};

  if (!['codex', 'xai'].includes(draft.targetType.trim().toLowerCase())) {
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
      autoRecoverEnabled: draft.autoRecoverEnabled === true,
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
  'targetType' | 'workers' | 'timeout' | 'usedPercentThreshold' | 'sampleSize'
> & {
  autoActionMode: CodexInspectionAutoActionMode | string;
  autoRecoverEnabled: boolean;
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
        key: 'auto',
        label: t('monitoring.server_codex_inspection_config_summary_auto'),
        value: autoActionLabel,
        tone: getAutoActionTone(autoActionMode),
        field: 'autoActionMode',
      },
      {
        key: 'recover',
        label: t('monitoring.codex_inspection_settings_auto_recover_label'),
        value: settings.autoRecoverEnabled ? t('common.enabled') : t('common.disabled'),
        tone: settings.autoRecoverEnabled ? 'good' : 'idle',
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
      key: 'auto',
      label: t('monitoring.codex_inspection_settings_auto_action_mode_label'),
      value: autoActionLabel,
      tone: getAutoActionTone(autoActionMode),
      field: 'autoActionMode',
    },
    {
      key: 'recover',
      label: t('monitoring.codex_inspection_settings_auto_recover_label'),
      value: settings.autoRecoverEnabled ? t('common.enabled') : t('common.disabled'),
      tone: settings.autoRecoverEnabled ? 'good' : 'idle',
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
