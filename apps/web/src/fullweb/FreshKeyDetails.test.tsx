import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FreshKeyDetails } from './FreshKeyDetails';
import { createPublicApiClient } from './publicApi';
import { fullWebText as t } from '../i18n/fullweb';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { workspaceDrawerStyles } from '../components/common/WorkspaceDrawer';

// Renderer tests exercise domain state; browser acceptance covers the shared
// portal, focus, animation and close controls.
vi.mock('../components/ui/Modal', () => ({
  Modal: ({
    open,
    closeDisabled,
    className,
    children,
    footer,
  }: {
    open: boolean;
    closeDisabled?: boolean;
    className?: string;
    children?: ReactNode;
    footer?: ReactNode;
  }) =>
    open ? (
      <div className={className} data-close-disabled={String(Boolean(closeDisabled))}>
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
    expect(view()).not.toContain(t('keysNameSaved'));
    expect(view()).toContain(renamed.name);
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
    expect(renderer.root.findAllByProps({ 'data-testid': 'detail-lifecycle-state' })).toHaveLength(0);
    expect(renderer.root.findByProps({ 'data-testid': 'detail-runtime-state' }).props.children).toBe(
      k('statusActive')
    );
    expect(renderer.root.findAllByProps({ 'data-testid': 'detail-effective-help' })).toHaveLength(0);
    expect(view()).not.toContain(t('keysInstallOne'));
    expect(view()).not.toContain(t('keysCheckInstallation'));
    expect(view()).not.toContain(k('advancedInstallation'));
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'detail-access-info-management' })
    ).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'detail-access-info-mode' })).toHaveLength(
      0
    );
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'detail-access-info-protocols-disclosure' })
    ).toHaveLength(0);
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
  it('guards duplicate saves and reconciles a committed but lost response via refresh-and-retry without replay', async () => {
    let resolveDeferred!: (value: Response | PromiseLike<Response>) => void;
    let rejectDeferred!: (reason?: unknown) => void;
    const deferred = {
      promise: new Promise<Response>((res, rej) => {
        resolveDeferred = res;
        rejectDeferred = rej;
      }),
      resolve: (val: Response) => resolveDeferred(val),
      reject: (err?: unknown) => rejectDeferred(err),
    };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(key))
      .mockReturnValueOnce(deferred.promise)
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
    await act(async () => deferred.reject(new Error('lost reply secret')));
    expect(view()).toContain(t('keysNameUnknown'));
    expect(view()).not.toContain('lost reply secret');
    expect(
      renderer.root.find((n) => n.type === 'div' && n.props.className === 'key-rename-modal').props[
        'data-close-disabled'
      ]
    ).toBe('false');
    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'rename-refresh-retry' }).props.onClick();
    });
    expect(saved).toHaveBeenLastCalledWith(
      expect.objectContaining({ name: 'saved', nameRevision: 2 })
    );
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
    expect(pending.mock.calls[pending.mock.calls.length - 1]).toEqual([false]);
    expect(renderer.root.findAll((n) => n.type === 'div' && n.props.className === 'key-rename-modal')).toHaveLength(0);
  });
  it('preserves authoritative GET runtime downgrades during rename refresh-and-retry instead of masking them', async () => {
    const activeKey = { ...key, runtimeState: 'effective_observed' as const, operationId: '' };
    const degradedLatest = {
      ...activeKey,
      name: 'concurrent',
      nameRevision: 2,
      runtimeState: 'not_applied' as const,
      operationId: '',
    };
    const retriedPut = {
      ...degradedLatest,
      name: 'renamed-after-drift',
      nameRevision: 3,
      runtimeState: 'not_applied' as const,
      operationId: '',
    };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(activeKey))
      .mockResolvedValueOnce(new Response('', { status: 409 }))
      .mockResolvedValueOnce(Response.json(degradedLatest))
      .mockResolvedValueOnce(Response.json(retriedPut));
    await mount(fetchMock);
    await draft('renamed-after-drift');
    await submit();
    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'rename-refresh-retry' }).props.onClick();
    });
    expect(saved).toHaveBeenLastCalledWith(
      expect.objectContaining({
        name: 'renamed-after-drift',
        nameRevision: 3,
        runtimeState: 'not_applied',
      })
    );
    expect(
      renderer.root.findByProps({ 'data-testid': 'detail-runtime-state' }).props.children
    ).toBe(k('statusNotApplied'));
  });
  it.each([400, 404, 409, 503])(
    'handles HTTP %i in rename modal while preserving draft and offering refresh-and-retry',
    async (status) => {
      const updated = { ...key, name: 'not saved', nameRevision: 3 };
      const fetchMock = vi
        .fn<typeof fetch>()
        .mockResolvedValueOnce(Response.json(key))
        .mockResolvedValueOnce(new Response('', { status }))
        .mockResolvedValueOnce(Response.json({ ...key, name: 'concurrent', nameRevision: 2 }))
        .mockResolvedValueOnce(Response.json(updated));
      await mount(fetchMock);
      await draft('not saved');
      await submit();
      expect(view()).not.toContain(t('keysNameSaved'));
      expect(fetchMock).toHaveBeenCalledTimes(2);
      expect(renderer.root.findByType('input').props.value).toBe('not saved');
      expect(
        renderer.root.findAllByProps({ 'data-testid': 'rename-refresh-retry' })
      ).toHaveLength(1);
      if (status === 503) {
        expect(view()).toContain(t('keysNameUnknown'));
        expect(pending.mock.calls[pending.mock.calls.length - 1]).toEqual([true]);
      } else {
        expect(pending.mock.calls[pending.mock.calls.length - 1]).toEqual([false]);
      }
      await act(async () => {
        await renderer.root.findByProps({ 'data-testid': 'rename-refresh-retry' }).props.onClick();
      });
      if (status === 409 || status === 503) {
        expect(fetchMock.mock.calls[3][1]?.method).toBe('PUT');
        expect(JSON.parse(String(fetchMock.mock.calls[3][1]?.body))).toEqual({
          name: 'not saved',
          nameRevision: 2,
        });
      } else {
        expect(renderer.root.findByType('input').props.value).toBe('not saved');
      }
      expect(pending.mock.calls[pending.mock.calls.length - 1]).toEqual([false]);
    }
  );
  it('closes rename modal cleanly without window.confirm and avoids Initial key prefill', async () => {
    const installerDefault = {
      ...key,
      name: 'Initial key',
      origin: 'installer' as const,
      maskedPreview: 'sk-…cccc',
      nameRevision: 1,
    };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(installerDefault))
      .mockResolvedValueOnce(
        Response.json({ ...installerDefault, name: 'My Laptop', nameRevision: 2 })
      );
    await mount(fetchMock);
    await edit();
    expect(renderer.root.findByType('input').props.value).toBe('');
    await act(async () =>
      renderer.root.findByType('input').props.onChange({ target: { value: 'Draft' } })
    );
    await act(async () => button(k('cancel')).props.onClick());
    expect(window.confirm).not.toHaveBeenCalled();
    expect(renderer.root.findAll((n) => n.type === 'div' && n.props.className === 'key-rename-modal')).toHaveLength(0);

    await edit();
    await act(async () =>
      renderer.root.findByType('input').props.onChange({ target: { value: 'My Laptop' } })
    );
    await submit();
    expect(JSON.parse(String(fetchMock.mock.calls[1][1]?.body))).toEqual({
      name: 'My Laptop',
      nameRevision: 1,
    });
    expect(saved).toHaveBeenLastCalledWith(
      expect.objectContaining({ name: 'My Laptop', nameRevision: 2 })
    );
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
  it('surfaces lastRotatedAtMs, revokedAtMs, diagnostics details, regenerate, and revoke buttons cleanly', async () => {
    const onRequestOperation = vi.fn();
    const rotatedAndRevoked = {
      ...key,
      lastRotatedAtMs: 1_700_000_500_000,
      revokedAtMs: 1_700_000_900_000,
      operationId: 'f'.repeat(32),
    };
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json(rotatedAndRevoked));
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
    const readText = (node: unknown): string => {
      if (typeof node === 'string' || typeof node === 'number') return String(node);
      if (!node || typeof node !== 'object' || !('children' in node)) return '';
      const children = (node as { children?: unknown[] }).children;
      return Array.isArray(children) ? children.map(readText).join(' ') : '';
    };
    const metaText = readText(renderer.root.findByProps({ 'data-testid': 'key-detail-metadata' }));
    expect(metaText).toContain(k('revokedAtLabel'));
    expect(metaText).not.toContain(k('revisionLabel'));
    expect(metaText).toContain(t('keysIdentity'));
    expect(metaText).toContain(key.id);
    expect(renderer.root.findAllByProps({ className: 'key-detail-footer' })).toHaveLength(0);
    expect(view()).not.toContain(k('policyGlobalHelp'));
    expect(renderer.root.findByProps({ 'data-testid': 'detail-operation-id' }).props.children).toBe(
      'f'.repeat(32)
    );

    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-regenerate-key' }).props.onClick()
    );
    expect(onRequestOperation).toHaveBeenCalledWith('regenerate');
    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-revoke-key' }).props.onClick()
    );
    expect(onRequestOperation).toHaveBeenCalledWith('revoke');
  });
  it('renders semantic usage bars, 24h/7d/30d window queries, empty state on zero partial calls, and cross-page links', async () => {
    const completeUsage = {
      keyId: key.id,
      fromMs: 1,
      toMs: 2,
      bucketMs: 1,
      coverage: { state: 'complete', reason: 'observed', collectingSinceMs: 1, lastProcessedEventId: 2, targetEventId: 2 },
      totals: { calls: 3, success: 2, failure: 1, inputTokens: 30, outputTokens: 15, totalTokens: 45, lastUsedAtMs: 2 },
      series: [{ bucketMs: 1, calls: 3, failure: 1, totalTokens: 45 }],
    };
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.includes('/usage')) {
        return Promise.resolve(Response.json(completeUsage));
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
      return Promise.resolve(Response.json(key));
    });
    await act(async () => {
      renderer = create(
        <FreshKeyDetails
          id={key.id}
          client={createPublicApiClient(() => context, fetchMock)}
          credential="admin-memory"
          onUnauthorized={unauthorized}
          onPending={pending}
          onSaved={saved}
          onClose={close}
        />
      );
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-usage-state' })).toHaveLength(0);
    expect(view()).not.toContain(k('usageWindowHelp'));
    expect(view()).toContain(k('usageCalls'));
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-usage-series' })).toHaveLength(1);
    expect(renderer.root.findAllByProps({ className: 'key-usage-bars' })).toHaveLength(1);

    // Switch usage window to 7d -> fetches bounded ?from_ms=...&to_ms=... query.
    const window7dTab = renderer.root
      .findAllByProps({ role: 'tab' })
      .find((node) => node.props.id === 'key-usage-window-7d')!;
    await act(async () => {
      window7dTab.props.onClick();
    });
    expect(
      fetchMock.mock.calls.some(([url]) =>
        new RegExp(`/api-keys/${key.id}/usage\\?from_ms=\\d+&to_ms=\\d+$`).test(String(url))
      )
    ).toBe(true);

    const links = renderer.root
      .findByProps({ 'data-testid': 'key-usage-links' })
      .findAllByType('a')
      .map((a) => a.props.href);
    expect(links).toEqual([
      `/monitoring?apiKeyId=${key.id}`,
      `/usage-analytics?apiKeyId=${key.id}`,
    ]);

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
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-usage-empty' })).toHaveLength(1);
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
    expect(renderer.root.findAllByProps({ 'data-testid': 'copy-saved-client-secret' })).toHaveLength(0);
    expect(renderer.root.findAllByProps({ 'data-testid': 'detail-copy-secret' })).toHaveLength(0);
    expect(renderer.root.findByProps({ 'data-testid': 'detail-runtime-state' }).props.children).toBe(
      k('statusRevoked')
    );
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
    expect(renderer.root.findAllByProps({ 'data-testid': 'detail-lifecycle-state' })).toHaveLength(0);
    expect(renderer.root.findByProps({ 'data-testid': 'detail-runtime-state' }).props.children).toBe(
      k('statusMissing')
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
      k('statusSyncing')
    );
    expect(renderer.root.findAllByProps({ 'data-testid': 'detail-retry-apply' })).toHaveLength(0);

    await act(async () =>
      renderer.root.findByProps({ 'data-testid': 'detail-reconcile-apply' }).props.onClick()
    );
    expect(renderer.root.findByProps({ 'data-testid': 'detail-runtime-state' }).props.children).toBe(
      k('statusNotApplied')
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
      k('statusMissing')
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
      k('statusActive')
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
      k('statusActive')
    );
  });

  it('switches between overview and usage tabs and supports opening directly into rename mode', async () => {
    const activeKey = { ...key, runtimeState: 'effective_observed' as const };
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url.includes('/usage')) {
        return Response.json({
          keyId: activeKey.id,
          fromMs: 1_700_000_000_000,
          toMs: 1_700_086_400_000,
          bucketMs: 3_600_000,
          coverage: {
            state: 'complete',
            reason: 'observed',
            collectingSinceMs: 1_700_000_000_000,
            lastProcessedEventId: 1,
            targetEventId: 1,
          },
          totals: {
            calls: 5,
            success: 5,
            failure: 0,
            inputTokens: 100,
            outputTokens: 50,
            totalTokens: 150,
            lastUsedAtMs: 1_700_050_000_000,
          },
          series: [],
        });
      }
      return Response.json(activeKey);
    });
    const client = createPublicApiClient(() => context, fetchMock);
    await act(async () => {
      renderer = create(
        <FreshKeyDetails
          id={activeKey.id}
          client={client}
          gatewayPending={false}
          credential="admin-memory"
          onUnauthorized={unauthorized}
          onPending={pending}
          onSaved={saved}
          onClose={close}
        />
      );
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-detail-panel-overview' }).props.hidden
    ).toBe(false);
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-detail-panel-usage' }).props.hidden
    ).toBe(true);

    const usageTabButton = renderer.root
      .findAllByProps({ role: 'tab' })
      .find((node) => JSON.stringify(node.children).includes(k('usageTab')))!;
    await act(async () => {
      usageTabButton.props.onClick();
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-detail-panel-overview' }).props.hidden
    ).toBe(true);
    expect(
      renderer.root.findByProps({ 'data-testid': 'key-detail-panel-usage' }).props.hidden
    ).toBe(false);

    await act(async () => {
      renderer.update(
        <FreshKeyDetails
          id={activeKey.id}
          initialAction="rename"
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
    expect(renderer.root.findByProps({ id: 'key-name' })).toBeTruthy();
  });

  it('allows revealing and copying saved secret for an adopted or installer key after rotation to cpamp_sk_', async () => {
    const rotatedAdoptedKey = {
      ...key,
      runtimeState: 'effective_observed' as const,
      origin: 'adopted' as const,
      maskedPreview: 'cpamp_sk_…89ab',
      lastRotatedAtMs: 1_760_000_005_000,
    };
    const rotatedSecret = `cpamp_sk_${'0'.repeat(60)}89ab`;
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(globalThis.navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    });
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input, init) => {
      const url = String(input);
      const method = (init?.method ?? 'GET').toUpperCase();
      if (method === 'GET' && url.endsWith(`/api-keys/${rotatedAdoptedKey.id}`)) {
        return Response.json(rotatedAdoptedKey);
      }
      if (method === 'GET' && url.includes('/usage')) {
        return Response.json({
          keyId: rotatedAdoptedKey.id,
          fromMs: 1_700_000_000_000,
          toMs: 1_700_086_400_000,
          bucketMs: 3_600_000,
          coverage: {
            state: 'complete',
            reason: 'observed',
            collectingSinceMs: 1_700_000_000_000,
            lastProcessedEventId: 1,
            targetEventId: 1,
          },
          totals: {
            calls: 1,
            success: 1,
            failure: 0,
            inputTokens: 10,
            outputTokens: 5,
            totalTokens: 15,
            lastUsedAtMs: 1_700_050_000_000,
          },
          series: [],
        });
      }
      if (method === 'POST' && url.endsWith(`/api-keys/${rotatedAdoptedKey.id}/secret`)) {
        return Response.json({ secret: rotatedSecret });
      }
      throw new Error(`unexpected ${method} ${url}`);
    });
    const client = createPublicApiClient(() => context, fetchMock);
    await act(async () => {
      renderer = create(
        <FreshKeyDetails
          id={rotatedAdoptedKey.id}
          client={client}
          gatewayPending={false}
          credential="admin-memory"
          onUnauthorized={unauthorized}
          onPending={pending}
          onSaved={saved}
          onClose={close}
        />
      );
      await Promise.resolve();
      await Promise.resolve();
    });
    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'copy-saved-client-secret' }).props.onClick();
    });
    expect(writeText).toHaveBeenCalledWith(rotatedSecret);
  });

  it('preserves statusActive when renaming an effective_observed key returns effect_unknown without showing keysNameSaved banner', async () => {
    const effectiveKey = { ...key, runtimeState: 'effective_observed' as const };
    const renamedUnknown = {
      ...effectiveKey,
      name: 'Renamed Prod',
      nameRevision: 2,
      runtimeState: 'effect_unknown' as const,
      operationId: '',
    };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(effectiveKey))
      .mockResolvedValueOnce(Response.json(renamedUnknown));
    await mount(fetchMock, effectiveKey.id, undefined, false);
    expect(renderer.root.findByProps({ 'data-testid': 'detail-runtime-state' }).props.children).toBe(
      k('statusActive')
    );
    await edit();
    await draft('Renamed Prod');
    await submit();
    expect(view()).not.toContain(t('keysNameSaved'));
    expect(renderer.root.findByProps({ 'data-testid': 'detail-runtime-state' }).props.children).toBe(
      k('statusActive')
    );
    expect(saved).toHaveBeenLastCalledWith({
      ...renamedUnknown,
      runtimeState: 'effective_observed',
      operationId: undefined,
      effectCode: undefined,
    });
    expect(button(t('keysReadDetails')).props.className).toBe('btn btn-secondary btn-sm');
  });

  it('renders statusPending for effect_unknown without operationId and compact adopted note without basic section', async () => {
    const pendingAdopted = {
      ...key,
      origin: 'adopted' as const,
      maskedPreview: '…aaaa',
      runtimeState: 'effect_unknown' as const,
      operationId: '',
    };
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json(pendingAdopted));
    await mount(fetchMock, pendingAdopted.id, undefined, false);
    expect(renderer.root.findByProps({ 'data-testid': 'detail-runtime-state' }).props.children).toBe(
      k('statusPending')
    );
    expect(view()).toContain(k('adoptedOriginNote'));
    expect(view()).not.toContain(k('basic'));
    expect(view()).not.toContain(k('privateKeyHelp'));
  });
  it('renders identity in drawer header, preserves 24h KPI strip across usage window switches, and shows zero-calls series caption', async () => {
    const activeKey = { ...key, name: 'Gateway Edge', runtimeState: 'effective_observed' as const };
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.includes('/usage?from_ms=')) {
        return Promise.resolve(
          Response.json({
            keyId: activeKey.id,
            fromMs: 1_700_000_000_000,
            toMs: 1_700_604_800_000,
            bucketMs: 86_400_000,
            coverage: {
              state: 'complete',
              reason: 'observed',
              collectingSinceMs: 1_700_000_000_000,
              lastProcessedEventId: 10,
              targetEventId: 10,
            },
            totals: {
              calls: 0,
              success: 0,
              failure: 0,
              inputTokens: 0,
              outputTokens: 0,
              totalTokens: 0,
              lastUsedAtMs: null,
            },
            series: [{ bucketMs: 1_700_000_000_000, calls: 0, failure: 0, totalTokens: 0 }],
          })
        );
      }
      if (url.endsWith(`/api-keys/${activeKey.id}/usage`)) {
        return Promise.resolve(
          Response.json({
            keyId: activeKey.id,
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
              calls: 12,
              success: 12,
              failure: 0,
              inputTokens: 200,
              outputTokens: 100,
              totalTokens: 300,
              lastUsedAtMs: 1_700_050_000_000,
            },
            series: [{ bucketMs: 1_700_000_000_000, calls: 12, failure: 0, totalTokens: 300 }],
          })
        );
      }
      return Promise.resolve(Response.json(activeKey));
    });
    await act(async () => {
      renderer = create(
        <FreshKeyDetails
          id={activeKey.id}
          client={createPublicApiClient(() => context, fetchMock)}
          gatewayPending={false}
          credential="admin-memory"
          onUnauthorized={unauthorized}
          onPending={pending}
          onSaved={saved}
          onClose={close}
        />
      );
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(renderer.root.findAllByProps({ className: 'key-detail-hero' })).toHaveLength(0);
    expect(renderer.root.findAllByType('h3').map((n) => n.props.children)).toContain('Gateway Edge');
    expect(renderer.root.findByProps({ 'data-testid': 'detail-edit-name' })).toBeTruthy();
    expect(renderer.root.findByProps({ 'data-testid': 'detail-copy-secret' })).toBeTruthy();
    expect(renderer.root.findAllByProps({ 'data-testid': 'detail-operation-id' })).toHaveLength(0);

    const readText = (node: unknown): string => {
      if (typeof node === 'string' || typeof node === 'number') return String(node);
      if (!node || typeof node !== 'object' || !('children' in node)) return '';
      return Array.isArray(node.children) ? node.children.map(readText).join(' ') : '';
    };
    expect(readText(renderer.root.findByProps({ 'data-testid': 'key-overview-kpi-strip' }))).toContain('12');
    expect(readText(renderer.root.findByProps({ 'data-testid': 'key-overview-kpi-strip' }))).toContain('300');

    // Switch to Usage tab and switch window to 7d (returns 0 calls across a non-empty zero series).
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-detail-tab-usage' }).props.onClick();
    });
    await act(async () => {
      renderer.root.findByProps({ id: 'key-usage-window-7d' }).props.onClick();
    });
    expect(readText(renderer.root.findByProps({ 'data-testid': 'key-usage-series' }))).toContain(
      k('usageNoCalls')
    );
    // Overview 24h KPI strip keeps 24h counts (12 / 300) when switching back.
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-detail-tab-overview' }).props.onClick();
    });
    expect(readText(renderer.root.findByProps({ 'data-testid': 'key-overview-kpi-strip' }))).toContain('12');
    expect(readText(renderer.root.findByProps({ 'data-testid': 'key-overview-kpi-strip' }))).toContain('300');
    // Usage stat grid renders structured 7-metric tiles and centered empty-state message when empty.
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-detail-tab-usage' }).props.onClick();
    });
    const statTiles = renderer.root
      .findByProps({ 'data-testid': 'key-usage-totals' })
      .findAllByType('dt');
    expect(statTiles).toHaveLength(7);

    // Switch back to Overview and copy Key ID via detail-copy-key-id.
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-detail-tab-overview' }).props.onClick();
    });
    await act(async () => {
      await renderer.root.findByProps({ 'data-testid': 'detail-copy-key-id' }).props.onClick();
    });
    expect(writeText).toHaveBeenCalledWith(activeKey.id);
  });
  it('omits duplicate origin label in drawer subtitle when unnamed installer key uses origin as title', async () => {
    const unnamedInstaller = {
      ...key,
      name: '',
      origin: 'installer' as const,
      maskedPreview: 'sk-…1234',
      runtimeState: 'effective_observed' as const,
    };
    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.endsWith(`/api-keys/${unnamedInstaller.id}/usage`)) {
        return Promise.resolve(
          Response.json({
            keyId: unnamedInstaller.id,
            fromMs: 1,
            toMs: 2,
            bucketMs: 1,
            coverage: {
              state: 'complete',
              reason: 'observed',
              collectingSinceMs: 1_700_000_000_000,
              lastProcessedEventId: 10,
              targetEventId: 10,
            },
            totals: {
              calls: 0,
              success: 0,
              failure: 0,
              inputTokens: 0,
              outputTokens: 0,
              totalTokens: 0,
              lastUsedAtMs: null,
            },
            series: [],
          })
        );
      }
      return Promise.resolve(Response.json(unnamedInstaller));
    });
    await act(async () => {
      renderer = create(
        <FreshKeyDetails
          id={unnamedInstaller.id}
          client={createPublicApiClient(() => context, fetchMock)}
          gatewayPending={false}
          credential="admin-memory"
          onUnauthorized={unauthorized}
          onPending={pending}
          onSaved={saved}
          onClose={close}
        />
      );
      await Promise.resolve();
      await Promise.resolve();
    });
    const readMetaText = (node: unknown): string => {
      if (typeof node === 'string' || typeof node === 'number') return String(node);
      if (Array.isArray(node)) return node.map(readMetaText).join(' ');
      if (!node || typeof node !== 'object' || !('children' in node)) return '';
      return readMetaText(node.children);
    };
    const subtitleMeta = renderer.root.findByProps({
      className: workspaceDrawerStyles.drawerTitleMeta,
    });
    expect(readMetaText(subtitleMeta)).not.toContain(k('installerOrigin'));

    // Empty zero-call window renders centered key-usage-empty-state.
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-detail-tab-usage' }).props.onClick();
    });
    expect(renderer.root.findByProps({ 'data-testid': 'key-usage-empty' }).props.className).toBe(
      'key-usage-empty-state'
    );
  });
});
