import type { AuthFileItem } from '@/types';
import type { CodexInspectionResult } from '@/services/api/usageService';
import type { TFunction } from 'i18next';
import {
  normalizeRecentRequestBuckets,
  sumRecentRequests,
  type RecentRequestBucket,
} from '@/utils/recentRequests';
import {
  authFileMatchesCodexStatusFilter,
  getAuthFileCodexInspectionKey,
  getAuthFileCodexInspectionKeyForFile,
  getAuthFileCodexInspectionKeyForIdentity,
  getAuthFileSelectionKey,
  getAuthFileCredentialStatusCodes,
  isAuthFileInspectionAuthenticationFailure,
  hasActiveCodexInspectionAuthenticationFailure,
  type AuthFileCodexStatusSummary,
} from '@/features/authFiles/model/credentialStatus';
import {
  compareQuotaResetLabels,
  compareQuotaResets,
  normalizeAccountProvider,
  readAuthFileCreatedAtMs,
  readAuthFileCredentialRefreshAtMs,
  readAuthFileUpdatedAtMs,
  resolveAccountQuota,
  type AccountQuotaOverrides,
  type AccountQuotaSortDirection,
  type AccountQuotaStores,
  type AccountQuotaSummary,
} from '@/features/accounts/model/accountQuotaSummary';
import {
  getAccountRequestCredentialEvidence,
  hasAccountQuotaLimitEvidence,
  isAccountCredentialStatusProblemCurrent,
  isAccountInspectionHealthyEvidence,
  isAccountInspectionActionable,
  isAccountObservedDiagnosticProblemCurrent,
  isAccountQuotaRefreshProblemCurrent,
  isAccountRequestCredentialEvidenceCurrent,
  isAccountRequestHealthEvidenceCurrent,
  resolveAccountAuthenticationProblemEvidence,
  resolveAccountRequestHealthEvidence,
  type AccountRequestEvidenceBySelectionKey,
} from './accountHealthEvidence';
import {
  getAccountCredentialEvidenceCutoffs,
  type AccountCredentialEvidenceBoundary,
  type AccountInspectionSummary,
} from '@/features/accounts/model/accountCredentialEvidence';
import { getCredentialScopedQuotaState } from '@/utils/quota/credentialScope';
import {
  getCanonicalPlanFilterLabel,
  getCanonicalPlanType,
  getPlanPresentation,
  resolveAuthFilePlanType,
} from '@/utils/plans';

export {
  compareQuotaResetLabels,
  compareQuotaResets,
  normalizeAccountProvider,
  readAuthFileCreatedAtMs,
  readAuthFileCredentialRefreshAtMs,
  readAuthFileUpdatedAtMs,
  resolveAccountQuota,
};
export type {
  AccountQuotaOverrides,
  AccountQuotaSource,
  AccountQuotaStatus,
  AccountQuotaStores,
  AccountQuotaSummary,
} from '@/features/accounts/model/accountQuotaSummary';

export type AccountQuotaBand = 'all' | 'ge50' | 'between20and50' | 'lt20' | 'spent';
export const ACCOUNT_CODEX_STATUS_FILTERS = [
  'reauth',
  'quota_limited',
  'five_hour_limited',
  'weekly_limited',
  'monthly_limited',
  'disabled_with_reset',
] as const;
export const ACCOUNT_STATUS_FILTERS = [
  'all',
  'available',
  'unconfirmed',
  'disabled',
  'problem',
  'low',
  'exhausted',
  'inspection',
  ...ACCOUNT_CODEX_STATUS_FILTERS,
] as const;
export type AccountCodexStatusFilter = (typeof ACCOUNT_CODEX_STATUS_FILTERS)[number];
export type AccountStatusFilter = (typeof ACCOUNT_STATUS_FILTERS)[number];
export type AccountRowSortKey =
  | 'default'
  | 'name'
  | 'plan'
  | 'note'
  | 'reset'
  | 'priority'
  | 'recent'
  | 'quota'
  | 'created';
export type AccountRowSortDirection = AccountQuotaSortDirection;

export interface AccountRowSort {
  key: AccountRowSortKey;
  direction: AccountRowSortDirection;
}

export type { AccountInspectionSummary } from '@/features/accounts/model/accountCredentialEvidence';

export type AccountInspectionResult = CodexInspectionResult & {
  inspectionSource?: 'local' | 'server';
};

export const getAccountInspectionResultSnapshotKey = (result: AccountInspectionResult): string => {
  const identityKey = getAuthFileCodexInspectionKeyForIdentity(result);
  if (result.inspectionSource === 'local' || result.runId <= 0 || result.id <= 0) {
    return [identityKey, 'local', result.createdAtMs, result.id].join('\u001f');
  }
  return [identityKey, 'server', result.runId, result.id, result.createdAtMs].join('\u001f');
};

export const filterSuppressedAccountInspectionResults = (
  results: AccountInspectionResult[],
  suppressedResultKeys: ReadonlySet<string>
): AccountInspectionResult[] => {
  if (suppressedResultKeys.size === 0) return results;
  const filtered = results.filter(
    (result) => !suppressedResultKeys.has(getAccountInspectionResultSnapshotKey(result))
  );
  return filtered.length === results.length ? results : filtered;
};

