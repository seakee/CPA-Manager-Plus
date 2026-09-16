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

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((nextResolve, nextReject) => {
    resolve = nextResolve;
    reject = nextReject;
  });
  return { promise, reject, resolve };
};

const textContent = (node: ReactTestInstance): string =>
  node.children.map((child) => (typeof child === 'string' ? child : textContent(child))).join('');

const treeText = (renderer: ReactTestRenderer): string =>
  renderer.root.children
    .map((child) => (typeof child === 'string' ? child : textContent(child)))
    .join('');

const findButton = (renderer: ReactTestRenderer, text: string): ReactTestInstance => {
  const button = renderer.root
    .findAllByType('button')
    .find((candidate) => textContent(candidate) === text);
  if (!button) throw new Error(`Button not found: ${text}`);
  return button;
};

const renderOAuthPage = async (): Promise<ReactTestRenderer> => {
  let renderer!: ReactTestRenderer;
  await act(async () => {
    renderer = create(<OAuthPage />);
    await Promise.resolve();
  });
  mountedRenderers.add(renderer);
  return renderer;
};

const mountedRenderers = new Set<ReactTestRenderer>();

const startCodexAuth = (renderer: ReactTestRenderer): Promise<void> => {
  let promise!: Promise<void>;
  act(() => {
    promise = Promise.resolve(
      findButton(renderer, 'auth_login.codex_oauth_button').props.onClick()
    );
  });
  return promise;
};

const startDevinAuth = (renderer: ReactTestRenderer): Promise<void> => {
  let promise!: Promise<void>;
  act(() => {
    promise = Promise.resolve(
      findButton(renderer, 'auth_login.devin_oauth_button').props.onClick()
    );
  });
  return promise;
};

