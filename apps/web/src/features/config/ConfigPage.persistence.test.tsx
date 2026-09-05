import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ApiKeyMutation } from '@/components/config/ApiKeysCardEditor';
import type { ManagerConfigResponse } from '@/services/api/usageService';

vi.mock('react-dom', () => ({
  createPortal: (children: ReactNode) => children,
}));

const mocks = vi.hoisted(() => ({
  fetchConfigYaml: vi.fn(),
  saveConfigYaml: vi.fn(),
  apiKeysList: vi.fn(),
  apiKeysReplace: vi.fn(),
  apiKeysReplaceValue: vi.fn(),
  apiKeysDeleteValue: vi.fn(),
  apiKeyMutationErrors: [] as unknown[],
  getInfo: vi.fn(),
  showNotification: vi.fn(),
  showConfirmation: vi.fn(),
  commitApiKeysText: vi.fn(),
  loadVisualValuesFromYaml: vi.fn(),
  applyVisualChangesToYaml: vi.fn(),
  setVisualValues: vi.fn(),
  clearCache: vi.fn(),
  fetchGlobalConfig: vi.fn(),
  setUsageServiceConfig: vi.fn(),
  getManagerConfig: vi.fn(),
  saveManagerConfig: vi.fn(),
  reloadPage: vi.fn(),
  capturedApiKeyOperationStart: null as (() => void) | null,
  capturedApiKeyOperationEnd: null as (() => void) | null,
  translate: (key: string) => key,
  visualState: {
    apiKeysText: 'sk-old',
    dirty: false,
  },
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: mocks.translate }),
}));

vi.mock('@/components/common/PageTransitionLayer', () => ({
  usePageTransitionLayer: () => null,
}));

vi.mock('@/hooks/useMediaQuery', () => ({
  useMediaQuery: () => false,
}));

vi.mock('@/components/config/VisualConfigEditor', () => ({
  VisualConfigEditor: ({
    onPersistApiKeyMutation,
    onApiKeyOperationStart,
    onApiKeyOperationEnd,
  }: {
    onPersistApiKeyMutation: (mutation: ApiKeyMutation) => Promise<string[]>;
    onApiKeyOperationStart: () => void;
    onApiKeyOperationEnd: () => void;
  }) => {
    mocks.capturedApiKeyOperationStart = onApiKeyOperationStart;
    mocks.capturedApiKeyOperationEnd = onApiKeyOperationEnd;
    const runMutation = async (mutation: ApiKeyMutation) => {
      try {
        onApiKeyOperationStart();
        await onPersistApiKeyMutation(mutation);
      } catch (error) {
        // The real editor renders mutation errors. This harness only observes the page contract.
        mocks.apiKeyMutationErrors.push(error);
      } finally {
        onApiKeyOperationEnd();
      }
    };

    return (
      <div data-test="visual-editor">
        <button
          type="button"
          data-test="create-key"
          onClick={() => runMutation({ type: 'create', apiKey: 'sk-new' })}
        />
        <button
          type="button"
          data-test="replace-key"
          onClick={() => runMutation({ type: 'replace', oldApiKey: 'sk-old', newApiKey: 'sk-new' })}
        />
        <button
          type="button"
          data-test="delete-key"
          onClick={() => runMutation({ type: 'delete', apiKey: 'sk-old' })}
        />
      </div>
    );
  },
}));

vi.mock('./components/ManagerConfigPanel', () => ({
  ManagerConfigPanel: ({
    managerSaving,
    onCollectorModeChange,
    onCPABaseInputChange,
    onCPAManagementKeyInputChange,
  }: {
    managerSaving: boolean;
    onCollectorModeChange: (value: string) => void;
    onCPABaseInputChange: (value: string) => void;
    onCPAManagementKeyInputChange: (value: string) => void;
  }) => (
    <div data-test="manager-panel">
      <button
        type="button"
        data-test="manager-dirty"
        disabled={managerSaving}
        onClick={() => onCollectorModeChange('http')}
      />
      <button
        type="button"
        data-test="manager-change-cpa"
        disabled={managerSaving}
        onClick={() => onCPABaseInputChange('http://cpa-next.local:8317')}
      />
      <button
        type="button"
        data-test="manager-change-key"
        disabled={managerSaving}
        onClick={() => onCPAManagementKeyInputChange('next-management-key')}
      />
    </div>
  ),
}));

vi.mock('@/components/config/DiffModal', () => ({
  DiffModal: () => null,
}));

vi.mock('@/components/config/ConfigSourceEditor', () => ({
  default: ({
    value,
    onChange,
    editable,
  }: {
    value: string;
    onChange: (value: string) => void;
    editable: boolean;
  }) => (
    <textarea
      data-test="source-editor"
      value={value}
      onChange={(event) => onChange(event.target.value)}
      readOnly={!editable}
    />
  ),
}));

