import type { TFunction } from 'i18next';
import { ANTIGRAVITY_CONFIG } from '@/components/quota';
import {
  getQuotaWindowShortLabel,
  isModelScopedAccountQuotaWindow,
  isStandardAccountQuotaListWindow,
} from '@/features/accounts/model/accountQuotaDisplayWindows';
import type {
  AccountQuotaWindowKind,
  AccountQuotaDisplayWindow,
} from '@/features/accounts/model/accountQuotaDisplayWindows';
import type {
  AccountRow,
  AccountRowSortDirection,
  AccountRowSortKey,
} from '@/features/accounts/model/accountRows';
import {
  getAuthFilePatchTarget,
  type AuthFileCodexInspectionSnapshot,
} from '@/features/authFiles/model/credentialStatus';
import type { MonitoringAccountHistoryItem, MonitoringAnalyticsEventRow } from '@/services/api';
import { parseQuotaResetLabelMs } from '@/utils/quota/formatters';
import { formatUsd } from '@/utils/usage';
import { getEffectiveAccountInspectionAction } from './accountCredentialEvidence';

export type AccountsView = 'accounts' | 'health' | 'oauth';
export type DetailTab = 'overview' | 'quota' | 'config' | 'models' | 'diagnostics';
export type SortableAccountColumn = Extract<
  AccountRowSortKey,
  'name' | 'plan' | 'note' | 'reset' | 'remaining' | 'priority' | 'recent' | 'quota' | 'created'
>;
export type AccountSortFieldValue = 'default' | SortableAccountColumn;
type AntigravityQuotaMatrixWindowKind = Extract<AccountQuotaWindowKind, 'five_hour' | 'weekly'>;

export interface AntigravityQuotaMatrixCell {
  groupLabel: string;
  displayLabel: string;
  window: AccountQuotaDisplayWindow;
}

export interface AntigravityQuotaMatrixRow {
  key: AntigravityQuotaMatrixWindowKind;
  label: string;
  cells: AntigravityQuotaMatrixCell[];
}

export interface AntigravityQuotaMatrix {
  rows: AntigravityQuotaMatrixRow[];
  windowKeys: Set<string>;
}

export const PAGE_SIZE_OPTIONS = [
  { value: '10', label: '10' },
  { value: '20', label: '20' },
  { value: '50', label: '50' },
];

export const DETAIL_EVENTS_RANGE_MS = 7 * 24 * 60 * 60 * 1000;
export const DETAIL_EVENTS_LIMIT = 20;

export const ACCOUNT_SORT_DEFAULT_DIRECTIONS: Record<
  SortableAccountColumn,
  AccountRowSortDirection
> = {
  name: 'asc',
  plan: 'asc',
  note: 'asc',
  reset: 'asc',
  remaining: 'asc',
  priority: 'desc',
  recent: 'desc',
  quota: 'desc',
  created: 'desc',
};

const DEFAULT_ACCOUNT_SORT_FIELD_OPTION = {
  value: 'default',
  labelKey: 'accounts.sort_default',
} as const;

export const ACCOUNT_SORT_FIELD_OPTIONS: Array<{
  value: AccountSortFieldValue;
  labelKey: string;
}> = [
  DEFAULT_ACCOUNT_SORT_FIELD_OPTION,
  { value: 'name', labelKey: 'accounts.sort_name' },
  { value: 'plan', labelKey: 'accounts.col_plan' },
  { value: 'remaining', labelKey: 'accounts.sort_remaining' },
  { value: 'note', labelKey: 'auth_files.note_label' },
  { value: 'reset', labelKey: 'accounts.col_reset' },
  { value: 'quota', labelKey: 'accounts.col_quota' },
  { value: 'priority', labelKey: 'accounts.col_priority' },
  { value: 'recent', labelKey: 'accounts.col_recent' },
  { value: 'created', labelKey: 'accounts.col_created' },
];

export const getAccountSortFieldOption = (value: AccountSortFieldValue) =>
  ACCOUNT_SORT_FIELD_OPTIONS.find((option) => option.value === value) ??
  DEFAULT_ACCOUNT_SORT_FIELD_OPTION;

