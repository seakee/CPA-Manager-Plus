import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  AI_PROVIDERS_WORKSPACE_UI_STORAGE_KEY,
  DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE,
  normalizeAiProvidersWorkspaceUiState,
  readAiProvidersWorkspaceUiState,
  writeAiProvidersWorkspaceUiState,
} from './aiProvidersWorkspaceUiState';

function createMemoryStorage(): Storage {
  const store = new Map<string, string>();
  return {
    get length() {
      return store.size;
    },
    clear() {
      store.clear();
    },
    getItem(key: string) {
      return store.get(key) ?? null;
    },
    key(index: number) {
      return Array.from(store.keys())[index] ?? null;
    },
    removeItem(key: string) {
      store.delete(key);
    },
    setItem(key: string, value: string) {
      store.set(key, String(value));
    },
  };
}

describe('aiProvidersWorkspaceUiState', () => {
  beforeEach(() => {
    vi.stubGlobal('window', {
      localStorage: createMemoryStorage(),
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('returns safe defaults when storage is empty or unavailable', () => {
    expect(readAiProvidersWorkspaceUiState()).toEqual(DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE);

    vi.unstubAllGlobals();
    expect(readAiProvidersWorkspaceUiState()).toEqual(DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE);
  });

  it('persists and restores valid workspace UI preferences', () => {
    writeAiProvidersWorkspaceUiState({
      layoutMode: 'grid',
      pageSize: 20,
      sortOption: 'models',
      sortDirection: 'asc',
    });

    expect(window.localStorage.getItem(AI_PROVIDERS_WORKSPACE_UI_STORAGE_KEY)).toBe(
      JSON.stringify({
        layoutMode: 'grid',
        pageSize: 20,
        sortOption: 'models',
        sortDirection: 'asc',
      })
    );
    expect(readAiProvidersWorkspaceUiState()).toEqual({
      layoutMode: 'grid',
      pageSize: 20,
      sortOption: 'models',
      sortDirection: 'asc',
    });
  });

  it('falls back field-by-field when stored JSON contains invalid values', () => {
    window.localStorage.setItem(
      AI_PROVIDERS_WORKSPACE_UI_STORAGE_KEY,
      JSON.stringify({
        layoutMode: 'masonry',
        pageSize: 999,
        sortOption: 'recent-success',
        sortDirection: 'sideways',
      })
    );

    expect(readAiProvidersWorkspaceUiState()).toEqual({
      layoutMode: 'table',
      pageSize: 10,
      sortOption: 'recent-success',
      sortDirection: 'desc',
    });
  });

  it('handles malformed JSON, non-object inputs, and throwing localStorage gracefully', () => {
    window.localStorage.setItem(AI_PROVIDERS_WORKSPACE_UI_STORAGE_KEY, '{broken');
    expect(readAiProvidersWorkspaceUiState()).toEqual(DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE);

    expect(normalizeAiProvidersWorkspaceUiState(null)).toEqual(
      DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE
    );
    expect(normalizeAiProvidersWorkspaceUiState(['grid'])).toEqual(
      DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE
    );

    vi.stubGlobal('window', {
      localStorage: {
        getItem: () => {
          throw new Error('blocked');
        },
        setItem: () => {
          throw new Error('quota');
        },
      },
    });

    expect(readAiProvidersWorkspaceUiState()).toEqual(DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE);
    expect(() =>
      writeAiProvidersWorkspaceUiState(DEFAULT_AI_PROVIDERS_WORKSPACE_UI_STATE)
    ).not.toThrow();
  });
});
