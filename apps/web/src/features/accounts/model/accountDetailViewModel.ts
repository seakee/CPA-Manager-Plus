import type { TFunction } from 'i18next';
import type { CodexQuotaState, QuotaResetAccuracy, XaiQuotaState } from '@/types';
import { getSortedCodexResetCreditExpiries } from '@/components/quota/quotaConfigs';
import type {
  AccountActionCandidate,
  MonitoringAccountHistoryItem,
  MonitoringAccountLatestRequest,
  MonitoringAnalyticsEventRow,
  MonitoringAnalyticsRecentFailure,
  MonitoringAnalyticsSummary,
  MonitoringAccountWindowUsageItem,
  QuotaCooldownInfo,
} from '@/services/api';
import type { AuthFileCodexStatusSummary } from '@/features/authFiles/model/credentialStatus';
import { normalizeStringValue, parseIdTokenPayload } from '@/utils/quota/parsers';
import { isValidQuotaResetAtMs } from '@/utils/quota/formatters';
import { isCodexMainQuotaWindow } from '@/utils/quota/codexQuota';
import { parseTimestampMs } from '@/utils/timestamp';
import { sumRecentRequests, type RecentRequestBucket } from '@/utils/recentRequests';
import type { AccountRow } from './accountRows';
import {
  buildAccountListItem,
  getRecommendationActionLabelKey,
  type AccountListHealthStatusKey,
  type AccountListPresentationItem,
} from './accountListPresentation';
import { summarizeGroupedQuotaAvailability } from './accountQuotaSummary';
import {
  inferAccountQuotaWindowKind,
  type AccountQuotaDisplayWindow,
  type AccountQuotaWindowKind,
} from './accountQuotaDisplayWindows';
import { accountWindowUsageRequestKey } from './accountWindowUsageRows';
import { estimateWindowUsage, type WindowUsageForecast } from './estimateWindowUsage';
import type {
  AccountQuotaBoundaryAccuracy,
  AccountQuotaCycleDefinition,
} from './accountQuotaWindowDefinitions';
import { accountOperationalItemMatchesRow } from './accountOperationalScope';
import {
  buildAccountRecommendation,
  isAccountRecommendationEvidenceSensitive,
  type AccountRecommendation,
  type AccountRecommendationPriority,
} from './quotaRecommendations';
import type { UsageValueRow, UsageValueSource } from './usageValueRows';
import { getPlanPresentation, resolveAuthFilePlanType, type PlanPresentation } from '@/utils/plans';
import {
  classifyAccountCredentialStatusEvidence,
  getAccountRequestCredentialEvidence,
  isAccountCredentialStatusProblemCurrent,
  isAccountInspectionAuthenticationFailure,
  isAccountInspectionHealthyEvidence,
  isAccountRequestCredentialEvidenceCurrent,
  isAccountRequestHealthEvidenceCurrent,
  mergeAccountRequestEvidenceInputs,
  resolveAccountRequestHealthEvidence,
  type AccountRequestEvidenceDirection,
  type AccountRequestEvidenceInput,
} from './accountHealthEvidence';

export type AccountDetailValueKind =
  | 'text'
  | 'i18n'
  | 'number'
  | 'percent'
  | 'money'
  | 'timestamp'
  | 'quota_reset';

export const ACCOUNT_OVERVIEW_ACTIVITY_RANGE_DAYS = 7;
export const ACCOUNT_OVERVIEW_ACTIVITY_RANGE_MS =
  ACCOUNT_OVERVIEW_ACTIVITY_RANGE_DAYS * 24 * 60 * 60 * 1000;

export type AccountDetailOverviewTargetTab = 'quota' | 'config' | 'diagnostics';
export type AccountDetailOverviewActivityScope = 'monitoring_7d' | 'recent_snapshot';

export interface AccountDetailField {
  key: string;
  labelKey: string;
  value: string | number | null;
  valueKind?: AccountDetailValueKind;
}

export interface AccountDetailQuotaWindowInput extends Omit<
  AccountQuotaDisplayWindow,
  'limitWindowSeconds' | 'resetAtMs' | 'resetAccuracy' | 'fromMs' | 'toMs'
> {
  providerWindowId?: string;
  limitWindowSeconds?: number | null;
  resetAtMs?: number | null;
  resetAccuracy?: QuotaResetAccuracy;
  fromMs?: number | null;
  toMs?: number | null;
  boundaryAccuracy?: AccountQuotaBoundaryAccuracy;
  stale?: boolean;
  logicalWindowId?: number;
  activationGeneration?: number;
  availability?: string;
  relationshipKind?: string;
  containerProviderWindowId?: string;
  firstSeenAtMs?: number;
  lastSeenAtMs?: number;
  missingSinceMs?: number | null;
  deactivatedAtMs?: number | null;
  currentCycle?: AccountQuotaCycleDefinition | null;
  previousCycle?: AccountQuotaCycleDefinition | null;
}

export interface AccountDetailWindowUsageSummary {
  fromMs: number;
  toMs: number;
  matched: boolean;
  totalRequests: number;
  successCalls: number;
  failureCalls: number;
  totalTokens: number;
  totalCost: number;
  successRate: number | null;
  lastSeenMs: number | null;
  syncStatus: string;
  scopeMatchStatus: string;
  unmatchedRequests: number;
}

export interface AccountDetailQuotaWindow extends Omit<
  AccountDetailQuotaWindowInput,
  'resetAtMs' | 'resetAccuracy'
> {
  resetAtMs: number | null;
  resetAccuracy: QuotaResetAccuracy;
  usage: AccountDetailWindowUsageSummary | null;
  currentUsage: AccountDetailWindowUsageSummary | null;
  previousUsage: AccountDetailWindowUsageSummary | null;
  previousPeriod: 'previous' | 'previous_equal_range' | null;
  forecast: WindowUsageForecast | null;
}

export interface AccountDetailResetCreditExpiry {
  id: string;
  expiresAtMs: number;
}

export interface AccountDetailActionCandidateSummary {
  id: number;
  actionType: string;
  status: string;
  reasonCode: string;
  reason: string;
  hitCount: number;
  firstSeenAtMs: number;
  lastSeenAtMs: number;
  updatedAtMs: number;
  accountSnapshot: string;
  authLabel: string;
  hasEvidence: boolean;
}

export type AccountDetailDiagnosticEvidenceStatus = 'current' | 'outdated' | 'conflict';

export interface AccountDetailRecentFailureSummary {
  timestampMs: number;
  reason: string;
  statusCode: number | null;
  model: string;
}

export interface AccountDetailDiagnosticsActivity {
  totalCalls: number | null;
  failureCalls: number | null;
  failureRate: number | null;
  p95LatencyMs: number | null;
  latestActivityAtMs: number | null;
  latestSuccessAtMs: number | null;
  latestFailureAtMs: number | null;
  latestHealthDirection: AccountRequestEvidenceDirection | null;
  latestHealthEvidenceAtMs: number | null;
  recentFailure: AccountDetailRecentFailureSummary | null;
}

export interface AccountDetailDiagnosticConclusion {
  actionLabelKey: string;
  reasonKey: string;
  priority: AccountRecommendationPriority | null;
  sourceLabelKey: string;
  observedAtMs: number | null;
  evidenceStatus: AccountDetailDiagnosticEvidenceStatus;
  evidenceStatusLabelKey: string;
  latestActivityAtMs: number | null;
}

export interface AccountDetailValueSummary {
  requests: number;
  successRate: number | null;
  inputTokens: number;
  outputTokens: number;
  totalTokens: number;
  estimatedCost: number | null;
  lastSeenMs: number | null;
  source: UsageValueSource;
}

export interface AccountDetailHistorySummary {
  matched: boolean;
  totalRequests: number;
  successCalls: number;
  failureCalls: number;
  totalTokens: number;
  totalCost: number;
  successRate: number | null;
  firstSeenMs: number | null;
  lastSeenMs: number | null;
  generatedAtMs: number | null;
  syncStatus: string;
}