export const getProviderLabel = (provider: string, t: TFunction) => {
  const key = `auth_files.filter_${provider}`;
  const translated = t(key);
  if (translated !== key) return translated;
  if (provider === 'all') return t('accounts.filter_all');
  if (provider === 'iflow') return 'iFlow';
  if (provider === 'xai') return 'xAI';
  return provider.charAt(0).toUpperCase() + provider.slice(1);
};

export const formatPercent = (value: number | null | undefined, digits = 0) =>
  typeof value === 'number' && Number.isFinite(value) ? `${value.toFixed(digits)}%` : '-';

export const formatMoney = (value: number) => formatUsd(value);

export const formatHistoryNumber = (value: number, locale: string) => {
  const numberValue = Number(value);
  if (!Number.isFinite(numberValue)) return '-';
  return new Intl.NumberFormat(locale || undefined).format(numberValue);
};

export const formatHistorySuccessRate = (value: number | null | undefined, digits = 1) =>
  typeof value === 'number' && Number.isFinite(value) ? formatPercent(value * 100, digits) : '-';

export const getAccountHistoryTitle = (
  t: TFunction,
  item: MonitoringAccountHistoryItem | null,
  loading: boolean,
  error: string,
  locale = 'en-US'
) => {
  if (error) return t('accounts.history_unavailable');
  if (loading && !item) return t('accounts.history_loading');
  if (!item || !item.matched) return t('accounts.history_empty');
  if (item.sync_status === 'pending') return t('accounts.history_pending_title');
  return t('accounts.history_title', {
    requests: formatHistoryNumber(item.total_requests, locale),
    tokens: formatHistoryNumber(item.total_tokens, locale),
    cost: formatMoney(item.total_cost),
    rate: formatHistorySuccessRate(item.success_rate, 2),
  });
};

export const parsePriorityValue = (value: string) => {
  const trimmed = value.trim();
  if (!/^-?\d+$/.test(trimmed)) return null;
  const parsed = Number(trimmed);
  return Number.isSafeInteger(parsed) ? parsed : null;
};

const resolveValidTimestampDate = (value: number | null | undefined): Date | null => {
  if (typeof value !== 'number' || !Number.isFinite(value) || value === 0) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
};

const padTimestampPart = (value: number) => String(value).padStart(2, '0');

const formatNumericTimestamp = (date: Date, includeSeconds = false) => {
  const base = `${padTimestampPart(date.getMonth() + 1)}/${padTimestampPart(
    date.getDate()
  )} ${padTimestampPart(date.getHours())}:${padTimestampPart(date.getMinutes())}`;
  return includeSeconds ? `${base}:${padTimestampPart(date.getSeconds())}` : base;
};

const QUOTA_RESET_DAY_MS = 24 * 60 * 60 * 1000;

export const formatTimestamp = (value: number | null, _locale: string, includeSeconds = false) => {
  const date = resolveValidTimestampDate(value);
  if (!date) return '-';
  return formatNumericTimestamp(date, includeSeconds);
};

export const formatTimestampTitle = (
  value: number | null | undefined,
  locale: string
): string | undefined => {
  const date = resolveValidTimestampDate(value);
  if (!date) return undefined;
  try {
    return new Intl.DateTimeFormat(locale, {
      dateStyle: 'medium',
      timeStyle: 'medium',
    }).format(date);
  } catch {
    return undefined;
  }
};

export const formatQuotaResetTimestamp = (value: number | null | undefined, _locale?: string) => {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '-';
  return formatNumericTimestamp(date);
};

export const getQuotaResetRemainingDays = (
  expiresAtMs: number | null | undefined,
  nowMs = Date.now()
): number | null => {
  if (
    typeof expiresAtMs !== 'number' ||
    !Number.isFinite(expiresAtMs) ||
    expiresAtMs <= 0 ||
    !Number.isFinite(nowMs)
  ) {
    return null;
  }
  return Math.max(0, Math.ceil((expiresAtMs - nowMs) / QUOTA_RESET_DAY_MS));
};

