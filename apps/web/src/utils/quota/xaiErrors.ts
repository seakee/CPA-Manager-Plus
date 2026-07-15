export type XaiProbeSurface = 'oauth' | 'billing' | 'inference';

export type XaiProbeClassification =
  | 'billing_healthy'
  | 'inference_healthy'
  | 'free_quota_exhausted'
  | 'spending_limit'
  | 'auth_invalid'
  | 'entitlement_denied'
  | 'policy_denied'
  | 'permission_unknown'
  | 'quota_or_entitlement_unknown'
  | 'rate_limited'
  | 'client_outdated'
  | 'probe_invalid'
  | 'model_unavailable'
  | 'upstream_error'
  | 'protocol_changed'
  | 'unknown';

export type XaiProbeSuggestedAction = 'keep' | 'enable' | 'disable' | 'reauth' | 'delete';

export type XaiProbeConfidence = 'verified' | 'inferred' | 'unknown';

export interface XaiErrorEnvelope {
  statusCode: number | null;
  code: string;
  type: string;
  message: string;
  retryAfterSeconds: number | null;
  rawBody: unknown;
}

export interface XaiProbeDecision {
  classification: XaiProbeClassification;
  suggestedAction: XaiProbeSuggestedAction;
  reasonCode: string;
  confidence: XaiProbeConfidence;
  needsReview: boolean;
  retryAfterSeconds: number | null;
}

export interface ParseXaiErrorEnvelopeInput {
  statusCode?: number | null;
  body?: unknown;
  bodyText?: string;
  headers?: Record<string, string[] | string | undefined> | null;
}

export interface ClassifyXaiProbeInput {
  surface: XaiProbeSurface;
  envelope: XaiErrorEnvelope;
  hasPayload?: boolean;
  requestError?: string;
  disabled?: boolean;
  autoRecoverOwned?: boolean;
}

type UnknownRecord = Record<string, unknown>;

const asRecord = (value: unknown): UnknownRecord | null =>
  value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as UnknownRecord)
    : null;

const normalizeString = (value: unknown): string => {
  if (typeof value === 'string') return value.trim();
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  return '';
};

const parseMaybeJson = (value: unknown): unknown => {
  if (typeof value !== 'string') return value;
  const trimmed = value.trim();
  if (!trimmed) return '';
  try {
    return JSON.parse(trimmed) as unknown;
  } catch {
    return value;
  }
};

const normalizeStatusCode = (value: unknown): number | null => {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed > 0 ? Math.trunc(parsed) : null;
};

const collectCandidateRecords = (root: unknown): UnknownRecord[] => {
  const records: UnknownRecord[] = [];
  const seen = new Set<unknown>();
  const visit = (value: unknown, depth: number) => {
    if (depth > 4 || value === null || value === undefined || seen.has(value)) return;
    seen.add(value);
    const parsed = parseMaybeJson(value);
    const record = asRecord(parsed);
    if (!record) return;
    records.push(record);
    for (const key of ['error', 'body', 'response', 'detail', 'cause']) {
      visit(record[key], depth + 1);
    }
  };
  visit(root, 0);
  return records;
};

const firstField = (records: UnknownRecord[], keys: string[]): string => {
  for (const record of records) {
    for (const key of keys) {
      const value = normalizeString(record[key]);
      if (value) return value;
    }
  }
  return '';
};

const getHeaderValue = (headers: ParseXaiErrorEnvelopeInput['headers'], target: string): string => {
  if (!headers) return '';
  const normalizedTarget = target.toLowerCase();
  for (const [key, rawValue] of Object.entries(headers)) {
    if (key.toLowerCase() !== normalizedTarget) continue;
    if (Array.isArray(rawValue)) return rawValue.find((value) => value.trim())?.trim() ?? '';
    return normalizeString(rawValue);
  }
  return '';
};

const parseRetryAfterSeconds = (headers: ParseXaiErrorEnvelopeInput['headers']): number | null => {
  const rawValue = getHeaderValue(headers, 'retry-after');
  if (!rawValue) return null;
  const numeric = Number(rawValue);
  if (Number.isFinite(numeric) && numeric >= 0) return Math.ceil(numeric);
  const retryAt = Date.parse(rawValue);
  if (!Number.isFinite(retryAt)) return null;
  return Math.max(0, Math.ceil((retryAt - Date.now()) / 1000));
};

