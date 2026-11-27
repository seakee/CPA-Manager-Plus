/**
 * 配置状态管理
 * 从原项目 src/core/config-service.js 迁移
 */

import { create } from 'zustand';
import type { Config } from '@/types';
import type { RawConfigSection } from '@/types/config';
import { CONFIG_SECTION_KEYS, extractConfigSectionValue } from '@/entities/config/sections';
import { configApi } from '@/services/api/config';
import { providersApi } from '@/services/api/providers';
import { CACHE_EXPIRY_MS } from '@/utils/constants';

interface ConfigCache {
  data: unknown;
  timestamp: number;
}

export interface ConfigFetchOptions {
  maxAgeMs?: number;
  silent?: boolean;
}

export class ConfigRequestSupersededError extends Error {
  readonly code = 'CONFIG_REQUEST_SUPERSEDED' as const;

  constructor(message = 'Config request was superseded') {
    super(message);
    this.name = 'ConfigRequestSupersededError';
  }
}

export function isConfigRequestSupersededError(
  error: unknown
): error is ConfigRequestSupersededError {
  return (
    error instanceof ConfigRequestSupersededError ||
    (typeof error === 'object' &&
      error !== null &&
      (error as { code?: unknown }).code === 'CONFIG_REQUEST_SUPERSEDED')
  );
}

interface ConfigState {
  config: Config | null;
  cache: Map<string, ConfigCache>;
  loading: boolean;
  error: string | null;

  // 操作
  fetchConfig: {
    (section?: undefined, forceRefresh?: boolean, options?: ConfigFetchOptions): Promise<Config>;
    (
      section: RawConfigSection,
      forceRefresh?: boolean,
      options?: ConfigFetchOptions
    ): Promise<unknown>;
  };
  updateConfigValue: (section: RawConfigSection, value: unknown) => void;
  clearCache: (section?: RawConfigSection) => void;
  isCacheValid: (section?: RawConfigSection, maxAgeMs?: number) => boolean;
}

const PROVIDER_CONFIG_SECTION_LIST: readonly RawConfigSection[] = [
  'gemini-api-key',
  'interactions-api-key',
  'codex-api-key',
  'xai-api-key',
  'claude-api-key',
  'vertex-api-key',
  'meta-api-key',
  'openai-compatibility',
];

const PROVIDER_CONFIG_SECTIONS: Partial<Record<RawConfigSection, true>> = {
  'gemini-api-key': true,
  'interactions-api-key': true,
  'codex-api-key': true,
  'xai-api-key': true,
  'claude-api-key': true,
  'vertex-api-key': true,
  'meta-api-key': true,
  'openai-compatibility': true,
};

