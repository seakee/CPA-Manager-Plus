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
    gatewayPending?: boolean,
    accessInfoResponse?: unknown,
    onRebind?: () => void,
    onBlocker?: (code: string, detail?: string) => void
  ) {
    const routedFetch = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys/' + id + '/usage')) {
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
      if (url.endsWith('/api-keys/access-info')) {
        return Promise.resolve(
          Response.json(
            accessInfoResponse ?? {
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
              ],
              ready: true,
            }
          )
        );
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
          onRebind={onRebind}
          onBlocker={onBlocker}
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

  it('shows effect-confirmed runtime status and persistent API access without manual S1 install for ordinary managed flow', async () => {
    const effectiveKey = { ...key, runtimeState: 'effective_observed' as const };
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json(effectiveKey));
    await mount(fetchMock, effectiveKey.id, undefined, false);
    expect(renderer.root.findByProps({ 'data-testid': 'detail-runtime-state' }).props.children).toBe(
      k('runtimeEffectiveObserved')
    );
    expect(renderer.root.findByProps({ 'data-testid': 'detail-effective-help' }).props.children).toBe(
      k('detailEffectiveHelp')
    );
    expect(view()).not.toContain(t('keysInstallOne'));
    expect(view()).not.toContain(t('keysCheckInstallation'));
    expect(view()).not.toContain(k('advancedInstallation'));
    expect(
      renderer.root.findByProps({ 'data-testid': 'detail-access-info-protocol-base-openai' }).props
        .children
    ).toBe('https://manager.example/api/v1');
  });

  it('blocks close during apply retry and switches to read-only reconcile if apply reply is lost', async () => {
    let rejectApply!: (reason: Error) => void;
    const unapplied = { ...key, runtimeState: 'not_applied' as const };
    const effective = { ...key, runtimeState: 'effective_observed' as const };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(unapplied))
      .mockReturnValueOnce(
        new Promise<Response>((_resolve, reject) => {
          rejectApply = reject;
        })
      )
      .mockResolvedValueOnce(Response.json(effective));
    await mount(fetchMock, unapplied.id, undefined, false);
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'detail-retry-apply' }).props.onClick();
      button(t('keysCloseDetails')).props.onClick();
    });
    expect(close).not.toHaveBeenCalled();
    expect(button(t('keysCloseDetails')).props.disabled).toBe(true);
    expect(button(t('keysReadDetails')).props.disabled).toBe(true);
    expect(pending).toHaveBeenLastCalledWith(true);
    expect(fetchMock.mock.calls[1]).toMatchObject([
      'https://manager.example/console/api/v2/api-keys',
      { method: 'POST', body: JSON.stringify({ requestId: unapplied.requestId }) },
    ]);

    await act(async () => rejectApply(new Error('lost apply reply')));
    expect(button(t('keysCloseDetails')).props.disabled).toBe(true);
    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'detail-reconcile-apply' }).props.onClick();
    });
    expect(fetchMock.mock.calls[2]).toMatchObject([
      'https://manager.example/console/api/v2/api-keys/requests/' + unapplied.requestId,
      { method: 'GET' },
    ]);
    expect(pending).toHaveBeenLastCalledWith(false);
    expect(button(t('keysCloseDetails')).props.disabled).toBe(false);
    expect(saved).toHaveBeenLastCalledWith(effective);
    await act(async () => button(t('keysCloseDetails')).props.onClick());
    expect(close).toHaveBeenCalledOnce();
  });

  it('discards stale detail replies when reloadToken changes mid-flight', async () => {
    let resolveOld!: (res: Response) => void;
    const oldKey = { ...key, name: 'stale-before-rebind' };
    const newKey = { ...key, name: 'fresh-after-rebind', runtimeState: 'effective_observed' as const };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementation((input) => {
        const url = String(input);
        if (url.endsWith('/usage')) {
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
        }
        if (url.endsWith('/api-keys/' + key.id)) {
          return fetchMock.mock.calls.filter(([u]) => String(u).endsWith('/api-keys/' + key.id)).length === 1
            ? new Promise<Response>((resolve) => {
                resolveOld = resolve;
              })
            : Promise.resolve(Response.json(newKey));
        }
        return Promise.resolve(Response.json({}));
      });
    await act(async () => {
      renderer = create(
        <FreshKeyDetails
          id={key.id}
          client={createPublicApiClient(() => context, fetchMock)}
          gatewayPending={false}
          accessInfo={{
            mode: 'connect_external',
            managementBasePath: '/console',
            inferenceBasePath: '',
            externalBaseUrl: 'https://cpa.remote.example',
            protocols: [],
            ready: false,
            code: 'external_target_unavailable',
          }}
          reloadToken={0}
          credential="admin-memory"
          onUnauthorized={unauthorized}
          onPending={pending}
          onSaved={saved}
          onClose={close}
        />
      );
    });
    await act(async () => {
      renderer.update(
        <FreshKeyDetails
          id={key.id}
          client={createPublicApiClient(() => context, fetchMock)}
          gatewayPending={false}
          accessInfo={{
            mode: 'connect_external',
            managementBasePath: '/console',
            inferenceBasePath: '',
            externalBaseUrl: 'https://cpa.remote.example',
            protocols: [],
            ready: false,
            code: 'external_target_unavailable',
          }}
          reloadToken={1}
          credential="admin-memory"
          onUnauthorized={unauthorized}
          onPending={pending}
          onSaved={saved}
          onClose={close}
        />
      );
    });
    expect(view()).toContain('fresh-after-rebind');
    await act(async () => resolveOld(Response.json(oldKey)));
    expect(view()).toContain('fresh-after-rebind');
    expect(view()).not.toContain('stale-before-rebind');
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

  it('refreshes a concurrently retired key after a secret-copy conflict', async () => {
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const retired = { ...key, lifecycle: 'superseded', runtimeState: 'revoked_observed' };
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(key))
      .mockResolvedValueOnce(new Response(null, { status: 409 }))
      .mockResolvedValueOnce(Response.json(retired));
    await mount(fetchMock);
    await act(async () => renderer.root.findByProps({ 'data-testid': 'copy-saved-client-secret' }).props.onClick());
    expect(renderer.root.findByProps({ 'data-testid': 'copy-saved-client-secret' }).props.disabled).toBe(true);
    expect(saved).toHaveBeenLastCalledWith(retired);
    expect(writeText).not.toHaveBeenCalled();
  });

  it('shows connectionPending when gatewayPending is true and hides it once admitted', async () => {
    await mount(vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json(key)));
    expect(view()).toContain(k('connectionPending'));

    await act(async () => renderer.unmount());
    await mount(vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json(key)), key.id, undefined, false);
    expect(view()).not.toContain(k('connectionPending'));
  });

  it('displays missing lifecycle safely as non-retired while keeping superseded disabled', async () => {
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const missingKey = { ...key, lifecycle: 'missing' as const, runtimeState: 'not_applied' as const };
    const fetchMock = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(missingKey))
      .mockResolvedValueOnce(Response.json({ secret: 'cpamp_sk_' + 'c'.repeat(64) }));
    await mount(fetchMock, missingKey.id, undefined, false);
    expect(renderer.root.findByProps({ 'data-testid': 'detail-lifecycle-state' }).props.children).toBe(
      k('missingLifecycle')
    );
    expect(renderer.root.findByProps({ 'data-testid': 'copy-saved-client-secret' }).props.disabled).toBe(
      false
    );
    expect(renderer.root.findByProps({ 'data-testid': 'detail-retry-apply' }).props.disabled).toBe(false);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'copy-saved-client-secret' }).props.onClick()
    );
    expect(writeText).toHaveBeenCalledTimes(1);
  });

  it('consumes 503 aborted operation snapshot in reconcileApply, refetches canonical not_applied key, and ignores mismatched target snapshots', async () => {
    const opId = 'd'.repeat(32);
    const unknownKey = {
      ...key,
      runtimeState: 'effect_unknown' as const,
      operationId: opId,
    };
    const notAppliedKey = {
      ...key,
      runtimeState: 'not_applied' as const,
      operationId: '',
      effectCode: '',
    };
    const wrongTargetOp = {
      id: opId,
      targetId: 'f'.repeat(32),
      keeperId: '',
      mode: 'add' as const,
      outcome: 'aborted' as const,
      revision: 2,
      steps: [{ name: 'add' as const, state: 'unattempted' as const, returned: false }],
    };
    const abortedOp = { ...wrongTargetOp, targetId: key.id };
    let opCall = 0;
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys/' + key.id) && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(Response.json(opCall < 2 ? unknownKey : notAppliedKey));
      }
      if (url.endsWith('/api-keys/requests/' + key.requestId)) {
        return Promise.resolve(Response.json(unknownKey));
      }
      if (url.endsWith('/api-keys/operations/' + opId)) {
        opCall += 1;
        return Promise.resolve(
          Response.json(
            {
              code: 'key_operation_unknown',
              result: {
                operation: opCall === 1 ? wrongTargetOp : abortedOp,
                nextStep: '',
                runtimeState: 'not_applied',
                expiresAtMs: 0,
              },
            },
            { status: 503 }
          )
        );
      }
      return Promise.reject(new Error('unexpected ' + url));
    });

    await mount(fetchMock, key.id, undefined, false);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-reconcile-apply' }).props.onClick()
    );
    // Mismatched target snapshot is not consumed to unlock retry.
    expect(renderer.root.findByProps({ 'data-testid': 'detail-runtime-state' }).props.children).toBe(
      k('runtimeEffectUnknown')
    );
    expect(renderer.root.findAllByProps({ 'data-testid': 'detail-retry-apply' })).toHaveLength(0);

    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-reconcile-apply' }).props.onClick()
    );
    expect(renderer.root.findByProps({ 'data-testid': 'detail-runtime-state' }).props.children).toBe(
      k('runtimeNotApplied')
    );
    expect(view()).toContain(k('operationAborted'));
    expect(renderer.root.findAllByProps({ 'data-testid': 'detail-retry-apply' })).toHaveLength(1);
    expect(saved).toHaveBeenLastCalledWith(notAppliedKey);
    expect(pending).toHaveBeenLastCalledWith(false);
    await act(async () => button(t('keysCloseDetails')).props.onClick());
    expect(close).toHaveBeenCalledTimes(1);
  });

  it('preserves client_forbidden detail, notifies global blocker, hides invalid retry, and surfaces onRebind on aborted 409 and target_moved', async () => {
    const rebind = vi.fn();
    const onBlocker = vi.fn();
    const opId = 'd'.repeat(32);
    const unknownKey = {
      ...key,
      runtimeState: 'effect_unknown' as const,
      operationId: opId,
    };
    const blockedUnknownKey = {
      ...unknownKey,
      operationId: '',
      effectCode: 'external_management_key_rejected',
    };
    const abortedOp = {
      id: opId,
      targetId: key.id,
      keeperId: '',
      mode: 'add' as const,
      outcome: 'aborted' as const,
      revision: 2,
      steps: [{ name: 'add' as const, state: 'unattempted' as const, returned: false }],
    };
    let refreshedAfterOp = false;
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.endsWith('/api-keys/' + key.id)) {
        return Promise.resolve(Response.json(refreshedAfterOp ? blockedUnknownKey : unknownKey));
      }
      if (url.endsWith('/api-keys/requests/' + key.requestId)) {
        return Promise.resolve(Response.json(unknownKey));
      }
      if (url.endsWith('/api-keys/operations/' + opId)) {
        refreshedAfterOp = true;
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
      return Promise.reject(new Error('unexpected ' + url));
    });

    await mount(fetchMock, key.id, undefined, false, undefined, rebind, onBlocker);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-reconcile-apply' }).props.onClick()
    );
    expect(renderer.root.findByProps({ 'data-testid': 'detail-apply-aborted' }).props.children).toBe(
      k('operationAborted')
    );
    expect(
      renderer.root.findByProps({ 'data-testid': 'detail-apply-blocker-reason' }).props.children
    ).toBe(k('effectBlockerExternalRejected'));
    expect(
      JSON.stringify(
        renderer.root.findByProps({ 'data-testid': 'detail-apply-blocker-detail' }).findByType('p').props.children
      )
    ).toContain('client_forbidden');
    expect(onBlocker).toHaveBeenCalledWith('external_management_key_rejected', 'client_forbidden');
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-rebind-action' }).props.onClick()
    );
    expect(rebind).toHaveBeenCalledTimes(1);

    // Target moved on retryApply hides invalid retry and exposes Rebind escape.
    await act(async () => renderer.unmount());
    rebind.mockClear();
    onBlocker.mockClear();
    const movedKey = { ...key, runtimeState: 'not_applied' as const, effectCode: 'external_target_moved' };
    const retryMovedFetch = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys/' + key.id)) return Promise.resolve(Response.json(key));
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        return Promise.resolve(
          Response.json({ code: 'external_target_moved', key: movedKey }, { status: 409 })
        );
      }
      return Promise.reject(new Error('unexpected ' + url));
    });
    await mount(retryMovedFetch, key.id, undefined, false, undefined, rebind, onBlocker);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-retry-apply' }).props.onClick()
    );
    expect(renderer.root.findAllByProps({ 'data-testid': 'detail-retry-apply' })).toHaveLength(0);
    expect(onBlocker).toHaveBeenCalledWith('external_target_moved', undefined);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-rebind-action' }).props.onClick()
    );
    expect(rebind).toHaveBeenCalledTimes(1);
  });

  it('reapplies a previously committed missing key via fresh durable add intent while preserving canonical id, operation:null safeguard, and owner fencing', async () => {
    let seq = 0x10;
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
      ...key,
      lifecycle: 'active' as const,
      runtimeState: 'effective_observed' as const,
      effectCode: '',
      operationId: '',
    };
    const origCommittedOp = {
      id: 'e'.repeat(32),
      targetId: missingKey.id,
      keeperId: '',
      mode: 'add' as const,
      outcome: 'committed' as const,
      revision: 2,
      steps: [{ name: 'add' as const, state: 'observed' as const, returned: false }],
    };
    let freshReqId = '';
    let phase: 'initial' | 'null_op' | 'reapply_done' = 'initial';
    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys/' + missingKey.id) && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(Response.json(phase === 'reapply_done' ? activeKey : missingKey));
      }
      if (url.endsWith('/api-keys/requests/' + missingKey.requestId)) {
        return Promise.resolve(Response.json(missingKey));
      }
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        expect(JSON.parse(String(init.body))).toEqual({ requestId: missingKey.requestId });
        return Promise.resolve(
          Response.json(
            {
              code: 'key_operation_unknown',
              key: { ...missingKey, effectCode: 'key_operation_unknown' },
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
        const payload = JSON.parse(String(init.body));
        expect(payload.keeperId).toBe('');
        expect(payload.mode).toBe('add');
        expect(payload.requestId).not.toBe(missingKey.requestId);
        freshReqId = payload.requestId;
        if (phase === 'null_op') {
          return Promise.resolve(
            Response.json({
              operation: null,
              nextStep: '',
              runtimeState: 'effective_observed',
              expiresAtMs: 0,
            })
          );
        }
        return Promise.resolve(
          Response.json({
            operation: {
              id: freshReqId,
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
      if (url.endsWith('/api-keys/operations/' + freshReqId) && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(
          Response.json({
            operation: null,
            nextStep: '',
            runtimeState: 'effect_unknown',
            expiresAtMs: 0,
          })
        );
      }
      if (
        url.endsWith('/api-keys/operations/' + freshReqId + '/continue') &&
        init?.method === 'POST'
      ) {
        phase = 'reapply_done';
        return Promise.resolve(
          Response.json({
            operation: {
              id: freshReqId,
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
      return Promise.reject(new Error('unexpected ' + url));
    });

    // 1. operation: null on prepare must fail closed (never treat as effective) and reconcile unlocks retry.
    phase = 'null_op';
    await mount(fetchMock, missingKey.id, undefined, false);
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-retry-apply' }).props.onClick()
    );
    expect(renderer.root.findByProps({ 'data-testid': 'detail-runtime-state' }).props.children).toBe(
      k('runtimeNotApplied')
    );
    expect(renderer.root.findAllByProps({ 'data-testid': 'detail-reconcile-apply' })).toHaveLength(1);

    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-reconcile-apply' }).props.onClick()
    );
    expect(renderer.root.findAllByProps({ 'data-testid': 'detail-retry-apply' })).toHaveLength(1);

    // 2. Explicit retry succeeds via fresh durable add intent and preserves canonical id/requestId.
    phase = 'initial';
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-retry-apply' }).props.onClick()
    );
    expect(renderer.root.findByProps({ 'data-testid': 'detail-runtime-state' }).props.children).toBe(
      k('runtimeEffectiveObserved')
    );
    expect(saved).toHaveBeenLastCalledWith(activeKey);
    expect(pending).toHaveBeenLastCalledWith(false);

    // 3. Owner fencing: if key id/credential switches while reapply is in flight, stale completion does not leak.
    await act(async () => renderer.unmount());
    saved.mockClear();
    let releaseContinue: (() => void) | null = null;
    const otherId = 'f'.repeat(32);
    const otherKey = { ...activeKey, id: otherId, requestId: '9'.repeat(32) };
    const gatedFetch = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith('/api-keys/' + missingKey.id + '/usage') || url.endsWith('/api-keys/' + otherId + '/usage')) {
        return Promise.resolve(
          Response.json({
            keyId: url.includes(missingKey.id) ? missingKey.id : otherId,
            fromMs: 1,
            toMs: 2,
            bucketMs: 1,
            coverage: { state: 'unknown', reason: 'usage_read_failed', collectingSinceMs: 0, lastProcessedEventId: 0, targetEventId: 0 },
            totals: null,
            series: [],
          })
        );
      }
      if (url.endsWith('/api-keys/access-info')) {
        return Promise.resolve(Response.json({ mode: 'new_managed', managementBasePath: '/console', inferenceBasePath: '/api', protocols: [], ready: true }));
      }
      if (url.endsWith('/api-keys/' + missingKey.id)) return Promise.resolve(Response.json(missingKey));
      if (url.endsWith('/api-keys/' + otherId)) return Promise.resolve(Response.json(otherKey));
      if (url.endsWith('/api-keys') && init?.method === 'POST') {
        return Promise.resolve(
          Response.json(
            {
              code: 'key_operation_unknown',
              key: missingKey,
              result: { operation: origCommittedOp, nextStep: '', runtimeState: 'not_applied', expiresAtMs: 0 },
            },
            { status: 503 }
          )
        );
      }
      if (url.endsWith('/api-keys/' + missingKey.id + '/revocation')) {
        const payload = JSON.parse(String(init?.body));
        freshReqId = payload.requestId;
        return Promise.resolve(
          Response.json({
            operation: {
              id: freshReqId,
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
      if (url.endsWith('/api-keys/operations/' + freshReqId + '/continue')) {
        return new Promise<Response>((resolve) => {
          releaseContinue = () =>
            resolve(
              Response.json({
                operation: {
                  id: freshReqId,
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
        });
      }
      return Promise.reject(new Error('unexpected ' + url));
    });
    const client = createPublicApiClient(() => context, gatedFetch);
    await act(async () => {
      renderer = create(
        <FreshKeyDetails
          id={missingKey.id}
          key={missingKey.id}
          client={client}
          gatewayPending={false}
          credential="admin-memory"
          onUnauthorized={unauthorized}
          onPending={pending}
          onSaved={saved}
          onClose={close}
        />
      );
    });
    let reapplyPromise!: Promise<void>;
    await act(async () => {
      reapplyPromise = renderer.root
        .findByProps({ 'data-testid': 'detail-retry-apply' })
        .props.onClick() as Promise<void>;
      for (let i = 0; !releaseContinue && i < 50; i += 1) await Promise.resolve();
      expect(releaseContinue).not.toBeNull();
    });
    // Switch owner while continue is pending.
    await act(async () => {
      renderer.update(
        <FreshKeyDetails
          id={otherId}
          key={otherId}
          client={client}
          gatewayPending={false}
          credential="admin-memory"
          onUnauthorized={unauthorized}
          onPending={pending}
          onSaved={saved}
          onClose={close}
        />
      );
    });
    await act(async () => {
      releaseContinue?.();
      await reapplyPromise;
    });
    expect(saved).not.toHaveBeenCalledWith(activeKey);
    expect(renderer.root.findByProps({ 'data-testid': 'detail-runtime-state' }).props.children).toBe(
      k('runtimeEffectiveObserved')
    );
  });
});
