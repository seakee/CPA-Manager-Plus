import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import type {
  DashboardSummaryResponse,
  UsageServiceStatus,
} from '@/services/api/usageService';
import type { ConnectionStatus } from '@/types';
import { DASHBOARD_UI_STATE_STORAGE_KEY } from './dashboardUiState';
import { DashboardPage } from './DashboardPage';
import { CollectorStatusCard } from './components/CollectorStatusCard';
import { HealthAlertsCard } from './components/HealthAlertsCard';
import { TrafficOverviewCard } from './components/TrafficOverviewCard';
import { UsageMetricsCard } from './components/UsageMetricsCard';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const { mocks } = vi.hoisted(() => ({
  mocks: {
    auth: {
      connectionStatus: 'connected' as ConnectionStatus,
      serverVersion: '8.0.55',
      serverBuildDate: '2026-11-10T00:00:00Z',
      apiBase: 'http://gw-1.local:8317',
      managementKey: 'key-1',
      sessionMode: 'manager_embedded' as const,
    },
    config: {
      debug: false,
      loggingToFile: true,
      requestRetry: 2,
      wsAuth: false,
      routingStrategy: 'round-robin',
      apiKeys: ['sk-1'],
    },
    models: {
      models: [{ id: 'gpt-5' }, { id: 'claude-4' }],
      loading: false,
      error: null as string | null,
      cache: { timestamp: 1 } as Record<string, unknown> | null,
    },
    featureAvailability: {
      checking: false,
      panelHostConfirmed: true,
      panelHostMode: 'manager_embedded' as const,
      panelBase: '',
      managerServiceBase: 'http://manager.local:18317',
      managerServiceAvailable: true,
      requestMonitoringAvailable: true,
      modelPricesAvailable: true,
      serverCodexInspectionAvailable: true,
      dockerSetupAvailable: true,
      externalManagerConfigAvailable: false,
      reason: '' as PanelFeatureAvailability['reason'],
    } as PanelFeatureAvailability,
    updates: {
      status: {
        current_version: '2.0.0-beta.1',
        channel: 'beta',
        state: 'up_to_date',
        stale: false,
      } as Record<string, unknown>,
      check: vi.fn(),
      available: true,
      busy: false,
      error: false,
    },
    // 16 API/Store spies tracked for T1-8 zero-extra-request invariant
    apiKeysList: vi.fn(),
    authFilesList: vi.fn(),
    getGeminiKeys: vi.fn(),
    getCodexConfigs: vi.fn(),
    getXAIConfigs: vi.fn(),
    getClaudeConfigs: vi.fn(),
    getOpenAIProviders: vi.fn(),
    fetchModels: vi.fn(),
    getSummary: vi.fn(),
    getStatus: vi.fn(),
    fetchErrorLogs: vi.fn(),
    getManagerConfig: vi.fn(),
    loadMonitoringMeta: vi.fn(),
    getApiKeyAliases: vi.fn(),
    checkLatest: vi.fn(),
    checkManagerUpdateIndex: vi.fn(),
    showNotification: vi.fn(),
    // ECharts lifecycle spies
    echartsInit: vi.fn(),
    echartsSetOption: vi.fn(),
    echartsResize: vi.fn(),
    echartsDispose: vi.fn(),
  },
}));

vi.mock('react-i18next', () => {
  const translate = (key: string, options?: Record<string, unknown>) => {
    if (options?.version !== undefined) return `${key}:${String(options.version)}`;
    if (options?.count !== undefined) return `${key}:${String(options.count)}`;
    if (options?.error !== undefined) return `${key}:${String(options.error)}`;
    if (options?.value !== undefined) return `${key}:${String(options.value)}`;
    return key;
  };
  const i18n = { language: 'en-US' };
  const translation = { t: translate, i18n };
  return {
    initReactI18next: { type: '3rdParty', init: () => undefined },
    useTranslation: () => translation,
  };
});

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (s: typeof mocks.auth) => unknown) => selector(mocks.auth),
  useConfigStore: (selector: (s: { config: typeof mocks.config }) => unknown) =>
    selector({ config: mocks.config }),
  useModelsStore: (
    selector: (s: typeof mocks.models & { fetchModels: typeof mocks.fetchModels }) => unknown
  ) => selector({ ...mocks.models, fetchModels: mocks.fetchModels }),
  useNotificationStore: (
    selector: (s: { showNotification: typeof mocks.showNotification }) => unknown
  ) => selector({ showNotification: mocks.showNotification }),
  useThemeStore: (selector: (s: { resolvedTheme: 'light' | 'dark' }) => unknown) =>
    selector({ resolvedTheme: 'light' }),
}));

