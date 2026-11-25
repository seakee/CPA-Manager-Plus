/**
 * AI 提供商相关 API
 */

import { apiClient, type ScopedApiRequestConfig } from './client';
import {
  getPersistedOpenAIKeyEntryIndex,
  hasOpenAIKeyEntryConfiguration,
  isPersistedOpenAIKeyEntry,
  preservePersistedOpenAIKeyEntry,
} from '@/utils/openAIKeyEntries';
import { configApi } from './config';
import {
  normalizeClaudeFingerprintProfile,
  normalizeGeminiKeyConfig,
  normalizeOpenAIProvider,
  normalizeProviderKeyConfig,
  normalizeV8ProviderGroups,
} from './transformers';
import type {
  GeminiKeyConfig,
  OpenAIProviderConfig,
  ProviderKeyConfig,
  ProviderV8Family,
  ProviderV8GroupSource,
  ApiKeyEntry,
  ModelAlias,
} from '@/types';
import {
  hasModelThinkingLevelsClearMarker,
  hasModelThinkingLevelsEditMarker,
  markModelThinkingLevelsForClear,
  markModelThinkingLevelsForEdit,
  removeThinkingFlagAliases,
  stripModelThinkingLevelsMarkers,
  THINKING_DYNAMIC_ALLOWED_FIELDS,
  THINKING_ZERO_ALLOWED_FIELDS,
} from '@/types';

const serializeHeaders = (headers?: Record<string, string>) =>
  headers && Object.keys(headers).length ? headers : undefined;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const hasOwnField = (value: unknown, field: string): value is Record<string, unknown> =>
  isRecord(value) && Object.prototype.hasOwnProperty.call(value, field);

const AUTH_INDEX_FIELDS = ['auth-index', 'authIndex', 'auth_index'] as const;
const DISABLE_COOLING_FIELDS = ['disable-cooling', 'disableCooling', 'disable_cooling'] as const;
const FINGERPRINT_PROFILE_FIELDS = [
  'fingerprint-profile',
  'fingerprintProfile',
  'fingerprint_profile',
] as const;
const LEGACY_CCH_FIELDS = [
  'experimental-cch-signing',
  'experimentalCchSigning',
  'experimental_cch_signing',
] as const;

const COMMON_PROVIDER_KEY_FIELDS = [
  'api-key',
  'apiKey',
  ...AUTH_INDEX_FIELDS,
  'priority',
  'weight',
  'prefix',
  'base-url',
  'baseUrl',
  'base_url',
  'proxy-url',
  'proxyUrl',
  'proxy_url',
  'headers',
  'models',
  'excluded-models',
  'excludedModels',
  'excluded_models',
] as const;

const COOLING_PROVIDER_KEY_FIELDS = [
  ...COMMON_PROVIDER_KEY_FIELDS,
  ...DISABLE_COOLING_FIELDS,
] as const;
const CODEX_KEY_FIELDS = [...COOLING_PROVIDER_KEY_FIELDS, 'websockets'] as const;
const XAI_KEY_FIELDS = CODEX_KEY_FIELDS;
const CLAUDE_KEY_FIELDS = [
  ...COOLING_PROVIDER_KEY_FIELDS,
  'cloak',
  ...FINGERPRINT_PROFILE_FIELDS,
  ...LEGACY_CCH_FIELDS,
  'rebuild-mid-system-message',
  'rebuildMidSystemMessage',
  'rebuild_mid_system_message',
] as const;
const GEMINI_KEY_FIELDS = COOLING_PROVIDER_KEY_FIELDS;
const VERTEX_KEY_FIELDS = COOLING_PROVIDER_KEY_FIELDS;

const OPENAI_PROVIDER_FIELDS = [
  'name',
  'priority',
  'disabled',
  'prefix',
  'base-url',
  'baseUrl',
  'base_url',
  'api-key-entries',
  'apiKeyEntries',
  'api_key_entries',
  'api-keys',
  'apiKeys',
  'api_keys',
  ...AUTH_INDEX_FIELDS,
  'headers',
  'models',
  'test-model',
  'testModel',
  'test_model',
  'disable-cooling',
  'disableCooling',
  'disable_cooling',
] as const;

const MODEL_ALIAS_FIELDS = [
  'name',
  'id',
  'model',
  'alias',
  'display-name',
  'display_name',
  'displayName',
  'priority',
  'test-model',
  'testModel',
  'test_model',
  'image',
  'force-mapping',
  'forceMapping',
  'force_mapping',
  'max-context-length',
  'maxContextLength',
  'max_context_length',
  'is-compat',
  'isCompat',
  'is_compat',
  'openapi',
  'support-configuration-update',
  'supportConfigurationUpdate',
  'support_configuration_update',
  'use-max-completion-tokens',
  'useMaxCompletionTokens',
  'use_max_completion_tokens',
  'input-modalities',
  'inputModalities',
  'input_modalities',
  'output-modalities',
  'outputModalities',
  'output_modalities',
] as const;

const API_KEY_ENTRY_FIELDS = [
  'api-key',
  'apiKey',
  'key',
  ...AUTH_INDEX_FIELDS,
  'weight',
  'proxy-url',
  'proxyUrl',
  'proxy_url',
  'headers',
] as const;

const CLOAK_FIELDS = [
  'mode',
  'strict-mode',
  'strictMode',
  'strict_mode',
  'sensitive-words',
  'sensitiveWords',
  'sensitive_words',
  'cache-user-id',
  'cacheUserId',
  'cache_user_id',
  'sensitive-words-replace',
  'sensitiveWordsReplace',
  'sensitive_words_replace',
  'sensitive-patterns-replace',
  'sensitivePatternsReplace',
  'sensitive_patterns_replace',
] as const;

const RAW_SECTION_ALIASES: Record<string, readonly string[]> = {
  'gemini-api-key': ['gemini-api-key', 'geminiApiKey', 'geminiApiKeys'],
  'interactions-api-key': ['interactions-api-key', 'interactionsApiKey', 'interactionsApiKeys'],
  'codex-api-key': ['codex-api-key', 'codexApiKey', 'codexApiKeys'],
  'xai-api-key': ['xai-api-key', 'xaiApiKey', 'xaiApiKeys'],
  'claude-api-key': ['claude-api-key', 'claudeApiKey', 'claudeApiKeys'],
  'vertex-api-key': ['vertex-api-key', 'vertexApiKey', 'vertexApiKeys'],
  'meta-api-key': ['meta-api-key', 'metaApiKey', 'metaApiKeys'],
  'openai-compatibility': ['openai-compatibility', 'openaiCompatibility', 'openAICompatibility'],
};

const normalizeEtagValue = (etag: unknown): string | undefined => {
  if (typeof etag !== 'string') return undefined;
  const trimmed = etag.trim();
  if (!trimmed) return undefined;
  if (/^w\//i.test(trimmed)) {
    const stripped = trimmed.slice(2).trim();
    return stripped || undefined;
  }
  return trimmed;
};

const readConnectionEpoch = (): number =>
  typeof apiClient.getConnectionEpoch === 'function' ? apiClient.getConnectionEpoch() : 0;

type CapturedWriteScope = {
  epoch: number;
  v0Config?: ScopedApiRequestConfig;
  v8Config?: ScopedApiRequestConfig;
};

const captureWriteScope = (): CapturedWriteScope => {
  const epoch = readConnectionEpoch();
  const rawV0 =
    typeof apiClient.createCurrentScopedConfig === 'function'
      ? apiClient.createCurrentScopedConfig()
      : undefined;
  const rawV8 =
    typeof apiClient.createCurrentScopedV8Config === 'function'
      ? apiClient.createCurrentScopedV8Config()
      : undefined;
  const v0Config =
    rawV0 && typeof rawV0.baseURL === 'string' && rawV0.baseURL.trim() ? rawV0 : undefined;
  const v8Config =
    rawV8 && typeof rawV8.baseURL === 'string' && rawV8.baseURL.trim() ? rawV8 : undefined;
  return { epoch, v0Config, v8Config };
};

const assertConnectionEpochUnchanged = (requestEpoch: number): void => {
  if (readConnectionEpoch() !== requestEpoch) {
    throw Object.assign(new Error('Connection changed; write aborted.'), {
      code: 'CONNECTION_CHANGED',
    });
  }
};

const getScopedRawV8 = (url: string, scope: CapturedWriteScope) => {
  assertConnectionEpochUnchanged(scope.epoch);
  return scope.v8Config ? apiClient.getRawV8(url, scope.v8Config) : apiClient.getRawV8(url);
};

const getScopedV0 = <T = unknown>(url: string, scope?: CapturedWriteScope) => {
  if (scope) {
    assertConnectionEpochUnchanged(scope.epoch);
    if (scope.v0Config) {
      return apiClient.get<T>(url, scope.v0Config);
    }
  }
  return apiClient.get<T>(url);
};

const putScopedV0 = <T = unknown>(url: string, data: unknown, scope: CapturedWriteScope) => {
  assertConnectionEpochUnchanged(scope.epoch);
  return scope.v0Config ? apiClient.put<T>(url, data, scope.v0Config) : apiClient.put<T>(url, data);
};

const deleteScopedV0 = <T = unknown>(url: string, scope: CapturedWriteScope) => {
  assertConnectionEpochUnchanged(scope.epoch);
  return scope.v0Config ? apiClient.delete<T>(url, scope.v0Config) : apiClient.delete<T>(url);
};

let latestV8ConfigEtag: string | undefined;
let latestV8ConfigEtagEpoch: number | undefined;
let v8EtagMutationVersion = 0;

const recordLatestV8ConfigEtag = (
  etag: unknown,
  requestEpoch: number = readConnectionEpoch()
): string | undefined => {
  const normalized = normalizeEtagValue(etag);
  if (requestEpoch === readConnectionEpoch()) {
    if (normalized) {
      latestV8ConfigEtag = normalized;
      latestV8ConfigEtagEpoch = requestEpoch;
    }
    v8EtagMutationVersion += 1;
  }
  return normalized;
};

const recordPassiveReadLatestV8ConfigEtag = (
  etag: unknown,
  requestEpoch: number = readConnectionEpoch(),
  writeVersionBefore: number = v8EtagMutationVersion
): string | undefined => {
  const normalized = normalizeEtagValue(etag);
  if (
    normalized &&
    requestEpoch === readConnectionEpoch() &&
    writeVersionBefore === v8EtagMutationVersion
  ) {
    latestV8ConfigEtag = normalized;
    latestV8ConfigEtagEpoch = requestEpoch;
  }
  return normalized;
};

const setLatestV8ConfigEtagForEpoch = (
  etag: unknown,
  requestEpoch: number = readConnectionEpoch(),
  options?: { passiveRead?: boolean; writeVersionBefore?: number }
): string | undefined => {
  const normalized = normalizeEtagValue(etag);
  if (requestEpoch === readConnectionEpoch()) {
    if (
      options?.passiveRead &&
      options.writeVersionBefore !== undefined &&
      options.writeVersionBefore !== v8EtagMutationVersion
    ) {
      return normalized;
    }
    latestV8ConfigEtag = normalized;
    latestV8ConfigEtagEpoch = normalized ? requestEpoch : undefined;
    if (!options?.passiveRead) {
      v8EtagMutationVersion += 1;
    }
  }
  return normalized;
};

const clearLatestV8ConfigEtagForEpoch = (
  requestEpoch: number = readConnectionEpoch(),
  options?: { passiveRead?: boolean; writeVersionBefore?: number }
): void => {
  if (requestEpoch === readConnectionEpoch()) {
    if (
      options?.passiveRead &&
      options.writeVersionBefore !== undefined &&
      options.writeVersionBefore !== v8EtagMutationVersion
    ) {
      return;
    }
    latestV8ConfigEtag = undefined;
    latestV8ConfigEtagEpoch = undefined;
    if (!options?.passiveRead) {
      v8EtagMutationVersion += 1;
    }
  }
};

const getStringField = (record: Record<string, unknown>, keys: readonly string[]) => {
  for (const key of keys) {
    const value = record[key];
    if (value === undefined || value === null) continue;
    const text = String(value).trim();
    if (text) return text;
  }
  return '';
};

const providerKeyIdentity = (record: Record<string, unknown>) => {
  const authIndex = getStringField(record, AUTH_INDEX_FIELDS);
  if (authIndex) return `auth-index\u0000${authIndex}`;
  const apiKey = getStringField(record, ['api-key', 'apiKey']);
  if (!apiKey) return '';
  const baseUrl = getStringField(record, ['base-url', 'baseUrl', 'base_url']);
  return `${apiKey}\u0000${baseUrl}`;
};

const openAIProviderIdentity = (record: Record<string, unknown>) =>
  getStringField(record, ['name', 'id']);

const modelIdentity = (record: Record<string, unknown>) =>
  getStringField(record, ['name', 'id', 'model']);

const cloneWithoutKnownFields = (
  raw: unknown,
  knownFields: readonly string[]
): Record<string, unknown> => {
  const next: Record<string, unknown> = isRecord(raw) ? { ...raw } : {};
  knownFields.forEach((field) => {
    delete next[field];
  });
  return next;
};

const mergeKnownFields = (
  raw: unknown,
  payload: Record<string, unknown>,
  knownFields: readonly string[]
) => {
  const next = cloneWithoutKnownFields(raw, knownFields);
  Object.entries(payload).forEach(([key, value]) => {
    if (value !== undefined) {
      next[key] = value;
    }
  });
  return next;
};

const hasDefinedField = (record: Record<string, unknown>, fields: readonly string[]) =>
  fields.some((field) => record[field] !== undefined);

const preserveOmittedRawField = (
  raw: unknown,
  payload: Record<string, unknown>,
  next: Record<string, unknown>,
  fields: readonly string[]
) => {
  if (!isRecord(raw) || hasDefinedField(payload, fields)) return;
  const rawField = fields.find((field) => raw[field] !== undefined);
  if (rawField) {
    next[rawField] = raw[rawField];
  }
};

const findRawRecord = (
  rawRecords: Array<Record<string, unknown> | undefined>,
  usedIndexes: Set<number>,
  payload: Record<string, unknown>,
  index: number,
  getIdentity: (record: Record<string, unknown>) => string
) => {
  const identity = getIdentity(payload);
  if (identity) {
    for (let i = 0; i < rawRecords.length; i += 1) {
      const candidate = rawRecords[i];
      if (!candidate || usedIndexes.has(i)) continue;
      if (getIdentity(candidate) === identity) {
        usedIndexes.add(i);
        return candidate;
      }
    }
  }

  const fallback = rawRecords[index];
  if (fallback && !usedIndexes.has(index)) {
    usedIndexes.add(index);
    return fallback;
  }

  return undefined;
};

const mergeOpenAIKeyEntries = (rawItems: unknown, payloadItems: Record<string, unknown>[]) => {
  const rawRecords = Array.isArray(rawItems)
    ? rawItems.map((item) => (isRecord(item) ? item : undefined))
    : [];
  const usedIndexes = new Set<number>();

  return payloadItems.map((payload) => {
    const persistedIndex = getPersistedOpenAIKeyEntryIndex(payload);
    let raw: Record<string, unknown> | undefined;
    if (persistedIndex !== undefined && !usedIndexes.has(persistedIndex)) {
      raw = rawRecords[persistedIndex];
      if (raw) {
        usedIndexes.add(persistedIndex);
      }
    }
    return mergeKnownFields(raw, payload, API_KEY_ENTRY_FIELDS);
  });
};

const getRawSectionList = (rawConfig: unknown, section: string) => {
  if (!isRecord(rawConfig)) return [];
  const aliases = RAW_SECTION_ALIASES[section] ?? [section];
  for (const alias of aliases) {
    const value = rawConfig[alias];
    if (Array.isArray(value)) return value;
  }
  return [];
};

const mergeModelPayloads = (raw: unknown, models: unknown) =>
  Array.isArray(models)
    ? (() => {
        const payloadItems = models.filter(isRecord);
        const rawItems = isRecord(raw) ? raw.models : undefined;
        const rawRecords = Array.isArray(rawItems)
          ? rawItems.map((item) => (isRecord(item) ? item : undefined))
          : [];
        const usedIndexes = new Set<number>();

        return payloadItems.map((payload, index) => {
          const rawModel = findRawRecord(rawRecords, usedIndexes, payload, index, modelIdentity);
          const next = mergeKnownFields(rawModel, payload, MODEL_ALIAS_FIELDS);
          if (
            payload.alias === undefined &&
            isRecord(rawModel) &&
            typeof rawModel.alias === 'string' &&
            rawModel.alias.trim() &&
            rawModel.alias.trim() === String(payload.name ?? '').trim()
          ) {
            next.alias = rawModel.alias.trim();
          }
          const payloadThinking = payload.thinking;
          const clearThinkingLevels =
            hasModelThinkingLevelsClearMarker(payload) || payloadThinking === null;
          const editThinkingLevels = hasModelThinkingLevelsEditMarker(payload);
          preserveOmittedRawField(rawModel, payload, next, [
            'display-name',
            'displayName',
            'display_name',
          ]);
          preserveOmittedRawField(rawModel, payload, next, ['image']);
          preserveOmittedRawField(rawModel, payload, next, [
            'force-mapping',
            'forceMapping',
            'force_mapping',
          ]);
          preserveOmittedRawField(rawModel, payload, next, [
            'max-context-length',
            'maxContextLength',
            'max_context_length',
          ]);
          preserveOmittedRawField(rawModel, payload, next, ['is-compat', 'isCompat', 'is_compat']);
          preserveOmittedRawField(rawModel, payload, next, ['openapi']);
          preserveOmittedRawField(rawModel, payload, next, [
            'support-configuration-update',
            'supportConfigurationUpdate',
            'support_configuration_update',
          ]);
          preserveOmittedRawField(rawModel, payload, next, [
            'use-max-completion-tokens',
            'useMaxCompletionTokens',
            'use_max_completion_tokens',
          ]);
          preserveOmittedRawField(rawModel, payload, next, [
            'input-modalities',
            'inputModalities',
            'input_modalities',
          ]);
          preserveOmittedRawField(rawModel, payload, next, [
            'output-modalities',
            'outputModalities',
            'output_modalities',
          ]);
          if (clearThinkingLevels) {
            const rawThinking = isRecord(rawModel?.thinking) ? rawModel.thinking : undefined;
            const nextThinking = {
              ...(rawThinking ?? {}),
              ...(isRecord(payloadThinking) ? payloadThinking : {}),
            };
            delete nextThinking.levels;
            const preserveExplicitEmptyThinking =
              (isRecord(rawThinking) && Object.keys(rawThinking).length === 0) ||
              (isRecord(payloadThinking) && Object.keys(payloadThinking).length === 0);
            if (Object.keys(nextThinking).length > 0 || preserveExplicitEmptyThinking) {
              next.thinking = nextThinking;
            } else {
              delete next.thinking;
            }
          } else if (editThinkingLevels && isRecord(payloadThinking)) {
            const rawThinking = isRecord(rawModel?.thinking) ? rawModel.thinking : undefined;
            let nextThinking = {
              ...(rawThinking ?? {}),
              ...payloadThinking,
            };
            nextThinking = removeThinkingFlagAliases(nextThinking, [
              ...THINKING_ZERO_ALLOWED_FIELDS,
              ...THINKING_DYNAMIC_ALLOWED_FIELDS,
            ]);
            next.thinking = nextThinking;
          } else if (isRecord(payloadThinking)) {
            const rawThinking = isRecord(rawModel?.thinking) ? rawModel.thinking : undefined;
            next.thinking = {
              ...(rawThinking ?? {}),
              ...payloadThinking,
            };
          } else if (hasOwnField(rawModel, 'thinking') && isRecord(rawModel.thinking)) {
            // Preserve an explicitly empty or future-only Thinking container when the
            // normalized payload does not mention it.
            next.thinking = { ...rawModel.thinking };
          }
          if (next.thinking === null) {
            delete next.thinking;
          }
          return next;
        });
      })()
    : undefined;

