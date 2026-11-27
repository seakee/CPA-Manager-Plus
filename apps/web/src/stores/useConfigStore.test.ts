import { beforeEach, describe, expect, it, vi } from 'vitest';
import { apiClient } from '@/services/api/client';
import { configApi } from '@/services/api/config';
import type { Config } from '@/types';
import { providersApi } from '@/services/api/providers';
import { ConfigRequestSupersededError, useConfigStore } from './useConfigStore';

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

  it('supports short maxAgeMs silent revalidation with in-flight dedupe without changing default 30s validity', async () => {
    const nowSpy = vi.spyOn(Date, 'now');
    try {
      nowSpy.mockReturnValue(1_000_000);
      const initialConfig: Config = { debug: false, v8Supported: true };
      const refreshedConfig: Config = { debug: true, v8Supported: true };

      vi.mocked(configApi.getConfig).mockResolvedValueOnce(initialConfig);
      await useConfigStore.getState().fetchConfig();
      expect(configApi.getConfig).toHaveBeenCalledTimes(1);

      // Within 5s: short maxAgeMs hits cache with zero extra requests.
      nowSpy.mockReturnValue(1_003_000);
      expect(useConfigStore.getState().isCacheValid(undefined, 5_000)).toBe(true);
      await expect(
        useConfigStore.getState().fetchConfig(undefined, false, { maxAgeMs: 5_000, silent: true })
      ).resolves.toEqual(initialConfig);
      expect(configApi.getConfig).toHaveBeenCalledTimes(1);

      // After 5s (< 30s): default 30s cache is still valid, but 5s revalidation fires once silently.
      nowSpy.mockReturnValue(1_006_000);
      expect(useConfigStore.getState().isCacheValid()).toBe(true);
      expect(useConfigStore.getState().isCacheValid(undefined, 5_000)).toBe(false);

      const pendingRefresh = deferred<Config>();
      vi.mocked(configApi.getConfig).mockReturnValueOnce(pendingRefresh.promise);

      const p1 = useConfigStore
        .getState()
        .fetchConfig(undefined, false, { maxAgeMs: 5_000, silent: true });
      const p2 = useConfigStore
        .getState()
        .fetchConfig(undefined, false, { maxAgeMs: 5_000, silent: true });

      // Single-flight deduplication and no loading flash while silent revalidation is in flight.
      expect(configApi.getConfig).toHaveBeenCalledTimes(2);
      expect(useConfigStore.getState().loading).toBe(false);
      expect(useConfigStore.getState().config).toEqual(initialConfig);

      pendingRefresh.resolve(refreshedConfig);
      await expect(Promise.all([p1, p2])).resolves.toEqual([refreshedConfig, refreshedConfig]);
      expect(useConfigStore.getState().config).toEqual(refreshedConfig);
      expect(useConfigStore.getState().isCacheValid(undefined, 5_000)).toBe(true);

      // Transient silent failure preserves settled config and avoids polluting store error.
      nowSpy.mockReturnValue(1_012_000);
      vi.mocked(configApi.getConfig).mockRejectedValueOnce(new Error('network blip'));
      await expect(
        useConfigStore.getState().fetchConfig(undefined, false, { maxAgeMs: 5_000, silent: true })
      ).rejects.toThrow('network blip');
      expect(useConfigStore.getState().config).toEqual(refreshedConfig);
      expect(useConfigStore.getState().error).toBeNull();
    } finally {
      nowSpy.mockRestore();
    }
  });

  it('discards stale silent revalidation superseded by forceRefresh and promotes error reporting when joined by foreground caller', async () => {
    const staleSilent = deferred<Config>();
    const freshForce = deferred<Config>();
    vi.mocked(configApi.getConfig)
      .mockReturnValueOnce(staleSilent.promise)
      .mockReturnValueOnce(freshForce.promise);

    const silentCall = useConfigStore
      .getState()
      .fetchConfig(undefined, false, { maxAgeMs: 5_000, silent: true });
    const forceCall = useConfigStore.getState().fetchConfig(undefined, true);

    const newerConfig: Config = { debug: true, routingStrategy: 'fill-first', v8Supported: true };
    freshForce.resolve(newerConfig);
    await expect(forceCall).resolves.toEqual(newerConfig);

    // Stale silent response arriving later must neither overwrite store nor resolve stale config.
    staleSilent.resolve({ debug: false, routingStrategy: 'round-robin', v8Supported: true });
    await expect(silentCall).resolves.toEqual(newerConfig);
    expect(useConfigStore.getState().config).toEqual(newerConfig);

    // Foreground caller joining an in-flight silent request promotes loading/error reporting.
    const joinedFailure = deferred<Config>();
    vi.mocked(configApi.getConfig).mockReturnValueOnce(joinedFailure.promise);
    const pSilent = useConfigStore
      .getState()
      .fetchConfig(undefined, false, { maxAgeMs: 0, silent: true });
    const pForeground = useConfigStore
      .getState()
      .fetchConfig(undefined, false, { maxAgeMs: 0, silent: false });
    expect(useConfigStore.getState().loading).toBe(true);
    joinedFailure.reject(new Error('foreground-visible-failure'));
    await expect(pSilent).rejects.toThrow('foreground-visible-failure');
    await expect(pForeground).rejects.toThrow('foreground-visible-failure');
    expect(useConfigStore.getState().error).toBe('foreground-visible-failure');
  });

  it('awaits superseding forceRefresh when stale request settles first, and rejects requests invalidated by clearCache', async () => {
    vi.mocked(configApi.getConfig).mockResolvedValueOnce({
      debug: false,
      routingStrategy: 'round-robin',
    });
    await useConfigStore.getState().fetchConfig();

    // Order 1: stale silent request resolves BEFORE superseding forceRefresh finishes.
    const staleFirst = deferred<Config>();
    const forceSecond = deferred<Config>();
    vi.mocked(configApi.getConfig)
      .mockReturnValueOnce(staleFirst.promise)
      .mockReturnValueOnce(forceSecond.promise);

    const pStale = useConfigStore
      .getState()
      .fetchConfig(undefined, false, { maxAgeMs: 0, silent: true });
    const pForce = useConfigStore.getState().fetchConfig(undefined, true);

    staleFirst.resolve({ debug: false, routingStrategy: 'round-robin' });
    const freshConfig: Config = { debug: true, routingStrategy: 'fill-first' };
    forceSecond.resolve(freshConfig);

    await expect(pStale).resolves.toEqual(freshConfig);
    await expect(pForce).resolves.toEqual(freshConfig);
    expect(useConfigStore.getState().config).toEqual(freshConfig);

    // Order 2: full clearCache() (logout/connection switch, config === null) before response returns.
    const resetPending = deferred<Config>();
    vi.mocked(configApi.getConfig).mockReturnValueOnce(resetPending.promise);
    const pBeforeReset = useConfigStore.getState().fetchConfig(undefined, false, { maxAgeMs: 0 });
    useConfigStore.getState().clearCache();
    expect(useConfigStore.getState().config).toBeNull();

    resetPending.resolve({ debug: true, routingStrategy: 'stale-from-previous-session' });
    await expect(pBeforeReset).rejects.toBeInstanceOf(ConfigRequestSupersededError);
    expect(useConfigStore.getState().config).toBeNull();

    // Order 3: section clearCache invalidates an in-flight read without starting a replacement fetch.
    const sectionInvalidated = deferred<Config>();
    vi.mocked(configApi.getConfig).mockReturnValueOnce(sectionInvalidated.promise);
    const pBeforeSectionWrite = useConfigStore
      .getState()
      .fetchConfig(undefined, false, { maxAgeMs: 0, silent: true });
    useConfigStore.getState().clearCache('claude-api-key');
    sectionInvalidated.resolve({ debug: false });
    await expect(pBeforeSectionWrite).rejects.toBeInstanceOf(ConfigRequestSupersededError);

    // Order 4: transport rejection after forceRefresh or clearCache follows supersession handling.
    const oldFailing = deferred<Config>();
    const replacementOk = deferred<Config>();
    vi.mocked(configApi.getConfig)
      .mockReturnValueOnce(oldFailing.promise)
      .mockReturnValueOnce(replacementOk.promise);
    const pOldFail = useConfigStore.getState().fetchConfig(undefined, false, { maxAgeMs: 0 });
    const pReplacement = useConfigStore.getState().fetchConfig(undefined, true);
    oldFailing.reject(new Error('obsolete-socket-error'));
    const recovered: Config = { debug: true, routingStrategy: 'round-robin' };
    replacementOk.resolve(recovered);
    await expect(pOldFail).resolves.toEqual(recovered);
    await expect(pReplacement).resolves.toEqual(recovered);
  });
});