vi.mock('@/components/ui/SegmentedTabs', () => ({
  SegmentedTabs: ({
    items,
    activeTab,
    onChange,
  }: {
    items: ReadonlyArray<{ id: string; label: ReactNode; disabled?: boolean }>;
    activeTab: string;
    onChange?: (tab: string) => void;
  }) => (
    <div data-test="tabs">
      {items.map((item) => (
        <button
          key={item.id}
          type="button"
          data-tab={item.id}
          data-active={item.id === activeTab}
          disabled={item.disabled}
          onClick={() => onChange?.(item.id)}
        >
          {item.label}
        </button>
      ))}
    </div>
  ),
}));

vi.mock('@/stores', () => ({
  useAuthStore: (
    selector: (state: { connectionStatus: string; managementKey: string }) => unknown
  ) => selector({ connectionStatus: 'connected', managementKey: 'management-key' }),
  useNotificationStore: (
    selector: (state: {
      showNotification: typeof mocks.showNotification;
      showConfirmation: typeof mocks.showConfirmation;
    }) => unknown
  ) =>
    selector({
      showNotification: mocks.showNotification,
      showConfirmation: mocks.showConfirmation,
    }),
  useThemeStore: (selector: (state: { resolvedTheme: string }) => unknown) =>
    selector({ resolvedTheme: 'light' }),
  useConfigStore: {
    getState: () => ({
      clearCache: mocks.clearCache,
      fetchConfig: mocks.fetchGlobalConfig,
    }),
  },
  useUsageServiceStore: (
    selector: (state: { setUsageServiceConfig: typeof mocks.setUsageServiceConfig }) => unknown
  ) => selector({ setUsageServiceConfig: mocks.setUsageServiceConfig }),
}));

vi.mock('@/hooks/useVisualConfig', () => ({
  useVisualConfig: () => ({
    visualValues: {
      apiKeysText: mocks.visualState.apiKeysText,
      redisUsageQueueRetentionSeconds: '60',
    },
    visualDirty: mocks.visualState.dirty,
    visualParseError: null,
    visualValidationErrors: {},
    visualHasPayloadValidationErrors: false,
    loadVisualValuesFromYaml: mocks.loadVisualValuesFromYaml,
    applyVisualChangesToYaml: mocks.applyVisualChangesToYaml,
    setVisualValues: mocks.setVisualValues,
    commitApiKeysText: mocks.commitApiKeysText,
  }),
}));

vi.mock('@/services/api/configFile', () => ({
  configFileApi: {
    fetchConfigYaml: mocks.fetchConfigYaml,
    saveConfigYaml: mocks.saveConfigYaml,
  },
}));

vi.mock('@/services/api/apiKeys', () => ({
  apiKeysApi: {
    list: mocks.apiKeysList,
    replace: mocks.apiKeysReplace,
    replaceValue: mocks.apiKeysReplaceValue,
    deleteValue: mocks.apiKeysDeleteValue,
  },
}));

vi.mock('@/services/api/usageService', () => ({
  getUsageServiceErrorCode: () => '',
  isUsageServiceId: (service?: string) => service === 'test-manager',
  normalizeUsageServiceBase: (value: string) => value,
  usageServiceApi: {
    getInfo: mocks.getInfo,
    getManagerConfig: mocks.getManagerConfig,
    saveManagerConfig: mocks.saveManagerConfig,
  },
}));

vi.mock('@/utils/connection', () => ({
  detectApiBaseFromLocation: () => 'http://panel.local',
}));

const { ConfigPage } = await import('./ConfigPage');

const INITIAL_YAML = 'api-keys:\n  - sk-old\n';
const LATEST_WITHOUT_OLD_KEY = 'api-keys: []\n';
const MANAGER_CONFIG_RESPONSE: ManagerConfigResponse = {
  config: {
    cpaConnection: {
      cpaBaseUrl: 'http://cpa.local:8317',
      managementKeyConfigured: true,
    },
    collector: {
      enabled: true,
      collectorMode: 'auto',
      queue: 'usage',
      popSide: 'right',
      batchSize: 100,
      pollIntervalMs: 500,
      queryLimit: 50000,
    },
    externalUsageService: {
      enabled: false,
      serviceBase: '',
    },
  },
  source: 'db',
  cpaUsage: {
    usageStatisticsEnabled: true,
    redisUsageQueueRetentionSeconds: 60,
  },
};

let renderer: ReactTestRenderer | null = null;
const originalLocalStorage = globalThis.localStorage;
const originalDocument = globalThis.document;

type ConfirmationOptions = {
  onConfirm: () => void | Promise<void>;
};

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T | PromiseLike<T>) => void;
  reject: (reason?: unknown) => void;
};

