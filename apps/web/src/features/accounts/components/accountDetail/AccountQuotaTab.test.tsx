import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import type { AccountDetailViewModel } from '@/features/accounts/model/accountDetailViewModel';
import { AccountQuotaTab } from './AccountQuotaTab';

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? key,
      i18n: { language: 'en-US' },
    }),
  };
});

const detailView = {
  identity: { provider: 'example-plugin' },
  quota: {
    windows: [],
    cooldown: null,
    resetCreditsAvailableCount: null,
    resetCreditExpiries: [],
    plugin: {
      availability: 'available',
      stale: false,
      observedAtMs: Date.parse('2026-08-28T11:55:00Z'),
      currency: 'JPY',
      minorUnit: 0,
      windows: [],
      spend: {
        meteredMinorUnits: 98655,
        todayMinorUnits: 458,
        periodMinorUnits: 124717,
        latestTokens: 1901,
        periodTokens: 245002,
      },
      daily: [
        { date: '2026-08-27', costMinorUnits: 19800, tokens: 12600 },
        { date: '2026-08-28', costMinorUnits: 458, tokens: 1901 },
      ],
      topModel: 'example-model',
      provenance: ['usage_summary', 'usage_events'],
    },
  },
  history: {
    totalRequests: 12,
    totalTokens: 3456,
    totalCost: 4.5,
    successRate: 99.5,
    firstSeenMs: Date.parse('2026-08-27T00:00:00Z'),
    lastSeenMs: Date.parse('2026-08-27T01:00:00Z'),
  },
} as unknown as AccountDetailViewModel;

const render = async (view: AccountDetailViewModel = detailView) => {
  let renderer: ReactTestRenderer;
  await act(async () => {
    renderer = create(
      <AccountQuotaTab
        detailView={view}
        windowUsageError=""
        historyAvailable={false}
        historyRefreshing={false}
        onRefreshHistory={() => undefined}
        onResetQuota={() => undefined}
        resetQuotaDisabled
      />
    );
  });
  return renderer!;
};

const text = (value: unknown): string =>
  Array.isArray(value)
    ? value.map(text).join('')
    : typeof value === 'string' || typeof value === 'number'
      ? String(value)
      : value && typeof value === 'object' && 'children' in value
        ? text((value as { children?: unknown }).children)
        : '';

describe('AccountQuotaTab plugin quota details', () => {
  it('adds generic plugin usage without replacing native usage metrics', async () => {
    const renderer = await render();
    const renderedText = text(renderer.toJSON());
    const pluginPanelText = text(
      renderer.root.findByProps({ 'data-account-plugin-quota': 'true' }).children
    );

    expect(renderedText).toContain('accounts.detail_total_requests');
    expect(renderedText).toContain('accounts.detail_total_tokens');
    expect(renderedText).toContain('accounts.detail_total_cost');
    expect(renderedText).toContain('accounts.detail_success_rate');
    expect(renderedText).toContain('Metered spend');
    expect(renderedText).toContain('JPY');
    expect(pluginPanelText).not.toContain('$');
    expect(pluginPanelText).not.toContain('Cursor');
    expect(renderedText.indexOf('accounts.detail_success_rate')).toBeLessThan(
      renderedText.indexOf('Metered spend')
    );
    expect(renderer.root.findAllByProps({ 'data-account-plugin-quota': 'true' })).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({ 'data-account-plugin-quota-provenance': 'true' })
    ).toHaveLength(1);
  });

  it('renders the daily histogram as visible semantic list content', async () => {
    const renderer = await render();
    const chart = renderer.root.findByProps({ 'data-account-quota-daily-chart': 'true' });

    expect(chart.type).toBe('ul');
    expect(chart.props.role).toBeUndefined();
    expect(chart.props['aria-label']).toBeUndefined();
    expect(renderer.root.findAllByProps({ 'data-account-quota-daily-bar': 'true' })).toHaveLength(
      2
    );
    expect(text(renderer.toJSON())).toContain('2026-08-28');
    expect(text(renderer.toJSON())).toContain('458 JPY');
    expect(text(renderer.toJSON())).toContain('1,901 accounts.detail_usage_tokens');
    expect(text(renderer.toJSON())).not.toContain('1,901 tokens');
  });

  it('renders daily raw minor units when spend and money metadata are absent', async () => {
    const dailyOnlyView = {
      ...detailView,
      quota: {
        ...detailView.quota,
        plugin: {
          ...detailView.quota.plugin,
          currency: null,
          minorUnit: null,
          spend: null,
          daily: [{ date: '2026-08-28', costMinorUnits: 458, tokens: 1901 }],
        },
      },
    } as AccountDetailViewModel;
    const renderer = await render(dailyOnlyView);

    expect(text(renderer.toJSON())).toContain('458 minor units');
    expect(renderer.root.findAllByProps({ 'data-account-quota-daily-bar': 'true' })).toHaveLength(
      1
    );
  });

  it('renders unavailable and stale state while retaining native metrics', async () => {
    const staleView = {
      ...detailView,
      quota: {
        ...detailView.quota,
        plugin: {
          ...detailView.quota.plugin,
          spend: null,
          daily: [],
          topModel: null,
          provenance: [],
          availability: 'unavailable',
          stale: true,
        },
      },
    } as AccountDetailViewModel;
    const renderer = await render(staleView);
    const renderedText = text(renderer.toJSON());

    expect(renderedText).toContain('accounts.detail_quota_snapshot_stale');
    expect(renderedText).toContain('accounts.detail_total_requests');
    const state = renderer.root.findByProps({ 'data-account-plugin-quota-state': 'true' });
    expect(state.props.role).toBeUndefined();
  });
});
