import { describe, expect, it } from 'vitest';
import type { AuthFileItem, PluginListEntry, PluginQuotaSummaryItem } from '@/types';
import {
  formatPluginQuotaSummaryValue,
  getPluginQuotaWindowLabelKey,
  hasMeaningfulPluginQuotaSummaryValue,
  isPluginQuotaCandidateFile,
  parsePluginQuotaWindowSeconds,
  readAuthFileQuotaAuthIndex,
  readAuthFileQuotaPluginId,
  readAuthFileQuotaProvider,
  resolveQuotaPluginFromList,
} from './pluginQuota';

const buildPlugin = (overrides: Partial<PluginListEntry> = {}): PluginListEntry => ({
  id: 'clinepassbridge',
  path: 'plugins/clinepassbridge-v0.4.0.so',
  configured: true,
  registered: true,
  enabled: true,
  effectiveEnabled: true,
  supportsOAuth: false,
  supportsQuota: true,
  quotaProvider: 'cline-pass',
  logo: '',
  configFields: [],
  menus: [],
  metadata: null,
  ...overrides,
});

describe('isPluginQuotaCandidateFile', () => {
  it('detects snake_case and camelCase support markers', () => {
    expect(isPluginQuotaCandidateFile({ name: 'a.json', supports_quota: true })).toBe(true);
    expect(isPluginQuotaCandidateFile({ name: 'a.json', supportsQuota: true })).toBe(true);
    expect(isPluginQuotaCandidateFile({ name: 'a.json', supports_quota: 'true' })).toBe(true);
  });

  it('rejects missing or falsy markers', () => {
    expect(isPluginQuotaCandidateFile({ name: 'a.json' })).toBe(false);
    expect(isPluginQuotaCandidateFile({ name: 'a.json', supports_quota: false })).toBe(false);
    expect(isPluginQuotaCandidateFile({ name: 'a.json', supports_quota: 'nope' })).toBe(false);
  });
});

describe('auth file quota field readers', () => {
  it('reads plugin id, provider and auth index from snake_case fields', () => {
    const file: AuthFileItem = {
      name: 'clinepassbridge-abc.json',
      quota_plugin_id: 'clinepassbridge',
      quota_provider: 'cline-pass',
      auth_index: '2912b086db072af7',
    };
    expect(readAuthFileQuotaPluginId(file)).toBe('clinepassbridge');
    expect(readAuthFileQuotaProvider(file)).toBe('cline-pass');
    expect(readAuthFileQuotaAuthIndex(file)).toBe('2912b086db072af7');
  });

  it('returns empty strings for missing fields', () => {
    const file: AuthFileItem = { name: 'a.json' };
    expect(readAuthFileQuotaPluginId(file)).toBe('');
    expect(readAuthFileQuotaProvider(file)).toBe('');
    expect(readAuthFileQuotaAuthIndex(file)).toBe('');
  });
});

describe('resolveQuotaPluginFromList', () => {
  it('matches by declared quota provider', () => {
    const plugins = [buildPlugin(), buildPlugin({ id: 'other', quotaProvider: 'other-pass' })];
    expect(resolveQuotaPluginFromList('cline-pass', plugins)?.id).toBe('clinepassbridge');
  });

  it('falls back to plugin id equality and is case-insensitive', () => {
    const plugins = [buildPlugin({ quotaProvider: undefined })];
    expect(resolveQuotaPluginFromList('ClinePassBridge', plugins)?.id).toBe('clinepassbridge');
  });

  it('skips disabled plugins', () => {
    const plugins = [buildPlugin({ enabled: false, effectiveEnabled: false })];
    expect(resolveQuotaPluginFromList('cline-pass', plugins)).toBeNull();
  });

  it('returns null without a provider', () => {
    expect(resolveQuotaPluginFromList('', [buildPlugin()])).toBeNull();
    expect(resolveQuotaPluginFromList('cline-pass', [])).toBeNull();
  });
});

