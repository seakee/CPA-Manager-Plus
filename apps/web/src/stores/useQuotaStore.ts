/**
 * Quota cache that survives route switches.
 */

import { create } from 'zustand';
import { createJSONStorage, persist } from 'zustand/middleware';
import type {
  AntigravityQuotaState,
  ClaudeQuotaState,
  CodexQuotaState,
  KimiQuotaState,
  XaiQuotaState,
} from '@/types';
import { obfuscatedStorage } from '@/services/storage/secureStorage';
import { STORAGE_KEY_QUOTA_CACHE } from '@/utils/constants';

type QuotaUpdater<T> = T | ((prev: T) => T);

interface QuotaStoreState {
  cacheScope: string;
  cacheGeneration: number;
  antigravityQuota: Record<string, AntigravityQuotaState>;
  claudeQuota: Record<string, ClaudeQuotaState>;
  codexQuota: Record<string, CodexQuotaState>;
  kimiQuota: Record<string, KimiQuotaState>;
  xaiQuota: Record<string, XaiQuotaState>;
  setAntigravityQuota: (updater: QuotaUpdater<Record<string, AntigravityQuotaState>>) => void;
  setClaudeQuota: (updater: QuotaUpdater<Record<string, ClaudeQuotaState>>) => void;
  setCodexQuota: (updater: QuotaUpdater<Record<string, CodexQuotaState>>) => void;
  setKimiQuota: (updater: QuotaUpdater<Record<string, KimiQuotaState>>) => void;
  setXaiQuota: (updater: QuotaUpdater<Record<string, XaiQuotaState>>) => void;
  activateQuotaCacheScope: (scope: string) => void;
  clearQuotaCache: () => void;
}

const resolveUpdater = <T,>(updater: QuotaUpdater<T>, prev: T): T => {
  if (typeof updater === 'function') {
    return (updater as (value: T) => T)(prev);
  }
  return updater;
};

const emptyQuotaState = {
  antigravityQuota: {},
  claudeQuota: {},
  codexQuota: {},
  kimiQuota: {},
  xaiQuota: {},
};

const quotaStateForScope = (cacheScope: string, cacheGeneration: number) => ({
  cacheScope,
  cacheGeneration,
  ...emptyQuotaState,
});

const filterPersistableCodexQuota = (
  quota: Record<string, CodexQuotaState> | undefined
): Record<string, CodexQuotaState> => {
  if (!quota) return {};

  return Object.fromEntries(
    Object.entries(quota).filter(([, item]) => {
      return item?.status === 'success' && item.observedFromUsageHeaders !== true;
    })
  );
};

export const useQuotaStore = create<QuotaStoreState>()(
  persist(
    (set) => ({
      cacheScope: '',
      cacheGeneration: 0,
      ...emptyQuotaState,
      setAntigravityQuota: (updater) =>
        set((state) => ({
          antigravityQuota: resolveUpdater(updater, state.antigravityQuota),
        })),
      setClaudeQuota: (updater) =>
        set((state) => ({
          claudeQuota: resolveUpdater(updater, state.claudeQuota),
        })),
      setCodexQuota: (updater) =>
        set((state) => ({
          codexQuota: resolveUpdater(updater, state.codexQuota),
        })),
      setKimiQuota: (updater) =>
        set((state) => ({
          kimiQuota: resolveUpdater(updater, state.kimiQuota),
        })),
      setXaiQuota: (updater) =>
        set((state) => ({
          xaiQuota: resolveUpdater(updater, state.xaiQuota),
        })),
      activateQuotaCacheScope: (scope) =>
        set((state) => {
          const nextScope = scope.trim();
          if (state.cacheScope === nextScope) return state;
          return quotaStateForScope(nextScope, state.cacheGeneration + 1);
        }),
      clearQuotaCache: () =>
        set((state) => quotaStateForScope('', state.cacheGeneration + 1)),
    }),
    {
      name: STORAGE_KEY_QUOTA_CACHE,
      storage: createJSONStorage(() => ({
        getItem: (name) => {
          if (typeof localStorage === 'undefined') return null;
          const data = obfuscatedStorage.getItem<Partial<QuotaStoreState>>(name);
          return data ? JSON.stringify(data) : null;
        },
        setItem: (name, value) => {
          if (typeof localStorage === 'undefined') return;
          obfuscatedStorage.setItem(name, JSON.parse(value));
        },
        removeItem: (name) => {
          if (typeof localStorage === 'undefined') return;
          obfuscatedStorage.removeItem(name);
        },
      })),
      partialize: (state) => ({
        cacheScope: state.cacheScope,
        codexQuota: filterPersistableCodexQuota(state.codexQuota),
      }),
      merge: (persistedState, currentState) => {
        const persisted = persistedState as Partial<QuotaStoreState> | undefined;
        return {
          ...currentState,
          cacheScope: typeof persisted?.cacheScope === 'string' ? persisted.cacheScope : '',
          codexQuota: filterPersistableCodexQuota(persisted?.codexQuota),
        };
      },
    }
  )
);

export const captureQuotaCacheGeneration = (): number =>
  useQuotaStore.getState().cacheGeneration;

export const commitIfQuotaCacheCurrent = (
  generation: number,
  commit: () => void
): boolean => {
  if (useQuotaStore.getState().cacheGeneration !== generation) return false;
  commit();
  return true;
};