export const getHandledAccountInspectionResultKeys = (
  results: AccountInspectionResult[],
  targetIdentityKey: string,
  targetFileName: string,
  files: AuthFileItem[]
): string[] => {
  const normalizedFileName = targetFileName.trim();
  if (!normalizedFileName) return [];

  const fileNameFallbackKey = getAuthFileCodexInspectionKey(normalizedFileName, null);
  const targetHasStableIdentity = targetIdentityKey !== fileNameFallbackKey;
  const hasUniqueFileName = files.filter((file) => file.name === normalizedFileName).length === 1;

  return results
    .filter((result) => {
      if (
        result.fileName.trim() !== normalizedFileName ||
        !isAuthFileInspectionAuthenticationFailure(result)
      ) {
        return false;
      }
      const resultIdentityKey = getAuthFileCodexInspectionKeyForIdentity(result);
      if (targetHasStableIdentity && resultIdentityKey === targetIdentityKey) return true;
      return hasUniqueFileName && resultIdentityKey === fileNameFallbackKey;
    })
    .map(getAccountInspectionResultSnapshotKey);
};

export interface AccountUsageSummary {
  success: number;
  failure: number;
  successRate: number | null;
  recentRequests: RecentRequestBucket[];
}

export interface AccountRow {
  key: string;
  selectionKey: string;
  fileName: string;
  accountLabel: string;
  provider: string;
  planType: string | null;
  /** Canonical plan identity used by filtering/grouping; planType remains raw data. */
  canonicalPlanType?: string | null;
  disabled: boolean;
  runtimeOnly: boolean;
  statusMessage: string;
  authIndex: string;
  projectId: string;
  note?: string;
  priority: number | null;
  createdAtMs: number | null;
  updatedAtMs: number | null;
  authenticationAtMs: number;
  rawCredentialStatusSuperseded: boolean;
  quota: AccountQuotaSummary;
  usage: AccountUsageSummary;
  inspection: AccountInspectionSummary | null;
  raw: AuthFileItem;
}

export interface AccountInspectionTarget {
  fileName: string;
  runtimeId?: string | null;
  provider?: string | null;
  authIndex?: string | null;
  accountId?: string | null;
  accountSnapshot?: string | null;
}

export interface AccountMetrics {
  total: number;
  available: number;
  needsAttention: number;
  quotaRisk: number;
  disabled: number;
  unconfirmed: number;
  needsInspectionAction: number;
}

export interface AccountMetricOperationalContext {
  pendingActionsByRowKey?: ReadonlyMap<string, readonly unknown[]>;
  quotaCooldownsByRowKey?: ReadonlyMap<string, readonly unknown[]>;
  requestEvidenceBySelectionKey?: AccountRequestEvidenceBySelectionKey;
}

export interface AccountRowFilters extends AccountMetricOperationalContext {
  provider: string;
  status: AccountStatusFilter;
  plan: string;
  quotaBand: AccountQuotaBand;
  search: string;
  codexStatusBySelectionKey?: ReadonlyMap<string, AuthFileCodexStatusSummary>;
}

export interface AccountPlanOption {
  value: string;
  label: string;
}

const QUOTA_LOW_THRESHOLD = 20;
const QUOTA_OK_THRESHOLD = 50;
const UNKNOWN_ACCOUNT_PLAN = 'unknown';
const ACCOUNT_CODEX_STATUS_FILTER_SET = new Set<AccountCodexStatusFilter>(
  ACCOUNT_CODEX_STATUS_FILTERS
);

export const isAccountCodexStatusFilter = (
  status: AccountStatusFilter
): status is AccountCodexStatusFilter =>
  ACCOUNT_CODEX_STATUS_FILTER_SET.has(status as AccountCodexStatusFilter);

const readString = (value: unknown): string => {
  if (typeof value === 'string') return value.trim();
  if (typeof value === 'number' && Number.isFinite(value)) return String(value);
  return '';
};

const readNumber = (value: unknown): number | null => {
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  if (typeof value === 'string') {
    const parsed = Number(value.trim());
    return Number.isFinite(parsed) ? parsed : null;
  }
  return null;
};

const getAccountPlanFilterValue = (provider: string, planType: string | null): string =>
  getCanonicalPlanType(provider, planType) || UNKNOWN_ACCOUNT_PLAN;

const readAuthIndex = (file: AuthFileItem): string =>
  readString(file.authIndex ?? file['auth_index']);

const readProjectId = (file: AuthFileItem): string =>
  readString(
    file.projectId ?? file.project_id ?? file.geminiVirtualProject ?? file.gemini_virtual_project
  );

const readPlanType = (file: AuthFileItem): string | null => {
  return resolveAuthFilePlanType(file);
};

const resolveAccountLabel = (file: AuthFileItem): string =>
  readString(file.email) ||
  readString(file.account) ||
  readString(file.label) ||
  readString(file.note) ||
  file.name;

const resolveStatusMessage = (file: AuthFileItem): string =>
  readString(file.statusMessage ?? file['status_message']);