const mergeProviderKeyPayload = (
  raw: unknown,
  payload: Record<string, unknown>,
  knownFields: readonly string[]
) => {
  // Fingerprint and legacy CCH round-trip independently:
  // - untouched (no fingerprint variant in the payload): both raw groups are
  //   preserved verbatim, including unknown future fingerprint-profile values.
  // - explicitly enabled (payload carries a profile): the raw fingerprint is
  //   replaced and the legacy CCH field is kept, so moving a config to the
  //   new profile never silently drops its previous CCH setting (older CPA
  //   builds ignore fingerprint-profile and would otherwise lose both).
  // - explicitly cleared (payload carries ''): the raw fingerprint and the
  //   legacy CCH field are both dropped.
  const fingerprintDefined = hasDefinedField(payload, FINGERPRINT_PROFILE_FIELDS);
  const fingerprintCleared = FINGERPRINT_PROFILE_FIELDS.some((field) => payload[field] === '');
  const next = mergeKnownFields(raw, payload, knownFields);
  if (fingerprintDefined) {
    FINGERPRINT_PROFILE_FIELDS.forEach((field) => {
      if (next[field] === '') {
        delete next[field];
      }
    });
  }
  preserveOmittedRawField(raw, payload, next, DISABLE_COOLING_FIELDS);
  if (!fingerprintDefined) {
    preserveOmittedRawField(raw, payload, next, FINGERPRINT_PROFILE_FIELDS);
  }
  if (!fingerprintCleared) {
    preserveOmittedRawField(raw, payload, next, LEGACY_CCH_FIELDS);
  }
  const models = mergeModelPayloads(raw, payload.models);
  if (models) next.models = models;
  if (payload.cloak === null) {
    delete next.cloak;
  } else if (isRecord(payload.cloak)) {
    const rawCloak = isRecord(raw) && isRecord(raw.cloak) ? raw.cloak : undefined;
    const cloakPayload = payload.cloak;
    const cloak = mergeKnownFields(rawCloak, cloakPayload, CLOAK_FIELDS);
    const preserveRawCloakCanonicalField = (
      aliases: readonly string[],
      canonicalField: string
    ) => {
      if (!rawCloak || hasDefinedField(cloakPayload, aliases)) {
        return;
      }
      for (const alias of aliases) {
        if (rawCloak[alias] !== undefined) {
          cloak[canonicalField] = rawCloak[alias];
          break;
        }
      }
    };
    preserveRawCloakCanonicalField(
      ['cache-user-id', 'cacheUserId', 'cache_user_id'],
      'cache-user-id'
    );
    preserveRawCloakCanonicalField(
      ['sensitive-words-replace', 'sensitiveWordsReplace', 'sensitive_words_replace'],
      'sensitive-words-replace'
    );
    preserveRawCloakCanonicalField(
      ['sensitive-patterns-replace', 'sensitivePatternsReplace', 'sensitive_patterns_replace'],
      'sensitive-patterns-replace'
    );
    next.cloak = cloak;
  }
  return next;
};

const mergeOpenAIProviderPayload = (raw: unknown, payload: Record<string, unknown>) => {
  const next = mergeKnownFields(raw, payload, OPENAI_PROVIDER_FIELDS);
  preserveOmittedRawField(raw, payload, next, DISABLE_COOLING_FIELDS);
  const rawApiKeyEntries = isRecord(raw)
    ? (raw['api-key-entries'] ?? raw.apiKeyEntries)
    : undefined;
  const apiKeyEntries = payload['api-key-entries'];
  if (Array.isArray(apiKeyEntries)) {
    next['api-key-entries'] = mergeOpenAIKeyEntries(
      rawApiKeyEntries,
      apiKeyEntries.filter(isRecord)
    );
  }
  const models = mergeModelPayloads(raw, payload.models);
  if (models) next.models = models;
  return next;
};

const buildPreservedList = async <T>(
  section: string,
  configs: T[],
  serialize: (item: T) => Record<string, unknown>,
  mergePayload: (raw: unknown, payload: Record<string, unknown>) => Record<string, unknown>,
  getIdentity: (record: Record<string, unknown>) => string,
  scope?: CapturedWriteScope
) => {
  const payloads = configs.map((item) => serialize(item));

  let rawConfig: unknown;
  try {
    rawConfig = await getScopedV0('/config', scope);
  } catch (error) {
    if (isRecord(error) && error.code === 'CONNECTION_CHANGED') {
      throw error;
    }
    return payloads.map((payload) => {
      if (!Array.isArray(payload.models)) return payload;
      return {
        ...payload,
        models: payload.models.map((model) => {
          if (!isRecord(model)) return model;
          const next = stripModelThinkingLevelsMarkers(model);
          if (hasModelThinkingLevelsClearMarker(model)) {
            const nextThinking = isRecord(model.thinking) ? { ...model.thinking } : {};
            delete nextThinking.levels;
            const preserveExplicitEmptyThinking =
              isRecord(model.thinking) && Object.keys(model.thinking).length === 0;
            if (Object.keys(nextThinking).length > 0 || preserveExplicitEmptyThinking) {
              next.thinking = nextThinking;
            } else {
              delete next.thinking;
            }
          } else if (hasModelThinkingLevelsEditMarker(model) && isRecord(next.thinking)) {
            next.thinking = removeThinkingFlagAliases(next.thinking, [
              ...THINKING_ZERO_ALLOWED_FIELDS,
              ...THINKING_DYNAMIC_ALLOWED_FIELDS,
            ]);
          } else if (next.thinking === null) {
            delete next.thinking;
          }
          return next;
        }),
      };
    });
  }

  const rawItems = getRawSectionList(rawConfig, section);
  const rawRecords = Array.isArray(rawItems)
    ? rawItems.map((item) => (isRecord(item) ? item : undefined))
    : [];
  const usedIndexes = new Set<number>();

  return payloads.map((payload, index) => {
    const raw = findRawRecord(rawRecords, usedIndexes, payload, index, getIdentity);
    return mergePayload(raw, payload);
  });
};

type ProviderRecordMerger = (
  raw: unknown,
  payload: Record<string, unknown>
) => Record<string, unknown>;

export const appendLatestProviderRecord = (
  latestItems: unknown[],
  payload: Record<string, unknown>,
  mergePayload: ProviderRecordMerger
): unknown[] => [...latestItems, mergePayload(undefined, payload)];

export const replaceLatestProviderRecord = (
  latestItems: unknown[],
  isTarget: (record: Record<string, unknown>, index: number) => boolean,
  payload: Record<string, unknown>,
  mergePayload: ProviderRecordMerger
): unknown[] => {
  const targetIndex = latestItems.findIndex(
    (item, index) => isRecord(item) && isTarget(item, index)
  );
  if (targetIndex < 0) {
    throw new Error('Provider configuration changed; refresh and try again.');
  }
  return latestItems.map((item, index) =>
    index === targetIndex ? mergePayload(item, payload) : item
  );
};

/** Serializes read-modify-write updates per CPA config section to avoid lost updates. */
const providerSectionWriteQueues = new Map<string, Promise<unknown>>();

const enqueueProviderSectionWrite = <T>(section: string, task: () => Promise<T>): Promise<T> => {
  const previous = providerSectionWriteQueues.get(section) ?? Promise.resolve();
  const run = previous.then(task, task);
  providerSectionWriteQueues.set(
    section,
    run.then(
      () => undefined,
      () => undefined
    )
  );
  return run;
};

const runProviderListMutationV0 = async (
  section: string,
  mutate: (latestItems: unknown[]) => unknown[],
  scope: CapturedWriteScope
) => {
  const rawConfig = await getScopedV0('/config', scope);
  const latestItems = getRawSectionList(rawConfig, section);
  await putScopedV0(`/${section}`, mutate(latestItems), scope);
};
const matchesProviderConfig = (
  record: Record<string, unknown>,
  original: GeminiKeyConfig | ProviderKeyConfig
) =>
  providerKeyIdentity(record) ===
  providerKeyIdentity({
    'auth-index': original.authIndex,
    'api-key': original.apiKey,
    'base-url': original.baseUrl,
  });

export type ClaudeFingerprintVerification = 'confirmed' | 'not-applied' | 'not-found';

export type ClaudeFingerprintVerificationTarget =
  | { mode: 'edit'; index: number; apiKey?: string; baseUrl?: string }
  | { mode: 'create'; apiKey?: string; baseUrl?: string };

// One GET /config after a committed save: the normalized section refreshes the
// global config store, while the raw section is the verification source of
// truth. The raw section is required because an unknown future fingerprint
// value normalizes to undefined and would otherwise make an explicit Default
// look confirmed even though the raw field is still present.
export const readBackClaudeConfigAfterSave = async (): Promise<{
  claudeApiKeys: ProviderKeyConfig[];
  rawRecords: Array<Record<string, unknown>>;
}> => {
  const config = await configApi.getConfig();
  const claudeApiKeys = config.claudeApiKeys ?? [];
  const rawSectionRecords = getRawSectionList(config.raw, 'claude-api-key');
  if (rawSectionRecords.length > 0 || claudeApiKeys.length === 0) {
    return {
      claudeApiKeys,
      rawRecords: rawSectionRecords,
    };
  }
  const v8FlattenedRecords = claudeApiKeys.flatMap((entry) => {
    const source = entry.v8Source;
    if (!source || !isRecord(source.group) || !Array.isArray(source.group.keys)) return [];
    const rawKey = source.group.keys[source.keyIndex ?? 0];
    if (!isRecord(rawKey)) return [];
    const combined: Record<string, unknown> = { ...source.group };
    delete combined.keys;
    Object.entries(rawKey).forEach(([field, val]) => {
      if (val !== undefined && val !== null) {
        combined[field] = val;
      }
    });
    return [combined];
  });
  return {
    claudeApiKeys,
    rawRecords: v8FlattenedRecords,
  };
};

const matchesPersistedIdentity = (
  record: Record<string, unknown>,
  apiKey?: string,
  baseUrl?: string
) => {
  const targetApiKey = (apiKey ?? '').trim();
  const targetBaseUrl = (baseUrl ?? '').trim();
  if (!targetApiKey && !targetBaseUrl) return true;
  const recordApiKey = (getStringField(record, ['api-key', 'apiKey']) ?? '').trim();
  const recordBaseUrl = (getStringField(record, ['base-url', 'baseUrl', 'base_url']) ?? '').trim();
  return recordApiKey === targetApiKey && recordBaseUrl === targetBaseUrl;
};

// Read-back check for explicit fingerprint changes against the persisted raw
// /config list. Older CPA builds unmarshal config sections into structs
// without fingerprint-profile, silently ignore the field, and still answer
// 200 — so a successful PUT alone proves nothing. The runtime auth-index from
// /claude-api-key deliberately plays no part because /config entries never
// carry it: Edit verifies positionally (the PUT replaced the record in place)
// with an apiKey + baseUrl fallback; Create verifies the appended last record,
// because duplicate api-key/base-url entries are legal (they differ in
// proxy/prefix/headers) and an identity find() could verify an older record.
export const verifyClaudeFingerprintInRawConfig = (
  rawRecords: Array<Record<string, unknown>>,
  expected: ProviderKeyConfig['fingerprintProfile'],
  target: ClaudeFingerprintVerificationTarget
): ClaudeFingerprintVerification => {
  let saved: Record<string, unknown> | undefined;
  if (target.mode === 'create') {
    const last = rawRecords[rawRecords.length - 1];
    // Sanity check only: on mismatch we cannot tell which record was written,
    // and falling back to find() could verify an older duplicate record.
    if (isRecord(last) && matchesPersistedIdentity(last, target.apiKey, target.baseUrl)) {
      saved = last;
    }
  } else {
    const byIndex = rawRecords[target.index];
    if (isRecord(byIndex) && matchesPersistedIdentity(byIndex, target.apiKey, target.baseUrl)) {
      saved = byIndex;
    } else if ((target.apiKey ?? '').trim() || (target.baseUrl ?? '').trim()) {
      saved = rawRecords.find((record) =>
        matchesPersistedIdentity(record, target.apiKey, target.baseUrl)
      );
    }
  }
  if (!isRecord(saved)) return 'not-found';
  const rawValue =
    saved['fingerprint-profile'] ?? saved.fingerprintProfile ?? saved.fingerprint_profile;
  const actual = normalizeClaudeFingerprintProfile(rawValue);
  if (expected === 'claude-code-cli') {
    return actual === 'claude-code-cli' ? 'confirmed' : 'not-applied';
  }
  // Explicit Default is only confirmed when the raw field is really gone;
  // an unknown future value reads as undefined through the recognized-alias
  // normalization but must count as not-applied here.
  const rawFieldAbsent =
    rawValue === undefined || (typeof rawValue === 'string' && rawValue.trim() === '');
  return rawFieldAbsent ? 'confirmed' : 'not-applied';
};

const extractArrayPayload = (data: unknown, key: string): unknown[] => {
  if (Array.isArray(data)) return data;
  if (!isRecord(data)) return [];
  const candidate = data[key] ?? data.items ?? data.data ?? data;
  return Array.isArray(candidate) ? candidate : [];
};

const getOpenAIProviderKey = (provider: Pick<OpenAIProviderConfig, 'name' | 'baseUrl'>) =>
  `${String(provider.name ?? '')
    .trim()
    .toLowerCase()}\u0000${String(provider.baseUrl ?? '')
    .trim()
    .toLowerCase()}`;

// CPA upstream currently omits some OpenAI provider fields from GET /openai-compatibility,
// so we rehydrate them from /config when they are missing.
// This is a temporary workaround until upstream fixes the issue.
const hydrateOpenAIProvidersDisableCooling = async (providers: OpenAIProviderConfig[]) => {
  if (!providers.some((provider) => provider.disableCooling === undefined)) {
    return providers;
  }

  let rawConfig: unknown;
  try {
    rawConfig = await apiClient.get('/config');
  } catch {
    return providers;
  }

  const fallbackProviders = extractArrayPayload(rawConfig, 'openai-compatibility')
    .map((item) => normalizeOpenAIProvider(item))
    .filter(Boolean) as OpenAIProviderConfig[];

  if (!fallbackProviders.length) {
    return providers;
  }

  const fallbackByKey = new Map(
    fallbackProviders.map((provider) => [getOpenAIProviderKey(provider), provider])
  );

  return providers.map((provider) => {
    if (provider.disableCooling !== undefined) {
      return provider;
    }
    const fallback = fallbackByKey.get(getOpenAIProviderKey(provider));
    if (!fallback || fallback.disableCooling === undefined) {
      return provider;
    }
    return { ...provider, disableCooling: fallback.disableCooling };
  });
};

const buildProviderDeleteQuery = (apiKey: string, baseUrl?: string) => {
  const params = new URLSearchParams();
  params.set('api-key', apiKey.trim());
  params.set('base-url', (baseUrl ?? '').trim());
  return `?${params.toString()}`;
};

const serializeModelAliases = (models?: ModelAlias[]) =>
  Array.isArray(models)
    ? models
        .map((model) => {
          if (!model?.name) return null;
          const payload: Record<string, unknown> = { name: model.name };
          if (model.alias && model.alias !== model.name) {
            payload.alias = model.alias;
          }
          if (model.displayName?.trim()) {
            payload['display-name'] = model.displayName.trim();
          }
          if (model.priority !== undefined) {
            payload.priority = model.priority;
          }
          if (model.testModel) {
            payload['test-model'] = model.testModel;
          }
          if (model.image !== undefined) {
            payload.image = model.image;
          }
          if (model.forceMapping !== undefined) {
            payload['force-mapping'] = model.forceMapping;
          }
          if (model.maxContextLength !== undefined) {
            payload['max-context-length'] = model.maxContextLength;
          }
          if (model.isCompat !== undefined) {
            payload['is-compat'] = model.isCompat;
          }
          if (model.openapi !== undefined) {
            payload.openapi = model.openapi;
          }
          if (model.supportConfigurationUpdate !== undefined) {
            payload['support-configuration-update'] = model.supportConfigurationUpdate;
          }
          if (model.useMaxCompletionTokens !== undefined) {
            payload['use-max-completion-tokens'] = model.useMaxCompletionTokens;
          }
          if (model.inputModalities !== undefined) {
            payload['input-modalities'] = model.inputModalities;
          }
          if (model.outputModalities !== undefined) {
            payload['output-modalities'] = model.outputModalities;
          }
          if (isRecord(model.thinking)) {
            payload.thinking = model.thinking;
          }
          if (hasModelThinkingLevelsClearMarker(model)) {
            // The non-enumerable marker is consumed by the raw merge layer.
            markModelThinkingLevelsForClear(payload);
          }
          if (hasModelThinkingLevelsEditMarker(model)) {
            markModelThinkingLevelsForEdit(payload);
          }
          return payload;
        })
        .filter(Boolean)
    : undefined;

const serializeAuthIndex = (value?: string) => {
  const trimmed = String(value ?? '').trim();
  return trimmed || undefined;
};

const serializeApiKeyEntry = (entry: ApiKeyEntry) => {
  const payload: Record<string, unknown> = {};
  const apiKey = entry.apiKey?.trim();
  if (apiKey) {
    payload['api-key'] = apiKey;
  } else if (isPersistedOpenAIKeyEntry(entry)) {
    payload['api-key'] = '';
  }
  const authIndex = serializeAuthIndex(entry.authIndex);
  if (authIndex) payload['auth-index'] = authIndex;
  if (entry.weight !== undefined) payload.weight = entry.weight;
  if (entry.proxyUrl) payload['proxy-url'] = entry.proxyUrl;
  const headers = serializeHeaders(entry.headers);
  if (headers) payload.headers = headers;
  return preservePersistedOpenAIKeyEntry(entry, payload);
};

