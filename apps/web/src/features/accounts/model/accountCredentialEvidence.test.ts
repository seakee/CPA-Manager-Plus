import { describe, expect, it } from 'vitest';
import type { AuthFileItem, CodexQuotaState } from '@/types';
import {
  buildInspectionCodexQuotaState,
  getAccountCredentialEvidenceCutoffs,
  getEffectiveAccountInspectionAction,
  hasPendingAccountInspectionAction,
  isKnownHealthyCodexQuota,
  mergeConfirmedReauthCodexQuotaStates,
  reconcileCodexQuotaEvidence,
  stripSupersededAccountInspectionStatus,
  type AccountInspectionSummary,
} from './accountCredentialEvidence';

const file: AuthFileItem = {
  name: 'shared.json',
  provider: 'codex',
  authIndex: 'codex-1',
};

const inspection = (
  overrides: Partial<AccountInspectionSummary> = {}
): AccountInspectionSummary => ({
  source: 'server',
  disabled: false,
  action: 'keep',
  actionReason: '',
  actionStatus: 'none',
  executedAction: '',
  statusCode: 200,
  usedPercent: 30,
  isQuota: false,
  planType: 'plus',
  quotaWindows: [
    {
      id: 'weekly',
      labelKey: 'codex_quota.secondary_window',
      usedPercent: 30,
      resetLabel: 'later',
      resetAtMs: 20_000,
      resetAccuracy: 'exact',
      limitWindowSeconds: 604_800,
    },
  ],
  error: '',
  errorKind: '',
  runId: 1,
  resultId: 1,
  createdAtMs: 2_000,
  ...overrides,
});

