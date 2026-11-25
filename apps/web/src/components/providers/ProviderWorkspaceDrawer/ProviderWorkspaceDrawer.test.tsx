import { createElement, type ReactNode } from 'react';
import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { buildProviderRows } from '../ProviderTable/rowData';
import type { ProviderRecentUsageMap } from '../utils';
import type { ProviderKeyConfig } from '@/types';
import {
  getPersistedOpenAIKeyEntryIndex,
  isPersistedOpenAIKeyEntry,
  markPersistedOpenAIKeyEntry,
} from '@/utils/openAIKeyEntries';

const mocks = vi.hoisted(() => ({
  getGeminiKeys: vi.fn(),
  saveGeminiKeys: vi.fn(),
  createGeminiKey: vi.fn(),
  updateGeminiKey: vi.fn(),
  getInteractionsKeys: vi.fn(),
  saveInteractionsKeys: vi.fn(),
  createInteractionsKey: vi.fn(),
  updateInteractionsKey: vi.fn(),
  getCodexConfigs: vi.fn(),
  saveCodexConfigs: vi.fn(),
  createCodexConfig: vi.fn(),
  updateCodexConfig: vi.fn(),
  getXAIConfigs: vi.fn(),
  saveXAIConfigs: vi.fn(),
  createXAIConfig: vi.fn(),
  updateXAIConfig: vi.fn(),
  getClaudeConfigs: vi.fn(),
  saveClaudeConfigs: vi.fn(),
  createClaudeConfig: vi.fn(),
  updateClaudeConfig: vi.fn(),
  getVertexConfigs: vi.fn(),
  saveVertexConfigs: vi.fn(),
  createVertexConfig: vi.fn(),
  updateVertexConfig: vi.fn(),
  getOpenAIProviders: vi.fn(),
  saveOpenAIProviders: vi.fn(),
  updateOpenAIProvider: vi.fn(),
  createOpenAIProvider: vi.fn(),
  readBackClaudeConfigAfterSave: vi.fn(),
  verifyClaudeFingerprintInRawConfig: vi.fn(),
  apiCallRequest: vi.fn(),
  fetchModelsViaApiCall: vi.fn(),
  fetchV1ModelsViaApiCall: vi.fn(),
  fetchClaudeModelsViaApiCall: vi.fn(),
  fetchGeminiModelsViaApiCall: vi.fn(),
  showNotification: vi.fn(),
  showConfirmation: vi.fn(),
  updateConfigValue: vi.fn(),
  clearCache: vi.fn(),
  drawerPropsRef: {
    current: null as {
      onBeforeClose?: () => boolean | Promise<boolean>;
      onClose?: () => void;
    } | null,
  },
}));

vi.mock('@/services/api', () => ({
  providersApi: {
    getGeminiKeys: mocks.getGeminiKeys,
    saveGeminiKeys: mocks.saveGeminiKeys,
    createGeminiKey: mocks.createGeminiKey,
    updateGeminiKey: mocks.updateGeminiKey,
    getInteractionsKeys: mocks.getInteractionsKeys,
    saveInteractionsKeys: mocks.saveInteractionsKeys,
    createInteractionsKey: mocks.createInteractionsKey,
    updateInteractionsKey: mocks.updateInteractionsKey,
    getCodexConfigs: mocks.getCodexConfigs,
    saveCodexConfigs: mocks.saveCodexConfigs,
    createCodexConfig: mocks.createCodexConfig,
    updateCodexConfig: mocks.updateCodexConfig,
    getXAIConfigs: mocks.getXAIConfigs,
    saveXAIConfigs: mocks.saveXAIConfigs,
    createXAIConfig: mocks.createXAIConfig,
    updateXAIConfig: mocks.updateXAIConfig,
    getClaudeConfigs: mocks.getClaudeConfigs,
    saveClaudeConfigs: mocks.saveClaudeConfigs,
    createClaudeConfig: mocks.createClaudeConfig,
    updateClaudeConfig: mocks.updateClaudeConfig,
    getVertexConfigs: mocks.getVertexConfigs,
    saveVertexConfigs: mocks.saveVertexConfigs,
    createVertexConfig: mocks.createVertexConfig,
    updateVertexConfig: mocks.updateVertexConfig,
    getOpenAIProviders: mocks.getOpenAIProviders,
    saveOpenAIProviders: mocks.saveOpenAIProviders,
    updateOpenAIProvider: mocks.updateOpenAIProvider,
    createOpenAIProvider: mocks.createOpenAIProvider,
    getLatestV8ConfigEtag: vi.fn(),
  },
  modelsApi: {
    fetchModelsViaApiCall: mocks.fetchModelsViaApiCall,
    fetchV1ModelsViaApiCall: mocks.fetchV1ModelsViaApiCall,
    fetchClaudeModelsViaApiCall: mocks.fetchClaudeModelsViaApiCall,
    fetchGeminiModelsViaApiCall: mocks.fetchGeminiModelsViaApiCall,
    buildOpenAIModelsEndpoint: (base: string) => `${base}/models`,
    buildClaudeModelsEndpoint: (base: string) => `${base}/v1/models`,
    buildV1ModelsEndpoint: (base: string) => `${base}/v1/models`,
    buildGeminiModelsEndpoint: (base: string) => `${base}/v1beta/models`,
  },
  apiCallApi: {
    request: mocks.apiCallRequest,
  },
  getApiCallErrorMessage: (result: {
    statusCode: number;
    body?: { error?: { message?: string } };
    bodyText?: string;
  }) => {
    const msg = result.body?.error?.message;
    if (msg) return `${result.statusCode} ${msg}`;
    if (result.bodyText) return `${result.statusCode} ${result.bodyText}`;
    return `HTTP ${result.statusCode}`;
  },
  getApiCallErrorDetails: (result: { statusCode: number; bodyText?: string }) =>
    result.bodyText
      ? `HTTP ${result.statusCode} | Body: ${result.bodyText}`
      : `HTTP ${result.statusCode}`,
  readBackClaudeConfigAfterSave: mocks.readBackClaudeConfigAfterSave,
  verifyClaudeFingerprintInRawConfig: mocks.verifyClaudeFingerprintInRawConfig,
}));

vi.mock('@/stores', () => ({
  useConfigStore: (
    selector: (state: {
      config: Record<string, unknown>;
      updateConfigValue: typeof mocks.updateConfigValue;
      clearCache: typeof mocks.clearCache;
    }) => unknown
  ) =>
    selector({
      config: { credentialCoolingEnabled: false },
      updateConfigValue: mocks.updateConfigValue,
      clearCache: mocks.clearCache,
    }),
  useNotificationStore: Object.assign(
    (
      selector?: (state: {
        showNotification: typeof mocks.showNotification;
        showConfirmation: typeof mocks.showConfirmation;
      }) => unknown
    ) => {
      const state = {
        showNotification: mocks.showNotification,
        showConfirmation: mocks.showConfirmation,
      };
      return selector ? selector(state) : state;
    },
    {
      getState: () => ({
        showNotification: mocks.showNotification,
        showConfirmation: mocks.showConfirmation,
      }),
    }
  ),
}));

vi.mock('@/stores/useAuthStore', () => ({
  useAuthStore: (selector: (state: { apiBase: string; managementKey: string }) => unknown) =>
    selector({ apiBase: 'http://localhost:8317', managementKey: 'mgmt-key' }),
}));

vi.mock('@/components/ui/Drawer', () => ({
  Drawer: ({
    open,
    children,
    footer,
    title,
    onBeforeClose,
    onClose,
  }: {
    open: boolean;
    children: ReactNode;
    footer?: ReactNode;
    title?: ReactNode;
    onBeforeClose?: () => boolean | Promise<boolean>;
    onClose?: () => void;
  }) => {
    mocks.drawerPropsRef.current = { onBeforeClose, onClose };
    return open ? createElement('div', { 'data-testid': 'drawer-shell' }, title, children, footer) : null;
  },
}));

import { ProviderWorkspaceDrawer } from './ProviderWorkspaceDrawer';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const emptyUsageByProvider = new Map() as ProviderRecentUsageMap;
const renderers: ReactTestRenderer[] = [];

const flush = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
};

// tsconfig lib targets < ES2024 where Promise.withResolvers is unavailable.
const createDeferred = <T,>() => {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
};

const extractText = (node: ReactTestInstance): string => {
  return node.children
    .map((child) => {
      if (typeof child === 'string' || typeof child === 'number') return String(child);
      return extractText(child);
    })
    .join(' ');
};

const findTabByText = (root: ReactTestInstance, labelSubstring: string) =>
  root
    .findAll((node) => node.type === 'button' && node.props.role === 'tab')
    .find((btn) => extractText(btn).includes(labelSubstring));

const findButtonByText = (root: ReactTestInstance, labelText: string) =>
  root
    .findAllByType('button')
    .find((btn) => extractText(btn).trim().includes(labelText));

const findInputByLabel = (root: ReactTestInstance, labelSubstring: string) =>
  root
    .findAllByType('input')
    .find((input) => {
      const label = input.parent?.parent?.findAllByType('label')[0];
      if (label && extractText(label).includes(labelSubstring)) return true;
      return String(input.props['aria-label'] ?? '').includes(labelSubstring);
    });