const buildInspectionMap = (
  results: AccountInspectionResult[] | undefined
): Map<string, AccountInspectionSummary> => {
  const map = new Map<string, AccountInspectionSummary>();
  if (!results) return map;

  results.forEach((result) => {
    const fileName = result.fileName.trim();
    if (!fileName) return;
    const key = getAuthFileCodexInspectionKeyForIdentity({
      fileName,
      runtimeId: result.runtimeId,
      provider: result.provider,
      authIndex: result.authIndex,
      accountId: result.accountId,
      accountSnapshot: result.accountSnapshot,
    });
    const current = map.get(key);
    if (current && current.createdAtMs >= result.createdAtMs) return;
    map.set(key, {
      source: result.inspectionSource ?? 'server',
      disabled: result.disabled,
      action: result.action || 'keep',
      actionReason: result.actionReason || '',
      actionStatus: result.actionStatus || 'none',
      executedAction: result.executedAction || '',
      statusCode: result.statusCode ?? null,
      usedPercent: result.usedPercent ?? null,
      isQuota: result.isQuota ?? null,
      planType: result.planType ?? null,
      quotaWindows: result.quotaWindows ?? [],
      quotaInventoryObserved: result.quotaInventoryObserved,
      error: result.error ?? '',
      errorKind: result.errorKind ?? '',
      runId: result.runId,
      resultId: result.id,
      createdAtMs: result.createdAtMs,
    });
  });
  return map;
};

export const buildAccountInspectionBySelectionKey = (
  files: AuthFileItem[],
  results: AccountInspectionResult[] | undefined,
  evidenceBoundaryBySelectionKey?: ReadonlyMap<string, AccountCredentialEvidenceBoundary>
): Map<string, AccountInspectionSummary> => {
  const inspectionByIdentity = buildInspectionMap(results);
  const fileNameCounts = files.reduce((counts, file) => {
    counts.set(file.name, (counts.get(file.name) ?? 0) + 1);
    return counts;
  }, new Map<string, number>());
  const inspections = new Map<string, AccountInspectionSummary>();
  files.forEach((file) => {
    const selectionKey = getAuthFileSelectionKey(file);
    const exactInspection = inspectionByIdentity.get(getAuthFileCodexInspectionKeyForFile(file));
    const fallbackInspection =
      fileNameCounts.get(file.name) === 1
        ? inspectionByIdentity.get(getAuthFileCodexInspectionKey(file.name, null))
        : undefined;
    const inspection =
      exactInspection && fallbackInspection
        ? fallbackInspection.createdAtMs > exactInspection.createdAtMs
          ? fallbackInspection
          : exactInspection
        : (exactInspection ?? fallbackInspection);
    if (!inspection) return;
    const boundary = evidenceBoundaryBySelectionKey?.get(selectionKey);
    const usesExactInspection = inspection === exactInspection;
    const boundaryAtMs = Math.max(
      boundary?.inspectionAtMs ?? 0,
      usesExactInspection ? 0 : (boundary?.fallbackInspectionAtMs ?? 0)
    );
    const authenticationBoundaryAtMs = boundary?.authenticationAtMs ?? 0;
    const inspectionBaselinePending = usesExactInspection
      ? boundary?.inspectionBaselinePending === true
      : boundary?.fallbackInspectionBaselinePending === true;
    if (inspectionBaselinePending) return;
    if (inspection.createdAtMs <= boundaryAtMs) return;
    if (
      inspection.createdAtMs <= authenticationBoundaryAtMs &&
      hasActiveCodexInspectionAuthenticationFailure(inspection)
    ) {
      return;
    }
    const credentialRefreshAtMs = readAuthFileCredentialRefreshAtMs(file) ?? 0;
    if (
      credentialRefreshAtMs > 0 &&
      inspection.createdAtMs <= credentialRefreshAtMs &&
      hasActiveCodexInspectionAuthenticationFailure(inspection)
    ) {
      return;
    }
    inspections.set(selectionKey, inspection);
  });
  return inspections;
};

const buildUsageSummary = (file: AuthFileItem): AccountUsageSummary => {
  const recentRequests = normalizeRecentRequestBuckets(file.recent_requests ?? file.recentRequests);
  const totals = sumRecentRequests(recentRequests);
  const total = totals.success + totals.failure;
  return {
    success: totals.success,
    failure: totals.failure,
    successRate: total > 0 ? (totals.success / total) * 100 : null,
    recentRequests,
  };
};

