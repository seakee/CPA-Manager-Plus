import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PluginListEntry, PluginStoreEntry } from '@/types';
import {
  collectPluginResourceEntries,
  getPluginConfirmToken,
  getPluginRepositorySlug,
  isDefaultPluginStoreSource,
  isOfficialPlugin,
  isOfficialRepository,
  isPluginManagementNavVisible,
  isPluginResourceNavVisible,
  isSupportedPluginResourcePath,
  resolvePluginAssetURL,
} from './pluginResources';

const createPlugin = (patch: Partial<PluginListEntry> = {}): PluginListEntry => ({
  id: 'demo-plugin',
  path: 'plugins/demo-plugin.so',
  configured: true,
  registered: true,
  enabled: true,
  effectiveEnabled: true,
  supportsOAuth: false,
  logo: '',
  configFields: [],
  menus: [
    {
      path: '/v0/resource/plugins/demo-plugin/page',
      menu: 'Demo Plugin',
      description: 'Demo plugin page',
    },
  ],
  metadata: {
    name: 'Demo Plugin',
    version: '1.0.0',
    author: 'router-for-me',
    githubRepository: 'router-for-me/demo-plugin',
    logo: '',
    configFields: [],
  },
  ...patch,
});

const createStoreEntry = (patch: Partial<PluginStoreEntry> = {}): PluginStoreEntry => ({
  storeId: 'official/demo-plugin',
  sourceId: 'official',
  sourceName: 'official',
  sourceUrl: 'https://example.test/registry.json',
  id: 'demo-plugin',
  name: 'Demo Plugin',
  description: '',
  author: '',
  version: '1.0.0',
  repository: 'router-for-me/demo-plugin',
  installType: '',
  authRequired: false,
  authConfigured: false,
  platforms: [],
  logo: '',
  homepage: '',
  license: '',
  tags: [],
  installed: false,
  installedVersion: '',
  path: '',
  configured: false,
  registered: false,
  enabled: false,
  effectiveEnabled: false,
  updateAvailable: false,
  ...patch,
});

describe('plugin resource helpers', () => {
  it('keeps plugin management reachable while the global switch is off', () => {
    // Visibility depends on deployment capability alone: the switch that turns
    // plugins back on lives on this page, so the page stays reachable whether
    // the global switch is on, off, or not yet known.
    expect(isPluginManagementNavVisible({ supportsPlugin: true })).toBe(true);
    expect(isPluginManagementNavVisible({ supportsPlugin: false })).toBe(false);
  });

  it('shows plugin resource nav only when supported and globally enabled', () => {
    expect(
      isPluginResourceNavVisible({ supportsPlugin: true, pluginsEnabled: true })
    ).toBe(true);
    expect(
      isPluginResourceNavVisible({ supportsPlugin: true, pluginsEnabled: false })
    ).toBe(false);
    expect(isPluginResourceNavVisible({ supportsPlugin: true })).toBe(false);
    expect(
      isPluginResourceNavVisible({ supportsPlugin: false, pluginsEnabled: true })
    ).toBe(false);
  });

  it('collects only effective plugin menus with resource paths', () => {
    const entries = collectPluginResourceEntries([
      createPlugin(),
      createPlugin({
        id: 'inactive-plugin',
        effectiveEnabled: false,
        menus: [
          {
            path: '/v0/resource/plugins/inactive/page',
            menu: 'Inactive',
            description: '',
          },
        ],
      }),
      createPlugin({
        id: 'empty-menu-plugin',
        menus: [
          {
            path: '',
            menu: 'Empty Menu',
            description: '',
          },
        ],
      }),
    ]);

    expect(entries).toHaveLength(1);
    expect(entries[0]).toMatchObject({
      pluginID: 'demo-plugin',
      pluginTitle: 'Demo Plugin',
      label: 'Demo Plugin',
      route: '/plugin-pages/demo-plugin/0',
    });
  });

  it('normalizes repository slugs for plugin install confirmation', () => {
    expect(getPluginRepositorySlug('router-for-me/demo.git')).toBe('router-for-me/demo');
    expect(getPluginRepositorySlug('https://github.com/owner/repo.git')).toBe('owner/repo');
    expect(getPluginConfirmToken(createStoreEntry({ repository: '' }))).toBe('demo-plugin');
  });

  it('detects only official router-for-me GitHub repositories as first-party', () => {
    expect(isOfficialRepository('router-for-me/demo')).toBe(true);
    expect(isOfficialRepository('https://github.com/router-for-me/demo')).toBe(true);
    expect(isOfficialRepository('https://github.com.evil.test/router-for-me/demo')).toBe(false);
    expect(isOfficialRepository('other/demo')).toBe(false);
    expect(isOfficialPlugin(createStoreEntry())).toBe(true);
    expect(isOfficialPlugin(createStoreEntry({ sourceId: 'third-party' }))).toBe(false);
    expect(isOfficialPlugin(createStoreEntry({ repository: 'other/demo' }))).toBe(false);
  });

  it('recognizes the default plugin store source', () => {
    expect(isDefaultPluginStoreSource(createStoreEntry({ sourceId: 'official' }))).toBe(true);
    expect(isDefaultPluginStoreSource(createStoreEntry({ sourceId: '', sourceName: 'Official' }))).toBe(
      true
    );
    expect(isDefaultPluginStoreSource(createStoreEntry({ sourceId: 'custom', sourceName: 'Custom' }))).toBe(
      false
    );
  });
});

