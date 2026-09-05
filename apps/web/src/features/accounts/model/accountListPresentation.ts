import type { TFunction } from 'i18next';
import type { QuotaCooldownInfo } from '@/services/api';
import type { AuthFileCodexStatusSummary } from '@/features/authFiles/model/credentialStatus';
import type { AccountRow } from './accountRows';
import {
  summarizeGroupedQuotaAvailability,
  type AccountGroupedQuotaAvailabilitySummary,
} from './accountQuotaSummary';
import type { AccountQuotaWindowKind } from './accountQuotaDisplayWindows';
import type { QuotaModelScope, QuotaResetAccuracy } from '@/types';
import {
  buildAccountRecommendation,
  isAccountRecommendationEvidenceSensitive,
  type AccountRecommendation,
} from './quotaRecommendations';
import type { UsageValueSource } from './usageValueRows';
import { isValidQuotaResetAtMs } from '@/utils/quota/formatters';
import { isCodexMainQuotaWindow } from '@/utils/quota/codexQuota';
import { getPlanPresentation, type PlanPresentation } from '@/utils/plans';
import {
  classifyAccountCredentialStatusEvidence,
  classifyAccountObservedDiagnosticEvidence,
  classifyAccountQuotaRefreshEvidence,
  getAccountRequestCredentialEvidence,
  getAccountRequestEvidenceDetail,
  getAccountRequestQuotaEvidenceDetail,
  hasAccountQuotaLimitEvidence,
  isAccountCredentialQuotaLimitCurrent,
  isAccountCredentialStatusProblemCurrent,
  isAccountInspectionAuthenticationFailure,
  isAccountInspectionHealthyEvidence,
  isAccountInspectionActionable,
  isAccountInspectionStatusEvidenceCurrent,
  isAccountObservedDiagnosticProblemCurrent,
  isAccountRequestCredentialEvidenceCurrent,
  isAccountRequestHealthEvidenceCurrent,
  isAccountRequestQuotaEvidenceCurrent,
  resolveAccountAuthenticationProblemEvidence,
  resolveAccountExceptionProblemEvidence,
  resolveAccountRequestHealthEvidence,
  resolveAccountRequestQuotaEvidence,
  type AccountAuthenticationProblemSource,
  type AccountRequestEvidenceInput,
  type AccountRequestCredentialEvidence,
  type AccountRequestHealthEvidence,
  type AccountRequestQuotaEvidence,
} from './accountHealthEvidence';

export type AccountListHealthStatusKey =
  | 'reauth'
  | 'five_hour_cooldown'
  | 'weekly_cooldown'
  | 'monthly_cooldown'
  | 'five_hour_exhausted'
  | 'weekly_exhausted'
  | 'monthly_exhausted'
  | 'limited'
  | 'partial'
  | 'disabled'
  | 'exception'
  | 'available'
  | 'raw';
export type AccountListHealthReasonTone = 'muted' | 'warning' | 'danger';

type AccountListQuotaWindowKind = AccountQuotaWindowKind;
type AccountListSupportedLimitKind = Extract<
  AccountQuotaWindowKind,
  'five_hour' | 'weekly' | 'monthly'
>;
type AccountListQuotaLimitKind = AccountListSupportedLimitKind | 'unknown';
type HealthTooltipParams = Record<string, string | number>;

export interface AccountListQuotaWindowPresentation {
  key: string;
  label: string;
  kind?: AccountQuotaWindowKind;
  remainingPercent: number | null;
  usedPercent: number | null;
  resetLabel: string;
  resetAtMs: number | null;
  resetAccuracy: QuotaResetAccuracy;
  groupLabel?: string;
  modelScope?: QuotaModelScope;
}

export type AccountListQuotaWindowInput = Omit<
  AccountListQuotaWindowPresentation,
  'resetAtMs' | 'resetAccuracy'
> & {
  resetAtMs?: number | null;
  resetAccuracy?: QuotaResetAccuracy;
};

export interface AccountListPresentationItem {
  identity: {
    title: string;
    subtitle: string;
    fileName: string;
    provider: string;
    planType: string | null;
    planPresentation: PlanPresentation | null;
    priority: number;
    priorityIsNegative: boolean;
  };
  health: {
    status: AccountListHealthStatusKey;
    labelKey: string;
    tooltipKey: string;
    tooltipParams: HealthTooltipParams;
    reasonKey: string;
    reasonParams: HealthTooltipParams;
    reasonTone: AccountListHealthReasonTone;
    cooldown: QuotaCooldownInfo | null;
    resetAtMs: number | null;
    basisLabelKey?: string;
    observedAtMs: number | null;
  };
  quota: {
    remainingPercent: number | null;
    usedPercent: number | null;
    resetLabel: string;
    resetAtMs: number | null;
    resetAccuracy: QuotaResetAccuracy;
    statusLabelKey: string;
    sourceShortLabelKey: string;
  };
  activity: {
    recentTotal: number;
    successCount: number;
    failureCount: number;
    successRate: number | null;
    estimatedValue: number;
    totalTokens: number;
    lastSeenMs: number | null;
    source: UsageValueSource;
    hasHealthData: boolean;
  };
  recommendation: {
    item: AccountRecommendation | null;
    hasRecommendation: boolean;
    actionLabelKey: string;
    reasonKey: string;
    priority: AccountRecommendation['priority'] | null;
  };
}

export interface AccountListPresentationOptions {
  t?: TFunction;
  recommendation?: AccountRecommendation | null;
  quotaCooldown?: QuotaCooldownInfo | null;
  estimatedValuePerRequest?: number;
  activity?: {
    requests: number;
    successRate: number | null;
    inputTokens: number;
    outputTokens: number;
    estimatedCost: number;
    lastSeenMs: number | null;
    source: UsageValueSource;
  } | null;
  codexStatus?: AuthFileCodexStatusSummary | null;
  quotaWindows?: AccountListQuotaWindowInput[];
  requestEvidence?: AccountRequestEvidenceInput;
}

