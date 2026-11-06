/**
 * 模型列表状态管理（带缓存）
 */

import { create } from 'zustand';
import { modelsApi } from '@/services/api/models';
import { CACHE_EXPIRY_MS } from '@/utils/constants';
import type { ModelInfo } from '@/utils/models';

interface ModelsCache {
  data: ModelInfo[];
  timestamp: number;
  apiBase: string;
  apiKey: string;
  managementKey: string;
}

interface ModelsState {
  models: ModelInfo[];
  loading: boolean;
  error: string | null;
  cache: ModelsCache | null;

  fetchModels: (
    apiBase: string,
    apiKey?: string,
    forceRefresh?: boolean,
    managementKey?: string
  ) => Promise<ModelInfo[]>;
  clearCache: () => void;
  isCacheValid: (apiBase: string, apiKey?: string, managementKey?: string) => boolean;
}

let requestGeneration = 0;

export const useModelsStore = create<ModelsState>((set, get) => ({
  models: [],
  loading: false,
  error: null,
  cache: null,

  fetchModels: async (apiBase, apiKey, forceRefresh = false, managementKey) => {
    const generation = ++requestGeneration;
    const { cache, isCacheValid } = get();
    const apiKeyScope = apiKey?.trim() || '';
    const managementKeyScope = managementKey?.trim() || '';

    // 检查缓存
    if (!forceRefresh && isCacheValid(apiBase, apiKeyScope, managementKeyScope) && cache) {
      set({ models: cache.data, error: null, loading: false });
      return cache.data;
    }

    const sameScope =
      cache?.apiBase === apiBase &&
      cache.apiKey === apiKeyScope &&
      cache.managementKey === managementKeyScope;
    // A refresh keeps the last successful list for the same scope visible;
    // switching connection scope clears it immediately instead of showing
    // another server's models while the new list is pending.
    set({ loading: true, error: null, ...(sameScope ? {} : { models: [], cache: null }) });

    try {
      // The Fresh Console bridge is a same-origin Manager API. Its canonical
      // boundary requires the verified Manager admin credential; a CPA client
      // key alone is intentionally insufficient when the saved CPA connection
      // has no configured key yet. Standalone Panel callers keep their
      // existing CPA-key behavior by leaving this header empty.
      const requestHeaders: Record<string, string> = managementKeyScope
        ? { Authorization: `Bearer ${managementKeyScope}` }
        : {};
      const list = await modelsApi.fetchModels(apiBase, apiKeyScope || undefined, requestHeaders);
      // A later request (or clearCache) owns the state now.
      if (generation !== requestGeneration) return list;
      const now = Date.now();

      set({
        models: list,
        loading: false,
        cache: {
          data: list,
          timestamp: now,
          apiBase,
          apiKey: apiKeyScope,
          managementKey: managementKeyScope,
        },
      });

      return list;
    } catch (error: unknown) {
      if (generation !== requestGeneration) throw error;
      const message =
        error instanceof Error
          ? error.message
          : typeof error === 'string'
            ? error
            : 'Failed to fetch models';
      // Keep the last successful list on failure; only the error is new.
      set({
        error: message,
        loading: false,
      });
      throw error;
    }
  },

  clearCache: () => {
    requestGeneration += 1;
    set({ cache: null, models: [], loading: false, error: null });
  },

  isCacheValid: (apiBase, apiKey, managementKey) => {
    const { cache } = get();
    if (!cache) return false;
    if (cache.apiBase !== apiBase) return false;
    const apiKeyScope = apiKey?.trim() || '';
    if ((cache.apiKey || '') !== apiKeyScope) return false;
    const managementKeyScope = managementKey?.trim() || '';
    if ((cache.managementKey || '') !== managementKeyScope) return false;
    return Date.now() - cache.timestamp < CACHE_EXPIRY_MS;
  },
}));
