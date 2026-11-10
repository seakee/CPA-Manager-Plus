import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ManagerConfig, ManagerConfigResponse } from '@/services/api/usageService';
import { useRequestMonitoringSwitch, type RequestMonitoringSwitch } from './useRequestMonitoringSwitch';

const mocks = vi.hoisted(() => ({
  getManagerConfig: vi.fn<(base: string, key?: string) => Promise<ManagerConfigResponse>>(),
  saveManagerConfig: vi.fn<
    (base: string, config: ManagerConfig, key?: string) => Promise<ManagerConfigResponse>
  >(),
  refreshPanelFeatureAvailability: vi.fn(),
  managerServiceBase: '/console/api/v2/cpamp',
  managementKey: 'admin-key',
}));

vi.mock('@/services/api/usageService', () => ({
  usageServiceApi: {
    getManagerConfig: mocks.getManagerConfig,
    saveManagerConfig: mocks.saveManagerConfig,
  },
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => ({
    managerServiceBase: mocks.managerServiceBase,
    checking: false,
  }),
  refreshPanelFeatureAvailability: mocks.refreshPanelFeatureAvailability,
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: { managementKey: string }) => unknown) =>
    selector({ managementKey: mocks.managementKey }),
}));

const stored: ManagerConfig = {
  cpaConnection: { cpaBaseUrl: 'http://cpa.internal:8317', managementKeyConfigured: true },
  collector: {
    enabled: true,
    collectorMode: 'resp',
    queue: 'usage-events',
    popSide: 'right',
    batchSize: 250,
    pollIntervalMs: 1500,
    queryLimit: 20000,
    tlsSkipVerify: true,
  },
  codexInspection: { enabled: true, workers: 4 },
  externalUsageService: { enabled: true, serviceBase: 'https://usage.example.com' },
};

const response = (config: ManagerConfig): ManagerConfigResponse => ({ config, source: 'db' });

let renderer: ReactTestRenderer | undefined;
let current: RequestMonitoringSwitch | undefined;

function Probe() {
  const state = useRequestMonitoringSwitch();
  useEffect(() => {
    current = state;
  });
  return null;
}

async function mount() {
  await act(async () => {
    renderer = create(<Probe />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

beforeEach(() => {
  current = undefined;
  mocks.getManagerConfig.mockReset();
  mocks.saveManagerConfig.mockReset();
  mocks.refreshPanelFeatureAvailability.mockReset();
  mocks.getManagerConfig.mockResolvedValue(response(stored));
  mocks.saveManagerConfig.mockImplementation(
    async (_base: string, config: ManagerConfig) => response(config)
  );
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(async () => {
  if (renderer) await act(async () => renderer?.unmount());
  renderer = undefined;
});

describe('useRequestMonitoringSwitch', () => {
  it('reads the stored collector state from the Manager service', async () => {
    await mount();

    expect(mocks.getManagerConfig).toHaveBeenCalledWith(
      '/console/api/v2/cpamp',
      'admin-key'
    );
    expect(current?.loaded).toBe(true);
    expect(current?.enabled).toBe(true);
    expect(current?.writable).toBe(true);
  });

  it('writes the config it just read with only collector.enabled changed', async () => {
    await mount();

    await act(async () => {
      await current!.setEnabled(false);
    });

    expect(mocks.saveManagerConfig).toHaveBeenCalledTimes(1);
    const [base, payload, key] = mocks.saveManagerConfig.mock.calls[0];
    expect(base).toBe('/console/api/v2/cpamp');
    expect(key).toBe('admin-key');
    expect(payload).toEqual({
      ...stored,
      collector: { ...stored.collector, enabled: false },
    });
    expect(payload.cpaConnection).toEqual(stored.cpaConnection);
    expect(payload.codexInspection).toEqual(stored.codexInspection);
    expect(payload.externalUsageService).toEqual(stored.externalUsageService);
    expect(current?.enabled).toBe(false);
    // Sidebar entry and route gate read the same availability probe.
    expect(mocks.refreshPanelFeatureAvailability).toHaveBeenCalledTimes(1);
  });

  it('keeps the stored state and reports the failure when the write is rejected', async () => {
    mocks.saveManagerConfig.mockRejectedValueOnce(new Error('cpa unreachable'));
    await mount();

    await act(async () => {
      await current!.setEnabled(false);
    });

    expect(current?.enabled).toBe(true);
    expect(current?.failed).toBe(true);
    expect(mocks.refreshPanelFeatureAvailability).not.toHaveBeenCalled();
  });

  it('never calls the service when no Manager service is available', async () => {
    mocks.managerServiceBase = '';
    await mount();

    await act(async () => {
      await current!.setEnabled(true);
    });

    expect(mocks.getManagerConfig).not.toHaveBeenCalled();
    expect(mocks.saveManagerConfig).not.toHaveBeenCalled();
    expect(current?.writable).toBe(false);
    mocks.managerServiceBase = '/console/api/v2/cpamp';
  });
});