describe('confirmed reauth Codex quota state merge', () => {
  it('retains older exhausted windows when replacement state is a stale 401', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      providerQuota(),
      {
        status: 'error',
        windows: [],
        error: 'HTTP 401 token expired',
        errorStatus: 401,
        failedAtMs: 1_500,
      },
      2_000
    );

    expect(result).toMatchObject({
      status: 'success',
      quotaInventoryObserved: true,
      windows: [expect.objectContaining({ usedPercent: 100 })],
    });
    expect(result?.error).toBeUndefined();
    expect(result?.errorStatus).toBeUndefined();
  });

  it('keeps newer quota facts from a replacement state whose 401 is stale', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      providerQuota({ planType: 'team' }),
      {
        status: 'error',
        windows: [
          {
            id: 'weekly',
            label: 'Weekly replacement',
            usedPercent: 20,
            resetLabel: 'replacement reset',
            observedAtMs: 1_500,
          },
        ],
        quotaInventoryObserved: true,
        fetchedAtMs: 1_500,
        observedAtMs: 1_500,
        planType: 'plus',
        error: 'HTTP 401 token expired',
        errorStatus: 401,
        failedAtMs: 1_600,
      },
      2_000
    );

    expect(result).toMatchObject({
      status: 'success',
      error: undefined,
      errorStatus: undefined,
      planType: 'plus',
      fetchedAtMs: 1_500,
      windows: [expect.objectContaining({ usedPercent: 20, observedAtMs: 1_500 })],
    });
  });

  it('clears stale scalar values from a newer partial Provider success', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      providerQuota({
        windows: [
          {
            id: 'weekly',
            label: 'Weekly source',
            usedPercent: 100,
            resetLabel: 'old',
            observedAtMs: 1_000,
          },
        ],
        planType: 'team',
        subscriptionActiveUntil: 'old-until',
        creditsHasCredits: true,
        creditsUnlimited: false,
        creditsBalance: '120',
        creditsOverageLimitReached: true,
        creditsApproxLocalMessages: 24,
        creditsApproxCloudMessages: 12,
        spendControlReached: true,
        spendControlIndividualLimit: 200,
        primaryOverSecondaryLimitPercent: 100,
        rateLimitResetCreditsAvailableCount: 2,
        rateLimitResetCredits: [
          {
            id: 'credit-1',
            status: 'available',
            grantedAt: '2026-01-01T00:00:00Z',
            expiresAt: '2026-01-02T00:00:00Z',
          },
        ],
        rateLimitResetCreditsError: 'old reset error',
      }),
      {
        status: 'success',
        quotaInventoryObserved: false,
        windows: [],
        fetchedAtMs: 1_500,
        observedAtMs: 1_500,
        planType: null,
        subscriptionActiveUntil: null,
        creditsHasCredits: null,
        creditsUnlimited: undefined,
        creditsBalance: null,
        creditsOverageLimitReached: false,
        creditsApproxLocalMessages: null,
        creditsApproxCloudMessages: null,
        spendControlReached: null,
        spendControlIndividualLimit: null,
        primaryOverSecondaryLimitPercent: 0,
        rateLimitResetCreditsAvailableCount: null,
        rateLimitResetCredits: [],
        rateLimitResetCreditsError: null,
      },
      2_000
    );

    expect(result).toMatchObject({
      status: 'success',
      quotaInventoryObserved: false,
      windows: [expect.objectContaining({ id: 'weekly', usedPercent: 100 })],
      planType: null,
      subscriptionActiveUntil: null,
      creditsHasCredits: null,
      creditsBalance: null,
      creditsOverageLimitReached: false,
      creditsApproxLocalMessages: null,
      creditsApproxCloudMessages: null,
      spendControlReached: null,
      spendControlIndividualLimit: null,
      primaryOverSecondaryLimitPercent: 0,
      rateLimitResetCreditsAvailableCount: null,
      rateLimitResetCredits: [],
      rateLimitResetCreditsError: null,
    });
    expect(result?.creditsUnlimited).toBeUndefined();
  });

  it('preserves newer authoritative empty Provider inventory under a non-auth error lifecycle', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      providerQuota(),
      {
        status: 'error',
        quotaInventoryObserved: true,
        windows: [],
        fetchedAtMs: 1_500,
        observedAtMs: 1_500,
        error: 'HTTP 503',
        errorStatus: 503,
        failedAtMs: 1_600,
      },
      2_000
    );

    expect(result).toMatchObject({
      status: 'error',
      error: 'HTTP 503',
      errorStatus: 503,
      failedAtMs: 1_600,
      quotaInventoryObserved: true,
      windows: [],
    });
  });

  it('preserves newer Provider scalar clears under a non-auth error lifecycle', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      providerQuota({
        planType: 'team',
        creditsBalance: '100',
        spendControlIndividualLimit: 200,
        rateLimitResetCreditsAvailableCount: 2,
        rateLimitResetCredits: [
          {
            id: 'credit-1',
            status: 'available',
            grantedAt: '2026-01-01T00:00:00Z',
            expiresAt: '2026-01-02T00:00:00Z',
          },
        ],
      }),
      {
        status: 'error',
        quotaInventoryObserved: false,
        windows: [],
        fetchedAtMs: 1_500,
        observedAtMs: 1_500,
        planType: null,
        creditsBalance: null,
        spendControlIndividualLimit: null,
        rateLimitResetCreditsAvailableCount: null,
        rateLimitResetCredits: [],
        error: 'HTTP 503',
        errorStatus: 503,
        failedAtMs: 1_600,
      },
      2_000
    );

    expect(result).toMatchObject({
      status: 'error',
      errorStatus: 503,
      failedAtMs: 1_600,
      planType: null,
      creditsBalance: null,
      spendControlIndividualLimit: null,
      rateLimitResetCreditsAvailableCount: null,
      rateLimitResetCredits: [],
      windows: [expect.objectContaining({ usedPercent: 100 })],
    });
  });

  it('preserves a cached Provider snapshot when a stale 401 lifecycle is superseded', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      providerQuota({ planType: 'team', creditsBalance: '100' }),
      {
        status: 'error',
        quotaInventoryObserved: true,
        windows: [],
        fetchedAtMs: 1_500,
        observedAtMs: 1_500,
        planType: null,
        creditsBalance: null,
        error: 'HTTP 401 token expired',
        errorStatus: 401,
        failedAtMs: 1_600,
      },
      2_000
    );

    expect(result).toMatchObject({
      status: 'success',
      error: undefined,
      errorStatus: undefined,
      failedAtMs: undefined,
      quotaInventoryObserved: true,
      windows: [],
      planType: null,
      creditsBalance: null,
    });
  });

  it('preserves quota-limit facts from a partial Provider success', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      providerQuota({
        fetchedAtMs: 1_000,
        windows: [
          {
            id: 'weekly',
            label: 'Weekly source',
            usedPercent: 30,
            resetLabel: 'source reset',
            observedAtMs: 1_000,
          },
        ],
      }),
      {
        status: 'success',
        quotaInventoryObserved: false,
        fetchedAtMs: 1_500,
        observedAtMs: 1_500,
        windows: [],
        spendControlReached: true,
        creditsOverageLimitReached: true,
        rateLimitResetCreditsError: 'reset credits failed',
      },
      2_000
    );

    expect(result).toMatchObject({
      status: 'success',
      error: undefined,
      errorStatus: undefined,
      failedAtMs: undefined,
      spendControlReached: true,
      creditsOverageLimitReached: true,
      rateLimitResetCreditsError: 'reset credits failed',
      windows: [expect.objectContaining({ usedPercent: 30, observedAtMs: 1_000 })],
    });
  });

  it('does not let an older success lifecycle erase fresher source quota facts', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      {
        status: 'error',
        quotaInventoryObserved: false,
        fetchedAtMs: 1_500,
        observedAtMs: 1_500,
        windows: [],
        spendControlReached: true,
        creditsOverageLimitReached: true,
        error: 'HTTP 401 token expired',
        errorStatus: 401,
        failedAtMs: 1_600,
      },
      {
        status: 'success',
        quotaInventoryObserved: false,
        fetchedAtMs: 1_000,
        observedAtMs: 1_000,
        windows: [],
      },
      2_000
    );

    expect(result).toMatchObject({
      status: 'success',
      error: undefined,
      errorStatus: undefined,
      failedAtMs: undefined,
      spendControlReached: true,
      creditsOverageLimitReached: true,
    });
  });

  it.each([429, 503])(
    'does not turn a retained HTTP %s lifecycle into synthetic success after stale 401 cleanup',
    (status) => {
      const result = mergeConfirmedReauthCodexQuotaStates(
        {
          status: 'error',
          windows: [],
          error: 'HTTP 401 token expired',
          errorStatus: 401,
          failedAtMs: 1_500,
        },
        {
          status: 'error',
          windows: [],
          error: `HTTP ${status}`,
          errorStatus: status,
          failedAtMs: 1_200,
        },
        2_000
      );

      expect(result).toMatchObject({
        status: 'error',
        errorStatus: status,
        failedAtMs: 1_200,
      });
      expect(result?.errorStatus).not.toBe(401);
    }
  );

  it('keeps non-auth lifecycle and independently fresher source quota facts', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      {
        status: 'error',
        windows: [
          {
            id: 'weekly',
            label: 'Weekly source',
            usedPercent: 30,
            resetLabel: 'source reset',
            observedAtMs: 1_400,
          },
        ],
        fetchedAtMs: 1_400,
        error: 'HTTP 401 token expired',
        errorStatus: 401,
        failedAtMs: 1_600,
      },
      {
        status: 'error',
        windows: [
          {
            id: 'weekly',
            label: 'Weekly replacement cache',
            usedPercent: 20,
            resetLabel: 'stale replacement reset',
            observedAtMs: 1_200,
          },
        ],
        fetchedAtMs: 1_200,
        error: 'HTTP 503',
        errorStatus: 503,
        failedAtMs: 1_500,
      },
      2_000
    );

    expect(result).toMatchObject({
      status: 'error',
      errorStatus: 503,
      failedAtMs: 1_500,
      windows: [expect.objectContaining({ usedPercent: 30, observedAtMs: 1_400 })],
    });
  });

  it('keeps old quota facts when a pure stale 401 is sanitized', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      providerQuota(),
      {
        status: 'error',
        windows: [],
        error: 'HTTP 401 token expired',
        errorStatus: 401,
        failedAtMs: 1_500,
      },
      2_000
    );

    expect(result).toMatchObject({
      status: 'success',
      quotaInventoryObserved: true,
      windows: [expect.objectContaining({ usedPercent: 100 })],
    });
  });

  it.each([429, 503])('retains older windows alongside a replacement HTTP %s', (status) => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      providerQuota(),
      {
        status: 'error',
        windows: [],
        error: `HTTP ${status}`,
        errorStatus: status,
        failedAtMs: 1_500,
      },
      2_000
    );

    expect(result).toMatchObject({
      status: 'error',
      errorStatus: status,
      windows: [expect.objectContaining({ usedPercent: 100 })],
    });
  });

  it.each([429, 503])(
    'does not use the HTTP %s lifecycle timestamp to replace fresher source windows',
    (status) => {
      const result = mergeConfirmedReauthCodexQuotaStates(
        providerQuota({ planType: 'team' }),
        {
          status: 'error',
          windows: [
            {
              id: 'weekly',
              label: 'Weekly replacement cache',
              usedPercent: 20,
              resetLabel: 'stale',
              observedAtMs: 500,
            },
          ],
          quotaInventoryObserved: true,
          planType: 'plus',
          fetchedAtMs: 500,
          observedAtMs: 500,
          error: `HTTP ${status}`,
          errorStatus: status,
          failedAtMs: 1_500,
        },
        2_000
      );

      expect(result).toMatchObject({
        status: 'error',
        errorStatus: status,
        failedAtMs: 1_500,
        planType: 'team',
        fetchedAtMs: 1_000,
        windows: [expect.objectContaining({ usedPercent: 100, observedAtMs: 1_000 })],
      });
    }
  );

  it('keeps fresher scalar quota facts when an error carries an older inherited payload', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      providerQuota({
        planType: 'team',
        subscriptionActiveUntil: 'team-until',
        creditsBalance: '40',
        rateLimitResetCreditsAvailableCount: 4,
      }),
      {
        status: 'error',
        windows: [],
        quotaInventoryObserved: true,
        planType: 'plus',
        subscriptionActiveUntil: 'plus-until',
        creditsBalance: '10',
        rateLimitResetCreditsAvailableCount: 0,
        fetchedAtMs: 500,
        observedAtMs: 500,
        error: 'HTTP 503',
        errorStatus: 503,
        failedAtMs: 1_500,
      },
      2_000
    );

    expect(result).toMatchObject({
      status: 'error',
      errorStatus: 503,
      failedAtMs: 1_500,
      planType: 'team',
      subscriptionActiveUntil: 'team-until',
      creditsBalance: '40',
      rateLimitResetCreditsAvailableCount: 4,
    });
  });

  it('retains rate-limit reset credits and plan metadata alongside an error-only replacement', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      providerQuota({
        planType: 'plus',
        rateLimitResetCreditsAvailableCount: 2,
        rateLimitResetCredits: [
          {
            id: 'credit-1',
            status: 'available',
            grantedAt: '2026-01-01T00:00:00Z',
            expiresAt: '2026-01-02T00:00:00Z',
          },
        ],
        rateLimitResetCreditsError: 'old refresh warning',
      }),
      {
        status: 'error',
        windows: [],
        error: 'HTTP 429 rate limited',
        errorStatus: 429,
        failedAtMs: 1_500,
        rateLimitResetCredits: [],
        rateLimitResetCreditsAvailableCount: null,
        rateLimitResetCreditsError: null,
      },
      2_000
    );

    expect(result).toMatchObject({
      status: 'error',
      errorStatus: 429,
      planType: 'plus',
      rateLimitResetCreditsAvailableCount: 2,
      rateLimitResetCredits: [expect.objectContaining({ id: 'credit-1' })],
      rateLimitResetCreditsError: 'old refresh warning',
    });
  });

  it('lets a newer complete quota inventory replace older windows', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      providerQuota(),
      providerQuota({
        fetchedAtMs: 3_000,
        windows: [
          {
            id: 'weekly',
            label: 'Weekly',
            usedPercent: 20,
            resetLabel: 'new',
            observedAtMs: 3_000,
          },
        ],
      }),
      2_000
    );

    expect(result).toMatchObject({
      status: 'success',
      windows: [expect.objectContaining({ usedPercent: 20, resetLabel: 'new' })],
    });
  });

  it('lets an authoritative inventory clear stale quota-limit and diagnostic facts', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      providerQuota({
        activeLimit: 'secondary',
        creditsOverageLimitReached: true,
        spendControlReached: true,
        spendControlIndividualLimit: 100,
        rateLimitReachedType: 'secondary',
        primaryOverSecondaryLimitPercent: 100,
        observedFromUsageHeaders: true,
        observedErrorKind: 'rate_limit',
        observedErrorCode: 'usage_limit_reached',
        rateLimitResetCreditsError: 'old error',
      }),
      providerQuota({
        fetchedAtMs: 3_000,
        observedAtMs: 3_000,
        windows: [
          {
            id: 'weekly',
            label: 'Weekly new',
            usedPercent: 20,
            resetLabel: 'new',
            observedAtMs: 3_000,
          },
        ],
        activeLimit: null,
        creditsOverageLimitReached: false,
        spendControlReached: null,
        spendControlIndividualLimit: null,
        rateLimitReachedType: null,
        primaryOverSecondaryLimitPercent: null,
        rateLimitResetCreditsError: null,
      }),
      2_000
    );

    expect(result).toMatchObject({
      status: 'success',
      windows: [expect.objectContaining({ usedPercent: 20 })],
      activeLimit: null,
      creditsOverageLimitReached: false,
      spendControlReached: null,
      spendControlIndividualLimit: null,
      rateLimitReachedType: null,
      primaryOverSecondaryLimitPercent: null,
      rateLimitResetCreditsError: null,
    });
    expect(result?.observedFromUsageHeaders).toBeUndefined();
    expect(result?.observedErrorKind).toBeUndefined();
    expect(result?.observedErrorCode).toBeUndefined();
    expect(isKnownHealthyCodexQuota(result)).toBe(true);
  });

  it('clears an older 401 before applying a newer complete quota inventory', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      {
        status: 'error',
        windows: [],
        error: 'HTTP 401 token expired',
        errorStatus: 401,
        failedAtMs: 1_000,
      },
      providerQuota({ fetchedAtMs: 3_000 }),
      2_000
    );

    expect(result).toMatchObject({
      status: 'success',
      windows: [expect.objectContaining({ usedPercent: 100 })],
    });
    expect(result?.error).toBeUndefined();
    expect(result?.errorStatus).toBeUndefined();
  });

  it('retains windows when an unknown-time old 401 is sanitized during migration', () => {
    const result = mergeConfirmedReauthCodexQuotaStates(
      {
        status: 'error',
        windows: [
          {
            id: 'weekly',
            label: 'Weekly',
            usedPercent: 100,
            resetLabel: 'later',
            observedAtMs: 1_000,
          },
        ],
        error: 'HTTP 401',
        errorStatus: 401,
      },
      undefined,
      2_000
    );

    expect(result).toMatchObject({
      status: 'success',
      error: undefined,
      errorStatus: undefined,
      windows: [expect.objectContaining({ usedPercent: 100 })],
    });
  });
});

