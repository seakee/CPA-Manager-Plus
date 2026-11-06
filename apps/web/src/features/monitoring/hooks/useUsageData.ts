import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import i18n from '@/i18n';
import { useNotificationStore } from '@/stores/useNotificationStore';
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
import {
  clearModelPriceFallback,
  clearModelPrices,
  loadModelPriceFallback,
  loadModelPrices,
  saveModelPriceFallback,
  type ModelPrice,
} from '@/utils/usage';
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
  /** True once the current connection scope has a successful price snapshot. */
  modelPricesLoaded: boolean;
  /** Identity of the scope the price state belongs to. */
  modelPricesScopeKey: string;
  /** True while the price read for the current scope is in flight. */
  modelPricesLoading: boolean;
  /** Re-reads prices for the current scope without dropping the snapshot. */
  reloadModelPrices: () => Promise<void>;
  apiKeyAliases: ApiKeyAlias[];
  usageServiceAvailable: boolean;
  setModelPrices: (prices: Record<string, ModelPrice>) => Promise<void>;
  loadApiKeyAliases: () => Promise<void>;
  /**
   * Runs a price sync for the current scope. Resolves `null` when another scope
   * or a newer price operation superseded it, so callers never publish a stale
   * candidate set.
   */
  syncModelPrices: (models?: string[]) => Promise<ModelPriceSyncResponse | null>;
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
  const apiBase = useAuthStore((state) => state.apiBase);
  const featureAvailability = usePanelFeatureAvailability();
  const [usage, setUsage] = useState<UsagePayload | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [lastRefreshedAt, setLastRefreshedAt] = useState<Date | null>(null);
  const [modelPrices, setModelPricesState] = useState<Record<string, ModelPrice>>({});
  const [modelPricesLoaded, setModelPricesLoaded] = useState(false);
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
  // Prices are read, migrated and written back per connection scope. A single
  // generation covers all three so a late response from another scope can
  // never publish into the current one.
  const modelPricesScopeKey = useMemo(
    () => JSON.stringify([apiBase, managementKey, modelPriceServiceBase]),
    [apiBase, managementKey, modelPriceServiceBase]
  );
  const modelPricesScopeRef = useRef(modelPricesScopeKey);
  const modelPricesRequestIdRef = useRef(0);
  const modelPricesLoadedRef = useRef(false);
  // The mount effect always starts a price read, so the first render is loading.
  const [modelPricesLoading, setModelPricesLoading] = useState(true);

  // Another connection's prices must not survive a scope switch, and any
  // in-flight read/migration/write for the old scope becomes stale.
  useLayoutEffect(() => {
    modelPricesScopeRef.current = modelPricesScopeKey;
    modelPricesRequestIdRef.current += 1;
    modelPricesLoadedRef.current = false;
    setModelPricesLoaded(false);
    setModelPricesState({});
    // The new scope is about to read its prices; keep the first-load state up
    // instead of blanking the page between the switch and the request.
    setModelPricesLoading(true);
    return () => {
      modelPricesRequestIdRef.current += 1;
    };
  }, [modelPricesScopeKey]);

  // Another connection's aliases must not survive a scope switch.
  useLayoutEffect(() => {
    aliasRequestIdRef.current += 1;
    setApiKeyAliases([]);
    return () => {
      aliasRequestIdRef.current += 1;
    };
  }, [apiBase, managementKey, modelPriceServiceBase]);

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
    if (modelPricesScopeRef.current !== modelPricesScopeKey) return;
    const requestId = modelPricesRequestIdRef.current + 1;
    modelPricesRequestIdRef.current = requestId;
    const requestScope = modelPricesScopeKey;
    const isCurrentRequest = () =>
      modelPricesRequestIdRef.current === requestId && modelPricesScopeRef.current === requestScope;
    const publish = (prices: Record<string, ModelPrice>) => {
      modelPricesLoadedRef.current = true;
      setModelPricesLoaded(true);
      setModelPricesState(prices);
    };
    const legacyPrices = loadModelPrices();
    setModelPricesLoading(true);
    try {
      const response = await getModelPricesFromApi();
      if (!isCurrentRequest()) return;
      const apiPrices = response.prices ?? {};
      if (Object.keys(apiPrices).length > 0) {
        publish(apiPrices);
        clearModelPrices();
        clearModelPriceFallback(requestScope);
        return;
      }
      if (Object.keys(legacyPrices).length > 0) {
        const migrated = await saveModelPricesToApi(legacyPrices);
        if (!isCurrentRequest()) return;
        publish(migrated.prices ?? legacyPrices);
        clearModelPrices();
        clearModelPriceFallback(requestScope);
        return;
      }
      // A successful empty result is authoritative for this scope: local edits
      // left by a failed save must neither override it nor be pushed to the
      // gateway, so they are dropped here.
      clearModelPriceFallback(requestScope);
      publish({});
    } catch {
      if (!isCurrentRequest()) return;
      // A same-scope failure keeps the last successful snapshot; only scopes
      // that never loaded fall back to local prices.
      if (!modelPricesLoadedRef.current) {
        const scopedFallback = loadModelPriceFallback(requestScope);
        setModelPricesState(Object.keys(scopedFallback).length > 0 ? scopedFallback : legacyPrices);
      }
    } finally {
      if (isCurrentRequest()) setModelPricesLoading(false);
    }
  }, [getModelPricesFromApi, modelPricesScopeKey, saveModelPricesToApi]);

  const reloadModelPrices = useCallback(async () => {
    await loadModelPricesFromStorage();
  }, [loadModelPricesFromStorage]);

  const loadApiKeyAliases = useCallback(async () => {
    const requestId = aliasRequestIdRef.current + 1;
    aliasRequestIdRef.current = requestId;
    try {
      const response = await getApiKeyAliasesFromApi();
      if (aliasRequestIdRef.current !== requestId) return;
      setApiKeyAliases(Array.isArray(response.items) ? response.items : []);
    } catch {
      if (aliasRequestIdRef.current !== requestId) return;
      useNotificationStore
        .getState()
        .showNotification(i18n.t('notification.refresh_failed'), 'error');
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
  }, [apiBase, loadApiKeyAliases, loadModelPricesFromStorage, loadUsage]);

  const setModelPrices = useCallback(
    async (prices: Record<string, ModelPrice>) => {
      // The callback may be captured by a previous connection. Never bump the
      // current generation or write to the gateway for a scope that is gone.
      if (modelPricesScopeRef.current !== modelPricesScopeKey) return;
      const requestId = modelPricesRequestIdRef.current + 1;
      modelPricesRequestIdRef.current = requestId;
      const requestScope = modelPricesScopeKey;
      const isCurrentRequest = () =>
        modelPricesRequestIdRef.current === requestId &&
        modelPricesScopeRef.current === requestScope;
      if (isCurrentRequest()) setModelPricesState(prices);
      try {
        const response = await saveModelPricesToApi(prices);
        if (!isCurrentRequest()) return;
        modelPricesLoadedRef.current = true;
        setModelPricesLoaded(true);
        setModelPricesState(response.prices ?? prices);
        clearModelPrices();
        clearModelPriceFallback(requestScope);
      } catch {
        if (!isCurrentRequest()) return;
        // Keep the failed save for this scope only. Writing the unscoped legacy
        // entry would let another connection migrate it to its own gateway.
        saveModelPriceFallback(requestScope, prices);
      } finally {
        if (isCurrentRequest()) setModelPricesLoading(false);
      }
    },
    [modelPricesScopeKey, saveModelPricesToApi]
  );

  const syncModelPrices = useCallback(
    async (models?: string[]) => {
      if (modelPricesScopeRef.current !== modelPricesScopeKey) return null;
      const requestId = modelPricesRequestIdRef.current + 1;
      modelPricesRequestIdRef.current = requestId;
      const requestScope = modelPricesScopeKey;
      const isCurrentRequest = () =>
        modelPricesRequestIdRef.current === requestId &&
        modelPricesScopeRef.current === requestScope;
      try {
        const response = await syncModelPricesFromApi(models);
        // A superseded sync must not hand its candidates to the caller: they
        // belong to the previous scope and would be saved to the current one.
        if (!isCurrentRequest()) return null;
        modelPricesLoadedRef.current = true;
        setModelPricesLoaded(true);
        setModelPricesState(response.prices ?? {});
        clearModelPrices();
        clearModelPriceFallback(requestScope);
        return response;
      } finally {
        if (isCurrentRequest()) setModelPricesLoading(false);
      }
    },
    [modelPricesScopeKey, syncModelPricesFromApi]
  );

  return {
    usage,
    loading,
    error,
    lastRefreshedAt,
    modelPrices,
    modelPricesLoaded,
    modelPricesLoading,
    modelPricesScopeKey,
    reloadModelPrices,
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
