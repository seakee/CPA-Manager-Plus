import i18n from '@/i18n';
import { maskApiKey } from './format';
import { normalizeAuthIndex } from './authIndex';
import { parseTimestampMs } from './timestamp';

export { normalizeAuthIndex };

export interface ModelPrice {
  prompt: number;
  completion: number;
  cache: number;
  cacheRead?: number;
  cacheCreation?: number;
  promptConfigured?: boolean;
  completionConfigured?: boolean;
  cacheReadConfigured?: boolean;
  cacheCreationConfigured?: boolean;
  source?: string;
  sourceModelId?: string;
  rawJson?: string;
  updatedAtMs?: number;
  syncedAtMs?: number;
}

export interface UsageTokens {
  input_tokens?: number;
  output_tokens?: number;
  reasoning_tokens?: number;
  cached_tokens?: number;
  cache_tokens?: number;
  cache_read_tokens?: number;
  cache_read_input_tokens?: number;
  cacheReadInputTokens?: number;
  cache_creation_tokens?: number;
  cache_creation_input_tokens?: number;
  cacheCreationInputTokens?: number;
  cache_write_tokens?: number;
  cacheWriteTokens?: number;
  cache_write_input_tokens?: number;
  cacheWriteInputTokens?: number;
  total_tokens?: number;
}

export interface UsageResponseHeaderQuotaWindow {
  used_percent?: number;
  reset_at_ms?: number;
  reset_after_seconds?: number;
  window_minutes?: number;
}

export interface UsageResponseHeaderMetadata {
  quota?: {
    plan_type?: string;
    active_limit?: string;
    rate_limit_reached_type?: string;
    summary_window_kind?: string;
    summary_window_source?: string;
    reached_window_kind?: string;
    reached_window_source?: string;
    primary?: UsageResponseHeaderQuotaWindow;
    secondary?: UsageResponseHeaderQuotaWindow;
    recover_at_ms?: number;
    used_percent?: number;
  };
  errors?: {
    kind?: string;
    code?: string;
    authorization_error?: string;
    ide_error_code?: string;
    ide_root_error_code?: string;
    retry_after_seconds?: number;
    retry_after_recover_at_ms?: number;
    rate_limit_bypass?: string;
  };
  trace?: {
    primary_trace_id?: string;
    openai_request_id?: string;
    request_id?: string;
    oneapi_request_id?: string;
    cf_ray?: string;
    eagle_id?: string;
    cloud_ai_companion_trace_id?: string;
    client_request_id?: string;
    zeabur_request_id?: string;
  };
  routing?: {
    openai_proxy_wasm?: string;
    models_etag?: string;
    new_api_version?: string;
    server?: string;
    via?: string;
    cf_cache_status?: string;
    site_cache_status?: string;
    served_by?: string;
    mife_upstream_status?: string;
  };
  response?: {
    content_type?: string;
    content_length?: number;
    content_disposition?: string;
    server_timing?: string;
  };
  providers?: {
    antigravity_trace_id?: string;
    antigravity_server_timing?: string;
    mife_upstream_status?: string;
    oneapi_request_id?: string;
    cloudflare_ray?: string;
    cloudflare_cache_status?: string;
  };
}

export interface UsageDetail {
  timestamp: string;
  source: string;
  auth_index: string | number | null;
  api_key_hash?: string;
  apiKeyHash?: string;
  account_snapshot?: string;
  accountSnapshot?: string;
  auth_label_snapshot?: string;
  authLabelSnapshot?: string;
  auth_file_snapshot?: string;
  authFileSnapshot?: string;
  auth_provider_snapshot?: string;
  authProviderSnapshot?: string;
  auth_project_id_snapshot?: string;
  authProjectIdSnapshot?: string;
  auth_snapshot_at_ms?: number;
  authSnapshotAtMs?: number;
  reasoning_effort?: string;
  reasoningEffort?: string;
  service_tier?: string;
  serviceTier?: string;
  executor_type?: string;
  executorType?: string;
  latency_ms?: number;
  ttft_ms?: number;
  tokens: UsageTokens;
  failed: boolean;
  fail_status_code?: number | null;
  failStatusCode?: number | null;
  fail_summary?: string;
  failSummary?: string;
  response_metadata?: UsageResponseHeaderMetadata;
  responseMetadata?: UsageResponseHeaderMetadata;
  header_quota_recover_at_ms?: number | null;
  headerQuotaRecoverAtMs?: number | null;
  header_quota_used_percent?: number | null;
  headerQuotaUsedPercent?: number | null;
  header_quota_plan_type?: string;
  headerQuotaPlanType?: string;
  header_error_kind?: string;
  headerErrorKind?: string;
  header_error_code?: string;
  headerErrorCode?: string;
  header_trace_id?: string;
  headerTraceId?: string;
  fail_body?: string;
  failBody?: string;
  __modelName?: string;
  __resolvedModel?: string;
  __timestampMs?: number;
}

export interface UsageDetailWithEndpoint extends UsageDetail {
  __endpoint: string;
  __endpointMethod?: string;
  __endpointPath?: string;
  __timestampMs: number;
}

export interface DurationFormatOptions {
  maxUnits?: number;
  invalidText?: string;
  secondDecimals?: number | 'auto';
  locale?: string;
}

