import type { TFunction } from 'i18next';
import type { AuthFileItem, PluginQuotaData, PluginQuotaItem } from '@/types';
import type { QuotaFetchContext } from './metaQuota';
import type { AuthFilesApiRequestScope } from '@/services/api';
import {
  pluginQuotaApi,
  readAuthFileQuotaProvider,
  type PluginQuotaProviderDescriptor,
} from '@/services/api/pluginQuota';
import { normalizeAuthIndex } from '@/utils/authIndex';

/**
 * Plugin quota providers are named by CPA, never by the panel. The catalogue is
 * loaded from the Manager Server at runtime and every lookup below is keyed by
 * the binding CPA itself reports on the auth file, so a provider the panel has
 * never heard of still resolves.
 */
const pluginQuotaCatalogue = new Map<string, PluginQuotaProviderDescriptor>();
const catalogueListeners = new Set<() => void>();
let catalogueVersion = 0;

/**
 * The catalogue is published by the Manager Server at runtime, so panel rows
 * subscribe to it instead of importing a static provider list.
 */
export const subscribePluginQuotaProviders = (listener: () => void): (() => void) => {
  catalogueListeners.add(listener);
  return () => {
    catalogueListeners.delete(listener);
  };
};

export const getPluginQuotaCatalogueVersion = (): number => catalogueVersion;

const notifyCatalogueChanged = (): void => {
  catalogueVersion += 1;
  for (const listener of catalogueListeners) listener();
};

export const normalizePluginQuotaProviderKey = (value: unknown): string =>
  typeof value === 'string' ? value.trim().toLowerCase().replace(/_/g, '-') : '';

export const registerPluginQuotaProviders = (
  providers: PluginQuotaProviderDescriptor[] | undefined
): void => {
  pluginQuotaCatalogue.clear();
  for (const provider of providers ?? []) {
    const key = normalizePluginQuotaProviderKey(provider?.provider ?? provider?.plugin_id);
    if (!key || pluginQuotaCatalogue.has(key)) continue;
    pluginQuotaCatalogue.set(key, provider);
  }
  notifyCatalogueChanged();
};

export const clearPluginQuotaProviders = (): void => {
  pluginQuotaCatalogue.clear();
  notifyCatalogueChanged();
};

export const getPluginQuotaProviderCatalogue = (): PluginQuotaProviderDescriptor[] => [
  ...pluginQuotaCatalogue.values(),
];

export const getPluginQuotaProviderDescriptor = (
  provider: string
): PluginQuotaProviderDescriptor | null =>
  pluginQuotaCatalogue.get(normalizePluginQuotaProviderKey(provider)) ?? null;

/**
 * Resolves the plugin quota provider that owns one credential. CPA's own
 * quota_provider binding wins; the catalogue's supported credentials are the
 * fallback for a CPA build that reports the binding only on the catalogue.
 * While the catalogue is still empty the declared binding is trusted, so a
 * refresh started before the catalogue load still works.
 */
export const resolvePluginQuotaProvider = (file: AuthFileItem | undefined): string | null => {
  if (!file) return null;
  const declared = normalizePluginQuotaProviderKey(readAuthFileQuotaProvider(file));
  const catalogueLoaded = pluginQuotaCatalogue.size > 0;
  if (declared && (!catalogueLoaded || pluginQuotaCatalogue.has(declared))) {
    return declared;
  }
  const credentialProvider = normalizePluginQuotaProviderKey(
    typeof file.provider === 'string' ? file.provider : ''
  );
  if (!credentialProvider) return null;
  for (const [key, descriptor] of pluginQuotaCatalogue) {
    const supported = (descriptor.supported_providers ?? [])
      .map(normalizePluginQuotaProviderKey)
      .filter(Boolean);
    if (supported.includes(credentialProvider)) return key;
  }
  return null;
};

export const isPluginQuotaCredential = (file: AuthFileItem | undefined): boolean =>
  resolvePluginQuotaProvider(file) !== null;

export const isPluginQuotaProvider = (provider: string): boolean =>
  pluginQuotaCatalogue.has(normalizePluginQuotaProviderKey(provider));

