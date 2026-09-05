import type { TFunction } from 'i18next';
import type { AxiosRequestConfig } from 'axios';
import type {
  AntigravityQuotaGroup,
  AntigravityQuotaSubscription,
  AntigravityQuotaSummaryPayload,
  AuthFileItem,
  ClaudeExtraUsage,
  ClaudeProfileResponse,
  ClaudeQuotaWindow,
  ClaudeUsagePayload,
  CodexRateLimitResetCredit,
  CodexQuotaWindow,
  CodexUsagePayload,
  KimiQuotaRow,
  KimiUsagePayload,
  XaiBillingConfig,
  XaiBillingPayload,
  XaiBillingDiagnostic,
  XaiBillingPeriod,
  XaiBillingPeriodType,
  XaiBillingSummary,
  XaiOfficialApiHealth,
  XaiProductUsageSummary,
} from '@/types';
import { apiCallApi, getApiCallErrorMessage } from '@/services/api/apiCall';
import { createScopedApiRequestConfig, type ApiClientRequestScope } from '@/services/api/client';
import { isRecord } from '@/utils/helpers';
import { sha256Hex } from '@/utils/apiKeyHash';
import {
  antigravitySubscriptionApi,
  type AntigravitySubscriptionSummary,
} from '@/services/api/antigravitySubscription';
import { authFilesApi } from '@/services/api/authFiles';
import {
  ANTIGRAVITY_AVAILABLE_MODELS_URLS,
  ANTIGRAVITY_QUOTA_SUMMARY_URLS,
  ANTIGRAVITY_REQUEST_HEADERS,
  CLAUDE_PROFILE_URL,
  CLAUDE_REQUEST_HEADERS,
  CLAUDE_USAGE_URL,
  CLAUDE_USAGE_WINDOW_KEYS,
  CODEX_RATE_LIMIT_RESET_CREDITS_URL,
  CODEX_USAGE_URL,
  KIMI_REQUEST_HEADERS,
  KIMI_USAGE_URL,
  XAI_BILLING_MONTHLY_URL,
  XAI_BILLING_WEEKLY_URL,
  XAI_CLI_CHAT_PROXY_BASE_URL,
  XAI_GROK_CLIENT_VERSION,
  DEFAULT_XAI_INSPECTION_MODEL,
  DEFAULT_XAI_INSPECTION_PROMPT,
  XAI_INFERENCE_USER_AGENT,
  XAI_OFFICIAL_API_BASE_URL,
  XAI_OFFICIAL_API_ME_URL,
  XAI_REQUEST_HEADERS,
} from './constants';
import { buildAntigravityQuotaGroups, buildKimiQuotaRows } from './builders';
import {
  createStatusError,
  formatQuotaResetTime,
  getStatusFromError,
  resolveAbsoluteQuotaReset,
  type CodexQuotaResetSource,
} from './formatters';
import {
  normalizeAuthIndex,
  normalizeNumberValue,
  normalizePlanType,
  normalizeStringValue,
  parseAntigravityPayload,
  parseClaudeUsagePayload,
  parseCodexUsagePayload,
  parseKimiUsagePayload,
  parseXaiBillingPayload,
} from './parsers';
import { resolveCodexChatgptAccountId, resolveCodexPlanType } from './resolvers';
import { buildCodexQuotaWindowInfos, type CodexQuotaScopeResolution } from './codexQuota';
import {
  buildCodexResetCreditsRequestHeaders,
  buildCodexUsageRequestHeaders,
} from './codexRequestHeaders';
import { normalizeCodexResetCreditsPayload } from './resetCredits';
import { classifyXaiProbe, parseXaiErrorEnvelope, XaiProbeError } from './xaiErrors';

const DEFAULT_ANTIGRAVITY_PROJECT_ID = 'bamboo-precept-lgxtn';
const CODEX_RESET_CREDITS_REQUEST_TIMEOUT_MS = 8000;
const XAI_LEGACY_BILLING_REQUEST_TIMEOUT_MS = 3000;

export type CodexQuotaData = {
  planType: string | null;
  windows: CodexQuotaWindow[];
  observedAtMs?: number;
  quotaInventoryObserved: boolean;
  subscriptionActiveUntil: string | number | null;
  creditsHasCredits?: boolean | null;
  creditsUnlimited?: boolean | null;
  creditsBalance?: string | null;
  creditsOverageLimitReached?: boolean | null;
  creditsApproxLocalMessages?: number | null;
  creditsApproxCloudMessages?: number | null;
  spendControlReached?: boolean | null;
  spendControlIndividualLimit?: number | null;
  rateLimitResetCreditsAvailableCount: number | null;
  rateLimitResetCredits: CodexRateLimitResetCredit[];
  rateLimitResetCreditsError: string | null;
};

const isCodexRateLimitInventory = (value: unknown): boolean =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

export const hasCodexQuotaInventory = (payload: CodexUsagePayload): boolean =>
  [
    payload.rate_limit,
    payload.rateLimit,
    payload.code_review_rate_limit,
    payload.codeReviewRateLimit,
  ].some(isCodexRateLimitInventory) ||
  [payload.additional_rate_limits, payload.additionalRateLimits].some(Array.isArray);

export type ClaudeQuotaData = {
  windows: ClaudeQuotaWindow[];
  quotaInventoryObserved: boolean;
  extraUsage?: ClaudeExtraUsage | null;
  planType?: string | null;
};

export type AntigravityQuotaData = {
  groups: AntigravityQuotaGroup[];
  quotaInventoryObserved: boolean;
  subscription?: AntigravityQuotaSubscription | null;
  serverTimeOffsetMs: number | null;
};

export type KimiQuotaData = {
  rows: KimiQuotaRow[];
  quotaInventoryObserved: boolean;
};

const hasOwn = (value: object, key: PropertyKey): boolean =>
  Object.prototype.hasOwnProperty.call(value, key);

const hasExplicitEmptyAntigravityInventory = (payload: AntigravityQuotaSummaryPayload): boolean => {
  if (Array.isArray(payload.groups) && payload.groups.length === 0) return true;
  return isRecord(payload.models) && Object.keys(payload.models).length === 0;
};

const antigravitySubscriptionRequests = new Map<
  string,
  Promise<AntigravityQuotaSubscription | null>
>();

const toAntigravityQuotaSubscription = (
  summary: AntigravitySubscriptionSummary | null
): AntigravityQuotaSubscription | null => {
  if (!summary) return null;
  return {
    plan: summary.plan,
    tierName: summary.tierName,
    tierId: summary.tierId,
  };
};

const fetchAntigravityQuotaSubscription = (
  authIndex: string,
  requestScope?: ApiClientRequestScope
): Promise<AntigravityQuotaSubscription | null> => {
  const requestKey = requestScope
    ? `${sha256Hex(`${requestScope.apiBase.trim()}\u0000${requestScope.managementKey.trim()}`)}\u0000${authIndex}`
    : authIndex;
  const existing = antigravitySubscriptionRequests.get(requestKey);
  if (existing) return existing;

  const request = antigravitySubscriptionApi
    .get(authIndex, requestScope)
    .then(toAntigravityQuotaSubscription)
    .catch(() => null)
    .finally(() => {
      antigravitySubscriptionRequests.delete(requestKey);
    });
  antigravitySubscriptionRequests.set(requestKey, request);
  return request;
};

export const resolveAntigravityProjectId = async (
  file: AuthFileItem,
  requestScope?: ApiClientRequestScope
): Promise<string> => {
  const directProjectId = normalizeStringValue(file.project_id ?? file.projectId);
  if (directProjectId) return directProjectId;

  const metadata =
    file.metadata && typeof file.metadata === 'object' && file.metadata !== null
      ? (file.metadata as Record<string, unknown>)
      : null;
  const metadataProjectId = metadata
    ? normalizeStringValue(metadata.project_id ?? metadata.projectId)
    : null;
  if (metadataProjectId) return metadataProjectId;

  const attributes =
    file.attributes && typeof file.attributes === 'object' && file.attributes !== null
      ? (file.attributes as Record<string, unknown>)
      : null;
  const attributesProjectId = attributes
    ? normalizeStringValue(
        attributes.project_id ?? attributes.projectId ?? attributes.gemini_virtual_project
      )
    : null;
  if (attributesProjectId) return attributesProjectId;

  try {
    const text = await authFilesApi.downloadText(file.name, requestScope);
    const trimmed = text.trim();
    if (!trimmed) return DEFAULT_ANTIGRAVITY_PROJECT_ID;

    const parsed = JSON.parse(trimmed) as Record<string, unknown>;
    const topLevel = normalizeStringValue(parsed.project_id ?? parsed.projectId);
    if (topLevel) return topLevel;

    const installed =
      parsed.installed && typeof parsed.installed === 'object' && parsed.installed !== null
        ? (parsed.installed as Record<string, unknown>)
        : null;
    const installedProjectId = installed
      ? normalizeStringValue(installed.project_id ?? installed.projectId)
      : null;
    if (installedProjectId) return installedProjectId;

    const web =
      parsed.web && typeof parsed.web === 'object' && parsed.web !== null
        ? (parsed.web as Record<string, unknown>)
        : null;
    const webProjectId = web ? normalizeStringValue(web.project_id ?? web.projectId) : null;
    if (webProjectId) return webProjectId;
  } catch {
    return DEFAULT_ANTIGRAVITY_PROJECT_ID;
  }

  return DEFAULT_ANTIGRAVITY_PROJECT_ID;
};

