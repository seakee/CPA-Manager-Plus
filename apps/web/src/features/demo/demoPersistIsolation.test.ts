import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

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

const USAGE_SERVICE_STORAGE_KEY = 'cli-proxy-usage-service';

describe('demo persist isolation', () => {
  let storage: StorageLike;

  beforeEach(() => {
    vi.resetModules();
    storage = createMemoryStorage();
    vi.stubGlobal('localStorage', storage);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('keeps demo auth and usage-service state out of persisted storage', async () => {
    const { STORAGE_KEY_AUTH } = await import('@/utils/constants');
    const { obfuscatedStorage } = await import('@/services/storage/secureStorage');

    obfuscatedStorage.setItem(STORAGE_KEY_AUTH, {
      state: {
        apiBase: 'http://real.local:18317',
        managementKey: 'real-management-key',
        rememberPassword: true,
        serverVersion: 'v7.1.18',
        serverBuildDate: '2026-06-30',
        sessionMode: 'manager_embedded',
        sessionPanelBase: 'http://real.local:18317',
      },
    });
    obfuscatedStorage.setItem(USAGE_SERVICE_STORAGE_KEY, {
      state: {
        enabled: true,
        serviceBase: 'http://real.local:18317',
        panelBase: 'http://real.local:18317',
        panelHostMode: 'manager_embedded',
      },
    });

    const { useAuthStore, useUsageServiceStore } = await import('@/stores');
    const { enableDemoPersistIsolation } = await import('./demoPersistIsolation');
    const restorePersistIsolation = enableDemoPersistIsolation();

    useAuthStore.setState({
      isAuthenticated: true,
      apiBase: 'http://demo.local',
      managementKey: 'demo-management-key',
      rememberPassword: false,
      serverVersion: 'v7.1.18-demo',
      serverBuildDate: '2026-06-30',
      sessionMode: 'manager_embedded',
      sessionPanelBase: 'http://demo.local',
      connectionStatus: 'connected',
      connectionError: null,
    });
    useUsageServiceStore.setState({
      enabled: true,
      serviceBase: 'http://demo.local',
      panelBase: 'http://demo.local',
      panelHostMode: 'manager_embedded',
    });

    expect(obfuscatedStorage.getItem<{ state?: { apiBase?: string } }>(STORAGE_KEY_AUTH)?.state)
      .toMatchObject({
        apiBase: 'http://real.local:18317',
        managementKey: 'real-management-key',
      });
    expect(
      obfuscatedStorage.getItem<{ state?: { serviceBase?: string } }>(USAGE_SERVICE_STORAGE_KEY)
        ?.state
    ).toMatchObject({
      serviceBase: 'http://real.local:18317',
      panelBase: 'http://real.local:18317',
    });

    restorePersistIsolation();

    useAuthStore.setState({
      apiBase: 'http://next.local:18317',
      managementKey: 'next-management-key',
      rememberPassword: true,
      sessionMode: 'manager_embedded',
      sessionPanelBase: 'http://next.local:18317',
    });

    expect(obfuscatedStorage.getItem<{ state?: { apiBase?: string } }>(STORAGE_KEY_AUTH)?.state)
      .toMatchObject({
        apiBase: 'http://next.local:18317',
        managementKey: 'next-management-key',
      });
  });
});
