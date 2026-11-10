import { fullWebLanguage, setFullWebLanguage } from '../i18n/fullweb';
import type { Language } from '../types';

// The Bootstrap entry renders before any Console chrome exists, so it cannot
// import the Console language store: that graph also carries the legacy
// endpoint table and session storage, which tests/frontendTransportIsolation
// keeps out of this entry. It reads and writes the same persisted preference
// instead — the `cli-proxy-language` payload the Console header menu writes —
// so a language chosen during setup is already selected in the Console.
export const FRESH_LANGUAGES = ['zh-CN', 'zh-TW', 'en', 'ru'] as const;

// Mirrors STORAGE_KEY_LANGUAGE. Pinned by freshLanguage.test.ts so the two
// entries can never disagree about where the language lives.
export const FRESH_LANGUAGE_STORAGE_KEY = 'cli-proxy-language';

export function isFreshLanguage(value: string): value is Language {
  return (FRESH_LANGUAGES as readonly string[]).includes(value);
}

/** The persisted Console language, or null when nothing valid is stored. */
export function persistedFreshLanguage(): Language | null {
  if (typeof localStorage === 'undefined') return null;
  try {
    const raw = localStorage.getItem(FRESH_LANGUAGE_STORAGE_KEY);
    if (!raw) return null;
    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch {
      parsed = raw;
    }
    if (typeof parsed === 'string') return isFreshLanguage(parsed) ? parsed : null;
    if (!parsed || typeof parsed !== 'object') return null;
    // zustand persist wraps the state; older builds stored the plain value.
    const state: unknown = 'state' in parsed ? parsed.state : parsed;
    if (!state || typeof state !== 'object') return null;
    const candidate: unknown = 'language' in state ? state.language : null;
    return typeof candidate === 'string' && isFreshLanguage(candidate) ? candidate : null;
  } catch {
    return null;
  }
}

/** Writes the same zustand persist payload the Console language store writes. */
export function persistFreshLanguage(language: Language): void {
  if (typeof localStorage === 'undefined') return;
  try {
    localStorage.setItem(
      FRESH_LANGUAGE_STORAGE_KEY,
      JSON.stringify({ state: { language }, version: 0 })
    );
  } catch {
    // A blocked storage must not stop setup; the choice still applies here.
  }
}

/** Applies the persisted language before the first render and returns it. */
export function activateFreshLanguage(): Language {
  const persisted = persistedFreshLanguage();
  if (persisted) {
    setFullWebLanguage(persisted);
    return persisted;
  }
  const current = fullWebLanguage();
  return isFreshLanguage(current) ? current : 'zh-CN';
}
