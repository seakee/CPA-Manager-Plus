import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FreshApiKeys } from './FreshApiKeys';
import { createPublicApiClient } from './publicApi';
import { fullWebText as t } from '../i18n/fullweb';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { DropdownMenu } from '../components/ui/DropdownMenu';

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
};
const secret = 'cpamp_sk_' + 'c'.repeat(64);
const empty = () => Response.json({ keys: [], gatewayPending: true, revocationAvailable: false });

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
    onCreationPending = vi.fn()
  ) {
    await act(async () => {
      renderer = create(
        <FreshApiKeys
          client={createPublicApiClient(() => context, fetchMock)}
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
    expect(view()).toContain(k('notApplied'));
    expect(view()).toContain(k('scope'));
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it('keeps the Console exit lock when a revocation read finishes during an unknown S1 install', async () => {
    const onCreationPending = vi.fn();
    let finishOperation!: (response: Response) => void;
    let rejectInstall!: (reason: Error) => void;
    let initialReads = 0;
    const initial = {
      keyId: key.id,
      managerStaged: true,
      cpaConfigured: false,
      runtimeState: 'effect_unknown',
      runtimeGeneration: '7',
      observedFromMs: 0,
      observedUntilMs: 0,
      expiresAtMs: 0,
      ingressAuthorized: false,
      installAvailable: true,
      attempted: false,
    };
    const fetchMock = vi.fn<typeof fetch>((url, options) => {
      const address = String(url);
      if (address.endsWith('/api-keys') && options?.method === 'GET')
        return Promise.resolve(
          Response.json({ keys: [key], gatewayPending: true, revocationAvailable: true })
        );
      if (address.endsWith('/api-keys/operations/latest'))
        return new Promise<Response>((resolve) => {
          finishOperation = resolve;
        });
      if (address.endsWith('/api-keys/' + key.id) && options?.method === 'GET')
        return Promise.resolve(Response.json(key));
      if (address.endsWith('/initial-install') && options?.method === 'GET')
        return Promise.resolve(
          Response.json(
            initialReads++ === 0
              ? initial
              : { ...initial, attempted: true, installAvailable: false }
          )
        );
      if (address.endsWith('/initial-install') && options?.method === 'POST')
        return new Promise<Response>((_resolve, reject) => {
          rejectInstall = reject;
        });
      return Promise.reject(new Error('unexpected Key API: ' + address));
    });
    await mount(fetchMock, onCreationPending);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'open-key-' + key.id }).props.onClick()
    );
    await click(t('keysCheckInstallation'));
    await act(async () => button(t('keysInstallOne')).props.onClick());
    expect(onCreationPending).toHaveBeenLastCalledWith(true);

    // The separate Revocation component remains mounted while Key Details is open.
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
    await act(async () => rejectInstall(new Error('install response lost')));
    expect(onCreationPending).toHaveBeenLastCalledWith(true);
    await click(t('keysCheckInstallation'));
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

  it('does not treat malformed or Gateway-effective state as a valid list', async () => {
    await mount(
      vi
        .fn<typeof fetch>()
        .mockResolvedValue(
          Response.json({ keys: [key], gatewayPending: false, revocationAvailable: true })
        )
    );
    expect(view()).not.toContain(key.id);
    expect(button(t('keysCreate')).props.disabled).toBe(true);
    expect(view()).toContain(t('keysUnavailable'));
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

  it.each(['not_applied', 'effect_unknown'])(
    'reports unknown Gateway effect rather than a saved-key count for %s metadata',
    async (runtimeState) => {
      const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(
        Response.json({
          keys: [{ ...key, runtimeState }],
          gatewayPending: true,
          revocationAvailable: false,
        })
      );
      await mount(fetchMock);
      const runtime = renderer.root.findByProps({ 'data-testid': 'key-summary-runtime' });
      expect(runtime.findByType('strong').findByType('span').children).toEqual([
        k('effectUnknown'),
      ]);
      expect(
        renderer.root.findByProps({ 'data-testid': 'key-summary-saved' }).findByType('strong')
          .children
      ).toEqual(['1']);
      expect(fetchMock).toHaveBeenCalledOnce();
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
});