export const formatQuotaResetDisplay = (
  resetAtMs: number | null | undefined,
  resetLabel: string | null | undefined,
  locale?: string
) => {
  const normalizedResetAt = formatQuotaResetTimestamp(resetAtMs, locale);
  if (normalizedResetAt !== '-') return normalizedResetAt;

  const normalizedLabel = resetLabel?.trim() ?? '';
  if (!normalizedLabel || normalizedLabel === '-') return '-';
  const parsedLabel = formatQuotaResetTimestamp(parseQuotaResetLabelMs(normalizedLabel), locale);
  if (parsedLabel !== '-') return parsedLabel;
  return normalizedLabel;
};

const QUOTA_RESET_MINUTE_MS = 60 * 1000;
const QUOTA_RESET_HOUR_MS = 60 * QUOTA_RESET_MINUTE_MS;

export interface QuotaResetRelativeOptions {
  locale?: string;
  style?: 'long' | 'short';
}

const normalizeQuotaResetLocale = (locale?: string): 'zh-CN' | 'zh-TW' | 'en' | 'ru' => {
  if (!locale) return 'zh-CN';
  const lower = locale.toLowerCase();
  if (lower.startsWith('zh-tw') || lower.startsWith('zh-hk') || lower.startsWith('zh-hant')) {
    return 'zh-TW';
  }
  if (lower.startsWith('zh')) {
    return 'zh-CN';
  }
  if (lower.startsWith('en')) {
    return 'en';
  }
  if (lower.startsWith('ru')) {
    return 'ru';
  }
  return 'zh-CN';
};

const formatRelativeUnit = (
  count: number,
  unit: 'd' | 'h' | 'm',
  localeType: 'zh-CN' | 'zh-TW' | 'en' | 'ru'
): string => {
  if (localeType === 'zh-CN') {
    if (unit === 'd') return `${count} 天后`;
    if (unit === 'h') return `${count} 小时后`;
    return `${count} 分钟后`;
  }
  if (localeType === 'zh-TW') {
    if (unit === 'd') return `${count} 天後`;
    if (unit === 'h') return `${count} 小時後`;
    return `${count} 分鐘後`;
  }
  if (localeType === 'en') {
    if (unit === 'd') return `in ${count} ${count === 1 ? 'day' : 'days'}`;
    if (unit === 'h') return `in ${count} ${count === 1 ? 'hour' : 'hours'}`;
    return `in ${count} ${count === 1 ? 'minute' : 'minutes'}`;
  }
  if (unit === 'd') return `через ${count} дн.`;
  if (unit === 'h') return `через ${count} ч.`;
  return `через ${count} мин.`;
};

const formatSubMinuteRelative = (localeType: 'zh-CN' | 'zh-TW' | 'en' | 'ru'): string => {
  if (localeType === 'zh-CN') return '<1 分钟后';
  if (localeType === 'zh-TW') return '<1 分鐘後';
  if (localeType === 'en') return 'in <1 min';
  return '< 1 мин.';
};

