import { describe, expect, it } from 'vitest';
import en from '@/i18n/locales/en.json';
import ru from '@/i18n/locales/ru.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';

describe('model window empty usage copy', () => {
  it('states that an explicit empty period has no matching local usage records', () => {
    expect(en.accounts.detail_model_window_empty_usage).toBe(
      'No matching local usage records'
    );
    expect(en.accounts.detail_model_window_empty_usage).not.toBe('No usage in this window yet');
    expect(ru.accounts.detail_model_window_empty_usage).toBe(
      'Нет подходящих локальных записей об использовании'
    );
    expect(zhCN.accounts.detail_model_window_empty_usage).toBe('未找到匹配的本地用量记录');
    expect(zhTW.accounts.detail_model_window_empty_usage).toBe('未找到相符的本機用量記錄');
  });
});
