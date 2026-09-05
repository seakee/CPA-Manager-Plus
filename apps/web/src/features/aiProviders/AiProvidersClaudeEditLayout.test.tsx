import type { ReactElement } from 'react';
import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ProviderKeyConfig } from '@/types';

const mocks = vi.hoisted(() => ({
  t: (key: string) => key,
  params: { index: '0' } as Record<string, string | undefined>,
  config: { claudeApiKeys: [] as ProviderKeyConfig[] },
  fetchConfig: vi.fn(),
  updateConfigValue: vi.fn(),
  clearCache: vi.fn(),
  showNotification: vi.fn(),
  allowNextNavigation: vi.fn(),
  navigate: vi.fn(),
  updateClaudeConfig: vi.fn(),
  createClaudeConfig: vi.fn(),
  readBack: vi.fn(),
  outletContext: { current: null as Record<string, unknown> | null },
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: mocks.t }),
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
  useUnsavedChangesGuard: () => ({ allowNextNavigation: mocks.allowNextNavigation }),
}));

vi.mock('@/stores', async () => {
  const draftStore = await import('@/stores/useClaudeEditDraftStore');
  return {
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
    useClaudeEditDraftStore: draftStore.useClaudeEditDraftStore,
  };
});

vi.mock('@/services/api', async () => {
  const actual = await import('@/services/api/providers');
  return {
    providersApi: {
      updateClaudeConfig: mocks.updateClaudeConfig,
      createClaudeConfig: mocks.createClaudeConfig,
    },
    readBackClaudeConfigAfterSave: mocks.readBack,
    verifyClaudeFingerprintInRawConfig: actual.verifyClaudeFingerprintInRawConfig,
  };
});

import { AiProvidersClaudeEditLayout } from './AiProvidersClaudeEditLayout';
import { useClaudeEditDraftStore } from '@/stores/useClaudeEditDraftStore';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type EditorContext = {
  form: { fingerprintProfile?: string; apiKey?: string; baseUrl?: string };
  setForm: (action: (prev: Record<string, unknown>) => Record<string, unknown>) => void;
  handleSave: () => Promise<void>;
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
    renderer = create(createElement(AiProvidersClaudeEditLayout) as ReactElement);
  });
  await flush();
  return renderer!;
};

const patchForm = async (patch: Record<string, unknown>) => {
  const context = mocks.outletContext.current as EditorContext | null;
  if (!context) throw new Error('editor context was not captured');
  await act(async () => {
    context.setForm((prev) => ({ ...prev, ...patch }));
  });
};

const save = async () => {
  const context = mocks.outletContext.current as EditorContext | null;
  if (!context) throw new Error('editor context was not captured');
  await act(async () => {
    await context.handleSave();
  });
};

const notifications = (type: string) =>
  mocks.showNotification.mock.calls.filter((call) => call[1] === type);

