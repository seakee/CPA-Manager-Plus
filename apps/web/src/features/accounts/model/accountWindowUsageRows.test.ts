import { describe, expect, it } from 'vitest';
import type { AccountRow } from './accountRows';
import {
  accountWindowUsageRequestKey,
  buildAccountWindowUsageByKey,
  buildAccountWindowUsageTargetEntries,
  filterAccountWindowUsageByTargetRanges,
} from './accountWindowUsageRows';
import type { AccountQuotaWindowDefinition } from './accountQuotaWindowDefinitions';

const makeRow = (overrides: Partial<AccountRow>): AccountRow =>
  ({
    selectionKey: 'codex.json\x00auth-1',
    fileName: 'codex.json',
    provider: 'codex',
    authIndex: 'auth-1',
    authenticationAtMs: 0,
    rawCredentialStatusSuperseded: false,
    raw: {
      account: 'codex@example.com',
      label: 'Codex Seat',
    },
    ...overrides,
  }) as AccountRow;

describe('accountWindowUsageRows', () => {
  it('builds window-scoped targets from account rows and valid window ranges', () => {
    const row = makeRow({
      provider: 'codex',
      projectId: 'unsafe-generic-project',
      raw: {
        name: 'codex.json',
        provider: 'codex',
        authIndex: 'auth-1',
        account: 'codex@example.com',
        label: 'Codex Seat',
        account_id: 'account-1',
      },
    });
    const entries = buildAccountWindowUsageTargetEntries(
      [row],
      new Map([
        [
          row.selectionKey,
          [
            { key: '5h', fromMs: 1000, toMs: 2000 },
            { key: 'missing-range', fromMs: null, toMs: 3000 },
          ],
        ],
      ])
    );

    expect(entries).toHaveLength(1);
    expect(entries[0]).toMatchObject({
      rowKey: row.selectionKey,
      windowKey: '5h',
      requestKey: accountWindowUsageRequestKey(row.selectionKey, '5h', 'current', {
        kind: 'all',
        complete: true,
      }),
      target: {
        row_key: row.selectionKey,
        window_key: '5h',
        from_ms: 1000,
        to_ms: 2000,
        account_snapshot: 'codex@example.com',
        auth_label_snapshot: 'Codex Seat',
        auth_file_snapshot: 'codex.json',
        auth_provider_snapshot: 'codex',
        auth_account_id_snapshot: 'account-1',
        auth_project_id_snapshot: undefined,
        auth_index: 'auth-1',
        source: 'codex.json',
      },
    });
  });

  it('does not treat generic Codex project ids as trusted account ids', () => {
    const row = makeRow({ provider: 'codex', projectId: 'unsafe-generic-project' });
    const [entry] = buildAccountWindowUsageTargetEntries(
      [row],
      new Map([[row.selectionKey, [{ key: '5h', fromMs: 1000, toMs: 2000 }]]])
    );

    expect(entry?.target.auth_project_id_snapshot).toBeUndefined();
  });

  it('skips weak legacy identities without dropping valid rows from the batch', () => {
    const weakRow = makeRow({
      selectionKey: 'weak-row',
      fileName: 'providerless.json',
      authIndex: 'auth-weak',
      provider: 'unknown',
      projectId: '',
      raw: {
        name: 'providerless.json',
        account: 'providerless@example.com',
        authIndex: 'auth-weak',
      },
    });
    const strongRow = makeRow({ selectionKey: 'strong-row', provider: 'codex' });
    const windows = new Map([
      [weakRow.selectionKey, [{ key: '5h', fromMs: 1000, toMs: 2000 }]],
      [strongRow.selectionKey, [{ key: '5h', fromMs: 1000, toMs: 2000 }]],
    ]);

    const entries = buildAccountWindowUsageTargetEntries([weakRow, strongRow], windows);

    expect(entries).toHaveLength(1);
    expect(entries[0].rowKey).toBe(strongRow.selectionKey);
    expect(entries[0].target.auth_file_snapshot).toBe('codex.json');
  });

  it('indexes response items by row and quota window', () => {
    const row = makeRow({});
    const entries = buildAccountWindowUsageTargetEntries(
      [row],
      new Map([[row.selectionKey, [{ key: '5h', fromMs: 1000, toMs: 2000 }]]])
    );
    const byKey = buildAccountWindowUsageByKey(entries, [
      {
        row_key: row.selectionKey,
        window_key: '5h',
        from_ms: 1000,
        to_ms: 2000,
        matched: true,
        total_requests: 32,
        success_calls: 30,
        failure_calls: 2,
        total_tokens: 240000,
        total_cost: 5.2,
        success_rate: 0.9375,
        last_seen_ms: 1900,
        sync_status: 'ready',
      },
    ]);

    expect(
      byKey.get(
        accountWindowUsageRequestKey(row.selectionKey, '5h', 'current', {
          kind: 'all',
          complete: true,
        })
      )
    ).toMatchObject({
      matched: true,
      total_requests: 32,
      total_cost: 5.2,
    });
  });

  it('does not reuse usage from an older cycle with the same provider window id', () => {
    const row = makeRow({});
    const previousEntries = buildAccountWindowUsageTargetEntries(
      [row],
      new Map([[row.selectionKey, [{ key: '5h', fromMs: 1_000, toMs: 2_000 }]]])
    );
    const usageByKey = buildAccountWindowUsageByKey(previousEntries, [
      {
        row_key: row.selectionKey,
        window_key: '5h',
        from_ms: 1_000,
        to_ms: 2_000,
        matched: true,
        total_requests: 32,
        success_calls: 30,
        failure_calls: 2,
        total_tokens: 240_000,
        total_cost: 5.2,
        success_rate: 0.9375,
        last_seen_ms: 1_900,
        sync_status: 'ready',
      },
    ]);
    const currentEntries = buildAccountWindowUsageTargetEntries(
      [row],
      new Map([[row.selectionKey, [{ key: '5h', fromMs: 2_000, toMs: 3_000 }]]])
    );

    expect(filterAccountWindowUsageByTargetRanges(previousEntries, usageByKey)).toHaveLength(1);
    expect(filterAccountWindowUsageByTargetRanges(currentEntries, usageByKey)).toHaveLength(0);
  });

  it('creates distinct current and previous targets across a lifecycle gap', () => {
    const row = makeRow({});
    const definition: AccountQuotaWindowDefinition = {
      key: '5h',
      providerWindowId: '5h',
      provider: 'codex',
      label: '5h',
      kind: 'five_hour',
      windowMode: 'fixed',
      modelScope: { kind: 'all', complete: true },
      observationSource: 'api_query',
      observedAtMs: 30_000,
      quotaProgressObservedAtMs: 30_000,
      boundaryAccuracy: 'exact',
      cycleStartMs: 20_000,
      cycleEndMs: 38_000,
      durationSeconds: 18,
      remainingPercent: 60,
      usedPercent: 40,
      stale: false,
      display: {
        key: '5h',
        label: '5h',
        kind: 'five_hour',
        remainingPercent: 60,
        usedPercent: 40,
        resetLabel: 'reset',
        resetAccuracy: 'exact',
        limitWindowSeconds: 18,
        resetAtMs: 38_000,
        fromMs: 20_000,
        toMs: 30_000,
      },
      currentCycle: {
        id: 2,
        activationId: 1,
        state: 'active',
        scheduledStartMs: 25_000,
        scheduledEndMs: 38_000,
        actualStartMs: 25_000,
        actualEndMs: null,
        durationSeconds: 18,
        boundaryAccuracy: 'exact',
        endReason: '',
        parentCycleId: null,
        forecastEligible: true,
      },
      previousCycle: {
        id: 1,
        activationId: 1,
        state: 'closed',
        scheduledStartMs: 2_000,
        scheduledEndMs: 20_000,
        actualStartMs: 8_000,
        actualEndMs: 20_000,
        durationSeconds: 18,
        boundaryAccuracy: 'exact',
        endReason: 'scheduled',
        parentCycleId: null,
        forecastEligible: true,
      },
    };

    const entries = buildAccountWindowUsageTargetEntries(
      [row],
      new Map([[row.selectionKey, [definition]]]),
      30_000
    );

    expect(entries).toHaveLength(2);
    expect(entries.map((entry) => entry.period)).toEqual(['current', 'previous']);
    expect(entries.map((entry) => [entry.target.from_ms, entry.target.to_ms])).toEqual([
      [25_000, 30_000],
      [8_000, 20_000],
    ]);
    expect(entries[0].requestKey).not.toBe(entries[1].requestKey);
  });

  it('keeps the previous target when the current lifecycle boundary is provisional', () => {
    const row = makeRow({});
    const definition: AccountQuotaWindowDefinition = {
      key: '5h',
      providerWindowId: '5h',
      provider: 'codex',
      label: '5h',
      kind: 'five_hour',
      windowMode: 'fixed',
      modelScope: { kind: 'all', complete: true },
      observationSource: 'api_query',
      observedAtMs: 30_000,
      quotaProgressObservedAtMs: 30_000,
      boundaryAccuracy: 'unknown',
      cycleStartMs: 20_000,
      cycleEndMs: 38_000,
      durationSeconds: 18,
      remainingPercent: 100,
      usedPercent: 0,
      stale: false,
      display: {
        key: '5h',
        label: '5h',
        kind: 'five_hour',
        remainingPercent: 100,
        usedPercent: 0,
        resetLabel: 'reset',
        resetAccuracy: 'unknown',
        limitWindowSeconds: 18,
        resetAtMs: 38_000,
        fromMs: 20_000,
        toMs: 38_000,
      },
      currentCycle: {
        id: 2,
        activationId: 1,
        state: 'provisional',
        scheduledStartMs: 20_000,
        scheduledEndMs: 38_000,
        actualStartMs: 20_000,
        actualEndMs: null,
        durationSeconds: 18,
        boundaryAccuracy: 'unknown',
        endReason: '',
        parentCycleId: null,
        forecastEligible: false,
      },
      previousCycle: {
        id: 1,
        activationId: 1,
        state: 'closed',
        scheduledStartMs: 2_000,
        scheduledEndMs: 20_000,
        actualStartMs: 8_000,
        actualEndMs: 20_000,
        durationSeconds: 18,
        boundaryAccuracy: 'exact',
        endReason: 'scheduled',
        parentCycleId: null,
        forecastEligible: false,
      },
    };

    const entries = buildAccountWindowUsageTargetEntries(
      [row],
      new Map([[row.selectionKey, [definition]]]),
      30_000
    );

    expect(entries).toHaveLength(1);
    expect(entries[0]).toMatchObject({
      period: 'previous',
      target: {
        from_ms: 8_000,
        to_ms: 20_000,
      },
    });
  });

  it('uses unique window keys instead of provider order for legacy scoped responses', () => {
    const row = makeRow({});
    const alphaScope = { kind: 'models' as const, models: ['model-alpha'] };
    const betaScope = { kind: 'models' as const, models: ['model-beta'] };
    const alphaRequestKey = accountWindowUsageRequestKey(
      row.selectionKey,
      'shared-window',
      'current',
      alphaScope
    );
    const betaRequestKey = accountWindowUsageRequestKey(
      row.selectionKey,
      'shared-window',
      'current',
      betaScope
    );
    const entries = [
      {
        rowKey: row.selectionKey,
        windowKey: 'shared-window::scope::models::-::model-alpha',
        providerWindowId: 'shared-window',
        period: 'current' as const,
        requestKey: alphaRequestKey,
        target: {
          row_key: row.selectionKey,
          window_key: 'shared-window::scope::models::-::model-alpha',
          provider_window_id: 'shared-window',
          period: 'current' as const,
          from_ms: 1_000,
          to_ms: 2_000,
          model_scope: alphaScope,
        },
      },
      {
        rowKey: row.selectionKey,
        windowKey: 'shared-window::scope::models::-::model-beta',
        providerWindowId: 'shared-window',
        period: 'current' as const,
        requestKey: betaRequestKey,
        target: {
          row_key: row.selectionKey,
          window_key: 'shared-window::scope::models::-::model-beta',
          provider_window_id: 'shared-window',
          period: 'current' as const,
          from_ms: 1_000,
          to_ms: 2_000,
          model_scope: betaScope,
        },
      },
    ];
    const byKey = buildAccountWindowUsageByKey(entries, [
      {
        row_key: row.selectionKey,
        window_key: entries[1].windowKey,
        provider_window_id: 'shared-window',
        period: 'current',
        from_ms: 1_000,
        to_ms: 2_000,
        matched: true,
        total_requests: 22,
        success_calls: 22,
        failure_calls: 0,
        total_tokens: 2_200,
        total_cost: 2.2,
        success_rate: 1,
        last_seen_ms: 1_900,
        sync_status: 'ready',
      },
      {
        row_key: row.selectionKey,
        window_key: entries[0].windowKey,
        provider_window_id: 'shared-window',
        period: 'current',
        from_ms: 1_000,
        to_ms: 2_000,
        matched: true,
        total_requests: 11,
        success_calls: 11,
        failure_calls: 0,
        total_tokens: 1_100,
        total_cost: 1.1,
        success_rate: 1,
        last_seen_ms: 1_900,
        sync_status: 'ready',
      },
    ]);

    expect(byKey.get(alphaRequestKey)?.total_requests).toBe(11);
    expect(byKey.get(betaRequestKey)?.total_requests).toBe(22);
  });

  it('does not assign an ambiguous provider-only response to either model scope', () => {
    const row = makeRow({});
    const entries = ['model-alpha', 'model-beta'].map((model) => {
      const modelScope = { kind: 'models' as const, models: [model] };
      return {
        rowKey: row.selectionKey,
        windowKey: `shared-window::${model}`,
        providerWindowId: 'shared-window',
        period: 'current' as const,
        requestKey: accountWindowUsageRequestKey(
          row.selectionKey,
          'shared-window',
          'current',
          modelScope
        ),
        target: {
          row_key: row.selectionKey,
          provider_window_id: 'shared-window',
          period: 'current' as const,
          from_ms: 1_000,
          to_ms: 2_000,
          model_scope: modelScope,
        },
      };
    });
    const byKey = buildAccountWindowUsageByKey(entries, [
      {
        row_key: row.selectionKey,
        provider_window_id: 'shared-window',
        period: 'current',
        from_ms: 1_000,
        to_ms: 2_000,
        matched: true,
        total_requests: 99,
        success_calls: 99,
        failure_calls: 0,
        total_tokens: 9_900,
        total_cost: 9.9,
        success_rate: 1,
        last_seen_ms: 1_900,
        sync_status: 'ready',
      },
    ]);

    expect(byKey).toHaveLength(0);
  });

  it('skips incomplete model and feature scopes without dropping other quota windows', () => {
    const row = makeRow({});
    const incompleteDefinition = {
      key: 'weekly-scoped-label-only',
      providerWindowId: 'weekly-scoped-label-only',
      provider: 'claude',
      label: 'Label-only model',
      kind: 'weekly',
      windowMode: 'fixed',
      modelScope: { kind: 'models', models: [], complete: false },
      observationSource: 'api_query',
      observedAtMs: 5_000,
      quotaProgressObservedAtMs: 5_000,
      boundaryAccuracy: 'exact',
      cycleStartMs: 1_000,
      cycleEndMs: 7_000,
      durationSeconds: 6,
      remainingPercent: 50,
      usedPercent: 50,
      stale: false,
      display: {
        key: 'weekly-scoped-label-only',
        label: 'Label-only model',
        remainingPercent: 50,
        usedPercent: 50,
        resetLabel: '-',
        resetAccuracy: 'exact',
        limitWindowSeconds: 6,
        resetAtMs: 7_000,
        fromMs: 1_000,
        toMs: 5_000,
      },
    } satisfies AccountQuotaWindowDefinition;
    const incompleteFeatureDefinition = {
      ...incompleteDefinition,
      key: 'future-feature-weekly-0',
      providerWindowId: 'future-feature-weekly-0',
      provider: 'codex',
      label: 'Future Feature',
      modelScope: { kind: 'feature', key: 'future_feature', complete: false },
      display: {
        ...incompleteDefinition.display,
        key: 'future-feature-weekly-0',
        label: 'Future Feature',
      },
    } satisfies AccountQuotaWindowDefinition;

    const entries = buildAccountWindowUsageTargetEntries(
      [row],
      new Map([
        [
          row.selectionKey,
          [
            { key: '5h', fromMs: 1000, toMs: 2000 },
            incompleteDefinition,
            incompleteFeatureDefinition,
          ],
        ],
      ]),
      5_000
    );

    expect(entries).toHaveLength(1);
    expect(entries[0].windowKey).toBe('5h');
  });
});
