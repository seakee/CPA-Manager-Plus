import type { TFunction } from 'i18next';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { AuthFileItem, DevinQuotaData } from '@/types';
import { fetchClaudeQuota, fetchCodexQuota, fetchDevinQuota } from '@/utils/quota';
import { getQuotaCredentialStoreKey } from '@/utils/quota/credentialScope';
import { useQuotaStore } from '@/stores/useQuotaStore';
import { CLAUDE_CONFIG, CODEX_CONFIG, DEVIN_CONFIG, type QuotaConfig } from './quotaConfigs';
import { refreshQuotaWithConfig, type QuotaSetter } from './quotaRefresh';

vi.mock('@/utils/quota', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/utils/quota')>();
  return {
    ...actual,
    fetchClaudeQuota: vi.fn(),
    fetchCodexQuota: vi.fn(),
    fetchDevinQuota: vi.fn(),
  };
});

const t = ((key: string) => key) as TFunction;

const codexFile = {
  name: 'codex.json',
  type: 'codex',
  provider: 'codex',
  authIndex: '1',
} as AuthFileItem;

const claudeFile = {
  name: 'claude.json',
  type: 'claude',
  provider: 'claude',
  authIndex: '1',
} as AuthFileItem;

const codexData = (usedPercent: number) => ({
  planType: 'plus',
  windows: [
    {
      id: 'weekly',
      label: 'Weekly',
      usedPercent,
      resetLabel: 'tomorrow',
      observedAtMs: 1_000,
    },
  ],
  observedAtMs: 1_000,
  quotaInventoryObserved: true,
  subscriptionActiveUntil: null,
  rateLimitResetCreditsAvailableCount: null,
  rateLimitResetCredits: [],
  rateLimitResetCreditsError: null,
});

const claudeData = {
  quotaInventoryObserved: true,
  windows: [
    {
      id: 'five-hour',
      label: '5-hour',
      usedPercent: 25,
      resetLabel: 'tomorrow',
    },
  ],
  planType: 'plan_pro',
};

const runRefresh = <TState, TData>(
  config: QuotaConfig<TState, TData>,
  file: AuthFileItem,
  setQuota: QuotaSetter<TState>,
  currentState?: TState,
  isCurrent = () => true
) =>
  refreshQuotaWithConfig({
    config,
    file,
    setQuota,
    t,
    isCurrent,
    currentState,
  });

const deferred = <T>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve;
  });
  return { promise, resolve };
};

