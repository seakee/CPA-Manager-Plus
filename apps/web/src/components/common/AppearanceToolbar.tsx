import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IconLanguages, IconMoon, IconSun } from '@/components/ui/icons';
import { useLanguageStore, useThemeStore } from '@/stores';
import { LANGUAGE_LABEL_KEYS, LANGUAGE_ORDER } from '@/utils/constants';
import { isSupportedLanguage } from '@/utils/language';
import styles from './AppearanceToolbar.module.scss';

/** Theme and language switches pinned to the top-right of standalone pages. */
export function AppearanceToolbar() {
  const { t } = useTranslation();
  const language = useLanguageStore((state) => state.language);
  const setLanguage = useLanguageStore((state) => state.setLanguage);
  const theme = useThemeStore((state) => state.theme);
  const cycleTheme = useThemeStore((state) => state.cycleTheme);
  const languageMenuRef = useRef<HTMLDivElement | null>(null);
  const [languageMenuOpen, setLanguageMenuOpen] = useState(false);

  const handleLanguageSelect = (selectedLanguage: string) => {
    if (!isSupportedLanguage(selectedLanguage)) {
      return;
    }
    setLanguage(selectedLanguage);
    setLanguageMenuOpen(false);
  };

  useEffect(() => {
    if (!languageMenuOpen) {
      return;
    }

    const handlePointerDown = (event: MouseEvent) => {
      if (!languageMenuRef.current?.contains(event.target as Node)) {
        setLanguageMenuOpen(false);
      }
    };

    const handleEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setLanguageMenuOpen(false);
      }
    };

    document.addEventListener('mousedown', handlePointerDown);
    document.addEventListener('keydown', handleEscape);

    return () => {
      document.removeEventListener('mousedown', handlePointerDown);
      document.removeEventListener('keydown', handleEscape);
    };
  }, [languageMenuOpen]);

  return (
    <div className={styles.toolBar}>
      <button
        type="button"
        className={styles.toolButton}
        onClick={cycleTheme}
        aria-label={t('theme.switch')}
        title={t('theme.switch')}
      >
        {theme === 'dark' ? <IconMoon size={17} /> : <IconSun size={17} />}
      </button>
      <div className={styles.languageMenu} ref={languageMenuRef}>
        <button
          type="button"
          className={styles.toolButton}
          onClick={() => setLanguageMenuOpen((prev) => !prev)}
          aria-label={t('language.switch')}
          title={t('language.switch')}
          aria-haspopup="menu"
          aria-expanded={languageMenuOpen}
        >
          <IconLanguages size={17} />
        </button>
        {languageMenuOpen && (
          <div className={styles.languagePopover} role="menu" aria-label={t('language.switch')}>
            {LANGUAGE_ORDER.map((lang) => (
              <button
                key={lang}
                type="button"
                className={`${styles.languageOption} ${
                  language === lang ? styles.languageOptionActive : ''
                }`}
                onClick={() => handleLanguageSelect(lang)}
                role="menuitemradio"
                aria-checked={language === lang}
              >
                {t(LANGUAGE_LABEL_KEYS[lang])}
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
