import type { MonitoringCustomTimeRange, MonitoringTimeRange } from './types';

export const padNumber = (value: number) => String(value).padStart(2, '0');

export const buildLocalDayKey = (timestampMs: number) => {
  const date = new Date(timestampMs);
  return `${date.getFullYear()}-${padNumber(date.getMonth() + 1)}-${padNumber(date.getDate())}`;
};

export const buildHourLabel = (timestampMs: number) =>
  `${padNumber(new Date(timestampMs).getHours())}:00`;

export const buildDayLabel = (dayKey: string) => dayKey.slice(5).replace('-', '/');

const startOfTodayMs = (nowMs: number) => {
  const now = new Date(nowMs);
  now.setHours(0, 0, 0, 0);
  return now.getTime();
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
    case 'today':
      return { startMs: todayStart, endMs: nowMs };
    case 'yesterday':
      return { startMs: todayStart - 24 * 60 * 60 * 1000, endMs: todayStart };
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

export const shouldUseHourlyTimeline = (
  range: MonitoringTimeRange,
  customRange?: MonitoringCustomTimeRange | null
) =>
  range === 'today' ||
  range === 'yesterday' ||
  (range === 'custom' &&
    isValidCustomTimeRange(customRange) &&
    buildLocalDayKey(customRange.startMs) === buildLocalDayKey(customRange.endMs));
