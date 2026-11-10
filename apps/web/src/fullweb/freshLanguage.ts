import { browserLanguage } from '../i18n/browserLanguage';
import { setFullWebLanguage } from '../i18n/fullweb';
import type { Language } from '../types';

// The Bootstrap entry renders before any Console chrome exists, so it cannot
// import the Console language store: that graph also carries the legacy
// endpoint table and session storage, which tests/frontendTransportIsolation
// keeps out of this entry. It reads and writes the same persisted preference
// instead — the `cli-proxy-language` payload the Console header menu writes —
// so a language chosen during setup is already selected in the Console. A first
// visit derives the language from the browser through the same mapping the
// Console uses, so the two entries never start on different languages.
export const FRESH_LANGUAGES = ['zh-CN', 'zh-TW', 'en', 'ru'] as const;

// Mirrors STORAGE_KEY_LANGUAGE. Pinned by freshLanguage.test.ts so the two
// entries can never disagree about where the language lives.
export const FRESH_LANGUAGE_STORAGE_KEY = 'cli-proxy-language';

export function isFreshLanguage(value: string): value is Language {
  return (FRESH_LANGUAGES as readonly string[]).includes(value);
}

// The Bootstrap entry's only storage touchpoint. Both entries share one key,
// and this typed accessor exposes no way to name another: a read or write of
// any other key has to reintroduce a raw `localStorage` call. The runtime gate
// in tests/frontendTransportIsolation.test.mjs records every key this entry
// actually touches while it activates and persists the language.
const languageStorage = {
  read: (): string | null => localStorage.getItem(FRESH_LANGUAGE_STORAGE_KEY),
  write: (value: string): void => localStorage.setItem(FRESH_LANGUAGE_STORAGE_KEY, value),
};

interface PersistedPayload {
  state: Record<string, unknown>;
  version: number;
}

/** The stored Console state; an absent or unreadable payload reads as empty. */
function persistedPayload(): PersistedPayload {
  const empty: PersistedPayload = { state: {}, version: 0 };
  if (typeof localStorage === 'undefined') return empty;
  let parsed: unknown;
  try {
    const raw = languageStorage.read();
    if (!raw) return empty;
    try {
      parsed = JSON.parse(raw);
    } catch {
      parsed = raw;
    }
  } catch {
    return empty;
  }
  if (typeof parsed === 'string') return { state: { language: parsed }, version: 0 };
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return empty;
  const record = parsed as Record<string, unknown>;
  const version = typeof record.version === 'number' ? record.version : 0;
  // zustand persist wraps the state; older builds stored the plain value.
  const state: unknown = 'state' in record ? record.state : record;
  if (!state || typeof state !== 'object' || Array.isArray(state)) return empty;
  return { state: { ...(state as Record<string, unknown>) }, version };
}

/** The persisted Console language, or null when nothing valid is stored. */
export function persistedFreshLanguage(): Language | null {
  const candidate = persistedPayload().state.language;
  return typeof candidate === 'string' && isFreshLanguage(candidate) ? candidate : null;
}

/**
 * Writes the same zustand persist payload the Console language store writes.
 * Only `language` belongs to this entry, so the rest of the stored state and
 * its version are carried over instead of being replaced.
 */
export function persistFreshLanguage(language: Language): void {
  if (typeof localStorage === 'undefined') return;
  try {
    const payload = persistedPayload();
    languageStorage.write(
      JSON.stringify({ state: { ...payload.state, language }, version: payload.version })
    );
  } catch {
    // A blocked storage must not stop setup; the choice still applies here.
  }
}

/** Applies the language the first render must use, and returns it. */
export function activateFreshLanguage(): Language {
  const language = persistedFreshLanguage() ?? browserLanguage();
  setFullWebLanguage(language);
  return language;
}
