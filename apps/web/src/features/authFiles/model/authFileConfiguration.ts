import {
  coolingPolicyFromOverride,
  coolingPolicyToOverride,
  readCredentialCoolingOverride,
  type AuthFileItem,
  type CoolingPolicy,
} from '@/types';
import type { AuthFileFieldsPatch } from '@/services/api/authFiles';
import {
  normalizeExcludedModels,
  normalizeProviderKey,
  parseDisableCoolingValue,
  readAuthFileWebsockets,
} from '@/features/authFiles/constants';
import {
  readAuthFileStatusAccountId,
  readAuthFileStatusAccountIdInvalid,
  readAuthFileStatusAccountSnapshot,
  readAuthFileStatusAuthIndex,
  readAuthFileStatusProvider,
  readAuthFileStatusCodexMember,
  readAuthFileStatusCodexMemberInvalid,
} from '@/utils/authFileStatusMutation';

export const AUTH_FILE_CONFIGURATION_INVALID_JSON = 'AUTH_FILE_CONFIGURATION_INVALID_JSON';
export const AUTH_FILE_CONFIGURATION_TARGET_NOT_FOUND = 'AUTH_FILE_CONFIGURATION_TARGET_NOT_FOUND';
export const XAI_OFFICIAL_API_BASE_URL = 'https://api.x.ai/v1';
export const AUTH_FILE_WEIGHT_MAX = 1_000_000;

export type XaiRoutingMode = 'grok-build' | 'official-api';

export type AuthFileConfigurationDraft = {
  prefix: string;
  proxyUrl: string;
  priority: string;
  weight: string;
  note: string;
  headersText: string;
  excludedModelsText: string;
  disableCooling: CoolingPolicy;
  requestRetry: string;
  websockets: boolean;
  xaiRoutingMode: XaiRoutingMode;
  baseUrl: string;
  cloakMode: string;
  cloakStrictMode: boolean;
  cloakSensitiveWordsText: string;
  cloakCacheUserId: boolean;
  toolPrefixDisabled: boolean;
};

export type AuthFileConfigurationErrorKey =
  | 'auth_files.headers_invalid_json'
  | 'auth_files.headers_invalid_object'
  | 'auth_files.headers_invalid_value'
  | 'accounts.config_error_priority_integer'
  | 'accounts.config_error_weight_integer'
  | 'accounts.config_error_weight_range'
  | 'accounts.config_error_request_retry_integer'
  | 'accounts.config_error_xai_base_url'
  | 'accounts.config_error_cloak_mode';

export type AuthFileConfigurationErrors = Partial<
  Record<keyof AuthFileConfigurationDraft, AuthFileConfigurationErrorKey>
>;

export type AuthFileConfigurationCapabilities = {
  websockets: boolean;
  xaiRouting: boolean;
  claudeCloak: boolean;
};

export type ParsedAuthFileConfigurationSource = {
  record: Record<string, unknown>;
  providerKey: string;
  recordIndex: number | null;
};

export type AuthFileConfigurationPatchResult = {
  patch: AuthFileFieldsPatch;
  errors: AuthFileConfigurationErrors;
};

type AuthFileHeaders = Record<string, string>;

const REDACTED_VALUE = '[redacted]';
const SENSITIVE_KEY_PARTS = [
  'apikey',
  'authorization',
  'authorizationcode',
  'bearer',
  'clientsecret',
  'codeverifier',
  'cookie',
  'credential',
  'devicecode',
  'jwt',
  'managementkey',
  'password',
  'privatekey',
  'secret',
  'sessionid',
];

const isRecordObject = (value: unknown): value is Record<string, unknown> =>
  Boolean(value) && typeof value === 'object' && !Array.isArray(value);

const readTrimmedString = (value: unknown): string =>
  typeof value === 'string' ? value.trim() : '';

const readIntegerText = (value: unknown): string => {
  if (typeof value === 'number' && Number.isFinite(value)) return String(value);
  if (typeof value === 'string') return value.trim();
  return '';
};

const readBoolean = (value: unknown): boolean => parseDisableCoolingValue(value) ?? false;

const readHeaders = (value: unknown): AuthFileHeaders => {
  if (!isRecordObject(value)) return {};
  const result: AuthFileHeaders = {};
  Object.entries(value).forEach(([rawName, rawValue]) => {
    if (typeof rawValue !== 'string') return;
    const name = rawName.trim();
    if (!name) return;
    result[name] = rawValue;
  });
  return result;
};

