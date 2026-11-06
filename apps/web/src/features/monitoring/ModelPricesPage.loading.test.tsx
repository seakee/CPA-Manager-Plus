import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ModelPrice } from '@/utils/usage';
import { ModelPricesPage } from './ModelPricesPage';

const mocks = vi.hoisted(() => ({
  auth: { managementKey: 'key-a' },
  availability: {
    modelPricesAvailable: true,
    managerServiceBase: 'http://manager.local',
  },
  getModelPrices: vi.fn(),
  getModelPriceUsageSummary: vi.fn(),
  saveModelPrices: vi.fn(),
  syncModelPrices: vi.fn(),
  getApiKeyAliases: vi.fn(),
  notify: vi.fn(),
}));

vi.mock('react-i18next', async (importOriginal) => ({
  ...(await importOriginal<typeof import('react-i18next')>()),
  useTranslation: () => ({ t: (key: string) => key }),
}));
vi.mock('react-router-dom', () => ({ Link: 'a' }));
vi.mock('@/i18n', () => ({ default: { t: (key: string) => key } }));
vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => ({
    checking: false,
    panelHostConfirmed: true,
    panelHostMode: 'manager_embedded',
    panelBase: mocks.availability.managerServiceBase,
    managerServiceBase: mocks.availability.managerServiceBase,
    managerServiceAvailable: true,
    requestMonitoringAvailable: true,
    modelPricesAvailable: mocks.availability.modelPricesAvailable,
    serverCodexInspectionAvailable: true,
    dockerSetupAvailable: true,
    externalManagerConfigAvailable: false,
    reason: '',
  }),
}));
vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: { managementKey: string }) => unknown) => selector(mocks.auth),
  useNotificationStore: () => ({ showNotification: mocks.notify }),
}));
vi.mock('@/stores/useNotificationStore', () => ({
  useNotificationStore: { getState: () => ({ showNotification: mocks.notify }) },
}));
vi.mock('@/services/api/usageService', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/api/usageService')>();
  return {
    ...actual,
    usageServiceApi: {
      ...actual.usageServiceApi,
      getModelPrices: mocks.getModelPrices,
      getModelPriceUsageSummary: mocks.getModelPriceUsageSummary,
      saveModelPrices: mocks.saveModelPrices,
      syncModelPrices: mocks.syncModelPrices,
      getApiKeyAliases: mocks.getApiKeyAliases,
      getUsage: vi.fn(),
    },
  };
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

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

describe('ModelPricesPage first load', () => {
  let renderer: ReactTestRenderer | undefined;
  const pageText = () => JSON.stringify(renderer!.toJSON());

  beforeEach(() => {
    vi.clearAllMocks();
    mocks.auth = { managementKey: 'key-a' };
    mocks.availability = {
      modelPricesAvailable: true,
      managerServiceBase: 'http://manager.local',
    };
    mocks.getApiKeyAliases.mockResolvedValue({ items: [] });
    mocks.saveModelPrices.mockResolvedValue({ prices: {} });
  });

  afterEach(() => {
    act(() => renderer?.unmount());
    renderer = undefined;
  });

  it('keeps the first-load state when the usage summary settles before the price read', async () => {
    const prices = deferred<{ prices: Record<string, ModelPrice> }>();
    mocks.getModelPrices.mockReturnValue(prices.promise);
    mocks.getModelPriceUsageSummary.mockRejectedValue(new Error('summary offline'));

    await act(async () => {
      renderer = create(<ModelPricesPage />);
      await Promise.resolve();
      await Promise.resolve();
    });

    // The summary failed and the usage-events load is irrelevant: the price read
    // is still pending, so the page must keep showing the first-load state.
    expect(pageText()).toContain('common.loading');
    expect(renderer!.toJSON()).not.toBeNull();

    await act(async () => {
      prices.resolve({ prices: {} });
      await Promise.resolve();
    });
    expect(pageText()).toContain('model_prices.empty');
    expect(pageText()).not.toContain('common.loading');
  });

  it('falls back to an empty state instead of a blank page when both requests fail', async () => {
    mocks.getModelPrices.mockRejectedValue(new Error('prices offline'));
    mocks.getModelPriceUsageSummary.mockRejectedValue(new Error('summary offline'));

    await act(async () => {
      renderer = create(<ModelPricesPage />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(renderer!.toJSON()).not.toBeNull();
    expect(pageText()).not.toContain('common.loading');
    expect(pageText()).toContain('model_prices.empty');
  });

  it('publishes a price snapshot read for the current connection', async () => {
    mocks.getModelPrices.mockResolvedValue({ prices: { 'a-model': price(3) } });
    mocks.getModelPriceUsageSummary.mockResolvedValue({
      sampled_events: 0,
      total_events: 0,
      truncated: false,
      models: [],
    });

    await act(async () => {
      renderer = create(<ModelPricesPage />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(pageText()).toContain('a-model');
    expect(pageText()).not.toContain('common.loading');
  });
});
