import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setFullWebLanguage } from '../i18n/fullweb';
import { useConsoleDocumentTitle } from './useConsoleDocumentTitle';

function TitleProbe() {
  useConsoleDocumentTitle();
  return null;
}

const changeLanguage = async (language: string) => {
  await act(async () => {
    setFullWebLanguage(language);
    await Promise.resolve();
  });
};

describe('useConsoleDocumentTitle', () => {
  let renderer: ReactTestRenderer;
  let page: { title: string; documentElement: { lang: string } };

  beforeEach(() => {
    page = { title: '', documentElement: { lang: '' } };
    vi.stubGlobal('document', page);
  });

  afterEach(async () => {
    await act(async () => renderer?.unmount());
    vi.unstubAllGlobals();
  });

  it('keeps the tab title in the language the shell switches to', async () => {
    await changeLanguage('zh-CN');
    await act(async () => {
      renderer = create(<TitleProbe />);
    });
    expect(page.title).toBe('CPAMP 控制台');

    await changeLanguage('en');
    expect(page.title).toBe('CPAMP Console');

    await changeLanguage('ru');
    expect(page.title).toBe('Консоль CPAMP');
  });
});
