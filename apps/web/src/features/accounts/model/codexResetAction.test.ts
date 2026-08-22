import { describe, expect, it } from 'vitest';
import {
  isCodexResetActionExecutable,
  resolveCodexResetActionPresentation,
  resolveCodexResetActionState,
  type CodexResetActionState,
  type CodexResetActionStateInput,
} from '@/features/accounts/model/codexResetAction';
import type { AuthFileItem, CodexQuotaState } from '@/types';

const codexFile = { name: 'codex.json', authIndex: 'auth-1' } as AuthFileItem;

const buildInput = (
  overrides: Partial<CodexResetActionStateInput> = {}
): CodexResetActionStateInput => ({
  row: { provider: 'codex', disabled: false, runtimeOnly: false, raw: codexFile },
  liveQuota: undefined,
  displayQuota: undefined,
  configurationSaving: false,
  verifying: false,
  ...overrides,
});

const liveSuccess = (count: number | null): CodexQuotaState =>
  ({
    status: 'success',
    windows: [],
    rateLimitResetCreditsAvailableCount: count,
  }) as CodexQuotaState;

describe('codex reset action state', () => {
  it('marks non-codex providers unsupported', () => {
    expect(
      resolveCodexResetActionState(buildInput({ row: { ...buildInput().row, provider: 'claude' } }))
    ).toEqual({
      kind: 'unsupported',
      reasonKey: 'codex_quota.reset_unsupported_credential',
    });
  });

  it('keeps runtime-only credentials unsupported', () => {
    expect(
      resolveCodexResetActionState(buildInput({ row: { ...buildInput().row, runtimeOnly: true } }))
    ).toEqual({
      kind: 'unsupported',
      reasonKey: 'codex_quota.reset_unsupported_credential',
    });
  });

  it('marks credentials without auth_index unsupported', () => {
    const file = { name: 'codex.json' } as AuthFileItem;
    expect(
      resolveCodexResetActionState(buildInput({ row: { ...buildInput().row, raw: file } }))
    ).toEqual({
      kind: 'unsupported',
      reasonKey: 'codex_quota.missing_auth_index',
    });
  });

  it('treats configuration saving as busy without verifying flag', () => {
    expect(resolveCodexResetActionState(buildInput({ configurationSaving: true }))).toEqual({
      kind: 'busy',
      verifying: false,
    });
  });

  it('treats an in-flight verification as busy with verifying flag', () => {
    expect(resolveCodexResetActionState(buildInput({ verifying: true }))).toEqual({
      kind: 'busy',
      verifying: true,
    });
  });

  it('allows reset immediately when live quota verifies a positive count', () => {
    expect(resolveCodexResetActionState(buildInput({ liveQuota: liveSuccess(3) }))).toEqual({
      kind: 'available',
      count: 3,
    });
  });

  it('stays unavailable when live quota verifies zero credits', () => {
    expect(resolveCodexResetActionState(buildInput({ liveQuota: liveSuccess(0) }))).toEqual({
      kind: 'unavailable',
      reasonKey: 'codex_quota.reset_unavailable_no_credits',
    });
  });

  it('requires verification when live count is unknown but display evidence shows credits', () => {
    const display = liveSuccess(2);
    expect(
      resolveCodexResetActionState(
        buildInput({ liveQuota: liveSuccess(null), displayQuota: display })
      )
    ).toEqual({ kind: 'needs_verification', snapshotCount: 2 });
  });

  it('requires verification when live quota is idle or missing entirely', () => {
    const idle = { status: 'idle', windows: [] } as CodexQuotaState;
    expect(
      resolveCodexResetActionState(buildInput({ liveQuota: idle, displayQuota: liveSuccess(1) }))
    ).toEqual({ kind: 'needs_verification', snapshotCount: 1 });
    expect(resolveCodexResetActionState(buildInput({ displayQuota: liveSuccess(1) }))).toEqual({
      kind: 'needs_verification',
      snapshotCount: 1,
    });
  });

  it('requires verification when live quota is a preserved failure state with display evidence', () => {
    const errorState = {
      status: 'error',
      windows: [],
      error: 'failed',
      rateLimitResetCreditsAvailableCount: 1,
    } as CodexQuotaState;
    expect(
      resolveCodexResetActionState(buildInput({ liveQuota: errorState, displayQuota: errorState }))
    ).toEqual({ kind: 'needs_verification', snapshotCount: 1 });
  });

  it('requires verification when only the reset-credit list is present without a count', () => {
    const display = {
      status: 'success',
      windows: [],
      rateLimitResetCreditsAvailableCount: null,
      rateLimitResetCredits: [
        { id: 'credit-1', status: 'available', grantedAt: '', expiresAt: new Date().toISOString() },
      ],
    } as unknown as CodexQuotaState;
    expect(resolveCodexResetActionState(buildInput({ displayQuota: display }))).toEqual({
      kind: 'needs_verification',
      snapshotCount: null,
    });
  });

  it('stays unavailable when display evidence is a verified zero', () => {
    expect(resolveCodexResetActionState(buildInput({ displayQuota: liveSuccess(0) }))).toEqual({
      kind: 'unavailable',
      reasonKey: 'codex_quota.reset_unavailable_no_credits',
    });
  });

  it('does not block disabled credentials with a verified reset credit', () => {
    expect(
      resolveCodexResetActionState(
        buildInput({ row: { ...buildInput().row, disabled: true }, liveQuota: liveSuccess(1) })
      )
    ).toEqual({ kind: 'available', count: 1 });
  });
});

