import { describe, expect, it, vi } from 'vitest';
import {
  buildNavPreferenceScope,
  getNavSectionsStorageKey,
  getPinnedNavStorageKey,
  MAX_PINNED_NAV_ID_LENGTH,
  MAX_PINNED_NAV_IDS,
  parseOpenNavSections,
  parsePinnedNavPreference,
  PINNED_NAV_PREFERENCE_VERSION,
  pinNavItem,
  readOpenNavSections,
  readPinnedNavIds,
  serializePinnedNavPreference,
  unpinNavItem,
  writeOpenNavSections,
  writePinnedNavIds,
} from './navPreferences';

describe('navPreferences', () => {
  it('normalizes scoped storage keys by mode and controlBasePath', () => {
    expect(buildNavPreferenceScope('/console/', 'fullweb')).toBe('fullweb:/console');
    expect(buildNavPreferenceScope('///ctrl-custom///', 'fullweb')).toBe('fullweb:/ctrl-custom');
    expect(buildNavPreferenceScope('', 'fullweb')).toBe('fullweb:/');
    expect(getPinnedNavStorageKey('/console', 'fullweb')).toBe('cpamp-nav-pinned:v1:fullweb:/console');
    expect(getPinnedNavStorageKey('/admin', 'fullweb')).not.toBe(
      getPinnedNavStorageKey('/console', 'fullweb')
    );
    expect(getNavSectionsStorageKey('', 'panel', 'legacy-sections')).toBe('legacy-sections');
    expect(getNavSectionsStorageKey('/console', 'fullweb', 'legacy-sections')).toBe(
      'cpamp-nav-sections:v1:fullweb:/console'
    );
  });

  it('parses valid v1 payloads, deduplicates, and rejects malformed or unsupported versions', () => {
    const raw = JSON.stringify({
      version: PINNED_NAV_PREFERENCE_VERSION,
      pinnedIds: [
        'resources.accounts',
        'system.logs',
        'resources.accounts',
        '',
        'has space',
        'bad\ncontrol',
        123,
        null,
        'x'.repeat(MAX_PINNED_NAV_ID_LENGTH + 1),
        'plugin:demo:/v0/resource/plugins/demo/index.html',
      ],
    });
    expect(parsePinnedNavPreference(raw)).toEqual([
      'resources.accounts',
      'system.logs',
      'plugin:demo:/v0/resource/plugins/demo/index.html',
    ]);

    expect(parsePinnedNavPreference('not-json')).toEqual([]);
    expect(parsePinnedNavPreference(JSON.stringify({ version: 2, pinnedIds: ['system.logs'] }))).toEqual([]);
    expect(parsePinnedNavPreference(JSON.stringify(['system.logs']))).toEqual([]);
    expect(parsePinnedNavPreference(JSON.stringify({ version: 1, pinnedIds: 'system.logs' }))).toEqual([]);
  });

  it('caps parsed ids at MAX_PINNED_NAV_IDS', () => {
    const ids = Array.from({ length: MAX_PINNED_NAV_IDS + 20 }, (_, i) => `item.${i}`);
    const parsed = parsePinnedNavPreference(JSON.stringify({ version: 1, pinnedIds: ids }));
    expect(parsed).toHaveLength(MAX_PINNED_NAV_IDS);
    expect(parsed[0]).toBe('item.0');
    expect(parsed[MAX_PINNED_NAV_IDS - 1]).toBe(`item.${MAX_PINNED_NAV_IDS - 1}`);
  });

  it('preserves pin chronological order, avoids reordering duplicates, and appends after re-pinning', () => {
    let ids: string[] = [];
    ids = pinNavItem(ids, 'resources.accounts');
    ids = pinNavItem(ids, 'system.logs');
    ids = pinNavItem(ids, 'resources.accounts');
    expect(ids).toEqual(['resources.accounts', 'system.logs']);

    ids = unpinNavItem(ids, 'resources.accounts');
    expect(ids).toEqual(['system.logs']);

    ids = pinNavItem(ids, 'resources.accounts');
    expect(ids).toEqual(['system.logs', 'resources.accounts']);

    expect(pinNavItem(ids, 'bad id')).toEqual(['system.logs', 'resources.accounts']);
    expect(unpinNavItem(ids, 'missing.id')).toEqual(['system.logs', 'resources.accounts']);
  });

  it('degrades gracefully when storage is unavailable or throws', () => {
    const throwingReader = {
      getItem: vi.fn(() => {
        throw new Error('storage blocked');
      }),
    };
    const throwingWriter = {
      setItem: vi.fn(() => {
        throw new Error('quota exceeded');
      }),
    };

    expect(readPinnedNavIds('key', throwingReader)).toEqual([]);
    expect(readPinnedNavIds('key', null)).toEqual([]);
    expect(writePinnedNavIds('key', ['system.logs'], throwingWriter)).toBe(false);
    expect(writePinnedNavIds('key', ['system.logs'], null)).toBe(false);
    expect(readOpenNavSections('scoped', 'legacy', throwingReader)).toEqual({});
    expect(writeOpenNavSections('scoped', { resources: true }, throwingWriter)).toBe(false);
  });

  it('reads scoped open-section preferences with legacy fallback and serializes only version+pinnedIds', () => {
    const store = new Map<string, string>([
      ['legacy-key', JSON.stringify({ resources: true, bad: 'ignored' })],
    ]);
    const adapter = {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => {
        store.set(key, value);
      },
    };

    expect(readOpenNavSections('scoped-key', 'legacy-key', adapter)).toEqual({ resources: true });
    expect(writeOpenNavSections('scoped-key', { pinned: true, system: false }, adapter)).toBe(true);
    expect(readOpenNavSections('scoped-key', 'legacy-key', adapter)).toEqual({
      pinned: true,
      system: false,
    });
    expect(parseOpenNavSections(null)).toEqual({});

    const serialized = serializePinnedNavPreference(['resources.accounts', 'resources.accounts']);
    expect(JSON.parse(serialized)).toEqual({
      version: 1,
      pinnedIds: ['resources.accounts'],
    });
  });
});
