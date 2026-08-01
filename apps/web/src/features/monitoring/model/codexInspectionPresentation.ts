import type { TFunction } from 'i18next';
import {
  type CodexInspectionAction,
  type CodexInspectionAutoActionMode,
  type CodexInspectionConfigurableSettings,
  type CodexInspectionLogLevel,
  type CodexInspectionProgressSnapshot,
  type CodexInspectionResultItem,
  type CodexInspectionRunResult,
  type CodexInspectionStoredActionFilter,
  type CodexInspectionStoredLogEntry,
} from '@/features/monitoring/codexInspection';
import type {
  CodexInspectionLog,
  CodexInspectionResult,
  CodexInspectionRun,
} from '@/services/api/usageService';
import { formatQuotaResetTime } from '@/utils/quota/formatters';
import { formatXaiProbeIssue } from '@/utils/quota/xaiPresentation';
import {
  codexInspectionTargetTypesToSelection,
  normalizeCodexInspectionTargetTypes,
} from './codexInspectionSettings';
import {
  getCodexInspectionOwnershipIdentityKey,
  hasCodexInspectionStableIdentity,
} from './codexInspectionOwnership';

export type RunStatus = 'idle' | 'running' | 'paused' | 'success' | 'error';

export type ActionFilter = CodexInspectionStoredActionFilter;
export type HandlingFilter = 'all' | 'pending' | 'no_action';

export type StatusTone = 'idle' | 'info' | 'good' | 'warn' | 'bad';

export type InspectionLogEntry = CodexInspectionStoredLogEntry;
export type InspectionLogLevelFilter = 'all' | CodexInspectionLogLevel;
export type InspectionLogViewEntry = {
  id: string | number;
  level: CodexInspectionLogLevel;
  message: string;
  timestamp: number;
  detail?: string;
};

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
  autoActionMode: CodexInspectionAutoActionMode;
  autoRecoverEnabled: boolean;
};

export type InspectionSettingsDraftField = Exclude<
  keyof InspectionSettingsDraft,
  'autoActionMode' | 'autoRecoverEnabled' | 'xaiInferenceEnabled'
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

export const formatInspectionTargetLabel = (targetTypes: unknown, t: TFunction): string => {
  const normalized = normalizeCodexInspectionTargetTypes(targetTypes);
  if (normalized.includes('codex') && normalized.includes('xai')) {
    return t('monitoring.codex_inspection_target_codex_xai');
  }
  if (normalized.includes('xai')) return t('monitoring.codex_inspection_target_xai');
  return t('monitoring.codex_inspection_target_codex');
};

const ISO_QUOTA_RESET_PATTERN = /^\d{4}-\d{2}-\d{2}T/;

export const formatInspectionQuotaResetLabel = (value?: string): string => {
  const normalized = value?.trim() ?? '';
  if (!normalized || normalized === '-') return '';
  if (!ISO_QUOTA_RESET_PATTERN.test(normalized)) return normalized;
  const formatted = formatQuotaResetTime(normalized);
  return formatted === '-' ? normalized : formatted;
};

