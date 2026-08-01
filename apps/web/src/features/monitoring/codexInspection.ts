import { authFilesApi } from '@/services/api/authFiles';
import type { TFunction } from 'i18next';
import { getApiCallErrorMessage } from '@/services/api/apiCall';
import type { AuthFileItem, Config } from '@/types';
import {
  CODEX_INSPECTION_AUTO_ACTION_MODES,
  CODEX_INSPECTION_SETTINGS_STORAGE_KEY,
  DEFAULT_CODEX_INSPECTION_SETTINGS,
  clearCodexInspectionConfigurableSettings,
  loadCodexInspectionConfigurableSettings,
  normalizeAutoActionMode,
  normalizeConfigurableSettings,
  readConfigurableSettingsFromConfig,
  readString,
  saveCodexInspectionConfigurableSettings,
} from '@/features/monitoring/model/codexInspectionSettings';
import {
  CODEX_INSPECTION_LAST_RUN_STORAGE_KEY,
  clearCodexInspectionLastRun,
  hydrateCodexInspectionLastRun,
  loadCodexInspectionLastRun,
  saveCodexInspectionLastRun,
  serializeCodexInspectionLastRun,
  sortCodexInspectionResults as sortResults,
} from '@/features/monitoring/model/codexInspectionStorage';
import {
  getCodexInspectionOwnedDisableIdentityKeys,
  getCodexInspectionOwnershipIdentityKey,
  hasCodexInspectionStableIdentity,
} from '@/features/monitoring/model/codexInspectionOwnership';
import {
  inspectSingleAccount,
  toInspectionAccount,
} from '@/features/monitoring/model/codexInspectionProbe';
import { inspectSingleXaiAccount } from '@/features/monitoring/model/xaiInspectionProbe';
import {
  buildProgressSummary,
  buildSummary,
  createProgressSnapshot,
} from '@/features/monitoring/model/codexInspectionProgress';

export {
  CODEX_INSPECTION_AUTO_ACTION_MODES,
  CODEX_INSPECTION_SETTINGS_STORAGE_KEY,
  DEFAULT_CODEX_INSPECTION_SETTINGS,
  clearCodexInspectionConfigurableSettings,
  loadCodexInspectionConfigurableSettings,
  saveCodexInspectionConfigurableSettings,
};

export {
  CODEX_INSPECTION_LAST_RUN_STORAGE_KEY,
  clearCodexInspectionLastRun,
  hydrateCodexInspectionLastRun,
  loadCodexInspectionLastRun,
  saveCodexInspectionLastRun,
  serializeCodexInspectionLastRun,
};

export { executeCodexInspectionActions } from '@/features/monitoring/model/codexInspectionExecution';

export type CodexInspectionLogLevel = 'info' | 'success' | 'warning' | 'error';
export type CodexInspectionLogDetail = Record<string, unknown>;
export type CodexInspectionLogHandler = (
  level: CodexInspectionLogLevel,
  message: string,
  detail?: CodexInspectionLogDetail
) => void;
export type CodexInspectionAction = 'keep' | 'delete' | 'disable' | 'enable' | 'reauth';
export type CodexInspectionExecutionAction = Extract<
  CodexInspectionAction,
  'delete' | 'disable' | 'enable'
>;
export type CodexInspectionExecutionStatus = 'success' | 'failed' | 'skipped' | 'needs_review';
export type CodexInspectionProgressStatus = 'idle' | 'running' | 'paused' | 'stopped' | 'completed';
export type CodexInspectionAutoActionMode = 'none' | 'enable' | 'disable' | 'delete';
export type CodexInspectionStoredActionFilter =
  | 'all'
  | 'delete'
  | 'disable'
  | 'enable'
  | 'reauth'
  | 'keep';

const identityT = ((key: string) => key) as TFunction;

const formatInspectionTargetTypes = (targetTypes: string[], t: TFunction) => {
  const providers = new Set(targetTypes.map((item) => item.trim().toLowerCase()));
  if (providers.has('codex') && providers.has('xai')) {
    return t('monitoring.codex_inspection_target_codex_xai');
  }
  if (providers.has('xai')) return t('monitoring.codex_inspection_target_xai');
  return t('monitoring.codex_inspection_target_codex');
};