describe('codex reset action presentation', () => {
  it('disables unsupported credentials with their reason as title', () => {
    expect(
      resolveCodexResetActionPresentation({
        kind: 'unsupported',
        reasonKey: 'codex_quota.reset_unsupported_credential',
      })
    ).toEqual({
      disabled: true,
      busy: false,
      titleKey: 'codex_quota.reset_unsupported_credential',
      interactive: false,
    });
  });

  it('shows a verifying spinner for the busy verifying state', () => {
    expect(resolveCodexResetActionPresentation({ kind: 'busy', verifying: true })).toEqual({
      disabled: true,
      busy: true,
      titleKey: 'codex_quota.reset_verify_in_progress',
      interactive: true,
    });
  });

  it('keeps configuration-saving busy clickable-in-principle but disabled', () => {
    expect(resolveCodexResetActionPresentation({ kind: 'busy', verifying: false })).toEqual({
      disabled: true,
      busy: false,
      titleKey: null,
      interactive: true,
    });
  });

  it('hints that verification runs before reset for executable states', () => {
    const states: CodexResetActionState[] = [
      { kind: 'available', count: 1 },
      { kind: 'needs_verification', snapshotCount: 1 },
    ];
    for (const state of states) {
      expect(resolveCodexResetActionPresentation(state)).toEqual({
        disabled: false,
        busy: false,
        titleKey: 'codex_quota.reset_requires_verification_hint',
        interactive: true,
      });
    }
  });

  it('disables unavailable credentials with the no-credits reason', () => {
    expect(
      resolveCodexResetActionPresentation({
        kind: 'unavailable',
        reasonKey: 'codex_quota.reset_unavailable_no_credits',
      })
    ).toEqual({
      disabled: true,
      busy: false,
      titleKey: 'codex_quota.reset_unavailable_no_credits',
      interactive: false,
    });
  });
});

describe('isCodexResetActionExecutable', () => {
  it('only treats available and needs_verification as executable', () => {
    expect(isCodexResetActionExecutable({ kind: 'available', count: 1 })).toBe(true);
    expect(isCodexResetActionExecutable({ kind: 'needs_verification', snapshotCount: 1 })).toBe(
      true
    );
    expect(
      isCodexResetActionExecutable({
        kind: 'unsupported',
        reasonKey: 'codex_quota.reset_unsupported_credential',
      })
    ).toBe(false);
    expect(isCodexResetActionExecutable({ kind: 'busy', verifying: true })).toBe(false);
    expect(
      isCodexResetActionExecutable({
        kind: 'unavailable',
        reasonKey: 'codex_quota.reset_unavailable_no_credits',
      })
    ).toBe(false);
  });
});
