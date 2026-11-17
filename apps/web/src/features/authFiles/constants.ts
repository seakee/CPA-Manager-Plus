import type { TFunction } from 'i18next';
import iconAntigravity from '@/assets/icons/antigravity.svg';
import iconClaude from '@/assets/icons/claude.svg';
import iconCodex from '@/assets/icons/codex.svg';
import iconGemini from '@/assets/icons/gemini.svg';
import iconGrok from '@/assets/icons/grok.svg';
import iconGrokDark from '@/assets/icons/grok-dark.svg';
import iconIflow from '@/assets/icons/iflow.svg';
import iconKimiDark from '@/assets/icons/kimi-dark.svg';
import iconKimiLight from '@/assets/icons/kimi-light.svg';
import iconQwen from '@/assets/icons/qwen.svg';
import iconVertex from '@/assets/icons/vertex.svg';
import type { AuthFileItem } from '@/types';
import { parseTimestamp } from '@/utils/timestamp';

export type ResolvedTheme = 'light' | 'dark';
export type AuthFileModelItem = {
  id: string;
  display_name?: string;
  type?: string;
  owned_by?: string;
};
export type AuthFileIconAsset = string | { light: string; dark: string };

export type QuotaProviderType = 'antigravity' | 'claude' | 'codex' | 'kimi' | 'xai';
export type OAuthConfigLoadState = 'loading' | 'ready' | 'unsupported' | 'error';

export const QUOTA_PROVIDER_TYPES = new Set<QuotaProviderType>([
  'antigravity',
  'claude',
  'codex',
  'kimi',
  'xai',
]);

export const AUTH_FILE_REFRESH_WARNING_MS = 24 * 60 * 60 * 1000;

export const AUTH_FILE_HEALTHY_STATUS_MESSAGES = new Set([
  'ok',
  'healthy',
  'ready',
  'success',
  'available',
]);

export const isHealthyAuthFileStatusMessage = (value: string): boolean =>
  AUTH_FILE_HEALTHY_STATUS_MESSAGES.has(value.trim().toLowerCase());

export const INTEGER_STRING_PATTERN = /^[+-]?\d+$/;
export const TRUTHY_TEXT_VALUES = new Set(['true', '1', 'yes', 'y', 'on']);
export const FALSY_TEXT_VALUES = new Set(['false', '0', 'no', 'n', 'off']);
export const AUTH_FILE_WEBSOCKET_PROVIDERS = new Set(['codex', 'xai']);
export const supportsAuthFileUsingApi = (provider: string) =>
  normalizeProviderKey(provider) === 'xai';


export const AUTH_FILE_ICONS: Record<string, AuthFileIconAsset> = {
  antigravity: iconAntigravity,
  aistudio: iconGemini,
  claude: iconClaude,
  codex: iconCodex,
  gemini: iconGemini,
  xai: { light: iconGrok, dark: iconGrokDark },
  iflow: iconIflow,
  kimi: { light: iconKimiLight, dark: iconKimiDark },
  qwen: iconQwen,
  vertex: iconVertex,
};

export const resolveQuotaErrorMessage = (
  t: TFunction,
  status: number | undefined,
  fallback: string
): string => {
  if (status === 404) return t('common.quota_update_required');
  if (status === 403) return t('common.quota_check_credential');
  return fallback;
};

export const normalizeProviderKey = (value: string) => {
  const key = value.trim().toLowerCase().replace(/_/g, '-');
  if (key === 'x-ai' || key === 'grok') return 'xai';
  return key;
};

export const getEquivalentProviderKeys = (value: string): string[] => {
  const providerKey = normalizeProviderKey(value);
  if (!providerKey) return [];
  if (providerKey === 'gemini') return ['gemini', 'gemini-cli'];
  if (providerKey === 'gemini-cli') return ['gemini-cli', 'gemini'];
  return [providerKey];
};

export const getProviderRecordValues = <T>(record: Record<string, T>, provider: string): T[] => {
  const providerKeys = getEquivalentProviderKeys(provider);
  if (providerKeys.length === 0) return [];
  const entries = Object.entries(record);
  return providerKeys.flatMap((providerKey) =>
    entries.filter(([key]) => normalizeProviderKey(key) === providerKey).map(([, value]) => value)
  );
};

export const getAuthFileStatusMessage = (file: AuthFileItem): string => {
  const raw = file['status_message'] ?? file.statusMessage;
  if (typeof raw === 'string') return raw.trim();
  if (raw == null) return '';
  return String(raw).trim();
};

export const hasAuthFileStatusMessage = (file: AuthFileItem): boolean =>
  getAuthFileStatusMessage(file).length > 0;

