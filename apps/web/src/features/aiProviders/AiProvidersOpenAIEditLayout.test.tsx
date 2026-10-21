import { createElement, type ReactElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { OpenAIProviderConfig } from '@/types';
import type { OpenAIFormState } from '@/components/providers/types';

const mocks = vi.hoisted(() => ({
  params: { index: '0' } as Record<string, string | undefined>,
  config: { openaiCompatibility: [] as OpenAIProviderConfig[] },
  getOpenAIProviders: vi.fn(),
  fetchConfig: vi.fn(),
  updateConfigValue: vi.fn(),
  showNotification: vi.fn(),
  navigate: vi.fn(),
  allowNextNavigation: vi.fn(),
  outletContext: { current: null as Record<string, unknown> | null },
  guardShouldBlock: { current: null as ((args: { nextLocation: { pathname: string } }) => boolean) | null },
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock('react-router-dom', () => ({
  Outlet: (props: { context?: unknown }) => {
    mocks.outletContext.current = (props.context ?? null) as Record<string, unknown> | null;
    return null;
  },
  useNavigate: () => mocks.navigate,
  useLocation: () => ({ state: null }),
  useParams: () => mocks.params,
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
    useNotificationStore: () => ({ showNotification: mocks.showNotification }),
    useOpenAIEditDraftStore: draftStore.useOpenAIEditDraftStore,
  };
});

vi.mock('@/services/api', () => ({
  providersApi: {
    getOpenAIProviders: mocks.getOpenAIProviders,
    updateOpenAIProvider: vi.fn(),
    createOpenAIProvider: vi.fn(),
  },
}));

import { AiProvidersOpenAIEditLayout } from './AiProvidersOpenAIEditLayout';
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
});
