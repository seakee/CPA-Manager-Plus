import { describe, expect, it, vi } from 'vitest';
import { getRangeBounds, shouldUseHourlyTimeline } from './range';

describe('monitoring range', () => {
  it('keeps 24 elapsed hours before the local hour and includes the current partial hour across timezones', () => {
    for (const [zone, now, boundary] of [
      ['Asia/Shanghai', '2026-11-03T00:32:00+08:00', '2026-11-03T00:00:00+08:00'],
      ['America/New_York', '2026-11-01T01:32:00-05:00', '2026-11-01T01:00:00-05:00'],
      ['Asia/Kathmandu', '2026-11-03T10:32:00+05:45', '2026-11-03T10:00:00+05:45'],
    ]) {
      try {
        vi.stubEnv('TZ', zone);
        const endMs = Date.parse(boundary);
        expect(getRangeBounds('24h', Date.parse(now))).toEqual({
          startMs: endMs - 86_400_000,
          endMs: Date.parse(now),
        });
        expect(getRangeBounds('24h', Date.parse(now) + 30_000)).toEqual({
          startMs: endMs - 86_400_000,
          endMs: Date.parse(now) + 30_000,
        });
        expect(getRangeBounds('24h', endMs + 3_600_000)).toEqual({
          startMs: endMs - 82_800_000,
          endMs: endMs + 3_600_000,
        });
      } finally {
        vi.unstubAllEnvs();
      }
    }
    expect(shouldUseHourlyTimeline('24h')).toBe(true);
  });
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
});
