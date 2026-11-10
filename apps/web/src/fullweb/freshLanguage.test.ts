import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { LANGUAGE_ORDER, STORAGE_KEY_LANGUAGE } from '../utils/constants';
import { fullWebLanguage, setFullWebLanguage } from '../i18n/fullweb';
import { bootstrapJourneyText } from '../i18n/bootstrapJourney';
import {
  FRESH_LANGUAGES,
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

  it('shares the Console language key and menu order', () => {
    expect(FRESH_LANGUAGE_STORAGE_KEY).toBe(STORAGE_KEY_LANGUAGE);
    expect([...FRESH_LANGUAGES]).toEqual([...LANGUAGE_ORDER]);
  });

  it('reads and writes the zustand payload the Console store persists', () => {
    persistFreshLanguage('ru');
    expect(storage.get(FRESH_LANGUAGE_STORAGE_KEY)).toBe(
      JSON.stringify({ state: { language: 'ru' }, version: 0 })
    );
    expect(persistedFreshLanguage()).toBe('ru');

    // An older plain value still resolves; an unsupported one never does.
    storage.set(FRESH_LANGUAGE_STORAGE_KEY, 'en');
    expect(persistedFreshLanguage()).toBe('en');
    storage.set(FRESH_LANGUAGE_STORAGE_KEY, JSON.stringify({ state: { language: 'fr' } }));
    expect(persistedFreshLanguage()).toBeNull();
    storage.set(FRESH_LANGUAGE_STORAGE_KEY, 'not-json');
    expect(persistedFreshLanguage()).toBeNull();
  });

  it('selects the stored language for the first Bootstrap render', () => {
    storage.set(FRESH_LANGUAGE_STORAGE_KEY, JSON.stringify({ state: { language: 'zh-TW' } }));
    expect(activateFreshLanguage()).toBe('zh-TW');
    expect(fullWebLanguage()).toBe('zh-TW');
    expect(bootstrapJourneyText('welcome')).toBe(bootstrapJourneyText('welcome', 'zh-TW'));
  });

  it('keeps the resolved language when nothing is stored and rejects unknown codes', () => {
    setFullWebLanguage('en');
    expect(activateFreshLanguage()).toBe('en');
    expect(isFreshLanguage('ru')).toBe(true);
    expect(isFreshLanguage('fr')).toBe(false);
  });
});