const serializeProviderKey = (config: ProviderKeyConfig) => {
  const payload: Record<string, unknown> = {};
  const apiKey = config.apiKey?.trim();
  if (apiKey) payload['api-key'] = apiKey;
  const authIndex = serializeAuthIndex(config.authIndex);
  if (authIndex) payload['auth-index'] = authIndex;
  if (config.priority !== undefined) payload.priority = config.priority;
  if (config.weight !== undefined) payload.weight = config.weight;
  if (config.prefix?.trim()) payload.prefix = config.prefix.trim();
  if (config.baseUrl) payload['base-url'] = config.baseUrl;
  if (config.websockets !== undefined) payload.websockets = config.websockets;
  if (config.disableCooling !== undefined) payload['disable-cooling'] = config.disableCooling;
  if (config.fingerprintProfile !== undefined) {
    // '' is the form's explicit "clear fingerprint-profile" signal; the
    // merge layer strips the empty key instead of writing it to CPA.
    payload['fingerprint-profile'] = config.fingerprintProfile.trim();
  }
  if (config.rebuildMidSystemMessage !== undefined) {
    payload['rebuild-mid-system-message'] = config.rebuildMidSystemMessage;
  }
  if (config.proxyUrl) payload['proxy-url'] = config.proxyUrl;
  const headers = serializeHeaders(config.headers);
  if (headers) payload.headers = headers;
  const models = serializeModelAliases(config.models);
  if (models && models.length) payload.models = models;
  if (config.excludedModels && config.excludedModels.length) {
    payload['excluded-models'] = config.excludedModels;
  }
  if ('cloak' in config && config.cloak === null) {
    payload.cloak = null;
  } else if (isRecord(config.cloak)) {
    const cloakPayload: Record<string, unknown> = {};
    const mode = typeof config.cloak.mode === 'string' ? config.cloak.mode.trim() : '';
    if (mode) cloakPayload.mode = mode;
    if (typeof config.cloak.strictMode === 'boolean') {
      cloakPayload['strict-mode'] = config.cloak.strictMode;
    }
    if (Array.isArray(config.cloak.sensitiveWords) && config.cloak.sensitiveWords.length > 0) {
      cloakPayload['sensitive-words'] = config.cloak.sensitiveWords;
    }
    if (typeof config.cloak.cacheUserId === 'boolean') {
      cloakPayload['cache-user-id'] = config.cloak.cacheUserId;
    }
    if (isRecord(config.cloak.sensitiveWordsReplace)) {
      cloakPayload['sensitive-words-replace'] = { ...config.cloak.sensitiveWordsReplace };
    }
    if (Array.isArray(config.cloak.sensitivePatternsReplace)) {
      cloakPayload['sensitive-patterns-replace'] = config.cloak.sensitivePatternsReplace
        .filter(isRecord)
        .map((rule) => ({ ...rule }));
    }
    payload.cloak = cloakPayload;
  }
  return payload;
};

const serializeVertexModelAliases = (models?: ModelAlias[]) =>
  Array.isArray(models)
    ? models
        .map((model) => {
          const name = typeof model?.name === 'string' ? model.name.trim() : '';
          if (!name) return null;
          const payload: Record<string, unknown> = { name };
          const alias = typeof model?.alias === 'string' ? model.alias.trim() : '';
          if (alias && alias !== name) {
            payload.alias = alias;
          }
          if (model.displayName?.trim()) {
            payload['display-name'] = model.displayName.trim();
          }
          if (model.forceMapping !== undefined) {
            payload['force-mapping'] = model.forceMapping;
          }
          if (model.openapi !== undefined) {
            payload.openapi = model.openapi;
          }
          if (isRecord(model.thinking)) {
            payload.thinking = model.thinking;
          }
          if (hasModelThinkingLevelsClearMarker(model)) {
            markModelThinkingLevelsForClear(payload);
          }
          if (hasModelThinkingLevelsEditMarker(model)) {
            markModelThinkingLevelsForEdit(payload);
          }
          return payload;
        })
        .filter(Boolean)
    : undefined;

const serializeCodexKey = (config: ProviderKeyConfig) => {
  const payload = serializeProviderKey(config);
  delete payload.cloak;
  delete payload['fingerprint-profile'];
  delete payload['rebuild-mid-system-message'];
  return payload;
};

const serializeClaudeKey = (config: ProviderKeyConfig) => {
  const payload = serializeProviderKey(config);
  delete payload.websockets;
  return payload;
};

// An update that leaves the fingerprint untouched must not carry it in the
// payload: the round-tripped form value would otherwise read as an explicit
// change and make the merge layer drop legacy CCH fields from the raw config.
const serializeClaudeUpdatePayload = (original: ProviderKeyConfig, value: ProviderKeyConfig) => {
  const payload = serializeClaudeKey(value);
  if (value.fingerprintProfile === original.fingerprintProfile) {
    delete payload['fingerprint-profile'];
  }
  const cloakExplicitlyNull = 'cloak' in value && value.cloak === null;
  if (cloakExplicitlyNull || (original.cloak !== undefined && value.cloak === undefined)) {
    payload.cloak = null;
  }
  return payload;
};

const serializeVertexKey = (config: ProviderKeyConfig) => {
  const payload: Record<string, unknown> = {};
  const apiKey = config.apiKey?.trim();
  if (apiKey) payload['api-key'] = apiKey;
  const authIndex = serializeAuthIndex(config.authIndex);
  if (authIndex) payload['auth-index'] = authIndex;
  if (config.priority !== undefined) payload.priority = config.priority;
  if (config.weight !== undefined) payload.weight = config.weight;
  if (config.prefix?.trim()) payload.prefix = config.prefix.trim();
  if (config.baseUrl) payload['base-url'] = config.baseUrl;
  if (config.disableCooling !== undefined) payload['disable-cooling'] = config.disableCooling;
  if (config.proxyUrl) payload['proxy-url'] = config.proxyUrl;
  const headers = serializeHeaders(config.headers);
  if (headers) payload.headers = headers;
  const models = serializeVertexModelAliases(config.models);
  if (models && models.length) payload.models = models;
  if (config.excludedModels && config.excludedModels.length) {
    payload['excluded-models'] = config.excludedModels;
  }
  return payload;
};

const serializeGeminiKey = (config: GeminiKeyConfig) => {
  const payload: Record<string, unknown> = {};
  const apiKey = config.apiKey?.trim();
  const baseUrl = config.baseUrl?.trim();
  if (!apiKey && !baseUrl) {
    throw new Error('API key is required for Gemini and Interactions providers');
  }
  payload['api-key'] = apiKey || '';
  if (config.priority !== undefined) payload.priority = config.priority;
  if (config.weight !== undefined) payload.weight = config.weight;
  if (config.prefix?.trim()) payload.prefix = config.prefix.trim();
  if (config.baseUrl) payload['base-url'] = config.baseUrl;
  if (config.proxyUrl) payload['proxy-url'] = config.proxyUrl;
  if (config.disableCooling !== undefined) payload['disable-cooling'] = config.disableCooling;
  const headers = serializeHeaders(config.headers);
  if (headers) payload.headers = headers;
  const models = serializeModelAliases(config.models);
  if (models && models.length) payload.models = models;
  if (config.excludedModels && config.excludedModels.length) {
    payload['excluded-models'] = config.excludedModels;
  }
  return payload;
};

const serializeOpenAIProvider = (provider: OpenAIProviderConfig) => {
  const payload: Record<string, unknown> = {
    name: provider.name,
    'base-url': provider.baseUrl,
    'api-key-entries': Array.isArray(provider.apiKeyEntries)
      ? provider.apiKeyEntries
          .filter((entry) => hasOpenAIKeyEntryConfiguration(entry))
          .map((entry) => serializeApiKeyEntry(entry))
      : [],
  };
  const authIndex = serializeAuthIndex(provider.authIndex);
  if (authIndex) payload['auth-index'] = authIndex;
  if (provider.prefix?.trim()) payload.prefix = provider.prefix.trim();
  if (provider.disabled !== undefined) payload.disabled = provider.disabled;
  if (provider.disableCooling !== undefined) payload['disable-cooling'] = provider.disableCooling;
  const headers = serializeHeaders(provider.headers);
  if (headers) payload.headers = headers;
  const models = serializeModelAliases(provider.models);
  if (models && models.length) payload.models = models;
  if (provider.priority !== undefined) payload.priority = provider.priority;
  if (provider.testModel) payload['test-model'] = provider.testModel;
  return payload;
};

export interface ProvidersV8Snapshot {
  gemini: GeminiKeyConfig[];
  interactions: GeminiKeyConfig[];
  codex: ProviderKeyConfig[];
  xai: ProviderKeyConfig[];
  claude: ProviderKeyConfig[];
  vertex: ProviderKeyConfig[];
  meta?: ProviderKeyConfig[];
  openai: OpenAIProviderConfig[];
  v8Supported: boolean;
}

export interface ProviderWriteOptions {
  expectedEtag?: string;
}
const EDITABLE_V8_NATIVE_SHARED_FIELDS = [
  'priority',
  'prefix',
  'proxy-url',
  'headers',
  'models',
  'excluded-models',
  'disable-cooling',
] as const;

const V8_NATIVE_GROUP_STRIP_ON_REBUILD = [
  'keys',
  'name',
  'base-url',
  'baseUrl',
  'base_url',
  'auth-index',
  'authIndex',
  'auth_index',
  'test-model',
  'testModel',
  'test_model',
  'v8Source',
  'priority',
  'prefix',
  'proxy-url',
  'proxyUrl',
  'proxy_url',
  'headers',
  'models',
  'excluded-models',
  'excludedModels',
  'excluded_models',
  'disable-cooling',
  'disableCooling',
  'disable_cooling',
] as const;

const readResponseEtag = (headers: unknown): string | undefined => {
  if (!isRecord(headers)) return undefined;
  if ('get' in headers && typeof headers.get === 'function') {
    const viaGetter: unknown = headers.get('etag') ?? headers.get('ETag');
    const normalized = normalizeEtagValue(viaGetter);
    if (normalized) return normalized;
  }
  for (const [key, value] of Object.entries(headers)) {
    if (key.toLowerCase() === 'etag') {
      const normalized = normalizeEtagValue(value);
      if (normalized) return normalized;
    }
  }
  return undefined;
};

const readErrorStatus = (error: unknown): number | undefined => {
  if (!isRecord(error)) return undefined;
  if (typeof error.status === 'number') return error.status;
  if (isRecord(error.response) && typeof error.response.status === 'number') {
    return error.response.status;
  }
  return undefined;
};

const isV8UnsupportedStatus = (error: unknown): boolean => {
  const status = readErrorStatus(error);
  return status === 404 || status === 405;
};

const isV8MissingConfigPathError = (error: unknown): boolean => {
  if (readErrorStatus(error) !== 404 || !isRecord(error)) return false;
  const details = isRecord(error.details)
    ? error.details
    : isRecord(error.data)
      ? error.data
      : isRecord(error.response) && isRecord(error.response.data)
        ? error.response.data
        : null;
  return details?.error === 'not_found';
};

const extractRawV8GroupsArray = (data: unknown, family: ProviderV8Family): Record<string, unknown>[] => {
  if (Array.isArray(data)) return data.filter(isRecord);
  if (!isRecord(data)) return [];
  const direct = data[family];
  if (Array.isArray(direct)) return direct.filter(isRecord);
  const nested = data['api-keys'];
  if (isRecord(nested) && Array.isArray(nested[family])) {
    return nested[family].filter(isRecord);
  }
  return [];
};

const resolveV8GroupName = (
  desiredName: string | undefined,
  usedNames: Set<string>,
  family: ProviderV8Family
): string => {
  const trimmed = desiredName?.trim() ?? '';
  if (!trimmed) {
    let index = 1;
    while (usedNames.has(`${family}-${index}`)) {
      index += 1;
    }
    const generated = `${family}-${index}`;
    usedNames.add(generated);
    return generated;
  }
  if (!usedNames.has(trimmed)) {
    usedNames.add(trimmed);
    return trimmed;
  }
  let suffix = 2;
  while (usedNames.has(`${trimmed}-${suffix}`)) {
    suffix += 1;
  }
  const disambiguated = `${trimmed}-${suffix}`;
  usedNames.add(disambiguated);
  return disambiguated;
};

const collectUsedGroupNames = (
  groups: Record<string, unknown>[],
  excludeGroupIndex?: number
): Set<string> => {
  const used = new Set<string>();
  groups.forEach((group, index) => {
    if (index === excludeGroupIndex) return;
    if (typeof group.name === 'string' && group.name.trim()) {
      used.add(group.name.trim());
    }
  });
  return used;
};

const V8_THINKING_SUPPORT_ALLOWED_FIELDS: Record<string, true> = {
  min: true,
  max: true,
  'zero-allowed': true,
  'dynamic-allowed': true,
  'disable-type': true,
  levels: true,
};

const normalizeThinkingForV8Write = (
  thinking: Record<string, unknown>
): Record<string, unknown> => {
  const next: Record<string, unknown> = { ...thinking };
  const aliasPairs: ReadonlyArray<readonly [readonly string[], string]> = [
    [['zero_allowed', 'zeroAllowed'], 'zero-allowed'],
    [['dynamic_allowed', 'dynamicAllowed'], 'dynamic-allowed'],
    [['disable_type', 'disableType'], 'disable-type'],
  ];
  aliasPairs.forEach(([legacyKeys, canonicalKey]) => {
    if (next[canonicalKey] === undefined) {
      for (const legacyKey of legacyKeys) {
        if (next[legacyKey] !== undefined) {
          next[canonicalKey] = next[legacyKey];
          break;
        }
      }
    }
    legacyKeys.forEach((legacyKey) => {
      delete next[legacyKey];
    });
  });
  Object.keys(next).forEach((field) => {
    if (!V8_THINKING_SUPPORT_ALLOWED_FIELDS[field]) {
      delete next[field];
    }
  });
  return next;
};

const finalizeModelRecordForWrite = (model: unknown, stripTestModel: boolean): unknown => {
  if (!isRecord(model)) return model;
  const next = stripModelThinkingLevelsMarkers(model);
  if (hasModelThinkingLevelsClearMarker(model)) {
    const nextThinking = isRecord(model.thinking) ? { ...model.thinking } : {};
    delete nextThinking.levels;
    const preserveExplicitEmptyThinking =
      isRecord(model.thinking) && Object.keys(model.thinking).length === 0;
    if (Object.keys(nextThinking).length > 0 || preserveExplicitEmptyThinking) {
      next.thinking = nextThinking;
    } else {
      delete next.thinking;
    }
  } else if (hasModelThinkingLevelsEditMarker(model) && isRecord(next.thinking)) {
    next.thinking = removeThinkingFlagAliases(next.thinking, [
      ...THINKING_ZERO_ALLOWED_FIELDS,
      ...THINKING_DYNAMIC_ALLOWED_FIELDS,
    ]);
  } else if (next.thinking === null) {
    delete next.thinking;
  }
  if (stripTestModel) {
    delete next['test-model'];
    delete next.testModel;
    delete next.test_model;
  }
  return next;
};

const V8_COMMON_NATIVE_MODEL_ALLOWED_FIELDS: Record<string, true> = {
  name: true,
  alias: true,
  'display-name': true,
  'force-mapping': true,
  'max-context-length': true,
  'is-compat': true,
  thinking: true,
};

const V8_OPENAI_COMPATIBILITY_MODEL_ALLOWED_FIELDS: Record<string, true> = {
  ...V8_COMMON_NATIVE_MODEL_ALLOWED_FIELDS,
  image: true,
  'input-modalities': true,
  'output-modalities': true,
  'use-max-completion-tokens': true,
};

const V8_CODEX_MODEL_ALLOWED_FIELDS: Record<string, true> = {
  ...V8_COMMON_NATIVE_MODEL_ALLOWED_FIELDS,
  'support-configuration-update': true,
};

const V8_VERTEX_MODEL_ALLOWED_FIELDS: Record<string, true> = {
  name: true,
  alias: true,
  'display-name': true,
  'force-mapping': true,
  openapi: true,
  thinking: true,
};

const getAllowedV8ModelFields = (family: ProviderV8Family): Readonly<Record<string, true>> => {
  switch (family) {
    case 'openai-compatibility':
      return V8_OPENAI_COMPATIBILITY_MODEL_ALLOWED_FIELDS;
    case 'codex':
    case 'xai':
    case 'meta':
      return V8_CODEX_MODEL_ALLOWED_FIELDS;
    case 'vertex':
      return V8_VERTEX_MODEL_ALLOWED_FIELDS;
    case 'claude':
    case 'gemini':
    case 'interactions':
      return V8_COMMON_NATIVE_MODEL_ALLOWED_FIELDS;
  }
};

const cleanModelRecordForV8Family = (family: ProviderV8Family, model: unknown): unknown => {
  const wasExplicitEmptyThinking =
    isRecord(model) && isRecord(model.thinking) && Object.keys(model.thinking).length === 0;
  const finalized = finalizeModelRecordForWrite(model, true);
  if (!isRecord(finalized)) return finalized;
  const next: Record<string, unknown> = { ...finalized };

  const canonicalPairs: ReadonlyArray<readonly [readonly string[], string]> = [
    [['displayName', 'display_name'], 'display-name'],
    [['forceMapping', 'force_mapping'], 'force-mapping'],
    [['maxContextLength', 'max_context_length'], 'max-context-length'],
    [['isCompat', 'is_compat'], 'is-compat'],
    [['supportConfigurationUpdate', 'support_configuration_update'], 'support-configuration-update'],
    [['useMaxCompletionTokens', 'use_max_completion_tokens'], 'use-max-completion-tokens'],
    [['inputModalities', 'input_modalities'], 'input-modalities'],
    [['outputModalities', 'output_modalities'], 'output-modalities'],
  ];
  canonicalPairs.forEach(([legacyKeys, canonicalKey]) => {
    if (next[canonicalKey] === undefined) {
      for (const legacyKey of legacyKeys) {
        if (next[legacyKey] !== undefined) {
          next[canonicalKey] = next[legacyKey];
          break;
        }
      }
    }
    legacyKeys.forEach((legacyKey) => {
      delete next[legacyKey];
    });
  });

  if (isRecord(next.thinking)) {
    const normalizedThinking = normalizeThinkingForV8Write(next.thinking);
    if (Object.keys(normalizedThinking).length > 0 || wasExplicitEmptyThinking) {
      next.thinking = normalizedThinking;
    } else {
      delete next.thinking;
    }
  }

  const allowedFields = getAllowedV8ModelFields(family);
  Object.keys(next).forEach((field) => {
    if (!allowedFields[field]) {
      delete next[field];
    }
  });
  return next;
};

const V8_BASE_NATIVE_GROUP_ALLOWED_FIELDS = [
  'name',
  'base-url',
  'keys',
  'priority',
  'prefix',
  'proxy-url',
  'headers',
  'models',
  'excluded-models',
  'disable-cooling',
  'request-retry',
  'connection-pool',
] as const;

