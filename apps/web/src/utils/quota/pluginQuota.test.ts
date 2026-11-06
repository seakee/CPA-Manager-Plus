import { describe, expect, it } from 'vitest';
import type { AuthFileItem, PluginListEntry } from '@/types';
import {
  getPluginQuotaWindowLabelKey,
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