const TOKENS_PER_PRICE_UNIT = 1_000_000;
const MODEL_PRICE_STORAGE_KEY = 'cli-proxy-model-prices-v2';
const USAGE_ENDPOINT_METHOD_REGEX = /^(GET|POST|PUT|PATCH|DELETE|OPTIONS|HEAD)\s+(\S+)/i;
const USAGE_SOURCE_PREFIX_KEY = 'k:';
const USAGE_SOURCE_PREFIX_MASKED = 'm:';
const USAGE_SOURCE_PREFIX_TEXT = 't:';
const KEY_LIKE_TOKEN_REGEX =
  /(sk-proj-[A-Za-z0-9-_]{6,}|sk-ant-[A-Za-z0-9-_]{6,}|sk-[A-Za-z0-9-_]{6,}|sess-[A-Za-z0-9-_]{6,}|ghp_[A-Za-z0-9]{6,}|github_pat_[A-Za-z0-9_]{20,}|AIza[0-9A-Za-z-_]{8,}|hf_[A-Za-z0-9]{6,}|pk_[A-Za-z0-9]{6,}|rk_[A-Za-z0-9]{6,})/;
const MASKED_TOKEN_HINT_REGEX = /^[^\s]{1,24}(\*{2,}|\.{3})[^\s]{1,24}$/;
const BACKEND_MASKED_SOURCE_REGEX = /^m:(\*{4}|[^\s/\\]{4}\.\.\.[^\s/\\]{4})$/;

const keyFingerprintCache = new Map<string, string>();
const usageDetailsCache = new WeakMap<object, UsageDetail[]>();
const usageDetailsWithEndpointCache = new WeakMap<object, UsageDetailWithEndpoint[]>();
const CACHE_READ_TOKEN_KEYS = [
  'cache_read_tokens',
  'cacheReadTokens',
  'cache_read_input_tokens',
  'cacheReadInputTokens',
] as const;
const CACHE_CREATION_TOKEN_KEYS = [
  'cache_creation_tokens',
  'cacheCreationTokens',
  'cache_creation_input_tokens',
  'cacheCreationInputTokens',
  'cache_write_tokens',
  'cacheWriteTokens',
  'cache_write_input_tokens',
  'cacheWriteInputTokens',
] as const;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const toFiniteNumber = (value: unknown): number => {
  const numberValue = typeof value === 'number' ? value : Number(value);
  return Number.isFinite(numberValue) ? numberValue : 0;
};

const readFirstTokenNumber = (record: Record<string, unknown>, keys: readonly string[]): number => {
  for (const key of keys) {
    const value = toFiniteNumber(record[key]);
    if (value !== 0) return value;
  }
  return 0;
};

const toPositiveNumber = (value: unknown): number | undefined => {
  const numberValue = toFiniteNumber(value);
  return numberValue > 0 ? numberValue : undefined;
};

const toOptionalNumber = (value: unknown): number | undefined => {
  if (value === null || value === undefined || (typeof value === 'string' && value.trim() === '')) {
    return undefined;
  }
  const numberValue = toFiniteNumber(value);
  return Number.isFinite(numberValue) ? numberValue : undefined;
};

const readDetailString = (value: unknown): string | undefined => {
  if (value === null || value === undefined) return undefined;
  const text = String(value).trim();
  return text || undefined;
};

const readResponseHeaderMetadata = (value: unknown): UsageResponseHeaderMetadata | undefined =>
  isRecord(value) ? (value as UsageResponseHeaderMetadata) : undefined;

const normalizedModelSlug = (modelName: string): string => {
  const normalized = String(modelName ?? '')
    .trim()
    .toLowerCase();
  const separator = normalized.lastIndexOf('/');
  return separator >= 0 ? normalized.slice(separator + 1) : normalized;
};

const isModelFamily = (modelName: string, family: string): boolean => {
  const slug = normalizedModelSlug(modelName);
  return slug === family || slug.startsWith(`${family}-`);
};

const isGpt56Model = (modelName: string): boolean => isModelFamily(modelName, 'gpt-5.6');

const isConfiguredPriceValue = (value: unknown, configured?: boolean): boolean => {
  const parsed = Number(value);
  return configured === true || (Number.isFinite(parsed) && parsed > 0);
};

const getOfficialGpt56Price = (modelName: string): ModelPrice | undefined => {
  if (isModelFamily(modelName, 'gpt-5.6-sol')) {
    return {
      prompt: 5,
      completion: 30,
      cache: 0.5,
      cacheRead: 0.5,
      cacheCreation: 6.25,
      promptConfigured: true,
      completionConfigured: true,
      cacheReadConfigured: true,
      cacheCreationConfigured: true,
    };
  }
  if (isModelFamily(modelName, 'gpt-5.6-terra')) {
    return {
      prompt: 2.5,
      completion: 15,
      cache: 0.25,
      cacheRead: 0.25,
      cacheCreation: 3.125,
      promptConfigured: true,
      completionConfigured: true,
      cacheReadConfigured: true,
      cacheCreationConfigured: true,
    };
  }
  if (isModelFamily(modelName, 'gpt-5.6-luna')) {
    return {
      prompt: 1,
      completion: 6,
      cache: 0.1,
      cacheRead: 0.1,
      cacheCreation: 1.25,
      promptConfigured: true,
      completionConfigured: true,
      cacheReadConfigured: true,
      cacheCreationConfigured: true,
    };
  }
  return undefined;
};

