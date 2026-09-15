import { beforeEach, describe, expect, it, vi } from 'vitest';

const { mocks } = vi.hoisted(() => {
  const requestUse = vi.fn();
  const responseUse = vi.fn();
  const axiosInstance = {
    defaults: { timeout: 0 },
    interceptors: {
      request: { use: requestUse },
      response: { use: responseUse },
    },
  };
  return {
    mocks: {
      create: vi.fn(() => axiosInstance),
      post: vi.fn(),
    },
    axiosInstance,
  };
});

vi.mock('axios', () => ({
  default: {
    create: mocks.create,
    post: mocks.post,
    isAxiosError: () => false,
  },
}));

vi.mock('@/features/demo/demoMode', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/demo/demoMode')>();
  return {
    ...actual,
    isDemoMode: () => false,
  };
});

import { usageServiceApi } from './usageService';

beforeEach(() => {
  mocks.post.mockReset();
  mocks.post.mockResolvedValue({ data: { prices: {}, imported: 0, skipped: 0 } });
});

describe('usageServiceApi.syncModelPrices', () => {
  it('omits source for the default fallback chain', async () => {
    await usageServiceApi.syncModelPrices('http://manager.example', 'manager-key', {
      models: ['gpt-test'],
    });

    expect(mocks.post).toHaveBeenCalledWith(
      'http://manager.example/v0/management/model-prices/sync',
      { models: ['gpt-test'] },
      expect.objectContaining({ headers: { Authorization: 'Bearer manager-key' } })
    );
  });

  it('sends an explicitly selected sync source', async () => {
    await usageServiceApi.syncModelPrices('http://manager.example', 'manager-key', {
      models: ['gpt-test'],
      source: 'litellm',
    });

    expect(mocks.post).toHaveBeenCalledWith(
      'http://manager.example/v0/management/model-prices/sync',
      { models: ['gpt-test'], source: 'litellm' },
      expect.objectContaining({ headers: { Authorization: 'Bearer manager-key' } })
    );
  });
});
