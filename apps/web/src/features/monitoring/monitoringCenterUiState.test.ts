import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import {
  DEFAULT_MONITORING_DATA_TAB,
  MONITORING_CENTER_UI_STATE_STORAGE_KEY,
  getDefaultMonitoringCenterUiState,
  normalizeMonitoringCenterUiState,
  normalizeMonitoringAutoRefreshMs,
  normalizeMonitoringDataTab,
  normalizeMonitoringStatusFilter,
  normalizeMonitoringTimeRange,
  readMonitoringCenterUiState,
  writeMonitoringCenterUiState,
} from './monitoringCenterUiState';

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

const originalWindow = (globalThis as { window?: unknown }).window;

describe('monitoringCenterUiState', () => {
  let storage: StorageLike;

  beforeEach(() => {
    storage = createMemoryStorage();
    (globalThis as { window?: unknown }).window = { localStorage: storage };
  });

  afterEach(() => {
    if (originalWindow === undefined) {
      delete (globalThis as { window?: unknown }).window;
    } else {
      (globalThis as { window?: unknown }).window = originalWindow;
    }
  });

  it('falls back to default tab for unknown values', () => {
    expect(normalizeMonitoringDataTab('weird')).toBe(DEFAULT_MONITORING_DATA_TAB);
    expect(normalizeMonitoringDataTab(undefined)).toBe(DEFAULT_MONITORING_DATA_TAB);
    expect(normalizeMonitoringDataTab(42)).toBe(DEFAULT_MONITORING_DATA_TAB);
  });

  it('keeps known tab ids during normalization', () => {
    expect(normalizeMonitoringDataTab('accounts')).toBe('accounts');
    expect(normalizeMonitoringDataTab('apiKeys')).toBe('apiKeys');
    expect(normalizeMonitoringDataTab('realtime')).toBe('realtime');
  });

  it('normalizes persisted filter fields', () => {
    expect(normalizeMonitoringTimeRange('30d')).toBe('30d');
    expect(normalizeMonitoringTimeRange('bad')).toBe('today');
    expect(normalizeMonitoringStatusFilter('failed')).toBe('failed');
    expect(normalizeMonitoringStatusFilter('bad')).toBe('all');
    expect(normalizeMonitoringAutoRefreshMs(30000)).toBe('30000');
    expect(normalizeMonitoringAutoRefreshMs('123')).toBe('30000');
  });

  it('normalizes ui state from arbitrary input', () => {
    expect(normalizeMonitoringCenterUiState(null)).toEqual(getDefaultMonitoringCenterUiState());
    expect(normalizeMonitoringCenterUiState({ activeDataTab: 'realtime' })).toEqual({
      ...getDefaultMonitoringCenterUiState(),
      activeDataTab: 'realtime',
    });
    expect(
      normalizeMonitoringCenterUiState({
        activeDataTab: 'nope',
        timeRange: 'custom',
        customStartInput: '2026-05-01T00:00',
        customEndInput: '2026-05-02T00:00',
        searchInput: 'gpt',
        autoRefreshMs: '60000',
        selectedAccount: 'account@example.com',
        selectedProvider: 'codex',
        selectedModel: 'gpt-5',
        selectedChannel: 'default',
        selectedApiKeyHash: 'hash',
        selectedStatus: 'failed',
        apiKeyPageSize: 50,
        realtimePageSize: 150,
      })
    ).toEqual({
      ...getDefaultMonitoringCenterUiState(),
      activeDataTab: DEFAULT_MONITORING_DATA_TAB,
      timeRange: 'custom',
      customStartInput: '2026-05-01T00:00',
      customEndInput: '2026-05-02T00:00',
      searchInput: 'gpt',
      autoRefreshMs: '60000',
      selectedAccount: 'account@example.com',
      selectedProvider: 'codex',
      selectedModel: 'gpt-5',
      selectedChannel: 'default',
      selectedApiKeyHash: 'hash',
      selectedStatus: 'failed',
      apiKeyPageSize: 50,
      realtimePageSize: 150,
    });
  });

  it('persists and reads ui state via localStorage', () => {
    writeMonitoringCenterUiState({
      activeDataTab: 'apiKeys',
      selectedProvider: 'claude',
      apiKeyPageSize: 20,
    });
    expect(JSON.parse(storage.getItem(MONITORING_CENTER_UI_STATE_STORAGE_KEY) ?? '{}')).toEqual({
      ...getDefaultMonitoringCenterUiState(),
      activeDataTab: 'apiKeys',
      selectedProvider: 'claude',
      apiKeyPageSize: 20,
    });
    expect(readMonitoringCenterUiState()).toEqual({
      ...getDefaultMonitoringCenterUiState(),
      activeDataTab: 'apiKeys',
      selectedProvider: 'claude',
      apiKeyPageSize: 20,
    });
  });

  it('returns defaults when stored payload is invalid JSON', () => {
    storage.setItem(MONITORING_CENTER_UI_STATE_STORAGE_KEY, '{not json');
    expect(readMonitoringCenterUiState()).toEqual(getDefaultMonitoringCenterUiState());
  });
});