const normalizeHeaders = (value: AuthFileHeaders): AuthFileHeaders => {
  const result: AuthFileHeaders = {};
  Object.entries(value).forEach(([rawName, rawValue]) => {
    const name = rawName.trim();
    const headerValue = rawValue.trim();
    if (!name || !headerValue) return;
    result[name] = headerValue;
  });
  return result;
};

const parseHeadersText = (
  value: string
): { value: AuthFileHeaders | null; error?: AuthFileConfigurationErrorKey } => {
  const trimmed = value.trim();
  if (!trimmed) return { value: {} };

  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed) as unknown;
  } catch {
    return { value: null, error: 'auth_files.headers_invalid_json' };
  }
  if (!isRecordObject(parsed)) {
    return { value: null, error: 'auth_files.headers_invalid_object' };
  }
  if (!Object.values(parsed).every((item) => typeof item === 'string')) {
    return { value: null, error: 'auth_files.headers_invalid_value' };
  }
  return { value: parsed as AuthFileHeaders };
};

const buildHeadersPatch = (
  originalHeaders: AuthFileHeaders,
  nextHeaders: AuthFileHeaders
): AuthFileHeaders | undefined => {
  const patch: AuthFileHeaders = {};
  const original = normalizeHeaders(originalHeaders);
  const next = normalizeHeaders(nextHeaders);
  const nextNames = new Set(Object.keys(next));

  Object.entries(next).forEach(([name, value]) => {
    if (original[name] !== value) patch[name] = value;
  });
  Object.keys(original).forEach((name) => {
    if (!nextNames.has(name)) patch[name] = '';
  });
  return Object.keys(patch).length > 0 ? patch : undefined;
};

const normalizeSensitiveWords = (value: unknown): string[] => {
  const source = Array.isArray(value)
    ? value
    : typeof value === 'string'
      ? value.split(/[\n,]+/)
      : [];
  const seen = new Set<string>();
  const result: string[] = [];
  source.forEach((item) => {
    const word = String(item ?? '').trim();
    if (!word || seen.has(word)) return;
    seen.add(word);
    result.push(word);
  });
  return result;
};

const arraysEqual = (left: string[], right: string[]): boolean =>
  left.length === right.length && left.every((value, index) => value === right[index]);

const parseInteger = (value: string): number | null => {
  const trimmed = value.trim();
  if (!/^[+-]?\d+$/.test(trimmed)) return null;
  const parsed = Number.parseInt(trimmed, 10);
  return Number.isSafeInteger(parsed) ? parsed : null;
};

const readPriorityText = (value: unknown): string => {
  const text = readIntegerText(value);
  return text && parseInteger(text) === 0 ? '' : text;
};

const isValidHttpUrl = (value: string): boolean => {
  try {
    const parsed = new URL(value);
    return parsed.protocol === 'http:' || parsed.protocol === 'https:';
  } catch {
    return false;
  }
};

const normalizeProviderIdentityKey = (value: string): string => {
  const providerKey = normalizeProviderKey(value);
  return providerKey === 'gemini' ? 'gemini-cli' : providerKey;
};

const isXaiOfficialApiBaseUrl = (value: string): boolean =>
  value.trim().replace(/\/+$/, '') === XAI_OFFICIAL_API_BASE_URL;

const normalizeRecordAuthIndex = (record: Record<string, unknown>, arrayIndex?: number): string => {
  const explicit = String(
    record.authIndex ?? record.auth_index ?? record['auth-index'] ?? ''
  ).trim();
  if (explicit) return explicit;
  return arrayIndex === undefined ? '' : String(arrayIndex);
};