const DEFAULT_ESTIMATED_VALUE_PER_REQUEST = 0.018;

const quotaStatusLabelKey = (status: AccountRow['quota']['status']) => {
  switch (status) {
    case 'ok':
      return 'accounts.quota_status_ok';
    case 'low':
      return 'accounts.quota_status_low';
    case 'exhausted':
      return 'accounts.quota_status_exhausted';
    case 'error':
      return 'accounts.quota_status_error';
    case 'loading':
      return 'accounts.quota_status_loading';
    case 'disabled':
      return 'accounts.quota_status_disabled';
    case 'unknown':
    default:
      return 'accounts.quota_status_unknown';
  }
};

const quotaSourceShortLabelKey = (source: AccountRow['quota']['source']) => {
  switch (source) {
    case 'observed-header':
      return 'accounts.quota_source_short_observed';
    case 'cache':
      return 'accounts.quota_source_short_cache';
    case 'none':
    default:
      return 'accounts.quota_source_short_none';
  }
};

export const getRecommendationActionLabelKey = (action: AccountRecommendation['action']) => {
  switch (action) {
    case 'refresh':
      return 'accounts.recommend_action_refresh';
    case 'disable':
      return 'accounts.recommend_action_disable';
    case 'enable':
      return 'accounts.recommend_action_enable';
    case 'restore-default':
      return 'accounts.recommend_action_restore';
    case 'reauth':
      return 'accounts.recommend_action_reauth';
    default:
      return 'accounts.recommend_action_review';
  }
};

const extractHttpStatusCode = (value: string | null | undefined): string => {
  const text = value?.trim() ?? '';
  const match = text.match(/\b([1-5][0-9]{2})\b/);
  return match?.[1] ?? '';
};

const getCooldownRecoverAtLabel = (quotaCooldown: QuotaCooldownInfo): string => {
  const date = new Date(quotaCooldown.recoverAtMs);
  return Number.isNaN(date.getTime()) ? '-' : date.toLocaleString();
};

const getFirstDetail = (...values: Array<string | number | null | undefined>): string => {
  for (const value of values) {
    const text = value === null || value === undefined ? '' : String(value).trim();
    if (text) return text;
  }
  return '-';
};

const getHttpStatusDetail = (statusCode: number | null | undefined): string =>
  statusCode ? `HTTP ${statusCode}` : '';

const getAuthenticationProblemDetail = (
  row: AccountRow,
  source: AccountAuthenticationProblemSource | null,
  statusCode: number | null,
  inspection: AccountRow['inspection'],
  requestCredentialEvidence: AccountRequestCredentialEvidence | null,
  credentialStatusDetail: string
): string => {
  if (source === 'request' && requestCredentialEvidence) {
    return getAccountRequestEvidenceDetail(requestCredentialEvidence);
  }
  if (source === 'quota_refresh') {
    return getFirstDetail(
      row.quota.error,
      getHttpStatusDetail(row.quota.errorStatus),
      row.quota.observedErrorCode
    );
  }
  if (source === 'inspection') {
    const inspectionStatusDetail = getHttpStatusDetail(inspection?.statusCode);
    if (inspection?.statusCode === 401) {
      return getFirstDetail(
        inspectionStatusDetail,
        inspection.actionReason,
        inspection.errorKind,
        credentialStatusDetail
      );
    }
    return getFirstDetail(
      inspection?.actionReason,
      inspection?.errorKind,
      inspectionStatusDetail,
      credentialStatusDetail
    );
  }
  if (source === 'observed_header') {
    return getFirstDetail(
      row.quota.observedErrorCode,
      row.quota.observedErrorKind,
      getHttpStatusDetail(statusCode)
    );
  }
  if (source === 'credential_status') {
    return getFirstDetail(
      credentialStatusDetail,
      row.statusMessage,
      getHttpStatusDetail(statusCode)
    );
  }
  return getFirstDetail(
    requestCredentialEvidence ? getAccountRequestEvidenceDetail(requestCredentialEvidence) : '',
    inspection?.actionReason,
    credentialStatusDetail,
    row.quota.error,
    row.quota.observedErrorCode,
    row.quota.observedErrorKind
  );
};

const getExceptionDetail = (
  row: AccountRow,
  inspection: AccountRow['inspection'],
  requestEvidence: AccountRequestHealthEvidence | null,
  credentialStatusDetail: string,
  quotaRefreshDetail: string,
  observedDiagnosticDetail: string
): string =>
  getFirstDetail(
    requestEvidence ? getAccountRequestEvidenceDetail(requestEvidence) : '',
    credentialStatusDetail,
    quotaRefreshDetail,
    inspection?.actionReason,
    observedDiagnosticDetail,
    observedDiagnosticDetail ? row.quota.observedTraceId : ''
  );

const getExceptionReasonDetail = (
  inspection: AccountRow['inspection'],
  requestEvidence: AccountRequestHealthEvidence | null,
  credentialStatusDetail: string,
  quotaRefreshDetail: string,
  observedDiagnosticDetail: string
): string =>
  getFirstDetail(
    requestEvidence ? getAccountRequestEvidenceDetail(requestEvidence) : '',
    getHttpStatusDetail(inspection?.statusCode),
    observedDiagnosticDetail,
    quotaRefreshDetail,
    inspection?.actionReason,
    credentialStatusDetail
  );

const getExceptionReasonKey = (
  inspection: AccountRow['inspection'],
  requestEvidence: AccountRequestHealthEvidence | null,
  quotaRefreshProblem: boolean,
  hasObservedDiagnosticProblem: boolean
): string => {
  if (requestEvidence?.kind === 'transient_failure') {
    return 'accounts.health_reason_exception_request';
  }
  if (quotaRefreshProblem) {
    return 'accounts.health_reason_exception_quota';
  }
  if (hasObservedDiagnosticProblem) {
    return 'accounts.health_reason_exception_header';
  }
  if (inspection) {
    return 'accounts.health_reason_exception_inspection';
  }
  return 'accounts.health_reason_exception_request';
};