describe('ProviderWorkspaceDrawer', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.drawerPropsRef.current = null;
    mocks.getClaudeConfigs.mockResolvedValue([]);
    mocks.saveClaudeConfigs.mockResolvedValue(undefined);
    mocks.createClaudeConfig.mockResolvedValue(undefined);
    mocks.updateClaudeConfig.mockResolvedValue(undefined);
    mocks.createGeminiKey.mockResolvedValue(undefined);
    mocks.updateGeminiKey.mockResolvedValue(undefined);
    mocks.createInteractionsKey.mockResolvedValue(undefined);
    mocks.updateInteractionsKey.mockResolvedValue(undefined);
    mocks.createCodexConfig.mockResolvedValue(undefined);
    mocks.updateCodexConfig.mockResolvedValue(undefined);
    mocks.createXAIConfig.mockResolvedValue(undefined);
    mocks.updateXAIConfig.mockResolvedValue(undefined);
    mocks.createVertexConfig.mockResolvedValue(undefined);
    mocks.updateVertexConfig.mockResolvedValue(undefined);
    mocks.getOpenAIProviders.mockResolvedValue([]);
    mocks.updateOpenAIProvider.mockResolvedValue(undefined);
    mocks.createOpenAIProvider.mockResolvedValue(undefined);
    mocks.apiCallRequest.mockResolvedValue({ statusCode: 200, body: '{}' });
  });

  afterEach(async () => {
    for (const renderer of renderers.splice(0)) {
      await act(async () => {
        renderer.unmount();
      });
    }
  });

  it('preserves unsaved draft across overview, config, models, and connectivity tabs', async () => {
    const rows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Claude 主力',
          apiKey: 'sk-ant-initial',
          baseUrl: 'https://claude.initial.example.com',
          models: [{ name: 'claude-sonnet-4-5', alias: 'claude-sonnet-4-5' }],
          excludedModels: ['claude-3-opus'],
        },
      ],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(renderer!);
    await flush();

    // 1. Switch to config tab and edit alias + baseUrl
    const configTab = findTabByText(renderer!.root, i18n.t('ai_providers.drawer_tab_config', { defaultValue: '配置' }));
    expect(configTab).toBeDefined();
    await act(async () => {
      configTab!.props.onClick();
    });

    const aliasInput = findInputByLabel(renderer!.root, i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' }));
    const baseUrlInput = findInputByLabel(renderer!.root, i18n.t('ai_providers.claude_add_modal_url_label'));
    expect(aliasInput).toBeDefined();
    expect(baseUrlInput).toBeDefined();

    await act(async () => {
      aliasInput!.props.onChange({ target: { value: 'Claude 灰度网关' } });
      baseUrlInput!.props.onChange({ target: { value: 'https://draft.claude.example.com' } });
    });

    // 2. Switch to models tab and edit excluded models
    const modelsTab = findTabByText(renderer!.root, i18n.t('ai_providers.drawer_tab_models', { defaultValue: '模型' }));
    await act(async () => {
      modelsTab!.props.onClick();
    });
    const excludedTextarea = renderer!.root.findByType('textarea');
    expect(excludedTextarea.props.value).toBe('claude-3-opus');
    await act(async () => {
      excludedTextarea.props.onChange({ target: { value: 'claude-3-opus\nclaude-3-haiku' } });
    });

    // 3. Switch to connectivity tab and test using LIVE draft values before saving
    const connectivityTab = findTabByText(
      renderer!.root,
      i18n.t('ai_providers.drawer_tab_connectivity', { defaultValue: '测活诊断' })
    );
    await act(async () => {
      connectivityTab!.props.onClick();
    });

    const sendTestButton = findButtonByText(
      renderer!.root,
      i18n.t('ai_providers.send_test_request_btn', { defaultValue: '立即发送测试请求' })
    );
    expect(sendTestButton).toBeDefined();
    await act(async () => {
      await sendTestButton!.props.onClick();
    });

    expect(mocks.apiCallRequest).toHaveBeenCalledWith(
      expect.objectContaining({
        url: 'https://draft.claude.example.com/v1/messages',
      }),
      expect.any(Object)
    );

    // 4. Switch back to config & models and verify draft edits survived
    await act(async () => {
      findTabByText(renderer!.root, i18n.t('ai_providers.drawer_tab_config', { defaultValue: '配置' }))!.props.onClick();
    });
    expect(
      findInputByLabel(renderer!.root, i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' }))!
        .props.value
    ).toBe('Claude 灰度网关');

    await act(async () => {
      findTabByText(renderer!.root, i18n.t('ai_providers.drawer_tab_models', { defaultValue: '模型' }))!.props.onClick();
    });
    expect(renderer!.root.findByType('textarea').props.value).toBe('claude-3-opus\nclaude-3-haiku');
  });

  it('saves CPA v8 name alias and preserves v8Source for both claude and openai providers', async () => {
    const v8ClaudeSource = {
      family: 'claude' as const,
      groupIndex: 0,
      keyIndex: 0,
      group: { name: 'old-claude', 'base-url': 'https://claude.example.com' },
    };
    const claudeRows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'old-claude',
          apiKey: 'sk-ant-1234',
          baseUrl: 'https://claude.example.com',
          v8Source: v8ClaudeSource,
        },
      ],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    });
    mocks.getClaudeConfigs.mockResolvedValue([claudeRows[0].raw]);
    const onSaved = vi.fn();

    let claudeRenderer: ReactTestRenderer;
    await act(async () => {
      claudeRenderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={claudeRows[0]}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={onSaved}
        />
      );
    });
    renderers.push(claudeRenderer!);
    await flush();

    const claudeNameInput = findInputByLabel(
      claudeRenderer!.root,
      i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
    );
    await act(async () => {
      claudeNameInput!.props.onChange({ target: { value: 'Claude 主力别名' } });
    });

    const saveBtn = findButtonByText(
      claudeRenderer!.root,
      i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
    );
    await act(async () => {
      await saveBtn!.props.onClick();
    });
    await flush();

    expect(mocks.updateClaudeConfig).toHaveBeenCalledTimes(1);
    expect(mocks.updateClaudeConfig).toHaveBeenCalledWith(
      expect.objectContaining({
        name: 'old-claude',
        v8Source: v8ClaudeSource,
      }),
      expect.objectContaining({
        name: 'Claude 主力别名',
        v8Source: v8ClaudeSource,
      })
    );
    expect(onSaved).toHaveBeenCalledWith(
      'claude',
      expect.objectContaining({
        name: 'Claude 主力别名',
        apiKey: 'sk-ant-1234',
        baseUrl: 'https://claude.example.com',
      })
    );

    // Now verify OpenAI alias & v8Source preservation
    const v8OpenAISource = {
      family: 'openai-compatibility' as const,
      groupIndex: 1,
      group: { name: 'openrouter-old' },
    };
    const openAIRows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [],
      vertex: [],
      openai: [
        {
          name: 'openrouter-old',
          baseUrl: 'https://openrouter.ai/api/v1',
          apiKeyEntries: [{ apiKey: 'sk-or-1234', weight: 2 }],
          v8Source: v8OpenAISource,
        },
      ],
      usageByProvider: emptyUsageByProvider,
    });
    mocks.getOpenAIProviders.mockResolvedValue([openAIRows[0].raw]);

    let openAIRenderer: ReactTestRenderer;
    await act(async () => {
      openAIRenderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={openAIRows[0]}
          initialTab="config"
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={onSaved}
        />
      );
    });
    renderers.push(openAIRenderer!);
    await flush();

    const openAINameInput = findInputByLabel(
      openAIRenderer!.root,
      i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
    );
    await act(async () => {
      openAINameInput!.props.onChange({ target: { value: 'openrouter-new' } });
    });

    const saveOpenAIBtn = findButtonByText(
      openAIRenderer!.root,
      i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
    );
    await act(async () => {
      await saveOpenAIBtn!.props.onClick();
    });
    await flush();

    expect(mocks.updateOpenAIProvider).toHaveBeenCalledWith(
      'openrouter-old',
      0,
      expect.objectContaining({
        name: 'openrouter-new',
        v8Source: v8OpenAISource,
      })
    );
    expect(onSaved).toHaveBeenCalledWith(
      'openai',
      expect.objectContaining({
        name: 'openrouter-new',
        baseUrl: 'https://openrouter.ai/api/v1',
      })
    );
  });

  it('preserves disabled * rule in excludedModels when editing a disabled single-key provider', async () => {
    const rows = buildProviderRows({
      gemini: [
        {
          name: 'Disabled Gemini',
          apiKey: 'gm-disabled-key',
          baseUrl: 'https://generativelanguage.googleapis.com',
          excludedModels: ['gemini-pro-vision', '*'],
        },
      ],
      codex: [],
      claude: [],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    });
    expect(rows[0].enabled).toBe(false);
    mocks.getGeminiKeys.mockResolvedValue([rows[0].raw]);
    mocks.saveGeminiKeys.mockResolvedValue(undefined);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          initialTab="models"
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(renderer!);
    await flush();

    const textarea = renderer!.root.findByType('textarea');
    expect(textarea.props.value).toBe('gemini-pro-vision');
    expect(textarea.props.value).not.toContain('*');

    await act(async () => {
      textarea.props.onChange({ target: { value: 'gemini-pro-vision\ngemini-1.5-flash' } });
    });

    const saveBtn = findButtonByText(
      renderer!.root,
      i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
    );
    await act(async () => {
      await saveBtn!.props.onClick();
    });
    await flush();

    expect(mocks.updateGeminiKey).toHaveBeenCalledWith(
      expect.objectContaining({ apiKey: 'gm-disabled-key' }),
      expect.objectContaining({
        excludedModels: ['gemini-pro-vision', 'gemini-1.5-flash', '*'],
      })
    );
  });

  it('blocks close via onBeforeClose when draft is dirty and user cancels confirmation', async () => {
    const rows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Claude Dirty Test',
          apiKey: 'sk-ant-1234',
          baseUrl: 'https://claude.example.com',
        },
      ],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    });

    const onClose = vi.fn();
    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          initialTab="config"
          usageByProvider={emptyUsageByProvider}
          onClose={onClose}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(renderer!);
    await flush();

    const aliasInput = findInputByLabel(
      renderer!.root,
      i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
    );
    await act(async () => {
      aliasInput!.props.onChange({ target: { value: 'Unsaved Edit' } });
    });

    mocks.showConfirmation.mockImplementationOnce((opts: { onCancel?: () => void }) => {
      opts.onCancel?.();
    });

    const allowedWhenCancelled = await mocks.drawerPropsRef.current?.onBeforeClose?.();
    expect(mocks.showConfirmation).toHaveBeenCalledTimes(1);
    expect(allowedWhenCancelled).toBe(false);
    expect(onClose).not.toHaveBeenCalled();

    mocks.showConfirmation.mockImplementationOnce((opts: { onConfirm?: () => void }) => {
      opts.onConfirm?.();
    });

    const allowedWhenConfirmed = await mocks.drawerPropsRef.current?.onBeforeClose?.();
    expect(allowedWhenConfirmed).toBe(true);
  });

  it('supports create mode with 3 tabs, kind switching, and saving a new gemini provider', async () => {
    mocks.getGeminiKeys.mockResolvedValue([]);
    mocks.saveGeminiKeys.mockResolvedValue(undefined);
    const onClose = vi.fn();
    const onSaved = vi.fn();
    let currentCreateKind: 'openai' | 'gemini' = 'openai';
    let renderer: ReactTestRenderer;
    const onCreateKindChange = vi.fn((nextKind: string) => {
      if (nextKind === 'gemini') {
        currentCreateKind = 'gemini';
        renderer.update(
          <ProviderWorkspaceDrawer
            open
            mode="create"
            row={null}
            createKind={currentCreateKind}
            onCreateKindChange={onCreateKindChange}
            usageByProvider={emptyUsageByProvider}
            onClose={onClose}
            onSaved={onSaved}
          />
        );
      }
    });

    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="create"
          row={null}
          createKind={currentCreateKind}
          onCreateKindChange={onCreateKindChange}
          usageByProvider={emptyUsageByProvider}
          onClose={onClose}
          onSaved={onSaved}
        />
      );
    });
    renderers.push(renderer!);
    await flush();

    const drawerTabList = renderer!.root.find(
      (node) =>
        node.props.role === 'tablist' &&
        node.props['aria-label'] ===
          i18n.t('ai_providers.drawer_tabs_label', { defaultValue: '提供商配置标签页' })
    );
    const tabs = drawerTabList.findAll(
      (node) => node.type === 'button' && node.props.role === 'tab'
    );
    expect(tabs).toHaveLength(3);
    expect(
      findTabByText(
        drawerTabList,
        i18n.t('ai_providers.drawer_tab_overview', { defaultValue: '概览' })
      )
    ).toBeUndefined();

    const geminiKindButton = findButtonByText(renderer!.root, 'Gemini');
    expect(geminiKindButton).toBeDefined();
    await act(async () => {
      geminiKindButton!.props.onClick();
    });
    expect(onCreateKindChange).toHaveBeenCalledWith('gemini');
    const aliasInput = findInputByLabel(
      renderer!.root,
      i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
    );
    const keyInput = findInputByLabel(
      renderer!.root,
      i18n.t('ai_providers.api_key_label', { defaultValue: 'API Key' })
    );
    expect(aliasInput).toBeDefined();
    expect(keyInput).toBeDefined();

    await act(async () => {
      aliasInput!.props.onChange({ target: { value: 'New Gemini Pool' } });
      keyInput!.props.onChange({ target: { value: 'AIza-new-key' } });
    });

    const submitButton = findButtonByText(
      renderer!.root,
      i18n.t('ai_providers.drawer_create_submit', { defaultValue: '创建配置' })
    );
    expect(submitButton).toBeDefined();
    await act(async () => {
      await submitButton!.props.onClick();
    });
    await flush();

    expect(mocks.createGeminiKey).toHaveBeenCalledWith(
      expect.objectContaining({
        name: 'New Gemini Pool',
        apiKey: 'AIza-new-key',
      })
    );
    expect(onSaved).toHaveBeenCalledWith(
      'gemini',
      expect.objectContaining({
        name: 'New Gemini Pool',
        apiKey: 'AIza-new-key',
      })
    );
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('disables single-key alias input when v8AliasSupported is false and supports overview quick-enable & reveal', async () => {
    const rows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Legacy Claude',
          apiKey: 'sk-ant-secret-9999',
          baseUrl: 'https://claude.example.com',
          excludedModels: ['*'],
        },
      ],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    });
    const onToggleEnabled = vi.fn();

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          v8AliasSupported={false}
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          onToggleEnabled={onToggleEnabled}
        />
      );
    });
    renderers.push(renderer!);
    await flush();

    // Overview quick enable
    const quickEnableBtn = findButtonByText(
      renderer!.root,
      i18n.t('ai_providers.drawer_quick_enable', { defaultValue: '立即启用' })
    );
    expect(quickEnableBtn).toBeDefined();
    await act(async () => {
      quickEnableBtn!.props.onClick();
    });
    expect(onToggleEnabled).toHaveBeenCalledWith(rows[0], true);

    // Overview eye reveal toggle
    const revealBtn = renderer!.root
      .findAllByType('button')
      .find(
        (btn) =>
          btn.props['aria-label'] === i18n.t('common.reveal', { defaultValue: '显示明文' })
      );
    expect(revealBtn).toBeDefined();
    expect(extractText(renderer!.root)).not.toContain('sk-ant-secret-9999');
    await act(async () => {
      revealBtn!.props.onClick();
    });
    expect(extractText(renderer!.root)).toContain('sk-ant-secret-9999');


    // Overview clean labels (no "(可选)"), historical stats vs recent 200m window separation, and inactive-weight hint
    const overviewText = extractText(renderer!.root);
    expect(overviewText).not.toContain('(可选)');
    expect(overviewText).toContain(i18n.t('ai_providers.endpoint_prefix_label', { defaultValue: '前缀' }));
    expect(overviewText).toContain(i18n.t('ai_providers.overview_cloak_label', { defaultValue: '请求伪装' }));
    expect(overviewText).toContain(
      i18n.t('accounts.detail_overview_recent_status_title', { defaultValue: '最近状态' })
    );
    expect(overviewText).toContain(
      i18n.t('ai_providers.card_history_title', { defaultValue: '历史用量' })
    );
    // Config tab alias input disabled when v8AliasSupported is false
    const configTab = findTabByText(
      renderer!.root,
      i18n.t('ai_providers.drawer_tab_config', { defaultValue: '配置' })
    );
    await act(async () => {
      configTab!.props.onClick();
    });
    const aliasInput = findInputByLabel(
      renderer!.root,
      i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
    );
    expect(aliasInput!.props.disabled).toBe(true);
  });

  it('surfaces conflict error instead of overwriting unrelated entry and preserves v8Source carry-over', async () => {
    const v8ClaudeSource = {
      family: 'claude' as const,
      groupIndex: 0,
      keyIndex: 0,
      group: { name: 'Target Claude' },
      etag: '"etag-1"',
    };
    const rows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Target Claude',
          apiKey: 'sk-ant-target',
          baseUrl: 'https://claude.target.example.com',
          authIndex: 'auth-target',
          v8Source: v8ClaudeSource,
        },
      ],
      vertex: [],
      openai: [
        {
          name: 'target-openai',
          baseUrl: 'https://openai.target.example.com/v1',
          apiKeyEntries: [{ apiKey: 'sk-target' }],
        },
      ],
      usageByProvider: emptyUsageByProvider,
    });

    // 1. Non-empty latestList missing target -> conflict error, no overwrite of index 0
    mocks.getClaudeConfigs.mockResolvedValue([
      {
        name: 'Unrelated Claude',
        apiKey: 'sk-ant-other',
        baseUrl: 'https://claude.other.example.com',
        authIndex: 'auth-other',
      },
    ]);
    let claudeRenderer: ReactTestRenderer;
    await act(async () => {
      claudeRenderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(claudeRenderer!);
    await flush();

    const claudeNameInput = findInputByLabel(
      claudeRenderer!.root,
      i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
    );
    await act(async () => {
      claudeNameInput!.props.onChange({ target: { value: 'Edited Target' } });
    });
    const saveClaudeBtn = findButtonByText(
      claudeRenderer!.root,
      i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
    );
    await act(async () => {
      await saveClaudeBtn!.props.onClick();
    });
    await flush();

    expect(mocks.updateClaudeConfig).not.toHaveBeenCalled();
    expect(mocks.saveClaudeConfigs).not.toHaveBeenCalled();
    const alertNode = claudeRenderer!.root.find((node) => node.props.role === 'alert');
    expect(extractText(alertNode)).toContain('Provider configuration changed');

    // 2. Non-empty OpenAI latestList missing target -> conflict error, no overwrite
    mocks.getOpenAIProviders.mockResolvedValue([
      {
        name: 'unrelated-openai',
        baseUrl: 'https://unrelated.example.com/v1',
        apiKeyEntries: [{ apiKey: 'sk-other' }],
      },
    ]);
    let openAIRenderer: ReactTestRenderer;
    await act(async () => {
      openAIRenderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[1]}
          initialTab="config"
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(openAIRenderer!);
    await flush();

    const openAINameInput = findInputByLabel(
      openAIRenderer!.root,
      i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
    );
    await act(async () => {
      openAINameInput!.props.onChange({ target: { value: 'renamed-openai' } });
    });
    const saveOpenAIBtn = findButtonByText(
      openAIRenderer!.root,
      i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
    );
    await act(async () => {
      await saveOpenAIBtn!.props.onClick();
    });
    await flush();
    expect(mocks.updateOpenAIProvider).not.toHaveBeenCalled();

    // 3. Match present in latestList without v8Source -> carries row.raw.v8Source into updateClaudeConfig
    mocks.getClaudeConfigs.mockResolvedValue([
      {
        name: 'Target Claude',
        apiKey: 'sk-ant-target',
        baseUrl: 'https://claude.target.example.com',
        authIndex: 'auth-target',
      },
    ]);
    await act(async () => {
      await saveClaudeBtn!.props.onClick();
    });
    await flush();
    expect(mocks.updateClaudeConfig).toHaveBeenCalledWith(
      expect.objectContaining({ v8Source: v8ClaudeSource }),
      expect.objectContaining({ v8Source: v8ClaudeSource })
    );
  });

  it('preserves OpenAI key metadata across weight/proxy edits, hides per-key headers UI, and bypasses stale authIndex on edited apiKey', async () => {
    const markedEntries = [
      markPersistedOpenAIKeyEntry({ apiKey: '', weight: 1, proxyUrl: 'http://old-proxy:8080' }, 0),
    ];
    const rows = buildProviderRows({
      gemini: [],
      codex: [
        {
          name: 'Codex Stale Auth',
          apiKey: 'sk-old-codex',
          baseUrl: 'https://codex.example.com/v1',
          authIndex: 'auth-stale-codex',
          models: [{ name: 'gpt-5-codex' }],
        },
      ],
      claude: [],
      vertex: [],
      openai: [
        {
          name: 'keyless-openai',
          baseUrl: 'https://local-openai.example/v1',
          apiKeyEntries: markedEntries,
          models: [{ name: 'local-model' }],
        },
      ],
      usageByProvider: emptyUsageByProvider,
    });

    // 1. OpenAI metadata preservation + no per-key headers UI
    let openAIRenderer: ReactTestRenderer;
    await act(async () => {
      openAIRenderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[1]}
          initialTab="config"
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(openAIRenderer!);
    await flush();

    // Per-key headers expand button must not exist in OpenAI key cards
    const headerButtons = openAIRenderer!.root
      .findAllByType('button')
      .filter((btn) => extractText(btn).includes(i18n.t('common.custom_headers_label')));
    expect(headerButtons).toHaveLength(0);

    const weightInput = findInputByLabel(
      openAIRenderer!.root,
      i18n.t('ai_providers.weight_label')
    );
    const proxyInput = findInputByLabel(
      openAIRenderer!.root,
      i18n.t('ai_providers.proxy_url_label', { defaultValue: '代理' })
    );
    expect(weightInput).toBeDefined();
    expect(proxyInput).toBeDefined();

    await act(async () => {
      weightInput!.props.onChange({ target: { value: '5' } });
      proxyInput!.props.onChange({ target: { value: 'http://new-proxy:8080' } });
    });
    mocks.getOpenAIProviders.mockResolvedValue([rows[1].raw]);
    const saveOpenAIBtn = findButtonByText(
      openAIRenderer!.root,
      i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
    );
    await act(async () => {
      await saveOpenAIBtn!.props.onClick();
    });
    await flush();
    expect(mocks.updateOpenAIProvider).toHaveBeenCalledTimes(1);
    const savedOpenAIPayload = mocks.updateOpenAIProvider.mock.calls[0][2];
    expect(isPersistedOpenAIKeyEntry(savedOpenAIPayload.apiKeyEntries[0])).toBe(true);
    expect(getPersistedOpenAIKeyEntryIndex(savedOpenAIPayload.apiKeyEntries[0])).toBe(0);
    expect(savedOpenAIPayload.apiKeyEntries[0]).not.toHaveProperty('headers');

    // 2. Single-key edited apiKey bypasses stale authIndex during connectivity test & omits it on save
    let codexRenderer: ReactTestRenderer;
    await act(async () => {
      codexRenderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          initialTab="config"
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(codexRenderer!);
    await flush();

    const codexKeyInput = findInputByLabel(
      codexRenderer!.root,
      i18n.t('ai_providers.api_key_label', { defaultValue: 'API Key' })
    );
    await act(async () => {
      codexKeyInput!.props.onChange({ target: { value: 'sk-new-codex' } });
    });

    const connTab = findTabByText(
      codexRenderer!.root,
      i18n.t('ai_providers.drawer_tab_connectivity', { defaultValue: '测活诊断' })
    );
    await act(async () => {
      connTab!.props.onClick();
    });
    const runTestBtn = findButtonByText(
      codexRenderer!.root,
      i18n.t('ai_providers.send_test_request_btn', { defaultValue: '立即发送测试请求' })
    );
    await act(async () => {
      await runTestBtn!.props.onClick();
    });
    await flush();

    expect(mocks.apiCallRequest).toHaveBeenCalledWith(
      expect.objectContaining({
        authIndex: undefined,
        header: expect.objectContaining({
          Authorization: 'Bearer sk-new-codex',
        }),
      }),
      expect.any(Object)
    );

    mocks.getCodexConfigs.mockResolvedValue([rows[0].raw]);
    const saveCodexBtn = findButtonByText(
      codexRenderer!.root,
      i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
    );
    await act(async () => {
      await saveCodexBtn!.props.onClick();
    });
    await flush();
    expect(mocks.updateCodexConfig).toHaveBeenCalledWith(
      expect.objectContaining({ authIndex: 'auth-stale-codex' }),
      expect.objectContaining({
        apiKey: 'sk-new-codex',
        authIndex: undefined,
      })
    );
  });

  it('cancels stale connectivity responses on row switch, blocks close while saving, guards create-mode close, and retains results across same-row refresh', async () => {
    const rows = buildProviderRows({
      gemini: [],
      codex: [
        {
          name: 'Provider A',
          apiKey: 'sk-a',
          baseUrl: 'https://a.example.com/v1',
          models: [{ name: 'gpt-5' }],
        },
        {
          name: 'Provider B',
          apiKey: 'sk-b',
          baseUrl: 'https://b.example.com/v1',
          models: [{ name: 'gpt-5' }],
        },
      ],
      claude: [],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    });

    const deferredConnA = createDeferred<{ statusCode: number; body: string }>();
    mocks.apiCallRequest.mockImplementationOnce(() => deferredConnA.promise);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          initialTab="connectivity"
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(renderer!);
    await flush();

    const testBtn = findButtonByText(
      renderer!.root,
      i18n.t('ai_providers.send_test_request_btn', { defaultValue: '立即发送测试请求' })
    );
    act(() => {
      void testBtn!.props.onClick();
    });
    await flush();

    // Switch to Provider B while Provider A test is in flight
    await act(async () => {
      renderer!.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[1]}
          initialTab="connectivity"
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    await flush();

    await act(async () => {
      deferredConnA.resolve({ statusCode: 500, body: 'provider-a-boom' });
    });
    expect(extractText(renderer!.root)).not.toContain('provider-a-boom');
    expect(mocks.showNotification).not.toHaveBeenCalled();

    // Run successful connectivity test on Provider B, then refresh Provider B with a new row.raw reference
    mocks.apiCallRequest.mockResolvedValueOnce({ statusCode: 200, body: '{}' });
    const testBtnB = findButtonByText(
      renderer!.root,
      i18n.t('ai_providers.send_test_request_btn', { defaultValue: '立即发送测试请求' })
    );
    await act(async () => {
      await testBtnB!.props.onClick();
    });
    await flush();
    expect(extractText(renderer!.root)).toContain(i18n.t('ai_providers.codex_test_success'));

    const rowB = rows[1];
    if (rowB.kind !== 'codex') throw new Error('expected codex row');
    const refreshedRowB = { ...rowB, raw: { ...rowB.raw, weight: 3 } };
    await act(async () => {
      renderer!.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={refreshedRowB}
          initialTab="connectivity"
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    await flush();
    expect(extractText(renderer!.root)).toContain(i18n.t('ai_providers.codex_test_success'));

    // Block close while saving is true + guard create-mode onClose when drawer closes mid-request
    const deferredCreate = createDeferred<void>();
    mocks.createGeminiKey.mockImplementationOnce(() => deferredCreate.promise);
    const onCreateClose = vi.fn();
    let createRenderer: ReactTestRenderer;
    await act(async () => {
      createRenderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="create"
          row={null}
          createKind="gemini"
          usageByProvider={emptyUsageByProvider}
          onClose={onCreateClose}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(createRenderer!);
    await flush();
    const keyInput = findInputByLabel(
      createRenderer!.root,
      i18n.t('ai_providers.api_key_label', { defaultValue: 'API Key' })
    );
    await act(async () => {
      keyInput!.props.onChange({ target: { value: 'AIza-create' } });
    });
    const createSubmit = findButtonByText(
      createRenderer!.root,
      i18n.t('ai_providers.drawer_create_submit', { defaultValue: '创建配置' })
    );
    act(() => {
      void createSubmit!.props.onClick();
    });
    await flush();

    // Mid-save close attempt must be blocked
    await expect(mocks.drawerPropsRef.current?.onBeforeClose?.()).resolves.toBe(false);

    // Close drawer externally before create resolves -> stale create completion must not fire onClose
    await act(async () => {
      createRenderer!.update(
        <ProviderWorkspaceDrawer
          open={false}
          mode="create"
          row={null}
          createKind="gemini"
          usageByProvider={emptyUsageByProvider}
          onClose={onCreateClose}
          onSaved={vi.fn()}
        />
      );
    });
    await act(async () => {
      deferredCreate.resolve();
    });
    await flush();
    expect(onCreateClose).not.toHaveBeenCalled();
  });

  it('supports Claude cloak raw typing & extra fields, Vertex alias-less models, force-mapping, and filtered model edits', async () => {
    const rows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Cloak Claude',
          apiKey: 'sk-ant-cloak',
          baseUrl: 'https://claude.example.com',
          cloak: {
            mode: 'always',
            strictMode: true,
            sensitiveWords: ['secret-one'],
            sensitiveWordsReplace: { secret: '[REDACTED]' },
            sensitivePatternsReplace: [{ pattern: 'foo', replace: 'bar' }],
            cacheUserId: true,
          },
        },
      ],
      vertex: [
        {
          name: 'Vertex Pool',
          apiKey: 'vx-key',
          baseUrl: 'https://vertex.example.com',
          models: [
            {
              name: 'gemini-2.5-pro',
              displayName: 'Gemini 2.5 Pro',
              openapi: true,
              forceMapping: true,
            },
            { name: 'gemini-2.5-flash', alias: 'flash' },
            { name: 'claude-sonnet-4', alias: 'sonnet' },
            { name: 'gpt-4.1', alias: 'g41' },
            { name: 'o3-mini', alias: 'o3m' },
          ],
        },
      ],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    });

    // 1. Claude cloak newline/comma typing + extra fields preservation
    mocks.getClaudeConfigs.mockResolvedValue([rows[0].raw]);
    let claudeRenderer: ReactTestRenderer;
    await act(async () => {
      claudeRenderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(claudeRenderer!);
    await flush();

    const cloakTextarea = claudeRenderer!.root.findByType('textarea');
    await act(async () => {
      cloakTextarea.props.onChange({ target: { value: 'alpha,\nbeta\n' } });
    });
    expect(cloakTextarea.props.value).toBe('alpha,\nbeta\n');

    const saveClaudeBtn = findButtonByText(
      claudeRenderer!.root,
      i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
    );
    await act(async () => {
      await saveClaudeBtn!.props.onClick();
    });
    await flush();
    expect(mocks.updateClaudeConfig).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({
        cloak: expect.objectContaining({
          mode: 'always',
          strictMode: true,
          sensitiveWords: ['alpha', 'beta'],
          sensitiveWordsReplace: { secret: '[REDACTED]' },
          sensitivePatternsReplace: [{ pattern: 'foo', replace: 'bar' }],
          cacheUserId: true,
        }),
      })
    );

    // 2. Vertex force-mapping enabled, filtered add clears filter, filtered delete removes only visible row, alias optional
    mocks.getVertexConfigs.mockResolvedValue([rows[1].raw]);
    let vertexRenderer: ReactTestRenderer;
    await act(async () => {
      vertexRenderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[1]}
          initialTab="models"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(vertexRenderer!);
    await flush();

    expect(extractText(vertexRenderer!.root)).toContain(
      i18n.t('ai_providers.force_mapping_label')
    );

    const filterInput = vertexRenderer!.root
      .findAllByType('input')
      .find(
        (input) =>
          input.props.placeholder ===
          i18n.t('ai_providers.filter_models_placeholder', {
            defaultValue: '筛选模型名称或别名...',
          })
      );
    expect(filterInput).toBeDefined();
    await act(async () => {
      filterInput!.props.onChange({ target: { value: 'o3-mini' } });
    });

    // Click + Add model while filtered -> clears filter and appends blank row preserving all 5 entries
    const addModelBtn = findButtonByText(
      vertexRenderer!.root,
      i18n.t('ai_providers.add_model_row_btn', { defaultValue: '添加模型' })
    );
    await act(async () => {
      addModelBtn!.props.onClick();
    });
    expect(filterInput!.props.value).toBe('');

    const saveVertexBtn = findButtonByText(
      vertexRenderer!.root,
      i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
    );
    const modelNameInputs = vertexRenderer!.root
      .findAllByType('input')
      .filter(
        (input) => input.props.placeholder === i18n.t('ai_providers.vertex_model_name_placeholder')
      );
    const lastNameInput = modelNameInputs[modelNameInputs.length - 1];
    await act(async () => {
      lastNameInput.props.onChange({ target: { value: 'gemini-2.5-pro-preview' } });
    });
    await act(async () => {
      await saveVertexBtn!.props.onClick();
    });
    await flush();
    expect(mocks.updateVertexConfig).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({
        models: expect.arrayContaining([
          expect.objectContaining({
            name: 'gemini-2.5-pro',
            displayName: 'Gemini 2.5 Pro',
            openapi: true,
            forceMapping: true,
          }),
          expect.objectContaining({ name: 'gemini-2.5-pro-preview' }),
        ]),
      })
    );

    // Filter to 1 model, backspace name to '' and type a new name -> keeps the single filtered row intact instead of deleting it
    await act(async () => {
      filterInput!.props.onChange({ target: { value: 'o3-mini' } });
    });
    const filteredAliasInputs = vertexRenderer!.root
      .findAllByType('input')
      .filter(
        (input) => input.props.placeholder === i18n.t('ai_providers.vertex_model_alias_placeholder')
      );
    expect(filteredAliasInputs).toHaveLength(1);
    await act(async () => {
      filteredAliasInputs[0].props.onChange({ target: { value: '' } });
    });
    const filteredNameInputs = vertexRenderer!.root
      .findAllByType('input')
      .filter(
        (input) => input.props.placeholder === i18n.t('ai_providers.vertex_model_name_placeholder')
      );
    expect(filteredNameInputs).toHaveLength(1);
    await act(async () => {
      filteredNameInputs[0].props.onChange({ target: { value: '' } });
    });
    const afterBackspaceNameInputs = vertexRenderer!.root
      .findAllByType('input')
      .filter(
        (input) => input.props.placeholder === i18n.t('ai_providers.vertex_model_name_placeholder')
      );
    expect(afterBackspaceNameInputs).toHaveLength(1);
    expect(afterBackspaceNameInputs[0].props.value).toBe('');
    await act(async () => {
      afterBackspaceNameInputs[0].props.onChange({ target: { value: 'o3-mini-renamed' } });
    });
    await act(async () => {
      await saveVertexBtn!.props.onClick();
    });
    await flush();
    const savedAfterFilteredRename =
      mocks.updateVertexConfig.mock.calls[mocks.updateVertexConfig.mock.calls.length - 1][1]
        .models;
    expect(savedAfterFilteredRename.map((m: { name: string }) => m.name)).toContain(
      'o3-mini-renamed'
    );
    expect(savedAfterFilteredRename.map((m: { name: string }) => m.name)).not.toContain('o3-mini');
    expect(savedAfterFilteredRename.map((m: { name: string }) => m.name)).toEqual(
      expect.arrayContaining(['gemini-2.5-pro', 'gemini-2.5-flash', 'claude-sonnet-4', 'gpt-4.1'])
    );

    // Clicking the remove button on the single filtered row removes only that row while keeping unfiltered models intact
    await act(async () => {
      filterInput!.props.onChange({ target: { value: 'o3-mini-renamed' } });
    });
    const removeModelBtn = vertexRenderer!.root
      .findAllByType('button')
      .find((btn) => btn.props['aria-label'] === 'Remove' || btn.props.title === 'Remove');
    expect(removeModelBtn).toBeDefined();
    expect(removeModelBtn!.props.disabled).toBe(false);
    await act(async () => {
      removeModelBtn!.props.onClick();
    });
    await act(async () => {
      await saveVertexBtn!.props.onClick();
    });
    await flush();
    const savedAfterFilteredDelete =
      mocks.updateVertexConfig.mock.calls[mocks.updateVertexConfig.mock.calls.length - 1][1]
        .models;
    expect(savedAfterFilteredDelete.map((m: { name: string }) => m.name)).not.toContain(
      'o3-mini-renamed'
    );
    expect(savedAfterFilteredDelete.map((m: { name: string }) => m.name)).toEqual(
      expect.arrayContaining(['gemini-2.5-pro', 'gemini-2.5-flash', 'claude-sonnet-4', 'gpt-4.1'])
    );
  });

  it('wires ARIA tabpanel linkage & keyboard navigation, resets revealed keys on row switch, and disables Overview quick-enable when dirty', async () => {
    const rows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'First Claude',
          apiKey: 'sk-ant-first-secret',
          baseUrl: 'https://first.example.com',
          excludedModels: ['*'],
        },
        {
          name: 'Second Claude',
          apiKey: 'sk-ant-second-secret',
          baseUrl: 'https://second.example.com',
        },
      ],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          onToggleEnabled={vi.fn()}
        />
      );
    });
    renderers.push(renderer!);
    await flush();

    // Verify ARIA tab / tabpanel linkage and ArrowRight keyboard navigation
    const tabs = renderer!.root.findAll(
      (node) => node.type === 'button' && node.props.role === 'tab'
    );
    const overviewPanel = renderer!.root.find((node) => node.props.role === 'tabpanel');
    expect(tabs[0].props['aria-controls']).toBe(overviewPanel.props.id);
    expect(overviewPanel.props['aria-labelledby']).toBe(tabs[0].props.id);

    // Reveal key in Overview on Provider 1
    const revealBtn = renderer!.root
      .findAllByType('button')
      .find(
        (btn) =>
          btn.props['aria-label'] === i18n.t('common.reveal', { defaultValue: '显示明文' })
      );
    await act(async () => {
      revealBtn!.props.onClick();
    });
    expect(extractText(renderer!.root)).toContain('sk-ant-first-secret');

    // Switch to Provider 2 -> key reveal resets back to masked
    await act(async () => {
      renderer!.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[1]}
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          onToggleEnabled={vi.fn()}
        />
      );
    });
    await flush();
    expect(extractText(renderer!.root)).not.toContain('sk-ant-second-secret');

    // Switch back to Provider 1, dirty config via ArrowRight -> Config tab, then switch back to Overview -> quick-enable is disabled
    await act(async () => {
      renderer!.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          onToggleEnabled={vi.fn()}
        />
      );
    });
    await flush();
    const currentTabs = renderer!.root.findAll(
      (node) => node.type === 'button' && node.props.role === 'tab'
    );
    await act(async () => {
      currentTabs[0].props.onKeyDown({ key: 'ArrowRight', preventDefault: vi.fn() });
    });
    const configPanel = renderer!.root.find((node) => node.props.role === 'tabpanel');
    expect(configPanel.props['aria-labelledby']).toBe(currentTabs[1].props.id);

    const nameInput = findInputByLabel(
      renderer!.root,
      i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
    );
    await act(async () => {
      nameInput!.props.onChange({ target: { value: 'Dirty Name' } });
    });
    await act(async () => {
      currentTabs[1].props.onKeyDown({ key: 'ArrowLeft', preventDefault: vi.fn() });
    });
    const quickEnableBtn = findButtonByText(
      renderer!.root,
      i18n.t('ai_providers.drawer_quick_enable', { defaultValue: '立即启用' })
    );
    expect(quickEnableBtn!.props.disabled).toBe(true);
  });

  it('re-anchors OpenAI duplicate-name collisions and single-key auto-named saves across consecutive saves while keeping active config tab', async () => {
    // 1. OpenAI duplicate-name collision: Alpha -> Beta deduplicated to Beta-2, advancing ETag "h1" -> "h2" -> "h3"
    let serverOpenAI = [
      {
        name: 'Alpha',
        baseUrl: 'https://alpha.example/v1',
        apiKeyEntries: [{ apiKey: 'sk-alpha' }],
        v8Source: {
          family: 'openai-compatibility' as const,
          groupIndex: 0,
          group: { name: 'Alpha' },
          etag: '"h1"',
        },
      },
      {
        name: 'Beta',
        baseUrl: 'https://beta.example/v1',
        apiKeyEntries: [{ apiKey: 'sk-beta' }],
        v8Source: {
          family: 'openai-compatibility' as const,
          groupIndex: 1,
          group: { name: 'Beta' },
          etag: '"h1"',
        },
      },
    ];
    mocks.getOpenAIProviders.mockImplementation(async () => serverOpenAI);
    mocks.updateOpenAIProvider.mockImplementation(
      async (_origName: string, targetIdx: number, payload: { prefix?: string; baseUrl: string }) => {
        const nextEtag = serverOpenAI[0].v8Source.etag === '"h1"' ? '"h2"' : '"h3"';
        serverOpenAI = serverOpenAI.map((entry, idx) =>
          idx === targetIdx
            ? {
                ...entry,
                name: 'Beta-2',
                prefix: payload.prefix,
                v8Source: { ...entry.v8Source, group: { name: 'Beta-2' }, etag: nextEtag },
              }
            : { ...entry, v8Source: { ...entry.v8Source, etag: nextEtag } }
        );
        return { ok: true, etag: nextEtag };
      }
    );

    const buildOpenAIRows = () =>
      buildProviderRows({
        gemini: [],
        codex: [],
        claude: [],
        vertex: [],
        openai: serverOpenAI,
        usageByProvider: emptyUsageByProvider,
      });

    let openAIRenderer!: ReactTestRenderer;
    const onOpenAISaved = vi.fn(async () => {
      const nextRows = buildOpenAIRows();
      openAIRenderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={nextRows[0]}
          initialTab="overview"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={onOpenAISaved}
        />
      );
    });

    await act(async () => {
      openAIRenderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={buildOpenAIRows()[0]}
          initialTab="overview"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={onOpenAISaved}
        />
      );
    });
    renderers.push(openAIRenderer);
    await flush();

    // Switch from overview to config tab
    const configTab = findTabByText(
      openAIRenderer.root,
      i18n.t('ai_providers.drawer_tab_config', { defaultValue: '配置' })
    );
    await act(async () => {
      configTab!.props.onClick();
    });

    const openAINameInput = findInputByLabel(
      openAIRenderer.root,
      i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
    );
    await act(async () => {
      openAINameInput!.props.onChange({ target: { value: 'Beta' } });
    });

    const saveOpenAIBtn = findButtonByText(
      openAIRenderer.root,
      i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
    );
    await act(async () => {
      await saveOpenAIBtn!.props.onClick();
    });
    await flush();

    expect(onOpenAISaved).toHaveBeenCalledWith(
      'openai',
      expect.objectContaining({
        name: 'Beta-2',
        originalIndex: 0,
        v8Source: expect.objectContaining({ groupIndex: 0, etag: '"h2"' }),
      })
    );
    // Active tab stays on 'config' even though row.key changed from Alpha to Beta-2 and initialTab="overview"
    const activeTabAfterOpenAISave = openAIRenderer.root
      .findAll((node) => node.type === 'button' && node.props.role === 'tab')
      .find((btn) => btn.props['aria-selected'] === true);
    expect(extractText(activeTabAfterOpenAISave!)).toContain(
      i18n.t('ai_providers.drawer_tab_config', { defaultValue: '配置' })
    );
    expect(
      findInputByLabel(
        openAIRenderer.root,
        i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
      )?.props.value
    ).toBe('Beta-2');

    // Consecutive 2nd save updates prefix with fresh ETag "h2" and originalName "Beta-2"
    const openAIPrefixInput = findInputByLabel(
      openAIRenderer.root,
      i18n.t('ai_providers.prefix_label')
    );
    await act(async () => {
      openAIPrefixInput!.props.onChange({ target: { value: 'p2' } });
    });
    await act(async () => {
      await findButtonByText(
        openAIRenderer.root,
        i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
      )!.props.onClick();
    });
    await flush();

    expect(mocks.updateOpenAIProvider).toHaveBeenLastCalledWith(
      'Beta-2',
      0,
      expect.objectContaining({
        name: 'Beta-2',
        prefix: 'p2',
        v8Source: expect.objectContaining({ groupIndex: 0, etag: '"h2"' }),
      })
    );

    // 2. Single-key cleared-name auto-naming: "Old Name" -> "" -> "claude-1" with consecutive save & kept config tab
    const initialClaudeV8 = {
      family: 'claude' as const,
      groupIndex: 0,
      keyIndex: 0,
      group: {},
      etag: '"h1"',
    };
    let serverClaude: ProviderKeyConfig[] = [
      {
        name: 'Old Name',
        apiKey: 'sk-old',
        baseUrl: 'https://claude.example.com',
        v8Source: initialClaudeV8,
      },
    ];
    mocks.getClaudeConfigs.mockImplementation(async () => serverClaude);
    mocks.updateClaudeConfig.mockImplementation(
      async (_existing: unknown, payload: { apiKey: string; prefix?: string }) => {
        const currentV8 = serverClaude[0]?.v8Source ?? initialClaudeV8;
        const nextEtag = currentV8.etag === '"h1"' ? '"h2"' : '"h3"';
        serverClaude = [
          {
            ...serverClaude[0],
            name: 'claude-1',
            apiKey: payload.apiKey,
            prefix: payload.prefix,
            v8Source: { ...currentV8, etag: nextEtag },
          },
        ];
        return { ok: true, etag: nextEtag };
      }
    );

    const buildClaudeRows = () =>
      buildProviderRows({
        gemini: [],
        codex: [],
        claude: serverClaude,
        vertex: [],
        openai: [],
        usageByProvider: emptyUsageByProvider,
      });

    let claudeRenderer!: ReactTestRenderer;
    const onClaudeSaved = vi.fn(async () => {
      const nextRows = buildClaudeRows();
      claudeRenderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={nextRows[0]}
          initialTab="overview"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={onClaudeSaved}
        />
      );
    });

    await act(async () => {
      claudeRenderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={buildClaudeRows()[0]}
          initialTab="overview"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={onClaudeSaved}
        />
      );
    });
    renderers.push(claudeRenderer);
    await flush();

    await act(async () => {
      findTabByText(
        claudeRenderer.root,
        i18n.t('ai_providers.drawer_tab_config', { defaultValue: '配置' })
      )!.props.onClick();
    });
    await act(async () => {
      findInputByLabel(
        claudeRenderer.root,
        i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
      )!.props.onChange({ target: { value: '' } });
      findInputByLabel(
        claudeRenderer.root,
        i18n.t('ai_providers.api_key_label', { defaultValue: 'API Key' })
      )!.props.onChange({ target: { value: 'sk-rotated' } });
    });
    await act(async () => {
      await findButtonByText(
        claudeRenderer.root,
        i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
      )!.props.onClick();
    });
    await flush();

    expect(onClaudeSaved).toHaveBeenCalledWith(
      'claude',
      expect.objectContaining({
        name: 'claude-1',
        apiKey: 'sk-rotated',
        v8Source: expect.objectContaining({ groupIndex: 0, keyIndex: 0, etag: '"h2"' }),
      })
    );
    const activeTabAfterClaudeSave = claudeRenderer.root
      .findAll((node) => node.type === 'button' && node.props.role === 'tab')
      .find((btn) => btn.props['aria-selected'] === true);
    expect(extractText(activeTabAfterClaudeSave!)).toContain(
      i18n.t('ai_providers.drawer_tab_config', { defaultValue: '配置' })
    );

    // Consecutive 2nd save on Claude sends fresh ETag "h2"
    await act(async () => {
      findInputByLabel(claudeRenderer.root, i18n.t('ai_providers.prefix_label'))!.props.onChange({
        target: { value: 'c2' },
      });
    });
    await act(async () => {
      await findButtonByText(
        claudeRenderer.root,
        i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
      )!.props.onClick();
    });
    await flush();

    expect(mocks.updateClaudeConfig).toHaveBeenLastCalledWith(
      expect.objectContaining({
        name: 'claude-1',
        v8Source: expect.objectContaining({ etag: '"h2"' }),
      }),
      expect.objectContaining({
        name: 'claude-1',
        prefix: 'c2',
        v8Source: expect.objectContaining({ etag: '"h2"' }),
      })
    );
  });

  it('disables OpenAI proxyUrl input and Claude cloakSensitiveWordsText textarea when actionsDisabled is true', async () => {
    const rows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Claude Cloak',
          apiKey: 'sk-ant-cloak',
          cloak: { mode: 'always', sensitiveWords: ['secret-word'] },
        },
      ],
      vertex: [],
      openai: [
        {
          name: 'OpenAI Proxy',
          baseUrl: 'https://api.openai.com/v1',
          apiKeyEntries: [{ apiKey: 'sk-oa', proxyUrl: 'http://127.0.0.1:7890' }],
        },
      ],
      usageByProvider: emptyUsageByProvider,
    });

    let oaRenderer!: ReactTestRenderer;
    await act(async () => {
      oaRenderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[1]}
          initialTab="config"
          actionsDisabled
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(oaRenderer);
    await flush();

    const proxyInputs = oaRenderer.root
      .findAllByType('input')
      .filter((input) => input.props.value === 'http://127.0.0.1:7890');
    expect(proxyInputs).toHaveLength(1);
    expect(proxyInputs[0].props.disabled).toBe(true);

    let claudeRenderer!: ReactTestRenderer;
    await act(async () => {
      claudeRenderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          initialTab="config"
          actionsDisabled
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(claudeRenderer);
    await flush();

    const cloakTextareas = claudeRenderer.root
      .findAllByType('textarea')
      .filter((ta) => String(ta.props.value ?? '').includes('secret-word'));
    expect(cloakTextareas).toHaveLength(1);
    expect(cloakTextareas[0].props.disabled).toBe(true);
  });

  it('preserves committed values and fresh ETag across stale pre-save row updates and resetForm when post-save refresh fails', async () => {
    const preSaveV8Source = {
      family: 'claude' as const,
      groupIndex: 0,
      keyIndex: 0,
      group: { name: 'Pre-Save Claude' },
      etag: '"etag-1"',
    };
    let serverClaude: ProviderKeyConfig[] = [
      {
        name: 'Pre-Save Claude',
        apiKey: 'sk-ant-1',
        baseUrl: 'https://claude.example.com',
        prefix: 'p1',
        v8Source: preSaveV8Source,
      },
    ];
    const staleRow = buildProviderRows({
      gemini: [],
      codex: [],
      claude: serverClaude,
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    })[0];

    mocks.getClaudeConfigs.mockImplementation(async () => serverClaude);
    mocks.updateClaudeConfig.mockImplementation(
      async (_existing: unknown, payload: { name?: string; prefix?: string }) => {
        const currentV8 = serverClaude[0]?.v8Source ?? preSaveV8Source;
        const nextEtag = currentV8.etag === '"etag-1"' ? '"etag-2"' : '"etag-3"';
        serverClaude = [
          {
            ...serverClaude[0],
            name: payload.name ?? serverClaude[0].name,
            prefix: payload.prefix,
            v8Source: { ...currentV8, etag: nextEtag },
          },
        ];
        return { ok: true, etag: nextEtag };
      }
    );

    let renderer!: ReactTestRenderer;
    const onSaved = vi.fn();
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={staleRow}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={onSaved}
        />
      );
    });
    renderers.push(renderer);
    await flush();

    await act(async () => {
      findInputByLabel(
        renderer.root,
        i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
      )!.props.onChange({ target: { value: 'Committed Claude' } });
    });

    await act(async () => {
      await findButtonByText(
        renderer.root,
        i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
      )!.props.onClick();
    });
    await flush();

    expect(onSaved).toHaveBeenCalledTimes(1);
    expect(
      findInputByLabel(
        renderer.root,
        i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
      )?.props.value
    ).toBe('Committed Claude');

    // Even if parent passes a cloned pre-save row (stale business fields + rotated ETag),
    // rawChangedWhileClean must NOT clobber the committed snapshot.
    const staleClonedRow =
      staleRow.kind === 'claude'
        ? {
            ...staleRow,
            raw: {
              ...staleRow.raw,
              v8Source: { ...preSaveV8Source, etag: '"etag-2"' },
            },
          }
        : staleRow;
    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={staleClonedRow}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={onSaved}
        />
      );
    });
    await flush();

    expect(
      findInputByLabel(
        renderer.root,
        i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
      )?.props.value
    ).toBe('Committed Claude');

    // Edit another field while row is still unrefreshed, then click footer Reset ->
    // must reset back to the committed post-save snapshot, NOT pre-save stale row!
    await act(async () => {
      findInputByLabel(renderer.root, i18n.t('ai_providers.prefix_label'))!.props.onChange({
        target: { value: 'dirty-prefix' },
      });
    });
    const resetButtons = renderer.root
      .findAllByType('button')
      .filter((btn) => extractText(btn).trim().includes(i18n.t('common.reset', { defaultValue: '重置' })));
    const footerResetBtn = resetButtons[resetButtons.length - 1];
    expect(footerResetBtn).toBeDefined();
    await act(async () => {
      footerResetBtn.props.onClick();
    });
    await flush();

    expect(
      findInputByLabel(
        renderer.root,
        i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
      )?.props.value
    ).toBe('Committed Claude');
    expect(
      findInputByLabel(renderer.root, i18n.t('ai_providers.prefix_label'))?.props.value
    ).toBe('p1');

    // Subsequent save still uses the committed name and rotated ETag "etag-2"
    await act(async () => {
      findInputByLabel(renderer.root, i18n.t('ai_providers.prefix_label'))!.props.onChange({
        target: { value: 'p2' },
      });
    });
    await act(async () => {
      await findButtonByText(
        renderer.root,
        i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
      )!.props.onClick();
    });
    await flush();

    expect(mocks.updateClaudeConfig).toHaveBeenLastCalledWith(
      expect.objectContaining({
        name: 'Committed Claude',
        v8Source: expect.objectContaining({ etag: '"etag-2"' }),
      }),
      expect.objectContaining({
        name: 'Committed Claude',
        prefix: 'p2',
        v8Source: expect.objectContaining({ etag: '"etag-2"' }),
      })
    );

    // Once row genuinely refreshes from server, rawChangedWhileClean accepts the refreshed item
    const refreshedRow = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Server Refreshed Claude',
          apiKey: 'sk-ant-1',
          baseUrl: 'https://claude.example.com',
          prefix: 'p2',
          v8Source: { ...preSaveV8Source, etag: '"etag-3"' },
        },
      ],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    })[0];
    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={refreshedRow}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={onSaved}
        />
      );
    });
    await flush();
    expect(
      findInputByLabel(
        renderer.root,
        i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
      )?.props.value
    ).toBe('Server Refreshed Claude');
  });

  it('advances v8Source.etag on a dirty drawer only when non-ETag fields remain unchanged and saves without 412', async () => {
    const initialV8 = {
      family: 'claude' as const,
      groupIndex: 0,
      keyIndex: 0,
      group: { name: 'Claude Alpha' },
      etag: '"etag-1"',
    };
    let serverClaude: ProviderKeyConfig[] = [
      {
        name: 'Claude Alpha',
        apiKey: 'sk-ant-alpha',
        baseUrl: 'https://claude.example.com',
        v8Source: initialV8,
      },
    ];
    mocks.getClaudeConfigs.mockImplementation(async () => serverClaude);
    mocks.updateClaudeConfig.mockImplementation(
      async (existing: ProviderKeyConfig, payload: ProviderKeyConfig) => {
        const serverEtag = serverClaude[0]?.v8Source?.etag;
        if (existing.v8Source?.etag !== serverEtag) {
          throw Object.assign(new Error('Precondition Failed'), { status: 412 });
        }
        const nextEtag = '"etag-3"';
        serverClaude = [
          {
            ...serverClaude[0],
            ...payload,
            v8Source: { ...initialV8, etag: nextEtag },
          },
        ];
        return { ok: true, etag: nextEtag };
      }
    );

    const buildClaudeRow = (override?: Partial<ProviderKeyConfig>) =>
      buildProviderRows({
        gemini: [],
        codex: [],
        claude: [{ ...serverClaude[0], ...override }],
        vertex: [],
        openai: [],
        usageByProvider: emptyUsageByProvider,
      })[0];

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={buildClaudeRow()}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(renderer);
    await flush();

    // Dirty the drawer
    await act(async () => {
      findInputByLabel(
        renderer.root,
        i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
      )!.props.onChange({ target: { value: 'Dirty Local Name' } });
    });

    // Concurrent mutation in another family rotates global ETag "etag-1" -> "etag-2" while row fields stay identical
    serverClaude = [{ ...serverClaude[0], v8Source: { ...initialV8, etag: '"etag-2"' } }];
    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={buildClaudeRow()}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    await flush();

    // Dirty edit must be preserved
    expect(
      findInputByLabel(
        renderer.root,
        i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
      )?.props.value
    ).toBe('Dirty Local Name');

    // Save sends updated ETag "etag-2" and succeeds without 412
    await act(async () => {
      await findButtonByText(
        renderer.root,
        i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
      )!.props.onClick();
    });
    await flush();

    expect(mocks.updateClaudeConfig).toHaveBeenCalledTimes(1);
    expect(mocks.updateClaudeConfig).toHaveBeenCalledWith(
      expect.objectContaining({ v8Source: expect.objectContaining({ etag: '"etag-2"' }) }),
      expect.objectContaining({
        name: 'Dirty Local Name',
        v8Source: expect.objectContaining({ etag: '"etag-2"' }),
      })
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(expect.any(String), 'success');

    // Now verify negative guard: if a non-ETag field on this row ALSO changed on server alongside ETag rotation,
    // ETag must NOT be silently advanced while dirty (412 conflict protection preserved).
    const postSaveRow = buildClaudeRow();
    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={postSaveRow}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    await flush();

    await act(async () => {
      findInputByLabel(renderer.root, i18n.t('ai_providers.prefix_label'))!.props.onChange({
        target: { value: 'local-conflict-prefix' },
      });
    });

    serverClaude = [
      {
        ...serverClaude[0],
        prefix: 'server-other-edit',
        v8Source: { ...initialV8, etag: '"etag-4"' },
      },
    ];
    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={buildClaudeRow()}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    await flush();

    await act(async () => {
      await findButtonByText(
        renderer.root,
        i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
      )!.props.onClick();
    });
    await flush();

    expect(mocks.updateClaudeConfig).toHaveBeenLastCalledWith(
      expect.objectContaining({ v8Source: expect.objectContaining({ etag: '"etag-3"' }) }),
      expect.objectContaining({ v8Source: expect.objectContaining({ etag: '"etag-3"' }) })
    );
    expect(mocks.showNotification).toHaveBeenLastCalledWith(
      expect.stringContaining('Precondition Failed'),
      'error'
    );

    // Also verify OpenAI dirty drawer advances v8Source.etag when non-ETag fields remain unchanged
    const openAIInitialV8 = {
      family: 'openai-compatibility' as const,
      groupIndex: 0,
      group: { name: 'OpenAI Alpha' },
      etag: '"oa-1"',
    };
    let serverOpenAI = [
      {
        name: 'OpenAI Alpha',
        baseUrl: 'https://oa.example.com/v1',
        apiKeyEntries: [{ apiKey: 'sk-oa-1' }],
        v8Source: openAIInitialV8,
      },
    ];
    const buildOpenAIRow = () =>
      buildProviderRows({
        gemini: [],
        codex: [],
        claude: [],
        vertex: [],
        openai: serverOpenAI,
        usageByProvider: emptyUsageByProvider,
      })[0];

    mocks.getOpenAIProviders.mockImplementation(async () => serverOpenAI);
    mocks.updateOpenAIProvider.mockImplementation(
      async (_origName: string, _idx: number, payload: { v8Source?: { etag?: string } }) => {
        if (payload.v8Source?.etag !== serverOpenAI[0].v8Source.etag) {
          throw Object.assign(new Error('Precondition Failed'), { status: 412 });
        }
        serverOpenAI = [{ ...serverOpenAI[0], v8Source: { ...openAIInitialV8, etag: '"oa-3"' } }];
        return { ok: true, etag: '"oa-3"' };
      }
    );

    let oaRenderer!: ReactTestRenderer;
    await act(async () => {
      oaRenderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={buildOpenAIRow()}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(oaRenderer);
    await flush();

    await act(async () => {
      findInputByLabel(oaRenderer.root, i18n.t('ai_providers.prefix_label'))!.props.onChange({
        target: { value: 'oa-dirty' },
      });
    });

    serverOpenAI = [{ ...serverOpenAI[0], v8Source: { ...openAIInitialV8, etag: '"oa-2"' } }];
    await act(async () => {
      oaRenderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={buildOpenAIRow()}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    await flush();

    await act(async () => {
      await findButtonByText(
        oaRenderer.root,
        i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
      )!.props.onClick();
    });
    await flush();

    expect(mocks.updateOpenAIProvider).toHaveBeenCalledWith(
      'OpenAI Alpha',
      0,
      expect.objectContaining({
        prefix: 'oa-dirty',
        v8Source: expect.objectContaining({ etag: '"oa-2"' }),
      })
    );
  });

  it('resets activeTab back to initialTab when closing and reopening the same row or create drawer', async () => {
    const row = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Reopen Claude',
          apiKey: 'sk-ant-reopen',
          baseUrl: 'https://reopen.example.com',
          v8Source: {
            family: 'claude',
            groupIndex: 0,
            keyIndex: 0,
            group: {},
            etag: '"etag-1"',
          },
        },
      ],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    })[0];

    mocks.getClaudeConfigs.mockResolvedValue([row.raw]);
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={row}
          initialTab="overview"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(renderer);
    await flush();

    // Switch to Config tab and save (sets tabState.reanchor), then switch to Models tab
    await act(async () => {
      findTabByText(
        renderer.root,
        i18n.t('ai_providers.drawer_tab_config', { defaultValue: '配置' })
      )!.props.onClick();
    });
    await act(async () => {
      findInputByLabel(renderer.root, i18n.t('ai_providers.prefix_label'))!.props.onChange({
        target: { value: 'p-reopen' },
      });
    });
    await act(async () => {
      await findButtonByText(
        renderer.root,
        i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
      )!.props.onClick();
    });
    await flush();

    // Close drawer (open=false), then reopen the exact same row -> must reset back to initialTab="overview"
    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open={false}
          mode="view-edit"
          row={row}
          initialTab="overview"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={row}
          initialTab="overview"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    await flush();

    const selectedTabAfterReopen = renderer.root
      .findAll((node) => node.type === 'button' && node.props.role === 'tab')
      .find((btn) => btn.props['aria-selected'] === true);
    expect(extractText(selectedTabAfterReopen!)).toContain(
      i18n.t('ai_providers.drawer_tab_overview', { defaultValue: '概览' })
    );

    // Create mode: switch to Models tab, save (triggers onClose), then reopen create -> resets back to config
    let createOpen = true;
    const handleCreateClose = vi.fn(() => {
      createOpen = false;
    });
    let createRenderer!: ReactTestRenderer;
    await act(async () => {
      createRenderer = create(
        <ProviderWorkspaceDrawer
          open={createOpen}
          mode="create"
          row={null}
          createKind="claude"
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={handleCreateClose}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(createRenderer);
    await flush();

    await act(async () => {
      findInputByLabel(
        createRenderer.root,
        i18n.t('ai_providers.api_key_label', { defaultValue: 'API Key' })
      )!.props.onChange({ target: { value: 'sk-create-1' } });
      findInputByLabel(
        createRenderer.root,
        i18n.t('ai_providers.claude_add_modal_url_label')
      )!.props.onChange({ target: { value: 'https://claude-create.example.com' } });
    });
    await act(async () => {
      findTabByText(
        createRenderer.root,
        i18n.t('ai_providers.drawer_tab_models', { defaultValue: '模型' })
      )!.props.onClick();
    });
    expect(
      extractText(
        createRenderer.root
          .findAll(
            (node) =>
              node.type === 'button' &&
              node.props.role === 'tab' &&
              String(node.props['aria-controls'] ?? '').includes('-panel-')
          )
          .find((btn) => btn.props['aria-selected'] === true)!
      )
    ).toContain(i18n.t('ai_providers.drawer_tab_models', { defaultValue: '模型' }));

    await act(async () => {
      await findButtonByText(
        createRenderer.root,
        i18n.t('ai_providers.drawer_create_submit', { defaultValue: '创建配置' })
      )!.props.onClick();
    });
    await flush();
    expect(handleCreateClose).toHaveBeenCalledTimes(1);

    await act(async () => {
      createRenderer.update(
        <ProviderWorkspaceDrawer
          open={false}
          mode="create"
          row={null}
          createKind="claude"
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={handleCreateClose}
          onSaved={vi.fn()}
        />
      );
    });
    await act(async () => {
      createRenderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="create"
          row={null}
          createKind="claude"
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={handleCreateClose}
          onSaved={vi.fn()}
        />
      );
    });
    await flush();

    const selectedCreateTabAfterReopen = createRenderer.root
      .findAll(
        (node) =>
          node.type === 'button' &&
          node.props.role === 'tab' &&
          String(node.props['aria-controls'] ?? '').includes('-panel-')
      )
      .find((btn) => btn.props['aria-selected'] === true);
    expect(extractText(selectedCreateTabAfterReopen!)).toContain(
      i18n.t('ai_providers.drawer_tab_config', { defaultValue: '配置' })
    );
  });

  it('clears ProviderModelsTab modelFilter and pinnedEditOrigIndex when clicking drawer footer Reset', async () => {
    const row = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [],
      vertex: [
        {
          name: 'Vertex Filter Reset',
          apiKey: 'vx-key-1',
          baseUrl: 'https://vertex.example.com',
          models: [
            { name: 'gemini-2.5-pro', alias: 'pro' },
            { name: 'gemini-2.5-flash', alias: 'flash' },
            { name: 'claude-sonnet-4', alias: 'sonnet' },
            { name: 'gpt-4.1', alias: 'gpt41' },
            { name: 'o3-mini', alias: 'o3m' },
          ],
        },
      ],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    })[0];

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={row}
          initialTab="models"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(renderer);
    await flush();

    const filterPlaceholder = i18n.t('ai_providers.filter_models_placeholder', {
      defaultValue: '筛选模型名称或别名...',
    });
    const filterInput = renderer.root
      .findAllByType('input')
      .find((input) => input.props.placeholder === filterPlaceholder);
    expect(filterInput).toBeDefined();

    // Filter to o3-mini and edit its name so it stops matching 'o3' (pins pinnedEditOrigIndex = 4)
    await act(async () => {
      filterInput!.props.onChange({ target: { value: 'o3' } });
    });
    const filteredNameInputs = renderer.root
      .findAllByType('input')
      .filter(
        (input) => input.props.placeholder === i18n.t('ai_providers.vertex_model_name_placeholder')
      );
    expect(filteredNameInputs).toHaveLength(1);
    await act(async () => {
      filteredNameInputs[0].props.onChange({ target: { value: 'custom-unmatched-name' } });
    });
    // Because index 4 is pinned, the edited entry stays visible even though it no longer matches 'o3'
    expect(
      renderer.root
        .findAllByType('input')
        .filter(
          (input) =>
            input.props.placeholder === i18n.t('ai_providers.vertex_model_name_placeholder')
        )
        .map((input) => input.props.value)
    ).toEqual(['custom-unmatched-name']);

    // Click drawer footer Reset (the last Reset button in DOM, located in drawerFooterRight)
    const resetButtons = renderer.root
      .findAllByType('button')
      .filter((btn) =>
        extractText(btn).trim().includes(i18n.t('common.reset', { defaultValue: '重置' }))
      );
    expect(resetButtons).toHaveLength(1);
    const footerResetBtn = resetButtons[resetButtons.length - 1];
    await act(async () => {
      footerResetBtn.props.onClick();
    });
    await flush();

    // modelFilter must be cleared and all 5 original models restored
    const filterInputAfterReset = renderer.root
      .findAllByType('input')
      .find((input) => input.props.placeholder === filterPlaceholder);
    expect(filterInputAfterReset?.props.value).toBe('');
    expect(
      renderer.root
        .findAllByType('input')
        .filter(
          (input) =>
            input.props.placeholder === i18n.t('ai_providers.vertex_model_name_placeholder')
        )
        .map((input) => input.props.value)
    ).toEqual(['gemini-2.5-pro', 'gemini-2.5-flash', 'claude-sonnet-4', 'gpt-4.1', 'o3-mini']);

    // Re-applying filter 'flash' must NOT still pin index 4 ('o3-mini')
    await act(async () => {
      filterInputAfterReset!.props.onChange({ target: { value: 'flash' } });
    });
    expect(
      renderer.root
        .findAllByType('input')
        .filter(
          (input) =>
            input.props.placeholder === i18n.t('ai_providers.vertex_model_name_placeholder')
        )
        .map((input) => input.props.value)
    ).toEqual(['gemini-2.5-flash']);

    // Also verify switching row while on Models tab resets modelFilter & pinnedEditOrigIndex
    await act(async () => {
      filterInputAfterReset!.props.onChange({ target: { value: 'o3' } });
    });
    const secondRow = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [],
      vertex: [
        {
          name: 'Vertex Second Row',
          apiKey: 'vx-key-2',
          baseUrl: 'https://vertex2.example.com',
          models: [
            { name: 'alpha-model', alias: 'alpha' },
            { name: 'beta-model', alias: 'beta' },
          ],
        },
      ],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    })[0];
    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={secondRow}
          initialTab="models"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    await flush();
    expect(
      renderer.root
        .findAllByType('input')
        .filter(
          (input) =>
            input.props.placeholder === i18n.t('ai_providers.vertex_model_name_placeholder')
        )
        .map((input) => input.props.value)
    ).toEqual(['alpha-model', 'beta-model']);
  });
  it('invokes onSaved before updateConfigValue/clearCache and skips store cache eviction when onSaved returns true', async () => {
    const callOrder: string[] = [];
    const initialRow = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [],
      vertex: [],
      openai: [
        {
          name: 'OldOpenAI',
          baseUrl: 'https://oa.example.com/v1',
          apiKeyEntries: [{ apiKey: 'sk-oa-1' }],
          v8Source: { family: 'openai-compatibility', groupIndex: 0, group: {}, etag: '"etag-1"' },
        },
      ],
      usageByProvider: emptyUsageByProvider,
    })[0];

    mocks.getOpenAIProviders
      .mockResolvedValueOnce([initialRow.raw])
      .mockResolvedValueOnce([
        {
          name: 'NewOpenAI',
          baseUrl: 'https://oa.example.com/v1',
          apiKeyEntries: [{ apiKey: 'sk-oa-1' }],
          v8Source: { family: 'openai-compatibility', groupIndex: 0, group: {}, etag: '"etag-2"' },
        },
      ]);
    mocks.updateConfigValue.mockImplementation(() => {
      callOrder.push('updateConfigValue');
    });
    mocks.clearCache.mockImplementation(() => {
      callOrder.push('clearCache');
    });

    const onSavedManaged = vi.fn(async () => {
      callOrder.push('onSavedManaged');
      return true;
    });

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={initialRow}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={onSavedManaged}
        />
      );
    });
    renderers.push(renderer);
    await flush();

    await act(async () => {
      findInputByLabel(renderer.root, i18n.t('ai_providers.name_label'))!.props.onChange({
        target: { value: 'NewOpenAI' },
      });
    });
    await act(async () => {
      await findButtonByText(
        renderer.root,
        i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
      )!.props.onClick();
    });
    await flush();

    expect(callOrder).toEqual(['onSavedManaged']);

    // Standalone caller returning void still falls back to updateConfigValue + clearCache AFTER onSaved
    callOrder.length = 0;
    const onSavedStandalone = vi.fn(async () => {
      callOrder.push('onSavedStandalone');
    });
    const rowStandalone = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'ClaudeOne',
          apiKey: 'sk-c-1',
          baseUrl: 'https://claude.example.com',
          v8Source: { family: 'claude', groupIndex: 0, keyIndex: 0, group: {}, etag: '"etag-1"' },
        },
      ],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    })[0];
    mocks.getClaudeConfigs
      .mockResolvedValueOnce([rowStandalone.raw])
      .mockResolvedValueOnce([
        {
          name: 'ClaudeOne',
          apiKey: 'sk-c-2',
          baseUrl: 'https://claude.example.com',
          v8Source: { family: 'claude', groupIndex: 0, keyIndex: 0, group: {}, etag: '"etag-2"' },
        },
      ]);

    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rowStandalone}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={onSavedStandalone}
        />
      );
    });
    await flush();

    await act(async () => {
      findInputByLabel(
        renderer.root,
        i18n.t('ai_providers.api_key_label', { defaultValue: 'API Key' })
      )!.props.onChange({ target: { value: 'sk-c-2' } });
    });
    await act(async () => {
      await findButtonByText(
        renderer.root,
        i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
      )!.props.onClick();
    });
    await flush();

    expect(callOrder).toEqual(['onSavedStandalone', 'updateConfigValue', 'clearCache']);
  });

  it('preserves dirty edits, activeTab, and ProviderModelsTab filter across sibling-deletion index shift and saves cleanly at new index', async () => {
    const buildRows = (items: ProviderKeyConfig[]) =>
      buildProviderRows({
        gemini: [],
        codex: [],
        claude: items,
        vertex: [],
        openai: [],
        usageByProvider: emptyUsageByProvider,
      });

    const initialItems: ProviderKeyConfig[] = [
      {
        name: 'Claude Sibling 0',
        apiKey: 'sk-sibling-0',
        baseUrl: 'https://c0.example.com',
        v8Source: { family: 'claude', groupIndex: 0, keyIndex: 0, group: {}, etag: '"etag-1"' },
      },
      {
        name: 'Claude Target 1',
        apiKey: 'sk-target-1',
        baseUrl: 'https://c1.example.com',
        models: [
          { name: 'claude-3-5-sonnet', alias: 'sonnet' },
          { name: 'claude-3-5-haiku', alias: 'haiku' },
          { name: 'claude-3-7-sonnet', alias: 'sonnet-37' },
          { name: 'claude-4-opus', alias: 'opus' },
          { name: 'claude-4-sonnet', alias: 'sonnet-4' },
        ],
        v8Source: { family: 'claude', groupIndex: 1, keyIndex: 0, group: {}, etag: '"etag-1"' },
      },
    ];
    const shiftedItems: ProviderKeyConfig[] = [
      {
        ...initialItems[1],
        v8Source: { family: 'claude', groupIndex: 0, keyIndex: 0, group: {}, etag: '"etag-2"' },
      },
    ];

    const initialRows = buildRows(initialItems);
    const shiftedRows = buildRows(shiftedItems);
    expect(initialRows[1].key).toMatch(/::1$/);
    expect(shiftedRows[0].key).toMatch(/::0$/);

    mocks.getClaudeConfigs.mockResolvedValue(shiftedItems);
    const onSaved = vi.fn();

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={initialRows[1]}
          initialTab="overview"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={onSaved}
        />
      );
    });
    renderers.push(renderer);
    await flush();

    // Switch to Config tab and create dirty edit, then shift row 1 -> 0
    await act(async () => {
      findTabByText(
        renderer.root,
        i18n.t('ai_providers.drawer_tab_config', { defaultValue: '配置' })
      )!.props.onClick();
    });
    await act(async () => {
      findInputByLabel(renderer.root, i18n.t('ai_providers.prefix_label'))!.props.onChange({
        target: { value: 'dirty-shift-prefix' },
      });
    });

    // Row 0 deleted externally: Target 1 shifts from ::1 to ::0
    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={shiftedRows[0]}
          initialTab="overview"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={onSaved}
        />
      );
    });
    await flush();

    // Active tab stays Config and dirty prefix edit is preserved
    const selectedTab = renderer.root
      .findAll((node) => node.type === 'button' && node.props.role === 'tab')
      .find((btn) => btn.props['aria-selected'] === true);
    expect(extractText(selectedTab!)).toContain(
      i18n.t('ai_providers.drawer_tab_config', { defaultValue: '配置' })
    );
    expect(findInputByLabel(renderer.root, i18n.t('ai_providers.prefix_label'))?.props.value).toBe(
      'dirty-shift-prefix'
    );

    // Also verify staying on Models tab across an index shift preserves ProviderModelsTab filter & dirty edit
    await act(async () => {
      findTabByText(
        renderer.root,
        i18n.t('ai_providers.drawer_tab_models', { defaultValue: '模型' })
      )!.props.onClick();
    });
    const filterPlaceholder = i18n.t('ai_providers.filter_models_placeholder', {
      defaultValue: '筛选模型名称或别名...',
    });
    await act(async () => {
      renderer.root
        .findAllByType('input')
        .find((input) => input.props.placeholder === filterPlaceholder)!
        .props.onChange({ target: { value: 'sonnet' } });
    });
    const shiftedBackRows = buildRows([
      {
        name: 'Claude Sibling 0 Readded',
        apiKey: 'sk-sibling-0b',
        baseUrl: 'https://c0b.example.com',
        v8Source: { family: 'claude', groupIndex: 0, keyIndex: 0, group: {}, etag: '"etag-3"' },
      },
      {
        ...initialItems[1],
        v8Source: { family: 'claude', groupIndex: 1, keyIndex: 0, group: {}, etag: '"etag-3"' },
      },
    ]);
    mocks.getClaudeConfigs.mockResolvedValue([shiftedBackRows[0].raw, shiftedBackRows[1].raw]);
    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={shiftedBackRows[1]}
          initialTab="overview"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={onSaved}
        />
      );
    });
    await flush();
    expect(
      renderer.root
        .findAllByType('input')
        .find((input) => input.props.placeholder === filterPlaceholder)?.props.value
    ).toBe('sonnet');
    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={shiftedRows[0]}
          initialTab="overview"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={onSaved}
        />
      );
    });
    mocks.getClaudeConfigs.mockResolvedValue(shiftedItems);
    await flush();
    // Saving writes cleanly at index 0 with rotated ETag "etag-2"
    await act(async () => {
      await findButtonByText(
        renderer.root,
        i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
      )!.props.onClick();
    });
    await flush();

    expect(mocks.updateClaudeConfig).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'Claude Target 1' }),
      expect.objectContaining({
        name: 'Claude Target 1',
        prefix: 'dirty-shift-prefix',
        v8Source: expect.objectContaining({ groupIndex: 0, keyIndex: 0, etag: '"etag-2"' }),
      })
    );
  });

  it('refuses to advance v8Source.etag when a different sibling shifts into the same row.key while dirty, failing safely with 412', async () => {
    const row1 = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Other',
          apiKey: 'sk-other',
          baseUrl: 'https://other.example.com',
          v8Source: { family: 'claude', groupIndex: 0, keyIndex: 0, group: {}, etag: '"etag-1"' },
        },
        {
          name: 'Original Row 1',
          apiKey: 'sk-shared',
          baseUrl: 'https://shared.example.com',
          v8Source: { family: 'claude', groupIndex: 1, keyIndex: 0, group: {}, etag: '"etag-1"' },
        },
      ],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    })[1];

    // Sibling Row 2 (same apiKey+baseUrl -> same ::1 key, different name & group/etag) shifts into ::1 after Row 1 is deleted
    const shiftedSiblingAtSameKey = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Other',
          apiKey: 'sk-other',
          baseUrl: 'https://other.example.com',
          v8Source: { family: 'claude', groupIndex: 0, keyIndex: 0, group: {}, etag: '"etag-2"' },
        },
        {
          name: 'Sibling Row 2',
          apiKey: 'sk-shared',
          baseUrl: 'https://shared.example.com',
          v8Source: { family: 'claude', groupIndex: 2, keyIndex: 0, group: {}, etag: '"etag-2"' },
        },
      ],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    })[1];

    expect(row1.key).toBe(shiftedSiblingAtSameKey.key);

    mocks.getClaudeConfigs.mockResolvedValue([
      { name: 'Other', apiKey: 'sk-other', baseUrl: 'https://other.example.com' },
      shiftedSiblingAtSameKey.raw,
    ]);
    mocks.updateClaudeConfig.mockImplementation(async (_idx, payload) => {
      if (payload.v8Source?.etag !== '"etag-2"') {
        throw Object.assign(new Error('Precondition Failed'), { status: 412 });
      }
    });

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={row1}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(renderer);
    await flush();

    await act(async () => {
      findInputByLabel(renderer.root, i18n.t('ai_providers.prefix_label'))!.props.onChange({
        target: { value: 'dirty-edit' },
      });
    });

    // Update row prop with the shifted sibling occupying ::1
    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={shiftedSiblingAtSameKey}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    await flush();

    // Save must NOT advance etag to "etag-2"; it sends stale "etag-1" and fails safely with 412
    await act(async () => {
      await findButtonByText(
        renderer.root,
        i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
      )!.props.onClick();
    });
    await flush();

    expect(mocks.updateClaudeConfig).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'Sibling Row 2' }),
      expect.objectContaining({
        v8Source: expect.objectContaining({ etag: '"etag-1"' }),
      })
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      `${i18n.t('notification.update_failed')}: Precondition Failed`,
      'error'
    );
  });

  it('refuses to advance v8Source.etag when only v8Source.groupIndex/keyIndex changes at the same positional row.key while dirty', async () => {
    const row1 = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Group 0 Key 0',
          apiKey: 'sk-g0',
          baseUrl: 'https://g0.example.com',
          v8Source: { family: 'claude', groupIndex: 0, keyIndex: 0, group: {}, etag: '"etag-1"' },
        },
        {
          name: 'Identical Clone',
          apiKey: 'sk-clone',
          baseUrl: 'https://clone.example.com',
          v8Source: { family: 'claude', groupIndex: 0, keyIndex: 1, group: {}, etag: '"etag-1"' },
        },
      ],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    })[1];

    // Identical business fields at index 1 (same ::1 key), but different v8Source coordinates (groupIndex 1, keyIndex 0)
    const cloneFromOtherGroup = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Group 0 Key 0',
          apiKey: 'sk-g0',
          baseUrl: 'https://g0.example.com',
          v8Source: { family: 'claude', groupIndex: 0, keyIndex: 0, group: {}, etag: '"etag-2"' },
        },
        {
          name: 'Identical Clone',
          apiKey: 'sk-clone',
          baseUrl: 'https://clone.example.com',
          v8Source: { family: 'claude', groupIndex: 1, keyIndex: 0, group: {}, etag: '"etag-2"' },
        },
      ],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    })[1];

    expect(row1.key).toBe(cloneFromOtherGroup.key);
    mocks.getClaudeConfigs.mockResolvedValue([
      { name: 'Group 0 Key 0', apiKey: 'sk-g0', baseUrl: 'https://g0.example.com' },
      cloneFromOtherGroup.raw,
    ]);
    mocks.updateClaudeConfig.mockImplementation(async (_existing, payload) => {
      if (payload.v8Source?.etag !== '"etag-2"') {
        throw Object.assign(new Error('Precondition Failed'), { status: 412 });
      }
    });

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={row1}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(renderer);
    await flush();

    await act(async () => {
      findInputByLabel(renderer.root, i18n.t('ai_providers.prefix_label'))!.props.onChange({
        target: { value: 'dirty-clone-edit' },
      });
    });

    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={cloneFromOtherGroup}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    await flush();

    await act(async () => {
      await findButtonByText(
        renderer.root,
        i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
      )!.props.onClick();
    });
    await flush();

    expect(mocks.updateClaudeConfig).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({
        v8Source: expect.objectContaining({ etag: '"etag-1"' }),
      })
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      `${i18n.t('notification.update_failed')}: Precondition Failed`,
      'error'
    );
  });

  it('shows warning banner and disables mutating actions when rowDeletedExternally is true while keeping unsaved draft readable and discard-confirmed', async () => {
    const row = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Orphaned Claude',
          apiKey: 'sk-orphan',
          baseUrl: 'https://orphan.example.com',
        },
      ],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    })[0];

    mocks.showConfirmation.mockImplementation((opts: { onConfirm?: () => void }) => {
      opts.onConfirm?.();
    });
    const onClose = vi.fn();

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={row}
          initialTab="config"
          v8AliasSupported
          usageByProvider={emptyUsageByProvider}
          onClose={onClose}
          onSaved={vi.fn()}
          onToggleEnabled={vi.fn()}
          onDelete={vi.fn()}
        />
      );
    });
    renderers.push(renderer);
    await flush();

    await act(async () => {
      findInputByLabel(renderer.root, i18n.t('ai_providers.name_label'))!.props.onChange({
        target: { value: 'Unsaved Orphan Draft' },
      });
    });

    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={row}
          initialTab="config"
          v8AliasSupported
          rowDeletedExternally
          usageByProvider={emptyUsageByProvider}
          onClose={onClose}
          onSaved={vi.fn()}
          onToggleEnabled={vi.fn()}
          onDelete={vi.fn()}
        />
      );
    });
    await flush();

    const notice = renderer.root.findByProps({ 'data-testid': 'drawer-deleted-externally-notice' });
    expect(notice.props.role).toBe('alert');
    expect(findInputByLabel(renderer.root, i18n.t('ai_providers.name_label'))?.props.value).toBe(
      'Unsaved Orphan Draft'
    );
    expect(
      findButtonByText(
        renderer.root,
        i18n.t('ai_providers.drawer_save_config', { defaultValue: '保存配置' })
      )?.props.disabled
    ).toBe(true);
    expect(
      findButtonByText(renderer.root, i18n.t('common.delete'))?.props.disabled
    ).toBe(true);

    let closeAllowed: boolean | undefined;
    await act(async () => {
      closeAllowed = await mocks.drawerPropsRef.current?.onBeforeClose?.();
    });
    expect(closeAllowed).toBe(true);
    expect(mocks.showConfirmation).toHaveBeenCalledTimes(1);
  });

  it('resets form, dirty state, and connectivity results when switching sessionId between duplicate-field non-authIndex sibling rows', async () => {
    const duplicateClaudeItems: ProviderKeyConfig[] = [
      {
        name: 'Dup Claude',
        apiKey: 'sk-dup-claude',
        baseUrl: 'https://dup.claude.example.com',
        prefix: 'dup-prefix',
        models: [{ name: 'claude-sonnet-4-5', alias: 'claude-sonnet-4-5' }],
      },
      {
        name: 'Dup Claude',
        apiKey: 'sk-dup-claude',
        baseUrl: 'https://dup.claude.example.com',
        prefix: 'dup-prefix',
        models: [{ name: 'claude-sonnet-4-5', alias: 'claude-sonnet-4-5' }],
      },
    ];
    const rows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: duplicateClaudeItems,
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    });

    expect(rows[0].originalIndex).toBe(0);
    expect(rows[1].originalIndex).toBe(1);
    expect(rows[0].key).not.toBe(rows[1].key);

    mocks.showConfirmation.mockImplementation((opts: { onConfirm?: () => void }) => {
      opts.onConfirm?.();
    });
    mocks.apiCallRequest.mockResolvedValueOnce({ statusCode: 200, body: '{}' });

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          initialTab="config"
          sessionId={1}
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    renderers.push(renderer);
    await flush();

    await act(async () => {
      findInputByLabel(
        renderer.root,
        i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
      )!.props.onChange({ target: { value: 'Dirty Sibling Zero' } });
      findInputByLabel(renderer.root, i18n.t('ai_providers.prefix_label'))!.props.onChange({
        target: { value: 'dirty-zero-prefix' },
      });
    });
    expect(extractText(renderer.root)).toContain(
      i18n.t('ai_providers.drawer_unsaved_changes', { defaultValue: '未保存更改' })
    );

    // Switch to Connectivity tab, customize test model, and execute connectivity check
    await act(async () => {
      findTabByText(
        renderer.root,
        i18n.t('ai_providers.drawer_tab_connectivity', { defaultValue: '测活诊断' })
      )!.props.onClick();
    });
    const customModelPlaceholder = i18n.t('ai_providers.custom_test_model_placeholder', {
      defaultValue: '或输入自定义测试模型',
    });
    await act(async () => {
      renderer.root
        .findAllByType('input')
        .find((input) => input.props.placeholder === customModelPlaceholder)!
        .props.onChange({ target: { value: 'custom-row0-probe-model' } });
    });
    await act(async () => {
      await findButtonByText(
        renderer.root,
        i18n.t('ai_providers.send_test_request_btn', { defaultValue: '立即发送测试请求' })
      )!.props.onClick();
    });
    await flush();

    expect(extractText(renderer.root)).toContain(i18n.t('ai_providers.claude_test_success'));
    expect(
      renderer.root
        .findAllByType('input')
        .find((input) => input.props.placeholder === customModelPlaceholder)?.props.value
    ).toBe('custom-row0-probe-model');

    // Simulate parent confirming discard and switching drawer sessionId to duplicate sibling row 1
    let discardAllowed: boolean | undefined;
    await act(async () => {
      discardAllowed = await mocks.drawerPropsRef.current?.onBeforeClose?.();
    });
    expect(discardAllowed).toBe(true);
    expect(mocks.showConfirmation).toHaveBeenCalledTimes(1);

    await act(async () => {
      renderer.update(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[1]}
          initialTab="config"
          sessionId={2}
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
        />
      );
    });
    await flush();

    expect(extractText(renderer.root)).not.toContain(
      i18n.t('ai_providers.drawer_unsaved_changes', { defaultValue: '未保存更改' })
    );
    expect(
      findInputByLabel(
        renderer.root,
        i18n.t('ai_providers.name_label', { defaultValue: '名称 / 别名' })
      )?.props.value
    ).toBe('Dup Claude');
    expect(findInputByLabel(renderer.root, i18n.t('ai_providers.prefix_label'))?.props.value).toBe(
      'dup-prefix'
    );

    await act(async () => {
      findTabByText(
        renderer.root,
        i18n.t('ai_providers.drawer_tab_connectivity', { defaultValue: '测活诊断' })
      )!.props.onClick();
    });
    await flush();

    expect(extractText(renderer.root)).not.toContain(i18n.t('ai_providers.claude_test_success'));
    expect(extractText(renderer.root)).not.toContain(
      i18n.t('ai_providers.connectivity_status_success', { defaultValue: '连通正常' })
    );
    expect(renderer.root.findAllByProps({ role: 'status' })).toHaveLength(0);
    expect(
      renderer.root
        .findAllByType('input')
        .find((input) => input.props.placeholder === customModelPlaceholder)?.props.value
    ).toBe('claude-sonnet-4-5');
  });

  it('enforces single-authority footer, Overview alias->upstream direction, Config key masking, and Connectivity quick-model chips + preflight preview', async () => {
    const rows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Claude Official',
          apiKey: 'sk-ant-secret-01',
          baseUrl: 'https://api.anthropic.com',
          prefix: 'cl',
          models: [
            { name: 'claude-sonnet-4-5', alias: 'sonnet' },
            { name: 'claude-3-5-haiku-latest', alias: 'haiku' },
          ],
        },
      ],
      xai: [],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    });

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          onToggleEnabled={vi.fn()}
          onDelete={vi.fn()}
        />
      );
    });
    renderers.push(renderer);
    await flush();

    // 1. Overview: alias -> upstream model direction and no "排除规则 0 条" noise
    const overviewText = extractText(renderer.root);
    expect(overviewText).toMatch(/sonnet\s*→\s*claude-sonnet-4-5/);
    expect(overviewText).not.toContain(
      i18n.t('ai_providers.overview_excluded_models_count', { count: 0 })
    );

    // 2. Connectivity (clean): footer right shows Close instead of duplicate primary CTA, quick chips switch model, preflight shows POST endpoint
    await act(async () => {
      findTabByText(
        renderer.root,
        i18n.t('ai_providers.drawer_tab_connectivity', { defaultValue: '测活诊断' })
      )!.props.onClick();
    });
    await flush();

    const connectivityText = extractText(renderer.root);
    expect(connectivityText).toContain('POST');
    expect(connectivityText).toContain('https://api.anthropic.com/v1/messages');
    expect(connectivityText).toContain(
      i18n.t('ai_providers.connectivity_using_saved_badge', { defaultValue: '已同步保存配置' })
    );

    const toggleModelListBtn = renderer.root
      .findAllByType('button')
      .find(
        (btn) =>
          btn.props['aria-label'] ===
          i18n.t('ai_providers.connectivity_toggle_model_list', {
            defaultValue: '展开已配置模型列表',
          })
      );
    expect(toggleModelListBtn).toBeDefined();
    await act(async () => {
      toggleModelListBtn!.props.onClick();
    });
    const haikuOption = renderer.root
      .findAllByType('button')
      .find(
        (btn) =>
          btn.props.role === 'option' &&
          extractText(btn).includes('claude-3-5-haiku-latest') &&
          extractText(btn).includes('haiku')
      );
    expect(haikuOption).toBeDefined();
    await act(async () => {
      haikuOption!.props.onClick();
    });
    const customModelPlaceholder = i18n.t('ai_providers.custom_test_model_placeholder', {
      defaultValue: '选择已配模型或输入自定义模型 ID',
    });
    expect(
      renderer.root
        .findAllByType('input')
        .find((input) => input.props.placeholder === customModelPlaceholder)?.props.value
    ).toBe('claude-3-5-haiku-latest');
    // 3. Config: API Key defaults to password type and toggles to text via eye button; footer Disable stays mounted when dirty
    await act(async () => {
      findTabByText(
        renderer.root,
        i18n.t('ai_providers.drawer_tab_config', { defaultValue: '配置' })
      )!.props.onClick();
    });
    await flush();

    const apiKeyInput = findInputByLabel(
      renderer.root,
      i18n.t('ai_providers.api_key_label', { defaultValue: 'API Key' })
    );
    expect(apiKeyInput?.props.type).toBe('password');
    const revealBtn = renderer.root
      .findAllByType('button')
      .find(
        (btn) => btn.props['aria-label'] === i18n.t('common.reveal', { defaultValue: '显示明文' })
      );
    expect(revealBtn).toBeDefined();
    await act(async () => {
      revealBtn!.props.onClick();
    });
    expect(
      findInputByLabel(
        renderer.root,
        i18n.t('ai_providers.api_key_label', { defaultValue: 'API Key' })
      )?.props.type
    ).toBe('text');

    await act(async () => {
      findInputByLabel(renderer.root, i18n.t('ai_providers.prefix_label'))!.props.onChange({
        target: { value: 'cl-next' },
      });
    });
    const disableBtn = findButtonByText(
      renderer.root,
      i18n.t('ai_providers.action_disable', { defaultValue: '停用' })
    );
    expect(disableBtn).toBeDefined();
    expect(disableBtn?.props.disabled).toBe(true);
  });

  it('switches Models tab into drawer-native discovery sub-view (Plan A) and coordinates single-authority footer', async () => {
    mocks.fetchClaudeModelsViaApiCall.mockResolvedValueOnce([
      { name: 'claude-sonnet-4-5', description: 'Flagship Sonnet' },
      { name: 'claude-opus-4-6', description: 'Flagship Opus' },
    ]);

    const rows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Claude Official',
          apiKey: 'sk-ant-secret-01',
          baseUrl: 'https://api.anthropic.com',
          models: [{ name: 'claude-sonnet-4-5', alias: 'sonnet' }],
        },
      ],
      xai: [],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    });

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          initialTab="models"
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          onToggleEnabled={vi.fn()}
          onDelete={vi.fn()}
        />
      );
    });
    renderers.push(renderer);
    await flush();

    const fetchBtn = findButtonByText(
      renderer.root,
      i18n.t('ai_providers.fetch_upstream_models_btn', { defaultValue: '从上游 /models 获取' })
    );
    expect(fetchBtn).toBeDefined();
    await act(async () => {
      fetchBtn!.props.onClick();
    });
    await flush();

    // Renders inline discovery sub-view inside drawer (no Modal overlay)
    const subView = renderer.root.findByProps({ 'data-testid': 'drawer-model-discovery-subview' });
    expect(subView).toBeDefined();
    expect(extractText(subView)).toContain('GET');
    expect(extractText(subView)).toContain('https://api.anthropic.com/v1/models');
    expect(extractText(subView)).toContain('claude-opus-4-6');

    // Select claude-opus-4-6 and apply via coordinated Drawer footer primary CTA
    const opusCheckbox = subView
      .findAllByType('input')
      .find((input) => input.props.type === 'checkbox' && input.props['aria-label'] === 'claude-opus-4-6');
    expect(opusCheckbox).toBeDefined();
    await act(async () => {
      opusCheckbox!.props.onChange({ target: { checked: true } });
    });

    const applyBtn = findButtonByText(
      renderer.root,
      i18n.t('ai_providers.apply_selected_models_btn', {
        count: 1,
        defaultValue: '添加所选模型 (1)',
      })
    );
    expect(applyBtn).toBeDefined();
    await act(async () => {
      applyBtn!.props.onClick();
    });
    await flush();

    // Returns to model mapping list with claude-opus-4-6 added and Save button enabled
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'drawer-model-discovery-subview' })
    ).toHaveLength(0);
    expect(
      renderer.root.findAllByType('input').some((input) => input.props.value === 'claude-opus-4-6')
    ).toBe(true);
  });

  it('renders structured Claude 401 auth diagnostic chips, collapsible response body, /models probe mode, and zero duplicate Toast notifications', async () => {
    mocks.apiCallRequest.mockResolvedValueOnce({
      statusCode: 401,
      body: {
        type: 'error',
        error: { type: 'authentication_error', message: 'invalid x-api-key' },
      },
      bodyText: '{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}',
    });
    mocks.fetchClaudeModelsViaApiCall.mockResolvedValueOnce([
      { name: 'claude-sonnet-4-5', description: 'Sonnet' },
    ]);

    const rows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [
        {
          name: 'Claude Official',
          apiKey: 'sk-ant-demo-key',
          baseUrl: 'https://api.anthropic.com',
          models: [{ name: 'claude-sonnet-4-5', alias: 'sonnet' }],
        },
      ],
      xai: [],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    });

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          initialTab="connectivity"
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          onToggleEnabled={vi.fn()}
          onDelete={vi.fn()}
        />
      );
    });
    renderers.push(renderer);
    await flush();

    mocks.showNotification.mockClear();
    const sendBtn = findButtonByText(
      renderer.root,
      i18n.t('ai_providers.send_test_request_btn', { defaultValue: '立即发送测试请求' })
    );
    await act(async () => {
      sendBtn!.props.onClick();
    });
    await flush();

    expect(mocks.showNotification).not.toHaveBeenCalled();
    const textAfterFail = extractText(renderer.root);
    expect(textAfterFail).toContain('HTTP 401');
    expect(textAfterFail).toContain('invalid x-api-key');
    expect(textAfterFail).not.toContain('[diag:');
    expect(textAfterFail).toContain(
      i18n.t('ai_providers.connectivity_diag_header', { header: 'x-api-key' })
    );
    expect(textAfterFail).toContain(
      i18n.t('ai_providers.connectivity_diag_endpoint_type', {
        type: i18n.t('ai_providers.connectivity_diag_endpoint_anthropic'),
      })
    );

    // Expand response body on demand
    const showBodyBtn = findButtonByText(
      renderer.root,
      i18n.t('ai_providers.connectivity_show_body', { defaultValue: '查看响应体' })
    );
    expect(showBodyBtn).toBeDefined();
    await act(async () => {
      showBodyBtn!.props.onClick();
    });
    expect(extractText(renderer.root)).toContain('"authentication_error"');

    // Switch to /models probe mode
    const modelsModeBtn = findButtonByText(
      renderer.root,
      i18n.t('ai_providers.connectivity_mode_models', { defaultValue: '/models 探活' })
    );
    expect(modelsModeBtn).toBeDefined();
    await act(async () => {
      modelsModeBtn!.props.onClick();
    });
    await flush();
    expect(extractText(renderer.root)).toContain('GET');
    expect(extractText(renderer.root)).toContain('https://api.anthropic.com/v1/models');

    const sendModelsBtn = findButtonByText(
      renderer.root,
      i18n.t('ai_providers.send_test_request_btn', { defaultValue: '立即发送测试请求' })
    );
    await act(async () => {
      sendModelsBtn!.props.onClick();
    });
    await flush();
    expect(mocks.fetchClaudeModelsViaApiCall).toHaveBeenCalledTimes(1);
    expect(mocks.showNotification).not.toHaveBeenCalled();
  });

  it('renders concise OpenAI multi-key error summary, failed-only filter, and collapsible response body without duplicate Toasts', async () => {
    mocks.apiCallRequest
      .mockResolvedValueOnce({ statusCode: 200, body: { id: 'chatcmpl-1' }, bodyText: '{}' })
      .mockResolvedValueOnce({
        statusCode: 401,
        body: { error: { message: 'User not found.', code: 401 } },
        bodyText: '{"error":{"message":"User not found.","code":401}}',
      });

    const rows = buildProviderRows({
      gemini: [],
      codex: [],
      claude: [],
      xai: [],
      vertex: [],
      openai: [
        {
          name: 'OpenRouter Primary',
          baseUrl: 'https://openrouter.ai/api/v1',
          apiKeyEntries: [
            markPersistedOpenAIKeyEntry({ apiKey: 'sk-or-valid-key-1' }, 0),
            markPersistedOpenAIKeyEntry({ apiKey: 'sk-or-broken-key-2' }, 1),
          ],
          models: [{ name: 'openai/gpt-4.1', alias: 'gpt-4.1' }],
        },
      ],
      usageByProvider: emptyUsageByProvider,
    });

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProviderWorkspaceDrawer
          open
          mode="view-edit"
          row={rows[0]}
          initialTab="connectivity"
          usageByProvider={emptyUsageByProvider}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          onToggleEnabled={vi.fn()}
          onDelete={vi.fn()}
        />
      );
    });
    renderers.push(renderer);
    await flush();

    mocks.showNotification.mockClear();
    const testAllBtn = findButtonByText(
      renderer.root,
      i18n.t('ai_providers.openai_test_all_action', { defaultValue: '一键测试全部密钥' })
    );
    await act(async () => {
      testAllBtn!.props.onClick();
    });
    await flush();

    expect(mocks.showNotification).not.toHaveBeenCalled();
    const textAfterBatch = extractText(renderer.root);
    expect(textAfterBatch).toContain('User not found.');
    expect(textAfterBatch).not.toContain('Body: {');

    // Filter to failed-only keys
    const failedFilterBtn = findButtonByText(
      renderer.root,
      i18n.t('ai_providers.connectivity_filter_failed_only', { count: 1 })
    );
    expect(failedFilterBtn).toBeDefined();
    await act(async () => {
      failedFilterBtn!.props.onClick();
    });
    expect(extractText(renderer.root)).not.toContain('#1');
    expect(extractText(renderer.root)).toContain('#2');
  });
});
