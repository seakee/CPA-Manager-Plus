import type { AccountActionCandidate } from '@/services/api/usageService';

export type AccountAutomationTone = 'danger' | 'warning' | 'info';

export interface AccountAutomationPresentation {
  labelKey: string;
  labelDefault: string;
  titleKey: string;
  titleDefault: string;
  tone: AccountAutomationTone;
}

export const isPendingAccountAction = (candidate?: AccountActionCandidate): boolean =>
  candidate?.status === 'pending';

export const canBulkDeleteAccountAction = (candidate?: AccountActionCandidate): boolean =>
  isPendingAccountAction(candidate) && candidate?.actionType === 'delete';

export const canBulkDeleteAccountActions = (candidates: AccountActionCandidate[]): boolean => {
  const pending = candidates.filter(isPendingAccountAction);
  return pending.length > 0 && pending.every(canBulkDeleteAccountAction);
};

export const selectAccountActionCandidate = (
  candidates: AccountActionCandidate[]
): AccountActionCandidate | undefined => {
  const pending = candidates.filter(isPendingAccountAction);
  if (pending.length === 0) return undefined;
  return [...pending].sort((left, right) => {
    const disabledCompare =
      Number(Boolean(right.autoDisabledAtMs)) - Number(Boolean(left.autoDisabledAtMs));
    if (disabledCompare !== 0) return disabledCompare;
    return right.lastSeenAtMs - left.lastSeenAtMs;
  })[0];
};

export function getAccountAutomationPresentation(
  candidate: AccountActionCandidate
): AccountAutomationPresentation {
  if (candidate.actionType === 'delete') {
    return {
      labelKey: 'auth_files.automation_action_delete',
      labelDefault: 'Delete suggested',
      titleKey: 'auth_files.automation_action_delete_title',
      titleDefault: 'A request or inspection reported that this account is no longer usable.',
      tone: 'danger',
    };
  }
  if (candidate.actionType === 'reauth') {
    return {
      labelKey: 'auth_files.automation_action_reauth',
      labelDefault: 'Re-login required',
      titleKey: 'auth_files.automation_action_reauth_title',
      titleDefault: 'The credential is invalid or expired and should be authorized again.',
      tone: 'warning',
    };
  }
  return {
    labelKey: candidate.autoDisabledAtMs
      ? 'auth_files.automation_action_review_disabled'
      : 'auth_files.automation_action_review',
    labelDefault: candidate.autoDisabledAtMs ? 'Disabled · review' : 'Review required',
    titleKey: candidate.autoDisabledAtMs
      ? 'auth_files.automation_action_review_disabled_title'
      : 'auth_files.automation_action_review_title',
    titleDefault: candidate.autoDisabledAtMs
      ? 'The credential was automatically disabled and requires manual review.'
      : 'The authentication failure requires manual review.',
    tone: candidate.autoDisabledAtMs ? 'danger' : 'info',
  };
}
