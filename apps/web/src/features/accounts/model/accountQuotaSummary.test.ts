import { describe, expect, it } from 'vitest';
import type { AuthFileItem, CodexQuotaState, XaiBillingSummary } from '@/types';
import { getAuthFileSelectionKey } from '@/features/authFiles/model/credentialStatus';
import { CODEX_SPARK_MODEL_ID } from '@/utils/quota/codexQuota';
import { buildQuotaCredentialIdentity } from '@/utils/quota/credentialScope';
import {
  hasConfirmedXaiBillingEntitlement,
  resolveAccountQuota,
  type AccountQuotaStores,
} from './accountQuotaSummary';

const emptyStores = (): AccountQuotaStores => ({
  antigravityQuota: {},
  claudeQuota: {},
  codexQuota: {},
  devinQuota: {},
  kimiQuota: {},
  xaiQuota: {},
});

const makeXaiBilling = (overrides: Partial<XaiBillingSummary> = {}): XaiBillingSummary => ({
  periodType: 'weekly',
  usagePercent: null,
  productUsage: [],
  monthlyLimitCents: null,
  usedCents: null,
  includedUsedCents: null,
  onDemandCapCents: null,
  onDemandUsedCents: null,
  onDemandUsedPercent: null,
  usedPercent: null,
  ...overrides,
});

