import type { ReactElement, ReactNode } from 'react';
import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ProviderKeyConfig } from '@/types';

const mocks = vi.hoisted(() => ({
  t: (key: string) => key,
  config: { claudeApiKeys: [] as ProviderKeyConfig[] },
  fetchConfig: vi.fn(),
  updateConfigValue: vi.fn(),
  clearCache: vi.fn(),
  showNotification: vi.fn(),
  onSaved: vi.fn(),
  onClose: vi.fn(),
  updateClaudeConfig: vi.fn(),
  createClaudeConfig: vi.fn(),
  readBack: vi.fn(),
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: mocks.t }),
}));

vi.mock('@/components/ui/Drawer', () => ({
  Drawer: ({
    children,
    footer,
    title,
  }: {
    children?: ReactNode;
    footer?: ReactNode;
    title?: ReactNode;
  }) => (
    <div>
      <div data-drawer-title>{title}</div>
      {children}
      <div data-drawer-footer>{footer}</div>
    </div>
  ),
}));

vi.mock('@/components/ui/Modal', () => ({
  Modal: ({ children }: { children?: ReactNode }) => <div>{children}</div>,
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: { connectionStatus: string }) => unknown) =>
    selector({ connectionStatus: 'connected' }),
  useConfigStore: (selector: (state: Record<string, unknown>) => unknown) =>
    selector({
      config: mocks.config,
      fetchConfig: mocks.fetchConfig,
      updateConfigValue: mocks.updateConfigValue,
      clearCache: mocks.clearCache,
      isCacheValid: () => true,
    }),
  useNotificationStore: () => ({ showNotification: mocks.showNotification }),
}));

vi.mock('@/services/api', async () => {
  const actual = await import('@/services/api/providers');
  return {
    providersApi: {
      updateClaudeConfig: mocks.updateClaudeConfig,
      createClaudeConfig: mocks.createClaudeConfig,
    },
    readBackClaudeConfigAfterSave: mocks.readBack,
    verifyClaudeFingerprintInRawConfig: actual.verifyClaudeFingerprintInRawConfig,
    modelsApi: {
      getModels: vi.fn().mockResolvedValue([]),
      buildClaudeModelsEndpoint: vi.fn(() => ''),
    },
    apiCallApi: {
      request: vi.fn(),
    },
    getApiCallErrorMessage: () => '',
  };
});

import { ClaudeEditDrawer } from './ClaudeEditDrawer';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const flush = async () => {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
};

const renderDrawer = async () => {
  let renderer: ReactTestRenderer | undefined;
  await act(async () => {
    renderer = create(
      createElement(ClaudeEditDrawer, {
        open: true,
        editIndex: 0,
        disabled: false,
        onClose: mocks.onClose,
        onSaved: mocks.onSaved,
      }) as ReactElement
    );
  });
  await flush();
  return renderer!;
};

const setFingerprint = async (renderer: ReactTestRenderer, value: string) => {
  const select = renderer.root.find(
    (node) => node.props?.ariaLabel === 'ai_providers.claude_request_fingerprint_label'
  );
  await act(async () => {
    select.props.onChange(value);
  });
};

const save = async (renderer: ReactTestRenderer) => {
  const saveButton = renderer.root.find(
    (node) => node.props?.onClick && node.props?.children === 'common.save'
  );
  await act(async () => {
    await saveButton.props.onClick();
  });
};

const notifications = (type: string) =>
  mocks.showNotification.mock.calls.filter((call) => call[1] === type);

describe('ClaudeEditDrawer fingerprint save verification', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.config = {
      claudeApiKeys: [{ apiKey: 'key', baseUrl: 'https://api.anthropic.com' }],
    };
    mocks.fetchConfig.mockResolvedValue(mocks.config.claudeApiKeys);
    mocks.updateClaudeConfig.mockResolvedValue(undefined);
  });

  it('saves the fingerprint, verifies against the persisted raw /config, and closes as committed', async () => {
    mocks.readBack.mockResolvedValue({
      claudeApiKeys: [
        {
          apiKey: 'key',
          baseUrl: 'https://api.anthropic.com',
          fingerprintProfile: 'claude-code-cli',
        },
      ],
      rawRecords: [
        {
          'api-key': 'key',
          'base-url': 'https://api.anthropic.com',
          'fingerprint-profile': 'claude-code-cli',
        },
      ],
    });

    const renderer = await renderDrawer();
    await setFingerprint(renderer, 'claude-code-cli');
    await save(renderer);

    expect(mocks.updateClaudeConfig).toHaveBeenCalledWith(
      mocks.config.claudeApiKeys[0],
      expect.objectContaining({ fingerprintProfile: 'claude-code-cli' })
    );
    expect(notifications('warning')).toHaveLength(0);
    expect(notifications('success')[0][0]).toBe('notification.claude_config_updated');
    expect(mocks.updateConfigValue).toHaveBeenLastCalledWith('claude-api-key', [
      {
        apiKey: 'key',
        baseUrl: 'https://api.anthropic.com',
        fingerprintProfile: 'claude-code-cli',
      },
    ]);
    expect(mocks.onSaved).toHaveBeenCalledTimes(1);
    expect(mocks.onClose).toHaveBeenCalledTimes(1);
  });

  it('warns on a committed save when the CPA drops the fingerprint, then closes', async () => {
    mocks.readBack.mockResolvedValue({
      claudeApiKeys: [{ apiKey: 'key', baseUrl: 'https://api.anthropic.com' }],
      rawRecords: [{ 'api-key': 'key', 'base-url': 'https://api.anthropic.com' }],
    });

    const renderer = await renderDrawer();
    await setFingerprint(renderer, 'claude-code-cli');
    await save(renderer);

    expect(notifications('success')).toHaveLength(0);
    expect(notifications('warning')).toHaveLength(1);
    expect(notifications('warning')[0][0]).toBe('notification.claude_fingerprint_not_applied');
    expect(mocks.updateConfigValue).toHaveBeenLastCalledWith('claude-api-key', [
      { apiKey: 'key', baseUrl: 'https://api.anthropic.com' },
    ]);
    expect(mocks.onSaved).toHaveBeenCalledTimes(1);
    expect(mocks.onClose).toHaveBeenCalledTimes(1);
  });
  it('treats a re-picked Default on an untouched config as untouched when read-back fails', async () => {
    mocks.readBack.mockRejectedValue(new Error('read-back failed'));

    const renderer = await renderDrawer();
    // undefined (untouched) and '' (explicit Default) both display Default;
    // re-picking it must not turn the config into an explicit clear, so the
    // save is a plain committed success even though verification failed.
    await setFingerprint(renderer, '');
    await save(renderer);

    expect(mocks.updateClaudeConfig).toHaveBeenCalledWith(
      mocks.config.claudeApiKeys[0],
      expect.objectContaining({ fingerprintProfile: undefined })
    );
    expect(notifications('warning')).toHaveLength(0);
    expect(notifications('success')[0][0]).toBe('notification.claude_config_updated');
    expect(mocks.onSaved).toHaveBeenCalledTimes(1);
    expect(mocks.onClose).toHaveBeenCalledTimes(1);
  });
});
