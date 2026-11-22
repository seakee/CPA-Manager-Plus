import { act, create, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { PageControlBar } from '@/components/common/PageControlBar';
import { PaginationFooter } from '@/components/common/PaginationFooter';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import { triggerHeaderRefresh } from '@/hooks/useHeaderRefresh';
import type { UsageServiceApiError } from '@/services/api/usageService';
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
      requestMonitoringAvailable?: boolean;
      managerServiceBase?: string;
    },
    usageSummary: vi.fn(),
    sync: vi.fn(),
    reload: vi.fn(),
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
    reloadModelPrices: mocks.reload,
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

  it('shows the localized server rejection error instead of success when saving a draft fails with archive lock', async () => {
    const lockedError = new Error(
      'Model price structure cannot change after archived raw usage has been deleted.'
    ) as UsageServiceApiError;
    lockedError.name = 'UsageServiceApiError';
    lockedError.status = 409;
    lockedError.code = 'model_price_structure_locked_by_usage_archive';
    mocks.save.mockRejectedValueOnce(lockedError);

    await mountAndSettle();
    await act(async () => {
      renderer.root.findAllByProps({ 'aria-label': 'common.edit' })[0].props.onClick();
    });
    expect(hasDraftEditor()).toBe(true);
    await clickBySpanLabel('common.save');
    await act(async () => {
      await Promise.resolve();
    });

    expect(mocks.notify).toHaveBeenCalledWith(
      'usage_service_errors.model_price_structure_locked_by_usage_archive',
      'error'
    );
    expect(notifiedWith('usage_stats.model_price_saved')).toBe(false);
    expect(hasDraftEditor()).toBe(true);
  });

  it('shows the server rejection error and preserves candidates or drafts on confirm and delete failures', async () => {
    const lockedError = new Error(
      'Model price structure cannot change after archived raw usage has been deleted.'
    ) as UsageServiceApiError;
    lockedError.name = 'UsageServiceApiError';
    lockedError.status = 409;
    lockedError.code = 'model_price_structure_locked_by_usage_archive';

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

    mocks.save.mockRejectedValueOnce(lockedError);
    await clickBySpanLabel('model_prices.confirm_candidate');
    await act(async () => {
      await Promise.resolve();
    });
    expect(mocks.notify).toHaveBeenCalledWith(
      'usage_service_errors.model_price_structure_locked_by_usage_archive',
      'error'
    );
    expect(notifiedWith('model_prices.candidate_confirmed')).toBe(false);
    expect(candidateSelects()).toHaveLength(1);

    mocks.notify.mockClear();
    await act(async () => {
      renderer.unmount();
    });
    mocks.prices = { 'tiered-model': tieredPrice() };
    await mountAndSettle();
    await act(async () => {
      renderer.root.findAllByProps({ 'aria-label': 'common.edit' })[0].props.onClick();
    });
    expect(hasDraftEditor()).toBe(true);
    mocks.save.mockRejectedValueOnce(new Error('unexpected failure'));
    await act(async () => {
      renderer.root.findAllByProps({ 'aria-label': 'common.delete' })[0].props.onClick();
      await Promise.resolve();
    });
    expect(mocks.notify).toHaveBeenCalledWith('unexpected failure', 'error');
    expect(hasDraftEditor()).toBe(true);
  });

  it('ignores a rejected save from a superseded connection and falls back when error has no message', async () => {
    mocks.prices = { 'tiered-model': tieredPrice() };
    await mountAndSettle();
    await act(async () => {
      renderer.root.findAllByProps({ 'aria-label': 'common.edit' })[0].props.onClick();
    });
    let rejectSave!: (reason?: unknown) => void;
    const savePromise = new Promise<undefined>((_resolve, reject) => {
      rejectSave = reject;
    });
    mocks.save.mockReturnValueOnce(savePromise);
    await clickBySpanLabel('common.save');

    await switchToScopeB();
    mocks.notify.mockClear();

    await act(async () => {
      rejectSave(new Error('late locked error'));
      await Promise.resolve();
    });
    expect(mocks.notify).not.toHaveBeenCalled();

    await act(async () => {
      renderer.root.findAllByProps({ 'aria-label': 'common.edit' })[0].props.onClick();
    });
    mocks.save.mockRejectedValueOnce({});
    await clickBySpanLabel('common.save');
    await act(async () => {
      await Promise.resolve();
    });
    expect(mocks.notify).toHaveBeenCalledWith('usage_service_errors.request_failed', 'error');
  });

  it('guards saves with an in-flight lock so overlapping writes cannot race', async () => {
    mocks.availability = {
      modelPricesAvailable: true,
      managerServiceBase: 'http://manager.local',
    };
    mocks.prices = { 'tiered-model': tieredPrice() };
    await mountAndSettle();

    let resolveSave!: () => void;
    const pendingSave = new Promise<undefined>((resolve) => {
      resolveSave = () => resolve(undefined);
    });
    mocks.save.mockReturnValueOnce(pendingSave);

    await act(async () => {
      renderer.root.findAllByProps({ 'aria-label': 'common.edit' })[0].props.onClick();
    });
    const findSaveButton = () =>
      renderer.root.findAllByType('button').find((button) =>
        button.findAllByType('span').some((span) => span.children.includes('common.save'))
      )!;
    const findDeleteButton = () =>
      renderer.root.findAllByProps({ 'aria-label': 'common.delete' })[0];

    await act(async () => {
      findSaveButton().props.onClick();
    });
    expect(mocks.save).toHaveBeenCalledTimes(1);
    expect(findSaveButton().props.disabled).toBe(true);
    expect(findDeleteButton().props.disabled).toBe(true);

    // Overlapping clicks on save or delete while save is in flight are ignored.
    await act(async () => {
      findSaveButton().props.onClick();
      findDeleteButton().props.onClick();
    });
    expect(mocks.save).toHaveBeenCalledTimes(1);

    await act(async () => {
      resolveSave();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(hasDraftEditor()).toBe(false);
    expect(findDeleteButton().props.disabled).toBe(false);
    mocks.save.mockResolvedValueOnce(undefined);
    await act(async () => {
      findDeleteButton().props.onClick();
      await Promise.resolve();
    });
    expect(mocks.save).toHaveBeenCalledTimes(2);
  });

  it('keeps mutations disabled and shows loading while initial price baseline has not settled even if usage summary resolves', async () => {
    mocks.availability = {
      modelPricesAvailable: true,
      managerServiceBase: 'http://manager.local',
    };
    mocks.prices = {};
    mocks.usageSummary.mockResolvedValue({
      sampled_events: 1,
      total_events: 1,
      truncated: false,
      models: [{ model: 'gpt-5.5', request_count: 1, unpriced_count: 1 }],
    });
    mocks.usage = {
      loading: false,
      modelPricesLoaded: false,
      modelPricesLoading: true,
      modelPricesScopeKey: 'scope-a',
    };
    await act(async () => {
      renderer = create(<ModelPricesPage />);
      await Promise.resolve();
    });

    expect(JSON.stringify(renderer.toJSON())).toContain('common.loading');
    const hasSpanLabel = (btn: ReactTestInstance, label: string) =>
      btn.findAllByType('span').some((span) => span.children.includes(label));
    const toolbarButtons = renderer.root.findAllByType('button');
    const addButton = toolbarButtons.find((btn) => hasSpanLabel(btn, 'model_prices.add_manual'));
    const syncButton = toolbarButtons.find((btn) =>
      hasSpanLabel(btn, 'usage_stats.model_price_sync')
    );
    expect(addButton?.props.disabled).toBe(true);
    expect(syncButton?.props.disabled).toBe(true);

    // Failed price load (!modelPricesLoaded && !modelPricesLoading) renders load-failed alert & keeps row mutations disabled.
    mocks.usage = {
      loading: false,
      modelPricesLoaded: false,
      modelPricesLoading: false,
      modelPricesScopeKey: 'scope-a',
    };
    await act(async () => {
      renderer.update(<ModelPricesPage />);
    });
    expect(renderer.root.findByProps({ 'data-testid': 'model-prices-load-failed' })).toBeDefined();
    const rowAddBtn = renderer.root
      .findAllByType('button')
      .filter((btn) => btn.findAll((node) => node.children?.includes('model_prices.add_manual')).length > 0);
    expect(rowAddBtn.every((btn) => btn.props.disabled === true)).toBe(true);
  });
  it('disables Add Manual and Edit while saving or syncing is in flight', async () => {
    mocks.availability = {
      modelPricesAvailable: true,
      managerServiceBase: 'http://manager.local',
    };
    mocks.prices = { 'tiered-model': tieredPrice() };
    mocks.usageSummary.mockResolvedValue({
      sampled_events: 1,
      total_events: 1,
      truncated: false,
      models: [{ model: 'unpriced-model', calls: 1, requested_calls: 1, resolved_calls: 1 }],
    });
    await mountAndSettle();

    const toolbarAddBtn = () =>
      renderer.root
        .findAllByType('button')
        .find(
          (btn) =>
            btn.props.className?.includes('toolbarButton') &&
            btn.findAllByType('span').some((span) => span.children.includes('model_prices.add_manual'))
        )!;
    const rowAddButtons = () =>
      renderer.root
        .findAllByType('button')
        .filter(
          (btn) =>
            !btn.props.className?.includes('toolbarButton') &&
            btn.findAllByType('span').some((span) => span.children.includes('model_prices.add_manual'))
        );
    const editButtons = () => renderer.root.findAllByProps({ 'aria-label': 'common.edit' });

    const pendingSave = deferred<undefined>();
    mocks.save.mockReturnValueOnce(pendingSave.promise);
    await act(async () => {
      editButtons()[0].props.onClick();
    });
    await clickBySpanLabel('common.save');

    expect(toolbarAddBtn().props.disabled).toBe(true);
    expect(rowAddButtons().every((btn) => btn.props.disabled === true)).toBe(true);
    expect(editButtons().every((btn) => btn.props.disabled === true)).toBe(true);

    await act(async () => {
      pendingSave.resolve(undefined);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(hasDraftEditor()).toBe(false);
    expect(toolbarAddBtn().props.disabled).toBe(false);
    expect(editButtons().every((btn) => btn.props.disabled === false)).toBe(true);

    const pendingSync = deferred<typeof candidateResponse>();
    mocks.sync.mockReturnValueOnce(pendingSync.promise);
    await clickBySpanLabel('usage_stats.model_price_sync');
    expect(toolbarAddBtn().props.disabled).toBe(true);
    expect(rowAddButtons().every((btn) => btn.props.disabled === true)).toBe(true);
    expect(editButtons().every((btn) => btn.props.disabled === true)).toBe(true);

    // Clicking disabled Add Manual or Edit while syncing must not open the editor.
    await act(async () => {
      toolbarAddBtn().props.onClick();
      editButtons()[0].props.onClick();
    });
    expect(hasDraftEditor()).toBe(false);

    await act(async () => {
      pendingSync.resolve({ ...candidateResponse, candidates: [] });
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(toolbarAddBtn().props.disabled).toBe(false);
    expect(editButtons().every((btn) => btn.props.disabled === false)).toBe(true);
  });

  it('blocks writes when usage summary settles before pending or failed price baseline and recovers after retry', async () => {
    mocks.availability = {
      modelPricesAvailable: true,
      managerServiceBase: 'http://manager.local',
    };
    mocks.prices = {};
    mocks.usage = {
      loading: false,
      modelPricesLoaded: false,
      modelPricesLoading: true,
      modelPricesScopeKey: 'scope-a',
    };
    mocks.usageSummary.mockResolvedValue({
      sampled_events: 1,
      total_events: 1,
      truncated: false,
      models: [{ model: 'gpt-5.5', calls: 3, requested_calls: 3, resolved_calls: 3 }],
    });

    await mountAndSettle();
    const toolbarActionButtons = () =>
      renderer.root
        .findAllByType('button')
        .filter((btn) =>
          btn
            .findAllByType('span')
            .some(
              (span) =>
                span.children.includes('model_prices.add_manual') ||
                span.children.includes('usage_stats.model_price_sync')
            )
        );
    for (const btn of toolbarActionButtons()) {
      expect(btn.props.disabled).toBe(true);
      await act(async () => {
        btn.props.onClick();
      });
    }
    expect(hasDraftEditor()).toBe(false);
    expect(mocks.save).not.toHaveBeenCalled();
    expect(mocks.sync).not.toHaveBeenCalled();

    // Price baseline read fails while fallback prices or usageSummary rows exist: all mutations stay blocked.
    mocks.prices = { 'fallback-model': tieredPrice() };
    mocks.usage = {
      loading: false,
      modelPricesLoaded: false,
      modelPricesLoading: false,
      modelPricesScopeKey: 'scope-a',
    };
    await act(async () => {
      renderer.update(<ModelPricesPage />);
    });

    const alertBox = renderer.root.findByProps({ 'data-testid': 'model-prices-load-failed' });
    const retryBtn = alertBox.findByType('button');
    expect(retryBtn).toBeDefined();

    const editBtn = renderer.root.findAllByProps({ 'aria-label': 'common.edit' })[0];
    const deleteBtn = renderer.root.findAllByProps({ 'aria-label': 'common.delete' })[0];
    expect(editBtn.props.disabled).toBe(true);
    expect(deleteBtn.props.disabled).toBe(true);
    for (const btn of [...toolbarActionButtons(), editBtn, deleteBtn]) {
      expect(btn.props.disabled).toBe(true);
      await act(async () => {
        btn.props.onClick();
      });
    }
    expect(hasDraftEditor()).toBe(false);
    expect(mocks.save).not.toHaveBeenCalled();
    expect(mocks.sync).not.toHaveBeenCalled();

    // Retry triggers reloadModelPrices and restores mutations once server prices load.
    await act(async () => {
      retryBtn.props.onClick();
    });
    expect(mocks.reload).toHaveBeenCalledTimes(1);

    mocks.prices = { 'tiered-model': tieredPrice() };
    mocks.usage = {
      loading: false,
      modelPricesLoaded: true,
      modelPricesLoading: false,
      modelPricesScopeKey: 'scope-a',
    };
    await act(async () => {
      renderer.update(<ModelPricesPage />);
    });

    expect(renderer.root.findAllByProps({ 'data-testid': 'model-prices-load-failed' })).toHaveLength(0);
    const rowAddBtn = renderer.root
      .findAllByType('button')
      .find(
        (btn) =>
          !btn.props.className?.includes('toolbarButton') &&
          btn.findAllByType('span').some((span) => span.children.includes('model_prices.add_manual'))
      )!;
    expect(rowAddBtn.props.disabled).toBe(false);

    await act(async () => {
      rowAddBtn.props.onClick();
    });
    expect(hasDraftEditor()).toBe(true);
    await changeField('usage_stats.model_price_prompt ($/1M)', '2');
    await changeField('usage_stats.model_price_completion ($/1M)', '6');
    mocks.save.mockResolvedValueOnce(undefined);
    const saved = await save();
    expect(saved['tiered-model']).toEqual(tieredPrice());
    expect(saved['gpt-5.5']).toMatchObject({ prompt: 2, completion: 6, source: 'manual' });
  });

  it.each([
    'external_management_key_rejected',
    'management_rejected',
    'target_moved',
    'external_cpa_authority_unavailable',
  ])('maps usage service error code %s on model price sync failures', async (code) => {
    mocks.availability = {
      modelPricesAvailable: true,
      managerServiceBase: 'http://manager.local',
    };
    mocks.prices = { 'tiered-model': tieredPrice() };
    await mountAndSettle();

    const syncErr = new Error('raw key rejected') as UsageServiceApiError;
    syncErr.name = 'UsageServiceApiError';
    syncErr.status = code === 'external_cpa_authority_unavailable' ? 503 : 409;
    syncErr.code = code;
    mocks.sync.mockRejectedValueOnce(syncErr);

    await clickBySpanLabel('usage_stats.model_price_sync');
    await act(async () => {
      await Promise.resolve();
    });
    expect(mocks.notify).toHaveBeenCalledWith(
      `usage_stats.model_price_sync_failed: usage_service_errors.${code}`,
      'error'
    );
  });

  it('omits the legacy back_to_monitoring link and renders a unified PageControlBar', async () => {
    mocks.availability = {
      modelPricesAvailable: true,
      requestMonitoringAvailable: true,
      managerServiceBase: 'http://manager.local',
    };
    mocks.prices = { 'tiered-model': tieredPrice() };
    await mountAndSettle();

    const renderedText = JSON.stringify(renderer.toJSON());
    expect(renderer.root.findAllByType(PageControlBar)).toHaveLength(1);
    expect(renderedText).not.toContain('model_prices.back_to_monitoring');
    expect(
      renderer.root.findAllByType('a').some((link) => link.props.to === '/monitoring')
    ).toBe(false);
    expect(renderedText).toContain('model_prices.sync_model_count');
    expect(renderedText).toContain('common.refresh');
  });

  it('wires useHeaderRefresh and toolbar refresh to reload model prices and usage summary', async () => {
    mocks.availability = {
      modelPricesAvailable: true,
      managerServiceBase: 'http://manager.local',
    };
    mocks.prices = { 'tiered-model': tieredPrice() };
    await mountAndSettle();
    const baselineSummaryCalls = mocks.usageSummary.mock.calls.length;

    await act(async () => {
      await triggerHeaderRefresh();
    });

    expect(mocks.reload).toHaveBeenCalledTimes(1);
    expect(mocks.usageSummary).toHaveBeenCalledTimes(baselineSummaryCalls + 1);

    await clickBySpanLabel('common.refresh');
    await act(async () => {
      await Promise.resolve();
    });

    expect(mocks.reload).toHaveBeenCalledTimes(2);
    expect(mocks.usageSummary).toHaveBeenCalledTimes(baselineSummaryCalls + 2);
  });

  it('renders semantic status dot, joined filter tabs, canvas empty state, and permanent inset pagination', async () => {
    mocks.availability = {
      modelPricesAvailable: true,
      managerServiceBase: 'http://manager.local',
    };
    mocks.prices = {};
    await mountAndSettle();

    const statusDots = renderer.root
      .findAllByType('span')
      .filter((node) => String(node.props.className ?? '').includes('statusDot'));
    expect(statusDots).toHaveLength(1);

    const filterTabs = renderer.root.findByType(SegmentedTabs);
    expect(filterTabs.props.variant).toBe('joined');
    expect(filterTabs.props.responsiveFullWidth).toBe(false);

    const emptyStates = renderer.root.findAllByType(EmptyState);
    expect(emptyStates).toHaveLength(1);
    expect(emptyStates[0].props.variant).toBe('canvas');
    expect(renderer.root.findAllByType(PaginationFooter)).toHaveLength(0);

    const sixtyModels = Object.fromEntries(
      Array.from({ length: 60 }, (_, index) => [
        `model-${String(index + 1).padStart(2, '0')}`,
        tieredPrice(),
      ])
    );
    mocks.prices = sixtyModels;
    await act(async () => {
      renderer.update(<ModelPricesPage />);
    });

    const footer = renderer.root.findByType(PaginationFooter);
    expect(footer.props.variant).toBe('inset');
    expect(footer.props.pageSize).toBe(50);
    expect(footer.props.pageSizeOptions).toEqual([20, 50, 100]);
    expect(footer.props.currentPage).toBe(1);
    expect(footer.props.totalPages).toBe(2);
    expect(renderer.root.findAllByType('tbody')[0].findAllByType('tr')).toHaveLength(50);

    await act(async () => {
      footer.props.onPageChange?.(2);
    });
    expect(renderer.root.findByType(PaginationFooter).props.currentPage).toBe(2);
    expect(renderer.root.findAllByType('tbody')[0].findAllByType('tr')).toHaveLength(10);

    const searchInput = renderer.root
      .findAllByType(Input)
      .find((input) => input.props.placeholder === 'model_prices.search_placeholder');
    await act(async () => {
      searchInput?.props.onChange({ target: { value: 'model-0' } });
    });
    expect(renderer.root.findByType(PaginationFooter).props.currentPage).toBe(1);
  });
});
