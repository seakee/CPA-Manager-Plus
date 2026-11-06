/**
 * Pure helpers for CPA plugin quota providers.
 *
 * Plugin credentials advertise quota support through the auth-file entry
 * (`supports_quota` plus `quota_provider`, and optionally `quota_plugin_id`).
 * The management panel resolves the owning plugin, then queries the generic
 * plugin quota endpoint `POST /v0/management/plugins/{pluginId}/quota`.
 */

import type { AuthFileItem, PluginListEntry } from '@/types';

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
