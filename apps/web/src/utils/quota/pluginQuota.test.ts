import { afterEach, describe, expect, it, vi } from 'vitest';
import type { TFunction } from 'i18next';
import type { AuthFileItem } from '@/types';
import { pluginQuotaApi } from '@/services/api/pluginQuota';
import {
  clearPluginQuotaProviders,
  fetchPluginQuota,
  formatPluginQuotaItemAmount,
  formatPluginQuotaItemValue,
  getPluginQuotaProviderDescriptor,
  isPluginQuotaCredential,
  isPluginQuotaProvider,
  normalizePluginQuotaItems,
  normalizePluginQuotaProviderKey,
  registerPluginQuotaProviders,
  resolvePluginQuotaProvider,
} from './pluginQuota';

const t = ((key: string) => key) as TFunction;

const authFile = (overrides: Partial<AuthFileItem> = {}): AuthFileItem => ({
  name: 'example-plugin-account.json',
  provider: 'example-plugin',
  auth_index: 'auth-index-1',
  ...overrides,
});

afterEach(() => {
  clearPluginQuotaProviders();
  vi.restoreAllMocks();
});

describe('plugin quota provider catalogue', () => {
  it('normalizes provider keys the way CPA and the Manager Server do', () => {
    expect(normalizePluginQuotaProviderKey(' Example_Plugin ')).toBe('example-plugin');
    expect(normalizePluginQuotaProviderKey(undefined)).toBe('');
  });

  it('registers the runtime catalogue and resolves a credential binding', () => {
    registerPluginQuotaProviders([
      {
        plugin_id: 'example-plugin',
        provider: 'example-plugin',
        display_name: 'Example quota',
        supported_providers: ['example-plugin', 'shared-plugin'],
      },
    ]);

    expect(isPluginQuotaProvider('Example_Plugin')).toBe(true);
    expect(getPluginQuotaProviderDescriptor('example-plugin')?.display_name).toBe('Example quota');
    expect(resolvePluginQuotaProvider(authFile({ quota_provider: 'example-plugin' }))).toBe(
      'example-plugin'
    );
    expect(
      resolvePluginQuotaProvider(authFile({ provider: 'shared-plugin', quota_provider: undefined }))
    ).toBe('example-plugin');
  });

  it('ignores a credential whose declared quota provider is no longer installed', () => {
    registerPluginQuotaProviders([{ provider: 'example-plugin' }]);

    const retired = authFile({ quota_provider: 'retired-plugin' });
    expect(resolvePluginQuotaProvider(retired)).toBeNull();
    expect(isPluginQuotaCredential(retired)).toBe(false);
  });

  it('trusts the declared binding before the catalogue is loaded', () => {
    const file = authFile({ quota_provider: 'example-plugin' });
    expect(resolvePluginQuotaProvider(file)).toBe('example-plugin');
    expect(isPluginQuotaCredential(file)).toBe(true);
  });

  it('does not treat an unbound credential as a plugin credential', () => {
    registerPluginQuotaProviders([{ provider: 'example-plugin' }]);
    expect(resolvePluginQuotaProvider(authFile({ provider: 'codex' }))).toBeNull();
    expect(resolvePluginQuotaProvider(undefined)).toBeNull();
  });
});

