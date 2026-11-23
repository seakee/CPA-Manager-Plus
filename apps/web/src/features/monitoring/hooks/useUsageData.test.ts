import { createElement, useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import type { UsageServiceApiError } from '@/services/api/usageService';
import type { ModelPrice } from '@/utils/usage';
import { useUsageData, type UseUsageDataReturn } from './useUsageData';

const mocks = vi.hoisted(() => ({
  auth: { apiBase: 'https://gateway-a.test', managementKey: 'key-a' },
  availability: {} as PanelFeatureAvailability,
  getModelPrices: vi.fn(),
  saveModelPrices: vi.fn(),
  syncModelPrices: vi.fn(),
  getApiKeyAliases: vi.fn(),
  loadLocalPrices: vi.fn(),
  saveLocalPrices: vi.fn(),
  clearLocalPrices: vi.fn(),
  loadScopedPrices: vi.fn(),
  saveScopedPrices: vi.fn(),
  clearScopedPrices: vi.fn(),
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => mocks.availability,
}));
vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: typeof mocks.auth) => unknown) => selector(mocks.auth),
}));
vi.mock('@/stores/useNotificationStore', () => ({
  useNotificationStore: { getState: () => ({ showNotification: vi.fn() }) },
}));
vi.mock('@/i18n', () => ({ default: { t: (key: string) => key } }));
vi.mock('@/services/api/usageService', () => ({
  usageServiceApi: {
    getModelPrices: mocks.getModelPrices,
    saveModelPrices: mocks.saveModelPrices,
    syncModelPrices: mocks.syncModelPrices,
    getApiKeyAliases: mocks.getApiKeyAliases,
    getUsage: vi.fn(),
  },
}));
vi.mock('@/utils/usage', () => ({
  loadModelPrices: mocks.loadLocalPrices,
  saveModelPrices: mocks.saveLocalPrices,
  clearModelPrices: mocks.clearLocalPrices,
  loadModelPriceFallback: mocks.loadScopedPrices,
  saveModelPriceFallback: mocks.saveScopedPrices,
  clearModelPriceFallback: mocks.clearScopedPrices,
}));
vi.mock('@/services/usageImportSession', () => ({
  cancelUsageImportFile: vi.fn(),
  uploadUsageImportFile: vi.fn(),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const availabilityFor = (
  managerServiceBase: string,
  modelPricesAvailable = true
): PanelFeatureAvailability => ({
  checking: false,
  panelHostConfirmed: true,
  panelHostMode: 'manager_embedded',
  panelBase: managerServiceBase,
  managerServiceBase,
  managerServiceAvailable: modelPricesAvailable,
  requestMonitoringAvailable: modelPricesAvailable,
  modelPricesAvailable,
  serverCodexInspectionAvailable: modelPricesAvailable,
  dockerSetupAvailable: modelPricesAvailable,
  externalManagerConfigAvailable: false,
  reason: '',
});

const price = (prompt: number): ModelPrice => ({ prompt, completion: prompt * 2, cache: 0 });

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
};

describe('useUsageData model price save error handling', () => {
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
      renderer = create(createElement(Harness));
      await Promise.resolve();
    });
  };

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.auth = { apiBase: 'https://gateway-a.test', managementKey: 'key-a' };
    mocks.availability = availabilityFor('http://manager-a.local');
    mocks.loadLocalPrices.mockReturnValue({});
    mocks.loadScopedPrices.mockReturnValue({});
    mocks.getApiKeyAliases.mockResolvedValue({ items: [] });
  });

  afterEach(() => {
    act(() => renderer?.unmount());
    renderer = undefined;
  });

  it('rolls back optimistic state and rethrows without writing localStorage on archive lock rejection', async () => {
    const initialPrices = { 'model-a': price(1) };
    mocks.getModelPrices.mockResolvedValue({ prices: initialPrices });
    await mount();
    expect(latest.modelPrices).toEqual(initialPrices);

    const lockedError = new Error(
      'Model price structure cannot change after archived raw usage has been deleted.'
    ) as UsageServiceApiError;
    lockedError.name = 'UsageServiceApiError';
    lockedError.status = 409;
    lockedError.code = 'model_price_structure_locked_by_usage_archive';
    mocks.saveModelPrices.mockRejectedValueOnce(lockedError);

    const editedPrices = { 'model-a': price(1), 'model-b': price(2) };
    await act(async () => {
      await expect(latest.setModelPrices(editedPrices)).rejects.toBe(lockedError);
    });

    expect(latest.modelPrices).toEqual(initialPrices);
    expect(latest.modelPricesLoaded).toBe(true);
    expect(latest.modelPricesLoading).toBe(false);
    expect(mocks.saveScopedPrices).not.toHaveBeenCalled();
    expect(mocks.saveLocalPrices).not.toHaveBeenCalled();
  });

  it('rolls back and rethrows for HTTP 4xx errors without code and non-network server codes without status', async () => {
    const initialPrices = { 'model-a': price(1) };
    mocks.getModelPrices.mockResolvedValue({ prices: initialPrices });
    await mount();

    const http400 = new Error('Bad request') as UsageServiceApiError;
    http400.name = 'UsageServiceApiError';
    http400.status = 400;
    mocks.saveModelPrices.mockRejectedValueOnce(http400);

    await act(async () => {
      await expect(latest.setModelPrices({ 'model-a': price(5) })).rejects.toBe(http400);
    });
    expect(latest.modelPrices).toEqual(initialPrices);
    expect(mocks.saveScopedPrices).not.toHaveBeenCalled();

    const codeOnly = new Error('Locked') as UsageServiceApiError;
    codeOnly.code = 'model_price_structure_locked_by_usage_archive';
    mocks.saveModelPrices.mockRejectedValueOnce(codeOnly);

    await act(async () => {
      await expect(latest.setModelPrices({})).rejects.toBe(codeOnly);
    });
    expect(latest.modelPrices).toEqual(initialPrices);
    expect(mocks.saveScopedPrices).not.toHaveBeenCalled();
  });

  it('rolls back and rethrows network errors when manager service is configured, while persisting standalone fallback locally across remounts', async () => {
    const initialPrices = { 'model-a': price(1) };
    mocks.getModelPrices.mockResolvedValue({ prices: initialPrices });
    await mount();

    const networkError = new Error('Network Error') as UsageServiceApiError;
    networkError.name = 'UsageServiceApiError';
    networkError.code = 'ERR_NETWORK';
    mocks.saveModelPrices.mockRejectedValueOnce(networkError);

    const unsavedPrices = { 'offline-model': price(4) };
    await act(async () => {
      await expect(latest.setModelPrices(unsavedPrices)).rejects.toBe(networkError);
    });
    expect(latest.modelPrices).toEqual(initialPrices);
    expect(mocks.saveScopedPrices).not.toHaveBeenCalled();

    mocks.saveScopedPrices.mockClear();
    mocks.availability = availabilityFor('', false);
    await act(async () => {
      renderer!.update(createElement(Harness));
      await Promise.resolve();
    });

    const unavailablePrices = { 'local-only-model': price(7) };
    await act(async () => {
      await latest.setModelPrices(unavailablePrices);
    });
    expect(latest.modelPrices).toEqual(unavailablePrices);
    expect(mocks.saveScopedPrices).toHaveBeenCalledWith(
      expect.stringContaining('gateway-a.test'),
      unavailablePrices
    );

    // Reloading and remounting standalone mode keep persisted scoped fallback instead of wiping it.
    mocks.clearScopedPrices.mockClear();
    mocks.loadScopedPrices.mockReturnValue(unavailablePrices);
    await act(async () => {
      await latest.reloadModelPrices();
    });
    expect(latest.modelPrices).toEqual(unavailablePrices);
    expect(latest.modelPricesLoaded).toBe(true);
    expect(mocks.clearScopedPrices).not.toHaveBeenCalled();

    act(() => renderer?.unmount());
    renderer = undefined;
    await mount();
    expect(latest.modelPrices).toEqual(unavailablePrices);
    expect(latest.modelPricesLoaded).toBe(true);
    expect(mocks.clearScopedPrices).not.toHaveBeenCalled();
  });

  it('keeps real server baseline after ERR_NETWORK followed by 409 rejection', async () => {
    const initialPrices = { 'model-a': price(1) };
    mocks.getModelPrices.mockResolvedValue({ prices: initialPrices });
    await mount();

    const networkError = new Error('Network Error') as UsageServiceApiError;
    networkError.name = 'UsageServiceApiError';
    networkError.code = 'ERR_NETWORK';
    mocks.saveModelPrices.mockRejectedValueOnce(networkError);

    await act(async () => {
      await expect(latest.setModelPrices({ 'unsaved-model': price(4) })).rejects.toBe(networkError);
    });
    expect(latest.modelPrices).toEqual(initialPrices);
    expect(mocks.saveScopedPrices).not.toHaveBeenCalled();

    const conflict409 = new Error('Archive locked') as UsageServiceApiError;
    conflict409.name = 'UsageServiceApiError';
    conflict409.status = 409;
    conflict409.code = 'model_price_structure_locked_by_usage_archive';
    mocks.saveModelPrices.mockRejectedValueOnce(conflict409);

    await act(async () => {
      await expect(latest.setModelPrices({ 'another-unsaved': price(8) })).rejects.toBe(conflict409);
    });
    expect(latest.modelPrices).toEqual(initialPrices);
    expect(mocks.saveScopedPrices).not.toHaveBeenCalled();
  });
  it('does not overwrite a newer scope state when a previous-scope save rejects with 409', async () => {
    const initialPrices = { 'model-a': price(1) };
    mocks.getModelPrices.mockResolvedValueOnce({ prices: initialPrices });
    await mount();
    const saveFromA = latest.setModelPrices;

    const pendingSave = deferred<never>();
    mocks.saveModelPrices.mockReturnValueOnce(pendingSave.promise);
    let savePromise!: Promise<void>;
    act(() => {
      savePromise = saveFromA({ 'model-a': price(9) });
    });

    const bPrices = { 'model-b': price(3) };
    mocks.auth = { apiBase: 'https://gateway-b.test', managementKey: 'key-b' };
    mocks.availability = availabilityFor('http://manager-b.local');
    mocks.getModelPrices.mockResolvedValueOnce({ prices: bPrices });
    await act(async () => {
      renderer!.update(createElement(Harness));
      await Promise.resolve();
    });
    expect(latest.modelPrices).toEqual(bPrices);

    const lockedError = new Error('Locked') as UsageServiceApiError;
    lockedError.name = 'UsageServiceApiError';
    lockedError.status = 409;
    lockedError.code = 'model_price_structure_locked_by_usage_archive';
    await act(async () => {
      pendingSave.reject(lockedError);
      await expect(savePromise).rejects.toBe(lockedError);
    });
    expect(latest.modelPrices).toEqual(bPrices);
  });

  it('treats HTTP 500/502/503 without code and with non-whitelisted data.code as authoritative failures', async () => {
    const initialPrices = { 'model-a': price(1) };
    mocks.getModelPrices.mockResolvedValue({ prices: initialPrices });
    await mount();

    const makeAxiosLike = (
      status: number,
      code?: string,
      dataCode?: string,
      inResponse = false
    ) => {
      const err = new Error(`HTTP ${status}`) as UsageServiceApiError & {
        response?: { status: number; data?: { code?: string } };
      };
      err.name = 'UsageServiceApiError';
      err.status = status;
      if (code) err.code = code;
      if (dataCode) {
        if (inResponse) {
          err.response = { status, data: { code: dataCode } };
        } else {
          err.data = { code: dataCode };
        }
      }
      return err;
    };

    const cases = [
      makeAxiosLike(500),
      makeAxiosLike(502, 'ERR_BAD_RESPONSE'),
      makeAxiosLike(503, 'ERR_BAD_RESPONSE', 'upstream_maintenance', false),
      makeAxiosLike(500, 'ERR_BAD_RESPONSE', 'custom_unwhitelisted_code', true),
    ];

    for (const err of cases) {
      mocks.saveModelPrices.mockRejectedValueOnce(err);
      await act(async () => {
        await expect(latest.setModelPrices({ 'model-a': price(9) })).rejects.toBe(err);
      });
      expect(latest.modelPrices).toEqual(initialPrices);
      expect(mocks.saveScopedPrices).not.toHaveBeenCalled();
    }

    // Even without HTTP status, error.data.code takes precedence over Axios transport error.code.
    const codePriorityErr = new Error('Server rejection') as UsageServiceApiError;
    codePriorityErr.code = 'ERR_BAD_RESPONSE';
    codePriorityErr.data = { code: 'non_whitelisted_server_code' };
    mocks.saveModelPrices.mockRejectedValueOnce(codePriorityErr);
    await act(async () => {
      await expect(latest.setModelPrices({ 'model-a': price(8) })).rejects.toBe(codePriorityErr);
    });
    expect(latest.modelPrices).toEqual(initialPrices);
    expect(mocks.saveScopedPrices).not.toHaveBeenCalled();
  });

  it('serializes concurrent setModelPrices saves and rolls back second failure to confirmed P0 rather than uncommitted P1', async () => {
    const p0 = { 'model-a': price(1) };
    const p1 = { 'model-a': price(5) };
    const p2 = { 'model-a': price(9) };
    mocks.getModelPrices.mockResolvedValue({ prices: p0 });
    await mount();

    const firstPending = deferred<{ prices: Record<string, ModelPrice> }>();
    const secondPending = deferred<never>();
    mocks.saveModelPrices
      .mockReturnValueOnce(firstPending.promise)
      .mockReturnValueOnce(secondPending.promise);

    let firstPromise!: Promise<void>;
    let secondPromise!: Promise<void>;
    await act(async () => {
      firstPromise = latest.setModelPrices(p1);
      secondPromise = latest.setModelPrices(p2);
      await Promise.resolve();
    });
    // Second write waits in the per-scope promise queue until the first finishes.
    expect(mocks.saveModelPrices).toHaveBeenCalledTimes(1);
    expect(mocks.saveModelPrices.mock.calls[0][1]).toEqual(p1);
    expect(latest.modelPrices).toEqual(p1);

    const networkError = new Error('Network Error') as UsageServiceApiError;
    networkError.name = 'UsageServiceApiError';
    networkError.code = 'ERR_NETWORK';
    await act(async () => {
      firstPending.reject(networkError);
      await expect(firstPromise).rejects.toBe(networkError);
      await Promise.resolve();
    });

    expect(mocks.saveModelPrices).toHaveBeenCalledTimes(2);
    expect(mocks.saveModelPrices.mock.calls[1][1]).toEqual(p2);

    const conflict409 = new Error('Locked') as UsageServiceApiError;
    conflict409.name = 'UsageServiceApiError';
    conflict409.status = 409;
    conflict409.code = 'model_price_structure_locked_by_usage_archive';
    await act(async () => {
      secondPending.reject(conflict409);
      await expect(secondPromise).rejects.toBe(conflict409);
    });

    expect(latest.modelPrices).toEqual(p0);
    expect(mocks.saveScopedPrices).not.toHaveBeenCalled();
  });

  it('tracks an earlier resolved setModelPrices snapshot as the rollback baseline if a newer write fails later', async () => {
    const p0 = { 'model-a': price(1) };
    const p1 = { 'model-a': price(5) };
    const p2 = { 'model-a': price(9) };
    mocks.getModelPrices.mockResolvedValue({ prices: p0 });
    await mount();

    const firstPending = deferred<{ prices: Record<string, ModelPrice> }>();
    const secondPending = deferred<never>();
    mocks.saveModelPrices
      .mockReturnValueOnce(firstPending.promise)
      .mockReturnValueOnce(secondPending.promise);

    let firstPromise!: Promise<void>;
    let secondPromise!: Promise<void>;
    await act(async () => {
      firstPromise = latest.setModelPrices(p1);
      secondPromise = latest.setModelPrices(p2);
      await Promise.resolve();
    });
    expect(mocks.saveModelPrices).toHaveBeenCalledTimes(1);

    await act(async () => {
      firstPending.resolve({ prices: p1 });
      await firstPromise;
      await Promise.resolve();
    });
    expect(mocks.saveModelPrices).toHaveBeenCalledTimes(2);

    const server503 = new Error('Service Unavailable') as UsageServiceApiError;
    server503.name = 'UsageServiceApiError';
    server503.status = 503;
    await act(async () => {
      secondPending.reject(server503);
      await expect(secondPromise).rejects.toBe(server503);
    });

    expect(latest.modelPrices).toEqual(p1);
  });

  it('preserves call order across concurrent successful saves so final baseline is not overwritten by an earlier write', async () => {
    const p0 = { 'model-a': price(1) };
    const p1 = { 'model-a': price(5) };
    const p2 = { 'model-a': price(9) };
    mocks.getModelPrices.mockResolvedValue({ prices: p0 });
    await mount();

    const firstPending = deferred<{ prices: Record<string, ModelPrice> }>();
    const secondPending = deferred<{ prices: Record<string, ModelPrice> }>();
    mocks.saveModelPrices
      .mockReturnValueOnce(firstPending.promise)
      .mockReturnValueOnce(secondPending.promise);

    let firstPromise!: Promise<void>;
    let secondPromise!: Promise<void>;
    await act(async () => {
      firstPromise = latest.setModelPrices(p1);
      secondPromise = latest.setModelPrices(p2);
      await Promise.resolve();
    });
    expect(mocks.saveModelPrices).toHaveBeenCalledTimes(1);
    expect(mocks.saveModelPrices.mock.calls[0][1]).toEqual(p1);

    await act(async () => {
      firstPending.resolve({ prices: p1 });
      await firstPromise;
      await Promise.resolve();
    });
    expect(mocks.saveModelPrices).toHaveBeenCalledTimes(2);
    expect(mocks.saveModelPrices.mock.calls[1][1]).toEqual(p2);

    await act(async () => {
      secondPending.resolve({ prices: p2 });
      await secondPromise;
    });
    expect(latest.modelPrices).toEqual(p2);

    const err500 = new Error('Internal Server Error') as UsageServiceApiError;
    err500.status = 500;
    mocks.saveModelPrices.mockRejectedValueOnce(err500);
    await act(async () => {
      await expect(latest.setModelPrices({ 'model-a': price(99) })).rejects.toBe(err500);
    });
    expect(latest.modelPrices).toEqual(p2);
  });
});