export const parseXaiErrorEnvelope = ({
  statusCode,
  body,
  bodyText,
  headers,
}: ParseXaiErrorEnvelopeInput): XaiErrorEnvelope => {
  const rawBody = body ?? parseMaybeJson(bodyText ?? '');
  const records = collectCandidateRecords(rawBody);
  let resolvedStatus = normalizeStatusCode(statusCode);
  if (resolvedStatus === null) {
    for (const record of records) {
      resolvedStatus = normalizeStatusCode(
        record.status ?? record.status_code ?? record.statusCode ?? record.http_status
      );
      if (resolvedStatus !== null) break;
    }
  }

  let code = firstField(records, ['code', 'error_code', 'errorCode']);
  const type = firstField(records, ['error_type', 'errorType', 'type']);
  let message = firstField(records, ['message', 'error_description', 'errorDescription']);
  for (const record of records) {
    const errorValue = normalizeString(record.error);
    if (errorValue) {
      if (!code) code = errorValue;
      if (!message) {
        message = errorValue;
      }
      break;
    }
  }
  if (!message && typeof rawBody === 'string') message = rawBody.trim();

  return {
    statusCode: resolvedStatus,
    code,
    type,
    message,
    retryAfterSeconds: parseRetryAfterSeconds(headers),
    rawBody,
  };
};

const includesAny = (text: string, markers: readonly string[]) =>
  markers.some((marker) => text.includes(marker));

const freeQuotaMarkers = [
  'subscription:free-usage-exhausted',
  'free-usage-exhausted',
  'included free usage',
] as const;

const spendingLimitMarkers = [
  'personal-team-blocked:spending-limit',
  'spending-limit',
  'run out of credits',
  'used all available credits',
  'monthly spending limit',
  'purchase more credits',
  'add credits',
] as const;

const invalidAuthMarkers = [
  'invalid_grant',
  'refresh_token_reused',
  'invalid_refresh_token',
  'token_invalidated',
  'token_revoked',
  'refresh token has been revoked',
  'bad-credentials',
  'unauthenticated:bad-credentials',
  'invalid or expired credentials',
  'authentication token has been invalidated',
] as const;

const entitlementMarkers = [
  'need a grok subscription',
  'do not have an active grok subscription',
  'no active grok subscription',
  'not entitled',
  'not authorized for xai api access',
  'tier denied',
  'subscription required',
  'access to the chat endpoint is denied',
] as const;

const policyMarkers = [
  'content violates usage guidelines',
  'usage guideline violation',
  'safety_check',
  'safety check',
  'policy violation',
] as const;

const actionForHealthy = (disabled: boolean, autoRecoverOwned: boolean) =>
  disabled && autoRecoverOwned ? ('enable' as const) : ('keep' as const);

const actionForDisable = (disabled: boolean) =>
  disabled ? ('keep' as const) : ('disable' as const);

