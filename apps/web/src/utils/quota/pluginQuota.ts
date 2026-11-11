/**
 * Pure helpers for CPA plugin quota providers.
 *
 * Plugin credentials advertise quota support through the auth-file entry
 * (`supports_quota` plus `quota_provider`, and optionally `quota_plugin_id`).
 * The management panel resolves the owning plugin, then queries the generic
 * plugin quota endpoint `POST /v0/management/plugins/{pluginId}/quota`.
 */

import type { AuthFileItem, PluginListEntry, PluginQuotaSummaryItem } from '@/types';

const readTrimmedString = (value: unknown): string => {
  if (typeof value !== 'string' && typeof value !== 'number') return '';
  return String(value).trim();
};

export const readAuthFileQuotaPluginId = (file: AuthFileItem): string => {
  const raw = file['quota_plugin_id'] ?? file.quotaPluginId;
  return readTrimmedString(raw);
};

export const readAuthFileQuotaProvider = (file: AuthFileItem): string => {
  const raw = file['quota_provider'] ?? file.quotaProvider;
  return readTrimmedString(raw);
};

export const readAuthFileQuotaAuthIndex = (file: AuthFileItem): string => {
  const raw = file['auth_index'] ?? file.authIndex;
  return readTrimmedString(raw);
};

export const isPluginQuotaCandidateFile = (file: AuthFileItem): boolean => {
  const raw = file['supports_quota'] ?? file.supportsQuota;
  if (typeof raw === 'boolean') return raw;
  if (typeof raw === 'string') return raw.trim().toLowerCase() === 'true';
  return false;
};

/**
 * Pick the plugin that serves quota for a credential provider. Explicit
 * `quota_plugin_id` wins; otherwise match the plugin's declared
 * `quota_provider`, falling back to the plugin id itself. Only enabled
 * plugins are eligible, mirroring the upstream management center.
 */
export const resolveQuotaPluginFromList = (
  provider: string,
  plugins: PluginListEntry[]
): PluginListEntry | null => {
  const candidates = plugins.filter((plugin) => plugin.effectiveEnabled || plugin.enabled);
  const providerKey = provider.trim().toLowerCase();
  if (!providerKey) return null;
  return (
    candidates.find(
      (plugin) => (plugin.quotaProvider ?? '').trim().toLowerCase() === providerKey
    ) ?? candidates.find((plugin) => plugin.id.trim().toLowerCase() === providerKey) ?? null
  );
};

export type PluginQuotaWindowLabelKey =
  | 'window_five_hour'
  | 'window_daily'
  | 'window_weekly'
  | 'window_monthly';

/**
 * Map a plugin bucket window hint (e.g. "5h", "7d", "monthly") onto a
 * localized label key. Returns null when the window is unknown so callers can
 * fall back to the raw label reported by the plugin.
 */
export const getPluginQuotaWindowLabelKey = (
  window: string | undefined
): PluginQuotaWindowLabelKey | null => {
  const normalized = (window ?? '').trim().toLowerCase().replace(/[\s_]+/g, '-');
  if (!normalized) return null;
  if (['5h', '5-hour', 'five-hour', 'five-hour-limit', '5-hour-limit'].includes(normalized)) {
    return 'window_five_hour';
  }
  if (['24h', 'day', 'daily', 'daily-limit'].includes(normalized)) return 'window_daily';
  if (['7d', 'week', 'weekly', 'weekly-limit'].includes(normalized)) return 'window_weekly';
  if (['30d', 'month', 'monthly', 'monthly-limit'].includes(normalized)) return 'window_monthly';
  return null;
};

/**
 * Convert a window hint like "5h" / "7d" / "monthly" into an approximate
 * duration in seconds for interval display. Calendar windows (week/month)
 * use their nominal length.
 */
export const parsePluginQuotaWindowSeconds = (window: string | undefined): number | null => {
  const normalized = (window ?? '').trim().toLowerCase().replace(/[\s_]+/g, '-');
  if (!normalized) return null;
  const match = normalized.match(/^(\d+)(m|h|d|w)$/);
  if (match) {
    const amount = Number(match[1]);
    const multiplier = { m: 60, h: 3600, d: 86400, w: 604800 }[match[2] as 'm' | 'h' | 'd' | 'w'];
    return Number.isFinite(amount) && Number.isFinite(multiplier) ? amount * multiplier : null;
  }
  const wordMatch = normalized.match(/^(\d+)[-\s]?hour/);
  if (wordMatch) {
    const hours = Number(wordMatch[1]);
    return Number.isFinite(hours) && hours > 0 ? hours * 3600 : 5 * 3600;
  }
  if (normalized.includes('hour')) return 5 * 3600;
  if (normalized.includes('day')) return 24 * 3600;
  if (normalized.includes('week')) return 7 * 86400;
  if (normalized.includes('month')) return 30 * 86400;
  return null;
};