export const toSettingsDraft = (
  settings: CodexInspectionConfigurableSettings
): InspectionSettingsDraft => ({
  targetTypes: codexInspectionTargetTypesToSelection(settings.targetTypes, settings.targetType),
  workers: String(settings.workers),
  deleteWorkers: String(settings.deleteWorkers),
  timeout: String(settings.timeout),
  retries: String(settings.retries),
  userAgent: settings.userAgent,
  xaiInferenceUserAgent: settings.xaiInferenceUserAgent,
  xaiInferenceEnabled: settings.xaiInferenceEnabled,
  xaiInferenceModel: settings.xaiInferenceModel,
  xaiInferencePrompt: settings.xaiInferencePrompt,
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

const isCodexInspectionActionValue = (value: unknown): value is CodexInspectionAction =>
  value === 'delete' ||
  value === 'disable' ||
  value === 'enable' ||
  value === 'reauth' ||
  value === 'keep';

const SERVER_CODEX_INSPECTION_LOG_MESSAGE_KEYS: Record<string, string> = {
  凭证健康巡检开始: 'monitoring.server_codex_inspection_log_message_run_started',
  加载认证文件列表失败: 'monitoring.server_codex_inspection_log_message_auth_files_failed',
  凭证健康巡检集合已准备: 'monitoring.server_codex_inspection_log_message_set_ready',
  凭证健康巡检已取消: 'monitoring.server_codex_inspection_log_message_cancelled',
  凭证健康巡检已中断: 'monitoring.server_codex_inspection_log_message_interrupted',
  凭证健康巡检生命周期已收尾: 'monitoring.server_codex_inspection_log_message_lifecycle_finalized',
  '服务重启或任务租约过期，巡检已中断':
    'monitoring.server_codex_inspection_log_message_recovered_interrupted',
  服务关闭导致巡检未能启动: 'monitoring.server_codex_inspection_log_message_shutdown_before_start',
  '本地巡检状态冲突，任务未能启动':
    'monitoring.server_codex_inspection_log_message_local_conflict_before_start',
  凭证健康巡检完成: 'monitoring.server_codex_inspection_log_message_completed',
  自动处理账号开始: 'monitoring.server_codex_inspection_log_message_auto_started',
  自动处理账号完成: 'monitoring.server_codex_inspection_log_message_auto_completed',
  手动处理账号开始: 'monitoring.server_codex_inspection_log_message_manual_started',
  手动处理账号跳过: 'monitoring.server_codex_inspection_log_message_manual_skipped',
  手动处理账号完成: 'monitoring.server_codex_inspection_log_message_manual_completed',
  写入巡检账号结果失败: 'monitoring.server_codex_inspection_log_message_result_write_failed',
  '账号缺少 auth_index，跳过探测':
    'monitoring.server_codex_inspection_log_message_missing_auth_index',
  '账号探测异常，保留账号': 'monitoring.server_codex_inspection_log_message_probe_failed',
  '账号探测未返回 status_code，保留账号':
    'monitoring.server_codex_inspection_log_message_missing_status',
  账号探测完成: 'monitoring.server_codex_inspection_log_message_probe_completed',
  自动处理账号失败: 'monitoring.server_codex_inspection_log_message_auto_action_failed',
  自动处理账号成功: 'monitoring.server_codex_inspection_log_message_auto_action_succeeded',
  自动处理账号跳过: 'monitoring.server_codex_inspection_log_message_auto_action_skipped',
  自动处理账号校验失败: 'monitoring.server_codex_inspection_log_message_auto_validation_failed',
  手动处理账号失败: 'monitoring.server_codex_inspection_log_message_manual_action_failed',
  手动处理账号成功: 'monitoring.server_codex_inspection_log_message_manual_action_succeeded',
  '加载巡检禁用所有权失败，自动恢复将保持关闭':
    'monitoring.server_codex_inspection_log_message_ownership_failed',
  清理巡检禁用所有权失败: 'monitoring.server_codex_inspection_log_message_ownership_cleanup_failed',
  手动处理账号校验失败: 'monitoring.server_codex_inspection_log_message_manual_validation_failed',
};

export const formatServerCodexInspectionLogMessage = (message: string, t: TFunction): string => {
  if (message.startsWith('monitoring.')) return t(message);
  const key = SERVER_CODEX_INSPECTION_LOG_MESSAGE_KEYS[message];
  return key ? t(key) : message;
};

const formatXaiServerHealthEvidence = (
  value: string,
  surface: 'billing' | 'inference',
  t: TFunction
) => {
  switch (value) {
    case 'billing_healthy':
      return t('monitoring.xai_inspection_evidence_billing_healthy');
    case 'billing_partial':
      return t('monitoring.xai_inspection_evidence_billing_partial');
    case 'basic_healthy':
      return t('monitoring.xai_inspection_evidence_basic_healthy');
    case 'official_api_healthy':
      return t('monitoring.xai_inspection_evidence_official_api_healthy');
    case 'inference_healthy':
      return t('monitoring.xai_inspection_evidence_inference_healthy');
    default:
      return formatXaiProbeIssue(value, t, surface) ?? value;
  }
};

type XaiInspectionMode = 'billing' | 'identity' | 'inference' | 'skipped';

const isXaiInspectionLogDetail = (record: Record<string, unknown>): boolean =>
  String(record.provider ?? '')
    .trim()
    .toLowerCase() === 'xai' ||
  'inferenceEnabled' in record ||
  'inferenceHealthy' in record ||
  'billingPartial' in record;

const resolveXaiInspectionMode = (record: Record<string, unknown>): XaiInspectionMode => {
  const explicitMode = String(record.inspectionMode ?? '')
    .trim()
    .toLowerCase();
  if (
    explicitMode === 'billing' ||
    explicitMode === 'identity' ||
    explicitMode === 'inference' ||
    explicitMode === 'skipped'
  ) {
    return explicitMode;
  }

  const evidence = String(record.healthEvidence ?? '')
    .trim()
    .toLowerCase();
  if (evidence === 'official_api_healthy') return 'identity';
  if (evidence === 'missing_auth_index') return 'skipped';
  if (record.inferenceEnabled === true) return 'inference';
  if (record.inferenceEnabled === false) return 'billing';
  if (typeof record.inferenceHealthy === 'boolean') return 'inference';
  return 'billing';
};

const resolveXaiHealthEvidence = (
  record: Record<string, unknown>,
  mode: XaiInspectionMode
): string => {
  const explicitEvidence = String(record.healthEvidence ?? '').trim();
  if (explicitEvidence) return explicitEvidence;
  if (mode === 'skipped') return 'missing_auth_index';
  if (mode === 'identity') return 'official_api_healthy';
  if (mode === 'inference' && record.inferenceHealthy === true) return 'inference_healthy';

  const action = String(record.action ?? '')
    .trim()
    .toLowerCase();
  if (
    mode === 'billing' &&
    record.billingPartial === true &&
    (action === '' || action === 'keep' || action === 'enable')
  ) {
    return 'billing_partial';
  }
  if (
    mode === 'billing' &&
    record.inferenceEnabled === false &&
    record.billingPartial === false &&
    action === 'keep'
  ) {
    return 'basic_healthy';
  }
  return 'unknown';
};

export const formatInspectionLogDetail = (detail: unknown, t: TFunction): string => {
  if (typeof detail === 'string') return detail;
  if (detail === null || detail === undefined) return '';
  if (typeof detail === 'object' && !Array.isArray(detail)) {
    const record = detail as Record<string, unknown>;
    const normalized = { ...record };
    if (isCodexInspectionActionValue(record.action)) {
      normalized.action = formatActionLabel(record.action, t);
    }
    if (Array.isArray(record.actionErrors)) {
      normalized.actionErrors = record.actionErrors.map((item) => {
        if (!item || typeof item !== 'object' || Array.isArray(item)) return item;
        const actionError = item as Record<string, unknown>;
        return {
          ...actionError,
          ...(isCodexInspectionActionValue(actionError.action)
            ? { action: formatActionLabel(actionError.action, t) }
            : {}),
        };
      });
    }
    if (isXaiInspectionLogDetail(record)) {
      const mode = resolveXaiInspectionMode(record);
      const evidence = resolveXaiHealthEvidence(record, mode);
      normalized.provider = 'xai';
      normalized.inspectionMode = t(
        mode === 'inference'
          ? 'monitoring.server_codex_inspection_log_mode_inference'
          : mode === 'identity'
            ? 'monitoring.server_codex_inspection_log_mode_identity'
            : mode === 'skipped'
              ? 'monitoring.server_codex_inspection_log_mode_skipped'
              : 'monitoring.server_codex_inspection_log_mode_billing'
      );
      normalized.healthEvidence = formatXaiServerHealthEvidence(
        evidence,
        mode === 'inference' ? 'inference' : 'billing',
        t
      );
      for (const key of [
        'billingAvailable',
        'billingPartial',
        'inferenceEnabled',
        'inferenceHealthy',
      ] as const) {
        if (typeof record[key] === 'boolean') {
          normalized[key] = t(record[key] ? 'common.yes' : 'common.no');
        }
      }
    }
    return JSON.stringify(normalized);
  }
  return JSON.stringify(detail) ?? String(detail);
};

export const formatServerCodexInspectionLogDetail = formatInspectionLogDetail;

const readServerLogDetailRecord = (detail: unknown): Record<string, unknown> =>
  detail && typeof detail === 'object' && !Array.isArray(detail)
    ? (detail as Record<string, unknown>)
    : {};

const readServerLogString = (record: Record<string, unknown>, ...keys: string[]): string => {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return '';
};

const readServerLogNumber = (record: Record<string, unknown>, key: string): number | null => {
  const value = record[key];
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
};

const matchesServerLogEvent = (message: string, sourceMessage: string): boolean =>
  message === sourceMessage || message === SERVER_CODEX_INSPECTION_LOG_MESSAGE_KEYS[sourceMessage];

const formatServerLogAction = (record: Record<string, unknown>, t: TFunction): string =>
  isCodexInspectionActionValue(record.action)
    ? formatActionLabel(record.action, t)
    : readServerLogString(record, 'action') || t('monitoring.codex_inspection_action_keep');

const formatServerCodexAccountLogSummary = (
  record: Record<string, unknown>,
  t: TFunction
): string =>
  t('monitoring.codex_inspection_log_result', {
    account: readServerLogString(record, 'displayAccount', 'fileName') || '-',
    action: formatServerLogAction(record, t),
    status: readServerLogNumber(record, 'statusCode') ?? '--',
    percent: formatPercent(readServerLogNumber(record, 'usedPercent')),
  });

const formatServerXaiAccountLogSummary = (
  record: Record<string, unknown>,
  t: TFunction
): string => {
  const account = readServerLogString(record, 'displayAccount', 'fileName') || '-';
  const action = formatServerLogAction(record, t);
  const mode = resolveXaiInspectionMode(record);
  const evidence = resolveXaiHealthEvidence(record, mode);
  const evidenceLabel = formatXaiServerHealthEvidence(
    evidence,
    mode === 'inference' ? 'inference' : 'billing',
    t
  );
  if (
    evidence === 'billing_healthy' ||
    evidence === 'billing_partial' ||
    evidence === 'basic_healthy' ||
    evidence === 'official_api_healthy' ||
    evidence === 'inference_healthy'
  ) {
    return t('monitoring.xai_inspection_log_result', {
      account,
      action,
      evidence: evidenceLabel,
      percent: formatPercent(readServerLogNumber(record, 'usedPercent')),
    });
  }
  return t('monitoring.xai_inspection_log_classified', {
    account,
    action,
    surface: t(
      mode === 'inference'
        ? 'monitoring.xai_inspection_surface_inference'
        : 'monitoring.xai_inspection_surface_billing'
    ),
    reason: evidenceLabel,
  });
};

export const formatServerCodexInspectionLogSummary = (
  entry: Pick<CodexInspectionLog, 'message' | 'detail'>,
  run: CodexInspectionRun | null | undefined,
  t: TFunction
): string => {
  const record = readServerLogDetailRecord(entry.detail);
  if (matchesServerLogEvent(entry.message, '凭证健康巡检开始')) {
    const targetTypes = Array.isArray(record.targetTypes)
      ? record.targetTypes
      : run?.settings?.targetTypes;
    return t('monitoring.codex_inspection_log_loading', {
      target: formatInspectionTargetLabel(targetTypes, t),
    });
  }
  if (matchesServerLogEvent(entry.message, '凭证健康巡检集合已准备')) {
    return t('monitoring.codex_inspection_log_set_ready', {
      total: readServerLogNumber(record, 'probeSetCount') ?? run?.probeSetCount ?? 0,
      sampled: readServerLogNumber(record, 'sampledCount') ?? run?.sampledCount ?? 0,
    });
  }
  if (matchesServerLogEvent(entry.message, '加载认证文件列表失败')) {
    return t('monitoring.codex_inspection_log_auth_files_failed', {
      message: readServerLogString(record, 'error', 'reason') || t('common.unknown_error'),
    });
  }
  if (matchesServerLogEvent(entry.message, '凭证健康巡检已取消')) {
    return t('monitoring.codex_inspection_log_cancelled', {
      message: readServerLogString(record, 'error') || t('common.unknown_error'),
    });
  }
  if (matchesServerLogEvent(entry.message, '手动处理账号开始')) {
    return t('monitoring.codex_inspection_log_manual_started', {
      requested: readServerLogNumber(record, 'requestedCount') ?? 0,
      actions: readServerLogNumber(record, 'actionCount') ?? 0,
    });
  }
  if (matchesServerLogEvent(entry.message, '自动处理账号开始')) {
    return t('monitoring.codex_inspection_log_auto_started', {
      requested: readServerLogNumber(record, 'requestedCount') ?? 0,
      actions: readServerLogNumber(record, 'actionCount') ?? 0,
    });
  }
  if (
    matchesServerLogEvent(entry.message, '自动处理账号跳过') ||
    matchesServerLogEvent(entry.message, '手动处理账号跳过')
  ) {
    return t(
      record.status === 'needs_review'
        ? 'monitoring.codex_inspection_log_action_needs_review'
        : 'monitoring.codex_inspection_log_action_skipped',
      {
        account: readServerLogString(record, 'displayAccount', 'fileName') || '-',
        action: formatServerLogAction(record, t),
        message: readServerLogString(record, 'reason', 'error') || t('common.unknown_error'),
      }
    );
  }
  if (matchesServerLogEvent(entry.message, '手动处理账号完成')) {
    return t('monitoring.codex_inspection_log_manual_completed', {
      success: readServerLogNumber(record, 'successCount') ?? 0,
      skipped: readServerLogNumber(record, 'skippedCount') ?? 0,
      review: readServerLogNumber(record, 'needsReviewCount') ?? 0,
      failed: readServerLogNumber(record, 'failedCount') ?? 0,
    });
  }
  if (matchesServerLogEvent(entry.message, '自动处理账号完成')) {
    return t('monitoring.codex_inspection_log_auto_completed', {
      success: readServerLogNumber(record, 'successCount') ?? 0,
      skipped: readServerLogNumber(record, 'skippedCount') ?? 0,
      review: readServerLogNumber(record, 'needsReviewCount') ?? 0,
      failed: readServerLogNumber(record, 'failedCount') ?? 0,
      remaining: readServerLogNumber(record, 'remainingCount') ?? 0,
    });
  }
  if (matchesServerLogEvent(entry.message, '写入巡检账号结果失败')) {
    return t(
      record.retryScheduled === true
        ? 'monitoring.codex_inspection_log_result_write_retry'
        : 'monitoring.codex_inspection_log_result_write_failed',
      {
        account: readServerLogString(record, 'displayAccount', 'fileName') || '-',
        message: readServerLogString(record, 'error') || t('common.unknown_error'),
      }
    );
  }
  if (matchesServerLogEvent(entry.message, '加载巡检禁用所有权失败，自动恢复将保持关闭')) {
    return t('monitoring.codex_inspection_log_ownership_failed', {
      message: readServerLogString(record, 'error') || t('common.unknown_error'),
    });
  }
  if (
    matchesServerLogEvent(entry.message, '自动处理账号校验失败') ||
    matchesServerLogEvent(entry.message, '手动处理账号校验失败')
  ) {
    return t('monitoring.codex_inspection_log_manual_validation_failed', {
      account: readServerLogString(record, 'displayAccount', 'fileName') || '-',
      action: formatServerLogAction(record, t),
      message: readServerLogString(record, 'error') || t('common.unknown_error'),
    });
  }
  if (matchesServerLogEvent(entry.message, '账号缺少 auth_index，跳过探测')) {
    return t('monitoring.codex_inspection_log_missing_auth_index', {
      account: readServerLogString(record, 'displayAccount', 'fileName') || '-',
    });
  }
  if (matchesServerLogEvent(entry.message, '账号探测异常，保留账号')) {
    return t('monitoring.codex_inspection_log_request_error', {
      account: readServerLogString(record, 'displayAccount', 'fileName') || '-',
      message: readServerLogString(record, 'error') || t('common.unknown_error'),
    });
  }
  if (matchesServerLogEvent(entry.message, '账号探测未返回 status_code，保留账号')) {
    return t('monitoring.codex_inspection_log_missing_status', {
      account: readServerLogString(record, 'displayAccount', 'fileName') || '-',
    });
  }
  if (matchesServerLogEvent(entry.message, '账号探测完成')) {
    return formatServerCodexAccountLogSummary(record, t);
  }
  if (entry.message === 'monitoring.xai_inspection_log_server_complete') {
    return formatServerXaiAccountLogSummary(record, t);
  }
  if (entry.message === 'monitoring.xai_inspection_log_server_missing_auth_index') {
    return t('monitoring.xai_inspection_log_missing_auth_index', {
      account: readServerLogString(record, 'displayAccount', 'fileName') || '-',
    });
  }
  if (matchesServerLogEvent(entry.message, '凭证健康巡检完成')) {
    return t('monitoring.codex_inspection_log_completed', {
      delete: readServerLogNumber(record, 'deleteCount') ?? run?.deleteCount ?? 0,
      disable: readServerLogNumber(record, 'disableCount') ?? run?.disableCount ?? 0,
      enable: readServerLogNumber(record, 'enableCount') ?? run?.enableCount ?? 0,
      reauth: readServerLogNumber(record, 'reauthCount') ?? run?.reauthCount ?? 0,
      keep: readServerLogNumber(record, 'keepCount') ?? run?.keepCount ?? 0,
    });
  }
  if (
    matchesServerLogEvent(entry.message, '自动处理账号成功') ||
    matchesServerLogEvent(entry.message, '手动处理账号成功')
  ) {
    return t('monitoring.codex_inspection_log_action_success', {
      account: readServerLogString(record, 'displayAccount', 'fileName') || '-',
      action: formatServerLogAction(record, t),
    });
  }
  if (
    matchesServerLogEvent(entry.message, '自动处理账号失败') ||
    matchesServerLogEvent(entry.message, '手动处理账号失败')
  ) {
    return t('monitoring.codex_inspection_log_action_failed', {
      account: readServerLogString(record, 'displayAccount', 'fileName') || '-',
      action: formatServerLogAction(record, t),
      message: readServerLogString(record, 'error', 'reason') || t('common.unknown_error'),
    });
  }
  return formatServerCodexInspectionLogMessage(entry.message, t);
};

export const normalizeInspectionLogLevel = (value: unknown): CodexInspectionLogLevel => {
  if (value === 'success' || value === 'warning' || value === 'error') return value;
  return 'info';
};

export const toServerInspectionLogViewEntry = (
  entry: CodexInspectionLog,
  run: CodexInspectionRun | null | undefined,
  t: TFunction
): InspectionLogViewEntry => {
  const detail = formatInspectionLogDetail(entry.detail, t);
  return {
    id: entry.id,
    level: normalizeInspectionLogLevel(entry.level),
    message: formatServerCodexInspectionLogSummary(entry, run, t),
    timestamp: entry.createdAtMs,
    ...(detail ? { detail } : {}),
  };
};

export const toLocalInspectionLogViewEntry = (
  entry: InspectionLogEntry,
  t: TFunction
): InspectionLogViewEntry => {
  const detail = formatInspectionLogDetail(entry.detail, t);
  return {
    id: entry.id,
    level: entry.level,
    message: entry.message,
    timestamp: entry.timestamp,
    ...(detail ? { detail } : {}),
  };
};

export const formatInspectionLogsForClipboard = (logs: readonly InspectionLogViewEntry[]): string =>
  logs
    .map((entry) => {
      const detail = entry.detail ? ` ${entry.detail}` : '';
      return `[${new Date(entry.timestamp).toISOString()}] [${entry.level}] ${entry.message}${detail}`;
    })
    .join('\n');

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
  item: Pick<CodexInspectionResult, 'id' | 'action' | 'actionStatus'> &
    Partial<
      Pick<
        CodexInspectionResult,
        'fileName' | 'provider' | 'authIndex' | 'accountId' | 'accountSnapshot'
      >
    >
) => {
  const status = normalizeServerCodexInspectionActionStatus(item);
  return (
    item.id > 0 &&
    isServerCodexInspectionAction(item.action) &&
    (status === 'pending' || status === 'failed') &&
    hasCodexInspectionStableIdentity({
      fileName: item.fileName ?? '',
      provider: item.provider,
      authIndex: item.authIndex,
      accountId: item.accountId,
      accountSnapshot: item.accountSnapshot,
    })
  );
};

export const isPendingServerReauthResult = (
  item: Pick<CodexInspectionResult, 'action' | 'actionStatus' | 'executedAction'>
) => {
  if (item.action !== 'reauth' || item.executedAction === 'delete') return false;
  const status = normalizeServerCodexInspectionActionStatus(item);
  return status === 'none' || status === 'pending' || status === 'failed';
};

export const isHandledServerCodexInspectionResult = (
  item: Pick<CodexInspectionResult, 'action' | 'actionStatus' | 'executedAction'>
) => {
  const status = normalizeServerCodexInspectionActionStatus(item);
  if (status === 'success' || status === 'skipped') return true;
  return item.action === 'reauth' && item.executedAction === 'delete';
};

type ServerActionIdentity = Pick<CodexInspectionResult, 'id' | 'fileName' | 'action'> &
  Partial<
    Pick<
      CodexInspectionResult,
      'provider' | 'authIndex' | 'accountId' | 'accountSnapshot' | 'actionStatus'
    >
  >;

const getServerActionIdentityKey = (item: ServerActionIdentity) =>
  getCodexInspectionOwnershipIdentityKey({
    fileName: item.fileName,
    provider: item.provider,
    authIndex: item.authIndex,
    accountId: item.accountId,
    accountSnapshot: item.accountSnapshot,
  });

const buildServerActionGroups = <T extends ServerActionIdentity>(results: T[]): T[][] => {
  const groups: T[][] = [];
  const itemsByFile = new Map<string, T[]>();
  results.forEach((item) => {
    const fileName = item.fileName.trim();
    if (!fileName) return;
    const fileItems = itemsByFile.get(fileName) ?? [];
    fileItems.push(item);
    itemsByFile.set(fileName, fileItems);
  });
  itemsByFile.forEach((allFileItems) => {
    const fileItems = allFileItems.filter((item) => isServerCodexInspectionAction(item.action));
    if (fileItems.length === 0) return;
    if (fileItems.some((item) => item.action === 'delete')) {
      groups.push(allFileItems);
      return;
    }
    const identityGroups = new Map<string, T[]>();
    fileItems.forEach((item) => {
      const identityKey = getServerActionIdentityKey(item);
      const identityItems = identityGroups.get(identityKey) ?? [];
      identityItems.push(item);
      identityGroups.set(identityKey, identityItems);
    });
    groups.push(...identityGroups.values());
  });
  return groups;
};

export const getCanonicalServerCodexInspectionActionIds = (
  results: Array<
    Pick<CodexInspectionResult, 'id' | 'fileName' | 'action' | 'actionStatus'> &
      Partial<
        Pick<CodexInspectionResult, 'provider' | 'authIndex' | 'accountId' | 'accountSnapshot'>
      >
  >
) => {
  const canonicalIds = new Set<number>();
  for (const group of buildServerActionGroups(results)) {
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
  results: Array<
    Pick<CodexInspectionResult, 'id' | 'fileName' | 'action'> &
      Partial<
        Pick<CodexInspectionResult, 'provider' | 'authIndex' | 'accountId' | 'accountSnapshot'>
      >
  >
) => {
  const mixedIds = new Set<number>();
  for (const group of buildServerActionGroups(results)) {
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

export const getVisibleActionFilters = (): ActionFilter[] => [...ACTION_FILTERS];

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

export type XaiInferenceState = 'success' | 'failed' | 'skipped' | 'not-applicable';

export type InspectionProbeSource =
  | 'codex_usage'
  | 'xai_billing'
  | 'xai_identity'
  | 'xai_inference';

export type InspectionProbeState = 'success' | 'failed' | 'skipped';

export type InspectionProbePresentation = {
  source: InspectionProbeSource;
  state: InspectionProbeState;
  statusCode: number | null;
};

const XAI_BILLING_HEALTHY_KINDS = new Set(['billing_healthy', 'billing_partial']);
const XAI_IDENTITY_HEALTHY_KINDS = new Set(['identity_healthy', 'official_api_healthy']);
const HEALTHY_KEEP_KINDS = new Set([
  '',
  'billing_healthy',
  'inference_healthy',
  'identity_healthy',
  'official_api_healthy',
]);

export const shouldShowInspectionConclusionReason = (
  item: Pick<CodexInspectionResultItem, 'action' | 'disabled' | 'errorKind' | 'statusCode'>
): boolean => {
  if (item.action !== 'keep' || item.disabled) return true;
  const errorKind = item.errorKind?.trim() ?? '';
  if (!HEALTHY_KEEP_KINDS.has(errorKind)) return true;
  const statusCode = item.statusCode ?? null;
  return statusCode !== null && (statusCode < 200 || statusCode >= 300);
};

export const getInspectionProbePresentation = (
  item: Pick<CodexInspectionResultItem, 'provider' | 'statusCode' | 'errorKind'>,
  options: { xaiInferenceEnabled?: boolean } = {}
): InspectionProbePresentation => {
  const provider = item.provider.trim().toLowerCase();
  const errorKind = item.errorKind ?? '';
  const statusCode = item.statusCode ?? null;

  if (provider !== 'xai') {
    if (errorKind === 'missing_auth_index') {
      return { source: 'codex_usage', state: 'skipped', statusCode };
    }
    const succeeded = statusCode !== null && statusCode >= 200 && statusCode < 300 && !errorKind;
    return { source: 'codex_usage', state: succeeded ? 'success' : 'failed', statusCode };
  }

  if (errorKind === 'missing_auth_index') {
    return {
      source: options.xaiInferenceEnabled ? 'xai_inference' : 'xai_billing',
      state: 'skipped',
      statusCode,
    };
  }
  if (errorKind === 'inference_healthy') {
    return { source: 'xai_inference', state: 'success', statusCode };
  }
  if (XAI_BILLING_HEALTHY_KINDS.has(errorKind)) {
    return { source: 'xai_billing', state: 'success', statusCode };
  }
  if (XAI_IDENTITY_HEALTHY_KINDS.has(errorKind)) {
    return { source: 'xai_identity', state: 'success', statusCode };
  }
  return {
    source: options.xaiInferenceEnabled ? 'xai_inference' : 'xai_billing',
    state: 'failed',
    statusCode,
  };
};

export const getXaiInferenceState = (
  item: Pick<CodexInspectionResultItem, 'provider' | 'errorKind'>
): XaiInferenceState => {
  if (item.provider.trim().toLowerCase() !== 'xai') return 'not-applicable';
  if (item.errorKind === 'missing_auth_index') return 'skipped';
  if (item.errorKind === 'inference_healthy') return 'success';
  if (
    item.errorKind === 'billing_healthy' ||
    item.errorKind === 'billing_partial' ||
    item.errorKind === 'identity_healthy' ||
    item.errorKind === 'official_api_healthy'
  ) {
    return 'skipped';
  }
  return 'failed';
};

export const summarizeInspectionError = (
  item: Pick<
    CodexInspectionResultItem,
    'provider' | 'action' | 'statusCode' | 'errorKind' | 'error' | 'errorDetail'
  >,
  t: TFunction,
  options: { xaiInferenceEnabled?: boolean } = {}
) => {
  if (item.action === 'reauth' || item.statusCode === 401) {
    return t('monitoring.codex_inspection_error_summary_reauth');
  }
  if (
    item.errorKind === 'billing_healthy' ||
    item.errorKind === 'billing_partial' ||
    item.errorKind === 'inference_healthy' ||
    item.errorKind === 'identity_healthy' ||
    item.errorKind === 'official_api_healthy'
  ) {
    return '';
  }
  if (item.errorKind) {
    const xaiIssue =
      item.provider.trim().toLowerCase() === 'xai'
        ? formatXaiProbeIssue(
            item.errorKind,
            t,
            options.xaiInferenceEnabled ? 'inference' : 'billing'
          )
        : null;
    if (xaiIssue) return xaiIssue;
    switch (item.errorKind) {
      case 'http_status':
        return t('monitoring.codex_inspection_error_summary_http_status');
      case 'missing_status':
        return t('monitoring.codex_inspection_error_summary_missing_status');
      case 'request_error':
        return t('monitoring.codex_inspection_error_summary_request_error');
      case 'missing_auth_index':
        return t('xai_quota.diagnostic_missing_auth_index');
      case 'quota':
        return t('monitoring.codex_inspection_error_summary_quota');
      default:
        return t('monitoring.codex_inspection_error_summary_response');
    }
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
  | 'targetTypes'
  | 'usedPercentThreshold'
  | 'sampleSize'
  | 'workers'
  | 'deleteWorkers'
  | 'timeout'
  | 'retries'
  | 'userAgent'
  | 'xaiInferenceUserAgent'
  | 'xaiInferenceModel'
  | 'xaiInferencePrompt';

export type SharedInspectionConfigDraft = {
  [K in SharedInspectionConfigField]: string;
} & {
  autoActionMode: CodexInspectionAutoActionMode | string;
  autoRecoverEnabled: boolean;
  xaiInferenceEnabled: boolean;
};

export type InspectionConfigFieldErrors = Partial<Record<SharedInspectionConfigField, string>>;

export const getInspectionUserAgentVisibility = (
  targetTypes: unknown,
  xaiInferenceEnabled: boolean
) => {
  const normalizedTargets = normalizeCodexInspectionTargetTypes(targetTypes);
  return {
    codex: normalizedTargets.includes('codex'),
    xaiInference: normalizedTargets.includes('xai') && xaiInferenceEnabled,
  };
};

export type ValidatedInspectionConfigValues = {
  targetTypes: string[];
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

  if (normalizeCodexInspectionTargetTypes(draft.targetTypes).length === 0) {
    errors.targetTypes = t('monitoring.codex_inspection_settings_target_type_required');
  }
  if (
    normalizeCodexInspectionTargetTypes(draft.targetTypes).includes('xai') &&
    draft.xaiInferenceEnabled
  ) {
    if (!draft.xaiInferenceModel.trim()) {
      errors.xaiInferenceModel = t('monitoring.codex_inspection_settings_xai_model_required');
    }
    if (!draft.xaiInferencePrompt.trim()) {
      errors.xaiInferencePrompt = t('monitoring.codex_inspection_settings_xai_prompt_required');
    }
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
      targetTypes: normalizeCodexInspectionTargetTypes(draft.targetTypes),
      workers: Number(draft.workers.trim()),
      deleteWorkers: Number(draft.deleteWorkers.trim()),
      timeout: Number(draft.timeout.trim()),
      retries: Number(draft.retries.trim()),
      userAgent: draft.userAgent.trim(),
      xaiInferenceUserAgent: draft.xaiInferenceUserAgent.trim(),
      xaiInferenceEnabled: draft.xaiInferenceEnabled === true,
      xaiInferenceModel: draft.xaiInferenceModel.trim(),
      xaiInferencePrompt: draft.xaiInferencePrompt.trim(),
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
  display?: 'default' | 'wide' | 'long-text';
};

type ConfigOverviewSettings = Pick<
  CodexInspectionConfigurableSettings,
  | 'targetTypes'
  | 'targetType'
  | 'workers'
  | 'timeout'
  | 'usedPercentThreshold'
  | 'sampleSize'
  | 'xaiInferenceEnabled'
  | 'xaiInferenceModel'
  | 'xaiInferencePrompt'
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
  const targetTypes = normalizeCodexInspectionTargetTypes(
    settings.targetTypes,
    settings.targetType
  );
  const targetLabel =
    targetTypes.length > 1
      ? t('monitoring.codex_inspection_target_codex_xai')
      : targetTypes[0] === 'xai'
        ? t('monitoring.codex_inspection_target_xai')
        : t('monitoring.codex_inspection_target_codex');
  const providerItems: ConfigOverviewItem[] = [
    {
      key: 'target',
      label: t('monitoring.codex_inspection_target_type'),
      value: targetLabel,
      field: 'targetTypes',
    },
  ];
  if (targetTypes.includes('xai')) {
    providerItems.push({
      key: 'xai-inference',
      label: t('monitoring.codex_inspection_settings_xai_inference_enabled_label'),
      value: settings.xaiInferenceEnabled ? t('common.enabled') : t('common.disabled'),
      tone: settings.xaiInferenceEnabled ? 'warn' : 'idle',
      field: 'xaiInferenceEnabled',
    });
  }

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
      ...providerItems,
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
    ...providerItems,
  ];
};
