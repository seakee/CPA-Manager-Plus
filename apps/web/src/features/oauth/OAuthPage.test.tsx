import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import type { PluginListEntry } from '@/types';
import { OAuthPage } from './OAuthPage';
import {
  isOAuthPollingScopeCurrent,
  isOAuthProviderAttemptCurrent,
  resolvePluginOAuthProviderId,
  shouldShowPluginOAuthProvider,
} from './oauthProviderHelpers';

const { pageMocks } = vi.hoisted(() => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  return {
    pageMocks: {
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'manager-key',
      supportsPlugin: false,
      location: {
        key: 'nav-1',
        pathname: '/oauth',
        search: '?accountReauth=session-a',
        hash: '',
      },
      startAuth: vi.fn(),
      getAuthStatus: vi.fn(),
      submitCallback: vi.fn(),
      cancelSession: vi.fn(),
      siwcStart: vi.fn(),
      siwcStatus: vi.fn(),
      siwcCallback: vi.fn(),
      siwcCancel: vi.fn(),
      authFilesList: vi.fn(),
      pluginsList: vi.fn(),
      showNotification: vi.fn(),
      completeReauth: vi.fn(),
      publishMutationRevision: vi.fn(),
      recordMutationMarker: vi.fn(),
      navigate: vi.fn(),
      scheduledPolls: [] as Array<() => void>,
    },
  };
});

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key, i18n: { language: 'en' } }),
}));

vi.mock('react-router-dom', () => ({
  useLocation: () => pageMocks.location,
  useNavigate: () => pageMocks.navigate,
}));

vi.mock('@/components/ui/Modal', () => ({
  Modal: ({
    open,
    title,
    children,
    footer,
  }: {
    open: boolean;
    title?: React.ReactNode;
    children?: React.ReactNode;
    footer?: React.ReactNode;
  }) =>
    open ? (
      <div data-testid="modal">
        <div data-testid="modal-title">{title}</div>
        <div>{children}</div>
        <div data-testid="modal-footer">{footer}</div>
      </div>
    ) : null,
}));

vi.mock('@/stores', () => {
  const readAuthState = () => ({
    isAuthenticated: true,
    connectionStatus: 'connected' as const,
    apiBase: pageMocks.apiBase,
    managementKey: pageMocks.managementKey,
    supportsPlugin: pageMocks.supportsPlugin,
  });
  const useAuthStore = Object.assign(
    (selector: (state: ReturnType<typeof readAuthState>) => unknown) => selector(readAuthState()),
    { getState: readAuthState, subscribe: () => () => undefined }
  );
  return {
    publishAccountCredentialMutationRevision: pageMocks.publishMutationRevision,
    useAuthStore,
    useNotificationStore: (
      selector?: (state: { showNotification: typeof pageMocks.showNotification }) => unknown
    ) => {
      const state = { showNotification: pageMocks.showNotification };
      return selector ? selector(state) : state;
    },
    useThemeStore: (selector: (state: { resolvedTheme: 'light' }) => unknown) =>
      selector({ resolvedTheme: 'light' }),
  };
});

vi.mock('@/services/api', () => ({
  oauthApi: {
    startAuth: pageMocks.startAuth,
    getAuthStatus: pageMocks.getAuthStatus,
    submitCallback: pageMocks.submitCallback,
    cancelSession: pageMocks.cancelSession,
  },
  authFilesApi: {
    list: pageMocks.authFilesList,
  },
  pluginsApi: {
    list: pageMocks.pluginsList,
  },
}));

vi.mock('@/services/api/authFiles', () => ({
  authFilesApi: {
    list: pageMocks.authFilesList,
  },
}));

vi.mock('@/services/api/oauth', () => ({
  captureSiwcBindingApi: () => ({
    start: pageMocks.siwcStart,
    getStatus: pageMocks.siwcStatus,
    submitCallback: pageMocks.siwcCallback,
    cancelSession: pageMocks.siwcCancel,
  }),
}));

vi.mock('@/features/accounts/model/accountCredentialMutationMarker', () => ({
  recordAccountCredentialMutationMarker: pageMocks.recordMutationMarker,
}));

vi.mock('@/services/api/vertex', () => ({
  vertexApi: { importCredential: vi.fn() },
}));

