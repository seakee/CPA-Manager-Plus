import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { PublicApiError, type PublicApiClient } from '@/fullweb/publicApi';
import i18n from '@/i18n';
import { RuntimeStatusPage } from './RuntimeStatusPage';
import { apiKeysApi } from '@/services/api/apiKeys';
import { modelsApi } from '@/services/api/models';
import { usageServiceApi } from '@/services/api/usageService';
import { memoryStorage } from './cpaUpdateTestFixtures';
import { useAuthStore, useConfigStore, useModelsStore } from '@/stores';
const availabilityMock = vi.hoisted(() => ({
  managerServiceAvailable: false,
  managerServiceBase: '',
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => availabilityMock,
}));

const DIGEST = 'sha256:' + 'd'.repeat(64);

function embeddedStatus() {
  return {
    mode: 'embedded' as const,
    status: 'healthy',
    observedAt: '2026-11-09T00:00:00Z',
    manager: { state: 'ready' },
    gateway: {
      state: 'online',
      ready: true,
      artifactId: DIGEST,
      capabilities: ['restart', 'stop'],
    },
  };
}

describe('RuntimeStatusPage artifact origin display', () => {
  let renderer: ReactTestRenderer | undefined;

  beforeEach(async () => {
    await i18n.changeLanguage('zh-CN');
    availabilityMock.managerServiceAvailable = false;
    availabilityMock.managerServiceBase = '';
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    vi.useFakeTimers();
    vi.stubGlobal('window', {
      setInterval,
      clearInterval,
      location: { hash: '', search: '' },
    });
    vi.stubGlobal('document', {
      visibilityState: 'visible',
    });
  });

  afterEach(async () => {
    if (renderer) {
      await act(async () => renderer!.unmount());
      renderer = undefined;
    }
    // Unmounting cancels the poll, but a refresh that is already awaiting its
    // reads still settles its state later; flush it before the stubbed globals
    // (window, document) go away, so it cannot run against a bare test context.
    await act(async () => {});
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  async function mount(client: PublicApiClient) {
    if (renderer) {
      await act(async () => renderer!.unmount());
      renderer = undefined;
    }
    await act(async () => {
      renderer = create(
        <MemoryRouter>
          <RuntimeStatusPage client={client} credential="admin-secret" />
        </MemoryRouter>
      );
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it.each([
    [
      'bundled',
      {
        schemaVersion: 1,
        pinned: { version: '8.0.55', artifactId: DIGEST },
        active: { artifactId: DIGEST, origin: 'bundled' },
        required: false,
        operation: null,
        cdnPresets: [],
      },
      'runtime_status.engine_source_bundled',
    ],
    [
      'acquired',
      {
        schemaVersion: 1,
        pinned: { version: '8.0.55', artifactId: DIGEST },
        active: { artifactId: DIGEST, origin: 'acquired' },
        required: false,
        operation: null,
        cdnPresets: [],
      },
      'runtime_status.engine_source_acquired',
    ],
    [
      'required',
      {
        schemaVersion: 1,
        pinned: { version: '8.0.55', artifactId: DIGEST },
        active: null,
        required: true,
        operation: null,
        cdnPresets: [],
      },
      'runtime_status.engine_source_required',
    ],
  ])('renders engine version source for %s artifact state', async (_label, artifactPayload, key) => {
    const client: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return embeddedStatus();
        if (resource === 'runtime/artifact') return artifactPayload;
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    await mount(client);

    expect(client.get).toHaveBeenCalledWith('runtime/status', {
      kind: 'admin',
      value: 'admin-secret',
    });
    expect(client.get).toHaveBeenCalledWith('runtime/artifact', {
      kind: 'admin',
      value: 'admin-secret',
    });
    const valueNode = renderer!.root.findByProps({
      'data-testid': 'runtime-status-engine-source-value',
    });
    // The value is named by the console catalog, so the test asserts the state is
    // translated rather than pinning one wording of one language.
    expect(valueNode.props.children).toBe(i18n.t(key, { lng: 'zh-CN' }));
  });

  it('shows nothing extra on unsupported_platform and does not break the page when runtime/artifact fails', async () => {
    const unsupportedClient: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return embeddedStatus();
        if (resource === 'runtime/artifact') {
          throw new PublicApiError('http', 409, undefined, 'unsupported_platform');
        }
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    await mount(unsupportedClient);

    expect(
      renderer!.root.findByProps({ 'data-testid': 'runtime-status-page' })
    ).toBeTruthy();
    expect(
      renderer!.root.findAllByProps({ 'data-testid': 'runtime-status-engine-source' })
    ).toHaveLength(0);

    const failingClient: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return embeddedStatus();
        throw new PublicApiError('network');
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    await mount(failingClient);

    expect(
      renderer!.root.findByProps({ 'data-testid': 'runtime-status-page' })
    ).toBeTruthy();
    expect(
      renderer!.root.findAllByProps({ 'data-testid': 'runtime-status-engine-source' })
    ).toHaveLength(0);
    expect(JSON.stringify(renderer!.toJSON())).toContain('healthy');
  });
  it('uses prop credential in refreshUsageStatus when auth.managementKey is empty', async () => {
    availabilityMock.managerServiceAvailable = true;
    availabilityMock.managerServiceBase = 'https://manager.test';
    const getStatusSpy = vi
      .spyOn(usageServiceApi, 'getStatus')
      .mockResolvedValue({ service: 'ok', events: 12, deadLetters: 0 });
    const client: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return embeddedStatus();
        if (resource === 'runtime/artifact') throw new PublicApiError('network');
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    await mount(client);

    expect(getStatusSpy).toHaveBeenCalledWith('https://manager.test', 'admin-secret');
    getStatusSpy.mockRestore();
  });
  it('copies diagnostic snapshot through copyToClipboard fallback when navigator.clipboard is unavailable', async () => {
    const execCommand = vi.fn(() => true);
    const textarea = {
      value: '',
      style: {} as Record<string, string>,
      setAttribute: vi.fn(),
      focus: vi.fn(),
      select: vi.fn(),
      setSelectionRange: vi.fn(),
      remove: vi.fn(),
    };
    vi.stubGlobal('navigator', {});
    vi.stubGlobal('document', {
      createElement: vi.fn(() => textarea),
      body: { appendChild: vi.fn(), removeChild: vi.fn() },
      execCommand,
    });
    const client: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return embeddedStatus();
        if (resource === 'runtime/artifact') throw new PublicApiError('network');
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    await mount(client);
    const copyButton = renderer!.root.findByProps({
      'data-testid': 'runtime-copy-snapshot-btn',
    });
    await act(async () => {
      await copyButton.props.onClick({ preventDefault: vi.fn(), stopPropagation: vi.fn() });
    });

    expect(execCommand).toHaveBeenCalledWith('copy');
    expect(textarea.value).toContain('healthy');

    const summaries = renderer!.root.findAllByType('summary');
    for (const summary of summaries) {
      expect(
        summary.findAllByProps({ 'data-testid': 'runtime-copy-snapshot-btn' })
      ).toHaveLength(0);
    }
  });

  it('fetches models in manager_embedded mode even when apiKeys list is empty', async () => {
    const prevAuth = {
      connectionStatus: useAuthStore.getState().connectionStatus,
      apiBase: useAuthStore.getState().apiBase,
      sessionMode: useAuthStore.getState().sessionMode,
      managementKey: useAuthStore.getState().managementKey,
    };
    const prevConfig = useConfigStore.getState().config;

    vi.stubGlobal('localStorage', memoryStorage());
    useAuthStore.setState({
      connectionStatus: 'connected',
      apiBase: '/console/api/v2/cpamp',
      sessionMode: 'manager_embedded',
      managementKey: 'admin-secret',
    });
    useConfigStore.setState({ config: { apiKeys: [] } as never });
    useModelsStore.getState().clearCache();
    useModelsStore.setState({ models: [], loading: false, error: null, cache: null });

    const apiKeysSpy = vi.spyOn(apiKeysApi, 'list').mockResolvedValue([]);
    const fetchModelsSpy = vi
      .spyOn(modelsApi, 'fetchModels')
      .mockResolvedValue([{ name: 'gpt-4o' }, { name: 'claude-3-7-sonnet' }]);

    const client: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return embeddedStatus();
        if (resource === 'runtime/artifact') throw new PublicApiError('network');
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    try {
      await mount(client);
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(apiKeysSpy).toHaveBeenCalledTimes(1);
      expect(fetchModelsSpy).toHaveBeenCalledWith(
        '/console/api/v2/cpamp',
        undefined,
        { Authorization: 'Bearer admin-secret' }
      );
      expect(useModelsStore.getState().models).toHaveLength(2);
      expect(renderer!.root.findByProps({ 'data-testid': 'runtime-models-card' })).toBeTruthy();
      expect(JSON.stringify(renderer!.toJSON())).toContain('gpt-4o');
    } finally {
      if (renderer) {
        await act(async () => renderer!.unmount());
        renderer = undefined;
      }
      apiKeysSpy.mockRestore();
      fetchModelsSpy.mockRestore();
      useModelsStore.getState().clearCache();
      useModelsStore.setState({ models: [], loading: false, error: null, cache: null });
      useConfigStore.setState({ config: prevConfig });
      useAuthStore.setState(prevAuth);
    }
  });

  it('skips fetching models in non-embedded mode when apiKeys list is empty and no modelManagerCredential exists', async () => {
    const prevAuth = {
      connectionStatus: useAuthStore.getState().connectionStatus,
      apiBase: useAuthStore.getState().apiBase,
      sessionMode: useAuthStore.getState().sessionMode,
      managementKey: useAuthStore.getState().managementKey,
    };
    const prevConfig = useConfigStore.getState().config;

    vi.stubGlobal('localStorage', memoryStorage());
    useAuthStore.setState({
      connectionStatus: 'connected',
      apiBase: 'https://cpa.example/v0/management',
      sessionMode: 'external_panel',
      managementKey: 'direct-key',
    });
    useConfigStore.setState({ config: { apiKeys: [] } as never });
    useModelsStore.getState().clearCache();
    useModelsStore.setState({ models: [], loading: false, error: null, cache: null });

    const apiKeysSpy = vi.spyOn(apiKeysApi, 'list').mockResolvedValue([]);
    const fetchModelsSpy = vi.spyOn(modelsApi, 'fetchModels').mockResolvedValue([]);

    const client: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return embeddedStatus();
        if (resource === 'runtime/artifact') throw new PublicApiError('network');
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    try {
      await mount(client);
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(apiKeysSpy).toHaveBeenCalledTimes(1);
      expect(fetchModelsSpy).not.toHaveBeenCalled();
    } finally {
      if (renderer) {
        await act(async () => renderer!.unmount());
        renderer = undefined;
      }
      apiKeysSpy.mockRestore();
      fetchModelsSpy.mockRestore();
      useModelsStore.getState().clearCache();
      useModelsStore.setState({ models: [], loading: false, error: null, cache: null });
      useConfigStore.setState({ config: prevConfig });
      useAuthStore.setState(prevAuth);
    }
  });

  it('tolerates apiKeysApi.list() failure in both embedded and external modes', async () => {
    const prevAuth = {
      connectionStatus: useAuthStore.getState().connectionStatus,
      apiBase: useAuthStore.getState().apiBase,
      sessionMode: useAuthStore.getState().sessionMode,
      managementKey: useAuthStore.getState().managementKey,
    };
    const prevConfig = useConfigStore.getState().config;

    vi.stubGlobal('localStorage', memoryStorage());
    useAuthStore.setState({
      connectionStatus: 'connected',
      apiBase: '/console/api/v2/cpamp',
      sessionMode: 'manager_embedded',
      managementKey: 'admin-secret',
    });
    useConfigStore.setState({ config: { apiKeys: [] } as never });
    useModelsStore.getState().clearCache();
    useModelsStore.setState({ models: [], loading: false, error: null, cache: null });

    const apiKeysSpy = vi.spyOn(apiKeysApi, 'list').mockRejectedValue(new Error('list failed'));
    const fetchModelsSpy = vi
      .spyOn(modelsApi, 'fetchModels')
      .mockResolvedValue([{ name: 'fallback-embedded-model' }]);

    const client: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return embeddedStatus();
        if (resource === 'runtime/artifact') throw new PublicApiError('network');
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    try {
      await mount(client);
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(apiKeysSpy).toHaveBeenCalledTimes(1);
      expect(fetchModelsSpy).toHaveBeenCalledWith('/console/api/v2/cpamp', undefined, {
        Authorization: 'Bearer admin-secret',
      });
      expect(JSON.stringify(renderer!.toJSON())).toContain('fallback-embedded-model');

      fetchModelsSpy.mockClear();
      apiKeysSpy.mockClear();
      useAuthStore.setState({
        connectionStatus: 'connected',
        apiBase: 'https://cpa.example/v0/management',
        sessionMode: 'external_panel',
        managementKey: 'direct-key',
      });
      useModelsStore.getState().clearCache();
      useModelsStore.setState({ models: [], loading: false, error: null, cache: null });
      await mount(client);
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(apiKeysSpy).toHaveBeenCalledTimes(1);
      expect(fetchModelsSpy).not.toHaveBeenCalled();
    } finally {
      if (renderer) {
        await act(async () => renderer!.unmount());
        renderer = undefined;
      }
      apiKeysSpy.mockRestore();
      fetchModelsSpy.mockRestore();
      useModelsStore.getState().clearCache();
      useModelsStore.setState({ models: [], loading: false, error: null, cache: null });
      useConfigStore.setState({ config: prevConfig });
      useAuthStore.setState(prevAuth);
    }
  });

  it('ignores stale refreshModels(false) once manual refreshModels(true) has started and keeps forced models', async () => {
    const prevAuth = {
      connectionStatus: useAuthStore.getState().connectionStatus,
      apiBase: useAuthStore.getState().apiBase,
      sessionMode: useAuthStore.getState().sessionMode,
      managementKey: useAuthStore.getState().managementKey,
    };
    const prevConfig = useConfigStore.getState().config;

    vi.stubGlobal('localStorage', memoryStorage());
    useAuthStore.setState({
      connectionStatus: 'connected',
      apiBase: 'https://cpa.example/v0/management',
      sessionMode: 'external_panel',
      managementKey: 'direct-key',
    });
    useConfigStore.setState({ config: { apiKeys: [] } as never });
    useModelsStore.getState().clearCache();
    useModelsStore.setState({ models: [], loading: false, error: null, cache: null });

    let resolveInitialList!: (value: string[]) => void;
    const apiKeysSpy = vi
      .spyOn(apiKeysApi, 'list')
      .mockImplementationOnce(
        () =>
          new Promise<string[]>((resolve) => {
            resolveInitialList = resolve;
          })
      )
      .mockResolvedValueOnce(['sk-manual']);
    const fetchModelsSpy = vi
      .spyOn(modelsApi, 'fetchModels')
      .mockResolvedValue([{ name: 'forced-model' }]);

    const client: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return embeddedStatus();
        if (resource === 'runtime/artifact') throw new PublicApiError('network');
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    try {
      await mount(client);
      expect(apiKeysSpy).toHaveBeenCalledTimes(1);
      expect(fetchModelsSpy).not.toHaveBeenCalled();

      // Trigger manual refreshModels(true) while initial refreshModels(false) is still awaiting apiKeysApi.list()
      const modelsCard = renderer!.root.findByProps({ 'data-testid': 'runtime-models-card' });
      const refreshBtn = modelsCard.findByProps({ 'data-testid': 'runtime-models-refresh-btn' });
      await act(async () => {
        refreshBtn.props.onClick();
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(apiKeysSpy).toHaveBeenCalledTimes(2);
      expect(fetchModelsSpy).toHaveBeenCalledTimes(1);
      expect(fetchModelsSpy).toHaveBeenCalledWith(
        'https://cpa.example/v0/management',
        'sk-manual',
        {}
      );
      expect(JSON.stringify(renderer!.toJSON())).toContain('forced-model');

      // Late resolution of initial refreshModels(false) must be ignored by refreshModelsSeqRef
      await act(async () => {
        resolveInitialList(['sk-stale']);
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(fetchModelsSpy).toHaveBeenCalledTimes(1);
      expect(useModelsStore.getState().models.map((m) => m.name)).toEqual(['forced-model']);
      expect(JSON.stringify(renderer!.toJSON())).toContain('forced-model');
    } finally {
      if (renderer) {
        await act(async () => renderer!.unmount());
        renderer = undefined;
      }
      apiKeysSpy.mockRestore();
      fetchModelsSpy.mockRestore();
      useModelsStore.getState().clearCache();
      useModelsStore.setState({ models: [], loading: false, error: null, cache: null });
      useConfigStore.setState({ config: prevConfig });
      useAuthStore.setState(prevAuth);
    }
  });

  it('localizes 503 gateway pending configuration errors and avoids refetching when config.apiKeys keeps identical values', async () => {
    const prevAuth = {
      connectionStatus: useAuthStore.getState().connectionStatus,
      apiBase: useAuthStore.getState().apiBase,
      sessionMode: useAuthStore.getState().sessionMode,
      managementKey: useAuthStore.getState().managementKey,
    };
    const prevConfig = useConfigStore.getState().config;

    vi.stubGlobal('localStorage', memoryStorage());
    useAuthStore.setState({
      connectionStatus: 'connected',
      apiBase: '/console/api/v2/cpamp',
      sessionMode: 'manager_embedded',
      managementKey: 'admin-secret',
    });
    useConfigStore.setState({ config: { apiKeys: ['sk-same'] } as never });
    useModelsStore.getState().clearCache();
    useModelsStore.setState({ models: [], loading: false, error: null, cache: null });

    const pending503 = Object.assign(new Error('Request failed with status code 503'), {
      isAxiosError: true,
      response: {
        status: 503,
        data: { error: 'gateway pending configuration' },
      },
    });
    const fetchModelsSpy = vi.spyOn(modelsApi, 'fetchModels').mockRejectedValue(pending503);

    const client: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return embeddedStatus();
        if (resource === 'runtime/artifact') throw new PublicApiError('network');
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    try {
      await mount(client);
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(fetchModelsSpy).toHaveBeenCalledTimes(1);
      const modelsCard = renderer!.root.findByProps({ 'data-testid': 'runtime-models-card' });
      const errorBox = modelsCard.findByProps({ className: 'error-box' });
      expect(errorBox.props.children).toBe(
        i18n.t('system_info.models_gateway_pending', { lng: 'zh-CN' })
      );

      // Updating config with a new array holding identical keys must not trigger another fetchModels call
      await act(async () => {
        useConfigStore.setState({ config: { apiKeys: [' sk-same '] } as never });
        await Promise.resolve();
      });
      expect(fetchModelsSpy).toHaveBeenCalledTimes(1);
    } finally {
      if (renderer) {
        await act(async () => renderer!.unmount());
        renderer = undefined;
      }
      fetchModelsSpy.mockRestore();
      useModelsStore.getState().clearCache();
      useModelsStore.setState({ models: [], loading: false, error: null, cache: null });
      useConfigStore.setState({ config: prevConfig });
      useAuthStore.setState(prevAuth);
    }
  });

  it('surfaces gateway version, recovery state, copyable artifact digest, and places anomaly banner before hero cards', async () => {
    const execCommand = vi.fn(() => true);
    const textarea = {
      value: '',
      style: {} as Record<string, string>,
      setAttribute: vi.fn(),
      focus: vi.fn(),
      select: vi.fn(),
      setSelectionRange: vi.fn(),
      remove: vi.fn(),
    };
    vi.stubGlobal('navigator', {});
    vi.stubGlobal('document', {
      visibilityState: 'visible',
      createElement: vi.fn(() => textarea),
      body: { appendChild: vi.fn(), removeChild: vi.fn() },
      execCommand,
    });

    const client: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') {
          return {
            mode: 'embedded' as const,
            status: 'degraded',
            observedAt: '2026-11-19T11:20:14Z',
            manager: { state: 'ready' },
            gateway: {
              identity: 'embedded-default',
              protocolVersion: 'v1',
              state: 'starting',
              ready: false,
              version: '8.0.78',
              artifactId: DIGEST,
              capabilities: ['restart', 'stop'],
              recovery: { state: 'armed', attemptsRemaining: 3 },
            },
          };
        }
        if (resource === 'runtime/artifact') throw new PublicApiError('network');
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    await mount(client);

    expect(
      renderer!.root.findByProps({ 'data-testid': 'runtime-status-gateway-version' }).props.children
    ).toBe('v8.0.78');
    expect(
      renderer!.root.findByProps({ 'data-testid': 'runtime-status-recovery' }).props.children
    ).toBe(i18n.t('runtime_status.recovery_armed', { lng: 'zh-CN', count: 3 }));

    // Anomaly banner is promoted above the hero lifecycle cards
    const pageChildren = renderer!.root.findByProps({ 'data-testid': 'runtime-status-page' }).children;
    const anomalyIndex = pageChildren.findIndex(
      (child) =>
        typeof child !== 'string' &&
        String(child.props.className ?? '').includes('runtime-status-anomaly')
    );
    const heroIndex = pageChildren.findIndex(
      (child) => typeof child !== 'string' && child.props.className === 'runtime-status-cards'
    );
    expect(anomalyIndex).toBeGreaterThan(-1);
    expect(heroIndex).toBeGreaterThan(anomalyIndex);

    // Clicking abbreviated artifact digest copies full sha256 digest
    const digestBtn = renderer!.root.findByProps({ 'data-testid': 'runtime-artifact-digest-btn' });
    await act(async () => {
      await digestBtn.props.onClick();
    });
    expect(execCommand).toHaveBeenCalledWith('copy');
    expect(textarea.value).toBe(DIGEST);
  });

  it('filters available models and copies model ID on chip click', async () => {
    const prevAuth = {
      connectionStatus: useAuthStore.getState().connectionStatus,
      apiBase: useAuthStore.getState().apiBase,
      sessionMode: useAuthStore.getState().sessionMode,
      managementKey: useAuthStore.getState().managementKey,
    };
    const prevConfig = useConfigStore.getState().config;

    const execCommand = vi.fn(() => true);
    const textarea = {
      value: '',
      style: {} as Record<string, string>,
      setAttribute: vi.fn(),
      focus: vi.fn(),
      select: vi.fn(),
      setSelectionRange: vi.fn(),
      remove: vi.fn(),
    };
    vi.stubGlobal('localStorage', memoryStorage());
    vi.stubGlobal('navigator', {});
    vi.stubGlobal('document', {
      visibilityState: 'visible',
      createElement: vi.fn(() => textarea),
      body: { appendChild: vi.fn(), removeChild: vi.fn() },
      execCommand,
    });
    useAuthStore.setState({
      connectionStatus: 'connected',
      apiBase: '/console/api/v2/cpamp',
      sessionMode: 'manager_embedded',
      managementKey: 'admin-secret',
    });
    useConfigStore.setState({ config: { apiKeys: ['sk-test'] } as never });
    useModelsStore.getState().clearCache();
    useModelsStore.setState({ models: [], loading: false, error: null, cache: null });

    const fetchModelsSpy = vi
      .spyOn(modelsApi, 'fetchModels')
      .mockResolvedValue([{ name: 'gpt-6.1-terra' }, { name: 'claude-sonnet-4-5' }]);

    const client: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return embeddedStatus();
        if (resource === 'runtime/artifact') throw new PublicApiError('network');
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    try {
      await mount(client);
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });

      const toggleBtn = renderer!.root.findByProps({ 'data-testid': 'runtime-models-toggle-btn' });
      await act(async () => {
        toggleBtn.props.onClick();
      });

      const filterInput = renderer!.root.findByProps({
        'data-testid': 'runtime-models-filter-input',
      });
      await act(async () => {
        filterInput.props.onChange({ target: { value: 'claude' } });
      });

      const chips = renderer!.root.findAllByProps({ 'data-testid': 'runtime-model-chip' });
      expect(chips).toHaveLength(1);
      expect(chips[0].props.title).toBe('claude-sonnet-4-5');

      await act(async () => {
        await chips[0].props.onClick();
      });
      expect(execCommand).toHaveBeenCalledWith('copy');
      expect(textarea.value).toBe('claude-sonnet-4-5');
    } finally {
      if (renderer) {
        await act(async () => renderer!.unmount());
        renderer = undefined;
      }
      fetchModelsSpy.mockRestore();
      useModelsStore.getState().clearCache();
      useModelsStore.setState({ models: [], loading: false, error: null, cache: null });
      useConfigStore.setState({ config: prevConfig });
      useAuthStore.setState(prevAuth);
    }
  });

  it('triggers upstream model refresh and reloads models list on sync button click', async () => {
    const prevAuth = {
      connectionStatus: useAuthStore.getState().connectionStatus,
      apiBase: useAuthStore.getState().apiBase,
      sessionMode: useAuthStore.getState().sessionMode,
      managementKey: useAuthStore.getState().managementKey,
    };
    const prevConfig = useConfigStore.getState().config;

    vi.stubGlobal('localStorage', memoryStorage());
    useAuthStore.setState({
      connectionStatus: 'connected',
      apiBase: '/console/api/v2/cpamp',
      sessionMode: 'manager_embedded',
      managementKey: 'admin-secret',
    });
    useConfigStore.setState({ config: { apiKeys: ['sk-test'] } as never });
    useModelsStore.getState().clearCache();
    useModelsStore.setState({ models: [], loading: false, error: null, cache: null });

    const refreshUpstreamSpy = vi
      .spyOn(modelsApi, 'refreshUpstreamModels')
      .mockResolvedValue({ status: 'ok' });
    const fetchModelsSpy = vi
      .spyOn(modelsApi, 'fetchModels')
      .mockResolvedValueOnce([{ name: 'gpt-old' }])
      .mockResolvedValueOnce([{ name: 'gpt-old' }, { name: 'gpt-new-upstream' }]);

    const client: PublicApiClient = {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return embeddedStatus();
        if (resource === 'runtime/artifact') throw new PublicApiError('network');
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(),
      put: vi.fn(),
    };

    try {
      await mount(client);
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });

      const syncBtn = renderer!.root.findByProps({ 'data-testid': 'runtime-models-sync-btn' });
      await act(async () => {
        await syncBtn.props.onClick();
      });

      expect(refreshUpstreamSpy).toHaveBeenCalledWith('/console/api/v2/cpamp', 'admin-secret');
      expect(fetchModelsSpy).toHaveBeenCalledTimes(2);
      expect(useModelsStore.getState().models.map((m) => m.name)).toContain('gpt-new-upstream');
    } finally {
      if (renderer) {
        await act(async () => renderer!.unmount());
        renderer = undefined;
      }
      refreshUpstreamSpy.mockRestore();
      fetchModelsSpy.mockRestore();
      useModelsStore.getState().clearCache();
      useModelsStore.setState({ models: [], loading: false, error: null, cache: null });
      useConfigStore.setState({ config: prevConfig });
      useAuthStore.setState(prevAuth);
    }
  });
});
describe('RuntimeStatusPage state wording', () => {
  let renderer: ReactTestRenderer | undefined;

  beforeEach(async () => {
    availabilityMock.managerServiceAvailable = false;
    availabilityMock.managerServiceBase = '';
    await i18n.changeLanguage('zh-CN');
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    vi.useFakeTimers();
    vi.stubGlobal('window', {
      setInterval,
      clearInterval,
      location: { hash: '', search: '' },
    });
    vi.stubGlobal('document', {
      visibilityState: 'visible',
    });
  });

  afterEach(async () => {
    if (renderer) {
      await act(async () => renderer!.unmount());
      renderer = undefined;
    }
    // Unmounting cancels the poll, but a refresh that is already awaiting its
    // reads still settles its state later; flush it before the stubbed globals
    // (window, document) go away, so it cannot run against a bare test context.
    await act(async () => {});
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  async function mount(client: PublicApiClient) {
    await act(async () => {
      renderer = create(
        <MemoryRouter>
          <RuntimeStatusPage client={client} credential="admin-secret" />
        </MemoryRouter>
      );
    });
  }

  function stateNode(testID: string) {
    return renderer!.root.findByProps({ 'data-testid': testID }).props.children as string;
  }

  function statusClient(status: unknown, operationState = 'running'): PublicApiClient {
    return {
      get: vi.fn(async (resource: string) => {
        if (resource === 'runtime/status') return status;
        if (resource === 'runtime/artifact') throw new PublicApiError('network');
        throw new Error(`unexpected resource ${resource}`);
      }) as PublicApiClient['get'],
      post: vi.fn(async () => ({ action: 'start', state: operationState })),
      put: vi.fn(),
    } as unknown as PublicApiClient;
  }

  // The text of an operation line, which is a sentence plus an optional trailing
  // note, so the strings it renders are read out of the node.
  function operationText() {
    const children = renderer!.root.findByProps({
      'data-testid': 'runtime-status-operation',
    }).props.children as unknown;
    return (Array.isArray(children) ? children : [children])
      .filter((child): child is string => typeof child === 'string')
      .join('');
  }

  // This language's own entry, without the zh-CN fallback: a missing key comes
  // back as the key itself, so a state that only appears translated because the
  // default language filled the gap cannot pass.
  function ownText(language: string, key: string, vars?: Record<string, string>) {
    return i18n.t(key, { lng: language, fallbackLng: false, ...vars });
  }

  // One observation per state, mounted fresh so each one is rendered by the page
  // exactly as it renders a real response.
  async function renderObservation(
    status: string,
    gateway: Record<string, unknown> = { state: status, ready: status === 'ready' }
  ) {
    if (renderer) {
      await act(async () => renderer!.unmount());
      renderer = undefined;
    }
    await mount(
      statusClient({
        mode: 'embedded',
        status,
        observedAt: '2026-11-09T00:00:00Z',
        manager: { state: status },
        gateway,
      })
    );
  }

  it('names the Manager and gateway states in the console language, never the wire value', async () => {
    await mount(
      statusClient({
        mode: 'embedded',
        status: 'ready',
        observedAt: '2026-11-09T00:00:00Z',
        manager: { state: 'ready' },
        gateway: { state: 'offline', ready: false },
      })
    );

    expect(stateNode('runtime-status-state')).toBe(
      i18n.t('runtime_status.state_ready', { lng: 'zh-CN' })
    );
    expect(stateNode('runtime-status-manager-state')).toBe(
      i18n.t('runtime_status.state_ready', { lng: 'zh-CN' })
    );
    expect(stateNode('runtime-status-gateway-state')).toBe(
      i18n.t('runtime_status.state_offline', { lng: 'zh-CN' })
    );
    expect(stateNode('runtime-status-gateway-state')).not.toBe('offline');

    await act(async () => {
      await i18n.changeLanguage('en');
    });
    expect(stateNode('runtime-status-state')).toBe(
      i18n.t('runtime_status.state_ready', { lng: 'en' })
    );
    expect(stateNode('runtime-status-gateway-state')).toBe(
      i18n.t('runtime_status.state_offline', { lng: 'en' })
    );

    await act(async () => {
      await i18n.changeLanguage('ru');
    });
    expect(stateNode('runtime-status-gateway-state')).toBe(
      i18n.t('runtime_status.state_offline', { lng: 'ru' })
    );
  });

  it('names a running operation in the console language', async () => {
    // The Manager reports `running` while an operation is in flight; an unnamed
    // state would print the wire word in every language.
    const mounted = statusClient({
      mode: 'embedded',
      status: 'ready',
      manager: { state: 'ready' },
      gateway: { state: 'offline', ready: false, capabilities: ['start', 'stop'] },
    });
    await mount(mounted);
    await act(async () => {
      await renderer!.root
        .findByProps({ 'data-testid': 'runtime-status-start' })
        .props.onClick();
    });

    expect(operationText()).toContain(
      i18n.t('runtime_status.operation_running', { lng: 'zh-CN' })
    );
    expect(operationText()).not.toMatch(/\brunning\b/);
  });

  it.each(['zh-CN', 'zh-TW', 'en', 'ru'])(
    'renders every Manager, gateway and operation state in %s',
    async (language) => {
      await act(async () => {
        await i18n.changeLanguage(language);
      });

      // The Manager's closed state set, rendered one observation at a time. The
      // expected sentence is looked up in this language without the zh-CN
      // fallback, so an entry this language does not carry renders as the key
      // instead of the default language's sentence and fails here.
      for (const state of ['ready', 'offline', 'starting', 'unknown'] as const) {
        await renderObservation(state);
        const key = `runtime_status.state_${state}`;
        const expected = ownText(language, key);
        expect(expected, key).not.toBe(key);
        for (const testID of [
          'runtime-status-state',
          'runtime-status-manager-state',
          'runtime-status-gateway-state',
        ]) {
          expect(stateNode(testID), `${testID}/${language}`).toBe(expected);
          // The wire word is never what the page shows an operator.
          expect(stateNode(testID), `${testID}/${language}`).not.toBe(state);
        }
      }

      // The operation states a Start leaves on screen, driven through the same
      // button an operator presses rather than through the label helper.
      for (const state of ['accepted', 'running', 'succeeded', 'failed'] as const) {
        if (renderer) {
          await act(async () => renderer!.unmount());
          renderer = undefined;
        }
        await mount(
          statusClient(
            {
              mode: 'embedded',
              status: 'ready',
              observedAt: '2026-11-09T00:00:00Z',
              manager: { state: 'ready' },
              gateway: { state: 'offline', ready: false, capabilities: ['start', 'stop'] },
            },
            state
          )
        );
        await act(async () => {
          await renderer!.root
            .findByProps({ 'data-testid': 'runtime-status-start' })
            .props.onClick();
        });

        const key = `runtime_status.operation_${state}`;
        const expectedState = ownText(language, key);
        expect(expectedState, key).not.toBe(key);
        expect(expectedState, key).not.toBe(state);
        const expected = ownText(language, 'runtime_status.operation_status', {
          action: ownText(language, 'runtime_status.start'),
          state: expectedState,
        });
        // The line is this language's sentence, not a key left unresolved.
        expect(expected, key).not.toContain('runtime_status.');
        expect(operationText(), `${key}/${language}`).toBe(expected);
      }
    }
  );
});