describe('plugin quota items', () => {
  it('drops items without a plugin key and keeps the plugin value shape', () => {
    expect(
      normalizePluginQuotaItems([
        { key: ' credit_remaining ', label: ' 剩余额度 ', value: 1739.5, unit: ' credit ' },
        { key: '', label: 'missing key', value: 1 },
        { key: 'balance', value: 5, format: ' Currency ', currency: 'usd' },
        { key: 'note', value: null },
      ])
    ).toEqual([
      { key: 'credit_remaining', label: '剩余额度', value: 1739.5, unit: 'credit' },
      { key: 'balance', label: 'balance', value: 5, format: 'currency', currency: 'USD' },
      { key: 'note', label: 'note', value: null },
    ]);
  });

  it('formats numbers, currency, percentages, booleans and text', () => {
    expect(
      formatPluginQuotaItemValue({ key: 'a', label: 'a', value: 1739.5, format: 'number' }, 'en-US')
    ).toBe('1,739.5');
    expect(
      formatPluginQuotaItemValue(
        { key: 'a', label: 'a', value: 5, format: 'currency', currency: 'USD' },
        'en-US'
      )
    ).toBe('$5.00');
    expect(
      formatPluginQuotaItemValue({ key: 'a', label: 'a', value: 12.5, format: 'percent' }, 'en-US')
    ).toBe('12.5%');
    expect(
      formatPluginQuotaItemValue({ key: 'a', label: 'a', value: 0, format: 'boolean' })
    ).toBe('—');
    expect(
      formatPluginQuotaItemValue({ key: 'a', label: 'a', value: 1, format: 'boolean' })
    ).toBe('✓');
    expect(formatPluginQuotaItemValue({ key: 'a', label: 'a', value: 'unknown' })).toBe('unknown');
  });

  it('appends the plugin unit to non formatted amounts', () => {
    expect(
      formatPluginQuotaItemAmount({ key: 'a', label: 'a', value: 1739.5, unit: 'credit', format: 'number' })
    ).toBe('1,739.5 credit');
    expect(
      formatPluginQuotaItemAmount({
        key: 'a',
        label: 'a',
        value: 5,
        unit: 'USD',
        format: 'currency',
        currency: 'USD',
      })
    ).toBe('$5.00');
  });
});

describe('fetchPluginQuota', () => {
  it('reads the credential through the Manager Server and returns its items', async () => {
    const credential = vi.spyOn(pluginQuotaApi, 'credential').mockResolvedValue({
      provider: 'example-plugin',
      plugin_id: 'example-plugin',
      display_name: 'Example quota',
      supports_reset: false,
      observed_at_ms: 1_700_000_000_000,
      items: [{ key: 'credit_remaining', label: '剩余额度', value: 1739.5, unit: 'credit' }],
    });

    const data = await fetchPluginQuota(authFile(), t, undefined, {
      managerServiceBase: 'http://manager.local:18317',
      managementKey: 'manager-key',
    });

    expect(credential).toHaveBeenCalledWith(
      'http://manager.local:18317',
      'manager-key',
      'auth-index-1'
    );
    expect(data.provider).toBe('example-plugin');
    expect(data.observedAtMs).toBe(1_700_000_000_000);
    expect(data.items).toHaveLength(1);
  });

  it('rejects a credential without an auth index', async () => {
    const credential = vi.spyOn(pluginQuotaApi, 'credential');
    await expect(
      fetchPluginQuota(authFile({ auth_index: '', authIndex: null }), t, undefined, {
        managerServiceBase: 'http://manager.local:18317',
      })
    ).rejects.toThrow('plugin_quota.missing_identity');
    expect(credential).not.toHaveBeenCalled();
  });

  it('rejects a refresh without a Manager Server connection', async () => {
    await expect(fetchPluginQuota(authFile(), t)).rejects.toThrow(
      'plugin_quota.manager_unavailable'
    );
  });

  it('rejects an empty plugin summary', async () => {
    vi.spyOn(pluginQuotaApi, 'credential').mockResolvedValue({ items: [] });
    await expect(
      fetchPluginQuota(authFile(), t, undefined, {
        managerServiceBase: 'http://manager.local:18317',
      })
    ).rejects.toThrow('plugin_quota.empty_data');
  });

  it('drops a superseded response', async () => {
    vi.spyOn(pluginQuotaApi, 'credential').mockResolvedValue({
      items: [{ key: 'credit_remaining', value: 1 }],
    });
    await expect(
      fetchPluginQuota(authFile(), t, undefined, {
        managerServiceBase: 'http://manager.local:18317',
        isCurrent: () => false,
      })
    ).rejects.toThrow('plugin_quota.stale_response');
  });
});
