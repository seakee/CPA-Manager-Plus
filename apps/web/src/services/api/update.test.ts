import { beforeEach, describe, expect, it, vi } from 'vitest';

const get = vi.fn();
const post = vi.fn();

vi.mock('./client', () => ({
  apiClient: { get, post },
}));

vi.mock('@/features/demo/demoMode', () => ({
  isDemoMode: () => false,
}));

describe('managedUpdateApi', () => {
  beforeEach(() => {
    get.mockReset();
    post.mockReset();
  });

  it('uses authenticated Manager Server management endpoints', async () => {
    const { managedUpdateApi } = await import('./update');
    get.mockResolvedValue({ supported: true });
    post.mockResolvedValue({ state: 'staged' });

    await managedUpdateApi.capability();
    await managedUpdateApi.check();
    await managedUpdateApi.status();
    await managedUpdateApi.plan();
    await managedUpdateApi.apply();

    expect(get).toHaveBeenNthCalledWith(1, '/update/capability');
    expect(get).toHaveBeenNthCalledWith(2, '/update/check');
    expect(get).toHaveBeenNthCalledWith(3, '/update/status');
    expect(post).toHaveBeenNthCalledWith(1, '/update/plan');
    expect(post).toHaveBeenNthCalledWith(2, '/update/apply');
  });
});
