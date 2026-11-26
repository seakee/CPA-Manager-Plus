import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FreshKeyDetails } from './FreshKeyDetails';
import { createPublicApiClient } from './publicApi';
import { setFullWebLanguage, fullWebText as t } from '../i18n/fullweb';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { workspaceDrawerStyles } from '../components/common/WorkspaceDrawer';
import { EChartsView } from '../components/charts/EChartsView';

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
    setFullWebLanguage('en');
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
    vi.stubGlobal('navigator', { clipboard: { writeText } });
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
      .findAllByProps({ 'data-metric-summary-header': 'true' });
    expect(statTiles).toHaveLength(4);

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

  it('renders EChartsView trend, token composition bar, and model consumption breakdown in usage tab', async () => {
    await mount(
      vi.fn<typeof fetch>(() => Promise.resolve(Response.json(key))),
      key.id,
      {
        keyId: key.id,
        fromMs: 1_000,
        toMs: 2_000,
        bucketMs: 1_000,
        coverage: {
          state: 'complete',
          reason: 'observed',
          collectingSinceMs: 500,
          lastProcessedEventId: 10,
          targetEventId: 10,
        },
        totals: {
          calls: 20,
          success: 19,
          failure: 1,
          inputTokens: 100_000,
          outputTokens: 20_000,
          cachedTokens: 40_000,
          totalTokens: 120_000,
          estimatedCostUsd: 1.8,
          lastUsedAtMs: 1_800,
        },
        topModels: [
          { model: 'gpt-5.3-codex', calls: 12, totalTokens: 70_000, estimatedCostUsd: 0.6 },
          { model: 'claude-opus-4-6', calls: 8, totalTokens: 50_000, estimatedCostUsd: 1.2 },
        ],
        modelCount: 2,
        series: [{ bucketMs: 1_000, calls: 20, failure: 1, totalTokens: 120_000 }],
      }
    );
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-detail-tab-usage' }).props.onClick();
      await Promise.resolve();
    });
    const readNodeText = (node: unknown): string => {
      if (typeof node === 'string' || typeof node === 'number') return String(node);
      if (Array.isArray(node)) return node.map(readNodeText).join('');
      if (!node || typeof node !== 'object' || !('children' in node)) return '';
      return readNodeText(node.children);
    };
    const tokenMixNode = renderer.root.findByProps({ 'data-testid': 'key-usage-token-mix' });
    const tokenMixText = readNodeText(tokenMixNode);
    expect(tokenMixText).toContain('40.0%');
    expect(tokenMixText).toContain('60K');
    expect(tokenMixText).toContain('40K');
    expect(tokenMixText).toContain('20K');

    const modelsNode = renderer.root.findByProps({ 'data-testid': 'key-usage-models' });
    const modelsText = readNodeText(modelsNode);
    expect(modelsText.indexOf('claude-opus-4-6')).toBeLessThan(modelsText.indexOf('gpt-5.3-codex'));
    expect(modelsText).toContain('$1.20');
    expect(modelsText).toContain('$0.60');
  });

  it('renders unified 3-card overview with 24h cost KPI, active models, and masked protocol auth header copy', async () => {
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const fullSecret = 'cpamp_sk_' + 'c'.repeat(64);
    const effectiveKey = {
      ...key,
      name: 'Prod Gateway',
      runtimeState: 'effective_observed' as const,
    };
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(effectiveKey))
      .mockResolvedValueOnce(Response.json({ secret: fullSecret }));

    await mount(
      fetchMock,
      effectiveKey.id,
      {
        keyId: effectiveKey.id,
        fromMs: 1_000,
        toMs: 2_000,
        bucketMs: 1_000,
        coverage: {
          state: 'complete',
          reason: 'observed',
          collectingSinceMs: 500,
          lastProcessedEventId: 10,
          targetEventId: 10,
        },
        totals: {
          calls: 52,
          success: 48,
          failure: 4,
          inputTokens: 687_900,
          outputTokens: 124_700,
          cachedTokens: 292_400,
          totalTokens: 812_600,
          estimatedCostUsd: 2.52,
          lastUsedAtMs: 1_700_000_000_000,
        },
        topModels: [
          { model: 'claude-sonnet-4-5', calls: 16, totalTokens: 302_100, estimatedCostUsd: 1.0 },
          { model: 'gemini-2.5-pro', calls: 12, totalTokens: 225_900, estimatedCostUsd: 0.44 },
          { model: 'gpt-5.3-codex', calls: 13, totalTokens: 192_800, estimatedCostUsd: 0.4 },
        ],
        modelCount: 5,
        series: [{ bucketMs: 1_000, calls: 52, failure: 4, totalTokens: 812_600 }],
      },
      false,
      {
        mode: 'connect_external',
        managementBasePath: '/cpamp',
        inferenceBasePath: '',
        externalBaseUrl: 'http://127.0.0.1:18317',
        protocols: [
          {
            id: 'openai',
            label: 'OpenAI',
            basePath: 'http://127.0.0.1:18317/v1',
            examplePath: 'http://127.0.0.1:18317/v1/chat/completions',
          },
          {
            id: 'anthropic',
            label: 'Anthropic',
            basePath: 'http://127.0.0.1:18317',
            examplePath: 'http://127.0.0.1:18317/v1/messages',
          },
        ],
        ready: true,
      }
    );

    const readNodeText = (node: unknown): string => {
      if (typeof node === 'string' || typeof node === 'number') return String(node);
      if (Array.isArray(node)) return node.map(readNodeText).join('');
      if (!node || typeof node !== 'object' || !('children' in node)) return '';
      return readNodeText(node.children);
    };

    // 1. 24h KPI strip includes calls, success rate, tokens, and estimated cost ($2.52).
    const kpiText = readNodeText(
      renderer.root.findByProps({ 'data-testid': 'key-overview-kpi-strip' })
    );
    expect(kpiText).toContain('52');
    expect(kpiText).toContain('92.3%');
    expect(kpiText).toContain('812.6K');
    expect(kpiText).toContain('$2.52');

    // 2. Key Information deduplicates header fields and surfaces active models + +2 badge.
    const metaNode = renderer.root.findByProps({ 'data-testid': 'key-detail-metadata' });
    const metaText = readNodeText(metaNode);
    expect(metaText).not.toContain(k('created'));
    expect(metaText).not.toContain(k('originLabel'));
    expect(metaText).toContain('claude-sonnet-4-5');
    expect(metaText).toContain('+2');

    // 3. Streamlined Client Access shows masked header in DOM, switches protocols, and copies verified header without rendering raw secret.
    expect(
      renderer.root.findByProps({ 'data-testid': 'detail-access-info-protocol-header-openai' })
        .props.children
    ).toBe('Authorization: Bearer cpamp_sk_…cccc');
    expect(
      readNodeText(
        renderer.root.findByProps({ 'data-testid': 'detail-access-info-external-note' })
      )
    ).toContain('http://127.0.0.1:18317');

    await act(async () => {
      renderer.root
        .findByProps({ 'data-testid': 'detail-access-info-protocol-tab-anthropic' })
        .props.onClick();
    });
    expect(
      renderer.root.findByProps({ 'data-testid': 'detail-access-info-protocol-header-anthropic' })
        .props.children
    ).toBe('x-api-key: cpamp_sk_…cccc');

    await act(async () => {
      renderer.root
        .findByProps({ 'data-testid': 'detail-access-info-copy-header-anthropic' })
        .props.onClick();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(writeText).toHaveBeenCalledWith(`x-api-key: ${fullSecret}`);
    expect(view()).not.toContain(fullSecret);
  });

  it('keeps initialWindow=7d active usage separate from background 24h overview, refreshes both, and protects 7d query to_ms against clock skew', async () => {
    const activeKey = { ...key, runtimeState: 'effective_observed' as const };
    let resolve7d!: (res: Response) => void;
    let resolve24h!: (res: Response) => void;
    const usage7dUrls: string[] = [];
    let fetchCount7d = 0;
    let fetchCount24h = 0;

    const server24hToMs = Date.now() + 120_000;
    const payload24h = {
      keyId: activeKey.id,
      fromMs: server24hToMs - 86_400_000,
      toMs: server24hToMs,
      bucketMs: 3_600_000,
      coverage: {
        state: 'complete',
        reason: 'observed',
        collectingSinceMs: 1_000,
        lastProcessedEventId: 10,
        targetEventId: 10,
      },
      totals: {
        calls: 11,
        success: 11,
        failure: 0,
        inputTokens: 1_100,
        outputTokens: 200,
        totalTokens: 1_300,
        estimatedCostUsd: 0.42,
        lastUsedAtMs: server24hToMs - 5_000,
      },
      series: [{ bucketMs: server24hToMs - 3_600_000, calls: 11, failure: 0, totalTokens: 1_300 }],
    };
    const payload7d = {
      keyId: activeKey.id,
      fromMs: 1_700_000_000_000,
      toMs: 1_700_604_800_000,
      bucketMs: 21_600_000,
      coverage: {
        state: 'complete',
        reason: 'observed',
        collectingSinceMs: 1_000,
        lastProcessedEventId: 90,
        targetEventId: 90,
      },
      totals: {
        calls: 77,
        success: 70,
        failure: 7,
        inputTokens: 70_000,
        outputTokens: 7_000,
        totalTokens: 77_000,
        estimatedCostUsd: 7.77,
        lastUsedAtMs: 1_700_600_000_000,
      },
      series: [{ bucketMs: 1_700_000_000_000, calls: 77, failure: 7, totalTokens: 77_000 }],
    };

    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.includes('/usage?from_ms=')) {
        usage7dUrls.push(url);
        fetchCount7d += 1;
        if (fetchCount7d === 1) {
          return new Promise<Response>((resolve) => {
            resolve7d = resolve;
          });
        }
        return Promise.resolve(
          Response.json({
            ...payload7d,
            totals: { ...payload7d.totals, calls: 88, success: 81, failure: 7 },
          })
        );
      }
      if (url.endsWith(`/api-keys/${activeKey.id}/usage`)) {
        fetchCount24h += 1;
        if (fetchCount24h === 1) {
          return new Promise<Response>((resolve) => {
            resolve24h = resolve;
          });
        }
        return Promise.resolve(
          Response.json({
            ...payload24h,
            totals: { ...payload24h.totals, calls: 22, success: 22, failure: 0 },
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
      return Promise.resolve(Response.json(activeKey));
    });

    await act(async () => {
      renderer = create(
        <FreshKeyDetails
          id={activeKey.id}
          client={createPublicApiClient(() => context, fetchMock)}
          gatewayPending={false}
          credential="admin-memory"
          initialTab="usage"
          initialWindow="7d"
          onUnauthorized={unauthorized}
          onPending={pending}
          onSaved={saved}
          onClose={close}
        />
      );
    });

    // Resolve 7d first, then 24h second: 24h must NOT overwrite active 7d Usage tab!
    await act(async () => {
      resolve7d(Response.json(payload7d));
      await Promise.resolve();
    });
    await act(async () => {
      resolve24h(Response.json(payload24h));
      await Promise.resolve();
    });

    const readNodeText = (node: unknown): string => {
      if (typeof node === 'string' || typeof node === 'number') return String(node);
      if (Array.isArray(node)) return node.map(readNodeText).join('');
      if (!node || typeof node !== 'object' || !('children' in node)) return '';
      return readNodeText(node.children);
    };

    const usageTotalsText = readNodeText(
      renderer.root.findByProps({ 'data-testid': 'key-usage-totals' })
    );
    expect(usageTotalsText).toContain('77');
    expect(usageTotalsText).toContain('$7.77');
    expect(usageTotalsText).not.toContain('$0.42');

    const overviewStripText = readNodeText(
      renderer.root.findByProps({ 'data-testid': 'key-overview-kpi-strip' })
    );
    expect(overviewStripText).toContain('11');
    expect(overviewStripText).toContain('$0.42');
    expect(overviewStripText).not.toContain('$7.77');

    // Manual refresh updates both active 7d window and 24h overview, using server toMs floor.
    await act(async () => {
      button(t('keysReadDetails')).props.onClick();
      for (let i = 0; i < 6; i += 1) await Promise.resolve();
    });
    expect(fetchCount7d).toBe(2);
    expect(fetchCount24h).toBe(2);
    const refreshedToMs = Number(new URL(usage7dUrls[1]!).searchParams.get('to_ms'));
    expect(refreshedToMs).toBeGreaterThanOrEqual(server24hToMs);
    expect(
      readNodeText(renderer.root.findByProps({ 'data-testid': 'key-usage-totals' }))
    ).toContain('88');
    expect(
      readNodeText(renderer.root.findByProps({ 'data-testid': 'key-overview-kpi-strip' }))
    ).toContain('22');
  });

  it('handles copySavedAuthHeader success, 401, 409 refresh, concurrent busy call, and rejection without template fallback', async () => {
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    const fullSecret = 'cpamp_sk_' + 'c'.repeat(64);
    const effectiveKey = {
      ...key,
      name: 'Header Copy Key',
      runtimeState: 'effective_observed' as const,
    };
    const accessInfo = {
      mode: 'new_managed' as const,
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
    };

    // 1. Success + concurrent busy call ('ignored', no red copyFailed)
    let resolveSecret!: (res: Response) => void;
    let secretCalls = 0;
    const concurrentFetch = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      if (url.endsWith(`/api-keys/${effectiveKey.id}/secret`) && init?.method === 'POST') {
        secretCalls += 1;
        return new Promise<Response>((resolve) => {
          resolveSecret = resolve;
        });
      }
      return Promise.resolve(Response.json(effectiveKey));
    });
    await mount(concurrentFetch, effectiveKey.id, undefined, false, accessInfo);
    await act(async () => {
      const pSecret = renderer.root
        .findByProps({ 'data-testid': 'copy-saved-client-secret' })
        .props.onClick();
      const pHeader = renderer.root
        .findByProps({ 'data-testid': 'detail-access-info-copy-header-openai' })
        .props.onClick();
      await Promise.resolve();
      await pHeader;
      void pSecret;
    });
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'detail-access-info-copy-header-openai' })
        .props.onClick();
    });
    expect(secretCalls).toBe(1);
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'detail-access-info-copy-feedback' })
    ).toHaveLength(0);
    await act(async () => {
      resolveSecret(Response.json({ secret: fullSecret }));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(writeText).toHaveBeenCalledWith(fullSecret);

    // Normal header copy succeeds
    writeText.mockClear();
    await act(async () => {
      const p = renderer.root
        .findByProps({ 'data-testid': 'detail-access-info-copy-header-openai' })
        .props.onClick();
      for (let i = 0; i < 4; i += 1) await Promise.resolve();
      resolveSecret(Response.json({ secret: fullSecret }));
      await p;
    });
    expect(writeText).toHaveBeenCalledWith(`Authorization: Bearer ${fullSecret}`);
    expect(view()).not.toContain(fullSecret);

    // 2. 401 -> calls onUnauthorized, no red copyFailed
    await act(async () => renderer.unmount());
    unauthorized.mockClear();
    writeText.mockClear();
    const fetch401 = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(effectiveKey))
      .mockResolvedValueOnce(new Response('', { status: 401 }));
    await mount(fetch401, effectiveKey.id, undefined, false, accessInfo);
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'detail-access-info-copy-header-openai' })
        .props.onClick();
    });
    expect(unauthorized).toHaveBeenCalledOnce();
    expect(
      renderer.root.findAllByProps({ 'data-testid': 'detail-access-info-copy-feedback' })
    ).toHaveLength(0);

    // 3. 409 -> refreshes retired key, never writes template or secret
    await act(async () => renderer.unmount());
    const retired = {
      ...effectiveKey,
      lifecycle: 'superseded' as const,
      runtimeState: 'revoked_observed' as const,
    };
    const fetch409 = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(effectiveKey))
      .mockResolvedValueOnce(new Response('', { status: 409 }))
      .mockResolvedValueOnce(Response.json(retired));
    await mount(fetch409, effectiveKey.id, undefined, false, accessInfo);
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'detail-access-info-copy-header-openai' })
        .props.onClick();
    });
    expect(writeText).not.toHaveBeenCalled();
    expect(saved).toHaveBeenLastCalledWith(retired);
    expect(renderer.root.findByProps({ 'data-testid': 'detail-runtime-state' }).props.children).toBe(
      k('statusRevoked')
    );

    // 4. Rejection -> shows copyFailed and never copies fallback template
    await act(async () => renderer.unmount());
    const fetchErr = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json(effectiveKey))
      .mockRejectedValueOnce(new Error('secret read failed'));
    await mount(fetchErr, effectiveKey.id, undefined, false, accessInfo);
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'detail-access-info-copy-header-openai' })
        .props.onClick();
    });
    expect(writeText).not.toHaveBeenCalled();
    expect(
      renderer.root.findByProps({ 'data-testid': 'detail-access-info-copy-feedback' }).props.children
    ).toContain('Clipboard');
  });

  it('handles token mix reasoningTokens, clamped cache hit, overflow guard, partial avg KPIs, model sort, and >32 bucket step-up', async () => {
    const readNodeText = (node: unknown): string => {
      if (typeof node === 'string' || typeof node === 'number') return String(node);
      if (Array.isArray(node)) return node.map(readNodeText).join('');
      if (!node || typeof node !== 'object' || !('children' in node)) return '';
      return readNodeText(node.children);
    };

    // Case A: omitted reasoningTokens must NOT fabricate Reasoning even when totalTokens > input + output;
    // cachedTokens > inputTokens clamps cache hit rate <= 100%; partial coverage omits >= on avg ratios;
    // model breakdown sorts strictly by calls descending when hasAnyCost is false;
    // >32 base buckets step up cleanly instead of collapsing earlier buckets into bar 0.
    const startMs = Math.floor(1_700_000_000_000 / 43_200_000) * 43_200_000 + 3 * 3_600_000;
    const eightDaysEndMs = startMs + 8 * 86_400_000 - 1_000; // <= 8d so base step is 6h, spanning 33 6h-buckets (> 32)
    await mount(vi.fn<typeof fetch>().mockResolvedValue(Response.json(key)), key.id, {
      keyId: key.id,
      fromMs: startMs,
      toMs: eightDaysEndMs,
      bucketMs: 21_600_000,
      coverage: {
        state: 'partial',
        reason: 'window_partial',
        collectingSinceMs: startMs + 3_600_000,
        lastProcessedEventId: 10,
        targetEventId: 10,
      },
      totals: {
        calls: 10,
        success: 10,
        failure: 0,
        inputTokens: 100,
        outputTokens: 50,
        cachedTokens: 150,
        totalTokens: 500,
        estimatedCostUsd: 2.0,
        lastUsedAtMs: eightDaysEndMs - 1_000,
      },
      topModels: [
        { model: 'zero-priced-model', calls: 2, totalTokens: 50, estimatedCostUsd: 0 },
        { model: 'heavy-unpriced-model', calls: 1_000, totalTokens: 450 },
      ],
      modelCount: 2,
      series: [
        { bucketMs: startMs, calls: 1, failure: 0, totalTokens: 50 },
        { bucketMs: startMs + 6 * 3_600_000, calls: 2, failure: 0, totalTokens: 100 },
        { bucketMs: startMs + 12 * 3_600_000, calls: 4, failure: 0, totalTokens: 200 },
        { bucketMs: eightDaysEndMs - 3_600_000, calls: 3, failure: 0, totalTokens: 150 },
      ],
    });

    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-detail-tab-usage' }).props.onClick();
    });

    // Partial totals have >=, but avg ratios do NOT have >=
    const kpiMetas = renderer.root
      .findByProps({ 'data-testid': 'key-usage-totals' })
      .findAllByProps({ 'data-metric-summary-meta': 'true' })
      .map((node) => readNodeText(node));
    expect(kpiMetas.some((m) => m.includes('50 /') && !m.includes('≥50'))).toBe(true);
    expect(kpiMetas.some((m) => m.includes('$0.20 /') && !m.includes('≥$0.20'))).toBe(true);

    // Token mix: no Reasoning fabricated, cache hit clamped <= 100%, bar widths <= 100%
    const tokenMixA = renderer.root.findByProps({ 'data-testid': 'key-usage-token-mix' });
    const tokenMixTextA = readNodeText(tokenMixA);
    expect(tokenMixTextA).toContain('100.0%');
    expect(tokenMixTextA).not.toContain('150.0%');
    expect(tokenMixTextA).not.toContain(k('usageReasoningTokens'));
    const widthsA = tokenMixA
      .findAll((n) => typeof n.props.className === 'string' && n.props.className.includes('key-token-mix-seg'))
      .map((n) => parseFloat(String(n.props.style.width)));
    expect(widthsA.reduce((sum, w) => sum + w, 0)).toBeLessThanOrEqual(100.05);

    // Model breakdown: heavy-unpriced-model (1000 calls) sorts ahead of zero-priced-model ($0, 2 calls)
    const modelsText = readNodeText(renderer.root.findByProps({ 'data-testid': 'key-usage-models' }));
    expect(modelsText.indexOf('heavy-unpriced-model')).toBeLessThan(
      modelsText.indexOf('zero-priced-model')
    );

    // >32 bucket step-up: bar 0 only holds buckets within its own 12h slot (3 calls, not collapsing slot 12h=4 calls into bar 0)
    const readChartSummary = () => {
      const opt = renderer.root.findByType(EChartsView).props.option;
      if (!opt || typeof opt !== 'object') {
        throw new Error('Expected chart option object');
      }
      const xAxis = 'xAxis' in opt && opt.xAxis && typeof opt.xAxis === 'object' ? opt.xAxis : null;
      const xData = xAxis && 'data' in xAxis && Array.isArray(xAxis.data) ? xAxis.data : [];
      const seriesList = 'series' in opt && Array.isArray(opt.series) ? opt.series : [];
      const firstSeries =
        seriesList[0] && typeof seriesList[0] === 'object' ? seriesList[0] : null;
      const seriesName =
        firstSeries && 'name' in firstSeries && typeof firstSeries.name === 'string'
          ? firstSeries.name
          : '';
      const seriesData =
        firstSeries && 'data' in firstSeries && Array.isArray(firstSeries.data)
          ? firstSeries.data
          : [];
      const readDatumValue = (datum: unknown) =>
        typeof datum === 'number'
          ? datum
          : datum &&
              typeof datum === 'object' &&
              'value' in datum &&
              typeof datum.value === 'number'
            ? datum.value
            : NaN;
      return {
        slotCount: xData.length,
        seriesName,
        firstValue: readDatumValue(seriesData[0]),
        secondValue: readDatumValue(seriesData[1]),
        lastValue: readDatumValue(seriesData[seriesData.length - 1]),
      };
    };
    const summaryBeforeLang = readChartSummary();
    expect(summaryBeforeLang.slotCount).toBe(17);
    expect(summaryBeforeLang.firstValue).toBe(3);
    expect(summaryBeforeLang.secondValue).toBe(4);
    expect(summaryBeforeLang.lastValue).toBe(3);

    // Language change updates chart option labels via memo deps
    await act(async () => {
      setFullWebLanguage('zh-CN');
    });
    expect(readChartSummary().seriesName).not.toBe(summaryBeforeLang.seriesName);
    await act(async () => {
      setFullWebLanguage('en');
    });

    // Case B: explicit reasoningTokens renders Reasoning segment; totalTokens < input + output never overflows 100%
    await act(async () => renderer.unmount());
    await mount(vi.fn<typeof fetch>().mockResolvedValue(Response.json(key)), key.id, {
      keyId: key.id,
      fromMs: 1_000,
      toMs: 2_000,
      bucketMs: 1_000,
      coverage: {
        state: 'complete',
        reason: 'observed',
        collectingSinceMs: 500,
        lastProcessedEventId: 10,
        targetEventId: 10,
      },
      totals: {
        calls: 5,
        success: 5,
        failure: 0,
        inputTokens: 80,
        outputTokens: 50,
        reasoningTokens: 30,
        totalTokens: 100,
        lastUsedAtMs: 1_800,
      },
      series: [],
    });
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'key-detail-tab-usage' }).props.onClick();
    });
    const tokenMixB = renderer.root.findByProps({ 'data-testid': 'key-usage-token-mix' });
    expect(readNodeText(tokenMixB)).toContain(k('usageReasoningTokens'));
    const widthsB = tokenMixB
      .findAll((n) => typeof n.props.className === 'string' && n.props.className.includes('key-token-mix-seg'))
      .map((n) => parseFloat(String(n.props.style.width)));
    expect(widthsB.reduce((sum, w) => sum + w, 0)).toBeLessThanOrEqual(100.05);
  });

  it('clears stale metrics on window switch while keeping 24h overview populated when switching early from 24h to 7d', async () => {
    const activeKey = { ...key, runtimeState: 'effective_observed' as const };
    let resolve24h!: (res: Response) => void;
    let resolve7d!: (res: Response) => void;
    let resolve30d!: (res: Response) => void;
    const usageQueryUrls: string[] = [];

    const server24hToMs = Date.now() + 180_000;
    const payload24h = {
      keyId: activeKey.id,
      fromMs: server24hToMs - 86_400_000,
      toMs: server24hToMs,
      bucketMs: 3_600_000,
      coverage: {
        state: 'complete',
        reason: 'observed',
        collectingSinceMs: 1_000,
        lastProcessedEventId: 10,
        targetEventId: 10,
      },
      totals: {
        calls: 11,
        success: 11,
        failure: 0,
        inputTokens: 1_100,
        outputTokens: 200,
        totalTokens: 1_300,
        estimatedCostUsd: 0.42,
        lastUsedAtMs: server24hToMs - 5_000,
      },
      series: [{ bucketMs: server24hToMs - 3_600_000, calls: 11, failure: 0, totalTokens: 1_300 }],
    };
    const payload7d = {
      keyId: activeKey.id,
      fromMs: server24hToMs - 7 * 86_400_000,
      toMs: server24hToMs,
      bucketMs: 21_600_000,
      coverage: {
        state: 'complete',
        reason: 'observed',
        collectingSinceMs: 1_000,
        lastProcessedEventId: 90,
        targetEventId: 90,
      },
      totals: {
        calls: 77,
        success: 70,
        failure: 7,
        inputTokens: 70_000,
        outputTokens: 7_000,
        totalTokens: 77_000,
        estimatedCostUsd: 7.77,
        lastUsedAtMs: server24hToMs - 10_000,
      },
      series: [{ bucketMs: server24hToMs - 21_600_000, calls: 77, failure: 7, totalTokens: 77_000 }],
    };
    const payload30d = {
      keyId: activeKey.id,
      fromMs: server24hToMs - 30 * 86_400_000,
      toMs: server24hToMs,
      bucketMs: 86_400_000,
      coverage: {
        state: 'complete',
        reason: 'observed',
        collectingSinceMs: 1_000,
        lastProcessedEventId: 120,
        targetEventId: 120,
      },
      totals: {
        calls: 120,
        success: 118,
        failure: 2,
        inputTokens: 110_000,
        outputTokens: 10_000,
        totalTokens: 120_000,
        estimatedCostUsd: 12.34,
        lastUsedAtMs: server24hToMs - 10_000,
      },
      series: [{ bucketMs: server24hToMs - 86_400_000, calls: 120, failure: 2, totalTokens: 120_000 }],
    };

    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.includes('/usage?from_ms=')) {
        usageQueryUrls.push(url);
        if (usageQueryUrls.length === 1) {
          return new Promise<Response>((resolve) => {
            resolve7d = resolve;
          });
        }
        return new Promise<Response>((resolve) => {
          resolve30d = resolve;
        });
      }
      if (url.endsWith(`/api-keys/${activeKey.id}/usage`)) {
        return new Promise<Response>((resolve) => {
          resolve24h = resolve;
        });
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
      return Promise.resolve(Response.json(activeKey));
    });

    const readNodeText = (node: unknown): string => {
      if (typeof node === 'string' || typeof node === 'number') return String(node);
      if (Array.isArray(node)) return node.map(readNodeText).join('');
      if (!node || typeof node !== 'object' || !('children' in node)) return '';
      return readNodeText(node.children);
    };

    await act(async () => {
      renderer = create(
        <FreshKeyDetails
          id={activeKey.id}
          client={createPublicApiClient(() => context, fetchMock)}
          gatewayPending={false}
          credential="admin-memory"
          initialTab="usage"
          initialWindow="24h"
          onUnauthorized={unauthorized}
          onPending={pending}
          onSaved={saved}
          onClose={close}
        />
      );
    });

    // Switch 24h -> 7d before initial 24h settles.
    await act(async () => {
      renderer.root.findByProps({ id: 'key-usage-window-7d' }).props.onClick();
    });
    expect(
      renderer.root.findByProps({ id: 'key-usage-window-7d' }).props['aria-selected']
    ).toBe(true);

    // Resolve 7d first -> Usage tab renders 7d metrics.
    await act(async () => {
      resolve7d(Response.json(payload7d));
      await Promise.resolve();
    });
    const usageTotals7d = readNodeText(
      renderer.root.findByProps({ 'data-testid': 'key-usage-totals' })
    );
    expect(usageTotals7d).toContain('77');
    expect(usageTotals7d).toContain('$7.77');

    // Resolve earlier 24h second -> Usage tab stays on 7d AND Overview KPIs populate from 24h!
    await act(async () => {
      resolve24h(Response.json(payload24h));
      await Promise.resolve();
    });
    const usageTotalsAfter24h = readNodeText(
      renderer.root.findByProps({ 'data-testid': 'key-usage-totals' })
    );
    expect(usageTotalsAfter24h).toContain('77');
    expect(usageTotalsAfter24h).toContain('$7.77');
    expect(usageTotalsAfter24h).not.toContain('$0.42');
    const overviewText = readNodeText(
      renderer.root.findByProps({ 'data-testid': 'key-overview-kpi-strip' })
    );
    expect(overviewText).toContain('11');
    expect(overviewText).toContain('$0.42');
    expect(overviewText).not.toContain('$7.77');

    // Switch 7d -> 30d: stale 7d metrics clear immediately while 30d request is pending,
    // and 30d to_ms still honors latestServerToMsRef from the 24h response.
    await act(async () => {
      renderer.root.findByProps({ id: 'key-usage-window-30d' }).props.onClick();
    });
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-usage-totals' })).toHaveLength(0);
    const usagePanelDuring30d = readNodeText(
      renderer.root.findByProps({ 'data-testid': 'key-detail-panel-usage' })
    );
    expect(usagePanelDuring30d).toContain(k('usageReading'));
    expect(usagePanelDuring30d).not.toContain('$7.77');
    const thirtyDayToMs = Number(new URL(usageQueryUrls[1]!).searchParams.get('to_ms'));
    expect(thirtyDayToMs).toBeGreaterThanOrEqual(server24hToMs);

    await act(async () => {
      resolve30d(Response.json(payload30d));
      await Promise.resolve();
    });
    const usageTotals30d = readNodeText(
      renderer.root.findByProps({ 'data-testid': 'key-usage-totals' })
    );
    expect(usageTotals30d).toContain('120');
    expect(usageTotals30d).toContain('$12.34');
  });

  it('syncs initialWindow updates while open on the same key id without double-fetching on mount', async () => {
    const activeKey = { ...key, runtimeState: 'effective_observed' as const };
    const now = Date.now();
    let fetch24hCount = 0;
    let fetchRangeCount = 0;
    let resolve7d!: (res: Response) => void;

    const fetchMock = vi.fn<typeof fetch>((input) => {
      const url = String(input);
      if (url.includes('/usage?from_ms=')) {
        fetchRangeCount += 1;
        return new Promise<Response>((resolve) => {
          resolve7d = resolve;
        });
      }
      if (url.endsWith(`/api-keys/${activeKey.id}/usage`)) {
        fetch24hCount += 1;
        return Promise.resolve(
          Response.json({
            keyId: activeKey.id,
            fromMs: now - 86_400_000,
            toMs: now,
            bucketMs: 3_600_000,
            coverage: {
              state: 'complete',
              reason: 'observed',
              collectingSinceMs: 1_000,
              lastProcessedEventId: 10,
              targetEventId: 10,
            },
            totals: {
              calls: 15,
              success: 15,
              failure: 0,
              inputTokens: 1_500,
              outputTokens: 500,
              totalTokens: 2_000,
              estimatedCostUsd: 0.65,
              lastUsedAtMs: now - 5_000,
            },
            series: [],
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
      return Promise.resolve(Response.json(activeKey));
    });

    const client = createPublicApiClient(() => context, fetchMock);
    const readNodeText = (node: unknown): string => {
      if (typeof node === 'string' || typeof node === 'number') return String(node);
      if (Array.isArray(node)) return node.map(readNodeText).join('');
      if (!node || typeof node !== 'object' || !('children' in node)) return '';
      return readNodeText(node.children);
    };

    await act(async () => {
      renderer = create(
        <FreshKeyDetails
          id={activeKey.id}
          client={client}
          gatewayPending={false}
          credential="admin-memory"
          open
          initialTab="usage"
          initialWindow="24h"
          onUnauthorized={unauthorized}
          onPending={pending}
          onSaved={saved}
          onClose={close}
        />
      );
    });
    expect(fetch24hCount).toBe(1);
    expect(fetchRangeCount).toBe(0);
    expect(readNodeText(renderer.root.findByProps({ 'data-testid': 'key-usage-totals' }))).toContain(
      '$0.65'
    );

    // Update initialWindow on same open key -> switches tab, clears stale metrics, fetches 7d once.
    await act(async () => {
      renderer.update(
        <FreshKeyDetails
          id={activeKey.id}
          client={client}
          gatewayPending={false}
          credential="admin-memory"
          open
          initialTab="usage"
          initialWindow="7d"
          onUnauthorized={unauthorized}
          onPending={pending}
          onSaved={saved}
          onClose={close}
        />
      );
    });
    expect(fetch24hCount).toBe(1);
    expect(fetchRangeCount).toBe(1);
    expect(
      renderer.root.findByProps({ id: 'key-usage-window-7d' }).props['aria-selected']
    ).toBe(true);
    expect(renderer.root.findAllByProps({ 'data-testid': 'key-usage-totals' })).toHaveLength(0);
    const usagePanelDuring7d = readNodeText(
      renderer.root.findByProps({ 'data-testid': 'key-detail-panel-usage' })
    );
    expect(usagePanelDuring7d).toContain(k('usageReading'));
    expect(usagePanelDuring7d).not.toContain('$0.65');

    await act(async () => {
      resolve7d(
        Response.json({
          keyId: activeKey.id,
          fromMs: now - 7 * 86_400_000,
          toMs: now,
          bucketMs: 21_600_000,
          coverage: {
            state: 'complete',
            reason: 'observed',
            collectingSinceMs: 1_000,
            lastProcessedEventId: 50,
            targetEventId: 50,
          },
          totals: {
            calls: 50,
            success: 50,
            failure: 0,
            inputTokens: 5_000,
            outputTokens: 1_000,
            totalTokens: 6_000,
            estimatedCostUsd: 3.21,
            lastUsedAtMs: now - 2_000,
          },
          series: [],
        })
      );
      await Promise.resolve();
    });
    expect(readNodeText(renderer.root.findByProps({ 'data-testid': 'key-usage-totals' }))).toContain(
      '$3.21'
    );
  });

  it('disables detail-access-info-copy-header-* when busy, copying, applyPending, or applyUnknown is active', async () => {
    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } });
    const notAppliedKey = {
      ...key,
      name: 'Retryable Key',
      runtimeState: 'not_applied' as const,
      effectCode: 'apply_failed',
    };
    const accessInfo = {
      mode: 'new_managed' as const,
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
    };
    let getCount = 0;
    let resolveRefresh!: (res: Response) => void;
    let resolveSecret!: (res: Response) => void;
    let resolveRetry!: (res: Response) => void;
    let rejectRetry!: (err: Error) => void;
    let retryCount = 0;

    const fetchMock = vi.fn<typeof fetch>((input, init) => {
      const url = String(input);
      const method = (init?.method ?? 'GET').toUpperCase();
      if (method === 'GET' && url.endsWith(`/api-keys/${notAppliedKey.id}`)) {
        getCount += 1;
        if (getCount === 1) return Promise.resolve(Response.json(notAppliedKey));
        return new Promise<Response>((resolve) => {
          resolveRefresh = resolve;
        });
      }
      if (method === 'POST' && url.endsWith(`/api-keys/${notAppliedKey.id}/secret`)) {
        return new Promise<Response>((resolve) => {
          resolveSecret = resolve;
        });
      }
      if (method === 'POST' && url.endsWith('/api-keys')) {
        retryCount += 1;
        return new Promise<Response>((resolve, reject) => {
          resolveRetry = resolve;
          rejectRetry = reject;
        });
      }
      throw new Error(`unexpected ${method} ${url}`);
    });

    await mount(fetchMock, notAppliedKey.id, undefined, false, accessInfo);
    const headerCopyBtn = () =>
      renderer.root.findByProps({ 'data-testid': 'detail-access-info-copy-header-openai' });
    expect(headerCopyBtn().props.disabled).toBe(false);

    // 1. copying === true disables auth header copy button
    await act(async () => {
      void renderer.root
        .findByProps({ 'data-testid': 'copy-saved-client-secret' })
        .props.onClick();
      await Promise.resolve();
    });
    expect(headerCopyBtn().props.disabled).toBe(true);
    await act(async () => {
      resolveSecret(Response.json({ secret: 'cpamp_sk_' + 'c'.repeat(64) }));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(headerCopyBtn().props.disabled).toBe(false);

    // 2. busy === true disables auth header copy button
    await act(async () => {
      button(t('keysReadDetails')).props.onClick();
    });
    expect(headerCopyBtn().props.disabled).toBe(true);
    await act(async () => {
      resolveRefresh(Response.json(notAppliedKey));
      await Promise.resolve();
    });
    expect(headerCopyBtn().props.disabled).toBe(false);

    // 3. applyPending === true and applyUnknown === true disable auth header copy button
    await act(async () => {
      renderer.root.findByProps({ 'data-testid': 'detail-retry-apply' }).props.onClick();
    });
    expect(retryCount).toBe(1);
    expect(headerCopyBtn().props.disabled).toBe(true);
    await act(async () => {
      rejectRetry(new Error('network_uncertain'));
      await Promise.resolve();
    });
    expect(headerCopyBtn().props.disabled).toBe(true);
    void resolveRetry;
  });
});
