import { createElement, useImperativeHandle, type ReactNode } from 'react';
import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import aiProvidersPageSource from './AiProvidersPage.tsx?raw';
import zhCNLocale from '@/i18n/locales/zh-CN.json';
import zhTWLocale from '@/i18n/locales/zh-TW.json';
import enLocale from '@/i18n/locales/en.json';
import ruLocale from '@/i18n/locales/ru.json';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  GeminiKeyConfig,
  OpenAIProviderConfig,
  ProviderKeyConfig,
  ProviderV8GroupSource,
} from '@/types';
import type { ProviderWorkspaceDrawerProps } from '@/components/providers/ProviderWorkspaceDrawer';
import type { ProviderRow } from '@/components/providers';
import type * as RowDataModule from '@/components/providers/ProviderTable/rowData';
import type * as SortModule from '@/components/providers/ProviderTable/sort';
import type * as KindMetaModule from '@/components/providers/ProviderTable/kindMeta';
import type * as ConfigStoreModule from '@/stores/useConfigStore';

const mocks = vi.hoisted(() => ({
  useRealConfigStore: false,
  useRealDrawer: false,
  config: {
    geminiApiKeys: [] as GeminiKeyConfig[],
    interactionsApiKeys: [] as GeminiKeyConfig[],
    codexApiKeys: [] as ProviderKeyConfig[],
    xaiApiKeys: [] as ProviderKeyConfig[],
    claudeApiKeys: [] as ProviderKeyConfig[],
    vertexApiKeys: [] as ProviderKeyConfig[],
    openaiCompatibility: [] as OpenAIProviderConfig[],
  },
  cacheValid: true,
  transitionLayer: null as { status: 'current' | 'background'; isCurrentLayer: boolean } | null,
  headerRefreshCallback: null as (() => Promise<void> | void) | null,
  getConfig: vi.fn(),
  fetchConfig: vi.fn(),
  updateConfigValue: vi.fn(),
  clearCache: vi.fn(),
  showNotification: vi.fn(),
  showConfirmation: vi.fn(),
  t: vi.fn((key: string, options?: { defaultValue?: string }) =>
    mocks.useRealDrawer && options?.defaultValue ? options.defaultValue : key
  ),
  pendingSavedIdentity: undefined as
    | {
        name?: string;
        apiKey?: string;
        baseUrl?: string;
        proxyUrl?: string;
        prefix?: string;
        authIndex?: string;
        v8Source?: ProviderV8GroupSource;
        originalIndex?: number;
      }
    | undefined,
  getGeminiKeys: vi.fn(),
  saveGeminiKeys: vi.fn(),
  createGeminiKey: vi.fn(),
  updateGeminiKey: vi.fn(),
  getInteractionsKeys: vi.fn(),
  createInteractionsKey: vi.fn(),
  updateInteractionsKey: vi.fn(),
  getCodexConfigs: vi.fn(),
  saveCodexConfigs: vi.fn(),
  createCodexConfig: vi.fn(),
  updateCodexConfig: vi.fn(),
  getXAIConfigs: vi.fn(),
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
  createOpenAIProvider: vi.fn(),
  updateOpenAIProvider: vi.fn(),
  deleteGeminiKey: vi.fn(),
  deleteInteractionsKey: vi.fn(),
  deleteCodexConfig: vi.fn(),
  deleteXAIConfig: vi.fn(),
  deleteClaudeConfig: vi.fn(),
  deleteVertexConfig: vi.fn(),
  deleteOpenAIProvider: vi.fn(),
  loadAllProvidersV8Snapshot: vi.fn(),
  getLatestV8ConfigEtag: vi.fn<() => string | undefined>(),
  setLatestV8ConfigEtag: vi.fn<(etag?: string) => void>(),
  getV8EtagMutationVersion: vi.fn(() => 0),
  readBackClaudeConfigAfterSave: vi.fn(),
  verifyClaudeFingerprintInRawConfig: vi.fn(),
  apiCallRequest: vi.fn(),
  fetchModelsViaApiCall: vi.fn(),
  fetchV1ModelsViaApiCall: vi.fn(),
  fetchClaudeModelsViaApiCall: vi.fn(),
  fetchGeminiModelsViaApiCall: vi.fn(),
  requestCloseOrSwitch: vi.fn<() => Promise<boolean>>(),
  refreshRecentRequests: vi.fn(async () => undefined),
  drawerPropsRef: {
    current: null as {
      onBeforeClose?: () => boolean | Promise<boolean>;
      onClose?: () => void;
    } | null,
  },
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: mocks.t,
  }),
}));

vi.mock('@/hooks/useHeaderRefresh', () => ({
  useHeaderRefresh: (cb: () => Promise<void> | void) => {
    mocks.headerRefreshCallback = cb;
  },
}));

vi.mock('@/hooks/useMediaQuery', () => ({
  useMediaQuery: () => false,
}));

vi.mock('@/components/common/PageTransitionLayer', () => ({
  usePageTransitionLayer: () => mocks.transitionLayer,
}));


vi.mock('@/services/api/config', () => ({
  configApi: {
    getConfig: (...args: unknown[]) => mocks.getConfig(...args),
  },
}));

vi.mock('@/services/api/providers', () => ({
  providersApi: {
    getGeminiKeys: mocks.getGeminiKeys,
    saveGeminiKeys: mocks.saveGeminiKeys,
    createGeminiKey: mocks.createGeminiKey,
    updateGeminiKey: mocks.updateGeminiKey,
    getInteractionsKeys: mocks.getInteractionsKeys,
    saveInteractionsKeys: vi.fn(),
    createInteractionsKey: mocks.createInteractionsKey,
    updateInteractionsKey: mocks.updateInteractionsKey,
    getCodexConfigs: mocks.getCodexConfigs,
    saveCodexConfigs: mocks.saveCodexConfigs,
    createCodexConfig: mocks.createCodexConfig,
    updateCodexConfig: mocks.updateCodexConfig,
    getXAIConfigs: mocks.getXAIConfigs,
    saveXAIConfigs: vi.fn(),
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
    createOpenAIProvider: mocks.createOpenAIProvider,
    updateOpenAIProvider: mocks.updateOpenAIProvider,
    deleteGeminiKey: mocks.deleteGeminiKey,
    deleteInteractionsKey: mocks.deleteInteractionsKey,
    deleteCodexConfig: mocks.deleteCodexConfig,
    deleteXAIConfig: mocks.deleteXAIConfig,
    deleteClaudeConfig: mocks.deleteClaudeConfig,
    deleteVertexConfig: mocks.deleteVertexConfig,
    deleteOpenAIProvider: mocks.deleteOpenAIProvider,
    loadAllProvidersV8Snapshot: mocks.loadAllProvidersV8Snapshot,
    getLatestV8ConfigEtag: mocks.getLatestV8ConfigEtag,
    setLatestV8ConfigEtag: mocks.setLatestV8ConfigEtag,
    getV8EtagMutationVersion: mocks.getV8EtagMutationVersion,
  },
}));

vi.mock('@/services/api', () => ({
  configApi: {
    getConfig: (...args: unknown[]) => mocks.getConfig(...args),
  },
  providersApi: {
    getGeminiKeys: mocks.getGeminiKeys,
    saveGeminiKeys: mocks.saveGeminiKeys,
    createGeminiKey: mocks.createGeminiKey,
    updateGeminiKey: mocks.updateGeminiKey,
    getInteractionsKeys: mocks.getInteractionsKeys,
    saveInteractionsKeys: vi.fn(),
    createInteractionsKey: mocks.createInteractionsKey,
    updateInteractionsKey: mocks.updateInteractionsKey,
    getCodexConfigs: mocks.getCodexConfigs,
    saveCodexConfigs: mocks.saveCodexConfigs,
    createCodexConfig: mocks.createCodexConfig,
    updateCodexConfig: mocks.updateCodexConfig,
    getXAIConfigs: mocks.getXAIConfigs,
    saveXAIConfigs: vi.fn(),
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
    createOpenAIProvider: mocks.createOpenAIProvider,
    updateOpenAIProvider: mocks.updateOpenAIProvider,
    deleteGeminiKey: mocks.deleteGeminiKey,
    deleteInteractionsKey: mocks.deleteInteractionsKey,
    deleteCodexConfig: mocks.deleteCodexConfig,
    deleteXAIConfig: mocks.deleteXAIConfig,
    deleteClaudeConfig: mocks.deleteClaudeConfig,
    deleteVertexConfig: mocks.deleteVertexConfig,
    deleteOpenAIProvider: mocks.deleteOpenAIProvider,
    loadAllProvidersV8Snapshot: mocks.loadAllProvidersV8Snapshot,
    getLatestV8ConfigEtag: mocks.getLatestV8ConfigEtag,
    setLatestV8ConfigEtag: mocks.setLatestV8ConfigEtag,
    getV8EtagMutationVersion: mocks.getV8EtagMutationVersion,
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
  getApiCallErrorMessage: (result: { statusCode: number }) => `HTTP ${result.statusCode}`,
  getApiCallErrorDetails: (result: { statusCode: number }) => `HTTP ${result.statusCode}`,
  readBackClaudeConfigAfterSave: mocks.readBackClaudeConfigAfterSave,
  verifyClaudeFingerprintInRawConfig: mocks.verifyClaudeFingerprintInRawConfig,
}));

vi.mock('@/stores/useAuthStore', () => ({
  useAuthStore: (selector: (s: { connectionStatus: string; apiBase: string; managementKey: string }) => unknown) =>
    selector({
      connectionStatus: 'connected',
      apiBase: 'http://localhost:8317',
      managementKey: 'mgmt-key',
    }),
}));

vi.mock('@/stores', async () => {
  const { useConfigStore: actualUseConfigStore } =
    await vi.importActual<typeof ConfigStoreModule>('@/stores/useConfigStore');

  return {
    useAuthStore: (selector: (s: { connectionStatus: string; apiBase: string; managementKey: string }) => unknown) =>
      selector({
        connectionStatus: 'connected',
        apiBase: 'http://localhost:8317',
        managementKey: 'mgmt-key',
      }),
    useConfigStore: Object.assign(
      (selector: (s: Record<string, unknown>) => unknown) => {
        if (mocks.useRealConfigStore) {
          return actualUseConfigStore(selector as never);
        }
        return selector({
          config: mocks.config,
          fetchConfig: mocks.fetchConfig,
          updateConfigValue: mocks.updateConfigValue,
          clearCache: mocks.clearCache,
          isCacheValid: () => mocks.cacheValid,
        });
      },
      {
        getState: () => actualUseConfigStore.getState(),
        setState: (partial: unknown) => actualUseConfigStore.setState(partial as never),
      }
    ),
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
    useThemeStore: (selector: (s: { resolvedTheme: string }) => unknown) =>
      selector({ resolvedTheme: 'light' }),
  };
});

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
    return open
      ? createElement('div', { 'data-testid': 'drawer-shell' }, title, children, footer)
      : null;
  },
}));

