import { beforeEach, describe, expect, it, vi } from 'vitest';
import { configApi } from '@/services/api/config';
import type { Config } from '@/types';
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

  it('clears config immediately when clearCache is called without a section', async () => {
    vi.mocked(configApi.getConfig).mockResolvedValueOnce({ debug: true });
    await useConfigStore.getState().fetchConfig();
    expect(useConfigStore.getState().config).toEqual({ debug: true });

    useConfigStore.getState().clearCache();
    expect(useConfigStore.getState().config).toBeNull();
  });
});