let configResetEpoch = 0;
let configRequestToken = 0;
let lastSettledRequestId = 0;
let inFlightConfigRequest: {
  id: number;
  resetEpoch: number;
  exposeStatus: boolean;
  promise: Promise<Config>;
} | null = null;
export const useConfigStore = create<ConfigState>((set, get) => ({
  config: null,
  cache: new Map(),
  loading: false,
  error: null,

  fetchConfig: (async (
    section?: RawConfigSection,
    forceRefresh: boolean = false,
    options?: ConfigFetchOptions
  ) => {
    const { cache, isCacheValid } = get();
    const maxAgeMs = options?.maxAgeMs;
    const silent = Boolean(options?.silent) && !forceRefresh;

    // 检查缓存
    const cacheKey = section || '__full__';
    if (!forceRefresh && isCacheValid(section, maxAgeMs)) {
      const cached = cache.get(cacheKey);
      if (cached) {
        return cached.data;
      }
    }

    // section 缓存未命中但 full 缓存可用时，直接复用已获取到的配置，避免重复 /config 请求
    if (!forceRefresh && section && isCacheValid(undefined, maxAgeMs)) {
      const fullCached = cache.get('__full__');
      if (fullCached?.data) {
        return extractConfigSectionValue(fullCached.data as Config, section);
      }
    }

    // 同一时刻合并多个 /config 请求（如 StrictMode 或多个页面同时触发）；强制刷新会绕过旧请求并在拉取期间保留现有 config 避免页面闪烁
    if (
      !forceRefresh &&
      inFlightConfigRequest &&
      inFlightConfigRequest.resetEpoch === configResetEpoch
    ) {
      if (!silent && !inFlightConfigRequest.exposeStatus) {
        inFlightConfigRequest.exposeStatus = true;
        set({ loading: true, error: null });
      }
      const data = await inFlightConfigRequest.promise;
      return section ? extractConfigSectionValue(data, section) : data;
    }

    if (forceRefresh) {
      inFlightConfigRequest = null;
      set({ cache: new Map(), loading: true, error: null });
    } else if (!silent) {
      set({ loading: true, error: null });
    }
    const resetEpochAtStart = configResetEpoch;
    const requestId = (configRequestToken += 1);
    const startedSilent = silent;
    const v8WriteVersionBefore = providersApi?.getV8EtagMutationVersion?.();
    const tracker = {
      id: requestId,
      resetEpoch: resetEpochAtStart,
      exposeStatus: !silent,
      promise: null as unknown as Promise<Config>,
    };
    const resolveSupersession = (): Promise<Config> | Config | null => {
      if (resetEpochAtStart !== configResetEpoch) {
        throw new ConfigRequestSupersededError('Config store reset during fetch');
      }
      if (requestId !== configRequestToken) {
        if (
          inFlightConfigRequest &&
          inFlightConfigRequest.resetEpoch === resetEpochAtStart &&
          inFlightConfigRequest.id === configRequestToken
        ) {
          return inFlightConfigRequest.promise;
        }
        const settledConfig = get().config;
        if (lastSettledRequestId > requestId && settledConfig !== null) {
          return settledConfig;
        }
        throw new ConfigRequestSupersededError('Config request invalidated during fetch');
      }
      return null;
    };
    const settledPromise = (async (): Promise<Config> => {
      try {
        const data = await configApi.getConfig({ syncLatestV8Etag: false });
        const superseded = resolveSupersession();
        if (superseded !== null) {
          return await superseded;
        }
        const now = Date.now();

        const v8VersionMatches =
          v8WriteVersionBefore === undefined ||
          v8WriteVersionBefore === providersApi?.getV8EtagMutationVersion?.();
        if (!v8VersionMatches && startedSilent) {
          if (tracker.exposeStatus) {
            return await get().fetchConfig(undefined, true);
          }
          throw new ConfigRequestSupersededError('Silent config revalidation superseded by v8 write');
        }

        if (v8VersionMatches && (data.v8Supported !== undefined || data.v8Etag !== undefined)) {
          providersApi?.setLatestV8ConfigEtag?.(
            data.v8Supported === false ? undefined : data.v8Etag,
            { passiveRead: true }
          );
        }
        // 更新缓存
        const newCache = new Map(get().cache);
        newCache.set('__full__', { data, timestamp: now });
        CONFIG_SECTION_KEYS.forEach((key) => {
          const value = extractConfigSectionValue(data, key);
          if (value !== undefined) {
            newCache.set(key, { data: value, timestamp: now });
          }
        });

        const supersededBeforePublish = resolveSupersession();
        if (supersededBeforePublish !== null) {
          return await supersededBeforePublish;
        }
        lastSettledRequestId = requestId;
        set({
          config: data,
          cache: newCache,
          loading: false,
          error: null,
        });

        return data;
      } catch (error: unknown) {
        if (!isConfigRequestSupersededError(error)) {
          const supersededOnError = resolveSupersession();
          if (supersededOnError !== null) {
            return await supersededOnError;
          }
        }
        if (isConfigRequestSupersededError(error)) {
          if (requestId === configRequestToken && resetEpochAtStart === configResetEpoch) {
            set({ loading: false });
          }
          throw error;
        }
        const message =
          error instanceof Error
            ? error.message
            : typeof error === 'string'
              ? error
              : 'Failed to fetch config';
        if (
          requestId === configRequestToken &&
          resetEpochAtStart === configResetEpoch &&
          tracker.exposeStatus
        ) {
          set({
            error: message || 'Failed to fetch config',
            loading: false,
          });
        }
        throw error;
      } finally {
        if (inFlightConfigRequest?.id === requestId) {
          inFlightConfigRequest = null;
        }
      }
    })();
    tracker.promise = settledPromise;
    inFlightConfigRequest = tracker;
    const settledData = await settledPromise;
    return section ? extractConfigSectionValue(settledData, section) : settledData;
  }) as ConfigState['fetchConfig'],

  updateConfigValue: (section, value) => {
    set((state) => {
      const raw = { ...(state.config?.raw || {}) };
      raw[section] = value;
      const nextConfig: Config = { ...(state.config || {}), raw };

      switch (section) {
        case 'debug':
          nextConfig.debug = value as Config['debug'];
          break;
        case 'proxy-url':
          nextConfig.proxyUrl = value as Config['proxyUrl'];
          break;
        case 'request-retry':
          nextConfig.requestRetry = value as Config['requestRetry'];
          break;
        case 'quota-exceeded':
          nextConfig.quotaExceeded = value as Config['quotaExceeded'];
          break;
        case 'request-log':
          nextConfig.requestLog = value as Config['requestLog'];
          break;
        case 'logging-to-file':
          nextConfig.loggingToFile = value as Config['loggingToFile'];
          break;
        case 'plugins':
          nextConfig.pluginsEnabled =
            value !== null &&
            typeof value === 'object' &&
            !Array.isArray(value) &&
            (value as { enabled?: unknown }).enabled === true;
          break;
        case 'logs-max-total-size-mb':
          nextConfig.logsMaxTotalSizeMb = value as Config['logsMaxTotalSizeMb'];
          break;
        case 'error-logs-max-files':
          nextConfig.errorLogsMaxFiles = value as Config['errorLogsMaxFiles'];
          break;
        case 'ws-auth':
          nextConfig.wsAuth = value as Config['wsAuth'];
          break;
        case 'force-model-prefix':
          nextConfig.forceModelPrefix = value as Config['forceModelPrefix'];
          break;
        case 'routing/strategy':
          nextConfig.routingStrategy = value as Config['routingStrategy'];
          break;
        case 'api-keys':
          nextConfig.apiKeys = value as Config['apiKeys'];
          break;
        case 'gemini-api-key':
          nextConfig.geminiApiKeys = value as Config['geminiApiKeys'];
          break;
        case 'interactions-api-key':
          nextConfig.interactionsApiKeys = value as Config['interactionsApiKeys'];
          break;
        case 'codex-api-key':
          nextConfig.codexApiKeys = value as Config['codexApiKeys'];
          break;
        case 'xai-api-key':
          nextConfig.xaiApiKeys = value as Config['xaiApiKeys'];
          break;
        case 'claude-api-key':
          nextConfig.claudeApiKeys = value as Config['claudeApiKeys'];
          break;
        case 'vertex-api-key':
          nextConfig.vertexApiKeys = value as Config['vertexApiKeys'];
          break;
        case 'meta-api-key':
          nextConfig.metaApiKeys = value as Config['metaApiKeys'];
          break;
        case 'openai-compatibility':
          nextConfig.openaiCompatibility = value as Config['openaiCompatibility'];
          break;
        case 'oauth-excluded-models':
          nextConfig.oauthExcludedModels = value as Config['oauthExcludedModels'];
          break;
        default:
          break;
      }

      return { config: nextConfig };
    });

    // 清除该 section 的缓存
    get().clearCache(section);
  },

  clearCache: (section) => {
    const { cache } = get();
    const newCache = new Map(cache);

    if (section) {
      newCache.delete(section);
      // 同时清除完整配置缓存以及所有提供商 section 缓存，防止兄弟家族保留过期的 v8Source.etag
      newCache.delete('__full__');
      PROVIDER_CONFIG_SECTION_LIST.forEach((providerSection) => {
        newCache.delete(providerSection);
      });
      if (!PROVIDER_CONFIG_SECTIONS[section]) {
        providersApi?.setLatestV8ConfigEtag?.(undefined);
      }

      // Section-level invalidation usually follows an optimistic write path. Invalidate any in-flight
      // full fetch so stale responses can't overwrite newer local changes.
      configRequestToken += 1;
      inFlightConfigRequest = null;

      set({ cache: newCache, loading: false, error: null });
      return;
    } else {
      newCache.clear();
      providersApi?.setLatestV8ConfigEtag?.(undefined);
    }

    // 清除全部缓存一般代表“切换连接/登出/全量刷新”，需要让 in-flight 的旧请求失效
    configResetEpoch += 1;
    configRequestToken += 1;
    lastSettledRequestId = 0;
    inFlightConfigRequest = null;
    set({ config: null, cache: newCache, loading: false, error: null });
  },

  isCacheValid: (section, maxAgeMs = CACHE_EXPIRY_MS) => {
    const { cache } = get();
    const cacheKey = section || '__full__';
    const cached = cache.get(cacheKey);

    if (!cached) return false;

    return Date.now() - cached.timestamp < maxAgeMs;
  },
}));