const createDeferred = <T,>(): Deferred<T> => {
  let resolve!: Deferred<T>['resolve'];
  let reject!: Deferred<T>['reject'];
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve;
    reject = promiseReject;
  });
  return { promise, resolve, reject };
};

const flush = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
};

const mountPage = async () => {
  await act(async () => {
    renderer = create(<ConfigPage />);
  });
  await flush();
  return renderer as ReactTestRenderer;
};

const click = async (testId: string) => {
  const target = renderer?.root.findByProps({ 'data-test': testId });
  if (!target) throw new Error(`Test target not found: ${testId}`);
  await act(async () => {
    await target.props.onClick();
  });
  await flush();
};

const clickTab = async (tab: string) => {
  const target = renderer?.root.findByProps({ 'data-tab': tab });
  if (!target) throw new Error(`Tab not found: ${tab}`);
  await act(async () => {
    await target.props.onClick();
  });
  await flush();
};

const startPendingSave = async () => {
  const target = renderer?.root.findByProps({ 'aria-label': 'config_management.save' });
  if (!target) throw new Error('Save button not found');
  let pending!: Promise<unknown>;
  await act(async () => {
    pending = target.props.onClick() as Promise<unknown>;
    await Promise.resolve();
  });
  await flush();
  return { pending };
};

const clickSave = async () => {
  const target = renderer?.root.findByProps({ 'aria-label': 'config_management.save' });
  if (!target) throw new Error('Save button not found');
  await act(async () => {
    await target.props.onClick();
  });
  await flush();
};

const getPendingConfirmation = (): ConfirmationOptions => {
  const call = mocks.showConfirmation.mock.calls[mocks.showConfirmation.mock.calls.length - 1];
  const options = call?.[0] as ConfirmationOptions | undefined;
  if (!options) throw new Error('Confirmation not found');
  return options;
};

const confirmPending = async () => {
  const confirmation = getPendingConfirmation();
  await act(async () => {
    await confirmation.onConfirm();
  });
  await flush();
};

const configureManagerMode = () => {
  mocks.getInfo.mockResolvedValue({ service: 'test-manager' });
  mocks.getManagerConfig.mockResolvedValue(MANAGER_CONFIG_RESPONSE);
};

beforeEach(() => {
  vi.clearAllMocks();
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: () => null,
      setItem: vi.fn(),
    },
  });
  mocks.fetchConfigYaml.mockResolvedValue(INITIAL_YAML);
  mocks.saveConfigYaml.mockResolvedValue(undefined);
  mocks.apiKeysList.mockResolvedValue([]);
  mocks.apiKeysReplace.mockResolvedValue(undefined);
  mocks.apiKeysReplaceValue.mockResolvedValue(undefined);
  mocks.apiKeysDeleteValue.mockResolvedValue(undefined);
  mocks.apiKeyMutationErrors.length = 0;
  mocks.getInfo.mockRejectedValue(new Error('not a Manager Server panel'));
  mocks.getManagerConfig.mockResolvedValue(MANAGER_CONFIG_RESPONSE);
  mocks.saveManagerConfig.mockResolvedValue(MANAGER_CONFIG_RESPONSE);
  mocks.capturedApiKeyOperationStart = null;
  mocks.capturedApiKeyOperationEnd = null;
  mocks.loadVisualValuesFromYaml.mockReturnValue({ ok: true });
  mocks.applyVisualChangesToYaml.mockImplementation((yaml: string) => yaml);
  mocks.commitApiKeysText.mockImplementation((apiKeysText: string) => {
    mocks.visualState.apiKeysText = apiKeysText;
    mocks.visualState.dirty = false;
  });
  mocks.visualState.apiKeysText = 'sk-old';
  mocks.visualState.dirty = false;
  vi.stubGlobal('window', { location: { reload: mocks.reloadPage } });
  Object.defineProperty(globalThis, 'document', {
    configurable: true,
    value: { body: {} },
  });
});

afterEach(() => {
  if (renderer) {
    act(() => renderer?.unmount());
    renderer = null;
  }
  vi.unstubAllGlobals();
  if (originalLocalStorage === undefined) {
    Reflect.deleteProperty(globalThis, 'localStorage');
  } else {
    Object.defineProperty(globalThis, 'localStorage', {
      configurable: true,
      value: originalLocalStorage,
    });
  }
  if (originalDocument === undefined) {
    Reflect.deleteProperty(globalThis, 'document');
  } else {
    Object.defineProperty(globalThis, 'document', {
      configurable: true,
      value: originalDocument,
    });
  }
});

