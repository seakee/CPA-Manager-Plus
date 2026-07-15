import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import {
  IconChartLine,
  IconCheck,
  IconInbox,
  IconRefreshCw,
  IconShield,
  IconTrash2,
} from '@/components/ui/icons';
import { Input } from '@/components/ui/Input';
import { Select, type SelectOption } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { CodexInspectionConfigOverview } from '@/features/monitoring/components/CodexInspectionConfigOverview';
import { CodexInspectionModeTabs } from '@/features/monitoring/components/CodexInspectionModeTabs';
import { Panel } from '@/features/monitoring/components/CodexInspectionPanels';
import { CodexInspectionResultsPanel } from '@/features/monitoring/components/CodexInspectionResultsPanel';
import { InspectionConfigDrawer } from '@/features/monitoring/components/InspectionConfigDrawer';
import { InspectionConfigFields } from '@/features/monitoring/components/InspectionConfigFields';
import { CodexReauthDialog } from '@/features/oauth/CodexReauthDialog';
import type { CodexReauthTarget } from '@/features/oauth/codexReauthModel';
import { formatPercent } from '@/features/monitoring/components/accountOverviewPresentation';
import {
  type CodexInspectionAction,
  type CodexInspectionResultItem,
  type CodexInspectionRunResult,
} from '@/features/monitoring/codexInspection';
import {
  CODEX_INSPECTION_RESULT_PAGE_SIZE_OPTIONS,
  buildCodexInspectionPaginationState,
  buildConfigOverviewItems,
  type CodexInspectionSummaryAccent,
  countHandlingStates,
  filterInspectionResults,
  formatActionLabel,
  formatTimestamp,
  getActionFilterCounts,
  getCanonicalServerCodexInspectionActionIds,
  getMixedServerCodexInspectionActionIds,
  isActionableServerCodexInspectionResult,
  isPendingServerReauthResult,
  normalizeServerCodexInspectionActionStatus,
  type ActionFilter,
  type HandlingFilter,
  type StatusTone,
  validateInspectionConfigDraft,
  validateInspectionConfigFields,
} from '@/features/monitoring/model/codexInspectionPresentation';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import {
  getUsageServiceErrorCode,
  monitoringAnalyticsApi,
  usageServiceApi,
  type CodexInspectionLog,
  type CodexInspectionResult,
  type CodexInspectionRun,
  type CodexInspectionRunDetail,
  type ManagerCodexInspectionConfig,
  type ManagerCodexInspectionScheduleMode,
  type ManagerConfig,
  type UsageHeaderSnapshot,
} from '@/services/api/usageService';
import { useAuthStore, useNotificationStore } from '@/stores';
import {
  buildUsageHeaderSnapshotLookup,
  getHeaderSnapshotErrorCode,
  getHeaderSnapshotErrorKind,
  getHeaderSnapshotPlanType,
  getHeaderSnapshotRecoverAtMs,
  getHeaderSnapshotTraceId,
  getHeaderSnapshotUsedPercent,
  getUsageHeaderSnapshotForIdentity,
} from '@/utils/usageHeaderSnapshots';
import styles from './CodexInspectionPage.module.scss';

type ServerCodexInspectionDraft = {
  enabled: boolean;
  scheduleMode: ManagerCodexInspectionScheduleMode;
  intervalMinutes: string;
  timePoints: string;
  timeZone: string;
  targetType: string;
  workers: string;
  deleteWorkers: string;
  timeout: string;
  retries: string;
  userAgent: string;
  usedPercentThreshold: string;
  sampleSize: string;
  autoActionMode: string;
  autoRecoverEnabled: boolean;
};

type NormalizedServerCodexInspectionConfig = {
  enabled: boolean;
  schedule: {
    mode: ManagerCodexInspectionScheduleMode;
    intervalMinutes: number;
    timePoints: string[];
    timeZone: string;
  };
  targetType: string;
  workers: number;
  deleteWorkers: number;
  timeout: number;
  retries: number;
  userAgent: string;
  usedPercentThreshold: number;
  sampleSize: number;
  autoActionMode: string;
  autoRecoverEnabled: boolean;
};

const DEFAULT_SERVER_CODEX_CONFIG: NormalizedServerCodexInspectionConfig = {
  enabled: false,
  schedule: {
    mode: 'interval',
    intervalMinutes: 60,
    timePoints: [],
    timeZone: '',
  },
  targetType: 'codex',
  workers: 4,
  deleteWorkers: 4,
  timeout: 15000,
  retries: 0,
  userAgent: 'codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal',
  usedPercentThreshold: 100,
  sampleSize: 0,
  autoActionMode: 'none',
  autoRecoverEnabled: false,
};

const RUNS_LIMIT = 30;

const COMMON_TIME_ZONES: ReadonlyArray<string> = [
  'UTC',
  'Asia/Shanghai',
  'Asia/Tokyo',
  'Asia/Singapore',
  'Asia/Hong_Kong',
  'Asia/Kolkata',
  'Europe/London',
  'Europe/Berlin',
  'Europe/Moscow',
  'America/New_York',
  'America/Los_Angeles',
];

const detectBrowserTimeZone = (): string => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || '';
  } catch {
    return '';
  }
};

const isScheduleMode = (value: unknown): value is ManagerCodexInspectionScheduleMode =>
  value === 'interval' || value === 'time_points';

const resolveServerCodexConfig = (
  config?: ManagerCodexInspectionConfig | null
): NormalizedServerCodexInspectionConfig => {
  const schedule = config?.schedule ?? {};
  const scheduleMode = isScheduleMode(schedule.mode)
    ? schedule.mode
    : schedule.timePoints && schedule.timePoints.length > 0
      ? 'time_points'
      : DEFAULT_SERVER_CODEX_CONFIG.schedule.mode;

  return {
    ...DEFAULT_SERVER_CODEX_CONFIG,
    ...config,
    enabled: config?.enabled ?? DEFAULT_SERVER_CODEX_CONFIG.enabled,
    schedule: {
      mode: scheduleMode,
      intervalMinutes:
        schedule.intervalMinutes && schedule.intervalMinutes > 0
          ? schedule.intervalMinutes
          : DEFAULT_SERVER_CODEX_CONFIG.schedule.intervalMinutes,
      timePoints: schedule.timePoints ?? DEFAULT_SERVER_CODEX_CONFIG.schedule.timePoints,
      timeZone:
        typeof schedule.timeZone === 'string'
          ? schedule.timeZone
          : DEFAULT_SERVER_CODEX_CONFIG.schedule.timeZone,
    },
    targetType: config?.targetType || DEFAULT_SERVER_CODEX_CONFIG.targetType,
    workers:
      config?.workers && config.workers > 0 ? config.workers : DEFAULT_SERVER_CODEX_CONFIG.workers,
    deleteWorkers:
      config?.deleteWorkers && config.deleteWorkers > 0
        ? config.deleteWorkers
        : DEFAULT_SERVER_CODEX_CONFIG.deleteWorkers,
    timeout:
      config?.timeout && config.timeout > 0 ? config.timeout : DEFAULT_SERVER_CODEX_CONFIG.timeout,
    retries:
      config?.retries !== undefined && config.retries >= 0
        ? config.retries
        : DEFAULT_SERVER_CODEX_CONFIG.retries,
    userAgent: config?.userAgent || DEFAULT_SERVER_CODEX_CONFIG.userAgent,
    usedPercentThreshold:
      config?.usedPercentThreshold !== undefined
        ? config.usedPercentThreshold
        : DEFAULT_SERVER_CODEX_CONFIG.usedPercentThreshold,
    sampleSize:
      config?.sampleSize !== undefined && config.sampleSize >= 0
        ? config.sampleSize
        : DEFAULT_SERVER_CODEX_CONFIG.sampleSize,
    autoActionMode: config?.autoActionMode || DEFAULT_SERVER_CODEX_CONFIG.autoActionMode,
    autoRecoverEnabled:
      config?.autoRecoverEnabled ?? DEFAULT_SERVER_CODEX_CONFIG.autoRecoverEnabled,
  };
};

const toDraft = (config?: ManagerCodexInspectionConfig | null): ServerCodexInspectionDraft => {
  const resolved = resolveServerCodexConfig(config);
  return {
    enabled: resolved.enabled,
    scheduleMode: resolved.schedule.mode as ManagerCodexInspectionScheduleMode,
    intervalMinutes: String(resolved.schedule.intervalMinutes),
    timePoints: resolved.schedule.timePoints.join(', '),
    timeZone: resolved.schedule.timeZone,
    targetType: resolved.targetType,
    workers: String(resolved.workers),
    deleteWorkers: String(resolved.deleteWorkers),
    timeout: String(resolved.timeout),
    retries: String(resolved.retries),
    userAgent: resolved.userAgent,
    usedPercentThreshold: String(resolved.usedPercentThreshold),
    sampleSize: String(resolved.sampleSize),
    autoActionMode: resolved.autoActionMode,
    autoRecoverEnabled: resolved.autoRecoverEnabled,
  };
};

