import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  rememberedUpgradeCredential,
  verifyUpgradeCredential,
  UPGRADE_AUTH_PATH,
} from './upgradeSession';

const storage = vi.hoisted(() => new Map<string, unknown>());
vi.mock('../services/storage/secureStorage', () => ({
  obfuscatedStorage: { getItem: (key: string) => storage.get(key) },
}));
vi.mock('../stores/useAuthStore', () => ({ useAuthStore: { setState: vi.fn() } }));

describe('existing Manager credential provenance', () => {
  beforeEach(() => {
    storage.clear();
    vi.stubGlobal('window', {
      location: new URL('https://manager.test/.well-known/cpamp-bootstrap/ui'),
    });
  });
  afterEach(() => vi.unstubAllGlobals());

  it('reads old standalone and current obfuscated-store shapes only for this Manager', () => {
    storage.set('apiUrl', 'https://manager.test/');
    storage.set('managementKey', 'cmp_admin_legacy');
    expect(rememberedUpgradeCredential()).toBe('cmp_admin_legacy');
    storage.set('managementKey', 'cpamp_current-generated');
    expect(rememberedUpgradeCredential()).toBe('cpamp_current-generated');
    storage.set('cli-proxy-auth', {
      state: {
        apiBase: 'https://manager.test',
        managementKey: 'old-weak-key',
        sessionMode: 'manager_embedded',
        rememberPassword: true,
      },
    });
    expect(rememberedUpgradeCredential()).toBe('old-weak-key');
  });

  it.each([
    {
      apiBase: 'https://other.test',
      sessionMode: 'manager_embedded',
      managementKey: 'cmp_admin_other',
    },
    {
      apiBase: 'https://manager.test/cpa',
      sessionMode: 'manager_embedded',
      managementKey: 'cmp_admin_other-path',
    },
    { apiBase: 'https://manager.test', sessionMode: 'cpa_panel', managementKey: 'cmp_admin_CPA' },
    { apiBase: 'https://manager.test', managementKey: 'ambiguous-CPA-key' },
    {
      apiBase: 'https://manager.test',
      sessionMode: 'manager_embedded',
      sessionPanelBase: 'https://other.test',
      managementKey: 'cmp_admin_other',
    },
    {
      apiBase: 'https://manager.test',
      sessionMode: 'manager_embedded',
      rememberPassword: false,
      managementKey: 'cmp_admin_not-remembered',
    },
    { isAuthenticated: true, managementKey: 'cmp_admin_without-base' },
    {
      apiBase: 'https://user:pass@manager.test',
      sessionMode: 'manager_embedded',
      managementKey: 'cmp_admin_with-userinfo',
    },
  ])('does not restore untrusted or ambiguous candidate %j', (state) => {
    storage.set('cli-proxy-auth', { state });
    expect(rememberedUpgradeCredential()).toBe('');
  });

  it('does not infer legacy destination or accept a bare CPA key', () => {
    storage.set('managementKey', 'cmp_admin_missing-base');
    expect(rememberedUpgradeCredential()).toBe('');
    storage.set('apiBase', 'https://manager.test');
    storage.set('managementKey', 'CPA-management-key');
    expect(rememberedUpgradeCredential()).toBe('');
  });

  it('sends only to the fixed same-origin leaf and rejects simulated migration success', async () => {
    const fetcher = vi.fn().mockResolvedValue(
      Response.json({
        contract_version: 'cpamp.upgrade-auth.v1',
        authenticated: true,
        source_revision: 'a'.repeat(64),
        status: 'unsupported_upgrade',
        migration_available: false,
        expires_in_seconds: 60,
      })
    );
    vi.stubGlobal('fetch', fetcher);
    const signal = new AbortController().signal;
    await expect(verifyUpgradeCredential('old-key', signal)).resolves.toBe('a'.repeat(64));
    expect(fetcher).toHaveBeenCalledWith(
      UPGRADE_AUTH_PATH,
      expect.objectContaining({
        redirect: 'error',
        mode: 'same-origin',
        credentials: 'omit',
        signal,
      })
    );
    fetcher.mockResolvedValue(Response.json({ authenticated: true, migration_available: true }));
    await expect(verifyUpgradeCredential('old-key', signal)).rejects.toMatchObject({
      reason: 'network',
    });
    fetcher.mockResolvedValue(new Response('', { status: 409 }));
    await expect(verifyUpgradeCredential('old-key', signal, 'a'.repeat(64))).rejects.toMatchObject({
      reason: 'changed',
    });
  });
});
