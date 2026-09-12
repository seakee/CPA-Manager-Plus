import { describe, expect, it } from 'vitest';
import type { AccountRow } from '@/features/accounts/model/accountRows';
import { collectCodexSubscriptionTargets, shouldShowCodexSubscriptionTab } from './tabGate';

const makeRow = (overrides: Partial<AccountRow> = {}): AccountRow =>
  ({
    key: 'row-1',
    selectionKey: 'row-1',
    fileName: 'codex-plus.json',
    provider: 'codex',
    accountLabel: 'plus@example.com',
    planType: 'plus',
    authIndex: 'codex-1',
    raw: {
      name: 'codex-plus.json',
      type: 'codex',
      chatgpt_account_id: 'acct_plus',
      authIndex: 'codex-1',
    },
    ...overrides,
  }) as AccountRow;

describe('shouldShowCodexSubscriptionTab', () => {
  it('shows the tab only for paid Codex', () => {
    expect(shouldShowCodexSubscriptionTab(makeRow())).toBe(true);
    expect(shouldShowCodexSubscriptionTab(makeRow({ planType: 'pro' }))).toBe(true);
    expect(shouldShowCodexSubscriptionTab(makeRow({ planType: 'free' }))).toBe(false);
    expect(shouldShowCodexSubscriptionTab(makeRow({ provider: 'claude', planType: 'pro' }))).toBe(
      false
    );
  });
});

describe('collectCodexSubscriptionTargets', () => {
  it('shares one target per chatgpt_account_id across credential files', () => {
    const shared = makeRow({
      key: 'a',
      selectionKey: 'a',
      fileName: 'a.json',
      authIndex: '1',
      raw: { name: 'a.json', type: 'codex', chatgpt_account_id: 'acct_shared', authIndex: '1' },
    });
    const duplicate = makeRow({
      key: 'b',
      selectionKey: 'b',
      fileName: 'b.json',
      authIndex: '2',
      raw: { name: 'b.json', type: 'codex', chatgpt_account_id: 'acct_shared', authIndex: '2' },
    });
    const other = makeRow({
      key: 'c',
      selectionKey: 'c',
      fileName: 'c.json',
      authIndex: '3',
      planType: 'team',
      raw: { name: 'c.json', type: 'codex', chatgpt_account_id: 'acct_other', authIndex: '3' },
    });
    const free = makeRow({
      key: 'd',
      selectionKey: 'd',
      planType: 'free',
      raw: { name: 'free.json', type: 'codex', chatgpt_account_id: 'acct_free', authIndex: '4' },
    });

    expect(collectCodexSubscriptionTargets([shared, duplicate, other, free])).toEqual([
      { accountId: 'acct_shared', authIndex: '1' },
      { accountId: 'acct_other', authIndex: '3' },
    ]);
  });
});
