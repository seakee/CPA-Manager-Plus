import type { TFunction } from 'i18next';

/**
 * User-visible text kept as a key. Anything that outlives a render (a fetched
 * status, a failed sign-in, a stored log) must not persist a translated
 * sentence: switching language has to re-render the current text.
 */
export type LocalizedText =
  | { key: string; params?: Record<string, string | number>; fallbackKey?: string }
  | { text: string };

export const formatLocalizedText = (value: LocalizedText, t: TFunction): string =>
  'key' in value
    ? t(value.key, {
        ...value.params,
        ...(value.fallbackKey ? { defaultValue: t(value.fallbackKey) } : {}),
      })
    : value.text;
