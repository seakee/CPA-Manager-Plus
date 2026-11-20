import { type ReactNode } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ApiKeyAlias } from '@/services/api/usageService';
import { MonitoringCenterPage } from './MonitoringCenterPage';

(
  globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

const mocks = vi.hoisted(() => ({
  search: '?apiKeyId=key_prod',
  apiKeyAliases: [] as ApiKeyAlias[],
  apiKeyAliasesLoaded: true,
  apiKeyAliasesError: false,
  lastScopeApiKeyHash: '' as string | undefined,
  lastScopeApiKeyHashes: undefined as string[] | undefined,
  lastSuspendAnalytics: false,
  lastCombinedError: '',
  lastActiveDataTab: '',
  navigate: vi.fn(),
  lastClearFilters: undefined as (() => void) | undefined,
  lastApiKeyChange: undefined as ((value: string) => void) | undefined,
  unscopedActiveCalls: 0,
}));

vi.mock('react-router-dom', () => ({
  useLocation: () => ({
    pathname: '/monitoring',
    search: mocks.search,
    hash: '',
    state: null,
    key: 'test',
  }),
  useNavigate: () => mocks.navigate,
}));

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => undefined },
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string }) => options?.defaultValue || key,
    i18n: { language: 'en' },
  }),
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: Record<string, unknown>) => unknown) =>
    selector({
      apiBase: '/console/api/v2/cpamp',
      managementKey: 'manager-key',
      connectionStatus: 'connected',
    }),
  useConfigStore: (selector: (state: Record<string, unknown>) => unknown) =>
    selector({ config: { apiKeys: [] } }),
  useNotificationStore: (selector: (state: Record<string, unknown>) => unknown) =>
    selector({ showNotification: vi.fn(), showConfirmation: vi.fn() }),
  useQuotaStore: (selector: (state: Record<string, unknown>) => unknown) =>
    selector({
      antigravityQuota: {},
      claudeQuota: {},
      codexQuota: {},
      kimiQuota: {},
      xaiQuota: {},
      setAntigravityQuota: vi.fn(),
      setClaudeQuota: vi.fn(),
      setCodexQuota: vi.fn(),
      setKimiQuota: vi.fn(),
      setXaiQuota: vi.fn(),
    }),
}));

vi.mock('@/stores/useUsageHeaderSnapshotStore', () => ({
  useUsageHeaderSnapshotStore: (selector: (state: Record<string, unknown>) => unknown) =>
    selector({ items: [], generatedAtMs: 0 }),
}));

vi.mock('@/features/monitoring/hooks/useUsageData', () => ({
  useUsageData: () => ({
    loading: false,
    error: '',
    modelPrices: {},
    apiKeyAliases: mocks.apiKeyAliases,
    apiKeyAliasesLoaded: mocks.apiKeyAliasesLoaded,
    apiKeyAliasesError: mocks.apiKeyAliasesError,
    loadApiKeyAliases: vi.fn(async () => undefined),
    exportUsage: vi.fn(),
    importUsage: vi.fn(),
    cancelUsageImport: vi.fn(),
  }),
}));

