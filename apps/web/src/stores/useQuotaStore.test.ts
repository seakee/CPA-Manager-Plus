import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { CodexQuotaState } from '@/types';

type StorageLike = {
  getItem: (key: string) => string | null;
  setItem: (key: string, value: string) => void;
  removeItem: (key: string) => void;
  clear: () => void;
};

const createMemoryStorage = (): StorageLike => {
  const store = new Map<string, string>();
  return {
    getItem: (key) => (store.has(key) ? (store.get(key) as string) : null),
    setItem: (key, value) => {
      store.set(key, value);
    },
    removeItem: (key) => {
      store.delete(key);
    },
    clear: () => {
      store.clear();
    },
  };
};

const readPersistedCodexQuota = async () => {
  const { STORAGE_KEY_QUOTA_CACHE } = await import('@/utils/constants');
  const { obfuscatedStorage } = await import('@/services/storage/secureStorage');
  const persisted = obfuscatedStorage.getItem<{
    state?: { codexQuota?: Record<string, CodexQuotaState> };
  }>(STORAGE_KEY_QUOTA_CACHE);
  return persisted?.state?.codexQuota ?? {};
};

const readPersistedQuotaScope = async () => {
  const { STORAGE_KEY_QUOTA_CACHE } = await import('@/utils/constants');
  const { obfuscatedStorage } = await import('@/services/storage/secureStorage');
  const persisted = obfuscatedStorage.getItem<{
    state?: { cacheScope?: string };
  }>(STORAGE_KEY_QUOTA_CACHE);
  return persisted?.state?.cacheScope ?? '';
};

describe('useQuotaStore persistence', () => {
  let storage: StorageLike;

  beforeEach(() => {
    vi.resetModules();
    storage = createMemoryStorage();
    vi.stubGlobal('localStorage', storage);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('persists only manually fetched Codex success states', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setCodexQuota({
      manual: {
        status: 'success',
        windows: [],
        fetchedAtMs: 2_000,
      },
      observed: {
        status: 'success',
        windows: [],
        observedFromUsageHeaders: true,
        observedAtMs: 1_000,
      },
      failed: {
        status: 'error',
        windows: [],
        error: 'failed',
      },
    });

    expect(Object.keys(await readPersistedCodexQuota())).toEqual(['manual']);
  });

  it('clears quota state and persisted quota cache together', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setCodexQuota({
      manual: {
        status: 'success',
        windows: [],
        fetchedAtMs: 2_000,
      },
    });

    useQuotaStore.getState().clearQuotaCache();

    expect(useQuotaStore.getState().codexQuota).toEqual({});
    expect(await readPersistedCodexQuota()).toEqual({});
  });

  it('keeps quota for the same connection scope and clears it when the scope changes', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().activateQuotaCacheScope('scope-a');
    useQuotaStore.getState().setCodexQuota({
      manual: {
        status: 'success',
        windows: [],
        fetchedAtMs: 2_000,
      },
    });
    const generation = useQuotaStore.getState().cacheGeneration;

    useQuotaStore.getState().activateQuotaCacheScope('scope-a');
    expect(useQuotaStore.getState().cacheGeneration).toBe(generation);
    expect(Object.keys(useQuotaStore.getState().codexQuota)).toEqual(['manual']);

    useQuotaStore.getState().activateQuotaCacheScope('scope-b');
    expect(useQuotaStore.getState().cacheGeneration).toBe(generation + 1);
    expect(useQuotaStore.getState().codexQuota).toEqual({});
    expect(await readPersistedQuotaScope()).toBe('scope-b');
  });

  it('rejects stale async commits after the connection scope changes', async () => {
    const { captureQuotaCacheGeneration, commitIfQuotaCacheCurrent, useQuotaStore } =
      await import('./useQuotaStore');

    useQuotaStore.getState().activateQuotaCacheScope('scope-a');
    const staleGeneration = captureQuotaCacheGeneration();
    useQuotaStore.getState().activateQuotaCacheScope('scope-b');

    let committed = false;
    expect(
      commitIfQuotaCacheCurrent(staleGeneration, () => {
        committed = true;
      })
    ).toBe(false);
    expect(committed).toBe(false);
  });
});
