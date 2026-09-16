import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { OAuthPage } from './OAuthPage';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const { mocks } = vi.hoisted(() => ({
  mocks: {
    apiBase: 'http://cpa-a.local:8317',
    managementKey: 'key-a',
    startAuth: vi.fn(),
    getAuthStatus: vi.fn(),
    submitCallback: vi.fn(),
    cancelSession: vi.fn(),
    authFilesList: vi.fn(async () => ({ files: [] })),
    pluginList: vi.fn(async () => ({ plugins: [] })),
    vertexImport: vi.fn(),
    showNotification: vi.fn(),
    navigate: vi.fn(),
    recordMutationMarker: vi.fn(),
    publishMutationRevision: vi.fn(),
    intervalCallbacks: [] as Array<() => void | Promise<void>>,
  },
}));

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => undefined },
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock('react-router-dom', () => ({
  useLocation: () => ({ hash: '' }),
  useNavigate: () => mocks.navigate,
}));

vi.mock('@/stores', () => {
  const readAuthState = () => ({
    apiBase: mocks.apiBase,
    managementKey: mocks.managementKey,
    connectionStatus: 'connected' as const,
    supportsPlugin: false,
  });
  return {
    publishAccountCredentialMutationRevision: mocks.publishMutationRevision,
    useAuthStore: Object.assign(
      (selector: (state: Record<string, unknown>) => unknown) => selector(readAuthState()),
      { getState: readAuthState }
    ),
    useNotificationStore: (
      selector?: (state: { showNotification: typeof mocks.showNotification }) => unknown
    ) => {
      const state = { showNotification: mocks.showNotification };
      return selector ? selector(state) : state;
    },
    useThemeStore: (selector: (state: { resolvedTheme: 'light' }) => unknown) =>
      selector({ resolvedTheme: 'light' }),
  };
});

vi.mock('@/services/api', () => ({
  oauthApi: {
    startAuth: mocks.startAuth,
    getAuthStatus: mocks.getAuthStatus,
    submitCallback: mocks.submitCallback,
    cancelSession: mocks.cancelSession,
  },
  authFilesApi: {
    list: mocks.authFilesList,
  },
  pluginsApi: {
    list: mocks.pluginList,
  },
}));

vi.mock('@/services/api/vertex', () => ({
  vertexApi: {
    importCredential: mocks.vertexImport,
  },
}));

vi.mock('@/features/monitoring/codexInspection', () => ({
  createCodexInspectionConnectionFingerprint: (apiBase: string, managementKey: string) =>
    apiBase && managementKey ? `${apiBase}:${managementKey}` : null,
}));

vi.mock('@/features/accounts/model/accountCredentialMutationMarker', () => ({
  createAccountCredentialMutationBaseline: (_files: unknown[], provider: string) => ({
    provider,
    credentials: [],
  }),
  recordAccountCredentialMutationMarker: mocks.recordMutationMarker,
}));

vi.mock('@/utils/clipboard', () => ({
  copyToClipboard: vi.fn(async () => true),
}));

vi.mock('@/features/plugins/pluginResources', () => ({
  getPluginTitle: (plugin: { id: string }) => plugin.id,
  resolvePluginAssetURL: () => '',
}));

const textContent = (node: ReactTestInstance): string =>
  node.children.map((child) => (typeof child === 'string' ? child : textContent(child))).join('');

const treeText = (renderer: ReactTestRenderer): string =>
  renderer.root.children
    .map((child) => (typeof child === 'string' ? child : textContent(child)))
    .join('');

const getDevinCard = (renderer: ReactTestRenderer): ReactTestInstance => {
  const card = renderer.root.find((node) => node.props?.id === 'oauth-provider-devin');
  if (!card) throw new Error('Devin provider card not found');
  return card;
};

const findDevinButton = (renderer: ReactTestRenderer, text: string): ReactTestInstance => {
  const card = getDevinCard(renderer);
  const button = card
    .findAllByType('button')
    .find((candidate) => textContent(candidate) === text);
  if (!button) throw new Error(`Button not found in Devin card: ${text}`);
  return button;
};