export function getServiceTierMultiplier(modelName: string, serviceTier?: string): number {
  const tier = String(serviceTier ?? '')
    .trim()
    .toLowerCase();
  if (tier !== 'priority' && tier !== 'fast') return 1;

  const normalizedModel = String(modelName ?? '')
    .trim()
    .toLowerCase();
  // OpenAI Priority pricing currently publishes tier multipliers for these
  // model families. Keep this as a compatibility layer until model prices can
  // be represented per tier, such as standard, priority, flex, and batch.
  if (isModelFamily(normalizedModel, 'gpt-5.6')) return 2;
  if (isModelFamily(normalizedModel, 'gpt-5.5')) return 2.5;
  if (isModelFamily(normalizedModel, 'gpt-5.4-mini')) return 2;
  if (isModelFamily(normalizedModel, 'gpt-5.4')) return 2;
  if (isModelFamily(normalizedModel, 'gpt-5.3-codex')) return 2;
  return 1;
}

export const compatibleCachedTokens = (
  cachedTokens: unknown,
  cacheTokens: unknown,
  cacheReadTokens: unknown,
  cacheCreationTokens: unknown
): number => {
  const cached = Math.max(
    Math.max(toFiniteNumber(cachedTokens), 0),
    Math.max(toFiniteNumber(cacheTokens), 0)
  );
  if (cached <= 0) return 0;
  const fineGrained =
    Math.max(toFiniteNumber(cacheReadTokens), 0) + Math.max(toFiniteNumber(cacheCreationTokens), 0);
  return Math.max(cached - fineGrained, 0);
};

const getApisRecord = (usageData: unknown): Record<string, unknown> | null => {
  const usageRecord = isRecord(usageData) ? usageData : null;
  const apisRaw = usageRecord ? usageRecord.apis : null;
  return isRecord(apisRaw) ? apisRaw : null;
};

const fnv1a64Hex = (value: string): string => {
  const cached = keyFingerprintCache.get(value);
  if (cached) return cached;

  const FNV_OFFSET_BASIS = 0xcbf29ce484222325n;
  const FNV_PRIME = 0x100000001b3n;

  let hash = FNV_OFFSET_BASIS;
  for (let i = 0; i < value.length; i += 1) {
    hash ^= BigInt(value.charCodeAt(i));
    hash = (hash * FNV_PRIME) & 0xffffffffffffffffn;
  }

  const hex = hash.toString(16).padStart(16, '0');
  keyFingerprintCache.set(value, hex);
  return hex;
};

const looksLikeRawSecret = (text: string): boolean => {
  if (!text || /\s/.test(text)) return false;

  const lower = text.toLowerCase();
  if (lower.endsWith('.json')) return false;
  if (lower.startsWith('http://') || lower.startsWith('https://')) return false;
  if (/[\\/]/.test(text)) return false;
  if (KEY_LIKE_TOKEN_REGEX.test(text)) return true;
  if (text.length >= 32 && text.length <= 512) return true;
  if (text.length >= 16 && text.length < 32 && /^[A-Za-z0-9._=-]+$/.test(text)) {
    return /[A-Za-z]/.test(text) && /\d/.test(text);
  }
  return false;
};

