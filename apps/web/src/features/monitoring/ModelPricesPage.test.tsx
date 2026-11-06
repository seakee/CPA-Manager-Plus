import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Input } from '@/components/ui/Input';
import { calculateCost, type ModelPrice } from '@/utils/usage';
import { ModelPricesPage } from './ModelPricesPage';

const mocks = vi.hoisted(() => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  return {
    prices: {} as Record<string, ModelPrice>,
    save: vi.fn(async (_prices: Record<string, ModelPrice>) => undefined),
    notify: vi.fn(),
    availability: { modelPricesAvailable: false } as {
      modelPricesAvailable: boolean;
      managerServiceBase?: string;
    },
    usageSummary: vi.fn(),
    sync: vi.fn(),
    usage: {
      loading: false,
      modelPricesLoaded: true,
      modelPricesLoading: false,
      modelPricesScopeKey: 'scope-a',
    },
  };
});

vi.mock('react-i18next', async (importOriginal) => ({
  ...(await importOriginal<typeof import('react-i18next')>()),
  useTranslation: () => ({ t: (key: string) => key }),
}));
vi.mock('react-router-dom', () => ({ Link: 'a' }));
vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: { managementKey: string }) => unknown) =>
    selector({ managementKey: '' }),
  useNotificationStore: () => ({ showNotification: mocks.notify }),
}));
vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => mocks.availability,
}));
vi.mock('@/services/api/usageService', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/api/usageService')>();
  return {
    ...actual,
    usageServiceApi: {
      ...actual.usageServiceApi,
      getModelPriceUsageSummary: mocks.usageSummary,
    },
  };
});
vi.mock('@/services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/api')>();
  return {
    ...actual,
    usageServiceApi: {
      ...actual.usageServiceApi,
      getModelPriceUsageSummary: mocks.usageSummary,
    },
  };
});
vi.mock('@/features/monitoring/hooks/useUsageData', () => ({
  useUsageData: () => ({
    loading: mocks.usage.loading,
    modelPrices: mocks.prices,
    modelPricesLoaded: mocks.usage.modelPricesLoaded,
    modelPricesLoading: mocks.usage.modelPricesLoading,
    modelPricesScopeKey: mocks.usage.modelPricesScopeKey,
    reloadModelPrices: vi.fn(),
    setModelPrices: mocks.save,
    syncModelPrices: mocks.sync,
    usageServiceAvailable: true,
  }),
}));

// Synthetic published-price shape; this exercises the real edit/save consumer,
// not historical database migration or upgrade qualification.
const tieredPrice = (): ModelPrice => ({
  prompt: 2,
  completion: 4,
  cache: 1,
  source: 'sync',
  contextTiers: [
    {
      thresholdTokens: 200_000,
      prompt: 8,
      completion: 0,
      cache: 0,
      promptConfigured: true,
      completionConfigured: false,
      cacheConfigured: false,
      cacheRead: 0,
      cacheReadConfigured: true,
    },
  ],
  serviceTiers: [
    {
      mode: 'fast',
      serviceTier: 'priority',
      prompt: 0,
      completion: 7,
      cache: 0,
      promptConfigured: true,
      completionConfigured: true,
      cacheConfigured: false,
    },
  ],
});

let renderer: ReactTestRenderer;

const editFirstModel = async () => {
  await act(async () => {
    renderer = create(<ModelPricesPage />);
  });
  await act(async () => {
    renderer.root.findAllByProps({ 'aria-label': 'common.edit' })[0].props.onClick();
  });
};

const changeField = async (label: string, value: string) => {
  await act(async () => {
    renderer.root
      .findAllByType(Input)
      .find((input) => input.props.label === label)!
      .props.onChange({ target: { value } });
  });
};