vi.mock('@/hooks/useHeaderRefresh', () => ({
  useHeaderRefresh: () => undefined,
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => mocks.featureAvailability,
}));

vi.mock('@/features/system/ManagerUpdates', () => ({
  useManagerUpdates: () => mocks.updates,
}));

vi.mock('@/services/api', () => ({
  apiKeysApi: { list: mocks.apiKeysList },
  authFilesApi: { list: mocks.authFilesList },
  providersApi: {
    getGeminiKeys: mocks.getGeminiKeys,
    getCodexConfigs: mocks.getCodexConfigs,
    getXAIConfigs: mocks.getXAIConfigs,
    getClaudeConfigs: mocks.getClaudeConfigs,
    getOpenAIProviders: mocks.getOpenAIProviders,
  },
  versionApi: {
    checkLatest: mocks.checkLatest,
    checkManagerUpdateIndex: mocks.checkManagerUpdateIndex,
  },
}));

vi.mock('@/services/api/logs', () => ({
  logsApi: {
    fetchErrorLogs: mocks.fetchErrorLogs,
  },
}));

vi.mock('@/services/api/usageService', () => ({
  dashboardApi: {
    getSummary: mocks.getSummary,
  },
  usageServiceApi: {
    getStatus: mocks.getStatus,
    getManagerConfig: mocks.getManagerConfig,
    getApiKeyAliases: mocks.getApiKeyAliases,
  },
}));

vi.mock('@/features/monitoring/services/monitoringMetaService', () => ({
  loadMonitoringMetaPayload: mocks.loadMonitoringMeta,
}));

vi.mock('@/components/charts/echartsCore', () => ({
  echarts: {
    init: mocks.echartsInit,
  },
}));

const makeSummary = (overrides: Partial<DashboardSummaryResponse> = {}): DashboardSummaryResponse => ({
  generated_at_ms: 1763000000000,
  window: {
    today_start_ms: 1762992000000,
    now_ms: 1763000000000,
    rolling_30m_start_ms: 1762998200000,
  },
  today: {
    total_calls: 240,
    success_calls: 236,
    failure_calls: 4,
    success_rate: 0.9833,
    zero_token_calls: 2,
    total_tokens: 96000,
    input_tokens: 64000,
    output_tokens: 24000,
    reasoning_tokens: 2000,
    cached_tokens: 6000,
    cache_read_tokens: 6000,
    cache_creation_tokens: 0,
    total_cost: 2.85,
    average_latency_ms: 380,
  },
  rolling_30m: {
    total_calls: 45,
    total_tokens: 18000,
    rpm: 1.5,
    tpm: 600,
  },
  top_models_today: [
    { model: 'gpt-5', calls: 120, tokens: 60000, cost: 1.9, success_rate: 0.99 },
  ],
  model_cost_rank: [
    {
      model: 'gpt-5',
      calls: 120,
      tokens: 60000,
      cost: 1.9,
      success_rate: 0.99,
      cost_share: 0.67,
    },
  ],
  traffic_timeline: [
    {
      bucket_ms: 1762995600000,
      calls: 80,
      tokens: 32000,
      success: 79,
      failure: 1,
      calls_share: 0.33,
      tokens_share: 0.33,
      failure_rate: 0.0125,
    },
    {
      bucket_ms: 1762999200000,
      calls: 160,
      tokens: 64000,
      success: 157,
      failure: 3,
      calls_share: 0.67,
      tokens_share: 0.67,
      failure_rate: 0.01875,
    },
  ],
  today_request_health_timeline: {
    from_ms: 1762992000000,
    to_ms: 1763000000000,
    bucket_ms: 600000,
    total_calls: 240,
    success_calls: 236,
    failure_calls: 4,
    success_rate: 0.9833,
    points: [
      {
        bucket_ms: 1762995600000,
        calls: 80,
        tokens: 32000,
        success: 79,
        failure: 1,
        success_rate: 0.9875,
        failure_rate: 0.0125,
        tone: 'good',
        intensity: 0.8,
        future: false,
      },
    ],
  },
  token_mix: [
    { key: 'input', tokens: 64000, share: 0.667 },
    { key: 'output', tokens: 24000, share: 0.25 },
  ],
  channel_health: [
    {
      source: 'openai',
      auth_index: 'auth-1',
      calls: 240,
      failures: 4,
      failure_rate: 0.0167,
      success_rate: 0.9833,
      tokens: 96000,
      cost: 2.85,
      average_latency_ms: 380,
      tone: 'good',
    },
  ],
  recent_failures: [],
  ...overrides,
});

const createDeferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
};

const getText = (node: ReactTestInstance): string =>
  node.children.map((child) => (typeof child === 'string' ? child : getText(child))).join(' ');

const getAllSixteenSpies = () => [
  mocks.apiKeysList,
  mocks.authFilesList,
  mocks.getGeminiKeys,
  mocks.getCodexConfigs,
  mocks.getXAIConfigs,
  mocks.getClaudeConfigs,
  mocks.getOpenAIProviders,
  mocks.fetchModels,
  mocks.getSummary,
  mocks.getStatus,
  mocks.fetchErrorLogs,
  mocks.getManagerConfig,
  mocks.loadMonitoringMeta,
  mocks.getApiKeyAliases,
  mocks.checkLatest,
  mocks.checkManagerUpdateIndex,
];

interface StorageSpy {
  getItem: ReturnType<typeof vi.fn<(key: string) => string | null>>;
  setItem: ReturnType<typeof vi.fn<(key: string, value: string) => void>>;
  removeItem: ReturnType<typeof vi.fn<(key: string) => void>>;
  clear: ReturnType<typeof vi.fn<() => void>>;
}

const createStorageSpy = (initial: Record<string, string> = {}): StorageSpy => {
  const map = new Map<string, string>(Object.entries(initial));
  return {
    getItem: vi.fn((key: string) => map.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => {
      map.set(key, value);
    }),
    removeItem: vi.fn((key: string) => {
      map.delete(key);
    }),
    clear: vi.fn(() => {
      map.clear();
    }),
  };
};

let renderer: ReactTestRenderer | null = null;
let storageSpy = createStorageSpy();

const flushPromises = async () => {
  for (let index = 0; index < 6; index += 1) {
    await Promise.resolve();
  }
};

const mountDashboard = async () => {
  await act(async () => {
    renderer = create(
      <MemoryRouter>
        <DashboardPage />
      </MemoryRouter>,
      {
        createNodeMock: (element) =>
          element.type === 'div'
            ? {
                clientWidth: 640,
                clientHeight: 240,
              }
            : null,
      }
    );
    await flushPromises();
  });
  if (!renderer) {
    throw new Error('Failed to mount DashboardPage');
  }
  return renderer;
};

const clickDisplayModeTab = async (root: ReactTestRenderer, mode: 'simple' | 'advanced') => {
  const labelKey =
    mode === 'simple' ? 'dashboard.display_mode_simple' : 'dashboard.display_mode_advanced';
  const tabButton = root.root.find(
    (node) => node.type === 'button' && node.props.role === 'tab' && getText(node).includes(labelKey)
  );
  await act(async () => {
    tabButton.props.onClick({ preventDefault: () => undefined });
    await flushPromises();
  });
};