export function formatQuotaResetRelative(
  resetAtMs: number | null | undefined,
  resetLabel?: string | null,
  nowMs?: number,
  localeOrOptions?: string | QuotaResetRelativeOptions
): string;
export function formatQuotaResetRelative(
  resetAtMs: number | null | undefined,
  resetLabel?: string | null,
  localeOrOptions?: string | QuotaResetRelativeOptions
): string;
export function formatQuotaResetRelative(
  resetAtMs: number | null | undefined,
  resetLabel?: string | null,
  nowMsOrLocale?: number | string | QuotaResetRelativeOptions,
  localeOrOptions?: string | QuotaResetRelativeOptions
): string {
  let nowMs = Date.now();
  let resolvedLocaleOrOptions = localeOrOptions;

  if (typeof nowMsOrLocale === 'number') {
    nowMs = nowMsOrLocale;
  } else if (nowMsOrLocale !== undefined) {
    resolvedLocaleOrOptions = nowMsOrLocale;
  }

  let locale = 'zh-CN';
  let style: 'long' | 'short' = 'long';
  if (typeof resolvedLocaleOrOptions === 'string') {
    locale = resolvedLocaleOrOptions;
  } else if (resolvedLocaleOrOptions) {
    if (resolvedLocaleOrOptions.locale) locale = resolvedLocaleOrOptions.locale;
    if (resolvedLocaleOrOptions.style) style = resolvedLocaleOrOptions.style;
  }

  const localeType = normalizeQuotaResetLocale(locale);

  let targetMs: number | null = null;
  if (typeof resetAtMs === 'number' && Number.isFinite(resetAtMs) && resetAtMs > 0) {
    targetMs = resetAtMs;
  } else if (resetLabel) {
    const parsed = parseQuotaResetLabelMs(resetLabel, nowMs);
    if (parsed !== null) {
      targetMs = parsed;
    }
  }

  if (targetMs === null) {
    const trimmed = resetLabel?.trim();
    if (trimmed && trimmed !== '-') {
      const match = trimmed.match(/(\d+)\s*([dhm])/i);
      if (match) {
        const count = Number.parseInt(match[1], 10);
        const unit = match[2].toLowerCase() as 'd' | 'h' | 'm';
        if (style === 'short') {
          return `${count}${unit}`;
        }
        return formatRelativeUnit(count, unit, localeType);
      }
    }
    return '';
  }

  const diffMs = targetMs - nowMs;
  if (diffMs <= 0) {
    return '';
  }

  if (diffMs >= QUOTA_RESET_DAY_MS) {
    const days = Math.floor(diffMs / QUOTA_RESET_DAY_MS);
    return style === 'short' ? `${days}d` : formatRelativeUnit(days, 'd', localeType);
  }

  if (diffMs >= QUOTA_RESET_HOUR_MS) {
    const hours = Math.floor(diffMs / QUOTA_RESET_HOUR_MS);
    return style === 'short' ? `${hours}h` : formatRelativeUnit(hours, 'h', localeType);
  }

  if (diffMs >= QUOTA_RESET_MINUTE_MS) {
    const minutes = Math.floor(diffMs / QUOTA_RESET_MINUTE_MS);
    return style === 'short' ? `${minutes}m` : formatRelativeUnit(minutes, 'm', localeType);
  }

  return style === 'short' ? '<1m' : formatSubMinuteRelative(localeType);
}

export interface QuotaRemainingPercentParts {
  prefix: string;
  percent: string;
}

export const getQuotaRemainingPercentLabel = (locale?: string): string => {
  const localeType = normalizeQuotaResetLocale(locale);
  switch (localeType) {
    case 'zh-TW':
      return '剩餘';
    case 'en':
      return 'Rem';
    case 'ru':
      return 'Ост.';
    case 'zh-CN':
    default:
      return '剩余';
  }
};

export const formatQuotaRemainingPercentParts = (
  percentText: string,
  locale?: string
): QuotaRemainingPercentParts | null => {
  if (!percentText || percentText === '-') return null;
  return {
    prefix: getQuotaRemainingPercentLabel(locale),
    percent: percentText,
  };
};

export const formatQuotaRemainingPercentDisplay = (
  percentText: string,
  locale?: string
): string => {
  const parts = formatQuotaRemainingPercentParts(percentText, locale);
  if (!parts) return '-';
  return `${parts.prefix} ${parts.percent}`;
};


export const formatQuotaResetTooltipParams = (
  params: Record<string, string | number>,
  resetAtMs: number | null | undefined,
  locale?: string,
  recoverAtMs?: number | null
) => {
  let formatted = params;
  if (Object.prototype.hasOwnProperty.call(params, 'resetAt')) {
    const resetAt = formatQuotaResetDisplay(resetAtMs, String(params.resetAt ?? ''), locale);
    if (resetAt !== params.resetAt) formatted = { ...formatted, resetAt };
  }
  if (Object.prototype.hasOwnProperty.call(params, 'recoverAt')) {
    const recoverAt = formatQuotaResetDisplay(recoverAtMs, String(params.recoverAt ?? ''), locale);
    if (recoverAt !== params.recoverAt) formatted = { ...formatted, recoverAt };
  }
  return formatted;
};

const normalizeDetailToken = (value: string | number | null | undefined) =>
  String(value ?? '')
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '');

