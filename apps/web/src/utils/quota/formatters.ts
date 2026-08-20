/**
 * Formatting functions for quota display.
 */

import type { TFunction } from 'i18next';
import type { CodexUsageWindow, QuotaResetAccuracy } from '@/types';
import { parseTimestampMs } from '@/utils/timestamp';
import { normalizeNumberValue } from './parsers';

export interface QuotaResetResolution {
  resetAtMs: number | null;
  resetAccuracy: QuotaResetAccuracy;
}

export interface QuotaResetTimeFormatOptions {
  locale?: string;
  timeZone?: string;
}

export type CodexQuotaResetSource = 'provider_api' | 'response_header';

const LEGACY_RESET_ROLLOVER_MS = 30 * 24 * 60 * 60 * 1000;

const resolveLegacyResetDateOrder = (locale?: string): 'month-day' | 'day-month' => {
  try {
    const parts = new Intl.DateTimeFormat(locale, {
      month: '2-digit',
      day: '2-digit',
    }).formatToParts(new Date(2000, 10, 22, 12, 0, 0, 0));
    const monthIndex = parts.findIndex((part) => part.type === 'month');
    const dayIndex = parts.findIndex((part) => part.type === 'day');
    if (monthIndex >= 0 && dayIndex >= 0) {
      return monthIndex < dayIndex ? 'month-day' : 'day-month';
    }
  } catch {
    // Legacy labels were formatted with the runtime locale; preserve the old MM/DD fallback.
  }
  return 'month-day';
};

export const isValidQuotaResetAtMs = (value: unknown): value is number =>
  typeof value === 'number' &&
  Number.isFinite(value) &&
  value > 0 &&
  !Number.isNaN(new Date(value).getTime());

const parseAbsoluteQuotaResetMs = (value: unknown): number | null => {
  const numeric = normalizeNumberValue(value);
  if (numeric !== null) {
    if (numeric <= 0) return null;
    const resetAtMs = numeric < 1e12 ? numeric * 1000 : numeric;
    return isValidQuotaResetAtMs(resetAtMs) ? resetAtMs : null;
  }

  const parsed = parseTimestampMs(value);
  return isValidQuotaResetAtMs(parsed) ? parsed : null;
};

export function parseQuotaResetLabelMs(
  value: string,
  nowMs = Date.now(),
  locale?: string
): number | null {
  const trimmed = value.trim();
  if (!trimmed || trimmed === '-') return null;

  if (/^\d+(?:\.\d+)?$/.test(trimmed)) {
    const numeric = Number(trimmed);
    if (!Number.isFinite(numeric) || numeric <= 0) return null;
    const resetAtMs = numeric < 1e12 ? numeric * 1000 : numeric;
    return isValidQuotaResetAtMs(resetAtMs) ? resetAtMs : null;
  }

  const compactMatch = trimmed.match(
    /^(\d{1,2})[./-](\d{1,2})(?:\.)?,?\s+(\d{1,2}):(\d{2})(?:\s*([ap]m))?\b/i
  );
  if (compactMatch) {
    const [, firstDateValue, secondDateValue, rawHourValue, minuteValue, meridiemValue] =
      compactMatch;
    const firstDatePart = Number(firstDateValue);
    const secondDatePart = Number(secondDateValue);
    const dateOrder = resolveLegacyResetDateOrder(locale);
    const usesDayMonth = firstDatePart > 12 || (secondDatePart <= 12 && dateOrder === 'day-month');
    const month = usesDayMonth ? secondDatePart : firstDatePart;
    const day = usesDayMonth ? firstDatePart : secondDatePart;
    const minute = Number(minuteValue);
    let hour = Number(rawHourValue);
    const meridiem = meridiemValue?.toLowerCase();
    if (
      month < 1 ||
      month > 12 ||
      day < 1 ||
      day > 31 ||
      minute < 0 ||
      minute > 59 ||
      (meridiem ? hour < 1 || hour > 12 : hour < 0 || hour > 23)
    ) {
      return null;
    }
    if (meridiem) {
      hour = (hour % 12) + (meridiem === 'pm' ? 12 : 0);
    }

    const referenceMs = Number.isFinite(nowMs) ? nowMs : Date.now();
    const reference = new Date(referenceMs);
    const candidate = new Date(reference.getFullYear(), month - 1, day, hour, minute, 0, 0);
    if (
      Number.isNaN(candidate.getTime()) ||
      candidate.getMonth() !== month - 1 ||
      candidate.getDate() !== day ||
      candidate.getHours() !== hour ||
      candidate.getMinutes() !== minute
    ) {
      return null;
    }
    if (candidate.getTime() < referenceMs - LEGACY_RESET_ROLLOVER_MS) {
      candidate.setFullYear(candidate.getFullYear() + 1);
    }
    return candidate.getTime();
  }

  if (/\b\d{4}\b/.test(trimmed)) {
    const parsed = parseTimestampMs(trimmed);
    return isValidQuotaResetAtMs(parsed) ? parsed : null;
  }

  return null;
}

