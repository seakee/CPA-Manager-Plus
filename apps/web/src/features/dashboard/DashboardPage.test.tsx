import type { ReactNode } from 'react';
import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { DashboardPage } from './DashboardPage';

const mocks = vi.hoisted(() => ({
  auth: {
    apiBase: 'http://127.0.0.1:18517/cpamp/api/v2/cpamp',
    managementKey: 'secret',
    connectionStatus: 'connected' as const,
    serverVersion: '7.2.143',
    serverBuildDate: '2026-11-20T00:00:00Z',
    sessionMode: 'manager_embedded' as 'manager_embedded' | 'external_panel',
  },
  availability: {
    checking: false,
    panelHostConfirmed: true,
    panelHostMode: 'manager_embedded' as 'manager_embedded' | 'external_panel',
    panelBase: 'http://127.0.0.1:18517/cpamp',
    managerServiceBase: 'http://127.0.0.1:18517/cpamp',
    managerServiceAvailable: true,
    requestMonitoringAvailable: true,
    modelPricesAvailable: true,
    serverCodexInspectionAvailable: true,
    dockerSetupAvailable: false,
    externalManagerConfigAvailable: false,
    reason: '',
  },
  config: {
    debug: false,
    loggingToFile: true,
    requestRetry: 3,
    wsAuth: false,
    routingStrategy: 'round-robin',
    apiKeys: ['key-1'],
    geminiApiKeys: [],
    claudeApiKeys: [],
    codexApiKeys: [],
    xaiApiKeys: [],
    vertexApiKeys: [],
    openaiCompatibility: [],
  },
  models: {
    models: [{ name: 'gpt-4o' }, { name: 'claude-sonnet-4' }],
    loading: false,
    error: null as string | null,
    cache: { data: [{ name: 'gpt-4o' }], timestamp: 1, apiBase: '', apiKey: '' },
    fetchModels: vi.fn(async () => undefined),
  },
  usageSummary: {
    enabled: true,
    loading: false,
    error: '',
    summary: {
      window: { now_ms: 1_700_000_000_000 },
      today: {
        total_calls: 10,
        success_calls: 9,
        failure_calls: 1,
        success_rate: 0.9,
        total_tokens: 1000,
        total_cost: 0.25,
        average_latency_ms: 120,
        zero_token_calls: 0,
      },
      rolling_30m: { rpm: 1.5, tpm: 200, total_calls: 5, total_tokens: 500 },
    },
    recentFailures: [],
    topModels: [],
    modelCostRank: [],
    trafficTimeline: [],
    hourlyActivity: [],
    todayRequestHealthTimeline: null,
    tokenMix: [],
    channelHealth: [],
    failureSources: [],
    lastRefreshedAt: new Date('2026-11-23T12:00:00Z'),
    serviceBase: 'http://127.0.0.1:18517/cpamp',
    refresh: vi.fn(async () => undefined),
  },
  navigate: vi.fn(),
  t: (key: string, options?: Record<string, unknown>) => {
    if (key === 'dashboard.last_refreshed_at') {
      return `Refreshed at ${String(options?.time ?? '')}`;
    }
    return key;
  },
  i18n: { language: 'en' },
}));

vi.mock('react-router-dom', () => ({
  Link: ({ to, children, ...rest }: { to: string; children?: ReactNode; [k: string]: unknown }) => (
    <a href={to} {...rest}>
      {children}
    </a>
  ),
  useNavigate: () => mocks.navigate,
}));

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => undefined },
  useTranslation: () => ({
    t: mocks.t,
    i18n: mocks.i18n,
  }),
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: typeof mocks.auth) => unknown) => selector(mocks.auth),
  useConfigStore: (selector: (state: { config: typeof mocks.config }) => unknown) =>
    selector({ config: mocks.config }),
  useModelsStore: (selector: (state: typeof mocks.models) => unknown) => selector(mocks.models),
}));

const apiMocks = vi.hoisted(() => ({
  headerRefreshCallback: null as (() => Promise<void>) | null,
  apiKeysList: vi.fn(async () => ['k1', 'k2']),
  authFilesList: vi.fn(async () => ({ files: [{ name: 'a.json' }] })),
  loadAllProvidersV8Snapshot: undefined as (() => Promise<unknown>) | undefined,
  getGeminiKeys: vi.fn(async () => [{}, {}]),
  getInteractionsKeys: vi.fn(async () => []),
  getCodexConfigs: vi.fn(async () => [{}]),
  getXAIConfigs: vi.fn(async () => []),
  getClaudeConfigs: vi.fn(async () => [{}, {}, {}]),
  getVertexConfigs: vi.fn(async () => []),
  getMetaConfigs: vi.fn(async () => []),
  getOpenAIProviders: vi.fn(async () => []),
}));