const extractRawSecretFromText = (text: string): string | null => {
  if (!text) return null;
  if (looksLikeRawSecret(text)) return text;

  const keyLikeMatch = text.match(KEY_LIKE_TOKEN_REGEX);
  if (keyLikeMatch?.[0]) return keyLikeMatch[0];

  const queryMatch = text.match(
    /(?:[?&])(api[-_]?key|key|token|access_token|authorization)=([^&#\s]+)/i
  );
  const queryValue = queryMatch?.[2];
  if (queryValue && looksLikeRawSecret(queryValue)) return queryValue;

  const headerMatch = text.match(
    /(api[-_]?key|key|token|access[-_]?token|authorization)\s*[:=]\s*([A-Za-z0-9._=-]+)/i
  );
  const headerValue = headerMatch?.[2];
  if (headerValue && looksLikeRawSecret(headerValue)) return headerValue;

  const bearerMatch = text.match(/\bBearer\s+([A-Za-z0-9._=-]{6,})/i);
  const bearerValue = bearerMatch?.[1];
  return bearerValue && looksLikeRawSecret(bearerValue) ? bearerValue : null;
};

export function maskUsageSecretSource(secret: string): string {
  const trimmed = String(secret || '').trim();
  if (!trimmed) return '';

  if (trimmed.length <= 8) return '****';
  return `${trimmed.slice(0, 4)}...${trimmed.slice(-4)}`;
}

export function normalizeUsageSourceId(
  value: unknown,
  masker: (val: string) => string = maskApiKey
): string {
  const raw =
    typeof value === 'string' ? value : value === null || value === undefined ? '' : String(value);
  const trimmed = raw.trim();
  if (!trimmed) return '';
  if (trimmed.startsWith(USAGE_SOURCE_PREFIX_KEY)) return trimmed;
  if (trimmed.startsWith(USAGE_SOURCE_PREFIX_MASKED)) {
    if (BACKEND_MASKED_SOURCE_REGEX.test(trimmed)) return trimmed;
    const maskedValue = trimmed.slice(USAGE_SOURCE_PREFIX_MASKED.length).trim();
    const extracted = extractRawSecretFromText(maskedValue) || extractRawSecretFromText(trimmed);
    return extracted ? `${USAGE_SOURCE_PREFIX_KEY}${fnv1a64Hex(extracted)}` : trimmed;
  }
  if (trimmed.startsWith(USAGE_SOURCE_PREFIX_TEXT)) {
    const textSource = trimmed.slice(USAGE_SOURCE_PREFIX_TEXT.length).trim();
    const extracted = extractRawSecretFromText(textSource);
    return extracted ? `${USAGE_SOURCE_PREFIX_KEY}${fnv1a64Hex(extracted)}` : trimmed;
  }

  const extracted = extractRawSecretFromText(trimmed);
  if (extracted) return `${USAGE_SOURCE_PREFIX_KEY}${fnv1a64Hex(extracted)}`;
  if (MASKED_TOKEN_HINT_REGEX.test(trimmed)) {
    return `${USAGE_SOURCE_PREFIX_MASKED}${masker(trimmed)}`;
  }
  return `${USAGE_SOURCE_PREFIX_TEXT}${trimmed}`;
}

export function buildCandidateUsageSourceIds(input: {
  apiKey?: string;
  prefix?: string;
}): string[] {
  const result: string[] = [];
  const prefix = input.prefix?.trim();
  if (prefix) result.push(`${USAGE_SOURCE_PREFIX_TEXT}${prefix}`);

  const apiKey = input.apiKey?.trim();
  if (apiKey) {
    result.push(normalizeUsageSourceId(apiKey));
    result.push(`${USAGE_SOURCE_PREFIX_MASKED}${maskUsageSecretSource(apiKey)}`);
    result.push(`${USAGE_SOURCE_PREFIX_MASKED}${maskApiKey(apiKey)}`);
    result.push(`${USAGE_SOURCE_PREFIX_TEXT}${maskApiKey(apiKey)}`);
  }

  return Array.from(new Set(result.filter(Boolean)));
}

export function extractLatencyMs(detail: unknown): number | null {
  const record = isRecord(detail) ? detail : null;
  const rawValue = record?.latency_ms ?? record?.latencyMs;
  if (
    rawValue === null ||
    rawValue === undefined ||
    (typeof rawValue === 'string' && rawValue.trim() === '')
  ) {
    return null;
  }

  const parsed = Number(rawValue);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : null;
}

export function extractTTFTMs(detail: unknown): number | null {
  const record = isRecord(detail) ? detail : null;
  const rawValue =
    record?.ttft_ms ??
    record?.ttftMs ??
    record?.time_to_first_token_ms ??
    record?.timeToFirstTokenMs;
  if (
    rawValue === null ||
    rawValue === undefined ||
    (typeof rawValue === 'string' && rawValue.trim() === '')
  ) {
    return null;
  }

  const parsed = Number(rawValue);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : null;
}

const readTokens = (detail: Record<string, unknown>): UsageTokens => {
  const tokensRaw = isRecord(detail.tokens) ? detail.tokens : {};
  const cacheReadTokens = readFirstTokenNumber(tokensRaw, CACHE_READ_TOKEN_KEYS);
  const cacheCreationTokens = readFirstTokenNumber(tokensRaw, CACHE_CREATION_TOKEN_KEYS);
  const cachedTokens = compatibleCachedTokens(
    tokensRaw.cached_tokens ?? tokensRaw.cachedTokens,
    tokensRaw.cache_tokens ?? tokensRaw.cacheTokens,
    cacheReadTokens,
    cacheCreationTokens
  );
  const inputTokens = toFiniteNumber(tokensRaw.input_tokens ?? tokensRaw.inputTokens);
  const outputTokens = toFiniteNumber(tokensRaw.output_tokens ?? tokensRaw.outputTokens);
  const reasoningTokens = toFiniteNumber(tokensRaw.reasoning_tokens ?? tokensRaw.reasoningTokens);
  const explicitTotalTokens = toFiniteNumber(tokensRaw.total_tokens ?? tokensRaw.totalTokens);
  const totalTokens =
    explicitTotalTokens > 0
      ? explicitTotalTokens
      : inputTokens +
        outputTokens +
        reasoningTokens +
        cachedTokens +
        cacheReadTokens +
        cacheCreationTokens;
  return {
    input_tokens: inputTokens,
    output_tokens: outputTokens,
    reasoning_tokens: reasoningTokens,
    cached_tokens: cachedTokens,
    cache_tokens: cachedTokens,
    cache_read_tokens: cacheReadTokens,
    cache_creation_tokens: cacheCreationTokens,
    total_tokens: totalTokens,
  };
};

const normalizeSourceWithCache = (sourceCache: Map<string, string>, value: unknown): string => {
  const raw =
    typeof value === 'string' ? value : value === null || value === undefined ? '' : String(value);
  const trimmed = raw.trim();
  if (!trimmed) return '';

  const cached = sourceCache.get(trimmed);
  if (cached !== undefined) return cached;

  const normalized = normalizeUsageSourceId(trimmed);
  sourceCache.set(trimmed, normalized);
  return normalized;
};

export function collectUsageDetails(usageData: unknown): UsageDetail[] {
  const cacheKey = isRecord(usageData) ? (usageData as object) : null;
  if (cacheKey) {
    const cached = usageDetailsCache.get(cacheKey);
    if (cached) return cached;
  }

  const apis = getApisRecord(usageData);
  if (!apis) return [];

  const details: UsageDetail[] = [];
  const sourceCache = new Map<string, string>();

  Object.values(apis).forEach((apiEntry) => {
    if (!isRecord(apiEntry)) return;
    const models = isRecord(apiEntry.models) ? apiEntry.models : null;
    if (!models) return;

    Object.entries(models).forEach(([modelName, modelEntry]) => {
      if (!isRecord(modelEntry)) return;
      const modelDetails = Array.isArray(modelEntry.details) ? modelEntry.details : [];

      modelDetails.forEach((detailRaw) => {
        if (!isRecord(detailRaw) || typeof detailRaw.timestamp !== 'string') return;
        const timestamp = detailRaw.timestamp;
        const timestampMs = parseTimestampMs(timestamp);
        const latencyMs = extractLatencyMs(detailRaw);
        const ttftMs = extractTTFTMs(detailRaw);
        const failRaw = isRecord(detailRaw.fail) ? detailRaw.fail : {};
        details.push({
          timestamp,
          source: normalizeSourceWithCache(sourceCache, detailRaw.source),
          auth_index: (detailRaw.auth_index ??
            detailRaw.authIndex ??
            detailRaw.AuthIndex ??
            null) as UsageDetail['auth_index'],
          api_key_hash: readDetailString(detailRaw.api_key_hash ?? detailRaw.apiKeyHash),
          account_snapshot: readDetailString(
            detailRaw.account_snapshot ?? detailRaw.accountSnapshot
          ),
          auth_label_snapshot: readDetailString(
            detailRaw.auth_label_snapshot ?? detailRaw.authLabelSnapshot
          ),
          auth_file_snapshot: readDetailString(
            detailRaw.auth_file_snapshot ?? detailRaw.authFileSnapshot
          ),
          auth_provider_snapshot: readDetailString(
            detailRaw.auth_provider_snapshot ?? detailRaw.authProviderSnapshot
          ),
          auth_project_id_snapshot: readDetailString(
            detailRaw.auth_project_id_snapshot ?? detailRaw.authProjectIdSnapshot
          ),
          auth_snapshot_at_ms: toPositiveNumber(
            detailRaw.auth_snapshot_at_ms ?? detailRaw.authSnapshotAtMs
          ),
          reasoning_effort: readDetailString(
            detailRaw.reasoning_effort ?? detailRaw.reasoningEffort
          ),
          service_tier: readDetailString(detailRaw.service_tier ?? detailRaw.serviceTier),
          executor_type: readDetailString(detailRaw.executor_type ?? detailRaw.executorType),
          latency_ms: latencyMs ?? undefined,
          ttft_ms: ttftMs ?? undefined,
          tokens: readTokens(detailRaw),
          failed: detailRaw.failed === true,
          fail_status_code:
            toOptionalNumber(
              detailRaw.fail_status_code ??
                detailRaw.failStatusCode ??
                failRaw.status_code ??
                failRaw.statusCode
            ) ?? null,
          fail_summary: readDetailString(detailRaw.fail_summary ?? detailRaw.failSummary),
          response_metadata: readResponseHeaderMetadata(
            detailRaw.response_metadata ?? detailRaw.responseMetadata
          ),
          header_quota_recover_at_ms:
            toOptionalNumber(
              detailRaw.header_quota_recover_at_ms ?? detailRaw.headerQuotaRecoverAtMs
            ) ?? null,
          header_quota_used_percent:
            toOptionalNumber(
              detailRaw.header_quota_used_percent ?? detailRaw.headerQuotaUsedPercent
            ) ?? null,
          header_quota_plan_type: readDetailString(
            detailRaw.header_quota_plan_type ?? detailRaw.headerQuotaPlanType
          ),
          header_error_kind: readDetailString(
            detailRaw.header_error_kind ?? detailRaw.headerErrorKind
          ),
          header_error_code: readDetailString(
            detailRaw.header_error_code ?? detailRaw.headerErrorCode
          ),
          header_trace_id: readDetailString(detailRaw.header_trace_id ?? detailRaw.headerTraceId),
          fail_body: readDetailString(detailRaw.fail_body ?? detailRaw.failBody ?? failRaw.body),
          __modelName: modelName,
          __resolvedModel: readDetailString(detailRaw.resolved_model ?? detailRaw.resolvedModel),
          __timestampMs: Number.isNaN(timestampMs) ? 0 : timestampMs,
        });
      });
    });
  });

  if (cacheKey) usageDetailsCache.set(cacheKey, details);
  return details;
}

export function collectUsageDetailsWithEndpoint(usageData: unknown): UsageDetailWithEndpoint[] {
  const cacheKey = isRecord(usageData) ? (usageData as object) : null;
  if (cacheKey) {
    const cached = usageDetailsWithEndpointCache.get(cacheKey);
    if (cached) return cached;
  }

  const apis = getApisRecord(usageData);
  if (!apis) return [];

  const details: UsageDetailWithEndpoint[] = [];
  const sourceCache = new Map<string, string>();

  Object.entries(apis).forEach(([endpoint, apiEntry]) => {
    if (!isRecord(apiEntry)) return;
    const models = isRecord(apiEntry.models) ? apiEntry.models : null;
    if (!models) return;

    const endpointMatch = endpoint.match(USAGE_ENDPOINT_METHOD_REGEX);
    const endpointMethod = endpointMatch?.[1]?.toUpperCase();
    const endpointPath = endpointMatch?.[2];

    Object.entries(models).forEach(([modelName, modelEntry]) => {
      if (!isRecord(modelEntry)) return;
      const modelDetails = Array.isArray(modelEntry.details) ? modelEntry.details : [];

      modelDetails.forEach((detailRaw) => {
        if (!isRecord(detailRaw) || typeof detailRaw.timestamp !== 'string') return;
        const timestamp = detailRaw.timestamp;
        const timestampMs = parseTimestampMs(timestamp);
        const latencyMs = extractLatencyMs(detailRaw);
        const ttftMs = extractTTFTMs(detailRaw);
        const failRaw = isRecord(detailRaw.fail) ? detailRaw.fail : {};
        details.push({
          timestamp,
          source: normalizeSourceWithCache(sourceCache, detailRaw.source),
          auth_index: (detailRaw.auth_index ??
            detailRaw.authIndex ??
            detailRaw.AuthIndex ??
            null) as UsageDetail['auth_index'],
          api_key_hash: readDetailString(detailRaw.api_key_hash ?? detailRaw.apiKeyHash),
          account_snapshot: readDetailString(
            detailRaw.account_snapshot ?? detailRaw.accountSnapshot
          ),
          auth_label_snapshot: readDetailString(
            detailRaw.auth_label_snapshot ?? detailRaw.authLabelSnapshot
          ),
          auth_file_snapshot: readDetailString(
            detailRaw.auth_file_snapshot ?? detailRaw.authFileSnapshot
          ),
          auth_provider_snapshot: readDetailString(
            detailRaw.auth_provider_snapshot ?? detailRaw.authProviderSnapshot
          ),
          auth_project_id_snapshot: readDetailString(
            detailRaw.auth_project_id_snapshot ?? detailRaw.authProjectIdSnapshot
          ),
          auth_snapshot_at_ms: toPositiveNumber(
            detailRaw.auth_snapshot_at_ms ?? detailRaw.authSnapshotAtMs
          ),
          reasoning_effort: readDetailString(
            detailRaw.reasoning_effort ?? detailRaw.reasoningEffort
          ),
          service_tier: readDetailString(detailRaw.service_tier ?? detailRaw.serviceTier),
          executor_type: readDetailString(detailRaw.executor_type ?? detailRaw.executorType),
          latency_ms: latencyMs ?? undefined,
          ttft_ms: ttftMs ?? undefined,
          tokens: readTokens(detailRaw),
          failed: detailRaw.failed === true,
          fail_status_code:
            toOptionalNumber(
              detailRaw.fail_status_code ??
                detailRaw.failStatusCode ??
                failRaw.status_code ??
                failRaw.statusCode
            ) ?? null,
          fail_summary: readDetailString(detailRaw.fail_summary ?? detailRaw.failSummary),
          response_metadata: readResponseHeaderMetadata(
            detailRaw.response_metadata ?? detailRaw.responseMetadata
          ),
          header_quota_recover_at_ms:
            toOptionalNumber(
              detailRaw.header_quota_recover_at_ms ?? detailRaw.headerQuotaRecoverAtMs
            ) ?? null,
          header_quota_used_percent:
            toOptionalNumber(
              detailRaw.header_quota_used_percent ?? detailRaw.headerQuotaUsedPercent
            ) ?? null,
          header_quota_plan_type: readDetailString(
            detailRaw.header_quota_plan_type ?? detailRaw.headerQuotaPlanType
          ),
          header_error_kind: readDetailString(
            detailRaw.header_error_kind ?? detailRaw.headerErrorKind
          ),
          header_error_code: readDetailString(
            detailRaw.header_error_code ?? detailRaw.headerErrorCode
          ),
          header_trace_id: readDetailString(detailRaw.header_trace_id ?? detailRaw.headerTraceId),
          fail_body: readDetailString(detailRaw.fail_body ?? detailRaw.failBody ?? failRaw.body),
          __modelName: modelName,
          __resolvedModel: readDetailString(detailRaw.resolved_model ?? detailRaw.resolvedModel),
          __endpoint: endpoint,
          __endpointMethod: endpointMethod,
          __endpointPath: endpointPath,
          __timestampMs: Number.isNaN(timestampMs) ? 0 : timestampMs,
        });
      });
    });
  });

  if (cacheKey) usageDetailsWithEndpointCache.set(cacheKey, details);
  return details;
}

export function extractTotalTokens(detail: unknown): number {
  const record = isRecord(detail) ? detail : null;
  const tokens = record && isRecord(record.tokens) ? record.tokens : {};
  const cacheReadTokens = Math.max(readFirstTokenNumber(tokens, CACHE_READ_TOKEN_KEYS), 0);
  const cacheCreationTokens = Math.max(readFirstTokenNumber(tokens, CACHE_CREATION_TOKEN_KEYS), 0);
  const explicitTotal = toFiniteNumber(tokens.total_tokens ?? tokens.totalTokens);
  if (explicitTotal > 0) return explicitTotal;

  const inputTokens = toFiniteNumber(tokens.input_tokens ?? tokens.inputTokens);
  const outputTokens = toFiniteNumber(tokens.output_tokens ?? tokens.outputTokens);
  const reasoningTokens = toFiniteNumber(tokens.reasoning_tokens ?? tokens.reasoningTokens);
  const cachedTokens = compatibleCachedTokens(
    tokens.cached_tokens ?? tokens.cachedTokens,
    tokens.cache_tokens ?? tokens.cacheTokens,
    cacheReadTokens,
    cacheCreationTokens
  );

  return (
    inputTokens +
    outputTokens +
    reasoningTokens +
    cachedTokens +
    cacheReadTokens +
    cacheCreationTokens
  );
}

export function calculateCost(
  detail: Pick<
    UsageDetail,
    'tokens' | '__modelName' | '__resolvedModel' | 'service_tier' | 'serviceTier'
  >,
  modelPrices: Record<string, ModelPrice>
): number {
  const resolvedModel = detail.__resolvedModel || '';
  const requestedModel = detail.__modelName || '';
  const resolvedPrice = resolvedModel ? modelPrices[resolvedModel] : undefined;
  const requestedPrice = requestedModel ? modelPrices[requestedModel] : undefined;
  const behaviorModel = resolvedModel || requestedModel;
  const behaviorFallback = getOfficialGpt56Price(behaviorModel);
  const officialCandidatePrice =
    getOfficialGpt56Price(resolvedModel) || getOfficialGpt56Price(requestedModel);
  const configuredPrice = resolvedPrice || requestedPrice;
  const price = configuredPrice
    ? {
        ...configuredPrice,
        prompt: isConfiguredPriceValue(
          configuredPrice.prompt,
          configuredPrice.promptConfigured
        )
          ? Number(configuredPrice.prompt)
          : (behaviorFallback?.prompt ?? 0),
        completion: isConfiguredPriceValue(
          configuredPrice.completion,
          configuredPrice.completionConfigured
        )
          ? Number(configuredPrice.completion)
          : (behaviorFallback?.completion ?? 0),
      }
    : officialCandidatePrice;
  if (!price) return 0;

  const inputTokens = Math.max(toFiniteNumber(detail.tokens.input_tokens), 0);
  const completionTokens = Math.max(toFiniteNumber(detail.tokens.output_tokens), 0);
  const cachedTokens = Math.max(
    Math.max(toFiniteNumber(detail.tokens.cached_tokens), 0),
    Math.max(toFiniteNumber(detail.tokens.cache_tokens), 0)
  );
  const cacheReadTokens = Math.max(toFiniteNumber(detail.tokens.cache_read_tokens), 0);
  const cacheCreationTokens = Math.max(toFiniteNumber(detail.tokens.cache_creation_tokens), 0);
  const promptPrice = Number(price.prompt) || 0;
  const completionPrice = Number(price.completion) || 0;
  let standardCost = 0;
  if (isGpt56Model(behaviorModel)) {
    const configuredCacheReadPrice = Number(price.cacheRead) || 0;
    const cacheReadPrice = isConfiguredPriceValue(
      configuredCacheReadPrice,
      price.cacheReadConfigured
    )
      ? configuredCacheReadPrice
      : promptPrice * 0.1;
    const configuredCacheCreationPrice = Number(price.cacheCreation) || 0;
    const cacheCreationPrice = isConfiguredPriceValue(
      configuredCacheCreationPrice,
      price.cacheCreationConfigured
    )
      ? configuredCacheCreationPrice
      : promptPrice * 1.25;
    const readTokens = cachedTokens + cacheReadTokens;
    const promptTokens = Math.max(inputTokens - readTokens - cacheCreationTokens, 0);
    const longContext = inputTokens > 272_000;
    const inputMultiplier = longContext ? 2 : 1;
    const outputMultiplier = longContext ? 1.5 : 1;
    standardCost =
      ((promptTokens / TOKENS_PER_PRICE_UNIT) * promptPrice +
        (readTokens / TOKENS_PER_PRICE_UNIT) * cacheReadPrice +
        (cacheCreationTokens / TOKENS_PER_PRICE_UNIT) * cacheCreationPrice) *
        inputMultiplier +
      (completionTokens / TOKENS_PER_PRICE_UNIT) * completionPrice * outputMultiplier;
  } else if (cacheReadTokens > 0 || cacheCreationTokens > 0) {
    const cacheReadPrice = Number(price.cacheRead) || Number(price.cache) || 0;
    const cacheCreationPrice = Number(price.cacheCreation) || promptPrice;
    const promptTokens = Math.max(inputTokens - cachedTokens, 0);
    standardCost =
      (promptTokens / TOKENS_PER_PRICE_UNIT) * promptPrice +
      (completionTokens / TOKENS_PER_PRICE_UNIT) * completionPrice +
      (cachedTokens / TOKENS_PER_PRICE_UNIT) * (Number(price.cache) || 0) +
      (cacheReadTokens / TOKENS_PER_PRICE_UNIT) * cacheReadPrice +
      (cacheCreationTokens / TOKENS_PER_PRICE_UNIT) * cacheCreationPrice;
  } else {
    const promptTokens = Math.max(inputTokens - cachedTokens, 0);
    const promptCost = (promptTokens / TOKENS_PER_PRICE_UNIT) * promptPrice;
    const completionCost = (completionTokens / TOKENS_PER_PRICE_UNIT) * completionPrice;
    const cachedCost = (cachedTokens / TOKENS_PER_PRICE_UNIT) * (Number(price.cache) || 0);
    standardCost = promptCost + cachedCost + completionCost;
  }

  const serviceTier = detail.service_tier ?? detail.serviceTier;
  const multiplier = getServiceTierMultiplier(behaviorModel, serviceTier);
  const total = standardCost * multiplier;
  return Number.isFinite(total) && total > 0 ? total : 0;
}

export function loadModelPrices(): Record<string, ModelPrice> {
  try {
    if (typeof localStorage === 'undefined') return {};
    const raw = localStorage.getItem(MODEL_PRICE_STORAGE_KEY);
    if (!raw) return {};

    const parsed: unknown = JSON.parse(raw);
    if (!isRecord(parsed)) return {};

    const normalized: Record<string, ModelPrice> = {};
    Object.entries(parsed).forEach(([model, price]) => {
      if (!model || !isRecord(price)) return;

      const prompt = toFiniteNumber(price.prompt);
      const completion = toFiniteNumber(price.completion);
      const cacheRaw = Number(price.cache);
      const cache = Number.isFinite(cacheRaw) && cacheRaw >= 0 ? cacheRaw : prompt;
      const cacheReadRaw = Number(price.cacheRead);
      const cacheRead = Number.isFinite(cacheReadRaw) && cacheReadRaw >= 0 ? cacheReadRaw : 0;
      const cacheCreationRaw = Number(price.cacheCreation);
      const cacheCreation =
        Number.isFinite(cacheCreationRaw) && cacheCreationRaw >= 0 ? cacheCreationRaw : 0;

      if (prompt < 0 || completion < 0 || cache < 0 || cacheRead < 0 || cacheCreation < 0) return;
      normalized[model] = {
        prompt,
        completion,
        cache,
        cacheRead,
        cacheCreation,
        source: readDetailString(price.source),
        sourceModelId: readDetailString(price.sourceModelId),
        rawJson: readDetailString(price.rawJson),
        updatedAtMs: toPositiveNumber(price.updatedAtMs),
        syncedAtMs: toPositiveNumber(price.syncedAtMs),
      };
    });

    return normalized;
  } catch {
    return {};
  }
}

export function saveModelPrices(prices: Record<string, ModelPrice>): void {
  try {
    if (typeof localStorage === 'undefined') return;
    localStorage.setItem(MODEL_PRICE_STORAGE_KEY, JSON.stringify(prices));
  } catch {
    // Ignore storage failures; pricing is an optional browser-side aid.
  }
}

export function clearModelPrices(): void {
  try {
    if (typeof localStorage === 'undefined') return;
    localStorage.removeItem(MODEL_PRICE_STORAGE_KEY);
  } catch {
    // Ignore storage failures; pricing is optional fallback data.
  }
}

export function formatCompactNumber(value: number): string {
  const num = Number(value);
  if (!Number.isFinite(num)) return '0';

  const abs = Math.abs(num);
  if (abs === 0) return '0';
  const units = [
    { threshold: 1_000_000_000_000_000, suffix: 'P' },
    { threshold: 1_000_000_000_000, suffix: 'T' },
    { threshold: 1_000_000_000, suffix: 'B' },
    { threshold: 1_000_000, suffix: 'M' },
    { threshold: 1_000, suffix: 'K' },
  ];
  const unit = units.find((item) => abs >= item.threshold);

  if (unit) {
    const formatted = (num / unit.threshold).toFixed(1);
    const nextUnit = units[units.indexOf(unit) - 1];
    if (nextUnit && Math.abs(Number(formatted)) >= 1000) {
      return `${(num / nextUnit.threshold).toFixed(1)}${nextUnit.suffix}`;
    }
    return `${formatted}${unit.suffix}`;
  }

  return abs >= 1 ? num.toFixed(0) : num.toFixed(2);
}

export function formatUsd(value: number): string {
  const num = Number(value);
  if (!Number.isFinite(num)) return '$0.00';

  const fixed = num.toFixed(2);
  const parts = Number(fixed).toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  });
  return `$${parts}`;
}

