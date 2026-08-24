import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { UsageServiceStatus } from '@/services/api/usageService';
import { DatabaseMaintenanceProvider } from './DatabaseMaintenanceContext';
import {
  useDatabaseMaintenance,
  type DatabaseMaintenanceContextValue,
} from './useDatabaseMaintenance';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const mocks = vi.hoisted(() => ({
  getStatus: vi.fn(),
  authState: {
    managementKey: 'manager-admin-key',
    connectionStatus: 'connected' as 'connected' | 'disconnected',
  },
  availability: {
    checking: false,
    managerServiceAvailable: true,
    managerServiceBase: 'http://manager.local:18317',
  },
}));

vi.mock('@/services/api/usageService', () => ({
  usageServiceApi: { getStatus: mocks.getStatus },
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: typeof mocks.authState) => unknown) => selector(mocks.authState),
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => mocks.availability,
}));

const maintenanceStatus = (required: boolean): UsageServiceStatus => ({
  databaseMaintenance: {
    required,
    performanceDegraded: required,
    deferredIndexes: required ? 10 : 0,
    offlineJobs: required ? 1 : 0,
    reasons: required ? ['deferred_indexes', 'offline_derived_cleanup'] : [],
    command: required ? 'cleanup-derived' : undefined,
  },
});

let renderer: ReactTestRenderer | null = null;
const contextObserver = vi.fn<(value: DatabaseMaintenanceContextValue) => void>();

function Probe() {
  const value = useDatabaseMaintenance();
  useEffect(() => contextObserver(value), [value]);
  return null;
}

const latestContext = () => {
  const calls = contextObserver.mock.calls;
  return calls[calls.length - 1]?.[0];
};

beforeEach(() => {
  contextObserver.mockReset();
  mocks.getStatus.mockReset();
  vi.stubGlobal('window', {
    setInterval: vi.fn(() => 1),
    clearInterval: vi.fn(),
  });
  vi.stubGlobal('document', {
    visibilityState: 'visible',
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  });
});

afterEach(() => {
  if (renderer) {
    act(() => renderer?.unmount());
    renderer = null;
  }
  vi.unstubAllGlobals();
});

describe('DatabaseMaintenanceProvider', () => {
  it('loads degraded metadata and recovers automatically after cleanup is observed', async () => {
    mocks.getStatus
      .mockResolvedValueOnce(maintenanceStatus(true))
      .mockResolvedValueOnce(maintenanceStatus(false));

    await act(async () => {
      renderer = create(
        <DatabaseMaintenanceProvider>
          <Probe />
        </DatabaseMaintenanceProvider>
      );
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.getStatus).toHaveBeenCalledWith(
      'http://manager.local:18317',
      'manager-admin-key',
      'database-maintenance'
    );
    expect(latestContext()?.status?.databaseMaintenance).toMatchObject({
      required: true,
      deferredIndexes: 10,
      offlineJobs: 1,
    });

    await act(async () => {
      await latestContext()?.refresh();
    });

    expect(latestContext()?.status?.databaseMaintenance).toEqual({
      required: false,
      performanceDegraded: false,
      deferredIndexes: 0,
      offlineJobs: 0,
      reasons: [],
    });
  });

  it('retains the last warning when a later bounded status request fails transiently', async () => {
    mocks.getStatus
      .mockResolvedValueOnce(maintenanceStatus(true))
      .mockRejectedValueOnce(new Error('temporary status failure'));

    await act(async () => {
      renderer = create(
        <DatabaseMaintenanceProvider>
          <Probe />
        </DatabaseMaintenanceProvider>
      );
      await Promise.resolve();
      await Promise.resolve();
    });

    await act(async () => {
      await latestContext()?.refresh();
    });

    expect(latestContext()?.status?.databaseMaintenance?.required).toBe(true);
    expect(latestContext()?.error).toBe('temporary status failure');
  });

  it('clears the previous server state immediately when the connection identity changes', async () => {
    let resolvePendingStatus: ((status: UsageServiceStatus) => void) | undefined;
    mocks.getStatus
      .mockResolvedValueOnce(maintenanceStatus(true))
      .mockImplementationOnce(
        () =>
          new Promise<UsageServiceStatus>((resolve) => {
            resolvePendingStatus = resolve;
          })
      );

    await act(async () => {
      renderer = create(
        <DatabaseMaintenanceProvider>
          <Probe />
        </DatabaseMaintenanceProvider>
      );
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(latestContext()?.status?.databaseMaintenance?.required).toBe(true);

    await act(async () => {
      void latestContext()?.refresh();
      await Promise.resolve();
    });
    expect(latestContext()?.status?.databaseMaintenance?.required).toBe(true);

    mocks.authState.connectionStatus = 'disconnected';
    await act(async () => {
      renderer?.update(
        <DatabaseMaintenanceProvider>
          <Probe />
        </DatabaseMaintenanceProvider>
      );
      await Promise.resolve();
    });

    expect(latestContext()?.status).toBeNull();
    expect(latestContext()?.error).toBe('');

    await act(async () => {
      resolvePendingStatus?.(maintenanceStatus(true));
      await Promise.resolve();
    });
    expect(latestContext()?.status).toBeNull();
    expect(mocks.getStatus).toHaveBeenCalledTimes(2);
  });
});
