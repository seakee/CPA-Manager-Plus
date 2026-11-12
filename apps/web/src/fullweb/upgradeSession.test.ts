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
    await expect(verifyUpgradeCredential('old-key', signal)).resolves.toEqual({
      revision: 'a'.repeat(64),
      observation: 'unsupported_upgrade',
    });
    expect(fetcher).toHaveBeenCalledWith(
      UPGRADE_AUTH_PATH,
      expect.objectContaining({
        redirect: 'error',
        mode: 'same-origin',
        credentials: 'omit',
        signal,
      })
    );
    // A proved key whose public observation was refused still authenticates; the
    // status is carried through instead of being read as a success or a refusal.
    fetcher.mockResolvedValue(
      Response.json({
        contract_version: 'cpamp.upgrade-auth.v1',
        authenticated: true,
        source_revision: 'b'.repeat(64),
        status: 'observation_unavailable',
        migration_available: false,
        expires_in_seconds: 60,
      })
    );
    await expect(verifyUpgradeCredential('old-key', signal)).resolves.toEqual({
      revision: 'b'.repeat(64),
      observation: 'observation_unavailable',
    });
    // An entry status this build cannot read is a reply it cannot read, never a
    // status it guesses at.
    fetcher.mockResolvedValue(
      Response.json({
        contract_version: 'cpamp.upgrade-auth.v1',
        authenticated: true,
        source_revision: 'b'.repeat(64),
        status: 'migration_ready',
        migration_available: false,
        expires_in_seconds: 60,
      })
    );
    await expect(verifyUpgradeCredential('old-key', signal)).rejects.toMatchObject({
      reason: 'network',
    });
    fetcher.mockResolvedValue(Response.json({ authenticated: true, migration_available: true }));
    await expect(verifyUpgradeCredential('old-key', signal)).rejects.toMatchObject({
      reason: 'network',
    });
    fetcher.mockResolvedValue(new Response('', { status: 409 }));
    await expect(verifyUpgradeCredential('old-key', signal, 'a'.repeat(64))).rejects.toMatchObject({
      reason: 'changed',
    });
    fetcher.mockResolvedValue(
      Response.json(
        {
          contract_version: 'cpamp.upgrade-auth.v1',
          error: {
            code: 'origin_rejected',
            expected_origin: 'https://public-manager.test',
          },
        },
        { status: 403 }
      )
    );
    await expect(verifyUpgradeCredential('old-key', signal)).rejects.toMatchObject({
      reason: 'origin_rejected',
      expectedOrigin: 'https://public-manager.test',
    });
    // Trailing slash on canonical origin normalizes cleanly to url.origin.
    fetcher.mockResolvedValue(
      Response.json(
        {
          contract_version: 'cpamp.upgrade-auth.v1',
          error: {
            code: 'origin_rejected',
            expected_origin: 'https://public-manager.test/',
          },
        },
        { status: 403 }
      )
    );
    await expect(verifyUpgradeCredential('old-key', signal)).rejects.toMatchObject({
      reason: 'origin_rejected',
      expectedOrigin: 'https://public-manager.test',
    });
    // Omitted expected_origin (CPAMP_PUBLIC_ORIGIN unset) and malformed/non-origin values stay origin_rejected with undefined expectedOrigin.
    for (const candidate of [
      undefined,
      '',
      'not-a-url',
      'ftp://public-manager.test',
      'https://public-manager.test/console',
      'https://user:pass@public-manager.test',
      'https://public-manager.test?x=1',
      'https://public-manager.test#frag',
      123,
    ]) {
      fetcher.mockResolvedValue(
        Response.json(
          {
            contract_version: 'cpamp.upgrade-auth.v1',
            error:
              candidate === undefined
                ? { code: 'origin_rejected' }
                : { code: 'origin_rejected', expected_origin: candidate },
          },
          { status: 403 }
        )
      );
      await expect(verifyUpgradeCredential('old-key', signal)).rejects.toMatchObject({
        reason: 'origin_rejected',
        expectedOrigin: undefined,
      });
    }
    // Non-origin_rejected 403 JSON, mismatched contract version, and non-JSON 403 fall back to unavailable.
    for (const response of [
      Response.json(
        { contract_version: 'cpamp.upgrade-auth.v1', error: { code: 'forbidden' } },
        { status: 403 }
      ),
      Response.json(
        { contract_version: 'cpamp.other.v1', error: { code: 'origin_rejected' } },
        { status: 403 }
      ),
      new Response('Forbidden', { status: 403 }),
    ]) {
      fetcher.mockResolvedValue(response);
      await expect(verifyUpgradeCredential('old-key', signal)).rejects.toMatchObject({
        reason: 'unavailable',
        expectedOrigin: undefined,
      });
    }
  });
});