describe('refreshQuotaWithConfig', () => {
  beforeEach(() => {
    useQuotaStore.getState().clearQuotaCache();
    vi.mocked(fetchClaudeQuota).mockReset();
    vi.mocked(fetchCodexQuota).mockReset();
    vi.mocked(fetchDevinQuota).mockReset();
  });

  it('writes the Provider state once to the shared Codex store for all consumers', async () => {
    vi.mocked(fetchCodexQuota).mockResolvedValue(codexData(20));

    const result = await runRefresh(
      CODEX_CONFIG,
      codexFile,
      useQuotaStore.getState().setCodexQuota
    );

    expect(fetchCodexQuota).toHaveBeenCalledTimes(1);
    expect(result).toMatchObject({ status: 'success', data: codexData(20) });
    const storeKey = getQuotaCredentialStoreKey(codexFile);
    expect(useQuotaStore.getState().codexQuota[storeKey]).toBe(result?.state);
    expect(useQuotaStore.getState().codexQuota[storeKey]).toMatchObject({
      status: 'success',
      authFileKey: storeKey,
      windows: [{ id: 'weekly', usedPercent: 20 }],
    });
  });

  it('preserves an existing Claude payload while recording a failed shared refresh', async () => {
    const previousState = CLAUDE_CONFIG.buildSuccessState(claudeData, claudeFile);
    const storeKey = getQuotaCredentialStoreKey(claudeFile);
    useQuotaStore.getState().setClaudeQuota({ [storeKey]: previousState });
    vi.mocked(fetchClaudeQuota).mockRejectedValue(
      Object.assign(new Error('temporary upstream failure'), { status: 503 })
    );

    const result = await runRefresh(
      CLAUDE_CONFIG,
      claudeFile,
      useQuotaStore.getState().setClaudeQuota,
      previousState
    );
    const state = useQuotaStore.getState().claudeQuota[storeKey];

    expect(fetchClaudeQuota).toHaveBeenCalledTimes(1);
    expect(result).toMatchObject({ status: 'error', error: 'temporary upstream failure' });
    expect(state).toMatchObject({
      status: 'error',
      error: 'temporary upstream failure',
      errorStatus: 503,
      windows: previousState.windows,
      fetchedAtMs: previousState.fetchedAtMs,
    });
  });

  it('does not commit a response after the request becomes stale', async () => {
    let current = true;
    vi.mocked(fetchCodexQuota).mockResolvedValue(codexData(40));

    const resultPromise = runRefresh(
      CODEX_CONFIG,
      codexFile,
      useQuotaStore.getState().setCodexQuota,
      undefined,
      () => current
    );
    current = false;
    const result = await resultPromise;

    expect(result).toBeNull();
    expect(useQuotaStore.getState().codexQuota).toMatchObject({
      [getQuotaCredentialStoreKey(codexFile)]: { status: 'loading' },
    });
  });

  it('does not commit a response from an invalidated cache generation', async () => {
    const response = deferred<ReturnType<typeof codexData>>();
    vi.mocked(fetchCodexQuota).mockReturnValue(response.promise);

    const resultPromise = runRefresh(
      CODEX_CONFIG,
      codexFile,
      useQuotaStore.getState().setCodexQuota
    );
    useQuotaStore.getState().activateQuotaCacheScope('new-connection');
    response.resolve(codexData(60));
    const result = await resultPromise;

    expect(result).toBeNull();
    expect(useQuotaStore.getState().codexQuota).toEqual({});
  });

  it('keeps the later same-credential refresh when the earlier response arrives last', async () => {
    const first = deferred<ReturnType<typeof codexData>>();
    const second = deferred<ReturnType<typeof codexData>>();
    vi.mocked(fetchCodexQuota)
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);

    const firstPromise = runRefresh(
      CODEX_CONFIG,
      codexFile,
      useQuotaStore.getState().setCodexQuota
    );
    const secondPromise = runRefresh(
      CODEX_CONFIG,
      codexFile,
      useQuotaStore.getState().setCodexQuota
    );

    second.resolve(codexData(50));
    const secondResult = await secondPromise;
    expect(secondResult).toMatchObject({ status: 'success' });
    expect(
      useQuotaStore.getState().codexQuota[getQuotaCredentialStoreKey(codexFile)]
    ).toMatchObject({
      windows: [{ usedPercent: 50 }],
    });

    first.resolve(codexData(40));
    const firstResult = await firstPromise;

    expect(firstResult).toBeNull();
    expect(
      useQuotaStore.getState().codexQuota[getQuotaCredentialStoreKey(codexFile)]
    ).toMatchObject({
      windows: [{ usedPercent: 50 }],
    });
  });

  it('does not supersede a refresh for a different credential', async () => {
    const firstFile = { ...codexFile, name: 'shared.json', authIndex: '1' };
    const secondFile = { ...codexFile, name: 'shared.json', authIndex: '2' };
    const first = deferred<ReturnType<typeof codexData>>();
    const second = deferred<ReturnType<typeof codexData>>();
    vi.mocked(fetchCodexQuota)
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);

    const firstPromise = runRefresh(
      CODEX_CONFIG,
      firstFile,
      useQuotaStore.getState().setCodexQuota
    );
    const secondPromise = runRefresh(
      CODEX_CONFIG,
      secondFile,
      useQuotaStore.getState().setCodexQuota
    );

    second.resolve(codexData(50));
    first.resolve(codexData(40));
    const [firstResult, secondResult] = await Promise.all([firstPromise, secondPromise]);

    expect(firstResult).toMatchObject({ status: 'success' });
    expect(secondResult).toMatchObject({ status: 'success' });
    expect(useQuotaStore.getState().codexQuota).toMatchObject({
      [getQuotaCredentialStoreKey(firstFile)]: { windows: [{ usedPercent: 40 }] },
      [getQuotaCredentialStoreKey(secondFile)]: { windows: [{ usedPercent: 50 }] },
    });
  });

  it('does not supersede a refresh for a different Provider', async () => {
    const codexProviderFile = { ...codexFile, name: 'shared.json' };
    const claudeProviderFile = {
      ...claudeFile,
      name: 'shared.json',
      authIndex: '1',
    };
    const codexRequest = deferred<ReturnType<typeof codexData>>();
    const claudeRequest = deferred<typeof claudeData>();
    vi.mocked(fetchCodexQuota).mockReturnValueOnce(codexRequest.promise);
    vi.mocked(fetchClaudeQuota).mockReturnValueOnce(claudeRequest.promise);

    const codexPromise = runRefresh(
      CODEX_CONFIG,
      codexProviderFile,
      useQuotaStore.getState().setCodexQuota
    );
    const claudePromise = runRefresh(
      CLAUDE_CONFIG,
      claudeProviderFile,
      useQuotaStore.getState().setClaudeQuota
    );

    claudeRequest.resolve(claudeData);
    codexRequest.resolve(codexData(50));
    const [codexResult, claudeResult] = await Promise.all([codexPromise, claudePromise]);

    expect(codexResult).toMatchObject({ status: 'success' });
    expect(claudeResult).toMatchObject({ status: 'success' });
    expect(
      useQuotaStore.getState().codexQuota[getQuotaCredentialStoreKey(codexProviderFile)]
    ).toMatchObject({
      status: 'success',
    });
    expect(
      useQuotaStore.getState().claudeQuota[getQuotaCredentialStoreKey(claudeProviderFile)]
    ).toMatchObject({
      status: 'success',
    });
  });

  it('exposes errorStatus on QuotaRefreshResult when fetchQuota rejects with status code', async () => {
    const errorWithStatus = Object.assign(new Error('Rate limit exceeded'), { status: 429 });
    vi.mocked(fetchCodexQuota).mockRejectedValueOnce(errorWithStatus);

    const result = await runRefresh(
      CODEX_CONFIG,
      codexFile,
      useQuotaStore.getState().setCodexQuota
    );

    expect(result).toMatchObject({
      status: 'error',
      error: 'Rate limit exceeded',
      errorStatus: 429,
    });
  });

  it('does not commit late Devin quota response into new connection state after connection changes', async () => {
    useQuotaStore.getState().activateQuotaCacheScope('connection-a');

    const devinFile = {
      name: 'devin.json',
      type: 'devin',
      provider: 'devin',
      authIndex: 'd-1',
    } as AuthFileItem;

    const devinData: DevinQuotaData = {
      windows: [
        { id: 'daily', remainingPercent: 90, resetAtMs: 1726000000000, periodHours: 24 },
        { id: 'weekly', remainingPercent: 70, resetAtMs: 1726500000000, periodHours: 168 },
      ],
      observedAtMs: 1725900000000,
      plan: 'Pro',
      planStartMs: null,
      planEndMs: null,
    };

    const devinDeferred = deferred<DevinQuotaData>();
    vi.mocked(fetchDevinQuota).mockReturnValueOnce(devinDeferred.promise);

    let connectionIsCurrent = true;
    const refreshPromise = runRefresh(
      DEVIN_CONFIG,
      devinFile,
      useQuotaStore.getState().setDevinQuota,
      undefined,
      () => connectionIsCurrent
    );

    // Switch to Connection B before request completes
    connectionIsCurrent = false;
    useQuotaStore.getState().activateQuotaCacheScope('connection-b');

    // Devin request from Connection A resolves late
    devinDeferred.resolve(devinData);
    const result = await refreshPromise;

    expect(result).toBeNull();
    const devinStoreKey = getQuotaCredentialStoreKey(devinFile);
    expect(useQuotaStore.getState().devinQuota[devinStoreKey]).toBeUndefined();
  });
});
