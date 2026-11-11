import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { fullWebI18n } from '../i18n/fullweb';

// The tab title belongs to the language the shell currently renders in. The
// Console and login headers switch language without remounting this shell, so
// the title has to follow the instance instead of being written once on mount.
export function useConsoleDocumentTitle() {
  const { t } = useTranslation(undefined, { i18n: fullWebI18n, useSuspense: false });
  const title = t('consoleTitle');
  useEffect(() => {
    document.title = title;
  }, [title]);
}
