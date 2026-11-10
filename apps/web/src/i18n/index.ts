/**
 * i18next 国际化配置
 */

import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import zhCN from './locales/zh-CN.json';
import zhTW from './locales/zh-TW.json';
import en from './locales/en.json';
import ru from './locales/ru.json';
import { getInitialLanguage } from '@/utils/language';

i18n.use(initReactI18next).init({
  resources: {
    'zh-CN': { translation: zhCN },
    'zh-TW': { translation: zhTW },
    en: { translation: en },
    ru: { translation: ru }
  },
  lng: getInitialLanguage(),
  fallbackLng: 'zh-CN',
  interpolation: {
    escapeValue: false // React 已经转义
  },
  react: {
    useSuspense: false
  }
});

export default i18n;

// index.html ships a static `lang`, and the language menu never touched it, so
// the document kept claiming Chinese for every locale. Keep the attribute on
// the language the UI actually renders in.
function applyDocumentLanguage() {
  if (typeof document !== 'undefined' && document.documentElement) {
    document.documentElement.lang = i18n.resolvedLanguage || 'zh-CN';
  }
}

applyDocumentLanguage();
i18n.on('languageChanged', applyDocumentLanguage);