const findDetailsToggleButton = (root: ReactTestRenderer) =>
  root.root.find(
    (node) =>
      node.type === 'button' &&
      typeof node.props['aria-expanded'] === 'boolean' &&
      (getText(node).includes('dashboard.expand_system_details') ||
        getText(node).includes('dashboard.collapse_system_details'))
  );

const installWindowStub = (localStorage: StorageSpy) => {
  vi.stubGlobal('window', {
    localStorage,
    setInterval,
    clearInterval,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  });
};

describe('Dashboard Simple / Advanced display mode pilot (Phase 1)', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    storageSpy = createStorageSpy();
    installWindowStub(storageSpy);

    mocks.auth = {
      connectionStatus: 'connected',
      serverVersion: '8.0.55',
      serverBuildDate: '2026-11-10T00:00:00Z',
      apiBase: 'http://gw-1.local:8317',
      managementKey: 'key-1',
      sessionMode: 'manager_embedded',
    };
    mocks.featureAvailability = {
      checking: false,
      panelHostConfirmed: true,
      panelHostMode: 'manager_embedded',
      panelBase: '',
      managerServiceBase: 'http://manager.local:18317',
      managerServiceAvailable: true,
      requestMonitoringAvailable: true,
      modelPricesAvailable: true,
      serverCodexInspectionAvailable: true,
      dockerSetupAvailable: true,
      externalManagerConfigAvailable: false,
      reason: '',
    };
    mocks.updates.status = {
      current_version: '2.0.0-beta.1',
      channel: 'beta',
      state: 'up_to_date',
      stale: false,
    };
    mocks.updates.available = true;
    mocks.updates.error = false;

    mocks.echartsInit.mockImplementation(() => ({
      setOption: mocks.echartsSetOption,
      on: vi.fn(),
      off: vi.fn(),
      resize: mocks.echartsResize,
      dispose: mocks.echartsDispose,
    }));

    mocks.apiKeysList.mockResolvedValue(['sk-1']);
    mocks.authFilesList.mockResolvedValue({ files: [{ name: 'codex-1.json' }] });
    mocks.getGeminiKeys.mockResolvedValue([]);
    mocks.getCodexConfigs.mockResolvedValue([{ apiKey: 'cx-1' }]);
    mocks.getXAIConfigs.mockResolvedValue([]);
    mocks.getClaudeConfigs.mockResolvedValue([]);
    mocks.getOpenAIProviders.mockResolvedValue([]);
    mocks.fetchModels.mockResolvedValue(undefined);
    mocks.getSummary.mockResolvedValue(makeSummary());
    mocks.getStatus.mockResolvedValue({
      events: 120,
      deadLetters: 0,
      collector: {
        collector: 'running',
        mode: 'redis',
        queue: 'cpa:usage',
        deadLetters: 0,
      },
    } satisfies UsageServiceStatus);
    mocks.fetchErrorLogs.mockResolvedValue({ files: [] });
    mocks.getManagerConfig.mockResolvedValue({
      config: { cpaConnection: { cpaBaseUrl: 'http://cpa.local:8317' } },
    });
    mocks.loadMonitoringMeta.mockResolvedValue({
      authFiles: [],
      authFilesLoaded: true,
      channels: [],
      channelsLoaded: true,
      error: '',
    });
    mocks.getApiKeyAliases.mockResolvedValue({ items: [] });
    mocks.checkLatest.mockResolvedValue({ 'latest-version': '8.0.56' });
    mocks.checkManagerUpdateIndex.mockResolvedValue({
      schema_version: 1,
      channels: { stable: { version: '2.0.0-beta.1' } },
    });
  });

  afterEach(() => {
    if (renderer) {
      act(() => renderer?.unmount());
      renderer = null;
    }
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it('T1-1 & T1-8: defaults to advanced with zero storage writes on mount/poll/expand, keeps VersionCard & ECharts alive, and triggers zero extra API requests across mode toggles', async () => {
    const view = await mountDashboard();

    const dashboardUiWrites = () =>
      storageSpy.setItem.mock.calls.filter(([key]) => key === DASHBOARD_UI_STATE_STORAGE_KEY);

    // Mount in default advanced mode: 0 writes to dashboard.uiState
    expect(dashboardUiWrites()).toHaveLength(0);
    expect(view.root.findAllByType(UsageMetricsCard)).toHaveLength(2);
    expect(view.root.findAllByType(TrafficOverviewCard)).toHaveLength(1);
    expect(view.root.findAllByType(HealthAlertsCard)).toHaveLength(1);
    expect(view.root.findAllByType(CollectorStatusCard)).toHaveLength(1);
    expect(mocks.echartsInit).toHaveBeenCalledTimes(1);
    expect(mocks.checkLatest).toHaveBeenCalledTimes(1);
    expect(getText(view.root)).toContain('dashboard.version_update_available:v8.0.56');

    const baselineCallCounts = getAllSixteenSpies().map((spy) => spy.mock.calls.length);

    // Switch advanced -> simple inside < 60s window
    await clickDisplayModeTab(view, 'simple');
    expect(dashboardUiWrites()).toHaveLength(1);
    expect(dashboardUiWrites()[0]?.[1]).toBe(
      JSON.stringify({ version: 1, displayMode: 'simple' })
    );

    const simpleText = getText(view.root);
    // Compact health strip confirmed healthy + Core update badge preserved + no "up to date" noise
    expect(simpleText).toContain('dashboard.compact_health_healthy');
    expect(simpleText).toContain('dashboard.version_update_available:v8.0.56');
    expect(simpleText).not.toContain('dashboard.version_is_latest');

    // Simple mode hides API Base when connected, hides 30m RPM/TPM, hides 10m health timeline, hides model rank & healthy collector card, and keeps HealthAlertsCard
    expect(simpleText).not.toContain('dashboard.api_base');
    expect(simpleText).toContain('dashboard.today_requests');
    expect(simpleText).toContain('dashboard.success_rate');
    expect(simpleText).toContain('dashboard.today_cost');
    expect(simpleText).toContain('dashboard.avg_latency');
    expect(simpleText).not.toContain('dashboard.rpm_30m');
    expect(simpleText).not.toContain('dashboard.tpm_30m');
    expect(simpleText).not.toContain('dashboard.metric_zero_token_calls');
    expect(simpleText).not.toContain('dashboard.request_health_timeline');
    expect(simpleText).toContain('dashboard.channel_health_status');
    expect(simpleText).toContain('dashboard.recent_failed_requests');
    expect(simpleText).not.toContain('dashboard.model_cost_rank_today');
    expect(view.root.findAllByType(CollectorStatusCard)).toHaveLength(0);
    expect(simpleText).not.toContain('dashboard.current_config_summary');

    // Expand details -> CollectorStatusCard & Config Summary appear, 0 additional storage writes
    const expandBtn = findDetailsToggleButton(view);
    expect(expandBtn.props['aria-expanded']).toBe(false);
    await act(async () => {
      expandBtn.props.onClick();
      await flushPromises();
    });
    expect(findDetailsToggleButton(view).props['aria-expanded']).toBe(true);
    expect(view.root.findAllByType(CollectorStatusCard)).toHaveLength(1);
    expect(getText(view.root)).toContain('dashboard.current_config_summary');
    expect(dashboardUiWrites()).toHaveLength(1);

    // Collapse details -> back to compact strip
    await act(async () => {
      findDetailsToggleButton(view).props.onClick();
      await flushPromises();
    });
    expect(findDetailsToggleButton(view).props['aria-expanded']).toBe(false);
    expect(view.root.findAllByType(CollectorStatusCard)).toHaveLength(0);

    // Switch simple -> advanced -> all 16 spies have 0 incremental calls, ECharts never disposed/re-inited
    await clickDisplayModeTab(view, 'advanced');
    expect(dashboardUiWrites()).toHaveLength(2);
    expect(getAllSixteenSpies().map((spy) => spy.mock.calls.length)).toEqual(baselineCallCounts);
    expect(mocks.echartsInit).toHaveBeenCalledTimes(1);
    expect(mocks.echartsDispose).toHaveBeenCalledTimes(0);
    expect(mocks.checkLatest).toHaveBeenCalledTimes(1);
    expect(getText(view.root)).toContain('dashboard.version_update_available:v8.0.56');

    // Disconnected gateway in simple mode auto-exposes API Base and attention status
    await clickDisplayModeTab(view, 'simple');
    mocks.auth.connectionStatus = 'disconnected';
    await act(async () => {
      view.update(
        <MemoryRouter>
          <DashboardPage />
        </MemoryRouter>
      );
      await flushPromises();
    });
    expect(getText(view.root)).toContain('dashboard.api_base');
    expect(getText(view.root)).toContain('dashboard.compact_health_attention');
    expect(getText(view.root)).not.toContain('dashboard.compact_health_healthy');
  });

  it('T1-2: never renders confirmed healthy in simple mode when collector status is null, incomplete, starting, or in an unknown phase', async () => {
    storageSpy = createStorageSpy({
      [DASHBOARD_UI_STATE_STORAGE_KEY]: JSON.stringify({ version: 1, displayMode: 'simple' }),
    });
    installWindowStub(storageSpy);

    for (const [statusPayload, expectedCopy] of [
      [null, 'dashboard.compact_health_unconfirmed'],
      [{ events: 10 }, 'dashboard.compact_health_unconfirmed'],
      [{ collector: {} }, 'dashboard.compact_health_unconfirmed'],
      [{ collector: { collector: 'starting' } }, 'dashboard.compact_health_checking'],
      [{ collector: { collector: 'reconnecting' } }, 'dashboard.compact_health_unconfirmed'],
    ] as const) {
      mocks.getStatus.mockResolvedValueOnce(statusPayload);
      const view = await mountDashboard();
      const text = getText(view.root);
      expect(text).toContain(expectedCopy);
      expect(text).not.toContain('dashboard.compact_health_healthy');
      act(() => view.unmount());
      renderer = null;
    }
  });

  it('T1-3: preserves confirmed alerts and ECharts instance across same-connection capability re-probe and isolates cross-connection switches', async () => {
    storageSpy = createStorageSpy({
      [DASHBOARD_UI_STATE_STORAGE_KEY]: JSON.stringify({ version: 1, displayMode: 'simple' }),
    });
    installWindowStub(storageSpy);

    mocks.getStatus.mockResolvedValue({
      events: 25,
      collector: {
        collector: 'running',
        lastError: 'redis stream boom',
      },
    } satisfies UsageServiceStatus);
    mocks.fetchErrorLogs.mockResolvedValue({
      files: [{ name: 'err-1.log', size: 120, modTime: '2026-11-13T00:00:00Z' }],
    });

    const view = await mountDashboard();
    expect(getText(view.root)).toContain('dashboard.compact_health_attention');
    expect(getText(view.root)).toContain('redis stream boom');
    expect(view.root.findAllByType(CollectorStatusCard)).toHaveLength(1);
    expect(
      view.root.findAll((node) => node.type === 'a' && node.props.href === '/logs?tab=errors')
    ).toHaveLength(1);
    expect(mocks.echartsInit).toHaveBeenCalledTimes(1);

    // Same-connection capability re-probe (usePanelFeatureAvailability transiently sets checking=true, managerServiceBase='')
    mocks.featureAvailability = {
      ...mocks.featureAvailability,
      checking: true,
      managerServiceBase: '',
      requestMonitoringAvailable: false,
      reason: 'checking',
    };
    await act(async () => {
      view.update(
        <MemoryRouter>
          <DashboardPage />
        </MemoryRouter>
      );
      await flushPromises();
    });

    // Must NOT unmount DashboardPageContent, wipe alerts, or dispose ECharts
    expect(mocks.echartsDispose).toHaveBeenCalledTimes(0);
    expect(mocks.echartsInit).toHaveBeenCalledTimes(1);
    expect(getText(view.root)).toContain('dashboard.compact_health_attention');
    expect(getText(view.root)).toContain('redis stream boom');
    expect(view.root.findAllByType(CollectorStatusCard)).toHaveLength(1);

    // Now switch gateway connection (S1 -> S2) while S2 probes are pending: old S1 alerts MUST be isolated immediately
    const pendingSummaryS2 = createDeferred<DashboardSummaryResponse>();
    const pendingStatusS2 = createDeferred<UsageServiceStatus>();
    mocks.getSummary.mockReturnValueOnce(pendingSummaryS2.promise);
    mocks.getStatus.mockReturnValueOnce(pendingStatusS2.promise);
    mocks.fetchErrorLogs.mockResolvedValueOnce({ files: [] });
    mocks.auth = {
      ...mocks.auth,
      apiBase: 'http://gw-2.local:8317',
      managementKey: 'key-2',
    };
    mocks.featureAvailability = {
      ...mocks.featureAvailability,
      checking: false,
      managerServiceBase: 'http://manager-2.local:18317',
      requestMonitoringAvailable: true,
      reason: '',
    };

    await act(async () => {
      view.update(
        <MemoryRouter>
          <DashboardPage />
        </MemoryRouter>
      );
      await flushPromises();
    });

    const textAfterSwitch = getText(view.root);
    expect(textAfterSwitch).not.toContain('redis stream boom');
    expect(textAfterSwitch).not.toContain('dashboard.compact_health_attention');
    expect(textAfterSwitch).toContain('dashboard.compact_health_checking');
    expect(view.root.findAllByType(CollectorStatusCard)).toHaveLength(0);
  });

  it('T1-4 & P1-1: renders exact unavailable_not_applicable copy in simple mode when monitoring is disabled, unconfigured, or running in Panel mode with empty evidenceScope', async () => {
    storageSpy = createStorageSpy({
      [DASHBOARD_UI_STATE_STORAGE_KEY]: JSON.stringify({ version: 1, displayMode: 'simple' }),
    });
    installWindowStub(storageSpy);

    for (const [availabilityPatch, expectedCopy] of [
      [
        {
          panelHostMode: 'external_panel' as const,
          managerServiceBase: '',
          managerServiceAvailable: false,
          requestMonitoringAvailable: false,
          reason: 'service_not_configured' as const,
        },
        'dashboard.compact_health_panel_mode',
      ],
      [
        {
          panelHostMode: 'manager_embedded' as const,
          managerServiceBase: 'http://manager.local:18317',
          managerServiceAvailable: true,
          requestMonitoringAvailable: false,
          reason: 'monitoring_disabled' as const,
        },
        'dashboard.compact_health_monitoring_disabled',
      ],
      [
        {
          panelHostMode: 'manager_embedded' as const,
          managerServiceBase: '',
          managerServiceAvailable: true,
          requestMonitoringAvailable: false,
          reason: 'service_not_configured' as const,
        },
        'dashboard.compact_health_usage_not_configured',
      ],
    ] as const) {
      mocks.featureAvailability = {
        ...mocks.featureAvailability,
        checking: false,
        ...availabilityPatch,
      };
      const view = await mountDashboard();
      const text = getText(view.root);
      expect(text).toContain(expectedCopy);
      expect(text).not.toContain('dashboard.compact_health_checking');
      expect(text).not.toContain('dashboard.compact_health_healthy');
      act(() => view.unmount());
      renderer = null;
    }
  });

  it('T1-5 & P1-2: auto-expands CollectorStatusCard in simple mode and displays non-green warning/error status in both VersionCard and CollectorStatusCard across all collector anomalies', async () => {
    storageSpy = createStorageSpy({
      [DASHBOARD_UI_STATE_STORAGE_KEY]: JSON.stringify({ version: 1, displayMode: 'simple' }),
    });
    installWindowStub(storageSpy);

    for (const [statusPayload, expectedDiagnostic] of [
      [
        {
          events: 40,
          deadLetters: 0,
          collector: { collector: 'running', deadLetters: 3 },
        } satisfies UsageServiceStatus,
        'dashboard.collector_dead_letters_warning:3',
      ],
      [
        {
          events: 40,
          collector: { collector: 'stopped' },
        } satisfies UsageServiceStatus,
        'dashboard.collector_state_stopped',
      ],
      [
        {
          events: 40,
          collector: { collector: 'error' },
        } satisfies UsageServiceStatus,
        'dashboard.health_status_problem',
      ],
      [
        {
          events: 40,
          collector: { collector: 'running' },
          database: { checkpoint: { error: 'wal checkpoint busy' } },
        } satisfies UsageServiceStatus,
        'dashboard.collector_checkpoint_error:wal checkpoint busy',
      ],
    ] as const) {
      mocks.getStatus.mockResolvedValueOnce(statusPayload);
      const view = await mountDashboard();
      const collectorCards = view.root.findAllByType(CollectorStatusCard);
      expect(collectorCards).toHaveLength(1);

      const collectorText = getText(collectorCards[0]!);
      expect(collectorText).toContain(expectedDiagnostic);
      expect(collectorText).not.toContain('dashboard.health_status_normal');

      const pageText = getText(view.root);
      expect(pageText).toContain('dashboard.compact_health_attention');
      expect(pageText).not.toContain('dashboard.compact_health_healthy');

      act(() => view.unmount());
      renderer = null;
    }
  });

  it('T1-6 & P1-3: prevents 60s background refresh flicker, preserves detailsExpanded, and retains usage refresh error while next retry is in flight', async () => {
    storageSpy = createStorageSpy({
      [DASHBOARD_UI_STATE_STORAGE_KEY]: JSON.stringify({ version: 1, displayMode: 'simple' }),
    });
    installWindowStub(storageSpy);

    const view = await mountDashboard();
    expect(getText(view.root)).toContain('dashboard.compact_health_healthy');

    // Expand details, then trigger 60s background poll with pending promises -> detailsExpanded stays true, no storage write
    await act(async () => {
      findDetailsToggleButton(view).props.onClick();
      await flushPromises();
    });
    expect(findDetailsToggleButton(view).props['aria-expanded']).toBe(true);

    const poll2Summary = createDeferred<DashboardSummaryResponse>();
    mocks.getSummary.mockReturnValueOnce(poll2Summary.promise);
    await act(async () => {
      vi.advanceTimersByTime(60_000);
      await flushPromises();
    });
    expect(findDetailsToggleButton(view).props['aria-expanded']).toBe(true);

    // Collapse back to compact strip while poll #2 rejects with 502
    await act(async () => {
      findDetailsToggleButton(view).props.onClick();
      poll2Summary.reject(new Error('summary 502 bad gateway'));
      await flushPromises();
    });
    expect(getText(view.root)).toContain('dashboard.compact_health_attention');
    expect(getText(view.root)).toContain('summary 502 bad gateway');

    // Poll #3 starts at +60s and stays pending in flight -> P1-3: must remain attention_required, NOT flip back to confirmed_healthy
    const poll3Summary = createDeferred<DashboardSummaryResponse>();
    mocks.getSummary.mockReturnValueOnce(poll3Summary.promise);
    await act(async () => {
      vi.advanceTimersByTime(60_000);
      await flushPromises();
    });
    expect(getText(view.root)).toContain('dashboard.compact_health_attention');
    expect(getText(view.root)).not.toContain('dashboard.compact_health_healthy');

    // Poll #3 succeeds -> returns to confirmed_healthy and 0 writes to dashboard.uiState
    await act(async () => {
      poll3Summary.resolve(makeSummary());
      await flushPromises();
    });
    expect(getText(view.root)).toContain('dashboard.compact_health_healthy');
    expect(
      storageSpy.setItem.mock.calls.filter(([key]) => key === DASHBOARD_UI_STATE_STORAGE_KEY)
    ).toHaveLength(0);
  });
});
