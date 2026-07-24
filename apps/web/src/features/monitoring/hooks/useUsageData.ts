import { useCallback, useEffect, useRef, useState } from 'react';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import {
  usageServiceApi,
  type ApiKeyAlias,
  type ApiKeyAliasesResponse,
  type ModelPricesResponse,
  type ModelPriceSyncResponse,
  type UsageExportResponse,
  type UsageImportResponse,
  type UsageImportSession,
} from '@/services/api/usageService';
import { useAuthStore } from '@/stores';
import { clearModelPrices, loadModelPrices, saveModelPrices, type ModelPrice } from '@/utils/usage';
import {
  cancelUsageImportFile,
  uploadUsageImportFile,
  type UsageImportProgress,
} from '@/features/monitoring/services/usageImportSession';

export interface UsagePayload {
  total_requests?: number;
  success_count?: number;
  failure_count?: number;
  total_tokens?: number;
  apis?: Record<string, unknown>;
  [key: string]: unknown;
}

export interface UseUsageDataReturn {
  usage: UsagePayload | null;
  loading: boolean;
  error: string;
  lastRefreshedAt: Date | null;
  modelPrices: Record<string, ModelPrice>;
  apiKeyAliases: ApiKeyAlias[];
  usageServiceAvailable: boolean;
  setModelPrices: (prices: Record<string, ModelPrice>) => Promise<void>;
  loadApiKeyAliases: () => Promise<void>;
  syncModelPrices: (models?: string[]) => Promise<ModelPriceSyncResponse>;
  exportUsage: () => Promise<UsageExportResponse>;
  importUsage: (file: File, options?: UsageImportOptions) => Promise<UsageImportResponse>;
  cancelUsageImport: (sessionId: string, file?: File) => Promise<UsageImportSession | null>;
  loadUsage: () => Promise<void>;
}

export interface UsageImportOptions {
  signal?: AbortSignal;
  onProgress?: (progress: UsageImportProgress) => void;
}

export interface UseUsageDataOptions {
  loadUsageEvents?: boolean;
}

