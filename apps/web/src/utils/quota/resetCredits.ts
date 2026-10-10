import type { CodexRateLimitResetCredit, CodexResetCreditsSummary } from '@/types';
import { normalizeNumberValue, normalizeStringValue } from './parsers';

const asRecord = (value: unknown): Record<string, unknown> | null =>
  value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;

const normalizeCredit = (value: unknown): CodexRateLimitResetCredit | null => {
  const record = asRecord(value);
  if (!record) return null;
  if (normalizeStringValue(record.reset_type ?? record.resetType) !== 'codex_rate_limits') {
    return null;
  }
  if (normalizeStringValue(record.status) !== 'available') {
    return null;
  }

  const expiresAt = normalizeStringValue(record.expires_at ?? record.expiresAt);
  if (!expiresAt) return null;

  return {
    id: normalizeStringValue(record.id) ?? '',
    status: normalizeStringValue(record.status) ?? '',
    grantedAt: normalizeStringValue(record.granted_at ?? record.grantedAt) ?? '',
    expiresAt,
  };
};

export const normalizeCodexResetCreditsPayload = (
  payload: unknown
): CodexResetCreditsSummary => {
  let parsedPayload = payload;
  if (typeof payload === 'string') {
    const trimmed = payload.trim();
    if (!trimmed) {
      return { availableCount: null, credits: [], creditsObserved: false, invalidPayload: true };
    }
    try {
      parsedPayload = JSON.parse(trimmed);
    } catch {
      return { availableCount: null, credits: [], creditsObserved: false, invalidPayload: true };
    }
  }

  const record = asRecord(parsedPayload);
  if (!record) {
    return { availableCount: null, credits: [], creditsObserved: false, invalidPayload: true };
  }

  const availableCount = normalizeNumberValue(record.available_count ?? record.availableCount);
  const rawCredits = Array.isArray(record.credits) ? record.credits : null;
  const creditsObserved = rawCredits !== null;
  const credits = rawCredits
    ? rawCredits.map(normalizeCredit).filter((credit): credit is CodexRateLimitResetCredit =>
        Boolean(credit)
      )
    : [];

  const hasCountObservation = availableCount !== null;
  const hasDetailObservation = creditsObserved;
  const invalidPayload = !hasCountObservation && !hasDetailObservation;

  return {
    availableCount,
    credits,
    creditsObserved,
    invalidPayload,
  };
};

export const RESET_CREDITS_DETAIL_TTL_MS = 5 * 60 * 1000;

export const resolveCodexResetCreditsCountEvidenceAtMs = (
  quota: Partial<import('@/types').CodexQuotaState> | null | undefined
): number | null => {
  if (!quota) return null;
  if (
    typeof quota.resetCreditsCountEvidenceAtMs === 'number' &&
    Number.isFinite(quota.resetCreditsCountEvidenceAtMs) &&
    quota.resetCreditsCountEvidenceAtMs > 0
  ) {
    return quota.resetCreditsCountEvidenceAtMs;
  }
  const hasCount =
    typeof quota.rateLimitResetCreditsAvailableCount === 'number' &&
    Number.isFinite(quota.rateLimitResetCreditsAvailableCount);
  if (hasCount) {
    if (
      typeof quota.resetCreditsEvidenceAtMs === 'number' &&
      Number.isFinite(quota.resetCreditsEvidenceAtMs) &&
      quota.resetCreditsEvidenceAtMs > 0
    ) {
      return quota.resetCreditsEvidenceAtMs;
    }
    const fallback = quota.fetchedAtMs ?? quota.observedAtMs;
    return typeof fallback === 'number' && Number.isFinite(fallback) && fallback > 0
      ? fallback
      : null;
  }
  return null;
};

export const resolveCodexResetCreditsObservationCount = (
  availableCount: number | null | undefined,
  credits?: readonly unknown[] | null,
  creditsObserved?: boolean
): number | null => {
  if (typeof availableCount === 'number' && Number.isFinite(availableCount)) {
    return availableCount;
  }
  const isCreditsObserved = creditsObserved !== undefined ? creditsObserved : Array.isArray(credits);
  if (isCreditsObserved && Array.isArray(credits)) {
    return credits.length;
  }
  return null;
};

