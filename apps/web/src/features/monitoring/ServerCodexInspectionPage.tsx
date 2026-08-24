import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { IconRefreshCw, IconShield, IconTrash2 } from '@/components/ui/icons';
import { Input } from '@/components/ui/Input';
import { Select, type SelectOption } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { CodexInspectionConfigOverview } from '@/features/monitoring/components/CodexInspectionConfigOverview';
import { CodexInspectionLogsPanel } from '@/features/monitoring/components/CodexInspectionLogsPanel';
import { Panel } from '@/features/monitoring/components/CodexInspectionPanels';
import { CodexInspectionResultsPanel } from '@/features/monitoring/components/CodexInspectionResultsPanel';
import { CodexInspectionStopButton } from '@/features/monitoring/components/CodexInspectionStopButton';
import { InspectionConfigDrawer } from '@/features/monitoring/components/InspectionConfigDrawer';
import { InspectionConfigFields } from '@/features/monitoring/components/InspectionConfigFields';
import {
  SummaryCard as MonitoringSummaryCard,
  type SummaryCardProps as MonitoringSummaryCardProps,
} from '@/features/monitoring/components/MonitoringShared';
import { CodexReauthDialog } from '@/features/oauth/CodexReauthDialog';
import type { CodexReauthTarget } from '@/features/oauth/codexReauthModel';
import {
  createCodexInspectionConnectionFingerprint,
  type CodexInspectionAction,
  type CodexInspectionResultItem,
  type CodexInspectionRunResult,
} from '@/features/monitoring/codexInspection';
import {
  CODEX_INSPECTION_RESULT_PAGE_SIZE_OPTIONS,
  buildCodexInspectionPaginationState,
  buildConfigOverviewItems,
  countHandlingStates,
  filterInspectionResults,
  formatActionLabel,
  formatInspectionLogsForClipboard,
  formatPercent,
  formatTimestamp,
  getActionFilterCounts,
  getCanonicalServerCodexInspectionActionIds,
  getMixedServerCodexInspectionActionIds,
  isActionableServerCodexInspectionResult,
  isHandledServerCodexInspectionResult,
  isPendingServerReauthResult,
  normalizeServerCodexInspectionActionStatus,
  toServerInspectionLogViewEntry,
  type ActionFilter,
  type HandlingFilter,
  type InspectionLogLevelFilter,
  type StatusTone,
  validateInspectionConfigDraft,
  validateInspectionConfigFields,
} from '@/features/monitoring/model/codexInspectionPresentation';
import {
  createServerCredentialInspectionSnapshot,
  isCompletedCredentialInspectionRun,
  type CredentialInspectionSnapshot,
  type CredentialInspectionTarget,
} from '@/features/monitoring/model/credentialInspectionSnapshot';
import {
  DEFAULT_CODEX_INSPECTION_SETTINGS,
  codexInspectionTargetTypesToSelection,
  normalizeCodexInspectionTargetTypes,
} from '@/features/monitoring/model/codexInspectionSettings';
import {
  findCancellableRun,
  getRunStatusLabel,
  hasActiveRun,
  isActiveRun,
} from '@/features/monitoring/model/serverCodexInspectionLifecycle';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import {
  getUsageServiceErrorCode,
  monitoringAnalyticsApi,
  usageServiceApi,
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
  getUsageHeaderSnapshotMatchForIdentity,
} from '@/utils/usageHeaderSnapshots';
import styles from './CodexInspectionPage.module.scss';

type ServerCodexInspectionDraft = {
  enabled: boolean;
  scheduleMode: ManagerCodexInspectionScheduleMode;
  intervalMinutes: string;
  timePoints: string;
  timeZone: string;
  targetTypes: string;
  workers: string;
  deleteWorkers: string;
  timeout: string;
  retries: string;
  userAgent: string;
  xaiInferenceUserAgent: string;
  xaiInferenceEnabled: boolean;
  xaiInferenceModel: string;
  xaiInferencePrompt: string;
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
  targetTypes: string[];
  targetType: string;
  workers: number;
  deleteWorkers: number;
  timeout: number;
  retries: number;
  userAgent: string;
  xaiInferenceUserAgent: string;
  xaiInferenceEnabled: boolean;
  xaiInferenceModel: string;
  xaiInferencePrompt: string;
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
  targetTypes: ['codex'],
  targetType: 'codex',
  workers: 4,
  deleteWorkers: 4,
  timeout: 15000,
  retries: 0,
  userAgent: 'codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal',
  xaiInferenceUserAgent: DEFAULT_CODEX_INSPECTION_SETTINGS.xaiInferenceUserAgent,
  xaiInferenceEnabled: false,
  xaiInferenceModel: DEFAULT_CODEX_INSPECTION_SETTINGS.xaiInferenceModel,
  xaiInferencePrompt: DEFAULT_CODEX_INSPECTION_SETTINGS.xaiInferencePrompt,
  usedPercentThreshold: 100,
  sampleSize: 0,
  autoActionMode: 'none',
  autoRecoverEnabled: false,
};

const RUNS_LIMIT = 30;
const EMPTY_CODEX_INSPECTION_RUNS: CodexInspectionRun[] = [];

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
    targetTypes: (() => {
      const targetTypes = normalizeCodexInspectionTargetTypes(
        config?.targetTypes,
        config?.targetType
      );
      return targetTypes.length > 0 ? targetTypes : DEFAULT_SERVER_CODEX_CONFIG.targetTypes;
    })(),
    targetType: (() => {
      const targetTypes = normalizeCodexInspectionTargetTypes(
        config?.targetTypes,
        config?.targetType
      );
      return targetTypes[0] ?? DEFAULT_SERVER_CODEX_CONFIG.targetType;
    })(),
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
    xaiInferenceUserAgent:
      config?.xaiInferenceUserAgent || DEFAULT_SERVER_CODEX_CONFIG.xaiInferenceUserAgent,
    xaiInferenceEnabled:
      config?.xaiInferenceEnabled ?? DEFAULT_SERVER_CODEX_CONFIG.xaiInferenceEnabled,
    xaiInferenceModel: config?.xaiInferenceModel || DEFAULT_SERVER_CODEX_CONFIG.xaiInferenceModel,
    xaiInferencePrompt:
      config?.xaiInferencePrompt || DEFAULT_SERVER_CODEX_CONFIG.xaiInferencePrompt,
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
    targetTypes: codexInspectionTargetTypesToSelection(resolved.targetTypes, resolved.targetType),
    workers: String(resolved.workers),
    deleteWorkers: String(resolved.deleteWorkers),
    timeout: String(resolved.timeout),
    retries: String(resolved.retries),
    userAgent: resolved.userAgent,
    xaiInferenceUserAgent: resolved.xaiInferenceUserAgent,
    xaiInferenceEnabled: resolved.xaiInferenceEnabled,
    xaiInferenceModel: resolved.xaiInferenceModel,
    xaiInferencePrompt: resolved.xaiInferencePrompt,
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
    targetTypes: validation.values.targetTypes,
    targetType: validation.values.targetTypes[0],
    workers: validation.values.workers,
    deleteWorkers: validation.values.deleteWorkers,
    timeout: validation.values.timeout,
    retries: validation.values.retries,
    userAgent: validation.values.userAgent,
    xaiInferenceUserAgent: validation.values.xaiInferenceUserAgent,
    xaiInferenceEnabled: validation.values.xaiInferenceEnabled,
    xaiInferenceModel: validation.values.xaiInferenceModel,
    xaiInferencePrompt: validation.values.xaiInferencePrompt,
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

