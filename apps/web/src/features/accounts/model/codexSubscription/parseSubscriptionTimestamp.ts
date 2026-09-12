import { parseTimestampMs } from '@/utils/timestamp';

export const parseSubscriptionTimestampMs = (value: unknown): number | null => {
  const numeric =
    typeof value === 'number'
      ? value
      : typeof value === 'string' && /^\d+(?:\.\d+)?$/.test(value.trim())
        ? Number(value.trim())
        : null;
  const parsed =
    numeric !== null && Number.isFinite(numeric)
      ? numeric < 1e12
        ? numeric * 1000
        : numeric
      : parseTimestampMs(value);
  if (!Number.isFinite(parsed) || parsed <= 0) return null;
  return Number.isNaN(new Date(parsed).getTime()) ? null : parsed;
};