const resolveResponseServerTimeOffsetMs = (
  header: Record<string, string[]> | undefined
): number | null => {
  if (!header) return null;
  const dateEntry = Object.entries(header).find(([key]) => key.toLowerCase() === 'date');
  const rawDate = dateEntry?.[1]?.[0];
  if (!rawDate) return null;
  const serverTime = new Date(rawDate).getTime();
  if (Number.isNaN(serverTime)) return null;
  return serverTime - Date.now();
};

export const fetchAntigravityQuota = async (
  file: AuthFileItem,
  t: TFunction,
  requestScope?: ApiClientRequestScope
): Promise<AntigravityQuotaData> => {
  const rawAuthIndex = file['auth_index'] ?? file.authIndex;
  const authIndex = normalizeAuthIndex(rawAuthIndex);
  if (!authIndex) {
    throw new Error(t('antigravity_quota.missing_auth_index'));
  }

  const projectId = await resolveAntigravityProjectId(file, requestScope);
  const requestBody = JSON.stringify({ project: projectId });
  const subscriptionPromise = fetchAntigravityQuotaSubscription(authIndex, requestScope);
  const requestConfig = requestScope ? createScopedApiRequestConfig(requestScope) : undefined;

  let lastError = '';
  let lastStatus: number | undefined;
  let priorityStatus: number | undefined;
  let hadSuccess = false;
  let quotaInventoryObserved = false;

  for (const url of [...ANTIGRAVITY_QUOTA_SUMMARY_URLS, ...ANTIGRAVITY_AVAILABLE_MODELS_URLS]) {
    try {
      const result = await apiCallApi.request(
        {
          authIndex,
          method: 'POST',
          url,
          header: { ...ANTIGRAVITY_REQUEST_HEADERS },
          data: requestBody,
        },
        requestConfig
      );

      if (result.statusCode < 200 || result.statusCode >= 300) {
        lastError = getApiCallErrorMessage(result);
        lastStatus = result.statusCode;
        if (result.statusCode === 403 || result.statusCode === 404) {
          priorityStatus ??= result.statusCode;
        }
        continue;
      }

      hadSuccess = true;
      const payload = parseAntigravityPayload(
        result.body ?? result.bodyText
      ) as AntigravityQuotaSummaryPayload | null;
      if (!payload) {
        lastError = t('antigravity_quota.empty_models');
        continue;
      }

      const groups = buildAntigravityQuotaGroups(payload);
      if (groups.length === 0) {
        quotaInventoryObserved ||= hasExplicitEmptyAntigravityInventory(payload);
        lastError = t('antigravity_quota.empty_models');
        continue;
      }

      return {
        groups,
        quotaInventoryObserved: true,
        subscription: await subscriptionPromise,
        serverTimeOffsetMs: resolveResponseServerTimeOffsetMs(result.header),
      };
    } catch (err: unknown) {
      lastError = err instanceof Error ? err.message : t('common.unknown_error');
      const status = getStatusFromError(err);
      if (status) {
        lastStatus = status;
        if (status === 403 || status === 404) {
          priorityStatus ??= status;
        }
      }
    }
  }

  if (hadSuccess) {
    return {
      groups: [],
      quotaInventoryObserved,
      subscription: await subscriptionPromise,
      serverTimeOffsetMs: null,
    };
  }

  throw createStatusError(lastError || t('common.unknown_error'), priorityStatus ?? lastStatus);
};

export const buildCodexQuotaWindows = (
  payload: CodexUsagePayload,
  t: TFunction,
  planType?: string | null,
  observedAtMs = Date.now(),
  source: CodexQuotaResetSource = 'provider_api',
  rateLimitScope?: CodexQuotaScopeResolution
): CodexQuotaWindow[] =>
  buildCodexQuotaWindowInfos(payload, { planType, observedAtMs, source, rateLimitScope }).map(
    (window) => ({
      id: window.id,
      label: t(window.labelKey, window.labelParams),
      labelKey: window.labelKey,
      labelParams: window.labelParams,
      usedPercent: window.usedPercent,
      resetLabel: window.resetLabel,
      resetAtMs: window.resetAtMs,
      resetAccuracy: window.resetAccuracy,
      limitWindowSeconds: window.limitWindowSeconds,
      observationSource: source === 'response_header' ? 'response_header' : 'api_query',
      observedAtMs,
      quotaProgressObservedAtMs:
        typeof window.usedPercent === 'number' && Number.isFinite(window.usedPercent)
          ? observedAtMs
          : null,
      modelScope: window.modelScope,
      providerWindowAliases: window.providerWindowAliases,
    })
  );

const resolveCodexRateLimitResetCreditsAvailableCount = (
  payload: CodexUsagePayload
): number | null => {
  const credits = payload.rate_limit_reset_credits ?? payload.rateLimitResetCredits;
  return normalizeNumberValue(credits?.available_count ?? credits?.availableCount);
};

const resolveCodexSubscriptionActiveUntil = (
  payload: CodexUsagePayload
): string | number | null => {
  const value = payload.subscription_active_until ?? payload.subscriptionActiveUntil;
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  return normalizeStringValue(value);
};

const normalizeBooleanValue = (value: unknown): boolean | null => {
  if (value === undefined || value === null) return null;
  if (typeof value === 'boolean') return value;
  if (typeof value === 'number') return value !== 0;
  if (typeof value === 'string') {
    const normalized = value.trim().toLowerCase();
    if (['true', '1', 'yes', 'y', 'on'].includes(normalized)) return true;
    if (['false', '0', 'no', 'n', 'off'].includes(normalized)) return false;
  }
  return null;
};

const resolveCodexCreditsInfo = (payload: CodexUsagePayload) => {
  const credits = payload.credits;
  return {
    creditsHasCredits: normalizeBooleanValue(credits?.has_credits ?? credits?.hasCredits),
    creditsUnlimited: normalizeBooleanValue(credits?.unlimited),
    creditsBalance: normalizeStringValue(credits?.balance),
    creditsOverageLimitReached: normalizeBooleanValue(
      credits?.overage_limit_reached ?? credits?.overageLimitReached
    ),
    creditsApproxLocalMessages: normalizeNumberValue(
      credits?.approx_local_messages ?? credits?.approxLocalMessages
    ),
    creditsApproxCloudMessages: normalizeNumberValue(
      credits?.approx_cloud_messages ?? credits?.approxCloudMessages
    ),
  };
};

const resolveCodexSpendControlInfo = (payload: CodexUsagePayload) => {
  const spendControl = payload.spend_control ?? payload.spendControl;
  return {
    spendControlReached: normalizeBooleanValue(spendControl?.reached),
    spendControlIndividualLimit: normalizeNumberValue(
      spendControl?.individual_limit ?? spendControl?.individualLimit
    ),
  };
};

type CodexResetCreditsData = {
  availableCount: number | null;
  credits: CodexRateLimitResetCredit[];
  error: string | null;
};

const resolveCodexResetCreditsAvailableCount = (
  resetCredits: CodexResetCreditsData,
  usageAvailableCount: number | null
): number | null => {
  if (resetCredits.availableCount !== null) return resetCredits.availableCount;
  if (resetCredits.credits.length > 0) return resetCredits.credits.length;
  return usageAvailableCount;
};

const fetchCodexResetCredits = async (
  authIndex: string,
  accountId: string | null | undefined,
  t: TFunction,
  requestConfig?: AxiosRequestConfig
): Promise<CodexResetCreditsData> => {
  try {
    const result = await apiCallApi.request(
      {
        authIndex,
        method: 'GET',
        url: CODEX_RATE_LIMIT_RESET_CREDITS_URL,
        header: buildCodexResetCreditsRequestHeaders(accountId),
      },
      { ...requestConfig, timeout: CODEX_RESET_CREDITS_REQUEST_TIMEOUT_MS }
    );

    if (result.statusCode < 200 || result.statusCode >= 300) {
      return {
        availableCount: null,
        credits: [],
        error: getApiCallErrorMessage(result),
      };
    }

    const payload = normalizeCodexResetCreditsPayload(result.body ?? result.bodyText);
    if (payload.invalidPayload) {
      return {
        availableCount: null,
        credits: [],
        error: t('codex_quota.reset_credits_invalid_payload'),
      };
    }

    return {
      availableCount: payload.availableCount,
      credits: payload.credits,
      error: null,
    };
  } catch (err: unknown) {
    return {
      availableCount: null,
      credits: [],
      error: err instanceof Error ? err.message : 'Failed to fetch Codex reset credits',
    };
  }
};