const save = async () => {
  await act(async () => {
    renderer.root
      .findAllByType('button')
      .find((button) =>
        button.findAllByType('span').some((span) => span.children.includes('common.save'))
      )!
      .props.onClick();
  });
  return mocks.save.mock.calls[mocks.save.mock.calls.length - 1][0];
};

beforeEach(() => {
  vi.clearAllMocks();
  mocks.prices = { 'tiered-model': tieredPrice() };
  mocks.availability = { modelPricesAvailable: false };
  mocks.usage = {
    loading: false,
    modelPricesLoaded: true,
    modelPricesLoading: false,
    modelPricesScopeKey: 'scope-a',
  };
  mocks.usageSummary.mockResolvedValue({ summary: {} });
});
afterEach(async () => {
  await act(async () => {
    renderer?.unmount();
  });
});

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((yes) => {
    resolve = yes;
  });
  return { promise, resolve };
};

describe('ModelPricesPage base price edits', () => {
  it('saves existing rules, inherited rates and explicit zero through the real Save action', async () => {
    const original = structuredClone(mocks.prices);
    await editFirstModel();
    expect(
      renderer.root
        .findAllByType('div')
        .some((node) => node.children.includes('model_prices.manual_preserves_pricing_rules'))
    ).toBe(true);
    await changeField('usage_stats.model_price_prompt ($/1M)', '3');
    await changeField('usage_stats.model_price_completion ($/1M)', '5');
    const saved = await save();
    expect(saved['tiered-model']).toMatchObject({ prompt: 3, completion: 5, source: 'manual' });
    expect(saved['tiered-model'].contextTiers).toEqual(original['tiered-model'].contextTiers);
    expect(saved['tiered-model'].serviceTiers).toEqual(original['tiered-model'].serviceTiers);
    expect(mocks.prices).toEqual(original);
    expect(saved['tiered-model'].contextTiers![0]).not.toBe(
      mocks.prices['tiered-model'].contextTiers![0]
    );

    // Context completion inherits the edited base rate; explicit free cache stays free.
    expect(
      calculateCost(
        {
          __modelName: 'tiered-model',
          tokens: { input_tokens: 300_000, output_tokens: 100_000, cache_read_tokens: 100_000 },
        },
        saved
      )
    ).toBeCloseTo(2.1);
    // Service-tier explicit zero prompt does not inherit the edited nonzero base.
    expect(
      calculateCost(
        {
          __modelName: 'tiered-model',
          service_tier: 'priority',
          tokens: { input_tokens: 100_000, output_tokens: 100_000 },
        },
        saved
      )
    ).toBeCloseTo(0.7);
  });

  it('uses the target model rules when the model name is changed', async () => {
    mocks.prices['z-target'] = {
      ...tieredPrice(),
      contextTiers: [{ ...tieredPrice().contextTiers![0], thresholdTokens: 500_000 }],
      serviceTiers: [{ ...tieredPrice().serviceTiers![0], completion: 12 }],
    };
    await editFirstModel();
    await changeField('usage_stats.model_name', ' z-target ');
    const saved = await save();
    expect(saved['z-target'].contextTiers).toEqual(mocks.prices['z-target'].contextTiers);
    expect(saved['z-target'].serviceTiers).toEqual(mocks.prices['z-target'].serviceTiers);
    expect(saved['tiered-model']).toEqual(mocks.prices['tiered-model']);
  });

  it('does not copy the original model rules into a new model', async () => {
    await editFirstModel();
    await changeField('usage_stats.model_name', 'new-model');
    const saved = await save();
    expect(saved['new-model'].contextTiers).toEqual([]);
    expect(saved['new-model'].serviceTiers).toEqual([]);
    expect(saved['tiered-model']).toEqual(mocks.prices['tiered-model']);
  });

  it('preserves current rules when prices refresh while the editor is open', async () => {
    await editFirstModel();
    mocks.prices = {
      'tiered-model': {
        ...tieredPrice(),
        serviceTiers: [],
        contextTiers: [{ ...tieredPrice().contextTiers![0], thresholdTokens: 400_000 }],
      },
    };
    await act(async () => {
      renderer.update(<ModelPricesPage />);
    });
    const saved = await save();
    expect(saved['tiered-model'].contextTiers).toEqual(mocks.prices['tiered-model'].contextTiers);
    expect(saved['tiered-model'].serviceTiers).toEqual([]);
  });
});

