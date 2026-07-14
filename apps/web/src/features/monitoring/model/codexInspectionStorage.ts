import type {
  CodexInspectionLastRunState,
  CodexInspectionQuotaWindow,
  CodexInspectionResultItem,
  CodexInspectionRunResult,
  CodexInspectionSettings,
  CodexInspectionStoredActionFilter,
  CodexInspectionStoredLogEntry,
  CodexInspectionSummary,
} from '@/features/monitoring/codexInspection';
import { normalizeNumberValue } from '@/utils/quota';
import {
  DEFAULT_CODEX_INSPECTION_SETTINGS,
  clampPositiveInteger,
  isRecord,
  normalizeConfigurableSettings,
  normalizeInspectionAction,
  normalizeLogLevel,
  normalizeStoredActionFilter,
  readBoolean,
  readNonNegativeInteger,
  readNullableNumber,
  readNullableString,
  readString,
} from './codexInspectionSettings';

export const CODEX_INSPECTION_LAST_RUN_STORAGE_KEY = 'cli-proxy-codex-inspection-last-run-v1';

const CODEX_INSPECTION_LAST_RUN_STORAGE_VERSION = 1;

export const sortCodexInspectionResults = (items: CodexInspectionResultItem[]) =>
  [...items].sort(
    (left, right) =>
      left.fileName.localeCompare(right.fileName) ||
      left.displayAccount.localeCompare(right.displayAccount) ||
      left.key.localeCompare(right.key)
  );

const sanitizeInspectionSettingsForStorage = (
  settings: CodexInspectionSettings
): CodexInspectionSettings => ({
  baseUrl: '',
  token: '',
  targetType: readString(settings.targetType) || DEFAULT_CODEX_INSPECTION_SETTINGS.targetType,
  workers: clampPositiveInteger(settings.workers, DEFAULT_CODEX_INSPECTION_SETTINGS.workers),
  deleteWorkers: clampPositiveInteger(
    settings.deleteWorkers,
    DEFAULT_CODEX_INSPECTION_SETTINGS.deleteWorkers
  ),
  timeout: clampPositiveInteger(settings.timeout, DEFAULT_CODEX_INSPECTION_SETTINGS.timeout),
  retries: Math.max(0, Math.floor(normalizeNumberValue(settings.retries) ?? 0)),
  userAgent: readString(settings.userAgent) || DEFAULT_CODEX_INSPECTION_SETTINGS.userAgent,
  usedPercentThreshold:
    normalizeNumberValue(settings.usedPercentThreshold) ??
    DEFAULT_CODEX_INSPECTION_SETTINGS.usedPercentThreshold,
  sampleSize: Math.max(0, Math.floor(normalizeNumberValue(settings.sampleSize) ?? 0)),
});

const normalizeStoredSettings = (value: unknown): CodexInspectionSettings => {
  const input = isRecord(value) ? value : {};
  const configurable = normalizeConfigurableSettings({
    targetType: input.targetType,
    workers: input.workers,
    deleteWorkers: input.deleteWorkers,
    timeout: input.timeout,
    retries: input.retries,
    userAgent: input.userAgent,
    usedPercentThreshold: input.usedPercentThreshold,
    sampleSize: input.sampleSize,
  });

  return {
    baseUrl: '',
    token: '',
    targetType: configurable.targetType,
    workers: configurable.workers,
    deleteWorkers: configurable.deleteWorkers,
    timeout: configurable.timeout,
    retries: configurable.retries,
    userAgent: configurable.userAgent,
    usedPercentThreshold: configurable.usedPercentThreshold,
    sampleSize: configurable.sampleSize,
  };
};

type StoredCodexInspectionResultItem = Omit<CodexInspectionResultItem, 'raw'>;

const normalizeQuotaWindowLabelParams = (
  value: unknown
): Record<string, string | number> | undefined => {
  if (!isRecord(value)) return undefined;
  const params: Record<string, string | number> = {};
  Object.entries(value).forEach(([key, raw]) => {
    if (typeof raw === 'number' && Number.isFinite(raw)) {
      params[key] = raw;
      return;
    }
    const text = readString(raw);
    if (text) {
      params[key] = text;
    }
  });
  return Object.keys(params).length > 0 ? params : undefined;
};

const serializeQuotaWindow = (window: CodexInspectionQuotaWindow): CodexInspectionQuotaWindow => ({
  id: readString(window.id),
  labelKey: readString(window.labelKey),
  labelParams: normalizeQuotaWindowLabelParams(window.labelParams),
  usedPercent: readNullableNumber(window.usedPercent),
  resetLabel: readString(window.resetLabel),
  limitWindowSeconds: readNullableNumber(window.limitWindowSeconds),
});

