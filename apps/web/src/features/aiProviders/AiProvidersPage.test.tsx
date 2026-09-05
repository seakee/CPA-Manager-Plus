import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { CoolingPolicy, GeminiKeyConfig, ProviderKeyConfig } from '@/types';

const mocks = vi.hoisted(() => ({
  config: {} as {
    geminiApiKeys: GeminiKeyConfig[];
    interactionsApiKeys: GeminiKeyConfig[];
    codexApiKeys: ProviderKeyConfig[];
    xaiApiKeys: ProviderKeyConfig[];
    claudeApiKeys: ProviderKeyConfig[];
    vertexApiKeys: ProviderKeyConfig[];
    openaiCompatibility: never[];
  },
  fetchConfig: vi.fn(),
  updateConfigValue: vi.fn(),
  clearCache: vi.fn(),
  updateGeminiKey: vi.fn(),
  updateVertexConfig: vi.fn(),
  getVertexConfigs: vi.fn(),
  getOpenAIProviders: vi.fn(),
  showNotification: vi.fn(),
  showConfirmation: vi.fn(),
  cacheValid: true,
  transitionLayer: null as { status: string } | null,
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock('@/hooks/useHeaderRefresh', () => ({ useHeaderRefresh: () => undefined }));
vi.mock('@/components/common/PageTransitionLayer', () => ({
  usePageTransitionLayer: () => mocks.transitionLayer,
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: { connectionStatus: string }) => unknown) =>
    selector({ connectionStatus: 'connected' }),
  useThemeStore: (selector: (state: { resolvedTheme: string }) => unknown) =>
    selector({ resolvedTheme: 'light' }),
  useConfigStore: (selector: (state: Record<string, unknown>) => unknown) =>
    selector({
      config: mocks.config,
      fetchConfig: mocks.fetchConfig,
      updateConfigValue: mocks.updateConfigValue,
      clearCache: mocks.clearCache,
      isCacheValid: () => mocks.cacheValid,
    }),
  useNotificationStore: () => ({
    showNotification: mocks.showNotification,
    showConfirmation: mocks.showConfirmation,
  }),
}));

vi.mock('@/services/api', () => ({
  providersApi: {
    getVertexConfigs: mocks.getVertexConfigs,
    getOpenAIProviders: mocks.getOpenAIProviders,
    updateGeminiKey: mocks.updateGeminiKey,
    updateVertexConfig: mocks.updateVertexConfig,
  },
}));

vi.mock('@/components/providers', async () => {
  const policyFromOverride = (value: boolean | null | undefined): CoolingPolicy =>
    value === true ? 'disabled' : value === false ? 'enabled' : 'inherit';

  return {
    buildProviderRows: ({
      gemini,
      vertex,
    }: {
      gemini: GeminiKeyConfig[];
      vertex: ProviderKeyConfig[];
    }) => [
      ...gemini.map((raw, originalIndex) => ({
        key: `gemini:${originalIndex}`,
        kind: 'gemini' as const,
        originalIndex,
        raw,
        modelNames: [],
        enabled: true,
        label: raw.apiKey,
        sortName: raw.apiKey,
        baseUrl: raw.baseUrl ?? '',
      })),
      ...vertex.map((raw, originalIndex) => ({
        key: `vertex:${originalIndex}`,
        kind: 'vertex' as const,
        originalIndex,
        raw,
        modelNames: [],
        enabled: true,
        label: raw.apiKey,
        sortName: raw.apiKey,
        baseUrl: raw.baseUrl ?? '',
      })),
    ],
    filterAndSortProviderRows: (rows: unknown[]) => rows,
    PROVIDER_KIND_LABELS: {
      all: 'All',
      gemini: 'Gemini',
      interactions: 'Interactions',
      codex: 'Codex',
      xai: 'xAI',
      claude: 'Claude',
      vertex: 'Vertex',
      openai: 'OpenAI',
    },
    ProviderToolbar: () => null,
    ProviderTable: ({
      rows,
      onShowDetail,
    }: {
      rows: Array<Record<string, unknown>>;
      onShowDetail: (row: Record<string, unknown>) => void;
    }) =>
      rows[0] ? (
        <button type="button" data-open-detail onClick={() => onShowDetail(rows[0])}>
          open
        </button>
      ) : null,
    ProviderDetailDrawer: ({
      row,
      open,
      onToggleDisableCooling,
    }: {
      row: { raw: GeminiKeyConfig | ProviderKeyConfig } | null;
      open: boolean;
      onToggleDisableCooling: (
        row: { raw: GeminiKeyConfig | ProviderKeyConfig },
        policy: CoolingPolicy
      ) => void;
    }) =>
      open && row ? (
        <div>
          <span data-current-policy>{policyFromOverride(row.raw.disableCooling)}</span>
          {(['inherit', 'enabled', 'disabled'] as const).map((policy) => (
            <button
              type="button"
              key={policy}
              data-policy={policy}
              onClick={() => onToggleDisableCooling(row, policy)}
            >
              {policy}
            </button>
          ))}
        </div>
      ) : null,
    ProviderHealthCheckDrawer: () => null,
    GeminiEditDrawer: () => null,
    CodexEditDrawer: () => null,
    ClaudeEditDrawer: () => null,
    OpenAIEditDrawer: () => null,
    VertexEditDrawer: () => null,
    useProviderRecentRequests: () => ({
      usageByProvider: new Map(),
      loadRecentRequests: vi.fn(async () => undefined),
      refreshRecentRequests: vi.fn(async () => undefined),
    }),
  };
});

import { AiProvidersPage } from './AiProvidersPage';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const flush = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
};

