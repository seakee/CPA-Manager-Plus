import { createElement, type ReactNode } from 'react';
import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { CoolingPolicySelect } from '@/components/providers/CoolingPolicySelect';
import { HeaderInputList } from '@/components/ui/HeaderInputList';
import { sha256Hex } from '@/utils/apiKeyHash';

const authState = vi.hoisted(() => ({
  serverVersion: 'v7.2.93' as string | null,
  serverCommit: null as string | null,
  managementKey: 'manager-key',
}));

vi.mock('@/stores/useAuthStore', () => ({
  useAuthStore: (selector: (state: typeof authState) => unknown) => selector(authState),
}));

const mocks = vi.hoisted(() => ({
  fetchConfig: vi.fn(),
  updateConfigValue: vi.fn(),
  clearCache: vi.fn(),
  showNotification: vi.fn(),
  updateCodexConfig: vi.fn(),
  getCodexConfigs: vi.fn(),
  createMetaConfig: vi.fn(),
  updateMetaConfig: vi.fn(),
  getMetaConfigs: vi.fn(),
  fetchV1ModelsViaApiCall: vi.fn(),
  getProviderKeyAliases: vi.fn(),
  saveProviderKeyAlias: vi.fn(),
  deleteProviderKeyAlias: vi.fn(),
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: typeof authState) => unknown) => selector(authState),
  useConfigStore: (selector: (state: unknown) => unknown) =>
    selector({
      fetchConfig: mocks.fetchConfig,
      updateConfigValue: mocks.updateConfigValue,
      clearCache: mocks.clearCache,
    }),
  useNotificationStore: (
    selector?: (state: { showNotification: typeof mocks.showNotification }) => unknown
  ) =>
    selector
      ? selector({ showNotification: mocks.showNotification })
      : { showNotification: mocks.showNotification },
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => ({
    checking: false,
    panelHostConfirmed: true,
    panelHostMode: 'manager_embedded',
    panelBase: 'http://manager.local',
    managerServiceBase: 'http://manager.local',
    managerServiceAvailable: true,
    requestMonitoringAvailable: true,
    modelPricesAvailable: true,
    serverCodexInspectionAvailable: true,
    dockerSetupAvailable: true,
    externalManagerConfigAvailable: false,
    reason: '',
  }),
}));

vi.mock('@/services/api/usageService', () => ({
  usageServiceApi: {
    getProviderKeyAliases: mocks.getProviderKeyAliases,
    saveProviderKeyAlias: mocks.saveProviderKeyAlias,
    deleteProviderKeyAlias: mocks.deleteProviderKeyAlias,
  },
}));

vi.mock('@/components/ui/Drawer', () => ({
  Drawer: ({
    open,
    children,
    footer,
    onClose,
  }: {
    open: boolean;
    children: ReactNode;
    footer: ReactNode;
    onClose: () => void;
  }) =>
    open
      ? createElement(
          'div',
          null,
          children,
          footer,
          createElement('button', { type: 'button', 'data-drawer-close': true, onClick: onClose })
        )
      : null,
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
  }) => (open ? createElement('div', { 'data-modal': true }, children, footer) : null),
}));

vi.mock('@/services/api', () => ({
  apiCallApi: { request: vi.fn() },
  getApiCallErrorMessage: vi.fn(() => ''),
  modelsApi: { fetchV1ModelsViaApiCall: mocks.fetchV1ModelsViaApiCall },
  providersApi: {
    updateCodexConfig: mocks.updateCodexConfig,
    getCodexConfigs: mocks.getCodexConfigs,
    createMetaConfig: mocks.createMetaConfig,
    updateMetaConfig: mocks.updateMetaConfig,
    getMetaConfigs: mocks.getMetaConfigs,
  },
}));

import { CodexEditDrawer } from './CodexEditDrawer';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const findSaveButton = (root: ReactTestInstance) =>
  root
    .findAllByType('button')
    .find((button) => String(button.props.className ?? '').includes('btn-primary'));

const findDrawerCloseButton = (root: ReactTestInstance) =>
  root.findAllByType('button').find((button) => button.props['data-drawer-close'] === true);

const findFetchModelsButton = (root: ReactTestInstance) =>
  root.findAllByType('button').find((button) =>
    button.findAllByType('span').some((span) => {
      const text = span.children.join('');
      return text.includes('/v1/models');
    })
  );