const normalizeTimePoint = (value: string): string | null => {
  const match = value.trim().match(/^(\d{1,2}):(\d{1,2})$/);
  if (!match) return null;
  const hour = Number(match[1]);
  const minute = Number(match[2]);
  if (!Number.isInteger(hour) || !Number.isInteger(minute)) return null;
  if (hour < 0 || hour > 23 || minute < 0 || minute > 59) return null;
  return `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`;
};

const splitTimePointTokens = (raw: string): string[] =>
  raw
    .split(/[\s,;，；]+/)
    .map((value) => value.trim())
    .filter(Boolean);

const parseTimePoints = (raw: string): string[] =>
  Array.from(
    new Set(
      splitTimePointTokens(raw)
        .map(normalizeTimePoint)
        .filter((value): value is string => Boolean(value))
    )
  ).sort();

const normalizeTimePointList = (values: string[]): string[] =>
  Array.from(
    new Set(values.map(normalizeTimePoint).filter((value): value is string => Boolean(value)))
  ).sort();

const readScheduleInteger = (raw: string, min: number): number | null => {
  const value = Number(raw);
  if (!Number.isInteger(value) || value < min) return null;
  return value;
};

const createConfigFromDraft = (
  draft: ServerCodexInspectionDraft,
  t: TFunction
): ManagerCodexInspectionConfig | null => {
  const validation = validateInspectionConfigDraft(draft, t);
  if (!validation.ok) {
    return null;
  }

  const parsedIntervalMinutes = readScheduleInteger(draft.intervalMinutes, 1);
  const intervalMinutes =
    parsedIntervalMinutes ?? DEFAULT_SERVER_CODEX_CONFIG.schedule.intervalMinutes;
  const hasInvalidTimePoint =
    draft.scheduleMode === 'time_points' &&
    splitTimePointTokens(draft.timePoints).some((value) => normalizeTimePoint(value) === null);
  const timePoints = parseTimePoints(draft.timePoints);

  if (draft.scheduleMode === 'interval' && parsedIntervalMinutes === null) {
    return null;
  }

  if (draft.scheduleMode === 'time_points' && (hasInvalidTimePoint || timePoints.length === 0)) {
    return null;
  }

  return {
    enabled: draft.enabled,
    schedule:
      draft.scheduleMode === 'time_points'
        ? {
            mode: 'time_points',
            timePoints,
            intervalMinutes,
            timeZone: draft.timeZone.trim(),
          }
        : {
            mode: 'interval',
            intervalMinutes,
            timePoints,
            timeZone: draft.timeZone.trim(),
          },
    targetType: validation.values.targetType,
    workers: validation.values.workers,
    deleteWorkers: validation.values.deleteWorkers,
    timeout: validation.values.timeout,
    retries: validation.values.retries,
    userAgent: validation.values.userAgent,
    usedPercentThreshold: validation.values.usedPercentThreshold,
    sampleSize: validation.values.sampleSize,
    autoActionMode: validation.values.autoActionMode,
    autoRecoverEnabled: validation.values.autoRecoverEnabled,
  };
};

const statusToneClass: Record<StatusTone, string> = {
  idle: styles['tone-idle'],
  info: styles['tone-info'],
  good: styles['tone-good'],
  warn: styles['tone-warn'],
  bad: styles['tone-bad'],
};

const summaryAccentClassMap: Record<CodexInspectionSummaryAccent, string> = {
  blue: styles.summaryAccentBlue,
  cyan: styles.summaryAccentCyan,
  red: styles.summaryAccentRed,
  amber: styles.summaryAccentAmber,
  green: styles.summaryAccentGreen,
  violet: styles.summaryAccentViolet,
};

const logLevelClass: Record<string, string> = {
  info: styles.logInfo,
  success: styles.logSuccess,
  warning: styles.logWarning,
  error: styles.logError,
};

function getRunTone(run?: CodexInspectionRun | null): StatusTone {
  switch (run?.status) {
    case 'completed':
      return 'good';
    case 'failed':
      return 'bad';
    case 'running':
      return 'info';
    default:
      return 'idle';
  }
}

function getRunStatusLabel(
  run: CodexInspectionRun | null | undefined,
  t: ReturnType<typeof useTranslation>['t']
) {
  switch (run?.status) {
    case 'completed':
      return t('monitoring.codex_inspection_status_success');
    case 'failed':
      return t('monitoring.codex_inspection_status_error');
    case 'running':
      return t('monitoring.codex_inspection_status_running');
    default:
      return t('monitoring.codex_inspection_status_idle');
  }
}

function formatDuration(
  run: CodexInspectionRun | null | undefined,
  t: ReturnType<typeof useTranslation>['t']
) {
  if (!run?.startedAtMs || !run.finishedAtMs) return t('common.not_set');
  const seconds = Math.max(0, Math.round((run.finishedAtMs - run.startedAtMs) / 1000));
  return t('monitoring.server_codex_inspection_duration_value', { seconds });
}

function formatTrigger(
  run: CodexInspectionRun | null | undefined,
  t: ReturnType<typeof useTranslation>['t']
) {
  if (!run) return t('common.not_set');
  if (run.triggerType === 'scheduled')
    return t('monitoring.server_codex_inspection_trigger_scheduled');
  return t('monitoring.server_codex_inspection_trigger_manual');
}

function formatResultStateHeader(
  run: CodexInspectionRun | null | undefined,
  t: ReturnType<typeof useTranslation>['t']
) {
  if (run?.triggerType === 'scheduled') {
    return t('monitoring.server_codex_inspection_result_state_scheduled');
  }
  if (run?.triggerType === 'manual') {
    return t('monitoring.server_codex_inspection_result_state_manual');
  }
  return t('monitoring.server_codex_inspection_result_state_snapshot');
}

function formatResultsDescription(
  run: CodexInspectionRun | null | undefined,
  locale: string,
  t: ReturnType<typeof useTranslation>['t']
) {
  const time = run?.finishedAtMs ? formatTimestamp(run.finishedAtMs, locale) : t('common.not_set');
  if (run?.triggerType === 'manual') {
    return t('monitoring.server_codex_inspection_results_desc_manual', { time });
  }
  if (run?.triggerType === 'scheduled') {
    return t('monitoring.server_codex_inspection_results_desc_scheduled', { time });
  }
  return t('monitoring.server_codex_inspection_results_desc');
}

function formatSchedule(
  config: NormalizedServerCodexInspectionConfig,
  t: ReturnType<typeof useTranslation>['t']
) {
  if (config.schedule.mode === 'time_points') {
    const base = t('monitoring.server_codex_inspection_schedule_time_points_value', {
      points: config.schedule.timePoints.join(', '),
    });
    const tz = config.schedule.timeZone?.trim();
    return tz ? `${base} (${tz})` : base;
  }
  return t('monitoring.server_codex_inspection_schedule_interval_value', {
    minutes: config.schedule.intervalMinutes,
  });
}

function getComparableConfig(config: NormalizedServerCodexInspectionConfig) {
  return {
    enabled: config.enabled,
    scheduleMode: config.schedule.mode,
    intervalMinutes: config.schedule.intervalMinutes,
    timePoints: normalizeTimePointList(config.schedule.timePoints),
    timeZone: (config.schedule.timeZone || '').trim(),
    targetType: config.targetType.trim(),
    workers: config.workers,
    deleteWorkers: config.deleteWorkers,
    timeout: config.timeout,
    retries: config.retries,
    userAgent: config.userAgent.trim(),
    usedPercentThreshold: config.usedPercentThreshold,
    sampleSize: config.sampleSize,
    autoActionMode: config.autoActionMode,
    autoRecoverEnabled: config.autoRecoverEnabled,
  };
}

function configsEquivalent(
  current: NormalizedServerCodexInspectionConfig,
  next: NormalizedServerCodexInspectionConfig
) {
  return JSON.stringify(getComparableConfig(current)) === JSON.stringify(getComparableConfig(next));
}

function resolveActionLabel(action: string, t: ReturnType<typeof useTranslation>['t']) {
  if (
    action === 'delete' ||
    action === 'disable' ||
    action === 'enable' ||
    action === 'reauth' ||
    action === 'keep'
  ) {
    return formatActionLabel(action, t);
  }
  return action || t('common.not_set');
}

