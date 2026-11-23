import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { STORAGE_KEY_VISUAL_EFFECTS } from '@/utils/constants';

type StorageLike = {
  getItem: (key: string) => string | null;
  setItem: (key: string, value: string) => void;
  removeItem: (key: string) => void;
  clear: () => void;
};

const createMemoryStorage = (): StorageLike => {
  const store = new Map<string, string>();
  return {
    getItem: (key) => (store.has(key) ? (store.get(key) as string) : null),
    setItem: (key, value) => {
      store.set(key, value);
    },
    removeItem: (key) => {
      store.delete(key);
    },
    clear: () => {
      store.clear();
    },
  };
};

describe('useVisualEffectsStore', () => {
  let storage: StorageLike;
  let attrs: Map<string, string>;

  beforeEach(() => {
    storage = createMemoryStorage();
    attrs = new Map<string, string>();
    vi.stubGlobal('localStorage', storage);
    vi.stubGlobal('window', { localStorage: storage });
    vi.stubGlobal('document', {
      documentElement: {
        setAttribute: (name: string, value: string) => {
          attrs.set(name, value);
        },
        getAttribute: (name: string) => attrs.get(name) ?? null,
      },
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

    // Re-evaluating the module after stubbing storage tests persisted hydration on load.
  it('syncs data-visual-effects immediately when persisted reduced mode rehydrates', async () => {
    storage.setItem(
      STORAGE_KEY_VISUAL_EFFECTS,
      JSON.stringify({ state: { mode: 'reduced' }, version: 0 })
    );

    vi.resetModules();
    const { useVisualEffectsStore } = await import('./useVisualEffectsStore');

    expect(useVisualEffectsStore.getState().mode).toBe('reduced');
    expect(attrs.get('data-visual-effects')).toBe('reduced');
  });

  it('toggles between full and reduced and updates the document attribute', async () => {
    vi.resetModules();
    const { useVisualEffectsStore } = await import('./useVisualEffectsStore');

    expect(useVisualEffectsStore.getState().mode).toBe('full');
    expect(attrs.get('data-visual-effects')).toBe('full');

    useVisualEffectsStore.getState().toggleMode();
    expect(useVisualEffectsStore.getState().mode).toBe('reduced');
    expect(attrs.get('data-visual-effects')).toBe('reduced');

    useVisualEffectsStore.getState().toggleMode();
    expect(useVisualEffectsStore.getState().mode).toBe('full');
    expect(attrs.get('data-visual-effects')).toBe('full');
  });
});
