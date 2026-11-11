import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import legacyI18n from '@/i18n';
import { STORAGE_KEY_LANGUAGE } from '@/utils/constants';
import { setFullWebLanguage } from './fullweb';

// index.html hard-codes a Chinese `lang`; both i18n instances have to keep the
// document attribute on the language the UI actually renders in.
describe('document language', () => {
  let root: { lang: string };

  beforeEach(() => {
    // Deliberately not a supported language: a missing write stays visible even
    // when the expected value happens to be the static index.html default.
    root = { lang: 'zz' };
    vi.stubGlobal('document', { documentElement: root });
  });

  afterEach(async () => {
    // Both instances are module singletons: leave them where the next suite
    // expects them instead of depending on test order.
    await legacyI18n.changeLanguage('zh-CN');
    setFullWebLanguage('zh-CN');
    vi.unstubAllGlobals();
  });

  it('follows the Console language instance', async () => {
    await legacyI18n.changeLanguage('ru');
    expect(root.lang).toBe('ru');

    await legacyI18n.changeLanguage('zh-TW');
    expect(root.lang).toBe('zh-TW');

    await legacyI18n.changeLanguage('zh-CN');
    expect(root.lang).toBe('zh-CN');
  });

  it('follows the Fresh language instance', async () => {
    setFullWebLanguage('en');
    await vi.waitFor(() => expect(root.lang).toBe('en'));

    setFullWebLanguage('ru');
    await vi.waitFor(() => expect(root.lang).toBe('ru'));
  });
});

// The singletons above are created once per test file, so they can only prove the
// change-event path. First load is a module-evaluation write, and only a fresh
// import with the environment already stubbed can see it.
describe('document language on first load', () => {
  let root: { lang: string };
  let stored: Map<string, string>;

  beforeEach(() => {
    root = { lang: 'zz' };
    stored = new Map<string, string>();
    vi.stubGlobal('document', { documentElement: root });
    vi.stubGlobal('window', {});
    vi.stubGlobal('navigator', { language: 'zh-CN', languages: ['zh-CN'] });
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => stored.get(key) ?? null,
      setItem: (key: string, value: string) => stored.set(key, value),
      removeItem: (key: string) => stored.delete(key),
      clear: () => stored.clear(),
    });
    // The suites above import the singletons statically; without dropping the
    // module registry the import below returns that already-evaluated module.
    vi.resetModules();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.resetModules();
  });

  it('writes the saved preference before any language change', async () => {
    stored.set(STORAGE_KEY_LANGUAGE, JSON.stringify({ state: { language: 'zh-TW' } }));
    vi.stubGlobal('navigator', { language: 'ru-RU', languages: ['ru-RU'] });

    await import('./index');

    expect(root.lang).toBe('zh-TW');
  });

  it('writes the browser language when nothing was saved', async () => {
    vi.stubGlobal('navigator', { language: 'ru-RU', languages: ['ru-RU'] });

    await import('./index');

    expect(root.lang).toBe('ru');
  });

  it('writes the resolved Fresh language before any language change', async () => {
    vi.stubGlobal('navigator', { language: 'en-GB', languages: ['en-GB'] });

    const fresh = await import('./fullweb');

    expect(fresh.fullWebI18n.resolvedLanguage).toBe('en');
    expect(root.lang).toBe('en');
  });
});
