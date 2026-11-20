export const PINNED_NAV_PREFERENCE_VERSION = 1 as const;
export const PINNED_NAV_STORAGE_KEY_PREFIX = 'cpamp-nav-pinned:v1:';
export const NAV_SECTIONS_STORAGE_KEY_PREFIX = 'cpamp-nav-sections:v1:';
export const MAX_PINNED_NAV_IDS = 100;
export const MAX_PINNED_NAV_ID_LENGTH = 256;

export type NavPreferenceMode = 'fullweb' | 'panel';

export interface PinnedNavPreferencePayload {
  version: typeof PINNED_NAV_PREFERENCE_VERSION;
  pinnedIds: string[];
}

export interface StorageReader {
  getItem: (key: string) => string | null;
}

export interface StorageWriter {
  setItem: (key: string, value: string) => void;
}

function getBrowserLocalStorage(): Storage | null {
  try {
    if (typeof window !== 'undefined' && window.localStorage) {
      return window.localStorage;
    }
    if (typeof localStorage !== 'undefined') {
      return localStorage;
    }
    return null;
  } catch {
    return null;
  }
}

function hasControlOrWhitespace(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code <= 0x20 || code === 0x7f) return true;
  }
  return false;
}

export function isValidNavPinId(value: unknown): value is string {
  if (typeof value !== 'string') return false;
  if (value.length === 0 || value.length > MAX_PINNED_NAV_ID_LENGTH) return false;
  return !hasControlOrWhitespace(value);
}

export function sanitizePinnedIds(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  const result: string[] = [];
  for (const entry of value) {
    if (!isValidNavPinId(entry) || seen.has(entry)) continue;
    seen.add(entry);
    result.push(entry);
    if (result.length >= MAX_PINNED_NAV_IDS) break;
  }
  return result;
}

export function buildNavPreferenceScope(
  routeBase = '',
  mode: NavPreferenceMode = 'fullweb'
): string {
  const trimmed = routeBase.trim();
  const normalizedBase =
    !trimmed || trimmed === '/' ? '/' : `/${trimmed.replace(/^\/+|\/+$/g, '')}`;
  return `${mode}:${normalizedBase}`;
}

export function getPinnedNavStorageKey(
  routeBase = '',
  mode: NavPreferenceMode = 'fullweb'
): string {
  return `${PINNED_NAV_STORAGE_KEY_PREFIX}${buildNavPreferenceScope(routeBase, mode)}`;
}

export function getNavSectionsStorageKey(
  routeBase = '',
  mode: NavPreferenceMode = 'fullweb',
  legacyKey?: string
): string {
  if (mode === 'panel' && legacyKey) return legacyKey;
  return `${NAV_SECTIONS_STORAGE_KEY_PREFIX}${buildNavPreferenceScope(routeBase, mode)}`;
}

export function parsePinnedNavPreference(raw: string | null | undefined): string[] {
  if (!raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return [];
    const candidate = parsed as Partial<PinnedNavPreferencePayload>;
    if (candidate.version !== PINNED_NAV_PREFERENCE_VERSION) return [];
    return sanitizePinnedIds(candidate.pinnedIds);
  } catch {
    return [];
  }
}

export function serializePinnedNavPreference(pinnedIds: readonly string[]): string {
  const payload: PinnedNavPreferencePayload = {
    version: PINNED_NAV_PREFERENCE_VERSION,
    pinnedIds: sanitizePinnedIds(pinnedIds),
  };
  return JSON.stringify(payload);
}

export function readPinnedNavIds(storageKey: string, storage?: StorageReader | null): string[] {
  const target = storage === undefined ? getBrowserLocalStorage() : storage;
  if (!target) return [];
  try {
    return parsePinnedNavPreference(target.getItem(storageKey));
  } catch {
    return [];
  }
}

export function writePinnedNavIds(
  storageKey: string,
  pinnedIds: readonly string[],
  storage?: StorageWriter | null
): boolean {
  const target = storage === undefined ? getBrowserLocalStorage() : storage;
  if (!target) return false;
  try {
    target.setItem(storageKey, serializePinnedNavPreference(pinnedIds));
    return true;
  } catch {
    return false;
  }
}

export function parseOpenNavSections(raw: string | null | undefined): Record<string, boolean> {
  if (!raw) return {};
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
    return Object.fromEntries(
      Object.entries(parsed).filter(([, value]) => typeof value === 'boolean')
    );
  } catch {
    return {};
  }
}

export function readOpenNavSections(
  storageKey: string,
  fallbackLegacyKey?: string,
  storage?: StorageReader | null
): Record<string, boolean> {
  const target = storage === undefined ? getBrowserLocalStorage() : storage;
  if (!target) return {};
  try {
    const scopedRaw = target.getItem(storageKey);
    if (scopedRaw !== null) {
      return parseOpenNavSections(scopedRaw);
    }
    if (fallbackLegacyKey && fallbackLegacyKey !== storageKey) {
      return parseOpenNavSections(target.getItem(fallbackLegacyKey));
    }
    return {};
  } catch {
    return {};
  }
}

export function writeOpenNavSections(
  storageKey: string,
  sections: Record<string, boolean>,
  storage?: StorageWriter | null
): boolean {
  const target = storage === undefined ? getBrowserLocalStorage() : storage;
  if (!target) return false;
  try {
    target.setItem(storageKey, JSON.stringify(sections));
    return true;
  } catch {
    return false;
  }
}

export function pinNavItem(pinnedIds: readonly string[], id: string): string[] {
  if (!isValidNavPinId(id) || pinnedIds.includes(id)) {
    return [...pinnedIds];
  }
  if (pinnedIds.length >= MAX_PINNED_NAV_IDS) {
    return [...pinnedIds];
  }
  return [...pinnedIds, id];
}

export function unpinNavItem(pinnedIds: readonly string[], id: string): string[] {
  if (!pinnedIds.includes(id)) {
    return [...pinnedIds];
  }
  return pinnedIds.filter((item) => item !== id);
}