export const translateDetailEnum = (
  t: TFunction,
  prefix: string,
  value: string | number | null | undefined
) => {
  const raw = String(value ?? '').trim();
  if (!raw) return '-';
  const token = normalizeDetailToken(raw);
  if (!token) return raw;
  return t(`${prefix}${token}`, { defaultValue: raw });
};

export const formatDurationMs = (value: number | null | undefined) => {
  if (value === null || value === undefined) return '-';
  return `${Math.round(value)} ms`;
};

export const getEventFailureReason = (event: MonitoringAnalyticsEventRow) =>
  event.fail_summary ||
  event.header_error_code ||
  event.header_error_kind ||
  event.header_trace_id ||
  '';

export const getEventStatusText = (event: MonitoringAnalyticsEventRow, t: TFunction) => {
  if (!event.failed) return t('accounts.detail_event_success');
  if (event.fail_status_code) {
    return t('accounts.detail_event_failed_with_code', {
      code: event.fail_status_code,
      defaultValue: `${t('accounts.detail_event_failed')} ${event.fail_status_code}`,
    });
  }
  return t('accounts.detail_event_failed');
};

export const quotaStatusLabelKey = (status: AccountRow['quota']['status']) => {
  switch (status) {
    case 'ok':
      return 'accounts.quota_status_ok';
    case 'low':
      return 'accounts.quota_status_low';
    case 'exhausted':
      return 'accounts.quota_status_exhausted';
    case 'error':
      return 'accounts.quota_status_error';
    case 'loading':
      return 'accounts.quota_status_loading';
    case 'disabled':
      return 'accounts.quota_status_disabled';
    case 'unknown':
    default:
      return 'accounts.quota_status_unknown';
  }
};

export type AccountQuotaLifecycleBarOverride = 'bad' | 'neutral' | null;

export const getAccountQuotaLifecycleBarOverride = (
  status: AccountRow['quota']['status']
): AccountQuotaLifecycleBarOverride => {
  switch (status) {
    case 'error':
      return 'bad';
    case 'loading':
    case 'disabled':
    case 'unknown':
      return 'neutral';
    case 'ok':
    case 'low':
    case 'exhausted':
    default:
      return null;
  }
};

const selectXaiQuotaListFallbackWindows = (
  windows: AccountQuotaDisplayWindow[]
): AccountQuotaDisplayWindow[] => {
  const billing =
    windows.find((window) => window.source === 'xai' && window.key === 'billing') ??
    windows.find(
      (window) =>
        window.source === 'xai' && window.key === 'credits-period' && window.kind === 'billing'
    );
  const payg = windows.find((window) => window.source === 'xai' && window.key === 'pay-as-you-go');

  return [billing, payg].filter((window): window is AccountQuotaDisplayWindow => Boolean(window));
};

const isCodexQuotaListCandidate = (window: AccountQuotaDisplayWindow): boolean =>
  !isModelScopedAccountQuotaWindow(window) &&
  (window.kind === 'five_hour' || window.kind === 'weekly' || window.kind === 'monthly');

const selectCodexQuotaListWindows = (
  quotaWindows: AccountQuotaDisplayWindow[]
): AccountQuotaDisplayWindow[] => {
  return quotaWindows.filter(isCodexQuotaListCandidate);
};

const selectKimiQuotaListWindows = (
  quotaWindows: AccountQuotaDisplayWindow[]
): AccountQuotaDisplayWindow[] => {
  const topLevelWindows = quotaWindows.filter(
    (window) => !window.key.startsWith('usage-')
  );

  const limits = topLevelWindows.filter(
    (window) =>
      window.key !== 'summary' &&
      !isModelScopedAccountQuotaWindow(window) &&
      (isStandardAccountQuotaListWindow(window) ||
        window.kind === 'five_hour' ||
        window.kind === 'daily' ||
        window.kind === 'weekly')
  );

  const summary = topLevelWindows.find(
    (window) => window.key === 'summary' && !isModelScopedAccountQuotaWindow(window)
  );

  if (summary) {
    return [...limits, summary];
  }
  return limits;
};

