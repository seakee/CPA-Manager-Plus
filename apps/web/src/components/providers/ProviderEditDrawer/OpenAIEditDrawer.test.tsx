import { createElement, type ReactNode } from 'react';
import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { CoolingPolicySelect } from '@/components/providers/CoolingPolicySelect';
import { ModelInputList } from '@/components/ui/ModelInputList';
import { hasModelThinkingLevelsClearMarker, hasModelThinkingLevelsEditMarker } from '@/types';

const authState = vi.hoisted(() => ({
  serverVersion: 'v7.2.93' as string | null,
  serverCommit: null as string | null,
}));

vi.mock('@/stores/useAuthStore', () => ({
  useAuthStore: (selector: (state: typeof authState) => unknown) => selector(authState),
}));

const mocks = vi.hoisted(() => ({
  getOpenAIProviders: vi.fn(),
  fetchModelsViaApiCall: vi.fn(),
  updateConfigValue: vi.fn(),
  showNotification: vi.fn(),
  updateOpenAIProvider: vi.fn(),
  createOpenAIProvider: vi.fn(),
}));

vi.mock('@/stores', () => ({
  useConfigStore: (selector: (state: unknown) => unknown) =>
    selector({
      config: { openaiCompatibility: [] },
      updateConfigValue: mocks.updateConfigValue,
    }),
  useNotificationStore: () => ({ showNotification: mocks.showNotification }),
}));

vi.mock('@/components/ui/Drawer', () => ({
  Drawer: ({
    open,
    children,
    footer,
  }: {
    open: boolean;
    children: ReactNode;
    footer?: ReactNode;
  }) => (open ? createElement('div', null, children, footer) : null),
}));

vi.mock('@/components/ui/Modal', () => ({
  Modal: ({
    open,
    children,
    footer,
  }: {
    open: boolean;
    children: ReactNode;
    footer?: ReactNode;
  }) => (open ? createElement('div', null, children, footer) : null),
}));

vi.mock('@/services/api', () => ({
  apiCallApi: { request: vi.fn() },
  getApiCallErrorDetails: vi.fn(() => ''),
  modelsApi: { fetchModelsViaApiCall: mocks.fetchModelsViaApiCall },
  providersApi: {
    createOpenAIProvider: mocks.createOpenAIProvider,
    getOpenAIProviders: mocks.getOpenAIProviders,
    updateOpenAIProvider: mocks.updateOpenAIProvider,
  },
}));

import { OpenAIEditDrawer } from './OpenAIEditDrawer';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const findModelsFetchButton = (root: ReactTestInstance) =>
  root
    .findAllByType('button')
    .find((button) =>
      button.findAllByType('span').some((span) => span.children.join('').includes('/models'))
    );

const findSaveButton = (root: ReactTestInstance) =>
  root
    .findAllByType('button')
    .filter((button) => String(button.props.className ?? '').includes('btn-primary'))
    .slice(-1)[0];

const findInputByLabelSuffix = (root: ReactTestInstance, suffix: string) =>
  root
    .findAllByType('input')
    .find((input) => String(input.props['aria-label'] ?? '').endsWith(suffix));