export interface AccountDetailCodexBadge {
  kind: string;
  tone: 'danger' | 'warning' | 'info';
  labelKey: string;
  defaultLabel: string;
  titleKey?: string;
  defaultTitle?: string;
  labelParams?: Record<string, string | number>;
}

export interface AccountDetailOverviewDecision {
  status: AccountListHealthStatusKey;
  labelKey: string;
  reasonKey: string;
  reasonParams: Record<string, string | number>;
  observedAtMs: number | null;
  basisLabelKey: string;
  targetTab: AccountDetailOverviewTargetTab;
}

export interface AccountDetailOverviewCapacity {
  kind: 'percent' | 'group_availability';
  statusLabelKey: string;
  remainingPercent: number | null;
  availableGroupCount: number | null;
  totalGroupCount: number | null;
  hasData: boolean;
  descriptionKey: string;
  fields: AccountDetailField[];
  targetTab: AccountDetailOverviewTargetTab;
}

export interface AccountDetailOverviewCredential {
  statusLabelKey: string;
  sourceLabelKey: string;
  fields: AccountDetailField[];
  targetTab: AccountDetailOverviewTargetTab;
}

export interface AccountDetailOverviewRecentStatus {
  success: number;
  failure: number;
  successRate: number | null;
  recentRequests: RecentRequestBucket[];
  statusMessage: string;
}

export interface AccountDetailOverviewActivity {
  scope: AccountDetailOverviewActivityScope;
  scopeDays: number | null;
  sourceLabelKey: string;
  hasActivity: boolean;
  emptyStateKey: string;
  metrics: AccountDetailField[];
  targetTab: AccountDetailOverviewTargetTab;
}

export interface AccountDetailOverviewAttention {
  priority: AccountRecommendationPriority;
  actionLabelKey: string;
  reasonKey: string;
  reasonParams?: Record<string, string | number>;
  targetTab: AccountDetailOverviewTargetTab;
}

export interface AccountDetailViewModel {
  selectionKey: string;
  identity: {
    title: string;
    subtitle: string;
    fileName: string;
    accountLabel: string;
    provider: string;
    planType: string | null;
    planPresentation: PlanPresentation | null;
    authIndex: string;
    projectId: string;
    priority: number;
    disabled: boolean;
    runtimeOnly: boolean;
  };
  health: {
    status: AccountListHealthStatusKey;
    labelKey: string;
    tooltipKey: string;
    tooltipParams: Record<string, string | number>;
    reasonKey: string;
    reasonParams: Record<string, string | number>;
    resetAtMs: number | null;
  };
  overview: {
    decision: AccountDetailOverviewDecision;
    capacity: AccountDetailOverviewCapacity;
    credential: AccountDetailOverviewCredential;
    recentStatus: AccountDetailOverviewRecentStatus;
    activity: AccountDetailOverviewActivity;
    attention: AccountDetailOverviewAttention | null;
  };
  quota: {
    statusLabelKey: string;
    sourceShortLabelKey: string;
    fields: AccountDetailField[];
    diagnostics: AccountDetailField[];
    windows: AccountDetailQuotaWindow[];
    cooldown: QuotaCooldownInfo | null;
    resetCreditsAvailableCount: number | null;
    resetCreditExpiries: AccountDetailResetCreditExpiry[];
  };
  auth: {
    fields: AccountDetailField[];
  };
  strategy: {
    recommendation: AccountRecommendation | null;
    recommendationActionLabelKey: string;
    recommendationReasonKey: string;
    conclusion: AccountDetailDiagnosticConclusion;
    activity: AccountDetailDiagnosticsActivity;
    inspectionFields: AccountDetailField[];
    codexBadges: AccountDetailCodexBadge[];
    actionCandidates: AccountDetailActionCandidateSummary[];
  };
  value: AccountDetailValueSummary;
  history: AccountDetailHistorySummary | null;
}

export interface BuildAccountDetailViewModelOptions {
  t?: TFunction;
  recommendation?: AccountRecommendation | null;
  quotaCooldown?: QuotaCooldownInfo | null;
  codexStatus?: AuthFileCodexStatusSummary | null;
  quotaWindows?: AccountDetailQuotaWindowInput[];
  windowUsageByKey?: Map<string, MonitoringAccountWindowUsageItem>;
  actionCandidates?: AccountActionCandidate[];
  matchedActionCandidates?: AccountActionCandidate[];
  history?: MonitoringAccountHistoryItem | null;
  valueRow?: UsageValueRow | null;
  codexQuota?: CodexQuotaState | null;
  xaiQuota?: XaiQuotaState | null;
  diagnosticsSummary?: MonitoringAnalyticsSummary | null;
  diagnosticsRecentFailure?: MonitoringAnalyticsRecentFailure | null;
  diagnosticsEvents?: MonitoringAnalyticsEventRow[];
  diagnosticsTotalCount?: number | null;
}

const normalizeAuthIndexKey = (value: unknown): string => {
  if (value === undefined || value === null) return '-';
  const normalized = String(value).trim();
  return normalized || '-';
};

const field = (
  key: string,
  labelKey: string,
  value: string | number | null | undefined,
  valueKind: AccountDetailValueKind = 'text'
): AccountDetailField | null => {
  if (value === undefined || value === null || value === '') return null;
  return { key, labelKey, value, valueKind };
};

const booleanField = (
  key: string,
  labelKey: string,
  value: boolean | null | undefined
): AccountDetailField | null => {
  if (value === undefined || value === null) return null;
  return field(key, labelKey, value ? 'common.yes' : 'common.no', 'i18n');
};

const compactFields = (
  fields: Array<AccountDetailField | null | undefined>
): AccountDetailField[] =>
  fields.filter((item): item is AccountDetailField => item !== null && item !== undefined);

const isMatchingActionCandidate = (row: AccountRow, candidate: AccountActionCandidate): boolean =>
  accountOperationalItemMatchesRow(row, candidate);

const toActionCandidateSummary = (
  candidate: AccountActionCandidate
): AccountDetailActionCandidateSummary => ({
  id: candidate.id,
  actionType: candidate.actionType,
  status: candidate.status,
  reasonCode: candidate.reasonCode ?? '',
  reason: candidate.reason,
  hitCount: candidate.hitCount,
  firstSeenAtMs: candidate.firstSeenAtMs,
  lastSeenAtMs: candidate.lastSeenAtMs,
  updatedAtMs: candidate.updatedAtMs,
  accountSnapshot: candidate.accountSnapshot ?? '',
  authLabel: candidate.authLabel ?? '',
  hasEvidence: candidate.evidence !== undefined && candidate.evidence !== null,
});

const maxTimestamp = (values: Array<number | null | undefined>): number | null => {
  const timestamps = values.filter(
    (value): value is number => typeof value === 'number' && Number.isFinite(value) && value > 0
  );
  return timestamps.length > 0 ? Math.max(...timestamps) : null;
};

const buildRecentFailureSummary = (
  failure: MonitoringAnalyticsRecentFailure | null | undefined
): AccountDetailRecentFailureSummary | null => {
  if (!failure) return null;
  const reason =
    failure.fail_summary?.trim() ||
    [failure.header_error_kind, failure.header_error_code].filter(Boolean).join(' / ') ||
    (typeof failure.fail_status_code === 'number' ? `HTTP ${failure.fail_status_code}` : '');
  return {
    timestampMs: failure.timestamp_ms,
    reason,
    statusCode: failure.fail_status_code ?? null,
    model: failure.model || '',
  };
};

const toLatestRequestFromEvent = (
  event: MonitoringAnalyticsEventRow
): MonitoringAccountLatestRequest => ({
  timestamp_ms: event.timestamp_ms,
  failed: event.failed,
  fail_status_code: event.fail_status_code,
  fail_summary: event.fail_summary,
  header_error_kind: event.header_error_kind,
  header_error_code: event.header_error_code,
  header_trace_id: event.header_trace_id,
});

const toLatestRequestFromRecentFailure = (
  failure: MonitoringAnalyticsRecentFailure
): MonitoringAccountLatestRequest => ({
  timestamp_ms: failure.timestamp_ms,
  failed: true,
  fail_status_code: failure.fail_status_code,
  fail_summary: failure.fail_summary,
  header_error_kind: failure.header_error_kind,
  header_error_code: failure.header_error_code,
  header_trace_id: failure.header_trace_id,
});

