import { describe, expect, it } from 'vitest';
import {
  parseSubscriptionsResponse,
  resolveSubscriptionUntilMs,
} from './parseSubscriptionsResponse';

const FETCHED_AT = 1_700_000_000_000;

describe('parseSubscriptionsResponse', () => {
  it('parses the ChatGPT subscriptions core fields', () => {
    const record = parseSubscriptionsResponse(
      {
        plan_type: 'plus',
        active_start: '2026-05-10T02:52:15Z',
        active_until: '2026-06-10T02:52:15Z',
        billing_period: 'monthly',
        will_renew: true,
        account_id: 'acc_123',
      },
      'acc_123',
      FETCHED_AT
    );

    expect(record).toMatchObject({
      accountId: 'acc_123',
      planType: 'plus',
      activeStartMs: Date.parse('2026-05-10T02:52:15Z'),
      activeUntilMs: Date.parse('2026-06-10T02:52:15Z'),
      billingPeriod: 'monthly',
      willRenew: true,
      fetchedAtMs: FETCHED_AT,
      source: 'subscriptions',
    });
    expect(resolveSubscriptionUntilMs(record)).toBe(Date.parse('2026-06-10T02:52:15Z'));
  });

  it('accepts camelCase aliases and nested subscription wrappers', () => {
    const record = parseSubscriptionsResponse(
      {
        subscription: {
          planType: 'pro',
          activeUntil: 1_788_220_799,
          willRenew: false,
        },
      },
      'acc_nested',
      FETCHED_AT
    );

    expect(record).toMatchObject({
      accountId: 'acc_nested',
      planType: 'pro',
      activeUntilMs: 1_788_220_799_000,
      willRenew: false,
    });
  });

  it('selects the matching account from an array payload', () => {
    const record = parseSubscriptionsResponse(
      [
        { account_id: 'other', plan_type: 'free', active_until: '2026-01-01T00:00:00Z' },
        { account_id: 'acc_match', plan_type: 'plus', active_until: '2026-08-01T00:00:00Z' },
      ],
      'acc_match',
      FETCHED_AT
    );

    expect(record?.accountId).toBe('acc_match');
    expect(record?.planType).toBe('plus');
    expect(record?.activeUntilMs).toBe(Date.parse('2026-08-01T00:00:00Z'));
  });

  it('parses optional extras only when present', () => {
    const record = parseSubscriptionsResponse(
      {
        plan_type: 'team',
        seats_in_use: 2,
        seats_entitled: 5,
        is_delinquent: true,
        grace_period_end_timestamp: '2026-07-01T00:00:00Z',
        discount: { label: 'nonprofit' },
      },
      'acc_team',
      FETCHED_AT
    );

    expect(record?.extras).toEqual({
      seatsInUse: 2,
      seatsEntitled: 5,
      isDelinquent: true,
      gracePeriodEndMs: Date.parse('2026-07-01T00:00:00Z'),
      discountLabel: 'nonprofit',
    });
  });

  it('returns null for empty or unrelated payloads', () => {
    expect(parseSubscriptionsResponse({}, 'acc_123', FETCHED_AT)).toBeNull();
    expect(parseSubscriptionsResponse(null, 'acc_123', FETCHED_AT)).toBeNull();
    expect(parseSubscriptionsResponse({ error: 'nope' }, 'acc_123', FETCHED_AT)).toBeNull();
    expect(parseSubscriptionsResponse({ plan_type: 'plus' }, '', FETCHED_AT)).toBeNull();
  });
});