function formatServerActionStatusLabel(
  item: CodexInspectionResult,
  t: ReturnType<typeof useTranslation>['t']
) {
  const status = normalizeServerCodexInspectionActionStatus(item);
  if (status === 'success') {
    return t('monitoring.server_codex_inspection_action_status_success', {
      action: resolveActionLabel(item.executedAction || item.action, t),
    });
  }
  if (status === 'failed') {
    return t('monitoring.server_codex_inspection_action_status_failed');
  }
  if (status === 'skipped') {
    return t('monitoring.server_codex_inspection_action_status_skipped');
  }
  if (status === 'needs_review') {
    return t('monitoring.server_codex_inspection_action_status_needs_review');
  }
  if (status === 'pending') {
    return t('monitoring.server_codex_inspection_action_status_pending');
  }
  return '';
}

function formatServerResultStateToken(
  value: string | undefined,
  t: ReturnType<typeof useTranslation>['t']
) {
  const normalized = (value ?? '').trim().toLowerCase();
  if (!normalized) return '';
  if (normalized === 'active') return t('monitoring.state_active');
  if (normalized === 'disabled') return t('monitoring.state_disabled');
  if (normalized === 'inactive') return t('monitoring.state_inactive');
  if (normalized === 'enabled') return t('monitoring.codex_inspection_state_enabled');
  return value?.trim() ?? '';
}

function formatServerResultStateDetail(
  item: CodexInspectionResult,
  t: ReturnType<typeof useTranslation>['t']
) {
  const errorText = item.actionError || item.errorDetail || item.error;
  if (errorText) return errorText;

  return (
    formatServerResultStateToken(item.status, t) ||
    formatServerResultStateToken(item.state, t) ||
    '--'
  );
}

function normalizeServerResultAction(action: string): CodexInspectionAction {
  if (
    action === 'delete' ||
    action === 'disable' ||
    action === 'enable' ||
    action === 'reauth' ||
    action === 'keep'
  ) {
    return action;
  }
  return 'keep';
}

