import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

// A counted message needs every plural category its language uses. When one is
// missing i18next silently answers with the fallback language (zh-CN), so the
// Russian Console printed "2 个配置字段" instead of a Russian form.
const LOCALES = ['zh-CN', 'zh-TW', 'en', 'ru'];
const PLURAL_SUFFIXES = ['_zero', '_one', '_two', '_few', '_many', '_other'];
const REQUIRED_CATEGORIES = {
  'zh-CN': ['_other'],
  'zh-TW': ['_other'],
  en: ['_one', '_other'],
  ru: ['_one', '_few', '_many', '_other'],
};
// Counted messages that must never lose their Russian group: deleting the whole
// group removes the family from the Russian resource itself, so only a fixed list
// can notice when all four locales agree on being wrong.
const PROTECTED_PLURAL_FAMILIES = [
  'plugin_management.config_field_count',
  'plugin_management.menu_count',
];
// Counts that select every Russian category: one (1), few (2, 3, 4), many (5, 11).
const RUSSIAN_PROBE_COUNTS = [1, 2, 5, 11, 21, 1.5];
const CJK_PATTERN = /[\u3400-\u9fff\uf900-\ufaff]/;

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const { createInstance } = createRequire(path.join(repoRoot, 'apps/web/package.json'))('i18next');

const readLocale = (locale) =>
  JSON.parse(
    readFileSync(path.join(repoRoot, 'apps/web/src/i18n/locales', `${locale}.json`), 'utf8')
  );

const flattenKeys = (value, prefix = '', keys = []) => {
  for (const [key, child] of Object.entries(value)) {
    const next = prefix ? `${prefix}.${key}` : key;
    if (child && typeof child === 'object' && !Array.isArray(child)) {
      flattenKeys(child, next, keys);
      continue;
    }
    keys.push(next);
  }
  return keys;
};

const findPluralSuffix = (key) => PLURAL_SUFFIXES.find((suffix) => key.endsWith(suffix));

// A counted family is a base key plus its category keys. A key that merely ends
// in a category word ("..._execute_one") carries a single suffix and no _other,
// so it is a plain label rather than a plural family.
const collectPluralFamilies = (locale) => {
  const families = new Map();
  for (const key of flattenKeys(locale)) {
    const suffix = findPluralSuffix(key);
    if (!suffix) continue;
    const base = key.slice(0, -suffix.length);
    const suffixes = families.get(base) ?? new Set();
    suffixes.add(suffix);
    families.set(base, suffixes);
  }
  return new Map(
    [...families].filter(([, suffixes]) => suffixes.has('_other') || suffixes.size > 1)
  );
};

const locales = new Map(LOCALES.map((code) => [code, readLocale(code)]));

// Every family any locale counts, whichever locale declared it: a family deleted
// from one file stays in scope because the other files still declare it.
const countedFamilies = () => {
  const families = new Set();
  for (const code of LOCALES) {
    for (const base of collectPluralFamilies(locales.get(code)).keys()) families.add(base);
  }
  return [...families];
};

const missingCategories = () => {
  const gaps = [];
  for (const base of countedFamilies()) {
    for (const code of LOCALES) {
      const suffixes = collectPluralFamilies(locales.get(code)).get(base);
      if (!suffixes) {
        gaps.push(`${code}: ${base} is missing entirely`);
        continue;
      }
      const missing = REQUIRED_CATEGORIES[code].filter((suffix) => !suffixes.has(suffix));
      if (missing.length) gaps.push(`${code}: ${base} is missing ${missing.join(', ')}`);
    }
  }
  return gaps.sort();
};

const missingProtectedFamilies = () => {
  const gaps = [];
  for (const base of PROTECTED_PLURAL_FAMILIES) {
    for (const code of LOCALES) {
      if (collectPluralFamilies(locales.get(code)).has(base)) continue;
      gaps.push(`${code}: ${base} is missing entirely`);
    }
  }
  return gaps.sort();
};

const createRussianTranslator = () => {
  const instance = createInstance();
  instance.init({
    resources: {
      ru: { translation: locales.get('ru') },
      'zh-CN': { translation: locales.get('zh-CN') },
    },
    lng: 'ru',
    fallbackLng: 'zh-CN',
    initAsync: false,
    interpolation: { escapeValue: false },
  });
  return (key, count) => instance.t(key, { count, defaultValue: key });
};

const pluralProbeLeaks = () => {
  const translate = createRussianTranslator();
  const leaked = [];
  for (const base of countedFamilies()) {
    for (const count of RUSSIAN_PROBE_COUNTS) {
      const text = translate(base, count);
      if (text === base || CJK_PATTERN.test(text)) leaked.push(`${base} count=${count}: ${text}`);
    }
  }
  return leaked;
};

describe('locale plural categories', () => {
  it('declares every category each language uses for every counted family', () => {
    expect(missingCategories()).toEqual([]);
  });

  it('keeps the counted families whose whole group could be deleted at once', () => {
    expect(missingProtectedFamilies()).toEqual([]);
  });

  it('keeps keys that merely end in a category word out of the counted families', () => {
    const families = collectPluralFamilies(locales.get('en'));

    expect(families.has('auth_files.paste_success_many')).toBe(false);
    expect(families.has('monitoring.server_codex_inspection_execute_one')).toBe(false);
    expect(flattenKeys(locales.get('en'))).toEqual(
      expect.arrayContaining([
        'auth_files.paste_success_many',
        'monitoring.server_codex_inspection_execute_one',
      ])
    );
  });

  it('resolves every Russian count without falling back to Chinese', () => {
    expect(pluralProbeLeaks()).toEqual([]);
  });
});
