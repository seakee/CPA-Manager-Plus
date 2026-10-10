import type { AccountRow } from './accountRows';
import {
  getAccountRequestCredentialEvidence,
  hasAccountNonSpendControlQuotaLimitEvidence,
  hasAccountQuotaLimitEvidence,
  isAccountInspectionHealthyEvidence,
  isAccountRequestCredentialEvidenceCurrent,
  resolveAccountAuthenticationProblemEvidence,
  resolveAccountExceptionProblemEvidence,
  resolveAccountRequestHealthEvidence,
  type AccountRequestEvidenceBySelectionKey,
  type AccountRequestEvidenceInput,
} from './accountHealthEvidence';

export type AccountRecommendationAction =
  | 'refresh'
  | 'disable'
  | 'enable'
  | 'restore-default'
  | 'reauth'
  | 'review';

export type AccountRecommendationPriority = 'critical' | 'high' | 'medium' | 'low';

export interface AccountRecommendation {
  row: AccountRow;
  action: AccountRecommendationAction;
  priority: AccountRecommendationPriority;
  reasonKey: string;
}

const priorityRank: Record<AccountRecommendationPriority, number> = {
  critical: 4,
  high: 3,
  medium: 2,
  low: 1,
};

export const getRecommendationRank = (priority: AccountRecommendationPriority) =>
  priorityRank[priority] ?? 0;

const evidenceSensitiveRecommendationReasonKeys = new Set([
  'accounts.recommend_reason_inspection',
  'accounts.recommend_reason_request_auth',
  'accounts.recommend_reason_credential_auth',
  'accounts.recommend_reason_request_failure',
  'accounts.recommend_reason_quota_limited',
  'accounts.recommend_reason_spend_control_limited',
  'accounts.recommend_reason_quota_auth',
  'accounts.recommend_reason_error',
]);

export const isAccountRecommendationEvidenceSensitive = (
  recommendation: AccountRecommendation | null | undefined
) =>
  recommendation !== null &&
  recommendation !== undefined &&
  evidenceSensitiveRecommendationReasonKeys.has(recommendation.reasonKey);

const isFiniteSpendControlAmount = (value: string | null | undefined): boolean => {
  const normalized = value?.trim() ?? '';
  return normalized !== '' && Number.isFinite(Number(normalized));
};

const hasResolvedSpendControlEvidence = (row: AccountRow): boolean => {
  const limit = row.quota.spendControlIndividualLimit;
  return (
    row.quota.spendControlReached === true &&
    limit !== null &&
    limit !== undefined &&
    isFiniteSpendControlAmount(limit.limit) &&
    isFiniteSpendControlAmount(limit.used) &&
    isFiniteSpendControlAmount(limit.remaining) &&
    typeof row.quota.fetchedAtMs === 'number' &&
    Number.isFinite(row.quota.fetchedAtMs)
  );
};