const formatNumberWith = (
  value: number,
  locale: string | undefined,
  options: Intl.NumberFormatOptions = {}
): string | null => {
  try {
    return new Intl.NumberFormat(locale || undefined, options).format(value);
  } catch {
    return null;
  }
};

// Whether this locale/formatting rounds the value's magnitude down to an
// exact-zero display ("0"/"-0"), which would hide meaningful nonzero quota
// evidence behind a reported-zero look.
const rendersAsZero = (
  value: number,
  locale: string | undefined,
  options: Intl.NumberFormatOptions = {}
): boolean => {
  const zeroDisplay = formatNumberWith(0, locale, options);
  if (zeroDisplay === null) return false;
  const magnitudeDisplay = formatNumberWith(Math.abs(value), locale, options);
  return magnitudeDisplay !== null && magnitudeDisplay === zeroDisplay;
};

const formatSummaryDecimal = (value: number, locale?: string): string => {
  const formatted = formatNumberWith(value, locale);
  if (formatted === null) return String(value);
  if (value !== 0 && rendersAsZero(value, locale)) {
    // Keep meaningful nonzero precision with explicit fraction digits, then
    // fall back to a faithful plain/scientific representation for values too
    // small for any fixed cap. Genuine zero never enters this branch.
    const preciseOptions = { maximumFractionDigits: 20 };
    if (!rendersAsZero(value, locale, preciseOptions)) {
      return formatNumberWith(value, locale, preciseOptions) ?? String(value);
    }
    return String(value);
  }
  return formatted;
};

/**
 * Format one generic plugin quota summary item using only its declared
 * primitive value and metadata. Numeric percent values are reported as-is
 * (32 with unit "%" displays "32%", never 3,200%), currency values use the
 * declared currency code, textual values stay plain text, and missing or
 * invalid values return null so callers can show an unknown presentation
 * instead of a fabricated zero.
 */
export const formatPluginQuotaSummaryValue = (
  item: Pick<PluginQuotaSummaryItem, 'value' | 'unit' | 'format' | 'currency'>,
  locale?: string
): string | null => {
  if (typeof item.value === 'number') {
    if (!Number.isFinite(item.value)) return null;
    const format = (item.format ?? '').trim().toLowerCase();
    const unit = (item.unit ?? '').trim();
    if (format === 'currency') {
      const currency = (item.currency ?? '').trim();
      if (currency) {
        try {
          const currencyOptions: Intl.NumberFormatOptions = { style: 'currency', currency };
          const formatted = new Intl.NumberFormat(locale || undefined, currencyOptions).format(
            item.value
          );
          // A tiny nonzero balance must not display as an exact zero balance.
          if (item.value !== 0 && rendersAsZero(item.value, locale, currencyOptions)) {
            const preciseOptions = { ...currencyOptions, maximumFractionDigits: 20 };
            if (!rendersAsZero(item.value, locale, preciseOptions)) {
              return new Intl.NumberFormat(locale || undefined, preciseOptions).format(item.value);
            }
            return `${formatSummaryDecimal(item.value, locale)} ${currency}`;
          }
          return formatted;
        } catch {
          // Unsupported currency code: keep the declared code as plain text
          // instead of inventing a conversion.
          return `${formatSummaryDecimal(item.value, locale)} ${currency}`;
        }
      }
      return `${formatSummaryDecimal(item.value, locale)}${unit ? ` ${unit}` : ''}`;
    }
    const formatted = formatSummaryDecimal(item.value, locale);
    if (!unit) return format === 'percent' ? `${formatted}%` : formatted;
    // Percent-like units join directly ("32%"); word units keep a space.
    return unit.startsWith('%') ? `${formatted}${unit}` : `${formatted} ${unit}`;
  }
  if (typeof item.value === 'string') {
    const text = item.value.trim();
    return text || null;
  }
  return null;
};

/**
 * Whether at least one normalized summary item carries a meaningful value.
 * Numeric zero and nonempty text count as evidence; all-null or invalid
 * summaries do not, so callers can keep treating them as failures instead of
 * replacing real facts with unknown placeholders.
 */
export const hasMeaningfulPluginQuotaSummaryValue = (
  items: PluginQuotaSummaryItem[]
): boolean => items.some((item) => item.value !== null);
