import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import type { ModelPrice } from '@/utils/usage';
import { useUsageData, type UseUsageDataReturn } from './useUsageData';

const mocks = vi.hoisted(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  return {
    availability: {} as PanelFeatureAvailability,
    getModelPrices: vi.fn(),
    saveModelPrices: vi.fn(),
    getApiKeyAliases: vi.fn(),
    loadLocalPrices: vi.fn(),
    saveLocalPrices: vi.fn(),
    clearLocalPrices: vi.fn(),
  };
});

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => mocks.availability,
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: { managementKey: string }) => unknown) =>
    selector({ managementKey: 'test-key' }),
}));

vi.mock('@/services/api/usageService', () => ({
  usageServiceApi: {
    getModelPrices: mocks.getModelPrices,
    saveModelPrices: mocks.saveModelPrices,
    getApiKeyAliases: mocks.getApiKeyAliases,
  },
}));

vi.mock('@/utils/usage', () => ({
  loadModelPrices: mocks.loadLocalPrices,
  saveModelPrices: mocks.saveLocalPrices,
  clearModelPrices: mocks.clearLocalPrices,
}));

vi.mock('@/features/monitoring/services/usageImportSession', () => ({
  cancelUsageImportFile: vi.fn(),
  uploadUsageImportFile: vi.fn(),
}));

const savedPrices: Record<string, ModelPrice> = {
  'saved-model': { prompt: 1, completion: 2, cache: 0.5 },
};
const changedPrices: Record<string, ModelPrice> = {
  ...savedPrices,
  'new-model': { prompt: 3, completion: 4, cache: 1 },
};

describe('useUsageData model price persistence', () => {
  let renderer: ReactTestRenderer | undefined;
  let latest: UseUsageDataReturn;

  function Harness() {
    const result = useUsageData({ loadUsageEvents: false });
    useEffect(() => {
      latest = result;
    }, [result]);
    return null;
  }

  const renderHook = async () => {
    await act(async () => {
      renderer = create(<Harness />);
    });
    mocks.clearLocalPrices.mockClear();
  };

  beforeEach(() => {
    vi.resetAllMocks();
    mocks.availability = {
      checking: false,
      panelHostConfirmed: true,
      panelHostMode: 'manager_embedded',
      panelBase: 'http://manager.local',
      managerServiceBase: 'http://manager.local',
      managerServiceAvailable: true,
      requestMonitoringAvailable: true,
      modelPricesAvailable: true,
      serverCodexInspectionAvailable: true,
      dockerSetupAvailable: true,
      externalManagerConfigAvailable: false,
      reason: '',
    };
    mocks.loadLocalPrices.mockReturnValue({});
    mocks.getModelPrices.mockResolvedValue({ prices: savedPrices });
    mocks.getApiKeyAliases.mockResolvedValue({ items: [] });
  });

  afterEach(() => {
    act(() => renderer?.unmount());
    renderer = undefined;
  });

  it('waits for server confirmation and then uses the canonical prices', async () => {
    const canonicalPrices = {
      'new-model': { prompt: 3, completion: 4, cache: 1, source: 'manual' },
    };
    let resolve!: (response: { prices: Record<string, ModelPrice> }) => void;
    mocks.saveModelPrices.mockReturnValueOnce(
      new Promise((resolvePromise) => {
        resolve = resolvePromise;
      })
    );
    await renderHook();

    let pending!: Promise<void>;
    act(() => {
      pending = latest.setModelPrices(changedPrices);
    });
    expect(latest.modelPrices).toEqual(savedPrices);
    expect(mocks.saveLocalPrices).not.toHaveBeenCalled();

    await act(async () => {
      resolve({ prices: canonicalPrices });
      await pending;
    });
    expect(latest.modelPrices).toEqual(canonicalPrices);
    expect(mocks.clearLocalPrices).toHaveBeenCalledTimes(1);
    expect(mocks.saveModelPrices).toHaveBeenCalledWith(
      'http://manager.local',
      changedPrices,
      'test-key'
    );
  });

  it.each([
    Object.assign(new Error('archive structure locked'), {
      code: 'model_price_structure_locked_by_usage_archive',
      status: 409,
    }),
    Object.assign(new Error('unauthorized'), { code: 'invalid_admin_key', status: 401 }),
    new Error('Network Error'),
  ])(
    'propagates a Manager rejection without replacing state or writing local prices: %s',
    async (error) => {
      mocks.saveModelPrices.mockRejectedValueOnce(error);
      await renderHook();

      await act(async () => {
        await expect(latest.setModelPrices(changedPrices)).rejects.toBe(error);
      });

      expect(latest.modelPrices).toEqual(savedPrices);
      expect(mocks.saveLocalPrices).not.toHaveBeenCalled();
      expect(mocks.clearLocalPrices).not.toHaveBeenCalled();
    }
  );

  it('keeps browser-only price saving for a confirmed external CPA panel', async () => {
    Object.assign(mocks.availability, {
      panelHostMode: 'external_panel',
      managerServiceBase: '',
      managerServiceAvailable: false,
      modelPricesAvailable: false,
    });
    await renderHook();

    await act(async () => latest.setModelPrices(changedPrices));

    expect(mocks.saveModelPrices).not.toHaveBeenCalled();
    expect(mocks.saveLocalPrices).toHaveBeenCalledWith(changedPrices);
    expect(latest.modelPrices).toEqual(changedPrices);
  });

  it.each([
    { checking: true, panelHostConfirmed: false, panelHostMode: 'external_panel' },
    { checking: false, panelHostConfirmed: false, panelHostMode: 'external_panel' },
    { checking: false, panelHostConfirmed: true, panelHostMode: 'manager_embedded' },
  ] as const)(
    'does not mistake an unready or unreachable Manager for local-only mode: %j',
    async (availability) => {
      Object.assign(mocks.availability, availability, {
        managerServiceBase: '',
        managerServiceAvailable: false,
        modelPricesAvailable: false,
      });
      await renderHook();

      await act(async () => {
        await expect(latest.setModelPrices(changedPrices)).rejects.toThrow(
          'model_price_api_unavailable'
        );
      });

      expect(mocks.saveModelPrices).not.toHaveBeenCalled();
      expect(mocks.saveLocalPrices).not.toHaveBeenCalled();
      expect(latest.modelPrices).toEqual({});
    }
  );
});