const isHeaderQuotaLimitEvidence = (row: AccountRow): boolean => {
  if (row.quota.source === 'observed-header') {
    return true;
  }
  if (classifyAccountObservedDiagnosticEvidence(row) === 'quota') return true;
  if (!row.quota.rateLimitReachedType || !row.quota.observedAtMs) return false;
  return !row.quota.fetchedAtMs || row.quota.observedAtMs > row.quota.fetchedAtMs;
};

const getLimitedReasonKey = (
  row: AccountRow,
  requestQuotaEvidence: AccountRequestQuotaEvidence | null = null
): string => {
  if (requestQuotaEvidence) return 'accounts.health_reason_limited_request';
  return isHeaderQuotaLimitEvidence(row)
    ? 'accounts.health_reason_limited_header'
    : 'accounts.health_reason_limited_quota';
};

const getQuotaLimitDetail = (
  row: AccountRow,
  requestQuotaEvidence: AccountRequestQuotaEvidence | null = null,
  hasCurrentCredentialQuotaLimit = true
): string => {
  const runtimeQuotaDetail =
    hasCurrentCredentialQuotaLimit && classifyAccountCredentialStatusEvidence(row) === 'quota'
      ? row.statusMessage
      : '';
  const observedQuotaDetail =
    classifyAccountObservedDiagnosticEvidence(row) === 'quota'
      ? getFirstDetail(row.quota.observedErrorCode, row.quota.observedErrorKind)
      : '';
  const detail = getFirstDetail(
    requestQuotaEvidence ? getAccountRequestQuotaEvidenceDetail(requestQuotaEvidence) : '',
    row.quota.rateLimitReachedType,
    runtimeQuotaDetail,
    observedQuotaDetail,
    row.quota.error
  );
  return detail === '-' ? '' : detail;
};

const getQuotaLimitTooltip = (
  row: AccountRow,
  requestQuotaEvidence: AccountRequestQuotaEvidence | null,
  hasCurrentCredentialQuotaLimit: boolean
): Pick<HealthStatusResolution, 'tooltipKey' | 'tooltipParams'> => {
  if (requestQuotaEvidence) {
    return {
      tooltipKey: 'accounts.health_tip_limited',
      tooltipParams: { detail: getAccountRequestQuotaEvidenceDetail(requestQuotaEvidence) },
    };
  }
  if (row.quota.creditsOverageLimitReached === true) {
    return { tooltipKey: 'accounts.health_tip_limited_credits_overage', tooltipParams: {} };
  }
  if (row.quota.spendControlReached === true) {
    return { tooltipKey: 'accounts.health_tip_limited_spend_control', tooltipParams: {} };
  }
  const detail = getQuotaLimitDetail(row, null, hasCurrentCredentialQuotaLimit);
  if (detail) {
    return { tooltipKey: 'accounts.health_tip_limited', tooltipParams: { detail } };
  }
  return { tooltipKey: 'accounts.health_tip_limited', tooltipParams: { detail: '-' } };
};

const inferQuotaWindowKind = (
  window: AccountListQuotaWindowPresentation
): AccountListQuotaWindowKind | null => {
  if (window.kind) return window.kind;
  const text = `${window.key} ${window.label}`.toLowerCase();
  if (/(pay-as-you-go|payg|on[-_\s]?demand|按量|按需)/.test(text)) return 'payg';
  if (/(billing|账单|帳單)/.test(text)) return 'billing';
  if (/(product|model|模型|产品|產品)/.test(text)) return 'product';
  if (/(month|monthly|30d|31d|月)/.test(text)) return 'monthly';
  if (/(week|weekly|7d|7 day|seven|周|週)/.test(text)) return 'weekly';
  if (/(day|daily|24h|24 h|日)/.test(text)) return 'daily';
  if (/(five|5h|5 h|5-hour|5_hour|five-hour|5小时|5 小时)/.test(text)) {
    return 'five_hour';
  }
  return null;
};

const isSupportedLimitWindowKind = (
  kind: AccountListQuotaWindowKind | null
): kind is AccountListSupportedLimitKind =>
  kind === 'five_hour' || kind === 'weekly' || kind === 'monthly';

const windowKindRank: Record<AccountListSupportedLimitKind, number> = {
  five_hour: 1,
  weekly: 2,
  monthly: 3,
};

const getHighestWindowKind = (
  left: AccountListSupportedLimitKind | null,
  right: AccountListSupportedLimitKind
): AccountListSupportedLimitKind =>
  left === null || windowKindRank[right] > windowKindRank[left] ? right : left;

const hasAvailablePaygWindow = (quotaWindows: AccountListQuotaWindowPresentation[]): boolean =>
  quotaWindows.some((window) => {
    if (window.remainingPercent === null || window.remainingPercent <= 0) return false;
    return inferQuotaWindowKind(window) === 'payg';
  });

const isCoveredBillingWindow = (
  window: AccountListQuotaWindowPresentation,
  hasPaygRemaining: boolean
): boolean => hasPaygRemaining && inferQuotaWindowKind(window) === 'billing';

const resolveQuotaWindowLimitKind = (
  quotaWindows: AccountListQuotaWindowPresentation[] = []
): AccountListQuotaLimitKind | null => {
  let selected: AccountListSupportedLimitKind | null = null;
  let hasUnknownLimitedWindow = false;
  const hasPaygRemaining = hasAvailablePaygWindow(quotaWindows);

  quotaWindows.forEach((window) => {
    if (window.remainingPercent === null || window.remainingPercent > 0) return;
    if (isCoveredBillingWindow(window, hasPaygRemaining)) return;
    const windowKind = inferQuotaWindowKind(window);
    if (!isSupportedLimitWindowKind(windowKind)) {
      hasUnknownLimitedWindow = true;
      return;
    }
    selected = getHighestWindowKind(selected, windowKind);
  });

  if (selected) return selected;
  return hasUnknownLimitedWindow ? 'unknown' : null;
};

