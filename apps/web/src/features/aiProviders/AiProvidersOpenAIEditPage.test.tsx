import { createElement, type ReactNode } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { OpenAIFormState } from '@/components/providers/types';

const mocks = vi.hoisted(() => ({
  context: null as unknown,
  setForm: vi.fn(),
  setTestModel: vi.fn(),
  setTestStatus: vi.fn(),
  setTestMessage: vi.fn(),
  setDraftKeyTestStatus: vi.fn(),
  setDraftKeyTestStatuses: vi.fn(),
  resetDraftKeyTestStatuses: vi.fn(),
  handleBack: vi.fn(),
  handleSave: vi.fn(),
  mergeDiscoveredModels: vi.fn(),
  navigate: vi.fn(),
  showNotification: vi.fn(),
  apiCallRequest: vi.fn(),
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock('react-router-dom', () => ({
  useNavigate: () => mocks.navigate,
  useOutletContext: () => mocks.context,
}));

vi.mock('@/hooks/useEdgeSwipeBack', () => ({
  useEdgeSwipeBack: () => ({ current: null }),
}));

vi.mock('@/stores', () => ({
  useNotificationStore: () => ({ showNotification: mocks.showNotification }),
}));

vi.mock('@/services/api', () => ({
  apiCallApi: { request: mocks.apiCallRequest },
  getApiCallErrorDetails: () => '',
}));

vi.mock('@/components/common/SecondaryScreenShell', () => ({
  SecondaryScreenShell: ({
    children,
    floatingAction,
  }: {
    children: ReactNode;
    floatingAction?: ReactNode;
  }) => createElement('div', null, children, floatingAction),
}));

vi.mock('@/components/ui/Button', () => ({
  Button: ({
    children,
    onClick,
    disabled,
    ...props
  }: {
    children?: ReactNode;
    onClick?: () => void;
    disabled?: boolean;
  }) => createElement('button', { ...props, onClick, disabled }, children),
}));

vi.mock('@/components/ui/Card', () => ({
  Card: ({ children }: { children: ReactNode }) => createElement('div', null, children),
}));

vi.mock('@/components/ui/Input', () => ({
  Input: () => null,
}));

vi.mock('@/components/ui/HeaderInputList', () => ({
  HeaderInputList: () => null,
}));

vi.mock('@/components/ui/ModelInputList', () => ({
  ModelInputList: () => null,
}));

vi.mock('@/components/ui/InfoTooltip', () => ({
  InfoTooltip: () => null,
}));

vi.mock('@/components/ui/Select', () => ({
  Select: () => null,
}));

vi.mock('@/components/providers/CoolingPolicySelect', () => ({
  CoolingPolicySelect: () => null,
}));

vi.mock('@/components/providers', () => ({
  OpenAIKeyTestStatusIndicator: () => null,
}));

import { AiProvidersOpenAIEditPage } from './AiProvidersOpenAIEditPage';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type EditorContext = {
  form: OpenAIFormState;
  setForm: typeof mocks.setForm;
  testModel: string;
  setTestModel: typeof mocks.setTestModel;
  testStatus: 'idle' | 'loading' | 'success' | 'error';
  setTestStatus: typeof mocks.setTestStatus;
  testMessage: string;
  setTestMessage: typeof mocks.setTestMessage;
  keyTestStatuses: Array<{ status: 'idle' | 'loading' | 'success' | 'error'; message: string }>;
  setDraftKeyTestStatus: typeof mocks.setDraftKeyTestStatus;
  setDraftKeyTestStatuses: typeof mocks.setDraftKeyTestStatuses;
  resetDraftKeyTestStatuses: typeof mocks.resetDraftKeyTestStatuses;
  availableModels: string[];
  hasIndexParam: boolean;
  editIndex: number | null;
  invalidIndexParam: boolean;
  invalidIndex: boolean;
  disableControls: boolean;
  loading: boolean;
  saving: boolean;
  handleBack: typeof mocks.handleBack;
  handleSave: typeof mocks.handleSave;
  mergeDiscoveredModels: typeof mocks.mergeDiscoveredModels;
};

const buildContext = (
  apiKeyEntries: OpenAIFormState['apiKeyEntries']
): EditorContext => ({
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
    baseUrl: 'https://model.example/v1',
    headers: [],
    apiKeyEntries,
    modelEntries: [{ name: 'local-model', alias: '' }],
    disableCooling: 'inherit',
  },
  setForm: mocks.setForm,
  testModel: 'local-model',
  setTestModel: mocks.setTestModel,
  testStatus: 'idle',
  setTestStatus: mocks.setTestStatus,
  testMessage: '',
  setTestMessage: mocks.setTestMessage,
  keyTestStatuses: [],
  setDraftKeyTestStatus: mocks.setDraftKeyTestStatus,
  setDraftKeyTestStatuses: mocks.setDraftKeyTestStatuses,
  resetDraftKeyTestStatuses: mocks.resetDraftKeyTestStatuses,
  availableModels: ['local-model'],
  handleBack: mocks.handleBack,
  handleSave: mocks.handleSave,
  mergeDiscoveredModels: mocks.mergeDiscoveredModels,
});

const renderPage = () => {
  let renderer!: ReactTestRenderer;
  act(() => {
    renderer = create(createElement(AiProvidersOpenAIEditPage));
  });
  return renderer;
};

const findKeyInput = (renderer: ReactTestRenderer) =>
  renderer.root.findAllByType('input').find(
    (input) => input.props.placeholder === 'ai_providers.openai_key_placeholder'
  );

const findActionButton = (renderer: ReactTestRenderer, label: string) =>
  renderer.root.findAllByType('button').find(
    (button) => button.children.join('') === label
  );

const findSingleTestButton = (renderer: ReactTestRenderer) =>
  findActionButton(renderer, 'ai_providers.openai_test_single_action');

const findTestAllButton = (renderer: ReactTestRenderer) =>
  findActionButton(renderer, 'ai_providers.openai_test_all_action');

const editKeyAndRerender = (
  renderer: ReactTestRenderer,
  value: string
) => {
  const input = findKeyInput(renderer);
  if (!input) throw new Error('API key input not found');
  act(() => {
    input.props.onChange({ target: { value } });
  });
  act(() => {
    renderer.update(createElement(AiProvidersOpenAIEditPage));
  });
};

describe('AiProvidersOpenAIEditPage keyless connectivity', () => {
  beforeEach(() => {
    vi.stubGlobal('window', {
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    vi.clearAllMocks();
    mocks.apiCallRequest.mockResolvedValue({ statusCode: 200, body: '{}' });
    mocks.context = buildContext([{ apiKey: '' }]);
    mocks.setForm.mockImplementation((action) => {
      const context = mocks.context as EditorContext;
      context.form =
        typeof action === 'function' ? action(context.form) : action;
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('tests a pure keyless provider without Authorization or auth-index', async () => {
    const renderer = renderPage();
    const button = findSingleTestButton(renderer);
    expect(button).toBeDefined();
    expect(button?.props.disabled).not.toBe(true);

    await act(async () => {
      await button?.props.onClick();
    });

    expect(mocks.apiCallRequest).toHaveBeenCalledWith(
      expect.objectContaining({
        authIndex: undefined,
        proxyUrl: undefined,
        header: { 'Content-Type': 'application/json' },
      }),
      expect.anything()
    );
    mocks.apiCallRequest.mockClear();
    const testAllButton = findTestAllButton(renderer);
    expect(testAllButton).toBeDefined();
    expect(testAllButton?.props.disabled).not.toBe(true);
    await act(async () => {
      await testAllButton?.props.onClick();
    });
    expect(mocks.apiCallRequest).toHaveBeenCalledWith(
      expect.objectContaining({
        authIndex: undefined,
        proxyUrl: undefined,
        header: { 'Content-Type': 'application/json' },
      }),
      expect.anything()
    );

    act(() => renderer.unmount());
  });

  it.each([
    ['keyless credential', '', 'old-keyless-index'],
    ['keyed credential', 'old-key', 'old-key-index'],
  ])(
    'uses the newly entered literal key after editing a %s',
    async (_label, apiKey, authIndex) => {
      mocks.context = buildContext([
        {
          apiKey,
          authIndex,
          proxyUrl: 'socks5://proxy.example:1080',
        },
      ]);
      const renderer = renderPage();

      editKeyAndRerender(renderer, 'new-real-key');

      const context = mocks.context as EditorContext;
      expect(context.form.apiKeyEntries[0]).toMatchObject({
        apiKey: 'new-real-key',
        authIndex: '',
      });

      const button = findSingleTestButton(renderer);
      await act(async () => {
        await button?.props.onClick();
      });

      expect(mocks.apiCallRequest).toHaveBeenCalledWith(
        expect.objectContaining({
          authIndex: undefined,
          proxyUrl: 'socks5://proxy.example:1080',
          header: {
            'Content-Type': 'application/json',
            Authorization: 'Bearer new-real-key',
          },
        }),
        expect.anything()
      );
      act(() => renderer.unmount());
    }
  );
});
