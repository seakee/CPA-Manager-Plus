/**
 * Generic hook for quota data fetching and management.
 */

import { useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import type { AuthFileItem } from '@/types';
import { useQuotaStore } from '@/stores';
import { getStatusFromError } from '@/utils/quota';
import {
  buildQuotaFailureState,
  getQuotaStoreKey,
  getScopedQuotaState,
  type QuotaConfig,
} from './quotaConfigs';

type QuotaScope = 'page' | 'all';

type QuotaUpdater<T> = T | ((prev: T) => T);

type QuotaSetter<T> = (updater: QuotaUpdater<T>) => void;

interface LoadQuotaResult<TData> {
  storeKey: string;
  file: AuthFileItem;
  status: 'success' | 'error';
  data?: TData;
  error?: string;
  errorStatus?: number;
}

const DEFAULT_QUOTA_REFRESH_CONCURRENCY = 4;

async function runWithConcurrencyLimit<TInput, TOutput>(
  items: TInput[],
  concurrency: number,
  worker: (item: TInput, index: number) => Promise<TOutput>
): Promise<TOutput[]> {
  if (items.length === 0) return [];

  const limit = Math.max(1, Math.min(concurrency, items.length));
  const results: TOutput[] = [];
  let nextIndex = 0;

  await Promise.all(
    Array.from({ length: limit }, async () => {
      while (nextIndex < items.length) {
        const currentIndex = nextIndex;
        nextIndex += 1;
        results[currentIndex] = await worker(items[currentIndex], currentIndex);
      }
    })
  );

  return results;
}

export function useQuotaLoader<TState, TData>(config: QuotaConfig<TState, TData>) {
  const { t } = useTranslation();
  const quota = useQuotaStore(config.storeSelector);
  const setQuota = useQuotaStore((state) => state[config.storeSetter]) as QuotaSetter<
    Record<string, TState>
  >;

  const loadingRef = useRef(false);
  const requestIdRef = useRef(0);

  const loadQuota = useCallback(
    async (
      targets: AuthFileItem[],
      scope: QuotaScope,
      setLoading: (loading: boolean, scope?: QuotaScope | null) => void
    ) => {
      if (loadingRef.current) return;
      loadingRef.current = true;
      const requestId = ++requestIdRef.current;
      setLoading(true, scope);

      try {
        if (targets.length === 0) return;

        const previousStateByStoreKey = new Map<string, TState | undefined>();
        setQuota((prev) => {
          const nextState = { ...prev };
          targets.forEach((file) => {
            const storeKey = getQuotaStoreKey(config, file);
            previousStateByStoreKey.set(storeKey, getScopedQuotaState(config, prev, file));
            nextState[storeKey] = config.buildLoadingState(file);
          });
          return nextState;
        });

        const results = await runWithConcurrencyLimit(
          targets,
          DEFAULT_QUOTA_REFRESH_CONCURRENCY,
          async (file): Promise<LoadQuotaResult<TData>> => {
            const storeKey = getQuotaStoreKey(config, file);
            try {
              const data = await config.fetchQuota(file, t);
              return { storeKey, file, status: 'success', data };
            } catch (err: unknown) {
              const message = err instanceof Error ? err.message : t('common.unknown_error');
              const errorStatus = getStatusFromError(err);
              return { storeKey, file, status: 'error', error: message, errorStatus };
            }
          }
        );

        if (requestId !== requestIdRef.current) return;

        setQuota((prev) => {
          const nextState = { ...prev };
          results.forEach((result) => {
            if (result.status === 'success') {
              nextState[result.storeKey] = config.buildSuccessState(
                result.data as TData,
                result.file
              );
            } else {
              nextState[result.storeKey] = buildQuotaFailureState(
                config,
                result.error || t('common.unknown_error'),
                result.errorStatus,
                result.file,
                previousStateByStoreKey.get(result.storeKey)
              );
            }
          });
          return nextState;
        });
      } finally {
        if (requestId === requestIdRef.current) {
          setLoading(false);
          loadingRef.current = false;
        }
      }
    },
    [config, setQuota, t]
  );

  return { quota, loadQuota };
}
