import { type ReactNode } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AuthFileItem } from '@/types';
import type { AccountQuotaState } from './components/accountOverviewPresentation';
import { createCodexInspectionConnectionFingerprint } from './codexInspection';
import { getQuotaCredentialStoreKey } from '@/utils/quota/credentialScope';
import {
  publishAccountCredentialMutationRevision,
  useAccountCredentialMutationRevisionStore,
} from '@/stores/useAccountCredentialMutationRevisionStore';
import { useQuotaStore } from '@/stores/useQuotaStore';
import { MonitoringCenterPage } from './MonitoringCenterPage';

(
  globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

const mocks = vi.hoisted(() => ({
  apiBase: 'http://cpa-a.local:8317',
  managementKey: 'manager-key-a',
  authFiles: [] as AuthFileItem[],
  nextAuthFiles: null as AuthFileItem[] | null,
  lastAccountOverviewProps: null as null | {
    accountQuotaStatesByRowId: Record<string, AccountQuotaState>;
    onLoadAccountQuota: (rowId: string, force: boolean) => void | Promise<void>;
  },
  lastHeaderRefresh: null as null | (() => void | Promise<void>),
  loadHeaderSnapshots: vi.fn(async () => undefined),
  refreshMeta: vi.fn(),
  refreshQuotaWithConfig: vi.fn(),
}));

const makeCodexFile = (authIndex: string, name: string): AuthFileItem =>
  ({
    name,
    type: 'codex',
    provider: 'codex',
    authIndex,
    auth_index: authIndex,
    account: 'workspace@example.com',
    account_id: 'workspace-a',
    status: 'ready',
    disabled: false,
  }) as AuthFileItem;

vi.mock('react-router-dom', () => ({
  useLocation: () => ({ pathname: '/monitoring', search: '', hash: '', state: null, key: 'test' }),
}));

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string) => key,
      i18n: { language: 'en' },
    }),
  };
});

vi.mock('@/stores', async () => {
  const quotaStore = await import('@/stores/useQuotaStore');
  return {
    useAuthStore: (selector: (state: Record<string, unknown>) => unknown) =>
      selector({
        apiBase: mocks.apiBase,
        managementKey: mocks.managementKey,
        connectionStatus: 'connected',
      }),
    useConfigStore: (selector: (state: Record<string, unknown>) => unknown) =>
      selector({ config: {} }),
    useNotificationStore: (selector: (state: Record<string, unknown>) => unknown) =>
      selector({ showNotification: vi.fn(), showConfirmation: vi.fn() }),
    useQuotaStore: quotaStore.useQuotaStore,
    captureQuotaCacheGeneration: () => 0,
    commitIfQuotaCacheCurrent: (_generation: number, commit: () => void) => {
      commit();
      return true;
    },
  };
});

vi.mock('@/stores/useUsageHeaderSnapshotStore', () => ({
  useUsageHeaderSnapshotStore: (selector: (state: Record<string, unknown>) => unknown) =>
    selector({ items: [], generatedAtMs: 0 }),
}));