export const buildAccountRows = (
  files: AuthFileItem[],
  stores: AccountQuotaStores,
  inspectionResults?: AccountInspectionResult[],
  overrides?: AccountQuotaOverrides,
  inspectionBySelectionKey?: ReadonlyMap<string, AccountInspectionSummary>,
  evidenceBoundaryBySelectionKey?: ReadonlyMap<string, AccountCredentialEvidenceBoundary>,
  statusBoundaryBySelectionKey?: ReadonlyMap<string, AccountCredentialEvidenceBoundary>
): AccountRow[] => {
  const resolvedInspectionBySelectionKey =
    inspectionBySelectionKey ?? buildAccountInspectionBySelectionKey(files, inspectionResults);
  return files.map((file) => {
    const provider = normalizeAccountProvider(file);
    const authIndex = readAuthIndex(file);
    const selectionKey = getAuthFileSelectionKey(file);
    const resolvedInspection = resolvedInspectionBySelectionKey.get(selectionKey) ?? null;
    const evidenceBoundary = evidenceBoundaryBySelectionKey?.get(selectionKey);
    const statusBoundary = statusBoundaryBySelectionKey?.get(selectionKey);
    const statusBoundaryAtMs = statusBoundary?.inspectionAtMs ?? 0;
    const inspection =
      resolvedInspection && resolvedInspection.createdAtMs <= statusBoundaryAtMs
        ? null
        : resolvedInspection;
    const updatedAtMs = readAuthFileUpdatedAtMs(file);
    const inspectionSupersedesRawDisabled =
      typeof inspection?.disabled === 'boolean' &&
      inspection.createdAtMs > 0 &&
      (updatedAtMs === null || inspection.createdAtMs >= updatedAtMs);
    const effectiveFile = inspectionSupersedesRawDisabled
      ? { ...file, disabled: inspection.disabled }
      : file;
    const codexQuota =
      provider === 'codex'
        ? (overrides?.codexQuotaBySelectionKey?.get(selectionKey) ??
          getCredentialScopedQuotaState(stores.codexQuota, file))
        : undefined;
    const credentialAuthenticationBoundaryAtMs = Math.max(
      evidenceBoundary?.authenticationAtMs ?? 0,
      statusBoundary?.authenticationAtMs ?? 0
    );
    const authenticationAtMs = getAccountCredentialEvidenceCutoffs({
      providerQuota: codexQuota,
      inspection,
      authenticationBoundaryAtMs: credentialAuthenticationBoundaryAtMs,
      credentialRefreshAtMs: readAuthFileCredentialRefreshAtMs(file) ?? 0,
    }).authenticationAtMs;
    const rawStatusMessage = resolveStatusMessage(file);
    const rawStatusCodes = getAuthFileCredentialStatusCodes(file);
    const boundarySupersedesRawStatus = (
      boundary: AccountCredentialEvidenceBoundary | undefined
    ): boolean => {
      if (!boundary) return false;
      const rawStatusMessageMatches =
        rawStatusMessage !== '' && (boundary.rawStatusMessages ?? []).includes(rawStatusMessage);
      const rawStatusCodeMatches = rawStatusCodes.some((statusCode) =>
        (boundary.rawStatusCodes ?? []).includes(statusCode)
      );
      if (!rawStatusMessageMatches && !rawStatusCodeMatches) {
        return false;
      }
      if (updatedAtMs === null) return true;
      if (
        (boundary.authenticationAtMs ?? 0) > 0 &&
        updatedAtMs > (boundary.authenticationAtMs ?? 0)
      ) {
        return false;
      }
      const boundaryAtMs = Math.max(
        boundary.rawStatusAtMs,
        (boundary.authenticationAtMs ?? 0) > 0
          ? (boundary.authenticationAtMs ?? 0)
          : boundary.localAtMs
      );
      return boundaryAtMs > 0 && updatedAtMs <= boundaryAtMs;
    };
    const hasCapturedRawStatusBoundary = [evidenceBoundary, statusBoundary].some(
      (boundary) =>
        (boundary?.rawStatusMessages?.length ?? 0) > 0 ||
        (boundary?.rawStatusCodes?.length ?? 0) > 0
    );
    const hasAuthenticationRecoveryBoundary = [evidenceBoundary, statusBoundary].some(
      (boundary) => (boundary?.authenticationAtMs ?? 0) > 0
    );
    const rawCredentialStatusSuperseded =
      boundarySupersedesRawStatus(evidenceBoundary) ||
      boundarySupersedesRawStatus(statusBoundary) ||
      (!hasCapturedRawStatusBoundary &&
        !hasAuthenticationRecoveryBoundary &&
        authenticationAtMs > 0 &&
        updatedAtMs !== null &&
        authenticationAtMs >= updatedAtMs);
    const quota = resolveAccountQuota(effectiveFile, stores, overrides);
    return {
      key: file.name,
      selectionKey,
      fileName: file.name,
      accountLabel: resolveAccountLabel(file),
      provider,
      planType: quota.planType ?? readPlanType(file),
      canonicalPlanType: getCanonicalPlanType(provider, quota.planType ?? readPlanType(file)),
      disabled: effectiveFile.disabled === true,
      runtimeOnly:
        file.runtimeOnly === true || file.runtimeOnly === 'true' || file.runtime_only === true,
      statusMessage: rawCredentialStatusSuperseded ? '' : rawStatusMessage,
      authIndex,
      projectId: readProjectId(file),
      note: readString(file.note),
      priority: readNumber(file.priority),
      createdAtMs: readAuthFileCreatedAtMs(file),
      updatedAtMs,
      authenticationAtMs,
      rawCredentialStatusSuperseded,
      quota,
      usage: buildUsageSummary(file),
      inspection,
      raw: file,
    };
  });
};

export const findAccountRowForInspectionTarget = (
  rows: AccountRow[],
  target: AccountInspectionTarget
): AccountRow | null => {
  const targetKey = getAuthFileCodexInspectionKeyForIdentity(target);
  const exactMatches = rows.filter(
    (row) => getAuthFileCodexInspectionKeyForFile(row.raw) === targetKey
  );
  if (exactMatches.length === 1) return exactMatches[0];
  if (exactMatches.length > 1) return null;

  const hasStableIdentity = Boolean(
    String(target.runtimeId ?? '').trim() ||
    String(target.authIndex ?? '').trim() ||
    String(target.accountId ?? '').trim() ||
    String(target.accountSnapshot ?? '').trim()
  );
  if (hasStableIdentity) return null;

  const matchingFileRows = rows.filter((row) => row.fileName === target.fileName);
  return matchingFileRows.length === 1 ? matchingFileRows[0] : null;
};

type AccountMetricStatus =
  | 'available'
  | 'needsAttention'
  | 'quotaRisk'
  | 'disabled'
  | 'unconfirmed';

const hasOperationalItems = (
  itemsByRowKey: ReadonlyMap<string, readonly unknown[]> | undefined,
  rowKey: string
): boolean => (itemsByRowKey?.get(rowKey)?.length ?? 0) > 0;