const getAllowedNativeV8GroupFields = (
  family: Exclude<ProviderV8Family, 'openai-compatibility'>
): Set<string> => {
  const allowed = new Set<string>(V8_BASE_NATIVE_GROUP_ALLOWED_FIELDS);
  if (family !== 'vertex') {
    allowed.add('request-scoped-errors');
  }
  return allowed;
};

const getFamilyKeyOnlyFields = (
  family: Exclude<ProviderV8Family, 'openai-compatibility'>
): readonly string[] => {
  switch (family) {
    case 'claude':
      return [
        'weight',
        'cloak',
        'fingerprint-profile',
        'experimental-cch-signing',
        'rebuild-mid-system-message',
        'native-tool-search',
      ];
    case 'codex':
    case 'xai':
    case 'meta':
      return ['weight', 'websockets', 'alpha-search', 'disable-codex-cloaking'];
    case 'vertex':
      return ['weight', 'interactions', 'openapi', 'openapi-location'];
    case 'gemini':
    case 'interactions':
      return ['weight'];
  }
};

const canonicalizeRecordFieldAliases = (
  target: Record<string, unknown>,
  aliases: readonly string[],
  canonical: string
): void => {
  if (target[canonical] === undefined) {
    for (const alias of aliases) {
      if (target[alias] !== undefined) {
        target[canonical] = target[alias];
        break;
      }
    }
  }
  aliases.forEach((alias) => {
    if (alias !== canonical) {
      delete target[alias];
    }
  });
};

const V8_CLOAK_ALLOWED_FIELDS: Record<string, true> = {
  mode: true,
  'strict-mode': true,
  'sensitive-words': true,
  'sensitive-words-replace': true,
  'sensitive-patterns-replace': true,
  'cache-user-id': true,
};

const V8_SENSITIVE_PATTERN_RULE_ALLOWED_FIELDS: Record<string, true> = {
  pattern: true,
  replacement: true,
};

const V8_CONNECTION_POOL_ALLOWED_FIELDS: Record<string, true> = {
  enabled: true,
  'idle-conn-timeout': true,
  'max-idle-conns-per-host': true,
};

const V8_REQUEST_SCOPED_ERROR_ALLOWED_FIELDS: Record<string, true> = {
  status: true,
  match: true,
  'match-regexr': true,
  action: true,
};

const coerceV8Boolean = (value: unknown): boolean | undefined => {
  if (typeof value === 'boolean') return value;
  if (typeof value === 'string') {
    const normalized = value.trim().toLowerCase();
    if (normalized === 'true') return true;
    if (normalized === 'false') return false;
  }
  return undefined;
};

const coerceV8Integer = (value: unknown): number | undefined => {
  if (typeof value === 'number') {
    return Number.isFinite(value) && Number.isInteger(value) ? value : undefined;
  }
  if (typeof value === 'string') {
    const trimmed = value.trim();
    if (!/^-?\d+$/.test(trimmed)) return undefined;
    const parsed = Number(trimmed);
    return Number.isSafeInteger(parsed) ? parsed : undefined;
  }
  return undefined;
};

const normalizeConnectionPoolForV8Write = (raw: unknown): Record<string, unknown> | undefined => {
  if (!isRecord(raw)) return undefined;
  const wasEmptyObject = Object.keys(raw).length === 0;
  const next: Record<string, unknown> = { ...raw };
  canonicalizeRecordFieldAliases(
    next,
    ['idleConnTimeout', 'idle_conn_timeout'],
    'idle-conn-timeout'
  );
  canonicalizeRecordFieldAliases(
    next,
    ['maxIdleConnsPerHost', 'max_idle_conns_per_host'],
    'max-idle-conns-per-host'
  );
  if (next.enabled !== undefined) {
    const enabled = coerceV8Boolean(next.enabled);
    if (enabled !== undefined) {
      next.enabled = enabled;
    } else {
      delete next.enabled;
    }
  }
  if (next['idle-conn-timeout'] !== undefined) {
    if (typeof next['idle-conn-timeout'] === 'string') {
      next['idle-conn-timeout'] = next['idle-conn-timeout'].trim();
    } else {
      delete next['idle-conn-timeout'];
    }
  }
  if (next['max-idle-conns-per-host'] !== undefined) {
    const maxIdle = coerceV8Integer(next['max-idle-conns-per-host']);
    if (maxIdle !== undefined) {
      next['max-idle-conns-per-host'] = maxIdle;
    } else {
      delete next['max-idle-conns-per-host'];
    }
  }
  Object.keys(next).forEach((field) => {
    if (!V8_CONNECTION_POOL_ALLOWED_FIELDS[field]) {
      delete next[field];
    }
  });
  if (Object.keys(next).length > 0 || wasEmptyObject) {
    return next;
  }
  return undefined;
};

const normalizeRequestScopedErrorsForV8Write = (
  raw: unknown
): Record<string, unknown>[] | undefined => {
  if (!Array.isArray(raw)) return undefined;
  return raw
    .filter(isRecord)
    .map((rule) => {
      const nextRule: Record<string, unknown> = { ...rule };
      canonicalizeRecordFieldAliases(
        nextRule,
        ['matchRegexr', 'match_regexr', 'match-regex', 'matchRegex', 'match_regex'],
        'match-regexr'
      );
      if (nextRule.status !== undefined) {
        const status = coerceV8Integer(nextRule.status);
        if (status !== undefined) {
          nextRule.status = status;
        } else {
          delete nextRule.status;
        }
      }
      if (nextRule.match !== undefined) {
        if (Array.isArray(nextRule.match)) {
          const filteredMatch = nextRule.match.filter(
            (item): item is string => typeof item === 'string' && item.length > 0
          );
          if (filteredMatch.length > 0) {
            nextRule.match = filteredMatch;
          } else {
            delete nextRule.match;
          }
        } else {
          delete nextRule.match;
        }
      }
      if (nextRule['match-regexr'] !== undefined) {
        if (Array.isArray(nextRule['match-regexr'])) {
          const filteredRegex = nextRule['match-regexr'].filter(
            (item): item is string => typeof item === 'string' && item.length > 0
          );
          if (filteredRegex.length > 0) {
            nextRule['match-regexr'] = filteredRegex;
          } else {
            delete nextRule['match-regexr'];
          }
        } else {
          delete nextRule['match-regexr'];
        }
      }
      if (nextRule.action !== undefined) {
        if (typeof nextRule.action === 'string' && nextRule.action.trim()) {
          nextRule.action = nextRule.action.trim();
        } else {
          delete nextRule.action;
        }
      }
      Object.keys(nextRule).forEach((field) => {
        if (!V8_REQUEST_SCOPED_ERROR_ALLOWED_FIELDS[field]) {
          delete nextRule[field];
        }
      });
      return nextRule;
    })
    .filter((rule) => Object.keys(rule).length > 0);
};

const normalizeCloakForV8Write = (cloak: Record<string, unknown>): Record<string, unknown> => {
  const next: Record<string, unknown> = { ...cloak };
  canonicalizeRecordFieldAliases(next, ['strictMode', 'strict_mode'], 'strict-mode');
  canonicalizeRecordFieldAliases(next, ['sensitiveWords', 'sensitive_words'], 'sensitive-words');
  canonicalizeRecordFieldAliases(
    next,
    ['sensitiveWordsReplace', 'sensitive_words_replace'],
    'sensitive-words-replace'
  );
  canonicalizeRecordFieldAliases(
    next,
    ['sensitivePatternsReplace', 'sensitive_patterns_replace'],
    'sensitive-patterns-replace'
  );
  canonicalizeRecordFieldAliases(
    next,
    ['cacheUserId', 'cacheUserID', 'cache_user_id'],
    'cache-user-id'
  );
  if (next.mode !== undefined) {
    if (typeof next.mode === 'string' && next.mode.trim()) {
      next.mode = next.mode.trim();
    } else {
      delete next.mode;
    }
  }
  if (next['strict-mode'] !== undefined) {
    const strictMode = coerceV8Boolean(next['strict-mode']);
    if (strictMode !== undefined) {
      next['strict-mode'] = strictMode;
    } else {
      delete next['strict-mode'];
    }
  }
  if (next['cache-user-id'] !== undefined) {
    const cacheUserId = coerceV8Boolean(next['cache-user-id']);
    if (cacheUserId !== undefined) {
      next['cache-user-id'] = cacheUserId;
    } else {
      delete next['cache-user-id'];
    }
  }
  if (next['sensitive-words'] !== undefined) {
    if (Array.isArray(next['sensitive-words'])) {
      const filteredWords = next['sensitive-words']
        .filter((item): item is string => typeof item === 'string')
        .map((item) => item.trim())
        .filter((item) => item.length > 0);
      if (filteredWords.length > 0) {
        next['sensitive-words'] = filteredWords;
      } else {
        delete next['sensitive-words'];
      }
    } else {
      delete next['sensitive-words'];
    }
  }
  if (next['sensitive-words-replace'] !== undefined) {
    if (isRecord(next['sensitive-words-replace'])) {
      const cleanedWordsReplace: Record<string, string> = {};
      Object.entries(next['sensitive-words-replace']).forEach(([word, replacement]) => {
        if (word.length > 0 && typeof replacement === 'string') {
          cleanedWordsReplace[word] = replacement;
        }
      });
      if (Object.keys(cleanedWordsReplace).length > 0) {
        next['sensitive-words-replace'] = cleanedWordsReplace;
      } else {
        delete next['sensitive-words-replace'];
      }
    } else {
      delete next['sensitive-words-replace'];
    }
  }
  if (Array.isArray(next['sensitive-patterns-replace'])) {
    const cleanedPatterns = next['sensitive-patterns-replace']
      .filter(isRecord)
      .map((rule) => {
        const nextRule: Record<string, unknown> = { ...rule };
        canonicalizeRecordFieldAliases(nextRule, ['replace'], 'replacement');
        if (typeof nextRule.pattern !== 'string' || nextRule.pattern.length === 0) {
          delete nextRule.pattern;
        }
        if (nextRule.replacement !== undefined && typeof nextRule.replacement !== 'string') {
          delete nextRule.replacement;
        }
        Object.keys(nextRule).forEach((field) => {
          if (!V8_SENSITIVE_PATTERN_RULE_ALLOWED_FIELDS[field]) {
            delete nextRule[field];
          }
        });
        return nextRule;
      })
      .filter((rule) => typeof rule.pattern === 'string' && rule.pattern.length > 0);
    if (cleanedPatterns.length > 0) {
      next['sensitive-patterns-replace'] = cleanedPatterns;
    } else {
      delete next['sensitive-patterns-replace'];
    }
  } else if (next['sensitive-patterns-replace'] !== undefined) {
    delete next['sensitive-patterns-replace'];
  }
  Object.keys(next).forEach((field) => {
    if (!V8_CLOAK_ALLOWED_FIELDS[field]) {
      delete next[field];
    }
  });
  return next;
};

const V8_OPENAI_COMPATIBILITY_GROUP_ALLOWED_FIELDS: Record<string, true> = {
  name: true,
  priority: true,
  disabled: true,
  'prefix': true,
  'base-url': true,
  headers: true,
  keys: true,
  models: true,
  'support-prompt-cache-key': true,
  'disable-cooling': true,
  'request-retry': true,
  'request-scoped-errors': true,
  'connection-pool': true,
};

const sanitizeV8RecordForFamily = (
  family: ProviderV8Family,
  record: Record<string, unknown>
): void => {
  delete record['test-model'];
  delete record.testModel;
  delete record.test_model;
  delete record['auth-index'];
  delete record.authIndex;
  delete record['auth_index'];
  delete record.v8Source;

  canonicalizeRecordFieldAliases(record, ['proxyUrl', 'proxy_url'], 'proxy-url');
  canonicalizeRecordFieldAliases(record, ['excludedModels', 'excluded_models'], 'excluded-models');
  canonicalizeRecordFieldAliases(record, ['connectionPool', 'connection_pool'], 'connection-pool');
  canonicalizeRecordFieldAliases(record, ['requestRetry', 'request_retry'], 'request-retry');
  canonicalizeRecordFieldAliases(
    record,
    ['requestScopedErrors', 'request_scoped_errors'],
    'request-scoped-errors'
  );
  canonicalizeRecordFieldAliases(
    record,
    ['fingerprintProfile', 'fingerprint_profile'],
    'fingerprint-profile'
  );
  canonicalizeRecordFieldAliases(
    record,
    ['experimentalCchSigning', 'experimental_cch_signing'],
    'experimental-cch-signing'
  );
  canonicalizeRecordFieldAliases(
    record,
    ['rebuildMidSystemMessage', 'rebuild_mid_system_message'],
    'rebuild-mid-system-message'
  );
  canonicalizeRecordFieldAliases(
    record,
    ['nativeToolSearch', 'native_tool_search'],
    'native-tool-search'
  );
  canonicalizeRecordFieldAliases(record, ['alphaSearch', 'alpha_search'], 'alpha-search');
  canonicalizeRecordFieldAliases(
    record,
    ['disableCodexCloaking', 'disable_codex_cloaking'],
    'disable-codex-cloaking'
  );
  canonicalizeRecordFieldAliases(
    record,
    ['openapiLocation', 'openapi_location'],
    'openapi-location'
  );

  if (record['connection-pool'] !== undefined) {
    const normalizedPool = normalizeConnectionPoolForV8Write(record['connection-pool']);
    if (normalizedPool !== undefined) {
      record['connection-pool'] = normalizedPool;
    } else {
      delete record['connection-pool'];
    }
  }

  if (family !== 'claude') {
    delete record.cloak;
    delete record['fingerprint-profile'];
    delete record['experimental-cch-signing'];
    delete record['rebuild-mid-system-message'];
    delete record['native-tool-search'];
  } else {
    if (record['fingerprint-profile'] === '') {
      delete record['fingerprint-profile'];
    }
    if (record.cloak === null) {
      delete record.cloak;
    } else if (isRecord(record.cloak)) {
      record.cloak = normalizeCloakForV8Write(record.cloak);
    } else if (record.cloak !== undefined) {
      delete record.cloak;
    }
  }

  if (family !== 'codex' && family !== 'xai' && family !== 'meta') {
    delete record.websockets;
    delete record['alpha-search'];
    delete record['disable-codex-cloaking'];
  }

  if (family === 'vertex') {
    delete record['request-scoped-errors'];
  } else {
    delete record.interactions;
    delete record.openapi;
    delete record['openapi-location'];
    if (record['request-scoped-errors'] !== undefined) {
      const normalizedRules = normalizeRequestScopedErrorsForV8Write(
        record['request-scoped-errors']
      );
      if (normalizedRules !== undefined) {
        record['request-scoped-errors'] = normalizedRules;
      } else {
        delete record['request-scoped-errors'];
      }
    }
  }
};

const cleanV8GroupsForWrite = (
  family: ProviderV8Family,
  groups: Record<string, unknown>[]
): Record<string, unknown>[] => {
  const usedNames = collectUsedGroupNames(groups);
  return groups.flatMap((group) => {
    const nextGroup: Record<string, unknown> = { ...group };
    sanitizeV8RecordForFamily(family, nextGroup);

    if (Array.isArray(nextGroup.models)) {
      const cleanedModels = nextGroup.models
        .map((model) => cleanModelRecordForV8Family(family, model))
        .filter(Boolean);
      if (cleanedModels.length > 0) {
        nextGroup.models = cleanedModels;
      } else {
        delete nextGroup.models;
      }
    }

    const rawGroupCooling =
      nextGroup['disable-cooling'] !== undefined
        ? nextGroup['disable-cooling']
        : nextGroup.disableCooling !== undefined
          ? nextGroup.disableCooling
          : nextGroup.disable_cooling;
    delete nextGroup.disableCooling;
    delete nextGroup.disable_cooling;
    if (rawGroupCooling === null) {
      delete nextGroup['disable-cooling'];
    } else if (typeof rawGroupCooling === 'boolean') {
      nextGroup['disable-cooling'] = rawGroupCooling;
    } else {
      delete nextGroup['disable-cooling'];
    }

    if (family === 'openai-compatibility') {
      canonicalizeRecordFieldAliases(nextGroup, ['baseUrl', 'base_url'], 'base-url');
      canonicalizeRecordFieldAliases(
        nextGroup,
        ['supportPromptCacheKey', 'support_prompt_cache_key'],
        'support-prompt-cache-key'
      );
      const rawKeys = Array.isArray(group.keys) ? group.keys : [];
      nextGroup.keys = rawKeys.filter(isRecord).map((entry) => {
        const cleanedEntry: Record<string, unknown> = {};
        const rawApiKey = entry['api-key'] ?? entry.apiKey ?? entry.api_key;
        if (rawApiKey !== undefined) {
          cleanedEntry['api-key'] = rawApiKey;
        }
        if (entry.weight !== undefined) {
          cleanedEntry.weight = entry.weight;
        }
        const rawProxyUrl = entry['proxy-url'] ?? entry.proxyUrl ?? entry.proxy_url;
        if (rawProxyUrl !== undefined) {
          cleanedEntry['proxy-url'] = rawProxyUrl;
        }
        const rawConnectionPool =
          entry['connection-pool'] ?? entry.connectionPool ?? entry.connection_pool;
        const normalizedPool = normalizeConnectionPoolForV8Write(rawConnectionPool);
        if (normalizedPool !== undefined) {
          cleanedEntry['connection-pool'] = normalizedPool;
        }
        return cleanedEntry;
      });
      Object.keys(nextGroup).forEach((field) => {
        if (!V8_OPENAI_COMPATIBILITY_GROUP_ALLOWED_FIELDS[field]) {
          delete nextGroup[field];
        }
      });
      return [nextGroup];
    }

    let groupBaseUrl = getStringField(nextGroup, ['base-url', 'baseUrl', 'base_url']);
    delete nextGroup.baseUrl;
    delete nextGroup.base_url;
    if (groupBaseUrl) {
      nextGroup['base-url'] = groupBaseUrl;
    } else {
      delete nextGroup['base-url'];
    }

    const pushedDownKeyFields: Record<string, unknown> = {};
    getFamilyKeyOnlyFields(family).forEach((keyField) => {
      const candidate = nextGroup[keyField];
      if (candidate !== undefined && candidate !== null) {
        pushedDownKeyFields[keyField] = candidate;
      }
      delete nextGroup[keyField];
    });

    const allowedGroupFields = getAllowedNativeV8GroupFields(family);
    Object.keys(nextGroup).forEach((field) => {
      if (!allowedGroupFields.has(field)) {
        delete nextGroup[field];
      }
    });

    const allowedMemberFields = new Set<string>(['api-key', ...getFamilyKeyOnlyFields(family)]);
    allowedGroupFields.forEach((field) => {
      if (field !== 'name' && field !== 'base-url' && field !== 'keys') {
        allowedMemberFields.add(field);
      }
    });

    const rawKeys = Array.isArray(group.keys) ? group.keys : [];
    const preparedMembers = rawKeys.filter(isRecord).map((member) => {
      const memberBaseUrl = getStringField(member, ['base-url', 'baseUrl', 'base_url']);
      const memberCooling =
        member['disable-cooling'] !== undefined
          ? member['disable-cooling']
          : member.disableCooling !== undefined
            ? member.disableCooling
            : member.disable_cooling;
      const nextMember: Record<string, unknown> = { ...member };
      canonicalizeRecordFieldAliases(nextMember, ['apiKey', 'api_key'], 'api-key');
      delete nextMember['base-url'];
      delete nextMember.baseUrl;
      delete nextMember.base_url;
      delete nextMember['disable-cooling'];
      delete nextMember.disableCooling;
      delete nextMember.disable_cooling;
      delete nextMember.name;
      sanitizeV8RecordForFamily(family, nextMember);
      Object.entries(pushedDownKeyFields).forEach(([keyField, value]) => {
        if (nextMember[keyField] === undefined) {
          nextMember[keyField] = value;
        }
      });
      if (Array.isArray(nextMember.models)) {
        const cleanedModels = nextMember.models
          .map((model) => cleanModelRecordForV8Family(family, model))
          .filter(Boolean);
        if (
          cleanedModels.length > 0 ||
          (Array.isArray(member.models) && member.models.length === 0)
        ) {
          nextMember.models = cleanedModels;
        } else {
          delete nextMember.models;
        }
      }
      Object.keys(nextMember).forEach((field) => {
        if (!allowedMemberFields.has(field)) {
          delete nextMember[field];
        }
      });
      return { cleaned: nextMember, memberBaseUrl, memberCooling };
    });

    if (
      preparedMembers.length === 1 &&
      preparedMembers[0].memberBaseUrl &&
      preparedMembers[0].memberBaseUrl !== groupBaseUrl
    ) {
      groupBaseUrl = preparedMembers[0].memberBaseUrl;
      nextGroup['base-url'] = groupBaseUrl;
    }

    const groupCoolingBoolean =
      typeof nextGroup['disable-cooling'] === 'boolean' ? nextGroup['disable-cooling'] : undefined;
    const hasNullMemberCoolingReset =
      groupCoolingBoolean !== undefined &&
      preparedMembers.some((item) => item.memberCooling === null);

    if (hasNullMemberCoolingReset && groupCoolingBoolean !== undefined) {
      delete nextGroup['disable-cooling'];
      preparedMembers.forEach((item) => {
        if (item.memberCooling === null) {
          delete item.cleaned['disable-cooling'];
        } else if (typeof item.memberCooling === 'boolean') {
          item.cleaned['disable-cooling'] = item.memberCooling;
        } else {
          item.cleaned['disable-cooling'] = groupCoolingBoolean;
        }
      });
    } else {
      preparedMembers.forEach((item) => {
        if (item.memberCooling === null) {
          delete item.cleaned['disable-cooling'];
        } else if (typeof item.memberCooling === 'boolean') {
          item.cleaned['disable-cooling'] = item.memberCooling;
        }
      });
    }

    if (preparedMembers.length <= 1) {
      nextGroup.keys = preparedMembers.map((item) => item.cleaned);
      return [nextGroup];
    }

    const stayingMembers = preparedMembers.filter(
      (item) => !item.memberBaseUrl || item.memberBaseUrl === groupBaseUrl
    );
    const splitMembers = preparedMembers.filter(
      (item) => Boolean(item.memberBaseUrl) && item.memberBaseUrl !== groupBaseUrl
    );

    if (splitMembers.length === 0) {
      nextGroup.keys = stayingMembers.map((item) => item.cleaned);
      return [nextGroup];
    }

    const resultGroups: Record<string, unknown>[] = [];
    if (stayingMembers.length > 0) {
      nextGroup.keys = stayingMembers.map((item) => item.cleaned);
      resultGroups.push(nextGroup);
    } else if (typeof nextGroup.name === 'string' && nextGroup.name.trim()) {
      usedNames.delete(nextGroup.name.trim());
    }

    const baseGroupName = typeof nextGroup.name === 'string' ? nextGroup.name : undefined;
    splitMembers.forEach((item) => {
      const splitGroup: Record<string, unknown> = {
        ...nextGroup,
        name: resolveV8GroupName(baseGroupName, usedNames, family),
        'base-url': item.memberBaseUrl,
        keys: [item.cleaned],
      };
      resultGroups.push(splitGroup);
    });
    return resultGroups;
  });
};