export interface CodexInspectionSettings {
  baseUrl: string;
  token: string;
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
}

export interface CodexInspectionConfigurableSettings {
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
  autoActionMode: CodexInspectionAutoActionMode;
  autoRecoverEnabled: boolean;
}

export interface CodexInspectionAccount {
  key: string;
  runtimeId?: string | null;
  fileName: string;
  displayAccount: string;
  accountSnapshot?: string | null;
  authIndex: string | null;
  accountId: string | null;
  provider: string;
  disabled: boolean;
  autoRecoverOwned: boolean;
  status: string;
  state: string;
  raw: AuthFileItem;
}

export interface CodexInspectionQuotaWindow {
  id: string;
  labelKey: string;
  labelParams?: Record<string, string | number>;
  usedPercent: number | null;
  resetLabel: string;
  limitWindowSeconds: number | null;
}

export interface CodexInspectionResultItem extends CodexInspectionAccount {
  action: CodexInspectionAction;
  actionReason: string;
  statusCode: number | null;
  usedPercent: number | null;
  isQuota: boolean;
  autoRecoverEligible: boolean;
  error: string;
  planType?: string | null;
  quotaWindows?: CodexInspectionQuotaWindow[];
  errorKind?: string;
  errorDetail?: string;
  actionHandled?: boolean;
  observedHeaderEvidence?: string[];
  observedHeaderAtMs?: number | null;
}

export interface CodexInspectionSummary {
  totalFiles: number;
  probeSetCount: number;
  sampledCount: number;
  disabledCount: number;
  enabledCount: number;
  deleteCount: number;
  disableCount: number;
  enableCount: number;
  reauthCount: number;
  keepCount: number;
  usedPercentThreshold: number;
  sampled: boolean;
  plannedActionPreview: string[];
}

export interface CodexInspectionProgressSummary {
  totalFiles: number;
  probeSetCount: number;
  sampledCount: number;
  deleteCount: number;
  disableCount: number;
  enableCount: number;
  reauthCount: number;
  keepCount: number;
}

export interface CodexInspectionRunResult {
  settings: CodexInspectionSettings;
  files: AuthFileItem[];
  results: CodexInspectionResultItem[];
  summary: CodexInspectionSummary;
  startedAt: number;
  finishedAt: number;
}

export interface CodexInspectionProgressSnapshot {
  total: number;
  completed: number;
  inFlight: number;
  pending: number;
  percent: number;
  status: CodexInspectionProgressStatus;
  summary: CodexInspectionProgressSummary;
  startedAt: number;
  updatedAt: number;
}

export interface CodexInspectionExecutionOutcome {
  accountKey: string;
  coveredByAccountKey?: string;
  action: CodexInspectionExecutionAction;
  fileName: string;
  displayAccount: string;
  status: CodexInspectionExecutionStatus;
  success: boolean;
  error: string;
}

export interface CodexInspectionExecutionResult {
  outcomes: CodexInspectionExecutionOutcome[];
  refreshedFiles: AuthFileItem[];
  refreshError: string;
}

export interface CodexInspectionStoredLogEntry {
  id: string;
  level: CodexInspectionLogLevel;
  message: string;
  timestamp: number;
  detail?: CodexInspectionLogDetail;
}

export interface CodexInspectionLastRunState {
  result: CodexInspectionRunResult;
  logs: CodexInspectionStoredLogEntry[];
  logsCollapsed: boolean;
  actionFilter: CodexInspectionStoredActionFilter;
  connectionFingerprint: string | null;
  savedAt: number;
}

type ProgressHandler = (progress: CodexInspectionProgressSnapshot) => void;
type ResultsChangeHandler = (result: CodexInspectionRunResult) => void;

type InspectCodexAccountsOptions = {
  config: Config | null;
  apiBase: string;
  managementKey: string;
  settings?: Partial<CodexInspectionConfigurableSettings> | null;
  onLog?: CodexInspectionLogHandler;
  onProgress?: ProgressHandler;
  onResultsChange?: ResultsChangeHandler;
  t?: TFunction;
};