export const resolveCodexResetCreditsDetailEvidenceAtMs = (
  quota: Partial<import('@/types').CodexQuotaState> | null | undefined
): number | null => {
  if (!quota) return null;
  if (
    typeof quota.resetCreditsDetailEvidenceAtMs === 'number' &&
    Number.isFinite(quota.resetCreditsDetailEvidenceAtMs) &&
    quota.resetCreditsDetailEvidenceAtMs > 0
  ) {
    return quota.resetCreditsDetailEvidenceAtMs;
  }
  const hasCredits =
    Array.isArray(quota.rateLimitResetCredits) && quota.rateLimitResetCredits.length > 0;
  if (hasCredits) {
    if (
      typeof quota.resetCreditsEvidenceAtMs === 'number' &&
      Number.isFinite(quota.resetCreditsEvidenceAtMs) &&
      quota.resetCreditsEvidenceAtMs > 0
    ) {
      return quota.resetCreditsEvidenceAtMs;
    }
    const fallback = quota.fetchedAtMs ?? quota.observedAtMs;
    return typeof fallback === 'number' && Number.isFinite(fallback) && fallback > 0
      ? fallback
      : null;
  }
  return null;
};

export interface CodexResetCreditsMergeInput {
  rateLimitResetCreditsAvailableCount?: number | null;
  rateLimitResetCredits?: CodexRateLimitResetCredit[];
  rateLimitResetCreditsError?: string | null;
  resetCreditsEvidenceAtMs?: number | null;
  resetCreditsCountEvidenceAtMs?: number | null;
  resetCreditsDetailEvidenceAtMs?: number | null;
  resetCreditsDetailStale?: boolean;
  observedAtMs?: number;
}

export interface CodexResetCreditsMergeResult {
  rateLimitResetCreditsAvailableCount: number | null;
  rateLimitResetCredits: CodexRateLimitResetCredit[];
  rateLimitResetCreditsError: string | null;
  resetCreditsEvidenceAtMs: number | null;
  resetCreditsCountEvidenceAtMs: number | null;
  resetCreditsDetailEvidenceAtMs: number | null;
  resetCreditsDetailStale: boolean;
}