vi.mock('@/utils/clipboard', () => ({
  copyToClipboard: vi.fn(async () => true),
}));

vi.mock('@/utils/pluginAssets', () => ({
  getPluginTitle: (plugin: { id: string; metadata?: { name?: string } }) =>
    plugin.metadata?.name?.trim() || plugin.id,
  resolvePluginAssetURL: (url: string) => url,
}));

vi.mock('@/utils/connectionFingerprint', () => ({
  createConnectionFingerprint: (apiBase: string, managementKey: string) =>
    `${apiBase}:${managementKey}`,
}));

vi.mock('@/entities/accounts/reauthSession', () => ({
  completeAccountOAuthReauthSessionFromSearch: pageMocks.completeReauth,
  readAccountOAuthReauthSessionId: (search: string) =>
    new URLSearchParams(search).get('accountReauth'),
}));

const createDeferred = <T,>() => {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve;
    reject = nextReject;
  });
  return { promise, resolve, reject };
};

const readText = (value: unknown): string => {
  if (typeof value === 'string' || typeof value === 'number') return String(value);
  if (Array.isArray(value)) return value.map(readText).join('');
  if (value && typeof value === 'object') {
    if ('children' in value && value.children !== undefined) {
      return readText(value.children);
    }
    if ('props' in value && value.props && typeof value.props === 'object' && 'children' in value.props) {
      return readText(value.props.children);
    }
  }
  return '';
};

const findTileButton = (renderer: ReactTestRenderer, label: string): ReactTestInstance => {
  const button = renderer.root
    .findAllByType('button')
    .find((node) => node.props['aria-label'] === label);
  if (!button) throw new Error(`Provider tile button not found: ${label}`);
  return button;
};

const findButtonByText = (renderer: ReactTestRenderer, text: string): ReactTestInstance => {
  const button = renderer.root
    .findAllByType(Button)
    .find((node) => readText(node.props.children).includes(text));
  if (!button) throw new Error(`Button not found: ${text}`);
  return button;
};

