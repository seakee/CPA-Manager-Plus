/**
 * Pure parser and request handling for Meta / Muse Quota.
 */

import type { TFunction } from 'i18next';
import type {
  AuthFileItem,
  MetaQuotaData,
  MetaQuotaWindow,
  QuotaResetAccuracy,
} from '@/types';
import type { ApiCallRequest, ApiCallResult } from '@/services/api/apiCall';
import { apiCallApi } from '@/services/api/apiCall';
import type { ApiClientRequestScope } from '@/services/api/client';
import { createScopedApiRequestConfig } from '@/services/api/client';
import { authFilesApi } from '@/services/api/authFiles';
import { normalizeAuthIndex } from '@/utils/authIndex';

export const META_MUSE_QUOTA_URL = 'https://api.meta.ai/muse-code/key';

export type MetaQuotaErrorCode =
  | 'missing_auth_index'
  | 'missing_file'
  | 'missing_dca_token'
  | 'invalid_auth_file'
  | 'download_failed'
  | 'stale_request'
  | 'invalid_response'
  | 'request_failed';

export class MetaQuotaError extends Error {
  readonly status?: number;

  constructor(
    public readonly code: MetaQuotaErrorCode,
    status?: number,
    message?: string
  ) {
    super(message ?? code);
    this.name = 'MetaQuotaError';
    this.status = status;
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

export interface QuotaFetchContext {
  isCurrent?: () => boolean;
  // Manager Server connection for quota sources that CPAMP, rather than CPA,
  // has to resolve (a plugin quota provider is named by CPA at runtime).
  managerServiceBase?: string;
  managementKey?: string;
}

export interface ParseMetaQuotaOptions {
  observedAtMs?: number;
}

const parseUsedPercent = (value: unknown): number | null => {
  if (value === null || value === undefined || typeof value === 'boolean') return null;

  let num: number;
  if (typeof value === 'number') {
    num = value;
  } else if (typeof value === 'string') {
    const trimmed = value.trim();
    if (!trimmed || !/^-?\d+(\.\d+)?$/.test(trimmed)) return null;
    num = Number(trimmed);
  } else {
    return null;
  }

  if (!Number.isFinite(num)) return null;
  return Math.min(100, Math.max(0, num));
};

const parseResetAt = (
  value: unknown
): { resetAtMs: number | null; resetAccuracy: QuotaResetAccuracy } => {
  if (value === null || value === undefined || typeof value === 'boolean') {
    return { resetAtMs: null, resetAccuracy: 'unknown' };
  }

  let seconds: number;
  if (typeof value === 'number') {
    seconds = value;
  } else if (typeof value === 'string') {
    const trimmed = value.trim();
    if (!trimmed || !/^\d+(\.\d+)?$/.test(trimmed)) {
      return { resetAtMs: null, resetAccuracy: 'unknown' };
    }
    seconds = Number(trimmed);
  } else {
    return { resetAtMs: null, resetAccuracy: 'unknown' };
  }

  if (!Number.isFinite(seconds) || seconds <= 0) {
    return { resetAtMs: null, resetAccuracy: 'unknown' };
  }

  const ms = seconds * 1000;
  if (!Number.isFinite(ms) || ms <= 0) {
    return { resetAtMs: null, resetAccuracy: 'unknown' };
  }

  return { resetAtMs: ms, resetAccuracy: 'exact' };
};

const parseWindowDurationSeconds = (value: unknown): number | null => {
  if (value === null || value === undefined || typeof value === 'boolean') return null;

  let mins: number;
  if (typeof value === 'number') {
    mins = value;
  } else if (typeof value === 'string') {
    const trimmed = value.trim();
    if (!trimmed || !/^\d+(\.\d+)?$/.test(trimmed)) return null;
    mins = Number(trimmed);
  } else {
    return null;
  }

  if (!Number.isFinite(mins) || mins <= 0) return null;
  return mins * 60;
};

/**
 * Strict whitelist parser for Meta / Muse Quota payloads.
 * Only extracts subs_tier_name, is_subs_active, subs_usage.
 * Drops all upstream secrets, PII, and unknown fields.
 */
export function parseMetaQuotaPayload(
  payload: unknown,
  options: ParseMetaQuotaOptions = {}
): MetaQuotaData | null {
  let body = payload;
  if (typeof body === 'string') {
    try {
      body = JSON.parse(body);
    } catch {
      return null;
    }
  }

  if (!body || typeof body !== 'object' || Array.isArray(body)) {
    return null;
  }

  const root = body as Record<string, unknown>;
  const observedAtMs = options.observedAtMs ?? Date.now();

  const hasSubsUsageObj =
    root.subs_usage !== null &&
    typeof root.subs_usage === 'object' &&
    !Array.isArray(root.subs_usage);
  const quotaInventoryObserved = hasSubsUsageObj;
  const usage = hasSubsUsageObj ? (root.subs_usage as Record<string, unknown>) : {};

  const explicitPlan =
    typeof root.subs_tier_name === 'string' ? root.subs_tier_name.trim() : '';
  const usagePlan = typeof usage.tier === 'string' ? usage.tier.trim() : '';
  const plan = explicitPlan || usagePlan || null;

  const isSubscriptionActive =
    typeof root.is_subs_active === 'boolean' ? root.is_subs_active : null;

  const rawWindow =
    usage.window !== null && typeof usage.window === 'object' && !Array.isArray(usage.window)
      ? (usage.window as Record<string, unknown>)
      : {};
  const rawWeekly =
    usage.weekly !== null && typeof usage.weekly === 'object' && !Array.isArray(usage.weekly)
      ? (usage.weekly as Record<string, unknown>)
      : {};

  const windowUsedPercent = parseUsedPercent(rawWindow.used_percent);
  const windowReset = parseResetAt(rawWindow.resets_at);
  const windowDurationSeconds = parseWindowDurationSeconds(rawWindow.window_duration_mins);
  const windowProgressObservedAtMs = windowUsedPercent !== null ? observedAtMs : null;

  const windowWindow: MetaQuotaWindow = {
    id: 'window',
    usedPercent: windowUsedPercent,
    resetAtMs: windowReset.resetAtMs,
    resetAccuracy: windowReset.resetAccuracy,
    limitWindowSeconds: windowDurationSeconds,
    quotaProgressObservedAtMs: windowProgressObservedAtMs,
  };

  const weeklyUsedPercent = parseUsedPercent(rawWeekly.used_percent);
  const weeklyReset = parseResetAt(rawWeekly.resets_at);
  const weeklyProgressObservedAtMs = weeklyUsedPercent !== null ? observedAtMs : null;

  const weeklyWindow: MetaQuotaWindow = {
    id: 'weekly',
    usedPercent: weeklyUsedPercent,
    resetAtMs: weeklyReset.resetAtMs,
    resetAccuracy: weeklyReset.resetAccuracy,
    limitWindowSeconds: null, // Weekly never assumes 7 days duration.
    quotaProgressObservedAtMs: weeklyProgressObservedAtMs,
  };

  return {
    windows: [windowWindow, weeklyWindow],
    observedAtMs,
    plan,
    isSubscriptionActive,
    quotaInventoryObserved,
  };
}

/** Extract only the persisted root dca_token field; never fall back to api_key or $TOKEN$. */
const readDcaToken = (text: string): string => {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    throw new MetaQuotaError('invalid_auth_file');
  }

  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new MetaQuotaError('invalid_auth_file');
  }

