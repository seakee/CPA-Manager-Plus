import i18n from '@/i18n';
import { keyWorkspaceText } from '@/i18n/freshKeyWorkspace';
import { fullWebLanguage } from '@/i18n/fullweb';
import type { ApiKeyAlias } from '@/services/api/usageService';
import { sha256Hex } from '@/utils/apiKeyHash';
import { maskApiKey, maskSensitiveText } from '@/utils/format';
import { formatApiKeyHashLabel, readString } from './base';

export type ApiKeyDisplayInfo = {
  label: string;
  masked: string;
  displayName?: string;
  copyValue?: string;
  apiKeyId?: string;
  maskedPreview?: string;
  origin?: string;
  lifecycle?: string;
  isRevoked?: boolean;
  isRotated?: boolean;
};

export const isMaskedApiKeyPreview = (value: string) => {
  const trimmed = readString(value);
  if (!trimmed) return false;
  return trimmed.includes('…') || trimmed.includes('***');
};

export const sanitizeApiKeyDisplayText = (value: string, fallback = '') => {
  const trimmed = readString(value);
  if (!trimmed) return fallback;
  return maskSensitiveText(trimmed) || fallback;
};

const resolveLanguage = (language?: string) =>
  language || i18n?.resolvedLanguage || i18n?.language || fullWebLanguage();

const translateOrFallback = (key: string, fallback: string, language?: string) => {
  if (typeof i18n?.t !== 'function') return fallback;
  const translated = i18n.t(key, {
    ...(language ? { lng: language } : {}),
    defaultValue: fallback,
  });
  return translated && translated !== key ? translated : fallback;
};

export const resolveRevokedBadgeText = (language?: string) => {
  const resolvedLanguage = resolveLanguage(language);
  const fallback = keyWorkspaceText('statusRevoked', resolvedLanguage);
  return translateOrFallback('fullweb.keysLifecycleRevoked', fallback, resolvedLanguage);
};

export const resolveRotatedBadgeText = (language?: string) => {
  const resolvedLanguage = resolveLanguage(language);
  return keyWorkspaceText('statusRotated', resolvedLanguage);
};

export const resolveOriginDefaultName = (origin: string | undefined, language?: string) => {
  const resolvedLanguage = resolveLanguage(language);
  if (origin === 'installer') {
    return translateOrFallback(
      'fullweb.keysOriginInstaller',
      keyWorkspaceText('installerOrigin', resolvedLanguage),
      resolvedLanguage
    );
  }
  if (origin === 'adopted') {
    return translateOrFallback(
      'fullweb.keysOriginAdopted',
      keyWorkspaceText('adoptedOrigin', resolvedLanguage),
      resolvedLanguage
    );
  }
  return '';
};

export const formatApiKeyDisplayName = (
  label: string,
  masked: string,
  isRevoked = false,
  language?: string,
  isRotated = false
) => {
  const cleanLabel = readString(label);
  const cleanMasked = readString(masked);
  let base = cleanLabel || cleanMasked;
  if (
    cleanLabel &&
    cleanMasked &&
    cleanLabel !== cleanMasked &&
    !cleanMasked.startsWith('sha256:')
  ) {
    base = `${cleanLabel} (${cleanMasked})`;
  }
  if (!base) return '';
  if (isRevoked) return `${base} · ${resolveRevokedBadgeText(language)}`;
  if (isRotated) return `${base} · ${resolveRotatedBadgeText(language)}`;
  return base;
};

export const resolveApiKeyDisplayName = (
  info: ApiKeyDisplayInfo | undefined,
  language?: string
) => {
  if (!info) return '';
  const explicitDisplayName = readString(info.displayName);
  if (explicitDisplayName) return explicitDisplayName;
  const label = readString(info.label) || readString(info.maskedPreview) || readString(info.masked);
  const masked = readString(info.maskedPreview) || readString(info.masked);
  const isRevoked = Boolean(info.isRevoked || info.lifecycle === 'superseded');
  const isRotated = !isRevoked && Boolean(info.isRotated || info.lifecycle === 'rotated');
  return formatApiKeyDisplayName(label, masked, isRevoked, language, isRotated);
};

export const buildApiKeyDisplayMap = (
  apiKeys: string[] = [],
  apiKeyAliases: ApiKeyAlias[] = [],
  language?: string
): Map<string, ApiKeyDisplayInfo> => {
  const map = new Map<string, ApiKeyDisplayInfo>();
  const resolvedLanguage = resolveLanguage(language);
  const normalizedKeys = Array.isArray(apiKeys) ? apiKeys : [];
  const normalizedAliases = Array.isArray(apiKeyAliases) ? apiKeyAliases : [];

  normalizedKeys.forEach((rawValue) => {
    const apiKey = readString(rawValue);
    if (!apiKey || isMaskedApiKeyPreview(apiKey)) return;
    const hash = sha256Hex(apiKey).toLowerCase();
    if (!hash || map.has(hash)) return;
    const masked = maskApiKey(apiKey) || formatApiKeyHashLabel(hash);
    map.set(hash, {
      label: masked,
      masked,
      displayName: masked,
      copyValue: apiKey,
    });
  });

  normalizedAliases.forEach((entry) => {
    const hash = readString(entry.apiKeyHash).toLowerCase();
    if (!hash) return;
    const existing = map.get(hash);
    const apiKeyId = readString(entry.apiKeyId) || undefined;
    const maskedPreview = readString(entry.maskedPreview) || undefined;
    const origin = readString(entry.origin) || undefined;
    const lifecycle = readString(entry.lifecycle) || undefined;
    const isManagedEntry = Boolean(apiKeyId || maskedPreview || origin || lifecycle);

    if (isManagedEntry) {
      const explicitAlias = sanitizeApiKeyDisplayText(readString(entry.alias));
      const effectiveName = explicitAlias || resolveOriginDefaultName(origin, resolvedLanguage);
      const masked = maskedPreview || existing?.masked || formatApiKeyHashLabel(hash);
      const isRevoked = lifecycle === 'superseded';
      const isRotated = lifecycle === 'rotated';
      const label = effectiveName || masked;
      const displayName = formatApiKeyDisplayName(
        effectiveName,
        masked,
        isRevoked,
        resolvedLanguage,
        isRotated
      );
      map.set(hash, {
        label,
        masked,
        displayName,
        ...(existing?.copyValue ? { copyValue: existing.copyValue } : {}),
        ...(apiKeyId ? { apiKeyId } : {}),
        ...(maskedPreview ? { maskedPreview } : {}),
        ...(origin ? { origin } : {}),
        ...(lifecycle ? { lifecycle } : {}),
        ...(isRevoked ? { isRevoked } : {}),
        ...(isRotated ? { isRotated } : {}),
      });
      return;
    }

    const alias = sanitizeApiKeyDisplayText(readString(entry.alias));
    if (!alias) return;
    const masked = existing?.masked || existing?.label || formatApiKeyHashLabel(hash);
    const displayName = formatApiKeyDisplayName(alias, masked, false, resolvedLanguage);
    map.set(hash, {
      label: alias,
      masked,
      displayName,
      ...(existing?.copyValue ? { copyValue: existing.copyValue } : {}),
    });
  });

  return map;
};

export const shouldPreferApiKeyAlias = (label: string, masked: string) =>
  Boolean(label) && label !== masked && !label.startsWith('sha256:');
