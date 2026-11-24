import { StrictMode, type ReactNode } from 'react';
import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import type { AuthFileItem } from '@/types';
import { SiwcBindDialog } from './SiwcBindDialog';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type AuthListener = () => void;

interface MockAuthState {
  isAuthenticated: boolean;
  apiBase: string;
  managementKey: string;
}
const { mocks } = vi.hoisted(() => {
  const listeners = new Set<AuthListener>();
  return {
    mocks: {
      isAuthenticated: true,
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'key-a',
      listeners,
      authFilesList: vi.fn(),
      startSiwcBind: vi.fn(),
      getV8OAuthStatus: vi.fn(),
      submitV8OAuthCallback: vi.fn(),
      cancelV8OAuthSession: vi.fn(),
      copyToClipboard: vi.fn(async () => true),
      showNotification: vi.fn(),
      recordMutationMarker: vi.fn(),
      publishMutationRevision: vi.fn(),
      scheduledTimers: [] as Array<{ id: number; delay: number; callback: () => void; cleared: boolean }>,
      nextTimerId: 1,
    },
  };
});

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => undefined },
  useTranslation: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      params && Object.keys(params).length > 0 ? `${key}:${JSON.stringify(params)}` : key,
    i18n: { language: 'en' },
  }),
}));

vi.mock('@/components/ui/Modal', () => ({
  Modal: ({
    open,
    title,
    children,
    footer,
    onClose,
  }: {
    open: boolean;
    title?: ReactNode;
    children?: ReactNode;
    footer?: ReactNode;
    onClose?: () => void;
  }) =>
    open ? (
      <div data-testid="modal">
        <button type="button" data-testid="modal-close-trigger" onClick={onClose}>
          modal-close
        </button>
        <div data-testid="modal-title">{title}</div>
        <div data-testid="modal-body">{children}</div>
        <div data-testid="modal-footer">{footer}</div>
      </div>
    ) : null,
}));

vi.mock('@/stores', () => {
  const readAuthState = (): MockAuthState => ({
    isAuthenticated: mocks.isAuthenticated,
    apiBase: mocks.apiBase,
    managementKey: mocks.managementKey,
  });
  const useAuthStore = Object.assign(
    (selector: (state: MockAuthState) => unknown) => selector(readAuthState()),
    {
      getState: readAuthState,
      subscribe: (listener: AuthListener) => {
        mocks.listeners.add(listener);
        return () => {
          mocks.listeners.delete(listener);
        };
      },
    }
  );
  return {
    publishAccountCredentialMutationRevision: mocks.publishMutationRevision,
    useAuthStore,
    useNotificationStore: (
      selector?: (state: { showNotification: typeof mocks.showNotification }) => unknown
    ) => {
      const state = { showNotification: mocks.showNotification };
      return selector ? selector(state) : state;
    },
  };
});

vi.mock('@/services/api/authFiles', () => ({
  authFilesApi: {
    list: mocks.authFilesList,
  },
}));

vi.mock('@/services/api/oauth', () => ({
  captureSiwcBindingApi: (requestScope?: { apiBase: string; managementKey: string }) => ({
    start: (name: string, signal?: AbortSignal) =>
      mocks.startSiwcBind(name, requestScope, signal),
    getStatus: (state: string, signal?: AbortSignal) =>
      mocks.getV8OAuthStatus(state, requestScope, signal),
    submitCallback: (provider: string, redirectUrl: string, signal?: AbortSignal) =>
      mocks.submitV8OAuthCallback(provider, redirectUrl, requestScope, signal),
    cancelSession: (state: string, signal?: AbortSignal) =>
      mocks.cancelV8OAuthSession(state, requestScope, signal),
  }),
}));

vi.mock('@/features/accounts/model/accountCredentialMutationMarker', () => ({
  recordAccountCredentialMutationMarker: mocks.recordMutationMarker,
}));

vi.mock('@/utils/clipboard', () => ({
  copyToClipboard: mocks.copyToClipboard,
}));