  const raw = (value as Record<string, unknown>).dca_token;
  const token = typeof raw === 'string' ? raw.trim() : '';
  if (!/^dca:[^\s]+$/.test(token)) {
    throw new MetaQuotaError('missing_dca_token');
  }

  return token;
};

export interface MetaQuotaDependencies {
  request: (
    payload: ApiCallRequest,
    requestScope?: ApiClientRequestScope
  ) => Promise<ApiCallResult>;
  downloadText: (
    name: string,
    requestScope?: ApiClientRequestScope
  ) => Promise<string>;
  captureCurrent?: (name: string) => () => boolean;
}

/** Secrets remain request-local, never in quota state, errors, or browser storage. */
export function createMetaQuotaFetcher(deps: MetaQuotaDependencies) {
  return async (
    file: AuthFileItem,
    t?: TFunction,
    requestScope?: ApiClientRequestScope,
    context?: QuotaFetchContext
  ): Promise<MetaQuotaData> => {
    const resolveMsg = (code: MetaQuotaErrorCode): string | undefined => {
      if (!t) return undefined;
      const key =
        code === 'missing_auth_index' ? 'meta_quota.missing_identity' : `meta_quota.${code}`;
      const translated = t(key);
      return translated && translated !== key ? translated : undefined;
    };

    const authIndex = normalizeAuthIndex(file.authIndex ?? file.auth_index);
    if (!authIndex) {
      throw new MetaQuotaError('missing_auth_index', undefined, resolveMsg('missing_auth_index'));
    }

    const fileName = typeof file.name === 'string' ? file.name.trim() : '';
    const runtimeOnly =
      file.runtimeOnly === true ||
      file.runtime_only === true ||
      file.runtime_only === 'true';

    if (!fileName || runtimeOnly) {
      throw new MetaQuotaError('missing_file', undefined, resolveMsg('missing_file'));
    }

    const isCurrent = () =>
      (context?.isCurrent ? context.isCurrent() : true) &&
      (deps.captureCurrent ? deps.captureCurrent(fileName)() : true);

    const assertCurrent = () => {
      if (!isCurrent()) {
        throw new MetaQuotaError('stale_request', undefined, resolveMsg('stale_request'));
      }
    };

    assertCurrent();

    let authText: string;
    try {
      authText = await deps.downloadText(fileName, requestScope);
    } catch {
      assertCurrent();
      throw new MetaQuotaError('download_failed', undefined, resolveMsg('download_failed'));
    }

    assertCurrent();
    const dcaToken = readDcaToken(authText);

    let response: ApiCallResult;
    try {
      assertCurrent();
      response = await deps.request(
        {
          authIndex,
          method: 'POST',
          url: META_MUSE_QUOTA_URL,
          header: {
            Accept: 'application/json',
            'Content-Type': 'application/json',
            Authorization: `Bearer ${dcaToken}`,
            'x-api-version': '1.0.0',
          },
          data: '{}',
        },
        requestScope
      );
    } catch (error: unknown) {
      assertCurrent();
      const status =
        error !== null &&
        typeof error === 'object' &&
        typeof (error as { status?: unknown }).status === 'number'
          ? (error as { status: number }).status
          : undefined;
      throw new MetaQuotaError('request_failed', status, resolveMsg('request_failed'));
    }

    assertCurrent();

    if (!(response.statusCode >= 200 && response.statusCode < 300)) {
      throw new MetaQuotaError('request_failed', response.statusCode, resolveMsg('request_failed'));
    }

    const quota = parseMetaQuotaPayload(response.body ?? response.bodyText, {
      observedAtMs: Date.now(),
    });
    if (!quota) {
      throw new MetaQuotaError('invalid_response', undefined, resolveMsg('invalid_response'));
    }

    return quota;
  };
}

export const fetchMetaQuota = (
  file: AuthFileItem,
  t?: TFunction,
  requestScope?: ApiClientRequestScope,
  context?: QuotaFetchContext
): Promise<MetaQuotaData> => {
  const fetcher = createMetaQuotaFetcher({
    request: (payload, scope) =>
      apiCallApi.request(
        payload,
        scope ? createScopedApiRequestConfig(scope) : undefined
      ),
    downloadText: (name, scope) => authFilesApi.downloadText(name, scope),
  });
  return fetcher(file, t, requestScope, context);
};