const resolveDurationLocale = (locale?: string): string | undefined =>
  locale?.trim() || i18n.resolvedLanguage || i18n.language || undefined;

const formatDurationNumber = (
  value: number,
  locale: string | undefined,
  options: Intl.NumberFormatOptions = {}
): string => {
  try {
    return new Intl.NumberFormat(locale, {
      useGrouping: false,
      ...options,
    }).format(value);
  } catch {
    return String(value);
  }
};

const getDurationUnitLabel = (unit: 'd' | 'h' | 'm' | 's' | 'ms'): string =>
  i18n.t(`usage_stats.duration_unit_${unit}`, { defaultValue: unit });

const formatDurationPart = (
  value: number,
  unit: 'd' | 'h' | 'm' | 's' | 'ms',
  locale: string | undefined,
  options: Intl.NumberFormatOptions = {}
): string => `${formatDurationNumber(value, locale, options)}${getDurationUnitLabel(unit)}`;

const normalizeDurationMaxUnits = (value: number | undefined): number => {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) return 2;
  return Math.min(Math.floor(parsed), 4);
};

const resolveSecondDecimalPlaces = (
  seconds: number,
  secondDecimals: number | 'auto' | undefined
): number => {
  if (secondDecimals === 'auto' || secondDecimals === undefined) return seconds < 10 ? 2 : 1;

  const parsed = Math.floor(Number(secondDecimals));
  if (!Number.isFinite(parsed) || parsed < 0) return seconds < 10 ? 2 : 1;
  return Math.min(parsed, 3);
};

