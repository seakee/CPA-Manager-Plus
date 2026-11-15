import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { StrictMode, type ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FreshApiKeys } from './FreshApiKeys';
import { createPublicApiClient, PublicApiError, type PublicApiClient } from './publicApi';
import { fullWebText as t } from '../i18n/fullweb';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { externalWorkspaceText as x } from '../i18n/freshExternal';
import { DropdownMenu } from '../components/ui/DropdownMenu';
import { Modal } from '../components/ui/Modal';
// Renderer tests exercise domain state; browser acceptance covers the shared
// portal, focus, animation and close controls.
vi.mock('../components/ui/Modal', () => ({
  Modal: ({
    open,
    children,
    footer,
  }: {
    open: boolean;
    children?: ReactNode;
    footer?: ReactNode;
    closeDisabled?: boolean;
    onClose?: () => void;
  }) =>
    open ? (
      <div>
        {children}
        {footer}
      </div>
    ) : null,
}));
vi.mock('../components/ui/Drawer', () => ({
  Drawer: ({
    open,
    children,
    footer,
  }: {
    open: boolean;
    children?: ReactNode;
    footer?: ReactNode;
  }) =>
    open ? (
      <div>
        {children}
        {footer}
      </div>
    ) : null,
}));

const context = { basePath: '/console', apiBase: '/console/api/v2', revision: 1 };
const key = {
  id: 'a'.repeat(32),
  requestId: 'b'.repeat(32),
  maskedPreview: 'cpamp_sk_…cccc',
  createdAtMs: 1234,
  managerState: 'persisted',
  runtimeState: 'not_applied',
  name: '',
  nameRevision: 1,
  origin: 'workspace' as const,
};
const secret = 'cpamp_sk_' + 'c'.repeat(64);
const empty = () => Response.json({ keys: [], gatewayPending: true, revocationAvailable: false });

function withIdleKeyOperation(transport: typeof fetch) {
  return vi.fn<typeof fetch>((input, init) =>
    String(input).endsWith('/api-keys/operations/latest') && (!init?.method || init.method === 'GET')
      ? Promise.resolve(Response.json({ operation: null, runtimeState: 'effect_unknown', expiresAtMs: 0, nextStep: '' }))
      : transport(input, init));
}

