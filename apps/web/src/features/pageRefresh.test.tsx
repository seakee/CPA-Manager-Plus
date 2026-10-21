import { useState, type ReactNode, type ReactElement } from 'react';
import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { LogsPage } from './logs/LogsPage';
import { PluginsPage } from './plugins/PluginsPage';
import { PluginResourcePage } from './plugins/PluginResourcePage';
import { SystemPage } from './system/SystemPage';
import { DashboardPage } from './dashboard/DashboardPage';
import { AccountActionCandidatesPage } from './monitoring/AccountActionCandidatesPage';
import { DatabaseStatusCard } from './system/components/DatabaseStatusCard';
import { VersionCard } from './dashboard/components/VersionCard';
import { UsageMetricsCard } from './dashboard/components/UsageMetricsCard';
import { TrafficOverviewCard } from './dashboard/components/TrafficOverviewCard';
import { HealthAlertsCard } from './dashboard/components/HealthAlertsCard';
import { AccountProcessingPolicySection } from './config/components/AccountProcessingPolicySection';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { DropdownMenu } from '@/components/ui/DropdownMenu';
import type { PluginListResponse, PluginStoreResponse } from '@/types';

const mocks = vi.hoisted(() => ({
  auth: {
    apiBase: 'https://a.test',
    managementKey: 'a',
    connectionStatus: 'connected',
    supportsPlugin: true,
  },
  managerBase: 'https://manager.test',
  params: new URLSearchParams(),
  t: (key: string, options?: { returnObjects?: boolean }) => options?.returnObjects ? [] : key,
  headerRefresh: undefined as undefined | (() => Promise<unknown>),
  plugins: vi.fn(),
  pluginStore: vi.fn(),
  updatePlugin: vi.fn(),
  installPlugin: vi.fn(),
  deletePlugin: vi.fn(),
  logs: vi.fn(),
  errorLogs: vi.fn(),
  status: vi.fn(),
  managerConfig: vi.fn(),
  aliases: vi.fn(),
  meta: vi.fn(),
  candidates: vi.fn(),
  ignoreCandidate: vi.fn(),
  deleteCandidate: vi.fn(),
  usageSummary: { loading: false, summary: null as object | null, error: '' },
  clearLogs: vi.fn(),
  confirmation: vi.fn(),
  keys: vi.fn(),
  files: vi.fn(),
  providers: vi.fn(),
  noop: vi.fn(),
  fetchModels: vi.fn(),
  fetchConfig: vi.fn(),
  models: {
    models: [{ name: 'gpt-retained' }], loading: false, error: null as string | null,
    cache: { data: [{ name: 'gpt-retained' }], timestamp: 1, apiBase: 'https://a.test', apiKey: 'key' },
  },
  config: { loggingToFile: true, requestLog: true, apiKeys: ['key'] },
  scrollNode: { scrollTop: 10, scrollHeight: 1000, clientHeight: 100 },
  requestScroll: vi.fn(),
  policy: vi.fn(), updatePolicy: vi.fn(),
}));
vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => {} },
  useTranslation: () => ({ t: mocks.t, i18n: { language: 'en' } }),
}));
vi.mock('react-router-dom', () => ({
  Link: ({ children }: { children: ReactNode }) => <a>{children}</a>,
  useNavigate: () => mocks.noop,
  useSearchParams: () => [mocks.params, mocks.noop],
  useParams: () => ({ pluginId: 'retained-plugin', menuIndex: '0' }),
}));
vi.mock('@/stores', () => ({
  useAuthStore: (selector?: (s: typeof mocks.auth) => unknown) =>
    selector ? selector(mocks.auth) : mocks.auth,
  useThemeStore: (selector: (s: object) => unknown) => selector({ resolvedTheme: 'light' }),
  useConfigStore: (selector: (s: object) => unknown) =>
    selector({ config: mocks.config, fetchConfig: mocks.fetchConfig, clearCache: mocks.noop }),
  useNotificationStore: (selector?: (s: object) => unknown) => {
    const state = { showNotification: mocks.noop, showConfirmation: mocks.confirmation };
    return selector ? selector(state) : state;
  },
  useModelsStore: (selector: (s: object) => unknown) =>
    selector({ ...mocks.models, fetchModels: mocks.fetchModels }),
}));
vi.mock('@/hooks/useHeaderRefresh', () => ({
  useHeaderRefresh: (refresh: () => Promise<unknown>) => {
    mocks.headerRefresh = refresh;
  },
}));
vi.mock('@/hooks/useLocalStorage', () => ({
  useLocalStorage: (_key: string, initial: unknown) => useState(initial),
}));
vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => ({
    checking: false,
    panelHostConfirmed: true,
    panelHostMode: 'manager_embedded',
    panelBase: mocks.auth.apiBase,
    managerServiceAvailable: true,
    managerServiceBase: mocks.managerBase,
    requestMonitoringAvailable: true,
  }),
}));
vi.mock('@/hooks/useRequestMonitoringAvailability', () => ({
  useRequestMonitoringAvailability: () => ({
    checking: false,
    available: true,
    serviceBase: mocks.managerBase,
  }),
}));
vi.mock('@/services/api', () => ({
  pluginsApi: { list: mocks.plugins, updateEnabled: mocks.updatePlugin, deletePlugin: mocks.deletePlugin },
  pluginStoreApi: { list: mocks.pluginStore, install: mocks.installPlugin },
  apiKeysApi: { list: mocks.keys },
  authFilesApi: { list: mocks.files },
  providersApi: {
    getGeminiKeys: mocks.providers,
    getCodexConfigs: mocks.providers,
    getXAIConfigs: mocks.providers,
    getClaudeConfigs: mocks.providers,
    getOpenAIProviders: mocks.providers,
  },
}));
vi.mock('@/services/api/apiKeys', () => ({ apiKeysApi: { list: mocks.keys } }));
vi.mock('@/services/api/logs', () => ({
  logsApi: { fetchLogs: mocks.logs, fetchErrorLogs: mocks.errorLogs, clearLogs: mocks.clearLogs },
}));
vi.mock('@/services/api/usageService', () => ({
  usageServiceApi: {
    getStatus: mocks.status,
    listAccountActionCandidates: mocks.candidates,
    ignoreAccountActionCandidate: mocks.ignoreCandidate,
    deleteAccountActionCandidateAuthFile: mocks.deleteCandidate,
    getManagerConfig: mocks.managerConfig,
    getApiKeyAliases: mocks.aliases,
    getAccountProcessingPolicy: mocks.policy, updateAccountProcessingPolicy: mocks.updatePolicy,
  },
}));
vi.mock('./monitoring/services/monitoringMetaService', () => ({
  loadMonitoringMetaPayload: mocks.meta,
}));
vi.mock('./dashboard/hooks/useDashboardUsageSummary', () => ({
  useDashboardUsageSummary: () => ({
    enabled: true,
    ...mocks.usageSummary,
    recentFailures: [],
    topModels: [],
    modelCostRank: [],
    trafficTimeline: [],
    hourlyActivity: [],
    tokenMix: [],
    channelHealth: [],
    failureSources: [],
    serviceBase: mocks.managerBase,
    refresh: mocks.noop,
  }),
}));
vi.mock('./system/ManagerUpdates', () => ({ useManagerUpdates: () => ({ available: false }) }));
vi.mock('./system/components/DatabaseStatusCard', () => ({
  DatabaseStatusCard: ({ status, error }: { status: unknown; error: string }) => (
    <section>
      {JSON.stringify(status)}
      {error}
    </section>
  ),
}));
vi.mock('./dashboard/components/VersionCard', () => ({
  VersionCard: ({
    collectorStatus,
    errorLogCount,
    collectorError,
    errorLogsError,
  }: {
    collectorStatus: unknown;
    errorLogCount: number;
    collectorError: string;
    errorLogsError: string;
  }) => (
    <section>
      {JSON.stringify(collectorStatus)}
      {errorLogCount}
      {collectorError}
      {errorLogsError}
    </section>
  ),
}));
vi.mock('./dashboard/components/UsageMetricsCard', () => ({ UsageMetricsCard: () => null }));
vi.mock('./dashboard/components/CollectorStatusCard', () => ({ CollectorStatusCard: () => null }));
vi.mock('./dashboard/components/HealthAlertsCard', () => ({ HealthAlertsCard: () => null }));
vi.mock('./dashboard/components/TrafficOverviewCard', () => ({ TrafficOverviewCard: () => null }));
vi.mock('./plugins/PluginStorePage', () => ({ PluginStorePage: () => null }));
vi.mock('./plugins/pluginHostStyle', () => ({ createPluginHostStyleBridge: () => null }));
vi.mock('./logs/hooks/useLogScroller', () => ({
  isNearBottom: (node: typeof mocks.scrollNode) =>
    !node || node.scrollHeight - node.scrollTop - node.clientHeight < 24,
  useLogScroller: () => ({
    logViewerRef: { current: mocks.scrollNode },
    canLoadMore: false,
    handleLogScroll: mocks.noop,
    requestScrollToBottom: mocks.requestScroll,
    scrollToBottom: mocks.noop,
  }),
}));
vi.mock('@/components/ui/Modal', () => ({ Modal: () => null }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
};
const text = (node: ReactTestInstance): string =>
  node.children.map((c) => (typeof c === 'string' ? c : text(c))).join('');
let renderer: ReactTestRenderer;
const mount = async (element: ReactElement) => {
  await act(async () => {
    renderer = create(element);
  });
};
const refresh = () => mocks.headerRefresh!();
beforeEach(() => {
  vi.stubGlobal('window', {
    setInterval,
    clearInterval,
    dispatchEvent: mocks.noop,
    setTimeout: mocks.noop,
    addEventListener: mocks.noop,
    removeEventListener: mocks.noop,
    location: { search: '' },
  });
  mocks.auth.managementKey = 'a';
  mocks.auth.apiBase = 'https://a.test';
  mocks.usageSummary = { loading: false, summary: null, error: '' };
  mocks.auth.connectionStatus = 'connected';
  mocks.auth.supportsPlugin = true;
  mocks.managerBase = 'https://manager.test';
  mocks.params = new URLSearchParams();
  mocks.models.loading = false;
  mocks.models.error = null;
  mocks.requestScroll.mockClear();
  mocks.fetchModels.mockResolvedValue(mocks.models.models);
  mocks.fetchConfig.mockResolvedValue({});
  mocks.plugins.mockReset();
  mocks.pluginStore.mockReset();
  mocks.installPlugin.mockReset().mockResolvedValue({ restartRequired: false });
  mocks.deletePlugin.mockReset();
  mocks.updatePlugin.mockReset();
  mocks.updatePlugin.mockResolvedValue(undefined);
  mocks.logs.mockReset();
  mocks.errorLogs.mockReset();
  mocks.status.mockReset();
  mocks.managerConfig.mockReset().mockResolvedValue({
    config: { cpaConnection: { cpaBaseUrl: 'https://cpa.last-good' } },
  });
  mocks.aliases.mockReset().mockResolvedValue({ items: [] });
  mocks.meta.mockReset().mockResolvedValue({
    authFiles: [],
    authFilesLoaded: true,
    channels: [],
    channelsLoaded: true,
    error: '',
  });
  mocks.candidates.mockReset();
  mocks.confirmation.mockReset();
  mocks.clearLogs.mockReset();
  mocks.clearLogs.mockResolvedValue(undefined);
  mocks.deleteCandidate.mockReset().mockResolvedValue({});
  mocks.ignoreCandidate.mockReset();
  mocks.keys.mockResolvedValue(['key']);
  mocks.files.mockResolvedValue({ files: [] });
  mocks.providers.mockResolvedValue([{}]);
  mocks.status.mockResolvedValue({ collector: { enabled: true }, marker: 'last-good' });
  mocks.errorLogs.mockResolvedValue({ files: [{ name: 'error-retained.log' }] });
});
afterEach(() => {
  act(() => renderer?.unmount());
  vi.unstubAllGlobals();
});

const pluginData: PluginListResponse = {
  pluginsEnabled: true,
  pluginsDir: 'plugins',
  plugins: [
    {
      id: 'retained-plugin',
      path: 'plugins/retained.so',
      configured: true,
      registered: true,
      enabled: true,
      effectiveEnabled: true,
      supportsOAuth: false,
      logo: '',
      configFields: [],
      metadata: { name: 'Retained Plugin', version: '1.0', author: 'test', configFields: [], githubRepository: 'test/plugin', logo: '' },
      menus: [{ menu: 'Resource', path: '/v0/resource/plugins/retained/page', description: '' }],
    },
  ],
};

const storeData: PluginStoreResponse = {
  pluginsEnabled: true,
  pluginsDir: 'plugins',
  sources: [],
  sourceErrors: [],
  plugins: [
    {
      storeId: 'official:retained-plugin',
      sourceId: 'official',
      sourceName: 'official',
      sourceUrl: '',
      id: 'retained-plugin',
      name: 'Retained Store Plugin',
      description: '',
      author: 'test',
      version: '2.0',
      repository: 'https://github.com/router-for-me/retained-plugin',
      installType: 'local',
      authRequired: false,
      authConfigured: true,
      platforms: [],
      logo: '',
      homepage: '',
      license: '',
      tags: [],
      installed: false,
      installedVersion: '',
      path: '',
      configured: false,
      registered: false,
      enabled: false,
      effectiveEnabled: false,
      updateAvailable: false,
    },
  ],
};

const installOptions = () =>
  renderer.root.find(
    (node) => typeof node.type === 'function' && node.type.name === 'PluginInstallOptionsModal'
  );
const clickButton = (label: string) =>
  renderer.root
    .findAllByType('button')
    .find((node) => text(node) === label)!
    .props.onClick();

describe('page refresh interactions', () => {
  it.each([
    { kind: 'logs', scope: 'apiBase' },
    { kind: 'logs', scope: 'managementKey' },
    { kind: 'candidates', scope: 'apiBase' },
    { kind: 'candidates', scope: 'managementKey' },
    { kind: 'candidates', scope: 'managerBase' },
  ] as const)(
    'invalidates a global destructive $kind confirmation across A to B to A ($scope)',
    async ({ kind, scope }) => {
      mocks.logs.mockResolvedValue({ lines: ['retained log'] });
      mocks.candidates.mockResolvedValue({ items: [{ id: 1, status: 'pending', accountSnapshot: 'account', actionType: 'delete' }], pendingCount: 1 });
      const element = () => kind === 'logs' ? <LogsPage /> : <AccountActionCandidatesPage />;
      const open = () => clickButton(kind === 'logs' ? 'logs.clear_button' : 'account_actions.delete_auth_file');
      const mutation = kind === 'logs' ? mocks.clearLogs : mocks.deleteCandidate;
      await mount(element());
      act(() => { open(); });
      const staleConfirm = mocks.confirmation.mock.lastCall![0].onConfirm;
      const original = scope === 'managerBase' ? mocks.managerBase : mocks.auth[scope];
      for (const value of ['https://other.test', original]) {
        if (scope === 'managerBase') mocks.managerBase = value;
        else mocks.auth[scope] = value;
        await act(async () => { renderer.update(element()); });
        await act(async () => { await staleConfirm(); });
        expect(mutation).not.toHaveBeenCalled();
      }
      act(() => { open(); });
      await act(async () => { await mocks.confirmation.mock.lastCall![0].onConfirm(); });
      expect(mutation).toHaveBeenCalledOnce();
    }
  );

  it('preserves a successful empty candidate snapshot through refresh and failure', async () => {
    mocks.candidates.mockResolvedValue({ items: [], pendingCount: 0 });
    await mount(<AccountActionCandidatesPage />);
    expect(text(renderer.root)).toContain('account_actions.empty_title');
    const next = deferred<unknown>();
    mocks.candidates.mockReturnValueOnce(next.promise);
    let pending!: Promise<unknown>;
    act(() => { pending = clickButton('common.refresh'); });
    expect(text(renderer.root)).toContain('account_actions.empty_title');
    expect(renderer.root.findAllByType('table')).toHaveLength(0);
    await act(async () => { next.reject(new Error('empty refresh offline')); await pending; });
    expect(text(renderer.root)).toContain('account_actions.empty_title');
    expect(text(renderer.root)).toContain('empty refresh offline');
  });

  it('keeps candidate bootstrap failure distinct from a successful empty result', async () => {
    mocks.candidates.mockRejectedValueOnce(new Error('candidate bootstrap offline'));
    await mount(<AccountActionCandidatesPage />);
    const output = text(renderer.root);
    expect(output).toContain('candidate bootstrap offline');
    expect(output).not.toContain('account_actions.empty_title');
  });

  it('keeps installed-plugin bootstrap failure distinct from a successful empty result', async () => {
    mocks.plugins.mockRejectedValueOnce(new Error('plugin bootstrap offline'));
    await mount(<PluginsPage />);
    const output = text(renderer.root);
    expect(output).toContain('plugin bootstrap offline');
    expect(output).not.toContain('plugin_management.no_plugins');
  });

  it('does not render dashboard no-data regions after a bootstrap summary failure', async () => {
    mocks.usageSummary = { loading: false, summary: null, error: 'summary bootstrap offline' };
    await mount(<DashboardPage />);
    expect(renderer.root.findAllByType(UsageMetricsCard)).toHaveLength(1);
    expect(renderer.root.findAllByType(TrafficOverviewCard)).toHaveLength(0);
    expect(renderer.root.findAllByType(HealthAlertsCard)).toHaveLength(0);
  });

  it('keeps manager CPA base unknown when manager config bootstrap fails', async () => {
    mocks.managerConfig.mockRejectedValueOnce(new Error('manager config bootstrap offline'));
    await mount(<DashboardPage />);
    const version = renderer.root.findByType(VersionCard);

    expect(version.props.cpaBase).toBe('');
    expect(text(renderer.root)).toContain('notification.refresh_failed');
  });

  it('uses channel config fallback only until a successful remote snapshot exists', async () => {
    const fallbackChannel = {
      key: 'fallback:0',
      name: 'fallback',
      baseUrl: 'https://fallback.test',
      host: 'fallback.test',
      disabled: false,
      authIndices: ['auth-fallback'],
      modelNames: [],
    };
    mocks.meta.mockResolvedValueOnce({
      authFiles: [],
      authFilesLoaded: true,
      channels: [fallbackChannel],
      channelsLoaded: false,
      error: 'channels offline',
    });

    await mount(<DashboardPage />);
    const health = () => renderer.root.findByType(HealthAlertsCard);
    expect(health().props.channelByAuthIndex.has('auth-fallback')).toBe(true);
    expect(text(renderer.root)).toContain('notification.refresh_failed');

    mocks.meta.mockResolvedValueOnce({
      authFiles: [],
      authFilesLoaded: true,
      channels: [],
      channelsLoaded: true,
      error: '',
    });
    await act(async () => { await refresh(); });
    expect(health().props.channelByAuthIndex.has('auth-fallback')).toBe(false);

    mocks.meta.mockResolvedValueOnce({
      authFiles: [],
      authFilesLoaded: true,
      channels: [fallbackChannel],
      channelsLoaded: false,
      error: 'channels offline again',
    });
    await act(async () => { await refresh(); });
    expect(health().props.channelByAuthIndex.has('auth-fallback')).toBe(false);
    expect(text(renderer.root)).toContain('notification.refresh_failed');
  });

  it('retains dashboard auxiliary snapshots across real partial meta refresh failures', async () => {
    mocks.meta.mockResolvedValue({
      authFiles: [
        {
          name: 'retained.json',
          type: 'codex',
          provider: 'codex',
          authIndex: 'auth-retained',
          account: 'retained@example.com',
        },
      ],
      authFilesLoaded: true,
      channels: [
        {
          key: 'retained:0',
          name: 'retained',
          baseUrl: 'https://channel.test',
          host: 'channel.test',
          disabled: false,
          authIndices: ['auth-retained'],
          modelNames: [],
        },
      ],
      channelsLoaded: true,
      error: '',
    });
    mocks.aliases.mockResolvedValue({
      items: [{ apiKeyHash: 'retained-hash', alias: 'Retained key' }],
    });

    await mount(<DashboardPage />);
    const version = () => renderer.root.findByType(VersionCard);
    const health = () => renderer.root.findByType(HealthAlertsCard);
    expect(version().props.cpaBase).toBe('https://cpa.last-good');
    expect(health().props.authFileMap.has('auth-retained')).toBe(true);
    expect(health().props.channelByAuthIndex.has('auth-retained')).toBe(true);
    expect(health().props.apiKeyAliasMap.has('retained-hash')).toBe(true);

    mocks.managerConfig.mockRejectedValueOnce(new Error('manager config offline'));
    mocks.meta.mockResolvedValueOnce({
      authFiles: [],
      authFilesLoaded: false,
      channels: [],
      channelsLoaded: false,
      error: 'meta offline',
    });
    mocks.aliases.mockRejectedValueOnce(new Error('aliases offline'));
    await act(async () => { await refresh(); });

    expect(version().props.cpaBase).toBe('https://cpa.last-good');
    expect(health().props.authFileMap.has('auth-retained')).toBe(true);
    expect(health().props.channelByAuthIndex.has('auth-retained')).toBe(true);
    expect(health().props.apiKeyAliasMap.has('retained-hash')).toBe(true);
    expect(text(renderer.root)).toContain('notification.refresh_failed');
  });

  it('uses a successful empty dashboard summary during background refresh', async () => {
    await mount(<DashboardPage />);
    mocks.usageSummary.loading = true;
    await act(async () => { renderer.update(<DashboardPage />); });
    expect(renderer.root.findAllByType(UsageMetricsCard).every(n => n.props.loading)).toBe(true);
    mocks.usageSummary.summary = { window: { now_ms: 1 } };
    await act(async () => { renderer.update(<DashboardPage />); });
    for (const component of [UsageMetricsCard, TrafficOverviewCard, HealthAlertsCard]) {
      const cards = renderer.root.findAllByType(component);
      expect(cards.length).toBeGreaterThan(0);
      expect(cards.every(n => n.props.loading === false)).toBe(true);
    }
  });

  it('distinguishes error-log bootstrap failure from a successfully loaded zero count', async () => {
    mocks.errorLogs.mockRejectedValueOnce(new Error('bootstrap logs offline'));
    await mount(<DashboardPage />);
    const version = () => renderer.root.findByType(VersionCard).props;
    expect(version().errorLogsLoaded).toBe(false);
    expect(version().errorLogsError).toBe('bootstrap logs offline');
    mocks.errorLogs.mockResolvedValueOnce({ files: [] });
    await act(async () => { await refresh(); });
    expect(version().errorLogsLoaded).toBe(true);
    expect(version().errorLogCount).toBe(0);
    mocks.errorLogs.mockRejectedValueOnce(new Error('refresh logs offline'));
    await act(async () => { await refresh(); });
    expect(version().errorLogsLoaded).toBe(true);
    expect(version().errorLogCount).toBe(0);
  });

  it.each(['installed-read', 'installed-delete', 'store-delete'] as const)(
    'never resumes an old reinstall after capability revoke and restore (%s)',
    async (phase) => {
      const { PluginStorePage } = await vi.importActual<typeof import('./plugins/PluginStorePage')>('./plugins/PluginStorePage');
      const installed = { ...storeData, plugins: [{ ...storeData.plugins[0], installed: true }] };
      mocks.plugins.mockResolvedValue(pluginData);
      mocks.pluginStore.mockResolvedValue(installed);
      mocks.deletePlugin.mockResolvedValue({ restartRequired: false });
      const element = () => phase === 'store-delete' ? <PluginStorePage /> : <PluginsPage />;
      const open = () => {
        if (phase === 'store-delete') clickButton('plugin_store.reinstall_plugin');
        else renderer.root.findByType(DropdownMenu).props.items.find((item: {key: string}) => item.key === 'reinstall').onClick();
      };
      await mount(element());
      act(open);
      const staleConfirm = mocks.confirmation.mock.lastCall![0].onConfirm;
      const next = deferred<unknown>();
      (phase === 'installed-read' ? mocks.pluginStore : mocks.deletePlugin).mockReturnValueOnce(next.promise);
      let pending!: Promise<unknown>;
      await act(async () => { pending = staleConfirm(); });
      for (const supports of [false, true]) {
        mocks.auth.supportsPlugin = supports;
        await act(async () => { renderer.update(element()); });
      }
      expect(text(renderer.root)).toContain(phase === 'store-delete' ? 'Retained Store Plugin' : 'Retained Plugin');
      await act(async () => {
        next.resolve(phase === 'installed-read' ? installed : { restartRequired: false });
        await pending;
        await staleConfirm();
      });
      expect(mocks.installPlugin).not.toHaveBeenCalled();
      expect(mocks.deletePlugin).toHaveBeenCalledTimes(phase === 'installed-read' ? 0 : 1);
      act(open);
      await act(async () => { await mocks.confirmation.mock.lastCall![0].onConfirm(); });
      expect(mocks.installPlugin).toHaveBeenCalledOnce();
    }
  );

  it.each([false, true])(
    'does not roll back a successful store mutation when an earlier read arrives (update=%s)',
    async (isUpdate) => {
      const { PluginStorePage } = await vi.importActual<typeof import('./plugins/PluginStorePage')>(
        './plugins/PluginStorePage'
      );
      const before = {
        ...storeData,
        plugins: [
          {
            ...storeData.plugins[0],
            installed: isUpdate,
            installedVersion: isUpdate ? '1.0' : '',
            updateAvailable: isUpdate,
          },
        ],
      };
      mocks.pluginStore.mockResolvedValueOnce(before);
      await mount(<PluginStorePage />);
      const old = deferred<PluginStoreResponse>();
      mocks.pluginStore.mockReturnValueOnce(old.promise);
      let pending!: Promise<unknown>;
      act(() => {
        pending = refresh();
      });
      act(() => {
        clickButton(isUpdate ? 'plugin_store.update' : 'plugin_store.install');
      });
      const after = {
        ...storeData,
        plugins: [
          {
            ...storeData.plugins[0],
            installed: true,
            installedVersion: '2.0',
            configured: true,
            registered: true,
            enabled: true,
            effectiveEnabled: true,
          },
        ],
      };
      mocks.pluginStore.mockResolvedValueOnce(after);
      await act(async () => {
        await installOptions().props.onConfirm();
      });
      expect(mocks.installPlugin).toHaveBeenCalledOnce();
      expect(text(renderer.root)).toContain('v2.0');
      const refreshReleased = !(
        renderer.root.findAllByType('button').find((n) => text(n) === 'plugin_store.refresh')!.props
          .disabled
      );
      await act(async () => {
        old.resolve(before);
        await pending;
      });
      expect(text(renderer.root)).toContain('v2.0');
      expect(
        renderer.root
          .findAllByType('button')
          .some((n) => text(n) === 'plugin_store.update' || text(n) === 'plugin_store.install')
      ).toBe(false);
      expect(refreshReleased).toBe(true);
    }
  );

  it.each(
    (['installed', 'store'] as const).flatMap((kind) =>
      ([401, 403, 404, 'capability'] as const).map((reason) => ({ kind, reason }))
    )
  )(
    'removes $kind data and blocks captured operations after access is revoked ($reason)',
    async ({ kind, reason }) => {
      const { PluginStorePage } = await vi.importActual<typeof import('./plugins/PluginStorePage')>(
        './plugins/PluginStorePage'
      );
      mocks.auth.supportsPlugin = true;
      mocks.plugins.mockResolvedValue(pluginData);
      mocks.pluginStore.mockResolvedValue(storeData);
      const element = () => (kind === 'installed' ? <PluginsPage /> : <PluginStorePage />);
      await mount(element());
      let staleOperation: () => Promise<unknown>;
      if (kind === 'installed') {
        const toggle = renderer.root.findByType(ToggleSwitch).props.onChange;
        staleOperation = () => toggle(false);
      } else {
        act(() => {
          clickButton('plugin_store.install');
        });
        staleOperation = installOptions().props.onConfirm;
      }
      if (reason === 'capability') {
        mocks.auth.supportsPlugin = false;
        await act(async () => {
          renderer.update(element());
        });
      } else {
        const api = kind === 'installed' ? mocks.plugins : mocks.pluginStore;
        api.mockRejectedValueOnce(Object.assign(new Error('access revoked'), { status: reason }));
        await act(async () => {
          await refresh();
        });
      }
      expect(text(renderer.root)).not.toContain(
        kind === 'installed' ? 'Retained Plugin' : 'Retained Store Plugin'
      );
      expect(text(renderer.root)).toContain(
        reason === 'capability' || reason === 404 ? 'unsupported_backend' : 'access revoked'
      );
      await act(async () => {
        await staleOperation();
      });
      expect(mocks.updatePlugin).not.toHaveBeenCalled();
      expect(mocks.installPlugin).not.toHaveBeenCalled();
    }
  );

  it.each(['installed', 'store'] as const)(
    'blocks a captured %s delete confirmation and ignores a read after capability revocation',
    async (kind) => {
      const { PluginStorePage } = await vi.importActual<typeof import('./plugins/PluginStorePage')>(
        './plugins/PluginStorePage'
      );
      mocks.plugins.mockResolvedValue(pluginData);
      mocks.pluginStore.mockResolvedValue({
        ...storeData,
        plugins: [{ ...storeData.plugins[0], installed: true }],
      });
      const element = () => (kind === 'installed' ? <PluginsPage /> : <PluginStorePage />);
      await mount(element());
      act(() => {
        if (kind === 'installed') {
          renderer.root
            .findByType(DropdownMenu)
            .props.items.find((item: { key: string }) => item.key === 'delete')
            .onClick();
        } else {
          renderer.root
            .findAllByType('button')
            .find((n) => n.props['aria-label'] === 'plugin_store.delete_plugin')!
            .props.onClick();
        }
      });
      const confirm = mocks.confirmation.mock.lastCall![0].onConfirm;
      const old = deferred<unknown>();
      (kind === 'installed' ? mocks.plugins : mocks.pluginStore).mockReturnValueOnce(old.promise);
      let pending!: Promise<unknown>;
      act(() => {
        pending = refresh();
      });
      mocks.auth.supportsPlugin = false;
      await act(async () => {
        renderer.update(element());
      });
      await act(async () => {
        await confirm();
        old.resolve(kind === 'installed' ? pluginData : storeData);
        await pending;
      });
      expect(mocks.deletePlugin).not.toHaveBeenCalled();
      expect(text(renderer.root)).not.toContain(
        kind === 'installed' ? 'Retained Plugin' : 'Retained Store Plugin'
      );
    }
  );

  it.each(['installed', 'store'] as const)(
    'clears %s data when mutation reconciliation rejects access',
    async (kind) => {
      const { PluginStorePage } = await vi.importActual<typeof import('./plugins/PluginStorePage')>(
        './plugins/PluginStorePage'
      );
      mocks.plugins.mockResolvedValueOnce(pluginData);
      mocks.pluginStore.mockResolvedValueOnce(storeData);
      await mount(kind === 'installed' ? <PluginsPage /> : <PluginStorePage />);
      (kind === 'installed' ? mocks.plugins : mocks.pluginStore).mockRejectedValueOnce(
        Object.assign(new Error('access revoked during reconciliation'), { status: 403 })
      );
      await act(async () => {
        if (kind === 'installed') {
          await renderer.root.findByType(ToggleSwitch).props.onChange(false);
        } else {
          clickButton('plugin_store.install');
        }
      });
      if (kind === 'store') {
        await act(async () => { await installOptions().props.onConfirm(); });
      }
      expect(text(renderer.root)).not.toContain(
        kind === 'installed' ? 'Retained Plugin' : 'Retained Store Plugin'
      );
      expect(text(renderer.root)).toContain('access revoked during reconciliation');
    }
  );

  it('does not restore store data when reconciliation finishes after capability revocation', async () => {
    const { PluginStorePage } = await vi.importActual<typeof import('./plugins/PluginStorePage')>(
      './plugins/PluginStorePage'
    );
    mocks.pluginStore.mockResolvedValueOnce(storeData);
    await mount(<PluginStorePage />);
    act(() => {
      clickButton('plugin_store.install');
    });
    const poll = deferred<PluginStoreResponse>();
    mocks.pluginStore.mockReturnValueOnce(poll.promise);
    let mutation!: Promise<unknown>;
    await act(async () => {
      mutation = installOptions().props.onConfirm();
    });
    mocks.auth.supportsPlugin = false;
    await act(async () => {
      renderer.update(<PluginStorePage active={false} />);
    });
    await act(async () => {
      poll.resolve({ ...storeData, plugins: [{ ...storeData.plugins[0], installed: true }] });
      await mutation;
    });
    expect(text(renderer.root)).not.toContain('Retained Store Plugin');
  });

  it('attributes an error-log-only failure to logs while keeping collector health and last-known count', async () => {
    await mount(<DashboardPage />);
    mocks.errorLogs.mockRejectedValueOnce(new Error('logs offline'));
    await act(async () => {
      await refresh();
    });
    const version = () => renderer.root.findByType(VersionCard);
    expect(version().props.collectorError).toBe('');
    expect(version().props.errorLogsError).toBe('logs offline');
    expect(version().props.errorLogCount).toBe(1);
    expect(version().props.collectorStatus.marker).toBe('last-good');
    await act(async () => {
      await refresh();
    });
    expect(version().props.errorLogsError).toBe('');
  });

  it('keeps cached model count visible and exposes a failed background refresh', async () => {
    await mount(<DashboardPage />);
    const modelLink = () =>
      renderer.root.findAllByType('a').find((n) => text(n).includes('dashboard.available_models'))!;
    expect(text(modelLink())).toContain('1');
    mocks.fetchModels.mockImplementationOnce(async () => {
      mocks.models.error = 'models offline';
    });
    await act(async () => {
      await refresh();
      renderer.update(<DashboardPage />);
    });
    expect(text(modelLink())).toContain('1');
    expect(
      renderer.root
        .findAllByProps({ role: 'alert' })
        .some((n) => text(n).includes('models offline'))
    ).toBe(true);
    mocks.models.error = null;
    await act(async () => {
      renderer.update(<DashboardPage />);
    });
    expect(text(renderer.root)).not.toContain('models offline');
  });

  it('keeps store results on failure and only accepts the latest overlapping refresh', async () => {
    const { PluginStorePage } = await vi.importActual<typeof import('./plugins/PluginStorePage')>('./plugins/PluginStorePage');
    const data = { plugins: [], sources: [], sourceErrors: [], pluginsEnabled: true, pluginsDir: 'plugins' };
    mocks.pluginStore.mockResolvedValueOnce(data);
    await mount(<PluginStorePage />);
    const old = deferred<unknown>(), next = deferred<unknown>();
    mocks.pluginStore.mockReturnValueOnce(old.promise).mockReturnValueOnce(next.promise);
    let first!: Promise<unknown>, second!: Promise<unknown>;
    act(() => { first = refresh(); second = refresh(); });
    await act(async () => { next.reject(new Error('current-offline')); await second; });
    expect(text(renderer.root)).toContain('current-offline');
    await act(async () => { old.reject(new Error('stale-offline')); await first; });
    expect(text(renderer.root)).not.toContain('stale-offline');
    expect(text(renderer.root)).toContain('current-offline');
    expect(text(renderer.root)).toContain('plugin_store.global_enabled');
  });

  it('releases refresh busy state when plugin mutation reconciliation supersedes a read', async () => {
    mocks.plugins.mockResolvedValueOnce(pluginData);
    await mount(<PluginsPage />);
    const old = deferred<PluginListResponse>();
    mocks.plugins.mockReturnValueOnce(old.promise);
    let pending!: Promise<unknown>;
    act(() => { pending = refresh(); });
    mocks.plugins.mockResolvedValueOnce({ ...pluginData, plugins: [{ ...pluginData.plugins[0], enabled: false }] });
    await act(async () => { await renderer.root.findByType(ToggleSwitch).props.onChange(false); });
    expect(renderer.root.findByType(ToggleSwitch).props.checked).toBe(false);
    expect(renderer.root.findAllByType('button').find(n => text(n) === 'plugin_management.refresh')!.props.disabled).toBe(false);
    await act(async () => { old.resolve(pluginData); await pending; });
    expect(renderer.root.findByType(ToggleSwitch).props.checked).toBe(false);
  });

  it('does not resurrect cleared logs when an earlier refresh finishes', async () => {
    mocks.logs.mockResolvedValueOnce({ lines: ['before-clear'] });
    await mount(<LogsPage />);
    const next = deferred<unknown>();
    mocks.logs.mockReturnValueOnce(next.promise);
    let pending!: Promise<unknown>;
    act(() => { pending = refresh(); });
    await act(async () => {
      renderer.root.findAllByType('button').find(n => text(n) === 'logs.clear_button')!.props.onClick();
      await mocks.confirmation.mock.calls[0][0].onConfirm();
    });
    expect(text(renderer.root)).not.toContain('before-clear');
    await act(async () => { next.resolve({ lines: ['stale-cleared-line'] }); await pending; });
    expect(text(renderer.root)).not.toContain('stale-cleared-line');
    expect(mocks.clearLogs).toHaveBeenCalled();
  });

  it('reconciles a completed mutation using the current candidate filter', async () => {
    const candidate = { id: 1, accountSnapshot: 'pending-account', status: 'pending', actionType: 'disable' };
    mocks.candidates.mockResolvedValue({ items: [candidate], pendingCount: 1 });
    await mount(<AccountActionCandidatesPage />);
    const mutation = deferred<unknown>();
    mocks.ignoreCandidate.mockReturnValueOnce(mutation.promise);
    let pending!: Promise<unknown>;
    act(() => { pending = renderer.root.findAllByType('button').find(n => text(n) === 'account_actions.ignore')!.props.onClick(); });
    await act(async () => {
      renderer.root.findAllByType('button').find(n => text(n) === 'account_actions.filter_all')!.props.onClick();
    });
    await act(async () => { mutation.resolve({}); await pending; });
    expect(mocks.candidates.mock.lastCall?.[2]).toBe('');
    expect(text(renderer.root)).toContain('pending-account');
  });

  it('keeps policy controls visible and busy during refresh and isolates a new manager', async () => {
    const capability = { enabled: true, configured: true, source: 'database', locked: false, envKey: 'ENV', configFileKey: 'field' };
    mocks.policy.mockResolvedValueOnce({ codexQuotaCooldown: capability, authIssueQueue: capability, authIssueAutoDisable: capability });
    await mount(<AccountProcessingPolicySection />);
    expect(renderer.root.findAllByType(ToggleSwitch).map(n => n.props.checked)).toEqual([true, true, true]);
    const next = deferred<unknown>(); mocks.policy.mockReturnValueOnce(next.promise);
    const refreshButton = renderer.root.findAllByType('button').find(n => text(n) === 'accountPolicy.refresh')!;
    let pending!: Promise<unknown>; act(() => { pending = refreshButton.props.onClick(); });
    expect(renderer.root.findAllByType(ToggleSwitch).every(n => n.props.disabled)).toBe(true);
    await act(async () => { next.reject(new Error('offline')); await pending; });
    expect(renderer.root.findAllByType(ToggleSwitch).map(n => n.props.checked)).toEqual([true, true, true]);
    expect(text(renderer.root)).toContain('offline');
    mocks.policy.mockReturnValue(new Promise(() => {})); mocks.managerBase = 'https://new-manager.test';
    await act(async () => { renderer.update(<AccountProcessingPolicySection />); });
    expect(renderer.root.findAllByType(ToggleSwitch)).toHaveLength(0);
  });

  it('does not publish a policy mutation result after switching managers', async () => {
    mocks.policy.mockReset();
    mocks.updatePolicy.mockReset();
    mocks.noop.mockClear();
    const capability = { enabled: true, configured: true, source: 'database', locked: false, envKey: 'ENV', configFileKey: 'field' };
    const policy = { codexQuotaCooldown: capability, authIssueQueue: capability, authIssueAutoDisable: capability };
    mocks.policy.mockResolvedValueOnce(policy);
    await mount(<AccountProcessingPolicySection />);

    const mutation = deferred<unknown>();
    mocks.updatePolicy.mockReturnValueOnce(mutation.promise);
    act(() => {
      renderer.root.findAllByType(ToggleSwitch)[0].props.onChange(false);
    });

    mocks.policy.mockReturnValue(new Promise(() => {}));
    mocks.managerBase = 'https://new-manager.test';
    await act(async () => {
      renderer.update(<AccountProcessingPolicySection />);
    });

    await act(async () => {
      mutation.resolve(policy);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(
      mocks.noop.mock.calls.some(([message]) => message === 'accountPolicy.save_success')
    ).toBe(false);
  });

  it.each(['logs', 'system', 'dashboard', 'candidates'] as const)('rejects a late %s response after a connection switch', async (kind) => {
    const old = deferred<unknown>(), next = deferred<unknown>();
    mocks.logs.mockResolvedValue({ lines: [] }); mocks.candidates.mockResolvedValue({ items: [], pendingCount: 0 });
    const api = kind === 'logs' ? mocks.logs : kind === 'candidates' ? mocks.candidates : mocks.status;
    api.mockReturnValueOnce(old.promise).mockReturnValueOnce(next.promise);
    const element = () => kind === 'logs' ? <LogsPage /> : kind === 'system' ? <SystemPage /> : kind === 'dashboard' ? <DashboardPage /> : <AccountActionCandidatesPage />;
    await mount(element());
    mocks.auth.managementKey = 'b';
    await act(async () => { renderer.update(element()); });
    await act(async () => { old.resolve(kind === 'logs' ? { lines: ['old-scope-secret'] } : kind === 'candidates' ? { items: [{ id: 1, accountSnapshot: 'old-scope-secret' }], pendingCount: 1 } : { marker: 'old-scope-secret' }); });
    expect(text(renderer.root)).not.toContain('old-scope-secret');
    await act(async () => { next.resolve(kind === 'logs' ? { lines: ['new-scope-result'] } : kind === 'candidates' ? { items: [{ id: 2, accountSnapshot: 'new-scope-result' }], pendingCount: 1 } : { marker: 'new-scope-result' }); });
    expect(text(renderer.root)).toContain('new-scope-result');
  });

  it.each(['installed', 'resource'])(
    'keeps the %s plugin mounted through pending/failure and clears it on connection change',
    async (kind) => {
      const element = kind === 'installed' ? <PluginsPage /> : <PluginResourcePage />;
      mocks.plugins.mockResolvedValueOnce(pluginData);
      await mount(element);
      const getNode = () =>
        kind === 'resource'
          ? renderer.root.findByType('iframe')
          : renderer.root.findByProps({ role: 'table' });
      const original = getNode();
      const next = deferred<PluginListResponse>();
      mocks.plugins.mockReturnValueOnce(next.promise);
      let pending!: Promise<unknown>;
      act(() => {
        pending = refresh();
      });
      expect(getNode()).toBe(original);
      await act(async () => {
        next.reject(new Error('offline'));
        await pending;
      });
      expect(getNode()).toBe(original);
      expect(text(renderer.root)).toContain('offline');
      mocks.plugins.mockReturnValueOnce(new Promise(() => {}));
      mocks.auth.managementKey = 'b';
      await act(async () => {
        renderer.update(kind === 'installed' ? <PluginsPage /> : <PluginResourcePage />);
      });
      expect(renderer.root.findAllByType(kind === 'resource' ? 'iframe' : 'table')).toHaveLength(0);
      expect(text(renderer.root)).not.toContain('Retained Plugin');
    }
  );

  it.each([
    ['logs', 'bootstrap logs offline', 'logs.empty_title'],
    ['errors', 'bootstrap errors offline', 'logs.error_logs_empty'],
  ] as const)(
    'does not render a known-empty state when %s bootstrap fails',
    async (tab, message, emptyKey) => {
      if (tab === 'errors') {
        mocks.params = new URLSearchParams('tab=errors');
        mocks.errorLogs.mockRejectedValueOnce(new Error(message));
      } else {
        mocks.logs.mockRejectedValueOnce(new Error(message));
      }

      await mount(<LogsPage />);
      expect(text(renderer.root)).toContain(message);
      expect(text(renderer.root)).not.toContain(emptyKey);
    }
  );

  it('keeps logs visible and reports auto-refresh failures', async () => {
    mocks.logs.mockResolvedValueOnce({ lines: ['poll-retained-line'], latestAfter: 1 });
    await mount(<LogsPage />);
    expect(text(renderer.root)).toContain('poll-retained-line');

    let poll: (() => void) | null = null;
    (window as unknown as { setInterval: (handler: () => void) => number }).setInterval = (
      handler
    ) => {
      poll = handler;
      return 1;
    };
    const autoRefreshToggle = renderer.root
      .findAllByType(ToggleSwitch)
      .find((node) => text(node).includes('logs.auto_refresh'));
    expect(autoRefreshToggle).toBeTruthy();
    act(() => { autoRefreshToggle!.props.onChange(true); });
    expect(poll).not.toBeNull();

    const next = deferred<unknown>();
    mocks.logs.mockReturnValueOnce(next.promise);
    act(() => { poll!(); });
    await act(async () => {
      next.reject(new Error('poll offline'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(text(renderer.root)).toContain('poll-retained-line');
    expect(text(renderer.root)).toContain('poll offline');
  });

  it.each(['logs', 'errors'])(
    'keeps existing %s while refreshing, reports failure, and replaces successful data',
    async (tab) => {
      if (tab === 'errors') mocks.params = new URLSearchParams('tab=errors');
      mocks.logs.mockResolvedValue({ lines: ['old-visible-line'], latestAfter: 1 });
      await mount(<LogsPage />);
      const expected = tab === 'logs' ? 'old-visible-line' : 'error-retained.log';
      expect(text(renderer.root)).toContain(expected);
      mocks.requestScroll.mockClear();
      const next = deferred<unknown>();
      const api = tab === 'logs' ? mocks.logs : mocks.errorLogs;
      api.mockReturnValueOnce(next.promise);
      let pending!: Promise<unknown>;
      act(() => {
        pending = refresh();
      });
      expect(text(renderer.root)).toContain(expected);
      if (tab === 'logs') expect(mocks.requestScroll).not.toHaveBeenCalled();
      await act(async () => {
        next.reject(new Error('offline'));
        await pending;
      });
      expect(text(renderer.root)).toContain(expected);
      expect(text(renderer.root)).toContain('offline');
      api.mockResolvedValueOnce(
        tab === 'logs' ? { lines: ['new-visible-line'] } : { files: [{ name: 'new-error.log' }] }
      );
      await act(async () => {
        await refresh();
      });
      expect(text(renderer.root)).toContain(tab === 'logs' ? 'new-visible-line' : 'new-error.log');
    }
  );

  it('retains system models and database status through refresh failure', async () => {
    await mount(<SystemPage />);
    expect(text(renderer.root)).toContain('gpt-retained');
    const originalStatus = renderer.root.findByType(DatabaseStatusCard).props.status;
    const next = deferred<unknown>();
    mocks.status.mockReturnValueOnce(next.promise);
    mocks.models.loading = true;
    await act(async () => {
      renderer.update(<SystemPage />);
    });
    let pending!: Promise<unknown>;
    act(() => {
      pending = refresh();
    });
    expect(text(renderer.root)).toContain('gpt-retained');
    expect(renderer.root.findByType(DatabaseStatusCard).props.status).toBe(originalStatus);
    await act(async () => {
      next.reject(new Error('offline'));
      await pending;
    });
    expect(renderer.root.findByType(DatabaseStatusCard).props.status).toBe(originalStatus);
    expect(renderer.root.findByType(DatabaseStatusCard).props.error).toBe('offline');
  });

  it('retains dashboard counts and operational snapshots when only some endpoints fail', async () => {
    await mount(<DashboardPage />);
    const version = () => renderer.root.findByType(VersionCard);
    const collector = version().props.collectorStatus;
    expect(version().props.errorLogCount).toBe(1);
    const next = deferred<unknown>();
    mocks.status.mockReturnValueOnce(next.promise);
    mocks.providers.mockRejectedValue(new Error('providers offline'));
    mocks.errorLogs.mockRejectedValueOnce(new Error('logs offline'));
    let pending!: Promise<unknown>;
    act(() => {
      pending = refresh();
    });
    expect(version().props.collectorStatus).toBe(collector);
    expect(version().props.errorLogCount).toBe(1);
    await act(async () => {
      next.reject(new Error('collector offline'));
      await pending;
    });
    expect(version().props.collectorStatus).toBe(collector);
    expect(version().props.errorLogCount).toBe(1);
    expect(text(renderer.root)).toContain('notification.refresh_failed');
    const providerLink = renderer.root
      .findAllByType('a')
      .find((n) => text(n).includes('nav.ai_providers'));
    expect(providerLink).toBeDefined();
    expect(text(providerLink!)).toContain('5');
  });

  it('retains candidates after failure and clears them on filter or manager changes', async () => {
    mocks.candidates.mockResolvedValueOnce({
      items: [
        {
          id: 1,
          accountSnapshot: 'retained-account',
          authFileName: 'retained.json',
          provider: 'codex',
          status: 'pending',
          actionType: 'disable',
          reason: 'test',
        },
      ],
      pendingCount: 1,
    });
    await mount(<AccountActionCandidatesPage />);
    const next = deferred<unknown>();
    mocks.candidates.mockReturnValueOnce(next.promise);
    const button = renderer.root.findAllByType('button').find((n) => text(n) === 'common.refresh')!;
    let pending!: Promise<unknown>;
    act(() => {
      pending = button.props.onClick();
    });
    expect(text(renderer.root)).toContain('retained-account');
    await act(async () => {
      next.reject(new Error('offline'));
      await pending;
    });
    expect(text(renderer.root)).toContain('retained-account');
    expect(text(renderer.root)).toContain('offline');
    mocks.candidates.mockReturnValue(new Promise(() => {}));
    act(() => {
      renderer.root
        .findAllByType('button')
        .find((n) => text(n) === 'account_actions.filter_all')!
        .props.onClick();
    });
    expect(text(renderer.root)).not.toContain('retained-account');
  });
});
