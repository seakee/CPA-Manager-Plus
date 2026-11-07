import { describe, expect, it } from 'vitest';
import {
  WINDOW_PACE_MAX_OBSERVATION_AGE_MS,
  WINDOW_PACE_MIN_ELAPSED_MS,
  estimateWindowPace,
  type EstimateWindowPaceInput,
} from './estimateWindowPace';

const NOW_MS = 1_700_000_000_000;
const HOUR_MS = 60 * 60 * 1000;

const fiveHourWindow = (
  overrides: Partial<EstimateWindowPaceInput> = {}
): EstimateWindowPaceInput => ({
  usedPercent: 50,
  windowMode: 'fixed',
  cycleStartMs: NOW_MS - 2 * HOUR_MS,
  cycleEndMs: NOW_MS + 3 * HOUR_MS,
  resetAtMs: NOW_MS + 3 * HOUR_MS,
  limitWindowSeconds: 5 * 60 * 60,
  quotaProgressObservedAtMs: NOW_MS,
  boundaryAccuracy: 'exact',
  stale: false,
  nowMs: NOW_MS,
  ...overrides,
});

describe('estimateWindowPace', () => {
  it('projects an exhaustion before the scheduled reset', () => {
    const pace = estimateWindowPace(fiveHourWindow());

    expect(pace).not.toBeNull();
    expect(pace?.status).toBe('exhausts_before_reset');
    expect(pace?.resetsAtMs).toBe(NOW_MS + 3 * HOUR_MS);
    expect(pace?.remainingPercent).toBe(50);
    // 50% used over two hours means the remaining 50% takes two more hours.
    expect(pace?.exhaustsAtMs).toBe(NOW_MS + 2 * HOUR_MS);
  });

  it('reports a window that outlives its reset', () => {
    const pace = estimateWindowPace(fiveHourWindow({ usedPercent: 20 }));

    expect(pace?.status).toBe('lasts_until_reset');
    // 20% per two hours leaves 80% to burn over eight hours, past the reset.
    expect(pace?.exhaustsAtMs).toBe(NOW_MS + 8 * HOUR_MS);
  });

  it('derives the cycle start of a fixed window from its advertised length', () => {
    const pace = estimateWindowPace(
      fiveHourWindow({ cycleStartMs: null, cycleEndMs: null, limitWindowSeconds: 18_000 })
    );

    expect(pace?.exhaustsAtMs).toBe(NOW_MS + 2 * HOUR_MS);
  });

  it('prefers the reset hint over the cycle end', () => {
    const pace = estimateWindowPace(
      fiveHourWindow({ resetAtMs: NOW_MS + 90 * 60 * 1000, cycleEndMs: NOW_MS + 3 * HOUR_MS })
    );

    expect(pace?.resetsAtMs).toBe(NOW_MS + 90 * 60 * 1000);
  });

  it('returns null for calendar windows without an observed start', () => {
    expect(
      estimateWindowPace(
        fiveHourWindow({
          windowMode: 'calendar',
          cycleStartMs: null,
          cycleEndMs: null,
          resetAtMs: NOW_MS + 3 * HOUR_MS,
        })
      )
    ).toBeNull();
  });

  it('returns null for windows without a scheduled reset', () => {
    expect(
      estimateWindowPace(
        fiveHourWindow({
          windowMode: 'rolling',
          cycleEndMs: null,
          resetAtMs: null,
        })
      )
    ).toBeNull();
  });

  it('returns null for stale snapshots', () => {
    expect(estimateWindowPace(fiveHourWindow({ stale: true }))).toBeNull();
  });

  it('returns null when the boundary accuracy is unknown', () => {
    expect(estimateWindowPace(fiveHourWindow({ boundaryAccuracy: 'unknown' }))).toBeNull();
  });

  it('returns null when the observation is older than the freshness budget', () => {
    expect(
      estimateWindowPace(
        fiveHourWindow({
          quotaProgressObservedAtMs: NOW_MS - WINDOW_PACE_MAX_OBSERVATION_AGE_MS - 1000,
        })
      )
    ).toBeNull();
  });

  it('accepts observations right at the freshness budget', () => {
    expect(
      estimateWindowPace(
        fiveHourWindow({
          quotaProgressObservedAtMs: NOW_MS - WINDOW_PACE_MAX_OBSERVATION_AGE_MS,
        })
      )
    ).not.toBeNull();
  });

  it('falls back to the observation time when the progress timestamp is missing', () => {
    const observedAtMs = NOW_MS - 5 * 60 * 1000;
    const pace = estimateWindowPace(
      fiveHourWindow({ quotaProgressObservedAtMs: null, observedAtMs })
    );

    expect(pace?.exhaustsAtMs).toBe(observedAtMs + (observedAtMs - (NOW_MS - 2 * HOUR_MS)));
  });

  it('returns null while the window has barely started', () => {
    expect(
      estimateWindowPace(
        fiveHourWindow({
          cycleStartMs: NOW_MS - WINDOW_PACE_MIN_ELAPSED_MS + 1000,
          cycleEndMs: NOW_MS + 5 * HOUR_MS,
          resetAtMs: NOW_MS + 5 * HOUR_MS,
        })
      )
    ).toBeNull();
  });

  it.each([null, undefined, Number.NaN])(
    'returns null for an unusable used percent (%s)',
    (usedPercent) => {
      expect(estimateWindowPace(fiveHourWindow({ usedPercent }))).toBeNull();
    }
  );

  it('reports a window without any usage instead of guessing a rate', () => {
    const pace = estimateWindowPace(fiveHourWindow({ usedPercent: 0 }));

    expect(pace).toMatchObject({
      status: 'no_usage',
      exhaustsAtMs: null,
      resetsAtMs: NOW_MS + 3 * HOUR_MS,
      remainingPercent: 100,
    });
  });

  it.each([100, 120])('reports an exhausted window when usage is at %s percent', (usedPercent) => {
    const pace = estimateWindowPace(fiveHourWindow({ usedPercent }));

    expect(pace).toMatchObject({
      status: 'exhausted',
      exhaustsAtMs: NOW_MS,
      resetsAtMs: NOW_MS + 3 * HOUR_MS,
      remainingPercent: 0,
    });
  });

  it('does not report an exhausted window that has already reset', () => {
    expect(
      estimateWindowPace(
        fiveHourWindow({
          usedPercent: 100,
          resetAtMs: NOW_MS - HOUR_MS,
          cycleEndMs: NOW_MS - HOUR_MS,
        })
      )
    ).toBeNull();
  });

  it('returns null once the window has already been reset', () => {
    expect(
      estimateWindowPace(
        fiveHourWindow({
          cycleStartMs: NOW_MS - 6 * HOUR_MS,
          resetAtMs: NOW_MS - HOUR_MS,
          cycleEndMs: NOW_MS - HOUR_MS,
        })
      )
    ).toBeNull();
  });

  it('returns null when the projection is already due but quota is still reported', () => {
    // 90% used in the first two hours would already be exhausted, which
    // contradicts the provider still reporting spare quota.
    expect(
      estimateWindowPace(
        fiveHourWindow({
          usedPercent: 99,
          quotaProgressObservedAtMs: NOW_MS - 20 * 60 * 1000,
        })
      )
    ).toBeNull();
  });

  it('returns null when the observation time is ahead of the local clock', () => {
    expect(
      estimateWindowPace(fiveHourWindow({ quotaProgressObservedAtMs: NOW_MS + 5 * 60 * 1000 }))
    ).toBeNull();
  });
});