const resolveAntigravityAvailability = (
  row: AccountRow,
  quotaWindows: AccountListQuotaWindowPresentation[]
): AccountGroupedQuotaAvailabilitySummary | null => {
  if (row.provider !== 'antigravity') return null;
  return summarizeGroupedQuotaAvailability(
    quotaWindows.map((window) => ({
      groupLabel: window.groupLabel,
      kind: window.kind,
      remainingPercent: window.remainingPercent,
      resetLabel: window.resetLabel,
      resetAtMs: window.resetAtMs,
      resetAccuracy: window.resetAccuracy,
    }))
  );
};

const resolveCodexLimitKind = (
  codexStatus?: AuthFileCodexStatusSummary | null
): AccountListQuotaLimitKind | null => {
  if (!codexStatus) return null;
  if (codexStatus.isMonthlyLimited) return 'monthly';
  if (codexStatus.isWeeklyLimited) return 'weekly';
  if (codexStatus.isFiveHourLimited) return 'five_hour';
  if (codexStatus.isUnknownQuotaLimited) return 'unknown';
  return null;
};

const getCooldownStatusForWindow = (
  windowKind: AccountListSupportedLimitKind
): AccountListHealthStatusKey => {
  if (windowKind === 'monthly') return 'monthly_cooldown';
  if (windowKind === 'weekly') return 'weekly_cooldown';
  return 'five_hour_cooldown';
};

const getExhaustedStatusForWindow = (
  windowKind: AccountListSupportedLimitKind
): AccountListHealthStatusKey => {
  if (windowKind === 'monthly') return 'monthly_exhausted';
  if (windowKind === 'weekly') return 'weekly_exhausted';
  return 'five_hour_exhausted';
};

type AccountListQuotaReset = Pick<
  AccountListQuotaWindowPresentation,
  'resetLabel' | 'resetAtMs' | 'resetAccuracy'
>;

const getResetForLimitKind = (
  kind: AccountListQuotaLimitKind | null,
  codexStatus?: AuthFileCodexStatusSummary | null,
  quotaWindows: AccountListQuotaWindowPresentation[] = [],
  fallback: AccountListQuotaReset = {
    resetLabel: '-',
    resetAtMs: null,
    resetAccuracy: 'unknown',
  }
): AccountListQuotaReset => {
  const matchedWindows = quotaWindows.filter(
    (window) =>
      window.remainingPercent !== null &&
      window.remainingPercent <= 0 &&
      (kind === null || kind === 'unknown' || inferQuotaWindowKind(window) === kind)
  );
  if (matchedWindows.length > 0) {
    const unknownResetWindow = matchedWindows.find((window) => window.resetAtMs === null);
    if (unknownResetWindow) {
      return {
        resetLabel: unknownResetWindow.resetLabel || fallback.resetLabel,
        resetAtMs: null,
        resetAccuracy: 'unknown',
      };
    }
    const matchedWindow = matchedWindows.reduce((current, next) =>
      (next.resetAtMs ?? 0) > (current.resetAtMs ?? 0) ? next : current
    );
    const resetAccuracy = matchedWindows.some((window) => window.resetAccuracy === 'unknown')
      ? 'unknown'
      : matchedWindows.some((window) => window.resetAccuracy === 'estimated')
        ? 'estimated'
        : 'exact';
    return {
      resetLabel: matchedWindow.resetLabel || fallback.resetLabel,
      resetAtMs: matchedWindow.resetAtMs,
      resetAccuracy,
    };
  }

  const codexReset =
    kind === 'monthly'
      ? {
          resetLabel: codexStatus?.monthlyResetLabel,
          resetAtMs: codexStatus?.monthlyResetAtMs ?? null,
          resetAccuracy: codexStatus?.monthlyResetAccuracy ?? 'unknown',
        }
      : kind === 'weekly'
        ? {
            resetLabel: codexStatus?.weeklyResetLabel,
            resetAtMs: codexStatus?.weeklyResetAtMs ?? null,
            resetAccuracy: codexStatus?.weeklyResetAccuracy ?? 'unknown',
          }
        : kind === 'five_hour'
          ? {
              resetLabel: codexStatus?.fiveHourResetLabel,
              resetAtMs: codexStatus?.fiveHourResetAtMs ?? null,
              resetAccuracy: codexStatus?.fiveHourResetAccuracy ?? 'unknown',
            }
          : null;
  if (codexReset && (codexReset.resetLabel || codexReset.resetAtMs !== null)) {
    return {
      resetLabel: codexReset.resetLabel ?? fallback.resetLabel,
      resetAtMs: codexReset.resetAtMs ?? null,
      resetAccuracy: codexReset.resetAccuracy ?? 'unknown',
    };
  }
  return fallback;
};

const hasKnownAvailableQuota = (
  row: AccountRow,
  quotaWindows: AccountListQuotaWindowPresentation[],
  antigravityAvailability: AccountGroupedQuotaAvailabilitySummary | null
): boolean => {
  if (antigravityAvailability) {
    return (
      antigravityAvailability.state === 'available' || antigravityAvailability.state === 'partial'
    );
  }
  const hasPaygRemaining = hasAvailablePaygWindow(quotaWindows);
  const knownWindowRemaining = quotaWindows
    .filter((window) => !isCoveredBillingWindow(window, hasPaygRemaining))
    .map((window) => window.remainingPercent)
    .filter((value): value is number => value !== null && Number.isFinite(value));

  if (knownWindowRemaining.length > 0) {
    return knownWindowRemaining.every((value) => value > 0);
  }

  return row.quota.status === 'ok' || row.quota.status === 'low';
};

type AccountAvailableEvidenceSource = 'quota' | 'inspection' | 'request';

type AccountAvailableEvidence = {
  source: AccountAvailableEvidenceSource;
  observedAtMs: number | null;
  priority: number;
};

const normalizeAvailableEvidenceTimestamp = (value: number | null | undefined): number | null =>
  typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null;

