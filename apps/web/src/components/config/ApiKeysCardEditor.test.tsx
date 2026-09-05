import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import { sha256Hex } from '@/utils/apiKeyHash';
import { ApiKeysCardEditor, type ApiKeyMutation } from './ApiKeysCardEditor';

const mocks = vi.hoisted(() => ({
  getApiKeyAliases: vi.fn(),
  saveApiKeyAliases: vi.fn(),
  deleteApiKeyAlias: vi.fn(),
  showNotification: vi.fn(),
  showConfirmation: vi.fn(),
  featureAvailability: {
    checking: false,
    panelHostConfirmed: true,
    panelHostMode: 'manager_embedded',
    managerServiceAvailable: true,
    managerServiceBase: 'http://manager.local',
  },
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
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
  }) =>
    open ? (
      <div data-test-modal="open">
        {children}
        {footer}
      </div>
    ) : null,
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => mocks.featureAvailability,
}));

vi.mock('@/services/api/usageService', () => ({
  usageServiceApi: {
    getApiKeyAliases: mocks.getApiKeyAliases,
    saveApiKeyAliases: mocks.saveApiKeyAliases,
    deleteApiKeyAlias: mocks.deleteApiKeyAlias,
  },
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: { managementKey: string }) => unknown) =>
    selector({ managementKey: 'management-key' }),
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
}));

type Confirmation = {
  onConfirm: () => void | Promise<void>;
};

type EditorMount = {
  renderer: ReactTestRenderer;
  getValue: () => string;
  updateValue: (value: string) => void;
};

const API_KEY_PLACEHOLDER = 'config_management.visual.api_keys.input_placeholder';
const ALIAS_PLACEHOLDER = 'config_management.visual.api_keys.alias_placeholder';

let pendingConfirmation: Confirmation | null = null;
const mountedRenderers: ReactTestRenderer[] = [];

const flush = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
};

const button = (renderer: ReactTestRenderer, label: string, occurrence = 0) => {
  const matches = renderer.root
    .findAllByType(Button)
    .filter((candidate) => candidate.props.children === label);
  const result = matches[occurrence];
  if (!result) throw new Error(`Button not found: ${label} (${occurrence})`);
  return result;
};

const clickButton = async (renderer: ReactTestRenderer, label: string, occurrence = 0) => {
  await act(async () => {
    await button(renderer, label, occurrence).props.onClick();
  });
};

const input = (renderer: ReactTestRenderer, placeholder: string) =>
  renderer.root.findByProps({ placeholder });

const setInput = (renderer: ReactTestRenderer, placeholder: string, value: string) => {
  act(() => {
    input(renderer, placeholder).props.onChange({ target: { value } });
  });
};

const mountEditor = (
  initialValue: string,
  options: {
    aliases?: Array<{ apiKeyHash: string; alias: string }>;
    loadAliases?: () => Promise<{ items: Array<{ apiKeyHash: string; alias: string }> }>;
    onPersistApiKeyMutation?: (mutation: ApiKeyMutation) => Promise<string[]>;
    onRefreshApiKeys?: () => Promise<string[]>;
    onApiKeyOperationStart?: () => void;
    onApiKeyOperationEnd?: () => void;
  } = {}
): EditorMount => {
  let currentValue = initialValue;
  let renderer!: ReactTestRenderer;
  const onPersistApiKeyMutation =
    options.onPersistApiKeyMutation ?? vi.fn(async () => currentValue.split('\n').filter(Boolean));
  const onRefreshApiKeys =
    options.onRefreshApiKeys ?? vi.fn(async () => currentValue.split('\n').filter(Boolean));
  const onApiKeyOperationStart = options.onApiKeyOperationStart ?? vi.fn();
  const onApiKeyOperationEnd = options.onApiKeyOperationEnd ?? vi.fn();

  mocks.getApiKeyAliases.mockImplementation(
    options.loadAliases ?? (async () => ({ items: options.aliases ?? [] }))
  );

  const render = () => (
    <ApiKeysCardEditor
      value={currentValue}
      onPersistApiKeyMutation={onPersistApiKeyMutation}
      onRefreshApiKeys={onRefreshApiKeys}
      onApiKeyOperationStart={onApiKeyOperationStart}
      onApiKeyOperationEnd={onApiKeyOperationEnd}
    />
  );

  act(() => {
    renderer = create(render());
  });
  mountedRenderers.push(renderer);

  return {
    renderer,
    getValue: () => currentValue,
    updateValue: (value: string) => {
      currentValue = value;
      act(() => renderer.update(render()));
    },
  };
};

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