describe('Fresh client key persistence UI', () => {
  let renderer: ReactTestRenderer;
  const unauthorized = vi.fn();
  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    unauthorized.mockClear();
    vi.stubGlobal('window', { location: new URL('https://manager.example/console') });
    vi.stubGlobal('crypto', { getRandomValues: (bytes: Uint8Array) => bytes.fill(0xbb) });
    vi.stubGlobal('navigator', {
      clipboard: { writeText: vi.fn().mockResolvedValue(undefined) },
    });
    vi.stubGlobal('localStorage', {
      setItem: vi.fn(() => {
        throw new Error('forbidden');
      }),
    });
    vi.stubGlobal('sessionStorage', {
      setItem: vi.fn(() => {
        throw new Error('forbidden');
      }),
    });
  });
  afterEach(async () => {
    if (renderer) await act(async () => renderer.unmount());
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });
  const view = () => JSON.stringify(renderer.toJSON());
  const button = (text: string) =>
    renderer.root
      .findAllByType('button')
      .find((item) => item.props['aria-label'] === text || item.props.children === text)!;
  async function mount(
    fetchMock: ReturnType<typeof vi.fn<typeof fetch>>,
    onCreationPending = vi.fn(),
    usageResponse: unknown = {
      fromMs: 1,
      toMs: 2,
      coverage: { state: 'unknown', reason: 'usage_read_failed' },
      items: [],
    },
    accessInfoResponse: unknown = {
      mode: 'new_managed',
      managementBasePath: '/console',
      inferenceBasePath: '/api',
      protocols: [
        {
          id: 'openai',
          label: 'OpenAI',
          basePath: '/api/v1',
          examplePath: '/api/v1/chat/completions',
        },
        {
          id: 'anthropic',
          label: 'Anthropic',
          basePath: '/api',
          examplePath: '/api/v1/messages',
        },
      ],
      ready: true,
    }
  ) {
    const routedFetch = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys/usage-summary'))
        return Promise.resolve(
          usageResponse instanceof Response ? usageResponse : Response.json(usageResponse)
        );
      if (url.endsWith('/api-keys/access-info') && accessInfoResponse !== 'passthrough')
        return Promise.resolve(
          accessInfoResponse instanceof Response
            ? accessInfoResponse
            : Response.json(accessInfoResponse)
        );
      return fetchMock(input, init);
    });
    await act(async () => {
      renderer = create(
        <FreshApiKeys
          client={createPublicApiClient(() => context, routedFetch)}
          credential="admin-test-only"
          onUnauthorized={unauthorized}
          onCreationPending={onCreationPending}
        />
      );
    });
  }
  async function click(text: string) {
    if (text === t('keysCreate')) {
      await openCreate();
      await act(async () =>
        renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() })
      );
      return;
    }
    await act(async () => button(text).props.onClick());
  }
  async function openCreate() {
    if (!renderer.root.findAllByType('form').length)
      await act(async () => button(t('keysCreate')).props.onClick());
  }
  async function setCreateName(value: string) {
    await openCreate();
    await act(async () =>
      renderer.root
        .findByProps({ 'data-testid': 'create-key-name-input' })
        .props.onChange({ target: { value } })
    );
  }

  it('opens and cancels creation without writing or showing the name input in the page toolbar', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(empty());
    await mount(fetchMock);
    expect(renderer.root.findAllByProps({ 'data-testid': 'create-key-name-input' })).toHaveLength(
      0
    );
    await openCreate();
    await setCreateName('Unsaved draft');
    expect(fetchMock).toHaveBeenCalledOnce();
    await click(k('cancel'));
    expect(renderer.root.findAllByProps({ 'data-testid': 'create-key-name-input' })).toHaveLength(
      0
    );
    await openCreate();
    expect(renderer.root.findByProps({ 'data-testid': 'create-key-name-input' }).props.value).toBe(
      ''
    );
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it('filters only actual saved metadata without issuing requests or claiming gateway effectiveness', async () => {
    const historical = {
      ...key,
      id: 'd'.repeat(32),
      requestId: 'e'.repeat(32),
      name: 'Retired client',
      lifecycle: 'superseded',
    };
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
      Response.json({
        keys: [{ ...key, name: 'Desktop client' }, historical],
        gatewayPending: true,
        revocationAvailable: false,
      })
    );
    await mount(fetchMock);
    await act(async () =>
      renderer.root
        .findByProps({ 'aria-label': k('lifecycle') })
        .props.onChange({ target: { value: 'all' } })
    );
    expect(renderer.root.findByType('tbody').findAllByType('tr')).toHaveLength(2);
    const search = renderer.root.findByProps({ 'aria-label': k('search') });
    await act(async () => search.props.onChange({ target: { value: 'desktop' } }));
    expect(renderer.root.findByType('tbody').findAllByType('tr')).toHaveLength(1);
    expect(view()).toContain('Desktop client');
    await act(async () => search.props.onChange({ target: { value: historical.id } }));
    expect(renderer.root.findByType('tbody').findAllByType('tr')).toHaveLength(1);
    expect(view()).toContain('Retired client');
    await act(async () => search.props.onChange({ target: { value: 'does-not-exist' } }));
    expect(view()).toContain(k('noMatches'));
    await click(k('clear'));
    expect(renderer.root.findByType('tbody').findAllByType('tr')).toHaveLength(1);
    expect(view()).toContain(k('runtimeNotApplied'));
    expect(view()).toContain(k('scope'));
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it('keeps the Console exit lock when a revocation read finishes during an unknown activation recovery in details', async () => {
    const onCreationPending = vi.fn();
    let finishOperation!: (response: Response) => void;
    let rejectRetry!: (reason: Error) => void;
    const activeAfterReconcile = { ...key, runtimeState: 'effective_observed' as const };
    const fetchMock = vi.fn<typeof fetch>((url, options) => {
      const address = String(url);
      if (address.endsWith('/api-keys') && options?.method === 'GET')
        return Promise.resolve(
          Response.json({ keys: [key], gatewayPending: false, revocationAvailable: true })
        );
      if (address.endsWith('/api-keys/operations/latest'))
        return new Promise<Response>((resolve) => {
          finishOperation = resolve;
        });
      if (address.endsWith('/api-keys/' + key.id) && options?.method === 'GET')
        return Promise.resolve(Response.json(key));
      if (address.endsWith('/api-keys/' + key.id + '/usage') && options?.method === 'GET')
        return Promise.resolve(
          Response.json({
            keyId: key.id,
            fromMs: 1,
            toMs: 2,
            bucketMs: 1,
            coverage: { state: 'unknown', reason: 'usage_read_failed', collectingSinceMs: 0, lastProcessedEventId: 0, targetEventId: 0 },
            totals: null,
            series: [],
          })
        );
      if (address.endsWith('/api-keys') && options?.method === 'POST')
        return new Promise<Response>((_resolve, reject) => {
          rejectRetry = reject;
        });
      if (address.endsWith('/api-keys/requests/' + key.requestId) && options?.method === 'GET')
        return Promise.resolve(Response.json(activeAfterReconcile));
      return Promise.reject(new Error('unexpected Key API: ' + address));
    });
    await mount(fetchMock, onCreationPending);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'open-key-' + key.id }).props.onClick()
    );
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-retry-apply' }).props.onClick()
    );
    expect(onCreationPending).toHaveBeenLastCalledWith(true);

    // The separate Revocation component settling must not release Key Details' pending lock.
    await act(async () =>
      finishOperation(
        Response.json({
          operation: null,
          nextStep: '',
          runtimeState: 'effect_unknown',
          expiresAtMs: 0,
        })
      )
    );
    expect(onCreationPending).toHaveBeenLastCalledWith(true);
    await act(async () => rejectRetry(new Error('apply response lost')));
    expect(onCreationPending).toHaveBeenLastCalledWith(true);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-reconcile-apply' }).props.onClick()
    );
    expect(onCreationPending).toHaveBeenLastCalledWith(false);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
  });
  it('keeps a pending revocation lock when an unrelated Key detail is read', async () => {
    const onCreationPending = vi.fn();
    const pendingOperation = {
      id: 'd'.repeat(32),
      targetId: key.id,
      keeperId: 'e'.repeat(32),
      mode: 'replace',
      outcome: 'pending',
      revision: 1,
      steps: [
        { name: 'add', state: 'unattempted', returned: false },
        { name: 'revoke', state: 'unattempted', returned: false },
      ],
    };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        Response.json({ keys: [key], gatewayPending: true, revocationAvailable: true })
      )
      .mockResolvedValueOnce(
        Response.json({
          operation: pendingOperation,
          nextStep: 'add',
          runtimeState: 'effect_unknown',
          expiresAtMs: 0,
        })
      )
      .mockResolvedValueOnce(Response.json(key));
    await mount(fetchMock, onCreationPending);
    expect(onCreationPending).toHaveBeenLastCalledWith(true);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'open-key-' + key.id }).props.onClick()
    );
    expect(onCreationPending).toHaveBeenLastCalledWith(true);
    expect(fetchMock.mock.calls.every(([, init]) => init?.method === 'GET')).toBe(true);
  });

  it('uses real Public API, shows creation secret once, preserves identity and never persists browser secrets', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(empty())
      .mockResolvedValueOnce(Response.json({ key, createdSecret: secret }, { status: 201 }));
    await mount(fetchMock);
    expect(view()).toContain(t('keysEmpty'));
    await click(t('keysCreate'));
    expect(view()).toContain(secret);
    expect(view()).toContain(key.id);
    expect(view()).toContain(k('unavailableHelp'));
    expect(button(t('keysCreate')).props.disabled).toBe(true);
    await click(t('keysCopy'));
    expect(navigator.clipboard.writeText).toHaveBeenCalledExactlyOnceWith(secret);
    expect(view()).toContain(t('keysCopied'));
    const [url, init] = fetchMock.mock.calls[1];
    expect(url).toBe('https://manager.example/console/api/v2/api-keys');
    expect(init).toMatchObject({
      method: 'POST',
      body: JSON.stringify({ requestId: key.requestId }),
      credentials: 'omit',
      redirect: 'error',
      cache: 'no-store',
      headers: { Authorization: 'Bearer admin-test-only', 'Content-Type': 'application/json' },
    });
    expect(String(url)).not.toContain('admin-test-only');
    await click(t('keysDismiss'));
    expect(view()).not.toContain(secret);
    expect(view()).not.toContain(t('keysCopied'));
    expect(view()).toContain(key.maskedPreview);
    expect(localStorage.setItem).not.toHaveBeenCalled();
    expect(sessionStorage.setItem).not.toHaveBeenCalled();
  });

  it('keeps the one-time key visible and reports failure when clipboard permission is denied', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('permission denied'));
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(empty())
      .mockResolvedValueOnce(Response.json({ key, createdSecret: secret }, { status: 201 }));
    await mount(fetchMock);
    await click(t('keysCreate'));
    await click(t('keysCopy'));
    expect(writeText).toHaveBeenCalledExactlyOnceWith(secret);
    expect(view()).toContain(t('keysCopyFailed'));
    expect(view()).toContain(secret);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    await click(t('keysDismiss'));
    expect(view()).not.toContain(secret);
    expect(view()).not.toContain(t('keysCopyFailed'));
  });

  it.each(['success', 'failure'] as const)(
    'ignores a delayed clipboard %s after the one-time secret is dismissed',
    async (outcome) => {
      let resolveCopy!: () => void;
      let rejectCopy!: (reason: Error) => void;
      const writeText = vi.fn(
        () =>
          new Promise<void>((resolve, reject) => {
            resolveCopy = resolve;
            rejectCopy = reject;
          })
      );
      vi.stubGlobal('navigator', { clipboard: { writeText } });
      const fetchMock = vi
        .fn<typeof fetch>()
        .mockResolvedValueOnce(empty())
        .mockResolvedValueOnce(Response.json({ key, createdSecret: secret }, { status: 201 }));
      await mount(fetchMock);
      await click(t('keysCreate'));
      await click(t('keysCopy'));
      expect(writeText).toHaveBeenCalledExactlyOnceWith(secret);
      expect(view()).toContain(secret);

      await click(t('keysDismiss'));
      expect(view()).not.toContain(secret);
      await act(async () => {
        if (outcome === 'success') resolveCopy();
        else rejectCopy(new Error('permission denied'));
      });
      expect(view()).not.toContain(secret);
      expect(view()).not.toContain(t('keysCopied'));
      expect(view()).not.toContain(t('keysCopyFailed'));
      expect(fetchMock).toHaveBeenCalledTimes(2);
    }
  );

  it('keeps the one-time key available for manual copy when Clipboard API is unavailable', async () => {
    vi.stubGlobal('navigator', {});
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(empty())
      .mockResolvedValueOnce(Response.json({ key, createdSecret: secret }, { status: 201 }));
    await mount(fetchMock);
    await click(t('keysCreate'));
    await click(t('keysCopy'));
    expect(view()).toContain(t('keysCopyFailed'));
    expect(view()).toContain(secret);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    await click(t('keysDismiss'));
    expect(view()).not.toContain(secret);
    expect(view()).not.toContain(t('keysCopyFailed'));
  });

  it('blocks double submission, reconciles lost reply with GET and never resends creation', async () => {
    let reject!: (reason: Error) => void;
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(empty())
      .mockReturnValueOnce(
        new Promise<Response>((_resolve, fail) => {
          reject = fail;
        })
      )
      .mockResolvedValueOnce(Response.json(key));
    await mount(fetchMock);
    await openCreate();
    await act(async () => {
      renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
      renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    // In-flight creation must not show the unknown-reply recovery copy before settling.
    expect(view()).not.toContain(t('keysUnknown'));
    await act(async () => reject(new Error('private transport error')));
    expect(view()).toContain(t('keysUnknown'));
    expect(view()).not.toContain('private transport error');
    expect(button(t('keysCreate')).props.disabled).toBe(true);
    await click(t('keysCheck'));
    expect(fetchMock.mock.calls[2][0]).toContain('/api-keys/requests/' + key.requestId);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    expect(view()).toContain(t('keysConfirmed'));
    expect(view()).not.toContain(secret);
  });

  it('retries only the same request after a not-found reconciliation and does not report unknown as success', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(empty())
      .mockResolvedValueOnce(new Response('', { status: 503 }))
      .mockResolvedValueOnce(new Response('', { status: 404 }))
      .mockResolvedValueOnce(Response.json({ key }));
    await mount(fetchMock);
    await click(t('keysCreate'));
    expect(view()).not.toContain(t('keysConfirmed'));
    await click(t('keysCheck'));
    expect(view()).toContain(t('keysUnknown'));
    await click(t('keysRetry'));
    expect(fetchMock.mock.calls[1][1]?.body).toBe(fetchMock.mock.calls[3][1]?.body);
    expect(view()).toContain(t('keysConfirmed'));
  });

  it('releases a hanging result check after its timeout and allows another check without a POST', async () => {
    vi.useFakeTimers();
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(empty())
      .mockRejectedValueOnce(new Error('lost creation reply'))
      .mockImplementationOnce(
        (_url, options) =>
          new Promise((_resolve, reject) => {
            options?.signal?.addEventListener('abort', () => reject(new Error('timed out')));
          })
      )
      .mockResolvedValueOnce(Response.json(key));
    await mount(fetchMock);
    await click(t('keysCreate'));
    await click(t('keysCheck'));
    expect(button(t('keysCheck')).props.disabled).toBe(true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15000);
    });
    expect(fetchMock.mock.calls[2][1]?.signal?.aborted).toBe(true);
    expect(button(t('keysCheck')).props.disabled).toBe(false);
    expect(view()).toContain(t('keysUnknown'));
    expect(view()).not.toContain(t('keysConfirmed'));
    await click(t('keysCheck'));
    expect(view()).toContain(t('keysConfirmed'));
    expect(fetchMock.mock.calls[2][0]).toBe(fetchMock.mock.calls[3][0]);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
  });

  it.each([401, 403])('retires Admin authority after %s', async (status) => {
    await mount(vi.fn<typeof fetch>().mockResolvedValue(new Response('', { status })));
    expect(unauthorized).toHaveBeenCalledTimes(1);
    expect(button(t('keysCreate')).props.disabled).toBe(true);
  });

  it('rejects malformed gatewayPending payloads and hides the pending banner once admitted', async () => {
    await mount(
      vi
        .fn<typeof fetch>()
        .mockResolvedValue(
          Response.json({ keys: [key], gatewayPending: 'false', revocationAvailable: true })
        )
    );
    expect(view()).not.toContain(key.id);
    expect(button(t('keysCreate')).props.disabled).toBe(true);
    expect(view()).toContain(t('keysUnavailable'));

    await mount(
      vi
        .fn<typeof fetch>()
        .mockResolvedValue(
          Response.json({ keys: [key], gatewayPending: false, revocationAvailable: true })
        )
    );
    expect(view()).toContain(key.id);
    expect(view()).not.toContain(k('unavailable'));
    expect(view()).not.toContain(k('unavailableHelp'));
  });

  it('creates a named key and recovers from partial name-save failure without re-creating the key', async () => {
    const pending = vi.fn();
    const named = { ...key, name: 'Prod CLI', nameRevision: 2 };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(empty())
      .mockResolvedValueOnce(Response.json({ key, createdSecret: secret }, { status: 201 }))
      .mockResolvedValueOnce(new Response('', { status: 503 }))
      .mockResolvedValueOnce(Response.json(key))
      .mockResolvedValueOnce(Response.json(named));
    await mount(fetchMock, pending);
    await openCreate();
    const nameInput = renderer.root.findByProps({ 'data-testid': 'create-key-name-input' });
    await act(async () => nameInput.props.onChange({ target: { value: 'Prod CLI' } }));
    await click(t('keysCreate'));

    // Key and one-time secret are preserved while name PUT failed
    expect(view()).toContain(secret);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'create-name-partial-failure' }).length
    ).toBeGreaterThan(0);
    expect(pending.mock.calls[pending.mock.calls.length - 1]).toEqual([true]);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);

    // Retry only checks + saves the name on the existing key without another POST
    const retryBtn = renderer.root.findByProps({ 'data-testid': 'retry-create-name' });
    await act(async () => retryBtn.props.onClick());
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(2);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'create-name-partial-failure' })
    ).toHaveLength(0);
    expect(view()).toContain('Prod CLI');
    expect(view()).toContain(secret);
    expect(pending).toHaveBeenLastCalledWith(true);
    await click(t('keysDismiss'));
    expect(pending).toHaveBeenLastCalledWith(false);
  });

  it.each([400, 404, 409])(
    'allows correcting a rejected creation name after %s without creating another key',
    async (status) => {
      const pending = vi.fn();
      const current = { ...key, name: 'Concurrent name', nameRevision: 3 };
      const named = { ...current, name: 'Corrected CLI', nameRevision: 4 };
      const fetchMock = vi
        .fn<typeof fetch>()
        .mockResolvedValueOnce(empty())
        .mockResolvedValueOnce(Response.json({ key, createdSecret: secret }, { status: 201 }))
        .mockResolvedValueOnce(new Response('', { status }))
        .mockResolvedValueOnce(Response.json(current))
        .mockResolvedValueOnce(Response.json(named));
      await mount(fetchMock, pending);
      await setCreateName('Prod\tCLI');
      await click(t('keysCreate'));
      expect(pending).toHaveBeenLastCalledWith(true);
      expect(
        renderer.root.findByProps({ 'data-testid': 'create-key-name-input' }).props.disabled
      ).toBe(false);
      expect(button(t('keysCreate')).props.disabled).toBe(true);

      await setCreateName('Corrected CLI');
      await click(k('retrySaveName'));
      expect(fetchMock.mock.calls[3][1]?.method).toBe('GET');
      expect(JSON.parse(String(fetchMock.mock.calls[4][1]?.body))).toEqual({
        name: 'Corrected CLI',
        nameRevision: 3,
      });
      expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
      expect(view()).toContain('Corrected CLI');
      expect(view()).toContain(secret);
      expect(pending).toHaveBeenLastCalledWith(true);
      await click(t('keysDismiss'));
      expect(pending).toHaveBeenLastCalledWith(false);
      expect(renderer.root.findAllByProps({ 'data-testid': 'create-key-name-input' })).toHaveLength(
        0
      );
    }
  );

  it('keeps an unknown name write locked until discard can read its saved result', async () => {
    const pending = vi.fn();
    const named = { ...key, name: 'Production', nameRevision: 2 };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(empty())
      .mockResolvedValueOnce(Response.json({ key, createdSecret: secret }, { status: 201 }))
      .mockRejectedValueOnce(new Error('name reply lost after commit'))
      .mockResolvedValueOnce(new Response('', { status: 503 }))
      .mockResolvedValueOnce(Response.json(named));
    await mount(fetchMock, pending);
    await setCreateName('Production');
    await click(t('keysCreate'));
    await click(k('discardCreateName'));
    expect(pending).toHaveBeenLastCalledWith(true);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'create-name-partial-failure' })
    ).toHaveLength(1);

    await click(k('discardCreateName'));
    expect(pending).toHaveBeenLastCalledWith(true);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'create-name-partial-failure' })
    ).toHaveLength(0);
    expect(fetchMock.mock.calls.map(([, init]) => init?.method)).toEqual([
      'GET',
      'POST',
      'PUT',
      'GET',
      'GET',
    ]);
    expect(view()).toContain('Production');
    expect(view()).toContain(secret);
    await click(t('keysDismiss'));
    expect(pending).toHaveBeenLastCalledWith(false);
    expect(button(t('keysCreate')).props.disabled).toBe(false);
    expect(renderer.root.findAllByProps({ 'data-testid': 'create-key-name-input' })).toHaveLength(
      0
    );
  });

  it('reconciles an already-saved creation name without repeating its PUT', async () => {
    const pending = vi.fn();
    const named = { ...key, name: 'Production', nameRevision: 2 };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(empty())
      .mockResolvedValueOnce(Response.json({ key, createdSecret: secret }, { status: 201 }))
      .mockRejectedValueOnce(new Error('name reply lost after commit'))
      .mockResolvedValueOnce(Response.json(named));
    await mount(fetchMock, pending);
    await setCreateName('Production');
    await click(t('keysCreate'));
    await click(k('retrySaveName'));
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
    expect(view()).toContain(secret);
    expect(pending).toHaveBeenLastCalledWith(true);
    await click(t('keysDismiss'));
    expect(pending).toHaveBeenLastCalledWith(false);
  });

  it.each(['save', 'discard'] as const)(
    'retains the requested name after lost creation reply and lets the user %s it on the same key',
    async (action) => {
      const pending = vi.fn();
      const named = { ...key, name: 'Production', nameRevision: 2 };
      const fetchMock = vi
        .fn<typeof fetch>()
        .mockResolvedValueOnce(empty())
        .mockRejectedValueOnce(new Error('creation reply lost after commit'))
        .mockResolvedValueOnce(Response.json(key))
        .mockResolvedValueOnce(Response.json(key))
        .mockResolvedValueOnce(Response.json(named));
      await mount(fetchMock, pending);
      await setCreateName('Production');
      await click(t('keysCreate'));
      await click(t('keysCheck'));
      expect(view()).toContain(k('createNameNotSaved'));
      expect(view()).not.toContain(t('keysConfirmed'));
      expect(pending).toHaveBeenLastCalledWith(true);
      expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(0);
      expect(
        renderer.root.findByProps({ 'data-testid': 'create-key-name-input' }).props.value
      ).toBe('Production');

      await click(k(action === 'save' ? 'retrySaveName' : 'discardCreateName'));
      expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
      expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(
        action === 'save' ? 1 : 0
      );
      if (action === 'save') expect(fetchMock.mock.calls[4][0]).toContain('/api-keys/' + key.id);
      expect(pending).toHaveBeenLastCalledWith(false);
      expect(view()).not.toContain(secret);
      expect(renderer.root.findAllByProps({ 'data-testid': 'create-key-name-input' })).toHaveLength(
        0
      );
    }
  );

  it.each(['not_applied', 'effect_unknown'] as const)(
    'reports unverified Gateway runtime count for %s metadata and verified status for effective_observed',
    async (runtimeState) => {
      const fetchMock = vi
        .fn<typeof fetch>()
        .mockResolvedValueOnce(
          Response.json({
            keys: [{ ...key, runtimeState }],
            gatewayPending: true,
            revocationAvailable: false,
          })
        )
        .mockResolvedValueOnce(
          Response.json({
            keys: [{ ...key, runtimeState: 'effective_observed' }],
            gatewayPending: false,
            revocationAvailable: true,
            presenceUnverified: false,
          })
        )
        .mockResolvedValueOnce(
          Response.json({
            keys: [{ ...key, runtimeState: 'effective_observed' }],
            gatewayPending: false,
            revocationAvailable: true,
            presenceUnverified: true,
          })
        );
      await mount(withIdleKeyOperation(fetchMock));
      const runtime = renderer.root.findByProps({ 'data-testid': 'key-summary-runtime' });
      expect(runtime.findByType('strong').findByType('span').children).toEqual([
        `0/1 ${k('summaryRuntimeVerifiedCount')}`,
      ]);
      expect(
        renderer.root.findByProps({ 'data-testid': 'key-summary-saved' }).findByType('strong')
          .children
      ).toEqual(['1']);

      await click(t('refresh'));
      expect(
        renderer.root
          .findByProps({ 'data-testid': 'key-summary-runtime' })
          .findByType('strong')
          .findByType('span').children
      ).toEqual([k('summaryRuntimeAllActive')]);
      expect(
        renderer.root.findAllByProps({ 'data-testid': 'keys-presence-unverified' })
      ).toHaveLength(0);

      await click(t('refresh'));
      expect(
        renderer.root.findAllByProps({ 'data-testid': 'keys-presence-unverified' })
      ).toHaveLength(1);
      expect(
        renderer.root
          .findByProps({ 'data-testid': 'key-summary-runtime' })
          .findByType('strong')
          .findByType('span').children
      ).toEqual([`1/1 ${k('summaryRuntimeVerifiedCount')}`]);
    }
  );

  it('preserves filter, search, and cards view mode when returning from key details and preselects replacement target', async () => {
    const activeA = { ...key, id: '1'.repeat(32), name: 'Alpha Gateway' };
    const activeB = {
      ...key,
      id: '2'.repeat(32),
      requestId: '3'.repeat(32),
      maskedPreview: 'cpamp_sk_…8888',
      name: 'Beta Worker',
    };
    const replacedC = {
      ...key,
      id: '4'.repeat(32),
      requestId: '5'.repeat(32),
      maskedPreview: 'cpamp_sk_…9999',
      name: 'Alpha Old',
      lifecycle: 'superseded' as const,
    };
    const fetchMock = vi.fn<typeof fetch>().mockImplementation((url) => {
      const target = String(url);
      if (target.endsWith('/operations/latest'))
        return Promise.resolve(
          Response.json({
            operation: null,
            nextStep: '',
            runtimeState: 'effect_unknown',
            expiresAtMs: 0,
          })
        );
      if (target.endsWith('/api-keys/' + activeA.id))
        return Promise.resolve(Response.json(activeA));
      return Promise.resolve(
        Response.json({
          keys: [activeA, activeB, replacedC],
          gatewayPending: true,
          revocationAvailable: true,
        })
      );
    });
    await mount(fetchMock);

    // Summary strip shows real counts and explicit Unavailable usage
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-summary-saved' }).findByType('strong')
        .children
    ).toEqual(['2']);
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-summary-replaced' }).findByType('strong')
        .children
    ).toEqual(['1']);
    expect(view()).toContain(k('summaryUsageValue'));

    // Switch to Saved filter, search "Alpha", and toggle Cards view
    await act(async () =>
      renderer.root
        .findByProps({ 'aria-label': k('lifecycle') })
        .props.onChange({ target: { value: 'saved' } })
    );
    const searchInput = renderer.root.findByProps({ type: 'search' });
    await act(async () => searchInput.props.onChange({ target: { value: 'Alpha' } }));
    const cardsBtn = renderer.root.findByProps({ 'data-testid': 'key-view-cards' });
    await act(async () => cardsBtn.props.onClick());
    expect(renderer.root.findByType('table').props.className).toContain('is-cards-view');
    expect(renderer.root.findByType('tbody').findAllByType('tr')).toHaveLength(1);

    // Open details and return: filter, search, and cards view remain intact
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'open-key-' + activeA.id }).props.onClick()
    );
    await click(t('keysCloseDetails'));
    expect(renderer.root.findByProps({ type: 'search' }).props.value).toBe('Alpha');
    expect(renderer.root.findByType('table').props.className).toContain('is-cards-view');
    expect(renderer.root.findByType('tbody').findAllByType('tr')).toHaveLength(1);

    // Quick replace preselects target, keeper, and replace mode in FreshKeyRevocation
    await act(async () =>
      renderer.root
        .findByType(DropdownMenu)
        .props.items.find((item: { key: string }) => item.key === 'replace')
        .onClick()
    );
    await click(k('replacementReady'));
    const selects = renderer.root
      .findAllByType('select')
      .filter((item) => item.props['aria-label'] !== k('lifecycle'));
    expect(selects[0].props.value).toBe(activeA.id);
    expect(selects[1].props.value).toBe(activeB.id);
    expect(selects[2].props.value).toBe('replace');
  });
  it('renders the usage column with lower-bound and unknown states instead of fake zeroes', async () => {
    const lastUsedAtMs = Date.UTC(2026, 0, 2, 3, 4);
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json({
      keys: [key],
      gatewayPending: true,
      revocationAvailable: false,
    }));
    await mount(fetchMock, vi.fn(), {
      fromMs: 1,
      toMs: 2,
      coverage: { state: 'partial', reason: 'projection_catching_up' },
      items: [{ keyId: key.id, calls: 3, lastUsedAtMs, coverage: { state: 'partial', reason: 'projection_catching_up' } }],
    });
    expect(view()).toContain(k('usageColumn'));
    renderer.root.findByProps({ 'data-testid': 'key-usage-' + key.id });
    expect(view()).toContain('≥3');
    expect(view()).toContain(k('usageStatePartial'));
    // The last-used instant comes from this key's summary item.
    expect(view()).toContain(String(new Date(lastUsedAtMs).getFullYear()));
    expect(view()).toContain(k('usageLastUsed'));

    await act(async () => {
      renderer.unmount();
    });
    const unknownFetch = vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json({
      keys: [key],
      gatewayPending: true,
      revocationAvailable: false,
    }));
    await mount(unknownFetch, vi.fn(), {
      fromMs: 1,
      toMs: 2,
      coverage: { state: 'unavailable', reason: 'binding_missing' },
      items: [{ keyId: key.id, calls: 0, lastUsedAtMs: null, coverage: { state: 'unavailable', reason: 'binding_missing' } }],
    });
    renderer.root.findByProps({ 'data-testid': 'key-usage-' + key.id });
    expect(view()).toContain('—');
    expect(view()).toContain(k('usageStateUnavailable'));
  });
  it('shows the summary total as a lower bound or hides it when a key is incomplete', async () => {
    const lastUsedAtMs = Date.UTC(2026, 0, 2, 3, 4);
    const summaryCard = () =>
      renderer.root
        .findByProps({ 'data-testid': 'key-summary-usage' })
        .findAllByType('span')
        .flatMap((node) => (Array.isArray(node.props.children) ? node.props.children : [node.props.children]))
        .filter((child): child is string => typeof child === 'string')
        .join(' | ');
    const keysOnly = () =>
      vi.fn<typeof fetch>().mockImplementation(() =>
        Promise.resolve(Response.json({ keys: [key], gatewayPending: true, revocationAvailable: false }))
      );
    // One proven key plus one key without attribution: the strip total is a
    // lower bound, never the exact sum.
    await mount(keysOnly(), vi.fn(), {
      fromMs: 1,
      toMs: 2,
      coverage: { state: 'partial', reason: 'binding_missing' },
      items: [
        { keyId: key.id, calls: 5, lastUsedAtMs, coverage: { state: 'complete', reason: 'observed' } },
        { keyId: 'd'.repeat(32), calls: 0, lastUsedAtMs: null, coverage: { state: 'unavailable', reason: 'binding_missing' } },
      ],
    });
    expect(summaryCard()).toContain('≥5');
    expect(summaryCard()).toContain(k('usageStatePartial'));

    // An unobserved key makes the total unprovable, so the strip hides it.
    await act(async () => {
      renderer.unmount();
    });
    await mount(keysOnly(), vi.fn(), {
      fromMs: 1,
      toMs: 2,
      coverage: { state: 'unknown', reason: 'window_unobserved' },
      items: [
        { keyId: key.id, calls: 5, lastUsedAtMs, coverage: { state: 'complete', reason: 'observed' } },
        { keyId: 'e'.repeat(32), calls: 0, lastUsedAtMs: null, coverage: { state: 'unknown', reason: 'window_unobserved' } },
      ],
    });
    expect(summaryCard()).toContain('—');
    expect(summaryCard()).not.toContain('≥5');
    expect(summaryCard()).toContain(k('usageStateUnknown'));
  });
  it('keeps the key list usable when the usage read fails', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json({
      keys: [key],
      gatewayPending: true,
      revocationAvailable: false,
    }));
    await mount(fetchMock, vi.fn(), new Response('failed', { status: 503 }));
    expect(view()).toContain(key.id);
    expect(view()).toContain(k('summaryUsageValue'));
    expect(view()).toContain(k('usageStateUnknown'));
    expect(view()).not.toContain('≥');
  });


  it('clears usage-originated external blocker after retry succeeds without clearing key-list blockers', async () => {
    let usagePostCount = 0;
    const usageBase = {
      cpaVersion: { version: 'v8.0.55', support: 'supported', writeBlocked: false },
    };
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys')) {
        return Promise.resolve(
          Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'unknown', reason: 'usage_read_failed' },
            items: [],
          })
        );
      }
      if (url.endsWith('/external/usage') && init?.method === 'POST') {
        usagePostCount += 1;
        if (usagePostCount === 1) {
          return Promise.resolve(
            Response.json({
              ...usageBase,
              statisticsEnabled: true,
              collector: 'stopped',
              collectionStarted: false,
              blockedReason: 'external_cpa_authority_unavailable',
            })
          );
        }
        return Promise.resolve(
          Response.json({
            ...usageBase,
            statisticsEnabled: true,
            collector: 'running',
            collectionStarted: true,
            blockedReason: '',
          })
        );
      }
      if (url.endsWith('/external/usage')) {
        return Promise.resolve(
          Response.json({
            ...usageBase,
            statisticsEnabled: false,
            collector: 'stopped',
            collectionStarted: false,
            blockedReason: 'usage_statistics_disabled',
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });

    await act(async () => {
      renderer = create(
        <FreshApiKeys
          client={createPublicApiClient(() => context, fetchMock)}
          credential="admin-test-only"
          onUnauthorized={unauthorized}
          onCreationPending={vi.fn()}
          externalUsage={true}
        />
      );
    });

    const enableBtn = renderer.root.findByProps({ 'data-testid': 'external-usage-enable' });
    await act(async () => enableBtn.props.onClick());
    const confirmBtn = renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' });
    await act(async () => confirmBtn.props.onClick());

    // First 200 with authority_unavailable blocker shows both page blocker and panel retry.
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-authority_unavailable' })
    ).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-usage-confirm-failed' })
    ).toHaveLength(1);

    // Retry succeeds -> page-level authority blocker disappears while collector reports running.
    const retryBtn = renderer.root.findByProps({ 'data-testid': 'external-usage-confirm-retry' });
    await act(async () => retryBtn.props.onClick());
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-authority_unavailable' })
    ).toHaveLength(0);
    const collectorText = renderer.root
      .findByProps({ 'data-testid': 'external-usage-collector' })
      .findByType('dd').children;
    expect(collectorText.join('')).not.toBe('');

    await act(async () => renderer.unmount());

    // A key-list refusal blocker (from GET /api-keys) must NOT be cleared by a healthy usage read.
    const keyListRefusedFetch = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.endsWith('/api-keys')) {
        return Promise.resolve(
          Response.json(
            { code: 'external_management_key_rejected', detail: '401' },
            { status: 409 }
          )
        );
      }
      if (url.endsWith('/external/usage')) {
        return Promise.resolve(
          Response.json({
            ...usageBase,
            statisticsEnabled: true,
            collector: 'running',
            collectionStarted: true,
            blockedReason: '',
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });

    await act(async () => {
      renderer = create(
        <FreshApiKeys
          client={createPublicApiClient(() => context, keyListRefusedFetch)}
          credential="admin-test-only"
          onUnauthorized={unauthorized}
          onCreationPending={vi.fn()}
          externalUsage={true}
        />
      );
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-key_rejected' })
    ).toHaveLength(1);
  });

  // Ordering 1: the panel's own read is still pending when the operator confirms
  // collection. That confirmation IS the newest state of the installation, so the
  // retired read's failure must not wipe the running facts it proved nor put an
  // authority blocker over them.
  it('does not let a read retired by a successful confirmation repaint a blocker', async () => {
    const usageBase = {
      cpaVersion: { version: 'v8.0.55', support: 'supported', writeBlocked: false },
    };
    const disabled = {
      ...usageBase,
      statisticsEnabled: false,
      collector: 'stopped',
      collectionStarted: false,
      blockedReason: 'usage_statistics_disabled',
    };
    const running = {
      ...usageBase,
      statisticsEnabled: true,
      collector: 'running',
      collectionStarted: true,
      blockedReason: '',
    };
    let settlePendingRead: (response: Response) => void = () => undefined;
    let usageGetCount = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys')) {
        return Promise.resolve(
          Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'unknown', reason: 'usage_read_failed' },
            items: [],
          })
        );
      }
      if (url.endsWith('/external/usage') && init?.method === 'POST') {
        return Promise.resolve(Response.json(running));
      }
      if (url.endsWith('/external/usage')) {
        usageGetCount += 1;
        // The first read settles so the enable action is offered; the read issued
        // with the confirmation is the one held open.
        if (usageGetCount === 1) return Promise.resolve(Response.json(disabled));
        return new Promise<Response>((resolve) => {
          settlePendingRead = resolve;
        });
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });

    const client = createPublicApiClient(() => context, fetchMock);
    const page = (reloadToken: number) => (
      <FreshApiKeys
        client={client}
        credential="admin-test-only"
        onUnauthorized={unauthorized}
        onCreationPending={vi.fn()}
        externalUsage={true}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(page(0));
    });
    const enableBtn = renderer.root.findByProps({ 'data-testid': 'external-usage-enable' });
    await act(async () => enableBtn.props.onClick());
    // A reload starts a read that is still pending when the operator confirms.
    await act(async () => {
      renderer.update(page(1));
    });
    expect(usageGetCount).toBe(2);

    const confirmBtn = renderer.root.findByProps({ 'data-testid': 'external-usage-confirm' });
    await act(async () => confirmBtn.props.onClick());
    expect(view()).toContain(x('usageCollectorRunning'));

    // The read issued before the confirmation now fails with the typed refusal.
    await act(async () => {
      settlePendingRead(
        Response.json({ code: 'external_cpa_authority_unavailable' }, { status: 503 })
      );
    });

    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-authority_unavailable' })
    ).toHaveLength(0);
    expect(view()).not.toContain(x('rebindUnavailableHelp'));
    expect(view()).toContain(x('usageCollectorRunning'));
  });

  // A rebind reload can start while a normal read is still in flight. When that
  // older read answers first its result is dropped, but it must also release
  // nothing: `inFlight` and `busy` belong to the read that replaced it, or the page
  // reports itself idle and lets a mutation interleave with a pending read whose
  // older snapshot would then erase the key it just created.
  it('keeps the page busy when a superseded read settles first', async () => {
    let settleFirstRead: (response: Response) => void = () => undefined;
    let settleSecondRead: (response: Response) => void = () => undefined;
    let keyReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        keyReads += 1;
        if (keyReads === 1)
          return new Promise<Response>((resolve) => {
            settleFirstRead = resolve;
          });
        return new Promise<Response>((resolve) => {
          settleSecondRead = resolve;
        });
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'known', reason: 'observed' },
            items: [],
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    const page = (reloadToken: number) => (
      <FreshApiKeys
        client={client}
        credential="admin-test-only"
        onUnauthorized={unauthorized}
        onCreationPending={vi.fn()}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(page(0));
    });
    // The rebind re-reads while the first read is still open.
    await act(async () => {
      renderer.update(page(1));
    });
    expect(keyReads).toBe(2);

    // The superseded read answers first with a list that predates anything.
    await act(async () => {
      settleFirstRead(
        Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
      );
    });

    // Its answer and its release both belong to the newer read.
    expect(renderer.root.findByProps({ 'aria-busy': true })).toBeDefined();
    expect(button(t('refresh')).props.disabled).toBe(true);
    expect(button(t('keysCreate')).props.disabled).toBe(true);

    await act(async () => {
      settleSecondRead(
        Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
      );
    });
    // The newer read settles the page: one list request per read, no extra GET,
    // and the key it reports is the only list statement the page holds.
    expect(keyReads).toBe(2);
    expect(renderer.root.findByProps({ 'aria-busy': false })).toBeDefined();
    expect(button(t('refresh')).props.disabled).toBe(false);
    expect(view()).toContain(key.id);
  });

  // The page's own read chain carries the same ownership rule as the usage panel.
  // A rebind re-reads it, and the answer the previous read is still waiting on
  // describes the installation before the fix: publishing it would repaint the
  // blocker the rebind resolved.
  it('does not repaint a key-list blocker from an api-keys read issued before the rebind', async () => {
    let settleStaleRead: (response: Response) => void = () => undefined;
    let keyReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        keyReads += 1;
        if (keyReads === 1)
          return new Promise<Response>((resolve) => {
            settleStaleRead = resolve;
          });
        return Promise.resolve(
          Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'known', reason: 'observed' },
            items: [],
          })
        );
      }
      if (url.endsWith('/external/usage')) {
        return Promise.resolve(
          Response.json({
            statisticsEnabled: true,
            collector: 'running',
            collectionStarted: true,
            blockedReason: '',
            cpaVersion: { version: 'v8.0.55', support: 'supported', writeBlocked: false },
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    const page = (reloadToken: number) => (
      <FreshApiKeys
        client={client}
        credential="admin-test-only"
        onUnauthorized={unauthorized}
        onCreationPending={vi.fn()}
        externalUsage={true}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(page(0));
    });
    expect(keyReads).toBe(1);

    // The rebind succeeded and the shell re-reads the page.
    await act(async () => {
      renderer.update(page(1));
    });
    // The retired read now answers with the typed key refusal.
    await act(async () => {
      settleStaleRead(
        Response.json(
          { code: 'external_management_key_rejected', detail: '401' },
          { status: 409 }
        )
      );
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-key_rejected' })
    ).toHaveLength(0);
    expect(view()).not.toContain(x('blockerKeyRejectedTitle'));
    expect(view()).toContain(key.id);
  });

  // An expired session reported by a retired *usage-summary* read is not a fact
  // about the current one: the rebind already proved the connection works. The
  // summary is the read the key list hands off to, so it is the one held open.
  it('does not end the session for an authentication failure of a retired page read', async () => {
    let settleStaleSummary: (response: Response) => void = () => undefined;
    let summaryReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(
          Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        summaryReads += 1;
        if (summaryReads === 1)
          return new Promise<Response>((resolve) => {
            settleStaleSummary = resolve;
          });
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'known', reason: 'observed' },
            items: [],
          })
        );
      }
      if (url.endsWith('/external/usage')) {
        return Promise.resolve(
          Response.json({
            statisticsEnabled: true,
            collector: 'running',
            collectionStarted: true,
            blockedReason: '',
            cpaVersion: { version: 'v8.0.55', support: 'supported', writeBlocked: false },
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    const page = (reloadToken: number) => (
      <FreshApiKeys
        client={client}
        credential="admin-test-only"
        onUnauthorized={unauthorized}
        onCreationPending={vi.fn()}
        externalUsage={true}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(page(0));
    });
    expect(summaryReads).toBe(1);
    // The rebind re-reads the page while the first summary is still open.
    await act(async () => {
      renderer.update(page(1));
    });
    expect(summaryReads).toBe(2);

    // The retired summary now answers as an expired session.
    await act(async () => {
      settleStaleSummary(Response.json({ code: 'unauthorized' }, { status: 401 }));
    });
    expect(unauthorized).not.toHaveBeenCalled();
    expect(view()).toContain(key.id);
  });

  // Regression: a rebind resolves the blocker, and the shell re-reads the page by
  // bumping `reloadToken`. The usage panel must not repaint the blocker with an
  // answer that belongs to the request it issued *before* the rebind: the page
  // would show the fix and the refusal it resolved in the same frame.
  it('does not repaint an External blocker from a usage read issued before the rebind', async () => {
    const usageBase = {
      cpaVersion: { version: 'v8.0.55', support: 'supported', writeBlocked: false },
    };
    // The pre-rebind usage read is held open so the test chooses how it settles.
    let settleStaleRead: (response: Response) => void = () => undefined;
    let usageGetCount = 0;
    const health = {
      ...usageBase,
      statisticsEnabled: true,
      collector: 'running',
      collectionStarted: true,
      blockedReason: '',
    };
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.endsWith('/api-keys')) {
        return Promise.resolve(
          Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'unknown', reason: 'usage_read_failed' },
            items: [],
          })
        );
      }
      if (url.endsWith('/external/usage')) {
        usageGetCount += 1;
        if (usageGetCount === 1)
          return new Promise<Response>((resolve) => {
            settleStaleRead = resolve;
          });
        return Promise.resolve(Response.json(health));
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    const page = (reloadToken: number) => (
      <FreshApiKeys
        client={client}
        credential="admin-test-only"
        onUnauthorized={unauthorized}
        onCreationPending={vi.fn()}
        externalUsage={true}
        reloadToken={reloadToken}
      />
    );

    await act(async () => {
      renderer = create(page(0));
    });
    expect(usageGetCount).toBe(1);

    // The rebind succeeded: the shell re-reads the page and the usage panel.
    await act(async () => {
      renderer.update(page(1));
    });
    // The retired read answers with the typed authority refusal, the very code
    // that paints this page's blocker when it belongs to the current state.
    await act(async () => {
      settleStaleRead(
        Response.json({ code: 'external_cpa_authority_unavailable' }, { status: 503 })
      );
    });

    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-authority_unavailable' })
    ).toHaveLength(0);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-key_rejected' })
    ).toHaveLength(0);
    expect(view()).not.toContain(x('rebindUnavailableHelp'));
    // The read that replaced it is what the page reports from.
    expect(view()).toContain(x('usageCollectorRunning'));
  });

  it('keeps the page busy when createKey settles while a rebind read is still open', async () => {
    const created = {
      ...key,
      id: 'f'.repeat(32),
      requestId: 'b'.repeat(32),
      maskedPreview: 'cpamp_sk_…cccc',
      runtimeState: 'effective_observed' as const,
    };
    let settlePost: (response: Response) => void = () => undefined;
    let settleRebindRead: (response: Response) => void = () => undefined;
    let keyReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        return new Promise<Response>((resolve) => {
          settlePost = resolve;
        });
      }
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        keyReads += 1;
        if (keyReads === 1) {
          return Promise.resolve(
            Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
          );
        }
        if (keyReads === 2) {
          return new Promise<Response>((resolve) => {
            settleRebindRead = resolve;
          });
        }
        return Promise.resolve(
          Response.json({ keys: [key, created], gatewayPending: false, revocationAvailable: false })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'known', reason: 'observed' },
            items: [],
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    const page = (reloadToken: number) => (
      <FreshApiKeys
        client={client}
        credential="admin-test-only"
        onUnauthorized={unauthorized}
        onCreationPending={vi.fn()}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(page(0));
    });
    await openCreate();
    act(() => {
      void renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
    });
    await act(async () => {
      renderer.update(page(1));
    });
    expect(keyReads).toBe(2);

    // Mutation settles while rebind read is still open: busy lock must stay held.
    await act(async () => {
      settlePost(Response.json({ key: created, createdSecret: secret }, { status: 201 }));
    });
    expect(renderer.root.findByProps({ 'aria-busy': true })).toBeDefined();
    expect(button(t('refresh')).props.disabled).toBe(true);
    expect(button(t('keysCreate')).props.disabled).toBe(true);

    await act(async () => {
      settleRebindRead(
        Response.json({ keys: [key, created], gatewayPending: false, revocationAvailable: false })
      );
    });
    expect(renderer.root.findByProps({ 'aria-busy': false })).toBeDefined();
    expect(button(t('refresh')).props.disabled).toBe(false);
    expect(view()).toContain(key.id);
    expect(view()).toContain(created.id);
  });

  it('keeps the page busy when a rebind read settles while createKey is still open', async () => {
    const created = {
      ...key,
      id: 'f'.repeat(32),
      requestId: 'b'.repeat(32),
      maskedPreview: 'cpamp_sk_…cccc',
      runtimeState: 'effective_observed' as const,
    };
    let settlePost: (response: Response) => void = () => undefined;
    let keyReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        return new Promise<Response>((resolve) => {
          settlePost = resolve;
        });
      }
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        keyReads += 1;
        return Promise.resolve(
          Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'known', reason: 'observed' },
            items: [],
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    const page = (reloadToken: number) => (
      <FreshApiKeys
        client={client}
        credential="admin-test-only"
        onUnauthorized={unauthorized}
        onCreationPending={vi.fn()}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(page(0));
    });
    await openCreate();
    act(() => {
      void renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
    });
    // Rebind read settles immediately while POST is still pending.
    await act(async () => {
      renderer.update(page(1));
    });
    expect(keyReads).toBe(2);
    expect(renderer.root.findByProps({ 'aria-busy': true })).toBeDefined();
    expect(button(t('refresh')).props.disabled).toBe(true);

    await act(async () => {
      settlePost(Response.json({ key: created, createdSecret: secret }, { status: 201 }));
    });
    expect(renderer.root.findByProps({ 'aria-busy': false })).toBeDefined();
    expect(button(t('refresh')).props.disabled).toBe(false);
    expect(view()).toContain(created.id);
  });

  it('re-reads and keeps the full post-rebind key list when reconcile resolves during a rebind read', async () => {
    const reconciled = {
      ...key,
      id: 'c'.repeat(32),
      requestId: 'b'.repeat(32),
      name: 'Recovered Key',
      runtimeState: 'effective_observed' as const,
    };
    const postRebindPeer = {
      ...key,
      id: 'd'.repeat(32),
      requestId: 'e'.repeat(32),
      name: 'Post-Rebind Peer',
    };
    let settleReconcile: (response: Response) => void = () => undefined;
    let settleRebindRead: (response: Response) => void = () => undefined;
    let keyReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        return Promise.reject(new Error('lost reply'));
      }
      if (url.includes('/api-keys/requests/')) {
        return new Promise<Response>((resolve) => {
          settleReconcile = resolve;
        });
      }
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        keyReads += 1;
        if (keyReads === 1) {
          return Promise.resolve(
            Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
          );
        }
        if (keyReads === 2) {
          return new Promise<Response>((resolve) => {
            settleRebindRead = resolve;
          });
        }
        return Promise.resolve(
          Response.json({
            keys: [key, postRebindPeer, reconciled],
            gatewayPending: false,
            revocationAvailable: false,
          })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'known', reason: 'observed' },
            items: [],
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    const page = (reloadToken: number) => (
      <FreshApiKeys
        client={client}
        credential="admin-test-only"
        onUnauthorized={unauthorized}
        onCreationPending={vi.fn()}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(page(0));
    });
    await click(t('keysCreate'));
    act(() => {
      void button(t('keysCheck')).props.onClick();
    });
    // Rebind starts while reconcile is pending.
    await act(async () => {
      renderer.update(page(1));
    });
    // Reconcile resolves first, bumping mutation revision while rebind read is open.
    await act(async () => {
      settleReconcile(Response.json(reconciled));
    });
    // Rebind read settles with the full post-rebind list.
    await act(async () => {
      settleRebindRead(
        Response.json({
          keys: [key, postRebindPeer, reconciled],
          gatewayPending: false,
          revocationAvailable: false,
        })
      );
    });
    expect(view()).toContain(key.id);
    expect(view()).toContain(postRebindPeer.id);
    expect(view()).toContain(reconciled.id);
    expect(renderer.root.findByProps({ 'aria-busy': false })).toBeDefined();
  });

  it('does not let an overlapping list snapshot overwrite a saved creation name', async () => {
    const unnamed = { ...key, id: 'f'.repeat(32), requestId: 'b'.repeat(32), name: '', nameRevision: 1, runtimeState: 'effective_observed' as const };
    const named = { ...unnamed, name: 'Prod CLI', nameRevision: 2 };
    let settleNamePut: (response: Response) => void = () => undefined;
    let settleRebindRead: (response: Response) => void = () => undefined;
    let keyReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        return Promise.resolve(Response.json({ key: unnamed, createdSecret: secret }, { status: 201 }));
      }
      if (url.endsWith('/api-keys/' + unnamed.id) && init?.method === 'PUT') {
        return new Promise<Response>((resolve) => {
          settleNamePut = resolve;
        });
      }
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        keyReads += 1;
        if (keyReads === 1) {
          return Promise.resolve(
            Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
          );
        }
        if (keyReads === 2) {
          return new Promise<Response>((resolve) => {
            settleRebindRead = resolve;
          });
        }
        return Promise.resolve(
          Response.json({ keys: [key, named], gatewayPending: false, revocationAvailable: false })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'known', reason: 'observed' },
            items: [],
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    const page = (reloadToken: number) => (
      <FreshApiKeys
        client={client}
        credential="admin-test-only"
        onUnauthorized={unauthorized}
        onCreationPending={vi.fn()}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(page(0));
    });
    await setCreateName('Prod CLI');
    await act(async () => {
      void renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
    });
    // Rebind read starts while name PUT is in flight.
    await act(async () => {
      renderer.update(page(1));
    });
    // Name PUT resolves first, then the pre-PUT list snapshot arrives.
    await act(async () => {
      settleNamePut(Response.json(named));
    });
    await act(async () => {
      settleRebindRead(
        Response.json({ keys: [key, unnamed], gatewayPending: false, revocationAvailable: false })
      );
    });
    expect(view()).toContain('Prod CLI');
  });

  it('does not let an overlapping list snapshot overwrite a key name saved in details', async () => {
    const renamed = { ...key, name: 'Renamed Key', nameRevision: 2 };
    let settleRenamePut: (response: Response) => void = () => undefined;
    let settleRebindRead: (response: Response) => void = () => undefined;
    let keyReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys/' + key.id) && init?.method === 'PUT') {
        return new Promise<Response>((resolve) => {
          settleRenamePut = resolve;
        });
      }
      if (url.endsWith('/api-keys/' + key.id) && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(Response.json(key));
      }
      if (url.endsWith('/api-keys/' + key.id + '/usage')) {
        return Promise.resolve(
          Response.json({
            keyId: key.id,
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'unknown', reason: 'usage_read_failed' },
            totals: null,
            series: [],
          })
        );
      }
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        keyReads += 1;
        if (keyReads === 1) {
          return Promise.resolve(
            Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
          );
        }
        if (keyReads === 2) {
          return new Promise<Response>((resolve) => {
            settleRebindRead = resolve;
          });
        }
        return Promise.resolve(
          Response.json({ keys: [renamed], gatewayPending: false, revocationAvailable: false })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'known', reason: 'observed' },
            items: [],
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    const page = (reloadToken: number) => (
      <FreshApiKeys
        client={client}
        credential="admin-test-only"
        onUnauthorized={unauthorized}
        onCreationPending={vi.fn()}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(page(0));
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'open-key-' + key.id }).props.onClick();
    });
    await click(k('rename'));
    await act(async () => {
      renderer.root
        .findByProps({ id: 'key-name' })
        .props.onChange({ target: { value: 'Renamed Key' } });
    });
    act(() => {
      void renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
    });
    await act(async () => {
      renderer.update(page(1));
    });
    expect(keyReads).toBe(2);
    await act(async () => {
      settleRenamePut(Response.json(renamed));
    });
    await act(async () => {
      settleRebindRead(
        Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
      );
    });
    await click(t('keysCloseDetails'));
    expect(view()).toContain('Renamed Key');
  });

  it('refreshes the key list when revocation commits while a read is in flight', async () => {
    const second = { ...key, id: 'd'.repeat(32), requestId: 'e'.repeat(32), name: 'Keeper Key' };
    const superseded = { ...key, lifecycle: 'superseded' as const };
    let revokeRequestId = '';
    let settleRevoke: (response: Response) => void = () => undefined;
    let settleInFlightRead: (response: Response) => void = () => undefined;
    let keyReads = 0;
    let revoked = false;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/operations/latest')) {
        return Promise.resolve(
          Response.json({
            operation: null,
            nextStep: '',
            runtimeState: 'effect_unknown',
            expiresAtMs: 0,
          })
        );
      }
      if (url.includes('/revocation/') && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(Response.json({ ready: true, reason: '' }));
      }
      if (url.endsWith('/api-keys/' + key.id + '/revocation') && init?.method === 'POST') {
        const parsed: unknown = JSON.parse(String(init.body));
        if (parsed && typeof parsed === 'object' && 'requestId' in parsed && typeof parsed.requestId === 'string') {
          revokeRequestId = parsed.requestId;
        }
        return new Promise<Response>((resolve) => {
          settleRevoke = resolve;
        });
      }
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        keyReads += 1;
        if (keyReads === 1) {
          return Promise.resolve(
            Response.json({ keys: [key, second], gatewayPending: false, revocationAvailable: true })
          );
        }
        if (keyReads === 2) {
          return new Promise<Response>((resolve) => {
            settleInFlightRead = resolve;
          });
        }
        return Promise.resolve(
          Response.json({
            keys: revoked ? [superseded, second] : [key, second],
            gatewayPending: false,
            revocationAvailable: true,
          })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'known', reason: 'observed' },
            items: [],
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    await act(async () => {
      renderer = create(
        <FreshApiKeys
          client={client}
          credential="admin-test-only"
          onUnauthorized={unauthorized}
          onCreationPending={vi.fn()}
        />
      );
    });
    await act(async () => {
      renderer.root
        .findAllByType(DropdownMenu)[0]
        .props.items.find((item: { key: string }) => item.key === 'revoke')
        .onClick();
    });
    act(() => {
      void button(k('confirmOperation')).props.onClick();
    });
    // An unforced read opens while the revocation request is in flight.
    act(() => {
      void button(t('refresh')).props.onClick();
    });
    expect(keyReads).toBe(2);

    // Revocation commits and fires onChanged() while read #2 is still open.
    revoked = true;
    await act(async () => {
      settleRevoke(
        Response.json({
          operation: {
            id: revokeRequestId,
            targetId: key.id,
            keeperId: second.id,
            mode: 'revoke',
            outcome: 'committed',
            revision: 1,
            steps: [{ name: 'revoke', state: 'observed', returned: true }],
          },
          runtimeState: 'effective_observed',
          expiresAtMs: 0,
          nextStep: '',
        })
      );
    });
    await act(async () => {
      settleInFlightRead(
        Response.json({ keys: [key, second], gatewayPending: false, revocationAvailable: true })
      );
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-summary-saved' }).findByType('strong').children
    ).toEqual(['1']);
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-summary-replaced' }).findByType('strong')
        .children
    ).toEqual(['1']);
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-row-' + key.id })).toHaveLength(0);
  });

  it('clears stale blockers, retires pre-rebind reads, and reloads keys on rebind even while the details drawer is open', async () => {
    const postRebind = { ...key, id: 'c'.repeat(32), requestId: 'd'.repeat(32), name: 'Post Rebind' };
    let settlePreRebindUsage: (response: Response) => void = () => undefined;
    let usageReads = 0;
    let keyReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.endsWith('/api-keys/' + key.id)) {
        return Promise.resolve(Response.json(key));
      }
      if (url.endsWith('/api-keys/' + key.id + '/usage')) {
        return Promise.resolve(
          Response.json({
            keyId: key.id,
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'unknown', reason: 'usage_read_failed' },
            totals: null,
            series: [],
          })
        );
      }
      if (url.endsWith('/api-keys')) {
        keyReads += 1;
        return Promise.resolve(
          Response.json({
            keys: keyReads === 1 ? [key] : [key, postRebind],
            gatewayPending: false,
            revocationAvailable: false,
          })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'known', reason: 'observed' },
            items: [],
          })
        );
      }
      if (url.endsWith('/external/usage')) {
        usageReads += 1;
        if (usageReads === 1) {
          return Promise.resolve(
            Response.json({ code: 'external_cpa_authority_unavailable' }, { status: 503 })
          );
        }
        if (usageReads === 2) {
          return new Promise<Response>((resolve) => {
            settlePreRebindUsage = resolve;
          });
        }
        return Promise.resolve(
          Response.json({
            statisticsEnabled: true,
            collector: 'running',
            collectionStarted: true,
            blockedReason: '',
            cpaVersion: { version: 'v8.0.55', support: 'supported', writeBlocked: false },
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    const page = (reloadToken: number) => (
      <FreshApiKeys
        client={client}
        credential="admin-test-only"
        onUnauthorized={unauthorized}
        onCreationPending={vi.fn()}
        externalUsage={true}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(page(0));
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-authority_unavailable' })
    ).toHaveLength(1);

    // Open details drawer, click reload on the usage panel so a pre-rebind read stays open,
    // then complete a rebind while the drawer remains open.
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'open-key-' + key.id }).props.onClick();
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'external-usage-reload' }).props.onClick();
    });
    await act(async () => {
      renderer.update(page(1));
    });
    // Pre-rebind read #2 settles late with a key rejection while drawer is still open.
    await act(async () => {
      settlePreRebindUsage(
        Response.json({ code: 'external_management_key_rejected', detail: '401' }, { status: 409 })
      );
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-authority_unavailable' })
    ).toHaveLength(0);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-key_rejected' })
    ).toHaveLength(0);

    await click(t('keysCloseDetails'));
    expect(view()).toContain(postRebind.id);
    expect(view()).toContain(x('usageCollectorRunning'));
  });

  it('does not let an overlapping list snapshot overwrite a retried creation name', async () => {
    const unnamed = { ...key, id: 'f'.repeat(32), requestId: 'b'.repeat(32), name: '', nameRevision: 1, runtimeState: 'effective_observed' as const };
    const named = { ...unnamed, name: 'Retried Name', nameRevision: 2 };
    let settleRetryPut: (response: Response) => void = () => undefined;
    let settleRebindRead: (response: Response) => void = () => undefined;
    let keyReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        return Promise.resolve(Response.json({ key: unnamed, createdSecret: secret }, { status: 201 }));
      }
      if (url.endsWith('/api-keys/' + unnamed.id) && init?.method === 'PUT') {
        const body: unknown = JSON.parse(String(init.body));
        if (body && typeof body === 'object' && 'name' in body && body.name === 'Initial Fail') {
          return Promise.resolve(new Response('', { status: 503 }));
        }
        return new Promise<Response>((resolve) => {
          settleRetryPut = resolve;
        });
      }
      if (url.endsWith('/api-keys/' + unnamed.id) && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(Response.json(unnamed));
      }
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        keyReads += 1;
        if (keyReads === 1) {
          return Promise.resolve(
            Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
          );
        }
        if (keyReads === 2) {
          return new Promise<Response>((resolve) => {
            settleRebindRead = resolve;
          });
        }
        return Promise.resolve(
          Response.json({ keys: [key, named], gatewayPending: false, revocationAvailable: false })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'known', reason: 'observed' },
            items: [],
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    const page = (reloadToken: number) => (
      <FreshApiKeys
        client={client}
        credential="admin-test-only"
        onUnauthorized={unauthorized}
        onCreationPending={vi.fn()}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(page(0));
    });
    await setCreateName('Initial Fail');
    await click(t('keysCreate'));
    await setCreateName('Retried Name');
    act(() => {
      void renderer.root.findByProps({ 'data-testid': 'retry-create-name' }).props.onClick();
    });
    // Rebind starts while retryCreateName PUT is in flight.
    await act(async () => {
      renderer.update(page(1));
    });
    await act(async () => {
      settleRetryPut(Response.json(named));
    });
    await act(async () => {
      settleRebindRead(
        Response.json({ keys: [key, unnamed], gatewayPending: false, revocationAvailable: false })
      );
    });
    expect(view()).toContain('Retried Name');
  });

  it('retires the discarded StrictMode mount read so its late 401/blocker cannot affect the page', async () => {
    let settleFirstRead: (response: Response) => void = () => undefined;
    let keyReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.endsWith('/api-keys')) {
        keyReads += 1;
        if (keyReads === 1) {
          return new Promise<Response>((resolve) => {
            settleFirstRead = resolve;
          });
        }
        return Promise.resolve(
          Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'known', reason: 'observed' },
            items: [],
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    await act(async () => {
      renderer = create(
        <StrictMode>
          <FreshApiKeys
            client={client}
            credential="admin-test-only"
            onUnauthorized={unauthorized}
            onCreationPending={vi.fn()}
          />
        </StrictMode>
      );
    });
    expect(keyReads).toBe(2);
    await act(async () => {
      settleFirstRead(Response.json({ code: 'unauthorized' }, { status: 401 }));
    });
    expect(unauthorized).not.toHaveBeenCalled();
    expect(renderer.root.findByProps({ 'aria-busy': false })).toBeDefined();
    expect(view()).toContain(key.id);
  });

  it('does not erase a post-rebind blocker or paint a stale error when a pre-rebind createKey fails', async () => {
    let settlePost: (response: Response) => void = () => undefined;
    let keyReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        return new Promise<Response>((resolve) => {
          settlePost = resolve;
        });
      }
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        keyReads += 1;
        if (keyReads === 1) {
          return Promise.resolve(
            Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
          );
        }
        return Promise.resolve(Response.json({ code: 'external_target_moved' }, { status: 409 }));
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'known', reason: 'observed' },
            items: [],
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    const page = (reloadToken: number) => (
      <FreshApiKeys
        client={client}
        credential="admin-test-only"
        onUnauthorized={unauthorized}
        onCreationPending={vi.fn()}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(page(0));
    });
    await openCreate();
    act(() => {
      void renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
    });
    // Rebind finishes and discovers external_target_moved while pre-rebind POST is still pending.
    await act(async () => {
      renderer.update(page(1));
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-target_moved' })
    ).toHaveLength(1);

    // Pre-rebind POST settles late with external_management_key_rejected.
    await act(async () => {
      settlePost(
        Response.json({ code: 'external_management_key_rejected', detail: '401' }, { status: 409 })
      );
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-target_moved' })
    ).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-key_rejected' })
    ).toHaveLength(0);
    expect(view()).not.toContain(t('keysUnavailable'));
  });

  it('does not erase a post-rebind blocker or paint a stale error when a pre-rebind reconcile fails', async () => {
    let settleReconcile: (response: Response) => void = () => undefined;
    let keyReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        return Promise.reject(new Error('lost reply'));
      }
      if (url.includes('/api-keys/requests/')) {
        return new Promise<Response>((resolve) => {
          settleReconcile = resolve;
        });
      }
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        keyReads += 1;
        if (keyReads === 1) {
          return Promise.resolve(
            Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
          );
        }
        return Promise.resolve(Response.json({ code: 'external_target_moved' }, { status: 409 }));
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'known', reason: 'observed' },
            items: [],
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, fetchMock);
    const page = (reloadToken: number) => (
      <FreshApiKeys
        client={client}
        credential="admin-test-only"
        onUnauthorized={unauthorized}
        onCreationPending={vi.fn()}
        reloadToken={reloadToken}
      />
    );
    await act(async () => {
      renderer = create(page(0));
    });
    await click(t('keysCreate'));
    act(() => {
      void button(t('keysCheck')).props.onClick();
    });
    await act(async () => {
      renderer.update(page(1));
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-target_moved' })
    ).toHaveLength(1);

    await act(async () => {
      settleReconcile(
        Response.json({ code: 'external_management_key_rejected', detail: '401' }, { status: 409 })
      );
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-target_moved' })
    ).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-key_rejected' })
    ).toHaveLength(0);
    expect(view()).not.toContain(t('keysUnavailable'));
  });

  it('releases the mutation lock when createKey requestId generation throws synchronously', async () => {
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.endsWith('/api-keys')) {
        return Promise.resolve(
          Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'known', reason: 'observed' },
            items: [],
          })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    await mount(fetchMock);
    await openCreate();
    const entropySpy = vi.spyOn(crypto, 'getRandomValues').mockImplementationOnce(() => {
      throw new Error('entropy unavailable');
    });
    try {
      await act(async () => {
        void renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
      });
    } finally {
      entropySpy.mockRestore();
    }
    expect(renderer.root.findByProps({ 'aria-busy': false })).toBeDefined();
    expect(button(t('refresh')).props.disabled).toBe(false);
  });

  it('renders effect-confirmed creation UI with persistent access endpoints and no staged copy or manual install', async () => {
    const effectiveKey = {
      ...key,
      runtimeState: 'effective_observed' as const,
    };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        Response.json({ keys: [], gatewayPending: false, revocationAvailable: true })
      )
      .mockResolvedValueOnce(
        Response.json({ key: effectiveKey, createdSecret: secret }, { status: 201 })
      );
    await mount(withIdleKeyOperation(fetchMock));
    expect(
      renderer.root.findByProps({ 'data-testid': 'access-info-protocol-base-openai' }).props
        .children
    ).toBe('https://manager.example/api/v1');
    expect(
      renderer.root.findByProps({ 'data-testid': 'access-info-protocol-base-anthropic' }).props
        .children
    ).toBe('https://manager.example/api');

    await click(t('keysCreate'));
    expect(view()).toContain(k('createdActiveTitle'));
    expect(view()).toContain(k('createdActiveHelp'));
    expect(view()).toContain(secret);
    expect(view()).not.toContain(t('keysCreated'));
    expect(view()).not.toContain(k('unavailableHelp'));
    expect(view()).not.toContain(t('keysInstallOne'));
    expect(
      renderer.root.findByProps({ 'data-testid': 'create-access-info-protocol-base-openai' }).props
        .children
    ).toBe('https://manager.example/api/v1');

    await click(t('keysDismiss'));
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-runtime-' + effectiveKey.id }).props.children
    ).toBe(k('runtimeEffectiveObserved'));
  });

  it('supports 200 OK create replay with on-demand copy and access info without secret persistence', async () => {
    const effectiveKey = { ...key, runtimeState: 'effective_observed' as const };
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        Response.json({ keys: [], gatewayPending: false, revocationAvailable: true })
      )
      .mockResolvedValueOnce(Response.json({ key: effectiveKey }, { status: 200 }))
      .mockResolvedValueOnce(Response.json({ secret }));
    await mount(withIdleKeyOperation(fetchMock));
    await click(t('keysCreate'));
    expect(view()).toContain(k('createdActiveConfirmed'));
    expect(view()).not.toContain(secret);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'create-access-info-summary' })
    ).toHaveLength(1);

    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'copy-confirmed-client-secret' }).props.onClick()
    );
    expect(writeText).toHaveBeenCalledWith(secret);
    expect(view()).not.toContain(secret);
  });

  it.each(['external_cpa_authority_unavailable', 'config_authority_unavailable'])(
    'recovers a persisted create after %s without creating a duplicate key', async (code) => {
    const persistedUnapplied = {
      ...key,
      name: '',
      runtimeState: 'not_applied' as const,
      effectCode: code,
    };
    const namedPersisted = { ...persistedUnapplied, name: 'Edge Key', nameRevision: 2 };
    const effectiveKey = { ...namedPersisted, runtimeState: 'effective_observed' as const, effectCode: '' };
    const onCreationPending = vi.fn();
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const refusedClient = createPublicApiClient(
      () => ({ basePath: '/console', apiBase: '/console/api/v2', revision: 1 }),
      vi.fn<typeof fetch>().mockResolvedValue(Response.json({ code, key: persistedUnapplied }, { status: 503 }))
    );
    const errWithKey = await refusedClient.post('api-keys',
      { requestId: persistedUnapplied.requestId },
      { kind: 'admin', value: 'admin-test-only' }
    ).catch((error: unknown) => error);

    const stubClient = {
      get: vi.fn((resource: string) => {
        if (resource === 'api-keys')
          return Promise.resolve({ keys: [], gatewayPending: false, revocationAvailable: true });
        if (resource === 'api-keys/operations/latest')
          return Promise.resolve({ operation: null, runtimeState: 'effect_unknown', expiresAtMs: 0, nextStep: '' });
        if (resource === 'api-keys/usage-summary')
          return Promise.resolve({ fromMs: 1, toMs: 2, coverage: { state: 'unknown', reason: 'usage_read_failed' }, items: [] });
        if (resource === 'api-keys/access-info')
          return Promise.resolve({
            mode: 'connect_external',
            managementBasePath: '/console',
            inferenceBasePath: '',
            externalBaseUrl: 'https://cpa.remote.example/gateway',
            protocols: [
              {
                id: 'openai',
                label: 'OpenAI',
                basePath: 'https://cpa.remote.example/gateway/v1',
                examplePath: 'https://cpa.remote.example/gateway/v1/chat/completions',
              },
            ],
            ready: true,
          });
        if (resource === 'api-keys/' + persistedUnapplied.id)
          return Promise.resolve(persistedUnapplied);
        return Promise.reject(new Error('unexpected GET ' + resource));
      }),
      post: vi.fn((resource: string) => {
        if (resource === 'api-keys') {
          if (stubClient.post.mock.calls.filter(([r]) => r === 'api-keys').length === 1)
            return Promise.reject(errWithKey);
          return Promise.resolve({ key: effectiveKey });
        }
        if (resource === 'api-keys/' + persistedUnapplied.id + '/secret')
          return Promise.resolve({ secret });
        return Promise.reject(new Error('unexpected POST ' + resource));
      }),
      put: vi.fn(() => Promise.resolve(namedPersisted)),
    };

    await act(async () => {
      renderer = create(
        <FreshApiKeys
          client={stubClient as unknown as PublicApiClient}
          credential="admin-test-only"
          onUnauthorized={unauthorized}
          onCreationPending={onCreationPending}
        />
      );
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'access-info-external' }).props.children
    ).toBe('https://cpa.remote.example/gateway');

    await setCreateName('Edge Key');
    await click(t('keysCreate'));

    expect(
      renderer.root.findAllByProps({ 'data-testid': 'create-apply-recovery' })
    ).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'create-name-partial-failure' })
    ).toHaveLength(1);
    expect(onCreationPending).toHaveBeenLastCalledWith(true);
    expect(view()).toContain(persistedUnapplied.id);

    // On-demand copy works for persisted unapplied key
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'copy-recovered-client-secret' }).props.onClick()
    );
    expect(writeText).toHaveBeenCalledWith(secret);

    // Save name on persisted key -> still locked until gateway apply succeeds
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'retry-create-name' }).props.onClick()
    );
    expect(onCreationPending).toHaveBeenLastCalledWith(true);

    // Retry gateway apply using the SAME requestId
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'create-retry-apply' }).props.onClick()
    );
    expect(stubClient.post.mock.calls.filter(([r]) => r === 'api-keys')).toEqual([
      ['api-keys', { requestId: persistedUnapplied.requestId }, expect.anything()],
      ['api-keys', { requestId: persistedUnapplied.requestId }, expect.anything()],
    ]);
    expect(onCreationPending).toHaveBeenLastCalledWith(false);
    expect(view()).toContain(k('createdActiveConfirmed'));
  });

  it('recovers effect_unknown create read-only via requests and operations without blind POST retry', async () => {
    const opId = 'c'.repeat(32);
    const unknownKey = {
      ...key,
      runtimeState: 'effect_unknown' as const,
      operationId: opId,
      effectCode: 'key_operation_unknown',
    };
    const activeKey = {
      ...key,
      runtimeState: 'effective_observed' as const,
    };
    const errUnknown = Object.assign(
      new PublicApiError('http', 503, undefined, 'key_operation_unknown', undefined, 'effect unknown'),
      { key: unknownKey }
    );
    const stubClient = {
      get: vi.fn((resource: string) => {
        if (resource === 'api-keys')
          return Promise.resolve({ keys: [], gatewayPending: false, revocationAvailable: true });
        if (resource === 'api-keys/operations/latest')
          return Promise.resolve({ operation: null, runtimeState: 'effect_unknown', expiresAtMs: 0, nextStep: '' });
        if (resource === 'api-keys/usage-summary')
          return Promise.resolve({ fromMs: 1, toMs: 2, coverage: { state: 'unknown', reason: 'usage_read_failed' }, items: [] });
        if (resource === 'api-keys/access-info')
          return Promise.resolve({
            mode: 'new_managed',
            managementBasePath: '/console',
            inferenceBasePath: '/api',
            protocols: [],
            ready: false,
            code: 'gateway_pending',
          });
        if (resource === 'api-keys/requests/' + unknownKey.requestId)
          return Promise.resolve(unknownKey);
        if (resource === 'api-keys/operations/' + opId)
          return Promise.resolve({
            operation: {
              id: opId,
              targetId: unknownKey.id,
              keeperId: '',
              mode: 'add',
              outcome: 'committed',
              revision: 2,
              steps: [{ name: 'add', state: 'observed', returned: true }],
            },
            runtimeState: 'effective_observed',
            expiresAtMs: 0,
            nextStep: '',
          });
        if (resource === 'api-keys/' + unknownKey.id) return Promise.resolve(activeKey);
        return Promise.reject(new Error('unexpected GET ' + resource));
      }),
      post: vi.fn(() => Promise.reject(errUnknown)),
      put: vi.fn(),
    };
    await act(async () => {
      renderer = create(
        <FreshApiKeys
          client={stubClient as unknown as PublicApiClient}
          credential="admin-test-only"
          onUnauthorized={unauthorized}
          onCreationPending={vi.fn()}
        />
      );
    });
    await click(t('keysCreate'));
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'create-reconcile-apply' })
    ).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-testid': 'create-retry-apply' })).toHaveLength(0);

    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'create-reconcile-apply' }).props.onClick()
    );
    expect(stubClient.post).toHaveBeenCalledTimes(1);
    expect(view()).toContain(k('createdActiveConfirmed'));
  });

  it('discards stale owner replies when credential changes and supports 3+ multi-key workspace', async () => {
    let resolveOldCreate!: (response: Response) => void;
    const staleKey = { ...key, id: '1'.repeat(32), requestId: 'b'.repeat(32), name: 'Old Owner Key', runtimeState: 'effective_observed' as const };
    const key2 = { ...key, id: '2'.repeat(32), requestId: '3'.repeat(32), origin: 'adopted' as const, maskedPreview: '…aaaa', name: 'Adopted Key', runtimeState: 'effective_observed' as const };
    const key3 = { ...key, id: '4'.repeat(32), requestId: '5'.repeat(32), origin: 'workspace' as const, name: 'Third Key', runtimeState: 'effective_observed' as const };
    let readCount = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        return new Promise<Response>((resolve) => {
          resolveOldCreate = resolve;
        });
      }
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        readCount += 1;
        return Promise.resolve(
          Response.json({
            keys: readCount === 1 ? [key] : [key, key2, key3],
            gatewayPending: false,
            revocationAvailable: true,
          })
        );
      }
      if (url.endsWith('/api-keys/usage-summary') || url.endsWith('/api-keys/access-info')) {
        return Promise.resolve(
          url.endsWith('/access-info')
            ? Response.json({
                mode: 'adopt_existing',
                managementBasePath: '/console',
                inferenceBasePath: '/api',
                protocols: [],
                ready: true,
              })
            : Response.json({
                fromMs: 1,
                toMs: 2,
                coverage: { state: 'unknown', reason: 'usage_read_failed' },
                items: [],
              })
        );
      }
      return Promise.reject(new Error('unexpected url: ' + url));
    });
    const client = createPublicApiClient(() => context, withIdleKeyOperation(fetchMock));
    const page = (cred: string) => (
      <FreshApiKeys
        client={client}
        credential={cred}
        onUnauthorized={unauthorized}
        onCreationPending={vi.fn()}
      />
    );
    await act(async () => {
      renderer = create(page('admin-owner-1'));
    });
    await openCreate();
    act(() => {
      void renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
    });
    // Credential changes to owner 2 while owner 1 create is still open
    await act(async () => {
      renderer.update(page('admin-owner-2'));
    });
    await act(async () => {
      resolveOldCreate(Response.json({ key: staleKey, createdSecret: secret }, { status: 201 }));
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-row-' + staleKey.id })).toHaveLength(0);
    expect(view()).not.toContain(secret);
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-row-' + key2.id })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-row-' + key3.id })).toHaveLength(1);
    expect(renderer.root.findByType('tbody').findAllByType('tr')).toHaveLength(3);
  });

  it('refreshes concurrently revoked key on copy 409 in both confirmed and recovery create modals', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });

    // 1. Confirmed create modal (200 replay without one-time secret) -> Copy 409 refreshes to superseded.
    const activeKey = {
      ...key,
      id: '1'.repeat(32),
      requestId: key.requestId,
      runtimeState: 'effective_observed' as const,
    };
    const revokedKey = {
      ...activeKey,
      lifecycle: 'superseded' as const,
      runtimeState: 'revoked_observed' as const,
    };
    let postCount = 0;
    const confirmedFetch = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(
          Response.json({ keys: [], gatewayPending: false, revocationAvailable: true })
        );
      }
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        postCount += 1;
        return Promise.resolve(Response.json({ key: activeKey }, { status: 200 }));
      }
      if (url.endsWith('/api-keys/' + activeKey.id + '/secret') && init?.method === 'POST') {
        return Promise.resolve(new Response(null, { status: 409 }));
      }
      if (url.endsWith('/api-keys/' + activeKey.id) && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(Response.json(revokedKey));
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'unknown', reason: 'usage_read_failed' },
            items: [],
          })
        );
      }
      if (url.endsWith('/api-keys/access-info')) {
        return Promise.resolve(
          Response.json({
            mode: 'new_managed',
            managementBasePath: '/console',
            inferenceBasePath: '/api',
            protocols: [],
            ready: true,
          })
        );
      }
      return Promise.reject(new Error('unexpected ' + url));
    });
    await mount(withIdleKeyOperation(confirmedFetch));
    await click(t('keysCreate'));
    expect(postCount).toBe(1);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'copy-confirmed-client-secret' })
    ).toHaveLength(1);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'copy-confirmed-client-secret' }).props.onClick()
    );
    expect(writeText).not.toHaveBeenCalled();
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'copy-confirmed-client-secret' })
    ).toHaveLength(0);
    expect(view()).toContain(t('keysRevokedHistory'));
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'close-confirmed-create' }).props.onClick()
    );
    await act(async () =>
      renderer.root
        .findByProps({ 'aria-label': k('lifecycle') })
        .props.onChange({ target: { value: 'all' } })
    );
    expect(renderer.root.findByProps({ 'data-testid': 'key-lifecycle-' + activeKey.id }).props.children).toBe(
      k('replaced')
    );
    expect(renderer.root.findByProps({ 'data-testid': 'key-runtime-' + activeKey.id }).props.children).toBe(
      k('runtimeRevokedObserved')
    );
    await act(async () => renderer.unmount());

    // 2. Recovery create modal (persisted not_applied) -> Copy 409 refreshes to superseded and releases lock.
    const recoveryUnapplied = {
      ...key,
      id: '3'.repeat(32),
      requestId: key.requestId,
      runtimeState: 'not_applied' as const,
      effectCode: 'external_cpa_authority_unavailable',
    };
    const recoveryRevoked = {
      ...recoveryUnapplied,
      lifecycle: 'superseded' as const,
      runtimeState: 'revoked_observed' as const,
      effectCode: '',
    };
    const onCreationPending = vi.fn();
    const recoveryFetch = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(
          Response.json({ keys: [], gatewayPending: false, revocationAvailable: true })
        );
      }
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        return Promise.resolve(
          Response.json(
            { code: 'external_cpa_authority_unavailable', key: recoveryUnapplied },
            { status: 503 }
          )
        );
      }
      if (url.endsWith('/api-keys/' + recoveryUnapplied.id + '/secret') && init?.method === 'POST') {
        return Promise.resolve(new Response(null, { status: 409 }));
      }
      if (
        url.endsWith('/api-keys/' + recoveryUnapplied.id) &&
        (!init?.method || init.method === 'GET')
      ) {
        return Promise.resolve(Response.json(recoveryRevoked));
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'unknown', reason: 'usage_read_failed' },
            items: [],
          })
        );
      }
      if (url.endsWith('/api-keys/access-info')) {
        return Promise.resolve(
          Response.json({
            mode: 'connect_external',
            managementBasePath: '/console',
            inferenceBasePath: '/api',
            protocols: [],
            ready: true,
          })
        );
      }
      return Promise.reject(new Error('unexpected ' + url));
    });
    const client = createPublicApiClient(() => context, withIdleKeyOperation(recoveryFetch));
    await act(async () => {
      renderer = create(
        <FreshApiKeys
          client={client}
          credential="admin-test-only"
          onUnauthorized={unauthorized}
          onCreationPending={onCreationPending}
        />
      );
    });
    await click(t('keysCreate'));
    expect(onCreationPending).toHaveBeenLastCalledWith(true);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'copy-recovered-client-secret' })
    ).toHaveLength(1);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'copy-recovered-client-secret' }).props.onClick()
    );
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'copy-recovered-client-secret' })
    ).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'create-retry-apply' })).toHaveLength(0);
    expect(view()).toContain(t('keysRevokedHistory'));
    expect(onCreationPending).toHaveBeenLastCalledWith(false);
    expect(
      renderer.root.findByProps({ 'data-testid': 'dismiss-create-recovery' }).props.disabled
    ).toBe(false);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'dismiss-create-recovery' }).props.onClick()
    );
    await act(async () =>
      renderer.root
        .findByProps({ 'aria-label': k('lifecycle') })
        .props.onChange({ target: { value: 'all' } })
    );
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-lifecycle-' + recoveryUnapplied.id }).props.children
    ).toBe(k('replaced'));
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-runtime-' + recoveryUnapplied.id }).props.children
    ).toBe(k('runtimeRevokedObserved'));
  });

  it('lets persisted external/source blocker recovery dismiss to rebind/details while keeping unstored pending closed', async () => {
    const persistedBlocked = {
      ...key,
      id: '7'.repeat(32),
      requestId: key.requestId,
      name: '',
      runtimeState: 'not_applied' as const,
      effectCode: 'external_management_key_rejected',
    };
    const onRebind = vi.fn();
    const onCreationPending = vi.fn();
    let createMode: 'rejected' | 'network_drop' | 'source_conflict' = 'rejected';
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(
          Response.json({ keys: [], gatewayPending: false, revocationAvailable: true })
        );
      }
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        if (createMode === 'rejected') {
          return Promise.resolve(
            Response.json(
              {
                code: 'external_management_key_rejected',
                detail: 'client_forbidden',
                key: persistedBlocked,
              },
              { status: 409 }
            )
          );
        }
        if (createMode === 'source_conflict') {
          return Promise.resolve(
            Response.json(
              {
                code: 'key_operation_source_conflict',
                key: { ...persistedBlocked, effectCode: 'key_operation_source_conflict' },
              },
              { status: 409 }
            )
          );
        }
        return Promise.reject(new Error('network drop'));
      }
      if (url.endsWith('/api-keys/' + persistedBlocked.id)) {
        return Promise.resolve(Response.json(persistedBlocked));
      }
      if (url.endsWith('/api-keys/' + persistedBlocked.id + '/usage')) {
        return Promise.resolve(
          Response.json({
            keyId: persistedBlocked.id,
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'unknown', reason: 'usage_read_failed' },
            totals: null,
            series: [],
          })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'unknown', reason: 'usage_read_failed' },
            items: [],
          })
        );
      }
      if (url.endsWith('/api-keys/access-info')) {
        return Promise.resolve(
          Response.json({
            mode: 'connect_external',
            managementBasePath: '/console',
            inferenceBasePath: '/api',
            protocols: [],
            ready: true,
          })
        );
      }
      return Promise.reject(new Error('unexpected ' + url));
    });
    const client = createPublicApiClient(() => context, withIdleKeyOperation(fetchMock));
    await act(async () => {
      renderer = create(
        <FreshApiKeys
          client={client}
          credential="admin-test-only"
          onUnauthorized={unauthorized}
          onCreationPending={onCreationPending}
          onRebind={onRebind}
        />
      );
    });
    const createModal = () =>
      renderer.root.findAllByType(Modal).find((m) => m.props.className === 'key-create-modal')!;
    await setCreateName('External Key');
    await click(t('keysCreate'));

    // Persisted external rejection allows both manage-later and direct rebind escape.
    expect(createModal().props.closeDisabled).toBe(false);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'recovery-rebind-action' })
    ).toHaveLength(1);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'dismiss-create-recovery' }).props.onClick()
    );
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'create-apply-recovery' })
    ).toHaveLength(0);
    expect(onCreationPending).toHaveBeenLastCalledWith(false);
    // Durable row stays in table with its identity, and FreshExternalBlocker + Rebind are reachable.
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'key-row-' + persistedBlocked.id })
    ).toHaveLength(1);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'external-blocker-rebind' }).props.onClick()
    );
    expect(onRebind).toHaveBeenCalledTimes(1);
    // Details drawer reaches retry-apply using the preserved requestId.
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'open-key-' + persistedBlocked.id }).props.onClick()
    );
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'detail-retry-apply' })
    ).toHaveLength(1);
    await click(t('keysCloseDetails'));

    // Direct rebind button in recovery modal dismisses modal without losing row and calls onRebind.
    await click(t('keysCreate'));
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'recovery-rebind-action' }).props.onClick()
    );
    expect(onRebind).toHaveBeenCalledTimes(2);
    expect(createModal().props.open).toBe(false);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'key-row-' + persistedBlocked.id })
    ).toHaveLength(1);

    // Source conflict on persisted key also unlocks via Manage later.
    createMode = 'source_conflict';
    await click(t('keysCreate'));
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'dismiss-create-recovery' }).props.onClick()
    );
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'create-apply-recovery' })
    ).toHaveLength(0);

    // Genuinely unknown in-flight/network failure without stored recovery key stays locked.
    createMode = 'network_drop';
    await click(t('keysCreate'));
    expect(createModal().props.closeDisabled).toBe(true);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'dismiss-create-recovery' })
    ).toHaveLength(0);
    await act(async () => createModal().props.onClose());
    expect(createModal().props.open).toBe(true);
    expect(view()).toContain(t('keysCheck'));
  });

  it('displays missing lifecycle safely as non-retired without invalidating the workspace', async () => {
    const activeKey = {
      ...key,
      id: '1'.repeat(32),
      requestId: '2'.repeat(32),
      name: 'Active Key',
      lifecycle: 'active' as const,
      runtimeState: 'effective_observed' as const,
    };
    const missingKey = {
      ...key,
      id: '3'.repeat(32),
      requestId: '4'.repeat(32),
      name: 'Missing Key',
      lifecycle: 'missing' as const,
      runtimeState: 'not_applied' as const,
    };
    const supersededKey = {
      ...key,
      id: '5'.repeat(32),
      requestId: '6'.repeat(32),
      name: 'Retired Key',
      lifecycle: 'superseded' as const,
      runtimeState: 'revoked_observed' as const,
    };
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(
          Response.json({
            keys: [activeKey, missingKey, supersededKey],
            gatewayPending: false,
            revocationAvailable: true,
          })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'unknown', reason: 'usage_read_failed' },
            items: [],
          })
        );
      }
      if (url.endsWith('/api-keys/access-info')) {
        return Promise.resolve(
          Response.json({
            mode: 'new_managed',
            managementBasePath: '/console',
            inferenceBasePath: '/api',
            protocols: [],
            ready: true,
          })
        );
      }
      return Promise.reject(new Error('unexpected ' + url));
    });
    await mount(withIdleKeyOperation(fetchMock));
    expect(view()).not.toContain(t('keysUnavailable'));
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-lifecycle-' + activeKey.id }).props.children
    ).toBe(k('saved'));
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-lifecycle-' + missingKey.id }).props.children
    ).toBe(k('missingLifecycle'));
    expect(
      renderer.root
        .findByProps({ 'data-testid': 'key-row-' + missingKey.id })
        .findAllByType(DropdownMenu)
    ).toHaveLength(1);

    // Switch to all keys filter to verify superseded stays retired/unusable alongside non-retired missing.
    await act(async () =>
      renderer.root
        .findByProps({ 'aria-label': k('lifecycle') })
        .props.onChange({ target: { value: 'all' } })
    );
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-lifecycle-' + supersededKey.id }).props
        .children
    ).toBe(k('replaced'));
    expect(
      renderer.root
        .findByProps({ 'data-testid': 'key-row-' + supersededKey.id })
        .findAllByType(DropdownMenu)
    ).toHaveLength(0);
  });

  it('reconciles 503 and 409 aborted operation snapshots in create recovery, unlocks retry/dismiss, preserves client_forbidden detail, and wires detail Rebind', async () => {
    const opId = 'e'.repeat(32);
    const unknownKey = {
      ...key,
      id: '7'.repeat(32),
      requestId: key.requestId,
      runtimeState: 'effect_unknown' as const,
      operationId: opId,
    };
    const notAppliedKey = {
      ...unknownKey,
      runtimeState: 'not_applied' as const,
      operationId: '',
      effectCode: '',
    };
    const blockedUnknownKey = {
      ...unknownKey,
      operationId: '',
      effectCode: 'external_management_key_rejected',
    };
    const abortedOp = {
      id: opId,
      targetId: unknownKey.id,
      keeperId: '',
      mode: 'add' as const,
      outcome: 'aborted' as const,
      revision: 2,
      steps: [{ name: 'add' as const, state: 'unattempted' as const, returned: false }],
    };
    const onRebind = vi.fn();
    const onCreationPending = vi.fn();
    let scenario: 'aborted_503' | 'aborted_409' = 'aborted_503';
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(
          Response.json({ keys: [], gatewayPending: false, revocationAvailable: true })
        );
      }
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        return Promise.resolve(
          Response.json(
            { code: 'key_operation_unknown', key: unknownKey },
            { status: 503 }
          )
        );
      }
      if (url.endsWith('/api-keys/requests/' + unknownKey.requestId)) {
        return Promise.resolve(Response.json(unknownKey));
      }
      if (url.endsWith('/api-keys/operations/' + opId)) {
        if (scenario === 'aborted_503') {
          return Promise.resolve(
            Response.json(
              {
                code: 'key_operation_unknown',
                result: {
                  operation: abortedOp,
                  nextStep: '',
                  runtimeState: 'not_applied',
                  expiresAtMs: 0,
                },
              },
              { status: 503 }
            )
          );
        }
        return Promise.resolve(
          Response.json(
            {
              code: 'external_management_key_rejected',
              detail: 'client_forbidden',
              result: {
                operation: abortedOp,
                nextStep: '',
                runtimeState: 'not_applied',
                expiresAtMs: 0,
              },
            },
            { status: 409 }
          )
        );
      }
      if (url.endsWith('/api-keys/' + unknownKey.id)) {
        return Promise.resolve(
          Response.json(scenario === 'aborted_503' ? notAppliedKey : blockedUnknownKey)
        );
      }
      if (url.endsWith('/api-keys/' + unknownKey.id + '/usage')) {
        return Promise.resolve(
          Response.json({
            keyId: unknownKey.id,
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'unknown', reason: 'usage_read_failed' },
            totals: null,
            series: [],
          })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'unknown', reason: 'usage_read_failed' },
            items: [],
          })
        );
      }
      if (url.endsWith('/api-keys/access-info')) {
        return Promise.resolve(
          Response.json({
            mode: 'connect_external',
            managementBasePath: '/console',
            inferenceBasePath: '/api',
            protocols: [],
            ready: true,
          })
        );
      }
      return Promise.reject(new Error('unexpected ' + url));
    });

    const client = createPublicApiClient(() => context, withIdleKeyOperation(fetchMock));
    await act(async () => {
      renderer = create(
        <FreshApiKeys
          client={client}
          credential="admin-test-only"
          onUnauthorized={unauthorized}
          onCreationPending={onCreationPending}
          onRebind={onRebind}
        />
      );
    });
    const createModal = () =>
      renderer.root.findAllByType(Modal).find((m) => m.props.className === 'key-create-modal')!;

    // 1. 503 carrying aborted snapshot refetches canonical not_applied key and unlocks retry & dismiss.
    await click(t('keysCreate'));
    expect(createModal().props.closeDisabled).toBe(true);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'create-reconcile-apply' }).props.onClick()
    );
    expect(
      renderer.root.findByProps({ 'data-testid': 'create-apply-blocker-reason' }).props.children
    ).toBe(k('operationAborted'));
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'create-retry-apply' })
    ).toHaveLength(1);
    expect(createModal().props.closeDisabled).toBe(false);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'dismiss-create-recovery' }).props.onClick()
    );
    expect(createModal().props.open).toBe(false);

    // 2. 409 external_management_key_rejected + client_forbidden + aborted preserves detail across modal, global blocker, and details drawer.
    scenario = 'aborted_409';
    await click(t('keysCreate'));
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'create-reconcile-apply' }).props.onClick()
    );
    expect(
      renderer.root.findByProps({ 'data-testid': 'create-apply-aborted' }).props.children
    ).toBe(k('operationAborted'));
    expect(
      JSON.stringify(
        renderer.root.findByProps({ 'data-testid': 'create-apply-blocker-detail' }).findByType('p').props.children
      )
    ).toContain('client_forbidden');
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'recovery-rebind-action' })
    ).toHaveLength(1);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'dismiss-create-recovery' }).props.onClick()
    );
    expect(
      JSON.stringify(
        renderer.root.findByProps({ 'data-testid': 'external-blocker-detail' }).findByType('p').props.children
      )
    ).toContain('client_forbidden');
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-rebind' })
    ).toHaveLength(1);

    // Details drawer gets functional onRebind directly and preserves client_forbidden on reconcile.
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'open-key-' + unknownKey.id }).props.onClick()
    );
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-reconcile-apply' }).props.onClick()
    );
    expect(
      JSON.stringify(
        renderer.root.findByProps({ 'data-testid': 'detail-apply-blocker-detail' }).findByType('p').props.children
      )
    ).toContain('client_forbidden');
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-rebind-action' }).props.onClick()
    );
    expect(onRebind).toHaveBeenCalledTimes(1);
  });

  it('clears local preflight error on cancel or close while preserving pending and failedCreateName no-close guards', async () => {
    let failRandom = true;
    vi.stubGlobal('crypto', {
      getRandomValues: (bytes: Uint8Array) => {
        if (failRandom) throw new Error('entropy unavailable');
        return bytes.fill(0xbb);
      },
    });
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(
          Response.json({ keys: [], gatewayPending: false, revocationAvailable: true })
        );
      }
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        return Promise.reject(new Error('network drop'));
      }
      return Promise.reject(new Error('unexpected ' + url));
    });
    await mount(withIdleKeyOperation(fetchMock));
    const createModal = () =>
      renderer.root.findAllByType(Modal).find((m) => m.props.className === 'key-create-modal')!;

    // 1. Local preflight failure sets error without durable pending/recoveryKey; Cancel clears error and unlocks keysCreate.
    await click(t('keysCreate'));
    expect(view()).toContain(t('keysUnavailable'));
    await click(k('cancel'));
    expect(createModal().props.open).toBe(false);
    expect(button(t('keysCreate')).props.disabled).toBe(false);

    // 2. Modal onClose also clears preflight error.
    await click(t('keysCreate'));
    expect(view()).toContain(t('keysUnavailable'));
    await act(async () => createModal().props.onClose());
    expect(createModal().props.open).toBe(false);
    expect(button(t('keysCreate')).props.disabled).toBe(false);

    // 3. No-close protection still holds when durable pending exists.
    failRandom = false;
    await click(t('keysCreate'));
    expect(createModal().props.closeDisabled).toBe(true);
    expect(button(k('cancel')).props.disabled).toBe(true);
    await act(async () => createModal().props.onClose());
    expect(createModal().props.open).toBe(true);
  });

  it('reapplies a previously committed missing key from create-retry-apply via fresh add intent without weakening pending create idempotence', async () => {
    let seq = 0x20;
    vi.stubGlobal('crypto', {
      getRandomValues: (bytes: Uint8Array) => {
        bytes.fill(seq);
        seq += 1;
        return bytes;
      },
    });
    const missingKey = {
      ...key,
      lifecycle: 'missing' as const,
      runtimeState: 'not_applied' as const,
    };
    const activeKey = {
      ...missingKey,
      lifecycle: 'active' as const,
      runtimeState: 'effective_observed' as const,
      effectCode: '',
    };
    const origCommittedOp = {
      id: 'd'.repeat(32),
      targetId: missingKey.id,
      keeperId: '',
      mode: 'add' as const,
      outcome: 'committed' as const,
      revision: 2,
      steps: [{ name: 'add' as const, state: 'observed' as const, returned: false }],
    };
    let createdReqId = '';
    let freshOpId = '';
    let postAttempt = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(
          Response.json({ keys: [], gatewayPending: false, revocationAvailable: true })
        );
      }
      if (url.endsWith('/api-keys/requests/' + createdReqId)) {
        return Promise.resolve(Response.json({ ...missingKey, requestId: createdReqId }));
      }
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        postAttempt += 1;
        const body = JSON.parse(String(init.body));
        if (postAttempt === 1) {
          createdReqId = body.requestId;
          return Promise.reject(new Error('network drop'));
        }
        expect(body.requestId).toBe(createdReqId);
        return Promise.resolve(
          Response.json(
            {
              code: 'key_operation_unknown',
              key: { ...missingKey, requestId: createdReqId },
              result: {
                operation: origCommittedOp,
                nextStep: '',
                runtimeState: 'not_applied',
                expiresAtMs: 0,
              },
            },
            { status: 503 }
          )
        );
      }
      if (url.endsWith('/api-keys/' + missingKey.id + '/revocation') && init?.method === 'POST') {
        const body = JSON.parse(String(init.body));
        expect(body.mode).toBe('add');
        expect(body.keeperId).toBe('');
        freshOpId = body.requestId;
        return Promise.resolve(
          Response.json({
            operation: {
              id: freshOpId,
              targetId: missingKey.id,
              keeperId: '',
              mode: 'add',
              outcome: 'pending',
              revision: 1,
              steps: [{ name: 'add', state: 'unattempted', returned: false }],
            },
            nextStep: 'add',
            runtimeState: 'effect_unknown',
            expiresAtMs: 0,
          })
        );
      }
      if (url.endsWith('/api-keys/operations/' + freshOpId + '/continue') && init?.method === 'POST') {
        return Promise.resolve(
          Response.json({
            operation: {
              id: freshOpId,
              targetId: missingKey.id,
              keeperId: '',
              mode: 'add',
              outcome: 'committed',
              revision: 2,
              steps: [{ name: 'add', state: 'observed', returned: false }],
            },
            nextStep: '',
            runtimeState: 'effective_observed',
            expiresAtMs: 0,
          })
        );
      }
      if (url.endsWith('/api-keys/' + missingKey.id)) {
        return Promise.resolve(Response.json({ ...activeKey, requestId: createdReqId }));
      }
      return Promise.reject(new Error('unexpected ' + url));
    });

    await mount(withIdleKeyOperation(fetchMock));
    // Lost-response recovery reads the canonical key without issuing a new add intent.
    await click(t('keysCreate'));
    await click(t('keysCheck'));
    expect(freshOpId).toBe('');
    expect(renderer.root.findAllByProps({ 'data-testid': 'create-retry-apply' })).toHaveLength(1);

    // 2. Explicit user click on create-retry-apply executes fresh durable add intent when POST /api-keys reports committed add + not_applied.
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'create-retry-apply' }).props.onClick()
    );
    expect(freshOpId).toMatch(/^[0-9a-f]{32}$/);
    expect(freshOpId).not.toBe(createdReqId);
    expect(renderer.root.findAllByProps({ 'data-testid': 'create-apply-recovery' })).toHaveLength(0);
  });
});