const recordHasFileIdentityConflict = (
  record: Record<string, unknown>,
  file: AuthFileItem
): boolean => {
  const candidate = { name: file.name, ...record } as AuthFileItem;
  const expectedProvider = normalizeProviderIdentityKey(readAuthFileStatusProvider(file));
  const candidateProvider = normalizeProviderIdentityKey(readAuthFileStatusProvider(candidate));
  if (expectedProvider && candidateProvider && candidateProvider !== expectedProvider) return true;

  const expectedAuthIndex = readAuthFileStatusAuthIndex(file);
  const candidateAuthIndex = normalizeRecordAuthIndex(record);
  if (expectedAuthIndex && candidateAuthIndex && candidateAuthIndex !== expectedAuthIndex) {
    return true;
  }

  const identityProvider = expectedProvider || candidateProvider;
  if (identityProvider === 'codex') {
    if (
      readAuthFileStatusAccountIdInvalid(file) ||
      readAuthFileStatusAccountIdInvalid(candidate) ||
      readAuthFileStatusCodexMemberInvalid(file) ||
      readAuthFileStatusCodexMemberInvalid(candidate)
    ) {
      return true;
    }

    const expectedWorkspace = readAuthFileStatusAccountId(file);
    const candidateWorkspace = readAuthFileStatusAccountId(candidate);
    if (expectedWorkspace && candidateWorkspace && expectedWorkspace !== candidateWorkspace) {
      return true;
    }

    const expectedMember = readAuthFileStatusCodexMember(file);
    const candidateMember = readAuthFileStatusCodexMember(candidate);
    return Boolean(expectedMember && candidateMember && expectedMember !== candidateMember);
  }

  const expectedAccountId = readAuthFileStatusAccountId(file);
  const candidateAccountId = readAuthFileStatusAccountId(candidate);
  if (expectedAccountId && candidateAccountId && candidateAccountId !== expectedAccountId) {
    return true;
  }

  const expectedSnapshot = readAuthFileStatusAccountSnapshot(file);
  const candidateSnapshot = readAuthFileStatusAccountSnapshot(candidate);
  return Boolean(expectedSnapshot && candidateSnapshot && candidateSnapshot !== expectedSnapshot);
};

const recordMatchesFileIdentity = (
  record: Record<string, unknown>,
  file: AuthFileItem
): boolean => {
  return !recordHasFileIdentityConflict(record, file);
};

export const getAuthFileConfigurationCapabilities = (
  provider: string
): AuthFileConfigurationCapabilities => {
  const providerKey = normalizeProviderKey(provider);
  return {
    websockets: providerKey === 'codex' || providerKey === 'xai',
    xaiRouting: providerKey === 'xai',
    claudeCloak: providerKey === 'claude',
  };
};

export const parseAuthFileConfigurationSource = (
  rawText: string,
  file: AuthFileItem
): ParsedAuthFileConfigurationSource => {
  let parsed: unknown;
  try {
    parsed = JSON.parse(rawText.trim()) as unknown;
  } catch {
    throw new Error(AUTH_FILE_CONFIGURATION_INVALID_JSON);
  }

  if (isRecordObject(parsed)) {
    if (recordHasFileIdentityConflict(parsed, file)) {
      throw new Error(AUTH_FILE_CONFIGURATION_TARGET_NOT_FOUND);
    }
    const providerKey = normalizeProviderKey(
      String(parsed.type ?? parsed.provider ?? file.type ?? file.provider ?? '')
    );
    return { record: { ...parsed }, providerKey, recordIndex: null };
  }

  if (!Array.isArray(parsed) || !parsed.every(isRecordObject)) {
    throw new Error(AUTH_FILE_CONFIGURATION_INVALID_JSON);
  }

  const authIndex = readAuthFileStatusAuthIndex(file) ?? '';
  const indexedCandidates = parsed
    .map((record, index) => ({ record, index }))
    .filter(({ record, index }) => normalizeRecordAuthIndex(record, index) === authIndex);

  const matches =
    indexedCandidates.length > 0
      ? indexedCandidates.filter(({ record }) => !recordHasFileIdentityConflict(record, file))
      : parsed
          .map((record, index) => ({ record, index }))
          .filter(({ record }) => recordMatchesFileIdentity(record, file));

  if (matches.length !== 1) {
    throw new Error(AUTH_FILE_CONFIGURATION_TARGET_NOT_FOUND);
  }

  const selected = matches[0];
  const providerKey = normalizeProviderKey(
    String(selected.record.type ?? selected.record.provider ?? file.type ?? file.provider ?? '')
  );
  return { record: { ...selected.record }, providerKey, recordIndex: selected.index };
};