vi.mock('@/components/providers', async () => {
  const actualRows = await vi.importActual<typeof RowDataModule>(
    '@/components/providers/ProviderTable/rowData'
  );
  const actualSort = await vi.importActual<typeof SortModule>(
    '@/components/providers/ProviderTable/sort'
  );
  const actualKinds = await vi.importActual<typeof KindMetaModule>(
    '@/components/providers/ProviderTable/kindMeta'
  );
  const actualDrawerModule = await vi.importActual<{
    ProviderWorkspaceDrawer: (props: ProviderWorkspaceDrawerProps) => ReactNode;
  }>('@/components/providers/ProviderWorkspaceDrawer');
  const MockProviderWorkspaceDrawer = ({
    open,
    mode,
    row,
    initialTab,
    v8AliasSupported,
    resolvedTheme,
    onSaved,
    onDelete,
    ref,
  }: ProviderWorkspaceDrawerProps) => {
    useImperativeHandle(
      ref,
      () => ({
        requestCloseOrSwitch: () => mocks.requestCloseOrSwitch(),
      }),
      []
    );
    return open ? (
      <div
        data-testid="provider-workspace-drawer"
        data-mode={mode}
        data-tab={initialTab}
        data-row-key={row?.key ?? ''}
        data-row-label={row?.label ?? ''}
        data-row-etag={String(row?.raw.v8Source?.etag ?? '')}
        data-resolved-theme={String(resolvedTheme ?? '')}
        data-v8-supported={String(Boolean(v8AliasSupported))}
      >
        {row && (
          <>
            <button
              type="button"
              data-drawer-save-identity
              onClick={() => void onSaved?.(row.kind, mocks.pendingSavedIdentity)}
            >
              save-identity
            </button>
            <button type="button" data-drawer-delete onClick={() => onDelete?.(row)}>
              delete
            </button>
          </>
        )}
      </div>
    ) : null;
  };

  return {
    ...actualRows,
    ...actualSort,
    ...actualKinds,
    ProviderToolbar: ({
      onRefresh,
      onAdd,
      onHealthCheck,
      onKindChange,
      onSearchTextChange,
      onStatusFilterChange,
    }: {
      onRefresh?: () => void;
      onAdd: (kind: string) => void;
      onHealthCheck: () => void;
      onKindChange?: (kind: string) => void;
      onSearchTextChange?: (value: string) => void;
      onStatusFilterChange?: (status: 'all' | 'enabled' | 'disabled') => void;
    }) => (
      <div data-testid="provider-toolbar">
        <button type="button" data-toolbar-refresh onClick={onRefresh}>
          refresh
        </button>
        <button type="button" data-toolbar-add onClick={() => onAdd('openai')}>
          add
        </button>
        <button type="button" data-toolbar-health onClick={onHealthCheck}>
          health
        </button>
        <button type="button" data-toolbar-kind-codex onClick={() => onKindChange?.('codex')}>
          codex
        </button>
        <button type="button" data-toolbar-kind-all onClick={() => onKindChange?.('all')}>
          all
        </button>
        <button
          type="button"
          data-toolbar-search-none
          onClick={() => onSearchTextChange?.('nonexistent-query')}
        >
          search-none
        </button>
        <button type="button" data-toolbar-search-clear onClick={() => onSearchTextChange?.('')}>
          search-clear
        </button>
        <button
          type="button"
          data-toolbar-status-enabled
          onClick={() => onStatusFilterChange?.('enabled')}
        >
          status-enabled
        </button>
      </div>
    ),
    ProviderTable: ({
      rows,
      selectionMode,
      selectedKeys,
      onToggleSelectRow,
      onInspect,
      onOpenModels,
      onEdit,
      onHealthCheckRow,
      onToggleEnabled,
      onDelete,
    }: {
      rows: ProviderRow[];
      selectionMode?: boolean;
      selectedKeys?: ReadonlySet<string>;
      onToggleSelectRow?: (key: string) => void;
      onInspect: (row: ProviderRow) => void;
      onOpenModels: (row: ProviderRow) => void;
      onEdit: (row: ProviderRow) => void;
      onHealthCheckRow?: (row: ProviderRow) => void;
      onToggleEnabled: (row: ProviderRow, enabled: boolean) => void;
      onDelete: (row: ProviderRow) => void;
    }) => (
      <div data-testid="provider-table" data-selection={Boolean(selectionMode)}>
        {rows.map((row) => (
          <div key={row.key} data-row-key={row.key} data-row-label={row.label}>
            {selectionMode && (
              <button
                type="button"
                data-select-row={row.key}
                data-selected={Boolean(selectedKeys?.has(row.key))}
                onClick={() => onToggleSelectRow?.(row.key)}
              >
                select
              </button>
            )}
            <button type="button" data-inspect={row.key} onClick={() => onInspect(row)}>
              inspect
            </button>
            <button type="button" data-models={row.key} onClick={() => onOpenModels(row)}>
              models
            </button>
            <button type="button" data-edit={row.key} onClick={() => onEdit(row)}>
              edit
            </button>
            <button
              type="button"
              data-health-row={row.key}
              onClick={() => onHealthCheckRow?.(row)}
            >
              row-health
            </button>
            <button
              type="button"
              data-toggle={row.key}
              onClick={() => onToggleEnabled(row, !row.enabled)}
            >
              toggle
            </button>
            <button type="button" data-delete={row.key} onClick={() => onDelete(row)}>
              row-delete
            </button>
          </div>
        ))}
      </div>
    ),
    ProviderCardGrid: ({ rows }: { rows: ProviderRow[] }) => (
      <div data-testid="provider-card-grid" data-count={rows.length} />
    ),
    ProviderWorkspaceDrawer: (props: ProviderWorkspaceDrawerProps) =>
      mocks.useRealDrawer ? (
        <actualDrawerModule.ProviderWorkspaceDrawer {...props} />
      ) : (
        <MockProviderWorkspaceDrawer {...props} />
      ),
    ProviderHealthCheckDrawer: ({
      open,
      rows,
      allRows,
      scopeMode,
      onOpenProviderConfig,
    }: {
      open: boolean;
      rows: ProviderRow[];
      allRows?: ProviderRow[];
      scopeMode?: 'filtered' | 'selected';
      onOpenProviderConfig?: (row: ProviderRow) => void;
    }) =>
      open ? (
        <div
          data-testid="provider-health-drawer"
          data-scope={scopeMode}
          data-count={rows.length}
          data-all-rows-count={allRows?.length ?? 0}
        >
          {rows[0] && (
            <button
              type="button"
              data-health-open-config
              onClick={() => onOpenProviderConfig?.(rows[0])}
            >
              open-config
            </button>
          )}
          {rows.map((item, idx) => (
            <button
              key={item.key}
              type="button"
              data-health-open-config-index={idx}
              onClick={() => onOpenProviderConfig?.(item)}
            >
              open-config-{idx}
            </button>
          ))}
        </div>
      ) : null,
    useProviderRecentRequests: () => ({
      usageByProvider: new Map(),
      loadRecentRequests: vi.fn(async () => undefined),
      refreshRecentRequests: mocks.refreshRecentRequests,
    }),
  };
});

import { AiProvidersPage } from './AiProvidersPage';
import { PaginationFooter } from '@/components/common/PaginationFooter';
import { useConfigStore } from '@/stores';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const flush = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
};

