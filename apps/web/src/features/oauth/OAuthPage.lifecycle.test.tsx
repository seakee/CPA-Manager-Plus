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
    pollCallbacks: [] as Array<() => void | Promise<void>>,
  },
}));

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => undefined },
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock('react-router-dom', () => ({
  useLocation: () => ({ key: 'lifecycle', pathname: '/oauth', search: '', hash: '' }),
  useNavigate: () => mocks.navigate,
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
        <div>{title}</div>
        <div>{children}</div>
        <div>{footer}</div>
      </div>
    ) : null,
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

vi.mock('@/utils/connectionFingerprint', () => ({
  createConnectionFingerprint: (apiBase: string, managementKey: string) =>
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

vi.mock('@/utils/pluginAssets', () => ({
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

const findTileButton = (renderer: ReactTestRenderer, label: string): ReactTestInstance => {
  const button = renderer.root
    .findAllByType('button')
    .find((candidate) => candidate.props['aria-label'] === label);
  if (!button) throw new Error(`Tile button not found: ${label}`);
  return button;
};

const findButton = (renderer: ReactTestRenderer, text: string): ReactTestInstance => {
  const button = renderer.root
    .findAllByType('button')
    .find((candidate) => textContent(candidate) === text);
  if (!button) throw new Error(`Button not found: ${text}`);
  return button;
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

const startCodexAuth = async (renderer: ReactTestRenderer): Promise<void> => {
  await act(async () => {
    findTileButton(renderer, 'Codex').props.onClick();
    await Promise.resolve();
  });
};

describe('OAuthPage connection lifecycle', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.apiBase = 'http://cpa-a.local:8317';
    mocks.managementKey = 'key-a';
    mocks.pollCallbacks = [];
    mocks.authFilesList.mockReset();
    mocks.authFilesList.mockResolvedValue({ files: [] });
    vi.stubGlobal('window', {
      requestAnimationFrame: vi.fn(() => 1),
      cancelAnimationFrame: vi.fn(),
      setInterval: vi.fn(() => 1),
      clearInterval: vi.fn(),
      setTimeout: vi.fn((callback: () => void | Promise<void>, delay?: number) => {
        if (delay === 3000) {
          mocks.pollCallbacks.push(callback);
        }
        return mocks.pollCallbacks.length + 100;
      }),
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

  it('ignores a late startAuth response after the CPA connection changes', async () => {
    const startA = deferred<{ url: string; state: string }>();
    mocks.startAuth.mockReturnValue(startA.promise);
    const renderer = await renderOAuthPage();

    await startCodexAuth(renderer);
    expect(mocks.startAuth).toHaveBeenCalledWith(
      'codex',
      { apiBase: 'http://cpa-a.local:8317', managementKey: 'key-a' },
      expect.any(AbortSignal)
    );

    mocks.apiBase = 'http://cpa-b.local:8317';
    mocks.managementKey = 'key-b';
    await act(async () => {
      renderer.update(<OAuthPage />);
      await Promise.resolve();
    });

    await act(async () => {
      startA.resolve({ url: 'https://auth.example/a', state: 'state-a' });
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(treeText(renderer)).not.toContain('auth.example');
    expect(mocks.pollCallbacks).toHaveLength(0);
  });

  it('ignores a late polling completion after the CPA connection changes', async () => {
    const pollA = deferred<{ status: 'ok' }>();
    mocks.startAuth.mockResolvedValue({ url: 'https://auth.example/codex', state: 'state-a' });
    mocks.getAuthStatus.mockReturnValue(pollA.promise);
    const renderer = await renderOAuthPage();

    await startCodexAuth(renderer);
    await act(async () => {
      await Promise.resolve();
    });
    expect(mocks.pollCallbacks).toHaveLength(1);

    await act(async () => {
      mocks.pollCallbacks[0]();
      await Promise.resolve();
    });
    expect(mocks.getAuthStatus).toHaveBeenCalledWith(
      'state-a',
      { apiBase: 'http://cpa-a.local:8317', managementKey: 'key-a' },
      expect.any(AbortSignal)
    );

    mocks.apiBase = 'http://cpa-b.local:8317';
    mocks.managementKey = 'key-b';
    await act(async () => {
      renderer.update(<OAuthPage />);
      await Promise.resolve();
    });

    await act(async () => {
      pollA.resolve({ status: 'ok' });
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.showNotification).not.toHaveBeenCalledWith(
      'auth_login.codex_oauth_status_success',
      'success'
    );
    expect(mocks.recordMutationMarker).not.toHaveBeenCalled();
  });

  it('ignores a late Vertex import completion after the CPA connection changes', async () => {
    const importA = deferred<{ project_id: string }>();
    mocks.vertexImport.mockReturnValue(importA.promise);
    const renderer = await renderOAuthPage();

    await act(async () => {
      findTileButton(renderer, 'vertex_import.tile_title').props.onClick();
    });

    const fileInput = renderer.root
      .findAllByType('input')
      .find((input) => input.props.type === 'file');
    if (!fileInput) throw new Error('Vertex file input not found');
    act(() =>
      fileInput.props.onChange({
        target: { files: [{ name: 'vertex-a.json', size: 512 }], value: 'vertex-a.json' },
      })
    );

    await act(async () => {
      findButton(renderer, 'vertex_import.import_button').props.onClick();
      await Promise.resolve();
    });
    expect(mocks.vertexImport).toHaveBeenCalledWith(
      { name: 'vertex-a.json', size: 512 },
      undefined,
      { apiBase: 'http://cpa-a.local:8317', managementKey: 'key-a' },
      expect.any(AbortSignal)
    );

    mocks.apiBase = 'http://cpa-b.local:8317';
    mocks.managementKey = 'key-b';
    await act(async () => {
      renderer.update(<OAuthPage />);
      await Promise.resolve();
    });

    await act(async () => {
      importA.resolve({ project_id: 'project-a' });
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(treeText(renderer)).not.toContain('project-a');
    expect(mocks.showNotification).not.toHaveBeenCalledWith('vertex_import.success', 'success');
    expect(mocks.recordMutationMarker).not.toHaveBeenCalled();
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

    await startCodexAuth(renderer);
    await act(async () => {
      await Promise.resolve();
    });

    await act(async () => {
      mocks.pollCallbacks[0]();
      await Promise.resolve();
    });

    const callbackInput = renderer.root
      .findAllByType('input')
      .find((input) => input.props.placeholder === 'auth_login.oauth_callback_placeholder');
    if (!callbackInput) throw new Error('Callback input not found');
    act(() => callbackInput.props.onChange({ target: { value: 'http://callback?code=1' } }));

    const callbackForm = renderer.root.findByType('form');
    await act(async () => {
      callbackForm.props.onSubmit({ preventDefault: vi.fn() });
      await Promise.resolve();
    });

    await act(async () => {
      callback.resolve({ status: 'ok' });
      await Promise.resolve();
    });
    expect(mocks.recordMutationMarker).not.toHaveBeenCalled();

    await act(async () => {
      callbackStatus.resolve({ status: 'ok' });
      await Promise.resolve();
      await Promise.resolve();
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
      await Promise.resolve();
      await Promise.resolve();
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

    await startCodexAuth(renderer);
    await act(async () => {
      await Promise.resolve();
    });

    const callbackInput = renderer.root
      .findAllByType('input')
      .find((input) => input.props.placeholder === 'auth_login.oauth_callback_placeholder');
    if (!callbackInput) throw new Error('Callback input not found');
    act(() => callbackInput.props.onChange({ target: { value: 'http://callback?code=1' } }));

    await act(async () => {
      renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.getAuthStatus).toHaveBeenCalledWith(
      'state-a',
      { apiBase: 'http://cpa-a.local:8317', managementKey: 'key-a' },
      expect.any(AbortSignal)
    );
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

    await startCodexAuth(renderer);
    await act(async () => {
      await Promise.resolve();
    });

    const callbackInput = renderer.root
      .findAllByType('input')
      .find((input) => input.props.placeholder === 'auth_login.oauth_callback_placeholder');
    if (!callbackInput) throw new Error('Callback input not found');
    act(() => callbackInput.props.onChange({ target: { value: 'http://callback?code=1' } }));

    await act(async () => {
      renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.getAuthStatus).toHaveBeenCalledWith(
      'state-a',
      { apiBase: 'http://cpa-a.local:8317', managementKey: 'key-a' },
      expect.any(AbortSignal)
    );
    expect(mocks.recordMutationMarker).toHaveBeenCalledTimes(1);
  });
});