describe('AiProvidersClaudeEditLayout fingerprint save verification', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useClaudeEditDraftStore.setState({ drafts: {}, refCounts: {} });
    mocks.params = { index: '0' };
    // /config fixture: persisted facts only — no runtime auth-index.
    mocks.config = {
      claudeApiKeys: [{ apiKey: 'key', baseUrl: 'https://api.anthropic.com', priority: 1 }],
    };
    mocks.fetchConfig.mockResolvedValue(mocks.config.claudeApiKeys);
    mocks.updateClaudeConfig.mockResolvedValue(undefined);
    mocks.createClaudeConfig.mockResolvedValue(undefined);
  });

  it('confirms Default → CLI against the persisted raw /config without any auth-index', async () => {
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

    await renderEditor();
    await patchForm({ fingerprintProfile: 'claude-code-cli' });
    await save();

    expect(mocks.updateClaudeConfig).toHaveBeenCalledWith(
      mocks.config.claudeApiKeys[0],
      expect.objectContaining({ fingerprintProfile: 'claude-code-cli' })
    );
    expect(notifications('error')).toHaveLength(0);
    expect(notifications('warning')).toHaveLength(0);
    expect(notifications('success')[0][0]).toBe('notification.claude_config_updated');
    expect(mocks.navigate).toHaveBeenCalledWith('/ai-providers', { replace: true });
  });

  it('confirms CLI → Default once the persisted raw config no longer exposes a profile', async () => {
    mocks.config = {
      claudeApiKeys: [
        {
          apiKey: 'key',
          baseUrl: 'https://api.anthropic.com',
          fingerprintProfile: 'claude-code-cli',
        },
      ],
    };
    mocks.fetchConfig.mockResolvedValue(mocks.config.claudeApiKeys);
    mocks.readBack.mockResolvedValue({
      claudeApiKeys: [{ apiKey: 'key', baseUrl: 'https://api.anthropic.com' }],
      rawRecords: [{ 'api-key': 'key', 'base-url': 'https://api.anthropic.com' }],
    });

    await renderEditor();
    await patchForm({ fingerprintProfile: '' });
    await save();

    expect(mocks.updateClaudeConfig).toHaveBeenCalledWith(
      mocks.config.claudeApiKeys[0],
      expect.objectContaining({ fingerprintProfile: '' })
    );
    expect(notifications('warning')).toHaveLength(0);
    expect(notifications('success')[0][0]).toBe('notification.claude_config_updated');
  });

  it('does not confirm an explicit Default while an unknown future raw profile survives', async () => {
    mocks.config = {
      claudeApiKeys: [
        {
          apiKey: 'key',
          baseUrl: 'https://api.anthropic.com',
          fingerprintProfile: 'claude-code-cli',
        },
      ],
    };
    mocks.fetchConfig.mockResolvedValue(mocks.config.claudeApiKeys);
    mocks.readBack.mockResolvedValue({
      // normalized read drops the unknown value, the raw read must not
      claudeApiKeys: [{ apiKey: 'key', baseUrl: 'https://api.anthropic.com' }],
      rawRecords: [
        {
          'api-key': 'key',
          'base-url': 'https://api.anthropic.com',
          'fingerprint-profile': 'claude-desktop',
        },
      ],
    });

    await renderEditor();
    await patchForm({ fingerprintProfile: '' });
    await save();

    expect(notifications('success')).toHaveLength(0);
    expect(notifications('warning')).toHaveLength(1);
    expect(notifications('warning')[0][0]).toBe('notification.claude_fingerprint_not_applied');
  });

  it('verifies with the new identity after the API key changes', async () => {
    mocks.readBack.mockResolvedValue({
      claudeApiKeys: [
        {
          apiKey: 'new-key',
          baseUrl: 'https://api.anthropic.com',
          fingerprintProfile: 'claude-code-cli',
        },
      ],
      rawRecords: [
        {
          'api-key': 'new-key',
          'base-url': 'https://api.anthropic.com',
          'fingerprint-profile': 'claude-code-cli',
        },
      ],
    });

    await renderEditor();
    await patchForm({ apiKey: 'new-key', fingerprintProfile: 'claude-code-cli' });
    await save();

    expect(mocks.updateClaudeConfig).toHaveBeenCalledWith(
      mocks.config.claudeApiKeys[0],
      expect.objectContaining({ apiKey: 'new-key', fingerprintProfile: 'claude-code-cli' })
    );
    expect(notifications('warning')).toHaveLength(0);
    expect(notifications('success')).toHaveLength(1);
  });

  it('verifies with the new identity after the base URL changes', async () => {
    mocks.readBack.mockResolvedValue({
      claudeApiKeys: [
        {
          apiKey: 'key',
          baseUrl: 'https://relay.example.com',
          fingerprintProfile: 'claude-code-cli',
        },
      ],
      rawRecords: [
        {
          'api-key': 'key',
          'base-url': 'https://relay.example.com',
          'fingerprint-profile': 'claude-code-cli',
        },
      ],
    });

    await renderEditor();
    await patchForm({
      baseUrl: 'https://relay.example.com',
      fingerprintProfile: 'claude-code-cli',
    });
    await save();

    expect(mocks.updateClaudeConfig).toHaveBeenCalledWith(
      mocks.config.claudeApiKeys[0],
      expect.objectContaining({ baseUrl: 'https://relay.example.com' })
    );
    expect(notifications('warning')).toHaveLength(0);
    expect(notifications('success')).toHaveLength(1);
  });

  it('warns that the save is committed when the connected CPA silently drops the fingerprint', async () => {
    mocks.readBack.mockResolvedValue({
      claudeApiKeys: [{ apiKey: 'key', baseUrl: 'https://api.anthropic.com' }],
      rawRecords: [{ 'api-key': 'key', 'base-url': 'https://api.anthropic.com' }],
    });

    await renderEditor();
    await patchForm({ fingerprintProfile: 'claude-code-cli' });
    await save();

    expect(notifications('success')).toHaveLength(0);
    expect(notifications('warning')).toHaveLength(1);
    expect(notifications('warning')[0][0]).toBe('notification.claude_fingerprint_not_applied');
    expect(mocks.updateConfigValue).toHaveBeenLastCalledWith('claude-api-key', [
      { apiKey: 'key', baseUrl: 'https://api.anthropic.com' },
    ]);
    expect(mocks.navigate).toHaveBeenCalledWith('/ai-providers', { replace: true });
  });

  it('warns that the save is committed when verification cannot read /config', async () => {
    mocks.readBack.mockRejectedValue(new Error('read-back failed'));

    await renderEditor();
    await patchForm({ fingerprintProfile: 'claude-code-cli' });
    await save();

    expect(notifications('success')).toHaveLength(0);
    expect(notifications('warning')).toHaveLength(1);
    expect(notifications('warning')[0][0]).toBe(
      'notification.claude_fingerprint_verify_unavailable'
    );
    expect(mocks.updateConfigValue).not.toHaveBeenCalled();
    expect(mocks.clearCache).toHaveBeenCalledWith('claude-api-key');
    expect(mocks.navigate).toHaveBeenCalledWith('/ai-providers', { replace: true });
  });

  it('keeps the plain success flow when an untouched fingerprint read-back fails', async () => {
    mocks.readBack.mockRejectedValue(new Error('read-back failed'));

    await renderEditor();
    await save();

    expect(notifications('warning')).toHaveLength(0);
    expect(notifications('success')[0][0]).toBe('notification.claude_config_updated');
    expect(mocks.navigate).toHaveBeenCalledWith('/ai-providers', { replace: true });
  });

  it('leaves create mode after a committed create even when verification fails', async () => {
    mocks.params = {};
    mocks.config = { claudeApiKeys: [] };
    mocks.fetchConfig.mockResolvedValue([]);
    mocks.readBack.mockResolvedValue({
      claudeApiKeys: [{ apiKey: 'new-key', baseUrl: 'https://api.anthropic.com' }],
      rawRecords: [{ 'api-key': 'new-key', 'base-url': 'https://api.anthropic.com' }],
    });

    await renderEditor();
    await patchForm({
      apiKey: 'new-key',
      baseUrl: 'https://api.anthropic.com',
      fingerprintProfile: 'claude-code-cli',
    });
    await save();

    // The editor navigates back to the provider list after the committed
    // create, so the create payload cannot be submitted a second time.
    expect(mocks.createClaudeConfig).toHaveBeenCalledTimes(1);
    expect(notifications('success')).toHaveLength(0);
    expect(notifications('warning')[0][0]).toBe('notification.claude_fingerprint_not_applied');
    expect(mocks.navigate).toHaveBeenCalledWith('/ai-providers', { replace: true });
  });

  it('verifies a create against the appended record when a duplicate apiKey + baseUrl exists', async () => {
    mocks.params = {};
    mocks.config = { claudeApiKeys: [] };
    mocks.fetchConfig.mockResolvedValue([]);
    mocks.readBack.mockResolvedValue({
      claudeApiKeys: [
        { apiKey: 'same-key', baseUrl: 'https://relay.example' },
        {
          apiKey: 'same-key',
          baseUrl: 'https://relay.example',
          fingerprintProfile: 'claude-code-cli',
        },
      ],
      rawRecords: [
        {
          'api-key': 'same-key',
          'base-url': 'https://relay.example',
          'proxy-url': 'http://proxy-a',
        },
        {
          'api-key': 'same-key',
          'base-url': 'https://relay.example',
          'proxy-url': 'http://proxy-b',
          'fingerprint-profile': 'claude-code-cli',
        },
      ],
    });

    await renderEditor();
    await patchForm({
      apiKey: 'same-key',
      baseUrl: 'https://relay.example',
      fingerprintProfile: 'claude-code-cli',
    });
    await save();

    // The appended (second) record carries the profile; verifying the first
    // duplicate would report a false not-applied warning.
    expect(mocks.createClaudeConfig).toHaveBeenCalledTimes(1);
    expect(notifications('warning')).toHaveLength(0);
    expect(notifications('success')).toHaveLength(1);
  });
});
