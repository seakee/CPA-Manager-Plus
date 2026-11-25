export const formatCompactTokens = (value: number): string => {
  if (!Number.isFinite(value) || value <= 0) return '0';
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1).replace(/\.0$/, '')}B`;
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1).replace(/\.0$/, '')}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1).replace(/\.0$/, '')}K`;
  return String(Math.round(value));
};

export const formatCompactHistoryUsd = (value: number): string => {
  if (!Number.isFinite(value) || value <= 0) return '$0.00';
  if (value < 0.01) return '<$0.01';
  if (value >= 1_000) return `$${formatCompactTokens(value)}`;
  return `$${value.toFixed(2)}`;
};

export const formatCompactTimestamp = (
  value: number | null | undefined,
  locale?: string
): { compact: string | null; full: string | null } => {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return { compact: null, full: null };
  }
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) {
    return { compact: null, full: null };
  }
  const full = date.toLocaleString(locale, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
  const now = new Date();
  const sameYear = date.getFullYear() === now.getFullYear();
  const sameDay =
    sameYear && date.getMonth() === now.getMonth() && date.getDate() === now.getDate();
  const compact = sameDay
    ? date.toLocaleTimeString(locale, {
        hour: '2-digit',
        minute: '2-digit',
        hour12: false,
      })
    : sameYear
      ? date.toLocaleString(locale, {
          month: '2-digit',
          day: '2-digit',
          hour: '2-digit',
          minute: '2-digit',
          hour12: false,
        })
      : full;
  return { compact, full };
};