const hasPartialGroupedQuota = (row: AccountRow): boolean =>
  row.quota.groupedAvailabilityState === 'partial';

const getAccountQuotaEvidenceAtMs = (row: AccountRow): number | null => {
  const value =
    row.quota.source === 'observed-header'
      ? (row.quota.observedQuotaAtMs ?? row.quota.observedAtMs ?? row.quota.fetchedAtMs)
      : (row.quota.fetchedAtMs ?? row.quota.observedQuotaAtMs ?? row.quota.observedAtMs);
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null;
};

const hasCurrentAccountQuotaEvidence = (row: AccountRow): boolean => {
  if (row.authenticationAtMs <= 0) return true;
  const observedAtMs = getAccountQuotaEvidenceAtMs(row);
  return observedAtMs !== null && observedAtMs >= row.authenticationAtMs;
};

const getRowRequestHealthEvidence = (
  row: AccountRow,
  requestEvidenceBySelectionKey?: AccountRequestEvidenceBySelectionKey
) => resolveAccountRequestHealthEvidence(requestEvidenceBySelectionKey?.get(row.selectionKey));

const getRowRequestEvidenceInput = (
  row: AccountRow,
  requestEvidenceBySelectionKey?: AccountRequestEvidenceBySelectionKey
) => requestEvidenceBySelectionKey?.get(row.selectionKey);

const getRowRequestCredentialEvidence = (
  row: AccountRow,
  requestEvidenceBySelectionKey?: AccountRequestEvidenceBySelectionKey
) => {
  const requestEvidence = getRowRequestHealthEvidence(row, requestEvidenceBySelectionKey);
  return isAccountRequestCredentialEvidenceCurrent(row, requestEvidence)
    ? getAccountRequestCredentialEvidence(requestEvidence)
    : null;
};

const needsAccountAttention = (
  row: AccountRow,
  context: AccountMetricOperationalContext
): boolean => {
  const requestEvidenceInput = getRowRequestEvidenceInput(
    row,
    context.requestEvidenceBySelectionKey
  );
  const requestEvidence = resolveAccountRequestHealthEvidence(requestEvidenceInput);
  const currentRequestEvidence = isAccountRequestHealthEvidenceCurrent(row, requestEvidence)
    ? requestEvidence
    : null;
  return Boolean(
    resolveAccountAuthenticationProblemEvidence(row, requestEvidence) ||
    isAccountQuotaRefreshProblemCurrent(row, requestEvidence) ||
    (isAccountObservedDiagnosticProblemCurrent(row, requestEvidence) &&
      !hasAccountQuotaLimitEvidence(row, requestEvidenceInput)) ||
    isAccountInspectionActionable(row, requestEvidence) ||
    currentRequestEvidence?.direction === 'negative' ||
    hasOperationalItems(context.pendingActionsByRowKey, row.selectionKey)
  );
};

const hasAccountQuotaRisk = (row: AccountRow, context: AccountMetricOperationalContext): boolean =>
  row.quota.status === 'low' ||
  row.quota.status === 'exhausted' ||
  hasAccountQuotaLimitEvidence(
    row,
    getRowRequestEvidenceInput(row, context.requestEvidenceBySelectionKey)
  ) ||
  hasPartialGroupedQuota(row) ||
  hasOperationalItems(context.quotaCooldownsByRowKey, row.selectionKey);

const hasConfirmedAvailableEvidence = (
  row: AccountRow,
  context: AccountMetricOperationalContext
): boolean => {
  const requestEvidence = getRowRequestHealthEvidence(row, context.requestEvidenceBySelectionKey);
  return (
    (row.quota.status === 'ok' && hasCurrentAccountQuotaEvidence(row)) ||
    isAccountInspectionHealthyEvidence(row) ||
    (isAccountRequestHealthEvidenceCurrent(row, requestEvidence) &&
      requestEvidence?.kind === 'success')
  );
};

const classifyAccountMetricStatus = (
  row: AccountRow,
  context: AccountMetricOperationalContext
): AccountMetricStatus => {
  if (row.disabled || row.quota.status === 'disabled') return 'disabled';
  if (needsAccountAttention(row, context)) return 'needsAttention';
  if (hasAccountQuotaRisk(row, context)) return 'quotaRisk';
  if (!hasConfirmedAvailableEvidence(row, context)) return 'unconfirmed';
  return 'available';
};

export const buildAccountMetrics = (
  rows: AccountRow[],
  context: AccountMetricOperationalContext = {}
): AccountMetrics => {
  const metrics: AccountMetrics = {
    total: rows.length,
    available: 0,
    needsAttention: 0,
    quotaRisk: 0,
    disabled: 0,
    unconfirmed: 0,
    needsInspectionAction: 0,
  };

  rows.forEach((row) => {
    const status = classifyAccountMetricStatus(row, context);
    metrics[status] += 1;
    const requestEvidence = getRowRequestHealthEvidence(row, context.requestEvidenceBySelectionKey);
    if (
      isAccountInspectionActionable(row, requestEvidence) &&
      row.inspection &&
      ['delete', 'disable', 'enable', 'reauth'].includes(row.inspection.action)
    ) {
      metrics.needsInspectionAction += 1;
    }
  });

  return metrics;
};