vi.mock('@/utils/connectionFingerprint', () => ({
  createConnectionFingerprint: (apiBase: string, managementKey: string) =>
    apiBase && managementKey ? `${apiBase}:${managementKey}` : null,
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

const flushMicrotasks = async () => {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
};

const readText = (value: unknown): string => {
  if (typeof value === 'string' || typeof value === 'number') return String(value);
  if (Array.isArray(value)) return value.map(readText).join('');
  if (value && typeof value === 'object') {
    if ('children' in value && value.children !== undefined) {
      return readText(value.children);
    }
    if (
      'props' in value &&
      value.props &&
      typeof value.props === 'object' &&
      'children' in value.props
    ) {
      return readText(value.props.children);
    }
  }
  return '';
};

const findButton = (renderer: ReactTestRenderer, text: string): ReactTestInstance => {
  const button = renderer.root
    .findAllByType(Button)
    .find((node) => readText(node.props.children).includes(text));
  if (!button) throw new Error(`Button not found: ${text}`);
  return button;
};

const makeCodexTarget = (overrides: Partial<AuthFileItem> & { name: string }): AuthFileItem => ({
  provider: 'codex',
  siwc_bind_supported: true,
  ...overrides,
});

const activeTimersByDelay = (delay: number) =>
  mocks.scheduledTimers.filter((entry) => !entry.cleared && entry.delay === delay);

describe('SiwcBindDialog', () => {
  const mountedRenderers = new Set<ReactTestRenderer>();

  const mount = async (element: ReactNode): Promise<ReactTestRenderer> => {
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(<>{element}</>);
      await flushMicrotasks();
    });
    mountedRenderers.add(renderer);
    return renderer;
  };

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.isAuthenticated = true;
    mocks.apiBase = 'http://cpa-a.local:8317';
    mocks.managementKey = 'key-a';
    mocks.listeners.clear();
    mocks.scheduledTimers = [];
    mocks.nextTimerId = 1;

    mocks.authFilesList.mockResolvedValue({ files: [] });
    mocks.startSiwcBind.mockResolvedValue({
      status: 'ok',
      flow: 'siwc_bind',
      state: 'siwc-state-1',
      url: 'https://auth.openai.com/oauth/authorize?state=siwc-state-1',
      expires_in: 300,
    });
    mocks.getV8OAuthStatus.mockResolvedValue({ status: 'wait' });
    mocks.submitV8OAuthCallback.mockResolvedValue({ status: 'ok' });
    mocks.cancelV8OAuthSession.mockResolvedValue({ status: 'ok', cancelled: true });

    vi.stubGlobal('window', {
      setTimeout: vi.fn((callback: () => void, delay?: number) => {
        const id = mocks.nextTimerId++;
        mocks.scheduledTimers.push({
          id,
          delay: delay ?? 0,
          callback,
          cleared: false,
        });
        return id;
      }),
      clearTimeout: vi.fn((id: number) => {
        const timer = mocks.scheduledTimers.find((entry) => entry.id === id);
        if (timer) timer.cleared = true;
      }),
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

  it('loads eligible Codex targets via scoped authFilesApi.list, renders all status badges, and hides expiry for unbound', async () => {
    mocks.authFilesList.mockResolvedValue({
      files: [
        makeCodexTarget({
          name: 'codex-active.json',
          siwc_bound: true,
          use_siwc: true,
          siwc_expires_at: '2027-06-01T00:00:00Z',
        }),
        makeCodexTarget({
          name: 'codex-invalid.json',
          siwc_bound: true,
          use_siwc: true,
          siwc_status: 'invalid',
        }),
        makeCodexTarget({
          name: 'codex-bound.json',
          siwc_bound: true,
          use_siwc: false,
        }),
        makeCodexTarget({
          name: 'codex-unbound.json',
          siwc_bound: false,
          use_siwc: false,
          siwc_expires_at: '2027-07-01T00:00:00Z',
        }),
        {
          name: 'claude-unsupported.json',
          provider: 'claude',
          siwc_bind_supported: false,
        },
      ],
    });

    const renderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
    );

    expect(mocks.authFilesList).toHaveBeenCalledWith({
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'key-a',
    });

    const renderedText = readText(renderer.toJSON());
    expect(renderedText).toContain('codex-active.json');
    expect(renderedText).toContain('codex-invalid.json');
    expect(renderedText).toContain('codex-bound.json');
    expect(renderedText).toContain('codex-unbound.json');
    expect(renderedText).not.toContain('claude-unsupported.json');

    expect(renderedText).toContain('siwc.active');
    expect(renderedText).toContain('siwc.invalid');
    expect(renderedText).toContain('siwc.bound');
    expect(renderedText).toContain('siwc.unbound');

    // Only codex-active.json should show expiry; unbound must omit expiry even if raw field exists.
    const labels = renderer.root.findAllByType('label');
    const unboundLabel = labels.find((node) => readText(node).includes('codex-unbound.json'));
    expect(unboundLabel).toBeDefined();
    expect(readText(unboundLabel)).not.toContain('siwc.expires');

    const activeLabel = labels.find((node) => readText(node).includes('codex-active.json'));
    expect(activeLabel).toBeDefined();
    expect(readText(activeLabel)).toContain('siwc.expires');

    // With multiple targets, start button remains disabled until a radio is selected.
    expect(findButton(renderer, 'siwc.start').props.disabled).toBe(true);
  });

  it('auto-selects a single eligible target and handles empty and load-error states', async () => {
    mocks.authFilesList.mockResolvedValueOnce({
      files: [makeCodexTarget({ name: 'codex-only.json', siwc_bound: false })],
    });

    const singleRenderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
    );
    const radios = singleRenderer.root.findAllByType('input');
    expect(radios).toHaveLength(1);
    expect(radios[0].props.checked).toBe(true);
    expect(findButton(singleRenderer, 'siwc.start').props.disabled).toBe(false);

    mocks.authFilesList.mockResolvedValueOnce({ files: [] });
    const emptyRenderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
    );
    expect(readText(emptyRenderer.toJSON())).toContain('siwc.no_targets');
    expect(findButton(emptyRenderer, 'siwc.start').props.disabled).toBe(true);

    mocks.authFilesList.mockRejectedValueOnce(new Error('network down'));
    const failedRenderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
    );
    expect(readText(failedRenderer.toJSON())).toContain('siwc.load_failed');
    expect(findButton(failedRenderer, 'siwc.start').props.disabled).toBe(true);
  });

  it('transitions to unsupported on HTTP 404 from start without falling back', async () => {
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-1.json' })],
    });
    mocks.startSiwcBind.mockRejectedValueOnce(Object.assign(new Error('Not Found'), { status: 404 }));

    const renderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
    );

    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });

    expect(mocks.startSiwcBind).toHaveBeenCalledTimes(1);
    expect(readText(renderer.toJSON())).toContain('siwc.unsupported');
    expect(mocks.cancelV8OAuthSession).not.toHaveBeenCalled();
  });

  it('rejects invalid callback URLs locally without calling the API', async () => {
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-1.json' })],
    });

    const renderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
    );

    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });

    const input = renderer.root.findByType(Input);
    act(() => {
      input.props.onChange({
        target: { value: 'http://127.0.0.1:1455/auth/callback?code=abc&state=wrong-state' },
      });
    });

    const form = renderer.root.findByType('form');
    await act(async () => {
      form.props.onSubmit({ preventDefault: vi.fn() });
      await flushMicrotasks();
    });

    expect(mocks.submitV8OAuthCallback).not.toHaveBeenCalled();
    expect(readText(renderer.toJSON())).toContain('siwc.invalid_callback');
  });

  it('prevents synchronous double-submit, probes status immediately on 200 OK, and fires onSuccess + both mutation markers', async () => {
    const onSuccess = vi.fn();
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-1.json' })],
    });
    const callbackDeferred = createDeferred<{ status: 'ok' }>();
    mocks.submitV8OAuthCallback.mockReturnValue(callbackDeferred.promise);
    mocks.getV8OAuthStatus.mockResolvedValue({ status: 'ok' });

    const renderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onSuccess={onSuccess} onViewAccounts={vi.fn()} />
    );

    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });

    const validCallback =
      'http://127.0.0.1:1455/auth/callback?code=auth-code&client_id=client-1&state=siwc-state-1';
    act(() => {
      renderer.root.findByType(Input).props.onChange({ target: { value: validCallback } });
    });

    // Fire two synchronous submits before React re-renders (e.g. Enter + click).
    const form = renderer.root.findByType('form');
    await act(async () => {
      form.props.onSubmit({ preventDefault: vi.fn() });
      form.props.onSubmit({ preventDefault: vi.fn() });
      await Promise.resolve();
    });

    expect(mocks.submitV8OAuthCallback).toHaveBeenCalledTimes(1);
    expect(mocks.submitV8OAuthCallback).toHaveBeenCalledWith(
      'codex',
      validCallback,
      { apiBase: 'http://cpa-a.local:8317', managementKey: 'key-a' },
      expect.any(AbortSignal)
    );

    await act(async () => {
      callbackDeferred.resolve({ status: 'ok' });
      await flushMicrotasks();
    });

    expect(mocks.getV8OAuthStatus).toHaveBeenCalledTimes(1);
    expect(mocks.getV8OAuthStatus).toHaveBeenCalledWith(
      'siwc-state-1',
      { apiBase: 'http://cpa-a.local:8317', managementKey: 'key-a' },
      expect.any(AbortSignal)
    );
    expect(readText(renderer.toJSON())).toContain('siwc.ok');
    expect(onSuccess).toHaveBeenCalledTimes(1);
    expect(mocks.recordMutationMarker).toHaveBeenCalledWith({
      connectionFingerprint: 'http://cpa-a.local:8317:key-a',
      provider: 'codex',
    });
    expect(mocks.publishMutationRevision).toHaveBeenCalledWith({
      connectionFingerprint: 'http://cpa-a.local:8317:key-a',
      provider: 'codex',
      kind: 'oauth',
      credentialIdentity: 'codex-1.json',
    });
    expect(mocks.showNotification).toHaveBeenCalledWith('siwc.ok', 'success');
    expect(mocks.cancelV8OAuthSession).not.toHaveBeenCalled();
  });

  it('preserves the active session on transient getStatus error, renders retry notice, and succeeds on retry', async () => {
    const onSuccess = vi.fn();
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-1.json' })],
    });
    mocks.getV8OAuthStatus
      .mockRejectedValueOnce(Object.assign(new Error('Gateway Timeout'), { status: 504 }))
      .mockResolvedValueOnce({ status: 'wait' })
      .mockResolvedValueOnce({ status: 'ok' });

    const renderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onSuccess={onSuccess} onViewAccounts={vi.fn()} />
    );

    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });

    // Trigger first scheduled 3s poll -> fails transiently.
    const firstPoll = activeTimersByDelay(3000)[0];
    expect(firstPoll).toBeDefined();
    await act(async () => {
      firstPoll.callback();
      await flushMicrotasks();
    });

    expect(mocks.cancelV8OAuthSession).not.toHaveBeenCalled();
    expect(readText(renderer.toJSON())).toContain('siwc.poll_retrying');
    expect(readText(renderer.toJSON())).toContain('siwc.retry_poll');
    expect(readText(renderer.toJSON())).not.toContain('siwc.error');
    // Another 3s poll must also remain scheduled.
    expect(activeTimersByDelay(3000).length).toBeGreaterThanOrEqual(1);

    // Click inline "Retry now" button -> returns wait and clears the notice.
    await act(async () => {
      findButton(renderer, 'siwc.retry_poll').props.onClick();
      await flushMicrotasks();
    });
    expect(readText(renderer.toJSON())).not.toContain('siwc.poll_retrying');
    expect(readText(renderer.toJSON())).toContain('siwc.wait');

    // Trigger next scheduled 3s poll -> returns ok.
    const nextPoll = activeTimersByDelay(3000)[activeTimersByDelay(3000).length - 1];
    expect(nextPoll).toBeDefined();
    await act(async () => {
      nextPoll.callback();
      await flushMicrotasks();
    });

    expect(readText(renderer.toJSON())).toContain('siwc.ok');
    expect(onSuccess).toHaveBeenCalledTimes(1);
    expect(mocks.cancelV8OAuthSession).not.toHaveBeenCalled();
  });

  it('transitions to expired without cancelling on HTTP 404 from getStatus', async () => {
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-1.json' })],
    });
    mocks.getV8OAuthStatus.mockRejectedValueOnce(
      Object.assign(new Error('session not found'), { status: 404 })
    );

    const renderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
    );

    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });

    const firstPoll = activeTimersByDelay(3000)[0];
    await act(async () => {
      firstPoll.callback();
      await flushMicrotasks();
    });

    expect(readText(renderer.toJSON())).toContain('siwc.expired');
    expect(mocks.cancelV8OAuthSession).not.toHaveBeenCalled();
  });

  it('probes status on HTTP 409 Conflict from callback and completes cleanly when session is ok', async () => {
    const onSuccess = vi.fn();
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-1.json' })],
    });
    mocks.submitV8OAuthCallback.mockRejectedValueOnce(
      Object.assign(new Error('already completed'), { status: 409 })
    );
    mocks.getV8OAuthStatus.mockResolvedValueOnce({ status: 'ok' });

    const renderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onSuccess={onSuccess} onViewAccounts={vi.fn()} />
    );

    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });

    const validCallback =
      'http://127.0.0.1:1455/auth/callback?code=auth-code&client_id=client-1&state=siwc-state-1';
    act(() => {
      renderer.root.findByType(Input).props.onChange({ target: { value: validCallback } });
    });

    await act(async () => {
      renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
      await flushMicrotasks();
    });

    expect(mocks.getV8OAuthStatus).toHaveBeenCalledTimes(1);
    expect(readText(renderer.toJSON())).toContain('siwc.ok');
    expect(readText(renderer.toJSON())).not.toContain('siwc.callback_failed');
    expect(onSuccess).toHaveBeenCalledTimes(1);
    expect(mocks.recordMutationMarker).toHaveBeenCalledTimes(1);
    expect(mocks.publishMutationRevision).toHaveBeenCalledTimes(1);
  });

  it('keeps waiting with submitted feedback on HTTP 409 Conflict when status probe is still wait', async () => {
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-1.json' })],
    });
    mocks.submitV8OAuthCallback.mockRejectedValueOnce(
      Object.assign(new Error('exchange in progress'), { status: 409 })
    );
    mocks.getV8OAuthStatus.mockResolvedValueOnce({ status: 'wait' });

    const renderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
    );

    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });

    const validCallback =
      'http://127.0.0.1:1455/auth/callback?code=auth-code&client_id=client-1&state=siwc-state-1';
    act(() => {
      renderer.root.findByType(Input).props.onChange({ target: { value: validCallback } });
    });

    await act(async () => {
      renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
      await flushMicrotasks();
    });

    expect(mocks.getV8OAuthStatus).toHaveBeenCalledTimes(1);
    expect(readText(renderer.toJSON())).toContain('siwc.submitted');
    expect(readText(renderer.toJSON())).toContain('siwc.wait');
    expect(readText(renderer.toJSON())).not.toContain('siwc.callback_failed');
  });

  it('transitions to error without cancelling when getStatus returns explicit status error', async () => {
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-1.json' })],
    });
    mocks.getV8OAuthStatus.mockResolvedValueOnce({ status: 'error', error: 'access_denied' });

    const renderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
    );

    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });

    const firstPoll = activeTimersByDelay(3000)[0];
    await act(async () => {
      firstPoll.callback();
      await flushMicrotasks();
    });

    expect(readText(renderer.toJSON())).toContain('siwc.error');
    expect(mocks.cancelV8OAuthSession).not.toHaveBeenCalled();
  });

  it('cancels the active session immediately on close while waiting, on the captured connection', async () => {
    const onClose = vi.fn();
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-1.json' })],
    });

    const renderer = await mount(
      <SiwcBindDialog open onClose={onClose} onViewAccounts={vi.fn()} />
    );

    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });

    await act(async () => {
      findButton(renderer, 'siwc.cancel').props.onClick();
      await flushMicrotasks();
    });

    expect(mocks.cancelV8OAuthSession).toHaveBeenCalledTimes(1);
    expect(mocks.cancelV8OAuthSession).toHaveBeenCalledWith(
      'siwc-state-1',
      { apiBase: 'http://cpa-a.local:8317', managementKey: 'key-a' },
      undefined
    );
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('cancels a late start response on the captured connection if closed or connection switched while start is in-flight', async () => {
    const onClose = vi.fn();
    const startDeferred = createDeferred<{
      status: 'ok';
      flow: 'siwc_bind';
      state: string;
      url: string;
    }>();
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-1.json' })],
    });
    mocks.startSiwcBind.mockReturnValueOnce(startDeferred.promise);

    const renderer = await mount(
      <SiwcBindDialog open onClose={onClose} onViewAccounts={vi.fn()} />
    );

    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });

    // Switch connection while start is still in-flight.
    mocks.apiBase = 'http://cpa-b.local:8317';
    mocks.managementKey = 'key-b';
    await act(async () => {
      mocks.listeners.forEach((listener) => listener());
      await flushMicrotasks();
    });
    expect(onClose).toHaveBeenCalledTimes(1);

    // Late start response arrives from connection A -> must cancel on connection A!
    await act(async () => {
      startDeferred.resolve({
        status: 'ok',
        flow: 'siwc_bind',
        state: 'orphan-state-a',
        url: 'https://auth.openai.com/oauth/authorize?state=orphan-state-a',
      });
      await flushMicrotasks();
    });

    expect(mocks.cancelV8OAuthSession).toHaveBeenCalledTimes(1);
    expect(mocks.cancelV8OAuthSession).toHaveBeenCalledWith(
      'orphan-state-a',
      { apiBase: 'http://cpa-a.local:8317', managementKey: 'key-a' },
      undefined
    );
  });

  it('fences terminal ok transition so a concurrently aborted submitCallback never writes stale feedback', async () => {
    const callbackDeferred = createDeferred<{ status: 'ok' }>();
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-1.json' })],
    });
    mocks.submitV8OAuthCallback.mockReturnValueOnce(callbackDeferred.promise);
    mocks.getV8OAuthStatus.mockResolvedValueOnce({ status: 'ok' });

    const renderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
    );

    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });

    const validCallback =
      'http://127.0.0.1:1455/auth/callback?code=auth-code&client_id=client-1&state=siwc-state-1';
    act(() => {
      renderer.root.findByType(Input).props.onChange({ target: { value: validCallback } });
    });

    await act(async () => {
      renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
      await Promise.resolve();
    });

    // Scheduled poll resolves ok while submitCallback is still in-flight (aborting its signal).
    const firstPoll = activeTimersByDelay(3000)[0];
    await act(async () => {
      firstPoll.callback();
      await flushMicrotasks();
    });
    expect(readText(renderer.toJSON())).toContain('siwc.ok');

    // Aborted submitCallback rejects -> must be ignored without clobbering ok state.
    await act(async () => {
      callbackDeferred.reject(new DOMException('Aborted', 'AbortError'));
      await flushMicrotasks();
    });

    expect(readText(renderer.toJSON())).toContain('siwc.ok');
    expect(readText(renderer.toJSON())).not.toContain('siwc.callback_failed');
  });

  it('fences expiry timeout so an aborted in-flight submitCallback never writes stale feedback', async () => {
    const callbackDeferred = createDeferred<{ status: 'ok' }>();
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-1.json' })],
    });
    mocks.submitV8OAuthCallback.mockReturnValueOnce(callbackDeferred.promise);

    const renderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
    );

    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });

    const validCallback =
      'http://127.0.0.1:1455/auth/callback?code=auth-code&client_id=client-1&state=siwc-state-1';
    act(() => {
      renderer.root.findByType(Input).props.onChange({ target: { value: validCallback } });
    });

    await act(async () => {
      renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
      await Promise.resolve();
    });

    // Expiry timer (300_000ms) fires while callback is still in flight.
    const expiryTimer = activeTimersByDelay(300_000)[0];
    expect(expiryTimer).toBeDefined();
    await act(async () => {
      expiryTimer.callback();
      await flushMicrotasks();
    });
    expect(readText(renderer.toJSON())).toContain('siwc.expired');
    expect(mocks.cancelV8OAuthSession).toHaveBeenCalledTimes(1);

    await act(async () => {
      callbackDeferred.reject(new DOMException('Aborted', 'AbortError'));
      await flushMicrotasks();
    });
    expect(readText(renderer.toJSON())).toContain('siwc.expired');
    expect(readText(renderer.toJSON())).not.toContain('siwc.callback_failed');
  });

  it('works cleanly under React.StrictMode double mount/unmount/remount', async () => {
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-strict.json' })],
    });

    const renderer = await mount(
      <StrictMode>
        <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
      </StrictMode>
    );

    expect(readText(renderer.toJSON())).toContain('codex-strict.json');
    expect(findButton(renderer, 'siwc.start').props.disabled).toBe(false);

    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });

    expect(mocks.startSiwcBind).toHaveBeenCalledTimes(1);
    expect(readText(renderer.toJSON())).toContain('siwc.wait');
    expect(mocks.cancelV8OAuthSession).not.toHaveBeenCalled();
  });

  it('resets state and captures fresh connection when reopened via open prop toggle', async () => {
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-a.json' })],
    });

    const renderer = await mount(
      <SiwcBindDialog open={false} onClose={vi.fn()} onViewAccounts={vi.fn()} />
    );
    expect(mocks.authFilesList).not.toHaveBeenCalled();

    // Open on connection A.
    await act(async () => {
      renderer.update(
        <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
      );
      await flushMicrotasks();
    });
    expect(mocks.authFilesList).toHaveBeenLastCalledWith({
      apiBase: 'http://cpa-a.local:8317',
      managementKey: 'key-a',
    });

    // Close and switch connection to B.
    await act(async () => {
      renderer.update(
        <SiwcBindDialog open={false} onClose={vi.fn()} onViewAccounts={vi.fn()} />
      );
      await flushMicrotasks();
    });

    mocks.apiBase = 'http://cpa-b.local:8317';
    mocks.managementKey = 'key-b';
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-b.json' })],
    });

    // Reopen on connection B.
    await act(async () => {
      renderer.update(
        <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
      );
      await flushMicrotasks();
    });
    expect(mocks.authFilesList).toHaveBeenLastCalledWith({
      apiBase: 'http://cpa-b.local:8317',
      managementKey: 'key-b',
    });
    expect(readText(renderer.toJSON())).toContain('codex-b.json');
    expect(readText(renderer.toJSON())).not.toContain('codex-a.json');
  });

  it('opens auth link in a new window and copies auth URL with feedback', async () => {
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-copy.json' })],
    });
    mocks.copyToClipboard.mockResolvedValue(true);
    const authUrl = 'https://auth.openai.com/oauth/authorize?state=st-open-copy';
    mocks.startSiwcBind.mockResolvedValue({
      status: 'ok',
      flow: 'siwc_bind',
      url: authUrl,
      state: 'st-open-copy',
      expires_in: 300,
    });

    const renderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
    );
    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });

    act(() => {
      findButton(renderer, 'siwc.open').props.onClick();
    });
    expect(window.open).toHaveBeenCalledWith(authUrl, '_blank', 'noopener,noreferrer');

    await act(async () => {
      findButton(renderer, 'siwc.copy').props.onClick();
      await flushMicrotasks();
    });
    expect(mocks.copyToClipboard).toHaveBeenCalledWith(authUrl);
    expect(readText(renderer.toJSON())).toContain('common.copied');
  });

  it('restarts cleanly from error and expired phases while hiding Retry on unsupported', async () => {
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-retry.json' })],
    });
    mocks.startSiwcBind.mockRejectedValueOnce(Object.assign(new Error('boom'), { status: 500 }));

    const renderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
    );

    // Error phase exposes siwc.retry and resets to select.
    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });
    expect(readText(renderer.toJSON())).toContain('siwc.error');
    await act(async () => {
      findButton(renderer, 'siwc.retry').props.onClick();
      await flushMicrotasks();
    });
    expect(findButton(renderer, 'siwc.start')).toBeDefined();

    // Expired phase exposes siwc.retry and resets to select.
    mocks.startSiwcBind.mockResolvedValueOnce({
      status: 'ok',
      flow: 'siwc_bind',
      url: 'https://auth.openai.com/oauth/authorize?state=st-exp-retry',
      state: 'st-exp-retry',
      expires_in: 300,
    });
    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });
    const expiryTimer = activeTimersByDelay(300_000)[0];
    expect(expiryTimer).toBeDefined();
    await act(async () => {
      expiryTimer.callback();
      await flushMicrotasks();
    });
    expect(readText(renderer.toJSON())).toContain('siwc.expired');
    await act(async () => {
      findButton(renderer, 'siwc.retry').props.onClick();
      await flushMicrotasks();
    });
    expect(findButton(renderer, 'siwc.start')).toBeDefined();

    // Unsupported (404) hides siwc.retry and keeps only Close.
    mocks.startSiwcBind.mockRejectedValueOnce(Object.assign(new Error('not found'), { status: 404 }));
    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });
    expect(readText(renderer.toJSON())).toContain('siwc.unsupported');
    expect(
      renderer.root
        .findAllByType(Button)
        .some((node) => readText(node.props.children).includes('siwc.retry'))
    ).toBe(false);
    expect(findButton(renderer, 'common.close')).toBeDefined();
  });

  it('rejects malformed start responses (non-HTTP URL or unexpected flow) and cancels returned state', async () => {
    mocks.authFilesList.mockResolvedValue({
      files: [makeCodexTarget({ name: 'codex-malformed.json' })],
    });

    const renderer = await mount(
      <SiwcBindDialog open onClose={vi.fn()} onViewAccounts={vi.fn()} />
    );

    // Non-HTTP URL rejected and state cancelled.
    mocks.startSiwcBind.mockResolvedValueOnce({
      status: 'ok',
      flow: 'siwc_bind',
      url: 'javascript:alert(1)',
      state: 'st-bad-url',
      expires_in: 300,
    });
    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });
    expect(readText(renderer.toJSON())).toContain('siwc.error');
    expect(mocks.cancelV8OAuthSession).toHaveBeenCalledWith(
      'st-bad-url',
      { apiBase: 'http://cpa-a.local:8317', managementKey: 'key-a' },
      undefined
    );

    await act(async () => {
      findButton(renderer, 'siwc.retry').props.onClick();
      await flushMicrotasks();
    });

    // Unexpected flow rejected and state cancelled.
    mocks.startSiwcBind.mockResolvedValueOnce({
      status: 'ok',
      flow: 'unexpected_flow',
      url: 'https://auth.openai.com/oauth/authorize?state=st-bad-flow',
      state: 'st-bad-flow',
      expires_in: 300,
    });
    await act(async () => {
      findButton(renderer, 'siwc.start').props.onClick();
      await flushMicrotasks();
    });
    expect(readText(renderer.toJSON())).toContain('siwc.error');
    expect(mocks.cancelV8OAuthSession).toHaveBeenCalledWith(
      'st-bad-flow',
      { apiBase: 'http://cpa-a.local:8317', managementKey: 'key-a' },
      undefined
    );
  });
});