export const isHealthyAuthFile = (file: AuthFileItem): boolean =>
  file.disabled !== true && !hasAuthFileStatusMessage(file);

export const getTypeLabel = (t: TFunction, type: string): string => {
  const providerKey = normalizeProviderKey(type);
  const key = `auth_files.filter_${providerKey}`;
  const translated = t(key);
  if (translated !== key) return translated;
  if (providerKey === 'iflow') return 'iFlow';
  return type.charAt(0).toUpperCase() + type.slice(1);
};


export const getAuthFileIcon = (type: string, resolvedTheme: ResolvedTheme): string | null => {
  const iconEntry = AUTH_FILE_ICONS[normalizeProviderKey(type)];
  if (!iconEntry) return null;
  return typeof iconEntry === 'string'
    ? iconEntry
    : resolvedTheme === 'dark'
      ? iconEntry.dark
      : iconEntry.light;
};

export const parsePriorityValue = (value: unknown): number | undefined => {
  if (typeof value === 'number') {
    return Number.isInteger(value) ? value : undefined;
  }

  if (typeof value !== 'string') return undefined;
  const trimmed = value.trim();
  if (!trimmed || !INTEGER_STRING_PATTERN.test(trimmed)) return undefined;
  const parsed = Number.parseInt(trimmed, 10);
  return Number.isSafeInteger(parsed) ? parsed : undefined;
};

export const normalizeExcludedModels = (value: unknown): string[] => {
  if (!Array.isArray(value)) return [];

  const seen = new Set<string>();
  const normalized: string[] = [];
  value.forEach((entry) => {
    const model = String(entry ?? '')
      .trim()
      .toLowerCase();
    if (!model || seen.has(model)) return;
    seen.add(model);
    normalized.push(model);
  });

  return normalized.sort((a, b) => a.localeCompare(b));
};

export const parseExcludedModelsText = (value: string): string[] =>
  normalizeExcludedModels(value.split(/[\n,]+/));

export const parseDisableCoolingValue = (value: unknown): boolean | undefined => {
  if (typeof value === 'boolean') return value;
  if (typeof value === 'number' && Number.isFinite(value)) return value !== 0;
  if (typeof value !== 'string') return undefined;

  const normalized = value.trim().toLowerCase();
  if (!normalized) return undefined;
  if (TRUTHY_TEXT_VALUES.has(normalized)) return true;
  if (FALSY_TEXT_VALUES.has(normalized)) return false;
  return undefined;
};

export const supportsAuthFileWebsockets = (providerKey: string): boolean =>
  AUTH_FILE_WEBSOCKET_PROVIDERS.has(normalizeProviderKey(providerKey));

export const readAuthFileWebsockets = (value: Record<string, unknown>): boolean =>
  parseDisableCoolingValue(value.websockets ?? value.websocket) ?? false;

export const applyAuthFileWebsockets = (
  value: Record<string, unknown>,
  websockets: boolean
): Record<string, unknown> => {
  const next = { ...value };
  delete next.websocket;
  next.websockets = websockets;
  return next;
};

export const readCodexAuthFileWebsockets = readAuthFileWebsockets;
export const applyCodexAuthFileWebsockets = applyAuthFileWebsockets;

export function isRuntimeOnlyAuthFile(file: AuthFileItem): boolean {
  const raw = file['runtime_only'] ?? file.runtimeOnly;
  if (typeof raw === 'boolean') return raw;
  if (typeof raw === 'string') return raw.trim().toLowerCase() === 'true';
  return false;
}

export const formatModified = (item: AuthFileItem): string => {
  const raw = item['modtime'] ?? item.modified;
  if (!raw) return '-';
  const asNumber = Number(raw);
  const date =
    Number.isFinite(asNumber) && !Number.isNaN(asNumber)
      ? new Date(asNumber < 1e12 ? asNumber * 1000 : asNumber)
      : (parseTimestamp(raw) ?? new Date(String(raw)));
  return Number.isNaN(date.getTime()) ? '-' : date.toLocaleString();
};

// 检查模型是否被 OAuth 排除
export const isModelExcluded = (
  modelId: string,
  providerType: string,
  excluded: Record<string, string[]>
): boolean => {
  const excludedModels = getProviderRecordValues(excluded, providerType).flat();
  return excludedModels.some((pattern) => {
    if (pattern.includes('*')) {
      // 支持通配符匹配：先转义正则特殊字符，再将 * 视为通配符
      const regexSafePattern = pattern
        .split('*')
        .map((segment) => segment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
        .join('.*');
      const regex = new RegExp(`^${regexSafePattern}$`, 'i');
      return regex.test(modelId);
    }
    return pattern.toLowerCase() === modelId.toLowerCase();
  });
};
