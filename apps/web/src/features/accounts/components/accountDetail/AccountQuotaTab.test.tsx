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

const makeDetailView = (availableCount: number | null): AccountDetailViewModel =>
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
      resetCreditExpiries: [],
      cooldown: null,
    },
    history: null,
  } as unknown as AccountDetailViewModel);

describe('AccountQuotaTab initial state', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(1_000_000);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('shows the initial no-quota state without inventing values from another account', () => {
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <AccountQuotaTab
          detailView={makeDetailView(0)}
          windowUsageError=""
          historyAvailable={false}
          historyRefreshing
          onRefreshHistory={vi.fn()}
          onResetQuota={vi.fn()}
          resetQuotaDisabled
        />
      );
    });

    const output = JSON.stringify(renderer.toJSON());
    expect(output).toContain('accounts.detail_tab_quota');
    expect(output).not.toMatch(/\d+%/);
    expect(
      renderer.root.findAllByProps({ 'data-quota-reset-credit-expiry': 'credit-1' })
    ).toHaveLength(0);
    act(() => renderer.unmount());
  });
});