const buildDetailRequestEvidenceInput = (
  history: MonitoringAccountHistoryItem | null | undefined,
  recentFailure: MonitoringAnalyticsRecentFailure | null | undefined,
  events: MonitoringAnalyticsEventRow[]
): AccountRequestEvidenceInput => {
  return mergeAccountRequestEvidenceInputs([
    {
      latestRequest: history?.latest_request ?? null,
      recentRequests: history?.recent_requests ?? [],
    },
    {
      recentRequests: events.map(toLatestRequestFromEvent),
    },
    {
      latestRequest: recentFailure ? toLatestRequestFromRecentFailure(recentFailure) : null,
    },
  ]);
};

const getLatestDetailActivityAtMs = (
  history: MonitoringAccountHistoryItem | null | undefined,
  recentFailure: MonitoringAnalyticsRecentFailure | null | undefined,
  events: MonitoringAnalyticsEventRow[]
): number | null =>
  maxTimestamp([
    history?.last_seen_ms,
    history?.latest_request?.timestamp_ms,
    ...(history?.recent_requests ?? []).map((request) => request.timestamp_ms),
    ...events.map((event) => event.timestamp_ms),
    recentFailure?.timestamp_ms,
  ]);

const buildDiagnosticsActivity = (
  row: AccountRow,
  summary: MonitoringAnalyticsSummary | null | undefined,
  recentFailure: MonitoringAnalyticsRecentFailure | null | undefined,
  totalCount: number | null | undefined,
  knownLatestActivityAtMs: number | null | undefined,
  requestEvidenceInput: AccountRequestEvidenceInput
): AccountDetailDiagnosticsActivity => {
  const requestRows = [
    ...(requestEvidenceInput.latestRequest ? [requestEvidenceInput.latestRequest] : []),
    ...(requestEvidenceInput.recentRequests ?? []),
  ];
  const latestSuccessAtMs = maxTimestamp(
    requestRows.filter((request) => !request.failed).map((request) => request.timestamp_ms)
  );
  const latestLoadedFailureAtMs = maxTimestamp(
    requestRows.filter((request) => request.failed).map((request) => request.timestamp_ms)
  );
  const recentFailureSummary = buildRecentFailureSummary(recentFailure);
  const latestFailureAtMs = maxTimestamp([
    latestLoadedFailureAtMs,
    recentFailureSummary?.timestampMs,
  ]);
  const latestActivityAtMs = maxTimestamp([
    ...requestRows.map((request) => request.timestamp_ms),
    recentFailureSummary?.timestampMs,
    knownLatestActivityAtMs,
  ]);
  const summaryTotalCalls = summary?.total_calls;
  const resolvedTotalCalls =
    typeof summaryTotalCalls === 'number'
      ? summaryTotalCalls
      : typeof totalCount === 'number'
        ? totalCount
        : null;
  const failureCalls = typeof summary?.failure_calls === 'number' ? summary.failure_calls : null;
  const failureRate =
    summary && summary.total_calls > 0
      ? (summary.failure_calls / summary.total_calls) * 100
      : summary && summary.total_calls === 0
        ? 0
        : null;
  const p95LatencyMs =
    typeof summary?.p95_latency_ms === 'number' && Number.isFinite(summary.p95_latency_ms)
      ? summary.p95_latency_ms
      : null;
  const resolvedRequestHealthEvidence = resolveAccountRequestHealthEvidence(requestEvidenceInput);
  const requestHealthEvidence = isAccountRequestHealthEvidenceCurrent(
    row,
    resolvedRequestHealthEvidence
  )
    ? resolvedRequestHealthEvidence
    : null;

  return {
    totalCalls: resolvedTotalCalls,
    failureCalls,
    failureRate,
    p95LatencyMs,
    latestActivityAtMs,
    latestSuccessAtMs,
    latestFailureAtMs,
    latestHealthDirection: requestHealthEvidence?.direction ?? null,
    latestHealthEvidenceAtMs: requestHealthEvidence?.request.timestamp_ms ?? null,
    recentFailure: recentFailureSummary,
  };
};

const toWindowUsageSummary = (
  item: MonitoringAccountWindowUsageItem | undefined
): AccountDetailWindowUsageSummary | null => {
  if (!item) return null;
  return {
    fromMs: item.from_ms,
    toMs: item.to_ms,
    matched: item.matched,
    totalRequests: item.total_requests,
    successCalls: item.success_calls,
    failureCalls: item.failure_calls,
    totalTokens: item.total_tokens,
    totalCost: item.total_cost,
    successRate: item.success_rate === null ? null : item.success_rate * 100,
    lastSeenMs: item.last_seen_ms,
    syncStatus: item.sync_status,
    scopeMatchStatus: item.scope_match_status ?? 'complete',
    unmatchedRequests: item.unmatched_requests ?? 0,
  };
};

const toHistorySummary = (
  item: MonitoringAccountHistoryItem | null | undefined
): AccountDetailHistorySummary | null => {
  if (!item) return null;
  return {
    matched: item.matched,
    totalRequests: item.total_requests,
    successCalls: item.success_calls,
    failureCalls: item.failure_calls,
    totalTokens: item.total_tokens,
    totalCost: item.total_cost,
    successRate: item.success_rate === null ? null : item.success_rate * 100,
    firstSeenMs: item.first_seen_ms,
    lastSeenMs: item.last_seen_ms,
    generatedAtMs: item.generated_at_ms ?? null,
    syncStatus: item.sync_status,
  };
};

const isValueRowForAccount = (row: AccountRow, valueRow: UsageValueRow): boolean => {
  if (valueRow.row) return valueRow.row.selectionKey === row.selectionKey;
  return valueRow.fileName === row.fileName && normalizeAuthIndexKey(row.authIndex) === '-';
};

const buildValueSummary = (
  row: AccountRow,
  valueRow: UsageValueRow | null | undefined
): AccountDetailValueSummary => {
  const matchedValue = valueRow && isValueRowForAccount(row, valueRow) ? valueRow : null;
  const monitoringValue = matchedValue?.source === 'monitoring' ? matchedValue : null;
  const requests = monitoringValue?.requests ?? row.usage.success + row.usage.failure;
  const inputTokens = monitoringValue?.inputTokens ?? 0;
  const outputTokens = monitoringValue?.outputTokens ?? 0;
  const totalTokens = monitoringValue?.totalTokens ?? inputTokens + outputTokens;
  return {
    requests,
    successRate: monitoringValue?.successRate ?? row.usage.successRate,
    inputTokens,
    outputTokens,
    totalTokens,
    estimatedCost: monitoringValue?.estimatedCost ?? null,
    lastSeenMs: monitoringValue?.lastSeenMs ?? null,
    source: monitoringValue ? 'monitoring' : 'recent',
  };
};

const FORECAST_COST_EPSILON = 1e-9;

