import type { ApiKeyAlias } from '@/services/api/usageService';
import { sha256Hex } from '@/utils/apiKeyHash';
import { maskApiKey, maskSensitiveText } from '@/utils/format';
import { formatApiKeyHashLabel, readString } from './base';

export type ApiKeyDisplayInfo = {
  label: string;
  masked: string;
  copyValue?: string;
  source?: string;
};

/** Catalog entry used only for display-map merge (plugin public metadata). */
export type PluginKeyCatalogEntry = {
  id: string;
  name?: string;
  keyPreview?: string;
  apiKeyHash: string;
  source?: string;
};

export const sanitizeApiKeyDisplayText = (value: string, fallback = '') => {
  const trimmed = readString(value);
  if (!trimmed) return fallback;
  return maskSensitiveText(trimmed) || fallback;
};

/**
 * Mirrors how Manager derives usage_events.api_key_hash: sha256(TrimSpace(value)).
 * Hashing an untrimmed value would silently fail to match recorded usage rows.
 */
const hashApiKeyValue = (value: string): string => {
  const trimmed = readString(value);
  return trimmed ? sha256Hex(trimmed).toLowerCase() : '';
};

/**
 * Build hash → display info map.
 * Priority: manual api_key_aliases > native configured key > plugin name >
 * plugin id / preview > sha256 label.
 * Plugin entries never set copyValue (id is not a callable secret).
 */
export const buildApiKeyDisplayMap = (
  apiKeys: string[] = [],
  apiKeyAliases: ApiKeyAlias[] = [],
  pluginKeys: PluginKeyCatalogEntry[] = []
): Map<string, ApiKeyDisplayInfo> => {
  const map = new Map<string, ApiKeyDisplayInfo>();

  apiKeys.forEach((apiKey) => {
    const hash = hashApiKeyValue(apiKey);
    if (!hash || map.has(hash)) return;
    const masked = maskApiKey(apiKey) || formatApiKeyHashLabel(hash);
    map.set(hash, { label: masked, masked, copyValue: apiKey });
  });

  pluginKeys.forEach((entry) => {
    const hash = readString(entry.apiKeyHash).toLowerCase() || hashApiKeyValue(entry.id);
    if (!hash) return;
    const existing = map.get(hash);
    // Do not overwrite a native configured key's copyValue/mask with plugin data.
    if (existing?.copyValue) return;

    const name = sanitizeApiKeyDisplayText(readString(entry.name));
    const idLabel = sanitizeApiKeyDisplayText(readString(entry.id));
    const preview = sanitizeApiKeyDisplayText(readString(entry.keyPreview));
    const label = name || idLabel || preview || formatApiKeyHashLabel(hash);
    const masked = preview || idLabel || formatApiKeyHashLabel(hash);
    if (existing) {
      // Keep prior label only if it already looks like a preferred name; else plugin name/id wins over bare hash.
      const preferExisting =
        Boolean(existing.label) &&
        existing.label !== existing.masked &&
        !existing.label.startsWith('sha256:');
      map.set(hash, {
        label: preferExisting ? existing.label : label,
        masked: existing.masked || masked,
        source: entry.source || existing.source,
      });
      return;
    }
    map.set(hash, {
      label,
      masked,
      source: entry.source,
    });
  });

  apiKeyAliases.forEach((entry) => {
    const hash = readString(entry.apiKeyHash).toLowerCase();
    const alias = sanitizeApiKeyDisplayText(readString(entry.alias));
    if (!hash || !alias) return;
    const existing = map.get(hash);
    map.set(hash, {
      label: alias,
      masked: existing?.masked || existing?.label || formatApiKeyHashLabel(hash),
      copyValue: existing?.copyValue,
      source: existing?.source,
    });
  });
  return map;
};

/**
 * Deduped count of currently configured caller keys (native CPA apiKeys +
 * plugin key hashes). `includePluginKeys` must be false whenever the catalog
 * read did not succeed, so a stale or missing catalog is never counted as if it
 * were current.
 */
export const countConfiguredApiKeys = (
  apiKeys: string[] = [],
  pluginKeys: Array<Pick<PluginKeyCatalogEntry, 'apiKeyHash' | 'id'>> = [],
  includePluginKeys = true
): number => {
  const hashes = new Set<string>();
  apiKeys.forEach((apiKey) => {
    const hash = hashApiKeyValue(apiKey);
    if (hash) hashes.add(hash);
  });
  if (includePluginKeys) {
    pluginKeys.forEach((entry) => {
      const hash = readString(entry.apiKeyHash).toLowerCase() || hashApiKeyValue(entry.id);
      if (hash) hashes.add(hash);
    });
  }
  return hashes.size;
};

export const shouldPreferApiKeyAlias = (label: string, masked: string) =>
  Boolean(label) && label !== masked && !label.startsWith('sha256:');