describe('getPluginQuotaWindowLabelKey', () => {
  it('maps common window hints to label keys', () => {
    expect(getPluginQuotaWindowLabelKey('5h')).toBe('window_five_hour');
    expect(getPluginQuotaWindowLabelKey('five_hour')).toBe('window_five_hour');
    expect(getPluginQuotaWindowLabelKey('24h')).toBe('window_daily');
    expect(getPluginQuotaWindowLabelKey('7d')).toBe('window_weekly');
    expect(getPluginQuotaWindowLabelKey('weekly')).toBe('window_weekly');
    expect(getPluginQuotaWindowLabelKey('monthly')).toBe('window_monthly');
  });

  it('returns null for unknown or empty windows', () => {
    expect(getPluginQuotaWindowLabelKey('milestone')).toBeNull();
    expect(getPluginQuotaWindowLabelKey(undefined)).toBeNull();
    expect(getPluginQuotaWindowLabelKey('')).toBeNull();
  });
});

describe('parsePluginQuotaWindowSeconds', () => {
  it('parses compact duration hints', () => {
    expect(parsePluginQuotaWindowSeconds('5h')).toBe(5 * 3600);
    expect(parsePluginQuotaWindowSeconds('24h')).toBe(24 * 3600);
    expect(parsePluginQuotaWindowSeconds('7d')).toBe(7 * 86400);
    expect(parsePluginQuotaWindowSeconds('30d')).toBe(30 * 86400);
  });

  it('parses wordy hints including numbered hour forms', () => {
    expect(parsePluginQuotaWindowSeconds('weekly')).toBe(7 * 86400);
    expect(parsePluginQuotaWindowSeconds('monthly')).toBe(30 * 86400);
    expect(parsePluginQuotaWindowSeconds('24-hour')).toBe(24 * 3600);
    expect(parsePluginQuotaWindowSeconds('five hour limit')).toBe(5 * 3600);
  });

  it('returns null for unknown windows', () => {
    expect(parsePluginQuotaWindowSeconds('milestone')).toBeNull();
    expect(parsePluginQuotaWindowSeconds(undefined)).toBeNull();
  });
});

