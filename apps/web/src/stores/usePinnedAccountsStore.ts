import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import {
  normalizeAccountPinnedKeys,
  toggleAccountPinnedKey,
} from '@/features/accounts/model/accountPinnedRows';

/**
 * Browser-local pin order for the accounts workspace. Pins only reorder rows and
 * never leave the device, so nothing credential-bearing is stored here.
 */
const STORAGE_KEY = 'cpa_manager_accounts_pinned_v1';

/**
 * 1.x stored a bare array of selection keys under this key. Rewrite it into the
 * persisted-store envelope before hydration so upgrading in place keeps the pins.
 */
const upgradeLegacyPinStorage = () => {
  if (typeof window === 'undefined' || !window.localStorage) return;
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return;
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return;
    window.localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ state: { pinnedKeys: normalizeAccountPinnedKeys(parsed) }, version: 0 })
    );
  } catch {
    // Unreadable storage just starts with no pins.
  }
};

upgradeLegacyPinStorage();

interface PinnedAccountsState {
  pinnedKeys: string[];
  togglePinned: (selectionKey: string) => void;
  clearPinned: () => void;
}

export const usePinnedAccountsStore = create<PinnedAccountsState>()(
  persist(
    (set) => ({
      pinnedKeys: [],
      togglePinned: (selectionKey) =>
        set((state) => ({ pinnedKeys: toggleAccountPinnedKey(state.pinnedKeys, selectionKey) })),
      clearPinned: () => set({ pinnedKeys: [] }),
    }),
    {
      name: STORAGE_KEY,
      partialize: (state) => ({ pinnedKeys: state.pinnedKeys }),
      merge: (persistedState, currentState) => ({
        ...currentState,
        pinnedKeys: normalizeAccountPinnedKeys(
          persistedState && typeof persistedState === 'object' && 'pinnedKeys' in persistedState
            ? persistedState.pinnedKeys
            : []
        ),
      }),
    }
  )
);