const buildQuotaWindows = (
  row: AccountRow,
  quotaWindows: AccountDetailQuotaWindowInput[],
  windowUsageByKey: Map<string, MonitoringAccountWindowUsageItem>
): AccountDetailQuotaWindow[] =>
  quotaWindows.map((window) => {
    const resetAtMs = isValidQuotaResetAtMs(window.resetAtMs) ? window.resetAtMs : null;
    const providerWindowId = window.providerWindowId ?? window.key;
    const modelScope = window.modelScope
      ? {
          kind: window.modelScope.kind,
          key: window.modelScope.key,
          models: window.modelScope.models,
          complete: window.modelScope.complete,
        }
      : undefined;
    const scopeAllowsUsage = modelScope?.complete !== false;
    const currentUsage = toWindowUsageSummary(
      windowUsageByKey.get(
        accountWindowUsageRequestKey(row.selectionKey, providerWindowId, 'current', modelScope)
      )
    );
    const previousPeriod =
      window.windowMode === 'rolling'
        ? ('previous_equal_range' as const)
        : window.windowMode === 'fixed' || window.windowMode === 'calendar'
          ? ('previous' as const)
          : null;
    const previousUsage = previousPeriod
      ? toWindowUsageSummary(
          windowUsageByKey.get(
            accountWindowUsageRequestKey(
              row.selectionKey,
              providerWindowId,
              previousPeriod,
              modelScope
            )
          )
        )
      : null;
    const hasLifecycleEvidence =
      window.availability !== undefined ||
      window.currentCycle !== undefined ||
      window.previousCycle !== undefined;
    const lifecycleActive = window.availability === undefined || window.availability === 'active';
    const previousForecastEligible = window.previousCycle
      ? window.previousCycle.forecastEligible
      : !hasLifecycleEvidence;
    const currentForecastEligible = window.currentCycle
      ? window.currentCycle.forecastEligible
      : !hasLifecycleEvidence;
    const canForecastCurrentWindow =
      !hasLifecycleEvidence || (currentForecastEligible && window.stale !== true);
    const quotaProgressObservedAtMs =
      typeof window.quotaProgressObservedAtMs === 'number' &&
      Number.isFinite(window.quotaProgressObservedAtMs) &&
      window.quotaProgressObservedAtMs > 0
        ? window.quotaProgressObservedAtMs
        : null;
    const hasReliableQuotaProgress =
      typeof window.usedPercent === 'number' &&
      Number.isFinite(window.usedPercent) &&
      quotaProgressObservedAtMs !== null;
    const hasReliableCurrentUsage =
      currentUsage?.matched === true &&
      currentUsage.scopeMatchStatus === 'complete' &&
      currentForecastEligible;
    const currentUsageAheadOfQuotaProgressObservation =
      hasReliableCurrentUsage &&
      hasReliableQuotaProgress &&
      quotaProgressObservedAtMs !== null &&
      currentUsage.lastSeenMs !== null &&
      currentUsage.lastSeenMs > quotaProgressObservedAtMs;
    const currentForecastUsage =
      hasReliableCurrentUsage &&
      hasReliableQuotaProgress &&
      quotaProgressObservedAtMs !== null &&
      currentUsage.lastSeenMs !== null &&
      currentUsage.lastSeenMs <= quotaProgressObservedAtMs
        ? {
            requests: currentUsage.totalRequests,
            tokens: currentUsage.totalTokens,
            cost: currentUsage.totalCost,
          }
        : null;
    const forecast =
      scopeAllowsUsage &&
      lifecycleActive &&
      canForecastCurrentWindow &&
      (window.windowMode === 'fixed' || window.windowMode === 'calendar') &&
      typeof window.cycleStartMs === 'number' &&
      typeof window.cycleEndMs === 'number'
        ? estimateWindowUsage({
            usedPercent: window.usedPercent,
            current: currentForecastUsage,
            previous:
              !currentUsageAheadOfQuotaProgressObservation &&
              previousForecastEligible &&
              previousUsage?.matched === true &&
              previousUsage.scopeMatchStatus === 'complete'
                ? {
                    requests: previousUsage.totalRequests,
                    tokens: previousUsage.totalTokens,
                    cost: previousUsage.totalCost,
                  }
                : null,
          })
        : null;
    const trustedCurrentActual =
      currentUsage?.matched === true &&
      currentUsage.scopeMatchStatus === 'complete' &&
      Number.isFinite(currentUsage.totalRequests) &&
      currentUsage.totalRequests >= 0 &&
      Number.isFinite(currentUsage.totalTokens) &&
      currentUsage.totalTokens >= 0 &&
      Number.isFinite(currentUsage.totalCost) &&
      currentUsage.totalCost >= 0
        ? {
            requests: currentUsage.totalRequests,
            tokens: currentUsage.totalTokens,
            cost: currentUsage.totalCost,
          }
        : null;
    const forecastIsConsistentWithCurrentActual =
      forecast === null ||
      trustedCurrentActual === null ||
      (forecast.requests >= trustedCurrentActual.requests &&
        forecast.tokens >= trustedCurrentActual.tokens &&
        forecast.cost + FORECAST_COST_EPSILON >= trustedCurrentActual.cost);
    return {
      ...window,
      providerWindowId,
      resetAtMs,
      resetAccuracy: resetAtMs !== null ? (window.resetAccuracy ?? 'unknown') : 'unknown',
      usage: currentUsage,
      currentUsage,
      previousUsage,
      previousPeriod,
      forecast: forecastIsConsistentWithCurrentActual ? forecast : null,
    };
  });

const buildQuotaDiagnostics = (
  row: AccountRow,
  xaiQuota?: XaiQuotaState | null
): AccountDetailField[] => {
  const xaiBilling = xaiQuota?.billing;
  const quotaErrorGuidance =
    row.quota.errorStatus === 404
      ? 'common.quota_update_required'
      : row.quota.errorStatus === 403
        ? 'common.quota_check_credential'
        : null;
  return compactFields([
    field('fetchedAtMs', 'accounts.detail_fetched_at', row.quota.fetchedAtMs, 'timestamp'),
    field('observedAtMs', 'accounts.detail_observed_at', row.quota.observedAtMs, 'timestamp'),
    field('trace', 'accounts.detail_header_trace', row.quota.observedTraceId),
    field('errorKind', 'accounts.detail_header_error_kind', row.quota.observedErrorKind),
    field('errorCode', 'accounts.detail_header_error_code', row.quota.observedErrorCode),
    field('activeLimit', 'accounts.detail_active_limit', row.quota.activeLimit),
    field('creditsBalance', 'accounts.detail_credits_balance', row.quota.creditsBalance),
    booleanField(
      'creditsHasCredits',
      'accounts.detail_credits_available',
      row.quota.creditsHasCredits
    ),
    booleanField(
      'creditsUnlimited',
      'accounts.detail_credits_unlimited',
      row.quota.creditsUnlimited
    ),
    booleanField(
      'creditsOverageLimitReached',
      'accounts.detail_credits_overage_reached',
      row.quota.creditsOverageLimitReached
    ),
    field(
      'creditsApproxLocalMessages',
      'accounts.detail_credits_approx_local_messages',
      row.quota.creditsApproxLocalMessages,
      'number'
    ),
    field(
      'creditsApproxCloudMessages',
      'accounts.detail_credits_approx_cloud_messages',
      row.quota.creditsApproxCloudMessages,
      'number'
    ),
    booleanField(
      'spendControlReached',
      'accounts.detail_spend_control_reached',
      row.quota.spendControlReached
    ),
    field(
      'spendControlIndividualLimit',
      'accounts.detail_spend_control_individual_limit',
      row.quota.spendControlIndividualLimit,
      'number'
    ),
    field(
      'rateLimitReachedType',
      'accounts.detail_rate_limit_reached_type',
      row.quota.rateLimitReachedType
    ),
    field(
      'primaryOverSecondary',
      'accounts.detail_primary_over_secondary',
      row.quota.primaryOverSecondaryLimitPercent,
      'percent'
    ),
    field('error', 'common.error', row.quota.error),
    field('errorGuidance', 'accounts.detail_quota_error_guidance', quotaErrorGuidance, 'i18n'),
    field(
      'xaiOfficialApiHealth',
      'xai_quota.official_api_plan',
      xaiBilling?.officialApiHealth ? 'xai_quota.official_api_health' : null,
      'i18n'
    ),
    booleanField('xaiPartial', 'accounts.detail_xai_partial', xaiBilling?.partial),
    ...(xaiBilling?.diagnostics ?? []).map((diagnostic, index) =>
      field(
        `xaiDiagnostic-${index}`,
        'accounts.detail_xai_diagnostic',
        [diagnostic.statusCode ? `HTTP ${diagnostic.statusCode}` : '', diagnostic.message]
          .filter(Boolean)
          .join(' · ')
      )
    ),
  ]);
};

const quotaSourceLabelKey = (source: AccountRow['quota']['source']) => {
  switch (source) {
    case 'observed-header':
      return 'accounts.quota_source_observed_header';
    case 'cache':
      return 'accounts.quota_source_cache';
    case 'none':
    default:
      return 'accounts.quota_source_none';
  }
};

