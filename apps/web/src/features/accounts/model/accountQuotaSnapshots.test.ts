import { describe, expect, it } from 'vitest';
import type { AccountQuotaSnapshotWindow } from '@/services/api/usageService';
import { CODEX_SPARK_MODEL_ID } from '@/utils/quota/codexQuota';
import type { AccountQuotaWindowDefinition } from './accountQuotaWindowDefinitions';
import {
  buildAccountQuotaSnapshotWriteEntries,
  mergeCodexResetCreditsFromQuotaSnapshots,
  mergeAccountQuotaSnapshotWindows,
} from './accountQuotaSnapshots';
import type { AccountRow } from './accountRows';

const makeDefinition = (
  overrides: Partial<AccountQuotaWindowDefinition> = {}
): AccountQuotaWindowDefinition => ({
  key: 'five-hour',
  providerWindowId: 'five-hour',
  provider: 'codex',
  label: '5H',
  kind: 'five_hour',
  windowMode: 'fixed',
  modelScope: { kind: 'all', complete: true },
  observationSource: 'api_query',
  observedAtMs: 10_000,
  boundaryAccuracy: 'exact',
  cycleStartMs: 1_000,
  cycleEndMs: 19_001_000,
  durationSeconds: 19_000,
  remainingPercent: 80,
  usedPercent: 20,
  stale: false,
  display: {
    key: 'five-hour',
    label: '5H',
    kind: 'five_hour',
    remainingPercent: 80,
    usedPercent: 20,
    resetLabel: '-',
    resetAccuracy: 'exact',
    limitWindowSeconds: 19_000,
    resetAtMs: 19_001_000,
    fromMs: 1_000,
    toMs: 10_000,
    source: 'codex',
  },
  ...overrides,
});

const makeSnapshot = (
  overrides: Partial<AccountQuotaSnapshotWindow> = {}
): AccountQuotaSnapshotWindow => ({
  provider_window_id: 'five-hour',
  window_kind: 'five_hour',
  window_mode: 'fixed',
  model_scope_kind: 'all',
  source: 'response_header',
  observed_at_ms: 20_000,
  boundary_accuracy: 'derived',
  cycle_start_ms: 2_000,
  cycle_end_ms: 20_002_000,
  duration_seconds: 20_000,
  used_percent: 35,
  remaining_percent: 65,
  stale: false,
  ...overrides,
});