export function useUsageData({
  loadUsageEvents = true,
}: UseUsageDataOptions = {}): UseUsageDataReturn {
  const managementKey = useAuthStore((state) => state.managementKey);
  const featureAvailability = usePanelFeatureAvailability();
  const [usage, setUsage] = useState<UsagePayload | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [lastRefreshedAt, setLastRefreshedAt] = useState<Date | null>(null);
  const [modelPrices, setModelPricesState] = useState<Record<string, ModelPrice>>({});
  const [apiKeyAliases, setApiKeyAliases] = useState<ApiKeyAlias[]>([]);
  const [usageServiceAvailable, setUsageServiceAvailable] = useState(false);
  const requestIdRef = useRef(0);
  const aliasRequestIdRef = useRef(0);
  const managerServiceAvailable = featureAvailability.managerServiceAvailable;
  const modelPriceServiceBase = featureAvailability.modelPricesAvailable
    ? featureAvailability.managerServiceBase
    : '';
  const usageEventsServiceBase = featureAvailability.requestMonitoringAvailable
    ? featureAvailability.managerServiceBase
    : '';

  const getModelPricesFromApi = useCallback(async (): Promise<ModelPricesResponse> => {
    if (!modelPriceServiceBase) {
      return { prices: {} };
    }
    return usageServiceApi.getModelPrices(modelPriceServiceBase, managementKey);
  }, [managementKey, modelPriceServiceBase]);

  const getApiKeyAliasesFromApi = useCallback(async (): Promise<ApiKeyAliasesResponse> => {
    if (!modelPriceServiceBase) {
      return { items: [] };
    }
    return usageServiceApi.getApiKeyAliases(modelPriceServiceBase, managementKey);
  }, [managementKey, modelPriceServiceBase]);

  const saveModelPricesToApi = useCallback(
    async (prices: Record<string, ModelPrice>): Promise<ModelPricesResponse> => {
      if (!modelPriceServiceBase) {
        throw new Error('model_price_api_unavailable');
      }
      return usageServiceApi.saveModelPrices(modelPriceServiceBase, prices, managementKey);
    },
    [managementKey, modelPriceServiceBase]
  );

  const syncModelPricesFromApi = useCallback(
    async (models?: string[]): Promise<ModelPriceSyncResponse> => {
      if (!modelPriceServiceBase) {
        throw new Error('model_price_sync_requires_usage_service');
      }
      return usageServiceApi.syncModelPrices(modelPriceServiceBase, managementKey, models);
    },
    [managementKey, modelPriceServiceBase]
  );

  const exportUsageFromApi = useCallback(async (): Promise<UsageExportResponse> => {
    if (!usageEventsServiceBase) {
      throw new Error('usage_import_export_requires_usage_service');
    }
    return usageServiceApi.exportUsage(usageEventsServiceBase, managementKey);
  }, [managementKey, usageEventsServiceBase]);

  const importUsageToApi = useCallback(
    async (file: File, options?: UsageImportOptions): Promise<UsageImportResponse> => {
      if (!usageEventsServiceBase) {
        throw new Error('usage_import_export_requires_usage_service');
      }
      return uploadUsageImportFile({
        base: usageEventsServiceBase,
        managementKey,
        file,
        signal: options?.signal,
        onProgress: options?.onProgress,
      });
    },
    [managementKey, usageEventsServiceBase]
  );

  const cancelUsageImportOnApi = useCallback(
    async (sessionId: string, file?: File): Promise<UsageImportSession | null> => {
      if (!usageEventsServiceBase) {
        throw new Error('usage_import_export_requires_usage_service');
      }
      return cancelUsageImportFile({
        base: usageEventsServiceBase,
        managementKey,
        sessionId,
        file,
      });
    },
    [managementKey, usageEventsServiceBase]
  );

  const loadModelPricesFromStorage = useCallback(async () => {
    const fallbackPrices = loadModelPrices();
    try {
      const response = await getModelPricesFromApi();
      const apiPrices = response.prices ?? {};
      if (Object.keys(apiPrices).length > 0) {
        setModelPricesState(apiPrices);
        clearModelPrices();
        return;
      }
      if (Object.keys(fallbackPrices).length > 0) {
        const migrated = await saveModelPricesToApi(fallbackPrices);
        setModelPricesState(migrated.prices ?? fallbackPrices);
        clearModelPrices();
        return;
      }
      setModelPricesState({});
    } catch {
      setModelPricesState(fallbackPrices);
    }
  }, [getModelPricesFromApi, saveModelPricesToApi]);

  const loadApiKeyAliases = useCallback(async () => {
    const requestId = aliasRequestIdRef.current + 1;
    aliasRequestIdRef.current = requestId;
    try {
      const response = await getApiKeyAliasesFromApi();
      if (aliasRequestIdRef.current !== requestId) return;
      setApiKeyAliases(Array.isArray(response.items) ? response.items : []);
    } catch {
      if (aliasRequestIdRef.current !== requestId) return;
      setApiKeyAliases([]);
    }
  }, [getApiKeyAliasesFromApi]);

  const loadUsage = useCallback(async () => {
    const requestId = requestIdRef.current + 1;
    requestIdRef.current = requestId;
    if (!loadUsageEvents) {
      setUsageServiceAvailable(false);
      setUsage(null);
      setLastRefreshedAt(null);
      setLoading(false);
      setError('');
      return;
    }

    setLoading(true);
    setError('');

    try {
      if (!usageEventsServiceBase) {
        setUsageServiceAvailable(false);
        setUsage(null);
        setLastRefreshedAt(null);
        return;
      }
      setUsageServiceAvailable(true);
      const payload = await usageServiceApi.getUsage(usageEventsServiceBase, managementKey);
      if (requestIdRef.current !== requestId) return;
      setUsage(payload ?? null);
      setLastRefreshedAt(new Date());
    } catch (err) {
      if (requestIdRef.current !== requestId) return;
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (requestIdRef.current === requestId) {
        setLoading(false);
      }
    }
  }, [loadUsageEvents, managementKey, usageEventsServiceBase]);

  useEffect(() => {
    void loadModelPricesFromStorage();
    void loadApiKeyAliases();
    void loadUsage();
  }, [loadApiKeyAliases, loadModelPricesFromStorage, loadUsage]);

  const setModelPrices = useCallback(
    async (prices: Record<string, ModelPrice>) => {
      setModelPricesState(prices);
      try {
        const response = await saveModelPricesToApi(prices);
        setModelPricesState(response.prices ?? prices);
        clearModelPrices();
      } catch {
        saveModelPrices(prices);
      }
    },
    [saveModelPricesToApi]
  );

  const syncModelPrices = useCallback(
    async (models?: string[]) => {
      const response = await syncModelPricesFromApi(models);
      setModelPricesState(response.prices ?? {});
      clearModelPrices();
      return response;
    },
    [syncModelPricesFromApi]
  );

  return {
    usage,
    loading,
    error,
    lastRefreshedAt,
    modelPrices,
    apiKeyAliases,
    usageServiceAvailable: managerServiceAvailable || usageServiceAvailable,
    setModelPrices,
    loadApiKeyAliases,
    syncModelPrices,
    exportUsage: exportUsageFromApi,
    importUsage: importUsageToApi,
    cancelUsageImport: cancelUsageImportOnApi,
    loadUsage,
  };
}
