import type { TFunction } from 'i18next';
import type { AuthFilesApiRequestScope } from '@/services/api';
import type { AuthFileItem } from '@/types';
import {
  buildQuotaFailureState,
  getScopedQuotaState,
  type QuotaConfig,
  type QuotaFetchContext,
} from './quotaConfigs';
import {
  captureQuotaCacheGeneration,
  commitIfQuotaCacheCurrent,
  isQuotaCacheGenerationCurrent,
} from '@/stores';

export type QuotaUpdater<T> = T | ((previous: T) => T);
export type QuotaSetter<T> = (updater: QuotaUpdater<Record<string, T>>) => void;

const quotaRefreshGenerations = new Map<string, number>();

export type QuotaRefreshResult<TState, TData> =
  | {
      status: 'success';
      data: TData;
      state: TState;
    }
  | {
      status: 'error';
      data: null;
      state: TState;
      error: string;
      errorStatus?: number;
    };

export const refreshQuotaWithConfig = async <TState, TData>({
  config,
  file,
  setQuota,
  t,
  isCurrent,
  requestScope,
  currentState,
  managerServiceBase,
  managementKey,
}: {
  config: QuotaConfig<TState, TData>;
  file: AuthFileItem;
  setQuota: QuotaSetter<TState>;
  t: TFunction;
  isCurrent: () => boolean;
  requestScope?: AuthFilesApiRequestScope;
  currentState?: TState;
  managerServiceBase?: string;
  managementKey?: string;
}): Promise<QuotaRefreshResult<TState, TData> | null> => {
  const storeKey = config.getStoreKey?.(file) ?? file.name;
  const requestKey = `${config.type}:${storeKey}`;
  const cacheGeneration = captureQuotaCacheGeneration();
  if (!isCurrent()) return null;
  const generation = (quotaRefreshGenerations.get(requestKey) ?? 0) + 1;
  quotaRefreshGenerations.set(requestKey, generation);
  const isSharedGenerationCurrent = () => quotaRefreshGenerations.get(requestKey) === generation;
  const isRefreshCurrent = () =>
    isCurrent() &&
    isSharedGenerationCurrent() &&
    isQuotaCacheGenerationCurrent(cacheGeneration);
  const commitIfRefreshCurrent = (commit: () => void) =>
    isRefreshCurrent() &&
    commitIfQuotaCacheCurrent(cacheGeneration, commit);

  // A cached quota remains the active evidence while it is being refreshed.
  // Fresh credentials still get the config-owned loading state, while an
  // existing credential keeps its previous data visible until success/failure.
  if (!currentState) {
    const loadingCommitted = commitIfRefreshCurrent(() => {
      setQuota((previous) => ({
        ...previous,
        [storeKey]: config.buildLoadingState(file),
      }));
    });
    if (!loadingCommitted) return null;
  }

  try {
    const context: QuotaFetchContext = {
      isCurrent: isRefreshCurrent,
      managerServiceBase,
      managementKey,
    };
    // Only the quota sources that CPAMP has to resolve need the Manager Server
    // connection; every other provider keeps its existing call shape.
    const data =
      config.type === 'meta' || config.type === 'plugin'
        ? await config.fetchQuota(file, t, requestScope, context)
        : await config.fetchQuota(file, t, requestScope);
    if (!isRefreshCurrent()) return null;
    let state = config.buildSuccessState(data, file, currentState);
    const committed = commitIfRefreshCurrent(() => {
      setQuota((previous) => {
        const previousState = getScopedQuotaState(config, previous, file) ?? currentState;
        state = config.buildSuccessState(data, file, previousState);
        return {
          ...previous,
          [storeKey]: state,
        };
      });
    });
    return committed ? { status: 'success', data, state } : null;
  } catch (error: unknown) {
    if (!isRefreshCurrent()) return null;
    const message = error instanceof Error ? error.message : t('common.unknown_error');
    const rawStatus =
      typeof error === 'object' && error !== null && 'status' in error
        ? Number((error as { status?: unknown }).status)
        : undefined;
    const errorStatus = Number.isFinite(rawStatus) ? rawStatus : undefined;
    let state: TState | undefined;
    const committed = commitIfRefreshCurrent(() => {
      setQuota((previous) => {
        const previousState = getScopedQuotaState(config, previous, file) ?? currentState;
        state = buildQuotaFailureState(
          config,
          message,
          errorStatus,
          file,
          previousState
        );
        return {
          ...previous,
          [storeKey]: state,
        };
      });
    });
    return committed && state
      ? { status: 'error', data: null, state, error: message, errorStatus }
      : null;
  }
};
