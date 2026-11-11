import i18next from 'i18next';
import { describe, expect, it } from 'vitest';
import en from '@/i18n/locales/en.json';
import ru from '@/i18n/locales/ru.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import { formatLoginError, formatLoginFailure, toLoginError } from './loginError';

const translator = (lng: string) => {
  const instance = i18next.createInstance();
  instance.init({
    resources: { en: { translation: en }, ru: { translation: ru }, 'zh-CN': { translation: zhCN } },
    lng,
    fallbackLng: 'zh-CN',
    initAsync: false,
    interpolation: { escapeValue: false },
  });
  return instance.t.bind(instance);
};

describe('login error text', () => {
  it('keeps the reason so a language switch retranslates the failure', () => {
    const failure = toLoginError({ status: 403, message: 'Request failed with status code 403' });
    const english = formatLoginError(failure, translator('en'));
    const russian = formatLoginError(failure, translator('ru'));

    // The HTTP prefix is protocol, the sentence after it is translated.
    expect(english.startsWith('HTTP 403: ')).toBe(true);
    expect(russian.startsWith('HTTP 403: ')).toBe(true);
    expect(russian).not.toBe(english);
    expect(english).not.toMatch(/(login|usage_service_errors)\./);
    expect(russian).not.toMatch(/(login|usage_service_errors)\./);

    const rejectedKey = toLoginError({ status: 401 });
    const rejectedEnglish = formatLoginError(rejectedKey, translator('en'));
    const rejectedRussian = formatLoginError(rejectedKey, translator('ru'));
    expect(rejectedEnglish.startsWith('HTTP')).toBe(false);
    expect(rejectedRussian).not.toBe(rejectedEnglish);
  });

  it('keeps the backend detail outside the translated sentence', () => {
    const failure = toLoginError({ status: 500, message: 'database is locked' });

    expect(failure).toEqual({ key: 'login.error_server', httpStatus: 500, backendDetail: 'database is locked' });
    const english = formatLoginError(failure, translator('en'));
    const russian = formatLoginError(failure, translator('ru'));
    expect(english.startsWith('HTTP 500: ')).toBe(true);
    // The raw backend text is appended verbatim in every language.
    expect(english).toContain('database is locked');
    expect(russian).toContain('database is locked');
    expect(russian).not.toBe(english);
  });

  it('builds a failure toast from the error that is on screen, in any language', () => {
    const failure = toLoginError({ status: 401 });
    const chineseBox = formatLoginError(failure, translator('zh-CN'));
    const chineseToast = formatLoginFailure(failure, translator('zh-CN'));
    const russianToast = formatLoginFailure(failure, translator('ru'));

    // The toast repeats the box above it and adds nothing but the failure prefix.
    expect(chineseToast).toContain(chineseBox);
    expect(chineseToast).not.toBe(chineseBox);
    expect(chineseToast).not.toMatch(/notification\./);
    // Re-rendering it with another translator changes the sentence entirely.
    expect(russianToast).not.toBe(chineseToast);
    expect(russianToast).not.toContain(chineseBox);
    expect(formatLoginError(failure, translator('ru'))).not.toBe(chineseBox);
  });

  it('maps transport failures and usage service codes to translation keys', () => {
    expect(toLoginError({ code: 'ERR_NETWORK', message: 'Network Error' })).toEqual({
      key: 'login.error_network',
    });
    expect(toLoginError(new Error('timeout of 10000ms exceeded'))).toEqual({
      key: 'login.error_timeout',
    });
    expect(toLoginError({ code: 'invalid_admin_key' })).toEqual({
      key: 'usage_service_errors.invalid_admin_key',
      fallbackKey: 'usage_service_errors.request_failed',
    });
    expect(toLoginError({ status: 400, message: 'Request failed with status code 400' })).toEqual({
      key: 'login.error_invalid',
      httpStatus: 400,
      backendDetail: undefined,
    });
  });
});
