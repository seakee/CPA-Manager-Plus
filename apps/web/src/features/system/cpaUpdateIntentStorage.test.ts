import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  claimCPAUpdateIntent,
  clearCPAUpdateIntent,
  cpaUpdateStorageKey,
  createCPAUpdateRequestID,
  persistCPAUpdateIntent,
  readCPAUpdateIntent,
  readCPAUpdateRecord,
} from './cpaUpdateIntentStorage';
import {
  memoryLockManager,
  memoryStorage,
  replacementArtifact,
  updateIntent,
} from './cpaUpdateTestFixtures';

const key = cpaUpdateStorageKey('https://manager.test', 'scope-secret');
let storage: Storage;
beforeEach(() => {
  storage = memoryStorage();
  vi.stubGlobal('localStorage', storage);
  vi.stubGlobal('navigator', { locks: memoryLockManager() });
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('CPA browser recovery records', () => {
  it('scopes records to normalized Manager service and auth without retaining the raw key', async () => {
    expect(cpaUpdateStorageKey('https://manager.test/v0/management/', 'scope-secret')).toBe(key);
    expect(cpaUpdateStorageKey('https://manager.test/', 'scope-secret')).toBe(key);
    expect(cpaUpdateStorageKey('https://other.test', 'scope-secret')).not.toBe(key);
    expect(cpaUpdateStorageKey('https://manager.test', 'other-secret')).not.toBe(key);
    expect(key).not.toContain('scope-secret');
    expect(key).toMatch(/^cpamp:cpa-update-intent:v1:[a-f0-9]{64}$/);
    await persistCPAUpdateIntent(key, updateIntent(), null);
    expect(storage.getItem(key)).not.toContain('scope-secret');
    expect(
      readCPAUpdateIntent(cpaUpdateStorageKey('https://other.test', 'scope-secret'))
    ).toBeNull();
  });

  it('persists exactly the recovery fields and reads the same record back without expiring it', async () => {
    const intent = updateIntent();
    await persistCPAUpdateIntent(key, intent, null);
    expect(JSON.parse(storage.getItem(key)!)).toEqual(intent);
    expect(readCPAUpdateIntent(key)).toEqual(intent);
    expect(Object.keys(JSON.parse(storage.getItem(key)!))).toHaveLength(8);
    vi.spyOn(Date, 'now').mockReturnValue(Number.MAX_SAFE_INTEGER);
    expect(readCPAUpdateIntent(key)).toEqual(intent);
  });

  it.each(['write', 'read_back', 'silent_write'] as const)(
    'fails closed on %s failure',
    async (failure) => {
      const set = vi.spyOn(storage, 'setItem');
      if (failure === 'write')
        set.mockImplementation(() => {
          throw new Error('quota');
        });
      if (failure === 'silent_write') set.mockImplementation(() => {});
      if (failure === 'read_back') {
        vi.spyOn(storage, 'getItem').mockImplementation(() => {
          if (set.mock.calls.length) throw new Error('blocked');
          return null;
        });
      }
      await expect(persistCPAUpdateIntent(key, updateIntent(), null)).rejects.toThrow();
    }
  );

  it.each([
    '{bad json',
    'null',
    '[]',
    JSON.stringify(updateIntent({ schema_version: 2 as 1 })),
    JSON.stringify({ ...updateIntent(), management_key: 'secret' }),
    JSON.stringify(updateIntent({ request_id: 'bad id' })),
    JSON.stringify(updateIntent({ target_version: 'latest' })),
    JSON.stringify(updateIntent({ target_version: 'v7.2.0' })),
    JSON.stringify(updateIntent({ expected_active_artifact_id: 'not-an-artifact' })),
    JSON.stringify(updateIntent({ phase: 'activate', client_stage: 'prepared' })),
    JSON.stringify(updateIntent({ created_at_ms: -1 })),
    JSON.stringify(updateIntent({ updated_at_ms: 999 })),
    JSON.stringify(updateIntent({ updated_at_ms: 1.5 })),
    ' '.repeat(4097),
  ])('preserves corrupt/unsupported records for an explicit decision: %s', async (raw) => {
    storage.setItem(key, raw);
    expect(() => readCPAUpdateIntent(key)).toThrow('corrupt');
    await expect(persistCPAUpdateIntent(key, updateIntent(), null)).rejects.toThrow();
    expect(storage.getItem(key)).toBe(raw);
  });

  it.each([
    { request_id: 'different-id' },
    { target_version: '7.3.0' },
    { expected_active_artifact_id: replacementArtifact },
  ])('never changes logical identity or guards on an existing record: %o', async (patch) => {
    const intent = updateIntent();
    await persistCPAUpdateIntent(key, intent, null);
    await expect(persistCPAUpdateIntent(key, { ...intent, ...patch }, intent)).rejects.toThrow(
      'changed'
    );
    expect(readCPAUpdateIntent(key)).toEqual(intent);
  });

  it('updates only the stage/phase and preserves the original snapshot', async () => {
    const intent = updateIntent();
    await persistCPAUpdateIntent(key, intent, null);
    const prepared = updateIntent({ client_stage: 'prepared', updated_at_ms: 2000 });
    await persistCPAUpdateIntent(key, prepared, intent);
    const activating = updateIntent({ phase: 'activate', updated_at_ms: 3000 });
    await persistCPAUpdateIntent(key, activating, prepared);
    expect(readCPAUpdateIntent(key)).toEqual(activating);
  });

  it('does not overwrite or terminal-clean another record', async () => {
    const original = updateIntent();
    const other = updateIntent({ request_id: 'other-tab' });
    storage.setItem(key, JSON.stringify(other));
    await expect(persistCPAUpdateIntent(key, original, null)).rejects.toThrow('changed');
    await expect(clearCPAUpdateIntent(key, original)).rejects.toThrow('changed');
    expect(readCPAUpdateIntent(key)).toEqual(other);
  });

  it('verifies removal and permits explicitly forgetting a corrupt record', async () => {
    storage.setItem(key, 'corrupt');
    await clearCPAUpdateIntent(key, readCPAUpdateRecord(key));
    expect(storage.getItem(key)).toBeNull();
    storage.setItem(key, JSON.stringify(updateIntent()));
    vi.spyOn(storage, 'removeItem').mockImplementation(() => {});
    await expect(clearCPAUpdateIntent(key, updateIntent())).rejects.toThrow('unavailable');
  });

  it('fails closed when localStorage is unavailable', async () => {
    vi.stubGlobal('localStorage', undefined);
    expect(() => readCPAUpdateIntent(key)).toThrow('unavailable');
    await expect(persistCPAUpdateIntent(key, updateIntent(), null)).rejects.toThrow('unavailable');
  });

  it('grants one new intent and generates an ID only for the exclusive claim winner', async () => {
    const createA = vi.fn(() => updateIntent({ request_id: 'tab-a' }));
    const createB = vi.fn(() => updateIntent({ request_id: 'tab-b' }));
    const set = vi.spyOn(storage, 'setItem');
    const [a, b] = await Promise.all([
      claimCPAUpdateIntent(key, createA),
      claimCPAUpdateIntent(key, createB),
    ]);
    expect([a.claimed, b.claimed]).toEqual([true, false]);
    expect(a.intent).toEqual(b.intent);
    expect(createA).toHaveBeenCalledOnce();
    expect(createB).not.toHaveBeenCalled();
    expect(set).toHaveBeenCalledOnce();
  });

  it.each(['corrupt record', JSON.stringify(updateIntent({ client_stage: 'prepared' }))])(
    'never deletes a replacement record after confirming snapshot %s',
    async (raw) => {
      storage.setItem(key, raw);
      const snapshot = readCPAUpdateRecord(key);
      const next = updateIntent({ phase: 'activate', updated_at_ms: 2000 });
      storage.setItem(key, JSON.stringify(next));
      const remove = vi.spyOn(storage, 'removeItem');
      await expect(clearCPAUpdateIntent(key, snapshot)).rejects.toThrow('changed');
      expect(remove).not.toHaveBeenCalled();
      expect(readCPAUpdateIntent(key)).toEqual(next);
    }
  );

  it('blocks claims, writes, and deletion when cross-tab coordination is unavailable', async () => {
    vi.stubGlobal('navigator', {});
    vi.stubGlobal('indexedDB', undefined);
    const create = vi.fn(() => updateIntent());
    await expect(claimCPAUpdateIntent(key, create)).rejects.toThrow('coordination_unavailable');
    await expect(persistCPAUpdateIntent(key, updateIntent(), null)).rejects.toThrow(
      'coordination_unavailable'
    );
    await expect(clearCPAUpdateIntent(key, { raw: null })).rejects.toThrow(
      'coordination_unavailable'
    );
    expect(create).not.toHaveBeenCalled();
    expect(storage.length).toBe(0);
  });

  it.each([false, true])(
    'never bypasses a failing shared transaction gate (Web Locks: %s)',
    async (nativeLocks) => {
      if (!nativeLocks) vi.stubGlobal('navigator', {});
      vi.stubGlobal('indexedDB', {
        open: () => {
          throw new DOMException('Storage disabled', 'SecurityError');
        },
      });
      const create = vi.fn(() => updateIntent());
      const set = vi.spyOn(storage, 'setItem');
      const remove = vi.spyOn(storage, 'removeItem');
      await expect(claimCPAUpdateIntent(key, create)).rejects.toThrow('coordination_unavailable');
      await expect(persistCPAUpdateIntent(key, updateIntent(), null)).rejects.toThrow(
        'coordination_unavailable'
      );
      await expect(clearCPAUpdateIntent(key, { raw: null })).rejects.toThrow(
        'coordination_unavailable'
      );
      expect(create).not.toHaveBeenCalled();
      expect(set).not.toHaveBeenCalled();
      expect(remove).not.toHaveBeenCalled();
    }
  );

  // Controlled IDB events cover failure/commit boundaries only; real exclusion
  // between page realms is tested by cpaUpdateConcurrency.browser.mjs.
  function transactionEvents() {
    const acquired = {} as IDBRequest;
    const transaction = {
      objectStore: () => ({ get: () => acquired }),
      abort: vi.fn(),
    } as unknown as IDBTransaction;
    const database = {
      transaction: vi.fn(() => transaction),
      close: vi.fn(),
    } as unknown as IDBDatabase;
    const opening = { result: database } as IDBOpenDBRequest;
    vi.stubGlobal('navigator', {});
    vi.stubGlobal('indexedDB', { open: vi.fn(() => opening) });
    return {
      database,
      transaction,
      open: () => opening.onsuccess!.call(opening, new Event('success')),
      acquire: () => acquired.onsuccess!.call(acquired, new Event('success')),
      commit: () => transaction.oncomplete!.call(transaction, new Event('complete')),
      fail: () => transaction.onabort!.call(transaction, new Event('abort')),
      block: () => opening.onblocked!.call(opening, {} as IDBVersionChangeEvent),
    };
  }

  it('does not grant a fallback claim until the transaction completes', async () => {
    const events = transactionEvents();
    const granted = vi.fn();
    const claim = claimCPAUpdateIntent(key, () => updateIntent()).then(granted);
    events.open();
    events.acquire();
    await Promise.resolve();
    expect(readCPAUpdateIntent(key)).toEqual(updateIntent());
    expect(granted).not.toHaveBeenCalled();
    events.commit();
    await claim;
    expect(granted).toHaveBeenCalledWith({ intent: updateIntent(), claimed: true });
    expect(events.database.close).toHaveBeenCalledOnce();
  });

  it('keeps the record but denies submission if the transaction aborts after persistence', async () => {
    const events = transactionEvents();
    const claim = claimCPAUpdateIntent(key, () => updateIntent());
    events.open();
    events.acquire();
    events.fail();
    await expect(claim).rejects.toThrow('coordination_unavailable');
    expect(readCPAUpdateIntent(key)).toEqual(updateIntent());
    expect(events.database.close).toHaveBeenCalledOnce();
  });

  it.each(['opening', 'queued transaction'] as const)(
    'cancels fallback waiting during %s without creating an intent',
    async (stage) => {
      const events = transactionEvents();
      const controller = new AbortController();
      const create = vi.fn(() => updateIntent());
      const claim = claimCPAUpdateIntent(key, create, controller.signal);
      if (stage === 'queued transaction') events.open();
      controller.abort();
      if (stage === 'opening') events.open();
      else events.acquire();
      await expect(claim).rejects.toMatchObject({ name: 'AbortError' });
      expect(create).not.toHaveBeenCalled();
      expect(storage.length).toBe(0);
      expect(events.database.close).toHaveBeenCalledOnce();
    }
  );

  it('fails closed on a blocked database and closes a late open connection', async () => {
    const events = transactionEvents();
    const create = vi.fn(() => updateIntent());
    const claim = claimCPAUpdateIntent(key, create);
    events.block();
    events.open();
    await expect(claim).rejects.toThrow('coordination_unavailable');
    expect(create).not.toHaveBeenCalled();
    expect(events.database.close).toHaveBeenCalledOnce();
  });

  it('treats repeated terminal cleanup as a no-op while keeping stale Forget strict', async () => {
    const remove = vi.spyOn(storage, 'removeItem');
    await clearCPAUpdateIntent(key, updateIntent());
    await expect(
      clearCPAUpdateIntent(key, { raw: JSON.stringify(updateIntent()) })
    ).rejects.toThrow('changed');
    expect(remove).not.toHaveBeenCalled();
  });

  it('abandons a queued claim on scope disposal without blocking a different scope', async () => {
    const locks = memoryLockManager();
    vi.stubGlobal('navigator', { locks });
    let release!: () => void;
    const held = locks.request(
      key,
      {},
      () =>
        new Promise<void>((resolve) => {
          release = resolve;
        })
    );
    await Promise.resolve();
    await Promise.resolve();
    const controller = new AbortController();
    const create = vi.fn(() => updateIntent());
    const queued = claimCPAUpdateIntent(key, create, controller.signal);
    const independent = await claimCPAUpdateIntent(key + '-other', () =>
      updateIntent({ request_id: 'independent' })
    );
    expect(independent.claimed).toBe(true);
    controller.abort();
    release();
    await held;
    await expect(queued).rejects.toMatchObject({ name: 'AbortError' });
    expect(create).not.toHaveBeenCalled();
    expect(readCPAUpdateIntent(key)).toBeNull();
  });

  it('uses Web Crypto UUIDs and the secure random byte fallback', () => {
    const weakRandom = vi.spyOn(Math, 'random').mockImplementation(() => {
      throw new Error('weak random');
    });
    const uuid = vi.fn(() => 'b20a0788-bdfc-4128-af01-7841328e131f');
    vi.stubGlobal('crypto', { randomUUID: uuid });
    expect(createCPAUpdateRequestID()).toBe(uuid.mock.results[0].value);
    const getRandomValues = vi.fn((bytes: Uint8Array) => bytes.fill(17));
    vi.stubGlobal('crypto', { getRandomValues });
    expect(createCPAUpdateRequestID()).toBe('11'.repeat(16));
    expect(getRandomValues).toHaveBeenCalledTimes(1);
    expect(weakRandom).not.toHaveBeenCalled();
    vi.stubGlobal('crypto', {});
    expect(() => createCPAUpdateRequestID()).toThrow('secure_random_unavailable');
  });
});