export const fetchCodexQuota = async (
  file: AuthFileItem,
  t: TFunction,
  requestScope?: ApiClientRequestScope
): Promise<CodexQuotaData> => {
  const rawAuthIndex = file['auth_index'] ?? file.authIndex;
  const authIndex = normalizeAuthIndex(rawAuthIndex);
  if (!authIndex) {
    throw new Error(t('codex_quota.missing_auth_index'));
  }

  const planTypeFromFile = resolveCodexPlanType(file);
  const accountId = resolveCodexChatgptAccountId(file);
  const requestConfig = requestScope ? createScopedApiRequestConfig(requestScope) : undefined;
  const result = await apiCallApi.request(
    {
      authIndex,
      method: 'GET',
      url: CODEX_USAGE_URL,
      header: buildCodexUsageRequestHeaders(accountId),
    },
    requestConfig
  );

  if (result.statusCode < 200 || result.statusCode >= 300) {
    throw createStatusError(getApiCallErrorMessage(result), result.statusCode);
  }

  const payload = parseCodexUsagePayload(result.body ?? result.bodyText);
  if (!payload) {
    throw new Error(t('codex_quota.empty_windows'));
  }

  const planTypeFromUsage = normalizePlanType(payload.plan_type ?? payload.planType);
  const planType = planTypeFromUsage ?? planTypeFromFile;
  const observedAtMs = Date.now();
  const windows = buildCodexQuotaWindows(payload, t, planType, observedAtMs);
  const usageResetCreditsAvailableCount = resolveCodexRateLimitResetCreditsAvailableCount(payload);
  const resetCredits = await fetchCodexResetCredits(authIndex, accountId, t, requestConfig);
  return {
    planType,
    windows,
    observedAtMs,
    quotaInventoryObserved: hasCodexQuotaInventory(payload),
    subscriptionActiveUntil: resolveCodexSubscriptionActiveUntil(payload),
    ...resolveCodexCreditsInfo(payload),
    ...resolveCodexSpendControlInfo(payload),
    rateLimitResetCreditsAvailableCount: resolveCodexResetCreditsAvailableCount(
      resetCredits,
      usageResetCreditsAvailableCount
    ),
    rateLimitResetCredits: resetCredits.credits,
    rateLimitResetCreditsError: resetCredits.error,
  };
};

const normalizeFlagValue = (value: unknown): boolean | undefined => {
  if (value === undefined || value === null) return undefined;
  if (typeof value === 'boolean') return value;
  if (typeof value === 'number') return value !== 0;
  if (typeof value === 'string') {
    const trimmed = value.trim().toLowerCase();
    if (['true', '1', 'yes', 'y', 'on'].includes(trimmed)) return true;
    if (['false', '0', 'no', 'n', 'off'].includes(trimmed)) return false;
  }
  return undefined;
};

const parseClaudeProfilePayload = (payload: unknown): ClaudeProfileResponse | null => {
  if (payload === undefined || payload === null) return null;
  if (typeof payload === 'string') {
    const trimmed = payload.trim();
    if (!trimmed) return null;
    try {
      return JSON.parse(trimmed) as ClaudeProfileResponse;
    } catch {
      return null;
    }
  }
  if (typeof payload === 'object') {
    return payload as ClaudeProfileResponse;
  }
  return null;
};

const resolveClaudePlanType = (profile: ClaudeProfileResponse | null): string | null => {
  if (!profile) return null;

  const hasClaudeMax = normalizeFlagValue(profile.account?.has_claude_max);
  if (hasClaudeMax) return 'plan_max';

  const hasClaudePro = normalizeFlagValue(profile.account?.has_claude_pro);
  if (hasClaudePro) return 'plan_pro';

  const organizationType = normalizeStringValue(
    profile.organization?.organization_type
  )?.toLowerCase();
  const subscriptionStatus = normalizeStringValue(
    profile.organization?.subscription_status
  )?.toLowerCase();

  if (organizationType === 'claude_team' && subscriptionStatus === 'active') {
    return 'plan_team';
  }

  if (hasClaudeMax === false && hasClaudePro === false) return 'plan_free';

  return null;
};

const normalizeClaudeLimitToken = (value: unknown): string | null => {
  const normalized = normalizeStringValue(value);
  if (!normalized) return null;
  return normalized
    .replace(/([a-z0-9])([A-Z])/g, '$1_$2')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '');
};

const isClaudeWeeklyScopedLimit = (limit: Record<string, unknown>): boolean => {
  const kind = normalizeClaudeLimitToken(limit.kind);
  const group = normalizeClaudeLimitToken(limit.group);
  if (group && group !== 'weekly') return false;
  if (kind === 'weekly_scoped' || kind === 'weekly_model_scoped') return true;
  return kind === 'model_scoped' && group === 'weekly';
};

type ClaudeBaseLimitWindowId = 'five-hour' | 'seven-day';

const resolveClaudeBaseLimitWindowId = (
  limit: Record<string, unknown>
): ClaudeBaseLimitWindowId | null => {
  const kind = normalizeClaudeLimitToken(limit.kind);
  const group = normalizeClaudeLimitToken(limit.group);

  if (kind === 'session' && (!group || group === 'session')) return 'five-hour';
  if (
    (kind === 'weekly' || kind === 'weekly_all') &&
    (!group || group === 'weekly' || group === 'weekly_all')
  ) {
    return 'seven-day';
  }
  return null;
};

type ClaudeLimitWindowValues = Pick<
  ClaudeQuotaWindow,
  'usedPercent' | 'resetLabel' | 'resetAtMs' | 'resetAccuracy'
>;

const resolveClaudeLimitResetAt = (limit: Record<string, unknown>): string | number | null => {
  const rawResetAt = limit.resets_at ?? limit.resetsAt ?? limit.reset_at ?? limit.resetAt;
  if (typeof rawResetAt === 'string') return rawResetAt.trim() || null;
  return typeof rawResetAt === 'number' && Number.isFinite(rawResetAt) ? rawResetAt : null;
};

const resolveClaudeLimitResetRank = (limit: Record<string, unknown>): number => {
  return resolveAbsoluteQuotaReset(resolveClaudeLimitResetAt(limit)).resetAtMs ?? -1;
};

const parseClaudeLimitWindowValues = (
  limit: Record<string, unknown>
): ClaudeLimitWindowValues | null => {
  const rawPercent = normalizeNumberValue(limit.percent ?? limit.utilization);
  const usedPercent = rawPercent !== null && rawPercent >= 0 ? rawPercent : null;
  const resetAt = resolveClaudeLimitResetAt(limit);
  const reset = resolveAbsoluteQuotaReset(resetAt);
  const resetLabel = formatQuotaResetTime(resetAt);
  if (usedPercent === null && resetLabel === '-') return null;
  return {
    usedPercent,
    resetLabel,
    resetAtMs: reset.resetAtMs,
    resetAccuracy: reset.resetAccuracy,
  };
};

const findClaudeModelDisplayName = (value: unknown): string | null => {
  if (!isRecord(value)) return null;

  const readDisplayName = (candidate: Record<string, unknown>): string | null => {
    const rawDisplayName = candidate.display_name ?? candidate.displayName ?? candidate.name;
    if (typeof rawDisplayName !== 'string') return null;
    const normalized = rawDisplayName.trim().replace(/\s+/g, ' ');
    return normalized || null;
  };

  const direct = readDisplayName(value);
  if (direct) return direct;

  const details = isRecord(value.details) ? value.details : null;
  if (details) {
    return readDisplayName(details);
  }
  return null;
};

const normalizeClaudeIdentityText = (value: string): string => {
  try {
    return value.normalize('NFKC');
  } catch {
    return value;
  }
};

const encodeClaudeWindowIdPart = (value: string): string => {
  try {
    return encodeURIComponent(value);
  } catch {
    const codeUnits = Array.from({ length: value.length }, (_, index) =>
      value.charCodeAt(index).toString(16).padStart(4, '0')
    );
    return `utf16-${codeUnits.join('-')}`;
  }
};

type ClaudeScopedWeeklyWindowEntry = {
  activityRank: number;
  identityKey: string;
  modelId: string | null;
  resetAtRank: number;
  sortLabel: string;
  usedPercentRank: number;
  window: ClaudeQuotaWindow;
};

const hasValidClaudeReset = (entry: ClaudeScopedWeeklyWindowEntry): boolean =>
  entry.resetAtRank >= 0;

const getClaudeScopedCompletenessRank = (entry: ClaudeScopedWeeklyWindowEntry): number =>
  (entry.usedPercentRank >= 0 ? 1 : 0) + (hasValidClaudeReset(entry) ? 1 : 0);

const shouldReplaceClaudeScopedWindow = (
  existing: ClaudeScopedWeeklyWindowEntry,
  candidate: ClaudeScopedWeeklyWindowEntry
): boolean => {
  if (
    hasValidClaudeReset(existing) &&
    hasValidClaudeReset(candidate) &&
    candidate.resetAtRank !== existing.resetAtRank
  ) {
    return candidate.resetAtRank > existing.resetAtRank;
  }
  if (candidate.activityRank !== existing.activityRank) {
    return candidate.activityRank > existing.activityRank;
  }
  const candidateCompleteness = getClaudeScopedCompletenessRank(candidate);
  const existingCompleteness = getClaudeScopedCompletenessRank(existing);
  if (candidateCompleteness !== existingCompleteness) {
    return candidateCompleteness > existingCompleteness;
  }
  if (hasValidClaudeReset(existing) !== hasValidClaudeReset(candidate)) {
    return hasValidClaudeReset(candidate);
  }
  if (candidate.resetAtRank !== existing.resetAtRank) {
    return candidate.resetAtRank > existing.resetAtRank;
  }
  return candidate.usedPercentRank > existing.usedPercentRank;
};

const areEquivalentClaudeScopedWindows = (
  left: ClaudeScopedWeeklyWindowEntry,
  right: ClaudeScopedWeeklyWindowEntry
): boolean =>
  left.activityRank === right.activityRank &&
  left.resetAtRank === right.resetAtRank &&
  left.usedPercentRank === right.usedPercentRank;

