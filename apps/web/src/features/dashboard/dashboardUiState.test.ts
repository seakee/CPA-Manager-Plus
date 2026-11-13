import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  DASHBOARD_UI_STATE_STORAGE_KEY,
  DEFAULT_DASHBOARD_DISPLAY_MODE,
  normalizeDashboardDisplayMode,
  readDashboardUiState,
  writeDashboardUiState,
} from './dashboardUiState';

const createMemoryStorage = (initial: Record<string, string> = {}) => {
  const store = new Map<string, string>(Object.entries(initial));
  return {
    getItem: vi.fn((key: string) => store.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => {
      store.set(key, value);
    }),
    removeItem: vi.fn((key: string) => {
      store.delete(key);
    }),
    clear: vi.fn(() => {
      store.clear();
    }),
  };
};

describe('dashboardUiState', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('defaults to advanced when storage is empty, malformed, unsupported version, or unknown mode', () => {
    const storage = createMemoryStorage({
      otherKey: 'existing-user-data',
    });
    vi.stubGlobal('window', { localStorage: storage });

    expect(readDashboardUiState()).toEqual({
      version: 1,
      displayMode: DEFAULT_DASHBOARD_DISPLAY_MODE,
    });
    expect(storage.getItem).toHaveBeenCalledTimes(1);
    expect(storage.getItem).toHaveBeenCalledWith(DASHBOARD_UI_STATE_STORAGE_KEY);

    storage.setItem(DASHBOARD_UI_STATE_STORAGE_KEY, '{corrupted-json');
    expect(readDashboardUiState().displayMode).toBe('advanced');

    storage.setItem(
      DASHBOARD_UI_STATE_STORAGE_KEY,
      JSON.stringify({ version: 2, displayMode: 'simple' })
    );
    expect(readDashboardUiState().displayMode).toBe('advanced');

    storage.setItem(
      DASHBOARD_UI_STATE_STORAGE_KEY,
      JSON.stringify({ version: 1, displayMode: 'compact' })
    );
    expect(readDashboardUiState().displayMode).toBe('advanced');

    expect(normalizeDashboardDisplayMode('simple')).toBe('simple');
    expect(normalizeDashboardDisplayMode('advanced')).toBe('advanced');
    expect(normalizeDashboardDisplayMode(null)).toBe('advanced');
  });

  it('catches SecurityError thrown directly by window.localStorage getter without crashing', () => {
    const restrictedWindow = {};
    Object.defineProperty(restrictedWindow, 'localStorage', {
      configurable: true,
      get() {
        throw new DOMException('Storage disabled', 'SecurityError');
      },
    });
    vi.stubGlobal('window', restrictedWindow);

    expect(() => readDashboardUiState()).not.toThrow();
    expect(readDashboardUiState()).toEqual({
      version: 1,
      displayMode: 'advanced',
    });
    expect(() => writeDashboardUiState('simple')).not.toThrow();
  });

  it('persists and reads back explicit displayMode updates under dashboard.uiState', () => {
    const storage = createMemoryStorage();
    vi.stubGlobal('window', { localStorage: storage });

    writeDashboardUiState('simple');
    expect(storage.setItem).toHaveBeenCalledWith(
      DASHBOARD_UI_STATE_STORAGE_KEY,
      JSON.stringify({ version: 1, displayMode: 'simple' })
    );
    expect(readDashboardUiState()).toEqual({
      version: 1,
      displayMode: 'simple',
    });

    writeDashboardUiState('advanced');
    expect(readDashboardUiState()).toEqual({
      version: 1,
      displayMode: 'advanced',
    });
  });
});