function formatObservedHeaderRecoverAt(value: number | null, locale: string) {
  if (!value || !Number.isFinite(value)) return '';
  return new Date(value).toLocaleString(locale, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
}

function buildObservedHeaderEvidence(
  snapshot: UsageHeaderSnapshot | undefined,
  locale: string,
  t: ReturnType<typeof useTranslation>['t']
) {
  if (!snapshot) return [];
  const evidence: string[] = [];
  const quotaParts = [
    getHeaderSnapshotPlanType(snapshot),
    (() => {
      const usedPercent = getHeaderSnapshotUsedPercent(snapshot);
      return typeof usedPercent === 'number' && Number.isFinite(usedPercent)
        ? formatPercent(usedPercent / 100)
        : '';
    })(),
    (() => {
      const recoverAt = formatObservedHeaderRecoverAt(
        getHeaderSnapshotRecoverAtMs(snapshot),
        locale
      );
      return recoverAt ? `${t('monitoring.header_recover_at')} ${recoverAt}` : '';
    })(),
  ].filter(Boolean);
  if (quotaParts.length > 0) {
    evidence.push(`${t('monitoring.header_quota')}: ${quotaParts.join(' / ')}`);
  }

  const errorParts = [
    getHeaderSnapshotErrorKind(snapshot),
    getHeaderSnapshotErrorCode(snapshot),
  ].filter(Boolean);
  if (errorParts.length > 0) {
    evidence.push(`${t('monitoring.header_error')}: ${errorParts.join(' / ')}`);
  }

  const traceId = getHeaderSnapshotTraceId(snapshot);
  if (traceId) {
    evidence.push(`${t('monitoring.header_trace')}: ${traceId}`);
  }
  return evidence;
}

function toServerResultItem(
  item: CodexInspectionResult,
  t: ReturnType<typeof useTranslation>['t'],
  snapshot: UsageHeaderSnapshot | undefined,
  locale: string
): CodexInspectionResultItem {
  const actionStatusLabel = formatServerActionStatusLabel(item, t);
  const actionReason = item.actionReason?.startsWith('monitoring.')
    ? t(item.actionReason)
    : item.actionReason;
  const reasonParts = [actionReason, actionStatusLabel].filter(Boolean);
  const observedHeaderEvidence = buildObservedHeaderEvidence(snapshot, locale, t);
  return {
    key: `server-${item.id || item.accountKey}`,
    fileName: item.fileName,
    displayAccount: item.displayAccount,
    authIndex: item.authIndex ?? null,
    accountId: item.accountId ?? null,
    provider: item.provider,
    disabled: item.disabled,
    autoRecoverOwned: item.autoRecoverEligible === true,
    status: item.status ?? '',
    state: item.state ?? '',
    raw: item as unknown as CodexInspectionResultItem['raw'],
    action: normalizeServerResultAction(item.action),
    actionReason: reasonParts.join(' · '),
    statusCode: item.statusCode ?? null,
    usedPercent: item.usedPercent ?? null,
    isQuota: item.isQuota,
    autoRecoverEligible: item.autoRecoverEligible === true,
    error: item.error ?? '',
    planType: item.planType ?? null,
    quotaWindows: item.quotaWindows?.map((window) => ({
      id: window.id,
      labelKey: window.labelKey,
      labelParams: window.labelParams,
      usedPercent: window.usedPercent ?? null,
      resetLabel: window.resetLabel ?? '',
      limitWindowSeconds: window.limitWindowSeconds ?? null,
    })),
    errorKind: item.errorKind,
    errorDetail: item.actionError || item.errorDetail || '',
    actionHandled: item.action === 'reauth' && !isPendingServerReauthResult(item),
    observedHeaderEvidence,
    observedHeaderAtMs: snapshot?.timestamp_ms ?? null,
  };
}

function countServerResultActions(items: CodexInspectionResult[]) {
  const counts = {
    delete: 0,
    disable: 0,
    enable: 0,
  };
  items.forEach((item) => {
    if (item.action === 'delete') counts.delete += 1;
    if (item.action === 'disable') counts.disable += 1;
    if (item.action === 'enable') counts.enable += 1;
  });
  return counts;
}

function getServerActionIcon(action: string) {
  if (action === 'delete') return IconTrash2;
  if (action === 'disable') return IconShield;
  return IconRefreshCw;
}

function getUsageServiceDisplayError(error: unknown, t: ReturnType<typeof useTranslation>['t']) {
  const code = getUsageServiceErrorCode(error);
  if (code) {
    return t(`usage_service_errors.${code}`, {
      defaultValue: t('usage_service_errors.request_failed'),
    });
  }
  if (error instanceof Error && error.message) return error.message;
  return t('usage_service_errors.request_failed');
}

function formatServiceHost(base: string): string {
  if (!base) return '';
  try {
    const url = new URL(base);
    return url.host;
  } catch {
    return base;
  }
}

export function ServerCodexInspectionPage() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const managementKey = useAuthStore((state) => state.managementKey);
  const featureAvailability = usePanelFeatureAvailability();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);

  const [serviceBase, setServiceBase] = useState('');
  const [managerConfig, setManagerConfig] = useState<ManagerConfig | null>(null);
  const [draft, setDraft] = useState<ServerCodexInspectionDraft>(() => toDraft(null));
  const [runs, setRuns] = useState<CodexInspectionRun[]>([]);
  const [detail, setDetail] = useState<CodexInspectionRunDetail | null>(null);
  const [headerSnapshots, setHeaderSnapshots] = useState<UsageHeaderSnapshot[]>([]);
  const [selectedRunId, setSelectedRunId] = useState<number | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState('');
  const [logsCollapsed, setLogsCollapsed] = useState(false);
  const [actionFilter, setActionFilter] = useState<ActionFilter>('all');
  const [handlingFilter, setHandlingFilter] = useState<HandlingFilter>('all');
  const [resultPage, setResultPage] = useState(1);
  const [resultPageSize, setResultPageSize] = useState<number>(
    CODEX_INSPECTION_RESULT_PAGE_SIZE_OPTIONS[0]
  );
  const [logLevelFilter, setLogLevelFilter] = useState<
    'all' | 'info' | 'success' | 'warning' | 'error'
  >('all');
  const [executingResultIds, setExecutingResultIds] = useState<Set<number>>(() => new Set());
  const [executingAllActions, setExecutingAllActions] = useState(false);
  const [configDrawerOpen, setConfigDrawerOpen] = useState(false);
  const [configFocusField, setConfigFocusField] = useState<string | null>(null);
  const [codexReauthTarget, setCodexReauthTarget] = useState<CodexReauthTarget | null>(null);
  const refreshInFlightRef = useRef(false);
  const actionInFlightRef = useRef(false);

  const loadRunDetail = useCallback(
    async (base: string, id: number) => {
      const nextDetail = await usageServiceApi.getCodexInspectionRun(base, managementKey, id);
      setDetail(nextDetail);
      setSelectedRunId(nextDetail.run.id);
      return nextDetail;
    },
    [managementKey]
  );

  const loadPageData = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const resolvedBase = featureAvailability.managerServiceBase;
      if (!resolvedBase || !featureAvailability.serverCodexInspectionAvailable) {
        throw new Error(t('monitoring.server_codex_inspection_service_unavailable'));
      }
      const response = await usageServiceApi.getManagerConfig(resolvedBase, managementKey);
      const responseConfig = response.config;

      setServiceBase(resolvedBase);
      setManagerConfig(responseConfig);
      setDraft(toDraft(responseConfig.codexInspection));

      const runsResponse = await usageServiceApi.listCodexInspectionRuns(
        resolvedBase,
        managementKey,
        RUNS_LIMIT
      );
      const snapshotsResponse = await monitoringAnalyticsApi
        .getHeaderSnapshots(resolvedBase, managementKey, { days: 30, limit: 1000 })
        .catch(() => ({ items: [] as UsageHeaderSnapshot[] }));
      setHeaderSnapshots(snapshotsResponse.items ?? []);
      setRuns(runsResponse.items);
      const nextSelectedId = runsResponse.items[0]?.id;
      if (nextSelectedId) {
        await loadRunDetail(resolvedBase, nextSelectedId);
      } else {
        setDetail(null);
        setSelectedRunId(null);
      }
    } catch (error: unknown) {
      setError(getUsageServiceDisplayError(error, t));
      setRuns([]);
      setDetail(null);
      setHeaderSnapshots([]);
      setSelectedRunId(null);
    } finally {
      setLoading(false);
    }
  }, [
    featureAvailability.managerServiceBase,
    featureAvailability.serverCodexInspectionAvailable,
    loadRunDetail,
    managementKey,
    t,
  ]);

  useEffect(() => {
    if (featureAvailability.checking) {
      return;
    }
    if (!managementKey) {
      setLoading(false);
      setError(t('monitoring.server_codex_inspection_connection_required'));
      return;
    }
    if (!featureAvailability.serverCodexInspectionAvailable) {
      setLoading(false);
      setError(t('monitoring.server_codex_inspection_service_unavailable'));
      return;
    }
    void loadPageData();
  }, [
    featureAvailability.checking,
    featureAvailability.serverCodexInspectionAvailable,
    loadPageData,
    managementKey,
    t,
  ]);

  const selectedConfig = useMemo(
    () => resolveServerCodexConfig(managerConfig?.codexInspection),
    [managerConfig?.codexInspection]
  );
  const draftConfig = useMemo(() => createConfigFromDraft(draft, t), [draft, t]);
  const normalizedDraftConfig = useMemo(
    () => (draftConfig ? resolveServerCodexConfig(draftConfig) : null),
    [draftConfig]
  );
  const hasUnsavedChanges = Boolean(
    managerConfig &&
    (!normalizedDraftConfig || !configsEquivalent(selectedConfig, normalizedDraftConfig))
  );
  const savedScheduleLabel = formatSchedule(selectedConfig, t);
  const hasRunningRun =
    runs.some((run) => run.status === 'running') || detail?.run.status === 'running';
  const latestRun = runs[0] ?? null;
  const activeRun = detail?.run ?? latestRun;
  const activeTone = getRunTone(activeRun);
  const actionCounts = activeRun
    ? activeRun.deleteCount +
      activeRun.disableCount +
      activeRun.enableCount +
      (activeRun.reauthCount ?? 0)
    : 0;

  const resultRows = useMemo(() => detail?.results ?? [], [detail?.results]);
  const headerSnapshotLookup = useMemo(
    () => buildUsageHeaderSnapshotLookup(headerSnapshots),
    [headerSnapshots]
  );
  const resultItems = useMemo(
    () =>
      resultRows.map((item) =>
        toServerResultItem(
          item,
          t,
          getUsageHeaderSnapshotForIdentity(headerSnapshotLookup, {
            fileName: item.fileName,
            authIndex: item.authIndex,
            account: item.accountId || item.displayAccount,
          }),
          i18n.language
        )
      ),
    [headerSnapshotLookup, i18n.language, resultRows, t]
  );
  const resultByKey = useMemo(() => {
    const map = new Map<string, CodexInspectionResult>();
    resultRows.forEach((item) => {
      map.set(`server-${item.id || item.accountKey}`, item);
    });
    return map;
  }, [resultRows]);
  const filteredResultRows = useMemo(
    () => filterInspectionResults(resultItems, handlingFilter, actionFilter),
    [actionFilter, handlingFilter, resultItems]
  );
  const resultPagination = useMemo(
    () => buildCodexInspectionPaginationState(filteredResultRows, resultPage, resultPageSize),
    [filteredResultRows, resultPage, resultPageSize]
  );

  useEffect(() => {
    setResultPage(1);
  }, [actionFilter, handlingFilter, detail?.run.id]);

  useEffect(() => {
    if (resultPage === resultPagination.currentPage) return;
    setResultPage(resultPagination.currentPage);
  }, [resultPage, resultPagination.currentPage]);

  const handleResultPageSizeChange = useCallback((pageSize: number) => {
    setResultPageSize(pageSize);
    setResultPage(1);
  }, []);

  const scheduleOptions = useMemo(
    () => [
      { value: 'interval', label: t('monitoring.server_codex_inspection_schedule_interval') },
      { value: 'time_points', label: t('monitoring.server_codex_inspection_schedule_time_points') },
    ],
    [t]
  );

  const browserTimeZone = useMemo(detectBrowserTimeZone, []);
  const timeZoneOptions = useMemo(() => {
    const seen = new Set<string>();
    const options: SelectOption[] = [
      { value: '', label: t('monitoring.server_codex_inspection_time_zone_server_default') },
    ];
    const push = (value: string, label: string) => {
      if (!value || seen.has(value)) return;
      seen.add(value);
      options.push({ value, label });
    };
    if (browserTimeZone && browserTimeZone !== 'UTC') {
      push(
        browserTimeZone,
        t('monitoring.server_codex_inspection_time_zone_browser', { tz: browserTimeZone })
      );
    }
    COMMON_TIME_ZONES.forEach((zone) => push(zone, zone));
    if (draft.timeZone && !seen.has(draft.timeZone)) {
      push(draft.timeZone, draft.timeZone);
    }
    return options;
  }, [browserTimeZone, draft.timeZone, t]);

  const updateDraft = <K extends keyof ServerCodexInspectionDraft>(
    key: K,
    value: ServerCodexInspectionDraft[K]
  ) => {
    setDraft((previous) => ({ ...previous, [key]: value }));
  };

  const refreshRuns = useCallback(
    async (options?: { silent?: boolean }) => {
      if (refreshInFlightRef.current) return;
      refreshInFlightRef.current = true;
      const silent = options?.silent ?? false;
      if (!serviceBase) {
        try {
          await loadPageData();
        } finally {
          refreshInFlightRef.current = false;
        }
        return;
      }
      if (!silent) {
        setLoading(true);
        setError('');
      }
      try {
        const response = await usageServiceApi.listCodexInspectionRuns(
          serviceBase,
          managementKey,
          RUNS_LIMIT
        );
        setRuns(response.items);
        const selectionStillValid =
          selectedRunId != null && response.items.some((run) => run.id === selectedRunId);
        if (selectionStillValid) {
          // 静默轮询时保留用户正在查看的历史详情,避免每 30s 重建详情导致结果表/日志
          // 重渲染、打断操作;但正在运行的巡检或尚无详情时仍需刷新以获取最新进度。
          const watchingRunning = detail?.run.status === 'running';
          if (!silent || !detail || watchingRunning) {
            await loadRunDetail(serviceBase, selectedRunId);
          }
        } else {
          const fallbackId = response.items[0]?.id;
          if (fallbackId) {
            await loadRunDetail(serviceBase, fallbackId);
          } else {
            setDetail(null);
            setSelectedRunId(null);
          }
        }
      } catch (error: unknown) {
        if (!silent) setError(getUsageServiceDisplayError(error, t));
      } finally {
        if (!silent) setLoading(false);
        refreshInFlightRef.current = false;
      }
    },
    [detail, loadPageData, loadRunDetail, managementKey, selectedRunId, serviceBase, t]
  );

  useEffect(() => {
    if (!serviceBase || (!selectedConfig.enabled && !hasRunningRun)) return;
    const timer = window.setInterval(() => {
      if (saving || running || actionInFlightRef.current) return;
      void refreshRuns({ silent: true });
    }, 30_000);

    return () => window.clearInterval(timer);
  }, [hasRunningRun, refreshRuns, running, saving, selectedConfig.enabled, serviceBase]);

  const handleSave = async () => {
    if (!serviceBase || !managerConfig) {
      showNotification(t('monitoring.server_codex_inspection_service_unavailable'), 'warning');
      return;
    }
    const codexInspection = createConfigFromDraft(draft, t);
    if (!codexInspection) {
      showNotification(t('monitoring.server_codex_inspection_config_invalid'), 'warning');
      return;
    }
    setSaving(true);
    try {
      const response = await usageServiceApi.saveManagerConfig(
        serviceBase,
        {
          ...managerConfig,
          codexInspection,
        },
        managementKey
      );
      setManagerConfig(response.config);
      setDraft(toDraft(response.config.codexInspection));
      showNotification(t('monitoring.server_codex_inspection_config_saved'), 'success');
      setConfigDrawerOpen(false);
    } catch (error: unknown) {
      showNotification(
        `${t('notification.save_failed')}: ${getUsageServiceDisplayError(error, t)}`,
        'error'
      );
    } finally {
      setSaving(false);
    }
  };

  const handleCloseConfigDrawer = useCallback(() => {
    if (hasUnsavedChanges) {
      showConfirmation({
        title: t('monitoring.server_codex_inspection_close_confirm_title'),
        message: t('monitoring.server_codex_inspection_close_unsaved_hint'),
        confirmText: t('monitoring.server_codex_inspection_discard'),
        cancelText: t('common.cancel'),
        variant: 'danger',
        onConfirm: () => {
          setDraft(toDraft(managerConfig?.codexInspection));
          setConfigDrawerOpen(false);
        },
      });
      return;
    }
    setConfigDrawerOpen(false);
  }, [hasUnsavedChanges, managerConfig, showConfirmation, t]);

  const openConfigDrawer = useCallback((field?: string) => {
    setConfigFocusField(field ?? null);
    setConfigDrawerOpen(true);
  }, []);

  const executeServerRun = useCallback(async () => {
    if (!serviceBase) {
      showNotification(t('monitoring.server_codex_inspection_service_unavailable'), 'warning');
      return;
    }
    setRunning(true);
    setError('');
    try {
      const nextDetail = await usageServiceApi.runCodexInspection(serviceBase, managementKey);
      setDetail(nextDetail);
      setSelectedRunId(nextDetail.run.id);
      const response = await usageServiceApi.listCodexInspectionRuns(
        serviceBase,
        managementKey,
        RUNS_LIMIT
      );
      setRuns(response.items);
      showNotification(t('monitoring.server_codex_inspection_run_success'), 'success');
    } catch (error: unknown) {
      const message = getUsageServiceDisplayError(error, t);
      showNotification(
        `${t('monitoring.server_codex_inspection_run_failed')}: ${message}`,
        'error'
      );
      await refreshRuns();
    } finally {
      setRunning(false);
    }
  }, [managementKey, refreshRuns, serviceBase, showNotification, t]);

  const handleRunNow = () => {
    showConfirmation({
      title: t('monitoring.server_codex_inspection_run_confirm_title'),
      message: t('monitoring.server_codex_inspection_run_confirm_body'),
      confirmText: t('monitoring.server_codex_inspection_run_now'),
      cancelText: t('common.cancel'),
      variant: selectedConfig.autoActionMode === 'delete' ? 'danger' : 'primary',
      onConfirm: executeServerRun,
    });
  };

  const executeServerActions = useCallback(
    async (
      targets: CodexInspectionResult[],
      scope: 'single' | 'bulk',
      overrideAction?: 'delete'
    ) => {
      if (!serviceBase || !detail) {
        showNotification(t('monitoring.server_codex_inspection_service_unavailable'), 'warning');
        return;
      }
      const resultIds = Array.from(
        new Set(
          targets
            .filter((item) =>
              overrideAction === 'delete'
                ? item.action === 'reauth' && item.id > 0
                : isActionableServerCodexInspectionResult(item)
            )
            .map((item) => item.id)
        )
      );
      if (resultIds.length === 0) {
        showNotification(t('monitoring.server_codex_inspection_no_actions'), 'warning');
        return;
      }
      setExecutingResultIds(new Set(resultIds));
      setExecutingAllActions(scope === 'bulk');
      actionInFlightRef.current = true;
      try {
        const response = await usageServiceApi.executeCodexInspectionActions(
          serviceBase,
          managementKey,
          detail.run.id,
          resultIds,
          overrideAction === 'delete'
            ? resultIds.map((resultId) => ({ resultId, action: 'delete' as const }))
            : []
        );
        setDetail(response.detail);
        setSelectedRunId(response.detail.run.id);

        const runsResponse = await usageServiceApi.listCodexInspectionRuns(
          serviceBase,
          managementKey,
          RUNS_LIMIT
        );
        setRuns(runsResponse.items);

        const failed = response.outcomes.filter((item) => !item.success);
        if (failed.length > 0) {
          showNotification(
            t('monitoring.server_codex_inspection_execute_partial', {
              failed: failed.length,
              total: response.outcomes.length,
            }),
            'warning'
          );
        } else {
          showNotification(t('monitoring.server_codex_inspection_execute_success'), 'success');
        }
      } catch (error: unknown) {
        showNotification(
          `${t('monitoring.server_codex_inspection_execute_failed')}: ${getUsageServiceDisplayError(error, t)}`,
          'error'
        );
      } finally {
        actionInFlightRef.current = false;
        setExecutingResultIds(new Set());
        setExecutingAllActions(false);
      }
    },
    [detail, managementKey, serviceBase, showNotification, t]
  );

  const handleExecuteServerActions = useCallback(
    (targets: CodexInspectionResult[], scope: 'single' | 'bulk') => {
      if (targets.length === 0) return;
      const counts = countServerResultActions(targets);
      const hasDelete = targets.some((item) => item.action === 'delete');
      const first = targets[0];
      showConfirmation({
        title:
          scope === 'bulk'
            ? t('monitoring.server_codex_inspection_execute_confirm_title')
            : t('monitoring.server_codex_inspection_execute_single_title'),
        message:
          scope === 'bulk'
            ? t('monitoring.server_codex_inspection_execute_confirm_body', {
                total: targets.length,
                delete: counts.delete,
                disable: counts.disable,
                enable: counts.enable,
              })
            : t('monitoring.server_codex_inspection_execute_single_body', {
                account: first.displayAccount,
                action: resolveActionLabel(first.action, t),
              }),
        confirmText:
          scope === 'bulk'
            ? t('monitoring.server_codex_inspection_execute_all')
            : resolveActionLabel(first.action, t),
        cancelText: t('common.cancel'),
        variant: hasDelete ? 'danger' : 'primary',
        onConfirm: () => executeServerActions(targets, scope),
      });
    },
    [executeServerActions, showConfirmation, t]
  );

  const handleOpenCodexReauth = useCallback((item: CodexInspectionResult) => {
    if (item.provider === 'xai') {
      navigate('/oauth#oauth-provider-xai');
      return;
    }
    setCodexReauthTarget({
      account: item.displayAccount || item.accountId || item.fileName,
      fileName: item.fileName,
      authIndex: item.authIndex ?? null,
      accountId: item.accountId ?? null,
    });
  }, [navigate]);

  const handleDeleteServerReauth = useCallback(
    (targets: CodexInspectionResult[], scope: 'single' | 'bulk') => {
      if (targets.length === 0) return;
      const first = targets[0];
      showConfirmation({
        title:
          scope === 'bulk'
            ? t('monitoring.codex_inspection_delete_reauth_confirm_title')
            : t('monitoring.codex_inspection_delete_reauth_single_title'),
        message:
          scope === 'bulk'
            ? t('monitoring.codex_inspection_delete_reauth_confirm_body', {
                count: targets.length,
              })
            : t('monitoring.codex_inspection_delete_reauth_single_body', {
                account: first.displayAccount,
                file: first.fileName,
              }),
        confirmText: t('monitoring.codex_inspection_action_delete'),
        cancelText: t('common.cancel'),
        variant: 'danger',
        onConfirm: () => executeServerActions(targets, scope, 'delete'),
      });
    },
    [executeServerActions, showConfirmation, t]
  );

  const handleCodexReauthSuccess = useCallback(async () => {
    await refreshRuns({ silent: true });
    showNotification(t('codex_reauth.rerun_hint'), 'success');
  }, [refreshRuns, showNotification, t]);

  const handleSelectRun = async (runID: number) => {
    if (!serviceBase || runID === selectedRunId) return;
    setSelectedRunId(runID);
    try {
      await loadRunDetail(serviceBase, runID);
    } catch (error: unknown) {
      showNotification(getUsageServiceDisplayError(error, t), 'error');
    }
  };

  const renderStatusPanel = () => {
    const lastRunTime = activeRun?.finishedAtMs
      ? new Date(activeRun.finishedAtMs).toLocaleTimeString(i18n.language)
      : '--';
    const durationLabel = formatDuration(activeRun, t);
    const serviceHost = formatServiceHost(serviceBase);
    const summaryBlankValue = '--';
    const configOverviewItems = buildConfigOverviewItems(selectedConfig, {
      mode: 'server',
      t,
      scheduleEnabled: selectedConfig.enabled,
      scheduleLabel: savedScheduleLabel,
    });

    return (
      <Panel className={styles.statusPanel}>
        <div className={styles.statusBar}>
          <div className={styles.statusInfo}>
            <span className={`${styles.statusBadge} ${statusToneClass[activeTone]}`}>
              <span className={styles.statusDot} aria-hidden="true" />
              {getRunStatusLabel(activeRun, t)}
            </span>
            <span
              className={`${styles.statusBadge} ${
                selectedConfig.enabled ? statusToneClass.good : statusToneClass.idle
              }`}
            >
              <span className={styles.statusDot} aria-hidden="true" />
              {selectedConfig.enabled
                ? t('monitoring.server_codex_inspection_schedule_enabled')
                : t('monitoring.server_codex_inspection_schedule_disabled')}
            </span>
            <div className={styles.statusMeta}>
              <span>
                {t('monitoring.server_codex_inspection_last_run')}: {lastRunTime}
                {activeRun?.finishedAtMs ? ` · ${durationLabel}` : ''}
              </span>
              {serviceHost ? (
                <span className={styles.statusMetaHost} title={serviceBase}>
                  {serviceHost}
                </span>
              ) : null}
            </div>
          </div>
          <div className={styles.statusActions}>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void refreshRuns()}
              loading={loading}
            >
              {t('common.refresh')}
            </Button>
            <Button
              size="sm"
              onClick={handleRunNow}
              loading={running}
              disabled={!serviceBase || running}
            >
              {t('monitoring.server_codex_inspection_run_now')}
            </Button>
          </div>
        </div>

        <details className={styles.infoNote}>
          <summary>{t('monitoring.server_codex_inspection_info_summary')}</summary>
          <ul className={styles.infoNoteList}>
            <li>
              <strong>{t('monitoring.server_codex_inspection_worker_poll')}:</strong>{' '}
              {t('monitoring.server_codex_inspection_effect_hint')}
            </li>
            <li>
              <strong>{t('monitoring.server_codex_inspection_time_basis')}:</strong>{' '}
              {t('monitoring.server_codex_inspection_server_time_hint')}
            </li>
            <li>
              <strong>{t('monitoring.server_codex_inspection_history_refresh')}:</strong>{' '}
              {t('monitoring.server_codex_inspection_auto_refresh_hint')}
            </li>
          </ul>
        </details>

        <CodexInspectionConfigOverview
          title={t('monitoring.codex_inspection_config_overview_title')}
          editLabel={t('monitoring.codex_inspection_config_overview_edit')}
          ariaLabel={t('monitoring.server_codex_inspection_config_summary_title')}
          items={configOverviewItems}
          onEdit={openConfigDrawer}
        />

        <div className={styles.summaryGrid}>
          {[
            {
              key: 'probe-total',
              label: t('monitoring.codex_inspection_total_accounts'),
              value: activeRun ? String(activeRun.probeSetCount) : summaryBlankValue,
              meta: t('monitoring.server_codex_inspection_total_files', {
                count: activeRun?.totalFiles ?? 0,
              }),
              Icon: IconInbox,
              accent: 'blue' as const,
            },
            {
              key: 'sampled',
              label: t('monitoring.codex_inspection_sampled_accounts'),
              value: activeRun ? String(activeRun.sampledCount) : summaryBlankValue,
              meta: formatTrigger(activeRun, t),
              Icon: IconChartLine,
              accent: 'cyan' as const,
            },
            {
              key: 'delete',
              label: t('monitoring.codex_inspection_delete_count'),
              value: activeRun ? String(activeRun.deleteCount) : summaryBlankValue,
              meta: t('monitoring.server_codex_inspection_action_total_value', {
                count: actionCounts,
              }),
              tone: 'bad',
              Icon: IconTrash2,
              accent: 'red' as const,
            },
            {
              key: 'disable',
              label: t('monitoring.codex_inspection_disable_count'),
              value: activeRun ? String(activeRun.disableCount) : summaryBlankValue,
              meta: `${t('monitoring.codex_inspection_threshold')}: ${selectedConfig.usedPercentThreshold}%`,
              tone: 'warn',
              Icon: IconShield,
              accent: 'amber' as const,
            },
            {
              key: 'enable',
              label: t('monitoring.codex_inspection_enable_count'),
              value: activeRun ? String(activeRun.enableCount) : summaryBlankValue,
              meta: t('monitoring.server_codex_inspection_keep_count', {
                count: activeRun?.keepCount ?? 0,
              }),
              tone: 'good',
              Icon: IconCheck,
              accent: 'green' as const,
            },
            {
              key: 'reauth',
              label: t('monitoring.codex_inspection_reauth_count'),
              value: activeRun ? String(activeRun.reauthCount) : summaryBlankValue,
              meta: t('monitoring.codex_inspection_action_reauth'),
              tone: 'info',
              Icon: IconRefreshCw,
              accent: 'violet' as const,
            },
          ].map((card) => {
            const SummaryIcon = card.Icon;
            return (
              <div
                key={card.key}
                className={[
                  styles.summaryCard,
                  summaryAccentClassMap[card.accent],
                  card.tone ? styles[`tone-${card.tone}`] : '',
                ]
                  .filter(Boolean)
                  .join(' ')}
              >
                <div className={styles.summaryHeader}>
                  <span className={styles.summaryIcon}>
                    <SummaryIcon size={18} />
                  </span>
                  <span className={styles.summaryLabel} title={card.label}>
                    {card.label}
                  </span>
                </div>
                <div className={styles.summaryBody}>
                  <strong className={styles.summaryValue}>{card.value}</strong>
                  <span className={styles.summaryMeta} title={card.meta}>
                    {card.meta}
                  </span>
                </div>
              </div>
            );
          })}
        </div>
      </Panel>
    );
  };

  const handleDiscard = () => {
    if (!managerConfig) return;
    setDraft(toDraft(managerConfig.codexInspection));
  };

  const renderConfigDrawer = () => {
    const fieldErrors = validateInspectionConfigFields(draft, t);

    return (
      <InspectionConfigDrawer
        open={configDrawerOpen}
        title={t('monitoring.server_codex_inspection_config_title')}
        description={t('monitoring.server_codex_inspection_config_desc')}
        closeLabel={t('common.close')}
        focusField={configFocusField}
        onClose={handleCloseConfigDrawer}
        footer={
          <>
            <div className={styles.configDrawerStatus}>
              {hasUnsavedChanges ? (
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
                onClick={handleDiscard}
                disabled={saving || !hasUnsavedChanges}
              >
                {t('monitoring.server_codex_inspection_discard')}
              </Button>
              <Button
                size="sm"
                onClick={handleSave}
                loading={saving}
                disabled={loading || saving || !hasUnsavedChanges}
              >
                {t('monitoring.server_codex_inspection_save_apply')}
              </Button>
            </div>
          </>
        }
      >
        <section className={styles.configSection} id="schedule">
          <header className={styles.configSectionHeader}>
            <span>{t('monitoring.server_codex_inspection_config_group_schedule')}</span>
          </header>
          <div className={styles.serverConfigGrid}>
            <div className={`${styles.serverField} ${styles.serverFieldWide}`}>
              <ToggleSwitch
                checked={draft.enabled}
                onChange={(value) => updateDraft('enabled', value)}
                label={t('monitoring.server_codex_inspection_enable_schedule')}
              />
            </div>

            <div className={`${styles.serverField} ${styles.serverFieldWide}`}>
              <span className={styles.serverFieldLabel}>
                {t('monitoring.server_codex_inspection_schedule_mode')}
              </span>
              <div
                className={styles.scheduleSegmented}
                role="tablist"
                aria-label={t('monitoring.server_codex_inspection_schedule_mode')}
              >
                {scheduleOptions.map((opt) => {
                  const active = draft.scheduleMode === opt.value;
                  return (
                    <button
                      key={opt.value}
                      type="button"
                      role="tab"
                      aria-selected={active}
                      className={`${styles.scheduleSegmentButton} ${active ? styles.scheduleSegmentButtonActive : ''}`}
                      onClick={() =>
                        updateDraft(
                          'scheduleMode',
                          isScheduleMode(opt.value)
                            ? opt.value
                            : DEFAULT_SERVER_CODEX_CONFIG.schedule.mode
                        )
                      }
                    >
                      {opt.label}
                    </button>
                  );
                })}
              </div>
            </div>

            {draft.scheduleMode === 'interval' ? (
              <div className={styles.serverField}>
                <Input
                  id="intervalMinutes"
                  label={t('monitoring.server_codex_inspection_interval_minutes')}
                  type="number"
                  min="1"
                  value={draft.intervalMinutes}
                  onChange={(event) => updateDraft('intervalMinutes', event.target.value)}
                />
              </div>
            ) : (
              <>
                <div className={`${styles.serverField} ${styles.serverFieldHalf}`}>
                  <Input
                    id="timePoints"
                    label={t('monitoring.server_codex_inspection_time_points')}
                    value={draft.timePoints}
                    onChange={(event) => updateDraft('timePoints', event.target.value)}
                    placeholder="09:00, 13:30, 22:00"
                    hint={t('monitoring.server_codex_inspection_time_points_hint')}
                  />
                </div>
                <div className={`${styles.serverField} ${styles.serverFieldHalf}`}>
                  <span className={styles.serverFieldLabel}>
                    {t('monitoring.server_codex_inspection_time_zone')}
                  </span>
                  <Select
                    value={draft.timeZone}
                    options={timeZoneOptions}
                    onChange={(value) => updateDraft('timeZone', value)}
                    ariaLabel={t('monitoring.server_codex_inspection_time_zone')}
                  />
                </div>
              </>
            )}
          </div>
        </section>

        <InspectionConfigFields
          draft={draft}
          errors={fieldErrors}
          t={t}
          onFieldChange={(field, value) => updateDraft(field, value)}
          onAutoActionModeChange={(value) => updateDraft('autoActionMode', value)}
          onAutoRecoverEnabledChange={(value) => updateDraft('autoRecoverEnabled', value)}
        />
      </InspectionConfigDrawer>
    );
  };

  const renderRunsPanel = () => (
    <Panel
      title={t('monitoring.server_codex_inspection_history_title')}
      subtitle={t('monitoring.server_codex_inspection_history_desc')}
    >
      {runs.length > 0 ? (
        <div
          className={styles.runHistoryList}
          role="tablist"
          aria-label={t('monitoring.server_codex_inspection_history_title')}
        >
          {runs.map((run) => {
            const tone = getRunTone(run);
            const selected = run.id === selectedRunId;
            const ariaLabel = `${getRunStatusLabel(run, t)} · #${run.id} · ${formatTimestamp(run.startedAtMs, i18n.language)}`;
            return (
              <button
                type="button"
                key={run.id}
                role="tab"
                aria-selected={selected}
                aria-label={ariaLabel}
                className={`${styles.runHistoryCard} ${selected ? styles.runHistoryCardActive : ''}`}
                onClick={() => void handleSelectRun(run.id)}
              >
                <div className={styles.runHistoryCardHead}>
                  <span className={`${styles.statusBadge} ${statusToneClass[tone]}`}>
                    <span className={styles.statusDot} aria-hidden="true" />
                    {getRunStatusLabel(run, t)}
                  </span>
                  <span className={styles.runHistoryCardId}>#{run.id}</span>
                </div>
                <div className={styles.runHistoryCardMeta}>
                  <span>{formatTimestamp(run.startedAtMs, i18n.language)}</span>
                  <span>
                    {formatTrigger(run, t)} · {t('monitoring.codex_inspection_sampled_accounts')}:{' '}
                    {run.sampledCount}
                  </span>
                </div>
                <div className={styles.runHistoryCardActionPills}>
                  {run.deleteCount > 0 ? (
                    <span
                      className={`${styles.runHistoryCardPill} ${styles.runHistoryCardPillDelete}`}
                    >
                      {t('monitoring.codex_inspection_action_delete')} {run.deleteCount}
                    </span>
                  ) : null}
                  {run.disableCount > 0 ? (
                    <span
                      className={`${styles.runHistoryCardPill} ${styles.runHistoryCardPillDisable}`}
                    >
                      {t('monitoring.codex_inspection_action_disable')} {run.disableCount}
                    </span>
                  ) : null}
                  {run.enableCount > 0 ? (
                    <span
                      className={`${styles.runHistoryCardPill} ${styles.runHistoryCardPillEnable}`}
                    >
                      {t('monitoring.codex_inspection_action_enable')} {run.enableCount}
                    </span>
                  ) : null}
                  {run.reauthCount > 0 ? (
                    <span
                      className={`${styles.runHistoryCardPill} ${styles.runHistoryCardPillReauth}`}
                    >
                      {t('monitoring.codex_inspection_action_reauth')} {run.reauthCount}
                    </span>
                  ) : null}
                  {run.keepCount > 0 ? (
                    <span
                      className={`${styles.runHistoryCardPill} ${styles.runHistoryCardPillKeep}`}
                    >
                      {t('monitoring.codex_inspection_action_keep')} {run.keepCount}
                    </span>
                  ) : null}
                </div>
              </button>
            );
          })}
        </div>
      ) : (
        <div className={styles.emptyBlock}>
          {t('monitoring.server_codex_inspection_history_empty')}
        </div>
      )}
    </Panel>
  );

  const renderResultsPanel = () => {
    const canonicalExecutableIds = getCanonicalServerCodexInspectionActionIds(resultRows);
    const mixedActionIds = getMixedServerCodexInspectionActionIds(resultRows);
    const executableResults = resultRows.filter((item) => canonicalExecutableIds.has(item.id));
    const reauthResults = resultRows.filter(isPendingServerReauthResult);
    const canExecuteActions = detail?.run.status === 'completed';
    const resultsRun = detail?.run ?? null;
    const actionFilterCounts = getActionFilterCounts(resultItems);
    const handlingFilterCounts = countHandlingStates(resultItems);
    const panelResult: CodexInspectionRunResult | null = resultsRun
      ? {
          settings: {
            baseUrl: serviceBase,
            token: '',
            targetType: selectedConfig.targetType,
            workers: selectedConfig.workers,
            deleteWorkers: selectedConfig.deleteWorkers,
            timeout: selectedConfig.timeout,
            retries: selectedConfig.retries,
            userAgent: selectedConfig.userAgent,
            usedPercentThreshold: selectedConfig.usedPercentThreshold,
            sampleSize: selectedConfig.sampleSize,
          },
          files: [],
          results: resultItems,
          summary: {
            totalFiles: resultsRun.totalFiles,
            probeSetCount: resultsRun.probeSetCount,
            sampledCount: resultsRun.sampledCount,
            disabledCount: resultsRun.disabledCount,
            enabledCount: resultsRun.enabledCount,
            deleteCount: resultsRun.deleteCount,
            disableCount: resultsRun.disableCount,
            enableCount: resultsRun.enableCount,
            reauthCount: resultsRun.reauthCount,
            keepCount: resultsRun.keepCount,
            usedPercentThreshold: selectedConfig.usedPercentThreshold,
            sampled: selectedConfig.sampleSize > 0,
            plannedActionPreview: [],
          },
          startedAt: resultsRun.startedAtMs,
          finishedAt: resultsRun.finishedAtMs ?? resultsRun.updatedAtMs,
        }
      : null;

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

    const renderOperation = (item: CodexInspectionResultItem) => {
      const source = resultByKey.get(item.key);
      if (!source) {
        return (
          <span className={styles.primaryReason}>{t('monitoring.codex_inspection_no_action')}</span>
        );
      }

      const actionStatus = normalizeServerCodexInspectionActionStatus(source);
      const detailText = formatServerResultStateDetail(source, t);
      const showDetail =
        detailText &&
        detailText !== '--' &&
        !source.actionError &&
        !source.errorDetail &&
        !source.error;

      return (
        <div className={styles.serverResultOperation}>
          {showDetail ? <span className={styles.primaryReason}>{detailText}</span> : null}
          {canonicalExecutableIds.has(source.id) ? (
            <Button
              size="xs"
              variant={source.action === 'delete' ? 'danger' : 'secondary'}
              loading={executingResultIds.has(source.id)}
              disabled={!canExecuteActions || executingResultIds.size > 0}
              className={styles.serverResultActionButton}
              onClick={() => handleExecuteServerActions([source], 'single')}
            >
              {(() => {
                const ActionIcon = getServerActionIcon(source.action);
                return <ActionIcon size={13} />;
              })()}
              {resolveActionLabel(source.action, t)}
            </Button>
          ) : actionStatus === 'needs_review' || mixedActionIds.has(source.id) ? (
            <span className={styles.primaryReason}>
              {t('monitoring.server_codex_inspection_action_needs_review_hint')}
            </span>
          ) : isActionableServerCodexInspectionResult(source) ? (
            <span className={styles.primaryReason}>
              {t('monitoring.server_codex_inspection_file_level_action_hint')}
            </span>
          ) : isPendingServerReauthResult(source) ? (
            <div className={styles.resultsHeaderActions}>
              <Button
                size="xs"
                variant="secondary"
                className={styles.serverResultActionButton}
                onClick={() => handleOpenCodexReauth(source)}
              >
                <IconRefreshCw size={13} />
                {t(
                  source.provider === 'xai' ? 'auth_login.xai_oauth_button' : 'codex_reauth.button'
                )}
              </Button>
              <Button
                size="xs"
                variant="danger"
                className={styles.serverResultActionButton}
                onClick={() => handleDeleteServerReauth([source], 'single')}
                disabled={!canExecuteActions || executingResultIds.size > 0}
              >
                <IconTrash2 size={13} />
                {t('monitoring.codex_inspection_action_delete')}
              </Button>
            </div>
          ) : source.action === 'keep' ? (
            <span className={styles.primaryReason}>
              {t('monitoring.codex_inspection_no_action')}
            </span>
          ) : null}
        </div>
      );
    };

    return (
      <CodexInspectionResultsPanel
        result={panelResult}
        filteredResults={resultPagination.pageItems}
        suggestedResults={resultItems.filter((item) => item.action !== 'keep')}
        pendingActionCount={executableResults.length}
        manualActionCount={reauthResults.length}
        reauthActionCount={reauthResults.length}
        handlingFilterCounts={handlingFilterCounts}
        filterCounts={actionFilterCounts}
        handlingFilter={handlingFilter}
        actionFilter={actionFilter}
        pagination={resultPagination}
        pageSize={resultPageSize}
        pageSizeOptions={CODEX_INSPECTION_RESULT_PAGE_SIZE_OPTIONS}
        executing={executingAllActions}
        isInspectionInFlight={Boolean(hasRunningRun)}
        t={t}
        title={t('monitoring.codex_inspection_results_title')}
        subtitle={formatResultsDescription(resultsRun, i18n.language, t)}
        stateHeaderLabel={formatResultStateHeader(resultsRun, t)}
        onActionFilterChange={setActionFilter}
        onHandlingFilterChange={setHandlingFilter}
        onPageChange={setResultPage}
        onPageSizeChange={handleResultPageSizeChange}
        onExecutePlanned={() => handleExecuteServerActions(executableResults, 'bulk')}
        onExecuteSingle={() => undefined}
        onReauthAccount={(item) => {
          const source = resultByKey.get(item.key);
          if (source) handleOpenCodexReauth(source);
        }}
        onDeleteReauthPlanned={
          reauthResults.length > 0
            ? () => handleDeleteServerReauth(reauthResults, 'bulk')
            : undefined
        }
        filterLabel={filterLabel}
        handlingFilterLabel={handlingFilterLabel}
        renderOperation={renderOperation}
      />
    );
  };

  const handleCopyLogs = useCallback(
    async (logs: CodexInspectionLog[]) => {
      if (!logs.length) return;
      const lines = logs.map((entry) => {
        const ts = new Date(entry.createdAtMs).toISOString();
        const detail = entry.detail
          ? ` ${typeof entry.detail === 'string' ? entry.detail : JSON.stringify(entry.detail)}`
          : '';
        const message = entry.message.startsWith('monitoring.') ? t(entry.message) : entry.message;
        return `[${ts}] [${entry.level}] ${message}${detail}`;
      });
      try {
        await navigator.clipboard.writeText(lines.join('\n'));
        showNotification(t('monitoring.server_codex_inspection_logs_copied'), 'success');
      } catch {
        showNotification(t('monitoring.server_codex_inspection_logs_copy_failed'), 'error');
      }
    },
    [showNotification, t]
  );

  const renderLogsPanel = (logs: CodexInspectionLog[]) => {
    const counts: Record<'all' | 'info' | 'success' | 'warning' | 'error', number> = {
      all: logs.length,
      info: 0,
      success: 0,
      warning: 0,
      error: 0,
    };
    for (const entry of logs) {
      if (
        entry.level === 'info' ||
        entry.level === 'success' ||
        entry.level === 'warning' ||
        entry.level === 'error'
      ) {
        counts[entry.level] += 1;
      }
    }
    const filterOptions: ReadonlyArray<{ value: typeof logLevelFilter; label: string }> = [
      { value: 'all', label: t('monitoring.server_codex_inspection_filter_all') },
      { value: 'info', label: t('monitoring.server_codex_inspection_log_level_info') },
      { value: 'success', label: t('monitoring.server_codex_inspection_log_level_success') },
      { value: 'warning', label: t('monitoring.server_codex_inspection_log_level_warning') },
      { value: 'error', label: t('monitoring.server_codex_inspection_log_level_error') },
    ];
    const filtered =
      logLevelFilter === 'all' ? logs : logs.filter((entry) => entry.level === logLevelFilter);
    return (
      <Panel
        title={t('monitoring.codex_inspection_logs_title')}
        subtitle={t('monitoring.server_codex_inspection_logs_desc')}
        extra={
          <div className={styles.logToolbar}>
            {logs.length > 0 ? (
              <div
                className={styles.logFilterGroup}
                role="tablist"
                aria-label={t('monitoring.codex_inspection_logs_title')}
              >
                <div className={styles.segmentedControl}>
                  {filterOptions.map((opt) => {
                    const active = logLevelFilter === opt.value;
                    return (
                      <button
                        key={opt.value}
                        type="button"
                        role="tab"
                        aria-selected={active}
                        className={`${styles.segmentButton} ${active ? styles.segmentButtonActive : ''}`}
                        onClick={() => setLogLevelFilter(opt.value)}
                      >
                        {opt.label}
                        <span className={styles.segmentCount}>{counts[opt.value]}</span>
                      </button>
                    );
                  })}
                </div>
              </div>
            ) : (
              <span />
            )}
            <div className={styles.logToolbarRight}>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => void handleCopyLogs(logs)}
                disabled={logs.length === 0}
                aria-label={t('monitoring.server_codex_inspection_logs_copy')}
              >
                {t('monitoring.server_codex_inspection_logs_copy')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => setLogsCollapsed((previous) => !previous)}
                disabled={logs.length === 0}
              >
                {logsCollapsed
                  ? t('monitoring.codex_inspection_expand_logs')
                  : t('monitoring.codex_inspection_fold_logs')}
              </Button>
            </div>
          </div>
        }
      >
        {!logsCollapsed ? (
          <div className={styles.logList}>
            {filtered.length > 0 ? (
              filtered.map((entry) => (
                <div
                  key={entry.id}
                  className={`${styles.logRow} ${logLevelClass[entry.level] ?? styles.logInfo}`}
                >
                  <span className={styles.logTime}>
                    {formatTimestamp(entry.createdAtMs, i18n.language)}
                  </span>
                  <span className={styles.logMessage}>
                    {entry.message.startsWith('monitoring.') ? t(entry.message) : entry.message}
                    {entry.detail ? (
                      <small className={styles.serverLogDetail}>
                        {typeof entry.detail === 'string'
                          ? entry.detail
                          : JSON.stringify(entry.detail)}
                      </small>
                    ) : null}
                  </span>
                </div>
              ))
            ) : (
              <div className={styles.emptyBlockSmall}>
                {t('monitoring.codex_inspection_logs_empty')}
              </div>
            )}
          </div>
        ) : (
          <div className={styles.logCollapsedBar}>
            <span>{t('monitoring.codex_inspection_logs_collapsed', { count: logs.length })}</span>
          </div>
        )}
      </Panel>
    );
  };

  return (
    <div className={styles.page}>
      <CodexInspectionModeTabs activeMode="server" />

      {error ? (
        <div className={styles.topErrorBar} role="alert" aria-live="polite">
          <span>{error}</span>
          <div className={styles.topErrorActions}>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void refreshRuns()}
              loading={loading}
            >
              {t('common.retry')}
            </Button>
          </div>
        </div>
      ) : null}
      {renderStatusPanel()}
      <div className={styles.serverDetailGrid}>
        {renderRunsPanel()}
        <div className={styles.serverDetailPanels}>
          {detail?.run.error ? (
            <div className={styles.serverError} role="alert">
              {detail.run.error}
            </div>
          ) : null}
          {renderResultsPanel()}
          {renderLogsPanel(detail?.logs ?? [])}
        </div>
      </div>
      {renderConfigDrawer()}
      <CodexReauthDialog
        open={Boolean(codexReauthTarget)}
        target={codexReauthTarget}
        onClose={() => setCodexReauthTarget(null)}
        onSuccess={handleCodexReauthSuccess}
      />
    </div>
  );
}