export const resolveWindowDurationSeconds = (
  window: AccountQuotaDisplayWindow
): number => {
  if (
    typeof window.limitWindowSeconds === 'number' &&
    Number.isFinite(window.limitWindowSeconds) &&
    window.limitWindowSeconds > 0
  ) {
    return window.limitWindowSeconds;
  }
  if (window.kind === 'five_hour') return 5 * 3600;
  if (window.kind === 'daily') return 24 * 3600;
  if (window.kind === 'weekly') return 7 * 86400;
  if (window.kind === 'monthly' || window.kind === 'billing') return 30 * 86400;
  return Number.MAX_SAFE_INTEGER;
};

const getAntigravityMatrixGroupDisplayLabel = (label: string) => {
  const normalized = label.toLowerCase();
  if (normalized.includes('claude') || normalized.includes('gpt')) return 'Claude';
  if (normalized.includes('gemini')) return 'Gemini';
  return label;
};

const resolveWeeklyQuotaLabel = (window: AccountQuotaDisplayWindow, t?: TFunction): string => {
  const rawLabel = window.label?.trim();
  if (rawLabel && rawLabel !== 'Quota window') {
    const lower = rawLabel.toLowerCase();
    const isEnglishWeekly =
      lower.includes('weekly') ||
      lower.includes('7 day') ||
      lower.includes('7-day') ||
      lower.includes('7d');
    if (!isEnglishWeekly) {
      return rawLabel;
    }
  }
  if (t) {
    const localized = t('accounts.detail_snapshot_window_weekly');
    if (localized && !localized.toLowerCase().includes('weekly')) {
      return localized;
    }
  }
  return 'Weekly';
};

const resolveMonthlyQuotaLabel = (window: AccountQuotaDisplayWindow, t?: TFunction): string => {
  const rawLabel = window.label?.trim();
  if (rawLabel && rawLabel !== 'Quota window') {
    const lower = rawLabel.toLowerCase();
    const isEnglishMonthly =
      lower.includes('monthly') ||
      lower.includes('30 day') ||
      lower.includes('30-day') ||
      lower.includes('30d');
    if (!isEnglishMonthly) {
      return rawLabel;
    }
  }
  if (t) {
    const localized = t('accounts.detail_snapshot_window_monthly');
    if (localized && !localized.toLowerCase().includes('monthly')) {
      return localized;
    }
  }
  return 'Monthly';
};

export const getQuotaWindowReadableLabel = (
  window: AccountQuotaDisplayWindow,
  t?: TFunction
): string => {
  let baseLabel: string;
  switch (window.kind) {
    case 'five_hour':
      baseLabel = '5h';
      break;
    case 'daily':
      baseLabel = '24h';
      break;
    case 'weekly':
      baseLabel = resolveWeeklyQuotaLabel(window, t);
      break;
    case 'monthly':
      baseLabel = resolveMonthlyQuotaLabel(window, t);
      break;
    case 'billing': {
      const rawLabel = window.label?.trim();
      if (rawLabel && rawLabel !== 'Quota window') {
        baseLabel = rawLabel;
      } else if (t) {
        baseLabel = t('accounts.detail_snapshot_window_monthly');
      } else {
        baseLabel = 'Billing';
      }
      break;
    }
    case 'payg': {
      const rawLabel = window.label?.trim();
      if (rawLabel && rawLabel !== 'Quota window') {
        baseLabel = rawLabel;
      } else {
        baseLabel = 'Pay-As-You-Go';
      }
      break;
    }
    case 'product':
      baseLabel = window.label?.trim() || 'Product';
      break;
    case 'summary':
      baseLabel = t ? t('accounts.col_quota') : (window.label?.trim() || 'Summary');
      break;
    default: {
      const label = window.label?.trim() ?? '';
      if (!label || label === 'Quota window') {
        baseLabel = t ? t('accounts.col_quota') : 'Quota';
      } else {
        const lower = label.toLowerCase();
        if (
          lower.includes('5 hour') ||
          lower.includes('5h') ||
          lower.includes('5-hour') ||
          lower.includes('five hour')
        ) {
          baseLabel = '5h';
        } else if (lower.includes('24 hour') || lower.includes('24h') || lower.includes('daily')) {
          baseLabel = '24h';
        } else if (
          lower.includes('weekly') ||
          lower.includes('7 day') ||
          lower.includes('7-day') ||
          lower.includes('7d')
        ) {
          baseLabel = resolveWeeklyQuotaLabel(window, t);
        } else if (
          lower.includes('monthly') ||
          lower.includes('30 day') ||
          lower.includes('30-day') ||
          lower.includes('30d')
        ) {
          baseLabel = resolveMonthlyQuotaLabel(window, t);
        } else {
          baseLabel = label.charAt(0).toUpperCase() + label.slice(1);
        }
      }
      break;
    }
  }

  if (window.source === 'antigravity' && window.groupLabel?.trim()) {
    const scopeLabel = getAntigravityMatrixGroupDisplayLabel(window.groupLabel.trim());
    if (scopeLabel && !baseLabel.toLowerCase().includes(scopeLabel.toLowerCase())) {
      return `${scopeLabel} ${baseLabel}`;
    }
  }

  return baseLabel;
};