describe('resolveAccountQuota', () => {
  it.each([
    {
      label: 'weekly current-period data',
      billing: makeXaiBilling({
        usagePercent: 42,
        periodStart: '2026-09-05T00:00:00Z',
        periodEnd: '2026-09-12T00:00:00Z',
      }),
    },
    {
      label: 'legacy monthly data without a positive limit',
      billing: makeXaiBilling({
        periodType: 'monthly',
        usedPercent: 20,
        usedCents: 2_000,
        includedUsedCents: 2_000,
        billingPeriodEnd: '2026-10-01T00:00:00Z',
      }),
    },
  ])('does not expose $label as xAI account quota', ({ billing }) => {
    const file = { name: 'xai.json', type: 'xai' } as AuthFileItem;
    const stores = emptyStores();
    stores.xaiQuota[file.name] = {
      ...buildQuotaCredentialIdentity(file),
      status: 'success',
      billing,
    };

    expect(resolveAccountQuota(file, stores)).toMatchObject({
      status: 'unknown',
      remainingPercent: null,
      usedPercent: null,
      resetLabel: '-',
      resetAtMs: null,
    });
  });

  it('does not expose billing quota for an explicitly Free xAI plan', () => {
    const file = { name: 'xai-free.json', type: 'xai', planType: 'free' } as AuthFileItem;
    const stores = emptyStores();
    stores.xaiQuota[file.name] = {
      ...buildQuotaCredentialIdentity(file),
      status: 'success',
      billing: makeXaiBilling({
        periodType: 'monthly',
        monthlyLimitCents: 10_000,
        usedCents: 2_000,
        includedUsedCents: 2_000,
        usedPercent: 20,
        billingPeriodEnd: '2026-10-01T00:00:00Z',
      }),
    };

    expect(resolveAccountQuota(file, stores)).toMatchObject({
      status: 'unknown',
      remainingPercent: null,
      usedPercent: null,
    });
  });

  it('keeps confirmed paid xAI billing quota available', () => {
    const file = { name: 'xai-paid.json', type: 'xai', planType: 'SuperGrok' } as AuthFileItem;
    const stores = emptyStores();
    stores.xaiQuota[file.name] = {
      ...buildQuotaCredentialIdentity(file),
      status: 'success',
      billing: makeXaiBilling({
        periodType: 'monthly',
        monthlyLimitCents: 10_000,
        usedCents: 2_000,
        includedUsedCents: 2_000,
        usedPercent: 20,
        billingPeriodEnd: '2026-10-01T00:00:00Z',
      }),
    };

    expect(resolveAccountQuota(file, stores)).toMatchObject({
      status: 'ok',
      remainingPercent: 80,
      usedPercent: 20,
      resetLabel: '2026-10-01T00:00:00Z',
    });
  });

  it('does not expose quota when plan is unknown even if positive limits exist', () => {
    const file = { name: 'xai-unknown.json', type: 'xai' } as AuthFileItem;
    const stores = emptyStores();
    stores.xaiQuota[file.name] = {
      ...buildQuotaCredentialIdentity(file),
      status: 'success',
      billing: makeXaiBilling({
        periodType: 'monthly',
        monthlyLimitCents: 10_000,
        usedCents: 2_000,
        includedUsedCents: 2_000,
        usedPercent: 20,
        billingPeriodEnd: '2026-10-01T00:00:00Z',
      }),
    };

    expect(resolveAccountQuota(file, stores)).toMatchObject({
      status: 'unknown',
      remainingPercent: null,
      usedPercent: null,
    });
  });

  it('does not expose quota when plan is unknown even if positive on-demand limit exists', () => {
    const file = { name: 'xai-unknown-payg.json', type: 'xai' } as AuthFileItem;
    const stores = emptyStores();
    stores.xaiQuota[file.name] = {
      ...buildQuotaCredentialIdentity(file),
      status: 'success',
      billing: makeXaiBilling({
        periodType: 'monthly',
        monthlyLimitCents: null,
        onDemandCapCents: 5_000,
        onDemandUsedCents: 2_500,
        onDemandUsedPercent: 50,
        billingPeriodEnd: '2026-10-01T00:00:00Z',
      }),
    };

    expect(resolveAccountQuota(file, stores)).toMatchObject({
      status: 'unknown',
      remainingPercent: null,
      usedPercent: null,
    });
  });

  it('keeps account quota summary fail-closed unknown for unconfirmed plan even with valid weekly observation (issue #744)', () => {
    const file = { name: 'xai-issue-744.json', type: 'xai', planType: null } as AuthFileItem;
    const stores = emptyStores();
    stores.xaiQuota[file.name] = {
      ...buildQuotaCredentialIdentity(file),
      status: 'success',
      billing: makeXaiBilling({
        periodType: 'weekly',
        usagePercent: 2.0,
        periodStart: '2026-09-11T13:42:16.586061+00:00',
        periodEnd: '2026-09-18T13:42:16.586061+00:00',
        productUsage: [{ product: 'GrokBuild', usagePercent: 2.0 }],
        monthlyLimitCents: 0,
        usedCents: 0,
        includedUsedCents: 0,
        onDemandCapCents: 0,
        onDemandUsedCents: 0,
        onDemandUsedPercent: null,
        billingPeriodEnd: '2026-10-01T00:00:00Z',
        usedPercent: 0,
      }),
    };

    expect(resolveAccountQuota(file, stores)).toMatchObject({
      status: 'unknown',
      remainingPercent: null,
      usedPercent: null,
      planType: null,
    });
  });

  it('does not expose billing quota for an explicitly Free xAI plan even with positive on-demand cap', () => {
    const file = { name: 'xai-free-payg.json', type: 'xai', planType: 'free' } as AuthFileItem;
    const stores = emptyStores();
    stores.xaiQuota[file.name] = {
      ...buildQuotaCredentialIdentity(file),
      status: 'success',
      billing: makeXaiBilling({
        periodType: 'weekly',
        onDemandCapCents: 5_000,
        onDemandUsedCents: 1_000,
        onDemandUsedPercent: 20,
        billingPeriodEnd: '2026-10-01T00:00:00Z',
      }),
    };

    expect(resolveAccountQuota(file, stores)).toMatchObject({
      status: 'unknown',
      remainingPercent: null,
      usedPercent: null,
    });
  });

  it('does not expose quota when plan is unknown even if positive on-demand cap exists', () => {
    const file = { name: 'xai-unknown-payg.json', type: 'xai' } as AuthFileItem;
    const stores = emptyStores();
    stores.xaiQuota[file.name] = {
      ...buildQuotaCredentialIdentity(file),
      status: 'success',
      billing: makeXaiBilling({
        periodType: 'weekly',
        onDemandCapCents: 5_000,
        onDemandUsedCents: 1_000,
        onDemandUsedPercent: 20,
        billingPeriodEnd: '2026-10-01T00:00:00Z',
      }),
    };

    expect(resolveAccountQuota(file, stores)).toMatchObject({
      status: 'unknown',
      remainingPercent: null,
      usedPercent: null,
    });
  });

  it('exposes pay-as-you-go quota for confirmed paid plan', () => {
    const file = {
      name: 'xai-paid-payg.json',
      type: 'xai',
      planType: 'SuperGrok',
    } as AuthFileItem;
    const stores = emptyStores();
    stores.xaiQuota[file.name] = {
      ...buildQuotaCredentialIdentity(file),
      status: 'success',
      billing: makeXaiBilling({
        periodType: 'weekly',
        onDemandCapCents: 5_000,
        onDemandUsedCents: 1_000,
        onDemandUsedPercent: 20,
        billingPeriodEnd: '2026-10-01T00:00:00Z',
      }),
    };

    expect(resolveAccountQuota(file, stores)).toMatchObject({
      status: 'ok',
      remainingPercent: 80,
      usedPercent: 20,
      resetLabel: '2026-10-01T00:00:00Z',
      planType: 'SuperGrok',
    });
  });

  it('exposes weekly quota for confirmed paid SuperGrok plan without legacy monthly limits', () => {
    const file = {
      name: 'xai-supergrok.json',
      type: 'xai',
      planType: 'SuperGrok',
    } as AuthFileItem;
    const stores = emptyStores();
    stores.xaiQuota[file.name] = {
      ...buildQuotaCredentialIdentity(file),
      status: 'success',
      billing: makeXaiBilling({
        periodType: 'weekly',
        usagePercent: 42,
        periodStart: '2026-09-05T00:00:00Z',
        periodEnd: '2026-09-12T00:00:00Z',
        monthlyLimitCents: null,
        onDemandCapCents: null,
      }),
    };

    expect(resolveAccountQuota(file, stores)).toMatchObject({
      status: 'ok',
      remainingPercent: 58,
      usedPercent: 42,
      resetLabel: '2026-09-12T00:00:00Z',
      planType: 'SuperGrok',
    });
  });

  it.each([
    { planType: 'SuperGrok Heavy', label: 'SuperGrok Heavy' },
    { planType: 'X Premium', label: 'X Premium' },
    { planType: 'Premium+', label: 'Premium+' },
  ])('exposes quota for confirmed paid plan $label without legacy limits', ({ planType }) => {
    const file = {
      name: `xai-${planType}.json`,
      type: 'xai',
      planType,
    } as AuthFileItem;
    const stores = emptyStores();
    stores.xaiQuota[file.name] = {
      ...buildQuotaCredentialIdentity(file),
      status: 'success',
      billing: makeXaiBilling({
        periodType: 'weekly',
        usagePercent: 30,
        periodStart: '2026-09-05T00:00:00Z',
        periodEnd: '2026-09-12T00:00:00Z',
        monthlyLimitCents: null,
        onDemandCapCents: null,
      }),
    };

    expect(resolveAccountQuota(file, stores)).toMatchObject({
      status: 'ok',
      remainingPercent: 70,
      usedPercent: 30,
      resetLabel: '2026-09-12T00:00:00Z',
      planType,
    });
  });

  it.each(['Free', 'Free Tier', 'free-tier', 'free_tier'])(
    'does not expose billing quota for %s xAI plan even with weekly usage and limits null',
    (planType) => {
      const file = { name: 'xai-free.json', type: 'xai', planType } as AuthFileItem;
      const stores = emptyStores();
      stores.xaiQuota[file.name] = {
        ...buildQuotaCredentialIdentity(file),
        status: 'success',
        billing: makeXaiBilling({
          periodType: 'weekly',
          usagePercent: 42,
          periodStart: '2026-09-05T00:00:00Z',
          periodEnd: '2026-09-12T00:00:00Z',
          monthlyLimitCents: null,
          onDemandCapCents: null,
        }),
      };

      expect(resolveAccountQuota(file, stores)).toMatchObject({
        status: 'unknown',
        remainingPercent: null,
        usedPercent: null,
      });
    }
  );

  it('keeps the account summary on Codex Main when Spark is more constrained', () => {
    const file = {
      name: 'codex.json',
      type: 'codex',
      authIndex: 'auth-1',
    } as AuthFileItem;
    const quota: CodexQuotaState = {
      status: 'success',
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 36,
          resetLabel: 'main-reset',
          modelScope: { kind: 'family', key: 'codex_main', complete: true },
        },
        {
          id: 'spark-weekly-0',
          label: 'Spark Weekly',
          usedPercent: 95,
          resetLabel: 'spark-reset',
          modelScope: {
            kind: 'models',
            models: [CODEX_SPARK_MODEL_ID],
            complete: true,
          },
        },
      ],
    };

    const summary = resolveAccountQuota(file, emptyStores(), {
      codexQuotaBySelectionKey: new Map([[getAuthFileSelectionKey(file), quota]]),
    });

    expect(summary).toMatchObject({
      usedPercent: 36,
      remainingPercent: 64,
      resetLabel: 'main-reset',
    });
  });

  it('does not treat a scoped Header observation as fresh account-wide quota evidence', () => {
    const file = {
      name: 'codex.json',
      type: 'codex',
      authIndex: 'auth-1',
    } as AuthFileItem;
    const quota: CodexQuotaState = {
      status: 'success',
      fetchedAtMs: 1_000,
      observedAtMs: 2_000,
      observedFromUsageHeaders: true,
      observedModelScope: {
        kind: 'models',
        models: [CODEX_SPARK_MODEL_ID],
        complete: true,
      },
      observedTraceId: 'spark-trace',
      activeLimit: 'main',
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 36,
          resetLabel: 'main-reset',
          modelScope: { kind: 'family', key: 'codex_main', complete: true },
        },
        {
          id: 'spark-weekly-0',
          label: 'Spark Weekly',
          usedPercent: 0,
          resetLabel: 'spark-reset',
          modelScope: {
            kind: 'models',
            models: [CODEX_SPARK_MODEL_ID],
            complete: true,
          },
        },
      ],
    };

    const summary = resolveAccountQuota(file, emptyStores(), {
      codexQuotaBySelectionKey: new Map([[getAuthFileSelectionKey(file), quota]]),
    });

    expect(summary).toMatchObject({
      source: 'cache',
      fetchedAtMs: 1_000,
      observedAtMs: 2_000,
      observedTraceId: 'spark-trace',
      activeLimit: 'main',
      usedPercent: 36,
      remainingPercent: 64,
    });
    expect(summary.observedQuotaAtMs).toBeUndefined();
  });

  it('uses Antigravity tier metadata when the stored plan is unknown', () => {
    const file = {
      name: 'antigravity.json',
      type: 'antigravity',
      authIndex: 'auth-1',
      planType: 'unknown',
    } as AuthFileItem;
    const stores = emptyStores();
    stores.antigravityQuota[file.name] = {
      ...buildQuotaCredentialIdentity(file),
      status: 'success',
      groups: [],
      subscription: {
        plan: 'unknown',
        tierName: 'Antigravity Future',
        tierId: 'future-tier',
      },
    };

    expect(resolveAccountQuota(file, stores).planType).toBe('Antigravity Future');
  });

  it('resolves Devin quota summary choosing the limiting window and preserving live plan', () => {
    const file = { name: 'devin.json', type: 'devin', authIndex: 'd-1' };
    const stores = emptyStores();
    stores.devinQuota['devin.json::d-1'] = {
      status: 'success',
      authFileKey: 'devin.json::d-1',
      authFileName: 'devin.json',
      authIndex: 'd-1',
      authFileIdentityVerified: true,
      windows: [
        { id: 'daily', remainingPercent: 0, resetAtMs: 1726400000000, periodHours: 24 },
        { id: 'weekly', remainingPercent: 80, resetAtMs: 1726900000000, periodHours: 168 },
      ],
      plan: 'Pro',
      planStartMs: 1726000000000,
      planEndMs: 1727000000000,
      observedAtMs: 1726000000100,
      fetchedAtMs: 1726000000100,
    };

    const exhaustedSummary = resolveAccountQuota(file, stores);
    expect(exhaustedSummary.status).toBe('exhausted');
    expect(exhaustedSummary.remainingPercent).toBe(0);
    expect(exhaustedSummary.usedPercent).toBe(100);
    expect(exhaustedSummary.planType).toBe('Pro');
    expect(exhaustedSummary.resetAccuracy).toBe('exact');

    // daily = 54, weekly = 77 -> summary = 54
    stores.devinQuota['devin.json::d-1'].windows[0].remainingPercent = 54;
    stores.devinQuota['devin.json::d-1'].windows[1].remainingPercent = 77;

    const activeSummary = resolveAccountQuota(file, stores);
    expect(activeSummary.status).toBe('ok');
    expect(activeSummary.remainingPercent).toBe(54);
    expect(activeSummary.usedPercent).toBe(46);
    expect(activeSummary.planType).toBe('Pro');

    // daily = 80, weekly = 35 -> summary = 35 (limiting window = min(daily, weekly))
    stores.devinQuota['devin.json::d-1'].windows[0].remainingPercent = 80;
    stores.devinQuota['devin.json::d-1'].windows[1].remainingPercent = 35;

    const reverseSummary = resolveAccountQuota(file, stores);
    expect(reverseSummary.status).toBe('ok');
    expect(reverseSummary.remainingPercent).toBe(35);
    expect(reverseSummary.usedPercent).toBe(65);
    expect(reverseSummary.planType).toBe('Pro');
  });
});