describe('OAuthPage request lifecycle and refactored flows', () => {
  let renderer: ReactTestRenderer | null = null;

  beforeEach(() => {
    pageMocks.apiBase = 'http://cpa-a.local:8317';
    pageMocks.managementKey = 'manager-key';
    pageMocks.supportsPlugin = false;
    pageMocks.location = {
      key: 'nav-1',
      pathname: '/oauth',
      search: '?accountReauth=session-a',
      hash: '',
    };
    pageMocks.scheduledPolls = [];
    pageMocks.startAuth.mockReset();
    pageMocks.getAuthStatus.mockReset();
    pageMocks.submitCallback.mockReset();
    pageMocks.cancelSession.mockReset();
    pageMocks.siwcStart.mockReset();
    pageMocks.siwcStatus.mockReset();
    pageMocks.siwcCallback.mockReset();
    pageMocks.siwcCancel.mockReset();
    pageMocks.recordMutationMarker.mockReset();
    pageMocks.authFilesList.mockReset();
    pageMocks.authFilesList.mockResolvedValue({ files: [] });
    pageMocks.pluginsList.mockReset();
    pageMocks.pluginsList.mockResolvedValue({ plugins: [] });
    pageMocks.showNotification.mockReset();
    pageMocks.completeReauth.mockReset();
    pageMocks.publishMutationRevision.mockReset();
    pageMocks.navigate.mockReset();

    vi.stubGlobal('document', {
      getElementById: vi.fn(() => ({ scrollIntoView: vi.fn() })),
    });
    vi.stubGlobal('window', {
      requestAnimationFrame: (callback: FrameRequestCallback) => {
        callback(0);
        return 1;
      },
      cancelAnimationFrame: vi.fn(),
      setInterval: vi.fn(() => 1),
      clearInterval: vi.fn(),
      setTimeout: vi.fn((callback: () => void, delay?: number) => {
        if (delay === 3000) {
          pageMocks.scheduledPolls.push(callback);
        }
        return pageMocks.scheduledPolls.length + 1;
      }),
      clearTimeout: vi.fn(),
      open: vi.fn(),
    });
  });

  afterEach(async () => {
    if (renderer) {
      await act(async () => renderer?.unmount());
      renderer = null;
    }
    vi.unstubAllGlobals();
  });

  it('discards a late start response and closes the dialog after the connection changes', async () => {
    const deferred = createDeferred<{ url: string; state: string }>();
    pageMocks.startAuth.mockReturnValueOnce(deferred.promise);

    await act(async () => {
      renderer = create(<OAuthPage />);
      await Promise.resolve();
    });

    await act(async () => {
      findTileButton(renderer!, 'Codex').props.onClick();
      await Promise.resolve();
    });
    expect(readText(renderer!.toJSON())).toContain('auth_login.starting_auth');

    pageMocks.apiBase = 'http://cpa-b.local:8317';
    await act(async () => {
      renderer!.update(<OAuthPage />);
      await Promise.resolve();
    });
    expect(readText(renderer!.toJSON())).not.toContain('auth_login.starting_auth');

    await act(async () => {
      deferred.resolve({ url: 'https://oauth.example/old', state: 'old-state' });
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(readText(renderer!.toJSON())).not.toContain('oauth.example');
    expect(pageMocks.getAuthStatus).not.toHaveBeenCalled();
  });

  it('discards a late callback response after the connection changes', async () => {
    const callbackDeferred = createDeferred<void>();
    pageMocks.startAuth.mockResolvedValueOnce({
      url: 'https://oauth.example/current',
      state: 'current-state',
    });
    pageMocks.submitCallback.mockReturnValueOnce(callbackDeferred.promise);

    await act(async () => {
      renderer = create(<OAuthPage />);
      await Promise.resolve();
    });
    await act(async () => {
      findTileButton(renderer!, 'Codex').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    const callbackInput = renderer!.root
      .findAllByType(Input)
      .find((node) => node.props.placeholder === 'auth_login.oauth_callback_placeholder');
    if (!callbackInput) throw new Error('OAuth callback input not found');
    await act(async () => {
      callbackInput.props.onChange({
        target: { value: 'http://127.0.0.1/callback?code=code&state=current-state' },
      });
    });

    const callbackForm = renderer!.root.findByType('form');
    await act(async () => {
      callbackForm.props.onSubmit({ preventDefault: vi.fn() });
      await Promise.resolve();
    });
    expect(pageMocks.submitCallback).toHaveBeenCalledTimes(1);
    expect(findButtonByText(renderer!, 'auth_login.oauth_callback_button').props.loading).toBe(true);

    pageMocks.apiBase = 'http://cpa-b.local:8317';
    await act(async () => {
      renderer!.update(<OAuthPage />);
      await Promise.resolve();
    });
    expect(renderer!.root.findAllByType('form')).toHaveLength(0);

    await act(async () => {
      callbackDeferred.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(pageMocks.showNotification).not.toHaveBeenCalledWith(
      'auth_login.oauth_callback_success',
      'success'
    );
  });

  it('adapts xAI dynamically to device-code flow when CPA returns flow=device and user_code', async () => {
    pageMocks.startAuth.mockResolvedValueOnce({
      url: 'https://accounts.x.ai/device',
      state: 'xai-device-state',
      flow: 'device',
      user_code: 'GROK-9942',
      expires_in: 600,
    });

    await act(async () => {
      renderer = create(<OAuthPage />);
      await Promise.resolve();
    });
    await act(async () => {
      findTileButton(renderer!, 'xAI').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    const content = readText(renderer!.toJSON());
    expect(content).toContain('GROK-9942');
    expect(content).toContain('auth_login.step_enter_device_code');
    expect(renderer!.root.findAllByType('form')).toHaveLength(0);
  });

  it('preserves active URL and state on transient polling network error so retry or callback still works', async () => {
    pageMocks.startAuth.mockResolvedValueOnce({
      url: 'https://auth.openai.com/oauth/authorize?client=1',
      state: 'codex-state-1',
    });
    pageMocks.getAuthStatus.mockRejectedValueOnce(new Error('network timeout'));

    await act(async () => {
      renderer = create(<OAuthPage />);
      await Promise.resolve();
    });
    await act(async () => {
      findTileButton(renderer!, 'Codex').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(pageMocks.scheduledPolls).toHaveLength(1);
    await act(async () => {
      pageMocks.scheduledPolls[0]();
      await Promise.resolve();
      await Promise.resolve();
    });

    const content = readText(renderer!.toJSON());
    expect(content).toContain('network timeout');
    expect(content).toContain('auth.openai.com');
    expect(content).toContain('auth_login.retry_status_check');
    expect(renderer!.root.findAllByType('form')).toHaveLength(1);
  });

  it('cancels an active session cleanly and tolerates 404 on older CPA servers', async () => {
    pageMocks.startAuth.mockResolvedValueOnce({
      url: 'https://auth.openai.com/oauth/authorize',
      state: 'codex-cancel-state',
    });
    pageMocks.cancelSession.mockRejectedValueOnce(
      Object.assign(new Error('Not Found'), { status: 404 })
    );

    await act(async () => {
      renderer = create(<OAuthPage />);
      await Promise.resolve();
    });
    await act(async () => {
      findTileButton(renderer!, 'Codex').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    await act(async () => {
      findButtonByText(renderer!, 'auth_login.cancel_auth').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(pageMocks.cancelSession).toHaveBeenCalledWith(
      'codex-cancel-state',
      { apiBase: 'http://cpa-a.local:8317', managementKey: 'manager-key' },
      expect.any(AbortSignal)
    );
    expect(pageMocks.showNotification).toHaveBeenCalledWith(
      'auth_login.oauth_session_cancelled',
      'info'
    );
  });

  it('filters disabled plugins, expands multi-entry plugins, and keeps them across same-connection accountReauth changes', async () => {
    pageMocks.supportsPlugin = true;
    const makePlugin = (overrides: Partial<PluginListEntry> & { id: string }): PluginListEntry => ({
      path: `/plugins/${overrides.id}`,
      configured: true,
      registered: true,
      enabled: true,
      effectiveEnabled: true,
      supportsOAuth: true,
      logo: '',
      configFields: [],
      menus: [],
      metadata: null,
      ...overrides,
    });
    const plugins: PluginListEntry[] = [
      makePlugin({
        id: 'disabled-plugin',
        oauthProvider: 'disabled-provider',
        enabled: false,
        effectiveEnabled: false,
      }),
      makePlugin({
        id: 'multi-plugin',
        oauthProvider: 'alpha',
        oauthProviders: ['alpha', 'beta'],
        oauthProviderEntries: [
          { identifier: 'alpha', name: 'Alpha OAuth' },
          { identifier: 'beta', name: 'Beta OAuth' },
        ],
      }),
    ];
    pageMocks.pluginsList.mockResolvedValue({ plugins });
    pageMocks.location = {
      key: 'nav-reauth',
      pathname: '/oauth',
      search: '?accountReauth=session-1',
      hash: '#oauth-provider-codex',
    };
    pageMocks.startAuth.mockResolvedValue({
      url: 'https://auth.openai.com/oauth/authorize',
      state: 'reauth-state',
    });

    await act(async () => {
      renderer = create(<OAuthPage />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(readText(renderer!.toJSON())).toContain('Alpha OAuth');
    expect(readText(renderer!.toJSON())).toContain('Beta OAuth');
    expect(readText(renderer!.toJSON())).not.toContain('disabled-plugin');
    expect(readText(renderer!.toJSON())).toContain('auth_login.reauth_banner_message');

    // Changing accountReauth on the same connection must NOT wipe loaded plugins.
    pageMocks.location = {
      key: 'nav-reauth-2',
      pathname: '/oauth',
      search: '?accountReauth=session-2',
      hash: '',
    };
    await act(async () => {
      renderer!.update(<OAuthPage />);
      await Promise.resolve();
    });

    expect(readText(renderer!.toJSON())).toContain('Alpha OAuth');
    expect(readText(renderer!.toJSON())).toContain('Beta OAuth');
  });

  it('highlights #oauth-provider-siwc, opens SiwcBindDialog on tile click, increments session addedCount on completion, and navigates to accounts', async () => {
    pageMocks.location = {
      key: 'nav-siwc',
      pathname: '/oauth',
      search: '',
      hash: '#oauth-provider-siwc',
    };
    pageMocks.authFilesList.mockResolvedValue({
      files: [
        {
          name: 'codex-main.json',
          provider: 'codex',
          siwc_bind_supported: true,
          siwc_bound: false,
        },
      ],
    });
    pageMocks.siwcStart.mockResolvedValue({
      status: 'ok',
      flow: 'siwc_bind',
      url: 'https://auth.openai.com/oauth/authorize?state=siwc-st-1',
      state: 'siwc-st-1',
      expires_in: 300,
    });
    pageMocks.siwcStatus.mockResolvedValue({ status: 'ok' });

    await act(async () => {
      renderer = create(<OAuthPage />);
      await Promise.resolve();
    });

    expect(document.getElementById).toHaveBeenCalledWith('oauth-provider-siwc');

    await act(async () => {
      findTileButton(renderer!, 'siwc.title').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(readText(renderer!.toJSON())).toContain('codex-main.json');
    expect(readText(renderer!.toJSON())).not.toContain('auth_login.added_this_session');

    await act(async () => {
      findButtonByText(renderer!, 'siwc.start').props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(pageMocks.siwcStart).toHaveBeenCalledWith('codex-main.json');
    expect(pageMocks.scheduledPolls.length).toBeGreaterThan(0);

    await act(async () => {
      pageMocks.scheduledPolls[pageMocks.scheduledPolls.length - 1]();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(readText(renderer!.toJSON())).toContain('siwc.ok');
    expect(readText(renderer!.toJSON())).toContain('+1 auth_login.added_this_session');

    const modalViewBtn = renderer!.root
      .findAllByType(Button)
      .filter((node) => readText(node.props.children).includes('auth_login.view_credentials'))
      .pop();
    expect(modalViewBtn).toBeDefined();

    await act(async () => {
      modalViewBtn!.props.onClick();
      await Promise.resolve();
    });

    expect(pageMocks.navigate).toHaveBeenCalledWith('/accounts');
  });
});

const builtInProviderIds = new Set(['codex', 'anthropic', 'antigravity', 'kimi', 'xai']);

describe('plugin OAuth provider helpers', () => {
  it('uses explicit plugin OAuth provider ids when present', () => {
    expect(resolvePluginOAuthProviderId({ id: 'legacy-plugin', oauthProvider: 'custom' })).toBe(
      'custom'
    );
    expect(resolvePluginOAuthProviderId({ id: 'legacy-plugin' })).toBe('legacy-plugin');
  });

  it('hides disabled or built-in colliding plugin OAuth entries', () => {
    expect(
      shouldShowPluginOAuthProvider(
        {
          id: 'custom-plugin',
          oauthProvider: 'codex',
          supportsOAuth: true,
        },
        builtInProviderIds
      )
    ).toBe(false);
    expect(
      shouldShowPluginOAuthProvider(
        {
          id: 'disabled-plugin',
          oauthProvider: 'custom-provider',
          supportsOAuth: true,
          effectiveEnabled: false,
        },
        builtInProviderIds
      )
    ).toBe(false);
    expect(
      shouldShowPluginOAuthProvider(
        {
          id: 'custom-plugin',
          oauthProvider: 'custom-provider',
          supportsOAuth: true,
          effectiveEnabled: true,
        },
        builtInProviderIds
      )
    ).toBe(true);
  });

  it('invalidates an OAuth polling attempt after its connection or reauth session changes', () => {
    const started = {
      connectionFingerprint: 'connection-a',
      accountReauthSessionId: 'session-a',
      search: '?accountReauth=session-a',
    };
    expect(
      isOAuthPollingScopeCurrent(started, {
        connectionFingerprint: 'connection-a',
        accountReauthSessionId: 'session-a',
        search: '?accountReauth=session-a',
      })
    ).toBe(true);
    expect(
      isOAuthPollingScopeCurrent(started, {
        connectionFingerprint: 'connection-b',
        accountReauthSessionId: 'session-a',
        search: '?accountReauth=session-a',
      })
    ).toBe(false);
    expect(
      isOAuthPollingScopeCurrent(started, {
        connectionFingerprint: 'connection-a',
        accountReauthSessionId: 'session-b',
        search: '?accountReauth=session-b',
      })
    ).toBe(false);
    expect(isOAuthProviderAttemptCurrent({ scope: started, version: 2 }, started, 2)).toBe(true);
    expect(isOAuthProviderAttemptCurrent({ scope: started, version: 2 }, started, 3)).toBe(false);
  });
});