export const selectAccountQuotaMainListWindows = (
  row: AccountRow,
  quotaWindows: AccountQuotaDisplayWindow[],
  maxWindows = 2
): AccountQuotaDisplayWindow[] => {
  const standardQuotaWindows = quotaWindows.filter(isStandardAccountQuotaListWindow);
  let candidates: AccountQuotaDisplayWindow[];

  switch (row.provider) {
    case 'codex':
      candidates = selectCodexQuotaListWindows(quotaWindows);
      break;
    case 'kimi':
      candidates = selectKimiQuotaListWindows(quotaWindows);
      break;
    case 'xai':
      candidates =
        standardQuotaWindows.length > 0
          ? standardQuotaWindows
          : selectXaiQuotaListFallbackWindows(quotaWindows);
      break;
    case 'antigravity':
      candidates =
        standardQuotaWindows.length > 0
          ? standardQuotaWindows
          : quotaWindows.filter(
              (window) =>
                window.windowMode !== 'non_window' &&
                window.kind !== 'summary' &&
                !isModelScopedAccountQuotaWindow(window)
            );
      if (candidates.length === 0) {
        candidates = quotaWindows.filter((window) => window.windowMode !== 'non_window');
      }
      break;
    case 'claude':
    default:
      candidates = standardQuotaWindows;
      break;
  }

  const indexed = candidates.map((w, index) => ({
    w,
    index,
    duration: resolveWindowDurationSeconds(w),
  }));

  indexed.sort((a, b) => {
    if (a.duration !== b.duration) {
      return a.duration - b.duration;
    }
    if (row.provider === 'antigravity') {
      const groupRankDiff =
        getAntigravityGroupRank(a.w.groupLabel ?? '') -
        getAntigravityGroupRank(b.w.groupLabel ?? '');
      if (groupRankDiff !== 0) {
        return groupRankDiff;
      }
    }
    if (a.index !== b.index) {
      return a.index - b.index;
    }
    return a.w.key.localeCompare(b.w.key);
  });

  return indexed.slice(0, maxWindows).map((item) => item.w);
};

export const selectAccountQuotaListWindows = (
  row: AccountRow,
  quotaWindows: AccountQuotaDisplayWindow[],
  standardQuotaWindows: AccountQuotaDisplayWindow[]
): AccountQuotaDisplayWindow[] => {
  switch (row.provider) {
    case 'codex':
      return selectCodexQuotaListWindows(quotaWindows);
    case 'kimi':
      return selectKimiQuotaListWindows(quotaWindows);
    case 'xai':
      return standardQuotaWindows.length > 0
        ? standardQuotaWindows
        : selectXaiQuotaListFallbackWindows(quotaWindows);
    case 'antigravity':
      return standardQuotaWindows.length > 0
        ? standardQuotaWindows
        : quotaWindows.slice(0, 2);
    case 'claude':
      return standardQuotaWindows;
    default:
      return standardQuotaWindows;
  }
};

