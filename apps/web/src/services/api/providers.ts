/**
 * AI 提供商相关 API
 */

import { apiClient } from './client';
import {
  normalizeGeminiKeyConfig,
  normalizeOpenAIProvider,
  normalizeProviderKeyConfig,
} from './transformers';
import type {
  GeminiKeyConfig,
  OpenAIProviderConfig,
  ProviderKeyConfig,
  ApiKeyEntry,
  ModelAlias,
} from '@/types';

const serializeHeaders = (headers?: Record<string, string>) =>
  headers && Object.keys(headers).length ? headers : undefined;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const AUTH_INDEX_FIELDS = ['auth-index', 'authIndex', 'auth_index'] as const;
const DISABLE_COOLING_FIELDS = ['disable-cooling', 'disableCooling', 'disable_cooling'] as const;

const COMMON_PROVIDER_KEY_FIELDS = [
  'api-key',
  'apiKey',
  ...AUTH_INDEX_FIELDS,
  'priority',
  'prefix',
  'base-url',
  'baseUrl',
  'base_url',
  'websockets',
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
  'experimental-cch-signing',
  'experimentalCchSigning',
  'experimental_cch_signing',
  'rebuild-mid-system-message',
  'rebuildMidSystemMessage',
  'rebuild_mid_system_message',
] as const;
const GEMINI_KEY_FIELDS = COOLING_PROVIDER_KEY_FIELDS;
const VERTEX_KEY_FIELDS = COMMON_PROVIDER_KEY_FIELDS;

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
  'input-modalities',
  'inputModalities',
  'input_modalities',
  'output-modalities',
  'outputModalities',
  'output_modalities',
  'thinking',
] as const;

const API_KEY_ENTRY_FIELDS = [
  'api-key',
  'apiKey',
  'key',
  ...AUTH_INDEX_FIELDS,
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
] as const;

