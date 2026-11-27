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

const apiClientSetConfig = vi.fn();
const fetchConfigMock = vi.fn();
const clearConfigCacheMock = vi.fn();
const clearModelsCacheMock = vi.fn();
const usageServiceGetManagerConfigMock = vi.fn();

vi.mock('@/services/api/client', () => ({
  apiClient: {
    setConfig: apiClientSetConfig,
  },
}));

vi.mock('./useConfigStore', () => ({
  useConfigStore: {
    getState: () => ({
      fetchConfig: fetchConfigMock,
      clearCache: clearConfigCacheMock,
    }),
  },
}));

vi.mock('./useModelsStore', () => ({
  useModelsStore: {
    getState: () => ({
      clearCache: clearModelsCacheMock,
    }),
  },
}));

vi.mock('@/services/api/usageService', async () => {
  const actual = await vi.importActual<typeof import('@/services/api/usageService')>(
    '@/services/api/usageService'
  );
  return {
    ...actual,
    usageServiceApi: {
      ...actual.usageServiceApi,
      getManagerConfig: usageServiceGetManagerConfigMock,
    },
  };
});

describe('useAuthStore logout', () => {
  let storage: StorageLike;

  beforeEach(() => {
    vi.resetModules();
    apiClientSetConfig.mockClear();
    fetchConfigMock.mockReset();
    clearConfigCacheMock.mockClear();
    clearModelsCacheMock.mockClear();
    usageServiceGetManagerConfigMock.mockReset();
    storage = createMemoryStorage();
    vi.stubGlobal('localStorage', storage);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it('clears usage service config and resets api client credentials', async () => {
    const { useAuthStore } = await import('./useAuthStore');
    const { useUsageServiceStore } = await import('./useUsageServiceStore');

    useUsageServiceStore.getState().setUsageServiceConfig(
      {
        enabled: true,
        serviceBase: 'http://manager.local:18317/',
      },
      {
        panelBase: 'http://panel.local:8317',
        panelHostMode: 'external_panel',
      }
    );
    useAuthStore.setState({
      isAuthenticated: true,
      apiBase: 'http://cpa.local:8317',
      managementKey: 'management-key',
      serverVersion: 'v7.2.93',
      serverCommit: '5bffd151',
      serverBuildDate: '2026-08-17',
      connectionStatus: 'connected',
    });
    storage.setItem('isLoggedIn', 'true');

    useAuthStore.getState().logout();

    expect(useUsageServiceStore.getState()).toMatchObject({
      enabled: false,
      serviceBase: '',
      panelBase: '',
      panelHostMode: '',
    });
    expect(apiClientSetConfig).toHaveBeenCalledWith({ apiBase: '', managementKey: '' });
    expect(useAuthStore.getState()).toMatchObject({
      isAuthenticated: false,
      apiBase: '',
      managementKey: '',
      serverVersion: null,
      serverCommit: null,
      serverBuildDate: null,
      connectionStatus: 'disconnected',
    });
    expect(storage.getItem('isLoggedIn')).toBeNull();
  });
});

describe('useAuthStore manager embedded login recovery', () => {
  let storage: StorageLike;

  beforeEach(() => {
    vi.resetModules();
    apiClientSetConfig.mockClear();
    fetchConfigMock.mockReset();
    clearConfigCacheMock.mockClear();
    clearModelsCacheMock.mockClear();
    usageServiceGetManagerConfigMock.mockReset();
    storage = createMemoryStorage();
    vi.stubGlobal('localStorage', storage);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it('allows Manager Server admin login to recover when the saved CPA key can no longer fetch CPA config', async () => {
    fetchConfigMock.mockRejectedValue(new Error('invalid management key'));
    usageServiceGetManagerConfigMock.mockResolvedValue({
      config: {
        cpaConnection: {
          cpaBaseUrl: 'http://cpa.local:8317',
          managementKey: 'old-cpa-key',
        },
        collector: {
          enabled: false,
          collectorMode: 'auto',
          queue: 'usage',
          popSide: 'right',
          batchSize: 100,
          pollIntervalMs: 500,
          queryLimit: 50000,
        },
        externalUsageService: {
          enabled: false,
          serviceBase: '',
        },
      },
      source: 'db',
    });

    const { useAuthStore } = await import('./useAuthStore');
    const { useUsageServiceStore } = await import('./useUsageServiceStore');

    useAuthStore.setState({
      serverVersion: 'v9.9.9',
      serverCommit: 'stale-commit',
      serverBuildDate: '2025-01-01',
    });

    const result = await useAuthStore.getState().login({
      apiBase: 'http://manager.local:18317',
      managementKey: 'manager-admin-key',
      rememberPassword: true,
      sessionMode: 'manager_embedded',
      sessionPanelBase: 'http://manager.local:18317',
    });

    expect(result).toEqual({ recoveryMode: 'manager_config' });
    expect(usageServiceGetManagerConfigMock).toHaveBeenCalledWith(
      'http://manager.local:18317',
      'manager-admin-key'
    );
    expect(useAuthStore.getState()).toMatchObject({
      isAuthenticated: true,
      apiBase: 'http://manager.local:18317',
      managementKey: 'manager-admin-key',
      sessionMode: 'manager_embedded',
      serverVersion: null,
      serverCommit: null,
      serverBuildDate: null,
      connectionStatus: 'connected',
    });
    expect(useUsageServiceStore.getState()).toMatchObject({
      enabled: true,
      serviceBase: 'http://manager.local:18317',
      panelBase: 'http://manager.local:18317',
      panelHostMode: 'manager_embedded',
    });
    expect(storage.getItem('isLoggedIn')).toBe('true');
    expect(clearConfigCacheMock).toHaveBeenCalled();
  });

  it('does not recover regular CPA panel logins through Manager Server config', async () => {
    fetchConfigMock.mockRejectedValue(new Error('invalid management key'));

    const { useAuthStore } = await import('./useAuthStore');

    await expect(
      useAuthStore.getState().login({
        apiBase: 'http://cpa.local:8317',
        managementKey: 'bad-cpa-key',
        sessionMode: 'external_panel',
      })
    ).rejects.toThrow('invalid management key');

    expect(usageServiceGetManagerConfigMock).not.toHaveBeenCalled();
    expect(useAuthStore.getState()).toMatchObject({
      isAuthenticated: false,
      connectionStatus: 'error',
    });
  });
});

describe('useAuthStore legacy cache session restoration', () => {
  let storage: StorageLike;

  const stubBrowser = (userAgent: string) => {
    vi.stubGlobal('window', {
      location: {
        host: 'panel.test:8317',
        hostname: 'panel.test',
        port: '8317',
        protocol: 'http:',
      },
      addEventListener: vi.fn(),
    });
    vi.stubGlobal('navigator', { userAgent });
  };

  const seedLegacyCache = async (values: Record<string, string>) => {
    const { obfuscatedStorage } = await import('@/services/storage/secureStorage');
    Object.entries(values).forEach(([key, value]) => obfuscatedStorage.setItem(key, value));
    storage.setItem('isLoggedIn', 'true');
    return Object.fromEntries(
      Object.keys(values).map((key) => {
        const bytes = storage.getItem(key);
        expect(bytes).toMatch(/^enc::v2::/);
        return [key, bytes];
      })
    );
  };

  const expectLegacyBytes = (original: Record<string, string | null>) => {
    Object.entries(original).forEach(([key, bytes]) => {
      expect(storage.getItem(key), `legacy ${key} bytes`).toBe(bytes);
    });
  };

  beforeEach(() => {
    vi.resetModules();
    apiClientSetConfig.mockClear();
    fetchConfigMock.mockReset();
    fetchConfigMock.mockResolvedValue({});
    clearConfigCacheMock.mockClear();
    clearModelsCacheMock.mockClear();
    usageServiceGetManagerConfigMock.mockReset();
    storage = createMemoryStorage();
    vi.stubGlobal('localStorage', storage);
    stubBrowser('Chrome/120.0.0.0 legacy-session-fixture');
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    vi.clearAllMocks();
  });

  it.each<{ name: string; values: Record<string, string>; expectedBase: string }>([
    {
      name: 'all three legacy keys, preferring apiBase over apiUrl',
      values: {
        apiBase: 'http://legacy-cpa.test:8317/v0/management/',
        apiUrl: 'http://ignored-cpa.test:8317/v0/management/',
        managementKey: 'legacy-management-key',
      },
      expectedBase: 'http://legacy-cpa.test:8317',
    },
    {
      name: 'apiUrl-only fallback',
      values: {
        apiUrl: 'http://fallback-cpa.test:8317/v0/management/',
        managementKey: 'legacy-management-key',
      },
      expectedBase: 'http://fallback-cpa.test:8317',
    },
  ])(
    'restores $name from unchanged v2 bytes after a module reload and UA change',
    async ({ values, expectedBase }) => {
      const { STORAGE_KEY_AUTH } = await import('@/utils/constants');
      const bytes = await seedLegacyCache(values);
      expect(storage.getItem(STORAGE_KEY_AUTH)).toBeNull();

      vi.resetModules();
      stubBrowser('Firefox/130.0 legacy-session-fixture');
      const { useAuthStore } = await import('./useAuthStore');
      expect(useAuthStore.persist.hasHydrated()).toBe(true);
      expect(useAuthStore.getState()).toMatchObject({
        isAuthenticated: false,
        apiBase: '',
        managementKey: '',
      });
      expect(storage.getItem(STORAGE_KEY_AUTH)).toBeNull();

      await expect(
        useAuthStore.getState().restoreSession({
          expectedMode: 'external_panel',
          expectedPanelBase: expectedBase,
        })
      ).resolves.toEqual({});
      expect(fetchConfigMock).toHaveBeenCalledWith(undefined, true);
      expect(usageServiceGetManagerConfigMock).not.toHaveBeenCalled();
      expect(apiClientSetConfig).toHaveBeenLastCalledWith({
        apiBase: expectedBase,
        managementKey: 'legacy-management-key',
      });
      expect(useAuthStore.getState()).toMatchObject({
        isAuthenticated: true,
        apiBase: expectedBase,
        managementKey: 'legacy-management-key',
        rememberPassword: true,
        sessionMode: 'external_panel',
        connectionStatus: 'connected',
      });
      expectLegacyBytes(bytes);
      if (!('apiBase' in values)) expect(storage.getItem('apiBase')).toBeNull();

      // A second real store instance hydrates the auth state created by the successful login.
      vi.resetModules();
      stubBrowser('Chrome/131.0.0.0 legacy-session-fixture');
      const { useAuthStore: reloadedStore } = await import('./useAuthStore');
      expect(reloadedStore.persist.hasHydrated()).toBe(true);
      expect(reloadedStore.getState()).toMatchObject({
        isAuthenticated: false,
        apiBase: expectedBase,
        managementKey: 'legacy-management-key',
      });
      await expect(
        reloadedStore.getState().restoreSession({
          expectedMode: 'external_panel',
          expectedPanelBase: expectedBase,
        })
      ).resolves.toEqual({});
      expect(reloadedStore.getState().isAuthenticated).toBe(true);
      expect(fetchConfigMock).toHaveBeenCalledTimes(2);
      expectLegacyBytes(bytes);
    }
  );

  it('hydrates a real persisted auth envelope and gives its nonempty credentials priority over legacy fallback', async () => {
    const { STORAGE_KEY_AUTH } = await import('@/utils/constants');
    const bytes = await seedLegacyCache({
      apiBase: 'http://legacy-cpa.test:8317',
      apiUrl: 'http://ignored-cpa.test:8317',
      managementKey: 'legacy-management-key',
    });
    const { obfuscatedStorage } = await import('@/services/storage/secureStorage');
    obfuscatedStorage.setItem(STORAGE_KEY_AUTH, {
      state: {
        apiBase: 'http://modern-cpa.test:8317',
        managementKey: 'modern-management-key',
        rememberPassword: true,
        sessionMode: 'external_panel',
        sessionPanelBase: 'http://modern-cpa.test:8317',
      },
      version: 0,
    });
    vi.resetModules();
    stubBrowser('Firefox/130.0 persisted-session-fixture');
    const { useAuthStore } = await import('./useAuthStore');
    expect(useAuthStore.persist.hasHydrated()).toBe(true);
    expect(useAuthStore.getState()).toMatchObject({
      isAuthenticated: false,
      apiBase: 'http://modern-cpa.test:8317',
      managementKey: 'modern-management-key',
    });

    await expect(
      useAuthStore.getState().restoreSession({
        expectedMode: 'external_panel',
        expectedPanelBase: 'http://modern-cpa.test:8317',
      })
    ).resolves.toEqual({});
    expect(apiClientSetConfig).toHaveBeenLastCalledWith({
      apiBase: 'http://modern-cpa.test:8317',
      managementKey: 'modern-management-key',
    });
    expect(fetchConfigMock).toHaveBeenCalledTimes(1);
    expect(useAuthStore.getState().isAuthenticated).toBe(true);
    expectLegacyBytes(bytes);
  });

  it('clears credentials without attempting login when the legacy target mismatches the expected runtime', async () => {
    const bytes = await seedLegacyCache({
      apiBase: 'http://legacy-cpa.test:8317',
      apiUrl: 'http://ignored-cpa.test:8317',
      managementKey: 'legacy-management-key',
    });
    const { useAuthStore } = await import('./useAuthStore');
    await expect(
      useAuthStore.getState().restoreSession({
        expectedMode: 'manager_embedded',
        expectedPanelBase: 'http://manager.test:18317',
      })
    ).resolves.toBe(false);
    expect(fetchConfigMock).not.toHaveBeenCalled();
    expect(usageServiceGetManagerConfigMock).not.toHaveBeenCalled();
    expect(apiClientSetConfig).toHaveBeenLastCalledWith({
      apiBase: 'http://manager.test:18317',
      managementKey: '',
    });
    expect(useAuthStore.getState()).toMatchObject({
      isAuthenticated: false,
      apiBase: 'http://manager.test:18317',
      managementKey: '',
      rememberPassword: false,
      sessionMode: 'manager_embedded',
    });
    expect(storage.getItem('isLoggedIn')).toBeNull();
    expectLegacyBytes(bytes);
  });

  it('keeps restoration unauthenticated when the decoded legacy credentials fail the real login check', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    fetchConfigMock.mockRejectedValueOnce(new Error('invalid management key'));
    const bytes = await seedLegacyCache({
      apiBase: 'http://legacy-cpa.test:8317',
      apiUrl: 'http://ignored-cpa.test:8317',
      managementKey: 'legacy-management-key',
    });
    const { useAuthStore } = await import('./useAuthStore');
    await expect(
      useAuthStore.getState().restoreSession({
        expectedMode: 'external_panel',
        expectedPanelBase: 'http://legacy-cpa.test:8317',
      })
    ).resolves.toBe(false);
    expect(fetchConfigMock).toHaveBeenCalledWith(undefined, true);
    expect(usageServiceGetManagerConfigMock).not.toHaveBeenCalled();
    expect(apiClientSetConfig).toHaveBeenLastCalledWith({
      apiBase: 'http://legacy-cpa.test:8317',
      managementKey: 'legacy-management-key',
    });
    expect(useAuthStore.getState()).toMatchObject({
      isAuthenticated: false,
      connectionStatus: 'error',
      connectionError: 'invalid management key',
    });
    expectLegacyBytes(bytes);
  });

  it.each(['：cpamp-admin-key', 'cpamp-admin-key│', 'cpamp-admin\nkey'])(
    'rejects invalid header credential %j with 401 ApiError before issuing API requests',
    async (invalidKey) => {
      // Dynamic import is required after vi.resetModules() + stubbed localStorage in beforeEach.
      const { useAuthStore } = await import('./useAuthStore');
      await expect(
        useAuthStore.getState().login({
          apiBase: 'http://manager.test:18317',
          managementKey: invalidKey,
          sessionMode: 'manager_embedded',
        })
      ).rejects.toMatchObject({
        name: 'ApiError',
        status: 401,
      });
      expect(fetchConfigMock).not.toHaveBeenCalled();
      expect(usageServiceGetManagerConfigMock).not.toHaveBeenCalled();
      expect(useAuthStore.getState()).toMatchObject({
        isAuthenticated: false,
        connectionStatus: 'error',
      });
    }
  );
});