const getAvailableQuotaObservedAtMs = (row: AccountRow): number | null =>
  normalizeAvailableEvidenceTimestamp(
    row.quota.source === 'observed-header'
      ? (row.quota.observedQuotaAtMs ?? row.quota.observedAtMs ?? row.quota.fetchedAtMs)
      : (row.quota.fetchedAtMs ?? row.quota.observedQuotaAtMs ?? row.quota.observedAtMs)
  );

const hasCurrentAvailableQuotaEvidence = (row: AccountRow): boolean => {
  if (row.authenticationAtMs <= 0) return true;
  const observedAtMs = getAvailableQuotaObservedAtMs(row);
  return observedAtMs !== null && observedAtMs >= row.authenticationAtMs;
};

const getAvailableQuotaBasisLabelKey = (row: AccountRow): string => {
  if (row.quota.source === 'observed-header') return 'accounts.quota_source_observed_header';
  if (row.quota.source === 'cache') return 'accounts.quota_source_cache';
  return 'accounts.quota_source_none';
};

const resolveLatestAvailableEvidence = (
  row: AccountRow,
  hasAvailableQuota: boolean,
  requestEvidence: AccountRequestHealthEvidence | null
): AccountAvailableEvidence | null => {
  const candidates: AccountAvailableEvidence[] = [];
  if (hasAvailableQuota && hasCurrentAvailableQuotaEvidence(row)) {
    candidates.push({
      source: 'quota',
      observedAtMs: getAvailableQuotaObservedAtMs(row),
      priority: 1,
    });
  }
  if (isAccountInspectionHealthyEvidence(row)) {
    candidates.push({
      source: 'inspection',
      observedAtMs: normalizeAvailableEvidenceTimestamp(row.inspection?.createdAtMs),
      priority: 2,
    });
  }
  if (requestEvidence?.kind === 'success') {
    candidates.push({
      source: 'request',
      observedAtMs: normalizeAvailableEvidenceTimestamp(requestEvidence.request.timestamp_ms),
      priority: 3,
    });
  }
  if (candidates.length === 0) return null;

  return candidates.sort((left, right) => {
    const leftUnknown = left.observedAtMs === null;
    const rightUnknown = right.observedAtMs === null;
    if (leftUnknown !== rightUnknown) return leftUnknown ? 1 : -1;
    const observedAtDiff = (right.observedAtMs ?? 0) - (left.observedAtMs ?? 0);
    return observedAtDiff || right.priority - left.priority;
  })[0];
};

type HealthStatusResolution = {
  status: AccountListHealthStatusKey;
  tooltipKey: string;
  tooltipParams?: HealthTooltipParams;
  reasonKey: string;
  reasonParams?: HealthTooltipParams;
  reasonTone: AccountListHealthReasonTone;
  resetAtMs?: number | null;
  basisLabelKey?: string;
  observedAtMs?: number | null;
};