type CreateCodexInspectionSessionOptions = InspectCodexAccountsOptions & {
  deferCompletionLog?: boolean;
};

type CodexInspectionSessionPromiseState = {
  promise: Promise<CodexInspectionRunResult>;
  resolve: (value: CodexInspectionRunResult) => void;
  reject: (reason?: unknown) => void;
};

export interface CodexInspectionSession {
  id: string;
  start: () => Promise<CodexInspectionRunResult>;
  resume: () => void;
  pause: () => void;
  stop: () => void;
  getProgress: () => CodexInspectionProgressSnapshot;
}

export class CodexInspectionStoppedError extends Error {
  constructor(message: string = '巡检已停止') {
    super(message);
    this.name = 'CodexInspectionStoppedError';
  }
}

export const createCodexInspectionConnectionFingerprint = (
  apiBase: string,
  managementKey: string
) => {
  const normalizedApiBase = readString(apiBase).replace(/\/+$/, '');
  const normalizedManagementKey = readString(managementKey);
  if (!normalizedApiBase || !normalizedManagementKey) return null;

  const input = `${normalizedApiBase}\u0000${normalizedManagementKey}`;
  let hashA = 0x811c9dc5;
  let hashB = 0x9e3779b9;

  for (let index = 0; index < input.length; index += 1) {
    const code = input.charCodeAt(index);
    hashA = Math.imul(hashA ^ code, 0x01000193);
    hashB = Math.imul(hashB ^ code, 0x85ebca6b);
  }

  return `v1:${(hashA >>> 0).toString(36)}${(hashB >>> 0).toString(36)}`;
};

const createDeferred = (): CodexInspectionSessionPromiseState => {
  let resolve: ((value: CodexInspectionRunResult) => void) | null = null;
  let reject: ((reason?: unknown) => void) | null = null;

  const promise = new Promise<CodexInspectionRunResult>((nextResolve, nextReject) => {
    resolve = nextResolve;
    reject = nextReject;
  });

  return {
    promise,
    resolve: (value) => resolve?.(value),
    reject: (reason) => reject?.(reason),
  };
};

const pickSample = <T>(items: T[], sampleSize: number): T[] => {
  if (sampleSize <= 0 || sampleSize >= items.length) return [...items];

  const shuffled = [...items];
  for (let index = shuffled.length - 1; index > 0; index -= 1) {
    const swapIndex = Math.floor(Math.random() * (index + 1));
    [shuffled[index], shuffled[swapIndex]] = [shuffled[swapIndex], shuffled[index]];
  }
  return shuffled.slice(0, sampleSize);
};

const pickSamplePerProvider = (
  items: CodexInspectionAccount[],
  sampleSize: number
): CodexInspectionAccount[] => {
  if (sampleSize <= 0) return [...items];

  const groups = new Map<string, CodexInspectionAccount[]>();
  items.forEach((item) => {
    const group = groups.get(item.provider) ?? [];
    group.push(item);
    groups.set(item.provider, group);
  });

  return Array.from(groups.values()).flatMap((group) => pickSample(group, sampleSize));
};

export const resolveCodexInspectionSettings = (
  config: Config | null,
  apiBase: string,
  managementKey: string,
  settingsOverride?: Partial<CodexInspectionConfigurableSettings> | null
): CodexInspectionSettings => {
  const clean = config?.clean ?? null;
  const configurable = normalizeConfigurableSettings({
    ...readConfigurableSettingsFromConfig(config),
    ...(settingsOverride ?? {}),
  });

  return {
    baseUrl: readString(apiBase) || readString(clean?.baseUrl),
    token: readString(managementKey) || readString(clean?.token),
    targetTypes: configurable.targetTypes,
    targetType: configurable.targetType,
    workers: configurable.workers,
    deleteWorkers: configurable.deleteWorkers,
    timeout: configurable.timeout,
    retries: configurable.retries,
    userAgent: configurable.userAgent,
    xaiInferenceUserAgent: configurable.xaiInferenceUserAgent,
    xaiInferenceEnabled: configurable.xaiInferenceEnabled,
    xaiInferenceModel: configurable.xaiInferenceModel,
    xaiInferencePrompt: configurable.xaiInferencePrompt,
    usedPercentThreshold: configurable.usedPercentThreshold,
    sampleSize: configurable.sampleSize,
  };
};