export const classifyXaiProbe = ({
  surface,
  envelope,
  hasPayload = false,
  requestError = '',
  disabled = false,
  autoRecoverOwned = false,
}: ClassifyXaiProbeInput): XaiProbeDecision => {
  const status = envelope.statusCode ?? 0;
  const blob = `${envelope.code} ${envelope.type} ${envelope.message} ${requestError}`
    .toLowerCase()
    .trim();
  const base = { retryAfterSeconds: envelope.retryAfterSeconds };

  if (includesAny(blob, freeQuotaMarkers)) {
    return {
      ...base,
      classification: 'free_quota_exhausted',
      suggestedAction: actionForDisable(disabled),
      reasonCode: 'xai_free_usage_exhausted',
      confidence: 'verified',
      needsReview: false,
    };
  }
  if (includesAny(blob, spendingLimitMarkers)) {
    return {
      ...base,
      classification: 'spending_limit',
      suggestedAction: actionForDisable(disabled),
      reasonCode: 'xai_spending_limit',
      confidence: 'verified',
      needsReview: false,
    };
  }
  if (includesAny(blob, invalidAuthMarkers) || status === 401) {
    return {
      ...base,
      classification: 'auth_invalid',
      suggestedAction: 'reauth',
      reasonCode: includesAny(blob, invalidAuthMarkers) ? 'xai_auth_invalid' : 'xai_http_401',
      confidence: includesAny(blob, invalidAuthMarkers) ? 'verified' : 'inferred',
      needsReview: true,
    };
  }
  if (status === 426) {
    return {
      ...base,
      classification: 'client_outdated',
      suggestedAction: 'keep',
      reasonCode: 'xai_client_outdated',
      confidence: 'verified',
      needsReview: false,
    };
  }
  if (includesAny(blob, policyMarkers)) {
    return {
      ...base,
      classification: 'policy_denied',
      suggestedAction: 'keep',
      reasonCode: 'xai_policy_denied',
      confidence: 'verified',
      needsReview: true,
    };
  }
  if (includesAny(blob, entitlementMarkers)) {
    return {
      ...base,
      classification: 'entitlement_denied',
      suggestedAction: actionForDisable(disabled),
      reasonCode: 'xai_entitlement_denied',
      confidence: 'verified',
      needsReview: true,
    };
  }
  if (status === 429) {
    return {
      ...base,
      classification: 'rate_limited',
      suggestedAction: 'keep',
      reasonCode: 'xai_rate_limited',
      confidence: 'inferred',
      needsReview: false,
    };
  }
  if (status === 403) {
    return {
      ...base,
      classification: 'permission_unknown',
      suggestedAction: 'keep',
      reasonCode: 'xai_permission_unknown',
      confidence: 'unknown',
      needsReview: true,
    };
  }
  if (status === 402) {
    return {
      ...base,
      classification: 'quota_or_entitlement_unknown',
      suggestedAction: 'keep',
      reasonCode: 'xai_http_402_unknown',
      confidence: 'unknown',
      needsReview: true,
    };
  }
  if (status === 400 || status === 422) {
    return {
      ...base,
      classification: 'probe_invalid',
      suggestedAction: 'keep',
      reasonCode: `xai_http_${status}`,
      confidence: 'inferred',
      needsReview: false,
    };
  }
  if (status === 404) {
    return {
      ...base,
      classification: 'model_unavailable',
      suggestedAction: 'keep',
      reasonCode: 'xai_model_or_endpoint_unavailable',
      confidence: 'inferred',
      needsReview: false,
    };
  }
  if (requestError || status === 0 || status >= 500) {
    return {
      ...base,
      classification: 'upstream_error',
      suggestedAction: 'keep',
      reasonCode: requestError ? 'xai_request_error' : `xai_http_${status || 0}`,
      confidence: 'inferred',
      needsReview: false,
    };
  }
  if (status >= 200 && status < 300) {
    return {
      ...base,
      classification: hasPayload
        ? surface === 'billing'
          ? 'billing_healthy'
          : 'inference_healthy'
        : 'protocol_changed',
      suggestedAction: hasPayload ? actionForHealthy(disabled, autoRecoverOwned) : 'keep',
      reasonCode: hasPayload
        ? surface === 'billing'
          ? 'xai_billing_healthy'
          : 'xai_inference_healthy'
        : 'xai_empty_or_changed_payload',
      confidence: hasPayload ? 'verified' : 'unknown',
      needsReview: !hasPayload,
    };
  }
  return {
    ...base,
    classification: 'unknown',
    suggestedAction: 'keep',
    reasonCode: 'xai_unknown_error',
    confidence: 'unknown',
    needsReview: true,
  };
};

export class XaiProbeError extends Error {
  readonly status?: number;
  readonly envelope: XaiErrorEnvelope;
  readonly decision: XaiProbeDecision;

  constructor(message: string, envelope: XaiErrorEnvelope, decision: XaiProbeDecision) {
    super(message);
    this.name = 'XaiProbeError';
    this.status = envelope.statusCode ?? undefined;
    this.envelope = envelope;
    this.decision = decision;
  }
}
