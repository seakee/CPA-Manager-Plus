import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AccountDetailViewModel } from '@/features/accounts/model/accountDetailViewModel';
import { AccountQuotaTab } from './AccountQuotaTab';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string, options?: Record<string, unknown>) => {
        if (key === 'codex_quota.reset_credit_expiry_remaining_less_than_minute') {
          return '<1 min';
        }
        if (key === 'codex_quota.reset_credit_expiry_item') {
          return `Credit ${options?.index ?? ''}`;
        }
        if (!options) return key;
        const params = Object.entries(options)
          .map(([name, value]) => `${name}=${String(value)}`)
          .join(',');
        return `${key}:${params}`;
      },
      i18n: {
        language: 'en',
      },
    }),
  };
});

describe('AccountQuotaTab timer crossing expiry', () => {
  const baseNow = 1_000_000;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(baseNow);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  const makeDetailView = (
    expiresAtMs: number,
    availableCount: number | null = 1
  ): AccountDetailViewModel =>
    ({
      identity: {
        rowKey: 'codex.json\u0000auth-1',
        name: 'codex.json',
        provider: 'codex',
        type: 'codex',
        authIndex: 'auth-1',
        account: 'test@example.com',
      },
      quota: {
        windows: [],
        resetCreditsAvailableCount: availableCount,
        resetCreditExpiries: [{ id: 'credit-1', expiresAtMs }],
        cooldown: null,
      },
      history: null,
    } as unknown as AccountDetailViewModel);

  it('hides expired credit row when fake timer crosses expiry without altering reset records panel existence', () => {
    const expiresAtMs = baseNow + 30_000; // 30 seconds later
    const detailView = makeDetailView(expiresAtMs, 1);

    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <AccountQuotaTab
          detailView={detailView}
          windowUsageError=""
          historyAvailable={false}
          historyRefreshing={false}
          onRefreshHistory={vi.fn()}
          onResetQuota={vi.fn()}
          resetQuotaDisabled={false}
        />
      );
    });

    // 1. Initial render: credit-1 row is visible with "<1 min"
    const expiryElementBefore = renderer.root.findAllByProps({
      'data-quota-reset-credit-expiry': 'credit-1',
    });
    expect(expiryElementBefore).toHaveLength(1);
    expect(expiryElementBefore[0].children.join('')).toContain('<1 min');

    // Reset records panel is visible
    expect(
      renderer.root.findAllByProps({ 'data-account-quota-reset-records': 'true' })
    ).toHaveLength(1);
    expect(
      renderer.root.findByProps({ 'data-quota-reset-action': 'true' })
    ).toBeTruthy();

    // 2. Advance fake timer by 60s (interval fires, nowMs advances to baseNow + 60_000 > expiresAtMs)
    act(() => {
      vi.advanceTimersByTime(60_000);
    });

    // 3. Expiry row should now be gone because nowMs > expiresAtMs
    const expiryElementAfter = renderer.root.findAllByProps({
      'data-quota-reset-credit-expiry': 'credit-1',
    });
    expect(expiryElementAfter).toHaveLength(0);

    // 4. Reset records panel and reset action button remain visible because count is still known (1)
    expect(
      renderer.root.findAllByProps({ 'data-account-quota-reset-records': 'true' })
    ).toHaveLength(1);
    expect(
      renderer.root.findByProps({ 'data-quota-reset-action': 'true' })
    ).toBeTruthy();
  });

  it('shows the initial no-quota state without inventing values from another account', () => {
    const detailView = makeDetailView(0, null);
    detailView.quota.resetCreditExpiries = [];
    let renderer!: ReactTestRenderer;
    act(() => { renderer = create(<AccountQuotaTab detailView={detailView} windowUsageError="" historyAvailable={false} historyRefreshing onRefreshHistory={vi.fn()} onResetQuota={vi.fn()} resetQuotaDisabled />); });
    const output = JSON.stringify(renderer.toJSON());
    expect(output).toContain('accounts.detail_quota');
    expect(output).not.toMatch(/\d+%/);
    expect(renderer.root.findAllByProps({ 'data-quota-reset-credit-expiry': 'credit-1' })).toHaveLength(0);
    act(() => renderer.unmount());
  });
});