export const loadPluginQuotaProviders = async (
  base: string,
  managementKey?: string,
  signal?: AbortSignal
): Promise<PluginQuotaProviderDescriptor[]> => {
  const response = await pluginQuotaApi.providers(base, managementKey, signal);
  registerPluginQuotaProviders(response?.providers);
  return getPluginQuotaProviderCatalogue();
};

export const fetchPluginQuota = async (
  file: AuthFileItem,
  t: TFunction,
  requestScope?: AuthFilesApiRequestScope,
  context?: QuotaFetchContext
): Promise<PluginQuotaData> => {
  const authIndex = normalizeAuthIndex(file?.auth_index ?? file?.authIndex);
  if (!authIndex) {
    throw new Error(t('plugin_quota.missing_identity'));
  }
  const base = (context?.managerServiceBase ?? requestScope?.apiBase ?? '').trim();
  if (!base) {
    throw new Error(t('plugin_quota.manager_unavailable'));
  }
  const response = await pluginQuotaApi.credential(
    base,
    context?.managementKey ?? requestScope?.managementKey,
    authIndex
  );
  if (context?.isCurrent && !context.isCurrent()) {
    throw new Error(t('plugin_quota.stale_response'));
  }
  const items = normalizePluginQuotaItems(response?.items);
  if (items.length === 0) {
    throw new Error(t('plugin_quota.empty_data'));
  }
  return {
    provider: normalizePluginQuotaProviderKey(response?.provider) || authIndex,
    pluginId: typeof response?.plugin_id === 'string' ? response.plugin_id : '',
    displayName: typeof response?.display_name === 'string' ? response.display_name : '',
    supportsReset: response?.supports_reset === true,
    observedAtMs:
      typeof response?.observed_at_ms === 'number' && Number.isFinite(response.observed_at_ms)
        ? response.observed_at_ms
        : Date.now(),
    items,
  };
};

export const normalizePluginQuotaItems = (
  items: Array<Partial<PluginQuotaItem>> | undefined
): PluginQuotaItem[] => {
  const result: PluginQuotaItem[] = [];
  for (const item of items ?? []) {
    const key = typeof item?.key === 'string' ? item.key.trim() : '';
    if (!key) continue;
    result.push({
      key,
      label: typeof item.label === 'string' && item.label.trim() ? item.label.trim() : key,
      value:
        typeof item.value === 'number' || typeof item.value === 'string' || typeof item.value === 'boolean'
          ? item.value
          : null,
      ...(typeof item.unit === 'string' && item.unit.trim() ? { unit: item.unit.trim() } : {}),
      ...(typeof item.format === 'string' && item.format.trim()
        ? { format: item.format.trim().toLowerCase() }
        : {}),
      ...(typeof item.currency === 'string' && item.currency.trim()
        ? { currency: item.currency.trim().toUpperCase() }
        : {}),
    });
  }
  return result;
};

/**
 * Renders one plugin reading. The plugin owns the format hint, so an unknown
 * format still renders its raw value instead of disappearing.
 */
export const formatPluginQuotaItemValue = (
  item: PluginQuotaItem,
  locale?: string
): string => {
  const format = (item.format ?? '').toLowerCase();
  if (typeof item.value === 'boolean' || format === 'boolean') {
    const enabled =
      typeof item.value === 'boolean' ? item.value : Number(item.value ?? 0) !== 0;
    return enabled ? '✓' : '—';
  }
  if (typeof item.value === 'string') return item.value;
  if (typeof item.value !== 'number' || !Number.isFinite(item.value)) return '—';
  if (format === 'currency') {
    const currency = item.currency || 'USD';
    try {
      return new Intl.NumberFormat(locale, { style: 'currency', currency }).format(item.value);
    } catch {
      return `${item.value} ${currency}`;
    }
  }
  if (format === 'percent') {
    return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 2 }).format(item.value)}%`;
  }
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 4 }).format(item.value);
};

/**
 * Composes the amount label the panel shows next to a plugin item, e.g.
 * `1739.5 credit` or `$5.00`.
 */
export const formatPluginQuotaItemAmount = (item: PluginQuotaItem, locale?: string): string => {
  const value = formatPluginQuotaItemValue(item, locale);
  const format = (item.format ?? '').toLowerCase();
  if (!item.unit || format === 'currency' || format === 'boolean') return value;
  return `${value} ${item.unit}`;
};
