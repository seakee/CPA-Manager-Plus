import { describe, expect, it } from 'vitest';
import type { MonitoringAccountRow } from './hooks/useMonitoringData';
import type { MonitoringAccountAuthState } from './accountOverviewState';
import { buildMonitoringAccountQuotaTargetsByRowId } from './accountOverviewQuotaTargets';

const createAccountRow = (overrides: Partial<MonitoringAccountRow> = {}): MonitoringAccountRow => ({
  id: overrides.id ?? 'account@example.com',
  account: overrides.account ?? 'account@example.com',
  provider: overrides.provider,
  displayAccount: overrides.displayAccount ?? overrides.account ?? 'account@example.com',
  accountMasked: overrides.accountMasked ?? 'acc***@example.com',
  authLabels: overrides.authLabels ?? [],
  authIndices: overrides.authIndices ?? [],
  channels: overrides.channels ?? [],
  totalCalls: overrides.totalCalls ?? 0,
  successCalls: overrides.successCalls ?? 0,
  failureCalls: overrides.failureCalls ?? 0,
  successRate: overrides.successRate ?? 1,
  inputTokens: overrides.inputTokens ?? 0,
  outputTokens: overrides.outputTokens ?? 0,
  cachedTokens: overrides.cachedTokens ?? 0,
  cacheReadTokens: overrides.cacheReadTokens ?? 0,
  cacheCreationTokens: overrides.cacheCreationTokens ?? 0,
  totalTokens: overrides.totalTokens ?? 0,
  totalCost: overrides.totalCost ?? 0,
  averageLatencyMs: overrides.averageLatencyMs ?? null,
  lastSeenAt: overrides.lastSeenAt ?? 0,
  recentPattern: overrides.recentPattern ?? [],
  models: overrides.models ?? [],
});

const createAuthState = (overrides: MonitoringAccountAuthState): MonitoringAccountAuthState =>
  overrides;