const presentOverviewText = (value: string | null | undefined): string | null => {
  const normalized = value?.trim() ?? '';
  return normalized && normalized !== '-' ? normalized : null;
};

const getOverviewHealthLimitKind = (
  status: AccountListHealthStatusKey
): Extract<AccountQuotaWindowKind, 'five_hour' | 'weekly' | 'monthly'> | null => {
  if (status === 'five_hour_exhausted' || status === 'five_hour_cooldown') return 'five_hour';
  if (status === 'weekly_exhausted' || status === 'weekly_cooldown') return 'weekly';
  if (status === 'monthly_exhausted' || status === 'monthly_cooldown') return 'monthly';
  return null;
};

const selectConservativeBlockingWindow = (
  windows: AccountDetailQuotaWindowInput[]
): AccountDetailQuotaWindowInput => {
  const unknownResetWindow = windows.find((window) => !isValidQuotaResetAtMs(window.resetAtMs));
  if (unknownResetWindow) return unknownResetWindow;
  return windows.reduce((current, next) =>
    (next.resetAtMs ?? 0) > (current.resetAtMs ?? 0) ? next : current
  );
};

const selectOverviewQuotaWindow = (
  quotaWindows: AccountDetailQuotaWindowInput[],
  healthStatus: AccountListHealthStatusKey
) => {
  if (quotaWindows.length === 0) return null;
  const windowsWithRemaining = quotaWindows.filter(
    (window) =>
      typeof window.remainingPercent === 'number' && Number.isFinite(window.remainingPercent)
  );
  if (windowsWithRemaining.length === 0) return quotaWindows[0];
  const healthLimitKind = getOverviewHealthLimitKind(healthStatus);
  if (healthLimitKind) {
    const matchingBlockingWindows = windowsWithRemaining.filter(
      (window) =>
        (window.remainingPercent ?? 100) <= 0 &&
        (window.kind ??
          inferAccountQuotaWindowKind({
            key: window.key,
            label: window.label,
            limitWindowSeconds: window.limitWindowSeconds,
          })) === healthLimitKind
    );
    if (matchingBlockingWindows.length > 0) {
      return selectConservativeBlockingWindow(matchingBlockingWindows);
    }
  }

  const minimumRemaining = windowsWithRemaining.reduce((current, next) =>
    (next.remainingPercent ?? 100) < (current.remainingPercent ?? 100) ? next : current
  ).remainingPercent;
  const limitingWindows = windowsWithRemaining.filter(
    (window) => window.remainingPercent === minimumRemaining
  );
  return (minimumRemaining ?? 100) <= 0 && limitingWindows.length > 1
    ? selectConservativeBlockingWindow(limitingWindows)
    : limitingWindows[0];
};

const getQuotaObservedAtMs = (row: AccountRow): number | null => {
  const failedAtMs = maxTimestamp([row.quota.failedAtMs]);
  if (row.quota.status === 'error' && failedAtMs !== null) return failedAtMs;
  return row.quota.source === 'observed-header'
    ? (row.quota.observedAtMs ?? row.quota.fetchedAtMs ?? null)
    : (row.quota.fetchedAtMs ?? row.quota.observedAtMs ?? null);
};

const getObservedHeaderAtMs = (row: AccountRow): number | null => row.quota.observedAtMs ?? null;

const resolveOverviewDecisionBasis = (
  row: AccountRow,
  listItem: AccountListPresentationItem,
  quotaCooldown: QuotaCooldownInfo | null
): { labelKey: string; observedAtMs: number | null } => {
  if (listItem.health.basisLabelKey) {
    return {
      labelKey: listItem.health.basisLabelKey,
      observedAtMs: listItem.health.observedAtMs,
    };
  }

  if (
    quotaCooldown &&
    (listItem.health.reasonKey === 'accounts.health_reason_cooldown' ||
      listItem.health.reasonKey === 'accounts.health_reason_limited_cooldown')
  ) {
    return {
      labelKey: 'accounts.detail_overview_basis_cooldown',
      observedAtMs: quotaCooldown.disabledAtMs ?? quotaCooldown.createdAtMs ?? null,
    };
  }

  if (listItem.health.status === 'reauth') {
    if (listItem.health.reasonKey === 'accounts.health_reason_reauth_quota_refresh') {
      return {
        labelKey: quotaSourceLabelKey(row.quota.source),
        observedAtMs: getQuotaObservedAtMs(row),
      };
    }
    if (listItem.health.reasonKey === 'accounts.health_reason_reauth_inspection') {
      return {
        labelKey: 'accounts.detail_overview_basis_inspection',
        observedAtMs: row.inspection?.createdAtMs ?? null,
      };
    }
    return {
      labelKey: 'accounts.detail_overview_basis_credential_state',
      observedAtMs: row.updatedAtMs,
    };
  }

  if (listItem.health.status === 'exception') {
    if (listItem.health.reasonKey === 'accounts.health_reason_exception_quota') {
      return {
        labelKey: quotaSourceLabelKey(row.quota.source),
        observedAtMs: getQuotaObservedAtMs(row),
      };
    }
    if (listItem.health.reasonKey === 'accounts.health_reason_exception_header') {
      return {
        labelKey: 'accounts.quota_source_observed_header',
        observedAtMs: getObservedHeaderAtMs(row),
      };
    }
    if (listItem.health.reasonKey === 'accounts.health_reason_exception_inspection') {
      return {
        labelKey: 'accounts.detail_overview_basis_inspection',
        observedAtMs: row.inspection?.createdAtMs ?? null,
      };
    }
    return {
      labelKey: 'accounts.detail_overview_basis_credential_state',
      observedAtMs: row.updatedAtMs,
    };
  }

  if (listItem.health.reasonKey === 'accounts.health_reason_limited_header') {
    return {
      labelKey: 'accounts.quota_source_observed_header',
      observedAtMs: getObservedHeaderAtMs(row),
    };
  }

  if (listItem.health.status === 'disabled' || listItem.health.status === 'raw') {
    return {
      labelKey: 'accounts.detail_overview_basis_credential_state',
      observedAtMs: row.updatedAtMs,
    };
  }

  if (
    row.quota.source !== 'none' ||
    row.quota.remainingPercent !== null ||
    row.quota.status === 'loading'
  ) {
    return {
      labelKey: quotaSourceLabelKey(row.quota.source),
      observedAtMs: getQuotaObservedAtMs(row),
    };
  }

  if (row.inspection) {
    return {
      labelKey: 'accounts.detail_overview_basis_inspection',
      observedAtMs: row.inspection.createdAtMs,
    };
  }

  return {
    labelKey: 'accounts.detail_overview_basis_credential_state',
    observedAtMs: row.updatedAtMs,
  };
};

const buildOverviewDecision = (
  row: AccountRow,
  listItem: AccountListPresentationItem,
  quotaCooldown: QuotaCooldownInfo | null
): AccountDetailOverviewDecision => {
  const basis = resolveOverviewDecisionBasis(row, listItem, quotaCooldown);

  return {
    status: listItem.health.status,
    labelKey: listItem.health.labelKey,
    reasonKey: listItem.health.reasonKey,
    reasonParams: listItem.health.reasonParams,
    observedAtMs: basis.observedAtMs,
    basisLabelKey: basis.labelKey,
    targetTab: 'diagnostics',
  };
};

