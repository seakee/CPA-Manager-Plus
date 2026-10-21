import { AxiosHeaders, type AxiosRequestConfig } from 'axios';
import type { AuthFileItem, XaiSubscriptionInfo } from '@/types';
import { apiCallApi } from '@/services/api/apiCall';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { XAI_CLI_CHAT_PROXY_BASE_URL, XAI_REQUEST_HEADERS } from './constants';
import { createKeyedSerialTaskQueue } from './providerRefreshScheduler';
import { getQuotaCredentialStoreKey } from './credentialScope';

const SUBSCRIPTION_TIMEOUT_MS = 8000;
// One optional lane may overlap the regular quota lane; it never adds a second
// background provider request, including across independently-triggered batches.
const subscriptionQueue = createKeyedSerialTaskQueue();
// Lookups for the same credential are coalesced by pendingSubscriptions below,
// not by the queue key. Each new lookup gets its own queue task id so that a
// superseded (aborted or too old) lookup cannot swallow the one replacing it.
let nextSubscriptionTaskId = 0;
interface PendingSubscription {
  consumers: Set<() => boolean>;
  controller: AbortController;
  startedAtMs?: number;
  promise: Promise<XaiSubscriptionInfo | null>;
}
const pendingSubscriptions = new Map<string, PendingSubscription>();

const subscriptionKey = (file: AuthFileItem, config?: AxiosRequestConfig): string | null => {
  // Without a captured connection, a global client may have changed underneath us.
  if (
    !config?.baseURL ||
    !('cpampScopedRequest' in config) ||
    !config.cpampScopedRequest ||
    config.cancelToken
  )
    return null;
  const headers = AxiosHeaders.from(config.headers as AxiosHeaders | undefined).toJSON();
  // This key exists only while pending, is never logged, and distinguishes keys
  // for the same CPA URL as well as replaced/refreshed credentials.
  return JSON.stringify([
    config.baseURL,
    Object.keys(headers)
      .sort()
      .map((key) => [key, headers[key]]),
    getQuotaCredentialStoreKey(file),
    normalizeAuthIndex(file.authIndex ?? file['auth_index']),
    ...[
      'lastRefresh',
      'last_refresh',
      'lastRefreshedAt',
      'last_refreshed_at',
      'updatedAtMs',
      'updated_at_ms',
      'updatedAt',
      'updated_at',
      'modified',
      'modtime',
    ].map((key) => file[key] ?? null),
  ]);
};
const PLANS: Readonly<Record<string, string>> = {
  free: 'Free',
  grokpro: 'SuperGrok',
  supergrok: 'SuperGrok',
  supergrokpro: 'SuperGrok Heavy',
  supergrokheavy: 'SuperGrok Heavy',
  supergroklite: 'SuperGrok Lite',
  supergrokplus: 'SuperGrok Plus',
  xbasic: 'X Basic',
  xpremium: 'X Premium',
  xpremiumplus: 'X Premium+',
  'xpremium+': 'X Premium+',
};

const normalizePlan = (value: unknown): string | null => {
  if (typeof value !== 'string') return null;
  const key = value
    .trim()
    .toLowerCase()
    .replace(/[\s_-]+/g, '');
  return Object.prototype.hasOwnProperty.call(PLANS, key) ? PLANS[key] : null;
};

const readRecord = (body: unknown): Record<string, unknown> | null => {
  try {
    const value = typeof body === 'string' ? JSON.parse(body) : body;
    return value && typeof value === 'object' && !Array.isArray(value) ? value : null;
  } catch {
    return null;
  }
};