function getRunTone(run?: CodexInspectionRun | null): StatusTone {
  switch (run?.status) {
    case 'completed':
    case 'cancelled':
      return 'good';
    case 'failed':
      return 'bad';
    case 'running':
    case 'cancelling':
      return 'info';
    case 'interrupted':
      return 'warn';
    default:
      return 'idle';
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
    targetTypes: normalizeCodexInspectionTargetTypes(config.targetTypes, config.targetType),
    workers: config.workers,
    deleteWorkers: config.deleteWorkers,
    timeout: config.timeout,
    retries: config.retries,
    userAgent: config.userAgent.trim(),
    xaiInferenceUserAgent: config.xaiInferenceUserAgent.trim(),
    xaiInferenceEnabled: config.xaiInferenceEnabled,
    xaiInferenceModel: config.xaiInferenceModel.trim(),
    xaiInferencePrompt: config.xaiInferencePrompt.trim(),
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

function formatServerTerminalActionStatusLabel(
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
  return '';
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
  const observedAt = formatObservedHeaderRecoverAt(snapshot.timestamp_ms, locale);
  if (observedAt) {
    evidence.push(
      t('monitoring.codex_inspection_observed_header_at', {
        time: observedAt,
      })
    );
  }
  const quotaParts = [
    getHeaderSnapshotPlanType(snapshot),
    (() => {
      const usedPercent = getHeaderSnapshotUsedPercent(snapshot);
      return typeof usedPercent === 'number' && Number.isFinite(usedPercent)
        ? formatPercent(usedPercent)
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

// eslint-disable-next-line react-refresh/only-export-components -- pure mapper is exported for unit testing
export function toServerResultItem(
  item: CodexInspectionResult,
  t: ReturnType<typeof useTranslation>['t'],
  snapshot: UsageHeaderSnapshot | undefined,
  locale: string
): CodexInspectionResultItem {
  const actionReason = item.actionReason?.startsWith('monitoring.')
    ? t(item.actionReason)
    : item.actionReason;
  const observedHeaderEvidence = buildObservedHeaderEvidence(snapshot, locale, t);
  return {
    key: `server-${item.id || item.accountKey}`,
    runtimeId: item.runtimeId ?? null,
    fileName: item.fileName,
    displayAccount: item.displayAccount,
    accountSnapshot: item.accountSnapshot ?? null,
    authIndex: item.authIndex ?? null,
    accountId: item.accountId ?? null,
    provider: item.provider,
    disabled: item.disabled,
    autoRecoverOwned: item.autoRecoverEligible === true,
    status: item.status ?? '',
    state: item.state ?? '',
    raw: item as unknown as CodexInspectionResultItem['raw'],
    action: normalizeServerResultAction(item.action),
    actionReason,
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
      resetAtMs: window.resetAtMs ?? null,
      resetAccuracy: window.resetAccuracy,
      limitWindowSeconds: window.limitWindowSeconds ?? null,
      modelScope: window.modelScope,
      providerWindowAliases: window.providerWindowAliases,
    })),
    quotaInventoryObserved: item.quotaInventoryObserved,
    errorKind: item.errorKind,
    errorDetail: item.errorDetail || '',
    actionHandled: isHandledServerCodexInspectionResult(item),
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

interface ServerCodexInspectionPageProps {
  embedded?: boolean;
  modeControl?: ReactNode;
  onSnapshotChange?: (snapshot: CredentialInspectionSnapshot) => void;
  onCredentialsChanged?: (
    target?: CodexReauthTarget | null,
    snapshot?: CredentialInspectionSnapshot | null
  ) => void | Promise<void>;
  onOpenCredential?: (target: CredentialInspectionTarget) => void;
}

export function ServerCodexInspectionPage({
  embedded = false,
  modeControl,
  onSnapshotChange,
  onCredentialsChanged,
  onOpenCredential,
}: ServerCodexInspectionPageProps = {}) {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const apiBase = useAuthStore((state) => state.apiBase);
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
  const [cancelling, setCancelling] = useState(false);
  const [error, setError] = useState('');
  const [logsCollapsed, setLogsCollapsed] = useState(false);
  const [actionFilter, setActionFilter] = useState<ActionFilter>('all');
  const [handlingFilter, setHandlingFilter] = useState<HandlingFilter>('all');
  const [resultPage, setResultPage] = useState(1);
  const [resultPageSize, setResultPageSize] = useState<number>(
    CODEX_INSPECTION_RESULT_PAGE_SIZE_OPTIONS[0]
  );
  const [logLevelFilter, setLogLevelFilter] = useState<InspectionLogLevelFilter>('all');
  const [executingResultIds, setExecutingResultIds] = useState<Set<number>>(() => new Set());
  const [executingAllActions, setExecutingAllActions] = useState(false);
  const [configDrawerOpen, setConfigDrawerOpen] = useState(false);
  const [configFocusField, setConfigFocusField] = useState<string | null>(null);
  const [codexReauthTarget, setCodexReauthTarget] = useState<CodexReauthTarget | null>(null);
  const refreshInFlightRef = useRef(false);
  const actionInFlightRef = useRef(false);
  const runsContextRef = useRef<object | null>(null);
  const detailContextRef = useRef<object | null>(null);
  const detailRequestGenerationRef = useRef(0);
  const runListMutationGenerationRef = useRef(0);
  const selectedRunIdRef = useRef<number | null>(null);
  const logListRef = useRef<HTMLDivElement | null>(null);
  const previousServerLogCursorRef = useRef<{
    runId: number | null;
    latestLogId: number | null;
  }>({ runId: null, latestLogId: null });

  const selectRunId = useCallback((id: number | null) => {
    selectedRunIdRef.current = id;
    detailRequestGenerationRef.current += 1;
    detailContextRef.current = null;
    setDetail(null);
    setSelectedRunId(id);
  }, []);

  const managerConnectionScopeKey = useMemo(
    () =>
      [
        featureAvailability.checking ? 'checking' : 'ready',
        featureAvailability.serverCodexInspectionAvailable ? 'available' : 'unavailable',
        createCodexInspectionConnectionFingerprint(
          featureAvailability.managerServiceBase,
          managementKey
        ) ?? 'disconnected',
        apiBase,
      ].join('\u001f'),
    [
      apiBase,
      featureAvailability.checking,
      featureAvailability.managerServiceBase,
      featureAvailability.serverCodexInspectionAvailable,
      managementKey,
    ]
  );
  const authFilesRequestScope = useMemo(
    () => ({ apiBase, managementKey }),
    [apiBase, managementKey]
  );
  const managerConnectionIdentity = useMemo<object>(
    () => ({ scopeKey: managerConnectionScopeKey }),
    [managerConnectionScopeKey]
  );
  const activeManagerConnectionIdentityRef = useRef(managerConnectionIdentity);
  activeManagerConnectionIdentityRef.current = managerConnectionIdentity;

  useLayoutEffect(() => {
    detailRequestGenerationRef.current += 1;
    runListMutationGenerationRef.current += 1;
    refreshInFlightRef.current = false;
    actionInFlightRef.current = false;
    selectedRunIdRef.current = null;
    runsContextRef.current = null;
    detailContextRef.current = null;
    setServiceBase('');
    setManagerConfig(null);
    setDraft(toDraft(null));
    setRuns([]);
    setDetail(null);
    setHeaderSnapshots([]);
    setSelectedRunId(null);
    setExecutingResultIds(new Set());
    setExecutingAllActions(false);
    setSaving(false);
    setRunning(false);
    setCancelling(false);
    setError('');
    setConfigDrawerOpen(false);
    setCodexReauthTarget(null);
  }, [managerConnectionIdentity]);

  useEffect(() => {
    if (!detail || !onSnapshotChange || !isCompletedCredentialInspectionRun(detail.run)) return;
    if (detailContextRef.current !== managerConnectionIdentity) return;
    if (runsContextRef.current !== managerConnectionIdentity) return;
    const snapshot = createServerCredentialInspectionSnapshot(detail, runs);
    if (snapshot) onSnapshotChange(snapshot);
  }, [detail, managerConnectionIdentity, onSnapshotChange, runs]);

  const loadRunDetail = useCallback(
    async (base: string, id: number) => {
      if (selectedRunIdRef.current !== id) return null;
      const connectionContext = managerConnectionIdentity;
      if (activeManagerConnectionIdentityRef.current !== connectionContext) return null;
      const requestGeneration = ++detailRequestGenerationRef.current;
      let nextDetail: CodexInspectionRunDetail;
      try {
        nextDetail = await usageServiceApi.getCodexInspectionRun(base, managementKey, id);
      } catch (error) {
        if (
          requestGeneration !== detailRequestGenerationRef.current ||
          activeManagerConnectionIdentityRef.current !== connectionContext ||
          selectedRunIdRef.current !== id
        ) {
          return null;
        }
        throw error;
      }
      if (
        requestGeneration !== detailRequestGenerationRef.current ||
        activeManagerConnectionIdentityRef.current !== connectionContext ||
        selectedRunIdRef.current !== id
      ) {
        return null;
      }
      detailContextRef.current = connectionContext;
      setDetail(nextDetail);
      return nextDetail;
    },
    [managementKey, managerConnectionIdentity]
  );

  const loadPageData = useCallback(async () => {
    const connectionContext = managerConnectionIdentity;
    const isCurrentConnection = () =>
      activeManagerConnectionIdentityRef.current === connectionContext;
    if (!isCurrentConnection()) return;
    setLoading(true);
    setError('');
    try {
      const resolvedBase = featureAvailability.managerServiceBase;
      if (!resolvedBase || !featureAvailability.serverCodexInspectionAvailable) {
        throw new Error(t('monitoring.server_codex_inspection_service_unavailable'));
      }
      const response = await usageServiceApi.getManagerConfig(resolvedBase, managementKey);
      if (!isCurrentConnection()) return;
      const responseConfig = response.config;

      setServiceBase(resolvedBase);
      setManagerConfig(responseConfig);
      setDraft(toDraft(responseConfig.codexInspection));

      const runsResponse = await usageServiceApi.listCodexInspectionRuns(
        resolvedBase,
        managementKey,
        RUNS_LIMIT
      );
      if (!isCurrentConnection()) return;
      const snapshotsResponse = await monitoringAnalyticsApi
        .getHeaderSnapshots(resolvedBase, managementKey, { days: 30, limit: 1000 })
        .catch(() => ({ items: [] as UsageHeaderSnapshot[] }));
      if (!isCurrentConnection()) return;
      setHeaderSnapshots(snapshotsResponse.items ?? []);
      runsContextRef.current = connectionContext;
      setRuns(runsResponse.items);
      const nextSelectedId = runsResponse.items[0]?.id;
      if (nextSelectedId) {
        selectRunId(nextSelectedId);
        await loadRunDetail(resolvedBase, nextSelectedId);
      } else {
        detailContextRef.current = null;
        setDetail(null);
        selectRunId(null);
      }
    } catch (error: unknown) {
      if (!isCurrentConnection()) return;
      setError(getUsageServiceDisplayError(error, t));
      runsContextRef.current = null;
      detailContextRef.current = null;
      setRuns([]);
      setDetail(null);
      setHeaderSnapshots([]);
      selectRunId(null);
    } finally {
      if (isCurrentConnection()) setLoading(false);
    }
  }, [
    featureAvailability.managerServiceBase,
    featureAvailability.serverCodexInspectionAvailable,
    loadRunDetail,
    managementKey,
    managerConnectionIdentity,
    selectRunId,
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
  const currentRuns = useMemo(
    () =>
      runsContextRef.current === managerConnectionIdentity ? runs : EMPTY_CODEX_INSPECTION_RUNS,
    [managerConnectionIdentity, runs]
  );
  const currentDetail = detailContextRef.current === managerConnectionIdentity ? detail : null;
  const serverLogEntries = useMemo(
    () =>
      (currentDetail?.logs ?? []).map((entry) =>
        toServerInspectionLogViewEntry(entry, currentDetail?.run, t)
      ),
    [currentDetail, t]
  );
  useEffect(() => {
    setLogLevelFilter('all');
  }, [currentDetail?.run.id]);
  const hasRunningRun = hasActiveRun(currentRuns, currentDetail?.run);
  const latestRun = currentRuns[0] ?? null;
  const activeRun = currentDetail?.run ?? latestRun;
  const cancellableRun = findCancellableRun(currentRuns, activeRun);
  const activeTone = getRunTone(activeRun);

  const resultRows = useMemo(() => currentDetail?.results ?? [], [currentDetail?.results]);
  const headerSnapshotCutoffMs =
    currentDetail?.run.finishedAtMs ?? currentDetail?.run.updatedAtMs ?? Number.POSITIVE_INFINITY;
  const headerSnapshotLookup = useMemo(
    () =>
      buildUsageHeaderSnapshotLookup(
        headerSnapshots.filter((snapshot) => snapshot.timestamp_ms <= headerSnapshotCutoffMs)
      ),
    [headerSnapshotCutoffMs, headerSnapshots]
  );
  const resultItems = useMemo(
    () =>
      resultRows.map((item) => {
        const snapshotMatch = getUsageHeaderSnapshotMatchForIdentity(headerSnapshotLookup, {
          fileName: item.fileName,
          authIndex: item.authIndex,
          account: item.accountId || item.displayAccount,
        });
        return toServerResultItem(
          item,
          t,
          snapshotMatch.confidence === 'high' ? snapshotMatch.snapshot : undefined,
          i18n.language
        );
      }),
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
  }, [actionFilter, currentDetail?.run.id, handlingFilter]);

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
      const connectionContext = managerConnectionIdentity;
      const isCurrentConnection = () =>
        activeManagerConnectionIdentityRef.current === connectionContext;
      if (!isCurrentConnection()) return;
      refreshInFlightRef.current = true;
      const silent = options?.silent ?? false;
      if (!serviceBase) {
        try {
          await loadPageData();
        } finally {
          if (isCurrentConnection()) refreshInFlightRef.current = false;
        }
        return;
      }
      if (!silent) {
        setLoading(true);
        setError('');
      }
      try {
        const mutationGeneration = runListMutationGenerationRef.current;
        const response = await usageServiceApi.listCodexInspectionRuns(
          serviceBase,
          managementKey,
          RUNS_LIMIT
        );
        // A start/cancel response is newer than any list request that began
        // before that lifecycle mutation completed. Discard the stale snapshot
        // so it cannot resurrect a running action after cancellation.
        if (!isCurrentConnection() || mutationGeneration !== runListMutationGenerationRef.current) {
          return;
        }
        runsContextRef.current = connectionContext;
        setRuns(response.items);
        const currentSelectedRunId = selectedRunIdRef.current;
        const selectionStillValid =
          currentSelectedRunId != null &&
          response.items.some((run) => run.id === currentSelectedRunId);
        if (selectionStillValid) {
          // 静默轮询时保留用户正在查看的历史详情,避免每 30s 重建详情导致结果表/日志
          // 重渲染、打断操作;但正在运行的巡检或尚无详情时仍需刷新以获取最新进度。
          const watchingRunning = isActiveRun(currentDetail?.run);
          if (!silent || !currentDetail || watchingRunning) {
            await loadRunDetail(serviceBase, currentSelectedRunId);
          }
        } else {
          const fallbackId = response.items[0]?.id;
          if (fallbackId) {
            selectRunId(fallbackId);
            await loadRunDetail(serviceBase, fallbackId);
          } else {
            detailContextRef.current = null;
            setDetail(null);
            selectRunId(null);
          }
        }
      } catch (error: unknown) {
        if (isCurrentConnection() && !silent) setError(getUsageServiceDisplayError(error, t));
      } finally {
        if (isCurrentConnection()) {
          if (!silent) setLoading(false);
          refreshInFlightRef.current = false;
        }
      }
    },
    [
      currentDetail,
      loadPageData,
      loadRunDetail,
      managementKey,
      managerConnectionIdentity,
      selectRunId,
      serviceBase,
      t,
    ]
  );

  useEffect(() => {
    if (!serviceBase || (!selectedConfig.enabled && !hasRunningRun)) return;
    const timer = window.setInterval(() => {
      if (saving || running || cancelling || actionInFlightRef.current) return;
      void refreshRuns({ silent: true });
    }, 30_000);

    return () => window.clearInterval(timer);
  }, [
    cancelling,
    hasRunningRun,
    refreshRuns,
    running,
    saving,
    selectedConfig.enabled,
    serviceBase,
  ]);

  const handleSave = async () => {
    if (!serviceBase || !managerConfig) {
      showNotification(t('monitoring.server_codex_inspection_service_unavailable'), 'warning');
      return;
    }
    const connectionContext = managerConnectionIdentity;
    const isCurrentConnection = () =>
      activeManagerConnectionIdentityRef.current === connectionContext;
    if (!isCurrentConnection()) return;
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
      if (!isCurrentConnection()) return;
      setManagerConfig(response.config);
      setDraft(toDraft(response.config.codexInspection));
      showNotification(t('monitoring.server_codex_inspection_config_saved'), 'success');
      setConfigDrawerOpen(false);
    } catch (error: unknown) {
      if (!isCurrentConnection()) return;
      showNotification(
        `${t('notification.save_failed')}: ${getUsageServiceDisplayError(error, t)}`,
        'error'
      );
    } finally {
      if (isCurrentConnection()) setSaving(false);
    }
  };

  const handleCloseConfigDrawer = useCallback(() => {
    if (hasUnsavedChanges) {
      const connectionContext = managerConnectionIdentity;
      showConfirmation({
        title: t('monitoring.server_codex_inspection_close_confirm_title'),
        message: t('monitoring.server_codex_inspection_close_unsaved_hint'),
        confirmText: t('monitoring.server_codex_inspection_discard'),
        cancelText: t('common.cancel'),
        variant: 'danger',
        onConfirm: () => {
          if (activeManagerConnectionIdentityRef.current !== connectionContext) return;
          setDraft(toDraft(managerConfig?.codexInspection));
          setConfigDrawerOpen(false);
        },
      });
      return;
    }
    setConfigDrawerOpen(false);
  }, [hasUnsavedChanges, managerConfig, managerConnectionIdentity, showConfirmation, t]);

  const openConfigDrawer = useCallback((field?: string) => {
    setConfigFocusField(field ?? null);
    setConfigDrawerOpen(true);
  }, []);

  const executeServerRun = useCallback(async () => {
    if (!serviceBase) {
      showNotification(t('monitoring.server_codex_inspection_service_unavailable'), 'warning');
      return;
    }
    const connectionContext = managerConnectionIdentity;
    const isCurrentConnection = () =>
      activeManagerConnectionIdentityRef.current === connectionContext;
    if (!isCurrentConnection()) return;
    setRunning(true);
    setError('');
    try {
      const nextDetail = await usageServiceApi.runCodexInspection(serviceBase, managementKey);
      if (!isCurrentConnection()) return;
      const mutationGeneration = ++runListMutationGenerationRef.current;
      selectRunId(nextDetail.run.id);
      detailContextRef.current = connectionContext;
      setDetail(nextDetail);
      showNotification(t('monitoring.server_codex_inspection_run_started'), 'success');
      try {
        const response = await usageServiceApi.listCodexInspectionRuns(
          serviceBase,
          managementKey,
          RUNS_LIMIT
        );
        if (!isCurrentConnection() || mutationGeneration !== runListMutationGenerationRef.current) {
          return;
        }
        runsContextRef.current = connectionContext;
        setRuns(response.items);
        const refreshedRun = response.items.find((item) => item.id === nextDetail.run.id);
        if (refreshedRun) {
          setDetail((current) =>
            current?.run.id === refreshedRun.id ? { ...current, run: refreshedRun } : current
          );
        }
        // A small inspection may finish between the asynchronous start response
        // and this list refresh. Reload its detail so the page does not retain a
        // synthetic running state with empty results after the run is terminal.
        await loadRunDetail(serviceBase, nextDetail.run.id);
      } catch {
        // Starting already succeeded. Reconciliation failure must not be shown
        // as a failed start; a later poll/manual refresh can retry it.
      }
    } catch (error: unknown) {
      if (!isCurrentConnection()) return;
      const message = getUsageServiceDisplayError(error, t);
      showNotification(
        `${t('monitoring.server_codex_inspection_run_failed')}: ${message}`,
        'error'
      );
      await refreshRuns();
    } finally {
      if (isCurrentConnection()) setRunning(false);
    }
  }, [
    loadRunDetail,
    managementKey,
    managerConnectionIdentity,
    refreshRuns,
    selectRunId,
    serviceBase,
    showNotification,
    t,
  ]);

  const handleRunNow = () => {
    const connectionContext = managerConnectionIdentity;
    showConfirmation({
      title: t('monitoring.server_codex_inspection_run_confirm_title'),
      message: t('monitoring.server_codex_inspection_run_confirm_body'),
      confirmText: t('monitoring.server_codex_inspection_run_now'),
      cancelText: t('common.cancel'),
      variant: selectedConfig.autoActionMode === 'delete' ? 'danger' : 'primary',
      onConfirm: () => {
        if (activeManagerConnectionIdentityRef.current !== connectionContext) return;
        void executeServerRun();
      },
    });
  };

  const executeServerCancel = useCallback(async () => {
    if (!serviceBase || !cancellableRun) return;
    const connectionContext = managerConnectionIdentity;
    const isCurrentConnection = () =>
      activeManagerConnectionIdentityRef.current === connectionContext;
    if (!isCurrentConnection()) return;
    const requestedRunId = cancellableRun.id;
    // Keep a user's history selection stable while the cancel request is in
    // flight. A response for the active run may update the detail view only
    // when that same run is still selected; otherwise the user's history view
    // must remain untouched.
    setCancelling(true);
    try {
      const nextDetail = await usageServiceApi.cancelCodexInspectionRun(
        serviceBase,
        managementKey,
        requestedRunId
      );
      if (!isCurrentConnection()) return;
      ++runListMutationGenerationRef.current;
      runsContextRef.current = connectionContext;
      setRuns((current) => {
        let found = false;
        const updated = current.map((item) => {
          if (item.id !== nextDetail.run.id) return item;
          found = true;
          return nextDetail.run;
        });
        return found ? updated : [nextDetail.run, ...updated];
      });
      if (selectedRunIdRef.current === requestedRunId) {
        selectRunId(nextDetail.run.id);
        detailContextRef.current = connectionContext;
        setDetail(nextDetail);
      }
      showNotification(t('monitoring.server_codex_inspection_cancel_requested'), 'success');
      await refreshRuns({ silent: true });
    } catch (error: unknown) {
      if (!isCurrentConnection()) return;
      showNotification(
        `${t('monitoring.server_codex_inspection_cancel_failed')}: ${getUsageServiceDisplayError(error, t)}`,
        'error'
      );
      // A conflict normally means the worker committed a newer terminal state.
      // Bypass the ordinary in-flight refresh guard so a pre-cancel poll cannot
      // leave the page on a stale running snapshot.
      const mutationGeneration = ++runListMutationGenerationRef.current;
      try {
        const response = await usageServiceApi.listCodexInspectionRuns(
          serviceBase,
          managementKey,
          RUNS_LIMIT
        );
        if (isCurrentConnection() && mutationGeneration === runListMutationGenerationRef.current) {
          runsContextRef.current = connectionContext;
          setRuns(response.items);
        }
      } catch {
        // The original cancellation error remains the user-facing failure. A
        // later poll/manual refresh can retry list reconciliation.
      }
      if (selectedRunIdRef.current === requestedRunId) {
        try {
          await loadRunDetail(serviceBase, requestedRunId);
        } catch {
          // Keep the cancellation error as the primary notification.
        }
      }
    } finally {
      if (isCurrentConnection()) setCancelling(false);
    }
  }, [
    cancellableRun,
    loadRunDetail,
    managementKey,
    managerConnectionIdentity,
    refreshRuns,
    selectRunId,
    selectedRunIdRef,
    serviceBase,
    showNotification,
    t,
  ]);

  const handleCancelRun = () => {
    if (!cancellableRun || cancellableRun.status === 'cancelling') return;
    const connectionContext = managerConnectionIdentity;
    showConfirmation({
      title: t('monitoring.server_codex_inspection_cancel_confirm_title'),
      message: t('monitoring.server_codex_inspection_cancel_confirm_body'),
      confirmText: t('monitoring.server_codex_inspection_stop'),
      cancelText: t('common.cancel'),
      variant: 'danger',
      onConfirm: () => {
        if (activeManagerConnectionIdentityRef.current !== connectionContext) return;
        void executeServerCancel();
      },
    });
  };

  const executeServerActions = useCallback(
    async (
      targets: CodexInspectionResult[],
      scope: 'single' | 'bulk',
      overrideAction?: 'delete'
    ) => {
      if (!serviceBase || !currentDetail) {
        showNotification(t('monitoring.server_codex_inspection_service_unavailable'), 'warning');
        return;
      }
      const connectionContext = managerConnectionIdentity;
      const isCurrentConnection = () =>
        activeManagerConnectionIdentityRef.current === connectionContext;
      if (!isCurrentConnection()) return;
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
          currentDetail.run.id,
          resultIds,
          overrideAction === 'delete'
            ? resultIds.map((resultId) => ({ resultId, action: 'delete' as const }))
            : []
        );
        if (!isCurrentConnection()) return;
        selectRunId(response.detail.run.id);
        detailContextRef.current = connectionContext;
        setDetail(response.detail);
        const actionSnapshot = createServerCredentialInspectionSnapshot(response.detail, [
          response.detail.run,
          ...currentRuns.filter((run) => run.id !== response.detail.run.id),
        ]);
        let synchronizationWarning = '';
        try {
          await onCredentialsChanged?.(undefined, actionSnapshot);
        } catch (error: unknown) {
          if (!isCurrentConnection()) return;
          synchronizationWarning = `${t('notification.refresh_failed')}: ${getUsageServiceDisplayError(error, t)}`;
        }
        if (!isCurrentConnection()) return;

        try {
          const runsResponse = await usageServiceApi.listCodexInspectionRuns(
            serviceBase,
            managementKey,
            RUNS_LIMIT
          );
          if (!isCurrentConnection()) return;
          runsContextRef.current = connectionContext;
          setRuns(runsResponse.items);
        } catch {
          // The credential action already succeeded. A later poll or manual refresh can
          // reconcile the history list without misreporting the mutation as failed.
        }
        if (!isCurrentConnection()) return;

        const outcomeSummary = response.outcomes.reduce(
          (summary, outcome) => {
            switch (outcome.status) {
              case 'success':
                summary.success += 1;
                break;
              case 'skipped':
                summary.skipped += 1;
                break;
              case 'needs_review':
                summary.needsReview += 1;
                break;
              case 'failed':
                summary.failed += 1;
                break;
              default:
                if (outcome.success) summary.success += 1;
                else summary.failed += 1;
            }
            return summary;
          },
          { success: 0, skipped: 0, needsReview: 0, failed: 0 }
        );
        const hasNonSuccessOutcome =
          outcomeSummary.failed > 0 || outcomeSummary.skipped > 0 || outcomeSummary.needsReview > 0;
        if (hasNonSuccessOutcome) {
          showNotification(
            [
              t('monitoring.codex_inspection_log_manual_completed', {
                success: outcomeSummary.success,
                skipped: outcomeSummary.skipped,
                review: outcomeSummary.needsReview,
                failed: outcomeSummary.failed,
              }),
              synchronizationWarning,
            ]
              .filter(Boolean)
              .join('；'),
            'warning'
          );
        } else if (synchronizationWarning) {
          showNotification(synchronizationWarning, 'warning');
        } else {
          showNotification(t('monitoring.server_codex_inspection_execute_success'), 'success');
        }
      } catch (error: unknown) {
        if (!isCurrentConnection()) return;
        showNotification(
          `${t('monitoring.server_codex_inspection_execute_failed')}: ${getUsageServiceDisplayError(error, t)}`,
          'error'
        );
      } finally {
        if (isCurrentConnection()) {
          actionInFlightRef.current = false;
          setExecutingResultIds(new Set());
          setExecutingAllActions(false);
        }
      }
    },
    [
      currentDetail,
      currentRuns,
      managementKey,
      managerConnectionIdentity,
      onCredentialsChanged,
      selectRunId,
      serviceBase,
      showNotification,
      t,
    ]
  );

  const handleExecuteServerActions = useCallback(
    (targets: CodexInspectionResult[], scope: 'single' | 'bulk') => {
      if (targets.length === 0) return;
      const connectionContext = managerConnectionIdentity;
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
        onConfirm: () => {
          if (activeManagerConnectionIdentityRef.current !== connectionContext) return;
          void executeServerActions(targets, scope);
        },
      });
    },
    [executeServerActions, managerConnectionIdentity, showConfirmation, t]
  );

  const handleOpenCodexReauth = useCallback(
    (item: CodexInspectionResult) => {
      if (item.provider === 'xai') {
        navigate('/oauth#oauth-provider-xai');
        return;
      }
      setCodexReauthTarget({
        account: item.displayAccount || item.accountId || item.fileName,
        fileName: item.fileName,
        runtimeId: item.runtimeId ?? null,
        provider: item.provider ?? 'codex',
        authIndex: item.authIndex ?? null,
        accountId: item.accountId ?? null,
        accountSnapshot: item.accountSnapshot ?? null,
      });
    },
    [navigate]
  );

  const handleDeleteServerReauth = useCallback(
    (targets: CodexInspectionResult[], scope: 'single' | 'bulk') => {
      if (targets.length === 0) return;
      const connectionContext = managerConnectionIdentity;
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
        onConfirm: () => {
          if (activeManagerConnectionIdentityRef.current !== connectionContext) return;
          void executeServerActions(targets, scope, 'delete');
        },
      });
    },
    [executeServerActions, managerConnectionIdentity, showConfirmation, t]
  );

  const handleCodexReauthSuccess = useCallback(async () => {
    const connectionContext = managerConnectionIdentity;
    if (activeManagerConnectionIdentityRef.current !== connectionContext) return;
    await refreshRuns({ silent: true });
    if (activeManagerConnectionIdentityRef.current !== connectionContext) return;
    await onCredentialsChanged?.(codexReauthTarget);
    if (activeManagerConnectionIdentityRef.current !== connectionContext) return;
    showNotification(t('codex_reauth.rerun_hint'), 'success');
  }, [
    codexReauthTarget,
    managerConnectionIdentity,
    onCredentialsChanged,
    refreshRuns,
    showNotification,
    t,
  ]);

  const handleSelectRun = async (runID: number) => {
    if (!serviceBase || runID === selectedRunId) return;
    const connectionContext = managerConnectionIdentity;
    if (activeManagerConnectionIdentityRef.current !== connectionContext) return;
    selectRunId(runID);
    try {
      await loadRunDetail(serviceBase, runID);
    } catch (error: unknown) {
      if (activeManagerConnectionIdentityRef.current !== connectionContext) return;
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
        <div className={styles.statusPanelHeader}>
          {modeControl ? <div className={styles.statusPanelTabs}>{modeControl}</div> : <div />}
          <div className={styles.statusPanelActions}>
            <details className={`${styles.infoNote} ${styles.infoNoteCompact}`}>
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
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void refreshRuns()}
              loading={loading}
            >
              {t('common.refresh')}
            </Button>
            <Button variant="secondary" size="sm" onClick={() => openConfigDrawer()}>
              {t('monitoring.codex_inspection_config_overview_edit')}
            </Button>
            <Button
              size="sm"
              onClick={handleRunNow}
              loading={running}
              disabled={!serviceBase || running || hasRunningRun}
            >
              {t('monitoring.server_codex_inspection_run_now')}
            </Button>
            <CodexInspectionStopButton
              run={cancellableRun}
              busy={cancelling}
              onClick={handleCancelRun}
              t={t}
            />
          </div>
        </div>

        <div className={styles.statusPanelBody}>
          <div className={styles.statusMetricsRow}>
            <div className={styles.statusMetric}>
              <span className={styles.statusMetricLabel}>{t('common.status')}</span>
              <span className={styles.statusMetricValue}>
                <span
                  className={styles.statusDot}
                  aria-hidden="true"
                  style={{
                    color: `var(--inspect-${activeTone === 'good' ? 'green' : activeTone === 'bad' ? 'red' : activeTone === 'warn' ? 'amber' : activeTone === 'info' ? 'accent' : 'muted'})`,
                    background: 'currentColor',
                  }}
                />
                {getRunStatusLabel(activeRun, t)}
              </span>
            </div>
            <div className={styles.statusMetric}>
              <span className={styles.statusMetricLabel}>
                {t('monitoring.server_codex_inspection_last_run')}
              </span>
              <span className={styles.statusMetricValue}>
                {lastRunTime} {activeRun?.finishedAtMs ? ` · ${durationLabel}` : ''}
              </span>
            </div>
            {serviceHost ? (
              <div className={styles.statusMetric}>
                <span className={styles.statusMetricLabel}>
                  {t('monitoring.server_codex_inspection_host')}
                </span>
                <span className={styles.statusMetricValue} title={serviceBase}>
                  {serviceHost}
                </span>
              </div>
            ) : null}
          </div>

          {!selectedConfig.enabled ? (
            <div className={styles.statusAlertBanner}>
              ⚠️ {t('monitoring.server_codex_inspection_schedule_disabled')}
            </div>
          ) : null}

          <CodexInspectionConfigOverview
            title={t('monitoring.codex_inspection_config_overview_title')}
            editLabel={t('monitoring.codex_inspection_config_overview_edit')}
            copyLabel={t('monitoring.codex_inspection_settings_copy_prompt')}
            copiedLabel={t('common.copied')}
            ariaLabel={t('monitoring.server_codex_inspection_config_summary_title')}
            items={configOverviewItems}
            onEdit={openConfigDrawer}
            compact
            embedded
            hideHeader
          />

          <section className={styles.summaryGrid}>
            {[
              {
                key: 'probe-total',
                label: t('monitoring.codex_inspection_total_accounts'),
                value: activeRun ? String(activeRun.probeSetCount) : summaryBlankValue,
                meta: t('monitoring.server_codex_inspection_total_files', {
                  count: activeRun?.totalFiles ?? 0,
                }),
                icon: 'probe' as const,
                accent: 'blue' as const,
              },
              {
                key: 'sampled',
                label: t('monitoring.codex_inspection_sampled_accounts'),
                value: activeRun ? String(activeRun.sampledCount) : summaryBlankValue,
                meta: getRunStatusLabel(activeRun, t),
                icon: 'sampled' as const,
                accent: 'cyan' as const,
              },
              {
                key: 'delete',
                label: t('monitoring.codex_inspection_delete_count'),
                value: activeRun ? String(activeRun.deleteCount) : summaryBlankValue,
                meta: t('monitoring.codex_inspection_delete_meta'),
                tone: 'bad' as const,
                icon: 'delete' as const,
                accent: 'red' as const,
              },
              {
                key: 'disable',
                label: t('monitoring.codex_inspection_disable_count'),
                value: activeRun ? String(activeRun.disableCount) : summaryBlankValue,
                meta: `${t('monitoring.codex_inspection_threshold')} ${selectedConfig.usedPercentThreshold}%`,
                tone: 'warn' as const,
                icon: 'disable' as const,
                accent: 'amber' as const,
              },
              {
                key: 'enable',
                label: t('monitoring.codex_inspection_enable_count'),
                value: activeRun ? String(activeRun.enableCount) : summaryBlankValue,
                meta: t('monitoring.codex_inspection_enable_meta'),
                tone: 'good' as const,
                icon: 'enable' as const,
                accent: 'green' as const,
              },
              {
                key: 'reauth',
                label: t('monitoring.codex_inspection_reauth_count'),
                value: activeRun ? String(activeRun.reauthCount) : summaryBlankValue,
                meta: t('monitoring.codex_inspection_reauth_meta'),
                icon: 'reauth' as const,
                accent: 'violet' as const,
              },
            ].map((card) => {
              const tone: MonitoringSummaryCardProps['tone'] = card.tone;
              return (
                <MonitoringSummaryCard
                  key={card.key}
                  label={card.label}
                  value={card.value}
                  meta={card.meta}
                  icon={card.icon}
                  accent={card.accent}
                  tone={tone}
                />
              );
            })}
          </section>
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
              <div className={styles.scheduleTimePointFields}>
                <div className={styles.scheduleTimePointField}>
                  <Input
                    id="timePoints"
                    label={t('monitoring.server_codex_inspection_time_points')}
                    value={draft.timePoints}
                    onChange={(event) => updateDraft('timePoints', event.target.value)}
                    placeholder="09:00, 13:30, 22:00"
                    aria-describedby="timePoints-hint"
                  />
                </div>
                <div className={styles.scheduleTimePointField}>
                  <span className={styles.serverFieldLabel}>
                    {t('monitoring.server_codex_inspection_time_zone')}
                  </span>
                  <Select
                    value={draft.timeZone}
                    options={timeZoneOptions}
                    onChange={(value) => updateDraft('timeZone', value)}
                    ariaLabel={t('monitoring.server_codex_inspection_time_zone')}
                    triggerClassName={styles.configSelectTrigger}
                    dropdownClassName={styles.configSelectDropdown}
                  />
                </div>
                <div id="timePoints-hint" className={styles.scheduleTimePointHint}>
                  {t('monitoring.server_codex_inspection_time_points_hint')}
                </div>
              </div>
            )}
          </div>
        </section>

        <InspectionConfigFields
          draft={draft}
          errors={fieldErrors}
          t={t}
          onFieldChange={(field, value) => updateDraft(field, value)}
          onXaiInferenceEnabledChange={(value) => updateDraft('xaiInferenceEnabled', value)}
          onAutoActionModeChange={(value) => updateDraft('autoActionMode', value)}
          onAutoRecoverEnabledChange={(value) => updateDraft('autoRecoverEnabled', value)}
        />
      </InspectionConfigDrawer>
    );
  };

  const renderRunsPanel = () => (
    <Panel title={t('monitoring.server_codex_inspection_history_title')}>
      {currentRuns.length > 0 ? (
        <div
          className={styles.runHistoryList}
          role="tablist"
          aria-label={t('monitoring.server_codex_inspection_history_title')}
        >
          {currentRuns.map((run) => {
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
    const canExecuteActions = currentDetail?.run.status === 'completed';
    const resultsRun = currentDetail?.run ?? null;
    const resultsConfig = resolveServerCodexConfig(
      resultsRun?.settings ?? managerConfig?.codexInspection
    );
    const actionFilterCounts = getActionFilterCounts(resultItems);
    const handlingFilterCounts = countHandlingStates(resultItems);
    const panelResult: CodexInspectionRunResult | null = resultsRun
      ? {
          settings: {
            baseUrl: serviceBase,
            token: '',
            targetTypes: resultsConfig.targetTypes,
            targetType: resultsConfig.targetType,
            workers: resultsConfig.workers,
            deleteWorkers: resultsConfig.deleteWorkers,
            timeout: resultsConfig.timeout,
            retries: resultsConfig.retries,
            userAgent: resultsConfig.userAgent,
            xaiInferenceUserAgent: resultsConfig.xaiInferenceUserAgent,
            xaiInferenceEnabled: resultsConfig.xaiInferenceEnabled,
            xaiInferenceModel: resultsConfig.xaiInferenceModel,
            xaiInferencePrompt: resultsConfig.xaiInferencePrompt,
            usedPercentThreshold: resultsConfig.usedPercentThreshold,
            sampleSize: resultsConfig.sampleSize,
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
            usedPercentThreshold: resultsConfig.usedPercentThreshold,
            sampled: resultsConfig.sampleSize > 0,
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
        return null;
      }

      const actionStatus = normalizeServerCodexInspectionActionStatus(source);
      const terminalStatusLabel = formatServerTerminalActionStatusLabel(source, t);
      const actionError = source.actionError?.trim() ?? '';
      const needsReview = actionStatus === 'needs_review' || mixedActionIds.has(source.id);
      const hasFileLevelAction = isActionableServerCodexInspectionResult(source);
      const pendingReauth = isPendingServerReauthResult(source);
      const hasOperation =
        Boolean(terminalStatusLabel) ||
        canonicalExecutableIds.has(source.id) ||
        needsReview ||
        hasFileLevelAction ||
        pendingReauth;

      if (!hasOperation) return null;

      return (
        <div className={styles.serverResultOperation}>
          {terminalStatusLabel ? (
            <span className={styles.primaryReason}>{terminalStatusLabel}</span>
          ) : null}
          {actionError ? (
            <small className={styles.primaryReason} title={actionError}>
              {actionError}
            </small>
          ) : null}
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
          ) : needsReview ? (
            <span className={styles.primaryReason}>
              {t('monitoring.server_codex_inspection_action_needs_review_hint')}
            </span>
          ) : hasFileLevelAction ? (
            <span className={styles.primaryReason}>
              {t('monitoring.server_codex_inspection_file_level_action_hint')}
            </span>
          ) : pendingReauth ? (
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
          ) : null}
        </div>
      );
    };

    return (
      <CodexInspectionResultsPanel
        result={panelResult}
        filteredResults={resultPagination.pageItems}
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
        xaiInferenceEnabled={resultsConfig.xaiInferenceEnabled}
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
        onOpenCredential={
          onOpenCredential
            ? (item) =>
                onOpenCredential({
                  fileName: item.fileName,
                  runtimeId: item.runtimeId ?? null,
                  provider: item.provider,
                  authIndex: item.authIndex ?? null,
                  accountId: item.accountId ?? null,
                  accountSnapshot: item.accountSnapshot ?? null,
                })
            : undefined
        }
        filterLabel={filterLabel}
        handlingFilterLabel={handlingFilterLabel}
        renderOperation={renderOperation}
      />
    );
  };

  const scrollLogsToBottom = useCallback(() => {
    const element = logListRef.current;
    if (!element) return;
    element.scrollTop = element.scrollHeight;
  }, []);

  useEffect(() => {
    if (logsCollapsed) return;
    const runId = currentDetail?.run.id ?? null;
    const latestLogId = currentDetail?.logs[currentDetail.logs.length - 1]?.id ?? null;
    const previous = previousServerLogCursorRef.current;
    previousServerLogCursorRef.current = { runId, latestLogId };
    if (latestLogId === null) return;
    if (previous.runId === runId && previous.latestLogId === latestLogId) return;
    scrollLogsToBottom();
  }, [currentDetail?.logs, currentDetail?.run.id, logsCollapsed, scrollLogsToBottom]);

  const handleJumpToLatestLog = useCallback(() => {
    if (logsCollapsed) {
      setLogsCollapsed(false);
      requestAnimationFrame(scrollLogsToBottom);
      return;
    }
    scrollLogsToBottom();
  }, [logsCollapsed, scrollLogsToBottom]);

  const handleCopyLogs = useCallback(async () => {
    if (serverLogEntries.length === 0) return;
    try {
      await navigator.clipboard.writeText(formatInspectionLogsForClipboard(serverLogEntries));
      showNotification(t('monitoring.codex_inspection_logs_copied'), 'success');
    } catch {
      showNotification(t('monitoring.codex_inspection_logs_copy_failed'), 'error');
    }
  }, [serverLogEntries, showNotification, t]);

  return (
    <div className={styles.page} data-embedded={embedded || undefined}>
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
          {currentDetail?.run.error ? (
            <div className={styles.serverError} role="alert">
              {currentDetail.run.error}
            </div>
          ) : null}
          {renderResultsPanel()}
          <CodexInspectionLogsPanel
            logs={serverLogEntries}
            logsCollapsed={logsCollapsed}
            levelFilter={logLevelFilter}
            logListRef={logListRef}
            locale={i18n.language}
            t={t}
            onLevelFilterChange={setLogLevelFilter}
            onCopyLogs={() => void handleCopyLogs()}
            onJumpToLatest={handleJumpToLatestLog}
            onToggleCollapsed={() => setLogsCollapsed((previous) => !previous)}
          />
        </div>
      </div>
      {renderConfigDrawer()}
      <CodexReauthDialog
        open={Boolean(codexReauthTarget)}
        target={codexReauthTarget}
        requestScope={authFilesRequestScope}
        onClose={() => setCodexReauthTarget(null)}
        onSuccess={handleCodexReauthSuccess}
      />
    </div>
  );
}