beforeEach(() => {
  vi.clearAllMocks();
  pendingConfirmation = null;
  mocks.getApiKeyAliases.mockResolvedValue({ items: [] });
  mocks.saveApiKeyAliases.mockImplementation(async (_base, items) => ({ items }));
  mocks.deleteApiKeyAlias.mockResolvedValue(undefined);
  mocks.featureAvailability.checking = false;
  mocks.featureAvailability.panelHostConfirmed = true;
  mocks.featureAvailability.panelHostMode = 'manager_embedded';
  mocks.featureAvailability.managerServiceAvailable = true;
  mocks.featureAvailability.managerServiceBase = 'http://manager.local';
  mocks.showConfirmation.mockImplementation((options: Confirmation) => {
    pendingConfirmation = options;
  });
});

afterEach(() => {
  for (const renderer of mountedRenderers.splice(0)) {
    act(() => renderer.unmount());
  }
});

describe('ApiKeysCardEditor immediate CPA persistence', () => {
  it('persists a new key without an alias and renders the canonical value', async () => {
    const persisted: ApiKeyMutation[] = [];
    const onPersistApiKeyMutation = vi.fn(async (mutation: ApiKeyMutation) => {
      persisted.push(mutation);
      editor.updateValue('sk-new');
      return ['sk-new'];
    });
    const editor = mountEditor(' ', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.api_keys.add');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    await clickButton(editor.renderer, 'config_management.visual.common.add');

    expect(persisted).toEqual([{ type: 'create', apiKey: 'sk-new' }]);
    expect(mocks.saveApiKeyAliases).not.toHaveBeenCalled();
    expect(editor.getValue()).toBe('sk-new');
    expect(editor.renderer.root.findAllByProps({ className: 'item-row' })).toHaveLength(1);
    expect(editor.renderer.root.findAllByProps({ 'data-test-modal': 'open' })).toHaveLength(0);
  });

  it('saves CPA before saving an alias for a new key', async () => {
    const events: string[] = [];
    const onPersistApiKeyMutation = vi.fn(async () => {
      events.push('cpa');
      editor.updateValue('sk-new');
      return ['sk-new'];
    });
    mocks.saveApiKeyAliases.mockImplementation(async (_base, items) => {
      events.push('alias');
      return { items };
    });
    const editor = mountEditor('', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.api_keys.add');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    setInput(editor.renderer, ALIAS_PLACEHOLDER, 'MacBook');
    await clickButton(editor.renderer, 'config_management.visual.common.add');

    expect(events).toEqual(['cpa', 'alias']);
    expect(onPersistApiKeyMutation).toHaveBeenCalledWith({ type: 'create', apiKey: 'sk-new' });
  });

  it('keeps the modal open and skips alias persistence when CPA create fails', async () => {
    const onPersistApiKeyMutation = vi.fn(async () => {
      throw new Error('CPA unavailable');
    });
    const editor = mountEditor('', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.api_keys.add');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    setInput(editor.renderer, ALIAS_PLACEHOLDER, 'MacBook');
    await clickButton(editor.renderer, 'config_management.visual.common.add');

    expect(onPersistApiKeyMutation).toHaveBeenCalledTimes(1);
    expect(mocks.saveApiKeyAliases).not.toHaveBeenCalled();
    expect(editor.getValue()).toBe('');
    expect(editor.renderer.root.findAllByProps({ 'data-test-modal': 'open' })).toHaveLength(1);
    expect(
      editor.renderer.root.findByProps({ className: 'error-box' }).children.join('')
    ).toContain('CPA unavailable');
  });

  it('keeps the modal open and skips alias persistence when the CPA mutation outcome is unknown', async () => {
    const mutationError = new Error('mutation outcome unknown') as Error & { code?: string };
    mutationError.code = 'api_key_mutation_outcome_unknown';
    const onPersistApiKeyMutation = vi.fn(async () => {
      throw mutationError;
    });
    const editor = mountEditor('', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.api_keys.add');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    setInput(editor.renderer, ALIAS_PLACEHOLDER, 'MacBook');
    await clickButton(editor.renderer, 'config_management.visual.common.add');

    expect(mocks.saveApiKeyAliases).not.toHaveBeenCalled();
    expect(editor.renderer.root.findAllByProps({ 'data-test-modal': 'open' })).toHaveLength(1);
    const errorBox = editor.renderer.root.findByProps({ className: 'error-box' });
    expect(errorBox.children.join('')).toBe('mutation outcome unknown');
    expect(errorBox.children.join('')).not.toContain('save_failed');
  });

  it('reports an unknown API-key state after CPA succeeds but canonical refresh fails', async () => {
    const refreshError = new Error('state refresh failed') as Error & { code?: string };
    refreshError.code = 'api_key_state_refresh_failed';
    const onPersistApiKeyMutation = vi.fn(async () => {
      throw refreshError;
    });
    const editor = mountEditor('', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.api_keys.add');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    setInput(editor.renderer, ALIAS_PLACEHOLDER, 'MacBook');
    await clickButton(editor.renderer, 'config_management.visual.common.add');

    expect(mocks.saveApiKeyAliases).not.toHaveBeenCalled();
    expect(editor.renderer.root.findAllByProps({ 'data-test-modal': 'open' })).toHaveLength(1);
    expect(
      editor.renderer.root.findByProps({ className: 'error-box' }).children.join('')
    ).toContain('state refresh failed');
  });

  it('keeps a CPA-created key when alias persistence fails and reports partial success', async () => {
    const onPersistApiKeyMutation = vi.fn(async () => {
      editor.updateValue('sk-new');
      return ['sk-new'];
    });
    mocks.saveApiKeyAliases.mockRejectedValue(new Error('alias unavailable'));
    const editor = mountEditor('', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.api_keys.add');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    setInput(editor.renderer, ALIAS_PLACEHOLDER, 'MacBook');
    await clickButton(editor.renderer, 'config_management.visual.common.add');

    expect(editor.getValue()).toBe('sk-new');
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'config_management.visual.api_keys.save_partial_success',
      'warning'
    );
    expect(editor.renderer.root.findAllByProps({ 'data-test-modal': 'open' })).toHaveLength(0);
  });

  it('refreshes CPA before saving a changed alias without mutating the key', async () => {
    const events: string[] = [];
    const onPersistApiKeyMutation = vi.fn(async () => {
      events.push('unexpected-cpa-mutation');
      return ['sk-existing'];
    });
    const onRefreshApiKeys = vi.fn(async () => {
      events.push('cpa-get');
      return ['sk-existing'];
    });
    mocks.saveApiKeyAliases.mockImplementation(async (_base, items) => {
      events.push('alias');
      return { items };
    });
    const editor = mountEditor('sk-existing', { onPersistApiKeyMutation, onRefreshApiKeys });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.edit');
    setInput(editor.renderer, ALIAS_PLACEHOLDER, 'Claude Code');
    await clickButton(editor.renderer, 'config_management.visual.common.update');

    expect(events).toEqual(['cpa-get', 'alias']);
    expect(onPersistApiKeyMutation).not.toHaveBeenCalled();
    expect(mocks.saveApiKeyAliases).toHaveBeenCalledTimes(1);
  });

  it('does not create an alias for a key that disappeared from CPA', async () => {
    const onRefreshApiKeys = vi.fn(async () => []);
    const editor = mountEditor('sk-stale', { onRefreshApiKeys });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.edit');
    setInput(editor.renderer, ALIAS_PLACEHOLDER, 'Stale Alias');
    await clickButton(editor.renderer, 'config_management.visual.common.update');

    expect(onRefreshApiKeys).toHaveBeenCalledTimes(1);
    expect(mocks.saveApiKeyAliases).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'config_management.visual.api_keys.stale_key_refreshed',
      'warning'
    );
  });

  it('blocks replace while the Manager Alias list is still loading', async () => {
    const onPersistApiKeyMutation = vi.fn(async () => ['sk-new']);
    const loadAliases = () =>
      new Promise<{ items: Array<{ apiKeyHash: string; alias: string }> }>(() => {});
    const editor = mountEditor('sk-old', { loadAliases, onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.edit');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    await clickButton(editor.renderer, 'config_management.visual.common.update');

    expect(onPersistApiKeyMutation).not.toHaveBeenCalled();
    expect(
      editor.renderer.root.findByProps({ className: 'error-box' }).children.join('')
    ).toContain('config_management.visual.api_keys.alias_state_unavailable');
  });

  it('blocks replace while Alias capability detection is still checking', async () => {
    mocks.featureAvailability.checking = true;
    mocks.featureAvailability.panelHostConfirmed = false;
    mocks.featureAvailability.panelHostMode = 'external_panel';
    mocks.featureAvailability.managerServiceAvailable = false;
    mocks.featureAvailability.managerServiceBase = '';
    const onPersistApiKeyMutation = vi.fn(async () => ['sk-new']);
    const editor = mountEditor('sk-old', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.edit');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    await clickButton(editor.renderer, 'config_management.visual.common.update');

    expect(onPersistApiKeyMutation).not.toHaveBeenCalled();
    expect(
      editor.renderer.root.findByProps({ className: 'error-box' }).children.join('')
    ).toContain('config_management.visual.api_keys.alias_state_unavailable');
  });

  it('blocks delete while Alias capability detection is still checking', async () => {
    mocks.featureAvailability.checking = true;
    mocks.featureAvailability.panelHostConfirmed = false;
    mocks.featureAvailability.panelHostMode = 'external_panel';
    mocks.featureAvailability.managerServiceAvailable = false;
    mocks.featureAvailability.managerServiceBase = '';
    const onPersistApiKeyMutation = vi.fn(async () => []);
    const editor = mountEditor('sk-old', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.delete');

    expect(pendingConfirmation).toBeNull();
    expect(onPersistApiKeyMutation).not.toHaveBeenCalled();
    expect(mocks.deleteApiKeyAlias).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'config_management.visual.api_keys.alias_state_unavailable',
      'warning'
    );
  });

  it('blocks replace while the panel host remains unconfirmed', async () => {
    mocks.featureAvailability.checking = false;
    mocks.featureAvailability.panelHostConfirmed = false;
    mocks.featureAvailability.panelHostMode = 'external_panel';
    mocks.featureAvailability.managerServiceAvailable = false;
    mocks.featureAvailability.managerServiceBase = '';
    const onPersistApiKeyMutation = vi.fn(async () => ['sk-new']);
    const editor = mountEditor('sk-old', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.edit');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    await clickButton(editor.renderer, 'config_management.visual.common.update');

    expect(onPersistApiKeyMutation).not.toHaveBeenCalled();
    expect(
      editor.renderer.root.findByProps({ className: 'error-box' }).children.join('')
    ).toContain('config_management.visual.api_keys.alias_state_unavailable');
  });

  it('blocks delete while the panel host remains unconfirmed', async () => {
    mocks.featureAvailability.checking = false;
    mocks.featureAvailability.panelHostConfirmed = false;
    mocks.featureAvailability.panelHostMode = 'external_panel';
    mocks.featureAvailability.managerServiceAvailable = false;
    mocks.featureAvailability.managerServiceBase = '';
    const onPersistApiKeyMutation = vi.fn(async () => []);
    const editor = mountEditor('sk-old', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.delete');

    expect(pendingConfirmation).toBeNull();
    expect(onPersistApiKeyMutation).not.toHaveBeenCalled();
    expect(mocks.deleteApiKeyAlias).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'config_management.visual.api_keys.alias_state_unavailable',
      'warning'
    );
  });

  it('blocks replace when a known Manager-hosted panel has no Alias service', async () => {
    mocks.featureAvailability.checking = false;
    mocks.featureAvailability.panelHostConfirmed = true;
    mocks.featureAvailability.panelHostMode = 'manager_embedded';
    mocks.featureAvailability.managerServiceAvailable = false;
    mocks.featureAvailability.managerServiceBase = '';
    const onPersistApiKeyMutation = vi.fn(async () => ['sk-new']);
    const editor = mountEditor('sk-old', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.edit');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    await clickButton(editor.renderer, 'config_management.visual.common.update');

    expect(onPersistApiKeyMutation).not.toHaveBeenCalled();
    expect(
      editor.renderer.root.findByProps({ className: 'error-box' }).children.join('')
    ).toContain('config_management.visual.api_keys.alias_state_unavailable');
  });

  it('blocks delete when a known Manager-hosted panel has no Alias service', async () => {
    mocks.featureAvailability.checking = false;
    mocks.featureAvailability.panelHostConfirmed = true;
    mocks.featureAvailability.panelHostMode = 'manager_embedded';
    mocks.featureAvailability.managerServiceAvailable = false;
    mocks.featureAvailability.managerServiceBase = '';
    const onPersistApiKeyMutation = vi.fn(async () => []);
    const editor = mountEditor('sk-old', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.delete');

    expect(pendingConfirmation).toBeNull();
    expect(onPersistApiKeyMutation).not.toHaveBeenCalled();
    expect(mocks.deleteApiKeyAlias).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'config_management.visual.api_keys.alias_state_unavailable',
      'warning'
    );
  });

  it('allows replace in a known external CPA panel without Alias APIs', async () => {
    mocks.featureAvailability.checking = false;
    mocks.featureAvailability.panelHostConfirmed = true;
    mocks.featureAvailability.panelHostMode = 'external_panel';
    mocks.featureAvailability.managerServiceAvailable = false;
    mocks.featureAvailability.managerServiceBase = '';
    const onPersistApiKeyMutation = vi.fn(async () => ['sk-new']);
    const editor = mountEditor('sk-old', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.edit');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    await clickButton(editor.renderer, 'config_management.visual.common.update');

    expect(onPersistApiKeyMutation).toHaveBeenCalledWith({
      type: 'replace',
      oldApiKey: 'sk-old',
      newApiKey: 'sk-new',
    });
    expect(mocks.saveApiKeyAliases).not.toHaveBeenCalled();
    expect(mocks.deleteApiKeyAlias).not.toHaveBeenCalled();
  });

  it('allows delete in a known external CPA panel without Alias cleanup', async () => {
    mocks.featureAvailability.checking = false;
    mocks.featureAvailability.panelHostConfirmed = true;
    mocks.featureAvailability.panelHostMode = 'external_panel';
    mocks.featureAvailability.managerServiceAvailable = false;
    mocks.featureAvailability.managerServiceBase = '';
    const onPersistApiKeyMutation = vi.fn(async () => []);
    const editor = mountEditor('sk-old', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.delete');
    expect(pendingConfirmation).not.toBeNull();
    await act(async () => {
      await pendingConfirmation?.onConfirm();
    });

    expect(onPersistApiKeyMutation).toHaveBeenCalledWith({ type: 'delete', apiKey: 'sk-old' });
    expect(mocks.deleteApiKeyAlias).not.toHaveBeenCalled();
  });

  it('blocks replace when the Manager Alias list is unavailable', async () => {
    const onPersistApiKeyMutation = vi.fn(async () => ['sk-new']);
    const editor = mountEditor('sk-old', {
      loadAliases: async () => {
        throw new Error('alias list unavailable');
      },
      onPersistApiKeyMutation,
    });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.edit');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    await clickButton(editor.renderer, 'config_management.visual.common.update');

    expect(onPersistApiKeyMutation).not.toHaveBeenCalled();
    expect(
      editor.renderer.root.findByProps({ className: 'error-box' }).children.join('')
    ).toContain('config_management.visual.api_keys.alias_state_unavailable');
  });

  it('allows creating a key without an alias when the Manager Alias list is unavailable', async () => {
    const onPersistApiKeyMutation = vi.fn(async () => ['sk-new']);
    const editor = mountEditor('', {
      loadAliases: async () => {
        throw new Error('alias list unavailable');
      },
      onPersistApiKeyMutation,
    });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.api_keys.add');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    await clickButton(editor.renderer, 'config_management.visual.common.add');

    expect(onPersistApiKeyMutation).toHaveBeenCalledWith({ type: 'create', apiKey: 'sk-new' });
  });

  it('allows creating a key without an alias while the panel host is unconfirmed', async () => {
    mocks.featureAvailability.checking = false;
    mocks.featureAvailability.panelHostConfirmed = false;
    mocks.featureAvailability.panelHostMode = 'external_panel';
    mocks.featureAvailability.managerServiceAvailable = false;
    mocks.featureAvailability.managerServiceBase = '';
    const onPersistApiKeyMutation = vi.fn(async () => ['sk-new']);
    const editor = mountEditor('', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.api_keys.add');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    await clickButton(editor.renderer, 'config_management.visual.common.add');

    expect(onPersistApiKeyMutation).toHaveBeenCalledWith({ type: 'create', apiKey: 'sk-new' });
  });

  it('migrates a same-value alias without redundantly deleting the old hash', async () => {
    const events: string[] = [];
    const oldHash = sha256Hex('sk-old');
    const onPersistApiKeyMutation = vi.fn(async () => {
      events.push('cpa-patch');
      editor.updateValue('sk-new');
      return ['sk-new'];
    });
    mocks.saveApiKeyAliases.mockImplementation(async (_base, items) => {
      events.push('alias-save');
      return { items };
    });
    mocks.deleteApiKeyAlias.mockImplementation(async () => {
      events.push('alias-delete');
    });
    const editor = mountEditor('sk-old', {
      aliases: [{ apiKeyHash: oldHash, alias: 'MacBook' }],
      onPersistApiKeyMutation,
    });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.edit');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    await clickButton(editor.renderer, 'config_management.visual.common.update');

    expect(events).toEqual(['cpa-patch', 'alias-save']);
    expect(mocks.saveApiKeyAliases).toHaveBeenCalledWith(
      'http://manager.local',
      [{ apiKeyHash: sha256Hex('sk-new'), alias: 'MacBook' }],
      'management-key',
      [sha256Hex('sk-new')],
      true
    );
    expect(mocks.deleteApiKeyAlias).not.toHaveBeenCalled();
  });

  it('deletes the old hash after a changed alias migration', async () => {
    const events: string[] = [];
    const oldHash = sha256Hex('sk-old');
    const onPersistApiKeyMutation = vi.fn(async () => {
      events.push('cpa-patch');
      editor.updateValue('sk-new');
      return ['sk-new'];
    });
    mocks.saveApiKeyAliases.mockImplementation(async (_base, items) => {
      events.push('alias-save');
      return { items };
    });
    mocks.deleteApiKeyAlias.mockImplementation(async () => {
      events.push('alias-delete');
    });
    const editor = mountEditor('sk-old', {
      aliases: [{ apiKeyHash: oldHash, alias: 'MacBook' }],
      onPersistApiKeyMutation,
    });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.edit');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    setInput(editor.renderer, ALIAS_PLACEHOLDER, 'Server');
    await clickButton(editor.renderer, 'config_management.visual.common.update');

    expect(events).toEqual(['cpa-patch', 'alias-save', 'alias-delete']);
    expect(mocks.deleteApiKeyAlias).toHaveBeenCalledWith(
      'http://manager.local',
      oldHash,
      'management-key'
    );
  });

  it('retains the old alias when the new alias migration write fails', async () => {
    const events: string[] = [];
    const oldHash = sha256Hex('sk-old');
    const onPersistApiKeyMutation = vi.fn(async () => {
      events.push('cpa-patch');
      editor.updateValue('sk-new');
      return ['sk-new'];
    });
    mocks.saveApiKeyAliases.mockImplementation(async () => {
      events.push('alias-save');
      throw new Error('alias migration failed');
    });
    mocks.deleteApiKeyAlias.mockImplementation(async () => {
      events.push('alias-delete');
    });
    const editor = mountEditor('sk-old', {
      aliases: [{ apiKeyHash: oldHash, alias: 'MacBook' }],
      onPersistApiKeyMutation,
    });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.edit');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    await clickButton(editor.renderer, 'config_management.visual.common.update');

    expect(events).toEqual(['cpa-patch', 'alias-save']);
    expect(mocks.deleteApiKeyAlias).not.toHaveBeenCalled();
    expect(editor.getValue()).toBe('sk-new');
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'config_management.visual.api_keys.update_partial_success',
      'warning'
    );
  });

  it('closes with a stale warning when replace preflight rejects a missing old key', async () => {
    const staleError = new Error('stale key') as Error & { code?: string };
    staleError.code = 'api_key_stale';
    const onPersistApiKeyMutation = vi.fn(async () => {
      throw staleError;
    });
    const editor = mountEditor('sk-old', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.edit');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    await clickButton(editor.renderer, 'config_management.visual.common.update');

    expect(mocks.showNotification).toHaveBeenCalledWith(
      'config_management.visual.api_keys.stale_key_refreshed',
      'warning'
    );
    expect(editor.renderer.root.findAllByProps({ 'data-test-modal': 'open' })).toHaveLength(0);
  });

  it('deletes CPA before cleaning up the alias', async () => {
    const events: string[] = [];
    const apiKeyHash = sha256Hex('sk-delete');
    const onPersistApiKeyMutation = vi.fn(async () => {
      events.push('cpa-delete');
      editor.updateValue('');
      return [];
    });
    mocks.deleteApiKeyAlias.mockImplementation(async () => {
      events.push('alias-delete');
    });
    const editor = mountEditor('sk-delete', {
      aliases: [{ apiKeyHash, alias: 'Delete Me' }],
      onPersistApiKeyMutation,
    });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.delete');
    expect(pendingConfirmation).not.toBeNull();
    await act(async () => {
      await pendingConfirmation?.onConfirm();
    });

    expect(events).toEqual(['cpa-delete', 'alias-delete']);
    expect(editor.getValue()).toBe('');
  });

  it('does not clean up the alias when CPA delete fails', async () => {
    const onPersistApiKeyMutation = vi.fn(async () => {
      throw new Error('CPA delete failed');
    });
    const editor = mountEditor('sk-delete', {
      aliases: [{ apiKeyHash: sha256Hex('sk-delete'), alias: 'Delete Me' }],
      onPersistApiKeyMutation,
    });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.delete');
    await act(async () => {
      await pendingConfirmation?.onConfirm();
    });

    expect(mocks.deleteApiKeyAlias).not.toHaveBeenCalled();
    expect(editor.getValue()).toBe('sk-delete');
  });

  it('keeps a deleted key absent when alias cleanup fails', async () => {
    const apiKeyHash = sha256Hex('sk-delete');
    const onPersistApiKeyMutation = vi.fn(async () => {
      editor.updateValue('');
      return [];
    });
    mocks.deleteApiKeyAlias.mockRejectedValue(new Error('alias cleanup failed'));
    const editor = mountEditor('sk-delete', {
      aliases: [{ apiKeyHash, alias: 'Delete Me' }],
      onPersistApiKeyMutation,
    });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.delete');
    await act(async () => {
      await pendingConfirmation?.onConfirm();
    });

    expect(editor.getValue()).toBe('');
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'config_management.visual.api_keys.delete_partial_success',
      'warning'
    );
  });

  it('deletes an alias even when the local Manager Alias list is unavailable', async () => {
    const apiKeyHash = sha256Hex('sk-delete');
    const onPersistApiKeyMutation = vi.fn(async () => {
      editor.updateValue('');
      return [];
    });
    const editor = mountEditor('sk-delete', {
      loadAliases: async () => {
        throw new Error('alias list unavailable');
      },
      onPersistApiKeyMutation,
    });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.common.delete');
    await act(async () => {
      await pendingConfirmation?.onConfirm();
    });

    expect(mocks.deleteApiKeyAlias).toHaveBeenCalledWith(
      'http://manager.local',
      apiKeyHash,
      'management-key'
    );
  });

  it('only fills the input when Generate is clicked', async () => {
    const onPersistApiKeyMutation = vi.fn(async () => ['sk-generated']);
    const editor = mountEditor('', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.api_keys.add');
    await clickButton(editor.renderer, 'config_management.visual.api_keys.generate');

    expect(input(editor.renderer, API_KEY_PLACEHOLDER).props.value).toMatch(/^sk-[A-Za-z0-9]+$/);
    expect(onPersistApiKeyMutation).not.toHaveBeenCalled();
  });

  it('allows only one mutation during a double click', async () => {
    let resolvePersistence!: (keys: string[]) => void;
    const onPersistApiKeyMutation = vi.fn(
      () => new Promise<string[]>((resolve) => (resolvePersistence = resolve))
    );
    const editor = mountEditor('', { onPersistApiKeyMutation });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.api_keys.add');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    const saveButton = button(editor.renderer, 'config_management.visual.common.add');
    let first!: Promise<void>;
    let second!: Promise<void>;
    await act(async () => {
      first = saveButton.props.onClick();
      second = saveButton.props.onClick();
      await Promise.resolve();
    });

    expect(onPersistApiKeyMutation).toHaveBeenCalledTimes(1);
    resolvePersistence(['sk-new']);
    await act(async () => {
      await Promise.all([first, second]);
    });
    expect(onPersistApiKeyMutation).toHaveBeenCalledTimes(1);
  });

  it('keeps API key CRUD blocked when the source guard rejects the operation', async () => {
    const onPersistApiKeyMutation = vi.fn(async () => ['sk-new']);
    const onApiKeyOperationStart = vi.fn(() => {
      const error = new Error('source is dirty') as Error & { code?: string };
      error.code = 'source_config_dirty';
      throw error;
    });
    const editor = mountEditor('', { onPersistApiKeyMutation, onApiKeyOperationStart });
    await flush();

    await clickButton(editor.renderer, 'config_management.visual.api_keys.add');
    setInput(editor.renderer, API_KEY_PLACEHOLDER, 'sk-new');
    await clickButton(editor.renderer, 'config_management.visual.common.add');

    expect(onApiKeyOperationStart).toHaveBeenCalledTimes(1);
    expect(onPersistApiKeyMutation).not.toHaveBeenCalled();
    expect(editor.renderer.root.findAllByProps({ 'data-test-modal': 'open' })).toHaveLength(1);
    expect(
      editor.renderer.root.findByProps({ className: 'error-box' }).children.join('')
    ).toContain('source is dirty');
  });
});