const buildOverviewCapacity = (
  row: AccountRow,
  quotaWindows: AccountDetailQuotaWindowInput[],
  listItem: AccountListPresentationItem
): AccountDetailOverviewCapacity => {
  const groupedAvailability =
    row.provider === 'antigravity'
      ? summarizeGroupedQuotaAvailability(
          quotaWindows.map((window) => ({
            groupLabel: window.groupLabel,
            kind: window.kind,
            remainingPercent: window.remainingPercent,
            resetLabel: window.resetLabel,
            resetAtMs: window.resetAtMs,
            resetAccuracy: window.resetAccuracy,
          }))
        )
      : null;
  const observedAtMs = getQuotaObservedAtMs(row);

  if (groupedAvailability) {
    const limitedGroups = groupedAvailability.groups
      .filter((group) => group.remainingPercent <= 0)
      .map((group) => group.label)
      .join(' · ');
    const resetValue =
      groupedAvailability.resetAtMs ?? presentOverviewText(groupedAvailability.resetLabel);
    return {
      kind: 'group_availability',
      statusLabelKey:
        groupedAvailability.state === 'partial'
          ? 'accounts.health_partial'
          : groupedAvailability.state === 'exhausted'
            ? 'accounts.quota_status_exhausted'
            : 'accounts.quota_status_ok',
      remainingPercent: row.quota.remainingPercent,
      availableGroupCount: groupedAvailability.availableGroupCount,
      totalGroupCount: groupedAvailability.totalGroupCount,
      hasData: true,
      descriptionKey: 'accounts.detail_overview_capacity_group_desc',
      fields: compactFields([
        field(
          'overviewLimitedGroups',
          'accounts.detail_overview_capacity_limited_groups',
          presentOverviewText(limitedGroups)
        ),
        field(
          'overviewGroupRecovery',
          groupedAvailability.state === 'available'
            ? 'accounts.detail_reset'
            : 'accounts.detail_overview_capacity_recovery',
          resetValue,
          typeof resetValue === 'number' ? 'quota_reset' : 'text'
        ),
        field('source', 'accounts.detail_source', quotaSourceLabelKey(row.quota.source), 'i18n'),
        field(
          'overviewQuotaObservedAt',
          'accounts.detail_overview_capacity_checked',
          observedAtMs,
          'timestamp'
        ),
      ]),
      targetTab: 'quota',
    };
  }

  const quotaWindow = selectOverviewQuotaWindow(quotaWindows, listItem.health.status);
  const resetValue = quotaWindow
    ? (quotaWindow.resetAtMs ?? presentOverviewText(quotaWindow.resetLabel))
    : (row.quota.resetAtMs ?? presentOverviewText(row.quota.resetLabel));
  const hasQuotaData =
    (typeof row.quota.remainingPercent === 'number' &&
      Number.isFinite(row.quota.remainingPercent)) ||
    quotaWindow !== null;

  return {
    kind: 'percent',
    statusLabelKey: listItem.quota.statusLabelKey,
    remainingPercent: row.quota.remainingPercent,
    availableGroupCount: null,
    totalGroupCount: null,
    hasData: hasQuotaData,
    descriptionKey: hasQuotaData
      ? 'accounts.detail_overview_capacity_desc'
      : 'accounts.detail_overview_capacity_missing_desc',
    fields: compactFields([
      field(
        'overviewQuotaWindow',
        'accounts.detail_overview_capacity_window',
        presentOverviewText(quotaWindow?.label)
      ),
      field(
        'overviewQuotaAmount',
        'accounts.detail_overview_capacity_amount',
        presentOverviewText(quotaWindow?.amountLabel)
      ),
      field(
        'reset',
        'accounts.detail_reset',
        resetValue,
        typeof resetValue === 'number' ? 'quota_reset' : 'text'
      ),
      field('source', 'accounts.detail_source', quotaSourceLabelKey(row.quota.source), 'i18n'),
      field(
        'overviewQuotaObservedAt',
        'accounts.detail_overview_capacity_checked',
        observedAtMs,
        'timestamp'
      ),
    ]),
    targetTab: 'quota',
  };
};

const buildOverviewCredential = (
  row: AccountRow,
  codexQuota: CodexQuotaState | null | undefined,
  t?: TFunction
): AccountDetailOverviewCredential => {
  const parseValidSubscriptionUntilMs = (value: unknown): number | null => {
    const numeric =
      typeof value === 'number'
        ? value
        : typeof value === 'string' && /^\d+(?:\.\d+)?$/.test(value.trim())
          ? Number(value.trim())
          : null;
    const parsed =
      numeric !== null && Number.isFinite(numeric)
        ? numeric < 1e12
          ? numeric * 1000
          : numeric
        : parseTimestampMs(value);
    if (!Number.isFinite(parsed) || parsed <= 0) return null;
    return Number.isNaN(new Date(parsed).getTime()) ? null : parsed;
  };
  const effectivePlanType = normalizeStringValue(
    codexQuota?.planType ?? row.planType ?? resolveAuthFilePlanType(row.raw)
  );
  const planPresentation = getPlanPresentation({
    provider: row.provider,
    planType: effectivePlanType,
    t,
  });
  const hasPaidCodexSubscription =
    row.provider === 'codex' &&
    effectivePlanType !== null &&
    planPresentation?.canonicalPlanType !== 'free';
  const liveSubscriptionUntilMs = hasPaidCodexSubscription
    ? parseValidSubscriptionUntilMs(codexQuota?.subscriptionActiveUntil)
    : null;
  const asRecord = (value: unknown): Record<string, unknown> | null =>
    value && typeof value === 'object' && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : null;
  const metadata = asRecord(row.raw.metadata);
  const attributes = asRecord(row.raw.attributes);
  const tokenSubscriptionUntilMs = hasPaidCodexSubscription
    ? [row.raw.id_token, metadata?.id_token, attributes?.id_token].reduce<number | null>(
        (resolved, candidate) => {
          if (resolved !== null) return resolved;
          const payload = parseIdTokenPayload(candidate);
          return parseValidSubscriptionUntilMs(
            payload?.chatgpt_subscription_active_until ?? payload?.chatgptSubscriptionActiveUntil
          );
        },
        null
      )
    : null;
  const subscriptionUntilMs = liveSubscriptionUntilMs ?? tokenSubscriptionUntilMs;
  const subscriptionUntilLabelKey =
    liveSubscriptionUntilMs !== null
      ? 'accounts.detail_subscription_until'
      : 'accounts.detail_subscription_until_token';

  return {
    statusLabelKey: row.disabled
      ? 'accounts.detail_auth_status_disabled'
      : 'accounts.detail_auth_status_enabled',
    sourceLabelKey: row.runtimeOnly
      ? 'accounts.detail_runtime_only'
      : 'accounts.detail_local_auth_file',
    fields: compactFields([
      field('provider', 'accounts.col_provider', row.provider),
      field('planType', 'accounts.col_plan', planPresentation?.fullLabel ?? effectivePlanType),
      field('updatedAtMs', 'accounts.detail_updated_at', row.updatedAtMs, 'timestamp'),
      field('subscriptionUntilMs', subscriptionUntilLabelKey, subscriptionUntilMs, 'quota_reset'),
      field('authIndex', 'accounts.detail_auth_index', presentOverviewText(row.authIndex)),
      field('priority', 'accounts.col_priority', row.priority ?? 0, 'number'),
    ]),
    targetTab: 'config',
  };
};

const buildOverviewActivity = (
  row: AccountRow,
  value: AccountDetailValueSummary
): AccountDetailOverviewActivity => {
  const monitoring = value.source === 'monitoring';
  const recentRequests = row.usage.success + row.usage.failure;
  return {
    scope: monitoring ? 'monitoring_7d' : 'recent_snapshot',
    scopeDays: monitoring ? ACCOUNT_OVERVIEW_ACTIVITY_RANGE_DAYS : null,
    sourceLabelKey: `accounts.value_source_${value.source}`,
    hasActivity: value.requests > 0,
    emptyStateKey: monitoring
      ? 'accounts.detail_overview_activity_empty_7d'
      : 'accounts.detail_overview_activity_empty_recent',
    metrics: monitoring
      ? compactFields([
          field('requests', 'accounts.value_requests', value.requests, 'number'),
          field('successRate', 'accounts.detail_success_rate', value.successRate, 'percent'),
          field('tokens', 'usage_analytics.trend_metric_totalTokens', value.totalTokens, 'number'),
          field('cost', 'accounts.history_cost', value.estimatedCost, 'money'),
          field(
            'lastSeenMs',
            'accounts.detail_overview_activity_last_active',
            value.lastSeenMs,
            'timestamp'
          ),
        ])
      : compactFields([
          field('requests', 'accounts.value_requests', recentRequests, 'number'),
          field('successRate', 'accounts.detail_success_rate', row.usage.successRate, 'percent'),
          field(
            'successCalls',
            'accounts.detail_overview_activity_success',
            row.usage.success,
            'number'
          ),
          field(
            'failureCalls',
            'accounts.detail_overview_activity_failure',
            row.usage.failure,
            'number'
          ),
        ]),
    targetTab: 'diagnostics',
  };
};