const hydrateQuotaWindow = (value: unknown): CodexInspectionQuotaWindow | null => {
  if (!isRecord(value)) return null;
  const id = readString(value.id);
  const labelKey = readString(value.labelKey);
  if (!id || !labelKey) return null;

  return {
    id,
    labelKey,
    labelParams: normalizeQuotaWindowLabelParams(value.labelParams),
    usedPercent: readNullableNumber(value.usedPercent),
    resetLabel: readString(value.resetLabel),
    limitWindowSeconds: readNullableNumber(value.limitWindowSeconds),
  };
};

const serializeResultItemForStorage = (
  item: CodexInspectionResultItem
): StoredCodexInspectionResultItem => ({
  key: item.key,
  fileName: item.fileName,
  displayAccount: item.displayAccount,
  authIndex: item.authIndex,
  accountId: null,
  provider: item.provider,
  disabled: item.disabled,
  autoRecoverOwned: item.autoRecoverOwned,
  status: item.status,
  state: item.state,
  action: item.action,
  actionReason: item.actionReason,
  statusCode: item.statusCode,
  usedPercent: item.usedPercent,
  isQuota: item.isQuota,
  autoRecoverEligible: item.autoRecoverEligible,
  error: item.error,
  planType: readNullableString(item.planType),
  quotaWindows: (item.quotaWindows ?? []).map(serializeQuotaWindow),
  errorKind: readString(item.errorKind),
  errorDetail: readString(item.errorDetail),
});

const hydrateStoredResultItem = (
  value: unknown,
  settings: CodexInspectionSettings
): CodexInspectionResultItem | null => {
  if (!isRecord(value)) return null;
  const fileName = readString(value.fileName);
  if (!fileName) return null;

  const authIndex = readNullableString(value.authIndex);
  const provider = readString(value.provider) || settings.targetType;
  const disabled = readBoolean(value.disabled, false);
  const key = readString(value.key) || `${fileName}::${authIndex || '-'}`;

  return {
    key,
    fileName,
    displayAccount: readString(value.displayAccount) || fileName,
    authIndex,
    accountId: readNullableString(value.accountId),
    provider,
    disabled,
    autoRecoverOwned: readBoolean(value.autoRecoverOwned, false),
    status: readString(value.status),
    state: readString(value.state),
    raw: {
      name: fileName,
      type: provider,
      authIndex,
      disabled,
    },
    action: normalizeInspectionAction(value.action),
    actionReason: readString(value.actionReason),
    statusCode: readNullableNumber(value.statusCode),
    usedPercent: readNullableNumber(value.usedPercent),
    isQuota: readBoolean(value.isQuota, false),
    autoRecoverEligible: readBoolean(value.autoRecoverEligible, false),
    error: readString(value.error),
    planType: readNullableString(value.planType),
    quotaWindows: Array.isArray(value.quotaWindows)
      ? value.quotaWindows
          .map(hydrateQuotaWindow)
          .filter((item): item is CodexInspectionQuotaWindow => item !== null)
      : [],
    errorKind: readString(value.errorKind),
    errorDetail: readString(value.errorDetail),
  };
};

const buildSummaryFromStoredResult = (
  storedSummary: unknown,
  results: CodexInspectionResultItem[],
  settings: CodexInspectionSettings
): CodexInspectionSummary => {
  const summary = isRecord(storedSummary) ? storedSummary : {};
  const deleteCount = results.filter((item) => item.action === 'delete').length;
  const disableCount = results.filter((item) => item.action === 'disable').length;
  const enableCount = results.filter((item) => item.action === 'enable').length;
  const reauthCount = results.filter((item) => item.action === 'reauth').length;
  const keepCount = results.length - deleteCount - disableCount - enableCount - reauthCount;
  const plannedActionPreview = results
    .filter((item) => item.action !== 'keep')
    .slice(0, 10)
    .map((item) => `${item.displayAccount} -> ${item.action}`);

  return {
    totalFiles: readNonNegativeInteger(summary.totalFiles, results.length),
    probeSetCount: readNonNegativeInteger(summary.probeSetCount, results.length),
    sampledCount: readNonNegativeInteger(summary.sampledCount, results.length),
    disabledCount: results.filter((item) => item.disabled).length,
    enabledCount: results.filter((item) => !item.disabled).length,
    deleteCount,
    disableCount,
    enableCount,
    reauthCount,
    keepCount,
    usedPercentThreshold:
      readNullableNumber(summary.usedPercentThreshold) ?? settings.usedPercentThreshold,
    sampled: readBoolean(summary.sampled, false),
    plannedActionPreview,
  };
};

