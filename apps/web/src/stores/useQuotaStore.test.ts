import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  AntigravityQuotaState,
  ClaudeQuotaState,
  CodexQuotaState,
  DevinQuotaState,
  KimiQuotaState,
  XaiQuotaState,
} from '@/types';

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

const readPersistedQuotaState = async () => {
  const { STORAGE_KEY_QUOTA_CACHE } = await import('@/utils/constants');
  const { obfuscatedStorage } = await import('@/services/storage/secureStorage');
  const persisted = obfuscatedStorage.getItem<{
    state?: {
      antigravityQuota?: Record<string, AntigravityQuotaState>;
      claudeQuota?: Record<string, ClaudeQuotaState>;
      codexQuota?: Record<string, CodexQuotaState>;
      devinQuota?: Record<string, DevinQuotaState>;
      kimiQuota?: Record<string, KimiQuotaState>;
      xaiQuota?: Record<string, XaiQuotaState>;
    };
  }>(STORAGE_KEY_QUOTA_CACHE);
  return persisted?.state ?? {};
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

  it('persists manually fetched Codex success and error states', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setCodexQuota({
      manual: {
        status: 'success',
        windows: [],
        authFileKey: 'manual',
        authFileIdentityVerified: true,
        fetchedAtMs: 2_000,
      },
      observed: {
        status: 'success',
        windows: [],
        authFileKey: 'observed',
        authFileIdentityVerified: true,
        observedFromUsageHeaders: true,
        observedAtMs: 1_000,
      },
      failed: {
        status: 'error',
        windows: [],
        error: 'failed',
        errorStatus: 401,
        authFileKey: 'failed',
        authFileIdentityVerified: true,
      },
      loading: {
        status: 'loading',
        windows: [],
      },
    });

    const persisted = await readPersistedQuotaState();
    expect(Object.keys(persisted.codexQuota ?? {})).toEqual(['manual', 'failed']);
    expect(persisted.codexQuota?.failed).toMatchObject({
      status: 'error',
      error: 'failed',
      errorStatus: 401,
    });
  });

  it('persists success and error states for every quota provider', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setClaudeQuota({
      claudeSuccess: {
        status: 'success',
        windows: [],
        authFileKey: 'claudeSuccess',
        authFileIdentityVerified: true,
      },
      claudeError: {
        status: 'error',
        windows: [],
        error: 'claude failed',
        errorStatus: 500,
        authFileKey: 'claudeError',
        authFileIdentityVerified: true,
      },
      claudeLoading: { status: 'loading', windows: [] },
    });
    useQuotaStore.getState().setAntigravityQuota({
      antigravitySuccess: {
        status: 'success',
        groups: [],
        authFileKey: 'antigravitySuccess',
        authFileIdentityVerified: true,
      },
      antigravityError: {
        status: 'error',
        groups: [],
        error: 'antigravity failed',
        authFileKey: 'antigravityError',
        authFileIdentityVerified: true,
      },
      antigravityLoading: { status: 'loading', groups: [] },
    });
    useQuotaStore.getState().setKimiQuota({
      kimiSuccess: {
        status: 'success',
        rows: [],
        authFileKey: 'kimiSuccess',
        authFileIdentityVerified: true,
      },
      kimiError: {
        status: 'error',
        rows: [],
        error: 'kimi failed',
        authFileKey: 'kimiError',
        authFileIdentityVerified: true,
      },
      kimiLoading: { status: 'loading', rows: [] },
    });
    useQuotaStore.getState().setXaiQuota({
      xaiSuccess: {
        status: 'success',
        billing: null,
        authFileKey: 'xaiSuccess',
        authFileIdentityVerified: true,
      },
      xaiError: {
        status: 'error',
        billing: null,
        error: 'xai failed',
        authFileKey: 'xaiError',
        authFileIdentityVerified: true,
      },
      xaiLoading: { status: 'loading', billing: null },
    });
    useQuotaStore.getState().setDevinQuota({
      devinSuccess: {
        status: 'success',
        windows: [],
        observedAtMs: null,
        plan: null,
        planStartMs: null,
        planEndMs: null,
        authFileKey: 'devinSuccess',
        authFileIdentityVerified: true,
      },
      devinError: {
        status: 'error',
        windows: [],
        observedAtMs: null,
        plan: null,
        planStartMs: null,
        planEndMs: null,
        error: 'devin failed',
        errorStatus: 500,
        authFileKey: 'devinError',
        authFileIdentityVerified: true,
      },
      devinLoading: {
        status: 'loading',
        windows: [],
        observedAtMs: null,
        plan: null,
        planStartMs: null,
        planEndMs: null,
      },
    });

    const persisted = await readPersistedQuotaState();

    expect(Object.keys(persisted.claudeQuota ?? {})).toEqual(['claudeSuccess', 'claudeError']);
    expect(Object.keys(persisted.antigravityQuota ?? {})).toEqual([
      'antigravitySuccess',
      'antigravityError',
    ]);
    expect(Object.keys(persisted.kimiQuota ?? {})).toEqual(['kimiSuccess', 'kimiError']);
    expect(Object.keys(persisted.xaiQuota ?? {})).toEqual(['xaiSuccess', 'xaiError']);
    expect(Object.keys(persisted.devinQuota ?? {})).toEqual(['devinSuccess', 'devinError']);
  });

  it('drops legacy and unverified quota cache entries while canonicalizing verified keys', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setClaudeQuota({
      legacy: { status: 'success', windows: [] },
      unverified: {
        status: 'success',
        windows: [],
        authFileKey: 'unverified',
        authFileIdentityVerified: false,
      },
      oldFilenameKey: {
        status: 'success',
        windows: [],
        authFileKey: 'canonical-credential-key',
        authFileIdentityVerified: true,
      },
    });

    const persisted = await readPersistedQuotaState();
    expect(Object.keys(persisted.claudeQuota ?? {})).toEqual(['canonical-credential-key']);
  });

  it('hydrates persisted quota success and error states', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setCodexQuota({
      failed: {
        status: 'error',
        windows: [],
        error: 'failed',
        errorStatus: 401,
        authFileKey: 'failed',
        authFileIdentityVerified: true,
      },
    });
    useQuotaStore.getState().setClaudeQuota({
      claudeSuccess: {
        status: 'success',
        windows: [],
        authFileKey: 'claudeSuccess',
        authFileIdentityVerified: true,
      },
    });
    useQuotaStore.getState().setDevinQuota({
      devinSuccess: {
        status: 'success',
        windows: [],
        observedAtMs: 1_000,
        plan: 'Team',
        planStartMs: null,
        planEndMs: null,
        authFileKey: 'devinSuccess',
        authFileIdentityVerified: true,
      },
      devinError: {
        status: 'error',
        windows: [],
        observedAtMs: null,
        plan: null,
        planStartMs: null,
        planEndMs: null,
        error: 'devin failed',
        errorStatus: 502,
        authFileKey: 'devinError',
        authFileIdentityVerified: true,
      },
    });

    vi.resetModules();
    const { useQuotaStore: hydratedQuotaStore } = await import('./useQuotaStore');

    expect(hydratedQuotaStore.getState().codexQuota.failed).toMatchObject({
      status: 'error',
      errorStatus: 401,
    });
    expect(hydratedQuotaStore.getState().claudeQuota.claudeSuccess).toMatchObject({
      status: 'success',
    });
    expect(hydratedQuotaStore.getState().devinQuota.devinSuccess).toMatchObject({
      status: 'success',
      plan: 'Team',
    });
    expect(hydratedQuotaStore.getState().devinQuota.devinError).toMatchObject({
      status: 'error',
      error: 'devin failed',
      errorStatus: 502,
    });
  });

  it('clears quota state and persisted quota cache together', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().setCodexQuota({
      manual: {
        status: 'success',
        windows: [],
        authFileKey: 'manual',
        authFileIdentityVerified: true,
        fetchedAtMs: 2_000,
      },
    });

    useQuotaStore.getState().clearQuotaCache();

    expect(useQuotaStore.getState().codexQuota).toEqual({});
    expect(await readPersistedQuotaState()).toMatchObject({
      antigravityQuota: {},
      claudeQuota: {},
      codexQuota: {},
      devinQuota: {},
      kimiQuota: {},
      xaiQuota: {},
    });
  });

  it('keeps quota for the same connection scope and clears it when the scope changes', async () => {
    const { useQuotaStore } = await import('./useQuotaStore');

    useQuotaStore.getState().activateQuotaCacheScope('scope-a');
    useQuotaStore.getState().setCodexQuota({
      manual: {
        status: 'success',
        windows: [],
        authFileKey: 'manual',
        authFileIdentityVerified: true,
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
