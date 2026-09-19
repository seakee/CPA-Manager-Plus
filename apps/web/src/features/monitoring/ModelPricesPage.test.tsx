import { MemoryRouter } from 'react-router-dom';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ModelPricesPage } from './ModelPricesPage';
import * as attentionHook from '@/features/model-price-attention/useModelPriceAttention';
import * as usageDataHook from './hooks/useUsageData';
import { usageServiceApi } from '@/services/api/usageService';

const { showNotification } = vi.hoisted(() => ({ showNotification: vi.fn() }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string, options?: { count?: number; defaultValue?: string }) => {
        if (key === 'usage_stats.model_price_sync') return '同步价格';
        if (key === 'model_prices.pending_sync_badge') return '待同步';
        if (key === 'model_prices.filter_missing') return '缺价格';
        if (key === 'model_prices.filter_all') return '全部';
        return options?.defaultValue || key;
      },
    }),
  };
});

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => ({
    modelPricesAvailable: true,
    requestMonitoringAvailable: true,
    managerServiceBase: 'http://localhost:18317',
  }),
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: { managementKey: string }) => unknown) =>
    selector({ managementKey: 'test-key' }),
  useNotificationStore: () => ({
    showNotification,
  }),
}));

describe('ModelPricesPage Attention UI', () => {
  let mockAttentionState: ReturnType<typeof attentionHook.useModelPriceAttention>;
  let mockSyncModelPrices: ReturnType<typeof vi.fn>;
  let mockSetModelPrices: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    showNotification.mockReset();
    vi.spyOn(usageServiceApi, 'getModelPriceUsageSummary').mockResolvedValue({
      sampled_events: 0,
      total_events: 0,
      truncated: false,
      models: [],
    });

    mockSyncModelPrices = vi.fn().mockResolvedValue({
      imported: 1,
      skipped: 0,
      prices: {},
    });
    mockSetModelPrices = vi.fn().mockResolvedValue(undefined);

    vi.spyOn(usageDataHook, 'useUsageData').mockReturnValue({
      loading: false,
      modelPrices: {},
      setModelPrices: mockSetModelPrices,
      syncModelPrices: mockSyncModelPrices,
      usageServiceAvailable: true,
    } as unknown as ReturnType<typeof usageDataHook.useUsageData>);

    mockAttentionState = {
      runtimeModels: ['runtime-new-model'],
      unpricedModels: ['runtime-new-model'],
      acknowledgedModels: [],
      pendingModels: ['runtime-new-model'],
      pendingCount: 1,
      hasAttention: true,
      modelPricesAvailable: true,
      loading: false,
      lastCheckedAtMs: null,
      check: vi.fn().mockResolvedValue(undefined),
      capturePendingSnapshot: vi.fn().mockReturnValue({
        scope: 'http://localhost:18317',
        models: ['runtime-new-model'],
      }),
      acknowledgeSnapshot: vi.fn().mockResolvedValue(undefined),
    };

    vi.spyOn(attentionHook, 'useModelPriceAttention').mockImplementation(() => mockAttentionState);
  });

  it('renders pending count badge on Sync Prices button when pendingCount > 0', async () => {
    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/model-prices']}>
          <ModelPricesPage />
        </MemoryRouter>
      );
    });

    const root = renderer!.root;
    const badge = root.findByProps({ 'data-testid': 'sync-pending-badge' });
    expect(badge).toBeDefined();
    expect(badge.props.children).toBe(1);
  });

  it('does not render pending count badge on Sync Prices button when pendingCount = 0', async () => {
    mockAttentionState.pendingCount = 0;
    mockAttentionState.pendingModels = [];
    mockAttentionState.hasAttention = false;

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/model-prices']}>
          <ModelPricesPage />
        </MemoryRouter>
      );
    });

    const root = renderer!.root;
    expect(root.findAllByProps({ 'data-testid': 'sync-pending-badge' })).toHaveLength(0);
  });

  it('renders pending badge next to pending runtime model in the table', async () => {
    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/model-prices']}>
          <ModelPricesPage />
        </MemoryRouter>
      );
    });

    const root = renderer!.root;
    const modelBadge = root.findByProps({
      'data-testid': 'pending-badge-runtime-new-model',
    });
    expect(modelBadge).toBeDefined();
    expect(modelBadge.props.children).toBe('待同步');
  });

  it('activates filter=missing initially from URL query, and allows switching to other tabs without being locked', async () => {
    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/model-prices?filter=missing']}>
          <ModelPricesPage />
        </MemoryRouter>
      );
    });

    const root = renderer!.root;
    const missingBtn = root.findByProps({ 'data-filter': 'missing' });
    expect(missingBtn.props['data-active']).toBe(true);

    // Click 'all' button
    const allBtn = root.findByProps({ 'data-filter': 'all' });
    await act(async () => {
      allBtn.props.onClick();
    });

    expect(allBtn.props['data-active']).toBe(true);
    expect(missingBtn.props['data-active']).toBe(false);

    // Click 'candidates' button
    const candidatesBtn = root.findByProps({ 'data-filter': 'candidates' });
    await act(async () => {
      candidatesBtn.props.onClick();
    });
    expect(candidatesBtn.props['data-active']).toBe(true);
    expect(allBtn.props['data-active']).toBe(false);
  });

  it('acknowledges all pending snapshot models when runtime discovery succeeds', async () => {
    mockAttentionState.capturePendingSnapshot = vi.fn().mockReturnValue({
      scope: 'http://localhost:18317',
      models: ['model-A', 'model-B'],
    });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/model-prices']}>
          <ModelPricesPage />
        </MemoryRouter>
      );
    });

    const root = renderer!.root;
    const syncButton = root.findByProps({ 'data-testid': 'sync-prices-button' });

    await act(async () => {
      syncButton.props.onClick();
    });

    expect(mockAttentionState.capturePendingSnapshot).toHaveBeenCalled();
    expect(mockSyncModelPrices).toHaveBeenCalled();
    expect(mockAttentionState.acknowledgeSnapshot).toHaveBeenCalledWith({
      scope: 'http://localhost:18317',
      models: ['model-A', 'model-B'],
    });
  });

  it('acknowledges only (pendingSnapshot ∩ syncModels) when runtime discovery fails with fallback sync success', async () => {
    mockAttentionState.capturePendingSnapshot = vi.fn().mockReturnValue({
      scope: 'http://localhost:18317',
      models: ['known-model-A', 'runtime-only-model-B'],
    });

    // Provide usage summary containing known-model-A so it enters syncModels
    vi.spyOn(usageServiceApi, 'getModelPriceUsageSummary').mockResolvedValue({
      sampled_events: 1,
      total_events: 1,
      truncated: false,
      models: [{ model: 'known-model-A', calls: 1, requested_calls: 1, resolved_calls: 1 }],
    });

    // Sync succeeds with runtimeModelDiscoveryError
    mockSyncModelPrices.mockResolvedValueOnce({
      imported: 1,
      skipped: 0,
      prices: {},
      runtimeModelDiscoveryError: '504 gateway timeout',
    });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/model-prices']}>
          <ModelPricesPage />
        </MemoryRouter>
      );
    });

    const root = renderer!.root;
    const syncButton = root.findByProps({ 'data-testid': 'sync-prices-button' });

    await act(async () => {
      syncButton.props.onClick();
    });

    // Only known-model-A was actually synced; runtime-only-model-B must NOT be acknowledged
    expect(mockAttentionState.acknowledgeSnapshot).toHaveBeenCalledWith({
      scope: 'http://localhost:18317',
      models: ['known-model-A'],
    });
  });

  it('does not acknowledge runtime-only pending models when discovery fails and syncModels has no overlap', async () => {
    mockAttentionState.capturePendingSnapshot = vi.fn().mockReturnValue({
      scope: 'http://localhost:18317',
      models: ['runtime-only-model-B'],
    });

    mockSyncModelPrices.mockResolvedValueOnce({
      imported: 0,
      skipped: 0,
      prices: {},
      runtimeModelDiscoveryError: 'discovery failed',
    });

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/model-prices']}>
          <ModelPricesPage />
        </MemoryRouter>
      );
    });

    const root = renderer!.root;
    const syncButton = root.findByProps({ 'data-testid': 'sync-prices-button' });

    await act(async () => {
      syncButton.props.onClick();
    });

    // Acknowledge should not be called because acknowledged models is empty
    expect(mockAttentionState.acknowledgeSnapshot).not.toHaveBeenCalled();
  });

  it('does not acknowledge any pending snapshot when syncModelPrices rejects', async () => {
    mockSyncModelPrices.mockRejectedValueOnce(new Error('Sync failed'));

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/model-prices']}>
          <ModelPricesPage />
        </MemoryRouter>
      );
    });

    const root = renderer!.root;
    const syncButton = root.findByProps({ 'data-testid': 'sync-prices-button' });

    await act(async () => {
      syncButton.props.onClick();
    });

    expect(mockAttentionState.acknowledgeSnapshot).not.toHaveBeenCalled();
  });

  it('triggers attention.check({ force: true }) after saving manual model price', async () => {
    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/model-prices']}>
          <ModelPricesPage />
        </MemoryRouter>
      );
    });

    const root = renderer!.root;
    // Open add price modal
    const addPriceBtn = root.findByProps({ 'data-testid': 'add-price-button' });
    await act(async () => {
      addPriceBtn.props.onClick();
    });

    // Fill draft model and price
    const modelInput = root.findByProps({ 'data-testid': 'draft-model-input' });
    const inputPrice = root.findByProps({ 'data-testid': 'draft-input-price' });
    const outputPrice = root.findByProps({ 'data-testid': 'draft-output-price' });

    await act(async () => {
      modelInput.props.onChange({ target: { value: 'custom-model' } });
      inputPrice.props.onChange({ target: { value: '1.5' } });
      outputPrice.props.onChange({ target: { value: '3.0' } });
    });

    // Click save
    const saveBtn = root.findByProps({ 'data-testid': 'save-draft-button' });
    await act(async () => {
      saveBtn.props.onClick();
    });

    expect(mockAttentionState.check).toHaveBeenCalledWith({ force: true });
  });

  it.each([
    [
      Object.assign(new Error('raw backend message'), {
        code: 'model_price_structure_locked_by_usage_archive',
      }),
      'model_prices.structure_locked_by_usage_archive',
    ],
    [new Error('Network Error'), 'model_prices.save_failed'],
    [new Error('model_price_api_unavailable'), 'model_prices.save_unavailable'],
  ])('keeps a rejected draft editable and never reports success: %s', async (error, message) => {
    mockSetModelPrices.mockRejectedValueOnce(error);
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/model-prices']}>
          <ModelPricesPage />
        </MemoryRouter>
      );
    });
    const root = renderer.root;
    await act(async () => root.findByProps({ 'data-testid': 'add-price-button' }).props.onClick());
    await act(async () => {
      root.findByProps({ 'data-testid': 'draft-model-input' }).props.onChange({
        target: { value: 'custom-model' },
      });
      root.findByProps({ 'data-testid': 'draft-input-price' }).props.onChange({
        target: { value: '1.5' },
      });
      root.findByProps({ 'data-testid': 'draft-output-price' }).props.onChange({
        target: { value: '3.0' },
      });
    });
    await act(async () => root.findByProps({ 'data-testid': 'save-draft-button' }).props.onClick());

    expect(showNotification).toHaveBeenCalledWith(message, 'error');
    expect(showNotification).not.toHaveBeenCalledWith(expect.anything(), 'success');
    expect(mockAttentionState.check).not.toHaveBeenCalled();
    expect(root.findByProps({ 'data-testid': 'draft-model-input' }).props.value).toBe(
      'custom-model'
    );
    expect(root.findByProps({ 'data-testid': 'draft-input-price' }).props.value).toBe('1.5');

    await act(async () => root.findByProps({ 'data-testid': 'save-draft-button' }).props.onClick());
    expect(mockSetModelPrices).toHaveBeenCalledTimes(2);
    expect(showNotification).toHaveBeenCalledWith('usage_stats.model_price_saved', 'success');
    expect(root.findAllByProps({ 'data-testid': 'draft-model-input' })).toHaveLength(0);
    act(() => renderer.unmount());
  });

  it('keeps the price and open editor when deletion is rejected', async () => {
    mockSetModelPrices.mockRejectedValueOnce(new Error('Network Error'));
    vi.mocked(usageDataHook.useUsageData).mockReturnValue({
      loading: false,
      modelPrices: { 'saved-model': { prompt: 1, completion: 2, cache: 0.5 } },
      setModelPrices: mockSetModelPrices,
      syncModelPrices: mockSyncModelPrices,
      usageServiceAvailable: true,
    } as unknown as ReturnType<typeof usageDataHook.useUsageData>);
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/model-prices']}>
          <ModelPricesPage />
        </MemoryRouter>
      );
    });
    const root = renderer.root;
    const button = (label: string) =>
      root.findAllByType('button').find((node) => node.props['aria-label'] === label)!;
    await act(async () => button('common.edit').props.onClick());
    await act(async () => button('common.delete').props.onClick());

    expect(mockSetModelPrices).toHaveBeenCalledWith({});
    expect(showNotification).toHaveBeenCalledWith('model_prices.save_failed', 'error');
    expect(button('common.delete')).toBeDefined();
    expect(root.findByProps({ 'data-testid': 'draft-model-input' }).props.value).toBe(
      'saved-model'
    );
    act(() => renderer.unmount());
  });

  it('keeps an unconfirmed candidate available when saving it fails', async () => {
    mockSyncModelPrices.mockResolvedValueOnce({
      imported: 0,
      skipped: 0,
      prices: {},
      candidates: [
        {
          model: 'runtime-new-model',
          candidates: [
            {
              sourceModelId: 'source-model',
              score: 0.9,
              price: { prompt: 1, completion: 2, cache: 0.5, source: 'test-source' },
            },
          ],
        },
      ],
    });
    mockSetModelPrices.mockRejectedValueOnce(new Error('Network Error'));
    let renderer!: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/model-prices']}>
          <ModelPricesPage />
        </MemoryRouter>
      );
    });
    const root = renderer.root;
    await act(async () =>
      root.findByProps({ 'data-testid': 'sync-prices-button' }).props.onClick()
    );
    showNotification.mockClear();
    const confirmButton = () =>
      root.findByProps({ children: 'model_prices.confirm_candidate' }).parent!;
    await act(async () => confirmButton().props.onClick());

    expect(showNotification).toHaveBeenCalledWith('model_prices.save_failed', 'error');
    expect(showNotification).not.toHaveBeenCalledWith(expect.anything(), 'success');
    expect(confirmButton()).toBeDefined();
    expect(mockAttentionState.check).not.toHaveBeenCalled();

    await act(async () => confirmButton().props.onClick());
    expect(showNotification).toHaveBeenCalledWith('model_prices.candidate_confirmed', 'success');
    expect(mockAttentionState.check).toHaveBeenCalledWith({ force: true });
    expect(root.findAllByProps({ children: 'model_prices.confirm_candidate' })).toHaveLength(0);
    act(() => renderer.unmount());
  });

  it('preserves special pricing rules and refreshes attention after editing an existing price', async () => {
    const setModelPrices = vi.fn().mockResolvedValue(undefined);
    vi.mocked(usageDataHook.useUsageData).mockReturnValue({
      loading: false,
      modelPrices: {
        'tiered-model': {
          prompt: 5,
          completion: 25,
          cache: 0.5,
          contextTiers: [
            {
              thresholdTokens: 128_000,
              prompt: 10,
              completion: 0,
              cache: 0,
              promptConfigured: true,
              completionConfigured: false,
              cacheConfigured: false,
            },
          ],
          serviceTiers: [
            {
              mode: 'fast',
              serviceTier: 'priority',
              prompt: 0,
              completion: 50,
              cache: 0,
              promptConfigured: true,
              completionConfigured: true,
              cacheConfigured: false,
            },
          ],
        },
      },
      setModelPrices,
      syncModelPrices: mockSyncModelPrices,
      usageServiceAvailable: true,
    } as unknown as ReturnType<typeof usageDataHook.useUsageData>);

    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={['/model-prices']}>
          <ModelPricesPage />
        </MemoryRouter>
      );
    });

    const root = renderer!.root;
    const editButton = root
      .findAllByType('button')
      .find((button) => button.props['aria-label'] === 'common.edit');
    if (!editButton) throw new Error('edit button not found');

    await act(async () => {
      editButton.props.onClick();
    });
    const inputPrice = root.findByProps({ 'data-testid': 'draft-input-price' });
    await act(async () => {
      inputPrice.props.onChange({ target: { value: '6' } });
    });

    const saveButton = root.findByProps({ 'data-testid': 'save-draft-button' });
    await act(async () => {
      saveButton.props.onClick();
    });

    expect(setModelPrices).toHaveBeenCalledWith({
      'tiered-model': expect.objectContaining({
        prompt: 6,
        source: 'manual',
        contextTiers: [
          expect.objectContaining({
            thresholdTokens: 128_000,
            prompt: 10,
            promptConfigured: true,
            completionConfigured: false,
          }),
        ],
        serviceTiers: [
          expect.objectContaining({
            mode: 'fast',
            serviceTier: 'priority',
            prompt: 0,
            completion: 50,
            promptConfigured: true,
            completionConfigured: true,
          }),
        ],
      }),
    });
    expect(mockAttentionState.check).toHaveBeenCalledWith({ force: true });
  });
});