const resolveHealthStatus = (
  row: AccountRow,
  quotaCooldown?: QuotaCooldownInfo | null,
  codexStatus?: AuthFileCodexStatusSummary | null,
  quotaWindows: AccountListQuotaWindowPresentation[] = [],
  requestEvidenceInput: AccountRequestEvidenceInput = {}
): HealthStatusResolution => {
  const resolvedRequestEvidence = resolveAccountRequestHealthEvidence(requestEvidenceInput);
  const requestEvidence = isAccountRequestHealthEvidenceCurrent(row, resolvedRequestEvidence)
    ? resolvedRequestEvidence
    : null;
  const requestCredentialEvidence = isAccountRequestCredentialEvidenceCurrent(
    row,
    resolvedRequestEvidence
  )
    ? getAccountRequestCredentialEvidence(resolvedRequestEvidence)
    : null;
  const actionableInspection = isAccountInspectionActionable(row, resolvedRequestEvidence)
    ? row.inspection
    : null;
  const statusInspection = isAccountInspectionStatusEvidenceCurrent(row, resolvedRequestEvidence)
    ? row.inspection
    : null;
  const hasCredentialStatusProblem = isAccountCredentialStatusProblemCurrent(
    row,
    resolvedRequestEvidence
  );
  const credentialStatusDetail = hasCredentialStatusProblem ? row.statusMessage : '';
  const hasObservedDiagnosticProblem = isAccountObservedDiagnosticProblemCurrent(
    row,
    resolvedRequestEvidence
  );
  const exceptionProblem = resolveAccountExceptionProblemEvidence(row, resolvedRequestEvidence);
  const exceptionRequestEvidence = exceptionProblem?.source === 'request' ? requestEvidence : null;
  const exceptionInspection =
    exceptionProblem?.source === 'inspection' ? actionableInspection : null;
  const quotaRefreshProblem = exceptionProblem?.source === 'quota_refresh';
  const quotaRefreshDetail = quotaRefreshProblem
    ? getFirstDetail(row.quota.error, getHttpStatusDetail(row.quota.errorStatus))
    : '';
  const observedDiagnosticDetail =
    !exceptionProblem && hasObservedDiagnosticProblem
      ? [row.quota.observedErrorKind, row.quota.observedErrorCode].filter(Boolean).join(' / ')
      : '';
  const resolvedRequestQuotaEvidence = resolveAccountRequestQuotaEvidence(requestEvidenceInput);
  const requestQuotaEvidence = isAccountRequestQuotaEvidenceCurrent(
    row,
    resolvedRequestQuotaEvidence
  )
    ? resolvedRequestQuotaEvidence
    : null;
  const hasQuotaLimitEvidence = hasAccountQuotaLimitEvidence(row, requestEvidenceInput);
  const hasCredentialQuotaLimitEvidence = isAccountCredentialQuotaLimitCurrent(
    row,
    resolvedRequestEvidence
  );
  const authenticationProblem = resolveAccountAuthenticationProblemEvidence(
    row,
    resolvedRequestEvidence
  );
  const hasRowAuthenticationEvidence =
    classifyAccountCredentialStatusEvidence(row) === 'credential_failure' ||
    classifyAccountQuotaRefreshEvidence(row) === 'credential_failure' ||
    classifyAccountObservedDiagnosticEvidence(row) === 'credential_failure' ||
    isAccountInspectionAuthenticationFailure(row);
  const providerStatusNeedsReauth =
    row.provider === 'xai' &&
    codexStatus?.needsReauth === true &&
    !hasRowAuthenticationEvidence &&
    requestCredentialEvidence?.direction !== 'positive';
  const antigravityAvailability = resolveAntigravityAvailability(row, quotaWindows);
  const resolveEffectiveQuotaWindowLimitKind = () =>
    antigravityAvailability
      ? antigravityAvailability.state !== 'exhausted'
        ? null
        : isSupportedLimitWindowKind(
              (antigravityAvailability.resetKind as AccountListQuotaWindowKind | undefined) ?? null
            )
          ? (antigravityAvailability.resetKind as AccountListSupportedLimitKind)
          : 'unknown'
      : resolveQuotaWindowLimitKind(quotaWindows);

  if (authenticationProblem || providerStatusNeedsReauth) {
    const authenticationSource =
      authenticationProblem?.source ?? (statusInspection ? 'inspection' : 'credential_status');
    const authenticationDetail = getAuthenticationProblemDetail(
      row,
      authenticationSource,
      authenticationProblem?.statusCode ?? null,
      statusInspection,
      requestCredentialEvidence,
      credentialStatusDetail
    );
    const quotaRefreshRequiresReauth = authenticationSource === 'quota_refresh';
    const quotaRefreshStatusCode = quotaRefreshRequiresReauth
      ? String(
          authenticationProblem?.statusCode ??
            row.quota.errorStatus ??
            extractHttpStatusCode(row.quota.error)
        )
      : '';
    const requestRequiresReauth = authenticationSource === 'request';
    return {
      status: 'reauth',
      tooltipKey: 'accounts.health_tip_reauth',
      tooltipParams: { detail: authenticationDetail },
      reasonKey: quotaRefreshStatusCode
        ? 'accounts.health_reason_reauth_quota_refresh'
        : authenticationSource === 'inspection'
          ? 'accounts.health_reason_reauth_inspection'
          : 'accounts.health_reason_reauth_auth',
      reasonParams: quotaRefreshStatusCode
        ? { code: quotaRefreshStatusCode }
        : { detail: authenticationDetail },
      reasonTone: 'danger',
      ...(requestRequiresReauth
        ? {
            basisLabelKey: 'accounts.latest_request_time_title',
            observedAtMs: authenticationProblem?.observedAtMs ?? null,
          }
        : authenticationSource === 'observed_header'
          ? {
              basisLabelKey: 'accounts.quota_source_observed_header',
              observedAtMs: authenticationProblem?.observedAtMs ?? null,
            }
          : {}),
    };
  }

  if (quotaCooldown) {
    const windowKind = resolveCodexLimitKind(codexStatus) ?? resolveEffectiveQuotaWindowLimitKind();
    if (windowKind && windowKind !== 'unknown') {
      const reset = getResetForLimitKind(windowKind, codexStatus, quotaWindows, {
        resetLabel: row.quota.resetLabel,
        resetAtMs: row.quota.resetAtMs,
        resetAccuracy: row.quota.resetAccuracy,
      });
      return {
        status: getCooldownStatusForWindow(windowKind),
        tooltipKey: `accounts.health_tip_${getCooldownStatusForWindow(windowKind)}`,
        tooltipParams: {
          recoverAt: getCooldownRecoverAtLabel(quotaCooldown),
          resetAt: reset.resetLabel,
        },
        reasonKey: 'accounts.health_reason_cooldown',
        reasonTone: 'warning',
        resetAtMs: reset.resetAtMs,
      };
    }
    return {
      status: 'limited',
      tooltipKey: 'accounts.health_tip_limited_cooldown',
      tooltipParams: {
        recoverAt: getCooldownRecoverAtLabel(quotaCooldown),
      },
      reasonKey: 'accounts.health_reason_limited_cooldown',
      reasonTone: 'warning',
    };
  }

  const codexLimitKind = resolveCodexLimitKind(codexStatus);
  if (codexLimitKind) {
    if (codexLimitKind !== 'unknown') {
      const reset = getResetForLimitKind(codexLimitKind, codexStatus, quotaWindows, {
        resetLabel: row.quota.resetLabel,
        resetAtMs: row.quota.resetAtMs,
        resetAccuracy: row.quota.resetAccuracy,
      });
      return {
        status: getExhaustedStatusForWindow(codexLimitKind),
        tooltipKey: `accounts.health_tip_${getExhaustedStatusForWindow(codexLimitKind)}`,
        tooltipParams: { resetAt: reset.resetLabel },
        reasonKey: `accounts.health_reason_${getExhaustedStatusForWindow(codexLimitKind)}`,
        reasonTone: 'warning',
        resetAtMs: reset.resetAtMs,
      };
    }
    return {
      status: 'limited',
      tooltipKey: 'accounts.health_tip_limited',
      tooltipParams: { detail: getFirstDetail(row.quota.rateLimitReachedType, row.quota.error) },
      reasonKey: getLimitedReasonKey(row),
      reasonTone: 'warning',
    };
  }

  const quotaWindowLimitKind = resolveEffectiveQuotaWindowLimitKind();
  if (quotaWindowLimitKind) {
    if (quotaWindowLimitKind !== 'unknown') {
      const reset =
        antigravityAvailability?.state === 'exhausted'
          ? {
              resetLabel: antigravityAvailability.resetLabel,
              resetAtMs: antigravityAvailability.resetAtMs,
              resetAccuracy: antigravityAvailability.resetAccuracy,
            }
          : getResetForLimitKind(quotaWindowLimitKind, null, quotaWindows, {
              resetLabel: row.quota.resetLabel,
              resetAtMs: row.quota.resetAtMs,
              resetAccuracy: row.quota.resetAccuracy,
            });
      return {
        status: getExhaustedStatusForWindow(quotaWindowLimitKind),
        tooltipKey: `accounts.health_tip_${getExhaustedStatusForWindow(quotaWindowLimitKind)}`,
        tooltipParams: { resetAt: reset.resetLabel },
        reasonKey: `accounts.health_reason_${getExhaustedStatusForWindow(quotaWindowLimitKind)}`,
        reasonTone: 'warning',
        resetAtMs: reset.resetAtMs,
      };
    }
    return {
      status: 'limited',
      tooltipKey: 'accounts.health_tip_limited',
      tooltipParams: { detail: getFirstDetail(row.quota.rateLimitReachedType, row.quota.error) },
      reasonKey: getLimitedReasonKey(row),
      reasonTone: 'warning',
    };
  }

  if (hasQuotaLimitEvidence) {
    const tooltip = getQuotaLimitTooltip(
      row,
      requestQuotaEvidence,
      hasCredentialQuotaLimitEvidence
    );
    return {
      status: 'limited',
      ...tooltip,
      reasonKey: getLimitedReasonKey(row, requestQuotaEvidence),
      reasonTone: 'warning',
      ...(requestQuotaEvidence
        ? {
            basisLabelKey: 'accounts.latest_request_time_title',
            observedAtMs: requestQuotaEvidence.request.timestamp_ms,
          }
        : hasCredentialQuotaLimitEvidence
          ? {
              basisLabelKey: 'accounts.detail_overview_basis_credential_state',
              observedAtMs: row.updatedAtMs,
            }
          : {}),
    };
  }

  if (row.quota.status === 'exhausted' || row.quota.remainingPercent === 0) {
    return {
      status: 'limited',
      tooltipKey: 'accounts.health_tip_limited',
      tooltipParams: {
        detail: getFirstDetail(row.quota.rateLimitReachedType, row.quota.resetLabel),
      },
      reasonKey: getLimitedReasonKey(row),
      reasonTone: 'warning',
    };
  }

  if (row.disabled || row.quota.status === 'disabled') {
    return {
      status: 'disabled',
      tooltipKey: 'accounts.health_tip_disabled',
      tooltipParams: {
        detail: getFirstDetail(credentialStatusDetail, actionableInspection?.actionReason),
      },
      reasonKey: 'accounts.health_reason_disabled',
      reasonTone: 'muted',
    };
  }

  if (
    (hasCredentialStatusProblem && antigravityAvailability?.state !== 'partial') ||
    exceptionProblem ||
    hasObservedDiagnosticProblem ||
    actionableInspection
  ) {
    const requestFailedTransiently = exceptionProblem?.source === 'request';
    return {
      status: 'exception',
      tooltipKey: 'accounts.health_tip_exception',
      tooltipParams: {
        detail: getExceptionDetail(
          row,
          exceptionInspection,
          exceptionRequestEvidence,
          credentialStatusDetail,
          quotaRefreshDetail,
          observedDiagnosticDetail
        ),
      },
      reasonKey: getExceptionReasonKey(
        exceptionInspection,
        exceptionRequestEvidence,
        quotaRefreshProblem,
        hasObservedDiagnosticProblem
      ),
      reasonParams: {
        detail: getExceptionReasonDetail(
          exceptionInspection,
          exceptionRequestEvidence,
          credentialStatusDetail,
          quotaRefreshDetail,
          observedDiagnosticDetail
        ),
      },
      reasonTone: 'danger',
      ...(requestFailedTransiently
        ? {
            basisLabelKey: 'accounts.latest_request_time_title',
            observedAtMs: exceptionRequestEvidence?.request.timestamp_ms ?? null,
          }
        : {}),
    };
  }

  if (antigravityAvailability?.state === 'partial') {
    const limitedGroups = antigravityAvailability.groups
      .filter((group) => group.remainingPercent <= 0)
      .map((group) => group.label)
      .join(', ');
    return {
      status: 'partial',
      tooltipKey: 'accounts.health_tip_partial',
      tooltipParams: {
        available: antigravityAvailability.availableGroupCount,
        total: antigravityAvailability.totalGroupCount,
        limited: limitedGroups || '-',
      },
      reasonKey: 'accounts.health_reason_partial',
      reasonParams: {
        available: antigravityAvailability.availableGroupCount,
        total: antigravityAvailability.totalGroupCount,
      },
      reasonTone: 'warning',
    };
  }

  const availableEvidence = resolveLatestAvailableEvidence(
    row,
    hasKnownAvailableQuota(row, quotaWindows, antigravityAvailability),
    requestEvidence
  );
  if (availableEvidence?.source === 'quota') {
    return {
      status: 'available',
      tooltipKey: 'accounts.health_tip_available',
      tooltipParams: { detail: getFirstDetail(row.quota.source, row.quota.resetLabel) },
      reasonKey: 'accounts.health_reason_available',
      reasonTone: 'muted',
      basisLabelKey: getAvailableQuotaBasisLabelKey(row),
      observedAtMs: availableEvidence.observedAtMs,
    };
  }

  if (availableEvidence?.source === 'inspection') {
    return {
      status: 'available',
      tooltipKey: 'accounts.health_tip_available',
      tooltipParams: { detail: row.inspection?.actionReason || '-' },
      reasonKey: 'accounts.health_reason_available',
      reasonTone: 'muted',
      basisLabelKey: 'accounts.detail_overview_basis_inspection',
      observedAtMs: availableEvidence.observedAtMs,
    };
  }

  if (availableEvidence?.source === 'request') {
    return {
      status: 'available',
      tooltipKey: 'accounts.health_tip_available_request',
      tooltipParams: {},
      reasonKey: 'accounts.health_reason_available_request',
      reasonTone: 'muted',
      basisLabelKey: 'accounts.latest_request_time_title',
      observedAtMs: availableEvidence.observedAtMs,
    };
  }

  return {
    status: 'raw',
    tooltipKey: 'accounts.health_tip_raw',
    tooltipParams: {
      detail: getFirstDetail(row.statusMessage, row.quota.error, row.quota.status),
    },
    reasonKey: 'accounts.health_reason_raw',
    reasonTone: 'muted',
  };
};

