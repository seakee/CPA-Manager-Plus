import { describe, expect, it } from 'vitest';
import type { AccountRow } from './accountRows';
import {
  ACCOUNT_HISTORY_TARGET_BATCH_SIZE,
  buildAccountHistoryByRowKey,
  buildAccountHistoryTargetBatches,
  buildAccountHistoryTargetEntries,
  retainAccountHistoryRowKeys,
} from './accountHistoryRows';

const makeRow = (overrides: Partial<AccountRow>): AccountRow =>
  ({
    key: 'codex.json',
    selectionKey: 'codex.json\u0000auth-1',
    fileName: 'codex.json',
    accountLabel: 'codex@example.com',
    provider: 'codex',
    planType: null,
    disabled: false,
    runtimeOnly: false,
    statusMessage: '',
    authIndex: 'auth-1',
    projectId: '',
    priority: 0,
    authenticationAtMs: 0,
    rawCredentialStatusSuperseded: false,
    quota: {
      status: 'unknown',
      remainingPercent: null,
      usedPercent: null,
      resetLabel: '-',
      planType: null,
      source: 'none',
    },
    usage: {
      success: 0,
      failure: 0,
      successRate: null,
      recentRequests: [],
    },
    inspection: null,
    raw: {
      name: 'codex.json',
      account: 'codex@example.com',
      authIndex: 'auth-1',
    },
    ...overrides,
  }) as AccountRow;

describe('accountHistoryRows', () => {
  it('splits account-history targets at the server request limit', () => {
    const entries = Array.from({ length: ACCOUNT_HISTORY_TARGET_BATCH_SIZE + 1 }, (_, index) => ({
      rowKey: `row-${index}`,
      target: { row_key: `row-${index}` },
    }));

    const batches = buildAccountHistoryTargetBatches(entries);

    expect(batches).toHaveLength(2);
    expect(batches[0]).toHaveLength(ACCOUNT_HISTORY_TARGET_BATCH_SIZE);
    expect(batches[1]).toHaveLength(1);
    expect(batches.flat()).toEqual(entries);
  });

  it('removes history state for rows that are no longer active', () => {
    const current = new Map([
      ['active-row', 'keep'],
      ['deleted-row', 'remove'],
    ]);

    expect(retainAccountHistoryRowKeys(current, new Set(['active-row']))).toEqual(
      new Map([['active-row', 'keep']])
    );
    expect(retainAccountHistoryRowKeys(current, new Set(current.keys()))).toBe(current);
  });

  it('builds strict account-history targets from current account rows', () => {
    const entries = buildAccountHistoryTargetEntries([
      makeRow({}),
      makeRow({
        selectionKey: 'label.json\u0000auth-2',
        fileName: 'label.json',
        accountLabel: 'Team login',
        authIndex: 'auth-2',
        raw: {
          name: 'label.json',
          label: 'Team login',
          authIndex: 'auth-2',
        },
      }),
    ]);

    expect(entries).toEqual([
      {
        rowKey: 'codex.json\u0000auth-1',
        target: {
          row_key: 'codex.json\u0000auth-1',
          account_snapshot: 'codex@example.com',
          auth_label_snapshot: undefined,
          auth_file_snapshot: 'codex.json',
          auth_provider_snapshot: 'codex',
          auth_project_id_snapshot: undefined,
          auth_index: 'auth-1',
          source: 'codex.json',
        },
      },
      {
        rowKey: 'label.json\u0000auth-2',
        target: {
          row_key: 'label.json\u0000auth-2',
          account_snapshot: undefined,
          auth_label_snapshot: 'Team login',
          auth_file_snapshot: 'label.json',
          auth_provider_snapshot: 'codex',
          auth_project_id_snapshot: undefined,
          auth_index: 'auth-2',
          source: 'label.json',
        },
      },
    ]);
  });

  it('preserves a compatible raw provider when the account row provider is unknown', () => {
    const [entry] = buildAccountHistoryTargetEntries([
      makeRow({
        provider: 'unknown',
        raw: {
          name: 'legacy-xai.json',
          typo: 'x_ai',
          authIndex: 'auth-xai',
          account: 'xai@example.com',
        },
      }),
    ]);

    expect(entry?.target.auth_provider_snapshot).toBe('xai');
  });

  it('skips file identities when the provider cannot be resolved', () => {
    const entries = buildAccountHistoryTargetEntries([
      makeRow({
        provider: 'unknown',
        authIndex: '',
        raw: {
          name: 'providerless.json',
          account: 'providerless@example.com',
        },
      }),
    ]);

    expect(entries).toEqual([]);
  });

  it('maps out-of-order account-history responses only by server row keys', () => {
    const entries = buildAccountHistoryTargetEntries([
      makeRow({}),
      makeRow({
        selectionKey: 'second.json\u0000auth-2',
        fileName: 'second.json',
        authIndex: 'auth-2',
        raw: {
          name: 'second.json',
          account: 'second@example.com',
          authIndex: 'auth-2',
        },
      }),
    ]);
    const byRowKey = buildAccountHistoryByRowKey(entries, [
      {
        row_key: 'second.json\u0000auth-2',
        account_key: 'opaque-second',
        matched: true,
        total_requests: 20,
        success_calls: 19,
        failure_calls: 1,
        total_tokens: 2400,
        total_cost: 0.84,
        success_rate: 0.95,
        first_seen_ms: 1,
        last_seen_ms: 2,
        sync_status: 'ready',
      },
      {
        row_key: 'codex.json\u0000auth-1',
        account_key: 'opaque-first',
        matched: true,
        total_requests: 10,
        success_calls: 9,
        failure_calls: 1,
        total_tokens: 1200,
        total_cost: 0.42,
        success_rate: 0.9,
        first_seen_ms: 1,
        last_seen_ms: 2,
        sync_status: 'ready',
      },
    ]);

    expect(byRowKey.get('codex.json\u0000auth-1')?.total_requests).toBe(10);
    expect(byRowKey.get('second.json\u0000auth-2')?.total_requests).toBe(20);
  });

  it('treats row keys as opaque and preserves surrounding whitespace', () => {
    const rowKey = ' spaced.json\u0000auth-1 ';
    const entries = buildAccountHistoryTargetEntries([makeRow({ selectionKey: rowKey })]);
    const byRowKey = buildAccountHistoryByRowKey(entries, [
      {
        row_key: rowKey,
        account_key: 'opaque-spaced',
        matched: true,
        total_requests: 1,
        success_calls: 1,
        failure_calls: 0,
        total_tokens: 10,
        total_cost: 0.01,
        success_rate: 1,
        first_seen_ms: 1,
        last_seen_ms: 2,
        sync_status: 'ready',
      },
    ]);

    expect(byRowKey.get(rowKey)?.total_requests).toBe(1);
  });

  it('does not guess response ownership from array position or account_key', () => {
    const entries = buildAccountHistoryTargetEntries([makeRow({})]);
    const byRowKey = buildAccountHistoryByRowKey(entries, [
      {
        row_key: 'unexpected-row',
        account_key: 'codex@example.com',
        matched: true,
        total_requests: 99,
        success_calls: 99,
        failure_calls: 0,
        total_tokens: 999,
        total_cost: 0,
        success_rate: 1,
        first_seen_ms: 1,
        last_seen_ms: 2,
        sync_status: 'ready',
      },
    ]);

    expect(byRowKey.size).toBe(0);
  });
});