describe('accountOverviewQuotaTargets', () => {
  it('builds quota targets for providers the account actually used', () => {
    const authStateByRowId = new Map<string, MonitoringAccountAuthState>([
      [
        'account@example.com',
        createAuthState({
          files: [
            {
              name: 'alpha.json',
              type: 'codex',
              authIndex: '1',
              label: 'Alpha',
              account: 'account@example.com',
            },
            {
              name: 'beta.json',
              type: 'codex',
              authIndex: '2',
              label: 'Beta',
              account: 'account@example.com',
            },
            {
              name: 'gamma.json',
              type: 'claude',
              authIndex: '3',
              label: 'Gamma',
              account: 'account@example.com',
            },
          ],
          enabledState: 'enabled',
        }),
      ],
    ]);

    const result = buildMonitoringAccountQuotaTargetsByRowId(
      [
        createAccountRow({
          id: 'account@example.com',
          account: 'account@example.com',
          authIndices: ['1'],
          authLabels: ['Alpha'],
        }),
      ],
      authStateByRowId
    );

    expect(result.get('account@example.com')).toMatchObject([
      { provider: 'codex', authIndex: '1', fileName: 'alpha.json', authLabel: 'Alpha' },
      { provider: 'codex', authIndex: '2', fileName: 'beta.json', authLabel: 'Beta' },
    ]);
  });

  it('keeps Codex quota targets when the account id is unavailable', () => {
    const authStateByRowId = new Map<string, MonitoringAccountAuthState>([
      [
        'account@example.com',
        createAuthState({
          files: [
            {
              name: 'codex-without-account.json',
              type: 'codex',
              authIndex: '1',
              label: 'Codex',
              account: 'account@example.com',
            },
          ],
          enabledState: 'enabled',
        }),
      ],
    ]);

    const result = buildMonitoringAccountQuotaTargetsByRowId(
      [
        createAccountRow({
          id: 'account@example.com',
          account: 'account@example.com',
          authIndices: ['1'],
          authLabels: ['Codex'],
        }),
      ],
      authStateByRowId
    );

    expect(result.get('account@example.com')).toMatchObject([
      {
        provider: 'codex',
        authIndex: '1',
        fileName: 'codex-without-account.json',
        accountId: null,
      },
    ]);
  });

  it('includes quota-supported providers actually used while skipping disabled and runtime-only files', () => {
    const authStateByRowId = new Map<string, MonitoringAccountAuthState>([
      [
        'account@example.com',
        createAuthState({
          files: [
            {
              name: 'antigravity.json',
              type: 'antigravity',
              authIndex: '1',
              label: 'Antigravity',
            },
            {
              name: 'kimi.json',
              type: 'kimi',
              authIndex: '2',
              label: 'Kimi',
            },
            {
              name: 'disabled-claude.json',
              type: 'claude',
              authIndex: '3',
              disabled: true,
              label: 'Disabled Claude',
            },
          ],
          enabledState: 'enabled',
        }),
      ],
    ]);

    const result = buildMonitoringAccountQuotaTargetsByRowId(
      [
        createAccountRow({
          id: 'account@example.com',
          account: 'account@example.com',
          authIndices: ['1', '2', '3'],
        }),
      ],
      authStateByRowId
    );

    expect(result.get('account@example.com')).toMatchObject([
      { provider: 'antigravity', fileName: 'antigravity.json' },
      { provider: 'kimi', fileName: 'kimi.json' },
    ]);
  });

  it('does not surface providers that share the account but were not used by the row', () => {
    const authStateByRowId = new Map<string, MonitoringAccountAuthState>([
      [
        'account@example.com',
        createAuthState({
          files: [
            {
              name: 'codex.json',
              type: 'codex',
              authIndex: '1',
              label: 'Codex',
              account: 'account@example.com',
            },
            {
              name: 'claude.json',
              type: 'claude',
              authIndex: '2',
              label: 'Claude',
              account: 'account@example.com',
            },
          ],
          enabledState: 'enabled',
        }),
      ],
    ]);

    const result = buildMonitoringAccountQuotaTargetsByRowId(
      [
        createAccountRow({
          id: 'account@example.com',
          account: 'account@example.com',
          authIndices: ['1'],
        }),
      ],
      authStateByRowId
    );

    expect(result.get('account@example.com')).toMatchObject([
      { provider: 'codex', authIndex: '1', fileName: 'codex.json' },
    ]);
  });

  it('keeps same-email provider quota targets in independent row-id buckets', () => {
    const codexRowId = 'monitoring-account:codex';
    const antigravityRowId = 'monitoring-account:antigravity';
    const authStateByRowId = new Map<string, MonitoringAccountAuthState>([
      [
        codexRowId,
        createAuthState({
          files: [
            {
              name: 'codex.json',
              type: 'codex',
              authIndex: 'codex-auth',
              account: 'same@example.com',
            },
          ],
          enabledState: 'enabled',
        }),
      ],
      [
        antigravityRowId,
        createAuthState({
          files: [
            {
              name: 'antigravity.json',
              type: 'antigravity',
              authIndex: 'antigravity-auth',
              account: 'same@example.com',
            },
          ],
          enabledState: 'enabled',
        }),
      ],
    ]);

    const result = buildMonitoringAccountQuotaTargetsByRowId(
      [
        createAccountRow({
          id: codexRowId,
          account: 'same@example.com',
          provider: 'codex',
          authIndices: ['codex-auth'],
        }),
        createAccountRow({
          id: antigravityRowId,
          account: 'same@example.com',
          provider: 'antigravity',
          authIndices: ['antigravity-auth'],
        }),
      ],
      authStateByRowId
    );

    expect(result.get(codexRowId)).toMatchObject([{ provider: 'codex', fileName: 'codex.json' }]);
    expect(result.get(antigravityRowId)).toMatchObject([
      { provider: 'antigravity', fileName: 'antigravity.json' },
    ]);
    expect(result.get('same@example.com')).toBeUndefined();
  });
});
