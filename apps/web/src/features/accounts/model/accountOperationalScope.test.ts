import { describe, expect, it } from 'vitest';
import type { AuthFileItem } from '@/types';
import { buildAccountRows } from './accountRows';
import { buildAccountOperationalItemsByRowKey } from './accountOperationalScope';

const emptyStores = () => ({
  antigravityQuota: {},
  claudeQuota: {},
  codexQuota: {},
  kimiQuota: {},
  xaiQuota: {},
});

describe('buildAccountOperationalItemsByRowKey', () => {
  it('assigns a file-level item to the only account row behind that file', () => {
    const rows = buildAccountRows(
      [{ name: 'single.json', type: 'codex', authIndex: 'auth-1' }],
      emptyStores()
    );
    const item = { id: 1, authFileName: 'single.json' };

    expect(buildAccountOperationalItemsByRowKey(rows, [item]).get(rows[0].selectionKey)).toEqual([
      item,
    ]);
  });

  it('does not spread a file-level item across shared account rows', () => {
    const files: AuthFileItem[] = [
      { name: 'shared.json', type: 'codex', authIndex: 'auth-1' },
      { name: 'shared.json', type: 'codex', authIndex: 'auth-2' },
    ];
    const rows = buildAccountRows(files, emptyStores());
    const item = { id: 1, authFileName: 'shared.json' };
    const result = buildAccountOperationalItemsByRowKey(rows, [item]);

    expect(result.get(rows[0].selectionKey)).toEqual([]);
    expect(result.get(rows[1].selectionKey)).toEqual([]);
  });

  it('keeps exact auth-index matches for shared account rows', () => {
    const files: AuthFileItem[] = [
      { name: 'shared.json', type: 'codex', authIndex: 'auth-1' },
      { name: 'shared.json', type: 'codex', authIndex: 'auth-2' },
    ];
    const rows = buildAccountRows(files, emptyStores());
    const item = { id: 1, authFileName: 'shared.json', authIndex: 'auth-2' };
    const result = buildAccountOperationalItemsByRowKey(rows, [item]);

    expect(result.get(rows[0].selectionKey)).toEqual([]);
    expect(result.get(rows[1].selectionKey)).toEqual([item]);
  });

  it('scopes same-file cooldowns by provider and account snapshot', () => {
    const files: AuthFileItem[] = [
      {
        id: 'runtime-first',
        name: 'shared.json',
        type: 'codex',
        provider: 'codex',
        account: 'first@example.com',
      },
      {
        id: 'runtime-second',
        name: 'shared.json',
        type: 'codex',
        provider: 'codex',
        account: 'second@example.com',
      },
    ];
    const rows = buildAccountRows(files, emptyStores());
    const item = {
      id: 1,
      authFileName: 'shared.json',
      provider: 'codex',
      accountSnapshot: 'second@example.com',
    };
    const result = buildAccountOperationalItemsByRowKey(rows, [item]);

    expect(result.get(rows[0].selectionKey)).toEqual([]);
    expect(result.get(rows[1].selectionKey)).toEqual([item]);
  });

  it('does not scope same-file action candidates by a Codex workspace ID', () => {
    const files: AuthFileItem[] = [
      {
        name: 'shared.json',
        type: 'codex',
        provider: 'codex',
        id_token: { account_id: 'account-first' },
      },
      {
        name: 'shared.json',
        type: 'codex',
        provider: 'codex',
        id_token: { account_id: 'account-second' },
      },
    ];
    const rows = buildAccountRows(files, emptyStores());
    const item = {
      id: 2,
      authFileName: 'shared.json',
      provider: 'codex',
      accountIdSnapshot: 'account-second',
    };
    const result = buildAccountOperationalItemsByRowKey(rows, [item]);

    expect(result.get(rows[0].selectionKey)).toEqual([]);
    expect(result.get(rows[1].selectionKey)).toEqual([]);
  });
});
