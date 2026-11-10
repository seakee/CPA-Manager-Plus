import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { PluginGate } from './MainRoutes';

const state = vi.hoisted(() => ({
  list: vi.fn<() => Promise<unknown>>(),
  apiBase: 'https://cpa.one.example.com',
  managementKey: 'key-one',
  supportsPlugin: false,
  updateServerPluginSupport: vi.fn<(supports: boolean) => void>((supports) => {
    state.supportsPlugin = supports;
  }),
}));

vi.mock('@/services/api/plugins', () => ({
  pluginsApi: { list: state.list },
}));

vi.mock('@/stores', () => ({
  useAuthStore: (
    selector: (storeState: {
      supportsPlugin: boolean;
      apiBase: string;
      managementKey: string;
      updateServerPluginSupport: (supports: boolean) => void;
    }) => unknown
  ) =>
    selector({
      supportsPlugin: state.supportsPlugin,
      apiBase: state.apiBase,
      managementKey: state.managementKey,
      updateServerPluginSupport: state.updateServerPluginSupport,
    }),
}));

const failure = (status: number) => Object.assign(new Error(`request failed: ${status}`), { status });

let renderer: ReactTestRenderer | undefined;
let pathname = '';

function LocationProbe() {
  const location = useLocation();
  useEffect(() => {
    pathname = location.pathname;
  }, [location.pathname]);
  return null;
}

async function render() {
  await act(async () => {
    renderer = create(
      <MemoryRouter initialEntries={['/console/plugins']}>
        <LocationProbe />
        <Routes>
          <Route
            path="/console/plugins"
            element={
              <PluginGate>
                <div data-testid="plugins-page" />
              </PluginGate>
            }
          />
          <Route path="/" element={<div data-testid="dashboard-page" />} />
        </Routes>
      </MemoryRouter>
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

async function retryProbe() {
  const buttons = renderer!.root.findAllByType('button');
  expect(buttons).toHaveLength(1);
  await act(async () => {
    buttons[0].props.onClick();
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const page = () => renderer!.root.findAll(
  (node) => node.props['data-testid'] === 'plugins-page'
);

beforeEach(() => {
  pathname = '';
  state.apiBase = 'https://cpa.one.example.com';
  state.managementKey = 'key-one';
  state.supportsPlugin = false;
  state.list.mockReset();
  state.updateServerPluginSupport.mockClear();
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
});

afterEach(async () => {
  if (renderer) await act(async () => renderer?.unmount());
  renderer = undefined;
});

describe('PluginGate', () => {
  it('offers a retry instead of redirecting when the probe fails for reasons other than a missing namespace', async () => {
    state.list.mockRejectedValueOnce(failure(401));
    await render();

    expect(pathname).toBe('/console/plugins');
    expect(page()).toHaveLength(0);
    expect(
      renderer!.root.findAll((node) => node.props['data-testid'] === 'plugin-probe-error')
    ).toHaveLength(1);
    expect(renderer!.root.findAllByType('button')).toHaveLength(1);
  });

  it('renders the plugin page after a retried probe succeeds', async () => {
    state.list.mockRejectedValueOnce(new Error('network request failed'));
    await render();

    state.list.mockResolvedValueOnce({ items: [] });
    await retryProbe();

    expect(page()).toHaveLength(1);
    expect(pathname).toBe('/console/plugins');
  });

  it('synchronizes supportsPlugin into the shared auth store when a cold-load probe succeeds without a response header', async () => {
    state.list.mockResolvedValueOnce({ items: [] });
    await render();

    expect(state.updateServerPluginSupport).toHaveBeenCalledTimes(1);
    expect(state.updateServerPluginSupport).toHaveBeenCalledWith(true);
    expect(state.supportsPlugin).toBe(true);
    expect(page()).toHaveLength(1);
    expect(pathname).toBe('/console/plugins');
  });

  it('redirects to the dashboard only when the CPA refuses the plugin namespace itself', async () => {
    state.list.mockRejectedValueOnce(failure(404));
    await render();

    expect(pathname).toBe('/');
    expect(page()).toHaveLength(0);
  });

  it('probes the new connection instead of keeping the previous verdict', async () => {
    state.list.mockRejectedValueOnce(failure(500));
    await render();
    expect(pathname).toBe('/console/plugins');
    expect(page()).toHaveLength(0);

    // Same gate, same mounted tree: the Console is now pointed at a CPA that
    // answers the plugin namespace, so the previous verdict must not stick.
    state.apiBase = 'https://cpa.two.example.com';
    state.list.mockResolvedValueOnce({ items: [] });
    await act(async () => {
      renderer!.update(
        <MemoryRouter initialEntries={['/console/plugins']}>
          <LocationProbe />
          <Routes>
            <Route
              path="/console/plugins"
              element={
                <PluginGate>
                  <div data-testid="plugins-page" />
                </PluginGate>
              }
            />
            <Route path="/" element={<div data-testid="dashboard-page" />} />
          </Routes>
        </MemoryRouter>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(state.list).toHaveBeenCalledTimes(2);
    expect(page()).toHaveLength(1);
  });

  it('re-probes automatically when managementKey changes on the same apiBase after a 401', async () => {
    state.list.mockRejectedValueOnce(failure(401));
    await render();
    expect(page()).toHaveLength(0);
    expect(
      renderer!.root.findAll((node) => node.props['data-testid'] === 'plugin-probe-error')
    ).toHaveLength(1);

    state.managementKey = 'key-two';
    state.list.mockResolvedValueOnce({ items: [] });
    await act(async () => {
      renderer!.update(
        <MemoryRouter initialEntries={['/console/plugins']}>
          <LocationProbe />
          <Routes>
            <Route
              path="/console/plugins"
              element={
                <PluginGate>
                  <div data-testid="plugins-page" />
                </PluginGate>
              }
            />
            <Route path="/" element={<div data-testid="dashboard-page" />} />
          </Routes>
        </MemoryRouter>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(state.list).toHaveBeenCalledTimes(2);
    expect(state.supportsPlugin).toBe(true);
    expect(page()).toHaveLength(1);
  });
});
