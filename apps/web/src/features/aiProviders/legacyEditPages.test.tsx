import { createElement, type ComponentType, type ReactNode } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  PAGE_TRANSITION_LAYER_CONTEXT_VALUES,
  PageTransitionLayerContext,
} from '@/components/common/PageTransitionLayer';
import { registerOpenDrawer, registerOpenModal } from '@/components/ui/overlayEscapeStack';
import { providersApi } from '@/services/api';
import type { ProviderKeyConfig } from '@/types';

const mocks = vi.hoisted(() => {
  const entry = {
    apiKey: 'test-key',
    baseUrl: 'https://provider.test',
    headers: {},
    models: [],
    excludedModels: [],
  };
  return {
    t: (key: string) => key,
    navigate: vi.fn(),
    handleBack: vi.fn(),
    fetchConfig: vi.fn(),
    noop: vi.fn(),
    auth: { apiBase: 'https://a.test', managementKey: 'secret', connectionStatus: 'connected' },
    config: {
      claudeApiKeys: [entry],
      codexApiKeys: [entry],
      geminiApiKeys: [entry],
      vertexApiKeys: [entry],
      openaiCompatibility: [{ ...entry, name: 'test', apiKeyEntries: [{ apiKey: 'test-key' }] }],
    },
    claudeContext: {
      hasIndexParam: true,
      editIndex: 0,
      invalidIndexParam: false,
      invalidIndex: false,
      disableControls: false,
      loading: false,
      saving: false,
      form: {
        ...entry,
        headers: [],
        modelEntries: [{ name: 'claude-sonnet-4', alias: '' }],
        excludedText: '',
        disableCooling: 'inherit' as const,
      },
      setForm: vi.fn(),
      testModel: 'claude-sonnet-4',
      setTestModel: vi.fn(),
      testStatus: 'idle' as const,
      setTestStatus: vi.fn(),
      testMessage: '',
      setTestMessage: vi.fn(),
      availableModels: ['claude-sonnet-4'],
      handleBack: vi.fn(),
      handleSave: vi.fn(),
      mergeDiscoveredModels: vi.fn(),
    },
    openaiContext: {
      hasIndexParam: true,
      editIndex: 0,
      invalidIndexParam: false,
      invalidIndex: false,
      disableControls: false,
      loading: false,
      saving: false,
      form: {
        name: 'openai-test',
        prefix: '',
        baseUrl: 'https://openai.test/v1',
        headers: [],
        apiKeyEntries: [{ apiKey: 'test-key', proxyUrl: '' }],
        modelEntries: [{ name: 'gpt-4o', alias: '' }],
        disableCooling: 'inherit' as const,
      },
      setForm: vi.fn(),
      testModel: 'gpt-4o',
      setTestModel: vi.fn(),
      testStatus: 'idle' as const,
      setTestStatus: vi.fn(),
      testMessage: '',
      setTestMessage: vi.fn(),
      keyTestStatuses: [],
      setDraftKeyTestStatus: vi.fn(),
      setDraftKeyTestStatuses: vi.fn(),
      resetDraftKeyTestStatuses: vi.fn(),
      availableModels: ['gpt-4o'],
      handleBack: vi.fn(),
      handleSave: vi.fn(),
      mergeDiscoveredModels: vi.fn(),
    },
    activeOutletContext: null as unknown,
  };
});

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: mocks.t }) }));
vi.mock('react-router-dom', () => ({
  useNavigate: () => mocks.navigate,
  useParams: () => ({ index: '0' }),
  useLocation: () => ({ pathname: '/ai-providers/test/0', state: null }),
  useOutletContext: () => mocks.activeOutletContext,
}));
vi.mock('@/hooks/useEdgeSwipeBack', () => ({ useEdgeSwipeBack: () => ({ current: null }) }));
vi.mock('@/hooks/useUnsavedChangesGuard', () => ({
  useUnsavedChangesGuard: () => ({ allowNextNavigation: mocks.noop, blocker: null }),
}));
vi.mock('@/components/common/SecondaryScreenShell', () => ({
  SecondaryScreenShell: ({
    children,
    isLoading,
  }: {
    children?: ReactNode;
    isLoading?: boolean;
  }) => <div data-testid="secondary-screen-shell" data-loading={String(Boolean(isLoading))}>{children}</div>,
}));
vi.mock('@/components/ui/Modal', () => ({ Modal: () => null }));
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
  useNotificationStore: () => ({
    showNotification: mocks.noop,
    showConfirmation: mocks.noop,
  }),
}));
vi.mock('@/services/api', () => ({
  modelsApi: {
    fetchClaudeModelsViaApiCall: vi.fn(async () => []),
    fetchV1ModelsViaApiCall: vi.fn(async () => []),
    fetchGeminiModelsViaApiCall: vi.fn(async () => []),
    fetchModelsViaApiCall: vi.fn(async () => []),
    buildClaudeModelsEndpoint: () => 'https://provider.test/v1/models',
    buildV1ModelsEndpoint: () => 'https://provider.test/v1/models',
    buildGeminiModelsEndpoint: () => 'https://provider.test/v1beta/models',
  },
  providersApi: {
    getVertexConfigs: vi.fn(async () => mocks.config.vertexApiKeys),
    getGeminiKeys: vi.fn(async () => mocks.config.geminiApiKeys),
    getCodexConfigs: vi.fn(async () => mocks.config.codexApiKeys),
  },
  apiCallApi: { request: vi.fn(async () => ({ statusCode: 200 })) },
  getApiCallErrorDetails: () => '',
  getApiCallErrorMessage: () => '',
}));

