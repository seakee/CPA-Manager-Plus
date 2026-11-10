import type { Language } from '@/types';
import { browserLanguage } from '@/i18n/browserLanguage';
import { STORAGE_KEY_LANGUAGE, SUPPORTED_LANGUAGES } from '@/utils/constants';

export const isSupportedLanguage = (value: string): value is Language =>
  SUPPORTED_LANGUAGES.includes(value as Language);

const parseStoredLanguage = (value: string): Language | null => {
  try {
    const parsed = JSON.parse(value);
    const candidate = parsed?.state?.language ?? parsed?.language ?? parsed;
    if (typeof candidate === 'string' && isSupportedLanguage(candidate)) {
      return candidate;
    }
  } catch {
    if (isSupportedLanguage(value)) {
      return value;
    }
  }
  return null;
};

const getStoredLanguage = (): Language | null => {
  if (typeof window === 'undefined') {
    return null;
  }
  try {
    const stored = localStorage.getItem(STORAGE_KEY_LANGUAGE);
    if (!stored) {
      return null;
    }
    return parseStoredLanguage(stored);
  } catch {
    return null;
  }
};

export const getInitialLanguage = (): Language => getStoredLanguage() ?? browserLanguage();