type ClaudeBaseLimitCandidate = {
  completenessRank: number;
  kindRank: number;
  resetAtRank: number;
  usedPercentRank: number;
  values: ClaudeLimitWindowValues;
};

const shouldReplaceClaudeBaseLimit = (
  existing: ClaudeBaseLimitCandidate,
  candidate: ClaudeBaseLimitCandidate
): boolean => {
  if (candidate.resetAtRank !== existing.resetAtRank) {
    return candidate.resetAtRank > existing.resetAtRank;
  }
  if (candidate.completenessRank !== existing.completenessRank) {
    return candidate.completenessRank > existing.completenessRank;
  }
  if (candidate.kindRank !== existing.kindRank) {
    return candidate.kindRank > existing.kindRank;
  }
  return candidate.usedPercentRank > existing.usedPercentRank;
};

const buildClaudeBaseLimitFallbacks = (
  payload: ClaudeUsagePayload
): Map<ClaudeBaseLimitWindowId, ClaudeLimitWindowValues> => {
  const candidates = new Map<ClaudeBaseLimitWindowId, ClaudeBaseLimitCandidate>();
  if (!Array.isArray(payload.limits)) return new Map();

  for (const rawLimit of payload.limits) {
    try {
      if (!isRecord(rawLimit)) continue;
      if (normalizeFlagValue(rawLimit.is_active ?? rawLimit.isActive) === false) continue;
      if (rawLimit.scope !== undefined && rawLimit.scope !== null) continue;

      const windowId = resolveClaudeBaseLimitWindowId(rawLimit);
      if (!windowId) continue;
      const values = parseClaudeLimitWindowValues(rawLimit);
      if (!values) continue;
      const kind = normalizeClaudeLimitToken(rawLimit.kind);
      const candidate: ClaudeBaseLimitCandidate = {
        completenessRank:
          (values.usedPercent !== null ? 1 : 0) + (values.resetAtMs !== null ? 1 : 0),
        kindRank: windowId === 'seven-day' && kind === 'weekly_all' ? 1 : 0,
        resetAtRank: resolveClaudeLimitResetRank(rawLimit),
        usedPercentRank: values.usedPercent ?? -1,
        values,
      };
      const existing = candidates.get(windowId);
      if (existing && !shouldReplaceClaudeBaseLimit(existing, candidate)) continue;
      candidates.set(windowId, candidate);
    } catch {
      continue;
    }
  }

  return new Map(
    [...candidates.entries()].map(([windowId, candidate]) => [windowId, candidate.values])
  );
};

const buildClaudeScopedWeeklyWindows = (payload: ClaudeUsagePayload): ClaudeQuotaWindow[] => {
  if (!Array.isArray(payload.limits)) return [];

  const idWindowsByModel = new Map<string, ClaudeScopedWeeklyWindowEntry>();
  const labelOnlyWindowsByModel = new Map<string, ClaudeScopedWeeklyWindowEntry>();
  const idKeysByLabel = new Map<string, Set<string>>();
  for (const rawLimit of payload.limits) {
    try {
      if (!isRecord(rawLimit) || !isClaudeWeeklyScopedLimit(rawLimit)) continue;

      const scope = isRecord(rawLimit.scope) ? rawLimit.scope : null;
      const model = isRecord(scope?.model) ? scope.model : null;
      if (!model) continue;
      const label = findClaudeModelDisplayName(model);
      if (!label) continue;

      const values = parseClaudeLimitWindowValues(rawLimit);
      if (!values) continue;

      const rawModelId = model.id ?? model.model_id ?? model.modelId;
      const modelId =
        typeof rawModelId === 'string' && rawModelId.trim()
          ? normalizeClaudeIdentityText(rawModelId.trim())
          : null;
      const labelKey = normalizeClaudeIdentityText(label).toLowerCase();
      const identityKey = modelId ? `id:${modelId}` : `label:${labelKey}`;
      const activeFlag = normalizeFlagValue(rawLimit.is_active ?? rawLimit.isActive);
      const activityRank = activeFlag === true ? 2 : activeFlag === undefined ? 1 : 0;

      const idPart = modelId
        ? `id-${encodeClaudeWindowIdPart(modelId)}`
        : encodeClaudeWindowIdPart(labelKey);
      const candidate: ClaudeScopedWeeklyWindowEntry = {
        activityRank,
        identityKey,
        modelId,
        resetAtRank: resolveClaudeLimitResetRank(rawLimit),
        sortLabel: labelKey,
        usedPercentRank: values.usedPercent ?? -1,
        window: {
          id: `weekly-scoped-${idPart}`,
          label,
          limitWindowSeconds: 7 * 24 * 60 * 60,
          modelScope: {
            kind: 'models',
            models: modelId ? [modelId] : [],
            complete: Boolean(modelId),
          },
          ...values,
        },
      };
      const targetMap = modelId ? idWindowsByModel : labelOnlyWindowsByModel;
      if (modelId) {
        const idKeys = idKeysByLabel.get(labelKey) ?? new Set<string>();
        idKeys.add(identityKey);
        idKeysByLabel.set(labelKey, idKeys);
      }
      const existing = targetMap.get(identityKey);
      if (existing && !shouldReplaceClaudeScopedWindow(existing, candidate)) continue;
      targetMap.set(identityKey, candidate);
    } catch {
      continue;
    }
  }

  for (const labelEntry of labelOnlyWindowsByModel.values()) {
    const matchingIdKeys = idKeysByLabel.get(labelEntry.sortLabel);
    if (matchingIdKeys?.size !== 1) continue;
    const identityKey = matchingIdKeys.values().next().value;
    if (!identityKey) continue;
    const idEntry = idWindowsByModel.get(identityKey);
    if (!idEntry) continue;
    if (areEquivalentClaudeScopedWindows(idEntry, labelEntry)) {
      labelOnlyWindowsByModel.delete(labelEntry.identityKey);
    }
  }

  const entries = [...idWindowsByModel.values(), ...labelOnlyWindowsByModel.values()];
  const labelCounts = new Map<string, number>();
  for (const entry of entries) {
    labelCounts.set(entry.sortLabel, (labelCounts.get(entry.sortLabel) ?? 0) + 1);
  }

  return entries
    .sort((left, right) => {
      if (left.sortLabel !== right.sortLabel) {
        return left.sortLabel < right.sortLabel ? -1 : 1;
      }
      return left.identityKey < right.identityKey
        ? -1
        : left.identityKey > right.identityKey
          ? 1
          : 0;
    })
    .map(({ sortLabel, modelId, window }) => {
      if (!modelId || (labelCounts.get(sortLabel) ?? 0) < 2) return window;
      return { ...window, label: `${window.label} (${modelId})` };
    });
};

const buildClaudeQuotaWindows = (
  payload: ClaudeUsagePayload,
  t: TFunction
): ClaudeQuotaWindow[] => {
  const windows: ClaudeQuotaWindow[] = [];
  const baseLimitFallbacks = buildClaudeBaseLimitFallbacks(payload);
  const scopedWeeklyWindows = buildClaudeScopedWeeklyWindows(payload);

  for (const { key, id, labelKey } of CLAUDE_USAGE_WINDOW_KEYS) {
    const window = payload[key as keyof ClaudeUsagePayload];
    let renderedTopLevelWindow = false;
    if (window && typeof window === 'object' && 'utilization' in window) {
      const typedWindow = window as { utilization: number; resets_at: string };
      const usedPercent = normalizeNumberValue(typedWindow.utilization);
      const reset = resolveAbsoluteQuotaReset(typedWindow.resets_at);
      const resetLabel = formatQuotaResetTime(typedWindow.resets_at);
      if (usedPercent !== null || resetLabel !== '-') {
        windows.push({
          id,
          label: t(labelKey),
          labelKey,
          usedPercent,
          resetLabel,
          resetAtMs: reset.resetAtMs,
          resetAccuracy: reset.resetAccuracy,
          limitWindowSeconds: id === 'five-hour' ? 5 * 60 * 60 : 7 * 24 * 60 * 60,
          modelScope: { kind: 'all', complete: true },
        });
        renderedTopLevelWindow = true;
      }
    }
    if (!renderedTopLevelWindow && (id === 'five-hour' || id === 'seven-day')) {
      const fallback = baseLimitFallbacks.get(id);
      if (fallback) {
        windows.push({
          id,
          label: t(labelKey),
          labelKey,
          ...fallback,
          limitWindowSeconds: id === 'five-hour' ? 5 * 60 * 60 : 7 * 24 * 60 * 60,
          modelScope: { kind: 'all', complete: true },
        });
      }
    }
    if (key === 'seven_day') {
      windows.push(...scopedWeeklyWindows);
    }
  }

  return windows;
};

const hasClaudeQuotaInventory = (
  payload: ClaudeUsagePayload,
  windows: ClaudeQuotaWindow[]
): boolean => {
  if (windows.length > 0) return true;
  if (Array.isArray(payload.limits) && payload.limits.length === 0) return true;

  return CLAUDE_USAGE_WINDOW_KEYS.some(({ key }) =>
    hasOwn(payload, key) ? payload[key as keyof ClaudeUsagePayload] === null : false
  );
};

