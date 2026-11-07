import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import type { ModelPriceSyncResponse } from '@/services/api/usageService';
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
vi.mock('@/features/monitoring/services/usageImportSession', () => ({
  cancelUsageImportFile: vi.fn(),
  uploadUsageImportFile: vi.fn(),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const availabilityFor = (managerServiceBase: string): PanelFeatureAvailability => ({
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

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
};

const price = (prompt: number): ModelPrice => ({ prompt, completion: prompt * 2, cache: 0 });

describe('useUsageData model price scope', () => {
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
      await Promise.resolve();
    });
  };
  const rerender = async () => {
    await act(async () => {
      renderer!.update(<Harness />);
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

  it('drops a previous-connection price response that resolves after the new scope loaded', async () => {
    const oldRead = deferred<{ prices: Record<string, ModelPrice> }>();
    const newRead = deferred<{ prices: Record<string, ModelPrice> }>();
    mocks.getModelPrices.mockReturnValueOnce(oldRead.promise).mockReturnValueOnce(newRead.promise);

    await mount();

    mocks.auth = { apiBase: 'https://gateway-b.test', managementKey: 'key-b' };
    mocks.availability = availabilityFor('http://manager-b.local');
    await rerender();
    expect(latest.modelPrices).toEqual({});

    await act(async () => {
      newRead.resolve({ prices: { 'b-model': price(2) } });
      await Promise.resolve();
    });
    expect(Object.keys(latest.modelPrices)).toEqual(['b-model']);

    await act(async () => {
      oldRead.resolve({ prices: { 'a-model': price(9) } });
      await Promise.resolve();
    });
    expect(Object.keys(latest.modelPrices)).toEqual(['b-model']);
  });

  it('keeps the new scope prices when the previous-connection read fails', async () => {
    const oldRead = deferred<{ prices: Record<string, ModelPrice> }>();
    const newRead = deferred<{ prices: Record<string, ModelPrice> }>();
    mocks.getModelPrices.mockReturnValueOnce(oldRead.promise).mockReturnValueOnce(newRead.promise);

    await mount();
    mocks.auth = { apiBase: 'https://gateway-b.test', managementKey: 'key-b' };
    mocks.availability = availabilityFor('http://manager-b.local');
    await rerender();

    await act(async () => {
      newRead.resolve({ prices: { 'b-model': price(2) } });
      await Promise.resolve();
    });
    mocks.loadLocalPrices.mockReturnValue({ 'local-model': price(7) });

    await act(async () => {
      oldRead.reject(new Error('old scope offline'));
      await Promise.resolve();
    });
    expect(latest.modelPrices).toEqual({ 'b-model': price(2) });
    expect(latest.modelPricesLoaded).toBe(true);
  });

  it('treats a successful empty price result as a loaded snapshot', async () => {
    mocks.getModelPrices.mockResolvedValue({ prices: {} });
    // Nothing to migrate: this exercises the verified-empty branch, not the
    // save-backfill fallback.
    mocks.loadLocalPrices.mockReturnValue({});

    await mount();
    await act(async () => {
      await Promise.resolve();
    });

    expect(latest.modelPricesLoaded).toBe(true);
    expect(latest.modelPrices).toEqual({});
    expect(mocks.saveModelPrices).not.toHaveBeenCalled();
  });

  it('keeps the last successful snapshot when a same-scope re-read fails', async () => {
    mocks.getModelPrices.mockResolvedValueOnce({ prices: { 'a-model': price(3) } });
    await mount();
    await act(async () => {
      await Promise.resolve();
    });
    expect(latest.modelPrices).toEqual({ 'a-model': price(3) });

    mocks.loadLocalPrices.mockReturnValue({ 'local-model': price(5) });
    mocks.getModelPrices.mockRejectedValueOnce(new Error('offline'));
    await act(async () => {
      await latest.reloadModelPrices();
    });

    expect(latest.modelPrices).toEqual({ 'a-model': price(3) });
    expect(latest.modelPricesLoaded).toBe(true);
  });

  it('still migrates legacy unscoped prices into the current scope', async () => {
    mocks.loadLocalPrices.mockReturnValue({ 'legacy-model': price(6) });
    mocks.getModelPrices.mockResolvedValue({ prices: {} });
    mocks.saveModelPrices.mockResolvedValueOnce({ prices: { 'legacy-model': price(6) } });

    await mount();
    await act(async () => {
      await Promise.resolve();
    });

    expect(mocks.saveModelPrices).toHaveBeenCalledTimes(1);
    expect(mocks.saveModelPrices.mock.calls[0][1]).toEqual({ 'legacy-model': price(6) });
    expect(latest.modelPrices).toEqual({ 'legacy-model': price(6) });
    expect(mocks.clearLocalPrices).toHaveBeenCalled();
  });

  it('drops a scoped fallback when the server returns an authoritative empty result', async () => {
    mocks.loadScopedPrices.mockReturnValue({ 'local-edit': price(8) });
    mocks.getModelPrices.mockResolvedValue({ prices: {} });

    await mount();
    await act(async () => {
      await Promise.resolve();
    });

    expect(mocks.saveModelPrices).not.toHaveBeenCalled();
    expect(latest.modelPrices).toEqual({});
    expect(mocks.clearScopedPrices).toHaveBeenCalledWith(expect.stringContaining('gateway-a.test'));
  });

  it('uses scoped local edits only when the read itself fails', async () => {
    mocks.loadScopedPrices.mockReturnValue({ 'local-edit': price(8) });
    mocks.getModelPrices.mockRejectedValue(new Error('offline'));

    await mount();
    await act(async () => {
      await Promise.resolve();
    });

    expect(latest.modelPrices).toEqual({ 'local-edit': price(8) });
    expect(mocks.saveModelPrices).not.toHaveBeenCalled();
    expect(mocks.clearScopedPrices).not.toHaveBeenCalled();
  });

  it('keeps a failed save for its own scope instead of the shared legacy entry', async () => {
    // Behavioural storage double: the scoped entry and the legacy entry are
    // separate, exactly like localStorage.
    const scopedStore = new Map<string, Record<string, ModelPrice>>();
    let legacyStore: Record<string, ModelPrice> = {};
    mocks.saveScopedPrices.mockImplementation(
      (scope: string, prices: Record<string, ModelPrice>) => {
        scopedStore.set(scope, prices);
      }
    );
    mocks.loadScopedPrices.mockImplementation((scope: string) => scopedStore.get(scope) ?? {});
    mocks.saveLocalPrices.mockImplementation((prices: Record<string, ModelPrice>) => {
      legacyStore = prices;
    });
    mocks.loadLocalPrices.mockImplementation(() => legacyStore);

    mocks.getModelPrices.mockResolvedValue({ prices: {} });
    await mount();
    await act(async () => {
      await Promise.resolve();
    });
    expect(latest.modelPricesLoaded).toBe(true);

    mocks.saveModelPrices.mockRejectedValueOnce(new Error('save offline'));
    await act(async () => {
      await latest.setModelPrices({ 'a-model': price(9) });
    });
    expect(mocks.saveLocalPrices).not.toHaveBeenCalled();
    expect(legacyStore).toEqual({});
    expect([...scopedStore.values()][0]).toEqual({ 'a-model': price(9) });

    const apiSavesBefore = mocks.saveModelPrices.mock.calls.length;
    mocks.auth = { apiBase: 'https://gateway-b.test', managementKey: 'key-b' };
    mocks.availability = availabilityFor('http://manager-b.local');
    mocks.getModelPrices.mockResolvedValue({ prices: {} });
    await rerender();
    await act(async () => {
      await Promise.resolve();
    });

    // Connection B must neither migrate A's leftovers nor show them.
    expect(mocks.saveModelPrices.mock.calls.length).toBe(apiSavesBefore);
    expect(latest.modelPrices).toEqual({});
    expect(latest.modelPricesLoaded).toBe(true);
  });

  it('does not publish a read that resolves after a successful save in the same scope', async () => {
    const read = deferred<{ prices: Record<string, ModelPrice> }>();
    mocks.getModelPrices.mockReturnValueOnce(read.promise);
    await mount();

    mocks.saveModelPrices.mockResolvedValueOnce({ prices: { 'saved-model': price(4) } });
    await act(async () => {
      await latest.setModelPrices({ 'saved-model': price(4) });
    });
    expect(latest.modelPrices).toEqual({ 'saved-model': price(4) });

    await act(async () => {
      read.resolve({ prices: { 'old-model': price(1) } });
      await Promise.resolve();
    });

    expect(latest.modelPrices).toEqual({ 'saved-model': price(4) });
  });

  it('reports a superseded sync as stale instead of returning the previous scope result', async () => {
    const sync = deferred<ModelPriceSyncResponse>();
    mocks.getModelPrices.mockResolvedValue({ prices: {} });
    await mount();
    await act(async () => {
      await Promise.resolve();
    });
    mocks.syncModelPrices.mockReturnValueOnce(sync.promise);

    let pending!: Promise<ModelPriceSyncResponse | null>;
    act(() => {
      pending = latest.syncModelPrices(['a-model']);
    });

    mocks.auth = { apiBase: 'https://gateway-b.test', managementKey: 'key-b' };
    mocks.availability = availabilityFor('http://manager-b.local');
    await rerender();

    sync.resolve({
      prices: {},
      imported: 0,
      skipped: 0,
      candidates: [
        {
          model: 'a-model',
          candidates: [
            {
              sourceModelId: 'gateway-a-model',
              score: 0.9,
              reason: 'similar-name',
              price: price(3),
            },
          ],
        },
      ],
      unmatched: [],
      preserved: [],
    });
    await act(async () => {
      expect(await pending).toBeNull();
    });

    expect(latest.modelPrices).toEqual({});
  });

  it('ignores a stale scope save without disturbing the current scope', async () => {
    mocks.getModelPrices.mockResolvedValueOnce({ prices: { 'a-model': price(1) } });
    await mount();
    await act(async () => {
      await Promise.resolve();
    });
    const staleSetter = latest.setModelPrices;

    // Switch to B while B's price read is still in flight.
    const bRead = deferred<{ prices: Record<string, ModelPrice> }>();
    mocks.getModelPrices.mockReturnValueOnce(bRead.promise);
    mocks.auth = { apiBase: 'https://gateway-b.test', managementKey: 'key-b' };
    mocks.availability = availabilityFor('http://manager-b.local');
    await rerender();

    const apiSavesBefore = mocks.saveModelPrices.mock.calls.length;
    await act(async () => {
      await staleSetter({ 'stale-model': price(9) });
    });
    // The previous connection's captured setter must not write anything, nor
    // invalidate the read that is in flight for the current connection.
    expect(mocks.saveModelPrices.mock.calls.length).toBe(apiSavesBefore);

    await act(async () => {
      bRead.resolve({ prices: { 'b-model': price(2) } });
      await Promise.resolve();
    });
    expect(latest.modelPrices).toEqual({ 'b-model': price(2) });
  });

  it('ignores a stale scope sync without disturbing the current scope', async () => {
    mocks.getModelPrices.mockResolvedValueOnce({ prices: { 'a-model': price(1) } });
    await mount();
    await act(async () => {
      await Promise.resolve();
    });
    const staleSync = latest.syncModelPrices;

    const bRead = deferred<{ prices: Record<string, ModelPrice> }>();
    mocks.getModelPrices.mockReturnValueOnce(bRead.promise);
    mocks.auth = { apiBase: 'https://gateway-b.test', managementKey: 'key-b' };
    mocks.availability = availabilityFor('http://manager-b.local');
    await rerender();

    const syncCallsBefore = mocks.syncModelPrices.mock.calls.length;
    let result: ModelPriceSyncResponse | null = null;
    await act(async () => {
      result = await staleSync(['a-model']);
    });
    expect(result).toBeNull();
    expect(mocks.syncModelPrices.mock.calls.length).toBe(syncCallsBefore);

    await act(async () => {
      bRead.resolve({ prices: { 'b-model': price(2) } });
      await Promise.resolve();
    });
    expect(latest.modelPrices).toEqual({ 'b-model': price(2) });
  });

  it('reports the price read in flight for the current scope only', async () => {
    const readA = deferred<{ prices: Record<string, ModelPrice> }>();
    mocks.getModelPrices.mockReturnValueOnce(readA.promise);
    await mount();
    expect(latest.modelPricesLoading).toBe(true);

    const readB = deferred<{ prices: Record<string, ModelPrice> }>();
    mocks.getModelPrices.mockReturnValueOnce(readB.promise);
    mocks.auth = { apiBase: 'https://gateway-b.test', managementKey: 'key-b' };
    mocks.availability = availabilityFor('http://manager-b.local');
    await rerender();
    expect(latest.modelPricesLoading).toBe(true);

    await act(async () => {
      readA.resolve({ prices: { 'a-model': price(1) } });
      await Promise.resolve();
    });
    // The stale read must not clear the current scope's loading state.
    expect(latest.modelPricesLoading).toBe(true);

    await act(async () => {
      readB.resolve({ prices: {} });
      await Promise.resolve();
    });
    expect(latest.modelPricesLoading).toBe(false);
  });

  it('clears modelPricesLoading when a save supersedes an in-flight read', async () => {
    const read = deferred<{ prices: Record<string, ModelPrice> }>();
    mocks.getModelPrices.mockReturnValueOnce(read.promise);
    await mount();
    expect(latest.modelPricesLoading).toBe(true);

    mocks.saveModelPrices.mockRejectedValueOnce(new Error('save offline'));
    await act(async () => {
      await latest.setModelPrices({ 'saved-model': price(4) });
    });
    expect(latest.modelPricesLoading).toBe(false);

    await act(async () => {
      read.reject(new Error('read offline'));
      await Promise.resolve();
    });
    expect(latest.modelPricesLoading).toBe(false);
  });

  it('ignores a stale scope reloadModelPrices without sticking modelPricesLoading or dropping the current read', async () => {
    mocks.getModelPrices.mockResolvedValueOnce({ prices: { 'a-model': price(1) } });
    await mount();
    await act(async () => {
      await Promise.resolve();
    });
    const staleReload = latest.reloadModelPrices;

    const bRead = deferred<{ prices: Record<string, ModelPrice> }>();
    mocks.getModelPrices.mockReturnValueOnce(bRead.promise);
    mocks.auth = { apiBase: 'https://gateway-b.test', managementKey: 'key-b' };
    mocks.availability = availabilityFor('http://manager-b.local');
    await rerender();

    const readsBefore = mocks.getModelPrices.mock.calls.length;
    await act(async () => {
      await staleReload();
    });
    expect(mocks.getModelPrices.mock.calls.length).toBe(readsBefore);

    await act(async () => {
      bRead.resolve({ prices: { 'b-model': price(2) } });
      await Promise.resolve();
    });
    expect(latest.modelPricesLoading).toBe(false);
    expect(latest.modelPrices).toEqual({ 'b-model': price(2) });
  });
});