const isAccountRowAvailable = (
  row: AccountRow,
  requestEvidenceBySelectionKey?: AccountRequestEvidenceBySelectionKey
): boolean => {
  const requestEvidenceInput = getRowRequestEvidenceInput(row, requestEvidenceBySelectionKey);
  const requestEvidence = resolveAccountRequestHealthEvidence(requestEvidenceInput);
  const currentRequestEvidence = isAccountRequestHealthEvidenceCurrent(row, requestEvidence)
    ? requestEvidence
    : null;
  return (
    !row.disabled &&
    !resolveAccountAuthenticationProblemEvidence(row, requestEvidence) &&
    !hasAccountQuotaLimitEvidence(row, requestEvidenceInput) &&
    !isAccountQuotaRefreshProblemCurrent(row, requestEvidence) &&
    row.quota.status !== 'exhausted' &&
    !isAccountObservedDiagnosticProblemCurrent(row, requestEvidence) &&
    !isAccountInspectionActionable(row, requestEvidence) &&
    currentRequestEvidence?.direction !== 'negative' &&
    hasConfirmedAvailableEvidence(row, { requestEvidenceBySelectionKey })
  );
};

export const filterAccountRows = (rows: AccountRow[], filters: AccountRowFilters): AccountRow[] => {
  const search = filters.search.trim().toLowerCase();
  const wildcard = search.includes('*')
    ? new RegExp(
        search
          .split('*')
          .map((segment) => segment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
          .join('.*'),
        'i'
      )
    : null;
  return rows.filter((row) => {
    if (filters.provider !== 'all' && row.provider !== filters.provider) return false;
    const rowPlan = getAccountPlanFilterValue(row.provider, row.planType);
    // `filters.plan` is already a canonical filter identity (see getPlanOptionValue),
    // so it must be compared directly against the row's canonical value. Re-canonicalizing
    // it per row provider would re-introduce cross-provider collisions (e.g. Codex `pro`
    // mapping to `pro_20x` while Claude/Antigravity `pro` stays `pro`).
    if (filters.plan !== 'all' && rowPlan !== filters.plan) {
      return false;
    }
    if (
      !matchesStatusFilter(
        row,
        filters.status,
        filters.codexStatusBySelectionKey,
        filters
      )
    ) {
      return false;
    }
    if (!matchesQuotaBand(row, filters.quotaBand)) return false;
    if (!search) return true;
    const values = [
      row.accountLabel,
      row.fileName,
      row.provider,
      row.planType,
      row.canonicalPlanType,
      row.authIndex,
      row.projectId,
      row.note,
      row.statusMessage,
      row.raw.state,
      row.raw.status,
      row.raw.error,
      row.raw.errorStatus,
      row.raw['error_status'],
      row.quota.source,
      row.quota.error,
      row.quota.observedTraceId,
      row.quota.observedErrorKind,
      row.quota.observedErrorCode,
      row.quota.activeLimit,
      row.quota.creditsBalance,
      row.quota.rateLimitReachedType,
      row.inspection?.actionReason,
    ];
    return values.some((value) => {
      const text = readString(value);
      return wildcard ? wildcard.test(text) : text.toLowerCase().includes(search);
    });
  });
};

export const sortAccountRows = (
  rows: AccountRow[],
  sort?: AccountRowSort,
  requestEvidenceBySelectionKey?: AccountRequestEvidenceBySelectionKey
): AccountRow[] => {
  const defaultSorted = [...rows].sort((left, right) =>
    compareDefaultAccountRows(left, right, requestEvidenceBySelectionKey)
  );
  if (!sort || sort.key === 'default') return defaultSorted;

  return defaultSorted.sort((left, right) => {
    const byColumn = compareAccountRowsBySort(left, right, sort);
    return byColumn === 0
      ? compareDefaultAccountRows(left, right, requestEvidenceBySelectionKey)
      : byColumn;
  });
};

export const getProviderOptions = (rows: AccountRow[]) =>
  Array.from(new Set(rows.map((row) => row.provider))).sort();

const getUnknownPlanLabel = (t?: TFunction): string =>
  t?.('auth_files.codex_plan_filter_unknown', { defaultValue: 'Unknown plan' }) ?? 'Unknown plan';

/** Explicit compatibility aliases for values persisted before canonical filters. */
const LEGACY_PLAN_FILTER_ALIASES: Readonly<Record<string, string>> = {
  prolite: 'pro_5x',
  'pro-lite': 'pro_5x',
  pro_lite: 'pro_5x',
  plan_free: 'free',
  plan_pro: 'pro',
  plan_max: 'max',
  plan_max5: 'max_5x',
  plan_max20: 'max_20x',
  plan_team: 'team',
  max5: 'max_5x',
  max20: 'max_20x',
  self_serve_business_prolite: 'business_premium_5x',
  self_serve_business_usage_based: 'business_usage_based',
  ent26: 'enterprise',
  hc: 'enterprise',
  enterprise_cbp_automation: 'enterprise_automation',
  enterprise_cbp_usage_based: 'enterprise_usage_based',
  education: 'edu',
  ultra_lite: 'ultra-lite',
};

const normalizePlanFilterValue = (value: string): string => {
  const normalized = value.trim().toLowerCase();
  return LEGACY_PLAN_FILTER_ALIASES[normalized] ?? normalized;
};

const comparePlanOptions = (left: AccountPlanOption, right: AccountPlanOption): number => {
  if (left.value === UNKNOWN_ACCOUNT_PLAN) return 1;
  if (right.value === UNKNOWN_ACCOUNT_PLAN) return -1;
  const byLabel = left.label.localeCompare(right.label, undefined, {
    numeric: true,
    sensitivity: 'base',
  });
  return byLabel || left.value.localeCompare(right.value, undefined, { numeric: true });
};

export const getPlanOptions = (rows: AccountRow[], t?: TFunction): AccountPlanOption[] => {
  const labels = new Map<string, string>();
  rows.forEach((row) => {
    const plan = getAccountPlanFilterValue(row.provider, row.planType);
    // The reserved `unknown` bucket aggregates both missing plan types and explicit
    // `unknown` raw values; its label must always be the localized "Unknown plan"
    // regardless of which row the Map encounters first.
    if (plan === UNKNOWN_ACCOUNT_PLAN) {
      labels.set(plan, getUnknownPlanLabel(t));
      return;
    }
    const presentation = getPlanPresentation({ provider: row.provider, planType: row.planType, t });
    const label = getCanonicalPlanFilterLabel(
      plan,
      t,
      presentation?.shortLabel ?? plan
    );
    const previousLabel = labels.get(plan);
    if (!previousLabel || label < previousLabel) labels.set(plan, label);
  });
  return Array.from(labels, ([value, label]) => ({ value, label })).sort(comparePlanOptions);
};

export const getPlanOptionLabel = (rows: AccountRow[], value: string, t?: TFunction): string => {
  const normalizedValue = normalizePlanFilterValue(value);
  if (!normalizedValue) return value;
  if (normalizedValue === UNKNOWN_ACCOUNT_PLAN) return getUnknownPlanLabel(t);
  const directOption = getPlanOptions(rows, t).find((option) => option.value === normalizedValue);
  if (directOption) return directOption.label;
  return getCanonicalPlanFilterLabel(normalizedValue, t);
};

export const getPlanOptionValue = (_rows: AccountRow[], value: string, _t?: TFunction): string => {
  const normalizedValue = normalizePlanFilterValue(value);
  if (!normalizedValue || normalizedValue === 'all' || normalizedValue === UNKNOWN_ACCOUNT_PLAN) {
    return normalizedValue || value;
  }
  return normalizedValue;
};

const matchesStatusFilter = (
  row: AccountRow,
  status: AccountStatusFilter,
  codexStatusBySelectionKey?: ReadonlyMap<string, AuthFileCodexStatusSummary>,
  context: AccountMetricOperationalContext = {}
) => {
  if (status === 'all') return true;
  if (isAccountCodexStatusFilter(status)) {
    const codexStatus = codexStatusBySelectionKey?.get(row.selectionKey);
    if (!codexStatus || !authFileMatchesCodexStatusFilter(codexStatus, status)) return false;
    if (status !== 'reauth') return true;
    return (
      getRowRequestCredentialEvidence(row, context.requestEvidenceBySelectionKey)?.direction !==
      'positive'
    );
  }
  if (status === 'available') {
    return isAccountRowAvailable(row, context.requestEvidenceBySelectionKey);
  }
  if (status === 'disabled') return row.disabled;
  if (status === 'unconfirmed') {
    return classifyAccountMetricStatus(row, context) === 'unconfirmed';
  }
  if (status === 'problem') {
    const requestEvidenceInput = getRowRequestEvidenceInput(
      row,
      context.requestEvidenceBySelectionKey
    );
    const requestEvidence = resolveAccountRequestHealthEvidence(requestEvidenceInput);
    const currentRequestEvidence = isAccountRequestHealthEvidenceCurrent(row, requestEvidence)
      ? requestEvidence
      : null;
    return (
      Boolean(resolveAccountAuthenticationProblemEvidence(row, requestEvidence)) ||
      isAccountQuotaRefreshProblemCurrent(row, requestEvidence) ||
      (isAccountObservedDiagnosticProblemCurrent(row, requestEvidence) &&
        !hasAccountQuotaLimitEvidence(row, requestEvidenceInput)) ||
      currentRequestEvidence?.direction === 'negative'
    );
  }
  if (status === 'low') return row.quota.status === 'low';
  if (status === 'exhausted') return row.quota.status === 'exhausted';
  if (status === 'inspection') {
    return isAccountInspectionActionable(
      row,
      getRowRequestHealthEvidence(row, context.requestEvidenceBySelectionKey)
    );
  }
  return true;
};

const matchesQuotaBand = (row: AccountRow, band: AccountQuotaBand) => {
  if (band === 'all') return true;
  const remaining = row.quota.remainingPercent;
  if (band === 'spent') return remaining !== null && remaining <= 0;
  if (band === 'lt20')
    return remaining !== null && remaining > 0 && remaining < QUOTA_LOW_THRESHOLD;
  if (band === 'between20and50') {
    return remaining !== null && remaining >= QUOTA_LOW_THRESHOLD && remaining < QUOTA_OK_THRESHOLD;
  }
  if (band === 'ge50') return remaining !== null && remaining >= QUOTA_OK_THRESHOLD;
  return true;
};

const getRiskRank = (
  row: AccountRow,
  requestEvidenceBySelectionKey?: AccountRequestEvidenceBySelectionKey
) => {
  const requestEvidenceInput = getRowRequestEvidenceInput(row, requestEvidenceBySelectionKey);
  const requestEvidence = resolveAccountRequestHealthEvidence(requestEvidenceInput);
  const currentRequestEvidence = isAccountRequestHealthEvidenceCurrent(row, requestEvidence)
    ? requestEvidence
    : null;
  const authenticationProblem = resolveAccountAuthenticationProblemEvidence(row, requestEvidence);
  if (authenticationProblem) return 7;
  if (isAccountInspectionActionable(row, requestEvidence)) return 7;
  if (row.quota.status === 'exhausted') return 6;
  if (row.quota.status === 'low') return 5;
  if (hasAccountQuotaLimitEvidence(row, requestEvidenceInput)) return 5;
  if (currentRequestEvidence?.kind === 'transient_failure') return 4;
  if (isAccountQuotaRefreshProblemCurrent(row, requestEvidence)) return 4;
  if (isAccountObservedDiagnosticProblemCurrent(row, requestEvidence)) return 4;
  if (hasPartialGroupedQuota(row)) return 3;
  if (row.disabled) return 2;
  if (isAccountCredentialStatusProblemCurrent(row, requestEvidence)) return 1;
  return 0;
};

const compareDefaultAccountRows = (
  left: AccountRow,
  right: AccountRow,
  requestEvidenceBySelectionKey?: AccountRequestEvidenceBySelectionKey
) => {
  const leftRisk = getRiskRank(left, requestEvidenceBySelectionKey);
  const rightRisk = getRiskRank(right, requestEvidenceBySelectionKey);
  if (leftRisk !== rightRisk) return rightRisk - leftRisk;
  if (left.provider !== right.provider) return left.provider.localeCompare(right.provider);
  return left.fileName.localeCompare(right.fileName, undefined, {
    numeric: true,
    sensitivity: 'base',
  });
};

const getAccountPlanSortRank = (provider: string, planType: string | null): number | null => {
  const presentation = getPlanPresentation({ provider, planType });
  if (!presentation?.known || !presentation.canonicalPlanType) return null;
  const normalized = presentation.canonicalPlanType;
  if (normalized === 'pro_20x') return 50;
  if (normalized === 'pro_5x') return 40;
  if (normalized === 'team') return 30;
  if (normalized === 'plus') return 20;
  if (normalized === 'free') return 10;
  return 0;
};

const compareAccountPlanTypes = (
  leftProvider: string,
  left: string | null,
  rightProvider: string,
  right: string | null,
  direction: AccountRowSortDirection
) => {
  const leftCanonical = getCanonicalPlanType(leftProvider, left);
  const rightCanonical = getCanonicalPlanType(rightProvider, right);
  const leftRank = getAccountPlanSortRank(leftProvider, left);
  const rightRank = getAccountPlanSortRank(rightProvider, right);
  const leftKnown = leftRank !== null;
  const rightKnown = rightRank !== null;
  if (!leftKnown && !rightKnown) return 0;
  if (!leftKnown) return 1;
  if (!rightKnown) return -1;
  const rankComparison = compareNumbers(leftRank, rightRank, direction);
  return rankComparison || compareText(leftCanonical ?? '', rightCanonical ?? '', direction);
};

const compareAccountRowsBySort = (left: AccountRow, right: AccountRow, sort: AccountRowSort) => {
  if (sort.key === 'name') {
    const accountComparison = compareText(left.accountLabel, right.accountLabel, sort.direction);
    return accountComparison || compareText(left.fileName, right.fileName, sort.direction);
  }
  if (sort.key === 'plan') {
    return compareAccountPlanTypes(
      left.provider,
      left.planType,
      right.provider,
      right.planType,
      sort.direction
    );
  }
  if (sort.key === 'note') {
    return compareText(left.note ?? '', right.note ?? '', sort.direction, true);
  }
  if (sort.key === 'priority') {
    return compareNumbers(left.priority ?? 0, right.priority ?? 0, sort.direction);
  }
  if (sort.key === 'recent') {
    const leftTotal = left.usage.success + left.usage.failure;
    const rightTotal = right.usage.success + right.usage.failure;
    return compareNumbers(leftTotal, rightTotal, sort.direction);
  }
  if (sort.key === 'quota') {
    return compareNullableNumbers(
      left.quota.remainingPercent,
      right.quota.remainingPercent,
      sort.direction
    );
  }
  if (sort.key === 'created') {
    return compareNullableNumbers(left.createdAtMs, right.createdAtMs, sort.direction);
  }
  if (sort.key === 'reset') {
    return compareQuotaResets(left.quota, right.quota, sort.direction);
  }
  return 0;
};

const compareText = (
  left: string,
  right: string,
  direction: AccountRowSortDirection,
  emptyLast = false
) => {
  const leftValue = left.trim();
  const rightValue = right.trim();
  if (emptyLast) {
    if (!leftValue && !rightValue) return 0;
    if (!leftValue) return 1;
    if (!rightValue) return -1;
  }
  const result = leftValue.localeCompare(rightValue, undefined, {
    numeric: true,
    sensitivity: 'base',
  });
  return direction === 'asc' ? result : -result;
};

const compareNumbers = (left: number, right: number, direction: AccountRowSortDirection) => {
  const result = left - right;
  return direction === 'asc' ? result : -result;
};

const compareNullableNumbers = (
  left: number | null,
  right: number | null,
  direction: AccountRowSortDirection
) => {
  const leftKnown = typeof left === 'number' && Number.isFinite(left);
  const rightKnown = typeof right === 'number' && Number.isFinite(right);
  if (!leftKnown && !rightKnown) return 0;
  if (!leftKnown) return 1;
  if (!rightKnown) return -1;
  return compareNumbers(left, right, direction);
};
