import { beforeEach, describe, expect, it, vi } from 'vitest';
import { apiClient } from '@/services/api/client';
import { configApi } from '@/services/api/config';
import type { Config } from '@/types';
import { providersApi } from '@/services/api/providers';
import { useConfigStore } from './useConfigStore';

vi.mock('@/services/api/config', () => ({
  configApi: {
    getConfig: vi.fn(),
  },
}));

const deferred = <T>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
};

describe('useConfigStore', () => {
  beforeEach(() => {
    useConfigStore.getState().clearCache();
    vi.mocked(configApi.getConfig).mockReset();
  });

  it('retains settled config during forceRefresh and supersedes older in-flight reads', async () => {
    const initialConfig: Config = {
      debug: false,
      usageStatisticsEnabled: true,
      loggingToFile: true,
    };
    const refreshedConfig: Config = {
      debug: true,
      usageStatisticsEnabled: true,
      loggingToFile: true,
    };

    vi.mocked(configApi.getConfig).mockResolvedValueOnce(initialConfig);
    await useConfigStore.getState().fetchConfig();
    expect(useConfigStore.getState().config).toEqual(initialConfig);

    const staleInFlight = deferred<Config>();
    const forcedFetch = deferred<Config>();
    vi.mocked(configApi.getConfig)
      .mockReturnValueOnce(staleInFlight.promise)
      .mockReturnValueOnce(forcedFetch.promise);

    // Trigger a non-forced refresh (cache still valid unless invalidated; bypass via section miss or clearCache)
    useConfigStore.getState().clearCache('debug');
    const stalePromise = useConfigStore.getState().fetchConfig();

    const forcePromise = useConfigStore.getState().fetchConfig(undefined, true);
    // Settled config must stay intact while forceRefresh is in flight so UI cards never vanish.
    expect(useConfigStore.getState().config).toEqual(initialConfig);
    expect(useConfigStore.getState().loading).toBe(true);

    forcedFetch.resolve(refreshedConfig);
    await expect(forcePromise).resolves.toEqual(refreshedConfig);
    expect(useConfigStore.getState().config).toEqual(refreshedConfig);

    // Late resolution of the superseded read must not overwrite the newer config.
    staleInFlight.resolve({ debug: false, usageStatisticsEnabled: false, loggingToFile: false });
    await stalePromise;
    expect(useConfigStore.getState().config).toEqual(refreshedConfig);
  });

  it('clears latestV8ConfigEtag on non-provider clearCache, preserves it on provider clearCache while evicting sibling provider caches, and resets all state on full clearCache', async () => {
    vi.mocked(configApi.getConfig).mockResolvedValueOnce({
      debug: true,
      v8Supported: true,
      v8Etag: '"etag-initial-1"',
      claudeApiKeys: [{ apiKey: 'sk-c1' }],
      codexApiKeys: [{ apiKey: 'sk-cx1' }],
      openaiCompatibility: [{ name: 'OA', baseUrl: 'https://oa.example/v1', apiKeyEntries: [] }],
    });
    await useConfigStore.getState().fetchConfig();
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"etag-initial-1"');
    expect(useConfigStore.getState().isCacheValid('claude-api-key')).toBe(true);
    expect(useConfigStore.getState().isCacheValid('codex-api-key')).toBe(true);
    expect(useConfigStore.getState().isCacheValid('openai-compatibility')).toBe(true);
    expect(useConfigStore.getState().isCacheValid('debug')).toBe(true);

    // Provider section invalidation evicts ALL provider section caches so no sibling retains stale v8Source.etag,
    // while preserving non-provider caches and the freshly written latestV8ConfigEtag.
    providersApi.setLatestV8ConfigEtag('"etag-after-provider-put-2"');
    useConfigStore.getState().clearCache('claude-api-key');
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"etag-after-provider-put-2"');
    expect(useConfigStore.getState().isCacheValid('claude-api-key')).toBe(false);
    expect(useConfigStore.getState().isCacheValid('codex-api-key')).toBe(false);
    expect(useConfigStore.getState().isCacheValid('openai-compatibility')).toBe(false);
    expect(useConfigStore.getState().isCacheValid('debug')).toBe(true);

    // Non-provider section invalidation (e.g. debug rewriting config.yaml via v0) clears latestV8ConfigEtag AND provider caches.
    useConfigStore.getState().clearCache('debug');
    expect(providersApi.getLatestV8ConfigEtag()).toBeUndefined();
    expect(useConfigStore.getState().isCacheValid('debug')).toBe(false);

    providersApi.setLatestV8ConfigEtag('"etag-before-logout"');
    useConfigStore.getState().clearCache();
    expect(useConfigStore.getState().config).toBeNull();
    expect(providersApi.getLatestV8ConfigEtag()).toBeUndefined();
  });

  it('does not overwrite newer latestV8ConfigEtag when a write occurs during fetchConfig', async () => {
    const pendingConfig = deferred<Config>();
    vi.mocked(configApi.getConfig).mockReturnValueOnce(pendingConfig.promise);

    const fetchPromise = useConfigStore.getState().fetchConfig();
    expect(configApi.getConfig).toHaveBeenCalledWith({ syncLatestV8Etag: false });

    providersApi.setLatestV8ConfigEtag('"etag-from-concurrent-write"');
    pendingConfig.resolve({
      debug: false,
      v8Supported: true,
      v8Etag: '"stale-etag-from-fetch"',
    });
    await fetchPromise;

    expect(providersApi.getLatestV8ConfigEtag()).toBe('"etag-from-concurrent-write"');
  });

  it('does not block fetchConfig from syncing a newer ETag when concurrent passive reads complete mid-flight', async () => {
    const pendingConfig = deferred<Config>();
    vi.mocked(configApi.getConfig).mockReturnValueOnce(pendingConfig.promise);

    const getRawV8Spy = vi.spyOn(apiClient, 'getRawV8').mockResolvedValueOnce({
      data: [{ name: 'Vertex Pool', keys: [{ 'api-key': 'vx-key-1' }] }],
      headers: { etag: '"older-vertex-etag-1"' },
    } as never);

    try {
      const fetchPromise = useConfigStore.getState().fetchConfig();
      await providersApi.getVertexConfigs();
      expect(providersApi.getLatestV8ConfigEtag()).toBe('"older-vertex-etag-1"');

      // Passive config read sync must also leave write mutation version untouched.
      providersApi.setLatestV8ConfigEtag('"passive-intermediate-etag"', { passiveRead: true });

      pendingConfig.resolve({
        debug: true,
        v8Supported: true,
        v8Etag: '"newer-fetch-config-etag-2"',
      });
      await fetchPromise;

      expect(providersApi.getLatestV8ConfigEtag()).toBe('"newer-fetch-config-etag-2"');
    } finally {
      getRawV8Spy.mockRestore();
    }
  });

  it('updates metaApiKeys and invalidates section cache when updateConfigValue is called for meta-api-key', async () => {
    const initialMetaKeys = [{ name: 'Meta Old', apiKey: 'meta-1' }];
    const nextMetaKeys = [{ name: 'Meta Updated', apiKey: 'meta-2', priority: 3 }];

    vi.mocked(configApi.getConfig).mockResolvedValueOnce({
      metaApiKeys: initialMetaKeys,
      raw: { 'meta-api-key': initialMetaKeys },
    });
    await useConfigStore.getState().fetchConfig();
    expect(useConfigStore.getState().isCacheValid('meta-api-key')).toBe(true);
    expect(useConfigStore.getState().isCacheValid()).toBe(true);

    useConfigStore.getState().updateConfigValue('meta-api-key', nextMetaKeys);

    expect(useConfigStore.getState().config?.metaApiKeys).toEqual(nextMetaKeys);
    expect(useConfigStore.getState().config?.raw?.['meta-api-key']).toEqual(nextMetaKeys);
    expect(useConfigStore.getState().isCacheValid('meta-api-key')).toBe(false);
    expect(useConfigStore.getState().isCacheValid()).toBe(false);
  });
});
