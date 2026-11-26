import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
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
    title,
    closeLabel,
    closeDisabled,
    onBeforeClose,
    onClose,
    children,
    footer,
  }: {
    open: boolean;
    title?: ReactNode;
    closeLabel?: string;
    closeDisabled?: boolean;
    onBeforeClose?: () => boolean | Promise<boolean>;
    onClose?: () => void;
    children?: ReactNode;
    footer?: ReactNode;
  }) =>
    open ? (
      <div>
        <div>
          <div>{title}</div>
          <button
            type="button"
            aria-label={closeLabel || 'common.close'}
            disabled={closeDisabled}
            onClick={() => {
              if (closeDisabled) return;
              const allowed = onBeforeClose ? onBeforeClose() : true;
              if (allowed && typeof allowed === 'object' && 'then' in allowed) {
                void allowed.then((ok) => {
                  if (ok !== false) onClose?.();
                });
              } else if (allowed !== false) {
                onClose?.();
              }
            }}
          />
        </div>
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
  const readText = (node: unknown): string => {
    if (typeof node === 'string' || typeof node === 'number') return String(node);
    if (Array.isArray(node)) return node.map(readText).join('');
    if (!node || typeof node !== 'object' || !('children' in node)) return '';
    return readText((node as ReactTestInstance).children);
  };
  const button = (text: string) =>
    renderer.root
      .findAllByType('button')
      .find((item) => item.props['aria-label'] === text || readText(item.props.children) === text)!;
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
      renderer.root.findByProps({ 'data-testid': 'key-filter-all' }).props.onClick()
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
    expect(view()).toContain(k('noFilteredKeys'));
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'reset-key-filters' }).props.onClick()
    );
    expect(renderer.root.findByType('tbody').findAllByType('tr')).toHaveLength(1);
    expect(view()).toContain(k('statusNotApplied'));
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
    expect(view()).not.toContain(t('keysEmpty'));
    expect(renderer.root.findByProps({ 'data-testid': 'empty-create-key' }).props.children).toBe(
      t('keysCreate')
    );
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

  it('creates a named key atomically via POST api-keys { requestId, name } without a second PUT and validates control chars', async () => {
    const pending = vi.fn();
    const named = { ...key, name: 'Prod CLI', nameRevision: 1, runtimeState: 'effective_observed' as const };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(empty())
      .mockResolvedValueOnce(Response.json({ key: named, createdSecret: secret }, { status: 201 }));
    await mount(fetchMock, pending);
    await openCreate();

    // Control characters disable confirm-create-key client-side without issuing POST.
    await setCreateName('Prod\tCLI');
    expect(
      renderer.root.findByProps({ 'data-testid': 'confirm-create-key' }).props.disabled
    ).toBe(true);

    await setCreateName('  Prod CLI  ');
    expect(
      renderer.root.findByProps({ 'data-testid': 'confirm-create-key' }).props.disabled
    ).toBe(false);
    await click(t('keysCreate'));

    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(0);
    expect(JSON.parse(String(fetchMock.mock.calls[1][1]?.body))).toEqual({
      requestId: key.requestId,
      name: 'Prod CLI',
    });
    expect(view()).toContain('Prod CLI');
    expect(view()).toContain(secret);
    // Single Done button in secret modal footer (no duplicate action inside card).
    expect(
      renderer.root
        .findAllByType('button')
        .filter((node) => node.props.children === t('keysDismiss'))
    ).toHaveLength(1);
    await click(t('keysDismiss'));
    expect(pending).toHaveBeenLastCalledWith(false);
  });

  it('reconciles a named creation after lost POST reply using GET /requests/{requestId} without re-sending name PUT', async () => {
    const pending = vi.fn();
    const named = { ...key, name: 'Production', nameRevision: 1, runtimeState: 'effective_observed' as const };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(empty())
      .mockRejectedValueOnce(new Error('creation reply lost after commit'))
      .mockResolvedValueOnce(Response.json(named));
    await mount(fetchMock, pending);
    await setCreateName('Production');
    await click(t('keysCreate'));
    expect(pending).toHaveBeenLastCalledWith(true);

    await click(t('keysCheck'));
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(0);
    expect(pending).toHaveBeenLastCalledWith(false);
    expect(view()).toContain('Production');
    expect(view()).toContain(k('createdActiveConfirmed'));
  });

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
      expect(
        renderer.root.findAllByProps({ 'data-testid': 'keys-presence-unverified' })
      ).toHaveLength(0);
      expect(
        renderer.root.findByProps({ 'data-testid': 'key-row-' + key.id }).findAllByType('td')
      ).toHaveLength(6);

      await click(t('refresh'));
      expect(
        renderer.root.findAllByProps({ 'data-testid': 'keys-presence-unverified' })
      ).toHaveLength(0);
      expect(view()).toContain(k('statusActive'));

      await click(t('refresh'));
      expect(
        renderer.root.findAllByProps({ 'data-testid': 'keys-presence-unverified' })
      ).toHaveLength(1);
    }
  );

  it('preserves filter, search, and cards view mode when returning from key details and preselects replacement target', async () => {
    const activeA = { ...key, id: '1'.repeat(32), name: 'Alpha Gateway', runtimeState: 'effective_observed' as const };
    const activeB = {
      ...key,
      id: '2'.repeat(32),
      requestId: '3'.repeat(32),
      maskedPreview: 'cpamp_sk_…8888',
      name: 'Beta Worker',
      runtimeState: 'effective_observed' as const,
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

    // Lifecycle tab badges show real counts
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-filter-saved' }).props.children
    ).toBeTruthy();
    expect(view()).toContain('2');
    expect(view()).toContain('1');
    // Switch to Saved filter, search "Alpha", and toggle Cards view
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'key-filter-saved' }).props.onClick()
    );
    const searchInput = renderer.root.findByProps({ type: 'search' });
    await act(async () => searchInput.props.onChange({ target: { value: 'Alpha' } }));
    const cardsBtn = renderer.root.findByProps({ 'data-testid': 'key-view-cards' });
    await act(async () => cardsBtn.props.onClick());
    expect(renderer.root.findAllByType('table')).toHaveLength(0);
    const cardGrid = renderer.root.findByProps({ 'data-testid': 'key-card-grid' });
    expect(cardGrid.findAllByType('article')).toHaveLength(1);
    renderer.root.findByProps({ 'data-testid': 'key-card-' + activeA.id });

    // Open details and return: filter, search, and dedicated cards view remain intact
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'open-key-' + activeA.id }).props.onClick()
    );
    await click(t('keysCloseDetails'));
    expect(renderer.root.findByProps({ type: 'search' }).props.value).toBe('Alpha');
    expect(renderer.root.findAllByType('table')).toHaveLength(0);
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-card-grid' }).findAllByType('article')
    ).toHaveLength(1);
    // Quick regenerate opens FreshKeyRegenerate targeted at activeA
    await act(async () =>
      renderer.root
        .findByType(DropdownMenu)
        .props.items.find((item: { key: string }) => item.key === 'regenerate')
        .onClick()
    );
    const targetCard = renderer.root.findByProps({ 'data-testid': 'key-regenerate-target-card' });
    expect(targetCard.findByType('code').props.children).toBe(activeA.maskedPreview);
    expect(renderer.root.findByProps({ 'data-testid': 'confirm-regenerate-key' })).toBeTruthy();
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
    expect(view()).toContain(k('timeActiveShort'));
  });

  it('renders 6-column table with models breakdown, credential-style 4-metric usage, liveness time, and 1x4 card tiles', async () => {
    const lastUsedAtMs = Date.UTC(2026, 0, 2, 3, 4);
    const adoptedRotatedKey = {
      ...key,
      id: 'b'.repeat(32),
      requestId: 'c'.repeat(32),
      origin: 'adopted' as const,
      maskedPreview: '…7f2d',
      lastRotatedAtMs: Date.UTC(2026, 0, 2, 1, 0),
    };
    const keysOnly = () =>
      vi.fn<typeof fetch>().mockImplementation(() =>
        Promise.resolve(
          Response.json({
            keys: [key, adoptedRotatedKey],
            gatewayPending: true,
            revocationAvailable: false,
          })
        )
      );
    await mount(keysOnly(), vi.fn(), {
      fromMs: 1,
      toMs: 2,
      coverage: { state: 'complete', reason: 'observed' },
      items: [
        {
          keyId: key.id,
          calls: 18,
          success: 16,
          failure: 2,
          inputTokens: 20_000,
          outputTokens: 4_500,
          cachedTokens: 12_000,
          totalTokens: 24_500,
          estimatedCostUsd: 1.42,
          topModels: [
            { model: 'claude-sonnet-4-5', calls: 12, totalTokens: 18_000, estimatedCostUsd: 1.1 },
            { model: 'gemini-2.5-pro', calls: 6, totalTokens: 6_500, estimatedCostUsd: 0.32 },
          ],
          modelCount: 3,
          lastUsedAtMs,
          coverage: { state: 'complete', reason: 'observed' },
        },
        {
          keyId: adoptedRotatedKey.id,
          calls: 0,
          success: 0,
          failure: 0,
          lastUsedAtMs: null,
          coverage: { state: 'complete', reason: 'observed' },
        },
      ],
    });
    const tableNode = renderer.root.findByProps({ 'data-testid': 'key-table' });
    expect(tableNode.findAllByType('th')).toHaveLength(6);

    const rowNode = renderer.root.findByProps({ 'data-testid': 'key-row-' + key.id });
    expect(
      rowNode.findAll(
        (node) =>
          typeof node.props?.className === 'string' &&
          node.props.className.includes('key-resource-icon')
      )
    ).toHaveLength(0);
    expect(view()).toContain(k('modelsColumn'));
    expect(view()).toContain(k('usageShortColumn'));
    expect(view()).toContain(k('timeColumn'));
    expect(
      rowNode.findAll((node) => node.props?.className === 'key-origin-pill')
    ).toHaveLength(0);
    expect(view()).toContain('claude-sonnet-4-5');
    expect(readText(rowNode)).toContain('+1');
    expect(readText(rowNode)).toContain('3 models');
    expect(readText(rowNode)).not.toContain('12 / 6');
    expect(readText(rowNode)).toContain('gemini-2.5-pro');

    // Hovering models trigger opens breakdown popover with calls, share %, and per-model cost
    await act(async () =>
      renderer.root
        .findByProps({ 'data-testid': `key-models-trigger-${key.id}` })
        .props.onMouseEnter({ currentTarget: null })
    );
    const modelsPopover = renderer.root.findByProps({
      'data-testid': `key-models-popover-${key.id}`,
    });
    expect(readText(modelsPopover)).toContain('12 calls');
    expect(readText(modelsPopover)).toContain('$1.10');
    expect(readText(modelsPopover)).toContain('6 calls');

    // Usage cell renders credential-style 2x2 history metrics (requests, tokens, cost, success rate)
    const usageNode = renderer.root.findByProps({ 'data-testid': 'key-usage-' + key.id });
    expect(readText(usageNode.findByProps({ className: 'key-stat-total' }))).toBe('18');
    expect(readText(usageNode)).toContain('24.5K');
    expect(readText(usageNode)).toContain('$1.42');
    expect(readText(usageNode)).toContain('88.9%');
    // Hovering usage cell reveals input/output/cache token breakdown and OK/Failed counts
    await act(async () =>
      renderer.root
        .findByProps({ 'data-testid': `open-key-usage-${key.id}` })
        .props.onMouseEnter({ currentTarget: null })
    );
    const usagePopover = renderer.root.findByProps({
      'data-testid': `key-usage-popover-${key.id}`,
    });
    expect(readText(usagePopover)).toContain('20,000');
    expect(readText(usagePopover)).toContain('4,500');
    expect(readText(usagePopover)).toContain('12,000');
    expect(readText(usagePopover)).toContain(`${k('usageSuccessShort')} 16`);
    expect(readText(usagePopover)).toContain(`${k('usageFailureShort')} 2`);

    // Time column elevates last-active time to primary line and keeps lifecycle time on secondary line
    const timeNode = renderer.root.findByProps({ 'data-testid': 'key-time-' + key.id });
    expect(readText(timeNode)).toContain(k('timeActiveShort'));
    expect(readText(timeNode)).toContain(k('timeCreatedShort'));

    const adoptedRowNode = renderer.root.findByProps({
      'data-testid': 'key-row-' + adoptedRotatedKey.id,
    });
    expect(
      adoptedRowNode.findAll((node) => node.props?.className === 'key-origin-pill')
    ).toHaveLength(1);
    expect(
      readText(
        renderer.root.findByProps({ 'data-testid': 'key-last-used-' + adoptedRotatedKey.id })
      )
    ).toBe(k('neverUsedShort'));
    expect(
      readText(
        renderer.root.findByProps({ 'data-testid': 'key-rotated-at-' + adoptedRotatedKey.id })
      )
    ).toContain(k('timeRotatedShort'));

    // Switch to Cards view and verify 1x4 credential-style usage tiles + Active/Never used header meta
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'key-view-cards' }).props.onClick()
    );
    const cardNode = renderer.root.findByProps({ 'data-testid': 'key-card-' + key.id });
    const cardText = readText(cardNode);
    expect(cardText).toContain('18');
    expect(cardText).toContain('24.5K');
    expect(cardText).toContain('$1.42');
    expect(cardText).toContain('88.9%');
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-card-recent-status-' + key.id })
    ).toBeDefined();
    const cardModelsText = readText(
      renderer.root.findByProps({ 'data-testid': 'key-card-models-' + key.id })
    );
    expect(cardModelsText).toContain(k('cardTopModelsTitle'));
    expect(cardModelsText).toContain('%');
    const adoptedCardNode = renderer.root.findByProps({
      'data-testid': 'key-card-' + adoptedRotatedKey.id,
    });
    expect(readText(adoptedCardNode)).toContain(k('adoptedOriginInline'));
    expect(readText(adoptedCardNode)).toContain(k('neverUsedShort'));
    expect(readText(adoptedCardNode)).toContain(k('timeRotatedShort'));
  });
  it('keeps the key list usable when the usage read fails and avoids duplicate Unavailable pills', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json({
      keys: [key],
      gatewayPending: true,
      revocationAvailable: false,
    }));
    await mount(fetchMock, vi.fn(), new Response('failed', { status: 503 }));
    expect(view()).toContain(key.id);
    expect(view()).toContain(k('usageStateUnknown'));
    expect(
      renderer.root.findAllByProps({ className: 'key-state-pill is-unavailable' })
    ).toHaveLength(0);
    expect(view()).not.toContain('≥');
  });


  it('surfaces a key-list external blocker when GET /api-keys reports rejected management credentials', async () => {
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
      return Promise.reject(new Error('unexpected url: ' + url));
    });

    await act(async () => {
      renderer = create(
        <FreshApiKeys
          client={createPublicApiClient(() => context, keyListRefusedFetch)}
          credential="admin-test-only"
          onUnauthorized={unauthorized}
          onCreationPending={vi.fn()}
        />
      );
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-key_rejected' })
    ).toHaveLength(1);
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

  function deferredResponse() {
    let resolve!: (value: Response | PromiseLike<Response>) => void;
    let reject!: (reason?: unknown) => void;
    const promise = new Promise<Response>((res, rej) => {
      resolve = res;
      reject = rej;
    });
    return { promise, resolve, reject };
  }
  it('does not let an overlapping list snapshot overwrite an atomically created key name', async () => {
    const named = { ...key, id: 'f'.repeat(32), requestId: 'b'.repeat(32), name: 'Prod CLI', nameRevision: 1, runtimeState: 'effective_observed' as const };
    const postDeferred = deferredResponse();
    const rebindDeferred = deferredResponse();
    let keyReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        return postDeferred.promise;
      }
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        keyReads += 1;
        if (keyReads === 1) {
          return Promise.resolve(
            Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
          );
        }
        if (keyReads === 2) {
          return rebindDeferred.promise;
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
    act(() => {
      void renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
    });
    await act(async () => {
      renderer.update(page(1));
    });
    await act(async () => {
      postDeferred.resolve(Response.json({ key: named, createdSecret: secret }, { status: 201 }));
    });
    await act(async () => {
      rebindDeferred.resolve(
        Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
      );
    });
    expect(view()).toContain('Prod CLI');
  });

  it('opens standalone rename modal from row menu without opening details drawer and survives overlapping snapshot', async () => {
    const renamed = { ...key, name: 'Renamed Key', nameRevision: 2 };
    const renameDeferred = deferredResponse();
    const rebindDeferred = deferredResponse();
    let keyReads = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys/' + key.id) && init?.method === 'PUT') {
        return renameDeferred.promise;
      }
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        keyReads += 1;
        if (keyReads === 1) {
          return Promise.resolve(
            Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
          );
        }
        if (keyReads === 2) {
          return rebindDeferred.promise;
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
      renderer.root
        .findByType(DropdownMenu)
        .props.items.find((item: { key: string }) => item.key === 'rename')
        .onClick();
    });
    // Standalone rename modal opens without triggering GET /api-keys/{id} or drawer.
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'key-detail-panel-overview' })
    ).toHaveLength(0);
    await act(async () => {
      renderer.root
        .findByProps({ id: 'key-name' })
        .props.onChange({ target: { value: 'Renamed Key' } });
    });
    act(() => {
      void renderer.root.findByProps({ id: 'rename-key-form' }).props.onSubmit({ preventDefault: vi.fn() });
    });
    await act(async () => {
      renderer.update(page(1));
    });
    expect(keyReads).toBe(2);
    await act(async () => {
      renameDeferred.resolve(Response.json(renamed));
    });
    await act(async () => {
      rebindDeferred.resolve(
        Response.json({ keys: [key], gatewayPending: false, revocationAvailable: false })
      );
    });
    expect(view()).toContain('Renamed Key');
  });

  it('refreshes the key list when revocation commits while a read is in flight', async () => {
    const second = {
      ...key,
      id: 'd'.repeat(32),
      requestId: 'e'.repeat(32),
      name: 'Keeper Key',
      runtimeState: 'effective_observed' as const,
    };
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
      void renderer.root.findByProps({ 'data-testid': 'confirm-pure-revoke' }).props.onClick();
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
            keeperId: '',
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
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-row-' + key.id })).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-row-' + second.id })).toHaveLength(1);
  });

  it('reloads keys and clears stale key-list blockers on rebind even while the details drawer is open', async () => {
    const postRebind = { ...key, id: 'c'.repeat(32), requestId: 'd'.repeat(32), name: 'Post Rebind' };
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
        if (keyReads === 2) {
          return Promise.resolve(
            Response.json({ code: 'external_cpa_authority_unavailable' }, { status: 503 })
          );
        }
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
    // Trigger a failed refresh that records a key-list blocker, then complete rebind while drawer stays open.
    await act(async () => {
      renderer.update(page(1));
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-authority_unavailable' })
    ).toHaveLength(1);

    await act(async () => {
      renderer.update(page(2));
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'external-blocker-authority_unavailable' })
    ).toHaveLength(0);

    await click(t('keysCloseDetails'));
    expect(view()).toContain(postRebind.id);
  });

  it('surfaces prominent pending-operation-banner, hides runtime filter on revoked tab, and exposes table ARIA roles', async () => {
    const pendingOp = {
      id: 'd'.repeat(32),
      targetId: key.id,
      keeperId: '',
      mode: 'rotate' as const,
      outcome: 'pending' as const,
      revision: 1,
      steps: [{ name: 'rotate' as const, state: 'unattempted' as const, returned: false }],
    };
    const namedKey = { ...key, name: 'Gateway Alpha', runtimeState: 'effective_observed' as const };
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.endsWith('/api-keys')) {
        return Promise.resolve(
          Response.json({ keys: [namedKey], gatewayPending: false, revocationAvailable: true })
        );
      }
      if (url.endsWith('/operations/latest')) {
        return Promise.resolve(
          Response.json({
            operation: pendingOp,
            nextStep: 'rotate',
            runtimeState: 'effect_unknown',
            expiresAtMs: 0,
          })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'complete', reason: 'observed' },
            items: [{ keyId: namedKey.id, calls: 7, lastUsedAtMs: 2000, coverage: { state: 'complete', reason: 'observed' } }],
          })
        );
      }
      return Promise.reject(new Error('unexpected ' + url));
    });
    await mount(fetchMock);

    // 1. Prominent pending-operation-banner shows mode + affected key and opens recovery modal.
    expect(renderer.root.findAllByProps({ 'data-testid': 'pending-operation-banner' })).toHaveLength(1);
    expect(view()).toContain(k('pendingBannerOpRotate'));
    expect(view()).toContain('Gateway Alpha');
    // 3. Table carries explicit ARIA roles for accessibility.
    expect(renderer.root.findByType('table').props.role).toBe('table');
    expect(renderer.root.findAllByProps({ role: 'columnheader' }).length).toBe(6);

    // 4. Switching to Revoked tab hides runtime filter select and renders emptyRevokedTab.
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'key-filter-replaced' }).props.onClick()
    );
    expect(renderer.root.findAllByProps({ ariaLabel: k('runtimeFilter') })).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-empty-tab-replaced' })).toHaveLength(
      1
    );

    // 5. Clicking Continue on banner opens Pure Revoke/Recovery Modal.
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'pending-operation-continue' }).props.onClick()
    );
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-operation-recovery' })).toHaveLength(
      1
    );
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
    expect(renderer.root.findAllByProps({ 'data-testid': 'access-info-management' })).toHaveLength(
      1
    );
    expect(renderer.root.findAllByProps({ 'data-testid': 'access-info-mode' })).toHaveLength(1);

    await click(t('keysCreate'));
    expect(view()).toContain(k('createdActiveTitle'));
    expect(view()).toContain(k('createdActiveHelp'));
    expect(view()).toContain(secret);
    expect(
      renderer.root.findAll(
        (node) => node.type === 'strong' && node.props.children === k('createdActiveTitle')
      )
    ).toHaveLength(1);
    expect(
      renderer.root.findAll(
        (node) => node.type === 'strong' && node.props.children === t('keysCreated')
      )
    ).toHaveLength(0);
    expect(view()).not.toContain(k('unavailableHelp'));
    expect(view()).not.toContain(t('keysInstallOne'));
    expect(
      renderer.root.findByProps({ 'data-testid': 'create-access-info-protocol-base-openai' }).props
        .children
    ).toBe('https://manager.example/api/v1');
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'create-access-info-management' })
    ).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'create-access-info-mode' })).toHaveLength(
      0
    );

    await click(t('keysDismiss'));
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-status-' + effectiveKey.id }).props.children
    ).toBe(k('statusActive'));
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
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'create-access-info-management' })
    ).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'create-access-info-mode' })).toHaveLength(
      0
    );

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
      name: 'Edge Key',
      nameRevision: 1,
      runtimeState: 'not_applied' as const,
      effectCode: code,
    };
    const effectiveKey = { ...persistedUnapplied, runtimeState: 'effective_observed' as const, effectCode: '' };
    const onCreationPending = vi.fn();
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const refusedClient = createPublicApiClient(
      () => ({ basePath: '/console', apiBase: '/console/api/v2', revision: 1 }),
      vi.fn<typeof fetch>().mockResolvedValue(Response.json({ code, key: persistedUnapplied }, { status: 503 }))
    );
    const errWithKey = await refusedClient.post('api-keys',
      { requestId: persistedUnapplied.requestId, name: 'Edge Key' },
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
      put: vi.fn(),
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
    expect(onCreationPending).toHaveBeenLastCalledWith(true);
    expect(view()).toContain(persistedUnapplied.id);
    expect(view()).toContain('Edge Key');

    // On-demand copy works for persisted unapplied key
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'copy-recovered-client-secret' }).props.onClick()
    );
    expect(writeText).toHaveBeenCalledWith(secret);

    // Retry gateway apply using the SAME requestId & name
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'create-retry-apply' }).props.onClick()
    );
    expect(stubClient.post.mock.calls.filter(([r]) => r === 'api-keys')).toEqual([
      ['api-keys', { requestId: persistedUnapplied.requestId, name: 'Edge Key' }, expect.anything()],
      ['api-keys', { requestId: persistedUnapplied.requestId, name: 'Edge Key' }, expect.anything()],
    ]);
    expect(stubClient.put).not.toHaveBeenCalled();
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
      renderer.root.findByProps({ 'data-testid': 'key-filter-all' }).props.onClick()
    );
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-status-' + activeKey.id }).props.children
    ).toBe(k('statusRevoked'));
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
      renderer.root.findByProps({ 'data-testid': 'key-filter-all' }).props.onClick()
    );
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-status-' + recoveryUnapplied.id }).props
        .children
    ).toBe(k('statusRevoked'));
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
      renderer.root.findByProps({ 'data-testid': 'key-status-' + activeKey.id }).props.children
    ).toBe(k('statusActive'));
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-status-' + missingKey.id }).props.children
    ).toBe(k('statusMissing'));
    expect(
      renderer.root
        .findByProps({ 'data-testid': 'key-row-' + missingKey.id })
        .findAllByType(DropdownMenu)
    ).toHaveLength(1);

    // Switch to all keys filter to verify superseded stays retired/unusable alongside non-retired missing.
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'key-filter-all' }).props.onClick()
    );
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-status-' + supersededKey.id }).props.children
    ).toBe(k('statusRevoked'));
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
    expect(
      renderer.root.findAllByType('button').some((item) => item.props.children === k('cancel'))
    ).toBe(false);
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

  it('surfaces the key list ahead of collapsed protocol access info and matches runtime subtitle to verification state', async () => {
    const verifiedKey = { ...key, runtimeState: 'effective_observed' as const };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        Response.json({
          keys: [verifiedKey],
          gatewayPending: false,
          revocationAvailable: true,
          presenceUnverified: false,
        })
      )
      .mockResolvedValueOnce(
        Response.json({
          keys: [{ ...key, runtimeState: 'not_applied' as const }],
          gatewayPending: false,
          revocationAvailable: true,
          presenceUnverified: false,
        })
      );
    await mount(withIdleKeyOperation(fetchMock));

    const heading = renderer.root.findByProps({ id: 'client-key-title' });
    expect(heading.props.className).toBe('sr-only');
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-filter-saved' }).props['aria-selected']
    ).toBe(true);
    expect(view()).toContain(k('statusActive'));
    expect(
      renderer.root.findAllByType('button').some((node) => node.props.children === k('clear'))
    ).toBe(false);
    const filterToolbar = renderer.root.findByProps({ 'data-testid': 'key-filter-toolbar' });
    expect(filterToolbar.findByProps({ 'data-testid': 'key-filter-saved' })).toBeTruthy();
    expect(filterToolbar.findByProps({ type: 'search' })).toBeTruthy();

    const accessPanel = renderer.root.findByProps({ 'data-testid': 'key-access-panel' });
    expect(accessPanel.props.hidden).toBe(true);
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-workspace-tab-access' }).props.onClick();
    });
    expect(renderer.root.findByProps({ 'data-testid': 'key-access-panel' }).props.hidden).toBe(
      false
    );
    expect(
      renderer.root.findByProps({ 'data-testid': 'access-info-protocol-base-openai' })
    ).toBeTruthy();

    await click(t('refresh'));
    expect(
      renderer.root
        .findByProps({ 'data-testid': 'key-row-' + key.id })
        .findAllByProps({ className: 'key-state-pill is-pending' })
    ).toHaveLength(1);
  });

  it('copies a saved key secret on demand from the list row, filters by runtime state, sorts by usage, and opens usage tab directly', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const savedSecret = 'cpamp_sk_' + 'a'.repeat(60) + '1234';
    const verifiedKey = {
      ...key,
      id: 'a'.repeat(32),
      name: 'Alpha Key',
      maskedPreview: 'cpamp_sk_…1234',
      runtimeState: 'effective_observed' as const,
      createdAtMs: 1_700_000_100_000,
    };
    const pendingKey = {
      ...key,
      id: 'b'.repeat(32),
      requestId: 'c'.repeat(32),
      name: 'Beta Pending',
      maskedPreview: 'cpamp_sk_…5678',
      runtimeState: 'not_applied' as const,
      createdAtMs: 1_700_000_200_000,
    };
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input, init) => {
      const url = typeof input === 'string' ? input : input.toString();
      const method = init?.method || 'GET';
      if (url.endsWith('/api-keys') && method === 'GET') {
        return Response.json({
          keys: [pendingKey, verifiedKey],
          gatewayPending: false,
          revocationAvailable: true,
          presenceUnverified: false,
        });
      }
      if (url.includes('/api-keys/usage-summary')) {
        return Response.json({
          fromMs: 1_700_000_000_000,
          toMs: 1_700_086_400_000,
          coverage: { state: 'complete', reason: 'observed' },
          items: [
            {
              keyId: verifiedKey.id,
              calls: 42,
              lastUsedAtMs: 1_700_080_000_000,
              coverage: { state: 'complete', reason: 'observed' },
            },
            {
              keyId: pendingKey.id,
              calls: 2,
              lastUsedAtMs: 1_700_010_000_000,
              coverage: { state: 'complete', reason: 'observed' },
            },
          ],
        });
      }
      if (url.endsWith(`/api-keys/${verifiedKey.id}/secret`) && method === 'POST') {
        return Response.json({ secret: savedSecret });
      }
      if (url.endsWith(`/api-keys/${verifiedKey.id}`) && method === 'GET') {
        return Response.json(verifiedKey);
      }
      if (url.includes(`/api-keys/${verifiedKey.id}/usage`)) {
        return Response.json({
          keyId: verifiedKey.id,
          fromMs: 1_700_000_000_000,
          toMs: 1_700_086_400_000,
          bucketMs: 3_600_000,
          coverage: {
            state: 'complete',
            reason: 'observed',
            collectingSinceMs: 1_700_000_000_000,
            lastProcessedEventId: 10,
            targetEventId: 10,
          },
          totals: {
            calls: 42,
            success: 40,
            failure: 2,
            inputTokens: 1000,
            outputTokens: 500,
            totalTokens: 1500,
            lastUsedAtMs: 1_700_080_000_000,
          },
          series: [],
        });
      }
      if (url.endsWith('/api-keys/operations/latest')) {
        return Response.json({ operation: null, runtimeState: 'effect_unknown', expiresAtMs: 0, nextStep: '' });
      }
      return Response.json({});
    });
    await mount(fetchMock, vi.fn(), {
      fromMs: 1_700_000_000_000,
      toMs: 1_700_086_400_000,
      coverage: { state: 'complete', reason: 'observed' },
      items: [
        {
          keyId: verifiedKey.id,
          calls: 42,
          lastUsedAtMs: 1_700_080_000_000,
          coverage: { state: 'complete', reason: 'observed' },
        },
        {
          keyId: pendingKey.id,
          calls: 2,
          lastUsedAtMs: 1_700_010_000_000,
          coverage: { state: 'complete', reason: 'observed' },
        },
      ],
    });

    // 1. On-demand row copy uses POST /secret and copies to clipboard.
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': `copy-row-secret-${verifiedKey.id}` })
        .props.onClick();
    });
    expect(writeText).toHaveBeenCalledExactlyOnceWith(savedSecret);
    expect(
      renderer.root.findByProps({ 'data-testid': `copy-row-secret-feedback-${verifiedKey.id}` })
        .props.children
    ).toBe(k('copiedShort'));

    // 2. Sort by 24h calls moves verifiedKey (42 calls) ahead of pendingKey (2 calls).
    const sortTrigger = renderer.root.findByProps({
      'aria-label': `${k('sortLabel')}: ${k('sortFieldCreated')}`,
    });
    await act(async () => {
      sortTrigger.props.onClick();
    });
    const callsOption = renderer.root
      .findAllByProps({ role: 'option' })
      .find((node) => node.props.children === k('sortFieldCalls24h'))!;
    await act(async () => {
      callsOption.props.onClick();
    });
    const sortedRows = renderer.root
      .findByType('tbody')
      .findAllByType('tr')
      .map((row) => row.props['data-testid']);
    expect(sortedRows).toEqual([`key-row-${verifiedKey.id}`, `key-row-${pendingKey.id}`]);

    // 3. Filter runtime state to "attention" keeps only pendingKey.
    const runtimeSelect = renderer.root.findByProps({ ariaLabel: k('runtimeFilter') });
    await act(async () => {
      runtimeSelect.props.onChange('attention');
    });
    const attentionRows = renderer.root
      .findByType('tbody')
      .findAllByType('tr')
      .map((row) => row.props['data-testid']);
    expect(attentionRows).toEqual([`key-row-${pendingKey.id}`]);

    // Reset runtime filter to restore both keys and open usage tab directly from the usage cell.
    await act(async () => {
      runtimeSelect.props.onChange('all');
    });
    await act(async () => {
      renderer.root
        .findByProps({ 'data-testid': `open-key-usage-${verifiedKey.id}` })
        .props.onClick();
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-detail-panel-usage' }).props.hidden
    ).toBe(false);
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-detail-panel-overview' }).props.hidden
    ).toBe(true);
  });

  it('aligns tab count to savedCount, auto-hides runtimeFilter when all active keys are effective, hides complete usage pill, and preserves effective_observed on rename', async () => {
    const activeKey = {
      ...key,
      id: '1'.repeat(32),
      requestId: '2'.repeat(32),
      name: 'Active One',
      runtimeState: 'effective_observed' as const,
    };
    const revokedKey = {
      ...key,
      id: '3'.repeat(32),
      requestId: '4'.repeat(32),
      maskedPreview: 'cpamp_sk_…3333',
      name: 'Old Key',
      lifecycle: 'superseded' as const,
      runtimeState: 'revoked_observed' as const,
    };
    const renamedUnknown = {
      ...activeKey,
      name: 'Renamed One',
      nameRevision: 2,
      runtimeState: 'effect_unknown' as const,
      operationId: '',
    };

    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys') && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(
          Response.json({
            keys: [activeKey, revokedKey],
            gatewayPending: false,
            revocationAvailable: true,
          })
        );
      }
      if (url.endsWith(`/api-keys/${activeKey.id}`) && init?.method === 'PUT') {
        return Promise.resolve(Response.json(renamedUnknown));
      }
      return Promise.reject(new Error('unexpected ' + url));
    });

    await mount(fetchMock, vi.fn(), {
      fromMs: 1_700_000_000_000,
      toMs: 1_700_086_400_000,
      coverage: { state: 'complete', reason: 'observed' },
      items: [
        {
          keyId: activeKey.id,
          calls: 15,
          lastUsedAtMs: 1_700_050_000_000,
          coverage: {
            state: 'complete',
            reason: 'observed',
            collectingSinceMs: 1_700_000_000_000,
            lastProcessedEventId: 10,
            targetEventId: 10,
          },
        },
      ],
    });

    // 1. Tab count badge shows savedCount (1 active), not total (2).
    const keysTab = renderer.root.findByProps({ 'data-testid': 'key-workspace-tab-keys' });
    const tabCount = keysTab.findByProps({ className: 'key-tab-badge' });
    expect(tabCount.props.children).toBe(1);

    // 2. runtimeFilter dropdown is hidden when all active keys are effective_observed.
    expect(renderer.root.findAllByProps({ ariaLabel: k('runtimeFilter') })).toHaveLength(0);

    // 3. Complete usage coverage pill is hidden in the list row, and row action uses rowDetails.
    const usageCellNode = renderer.root.findByProps({ 'data-testid': `key-usage-${activeKey.id}` });
    expect(
      usageCellNode.findAll(
        (node) => typeof node.props?.className === 'string' && node.props.className.includes('key-state-pill')
      )
    ).toHaveLength(0);
    expect(readText(usageCellNode.findByProps({ className: 'key-stat-total' }))).toBe('15');
    expect(
      renderer.root.findAllByProps({
        'data-testid': `key-recent-status-${activeKey.id}`,
      })
    ).toHaveLength(0);
    const detailBtn = renderer.root.findByProps({ 'data-testid': `open-key-${activeKey.id}` });
    expect(readText(detailBtn)).toBe(k('rowDetails'));
    expect(detailBtn.findAllByType('svg')).toHaveLength(0);

    // 4. Rename via row dropdown preserves effective_observed when PUT returns effect_unknown.
    await act(async () => {
      renderer.root
        .findByType(DropdownMenu)
        .props.items.find((item: { key: string }) => item.key === 'rename')
        .onClick();
    });
    await act(async () => {
      renderer.root.findByProps({ id: 'key-name' }).props.onChange({
        target: { value: 'Renamed One' },
      });
    });
    await act(async () => {
      await renderer.root
        .findByProps({ id: 'rename-key-form' })
        .props.onSubmit({ preventDefault: vi.fn() });
    });
    expect(
      renderer.root.findByProps({ 'data-testid': `key-status-${activeKey.id}` }).props.children
    ).toBe(k('statusActive'));
    expect(view()).toContain('Renamed One');
  });
  it('supports whole-row selection, quick row actions without bubbling, pagination bar, and empty filter hiding', async () => {
    const manyKeys = Array.from({ length: 12 }, (_, idx) => ({
      ...key,
      id: idx.toString(16).padStart(32, 'a'),
      requestId: idx.toString(16).padStart(32, 'b'),
      maskedPreview: `cpamp_sk_…${String(1000 + idx)}`,
      name: `Worker ${String(idx + 1).padStart(2, '0')}`,
      createdAtMs: 1_700_000_000_000 + (12 - idx) * 1000,
      runtimeState: 'effective_observed' as const,
    }));
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.endsWith(`/api-keys/${manyKeys[0].id}`)) {
        return Promise.resolve(Response.json(manyKeys[0]));
      }
      if (url.endsWith(`/api-keys/${manyKeys[0].id}/usage`)) {
        return Promise.resolve(
          Response.json({
            keyId: manyKeys[0].id,
            fromMs: 1,
            toMs: 2,
            bucketMs: 1,
            coverage: { state: 'unknown', reason: 'usage_read_failed', collectingSinceMs: 0, lastProcessedEventId: 0, targetEventId: 0 },
            totals: null,
            series: [],
          })
        );
      }
      if (url.endsWith('/api-keys')) {
        return Promise.resolve(
          Response.json({
            keys: manyKeys,
            gatewayPending: false,
            revocationAvailable: true,
            presenceUnverified: false,
          })
        );
      }
      return Promise.resolve(Response.json({}));
    });
    await mount(withIdleKeyOperation(fetchMock));

    expect(renderer.root.findByProps({ 'data-testid': 'key-pagination-bar' })).toBeTruthy();
    expect(renderer.root.findByType('tbody').findAllByType('tr')).toHaveLength(12);

    // Switch page size to 10 -> 2 pages.
    const pageSizeSelect = renderer.root.findByProps({ ariaLabel: k('paginationPageSizeLabel') });
    await act(async () => {
      pageSizeSelect.props.onChange('10');
    });
    expect(renderer.root.findByType('tbody').findAllByType('tr')).toHaveLength(10);
    expect(renderer.root.findByProps({ 'data-testid': 'key-pagination-prev' }).props.disabled).toBe(true);
    expect(renderer.root.findByProps({ 'data-testid': 'key-pagination-next' }).props.disabled).toBe(false);

    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-pagination-next' }).props.onClick();
    });
    expect(renderer.root.findByType('tbody').findAllByType('tr')).toHaveLength(2);
    expect(renderer.root.findByProps({ 'data-testid': 'key-pagination-prev' }).props.disabled).toBe(false);
    expect(renderer.root.findByProps({ 'data-testid': 'key-pagination-next' }).props.disabled).toBe(true);

    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-pagination-prev' }).props.onClick();
    });

    // Quick rename button does not trigger whole-row detail drawer.
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': `row-rename-${manyKeys[0].id}` }).props.onClick();
    });
    expect(renderer.root.findAllByProps({ id: 'rename-key-form' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-detail-panel-overview' })).toHaveLength(0);
    await act(async () => {
      await click(k('cancel'));
    });

    // Whole-row click opens details and marks row is-selected; switching to Access tab hides Create button.
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': `key-row-${manyKeys[0].id}` })
        .props.onClick({ target: null });
    });
    expect(
      renderer.root.findByProps({ 'data-testid': `key-row-${manyKeys[0].id}` }).props.className
    ).toContain('is-selected');
    expect(renderer.root.findByProps({ 'data-testid': 'key-detail-panel-overview' })).toBeTruthy();
    await act(async () => {
      await click(t('keysCloseDetails'));
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-workspace-tab-access' }).props.onClick();
    });
    expect(button(t('keysCreate'))).toBeUndefined();

    // Switch back to keys tab and verify table topbar + page jump input clamping.
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-workspace-tab-keys' }).props.onClick();
    });
    expect(renderer.root.findByProps({ 'data-testid': 'key-table-topbar' })).toBeTruthy();
    const jumpInput = renderer.root.findByProps({ 'data-testid': 'key-pagination-jump-input' });
    await act(async () => {
      jumpInput.props.onChange({ target: { value: '99' } });
    });
    await act(async () => {
      jumpInput.props.onBlur();
    });
    expect(renderer.root.findByType('tbody').findAllByType('tr')).toHaveLength(2);
    expect(renderer.root.findByProps({ 'data-testid': 'key-pagination-jump-input' }).props.value).toBe(
      '2'
    );
    await act(async () => {
      renderer.root
        .findByProps({ 'data-testid': 'key-pagination-jump-input' })
        .props.onChange({ target: { value: '1' } });
    });
    await act(async () => {
      renderer.root
        .findByProps({ 'data-testid': 'key-pagination-jump-input' })
        .props.onKeyDown({ key: 'Enter', preventDefault: vi.fn() });
    });
    expect(renderer.root.findByType('tbody').findAllByType('tr')).toHaveLength(10);
  });
  it('silently refreshes usage-summary after closing created key modal without flashing loading state', async () => {
    const created = {
      ...key,
      id: 'd'.repeat(32),
      requestId: 'b'.repeat(32),
      maskedPreview: 'cpamp_sk_…8888',
      name: 'Fresh Created',
      runtimeState: 'effective_observed' as const,
    };
    let usageCalls = 0;
    const coverage = {
      state: 'complete' as const,
      reason: 'observed',
      collectingSinceMs: 1_700_000_000_000,
      lastProcessedEventId: 10,
      targetEventId: 10,
    };
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      const method = init?.method || 'GET';
      if (url.endsWith('/api-keys') && method === 'POST') {
        return Promise.resolve(
          Response.json(
            { key: created, createdSecret: 'cpamp_sk_' + 'b'.repeat(60) + '8888' },
            { status: 201 }
          )
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        usageCalls += 1;
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage,
            items:
              usageCalls === 1
                ? [
                    {
                      keyId: key.id,
                      calls: 3,
                      lastUsedAtMs: null,
                      coverage,
                    },
                  ]
                : [
                    {
                      keyId: key.id,
                      calls: 3,
                      lastUsedAtMs: null,
                      coverage,
                    },
                    {
                      keyId: created.id,
                      calls: 0,
                      lastUsedAtMs: null,
                      coverage,
                    },
                  ],
          })
        );
      }
      if (url.endsWith('/api-keys')) {
        return Promise.resolve(
          Response.json({
            keys: [key],
            gatewayPending: false,
            revocationAvailable: true,
            presenceUnverified: false,
          })
        );
      }
      return Promise.resolve(Response.json({}));
    });
    const client = createPublicApiClient(() => context, withIdleKeyOperation(fetchMock));
    await act(async () => {
      renderer = create(
        <FreshApiKeys
          client={client}
          credential="admin-memory"
          onCreationPending={vi.fn()}
          onUnauthorized={vi.fn()}
        />
      );
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(usageCalls).toBe(1);

    await act(async () => button(t('keysCreate')).props.onClick());
    await act(async () => {
      renderer.root.findByProps({ id: 'create-key-name' }).props.onChange({
        target: { value: 'Fresh Created' },
      });
    });
    await act(async () => {
      await renderer.root
        .findByProps({ id: 'create-key-form' })
        .props.onSubmit({ preventDefault: vi.fn() });
    });

    // Before dismiss, newly created key is in list without new usage entry yet.
    expect(
      readText(renderer.root.findByProps({ 'data-testid': `key-usage-${created.id}` }))
    ).toContain('—');

    // Dismissing created secret modal triggers silent usage-summary refresh without clearing keys.
    await act(async () => {
      await button(t('keysDismiss')).props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(usageCalls).toBe(2);
    expect(
      readText(renderer.root.findByProps({ 'data-testid': `key-last-used-${created.id}` }))
    ).toBe(k('neverUsedShort'));
    const createdUsageCell = renderer.root.findByProps({
      'data-testid': `key-usage-${created.id}`,
    });
    expect(readText(createdUsageCell)).toBe('----');
    expect(
      createdUsageCell.findAll(
        (node) =>
          typeof node.props?.className === 'string' && node.props.className.includes('key-state-pill')
      )
    ).toHaveLength(0);
    expect(renderer.root.findByType('section').props['aria-busy']).toBe(false);
  });
  it('silently refreshes usage-summary when FreshKeyRegenerate finishes and closes without clearing keys', async () => {
    const rotated = {
      ...key,
      maskedPreview: 'cpamp_sk_…9999',
      lastRotatedAtMs: 1_700_000_050_000,
      runtimeState: 'effective_observed' as const,
    };
    const coverage = {
      state: 'complete' as const,
      reason: 'observed',
      collectingSinceMs: 1_700_000_000_000,
      lastProcessedEventId: 10,
      targetEventId: 10,
    };
    let usageCalls = 0;
    let keysCalls = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      const method = init?.method || 'GET';
      if (url.endsWith(`/api-keys/${key.id}/regenerate`) && method === 'POST') {
        return Promise.resolve(
          Response.json({
            key: rotated,
            createdSecret: 'cpamp_sk_' + 'b'.repeat(60) + '9999',
          })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        usageCalls += 1;
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage,
            items: [
              {
                keyId: key.id,
                calls: usageCalls >= 2 ? 7 : 0,
                lastUsedAtMs: null,
                coverage,
              },
            ],
          })
        );
      }
      if (url.endsWith('/api-keys')) {
        keysCalls += 1;
        return Promise.resolve(
          Response.json({
            keys: [keysCalls >= 2 ? rotated : key],
            gatewayPending: false,
            revocationAvailable: true,
            presenceUnverified: false,
          })
        );
      }
      return Promise.resolve(Response.json({}));
    });
    const client = createPublicApiClient(() => context, withIdleKeyOperation(fetchMock));
    await act(async () => {
      renderer = create(
        <FreshApiKeys
          client={client}
          credential="admin-memory"
          onCreationPending={vi.fn()}
          onUnauthorized={vi.fn()}
        />
      );
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(usageCalls).toBe(1);

    await act(async () => {
      renderer.root
        .findByType(DropdownMenu)
        .props.items.find((item: { key: string }) => item.key === 'regenerate')
        .onClick();
    });
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'confirm-regenerate-key' })
        .props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(usageCalls).toBeGreaterThanOrEqual(2);

    const callsBeforeClose = usageCalls;
    await act(async () => {
      await button(t('keysDismiss')).props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(usageCalls).toBe(callsBeforeClose + 1);
    expect(
      readText(renderer.root.findByProps({ 'data-testid': `key-usage-${key.id}` }))
    ).toContain('7');
    expect(renderer.root.findByType('section').props['aria-busy']).toBe(false);
  });

  it('consolidates presenceUnverified into the pending-operation banner instead of stacking two warning banners', async () => {
    const opId = '2'.repeat(32);
    const pendingKey = {
      ...key,
      runtimeState: 'effect_unknown' as const,
      operationId: opId,
      effectCode: 'key_operation_unknown',
    };
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
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
      if (url.endsWith('/api-keys/operations/latest')) {
        return Promise.resolve(
          Response.json({
            operation: {
              id: opId,
              targetId: pendingKey.id,
              keeperId: '',
              mode: 'add',
              outcome: 'unknown',
              revision: 1,
              steps: [{ name: 'add', state: 'attempted', returned: true }],
            },
            nextStep: '',
            runtimeState: 'effect_unknown',
            expiresAtMs: 0,
          })
        );
      }
      if (url.endsWith('/api-keys')) {
        return Promise.resolve(
          Response.json({
            keys: [pendingKey],
            gatewayPending: false,
            revocationAvailable: true,
            presenceUnverified: true,
          })
        );
      }
      return Promise.resolve(Response.json({}));
    });
    await mount(fetchMock);
    const pendingBanners = renderer.root.findAllByProps({
      'data-testid': 'pending-operation-banner',
    });
    expect(pendingBanners).toHaveLength(1);
    const unverifiedNotes = renderer.root.findAllByProps({
      'data-testid': 'keys-presence-unverified',
    });
    expect(unverifiedNotes).toHaveLength(1);
    expect(
      pendingBanners[0].findAllByProps({ 'data-testid': 'keys-presence-unverified' })
    ).toHaveLength(1);
  });

  it('switches usage summary window (24h / 7d / 30d) and sorts keys by estimated cost and total tokens', async () => {
    const keyLow = {
      ...key,
      id: '1'.repeat(32),
      requestId: '2'.repeat(32),
      name: 'Low Cost High Tokens',
      createdAtMs: 3_000,
    };
    const keyHigh = {
      ...key,
      id: '3'.repeat(32),
      requestId: '4'.repeat(32),
      name: 'High Cost Low Tokens',
      createdAtMs: 2_000,
    };
    const requestedUrls: string[] = [];
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      requestedUrls.push(url);
      if (url.includes('/api-keys/usage-summary?from_ms=')) {
        return Promise.resolve(
          Response.json({
            fromMs: 1_000,
            toMs: 9_000,
            coverage: { state: 'complete', reason: 'observed' },
            items: [
              {
                keyId: keyLow.id,
                calls: 50,
                totalTokens: 900_000,
                estimatedCostUsd: 0.45,
                lastUsedAtMs: 5_000,
                coverage: { state: 'complete', reason: 'observed' },
              },
              {
                keyId: keyHigh.id,
                calls: 12,
                totalTokens: 80_000,
                estimatedCostUsd: 8.75,
                lastUsedAtMs: 6_000,
                coverage: { state: 'complete', reason: 'observed' },
              },
            ],
          })
        );
      }
      if (url.endsWith('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: 8_000,
            toMs: 9_000,
            coverage: { state: 'complete', reason: 'observed' },
            items: [
              {
                keyId: keyLow.id,
                calls: 5,
                totalTokens: 90_000,
                estimatedCostUsd: 0.05,
                lastUsedAtMs: 5_000,
                coverage: { state: 'complete', reason: 'observed' },
              },
              {
                keyId: keyHigh.id,
                calls: 2,
                totalTokens: 10_000,
                estimatedCostUsd: 1.25,
                lastUsedAtMs: 6_000,
                coverage: { state: 'complete', reason: 'observed' },
              },
            ],
          })
        );
      }
      if (url.endsWith('/api-keys')) {
        return Promise.resolve(
          Response.json({
            keys: [keyLow, keyHigh],
            gatewayPending: false,
            revocationAvailable: true,
          })
        );
      }
      return Promise.resolve(Response.json({}));
    });

    await mount(fetchMock);
    expect(view()).toContain('Usage (24h)');

    // Switch to 7d window -> requests bounded from_ms/to_ms query and updates header + values
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-list-usage-window-7d' }).props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(requestedUrls.some((u) => /\/api-keys\/usage-summary\?from_ms=\d+&to_ms=\d+$/.test(u))).toBe(
      true
    );
    expect(view()).toContain('Usage (7d)');
    expect(
      readText(renderer.root.findByProps({ 'data-testid': `key-usage-${keyHigh.id}` }))
    ).toContain('$8.75');

    // Sort by Estimated cost descending -> keyHigh ($8.75) moves ahead of keyLow ($0.45)
    await act(async () => {
      renderer.root
        .findByProps({ 'aria-label': `${k('sortLabel')}: ${k('sortFieldCreated')}` })
        .props.onClick();
    });
    await act(async () => {
      renderer.root
        .findAllByType('button')
        .find((node) => node.props.children === k('sortFieldCost'))!
        .props.onClick();
    });
    const rowsAfterCostSort = renderer.root
      .findByProps({ 'data-testid': 'key-table' })
      .findByType('tbody')
      .findAllByType('tr');
    expect(rowsAfterCostSort[0]?.props['data-testid']).toBe(`key-row-${keyHigh.id}`);
    expect(rowsAfterCostSort[1]?.props['data-testid']).toBe(`key-row-${keyLow.id}`);

    // Sort by Total tokens descending -> keyLow (900K) moves ahead of keyHigh (80K)
    await act(async () => {
      renderer.root
        .findByProps({ 'aria-label': `${k('sortLabel')}: ${k('sortFieldCost')}` })
        .props.onClick();
    });
    await act(async () => {
      renderer.root
        .findAllByType('button')
        .find((node) => node.props.children === k('sortFieldTokens'))!
        .props.onClick();
    });
    const rowsAfterTokensSort = renderer.root
      .findByProps({ 'data-testid': 'key-table' })
      .findByType('tbody')
      .findAllByType('tr');
    expect(rowsAfterTokensSort[0]?.props['data-testid']).toBe(`key-row-${keyLow.id}`);
    expect(rowsAfterTokensSort[1]?.props['data-testid']).toBe(`key-row-${keyHigh.id}`);
  });

  it('clears stale metrics on window switch and failure, clamps to_ms to server clock, and updates sort label', async () => {
    const trackedKey = {
      ...key,
      id: '5'.repeat(32),
      requestId: '6'.repeat(32),
      name: 'Window Switch Key',
      runtimeState: 'effective_observed' as const,
      createdAtMs: 3_000,
    };
    const serverAheadToMs = Date.now() + 600_000;
    let resolveSevenDay!: (res: Response) => void;
    const sevenDayPending = new Promise<Response>((resolve) => {
      resolveSevenDay = resolve;
    });
    let failThirtyDay = false;
    const requestedUrls: string[] = [];

    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      requestedUrls.push(url);
      if (url.includes('/api-keys/usage-summary?from_ms=')) {
        if (failThirtyDay) {
          return Promise.resolve(new Response('backend unavailable', { status: 503 }));
        }
        return sevenDayPending;
      }
      if (url.endsWith('/api-keys')) {
        return Promise.resolve(
          Response.json({
            keys: [trackedKey],
            gatewayPending: false,
            revocationAvailable: true,
          })
        );
      }
      return Promise.resolve(Response.json({}));
    });

    await mount(fetchMock, vi.fn(), {
      fromMs: serverAheadToMs - 86_400_000,
      toMs: serverAheadToMs,
      coverage: { state: 'complete', reason: 'observed' },
      items: [
        {
          keyId: trackedKey.id,
          calls: 4,
          success: 4,
          failure: 0,
          totalTokens: 12_000,
          estimatedCostUsd: 1.25,
          lastUsedAtMs: serverAheadToMs - 1_000,
          coverage: { state: 'complete', reason: 'observed' },
        },
      ],
    });
    expect(
      readText(renderer.root.findByProps({ 'data-testid': `key-usage-${trackedKey.id}` }))
    ).toContain('$1.25');

    // Switch 24h -> 7d: stale 24h metric must clear immediately while 7d request is in flight.
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-list-usage-window-7d' }).props.onClick();
    });
    expect(view()).toContain('Usage (7d)');
    expect(
      readText(renderer.root.findByProps({ 'data-testid': `key-usage-${trackedKey.id}` }))
    ).not.toContain('$1.25');

    // Requested 7d to_ms must respect latest server toMs to avoid client clock skew truncation.
    const sevenDayUrl = requestedUrls.find((u) => u.includes('/api-keys/usage-summary?from_ms='))!;
    const sevenDayToMs = Number(new URL(sevenDayUrl, 'https://manager.example').searchParams.get('to_ms'));
    expect(sevenDayToMs).toBeGreaterThanOrEqual(serverAheadToMs);

    // Sort dropdown label reflects active 7d window instead of hardcoded 24h.
    await act(async () => {
      renderer.root
        .findByProps({ 'aria-label': `${k('sortLabel')}: ${k('sortFieldCreated')}` })
        .props.onClick();
    });
    const sortOptions = renderer.root
      .findAllByProps({ role: 'option' })
      .map((node) => node.props.children);
    expect(sortOptions).toContain('Calls (7d)');
    expect(sortOptions).not.toContain('Calls (24h)');

    // Resolve 7d summary and confirm new metrics appear.
    await act(async () => {
      resolveSevenDay(
        Response.json({
          fromMs: serverAheadToMs - 7 * 86_400_000,
          toMs: serverAheadToMs,
          coverage: { state: 'complete', reason: 'observed' },
          items: [
            {
              keyId: trackedKey.id,
              calls: 28,
              success: 28,
              failure: 0,
              totalTokens: 90_000,
              estimatedCostUsd: 8.75,
              lastUsedAtMs: serverAheadToMs - 1_000,
              coverage: { state: 'complete', reason: 'observed' },
            },
          ],
        })
      );
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(
      readText(renderer.root.findByProps({ 'data-testid': `key-usage-${trackedKey.id}` }))
    ).toContain('$8.75');

    // Switch 7d -> 30d with failing fetch: stale 7d metric must stay cleared.
    failThirtyDay = true;
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-list-usage-window-30d' }).props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(view()).toContain('Usage (30d)');
    expect(
      readText(renderer.root.findByProps({ 'data-testid': `key-usage-${trackedKey.id}` }))
    ).not.toContain('$8.75');
  });

  it('passes active 7d window to details drawer from table and card usage clicks without DOM id collision', async () => {
    const activeKey = {
      ...key,
      id: '7'.repeat(32),
      requestId: '8'.repeat(32),
      name: 'Drawer Window Parity Key',
      runtimeState: 'effective_observed' as const,
      createdAtMs: 4_000,
    };
    const now = Date.now();
    const detailUsageUrls: string[] = [];

    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.includes(`/api-keys/${activeKey.id}/usage`)) {
        detailUsageUrls.push(url);
        return Promise.resolve(
          Response.json({
            keyId: activeKey.id,
            fromMs: now - 7 * 86_400_000,
            toMs: now,
            bucketMs: 86_400_000,
            coverage: {
              state: 'complete',
              reason: 'observed',
              collectingSinceMs: now - 30 * 86_400_000,
              lastProcessedEventId: 10,
              targetEventId: 10,
            },
            totals: {
              calls: 15,
              success: 15,
              failure: 0,
              inputTokens: 10_000,
              outputTokens: 5_000,
              totalTokens: 15_000,
              estimatedCostUsd: 2.5,
              lastUsedAtMs: now - 5_000,
            },
            series: [],
          })
        );
      }
      if (url.endsWith(`/api-keys/${activeKey.id}`)) {
        return Promise.resolve(Response.json(activeKey));
      }
      if (url.includes('/api-keys/usage-summary')) {
        return Promise.resolve(
          Response.json({
            fromMs: now - 7 * 86_400_000,
            toMs: now,
            coverage: { state: 'complete', reason: 'observed' },
            items: [
              {
                keyId: activeKey.id,
                calls: 15,
                success: 15,
                failure: 0,
                totalTokens: 15_000,
                estimatedCostUsd: 2.5,
                lastUsedAtMs: now - 5_000,
                coverage: { state: 'complete', reason: 'observed' },
              },
            ],
          })
        );
      }
      if (url.endsWith('/api-keys')) {
        return Promise.resolve(
          Response.json({
            keys: [activeKey],
            gatewayPending: false,
            revocationAvailable: true,
          })
        );
      }
      return Promise.resolve(Response.json({}));
    });

    await mount(fetchMock, vi.fn(), {
      fromMs: now - 86_400_000,
      toMs: now,
      coverage: { state: 'complete', reason: 'observed' },
      items: [
        {
          keyId: activeKey.id,
          calls: 15,
          success: 15,
          failure: 0,
          totalTokens: 15_000,
          estimatedCostUsd: 2.5,
          lastUsedAtMs: now - 5_000,
          coverage: { state: 'complete', reason: 'observed' },
        },
      ],
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-list-usage-window-7d' }).props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    // Click usage cell in table view -> opens details drawer on Usage tab with 7d window.
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': `open-key-usage-${activeKey.id}` }).props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-detail-panel-usage' }).props.hidden
    ).toBe(false);
    expect(renderer.root.findAllByProps({ id: 'key-usage-window-7d' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ id: 'key-list-usage-window-7d' })).toHaveLength(1);
    expect(
      renderer.root.findByProps({ id: 'key-usage-window-7d' }).props['aria-selected']
    ).toBe(true);
    expect(detailUsageUrls.some((u) => u.includes('/usage?from_ms='))).toBe(true);

    // Close drawer, switch to cards view, and click card usage tile -> also opens 7d usage tab.
    detailUsageUrls.length = 0;
    await act(async () => {
      renderer.root
        .findByProps({ 'aria-label': t('keysCloseDetails') })
        .props.onClick();
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-view-cards' }).props.onClick();
    });
    await act(async () => {
      renderer.root
        .findByProps({ 'data-testid': `open-card-usage-${activeKey.id}` })
        .props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-detail-panel-usage' }).props.hidden
    ).toBe(false);
    expect(
      renderer.root.findByProps({ id: 'key-usage-window-7d' }).props['aria-selected']
    ).toBe(true);
    expect(detailUsageUrls.some((u) => u.includes('/usage?from_ms='))).toBe(true);
  });

  it('left-pads partial card recentSlots, avoids fabricating missing slots, and keeps status pill parity for operationId and revoked_observed', async () => {
    const noSlotsSyncingKey = {
      ...key,
      id: '9'.repeat(32),
      requestId: 'a'.repeat(32),
      name: 'Syncing Without Slots',
      runtimeState: 'effective_observed' as const,
      operationId: 'f'.repeat(32),
      createdAtMs: 5_000,
    };
    const partialSlotsRevokedKey = {
      ...key,
      id: 'b'.repeat(32),
      requestId: 'c'.repeat(32),
      name: 'Revoked Observed With Short Slots',
      runtimeState: 'revoked_observed' as const,
      createdAtMs: 4_000,
    };
    const now = Date.now();

    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.endsWith('/api-keys')) {
        return Promise.resolve(
          Response.json({
            keys: [noSlotsSyncingKey, partialSlotsRevokedKey],
            gatewayPending: false,
            revocationAvailable: true,
          })
        );
      }
      return Promise.resolve(Response.json({}));
    });

    await mount(fetchMock, vi.fn(), {
      fromMs: now - 86_400_000,
      toMs: now,
      coverage: { state: 'complete', reason: 'observed' },
      items: [
        {
          keyId: noSlotsSyncingKey.id,
          calls: 15,
          success: 12,
          failure: 3,
          totalTokens: 10_000,
          estimatedCostUsd: 0,
          lastUsedAtMs: now - 1_000,
          coverage: { state: 'complete', reason: 'observed' },
        },
        {
          keyId: partialSlotsRevokedKey.id,
          calls: 35,
          success: 34,
          failure: 1,
          totalTokens: 50_000,
          estimatedCostUsd: 0,
          modelCount: 4,
          topModels: [
            { model: 'model-a', calls: 10, estimatedCostUsd: 10 },
            { model: 'model-b', calls: 10, estimatedCostUsd: 10 },
            { model: 'model-c', calls: 10, estimatedCostUsd: 10 },
          ],
          recentSlots: [
            { startMs: now - 3_000, endMs: now - 2_000, success: 5, failure: 0 },
            { startMs: now - 2_000, endMs: now - 1_000, success: 0, failure: 0 },
            { startMs: now - 1_000, endMs: now, success: 29, failure: 1 },
          ],
          lastUsedAtMs: now - 500,
          coverage: { state: 'partial', reason: 'window_partial' },
        },
      ],
    });
    // Table view status pill parity: operationId -> statusSyncing, revoked_observed -> statusRevoked (is-muted)
    const tableSyncPill = renderer.root.findByProps({
      'data-testid': `key-status-${noSlotsSyncingKey.id}`,
    });
    expect(tableSyncPill.props.children).toBe(k('statusSyncing'));
    expect(tableSyncPill.props.className).toBe('key-state-pill is-pending');

    const tableRevokedPill = renderer.root.findByProps({
      'data-testid': `key-status-${partialSlotsRevokedKey.id}`,
    });
    expect(tableRevokedPill.props.children).toBe(k('statusRevoked'));
    expect(tableRevokedPill.props.className).toBe('key-state-pill is-muted');

    // Table view cost parity: complete $0.00 shown, partial 0 shown as '—' (not '$0.00').
    expect(
      readText(renderer.root.findByProps({ 'data-testid': `key-usage-${noSlotsSyncingKey.id}` }))
    ).toContain('$0.00');
    expect(
      readText(
        renderer.root.findByProps({ 'data-testid': `key-usage-${partialSlotsRevokedKey.id}` })
      )
    ).not.toContain('$0.00');

    // Table view models trigger is a semantic <button aria-expanded> that toggles popover on click.
    const modelsBtn = renderer.root.findByProps({
      'data-testid': `key-models-trigger-${partialSlotsRevokedKey.id}`,
    });
    expect(modelsBtn.type).toBe('button');
    expect(modelsBtn.props['aria-expanded']).toBe(false);
    await act(async () => {
      modelsBtn.props.onClick({ stopPropagation: vi.fn(), currentTarget: null });
    });
    expect(
      renderer.root.findByProps({
        'data-testid': `key-models-trigger-${partialSlotsRevokedKey.id}`,
      }).props['aria-expanded']
    ).toBe(true);
    expect(
      renderer.root.findByProps({
        'data-testid': `key-models-popover-${partialSlotsRevokedKey.id}`,
      })
    ).toBeDefined();

    // Switch to card view and verify status pill parity, recentSlots rules, and model mix rounding.
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-view-cards' }).props.onClick();
    });

    const cardSyncPill = renderer.root.findByProps({
      'data-testid': `key-status-${noSlotsSyncingKey.id}`,
    });
    expect(cardSyncPill.props.children).toBe(k('statusSyncing'));
    expect(cardSyncPill.props.className).toBe('key-card-status-badge is-pending');

    const cardRevokedPill = renderer.root.findByProps({
      'data-testid': `key-status-${partialSlotsRevokedKey.id}`,
    });
    expect(cardRevokedPill.props.children).toBe(k('statusRevoked'));
    expect(cardRevokedPill.props.className).toBe('key-card-status-badge is-muted');

    // Absent recentSlots must NOT fabricate a 20th active slot: all 20 blocks remain idle.
    const noSlotBlocks = renderer.root
      .findByProps({ 'data-testid': `key-card-recent-status-${noSlotsSyncingKey.id}` })
      .findAll((node) => typeof node.props?.className === 'string' && node.props.className.startsWith('key-status-block '));
    // Also match exact 'key-status-block' (active) + 'key-status-block is-idle'
    const allNoSlotBlocks = renderer.root
      .findByProps({ 'data-testid': `key-card-recent-status-${noSlotsSyncingKey.id}` })
      .findAll(
        (node) =>
          node.type === 'div' &&
          typeof node.props?.className === 'string' &&
          (node.props.className === 'key-status-block' ||
            node.props.className === 'key-status-block is-idle')
      );
    expect(noSlotBlocks).toHaveLength(20);
    expect(allNoSlotBlocks).toHaveLength(20);
    expect(allNoSlotBlocks.every((node) => node.props.className === 'key-status-block is-idle')).toBe(
      true
    );

    // Partial recentSlots (length 3) must left-pad 17 idle slots on the oldest edge.
    const shortSlotBlocks = renderer.root
      .findByProps({ 'data-testid': `key-card-recent-status-${partialSlotsRevokedKey.id}` })
      .findAll(
        (node) =>
          node.type === 'div' &&
          typeof node.props?.className === 'string' &&
          (node.props.className === 'key-status-block' ||
            node.props.className === 'key-status-block is-idle')
      );
    expect(shortSlotBlocks).toHaveLength(20);
    expect(
      shortSlotBlocks.slice(0, 17).every((node) => node.props.className === 'key-status-block is-idle')
    ).toBe(true);
    expect(shortSlotBlocks[17]?.props.className).toBe('key-status-block');
    expect(shortSlotBlocks[18]?.props.className).toBe('key-status-block is-idle');
    expect(shortSlotBlocks[19]?.props.className).toBe('key-status-block');

    // Card model mix uses largest-remainder rounding (34% + 33% + 33% = 100%) and shows call count for unpriced 1 other model.
    const cardModelsSection = renderer.root.findByProps({
      'data-testid': `key-card-models-${partialSlotsRevokedKey.id}`,
    });
    const pctNodes = cardModelsSection.findAllByProps({ className: 'key-model-mix-pct' });
    const pcts = pctNodes.map((node) => Number(readText(node).replace('%', '')));
    expect(pcts).toEqual([34, 33, 33]);
    expect(pcts.reduce((sum, n) => sum + n, 0)).toBeLessThanOrEqual(100);
    const cardModelsText = readText(cardModelsSection);
    expect(cardModelsText).toContain('1 other model · ≥5 calls');
    expect(cardModelsText).not.toContain('(0%)');
  });

  it('preserves active 7d usage summary when an earlier 24h refresh() fetch rejects afterwards', async () => {
    const trackedKey = {
      ...key,
      id: '9'.repeat(32),
      requestId: 'a'.repeat(32),
      name: 'Stale 24h Reject Key',
      runtimeState: 'effective_observed' as const,
      createdAtMs: 5_000,
    };
    const now = Date.now();
    let rejectInitial24h!: (reason?: unknown) => void;
    let initial24hCalls = 0;

    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.endsWith('/api-keys/usage-summary')) {
        initial24hCalls += 1;
        return new Promise<Response>((_, reject) => {
          rejectInitial24h = reject;
        });
      }
      if (url.includes('/api-keys/usage-summary?from_ms=')) {
        return Promise.resolve(
          Response.json({
            fromMs: now - 7 * 86_400_000,
            toMs: now,
            coverage: { state: 'complete', reason: 'observed' },
            items: [
              {
                keyId: trackedKey.id,
                calls: 28,
                success: 28,
                failure: 0,
                totalTokens: 90_000,
                estimatedCostUsd: 8.75,
                lastUsedAtMs: now - 1_000,
                coverage: { state: 'complete', reason: 'observed' },
              },
            ],
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
      if (url.endsWith('/api-keys')) {
        return Promise.resolve(
          Response.json({
            keys: [trackedKey],
            gatewayPending: false,
            revocationAvailable: true,
          })
        );
      }
      return Promise.resolve(Response.json({}));
    });

    await act(async () => {
      renderer = create(
        <FreshApiKeys
          client={createPublicApiClient(() => context, fetchMock)}
          credential="admin-test-only"
          onUnauthorized={unauthorized}
          onCreationPending={vi.fn()}
        />
      );
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(initial24hCalls).toBe(1);

    // Switch to 7d before initial 24h refresh settles and let 7d resolve.
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-list-usage-window-7d' }).props.onClick();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(
      readText(renderer.root.findByProps({ 'data-testid': `key-usage-${trackedKey.id}` }))
    ).toContain('$8.75');

    // Earlier 24h refresh() fetch rejects now -> 7d summary must stay intact!
    await act(async () => {
      rejectInitial24h(new Error('initial 24h summary failed'));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(
      readText(renderer.root.findByProps({ 'data-testid': `key-usage-${trackedKey.id}` }))
    ).toContain('$8.75');
    expect(
      readText(renderer.root.findByProps({ 'data-testid': `key-usage-${trackedKey.id}` }))
    ).toContain('28');
  });

  it('opens card-view models popover on pointerDown+focus+click without immediate close and clamps float drift in otherCost', async () => {
    const cardKey = {
      ...key,
      id: 'c'.repeat(32),
      requestId: 'd'.repeat(32),
      name: 'Card Models Popover Key',
      runtimeState: 'effective_observed' as const,
      createdAtMs: 6_000,
    };
    const now = Date.now();
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.endsWith('/api-keys')) {
        return Promise.resolve(
          Response.json({
            keys: [cardKey],
            gatewayPending: false,
            revocationAvailable: true,
          })
        );
      }
      return Promise.resolve(Response.json({}));
    });

    // 0.1 + 0.2 + 0.3 produces 0.6000000000000001 whereas sorted sum 0.3 + 0.2 + 0.1 === 0.6;
    // the 1e-16 drift must clamp to 0 instead of rendering "<$0.01 (0%)".
    await mount(fetchMock, vi.fn(), {
      fromMs: now - 86_400_000,
      toMs: now,
      coverage: { state: 'complete', reason: 'observed' },
      items: [
        {
          keyId: cardKey.id,
          calls: 35,
          success: 35,
          failure: 0,
          totalTokens: 40_000,
          estimatedCostUsd: 0.1 + 0.2 + 0.3,
          modelCount: 4,
          topModels: [
            { model: 'model-top', calls: 10, estimatedCostUsd: 0.3 },
            { model: 'model-mid', calls: 10, estimatedCostUsd: 0.2 },
            { model: 'model-low', calls: 10, estimatedCostUsd: 0.1 },
          ],
          lastUsedAtMs: now - 500,
          coverage: { state: 'complete', reason: 'observed' },
        },
      ],
    });

    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-view-cards' }).props.onClick();
    });

    const cardModelsSection = renderer.root.findByProps({
      'data-testid': `key-card-models-${cardKey.id}`,
    });
    const cardModelsText = readText(cardModelsSection);
    expect(cardModelsText).toContain('1 other model · 5 calls');
    expect(cardModelsText).not.toContain('<$0.01');
    expect(cardModelsText).not.toContain('(0%)');

    const cardModelsTrigger = () =>
      renderer.root.findByProps({
        'data-testid': `key-models-trigger-${cardKey.id}`,
      });
    expect(cardModelsTrigger().props['aria-expanded']).toBe(false);

    // Pointer click sequence fires pointerDown -> focus -> click: popover must stay open.
    await act(async () => {
      cardModelsTrigger().props.onPointerDown();
      cardModelsTrigger().props.onFocus({ currentTarget: null });
      cardModelsTrigger().props.onClick({ stopPropagation: vi.fn(), currentTarget: null });
    });
    expect(cardModelsTrigger().props['aria-expanded']).toBe(true);
    expect(
      renderer.root.findByProps({ 'data-testid': `key-models-popover-${cardKey.id}` })
    ).toBeDefined();

    // Second click closes it cleanly.
    await act(async () => {
      cardModelsTrigger().props.onPointerDown();
      cardModelsTrigger().props.onFocus({ currentTarget: null });
      cardModelsTrigger().props.onClick({ stopPropagation: vi.fn(), currentTarget: null });
    });
    expect(cardModelsTrigger().props['aria-expanded']).toBe(false);
    expect(
      renderer.root.findAllByProps({ 'data-testid': `key-models-popover-${cardKey.id}` })
    ).toHaveLength(0);
  });
});