const buildEffectiveEditableNativeRaw = (
  rawGroup?: Record<string, unknown>,
  rawKey?: Record<string, unknown>
): Record<string, unknown> | undefined => {
  if (!rawGroup && !rawKey) return undefined;
  const combined: Record<string, unknown> = { ...(rawKey || {}) };
  if (!rawGroup) return combined;
  const groupSharedCandidates: ReadonlyArray<readonly string[]> = [
    ['base-url', 'baseUrl', 'base_url'],
    ['priority'],
    ['prefix'],
    ['proxy-url', 'proxyUrl', 'proxy_url'],
    ['headers'],
    ['models'],
    ['excluded-models', 'excludedModels', 'excluded_models'],
    ['disable-cooling', 'disableCooling', 'disable_cooling'],
  ];
  groupSharedCandidates.forEach((aliases) => {
    const keyHasValue = aliases.some(
      (alias) => combined[alias] !== undefined && combined[alias] !== null
    );
    if (keyHasValue) return;
    for (const alias of aliases) {
      if (rawGroup[alias] !== undefined) {
        combined[alias] = rawGroup[alias];
        break;
      }
    }
  });
  return combined;
};

const buildSingleKeyV8NativeGroup = (params: {
  groupName: string;
  payload: Record<string, unknown>;
  knownFields: readonly string[];
  rawGroup?: Record<string, unknown>;
  rawKey?: Record<string, unknown>;
  keepSharedOnGroup?: boolean;
}): Record<string, unknown> => {
  const { groupName, payload, knownFields, rawGroup, rawKey, keepSharedOnGroup = false } = params;
  const effectiveRaw = buildEffectiveEditableNativeRaw(rawGroup, rawKey);
  const merged = mergeProviderKeyPayload(effectiveRaw, payload, knownFields);
  if (Array.isArray(merged.models)) {
    merged.models = merged.models.map((model) => finalizeModelRecordForWrite(model, true));
  }

  const preservedGroupUnknown: Record<string, unknown> = rawGroup ? { ...rawGroup } : {};
  V8_NATIVE_GROUP_STRIP_ON_REBUILD.forEach((field) => {
    delete preservedGroupUnknown[field];
  });

  const nextGroup: Record<string, unknown> = {
    name: groupName,
  };
  const baseUrl = merged['base-url'];
  delete merged['base-url'];
  if (typeof baseUrl === 'string' && baseUrl.trim()) {
    nextGroup['base-url'] = baseUrl;
  }
  Object.entries(preservedGroupUnknown).forEach(([field, value]) => {
    nextGroup[field] = value;
  });

  if (merged['disable-cooling'] === null) {
    delete merged['disable-cooling'];
    delete nextGroup['disable-cooling'];
  }

  EDITABLE_V8_NATIVE_SHARED_FIELDS.forEach((field) => {
    if (merged[field] === undefined) return;
    const livedOnGroupOnly =
      Boolean(rawGroup && hasOwnField(rawGroup, field)) &&
      (!rawKey || !hasOwnField(rawKey, field) || keepSharedOnGroup);
    if (livedOnGroupOnly) {
      nextGroup[field] = merged[field];
      delete merged[field];
    }
  });

  const apiKeyValue = typeof merged['api-key'] === 'string' ? merged['api-key'] : '';
  delete merged['api-key'];
  delete merged.name;
  delete merged['auth-index'];
  delete merged.authIndex;
  delete merged['auth_index'];
  delete merged['test-model'];
  delete merged.testModel;
  delete merged.test_model;
  delete merged.v8Source;

  const nextKey: Record<string, unknown> = {
    'api-key': apiKeyValue,
    ...merged,
  };
  nextGroup.keys = [nextKey];
  return nextGroup;
};

const emptyV8MemberOverride = (field: string): unknown => {
  if (field === 'models' || field === 'excluded-models') return [];
  if (field === 'headers') return {};
  if (field === 'priority') return 0;
  if (field === 'prefix' || field === 'proxy-url') return '';
  if (field === 'disable-cooling') return null;
  return undefined;
};

const V8_SHARED_FIELD_ALIASES: Record<
  (typeof EDITABLE_V8_NATIVE_SHARED_FIELDS)[number],
  readonly string[]
> = {
  priority: ['priority'],
  prefix: ['prefix'],
  'proxy-url': ['proxy-url', 'proxyUrl', 'proxy_url'],
  headers: ['headers'],
  models: ['models'],
  'excluded-models': ['excluded-models', 'excludedModels', 'excluded_models'],
  'disable-cooling': ['disable-cooling', 'disableCooling', 'disable_cooling'],
};

const updateMultiKeyV8NativeMemberInPlace = <T extends GeminiKeyConfig | ProviderKeyConfig>(params: {
  rawGroup: Record<string, unknown>;
  rawKey: Record<string, unknown>;
  original: T;
  value: T;
  serialize: (item: T) => Record<string, unknown>;
  updatePayload: Record<string, unknown>;
  knownFields: readonly string[];
}): Record<string, unknown> => {
  const { rawGroup, rawKey, original, value, serialize, updatePayload, knownFields } = params;
  const beforePayload = serialize(original);
  const afterPayload = serialize(value);
  const rawForModels: Record<string, unknown> = {
    ...rawKey,
    ...(rawKey.models === undefined && Array.isArray(rawGroup.models)
      ? { models: rawGroup.models }
      : {}),
  };
  const merged = mergeProviderKeyPayload(rawForModels, updatePayload, knownFields);
  if (Array.isArray(merged.models)) {
    merged.models = merged.models.map((model) => finalizeModelRecordForWrite(model, true));
  }

  EDITABLE_V8_NATIVE_SHARED_FIELDS.forEach((field) => {
    const aliases = V8_SHARED_FIELD_ALIASES[field];
    const rawKeyHasField = aliases.some((alias) => hasOwnField(rawKey, alias));
    const rawGroupValue = aliases
      .map((alias) => (hasOwnField(rawGroup, alias) ? rawGroup[alias] : undefined))
      .find((val) => val !== undefined);
    const unchanged = JSON.stringify(beforePayload[field]) === JSON.stringify(afterPayload[field]);
    if (unchanged && !rawKeyHasField && rawGroupValue !== undefined) {
      delete merged[field];
      return;
    }
    if (
      (beforePayload[field] !== undefined || rawKeyHasField) &&
      afterPayload[field] === undefined &&
      rawGroupValue !== undefined &&
      rawGroupValue !== null
    ) {
      const override = emptyV8MemberOverride(field);
      if (override !== undefined) {
        merged[field] = override;
      }
    }
  });

  delete merged['base-url'];
  delete merged.name;
  delete merged['auth-index'];
  delete merged.authIndex;
  delete merged['auth_index'];
  const apiKeyValue = typeof merged['api-key'] === 'string' ? merged['api-key'] : '';
  delete merged['api-key'];
  return {
    'api-key': apiKeyValue,
    ...merged,
  };
};

type LocatedV8NativeMember<T extends GeminiKeyConfig | ProviderKeyConfig> = {
  groupIndex: number;
  keyIndex: number;
  group: Record<string, unknown>;
  rawKey: Record<string, unknown>;
  normalized: T;
};

const locateV8NativeMember = <T extends GeminiKeyConfig | ProviderKeyConfig>(
  family: Exclude<ProviderV8Family, 'openai-compatibility'>,
  groups: Record<string, unknown>[],
  target: {
    apiKey?: string;
    baseUrl?: string;
    authIndex?: string;
    v8Source?: ProviderV8GroupSource;
  },
  usedSlots?: Set<string>
): LocatedV8NativeMember<T> | null => {
  const normalizedItems = normalizeV8ProviderGroups(groups, family) as T[];
  const slotKey = (gIdx: number, kIdx: number) => `${gIdx}:${kIdx}`;
  const targetApiKey = (target.apiKey ?? '').trim();
  const targetBaseUrl = (target.baseUrl ?? '').trim();
  const targetAuthIndex = (target.authIndex ?? '').trim();

  const matchesEntry = (entry: T, checkAuthIndex: boolean) => {
    if (checkAuthIndex && targetAuthIndex && (entry.authIndex ?? '').trim() === targetAuthIndex) {
      return true;
    }
    return (
      (entry.apiKey ?? '').trim() === targetApiKey &&
      (entry.baseUrl ?? '').trim() === targetBaseUrl
    );
  };

  const sourceHint = target.v8Source;
  const hintGroupRecord =
    sourceHint && sourceHint.family === family && isRecord(sourceHint.group)
      ? sourceHint.group
      : undefined;
  const hintRawKey =
    hintGroupRecord &&
    sourceHint?.keyIndex !== undefined &&
    Array.isArray(hintGroupRecord.keys) &&
    isRecord(hintGroupRecord.keys[sourceHint.keyIndex])
      ? hintGroupRecord.keys[sourceHint.keyIndex]
      : undefined;
  const hintApiKey = hintRawKey ? getStringField(hintRawKey, ['api-key', 'apiKey', 'api_key']) : '';
  const hintAuthIndex = hintRawKey ? getStringField(hintRawKey, AUTH_INDEX_FIELDS) : '';
  const hintBaseUrl =
    (hintRawKey ? getStringField(hintRawKey, ['base-url', 'baseUrl', 'base_url']) : '') ||
    (hintGroupRecord ? getStringField(hintGroupRecord, ['base-url', 'baseUrl', 'base_url']) : '');

  const matchesHintIdentity = (entry: T) => {
    if (!hintRawKey) return false;
    if (hintAuthIndex && (entry.authIndex ?? '').trim() === hintAuthIndex) {
      return true;
    }
    return (
      (entry.apiKey ?? '').trim() === hintApiKey &&
      (entry.baseUrl ?? '').trim() === hintBaseUrl
    );
  };

  if (sourceHint && sourceHint.family === family && sourceHint.keyIndex !== undefined) {
    const hintGroupIndex = sourceHint.groupIndex;
    const hintKeyIndex = sourceHint.keyIndex;
    const hinted = normalizedItems.find(
      (entry) =>
        entry.v8Source?.groupIndex === hintGroupIndex &&
        entry.v8Source?.keyIndex === hintKeyIndex &&
        !usedSlots?.has(slotKey(hintGroupIndex, hintKeyIndex)) &&
        (matchesEntry(entry, true) || matchesHintIdentity(entry))
    );
    if (hinted?.v8Source && hinted.v8Source.keyIndex !== undefined) {
      const { groupIndex, keyIndex } = hinted.v8Source;
      const group = groups[groupIndex];
      const rawKey = Array.isArray(group?.keys) ? group.keys[keyIndex] : undefined;
      if (isRecord(group) && isRecord(rawKey)) {
        usedSlots?.add(slotKey(groupIndex, keyIndex));
        return { groupIndex, keyIndex, group, rawKey, normalized: hinted };
      }
    }
  }

  const matched =
    normalizedItems.find((entry) => {
      const gIdx = entry.v8Source?.groupIndex;
      const kIdx = entry.v8Source?.keyIndex;
      if (gIdx === undefined || kIdx === undefined || usedSlots?.has(slotKey(gIdx, kIdx))) {
        return false;
      }
      return matchesEntry(entry, true);
    }) ??
    normalizedItems.find((entry) => {
      const gIdx = entry.v8Source?.groupIndex;
      const kIdx = entry.v8Source?.keyIndex;
      if (gIdx === undefined || kIdx === undefined || usedSlots?.has(slotKey(gIdx, kIdx))) {
        return false;
      }
      return matchesEntry(entry, false);
    }) ??
    normalizedItems.find((entry) => {
      const gIdx = entry.v8Source?.groupIndex;
      const kIdx = entry.v8Source?.keyIndex;
      if (gIdx === undefined || kIdx === undefined || usedSlots?.has(slotKey(gIdx, kIdx))) {
        return false;
      }
      return matchesHintIdentity(entry);
    });

  if (!matched?.v8Source || matched.v8Source.keyIndex === undefined) {
    return null;
  }
  const { groupIndex, keyIndex } = matched.v8Source;
  const group = groups[groupIndex];
  const rawKey = Array.isArray(group?.keys) ? group.keys[keyIndex] : undefined;
  if (!isRecord(group) || !isRecord(rawKey)) return null;
  usedSlots?.add(slotKey(groupIndex, keyIndex));
  return { groupIndex, keyIndex, group, rawKey, normalized: matched };
};

const buildV8OpenAIGroup = (
  provider: OpenAIProviderConfig,
  groupName: string,
  rawGroup?: Record<string, unknown>
): Record<string, unknown> => {
  const payload = serializeOpenAIProvider({
    ...provider,
    name: groupName,
  });
  const explicitSupportPromptCacheKey =
    typeof provider.supportPromptCacheKey === 'boolean'
      ? provider.supportPromptCacheKey
      : typeof provider['support-prompt-cache-key'] === 'boolean'
        ? provider['support-prompt-cache-key']
        : undefined;

  const rawForMerge = rawGroup
    ? {
        ...rawGroup,
        'api-key-entries': Array.isArray(rawGroup.keys)
          ? rawGroup.keys
          : rawGroup['api-key-entries'],
      }
    : undefined;
  const merged = mergeOpenAIProviderPayload(rawForMerge, payload);
  if (explicitSupportPromptCacheKey !== undefined) {
    merged['support-prompt-cache-key'] = explicitSupportPromptCacheKey;
    delete merged.supportPromptCacheKey;
    delete merged.support_prompt_cache_key;
  }
  const mergedKeys = Array.isArray(merged['api-key-entries']) ? merged['api-key-entries'] : [];
  delete merged['api-key-entries'];
  merged.keys = mergedKeys;
  return cleanV8GroupsForWrite('openai-compatibility', [merged])[0];
};

const getGeminiKeysV0 = async (): Promise<GeminiKeyConfig[]> => {
  const data = await apiClient.get('/gemini-api-key');
  const list = extractArrayPayload(data, 'gemini-api-key');
  return list.map((item) => normalizeGeminiKeyConfig(item)).filter(Boolean) as GeminiKeyConfig[];
};

const getInteractionsKeysV0 = async (): Promise<GeminiKeyConfig[]> => {
  const data = await apiClient.get('/interactions-api-key');
  const list = extractArrayPayload(data, 'interactions-api-key');
  return list.map((item) => normalizeGeminiKeyConfig(item)).filter(Boolean) as GeminiKeyConfig[];
};

const getCodexConfigsV0 = async (): Promise<ProviderKeyConfig[]> => {
  const data = await apiClient.get('/codex-api-key');
  const list = extractArrayPayload(data, 'codex-api-key');
  return list
    .map((item) => normalizeProviderKeyConfig(item))
    .filter(Boolean) as ProviderKeyConfig[];
};

