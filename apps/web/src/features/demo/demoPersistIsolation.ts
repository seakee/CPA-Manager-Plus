import type { PersistStorage } from 'zustand/middleware';
import { useAuthStore, useUsageServiceStore } from '@/stores';

const createWriteBlockedPersistStorage = (): PersistStorage<unknown> => ({
  getItem: () => null,
  setItem: () => undefined,
  removeItem: () => undefined,
});

export const enableDemoPersistIsolation = () => {
  const authStorage = useAuthStore.persist.getOptions().storage;
  const usageServiceStorage = useUsageServiceStore.persist.getOptions().storage;
  let restored = false;

  useAuthStore.persist.setOptions({ storage: createWriteBlockedPersistStorage() });
  useUsageServiceStore.persist.setOptions({ storage: createWriteBlockedPersistStorage() });

  return () => {
    if (restored) return;
    restored = true;

    if (authStorage) {
      useAuthStore.persist.setOptions({ storage: authStorage });
    }
    if (usageServiceStorage) {
      useUsageServiceStore.persist.setOptions({ storage: usageServiceStorage });
    }
  };
};
