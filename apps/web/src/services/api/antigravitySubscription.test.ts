import { describe, expect, it } from 'vitest';
import { parseAntigravitySubscriptionSummary } from './antigravitySubscription';

describe('parseAntigravitySubscriptionSummary', () => {
  it('parses g1-pro-tier as a Pro plan', () => {
    const summary = parseAntigravitySubscriptionSummary({
      currentTier: {
        id: 'free-tier',
        name: 'Antigravity',
      },
      paidTier: {
        id: 'g1-pro-tier',
        name: 'Antigravity Pro',
      },
    });

    expect(summary).toMatchObject({
      plan: 'pro',
      tierId: 'g1-pro-tier',
      tierName: 'Antigravity Pro',
      source: 'paid',
    });
  });

  it('parses free-tier subscription data from a proxied body string', () => {
    const summary = parseAntigravitySubscriptionSummary({
      body: JSON.stringify({
        currentTier: {
          id: 'free-tier',
          name: 'Antigravity',
        },
        paidTier: {
          id: 'free-tier',
          name: 'Antigravity Starter Quota',
        },
      }),
    });

    expect(summary).toMatchObject({
      plan: 'free',
      tierId: 'free-tier',
      tierName: 'Antigravity Starter Quota',
      source: 'paid',
      currentTier: {
        id: 'free-tier',
        name: 'Antigravity',
      },
      paidTier: {
        id: 'free-tier',
        name: 'Antigravity Starter Quota',
      },
    });
  });
});
