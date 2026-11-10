import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import legacyI18n from '@/i18n';
import { setFullWebLanguage } from './fullweb';

// index.html hard-codes a Chinese `lang`; both i18n instances have to keep the
// document attribute on the language the UI actually renders in.
describe('document language', () => {
  let root: { lang: string };

  beforeEach(() => {
    root = { lang: 'zh-CN' };
    vi.stubGlobal('document', { documentElement: root });
  });

  afterEach(() => {
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