vi.mock('@/hooks/useHeaderRefresh', () => ({
  useHeaderRefresh: (cb: () => Promise<void>) => {
    apiMocks.headerRefreshCallback = cb;
  },
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => mocks.availability,
}));

vi.mock('@/services/api', () => ({
  apiKeysApi: { list: apiMocks.apiKeysList },
  authFilesApi: { list: apiMocks.authFilesList },
  providersApi: {
    get loadAllProvidersV8Snapshot() {
      return apiMocks.loadAllProvidersV8Snapshot;
    },
    getGeminiKeys: apiMocks.getGeminiKeys,
    getInteractionsKeys: apiMocks.getInteractionsKeys,
    getCodexConfigs: apiMocks.getCodexConfigs,
    getXAIConfigs: apiMocks.getXAIConfigs,
    getClaudeConfigs: apiMocks.getClaudeConfigs,
    getVertexConfigs: apiMocks.getVertexConfigs,
    getMetaConfigs: apiMocks.getMetaConfigs,
    getOpenAIProviders: apiMocks.getOpenAIProviders,
  },
}));

vi.mock('@/services/api/logs', () => ({
  logsApi: { fetchErrorLogs: vi.fn(async () => ({ files: [] })) },
}));

vi.mock('@/services/api/usageService', () => ({
  usageServiceApi: {
    getStatus: vi.fn(async () => ({ events: 10, deadLetters: 0, collector: { queue: 'usage' } })),
    getManagerConfig: vi.fn(async () => ({
      config: { cpaConnection: { cpaBaseUrl: 'http://127.0.0.1:18518' } },
    })),
    getApiKeyAliases: vi.fn(async () => ({ items: [] })),
  },
}));

vi.mock('@/features/monitoring/services/monitoringMetaService', () => ({
  loadMonitoringMetaPayload: vi.fn(async () => ({
    authFiles: [],
    authFilesLoaded: true,
    channels: [],
    channelsLoaded: true,
    error: '',
  })),
}));
vi.stubGlobal('window', {
  setInterval: () => 0,
  clearInterval: () => undefined,
});

vi.mock('./hooks/useDashboardUsageSummary', () => ({
  useDashboardUsageSummary: () => mocks.usageSummary,
}));

