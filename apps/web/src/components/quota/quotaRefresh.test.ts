import type { TFunction } from 'i18next';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { AuthFileItem, DevinQuotaData, MetaQuotaData, PluginQuotaData } from '@/types';
import {
  fetchClaudeQuota,
  fetchCodexQuota,
  fetchDevinQuota,
  fetchMetaQuota,
  type CodexQuotaData,
} from '@/utils/quota';
import { getQuotaCredentialStoreKey } from '@/utils/quota/credentialScope';
import { useQuotaStore } from '@/stores/useQuotaStore';
import {
  CLAUDE_CONFIG,
  CODEX_CONFIG,
  DEVIN_CONFIG,
  META_CONFIG,
  PLUGIN_CONFIG,
  type QuotaConfig,
} from './quotaConfigs';
import { refreshQuotaWithConfig, type QuotaSetter } from './quotaRefresh';

vi.mock('@/utils/quota', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/utils/quota')>();
  return {
    ...actual,
    fetchClaudeQuota: vi.fn(),
    fetchCodexQuota: vi.fn(),
    fetchDevinQuota: vi.fn(),
    fetchMetaQuota: vi.fn(),
  };
});

const pluginQuotaFetchMock = vi.fn();

vi.mock('@/services/api/plugins', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/api/plugins')>();
  return {
    ...actual,
    pluginsApi: {
      ...actual.pluginsApi,
      // Apply the real response normalization so fixtures stay raw, exactly
      // like the payloads the plugin quota config receives in production.
      fetchQuota: async (...args: Parameters<typeof actual.pluginsApi.fetchQuota>) =>
        actual.normalizePluginQuotaResponse(await pluginQuotaFetchMock(...args)),
    },
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

const codexData = (usedPercent: number): CodexQuotaData => ({
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

  it('preserves reset-credit detail evidence updated in-flight when committing successful refresh', async () => {
    const storeKey = getQuotaCredentialStoreKey(codexFile);
    const initialQuotaState = CODEX_CONFIG.buildSuccessState(codexData(20), codexFile);
    useQuotaStore.getState().setCodexQuota({ [storeKey]: initialQuotaState });

    const codexDeferred = deferred<ReturnType<typeof codexData>>();
    vi.mocked(fetchCodexQuota).mockReturnValueOnce(codexDeferred.promise);

    const refreshPromise = runRefresh(
      CODEX_CONFIG,
      codexFile,
      useQuotaStore.getState().setCodexQuota,
      initialQuotaState
    );

    // In-flight: reset-credit detail evidence is updated in the store
    const updatedResetCredits = [
      {
        id: 'credit-in-flight-1',
        status: 'available',
        grantedAt: '2026-09-01T00:00:00Z',
        expiresAt: '2026-10-01T00:00:00Z',
      },
    ];
    const intermediateState = {
      ...initialQuotaState,
      rateLimitResetCreditsAvailableCount: 1,
      rateLimitResetCredits: updatedResetCredits,
      resetCreditsCountEvidenceAtMs: 2500,
      resetCreditsDetailEvidenceAtMs: 2500,
      resetCreditsDetailStale: false,
    };
    useQuotaStore.getState().setCodexQuota({ [storeKey]: intermediateState });

    // Quota response arrives with count but no detail (e.g. summary or empty detail payload)
    const incomingData = {
      ...codexData(30),
      rateLimitResetCreditsAvailableCount: 1,
      rateLimitResetCredits: [],
      rateLimitResetCreditsError: null,
      observedAtMs: 3000,
    };
    codexDeferred.resolve(incomingData);

    const result = await refreshPromise;
    expect(result).toMatchObject({ status: 'success' });

    const finalState = useQuotaStore.getState().codexQuota[storeKey];
    // Must preserve the in-flight detail evidence, proving commit reads latest previousState
    expect(finalState.rateLimitResetCredits).toEqual(updatedResetCredits);
    expect(finalState.rateLimitResetCreditsAvailableCount).toBe(1);
    expect(finalState.resetCreditsDetailEvidenceAtMs).toBe(2500);
    expect(finalState.windows[0]?.usedPercent).toBe(30);
  });

  it('does not commit late Meta quota response when context becomes stale in-flight', async () => {
    useQuotaStore.getState().activateQuotaCacheScope('connection-meta-a');

    const metaFile = {
      name: 'meta.json',
      type: 'meta',
      provider: 'meta',
      authIndex: 'meta-1',
    } as AuthFileItem;

    const metaDeferred = deferred<MetaQuotaData>();
    let observedContextIsCurrent: (() => boolean) | undefined;

    vi.mocked(fetchMetaQuota).mockImplementationOnce(async (_file, _t, _scope, context) => {
      observedContextIsCurrent = context?.isCurrent;
      return metaDeferred.promise;
    });

    let connectionIsCurrent = true;
    const refreshPromise = runRefresh(
      META_CONFIG,
      metaFile,
      useQuotaStore.getState().setMetaQuota,
      undefined,
      () => connectionIsCurrent
    );

    expect(observedContextIsCurrent).toBeDefined();
    expect(observedContextIsCurrent!()).toBe(true);

    // Switch connection / invalidate cache generation while request is in-flight
    connectionIsCurrent = false;
    useQuotaStore.getState().activateQuotaCacheScope('connection-meta-b');

    expect(observedContextIsCurrent!()).toBe(false);

    const metaData: MetaQuotaData = {
      windows: [
        {
          id: 'window',
          usedPercent: 40,
          resetAtMs: 1726000000000,
          resetAccuracy: 'exact',
          limitWindowSeconds: 18000,
          quotaProgressObservedAtMs: 1725900000000,
        },
        {
          id: 'weekly',
          usedPercent: null,
          resetAtMs: 1726500000000,
          resetAccuracy: 'exact',
          limitWindowSeconds: null,
          quotaProgressObservedAtMs: null,
        },
      ],
      observedAtMs: 1725900000000,
      plan: 'pro',
      isSubscriptionActive: true,
      quotaInventoryObserved: true,
    };

    metaDeferred.resolve(metaData);
    const result = await refreshPromise;

    expect(result).toBeNull();
    const metaStoreKey = getQuotaCredentialStoreKey(metaFile);
    expect(useQuotaStore.getState().metaQuota[metaStoreKey]).toBeUndefined();
  });

  describe('plugin quota retention', () => {
    const pluginFile = {
      name: 'commandcode-auth.json',
      type: 'commandcode',
      provider: 'commandcode',
      authIndex: 'cc-1',
      quota_plugin_id: 'commandcode',
    } as AuthFileItem;

    const pluginData = (
      summaryValue: number,
      extra?: Partial<PluginQuotaData>
    ): PluginQuotaData => ({
      groups: [],
      summary: [
        {
          key: 'usage_percent',
          label: 'Usage (rounded)',
          value: summaryValue,
          unit: '%',
          format: 'number',
          currency: null,
        },
        {
          key: 'extra_credits',
          label: 'Extra balance',
          value: 12.5,
          unit: null,
          format: 'currency',
          currency: 'USD',
        },
      ],
      subscription: null,
      serverTimeOffsetMs: null,
      pluginId: 'commandcode',
      quotaProvider: 'commandcode',
      ...extra,
    });

    beforeEach(() => {
      pluginQuotaFetchMock.mockReset();
    });

    it('keeps the previous summary and groups visible while a refresh is pending', async () => {
      const storeKey = getQuotaCredentialStoreKey(pluginFile);
      const previousState = PLUGIN_CONFIG.buildSuccessState(pluginData(10), pluginFile);
      useQuotaStore.getState().setPluginQuota({ [storeKey]: previousState });

      const pending = deferred<PluginQuotaData>();
      pluginQuotaFetchMock.mockReturnValueOnce(pending.promise);

      const refreshPromise = runRefresh(
        PLUGIN_CONFIG,
        pluginFile,
        useQuotaStore.getState().setPluginQuota,
        previousState
      );

      // Pending refresh must not wipe the previous scoped snapshot.
      expect(useQuotaStore.getState().pluginQuota[storeKey]).toMatchObject({
        status: 'success',
        summary: previousState.summary,
      });

      pending.resolve(pluginData(32));
      const result = await refreshPromise;
      expect(result).toMatchObject({ status: 'success' });
      expect(useQuotaStore.getState().pluginQuota[storeKey]).toMatchObject({
        status: 'success',
        summary: pluginData(32).summary,
      });
    });

    it('keeps prior plugin facts and records feedback on a transient refresh failure', async () => {
      const storeKey = getQuotaCredentialStoreKey(pluginFile);
      const previousState = PLUGIN_CONFIG.buildSuccessState(
        pluginData(10, {
          groups: [
            {
              id: 'windows',
              label: 'Windows',
              buckets: [{ id: '5h', label: '5h', window: '5h', remainingFraction: 0.4 }],
            },
          ],
        }),
        pluginFile
      );
      useQuotaStore.getState().setPluginQuota({ [storeKey]: previousState });
      pluginQuotaFetchMock.mockRejectedValueOnce(
        Object.assign(new Error('bridge unavailable'), { status: 503 })
      );

      const result = await runRefresh(
        PLUGIN_CONFIG,
        pluginFile,
        useQuotaStore.getState().setPluginQuota,
        previousState
      );
      const state = useQuotaStore.getState().pluginQuota[storeKey];

      expect(result).toMatchObject({ status: 'error', error: 'bridge unavailable', errorStatus: 503 });
      expect(state).toMatchObject({
        status: 'error',
        error: 'bridge unavailable',
        errorStatus: 503,
        summary: previousState.summary,
        groups: previousState.groups,
        pluginId: 'commandcode',
      });
    });

    it('rejects an evidence-free refresh and retains the previous scoped snapshot with feedback', async () => {
      const storeKey = getQuotaCredentialStoreKey(pluginFile);
      const previousState = PLUGIN_CONFIG.buildSuccessState(pluginData(32), pluginFile);
      useQuotaStore.getState().setPluginQuota({ [storeKey]: previousState });

      // A follow-up response whose only summary value normalizes to null is not
      // meaningful evidence: the refresh must fail instead of replacing facts.
      pluginQuotaFetchMock.mockResolvedValueOnce({
        subscription: null,
        summary: [{ key: 'broken', label: 'Malformed synthetic summary', value: { remaining: 1 } }],
        groups: [],
        serverTimeOffsetMs: null,
      });

      const result = await runRefresh(
        PLUGIN_CONFIG,
        pluginFile,
        useQuotaStore.getState().setPluginQuota,
        previousState
      );
      const state = useQuotaStore.getState().pluginQuota[storeKey];

      expect(result).toMatchObject({ status: 'error', error: 'plugin_quota.empty_data' });
      expect(state).toMatchObject({
        status: 'error',
        error: 'plugin_quota.empty_data',
        summary: previousState.summary,
        pluginId: 'commandcode',
      });
    });

    it('does not publish a late plugin response after a newer refresh replaced the snapshot', async () => {
      const storeKey = getQuotaCredentialStoreKey(pluginFile);
      const first = deferred<PluginQuotaData>();
      const second = deferred<PluginQuotaData>();
      pluginQuotaFetchMock
        .mockReturnValueOnce(first.promise)
        .mockReturnValueOnce(second.promise);

      const firstPromise = runRefresh(
        PLUGIN_CONFIG,
        pluginFile,
        useQuotaStore.getState().setPluginQuota
      );
      const secondPromise = runRefresh(
        PLUGIN_CONFIG,
        pluginFile,
        useQuotaStore.getState().setPluginQuota
      );

      second.resolve(pluginData(50));
      await secondPromise;
      first.resolve(pluginData(10));
      const firstResult = await firstPromise;

      expect(firstResult).toBeNull();
      expect(useQuotaStore.getState().pluginQuota[storeKey]).toMatchObject({
        status: 'success',
        summary: pluginData(50).summary,
      });
    });
  });
});
