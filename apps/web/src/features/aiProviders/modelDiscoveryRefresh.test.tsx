import { createElement, type ComponentType, type ReactNode } from 'react';
import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { buildProviderRows, ProviderWorkspaceDrawer } from '@/components/providers';
import { AiProvidersClaudeModelsPage } from './AiProvidersClaudeModelsPage';
import { AiProvidersOpenAIModelsPage } from './AiProvidersOpenAIModelsPage';
import { AiProvidersCodexEditPage } from './AiProvidersCodexEditPage';
import { AiProvidersGeminiEditPage } from './AiProvidersGeminiEditPage';
import type { ModelInfo } from '@/utils/models';

const mocks = vi.hoisted(() => {
  const entry = {
    apiKey: 'test-key',
    baseUrl: 'https://provider.test',
    headers: {},
    models: [],
    excludedModels: [],
  };
  const form = {
    ...entry,
    headers: [],
    modelEntries: [],
    apiKeyEntries: [{ apiKey: 'test-key', proxyUrl: '' }],
  };
  return {
    t: (key: string, options?: Record<string, unknown>) =>
      options?.count !== undefined ? `${key}:${String(options.count)}` : key,
    auth: { apiBase: 'https://a.test', managementKey: 'a', connectionStatus: 'connected' },
    fetch: vi.fn(),
    fetchConfig: vi.fn(),
    noop: vi.fn(),
    config: {
      claudeApiKeys: [entry],
      codexApiKeys: [entry],
      geminiApiKeys: [entry],
      openaiCompatibility: [{ ...entry, name: 'test', apiKeyEntries: form.apiKeyEntries }],
    },
    context: {
      form,
      loading: false,
      saving: false,
      disableControls: false,
      mergeDiscoveredModels: vi.fn(),
    },
  };
});
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: mocks.t }) }));
vi.mock('react-router-dom', () => ({
  useNavigate: () => mocks.noop,
  useParams: () => ({ index: '0' }),
  useLocation: () => ({ pathname: '/ai-providers/codex/0', state: null }),
  useOutletContext: () => mocks.context,
}));
vi.mock('@/hooks/useEdgeSwipeBack', () => ({ useEdgeSwipeBack: () => ({ current: null }) }));
vi.mock('@/hooks/useUnsavedChangesGuard', () => ({ useUnsavedChangesGuard: () => ({}) }));
vi.mock('@/components/ui/Drawer', () => ({
  Drawer: ({ children, footer }: { children: ReactNode; footer: ReactNode }) => (
    <div>
      {children}
      {footer}
    </div>
  ),
}));
vi.mock('@/components/ui/Modal', () => ({
  Modal: ({ open, children, footer }: { open: boolean; children: ReactNode; footer: ReactNode }) =>
    open ? (
      <div>
        {children}
        {footer}
      </div>
    ) : null,
}));
vi.mock('@/components/common/SecondaryScreenShell', () => ({
  SecondaryScreenShell: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));
vi.mock('@/stores/useAuthStore', () => ({
  useAuthStore: (selector: (s: typeof mocks.auth) => unknown) => selector(mocks.auth),
}));
vi.mock('@/stores', () => ({
  useAuthStore: (selector: (s: typeof mocks.auth) => unknown) => selector(mocks.auth),
  useConfigStore: (selector: (s: object) => unknown) =>
    selector({
      config: mocks.config,
      fetchConfig: mocks.fetchConfig,
      updateConfigValue: mocks.noop,
      clearCache: mocks.noop,
      isCacheValid: () => true,
    }),
  useNotificationStore: (selector?: (s: { showNotification: typeof mocks.noop; showConfirmation: typeof mocks.noop }) => unknown) => {
    const state = {
      showNotification: mocks.noop,
      showConfirmation: mocks.noop,
    };
    return selector ? selector(state) : state;
  },
}));
vi.mock('@/services/api', () => ({
  modelsApi: {
    fetchClaudeModelsViaApiCall: mocks.fetch,
    fetchV1ModelsViaApiCall: mocks.fetch,
    fetchGeminiModelsViaApiCall: mocks.fetch,
    fetchModelsViaApiCall: mocks.fetch,
    buildClaudeModelsEndpoint: () => 'https://provider.test/models',
    buildV1ModelsEndpoint: () => 'https://provider.test/models',
    buildGeminiModelsEndpoint: () => 'https://provider.test/models',
  },
  providersApi: { getOpenAIProviders: async () => mocks.config.openaiCompatibility },
  apiCallApi: {},
  getApiCallErrorDetails: () => null,
  getApiCallErrorMessage: (err: unknown) => (err instanceof Error ? err.message : String(err)),
  readBackClaudeConfigAfterSave: vi.fn(),
  verifyClaudeFingerprintInRawConfig: vi.fn(),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const deferred = () => {
  let resolve!: (value: ModelInfo[]) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<ModelInfo[]>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { resolve, reject, promise };
};
const extractText = (node: ReactTestInstance): string =>
  node.children
    .map((child) =>
      typeof child === 'string' || typeof child === 'number' ? String(child) : extractText(child)
    )
    .join(' ');
let renderer: ReactTestRenderer;
afterEach(() => {
  act(() => renderer?.unmount());
  vi.unstubAllGlobals();
});
beforeEach(() => {
  vi.stubGlobal('window', { addEventListener: vi.fn(), removeEventListener: vi.fn() });
  mocks.fetch.mockReset();
  mocks.auth.managementKey = 'a';
  mocks.fetchConfig.mockImplementation(async (section: string) =>
    section
      ? [
          {
            apiKey: 'test-key',
            baseUrl: 'https://provider.test',
            headers: {},
            models: [],
            excludedModels: [],
          },
        ]
      : mocks.config
  );
});

const buildDrawerRow = (provider: 'claude' | 'codex' | 'gemini' | 'openai') => {
  const rows = buildProviderRows({
    gemini: provider === 'gemini' ? mocks.config.geminiApiKeys : [],
    interactions: [],
    codex: provider === 'codex' ? mocks.config.codexApiKeys : [],
    xai: [],
    claude: provider === 'claude' ? mocks.config.claudeApiKeys : [],
    vertex: [],
    openai: provider === 'openai' ? mocks.config.openaiCompatibility : [],
    usageByProvider: new Map(),
  });
  return rows[0] ?? null;
};

const cases = [
  ['Claude workspace drawer', ProviderWorkspaceDrawer, 'claude', true],
  ['Codex workspace drawer', ProviderWorkspaceDrawer, 'codex', true],
  ['Gemini workspace drawer', ProviderWorkspaceDrawer, 'gemini', true],
  ['OpenAI workspace drawer', ProviderWorkspaceDrawer, 'openai', true],
  ['Claude models route', AiProvidersClaudeModelsPage, 'claude', false],
  ['OpenAI models route', AiProvidersOpenAIModelsPage, 'openai', false],
  ['Codex edit route', AiProvidersCodexEditPage, 'codex', false],
  ['Gemini edit route', AiProvidersGeminiEditPage, 'gemini', false],
] as const;

describe.each(cases)('%s revalidation', (_name, Component, provider, drawer) => {
  const loadingKey = drawer
    ? 'ai_providers.fetching_models_loading'
    : `ai_providers.${provider}_models_fetch_loading`;
  const emptyKey = drawer
    ? 'ai_providers.no_discovered_models'
    : `ai_providers.${provider}_models_fetch_empty`;

  const element = () =>
    createElement(
      Component as ComponentType<Record<string, unknown>>,
      drawer
        ? {
            open: true,
            mode: 'view-edit',
            row: buildDrawerRow(provider),
            initialTab: 'models',
            usageByProvider: new Map(),
            onClose: mocks.noop,
            onSaved: mocks.noop,
          }
        : {}
    );
  const findButtonByLabel = (label: string) =>
    renderer.root
      .findAllByType('button')
      .find((node) => node.findAllByType('span').some((span) => span.children.includes(label)));

  const triggerOpenButton = () =>
    drawer
      ? findButtonByLabel('ai_providers.fetch_upstream_models_btn')
      : findButtonByLabel(`ai_providers.${provider}_models_fetch_button`);

  const modelVisible = (name: string) =>
    renderer.root.findAllByType('input').some((node) => node.props['aria-label'] === name);

  const revalidate = async () => {
    const refresh = drawer
      ? findButtonByLabel('common.refresh')
      : findButtonByLabel(`ai_providers.${provider}_models_fetch_refresh`);
    if (refresh) {
      await act(async () => {
        refresh.props.onClick();
      });
      return;
    }
    const cancel = findButtonByLabel('common.cancel')!;
    act(() => {
      cancel.props.onClick();
    });
    await act(async () => {
      triggerOpenButton()!.props.onClick();
    });
  };
  const open = async () => {
    await act(async () => {
      renderer = create(element());
    });
    const trigger = triggerOpenButton();
    if (trigger) {
      await act(async () => {
        trigger.props.onClick();
      });
    }
  };
  it('keeps model choices during refresh and failure, then replaces them on success', async () => {
    const first = deferred();
    mocks.fetch.mockReturnValueOnce(first.promise);
    await open();
    expect(modelVisible('old-model')).toBe(false);
    expect(JSON.stringify(renderer.toJSON())).toContain(loadingKey);
    await act(async () => {
      first.resolve([{ name: 'old-model' }]);
    });
    expect(modelVisible('old-model')).toBe(true);
    const next = deferred();
    mocks.fetch.mockReturnValueOnce(next.promise);
    await revalidate();
    expect(modelVisible('old-model')).toBe(true);
    await act(async () => {
      next.reject(new Error('offline'));
    });
    expect(modelVisible('old-model')).toBe(true);
    expect(JSON.stringify(renderer.toJSON())).toContain('offline');
    mocks.fetch.mockResolvedValueOnce([{ name: 'new-model' }]);
    await revalidate();
    expect(modelVisible('new-model')).toBe(true);
    expect(modelVisible('old-model')).toBe(false);
  });
  it('discards old connection data and ignores its late completion', async () => {
    mocks.fetch.mockResolvedValueOnce([{ name: 'old-model' }]);
    await open();
    const pending = deferred();
    mocks.fetch.mockReturnValueOnce(pending.promise);
    await revalidate();
    mocks.fetch.mockReturnValue(new Promise(() => {}));
    mocks.auth.managementKey = 'b';
    await act(async () => {
      renderer.update(element());
    });
    expect(modelVisible('old-model')).toBe(false);
    await act(async () => {
      pending.resolve([{ name: 'late-model' }]);
    });
    expect(modelVisible('late-model')).toBe(false);
  });
  it('keeps a successful empty result out of initial loading on refresh', async () => {
    mocks.fetch.mockResolvedValueOnce([]);
    await open();
    const pending = deferred();
    mocks.fetch.mockReturnValueOnce(pending.promise);
    await revalidate();
    expect(JSON.stringify(renderer.toJSON())).not.toContain(loadingKey);
    await act(async () => {
      pending.reject(new Error('offline'));
    });
    expect(JSON.stringify(renderer.toJSON())).toContain('offline');
  });

  it('keeps a bootstrap discovery failure distinct from a successful empty result', async () => {
    const first = deferred();
    mocks.fetch.mockReturnValueOnce(first.promise);
    await open();
    await act(async () => {
      first.reject(new Error('bootstrap offline'));
    });
    const output = JSON.stringify(renderer.toJSON());
    expect(output).toContain('bootstrap offline');
    expect(output).not.toContain(emptyKey);
  });
});
describe('ProviderWorkspaceDrawer model discovery & merge', () => {
  it('merges OpenAI per-key headers during discovery and preserves multi-alias entries with deterministic addedCount', async () => {
    mocks.noop.mockClear();
    mocks.fetch.mockResolvedValueOnce([
      { name: 'gpt-4o', alias: 'gpt-4o-discovered' },
      { name: 'gpt-4.1', alias: 'g41' },
      { name: 'o3-mini' },
    ]);

    const rows = buildProviderRows({
      gemini: [],
      interactions: [],
      codex: [],
      xai: [],
      claude: [],
      vertex: [],
      openai: [
        {
          name: 'OpenAI Multi-Alias',
          baseUrl: 'https://provider.test/v1',
          headers: { 'X-Provider': 'base', 'X-Shared': 'provider' },
          apiKeyEntries: [
            {
              apiKey: 'sk-entry-1',
              headers: { 'X-Entry': 'entry-val', 'X-Shared': 'override' },
            },
          ],
          models: [
            { name: 'gpt-4o', alias: 'fast' },
            { name: 'gpt-4o', alias: 'smart' },
          ],
        },
      ],
      usageByProvider: new Map(),
    });

    await act(async () => {
      renderer = create(
        createElement(ProviderWorkspaceDrawer, {
          open: true,
          mode: 'view-edit',
          row: rows[0],
          initialTab: 'models',
          usageByProvider: new Map(),
          onClose: mocks.noop,
          onSaved: mocks.noop,
        })
      );
    });

    const discoverBtn = renderer.root
      .findAllByType('button')
      .find((btn) => extractText(btn).includes('ai_providers.fetch_upstream_models_btn'));
    expect(discoverBtn).toBeDefined();
    await act(async () => {
      discoverBtn!.props.onClick();
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(mocks.fetch).toHaveBeenCalledWith(
      'https://provider.test/v1',
      'sk-entry-1',
      { 'X-Provider': 'base', 'X-Shared': 'override', 'X-Entry': 'entry-val' },
      undefined,
      undefined,
      false
    );

    const selectAllBtn = renderer.root
      .findAllByType('button')
      .find((btn) => extractText(btn).includes('common.select_all'));
    expect(selectAllBtn).toBeDefined();
    await act(async () => {
      selectAllBtn!.props.onClick();
    });

    const applyBtn = renderer.root
      .findAllByType('button')
      .find((btn) => extractText(btn).includes('ai_providers.apply_selected_models_btn'));
    expect(applyBtn).toBeDefined();
    await act(async () => {
      applyBtn!.props.onClick();
    });
    expect(mocks.noop).toHaveBeenCalledWith('ai_providers.openai_models_fetch_added:2', 'success');
    const inputValues = renderer.root
      .findAllByType('input')
      .map((input) => String(input.props.value ?? ''));
    expect(inputValues).toEqual(
      expect.arrayContaining(['gpt-4o', 'fast', 'smart', 'gpt-4.1', 'g41', 'o3-mini'])
    );
  });
});