export const mergeCodexResetCreditsEvidence = (
  previousState: Partial<import('@/types').CodexQuotaState> | undefined,
  incoming: CodexResetCreditsMergeInput,
  options?: { isFullDetailObservation?: boolean }
): CodexResetCreditsMergeResult => {
  const incomingDetailEvidence =
    incoming.resetCreditsDetailEvidenceAtMs !== undefined
      ? incoming.resetCreditsDetailEvidenceAtMs
      : (incoming.resetCreditsEvidenceAtMs ?? null);
  const isFullDetail =
    options?.isFullDetailObservation ??
    (!incoming.rateLimitResetCreditsError && incomingDetailEvidence != null);

  if (isFullDetail) {
    const observedAt =
      incomingDetailEvidence ?? incoming.observedAtMs ?? Date.now();
    const count =
      resolveCodexResetCreditsObservationCount(
        incoming.rateLimitResetCreditsAvailableCount,
        incoming.rateLimitResetCredits
      ) ?? (previousState?.rateLimitResetCreditsAvailableCount ?? null);
    const countEvidence =
      incoming.resetCreditsCountEvidenceAtMs ?? incoming.resetCreditsEvidenceAtMs ?? observedAt;
    const effectiveCredits =
      count === 0
        ? []
        : (incoming.rateLimitResetCredits ?? []);
    return {
      rateLimitResetCreditsAvailableCount: count,
      rateLimitResetCredits: effectiveCredits,
      rateLimitResetCreditsError: null,
      resetCreditsEvidenceAtMs: observedAt,
      resetCreditsCountEvidenceAtMs: countEvidence,
      resetCreditsDetailEvidenceAtMs: observedAt,
      resetCreditsDetailStale: false,
    };
  }

  // Not a full detail observation (summary refresh, or full refresh where reset credits endpoint failed)
  const incomingCount = incoming.rateLimitResetCreditsAvailableCount;
  const detailError = incoming.rateLimitResetCreditsError ?? null;
  const oldCount = previousState?.rateLimitResetCreditsAvailableCount ?? null;
  const oldCredits = previousState?.rateLimitResetCredits ?? [];
  const oldDetailEvidence = resolveCodexResetCreditsDetailEvidenceAtMs(previousState);
  const oldDetailStale = previousState?.resetCreditsDetailStale ?? false;
  const oldCountEvidence = resolveCodexResetCreditsCountEvidenceAtMs(previousState);

  const hasNewCountObservation =
    incomingCount !== undefined &&
    incomingCount !== null &&
    (typeof incoming.resetCreditsCountEvidenceAtMs === 'number' ||
      typeof incoming.observedAtMs === 'number');
  const incomingCountEvidence =
    incoming.resetCreditsCountEvidenceAtMs ?? incoming.observedAtMs ?? Date.now();

  // 1. New count was not observed in this request
  if (!hasNewCountObservation && incomingCount === null) {
    return {
      rateLimitResetCreditsAvailableCount: oldCount,
      rateLimitResetCredits: oldCredits,
      rateLimitResetCreditsError: detailError,
      resetCreditsEvidenceAtMs: previousState?.resetCreditsEvidenceAtMs ?? null,
      resetCreditsCountEvidenceAtMs: oldCountEvidence,
      resetCreditsDetailEvidenceAtMs: oldDetailEvidence,
      resetCreditsDetailStale: oldDetailStale,
    };
  }

  // 2. New count = 0: definitive evidence that there are no credits available
  if (incomingCount === 0) {
    return {
      rateLimitResetCreditsAvailableCount: 0,
      rateLimitResetCredits: [],
      rateLimitResetCreditsError: detailError,
      resetCreditsEvidenceAtMs: incomingCountEvidence,
      resetCreditsCountEvidenceAtMs: incomingCountEvidence,
      resetCreditsDetailEvidenceAtMs: null,
      resetCreditsDetailStale: false,
    };
  }

  // 3. New count equals old count (and count > 0)
  if (oldCount !== null && incomingCount === oldCount) {
    return {
      rateLimitResetCreditsAvailableCount: incomingCount,
      rateLimitResetCredits: oldCredits,
      rateLimitResetCreditsError: detailError,
      resetCreditsEvidenceAtMs:
        previousState?.resetCreditsEvidenceAtMs ?? oldDetailEvidence ?? incomingCountEvidence,
      resetCreditsCountEvidenceAtMs: incomingCountEvidence,
      resetCreditsDetailEvidenceAtMs: oldDetailEvidence,
      resetCreditsDetailStale: oldDetailStale,
    };
  }

  // 4. New count changed and count > 0
  return {
    rateLimitResetCreditsAvailableCount: incomingCount ?? null,
    rateLimitResetCredits: [],
    rateLimitResetCreditsError: detailError,
    resetCreditsEvidenceAtMs:
      previousState?.resetCreditsEvidenceAtMs ?? incomingCountEvidence,
    resetCreditsCountEvidenceAtMs: incomingCountEvidence,
    resetCreditsDetailEvidenceAtMs: oldDetailEvidence,
    resetCreditsDetailStale: true,
  };
};

export const shouldAutoFetchCodexResetCreditDetails = (
  quota: Partial<import('@/types').CodexQuotaState> | null | undefined,
  nowMs = Date.now()
): boolean => {
  if (!quota) return false;
  const count = quota.rateLimitResetCreditsAvailableCount;
  if (typeof count !== 'number' || !Number.isFinite(count) || count <= 0) {
    return false;
  }
  if (quota.resetCreditsDetailStale === true) {
    return true;
  }
  const detailEvidenceAtMs = resolveCodexResetCreditsDetailEvidenceAtMs(quota);
  if (detailEvidenceAtMs === null || detailEvidenceAtMs <= 0) {
    return true;
  }
  return nowMs - detailEvidenceAtMs > RESET_CREDITS_DETAIL_TTL_MS;
};

export const buildCodexResetCreditAutoFetchSignature = (
  selectionKey: string,
  quota: Partial<import('@/types').CodexQuotaState> | null | undefined
): string => {
  const count = quota?.rateLimitResetCreditsAvailableCount ?? 'unknown';
  const countEvidence = resolveCodexResetCreditsCountEvidenceAtMs(quota) ?? 'none';
  const detailEvidence = resolveCodexResetCreditsDetailEvidenceAtMs(quota) ?? 'none';
  const stale = quota?.resetCreditsDetailStale ? 'stale' : 'fresh';
  return `${selectionKey}:${count}:${countEvidence}:${detailEvidence}:${stale}`;
};