export const createCodexInspectionSession = ({
  config,
  apiBase,
  managementKey,
  settings,
  onLog,
  onProgress,
  onResultsChange,
  t,
  deferCompletionLog = false,
}: CreateCodexInspectionSessionOptions): CodexInspectionSession => {
  const resolvedSettings = resolveCodexInspectionSettings(config, apiBase, managementKey, settings);
  const translate = t ?? identityT;
  const sessionId = `codex-inspection-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;

  let status: CodexInspectionProgressStatus = 'idle';
  let startedAt = 0;
  let finishedAt = 0;
  let files: AuthFileItem[] = [];
  let probeSet: CodexInspectionAccount[] = [];
  let sampledAccounts: CodexInspectionAccount[] = [];
  let cursor = 0;
  let inFlight = 0;
  let finalResult: CodexInspectionRunResult | null = null;
  let deferred: CodexInspectionSessionPromiseState | null = null;
  const resultMap = new Map<string, CodexInspectionResultItem>();

  const emitProgress = () => {
    const baseTime = startedAt || Date.now();
    const summary = buildProgressSummary(
      files,
      probeSet,
      sampledAccounts,
      Array.from(resultMap.values())
    );
    onProgress?.(
      createProgressSnapshot(
        sampledAccounts.length,
        resultMap.size,
        inFlight,
        status,
        baseTime,
        Date.now(),
        summary
      )
    );
  };

  const buildRunResult = (finishedTime: number): CodexInspectionRunResult => {
    const results = sortResults(Array.from(resultMap.values()));
    const summary = buildSummary(files, probeSet, results, resolvedSettings);
    return {
      settings: resolvedSettings,
      files,
      results,
      summary,
      startedAt,
      finishedAt: finishedTime,
    };
  };

  const emitResultsChange = (latestResult: CodexInspectionResultItem) => {
    if (latestResult.action === 'keep') return;
    onResultsChange?.(buildRunResult(0));
  };

  const settleStopped = () => {
    if (!deferred) return;
    const currentDeferred = deferred;
    deferred = null;
    currentDeferred.reject(new CodexInspectionStoppedError());
  };

  const settleCompleted = () => {
    if (!deferred) return;
    const currentDeferred = deferred;
    deferred = null;
    finishedAt = Date.now();
    finalResult = buildRunResult(finishedAt);
    status = 'completed';
    emitProgress();
    if (!deferCompletionLog) {
      onLog?.(
        'success',
        translate('monitoring.codex_inspection_log_completed', {
          delete: finalResult.summary.deleteCount,
          disable: finalResult.summary.disableCount,
          enable: finalResult.summary.enableCount,
          reauth: finalResult.summary.reauthCount,
          keep: finalResult.summary.keepCount,
        }),
        {
          deleteCount: finalResult.summary.deleteCount,
          disableCount: finalResult.summary.disableCount,
          enableCount: finalResult.summary.enableCount,
          reauthCount: finalResult.summary.reauthCount,
          keepCount: finalResult.summary.keepCount,
          actionSuccessCount: 0,
          actionFailedCount: 0,
          actionSkippedCount: 0,
          actionNeedsReviewCount: 0,
          actionErrors: [],
          resultWriteFailedCount: 0,
        }
      );
    }
    currentDeferred.resolve(finalResult);
  };

  const maybeSettle = () => {
    if (status === 'stopped') {
      if (inFlight === 0) {
        settleStopped();
      }
      return;
    }

    if (cursor >= sampledAccounts.length && inFlight === 0) {
      settleCompleted();
    }
  };

  const pump = () => {
    if (status !== 'running') {
      maybeSettle();
      return;
    }

    while (
      status === 'running' &&
      inFlight < resolvedSettings.workers &&
      cursor < sampledAccounts.length
    ) {
      const account = sampledAccounts[cursor];
      cursor += 1;
      inFlight += 1;
      emitProgress();

      void (
        account.provider === 'xai'
          ? inspectSingleXaiAccount(account, resolvedSettings, onLog, translate)
          : inspectSingleAccount(account, resolvedSettings, onLog, translate)
      )
        .then((inspectionResult) => {
          resultMap.set(inspectionResult.key, inspectionResult);
          emitResultsChange(inspectionResult);
        })
        .catch((error) => {
          const message = error instanceof Error ? error.message : String(error || '探测失败');
          onLog?.(
            'warning',
            translate('monitoring.codex_inspection_log_unexpected_error', {
              account: account.displayAccount,
              message,
            }),
            {
              provider: account.provider,
              fileName: account.fileName,
              displayAccount: account.displayAccount,
              action: 'keep',
              error: message,
            }
          );
          const fallbackResult: CodexInspectionResultItem = {
            ...account,
            action: 'keep',
            actionReason: '探测异常，保留账号',
            statusCode: null,
            usedPercent: null,
            isQuota: false,
            autoRecoverEligible: false,
            error: message,
          };
          resultMap.set(account.key, fallbackResult);
          emitResultsChange(fallbackResult);
        })
        .finally(() => {
          inFlight = Math.max(0, inFlight - 1);
          emitProgress();
          pump();
        });
    }

    maybeSettle();
  };

  const ensureStarted = () => {
    if (startedAt <= 0) {
      startedAt = Date.now();
    }
    if (!deferred) {
      deferred = createDeferred();
    }
    return deferred;
  };

  const initialize = async () => {
    onLog?.(
      'info',
      translate('monitoring.codex_inspection_log_loading', {
        target: formatInspectionTargetTypes(resolvedSettings.targetTypes, translate),
      }),
      {
        triggerType: 'manual',
        triggerKey: 'manual',
        targetTypes: [...resolvedSettings.targetTypes],
      }
    );

    const authFilesResponse = await authFilesApi.list();
    files = Array.isArray(authFilesResponse.files) ? authFilesResponse.files : [];
    const accounts = files.map(toInspectionAccount);
    const connectionFingerprint = createCodexInspectionConnectionFingerprint(
      resolvedSettings.baseUrl,
      resolvedSettings.token
    );
    const ownedDisableIdentityKeys = getCodexInspectionOwnedDisableIdentityKeys(
      connectionFingerprint ?? '',
      files
    );
    probeSet = accounts
      .filter((item) => resolvedSettings.targetTypes.includes(item.provider))
      .map((item) => ({
        ...item,
        autoRecoverOwned: ownedDisableIdentityKeys.has(
          getCodexInspectionOwnershipIdentityKey({
            fileName: item.fileName,
            provider: item.provider,
            authIndex: item.authIndex,
            accountId: item.accountId,
            accountSnapshot: item.accountSnapshot,
          })
        ),
      }));
    sampledAccounts =
      resolvedSettings.sampleSize > 0
        ? pickSamplePerProvider(probeSet, resolvedSettings.sampleSize)
        : probeSet;

    onLog?.(
      'info',
      translate('monitoring.codex_inspection_log_set_ready', {
        total: probeSet.length,
        sampled: sampledAccounts.length,
      }),
      {
        totalFiles: files.length,
        probeSetCount: probeSet.length,
        sampledCount: sampledAccounts.length,
        targetTypes: [...resolvedSettings.targetTypes],
      }
    );
    emitProgress();
  };

  const start = () => {
    if (finalResult) {
      return Promise.resolve(finalResult);
    }

    if (status === 'completed') {
      return Promise.reject(new Error('巡检已结束，请重新开始'));
    }

    if (status === 'running') {
      return ensureStarted().promise;
    }

    if (status === 'paused') {
      status = 'running';
      onLog?.('info', translate('monitoring.codex_inspection_log_resumed'));
      emitProgress();
      pump();
      return ensureStarted().promise;
    }

    if (status === 'stopped') {
      return Promise.reject(new CodexInspectionStoppedError('巡检已停止，请重新开始'));
    }

    const currentDeferred = ensureStarted();
    status = 'running';
    emitProgress();

    void initialize()
      .then(() => {
        pump();
      })
      .catch((error) => {
        const message =
          error instanceof Error
            ? error.message
            : String(error || translate('common.unknown_error'));
        onLog?.(
          'error',
          translate('monitoring.codex_inspection_log_auth_files_failed', { message }),
          { error: message }
        );
        status = 'completed';
        emitProgress();
        const activeDeferred = deferred;
        deferred = null;
        activeDeferred?.reject(error);
      });

    return currentDeferred.promise;
  };

  const resume = () => {
    if (status !== 'paused') return;
    status = 'running';
    onLog?.('info', translate('monitoring.codex_inspection_log_resumed'));
    emitProgress();
    pump();
  };

  const pause = () => {
    if (status !== 'running') return;
    status = 'paused';
    onLog?.(
      'info',
      inFlight > 0
        ? translate('monitoring.codex_inspection_log_paused_pending', { count: inFlight })
        : translate('monitoring.codex_inspection_log_paused')
    );
    emitProgress();
    maybeSettle();
  };

  const stop = () => {
    if (status === 'completed' || status === 'stopped' || status === 'idle') return;
    status = 'stopped';
    onLog?.(
      'warning',
      inFlight > 0
        ? translate('monitoring.codex_inspection_log_stopped_pending', { count: inFlight })
        : translate('monitoring.codex_inspection_log_stopped')
    );
    emitProgress();
    maybeSettle();
  };

  return {
    id: sessionId,
    start,
    resume,
    pause,
    stop,
    getProgress: () =>
      createProgressSnapshot(
        sampledAccounts.length,
        resultMap.size,
        inFlight,
        status,
        startedAt || Date.now(),
        Date.now(),
        buildProgressSummary(files, probeSet, sampledAccounts, Array.from(resultMap.values()))
      ),
  };
};

export const inspectCodexAccounts = async ({
  config,
  apiBase,
  managementKey,
  settings,
  onLog,
  onProgress,
  onResultsChange,
  t,
}: InspectCodexAccountsOptions): Promise<CodexInspectionRunResult> => {
  const session = createCodexInspectionSession({
    config,
    apiBase,
    managementKey,
    settings,
    onLog,
    onProgress,
    onResultsChange,
    t,
  });

  return session.start();
};

export const buildCodexInspectionError = (message: string) => message;

export const buildExecutionFailureMessage = (outcome: CodexInspectionExecutionOutcome) =>
  `${outcome.displayAccount}：${outcome.error || '执行失败'}`;

export const isSuggestedAction = (item: CodexInspectionResultItem) =>
  !item.actionHandled && item.action !== 'keep';

export const isExecutableAction = (item: CodexInspectionResultItem) =>
  !item.actionHandled &&
  (item.action === 'delete' || item.action === 'disable' || item.action === 'enable');

export const isReauthAction = (item: CodexInspectionResultItem) =>
  !item.actionHandled && item.action === 'reauth';

export const toReauthDeleteExecutionItem = (
  item: CodexInspectionResultItem
): CodexInspectionResultItem => ({
  ...item,
  action: 'delete',
  actionReason: item.actionReason
    ? `${item.actionReason}；用户选择删除需重新登录账号`
    : '用户选择删除需重新登录账号',
});

export interface CodexInspectionAutoActionPlan {
  items: CodexInspectionResultItem[];
  preflightOutcomes: CodexInspectionExecutionOutcome[];
}

const buildAutoActionPreflightOutcome = (
  item: CodexInspectionResultItem,
  status: Extract<CodexInspectionExecutionStatus, 'skipped' | 'needs_review'>,
  error: string,
  coveredByAccountKey?: string
): CodexInspectionExecutionOutcome => ({
  accountKey: item.key,
  ...(coveredByAccountKey ? { coveredByAccountKey } : {}),
  action: item.action as CodexInspectionExecutionAction,
  fileName: item.fileName,
  displayAccount: item.displayAccount,
  status,
  success: true,
  error,
});

export const resolveCodexInspectionAutoActionPlan = (
  mode: CodexInspectionAutoActionMode,
  autoRecoverEnabled: boolean,
  items: CodexInspectionResultItem[]
): CodexInspectionAutoActionPlan => {
  const normalizedMode = normalizeAutoActionMode(mode);
  if (normalizedMode === 'none' && !autoRecoverEnabled) {
    return { items: [], preflightOutcomes: [] };
  }

  const canAutoRecover = (item: CodexInspectionResultItem) =>
    autoRecoverEnabled && item.action === 'enable' && item.autoRecoverEligible;
  const allowsAction = (item: CodexInspectionResultItem) => {
    if (item.action === 'enable') return canAutoRecover(item);
    if (normalizedMode === 'disable' || normalizedMode === 'delete') {
      return item.action === 'delete' || item.action === 'disable';
    }
    return false;
  };

  const preparedItems = items.map((item) =>
    normalizedMode === 'disable' && item.action === 'delete'
      ? {
          ...item,
          action: 'disable' as const,
          actionReason: item.actionReason
            ? `${item.actionReason}；自动禁用策略改为禁用账号`
            : '自动禁用策略改为禁用账号',
        }
      : item
  );
  const itemsByFile = new Map<string, CodexInspectionResultItem[]>();
  preparedItems.forEach((item) => {
    const fileName = item.fileName.trim();
    if (!fileName) return;
    const fileItems = itemsByFile.get(fileName) ?? [];
    fileItems.push(item);
    itemsByFile.set(fileName, fileItems);
  });

  const groups: Array<{ items: CodexInspectionResultItem[]; mixed: boolean }> = [];
  itemsByFile.forEach((allFileItems) => {
    const fileItems = allFileItems.filter(isExecutableAction);
    if (fileItems.length === 0) return;
    if (fileItems.some((item) => item.action === 'delete')) {
      groups.push({
        items: fileItems,
        mixed: allFileItems.some((item) => item.action !== 'delete'),
      });
      return;
    }
    const identityGroups = new Map<string, CodexInspectionResultItem[]>();
    fileItems.forEach((item) => {
      const identityKey = getCodexInspectionOwnershipIdentityKey({
        fileName: item.fileName,
        provider: item.provider,
        authIndex: item.authIndex,
        accountId: item.accountId,
        accountSnapshot: item.accountSnapshot,
      });
      const identityItems = identityGroups.get(identityKey) ?? [];
      identityItems.push(item);
      identityGroups.set(identityKey, identityItems);
    });
    groups.push(
      ...Array.from(identityGroups.values(), (identityItems) => ({
        items: identityItems,
        mixed: new Set(identityItems.map((item) => item.action)).size > 1,
      }))
    );
  });

  const executableItems: CodexInspectionResultItem[] = [];
  const preflightOutcomes: CodexInspectionExecutionOutcome[] = [];
  groups.forEach((group) => {
    const stableItems = group.items.filter((item) =>
      hasCodexInspectionStableIdentity({
        fileName: item.fileName,
        provider: item.provider,
        authIndex: item.authIndex,
        accountId: item.accountId,
        accountSnapshot: item.accountSnapshot,
      })
    );
    group.items
      .filter((item) => allowsAction(item) && !stableItems.includes(item))
      .forEach((item) =>
        preflightOutcomes.push(
          buildAutoActionPreflightOutcome(
            item,
            'needs_review',
            '巡检结果缺少稳定账号标识，已阻止处理，请人工确认'
          )
        )
      );
    if (stableItems.length === 0) return;
    if (group.mixed) {
      stableItems.forEach((item) =>
        preflightOutcomes.push(
          buildAutoActionPreflightOutcome(
            item,
            'needs_review',
            '同一认证文件下存在多个不同建议动作，文件级处理已阻止，请到认证文件管理中手动处理'
          )
        )
      );
      return;
    }

    const eligible = stableItems.filter(allowsAction);
    const canonical = eligible[0];
    if (!canonical) return;
    executableItems.push(canonical);
    eligible
      .slice(1)
      .forEach((item) =>
        preflightOutcomes.push(
          buildAutoActionPreflightOutcome(
            item,
            'skipped',
            '该认证目标已由另一条结果处理',
            canonical.key
          )
        )
      );
  });

  return { items: executableItems, preflightOutcomes };
};

export const resolveCodexInspectionAutoActionItems = (
  mode: CodexInspectionAutoActionMode,
  autoRecoverEnabled: boolean,
  items: CodexInspectionResultItem[]
): CodexInspectionResultItem[] =>
  resolveCodexInspectionAutoActionPlan(mode, autoRecoverEnabled, items).items;

export const isCodexInspectionStoppedError = (
  error: unknown
): error is CodexInspectionStoppedError => error instanceof CodexInspectionStoppedError;

export const applyCodexInspectionExecutionResult = (
  previousResult: CodexInspectionRunResult,
  execution: CodexInspectionExecutionResult
): CodexInspectionRunResult => {
  const successfulOutcomes = new Map(
    execution.outcomes
      .filter((item) => item.success && item.status === 'success')
      .map((item) => [item.accountKey, item] as const)
  );
  const nonExecutionOutcomes = new Map(
    execution.outcomes
      .filter((item) => item.status === 'skipped' || item.status === 'needs_review')
      .map((item) => [item.accountKey, item] as const)
  );
  const refreshedAccounts = new Map(
    execution.refreshedFiles.map((file) => {
      const account = toInspectionAccount(file);
      return [account.key, account] as const;
    })
  );

  const nextResults = sortResults(
    previousResult.results.map((item) => {
      const refreshedAccount = refreshedAccounts.get(item.key);
      const baseItem: CodexInspectionResultItem = refreshedAccount
        ? {
            ...item,
            ...refreshedAccount,
            raw: refreshedAccount.raw,
          }
        : item;
      const outcome = successfulOutcomes.get(item.key);
      const nonExecutionOutcome = nonExecutionOutcomes.get(item.key);

      if (nonExecutionOutcome) {
        const coveringOutcome = nonExecutionOutcome.coveredByAccountKey
          ? execution.outcomes.find(
              (candidate) => candidate.accountKey === nonExecutionOutcome.coveredByAccountKey
            )
          : undefined;
        const coveredActionHandled =
          coveringOutcome?.status === 'success' || coveringOutcome?.status === 'skipped';
        return {
          ...baseItem,
          actionHandled:
            nonExecutionOutcome.status === 'skipped' &&
            (!nonExecutionOutcome.coveredByAccountKey || coveredActionHandled),
          actionReason: nonExecutionOutcome.error || baseItem.actionReason,
        };
      }
      if (!outcome) {
        return baseItem;
      }

      return {
        ...baseItem,
        disabled:
          outcome.action === 'disable'
            ? true
            : outcome.action === 'enable'
              ? false
              : baseItem.disabled,
        action: 'keep',
        actionReason: '无需处理',
        error: '',
      };
    })
  );

  const deleteCount = nextResults.filter((item) => item.action === 'delete').length;
  const disableCount = nextResults.filter((item) => item.action === 'disable').length;
  const enableCount = nextResults.filter((item) => item.action === 'enable').length;
  const reauthCount = nextResults.filter((item) => item.action === 'reauth').length;
  const keepCount = nextResults.length - deleteCount - disableCount - enableCount - reauthCount;
  const plannedActionPreview = nextResults
    .filter((item) => item.action !== 'keep')
    .slice(0, 10)
    .map((item) => `${item.displayAccount} -> ${item.action}`);

  return {
    ...previousResult,
    files: execution.refreshedFiles,
    results: nextResults,
    summary: {
      ...previousResult.summary,
      totalFiles: execution.refreshedFiles.length,
      disabledCount: nextResults.filter((item) => item.disabled).length,
      enabledCount: nextResults.filter((item) => !item.disabled).length,
      deleteCount,
      disableCount,
      enableCount,
      reauthCount,
      keepCount,
      plannedActionPreview,
    },
    finishedAt: Date.now(),
  };
};

export const buildSuggestedActionCountLabel = (summary: CodexInspectionSummary) =>
  summary.deleteCount + summary.disableCount + summary.enableCount + summary.reauthCount;

export const getProbeFailureMessage = (result: CodexInspectionResultItem) =>
  result.error ||
  getApiCallErrorMessage({
    statusCode: result.statusCode || 0,
    hasStatusCode: true,
    header: {},
    bodyText: '',
    body: null,
  });