vi.mock('./components/VersionCard', () => ({ VersionCard: () => <section data-testid="version" /> }));
vi.mock('./components/UsageMetricsCard', () => ({
  UsageMetricsCard: ({ mode }: { mode?: string }) => <section data-testid={`usage-${mode}`} />,
}));
vi.mock('./components/TrafficOverviewCard', () => ({
  TrafficOverviewCard: () => <section data-testid="traffic" />,
}));
vi.mock('./components/HealthAlertsCard', () => ({
  HealthAlertsCard: () => <section data-testid="health-alerts" />,
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const textOf = (node: ReactTestInstance): string =>
  node.children.map((child) => (typeof child === 'string' ? child : textOf(child))).join('');

let renderer: ReactTestRenderer | null = null;

beforeEach(() => {
  mocks.availability.panelHostMode = 'manager_embedded';
  mocks.availability.modelPricesAvailable = true;
  mocks.auth.sessionMode = 'manager_embedded';
  apiMocks.headerRefreshCallback = null;
  apiMocks.loadAllProvidersV8Snapshot = undefined;
  apiMocks.apiKeysList.mockReset().mockResolvedValue(['k1', 'k2']);
  apiMocks.authFilesList.mockReset().mockResolvedValue({ files: [{ name: 'a.json' }] });
  apiMocks.getGeminiKeys.mockReset().mockResolvedValue([{}, {}]);
  apiMocks.getInteractionsKeys.mockReset().mockResolvedValue([]);
  apiMocks.getCodexConfigs.mockReset().mockResolvedValue([{}]);
  apiMocks.getXAIConfigs.mockReset().mockResolvedValue([]);
  apiMocks.getClaudeConfigs.mockReset().mockResolvedValue([{}, {}, {}]);
  apiMocks.getVertexConfigs.mockReset().mockResolvedValue([]);
  apiMocks.getMetaConfigs.mockReset().mockResolvedValue([]);
  apiMocks.getOpenAIProviders.mockReset().mockResolvedValue([]);
});
afterEach(async () => {
  if (renderer) {
    await act(async () => {
      renderer?.unmount();
    });
    renderer = null;
  }
});

describe('DashboardPage P0 + P1 Plan A', () => {
  it('hides proxy API Base in Full Web header, fixes shortcut links, and renders readable provider breakdown', async () => {
    await act(async () => {
      renderer = create(<DashboardPage />);
      await Promise.resolve();
      await Promise.resolve();
    });

    const fullText = textOf(renderer!.root);
    expect(fullText).not.toContain('dashboard.api_base');
    expect(fullText).not.toContain('/cpamp/api/v2/cpamp');
    expect(fullText).toContain('Refreshed at ');
    expect(fullText).toContain('Gemini 2 · Codex 1 · Claude 3');
    expect(fullText).not.toContain('xAI');

    const links = renderer!.root.findAllByType('a').map((link) => ({
      href: link.props.href,
      text: textOf(link),
    }));
    expect(links.some((link) => link.href === '/api-keys' && link.text.includes('nav.api_keys'))).toBe(
      true
    );
    expect(
      links.some(
        (link) => link.href === '/model-prices' && link.text.includes('dashboard.available_models')
      )
    ).toBe(true);
    expect(
      links.some(
        (link) => link.href === '/config' && link.text.includes('dashboard.view_full_config')
      )
    ).toBe(true);
    expect(links.some((link) => link.href === '/system')).toBe(false);
  });

  it('falls back Available Models shortcut to /accounts when modelPricesAvailable is false and shows API Base in external panel mode', async () => {
    mocks.availability.panelHostMode = 'external_panel';
    mocks.availability.modelPricesAvailable = false;
    mocks.auth.sessionMode = 'external_panel';

    await act(async () => {
      renderer = create(<DashboardPage />);
      await Promise.resolve();
      await Promise.resolve();
    });

    const fullText = textOf(renderer!.root);
    expect(fullText).toContain('dashboard.api_base');

    const links = renderer!.root.findAllByType('a').map((link) => ({
      href: link.props.href,
      text: textOf(link),
    }));
    expect(
      links.some((link) => link.href === '/accounts' && link.text.includes('dashboard.available_models'))
    ).toBe(true);
    expect(links.some((link) => link.href === '/model-prices')).toBe(false);
  });

  it('keeps hasProviderStats false without a 0 sublabel during an initial 502 outage and preserves cached meta counts on refresh outage', async () => {
    const badGateway = () =>
      Promise.reject(Object.assign(new Error('Bad Gateway'), { status: 502 }));

    apiMocks.loadAllProvidersV8Snapshot = vi.fn(badGateway);
    apiMocks.apiKeysList.mockImplementation(badGateway);
    apiMocks.authFilesList.mockImplementation(badGateway);
    apiMocks.getGeminiKeys.mockImplementation(badGateway);
    apiMocks.getInteractionsKeys.mockImplementation(badGateway);
    apiMocks.getCodexConfigs.mockImplementation(badGateway);
    apiMocks.getXAIConfigs.mockImplementation(badGateway);
    apiMocks.getClaudeConfigs.mockImplementation(badGateway);
    apiMocks.getVertexConfigs.mockImplementation(badGateway);
    apiMocks.getMetaConfigs.mockImplementation(badGateway);
    apiMocks.getOpenAIProviders.mockImplementation(badGateway);

    await act(async () => {
      renderer = create(<DashboardPage />);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    const initialProvidersLink = renderer!.root
      .findAllByType('a')
      .find((link) => link.props.href === '/ai-providers');
    expect(initialProvidersLink).toBeDefined();
    const initialSpans = initialProvidersLink!.findAllByType('span').map(textOf);
    expect(initialSpans).toEqual(['nav.ai_providers', '-']);
    expect(textOf(renderer!.root)).toContain('notification.refresh_failed');

    await act(async () => {
      renderer?.unmount();
    });
    renderer = null;

    // Now verify cached meta count survives a subsequent total 502 outage during refresh.
    apiMocks.loadAllProvidersV8Snapshot = vi.fn(async () => ({
      gemini: [{}, {}],
      interactions: [],
      codex: [{}],
      xai: [],
      claude: [{}, {}, {}],
      vertex: [],
      meta: [{}, {}],
      openai: [],
      v8Supported: true,
    }));
    apiMocks.apiKeysList.mockResolvedValue(['k1']);
    apiMocks.authFilesList.mockResolvedValue({ files: [] });

    await act(async () => {
      renderer = create(<DashboardPage />);
      await Promise.resolve();
      await Promise.resolve();
    });

    const seededProvidersLink = renderer!.root
      .findAllByType('a')
      .find((link) => link.props.href === '/ai-providers');
    expect(seededProvidersLink!.findAllByType('span').map(textOf)).toEqual([
      'nav.ai_providers',
      '8',
      'Gemini 2 · Codex 1 · Claude 3 · Meta 2',
    ]);

    apiMocks.loadAllProvidersV8Snapshot = vi.fn(badGateway);
    await act(async () => {
      await apiMocks.headerRefreshCallback?.();
    });

    const postOutageProvidersLink = renderer!.root
      .findAllByType('a')
      .find((link) => link.props.href === '/ai-providers');
    expect(postOutageProvidersLink!.findAllByType('span').map(textOf)).toEqual([
      'nav.ai_providers',
      '8',
      'Gemini 2 · Codex 1 · Claude 3 · Meta 2',
    ]);
    expect(textOf(renderer!.root)).toContain('notification.refresh_failed');
  });
});