vi.mock('@/features/monitoring/hooks/useMonitoringData', async () => {
  const React = await import('react');
  return {
    buildRealtimeMonitorRows: () => [],
    getRangeBounds: () => ({ startMs: 0, endMs: Date.now() }),
    useMonitoringData: () => {
      const [, setRevision] = React.useState(0);
      const refreshMeta = React.useCallback(() => {
        const applyRefresh = (payload: unknown) => {
          if (mocks.nextAuthFiles) {
            mocks.authFiles = mocks.nextAuthFiles;
            mocks.nextAuthFiles = null;
          }
          setRevision((current) => current + 1);
          return payload;
        };
        const result = mocks.refreshMeta();
        if (result && typeof (result as Promise<unknown>).then === 'function') {
          return (result as Promise<unknown>).then(applyRefresh);
        }
        applyRefresh(result);
        return Promise.resolve(result);
      }, []);
      const authIndex = String(mocks.authFiles[0]?.authIndex ?? '');
      const row = {
        id: 'workspace-a',
        account: 'workspace-a',
        displayAccount: 'workspace@example.com',
        accountMasked: 'wor***-a',
        authLabels: ['workspace@example.com'],
        authIndices: authIndex ? [authIndex] : [],
        channels: [],
        totalCalls: 1,
        successCalls: 1,
        failureCalls: 0,
        successRate: 1,
        inputTokens: 1,
        outputTokens: 1,
        cachedTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        totalTokens: 2,
        totalCost: 0,
        averageLatencyMs: 1,
        lastSeenAt: 1,
        recentPattern: [true],
        models: [],
      };
      return {
        loading: false,
        error: '',
        authFiles: mocks.authFiles,
        channels: [],
        summary: {
          totalCalls: 1,
          successCalls: 1,
          failureCalls: 0,
          successRate: 1,
          inputTokens: 1,
          outputTokens: 1,
          reasoningTokens: 0,
          cachedTokens: 0,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          totalTokens: 2,
          totalCost: 0,
          averageLatencyMs: 1,
          rpm30m: 0,
          tpm30m: 0,
          avgDailyRequests: 0,
          avgDailyTokens: 0,
          approxTasks: 0,
          approxTaskFailures: 0,
          approxTaskSuccessRate: 0,
          zeroTokenCalls: 0,
          zeroTokenModels: [],
        },
        metadata: {
          totalAuthFiles: 1,
          activeAuthFiles: 1,
          unavailableAuthFiles: 0,
          runtimeOnlyAuthFiles: 0,
          totalChannels: 0,
          enabledChannels: 0,
          configuredModels: 0,
          planTypes: [],
        },
        statusChips: [],
        timeline: [],
        timelineGranularity: 'hour' as const,
        hourlyDistribution: [],
        modelShareRows: [],
        channelRows: [],
        modelRows: [],
        failureSourceRows: [],
        taskBuckets: [],
        recentFailures: [],
        accountRows: [row],
        apiKeyRows: [],
        filterOptions: {
          accountRows: [row],
          apiKeyRows: [],
          providers: ['codex'],
          models: [],
          channels: [],
          headerTraceIds: [],
        },
        filteredRows: [],
        eventsHasMore: false,
        eventsLoadingMore: false,
        eventsRetentionLimited: false,
        eventsTotalCount: 0,
        eventsLoadedCount: 0,
        lastRefreshedAt: new Date(1),
        isTransitioningScope: false,
        hasPresentationSnapshot: true,
        refreshMeta,
        loadMoreEvents: vi.fn(),
      };
    },
  };
});

vi.mock('@/features/monitoring/hooks/useUsageData', () => ({
  useUsageData: () => ({
    loading: false,
    error: '',
    modelPrices: {},
    apiKeyAliases: {},
    providerKeyAliases: [],
    loadApiKeyAliases: vi.fn(async () => undefined),
    loadProviderKeyAliases: vi.fn(async () => undefined),
    exportUsage: vi.fn(),
    importUsage: vi.fn(),
    cancelUsageImport: vi.fn(),
  }),
}));

vi.mock('@/features/monitoring/hooks/useHeaderSnapshotsLoader', () => ({
  useHeaderSnapshotsLoader: () => mocks.loadHeaderSnapshots,
}));

vi.mock('@/hooks/useHeaderRefresh', () => ({
  useHeaderRefresh: (handler: (() => void | Promise<void>) | null | undefined) => {
    mocks.lastHeaderRefresh = handler ?? null;
  },
}));
vi.mock('@/hooks/useInterval', () => ({ useInterval: vi.fn() }));
vi.mock('@/hooks/useRequestMonitoringAvailability', () => ({
  useRequestMonitoringAvailability: () => ({
    checking: false,
    available: true,
    serviceBase: 'http://manager.local:8318',
    modelPricesAvailable: true,
    reason: '',
  }),
}));
vi.mock('@/components/common/PageTransitionLayer', () => ({
  usePageTransitionLayer: () => null,
}));
vi.mock('@/components/common/useDatabaseMaintenance', () => ({
  useDatabaseMaintenance: () => ({ status: null }),
}));

vi.mock('@/components/quota', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/components/quota')>();
  return {
    ...actual,
    refreshQuotaWithConfig: mocks.refreshQuotaWithConfig,
  };
});

vi.mock('@/features/monitoring/model/monitoringCenterPageModel', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/monitoring/model/monitoringCenterPageModel')>();
  return actual;
});