describe('AiProvidersPage P1 workspace & unified drawer', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.cacheValid = true;
    mocks.transitionLayer = null;
    mocks.headerRefreshCallback = null;
    mocks.pendingSavedIdentity = undefined;
    mocks.t.mockImplementation((key: string) => key);
    mocks.getLatestV8ConfigEtag.mockReturnValue(undefined);
    mocks.requestCloseOrSwitch.mockResolvedValue(true);
    mocks.config = {
      geminiApiKeys: [{ name: 'Gemini 主力', apiKey: 'gemini-key-1' }],
      interactionsApiKeys: [],
      codexApiKeys: [{ name: 'Codex 备份', apiKey: 'codex-key-1' }],
      xaiApiKeys: [],
      claudeApiKeys: [],
      vertexApiKeys: [],
      openaiCompatibility: [],
    };
    mocks.fetchConfig.mockImplementation(async () => mocks.config);
    mocks.getVertexConfigs.mockResolvedValue([]);
    mocks.getOpenAIProviders.mockResolvedValue([]);
    mocks.loadAllProvidersV8Snapshot.mockImplementation(async () => ({
      gemini: mocks.config.geminiApiKeys,
      interactions: mocks.config.interactionsApiKeys,
      codex: mocks.config.codexApiKeys,
      xai: mocks.config.xaiApiKeys,
      claude: mocks.config.claudeApiKeys,
      vertex: mocks.config.vertexApiKeys,
      openai: mocks.config.openaiCompatibility,
      v8Supported: true,
    }));
    mocks.saveGeminiKeys.mockResolvedValue(undefined);
    mocks.saveCodexConfigs.mockResolvedValue(undefined);
    mocks.deleteGeminiKey.mockResolvedValue(undefined);
    mocks.deleteInteractionsKey.mockResolvedValue(undefined);
    mocks.deleteCodexConfig.mockResolvedValue(undefined);
    mocks.deleteXAIConfig.mockResolvedValue(undefined);
    mocks.deleteClaudeConfig.mockResolvedValue(undefined);
    mocks.deleteVertexConfig.mockResolvedValue(undefined);
    mocks.deleteOpenAIProvider.mockResolvedValue(undefined);
  });

  it('keeps PaginationFooter mounted even when rows fit on a single page and renders v8 aliases', async () => {
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const footer = renderer.root.findByType(PaginationFooter);
    expect(footer.props.variant).toBe('flush');
    expect(footer.props.currentPage).toBe(1);
    expect(footer.props.totalPages).toBe(1);
    expect(footer.props.showPageJump).toBe(true);

    const rowNodes = renderer.root.findAll((node) => Boolean(node.props?.['data-row-label']));
    expect(rowNodes.map((node) => node.props['data-row-label'])).toEqual([
      'Gemini 主力',
      'Codex 备份',
    ]);

    act(() => renderer.unmount());
  });

  it('routes inspect, models, config, and health-check-to-config into the unified ProviderWorkspaceDrawer', async () => {
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const firstRowKey = renderer.root.findAll((n) => Boolean(n.props?.['data-row-key']))[0].props[
      'data-row-key'
    ] as string;

    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': firstRowKey }).props.onClick();
    });
    expect(renderer.root.findByProps({ 'data-testid': 'provider-workspace-drawer' }).props).toMatchObject({
      'data-mode': 'view-edit',
      'data-tab': 'overview',
      'data-row-label': 'Gemini 主力',
    });

    await act(async () => {
      renderer.root.findByProps({ 'data-models': firstRowKey }).props.onClick();
    });
    expect(renderer.root.findByProps({ 'data-testid': 'provider-workspace-drawer' }).props['data-tab']).toBe(
      'models'
    );

    await act(async () => {
      renderer.root.findByProps({ 'data-health-row': firstRowKey }).props.onClick();
    });
    expect(renderer.root.findByProps({ 'data-testid': 'provider-workspace-drawer' }).props['data-tab']).toBe(
      'connectivity'
    );
    expect(
      renderer.root.findAll((n) => n.props?.['data-testid'] === 'provider-health-drawer')
    ).toHaveLength(0);

    await act(async () => {
      renderer.root.findByProps({ 'data-toolbar-health': true }).props.onClick();
    });
    expect(renderer.root.findByProps({ 'data-testid': 'provider-health-drawer' }).props['data-scope']).toBe(
      'filtered'
    );

    await act(async () => {
      renderer.root.findByProps({ 'data-health-open-config': true }).props.onClick();
    });
    expect(renderer.root.findByProps({ 'data-testid': 'provider-workspace-drawer' }).props['data-tab']).toBe(
      'config'
    );

    act(() => renderer.unmount());
  });

  it('refreshes both provider configs and recent requests on toolbar/header refresh', async () => {
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();
    expect(mocks.fetchConfig).toHaveBeenCalledTimes(1);

    await act(async () => {
      await mocks.headerRefreshCallback?.();
    });
    expect(mocks.fetchConfig).toHaveBeenCalledWith(undefined, true);
    expect(mocks.refreshRecentRequests).toHaveBeenCalledTimes(1);

    act(() => renderer.unmount());
  });

  it('supports batch disabling and scoped health check across multiple provider families', async () => {
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const findButtonByText = (text: string) =>
      renderer.root
        .findAllByType('button')
        .find((b) => b.findAllByType('span').some((s) => s.children.includes(text)))!;

    // Enter selection mode
    await act(async () => {
      findButtonByText('ai_providers.enter_selection_mode').props.onClick();
    });

    const selectButtons = renderer.root.findAll((n) => Boolean(n.props?.['data-select-row']));
    for (const btn of selectButtons) {
      await act(async () => {
        btn.props.onClick();
      });
    }

    // Batch health check uses selected scope
    await act(async () => {
      findButtonByText('ai_providers.batch_health_check').props.onClick();
    });
    expect(renderer.root.findByProps({ 'data-testid': 'provider-health-drawer' }).props).toMatchObject({
      'data-scope': 'selected',
      'data-count': 2,
    });

    // Batch disable saves once per affected family
    await act(async () => {
      findButtonByText('ai_providers.batch_disable').props.onClick();
    });
    await flush();

    expect(mocks.saveGeminiKeys).toHaveBeenCalledWith([
      expect.objectContaining({
        name: 'Gemini 主力',
        excludedModels: ['*'],
      }),
    ]);
    expect(mocks.saveCodexConfigs).toHaveBeenCalledWith([
      expect.objectContaining({
        name: 'Codex 备份',
        excludedModels: ['*'],
      }),
    ]);

    act(() => renderer.unmount());
  });

  it('isolates selectedRows to visibleRows when switching kind tabs or search filters', async () => {
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const findButtonByText = (text: string) =>
      renderer.root
        .findAllByType('button')
        .find((b) => b.findAllByType('span').some((s) => s.children.includes(text)))!;

    await act(async () => {
      findButtonByText('ai_providers.enter_selection_mode').props.onClick();
    });

    const selectButtons = renderer.root.findAll((n) => Boolean(n.props?.['data-select-row']));
    expect(selectButtons).toHaveLength(2);
    for (const btn of selectButtons) {
      await act(async () => {
        btn.props.onClick();
      });
    }

    // Switch to Codex tab so Gemini row becomes hidden
    await act(async () => {
      renderer.root.findByProps({ 'data-toolbar-kind-codex': true }).props.onClick();
    });

    await act(async () => {
      findButtonByText('ai_providers.batch_disable').props.onClick();
    });
    await flush();

    expect(mocks.saveGeminiKeys).not.toHaveBeenCalled();
    expect(mocks.saveCodexConfigs).toHaveBeenCalledTimes(1);
    expect(mocks.saveCodexConfigs).toHaveBeenCalledWith([
      expect.objectContaining({
        name: 'Codex 备份',
        excludedModels: ['*'],
      }),
    ]);

    act(() => renderer.unmount());
  });

  it('keeps drawer open on failed delete and closes it only after per-item delete succeeds with refreshed etag', async () => {
    const initialV8Source = {
      family: 'gemini' as const,
      groupIndex: 0,
      keyIndex: 0,
      group: {},
      etag: '"v1"',
    };
    const remainingV8Source = {
      family: 'gemini' as const,
      groupIndex: 0,
      keyIndex: 0,
      group: {},
      etag: '"v2"',
    };

    mocks.config.geminiApiKeys = [
      { name: 'Gemini One', apiKey: 'gemini-key-1', baseUrl: 'https://g1.example', v8Source: initialV8Source },
      { name: 'Gemini Two', apiKey: 'gemini-key-2', baseUrl: 'https://g2.example', v8Source: initialV8Source },
    ];
    mocks.config.codexApiKeys = [];

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const firstKey = renderer.root.findAll((n) => Boolean(n.props?.['data-row-key']))[0].props[
      'data-row-key'
    ] as string;
    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': firstKey }).props.onClick();
    });
    expect(renderer.root.findByProps({ 'data-testid': 'provider-workspace-drawer' })).toBeTruthy();

    // Failed delete keeps drawer open
    mocks.deleteGeminiKey.mockRejectedValueOnce(new Error('network timeout'));
    mocks.showConfirmation.mockImplementationOnce(
      ({ onConfirm }: { onConfirm: () => Promise<void> }) => {
        void onConfirm();
      }
    );
    await act(async () => {
      renderer.root.findByProps({ 'data-drawer-delete': true }).props.onClick();
    });
    await flush();

    expect(mocks.deleteGeminiKey).toHaveBeenCalledWith(
      'gemini-key-1',
      'https://g1.example',
      initialV8Source
    );
    expect(
      renderer.root.findAll((n) => n.props?.['data-testid'] === 'provider-workspace-drawer')
    ).toHaveLength(1);

    // Successful delete closes drawer and reloads fresh v8Source etag for remaining row
    mocks.deleteGeminiKey.mockImplementationOnce(async () => {
      mocks.config.geminiApiKeys = [
        {
          name: 'Gemini Two',
          apiKey: 'gemini-key-2',
          baseUrl: 'https://g2.example',
          v8Source: remainingV8Source,
        },
      ];
    });
    mocks.showConfirmation.mockImplementationOnce(
      ({ onConfirm }: { onConfirm: () => Promise<void> }) => {
        void onConfirm();
      }
    );
    await act(async () => {
      renderer.root.findByProps({ 'data-drawer-delete': true }).props.onClick();
    });
    await flush();

    expect(
      renderer.root.findAll((n) => n.props?.['data-testid'] === 'provider-workspace-drawer')
    ).toHaveLength(0);

    const remainingKey = renderer.root.findAll((n) => Boolean(n.props?.['data-row-key']))[0].props[
      'data-row-key'
    ] as string;
    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': remainingKey }).props.onClick();
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'provider-workspace-drawer' }).props['data-row-etag']
    ).toBe('"v2"');

    act(() => renderer.unmount());
  });

  it('re-anchors drawerRowKey after identity-changing edits via savedIdentity', async () => {
    mocks.config.geminiApiKeys = [
      { name: 'Old Alias', apiKey: 'sk-old-key', baseUrl: 'https://old.example' },
    ];
    mocks.config.openaiCompatibility = [
      {
        name: 'Old OpenAI',
        baseUrl: 'https://openai.example/v1',
        apiKeyEntries: [{ apiKey: 'sk-oa-1' }],
      },
    ];
    mocks.getOpenAIProviders.mockImplementation(async () => mocks.config.openaiCompatibility);

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const geminiRowKey = renderer.root
      .findAll((n) => Boolean(n.props?.['data-row-key']))
      .find((n) => String(n.props['data-row-key']).startsWith('gemini:'))!.props[
      'data-row-key'
    ] as string;
    await act(async () => {
      renderer.root.findByProps({ 'data-edit': geminiRowKey }).props.onClick();
    });

    mocks.config.geminiApiKeys = [
      { name: 'New Alias', apiKey: 'sk-new-key', baseUrl: 'https://new.example' },
    ];
    mocks.pendingSavedIdentity = {
      name: 'New Alias',
      apiKey: 'sk-new-key',
      baseUrl: 'https://new.example',
    };
    await act(async () => {
      renderer.root.findByProps({ 'data-drawer-save-identity': true }).props.onClick();
    });
    await flush();

    const drawerAfterSingleEdit = renderer.root.findByProps({
      'data-testid': 'provider-workspace-drawer',
    });
    expect(drawerAfterSingleEdit.props['data-row-label']).toBe('New Alias');
    expect(drawerAfterSingleEdit.props['data-row-key']).toBe(
      'gemini:sk-new-key::https://new.example::::0'
    );

    // Also verify OpenAI rename keeps drawer anchored
    const openaiRowKey = renderer.root
      .findAll((n) => Boolean(n.props?.['data-row-key']))
      .find((n) => String(n.props['data-row-key']).startsWith('openai:'))!.props[
      'data-row-key'
    ] as string;
    await act(async () => {
      renderer.root.findByProps({ 'data-edit': openaiRowKey }).props.onClick();
    });

    mocks.config.openaiCompatibility = [
      {
        name: 'Renamed OpenAI',
        baseUrl: 'https://openai.example/v1',
        apiKeyEntries: [{ apiKey: 'sk-oa-1' }],
      },
    ];
    mocks.pendingSavedIdentity = {
      name: 'Renamed OpenAI',
      baseUrl: 'https://openai.example/v1',
    };
    await act(async () => {
      renderer.root.findByProps({ 'data-drawer-save-identity': true }).props.onClick();
    });
    await flush();

    const drawerAfterOpenAIEdit = renderer.root.findByProps({
      'data-testid': 'provider-workspace-drawer',
    });
    expect(drawerAfterOpenAIEdit.props['data-row-label']).toBe('Renamed OpenAI');
    expect(drawerAfterOpenAIEdit.props['data-row-key']).toBe(
      'openai:Renamed OpenAI::https://openai.example/v1::::0'
    );

    act(() => renderer.unmount());
  });

  it('sets v8AliasSupported to false on legacy non-v8 config loads', async () => {
    mocks.config = {
      geminiApiKeys: [{ name: 'Legacy Gemini', apiKey: 'legacy-key-1' }],
      interactionsApiKeys: [],
      codexApiKeys: [],
      xaiApiKeys: [],
      claudeApiKeys: [],
      vertexApiKeys: [],
      openaiCompatibility: [],
      v8Supported: false,
    } as typeof mocks.config & { v8Supported?: boolean };

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const rowKey = renderer.root.findAll((n) => Boolean(n.props?.['data-row-key']))[0].props[
      'data-row-key'
    ] as string;
    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': rowKey }).props.onClick();
    });

    expect(
      renderer.root.findByProps({ 'data-testid': 'provider-workspace-drawer' }).props[
        'data-v8-supported'
      ]
    ).toBe('false');

    act(() => renderer.unmount());

    // Also verify omitting v8Supported without v8Source sets v8AliasSupported to false
    mocks.config = {
      geminiApiKeys: [{ name: 'Legacy No Flag', apiKey: 'legacy-key-2' }],
      interactionsApiKeys: [],
      codexApiKeys: [],
      xaiApiKeys: [],
      claudeApiKeys: [],
      vertexApiKeys: [],
      openaiCompatibility: [],
    };
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const nextRowKey = renderer.root.findAll((n) => Boolean(n.props?.['data-row-key']))[0].props[
      'data-row-key'
    ] as string;
    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': nextRowKey }).props.onClick();
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'provider-workspace-drawer' }).props[
        'data-v8-supported'
      ]
    ).toBe('false');

    act(() => renderer.unmount());
  });

  it('refreshes v8Source etag after commitChangedProviderLists on inline toggle', async () => {
    const v1Source = {
      family: 'gemini' as const,
      groupIndex: 0,
      keyIndex: 0,
      group: {},
      etag: '"etag-v1"',
    };
    const v2Source = {
      family: 'gemini' as const,
      groupIndex: 0,
      keyIndex: 0,
      group: {},
      etag: '"etag-v2"',
    };

    mocks.config.geminiApiKeys = [
      { name: 'V8 Gemini', apiKey: 'v8-key-1', v8Source: v1Source },
    ];
    mocks.config.codexApiKeys = [];
    mocks.saveGeminiKeys.mockImplementationOnce(async (nextList: GeminiKeyConfig[]) => {
      mocks.config.geminiApiKeys = nextList.map((entry) => ({ ...entry, v8Source: v2Source }));
    });

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const rowKey = renderer.root.findAll((n) => Boolean(n.props?.['data-row-key']))[0].props[
      'data-row-key'
    ] as string;
    await act(async () => {
      renderer.root.findByProps({ 'data-toggle': rowKey }).props.onClick();
    });
    await flush();

    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': rowKey }).props.onClick();
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'provider-workspace-drawer' }).props['data-row-etag']
    ).toBe('"etag-v2"');

    act(() => renderer.unmount());
  });

  it('reads fresh provider state from refs inside delete onConfirm after an intervening refresh', async () => {
    const staleV8Source = {
      family: 'gemini' as const,
      groupIndex: 0,
      keyIndex: 0,
      group: {},
      etag: '"stale-etag"',
    };
    const freshV8Source = {
      family: 'gemini' as const,
      groupIndex: 0,
      keyIndex: 0,
      group: {},
      etag: '"fresh-etag"',
    };

    mocks.config.geminiApiKeys = [
      { name: 'Target Gemini', apiKey: 'target-key', baseUrl: 'https://g.example', v8Source: staleV8Source },
    ];
    mocks.config.codexApiKeys = [];

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const rowKey = renderer.root.findAll((n) => Boolean(n.props?.['data-row-key']))[0].props[
      'data-row-key'
    ] as string;
    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': rowKey }).props.onClick();
    });

    let capturedConfirm: (() => Promise<void>) | undefined;
    mocks.showConfirmation.mockImplementationOnce(({ onConfirm }: { onConfirm: () => Promise<void> }) => {
      capturedConfirm = onConfirm;
    });

    await act(async () => {
      renderer.root.findByProps({ 'data-drawer-delete': true }).props.onClick();
    });
    expect(capturedConfirm).toBeDefined();

    // Simulate an intervening refresh updating v8Source/etag before user confirms
    mocks.config.geminiApiKeys = [
      { name: 'Target Gemini', apiKey: 'target-key', baseUrl: 'https://g.example', v8Source: freshV8Source },
    ];
    await act(async () => {
      renderer.root.findByProps({ 'data-toolbar-refresh': true }).props.onClick();
    });
    await flush();

    mocks.deleteGeminiKey.mockImplementationOnce(async () => {
      mocks.config.geminiApiKeys = [];
    });
    await act(async () => {
      await capturedConfirm?.();
    });
    await flush();

    expect(mocks.deleteGeminiKey).toHaveBeenCalledWith(
      'target-key',
      'https://g.example',
      freshV8Source
    );

    act(() => renderer.unmount());
  });

  it('chains advancing ETag across multi-family batch writes, seeds expectedEtag on full-family delete, syncs untouched families post-delete, and reloads on 412/partial failure', async () => {
    let latestEtag = '"h1"';
    const makeV8 = (family: ProviderV8GroupSource['family'], groupIndex: number, etag: string): ProviderV8GroupSource => ({
      family,
      groupIndex,
      keyIndex: 0,
      group: {},
      etag,
    });
    mocks.getLatestV8ConfigEtag.mockImplementation(() => latestEtag);
    mocks.config = {
      geminiApiKeys: [{ name: 'G1', apiKey: 'gk-1', v8Source: makeV8('gemini', 0, '"h1"') }],
      interactionsApiKeys: [],
      codexApiKeys: [{ name: 'C1', apiKey: 'ck-1', v8Source: makeV8('codex', 0, '"h1"') }],
      xaiApiKeys: [],
      claudeApiKeys: [{ name: 'Untouched Claude', apiKey: 'ak-1', v8Source: makeV8('claude', 0, '"h1"') }],
      vertexApiKeys: [],
      openaiCompatibility: [],
    };
    mocks.loadAllProvidersV8Snapshot.mockImplementation(async () => ({
      gemini: mocks.config.geminiApiKeys,
      interactions: mocks.config.interactionsApiKeys,
      codex: mocks.config.codexApiKeys,
      xai: mocks.config.xaiApiKeys,
      claude: mocks.config.claudeApiKeys,
      vertex: mocks.config.vertexApiKeys,
      openai: mocks.config.openaiCompatibility,
      v8Supported: true,
    }));

    mocks.saveGeminiKeys.mockImplementationOnce(async (nextList: GeminiKeyConfig[]) => {
      latestEtag = '"h2"';
      mocks.config.geminiApiKeys = nextList.map((item) => ({
        ...item,
        v8Source: makeV8('gemini', 0, '"h2"'),
      }));
      return { ok: true, etag: '"h2"' };
    });
    mocks.saveCodexConfigs.mockImplementationOnce(async (nextList: ProviderKeyConfig[]) => {
      latestEtag = '"h3"';
      mocks.config.codexApiKeys = nextList.map((item) => ({
        ...item,
        v8Source: makeV8('codex', 0, '"h3"'),
      }));
      // Deliberately leave mocks.config.claudeApiKeys at "h1" to verify syncAllListsEtag updates untouched families!
      return { ok: true, etag: '"h3"' };
    });

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const findButtonByText = (text: string) =>
      renderer.root
        .findAllByType('button')
        .find((b) => b.findAllByType('span').some((s) => s.children.includes(text)))!;

    await act(async () => {
      findButtonByText('ai_providers.enter_selection_mode').props.onClick();
    });
    const rowKeys = renderer.root
      .findAll((n) => Boolean(n.props?.['data-select-row']))
      .map((n) => n.props['data-select-row'] as string);
    const geminiSelect = rowKeys.find((k) => k.startsWith('gemini:'))!;
    const codexSelect = rowKeys.find((k) => k.startsWith('codex:'))!;
    const claudeRowKey = rowKeys.find((k) => k.startsWith('claude:'))!;

    await act(async () => {
      renderer.root.findByProps({ 'data-select-row': geminiSelect }).props.onClick();
      renderer.root.findByProps({ 'data-select-row': codexSelect }).props.onClick();
    });
    await act(async () => {
      findButtonByText('ai_providers.batch_disable').props.onClick();
    });
    await flush();

    expect(mocks.saveGeminiKeys).toHaveBeenCalledWith(
      expect.any(Array),
      { expectedEtag: '"h1"' }
    );
    expect(mocks.saveCodexConfigs).toHaveBeenCalledWith(
      expect.any(Array),
      { expectedEtag: '"h2"' }
    );
    // Untouched claude row synced to "h3"
    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': claudeRowKey }).props.onClick();
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'provider-workspace-drawer' }).props['data-row-etag']
    ).toBe('"h3"');
    expect(
      renderer.root.findByProps({ 'data-testid': 'provider-workspace-drawer' }).props[
        'data-resolved-theme'
      ]
    ).toBe('light');

    // Full-family batch delete of codex ([] empty array) STILL passes { expectedEtag: '"h3"' } seeded from previous
    mocks.saveCodexConfigs.mockImplementationOnce(async () => {
      latestEtag = '"h4"';
      mocks.config.codexApiKeys = [];
      return { ok: true, etag: '"h4"' };
    });
    await act(async () => {
      findButtonByText('ai_providers.clear_selected').props.onClick();
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-select-row': codexSelect }).props.onClick();
    });
    let batchDeleteConfirm: (() => Promise<void>) | undefined;
    mocks.showConfirmation.mockImplementationOnce(
      ({ onConfirm }: { onConfirm: () => Promise<void> }) => {
        batchDeleteConfirm = onConfirm;
      }
    );
    const batchMenu = renderer.root.find(
      (node) =>
        Array.isArray(node.props?.items) &&
        node.props.items.some((item: { key?: string }) => item.key === 'batch-delete')
    );
    const batchDeleteItem = batchMenu.props.items.find(
      (item: { key?: string }) => item.key === 'batch-delete'
    );
    await act(async () => {
      batchDeleteItem.onClick();
    });
    await act(async () => {
      await batchDeleteConfirm?.();
    });
    await flush();
    expect(mocks.saveCodexConfigs).toHaveBeenLastCalledWith([], { expectedEtag: '"h3"' });

    // Per-item delete syncs untouched claude etag to "h5" even when deleted family becomes empty []
    mocks.deleteGeminiKey.mockImplementationOnce(async () => {
      latestEtag = '"h5"';
      mocks.config.geminiApiKeys = [];
      return { ok: true, etag: '"h5"' };
    });
    let rowDeleteConfirm: (() => Promise<void>) | undefined;
    mocks.showConfirmation.mockImplementationOnce(
      ({ onConfirm }: { onConfirm: () => Promise<void> }) => {
        rowDeleteConfirm = onConfirm;
      }
    );
    await act(async () => {
      renderer.root.findByProps({ 'data-delete': geminiSelect }).props.onClick();
    });
    await act(async () => {
      await rowDeleteConfirm?.();
    });
    await flush();
    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': claudeRowKey }).props.onClick();
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'provider-workspace-drawer' }).props['data-row-etag']
    ).toBe('"h5"');

    // Partial failure mid-batch (write #1 succeeds, write #2 fails with 412) reloads live server state instead of reverting to stale previous
    mocks.config.geminiApiKeys = [
      { name: 'G2', apiKey: 'gk-2', v8Source: makeV8('gemini', 0, '"h5"') },
    ];
    mocks.config.claudeApiKeys = [
      { name: 'Untouched Claude', apiKey: 'ak-1', v8Source: makeV8('claude', 0, '"h5"') },
    ];
    await act(async () => {
      renderer.root.findByProps({ 'data-toolbar-refresh': true }).props.onClick();
    });
    await flush();

    mocks.saveGeminiKeys.mockImplementationOnce(async (nextList: GeminiKeyConfig[]) => {
      latestEtag = '"h6"';
      mocks.config.geminiApiKeys = nextList.map((item) => ({
        ...item,
        v8Source: makeV8('gemini', 0, '"h6"'),
      }));
      return { ok: true, etag: '"h6"' };
    });
    mocks.saveClaudeConfigs.mockRejectedValueOnce(
      Object.assign(new Error('ETag mismatch'), { status: 412 })
    );

    const loadCallsBeforePartial = mocks.loadAllProvidersV8Snapshot.mock.calls.length;
    await act(async () => {
      findButtonByText('ai_providers.enter_selection_mode').props.onClick();
    });
    const nextSelects = renderer.root
      .findAll((n) => Boolean(n.props?.['data-select-row']))
      .map((n) => n.props['data-select-row'] as string);
    for (const key of nextSelects) {
      await act(async () => {
        renderer.root.findByProps({ 'data-select-row': key }).props.onClick();
      });
    }
    await act(async () => {
      findButtonByText('ai_providers.batch_disable').props.onClick();
    });
    await flush();

    expect(mocks.loadAllProvidersV8Snapshot.mock.calls.length).toBeGreaterThan(
      loadCallsBeforePartial
    );
    // Gemini remained disabled (from server state) instead of being rolled back to enabled!
    const latestGeminiRow = renderer.root
      .findAll((n) => Boolean(n.props?.['data-row-key']))
      .find((n) => String(n.props['data-row-key']).startsWith('gemini:'))!;
    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': latestGeminiRow.props['data-row-key'] }).props.onClick();
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'provider-workspace-drawer' }).props['data-row-etag']
    ).toBe('"h6"');

    act(() => renderer.unmount());
  });

  it('awaits dirty-draft confirmation before switching drawer row via ProviderHealthCheckDrawer onOpenProviderConfig', async () => {
    mocks.config = {
      geminiApiKeys: [
        { name: 'First Gemini', apiKey: 'gk-1' },
        { name: 'Second Gemini', apiKey: 'gk-2' },
      ],
      interactionsApiKeys: [],
      codexApiKeys: [],
      xaiApiKeys: [],
      claudeApiKeys: [],
      vertexApiKeys: [],
      openaiCompatibility: [],
    };

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const rowKeys = renderer.root
      .findAll((n) => Boolean(n.props?.['data-row-key']))
      .map((n) => n.props['data-row-key'] as string);

    // Open drawer on Row 0 and open health check drawer
    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': rowKeys[0] }).props.onClick();
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-toolbar-health': true }).props.onClick();
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'provider-health-drawer' }).props[
        'data-all-rows-count'
      ]
    ).toBe(2);

    // Cancel discard -> keeps Row 0 open and keeps Health Check Drawer open
    mocks.requestCloseOrSwitch.mockResolvedValueOnce(false);
    await act(async () => {
      renderer.root.findByProps({ 'data-health-open-config-index': 1 }).props.onClick();
    });
    await flush();
    expect(mocks.requestCloseOrSwitch).toHaveBeenCalledTimes(1);
    expect(
      renderer.root.findByProps({ 'data-testid': 'provider-workspace-drawer' }).props['data-row-key']
    ).toBe(rowKeys[0]);
    expect(
      renderer.root.findAll((n) => n.props?.['data-testid'] === 'provider-health-drawer')
    ).toHaveLength(1);

    // Confirm discard -> switches drawer to Row 1 on 'config' tab and closes Health Check Drawer
    mocks.requestCloseOrSwitch.mockResolvedValueOnce(true);
    await act(async () => {
      renderer.root.findByProps({ 'data-health-open-config-index': 1 }).props.onClick();
    });
    await flush();
    expect(
      renderer.root.findByProps({ 'data-testid': 'provider-workspace-drawer' }).props
    ).toMatchObject({
      'data-row-key': rowKeys[1],
      'data-tab': 'config',
    });
    expect(
      renderer.root.findAll((n) => n.props?.['data-testid'] === 'provider-health-drawer')
    ).toHaveLength(0);

    act(() => renderer.unmount());
  });

  it('skips syncAllListsEtag on savedKind when post-save loadConfigs fails while still syncing other families', async () => {
    const v1ClaudeSource: ProviderV8GroupSource = {
      family: 'claude',
      groupIndex: 0,
      keyIndex: 0,
      group: { name: 'Pre-Save Claude' },
      etag: '"etag-1"',
    };
    const v1GeminiSource: ProviderV8GroupSource = {
      family: 'gemini',
      groupIndex: 0,
      keyIndex: 0,
      group: { name: 'Sibling Gemini' },
      etag: '"etag-1"',
    };

    mocks.config = {
      geminiApiKeys: [
        {
          name: 'Sibling Gemini',
          apiKey: 'gemini-key-1',
          baseUrl: 'https://gemini.example',
          v8Source: v1GeminiSource,
        },
      ],
      interactionsApiKeys: [],
      codexApiKeys: [],
      xaiApiKeys: [],
      claudeApiKeys: [
        {
          name: 'Pre-Save Claude',
          apiKey: 'claude-key-1',
          baseUrl: 'https://claude.example',
          v8Source: v1ClaudeSource,
        },
      ],
      vertexApiKeys: [],
      openaiCompatibility: [],
    };

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const rowKeys = renderer.root
      .findAll((n) => Boolean(n.props?.['data-row-key']))
      .map((n) => String(n.props['data-row-key']));
    const claudeKey = rowKeys.find((key) => key.startsWith('claude:'))!;
    const geminiKey = rowKeys.find((key) => key.startsWith('gemini:'))!;
    expect(claudeKey).toBeTruthy();
    expect(geminiKey).toBeTruthy();

    await act(async () => {
      renderer.root.findByProps({ 'data-edit': claudeKey }).props.onClick();
    });

    // Transient post-save refresh failure -> loadConfigs returns null
    mocks.loadAllProvidersV8Snapshot.mockRejectedValueOnce(new Error('transient refresh failure'));
    mocks.fetchConfig.mockRejectedValueOnce(new Error('transient refresh failure'));
    mocks.getLatestV8ConfigEtag.mockReturnValue('"etag-2"');
    mocks.pendingSavedIdentity = {
      name: 'Committed Claude',
      apiKey: 'claude-key-1',
      baseUrl: 'https://claude.example',
      v8Source: {
        ...v1ClaudeSource,
        etag: '"etag-2"',
      },
    };

    await act(async () => {
      renderer.root.findByProps({ 'data-drawer-save-identity': true }).props.onClick();
    });
    await flush();

    // Saved family must NOT be cloned into stale-fields + new-ETag when refresh failed
    const drawerAfterFailedRefresh = renderer.root.findByProps({
      'data-testid': 'provider-workspace-drawer',
    });
    expect(drawerAfterFailedRefresh.props['data-row-etag']).toBe('"etag-1"');

    // Other families still receive the rotated global ETag
    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': geminiKey }).props.onClick();
    });
    await flush();
    const geminiDrawer = renderer.root.findByProps({
      'data-testid': 'provider-workspace-drawer',
    });
    expect(geminiDrawer.props['data-row-etag']).toBe('"etag-2"');

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
    mocks.getVertexConfigs.mockResolvedValue([]);
    mocks.getOpenAIProviders.mockResolvedValue([]);
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

    mocks.transitionLayer = { status: 'background', isCurrentLayer: false };
    await updatePage(renderer);

    mocks.cacheValid = false;
    mocks.transitionLayer = { status: 'current', isCurrentLayer: true };
    await updatePage(renderer);

    expect(mocks.fetchConfig).toHaveBeenCalledTimes(2);
    act(() => renderer.unmount());
  });

  it('does not reload provider configs when the cache is still valid on re-entry', async () => {
    const renderer = await renderPage();
    expect(mocks.fetchConfig).toHaveBeenCalledTimes(1);

    mocks.transitionLayer = { status: 'background', isCurrentLayer: false };
    await updatePage(renderer);

    mocks.transitionLayer = { status: 'current', isCurrentLayer: true };
    await updatePage(renderer);

    expect(mocks.fetchConfig).toHaveBeenCalledTimes(1);
    act(() => renderer.unmount());
  });
});