const queryDevinButton = (renderer: ReactTestRenderer, text: string): ReactTestInstance | undefined => {
  const card = getDevinCard(renderer);
  return card
    .findAllByType('button')
    .find((candidate) => textContent(candidate) === text);
};

const mountedRenderers = new Set<ReactTestRenderer>();

const renderOAuthPage = async (): Promise<ReactTestRenderer> => {
  let renderer!: ReactTestRenderer;
  await act(async () => {
    renderer = create(<OAuthPage />);
    await Promise.resolve();
  });
  mountedRenderers.add(renderer);
  return renderer;
};

const findDevinLoginButton = (renderer: ReactTestRenderer): ReactTestInstance => {
  const button =
    queryDevinButton(renderer, 'auth_login.devin_oauth_button') ||
    queryDevinButton(renderer, 'auth_login.login_another_account');
  if (!button) throw new Error('Devin login button not found');
  return button;
};

const startDevinAuth = (renderer: ReactTestRenderer): Promise<void> => {
  const button = findDevinLoginButton(renderer);
  return Promise.resolve(button.props.onClick());
};

const submitDevinCallback = async (renderer: ReactTestRenderer, callbackUrl: string) => {
  const card = getDevinCard(renderer);
  const callbackInput = card
    .findAllByType('input')
    .find((input) => input.props.placeholder === 'auth_login.devin_callback_placeholder');
  if (!callbackInput) throw new Error('Devin callback input not found');

  await act(async () => {
    callbackInput.props.onChange({ target: { value: callbackUrl } });
    await Promise.resolve();
  });

  let callbackPromise!: Promise<void>;
  act(() => {
    callbackPromise = Promise.resolve(
      findDevinButton(renderer, 'auth_login.oauth_callback_button').props.onClick()
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
  return callbackPromise;
};

const cancelDevinAuth = (renderer: ReactTestRenderer): Promise<void> => {
  const button = findDevinButton(renderer, 'auth_login.devin_oauth_cancel');
  return Promise.resolve(button.props.onClick());
};

describe('OAuthPage Devin OAuth lifecycle', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.apiBase = 'http://cpa-a.local:8317';
    mocks.managementKey = 'key-a';
    mocks.intervalCallbacks = [];
    mocks.authFilesList.mockReset();
    mocks.authFilesList.mockResolvedValue({ files: [] });
    vi.stubGlobal('window', {
      requestAnimationFrame: vi.fn(() => 1),
      cancelAnimationFrame: vi.fn(),
      setInterval: vi.fn((callback: () => void | Promise<void>) => {
        mocks.intervalCallbacks.push(callback);
        return mocks.intervalCallbacks.length;
      }),
      clearInterval: vi.fn(),
      setTimeout: vi.fn(() => 100),
      clearTimeout: vi.fn(),
      open: vi.fn(),
    });
  });

  afterEach(async () => {
    await act(async () => {
      mountedRenderers.forEach((renderer) => renderer.unmount());
    });
    mountedRenderers.clear();
    vi.unstubAllGlobals();
  });

  it('renders Devin card, starts auth, and begins polling', async () => {
    mocks.startAuth.mockResolvedValue({
      url: 'https://auth.example/devin?state=devin-state-1',
      state: 'devin-state-1',
    });
    mocks.getAuthStatus.mockResolvedValue({ status: 'wait' });

    const renderer = await renderOAuthPage();
    await act(async () => {
      await startDevinAuth(renderer);
    });

    expect(mocks.startAuth).toHaveBeenCalledWith('devin', {
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'key-a',
    });

    expect(treeText(renderer)).toContain('https://auth.example/devin?state=devin-state-1');
    expect(treeText(renderer)).toContain('auth_login.devin_oauth_status_waiting');
    expect(queryDevinButton(renderer, 'auth_login.devin_oauth_cancel')).toBeDefined();

    // Verify polling callback is registered and triggers getAuthStatus
    expect(mocks.intervalCallbacks).toHaveLength(1);
    await act(async () => {
      await mocks.intervalCallbacks[0]?.();
    });
    expect(mocks.getAuthStatus).toHaveBeenCalledWith('devin-state-1', {
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'key-a',
    });
  });

  it('prevents duplicate auth start when Devin session is already pending', async () => {
    mocks.startAuth.mockResolvedValue({
      url: 'https://auth.example/devin?state=devin-state-1',
      state: 'devin-state-1',
    });

    const renderer = await renderOAuthPage();
    await act(async () => {
      await startDevinAuth(renderer);
    });
    expect(mocks.startAuth).toHaveBeenCalledTimes(1);

    const loginButton = findDevinButton(renderer, 'auth_login.devin_oauth_button');
    expect(loginButton.props.disabled).toBe(true);

    // Clicking again while pending state exists should not trigger another startAuth
    await act(async () => {
      await loginButton.props.onClick();
    });
    expect(mocks.startAuth).toHaveBeenCalledTimes(1);
  });

  it('submits valid Devin callback URL untouched and records mutation marker on completion', async () => {
    mocks.startAuth.mockResolvedValue({
      url: 'https://auth.example/devin?state=devin-state-1',
      state: 'devin-state-1',
    });
    mocks.submitCallback.mockResolvedValue({ status: 'ok' });
    mocks.getAuthStatus.mockResolvedValue({ status: 'ok' });

    const renderer = await renderOAuthPage();
    await act(async () => {
      await startDevinAuth(renderer);
    });

    const fullCallbackUrl = 'http://127.0.0.1:8317/callback?code=devin_code_xyz&state=devin-state-1';
    const callbackPromise = await submitDevinCallback(renderer, fullCallbackUrl);
    await act(async () => {
      await callbackPromise;
    });

    // submitCallback must receive the complete URL with host, port, path, and parameters intact
    expect(mocks.submitCallback).toHaveBeenCalledWith(
      'devin',
      fullCallbackUrl,
      {
        apiBase: 'http://cpa-a.local:8317',
        managementKey: 'key-a',
      }
    );

    expect(mocks.recordMutationMarker).toHaveBeenCalledWith({
      connectionFingerprint: 'http://cpa-a.local:8317:key-a',
      provider: 'devin',
      baseline: { provider: 'devin', credentials: [] },
      requireObservedMutation: true,
    });
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_login.oauth_callback_success',
      'success'
    );
  });

  it('blocks submission and shows warning when callback state does not match expected state', async () => {
    mocks.startAuth.mockResolvedValue({
      url: 'https://auth.example/devin?state=devin-state-1',
      state: 'devin-state-1',
    });

    const renderer = await renderOAuthPage();
    await act(async () => {
      await startDevinAuth(renderer);
    });

    const mismatchedUrl = 'http://127.0.0.1:8317/callback?code=devin_code_xyz&state=wrong-state';
    await submitDevinCallback(renderer, mismatchedUrl);

    expect(mocks.submitCallback).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_login.devin_callback_state_mismatch',
      'warning'
    );

    // Input retains user value
    const input = renderer.root
      .findAllByType('input')
      .find((candidate) => candidate.props.placeholder === 'auth_login.devin_callback_placeholder');
    expect(input?.props.value).toBe(mismatchedUrl);
  });

  it('blocks submission and shows warning when callback URL is malformed or missing code', async () => {
    mocks.startAuth.mockResolvedValue({
      url: 'https://auth.example/devin?state=devin-state-1',
      state: 'devin-state-1',
    });

    const renderer = await renderOAuthPage();
    await act(async () => {
      await startDevinAuth(renderer);
    });

    await submitDevinCallback(renderer, 'not-a-valid-url');

    expect(mocks.submitCallback).not.toHaveBeenCalled();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_login.devin_callback_invalid',
      'warning'
    );
  });

  it('cancels pending Devin auth session successfully when cancelled: true', async () => {
    mocks.startAuth.mockResolvedValue({
      url: 'https://auth.example/devin?state=devin-state-1',
      state: 'devin-state-1',
    });
    mocks.cancelSession.mockResolvedValue({ status: 'ok', cancelled: true });

    const renderer = await renderOAuthPage();
    await act(async () => {
      await startDevinAuth(renderer);
    });

    expect(queryDevinButton(renderer, 'auth_login.devin_oauth_cancel')).toBeDefined();

    await act(async () => {
      await cancelDevinAuth(renderer);
    });

    expect(mocks.cancelSession).toHaveBeenCalledWith('devin-state-1', {
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'key-a',
    });

    // Session reset: cancel button disappears, URL box removed, start button re-enabled
    expect(queryDevinButton(renderer, 'auth_login.devin_oauth_cancel')).toBeUndefined();
    expect(treeText(renderer)).not.toContain('https://auth.example/devin');
    const startButton = findDevinButton(renderer, 'auth_login.devin_oauth_button');
    expect(startButton.props.disabled).toBeFalsy();
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_login.devin_oauth_cancelled',
      'success'
    );
  });

  it('handles cancel race condition when cancelled: false and session already completed', async () => {
    mocks.startAuth.mockResolvedValue({
      url: 'https://auth.example/devin?state=devin-state-1',
      state: 'devin-state-1',
    });
    mocks.cancelSession.mockResolvedValue({ status: 'ok', cancelled: false });
    mocks.getAuthStatus.mockResolvedValue({ status: 'ok' });

    const renderer = await renderOAuthPage();
    await act(async () => {
      await startDevinAuth(renderer);
    });

    await act(async () => {
      await cancelDevinAuth(renderer);
    });

    expect(mocks.cancelSession).toHaveBeenCalledWith('devin-state-1', {
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'key-a',
    });
    expect(mocks.getAuthStatus).toHaveBeenCalledWith('devin-state-1', {
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'key-a',
    });
    expect(mocks.recordMutationMarker).toHaveBeenCalledWith({
      connectionFingerprint: 'http://cpa-a.local:8317:key-a',
      provider: 'devin',
      baseline: { provider: 'devin', credentials: [] },
      requireObservedMutation: true,
    });
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_login.devin_oauth_status_success',
      'success'
    );
  });

  it('handles cancel failure: preserves state, shows cancel error, and resumes polling', async () => {
    mocks.startAuth.mockResolvedValue({
      url: 'https://auth.example/devin?state=devin-state-1',
      state: 'devin-state-1',
    });
    mocks.cancelSession.mockRejectedValue(new Error('Network error on cancel'));

    const renderer = await renderOAuthPage();
    await act(async () => {
      await startDevinAuth(renderer);
    });

    await act(async () => {
      await cancelDevinAuth(renderer);
    });

    expect(mocks.showNotification).toHaveBeenCalledWith(
      'Network error on cancel',
      'error'
    );
    expect(treeText(renderer)).toContain('Network error on cancel');
    // State and Cancel button preserved
    expect(queryDevinButton(renderer, 'auth_login.devin_oauth_cancel')).toBeDefined();
    expect(treeText(renderer)).toContain('https://auth.example/devin');
  });

  it('clears cancelError when attempt finishes successfully and does not leak to a new Devin login', async () => {
    mocks.startAuth
      .mockResolvedValueOnce({
        url: 'https://auth.example/devin?state=devin-state-1',
        state: 'devin-state-1',
      })
      .mockResolvedValueOnce({
        url: 'https://auth.example/devin?state=devin-state-2',
        state: 'devin-state-2',
      });
    mocks.cancelSession.mockRejectedValueOnce(new Error('Network error on cancel'));
    mocks.getAuthStatus.mockResolvedValueOnce({ status: 'ok' });

    const renderer = await renderOAuthPage();
    await act(async () => {
      await startDevinAuth(renderer);
    });

    // Cancel fails, preserves pending state and shows cancelError
    await act(async () => {
      await cancelDevinAuth(renderer);
    });
    expect(treeText(renderer)).toContain('Network error on cancel');
    expect(queryDevinButton(renderer, 'auth_login.devin_oauth_cancel')).toBeDefined();

    // Subsequent polling succeeds and finishes the attempt
    const latestPollingCallback = mocks.intervalCallbacks[mocks.intervalCallbacks.length - 1];
    await act(async () => {
      await latestPollingCallback?.();
    });

    // Attempt completes successfully: cancelError cleared
    expect(treeText(renderer)).not.toContain('Network error on cancel');

    // Start a new Devin login attempt
    await act(async () => {
      await startDevinAuth(renderer);
    });

    // Old cancel error must not leak into the new login attempt
    expect(treeText(renderer)).not.toContain('Network error on cancel');
    expect(treeText(renderer)).toContain('https://auth.example/devin?state=devin-state-2');
  });

  it('clears cancelError when CPA returns error status and does not leak to a new Devin login', async () => {
    mocks.startAuth
      .mockResolvedValueOnce({
        url: 'https://auth.example/devin?state=devin-state-1',
        state: 'devin-state-1',
      })
      .mockResolvedValueOnce({
        url: 'https://auth.example/devin?state=devin-state-2',
        state: 'devin-state-2',
      });
    mocks.cancelSession.mockRejectedValueOnce(new Error('Network error on cancel'));
    mocks.getAuthStatus.mockResolvedValueOnce({ status: 'error', error: 'session expired' });

    const renderer = await renderOAuthPage();
    await act(async () => {
      await startDevinAuth(renderer);
    });

    // Cancel fails, cancelError is displayed
    await act(async () => {
      await cancelDevinAuth(renderer);
    });
    expect(treeText(renderer)).toContain('Network error on cancel');

    // Polling receives CPA error status
    const latestPollingCallback = mocks.intervalCallbacks[mocks.intervalCallbacks.length - 1];
    await act(async () => {
      await latestPollingCallback?.();
    });

    // Error status clears cancelError and resets session
    expect(treeText(renderer)).not.toContain('Network error on cancel');
    expect(treeText(renderer)).toContain('session expired');

    // Start a new Devin login attempt
    await act(async () => {
      await startDevinAuth(renderer);
    });

    // Old cancel error must not leak into the new login attempt
    expect(treeText(renderer)).not.toContain('Network error on cancel');
    expect(treeText(renderer)).toContain('https://auth.example/devin?state=devin-state-2');
  });

  it('preserves state and leaves cancel button visible when polling encounters network error', async () => {
    mocks.startAuth.mockResolvedValue({
      url: 'https://auth.example/devin?state=devin-state-1',
      state: 'devin-state-1',
    });
    mocks.getAuthStatus.mockRejectedValue(new Error('Connection reset'));

    const renderer = await renderOAuthPage();
    await act(async () => {
      await startDevinAuth(renderer);
    });

    // Fire polling interval
    await act(async () => {
      await mocks.intervalCallbacks[0]?.();
    });

    // Error status shown, but state and cancel button remain accessible
    expect(treeText(renderer)).toContain('auth_login.devin_oauth_status_error');
    expect(queryDevinButton(renderer, 'auth_login.devin_oauth_cancel')).toBeDefined();
    expect(treeText(renderer)).toContain('https://auth.example/devin');
  });

  it('resets Devin state back to idle when CPA explicitly returns error status', async () => {
    mocks.startAuth.mockResolvedValue({
      url: 'https://auth.example/devin?state=devin-state-1',
      state: 'devin-state-1',
    });
    mocks.getAuthStatus.mockResolvedValue({ status: 'error', error: 'session expired' });

    const renderer = await renderOAuthPage();
    await act(async () => {
      await startDevinAuth(renderer);
    });

    // Fire polling interval
    await act(async () => {
      await mocks.intervalCallbacks[0]?.();
    });

    // Error status handled: state cleared, cancel button gone, start button re-enabled
    expect(queryDevinButton(renderer, 'auth_login.devin_oauth_cancel')).toBeUndefined();
    expect(treeText(renderer)).not.toContain('https://auth.example/devin');
    const startButton = findDevinButton(renderer, 'auth_login.devin_oauth_button');
    expect(startButton.props.disabled).toBeFalsy();
  });
});