export const buildAuthFileConfigurationDraft = (
  record: Record<string, unknown>,
  provider: string
): AuthFileConfigurationDraft => {
  const providerKey = normalizeProviderKey(provider);
  const headers = readHeaders(record.headers);
  const usingApi = readBoolean(record.using_api ?? record.usingApi ?? record['using-api']);
  const rawBaseUrl = readTrimmedString(record.base_url ?? record.baseUrl ?? record['base-url']);
  const excludedModels = normalizeExcludedModels(
    record.excluded_models ?? record['excluded-models'] ?? record.excludedModels
  );
  const sensitiveWords = normalizeSensitiveWords(
    record.cloak_sensitive_words ?? record.cloakSensitiveWords ?? record['cloak-sensitive-words']
  );

  return {
    prefix: readTrimmedString(record.prefix),
    proxyUrl: readTrimmedString(record.proxy_url ?? record.proxyUrl ?? record['proxy-url']),
    priority: readPriorityText(record.priority),
    weight: readIntegerText(record.weight),
    note: readTrimmedString(record.note),
    headersText: Object.keys(headers).length > 0 ? JSON.stringify(headers, null, 2) : '',
    excludedModelsText: excludedModels.join('\n'),
    disableCooling: coolingPolicyFromOverride(readCredentialCoolingOverride(record)),
    requestRetry: readIntegerText(
      record.request_retry ?? record['request-retry'] ?? record.requestRetry
    ),
    websockets: readAuthFileWebsockets(record),
    xaiRoutingMode: usingApi ? 'official-api' : 'grok-build',
    baseUrl:
      providerKey === 'xai' && usingApi
        ? rawBaseUrl || XAI_OFFICIAL_API_BASE_URL
        : providerKey === 'xai' && isXaiOfficialApiBaseUrl(rawBaseUrl)
          ? ''
          : rawBaseUrl,
    cloakMode: readTrimmedString(record.cloak_mode ?? record.cloakMode ?? record['cloak-mode']),
    cloakStrictMode: readBoolean(
      record.cloak_strict_mode ?? record.cloakStrictMode ?? record['cloak-strict-mode']
    ),
    cloakSensitiveWordsText: sensitiveWords.join('\n'),
    cloakCacheUserId: readBoolean(
      record.cloak_cache_user_id ?? record.cloakCacheUserId ?? record['cloak-cache-user-id']
    ),
    toolPrefixDisabled: readBoolean(
      record.tool_prefix_disabled ?? record['tool-prefix-disabled'] ?? record.toolPrefixDisabled
    ),
  };
};

export const authFileConfigurationDraftsEqual = (
  left: AuthFileConfigurationDraft,
  right: AuthFileConfigurationDraft
): boolean => JSON.stringify(left) === JSON.stringify(right);

type AuthFileLegacyAlias =
  | 'proxyUrl'
  | 'proxy-url'
  | 'baseUrl'
  | 'base-url'
  | 'usingApi'
  | 'using-api'
  | 'excludedModels'
  | 'excluded_models'
  | 'disableCooling'
  | 'disable-cooling'
  | 'request-retry'
  | 'requestRetry'
  | 'cloakMode'
  | 'cloak-mode'
  | 'cloakStrictMode'
  | 'cloak-strict-mode'
  | 'cloakSensitiveWords'
  | 'cloak-sensitive-words'
  | 'cloakCacheUserId'
  | 'cloak-cache-user-id'
  | 'tool-prefix-disabled'
  | 'toolPrefixDisabled';

const tombstoneLegacyAliases = (
  patch: AuthFileFieldsPatch,
  record: Record<string, unknown>,
  aliases: readonly AuthFileLegacyAlias[]
) => {
  aliases.forEach((alias) => {
    if (Object.prototype.hasOwnProperty.call(record, alias)) {
      patch[alias] = null;
    }
  });
};

