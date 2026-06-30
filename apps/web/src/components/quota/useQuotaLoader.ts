/**
 * Generic hook for quota data fetching and management.
 */

import { useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import type { AuthFileItem } from '@/types';
import { useQuotaStore } from '@/stores';
import { getStatusFromError } from '@/utils/quota';
import { getQuotaStoreKey, type QuotaConfig } from './quotaConfigs';

type QuotaScope = 'page' | 'all';

type QuotaUpdater<T> = T | ((prev: T) => T);

type QuotaSetter<T> = (updater: QuotaUpdater<T>) => void;

// Concurrency limit for bulk quota refresh. The official panel caps a single
// batch via pagination/display thresholds (25/30), which QuotaSection already
// mirrors in this repo; here we add the actual concurrency pool to avoid firing
// dozens of wham/usage requests at once and being flagged as batch behavior.
const QUOTA_REFRESH_CONCURRENCY = 5;
// Random delay (ms) before each request to further spread out the cadence.
const QUOTA_REFRESH_JITTER_MIN_MS = 80;
const QUOTA_REFRESH_JITTER_MAX_MS = 400;

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

const randomQuotaRefreshJitterMs = () =>
  QUOTA_REFRESH_JITTER_MIN_MS +
  Math.floor(
    Math.random() * (QUOTA_REFRESH_JITTER_MAX_MS - QUOTA_REFRESH_JITTER_MIN_MS + 1)
  );

interface LoadQuotaResult<TData> {
  storeKey: string;
  file: AuthFileItem;
  status: 'success' | 'error';
  data?: TData;
  error?: string;
  errorStatus?: number;
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

        setQuota((prev) => {
          const nextState = { ...prev };
          targets.forEach((file) => {
            nextState[getQuotaStoreKey(config, file)] = config.buildLoadingState(file);
          });
          return nextState;
        });

        const fetchOne = async (file: AuthFileItem): Promise<LoadQuotaResult<TData>> => {
          const storeKey = getQuotaStoreKey(config, file);
          try {
            const data = await config.fetchQuota(file, t);
            return { storeKey, file, status: 'success', data };
          } catch (err: unknown) {
            const message = err instanceof Error ? err.message : t('common.unknown_error');
            const errorStatus = getStatusFromError(err);
            return { storeKey, file, status: 'error', error: message, errorStatus };
          }
        };

        // Bounded worker pool: each worker takes one task, jitters before
        // firing, and writes results back by index to preserve order.
        const results = new Array<LoadQuotaResult<TData>>(targets.length);
        let cursor = 0;
        const runWorker = async () => {
          for (;;) {
            const index = cursor;
            cursor += 1;
            if (index >= targets.length) return;
            await sleep(randomQuotaRefreshJitterMs());
            results[index] = await fetchOne(targets[index]);
          }
        };
        const workerCount = Math.min(QUOTA_REFRESH_CONCURRENCY, targets.length);
        await Promise.all(Array.from({ length: workerCount }, () => runWorker()));

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
              nextState[result.storeKey] = config.buildErrorState(
                result.error || t('common.unknown_error'),
                result.errorStatus,
                result.file
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