describe('plugin resource URL boundary', () => {
  const ORIGIN = 'https://admin.example.test';
  const API_BASE = `${ORIGIN}/api/v2/cpamp`;

  beforeEach(() => {
    vi.stubGlobal('window', { location: { origin: ORIGIN } });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('accepts only the deployment plugin namespace', () => {
    for (const supported of [
      '/v0/resource/plugins/demo/index.html',
      '/v0/resource/plugins/demo/nested/page.html?theme=dark',
      '/v0/resource/plugins/demo/logo.png',
    ]) {
      expect(isSupportedPluginResourcePath(supported), supported).toBe(true);
    }

    for (const rejected of [
      '',
      '/',
      '/v0/resource/plugins',
      '/v0/resource/plugins/',
      'v0/resource/plugins/demo/index.html',
      '/api/v2/cpamp/v0/resource/plugins/demo/index.html',
      'https://elsewhere.test/v0/resource/plugins/demo/index.html',
      '//elsewhere.test/v0/resource/plugins/demo/index.html',
      'data:text/html,<p>plugin</p>',
      'blob:https://admin.example.test/1234',
      'javascript:alert(1)',
      '/v0/resource/plugins/../management.html',
      '/v0/resource/plugins/demo/../../management.html',
      '/v0/resource/plugins/%2e%2e/management.html',
      '/v0/resource/plugins%2Fdemo/index.html',
      '/v0/resource/plugins//demo/index.html',
      '/v0/resource/plugins/demo\\index.html',
      '/v0/resource/plugins/demo/a b.html',
      '/v0/resource/plugins/demo/http://elsewhere.test/a.html',
    ]) {
      expect(isSupportedPluginResourcePath(rejected), JSON.stringify(rejected)).toBe(false);
    }
  });

  it('normalizes surrounding whitespace instead of passing it into the URL', () => {
    expect(
      resolvePluginAssetURL('  /v0/resource/plugins/demo/index.html  ', API_BASE)
    ).toBe(`${ORIGIN}/v0/resource/plugins/demo/index.html`);
  });

  it('resolves supported resources against the document origin, never the API prefix', () => {
    // The Manager proxy answers the namespace at the origin root, so the
    // console's `/api/v2/cpamp` namespace must not appear in the URL.
    expect(resolvePluginAssetURL('/v0/resource/plugins/demo/index.html', API_BASE)).toBe(
      `${ORIGIN}/v0/resource/plugins/demo/index.html`
    );
    expect(resolvePluginAssetURL('/v0/resource/plugins/demo/logo.png', '')).toBe(
      `${ORIGIN}/v0/resource/plugins/demo/logo.png`
    );
    expect(
      resolvePluginAssetURL('/v0/resource/plugins/demo/index.html', '/api/v2/cpamp')
    ).toBe(`${ORIGIN}/v0/resource/plugins/demo/index.html`);
    expect(
      resolvePluginAssetURL('/v0/resource/plugins/demo/index.html', '/console/api/v2/cpamp')
    ).toBe(`${ORIGIN}/v0/resource/plugins/demo/index.html`);
    expect(
      resolvePluginAssetURL('/v0/resource/plugins/demo/index.html', '/v0/management')
    ).toBe(`${ORIGIN}/v0/resource/plugins/demo/index.html`);
  });

  it('reports unsupported targets instead of producing a loadable URL', () => {
    for (const rejected of [
      'https://cdn.example.test/logo.png',
      'data:image/png;base64,AAAA',
      'blob:https://admin.example.test/logo',
      '/assets/logo.png',
      'demo/logo.png',
    ]) {
      expect(resolvePluginAssetURL(rejected, API_BASE), rejected).toBe('');
    }
  });

  it('refuses a configured API base on another origin', () => {
    // A cross-origin Manager cannot host a plugin document this admin
    // document may frame (frame-src 'self'), so it is unsupported outright.
    expect(
      resolvePluginAssetURL(
        '/v0/resource/plugins/demo/index.html',
        'https://manager.elsewhere.test/api/v2/cpamp'
      )
    ).toBe('');
    expect(resolvePluginAssetURL('/v0/resource/plugins/demo/index.html', 'not a url')).toBe('');
    expect(
      resolvePluginAssetURL(
        '/v0/resource/plugins/demo/index.html',
        '//manager.elsewhere.test/api/v2/cpamp'
      )
    ).toBe('');
    expect(
      resolvePluginAssetURL(
        '/v0/resource/plugins/demo/index.html',
        '//admin.example.test/api/v2/cpamp'
      )
    ).toBe('');
  });

  it('refuses to resolve anything without a browser origin', () => {
    vi.unstubAllGlobals();
    expect(resolvePluginAssetURL('/v0/resource/plugins/demo/index.html', API_BASE)).toBe('');
  });
});