const buildOverviewRecentStatus = (
  row: AccountRow,
  _decision: AccountDetailOverviewDecision,
  requestEvidence: ReturnType<typeof resolveAccountRequestHealthEvidence>
): AccountDetailOverviewRecentStatus => {
  const recentRequests = row.usage.recentRequests;
  const totals = sumRecentRequests(recentRequests);
  const total = totals.success + totals.failure;
  const credentialStatusKind = classifyAccountCredentialStatusEvidence(row);
  const hasProblemStatusMessage =
    credentialStatusKind === 'quota' ||
    (credentialStatusKind === 'credential_failure'
      ? isAccountCredentialStatusProblemCurrent(row, requestEvidence)
      : credentialStatusKind === 'transient_failure');

  return {
    success: totals.success,
    failure: totals.failure,
    successRate: total > 0 ? (totals.success / total) * 100 : null,
    recentRequests,
    statusMessage: hasProblemStatusMessage ? row.statusMessage : '',
  };
};

const getOverviewAttentionTarget = (
  action: AccountRecommendation['action']
): AccountDetailOverviewTargetTab => {
  if (action === 'refresh') return 'quota';
  if (action === 'reauth' || action === 'review') return 'diagnostics';
  return 'config';
};

const buildOverviewAttention = (
  recommendation: AccountRecommendation | null,
  actionCandidates: AccountDetailActionCandidateSummary[]
): AccountDetailOverviewAttention | null => {
  if (recommendation) {
    return {
      priority: recommendation.priority,
      actionLabelKey: getRecommendationActionLabelKey(recommendation.action),
      reasonKey: recommendation.reasonKey,
      targetTab: getOverviewAttentionTarget(recommendation.action),
    };
  }
  if (actionCandidates.length > 0) {
    return {
      priority: 'medium',
      actionLabelKey: 'accounts.detail_overview_attention_review',
      reasonKey: 'accounts.detail_overview_attention_candidates',
      reasonParams: { count: actionCandidates.length },
      targetTab: 'diagnostics',
    };
  }
  return null;
};

type DiagnosticEvidenceDirection = AccountRequestEvidenceDirection | null;

const getDiagnosticEvidenceDirection = (action: string): DiagnosticEvidenceDirection => {
  const normalized = action.trim().toLowerCase();
  if (!normalized) return null;
  return normalized === 'keep' || normalized === 'enable' ? 'positive' : 'negative';
};

const getInspectionDiagnosticEvidenceDirection = (row: AccountRow): DiagnosticEvidenceDirection => {
  if (!row.inspection) return null;
  if (isAccountInspectionAuthenticationFailure(row)) return 'negative';
  if (row.inspection.isQuota === true || isAccountInspectionHealthyEvidence(row)) {
    return 'positive';
  }
  return getDiagnosticEvidenceDirection(row.inspection.action);
};

const getDiagnosticEvidenceStatusLabelKey = (status: AccountDetailDiagnosticEvidenceStatus) =>
  `accounts.detail_diagnostic_evidence_${status}`;

const buildDiagnosticConclusion = (
  row: AccountRow,
  recommendation: AccountRecommendation | null,
  actionCandidates: AccountDetailActionCandidateSummary[],
  overviewDecision: AccountDetailOverviewDecision,
  activity: AccountDetailDiagnosticsActivity,
  requestEvidenceInput: AccountRequestEvidenceInput
): AccountDetailDiagnosticConclusion => {
  const candidate = actionCandidates[0] ?? null;
  let actionLabelKey = recommendation
    ? getRecommendationActionLabelKey(recommendation.action)
    : 'accounts.recommend_normal';
  let reasonKey = recommendation?.reasonKey ?? 'accounts.recommend_normal_desc';
  let priority = recommendation?.priority ?? null;
  let sourceLabelKey = overviewDecision.basisLabelKey;
  let observedAtMs = overviewDecision.observedAtMs;
  let direction: DiagnosticEvidenceDirection = null;
  let latestComparableDirection = activity.latestHealthDirection;
  let latestComparableEvidenceAtMs = activity.latestHealthEvidenceAtMs;
  const requestAuthRecommendation =
    recommendation?.reasonKey === 'accounts.recommend_reason_request_auth';
  const requestFailureRecommendation =
    recommendation?.reasonKey === 'accounts.recommend_reason_request_failure';
  const resolvedRequestEvidence = resolveAccountRequestHealthEvidence(requestEvidenceInput);
  const requestCredentialEvidence = isAccountRequestCredentialEvidenceCurrent(
    row,
    resolvedRequestEvidence
  )
    ? getAccountRequestCredentialEvidence(resolvedRequestEvidence)
    : null;
  const requestIsNewerThanInspection =
    activity.latestHealthEvidenceAtMs !== null &&
    (!row.inspection || activity.latestHealthEvidenceAtMs > row.inspection.createdAtMs);

  if (requestAuthRecommendation) {
    sourceLabelKey = 'accounts.latest_request_time_title';
    observedAtMs = requestCredentialEvidence?.request.timestamp_ms ?? overviewDecision.observedAtMs;
    direction = requestCredentialEvidence?.direction ?? 'negative';
    latestComparableDirection = requestCredentialEvidence?.direction ?? null;
    latestComparableEvidenceAtMs = requestCredentialEvidence?.request.timestamp_ms ?? null;
  } else if (requestFailureRecommendation) {
    sourceLabelKey = 'accounts.latest_request_time_title';
    observedAtMs = activity.latestHealthEvidenceAtMs;
    direction = activity.latestHealthDirection;
  } else if (
    recommendation?.reasonKey === 'accounts.recommend_reason_inspection' &&
    row.inspection
  ) {
    sourceLabelKey = `accounts.inspection_source_${row.inspection.source}`;
    observedAtMs = row.inspection.createdAtMs;
    direction = getInspectionDiagnosticEvidenceDirection(row);
  } else if (!recommendation && candidate) {
    actionLabelKey = `accounts.action_type_${candidate.actionType}`;
    reasonKey = 'accounts.detail_diagnostic_candidate_reason';
    priority = 'medium';
    sourceLabelKey = 'accounts.detail_action_candidates';
    observedAtMs = candidate.lastSeenAtMs;
    direction = getDiagnosticEvidenceDirection(candidate.actionType);
  } else if (!recommendation && requestIsNewerThanInspection) {
    sourceLabelKey = 'accounts.latest_request_time_title';
    observedAtMs = activity.latestHealthEvidenceAtMs;
    direction = activity.latestHealthDirection;
  } else if (
    !recommendation &&
    row.inspection &&
    overviewDecision.basisLabelKey === 'accounts.detail_overview_basis_inspection'
  ) {
    sourceLabelKey = `accounts.inspection_source_${row.inspection.source}`;
    observedAtMs = row.inspection.createdAtMs;
    direction = getInspectionDiagnosticEvidenceDirection(row);
  }

  let evidenceStatus: AccountDetailDiagnosticEvidenceStatus = 'current';
  if (
    direction !== null &&
    observedAtMs !== null &&
    latestComparableEvidenceAtMs !== null &&
    latestComparableEvidenceAtMs > observedAtMs
  ) {
    evidenceStatus = 'outdated';
  }

  const conflictsWithLatestHealthEvidence =
    direction !== null &&
    latestComparableDirection !== null &&
    direction !== latestComparableDirection &&
    observedAtMs !== null &&
    latestComparableEvidenceAtMs !== null &&
    latestComparableEvidenceAtMs > observedAtMs;

  if (conflictsWithLatestHealthEvidence) {
    evidenceStatus = 'conflict';
    actionLabelKey = 'accounts.detail_diagnostic_reinspect';
    reasonKey = 'accounts.detail_diagnostic_conflict_desc';
    priority = 'medium';
  }

  return {
    actionLabelKey,
    reasonKey,
    priority,
    sourceLabelKey,
    observedAtMs,
    evidenceStatus,
    evidenceStatusLabelKey: getDiagnosticEvidenceStatusLabelKey(evidenceStatus),
    latestActivityAtMs: activity.latestActivityAtMs,
  };
};