export function resolveAbsoluteQuotaReset(value: unknown): QuotaResetResolution {
  const resetAtMs = parseAbsoluteQuotaResetMs(value);
  if (resetAtMs === null) {
    return { resetAtMs: null, resetAccuracy: 'unknown' };
  }
  return { resetAtMs, resetAccuracy: 'exact' };
}

export function resolveRelativeQuotaReset(
  value: unknown,
  observedAtMs: number
): QuotaResetResolution {
  const resetAfterSeconds = normalizeNumberValue(value);
  if (
    resetAfterSeconds === null ||
    resetAfterSeconds <= 0 ||
    !isValidQuotaResetAtMs(observedAtMs)
  ) {
    return { resetAtMs: null, resetAccuracy: 'unknown' };
  }
  const resetAtMs = observedAtMs + resetAfterSeconds * 1000;
  if (!isValidQuotaResetAtMs(resetAtMs)) {
    return { resetAtMs: null, resetAccuracy: 'unknown' };
  }
  return {
    resetAtMs,
    resetAccuracy: 'estimated',
  };
}

export function resolveCodexQuotaReset(
  window?: CodexUsageWindow | null,
  observedAtMs = Date.now(),
  source: CodexQuotaResetSource = 'provider_api'
): QuotaResetResolution {
  if (!window) return { resetAtMs: null, resetAccuracy: 'unknown' };
  const resetAfterSeconds = normalizeNumberValue(
    window.reset_after_seconds ?? window.resetAfterSeconds
  );
  const absoluteReset = resolveAbsoluteQuotaReset(window.reset_at ?? window.resetAt);
  if (absoluteReset.resetAtMs !== null) {
    return source === 'response_header' && resetAfterSeconds !== null && resetAfterSeconds > 0
      ? { resetAtMs: absoluteReset.resetAtMs, resetAccuracy: 'estimated' }
      : absoluteReset;
  }
  return resolveRelativeQuotaReset(resetAfterSeconds, observedAtMs);
}

export function formatQuotaResetTime(
  value?: string | number | null,
  options: QuotaResetTimeFormatOptions = {}
): string {
  const resetAtMs = parseAbsoluteQuotaResetMs(value);
  if (resetAtMs === null) return '-';
  const date = new Date(resetAtMs);
  return new Intl.DateTimeFormat(options.locale, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
    ...(options.timeZone ? { timeZone: options.timeZone } : {}),
  }).format(date);
}

export function formatUnixSeconds(value: number | null): string {
  if (!value) return '-';
  const date = new Date(value * 1000);
  if (Number.isNaN(date.getTime())) return '-';
  return date.toLocaleString(undefined, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
}

export function formatCodexResetLabel(
  window?: CodexUsageWindow | null,
  observedAtMs = Date.now(),
  source: CodexQuotaResetSource = 'provider_api'
): string {
  const reset = resolveCodexQuotaReset(window, observedAtMs, source);
  return reset.resetAtMs === null
    ? '-'
    : formatQuotaResetTime(new Date(reset.resetAtMs).toISOString());
}

export function createStatusError(message: string, status?: number): Error & { status?: number } {
  const error = new Error(message) as Error & { status?: number };
  if (status !== undefined) {
    error.status = status;
  }
  return error;
}

export function getStatusFromError(err: unknown): number | undefined {
  if (typeof err === 'object' && err !== null && 'status' in err) {
    const rawStatus = (err as { status?: unknown }).status;
    if (typeof rawStatus === 'number' && Number.isFinite(rawStatus)) {
      return rawStatus;
    }
    const asNumber = Number(rawStatus);
    if (Number.isFinite(asNumber) && asNumber > 0) {
      return asNumber;
    }
  }
  return undefined;
}

export function formatKimiResetHint(t: TFunction, hint?: string): string {
  if (!hint) return '';
  return t('kimi_quota.reset_hint', { hint });
}
