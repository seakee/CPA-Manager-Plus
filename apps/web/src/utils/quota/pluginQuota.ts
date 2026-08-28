import type { AuthFileItem, QuotaResetAccuracy } from '@/types';

const SCHEMA = 'cliproxy.plugin.quota';
const MAX_ITEMS = 32;
const MAX_TEXT = 128;
const MIN_TIMESTAMP_MS = Date.UTC(2000, 0, 1);
const MAX_TIMESTAMP_MS = Date.UTC(2100, 0, 1);
const WINDOW_KINDS = [
  'five_hour',
  'daily',
  'weekly',
  'monthly',
  'billing',
  'payg',
  'product',
  'summary',
  'unknown',
] as const;

export interface PluginQuotaSpend {
  meteredMinorUnits: number | null;
  todayMinorUnits: number | null;
  periodMinorUnits: number | null;
  latestTokens: number | null;
  periodTokens: number | null;
}

export interface PluginQuotaDaily {
  date: string;
  costMinorUnits: number;
  tokens: number | null;
}

export interface PluginQuotaWindow {
  id: string;
  label: string;
  kind?: (typeof WINDOW_KINDS)[number];
  unit?: string;
  currency?: string;
  minorUnit?: number;
  used: number | null;
  limit: number | null;
  remaining: number | null;
  usedPercent: number | null;
  unlimited: boolean;
  windowStartMs: number | null;
  windowEndMs: number | null;
  resetAt: string;
  resetAtMs: number | null;
  resetAccuracy: QuotaResetAccuracy;
}

export interface PluginQuotaContract {
  availability: 'available' | 'unavailable';
  observedAtMs: number | null;
  stale: boolean;
  currency: string | null;
  minorUnit: number | null;
  windows: PluginQuotaWindow[];
  spend: PluginQuotaSpend | null;
  daily: PluginQuotaDaily[];
  topModel: string | null;
  provenance: string[];
}

const record = (value: unknown): Record<string, unknown> | null =>
  typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;