describe('ModelPricesPage usage summary revalidation', () => {
  it('keeps the existing model price table mounted while the usage summary loads', async () => {
    mocks.availability = {
      modelPricesAvailable: true,
      managerServiceBase: 'http://manager.local',
    };
    mocks.usageSummary.mockReturnValue(new Promise(() => {}));
    await act(async () => {
      renderer = create(<ModelPricesPage />);
    });

    expect(renderer.root.findAllByType('table')).toHaveLength(1);
    expect(
      renderer.root
        .findAllByType('div')
        .some((node) => node.children.includes('common.loading'))
    ).toBe(false);
  });
});

describe('ModelPricesPage snapshot gating', () => {
  const pageText = () => JSON.stringify(renderer.toJSON());

  it('shows the first-load state only while no price snapshot exists', async () => {
    mocks.prices = {};
    mocks.usage = {
      loading: false,
      modelPricesLoaded: false,
      modelPricesLoading: true,
      modelPricesScopeKey: 'scope-a',
    };
    await act(async () => {
      renderer = create(<ModelPricesPage />);
    });
    expect(pageText()).toContain('common.loading');

    mocks.usage = {
      loading: false,
      modelPricesLoaded: true,
      modelPricesLoading: false,
      modelPricesScopeKey: 'scope-a',
    };
    await act(async () => {
      renderer.update(<ModelPricesPage />);
    });
    expect(pageText()).toContain('model_prices.empty');
    expect(pageText()).not.toContain('common.loading');
  });

  it('never renders a blank page when both first requests settle without a snapshot', async () => {
    mocks.prices = {};
    mocks.usageSummary.mockRejectedValue(new Error('summary offline'));
    mocks.usage = {
      loading: false,
      modelPricesLoaded: false,
      modelPricesLoading: false,
      modelPricesScopeKey: 'scope-a',
    };
    await act(async () => {
      renderer = create(<ModelPricesPage />);
      await Promise.resolve();
    });

    expect(pageText()).not.toBe('null');
    expect(pageText()).not.toContain('common.loading');
    expect(pageText()).toContain('model_prices.empty');
  });

  it('keeps the known empty state while the usage summary is still loading', async () => {
    mocks.availability = {
      modelPricesAvailable: true,
      managerServiceBase: 'http://manager.local',
    };
    mocks.prices = {};
    mocks.usageSummary.mockReturnValue(new Promise(() => {}));
    await act(async () => {
      renderer = create(<ModelPricesPage />);
    });

    expect(pageText()).toContain('model_prices.empty');
    expect(pageText()).not.toContain('common.loading');
  });

  it('keeps the known empty state when a search filters every row out during a refresh', async () => {
    mocks.availability = {
      modelPricesAvailable: true,
      managerServiceBase: 'http://manager.local',
    };
    mocks.usageSummary.mockReturnValue(new Promise(() => {}));
    await act(async () => {
      renderer = create(<ModelPricesPage />);
    });

    const searchInput = renderer.root
      .findAllByType(Input)
      .find((input) => input.props.placeholder === 'model_prices.search_placeholder');
    await act(async () => {
      searchInput!.props.onChange({ target: { value: 'no-such-model' } });
    });

    expect(pageText()).toContain('model_prices.empty');
    expect(pageText()).not.toContain('common.loading');
  });

  it('discards a draft that belonged to the previous connection', async () => {
    await editFirstModel();
    const hasEditor = () =>
      renderer.root
        .findAllByType(Input)
        .some((input) => input.props.label === 'usage_stats.model_name');
    expect(hasEditor()).toBe(true);

    mocks.usage = {
      loading: false,
      modelPricesLoaded: true,
      modelPricesLoading: false,
      modelPricesScopeKey: 'scope-b',
    };
    mocks.prices = {};
    await act(async () => {
      renderer.update(<ModelPricesPage />);
    });

    expect(hasEditor()).toBe(false);
    expect(pageText()).not.toContain('tiered-model');
  });
});