export function formatDurationMs(
  value: number | null | undefined,
  options: DurationFormatOptions = {}
): string {
  const invalidText = options.invalidText ?? '--';
  if (value === null || value === undefined) return invalidText;

  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < 0) return invalidText;

  const locale = resolveDurationLocale(options.locale);
  if (parsed < 1000) return formatDurationPart(Math.round(parsed), 'ms', locale);

  const seconds = parsed / 1000;
  if (seconds < 60) {
    const secondDecimalPlaces = resolveSecondDecimalPlaces(seconds, options.secondDecimals);
    return formatDurationPart(seconds, 's', locale, {
      minimumFractionDigits: 0,
      maximumFractionDigits: secondDecimalPlaces,
    });
  }

  const totalSeconds = Math.floor(seconds);
  let remainingSeconds = totalSeconds;
  const days = Math.floor(remainingSeconds / 86_400);
  remainingSeconds -= days * 86_400;
  const hours = Math.floor(remainingSeconds / 3_600);
  remainingSeconds -= hours * 3_600;
  const minutes = Math.floor(remainingSeconds / 60);
  remainingSeconds -= minutes * 60;

  const parts = [
    { unit: 'd' as const, value: days },
    { unit: 'h' as const, value: hours },
    { unit: 'm' as const, value: minutes },
    { unit: 's' as const, value: remainingSeconds },
  ].filter((part) => part.value > 0);

  if (!parts.length) return formatDurationPart(0, 's', locale);

  return parts
    .slice(0, normalizeDurationMaxUnits(options.maxUnits))
    .map((part, index) =>
      formatDurationPart(part.value, part.unit, locale, {
        minimumIntegerDigits: index > 0 && (part.unit === 'm' || part.unit === 's') ? 2 : 1,
        maximumFractionDigits: 0,
      })
    )
    .join(' ');
}