describe('OpenAIEditDrawer model discovery', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    authState.serverVersion = 'v7.2.93';
    mocks.fetchModelsViaApiCall.mockResolvedValue([]);
    mocks.updateOpenAIProvider.mockResolvedValue(undefined);
    mocks.createOpenAIProvider.mockResolvedValue(undefined);
  });

  it('uses the proxy from the first valid credential when an earlier row is empty', async () => {
    mocks.getOpenAIProviders.mockResolvedValueOnce([
      {
        name: 'openai-example',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [
          { apiKey: '' },
          {
            apiKey: 'second-key',
            authIndex: 'auth-second',
            proxyUrl: 'socks5://proxy.example:1080',
          },
        ],
        models: [],
      },
    ]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <OpenAIEditDrawer open editIndex={0} disabled={false} onClose={vi.fn()} onSaved={vi.fn()} />
      );
    });

    const fetchButton = findModelsFetchButton(renderer!.root);
    expect(fetchButton).toBeDefined();

    await act(async () => {
      fetchButton!.props.onClick();
    });

    expect(mocks.fetchModelsViaApiCall).toHaveBeenCalledWith(
      'https://api.example.com/v1',
      'second-key',
      {},
      'auth-second',
      'socks5://proxy.example:1080'
    );

    act(() => renderer!.unmount());
  });

  it.each([
    [true, 'enabled', false],
    [true, 'inherit', null],
  ] as const)(
    'saves cooling %j -> %s as disable-cooling %j',
    async (initialOverride, nextPolicy, expectedOverride) => {
      const provider = {
        name: 'openai-example',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [{ apiKey: 'openai-key' }],
        models: [],
        disableCooling: initialOverride,
      };
      mocks.getOpenAIProviders.mockResolvedValue([provider]);
      const onSaved = vi.fn();
      let renderer: ReactTestRenderer;
      await act(async () => {
        renderer = create(
          <OpenAIEditDrawer
            open
            editIndex={0}
            disabled={false}
            onClose={vi.fn()}
            onSaved={onSaved}
          />
        );
      });

      act(() => renderer!.root.findByType(CoolingPolicySelect).props.onChange(nextPolicy));
      const saveButton = findSaveButton(renderer!.root);
      expect(saveButton?.props.disabled).not.toBe(true);

      await act(async () => {
        await saveButton?.props.onClick();
      });

      expect(mocks.updateOpenAIProvider).toHaveBeenCalledWith(
        'openai-example',
        0,
        expect.objectContaining({ disableCooling: expectedOverride })
      );
      expect(onSaved).toHaveBeenCalledTimes(1);

      act(() => renderer!.unmount());
    }
  );

  it('saves thinking levels and preserves the rest of the thinking object', async () => {
    mocks.getOpenAIProviders.mockResolvedValue([
      {
        name: 'openai-example',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [{ apiKey: 'openai-key' }],
        models: [
          {
            name: 'thinking-model',
            thinking: { levels: ['low'], 'future-option': { enabled: true } },
          },
        ],
      },
    ]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <OpenAIEditDrawer open editIndex={0} disabled={false} onClose={vi.fn()} onSaved={vi.fn()} />
      );
    });

    const maxLevel = findInputByLabelSuffix(renderer!.root, ' max');
    act(() => {
      maxLevel?.props.onChange({ target: { checked: true } });
    });

    const saveButton = findSaveButton(renderer!.root);
    expect(saveButton?.props.disabled).not.toBe(true);
    await act(async () => {
      await saveButton?.props.onClick();
    });

    expect(mocks.updateOpenAIProvider).toHaveBeenCalledWith(
      'openai-example',
      0,
      expect.objectContaining({
        models: [
          {
            name: 'thinking-model',
            thinking: { levels: ['low', 'max'], 'future-option': { enabled: true } },
          },
        ],
      })
    );

    act(() => renderer!.unmount());
  });

  it('clears only thinking levels and cleans the committed fallback after readback failure', async () => {
    const provider = {
      name: 'openai-example',
      baseUrl: 'https://api.example.com/v1',
      apiKeyEntries: [{ apiKey: 'openai-key' }],
      models: [
        {
          name: 'thinking-model',
          thinking: { levels: ['high'], 'future-option': { enabled: true } },
        },
      ],
    };
    mocks.getOpenAIProviders
      .mockResolvedValueOnce([provider])
      .mockRejectedValueOnce(new Error('readback failed'));

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <OpenAIEditDrawer open editIndex={0} disabled={false} onClose={vi.fn()} onSaved={vi.fn()} />
      );
    });

    act(() => {
      findInputByLabelSuffix(
        renderer!.root,
        i18n.t('ai_providers.thinking_default_label')
      )?.props.onChange();
    });

    const saveButton = findSaveButton(renderer!.root);
    await act(async () => {
      await saveButton?.props.onClick();
    });

    const mutationPayload = mocks.updateOpenAIProvider.mock.calls[0]?.[2] as {
      models?: Array<{ thinking?: Record<string, unknown> }>;
    };
    const mutationModel = mutationPayload.models?.[0];
    expect(hasModelThinkingLevelsClearMarker(mutationModel)).toBe(true);
    expect(mutationModel?.thinking).toEqual({ 'future-option': { enabled: true } });

    const lastUpdate = mocks.updateConfigValue.mock.calls.slice(-1)[0];
    const committedFallback = lastUpdate?.[1] as Array<{
      models?: Array<{ thinking?: Record<string, unknown> }>;
    }>;
    const committedModel = committedFallback[0]?.models?.[0];
    expect(hasModelThinkingLevelsClearMarker(committedModel)).toBe(false);
    expect(committedModel?.thinking).toEqual({ 'future-option': { enabled: true } });
    expect(committedModel?.thinking?.levels).toBeUndefined();

    act(() => renderer!.unmount());
  });

  it('materializes edited Thinking aliases in the committed fallback after readback failure', async () => {
    const provider = {
      name: 'openai-example',
      baseUrl: 'https://api.example.com/v1',
      apiKeyEntries: [{ apiKey: 'openai-key' }],
      models: [
        {
          name: 'thinking-model',
          thinking: {
            levels: ['low'],
            zero_allowed: true,
            'future-option': { enabled: true },
          },
        },
      ],
    };
    mocks.getOpenAIProviders
      .mockResolvedValueOnce([provider])
      .mockRejectedValueOnce(new Error('readback failed'));

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <OpenAIEditDrawer open editIndex={0} disabled={false} onClose={vi.fn()} onSaved={vi.fn()} />
      );
    });

    const noneLevel = findInputByLabelSuffix(renderer!.root, ' none');
    expect(noneLevel?.props.checked).toBe(true);
    act(() => {
      noneLevel?.props.onChange({ target: { checked: false } });
    });

    const saveButton = findSaveButton(renderer!.root);
    await act(async () => {
      await saveButton?.props.onClick();
    });

    const mutationPayload = mocks.updateOpenAIProvider.mock.calls[0]?.[2] as {
      models?: Array<{ thinking?: Record<string, unknown> }>;
    };
    const mutationModel = mutationPayload.models?.[0];
    expect(hasModelThinkingLevelsEditMarker(mutationModel)).toBe(true);
    expect(mutationModel?.thinking).toEqual({
      levels: ['low'],
      zero_allowed: true,
      'future-option': { enabled: true },
    });

    const lastUpdate = mocks.updateConfigValue.mock.calls.slice(-1)[0];
    const committedFallback = lastUpdate?.[1] as Array<{
      models?: Array<{ thinking?: Record<string, unknown> }>;
    }>;
    const committedModel = committedFallback[0]?.models?.[0];
    expect(hasModelThinkingLevelsEditMarker(committedModel)).toBe(false);
    expect(hasModelThinkingLevelsClearMarker(committedModel)).toBe(false);
    expect(committedModel?.thinking).toEqual({
      levels: ['low'],
      'future-option': { enabled: true },
    });
    expect(committedModel?.thinking?.zero_allowed).toBeUndefined();

    const modelList = renderer!.root.findByType(ModelInputList);
    const committedEntry = modelList.props.entries[0] as {
      thinking?: Record<string, unknown>;
    };
    expect(hasModelThinkingLevelsEditMarker(committedEntry)).toBe(false);
    expect(committedEntry.thinking).toEqual({
      levels: ['low'],
      'future-option': { enabled: true },
    });
    expect(committedEntry.thinking?.zero_allowed).toBeUndefined();

    act(() => renderer!.unmount());
  });

  it('normalizes equivalent known thinking levels before saving', async () => {
    mocks.getOpenAIProviders.mockResolvedValue([
      {
        name: 'openai-example',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [{ apiKey: 'openai-key' }],
        models: [{ name: 'thinking-model', thinking: { levels: ['HIGH'] } }],
      },
    ]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <OpenAIEditDrawer open editIndex={0} disabled={false} onClose={vi.fn()} onSaved={vi.fn()} />
      );
    });

    expect(findInputByLabelSuffix(renderer!.root, ' high')?.props.checked).toBe(true);
    act(() => {
      findInputByLabelSuffix(renderer!.root, ' max')?.props.onChange({
        target: { checked: true },
      });
    });

    const saveButton = findSaveButton(renderer!.root);
    await act(async () => {
      await saveButton?.props.onClick();
    });

    expect(mocks.updateOpenAIProvider).toHaveBeenCalledWith(
      'openai-example',
      0,
      expect.objectContaining({
        models: [{ name: 'thinking-model', thinking: { levels: ['high', 'max'] } }],
      })
    );

    act(() => renderer!.unmount());
  });

  it('blocks saving a custom model with no thinking levels', async () => {
    mocks.getOpenAIProviders.mockResolvedValue([
      {
        name: 'openai-example',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [{ apiKey: 'openai-key' }],
        models: [{ name: 'thinking-model' }],
      },
    ]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <OpenAIEditDrawer open editIndex={0} disabled={false} onClose={vi.fn()} onSaved={vi.fn()} />
      );
    });

    act(() => {
      findInputByLabelSuffix(
        renderer!.root,
        i18n.t('ai_providers.thinking_custom_label')
      )?.props.onChange();
    });

    expect(renderer!.root.findByProps({ role: 'alert' }).children.join('')).toContain(
      i18n.t('ai_providers.thinking_required_error')
    );
    const saveButton = findSaveButton(renderer!.root);
    expect(saveButton?.props.disabled).toBe(true);
    await act(async () => {
      await saveButton?.props.onClick();
    });
    expect(mocks.updateOpenAIProvider).not.toHaveBeenCalled();

    act(() => renderer!.unmount());
  });

  it('moves the CPA proxy compatibility message into a focusable tooltip', async () => {
    mocks.getOpenAIProviders.mockResolvedValue([
      {
        name: 'openai-example',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [{ apiKey: 'openai-key' }],
        models: [],
      },
    ]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <OpenAIEditDrawer open editIndex={0} disabled={false} onClose={vi.fn()} onSaved={vi.fn()} />
      );
    });

    expect(
      renderer!.root
        .findAllByProps({ className: 'hint' })
        .some((node) => node.children.join('').includes('CPA v7.2.130'))
    ).toBe(false);
    const tooltipTrigger = renderer!.root.findAllByProps({
      'data-info-tooltip-trigger': 'true',
    })[0];
    expect(tooltipTrigger.props.tabIndex).not.toBe(-1);

    act(() => {
      tooltipTrigger.props.onFocus();
    });
    const tooltip = renderer!.root.findByProps({ role: 'tooltip' });
    expect(tooltip.children.join('')).toContain('CPA v7.2.130');

    act(() => renderer!.unmount());
  });

  it('adds discovered models without opting them into thinking levels', async () => {
    mocks.getOpenAIProviders.mockResolvedValue([
      {
        name: 'openai-example',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [{ apiKey: 'openai-key' }],
        models: [],
      },
    ]);
    mocks.fetchModelsViaApiCall.mockResolvedValue([{ name: 'new-model' }]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <OpenAIEditDrawer open editIndex={0} disabled={false} onClose={vi.fn()} onSaved={vi.fn()} />
      );
    });

    await act(async () => {
      findModelsFetchButton(renderer!.root)!.props.onClick();
    });
    const discoveredModel = renderer!.root.findByProps({ 'aria-label': 'new-model' });
    act(() => {
      discoveredModel.props.onChange({ target: { checked: true } });
    });

    const applyLabel = i18n.t('ai_providers.openai_models_fetch_apply');
    const applyButton = renderer!.root
      .findAllByType('button')
      .find((button) =>
        button.findAllByType('span').some((span) => span.children.join('') === applyLabel)
      );
    expect(applyButton).toBeDefined();
    expect(applyButton?.props.disabled).not.toBe(true);
    await act(async () => {
      applyButton?.props.onClick();
    });

    const modelList = renderer!.root.findByType(ModelInputList);
    expect(modelList.props.entries).toEqual([{ name: 'new-model', alias: '' }]);

    act(() => renderer!.unmount());
  });
});
