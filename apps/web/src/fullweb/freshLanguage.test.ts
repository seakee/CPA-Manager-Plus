import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getInitialLanguage } from '../utils/language';
import { fullWebLanguage, setFullWebLanguage } from '../i18n/fullweb';
import {
  FRESH_LANGUAGE_STORAGE_KEY,
  activateFreshLanguage,
  isFreshLanguage,
  persistFreshLanguage,
  persistedFreshLanguage,
} from './freshLanguage';

const storage = new Map<string, string>();

describe('Bootstrap entry language preference', () => {
  beforeEach(() => {
    storage.clear();
    setFullWebLanguage('zh-CN');
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => void storage.set(key, String(value)),
      removeItem: (key: string) => void storage.delete(key),
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('leaves the Console store on the language chosen during setup', async () => {
    persistFreshLanguage('ru');

    // The Console language menu owns this store; loading it must find the same
    // preference the Bootstrap entry wrote, without either side naming a key.
    // This is a module-loading boundary: zustand persist captures `localStorage`
    // once, at store creation, so the store has to be evaluated after the
    // browser stub exists — a static import would capture it at file load.
    vi.resetModules();
    const { useLanguageStore } = await import('../stores/useLanguageStore');

    expect(useLanguageStore.getState().language).toBe('ru');
  });

  it('reads payloads the Console store and older builds leave behind', () => {
    persistFreshLanguage('ru');
    expect(persistedFreshLanguage()).toBe('ru');

    // An older plain value still resolves; an unsupported one never does.
    storage.set(FRESH_LANGUAGE_STORAGE_KEY, 'en');
    expect(persistedFreshLanguage()).toBe('en');
    storage.set(FRESH_LANGUAGE_STORAGE_KEY, JSON.stringify({ state: { language: 'fr' } }));
    expect(persistedFreshLanguage()).toBeNull();
    storage.set(FRESH_LANGUAGE_STORAGE_KEY, 'not-json');
    expect(persistedFreshLanguage()).toBeNull();
  });

  it('keeps the rest of the stored Console state when writing the language', () => {
    storage.set(
      FRESH_LANGUAGE_STORAGE_KEY,
      JSON.stringify({ state: { language: 'zh-CN', sidebarCollapsed: true }, version: 3 })
    );

    persistFreshLanguage('ru');

    expect(JSON.parse(storage.get(FRESH_LANGUAGE_STORAGE_KEY) ?? '')).toEqual({
      state: { language: 'ru', sidebarCollapsed: true },
      version: 3,
    });
  });

  it('selects the stored language for the first Bootstrap render', () => {
    storage.set(FRESH_LANGUAGE_STORAGE_KEY, JSON.stringify({ state: { language: 'zh-TW' } }));

    expect(activateFreshLanguage()).toBe('zh-TW');
    expect(fullWebLanguage()).toBe('zh-TW');
  });

  it('derives a first visit from the browser exactly as the Console does', () => {
    for (const [tag, expected] of [
      ['zh-HK', 'zh-TW'],
      ['zh-MO', 'zh-TW'],
      ['zh-Hant-TW', 'zh-TW'],
      ['zh-CN', 'zh-CN'],
      ['ru-RU', 'ru'],
      ['en-US', 'en'],
      ['fr-FR', 'en'],
    ] as const) {
      vi.stubGlobal('navigator', { languages: [tag], language: tag });

      expect(activateFreshLanguage()).toBe(expected);
      expect(fullWebLanguage()).toBe(expected);
      // The Console store starts from the same value for the same browser.
      expect(getInitialLanguage()).toBe(expected);
    }
  });

  it('accepts only the four Console languages', () => {
    expect(isFreshLanguage('ru')).toBe(true);
    expect(isFreshLanguage('fr')).toBe(false);
  });
});
