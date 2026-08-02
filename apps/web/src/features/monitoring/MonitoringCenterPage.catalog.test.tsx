import type { ReactElement, ReactNode } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  listKeyPolicyKeys: vi.fn(),
  loadApiKeyAliases: vi.fn(async () => undefined),
  refreshMeta: vi.fn(async () => undefined),
  getHeaderSnapshots: vi.fn(async () => ({ items: [] })),
  headerRefreshHandler: null as null | (() => void | Promise<void>),
  intervalHandler: null as null | (() => void),
  intervalDelay: null as number | null,
  monitoringParams: null as null | Record<string, unknown>,
  dataPanelProps: null as null | Record<string, unknown>,
  statusHeaderProps: null as null | Record<string, unknown>,
  initialUiState: {} as Record<string, unknown>,
  monitoringResult: {} as Record<string, unknown>,
  supportsPlugin: true,
}));

vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>();
  return {
    ...actual,
    Link: ({ children }: { children?: ReactNode }) => children ?? null,
    useLocation: () => ({ search: '' }),
  };
});

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string, options?: Record<string, unknown>) =>
        options?.count === undefined ? key : `${key}:${String(options.count)}`,
      i18n: { language: 'en' },
    }),
  };
});

vi.mock('@/components/common/PageTransitionLayer', () => ({
  usePageTransitionLayer: () => ({ status: 'current' }),
}));

vi.mock('@/hooks/useHeaderRefresh', () => ({
  useHeaderRefresh: (handler: () => void | Promise<void>, enabled = true) => {
    mocks.headerRefreshHandler = enabled ? handler : null;
  },
}));

vi.mock('@/hooks/useInterval', () => ({
  useInterval: (handler: () => void, delay: number | null) => {
    mocks.intervalHandler = handler;
    mocks.intervalDelay = delay;
  },
}));

vi.mock('@/hooks/useRequestMonitoringAvailability', () => ({
  useRequestMonitoringAvailability: () => ({
    checking: false,
    available: true,
    reason: '',
    serviceBase: 'http://manager.local',
  }),
}));

vi.mock('@/stores', () => ({
  useConfigStore: (selector: (state: Record<string, unknown>) => unknown) =>
    selector({ config: { apiKeys: [] } }),
  useAuthStore: (selector: (state: Record<string, unknown>) => unknown) =>
    selector({
      connectionStatus: 'connected',
      managementKey: 'manager-key',
      supportsPlugin: mocks.supportsPlugin,
    }),
  useNotificationStore: (selector: (state: Record<string, unknown>) => unknown) =>
    selector({ showNotification: vi.fn(), showConfirmation: vi.fn() }),
}));

vi.mock('@/services/api/plugins', () => ({
  pluginsApi: { listKeyPolicyKeys: mocks.listKeyPolicyKeys },
}));

vi.mock('@/services/api/usageService', () => ({
  monitoringAnalyticsApi: { getHeaderSnapshots: mocks.getHeaderSnapshots },
}));

vi.mock('@/features/monitoring/hooks/useUsageData', () => ({
  useUsageData: () => ({
    loading: false,
    error: '',
    modelPrices: {},
    apiKeyAliases: [],
    loadApiKeyAliases: mocks.loadApiKeyAliases,
    exportUsage: vi.fn(),
    importUsage: vi.fn(),
    cancelUsageImport: vi.fn(),
  }),
}));

vi.mock('@/features/monitoring/hooks/useMonitoringData', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/monitoring/hooks/useMonitoringData')>();
  const emptySummary = {
    totalCalls: 0,
    successCalls: 0,
    failureCalls: 0,
    successRate: 1,
    inputTokens: 0,
    outputTokens: 0,
    reasoningTokens: 0,
    cachedTokens: 0,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
    totalTokens: 0,
    totalCost: 0,
    averageLatencyMs: 0,
    rpm30m: 0,
    tpm30m: 0,
    avgDailyRequests: 0,
    avgDailyTokens: 0,
    approxTasks: 0,
    approxTaskFailures: 0,
    approxTaskSuccessRate: 1,
    zeroTokenCalls: 0,
    zeroTokenModels: [],
  };
  return {
    ...actual,
    useMonitoringData: (params: Record<string, unknown>) => {
      mocks.monitoringParams = params;
      const base = {
        loading: false,
        error: '',
        authFiles: [],
        summary: emptySummary,
        accountRows: [],
        apiKeyRows: [],
        filterOptions: {
          accountRows: [],
          apiKeyRows: [],
          accountCount: 0,
          apiKeyCount: 0,
          providers: [],
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
        lastRefreshedAt: null,
        isTransitioningScope: false,
        hasPresentationSnapshot: true,
        refreshMeta: mocks.refreshMeta,
        loadMoreEvents: vi.fn(),
        ...mocks.monitoringResult,
      };
      // This mock models the fresh-data contract. Uncached stale-scope fallback
      // is covered at the hook boundary in useMonitoringData.test.ts.
      return {
        ...base,
        filteredRows: actual.buildScopeFilteredRows(
          base.filteredRows as Parameters<typeof actual.buildScopeFilteredRows>[0],
          params.scopeFilters as Parameters<typeof actual.buildScopeFilteredRows>[1]
        ),
      };
    },
  };
});

