import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { useUsageData, type UseUsageDataReturn } from './useUsageData';

const mocks = vi.hoisted(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  return {
    availability: {} as PanelFeatureAvailability,
    auth: { managementKey: 'test-key', apiBase: 'http://gateway.local' },
    notify: vi.fn(),
    getApiKeyAliases: vi.fn(),
    getModelPrices: vi.fn(),
    events: vi.fn(),
  };
});

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => mocks.availability,
}));
vi.mock('@/i18n', () => ({ default: { t: (key: string) => key } }));
vi.mock('@/stores/useNotificationStore', () => ({
  useNotificationStore: { getState: () => ({ showNotification: mocks.notify }) },
}));
vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: typeof mocks.auth) => unknown) => selector(mocks.auth),
}));
vi.mock('@/services/api/usageService', () => ({
  usageServiceApi: {
    getModelPrices: mocks.getModelPrices,
    saveModelPrices: vi.fn(),
    getApiKeyAliases: mocks.getApiKeyAliases,
    getUsageEvents: mocks.events,
    syncModelPrices: vi.fn(),
    exportUsage: vi.fn(),
    importUsage: vi.fn(),
    cancelUsageImport: vi.fn(),
  },
}));
vi.mock('@/utils/usage', () => ({
  loadModelPrices: () => ({}),
  saveModelPrices: vi.fn(),
  clearModelPrices: vi.fn(),
  loadModelPriceFallback: () => ({}),
  saveModelPriceFallback: vi.fn(),
  clearModelPriceFallback: vi.fn(),
}));
vi.mock('@/features/monitoring/services/usageImportSession', () => ({
  cancelUsageImportFile: vi.fn(),
  uploadUsageImportFile: vi.fn(),
}));

const availability = (managerServiceBase: string): PanelFeatureAvailability => ({
  checking: false,
  panelHostConfirmed: true,
  panelHostMode: 'manager_embedded',
  panelBase: managerServiceBase,
  managerServiceBase,
  managerServiceAvailable: true,
  requestMonitoringAvailable: true,
  modelPricesAvailable: true,
  serverCodexInspectionAvailable: true,
  dockerSetupAvailable: true,
  externalManagerConfigAvailable: false,
  reason: '',
});

describe('useUsageData api key alias revalidation', () => {
  let renderer: ReactTestRenderer | undefined;
  let latest: UseUsageDataReturn;

  function Harness() {
    const result = useUsageData({ loadUsageEvents: false });
    useEffect(() => {
      latest = result;
    }, [result]);
    return null;
  }

  const mount = async () => {
    await act(async () => {
      renderer = create(<Harness />);
    });
  };

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.availability = availability('http://manager.local');
    mocks.getModelPrices.mockResolvedValue({ prices: {} });
    mocks.getApiKeyAliases.mockResolvedValue({ items: [] });
    mocks.events.mockResolvedValue({ events: [], total: 0 });
  });

  afterEach(() => {
    act(() => renderer?.unmount());
    renderer = undefined;
  });

  it('keeps the last good aliases through a pending refresh and a failure', async () => {
    // The hook loads aliases on mount, so the seeded response must be the default.
    mocks.getApiKeyAliases.mockResolvedValue({
      items: [{ apiKeyHash: 'hash-a', alias: 'last-good' }],
    });
    await mount();
    await act(async () => {
      await latest.loadApiKeyAliases();
    });
    expect(latest.apiKeyAliases[0]?.alias).toBe('last-good');

    let reject!: (error: Error) => void;
    mocks.getApiKeyAliases.mockReturnValueOnce(
      new Promise((_resolve, no) => {
        reject = no;
      })
    );
    let pending!: Promise<void>;
    act(() => {
      pending = latest.loadApiKeyAliases();
    });
    expect(latest.apiKeyAliases[0]?.alias).toBe('last-good');

    await act(async () => {
      reject(new Error('offline'));
      await pending;
    });
    expect(latest.apiKeyAliases[0]?.alias).toBe('last-good');
    expect(mocks.notify).toHaveBeenCalledWith('notification.refresh_failed', 'error');
  });

  it('drops aliases when the manager scope changes and ignores the late old reply', async () => {
    mocks.getApiKeyAliases.mockResolvedValue({
      items: [{ apiKeyHash: 'hash-a', alias: 'old-scope' }],
    });
    await mount();
    await act(async () => {
      await latest.loadApiKeyAliases();
    });
    expect(latest.apiKeyAliases[0]?.alias).toBe('old-scope');

    let resolveOld!: (response: { items: Array<{ apiKeyHash: string; alias: string }> }) => void;
    mocks.getApiKeyAliases.mockReturnValueOnce(
      new Promise((yes) => {
        resolveOld = yes;
      })
    );
    let pending!: Promise<void>;
    act(() => {
      pending = latest.loadApiKeyAliases();
    });

    mocks.getApiKeyAliases.mockReturnValueOnce(new Promise(() => {}));
    mocks.availability = availability('http://new-manager.local');
    await act(async () => {
      renderer!.update(<Harness />);
    });
    expect(latest.apiKeyAliases).toEqual([]);

    await act(async () => {
      resolveOld({ items: [{ apiKeyHash: 'old-hash', alias: 'stale' }] });
      await pending;
    });
    expect(latest.apiKeyAliases).toEqual([]);

    mocks.getApiKeyAliases.mockResolvedValueOnce({
      items: [{ apiKeyHash: 'new-hash', alias: 'new-scope' }],
    });
    await act(async () => {
      await latest.loadApiKeyAliases();
    });
    expect(latest.apiKeyAliases[0]?.alias).toBe('new-scope');
  });
});