// eslint-disable-next-line no-control-regex
const CONTROL_CHARACTERS = /[\u0000-\u001f\u007f]+/g;
const HOST_ESCAPES = ['&amp;', '&lt;', '&gt;', '&#34;', '&#39;'];
const RESTORED_TEXT = ['&', '<', '>', '"', "'"];
const decodeHostEscape = (entity: string) => RESTORED_TEXT[HOST_ESCAPES.indexOf(entity)];
const text = (value: unknown): string =>
  typeof value === 'string'
    ? value
        .replace(/&(?:amp|lt|gt|#34|#39);/g, decodeHostEscape)
        .replace(CONTROL_CHARACTERS, ' ')
        .trim()
        .slice(0, MAX_TEXT)
    : '';
const count = (value: unknown): number | null =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : null;
const percent = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= 100 ? value : null;
const currency = (value: unknown): string | null => {
  const parsed = text(value);
  return /^[A-Z]{3}$/.test(parsed) ? parsed : null;
};
const minorUnit = (value: unknown): number | null => {
  const parsed = count(value);
  return parsed !== null && parsed <= 9 ? parsed : null;
};
const validCalendarDate = (year: number, month: number, day: number): boolean => {
  const monthEnd = new Date(Date.UTC(year, month, 0)).getUTCDate();
  return month >= 1 && month <= 12 && day >= 1 && day <= monthEnd;
};
const timestamp = (value: unknown): number | null => {
  if (typeof value === 'number') {
    return Number.isSafeInteger(value) && value >= MIN_TIMESTAMP_MS && value <= MAX_TIMESTAMP_MS
      ? value
      : null;
  }
  const match = text(value).match(
    /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d+)?(Z|([+-])(\d{2}):(\d{2}))$/
  );
  if (!match) return null;
  const [year, month, day, hour, minute, second] = match.slice(1, 7).map(Number);
  const offsetHour = match[8] === 'Z' ? 0 : Number(match[10]);
  const offsetMinute = match[8] === 'Z' ? 0 : Number(match[11]);
  const invalidTime = [hour - 23, minute - 59, second - 59, offsetHour - 23, offsetMinute - 59];
  if (!validCalendarDate(year, month, day) || invalidTime.some((part) => part > 0)) return null;
  const fractionMs = Number((match[7]?.slice(1, 4) ?? '').padEnd(3, '0'));
  const offsetMs = (offsetHour * 60 + offsetMinute) * 60 * 1000;
  const parsed =
    Date.UTC(year, month - 1, day, hour, minute, second, fractionMs) -
    (match[9] === '-' ? -offsetMs : offsetMs);
  return parsed >= MIN_TIMESTAMP_MS && parsed <= MAX_TIMESTAMP_MS ? parsed : null;
};

const parseWindow = (value: unknown, version: 1 | 2): PluginQuotaWindow | null => {
  const item = record(value);
  const id = text(item?.id);
  if (!item || !id) return null;
  const unlimited = item.unlimited === true;
  const limit = count(item.limit);
  let used = count(item.used);
  let remaining = count(item.remaining);
  if (
    limit !== null &&
    ((used !== null && used > limit) ||
      (remaining !== null && remaining > limit) ||
      (used !== null && remaining !== null && used + remaining !== limit))
  ) {
    return null;
  }
  if (limit !== null && used !== null && remaining === null) remaining = limit - used;
  if (limit !== null && remaining !== null && used === null) used = limit - remaining;
  const utilization =
    unlimited || limit === 0
      ? null
      : limit !== null && used !== null
        ? (used / limit) * 100
        : percent(item.used_percent);
  const windowStartMs = timestamp(item.window_start);
  const windowEndMs = timestamp(item.window_end);
  const resetAtMs = timestamp(item.reset_at);
  if (utilization === null && !unlimited && windowEndMs === null && resetAtMs === null) return null;
  if (windowStartMs !== null && windowEndMs !== null && windowStartMs >= windowEndMs) return null;
  const unit = text(item.unit) || undefined;
  const moneyWindow = unit === 'currency_minor_units';
  const parsedCurrency = currency(item.currency);
  const parsedMinorUnit = minorUnit(item.minor_unit);
  if (moneyWindow && (version === 1 || parsedCurrency === null || parsedMinorUnit === null)) {
    return null;
  }
  const resetAccuracy = text(item.reset_accuracy);
  return {
    id,
    label: text(item.label) || id,
    kind: WINDOW_KINDS.find((kind) => kind === text(item.kind)),
    unit,
    ...(moneyWindow ? { currency: parsedCurrency!, minorUnit: parsedMinorUnit! } : {}),
    used,
    limit,
    remaining,
    usedPercent: utilization,
    unlimited,
    windowStartMs,
    windowEndMs,
    resetAt: resetAtMs === null ? '' : text(item.reset_at),
    resetAtMs,
    resetAccuracy:
      resetAtMs !== null && (resetAccuracy === 'exact' || resetAccuracy === 'estimated')
        ? resetAccuracy
        : 'unknown',
  };
};

const parseWindows = (value: unknown, version: 1 | 2): PluginQuotaWindow[] => {
  if (!Array.isArray(value) || value.length > MAX_ITEMS) return [];
  const seen = new Set<string>();
  return value.flatMap((item) => {
    const parsed = parseWindow(item, version);
    if (!parsed || seen.has(parsed.id)) return [];
    seen.add(parsed.id);
    return [parsed];
  });
};

const parseSpend = (value: unknown, version: 1 | 2): PluginQuotaSpend | null => {
  const item = record(value);
  if (!item) return null;
  const money = (v1: string, v2: string) => count(item[version === 1 ? v1 : v2]);
  const parsed = {
    meteredMinorUnits: money('metered_cents', 'metered_minor_units'),
    todayMinorUnits: money('today_cents', 'today_minor_units'),
    periodMinorUnits: money('period_cents', 'period_minor_units'),
    latestTokens: count(item.latest_tokens),
    periodTokens: count(item.period_tokens),
  };
  return Object.values(parsed).some((value) => value !== null) ? parsed : null;
};

const validDay = (value: string): boolean => {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
  const [year, month, day] = value.split('-').map(Number);
  return validCalendarDate(year, month, day);
};

const parseDaily = (value: unknown, version: 1 | 2): PluginQuotaDaily[] => {
  if (!Array.isArray(value) || value.length > MAX_ITEMS) return [];
  const seen = new Set<string>();
  return value.flatMap((entry) => {
    const item = record(entry);
    const date = text(item?.date);
    const costMinorUnits = count(item?.[version === 1 ? 'cost_cents' : 'cost_minor_units']);
    if (!item || !validDay(date) || costMinorUnits === null || seen.has(date)) return [];
    seen.add(date);
    return [{ date, costMinorUnits, tokens: count(item.tokens) }];
  });
};

export const parsePluginQuotaContract = (
  file: AuthFileItem,
  nowMs = Date.now()
): PluginQuotaContract | null => {
  const payload = record(file.plugin_quota);
  const version = payload?.version;
  if (!payload || text(payload.schema) !== SCHEMA || (version !== 1 && version !== 2)) return null;
  const observedAtMs = timestamp(payload.observed_at);
  const rawTtl = count(payload.ttl_seconds);
  const ttl = rawTtl && Math.min(rawTtl, 7 * 24 * 60 * 60);
  const stale =
    observedAtMs === null ||
    !ttl ||
    observedAtMs > nowMs + 5 * 60 * 1000 ||
    nowMs >= observedAtMs + ttl * 1000;
  const available = text(payload.availability) === 'available' && !stale;
  const spendRecord = record(payload.spend);
  const parsedCurrency = currency(spendRecord?.currency);
  const parsedMinorUnit = version === 1 ? 2 : minorUnit(spendRecord?.minor_unit);
  const parsedSpend = available ? parseSpend(payload.spend, version) : null;
  const spend =
    parsedSpend &&
    (parsedCurrency !== null && parsedMinorUnit !== null
      ? parsedSpend
      : {
          ...parsedSpend,
          meteredMinorUnits: null,
          todayMinorUnits: null,
          periodMinorUnits: null,
        });
  return {
    availability: available ? 'available' : 'unavailable',
    observedAtMs,
    stale,
    currency: parsedCurrency,
    minorUnit: parsedMinorUnit,
    windows: available ? parseWindows(payload.windows, version) : [],
    spend: spend && Object.values(spend).some((value) => value !== null) ? spend : null,
    daily: available ? parseDaily(payload.daily, version) : [],
    topModel: available && version === 2 ? text(payload.top_model) || null : null,
    provenance:
      available &&
      version === 2 &&
      Array.isArray(payload.provenance) &&
      payload.provenance.length <= 16
        ? [...new Set(payload.provenance.map(text).filter(Boolean))]
        : [],
  };
};
