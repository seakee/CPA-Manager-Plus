import { BrowserRouter, MemoryRouter } from 'react-router-dom';
import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import '../i18n';
import { AppLifecycle } from '../app/AppLifecycle';
import { LoginPage, type ConsoleLoginAdapter } from '../features/login/LoginPage';
import { setFullWebLanguage } from '../i18n/fullweb';
import { useLanguageStore } from '../stores/useLanguageStore';
import { useThemeStore } from '../stores/useThemeStore';
import { LANGUAGE_LABEL_KEYS, LANGUAGE_ORDER } from '../utils/constants';
import { FullWebIcon } from './FullWebIcon';
import logo from '../assets/brand/cpamp-logo-horizontal-color.svg?inline';
import logoOnDark from '../assets/brand/cpamp-logo-horizontal-on-dark.svg?inline';

// Keep Upgrade's appearance controls on the same original preference boundary
// as its login form, without bringing legacy stores into the Bootstrap core.
export function FullWebUpgradeHeader() {
  const { t, i18n } = useTranslation();
  const language = i18n.resolvedLanguage || i18n.language;
  const setLanguage = useLanguageStore((state) => state.setLanguage);
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const setTheme = useThemeStore((state) => state.setTheme);
  useEffect(() => {
    setFullWebLanguage(language);
  }, [language]);
  return (
    <>
      <AppLifecycle />
      <header className="upgrade-header">
        <img src={resolvedTheme === 'dark' ? logoOnDark : logo} alt="CPAMP" />
        <div className="upgrade-appearance">
          <label className="upgrade-language">
            <FullWebIcon name="globe" size={22} />
            <select
              aria-label={t('language.switch')}
              value={language}
              onChange={(event) => setLanguage(event.target.value)}
            >
              {LANGUAGE_ORDER.map((value) => (
                <option key={value} value={value}>
                  {t(LANGUAGE_LABEL_KEYS[value])}
                </option>
              ))}
            </select>
          </label>
          <button
            type="button"
            className="upgrade-theme"
            aria-label={t('theme.switch')}
            onClick={() => setTheme(resolvedTheme === 'dark' ? 'white' : 'dark')}
          >
            <FullWebIcon name={resolvedTheme === 'dark' ? 'moon' : 'sun'} size={24} />
          </button>
          <span className="journey-version">v2</span>
        </div>
      </header>
    </>
  );
}

export function FullWebLoginPage({ adapter }: { adapter: ConsoleLoginAdapter }) {
  const { i18n } = useTranslation();
  const language = i18n.resolvedLanguage || i18n.language;
  useEffect(() => {
    // The existing login owns language selection. Carry it into the Fresh
    // journey rather than switching back to navigator.language after login.
    setFullWebLanguage(language);
  }, [language]);
  const Router = typeof window.history?.replaceState === 'function' ? BrowserRouter : MemoryRouter;
  return (
    <Router>
      <AppLifecycle />
      <LoginPage consoleAuth={adapter} />
    </Router>
  );
}
