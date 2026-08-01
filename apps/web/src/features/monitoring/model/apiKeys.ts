import type { ApiKeyAlias } from '@/services/api/usageService';
import { sha256Hex } from '@/utils/apiKeyHash';
import { maskApiKey, maskSensitiveText } from '@/utils/format';
import { formatApiKeyHashLabel, readString } from './base';

export type ApiKeyDisplayInfo = {
  label: string;
  masked: string;
  copyValue?: string;
};

export const sanitizeApiKeyDisplayText = (value: string, fallback = '') => {
  const trimmed = readString(value);
  if (!trimmed) return fallback;
  return maskSensitiveText(trimmed) || fallback;
};

export const buildApiKeyDisplayMap = (
  apiKeys: string[] = [],
  apiKeyAliases: ApiKeyAlias[] = []
): Map<string, ApiKeyDisplayInfo> => {
  const map = new Map<string, ApiKeyDisplayInfo>();
  apiKeys.forEach((apiKey) => {
    const hash = sha256Hex(apiKey).toLowerCase();
    if (!hash || map.has(hash)) return;
    const masked = maskApiKey(apiKey) || formatApiKeyHashLabel(hash);
    map.set(hash, { label: masked, masked, copyValue: apiKey });
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
    });
  });
  return map;
};

export const shouldPreferApiKeyAlias = (label: string, masked: string) =>
  Boolean(label) && label !== masked && !label.startsWith('sha256:');
