import { create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { emptyCodexSubscriptionExtras } from '@/features/accounts/model/codexSubscription';
import type { CodexSubscriptionEntry, CodexSubscriptionRecord } from '@/features/accounts/model/codexSubscription';
import { formatQuotaResetTimestamp } from '@/features/accounts/model/accountsPagePresentation';
import { AccountSubscriptionTab } from './AccountSubscriptionTab';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string, options?: Record<string, unknown>) => {
        if (options && 'days' in options) return `${key}:${String(options.days)}`;
        if (options && 'kind' in options) return `${key}:${String(options.kind)}`;
        return key;
      },
      i18n: { language: 'en-US' },
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

const treeText = (renderer: ReactTestRenderer): string => readText(renderer.toJSON());

const makeRecord = (overrides: Partial<CodexSubscriptionRecord> = {}): CodexSubscriptionRecord => ({
  accountId: 'acct_pro',
  planType: 'pro',
  activeStartMs: Date.parse('2026-05-10T00:00:00Z'),
  activeUntilMs: Date.parse('2026-06-10T00:00:00Z'),
  billingPeriod: 'monthly',
  willRenew: true,
  fetchedAtMs: 1_700_000_000_000,
  source: 'subscriptions',
  extras: emptyCodexSubscriptionExtras(),
  ...overrides,
});

const renderTab = (
  entry: CodexSubscriptionEntry,
  overrides: { missingAccountId?: boolean; refreshing?: boolean } = {}
) =>
  create(
    <AccountSubscriptionTab
      entry={entry}
      missingAccountId={overrides.missingAccountId ?? false}
      refreshing={overrides.refreshing ?? false}
      onRefresh={() => undefined}
    />
  );

describe('AccountSubscriptionTab', () => {
  it('renders Quota-style summary cards and a styled secondary grid when ready', () => {
    const untilMs = Date.parse('2099-06-10T00:00:00Z');
    const renderer = renderTab({
      status: 'ready',
      record: makeRecord({
        planType: 'pro',
        activeUntilMs: untilMs,
      }),
    });
    const text = treeText(renderer);
    const summary = renderer.root.findByProps({ 'data-account-subscription-summary': 'true' });
    const details = renderer.root.findByProps({ 'data-account-subscription-details': 'true' });
    const metrics = renderer.root.findAllByProps({ 'data-account-subscription-metric': 'planType' });

    expect(summary.props.className).toContain('quotaSummaryPanel');
    expect(details.findByType('dl').props.className).toContain('overviewFieldGrid');
    expect(metrics).toHaveLength(1);
    expect(text).toContain('Pro 20x');
    expect(text).not.toMatch(/(^|[^A-Za-z])pro([^A-Za-z]|$)/);
    expect(text).toContain(formatQuotaResetTimestamp(untilMs, 'en-US'));
    expect(text).toContain('accounts.list_plan_remaining_days');
    expect(text).toContain('common.yes');
    expect(text).toContain('accounts.detail_subscription_billing_monthly');
    expect(renderer.root.findAllByProps({ 'data-account-subscription-delinquent': 'true' })).toHaveLength(
      0
    );
  });

  it('surfaces delinquent state with the existing danger attention treatment', () => {
    const renderer = renderTab({
      status: 'ready',
      record: makeRecord({
        extras: {
          ...emptyCodexSubscriptionExtras(),
          isDelinquent: true,
        },
      }),
    });
    const banner = renderer.root.findByProps({ 'data-account-subscription-delinquent': 'true' });
    const delinquentField = renderer.root.findByProps({ 'data-subscription-field': 'isDelinquent' });

    expect(banner.props.className).toContain('overviewAttentionCard');
    expect(banner.props['data-overview-attention-priority']).toBe('high');
    expect(treeText(renderer)).toContain('accounts.detail_subscription_delinquent_notice');
    expect(readText(delinquentField.props.children)).toContain('common.yes');
  });

  it('keeps loading, empty, missing, and soft-failed states on native status surfaces', () => {
    const loading = renderTab({
      status: 'loading',
      accountId: 'acct_pro',
      inflightSinceMs: 1,
    });
    expect(treeText(loading)).toContain('accounts.detail_subscription_loading');

    const idle = renderTab({ status: 'idle' });
    expect(treeText(idle)).toContain('accounts.detail_subscription_empty');

    const missing = renderTab({ status: 'idle' }, { missingAccountId: true });
    expect(treeText(missing)).toContain('accounts.detail_subscription_missing_account');
    expect(treeText(missing)).not.toContain('accounts.detail_subscription_empty');

    const failed = renderTab({
      status: 'soft_failed',
      accountId: 'acct_pro',
      failedAtMs: 1,
      errorKind: 'http',
    });
    const errorBox = failed.root.find(
      (node) => typeof node.props.className === 'string' && node.props.className.includes('errorBox')
    );
    expect(readText(errorBox.props.children)).toContain('accounts.detail_subscription_soft_failed:http');
  });
});
