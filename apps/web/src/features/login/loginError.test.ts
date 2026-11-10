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

    expect(formatLoginError(failure, translator('en'))).toBe(
      'HTTP 403: Access denied, insufficient permissions'
    );
    expect(formatLoginError(failure, translator('ru'))).toBe(
      'HTTP 403: Доступ запрещён, недостаточно прав'
    );

    const rejectedKey = toLoginError({ status: 401 });
    expect(formatLoginError(rejectedKey, translator('ru'))).toBe('Неверный ключ администратора.');
  });

  it('keeps the backend detail outside the translated sentence', () => {
    const failure = toLoginError({ status: 500, message: 'database is locked' });

    expect(failure).toEqual({ key: 'login.error_server', httpStatus: 500, backendDetail: 'database is locked' });
    expect(formatLoginError(failure, translator('en'))).toBe(
      'HTTP 500: Internal server error, please try again later (service response: database is locked)'
    );
    expect(formatLoginError(failure, translator('ru'))).toContain('детали сервера: database is locked');
  });

  it('builds a failure toast that retranslates with the error box', () => {
    const failure = toLoginError({ status: 401 });

    expect(formatLoginFailure(failure, translator('zh-CN'))).toBe('登录失败: 管理员密钥无效。');
    expect(formatLoginFailure(failure, translator('ru'))).toBe(
      'Вход не выполнен: Неверный ключ администратора.'
    );
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