describe('ConfigPage API-key source snapshot safety', () => {
  it('marks Source stale and reports an unknown outcome when create PUT rejects', async () => {
    mocks.apiKeysList.mockResolvedValueOnce([]);
    mocks.apiKeysReplace.mockRejectedValueOnce(new Error('create response lost'));
    mocks.fetchConfigYaml
      .mockReset()
      .mockResolvedValueOnce(INITIAL_YAML)
      .mockResolvedValueOnce(LATEST_WITHOUT_OLD_KEY);
    await mountPage();

    await click('create-key');

    expect(mocks.apiKeysReplace).toHaveBeenCalledWith(['sk-new']);
    expect(mocks.apiKeysList).toHaveBeenCalledTimes(1);
    expect(mocks.commitApiKeysText).not.toHaveBeenCalled();
    expect(mocks.apiKeyMutationErrors).toHaveLength(1);
    expect((mocks.apiKeyMutationErrors[0] as Error & { code?: string }).code).toBe(
      'api_key_mutation_outcome_unknown'
    );

    await clickTab('source');

    expect(mocks.fetchConfigYaml).toHaveBeenCalledTimes(2);
    expect(renderer?.root.findByProps({ 'data-tab': 'source' }).props['data-active']).toBe(true);
    expect(renderer?.root.findByProps({ 'data-test': 'source-editor' }).props.value).toBe(
      LATEST_WITHOUT_OLD_KEY
    );
  });

  it('keeps Source unavailable when create PUT rejects and the stale refresh also fails', async () => {
    mocks.apiKeysList.mockResolvedValueOnce([]);
    mocks.apiKeysReplace.mockRejectedValueOnce(new Error('create response lost'));
    mocks.fetchConfigYaml
      .mockReset()
      .mockResolvedValueOnce(INITIAL_YAML)
      .mockRejectedValueOnce(new Error('source refresh failed'));
    await mountPage();

    await click('create-key');
    await clickTab('source');

    expect(renderer?.root.findByProps({ 'data-tab': 'visual' }).props['data-active']).toBe(true);
    expect(renderer?.root.findAllByProps({ 'data-test': 'source-editor' })).toHaveLength(0);
    expect(mocks.saveConfigYaml).not.toHaveBeenCalled();
  });

  it('does not PUT when the create preflight response is malformed', async () => {
    mocks.apiKeysList.mockRejectedValueOnce(new Error('Invalid API key list response'));
    await mountPage();

    await click('create-key');

    expect(mocks.apiKeysList).toHaveBeenCalledTimes(1);
    expect(mocks.apiKeysReplace).not.toHaveBeenCalled();
    expect(mocks.apiKeysReplaceValue).not.toHaveBeenCalled();
    expect(mocks.apiKeysDeleteValue).not.toHaveBeenCalled();
    expect(mocks.commitApiKeysText).not.toHaveBeenCalled();
    expect(mocks.apiKeyMutationErrors).toHaveLength(1);
    expect((mocks.apiKeyMutationErrors[0] as Error & { code?: string }).code).not.toBe(
      'api_key_mutation_outcome_unknown'
    );

    await clickTab('source');

    expect(mocks.fetchConfigYaml).toHaveBeenCalledTimes(1);
    expect(renderer?.root.findAllByProps({ 'data-test': 'source-editor' })).toHaveLength(1);
  });

  it('does not PATCH when the replace preflight response is malformed', async () => {
    mocks.apiKeysList.mockRejectedValueOnce(new Error('Invalid API key list response'));
    await mountPage();

    await click('replace-key');

    expect(mocks.apiKeysList).toHaveBeenCalledTimes(1);
    expect(mocks.apiKeysReplace).not.toHaveBeenCalled();
    expect(mocks.apiKeysReplaceValue).not.toHaveBeenCalled();
    expect(mocks.apiKeysDeleteValue).not.toHaveBeenCalled();
    expect(mocks.commitApiKeysText).not.toHaveBeenCalled();
    expect(mocks.apiKeyMutationErrors).toHaveLength(1);
    expect((mocks.apiKeyMutationErrors[0] as Error & { code?: string }).code).not.toBe(
      'api_key_mutation_outcome_unknown'
    );

    await clickTab('source');

    expect(mocks.fetchConfigYaml).toHaveBeenCalledTimes(1);
    expect(renderer?.root.findAllByProps({ 'data-test': 'source-editor' })).toHaveLength(1);
  });

  it('marks Source stale and reports an unknown outcome when replace PATCH rejects', async () => {
    mocks.apiKeysList.mockResolvedValueOnce(['sk-old']);
    mocks.apiKeysReplaceValue.mockRejectedValueOnce(new Error('replace response lost'));
    mocks.fetchConfigYaml
      .mockReset()
      .mockResolvedValueOnce(INITIAL_YAML)
      .mockResolvedValueOnce(LATEST_WITHOUT_OLD_KEY);
    await mountPage();

    await click('replace-key');

    expect(mocks.apiKeysReplaceValue).toHaveBeenCalledWith('sk-old', 'sk-new');
    expect(mocks.apiKeysList).toHaveBeenCalledTimes(1);
    expect(mocks.commitApiKeysText).not.toHaveBeenCalled();
    expect((mocks.apiKeyMutationErrors[0] as Error & { code?: string }).code).toBe(
      'api_key_mutation_outcome_unknown'
    );

    await clickTab('source');

    expect(mocks.fetchConfigYaml).toHaveBeenCalledTimes(2);
    expect(renderer?.root.findByProps({ 'data-tab': 'source' }).props['data-active']).toBe(true);
  });

  it('marks Source stale and reports an unknown outcome when delete rejects', async () => {
    mocks.apiKeysDeleteValue.mockRejectedValueOnce(new Error('delete response lost'));
    mocks.fetchConfigYaml
      .mockReset()
      .mockResolvedValueOnce(INITIAL_YAML)
      .mockResolvedValueOnce(LATEST_WITHOUT_OLD_KEY);
    await mountPage();

    await click('delete-key');

    expect(mocks.apiKeysDeleteValue).toHaveBeenCalledWith('sk-old');
    expect(mocks.commitApiKeysText).not.toHaveBeenCalled();
    expect((mocks.apiKeyMutationErrors[0] as Error & { code?: string }).code).toBe(
      'api_key_mutation_outcome_unknown'
    );

    await clickTab('source');

    expect(mocks.fetchConfigYaml).toHaveBeenCalledTimes(2);
    expect(renderer?.root.findByProps({ 'data-tab': 'source' }).props['data-active']).toBe(true);
  });

  it('marks Source stale after delete succeeds but canonical key refresh fails', async () => {
    mocks.apiKeysList.mockRejectedValueOnce(new Error('canonical refresh failed'));
    mocks.fetchConfigYaml
      .mockResolvedValueOnce(INITIAL_YAML)
      .mockRejectedValueOnce(new Error('source refresh failed'));
    await mountPage();

    await click('delete-key');

    expect(mocks.apiKeysDeleteValue).toHaveBeenCalledWith('sk-old');
    expect(mocks.commitApiKeysText).not.toHaveBeenCalled();
    expect(mocks.apiKeyMutationErrors).toHaveLength(1);
    expect((mocks.apiKeyMutationErrors[0] as Error & { code?: string }).code).toBe(
      'api_key_state_refresh_failed'
    );

    await clickTab('source');

    expect(mocks.fetchConfigYaml).toHaveBeenCalledTimes(2);
    expect(renderer?.root.findByProps({ 'data-tab': 'visual' }).props['data-active']).toBe(true);
    expect(renderer?.root.findAllByProps({ 'data-test': 'source-editor' })).toHaveLength(0);
    expect(mocks.saveConfigYaml).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith('notification.refresh_failed', 'error');
  });

  it('keeps Source stale after a malformed post-write canonical response', async () => {
    mocks.apiKeysList
      .mockResolvedValueOnce([])
      .mockRejectedValueOnce(new Error('Invalid API key list response'));
    mocks.fetchConfigYaml
      .mockResolvedValueOnce(INITIAL_YAML)
      .mockRejectedValueOnce(new Error('source refresh failed'));
    await mountPage();

    await click('create-key');

    expect(mocks.apiKeysReplace).toHaveBeenCalledWith(['sk-new']);
    expect(mocks.commitApiKeysText).not.toHaveBeenCalled();
    expect(mocks.apiKeyMutationErrors).toHaveLength(1);
    expect((mocks.apiKeyMutationErrors[0] as Error & { code?: string }).code).toBe(
      'api_key_state_refresh_failed'
    );

    await clickTab('source');

    expect(mocks.fetchConfigYaml).toHaveBeenCalledTimes(2);
    expect(renderer?.root.findByProps({ 'data-tab': 'visual' }).props['data-active']).toBe(true);
  });

  it('keeps Source stale and skips alias migration after replace canonical refresh fails', async () => {
    mocks.apiKeysList
      .mockResolvedValueOnce(['sk-old'])
      .mockRejectedValueOnce(new Error('canonical refresh failed'));
    mocks.fetchConfigYaml
      .mockResolvedValueOnce(INITIAL_YAML)
      .mockRejectedValueOnce(new Error('source refresh failed'));
    await mountPage();

    await click('replace-key');

    expect(mocks.apiKeysReplaceValue).toHaveBeenCalledWith('sk-old', 'sk-new');
    expect(mocks.commitApiKeysText).not.toHaveBeenCalled();
    expect(mocks.apiKeyMutationErrors).toHaveLength(1);
    expect((mocks.apiKeyMutationErrors[0] as Error & { code?: string }).code).toBe(
      'api_key_state_refresh_failed'
    );

    await clickTab('source');

    expect(mocks.fetchConfigYaml).toHaveBeenCalledTimes(2);
    expect(renderer?.root.findByProps({ 'data-tab': 'visual' }).props['data-active']).toBe(true);
  });

  it('keeps a successful API-key commit when the source snapshot refresh fails', async () => {
    mocks.fetchConfigYaml
      .mockReset()
      .mockResolvedValueOnce(INITIAL_YAML)
      .mockRejectedValueOnce(new Error('snapshot refresh failed'));
    mocks.apiKeysList.mockResolvedValueOnce([]).mockResolvedValueOnce(['sk-new']);
    mocks.visualState.dirty = true;
    await mountPage();

    await click('create-key');

    expect(mocks.apiKeysReplace).toHaveBeenCalledWith(['sk-new']);
    expect(mocks.commitApiKeysText).toHaveBeenCalledWith('sk-new');
    expect(mocks.visualState.dirty).toBe(false);
    expect(mocks.saveConfigYaml).not.toHaveBeenCalled();
  });

  it('keeps the Visual tab and does not show the stale source buffer when refresh fails', async () => {
    mocks.fetchConfigYaml
      .mockReset()
      .mockResolvedValueOnce(INITIAL_YAML)
      .mockRejectedValueOnce(new Error('snapshot refresh failed'))
      .mockRejectedValueOnce(new Error('source refresh failed'));
    mocks.apiKeysList.mockResolvedValueOnce([]).mockResolvedValueOnce(['sk-new']);
    await mountPage();

    await click('create-key');
    await clickTab('source');

    expect(renderer?.root.findByProps({ 'data-tab': 'visual' }).props['data-active']).toBe(true);
    expect(renderer?.root.findAllByProps({ 'data-test': 'source-editor' })).toHaveLength(0);
    expect(mocks.showNotification).toHaveBeenCalledWith('notification.refresh_failed', 'error');
  });

  it('reloads the latest YAML before opening Source after a stale snapshot', async () => {
    mocks.fetchConfigYaml
      .mockReset()
      .mockResolvedValueOnce(INITIAL_YAML)
      .mockRejectedValueOnce(new Error('snapshot refresh failed'))
      .mockResolvedValueOnce(LATEST_WITHOUT_OLD_KEY);
    mocks.apiKeysList.mockResolvedValueOnce([]).mockResolvedValueOnce(['sk-new']);
    await mountPage();

    await click('create-key');
    await clickTab('source');

    const sourceEditor = renderer?.root.findByProps({ 'data-test': 'source-editor' });
    expect(sourceEditor?.props.value).toBe(LATEST_WITHOUT_OLD_KEY);
    expect(renderer?.root.findByProps({ 'data-tab': 'source' }).props['data-active']).toBe(true);
  });
});