const getAntigravityGroupRank = (label: string) => {
  const normalized = label.toLowerCase();
  if (normalized.includes('claude') || normalized.includes('gpt')) return 0;
  if (normalized.includes('gemini')) return 1;
  return 2;
};


export const getAccountQuotaFallbackVisibleScopeLabel = (
  row: AccountRow,
  window: AccountQuotaDisplayWindow
): string | null => {
  if (row.provider !== ANTIGRAVITY_CONFIG.type || window.source !== 'antigravity') return null;
  const groupLabel = window.groupLabel?.trim();
  return groupLabel ? getAntigravityMatrixGroupDisplayLabel(groupLabel) : null;
};

export const buildAntigravityQuotaMatrix = (
  row: AccountRow,
  windows: AccountQuotaDisplayWindow[]
): AntigravityQuotaMatrix | null => {
  if (row.provider !== ANTIGRAVITY_CONFIG.type) return null;

  const matrixWindows = windows.filter(
    (window) =>
      window.source === 'antigravity' &&
      Boolean(window.groupLabel) &&
      (window.kind === 'five_hour' || window.kind === 'weekly')
  );
  const groupOrder = new Map<string, number>();
  matrixWindows.forEach((window) => {
    const groupLabel = window.groupLabel ?? '';
    if (groupLabel && !groupOrder.has(groupLabel)) {
      groupOrder.set(groupLabel, groupOrder.size);
    }
  });

  const selectedGroupLabels = [...groupOrder.keys()]
    .sort((first, second) => {
      const rankDiff = getAntigravityGroupRank(first) - getAntigravityGroupRank(second);
      if (rankDiff !== 0) return rankDiff;
      return (groupOrder.get(first) ?? 0) - (groupOrder.get(second) ?? 0);
    })
    .slice(0, 2);
  if (selectedGroupLabels.length < 2) return null;

  const windowsByKindAndGroup = new Map<string, AccountQuotaDisplayWindow>();
  matrixWindows.forEach((window) => {
    if (!window.kind || !window.groupLabel) return;
    windowsByKindAndGroup.set(`${window.kind}\u0000${window.groupLabel}`, window);
  });

  const windowKeys = new Set<string>();
  const rows: AntigravityQuotaMatrixRow[] = [];
  for (const kind of ['five_hour', 'weekly'] satisfies AntigravityQuotaMatrixWindowKind[]) {
    const cells = selectedGroupLabels.map((groupLabel) => {
      const window = windowsByKindAndGroup.get(`${kind}\u0000${groupLabel}`);
      return window
        ? {
            groupLabel,
            displayLabel: getAntigravityMatrixGroupDisplayLabel(groupLabel),
            window,
          }
        : null;
    });
    if (cells.some((cell) => cell === null)) continue;
    cells.forEach((cell) => {
      if (cell) windowKeys.add(cell.window.key);
    });
    rows.push({
      key: kind,
      label: getQuotaWindowShortLabel(cells[0]!.window),
      cells: cells as AntigravityQuotaMatrixCell[],
    });
  }

  if (rows.length === 0) return null;
  return { rows, windowKeys };
};

export const toAuthFileCodexInspectionSnapshot = (
  row: AccountRow
): AuthFileCodexInspectionSnapshot | undefined => {
  if (!row.inspection) return undefined;
  const identity = getAuthFilePatchTarget(row.raw);
  return {
    fileName: row.fileName,
    runtimeId: identity.runtimeId,
    provider: identity.provider,
    authIndex: row.authIndex || null,
    accountId: identity.accountId,
    accountSnapshot: identity.accountSnapshot,
    statusCode: row.inspection.statusCode,
    action: getEffectiveAccountInspectionAction(row.inspection),
    actionStatus: row.inspection.actionStatus,
    executedAction: row.inspection.executedAction,
    usedPercent: row.inspection.usedPercent,
    isQuota:
      row.inspection.isQuota ??
      (row.inspection.usedPercent !== null ||
      getEffectiveAccountInspectionAction(row.inspection) === 'disable'
        ? true
        : null),
    inspectionAtMs: row.inspection.createdAtMs,
  };
};
