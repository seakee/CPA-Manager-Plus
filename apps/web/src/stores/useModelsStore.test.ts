import { beforeEach, describe, expect, it, vi } from 'vitest';
import { modelsApi } from '@/services/api/models';
import { useModelsStore } from './useModelsStore';
import type { ModelInfo } from '@/utils/models';

vi.mock('@/services/api/models', () => ({ modelsApi: { fetchModels: vi.fn() } }));
const deferred = () => {
  let resolve!: (value: ModelInfo[]) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<ModelInfo[]>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
};

describe('model list revalidation', () => {
  beforeEach(() => {
    useModelsStore.getState().clearCache();
    vi.mocked(modelsApi.fetchModels).mockReset();
  });

  it('retains the last successful list during pending and failed refreshes', async () => {
    vi.mocked(modelsApi.fetchModels).mockResolvedValueOnce([{ name: 'old' }]);
    await useModelsStore.getState().fetchModels('a', 'key');
    const next = deferred();
    vi.mocked(modelsApi.fetchModels).mockReturnValueOnce(next.promise);
    const pending = useModelsStore.getState().fetchModels('a', 'key', true);
    expect(useModelsStore.getState().models).toEqual([{ name: 'old' }]);
    next.reject(new Error('offline'));
    await expect(pending).rejects.toThrow('offline');
    expect(useModelsStore.getState().models).toEqual([{ name: 'old' }]);
    expect(useModelsStore.getState().error).toBe('offline');
  });

  it('clears old scope data immediately and ignores its late response', async () => {
    const old = deferred(),
      next = deferred();
    vi.mocked(modelsApi.fetchModels)
      .mockResolvedValueOnce([{ name: 'initial' }])
      .mockReturnValueOnce(old.promise)
      .mockReturnValueOnce(next.promise);
    await useModelsStore.getState().fetchModels('a', 'key');
    const first = useModelsStore.getState().fetchModels('a', 'key', true);
    const second = useModelsStore.getState().fetchModels('b', 'new-key', true);
    expect(useModelsStore.getState().models).toEqual([]);
    next.resolve([{ name: 'new-scope' }]);
    await second;
    old.resolve([{ name: 'stale' }]);
    await first;
    expect(useModelsStore.getState().models).toEqual([{ name: 'new-scope' }]);
  });

  it('does not restore data after an explicit cache invalidation', async () => {
    const old = deferred();
    vi.mocked(modelsApi.fetchModels).mockReturnValueOnce(old.promise);
    const pending = useModelsStore.getState().fetchModels('a');
    useModelsStore.getState().clearCache();
    old.resolve([{ name: 'invalidated' }]);
    await pending;
    expect(useModelsStore.getState().models).toEqual([]);
    expect(useModelsStore.getState().loading).toBe(false);
  });

  it('finishes busy state when a cache read supersedes an in-flight refresh', async () => {
    vi.mocked(modelsApi.fetchModels).mockResolvedValueOnce([{ name: 'cached' }]);
    await useModelsStore.getState().fetchModels('a');
    const old = deferred();
    vi.mocked(modelsApi.fetchModels).mockReturnValueOnce(old.promise);
    const pending = useModelsStore.getState().fetchModels('a', undefined, true);
    await useModelsStore.getState().fetchModels('a');
    expect(useModelsStore.getState().loading).toBe(false);
    old.resolve([{ name: 'superseded' }]);
    await pending;
    expect(useModelsStore.getState().models).toEqual([{ name: 'cached' }]);
  });
});