const getXAIConfigsV0 = async (): Promise<ProviderKeyConfig[]> => {
  const data = await apiClient.get('/xai-api-key');
  const list = extractArrayPayload(data, 'xai-api-key');
  return list
    .map((item) => normalizeProviderKeyConfig(item))
    .filter(Boolean) as ProviderKeyConfig[];
};

const getClaudeConfigsV0 = async (): Promise<ProviderKeyConfig[]> => {
  const data = await apiClient.get('/claude-api-key');
  const list = extractArrayPayload(data, 'claude-api-key');
  return list
    .map((item) => normalizeProviderKeyConfig(item))
    .filter(Boolean) as ProviderKeyConfig[];
};

const getVertexConfigsV0 = async (): Promise<ProviderKeyConfig[]> => {
  const data = await apiClient.get('/vertex-api-key');
  const list = extractArrayPayload(data, 'vertex-api-key');
  return list
    .map((item) => normalizeProviderKeyConfig(item))
    .filter(Boolean) as ProviderKeyConfig[];
};

const getMetaConfigsV0 = async (): Promise<ProviderKeyConfig[]> => {
  try {
    const data = await apiClient.get('/meta-api-key');
    const list = extractArrayPayload(data, 'meta-api-key');
    return list
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  } catch (error) {
    if (isV8UnsupportedStatus(error)) {
      return [];
    }
    throw error;
  }
};

const getOpenAIProvidersV0 = async (): Promise<OpenAIProviderConfig[]> => {
  const data = await apiClient.get('/openai-compatibility');
  const list = extractArrayPayload(data, 'openai-compatibility');
  const providers = list
    .map((item) => normalizeOpenAIProvider(item))
    .filter(Boolean) as OpenAIProviderConfig[];
  return hydrateOpenAIProvidersDisableCooling(providers);
};


const isProviderV8GroupSource = (value: unknown): value is ProviderV8GroupSource =>
  isRecord(value) && typeof value.family === 'string' && typeof value.groupIndex === 'number';

const assertV8EtagMatches = (
  callerEtag: string | undefined,
  fetchedEtag: string | undefined
): void => {
  const normalizedCaller = normalizeEtagValue(callerEtag);
  const normalizedFetched = normalizeEtagValue(fetchedEtag);
  if (normalizedCaller && normalizedFetched && normalizedCaller !== normalizedFetched) {
    throw Object.assign(
      new Error('Configuration was modified by another session; please refresh and retry.'),
      {
        status: 412,
        details: { error: 'precondition_failed' },
      }
    );
  }
};

const resolveNativeDeleteArgs = (
  v8SourceOrOptions?: ProviderV8GroupSource | ProviderWriteOptions,
  trailingOptions?: ProviderWriteOptions
): { v8Source?: ProviderV8GroupSource; expectedEtag?: string } => {
  const trailingEtag = normalizeEtagValue(trailingOptions?.expectedEtag);
  if (isProviderV8GroupSource(v8SourceOrOptions)) {
    return {
      v8Source: v8SourceOrOptions,
      expectedEtag: trailingEtag ?? normalizeEtagValue(v8SourceOrOptions.etag),
    };
  }
  return {
    expectedEtag:
      trailingEtag ??
      (isRecord(v8SourceOrOptions) ? normalizeEtagValue(v8SourceOrOptions.expectedEtag) : undefined),
  };
};

const resolveOpenAIDeleteArgs = (
  indexOrSourceOrOptions?: number | ProviderV8GroupSource | ProviderWriteOptions,
  trailingOptions?: ProviderWriteOptions | ProviderV8GroupSource
): { hintIndex?: number; v8Source?: ProviderV8GroupSource; expectedEtag?: string } => {
  let hintIndex: number | undefined;
  let v8Source: ProviderV8GroupSource | undefined;
  let explicitEtag: string | undefined;

  if (typeof indexOrSourceOrOptions === 'number') {
    hintIndex = indexOrSourceOrOptions;
    if (isProviderV8GroupSource(trailingOptions)) {
      v8Source = trailingOptions;
    } else if (isRecord(trailingOptions)) {
      explicitEtag = normalizeEtagValue(trailingOptions.expectedEtag);
    }
  } else if (isProviderV8GroupSource(indexOrSourceOrOptions)) {
    v8Source = indexOrSourceOrOptions;
    if (isRecord(trailingOptions) && !isProviderV8GroupSource(trailingOptions)) {
      explicitEtag = normalizeEtagValue(trailingOptions.expectedEtag);
    }
  } else if (isRecord(indexOrSourceOrOptions)) {
    explicitEtag = normalizeEtagValue(indexOrSourceOrOptions.expectedEtag);
    if (isRecord(trailingOptions) && !isProviderV8GroupSource(trailingOptions)) {
      explicitEtag = normalizeEtagValue(trailingOptions.expectedEtag) ?? explicitEtag;
    }
  }

  return {
    hintIndex,
    v8Source,
    expectedEtag: explicitEtag ?? normalizeEtagValue(v8Source?.etag),
  };
};

const canUseV8Write = (): boolean =>
  typeof apiClient.getRawV8 === 'function' &&
  (typeof apiClient.putRawV8 === 'function' || typeof apiClient.putV8 === 'function');

const readErrorEtag = (error: unknown): string | undefined => {
  if (!isRecord(error)) return undefined;
  const direct = normalizeEtagValue(error.etag);
  if (direct) return direct;
  const fromHeaders = readResponseEtag(error.headers);
  if (fromHeaders) return fromHeaders;
  if (isRecord(error.response)) {
    return readResponseEtag(error.response.headers);
  }
  return undefined;
};

const isMockedApiFn = (fn: unknown): boolean =>
  typeof fn === 'function' && isRecord((fn as { mock?: unknown }).mock);

const buildV8PutRequestConfig = (
  scope: CapturedWriteScope,
  ifMatchEtag?: string
): ScopedApiRequestConfig | undefined => {
  const strongEtag = normalizeEtagValue(ifMatchEtag);
  if (!scope.v8Config && !strongEtag) {
    return undefined;
  }
  if (!scope.v8Config && strongEtag) {
    return { headers: { 'If-Match': strongEtag } };
  }
  const mergedHeaders: Record<string, unknown> = {
    ...(isRecord(scope.v8Config?.headers) ? scope.v8Config.headers : {}),
  };
  if (strongEtag) {
    mergedHeaders['If-Match'] = strongEtag;
  }
  return {
    ...scope.v8Config,
    headers: mergedHeaders as ScopedApiRequestConfig['headers'],
  };
};

const putV8Groups = async (
  family: ProviderV8Family,
  groups: Record<string, unknown>[],
  effectiveEtag?: string,
  scope: CapturedWriteScope = { epoch: readConnectionEpoch() }
): Promise<unknown> => {
  assertConnectionEpochUnchanged(scope.epoch);
  const url = `/config/api-keys/${family}`;
  const requestConfig = buildV8PutRequestConfig(scope, effectiveEtag);
  const preferRawPut =
    typeof apiClient.putRawV8 === 'function' &&
    (!isMockedApiFn(apiClient.putV8) || isMockedApiFn(apiClient.putRawV8));

  try {
    if (preferRawPut) {
      const response = await apiClient.putRawV8(url, groups, requestConfig);
      const nextEtag = recordLatestV8ConfigEtag(readResponseEtag(response?.headers), scope.epoch);
      const body = response?.data;
      if (nextEtag) {
        return isRecord(body) ? { ...body, etag: nextEtag } : { data: body, etag: nextEtag };
      }
      return body;
    }

    const result = await apiClient.putV8(url, groups, requestConfig);
    const fallbackEtag = recordLatestV8ConfigEtag(
      isRecord(result)
        ? (result.etag ?? (isRecord(result.headers) ? readResponseEtag(result.headers) : undefined))
        : undefined,
      scope.epoch
    );
    if (fallbackEtag && isRecord(result)) {
      return { ...result, etag: fallbackEtag };
    }
    return result;
  } catch (error) {
    recordLatestV8ConfigEtag(readErrorEtag(error), scope.epoch);
    throw error;
  }
};

const getNativeFamilyV8First = async <T extends GeminiKeyConfig | ProviderKeyConfig>(
  family: Exclude<ProviderV8Family, 'openai-compatibility'>,
  v0Fallback: () => Promise<T[]>
): Promise<T[]> => {
  const requestEpoch = readConnectionEpoch();
  const writeVersionBefore = v8EtagMutationVersion;
  if (typeof apiClient.getRawV8 !== 'function') {
    clearLatestV8ConfigEtagForEpoch(requestEpoch, { passiveRead: true, writeVersionBefore });
    return v0Fallback();
  }
  try {
    const response = await apiClient.getRawV8(`/config/api-keys/${family}`);
    const etag = recordPassiveReadLatestV8ConfigEtag(
      readResponseEtag(response.headers),
      requestEpoch,
      writeVersionBefore
    );
    return normalizeV8ProviderGroups(response.data, family, etag) as T[];
  } catch (error) {
    if (isV8MissingConfigPathError(error)) {
      recordPassiveReadLatestV8ConfigEtag(readErrorEtag(error), requestEpoch, writeVersionBefore);
      return [];
    }
    if (isV8UnsupportedStatus(error)) {
      clearLatestV8ConfigEtagForEpoch(requestEpoch, { passiveRead: true, writeVersionBefore });
      return v0Fallback();
    }
    throw error;
  }
};

const saveNativeFamilyV8First = async <T extends GeminiKeyConfig | ProviderKeyConfig>(params: {
  family: Exclude<ProviderV8Family, 'openai-compatibility'>;
  section: string;
  configs: T[];
  serialize: (item: T) => Record<string, unknown>;
  updateSerialize?: (original: T, item: T) => Record<string, unknown>;
  knownFields: readonly string[];
  options?: ProviderWriteOptions;
}): Promise<unknown> => {
  const { family, section, configs, serialize, updateSerialize, knownFields, options } = params;
  // Validate synchronously up front before issuing any network request.
  configs.forEach((config) => {
    serialize(config);
  });
  const callerEtag = normalizeEtagValue(
    options?.expectedEtag ??
      configs.find((item) => normalizeEtagValue(item?.v8Source?.etag))?.v8Source?.etag
  );

  const writeScope = captureWriteScope();

  return enqueueProviderSectionWrite(section, async () => {
    assertConnectionEpochUnchanged(writeScope.epoch);
    const runV0Fallback = async () => {
      assertConnectionEpochUnchanged(writeScope.epoch);
      clearLatestV8ConfigEtagForEpoch(writeScope.epoch);
      const preservedList = await buildPreservedList(
        section,
        configs,
        serialize,
        (raw, payload) => mergeProviderKeyPayload(raw, payload, knownFields),
        providerKeyIdentity,
        writeScope
      );
      return putScopedV0(`/${section}`, preservedList, writeScope);
    };

    if (!canUseV8Write()) {
      return runV0Fallback();
    }

    let existingGroups: Record<string, unknown>[] = [];
    let etag: string | undefined;
    let missingConfigPath = false;
    const writeVersionBefore = v8EtagMutationVersion;
    try {
      const response = await getScopedRawV8(`/config/api-keys/${family}`, writeScope);
      existingGroups = extractRawV8GroupsArray(response.data, family);
      etag = recordPassiveReadLatestV8ConfigEtag(
        readResponseEtag(response.headers),
        writeScope.epoch,
        writeVersionBefore
      );
    } catch (error) {
      if (isV8MissingConfigPathError(error)) {
        existingGroups = [];
        etag = recordPassiveReadLatestV8ConfigEtag(
          readErrorEtag(error),
          writeScope.epoch,
          writeVersionBefore
        );
        missingConfigPath = true;
      } else if (isV8UnsupportedStatus(error)) {
        return runV0Fallback();
      } else {
        throw error;
      }
    }

    const v8ConfirmedByGet = !missingConfigPath;
    assertV8EtagMatches(callerEtag, etag);
    const effectiveEtag = callerEtag ?? etag;
    const usedSlots = new Set<string>();
    const matchedItems = configs.map((config) => ({
      config,
      located: locateV8NativeMember<T>(family, existingGroups, config, usedSlots),
    }));

    if (matchedItems.some((item) => item.located === null && Boolean(item.config.apiKey?.trim()))) {
      const normalizedExisting = normalizeV8ProviderGroups(existingGroups, family) as T[];
      const slotKey = (gIdx: number, kIdx: number) => `${gIdx}:${kIdx}`;
      matchedItems.forEach((item) => {
        if (item.located !== null) return;
        const targetApiKey = (item.config.apiKey ?? '').trim();
        if (!targetApiKey) return;
        const targetName = (item.config.name ?? '').trim();
        const candidates = normalizedExisting.filter((entry) => {
          const gIdx = entry.v8Source?.groupIndex;
          const kIdx = entry.v8Source?.keyIndex;
          if (gIdx === undefined || kIdx === undefined || usedSlots.has(slotKey(gIdx, kIdx))) {
            return false;
          }
          return (entry.apiKey ?? '').trim() === targetApiKey;
        });
        const sameNameCandidates = targetName
          ? candidates.filter((entry) => (entry.name ?? '').trim() === targetName)
          : [];
        const fallbackMatch =
          sameNameCandidates.length === 1
            ? sameNameCandidates[0]
            : !targetName && candidates.length === 1
              ? candidates[0]
              : undefined;
        if (fallbackMatch?.v8Source && fallbackMatch.v8Source.keyIndex !== undefined) {
          const { groupIndex, keyIndex } = fallbackMatch.v8Source;
          const group = existingGroups[groupIndex];
          const rawKey = Array.isArray(group?.keys) ? group.keys[keyIndex] : undefined;
          if (isRecord(group) && isRecord(rawKey)) {
            usedSlots.add(slotKey(groupIndex, keyIndex));
            item.located = {
              groupIndex,
              keyIndex,
              group,
              rawKey,
              normalized: fallbackMatch,
            };
          }
        }
      });
    }

    const stayingMembersByGroupIndex = new Map<
      number,
      Array<{ itemIndex: number; keyIndex: number; config: T; located: LocatedV8NativeMember<T> }>
    >();
    matchedItems.forEach(({ config, located }, itemIndex) => {
      if (!located || !Array.isArray(located.group.keys) || located.group.keys.length <= 1) {
        return;
      }
      const originalGroupName =
        typeof located.group.name === 'string' ? located.group.name.trim() : '';
      const originalBaseUrl = (located.normalized.baseUrl ?? '').trim();
      const desiredName = config.name?.trim() ?? '';
      if (
        Boolean(desiredName) &&
        desiredName === originalGroupName &&
        (config.baseUrl ?? '').trim() === originalBaseUrl
      ) {
        const bucket = stayingMembersByGroupIndex.get(located.groupIndex) ?? [];
        bucket.push({ itemIndex, keyIndex: located.keyIndex, config, located });
        stayingMembersByGroupIndex.set(located.groupIndex, bucket);
      }
    });

    const usedNames = new Set<string>();
    const preReservedNamesByGroupIndex = new Map<number, string>();
    stayingMembersByGroupIndex.forEach((members, groupIndex) => {
      if (members.length === 0) return;
      const originalGroupName =
        typeof members[0].located.group.name === 'string'
          ? members[0].located.group.name.trim()
          : '';
      if (originalGroupName && !usedNames.has(originalGroupName)) {
        usedNames.add(originalGroupName);
        preReservedNamesByGroupIndex.set(groupIndex, originalGroupName);
      }
    });

    const nextGroups: Record<string, unknown>[] = [];
    const emittedItemIndices = new Set<number>();

    for (let idx = 0; idx < matchedItems.length; idx += 1) {
      if (emittedItemIndices.has(idx)) continue;
      const { config, located } = matchedItems[idx];
      if (located) {
        const groupMembers = stayingMembersByGroupIndex.get(located.groupIndex);
        if (groupMembers && groupMembers.length > 1 && groupMembers[0].itemIndex === idx) {
          groupMembers.forEach((member) => {
            emittedItemIndices.add(member.itemIndex);
          });
          const originalGroupName =
            typeof located.group.name === 'string' ? located.group.name.trim() : '';
          const groupName =
            preReservedNamesByGroupIndex.get(located.groupIndex) ??
            resolveV8GroupName(originalGroupName, usedNames, family);
          const nextKeys = groupMembers.map((member) =>
            updateMultiKeyV8NativeMemberInPlace({
              rawGroup: located.group,
              rawKey: member.located.rawKey,
              original: member.located.normalized,
              value: member.config,
              serialize,
              updatePayload: updateSerialize
                ? updateSerialize(member.located.normalized, member.config)
                : serialize(member.config),
              knownFields,
            })
          );
          nextGroups.push({
            ...located.group,
            name: groupName,
            keys: nextKeys,
          });
          continue;
        }
      }

      const soleStayingMember =
        located &&
        stayingMembersByGroupIndex.get(located.groupIndex)?.length === 1 &&
        stayingMembersByGroupIndex.get(located.groupIndex)?.[0].itemIndex === idx;
      const groupName =
        soleStayingMember && located && preReservedNamesByGroupIndex.has(located.groupIndex)
          ? preReservedNamesByGroupIndex.get(located.groupIndex)!
          : resolveV8GroupName(config.name, usedNames, family);
      const payload =
        located && updateSerialize
          ? updateSerialize(located.normalized, config)
          : serialize(config);
      nextGroups.push(
        buildSingleKeyV8NativeGroup({
          groupName,
          payload,
          knownFields,
          rawGroup: located?.group,
          rawKey: located?.rawKey,
          keepSharedOnGroup: Boolean(
            located && Array.isArray(located.group.keys) && located.group.keys.length > 1
          ),
        })
      );
    }

    const cleaned = cleanV8GroupsForWrite(family, nextGroups);
    try {
      return await putV8Groups(family, cleaned, effectiveEtag, writeScope);
    } catch (putError) {
      if (!v8ConfirmedByGet && isV8UnsupportedStatus(putError)) {
        return runV0Fallback();
      }
      throw putError;
    }
  });
};