const buildIdentitySubtitle = (row: AccountRow) =>
  [row.fileName, row.authIndex ? `#${row.authIndex}` : '', row.projectId || '']
    .filter(Boolean)
    .join(' · ');

const clampPercent = (value: number) => Math.max(0, Math.min(100, value));

const deriveActivityHealthCounts = (
  row: AccountRow,
  activity: AccountListPresentationOptions['activity']
) => {
  const fallbackSuccessCount = Math.max(0, row.usage.success);
  const fallbackFailureCount = Math.max(0, row.usage.failure);

  if (activity && activity.requests > 0 && activity.successRate !== null) {
    const clampedRate = clampPercent(activity.successRate);
    const successCount = Math.round((activity.requests * clampedRate) / 100);
    return {
      successCount,
      failureCount: Math.max(0, activity.requests - successCount),
    };
  }

  return {
    successCount: fallbackSuccessCount,
    failureCount: fallbackFailureCount,
  };
};

const resolveActivitySuccessRate = (
  row: AccountRow,
  activity: AccountListPresentationOptions['activity'],
  successCount: number,
  failureCount: number
): number | null => {
  if (activity?.successRate !== null && activity?.successRate !== undefined) {
    return clampPercent(activity.successRate);
  }
  if (row.usage.successRate !== null) return clampPercent(row.usage.successRate);
  const total = successCount + failureCount;
  return total > 0 ? (successCount / total) * 100 : null;
};

