import { StrictMode, useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ConfigRequestSupersededError, useConfigStore } from '@/stores';
import { LogsGate, MonitoringGate } from './MainRoutes';
const state = vi.hoisted(() => ({
  checking: false,
  available: true,
  reason: '' as string,
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => ({
    checking: state.checking,
    requestMonitoringAvailable: state.available,
    reason: state.reason,
  }),
}));

let renderer: ReactTestRenderer | undefined;
let pathname = '';

function LocationProbe() {
  const location = useLocation();
  useEffect(() => {
    pathname = location.pathname;
  }, [location.pathname]);
  return null;
}

async function mount({
  path = '/monitoring',
  unavailablePath,
}: { path?: string; unavailablePath?: string } = {}) {
  await act(async () => {
    renderer = create(
      <MemoryRouter initialEntries={[path]}>
        <LocationProbe />
        <Routes>
          <Route
            path="/monitoring"
            element={
              <MonitoringGate>
                <div data-testid="monitoring-page" />
              </MonitoringGate>
            }
          />
          <Route
            path="/monitoring/account-actions"
            element={
              <MonitoringGate unavailablePath={unavailablePath}>
                <div data-testid="account-actions-page" />
              </MonitoringGate>
            }
          />
          <Route path="/config" element={<div data-testid="config-page" />} />
        </Routes>
      </MemoryRouter>
    );
  });
}

beforeEach(() => {
  state.checking = false;
  state.available = true;
  state.reason = '';
  pathname = '';
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(async () => {
  if (renderer) await act(async () => renderer?.unmount());
  renderer = undefined;
});

describe('MonitoringGate', () => {
  it('renders the monitoring page when request monitoring is on', async () => {
    await mount();

    expect(renderer!.root.findAllByProps({ 'data-testid': 'monitoring-page' })).toHaveLength(1);
    expect(pathname).toBe('/monitoring');
  });

  it('keeps /monitoring when only the collector is switched off', async () => {
    state.available = false;
    state.reason = 'monitoring_disabled';
    await mount();

    expect(renderer!.root.findAllByProps({ 'data-testid': 'monitoring-page' })).toHaveLength(1);
    expect(pathname).toBe('/monitoring');
  });

  it('sends a monitoring data sub-page to the page that explains the switch', async () => {
    state.available = false;
    state.reason = 'monitoring_disabled';
    await mount({ path: '/monitoring/account-actions', unavailablePath: '/monitoring' });

    expect(pathname).toBe('/monitoring');
    expect(renderer!.root.findAllByProps({ 'data-testid': 'account-actions-page' })).toHaveLength(0);
    expect(renderer!.root.findAllByProps({ 'data-testid': 'monitoring-page' })).toHaveLength(1);
  });

  it('still leaves for the configuration workspace when no Manager service answers', async () => {
    state.available = false;
    state.reason = 'service_not_configured';
    await mount();

    expect(renderer!.root.findAllByProps({ 'data-testid': 'config-page' })).toHaveLength(1);
    expect(pathname).toBe('/config');
  });

  it('waits for the availability probe instead of redirecting on the initial state', async () => {
    state.checking = true;
    state.available = false;
    await mount();

    expect(renderer!.root.findAllByProps({ 'data-testid': 'monitoring-page' })).toHaveLength(0);
    expect(
      renderer!.root.findAll(
        (node) => typeof node.props.className === 'string' && node.props.className.includes('splash-screen-content')
      )
    ).toHaveLength(1);
    expect(
      renderer!.root.findAll(
        (node) => typeof node.props.className === 'string' && node.props.className.includes('loading-spinner')
      )
    ).toHaveLength(0);
    expect(pathname).toBe('/monitoring');
  });
});

describe('LogsGate', () => {
  it('recovers under StrictMode when the initial config fetch is superseded', async () => {
    const initialStore = useConfigStore.getState();
    let callCount = 0;
    const fetchConfigMock = vi.fn(async () => {
      callCount += 1;
      if (callCount === 1) {
        throw new ConfigRequestSupersededError('superseded during mount');
      }
      const loaded = { debug: false, raw: {} };
      useConfigStore.setState({ config: loaded });
      return loaded;
    });

    useConfigStore.setState({
      config: null,
      fetchConfig: fetchConfigMock as typeof initialStore.fetchConfig,
    });

    try {
      await act(async () => {
        renderer = create(
          <StrictMode>
            <LogsGate>
              <div data-testid="logs-page" />
            </LogsGate>
          </StrictMode>
        );
      });
      await act(async () => {
        await Promise.resolve();
      });

      expect(fetchConfigMock.mock.calls.length).toBeGreaterThanOrEqual(2);
      expect(renderer!.root.findAllByProps({ 'data-testid': 'logs-page' })).toHaveLength(1);
    } finally {
      useConfigStore.setState({
        config: initialStore.config,
        fetchConfig: initialStore.fetchConfig,
      });
    }
  });
});
