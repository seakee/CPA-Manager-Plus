import { describe, expect, it } from 'vitest';
import { getRangeBounds, shouldUseHourlyTimeline } from './range';

describe('monitoring range', () => {
  it('uses the previous local calendar day for yesterday', () => {
    const nowMs = new Date(2026, 7, 28, 15, 30, 0, 0).getTime();

    expect(getRangeBounds('yesterday', nowMs)).toEqual({
      startMs: new Date(2026, 7, 27, 0, 0, 0, 0).getTime(),
      endMs: new Date(2026, 7, 28, 0, 0, 0, 0).getTime(),
    });
  });

  it('uses an hourly timeline for yesterday', () => {
    expect(shouldUseHourlyTimeline('yesterday')).toBe(true);
  });

  it('keeps the existing range behavior', () => {
    const nowMs = new Date(2026, 7, 28, 15, 30, 0, 0).getTime();
    const todayStartMs = new Date(2026, 7, 28, 0, 0, 0, 0).getTime();

    expect(getRangeBounds('today', nowMs)).toEqual({
      startMs: todayStartMs,
      endMs: nowMs,
    });
    expect(getRangeBounds('7d', nowMs)).toEqual({
      startMs: todayStartMs - 6 * 24 * 60 * 60 * 1000,
      endMs: nowMs,
    });
    expect(getRangeBounds('14d', nowMs)).toEqual({
      startMs: todayStartMs - 13 * 24 * 60 * 60 * 1000,
      endMs: nowMs,
    });
    expect(getRangeBounds('30d', nowMs)).toEqual({
      startMs: todayStartMs - 29 * 24 * 60 * 60 * 1000,
      endMs: nowMs,
    });
    expect(getRangeBounds('all', nowMs)).toEqual({
      startMs: Number.NEGATIVE_INFINITY,
      endMs: nowMs,
    });
  });

  it('aligns yesterday on the previous local calendar day regardless of DST length', () => {
    const cases: Array<{ label: string; now: Date }> = [
      { label: 'normal day', now: new Date(2026, 7, 28, 15, 30, 0, 0) },
      { label: 'spring-forward day (after switch)', now: new Date(2026, 2, 9, 0, 30, 0, 0) },
      { label: 'spring-forward day (mid-day)', now: new Date(2026, 2, 8, 12, 0, 0, 0) },
      { label: 'fall-back day (after switch)', now: new Date(2026, 10, 2, 0, 30, 0, 0) },
      { label: 'fall-back day (mid-day)', now: new Date(2026, 10, 1, 12, 0, 0, 0) },
    ];

    for (const { label, now } of cases) {
      const nowMs = now.getTime();
      const bounds = getRangeBounds('yesterday', nowMs);
      if (!bounds) throw new Error(`expected bounds for ${label}`);

      const expectedEnd = new Date(nowMs);
      expectedEnd.setHours(0, 0, 0, 0);
      const expectedStart = new Date(expectedEnd.getTime() - 24 * 60 * 60 * 1000);
      expectedStart.setHours(0, 0, 0, 0);

      expect(bounds.endMs, `${label}: endMs should equal local midnight`).toBe(expectedEnd.getTime());
      expect(bounds.startMs, `${label}: startMs should equal previous local midnight`).toBe(
        expectedStart.getTime()
      );

      const start = new Date(bounds.startMs);
      const end = new Date(bounds.endMs);
      expect(start.getHours(), `${label}: start hour`).toBe(0);
      expect(start.getMinutes(), `${label}: start minute`).toBe(0);
      expect(start.getSeconds(), `${label}: start second`).toBe(0);
      expect(start.getMilliseconds(), `${label}: start millisecond`).toBe(0);
      expect(end.getHours(), `${label}: end hour`).toBe(0);
    }
  });
});
