import { describe, expect, it } from 'vitest';
import type { AccountQuotaDisplayWindow } from './accountQuotaDisplayWindows';
import {
  buildAccountQuotaUsageRanges,
  buildAccountQuotaWindowDefinitions,
} from './accountQuotaWindowDefinitions';
import type { AccountQuotaCycleDefinition } from './accountQuotaWindowDefinitions';

const makeWindow = (overrides: Partial<AccountQuotaDisplayWindow>): AccountQuotaDisplayWindow => ({
  key: 'five-hour',
  label: '5 hours',
  kind: 'five_hour',
  remainingPercent: 60,
  usedPercent: 40,
  resetLabel: 'reset',
  resetAccuracy: 'exact',
  limitWindowSeconds: 5 * 60 * 60,
  resetAtMs: 38_000_000,
  fromMs: 20_000_000,
  toMs: 30_000_000,
  source: 'codex',
  observationSource: 'api_query',
  observedAtMs: 30_000_000,
  quotaProgressObservedAtMs: 30_000_000,
  windowMode: 'fixed',
  cycleStartMs: 20_000_000,
  cycleEndMs: 38_000_000,
  modelScope: { kind: 'all', complete: true },
  ...overrides,
});

const makeCycle = (
  overrides: Partial<AccountQuotaCycleDefinition> = {}
): AccountQuotaCycleDefinition => ({
  id: 2,
  activationId: 1,
  state: 'active',
  scheduledStartMs: 20_000_000,
  scheduledEndMs: 38_000_000,
  actualStartMs: 20_000_000,
  actualEndMs: null,
  durationSeconds: 18_000,
  boundaryAccuracy: 'exact',
  endReason: '',
  parentCycleId: null,
  forecastEligible: true,
  ...overrides,
});

