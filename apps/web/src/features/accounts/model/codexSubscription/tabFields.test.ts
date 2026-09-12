import { describe, expect, it } from 'vitest';
import { emptyCodexSubscriptionExtras } from './types';
import {
  buildCodexSubscriptionDetailFields,
  buildCodexSubscriptionSecondaryFields,
  buildCodexSubscriptionSummaryFields,
  buildCodexSubscriptionTabFields,
  getCodexSubscriptionRemainingDays,
} from './tabFields';

describe('buildCodexSubscriptionDetailFields', () => {
  it('renders the core paid-Codex subscription fields', () => {
    const fields = buildCodexSubscriptionDetailFields({
      accountId: 'acct_plus',
      planType: 'plus',
      activeStartMs: Date.parse('2026-05-10T00:00:00Z'),
      activeUntilMs: Date.parse('2026-06-10T00:00:00Z'),
      billingPeriod: 'monthly',
      willRenew: true,
      fetchedAtMs: 1_700_000_000_000,
      source: 'subscriptions',
      extras: emptyCodexSubscriptionExtras(),
    });

    expect(fields).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          key: 'planType',
          labelKey: 'accounts.detail_subscription_plan_type',
          value: 'plus',
        }),
        expect.objectContaining({
          key: 'activeUntilMs',
          labelKey: 'accounts.detail_subscription_active_until',
          value: Date.parse('2026-06-10T00:00:00Z'),
        }),
        expect.objectContaining({
          key: 'billingPeriod',
          value: 'accounts.detail_subscription_billing_monthly',
          valueKind: 'i18n',
        }),
        expect.objectContaining({
          key: 'willRenew',
          value: 'common.yes',
          valueKind: 'i18n',
        }),
      ])
    );
    expect(fields.map((field) => field.key)).not.toContain('seats');
  });

  it('adds extras only when they have a meaningful value', () => {
    const fields = buildCodexSubscriptionDetailFields({
      accountId: 'acct_team',
      planType: 'team',
      activeStartMs: null,
      activeUntilMs: null,
      billingPeriod: null,
      willRenew: null,
      fetchedAtMs: 1_700_000_000_000,
      source: 'subscriptions',
      extras: {
        seatsInUse: 2,
        seatsEntitled: 5,
        isDelinquent: true,
        gracePeriodEndMs: Date.parse('2026-07-01T00:00:00Z'),
        discountLabel: 'nonprofit',
      },
    });

    expect(fields.map((field) => field.key)).toEqual([
      'planType',
      'seats',
      'isDelinquent',
      'gracePeriodEndMs',
      'discount',
      'fetchedAtMs',
    ]);
  });
});

describe('buildCodexSubscriptionSummaryFields', () => {
  it('always returns the four primary metric fields', () => {
    const fields = buildCodexSubscriptionSummaryFields({
      accountId: 'acct_empty',
      planType: null,
      activeStartMs: null,
      activeUntilMs: null,
      billingPeriod: null,
      willRenew: null,
      fetchedAtMs: 1_700_000_000_000,
      source: 'subscriptions',
      extras: emptyCodexSubscriptionExtras(),
    });

    expect(fields.map((field) => field.key)).toEqual([
      'planType',
      'activeUntilMs',
      'billingPeriod',
      'willRenew',
    ]);
    expect(fields.every((field) => field.value === null)).toBe(true);
  });
});

describe('buildCodexSubscriptionSecondaryFields', () => {
  it('keeps start date, extras, and fetched-at out of the summary cards', () => {
    const fields = buildCodexSubscriptionSecondaryFields({
      accountId: 'acct_plus',
      planType: 'plus',
      activeStartMs: Date.parse('2026-05-10T00:00:00Z'),
      activeUntilMs: Date.parse('2026-06-10T00:00:00Z'),
      billingPeriod: 'monthly',
      willRenew: true,
      fetchedAtMs: 1_700_000_000_000,
      source: 'subscriptions',
      extras: {
        seatsInUse: 2,
        seatsEntitled: 5,
        isDelinquent: true,
        gracePeriodEndMs: Date.parse('2026-07-01T00:00:00Z'),
        discountLabel: 'nonprofit',
      },
    });

    expect(fields.map((field) => field.key)).toEqual([
      'activeStartMs',
      'seats',
      'isDelinquent',
      'gracePeriodEndMs',
      'discount',
      'fetchedAtMs',
    ]);
  });
});

describe('getCodexSubscriptionRemainingDays', () => {
  const nowMs = Date.parse('2026-05-18T00:00:00Z');

  it('uses the ceil-day rule and never returns zero for a future until', () => {
    expect(getCodexSubscriptionRemainingDays(nowMs + 1, nowMs)).toBe(1);
    expect(getCodexSubscriptionRemainingDays(nowMs + 23 * 86_400_000, nowMs)).toBe(23);
  });

  it('returns null when until is missing or not after now', () => {
    expect(getCodexSubscriptionRemainingDays(null, nowMs)).toBeNull();
    expect(getCodexSubscriptionRemainingDays(nowMs, nowMs)).toBeNull();
    expect(getCodexSubscriptionRemainingDays(nowMs - 1, nowMs)).toBeNull();
  });
});

describe('buildCodexSubscriptionTabFields', () => {
  it('returns no fields unless the entry is ready', () => {
    expect(
      buildCodexSubscriptionTabFields({
        status: 'soft_failed',
        accountId: 'acct_plus',
        failedAtMs: 1,
        errorKind: 'http',
      })
    ).toEqual([]);
    expect(buildCodexSubscriptionTabFields({ status: 'idle' })).toEqual([]);
  });
});
