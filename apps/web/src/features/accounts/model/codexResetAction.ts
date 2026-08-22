import type { AccountRow } from '@/features/accounts/model/accountRows';
import type { AuthFileItem, CodexQuotaState } from '@/types';
import { normalizeAuthIndex } from '@/utils/authIndex';

const CODEX_PROVIDER_TYPE = 'codex';

/**
 * Reset-credit action state for a Codex credential.
 *
 * Display evidence (live quota merged with persisted quota snapshots) may keep
 * showing a historical reset-credit count while the live quota state is
 * unknown. Snapshots are evidence for display only: the mutation must be
 * authorized by a verified live count, or trigger a live verification on
 * click. `busy` therefore does not reflect eligibility, only that the action
 * cannot start right now.
 */
export type CodexResetActionState =
  | { kind: 'unsupported'; reasonKey: string }
  | { kind: 'busy'; verifying: boolean }
  | { kind: 'available'; count: number }
  | { kind: 'needs_verification'; snapshotCount: number | null }
  | { kind: 'unavailable'; reasonKey: string };

export interface CodexResetActionStateInput {
  row: Pick<AccountRow, 'provider' | 'disabled' | 'runtimeOnly' | 'raw'>;
  /** Live reconciled Codex quota (provider/header/inspection evidence), without snapshot merge. */
  liveQuota: CodexQuotaState | undefined;
  /** Snapshot-merged quota used for display; the only legitimate snapshot consumer. */
  displayQuota: CodexQuotaState | undefined;
  configurationSaving: boolean;
  verifying: boolean;
}

export interface CodexResetActionPresentation {
  disabled: boolean;
  busy: boolean;
  titleKey: string | null;
  /** Whether the action is reachable at all (drawer menu item visibility). */
  interactive: boolean;
}

const hasResetAuthIndex = (file: AuthFileItem): boolean =>
  normalizeAuthIndex(file['auth_index'] ?? file.authIndex) !== null;

const hasPositiveResetEvidence = (quota: CodexQuotaState | undefined): boolean => {
  const count = quota?.rateLimitResetCreditsAvailableCount;
  if (typeof count === 'number') return count > 0;
  return (quota?.rateLimitResetCredits?.length ?? 0) > 0;
};

// row.disabled intentionally does not block the reset: the CPA management
// /api-call resolves disabled credentials by auth_index without filtering, and
// reset credits exist to recover quota-limited credentials. runtimeOnly rows
// stay blocked because plugin virtual credentials have no stable mutation
// target.
export const resolveCodexResetActionState = ({
  row,
  liveQuota,
  displayQuota,
  configurationSaving,
  verifying,
}: CodexResetActionStateInput): CodexResetActionState => {
  if (row.provider !== CODEX_PROVIDER_TYPE || row.runtimeOnly) {
    return { kind: 'unsupported', reasonKey: 'codex_quota.reset_unsupported_credential' };
  }
  if (!hasResetAuthIndex(row.raw)) {
    return { kind: 'unsupported', reasonKey: 'codex_quota.missing_auth_index' };
  }
  if (verifying || configurationSaving) {
    return { kind: 'busy', verifying };
  }
  const liveCount = liveQuota?.rateLimitResetCreditsAvailableCount;
  if (liveQuota?.status === 'success' && typeof liveCount === 'number') {
    return liveCount > 0
      ? { kind: 'available', count: liveCount }
      : { kind: 'unavailable', reasonKey: 'codex_quota.reset_unavailable_no_credits' };
  }
  return hasPositiveResetEvidence(displayQuota)
    ? {
        kind: 'needs_verification',
        snapshotCount: displayQuota?.rateLimitResetCreditsAvailableCount ?? null,
      }
    : { kind: 'unavailable', reasonKey: 'codex_quota.reset_unavailable_no_credits' };
};

export const isCodexResetActionExecutable = (state: CodexResetActionState): boolean =>
  state.kind === 'available' || state.kind === 'needs_verification';

export const resolveCodexResetActionPresentation = (
  state: CodexResetActionState
): CodexResetActionPresentation => {
  switch (state.kind) {
    case 'unsupported':
      return { disabled: true, busy: false, titleKey: state.reasonKey, interactive: false };
    case 'busy':
      return state.verifying
        ? {
            disabled: true,
            busy: true,
            titleKey: 'codex_quota.reset_verify_in_progress',
            interactive: true,
          }
        : { disabled: true, busy: false, titleKey: null, interactive: true };
    case 'available':
    case 'needs_verification':
      return {
        disabled: false,
        busy: false,
        titleKey: 'codex_quota.reset_requires_verification_hint',
        interactive: true,
      };
    case 'unavailable':
      return { disabled: true, busy: false, titleKey: state.reasonKey, interactive: false };
  }
};