vi.mock('@/features/monitoring/hooks/useMonitoringData', () => ({
  buildRealtimeMonitorRows: () => [],
  getRangeBounds: () => ({ startMs: 0, endMs: Date.now() }),
  useMonitoringData: (params: {
    scopeFilters?: { apiKeyHash?: string; apiKeyHashes?: string[] };
    suspendAnalytics?: boolean;
  }) => {
    mocks.lastScopeApiKeyHash = params?.scopeFilters?.apiKeyHash;
    mocks.lastScopeApiKeyHashes = params?.scopeFilters?.apiKeyHashes;
    mocks.lastSuspendAnalytics = Boolean(params?.suspendAnalytics);
    if (!params?.suspendAnalytics && params?.scopeFilters?.apiKeyHash === 'all') {
      mocks.unscopedActiveCalls += 1;
    }
    return {
      loading: false,
      error: '',
      authFiles: [],
      channels: [],
      summary: {
        totalCalls: 0,
        successCalls: 0,
        failureCalls: 0,
        successRate: 0,
        inputTokens: 0,
        outputTokens: 0,
        reasoningTokens: 0,
        cachedTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        totalTokens: 0,
        totalCost: 0,
        averageLatencyMs: null,
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
        totalAuthFiles: 0,
        activeAuthFiles: 0,
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
      accountRows: [],
      apiKeyRows: [],
      filterOptions: {
        accountRows: [],
        apiKeyRows: [],
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
      lastRefreshedAt: new Date(1),
      isTransitioningScope: false,
      hasPresentationSnapshot: true,
      refreshMeta: vi.fn(),
      loadMoreEvents: vi.fn(),
    };
  },
}));

vi.mock('@/features/monitoring/hooks/useHeaderSnapshotsLoader', () => ({
  useHeaderSnapshotsLoader: () => vi.fn(async () => undefined),
}));
vi.mock('@/hooks/useHeaderRefresh', () => ({
  useHeaderRefresh: vi.fn(),
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
vi.mock('@/features/monitoring/components/MonitoringDataPanel', () => ({
  MonitoringDataPanel: (props: { activeTab: string; renderContent: (tab: string) => ReactNode }) => {
    mocks.lastActiveDataTab = props.activeTab;
    return props.renderContent(props.activeTab);
  },
}));
vi.mock('@/features/monitoring/components/AccountOverviewPanel', () => ({
  AccountOverviewPanel: () => null,
  AccountOverviewPanelActions: () => null,
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
  MonitoringFiltersPanel: (props: {
    combinedError?: string;
    onClearFilters?: () => void;
    onApiKeyChange?: (value: string) => void;
  }) => {
    mocks.lastCombinedError = props.combinedError || '';
    mocks.lastClearFilters = props.onClearFilters;
    mocks.lastApiKeyChange = props.onApiKeyChange;
    return null;
  },
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

describe('MonitoringCenterPage ?apiKeyId deep link', () => {
  let renderer: ReactTestRenderer | null = null;

  beforeEach(() => {
    mocks.search = '?apiKeyId=key_prod';
    mocks.apiKeyAliasesLoaded = true;
    mocks.apiKeyAliasesError = false;
    mocks.lastScopeApiKeyHash = undefined;
    mocks.lastScopeApiKeyHashes = undefined;
    mocks.lastSuspendAnalytics = false;
    mocks.lastCombinedError = '';
    mocks.lastActiveDataTab = '';
    mocks.apiKeyAliases = [
      {
        apiKeyHash: 'hash-old-rotated',
        alias: 'Prod',
        updatedAtMs: 100,
        apiKeyId: 'key_prod',
        maskedPreview: 'cpamp_sk_…old1',
        origin: 'workspace',
        lifecycle: 'rotated',
      },
      {
        apiKeyHash: 'hash-active-current',
        alias: 'Prod',
        updatedAtMs: 200,
        apiKeyId: 'key_prod',
        maskedPreview: 'cpamp_sk_…new4',
        origin: 'workspace',
        lifecycle: 'active',
      },
    ];
  });

  afterEach(async () => {
    if (renderer) {
      await act(async () => renderer!.unmount());
      renderer = null;
    }
  });

  it('selects the matching active apiKeyHash for ?apiKeyId=<canonicalId> and opens realtime view', async () => {
    await act(async () => {
      renderer = create(<MonitoringCenterPage />);
      await Promise.resolve();
    });

    expect(mocks.lastScopeApiKeyHash).toBe('hash-active-current');
    expect(mocks.lastScopeApiKeyHashes).toEqual(['hash-old-rotated', 'hash-active-current']);
    expect(mocks.lastSuspendAnalytics).toBe(false);
    expect(mocks.lastActiveDataTab).toBe('realtime');
  });

  it('falls back to the latest superseded apiKeyHash when all hashes for ?apiKeyId are revoked', async () => {
    mocks.search = '?apiKeyId=key_revoked';
    mocks.apiKeyAliases = [
      {
        apiKeyHash: 'hash-revoked-older',
        alias: 'Legacy',
        updatedAtMs: 100,
        apiKeyId: 'key_revoked',
        maskedPreview: 'cpamp_sk_…1111',
        origin: 'workspace',
        lifecycle: 'superseded',
      },
      {
        apiKeyHash: 'hash-revoked-latest',
        alias: 'Legacy',
        updatedAtMs: 300,
        apiKeyId: 'key_revoked',
        maskedPreview: 'cpamp_sk_…2222',
        origin: 'workspace',
        lifecycle: 'superseded',
      },
    ];

    await act(async () => {
      renderer = create(<MonitoringCenterPage />);
      await Promise.resolve();
    });

    expect(mocks.lastScopeApiKeyHash).toBe('hash-revoked-latest');
    expect(mocks.lastActiveDataTab).toBe('realtime');
  });

  it('fails closed and suspends analytics when ?apiKeyId alias resolution fails', async () => {
    mocks.search = '?apiKeyId=key_prod';
    mocks.apiKeyAliases = [];
    mocks.apiKeyAliasesLoaded = false;
    mocks.apiKeyAliasesError = true;

    await act(async () => {
      renderer = create(<MonitoringCenterPage />);
      await Promise.resolve();
    });

    expect(mocks.lastSuspendAnalytics).toBe(true);
    expect(mocks.lastCombinedError).toContain('Failed to resolve Client API Key filter');
  });

  it('strips ?apiKeyId= from location search when clearFilters is invoked and avoids sentinel leakage', async () => {
    mocks.navigate.mockReset();
    mocks.navigate.mockImplementation((to: { search?: string } | string) => {
      if (typeof to === 'object' && to !== null) {
        mocks.search = to.search ?? '';
      }
    });
    mocks.search = '?apiKeyId=key_deleted_unknown';
    mocks.apiKeyAliases = [
      {
        apiKeyHash: 'hash-active-current',
        alias: 'Prod',
        updatedAtMs: 200,
        apiKeyId: 'key_prod',
        maskedPreview: 'cpamp_sk_…new4',
        origin: 'workspace',
        lifecycle: 'active',
      },
    ];

    await act(async () => {
      renderer = create(<MonitoringCenterPage />);
      await Promise.resolve();
    });

    expect(mocks.lastClearFilters).toBeTypeOf('function');
    await act(async () => {
      mocks.lastClearFilters?.();
      await Promise.resolve();
    });

    expect(mocks.navigate).toHaveBeenCalledWith(
      expect.objectContaining({ pathname: '/monitoring', search: '' }),
      { replace: true }
    );
    expect(mocks.lastScopeApiKeyHash).toBe('all');
  });

  it('suspends unscoped initial render before appliedApiKeyId commits and preserves manual <Select> change across deferred router transition', async () => {
    mocks.unscopedActiveCalls = 0;
    let commitDeferredNavigation: (() => void) | null = null;
    mocks.navigate.mockReset();
    mocks.navigate.mockImplementation((to: { search?: string } | string) => {
      commitDeferredNavigation = () => {
        if (typeof to === 'object' && to !== null) {
          mocks.search = to.search ?? '';
        }
      };
    });
    mocks.search = '?apiKeyId=key_prod';
    mocks.apiKeyAliasesLoaded = true;
    mocks.apiKeyAliasesError = false;
    mocks.apiKeyAliases = [
      {
        apiKeyHash: 'hash-prod',
        alias: 'Prod',
        updatedAtMs: 200,
        apiKeyId: 'key_prod',
        maskedPreview: 'cpamp_sk_…new4',
        origin: 'workspace',
        lifecycle: 'active',
      },
      {
        apiKeyHash: 'hash-staging',
        alias: 'Staging',
        updatedAtMs: 150,
        apiKeyId: 'key_staging',
        maskedPreview: 'cpamp_sk_…stg9',
        origin: 'workspace',
        lifecycle: 'active',
      },
    ];

    await act(async () => {
      renderer = create(<MonitoringCenterPage />);
      await Promise.resolve();
    });
    // Zero unscoped active analytics calls before appliedApiKeyId committed!
    expect(mocks.unscopedActiveCalls).toBe(0);
    expect(mocks.lastScopeApiKeyHash).toBe('hash-prod');

    // User manually selects 'hash-staging' BEFORE router transition commits location.search = ''!
    await act(async () => {
      mocks.lastApiKeyChange?.('hash-staging');
      await Promise.resolve();
    });
    expect(mocks.lastScopeApiKeyHash).toBe('hash-staging');
    expect(mocks.lastScopeApiKeyHashes).toBeUndefined();

    // Now router startTransition commits location.search = '' and re-renders: selection MUST remain 'hash-staging'!
    await act(async () => {
      commitDeferredNavigation?.();
      renderer?.update(<MonitoringCenterPage />);
      await Promise.resolve();
    });
    expect(mocks.lastScopeApiKeyHash).toBe('hash-staging');
    expect(mocks.lastScopeApiKeyHashes).toBeUndefined();
  });

  it('treats re-selecting active unmatched sentinel option in <Select> as a no-op without stripping ?apiKeyId=', async () => {
    mocks.navigate.mockReset();
    mocks.search = '?apiKeyId=key_unknown';
    mocks.apiKeyAliasesLoaded = true;
    mocks.apiKeyAliasesError = false;
    mocks.apiKeyAliases = [];

    await act(async () => {
      renderer = create(<MonitoringCenterPage />);
      await Promise.resolve();
    });
    expect(mocks.lastScopeApiKeyHash).toBe('__no_matching_api_key_id__');

    await act(async () => {
      mocks.lastApiKeyChange?.('__no_matching_api_key_id__');
      await Promise.resolve();
    });
    expect(mocks.navigate).not.toHaveBeenCalled();
    expect(mocks.lastScopeApiKeyHash).toBe('__no_matching_api_key_id__');
    expect(mocks.lastScopeApiKeyHashes).toEqual(['__no_matching_api_key_id__']);
  });
});