import { AiProvidersClaudeEditPage } from './AiProvidersClaudeEditPage';
import { AiProvidersClaudeModelsPage } from './AiProvidersClaudeModelsPage';
import { AiProvidersCodexEditPage } from './AiProvidersCodexEditPage';
import { AiProvidersGeminiEditPage } from './AiProvidersGeminiEditPage';
import { AiProvidersOpenAIEditPage } from './AiProvidersOpenAIEditPage';
import { AiProvidersOpenAIModelsPage } from './AiProvidersOpenAIModelsPage';
import { AiProvidersVertexEditPage } from './AiProvidersVertexEditPage';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe('Legacy standalone AI provider pages Escape guard and mount freshness', () => {
  let renderer: ReactTestRenderer | undefined;
  let keydownListeners: Set<EventListenerOrEventListenerObject>;

  const dispatchWindowEscape = (initialDefaultPrevented = false) => {
    let prevented = initialDefaultPrevented;
    const preventDefault = vi.fn(() => {
      prevented = true;
    });
    const event = {
      key: 'Escape',
      get defaultPrevented() {
        return prevented;
      },
      preventDefault,
    } as unknown as KeyboardEvent;
    for (const listener of Array.from(keydownListeners)) {
      if (typeof listener === 'function') {
        listener(event);
      } else {
        listener.handleEvent(event);
      }
    }
    return event;
  };

  beforeEach(() => {
    vi.clearAllMocks();
    keydownListeners = new Set();
    vi.stubGlobal('window', {
      addEventListener: (type: string, listener: EventListenerOrEventListenerObject) => {
        if (type === 'keydown') keydownListeners.add(listener);
      },
      removeEventListener: (type: string, listener: EventListenerOrEventListenerObject) => {
        if (type === 'keydown') keydownListeners.delete(listener);
      },
    });
    mocks.fetchConfig.mockResolvedValue(mocks.config.codexApiKeys);
  });

  afterEach(async () => {
    if (renderer) {
      await act(async () => {
        renderer?.unmount();
      });
      renderer = undefined;
    }
    vi.unstubAllGlobals();
  });

  const legacyPageCases: Array<{
    name: string;
    Component: ComponentType;
    outletContext?: 'claude' | 'openai';
    expectBackSpy: () => ReturnType<typeof vi.fn>;
  }> = [
    {
      name: 'AiProvidersCodexEditPage',
      Component: AiProvidersCodexEditPage,
      expectBackSpy: () => mocks.navigate,
    },
    {
      name: 'AiProvidersGeminiEditPage',
      Component: AiProvidersGeminiEditPage,
      expectBackSpy: () => mocks.navigate,
    },
    {
      name: 'AiProvidersVertexEditPage',
      Component: AiProvidersVertexEditPage,
      expectBackSpy: () => mocks.navigate,
    },
    {
      name: 'AiProvidersClaudeEditPage',
      Component: AiProvidersClaudeEditPage,
      outletContext: 'claude',
      expectBackSpy: () => mocks.claudeContext.handleBack,
    },
    {
      name: 'AiProvidersOpenAIEditPage',
      Component: AiProvidersOpenAIEditPage,
      outletContext: 'openai',
      expectBackSpy: () => mocks.openaiContext.handleBack,
    },
    {
      name: 'AiProvidersClaudeModelsPage',
      Component: AiProvidersClaudeModelsPage,
      outletContext: 'claude',
      expectBackSpy: () => mocks.navigate,
    },
    {
      name: 'AiProvidersOpenAIModelsPage',
      Component: AiProvidersOpenAIModelsPage,
      outletContext: 'openai',
      expectBackSpy: () => mocks.navigate,
    },
  ];

  it.each(legacyPageCases)(
    '$name ignores Escape when a Modal/Drawer is open or event is defaultPrevented, and navigates back when clear',
    async ({ Component, outletContext, expectBackSpy }) => {
      mocks.activeOutletContext =
        outletContext === 'claude'
          ? mocks.claudeContext
          : outletContext === 'openai'
            ? mocks.openaiContext
            : null;

      await act(async () => {
        renderer = create(createElement(Component));
        await Promise.resolve();
        await Promise.resolve();
      });

      const backSpy = expectBackSpy();
      expect(backSpy).not.toHaveBeenCalled();

      // Open Modal blocks window Escape back-navigation
      const unregisterModal = registerOpenModal();
      try {
        dispatchWindowEscape();
        expect(backSpy).not.toHaveBeenCalled();
      } finally {
        unregisterModal();
      }

      // Open Drawer blocks window Escape back-navigation
      const unregisterDrawer = registerOpenDrawer();
      try {
        dispatchWindowEscape();
        expect(backSpy).not.toHaveBeenCalled();
      } finally {
        unregisterDrawer();
      }

      // defaultPrevented blocks window Escape back-navigation
      dispatchWindowEscape(true);
      expect(backSpy).not.toHaveBeenCalled();

      // With no overlay open and not prevented, Escape navigates back
      dispatchWindowEscape(false);
      expect(backSpy).toHaveBeenCalledTimes(1);
    }
  );

  it('force-refreshes codex-api-key and gemini-api-key on standalone edit page mount', async () => {
    await act(async () => {
      renderer = create(createElement(AiProvidersCodexEditPage));
      await Promise.resolve();
    });
    expect(mocks.fetchConfig).toHaveBeenCalledWith('codex-api-key', true);

    await act(async () => {
      renderer?.unmount();
    });
    renderer = undefined;
    mocks.fetchConfig.mockClear();

    await act(async () => {
      renderer = create(createElement(AiProvidersGeminiEditPage));
      await Promise.resolve();
    });
    expect(mocks.fetchConfig).toHaveBeenCalledWith('gemini-api-key', true);
  });

  it('invokes only the foreground models page handleBack on a single Escape when stacked over an edit page', async () => {
    mocks.activeOutletContext = mocks.claudeContext;

    await act(async () => {
      renderer = create(
        <>
          <PageTransitionLayerContext.Provider
            value={PAGE_TRANSITION_LAYER_CONTEXT_VALUES.stacked}
          >
            <AiProvidersClaudeEditPage />
          </PageTransitionLayerContext.Provider>
          <PageTransitionLayerContext.Provider
            value={PAGE_TRANSITION_LAYER_CONTEXT_VALUES.current}
          >
            <AiProvidersClaudeModelsPage />
          </PageTransitionLayerContext.Provider>
        </>
      );
      await Promise.resolve();
      await Promise.resolve();
    });

    const claudeEvent = dispatchWindowEscape(false);
    expect(claudeEvent.preventDefault).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledWith(-1);
    expect(mocks.claudeContext.handleBack).not.toHaveBeenCalled();

    await act(async () => {
      renderer?.unmount();
    });
    renderer = undefined;
    vi.clearAllMocks();

    mocks.activeOutletContext = mocks.openaiContext;
    await act(async () => {
      renderer = create(
        <>
          <PageTransitionLayerContext.Provider
            value={PAGE_TRANSITION_LAYER_CONTEXT_VALUES.stacked}
          >
            <AiProvidersOpenAIEditPage />
          </PageTransitionLayerContext.Provider>
          <PageTransitionLayerContext.Provider
            value={PAGE_TRANSITION_LAYER_CONTEXT_VALUES.current}
          >
            <AiProvidersOpenAIModelsPage />
          </PageTransitionLayerContext.Provider>
        </>
      );
      await Promise.resolve();
      await Promise.resolve();
    });

    const openaiEvent = dispatchWindowEscape(false);
    expect(openaiEvent.preventDefault).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledTimes(1);
    expect(mocks.navigate).toHaveBeenCalledWith(-1);
    expect(mocks.openaiContext.handleBack).not.toHaveBeenCalled();
  });

  it.each([
    {
      name: 'AiProvidersCodexEditPage',
      Component: AiProvidersCodexEditPage,
      section: 'codex-api-key',
    },
    {
      name: 'AiProvidersGeminiEditPage',
      Component: AiProvidersGeminiEditPage,
      section: 'gemini-api-key',
    },
  ])(
    '$name keeps loading true during mount refresh when cache is valid and does not re-fetch or clobber edits on language change',
    async ({ Component, section }) => {
      let resolveFetch!: (value: unknown) => void;
      mocks.fetchConfig.mockImplementation(
        () =>
          new Promise((resolve) => {
            resolveFetch = resolve;
          })
      );

      await act(async () => {
        renderer = create(createElement(Component));
      });

      const shell = renderer!.root.findByProps({ 'data-testid': 'secondary-screen-shell' });
      expect(shell.props['data-loading']).toBe('true');
      expect(mocks.fetchConfig).toHaveBeenCalledTimes(1);
      expect(mocks.fetchConfig).toHaveBeenCalledWith(section, true);

      await act(async () => {
        resolveFetch([
          {
            apiKey: 'fresh-server-key',
            baseUrl: 'https://fresh.provider.test',
            headers: {},
            models: [],
            excludedModels: [],
          },
        ]);
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(
        renderer!.root.findByProps({ 'data-testid': 'secondary-screen-shell' }).props[
          'data-loading'
        ]
      ).toBe('false');

      const apiKeyInput = renderer!.root
        .findAllByType('input')
        .find((node) => node.props.value === 'fresh-server-key');
      expect(apiKeyInput).toBeDefined();

      // Simulate user editing the API key, then language (`t` reference) changing
      await act(async () => {
        apiKeyInput!.props.onChange({ target: { value: 'dirty-user-key' } });
      });

      const previousT = mocks.t;
      try {
        mocks.t = (key: string) => `alt:${key}`;
        await act(async () => {
          renderer!.update(createElement(Component));
          await Promise.resolve();
        });
      } finally {
        mocks.t = previousT;
      }

      expect(mocks.fetchConfig).toHaveBeenCalledTimes(1);
      const afterLangChangeInput = renderer!.root
        .findAllByType('input')
        .find((node) => node.props.value === 'dirty-user-key');
      expect(afterLangChangeInput).toBeDefined();
    }
  );

  it('AiProvidersVertexEditPage keeps loading true during mount refresh when cache is valid and does not re-fetch or clobber edits on language change', async () => {
    let resolveConfig!: (value: unknown) => void;
    let resolveVertex!: (value: ProviderKeyConfig[]) => void;
    mocks.fetchConfig.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveConfig = resolve;
        })
    );
    vi.mocked(providersApi.getVertexConfigs).mockImplementation(
      () =>
        new Promise<ProviderKeyConfig[]>((resolve) => {
          resolveVertex = resolve;
        })
    );
    await act(async () => {
      renderer = create(createElement(AiProvidersVertexEditPage));
    });

    expect(
      renderer!.root.findByProps({ 'data-testid': 'secondary-screen-shell' }).props['data-loading']
    ).toBe('true');
    expect(mocks.fetchConfig).toHaveBeenCalledTimes(1);

    const freshVertex = [
      {
        apiKey: 'fresh-vertex-key',
        baseUrl: 'https://fresh.vertex.test',
        headers: {},
        models: [],
        excludedModels: [],
      },
    ];
    await act(async () => {
      resolveConfig(freshVertex);
      resolveVertex(freshVertex);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(
      renderer!.root.findByProps({ 'data-testid': 'secondary-screen-shell' }).props['data-loading']
    ).toBe('false');

    const apiKeyInput = renderer!.root
      .findAllByType('input')
      .find((node) => node.props.value === 'fresh-vertex-key');
    expect(apiKeyInput).toBeDefined();

    await act(async () => {
      apiKeyInput!.props.onChange({ target: { value: 'dirty-vertex-key' } });
    });

    const previousT = mocks.t;
    try {
      mocks.t = (key: string) => `alt:${key}`;
      await act(async () => {
        renderer!.update(createElement(AiProvidersVertexEditPage));
        await Promise.resolve();
      });
    } finally {
      mocks.t = previousT;
    }

    expect(mocks.fetchConfig).toHaveBeenCalledTimes(1);
    expect(
      renderer!.root
        .findAllByType('input')
        .find((node) => node.props.value === 'dirty-vertex-key')
    ).toBeDefined();
  });

  it('AiProvidersClaudeEditPage preserves raw trailing newline and comma in cloak sensitiveWords textarea', async () => {
    let currentForm = {
      ...mocks.claudeContext.form,
      cloak: {
        mode: 'auto',
        strictMode: false,
        sensitiveWords: ['alpha'] as string[] | undefined,
      },
    };
    const updateForm = (
      action:
        | typeof currentForm
        | ((prev: typeof currentForm) => typeof currentForm)
    ) => {
      currentForm = typeof action === 'function' ? action(currentForm) : action;
      mocks.activeOutletContext = {
        ...mocks.claudeContext,
        form: currentForm,
        setForm: updateForm,
      };
    };
    mocks.activeOutletContext = {
      ...mocks.claudeContext,
      form: currentForm,
      setForm: updateForm,
    };

    await act(async () => {
      renderer = create(createElement(AiProvidersClaudeEditPage));
    });

    const findSensitiveTextarea = () =>
      renderer!.root
        .findAllByType('textarea')
        .find(
          (node) =>
            node.props.placeholder === 'ai_providers.claude_cloak_sensitive_words_placeholder'
        )!;

    expect(findSensitiveTextarea().props.value).toBe('alpha');

    // Typing a trailing newline keeps raw newline in textarea while parsed words stay ['alpha']
    await act(async () => {
      findSensitiveTextarea().props.onChange({ target: { value: 'alpha\n' } });
      renderer!.update(createElement(AiProvidersClaudeEditPage));
    });
    expect(currentForm.cloak.sensitiveWords).toEqual(['alpha']);
    expect(findSensitiveTextarea().props.value).toBe('alpha\n');

    // Typing a trailing comma keeps raw comma in textarea
    await act(async () => {
      findSensitiveTextarea().props.onChange({ target: { value: 'alpha, ' } });
      renderer!.update(createElement(AiProvidersClaudeEditPage));
    });
    expect(currentForm.cloak.sensitiveWords).toEqual(['alpha']);
    expect(findSensitiveTextarea().props.value).toBe('alpha, ');

    // External update to form.cloak.sensitiveWords syncs into the textarea
    currentForm = {
      ...currentForm,
      cloak: { ...currentForm.cloak, sensitiveWords: ['gamma', 'delta'] },
    };
    mocks.activeOutletContext = {
      ...mocks.claudeContext,
      form: currentForm,
      setForm: updateForm,
    };
    await act(async () => {
      renderer!.update(createElement(AiProvidersClaudeEditPage));
    });
    expect(findSensitiveTextarea().props.value).toBe('gamma\ndelta');
  });
});