/** Optional display metadata: never probe inference or fail a successful quota refresh. */
const lookupSubscription = async (
  file: AuthFileItem,
  requestConfig: AxiosRequestConfig | undefined,
  isCurrent: () => boolean
): Promise<XaiSubscriptionInfo | null> => {
  const authIndex = normalizeAuthIndex(file.authIndex ?? file['auth_index']);
  if (!authIndex || requestConfig?.signal?.aborted || !isCurrent()) return null;
  const configuredTimeout = requestConfig?.timeout;
  const config: AxiosRequestConfig = {
    ...requestConfig,
    timeout:
      configuredTimeout && configuredTimeout > 0
        ? Math.min(configuredTimeout, SUBSCRIPTION_TIMEOUT_MS)
        : SUBSCRIPTION_TIMEOUT_MS,
  };
  const request = async (path: string) => {
    const observedAtMs = Date.now();
    const result = await apiCallApi.request(
      {
        authIndex,
        method: 'GET',
        url: `${XAI_CLI_CHAT_PROXY_BASE_URL}${path}`,
        header: { ...XAI_REQUEST_HEADERS },
      },
      config
    );
    if (!result.hasStatusCode || result.statusCode < 200 || result.statusCode >= 300) return null;
    const record = readRecord(result.body ?? result.bodyText);
    return record ? { record, observedAtMs } : null;
  };
  // The live user endpoint wins over settings, whose label can reflect an older JWT.
  // A present null/unknown live tier must not resurrect an old paid label.
  for (const [source, path, keys] of [
    ['user', '/user?include=subscription', ['subscriptionTier', 'subscription_tier']],
    ['settings', '/settings', ['subscription_tier_display', 'subscriptionTierDisplay']],
  ] as const) {
    if (requestConfig?.signal?.aborted || !isCurrent()) return null;
    const response = await request(path).catch(() => null);
    if (requestConfig?.signal?.aborted || !isCurrent()) return null;
    if (!response) continue;
    const key = keys.find((candidate) =>
      Object.prototype.hasOwnProperty.call(response.record, candidate)
    );
    if (!key) continue;
    return {
      planType: normalizePlan(response.record[key]),
      source,
      observedAtMs: response.observedAtMs,
    };
  }
  return null;
};

/** Share scoped in-flight work without sharing a caller's lifetime or stale evidence. */
export const fetchXaiSubscription = async (
  file: AuthFileItem,
  requestConfig?: AxiosRequestConfig,
  isCurrent: () => boolean = () => true,
  minimumObservedAtMs = 0
): Promise<XaiSubscriptionInfo | null> => {
  const consumerIsCurrent = () => !requestConfig?.signal?.aborted && isCurrent();
  if (!consumerIsCurrent()) return null;
  const key = subscriptionKey(file, requestConfig);
  let pending = key ? pendingSubscriptions.get(key) : undefined;
  // A request started before the new quota snapshot cannot confirm its plan.
  // Equal millisecond timestamps are ambiguous, so only share if strictly newer.
  if (
    pending?.controller.signal.aborted ||
    (pending?.startedAtMs !== undefined && pending.startedAtMs <= minimumObservedAtMs)
  ) {
    pending = undefined;
  }
  if (!pending) {
    const consumers = new Set<() => boolean>();
    const controller = new AbortController();
    const entry: PendingSubscription = {
      consumers,
      controller,
      promise: Promise.resolve(null),
    };
    let result: XaiSubscriptionInfo | null = null;
    const anyCurrent = () => [...consumers].some((current) => current());
    entry.promise = subscriptionQueue
      .run(`xai-subscription-${nextSubscriptionTaskId++}`, async () => {
        try {
          if (!anyCurrent()) return;
          entry.startedAtMs = Date.now();
          result = await lookupSubscription(
            file,
            // Shared work has an independent cancellation signal. An unscoped
            // request has just one consumer and retains its original signal.
            key ? { ...requestConfig, signal: controller.signal } : requestConfig,
            anyCurrent
          );
        } finally {
          // Remove even skipped work before exposing its completion to other
          // microtasks, so a current consumer cannot join a finished lookup.
          if (key && pendingSubscriptions.get(key) === entry) pendingSubscriptions.delete(key);
        }
      })
      .then(() => result);
    pending = entry;
    if (key) pendingSubscriptions.set(key, entry);
  }
  const entry = pending;
  entry.consumers.add(consumerIsCurrent);
  const onAbort = () => {
    if (![...entry.consumers].some((current) => current())) entry.controller.abort();
  };
  requestConfig?.signal?.addEventListener?.('abort', onAbort);
  try {
    const result = await entry.promise;
    return consumerIsCurrent() && result && result.observedAtMs >= minimumObservedAtMs
      ? result
      : null;
  } finally {
    entry.consumers.delete(consumerIsCurrent);
    requestConfig?.signal?.removeEventListener?.('abort', onAbort);
  }
};