vi.mock('@/features/monitoring/components/AccountOverviewPanel', () => ({
  AccountOverviewPanel: (props: {
    accountQuotaStatesByRowId: Record<string, AccountQuotaState>;
    onLoadAccountQuota: (rowId: string, force: boolean) => void | Promise<void>;
  }) => {
    mocks.lastAccountOverviewProps = props;
    return null;
  },
  AccountOverviewPanelActions: () => null,
}));
vi.mock('@/features/monitoring/components/AccountOverviewCard', () => ({
  AccountExpandedDetails: () => null,
  AccountOverviewCard: () => null,
}));
vi.mock('@/features/monitoring/components/MonitoringDataPanel', () => ({
  MonitoringDataPanel: (props: { activeTab: string; renderContent: (tab: string) => ReactNode }) =>
    props.renderContent(props.activeTab),
}));

vi.mock('@/features/monitoring/components/ApiKeySummaryPanel', () => ({
  ApiKeySummaryPanel: () => null,
  ApiKeySummaryPanelActions: () => null,
}));
vi.mock('@/features/monitoring/components/MonitoringActionBar', () => ({
  MonitoringActionBar: () => null,
}));
vi.mock('@/features/monitoring/components/MonitoringDatabaseMaintenanceHint', () => ({
  MonitoringDatabaseMaintenanceHint: () => null,
}));
vi.mock('@/features/monitoring/components/MonitoringCustomRangeModal', () => ({
  MonitoringCustomRangeModal: () => null,
}));
vi.mock('@/features/monitoring/components/MonitoringFiltersPanel', () => ({
  MonitoringFiltersPanel: () => null,
}));
vi.mock('@/features/monitoring/components/UsageImportProgressModal', () => ({
  UsageImportProgressModal: () => null,
}));
vi.mock('@/features/monitoring/components/MonitoringSummarySection', () => ({
  MonitoringSummarySection: () => null,
}));

vi.mock('@/features/monitoring/components/MonitoringStatusHeader', () => ({
  MonitoringStatusHeader: () => null,
  MonitoringStatusSummary: () => null,
}));
vi.mock('@/features/monitoring/components/RealtimeEventsPanel', () => ({
  RealtimeEventsPanel: () => null,
  RealtimeEventsPanelActions: () => null,
}));

const flushPromises = async () => {
  await Promise.resolve();
  await Promise.resolve();
};

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve;
  });
  return { promise, resolve };
};

