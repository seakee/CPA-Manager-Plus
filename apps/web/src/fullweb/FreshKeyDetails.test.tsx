import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FreshKeyDetails } from './FreshKeyDetails';
import { createPublicApiClient } from './publicApi';
import { fullWebText as t } from '../i18n/fullweb';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';

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
  origin: 'workspace' as const,
};
describe('Canonical key detail and rename', () => {
  let renderer: ReactTestRenderer;
  const unauthorized = vi.fn(),
    pending = vi.fn(),
    saved = vi.fn(),
    close = vi.fn();
  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    vi.stubGlobal('window', {
      location: new URL('https://manager.example/console'),
      confirm: vi.fn(() => false),
    });
    unauthorized.mockClear();
    pending.mockClear();
    saved.mockClear();
    close.mockClear();
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
    fetchMock: typeof fetch,
    id = key.id,
    usageResponse?: unknown,
    gatewayPending?: boolean
  ) {
    const routedFetch = vi.fn<typeof fetch>((input, init) => {
      if (String(input).endsWith('/api-keys/' + id + '/usage')) {
        return Promise.resolve(Response.json(usageResponse ?? {
          keyId: id,
          fromMs: 1,
          toMs: 2,
          bucketMs: 1,
          coverage: { state: 'unknown', reason: 'usage_read_failed', collectingSinceMs: 0, lastProcessedEventId: 0, targetEventId: 0 },
          totals: null,
          series: [],
        }));
      }
      return fetchMock(input, init);
    });
    await act(async () => {
      renderer = create(
        <FreshKeyDetails
          id={id}
          key={id}
          client={createPublicApiClient(() => context, routedFetch)}
          gatewayPending={gatewayPending}
          credential="admin-memory"
          onUnauthorized={unauthorized}
          onPending={pending}
          onSaved={saved}
          onClose={close}
        />
      );
    });
  }
  async function draft(name: string) {
    await edit();
    await act(async () =>
      renderer.root.findByType('input').props.onChange({ target: { value: name } })
    );
  }
  async function edit() {
    if (!renderer.root.findAllByType('input').length)
      await act(async () => button(k('rename')).props.onClick());
  }
  async function submit() {
    await act(async () =>
      renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() })
    );
  }
  async function read() {
    await act(async () => button(t('keysReadDetails')).props.onClick());
  }

  it('reads by canonical ID, saves trimmed name with observed version, and rereads persisted details', async () => {
    const renamed = { ...key, name: '工作 client', nameRevision: 2 };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(key))
      .mockResolvedValueOnce(Response.json(renamed))
      .mockResolvedValueOnce(Response.json(renamed));
    await mount(fetchMock);
    await draft(' 工作 client ');
    await submit();
    expect(fetchMock.mock.calls[1]).toMatchObject([
      'https://manager.example/console/api/v2/api-keys/' + key.id,
      {
        method: 'PUT',
        body: JSON.stringify({ name: renamed.name, nameRevision: 1 }),
        headers: { Authorization: 'Bearer admin-memory' },
        cache: 'no-store',
      },
    ]);
    expect(view()).toContain(t('keysNameSaved'));
    expect(pending.mock.calls[pending.mock.calls.length - 1]).toEqual([false]);
    await read();
    await edit();
    expect(renderer.root.findByType('input').props.value).toBe(renamed.name);
    expect(view()).not.toContain('admin-memory');
  });

  it('blocks detail close and other edits during an S1 install and reconciles a lost response', async () => {
    let rejectInstall!: (reason: Error) => void;
    const installReady = {
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
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(key))
      .mockResolvedValueOnce(Response.json(installReady))
      .mockReturnValueOnce(
        new Promise<Response>((_resolve, reject) => {
          rejectInstall = reject;
        })
      )
      .mockResolvedValueOnce(
        Response.json({ ...installReady, attempted: true, installAvailable: false })
      );
    await mount(fetchMock);
    await edit();
    await act(async () => button(t('keysCheckInstallation')).props.onClick());
    await act(async () => {
      button(t('keysInstallOne')).props.onClick();
      button(t('keysCloseDetails')).props.onClick();
      renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
    });
    expect(close).not.toHaveBeenCalled();
    expect(button(t('keysCloseDetails')).props.disabled).toBe(true);
    expect(button(t('keysReadDetails')).props.disabled).toBe(true);
    expect(button(t('keysCheckInstallation')).props.disabled).toBe(true);
    expect(pending).toHaveBeenLastCalledWith(true);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(0);
    await act(async () => rejectInstall(new Error('lost install reply')));
    expect(button(t('keysCloseDetails')).props.disabled).toBe(true);
    expect(button(t('keysCheckInstallation')).props.disabled).toBe(false);
    await act(async () => button(t('keysCheckInstallation')).props.onClick());
    expect(pending).toHaveBeenLastCalledWith(false);
    expect(button(t('keysCloseDetails')).props.disabled).toBe(false);
    await act(async () => button(t('keysCloseDetails')).props.onClick());
    expect(close).toHaveBeenCalledOnce();
  });

  it('does not start S1 installation behind an earlier in-flight detail read', async () => {
    let finishRead!: (response: Response) => void;
    let rejectInstall!: (reason: Error) => void;
    const ready = {
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
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(key))
      .mockResolvedValueOnce(Response.json(ready))
      .mockReturnValueOnce(
        new Promise<Response>((resolve) => {
          finishRead = resolve;
        })
      )
      .mockReturnValueOnce(
        new Promise<Response>((_resolve, reject) => {
          rejectInstall = reject;
        })
      )
      .mockResolvedValueOnce(Response.json({ ...ready, attempted: true, installAvailable: false }));
    await mount(fetchMock);
    await act(async () => button(t('keysCheckInstallation')).props.onClick());
    await act(async () => {
      button(t('keysReadDetails')).props.onClick();
      // The old render has an enabled install button, but the live detail ref
      // must stop a second same-turn operation from starting.
      button(t('keysInstallOne')).props.onClick();
    });
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0);
    await act(async () => finishRead(Response.json(key)));
    await act(async () => button(t('keysInstallOne')).props.onClick());
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    expect(pending).toHaveBeenLastCalledWith(true);
    await act(async () => rejectInstall(new Error('lost reply')));
    expect(pending).toHaveBeenLastCalledWith(true);
    await act(async () => button(t('keysCheckInstallation')).props.onClick());
    expect(pending).toHaveBeenLastCalledWith(false);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
  });

  it('retains revoked details and name history without offering installation again', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValue(Response.json({ ...key, lifecycle: 'superseded' }));
    await mount(fetchMock);
    expect(view()).toContain(t('keysRevokedHistory'));
    expect(view()).toContain(key.id);
    expect(view()).not.toContain(t('keysInstallTitle'));
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
  it('guards duplicate saves and reconciles a committed but lost response without replay', async () => {
    let reject!: (reason: Error) => void;
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(key))
      .mockReturnValueOnce(
        new Promise((_resolve, fail) => {
          reject = fail;
        })
      )
      .mockResolvedValueOnce(Response.json({ ...key, name: 'saved', nameRevision: 2 }));
    await mount(fetchMock);
    await draft('saved');
    await act(async () => {
      const form = renderer.root.findByType('form');
      form.props.onSubmit({ preventDefault: vi.fn() });
      form.props.onSubmit({ preventDefault: vi.fn() });
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(pending.mock.calls[pending.mock.calls.length - 1]).toEqual([true]);
    await act(async () => reject(new Error('lost reply secret')));
    expect(view()).toContain(t('keysNameUnknown'));
    expect(view()).not.toContain('lost reply secret');
    expect(button(t('keysCloseDetails')).props.disabled).toBe(true);
    await read();
    expect(view()).toContain(t('keysNameChecked'));
    expect(renderer.root.findByType('input').props.value).toBe('saved');
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
    expect(pending.mock.calls[pending.mock.calls.length - 1]).toEqual([false]);
  });
  it.each([400, 404, 409, 503])(
    'handles real HTTP %i without claiming success or automatically retrying',
    async (status) => {
      const fetchMock = vi
        .fn<typeof fetch>()
        .mockResolvedValueOnce(Response.json(key))
        .mockResolvedValueOnce(new Response('', { status }))
        .mockResolvedValueOnce(Response.json(key));
      await mount(fetchMock);
      await draft('not saved');
      await submit();
      expect(view()).not.toContain(t('keysNameSaved'));
      expect(fetchMock).toHaveBeenCalledTimes(2);
      if (status === 503) {
        expect(view()).toContain(t('keysNameUnknown'));
        expect(pending.mock.calls[pending.mock.calls.length - 1]).toEqual([true]);
      } else {
        expect(pending.mock.calls[pending.mock.calls.length - 1]).toEqual([false]);
        vi.mocked(window.confirm).mockReturnValueOnce(true);
      }
      await read();
      expect(renderer.root.findByType('input').props.value).toBe('');
      expect(fetchMock).toHaveBeenCalledTimes(3);
    }
  );
  it.each([400, 404, 409].flatMap((status) => ['not saved', ''].map((name) => ({ status, name }))))(
    'confirms closing a rejected $status rename draft "$name"',
    async ({ status, name }) => {
      const fetchMock = vi
        .fn<typeof fetch>()
        .mockResolvedValueOnce(Response.json({ ...key, name: 'original' }))
        .mockResolvedValueOnce(new Response('', { status }));
      await mount(fetchMock);
      await draft(name);
      await submit();
      expect(renderer.root.findAllByType('form')).toHaveLength(0);
      expect(button(t('keysCloseDetails')).props.disabled).toBe(false);
      await act(async () => button(t('keysCloseDetails')).props.onClick());
      expect(window.confirm).toHaveBeenCalledWith(t('keysDiscardName'));
      expect(close).not.toHaveBeenCalled();
      vi.mocked(window.confirm).mockReturnValueOnce(true);
      await act(async () => button(t('keysCloseDetails')).props.onClick());
      expect(close).toHaveBeenCalledOnce();
      expect(fetchMock).toHaveBeenCalledTimes(2);
    }
  );
  it.each(['not saved', ''])(
    'protects a rejected draft "%s" until a confirmed reread succeeds',
    async (name) => {
      const fetchMock = vi
        .fn<typeof fetch>()
        .mockResolvedValueOnce(Response.json({ ...key, name: 'original' }))
        .mockResolvedValueOnce(new Response('', { status: 409 }))
        .mockResolvedValueOnce(new Response('', { status: 503 }))
        .mockResolvedValueOnce(Response.json({ ...key, name: 'current', nameRevision: 2 }));
      await mount(fetchMock);
      await draft(name);
      await submit();
      await read();
      expect(window.confirm).toHaveBeenCalledWith(t('keysDiscardName'));
      expect(fetchMock).toHaveBeenCalledTimes(2);
      vi.mocked(window.confirm).mockReturnValueOnce(true);
      await read();
      expect(view()).toContain(t('keysUnavailable'));
      await act(async () => button(t('keysCloseDetails')).props.onClick());
      expect(close).not.toHaveBeenCalled();
      vi.mocked(window.confirm).mockReturnValueOnce(true);
      await read();
      expect(renderer.root.findByType('input').props.value).toBe('current');
      vi.mocked(window.confirm).mockClear();
      await act(async () => button(t('keysCloseDetails')).props.onClick());
      expect(window.confirm).not.toHaveBeenCalled();
      expect(close).toHaveBeenCalledOnce();
      expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
    }
  );
  it('lets the rejected rename modal discard its draft explicitly before rereading', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json({ ...key, name: 'original' }))
      .mockResolvedValueOnce(new Response('', { status: 409 }))
      .mockResolvedValueOnce(Response.json({ ...key, name: 'concurrent', nameRevision: 2 }));
    await mount(fetchMock);
    await draft('rejected');
    await submit();
    await act(async () => button(k('cancel')).props.onClick());
    expect(window.confirm).toHaveBeenCalledWith(t('keysDiscardName'));
    expect(button(k('cancel'))).toBeDefined();
    vi.mocked(window.confirm).mockReturnValueOnce(true);
    await act(async () => button(k('cancel')).props.onClick());
    expect(
      renderer.root.findAllByType('button').some((item) => item.props.children === k('cancel'))
    ).toBe(false);
    await read();
    expect(view()).toContain('concurrent');
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
  });

  it('rejects a mismatched canonical response and clears rejected authentication', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json({ ...key, id: 'c'.repeat(32) }))
      .mockResolvedValueOnce(new Response('', { status: 401 }));
    await mount(fetchMock);
    expect(renderer.root.findAllByType('input')).toHaveLength(0);
    expect(view()).toContain(t('keysUnavailable'));
    await read();
    expect(unauthorized).toHaveBeenCalledOnce();
  });
  it.each([200, 401])(
    'ignores a delayed %i response after switching records/sessions',
    async (status) => {
      let resolve!: (response: Response) => void;
      const fetchMock = vi.fn<typeof fetch>().mockReturnValueOnce(
        new Promise((done) => {
          resolve = done;
        })
      );
      await mount(fetchMock);
      await act(async () => renderer.unmount());
      const next = { ...key, id: 'c'.repeat(32), name: 'new session' };
      await mount(vi.fn<typeof fetch>().mockResolvedValue(Response.json(next)), next.id);
      const count = saved.mock.calls.length;
      await act(async () =>
        resolve(
          status === 200
            ? Response.json({ ...key, name: 'stale record' })
            : new Response('', { status })
        )
      );
      expect(saved).toHaveBeenCalledTimes(count);
      expect(unauthorized).not.toHaveBeenCalled();
      expect(view()).toContain('new session');
    }
  );
  it('times out a hanging detail check and permits another GET while keeping unknown writes blocked', async () => {
    vi.useFakeTimers();
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(key))
      .mockRejectedValueOnce(new Error('lost'))
      .mockImplementationOnce(
        (_url, init) =>
          new Promise((_resolve, reject) =>
            init?.signal?.addEventListener('abort', () => reject(new Error('timeout')))
          )
      )
      .mockResolvedValueOnce(Response.json(key));
    await mount(fetchMock);
    await draft('unknown');
    await submit();
    await read();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15000);
    });
    expect(button(t('keysReadDetails')).props.disabled).toBe(false);
    expect(button(t('keysCloseDetails')).props.disabled).toBe(true);
    await read();
    expect(pending.mock.calls[pending.mock.calls.length - 1]).toEqual([false]);
    expect(fetchMock).toHaveBeenCalledTimes(4);
  });
  it('confirms discarding a name draft before closing and supports clearing a name', async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json({ ...key, name: 'original' }))
      .mockResolvedValueOnce(Response.json({ ...key, nameRevision: 2 }));
    await mount(fetchMock);
    await draft('');
    await act(async () => button(t('keysCloseDetails')).props.onClick());
    expect(window.confirm).toHaveBeenCalled();
    expect(close).not.toHaveBeenCalled();
    await submit();
    expect(view()).toContain(t('keysNameSaved'));
    expect(saved.mock.calls[saved.mock.calls.length - 1]?.[0].name).toBe('');
  });

  it('displays honest usage/policy boundaries and forwards quick replacement requests', async () => {
    const onRequestOperation = vi.fn();
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json(key));
    await act(async () => {
      renderer = create(
        <FreshKeyDetails
          id={key.id}
          client={createPublicApiClient(() => context, fetchMock)}
          credential="test-admin"
          onUnauthorized={unauthorized}
          onPending={pending}
          onSaved={saved}
          onClose={close}
          onRequestOperation={onRequestOperation}
        />
      );
    });
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'key-detail-usage-policy' }).length
    ).toBeGreaterThan(0);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'key-detail-metadata' }).length
    ).toBeGreaterThan(0);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-replace-key' }).props.onClick()
    );
    expect(onRequestOperation).toHaveBeenCalledWith('replace');
  });
  it('renders complete totals and hides zeroes for partial coverage', async () => {
    const completeUsage = {
      keyId: key.id,
      fromMs: 1,
      toMs: 2,
      bucketMs: 1,
      coverage: { state: 'complete', reason: 'observed', collectingSinceMs: 1, lastProcessedEventId: 2, targetEventId: 2 },
      totals: { calls: 3, success: 2, failure: 1, inputTokens: 30, outputTokens: 15, totalTokens: 45, lastUsedAtMs: 2 },
      series: [{ bucketMs: 1, calls: 3, failure: 1, totalTokens: 45 }],
    };
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json(key));
    await mount(fetchMock, key.id, completeUsage);
    expect(view()).toContain(k('usageStateComplete'));
    expect(view()).toContain(k('usageCalls'));
    expect(view()).toContain('3');
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-usage-series' })).toHaveLength(1);

    await act(async () => renderer.unmount());
    const partialFetch = vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json(key));
    await mount(partialFetch, key.id, {
      ...completeUsage,
      coverage: { ...completeUsage.coverage, state: 'partial', reason: 'raw_deleted' },
      totals: { ...completeUsage.totals, calls: 0, success: 0, failure: 0 },
      series: [],
    });
    renderer.root.findByProps({ 'data-testid': 'key-usage-totals' });
    expect(view()).toContain('—');
    expect(view()).toContain(k('usageStatePartial'));
  });
  it('publishes no totals when coverage cannot prove the window', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json(key));
    await mount(fetchMock, key.id, {
      keyId: key.id,
      fromMs: 1,
      toMs: 2,
      bucketMs: 1,
      coverage: { state: 'unknown', reason: 'collection_unproven', collectingSinceMs: 0, lastProcessedEventId: 0, targetEventId: 0 },
      totals: null,
      series: [],
    });
    expect(view()).toContain(k('usageStateUnknown'));
    expect(renderer.root.findByProps({ 'data-testid': 'key-usage-reason' }).props.children).toBe(
      k('usageReasonCollectionUnproven')
    );
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-usage-totals' })).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-usage-series' })).toHaveLength(0);
  });

  it('copies an adopted key once the returned secret reproduces its own mask', async () => {
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const adopted = { ...key, origin: 'adopted' as const, maskedPreview: '…-two', name: '' };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(adopted))
      .mockResolvedValueOnce(Response.json({ secret: 'source-client-two' }));
    await mount(fetchMock, adopted.id);
    expect(view()).toContain(k('adoptedOrigin'));
    await act(async () => renderer.root.findByProps({ 'data-testid': 'copy-saved-client-secret' }).props.onClick());
    expect(fetchMock.mock.calls[1]).toMatchObject([
      'https://manager.example/console/api/v2/api-keys/' + adopted.id + '/secret',
      { method: 'POST', cache: 'no-store' },
    ]);
    expect(writeText).toHaveBeenCalledWith('source-client-two');
    expect(renderer.root.findByProps({ 'data-testid': 'copy-saved-client-secret-feedback' }).props.children).toBe(
      k('keyCopied')
    );
  });

  it('refuses an adopted secret that does not reproduce the stored mask', async () => {
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const adopted = { ...key, origin: 'adopted' as const, maskedPreview: '…-two', name: '' };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(adopted))
      .mockResolvedValueOnce(Response.json({ secret: 'source-client-one' }));
    await mount(fetchMock, adopted.id);
    await act(async () => renderer.root.findByProps({ 'data-testid': 'copy-saved-client-secret' }).props.onClick());
    expect(writeText).not.toHaveBeenCalled();
    expect(renderer.root.findByProps({ 'data-testid': 'copy-saved-client-secret-feedback' }).props.children).toBe(
      k('keyCopyFailed')
    );
    expect(view()).not.toContain('source-client-one');
  });

  it('shows connectionPending when gatewayPending is true and hides it once admitted', async () => {
    await mount(vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json(key)));
    expect(view()).toContain(k('connectionPending'));

    await act(async () => renderer.unmount());
    await mount(vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json(key)), key.id, undefined, false);
    expect(view()).not.toContain(k('connectionPending'));
  });
});