export const fetchClaudeQuota = async (
  file: AuthFileItem,
  t: TFunction,
  requestScope?: ApiClientRequestScope
): Promise<ClaudeQuotaData> => {
  const rawAuthIndex = file['auth_index'] ?? file.authIndex;
  const authIndex = normalizeAuthIndex(rawAuthIndex);
  if (!authIndex) {
    throw new Error(t('claude_quota.missing_auth_index'));
  }

  const requestConfig = requestScope ? createScopedApiRequestConfig(requestScope) : undefined;
  const [usageResult, profileResult] = await Promise.allSettled([
    apiCallApi.request(
      {
        authIndex,
        method: 'GET',
        url: CLAUDE_USAGE_URL,
        header: { ...CLAUDE_REQUEST_HEADERS },
      },
      requestConfig
    ),
    apiCallApi.request(
      {
        authIndex,
        method: 'GET',
        url: CLAUDE_PROFILE_URL,
        header: { ...CLAUDE_REQUEST_HEADERS },
      },
      requestConfig
    ),
  ]);

  if (usageResult.status === 'rejected') {
    throw usageResult.reason;
  }

  const result = usageResult.value;

  if (result.statusCode < 200 || result.statusCode >= 300) {
    throw createStatusError(getApiCallErrorMessage(result), result.statusCode);
  }

  const payload = parseClaudeUsagePayload(result.body ?? result.bodyText);
  if (!payload) {
    throw new Error(t('claude_quota.empty_windows'));
  }

  const windows = buildClaudeQuotaWindows(payload, t);
  const planType =
    profileResult.status === 'fulfilled' &&
    profileResult.value.statusCode >= 200 &&
    profileResult.value.statusCode < 300
      ? resolveClaudePlanType(
          parseClaudeProfilePayload(profileResult.value.body ?? profileResult.value.bodyText)
        )
      : null;

  return {
    windows,
    quotaInventoryObserved: hasClaudeQuotaInventory(payload, windows),
    extraUsage: payload.extra_usage,
    planType,
  };
};

const hasKimiQuotaInventory = (payload: KimiUsagePayload, rows: KimiQuotaRow[]): boolean => {
  if (rows.length > 0) return true;
  if (Array.isArray(payload.limits) && payload.limits.length === 0) return true;
  if (Array.isArray(payload.usages) && payload.usages.length === 0) return true;
  return hasOwn(payload, 'usage') && payload.usage === null;
};

export const fetchKimiQuota = async (
  file: AuthFileItem,
  t: TFunction,
  requestScope?: ApiClientRequestScope
): Promise<KimiQuotaData> => {
  const rawAuthIndex = file['auth_index'] ?? file.authIndex;
  const authIndex = normalizeAuthIndex(rawAuthIndex);
  if (!authIndex) {
    throw new Error(t('kimi_quota.missing_auth_index'));
  }

  const result = await apiCallApi.request(
    {
      authIndex,
      method: 'GET',
      url: KIMI_USAGE_URL,
      header: { ...KIMI_REQUEST_HEADERS },
    },
    requestScope ? createScopedApiRequestConfig(requestScope) : undefined
  );

  if (result.statusCode < 200 || result.statusCode >= 300) {
    throw createStatusError(getApiCallErrorMessage(result), result.statusCode);
  }

  const payload = parseKimiUsagePayload(result.body ?? result.bodyText);
  if (!payload) {
    throw new Error(t('kimi_quota.empty_data'));
  }

  const rows = buildKimiQuotaRows(payload, { observedAtMs: Date.now() });
  return {
    rows,
    quotaInventoryObserved: hasKimiQuotaInventory(payload, rows),
  };
};

const normalizeXaiCentValue = (value: unknown): number | null => {
  if (value === undefined || value === null) return null;
  if (typeof value === 'object' && !Array.isArray(value)) {
    const record = value as { val?: unknown; value?: unknown };
    if (Object.keys(record).length === 0) return null;
    return normalizeNumberValue(record.val) ?? normalizeNumberValue(record.value);
  }
  return normalizeNumberValue(value);
};

const resolveXaiCentCandidate = (...values: unknown[]) => {
  for (const value of values) {
    const normalized = normalizeXaiCentValue(value);
    if (normalized !== null) {
      return { value: normalized, hasEvidence: true };
    }
  }
  return { value: null, hasEvidence: false };
};

const normalizeXaiPeriodTimestamp = (value: unknown): string | undefined =>
  normalizeStringValue(value) ?? undefined;

const hasValidXaiPeriodWindow = (start?: string, end?: string): boolean => {
  if (!start || !end) return false;
  const startMs = Date.parse(start);
  const endMs = Date.parse(end);
  return Number.isFinite(startMs) && Number.isFinite(endMs) && endMs > startMs;
};

const resolveXaiBillingConfig = (payload: XaiBillingPayload | null): XaiBillingConfig | null => {
  if (!payload || typeof payload !== 'object') return null;
  return payload.config ?? (payload as XaiBillingConfig);
};

const resolveXaiPeriodType = (period?: XaiBillingPeriod | null): XaiBillingPeriodType => {
  const rawType = normalizeStringValue(period?.type)?.toLowerCase() ?? '';
  if (rawType.includes('weekly')) return 'weekly';
  if (rawType.includes('monthly')) return 'monthly';
  return 'unknown';
};

const normalizeXaiProductUsage = (
  productUsage: XaiBillingConfig['productUsage'],
  fallbackPrefix: string
): XaiProductUsageSummary[] => {
  if (!Array.isArray(productUsage)) return [];

  return productUsage
    .map((item, index): XaiProductUsageSummary | null => {
      if (!item || typeof item !== 'object') return null;
      const product = normalizeStringValue(item.product) ?? `${fallbackPrefix} ${index + 1}`;
      const usagePercent = normalizeNumberValue(item.usagePercent ?? item.usage_percent);
      return { product, usagePercent };
    })
    .filter((item): item is XaiProductUsageSummary => item !== null);
};

const emptyXaiBillingSummary = (): XaiBillingSummary => ({
  periodType: 'unknown',
  usagePercent: null,
  productUsage: [],
  monthlyLimitCents: null,
  usedCents: null,
  includedUsedCents: null,
  onDemandCapCents: null,
  onDemandUsedCents: null,
  onDemandUsedPercent: null,
  usedPercent: null,
});

type XaiBillingFieldEvidence = {
  monthlyLimit: boolean;
  used: boolean;
  includedUsed: boolean;
  onDemandCap: boolean;
  onDemandUsed: boolean;
};

const xaiBillingFieldEvidence = new WeakMap<XaiBillingSummary, XaiBillingFieldEvidence>();

const getXaiBillingFieldEvidence = (summary: XaiBillingSummary): XaiBillingFieldEvidence =>
  xaiBillingFieldEvidence.get(summary) ?? {
    monthlyLimit: summary.monthlyLimitCents !== null,
    used: summary.usedCents !== null,
    includedUsed: summary.includedUsedCents !== null,
    onDemandCap: summary.onDemandCapCents !== null,
    onDemandUsed: summary.onDemandUsedCents !== null,
  };