describe('ModelPricesPage sync scope isolation', () => {
  const clickByLabel = async (label: string) => {
    const target = renderer.root
      .findAllByType('button')
      .find((button) => button.findAllByType('span').some((span) => span.children.includes(label)));
    expect(target).toBeTruthy();
    await act(async () => {
      target!.props.onClick();
    });
  };

  it('does not surface candidates from a sync that belonged to the previous connection', async () => {
    mocks.availability = {
      modelPricesAvailable: true,
      managerServiceBase: 'http://manager.local',
    };
    mocks.prices = {};
    mocks.usageSummary.mockResolvedValue({
      sampled_events: 1,
      total_events: 1,
      truncated: false,
      models: [{ model: 'tiered-model', calls: 1, requested_calls: 1, resolved_calls: 1 }],
    });
    const pending = deferred<{
      prices: Record<string, ModelPrice>;
      imported: string[];
      candidates: {
        model: string;
        candidates: {
          sourceModelId: string;
          score: number;
          reason: string;
          price: ModelPrice;
        }[];
      }[];
      unmatched: string[];
      preserved: string[];
    }>();
    mocks.sync.mockReturnValueOnce(pending.promise);
    await act(async () => {
      renderer = create(<ModelPricesPage />);
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    await clickByLabel('usage_stats.model_price_sync');
    expect(mocks.sync).toHaveBeenCalledTimes(1);

    // The manager connection changed while the sync was still running.
    mocks.usage = {
      loading: false,
      modelPricesLoaded: true,
      modelPricesLoading: false,
      modelPricesScopeKey: 'scope-b',
    };
    await act(async () => {
      renderer.update(<ModelPricesPage />);
    });

    await act(async () => {
      pending.resolve({
        prices: {},
        imported: [],
        candidates: [
          {
            model: 'tiered-model',
            candidates: [
              {
                sourceModelId: 'gateway-a-model',
                score: 0.9,
                reason: 'similar-name',
                price: { prompt: 5, completion: 6, cache: 0 },
              },
            ],
          },
        ],
        unmatched: [],
        preserved: [],
      });
      await Promise.resolve();
    });

    expect(
      renderer.root.findAllByProps({ 'aria-label': 'model_prices.candidate_select' })
    ).toHaveLength(0);
    expect(
      mocks.notify.mock.calls.some(
        (call) => typeof call[0] === 'string' && call[0].startsWith('model_prices.sync_success')
      )
    ).toBe(false);
  });
});

describe('ModelPricesPage price action scope isolation', () => {
  const candidateResponse = {
    prices: {},
    imported: 0,
    skipped: 0,
    candidates: [
      {
        model: 'tiered-model',
        candidates: [
          {
            sourceModelId: 'gateway-a-model',
            score: 0.9,
            reason: 'similar-name',
            price: { prompt: 5, completion: 6, cache: 0 },
          },
        ],
      },
    ],
    unmatched: [],
    preserved: [],
  };

  const mountAndSettle = async () => {
    await act(async () => {
      renderer = create(<ModelPricesPage />);
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  };

  const clickBySpanLabel = async (label: string) => {
    const target = renderer.root
      .findAllByType('button')
      .find((button) => button.findAllByType('span').some((span) => span.children.includes(label)));
    expect(target).toBeTruthy();
    await act(async () => {
      target!.props.onClick();
    });
  };

  const candidateSelects = () =>
    renderer.root.findAllByProps({ 'aria-label': 'model_prices.candidate_select' });
  const hasDraftEditor = () =>
    renderer.root
      .findAllByType(Input)
      .some((input) => input.props.label === 'usage_stats.model_name');
  const notifiedWith = (key: string) => mocks.notify.mock.calls.some((call) => call[0] === key);
  const switchToScopeB = async () => {
    mocks.usage = {
      loading: false,
      modelPricesLoaded: true,
      modelPricesLoading: false,
      modelPricesScopeKey: 'scope-b',
    };
    await act(async () => {
      renderer.update(<ModelPricesPage />);
    });
  };

  it('does not apply a candidate confirm that finishes after the connection changed', async () => {
    mocks.availability = {
      modelPricesAvailable: true,
      managerServiceBase: 'http://manager.local',
    };
    mocks.prices = {};
    mocks.usageSummary.mockResolvedValue({
      sampled_events: 1,
      total_events: 1,
      truncated: false,
      models: [{ model: 'tiered-model', calls: 1, requested_calls: 1, resolved_calls: 1 }],
    });
    mocks.sync.mockResolvedValue(candidateResponse);
    await mountAndSettle();
    await clickBySpanLabel('usage_stats.model_price_sync');
    expect(candidateSelects()).toHaveLength(1);

    const pendingSave = deferred<undefined>();
    mocks.save.mockReturnValueOnce(pendingSave.promise);
    await clickBySpanLabel('model_prices.confirm_candidate');

    // The connection changes while the confirm is still saving, and the new
    // connection builds its own candidate set for the same model.
    await switchToScopeB();
    await clickBySpanLabel('usage_stats.model_price_sync');
    expect(candidateSelects()).toHaveLength(1);
    mocks.notify.mockClear();

    await act(async () => {
      pendingSave.resolve(undefined);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(candidateSelects()).toHaveLength(1);
    expect(notifiedWith('model_prices.candidate_confirmed')).toBe(false);
  });

  it('does not close the current draft when a save finishes after the connection changed', async () => {
    mocks.prices = { 'tiered-model': tieredPrice() };
    await mountAndSettle();
    await act(async () => {
      renderer.root.findAllByProps({ 'aria-label': 'common.edit' })[0].props.onClick();
    });
    const pendingSave = deferred<undefined>();
    mocks.save.mockReturnValueOnce(pendingSave.promise);
    await clickBySpanLabel('common.save');

    await switchToScopeB();
    await act(async () => {
      renderer.root.findAllByProps({ 'aria-label': 'common.edit' })[0].props.onClick();
    });
    expect(hasDraftEditor()).toBe(true);
    mocks.notify.mockClear();

    await act(async () => {
      pendingSave.resolve(undefined);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(hasDraftEditor()).toBe(true);
    expect(notifiedWith('usage_stats.model_price_saved')).toBe(false);
  });

  it('does not clear the current draft when a delete finishes after the connection changed', async () => {
    mocks.prices = { 'tiered-model': tieredPrice() };
    await mountAndSettle();
    // The delete handler only clears a draft of the same model, so both
    // connections must have that model's editor open.
    await act(async () => {
      renderer.root.findAllByProps({ 'aria-label': 'common.edit' })[0].props.onClick();
    });
    expect(hasDraftEditor()).toBe(true);
    const pendingSave = deferred<undefined>();
    mocks.save.mockReturnValueOnce(pendingSave.promise);
    await act(async () => {
      renderer.root.findAllByProps({ 'aria-label': 'common.delete' })[0].props.onClick();
    });

    await switchToScopeB();
    await act(async () => {
      renderer.root.findAllByProps({ 'aria-label': 'common.edit' })[0].props.onClick();
    });
    expect(hasDraftEditor()).toBe(true);

    await act(async () => {
      pendingSave.resolve(undefined);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(hasDraftEditor()).toBe(true);
  });
});