describe('formatPluginQuotaSummaryValue', () => {
  const makeItem = (
    overrides: Partial<PluginQuotaSummaryItem>
  ): Pick<PluginQuotaSummaryItem, 'value' | 'unit' | 'format' | 'currency'> => ({
    value: null,
    unit: null,
    format: null,
    currency: null,
    ...overrides,
  });

  it('renders a declared percent value as-is without rescaling', () => {
    expect(formatPluginQuotaSummaryValue(makeItem({ value: 32, format: 'number', unit: '%' }))).toBe(
      '32%'
    );
    expect(formatPluginQuotaSummaryValue(makeItem({ value: 32, format: 'percent' }))).toBe('32%');
    // 32% must never become 3,200%.
    expect(formatPluginQuotaSummaryValue(makeItem({ value: 32, format: 'number', unit: '%' }))).not.
      toBe('3,200%');
  });

  it('keeps a valid zero visible instead of hiding it', () => {
    expect(formatPluginQuotaSummaryValue(makeItem({ value: 0, format: 'number', unit: '%' }))).toBe(
      '0%'
    );
    expect(
      formatPluginQuotaSummaryValue(makeItem({ value: 0, format: 'currency', currency: 'USD' }), 'en')
    ).toBe('$0.00');
  });

  it('formats declared currency values with the declared currency code', () => {
    expect(
      formatPluginQuotaSummaryValue(makeItem({ value: 12.5, format: 'currency', currency: 'USD' }), 'en')
    ).toBe('$12.50');
  });

  it('falls back to plain text for an unsupported currency code', () => {
    const formatted = formatPluginQuotaSummaryValue(
      makeItem({ value: 12.5, format: 'currency', currency: 'NOPE' }),
      'en'
    );
    expect(formatted).toContain('12.5');
    expect(formatted).toContain('NOPE');
  });

  it('keeps plain credit counts in their declared unit', () => {
    expect(
      formatPluginQuotaSummaryValue(makeItem({ value: 1234, format: 'number', unit: 'credits' }), 'en')
    ).toBe('1,234 credits');
  });

  it('keeps textual values as plain text without inventing units', () => {
    expect(formatPluginQuotaSummaryValue(makeItem({ value: 'unlimited', unit: '%' }))).toBe(
      'unlimited'
    );
  });

  it('returns null for missing or invalid values instead of a fabricated zero', () => {
    expect(formatPluginQuotaSummaryValue(makeItem({ value: null }))).toBeNull();
    expect(formatPluginQuotaSummaryValue(makeItem({ value: Number.NaN, format: 'number' }))).toBeNull();
    expect(
      formatPluginQuotaSummaryValue(makeItem({ value: Number.POSITIVE_INFINITY, format: 'number' }))
    ).toBeNull();
  });

  it('falls back to a plain decimal for unknown formats with numeric values', () => {
    expect(formatPluginQuotaSummaryValue(makeItem({ value: 42, format: 'tiles' }), 'en')).toBe('42');
  });

  it('keeps the declared unit when a currency value has no currency code', () => {
    expect(
      formatPluginQuotaSummaryValue(makeItem({ value: 12.5, format: 'currency', unit: 'USD' }), 'en')
    ).toBe('12.5 USD');
  });

  it('falls back to plain decimals when the locale itself is unsupported', () => {
    expect(formatPluginQuotaSummaryValue(makeItem({ value: 1234.5, format: 'number' }), 'x-invalid')).toBe(
      '1234.5'
    );
  });

  it('preserves meaningful nonzero credit precision instead of rounding to zero', () => {
    const small = formatPluginQuotaSummaryValue(
      makeItem({ value: 0.0004, format: 'number', unit: 'credits' }),
      'en'
    );
    expect(small).toBe('0.0004 credits');
    const negativeSmall = formatPluginQuotaSummaryValue(
      makeItem({ value: -0.0004, format: 'number', unit: 'credits' }),
      'en'
    );
    expect(negativeSmall).toBe('-0.0004 credits');
  });

  it('renders an extremely small finite value faithfully instead of as zero', () => {
    const tiny = formatPluginQuotaSummaryValue(
      makeItem({ value: 1e-25, format: 'number', unit: 'credits' }),
      'en'
    );
    // Numeric meaning: not an exact-zero display, and the value stays readable.
    expect(tiny).not.toBe('0 credits');
    expect(tiny).toContain('1e-25');
  });

  it('keeps small declared-currency values distinct from a zero balance', () => {
    expect(
      formatPluginQuotaSummaryValue(makeItem({ value: 0.004, format: 'currency', currency: 'USD' }), 'en')
    ).toBe('$0.004');
    expect(
      formatPluginQuotaSummaryValue(makeItem({ value: -0.004, format: 'currency', currency: 'USD' }), 'en')
    ).toBe('-$0.004');
  });

  it('keeps genuine zero and ordinary currency displays unchanged', () => {
    expect(formatPluginQuotaSummaryValue(makeItem({ value: 0, format: 'number', unit: 'credits' }), 'en')).toBe(
      '0 credits'
    );
    expect(formatPluginQuotaSummaryValue(makeItem({ value: 0, format: 'currency', currency: 'USD' }), 'en')).toBe(
      '$0.00'
    );
    expect(formatPluginQuotaSummaryValue(makeItem({ value: 12.5, format: 'currency', currency: 'USD' }), 'en')).toBe(
      '$12.50'
    );
    expect(formatPluginQuotaSummaryValue(makeItem({ value: 32, format: 'number', unit: '%' }), 'en')).toBe('32%');
  });
});

describe('hasMeaningfulPluginQuotaSummaryValue', () => {
  const makeItem = (
    value: PluginQuotaSummaryItem['value']
  ): PluginQuotaSummaryItem => ({
    key: 'item',
    label: 'Item',
    value,
    unit: null,
    format: null,
    currency: null,
  });

  it('treats zero, text, and numbers as meaningful evidence', () => {
    expect(hasMeaningfulPluginQuotaSummaryValue([makeItem(0)])).toBe(true);
    expect(hasMeaningfulPluginQuotaSummaryValue([makeItem(' limited ')])).toBe(true);
    expect(hasMeaningfulPluginQuotaSummaryValue([makeItem(32)])).toBe(true);
  });

  it('treats all-null and missing summaries as not meaningful', () => {
    expect(hasMeaningfulPluginQuotaSummaryValue([makeItem(null)])).toBe(false);
    expect(hasMeaningfulPluginQuotaSummaryValue([])).toBe(false);
    expect(hasMeaningfulPluginQuotaSummaryValue([makeItem(null), makeItem(null)])).toBe(false);
  });

  it('accepts a single meaningful item among unknown ones', () => {
    expect(hasMeaningfulPluginQuotaSummaryValue([makeItem(null), makeItem(32)])).toBe(true);
  });
});
