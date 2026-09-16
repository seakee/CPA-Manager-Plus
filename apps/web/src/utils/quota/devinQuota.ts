/**
 * Pure parsers and normalizers for Devin Quota responses.
 */

import type { DevinQuotaData, DevinQuotaWindow } from '@/types';

export const normalizeQuotaPercent = (value: unknown): number | null => {
  if (value === null || value === undefined) return null;
  if (typeof value === 'boolean') return null;

  let num: number;
  if (typeof value === 'number') {
    num = value;
  } else if (typeof value === 'string') {
    const trimmed = value.trim();
    if (!trimmed) return null;
    if (!/^-?\d+(\.\d+)?$/.test(trimmed)) return null;
    num = Number(trimmed);
  } else {
    return null;
  }

  if (!Number.isFinite(num)) return null;
  if (num < 0 || num > 100) return null;
  return num;
};

export const normalizeUnixSecondsToMs = (value: unknown): number | null => {
  if (typeof value !== 'number' && typeof value !== 'string') return null;
  if (typeof value === 'string' && !/^\d+$/.test(value)) return null;

  const seconds = Number(value);
  const ms = seconds * 1000;

  if (
    Number.isSafeInteger(seconds) &&
    seconds > 0 &&
    Number.isFinite(new Date(ms).getTime())
  ) {
    return ms;
  }
  return null;
};

export const normalizeIsoTimestampMs = (value: unknown): number | null => {
  if (typeof value !== 'string') return null;
  if (!/^\d{4}-\d{2}-\d{2}T/.test(value)) return null;

  const ms = Date.parse(value);
  return Number.isFinite(ms) && ms > 0 ? ms : null;
};

export const normalizePlanName = (value: unknown): string | null => {
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  return trimmed.length > 0 ? trimmed : null;
};

export interface ParseDevinQuotaOptions {
  observedAtMs?: number;
}

export const parseDevinQuotaPayload = (
  rawPayload: unknown,
  options: ParseDevinQuotaOptions = {}
): DevinQuotaData | null => {
  let body = rawPayload;
  if (typeof body === 'string') {
    try {
      body = JSON.parse(body);
    } catch {
      return null;
    }
  }

  if (!body || typeof body !== 'object') {
    return null;
  }

  const root = body as Record<string, unknown>;
  const userStatus =
    root.userStatus && typeof root.userStatus === 'object'
      ? (root.userStatus as Record<string, unknown>)
      : null;
  const planStatus =
    userStatus?.planStatus && typeof userStatus.planStatus === 'object'
      ? (userStatus.planStatus as Record<string, unknown>)
      : null;

  if (!planStatus) {
    return null;
  }

  const planInfo =
    planStatus.planInfo && typeof planStatus.planInfo === 'object'
      ? (planStatus.planInfo as Record<string, unknown>)
      : null;

  const plan = normalizePlanName(planInfo?.planName);
  const planStartMs = normalizeIsoTimestampMs(planStatus.planStart);
  const planEndMs = normalizeIsoTimestampMs(planStatus.planEnd);

  const dailyRemaining = normalizeQuotaPercent(planStatus.dailyQuotaRemainingPercent);
  const weeklyRemaining = normalizeQuotaPercent(planStatus.weeklyQuotaRemainingPercent);

  const dailyReset = normalizeUnixSecondsToMs(planStatus.dailyQuotaResetAtUnix);
  const weeklyReset = normalizeUnixSecondsToMs(planStatus.weeklyQuotaResetAtUnix);

  const hasObservation =
    dailyRemaining !== null ||
    dailyReset !== null ||
    weeklyRemaining !== null ||
    weeklyReset !== null;

  if (!hasObservation) {
    return null;
  }

  const dailyWindow: DevinQuotaWindow = {
    id: 'daily',
    remainingPercent: dailyRemaining,
    resetAtMs: dailyReset,
    periodHours: 24,
  };

  const weeklyWindow: DevinQuotaWindow = {
    id: 'weekly',
    remainingPercent: weeklyRemaining,
    resetAtMs: weeklyReset,
    periodHours: 168,
  };

  return {
    windows: [dailyWindow, weeklyWindow],
    observedAtMs: options.observedAtMs ?? Date.now(),
    plan,
    planStartMs,
    planEndMs,
  };
};
