import { beforeEach, describe, expect, it, vi } from 'vitest';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    patch: vi.fn(),
    delete: vi.fn(),
  },
}));

vi.mock('./client', () => ({
  apiClient: {
    get: mocks.get,
    post: mocks.post,
    put: mocks.put,
    patch: mocks.patch,
    delete: mocks.delete,
  },
}));

import {
  normalizePluginDeleteResult,
  normalizePluginList,
  normalizePluginStoreList,
  normalizePluginStoreInstallResult,
  pluginStoreApi,
} from './plugins';

beforeEach(() => {
  mocks.get.mockReset();
  mocks.post.mockReset();
  mocks.put.mockReset();
  mocks.patch.mockReset();
  mocks.delete.mockReset();
});

describe('plugin API normalizers', () => {
  it('normalizes plugin list responses and filters invalid entries', () => {
    const result = normalizePluginList({
      plugins_enabled: true,
      plugins_dir: 'custom-plugins',
      plugins: [
        {
          id: 'demo',
          path: '/plugins/demo',
          configured: true,
          registered: true,
          enabled: false,
          effective_enabled: true,
          supports_oauth: true,
          logo: '/plugins/demo/logo.png',
          config_fields: [
            {
              name: 'mode',
              type: 'enum',
              enum_values: ['fast', 'safe'],
              description: 'Mode',
            },
          ],
          menus: [{ path: '/plugins/demo/page', menu: 'Demo', description: 'Demo page' }],
          metadata: {
            name: 'Demo Plugin',
            version: '1.0.0',
            author: 'CPA',
            github_repository: 'owner/repo',
          },
        },
        { path: '/missing-id' },
      ],
    });

    expect(result.pluginsEnabled).toBe(true);
    expect(result.pluginsDir).toBe('custom-plugins');
    expect(result.plugins).toHaveLength(1);
    expect(result.plugins[0]).toMatchObject({
      id: 'demo',
      oauthProvider: 'demo',
      enabled: false,
      effectiveEnabled: true,
      supportsOAuth: true,
      metadata: {
        name: 'Demo Plugin',
        githubRepository: 'owner/repo',
      },
    });
    expect(result.plugins[0]?.configFields[0]?.enumValues).toEqual(['fast', 'safe']);
    expect(result.plugins[0]?.menus[0]?.path).toBe('/plugins/demo/page');
  });

  it('normalizes explicit plugin OAuth providers without legacy fallback override', () => {
    const result = normalizePluginList({
      plugins: [
        {
          id: 'legacy-plugin',
          supports_oauth: true,
          oauth_provider: 'custom-oauth-provider',
        },
        {
          id: 'no-provider',
          supports_oauth: true,
          oauth_provider: '',
        },
      ],
    });

    expect(result.plugins[0]).toMatchObject({
      id: 'legacy-plugin',
      oauthProvider: 'custom-oauth-provider',
    });
    expect(result.plugins[1]).toMatchObject({
      id: 'no-provider',
      supportsOAuth: true,
    });
    expect(result.plugins[1]?.oauthProvider).toBeUndefined();
  });

  it('normalizes plugin delete results', () => {
    expect(
      normalizePluginDeleteResult({
        status: 'deleted',
        id: 'demo',
        path: '/plugins/demo.so',
        file_deleted: true,
        configured_removed: true,
        restart_required: false,
      })
    ).toEqual({
      status: 'deleted',
      id: 'demo',
      path: '/plugins/demo.so',
      fileDeleted: true,
      configuredRemoved: true,
      restartRequired: false,
    });

    expect(
      normalizePluginDeleteResult({
        fileDeleted: true,
        configuredRemoved: false,
        restartRequired: true,
      })
    ).toMatchObject({
      fileDeleted: true,
      configuredRemoved: false,
      restartRequired: true,
    });
  });

  it('normalizes plugin store responses and install results', () => {
    const store = normalizePluginStoreList({
      pluginsEnabled: true,
      pluginsDir: 'plugins',
      sources: [
        { id: 'official', name: 'Official', url: 'https://example.test/registry.json' },
        { name: '', url: '' },
      ],
      source_errors: [
        {
          source_id: 'community',
          source_name: 'Community',
          source_url: 'https://community.test/registry.json',
          message: 'timeout',
        },
      ],
      plugins: [
        {
          store_id: 'official/demo',
          source_id: 'official',
          source_name: 'Official',
          source_url: 'https://example.test/registry.json',
          id: 'demo',
          name: 'Demo',
          install_type: 'github-release',
          auth_required: true,
          auth_configured: false,
          platforms: [{ goos: 'linux', goarch: 'amd64' }],
          installed: true,
          installedVersion: '1.0.0',
          effectiveEnabled: true,
          updateAvailable: true,
          tags: ['tool', null, ''],
        },
      ],
    });

    expect(store.pluginsEnabled).toBe(true);
    expect(store.sources).toEqual([
      { id: 'official', name: 'Official', url: 'https://example.test/registry.json' },
    ]);
    expect(store.sourceErrors).toEqual([
      {
        sourceId: 'community',
        sourceName: 'Community',
        sourceUrl: 'https://community.test/registry.json',
        message: 'timeout',
      },
    ]);
    expect(store.plugins[0]).toMatchObject({
      storeId: 'official/demo',
      sourceId: 'official',
      sourceName: 'Official',
      sourceUrl: 'https://example.test/registry.json',
      id: 'demo',
      installed: true,
      installedVersion: '1.0.0',
      effectiveEnabled: true,
      updateAvailable: true,
      tags: ['tool'],
      installType: 'github-release',
      authRequired: true,
      authConfigured: false,
      platforms: [{ goos: 'linux', goarch: 'amd64' }],
    });

    expect(
      normalizePluginStoreInstallResult({
        status: 'installed',
        source_id: 'official',
        source_name: 'Official',
        source_url: 'https://example.test/registry.json',
        id: 'demo',
        version: '1.1.0',
        install_type: 'github-release',
        path: '/plugins/demo',
        plugins_enabled: true,
        restart_required: true,
      })
    ).toEqual({
      status: 'installed',
      sourceId: 'official',
      sourceName: 'Official',
      sourceUrl: 'https://example.test/registry.json',
      id: 'demo',
      version: '1.1.0',
      installType: 'github-release',
      path: '/plugins/demo',
      pluginsEnabled: true,
      restartRequired: true,
    });
  });

  it('passes the selected source when installing from the plugin store', async () => {
    mocks.post.mockResolvedValue({
      status: 'installed',
      source_id: 'official',
      id: 'demo/plugin',
    });

    const result = await pluginStoreApi.install('demo/plugin', {
      sourceId: ' official ',
      version: ' v1.2.3 ',
    });

    expect(mocks.post).toHaveBeenCalledWith(
      '/plugin-store/demo%2Fplugin/install',
      { version: 'v1.2.3' },
      {
        params: { source: 'official', version: 'v1.2.3' },
      }
    );
    expect(result).toMatchObject({
      status: 'installed',
      sourceId: 'official',
      id: 'demo/plugin',
    });
  });

  it('preserves plugin field defaults without coercing their types', () => {
    const result = normalizePluginList({
      plugins: [
        {
          id: 'top-level',
          config_fields: [
            { name: 'on', type: 'boolean', default: true },
            { name: 'off', type: 'boolean', default: false },
            { name: 'count', type: 'integer', default: 0 },
            { name: 'options', type: 'object', default: { flags: [true, 2] } },
            { name: 'unset', type: 'string' },
          ],
        },
        {
          id: 'metadata-only',
          metadata: {
            config_fields: [{ name: 'timezone', type: 'string', default: 'Asia/Shanghai' }],
          },
        },
      ],
    });

    expect(result.plugins[0]?.configFields.map((field) => field.defaultValue)).toEqual([
      true,
      false,
      0,
      { flags: [true, 2] },
      undefined,
    ]);
    expect(result.plugins[1]?.configFields[0]?.defaultValue).toBe('Asia/Shanghai');
    expect(result.plugins[1]?.metadata?.configFields[0]?.defaultValue).toBe('Asia/Shanghai');
  });

});
