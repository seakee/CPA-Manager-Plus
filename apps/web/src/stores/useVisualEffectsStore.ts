import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type { VisualEffectsMode } from '@/types';
import { STORAGE_KEY_VISUAL_EFFECTS } from '@/utils/constants';

interface VisualEffectsState {
  mode: VisualEffectsMode;
  setMode: (mode: VisualEffectsMode) => void;
  toggleMode: () => void;
  initializeVisualEffects: () => void;
}

const isVisualEffectsMode = (mode: unknown): mode is VisualEffectsMode => {
  return mode === 'full' || mode === 'reduced';
};

const applyVisualEffects = (mode: VisualEffectsMode) => {
  if (typeof document === 'undefined') return;
  document.documentElement.setAttribute('data-visual-effects', mode);
};

export const useVisualEffectsStore = create<VisualEffectsState>()(
  persist(
    (set, get) => ({
      mode: 'full',

      setMode: (mode) => {
        applyVisualEffects(mode);
        set({ mode });
      },

      toggleMode: () => {
        const { mode, setMode } = get();
        setMode(mode === 'full' ? 'reduced' : 'full');
      },

      initializeVisualEffects: () => {
        const { mode, setMode } = get();
        setMode(isVisualEffectsMode(mode) ? mode : 'full');
      },
    }),
    {
      name: STORAGE_KEY_VISUAL_EFFECTS,
      merge: (persistedState, currentState) => {
        const nextMode = (persistedState as Partial<VisualEffectsState>)?.mode;
        if (isVisualEffectsMode(nextMode)) {
          return {
            ...currentState,
            ...(persistedState as Partial<VisualEffectsState>),
            mode: nextMode,
          };
        }
        return currentState;
      },
      onRehydrateStorage: () => (state) => {
        applyVisualEffects(isVisualEffectsMode(state?.mode) ? state.mode : 'full');
      },
    }
  )
);