export const buildAccountRecommendation = (
  row: AccountRow,
  requestEvidenceInput: AccountRequestEvidenceInput = {}
): AccountRecommendation | null => {
  const requestEvidence = resolveAccountRequestHealthEvidence(requestEvidenceInput);
  const requestCredentialEvidence = getAccountRequestCredentialEvidence(requestEvidence);
  const authenticationProblem = resolveAccountAuthenticationProblemEvidence(row, requestEvidence);
  const exceptionProblem = resolveAccountExceptionProblemEvidence(row, requestEvidence);

  if (row.runtimeOnly) {
    return {
      row,
      action: 'review',
      priority: 'low',
      reasonKey: 'accounts.recommend_reason_runtime',
    };
  }

  if (authenticationProblem?.source === 'request') {
    return {
      row,
      action: 'reauth',
      priority: 'critical',
      reasonKey: 'accounts.recommend_reason_request_auth',
    };
  }

  if (authenticationProblem?.source === 'quota_refresh') {
    return {
      row,
      action: 'reauth',
      priority: 'critical',
      reasonKey: 'accounts.recommend_reason_quota_auth',
    };
  }

  if (authenticationProblem?.source === 'inspection' && row.inspection) {
    const action =
      row.inspection.action === 'disable' ||
      row.inspection.action === 'enable' ||
      row.inspection.action === 'reauth'
        ? row.inspection.action
        : 'review';
    return {
      row,
      action,
      priority: row.inspection.action === 'reauth' ? 'critical' : 'high',
      reasonKey: 'accounts.recommend_reason_inspection',
    };
  }

  if (authenticationProblem) {
    return {
      row,
      action: 'reauth',
      priority: 'critical',
      reasonKey: 'accounts.recommend_reason_credential_auth',
    };
  }

  if (exceptionProblem?.source === 'inspection') {
    const action =
      row.inspection?.action === 'disable' ||
      row.inspection?.action === 'enable' ||
      row.inspection?.action === 'reauth'
        ? row.inspection.action
        : 'review';
    return {
      row,
      action,
      priority:
        row.disabled && action === 'enable' && isAccountInspectionHealthyEvidence(row)
          ? 'medium'
          : row.inspection?.action === 'reauth'
            ? 'critical'
            : 'high',
      reasonKey:
        row.disabled && action === 'enable' && isAccountInspectionHealthyEvidence(row)
          ? 'accounts.recommend_reason_recovered'
          : 'accounts.recommend_reason_inspection',
    };
  }

  if (row.quota.status === 'exhausted') {
    return {
      row,
      action: row.disabled ? 'enable' : 'disable',
      priority: 'critical',
      reasonKey: row.disabled
        ? 'accounts.recommend_reason_disabled_exhausted'
        : 'accounts.recommend_reason_exhausted',
    };
  }

  if (
    hasResolvedSpendControlEvidence(row) &&
    !hasAccountNonSpendControlQuotaLimitEvidence(row, requestEvidenceInput)
  ) {
    return {
      row,
      action: 'review',
      priority: 'high',
      reasonKey: 'accounts.recommend_reason_spend_control_limited',
    };
  }

  if (hasAccountQuotaLimitEvidence(row, requestEvidenceInput)) {
    return {
      row,
      action: 'refresh',
      priority: 'high',
      reasonKey: 'accounts.recommend_reason_quota_limited',
    };
  }

  if (row.quota.status === 'low') {
    return {
      row,
      action: 'refresh',
      priority: 'high',
      reasonKey: 'accounts.recommend_reason_low',
    };
  }

  if (exceptionProblem?.source === 'request') {
    return {
      row,
      action: 'review',
      priority: 'medium',
      reasonKey: 'accounts.recommend_reason_request_failure',
    };
  }

  if (exceptionProblem?.source === 'quota_refresh') {
    return {
      row,
      action: 'refresh',
      priority: 'medium',
      reasonKey: 'accounts.recommend_reason_error',
    };
  }

  if (
    row.disabled &&
    (row.quota.status === 'ok' ||
      (requestCredentialEvidence?.direction === 'positive' &&
        isAccountRequestCredentialEvidenceCurrent(row, requestEvidence)))
  ) {
    return {
      row,
      action: 'enable',
      priority: 'medium',
      reasonKey: 'accounts.recommend_reason_recovered',
    };
  }

  if (row.priority !== null && row.priority < 0) {
    return {
      row,
      action: 'restore-default',
      priority: 'low',
      reasonKey: 'accounts.recommend_reason_priority',
    };
  }

  return null;
};

export const buildAccountRecommendations = (
  rows: AccountRow[],
  requestEvidenceBySelectionKey?: AccountRequestEvidenceBySelectionKey
): AccountRecommendation[] =>
  rows
    .map((row) =>
      buildAccountRecommendation(row, requestEvidenceBySelectionKey?.get(row.selectionKey))
    )
    .filter((item): item is AccountRecommendation => item !== null)
    .sort((left, right) => {
      const rankDiff = getRecommendationRank(right.priority) - getRecommendationRank(left.priority);
      if (rankDiff !== 0) return rankDiff;
      return left.row.fileName.localeCompare(right.row.fileName, undefined, {
        numeric: true,
        sensitivity: 'base',
      });
    });
