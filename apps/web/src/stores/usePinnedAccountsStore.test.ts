import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

type StorageLike = {
  getItem: (key: string) => string | null;
  setItem: (key: string, value: string) => void;
  removeItem: (key: string) => void;
  clear: () => void;
};

const STORAGE_KEY = 'cpa_manager_accounts_pinned_v1';

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

// Persisted stores hydrate while the module is evaluated, so a fresh module
// registration is the only way to observe what a reload of the app would read.
const loadStore = async () => {
  vi.resetModules();
  return import('./usePinnedAccountsStore');
};

describe('usePinnedAccountsStore persistence', () => {
  let storage: StorageLike;

  beforeEach(() => {
    storage = createMemoryStorage();
    vi.stubGlobal('localStorage', storage);
    vi.stubGlobal('window', { localStorage: storage });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('keeps pin order across a reload', async () => {
    const first = await loadStore();
    first.usePinnedAccountsStore.getState().togglePinned('codex.json\u0000auth-1');
    first.usePinnedAccountsStore.getState().togglePinned('xai.json\u0000auth-7');

    const reloaded = await loadStore();
    expect(reloaded.usePinnedAccountsStore.getState().pinnedKeys).toEqual([
      'codex.json\u0000auth-1',
      'xai.json\u0000auth-7',
    ]);
  });

  it('toggles a credential off again and stores only selection keys', async () => {
    const { usePinnedAccountsStore } = await loadStore();
    usePinnedAccountsStore.getState().togglePinned('codex.json\u0000auth-1');
    usePinnedAccountsStore.getState().togglePinned('codex.json\u0000auth-1');

    expect(usePinnedAccountsStore.getState().pinnedKeys).toEqual([]);
    const raw = storage.getItem(STORAGE_KEY) ?? '';
    expect(raw).toContain('pinnedKeys');
    expect(raw).not.toContain('@example.com');
  });

  it('adopts the pin list that 1.x wrote as a bare array', async () => {
    storage.setItem(
      STORAGE_KEY,
      JSON.stringify(['xai.json\u0000auth-7', 1, '', 'xai.json\u0000auth-7', 'codex.json\u0000auth-1'])
    );

    const { usePinnedAccountsStore } = await loadStore();
    expect(usePinnedAccountsStore.getState().pinnedKeys).toEqual([
      'xai.json\u0000auth-7',
      'codex.json\u0000auth-1',
    ]);
  });

  it('survives corrupt stored data', async () => {
    storage.setItem(STORAGE_KEY, '{broken');

    const { usePinnedAccountsStore } = await loadStore();
    expect(usePinnedAccountsStore.getState().pinnedKeys).toEqual([]);
  });
});
