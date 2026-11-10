import type { Language } from '@/types';

// The one browser-language mapping both entries share. It carries no store,
// storage or transport import, so the Bootstrap graph can use it without
// pulling the Console language store (tests/frontendTransportIsolation).
const TRADITIONAL_CHINESE_PREFIXES = ['zh-tw', 'zh-hk', 'zh-mo', 'zh-hant'] as const;

/** Maps a BCP-47 tag onto the Console language set; unknown tags become `en`. */
const languageFromTag = (tag: string): Language => {
  const lower = tag.toLowerCase();
  if (TRADITIONAL_CHINESE_PREFIXES.some((prefix) => lower.startsWith(prefix))) return 'zh-TW';
  if (lower.startsWith('zh')) return 'zh-CN';
  if (lower.startsWith('ru')) return 'ru';
  return 'en';
};

/** The language a first visit starts on when nothing is persisted yet. */
export const browserLanguage = (): Language => {
  if (typeof navigator === 'undefined') {
    return 'zh-CN';
  }
  const raw = navigator.languages?.[0] || navigator.language || 'zh-CN';
  return languageFromTag(raw);
};
