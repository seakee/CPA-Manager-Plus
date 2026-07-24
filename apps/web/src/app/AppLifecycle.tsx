import { useEffect } from 'react';
import {
  useLanguageStore,
  useThemeCustomizationStore,
  useThemeStore,
  useVisualEffectsStore,
} from '@/stores';

export function AppLifecycle() {
  const initializeTheme = useThemeStore((state) => state.initializeTheme);
  const initializeVisualEffects = useVisualEffectsStore((state) => state.initializeVisualEffects);
  const initializeThemeCustomization = useThemeCustomizationStore(
    (state) => state.initializeThemeCustomization
  );
  const language = useLanguageStore((state) => state.language);
  const setLanguage = useLanguageStore((state) => state.setLanguage);

  useEffect(() => {
    const cleanupTheme = initializeTheme();
    return cleanupTheme;
  }, [initializeTheme]);

  useEffect(() => {
    initializeVisualEffects();
  }, [initializeVisualEffects]);

  useEffect(() => {
    initializeThemeCustomization();
  }, [initializeThemeCustomization]);

  useEffect(() => {
    setLanguage(language);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []); // 仅用于首屏同步 i18n 语言

  useEffect(() => {
    document.documentElement.lang = language;
  }, [language]);

  return null;
}