describe('account quota snapshots', () => {
  it('overlays server provenance, boundaries, scope, and stale state', () => {
    const merged = mergeAccountQuotaSnapshotWindows(
      [makeDefinition()],
      [
        makeSnapshot({
          stale: true,
        }),
      ]
    );

    expect(merged).toHaveLength(1);
    expect(merged[0]).toMatchObject({
      observationSource: 'response_header',
      boundaryAccuracy: 'derived',
      stale: true,
      modelScope: { kind: 'all', complete: true },
    });
  });

  it('merges legacy primary and secondary ids into the current Codex main windows', () => {
    const primary = makeDefinition({
      key: 'five-hour',
      providerWindowId: 'five-hour',
      kind: 'five_hour',
      modelScope: { kind: 'family', key: 'codex_main', complete: true },
    });
    const monthly = makeDefinition({
      key: 'monthly',
      providerWindowId: 'monthly',
      kind: 'monthly',
      modelScope: { kind: 'family', key: 'codex_main', complete: true },
    });
    const merged = mergeAccountQuotaSnapshotWindows(
      [primary, monthly],
      [
        makeSnapshot({
          provider_window_id: 'primary',
          window_kind: 'five_hour',
          model_scope_kind: 'all',
          used_percent: 11,
        }),
        makeSnapshot({
          provider_window_id: 'secondary',
          window_kind: 'monthly',
          model_scope_kind: 'all',
          used_percent: 22,
        }),
      ],
      { provider: 'codex' }
    );

    expect(merged).toHaveLength(2);
    expect(merged.find((window) => window.providerWindowId === 'five-hour')).toMatchObject({
      usedPercent: 11,
      modelScope: { kind: 'family', key: 'codex_main', complete: true },
    });
    expect(merged.find((window) => window.providerWindowId === 'monthly')).toMatchObject({
      usedPercent: 22,
      modelScope: { kind: 'family', key: 'codex_main', complete: true },
    });
  });

  it('does not apply Codex legacy suppression rules to another provider', () => {
    const accountWide = makeDefinition({
      key: 'shared-all',
      provider: 'antigravity',
      providerWindowId: 'shared-window',
      modelScope: { kind: 'all', complete: true },
    });
    const scoped = makeDefinition({
      key: 'shared-model',
      provider: 'antigravity',
      providerWindowId: 'shared-window',
      modelScope: { kind: 'models', models: ['model-alpha'], complete: true },
    });
    const merged = mergeAccountQuotaSnapshotWindows(
      [accountWide, scoped],
      [
        makeSnapshot({
          provider_window_id: 'shared-window',
          model_scope_kind: 'all',
          used_percent: 12,
        }),
        makeSnapshot({
          provider_window_id: 'shared-window',
          model_scope_kind: 'models',
          model_ids: ['model-alpha'],
          used_percent: 34,
        }),
      ],
      { provider: 'antigravity' }
    );

    expect(merged).toHaveLength(2);
    expect(merged.find((window) => window.key === 'shared-all')?.usedPercent).toBe(12);
    expect(merged.find((window) => window.key === 'shared-model')?.usedPercent).toBe(34);
  });

  it('keeps newer live quota definitions ahead of an older persisted snapshot', () => {
    const definition = makeDefinition({ observedAtMs: 30_000, usedPercent: 12 });
    const merged = mergeAccountQuotaSnapshotWindows(
      [definition],
      [makeSnapshot({ observed_at_ms: 20_000, used_percent: 91 })]
    );

    expect(merged[0]).toBe(definition);
    expect(merged[0].usedPercent).toBe(12);
  });

  it('uses server lifecycle cycles as the canonical current and previous ranges', () => {
    const merged = mergeAccountQuotaSnapshotWindows(
      [makeDefinition()],
      [
        makeSnapshot({
          availability: 'active',
          activation_generation: 2,
          relationship_kind: 'concurrent_subwindow',
          container_provider_window_id: 'weekly',
          current_cycle: {
            id: 12,
            activation_id: 8,
            state: 'active',
            scheduled_end_ms: 30_000,
            actual_start_ms: 12_000,
            duration_seconds: 18,
            boundary_accuracy: 'exact',
            parent_cycle_id: 11,
            forecast_eligible: true,
          },
          previous_cycle: {
            id: 10,
            activation_id: 8,
            state: 'closed',
            actual_start_ms: 4_000,
            actual_end_ms: 12_000,
            duration_seconds: 18,
            boundary_accuracy: 'exact',
            end_reason: 'early_reset',
            forecast_eligible: false,
          },
        }),
      ]
    );

    expect(merged[0]).toMatchObject({
      cycleStartMs: 12_000,
      cycleEndMs: 30_000,
      durationSeconds: 18,
      availability: 'active',
      activationGeneration: 2,
      relationshipKind: 'concurrent_subwindow',
      containerProviderWindowId: 'weekly',
      currentCycle: { id: 12, actualStartMs: 12_000, parentCycleId: 11 },
      previousCycle: {
        id: 10,
        actualStartMs: 4_000,
        actualEndMs: 12_000,
        endReason: 'early_reset',
        forecastEligible: false,
      },
    });
  });

  it('does not assign a snapshot to another model-scoped quota item', () => {
    const alpha = makeDefinition({
      key: 'shared-alpha',
      providerWindowId: 'shared-window',
      provider: 'antigravity',
      modelScope: { kind: 'models', models: ['model-alpha'], complete: true },
      usedPercent: 10,
    });
    const beta = makeDefinition({
      key: 'shared-beta',
      providerWindowId: 'shared-window',
      provider: 'antigravity',
      modelScope: { kind: 'models', models: ['model-beta'], complete: true },
      usedPercent: 20,
    });
    const merged = mergeAccountQuotaSnapshotWindows(
      [alpha, beta],
      [
        makeSnapshot({
          provider_window_id: 'shared-window',
          model_scope_kind: 'models',
          model_ids: ['model-beta'],
          used_percent: 72,
        }),
        makeSnapshot({
          provider_window_id: 'shared-window',
          model_scope_kind: 'models',
          model_ids: ['model-alpha'],
          used_percent: 31,
        }),
      ]
    );

    expect(merged.find((item) => item.key === 'shared-alpha')?.usedPercent).toBe(31);
    expect(merged.find((item) => item.key === 'shared-beta')?.usedPercent).toBe(72);
  });

  it('round-trips an incomplete model scope without duplicating the live window', () => {
    const incomplete = makeDefinition({
      key: 'shared-incomplete',
      providerWindowId: 'shared-window',
      provider: 'antigravity',
      modelScope: { kind: 'models', models: [], complete: false },
      boundaryAccuracy: 'unknown',
      windowMode: 'unknown',
    });
    const row = {
      selectionKey: 'antigravity.json\u0000auth-1',
      fileName: 'antigravity.json',
      provider: 'antigravity',
      authIndex: 'auth-1',
      accountLabel: 'user@example.com',
      raw: {
        name: 'antigravity.json',
        provider: 'antigravity',
        type: 'antigravity',
        auth_index: 'auth-1',
        account: 'user@example.com',
      },
    } as unknown as AccountRow;
    const [entry] = buildAccountQuotaSnapshotWriteEntries(
      [row],
      new Map([[row.selectionKey, [incomplete]]]),
      { nowMs: 20_000 }
    );

    expect(entry.windows).toEqual([
      expect.objectContaining({
        provider_window_id: 'shared-window',
        model_scope_kind: 'feature',
        model_scope_key: 'scope_unknown',
        model_ids: undefined,
      }),
    ]);

    const merged = mergeAccountQuotaSnapshotWindows(
      [incomplete],
      [
        makeSnapshot({
          ...entry.windows[0],
          stale: false,
        }),
      ],
      { provider: 'antigravity' }
    );

    expect(merged).toHaveLength(1);
    expect(merged[0]).toMatchObject({
      key: 'shared-incomplete',
      providerWindowId: 'shared-window',
      modelScope: { kind: 'models', models: [], complete: false },
    });
  });

  it('encodes an incomplete all scope as fail-closed feature scope', () => {
    const incomplete = makeDefinition({
      key: 'unknown-window',
      providerWindowId: 'future-feature-weekly-0',
      modelScope: { kind: 'all', complete: false },
      boundaryAccuracy: 'unknown',
      windowMode: 'unknown',
    });
    const row = {
      selectionKey: 'codex.json\u0000auth-1',
      fileName: 'codex.json',
      provider: 'codex',
      authIndex: 'auth-1',
      accountLabel: 'user@example.com',
      raw: {
        name: 'codex.json',
        provider: 'codex',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'user@example.com',
      },
    } as unknown as AccountRow;
    const [entry] = buildAccountQuotaSnapshotWriteEntries(
      [row],
      new Map([[row.selectionKey, [incomplete]]]),
      { nowMs: 20_000 }
    );

    expect(entry.windows[0]).toMatchObject({
      model_scope_kind: 'feature',
      model_scope_key: 'scope_unknown',
      model_ids: undefined,
      window_mode: 'unknown',
      boundary_accuracy: 'unknown',
    });

    const merged = mergeAccountQuotaSnapshotWindows([], [makeSnapshot({
      ...entry.windows[0],
      provider_window_id: 'future-feature-weekly-0',
      window_kind: 'weekly',
      stale: false,
    })], { provider: 'codex' });
    expect(merged).toMatchObject([
      expect.objectContaining({
        providerWindowId: 'future-feature-weekly-0',
        modelScope: { kind: 'models', models: [], complete: false },
      }),
    ]);
  });

  it('suppresses a legacy non-main all-scope snapshot when no replacement is complete', () => {
    const merged = mergeAccountQuotaSnapshotWindows(
      [],
      [
        makeSnapshot({
          provider_window_id: 'fast-coding-weekly-0',
          window_kind: 'weekly',
          model_scope_kind: 'all',
          used_percent: 50,
        }),
      ],
      { provider: 'codex' }
    );

    expect(merged).toEqual([]);
  });

  it('gives snapshot-only model scopes distinct display keys while preserving provider identity', () => {
    const snapshots = [
      makeSnapshot({
        provider_window_id: 'shared-window',
        model_scope_kind: 'models',
        model_ids: ['model-beta'],
      }),
      makeSnapshot({
        provider_window_id: 'shared-window',
        model_scope_kind: 'models',
        model_ids: ['model-alpha'],
      }),
    ];

    const merged = mergeAccountQuotaSnapshotWindows([], snapshots, { provider: 'antigravity' });
    const keys = merged.map((item) => item.key);

    expect(merged).toHaveLength(2);
    expect(new Set(keys).size).toBe(2);
    expect(merged.every((item) => item.providerWindowId === 'shared-window')).toBe(true);
    expect(merged.every((item) => item.display.key === item.key)).toBe(true);
    expect(
      mergeAccountQuotaSnapshotWindows([], [...snapshots].reverse(), {
        provider: 'antigravity',
      })
        .map((item) => item.key)
        .sort()
    ).toEqual([...keys].sort());
  });

  it('suppresses a legacy Spark all-scope snapshot when scoped evidence exists', () => {
    const merged = mergeAccountQuotaSnapshotWindows(
      [],
      [
        makeSnapshot({
          provider_window_id: 'gpt-5-3-codex-spark-weekly-0',
          window_kind: 'weekly',
          model_scope_kind: 'all',
          used_percent: 50,
          observed_at_ms: 10_000,
        }),
        makeSnapshot({
          provider_window_id: 'spark-weekly-0',
          window_kind: 'weekly',
          model_scope_kind: 'models',
          model_ids: [CODEX_SPARK_MODEL_ID],
          used_percent: 0,
          observed_at_ms: 20_000,
        }),
      ],
      { provider: 'codex' }
    );

    expect(merged).toHaveLength(1);
    expect(merged[0]).toMatchObject({
      providerWindowId: 'spark-weekly-0',
      usedPercent: 0,
      modelScope: {
        kind: 'models',
        models: [CODEX_SPARK_MODEL_ID],
        complete: true,
      },
    });
  });

  it('suppresses a legacy display-label Spark snapshot through the live alias', () => {
    const definition = makeDefinition({
      key: 'spark-weekly-0',
      providerWindowId: 'spark-weekly-0',
      provider: 'codex',
      modelScope: {
        kind: 'models',
        models: [CODEX_SPARK_MODEL_ID],
        complete: true,
      },
      providerWindowAliases: ['fast-coding-weekly-0'],
    });
    const merged = mergeAccountQuotaSnapshotWindows(
      [definition],
      [
        makeSnapshot({
          provider_window_id: 'fast-coding-weekly-0',
          window_kind: 'weekly',
          model_scope_kind: 'all',
          used_percent: 50,
          observed_at_ms: 10_000,
        }),
      ],
      { provider: 'codex' }
    );

    expect(merged).toHaveLength(1);
    expect(merged[0]).toMatchObject({
      providerWindowId: 'spark-weekly-0',
      usedPercent: definition.usedPercent,
      modelScope: definition.modelScope,
    });
  });

  it('suppresses only the matching legacy all-scope snapshot for an incomplete feature', () => {
    const merged = mergeAccountQuotaSnapshotWindows(
      [],
      [
        makeSnapshot({
          provider_window_id: 'future-feature-weekly-0',
          window_kind: 'weekly',
          model_scope_kind: 'all',
          used_percent: 50,
          observed_at_ms: 10_000,
        }),
        makeSnapshot({
          provider_window_id: 'future-feature-weekly-0',
          window_kind: 'weekly',
          model_scope_kind: 'feature',
          model_scope_key: 'future_feature',
          used_percent: 0,
          observed_at_ms: 20_000,
        }),
        makeSnapshot({
          provider_window_id: 'other-feature-weekly-0',
          window_kind: 'weekly',
          model_scope_kind: 'all',
          used_percent: 25,
          observed_at_ms: 15_000,
        }),
      ],
      { provider: 'codex' }
    );

    expect(merged).toHaveLength(2);
    expect(
      merged.find((item) => item.providerWindowId === 'future-feature-weekly-0')
    ).toMatchObject({
      usedPercent: 0,
      modelScope: { kind: 'feature', key: 'future_feature', complete: false },
    });
    expect(merged.find((item) => item.providerWindowId === 'other-feature-weekly-0')).toMatchObject(
      {
        usedPercent: 25,
        modelScope: { kind: 'all', complete: true },
      }
    );
  });

  it('keeps an explicitly incomplete all-scope replacement from restoring legacy account-wide usage', () => {
    const definition = makeDefinition({
      key: 'future-feature-weekly-0',
      providerWindowId: 'future-feature-weekly-0',
      provider: 'codex',
      kind: 'weekly',
      modelScope: { kind: 'all', complete: false },
      windowMode: 'unknown',
      boundaryAccuracy: 'unknown',
    });
    const merged = mergeAccountQuotaSnapshotWindows(
      [definition],
      [
        makeSnapshot({
          provider_window_id: 'future-feature-weekly-0',
          window_kind: 'weekly',
          model_scope_kind: 'all',
          used_percent: 75,
        }),
      ],
      { provider: 'codex' }
    );

    expect(merged).toHaveLength(1);
    expect(merged[0]).toBe(definition);
    expect(merged[0].modelScope).toEqual({ kind: 'all', complete: false });
  });

  it('adds snapshot-only rolling windows and keeps them ahead of non-window quotas', () => {
    const merged = mergeAccountQuotaSnapshotWindows(
      [
        makeDefinition({
          key: 'billing',
          providerWindowId: 'billing',
          provider: 'xai',
          kind: 'billing',
          windowMode: 'non_window',
          durationSeconds: null,
        }),
      ],
      [
        makeSnapshot({
          provider_window_id: 'included-free-rolling-24h',
          window_kind: 'rolling_24h',
          window_mode: 'rolling',
          model_scope_kind: 'models',
          model_scope_key: 'grok-4.5-build-free',
          model_ids: ['grok-4.5-build-free'],
          source: 'response_body',
          boundary_accuracy: 'estimated',
          cycle_start_ms: undefined,
          cycle_end_ms: 86_410_000,
          duration_seconds: 86_400,
          used_value: 1_000_000,
          limit_value: 1_000_000,
          quota_unit: 'tokens',
        }),
      ],
      { provider: 'xai', getLabel: () => 'Last 24 hours' }
    );

    expect(merged.map((item) => item.providerWindowId)).toEqual([
      'included-free-rolling-24h',
      'billing',
    ]);
    expect(merged[0]).toMatchObject({
      provider: 'xai',
      label: 'Last 24 hours',
      windowMode: 'rolling',
      observationSource: 'response_body',
      boundaryAccuracy: 'estimated',
      durationSeconds: 86_400,
    });
    expect(merged[0].display.amountLabel).toBe('1000000 / 1000000 tokens');
  });

  it('writes only standardized allowlisted fields', () => {
    const row = {
      selectionKey: 'codex.json\u0000auth-1',
      fileName: 'codex.json',
      provider: 'codex',
      authIndex: 'auth-1',
      accountLabel: 'user@example.com',
      raw: {
        name: 'codex.json',
        provider: 'codex',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'user@example.com',
        access_token: 'must-not-leak',
      },
    } as unknown as AccountRow;
    const entries = buildAccountQuotaSnapshotWriteEntries(
      [row],
      new Map([[row.selectionKey, [makeDefinition()]]]),
      { nowMs: 20_000 }
    );

    expect(entries).toHaveLength(1);
    expect(JSON.stringify(entries)).not.toContain('must-not-leak');
    expect(entries[0].windows[0]).toMatchObject({
      provider_window_id: 'five-hour',
      source: 'api_query',
      boundary_accuracy: 'exact',
    });
  });

  it('does not persist live definitions when an observation provider has no observation', () => {
    const row = {
      selectionKey: 'codex.json\u0000auth-1',
      fileName: 'codex.json',
      provider: 'codex',
      authIndex: 'auth-1',
      accountLabel: 'user@example.com',
      raw: {
        name: 'codex.json',
        provider: 'codex',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'user@example.com',
      },
    } as unknown as AccountRow;

    expect(
      buildAccountQuotaSnapshotWriteEntries(
        [row],
        new Map([[row.selectionKey, [makeDefinition()]]]),
        { getObservation: () => undefined }
      )
    ).toEqual([]);
  });

  it('writes a complete empty provider inventory and links Codex subwindows', () => {
    const row = {
      selectionKey: 'codex.json\u0000auth-1',
      fileName: 'codex.json',
      provider: 'codex',
      authIndex: 'auth-1',
      accountLabel: 'user@example.com',
      raw: {
        name: 'codex.json',
        provider: 'codex',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'user@example.com',
      },
    } as unknown as AccountRow;
    const observation = {
      source: 'api_query' as const,
      source_observation_id: 'provider-query-1',
      observed_at_ms: 20_000,
      inventory_scope_key: 'codex:rate-limits',
      inventory_mode: 'complete' as const,
    };

    const empty = buildAccountQuotaSnapshotWriteEntries([row], new Map([[row.selectionKey, []]]), {
      getObservation: () => observation,
    });
    expect(empty).toEqual([
      expect.objectContaining({
        observation,
        windows: [],
      }),
    ]);

    const partialEmpty = buildAccountQuotaSnapshotWriteEntries(
      [row],
      new Map([[row.selectionKey, []]]),
      {
        getObservation: () => ({ ...observation, inventory_mode: 'partial' }),
      }
    );
    expect(partialEmpty).toEqual([]);

    const weekly = makeDefinition({
      key: 'weekly',
      providerWindowId: 'weekly',
      label: '7D',
      kind: 'weekly',
      cycleEndMs: 604_801_000,
      durationSeconds: 604_800,
    });
    const linked = buildAccountQuotaSnapshotWriteEntries(
      [row],
      new Map([[row.selectionKey, [makeDefinition(), weekly]]]),
      { getObservation: () => observation }
    );
    expect(linked[0].windows[0]).toMatchObject({
      provider_window_id: 'five-hour',
      relationship_kind: 'concurrent_subwindow',
      container_provider_window_id: 'weekly',
    });
  });

  it('links each Codex subwindow to the weekly window with the same model scope', () => {
    const row = {
      selectionKey: 'codex.json\u0000auth-1',
      fileName: 'codex.json',
      provider: 'codex',
      authIndex: 'auth-1',
      accountLabel: 'user@example.com',
      raw: {
        name: 'codex.json',
        provider: 'codex',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'user@example.com',
      },
    } as unknown as AccountRow;
    const scopedDefinitions = [
      makeDefinition({
        key: 'five-gemini',
        providerWindowId: 'five-gemini',
        modelScope: { kind: 'family', key: 'gemini', complete: true },
      }),
      makeDefinition({
        key: 'weekly-claude',
        providerWindowId: 'weekly-claude',
        kind: 'weekly',
        modelScope: { kind: 'family', key: 'claude_gpt', complete: true },
      }),
      makeDefinition({
        key: 'five-claude',
        providerWindowId: 'five-claude',
        modelScope: { kind: 'family', key: 'claude_gpt', complete: true },
      }),
      makeDefinition({
        key: 'weekly-gemini',
        providerWindowId: 'weekly-gemini',
        kind: 'weekly',
        modelScope: { kind: 'family', key: 'gemini', complete: true },
      }),
    ];

    const [entry] = buildAccountQuotaSnapshotWriteEntries(
      [row],
      new Map([[row.selectionKey, scopedDefinitions]])
    );
    const byID = new Map(entry.windows.map((window) => [window.provider_window_id, window]));
    expect(byID.get('five-gemini')).toMatchObject({
      relationship_kind: 'concurrent_subwindow',
      container_provider_window_id: 'weekly-gemini',
    });
    expect(byID.get('five-claude')).toMatchObject({
      relationship_kind: 'concurrent_subwindow',
      container_provider_window_id: 'weekly-claude',
    });
  });

  it('links multiple Codex quota families independently within the same model scope', () => {
    const row = {
      selectionKey: 'codex.json\u0000auth-1',
      fileName: 'codex.json',
      provider: 'codex',
      authIndex: 'auth-1',
      accountLabel: 'user@example.com',
      raw: {
        name: 'codex.json',
        provider: 'codex',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'user@example.com',
      },
    } as unknown as AccountRow;
    const definitions = [
      makeDefinition(),
      makeDefinition({
        key: 'weekly',
        providerWindowId: 'weekly',
        kind: 'weekly',
        durationSeconds: 604_800,
      }),
      makeDefinition({
        key: 'code-review-five-hour',
        providerWindowId: 'code-review-five-hour',
      }),
      makeDefinition({
        key: 'code-review-weekly',
        providerWindowId: 'code-review-weekly',
        kind: 'weekly',
        durationSeconds: 604_800,
      }),
      makeDefinition({
        key: 'credits-five-hour-0',
        providerWindowId: 'credits-five-hour-0',
      }),
      makeDefinition({
        key: 'credits-weekly-0',
        providerWindowId: 'credits-weekly-0',
        kind: 'weekly',
        durationSeconds: 604_800,
      }),
    ];

    const [entry] = buildAccountQuotaSnapshotWriteEntries(
      [row],
      new Map([[row.selectionKey, definitions]])
    );
    const byID = new Map(entry.windows.map((window) => [window.provider_window_id, window]));
    expect(byID.get('five-hour')).toMatchObject({
      relationship_kind: 'concurrent_subwindow',
      container_provider_window_id: 'weekly',
    });
    expect(byID.get('code-review-five-hour')).toMatchObject({
      relationship_kind: 'concurrent_subwindow',
      container_provider_window_id: 'code-review-weekly',
    });
    expect(byID.get('credits-five-hour-0')).toMatchObject({
      relationship_kind: 'concurrent_subwindow',
      container_provider_window_id: 'credits-weekly-0',
    });
  });

  it('does not link a Codex subwindow to a sole weekly window from another scope', () => {
    const row = {
      selectionKey: 'codex.json\u0000auth-1',
      fileName: 'codex.json',
      provider: 'codex',
      authIndex: 'auth-1',
      accountLabel: 'user@example.com',
      raw: {
        name: 'codex.json',
        provider: 'codex',
        type: 'codex',
        auth_index: 'auth-1',
        account: 'user@example.com',
      },
    } as unknown as AccountRow;
    const [entry] = buildAccountQuotaSnapshotWriteEntries(
      [row],
      new Map([
        [
          row.selectionKey,
          [
            makeDefinition({
              key: 'five-gemini',
              providerWindowId: 'five-hour',
              modelScope: { kind: 'family', key: 'gemini', complete: true },
            }),
            makeDefinition({
              key: 'weekly-claude',
              providerWindowId: 'weekly',
              kind: 'weekly',
              modelScope: { kind: 'family', key: 'claude_gpt', complete: true },
            }),
          ],
        ],
      ])
    );

    expect(entry.windows[0].relationship_kind).toBeUndefined();
    expect(entry.windows[0].container_provider_window_id).toBeUndefined();
  });

  it('matches snapshot scopes with normalized key casing', () => {
    const merged = mergeAccountQuotaSnapshotWindows(
      [
        makeDefinition({
          modelScope: { kind: 'family', key: 'Gemini', complete: true },
        }),
      ],
      [
        makeSnapshot({
          model_scope_kind: 'family',
          model_scope_key: ' gemini ',
          used_percent: 35,
        }),
      ]
    );

    expect(merged).toHaveLength(1);
    expect(merged[0].usedPercent).toBe(35);
  });

  it('uses field-level snapshot provenance for Codex reset-credit display fallback', () => {
    const merged = mergeCodexResetCreditsFromQuotaSnapshots(
      {
        status: 'error',
        windows: [],
        fetchedAtMs: 10_000,
        rateLimitResetCreditsAvailableCount: null,
      },
      [
        makeSnapshot({
          observed_at_ms: 20_000,
          reset_credits_available: 2,
          reset_credits: [{ id: 'credit-1', expires_at_ms: 100_000 }],
          field_sources: {
            reset_credits_available: { source: 'api_query', observed_at_ms: 15_000 },
            reset_credits: { source: 'api_query', observed_at_ms: 15_000 },
          },
        }),
      ]
    );

    expect(merged).toMatchObject({
      status: 'error',
      rateLimitResetCreditsAvailableCount: 2,
      rateLimitResetCredits: [
        {
          id: 'credit-1',
          status: 'available',
          expiresAt: new Date(100_000).toISOString(),
        },
      ],
    });
  });

  it('does not restore reset credits from stale or inactive snapshots', () => {
    const quota = {
      status: 'success' as const,
      windows: [],
      fetchedAtMs: 30_000,
      rateLimitResetCreditsAvailableCount: 1,
      rateLimitResetCredits: [
        {
          id: 'local-credit',
          status: 'available' as const,
          grantedAt: '',
          expiresAt: new Date(100_000).toISOString(),
        },
      ],
    };

    const merged = mergeCodexResetCreditsFromQuotaSnapshots(quota, [
      makeSnapshot({
        stale: true,
        reset_credits_available: 9,
        reset_credits: [{ id: 'stale-credit', expires_at_ms: 200_000 }],
      }),
      makeSnapshot({
        availability: 'inactive',
        reset_credits_available: 8,
        reset_credits: [{ id: 'inactive-credit', expires_at_ms: 300_000 }],
      }),
    ]);

    expect(merged).toBe(quota);
  });

  it('clears older reset-credit details when a newer zero count is observed', () => {
    const merged = mergeCodexResetCreditsFromQuotaSnapshots(
      {
        status: 'success',
        windows: [],
        fetchedAtMs: 10_000,
        rateLimitResetCreditsAvailableCount: 1,
        rateLimitResetCredits: [
          {
            id: 'old-credit',
            status: 'available',
            grantedAt: '',
            expiresAt: new Date(100_000).toISOString(),
          },
        ],
      },
      [
        makeSnapshot({
          observed_at_ms: 20_000,
          reset_credits_available: 0,
          field_sources: {
            reset_credits_available: { source: 'api_query', observed_at_ms: 20_000 },
          },
        }),
      ]
    );

    expect(merged?.rateLimitResetCreditsAvailableCount).toBe(0);
    expect(merged?.rateLimitResetCredits).toEqual([]);
  });

  it('uses a deterministic tie-break for snapshots observed at the same time', () => {
    const snapshots = [
      makeSnapshot({
        provider_window_id: 'z-window',
        source_observation_id: 'z-observation',
        reset_credits_available: 9,
        field_sources: {
          reset_credits_available: { source: 'api_query', observed_at_ms: 20_000 },
        },
      }),
      makeSnapshot({
        provider_window_id: 'a-window',
        source_observation_id: 'a-observation',
        reset_credits_available: 3,
        field_sources: {
          reset_credits_available: { source: 'api_query', observed_at_ms: 20_000 },
        },
      }),
    ];
    const forward = mergeCodexResetCreditsFromQuotaSnapshots(undefined, snapshots);
    const reverse = mergeCodexResetCreditsFromQuotaSnapshots(undefined, [...snapshots].reverse());

    expect(forward?.rateLimitResetCreditsAvailableCount).toBe(3);
    expect(reverse?.rateLimitResetCreditsAvailableCount).toBe(3);
  });
});