export const buildAuthFileConfigurationPatch = (
  record: Record<string, unknown>,
  provider: string,
  originalDraft: AuthFileConfigurationDraft,
  draft: AuthFileConfigurationDraft
): AuthFileConfigurationPatchResult => {
  const patch: AuthFileFieldsPatch = {};
  const errors: AuthFileConfigurationErrors = {};
  const capabilities = getAuthFileConfigurationCapabilities(provider);

  const originalPrefix = originalDraft.prefix.trim();
  const nextPrefix = draft.prefix.trim();
  if (nextPrefix !== originalPrefix) patch.prefix = nextPrefix;

  const originalProxyUrl = originalDraft.proxyUrl.trim();
  const nextProxyUrl = draft.proxyUrl.trim();
  if (nextProxyUrl !== originalProxyUrl) {
    patch.proxy_url = nextProxyUrl;
    tombstoneLegacyAliases(patch, record, ['proxyUrl', 'proxy-url']);
  }

  if (draft.priority.trim() !== originalDraft.priority.trim()) {
    const trimmed = draft.priority.trim();
    if (!trimmed) {
      patch.priority = 0;
    } else {
      const value = parseInteger(trimmed);
      if (value === null) errors.priority = 'accounts.config_error_priority_integer';
      else patch.priority = value;
    }
  }

  if (draft.weight.trim() !== originalDraft.weight.trim()) {
    const trimmed = draft.weight.trim();
    if (!trimmed) {
      patch.weight = null;
    } else {
      const value = parseInteger(trimmed);
      if (value === null) {
        errors.weight = 'accounts.config_error_weight_integer';
      } else if (value > AUTH_FILE_WEIGHT_MAX) {
        errors.weight = 'accounts.config_error_weight_range';
      } else {
        patch.weight = Math.max(0, value);
      }
    }
  }

  const originalNote = originalDraft.note.trim();
  const nextNote = draft.note.trim();
  if (nextNote !== originalNote) patch.note = nextNote;

  if (draft.headersText !== originalDraft.headersText) {
    const parsed = parseHeadersText(draft.headersText);
    if (parsed.error) {
      errors.headersText = parsed.error;
    } else {
      const headersPatch = buildHeadersPatch(readHeaders(record.headers), parsed.value ?? {});
      if (headersPatch) patch.headers = headersPatch;
    }
  }

  const originalExcludedModels = normalizeExcludedModels(
    originalDraft.excludedModelsText.split(/[\n,]+/)
  );
  const nextExcludedModels = normalizeExcludedModels(draft.excludedModelsText.split(/[\n,]+/));
  if (!arraysEqual(originalExcludedModels, nextExcludedModels)) {
    patch['excluded-models'] = nextExcludedModels;
    tombstoneLegacyAliases(patch, record, ['excludedModels', 'excluded_models']);
  }

  if (draft.disableCooling !== originalDraft.disableCooling) {
    patch.disable_cooling = coolingPolicyToOverride(draft.disableCooling);
    tombstoneLegacyAliases(patch, record, ['disableCooling', 'disable-cooling']);
  }

  if (draft.requestRetry.trim() !== originalDraft.requestRetry.trim()) {
    const trimmed = draft.requestRetry.trim();
    if (!trimmed) {
      patch.request_retry = null;
      tombstoneLegacyAliases(patch, record, ['request-retry', 'requestRetry']);
    } else {
      const value = parseInteger(trimmed);
      if (value === null || value < 0) {
        errors.requestRetry = 'accounts.config_error_request_retry_integer';
      } else {
        patch.request_retry = value;
        tombstoneLegacyAliases(patch, record, ['request-retry', 'requestRetry']);
      }
    }
  }

  if (capabilities.websockets && draft.websockets !== originalDraft.websockets) {
    patch.websockets = draft.websockets;
  }

  if (capabilities.xaiRouting) {
    const usingApi = draft.xaiRoutingMode === 'official-api';
    const originalUsingApi = originalDraft.xaiRoutingMode === 'official-api';
    if (usingApi !== originalUsingApi) {
      patch.using_api = usingApi;
      tombstoneLegacyAliases(patch, record, ['usingApi', 'using-api']);
    }

    const requestedBaseUrl = draft.baseUrl.trim();
    const nextBaseUrl =
      usingApi && !requestedBaseUrl
        ? XAI_OFFICIAL_API_BASE_URL
        : !usingApi && isXaiOfficialApiBaseUrl(requestedBaseUrl)
          ? ''
          : requestedBaseUrl;
    const originalBaseUrl = originalDraft.baseUrl.trim();
    if (nextBaseUrl && !isValidHttpUrl(nextBaseUrl)) {
      errors.baseUrl = 'accounts.config_error_xai_base_url';
    } else if (nextBaseUrl !== originalBaseUrl || usingApi !== originalUsingApi) {
      patch.base_url = nextBaseUrl;
      tombstoneLegacyAliases(patch, record, ['baseUrl', 'base-url']);
    }
  }

  if (capabilities.claudeCloak) {
    const nextMode = draft.cloakMode.trim().toLowerCase();
    const originalMode = originalDraft.cloakMode.trim().toLowerCase();
    if (nextMode !== originalMode) {
      if (nextMode && !['auto', 'always', 'never'].includes(nextMode)) {
        errors.cloakMode = 'accounts.config_error_cloak_mode';
      } else {
        patch.cloak_mode = nextMode;
        tombstoneLegacyAliases(patch, record, ['cloakMode', 'cloak-mode']);
      }
    }

    if (draft.cloakStrictMode !== originalDraft.cloakStrictMode) {
      patch.cloak_strict_mode = draft.cloakStrictMode ? 'true' : '';
      tombstoneLegacyAliases(patch, record, ['cloakStrictMode', 'cloak-strict-mode']);
    }

    const originalWords = normalizeSensitiveWords(originalDraft.cloakSensitiveWordsText);
    const nextWords = normalizeSensitiveWords(draft.cloakSensitiveWordsText);
    if (!arraysEqual(originalWords, nextWords)) {
      patch.cloak_sensitive_words = nextWords.join(',');
      tombstoneLegacyAliases(patch, record, ['cloakSensitiveWords', 'cloak-sensitive-words']);
    }

    if (draft.cloakCacheUserId !== originalDraft.cloakCacheUserId) {
      patch.cloak_cache_user_id = draft.cloakCacheUserId ? 'true' : '';
      tombstoneLegacyAliases(patch, record, ['cloakCacheUserId', 'cloak-cache-user-id']);
    }

    if (draft.toolPrefixDisabled !== originalDraft.toolPrefixDisabled) {
      patch.tool_prefix_disabled = draft.toolPrefixDisabled;
      tombstoneLegacyAliases(patch, record, ['tool-prefix-disabled', 'toolPrefixDisabled']);
    }
  }

  return { patch, errors };
};

