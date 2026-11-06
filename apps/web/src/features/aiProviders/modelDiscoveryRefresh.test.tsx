import { createElement, type ComponentType, type ReactNode } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ClaudeEditDrawer } from '@/components/providers/ProviderEditDrawer/ClaudeEditDrawer';
import { CodexEditDrawer } from '@/components/providers/ProviderEditDrawer/CodexEditDrawer';
import { GeminiEditDrawer } from '@/components/providers/ProviderEditDrawer/GeminiEditDrawer';
import { OpenAIEditDrawer } from '@/components/providers/ProviderEditDrawer/OpenAIEditDrawer';
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
    t: (key: string) => key,
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
  useNotificationStore: () => ({ showNotification: mocks.noop }),
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
  getApiCallErrorMessage: () => '',
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
const cases = [
  ['Claude drawer', ClaudeEditDrawer, 'claude', true],
  ['Codex drawer', CodexEditDrawer, 'codex', true],
  ['Gemini drawer', GeminiEditDrawer, 'gemini', true],
  ['OpenAI drawer', OpenAIEditDrawer, 'openai', true],
  ['Claude models route', AiProvidersClaudeModelsPage, 'claude', false],
  ['OpenAI models route', AiProvidersOpenAIModelsPage, 'openai', false],
  ['Codex edit route', AiProvidersCodexEditPage, 'codex', false],
  ['Gemini edit route', AiProvidersGeminiEditPage, 'gemini', false],
] as const;

describe.each(cases)('%s revalidation', (_name, Component, provider, drawer) => {
  const element = () =>
    createElement(
      Component as ComponentType<Record<string, unknown>>,
      drawer
        ? { open: true, editIndex: 0, disabled: false, onClose: mocks.noop, onSaved: mocks.noop }
        : {}
    );
  const button = (suffix: string) =>
    renderer.root
      .findAllByType('button')
      .find((node) =>
        node
          .findAllByType('span')
          .some((span) => span.children.includes(`ai_providers.${provider}_models_fetch_${suffix}`))
      )!;
  const modelVisible = (name: string) =>
    renderer.root.findAllByType('input').some((node) => node.props['aria-label'] === name);
  const revalidate = async () => {
    const refresh = button('refresh');
    if (refresh) {
      await act(async () => {
        refresh.props.onClick();
      });
      return;
    }
    const cancel = renderer.root
      .findAllByType('button')
      .find((node) =>
        node.findAllByType('span').some((span) => span.children.includes('common.cancel'))
      )!;
    act(() => {
      cancel.props.onClick();
    });
    await act(async () => {
      button('button').props.onClick();
    });
  };
  const open = async () => {
    await act(async () => {
      renderer = create(element());
    });
    const trigger = button('button');
    if (trigger)
      await act(async () => {
        trigger.props.onClick();
      });
  };
  it('keeps model choices during refresh and failure, then replaces them on success', async () => {
    const first = deferred();
    mocks.fetch.mockReturnValueOnce(first.promise);
    await open();
    expect(modelVisible('old-model')).toBe(false);
    expect(JSON.stringify(renderer.toJSON())).toContain(
      `ai_providers.${provider}_models_fetch_loading`
    );
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
    expect(JSON.stringify(renderer.toJSON())).not.toContain(
      `ai_providers.${provider}_models_fetch_loading`
    );
    await act(async () => { pending.reject(new Error('offline')); });
    expect(JSON.stringify(renderer.toJSON())).toContain('offline');
  });

  it('keeps a bootstrap discovery failure distinct from a successful empty result', async () => {
    const first = deferred();
    mocks.fetch.mockReturnValueOnce(first.promise);
    await open();
    await act(async () => { first.reject(new Error('bootstrap offline')); });
    const output = JSON.stringify(renderer.toJSON());
    expect(output).toContain('bootstrap offline');
    expect(output).not.toContain(`ai_providers.${provider}_models_fetch_empty`);
  });
});
