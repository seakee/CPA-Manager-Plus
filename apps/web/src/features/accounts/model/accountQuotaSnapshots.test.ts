import { describe, expect, it } from 'vitest';
import type {
  AccountQuotaSnapshotCycle,
  AccountQuotaSnapshotWindow,
} from '@/services/api/usageService';
import { CODEX_SPARK_MODEL_ID } from '@/utils/quota/codexQuota';
import type {
  AccountQuotaCycleDefinition,
  AccountQuotaWindowDefinition,
} from './accountQuotaWindowDefinitions';
import {
  buildAccountQuotaSnapshotWriteEntries,
  mergeCodexResetCreditsFromQuotaSnapshots,
  mergeAccountQuotaSnapshotWindows,
} from './accountQuotaSnapshots';
import type { AccountRow } from './accountRows';
import {
  buildAccountQuotaDisplayWindow,
  type AccountQuotaDisplayWindow,
} from './accountQuotaDisplayWindows';
import { buildAccountQuotaWindowDefinitions } from './accountQuotaWindowDefinitions';

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
  quotaProgressObservedAtMs: 10_000,
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
    observedAtMs: 10_000,
    quotaProgressObservedAtMs: 10_000,
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
  cycle_start_ms: 1_000,
  cycle_end_ms: 19_001_000,
  duration_seconds: 19_000,
  used_percent: 35,
  remaining_percent: 65,
  stale: false,
  ...overrides,
});

const makeSnapshotCycle = (
  overrides: Partial<AccountQuotaSnapshotCycle> = {}
): AccountQuotaSnapshotCycle => ({
  id: 10,
  activation_id: 1,
  state: 'active',
  scheduled_start_ms: 1_000,
  scheduled_end_ms: 19_001_000,
  actual_start_ms: 1_000,
  duration_seconds: 19_000,
  boundary_accuracy: 'exact',
  forecast_eligible: true,
  ...overrides,
});

const makeDefinitionCycle = (
  overrides: Partial<AccountQuotaCycleDefinition> = {}
): AccountQuotaCycleDefinition => ({
  id: 10,
  activationId: 1,
  state: 'active',
  scheduledStartMs: 1_000,
  scheduledEndMs: 19_001_000,
  actualStartMs: 1_000,
  actualEndMs: null,
  durationSeconds: 19_000,
  boundaryAccuracy: 'exact',
  endReason: '',
  parentCycleId: null,
  forecastEligible: true,
  ...overrides,
});