export const buildRecommendationBySelectionKey = (recommendations: AccountRecommendation[]) => {
  const map = new Map<string, AccountRecommendation>();
  recommendations.forEach((item) => {
    map.set(item.row.selectionKey, item);
  });
  return map;
};

export const buildAccountListItem = (
  row: AccountRow,
  options: AccountListPresentationOptions = {}
): AccountListPresentationItem => {
  const computedRecommendation = buildAccountRecommendation(row, options.requestEvidence);
  const providedRecommendation = options.recommendation ?? null;
  const providedRecommendationIsEvidenceSensitive =
    isAccountRecommendationEvidenceSensitive(providedRecommendation);
  const recommendation =
    computedRecommendation ??
    (providedRecommendationIsEvidenceSensitive ? null : providedRecommendation);
  const quotaCooldown = options.quotaCooldown ?? null;
  const fallbackRecentTotal = row.usage.success + row.usage.failure;
  const activity = options.activity ?? null;
  const recentTotal = activity?.requests ?? fallbackRecentTotal;
  const activityCounts = deriveActivityHealthCounts(row, activity);
  const successCount = activityCounts.successCount;
  const failureCount = activityCounts.failureCount;
  const successRate = resolveActivitySuccessRate(row, activity, successCount, failureCount);
  const estimatedValue =
    activity?.estimatedCost ??
    fallbackRecentTotal * (options.estimatedValuePerRequest ?? DEFAULT_ESTIMATED_VALUE_PER_REQUEST);
  const quotaWindows: AccountListQuotaWindowPresentation[] = (options.quotaWindows ?? []).map(
    (window) => {
      const resetAtMs = isValidQuotaResetAtMs(window.resetAtMs) ? window.resetAtMs : null;
      return {
        ...window,
        resetAtMs,
        resetAccuracy: resetAtMs !== null ? (window.resetAccuracy ?? 'unknown') : 'unknown',
      };
    }
  );
  const accountQuotaWindows =
    row.provider === 'codex' ? quotaWindows.filter(isCodexMainQuotaWindow) : quotaWindows;
  const health = resolveHealthStatus(
    row,
    quotaCooldown,
    options.codexStatus ?? null,
    accountQuotaWindows,
    options.requestEvidence
  );
  const planPresentation = getPlanPresentation({
    provider: row.provider,
    planType: row.planType,
    t: options.t,
  });

  return {
    identity: {
      title: row.accountLabel,
      subtitle: buildIdentitySubtitle(row),
      fileName: row.fileName,
      provider: row.provider,
      planType: row.planType,
      planPresentation,
      priority: row.priority ?? 0,
      priorityIsNegative: row.priority !== null && row.priority < 0,
    },
    health: {
      status: health.status,
      labelKey: `accounts.health_${health.status}`,
      tooltipKey: health.tooltipKey,
      tooltipParams: health.tooltipParams ?? {},
      reasonKey: health.reasonKey,
      reasonParams: health.reasonParams ?? {},
      reasonTone: health.reasonTone,
      cooldown: quotaCooldown,
      resetAtMs: health.resetAtMs ?? null,
      basisLabelKey: health.basisLabelKey,
      observedAtMs: health.observedAtMs ?? null,
    },
    quota: {
      remainingPercent: row.quota.remainingPercent,
      usedPercent: row.quota.usedPercent,
      resetLabel: row.quota.resetLabel,
      resetAtMs: row.quota.resetAtMs,
      resetAccuracy: row.quota.resetAccuracy,
      statusLabelKey: quotaStatusLabelKey(row.quota.status),
      sourceShortLabelKey: quotaSourceShortLabelKey(row.quota.source),
    },
    activity: {
      recentTotal,
      successCount,
      failureCount,
      successRate,
      estimatedValue,
      totalTokens: activity ? activity.inputTokens + activity.outputTokens : 0,
      lastSeenMs: activity?.lastSeenMs ?? null,
      source: activity?.source ?? 'recent',
      hasHealthData: successCount + failureCount > 0,
    },
    recommendation: {
      item: recommendation,
      hasRecommendation: recommendation !== null,
      actionLabelKey: recommendation
        ? getRecommendationActionLabelKey(recommendation.action)
        : 'accounts.recommend_normal',
      reasonKey: recommendation?.reasonKey ?? 'accounts.recommend_normal_desc',
      priority: recommendation?.priority ?? null,
    },
  };
};
