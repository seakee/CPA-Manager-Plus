import { create } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { emptyCodexSubscriptionExtras } from '@/features/accounts/model/codexSubscription';
import { AccountSubscriptionTab } from './AccountSubscriptionTab';

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string, options?: Record<string, unknown>) => {
        if (!options) return key;
        const params = Object.entries(options)
          .filter(([name]) => name !== 'defaultValue')
          .map(([name, value]) => `${name}=${String(value)}`)
          .join(',');
        return params ? `${key}:${params}` : key;
      },
      i18n: { language: 'en' },
    }),
  };
});

const readText = (value: unknown): string => {
  if (typeof value === 'string' || typeof value === 'number') return String(value);
  if (Array.isArray(value)) return value.map(readText).join('');
  if (value && typeof value === 'object' && 'children' in value) {
    return readText((value as { children?: unknown }).children);
  }
  return '';
};

describe('AccountSubscriptionTab', () => {
  it('renders core subscription fields from a ready record', () => {
    const renderer = create(
      <AccountSubscriptionTab
        missingAccountId={false}
        refreshing={false}
        onRefresh={() => undefined}
        entry={{
          status: 'ready',
          record: {
            accountId: 'acct_plus',
            planType: 'plus',
            activeStartMs: Date.parse('2026-05-10T00:00:00Z'),
            activeUntilMs: Date.parse('2026-06-10T00:00:00Z'),
            billingPeriod: 'monthly',
            willRenew: true,
            fetchedAtMs: 1_700_000_000_000,
            source: 'subscriptions',
            extras: emptyCodexSubscriptionExtras(),
          },
        }}
      />
    );

    const text = readText(renderer.toJSON());
    expect(text).toContain('accounts.detail_subscription_plan_type');
    expect(text).toContain('plus');
    expect(text).toContain('accounts.detail_subscription_billing_monthly');
    expect(text).toContain('accounts.detail_subscription_will_renew');
  });

  it('shows a soft-fail message without quota copy', () => {
    const renderer = create(
      <AccountSubscriptionTab
        missingAccountId={false}
        refreshing={false}
        onRefresh={() => undefined}
        entry={{
          status: 'soft_failed',
          accountId: 'acct_plus',
          failedAtMs: 1_700_000_000_000,
          errorKind: 'http',
        }}
      />
    );

    expect(readText(renderer.toJSON())).toContain(
      'accounts.detail_subscription_soft_failed:kind=http'
    );
    expect(readText(renderer.toJSON())).not.toContain('accounts.detail_tab_quota');
  });
});