vi.mock('@/features/monitoring/monitoringCenterUiState', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/monitoring/monitoringCenterUiState')>();
  return {
    ...actual,
    readMonitoringCenterUiState: () => ({
      ...actual.getDefaultMonitoringCenterUiState(),
      ...mocks.initialUiState,
    }),
    writeMonitoringCenterUiState: vi.fn(),
  };
});

vi.mock('@/features/monitoring/components/MonitoringDataPanel', () => ({
  MonitoringDataPanel: (props: Record<string, unknown>) => {
    mocks.dataPanelProps = props;
    return null;
  },
}));

vi.mock('@/features/monitoring/components/MonitoringStatusHeader', async (importOriginal) => {
  const actual =
    await importOriginal<
      typeof import('@/features/monitoring/components/MonitoringStatusHeader')
    >();
  return {
    ...actual,
    MonitoringStatusHeader: (props: Record<string, unknown>) => {
      mocks.statusHeaderProps = props;
      return null;
    },
  };
});

import { MonitoringCenterPage } from './MonitoringCenterPage';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const flushEffects = async () => {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
};

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
};

describe('MonitoringCenterPage plugin key catalog lifecycle', () => {
  let renderer: ReactTestRenderer | null = null;

  beforeEach(() => {
    mocks.listKeyPolicyKeys.mockReset();
    mocks.loadApiKeyAliases.mockClear();
    mocks.refreshMeta.mockClear();
    mocks.getHeaderSnapshots.mockClear();
    mocks.headerRefreshHandler = null;
    mocks.intervalHandler = null;
    mocks.intervalDelay = null;
    mocks.monitoringParams = null;
    mocks.dataPanelProps = null;
    mocks.statusHeaderProps = null;
    mocks.initialUiState = {
      autoRefreshMs: '60000',
      selectedProvider: 'codex',
      selectedApiKeyHash: 'hash-one',
    };
    mocks.monitoringResult = {};
    mocks.supportsPlugin = true;
  });

  afterEach(() => {
    renderer?.unmount();
    renderer = null;
  });

  /** API Key summary actions currently rendered in the panel header. */
  const readApiKeyActionsProps = () => {
    act(() => {
      (mocks.dataPanelProps?.onTabChange as ((tab: string) => void) | undefined)?.('apiKeys');
    });
    return (
      mocks.dataPanelProps?.actions as
        | ReactElement<{ configuredCount?: number; pluginCatalogUnavailable?: boolean }>
        | undefined
    )?.props;
  };

  it('loads catalog initially and manually, excludes it from auto-refresh, and marks failed refresh unavailable', async () => {
    const catalog = [
      {
        id: 'plugin-one',
        name: 'Plugin One',
        enabled: true,
        keyPreview: 'cpa_..one',
        apiKeyHash: 'hash-one',
        source: 'plugin:cpa-key-policy',
      },
    ];
    mocks.listKeyPolicyKeys.mockResolvedValue({ status: 'ready', keys: catalog });

    await act(async () => {
      renderer = create(<MonitoringCenterPage />);
      await flushEffects();
    });

    expect(mocks.listKeyPolicyKeys).toHaveBeenCalledTimes(1);
    expect(mocks.monitoringParams).toMatchObject({ pluginKeyCatalog: catalog });
    expect(mocks.intervalDelay).toBe(60_000);

    await act(async () => {
      mocks.intervalHandler?.();
      await flushEffects();
    });
    expect(mocks.listKeyPolicyKeys).toHaveBeenCalledTimes(1);
    expect(mocks.refreshMeta).toHaveBeenCalledTimes(1);

    mocks.listKeyPolicyKeys.mockRejectedValueOnce(new Error('catalog unavailable'));
    await act(async () => {
      await mocks.headerRefreshHandler?.();
      await flushEffects();
    });
    expect(mocks.listKeyPolicyKeys).toHaveBeenCalledTimes(2);
    // Labels survive a failed refresh; only the configured count stops trusting it.
    expect(mocks.monitoringParams).toMatchObject({ pluginKeyCatalog: catalog });

    await act(async () => {
      (mocks.dataPanelProps?.onTabChange as ((tab: string) => void) | undefined)?.('apiKeys');
      await flushEffects();
    });
    const actions = mocks.dataPanelProps?.actions as
      | { props?: { configuredCount?: number; pluginCatalogUnavailable?: boolean } }
      | undefined;
    expect(actions?.props).toMatchObject({
      configuredCount: 0,
      pluginCatalogUnavailable: true,
    });
  });

  it('preserves non-key filters across tab switches and clears only the key filter', async () => {
    mocks.listKeyPolicyKeys.mockResolvedValue({ status: 'ready', keys: [] });
    await act(async () => {
      renderer = create(<MonitoringCenterPage />);
      await flushEffects();
    });

    expect(mocks.monitoringParams?.scopeFilters).toMatchObject({
      provider: 'codex',
      apiKeyHash: 'hash-one',
    });

    await act(async () => {
      (mocks.dataPanelProps?.onTabChange as ((tab: string) => void) | undefined)?.('apiKeys');
      await flushEffects();
    });
    expect(mocks.monitoringParams).toMatchObject({
      activeDataTab: 'apiKeys',
      scopeFilters: { provider: 'codex', apiKeyHash: 'hash-one' },
    });

    const actions = mocks.dataPanelProps?.actions as
      | { props?: { onClearApiKeyFilter?: () => void } }
      | undefined;
    await act(async () => {
      actions?.props?.onClearApiKeyFilter?.();
      await flushEffects();
    });
    expect(mocks.monitoringParams?.scopeFilters).toMatchObject({
      provider: 'codex',
      apiKeyHash: 'all',
    });
  });

  it('keeps realtime rows aligned with status controls and masks uncached scope transitions', async () => {
    const createEventRow = (id: string, failed: boolean) => ({
      id,
      timestamp: '2026-08-02T00:00:00.000Z',
      timestampMs: Date.parse('2026-08-02T00:00:00.000Z'),
      dayKey: '2026-08-02',
      hourLabel: '00:00',
      model: 'gpt-5.6-sol',
      endpoint: 'POST /v1/responses',
      endpointMethod: 'POST',
      endpointPath: '/v1/responses',
      sourceKey: 'source:test',
      source: 'test.json',
      sourceMasked: 't***',
      account: 'test@example.com',
      accountMasked: 't***@example.com',
      authIndex: 'auth-1',
      authIndexMasked: 'a***1',
      authLabel: 'test.json',
      projectId: '',
      apiKeyHash: 'hash-one',
      apiKeyLabel: 'Plugin One',
      apiKeyMasked: 'cpa_..one',
      provider: 'codex',
      planType: 'pro',
      channel: 'codex',
      channelHost: 'api.openai.com',
      channelDisabled: false,
      failed,
      statsIncluded: true,
      latencyMs: 1000,
      ttftMs: 200,
      tokensPerSecond: 10,
      inputTokens: 10,
      outputTokens: 5,
      reasoningTokens: 0,
      cachedTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      totalTokens: 15,
      totalCost: 0.01,
      taskKey: id,
      searchText: id,
    });
    const successRow = createEventRow('success-row', false);
    const failedRow = createEventRow('failed-row', true);
    mocks.initialUiState = {
      activeDataTab: 'realtime',
      selectedStatus: 'failed',
      selectedProvider: 'all',
      selectedApiKeyHash: 'all',
    };
    mocks.monitoringResult = {
      filteredRows: [successRow, failedRow],
      eventsLoadedCount: 2,
      eventsTotalCount: 2,
    };
    mocks.listKeyPolicyKeys.mockResolvedValue({ status: 'ready', keys: [] });

    await act(async () => {
      renderer = create(<MonitoringCenterPage />);
      await flushEffects();
    });

    const readRealtimeView = () => {
      const actions = mocks.dataPanelProps?.actions as
        | ReactElement<{
            statusFilter?: string;
            onStatusFilterChange?: (status: 'success' | 'failed') => void;
          }>
        | undefined;
      const renderContent = mocks.dataPanelProps?.renderContent as
        | ((tab: string) => ReactElement<{ rows?: Array<{ id: string }> }>)
        | undefined;
      return {
        actions,
        rows: renderContent?.('realtime').props.rows ?? [],
      };
    };

    expect(readRealtimeView().actions?.props.statusFilter).toBe('failed');
    expect(readRealtimeView().rows.map((row) => row.id)).toEqual(['failed-row']);

    await act(async () => {
      readRealtimeView().actions?.props.onStatusFilterChange?.('success');
      await flushEffects();
    });
    expect(mocks.monitoringParams?.scopeFilters).toMatchObject({ status: 'success' });
    expect(readRealtimeView().actions?.props.statusFilter).toBe('success');
    expect(readRealtimeView().rows.map((row) => row.id)).toEqual(['success-row']);

    await act(async () => {
      readRealtimeView().actions?.props.onStatusFilterChange?.('success');
      await flushEffects();
    });
    expect(mocks.monitoringParams?.scopeFilters).toMatchObject({ status: 'all' });
    expect(readRealtimeView().actions?.props.statusFilter).toBe('all');
    expect(readRealtimeView().rows.map((row) => row.id)).toEqual(['success-row', 'failed-row']);

    mocks.monitoringResult = {
      ...mocks.monitoringResult,
      loading: true,
      isTransitioningScope: true,
      hasPresentationSnapshot: false,
    };
    await act(async () => {
      renderer?.update(<MonitoringCenterPage />);
      await flushEffects();
    });
    expect(mocks.statusHeaderProps?.showLoadingOverlay).toBe(true);
  });

  it('uses a persisted event query limit and wires changes back into analytics requests', async () => {
    mocks.initialUiState = {
      activeDataTab: 'realtime',
      realtimeQueryLimit: 50,
      selectedProvider: 'all',
      selectedApiKeyHash: 'all',
    };
    mocks.listKeyPolicyKeys.mockResolvedValue({ status: 'ready', keys: [] });

    await act(async () => {
      renderer = create(<MonitoringCenterPage />);
      await flushEffects();
    });

    const readActions = () =>
      mocks.dataPanelProps?.actions as
        | ReactElement<{
            queryLimit?: number;
            onQueryLimitChange?: (limit: number) => void;
          }>
        | undefined;

    expect(mocks.monitoringParams).toMatchObject({ eventsPageLimit: 50 });
    expect(readActions()?.props.queryLimit).toBe(50);

    await act(async () => {
      readActions()?.props.onQueryLimitChange?.(100);
      await flushEffects();
    });

    expect(mocks.monitoringParams).toMatchObject({ eventsPageLimit: 100 });
    expect(readActions()?.props.queryLimit).toBe(100);
  });

  it('ignores an older catalog failure after a newer refresh succeeds', async () => {
    mocks.listKeyPolicyKeys.mockResolvedValue({ status: 'ready', keys: [] });
    await act(async () => {
      renderer = create(<MonitoringCenterPage />);
      await flushEffects();
    });

    const older = deferred<never>();
    const newer = deferred<{ status: 'ready'; keys: Array<Record<string, unknown>> }>();
    mocks.listKeyPolicyKeys
      .mockImplementationOnce(() => older.promise)
      .mockImplementationOnce(() => newer.promise);

    let olderRefresh: void | Promise<void> | undefined;
    let newerRefresh: void | Promise<void> | undefined;
    act(() => {
      olderRefresh = mocks.headerRefreshHandler?.();
      newerRefresh = mocks.headerRefreshHandler?.();
    });

    const currentCatalog = [
      {
        id: 'current',
        name: 'Current',
        enabled: true,
        keyPreview: 'cpa_..current',
        apiKeyHash: 'current-hash',
        source: 'plugin:cpa-key-policy',
      },
    ];
    await act(async () => {
      newer.resolve({ status: 'ready', keys: currentCatalog });
      await newerRefresh;
      await flushEffects();
    });
    await act(async () => {
      older.reject(new Error('older request failed'));
      await olderRefresh;
      await flushEffects();
    });

    expect(mocks.monitoringParams).toMatchObject({ pluginKeyCatalog: currentCatalog });
    expect(readApiKeyActionsProps()?.pluginCatalogUnavailable).toBe(false);
  });

  it('probes the catalog even before another response advertises generic plugin support', async () => {
    mocks.supportsPlugin = false;
    const catalog = [
      {
        id: 'capability-independent',
        name: 'Capability Independent',
        enabled: true,
        keyPreview: 'cpa_..ci',
        apiKeyHash: 'capability-independent-hash',
        source: 'plugin:cpa-key-policy',
      },
    ];
    mocks.listKeyPolicyKeys.mockResolvedValue({ status: 'ready', keys: catalog });
    await act(async () => {
      renderer = create(<MonitoringCenterPage />);
      await flushEffects();
    });

    expect(mocks.listKeyPolicyKeys).toHaveBeenCalledTimes(1);
    expect(mocks.monitoringParams).toMatchObject({ pluginKeyCatalog: catalog });
    expect(readApiKeyActionsProps()?.pluginCatalogUnavailable).toBe(false);
  });

  it('stays silent when the plugin endpoint reports the catalog is absent', async () => {
    mocks.listKeyPolicyKeys.mockResolvedValue({ status: 'absent' });
    await act(async () => {
      renderer = create(<MonitoringCenterPage />);
      await flushEffects();
    });

    expect(mocks.listKeyPolicyKeys).toHaveBeenCalledTimes(1);
    expect(mocks.monitoringParams).toMatchObject({ pluginKeyCatalog: [] });
    expect(readApiKeyActionsProps()?.pluginCatalogUnavailable).toBe(false);
  });
});
