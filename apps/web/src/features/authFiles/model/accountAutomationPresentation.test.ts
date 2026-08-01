import { describe, expect, it } from 'vitest';
import {
  canBulkDeleteAccountAction,
  canBulkDeleteAccountActions,
  getAccountAutomationPresentation,
  isPendingAccountAction,
  selectAccountActionCandidate,
} from './accountAutomationPresentation';

const candidate = (overrides = {}) => ({
  id: 1,
  actionType: 'review',
  status: 'pending',
  authFileName: 'xai.json',
  reason: 'permission denied',
  autoDisableEligible: true,
  firstSeenAtMs: 1,
  lastSeenAtMs: 1,
  hitCount: 1,
  createdAtMs: 1,
  updatedAtMs: 1,
  ...overrides,
});

describe('accountAutomationPresentation', () => {
  it('marks only explicit delete candidates as bulk deletable', () => {
    expect(canBulkDeleteAccountAction(candidate({ actionType: 'delete' }))).toBe(true);
    expect(canBulkDeleteAccountAction(candidate({ actionType: 'reauth' }))).toBe(false);
    expect(
      canBulkDeleteAccountAction(candidate({ status: 'resolved', actionType: 'delete' }))
    ).toBe(false);
  });

  it('shows auto-disabled review candidates as dangerous', () => {
    const item = candidate({ autoDisabledAtMs: 123 });
    expect(isPendingAccountAction(item)).toBe(true);
    expect(getAccountAutomationPresentation(item)).toMatchObject({
      tone: 'danger',
      labelKey: 'auth_files.automation_action_review_disabled',
      titleKey: 'auth_files.automation_action_review_disabled_title',
    });
  });

  it('does not bulk-delete mixed pending actions', () => {
    expect(
      canBulkDeleteAccountActions([
        candidate({ id: 1, actionType: 'delete' }),
        candidate({ id: 2, actionType: 'reauth' }),
      ])
    ).toBe(false);
    expect(
      canBulkDeleteAccountActions([
        candidate({ id: 1, actionType: 'delete' }),
        candidate({ id: 2, actionType: 'delete' }),
      ])
    ).toBe(true);
  });

  it('keeps an auto-disabled candidate visible over a newer review', () => {
    const selected = selectAccountActionCandidate([
      candidate({ id: 1, autoDisabledAtMs: 100, lastSeenAtMs: 100 }),
      candidate({ id: 2, lastSeenAtMs: 200 }),
    ]);
    expect(selected?.id).toBe(1);
  });

  it('does not mention auto-disable for ordinary review candidates', () => {
    expect(getAccountAutomationPresentation(candidate())).toMatchObject({
      labelKey: 'auth_files.automation_action_review',
      titleKey: 'auth_files.automation_action_review_title',
    });
  });
});