describe('accountQuotaWindowDefinitions', () => {
  it('propagates quota progress provenance separately from generic observation time', () => {
    const [definition] = buildAccountQuotaWindowDefinitions([
      makeWindow({ observedAtMs: 30_000_000, quotaProgressObservedAtMs: 20_000_000 }),
    ]);

    expect(definition.observedAtMs).toBe(30_000_000);
    expect(definition.quotaProgressObservedAtMs).toBe(20_000_000);
  });

  it('builds separate current and previous half-open ranges for fixed windows', () => {
    const [definition] = buildAccountQuotaWindowDefinitions([makeWindow({})], 30_000_000);
    expect(buildAccountQuotaUsageRanges(definition, 30_000_000)).toEqual([
      { period: 'current', fromMs: 20_000_000, toMs: 30_000_000 },
      { period: 'previous', fromMs: 2_000_000, toMs: 20_000_000 },
    ]);
  });

  it('classifies a complete fixed relative Header boundary as derived', () => {
    const [definition] = buildAccountQuotaWindowDefinitions([
      makeWindow({
        observationSource: 'response_header',
        resetAccuracy: 'estimated',
      }),
    ]);

    expect(definition.boundaryAccuracy).toBe('derived');
  });

  it('uses actual lifecycle cycle bounds instead of subtracting the nominal duration', () => {
    const [definition] = buildAccountQuotaWindowDefinitions([makeWindow({})], 30_000_000);
    definition.currentCycle = {
      id: 2,
      activationId: 1,
      state: 'active',
      scheduledStartMs: 20_000_000,
      scheduledEndMs: 38_000_000,
      actualStartMs: 20_000_000,
      actualEndMs: null,
      durationSeconds: 18_000,
      boundaryAccuracy: 'exact',
      endReason: '',
      parentCycleId: null,
      forecastEligible: true,
    };
    definition.previousCycle = {
      id: 1,
      activationId: 1,
      state: 'closed',
      scheduledStartMs: 2_000_000,
      scheduledEndMs: 20_000_000,
      actualStartMs: 8_000_000,
      actualEndMs: 20_000_000,
      durationSeconds: 18_000,
      boundaryAccuracy: 'exact',
      endReason: 'early_reset',
      parentCycleId: null,
      forecastEligible: false,
    };

    expect(buildAccountQuotaUsageRanges(definition, 30_000_000)).toEqual([
      { period: 'current', fromMs: 20_000_000, toMs: 30_000_000 },
      { period: 'previous', fromMs: 8_000_000, toMs: 20_000_000 },
    ]);
  });

  it('keeps a reliable previous cycle when the current cycle is provisional', () => {
    const [definition] = buildAccountQuotaWindowDefinitions([makeWindow({})], 30_000_000);
    definition.boundaryAccuracy = 'unknown';
    definition.currentCycle = makeCycle({
      state: 'provisional',
      boundaryAccuracy: 'unknown',
    });
    definition.previousCycle = makeCycle({
      id: 1,
      state: 'closed',
      scheduledStartMs: 2_000_000,
      scheduledEndMs: 20_000_000,
      actualStartMs: 8_000,
      actualEndMs: 20_000,
      boundaryAccuracy: 'exact',
      endReason: 'scheduled',
      forecastEligible: false,
    });

    expect(buildAccountQuotaUsageRanges(definition, 30_000_000)).toEqual([
      { period: 'previous', fromMs: 8_000, toMs: 20_000 },
    ]);
  });

  it('keeps reliable previous usage but rejects a stale lifecycle current cycle', () => {
    const [definition] = buildAccountQuotaWindowDefinitions([makeWindow({})], 30_000);
    definition.stale = true;
    definition.availability = 'active';
    definition.currentCycle = makeCycle({
      state: 'active',
      scheduledStartMs: 10_000,
      scheduledEndMs: 20_000,
      actualStartMs: 10_000,
      boundaryAccuracy: 'exact',
    });
    definition.previousCycle = makeCycle({
      id: 1,
      state: 'closed',
      scheduledStartMs: 1_000,
      scheduledEndMs: 8_000,
      actualStartMs: 1_000,
      actualEndMs: 8_000,
      boundaryAccuracy: 'exact',
      endReason: 'scheduled',
      forecastEligible: false,
    });

    expect(buildAccountQuotaUsageRanges(definition, 30_000)).toEqual([
      { period: 'previous', fromMs: 1_000, toMs: 8_000 },
    ]);
  });

  it('rejects both lifecycle ranges when both cycles have unreliable boundaries', () => {
    const [definition] = buildAccountQuotaWindowDefinitions([makeWindow({})], 30_000_000);
    definition.boundaryAccuracy = 'unknown';
    definition.currentCycle = makeCycle({
      state: 'provisional',
      boundaryAccuracy: 'unknown',
    });
    definition.previousCycle = makeCycle({
      id: 1,
      state: 'closed',
      actualStartMs: 8_000,
      actualEndMs: 20_000,
      boundaryAccuracy: 'estimated',
    });

    expect(buildAccountQuotaUsageRanges(definition, 30_000_000)).toEqual([]);
  });

  it('keeps an observation gap outside both lifecycle usage ranges', () => {
    const [definition] = buildAccountQuotaWindowDefinitions([makeWindow({})], 30_000_000);
    definition.currentCycle = {
      id: 2,
      activationId: 1,
      state: 'active',
      scheduledStartMs: 25_000_000,
      scheduledEndMs: 38_000_000,
      actualStartMs: 25_000_000,
      actualEndMs: null,
      durationSeconds: 18_000,
      boundaryAccuracy: 'exact',
      endReason: '',
      parentCycleId: null,
      forecastEligible: true,
    };
    definition.previousCycle = {
      id: 1,
      activationId: 1,
      state: 'closed',
      scheduledStartMs: 2_000_000,
      scheduledEndMs: 20_000_000,
      actualStartMs: 8_000_000,
      actualEndMs: 20_000_000,
      durationSeconds: 18_000,
      boundaryAccuracy: 'exact',
      endReason: 'scheduled',
      parentCycleId: null,
      forecastEligible: true,
    };

    expect(buildAccountQuotaUsageRanges(definition, 30_000_000)).toEqual([
      { period: 'current', fromMs: 25_000_000, toMs: 30_000_000 },
      { period: 'previous', fromMs: 8_000_000, toMs: 20_000_000 },
    ]);
  });

  it('does not invent a previous range for the first lifecycle cycle', () => {
    const [definition] = buildAccountQuotaWindowDefinitions([makeWindow({})], 30_000_000);
    definition.currentCycle = {
      id: 1,
      activationId: 1,
      state: 'active',
      scheduledStartMs: 20_000_000,
      scheduledEndMs: 38_000_000,
      actualStartMs: 20_000_000,
      actualEndMs: null,
      durationSeconds: 18_000,
      boundaryAccuracy: 'exact',
      endReason: '',
      parentCycleId: null,
      forecastEligible: true,
    };
    definition.previousCycle = null;

    expect(buildAccountQuotaUsageRanges(definition, 30_000_000)).toEqual([
      { period: 'current', fromMs: 20_000_000, toMs: 30_000_000 },
    ]);
  });

  it('uses previous_equal_range for rolling windows and never labels it previous', () => {
    const [definition] = buildAccountQuotaWindowDefinitions(
      [
        makeWindow({
          key: 'rolling-24h',
          windowMode: 'rolling',
          limitWindowSeconds: 24 * 60 * 60,
          cycleStartMs: null,
          cycleEndMs: null,
          resetAtMs: null,
          resetAccuracy: 'estimated',
        }),
      ],
      200_000_000
    );
    expect(
      buildAccountQuotaUsageRanges(definition, 200_000_000).map((item) => item.period)
    ).toEqual(['current', 'previous_equal_range']);
  });

  it('sorts timed windows by duration and places non-window quota last', () => {
    const definitions = buildAccountQuotaWindowDefinitions(
      [
        makeWindow({
          key: 'billing',
          kind: 'billing',
          windowMode: 'non_window',
          limitWindowSeconds: null,
        }),
        makeWindow({ key: 'weekly', kind: 'weekly', limitWindowSeconds: 7 * 24 * 60 * 60 }),
        makeWindow({ key: 'five-hour', limitWindowSeconds: 5 * 60 * 60 }),
      ],
      20_000_000
    );
    expect(definitions.map((item) => item.key)).toEqual(['five-hour', 'weekly', 'billing']);
  });

  it('does not generate ranges from unknown boundaries', () => {
    const [definition] = buildAccountQuotaWindowDefinitions(
      [makeWindow({ windowMode: 'unknown', resetAccuracy: 'unknown', cycleStartMs: null })],
      20_000_000
    );
    expect(buildAccountQuotaUsageRanges(definition, 20_000_000)).toEqual([]);
  });

  it('rejects non-finite usage durations instead of producing invalid ranges', () => {
    const [definition] = buildAccountQuotaWindowDefinitions([
      makeWindow({ limitWindowSeconds: Number.POSITIVE_INFINITY }),
    ]);

    expect(buildAccountQuotaUsageRanges(definition, 20_000_000)).toEqual([]);
  });
});
