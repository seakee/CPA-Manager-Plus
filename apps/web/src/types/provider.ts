/**
 * AI 提供商相关类型
 * 基于原项目 src/modules/ai-providers.js
 */

/** Internal UI-to-serializer marker; never serialized into CPA configuration. */
export const MODEL_THINKING_LEVELS_CLEAR_MARKER = Symbol('model-thinking-levels-clear');
/** Internal UI-to-serializer marker; never serialized into CPA configuration. */
export const MODEL_THINKING_LEVELS_EDIT_MARKER = Symbol('model-thinking-levels-edit');

type ThinkingLevelsClearMarkerCarrier = {
  [MODEL_THINKING_LEVELS_CLEAR_MARKER]?: true;
};

type ThinkingLevelsEditMarkerCarrier = {
  [MODEL_THINKING_LEVELS_EDIT_MARKER]?: true;
};

export const hasModelThinkingLevelsClearMarker = (value: unknown): boolean => {
  if (value === null || typeof value !== 'object') return false;
  return (value as ThinkingLevelsClearMarkerCarrier)[MODEL_THINKING_LEVELS_CLEAR_MARKER] === true;
};

export const hasModelThinkingLevelsEditMarker = (value: unknown): boolean => {
  if (value === null || typeof value !== 'object') return false;
  return (value as ThinkingLevelsEditMarkerCarrier)[MODEL_THINKING_LEVELS_EDIT_MARKER] === true;
};

export const markModelThinkingLevelsForClear = <T extends object>(
  value: T
): T & { [MODEL_THINKING_LEVELS_CLEAR_MARKER]: true } => {
  delete (value as ThinkingLevelsEditMarkerCarrier)[MODEL_THINKING_LEVELS_EDIT_MARKER];
  Object.defineProperty(value, MODEL_THINKING_LEVELS_CLEAR_MARKER, {
    configurable: true,
    enumerable: false,
    value: true,
  });
  return value as T & { [MODEL_THINKING_LEVELS_CLEAR_MARKER]: true };
};

export const markModelThinkingLevelsForEdit = <T extends object>(
  value: T
): T & { [MODEL_THINKING_LEVELS_EDIT_MARKER]: true } => {
  delete (value as ThinkingLevelsClearMarkerCarrier)[MODEL_THINKING_LEVELS_CLEAR_MARKER];
  Object.defineProperty(value, MODEL_THINKING_LEVELS_EDIT_MARKER, {
    configurable: true,
    enumerable: false,
    value: true,
  });
  return value as T & { [MODEL_THINKING_LEVELS_EDIT_MARKER]: true };
};

/** Remove one-shot UI commands before a provider becomes committed state. */
export const stripModelThinkingLevelsMarkers = <T extends object>(value: T): T => {
  const next = { ...value } as T &
    ThinkingLevelsClearMarkerCarrier &
    ThinkingLevelsEditMarkerCarrier;
  delete next[MODEL_THINKING_LEVELS_CLEAR_MARKER];
  delete next[MODEL_THINKING_LEVELS_EDIT_MARKER];
  return next as T;
};

/** Backwards-compatible name; committed snapshots strip both thinking markers. */
export const stripModelThinkingLevelsClearMarker = <T extends object>(value: T): T => {
  return stripModelThinkingLevelsMarkers(value);
};

export const THINKING_ZERO_ALLOWED_FIELDS = [
  'zero_allowed',
  'zero-allowed',
  'zeroAllowed',
] as const;

export const THINKING_DYNAMIC_ALLOWED_FIELDS = [
  'dynamic_allowed',
  'dynamic-allowed',
  'dynamicAllowed',
] as const;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

export const hasThinkingFlag = (thinking: unknown, fields: readonly string[]): boolean =>
  isRecord(thinking) && fields.some((field) => thinking[field] === true);

export const removeThinkingFlagAliases = (
  thinking: Record<string, unknown>,
  fields: readonly string[]
): Record<string, unknown> => {
  const next = { ...thinking };
  fields.forEach((field) => delete next[field]);
  return next;
};

type ModelThinkingCarrier = {
  thinking?: Record<string, unknown>;
};

/** Materialize one-shot Thinking commands before a model becomes committed state. */
export const toCommittedModelThinkingSnapshot = <T extends object>(value: T): T => {
  const editThinkingLevels = hasModelThinkingLevelsEditMarker(value);
  const next = stripModelThinkingLevelsMarkers(value) as T & ModelThinkingCarrier;
  if (editThinkingLevels && isRecord(next.thinking)) {
    next.thinking = removeThinkingFlagAliases(next.thinking, [
      ...THINKING_ZERO_ALLOWED_FIELDS,
      ...THINKING_DYNAMIC_ALLOWED_FIELDS,
    ]);
  }
  return next;
};

export interface ModelAlias {
  name: string;
  alias?: string;
  priority?: number;
  testModel?: string;
  image?: boolean;
  forceMapping?: boolean;
  inputModalities?: string[];
  outputModalities?: string[];
  thinking?: Record<string, unknown>;
  [MODEL_THINKING_LEVELS_CLEAR_MARKER]?: true;
  [MODEL_THINKING_LEVELS_EDIT_MARKER]?: true;
}

export interface ApiKeyEntry {
  apiKey: string;
  weight?: number;
  proxyUrl?: string;
  headers?: Record<string, string>;
  authIndex?: string;
}

export interface CloakConfig {
  mode?: string;
  strictMode?: boolean;
  sensitiveWords?: string[];
  cacheUserId?: boolean;
}

/**
 * Claude request fingerprint profile. `''` keeps the caller-owned
 * fingerprint; `'claude-code-cli'` opts in to the Claude Code CLI
 * request fingerprint. `oauth-cli` is accepted upstream as a legacy
 * alias and is normalized to `'claude-code-cli'` on read.
 */
export type ClaudeFingerprintProfile = '' | 'claude-code-cli';

export interface GeminiKeyConfig {
  apiKey: string;
  priority?: number;
  weight?: number;
  prefix?: string;
  baseUrl?: string;
  proxyUrl?: string;
  models?: ModelAlias[];
  headers?: Record<string, string>;
  excludedModels?: string[];
  authIndex?: string;
  disableCooling?: boolean | null;
}

export interface ProviderKeyConfig {
  apiKey: string;
  priority?: number;
  weight?: number;
  prefix?: string;
  baseUrl?: string;
  websockets?: boolean;
  proxyUrl?: string;
  headers?: Record<string, string>;
  models?: ModelAlias[];
  excludedModels?: string[];
  cloak?: CloakConfig;
  authIndex?: string;
  disableCooling?: boolean | null;
  fingerprintProfile?: ClaudeFingerprintProfile;
  /**
   * @deprecated CPA compatibility only. Do not use for new writes;
   * use {@link fingerprintProfile} instead. Kept so older configs that
   * carry `experimental-cch-signing` round-trip losslessly.
   */
  experimentalCchSigning?: boolean;
  rebuildMidSystemMessage?: boolean;
}

export interface OpenAIProviderConfig {
  name: string;
  prefix?: string;
  baseUrl: string;
  apiKeyEntries: ApiKeyEntry[];
  disabled?: boolean;
  headers?: Record<string, string>;
  models?: ModelAlias[];
  priority?: number;
  testModel?: string;
  authIndex?: string;
  disableCooling?: boolean | null;
  [key: string]: unknown;
}