export const buildXaiBillingSummary = (
  config: XaiBillingConfig | null | undefined
): XaiBillingSummary | null => {
  if (!config || typeof config !== 'object') return null;

  const summary = emptyXaiBillingSummary();
  const currentPeriod = config.currentPeriod ?? config.current_period ?? null;
  const periodType = resolveXaiPeriodType(currentPeriod);
  const rawCreditUsagePercent = normalizeNumberValue(
    config.creditUsagePercent ?? config.credit_usage_percent
  );
  const periodStart = normalizeXaiPeriodTimestamp(currentPeriod?.start);
  const periodEnd = normalizeXaiPeriodTimestamp(currentPeriod?.end);
  const creditUsagePercent =
    rawCreditUsagePercent ??
    (periodType === 'weekly' && hasValidXaiPeriodWindow(periodStart, periodEnd) ? 0 : null);
  const billingCycle = config.billingCycle ?? config.billing_cycle ?? null;
  const nestedUsage = config.usage ?? null;
  const productUsage = normalizeXaiProductUsage(
    config.productUsage ?? config.product_usage,
    'Product'
  );
  const monthlyLimit = resolveXaiCentCandidate(config.monthlyLimit, config.monthly_limit);
  const nestedIncludedUsed = resolveXaiCentCandidate(
    nestedUsage?.includedUsed,
    nestedUsage?.included_used
  );
  const explicitOnDemandUsed = resolveXaiCentCandidate(
    config.onDemandUsed,
    config.on_demand_used,
    nestedUsage?.onDemandUsed,
    nestedUsage?.on_demand_used
  );
  const rawUsed = resolveXaiCentCandidate(
    config.used,
    nestedUsage?.totalUsed,
    nestedUsage?.total_used
  );
  const onDemandCap = resolveXaiCentCandidate(config.onDemandCap, config.on_demand_cap);
  const monthlyLimitCents = monthlyLimit.value;
  const nestedIncludedUsedCents = nestedIncludedUsed.value;
  const explicitOnDemandUsedCents = explicitOnDemandUsed.value;
  const rawUsedCents = rawUsed.value;
  const usedCents =
    rawUsedCents ??
    (nestedIncludedUsedCents !== null || explicitOnDemandUsedCents !== null
      ? (nestedIncludedUsedCents ?? 0) + (explicitOnDemandUsedCents ?? 0)
      : null);
  const onDemandCapCents = onDemandCap.value;
  const billingPeriodStart =
    normalizeStringValue(
      config.billingPeriodStart ??
        config.billing_period_start ??
        billingCycle?.billingPeriodStart ??
        billingCycle?.billing_period_start
    ) ?? undefined;
  const billingPeriodEnd =
    normalizeStringValue(
      config.billingPeriodEnd ??
        config.billing_period_end ??
        billingCycle?.billingPeriodEnd ??
        billingCycle?.billing_period_end
    ) ?? undefined;

  const hasMonthlyData =
    monthlyLimit.hasEvidence || rawUsed.hasEvidence || nestedIncludedUsed.hasEvidence;
  const includedUsedCents = hasMonthlyData
    ? (nestedIncludedUsedCents ??
      (usedCents === null
        ? null
        : monthlyLimitCents !== null && monthlyLimitCents > 0
          ? Math.min(usedCents, monthlyLimitCents)
          : usedCents))
    : null;
  const derivedOnDemandUsedCents =
    usedCents !== null && monthlyLimitCents !== null
      ? Math.max(0, usedCents - monthlyLimitCents)
      : null;
  const onDemandUsedCents = explicitOnDemandUsedCents ?? derivedOnDemandUsedCents;
  const usedPercent =
    monthlyLimitCents !== null && monthlyLimitCents > 0 && includedUsedCents !== null
      ? (includedUsedCents / monthlyLimitCents) * 100
      : null;
  const onDemandUsedPercent =
    onDemandCapCents !== null && onDemandCapCents > 0 && onDemandUsedCents !== null
      ? (onDemandUsedCents / onDemandCapCents) * 100
      : null;

  const hasWeeklyData =
    creditUsagePercent !== null || periodType === 'weekly' || productUsage.length > 0;
  const hasOnDemandData =
    onDemandCap.hasEvidence ||
    explicitOnDemandUsed.hasEvidence ||
    (derivedOnDemandUsedCents !== null && derivedOnDemandUsedCents > 0);
  const hasBillingPeriodData = hasMonthlyData || hasOnDemandData;

  if (!hasWeeklyData && !hasMonthlyData && !hasOnDemandData) return null;

  summary.periodType = hasWeeklyData
    ? periodType === 'unknown'
      ? 'weekly'
      : periodType
    : hasMonthlyData
      ? 'monthly'
      : 'unknown';
  summary.usagePercent = hasWeeklyData ? creditUsagePercent : usedPercent;
  summary.periodStart = hasWeeklyData ? periodStart : billingPeriodStart;
  summary.periodEnd = hasWeeklyData ? periodEnd : billingPeriodEnd;
  summary.productUsage = productUsage;
  summary.monthlyLimitCents = hasMonthlyData ? monthlyLimitCents : null;
  summary.usedCents = hasBillingPeriodData ? usedCents : null;
  summary.includedUsedCents = includedUsedCents;
  summary.onDemandCapCents = hasOnDemandData ? onDemandCapCents : null;
  summary.onDemandUsedCents = hasOnDemandData ? onDemandUsedCents : null;
  summary.onDemandUsedPercent = hasOnDemandData ? onDemandUsedPercent : null;
  summary.billingPeriodStart = hasBillingPeriodData ? billingPeriodStart : undefined;
  summary.billingPeriodEnd = hasBillingPeriodData ? billingPeriodEnd : undefined;
  summary.usedPercent = usedPercent;
  xaiBillingFieldEvidence.set(summary, {
    monthlyLimit: monthlyLimit.hasEvidence,
    used: rawUsed.hasEvidence,
    includedUsed: nestedIncludedUsed.hasEvidence,
    onDemandCap: onDemandCap.hasEvidence,
    onDemandUsed: explicitOnDemandUsed.hasEvidence,
  });

  return summary;
};

export const mergeXaiBillingSummaries = (
  primary: XaiBillingSummary | null,
  fallback: XaiBillingSummary | null
): XaiBillingSummary | null => {
  if (!primary) return fallback;
  if (!fallback) return primary;

  const primaryHasWeeklyData =
    primary.periodType === 'weekly' ||
    primary.usagePercent !== null ||
    primary.productUsage.length > 0;
  const weeklySource = primaryHasWeeklyData ? primary : fallback;
  const primaryEvidence = getXaiBillingFieldEvidence(primary);
  const fallbackEvidence = getXaiBillingFieldEvidence(fallback);
  const primaryMonthlyLimit = primaryEvidence.monthlyLimit ? primary.monthlyLimitCents : null;
  const fallbackMonthlyLimit = fallbackEvidence.monthlyLimit ? fallback.monthlyLimitCents : null;
  const monthlyLimitCents = primaryMonthlyLimit ?? fallbackMonthlyLimit;
  const primaryRawUsed = primaryEvidence.used ? primary.usedCents : null;
  const fallbackRawUsed = fallbackEvidence.used ? fallback.usedCents : null;
  let includedUsedHasEvidence = primaryEvidence.includedUsed;
  let includedUsedCents = includedUsedHasEvidence ? primary.includedUsedCents : null;
  if (includedUsedCents === null && primaryRawUsed !== null) {
    includedUsedCents = primaryRawUsed;
  }
  if (includedUsedCents === null && fallbackEvidence.includedUsed) {
    includedUsedCents = fallback.includedUsedCents;
    includedUsedHasEvidence = includedUsedCents !== null;
  }
  if (includedUsedCents === null && fallbackRawUsed !== null) {
    includedUsedCents = fallbackRawUsed;
  }
  if (includedUsedCents !== null && monthlyLimitCents !== null && monthlyLimitCents > 0) {
    includedUsedCents = Math.min(includedUsedCents, monthlyLimitCents);
  }

  const primaryOnDemandCap = primaryEvidence.onDemandCap ? primary.onDemandCapCents : null;
  const fallbackOnDemandCap = fallbackEvidence.onDemandCap ? fallback.onDemandCapCents : null;
  const onDemandCapCents = primaryOnDemandCap ?? fallbackOnDemandCap;
  let onDemandUsedHasEvidence = primaryEvidence.onDemandUsed;
  let onDemandUsedCents = onDemandUsedHasEvidence ? primary.onDemandUsedCents : null;
  if (onDemandUsedCents === null && primaryRawUsed !== null && monthlyLimitCents !== null) {
    onDemandUsedCents = Math.max(0, primaryRawUsed - monthlyLimitCents);
  }
  if (onDemandUsedCents === null && fallbackEvidence.onDemandUsed) {
    onDemandUsedCents = fallback.onDemandUsedCents;
    onDemandUsedHasEvidence = onDemandUsedCents !== null;
  }
  if (onDemandUsedCents === null && fallbackRawUsed !== null && monthlyLimitCents !== null) {
    onDemandUsedCents = Math.max(0, fallbackRawUsed - monthlyLimitCents);
  }
  const hasPrimaryComponentEvidence = primaryEvidence.includedUsed || primaryEvidence.onDemandUsed;
  const usedCents =
    primaryRawUsed ??
    (hasPrimaryComponentEvidence
      ? includedUsedCents !== null && onDemandUsedCents !== null
        ? includedUsedCents + onDemandUsedCents
        : (includedUsedCents ?? onDemandUsedCents)
      : (fallbackRawUsed ??
        (includedUsedCents !== null && onDemandUsedCents !== null
          ? includedUsedCents + onDemandUsedCents
          : (includedUsedCents ?? onDemandUsedCents))));
  const usedPercent =
    monthlyLimitCents !== null && monthlyLimitCents > 0 && includedUsedCents !== null
      ? (includedUsedCents / monthlyLimitCents) * 100
      : (primary.usedPercent ?? fallback.usedPercent);
  const onDemandUsedPercent =
    onDemandCapCents !== null && onDemandCapCents > 0 && onDemandUsedCents !== null
      ? (onDemandUsedCents / onDemandCapCents) * 100
      : (primary.onDemandUsedPercent ?? fallback.onDemandUsedPercent);

  const merged: XaiBillingSummary = {
    periodType: weeklySource.periodType,
    usagePercent: weeklySource.usagePercent,
    periodStart: weeklySource.periodStart,
    periodEnd: weeklySource.periodEnd,
    productUsage: weeklySource.productUsage,
    monthlyLimitCents,
    usedCents,
    includedUsedCents,
    onDemandCapCents,
    onDemandUsedCents,
    onDemandUsedPercent,
    billingPeriodStart: primary.billingPeriodStart ?? fallback.billingPeriodStart,
    billingPeriodEnd: primary.billingPeriodEnd ?? fallback.billingPeriodEnd,
    usedPercent,
  };
  xaiBillingFieldEvidence.set(merged, {
    monthlyLimit: primaryEvidence.monthlyLimit || fallbackEvidence.monthlyLimit,
    used: primaryEvidence.used || (!hasPrimaryComponentEvidence && fallbackEvidence.used),
    includedUsed: includedUsedHasEvidence,
    onDemandCap: primaryEvidence.onDemandCap || fallbackEvidence.onDemandCap,
    onDemandUsed: onDemandUsedHasEvidence,
  });
  return merged;
};

const toXaiRecord = (value: unknown): Record<string, unknown> | null =>
  value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;

const normalizeXaiBoolean = (value: unknown): boolean | null => {
  if (typeof value === 'boolean') return value;
  if (typeof value !== 'string') return null;
  const normalized = value.trim().toLowerCase();
  if (normalized === 'true') return true;
  if (normalized === 'false') return false;
  return null;
};

