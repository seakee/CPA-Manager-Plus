import { createInstance } from 'i18next';
import { fullweb as zhCN } from './locales/zh-CN.json';
import { fullweb as zhTW } from './locales/zh-TW.json';
import { fullweb as en } from './locales/en.json';
import { fullweb as ru } from './locales/ru.json';

// Full Web does not enter the legacy connection or stored-preference graph.
// The Bootstrap entry re-selects the Console's saved language itself; see
// fullweb/freshLanguage.
const i18n = createInstance();
void i18n.init({
  resources: {
    'zh-CN': { translation: zhCN },
    'zh-TW': { translation: zhTW },
    en: { translation: en },
    ru: { translation: ru },
  },
  lng: typeof navigator !== 'undefined' && navigator.language ? navigator.language : 'zh-CN',
  supportedLngs: ['zh-CN', 'zh-TW', 'en', 'ru'],
  fallbackLng: 'zh-CN',
  initAsync: false,
  interpolation: { escapeValue: false },
});
export const fullWebText = (key: keyof typeof en): string => i18n.t(key);
export function setFullWebLanguage(language: string) {
  void i18n.changeLanguage(language);
}
export const fullWebLanguage = () => i18n.resolvedLanguage || 'zh-CN';
// The Full Web text helpers are plain functions that read `fullWebLanguage()` at
// render, so a language switch only reaches them when the surface that renders
// them re-renders. A component subscribes here to own that re-render instead of
// depending on an unrelated state change.
const languageListeners = new Set<() => void>();
i18n.on('languageChanged', () => {
  for (const listener of languageListeners) listener();
});
export function subscribeFullWebLanguage(listener: () => void): () => void {
  languageListeners.add(listener);
  return () => {
    languageListeners.delete(listener);
  };
}
