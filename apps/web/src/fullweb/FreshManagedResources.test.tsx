import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode } from 'react';
import { MemoryRouter, useNavigate } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FreshManagedResources } from './FreshManagedResources';
import { createPublicApiClient } from './publicApi';
import { fullWebText as t } from '../i18n/fullweb';
import i18n from '../i18n';
import { configurationText as c } from '../i18n/configurationWorkspace';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { gatewayViewFixture } from './gatewayViewFixture';

/** The text a node renders to, the way the operator reads it. */
function textOf(node: { children?: readonly unknown[] } | string): string {
  if (typeof node === 'string') return node;
  return (node.children ?? [])
    .map((child) =>
      typeof child === 'string' ? child : textOf(child as { children?: readonly unknown[] })
    )
    .join('');
}

// Renderer tests exercise domain state; browser acceptance covers the shared
// portal, focus, animation and close controls.
vi.mock('../components/ui/Modal', () => ({
  Modal: ({
    open,
    title,
    children,
    footer,
  }: {
    open: boolean;
    title?: ReactNode;
    children?: ReactNode;
    footer?: ReactNode;
  }) =>
    open ? (
      <div>
        {title ? <h2>{title}</h2> : null}
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

describe('Fresh resources within the original Console', () => {
  let renderer: ReactTestRenderer;
  const pending = vi.fn();
  beforeEach(async () => {
    await i18n.changeLanguage('zh-CN');
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    vi.stubGlobal('window', {
      location: new URL('https://manager.example/console'),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    vi.stubGlobal('crypto', { getRandomValues: (bytes: Uint8Array) => bytes.fill(0xbb) });
    pending.mockClear();
  });
  afterEach(async () => {
    if (renderer) await act(async () => renderer.unmount());
    vi.unstubAllGlobals();
  });
  function Navigation() {
    const navigate = useNavigate();
    return <select data-testid="navigate" onChange={(event) => navigate(event.target.value)} />;
  }
  async function mount(transport: ReturnType<typeof vi.fn<typeof fetch>>) {
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/console']}>
          <Navigation />
          <FreshManagedResources
            basePath={context.basePath}
            client={createPublicApiClient(() => context, transport)}
            credential="test-admin"
            onUnauthorized={vi.fn()}
            onPending={pending}
          />
        </MemoryRouter>
      );
    });
  }
  async function navigate(path: string) {
    await act(async () =>
      renderer.root
        .findByProps({ 'data-testid': 'navigate' })
        .props.onChange({ target: { value: path } })
    );
  }
  const panel = (name: string) =>
    renderer.root.findByProps({ 'data-testid': `fresh-route-${name}` });

  it.each(['', '/'])(
    'mounts resources only on their own page (suffix %j) and never adds an update-channel editor',
    async (suffix) => {
      const transport = vi
        .fn<typeof fetch>()
        .mockImplementation((url) =>
          Promise.resolve(
            String(url).endsWith('/api-keys')
              ? Response.json({ keys: [], gatewayPending: true, revocationAvailable: false })
              : new Response('', { status: 503 })
          )
        );
      await mount(transport);
      expect(transport).not.toHaveBeenCalled();
      await navigate('/console/api-keys' + suffix);
      expect(panel('keys').props.hidden).toBe(false);
      expect(renderer.root.findAllByProps({ 'data-testid': 'retry-editor' })).toHaveLength(0);
      expect(transport.mock.calls.map(([url]) => String(url))).toEqual([
        'https://manager.example/console/api/v2/api-keys',
        'https://manager.example/console/api/v2/api-keys/usage-summary',
      ]);
      await navigate('/console/config' + suffix);
      expect(panel('keys').props.hidden).toBe(true);
      expect(panel('retry').props.hidden).toBe(false);
      await navigate('/console/system/updates');
      expect(panel('retry').props.hidden).toBe(true);
      expect(transport.mock.calls.every(([url]) => !String(url).includes('update-channel'))).toBe(
        true
      );
    }
  );

  it('retains the same unknown key creation across navigation and reconciles it without another write', async () => {
    const record = {
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
    let failCreate!: (reason: Error) => void;
    const transport = vi.fn<typeof fetch>().mockImplementation((url, init) => {
      if (init?.method === 'POST') {
        return new Promise<Response>((_resolve, reject) => {
          failCreate = reject;
        });
      }
      return Promise.resolve(
        Response.json(
          String(url).includes('/requests/')
            ? record
            : {
                keys: [],
                gatewayPending: true,
                revocationAvailable: false,
              }
        )
      );
    });
    await mount(transport);
    await navigate('/console/api-keys');
    const button = (label: string) =>
      renderer.root
        .findAllByType('button')
        .find((node) => node.props.children === label || node.props['aria-label'] === label)!;
    await act(async () => button(t('keysCreate')).props.onClick());
    await act(async () => {
      renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() });
    });
    // While creation is in flight, the exit lock is held and the unknown-result
    // recovery panel does not flash prematurely.
    expect(pending).toHaveBeenLastCalledWith(true);
    const inFlightText = JSON.stringify(renderer.toJSON());
    expect(inFlightText).not.toContain(t('keysUnknown'));
    expect(inFlightText).not.toContain(k('pending'));

    await act(async () => {
      failCreate(new TypeError('reply lost'));
    });
    const settledText = JSON.stringify(renderer.toJSON());
    expect(settledText).toContain(t('keysUnknown'));
    expect(settledText).toContain(k('pending'));
    expect(pending).toHaveBeenLastCalledWith(true);
    expect(window.addEventListener).toHaveBeenCalledWith('beforeunload', expect.any(Function));
    await navigate('/console/system/updates');
    expect(panel('keys').props.hidden).toBe(true);
    expect(renderer.root.findAllByType('form')).toHaveLength(0);
    expect(pending).toHaveBeenLastCalledWith(true);
    await navigate('/console/api-keys');
    expect(renderer.root.findAllByType('form')).toHaveLength(1);
    await act(async () => button(t('keysCheck')).props.onClick());
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    expect(
      transport.mock.calls.some(([url]) => String(url).endsWith('/requests/' + record.requestId))
    ).toBe(true);
    expect(pending).toHaveBeenLastCalledWith(false);
  });

  it('updates the retained key workspace language without recreating a key or losing its secret', async () => {
    const record = {
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
    const transport = vi.fn<typeof fetch>().mockImplementation((url, init) => {
      if (String(url).endsWith('/api-keys/usage-summary'))
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'unknown', reason: 'usage_read_failed' },
            items: [],
          })
        );
      if (init?.method === 'POST')
        return Promise.resolve(Response.json({ key: record, createdSecret: secret }));
      return Promise.resolve(
        Response.json({ keys: [], gatewayPending: true, revocationAvailable: false })
      );
    });
    await mount(transport);
    await navigate('/console/api-keys');
    await act(async () =>
      renderer.root
        .findAllByType('button')
        .find((node) => node.props['aria-label'] === t('keysCreate'))!
        .props.onClick()
    );
    await act(async () =>
      renderer.root.findByType('form').props.onSubmit({ preventDefault: vi.fn() })
    );
    for (const language of ['en', 'ru', 'zh-TW']) {
      await act(async () => {
        await i18n.changeLanguage(language);
      });
      expect(panel('keys').props.lang).toBe(language);
      const view = JSON.stringify(renderer.toJSON());
      // The one-time key modal renders its secret heading once (`Modal.title`),
      // and its created summary line once.
      const secretHeadings = renderer.root.findAll(
        (node) =>
          ['h1', 'h2', 'h3', 'h4', 'h5', 'h6'].includes(String(node.type)) &&
          textOf(node) === k('secretTitle')
      );
      expect(secretHeadings).toHaveLength(1);
      const createdNotices = renderer.root.findAll(
        (node) =>
          ['p', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6'].includes(String(node.type)) &&
          textOf(node) === t('keysCreated')
      );
      expect(createdNotices).toHaveLength(1);
      expect(view).toContain(secret);
      expect(pending).toHaveBeenLastCalledWith(true);
    }
    expect(transport).toHaveBeenCalledTimes(3);
  });

  it('keeps an in-flight gateway save and its logout lock when another resource first loads', async () => {
    // The save never settles: the panel must keep its draft and its exit lock
    // across a navigation that unmounts the route it was edited on.
    const transport = vi.fn<typeof fetch>().mockImplementation((url, init) => {
      if (init?.method === 'POST') return new Promise<Response>(() => {});
      if (String(url).endsWith('/api-keys/usage-summary'))
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'unknown', reason: 'usage_read_failed' },
            items: [],
          })
        );
      if (String(url).endsWith('/api-keys'))
        return Promise.resolve(
          Response.json({ keys: [], gatewayPending: true, revocationAvailable: false })
        );
      return Promise.resolve(Response.json(gatewayViewFixture()));
    });
    await mount(transport);
    await navigate('/console/config');
    const node = (id: string) => renderer.root.findByProps({ 'data-testid': id });
    await act(async () => node('gateway-tab-retry').props.onClick());
    const field = () => node('gateway-field-routing.retry.request-retry');
    await act(async () => field().findByType('input').props.onChange({ target: { value: '3' } }));
    await act(async () =>
      node('gateway-group-retry')
        .findAll((entry) => entry.type === 'button' && entry.props.children === c('groupSave'))[0]
        .props.onClick()
    );
    // The write is in flight, so the exit lock is held.
    expect(pending).toHaveBeenLastCalledWith(true);
    await navigate('/console/api-keys');
    expect(panel('retry').props.hidden).toBe(true);
    expect(pending).toHaveBeenLastCalledWith(true);
    await navigate('/console/config');
    // The draft outlived the route change and the write was never resent.
    expect(field().findByType('input').props.value).toBe('3');
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
  });

  it('keeps the exit lock for a save whose reply was lost, until it is reconciled', async () => {
    const transport = vi.fn<typeof fetch>().mockImplementation((url, init) => {
      if (init?.method === 'POST') return Promise.reject(new TypeError('reply lost'));
      if (String(url).endsWith('/api-keys/usage-summary'))
        return Promise.resolve(
          Response.json({
            fromMs: 1,
            toMs: 2,
            coverage: { state: 'unknown', reason: 'usage_read_failed' },
            items: [],
          })
        );
      if (String(url).endsWith('/api-keys'))
        return Promise.resolve(
          Response.json({ keys: [], gatewayPending: true, revocationAvailable: false })
        );
      return Promise.resolve(Response.json(gatewayViewFixture()));
    });
    await mount(transport);
    await navigate('/console/config');
    const node = (id: string) => renderer.root.findByProps({ 'data-testid': id });
    await act(async () => node('gateway-tab-retry').props.onClick());
    const field = () => node('gateway-field-routing.retry.request-retry');
    await act(async () => field().findByType('input').props.onChange({ target: { value: '3' } }));
    await act(async () =>
      node('gateway-group-retry')
        .findAll((entry) => entry.type === 'button' && entry.props.children === c('groupSave'))[0]
        .props.onClick()
    );
    await act(async () => {
      await Promise.resolve();
    });
    // Nothing proves the document unchanged, so the result is unknown and the
    // group offers the original operation instead of a second save.
    expect(node('gateway-group-error-retry').props.children).toBe(c('groupSaveUnknown'));
    expect(node('gateway-group-reconcile-retry')).toBeDefined();
    expect(field().findByType('input').props.value).toBe('3');
    // Nobody could reconcile an abandoned operation from a signed-out page, so
    // the exit lock stays held until it settles.
    expect(pending).toHaveBeenLastCalledWith(true);
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    // The unresolved original is product state, not route state: leaving the
    // page and coming back must show the same reconcile action for the same
    // request, and still must not resend the write.
    await navigate('/console/api-keys');
    expect(panel('retry').props.hidden).toBe(true);
    expect(pending).toHaveBeenLastCalledWith(true);
    await navigate('/console/config');
    expect(node('gateway-group-error-retry').props.children).toBe(c('groupSaveUnknown'));
    expect(node('gateway-group-reconcile-retry')).toBeDefined();
    expect(field().findByType('input').props.value).toBe('3');
    expect(pending).toHaveBeenLastCalledWith(true);
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
  });

  it('mounts gateway workspace on /console/gateway with preserved retained state', async () => {
    const transport = vi
      .fn<typeof fetch>()
      .mockImplementation(() => Promise.resolve(Response.json(gatewayViewFixture())));
    await mount(transport);
    await navigate('/console/gateway');
    expect(panel('retry').props.hidden).toBe(false);
    expect(renderer.root.findByProps({ 'data-testid': 'gateway-config-workspace' })).toBeDefined();
    // One projection read, not one request per leaf.
    expect(
      transport.mock.calls.filter(([url]) => String(url).endsWith('/gateway/config'))
    ).toHaveLength(1);
  });
});