describe('ConfigPage API-key replace preflight', () => {
  it('commits the canonical list when create preflight finds a duplicate', async () => {
    mocks.apiKeysList.mockResolvedValueOnce(['sk-new']);
    await mountPage();

    await click('create-key');

    expect(mocks.apiKeysReplace).not.toHaveBeenCalled();
    expect(mocks.commitApiKeysText).toHaveBeenCalledWith('sk-new');
    expect(mocks.apiKeyMutationErrors).toHaveLength(1);
    expect((mocks.apiKeyMutationErrors[0] as Error & { code?: string }).code).toBe(
      'api_key_duplicate'
    );
  });

  it('does not PATCH a stale old key and commits the canonical list', async () => {
    mocks.apiKeysList.mockResolvedValueOnce(['sk-other']);
    await mountPage();

    await click('replace-key');

    expect(mocks.apiKeysReplaceValue).not.toHaveBeenCalled();
    expect(mocks.commitApiKeysText).toHaveBeenCalledWith('sk-other');
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'notification.save_failed',
      expect.anything()
    );
  });

  it('does not PATCH and commits the canonical list when the replacement key already exists', async () => {
    mocks.apiKeysList.mockResolvedValueOnce(['sk-old', 'sk-new']);
    await mountPage();

    await click('replace-key');

    expect(mocks.apiKeysReplaceValue).not.toHaveBeenCalled();
    expect(mocks.commitApiKeysText).toHaveBeenCalledWith('sk-old\nsk-new');
    expect(mocks.apiKeyMutationErrors).toHaveLength(1);
    expect((mocks.apiKeyMutationErrors[0] as Error & { code?: string }).code).toBe(
      'api_key_duplicate'
    );
  });

  it('orders normal replace as preflight GET, PATCH, canonical GET', async () => {
    const events: string[] = [];
    mocks.apiKeysList.mockImplementation(async () => {
      events.push('get');
      return events.filter((event) => event === 'get').length === 1 ? ['sk-old'] : ['sk-new'];
    });
    mocks.apiKeysReplaceValue.mockImplementation(async () => {
      events.push('patch');
    });
    await mountPage();

    await click('replace-key');

    expect(events).toEqual(['get', 'patch', 'get']);
    expect(mocks.apiKeysReplaceValue).toHaveBeenCalledWith('sk-old', 'sk-new');
  });

  it('sends the raw canonical old key when replacing a whitespace-padded entry', async () => {
    mocks.apiKeysList.mockResolvedValueOnce(['  sk-old  ']).mockResolvedValueOnce(['sk-new']);
    await mountPage();

    await click('replace-key');

    expect(mocks.apiKeysReplaceValue).toHaveBeenCalledWith('  sk-old  ', 'sk-new');
  });

  it('does not PATCH when the old runtime identity is canonically ambiguous', async () => {
    mocks.apiKeysList.mockResolvedValueOnce(['sk-old', '  sk-old  ']);
    await mountPage();

    await click('replace-key');

    expect(mocks.apiKeysReplaceValue).not.toHaveBeenCalled();
    expect(mocks.commitApiKeysText).toHaveBeenCalledWith('sk-old\n  sk-old  ');
    expect(mocks.apiKeyMutationErrors).toHaveLength(1);
    expect((mocks.apiKeyMutationErrors[0] as Error & { code?: string }).code).toBe(
      'api_key_ambiguous'
    );

    const fetchCallsAfterPreflight = mocks.fetchConfigYaml.mock.calls.length;
    await clickTab('source');
    expect(mocks.fetchConfigYaml).toHaveBeenCalledTimes(fetchCallsAfterPreflight);
  });

  it('does not PATCH when the new runtime identity already exists with whitespace', async () => {
    mocks.apiKeysList.mockResolvedValueOnce(['sk-old', '  sk-new  ']);
    await mountPage();

    await click('replace-key');

    expect(mocks.apiKeysReplaceValue).not.toHaveBeenCalled();
    expect(mocks.commitApiKeysText).toHaveBeenCalledWith('sk-old\n  sk-new  ');
    expect(mocks.apiKeyMutationErrors).toHaveLength(1);
    expect((mocks.apiKeyMutationErrors[0] as Error & { code?: string }).code).toBe(
      'api_key_duplicate'
    );
  });
});

