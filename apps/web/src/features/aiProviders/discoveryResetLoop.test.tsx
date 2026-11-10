import { createElement, type ComponentType, type ReactNode } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ClaudeEditDrawer } from '@/components/providers/ProviderEditDrawer/ClaudeEditDrawer';
import { CodexEditDrawer } from '@/components/providers/ProviderEditDrawer/CodexEditDrawer';
import { GeminiEditDrawer } from '@/components/providers/ProviderEditDrawer/GeminiEditDrawer';
import { OpenAIEditDrawer } from '@/components/providers/ProviderEditDrawer/OpenAIEditDrawer';
import type { ModelInfo } from '@/utils/models';

const mocks = vi.hoisted(() => {
  const renders = { count: 0 };
  const t = (key: string) => key;
  const entry = { apiKey: '', baseUrl: '', headers: {}, models: [], excludedModels: [] };
  return {
    t,
    renders,
    auth: { apiBase: 'https://a.test', managementKey: 'a', connectionStatus: 'connected' },
    fetch: vi.fn(async () => [] as ModelInfo[]),
    fetchConfig: vi.fn(async () => null),
    noop: vi.fn(),
    config: {
      claudeApiKeys: [entry],
      codexApiKeys: [entry],
      geminiApiKeys: [entry],
      openaiCompatibility: [
        { ...entry, name: 'test', apiKeyEntries: [{ apiKey: '', proxyUrl: '' }] },
      ],
    },
  };
});

vi.mock('react-i18next', () => ({
  useTranslation: () => {
    mocks.renders.count += 1;
    return { t: mocks.t };
  },
}));
vi.mock('react-router-dom', () => ({
  useNavigate: () => mocks.noop,
  useParams: () => ({ index: '0' }),
  useLocation: () => ({ pathname: '/ai-providers/openai', state: null }),
  useOutletContext: () => ({ form: null, loading: false, saving: false }),
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
let renderer: ReactTestRenderer | undefined;

beforeEach(() => {
  vi.stubGlobal('window', { addEventListener: vi.fn(), removeEventListener: vi.fn() });
  mocks.fetch.mockClear();
});

const drawers = [
  ['OpenAI drawer', OpenAIEditDrawer, 'ai_providers.openai_add_modal_url_label'],
  ['Gemini drawer', GeminiEditDrawer, 'ai_providers.gemini_base_url_label'],
  ['Claude drawer', ClaudeEditDrawer, 'ai_providers.claude_add_modal_url_label'],
  ['Codex drawer', CodexEditDrawer, 'ai_providers.codex_add_modal_url_label'],
] as const;

describe.each(drawers)('%s base url typing', (_name, Component, urlLabel) => {
  it('survives a long base url without re-entering the render loop', async () => {
    const view = createElement(Component as unknown as ComponentType<Record<string, unknown>>, {
      open: true,
      editIndex: null,
      disabled: false,
      onClose: mocks.noop,
      onSaved: mocks.noop,
    });
    await act(async () => {
      renderer = create(view);
    });
    const mounted = renderer!;
    const urlInput = mounted.root
      .findAllByType('div')
      .filter((node) => node.props.className === 'form-group')
      .map((group) => ({
        group,
        label: group.findAllByType('label')[0],
        input: group.findAllByType('input')[0],
      }))
      .find(({ label }) => String(label?.props.children ?? '').includes(urlLabel))?.input;
    expect(urlInput).toBeDefined();
    const longUrl = 'http://127.0.0.1:19088/v1?pad=' + 'pad'.repeat(450);
    const before = mocks.renders.count;
    for (let length = 1; length <= 40; length += 1) {
      const value = longUrl.slice(0, length);
      await act(async () => {
        urlInput!.props.onChange({ target: { value } });
      });
    }
    // The drawer's own re-render costs 5 passes per keystroke. The pre-fix
    // discovery reset cost 4 more: it replaced the (already empty) result list
    // with a new array, so every keystroke re-ran the effects that read it. The
    // backlog that built up from that is what ended in React error #185.
    const rendersPerKeystroke = (mocks.renders.count - before) / 40;
    expect(rendersPerKeystroke).toBeLessThanOrEqual(6);
    expect(JSON.stringify(mounted.toJSON())).toContain(longUrl.slice(0, 40));
    await act(async () => {
      mounted.unmount();
      renderer = undefined;
    });
  }, 120_000);
});