describe('AiProvidersPage error presentation & pagination', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.cacheValid = false;
    mocks.transitionLayer = null;
  });

  it('renders error-box below PageControlBar when Manager returns 428', async () => {
    mocks.config = {
      geminiApiKeys: [],
      interactionsApiKeys: [],
      codexApiKeys: [],
      xaiApiKeys: [],
      claudeApiKeys: [],
      vertexApiKeys: [],
      openaiCompatibility: [],
    };
    mocks.fetchConfig.mockRejectedValue(
      Object.assign(new Error('usage service is not configured'), { status: 428 })
    );
    mocks.getVertexConfigs.mockResolvedValue([]);
    mocks.getOpenAIProviders.mockResolvedValue([]);

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const json = JSON.stringify(renderer.toJSON());
    expect(renderer.root.findByProps({ className: 'error-box' }).children.join('')).toBe(
      'usage_service_errors.usage_service_not_configured'
    );
    expect(json.indexOf('provider-toolbar')).toBeLessThan(json.indexOf('error-box'));
    act(() => renderer.unmount());
  });

  it('renders PaginationFooter with inset variant, page jump, and page-size reset', async () => {
    mocks.cacheValid = true;
    mocks.config = {
      geminiApiKeys: Array.from({ length: 12 }, (_, index) => ({
        apiKey: `gemini-key-${index + 1}`,
      })),
      interactionsApiKeys: [],
      codexApiKeys: [],
      xaiApiKeys: [],
      claudeApiKeys: [],
      vertexApiKeys: [],
      openaiCompatibility: [],
    };
    mocks.fetchConfig.mockImplementation(async () => mocks.config);
    mocks.getVertexConfigs.mockResolvedValue([]);
    mocks.getOpenAIProviders.mockResolvedValue([]);

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const footer = renderer.root.findByType(PaginationFooter);
    expect(footer.props.variant).toBe('flush');
    expect(footer.props.currentPage).toBe(1);
    expect(footer.props.totalPages).toBe(2);
    expect(footer.props.showPageJump).toBe(true);

    await act(async () => {
      footer.props.onPageChange(2);
    });
    expect(renderer.root.findByType(PaginationFooter).props.currentPage).toBe(2);

    await act(async () => {
      renderer.root.findByType(PaginationFooter).props.onPageSizeChange?.(20);
    });
    const updatedFooter = renderer.root.findByType(PaginationFooter);
    expect(updatedFooter.props.pageSize).toBe(20);
    expect(updatedFooter.props.currentPage).toBe(1);
    expect(updatedFooter.props.totalPages).toBe(1);
    expect(updatedFooter.props.showPageJump).toBe(true);

    act(() => renderer.unmount());
  });

  it('keeps PaginationFooter permanently mounted at 0 visible rows', async () => {
    mocks.cacheValid = true;
    mocks.config = {
      geminiApiKeys: [{ name: 'Single Gemini', apiKey: 'gemini-only-1' }],
      interactionsApiKeys: [],
      codexApiKeys: [],
      xaiApiKeys: [],
      claudeApiKeys: [],
      vertexApiKeys: [],
      openaiCompatibility: [],
    };
    mocks.fetchConfig.mockImplementation(async () => mocks.config);
    mocks.getVertexConfigs.mockResolvedValue([]);
    mocks.getOpenAIProviders.mockResolvedValue([]);

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    await act(async () => {
      renderer.root.findByProps({ 'data-toolbar-search-none': true }).props.onClick();
    });

    const footer = renderer.root.findByType(PaginationFooter);
    expect(footer.props.variant).toBe('flush');
    expect(footer.props.currentPage).toBe(1);
    expect(footer.props.totalPages).toBe(1);
    expect(footer.props.showPageJump).toBe(true);
    expect(mocks.t).toHaveBeenCalledWith('monitoring.pagination_info', {
      current: 1,
      total: 1,
      start: 0,
      end: 0,
      count: 0,
    });

    act(() => renderer.unmount());
  });

  it('has zero missing translation keys in AiProvidersPage.tsx across all 4 locales', () => {
    const staticKeys = [...aiProvidersPageSource.matchAll(/t\(\s*'([^']+)'/g)].map(
      (match) => match[1]
    );
    const kinds = ['gemini', 'interactions', 'codex', 'xai', 'claude', 'vertex', 'openai'];
    const requiredKeys = Array.from(
      new Set([
        ...staticKeys,
        ...kinds.map((kind) => `ai_providers.${kind}_delete_title`),
        ...kinds.map((kind) => `ai_providers.${kind}_delete_confirm`),
        'usage_service_errors.request_failed',
      ])
    );

    const locales: Array<[string, Record<string, unknown>]> = [
      ['zh-CN', zhCNLocale as Record<string, unknown>],
      ['zh-TW', zhTWLocale as Record<string, unknown>],
      ['en', enLocale as Record<string, unknown>],
      ['ru', ruLocale as Record<string, unknown>],
    ];
    for (const [locale, dict] of locales) {
      const missing = requiredKeys.filter((fullKey) => {
        let cursor: unknown = dict;
        for (const segment of fullKey.split('.')) {
          if (cursor && typeof cursor === 'object' && segment in cursor) {
            cursor = (cursor as Record<string, unknown>)[segment];
          } else {
            return true;
          }
        }
        return typeof cursor !== 'string' || cursor.length === 0;
      });
      expect(missing, `Missing keys in ${locale}.json`).toEqual([]);
    }
  });
});
describe('AiProvidersPage stateful store + real drawer integration', () => {
  const extractText = (node: ReactTestInstance): string =>
    node.children
      .map((child) =>
        typeof child === 'string' || typeof child === 'number' ? String(child) : extractText(child)
      )
      .join(' ');

  const findButtonByText = (root: ReactTestInstance, text: string) =>
    root.findAllByType('button').find((btn) => extractText(btn).trim().includes(text));

  const findTabByText = (root: ReactTestInstance, text: string) =>
    root
      .findAll((node) => node.type === 'button' && node.props.role === 'tab')
      .find((btn) => extractText(btn).includes(text));

  const findInputByLabel = (root: ReactTestInstance, labelSubstring: string) =>
    root.findAllByType('input').find((input) => {
      const label = input.parent?.parent?.findAllByType('label')[0];
      if (label && extractText(label).includes(labelSubstring)) return true;
      return String(input.props['aria-label'] ?? '').includes(labelSubstring);
    });

  const resetRealStore = () => {
    const storeWithState = useConfigStore as unknown as {
      getState: () => {
        clearCache: () => void;
        cache: Map<string, unknown>;
        isCacheValid: () => boolean;
      };
      setState: (partial: Record<string, unknown>) => void;
    };
    storeWithState.getState().clearCache();
    storeWithState.setState({ config: null, cache: new Map(), loading: false, error: null });
  };

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.useRealConfigStore = true;
    mocks.useRealDrawer = true;
    mocks.t.mockImplementation((key: string, options?: { defaultValue?: string }) =>
      mocks.useRealDrawer && options?.defaultValue ? options.defaultValue : key
    );
    mocks.transitionLayer = null;
    mocks.drawerPropsRef.current = null;
    mocks.getLatestV8ConfigEtag.mockReturnValue(undefined);
    resetRealStore();
  });

  afterEach(() => {
    mocks.useRealConfigStore = false;
    mocks.useRealDrawer = false;
  });

  it('keeps ProviderWorkspaceDrawer open, preserves active config tab, and re-anchors drawerRowKey on OpenAI rename and non-authIndex single-key save', async () => {
    let serverEtag = '"etag-1"';
    let serverOpenAI: OpenAIProviderConfig[] = [
      {
        name: 'OldOpenAI',
        baseUrl: 'https://oa.example.com/v1',
        apiKeyEntries: [{ apiKey: 'sk-oa-1' }],
        v8Source: { family: 'openai-compatibility', groupIndex: 0, group: {}, etag: serverEtag },
      },
    ];
    let serverClaude: ProviderKeyConfig[] = [
      {
        name: 'ClaudeOne',
        apiKey: 'sk-c-1',
        baseUrl: 'https://c1.example.com',
        v8Source: { family: 'claude', groupIndex: 0, keyIndex: 0, group: {}, etag: serverEtag },
      },
    ];

    const buildServerConfig = () => ({
      v8Supported: true,
      geminiApiKeys: [],
      interactionsApiKeys: [],
      codexApiKeys: [],
      xaiApiKeys: [],
      claudeApiKeys: serverClaude.map((item) => ({
        ...item,
        v8Source: { ...item.v8Source!, etag: serverEtag },
      })),
      vertexApiKeys: [],
      openaiCompatibility: serverOpenAI.map((item) => ({
        ...item,
        v8Source: { ...item.v8Source!, etag: serverEtag },
      })),
    });

    mocks.getConfig.mockImplementation(async () => buildServerConfig());
    mocks.getVertexConfigs.mockResolvedValue([]);
    mocks.getOpenAIProviders.mockImplementation(async () =>
      serverOpenAI.map((item) => ({
        ...item,
        v8Source: { ...item.v8Source!, etag: serverEtag },
      }))
    );
    mocks.getClaudeConfigs.mockImplementation(async () =>
      serverClaude.map((item) => ({
        ...item,
        v8Source: { ...item.v8Source!, etag: serverEtag },
      }))
    );
    mocks.updateOpenAIProvider.mockImplementation(async (_origName, idx, payload) => {
      serverEtag = serverEtag === '"etag-1"' ? '"etag-2"' : '"etag-3"';
      mocks.getLatestV8ConfigEtag.mockReturnValue(serverEtag);
      serverOpenAI = serverOpenAI.map((item, i) =>
        i === idx
          ? { ...payload, v8Source: { ...item.v8Source!, etag: serverEtag } }
          : item
      );
    });
    mocks.updateClaudeConfig.mockImplementation(async (_existing, payload) => {
      serverEtag = serverEtag === '"etag-2"' ? '"etag-3"' : '"etag-4"';
      mocks.getLatestV8ConfigEtag.mockReturnValue(serverEtag);
      serverClaude = [{ ...payload, v8Source: { ...serverClaude[0].v8Source!, etag: serverEtag } }];
    });

    // Make snapshotLoader settle slightly after fetchConfig so mid-save store update is also exercised
    mocks.loadAllProvidersV8Snapshot.mockImplementation(async () => {
      await Promise.resolve();
      const cfg = buildServerConfig();
      return {
        v8Supported: true,
        gemini: [],
        interactions: [],
        codex: [],
        xai: [],
        claude: cfg.claudeApiKeys,
        vertex: [],
        openai: cfg.openaiCompatibility,
      };
    });

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const initialOpenAIKey = 'openai:OldOpenAI::https://oa.example.com/v1::::0';
    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': initialOpenAIKey }).props.onClick();
    });
    await flush();

    expect(renderer.root.findByProps({ 'data-testid': 'drawer-shell' })).toBeTruthy();
    await act(async () => {
      findTabByText(renderer.root, '配置')!.props.onClick();
    });

    // Rename OpenAI provider (changes row.key from OldOpenAI -> RenamedOpenAI)
    await act(async () => {
      findInputByLabel(renderer.root, '名称 / 别名')!.props.onChange({
        target: { value: 'RenamedOpenAI' },
      });
    });
    await act(async () => {
      await findButtonByText(renderer.root, '保存配置')!.props.onClick();
    });
    await flush();

    // Drawer must stay open, remain on Config tab, and table key must be re-anchored
    expect(renderer.root.findByProps({ 'data-testid': 'drawer-shell' })).toBeTruthy();
    const selectedTabAfterOpenAISave = renderer.root
      .findAll((node) => node.type === 'button' && node.props.role === 'tab')
      .find((btn) => btn.props['aria-selected'] === true);
    expect(extractText(selectedTabAfterOpenAISave!)).toContain('配置');
    expect(findInputByLabel(renderer.root, '名称 / 别名')?.props.value).toBe('RenamedOpenAI');
    expect(
      renderer.root.findAllByProps({
        'data-row-key': 'openai:RenamedOpenAI::https://oa.example.com/v1::::0',
      })
    ).toHaveLength(1);

    // Close and now test non-authIndex single-key (Claude) changing apiKey & baseUrl
    await act(async () => {
      mocks.drawerPropsRef.current?.onClose?.();
    });
    await flush();

    const initialClaudeKey = 'claude:sk-c-1::https://c1.example.com::::0';
    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': initialClaudeKey }).props.onClick();
    });
    await flush();

    await act(async () => {
      findTabByText(renderer.root, '配置')!.props.onClick();
    });
    await act(async () => {
      findInputByLabel(renderer.root, 'API Key')!.props.onChange({
        target: { value: 'sk-c-2' },
      });
      findInputByLabel(renderer.root, 'ai_providers.claude_add_modal_url_label')!.props.onChange({
        target: { value: 'https://c2.example.com' },
      });
    });
    await act(async () => {
      await findButtonByText(renderer.root, '保存配置')!.props.onClick();
    });
    await flush();

    expect(renderer.root.findByProps({ 'data-testid': 'drawer-shell' })).toBeTruthy();
    const selectedTabAfterClaudeSave = renderer.root
      .findAll((node) => node.type === 'button' && node.props.role === 'tab')
      .find((btn) => btn.props['aria-selected'] === true);
    expect(extractText(selectedTabAfterClaudeSave!)).toContain('配置');
    expect(findInputByLabel(renderer.root, 'API Key')?.props.value).toBe('sk-c-2');
    expect(
      renderer.root.findAllByProps({
        'data-row-key': 'claude:sk-c-2::https://c2.example.com::::0',
      })
    ).toHaveLength(1);

    act(() => renderer.unmount());
  });

  it('keeps ProviderWorkspaceDrawer open and re-anchors row.key when fetchConfig(undefined, true) rejects during handleDrawerSaved', async () => {
    let serverEtag = '"etag-1"';
    let serverOpenAI: OpenAIProviderConfig[] = [
      {
        name: 'OldOpenAI',
        baseUrl: 'https://oa.example.com/v1',
        apiKeyEntries: [{ apiKey: 'sk-oa-1' }],
        v8Source: { family: 'openai-compatibility', groupIndex: 0, group: {}, etag: serverEtag },
      },
    ];
    let serverClaude: ProviderKeyConfig[] = [
      {
        name: 'ClaudeOne',
        apiKey: 'sk-c-1',
        baseUrl: 'https://c1.example.com',
        v8Source: { family: 'claude', groupIndex: 0, keyIndex: 0, group: {}, etag: serverEtag },
      },
    ];

    const buildServerConfig = () => ({
      v8Supported: true,
      geminiApiKeys: [],
      interactionsApiKeys: [],
      codexApiKeys: [],
      xaiApiKeys: [],
      claudeApiKeys: serverClaude.map((item) => ({
        ...item,
        v8Source: { ...item.v8Source!, etag: serverEtag },
      })),
      vertexApiKeys: [],
      openaiCompatibility: serverOpenAI.map((item) => ({
        ...item,
        v8Source: { ...item.v8Source!, etag: serverEtag },
      })),
    });

    // Initial mount config load succeeds; forced refresh during handleDrawerSaved rejects
    mocks.getConfig
      .mockResolvedValueOnce(buildServerConfig())
      .mockRejectedValue(new Error('forced refresh failed'));
    mocks.getVertexConfigs.mockResolvedValue([]);
    mocks.getOpenAIProviders.mockImplementation(async () =>
      serverOpenAI.map((item) => ({
        ...item,
        v8Source: { ...item.v8Source!, etag: serverEtag },
      }))
    );
    mocks.getClaudeConfigs.mockImplementation(async () =>
      serverClaude.map((item) => ({
        ...item,
        v8Source: { ...item.v8Source!, etag: serverEtag },
      }))
    );
    mocks.updateOpenAIProvider.mockImplementation(async (_origName, idx, payload) => {
      serverEtag = '"etag-2"';
      mocks.getLatestV8ConfigEtag.mockReturnValue(serverEtag);
      serverOpenAI = serverOpenAI.map((item, i) =>
        i === idx
          ? { ...payload, v8Source: { ...item.v8Source!, etag: serverEtag } }
          : item
      );
    });
    mocks.updateClaudeConfig.mockImplementation(async (_existing, payload) => {
      serverEtag = '"etag-3"';
      mocks.getLatestV8ConfigEtag.mockReturnValue(serverEtag);
      serverClaude = [{ ...payload, v8Source: { ...serverClaude[0].v8Source!, etag: serverEtag } }];
    });
    mocks.loadAllProvidersV8Snapshot.mockImplementation(async () => {
      const cfg = buildServerConfig();
      return {
        v8Supported: true,
        gemini: [],
        interactions: [],
        codex: [],
        xai: [],
        claude: cfg.claudeApiKeys,
        vertex: [],
        openai: cfg.openaiCompatibility,
      };
    });

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    const initialOpenAIKey = 'openai:OldOpenAI::https://oa.example.com/v1::::0';
    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': initialOpenAIKey }).props.onClick();
    });
    await flush();

    expect(renderer.root.findByProps({ 'data-testid': 'drawer-shell' })).toBeTruthy();
    await act(async () => {
      findTabByText(renderer.root, '配置')!.props.onClick();
    });

    // Rename OpenAI provider (changes row.key OldOpenAI -> RenamedOpenAI) while fetchConfig(..., true) rejects
    await act(async () => {
      findInputByLabel(renderer.root, '名称 / 别名')!.props.onChange({
        target: { value: 'RenamedOpenAI' },
      });
    });
    await act(async () => {
      await findButtonByText(renderer.root, '保存配置')!.props.onClick();
    });
    await flush();
    await flush();

    expect(mocks.getConfig).toHaveBeenCalledTimes(2);
    expect(renderer.root.findByProps({ 'data-testid': 'drawer-shell' })).toBeTruthy();
    const selectedTabAfterOpenAISave = renderer.root
      .findAll((node) => node.type === 'button' && node.props.role === 'tab')
      .find((btn) => btn.props['aria-selected'] === true);
    expect(extractText(selectedTabAfterOpenAISave!)).toContain('配置');
    expect(findInputByLabel(renderer.root, '名称 / 别名')?.props.value).toBe('RenamedOpenAI');
    expect(
      renderer.root.findAllByProps({
        'data-row-key': 'openai:RenamedOpenAI::https://oa.example.com/v1::::0',
      })
    ).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({
        'data-row-key': initialOpenAIKey,
      })
    ).toHaveLength(0);

    // Close and repeat with non-authIndex Claude changing apiKey + baseUrl while fetchConfig(..., true) rejects
    await act(async () => {
      mocks.drawerPropsRef.current?.onClose?.();
    });
    await flush();

    const initialClaudeKey = 'claude:sk-c-1::https://c1.example.com::::0';
    await act(async () => {
      renderer.root.findByProps({ 'data-inspect': initialClaudeKey }).props.onClick();
    });
    await flush();

    await act(async () => {
      findTabByText(renderer.root, '配置')!.props.onClick();
    });
    await act(async () => {
      findInputByLabel(renderer.root, 'API Key')!.props.onChange({
        target: { value: 'sk-c-2' },
      });
      findInputByLabel(renderer.root, 'ai_providers.claude_add_modal_url_label')!.props.onChange({
        target: { value: 'https://c2.example.com' },
      });
    });
    await act(async () => {
      await findButtonByText(renderer.root, '保存配置')!.props.onClick();
    });
    await flush();
    await flush();

    expect(mocks.getConfig).toHaveBeenCalledTimes(3);
    expect(renderer.root.findByProps({ 'data-testid': 'drawer-shell' })).toBeTruthy();
    const selectedTabAfterClaudeSave = renderer.root
      .findAll((node) => node.type === 'button' && node.props.role === 'tab')
      .find((btn) => btn.props['aria-selected'] === true);
    expect(extractText(selectedTabAfterClaudeSave!)).toContain('配置');
    expect(findInputByLabel(renderer.root, 'API Key')?.props.value).toBe('sk-c-2');
    expect(
      findInputByLabel(renderer.root, 'ai_providers.claude_add_modal_url_label')?.props.value
    ).toBe('https://c2.example.com');
    expect(
      renderer.root.findAllByProps({
        'data-row-key': 'claude:sk-c-2::https://c2.example.com::::0',
      })
    ).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({
        'data-row-key': initialClaudeKey,
      })
    ).toHaveLength(0);

    act(() => renderer.unmount());
  });

  it('re-anchors dirty open non-authIndex row when sibling row 0 is deleted externally (even with an identical-name sibling shifting into index 1) and protects dirty draft on true deletion', async () => {
    const storeControl = useConfigStore as unknown as {
      setState: (partial: Record<string, unknown>) => void;
      getState: () => { config: Record<string, unknown> | null };
    };

    const row0: OpenAIProviderConfig = {
      name: 'OtherProvider',
      baseUrl: 'https://other.example.com/v1',
      apiKeyEntries: [{ apiKey: 'sk-other' }],
      v8Source: { family: 'openai-compatibility', groupIndex: 0, group: {}, etag: '"etag-1"' },
    };
    const row1: OpenAIProviderConfig = {
      name: 'DupName',
      baseUrl: 'https://dup.example.com/v1',
      apiKeyEntries: [{ apiKey: 'sk-target-1' }],
      v8Source: { family: 'openai-compatibility', groupIndex: 1, group: {}, etag: '"etag-1"' },
    };
    const row2: OpenAIProviderConfig = {
      name: 'DupName',
      baseUrl: 'https://dup.example.com/v1',
      apiKeyEntries: [{ apiKey: 'sk-sibling-2' }],
      v8Source: { family: 'openai-compatibility', groupIndex: 2, group: {}, etag: '"etag-1"' },
    };

    mocks.getConfig.mockResolvedValue({
      v8Supported: true,
      geminiApiKeys: [],
      interactionsApiKeys: [],
      codexApiKeys: [],
      xaiApiKeys: [],
      claudeApiKeys: [],
      vertexApiKeys: [],
      openaiCompatibility: [row0, row1, row2],
    });
    mocks.getVertexConfigs.mockResolvedValue([]);
    mocks.getOpenAIProviders.mockResolvedValue([row0, row1, row2]);

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    // Open Row 1 (sk-target-1, ::1), switch to Config tab, and enter dirty prefix edit
    await act(async () => {
      renderer.root
        .findByProps({ 'data-inspect': 'openai:DupName::https://dup.example.com/v1::::1' })
        .props.onClick();
    });
    await flush();
    await act(async () => {
      findTabByText(renderer.root, '配置')!.props.onClick();
    });
    await act(async () => {
      findInputByLabel(renderer.root, 'ai_providers.prefix_label')!.props.onChange({
        target: { value: 'unsaved-row1-prefix' },
      });
    });

    // Externally delete Row 0 -> Row 1 shifts to ::0 while Row 2 shifts into ::1
    await act(async () => {
      const prevConfig = storeControl.getState().config ?? {};
      storeControl.setState({
        config: {
          ...prevConfig,
          openaiCompatibility: [
            {
              ...row1,
              v8Source: { family: 'openai-compatibility', groupIndex: 0, group: {}, etag: '"etag-2"' },
            },
            {
              ...row2,
              v8Source: { family: 'openai-compatibility', groupIndex: 1, group: {}, etag: '"etag-2"' },
            },
          ],
        },
      });
    });
    await flush();

    // Drawer must stay open, remain on Config tab, keep dirty edit, and stay bound to sk-target-1 (at ::0, NOT sk-sibling-2 at ::1)
    expect(renderer.root.findByProps({ 'data-testid': 'drawer-shell' })).toBeTruthy();
    expect(findInputByLabel(renderer.root, 'ai_providers.prefix_label')?.props.value).toBe(
      'unsaved-row1-prefix'
    );
    expect(
      renderer.root.findAllByType('input').some((input) => input.props.value === 'sk-target-1')
    ).toBe(true);
    expect(
      renderer.root.findAllByType('input').some((input) => input.props.value === 'sk-sibling-2')
    ).toBe(false);

    // Now truly delete Row 1 externally while STILL dirty -> drawer must NOT silently close or discard unsaved edit!
    await act(async () => {
      const prevConfig = storeControl.getState().config ?? {};
      storeControl.setState({
        config: {
          ...prevConfig,
          openaiCompatibility: [
            {
              ...row2,
              v8Source: { family: 'openai-compatibility', groupIndex: 0, group: {}, etag: '"etag-3"' },
            },
          ],
        },
      });
    });
    await flush();

    expect(renderer.root.findByProps({ 'data-testid': 'drawer-shell' })).toBeTruthy();
    expect(
      renderer.root.findByProps({ 'data-testid': 'drawer-deleted-externally-notice' })
    ).toBeTruthy();
    expect(findInputByLabel(renderer.root, 'ai_providers.prefix_label')?.props.value).toBe(
      'unsaved-row1-prefix'
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'notification.provider_conflict_refresh',
      'warning'
    );

    // Resetting dirty state then closing closes cleanly; opening clean row & deleting externally auto-closes
    await act(async () => {
      const resetButtons = renderer.root
        .findAllByType('button')
        .filter((btn) => extractText(btn).trim().includes('重置'));
      resetButtons[resetButtons.length - 1].props.onClick();
    });
    await act(async () => {
      mocks.drawerPropsRef.current?.onClose?.();
    });
    await flush();
    expect(renderer.root.findAllByProps({ 'data-testid': 'drawer-shell' })).toHaveLength(0);

    // Clean drawer closes automatically when its row is deleted externally
    await act(async () => {
      renderer.root
        .findByProps({ 'data-inspect': 'openai:DupName::https://dup.example.com/v1::::0' })
        .props.onClick();
    });
    await flush();
    expect(renderer.root.findByProps({ 'data-testid': 'drawer-shell' })).toBeTruthy();

    await act(async () => {
      const prevConfig = storeControl.getState().config ?? {};
      storeControl.setState({
        config: {
          ...prevConfig,
          openaiCompatibility: [],
        },
      });
    });
    await flush();
    expect(renderer.root.findAllByProps({ 'data-testid': 'drawer-shell' })).toHaveLength(0);

    act(() => renderer.unmount());
  });

  it('preserves __full__ cache in real useConfigStore across non-forced initial v8 load of /ai-providers', async () => {
    const storeControl = useConfigStore as unknown as {
      getState: () => {
        cache: Map<string, unknown>;
        isCacheValid: () => boolean;
        clearCache: (section?: string) => void;
        updateConfigValue: (section: string, value: unknown) => void;
      };
    };

    const clearCacheSpy = vi.spyOn(storeControl.getState(), 'clearCache');
    const updateConfigValueSpy = vi.spyOn(storeControl.getState(), 'updateConfigValue');

    const vertexItems: ProviderKeyConfig[] = [
      { name: 'Vertex V8', apiKey: 'vx-1', baseUrl: 'https://vertex.example.com' },
    ];
    const openAIItems: OpenAIProviderConfig[] = [
      {
        name: 'OpenAI V8',
        baseUrl: 'https://oa.example.com/v1',
        apiKeyEntries: [{ apiKey: 'sk-oa-v8' }],
      },
    ];

    mocks.getConfig.mockResolvedValue({
      v8Supported: true,
      geminiApiKeys: [],
      interactionsApiKeys: [],
      codexApiKeys: [],
      xaiApiKeys: [],
      claudeApiKeys: [],
      vertexApiKeys: vertexItems,
      openaiCompatibility: openAIItems,
    });
    mocks.getVertexConfigs.mockResolvedValue(vertexItems);
    mocks.getOpenAIProviders.mockResolvedValue(openAIItems);

    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<AiProvidersPage />);
    });
    await flush();

    expect(storeControl.getState().cache.has('__full__')).toBe(true);
    expect(storeControl.getState().isCacheValid()).toBe(true);
    expect(clearCacheSpy).not.toHaveBeenCalled();
    expect(updateConfigValueSpy).not.toHaveBeenCalled();
    expect(mocks.getConfig).toHaveBeenCalledTimes(1);

    // Re-entering current transition layer reuses valid __full__ cache without refetching
    mocks.transitionLayer = { status: 'background', isCurrentLayer: false };
    await act(async () => {
      renderer.update(<AiProvidersPage />);
    });
    await flush();
    mocks.transitionLayer = { status: 'current', isCurrentLayer: true };
    await act(async () => {
      renderer.update(<AiProvidersPage />);
    });
    await flush();

    expect(mocks.getConfig).toHaveBeenCalledTimes(1);
    expect(storeControl.getState().cache.has('__full__')).toBe(true);

    clearCacheSpy.mockRestore();
    updateConfigValueSpy.mockRestore();
    act(() => renderer.unmount());
  });
});