const makeSnapshotRow = (provider: 'codex' | 'antigravity' = 'codex'): AccountRow =>
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

  it('preserves quota progress provenance when a newer snapshot only provides metadata', () => {
    const definition = makeDefinition({
      observedAtMs: 1_000,
      quotaProgressObservedAtMs: 1_000,
      currentCycle: makeDefinitionCycle(),
      remainingPercent: 50,
      usedPercent: 50,
      display: {
        ...makeDefinition().display,
        observedAtMs: 1_000,
        quotaProgressObservedAtMs: 1_000,
        remainingPercent: 50,
        usedPercent: 50,
      },
    });
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [definition],
      [
        makeSnapshot({
          observed_at_ms: 2_000,
          used_percent: undefined,
          remaining_percent: undefined,
          availability: 'active',
          field_sources: {
            quota: { source: 'response_header', observed_at_ms: 2_000 },
          },
          current_cycle: makeSnapshotCycle(),
        }),
      ]
    );

    expect(merged).toMatchObject({
      observedAtMs: 2_000,
      usedPercent: 50,
      remainingPercent: 50,
      quotaProgressObservedAtMs: 1_000,
      display: {
        observedAtMs: 2_000,
        usedPercent: 50,
        remainingPercent: 50,
        quotaProgressObservedAtMs: 1_000,
      },
    });
  });

  it('uses field-level quota provenance for new snapshot progress', () => {
    const snapshot = makeSnapshot({
      observed_at_ms: 3_000,
      used_percent: 60,
      remaining_percent: 40,
      field_sources: {
        quota: { source: 'api_query', observed_at_ms: 2_000 },
      },
    });
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [makeDefinition({ observedAtMs: 1_000, quotaProgressObservedAtMs: 1_000 })],
      [snapshot]
    );

    expect(merged).toMatchObject({
      observedAtMs: 3_000,
      usedPercent: 60,
      remainingPercent: 40,
      quotaProgressObservedAtMs: 2_000,
    });

    const [snapshotOnly] = mergeAccountQuotaSnapshotWindows([], [snapshot]);
    expect(snapshotOnly).toMatchObject({
      usedPercent: 60,
      remainingPercent: 40,
      quotaProgressObservedAtMs: 2_000,
      display: { quotaProgressObservedAtMs: 2_000 },
    });
  });

  it.each([
    { label: 'cycle id', id: 11, activationId: 1, cycle: { id: 11 } },
    { label: 'activation id', id: 10, activationId: 2, cycle: { activation_id: 2 } },
  ])(
    'does not inherit quota across a different lifecycle $label',
    ({ cycle, id, activationId }) => {
      const [merged] = mergeAccountQuotaSnapshotWindows(
        [
          makeDefinition({
            observedAtMs: 1_000,
            usedPercent: 95,
            remainingPercent: 5,
            currentCycle: makeDefinitionCycle(),
          }),
        ],
        [
          makeSnapshot({
            observed_at_ms: 2_000,
            used_percent: undefined,
            remaining_percent: undefined,
            current_cycle: makeSnapshotCycle(cycle),
          }),
        ]
      );

      expect(merged).toMatchObject({
        usedPercent: null,
        remainingPercent: null,
        quotaProgressObservedAtMs: null,
        currentCycle: { id, activationId },
      });
    }
  );

  it('does not let an older different-cycle used snapshot supersede newer live quota', () => {
    const definition = makeDefinition({
      observedAtMs: 3_000,
      quotaProgressObservedAtMs: 3_000,
      usedPercent: 3,
      remainingPercent: 97,
      currentCycle: makeDefinitionCycle({ id: 11 }),
      display: {
        ...makeDefinition().display,
        observedAtMs: 3_000,
        quotaProgressObservedAtMs: 3_000,
        usedPercent: 3,
        remainingPercent: 97,
      },
    });
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [definition],
      [
        makeSnapshot({
          observed_at_ms: 2_000,
          used_percent: 95,
          remaining_percent: 5,
          current_cycle: makeSnapshotCycle({ id: 10 }),
        }),
      ]
    );

    expect(merged).toBe(definition);
    expect(merged).toMatchObject({
      observedAtMs: 3_000,
      usedPercent: 3,
      remainingPercent: 97,
      quotaProgressObservedAtMs: 3_000,
      currentCycle: { id: 11 },
    });
  });

  it('does not let an older different-cycle remaining snapshot clear newer live quota', () => {
    const definition = makeDefinition({
      observedAtMs: 3_000,
      quotaProgressObservedAtMs: 3_000,
      usedPercent: 3,
      remainingPercent: 97,
      currentCycle: makeDefinitionCycle({ id: 11 }),
      display: {
        ...makeDefinition().display,
        observedAtMs: 3_000,
        quotaProgressObservedAtMs: 3_000,
        usedPercent: 3,
        remainingPercent: 97,
      },
    });
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [definition],
      [
        makeSnapshot({
          observed_at_ms: 2_000,
          used_percent: undefined,
          remaining_percent: 40,
          current_cycle: makeSnapshotCycle({ id: 10 }),
        }),
      ]
    );

    expect(merged).toBe(definition);
    expect(merged).toMatchObject({
      observedAtMs: 3_000,
      usedPercent: 3,
      remainingPercent: 97,
      quotaProgressObservedAtMs: 3_000,
      currentCycle: { id: 11 },
    });
  });

  it('does not let an older different-cycle metadata snapshot clear newer live quota', () => {
    const definition = makeDefinition({
      observedAtMs: 3_000,
      quotaProgressObservedAtMs: 3_000,
      usedPercent: 3,
      remainingPercent: 97,
      currentCycle: makeDefinitionCycle({ id: 11 }),
      display: {
        ...makeDefinition().display,
        observedAtMs: 3_000,
        quotaProgressObservedAtMs: 3_000,
        usedPercent: 3,
        remainingPercent: 97,
      },
    });
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [definition],
      [
        makeSnapshot({
          observed_at_ms: 2_000,
          used_percent: undefined,
          remaining_percent: undefined,
          current_cycle: makeSnapshotCycle({ id: 10 }),
        }),
      ]
    );

    expect(merged).toBe(definition);
    expect(merged).toMatchObject({
      observedAtMs: 3_000,
      usedPercent: 3,
      remainingPercent: 97,
      quotaProgressObservedAtMs: 3_000,
      currentCycle: { id: 11 },
    });
  });

  it('does not let an older boundary-only cycle snapshot supersede newer live quota', () => {
    const oldCycleStartMs = 1_700_000_000_000;
    const oldCycleEndMs = oldCycleStartMs + 5 * 60 * 60 * 1000;
    const newCycleStartMs = oldCycleEndMs;
    const newCycleEndMs = newCycleStartMs + 5 * 60 * 60 * 1000;
    const definition = makeDefinition({
      observedAtMs: newCycleStartMs + 1_000,
      quotaProgressObservedAtMs: newCycleStartMs + 1_000,
      usedPercent: 3,
      remainingPercent: 97,
      cycleStartMs: newCycleStartMs,
      cycleEndMs: newCycleEndMs,
      durationSeconds: 18_000,
      currentCycle: undefined,
      display: {
        ...makeDefinition().display,
        observedAtMs: newCycleStartMs + 1_000,
        quotaProgressObservedAtMs: newCycleStartMs + 1_000,
        usedPercent: 3,
        remainingPercent: 97,
        resetAtMs: newCycleEndMs,
      },
    });
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [definition],
      [
        makeSnapshot({
          observed_at_ms: oldCycleStartMs + 1_000,
          cycle_start_ms: oldCycleStartMs,
          cycle_end_ms: oldCycleEndMs,
          duration_seconds: 18_000,
          used_percent: undefined,
          remaining_percent: undefined,
          current_cycle: undefined,
        }),
      ]
    );

    expect(merged).toBe(definition);
    expect(merged).toMatchObject({
      observedAtMs: newCycleStartMs + 1_000,
      cycleStartMs: newCycleStartMs,
      cycleEndMs: newCycleEndMs,
      usedPercent: 3,
      remainingPercent: 97,
      quotaProgressObservedAtMs: newCycleStartMs + 1_000,
    });
  });

  it('accepts new-cycle used quota without old-cycle freshness blocking it', () => {
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [
        makeDefinition({
          observedAtMs: 3_000,
          quotaProgressObservedAtMs: 2_000,
          usedPercent: 95,
          remainingPercent: 5,
          currentCycle: makeDefinitionCycle(),
        }),
      ],
      [
        makeSnapshot({
          observed_at_ms: 4_000,
          used_percent: 3,
          remaining_percent: 97,
          field_sources: { quota: { source: 'api_query', observed_at_ms: 1_000 } },
          current_cycle: makeSnapshotCycle({ id: 11 }),
        }),
      ]
    );

    expect(merged).toMatchObject({
      usedPercent: 3,
      remainingPercent: 97,
      quotaProgressObservedAtMs: 1_000,
      currentCycle: { id: 11 },
    });
  });

  it('keeps only remaining evidence when a new cycle snapshot is remaining-only', () => {
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [
        makeDefinition({
          usedPercent: 95,
          remainingPercent: 5,
          currentCycle: makeDefinitionCycle(),
        }),
      ],
      [
        makeSnapshot({
          observed_at_ms: 20_000,
          used_percent: undefined,
          remaining_percent: 40,
          current_cycle: makeSnapshotCycle({ id: 11 }),
        }),
      ]
    );

    expect(merged).toMatchObject({
      usedPercent: null,
      remainingPercent: 40,
      quotaProgressObservedAtMs: null,
    });
  });

  it('clears all inherited quota for a new-cycle metadata-only snapshot', () => {
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [
        makeDefinition({
          usedPercent: 95,
          remainingPercent: 5,
          currentCycle: makeDefinitionCycle(),
        }),
      ],
      [
        makeSnapshot({
          observed_at_ms: 20_000,
          used_percent: undefined,
          remaining_percent: undefined,
          current_cycle: makeSnapshotCycle({ id: 11 }),
        }),
      ]
    );

    expect(merged).toMatchObject({
      usedPercent: null,
      remainingPercent: null,
      quotaProgressObservedAtMs: null,
    });
  });

  it('clears inherited quota on a boundary-only fixed-window rollover', () => {
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [
        makeDefinition({
          observedAtMs: 1_000,
          cycleEndMs: 1_000_000,
          durationSeconds: 18_000,
          usedPercent: 95,
          remainingPercent: 5,
          currentCycle: undefined,
        }),
      ],
      [
        makeSnapshot({
          observed_at_ms: 2_000,
          cycle_start_ms: 1_000_000,
          cycle_end_ms: 19_000_000,
          duration_seconds: 18_000,
          used_percent: undefined,
          remaining_percent: undefined,
          current_cycle: undefined,
        }),
      ]
    );

    expect(merged).toMatchObject({
      usedPercent: null,
      remainingPercent: null,
      quotaProgressObservedAtMs: null,
      cycleEndMs: 19_000_000,
    });
  });

  it('clears inherited quota after a materially backward boundary correction', () => {
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [
        makeDefinition({
          observedAtMs: 1_000,
          quotaProgressObservedAtMs: 1_000,
          cycleEndMs: 2_000_000,
          usedPercent: 60,
          remainingPercent: 40,
          currentCycle: undefined,
        }),
      ],
      [
        makeSnapshot({
          observed_at_ms: 3_000,
          cycle_start_ms: 1_000,
          cycle_end_ms: 1_000_000,
          used_percent: undefined,
          remaining_percent: undefined,
          boundary_accuracy: 'estimated',
          current_cycle: undefined,
        }),
      ]
    );

    expect(merged).toMatchObject({
      usedPercent: null,
      remainingPercent: null,
      quotaProgressObservedAtMs: null,
    });
  });

  it('keeps newer live quota progress when persisted metadata is newer', () => {
    const definition = makeDefinition({
      observedAtMs: 2_500,
      quotaProgressObservedAtMs: 2_500,
      usedPercent: 60,
      remainingPercent: 40,
      display: {
        ...makeDefinition().display,
        observedAtMs: 2_500,
        quotaProgressObservedAtMs: 2_500,
        usedPercent: 60,
        remainingPercent: 40,
      },
    });
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [definition],
      [
        makeSnapshot({
          observed_at_ms: 3_000,
          used_percent: 50,
          remaining_percent: 50,
          field_sources: {
            quota: { source: 'api_query', observed_at_ms: 2_000 },
          },
        }),
      ]
    );

    expect(merged).toMatchObject({
      observedAtMs: 3_000,
      usedPercent: 60,
      remainingPercent: 40,
      quotaProgressObservedAtMs: 2_500,
      display: {
        observedAtMs: 3_000,
        usedPercent: 60,
        remainingPercent: 40,
        quotaProgressObservedAtMs: 2_500,
      },
    });
  });

  it('keeps newer live metadata while accepting a newer persisted quota field', () => {
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [
        makeDefinition({
          observedAtMs: 3_000,
          quotaProgressObservedAtMs: 2_000,
          usedPercent: 50,
          remainingPercent: 50,
          display: {
            ...makeDefinition().display,
            observedAtMs: 3_000,
            quotaProgressObservedAtMs: 2_000,
            usedPercent: 50,
            remainingPercent: 50,
          },
        }),
      ],
      [
        makeSnapshot({
          observed_at_ms: 2_500,
          used_percent: 60,
          remaining_percent: 40,
          field_sources: {
            quota: { source: 'api_query', observed_at_ms: 2_500 },
          },
        }),
      ]
    );

    expect(merged).toMatchObject({
      observedAtMs: 3_000,
      usedPercent: 60,
      remainingPercent: 40,
      quotaProgressObservedAtMs: 2_500,
      display: {
        observedAtMs: 3_000,
        usedPercent: 60,
        remainingPercent: 40,
        quotaProgressObservedAtMs: 2_500,
      },
    });
  });

  it('uses newer persisted quota progress when live progress is older', () => {
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [
        makeDefinition({
          observedAtMs: 2_500,
          quotaProgressObservedAtMs: 1_000,
          usedPercent: 50,
          remainingPercent: 50,
        }),
      ],
      [
        makeSnapshot({
          observed_at_ms: 3_000,
          used_percent: 60,
          remaining_percent: 40,
          field_sources: {
            quota: { source: 'api_query', observed_at_ms: 2_000 },
          },
        }),
      ]
    );

    expect(merged).toMatchObject({
      observedAtMs: 3_000,
      usedPercent: 60,
      remainingPercent: 40,
      quotaProgressObservedAtMs: 2_000,
    });
  });

  it('does not advance quota progress from a remaining-only snapshot', () => {
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [makeDefinition({ observedAtMs: 1_000, quotaProgressObservedAtMs: 1_000 })],
      [
        makeSnapshot({
          observed_at_ms: 2_000,
          used_percent: undefined,
          remaining_percent: 40,
          field_sources: {
            quota: { source: 'api_query', observed_at_ms: 2_000 },
          },
        }),
      ]
    );

    expect(merged).toMatchObject({
      observedAtMs: 2_000,
      usedPercent: null,
      remainingPercent: 40,
      quotaProgressObservedAtMs: null,
      display: {
        observedAtMs: 2_000,
        usedPercent: null,
        remainingPercent: 40,
        quotaProgressObservedAtMs: null,
      },
    });

    const [snapshotOnly] = mergeAccountQuotaSnapshotWindows(
      [],
      [
        makeSnapshot({
          observed_at_ms: 2_000,
          used_percent: undefined,
          remaining_percent: 35,
        }),
      ]
    );
    expect(snapshotOnly).toMatchObject({
      usedPercent: null,
      remainingPercent: 35,
      quotaProgressObservedAtMs: null,
      display: {
        usedPercent: null,
        remainingPercent: 35,
        quotaProgressObservedAtMs: null,
      },
    });
  });

  it('does not let stale remaining evidence override a newer live quota pair', () => {
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [
        makeDefinition({
          observedAtMs: 3_000,
          quotaProgressObservedAtMs: 2_500,
          usedPercent: 20,
          remainingPercent: 80,
          display: {
            ...makeDefinition().display,
            observedAtMs: 3_000,
            quotaProgressObservedAtMs: 2_500,
            usedPercent: 20,
            remainingPercent: 80,
          },
        }),
      ],
      [
        makeSnapshot({
          observed_at_ms: 3_000,
          used_percent: undefined,
          remaining_percent: 40,
          field_sources: {
            quota: { source: 'api_query', observed_at_ms: 2_000 },
          },
        }),
      ]
    );

    expect(merged).toMatchObject({
      observedAtMs: 3_000,
      usedPercent: 20,
      remainingPercent: 80,
      quotaProgressObservedAtMs: 2_500,
      display: {
        observedAtMs: 3_000,
        usedPercent: 20,
        remainingPercent: 80,
        quotaProgressObservedAtMs: 2_500,
      },
    });
  });

  it('derives the remaining percentage when a newer snapshot only has used percentage', () => {
    const snapshot = makeSnapshot({
      observed_at_ms: 2_000,
      used_percent: 60,
      remaining_percent: undefined,
      field_sources: {
        quota: { source: 'api_query', observed_at_ms: 2_000 },
      },
    });
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [makeDefinition({ quotaProgressObservedAtMs: 1_000 })],
      [snapshot]
    );

    expect(merged).toMatchObject({
      usedPercent: 60,
      remainingPercent: 40,
      quotaProgressObservedAtMs: 2_000,
    });

    const [snapshotOnly] = mergeAccountQuotaSnapshotWindows([], [snapshot]);
    expect(snapshotOnly).toMatchObject({
      usedPercent: 60,
      remainingPercent: 40,
      quotaProgressObservedAtMs: 2_000,
    });
  });

  it('restores legacy snapshot quota provenance from the snapshot observation time', () => {
    const [merged] = mergeAccountQuotaSnapshotWindows(
      [],
      [
        makeSnapshot({
          observed_at_ms: 2_000,
          used_percent: 60,
          remaining_percent: undefined,
        }),
      ]
    );

    expect(merged).toMatchObject({
      usedPercent: 60,
      remainingPercent: 40,
      quotaProgressObservedAtMs: 2_000,
      display: { quotaProgressObservedAtMs: 2_000 },
    });
  });

  it('preserves non-Codex observedAt compatibility for snapshot write and stale merge', () => {
    const observedAtMs = 3_000;
    const display: AccountQuotaDisplayWindow = buildAccountQuotaDisplayWindow({
      key: 'antigravity-five-hour',
      label: '5H',
      kind: 'five_hour',
      remainingPercent: 40,
      usedPercent: 60,
      resetLabel: '-',
      source: 'antigravity',
      observedAtMs,
    });
    const [definition] = buildAccountQuotaWindowDefinitions([display]);

    expect(definition.quotaProgressObservedAtMs).toBe(observedAtMs);

    const row = makeSnapshotRow('antigravity');
    const [entry] = buildAccountQuotaSnapshotWriteEntries(
      [row],
      new Map([[row.selectionKey, [definition]]]),
      {
        getObservation: () => ({
          source: 'api_query',
          observed_at_ms: observedAtMs,
          inventory_scope_key: 'antigravity:quota',
          inventory_mode: 'complete',
        }),
      }
    );

    expect(entry.windows[0]).toMatchObject({
      observed_at_ms: observedAtMs,
      used_percent: 60,
      remaining_percent: 40,
    });

    const [merged] = mergeAccountQuotaSnapshotWindows(
      [definition],
      [
        makeSnapshot({
          provider_window_id: definition.providerWindowId,
          observed_at_ms: 2_000,
          used_percent: 50,
          remaining_percent: 50,
        }),
      ],
      { provider: 'antigravity' }
    );

    expect(merged).toMatchObject({
      observedAtMs: observedAtMs,
      usedPercent: 60,
      remainingPercent: 40,
      quotaProgressObservedAtMs: observedAtMs,
    });
  });

  it('does not persist old quota progress under a newer snapshot observation', () => {
    const row = makeSnapshotRow();
    const [entry] = buildAccountQuotaSnapshotWriteEntries(
      [row],
      new Map([
        [
          row.selectionKey,
          [
            makeDefinition({
              observedAtMs: 2_000,
              quotaProgressObservedAtMs: 1_000,
              usedPercent: 50,
              remainingPercent: 50,
            }),
          ],
        ],
      ]),
      {
        getObservation: () => ({
          source: 'response_header',
          source_observation_id: 'header-1',
          observed_at_ms: 2_000,
          inventory_scope_key: 'codex:rate-limits',
          inventory_mode: 'complete',
        }),
      }
    );

    expect(entry.windows[0]).not.toHaveProperty('used_percent');
    expect(entry.windows[0]).not.toHaveProperty('remaining_percent');
    expect(entry.windows[0].observed_at_ms).toBe(2_000);
  });

  it('persists same-observation remaining-only quota without used provenance', () => {
    const row = makeSnapshotRow();
    const [entry] = buildAccountQuotaSnapshotWriteEntries(
      [row],
      new Map([
        [
          row.selectionKey,
          [
            makeDefinition({
              observedAtMs: 2_000,
              quotaProgressObservedAtMs: null,
              usedPercent: null,
              remainingPercent: 35,
            }),
          ],
        ],
      ]),
      {
        getObservation: () => ({
          source: 'response_header',
          source_observation_id: 'header-remaining-only',
          observed_at_ms: 2_000,
          inventory_scope_key: 'codex:rate-limits',
          inventory_mode: 'complete',
        }),
      }
    );

    expect(entry.windows[0]).toMatchObject({
      observed_at_ms: 2_000,
      remaining_percent: 35,
    });
    expect(entry.windows[0]).not.toHaveProperty('used_percent');
  });

  it('round-trips remaining-only quota through snapshot write and restore', () => {
    const row = makeSnapshotRow();
    const [entry] = buildAccountQuotaSnapshotWriteEntries(
      [row],
      new Map([
        [
          row.selectionKey,
          [
            makeDefinition({
              observedAtMs: 2_000,
              quotaProgressObservedAtMs: null,
              usedPercent: null,
              remainingPercent: 35,
            }),
          ],
        ],
      ]),
      {
        getObservation: () => ({
          source: 'response_header',
          observed_at_ms: 2_000,
          inventory_scope_key: 'codex:rate-limits',
          inventory_mode: 'complete',
        }),
      }
    );
    const snapshot = { ...entry.windows[0], stale: false } as AccountQuotaSnapshotWindow;
    const [restored] = mergeAccountQuotaSnapshotWindows([], [snapshot], { provider: 'codex' });

    expect(restored).toMatchObject({
      observedAtMs: 2_000,
      usedPercent: null,
      remainingPercent: 35,
      quotaProgressObservedAtMs: null,
      display: {
        usedPercent: null,
        remainingPercent: 35,
        quotaProgressObservedAtMs: null,
      },
    });
  });

  it('does not restamp remaining-only quota under a newer snapshot observation', () => {
    const row = makeSnapshotRow();
    const [entry] = buildAccountQuotaSnapshotWriteEntries(
      [row],
      new Map([
        [
          row.selectionKey,
          [
            makeDefinition({
              observedAtMs: 1_000,
              quotaProgressObservedAtMs: null,
              usedPercent: null,
              remainingPercent: 35,
            }),
          ],
        ],
      ]),
      {
        getObservation: () => ({
          source: 'response_header',
          observed_at_ms: 2_000,
          inventory_scope_key: 'codex:rate-limits',
          inventory_mode: 'complete',
        }),
      }
    );

    expect(entry.windows[0]).toMatchObject({ observed_at_ms: 2_000 });
    expect(entry.windows[0]).not.toHaveProperty('used_percent');
    expect(entry.windows[0]).not.toHaveProperty('remaining_percent');
  });

  it('persists quota progress when its provenance matches the snapshot observation', () => {
    const row = makeSnapshotRow();
    const [entry] = buildAccountQuotaSnapshotWriteEntries(
      [row],
      new Map([
        [
          row.selectionKey,
          [
            makeDefinition({
              observedAtMs: 2_000,
              quotaProgressObservedAtMs: 2_000,
              usedPercent: 60,
              remainingPercent: 40,
            }),
          ],
        ],
      ]),
      {
        getObservation: () => ({
          source: 'api_query',
          source_observation_id: 'api-1',
          observed_at_ms: 2_000,
          inventory_scope_key: 'codex:rate-limits',
          inventory_mode: 'complete',
        }),
      }
    );

    expect(entry.windows[0]).toMatchObject({
      observed_at_ms: 2_000,
      used_percent: 60,
      remaining_percent: 40,
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
    const definition = makeDefinition({
      observedAtMs: 30_000,
      quotaProgressObservedAtMs: 30_000,
      usedPercent: 12,
    });
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