const RAW_SECTION_ALIASES: Record<string, readonly string[]> = {
  'gemini-api-key': ['gemini-api-key', 'geminiApiKey', 'geminiApiKeys'],
  'interactions-api-key': ['interactions-api-key', 'interactionsApiKey', 'interactionsApiKeys'],
  'codex-api-key': ['codex-api-key', 'codexApiKey', 'codexApiKeys'],
  'xai-api-key': ['xai-api-key', 'xaiApiKey', 'xaiApiKeys'],
  'claude-api-key': ['claude-api-key', 'claudeApiKey', 'claudeApiKeys'],
  'vertex-api-key': ['vertex-api-key', 'vertexApiKey', 'vertexApiKeys'],
  'openai-compatibility': ['openai-compatibility', 'openaiCompatibility', 'openAICompatibility'],
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

const apiKeyEntryIdentity = (record: Record<string, unknown>) =>
  getStringField(record, AUTH_INDEX_FIELDS) || getStringField(record, ['api-key', 'apiKey', 'key']);

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

const mergeKnownRecordList = (
  rawItems: unknown,
  payloadItems: Record<string, unknown>[],
  knownFields: readonly string[],
  getIdentity: (record: Record<string, unknown>) => string
) => {
  const rawRecords = Array.isArray(rawItems)
    ? rawItems.map((item) => (isRecord(item) ? item : undefined))
    : [];
  const usedIndexes = new Set<number>();

  return payloadItems.map((payload, index) => {
    const raw = findRawRecord(rawRecords, usedIndexes, payload, index, getIdentity);
    return mergeKnownFields(raw, payload, knownFields);
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
          preserveOmittedRawField(rawModel, payload, next, ['image']);
          preserveOmittedRawField(rawModel, payload, next, [
            'force-mapping',
            'forceMapping',
            'force_mapping',
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
          preserveOmittedRawField(rawModel, payload, next, ['thinking']);
          return next;
        });
      })()
    : undefined;

const mergeProviderKeyPayload = (
  raw: unknown,
  payload: Record<string, unknown>,
  knownFields: readonly string[]
) => {
  const next = mergeKnownFields(raw, payload, knownFields);
  preserveOmittedRawField(raw, payload, next, DISABLE_COOLING_FIELDS);
  preserveOmittedRawField(raw, payload, next, [
    'experimental-cch-signing',
    'experimentalCchSigning',
    'experimental_cch_signing',
  ]);
  const models = mergeModelPayloads(raw, payload.models);
  if (models) next.models = models;
  if (isRecord(payload.cloak)) {
    const rawCloak = isRecord(raw) ? raw.cloak : undefined;
    const cloak = mergeKnownFields(rawCloak, payload.cloak, CLOAK_FIELDS);
    preserveOmittedRawField(rawCloak, payload.cloak, cloak, [
      'cache-user-id',
      'cacheUserId',
      'cache_user_id',
    ]);
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
    next['api-key-entries'] = mergeKnownRecordList(
      rawApiKeyEntries,
      apiKeyEntries.filter(isRecord),
      API_KEY_ENTRY_FIELDS,
      apiKeyEntryIdentity
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
  getIdentity: (record: Record<string, unknown>) => string
) => {
  const payloads = configs.map((item) => serialize(item));

  let rawConfig: unknown;
  try {
    rawConfig = await apiClient.get('/config');
  } catch {
    return payloads;
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

const mutateLatestProviderList = async (
  section: string,
  mutate: (latestItems: unknown[]) => unknown[]
) =>
  enqueueProviderSectionWrite(section, async () => {
    const rawConfig = await apiClient.get('/config');
    const latestItems = getRawSectionList(rawConfig, section);
    await apiClient.put(`/${section}`, mutate(latestItems));
  });

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
          if (model.inputModalities !== undefined) {
            payload['input-modalities'] = model.inputModalities;
          }
          if (model.outputModalities !== undefined) {
            payload['output-modalities'] = model.outputModalities;
          }
          if (isRecord(model.thinking)) {
            payload.thinking = model.thinking;
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
  if (apiKey) payload['api-key'] = apiKey;
  const authIndex = serializeAuthIndex(entry.authIndex);
  if (authIndex) payload['auth-index'] = authIndex;
  if (entry.proxyUrl) payload['proxy-url'] = entry.proxyUrl;
  const headers = serializeHeaders(entry.headers);
  if (headers) payload.headers = headers;
  return payload;
};

const serializeProviderKey = (config: ProviderKeyConfig) => {
  const payload: Record<string, unknown> = {};
  const apiKey = config.apiKey?.trim();
  if (apiKey) payload['api-key'] = apiKey;
  const authIndex = serializeAuthIndex(config.authIndex);
  if (authIndex) payload['auth-index'] = authIndex;
  if (config.priority !== undefined) payload.priority = config.priority;
  if (config.prefix?.trim()) payload.prefix = config.prefix.trim();
  if (config.baseUrl) payload['base-url'] = config.baseUrl;
  if (config.websockets !== undefined) payload.websockets = config.websockets;
  if (config.disableCooling !== undefined) payload['disable-cooling'] = config.disableCooling;
  if (config.experimentalCchSigning !== undefined) {
    payload['experimental-cch-signing'] = config.experimentalCchSigning;
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
  if (config.cloak) {
    const cloakPayload: Record<string, unknown> = {};
    const mode = config.cloak.mode?.trim();
    if (mode) cloakPayload.mode = mode;
    if (config.cloak.strictMode !== undefined)
      cloakPayload['strict-mode'] = config.cloak.strictMode;
    if (config.cloak.sensitiveWords && config.cloak.sensitiveWords.length) {
      cloakPayload['sensitive-words'] = config.cloak.sensitiveWords;
    }
    if (config.cloak.cacheUserId !== undefined) {
      cloakPayload['cache-user-id'] = config.cloak.cacheUserId;
    }
    if (Object.keys(cloakPayload).length) {
      payload.cloak = cloakPayload;
    }
  }
  return payload;
};

const serializeVertexModelAliases = (models?: ModelAlias[]) =>
  Array.isArray(models)
    ? models
        .map((model) => {
          const name = typeof model?.name === 'string' ? model.name.trim() : '';
          const alias = typeof model?.alias === 'string' ? model.alias.trim() : '';
          if (!name || !alias) return null;
          return { name, alias };
        })
        .filter(Boolean)
    : undefined;

const serializeVertexKey = (config: ProviderKeyConfig) => {
  const payload: Record<string, unknown> = {};
  const apiKey = config.apiKey?.trim();
  if (apiKey) payload['api-key'] = apiKey;
  const authIndex = serializeAuthIndex(config.authIndex);
  if (authIndex) payload['auth-index'] = authIndex;
  if (config.priority !== undefined) payload.priority = config.priority;
  if (config.prefix?.trim()) payload.prefix = config.prefix.trim();
  if (config.baseUrl) payload['base-url'] = config.baseUrl;
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
  if (!apiKey) {
    throw new Error('API key is required for Gemini and Interactions providers');
  }
  payload['api-key'] = apiKey;
  if (config.priority !== undefined) payload.priority = config.priority;
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
      ? provider.apiKeyEntries.map((entry) => serializeApiKeyEntry(entry))
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

export const providersApi = {
  async getGeminiKeys(): Promise<GeminiKeyConfig[]> {
    const data = await apiClient.get('/gemini-api-key');
    const list = extractArrayPayload(data, 'gemini-api-key');
    return list.map((item) => normalizeGeminiKeyConfig(item)).filter(Boolean) as GeminiKeyConfig[];
  },

  saveGeminiKeys: async (configs: GeminiKeyConfig[]) =>
    apiClient.put(
      '/gemini-api-key',
      await buildPreservedList(
        'gemini-api-key',
        configs,
        serializeGeminiKey,
        (raw, payload) => mergeProviderKeyPayload(raw, payload, GEMINI_KEY_FIELDS),
        providerKeyIdentity
      )
    ),

  createGeminiKey: (config: GeminiKeyConfig) =>
    mutateLatestProviderList('gemini-api-key', (latestItems) =>
      appendLatestProviderRecord(latestItems, serializeGeminiKey(config), (raw, payload) =>
        mergeProviderKeyPayload(raw, payload, GEMINI_KEY_FIELDS)
      )
    ),

  updateGeminiKey: (original: GeminiKeyConfig, value: GeminiKeyConfig) =>
    mutateLatestProviderList('gemini-api-key', (latestItems) =>
      replaceLatestProviderRecord(
        latestItems,
        (record) => matchesProviderConfig(record, original),
        serializeGeminiKey(value),
        (raw, payload) => mergeProviderKeyPayload(raw, payload, GEMINI_KEY_FIELDS)
      )
    ),

  deleteGeminiKey: (apiKey: string, baseUrl?: string) =>
    apiClient.delete(`/gemini-api-key${buildProviderDeleteQuery(apiKey, baseUrl)}`),

  async getInteractionsKeys(): Promise<GeminiKeyConfig[]> {
    const data = await apiClient.get('/interactions-api-key');
    const list = extractArrayPayload(data, 'interactions-api-key');
    return list.map((item) => normalizeGeminiKeyConfig(item)).filter(Boolean) as GeminiKeyConfig[];
  },

  saveInteractionsKeys: async (configs: GeminiKeyConfig[]) =>
    apiClient.put(
      '/interactions-api-key',
      await buildPreservedList(
        'interactions-api-key',
        configs,
        serializeGeminiKey,
        (raw, payload) => mergeProviderKeyPayload(raw, payload, GEMINI_KEY_FIELDS),
        providerKeyIdentity
      )
    ),

  createInteractionsKey: (config: GeminiKeyConfig) =>
    mutateLatestProviderList('interactions-api-key', (latestItems) =>
      appendLatestProviderRecord(latestItems, serializeGeminiKey(config), (raw, payload) =>
        mergeProviderKeyPayload(raw, payload, GEMINI_KEY_FIELDS)
      )
    ),

  updateInteractionsKey: (original: GeminiKeyConfig, value: GeminiKeyConfig) =>
    mutateLatestProviderList('interactions-api-key', (latestItems) =>
      replaceLatestProviderRecord(
        latestItems,
        (record) => matchesProviderConfig(record, original),
        serializeGeminiKey(value),
        (raw, payload) => mergeProviderKeyPayload(raw, payload, GEMINI_KEY_FIELDS)
      )
    ),

  deleteInteractionsKey: (apiKey: string, baseUrl?: string) =>
    apiClient.delete(`/interactions-api-key${buildProviderDeleteQuery(apiKey, baseUrl)}`),

  async getCodexConfigs(): Promise<ProviderKeyConfig[]> {
    const data = await apiClient.get('/codex-api-key');
    const list = extractArrayPayload(data, 'codex-api-key');
    return list
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  },

  saveCodexConfigs: async (configs: ProviderKeyConfig[]) =>
    apiClient.put(
      '/codex-api-key',
      await buildPreservedList(
        'codex-api-key',
        configs,
        serializeProviderKey,
        (raw, payload) => mergeProviderKeyPayload(raw, payload, CODEX_KEY_FIELDS),
        providerKeyIdentity
      )
    ),

  createCodexConfig: (config: ProviderKeyConfig) =>
    mutateLatestProviderList('codex-api-key', (latestItems) =>
      appendLatestProviderRecord(latestItems, serializeProviderKey(config), (raw, payload) =>
        mergeProviderKeyPayload(raw, payload, CODEX_KEY_FIELDS)
      )
    ),

  updateCodexConfig: (original: ProviderKeyConfig, value: ProviderKeyConfig) =>
    mutateLatestProviderList('codex-api-key', (latestItems) =>
      replaceLatestProviderRecord(
        latestItems,
        (record) => matchesProviderConfig(record, original),
        serializeProviderKey(value),
        (raw, payload) => mergeProviderKeyPayload(raw, payload, CODEX_KEY_FIELDS)
      )
    ),

  deleteCodexConfig: (apiKey: string, baseUrl?: string) =>
    apiClient.delete(`/codex-api-key${buildProviderDeleteQuery(apiKey, baseUrl)}`),

  async getXAIConfigs(): Promise<ProviderKeyConfig[]> {
    const data = await apiClient.get('/xai-api-key');
    const list = extractArrayPayload(data, 'xai-api-key');
    return list
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  },

  saveXAIConfigs: async (configs: ProviderKeyConfig[]) =>
    apiClient.put(
      '/xai-api-key',
      await buildPreservedList(
        'xai-api-key',
        configs,
        serializeProviderKey,
        (raw, payload) => mergeProviderKeyPayload(raw, payload, XAI_KEY_FIELDS),
        providerKeyIdentity
      )
    ),

  createXAIConfig: (config: ProviderKeyConfig) =>
    mutateLatestProviderList('xai-api-key', (latestItems) =>
      appendLatestProviderRecord(latestItems, serializeProviderKey(config), (raw, payload) =>
        mergeProviderKeyPayload(raw, payload, XAI_KEY_FIELDS)
      )
    ),

  updateXAIConfig: (original: ProviderKeyConfig, value: ProviderKeyConfig) =>
    mutateLatestProviderList('xai-api-key', (latestItems) =>
      replaceLatestProviderRecord(
        latestItems,
        (record) => matchesProviderConfig(record, original),
        serializeProviderKey(value),
        (raw, payload) => mergeProviderKeyPayload(raw, payload, XAI_KEY_FIELDS)
      )
    ),

  deleteXAIConfig: (apiKey: string, baseUrl?: string) =>
    apiClient.delete(`/xai-api-key${buildProviderDeleteQuery(apiKey, baseUrl)}`),

  async getClaudeConfigs(): Promise<ProviderKeyConfig[]> {
    const data = await apiClient.get('/claude-api-key');
    const list = extractArrayPayload(data, 'claude-api-key');
    return list
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  },

  saveClaudeConfigs: async (configs: ProviderKeyConfig[]) =>
    apiClient.put(
      '/claude-api-key',
      await buildPreservedList(
        'claude-api-key',
        configs,
        serializeProviderKey,
        (raw, payload) => mergeProviderKeyPayload(raw, payload, CLAUDE_KEY_FIELDS),
        providerKeyIdentity
      )
    ),

  createClaudeConfig: (config: ProviderKeyConfig) =>
    mutateLatestProviderList('claude-api-key', (latestItems) =>
      appendLatestProviderRecord(latestItems, serializeProviderKey(config), (raw, payload) =>
        mergeProviderKeyPayload(raw, payload, CLAUDE_KEY_FIELDS)
      )
    ),

  updateClaudeConfig: (original: ProviderKeyConfig, value: ProviderKeyConfig) =>
    mutateLatestProviderList('claude-api-key', (latestItems) =>
      replaceLatestProviderRecord(
        latestItems,
        (record) => matchesProviderConfig(record, original),
        serializeProviderKey(value),
        (raw, payload) => mergeProviderKeyPayload(raw, payload, CLAUDE_KEY_FIELDS)
      )
    ),

  deleteClaudeConfig: (apiKey: string, baseUrl?: string) =>
    apiClient.delete(`/claude-api-key${buildProviderDeleteQuery(apiKey, baseUrl)}`),

  async getVertexConfigs(): Promise<ProviderKeyConfig[]> {
    const data = await apiClient.get('/vertex-api-key');
    const list = extractArrayPayload(data, 'vertex-api-key');
    return list
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  },

  saveVertexConfigs: async (configs: ProviderKeyConfig[]) =>
    apiClient.put(
      '/vertex-api-key',
      await buildPreservedList(
        'vertex-api-key',
        configs,
        serializeVertexKey,
        (raw, payload) => mergeProviderKeyPayload(raw, payload, VERTEX_KEY_FIELDS),
        providerKeyIdentity
      )
    ),

  createVertexConfig: (config: ProviderKeyConfig) =>
    mutateLatestProviderList('vertex-api-key', (latestItems) =>
      appendLatestProviderRecord(latestItems, serializeVertexKey(config), (raw, payload) =>
        mergeProviderKeyPayload(raw, payload, VERTEX_KEY_FIELDS)
      )
    ),

  updateVertexConfig: (original: ProviderKeyConfig, value: ProviderKeyConfig) =>
    mutateLatestProviderList('vertex-api-key', (latestItems) =>
      replaceLatestProviderRecord(
        latestItems,
        (record) => matchesProviderConfig(record, original),
        serializeVertexKey(value),
        (raw, payload) => mergeProviderKeyPayload(raw, payload, VERTEX_KEY_FIELDS)
      )
    ),

  deleteVertexConfig: (apiKey: string, baseUrl?: string) =>
    apiClient.delete(`/vertex-api-key${buildProviderDeleteQuery(apiKey, baseUrl)}`),

  async getOpenAIProviders(): Promise<OpenAIProviderConfig[]> {
    const data = await apiClient.get('/openai-compatibility');
    const list = extractArrayPayload(data, 'openai-compatibility');
    const providers = list
      .map((item) => normalizeOpenAIProvider(item))
      .filter(Boolean) as OpenAIProviderConfig[];
    return hydrateOpenAIProvidersDisableCooling(providers);
  },

  saveOpenAIProviders: async (providers: OpenAIProviderConfig[]) =>
    apiClient.put(
      '/openai-compatibility',
      await buildPreservedList(
        'openai-compatibility',
        providers,
        serializeOpenAIProvider,
        mergeOpenAIProviderPayload,
        openAIProviderIdentity
      )
    ),

  createOpenAIProvider: (provider: OpenAIProviderConfig) =>
    mutateLatestProviderList('openai-compatibility', (latestItems) =>
      appendLatestProviderRecord(
        latestItems,
        serializeOpenAIProvider(provider),
        mergeOpenAIProviderPayload
      )
    ),

  updateOpenAIProvider: (
    originalName: string,
    originalIndex: number,
    value: OpenAIProviderConfig
  ) =>
    mutateLatestProviderList('openai-compatibility', (latestItems) =>
      replaceLatestProviderRecord(
        latestItems,
        (record, index) =>
          index === originalIndex && openAIProviderIdentity(record) === originalName.trim(),
        serializeOpenAIProvider(value),
        mergeOpenAIProviderPayload
      )
    ),

  deleteOpenAIProvider: (name: string) =>
    apiClient.delete(`/openai-compatibility?name=${encodeURIComponent(name)}`),
};
