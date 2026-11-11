import i18next from 'i18next';
import { describe, expect, it } from 'vitest';
import en from '@/i18n/locales/en.json';
import ru from '@/i18n/locales/ru.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import { formatModelStatus } from './modelStatus';

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

describe('model list status', () => {
  it('renders the fetched count in the language shown now', () => {
    const status = {
      type: 'success' as const,
      message: { key: 'system_info.models_count', params: { count: 2 } },
    };

    expect(formatModelStatus(status, translator('zh-CN'))).toBe('可用模型 2 个');
    expect(formatModelStatus(status, translator('en'))).toBe('2 available models');
    expect(formatModelStatus(status, translator('ru'))).toBe('Доступно моделей: 2');
  });

  it('keeps a translated error reason plus the raw service detail', () => {
    const status = {
      type: 'error' as const,
      message: { key: 'system_info.models_error' },
      detail: { key: 'usage_service_errors.request_failed' },
    };

    expect(formatModelStatus(status, translator('en'))).toBe(
      'Failed to load model list: Manager Server request failed'
    );
    expect(formatModelStatus(status, translator('ru'))).toContain('Не удалось загрузить список моделей');
  });
});
