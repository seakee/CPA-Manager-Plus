import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  clearCPAUpdateIntent,
  cpaUpdateStorageKey,
  createCPAUpdateRequestID,
  persistCPAUpdateIntent,
  readCPAUpdateIntent,
} from './cpaUpdateIntentStorage';
import { memoryStorage, replacementArtifact, updateIntent } from './cpaUpdateTestFixtures';

const key = cpaUpdateStorageKey('https://manager.test', 'scope-secret');
let storage: Storage;
beforeEach(() => {
  storage = memoryStorage();
  vi.stubGlobal('localStorage', storage);
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('CPA browser recovery records', () => {
  it('scopes records to normalized Manager service and auth without retaining the raw key', () => {
    expect(cpaUpdateStorageKey('https://manager.test/v0/management/', 'scope-secret')).toBe(key);
    expect(cpaUpdateStorageKey('https://manager.test/', 'scope-secret')).toBe(key);
    expect(cpaUpdateStorageKey('https://other.test', 'scope-secret')).not.toBe(key);
    expect(cpaUpdateStorageKey('https://manager.test', 'other-secret')).not.toBe(key);
    expect(key).not.toContain('scope-secret');
    expect(key).toMatch(/^cpamp:cpa-update-intent:v1:[a-f0-9]{64}$/);
    persistCPAUpdateIntent(key, updateIntent(), null);
    expect(storage.getItem(key)).not.toContain('scope-secret');
    expect(
      readCPAUpdateIntent(cpaUpdateStorageKey('https://other.test', 'scope-secret'))
    ).toBeNull();
  });

  it('persists exactly the recovery fields and reads the same record back without expiring it', () => {
    const intent = updateIntent();
    persistCPAUpdateIntent(key, intent, null);
    expect(JSON.parse(storage.getItem(key)!)).toEqual(intent);
    expect(readCPAUpdateIntent(key)).toEqual(intent);
    expect(Object.keys(JSON.parse(storage.getItem(key)!))).toHaveLength(8);
    vi.spyOn(Date, 'now').mockReturnValue(Number.MAX_SAFE_INTEGER);
    expect(readCPAUpdateIntent(key)).toEqual(intent);
  });

  it.each(['write', 'read_back', 'silent_write'] as const)(
    'fails closed on %s failure',
    (failure) => {
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
      expect(() => persistCPAUpdateIntent(key, updateIntent(), null)).toThrow();
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
    JSON.stringify(updateIntent({ expected_active_artifact_id: 'not-an-artifact' })),
    JSON.stringify(updateIntent({ phase: 'activate', client_stage: 'prepared' })),
    JSON.stringify(updateIntent({ created_at_ms: -1 })),
    JSON.stringify(updateIntent({ updated_at_ms: 999 })),
    JSON.stringify(updateIntent({ updated_at_ms: 1.5 })),
    ' '.repeat(4097),
  ])('preserves corrupt/unsupported records for an explicit decision: %s', (raw) => {
    storage.setItem(key, raw);
    expect(() => readCPAUpdateIntent(key)).toThrow('corrupt');
    expect(() => persistCPAUpdateIntent(key, updateIntent(), null)).toThrow();
    expect(storage.getItem(key)).toBe(raw);
  });

  it.each([
    { request_id: 'different-id' },
    { target_version: 'v7.3.0' },
    { expected_active_artifact_id: replacementArtifact },
  ])('never changes logical identity or guards on an existing record: %o', (patch) => {
    const intent = updateIntent();
    persistCPAUpdateIntent(key, intent, null);
    expect(() => persistCPAUpdateIntent(key, { ...intent, ...patch }, intent)).toThrow('changed');
    expect(readCPAUpdateIntent(key)).toEqual(intent);
  });

  it('updates only the stage/phase and preserves the original snapshot', () => {
    const intent = updateIntent();
    persistCPAUpdateIntent(key, intent, null);
    const prepared = updateIntent({ client_stage: 'prepared', updated_at_ms: 2000 });
    persistCPAUpdateIntent(key, prepared, intent);
    const activating = updateIntent({ phase: 'activate', updated_at_ms: 3000 });
    persistCPAUpdateIntent(key, activating, prepared);
    expect(readCPAUpdateIntent(key)).toEqual(activating);
  });

  it('does not overwrite or terminal-clean another record', () => {
    const original = updateIntent();
    const other = updateIntent({ request_id: 'other-tab' });
    storage.setItem(key, JSON.stringify(other));
    expect(() => persistCPAUpdateIntent(key, original, null)).toThrow('changed');
    expect(() => clearCPAUpdateIntent(key, original)).toThrow('changed');
    expect(readCPAUpdateIntent(key)).toEqual(other);
  });

  it('verifies removal and permits explicitly forgetting a corrupt record', () => {
    storage.setItem(key, 'corrupt');
    clearCPAUpdateIntent(key);
    expect(storage.getItem(key)).toBeNull();
    storage.setItem(key, JSON.stringify(updateIntent()));
    vi.spyOn(storage, 'removeItem').mockImplementation(() => {});
    expect(() => clearCPAUpdateIntent(key, updateIntent())).toThrow('unavailable');
  });

  it('fails closed when localStorage is unavailable', () => {
    vi.stubGlobal('localStorage', undefined);
    expect(() => readCPAUpdateIntent(key)).toThrow('unavailable');
    expect(() => persistCPAUpdateIntent(key, updateIntent(), null)).toThrow('unavailable');
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