describe('MonitoringCenterPage credential quota revision lifecycle', () => {
  let renderer!: ReactTestRenderer;
  let rendererMounted = false;
  // Use fake timers so bounded retry delays are deterministic and instant.
  let timersActivated = false;

  const successMetaPayload = (authFiles: AuthFileItem[]) => ({
    authFiles,
    authFilesLoaded: true as const,
    channels: [] as const,
    channelsLoaded: true as const,
    error: '',
  });

  beforeEach(async () => {
    useAccountCredentialMutationRevisionStore.getState().clearForTests();
    useQuotaStore.getState().clearQuotaCache();
    mocks.apiBase = 'http://cpa-a.local:8317';
    mocks.managementKey = 'manager-key-a';
    mocks.authFiles = [makeCodexFile('1', 'codex-old.json')];
    mocks.nextAuthFiles = null;
    mocks.lastAccountOverviewProps = null;
    mocks.lastHeaderRefresh = null;
    mocks.loadHeaderSnapshots.mockClear();
    // Default: refreshMeta resolves with a successful auth-files payload so the
    // production success-coverage path is exercised, not the failure path.
    mocks.refreshMeta.mockReset().mockImplementation(() => successMetaPayload(mocks.authFiles));
    mocks.refreshQuotaWithConfig
      .mockReset()
      .mockImplementation(
        async ({
          config,
          file,
          setQuota,
        }: {
          config: { getStoreKey?: (authFile: AuthFileItem) => string };
          file: AuthFileItem;
          setQuota: (updater: unknown) => void;
        }) => {
          const authIndex = String(file.authIndex ?? file['auth_index'] ?? '');
          const state = {
            status: 'success' as const,
            windows: [
              {
                id: 'weekly',
                label: 'Weekly',
                usedPercent: authIndex === '1' ? 90 : 10,
                resetLabel: '-',
              },
            ],
            quotaInventoryObserved: true,
            authFileKey: getQuotaCredentialStoreKey(file),
            authFileName: file.name,
            authIndex,
            authFileIdentityVerified: true,
            fetchedAtMs: Date.now(),
          };
          const storeKey = config.getStoreKey?.(file) ?? file.name;
          setQuota((previous: Record<string, unknown>) => ({
            ...previous,
            [storeKey]: state,
          }));
          return { status: 'success' as const, data: null, state };
        }
      );
    timersActivated = false;
    await act(async () => {
      renderer = create(<MonitoringCenterPage />);
      rendererMounted = true;
      await flushPromises();
    });
  });

  // Test 1: successful coverage triggers quota reload.
  it('invalidates mounted quota, rebuilds an authIndex-changed target, and bypasses stale cache', async () => {
    await act(async () => {
      await mocks.lastAccountOverviewProps?.onLoadAccountQuota('workspace-a', true);
    });
    expect(mocks.refreshQuotaWithConfig).toHaveBeenCalledTimes(1);
    expect(mocks.refreshQuotaWithConfig.mock.calls[0]?.[0].file.authIndex).toBe('1');
    expect(
      mocks.lastAccountOverviewProps?.accountQuotaStatesByRowId['workspace-a']?.entries[0]
        ?.windows[0]?.remainingPercent
    ).toBe(10);

    mocks.nextAuthFiles = [makeCodexFile('2', 'codex-new.json')];
    mocks.refreshMeta.mockImplementation(() => successMetaPayload(mocks.nextAuthFiles!));
    await act(async () => {
      publishAccountCredentialMutationRevision({
        connectionFingerprint: createCodexInspectionConnectionFingerprint(
          mocks.apiBase,
          mocks.managementKey
        )!,
        provider: 'codex',
        kind: 'reauth',
        credentialIdentity: 'workspace-a',
      });
      await flushPromises();
    });

    expect(mocks.refreshMeta).toHaveBeenCalledTimes(1);
    expect(mocks.refreshQuotaWithConfig).toHaveBeenCalledTimes(2);
    expect(mocks.refreshQuotaWithConfig.mock.calls[1]?.[0].file.authIndex).toBe('2');
    expect(
      mocks.lastAccountOverviewProps?.accountQuotaStatesByRowId['workspace-a']?.targetKey
    ).toContain('codex::2::codex-new.json');
    expect(
      mocks.lastAccountOverviewProps?.accountQuotaStatesByRowId['workspace-a']?.entries[0]
        ?.windows[0]?.remainingPercent
    ).toBe(90);
  });

  it('rerenders a mounted row from a shared Provider store update without refetching', async () => {
    const file = mocks.authFiles[0];
    if (!file) throw new Error('expected a mounted Codex credential');
    const storeKey = getQuotaCredentialStoreKey(file);

    expect(mocks.refreshQuotaWithConfig).not.toHaveBeenCalled();

    await act(async () => {
      useQuotaStore.getState().setCodexQuota({
        [storeKey]: {
          status: 'success',
          windows: [
            {
              id: 'weekly',
              label: 'Weekly',
              usedPercent: 40,
              resetLabel: 'tomorrow',
            },
          ],
          quotaInventoryObserved: true,
          authFileKey: storeKey,
          authFileName: file.name,
          authIndex: String(file.authIndex),
          authFileIdentityVerified: true,
          fetchedAtMs: 2_000,
        },
      });
      await flushPromises();
    });

    expect(mocks.refreshQuotaWithConfig).not.toHaveBeenCalled();
    expect(
      mocks.lastAccountOverviewProps?.accountQuotaStatesByRowId['workspace-a']?.entries[0]
        ?.windows[0]?.remainingPercent
    ).toBe(60);
  });

  // Test 2: auth-files failure does not reload quota until a retry succeeds.
  it('does not reload quota when auth-files fail and eventually covers via bounded retry', async () => {
    vi.useFakeTimers();
    timersActivated = true;

    // Pre-load quota so the row is mounted and the invalidation effect will
    // trigger a reload once coverage succeeds.
    await act(async () => {
      await mocks.lastAccountOverviewProps?.onLoadAccountQuota('workspace-a', true);
    });

    const fingerprint = createCodexInspectionConnectionFingerprint(
      mocks.apiBase,
      mocks.managementKey
    )!;
    const failPayload = {
      authFiles: [] as AuthFileItem[],
      authFilesLoaded: false as const,
      channels: [] as const,
      channelsLoaded: true as const,
      error: 'auth-files unavailable',
    };
    // The initial refresh and the 0ms retry both fail; the 1s retry succeeds.
    mocks.refreshMeta.mockReset();
    mocks.refreshMeta.mockReturnValueOnce(failPayload).mockReturnValueOnce(failPayload);
    mocks.nextAuthFiles = [makeCodexFile('2', 'codex-new.json')];
    mocks.refreshMeta.mockImplementation(() => successMetaPayload(mocks.nextAuthFiles!));

    await act(async () => {
      publishAccountCredentialMutationRevision({
        connectionFingerprint: fingerprint,
        provider: 'codex',
        kind: 'reauth',
        credentialIdentity: 'workspace-a',
      });
      await flushPromises();
    });

    // After the failed refresh, no mutation-driven quota reload should have
    // occurred — any shared quota refresh calls must still use the old authIndex.
    const callsAfterFailure = mocks.refreshQuotaWithConfig.mock.calls;
    expect(callsAfterFailure.some((call) => call[0]?.authIndex === '2')).toBe(false);

    // Advance through the bounded retry delays until the retry succeeds.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(8_000);
    });

    // Now the retry has succeeded and quota should be reloaded with authIndex=2.
    expect(
      mocks.refreshQuotaWithConfig.mock.calls.some((call) => call[0]?.file.authIndex === '2')
    ).toBe(true);

    vi.useRealTimers();
    timersActivated = false;
  });

  // Test 3: a newer revision during refresh triggers a serialized follow-up.
  it('runs a follow-up metadata refresh when a newer revision arrives during refresh', async () => {
    const firstRefresh = deferred<{
      authFiles: AuthFileItem[];
      authFilesLoaded: boolean;
      channels: [];
      channelsLoaded: boolean;
      error: string;
    }>();
    const secondRefresh = deferred<{
      authFiles: AuthFileItem[];
      authFilesLoaded: boolean;
      channels: [];
      channelsLoaded: boolean;
      error: string;
    }>();
    mocks.refreshMeta.mockReset();
    mocks.refreshMeta
      .mockReturnValueOnce(firstRefresh.promise)
      .mockReturnValueOnce(secondRefresh.promise);

    const fingerprint = createCodexInspectionConnectionFingerprint(
      mocks.apiBase,
      mocks.managementKey
    )!;
    await act(async () => {
      publishAccountCredentialMutationRevision({
        connectionFingerprint: fingerprint,
        provider: 'codex',
        kind: 'reauth',
      });
      await flushPromises();
    });
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(1);

    await act(async () => {
      publishAccountCredentialMutationRevision({
        connectionFingerprint: fingerprint,
        provider: 'codex',
        kind: 'reauth',
      });
      await flushPromises();
    });
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(1);

    await act(async () => {
      firstRefresh.resolve({
        authFiles: [makeCodexFile('1', 'codex-old.json')],
        authFilesLoaded: true,
        channels: [],
        channelsLoaded: true,
        error: '',
      });
      await flushPromises();
    });
    // First refresh covered rev1, but rev2 is still pending → follow-up.
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(2);

    await act(async () => {
      secondRefresh.resolve({
        authFiles: [makeCodexFile('2', 'codex-new.json')],
        authFilesLoaded: true,
        channels: [],
        channelsLoaded: true,
        error: '',
      });
      await flushPromises();
    });
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(2);
  });

  // Test 4: a newer revision during a retry refresh is not covered by the
  // earlier retry request's metadata response.
  it('does not cover a newer revision that arrives during a retry request', async () => {
    vi.useFakeTimers();
    timersActivated = true;

    await act(async () => {
      await mocks.lastAccountOverviewProps?.onLoadAccountQuota('workspace-a', true);
    });

    const fingerprint = createCodexInspectionConnectionFingerprint(
      mocks.apiBase,
      mocks.managementKey
    )!;
    const failPayload = {
      authFiles: [] as AuthFileItem[],
      authFilesLoaded: false as const,
      channels: [] as const,
      channelsLoaded: true as const,
      error: 'auth-files unavailable',
    };
    const retryRefresh = deferred<{
      authFiles: AuthFileItem[];
      authFilesLoaded: boolean;
      channels: [];
      channelsLoaded: boolean;
      error: string;
    }>();
    const finalRefresh = deferred<{
      authFiles: AuthFileItem[];
      authFilesLoaded: boolean;
      channels: [];
      channelsLoaded: boolean;
      error: string;
    }>();
    const retryFiles = [makeCodexFile('2', 'codex-retry.json')];
    const finalFiles = [makeCodexFile('3', 'codex-final.json')];
    mocks.refreshMeta.mockReset();
    mocks.refreshMeta
      .mockReturnValueOnce(failPayload)
      .mockReturnValueOnce(retryRefresh.promise)
      .mockImplementationOnce(() => {
        mocks.nextAuthFiles = finalFiles;
        return finalRefresh.promise;
      });

    await act(async () => {
      publishAccountCredentialMutationRevision({
        connectionFingerprint: fingerprint,
        provider: 'codex',
        kind: 'reauth',
        credentialIdentity: 'workspace-a',
      });
      await flushPromises();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
      await flushPromises();
    });
    // The initial failure has now entered the 0ms retry request.
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(2);

    await act(async () => {
      publishAccountCredentialMutationRevision({
        connectionFingerprint: fingerprint,
        provider: 'codex',
        kind: 'reauth',
        credentialIdentity: 'workspace-a',
      });
      await flushPromises();
    });
    // rev2 arrives while retry R is still in flight; it cannot start a
    // parallel request.
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(2);
    expect(mocks.refreshQuotaWithConfig).toHaveBeenCalledTimes(1);

    await act(async () => {
      mocks.nextAuthFiles = retryFiles;
      retryRefresh.resolve({
        authFiles: retryFiles,
        authFilesLoaded: true,
        channels: [],
        channelsLoaded: true,
        error: '',
      });
      await flushPromises();
    });
    // R covered only its rev1 snapshot, so rev2 requires a serialized
    // follow-up. Quota remains on the old target until that request completes.
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(3);
    expect(mocks.refreshQuotaWithConfig).toHaveBeenCalledTimes(1);

    await act(async () => {
      finalRefresh.resolve({
        authFiles: finalFiles,
        authFilesLoaded: true,
        channels: [],
        channelsLoaded: true,
        error: '',
      });
      await flushPromises();
    });
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(3);
    expect(mocks.refreshQuotaWithConfig).toHaveBeenCalledTimes(2);
    expect(mocks.refreshQuotaWithConfig.mock.calls[1]?.[0].file.authIndex).toBe('3');
    expect(
      mocks.lastAccountOverviewProps?.accountQuotaStatesByRowId['workspace-a']?.targetKey
    ).toContain('codex::3::codex-final.json');

    vi.useRealTimers();
    timersActivated = false;
  });

  // Test 5: a mutation refresh superseded by a same-scope newer metadata request
  // (returning null) eventually recovers via retry without a new mutation.
  it('recovers a superseded mutation refresh via retry without a new revision', async () => {
    vi.useFakeTimers();
    timersActivated = true;

    // Pre-load quota so the row is mounted.
    await act(async () => {
      await mocks.lastAccountOverviewProps?.onLoadAccountQuota('workspace-a', true);
    });

    const fingerprint = createCodexInspectionConnectionFingerprint(
      mocks.apiBase,
      mocks.managementKey
    )!;
    // First attempt returns null (superseded by generation fence). Subsequent
    // retry returns a successful payload.
    // The initial refresh and the 0ms retry both return null (superseded);
    // the 1s retry succeeds.
    mocks.refreshMeta.mockReset();
    mocks.refreshMeta.mockReturnValueOnce(null).mockReturnValueOnce(null);
    mocks.nextAuthFiles = [makeCodexFile('2', 'codex-new.json')];
    mocks.refreshMeta.mockImplementation(() => successMetaPayload(mocks.nextAuthFiles!));

    await act(async () => {
      publishAccountCredentialMutationRevision({
        connectionFingerprint: fingerprint,
        provider: 'codex',
        kind: 'reauth',
        credentialIdentity: 'workspace-a',
      });
      await flushPromises();
    });
    // Superseded: no mutation-driven reload with the new authIndex yet.
    expect(
      mocks.refreshQuotaWithConfig.mock.calls.some((call) => call[0]?.file.authIndex === '2')
    ).toBe(false);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(8_000);
    });
    // Retry succeeded → quota reloaded with authIndex=2.
    expect(
      mocks.refreshQuotaWithConfig.mock.calls.some((call) => call[0]?.file.authIndex === '2')
    ).toBe(true);

    vi.useRealTimers();
    timersActivated = false;
  });

  // Test 6: connection switch cancels an in-flight retry.
  it('cancels pending retry when the connection fingerprint changes', async () => {
    vi.useFakeTimers();
    timersActivated = true;

    const fingerprint = createCodexInspectionConnectionFingerprint(
      mocks.apiBase,
      mocks.managementKey
    )!;
    mocks.refreshMeta.mockReset().mockImplementation(() => ({
      authFiles: [],
      authFilesLoaded: false,
      channels: [],
      channelsLoaded: true,
      error: 'auth-files unavailable',
    }));

    await act(async () => {
      publishAccountCredentialMutationRevision({
        connectionFingerprint: fingerprint,
        provider: 'codex',
        kind: 'reauth',
        credentialIdentity: 'workspace-a',
      });
      await flushPromises();
    });
    expect(mocks.refreshQuotaWithConfig).not.toHaveBeenCalled();

    // Switch connection — generation bumps and old retries become no-ops.
    await act(async () => {
      mocks.apiBase = 'http://cpa-b.local:8317';
      mocks.managementKey = 'manager-key-b';
      mocks.refreshMeta.mockImplementation(() =>
        successMetaPayload([makeCodexFile('1', 'codex-old.json')])
      );
      renderer.update(<MonitoringCenterPage />);
      await vi.advanceTimersByTimeAsync(8_000);
    });

    // The old mutation's quota should not have been invalidated.
    expect(mocks.refreshQuotaWithConfig).not.toHaveBeenCalled();

    vi.useRealTimers();
    timersActivated = false;
  });

  // Test 7: unmount fences delayed mutation retries.
  it('stops credential mutation retries after unmount', async () => {
    vi.useFakeTimers();
    timersActivated = true;

    await act(async () => {
      await mocks.lastAccountOverviewProps?.onLoadAccountQuota('workspace-a', true);
    });

    const fingerprint = createCodexInspectionConnectionFingerprint(
      mocks.apiBase,
      mocks.managementKey
    )!;
    const failPayload = {
      authFiles: [] as AuthFileItem[],
      authFilesLoaded: false as const,
      channels: [] as const,
      channelsLoaded: true as const,
      error: 'auth-files unavailable',
    };
    mocks.refreshMeta.mockReset().mockImplementation(() => failPayload);

    await act(async () => {
      publishAccountCredentialMutationRevision({
        connectionFingerprint: fingerprint,
        provider: 'codex',
        kind: 'reauth',
        credentialIdentity: 'workspace-a',
      });
      await flushPromises();
    });
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(2);
    const callsBeforeUnmount = mocks.refreshMeta.mock.calls.length;
    const quotaCallsBeforeUnmount = mocks.refreshQuotaWithConfig.mock.calls.length;

    await act(async () => renderer.unmount());
    rendererMounted = false;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000);
      await flushPromises();
    });

    expect(mocks.refreshMeta).toHaveBeenCalledTimes(callsBeforeUnmount);
    expect(mocks.refreshQuotaWithConfig).toHaveBeenCalledTimes(quotaCallsBeforeUnmount);

    vi.useRealTimers();
    timersActivated = false;
  });

  // Test 8: a successful normal refresh re-arms an exhausted coverage cycle.
  it('re-arms an exhausted uncovered revision after a successful normal refresh', async () => {
    vi.useFakeTimers();
    timersActivated = true;

    await act(async () => {
      await mocks.lastAccountOverviewProps?.onLoadAccountQuota('workspace-a', true);
    });

    const fingerprint = createCodexInspectionConnectionFingerprint(
      mocks.apiBase,
      mocks.managementKey
    )!;
    const failPayload = {
      authFiles: [] as AuthFileItem[],
      authFilesLoaded: false as const,
      channels: [] as const,
      channelsLoaded: true as const,
      error: 'auth-files unavailable',
    };
    mocks.refreshMeta.mockReset().mockImplementation(() => failPayload);

    await act(async () => {
      publishAccountCredentialMutationRevision({
        connectionFingerprint: fingerprint,
        provider: 'codex',
        kind: 'reauth',
        credentialIdentity: 'workspace-a',
      });
      await flushPromises();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000);
      await flushPromises();
    });
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(6);
    expect(mocks.refreshQuotaWithConfig).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000);
      await flushPromises();
    });
    // Exhaustion is bounded; timers do not create a permanent retry loop.
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(6);

    const recoveryFiles = [makeCodexFile('2', 'codex-recovered.json')];
    const coverageRefresh = deferred<{
      authFiles: AuthFileItem[];
      authFilesLoaded: boolean;
      channels: [];
      channelsLoaded: boolean;
      error: string;
    }>();
    mocks.refreshMeta.mockReset();
    mocks.refreshMeta
      .mockImplementationOnce(() => {
        mocks.nextAuthFiles = recoveryFiles;
        return successMetaPayload(recoveryFiles);
      })
      .mockImplementationOnce(() => {
        mocks.nextAuthFiles = recoveryFiles;
        return coverageRefresh.promise;
      });

    const refreshHeader = mocks.lastHeaderRefresh;
    if (!refreshHeader) throw new Error('header refresh callback was not captured');
    await act(async () => {
      await refreshHeader();
      await flushPromises();
    });
    // refreshAll itself only discovers the stranded revision; the dedicated
    // coverage cycle is the second real metadata request.
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(2);
    expect(mocks.refreshQuotaWithConfig).toHaveBeenCalledTimes(1);

    await act(async () => {
      coverageRefresh.resolve({
        authFiles: recoveryFiles,
        authFilesLoaded: true,
        channels: [],
        channelsLoaded: true,
        error: '',
      });
      await flushPromises();
    });
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(2);
    expect(mocks.refreshQuotaWithConfig).toHaveBeenCalledTimes(2);
    expect(mocks.refreshQuotaWithConfig.mock.calls[1]?.[0].file.authIndex).toBe('2');
    expect(
      mocks.lastAccountOverviewProps?.accountQuotaStatesByRowId['workspace-a']?.targetKey
    ).toContain('codex::2::codex-recovered.json');

    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000);
      await flushPromises();
    });
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(2);

    vi.useRealTimers();
    timersActivated = false;
  });

  // Test 9: a newly re-armed coverage cycle invalidates older delayed retries.
  it('invalidates an older delayed retry when a normal refresh starts a new coverage cycle', async () => {
    vi.useFakeTimers();
    timersActivated = true;

    await act(async () => {
      await mocks.lastAccountOverviewProps?.onLoadAccountQuota('workspace-a', true);
    });

    const fingerprint = createCodexInspectionConnectionFingerprint(
      mocks.apiBase,
      mocks.managementKey
    )!;
    const failPayload = {
      authFiles: [] as AuthFileItem[],
      authFilesLoaded: false as const,
      channels: [] as const,
      channelsLoaded: true as const,
      error: 'auth-files unavailable',
    };
    const recoveryFiles = [makeCodexFile('2', 'codex-recovered.json')];
    const cycle2Refresh = deferred<ReturnType<typeof successMetaPayload>>();
    mocks.refreshMeta.mockReset();
    mocks.refreshMeta
      .mockReturnValueOnce(failPayload)
      .mockReturnValueOnce(failPayload)
      .mockImplementationOnce(() => {
        mocks.nextAuthFiles = recoveryFiles;
        return successMetaPayload(recoveryFiles);
      })
      .mockImplementationOnce(() => {
        mocks.nextAuthFiles = recoveryFiles;
        return cycle2Refresh.promise;
      });

    await act(async () => {
      publishAccountCredentialMutationRevision({
        connectionFingerprint: fingerprint,
        provider: 'codex',
        kind: 'reauth',
        credentialIdentity: 'workspace-a',
      });
      await flushPromises();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
      await flushPromises();
    });
    // C1 initial and 0ms retry failed; its 1s retry is now delayed.
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(2);

    const refreshHeader = mocks.lastHeaderRefresh;
    if (!refreshHeader) throw new Error('header refresh callback was not captured');
    await act(async () => {
      await refreshHeader();
      await flushPromises();
    });
    // The normal refresh is call 3; its successful result re-arms C2, call 4.
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(4);
    expect(mocks.refreshQuotaWithConfig).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
      await flushPromises();
    });
    // C1's delayed retry woke after C2 started, but its generation is stale.
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(4);
    expect(mocks.refreshQuotaWithConfig).toHaveBeenCalledTimes(1);

    await act(async () => {
      cycle2Refresh.resolve(successMetaPayload(recoveryFiles));
      await flushPromises();
    });
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(4);
    expect(mocks.refreshQuotaWithConfig).toHaveBeenCalledTimes(2);
    expect(mocks.refreshQuotaWithConfig.mock.calls[1]?.[0].file.authIndex).toBe('2');
    expect(
      mocks.lastAccountOverviewProps?.accountQuotaStatesByRowId['workspace-a']?.targetKey
    ).toContain('codex::2::codex-recovered.json');

    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000);
      await flushPromises();
    });
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(4);

    vi.useRealTimers();
    timersActivated = false;
  });

  afterEach(async () => {
    if (timersActivated) {
      vi.useRealTimers();
    }
    if (rendererMounted) {
      await act(async () => renderer.unmount());
      rendererMounted = false;
    }
  });
});
