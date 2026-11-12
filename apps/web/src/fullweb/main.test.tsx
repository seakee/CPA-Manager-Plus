import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, expect, it, vi } from 'vitest';

function textOf(node: ReactTestInstance): string {
  return node.children
    .map((child) => (typeof child === 'string' ? child : textOf(child)))
    .join('');
}
const { render } = vi.hoisted(() => ({ render: vi.fn() }));
vi.mock('react-dom/client', () => ({ createRoot: () => ({ render }) }));
vi.mock('react-router-dom', () => ({ createBrowserRouter: vi.fn(), RouterProvider: () => null }));
afterEach(() => {
  vi.unstubAllGlobals();
  vi.resetModules();
  render.mockClear();
});

it('consumes a new host Handoff in an already-open Bootstrap tab and retires its previous in-memory transport', async () => {
  const location = new URL('https://manager.example/.well-known/cpamp-bootstrap/ui');
  let hashChanged!: () => void;
  vi.stubGlobal('window', {
    location,
    history: {
      replaceState: (_: unknown, _title: string, path: string) => {
        location.href = new URL(path, location.origin).href;
      },
    },
    addEventListener: (name: string, handler: () => void) => {
      if (name === 'hashchange') hashChanged = handler;
    },
  });
  vi.stubGlobal('document', { getElementById: () => null });
  await import('./main');
  const first = render.mock.calls[0][0].props.children[1].props.children;
  expect(first.props.handoff).toBeNull();
  const clear = vi.spyOn(first.props.transport, 'clear');
  const handoff = {
    identity: {
      admissionId: 'admission',
      installationId: 'installation',
      generation: 'generation',
      operationId: 'fresh-op',
      databasePath: '/data/usage.sqlite',
    },
    claimGeneration: 2,
    token: 'x'.repeat(43),
    resume: true,
  };
  location.hash =
    '#handoff=' +
    btoa(JSON.stringify(handoff)).replace(/=/g, '').replace(/\+/g, '-').replace(/\//g, '_');
  hashChanged();
  const second = render.mock.calls[1][0].props.children[1].props.children;
  expect(clear).toHaveBeenCalledOnce();
  expect(location.hash).toBe('');
  expect(second.key).not.toBe(first.key);
  expect(second.props.transport).not.toBe(first.props.transport);
  expect(second.props.handoff).toEqual(handoff);
});

it('renders unfinished-install Bootstrap guidance without consuming #handoff on an uncommitted initial route', async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  const validHandoff =
    '#handoff=' +
    btoa(
      JSON.stringify({
        identity: {
          admissionId: 'admission',
          installationId: 'installation',
          generation: 'generation',
          operationId: 'fresh-op',
          databasePath: '/data/usage.sqlite',
        },
        claimGeneration: 2,
        token: 'x'.repeat(43),
        resume: true,
      })
    )
      .replace(/=/g, '')
      .replace(/\+/g, '-')
      .replace(/\//g, '_');
  const location = new URL(`https://manager.example/console?probe=1${validHandoff}`);
  let hashChanged!: () => void;
  const fetchMock = vi.fn().mockResolvedValue(
    Response.json(
      {
        contract_version: 'cpamp.phase4-01.discovery.v1',
        status: 'fresh_claim_required',
        mode: 'fresh',
        auth_entry: 'fresh_claim',
        reason: 'fresh_resume_required',
        authentication_required: true,
      },
      { status: 200 }
    )
  );
  vi.stubGlobal('fetch', fetchMock);
  vi.stubGlobal('window', {
    location,
    history: {
      replaceState: (_: unknown, _title: string, path: string) => {
        location.href = new URL(path, location.origin).href;
      },
    },
    addEventListener: (name: string, handler: () => void) => {
      if (name === 'hashchange') hashChanged = handler;
    },
  });
  vi.stubGlobal('document', {
    documentElement: {
      setAttribute: vi.fn(),
      classList: { toggle: vi.fn() },
    },
    getElementById: (id: string) =>
      id === 'cpamp-routing-context'
        ? {
            textContent: '{"state":"initial_routing"}',
            getAttribute: () => 'application/json',
          }
        : null,
  });
  // main.tsx runs its bootstrap routing side effects at module evaluation time.
  await import('./main');
  expect(location.search).toBe('?probe=1');
  expect(location.hash).toBe('');

  let mounted!: ReactTestRenderer;
  await act(async () => {
    mounted = create(render.mock.calls[0][0]);
  });
  const shell = mounted.root.findByProps({ 'data-testid': 'fresh-bootstrap-shell' });
  expect(textOf(shell)).not.toContain('Routing authority unavailable');
  expect(mounted.root.findByProps({ 'data-testid': 'fresh-resume-instructions' })).toBeTruthy();
  expect(
    textOf(mounted.root.findByProps({ 'data-testid': 'cli-handoff-command' }))
  ).toBe('cpa-manager-plus initialize');
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(fetchMock.mock.calls[0][0]).toBe('/.well-known/cpamp-bootstrap');

  location.hash = validHandoff;
  hashChanged();
  expect(location.hash).toBe('');
  expect(location.search).toBe('?probe=1');
  expect(render).toHaveBeenCalledTimes(1);
  expect(fetchMock).toHaveBeenCalledTimes(1);
  await act(async () => mounted.unmount());
});

it('fails closed to Routing authority unavailable when the routing context carrier is malformed', async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  vi.stubGlobal('window', {
    location: new URL('https://manager.example/console'),
    history: { replaceState: vi.fn() },
    addEventListener: vi.fn(),
  });
  vi.stubGlobal('document', {
    getElementById: (id: string) =>
      id === 'cpamp-routing-context'
        ? {
            textContent: '{broken',
            getAttribute: () => 'application/json',
          }
        : null,
  });
  await import('./main');
  let mounted!: ReactTestRenderer;
  await act(async () => {
    mounted = create(render.mock.calls[0][0]);
  });
  expect(textOf(mounted.root.findByType('p'))).toBe('Routing authority unavailable');
  await act(async () => mounted.unmount());
});