const buildQuotaFields = (row: AccountRow, listItem: AccountListPresentationItem) =>
  compactFields([
    field('status', 'accounts.detail_status', listItem.quota.statusLabelKey, 'i18n'),
    field('used', 'accounts.detail_used', row.quota.usedPercent, 'percent'),
    field('remaining', 'accounts.detail_quota', row.quota.remainingPercent, 'percent'),
    field(
      'reset',
      'accounts.detail_reset',
      row.quota.resetAtMs ?? row.quota.resetLabel,
      row.quota.resetAtMs === null ? 'text' : 'quota_reset'
    ),
    field('source', 'accounts.detail_source', quotaSourceLabelKey(row.quota.source), 'i18n'),
  ]);

const buildAuthFields = (row: AccountRow): AccountDetailField[] =>
  compactFields([
    field('authIndex', 'accounts.detail_auth_index', row.authIndex),
    field('projectId', 'accounts.detail_project_id', row.projectId),
    field(
      'runtime',
      'accounts.detail_runtime_source',
      row.runtimeOnly ? 'accounts.detail_runtime_only' : 'accounts.detail_local_auth_file',
      'i18n'
    ),
  ]);

const buildInspectionFields = (row: AccountRow): AccountDetailField[] => {
  if (!row.inspection) return [];
  return compactFields([
    field(
      'source',
      'accounts.detail_inspection_source',
      `accounts.inspection_source_${row.inspection.source}`,
      'i18n'
    ),
    field('action', 'common.action', `accounts.action_${row.inspection.action}`, 'i18n'),
    field('httpStatus', 'accounts.detail_http_status', row.inspection.statusCode ?? '-'),
    field(
      'reason',
      'accounts.detail_reason',
      row.inspection.actionReason || '-',
      row.inspection.actionReason.startsWith('monitoring.') ? 'i18n' : 'text'
    ),
    ...(row.inspection.action === 'keep'
      ? []
      : [
          field(
            'actionStatus',
            'accounts.detail_action_status',
            row.inspection.actionStatus || '-'
          ),
        ]),
    field('usedPercent', 'accounts.detail_used', row.inspection.usedPercent, 'percent'),
    field('createdAtMs', 'accounts.detail_observed_at', row.inspection.createdAtMs, 'timestamp'),
  ]);
};

export const buildAccountDetailViewModel = (
  row: AccountRow,
  options: BuildAccountDetailViewModelOptions = {}
): AccountDetailViewModel => {
  const diagnosticsEvents = options.diagnosticsEvents ?? [];
  const requestEvidence = buildDetailRequestEvidenceInput(
    options.history,
    options.diagnosticsRecentFailure,
    diagnosticsEvents
  );
  const computedRecommendation = buildAccountRecommendation(row, requestEvidence);
  const providedRecommendation = options.recommendation ?? null;
  const providedRecommendationIsEvidenceSensitive =
    isAccountRecommendationEvidenceSensitive(providedRecommendation);
  const recommendation =
    computedRecommendation ??
    (providedRecommendationIsEvidenceSensitive ? null : providedRecommendation);
  const quotaCooldown = options.quotaCooldown ?? null;
  const quotaWindows: AccountDetailQuotaWindowInput[] = (options.quotaWindows ?? []).map(
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
  const listItem = buildAccountListItem(row, {
    t: options.t,
    recommendation,
    quotaCooldown,
    codexStatus: options.codexStatus ?? null,
    quotaWindows: accountQuotaWindows,
    requestEvidence,
  });
  const value = buildValueSummary(row, options.valueRow);
  const rawHistory = toHistorySummary(options.history);
  const history = rawHistory?.matched === true ? rawHistory : null;
  const actionCandidates = [
    ...(options.matchedActionCandidates ??
      (options.actionCandidates ?? []).filter((candidate) =>
        isMatchingActionCandidate(row, candidate)
      )),
  ]
    .sort((left, right) => right.lastSeenAtMs - left.lastSeenAtMs)
    .map(toActionCandidateSummary);
  const diagnosticsActivity = buildDiagnosticsActivity(
    row,
    options.diagnosticsSummary,
    options.diagnosticsRecentFailure,
    options.diagnosticsTotalCount,
    maxTimestamp([
      value.lastSeenMs,
      getLatestDetailActivityAtMs(
        options.history,
        options.diagnosticsRecentFailure,
        diagnosticsEvents
      ),
    ]),
    requestEvidence
  );
  const overviewDecision = buildOverviewDecision(row, listItem, quotaCooldown);
  const overview = {
    decision: overviewDecision,
    capacity: buildOverviewCapacity(row, accountQuotaWindows, listItem),
    credential: buildOverviewCredential(row, options.codexQuota, options.t),
    recentStatus: buildOverviewRecentStatus(
      row,
      overviewDecision,
      resolveAccountRequestHealthEvidence(requestEvidence)
    ),
    activity: buildOverviewActivity(row, value),
    attention: buildOverviewAttention(recommendation, actionCandidates),
  };
  const diagnosticConclusion = buildDiagnosticConclusion(
    row,
    recommendation,
    actionCandidates,
    overview.decision,
    diagnosticsActivity,
    requestEvidence
  );

  return {
    selectionKey: row.selectionKey,
    identity: {
      title: row.accountLabel,
      subtitle: [row.fileName, row.authIndex ? `#${row.authIndex}` : '', row.projectId || '']
        .filter(Boolean)
        .join(' · '),
      fileName: row.fileName,
      accountLabel: row.accountLabel,
      provider: row.provider,
      planType: row.planType,
      planPresentation: getPlanPresentation({
        provider: row.provider,
        planType: options.codexQuota?.planType ?? row.planType ?? resolveAuthFilePlanType(row.raw),
        t: options.t,
      }),
      authIndex: row.authIndex,
      projectId: row.projectId,
      priority: row.priority ?? 0,
      disabled: row.disabled,
      runtimeOnly: row.runtimeOnly,
    },
    health: {
      status: listItem.health.status,
      labelKey: listItem.health.labelKey,
      tooltipKey: listItem.health.tooltipKey,
      tooltipParams: listItem.health.tooltipParams,
      reasonKey: listItem.health.reasonKey,
      reasonParams: listItem.health.reasonParams,
      resetAtMs: listItem.health.resetAtMs,
    },
    overview,
    quota: {
      statusLabelKey: listItem.quota.statusLabelKey,
      sourceShortLabelKey: listItem.quota.sourceShortLabelKey,
      fields: buildQuotaFields(row, listItem),
      diagnostics: buildQuotaDiagnostics(row, options.xaiQuota),
      windows: buildQuotaWindows(row, quotaWindows, options.windowUsageByKey ?? new Map()),
      cooldown: quotaCooldown,
      resetCreditsAvailableCount: options.codexQuota?.rateLimitResetCreditsAvailableCount ?? null,
      resetCreditExpiries: getSortedCodexResetCreditExpiries(
        options.codexQuota?.rateLimitResetCredits
      ).map((item) => ({ id: item.id, expiresAtMs: item.expiresAtMs })),
    },
    auth: {
      fields: buildAuthFields(row),
    },
    strategy: {
      recommendation,
      recommendationActionLabelKey: recommendation
        ? getRecommendationActionLabelKey(recommendation.action)
        : 'accounts.recommend_normal',
      recommendationReasonKey: recommendation?.reasonKey ?? 'accounts.recommend_normal_desc',
      conclusion: diagnosticConclusion,
      activity: diagnosticsActivity,
      inspectionFields: buildInspectionFields(row),
      codexBadges: options.codexStatus?.badges ?? [],
      actionCandidates,
    },
    value,
    history,
  };
};