const providerQuota = (overrides: Partial<CodexQuotaState> = {}): CodexQuotaState => ({
  status: 'success',
  windows: [
    {
      id: 'weekly',
      label: 'Weekly',
      usedPercent: 100,
      resetLabel: 'old',
      observedAtMs: 1_000,
    },
  ],
  quotaInventoryObserved: true,
  fetchedAtMs: 1_000,
  ...overrides,
});

describe('account credential evidence', () => {
  it('removes superseded inspection status while retaining valid quota evidence', () => {
    const stale = inspection({
      disabled: true,
      action: 'disable',
      actionReason: 'quota exhausted',
      actionStatus: 'pending',
      statusCode: 429,
      isQuota: true,
    });

    const retained = stripSupersededAccountInspectionStatus(stale, stale.createdAtMs + 1);

    expect(retained).toMatchObject({
      disabled: undefined,
      action: 'keep',
      actionReason: '',
      actionStatus: 'resolved',
      executedAction: '',
      statusCode: 429,
      quotaWindows: stale.quotaWindows,
    });
    expect(hasPendingAccountInspectionAction(retained)).toBe(false);
    expect(buildInspectionCodexQuotaState(file, retained)).toMatchObject({
      status: 'success',
      windows: [expect.objectContaining({ id: 'weekly', usedPercent: 30 })],
    });
  });

  it('lets a newer healthy inspection replace an older cached 401 and quota limit', () => {
    const old401 = providerQuota({
      status: 'error',
      error: 'HTTP 401',
      errorStatus: 401,
      failedAtMs: 1_000,
      observedFromUsageHeaders: true,
      observedErrorKind: 'auth_invalid',
      observedErrorCode: 'token_expired',
      rateLimitReachedType: 'secondary',
    });
    const inspectionQuota = buildInspectionCodexQuotaState(file, inspection());

    const result = reconcileCodexQuotaEvidence({
      providerQuota: old401,
      inspectionQuota,
    });

    expect(result?.status).toBe('success');
    expect(result?.errorStatus).toBeUndefined();
    expect(result?.observedFromUsageHeaders).toBeUndefined();
    expect(result?.observedErrorKind).toBeUndefined();
    expect(result?.observedErrorCode).toBeUndefined();
    expect(result?.rateLimitReachedType).toBeUndefined();
    expect(result?.windows[0]).toMatchObject({
      id: 'weekly',
      usedPercent: 30,
      resetAtMs: 20_000,
      observationSource: 'inspection',
    });
  });

  it('keeps a newer provider 401 ahead of an older healthy inspection', () => {
    const result = reconcileCodexQuotaEvidence({
      providerQuota: providerQuota({
        status: 'error',
        error: 'HTTP 401',
        errorStatus: 401,
        failedAtMs: 3_000,
      }),
      inspectionQuota: buildInspectionCodexQuotaState(file, inspection()),
    });

    expect(result?.status).toBe('error');
    expect(result?.errorStatus).toBe(401);
  });

  it.each([402, 429])('synchronizes quota evidence returned with HTTP %s', (statusCode) => {
    const result = buildInspectionCodexQuotaState(file, inspection({ statusCode, isQuota: true }));

    expect(result).toMatchObject({
      status: 'success',
      quotaInventoryObserved: true,
      observedAtMs: 2_000,
      rateLimitReachedType: 'inspection',
    });
    expect(result?.windows[0]).toMatchObject({
      id: 'weekly',
      usedPercent: 30,
      observationSource: 'inspection',
    });
  });

  it('keeps non-quota HTTP failures from becoming healthy through an empty observed inventory', () => {
    const failedInspection = inspection({
      actionReason: 'quota response unavailable',
      statusCode: 429,
      usedPercent: null,
      isQuota: false,
      quotaWindows: [],
      quotaInventoryObserved: true,
    });
    const inspectionQuota = buildInspectionCodexQuotaState(file, failedInspection);

    expect(inspectionQuota).toMatchObject({
      status: 'error',
      windows: [],
      error: 'quota response unavailable',
      errorStatus: 429,
      failedAtMs: 2_000,
      quotaInventoryObserved: true,
    });
    expect(
      reconcileCodexQuotaEvidence({
        providerQuota: providerQuota(),
        inspectionQuota,
      })
    ).toMatchObject({
      status: 'error',
      errorStatus: 429,
      windows: [expect.objectContaining({ id: 'weekly', usedPercent: 100 })],
    });
    expect(getAccountCredentialEvidenceCutoffs({ inspection: failedInspection })).toEqual({
      authenticationAtMs: 2_000,
      healthyQuotaAtMs: 0,
    });
  });

  it('does not use quota-limited inspection evidence as a healthy quota cutoff', () => {
    const quotaLimitedInspection = inspection({ statusCode: 429, isQuota: true });

    expect(getAccountCredentialEvidenceCutoffs({ inspection: quotaLimitedInspection })).toEqual({
      authenticationAtMs: 2_000,
      healthyQuotaAtMs: 0,
    });
  });

  it('normalizes derived inspection reset accuracy to estimated display evidence', () => {
    const result = buildInspectionCodexQuotaState(
      file,
      inspection({
        quotaWindows: [
          {
            id: 'five-hour',
            labelKey: 'codex_quota.primary_window',
            usedPercent: 30,
            resetLabel: 'later',
            resetAtMs: 20_000,
            resetAccuracy: 'derived',
            limitWindowSeconds: 18_000,
          },
        ],
      })
    );

    expect(result?.windows[0]?.resetAccuracy).toBe('estimated');
  });

  it('replaces unmatched stale Provider windows with a newer inspection inventory', () => {
    const inspectionQuota = buildInspectionCodexQuotaState(
      file,
      inspection({
        usedPercent: 20,
        quotaWindows: [
          {
            id: 'five-hour',
            labelKey: 'codex_quota.primary_window',
            usedPercent: 20,
            resetLabel: 'soon',
            resetAtMs: 10_000,
            resetAccuracy: 'exact',
            limitWindowSeconds: 18_000,
          },
        ],
      })
    );

    const result = reconcileCodexQuotaEvidence({
      providerQuota: providerQuota(),
      inspectionQuota,
    });

    expect(result?.windows).toEqual([
      expect.objectContaining({ id: 'five-hour', usedPercent: 20 }),
    ]);
  });

  it('lets a newer explicitly empty inspection inventory clear stale exhausted windows', () => {
    const inspectionQuota = buildInspectionCodexQuotaState(
      file,
      inspection({
        usedPercent: null,
        quotaWindows: [],
        quotaInventoryObserved: true,
      })
    );

    const result = reconcileCodexQuotaEvidence({
      providerQuota: providerQuota(),
      inspectionQuota,
    });

    expect(inspectionQuota).toMatchObject({
      status: 'success',
      windows: [],
      quotaInventoryObserved: true,
    });
    expect(result).toMatchObject({
      status: 'success',
      windows: [],
      quotaInventoryObserved: true,
    });
  });

  it('uses a credential boundary to discard all older quota evidence', () => {
    const result = reconcileCodexQuotaEvidence({
      providerQuota: providerQuota(),
      inspectionQuota: buildInspectionCodexQuotaState(file, inspection()),
      boundaryAtMs: 3_000,
    });

    expect(result).toBeUndefined();
  });

  it('keeps an older exhausted quota fact across an authentication recovery boundary', () => {
    const result = reconcileCodexQuotaEvidence({
      providerQuota: providerQuota({ fetchedAtMs: 1_000 }),
      authenticationBoundaryAtMs: 2_000,
    });

    expect(result).toMatchObject({
      status: 'success',
      fetchedAtMs: 1_000,
      windows: [expect.objectContaining({ usedPercent: 100 })],
    });
  });

  it.each([402, 429])(
    'keeps an older %s quota failure across an authentication recovery boundary',
    (statusCode) => {
      const result = reconcileCodexQuotaEvidence({
        providerQuota: providerQuota({
          status: 'error',
          windows: [],
          error: `HTTP ${statusCode} quota limit reached`,
          errorStatus: statusCode,
          fetchedAtMs: undefined,
          failedAtMs: 1_000,
        }),
        authenticationBoundaryAtMs: 2_000,
      });

      expect(result).toMatchObject({
        status: 'error',
        errorStatus: statusCode,
        failedAtMs: 1_000,
      });
    }
  );

  it('keeps an older inspection quota fact across an authentication recovery boundary', () => {
    const inspectionQuota = buildInspectionCodexQuotaState(
      file,
      inspection({
        createdAtMs: 1_000,
        statusCode: 429,
        isQuota: true,
      })
    );

    expect(
      reconcileCodexQuotaEvidence({
        inspectionQuota,
        authenticationBoundaryAtMs: 2_000,
      })
    ).toMatchObject({
      status: 'success',
      windows: [expect.objectContaining({ usedPercent: 30 })],
    });
  });

  it('drops an older 401 quota failure at an authentication recovery boundary', () => {
    const result = reconcileCodexQuotaEvidence({
      providerQuota: providerQuota({
        status: 'error',
        windows: [],
        error: 'HTTP 401 token expired',
        errorStatus: 401,
        fetchedAtMs: undefined,
        failedAtMs: 1_000,
      }),
      authenticationBoundaryAtMs: 2_000,
    });

    expect(result).toBeUndefined();
  });

  it('keeps a new 401 quota failure after an authentication recovery boundary', () => {
    const result = reconcileCodexQuotaEvidence({
      providerQuota: providerQuota({
        status: 'error',
        windows: [],
        error: 'HTTP 401 token expired',
        errorStatus: 401,
        fetchedAtMs: undefined,
        failedAtMs: 3_000,
      }),
      authenticationBoundaryAtMs: 2_000,
    });

    expect(result).toMatchObject({
      status: 'error',
      errorStatus: 401,
      failedAtMs: 3_000,
    });
  });

  it('retains old healthy quota for display without treating it as post-recovery authentication evidence', () => {
    const quota = providerQuota({
      fetchedAtMs: 1_000,
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 30,
          resetLabel: 'later',
          observedAtMs: 1_000,
        },
      ],
    });

    expect(
      reconcileCodexQuotaEvidence({
        providerQuota: quota,
        authenticationBoundaryAtMs: 2_000,
      })
    ).toMatchObject({ status: 'success', fetchedAtMs: 1_000 });
    expect(
      getAccountCredentialEvidenceCutoffs({
        providerQuota: quota,
        authenticationBoundaryAtMs: 2_000,
      })
    ).toEqual({ authenticationAtMs: 2_000, healthyQuotaAtMs: 0 });
  });

  it.each([1_000, undefined])(
    'discards a persisted Provider 401 at or without timestamp after credential refresh: %s',
    (failedAtMs) => {
      const result = reconcileCodexQuotaEvidence({
        providerQuota: providerQuota({
          status: 'error',
          windows: [],
          error: 'HTTP 401',
          errorStatus: 401,
          failedAtMs,
        }),
        credentialRefreshAtMs: 1_000,
      });

      expect(result).toBeUndefined();
    }
  );

  it('does not use credential refresh alone to discard quota-limit evidence', () => {
    const result = reconcileCodexQuotaEvidence({
      providerQuota: providerQuota({ fetchedAtMs: 1_000 }),
      credentialRefreshAtMs: 2_000,
    });

    expect(result?.status).toBe('success');
    expect(result?.windows[0]?.usedPercent).toBe(100);
  });

  it('lets a same-timestamp Provider success supersede Header limit metadata', () => {
    const result = reconcileCodexQuotaEvidence({
      headerQuota: providerQuota({
        fetchedAtMs: undefined,
        observedAtMs: 2_000,
        observedFromUsageHeaders: true,
        observedErrorKind: 'rate_limit',
        observedErrorCode: 'usage_limit_reached',
        rateLimitReachedType: 'secondary',
      }),
      providerQuota: providerQuota({
        fetchedAtMs: 2_000,
        windows: [
          {
            id: 'weekly',
            label: 'Weekly',
            usedPercent: 30,
            resetLabel: 'new',
            observedAtMs: 2_000,
          },
        ],
      }),
    });

    expect(result?.windows[0]?.usedPercent).toBe(30);
    expect(result?.observedFromUsageHeaders).toBeUndefined();
    expect(result?.observedErrorKind).toBeUndefined();
    expect(result?.observedErrorCode).toBeUndefined();
    expect(result?.rateLimitReachedType).toBeUndefined();
  });

  it('treats successfully executed inspection actions as handled', () => {
    const handled = inspection({ action: 'enable', actionStatus: 'success' });

    expect(hasPendingAccountInspectionAction(handled)).toBe(false);
    expect(getEffectiveAccountInspectionAction(handled)).toBe('keep');
  });

  it('does not turn a handled inspection 401 back into a reauth quota error', () => {
    const handled = inspection({
      action: 'reauth',
      actionStatus: 'success',
      statusCode: 401,
      usedPercent: null,
      quotaWindows: [],
    });

    expect(hasPendingAccountInspectionAction(handled)).toBe(false);
    expect(buildInspectionCodexQuotaState(file, handled)).toBeUndefined();
  });

  it('does not treat a null-status request failure as authentication recovery', () => {
    const failedInspection = inspection({
      action: 'keep',
      actionStatus: 'success',
      statusCode: null,
      usedPercent: null,
      isQuota: false,
      quotaWindows: [],
      error: 'request failed',
      errorKind: 'network_error',
      createdAtMs: 4_000,
    });

    expect(buildInspectionCodexQuotaState(file, failedInspection)).toBeUndefined();
    expect(getAccountCredentialEvidenceCutoffs({ inspection: failedInspection })).toEqual({
      authenticationAtMs: 0,
      healthyQuotaAtMs: 0,
    });
  });

  it('does not let a transient provider failure prove authentication recovery', () => {
    const cutoffs = getAccountCredentialEvidenceCutoffs({
      providerQuota: providerQuota({
        status: 'error',
        error: 'temporary failure',
        errorStatus: 503,
        failedAtMs: 4_000,
      }),
      inspection: inspection({
        action: 'reauth',
        actionStatus: 'pending',
        statusCode: 401,
        createdAtMs: 2_000,
      }),
    });

    expect(cutoffs).toEqual({ authenticationAtMs: 0, healthyQuotaAtMs: 0 });
  });

  it('uses credential refresh only as an authentication cutoff', () => {
    const cutoffs = getAccountCredentialEvidenceCutoffs({ credentialRefreshAtMs: 4_000 });

    expect(cutoffs).toEqual({ authenticationAtMs: 4_000, healthyQuotaAtMs: 0 });
  });

  it('uses healthy Header quota as authentication and quota recovery evidence', () => {
    const cutoffs = getAccountCredentialEvidenceCutoffs({
      headerQuota: providerQuota({
        windows: [
          {
            id: 'weekly',
            label: 'Weekly',
            usedPercent: 30,
            resetLabel: 'later',
            observedAtMs: 4_000,
          },
        ],
        quotaInventoryObserved: false,
        fetchedAtMs: undefined,
        observedAtMs: 4_000,
        observedFromUsageHeaders: true,
      }),
    });

    expect(cutoffs).toEqual({ authenticationAtMs: 4_000, healthyQuotaAtMs: 4_000 });
  });

  it('uses explicit Header quota-limit evidence only as authentication recovery', () => {
    const cutoffs = getAccountCredentialEvidenceCutoffs({
      headerQuota: providerQuota({
        fetchedAtMs: undefined,
        observedAtMs: 4_000,
        observedFromUsageHeaders: true,
        observedErrorKind: 'rate_limit',
        observedErrorCode: 'usage_limit_reached',
      }),
    });

    expect(cutoffs).toEqual({ authenticationAtMs: 4_000, healthyQuotaAtMs: 0 });
  });

  it('does not let a plan-only Header snapshot clear reauth evidence', () => {
    const cutoffs = getAccountCredentialEvidenceCutoffs({
      headerQuota: providerQuota({
        windows: [],
        quotaInventoryObserved: false,
        planType: 'plus',
        fetchedAtMs: undefined,
        observedAtMs: 4_000,
        observedFromUsageHeaders: true,
      }),
    });

    expect(cutoffs).toEqual({ authenticationAtMs: 0, healthyQuotaAtMs: 0 });
  });

  it.each([
    { planType: 'plus' },
    { observedErrorKind: 'rate_limit', observedErrorCode: 'retry_after' },
    { observedErrorKind: 'upstream_error', observedErrorCode: 'bad_gateway' },
  ])('keeps an older 401 ahead of weak Header evidence: %o', (headerMetadata) => {
    const result = reconcileCodexQuotaEvidence({
      inspectionQuota: buildInspectionCodexQuotaState(
        file,
        inspection({
          action: 'reauth',
          actionStatus: 'pending',
          statusCode: 401,
          usedPercent: null,
          quotaWindows: [],
          createdAtMs: 2_000,
        })
      ),
      headerQuota: providerQuota({
        windows: [],
        quotaInventoryObserved: false,
        fetchedAtMs: undefined,
        observedAtMs: 4_000,
        observedFromUsageHeaders: true,
        ...headerMetadata,
      }),
    });

    expect(result).toMatchObject({ status: 'error', errorStatus: 401, failedAtMs: 2_000 });
  });

  it('does not let generic Header errors clear reauth when quota metadata is also present', () => {
    const result = reconcileCodexQuotaEvidence({
      inspectionQuota: buildInspectionCodexQuotaState(
        file,
        inspection({
          action: 'reauth',
          actionStatus: 'pending',
          statusCode: 401,
          usedPercent: null,
          quotaWindows: [],
          createdAtMs: 2_000,
        })
      ),
      headerQuota: providerQuota({
        windows: [
          {
            id: 'weekly',
            label: 'Weekly',
            usedPercent: 30,
            resetLabel: 'later',
            observedAtMs: 4_000,
          },
        ],
        fetchedAtMs: undefined,
        observedAtMs: 4_000,
        observedFromUsageHeaders: true,
        observedErrorKind: 'upstream_error',
        observedErrorCode: 'bad_gateway',
      }),
    });

    expect(result).toMatchObject({ status: 'error', errorStatus: 401, failedAtMs: 2_000 });
  });

  it.each([
    { observedErrorKind: 'auth_invalid', observedErrorCode: 'token_expired' },
    { observedErrorKind: 'upstream_error', observedErrorCode: 'bad_gateway' },
  ])('does not let Header error metadata clear reauth evidence: $observedErrorKind', (error) => {
    const cutoffs = getAccountCredentialEvidenceCutoffs({
      headerQuota: providerQuota({
        windows: [],
        quotaInventoryObserved: false,
        planType: 'plus',
        fetchedAtMs: undefined,
        observedAtMs: 4_000,
        observedFromUsageHeaders: true,
        ...error,
      }),
    });

    expect(cutoffs).toEqual({ authenticationAtMs: 0, healthyQuotaAtMs: 0 });
  });
});