const createNativeFamilyV8First = async <T extends GeminiKeyConfig | ProviderKeyConfig>(params: {
  family: Exclude<ProviderV8Family, 'openai-compatibility'>;
  section: string;
  config: T;
  serialize: (item: T) => Record<string, unknown>;
  knownFields: readonly string[];
  options?: ProviderWriteOptions;
}): Promise<unknown> => {
  const { family, section, config, serialize, knownFields, options } = params;
  const serializedPayload = serialize(config);
  const callerEtag = normalizeEtagValue(options?.expectedEtag ?? config.v8Source?.etag);

  const writeScope = captureWriteScope();

  return enqueueProviderSectionWrite(section, async () => {
    assertConnectionEpochUnchanged(writeScope.epoch);
    const runV0Fallback = async () => {
      assertConnectionEpochUnchanged(writeScope.epoch);
      clearLatestV8ConfigEtagForEpoch(writeScope.epoch);
      await runProviderListMutationV0(
        section,
        (latestItems) =>
          appendLatestProviderRecord(latestItems, serializedPayload, (raw, payload) =>
            mergeProviderKeyPayload(raw, payload, knownFields)
          ),
        writeScope
      );
      return undefined;
    };

    if (!canUseV8Write()) {
      return runV0Fallback();
    }

    let existingGroups: Record<string, unknown>[] = [];
    let etag: string | undefined;
    let missingConfigPath = false;
    const writeVersionBefore = v8EtagMutationVersion;
    try {
      const response = await getScopedRawV8(`/config/api-keys/${family}`, writeScope);
      existingGroups = extractRawV8GroupsArray(response.data, family);
      etag = recordPassiveReadLatestV8ConfigEtag(
        readResponseEtag(response.headers),
        writeScope.epoch,
        writeVersionBefore
      );
    } catch (error) {
      if (isV8MissingConfigPathError(error)) {
        existingGroups = [];
        etag = recordPassiveReadLatestV8ConfigEtag(
          readErrorEtag(error),
          writeScope.epoch,
          writeVersionBefore
        );
        missingConfigPath = true;
      } else if (isV8UnsupportedStatus(error)) {
        return runV0Fallback();
      } else {
        throw error;
      }
    }

    const v8ConfirmedByGet = !missingConfigPath;
    assertV8EtagMatches(callerEtag, etag);
    const effectiveEtag = callerEtag ?? etag;
    const usedNames = collectUsedGroupNames(existingGroups);
    const groupName = resolveV8GroupName(config.name, usedNames, family);
    const newGroup = buildSingleKeyV8NativeGroup({
      groupName,
      payload: { ...serializedPayload },
      knownFields,
    });
    const cleaned = cleanV8GroupsForWrite(family, [...existingGroups, newGroup]);

    try {
      return await putV8Groups(family, cleaned, effectiveEtag, writeScope);
    } catch (putError) {
      if (!v8ConfirmedByGet && isV8UnsupportedStatus(putError)) {
        return runV0Fallback();
      }
      throw putError;
    }
  });
};

const updateNativeFamilyV8First = async <T extends GeminiKeyConfig | ProviderKeyConfig>(params: {
  family: Exclude<ProviderV8Family, 'openai-compatibility'>;
  section: string;
  original: T;
  value: T;
  serialize: (item: T) => Record<string, unknown>;
  updateSerialize?: (original: T, item: T) => Record<string, unknown>;
  knownFields: readonly string[];
  options?: ProviderWriteOptions;
}): Promise<unknown> => {
  const { family, section, original, value, serialize, updateSerialize, knownFields, options } =
    params;
  const updatePayload = updateSerialize ? updateSerialize(original, value) : serialize(value);
  const callerEtag = normalizeEtagValue(
    options?.expectedEtag ?? original.v8Source?.etag ?? value.v8Source?.etag
  );

  const writeScope = captureWriteScope();

  return enqueueProviderSectionWrite(section, async () => {
    assertConnectionEpochUnchanged(writeScope.epoch);
    const runV0Fallback = async () => {
      assertConnectionEpochUnchanged(writeScope.epoch);
      clearLatestV8ConfigEtagForEpoch(writeScope.epoch);
      await runProviderListMutationV0(
        section,
        (latestItems) =>
          replaceLatestProviderRecord(
            latestItems,
            (record) => matchesProviderConfig(record, original),
            updatePayload,
            (raw, payload) => mergeProviderKeyPayload(raw, payload, knownFields)
          ),
        writeScope
      );
      return undefined;
    };

    if (!canUseV8Write()) {
      return runV0Fallback();
    }

    let existingGroups: Record<string, unknown>[] = [];
    let etag: string | undefined;
    const writeVersionBefore = v8EtagMutationVersion;
    try {
      const response = await getScopedRawV8(`/config/api-keys/${family}`, writeScope);
      existingGroups = extractRawV8GroupsArray(response.data, family);
      etag = recordPassiveReadLatestV8ConfigEtag(
        readResponseEtag(response.headers),
        writeScope.epoch,
        writeVersionBefore
      );
    } catch (error) {
      if (isV8MissingConfigPathError(error) && (original.v8Source || value.v8Source)) {
        recordPassiveReadLatestV8ConfigEtag(
          readErrorEtag(error),
          writeScope.epoch,
          writeVersionBefore
        );
        throw new Error('Provider configuration changed; refresh and try again.');
      }
      if (isV8UnsupportedStatus(error)) {
        return runV0Fallback();
      }
      throw error;
    }

    const v8ConfirmedByGet = true;
    assertV8EtagMatches(callerEtag, etag);
    const effectiveEtag = callerEtag ?? etag;
    const located = locateV8NativeMember<T>(family, existingGroups, {
      apiKey: original.apiKey,
      baseUrl: original.baseUrl,
      authIndex: original.authIndex,
      v8Source: value.v8Source ?? original.v8Source,
    });
    if (!located) {
      throw new Error('Provider configuration changed; refresh and try again.');
    }

    const rawKeys = Array.isArray(located.group.keys) ? located.group.keys.filter(isRecord) : [];
    let nextGroups: Record<string, unknown>[];

    if (rawKeys.length <= 1) {
      const usedNames = collectUsedGroupNames(existingGroups, located.groupIndex);
      const groupName = resolveV8GroupName(value.name, usedNames, family);
      const updatedGroup = buildSingleKeyV8NativeGroup({
        groupName,
        payload: { ...updatePayload },
        knownFields,
        rawGroup: located.group,
        rawKey: located.rawKey,
      });
      nextGroups = existingGroups.map((group, idx) =>
        idx === located.groupIndex ? updatedGroup : group
      );
    } else {
      const originalGroupName =
        typeof located.group.name === 'string' ? located.group.name.trim() : '';
      const originalBaseUrl = (located.normalized.baseUrl ?? '').trim();
      const desiredTrimmedName = value.name?.trim() ?? '';
      const nameChanged = !desiredTrimmedName || desiredTrimmedName !== originalGroupName;
      const baseUrlChanged = (value.baseUrl ?? '').trim() !== originalBaseUrl;

      if (nameChanged || baseUrlChanged) {
        const remainingKeys = rawKeys.filter((_, idx) => idx !== located.keyIndex);
        const trimmedOriginalGroup = {
          ...located.group,
          keys: remainingKeys,
        };
        const groupsWithTrimmedOriginal = existingGroups.map((group, idx) =>
          idx === located.groupIndex ? trimmedOriginalGroup : group
        );
        const usedNames = collectUsedGroupNames(groupsWithTrimmedOriginal);
        const splitGroupName = resolveV8GroupName(value.name, usedNames, family);
        const splitGroup = buildSingleKeyV8NativeGroup({
          groupName: splitGroupName,
          payload: { ...updatePayload },
          knownFields,
          rawGroup: located.group,
          rawKey: located.rawKey,
          keepSharedOnGroup: true,
        });
        nextGroups = [...groupsWithTrimmedOriginal, splitGroup];
      } else {
        const updatedKey = updateMultiKeyV8NativeMemberInPlace({
          rawGroup: located.group,
          rawKey: located.rawKey,
          original: located.normalized,
          value,
          serialize,
          updatePayload: { ...updatePayload },
          knownFields,
        });
        nextGroups = existingGroups.map((group, idx) =>
          idx === located.groupIndex
            ? {
                ...group,
                keys: rawKeys.map((member, memberIdx) =>
                  memberIdx === located.keyIndex ? updatedKey : member
                ),
              }
            : group
        );
      }
    }

    const cleaned = cleanV8GroupsForWrite(family, nextGroups);
    try {
      return await putV8Groups(family, cleaned, effectiveEtag, writeScope);
    } catch (putError) {
      if (!v8ConfirmedByGet && isV8UnsupportedStatus(putError)) {
        return runV0Fallback();
      }
      throw putError;
    }
  });
};

const deleteNativeFamilyV8First = async (
  family: Exclude<ProviderV8Family, 'openai-compatibility'>,
  section: string,
  apiKey: string,
  baseUrl?: string,
  v8SourceOrOptions?: ProviderV8GroupSource | ProviderWriteOptions,
  trailingOptions?: ProviderWriteOptions
): Promise<unknown> => {
  const { v8Source, expectedEtag: callerEtag } = resolveNativeDeleteArgs(
    v8SourceOrOptions,
    trailingOptions
  );
  const writeScope = captureWriteScope();
  return enqueueProviderSectionWrite(section, async () => {
    assertConnectionEpochUnchanged(writeScope.epoch);
    const runV0Fallback = () => {
      assertConnectionEpochUnchanged(writeScope.epoch);
      clearLatestV8ConfigEtagForEpoch(writeScope.epoch);
      return deleteScopedV0(`/${section}${buildProviderDeleteQuery(apiKey, baseUrl)}`, writeScope);
    };

    if (!canUseV8Write()) {
      return runV0Fallback();
    }

    let existingGroups: Record<string, unknown>[] = [];
    let etag: string | undefined;
    const writeVersionBefore = v8EtagMutationVersion;
    try {
      const response = await getScopedRawV8(`/config/api-keys/${family}`, writeScope);
      existingGroups = extractRawV8GroupsArray(response.data, family);
      etag = recordPassiveReadLatestV8ConfigEtag(
        readResponseEtag(response.headers),
        writeScope.epoch,
        writeVersionBefore
      );
    } catch (error) {
      if (isV8MissingConfigPathError(error)) {
        recordPassiveReadLatestV8ConfigEtag(
          readErrorEtag(error),
          writeScope.epoch,
          writeVersionBefore
        );
        return undefined;
      }
      if (isV8UnsupportedStatus(error)) {
        return runV0Fallback();
      }
      throw error;
    }

    const v8ConfirmedByGet = true;
    assertV8EtagMatches(callerEtag, etag);
    const effectiveEtag = callerEtag ?? etag;

    const located = locateV8NativeMember(family, existingGroups, {
      apiKey,
      baseUrl,
      v8Source,
    });
    if (!located) {
      return undefined;
    }

    const rawKeys = Array.isArray(located.group.keys) ? located.group.keys.filter(isRecord) : [];
    const remainingKeys = rawKeys.filter((_, idx) => idx !== located.keyIndex);
    const nextGroups =
      remainingKeys.length > 0
        ? existingGroups.map((group, idx) =>
            idx === located.groupIndex ? { ...group, keys: remainingKeys } : group
          )
        : existingGroups.filter((_, idx) => idx !== located.groupIndex);

    const cleaned = cleanV8GroupsForWrite(family, nextGroups);
    try {
      return await putV8Groups(family, cleaned, effectiveEtag, writeScope);
    } catch (putError) {
      if (!v8ConfirmedByGet && isV8UnsupportedStatus(putError)) {
        return runV0Fallback();
      }
      throw putError;
    }
  });
};

const tolerateOptionalV0Family = <T>(promise: Promise<T[]>): Promise<T[]> =>
  promise.catch((error: unknown) => {
    if (isV8UnsupportedStatus(error)) {
      return [];
    }
    throw error;
  });

export const loadAllProvidersV8Snapshot = async (): Promise<ProvidersV8Snapshot> => {
  const requestEpoch = readConnectionEpoch();
  const writeVersionBefore = v8EtagMutationVersion;
  const loadV0Snapshot = async (): Promise<ProvidersV8Snapshot> => {
    clearLatestV8ConfigEtagForEpoch(requestEpoch, { passiveRead: true, writeVersionBefore });
    const [gemini, interactions, codex, xai, claude, vertex, meta, openai] = await Promise.all([
      getGeminiKeysV0(),
      tolerateOptionalV0Family(getInteractionsKeysV0()),
      getCodexConfigsV0(),
      tolerateOptionalV0Family(getXAIConfigsV0()),
      getClaudeConfigsV0(),
      tolerateOptionalV0Family(getVertexConfigsV0()),
      getMetaConfigsV0(),
      getOpenAIProvidersV0(),
    ]);
    return {
      gemini,
      interactions,
      codex,
      xai,
      claude,
      vertex,
      meta,
      openai,
      v8Supported: false,
    };
  };

  if (typeof apiClient.getRawV8 !== 'function') {
    return loadV0Snapshot();
  }

  try {
    const response = await apiClient.getRawV8('/config/api-keys');
    const rawEtag = readResponseEtag(response.headers);
    const etag = rawEtag
      ? recordPassiveReadLatestV8ConfigEtag(rawEtag, requestEpoch, writeVersionBefore)
      : setLatestV8ConfigEtagForEpoch(undefined, requestEpoch, {
          passiveRead: true,
          writeVersionBefore,
        });
    return {
      gemini: normalizeV8ProviderGroups(response.data, 'gemini', etag),
      interactions: normalizeV8ProviderGroups(response.data, 'interactions', etag),
      codex: normalizeV8ProviderGroups(response.data, 'codex', etag),
      xai: normalizeV8ProviderGroups(response.data, 'xai', etag),
      claude: normalizeV8ProviderGroups(response.data, 'claude', etag),
      vertex: normalizeV8ProviderGroups(response.data, 'vertex', etag),
      meta: normalizeV8ProviderGroups(response.data, 'meta', etag),
      openai: normalizeV8ProviderGroups(response.data, 'openai-compatibility', etag),
      v8Supported: true,
    };
  } catch (error) {
    if (isV8MissingConfigPathError(error)) {
      const errorEtag = readErrorEtag(error);
      if (errorEtag) {
        recordPassiveReadLatestV8ConfigEtag(errorEtag, requestEpoch, writeVersionBefore);
      } else {
        setLatestV8ConfigEtagForEpoch(undefined, requestEpoch, {
          passiveRead: true,
          writeVersionBefore,
        });
      }
      return {
        gemini: [],
        interactions: [],
        codex: [],
        xai: [],
        claude: [],
        vertex: [],
        meta: [],
        openai: [],
        v8Supported: true,
      };
    }
    if (isV8UnsupportedStatus(error)) {
      return loadV0Snapshot();
    }
    throw error;
  }
};