describe('hasConfirmedXaiBillingEntitlement', () => {
  it('returns false when billing is null or undefined', () => {
    expect(hasConfirmedXaiBillingEntitlement(null, 'SuperGrok')).toBe(false);
    expect(hasConfirmedXaiBillingEntitlement(undefined, 'SuperGrok')).toBe(false);
  });

  it('returns false for explicit Free plans regardless of limits', () => {
    const withLimit = makeXaiBilling({ monthlyLimitCents: 10_000 });
    const withoutLimit = makeXaiBilling();
    for (const plan of ['free', 'Free', 'Free Tier', 'free-tier', 'free_tier', 'xaifree']) {
      expect(hasConfirmedXaiBillingEntitlement(withLimit, plan)).toBe(false);
      expect(hasConfirmedXaiBillingEntitlement(withoutLimit, plan)).toBe(false);
    }
  });

  it('returns true for confirmed paid plans even when limits are null', () => {
    const withoutLimit = makeXaiBilling({
      monthlyLimitCents: null,
      onDemandCapCents: null,
    });
    for (const plan of [
      'SuperGrok',
      'SuperGrok Heavy',
      'supergrok-heavy',
      'Super Grok',
      'X Premium',
      'x-premium',
      'X Premium+',
      'Premium+',
      'Premium',
    ]) {
      expect(hasConfirmedXaiBillingEntitlement(withoutLimit, plan)).toBe(true);
    }
  });

  it('returns false when plan is unknown and limits are absent', () => {
    const withoutLimit = makeXaiBilling({
      monthlyLimitCents: null,
      onDemandCapCents: null,
    });
    expect(hasConfirmedXaiBillingEntitlement(withoutLimit, null)).toBe(false);
    expect(hasConfirmedXaiBillingEntitlement(withoutLimit, undefined)).toBe(false);
    expect(hasConfirmedXaiBillingEntitlement(withoutLimit, '')).toBe(false);
    expect(hasConfirmedXaiBillingEntitlement(withoutLimit, '   ')).toBe(false);
    expect(hasConfirmedXaiBillingEntitlement(withoutLimit, 'unknown')).toBe(false);
  });

  it('returns false when plan is unknown even if positive limits exist', () => {
    const withMonthlyLimit = makeXaiBilling({ monthlyLimitCents: 10_000 });
    const withOnDemandLimit = makeXaiBilling({ onDemandCapCents: 5_000 });
    expect(hasConfirmedXaiBillingEntitlement(withMonthlyLimit, null)).toBe(false);
    expect(hasConfirmedXaiBillingEntitlement(withMonthlyLimit, undefined)).toBe(false);
    expect(hasConfirmedXaiBillingEntitlement(withMonthlyLimit, 'unknown')).toBe(false);
    expect(hasConfirmedXaiBillingEntitlement(withOnDemandLimit, null)).toBe(false);
    expect(hasConfirmedXaiBillingEntitlement(withOnDemandLimit, undefined)).toBe(false);
    expect(hasConfirmedXaiBillingEntitlement(withOnDemandLimit, 'unknown')).toBe(false);
  });
});