const isSensitiveKey = (key: string): boolean => {
  const normalized = key.toLowerCase().replace(/[^a-z0-9]/g, '');
  return (
    normalized === 'key' ||
    normalized.endsWith('key') ||
    normalized === 'keys' ||
    normalized.endsWith('keys') ||
    normalized === 'token' ||
    normalized.endsWith('token') ||
    normalized === 'tokens' ||
    normalized.endsWith('tokens') ||
    SENSITIVE_KEY_PARTS.some((part) => normalized.includes(part))
  );
};

const redactConfigurationUrl = (value: string): string | null => {
  if (!value.trim()) return null;
  try {
    const parsed = new URL(value);
    parsed.username = '';
    parsed.password = '';
    parsed.search = '';
    parsed.hash = '';
    return parsed.toString();
  } catch {
    return null;
  }
};

const isUrlFieldKey = (key: string): boolean => {
  const trimmed = key.trim();
  return /^url$/i.test(trimmed) || /(?:^|[-_])url$/i.test(trimmed) || /(?:Url|URL)$/.test(trimmed);
};

export const redactAuthFileConfigurationValue = (value: unknown, key = ''): unknown => {
  if (key && isSensitiveKey(key)) return REDACTED_VALUE;
  if (typeof value === 'string') {
    if (!value.trim()) return value;
    const redactedUrl = redactConfigurationUrl(value);
    if (redactedUrl !== null) return redactedUrl;
    if (isUrlFieldKey(key)) return REDACTED_VALUE;
  }
  if (Array.isArray(value)) {
    return value.map((item) => redactAuthFileConfigurationValue(item));
  }
  if (!isRecordObject(value)) return value;

  if (key.toLowerCase() === 'headers') {
    return Object.fromEntries(Object.keys(value).map((headerName) => [headerName, REDACTED_VALUE]));
  }
  return Object.fromEntries(
    Object.entries(value).map(([entryKey, entryValue]) => [
      entryKey,
      redactAuthFileConfigurationValue(entryValue, entryKey),
    ])
  );
};

export const buildRedactedAuthFileConfigurationText = (record: Record<string, unknown>): string =>
  JSON.stringify(redactAuthFileConfigurationValue(record), null, 2);