const resolveXaiUserId = (file: AuthFileItem): string | null => {
  const metadata = toXaiRecord(file.metadata);
  const attributes = toXaiRecord(file.attributes);
  const oauth = toXaiRecord(file.oauth ?? metadata?.oauth ?? attributes?.oauth);
  const user = toXaiRecord(file.user ?? metadata?.user ?? attributes?.user);

  const candidates = [
    file.sub,
    file.subject,
    file.user_id,
    file.userId,
    metadata?.sub,
    metadata?.subject,
    metadata?.user_id,
    metadata?.userId,
    attributes?.sub,
    attributes?.subject,
    attributes?.user_id,
    attributes?.userId,
    oauth?.sub,
    oauth?.subject,
    user?.sub,
    user?.id,
  ];

  for (const candidate of candidates) {
    const userId = normalizeStringValue(candidate);
    if (userId) return userId;
  }

  return null;
};

const buildXaiRequestHeaders = (file: AuthFileItem): Record<string, string> => {
  const headers: Record<string, string> = { ...XAI_REQUEST_HEADERS };
  const userId = resolveXaiUserId(file);
  if (userId) {
    headers['x-userid'] = userId;
  }
  return headers;
};

const readXaiAuthString = (file: AuthFileItem, ...keys: string[]) => {
  const metadata = toXaiRecord(file.metadata);
  const attributes = toXaiRecord(file.attributes);
  for (const record of [file, metadata, attributes]) {
    if (!record) continue;
    for (const key of keys) {
      const value = normalizeStringValue(record[key]);
      if (value) return value;
    }
  }
  return '';
};

const readXaiAuthBoolean = (file: AuthFileItem, ...keys: string[]): boolean | null => {
  const metadata = toXaiRecord(file.metadata);
  const attributes = toXaiRecord(file.attributes);
  for (const record of [file, metadata, attributes]) {
    if (!record) continue;
    for (const key of keys) {
      const value = record[key];
      if (typeof value === 'boolean') return value;
      if (typeof value === 'string') {
        const normalized = value.trim().toLowerCase();
        if (normalized === 'true') return true;
        if (normalized === 'false') return false;
      }
    }
  }
  return null;
};

const sameXaiBaseUrl = (left: string, right: string) =>
  left.trim().replace(/\/+$/, '').toLowerCase() === right.trim().replace(/\/+$/, '').toLowerCase();

export type XaiInferenceRouteMode = 'auto' | 'official';

const resolveXaiInferenceRequest = (
  file: AuthFileItem,
  userAgent?: string,
  routeMode: XaiInferenceRouteMode = 'auto'
) => {
  const configuredBaseUrl = readXaiAuthString(file, 'base_url', 'baseUrl').replace(/\/+$/, '');
  const usingApi = readXaiAuthBoolean(file, 'using_api', 'usingApi');
  const authKind = readXaiAuthString(file, 'auth_kind', 'authKind').toLowerCase();
  // xAI OAuth/CLI credentials may omit auth_kind and using_api from the
  // management auth-files listing. Keep those credentials on the CLI proxy;
  // API credentials must opt in explicitly with using_api=true or api_key.
  const resolvedUsingApi = usingApi ?? (authKind ? authKind !== 'oauth' : false);
  const usesCliChatProxy =
    routeMode !== 'official' &&
    !resolvedUsingApi &&
    (!configuredBaseUrl || sameXaiBaseUrl(configuredBaseUrl, XAI_OFFICIAL_API_BASE_URL));
  const baseUrl =
    routeMode === 'official'
      ? XAI_OFFICIAL_API_BASE_URL
      : usesCliChatProxy
        ? XAI_CLI_CHAT_PROXY_BASE_URL
        : configuredBaseUrl || XAI_OFFICIAL_API_BASE_URL;
  const header: Record<string, string> = {
    Authorization: 'Bearer $TOKEN$',
    Accept: 'application/json',
    'Content-Type': 'application/json',
    'User-Agent': normalizeStringValue(userAgent) || XAI_INFERENCE_USER_AGENT,
  };
  const targetsCliChatProxy =
    usesCliChatProxy || sameXaiBaseUrl(baseUrl, XAI_CLI_CHAT_PROXY_BASE_URL);
  if (targetsCliChatProxy) {
    header['x-xai-token-auth'] = 'xai-grok-cli';
    header['x-grok-client-version'] = XAI_GROK_CLIENT_VERSION;
    const userId = resolveXaiUserId(file);
    if (userId) header['x-userid'] = userId;
  }
  return { url: `${baseUrl}/responses`, header };
};

const requestXaiBilling = async (
  authIndex: string,
  url: string,
  header: Record<string, string>,
  requestConfig?: AxiosRequestConfig
): Promise<{ summary: XaiBillingSummary; statusCode: number | null } | null> => {
  const result = await apiCallApi.request(
    {
      authIndex,
      method: 'GET',
      url,
      header,
    },
    requestConfig
  );

  if (result.statusCode < 200 || result.statusCode >= 300) {
    const envelope = parseXaiErrorEnvelope({
      statusCode: result.hasStatusCode ? result.statusCode : null,
      body: result.body,
      bodyText: result.bodyText,
      headers: result.header,
    });
    const decision = classifyXaiProbe({ surface: 'billing', envelope });
    throw new XaiProbeError(getApiCallErrorMessage(result), envelope, decision);
  }

  const payload = parseXaiBillingPayload(result.body ?? result.bodyText);
  const summary = buildXaiBillingSummary(resolveXaiBillingConfig(payload));
  if (!summary) {
    const envelope = parseXaiErrorEnvelope({
      statusCode: result.hasStatusCode ? result.statusCode : null,
      body: result.body,
      bodyText: result.bodyText,
      headers: result.header,
    });
    const decision = classifyXaiProbe({ surface: 'billing', envelope, hasPayload: false });
    throw new XaiProbeError('xAI billing response schema changed', envelope, decision);
  }
  return {
    summary,
    statusCode: result.hasStatusCode ? result.statusCode : null,
  };
};

const requestXaiOfficialApiHealth = async (
  authIndex: string,
  requestConfig?: AxiosRequestConfig
): Promise<{ health: XaiOfficialApiHealth; statusCode: number | null }> => {
  const result = await apiCallApi.request(
    {
      authIndex,
      method: 'GET',
      url: XAI_OFFICIAL_API_ME_URL,
      header: {
        Authorization: 'Bearer $TOKEN$',
        accept: 'application/json',
      },
    },
    requestConfig
  );
  const payload = toXaiRecord(result.body);

  if (result.statusCode < 200 || result.statusCode >= 300) {
    const envelope = parseXaiErrorEnvelope({
      statusCode: result.hasStatusCode ? result.statusCode : null,
      body: result.body,
      bodyText: result.bodyText,
      headers: result.header,
    });
    const decision = classifyXaiProbe({ surface: 'oauth', envelope });
    throw new XaiProbeError(getApiCallErrorMessage(result), envelope, decision);
  }

  const userId = normalizeStringValue(payload?.user_id ?? payload?.userId);
  const teamId = normalizeStringValue(payload?.team_id ?? payload?.teamId);
  const teamBlocked = normalizeXaiBoolean(payload?.team_blocked ?? payload?.teamBlocked);
  if (!userId && !teamId && teamBlocked === null) {
    const envelope = parseXaiErrorEnvelope({
      statusCode: result.hasStatusCode ? result.statusCode : null,
      body: result.body,
      bodyText: result.bodyText,
      headers: result.header,
    });
    const decision = classifyXaiProbe({ surface: 'oauth', envelope, hasPayload: false });
    throw new XaiProbeError(
      'xAI official API identity response schema changed',
      envelope,
      decision
    );
  }
  if (teamBlocked === true) {
    const body = { ...payload, code: 'personal-team-blocked:spending-limit' };
    const envelope = parseXaiErrorEnvelope({ statusCode: 403, body });
    const decision = classifyXaiProbe({ surface: 'oauth', envelope });
    throw new XaiProbeError('xAI official API team is blocked', envelope, decision);
  }

  return {
    health: {
      source: 'api.x.ai/v1/me',
      userId,
      teamId,
      teamBlocked,
    },
    statusCode: result.hasStatusCode ? result.statusCode : null,
  };
};

export interface XaiBillingProbeResult {
  summary: XaiBillingSummary;
  failures: unknown[];
  partial: boolean;
  statusCode?: number | null;
  blockingFailure?: unknown;
}

export interface XaiQuotaProbeResult extends XaiBillingProbeResult {
  source: 'billing' | 'official-api';
}

const xaiFailurePriority = (failure: unknown) => {
  if (!(failure instanceof XaiProbeError)) return 0;
  switch (failure.decision.classification) {
    case 'auth_invalid':
      return 100;
    case 'free_quota_exhausted':
    case 'spending_limit':
      return 90;
    case 'entitlement_denied':
      return 85;
    case 'client_outdated':
      return 80;
    case 'permission_unknown':
    case 'quota_or_entitlement_unknown':
      return 70;
    case 'policy_denied':
      return 60;
    case 'rate_limited':
      return 40;
    case 'probe_invalid':
      return 30;
    case 'upstream_error':
      return 10;
    default:
      return 1;
  }
};