describe('ConfigPage Manager/API-key operation lock', () => {
  it('blocks switching CPA bases while Visual config has unsaved changes', async () => {
    configureManagerMode();
    mocks.visualState.dirty = true;
    await mountPage();
    await clickTab('manager');

    await click('manager-change-cpa');
    await clickSave();

    expect(mocks.saveManagerConfig).not.toHaveBeenCalled();
    expect(mocks.reloadPage).not.toHaveBeenCalled();
    expect(mocks.showConfirmation).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'config_management.manager.cpa_switch_unsaved_config',
      'warning'
    );
  });

  it('blocks switching CPA bases while Source has unsaved changes', async () => {
    configureManagerMode();
    await mountPage();
    await clickTab('source');

    const sourceEditor = renderer?.root.findByProps({ 'data-test': 'source-editor' });
    if (!sourceEditor) throw new Error('Source editor not found');
    act(() => {
      sourceEditor.props.onChange({ target: { value: `${INITIAL_YAML}# unsaved draft\n` } });
    });

    await clickTab('manager');
    await click('manager-change-cpa');
    await clickSave();

    expect(mocks.saveManagerConfig).not.toHaveBeenCalled();
    expect(mocks.reloadPage).not.toHaveBeenCalled();
    expect(mocks.showConfirmation).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'config_management.manager.cpa_switch_unsaved_config',
      'warning'
    );
  });

  it('reloads once after a successful CPA base switch', async () => {
    configureManagerMode();
    await mountPage();
    await clickTab('manager');
    await click('manager-change-cpa');

    const saveDeferred = createDeferred<ManagerConfigResponse>();
    mocks.saveManagerConfig.mockReturnValueOnce(saveDeferred.promise);
    await clickSave();

    expect(mocks.showConfirmation).toHaveBeenCalledTimes(1);
    expect(mocks.saveManagerConfig).not.toHaveBeenCalled();
    const confirmation = getPendingConfirmation();
    let savePromise!: Promise<void>;
    await act(async () => {
      savePromise = confirmation.onConfirm() as Promise<void>;
      await Promise.resolve();
    });
    await flush();

    expect(mocks.saveManagerConfig).toHaveBeenCalledTimes(1);
    expect(mocks.reloadPage).not.toHaveBeenCalled();

    saveDeferred.resolve(MANAGER_CONFIG_RESPONSE);
    await act(async () => {
      await savePromise;
    });
    await flush();

    expect(mocks.reloadPage).toHaveBeenCalledTimes(1);
  });

  it('reloads after a CPA base save request fails', async () => {
    configureManagerMode();
    await mountPage();
    await clickTab('manager');
    await click('manager-change-cpa');
    mocks.saveManagerConfig.mockRejectedValueOnce(new Error('CPA switch failed'));
    await clickSave();

    const confirmation = getPendingConfirmation();
    await act(async () => {
      await confirmation.onConfirm();
    });
    await flush();

    expect(mocks.reloadPage).toHaveBeenCalledTimes(1);
  });

  it('does not reload for a Management Key-only save', async () => {
    configureManagerMode();
    await mountPage();
    await clickTab('manager');
    await click('manager-change-key');
    await clickSave();
    await confirmPending();

    expect(mocks.saveManagerConfig).toHaveBeenCalledTimes(1);
    expect(mocks.reloadPage).not.toHaveBeenCalled();
  });

  it('does not reload when a Management Key-only save request fails', async () => {
    configureManagerMode();
    await mountPage();
    await clickTab('manager');
    await click('manager-change-key');
    mocks.saveManagerConfig.mockRejectedValueOnce(new Error('Management Key rejected'));
    await clickSave();

    const confirmation = getPendingConfirmation();
    await act(async () => {
      await expect(confirmation.onConfirm()).rejects.toThrow('Management Key rejected');
    });
    await flush();

    expect(mocks.reloadPage).not.toHaveBeenCalled();
  });

  it('blocks API-key operation and tab changes while Manager Save is pending', async () => {
    configureManagerMode();
    await mountPage();
    await clickTab('manager');
    await click('manager-dirty');

    const saveDeferred = createDeferred<ManagerConfigResponse>();
    mocks.saveManagerConfig.mockReturnValueOnce(saveDeferred.promise);
    const { pending: savePromise } = await startPendingSave();

    expect(renderer?.root.findByProps({ 'data-tab': 'visual' }).props.disabled).toBe(true);
    expect(renderer?.root.findByProps({ 'data-tab': 'source' }).props.disabled).toBe(true);
    expect(renderer?.root.findByProps({ 'data-tab': 'manager' }).props.disabled).toBe(true);
    expect(renderer?.root.findByProps({ 'data-tab': 'manager' }).props['data-active']).toBe(true);

    expect(mocks.capturedApiKeyOperationStart).toEqual(expect.any(Function));
    expect(() => mocks.capturedApiKeyOperationStart!()).toThrow();
    expect(mocks.apiKeysList).not.toHaveBeenCalled();
    expect(mocks.apiKeysReplace).not.toHaveBeenCalled();
    expect(mocks.apiKeysReplaceValue).not.toHaveBeenCalled();
    expect(mocks.apiKeysDeleteValue).not.toHaveBeenCalled();

    saveDeferred.resolve(MANAGER_CONFIG_RESPONSE);
    await act(async () => {
      await savePromise;
    });
    await flush();

    expect(renderer?.root.findByProps({ 'data-tab': 'visual' }).props.disabled).toBe(false);
  });

  it('blocks Manager Save while an API-key operation is in flight', async () => {
    configureManagerMode();
    await mountPage();
    await clickTab('manager');
    await click('manager-dirty');

    expect(mocks.capturedApiKeyOperationStart).toEqual(expect.any(Function));
    act(() => {
      mocks.capturedApiKeyOperationStart!();
    });
    await flush();

    await clickSave();

    expect(mocks.saveManagerConfig).not.toHaveBeenCalled();

    expect(mocks.capturedApiKeyOperationEnd).toEqual(expect.any(Function));
    act(() => {
      mocks.capturedApiKeyOperationEnd!();
    });
    await flush();
  });

  it('releases the Manager Save lock after a successful request', async () => {
    configureManagerMode();
    await mountPage();
    await clickTab('manager');
    await click('manager-dirty');

    const saveDeferred = createDeferred<ManagerConfigResponse>();
    mocks.saveManagerConfig.mockReturnValueOnce(saveDeferred.promise);
    const { pending: savePromise } = await startPendingSave();

    saveDeferred.resolve(MANAGER_CONFIG_RESPONSE);
    await act(async () => {
      await savePromise;
    });
    await flush();

    await clickTab('visual');
    await click('create-key');

    expect(mocks.apiKeysList).toHaveBeenCalled();
    expect(mocks.apiKeysReplace).toHaveBeenCalledWith(['sk-new']);
    expect(mocks.reloadPage).not.toHaveBeenCalled();
  });

  it('releases the Manager Save lock after a failed request', async () => {
    configureManagerMode();
    await mountPage();
    await clickTab('manager');
    await click('manager-dirty');

    const saveDeferred = createDeferred<ManagerConfigResponse>();
    mocks.saveManagerConfig.mockReturnValueOnce(saveDeferred.promise);
    const { pending: savePromise } = await startPendingSave();

    saveDeferred.reject(new Error('Manager save failed'));
    await act(async () => {
      await savePromise;
    });
    await flush();

    await clickTab('visual');
    await click('create-key');

    expect(mocks.apiKeysList).toHaveBeenCalled();
    expect(mocks.apiKeysReplace).toHaveBeenCalledWith(['sk-new']);
  });
});