const openDetail = async (renderer: ReactTestRenderer) => {
  await act(async () => {
    renderer.root.findByProps({ 'data-open-detail': true }).props.onClick();
  });
};

describe('AiProvidersPage cooling policy mutation', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.cacheValid = true;
    mocks.transitionLayer = null;
    mocks.config = {
      geminiApiKeys: [{ apiKey: 'gemini-key' }],
      interactionsApiKeys: [],
      codexApiKeys: [],
      xaiApiKeys: [],
      claudeApiKeys: [],
      vertexApiKeys: [],
      openaiCompatibility: [],
    };
    mocks.fetchConfig.mockImplementation(async () => mocks.config);
    mocks.getVertexConfigs.mockImplementation(async () => mocks.config.vertexApiKeys);
    mocks.getOpenAIProviders.mockResolvedValue([]);
    mocks.updateGeminiKey.mockImplementation(
      async (_original: GeminiKeyConfig, next: GeminiKeyConfig) => {
        mocks.config = { ...mocks.config, geminiApiKeys: [next] };
      }
    );
    mocks.updateVertexConfig.mockImplementation(
      async (_original: ProviderKeyConfig, next: ProviderKeyConfig) => {
        mocks.config = { ...mocks.config, vertexApiKeys: [next] };
      }
    );
  });

  it.each([
    [undefined, 'disabled', true],
    [undefined, 'enabled', false],
    [true, 'inherit', null],
    [false, 'inherit', null],
    [true, 'enabled', false],
    [false, 'disabled', true],
  ] as const)(
    'sends %j -> %s as disable-cooling %j',
    async (initialOverride, nextPolicy, expectedOverride) => {
      mocks.config.geminiApiKeys = [
        {
          apiKey: 'gemini-key',
          ...(initialOverride === undefined ? {} : { disableCooling: initialOverride }),
        },
      ];
      let renderer!: ReactTestRenderer;
      await act(async () => {
        renderer = create(<AiProvidersPage />);
      });
      await flush();
      await openDetail(renderer);

      await act(async () => {
        renderer.root.findByProps({ 'data-policy': nextPolicy }).props.onClick();
        await Promise.resolve();
        await Promise.resolve();
      });
      await flush();

      expect(mocks.updateGeminiKey).toHaveBeenCalledWith(
        expect.objectContaining({ apiKey: 'gemini-key' }),
        expect.objectContaining({
          apiKey: 'gemini-key',
          disableCooling: expectedOverride,
        })
      );
      expect(renderer.root.findByProps({ 'data-current-policy': true }).children.join('')).toBe(
        nextPolicy
      );

      act(() => renderer.unmount());
    }
  );

  it('rolls an optimistic cooling policy change back after a failed request', async () => {
    mocks.updateGeminiKey.mockRejectedValueOnce(new Error('save failed'));
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();
    await openDetail(renderer);

    await act(async () => {
      renderer.root.findByProps({ 'data-policy': 'disabled' }).props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    await flush();

    expect(renderer.root.findByProps({ 'data-current-policy': true }).children.join('')).toBe(
      'inherit'
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      expect.stringContaining('notification.update_failed'),
      'error'
    );

    act(() => renderer.unmount());
  });

  it.each([
    [undefined, 'enabled', false],
    [true, 'inherit', null],
  ] as const)(
    'sends the Vertex %j -> %s transition as disable-cooling %j',
    async (initialOverride, nextPolicy, expectedOverride) => {
      mocks.config.geminiApiKeys = [];
      mocks.config.vertexApiKeys = [
        {
          apiKey: 'vertex-key',
          baseUrl: 'https://vertex.example.com',
          ...(initialOverride === undefined ? {} : { disableCooling: initialOverride }),
        },
      ];
      let renderer!: ReactTestRenderer;
      await act(async () => {
        renderer = create(<AiProvidersPage />);
      });
      await flush();
      await openDetail(renderer);

      await act(async () => {
        renderer.root.findByProps({ 'data-policy': nextPolicy }).props.onClick();
        await Promise.resolve();
        await Promise.resolve();
      });
      await flush();

      expect(mocks.updateVertexConfig).toHaveBeenCalledWith(
        expect.objectContaining({ apiKey: 'vertex-key' }),
        expect.objectContaining({
          apiKey: 'vertex-key',
          disableCooling: expectedOverride,
        })
      );
      expect(renderer.root.findByProps({ 'data-current-policy': true }).children.join('')).toBe(
        nextPolicy
      );

      act(() => renderer.unmount());
    }
  );

  it('rolls a failed Vertex cooling policy mutation back', async () => {
    mocks.config.geminiApiKeys = [];
    mocks.config.vertexApiKeys = [{ apiKey: 'vertex-key', baseUrl: 'https://vertex.example.com' }];
    mocks.updateVertexConfig.mockRejectedValueOnce(new Error('vertex save failed'));
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();
    await openDetail(renderer);

    await act(async () => {
      renderer.root.findByProps({ 'data-policy': 'disabled' }).props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    await flush();

    expect(renderer.root.findByProps({ 'data-current-policy': true }).children.join('')).toBe(
      'inherit'
    );
    expect(mocks.updateConfigValue).toHaveBeenLastCalledWith(
      'vertex-api-key',
      expect.arrayContaining([expect.not.objectContaining({ disableCooling: true })])
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      expect.stringContaining('notification.update_failed'),
      'error'
    );

    act(() => renderer.unmount());
  });
});

describe('AiProvidersPage current-layer config refresh', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.cacheValid = true;
    mocks.transitionLayer = null;
    mocks.config = {
      geminiApiKeys: [],
      interactionsApiKeys: [],
      codexApiKeys: [],
      xaiApiKeys: [],
      claudeApiKeys: [],
      vertexApiKeys: [],
      openaiCompatibility: [],
    };
    mocks.fetchConfig.mockImplementation(async () => mocks.config);
    mocks.getVertexConfigs.mockImplementation(async () => mocks.config.vertexApiKeys);
    mocks.getOpenAIProviders.mockResolvedValue([]);
    mocks.updateGeminiKey.mockResolvedValue(undefined);
    mocks.updateVertexConfig.mockResolvedValue(undefined);
  });

  const renderPage = async () => {
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();
    return renderer;
  };

  const updatePage = async (renderer: ReactTestRenderer) => {
    await act(async () => {
      renderer.update(<AiProvidersPage />);
    });
    await flush();
  };

  it('reloads provider configs when the invalidated cache meets the current layer again', async () => {
    const renderer = await renderPage();
    expect(mocks.fetchConfig).toHaveBeenCalledTimes(1);

    mocks.transitionLayer = { status: 'background' };
    await updatePage(renderer);

    mocks.cacheValid = false;
    mocks.transitionLayer = { status: 'current' };
    await updatePage(renderer);

    expect(mocks.fetchConfig).toHaveBeenCalledTimes(2);
    act(() => renderer.unmount());
  });

  it('does not reload provider configs when the cache is still valid on re-entry', async () => {
    const renderer = await renderPage();
    expect(mocks.fetchConfig).toHaveBeenCalledTimes(1);

    mocks.transitionLayer = { status: 'background' };
    await updatePage(renderer);

    mocks.transitionLayer = { status: 'current' };
    await updatePage(renderer);

    expect(mocks.fetchConfig).toHaveBeenCalledTimes(1);
    act(() => renderer.unmount());
  });
});