const selectXaiBillingFailure = (failures: unknown[]) =>
  failures.reduce<unknown>(
    (selected, failure) =>
      xaiFailurePriority(failure) > xaiFailurePriority(selected) ? failure : selected,
    failures[0]
  );

const isXaiOfficialApiFallbackFailure = (failure: unknown): boolean =>
  failure instanceof XaiProbeError && failure.decision.classification === 'permission_unknown';

const isXaiBlockingBillingFailure = (failure: unknown): boolean => {
  if (!(failure instanceof XaiProbeError)) return false;
  return ![
    'upstream_error',
    'rate_limited',
    'probe_invalid',
    'model_unavailable',
    'protocol_changed',
  ].includes(failure.decision.classification);
};

const selectXaiBlockingBillingFailure = (failures: unknown[]) => {
  const blockingFailures = failures.filter(isXaiBlockingBillingFailure);
  return blockingFailures.length > 0 ? selectXaiBillingFailure(blockingFailures) : undefined;
};

const resolveXaiProbeAuthIndex = (file: AuthFileItem, t: TFunction): string => {
  const rawAuthIndex = file['auth_index'] ?? file.authIndex;
  const authIndex = normalizeAuthIndex(rawAuthIndex);
  if (!authIndex) {
    throw new Error(t('xai_quota.missing_auth_index'));
  }
  return authIndex;
};

export interface XaiInferenceProbeResult {
  statusCode: number;
}

export interface XaiInferenceProbeOptions {
  model?: string;
  prompt?: string;
  userAgent?: string;
  routeMode?: XaiInferenceRouteMode;
}

const hasCompletedXaiInferenceOutput = (value: unknown): boolean => {
  const response = toXaiRecord(value);
  if (!response || normalizeStringValue(response.status)?.toLowerCase() !== 'completed') {
    return false;
  }
  if (response.error !== undefined && response.error !== null) return false;
  if (!Array.isArray(response.output)) return false;
  return response.output.some((rawOutput) => {
    const output = toXaiRecord(rawOutput);
    if (!output || normalizeStringValue(output.type)?.toLowerCase() !== 'message') return false;
    if (!Array.isArray(output.content)) return false;
    return output.content.some((rawContent) => {
      const content = toXaiRecord(rawContent);
      return (
        normalizeStringValue(content?.type)?.toLowerCase() === 'output_text' &&
        Boolean(normalizeStringValue(content?.text))
      );
    });
  });
};

export const probeXaiInference = async (
  file: AuthFileItem,
  t: TFunction,
  requestConfig?: AxiosRequestConfig,
  options?: XaiInferenceProbeOptions
): Promise<XaiInferenceProbeResult> => {
  const authIndex = resolveXaiProbeAuthIndex(file, t);
  const { url, header } = resolveXaiInferenceRequest(file, options?.userAgent, options?.routeMode);
  const result = await apiCallApi.request(
    {
      authIndex,
      method: 'POST',
      url,
      header,
      data: JSON.stringify({
        model: normalizeStringValue(options?.model) || DEFAULT_XAI_INSPECTION_MODEL,
        input: normalizeStringValue(options?.prompt) || DEFAULT_XAI_INSPECTION_PROMPT,
        stream: false,
      }),
    },
    requestConfig
  );
  const envelope = parseXaiErrorEnvelope({
    statusCode: result.hasStatusCode ? result.statusCode : null,
    body: result.body,
    bodyText: result.bodyText,
    headers: result.header,
  });
  if (!result.hasStatusCode) {
    const decision = {
      ...classifyXaiProbe({ surface: 'inference', envelope, hasPayload: false }),
      classification: 'protocol_changed' as const,
      suggestedAction: 'keep' as const,
      reasonCode: 'xai_protocol_changed',
      confidence: 'verified' as const,
      needsReview: true,
    };
    throw new XaiProbeError('xAI inference response missing status_code', envelope, decision);
  }
  if (result.statusCode < 200 || result.statusCode >= 300) {
    const decision = classifyXaiProbe({ surface: 'inference', envelope });
    throw new XaiProbeError(getApiCallErrorMessage(result), envelope, decision);
  }
  if (!hasCompletedXaiInferenceOutput(result.body)) {
    const classified = classifyXaiProbe({ surface: 'inference', envelope, hasPayload: false });
    const decision = {
      ...classified,
      classification:
        classified.classification === 'unknown'
          ? ('protocol_changed' as const)
          : classified.classification,
      suggestedAction: 'keep' as const,
      reasonCode:
        classified.classification === 'unknown' ? 'xai_protocol_changed' : classified.reasonCode,
      confidence: 'verified' as const,
      needsReview: true,
    };
    throw new XaiProbeError(
      'xAI inference response did not contain completed output',
      envelope,
      decision
    );
  }
  return { statusCode: result.statusCode };
};

const requestXaiBillingProbe = async (
  file: AuthFileItem,
  t: TFunction,
  requestConfig?: AxiosRequestConfig
) => {
  const authIndex = resolveXaiProbeAuthIndex(file, t);
  const requestHeader = buildXaiRequestHeaders(file);
  const configuredTimeout = requestConfig?.timeout;
  const legacyRequestConfig: AxiosRequestConfig = {
    ...requestConfig,
    timeout:
      typeof configuredTimeout === 'number' && configuredTimeout > 0
        ? Math.min(configuredTimeout, XAI_LEGACY_BILLING_REQUEST_TIMEOUT_MS)
        : XAI_LEGACY_BILLING_REQUEST_TIMEOUT_MS,
  };
  const [weeklyResult, monthlyResult] = await Promise.allSettled([
    requestXaiBilling(authIndex, XAI_BILLING_WEEKLY_URL, requestHeader, requestConfig),
    requestXaiBilling(authIndex, XAI_BILLING_MONTHLY_URL, requestHeader, legacyRequestConfig),
  ]);
  const weeklyProbe = weeklyResult.status === 'fulfilled' ? weeklyResult.value : null;
  const monthlyProbe = monthlyResult.status === 'fulfilled' ? monthlyResult.value : null;
  const weeklySummary = weeklyProbe?.summary ?? null;
  const monthlySummary = monthlyProbe?.summary ?? null;
  const weeklyFailures = weeklyResult.status === 'rejected' ? [weeklyResult.reason] : [];
  const monthlyFailures = monthlyResult.status === 'rejected' ? [monthlyResult.reason] : [];
  const weeklyFailure = weeklyFailures[0];
  const failures = weeklySummary ? [] : weeklyFailure ? [weeklyFailure] : monthlyFailures;

  return {
    authIndex,
    weeklySummary,
    monthlySummary,
    failures,
    summary: mergeXaiBillingSummaries(weeklySummary, monthlySummary),
    statusCode: weeklyProbe?.statusCode ?? monthlyProbe?.statusCode ?? null,
  };
};

export const probeXaiBilling = async (
  file: AuthFileItem,
  t: TFunction,
  requestConfig?: AxiosRequestConfig
): Promise<XaiBillingProbeResult> => {
  const { failures, statusCode, summary, weeklySummary } = await requestXaiBillingProbe(
    file,
    t,
    requestConfig
  );
  if (!summary) {
    if (failures.length > 0) throw selectXaiBillingFailure(failures);
    throw new Error(t('xai_quota.empty_data'));
  }

  return {
    summary,
    failures,
    partial: weeklySummary === null,
    statusCode,
  };
};

export const probeXaiQuota = async (
  file: AuthFileItem,
  t: TFunction,
  requestConfig?: AxiosRequestConfig
): Promise<XaiQuotaProbeResult> => {
  const { authIndex, failures, statusCode, summary, weeklySummary } = await requestXaiBillingProbe(
    file,
    t,
    requestConfig
  );
  if (summary) {
    return {
      summary,
      failures,
      partial: weeklySummary === null,
      source: 'billing',
      statusCode,
      blockingFailure:
        weeklySummary === null ? selectXaiBlockingBillingFailure(failures) : undefined,
    };
  }
  if (failures.length === 0) {
    throw new Error(t('xai_quota.empty_data'));
  }
  const canFallbackToOfficialApi = failures.every(isXaiOfficialApiFallbackFailure);
  if (!canFallbackToOfficialApi) {
    throw selectXaiBillingFailure(failures);
  }

  try {
    const officialApiResult = await requestXaiOfficialApiHealth(authIndex, requestConfig);
    return {
      summary: { ...emptyXaiBillingSummary(), officialApiHealth: officialApiResult.health },
      failures: [],
      partial: false,
      source: 'official-api',
      statusCode: officialApiResult.statusCode,
    };
  } catch (error) {
    throw selectXaiBillingFailure([...failures, error]);
  }
};

export const fetchXaiQuota = async (
  file: AuthFileItem,
  t: TFunction,
  requestScope?: ApiClientRequestScope
): Promise<XaiBillingSummary> =>
  probeXaiQuota(
    file,
    t,
    requestScope ? createScopedApiRequestConfig(requestScope) : undefined
  ).then(({ summary, partial, failures }) => ({
    ...summary,
    partial,
    diagnostics: failures.map((failure): XaiBillingDiagnostic => {
      if (failure instanceof XaiProbeError) {
        return {
          classification: failure.decision.classification,
          statusCode: failure.envelope.statusCode,
          message: failure.message,
        };
      }
      return {
        classification: 'unknown',
        statusCode: null,
        message: failure instanceof Error ? failure.message : String(failure),
      };
    }),
  }));