const hydrateStoredLogEntry = (value: unknown): CodexInspectionStoredLogEntry | null => {
  if (!isRecord(value)) return null;
  const message = readString(value.message);
  if (!message) return null;
  const timestamp = readNullableNumber(value.timestamp) ?? Date.now();
  const id = readString(value.id) || `${timestamp}-${message.slice(0, 12)}`;

  return {
    id,
    level: normalizeLogLevel(value.level),
    message,
    timestamp,
  };
};

export const serializeCodexInspectionLastRun = ({
  result,
  logs,
  logsCollapsed = true,
  actionFilter = 'all',
  connectionFingerprint = null,
}: {
  result: CodexInspectionRunResult;
  logs?: CodexInspectionStoredLogEntry[];
  logsCollapsed?: boolean;
  actionFilter?: CodexInspectionStoredActionFilter;
  connectionFingerprint?: string | null;
}) => ({
  version: CODEX_INSPECTION_LAST_RUN_STORAGE_VERSION,
  savedAt: Date.now(),
  logsCollapsed,
  actionFilter,
  connectionFingerprint: readNullableString(connectionFingerprint),
  result: {
    settings: sanitizeInspectionSettingsForStorage(result.settings),
    results: result.results.map(serializeResultItemForStorage),
    summary: result.summary,
    startedAt: result.startedAt,
    finishedAt: result.finishedAt,
  },
  logs: (logs ?? []).slice(-500),
});

export const hydrateCodexInspectionLastRun = (
  value: unknown,
  options: { expectedConnectionFingerprint?: string | null } = {}
): CodexInspectionLastRunState | null => {
  if (!isRecord(value)) return null;
  if (value.version !== CODEX_INSPECTION_LAST_RUN_STORAGE_VERSION) return null;
  if (!isRecord(value.result)) return null;

  const connectionFingerprint = readNullableString(value.connectionFingerprint);
  const expectedConnectionFingerprint = readNullableString(options.expectedConnectionFingerprint);
  if (expectedConnectionFingerprint && connectionFingerprint !== expectedConnectionFingerprint) {
    return null;
  }

  const settings = normalizeStoredSettings(value.result.settings);
  const resultItemsRaw = Array.isArray(value.result.results) ? value.result.results : [];
  const results = sortCodexInspectionResults(
    resultItemsRaw
      .map((item) => hydrateStoredResultItem(item, settings))
      .filter((item): item is CodexInspectionResultItem => item !== null)
  );

  const startedAt = readNullableNumber(value.result.startedAt) ?? Date.now();
  const finishedAt = readNullableNumber(value.result.finishedAt) ?? startedAt;
  const logsRaw = Array.isArray(value.logs) ? value.logs : [];
  const logs = logsRaw
    .map(hydrateStoredLogEntry)
    .filter((item): item is CodexInspectionStoredLogEntry => item !== null)
    .slice(-500);

  return {
    result: {
      settings,
      files: [],
      results,
      summary: buildSummaryFromStoredResult(value.result.summary, results, settings),
      startedAt,
      finishedAt,
    },
    logs,
    logsCollapsed: readBoolean(value.logsCollapsed, true),
    actionFilter: normalizeStoredActionFilter(value.actionFilter),
    connectionFingerprint,
    savedAt: readNullableNumber(value.savedAt) ?? finishedAt,
  };
};

export const loadCodexInspectionLastRun = (
  expectedConnectionFingerprint?: string | null
): CodexInspectionLastRunState | null => {
  try {
    if (typeof localStorage === 'undefined') return null;
    const raw = localStorage.getItem(CODEX_INSPECTION_LAST_RUN_STORAGE_KEY);
    if (!raw) return null;
    return hydrateCodexInspectionLastRun(JSON.parse(raw), { expectedConnectionFingerprint });
  } catch {
    return null;
  }
};

export const saveCodexInspectionLastRun = (
  input: Parameters<typeof serializeCodexInspectionLastRun>[0]
) => {
  const payload = serializeCodexInspectionLastRun(input);
  try {
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem(CODEX_INSPECTION_LAST_RUN_STORAGE_KEY, JSON.stringify(payload));
    }
  } catch {
    console.warn('保存 Codex 巡检记录失败');
  }
  return hydrateCodexInspectionLastRun(payload);
};

export const clearCodexInspectionLastRun = () => {
  try {
    if (typeof localStorage !== 'undefined') {
      localStorage.removeItem(CODEX_INSPECTION_LAST_RUN_STORAGE_KEY);
    }
  } catch {
    console.warn('清除 Codex 巡检记录失败');
  }
};
