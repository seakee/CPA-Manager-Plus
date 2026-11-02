import type { MonitoringCustomTimeRange, MonitoringTimeRange } from './types';

export const HOUR_MS = 60 * 60 * 1000;

// Subtract the local minute portion instead of mutating Date: setMinutes can
// jump back to the first occurrence of a repeated hour at a DST transition.
export const startOfLocalHourMs = (nowMs: number) => {
  const now = new Date(nowMs);
  return nowMs - now.getMinutes() * 60_000 - now.getSeconds() * 1000 - now.getMilliseconds();
};

export const padNumber = (value: number) => String(value).padStart(2, '0');

export const buildLocalDayKey = (timestampMs: number) => {
  const date = new Date(timestampMs);
  return `${date.getFullYear()}-${padNumber(date.getMonth() + 1)}-${padNumber(date.getDate())}`;
};

export const buildHourLabel = (timestampMs: number) =>
  `${padNumber(new Date(timestampMs).getHours())}:00`;

export const formatHourOffset = (timestampMs: number) => {
  const offset = -new Date(timestampMs).getTimezoneOffset();
  return `${offset >= 0 ? '+' : '-'}${padNumber(Math.floor(Math.abs(offset) / 60))}:${padNumber(Math.abs(offset) % 60)}`;
};

export const buildDayLabel = (dayKey: string) => dayKey.slice(5).replace('-', '/');

const startOfTodayMs = (nowMs: number) => {
  const now = new Date(nowMs);
  now.setHours(0, 0, 0, 0);
  return now.getTime();
};

const startOfPreviousLocalDayMs = (todayStartMs: number) => {
  const previousDay = new Date(todayStartMs);
  previousDay.setDate(previousDay.getDate() - 1);
  return previousDay.getTime();
};

const isValidCustomTimeRange = (
  range: MonitoringCustomTimeRange | null | undefined
): range is MonitoringCustomTimeRange =>
  Boolean(
    range &&
    Number.isFinite(range.startMs) &&
    Number.isFinite(range.endMs) &&
    range.startMs <= range.endMs
  );

export const getRangeBounds = (
  range: MonitoringTimeRange,
  nowMs: number,
  customRange?: MonitoringCustomTimeRange | null
) => {
  if (range === 'custom') {
    return isValidCustomTimeRange(customRange)
      ? { startMs: customRange.startMs, endMs: customRange.endMs }
      : null;
  }

  const todayStart = startOfTodayMs(nowMs);

  switch (range) {
    case '24h': {
      const endMs = startOfLocalHourMs(nowMs);
      return { startMs: endMs - 24 * HOUR_MS, endMs };
    }
    case 'today':
      return { startMs: todayStart, endMs: nowMs };
    case 'yesterday':
      return { startMs: startOfPreviousLocalDayMs(todayStart), endMs: todayStart };
    case '7d':
      return { startMs: todayStart - 6 * 24 * 60 * 60 * 1000, endMs: nowMs };
    case '14d':
      return { startMs: todayStart - 13 * 24 * 60 * 60 * 1000, endMs: nowMs };
    case '30d':
      return { startMs: todayStart - 29 * 24 * 60 * 60 * 1000, endMs: nowMs };
    case 'all':
    default:
      return { startMs: Number.NEGATIVE_INFINITY, endMs: nowMs };
  }
};

// Display the last included instant, without altering the API's exclusive end.
export const getRangeDisplayBounds = (
  range: MonitoringTimeRange,
  bounds: MonitoringCustomTimeRange | null
) =>
  bounds && (range === '24h' || range === 'yesterday')
    ? { ...bounds, endMs: bounds.endMs - 1 }
    : bounds;

export const shouldUseHourlyTimeline = (
  range: MonitoringTimeRange,
  customRange?: MonitoringCustomTimeRange | null
) =>
  range === '24h' ||
  range === 'today' ||
  range === 'yesterday' ||
  (range === 'custom' &&
    isValidCustomTimeRange(customRange) &&
    buildLocalDayKey(customRange.startMs) === buildLocalDayKey(customRange.endMs));