describe('OAuthPage connection lifecycle', () => {
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

  it('ignores a late auth-link response after the CPA connection changes', async () => {
    const oldRequest = deferred<{ url: string; state: string }>();
    const newRequest = deferred<{ url: string; state: string }>();
    mocks.startAuth.mockImplementation((_provider: string, scope: { apiBase: string }) =>
      scope.apiBase === 'http://cpa-a.local:8317' ? oldRequest.promise : newRequest.promise
    );
    const renderer = await renderOAuthPage();

    const oldAuthPromise = startCodexAuth(renderer);
    await act(async () => {
      await Promise.resolve();
    });
    expect(mocks.startAuth).toHaveBeenCalledWith('codex', {
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'key-a',
    });

    mocks.apiBase = 'http://cpa-b.local:8317';
    mocks.managementKey = 'key-b';
    await act(async () => {
      renderer.update(<OAuthPage />);
    });
    const newAuthPromise = startCodexAuth(renderer);
    await act(async () => {
      await Promise.resolve();
    });

    await act(async () => {
      oldRequest.resolve({ url: 'https://auth.example/old', state: 'old-state' });
      await oldAuthPromise;
    });
    expect(treeText(renderer)).not.toContain('https://auth.example/old');
    expect(mocks.intervalCallbacks).toHaveLength(0);

    await act(async () => {
      newRequest.resolve({ url: 'https://auth.example/new', state: 'new-state' });
      await newAuthPromise;
    });
    expect(treeText(renderer)).toContain('https://auth.example/new');
    expect(mocks.intervalCallbacks).toHaveLength(1);
  });

  it('ignores a late polling success after the CPA connection changes', async () => {
    const polling = deferred<{ status: 'ok' }>();
    mocks.startAuth.mockResolvedValue({ url: 'https://auth.example/codex', state: 'state-a' });
    mocks.getAuthStatus.mockReturnValue(polling.promise);
    const renderer = await renderOAuthPage();
    const authPromise = startCodexAuth(renderer);
    await act(async () => {
      await authPromise;
    });

    let pollingPromise!: Promise<void>;
    await act(async () => {
      pollingPromise = Promise.resolve(mocks.intervalCallbacks[0]?.());
      await Promise.resolve();
    });
    expect(mocks.getAuthStatus).toHaveBeenCalledWith('state-a', {
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'key-a',
    });

    mocks.apiBase = 'http://cpa-b.local:8317';
    mocks.managementKey = 'key-b';
    await act(async () => {
      renderer.update(<OAuthPage />);
      polling.resolve({ status: 'ok' });
      await pollingPromise;
    });

    expect(mocks.recordMutationMarker).not.toHaveBeenCalled();
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'auth_login.codex_oauth_status_success',
      'success'
    );
  });

  it('ignores a late callback response after the CPA connection changes', async () => {
    const callback = deferred<{ status: 'ok' }>();
    mocks.startAuth.mockResolvedValue({ url: 'https://auth.example/codex', state: 'state-a' });
    mocks.submitCallback.mockReturnValue(callback.promise);
    const renderer = await renderOAuthPage();
    const authPromise = startCodexAuth(renderer);
    await act(async () => {
      await authPromise;
    });

    const callbackInput = renderer.root
      .findAllByType('input')
      .find((input) => input.props.placeholder === 'auth_login.oauth_callback_placeholder');
    if (!callbackInput) throw new Error('Callback input not found');
    act(() => callbackInput.props.onChange({ target: { value: 'http://callback?code=1' } }));
    let callbackPromise!: Promise<void>;
    await act(async () => {
      callbackPromise = Promise.resolve(
        findButton(renderer, 'auth_login.oauth_callback_button').props.onClick()
      );
      await Promise.resolve();
    });
    expect(mocks.submitCallback).toHaveBeenCalledWith('codex', 'http://callback?code=1', {
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'key-a',
    });

    mocks.apiBase = 'http://cpa-b.local:8317';
    mocks.managementKey = 'key-b';
    await act(async () => {
      renderer.update(<OAuthPage />);
      callback.resolve({ status: 'ok' });
      await callbackPromise;
    });

    expect(mocks.recordMutationMarker).not.toHaveBeenCalled();
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'auth_login.oauth_callback_success',
      'success'
    );
  });

  it('records one scoped mutation marker when callback success races with polling success', async () => {
    const polling = deferred<{ status: 'ok' }>();
    const callbackStatus = deferred<{ status: 'ok' }>();
    const callback = deferred<{ status: 'ok' }>();
    mocks.startAuth.mockResolvedValue({ url: 'https://auth.example/codex', state: 'state-a' });
    mocks.getAuthStatus
      .mockReturnValueOnce(polling.promise)
      .mockReturnValueOnce(callbackStatus.promise);
    mocks.submitCallback.mockReturnValue(callback.promise);
    const renderer = await renderOAuthPage();
    const authPromise = startCodexAuth(renderer);
    await act(async () => {
      await authPromise;
    });

    let pollingPromise!: Promise<void>;
    await act(async () => {
      pollingPromise = Promise.resolve(mocks.intervalCallbacks[0]?.());
      await Promise.resolve();
    });
    const callbackInput = renderer.root
      .findAllByType('input')
      .find((input) => input.props.placeholder === 'auth_login.oauth_callback_placeholder');
    if (!callbackInput) throw new Error('Callback input not found');
    act(() => callbackInput.props.onChange({ target: { value: 'http://callback?code=1' } }));
    let callbackPromise!: Promise<void>;
    await act(async () => {
      callbackPromise = Promise.resolve(
        findButton(renderer, 'auth_login.oauth_callback_button').props.onClick()
      );
      await Promise.resolve();
    });

    await act(async () => {
      callback.resolve({ status: 'ok' });
      await Promise.resolve();
    });
    expect(mocks.recordMutationMarker).not.toHaveBeenCalled();
    await act(async () => {
      callbackStatus.resolve({ status: 'ok' });
      await callbackPromise;
    });
    expect(mocks.recordMutationMarker).toHaveBeenCalledTimes(1);
    expect(mocks.recordMutationMarker).toHaveBeenCalledWith({
      connectionFingerprint: 'http://cpa-a.local:8317:key-a',
      provider: 'codex',
      baseline: { provider: 'codex', credentials: [] },
      requireObservedMutation: true,
    });
    expect(mocks.showNotification).toHaveBeenCalledWith(
      'auth_login.oauth_callback_success',
      'success'
    );

    await act(async () => {
      polling.resolve({ status: 'ok' });
      await pollingPromise;
    });
    expect(mocks.recordMutationMarker).toHaveBeenCalledTimes(1);
    expect(
      mocks.showNotification.mock.calls.filter(
        ([message, level]) =>
          message === 'auth_login.codex_oauth_status_success' && level === 'success'
      )
    ).toHaveLength(1);
  });

  it('keeps the provider attempt pending after an accepted callback', async () => {
    mocks.startAuth.mockResolvedValue({ url: 'https://auth.example/codex', state: 'state-a' });
    mocks.submitCallback.mockResolvedValue({ status: 'ok' });
    mocks.getAuthStatus.mockResolvedValue({ status: 'wait' });
    const renderer = await renderOAuthPage();
    const authPromise = startCodexAuth(renderer);
    await act(async () => {
      await authPromise;
    });

    const callbackInput = renderer.root
      .findAllByType('input')
      .find((input) => input.props.placeholder === 'auth_login.oauth_callback_placeholder');
    if (!callbackInput) throw new Error('Callback input not found');
    act(() => callbackInput.props.onChange({ target: { value: 'http://callback?code=1' } }));
    await act(async () => {
      await findButton(renderer, 'auth_login.oauth_callback_button').props.onClick();
    });

    expect(mocks.getAuthStatus).toHaveBeenCalledWith('state-a', {
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'key-a',
    });
    expect(mocks.recordMutationMarker).not.toHaveBeenCalled();
    expect(treeText(renderer)).toContain('auth_login.oauth_callback_status_success');
    expect(treeText(renderer)).toContain('auth_login.codex_oauth_status_waiting');
  });

  it('confirms an already-completed callback only through provider status', async () => {
    mocks.startAuth.mockResolvedValue({ url: 'https://auth.example/codex', state: 'state-a' });
    mocks.submitCallback.mockRejectedValue(
      Object.assign(new Error('oauth flow is already completed'), { status: 409 })
    );
    mocks.getAuthStatus.mockResolvedValue({ status: 'ok' });
    const renderer = await renderOAuthPage();
    const authPromise = startCodexAuth(renderer);
    await act(async () => {
      await authPromise;
    });

    const callbackInput = renderer.root
      .findAllByType('input')
      .find((input) => input.props.placeholder === 'auth_login.oauth_callback_placeholder');
    if (!callbackInput) throw new Error('Callback input not found');
    act(() => callbackInput.props.onChange({ target: { value: 'http://callback?code=1' } }));
    await act(async () => {
      await findButton(renderer, 'auth_login.oauth_callback_button').props.onClick();
    });

    expect(mocks.getAuthStatus).toHaveBeenCalledWith('state-a', {
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'key-a',
    });
    expect(mocks.recordMutationMarker).toHaveBeenCalledTimes(1);
  });

  it('ignores a late Devin polling response after the CPA connection changes', async () => {
    const polling = deferred<{ status: 'ok' }>();
    mocks.startAuth.mockResolvedValue({ url: 'https://auth.example/devin', state: 'state-devin-a' });
    mocks.getAuthStatus.mockReturnValue(polling.promise);
    const renderer = await renderOAuthPage();
    const authPromise = startDevinAuth(renderer);
    await act(async () => {
      await authPromise;
    });

    let pollingPromise!: Promise<void>;
    await act(async () => {
      pollingPromise = Promise.resolve(mocks.intervalCallbacks[0]?.());
      await Promise.resolve();
    });

    mocks.apiBase = 'http://cpa-b.local:8317';
    mocks.managementKey = 'key-b';
    await act(async () => {
      renderer.update(<OAuthPage />);
      polling.resolve({ status: 'ok' });
      await pollingPromise;
    });

    expect(mocks.recordMutationMarker).not.toHaveBeenCalled();
    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'auth_login.devin_oauth_status_success',
      'success'
    );
  });
});