export const providersApi = {
  getLatestV8ConfigEtag: (): string | undefined =>
    latestV8ConfigEtagEpoch === readConnectionEpoch() ? latestV8ConfigEtag : undefined,

  getV8EtagMutationVersion: (): number => v8EtagMutationVersion,

  setLatestV8ConfigEtag: (etag?: string, options?: { passiveRead?: boolean }): void => {
    setLatestV8ConfigEtagForEpoch(etag, readConnectionEpoch(), options);
  },
  loadAllProvidersV8Snapshot,

  getGeminiKeys: (): Promise<GeminiKeyConfig[]> =>
    getNativeFamilyV8First('gemini', getGeminiKeysV0),

  saveGeminiKeys: (configs: GeminiKeyConfig[], options?: ProviderWriteOptions) =>
    saveNativeFamilyV8First({
      family: 'gemini',
      section: 'gemini-api-key',
      configs,
      serialize: serializeGeminiKey,
      knownFields: GEMINI_KEY_FIELDS,
      options,
    }),

  createGeminiKey: (config: GeminiKeyConfig, options?: ProviderWriteOptions) =>
    createNativeFamilyV8First({
      family: 'gemini',
      section: 'gemini-api-key',
      config,
      serialize: serializeGeminiKey,
      knownFields: GEMINI_KEY_FIELDS,
      options,
    }),

  updateGeminiKey: (
    original: GeminiKeyConfig,
    value: GeminiKeyConfig,
    options?: ProviderWriteOptions
  ) =>
    updateNativeFamilyV8First({
      family: 'gemini',
      section: 'gemini-api-key',
      original,
      value,
      serialize: serializeGeminiKey,
      knownFields: GEMINI_KEY_FIELDS,
      options,
    }),

  deleteGeminiKey: (
    apiKey: string,
    baseUrl?: string,
    v8Source?: ProviderV8GroupSource | ProviderWriteOptions,
    options?: ProviderWriteOptions
  ) => deleteNativeFamilyV8First('gemini', 'gemini-api-key', apiKey, baseUrl, v8Source, options),

  getInteractionsKeys: (): Promise<GeminiKeyConfig[]> =>
    getNativeFamilyV8First('interactions', getInteractionsKeysV0),

  saveInteractionsKeys: (configs: GeminiKeyConfig[], options?: ProviderWriteOptions) =>
    saveNativeFamilyV8First({
      family: 'interactions',
      section: 'interactions-api-key',
      configs,
      serialize: serializeGeminiKey,
      knownFields: GEMINI_KEY_FIELDS,
      options,
    }),

  createInteractionsKey: (config: GeminiKeyConfig, options?: ProviderWriteOptions) =>
    createNativeFamilyV8First({
      family: 'interactions',
      section: 'interactions-api-key',
      config,
      serialize: serializeGeminiKey,
      knownFields: GEMINI_KEY_FIELDS,
      options,
    }),

  updateInteractionsKey: (
    original: GeminiKeyConfig,
    value: GeminiKeyConfig,
    options?: ProviderWriteOptions
  ) =>
    updateNativeFamilyV8First({
      family: 'interactions',
      section: 'interactions-api-key',
      original,
      value,
      serialize: serializeGeminiKey,
      knownFields: GEMINI_KEY_FIELDS,
      options,
    }),

  deleteInteractionsKey: (
    apiKey: string,
    baseUrl?: string,
    v8Source?: ProviderV8GroupSource | ProviderWriteOptions,
    options?: ProviderWriteOptions
  ) =>
    deleteNativeFamilyV8First(
      'interactions',
      'interactions-api-key',
      apiKey,
      baseUrl,
      v8Source,
      options
    ),

  getCodexConfigs: (): Promise<ProviderKeyConfig[]> =>
    getNativeFamilyV8First('codex', getCodexConfigsV0),

  saveCodexConfigs: (configs: ProviderKeyConfig[], options?: ProviderWriteOptions) =>
    saveNativeFamilyV8First({
      family: 'codex',
      section: 'codex-api-key',
      configs,
      serialize: serializeCodexKey,
      knownFields: CODEX_KEY_FIELDS,
      options,
    }),

  createCodexConfig: (config: ProviderKeyConfig, options?: ProviderWriteOptions) =>
    createNativeFamilyV8First({
      family: 'codex',
      section: 'codex-api-key',
      config,
      serialize: serializeCodexKey,
      knownFields: CODEX_KEY_FIELDS,
      options,
    }),

  updateCodexConfig: (
    original: ProviderKeyConfig,
    value: ProviderKeyConfig,
    options?: ProviderWriteOptions
  ) =>
    updateNativeFamilyV8First({
      family: 'codex',
      section: 'codex-api-key',
      original,
      value,
      serialize: serializeCodexKey,
      knownFields: CODEX_KEY_FIELDS,
      options,
    }),

  deleteCodexConfig: (
    apiKey: string,
    baseUrl?: string,
    v8Source?: ProviderV8GroupSource | ProviderWriteOptions,
    options?: ProviderWriteOptions
  ) => deleteNativeFamilyV8First('codex', 'codex-api-key', apiKey, baseUrl, v8Source, options),

  getXAIConfigs: (): Promise<ProviderKeyConfig[]> =>
    getNativeFamilyV8First('xai', getXAIConfigsV0),

  saveXAIConfigs: (configs: ProviderKeyConfig[], options?: ProviderWriteOptions) =>
    saveNativeFamilyV8First({
      family: 'xai',
      section: 'xai-api-key',
      configs,
      serialize: serializeCodexKey,
      knownFields: XAI_KEY_FIELDS,
      options,
    }),

  createXAIConfig: (config: ProviderKeyConfig, options?: ProviderWriteOptions) =>
    createNativeFamilyV8First({
      family: 'xai',
      section: 'xai-api-key',
      config,
      serialize: serializeCodexKey,
      knownFields: XAI_KEY_FIELDS,
      options,
    }),

  updateXAIConfig: (
    original: ProviderKeyConfig,
    value: ProviderKeyConfig,
    options?: ProviderWriteOptions
  ) =>
    updateNativeFamilyV8First({
      family: 'xai',
      section: 'xai-api-key',
      original,
      value,
      serialize: serializeCodexKey,
      knownFields: XAI_KEY_FIELDS,
      options,
    }),

  deleteXAIConfig: (
    apiKey: string,
    baseUrl?: string,
    v8Source?: ProviderV8GroupSource | ProviderWriteOptions,
    options?: ProviderWriteOptions
  ) => deleteNativeFamilyV8First('xai', 'xai-api-key', apiKey, baseUrl, v8Source, options),

  getClaudeConfigs: (): Promise<ProviderKeyConfig[]> =>
    getNativeFamilyV8First('claude', getClaudeConfigsV0),

  saveClaudeConfigs: (configs: ProviderKeyConfig[], options?: ProviderWriteOptions) =>
    saveNativeFamilyV8First({
      family: 'claude',
      section: 'claude-api-key',
      configs,
      serialize: serializeClaudeKey,
      updateSerialize: serializeClaudeUpdatePayload,
      knownFields: CLAUDE_KEY_FIELDS,
      options,
    }),

  createClaudeConfig: (config: ProviderKeyConfig, options?: ProviderWriteOptions) =>
    createNativeFamilyV8First({
      family: 'claude',
      section: 'claude-api-key',
      config,
      serialize: serializeClaudeKey,
      knownFields: CLAUDE_KEY_FIELDS,
      options,
    }),

  updateClaudeConfig: (
    original: ProviderKeyConfig,
    value: ProviderKeyConfig,
    options?: ProviderWriteOptions
  ) =>
    updateNativeFamilyV8First({
      family: 'claude',
      section: 'claude-api-key',
      original,
      value,
      serialize: serializeClaudeKey,
      updateSerialize: serializeClaudeUpdatePayload,
      knownFields: CLAUDE_KEY_FIELDS,
      options,
    }),

  deleteClaudeConfig: (
    apiKey: string,
    baseUrl?: string,
    v8Source?: ProviderV8GroupSource | ProviderWriteOptions,
    options?: ProviderWriteOptions
  ) => deleteNativeFamilyV8First('claude', 'claude-api-key', apiKey, baseUrl, v8Source, options),

  getVertexConfigs: (): Promise<ProviderKeyConfig[]> =>
    getNativeFamilyV8First('vertex', getVertexConfigsV0),

  saveVertexConfigs: (configs: ProviderKeyConfig[], options?: ProviderWriteOptions) =>
    saveNativeFamilyV8First({
      family: 'vertex',
      section: 'vertex-api-key',
      configs,
      serialize: serializeVertexKey,
      knownFields: VERTEX_KEY_FIELDS,
      options,
    }),

  createVertexConfig: (config: ProviderKeyConfig, options?: ProviderWriteOptions) =>
    createNativeFamilyV8First({
      family: 'vertex',
      section: 'vertex-api-key',
      config,
      serialize: serializeVertexKey,
      knownFields: VERTEX_KEY_FIELDS,
      options,
    }),

  updateVertexConfig: (
    original: ProviderKeyConfig,
    value: ProviderKeyConfig,
    options?: ProviderWriteOptions
  ) =>
    updateNativeFamilyV8First({
      family: 'vertex',
      section: 'vertex-api-key',
      original,
      value,
      serialize: serializeVertexKey,
      knownFields: VERTEX_KEY_FIELDS,
      options,
    }),

  deleteVertexConfig: (
    apiKey: string,
    baseUrl?: string,
    v8Source?: ProviderV8GroupSource | ProviderWriteOptions,
    options?: ProviderWriteOptions
  ) => deleteNativeFamilyV8First('vertex', 'vertex-api-key', apiKey, baseUrl, v8Source, options),

  getMetaConfigs: (): Promise<ProviderKeyConfig[]> =>
    getNativeFamilyV8First('meta', getMetaConfigsV0),

  async getOpenAIProviders(): Promise<OpenAIProviderConfig[]> {
    const requestEpoch = readConnectionEpoch();
    const writeVersionBefore = v8EtagMutationVersion;
    if (typeof apiClient.getRawV8 !== 'function') {
      clearLatestV8ConfigEtagForEpoch(requestEpoch, { passiveRead: true, writeVersionBefore });
      return getOpenAIProvidersV0();
    }
    try {
      const response = await apiClient.getRawV8('/config/api-keys/openai-compatibility');
      const etag = recordPassiveReadLatestV8ConfigEtag(
        readResponseEtag(response.headers),
        requestEpoch,
        writeVersionBefore
      );
      return normalizeV8ProviderGroups(response.data, 'openai-compatibility', etag);
    } catch (error) {
      if (isV8MissingConfigPathError(error)) {
        recordPassiveReadLatestV8ConfigEtag(
          readErrorEtag(error),
          requestEpoch,
          writeVersionBefore
        );
        return [];
      }
      if (isV8UnsupportedStatus(error)) {
        clearLatestV8ConfigEtagForEpoch(requestEpoch, { passiveRead: true, writeVersionBefore });
        return getOpenAIProvidersV0();
      }
      throw error;
    }
  },

  saveOpenAIProviders: (providers: OpenAIProviderConfig[], options?: ProviderWriteOptions) => {
    const callerEtag = normalizeEtagValue(
      options?.expectedEtag ??
        providers.find((item) => normalizeEtagValue(item?.v8Source?.etag))?.v8Source?.etag
    );
    const writeScope = captureWriteScope();
    return enqueueProviderSectionWrite('openai-compatibility', async () => {
      assertConnectionEpochUnchanged(writeScope.epoch);
      const runV0Fallback = async () => {
        assertConnectionEpochUnchanged(writeScope.epoch);
        clearLatestV8ConfigEtagForEpoch(writeScope.epoch);
        const preservedList = await buildPreservedList(
          'openai-compatibility',
          providers,
          serializeOpenAIProvider,
          mergeOpenAIProviderPayload,
          openAIProviderIdentity,
          writeScope
        );
        return putScopedV0('/openai-compatibility', preservedList, writeScope);
      };

      if (!canUseV8Write()) {
        return runV0Fallback();
      }

      let existingGroups: Record<string, unknown>[] = [];
      let etag: string | undefined;
      let missingConfigPath = false;
      const writeVersionBefore = v8EtagMutationVersion;
      try {
        const response = await getScopedRawV8('/config/api-keys/openai-compatibility', writeScope);
        existingGroups = extractRawV8GroupsArray(response.data, 'openai-compatibility');
        etag = recordPassiveReadLatestV8ConfigEtag(
          readResponseEtag(response.headers),
          writeScope.epoch,
          writeVersionBefore
        );
      } catch (error) {
        if (isV8MissingConfigPathError(error)) {
          existingGroups = [];
          etag = recordPassiveReadLatestV8ConfigEtag(
            readErrorEtag(error),
            writeScope.epoch,
            writeVersionBefore
          );
          missingConfigPath = true;
        } else if (isV8UnsupportedStatus(error)) {
          return runV0Fallback();
        } else {
          throw error;
        }
      }

      const v8ConfirmedByGet = !missingConfigPath;
      assertV8EtagMatches(callerEtag, etag);
      const effectiveEtag = callerEtag ?? etag;
      const rawRecords: Array<Record<string, unknown> | undefined> = existingGroups;
      const usedIndexes = new Set<number>();
      const usedNames = new Set<string>();
      const nextGroups = providers.map((provider, index) => {
        const sourceGroupIndex = provider.v8Source?.groupIndex;
        let matchedRaw: Record<string, unknown> | undefined;
        if (
          sourceGroupIndex !== undefined &&
          !usedIndexes.has(sourceGroupIndex) &&
          isRecord(existingGroups[sourceGroupIndex]) &&
          openAIProviderIdentity(existingGroups[sourceGroupIndex]) === provider.name.trim()
        ) {
          usedIndexes.add(sourceGroupIndex);
          matchedRaw = existingGroups[sourceGroupIndex];
        } else {
          matchedRaw = findRawRecord(
            rawRecords,
            usedIndexes,
            {
              name: provider.name,
              'auth-index': provider.authIndex,
            },
            index,
            openAIProviderIdentity
          );
        }
        const groupName = resolveV8GroupName(provider.name, usedNames, 'openai-compatibility');
        return buildV8OpenAIGroup(provider, groupName, matchedRaw);
      });

      const cleaned = cleanV8GroupsForWrite('openai-compatibility', nextGroups);
      try {
        return await putV8Groups('openai-compatibility', cleaned, effectiveEtag, writeScope);
      } catch (putError) {
        if (!v8ConfirmedByGet && isV8UnsupportedStatus(putError)) {
          return runV0Fallback();
        }
        throw putError;
      }
    });
  },

  createOpenAIProvider: (provider: OpenAIProviderConfig, options?: ProviderWriteOptions) => {
    const callerEtag = normalizeEtagValue(options?.expectedEtag ?? provider.v8Source?.etag);
    const writeScope = captureWriteScope();
    return enqueueProviderSectionWrite('openai-compatibility', async () => {
      assertConnectionEpochUnchanged(writeScope.epoch);
      const runV0Fallback = async () => {
        assertConnectionEpochUnchanged(writeScope.epoch);
        clearLatestV8ConfigEtagForEpoch(writeScope.epoch);
        await runProviderListMutationV0(
          'openai-compatibility',
          (latestItems) =>
            appendLatestProviderRecord(
              latestItems,
              serializeOpenAIProvider(provider),
              mergeOpenAIProviderPayload
            ),
          writeScope
        );
        return undefined;
      };

      if (!canUseV8Write()) {
        return runV0Fallback();
      }

      let existingGroups: Record<string, unknown>[] = [];
      let etag: string | undefined;
      let missingConfigPath = false;
      const writeVersionBefore = v8EtagMutationVersion;
      try {
        const response = await getScopedRawV8('/config/api-keys/openai-compatibility', writeScope);
        existingGroups = extractRawV8GroupsArray(response.data, 'openai-compatibility');
        etag = recordPassiveReadLatestV8ConfigEtag(
          readResponseEtag(response.headers),
          writeScope.epoch,
          writeVersionBefore
        );
      } catch (error) {
        if (isV8MissingConfigPathError(error)) {
          existingGroups = [];
          etag = recordPassiveReadLatestV8ConfigEtag(
            readErrorEtag(error),
            writeScope.epoch,
            writeVersionBefore
          );
          missingConfigPath = true;
        } else if (isV8UnsupportedStatus(error)) {
          return runV0Fallback();
        } else {
          throw error;
        }
      }

      const v8ConfirmedByGet = !missingConfigPath;
      assertV8EtagMatches(callerEtag, etag);
      const effectiveEtag = callerEtag ?? etag;

      const usedNames = collectUsedGroupNames(existingGroups);
      const groupName = resolveV8GroupName(provider.name, usedNames, 'openai-compatibility');
      const nextGroups = [...existingGroups, buildV8OpenAIGroup(provider, groupName)];
      const cleaned = cleanV8GroupsForWrite('openai-compatibility', nextGroups);
      try {
        return await putV8Groups('openai-compatibility', cleaned, effectiveEtag, writeScope);
      } catch (putError) {
        if (!v8ConfirmedByGet && isV8UnsupportedStatus(putError)) {
          return runV0Fallback();
        }
        throw putError;
      }
    });
  },

  updateOpenAIProvider: (
    originalName: string,
    originalIndex: number,
    value: OpenAIProviderConfig,
    options?: ProviderWriteOptions
  ) => {
    const callerEtag = normalizeEtagValue(options?.expectedEtag ?? value.v8Source?.etag);
    const writeScope = captureWriteScope();
    return enqueueProviderSectionWrite('openai-compatibility', async () => {
      assertConnectionEpochUnchanged(writeScope.epoch);
      const runV0Fallback = async () => {
        assertConnectionEpochUnchanged(writeScope.epoch);
        clearLatestV8ConfigEtagForEpoch(writeScope.epoch);
        await runProviderListMutationV0(
          'openai-compatibility',
          (latestItems) =>
            replaceLatestProviderRecord(
              latestItems,
              (record, index) =>
                index === originalIndex && openAIProviderIdentity(record) === originalName.trim(),
              serializeOpenAIProvider(value),
              mergeOpenAIProviderPayload
            ),
          writeScope
        );
        return undefined;
      };

      if (!canUseV8Write()) {
        return runV0Fallback();
      }

      let existingGroups: Record<string, unknown>[] = [];
      let etag: string | undefined;
      const writeVersionBefore = v8EtagMutationVersion;
      try {
        const response = await getScopedRawV8('/config/api-keys/openai-compatibility', writeScope);
        existingGroups = extractRawV8GroupsArray(response.data, 'openai-compatibility');
        etag = recordPassiveReadLatestV8ConfigEtag(
          readResponseEtag(response.headers),
          writeScope.epoch,
          writeVersionBefore
        );
      } catch (error) {
        if (isV8MissingConfigPathError(error) && value.v8Source) {
          recordPassiveReadLatestV8ConfigEtag(
            readErrorEtag(error),
            writeScope.epoch,
            writeVersionBefore
          );
          throw new Error('Provider configuration changed; refresh and try again.');
        }
        if (isV8UnsupportedStatus(error)) {
          return runV0Fallback();
        }
        throw error;
      }

      const v8ConfirmedByGet = true;
      assertV8EtagMatches(callerEtag, etag);
      const effectiveEtag = callerEtag ?? etag;
      const normalizedOriginal = originalName.trim();
      let targetIndex = -1;
      if (
        isRecord(existingGroups[originalIndex]) &&
        openAIProviderIdentity(existingGroups[originalIndex]) === normalizedOriginal
      ) {
        targetIndex = originalIndex;
      } else if (
        value.v8Source?.groupIndex !== undefined &&
        isRecord(existingGroups[value.v8Source.groupIndex]) &&
        openAIProviderIdentity(existingGroups[value.v8Source.groupIndex]) === normalizedOriginal
      ) {
        targetIndex = value.v8Source.groupIndex;
      } else {
        targetIndex = existingGroups.findIndex(
          (record) => openAIProviderIdentity(record) === normalizedOriginal
        );
      }
      if (targetIndex < 0) {
        throw new Error('Provider configuration changed; refresh and try again.');
      }

      const usedNames = collectUsedGroupNames(existingGroups, targetIndex);
      const groupName = resolveV8GroupName(value.name, usedNames, 'openai-compatibility');
      const updatedGroup = buildV8OpenAIGroup(value, groupName, existingGroups[targetIndex]);
      const nextGroups = existingGroups.map((group, idx) =>
        idx === targetIndex ? updatedGroup : group
      );
      const cleaned = cleanV8GroupsForWrite('openai-compatibility', nextGroups);
      try {
        return await putV8Groups('openai-compatibility', cleaned, effectiveEtag, writeScope);
      } catch (putError) {
        if (!v8ConfirmedByGet && isV8UnsupportedStatus(putError)) {
          return runV0Fallback();
        }
        throw putError;
      }
    });
  },

  deleteOpenAIProvider: (
    name: string,
    indexOrSource?: number | ProviderV8GroupSource | ProviderWriteOptions,
    options?: ProviderWriteOptions | ProviderV8GroupSource
  ) => {
    const {
      hintIndex,
      v8Source,
      expectedEtag: callerEtag,
    } = resolveOpenAIDeleteArgs(indexOrSource, options);
    const writeScope = captureWriteScope();
    return enqueueProviderSectionWrite('openai-compatibility', async () => {
      assertConnectionEpochUnchanged(writeScope.epoch);
      const runV0Fallback = () => {
        assertConnectionEpochUnchanged(writeScope.epoch);
        clearLatestV8ConfigEtagForEpoch(writeScope.epoch);
        return deleteScopedV0(
          `/openai-compatibility?name=${encodeURIComponent(name)}`,
          writeScope
        );
      };

      if (!canUseV8Write()) {
        return runV0Fallback();
      }

      let existingGroups: Record<string, unknown>[] = [];
      let etag: string | undefined;
      const writeVersionBefore = v8EtagMutationVersion;
      try {
        const response = await getScopedRawV8('/config/api-keys/openai-compatibility', writeScope);
        existingGroups = extractRawV8GroupsArray(response.data, 'openai-compatibility');
        etag = recordPassiveReadLatestV8ConfigEtag(
          readResponseEtag(response.headers),
          writeScope.epoch,
          writeVersionBefore
        );
      } catch (error) {
        if (isV8MissingConfigPathError(error)) {
          recordPassiveReadLatestV8ConfigEtag(
            readErrorEtag(error),
            writeScope.epoch,
            writeVersionBefore
          );
          return undefined;
        }
        if (isV8UnsupportedStatus(error)) {
          return runV0Fallback();
        }
        throw error;
      }

      const v8ConfirmedByGet = true;
      assertV8EtagMatches(callerEtag, etag);
      const effectiveEtag = callerEtag ?? etag;

      const normalizedName = name.trim();
      let targetIndex = -1;
      if (
        hintIndex !== undefined &&
        isRecord(existingGroups[hintIndex]) &&
        openAIProviderIdentity(existingGroups[hintIndex]) === normalizedName
      ) {
        targetIndex = hintIndex;
      } else if (
        v8Source?.groupIndex !== undefined &&
        isRecord(existingGroups[v8Source.groupIndex]) &&
        openAIProviderIdentity(existingGroups[v8Source.groupIndex]) === normalizedName
      ) {
        targetIndex = v8Source.groupIndex;
      } else {
        targetIndex = existingGroups.findIndex(
          (group) => openAIProviderIdentity(group) === normalizedName
        );
      }
      if (targetIndex < 0) {
        return undefined;
      }

      const nextGroups = existingGroups.filter((_, idx) => idx !== targetIndex);
      const cleaned = cleanV8GroupsForWrite('openai-compatibility', nextGroups);
      try {
        return await putV8Groups('openai-compatibility', cleaned, effectiveEtag, writeScope);
      } catch (putError) {
        if (!v8ConfirmedByGet && isV8UnsupportedStatus(putError)) {
          return runV0Fallback();
        }
        throw putError;
      }
    });
  },
};
