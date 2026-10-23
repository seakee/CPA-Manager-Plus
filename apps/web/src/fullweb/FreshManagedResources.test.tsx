import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter, useNavigate } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FreshManagedResources } from './FreshManagedResources';
import { createPublicApiClient } from './publicApi';
import { fullWebText as t } from '../i18n/fullweb';

const context = { basePath: '/console', apiBase: '/console/api/v2', revision: 1 };

describe('Fresh resources within the original Console', () => {
  let renderer: ReactTestRenderer;
  const pending = vi.fn();
  beforeEach(() => {
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

  it.each(['', '/'])('mounts resources only on their own page (suffix %j) and never adds an update-channel editor', async (suffix) => {
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
    ]);
    await navigate('/console/config' + suffix);
    expect(panel('keys').props.hidden).toBe(true);
    expect(panel('retry').props.hidden).toBe(false);
    await navigate('/console/system/updates');
    expect(panel('retry').props.hidden).toBe(true);
    expect(transport.mock.calls.every(([url]) => !String(url).includes('update-channel'))).toBe(
      true
    );
  });

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
    };
    const transport = vi.fn<typeof fetch>().mockImplementation((url, init) => {
      if (init?.method === 'POST') return Promise.reject(new TypeError('reply lost'));
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
    expect(pending).toHaveBeenLastCalledWith(true);
    expect(window.addEventListener).toHaveBeenCalledWith('beforeunload', expect.any(Function));
    await navigate('/console/system/updates');
    expect(panel('keys').props.hidden).toBe(true);
    expect(pending).toHaveBeenLastCalledWith(true);
    await navigate('/console/api-keys');
    await act(async () => button(t('keysCheck')).props.onClick());
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    expect(
      transport.mock.calls.some(([url]) => String(url).endsWith('/requests/' + record.requestId))
    ).toBe(true);
    expect(pending).toHaveBeenLastCalledWith(false);
  });

  it('keeps an unresolved retry and its logout lock when another resource first loads', async () => {
    const expected = {
      sourceId: 'private-source',
      generation: '7',
      revision: 2,
      sha256: 'a'.repeat(64),
    };
    const transport = vi.fn<typeof fetch>().mockImplementation((url, init) => {
      if (init?.method === 'PUT') return Promise.reject(new TypeError('reply lost'));
      if (String(url).endsWith('/api-keys'))
        return Promise.resolve(
          Response.json({
            keys: [],
            gatewayPending: true,
            revocationAvailable: false,
          })
        );
      return Promise.resolve(
        Response.json({
          field: String(url).includes('max-retry-interval')
            ? 'routing.retry.max-retry-interval'
            : 'routing.retry.request-retry',
          presence: 'present',
          value: 2,
          source: 'private-installation',
          authority: 'host-runtime',
          editable: true,
          reason: '',
          persisted: 'persisted',
          effective: 'unknown',
          concurrentProtection: 'host-exclusive-runtime-reservation',
          expected,
        })
      );
    });
    await mount(transport);
    await navigate('/console/config');
    const node = (id: string) => renderer.root.findByProps({ 'data-testid': id });
    await act(async () => node('retry-value').props.onChange({ target: { value: '3' } }));
    await act(async () => node('retry-save').props.onClick());
    const originalId = node('retry-operation-id').props.children;
    expect(originalId).toBe('b'.repeat(32));
    expect(pending).toHaveBeenLastCalledWith(true);
    await navigate('/console/api-keys');
    expect(panel('retry').props.hidden).toBe(true);
    expect(pending).toHaveBeenLastCalledWith(true);
    await navigate('/console/config');
    expect(node('retry-operation-id').props.children).toBe(originalId);
    expect(transport.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
  });

  it('mounts gateway workspace on /console/gateway with preserved retained state', async () => {
    const transport = vi.fn<typeof fetch>().mockResolvedValue(
      Response.json({
        field: 'routing.retry.request-retry',
        presence: 'present',
        value: 3,
        source: 'private-embedded-cpa-v8.0.13',
        authority: 'host-runtime',
        editable: true,
        reason: '',
        persisted: 'persisted',
        effective: 'unknown',
        concurrentProtection: 'host-exclusive-runtime-reservation',
        expected: {
          sourceId: 'private-source',
          generation: 'gen-1',
          revision: 1,
          sha256: 'a'.repeat(64),
        },
      })
    );
    await mount(transport);
    await navigate('/console/gateway');
    expect(panel('retry').props.hidden).toBe(false);
    expect(renderer.root.findByProps({ 'data-testid': 'fresh-gateway-workspace' })).toBeDefined();
  });
});
