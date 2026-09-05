import type { TFunction } from 'i18next';
import { describe, expect, it } from 'vitest';
import type { AccountQuotaSnapshotWindow } from '@/services/api/usageService';
import { CODEX_SPARK_MODEL_ID } from '@/utils/quota/codexQuota';
import type { AccountQuotaWindowDefinition } from './accountQuotaWindowDefinitions';
import { buildAccountWindowUsageTargetEntries } from './accountWindowUsageRows';
import {
  buildAccountQuotaSnapshotWriteEntries,
  filterCurrentAccountQuotaWindowDefinitions,
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

const makeSnapshotTestRow = (provider: AccountRow['provider'] = 'antigravity'): AccountRow =>
  ({
    selectionKey: `${provider}.json\u0000auth-1`,
    fileName: `${provider}.json`,
    provider,
    authIndex: 'auth-1',
    accountLabel: 'user@example.com',
    raw: {
      name: `${provider}.json`,
      provider,
      type: provider,
      auth_index: 'auth-1',
      account: 'user@example.com',
    },
  }) as unknown as AccountRow;

const codexSnapshotT = ((key: string, options?: Record<string, string | number>) => {
  const name = options?.name ?? '';
  const duration = options?.duration ?? '';
  const labels: Record<string, string> = {
    'codex_quota.additional_primary_window': `${name} 5-hour limit`,
    'codex_quota.additional_secondary_window': `${name} weekly limit`,
    'codex_quota.additional_monthly_window': `${name} monthly limit`,
    'codex_quota.additional_generic_window': `${name} ${duration} limit`,
    'codex_quota.code_review_primary_window': 'Code review 5-hour limit',
    'codex_quota.code_review_secondary_window': 'Code review weekly limit',
    'codex_quota.code_review_monthly_window': 'Code review monthly limit',
    'codex_quota.code_review_generic_window': `Code review ${duration} limit`,
  };
  return labels[key] ?? key;
}) as TFunction;

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
      label: 'Demo Model A',
      modelScope: { kind: 'models', models: [], complete: false },
      boundaryAccuracy: 'unknown',
      windowMode: 'unknown',
    });
    incomplete.display = {
      ...incomplete.display,
      label: 'Demo Model A',
      scopeDisplayName: 'Demo Model A',
      modelScope: incomplete.modelScope,
      source: 'antigravity',
    };
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
        scope_display_name: 'Demo Model A',
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
      display: { label: 'Demo Model A', scopeDisplayName: 'Demo Model A' },
    });
  });

  it('restores a snapshot-only scoped model name and preserves it on write-back', () => {
    const snapshot = makeSnapshot({
      provider_window_id: 'weekly-scoped-demo',
      window_kind: 'weekly',
      model_scope_kind: 'feature',
      model_scope_key: 'scope_unknown',
      model_ids: undefined,
      scope_display_name: 'Demo Model A',
    });
    const merged = mergeAccountQuotaSnapshotWindows([], [snapshot], {
      provider: 'claude',
      getLabel: () => 'detail_snapshot_window_weekly',
    });

    expect(merged).toHaveLength(1);
    expect(merged[0]).toMatchObject({
      label: 'Demo Model A',
      modelScope: { kind: 'models', models: [], complete: false },
      display: { label: 'Demo Model A', scopeDisplayName: 'Demo Model A' },
    });

    const row = makeSnapshotTestRow('claude');
    const [entry] = buildAccountQuotaSnapshotWriteEntries(
      [row],
      new Map([[row.selectionKey, merged]])
    );
    expect(entry.windows[0]).toMatchObject({
      model_scope_kind: 'feature',
      model_scope_key: 'scope_unknown',
      model_ids: undefined,
      scope_display_name: 'Demo Model A',
    });
  });

  it('uses the legacy snapshot label fallback when no display name was stored', () => {
    const merged = mergeAccountQuotaSnapshotWindows(
      [],
      [
        makeSnapshot({
          provider_window_id: 'weekly',
          window_kind: 'weekly',
          scope_display_name: undefined,
        }),
      ],
      {
        provider: 'claude',
        getLabel: () => 'detail_snapshot_window_weekly',
      }
    );

    expect(merged).toHaveLength(1);
    expect(merged[0].label).toBe('detail_snapshot_window_weekly');
    expect(merged[0].display.scopeDisplayName).toBeUndefined();
  });

  it('restores a Codex dynamic Additional Rate Limit with the current locale label', () => {
    const snapshot = makeSnapshot({
      provider_window_id: 'gpt-reserve-weekly-0',
      window_kind: 'weekly',
      model_scope_kind: 'feature',
      model_scope_key: 'gpt_reserve',
      scope_display_name: 'gpt-reserve',
    });
    const merged = mergeAccountQuotaSnapshotWindows([], [snapshot], {
      provider: 'codex',
      t: codexSnapshotT,
      getLabel: () => 'detail_snapshot_window_weekly',
    });

    expect(merged).toHaveLength(1);
    expect(merged[0]).toMatchObject({
      label: 'gpt-reserve weekly limit',
      display: { label: 'gpt-reserve weekly limit', scopeDisplayName: 'gpt-reserve' },
    });
  });

  it('restores Codex canonical Code Review and Spark labels without persisted translations', () => {
    const codeReview = mergeAccountQuotaSnapshotWindows(
      [],
      [
        makeSnapshot({
          provider_window_id: 'code-review-weekly',
          window_kind: 'weekly',
          model_scope_kind: 'feature',
          model_scope_key: 'code_review',
          scope_display_name: undefined,
        }),
      ],
      { provider: 'codex', t: codexSnapshotT }
    );
    const spark = mergeAccountQuotaSnapshotWindows(
      [],
      [
        makeSnapshot({
          provider_window_id: 'spark-weekly-0',
          window_kind: 'weekly',
          model_scope_kind: 'models',
          model_ids: [CODEX_SPARK_MODEL_ID],
          scope_display_name: undefined,
        }),
      ],
      { provider: 'codex', t: codexSnapshotT }
    );

    expect(codeReview[0]?.label).toBe('Code review weekly limit');
    expect(spark[0]?.label).toBe('Spark weekly limit');
  });

  it('rebuilds a dynamic Codex label from the same raw snapshot name in another locale', () => {
    const snapshot = makeSnapshot({
      provider_window_id: 'gpt-reserve-weekly-0',
      window_kind: 'weekly',
      model_scope_kind: 'feature',
      model_scope_key: 'gpt_reserve',
      scope_display_name: 'gpt-reserve',
    });
    const english = mergeAccountQuotaSnapshotWindows([], [snapshot], {
      provider: 'codex',
      t: codexSnapshotT,
    });
    const chineseT = ((key: string, options?: Record<string, string | number>) =>
      key === 'codex_quota.additional_secondary_window'
        ? `${options?.name ?? ''} 周限额`
        : key) as TFunction;
    const chinese = mergeAccountQuotaSnapshotWindows([], [snapshot], {
      provider: 'codex',
      t: chineseT,
    });

    expect(english[0]?.label).toBe('gpt-reserve weekly limit');
    expect(chinese[0]?.label).toBe('gpt-reserve 周限额');
    expect(english[0]?.display.scopeDisplayName).toBe('gpt-reserve');
    expect(chinese[0]?.display.scopeDisplayName).toBe('gpt-reserve');
  });

  it('keeps the live definition label ahead of an older snapshot display name', () => {
    const live = makeDefinition({
      provider: 'claude',
      label: 'New Model Name',
      observedAtMs: 10_000,
    });
    live.display = {
      ...live.display,
      label: 'New Model Name',
      scopeDisplayName: 'New Model Name',
      source: 'claude',
      modelScope: live.modelScope,
    };
    const merged = mergeAccountQuotaSnapshotWindows(
      [live],
      [
        makeSnapshot({
          observed_at_ms: 5_000,
          scope_display_name: 'Old Model Name',
        }),
      ],
      { provider: 'claude' }
    );

    expect(merged).toHaveLength(1);
    expect(merged[0]).toMatchObject({
      label: 'New Model Name',
      display: { label: 'New Model Name', scopeDisplayName: 'New Model Name' },
    });
  });

  it('keeps active, pending, and legacy windows in the current view but filters inactive windows', () => {
    const definitions = [
      makeDefinition({ key: 'active', providerWindowId: 'active', availability: 'active' }),
      makeDefinition({
        key: 'pending',
        providerWindowId: 'pending',
        availability: 'pending_absent',
      }),
      makeDefinition({ key: 'inactive', providerWindowId: 'inactive', availability: 'inactive' }),
      makeDefinition({ key: 'legacy', providerWindowId: 'legacy' }),
    ];

    expect(filterCurrentAccountQuotaWindowDefinitions(definitions).map((item) => item.key)).toEqual(
      ['active', 'pending', 'legacy']
    );
  });

  it('filters only the shadowed pending lifecycle row from an ambiguous current set', () => {
    const definitions = [
      makeDefinition({
        key: 'future-feature-weekly-0',
        providerWindowId: 'future-feature-weekly-0',
        availability: 'pending_absent',
        currentHidden: true,
      }),
      makeDefinition({
        key: 'cpamp:ambiguous:future-feature-weekly-0',
        providerWindowId: 'cpamp:ambiguous:future-feature-weekly-0',
        identityAmbiguous: true,
      }),
      makeDefinition({
        key: 'cpamp:ambiguous:future-feature-weekly-1',
        providerWindowId: 'cpamp:ambiguous:future-feature-weekly-1',
        identityAmbiguous: true,
      }),
    ];

    expect(filterCurrentAccountQuotaWindowDefinitions(definitions).map((item) => item.key)).toEqual(
      ['cpamp:ambiguous:future-feature-weekly-0', 'cpamp:ambiguous:future-feature-weekly-1']
    );
  });

  it('clears an older current-hidden flag when newer local positive quota evidence wins', () => {
    const local = makeDefinition({
      observedAtMs: 400,
      availability: 'active',
      currentHidden: true,
      usedPercent: 40,
    });
    const snapshot = makeSnapshot({
      observed_at_ms: 100,
      availability: 'pending_absent',
      last_seen_at_ms: 300,
      missing_since_ms: 300,
      current_hidden: true,
    });

    const [merged] = mergeAccountQuotaSnapshotWindows([local], [snapshot]);

    expect(merged).toMatchObject({ availability: 'active', currentHidden: false });
    expect(merged?.display.currentHidden).toBe(false);
    expect(filterCurrentAccountQuotaWindowDefinitions(merged ? [merged] : [])).toHaveLength(1);
  });

  it('applies current-hidden lifecycle evidence without changing quota freshness', () => {
    const local = makeDefinition({ observedAtMs: 100, usedPercent: 40 });
    const snapshot = makeSnapshot({
      observed_at_ms: 200,
      used_percent: 10,
      availability: 'pending_absent',
      last_seen_at_ms: 200,
      missing_since_ms: 200,
      current_hidden: true,
    });

    const [merged] = mergeAccountQuotaSnapshotWindows([local], [snapshot]);

    expect(merged).toMatchObject({
      usedPercent: 10,
      availability: 'pending_absent',
      currentHidden: true,
    });
    expect(filterCurrentAccountQuotaWindowDefinitions(merged ? [merged] : [])).toHaveLength(0);
  });

  it('uses an inactive snapshot as a tombstone for a stale local Codex definition', () => {
    const local = makeDefinition({
      key: 'gpt-reserve-weekly-0',
      providerWindowId: 'gpt-reserve-weekly-0',
      kind: 'weekly',
      durationSeconds: 604_800,
      label: 'gpt-reserve weekly limit',
      modelScope: { kind: 'feature', key: 'gpt_reserve', complete: false },
    });
    const snapshot = makeSnapshot({
      provider_window_id: 'gpt-reserve-weekly-0',
      window_kind: 'weekly',
      model_scope_kind: 'feature',
      model_scope_key: 'gpt_reserve',
      availability: 'inactive',
      observed_at_ms: 10_000,
      deactivated_at_ms: 30_000,
    });

    const merged = mergeAccountQuotaSnapshotWindows([local], [snapshot], { provider: 'codex' });
    expect(merged).toHaveLength(1);
    expect(merged[0]).toMatchObject({
      providerWindowId: 'gpt-reserve-weekly-0',
      availability: 'inactive',
    });
    expect(filterCurrentAccountQuotaWindowDefinitions(merged)).toEqual([]);
  });

  it('merges newer inactive lifecycle evidence without replacing newer local quota data', () => {
    const local = makeDefinition({ observedAtMs: 200, usedPercent: 40 });
    const snapshot = makeSnapshot({
      observed_at_ms: 100,
      used_percent: 10,
      availability: 'inactive',
      deactivated_at_ms: 300,
    });

    const merged = mergeAccountQuotaSnapshotWindows([local], [snapshot]);

    expect(merged[0]).toMatchObject({ usedPercent: 40, availability: 'inactive' });
    expect(filterCurrentAccountQuotaWindowDefinitions(merged)).toEqual([]);
  });

  it('merges newer pending lifecycle evidence while retaining newer local quota data', () => {
    const local = makeDefinition({ observedAtMs: 200, usedPercent: 40 });
    const snapshot = makeSnapshot({
      observed_at_ms: 100,
      used_percent: 10,
      availability: 'pending_absent',
      missing_since_ms: 300,
    });

    const merged = mergeAccountQuotaSnapshotWindows([local], [snapshot]);

    expect(merged[0]).toMatchObject({ usedPercent: 40, availability: 'pending_absent' });
    expect(filterCurrentAccountQuotaWindowDefinitions(merged)).toHaveLength(1);
  });

  it('keeps a newer local positive observation visible over an older inactive tombstone', () => {
    const local = makeDefinition({ observedAtMs: 400, usedPercent: 40 });
    const snapshot = makeSnapshot({
      observed_at_ms: 100,
      availability: 'inactive',
      deactivated_at_ms: 300,
    });

    const merged = mergeAccountQuotaSnapshotWindows([local], [snapshot]);

    expect(merged[0]).toBe(local);
    expect(filterCurrentAccountQuotaWindowDefinitions(merged)).toHaveLength(1);
  });

  it('applies a newer quota snapshot when its quota evidence is fresher', () => {
    const local = makeDefinition({ observedAtMs: 100, usedPercent: 40 });
    const snapshot = makeSnapshot({ observed_at_ms: 200, used_percent: 10 });

    const merged = mergeAccountQuotaSnapshotWindows([local], [snapshot]);

    expect(merged[0]).toMatchObject({ usedPercent: 10, observationSource: 'response_header' });
  });

  it('applies newer display metadata independently from older quota evidence', () => {
    const local = makeDefinition({
      provider: 'claude',
      observedAtMs: 200,
      usedPercent: 40,
      label: 'Old Model Name',
      display: {
        ...makeDefinition().display,
        label: 'Old Model Name',
        scopeDisplayName: 'Old Model Name',
        source: 'claude',
      },
    });
    const snapshot = makeSnapshot({
      observed_at_ms: 100,
      used_percent: 10,
      scope_display_name: 'New Model Name',
      field_sources: {
        scope_display_name: { source: 'api_query', observed_at_ms: 300 },
      },
    });

    const merged = mergeAccountQuotaSnapshotWindows([local], [snapshot], { provider: 'claude' });

    expect(merged[0]).toMatchObject({
      usedPercent: 40,
      label: 'New Model Name',
      display: { label: 'New Model Name', scopeDisplayName: 'New Model Name' },
    });
  });

  it('applies server display evidence when newer local quota has no display evidence', () => {
    const local = makeDefinition({
      key: 'gpt-reserve-weekly-0',
      providerWindowId: 'gpt-reserve-weekly-0',
      kind: 'weekly',
      observedAtMs: 300,
      usedPercent: 40,
      modelScope: { kind: 'feature', key: 'gpt_reserve', complete: false },
    });
    const snapshot = makeSnapshot({
      provider_window_id: 'gpt-reserve-weekly-0',
      window_kind: 'weekly',
      model_scope_kind: 'feature',
      model_scope_key: 'gpt_reserve',
      observed_at_ms: 100,
      used_percent: 10,
      scope_display_name: 'Model A',
      field_sources: {
        scope_display_name: { source: 'api_query', observed_at_ms: 200 },
      },
    });

    const merged = mergeAccountQuotaSnapshotWindows([local], [snapshot], {
      provider: 'codex',
      t: codexSnapshotT,
    });

    expect(merged[0]).toMatchObject({
      usedPercent: 40,
      label: 'Model A weekly limit',
      display: { label: 'Model A weekly limit', scopeDisplayName: 'Model A' },
    });
  });

  it('does not let an older or blank display snapshot replace local presentation metadata', () => {
    const local = makeDefinition({
      provider: 'claude',
      observedAtMs: 300,
      label: 'New Model Name',
      display: {
        ...makeDefinition().display,
        label: 'New Model Name',
        scopeDisplayName: 'New Model Name',
        source: 'claude',
      },
    });
    const older = makeSnapshot({
      observed_at_ms: 200,
      scope_display_name: 'Old Model Name',
      field_sources: {
        scope_display_name: { source: 'api_query', observed_at_ms: 200 },
      },
    });
    const blank = makeSnapshot({ observed_at_ms: 400, scope_display_name: '   ' });

    const olderMerged = mergeAccountQuotaSnapshotWindows([local], [older], { provider: 'claude' });
    const blankMerged = mergeAccountQuotaSnapshotWindows([local], [blank], { provider: 'claude' });

    expect(olderMerged[0]).toMatchObject({
      label: 'New Model Name',
      display: { label: 'New Model Name', scopeDisplayName: 'New Model Name' },
    });
    expect(blankMerged[0]).toMatchObject({
      label: 'New Model Name',
      display: { label: 'New Model Name', scopeDisplayName: 'New Model Name' },
    });
  });

  it('does not create usage targets for inactive definitions after current filtering', () => {
    const row = makeSnapshotTestRow('codex');
    const active = makeDefinition({ key: 'active', providerWindowId: 'active' });
    const inactive = makeDefinition({
      key: 'gpt-reserve-weekly-0',
      providerWindowId: 'gpt-reserve-weekly-0',
      availability: 'inactive',
    });
    const current = filterCurrentAccountQuotaWindowDefinitions([active, inactive]);
    const entries = buildAccountWindowUsageTargetEntries(
      [row],
      new Map([[row.selectionKey, current]]),
      10_000
    );

    expect(entries).not.toHaveLength(0);
    expect(entries.every((entry) => entry.windowKey !== 'gpt-reserve-weekly-0')).toBe(true);
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

    const merged = mergeAccountQuotaSnapshotWindows(
      [],
      [
        makeSnapshot({
          ...entry.windows[0],
          provider_window_id: 'future-feature-weekly-0',
          window_kind: 'weekly',
          stale: false,
        }),
      ],
      { provider: 'codex' }
    );
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

describe('stale server ambiguous current-set suppression', () => {
  const completeCodexObservation = (observedAtMs: number) => ({
    observed_at_ms: observedAtMs,
    inventory_scope_key: 'codex:rate-limits',
    inventory_mode: 'complete' as const,
  });
  const ambiguousSnapshot = (
    index: number,
    observedAtMs: number
  ): AccountQuotaSnapshotWindow =>
    makeSnapshot({
      provider_window_id: `cpamp:ambiguous:future-feature-weekly-${index}`,
      window_kind: 'weekly',
      model_scope_kind: 'feature',
      model_scope_key: 'future_feature',
      observed_at_ms: observedAtMs,
      used_percent: 30,
      remaining_percent: 70,
    });
  const uniqueLocalDefinition = () =>
    makeDefinition({
      key: 'future-feature-weekly-0',
      providerWindowId: 'future-feature-weekly-0',
      modelScope: { kind: 'feature', key: 'future_feature', complete: false },
    });

  it('drops stale ambiguous snapshots when a newer local complete inventory exists', () => {
    const merged = mergeAccountQuotaSnapshotWindows(
      [uniqueLocalDefinition()],
      [ambiguousSnapshot(0, 100), ambiguousSnapshot(1, 100)],
      {
        provider: 'codex',
        localObservation: completeCodexObservation(200),
      }
    );

    expect(merged.map((definition) => definition.providerWindowId)).toEqual([
      'future-feature-weekly-0',
    ]);
  });

  it('drops stale ambiguous snapshots when the newer complete inventory is empty', () => {
    const merged = mergeAccountQuotaSnapshotWindows(
      [],
      [ambiguousSnapshot(0, 100), ambiguousSnapshot(1, 100)],
      {
        provider: 'codex',
        localObservation: completeCodexObservation(200),
      }
    );

    expect(merged).toEqual([]);
  });

  it('keeps ambiguous snapshots when the newer local observation is partial', () => {
    const merged = mergeAccountQuotaSnapshotWindows(
      [],
      [ambiguousSnapshot(0, 100), ambiguousSnapshot(1, 100)],
      {
        provider: 'codex',
        localObservation: {
          observed_at_ms: 200,
          inventory_scope_key: 'codex:rate-limits',
          inventory_mode: 'partial',
        },
      }
    );

    expect(merged.map((definition) => definition.providerWindowId)).toEqual([
      'cpamp:ambiguous:future-feature-weekly-0',
      'cpamp:ambiguous:future-feature-weekly-1',
    ]);
  });

  it('keeps ambiguous snapshots that are newer than the local observation', () => {
    const merged = mergeAccountQuotaSnapshotWindows(
      [uniqueLocalDefinition()],
      [ambiguousSnapshot(0, 300)],
      {
        provider: 'codex',
        localObservation: completeCodexObservation(200),
      }
    );

    expect(merged.map((definition) => definition.providerWindowId)).toEqual([
      'future-feature-weekly-0',
      'cpamp:ambiguous:future-feature-weekly-0',
    ]);
  });

  it('keeps server ambiguous evidence on equal timestamps', () => {
    const merged = mergeAccountQuotaSnapshotWindows(
      [uniqueLocalDefinition()],
      [ambiguousSnapshot(0, 200)],
      {
        provider: 'codex',
        localObservation: completeCodexObservation(200),
      }
    );

    expect(merged.map((definition) => definition.providerWindowId)).toEqual([
      'future-feature-weekly-0',
      'cpamp:ambiguous:future-feature-weekly-0',
    ]);
  });

  it('never suppresses identifiable lifecycle snapshots', () => {
    const pending = makeSnapshot({
      provider_window_id: 'future-feature-weekly-1',
      window_kind: 'weekly',
      model_scope_kind: 'feature',
      model_scope_key: 'future_feature',
      observed_at_ms: 100,
      availability: 'pending_absent',
      missing_since_ms: 100,
    });
    const inactive = makeSnapshot({
      provider_window_id: 'legacy-quota-weekly-0',
      window_kind: 'weekly',
      model_scope_kind: 'feature',
      model_scope_key: 'legacy_quota',
      observed_at_ms: 100,
      availability: 'inactive',
      deactivated_at_ms: 100,
    });
    const merged = mergeAccountQuotaSnapshotWindows([], [pending, inactive], {
      provider: 'codex',
      localObservation: completeCodexObservation(200),
    });

    expect(merged.map((definition) => definition.availability)).toEqual([
      'pending_absent',
      'inactive',
    ]);
  });

  it('requires the Codex rate-limits inventory scope before suppressing', () => {
    const merged = mergeAccountQuotaSnapshotWindows([], [ambiguousSnapshot(0, 100)], {
      provider: 'codex',
      localObservation: {
        observed_at_ms: 200,
        inventory_scope_key: 'codex:other-scope',
        inventory_mode: 'complete',
      },
    });

    expect(merged).toHaveLength(1);
  });

  describe('8.1 alias reconciliation', () => {
    it('Case A: reconciles local canonical with older active server snapshot by alias', () => {
      const local = makeDefinition({
        key: 'future-feature-weekly-0',
        providerWindowId: 'future-feature-weekly-0',
        providerWindowAliases: ['old-name-weekly-0'],
        kind: 'weekly',
        modelScope: { kind: 'feature', key: 'future_feature', complete: false },
        observedAtMs: 200,
        usedPercent: 40,
        availability: 'active',
      });
      const server = makeSnapshot({
        provider_window_id: 'old-name-weekly-0',
        window_kind: 'weekly',
        model_scope_kind: 'feature',
        model_scope_key: 'future_feature',
        observed_at_ms: 100,
        used_percent: 10,
        availability: 'active',
      });

      const merged = mergeAccountQuotaSnapshotWindows([local], [server], {
        provider: 'codex',
      });

      expect(merged).toHaveLength(1);
      expect(merged[0].providerWindowId).toBe('future-feature-weekly-0');
      expect(merged[0].usedPercent).toBe(40);
    });

    it('Case B: server pending absent evidence is applied while preserving local canonical quota', () => {
      const local = makeDefinition({
        key: 'future-feature-weekly-0',
        providerWindowId: 'future-feature-weekly-0',
        providerWindowAliases: ['old-name-weekly-0'],
        kind: 'weekly',
        modelScope: { kind: 'feature', key: 'future_feature', complete: false },
        observedAtMs: 200,
        usedPercent: 40,
        availability: 'active',
      });
      const server = makeSnapshot({
        provider_window_id: 'old-name-weekly-0',
        window_kind: 'weekly',
        model_scope_kind: 'feature',
        model_scope_key: 'future_feature',
        observed_at_ms: 100,
        missing_since_ms: 300,
        availability: 'pending_absent',
      });

      const merged = mergeAccountQuotaSnapshotWindows([local], [server], {
        provider: 'codex',
      });

      expect(merged).toHaveLength(1);
      expect(merged[0].providerWindowId).toBe('future-feature-weekly-0');
      expect(merged[0].usedPercent).toBe(40);
      expect(merged[0].availability).toBe('pending_absent');
    });

    it('Case C: older server inactive tombstone does not override newer local positive observation', () => {
      const local = makeDefinition({
        key: 'future-feature-weekly-0',
        providerWindowId: 'future-feature-weekly-0',
        providerWindowAliases: ['old-name-weekly-0'],
        kind: 'weekly',
        modelScope: { kind: 'feature', key: 'future_feature', complete: false },
        observedAtMs: 400,
        usedPercent: 40,
        availability: 'active',
        currentHidden: false,
      });
      const server = makeSnapshot({
        provider_window_id: 'old-name-weekly-0',
        window_kind: 'weekly',
        model_scope_kind: 'feature',
        model_scope_key: 'future_feature',
        observed_at_ms: 100,
        deactivated_at_ms: 300,
        availability: 'inactive',
        current_hidden: true,
      });

      const merged = mergeAccountQuotaSnapshotWindows([local], [server], {
        provider: 'codex',
      });

      expect(merged).toHaveLength(1);
      expect(merged[0].providerWindowId).toBe('future-feature-weekly-0');
      expect(merged[0].availability).toBe('active');
      expect(merged[0].currentHidden).toBe(false);
    });

    it('Case D: fails closed when multiple local definitions match the same server alias', () => {
      const local1 = makeDefinition({
        key: 'future-feature-weekly-0',
        providerWindowId: 'future-feature-weekly-0',
        providerWindowAliases: ['old-name-weekly-0'],
        kind: 'weekly',
        modelScope: { kind: 'feature', key: 'future_feature', complete: false },
        observedAtMs: 200,
      });
      const local2 = makeDefinition({
        key: 'other-feature-weekly-0',
        providerWindowId: 'other-feature-weekly-0',
        providerWindowAliases: ['old-name-weekly-0'],
        kind: 'weekly',
        modelScope: { kind: 'feature', key: 'future_feature', complete: false },
        observedAtMs: 200,
      });
      const server = makeSnapshot({
        provider_window_id: 'old-name-weekly-0',
        window_kind: 'weekly',
        model_scope_kind: 'feature',
        model_scope_key: 'future_feature',
        observed_at_ms: 100,
      });

      const merged = mergeAccountQuotaSnapshotWindows([local1, local2], [server], {
        provider: 'codex',
      });

      // Does not guess: both locals remain unmerged and server is appended as unmatched
      expect(merged).toHaveLength(3);
      expect(merged.map((m) => m.providerWindowId)).toContain('old-name-weekly-0');
    });

    it('Case E: cpamp:ambiguous:* does not participate in alias reconciliation', () => {
      const local = makeDefinition({
        key: 'cpamp:ambiguous:some-slot',
        providerWindowId: 'cpamp:ambiguous:some-slot',
        providerWindowAliases: ['old-name-weekly-0'],
        kind: 'weekly',
        identityAmbiguous: true,
        modelScope: { kind: 'feature', key: 'future_feature', complete: false },
        observedAtMs: 200,
      });
      const server = makeSnapshot({
        provider_window_id: 'old-name-weekly-0',
        window_kind: 'weekly',
        model_scope_kind: 'feature',
        model_scope_key: 'future_feature',
        observed_at_ms: 100,
      });

      const merged = mergeAccountQuotaSnapshotWindows([local], [server], {
        provider: 'codex',
      });

      // Ambiguous window does not alias match
      expect(merged).toHaveLength(2);
    });

    it('Case F: does not match alias when semantic scopes differ', () => {
      const local = makeDefinition({
        key: 'future-feature-weekly-0',
        providerWindowId: 'future-feature-weekly-0',
        providerWindowAliases: ['shared-alias-weekly-0'],
        kind: 'weekly',
        modelScope: { kind: 'feature', key: 'feature_a', complete: false },
        observedAtMs: 200,
      });
      const server = makeSnapshot({
        provider_window_id: 'shared-alias-weekly-0',
        window_kind: 'weekly',
        model_scope_kind: 'feature',
        model_scope_key: 'feature_b',
        observed_at_ms: 100,
      });

      const merged = mergeAccountQuotaSnapshotWindows([local], [server], {
        provider: 'codex',
      });

      expect(merged).toHaveLength(2);
    });
  });

  describe('8.2 local ambiguous coverage', () => {
    it('Case A: newer complete local ambiguous set shadows older identifiable active snapshots', () => {
      const local1 = makeDefinition({
        key: 'cpamp:ambiguous:same-quota-weekly-0',
        providerWindowId: 'cpamp:ambiguous:same-quota-weekly-0',
        kind: 'weekly',
        identityAmbiguous: true,
        modelScope: { kind: 'feature', key: 'same_quota', complete: false },
        observedAtMs: 200,
      });
      const local2 = makeDefinition({
        key: 'cpamp:ambiguous:same-quota-weekly-1',
        providerWindowId: 'cpamp:ambiguous:same-quota-weekly-1',
        kind: 'weekly',
        identityAmbiguous: true,
        modelScope: { kind: 'feature', key: 'same_quota', complete: false },
        observedAtMs: 200,
      });
      const server1 = makeSnapshot({
        provider_window_id: 'same-quota-weekly-0',
        window_kind: 'weekly',
        model_scope_kind: 'feature',
        model_scope_key: 'same_quota',
        observed_at_ms: 100,
        availability: 'active',
      });
      const server2 = makeSnapshot({
        provider_window_id: 'same-quota-weekly-1',
        window_kind: 'weekly',
        model_scope_kind: 'feature',
        model_scope_key: 'same_quota',
        observed_at_ms: 100,
        availability: 'active',
      });

      const merged = mergeAccountQuotaSnapshotWindows([local1, local2], [server1, server2], {
        provider: 'codex',
        localObservation: completeCodexObservation(200),
      });

      expect(merged).toHaveLength(2);
      expect(merged.map((m) => m.providerWindowId)).toEqual([
        'cpamp:ambiguous:same-quota-weekly-0',
        'cpamp:ambiguous:same-quota-weekly-1',
      ]);
    });

    it('Case B: partial local observation cannot suppress identifiable server rows', () => {
      const local = makeDefinition({
        key: 'cpamp:ambiguous:same-quota-weekly-0',
        providerWindowId: 'cpamp:ambiguous:same-quota-weekly-0',
        kind: 'weekly',
        identityAmbiguous: true,
        modelScope: { kind: 'feature', key: 'same_quota', complete: false },
        observedAtMs: 200,
      });
      const server = makeSnapshot({
        provider_window_id: 'same-quota-weekly-0',
        window_kind: 'weekly',
        model_scope_kind: 'feature',
        model_scope_key: 'same_quota',
        observed_at_ms: 100,
        availability: 'active',
      });

      const merged = mergeAccountQuotaSnapshotWindows([local], [server], {
        provider: 'codex',
        localObservation: {
          observed_at_ms: 200,
          inventory_scope_key: 'codex:rate-limits',
          inventory_mode: 'partial',
        },
      });

      expect(merged).toHaveLength(2);
    });

    it('Case C: empty complete local observation preserves omission debounce without suppressing', () => {
      const server = makeSnapshot({
        provider_window_id: 'same-quota-weekly-0',
        window_kind: 'weekly',
        model_scope_kind: 'feature',
        model_scope_key: 'same_quota',
        observed_at_ms: 100,
        availability: 'active',
      });

      const merged = mergeAccountQuotaSnapshotWindows([], [server], {
        provider: 'codex',
        localObservation: completeCodexObservation(200),
      });

      expect(merged).toHaveLength(1);
      expect(merged[0].providerWindowId).toBe('same-quota-weekly-0');
    });

    it('Case D: does not suppress when semantic scopes differ', () => {
      const local = makeDefinition({
        key: 'cpamp:ambiguous:same-quota-weekly-0',
        providerWindowId: 'cpamp:ambiguous:same-quota-weekly-0',
        kind: 'weekly',
        identityAmbiguous: true,
        modelScope: { kind: 'feature', key: 'scope_a', complete: false },
        observedAtMs: 200,
      });
      const server = makeSnapshot({
        provider_window_id: 'same-quota-weekly-0',
        window_kind: 'weekly',
        model_scope_kind: 'feature',
        model_scope_key: 'scope_b',
        observed_at_ms: 100,
        availability: 'active',
      });

      const merged = mergeAccountQuotaSnapshotWindows([local], [server], {
        provider: 'codex',
        localObservation: completeCodexObservation(200),
      });

      expect(merged).toHaveLength(2);
    });

    it('Case E: does not suppress when window roles differ (weekly vs five_hour)', () => {
      const local = makeDefinition({
        key: 'cpamp:ambiguous:same-quota-weekly-0',
        providerWindowId: 'cpamp:ambiguous:same-quota-weekly-0',
        kind: 'weekly',
        identityAmbiguous: true,
        modelScope: { kind: 'feature', key: 'same_quota', complete: false },
        observedAtMs: 200,
      });
      const server = makeSnapshot({
        provider_window_id: 'same-quota-five-hour-0',
        window_kind: 'five_hour',
        model_scope_kind: 'feature',
        model_scope_key: 'same_quota',
        observed_at_ms: 100,
        availability: 'active',
      });

      const merged = mergeAccountQuotaSnapshotWindows([local], [server], {
        provider: 'codex',
        localObservation: completeCodexObservation(200),
      });

      expect(merged).toHaveLength(2);
    });

    it('Case F: does not suppress when generic durations differ', () => {
      const local = makeDefinition({
        key: 'cpamp:ambiguous:custom-window-2d-0',
        providerWindowId: 'cpamp:ambiguous:custom-window-2d-0',
        kind: 'unknown',
        durationSeconds: 172_800,
        identityAmbiguous: true,
        modelScope: { kind: 'feature', key: 'same_quota', complete: false },
        observedAtMs: 200,
      });
      const server = makeSnapshot({
        provider_window_id: 'custom-window-3d-0',
        window_kind: 'unknown',
        duration_seconds: 259_200,
        model_scope_kind: 'feature',
        model_scope_key: 'same_quota',
        observed_at_ms: 100,
        availability: 'active',
      });

      const merged = mergeAccountQuotaSnapshotWindows([local], [server], {
        provider: 'codex',
        localObservation: completeCodexObservation(200),
      });

      expect(merged).toHaveLength(2);
    });

    it('Case G: does not suppress when server lifecycle evidence is newer than local observation', () => {
      const local = makeDefinition({
        key: 'cpamp:ambiguous:same-quota-weekly-0',
        providerWindowId: 'cpamp:ambiguous:same-quota-weekly-0',
        kind: 'weekly',
        identityAmbiguous: true,
        modelScope: { kind: 'feature', key: 'same_quota', complete: false },
        observedAtMs: 200,
      });
      const server = makeSnapshot({
        provider_window_id: 'same-quota-weekly-0',
        window_kind: 'weekly',
        model_scope_kind: 'feature',
        model_scope_key: 'same_quota',
        observed_at_ms: 100,
        missing_since_ms: 300,
        availability: 'pending_absent',
      });

      const merged = mergeAccountQuotaSnapshotWindows([local], [server], {
        provider: 'codex',
        localObservation: completeCodexObservation(200),
      });

      expect(merged).toHaveLength(2);
    });

    it('Case H: does not suppress on equal timestamps', () => {
      const local = makeDefinition({
        key: 'cpamp:ambiguous:same-quota-weekly-0',
        providerWindowId: 'cpamp:ambiguous:same-quota-weekly-0',
        kind: 'weekly',
        identityAmbiguous: true,
        modelScope: { kind: 'feature', key: 'same_quota', complete: false },
        observedAtMs: 200,
      });
      const server = makeSnapshot({
        provider_window_id: 'same-quota-weekly-0',
        window_kind: 'weekly',
        model_scope_kind: 'feature',
        model_scope_key: 'same_quota',
        observed_at_ms: 200,
        availability: 'active',
      });

      const merged = mergeAccountQuotaSnapshotWindows([local], [server], {
        provider: 'codex',
        localObservation: completeCodexObservation(200),
      });

      expect(merged).toHaveLength(2);
    });
  });
});