describe('AccountQuotaTab quota pace hint', () => {
  const makeWindow = (
    pace: AccountDetailViewModel['quota']['windows'][number]['pace']
  ): AccountDetailViewModel['quota']['windows'][number] =>
    ({
      key: 'weekly',
      label: 'Weekly',
      kind: 'weekly',
      remainingPercent: 50,
      usedPercent: 50,
      resetLabel: '-',
      resetAtMs: null,
      resetAccuracy: 'unknown',
      limitWindowSeconds: 7 * 24 * 60 * 60,
      fromMs: null,
      toMs: null,
      modelScope: { kind: 'all', complete: true },
      windowMode: 'fixed',
      cycleStartMs: null,
      cycleEndMs: null,
      boundaryAccuracy: 'exact',
      stale: false,
      usage: null,
      currentUsage: null,
      previousUsage: null,
      previousPeriod: null,
      forecast: null,
      pace,
    }) as AccountDetailViewModel['quota']['windows'][number];

  const makeDetailView = (
    windows: AccountDetailViewModel['quota']['windows']
  ): AccountDetailViewModel =>
    ({
      identity: {
        rowKey: 'antigravity.json\u0000auth-1',
        name: 'antigravity.json',
        provider: 'antigravity',
        type: 'antigravity',
        authIndex: 'auth-1',
        account: 'test@example.com',
      },
      quota: {
        windows,
        resetCreditsAvailableCount: null,
        resetCreditExpiries: [],
        cooldown: null,
      },
      history: null,
    }) as unknown as AccountDetailViewModel;

  const render = (detailView: AccountDetailViewModel, showQuotaPace?: boolean) => {
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <AccountQuotaTab
          detailView={detailView}
          windowUsageError=""
          historyAvailable={false}
          historyRefreshing={false}
          onRefreshHistory={vi.fn()}
          onResetQuota={vi.fn()}
          resetQuotaDisabled
          {...(showQuotaPace === undefined ? {} : { showQuotaPace })}
        />
      );
    });
    return renderer;
  };

  it('explains why nothing is projected when no window has a pace', () => {
    const renderer = render(makeDetailView([makeWindow(null)]));

    expect(renderer.root.findAllByProps({ 'data-quota-pace-empty': 'true' })).toHaveLength(1);
    act(() => renderer.unmount());
  });

  it('hides the hint once a window has a projection', () => {
    const renderer = render(
      makeDetailView([
        makeWindow({
          status: 'lasts_until_reset',
          exhaustsAtMs: 100,
          resetsAtMs: 200,
          remainingPercent: 40,
        }),
      ])
    );

    expect(renderer.root.findAllByProps({ 'data-quota-pace-empty': 'true' })).toHaveLength(0);
    act(() => renderer.unmount());
  });

  it('hides the hint when the preference is disabled', () => {
    const renderer = render(makeDetailView([makeWindow(null)]), false);

    expect(renderer.root.findAllByProps({ 'data-quota-pace-empty': 'true' })).toHaveLength(0);
    act(() => renderer.unmount());
  });
});

describe('AccountQuotaTab quota pace toggle', () => {
  const makeDetailView = (): AccountDetailViewModel =>
    ({
      identity: {
        rowKey: 'antigravity.json\u0000auth-1',
        name: 'antigravity.json',
        provider: 'antigravity',
        type: 'antigravity',
        authIndex: 'auth-1',
        account: 'test@example.com',
      },
      quota: {
        windows: [],
        resetCreditsAvailableCount: null,
        resetCreditExpiries: [],
        cooldown: null,
      },
      history: null,
    }) as unknown as AccountDetailViewModel;

  const readText = (value: unknown): string => {
    if (typeof value === 'string' || typeof value === 'number') return String(value);
    if (Array.isArray(value)) return value.map(readText).join('');
    if (value && typeof value === 'object' && 'children' in value) {
      return readText((value as { children?: unknown }).children);
    }
    return '';
  };

  it('exposes the quota pace switch and reports toggles', () => {
    const onToggleQuotaPace = vi.fn();
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <AccountQuotaTab
          detailView={makeDetailView()}
          windowUsageError=""
          historyAvailable={false}
          historyRefreshing={false}
          onRefreshHistory={vi.fn()}
          onResetQuota={vi.fn()}
          resetQuotaDisabled
          onToggleQuotaPace={onToggleQuotaPace}
        />
      );
    });

    const button = renderer.root.findAll(
      (node) => node.type === 'button' && node.props['aria-pressed'] === true
    )[0];
    if (!button) throw new Error('quota pace button not found');
    expect(button.props['aria-label']).toBe('accounts.quota_pace_hide_hint');

    act(() => button.props.onClick());
    expect(onToggleQuotaPace).toHaveBeenCalledTimes(1);

    act(() => renderer.unmount());
  });
});
