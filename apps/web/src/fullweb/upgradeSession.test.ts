import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  clearCommittedUpgradeStatus,
  rememberCommittedUpgradeStatus,
  rememberedCommittedUpgradeStatus,
  rememberedUpgradeCredential,
  UPGRADE_COMMITTED_STATUS_KEY,
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
    // Server-validated 200 OK renewals may bridge source_revision from fallback to live worker
    // while returning a validated 64-char lowercase hex operation_revision.
    fetcher.mockResolvedValue(
      Response.json({
        contract_version: 'cpamp.upgrade-auth.v1',
        authenticated: true,
        source_revision: 'b'.repeat(64),
        operation_revision: 'c'.repeat(64),
        status: 'unsupported_upgrade',
        migration_available: false,
        expires_in_seconds: 60,
      })
    );
    await expect(verifyUpgradeCredential('old-key', signal, 'a'.repeat(64))).resolves.toEqual({
      revision: 'b'.repeat(64),
      operationRevision: 'c'.repeat(64),
      observation: 'unsupported_upgrade',
    });
    expect(fetcher).toHaveBeenLastCalledWith(
      UPGRADE_AUTH_PATH,
      expect.objectContaining({
        headers: expect.objectContaining({
          'X-CPAMP-Upgrade-Source': 'a'.repeat(64),
        }),
      })
    );
    for (const invalidOpRev of ['', 'short', 'C'.repeat(64), 'g'.repeat(64), 123, null]) {
      fetcher.mockResolvedValue(
        Response.json({
          contract_version: 'cpamp.upgrade-auth.v1',
          authenticated: true,
          source_revision: 'b'.repeat(64),
          operation_revision: invalidOpRev,
          status: 'unsupported_upgrade',
          migration_available: false,
          expires_in_seconds: 60,
        })
      );
      await expect(verifyUpgradeCredential('old-key', signal)).rejects.toMatchObject({
        reason: 'network',
      });
    }
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

  it('persists non-secret committed routing metadata in sessionStorage without credentials or execution state', () => {
    const store = new Map<string, string>();
    vi.stubGlobal('sessionStorage', {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
    });

    rememberCommittedUpgradeStatus({
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'complete',
      stageKnown: true,
      nextAction: 'complete',
      domainsDone: ['source_copy'],
      sourceKind: 'legacy',
      sourceVersion: '1.9.0',
      reconciliation: {},
      manualItems: [],
      targetMode: 'external',
      routing: {
        configured: true,
        committed: true,
        controlBasePath: '/ctl-next',
        inferenceBasePath: '',
        sharedEntry: false,
        externalBaseUrl: 'https://user:pass@cpa.example:8317/base?token=leak#frag',
      },
      accessInfo: {
        mode: 'connect_external',
        managementBasePath: '/ctl-next',
        inferenceBasePath: '',
        externalBaseUrl: 'https://user:pass@cpa.example:8317/base?token=leak',
        protocols: [],
        ready: false,
        code: 'upgrade_restart_required',
      },
      execution: {
        available: true,
        running: true,
        action: 'activate',
        managedAvailable: false,
      },
    });

    const raw = store.get(UPGRADE_COMMITTED_STATUS_KEY);
    expect(raw).toBeTruthy();
    expect(raw).not.toContain('pass');
    expect(raw).not.toContain('token=leak');
    expect(raw).not.toContain('"execution"');

    const hydrated = rememberedCommittedUpgradeStatus();
    expect(hydrated?.controlBasePath).toBe('/ctl-next');
    expect(hydrated?.snapshot.stage).toBe('complete');
    expect((hydrated?.snapshot.routing as Record<string, unknown>).externalBaseUrl).toBe(
      'https://cpa.example:8317/base'
    );

    clearCommittedUpgradeStatus();
    expect(rememberedCommittedUpgradeStatus()).toBeUndefined();
  });

  it('ignores uncommitted, incomplete, or unsafe committed status snapshots', () => {
    const store = new Map<string, string>();
    vi.stubGlobal('sessionStorage', {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
    });

    rememberCommittedUpgradeStatus({
      stage: 'verify',
      stageKnown: true,
      routing: { committed: true, controlBasePath: '/ctl-next' },
    });
    expect(store.size).toBe(0);

    rememberCommittedUpgradeStatus({
      stage: 'complete',
      stageKnown: true,
      routing: { committed: true, controlBasePath: '//evil.example/ctl' },
    });
    expect(store.size).toBe(0);

    store.set(
      UPGRADE_COMMITTED_STATUS_KEY,
      JSON.stringify({
        controlBasePath: '/other',
        snapshot: {
          stage: 'complete',
          stageKnown: true,
          routing: { committed: true, controlBasePath: '/ctl-next' },
        },
      })
    );
    expect(rememberedCommittedUpgradeStatus()).toBeUndefined();
  });

  it('scopes persisted committed status to revision and preserves or resets activationRequested explicitly', () => {
    const store = new Map<string, string>();
    vi.stubGlobal('sessionStorage', {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
    });

    const snapshot = {
      contractVersion: 'cpamp.upgrade-wizard.v1',
      recorded: true,
      stage: 'complete',
      stageKnown: true,
      nextAction: 'complete',
      domainsDone: ['source_copy'],
      sourceKind: 'cpamp_1x',
      sourceVersion: 'v1.14.10',
      reconciliation: {},
      manualItems: [],
      targetMode: 'managed',
      routing: {
        configured: true,
        committed: true,
        controlBasePath: '/ctl-next',
        inferenceBasePath: '/inf-next',
        sharedEntry: false,
      },
    };

    const revA = 'a'.repeat(64);
    const revB = 'b'.repeat(64);
    rememberCommittedUpgradeStatus(snapshot, { revision: revA, activationRequested: true });

    // Matching revision hydrates activationRequested: true.
    expect(rememberedCommittedUpgradeStatus(revA)).toMatchObject({
      controlBasePath: '/ctl-next',
      revision: revA,
      activationRequested: true,
    });

    // Subsequent status write for the same revision preserves activationRequested.
    rememberCommittedUpgradeStatus(snapshot, { revision: revA });
    expect(rememberedCommittedUpgradeStatus(revA)?.activationRequested).toBe(true);

    // Explicit reset clears activationRequested to false.
    rememberCommittedUpgradeStatus(snapshot, { revision: revA, activationRequested: false });
    expect(rememberedCommittedUpgradeStatus(revA)?.activationRequested).toBe(false);

    // New revision does not inherit activationRequested from previous revision, and mismatched read evicts storage.
    rememberCommittedUpgradeStatus(snapshot, { revision: revA, activationRequested: true });
    expect(rememberedCommittedUpgradeStatus(revB)).toBeUndefined();
    expect(store.has(UPGRADE_COMMITTED_STATUS_KEY)).toBe(false);

    // Legacy stored snapshot without revision is evicted when an expectedRevision is supplied.
    rememberCommittedUpgradeStatus(snapshot);
    expect(rememberedCommittedUpgradeStatus()).toBeDefined();
    expect(rememberedCommittedUpgradeStatus(revB)).toBeUndefined();
    expect(store.has(UPGRADE_COMMITTED_STATUS_KEY)).toBe(false);

    // Changing revision on write does not inherit prior activationRequested.
    rememberCommittedUpgradeStatus(snapshot, { revision: revA, activationRequested: true });
    rememberCommittedUpgradeStatus(snapshot, { revision: revB });
    expect(rememberedCommittedUpgradeStatus(revB)?.activationRequested).toBeUndefined();
  });

  it.each([
    '：old-key',
    'old-key│',
    'old-key\nnext',
    'old-key\x01ctrl',
    'old-key\x1fctrl',
    'old-key\x7fdel',
    ' leading-space',
    'trailing-space ',
    '\tleading-tab',
    'trailing-tab\t',
    '',
  ])(
    'rejects invalid or boundary-whitespace header credential %j as invalid authentication without calling fetch',
    async (invalidCredential) => {
      const fetcher = vi.fn();
      vi.stubGlobal('fetch', fetcher);
      await expect(
        verifyUpgradeCredential(invalidCredential, new AbortController().signal)
      ).rejects.toMatchObject({
        reason: 'invalid',
      });
      expect(fetcher).not.toHaveBeenCalled();
    }
  );

  it.each(['latin1-éß-key', 'interior space key', 'interior\ttab\tkey'])(
    'preserves valid exact-match Latin-1 and interior-whitespace credential %j on the wire',
    async (validCredential) => {
      const fetcher = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ code: 'admin_unauthorized' }), {
          status: 401,
          headers: { 'Content-Type': 'application/json' },
        })
      );
      vi.stubGlobal('fetch', fetcher);
      await expect(
        verifyUpgradeCredential(validCredential, new AbortController().signal)
      ).rejects.toMatchObject({
        reason: 'invalid',
      });
      expect(fetcher).toHaveBeenCalledOnce();
      expect(fetcher.mock.calls[0]?.[1]).toMatchObject({
        headers: expect.objectContaining({
          Authorization: `Bearer ${validCredential}`,
        }),
      });
    }
  );
});
