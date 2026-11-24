import { createElement, type ReactElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { OpenAIProviderConfig } from '@/types';
import type { OpenAIFormState } from '@/components/providers/types';
import { normalizeOpenAIProvider } from '@/services/api/transformers';
import { providersApi as realProvidersApi } from '@/services/api/providers';

const mocks = vi.hoisted(() => ({
  params: { index: '0' } as Record<string, string | undefined>,
  config: { openaiCompatibility: [] as OpenAIProviderConfig[] },
  getOpenAIProviders: vi.fn(),
  updateOpenAIProvider: vi.fn(),
  clientGet: vi.fn(),
  clientPut: vi.fn(),
  renderPage: false,
  fetchConfig: vi.fn(),
  updateConfigValue: vi.fn(),
  showNotification: vi.fn(),
  showConfirmation: vi.fn(),
  navigate: vi.fn(),
  allowNextNavigation: vi.fn(),
  outletContext: { current: null as Record<string, unknown> | null },
  guardShouldBlock: {
    current: null as ((args: { nextLocation: { pathname: string } }) => boolean) | null,
  },
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock('react-router-dom', () => ({
  Outlet: (props: { context?: unknown }) => {
    mocks.outletContext.current = (props.context ?? null) as Record<string, unknown> | null;
    return mocks.renderPage ? createElement(AiProvidersOpenAIEditPage) : null;
  },
  useNavigate: () => mocks.navigate,
  useLocation: () => ({ state: null }),
  useParams: () => mocks.params,
  useOutletContext: () => mocks.outletContext.current,
}));

vi.mock('@/hooks/useUnsavedChangesGuard', () => ({
  useUnsavedChangesGuard: (options: {
    shouldBlock: (args: { nextLocation: { pathname: string } }) => boolean;
  }) => {
    mocks.guardShouldBlock.current = options.shouldBlock;
    return { allowNextNavigation: mocks.allowNextNavigation };
  },
}));

vi.mock('@/stores', async () => {
  const draftStore = await import('@/stores/useOpenAIEditDraftStore');
  return {
    useAuthStore: (selector: (state: { connectionStatus: string }) => unknown) =>
      selector({ connectionStatus: 'connected' }),
    useConfigStore: (selector: (state: Record<string, unknown>) => unknown) =>
      selector({
        config: mocks.config,
        fetchConfig: mocks.fetchConfig,
        updateConfigValue: mocks.updateConfigValue,
        isCacheValid: () => true,
      }),
    useNotificationStore: () => ({
    showNotification: mocks.showNotification,
    showConfirmation: mocks.showConfirmation,
  }),
    useOpenAIEditDraftStore: draftStore.useOpenAIEditDraftStore,
  };
});

vi.mock('@/services/api', () => ({
  providersApi: {
    getOpenAIProviders: mocks.getOpenAIProviders,
    updateOpenAIProvider: mocks.updateOpenAIProvider,
    createOpenAIProvider: vi.fn(),
  },
}));

vi.mock('@/services/api/client', () => ({
  apiClient: { get: mocks.clientGet, put: mocks.clientPut },
}));

vi.mock('@/hooks/useEdgeSwipeBack', () => ({
  useEdgeSwipeBack: () => ({ current: null }),
}));

vi.mock('@/components/common/SecondaryScreenShell', () => ({
  SecondaryScreenShell: ({
    children,
    floatingAction,
  }: {
    children: ReactElement;
    floatingAction: ReactElement;
  }) => createElement('div', null, children, floatingAction),
}));
import { AiProvidersOpenAIEditLayout } from './AiProvidersOpenAIEditLayout';
import { AiProvidersOpenAIEditPage } from './AiProvidersOpenAIEditPage';
import { useOpenAIEditDraftStore } from '@/stores/useOpenAIEditDraftStore';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type EditorContext = {
  form: OpenAIFormState;
  setForm: (action: (prev: OpenAIFormState) => OpenAIFormState) => void;
};

const flush = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
};

const renderEditor = async () => {
  let renderer: ReactTestRenderer | undefined;
  await act(async () => {
    renderer = create(createElement(AiProvidersOpenAIEditLayout) as ReactElement);
  });
  await flush();
  return renderer!;
};

const patchFirstEntry = async (patch: Record<string, unknown>) => {
  const context = mocks.outletContext.current as unknown as EditorContext | null;
  if (!context) throw new Error('editor context was not captured');
  await act(async () => {
    context.setForm((prev) => ({
      ...prev,
      apiKeyEntries: prev.apiKeyEntries.map((entry, index) =>
        index === 0 ? { ...entry, ...patch } : entry
      ),
    }));
  });
  await flush();
};

const isBlockedLeavingEditor = () => {
  const shouldBlock = mocks.guardShouldBlock.current;
  if (!shouldBlock) throw new Error('unsaved changes guard was not captured');
  return shouldBlock({ nextLocation: { pathname: '/ai-providers' } });
};

describe('AiProvidersOpenAIEditLayout dirty tracking', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.renderPage = false;
    vi.stubGlobal('window', { addEventListener: vi.fn(), removeEventListener: vi.fn() });
    useOpenAIEditDraftStore.setState({ drafts: {}, refCounts: {} });
    mocks.params = { index: '0' };
    const provider: OpenAIProviderConfig = {
      name: 'runtime-indexed',
      baseUrl: 'https://model.example/v1',
      apiKeyEntries: [{ apiKey: 'original-key', authIndex: 'server-runtime-index' }],
      models: [{ name: 'local-model' }],
    };
    mocks.config = { openaiCompatibility: [provider] };
    mocks.getOpenAIProviders.mockResolvedValue([provider]);
    mocks.fetchConfig.mockResolvedValue([provider]);
    mocks.outletContext.current = null;
    mocks.guardShouldBlock.current = null;
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it.each([
    { weight: 0, mixed: false },
    { weight: 2, mixed: false },
    { weight: 0, mixed: true },
    { weight: 2, mixed: true },
  ])(
    'drops the cleared keyed entry from standalone page PUT with weight=$weight and mixed=$mixed',
    async ({ weight, mixed }) => {
      const raw = {
        name: 'clear-key',
        'base-url': 'http://upstream/v1',
        'api-key-entries': [
          { 'api-key': 'old-key', 'proxy-url': 'http://proxy', weight },
          ...(mixed
            ? [{ 'api-key': '', 'connection-pool': { name: 'keyless' } }, { 'api-key': 'keep-key' }]
            : []),
        ],
      };
      const provider = normalizeOpenAIProvider(raw)!;
      mocks.config = { openaiCompatibility: [provider] };
      mocks.getOpenAIProviders.mockResolvedValue([provider]);
      mocks.clientGet.mockResolvedValue({ 'openai-compatibility': [raw] });
      mocks.clientPut.mockResolvedValue({});
      mocks.updateOpenAIProvider.mockImplementationOnce(realProvidersApi.updateOpenAIProvider);
      mocks.renderPage = true;
      const renderer = await renderEditor();
      const input = renderer.root
        .findAllByType('input')
        .find((node) => node.props.placeholder === 'ai_providers.openai_key_placeholder')!;
      act(() => input.props.onChange({ target: { value: '' } }));
      const save = renderer.root
        .findAllByType('button')
        .find((node) =>
          node.findAllByType('span').some((span) => span.children.join('') === 'common.save')
        )!;
      expect(save.props.disabled).toBe(false);
      await act(async () => {
        await save.props.onClick();
      });
      const [, saved] = mocks.clientPut.mock.calls[0];
      expect(saved[0]['api-key-entries']).toEqual(
        mixed
          ? [{ 'api-key': '', 'connection-pool': { name: 'keyless' } }, { 'api-key': 'keep-key' }]
          : []
      );
      act(() => renderer.unmount());
    }
  );
  it('does not stay dirty when a key is changed and restored after its runtime auth-index is cleared', async () => {
    const renderer = await renderEditor();

    const initialContext = mocks.outletContext.current as unknown as EditorContext;
    expect(initialContext.form.apiKeyEntries[0]).toMatchObject({
      apiKey: 'original-key',
      authIndex: 'server-runtime-index',
    });
    expect(isBlockedLeavingEditor()).toBe(false);

    await patchFirstEntry({ apiKey: 'changed-key', authIndex: '' });
    expect(isBlockedLeavingEditor()).toBe(true);

    await patchFirstEntry({ apiKey: 'original-key', authIndex: '' });
    expect(isBlockedLeavingEditor()).toBe(false);

    act(() => renderer.unmount());
  });

  it('keeps loading true until mount refresh settles when cache is valid and populates fresh data without re-fetching on language change', async () => {
    const staleProvider: OpenAIProviderConfig = {
      name: 'stale-provider',
      baseUrl: 'https://stale.example/v1',
      apiKeyEntries: [{ apiKey: 'stale-key' }],
      models: [{ name: 'gpt-4o-mini' }],
    };
    const freshProvider: OpenAIProviderConfig = {
      name: 'fresh-provider',
      baseUrl: 'https://fresh.example/v1',
      apiKeyEntries: [{ apiKey: 'fresh-key' }],
      models: [{ name: 'gpt-4.1' }],
    };
    mocks.config = { openaiCompatibility: [staleProvider] };

    let resolveFetch!: (value: OpenAIProviderConfig[]) => void;
    mocks.getOpenAIProviders.mockImplementation(
      () =>
        new Promise<OpenAIProviderConfig[]>((resolve) => {
          resolveFetch = resolve;
        })
    );

    let renderer: ReactTestRenderer | undefined;
    await act(async () => {
      renderer = create(createElement(AiProvidersOpenAIEditLayout) as ReactElement);
    });

    const loadingContext = mocks.outletContext.current as unknown as
      | (EditorContext & { loading: boolean })
      | null;
    expect(loadingContext?.loading).toBe(true);
    expect(mocks.getOpenAIProviders).toHaveBeenCalledTimes(1);

    await act(async () => {
      resolveFetch([freshProvider]);
    });
    await flush();

    const loadedContext = mocks.outletContext.current as unknown as
      | (EditorContext & { loading: boolean })
      | null;
    expect(loadedContext?.loading).toBe(false);
    expect(loadedContext?.form.name).toBe('fresh-provider');
    expect(loadedContext?.form.baseUrl).toBe('https://fresh.example/v1');
    expect(loadedContext?.form.apiKeyEntries[0]?.apiKey).toBe('fresh-key');

    // Editing and re-rendering with a new `t` function reference must not re-fetch or clobber edits
    await patchFirstEntry({ apiKey: 'user-edited-key' });
    await act(async () => {
      renderer!.update(createElement(AiProvidersOpenAIEditLayout) as ReactElement);
    });
    await flush();

    expect(mocks.getOpenAIProviders).toHaveBeenCalledTimes(1);
    expect(
      (mocks.outletContext.current as unknown as EditorContext | null)?.form.apiKeyEntries[0]
        ?.apiKey
    ).toBe('user-edited-key');

    act(() => renderer!.unmount());
  });
});