const findConnectivityTestButton = (root: ReactTestInstance) =>
  root
    .findAllByType('button')
    .find((button) => String(button.props.className ?? '').includes('modelTestAllButton'));

describe('CodexEditDrawer load baseline guard', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    authState.serverVersion = 'v7.2.93';
    mocks.fetchConfig.mockResolvedValue([]);
    mocks.updateCodexConfig.mockResolvedValue(undefined);
    mocks.getCodexConfigs.mockResolvedValue([]);
    mocks.fetchV1ModelsViaApiCall.mockResolvedValue([]);
    mocks.getProviderKeyAliases.mockResolvedValue({ items: [] });
    mocks.saveProviderKeyAlias.mockResolvedValue({ items: [] });
    mocks.deleteProviderKeyAlias.mockResolvedValue(undefined);
  });

  it('saves a Codex provider alias with the main form save', async () => {
    mocks.fetchConfig.mockResolvedValueOnce([
      { apiKey: 'codex-provider-secret', baseUrl: 'https://api.openai.com/v1' },
    ]);
    mocks.getProviderKeyAliases.mockResolvedValueOnce({ items: [] });
    mocks.saveProviderKeyAlias.mockResolvedValueOnce({
      items: [
        {
          provider: 'codex',
          apiKeyHash: sha256Hex('codex-provider-secret'),
          alias: 'WWP1',
        },
      ],
    });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer open editIndex={0} disabled={false} onClose={vi.fn()} onSaved={vi.fn()} />
      );
    });

    const aliasInput = renderer!.root
      .findAllByType('input')
      .find(
        (input) => input.props.placeholder === i18n.t('ai_providers.provider_key_alias_placeholder')
      );
    expect(aliasInput).toBeDefined();
    act(() => aliasInput?.props.onChange({ target: { value: 'WWP1' } }));
    expect(
      renderer!.root.findAllByType('button').some((button) =>
        button
          .findAllByType('span')
          .some((span) => span.children.join('') === i18n.t('ai_providers.provider_key_alias_save'))
      )
    ).toBe(false);
    await act(async () => {
      await findSaveButton(renderer!.root)?.props.onClick();
    });

    expect(mocks.saveProviderKeyAlias).toHaveBeenCalledWith(
      'http://manager.local',
      { provider: 'codex', apiKeyHash: sha256Hex('codex-provider-secret'), alias: 'WWP1' },
      'manager-key'
    );
    expect(mocks.updateCodexConfig).toHaveBeenCalledTimes(1);
    act(() => renderer!.unmount());
  });

  it('does not reuse a stale xAI edit baseline after a later load failure', async () => {
    mocks.fetchConfig
      .mockResolvedValueOnce([
        { apiKey: 'xai-old', baseUrl: 'https://api.x.ai/v1', websockets: true },
      ])
      .mockRejectedValueOnce(new Error('load failed'));

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={0}
          disabled={false}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          providerKind="xai"
        />
      );
    });

    expect(
      renderer!.root.findAllByType('input').some((input) => input.props.value === 'xai-old')
    ).toBe(true);

    await act(async () => {
      renderer!.update(
        <CodexEditDrawer
          open={false}
          editIndex={0}
          disabled={false}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          providerKind="xai"
        />
      );
    });
    await act(async () => {
      renderer!.update(
        <CodexEditDrawer
          open
          editIndex={0}
          disabled={false}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          providerKind="xai"
        />
      );
    });

    expect(renderer!.root.findAllByType('input')).toHaveLength(0);
    expect(findSaveButton(renderer!.root)?.props.disabled).toBe(true);

    act(() => renderer!.unmount());
  });

  it('treats an invalid weight as an unsaved edit while disabling save', async () => {
    mocks.fetchConfig.mockResolvedValueOnce([
      { apiKey: 'codex-key', baseUrl: 'https://api.openai.com/v1' },
    ]);
    const onClose = vi.fn();
    const confirmMock = vi.fn(() => false);
    vi.stubGlobal('window', { confirm: confirmMock });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer open editIndex={0} disabled={false} onClose={onClose} onSaved={vi.fn()} />
      );
    });

    const weightInput = renderer!.root
      .findAllByType('input')
      .find((input) => input.props.inputMode === 'text');
    expect(weightInput).toBeDefined();

    act(() => weightInput?.props.onChange({ target: { value: '1.5' } }));
    expect(findSaveButton(renderer!.root)?.props.disabled).toBe(true);

    act(() => findDrawerCloseButton(renderer!.root)?.props.onClick());
    expect(confirmMock).toHaveBeenCalledOnce();
    expect(onClose).not.toHaveBeenCalled();

    vi.unstubAllGlobals();
    act(() => renderer!.unmount());
  });

  it.each([
    [undefined, 'enabled', false],
    [true, 'inherit', null],
  ] as const)(
    'saves cooling %j -> %s as disable-cooling %j',
    async (initialOverride, nextPolicy, expectedOverride) => {
      mocks.fetchConfig.mockResolvedValueOnce([
        {
          apiKey: 'codex-key',
          baseUrl: 'https://api.openai.com/v1',
          ...(initialOverride === undefined ? {} : { disableCooling: initialOverride }),
        },
      ]);
      const onSaved = vi.fn();
      let renderer: ReactTestRenderer;
      await act(async () => {
        renderer = create(
          <CodexEditDrawer
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
      expect(saveButton?.props.disabled).toBe(false);

      await act(async () => {
        await saveButton?.props.onClick();
      });

      expect(mocks.updateCodexConfig).toHaveBeenCalledWith(
        expect.objectContaining({ apiKey: 'codex-key' }),
        expect.objectContaining({
          apiKey: 'codex-key',
          disableCooling: expectedOverride,
        })
      );
      expect(onSaved).toHaveBeenCalledTimes(1);

      act(() => renderer!.unmount());
    }
  );

  it('rejects DCA tokens when editing or creating a Meta provider', async () => {
    mocks.createMetaConfig.mockResolvedValue(undefined);
    mocks.getMetaConfigs.mockResolvedValue([]);
    const onSaved = vi.fn();

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={null}
          disabled={false}
          onClose={vi.fn()}
          onSaved={onSaved}
          providerKind="meta"
        />
      );
    });

    const inputs = renderer!.root.findAllByType('input');
    expect(inputs.length).toBeGreaterThan(0);
    const apiKeyInput = inputs[0];

    act(() => apiKeyInput?.props.onChange({ target: { value: 'dca:test-token' } }));

    const saveButton = findSaveButton(renderer!.root);
    await act(async () => {
      await saveButton?.props.onClick();
    });

    expect(mocks.showNotification).toHaveBeenCalledWith(
      i18n.t('ai_providers.meta_dca_not_accepted'),
      'error'
    );
    expect(mocks.createMetaConfig).not.toHaveBeenCalled();

    act(() => renderer!.unmount());
  });

  it('creates a Meta provider with valid key and omits websockets toggle', async () => {
    mocks.createMetaConfig.mockResolvedValue(undefined);
    mocks.getMetaConfigs.mockResolvedValue([]);
    const onSaved = vi.fn();

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={null}
          disabled={false}
          onClose={vi.fn()}
          onSaved={onSaved}
          providerKind="meta"
        />
      );
    });

    // Verify websockets toggle is not present
    const labels = renderer!.root.findAllByType('label');
    expect(labels.some((l) => l.props.children?.toString().includes('Websockets'))).toBe(false);

    const inputs = renderer!.root.findAllByType('input');
    expect(inputs.length).toBeGreaterThan(0);
    const apiKeyInput = inputs[0];

    act(() => apiKeyInput?.props.onChange({ target: { value: 'meta-valid-key' } }));

    const saveButton = findSaveButton(renderer!.root);
    await act(async () => {
      await saveButton?.props.onClick();
    });

    expect(mocks.createMetaConfig).toHaveBeenCalledWith(
      expect.objectContaining({
        apiKey: 'meta-valid-key',
        baseUrl: 'https://api.meta.ai/v1',
      })
    );
    expect(onSaved).toHaveBeenCalledTimes(1);

    act(() => renderer!.unmount());
  });

  it('rejects model discovery with DCA token for Meta provider', async () => {
    mocks.getMetaConfigs.mockResolvedValue([]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={null}
          disabled={false}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          providerKind="meta"
        />
      );
    });

    const apiKeyInput = renderer!.root.findAllByType('input')[0];
    act(() => apiKeyInput?.props.onChange({ target: { value: 'dca:test-token' } }));

    const fetchButton = findFetchModelsButton(renderer!.root);
    expect(fetchButton?.props.disabled).toBe(true);

    await act(async () => {
      await fetchButton?.props.onClick();
    });

    expect(mocks.fetchV1ModelsViaApiCall).not.toHaveBeenCalled();
    const errorBox = renderer!.root
      .findAllByType('div')
      .find((div) => div.props.className === 'error-box');
    expect(errorBox).toBeDefined();
    expect(errorBox?.children.join('')).toBe(i18n.t('ai_providers.meta_dca_not_accepted'));

    act(() => renderer!.unmount());
  });

  it('rejects model discovery with empty API key for Meta provider', async () => {
    mocks.getMetaConfigs.mockResolvedValue([]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={null}
          disabled={false}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          providerKind="meta"
        />
      );
    });

    const apiKeyInput = renderer!.root.findAllByType('input')[0];
    act(() => apiKeyInput?.props.onChange({ target: { value: '' } }));

    const fetchButton = findFetchModelsButton(renderer!.root);
    expect(fetchButton?.props.disabled).toBe(true);

    await act(async () => {
      await fetchButton?.props.onClick();
    });

    expect(mocks.fetchV1ModelsViaApiCall).not.toHaveBeenCalled();
    const errorBox = renderer!.root
      .findAllByType('div')
      .find((div) => div.props.className === 'error-box');
    expect(errorBox).toBeDefined();
    expect(errorBox?.children.join('')).toBe(i18n.t('ai_providers.meta_key_required'));

    act(() => renderer!.unmount());
  });

  it('fetches models via /v1/models with valid API key for Meta provider', async () => {
    mocks.getMetaConfigs.mockResolvedValue([]);
    mocks.fetchV1ModelsViaApiCall.mockResolvedValueOnce([{ name: 'muse-model' }]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={null}
          disabled={false}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          providerKind="meta"
        />
      );
    });

    const apiKeyInput = renderer!.root.findAllByType('input')[0];
    act(() => apiKeyInput?.props.onChange({ target: { value: 'meta-valid-key' } }));

    const fetchButton = findFetchModelsButton(renderer!.root);
    expect(fetchButton?.props.disabled).toBe(false);

    await act(async () => {
      await fetchButton?.props.onClick();
    });

    expect(mocks.fetchV1ModelsViaApiCall).toHaveBeenCalledWith(
      'https://api.meta.ai/v1',
      'meta-valid-key',
      expect.any(Object),
      undefined,
      ''
    );
    const errorBox = renderer!.root
      .findAllByType('div')
      .find((div) => div.props.className === 'error-box');
    expect(errorBox).toBeUndefined();

    act(() => renderer!.unmount());
  });

  it('allows model discovery without API key for Codex provider using auth index', async () => {
    mocks.fetchConfig.mockResolvedValueOnce([
      { apiKey: '', baseUrl: 'https://api.openai.com/v1', authIndex: 'codex-oauth' },
    ]);
    mocks.fetchV1ModelsViaApiCall.mockResolvedValueOnce([{ name: 'gpt-4o' }]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={0}
          disabled={false}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          providerKind="codex"
        />
      );
    });

    const fetchButton = findFetchModelsButton(renderer!.root);
    expect(fetchButton?.props.disabled).toBe(false);

    await act(async () => {
      await fetchButton?.props.onClick();
    });

    expect(mocks.fetchV1ModelsViaApiCall).toHaveBeenCalledWith(
      'https://api.openai.com/v1',
      undefined,
      expect.any(Object),
      'codex-oauth',
      undefined
    );

    act(() => renderer!.unmount());
  });

  it('rejects save when Meta provider has custom DCA Authorization header (Bearer dca:test)', async () => {
    mocks.createMetaConfig.mockResolvedValue(undefined);
    mocks.getMetaConfigs.mockResolvedValue([]);
    const onSaved = vi.fn();

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={null}
          disabled={false}
          onClose={vi.fn()}
          onSaved={onSaved}
          providerKind="meta"
        />
      );
    });

    const apiKeyInput = renderer!.root.findAllByType('input')[0];
    act(() => apiKeyInput?.props.onChange({ target: { value: 'meta-valid-key' } }));

    const headerList = renderer!.root.findByType(HeaderInputList);
    act(() =>
      headerList.props.onChange([{ key: 'Authorization', value: 'Bearer dca:secret-token' }])
    );

    const saveButton = findSaveButton(renderer!.root);
    await act(async () => {
      await saveButton?.props.onClick();
    });

    expect(mocks.showNotification).toHaveBeenCalledWith(
      i18n.t('ai_providers.meta_dca_not_accepted'),
      'error'
    );
    expect(mocks.createMetaConfig).not.toHaveBeenCalled();

    act(() => renderer!.unmount());
  });

  it('rejects save when Meta provider has custom DCA Authorization header (dca:test)', async () => {
    mocks.createMetaConfig.mockResolvedValue(undefined);
    mocks.getMetaConfigs.mockResolvedValue([]);
    const onSaved = vi.fn();

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={null}
          disabled={false}
          onClose={vi.fn()}
          onSaved={onSaved}
          providerKind="meta"
        />
      );
    });

    const apiKeyInput = renderer!.root.findAllByType('input')[0];
    act(() => apiKeyInput?.props.onChange({ target: { value: 'meta-valid-key' } }));

    const headerList = renderer!.root.findByType(HeaderInputList);
    act(() => headerList.props.onChange([{ key: 'authorization', value: 'dca:secret-token' }]));

    const saveButton = findSaveButton(renderer!.root);
    await act(async () => {
      await saveButton?.props.onClick();
    });

    expect(mocks.showNotification).toHaveBeenCalledWith(
      i18n.t('ai_providers.meta_dca_not_accepted'),
      'error'
    );
    expect(mocks.createMetaConfig).not.toHaveBeenCalled();

    act(() => renderer!.unmount());
  });

  it('allows save when Meta provider has valid custom Authorization header', async () => {
    mocks.createMetaConfig.mockResolvedValue(undefined);
    mocks.getMetaConfigs.mockResolvedValue([]);
    const onSaved = vi.fn();

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={null}
          disabled={false}
          onClose={vi.fn()}
          onSaved={onSaved}
          providerKind="meta"
        />
      );
    });

    const apiKeyInput = renderer!.root.findAllByType('input')[0];
    act(() => apiKeyInput?.props.onChange({ target: { value: 'meta-valid-key' } }));

    const headerList = renderer!.root.findByType(HeaderInputList);
    act(() =>
      headerList.props.onChange([{ key: 'Authorization', value: 'Bearer meta-custom-token' }])
    );

    const saveButton = findSaveButton(renderer!.root);
    await act(async () => {
      await saveButton?.props.onClick();
    });

    expect(mocks.createMetaConfig).toHaveBeenCalledWith(
      expect.objectContaining({
        apiKey: 'meta-valid-key',
        baseUrl: 'https://api.meta.ai/v1',
        headers: { Authorization: 'Bearer meta-custom-token' },
      })
    );
    expect(onSaved).toHaveBeenCalledTimes(1);

    act(() => renderer!.unmount());
  });

  it('rejects model discovery when Meta provider has custom DCA Authorization header', async () => {
    mocks.getMetaConfigs.mockResolvedValue([]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={null}
          disabled={false}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          providerKind="meta"
        />
      );
    });

    const apiKeyInput = renderer!.root.findAllByType('input')[0];
    act(() => apiKeyInput?.props.onChange({ target: { value: 'meta-valid-key' } }));

    const headerList = renderer!.root.findByType(HeaderInputList);
    act(() =>
      headerList.props.onChange([{ key: 'Authorization', value: 'Bearer dca:secret-token' }])
    );

    const fetchButton = findFetchModelsButton(renderer!.root);
    expect(fetchButton?.props.disabled).toBe(true);

    await act(async () => {
      await fetchButton?.props.onClick();
    });

    expect(mocks.fetchV1ModelsViaApiCall).not.toHaveBeenCalled();
    const errorBox = renderer!.root
      .findAllByType('div')
      .find((div) => div.props.className === 'error-box');
    expect(errorBox).toBeDefined();
    expect(errorBox?.children.join('')).toBe(i18n.t('ai_providers.meta_dca_not_accepted'));

    act(() => renderer!.unmount());
  });

  it('allows model discovery when Meta provider has valid custom Authorization header', async () => {
    mocks.getMetaConfigs.mockResolvedValue([]);
    mocks.fetchV1ModelsViaApiCall.mockResolvedValueOnce([{ name: 'llama-3.3-70b-instruct' }]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={null}
          disabled={false}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          providerKind="meta"
        />
      );
    });

    const apiKeyInput = renderer!.root.findAllByType('input')[0];
    act(() => apiKeyInput?.props.onChange({ target: { value: 'meta-valid-key' } }));

    const headerList = renderer!.root.findByType(HeaderInputList);
    act(() =>
      headerList.props.onChange([{ key: 'Authorization', value: 'Bearer meta-custom-token' }])
    );

    const fetchButton = findFetchModelsButton(renderer!.root);
    expect(fetchButton?.props.disabled).toBe(false);

    await act(async () => {
      await fetchButton?.props.onClick();
    });

    expect(mocks.fetchV1ModelsViaApiCall).toHaveBeenCalledWith(
      'https://api.meta.ai/v1',
      undefined,
      expect.objectContaining({ Authorization: 'Bearer meta-custom-token' }),
      undefined,
      ''
    );

    act(() => renderer!.unmount());
  });

  it('rejects connectivity test when Meta provider has custom DCA Authorization header', async () => {
    mocks.getMetaConfigs.mockResolvedValue([]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={null}
          disabled={false}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          providerKind="meta"
        />
      );
    });

    const apiKeyInput = renderer!.root.findAllByType('input')[0];
    act(() => apiKeyInput?.props.onChange({ target: { value: 'meta-valid-key' } }));

    const headerList = renderer!.root.findByType(HeaderInputList);
    act(() =>
      headerList.props.onChange([{ key: 'Authorization', value: 'Bearer dca:secret-token' }])
    );

    const testButton = findConnectivityTestButton(renderer!.root);
    expect(testButton).toBeDefined();

    await act(async () => {
      await testButton?.props.onClick();
    });

    expect(mocks.fetchV1ModelsViaApiCall).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      i18n.t('ai_providers.meta_dca_not_accepted'),
      'error'
    );

    act(() => renderer!.unmount());
  });

  it('allows connectivity test when Meta provider has valid custom Authorization header', async () => {
    mocks.getMetaConfigs.mockResolvedValue([]);
    mocks.fetchV1ModelsViaApiCall.mockResolvedValueOnce([{ name: 'llama-3.3-70b-instruct' }]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={null}
          disabled={false}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          providerKind="meta"
        />
      );
    });

    const apiKeyInput = renderer!.root.findAllByType('input')[0];
    act(() => apiKeyInput?.props.onChange({ target: { value: 'meta-valid-key' } }));

    const headerList = renderer!.root.findByType(HeaderInputList);
    act(() =>
      headerList.props.onChange([{ key: 'Authorization', value: 'Bearer meta-custom-token' }])
    );

    const testButton = findConnectivityTestButton(renderer!.root);
    expect(testButton).toBeDefined();

    await act(async () => {
      await testButton?.props.onClick();
    });

    expect(mocks.fetchV1ModelsViaApiCall).toHaveBeenCalledWith(
      'https://api.meta.ai/v1',
      undefined,
      expect.objectContaining({ Authorization: 'Bearer meta-custom-token' }),
      undefined,
      ''
    );
    expect(mocks.showNotification).toHaveBeenCalledWith(
      i18n.t('ai_providers.meta_test_success'),
      'success'
    );

    act(() => renderer!.unmount());
  });

  it('rejects connectivity test when Meta provider has DCA apiKey', async () => {
    mocks.getMetaConfigs.mockResolvedValue([]);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <CodexEditDrawer
          open
          editIndex={null}
          disabled={false}
          onClose={vi.fn()}
          onSaved={vi.fn()}
          providerKind="meta"
        />
      );
    });

    const apiKeyInput = renderer!.root.findAllByType('input')[0];
    act(() => apiKeyInput?.props.onChange({ target: { value: 'dca:secret-token' } }));

    const testButton = findConnectivityTestButton(renderer!.root);
    expect(testButton).toBeDefined();

    await act(async () => {
      await testButton?.props.onClick();
    });

    expect(mocks.fetchV1ModelsViaApiCall).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      i18n.t('ai_providers.meta_dca_not_accepted'),
      'error'
    );

    act(() => renderer!.unmount());
  });
});
