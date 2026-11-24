import {
  readCoolingOverride,
  type ApiKeyEntry,
  type ClaudeFingerprintProfile,
  type CloakConfig,
  type GeminiKeyConfig,
  type ModelAlias,
  type OpenAIProviderConfig,
  type ProviderKeyConfig,
  type ProviderV8Family,
  type ProviderV8GroupSource,
} from '@/types';
import type { Config } from '@/types/config';
import { buildHeaderObject } from '@/utils/headers';
import { normalizeCredentialWeight } from '@/utils/credentialWeight';
import { markPersistedOpenAIKeyEntry } from '@/utils/openAIKeyEntries';

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const normalizeBoolean = (value: unknown): boolean | undefined => {
  if (value === undefined || value === null) return undefined;
  if (typeof value === 'boolean') return value;
  if (typeof value === 'number') return value !== 0;
  if (typeof value === 'string') {
    const trimmed = value.trim().toLowerCase();
    if (['true', '1', 'yes', 'y', 'on'].includes(trimmed)) return true;
    if (['false', '0', 'no', 'n', 'off'].includes(trimmed)) return false;
    return undefined;
  }
  return undefined;
};

export const normalizeClaudeFingerprintProfile = (
  value: unknown
): ClaudeFingerprintProfile | undefined => {
  if (typeof value !== 'string') return undefined;
  const normalized = value.trim().toLowerCase();
  if (normalized === 'claude-code-cli' || normalized === 'oauth-cli') {
    return 'claude-code-cli';
  }
  return undefined;
};

const normalizeString = (value: unknown): string | undefined => {
  if (value === undefined || value === null) return undefined;
  const trimmed = String(value).trim();
  return trimmed ? trimmed : undefined;
};

const normalizeStringList = (value: unknown): string[] | undefined => {
  if (!Array.isArray(value)) return undefined;
  return value.map(normalizeString).filter((item): item is string => Boolean(item));
};

const normalizeNumber = (value: unknown): number | undefined => {
  if (typeof value === 'number' && Number.isFinite(value)) {
    return value;
  }
  if (typeof value === 'string' && value.trim() !== '') {
    const parsed = Number(value);
    if (Number.isFinite(parsed)) {
      return parsed;
    }
  }
  return undefined;
};

const normalizeModelAliases = (models: unknown): ModelAlias[] => {
  if (!Array.isArray(models)) return [];
  return models
    .map((item) => {
      if (item === undefined || item === null) return null;
      if (typeof item === 'string') {
        const trimmed = item.trim();
        return trimmed ? ({ name: trimmed } satisfies ModelAlias) : null;
      }
      if (!isRecord(item)) return null;

      const nameRaw = item.name || item.id || item.model;
      if (!nameRaw) return null;
      const name = String(nameRaw).trim();
      if (!name) return null;
      const entry: ModelAlias = { name };
      const rawAlias = typeof item.alias === 'string' ? item.alias.trim() : '';
      if (rawAlias && rawAlias !== name) {
        entry.alias = rawAlias;
      }
      const displayName = normalizeString(
        item['display-name'] ?? item.displayName ?? item.display_name
      );
      if (displayName) {
        entry.displayName = displayName;
      }
      const priority = item.priority ?? item['priority'];
      if (priority !== undefined) {
        const parsed = Number(priority);
        if (Number.isFinite(parsed)) {
          entry.priority = parsed;
        }
      }
      const testModel = item['test-model'] ?? item.testModel;
      if (testModel) {
        entry.testModel = String(testModel);
      }
      const image = normalizeBoolean(item.image ?? item['image']);
      if (image !== undefined) {
        entry.image = image;
      }
      const forceMapping = normalizeBoolean(
        item['force-mapping'] ?? item.forceMapping ?? item.force_mapping
      );
      if (forceMapping !== undefined) {
        entry.forceMapping = forceMapping;
      }
      const maxContextLength = normalizeNumber(
        item['max-context-length'] ?? item.maxContextLength ?? item.max_context_length
      );
      if (maxContextLength !== undefined) {
        entry.maxContextLength = maxContextLength;
      }
      const isCompat = normalizeBoolean(item['is-compat'] ?? item.isCompat ?? item.is_compat);
      if (isCompat !== undefined) {
        entry.isCompat = isCompat;
      }
      const openapi = normalizeBoolean(item.openapi ?? item['openapi']);
      if (openapi !== undefined) {
        entry.openapi = openapi;
      }
      const supportConfigurationUpdate = normalizeBoolean(
        item['support-configuration-update'] ??
          item.supportConfigurationUpdate ??
          item.support_configuration_update
      );
      if (supportConfigurationUpdate !== undefined) {
        entry.supportConfigurationUpdate = supportConfigurationUpdate;
      }
      const useMaxCompletionTokens = normalizeBoolean(
        item['use-max-completion-tokens'] ??
          item.useMaxCompletionTokens ??
          item.use_max_completion_tokens
      );
      if (useMaxCompletionTokens !== undefined) {
        entry.useMaxCompletionTokens = useMaxCompletionTokens;
      }
      const inputModalities = normalizeExcludedModels(
        item['input-modalities'] ?? item.inputModalities ?? item.input_modalities
      );
      if (inputModalities.length) {
        entry.inputModalities = inputModalities;
      }
      const outputModalities = normalizeExcludedModels(
        item['output-modalities'] ?? item.outputModalities ?? item.output_modalities
      );
      if (outputModalities.length) {
        entry.outputModalities = outputModalities;
      }
      const thinking = item.thinking ?? item['thinking'];
      if (isRecord(thinking)) {
        entry.thinking = thinking;
      }
      return entry;
    })
    .filter(Boolean) as ModelAlias[];
};

const normalizeHeaders = (headers: unknown) => {
  if (!headers || typeof headers !== 'object') return undefined;
  const normalized = buildHeaderObject(
    Array.isArray(headers)
      ? (headers as Array<{ key: string; value: string }>)
      : (headers as Record<string, string | undefined | null>)
  );
  return Object.keys(normalized).length ? normalized : undefined;
};

const normalizeExcludedModels = (input: unknown): string[] => {
  const rawList = Array.isArray(input)
    ? input
    : typeof input === 'string'
      ? input.split(/[\n,]/)
      : [];
  const seen = new Set<string>();
  const normalized: string[] = [];

  rawList.forEach((item) => {
    const trimmed = String(item ?? '').trim();
    if (!trimmed) return;
    const key = trimmed.toLowerCase();
    if (seen.has(key)) return;
    seen.add(key);
    normalized.push(trimmed);
  });

  return normalized;
};

const normalizePrefix = (value: unknown): string | undefined => {
  if (value === undefined || value === null) return undefined;
  const trimmed = String(value).trim();
  return trimmed ? trimmed : undefined;
};

const normalizeAuthIndex = (value: unknown): string | undefined => {
  if (value === undefined || value === null) return undefined;
  const trimmed = String(value).trim();
  return trimmed ? trimmed : undefined;
};

const normalizeApiKeyEntry = (entry: unknown, persistedIndex?: number): ApiKeyEntry | null => {
  if (entry === undefined || entry === null) return null;
  const record = isRecord(entry) ? entry : null;
  const apiKey =
    record?.['api-key'] ??
    record?.apiKey ??
    record?.key ??
    (typeof entry === 'string' ? entry : '');
  const trimmed = String(apiKey || '').trim();
  const authIndex = normalizeAuthIndex(
    record?.['auth-index'] ?? record?.authIndex ?? record?.['auth_index']
  );
  const proxyUrl = record ? (record['proxy-url'] ?? record.proxyUrl) : undefined;
  const headers = record ? normalizeHeaders(record.headers) : undefined;
  const weight = normalizeCredentialWeight(record?.weight);
  const hasExplicitApiKey =
    record !== null &&
    (['api-key', 'apiKey', 'key'] as const).some(
      (field) =>
        Object.prototype.hasOwnProperty.call(record, field) && typeof record[field] === 'string'
    );
  const hasConnectionPool =
    record !== null &&
    (isRecord(record['connection-pool']) ||
      isRecord(record.connectionPool) ||
      isRecord(record['connection_pool']));
  // CPA stores keyless entries as {"api-key": ""} (with optional proxy-url/weight/connection-pool);
  // providers disabled at startup have no runtime auth-index yet. Drop only entries
  // that have neither an explicit api-key field (without unsupported-only headers)
  // nor any other CPA-supported per-entry field.
  if (
    !trimmed &&
    !authIndex &&
    !String(proxyUrl ?? '').trim() &&
    weight === undefined &&
    !hasConnectionPool &&
    (!hasExplicitApiKey || Boolean(headers))
  ) {
    return null;
  }

  const result: ApiKeyEntry = {
    apiKey: trimmed,
    proxyUrl: proxyUrl ? String(proxyUrl) : undefined,
    headers,
  };
  if (weight !== undefined) result.weight = weight;
  if (authIndex) result.authIndex = authIndex;
  return markPersistedOpenAIKeyEntry(
    result,
    persistedIndex,
    hasExplicitApiKey || hasConnectionPool
  );
};

const normalizeProviderKeyConfig = (
  item: unknown,
  allowEndpointOnly = false
): ProviderKeyConfig | null => {
  if (item === undefined || item === null) return null;
  const record = isRecord(item) ? item : null;
  const apiKey = record?.['api-key'] ?? record?.apiKey ?? (typeof item === 'string' ? item : '');
  const trimmed = String(apiKey || '').trim();
  const authIndex = normalizeAuthIndex(
    record?.['auth-index'] ?? record?.authIndex ?? record?.['auth_index']
  );
  if (!trimmed && !authIndex && !allowEndpointOnly) return null;

  const config: ProviderKeyConfig = { apiKey: trimmed };
  const name =
    record && typeof record.name === 'string' && record.name.trim()
      ? record.name.trim()
      : undefined;
  if (name) config.name = name;
  const priority = record?.priority ?? record?.['priority'];
  if (priority !== undefined && priority !== null && String(priority).trim() !== '') {
    const parsed = Number(priority);
    if (Number.isFinite(parsed)) {
      config.priority = parsed;
    }
  }
  const weight = normalizeCredentialWeight(record?.weight);
  if (weight !== undefined) config.weight = weight;
  const prefix = normalizePrefix(record?.prefix ?? record?.['prefix']);
  if (prefix) config.prefix = prefix;
  const baseUrl = record ? (record['base-url'] ?? record.baseUrl) : undefined;
  const proxyUrl = record ? (record['proxy-url'] ?? record.proxyUrl) : undefined;
  if (baseUrl) config.baseUrl = String(baseUrl);
  const websockets = normalizeBoolean(record?.websockets ?? record?.['websockets']);
  if (websockets !== undefined) config.websockets = websockets;
  const disableCooling = readCoolingOverride(record);
  if (disableCooling !== undefined) config.disableCooling = disableCooling;
  const experimentalCchSigning = normalizeBoolean(
    record?.['experimental-cch-signing'] ??
      record?.experimentalCchSigning ??
      record?.experimental_cch_signing
  );
  if (experimentalCchSigning !== undefined) config.experimentalCchSigning = experimentalCchSigning;
  const fingerprintProfile = normalizeClaudeFingerprintProfile(
    record?.['fingerprint-profile'] ?? record?.fingerprintProfile ?? record?.fingerprint_profile
  );
  if (fingerprintProfile !== undefined) config.fingerprintProfile = fingerprintProfile;
  const rebuildMidSystemMessage = normalizeBoolean(
    record?.['rebuild-mid-system-message'] ??
      record?.rebuildMidSystemMessage ??
      record?.rebuild_mid_system_message
  );
  if (rebuildMidSystemMessage !== undefined) {
    config.rebuildMidSystemMessage = rebuildMidSystemMessage;
  }
  if (proxyUrl) config.proxyUrl = String(proxyUrl);
  const headers = normalizeHeaders(record?.headers);
  if (headers) config.headers = headers;
  const models = normalizeModelAliases(record?.models);
  if (models.length) config.models = models;
  const excludedModels = normalizeExcludedModels(
    record?.['excluded-models'] ??
      record?.excludedModels ??
      record?.['excluded_models'] ??
      record?.excluded_models
  );
  if (excludedModels.length) config.excludedModels = excludedModels;
  if (authIndex) config.authIndex = authIndex;

  const cloakRaw = record?.cloak;
  if (isRecord(cloakRaw)) {
    const cloak: CloakConfig = {};
    const mode = cloakRaw.mode ?? cloakRaw['mode'];
    if (typeof mode === 'string' && mode.trim()) {
      cloak.mode = mode.trim();
    }
    const strictMode = normalizeBoolean(
      cloakRaw['strict-mode'] ?? cloakRaw.strictMode ?? cloakRaw.strict_mode
    );
    if (strictMode !== undefined) {
      cloak.strictMode = strictMode;
    }
    const rawSensitiveWords =
      cloakRaw['sensitive-words'] ?? cloakRaw.sensitiveWords ?? cloakRaw.sensitive_words;
    const sensitiveWords = normalizeExcludedModels(
      Array.isArray(rawSensitiveWords)
        ? rawSensitiveWords.filter((item): item is string => typeof item === 'string')
        : rawSensitiveWords
    );
    if (sensitiveWords.length) {
      cloak.sensitiveWords = sensitiveWords;
    }
    const cacheUserId = normalizeBoolean(
      cloakRaw['cache-user-id'] ?? cloakRaw.cacheUserId ?? cloakRaw.cache_user_id
    );
    if (cacheUserId !== undefined) {
      cloak.cacheUserId = cacheUserId;
    }
    const rawWordsReplace =
      cloakRaw['sensitive-words-replace'] ??
      cloakRaw.sensitiveWordsReplace ??
      cloakRaw.sensitive_words_replace;
    if (isRecord(rawWordsReplace)) {
      const wordsReplace: Record<string, string> = {};
      Object.entries(rawWordsReplace).forEach(([word, replacement]) => {
        if (word && typeof replacement === 'string') {
          wordsReplace[word] = replacement;
        }
      });
      if (Object.keys(wordsReplace).length > 0) {
        cloak.sensitiveWordsReplace = wordsReplace;
      }
    }
    const rawPatternsReplace =
      cloakRaw['sensitive-patterns-replace'] ??
      cloakRaw.sensitivePatternsReplace ??
      cloakRaw.sensitive_patterns_replace;
    if (Array.isArray(rawPatternsReplace)) {
      const patternsReplace = rawPatternsReplace
        .filter(isRecord)
        .map((rule) => ({ ...rule }));
      if (patternsReplace.length > 0) {
        cloak.sensitivePatternsReplace = patternsReplace;
      }
    }
    config.cloak = cloak;
  }

  return config;
};

const normalizeGeminiKeyConfig = (
  item: unknown,
  allowEndpointOnly = false
): GeminiKeyConfig | null => {
  if (item === undefined || item === null) return null;
  const record = isRecord(item) ? item : null;
  let apiKey = record?.['api-key'] ?? record?.apiKey;
  if (!apiKey && typeof item === 'string') {
    apiKey = item;
  }
  const trimmed = String(apiKey || '').trim();
  const authIndex = normalizeAuthIndex(
    record?.['auth-index'] ?? record?.authIndex ?? record?.['auth_index']
  );
  if (!trimmed && !authIndex && !allowEndpointOnly) return null;

  const config: GeminiKeyConfig = { apiKey: trimmed };
  const name =
    record && typeof record.name === 'string' && record.name.trim()
      ? record.name.trim()
      : undefined;
  if (name) config.name = name;
  const priority = record?.priority ?? record?.['priority'];
  if (priority !== undefined && priority !== null && String(priority).trim() !== '') {
    const parsed = Number(priority);
    if (Number.isFinite(parsed)) {
      config.priority = parsed;
    }
  }
  const weight = normalizeCredentialWeight(record?.weight);
  if (weight !== undefined) config.weight = weight;
  const prefix = normalizePrefix(record?.prefix ?? record?.['prefix']);
  if (prefix) config.prefix = prefix;
  const baseUrl = record ? (record['base-url'] ?? record.baseUrl ?? record['base_url']) : undefined;
  if (baseUrl) config.baseUrl = String(baseUrl);
  const proxyUrl = record
    ? (record['proxy-url'] ?? record.proxyUrl ?? record['proxy_url'])
    : undefined;
  if (proxyUrl) config.proxyUrl = String(proxyUrl);
  const disableCooling = readCoolingOverride(record);
  if (disableCooling !== undefined) config.disableCooling = disableCooling;
  const models = normalizeModelAliases(record?.models);
  if (models.length) config.models = models;
  const headers = normalizeHeaders(record?.headers);
  if (headers) config.headers = headers;
  const excludedModels = normalizeExcludedModels(
    record?.['excluded-models'] ?? record?.excludedModels
  );
  if (excludedModels.length) config.excludedModels = excludedModels;
  if (authIndex) config.authIndex = authIndex;
  return config;
};

const normalizeOpenAIProvider = (provider: unknown): OpenAIProviderConfig | null => {
  if (!isRecord(provider)) return null;
  const name = provider.name || provider.id;
  const baseUrl = provider['base-url'] ?? provider.baseUrl;
  if (!name || !baseUrl) return null;

  let apiKeyEntries: ApiKeyEntry[] = [];
  const rawKeyEntries = Array.isArray(provider['api-key-entries'])
    ? provider['api-key-entries']
    : Array.isArray(provider.keys)
      ? provider.keys
      : Array.isArray(provider['api-keys'])
        ? provider['api-keys']
        : null;
  if (rawKeyEntries) {
    apiKeyEntries = rawKeyEntries
      .map((entry, index) => normalizeApiKeyEntry(entry, index))
      .filter(Boolean) as ApiKeyEntry[];
  }

  const headers = normalizeHeaders(provider.headers);
  const models = normalizeModelAliases(provider.models);
  const priority = provider.priority ?? provider['priority'];
  const testModel = provider['test-model'] ?? provider.testModel;

  const result: OpenAIProviderConfig = {
    name: String(name),
    baseUrl: String(baseUrl),
    apiKeyEntries,
  };

  const disabled = normalizeBoolean(provider.disabled ?? provider['disabled']);
  if (disabled !== undefined) result.disabled = disabled;
  const disableCooling = readCoolingOverride(provider);
  if (disableCooling !== undefined) result.disableCooling = disableCooling;
  const supportPromptCacheKey = normalizeBoolean(
    provider['support-prompt-cache-key'] ??
      provider.supportPromptCacheKey ??
      provider['support_prompt_cache_key']
  );
  if (supportPromptCacheKey !== undefined) {
    result.supportPromptCacheKey = supportPromptCacheKey;
  }
  const prefix = normalizePrefix(provider.prefix ?? provider['prefix']);
  if (prefix) result.prefix = prefix;
  if (headers) result.headers = headers;
  if (models.length) result.models = models;
  if (priority !== undefined) result.priority = Number(priority);
  if (testModel) result.testModel = String(testModel);
  const authIndex = normalizeAuthIndex(
    provider['auth-index'] ?? provider.authIndex ?? provider['auth_index']
  );
  if (authIndex) result.authIndex = authIndex;
  return result;
};

const ENDPOINT_ONLY_V8_FAMILIES: Partial<Record<ProviderV8Family, true>> = {
  gemini: true,
  interactions: true,
  vertex: true,
  codex: true,
  claude: true,
  xai: true,
  meta: true,
};

const extractRawV8FamilyGroups = (
  rawTreeOrGroups: unknown,
  family: ProviderV8Family
): Record<string, unknown>[] => {
  if (Array.isArray(rawTreeOrGroups)) {
    return rawTreeOrGroups.filter(isRecord);
  }
  if (!isRecord(rawTreeOrGroups)) return [];
  const direct = rawTreeOrGroups[family];
  if (Array.isArray(direct)) {
    return direct.filter(isRecord);
  }
  const nestedApiKeys = rawTreeOrGroups['api-keys'];
  if (isRecord(nestedApiKeys) && Array.isArray(nestedApiKeys[family])) {
    return nestedApiKeys[family].filter(isRecord);
  }
  return [];
};

const normalizeV8Etag = (etag: unknown): string | undefined => {
  if (typeof etag !== 'string') return undefined;
  const trimmed = etag.trim();
  if (!trimmed) return undefined;
  if (/^w\//i.test(trimmed)) {
    const stripped = trimmed.slice(2).trim();
    return stripped || undefined;
  }
  return trimmed;
};

export function normalizeV8ProviderGroups(
  rawTreeOrGroups: unknown,
  family: 'openai-compatibility',
  etag?: string
): OpenAIProviderConfig[];
export function normalizeV8ProviderGroups(
  rawTreeOrGroups: unknown,
  family: 'gemini' | 'interactions',
  etag?: string
): GeminiKeyConfig[];
export function normalizeV8ProviderGroups(
  rawTreeOrGroups: unknown,
  family: 'vertex' | 'codex' | 'claude' | 'xai' | 'meta',
  etag?: string
): ProviderKeyConfig[];
export function normalizeV8ProviderGroups(
  rawTreeOrGroups: unknown,
  family: ProviderV8Family,
  etag?: string
): Array<GeminiKeyConfig | ProviderKeyConfig | OpenAIProviderConfig>;
export function normalizeV8ProviderGroups(
  rawTreeOrGroups: unknown,
  family: ProviderV8Family,
  etag?: string
): Array<GeminiKeyConfig | ProviderKeyConfig | OpenAIProviderConfig> {
  const groups = extractRawV8FamilyGroups(rawTreeOrGroups, family);
  const normalizedEtag = normalizeV8Etag(etag);

  if (family === 'openai-compatibility') {
    return groups.flatMap((group, groupIndex) => {
      const normalized = normalizeOpenAIProvider(group);
      if (!normalized) return [];
      const v8Source: ProviderV8GroupSource = {
        family: 'openai-compatibility',
        groupIndex,
        group,
        groups,
        ...(normalizedEtag ? { etag: normalizedEtag } : {}),
      };
      return [{ ...normalized, v8Source }];
    });
  }

  return groups.flatMap((group, groupIndex) => {
    if (!Array.isArray(group.keys)) return [];
    const groupName =
      typeof group.name === 'string' && group.name.trim() ? group.name.trim() : undefined;
    const groupBaseUrl = group['base-url'] ?? group.baseUrl ?? group['base_url'];
    const allowEndpointOnly =
      Boolean(ENDPOINT_ONLY_V8_FAMILIES[family]) &&
      typeof groupBaseUrl === 'string' &&
      Boolean(groupBaseUrl.trim());

    return group.keys.flatMap((key, keyIndex) => {
      if (!isRecord(key)) return [];
      const effective: Record<string, unknown> = { ...group };
      delete effective.keys;
      delete effective['auth-index'];
      delete effective.authIndex;
      delete effective['auth_index'];

      Object.entries(key).forEach(([field, value]) => {
        if (value !== null && value !== undefined) {
          effective[field] = value;
        } else if (
          value === null &&
          (field === 'disable-cooling' ||
            field === 'disableCooling' ||
            field === 'disable_cooling') &&
          group['disable-cooling'] === undefined &&
          group.disableCooling === undefined &&
          group['disable_cooling'] === undefined
        ) {
          effective[field] = null;
        }
      });

      if (groupName) {
        effective.name = groupName;
      } else {
        delete effective.name;
      }

      const normalized =
        family === 'gemini' || family === 'interactions'
          ? normalizeGeminiKeyConfig(effective, allowEndpointOnly)
          : normalizeProviderKeyConfig(effective, allowEndpointOnly);
      if (!normalized) return [];

      const v8Source: ProviderV8GroupSource = {
        family,
        groupIndex,
        keyIndex,
        group,
        groups,
        ...(normalizedEtag ? { etag: normalizedEtag } : {}),
      };
      return [
        {
          ...normalized,
          ...(groupName ? { name: groupName } : {}),
          v8Source,
        },
      ];
    });
  });
}

const normalizeOauthExcluded = (payload: unknown): Record<string, string[]> | undefined => {
  if (!isRecord(payload)) return undefined;
  const source = payload['oauth-excluded-models'] ?? payload.items ?? payload;
  if (!isRecord(source)) return undefined;
  const map: Record<string, string[]> = {};
  Object.entries(source).forEach(([provider, models]) => {
    const key = String(provider || '').trim();
    if (!key) return;
    const normalized = normalizeExcludedModels(models);
    map[key.toLowerCase()] = normalized;
  });
  return map;
};

/**
 * 规范化 /config 返回值
 */
export const normalizeConfigResponse = (raw: unknown): Config => {
  const config: Config = { raw: isRecord(raw) ? raw : {} };
  if (!isRecord(raw)) {
    return config;
  }

  const observability = isRecord(raw.observability) ? raw.observability : null;
  const observabilityLogs =
    observability && isRecord(observability.logs) ? observability.logs : null;
  const observabilityPprof =
    observability && isRecord(observability.pprof) ? observability.pprof : null;

  config.debug = normalizeBoolean(raw.debug ?? observabilityLogs?.debug);
  const proxyUrl = raw['proxy-url'] ?? raw.proxyUrl;
  config.proxyUrl =
    typeof proxyUrl === 'string'
      ? proxyUrl
      : proxyUrl === undefined || proxyUrl === null
        ? undefined
        : String(proxyUrl);
  const requestRetry = raw['request-retry'] ?? raw.requestRetry;
  if (typeof requestRetry === 'number' && Number.isFinite(requestRetry)) {
    config.requestRetry = requestRetry;
  } else if (typeof requestRetry === 'string' && requestRetry.trim() !== '') {
    const parsed = Number(requestRetry);
    if (Number.isFinite(parsed)) {
      config.requestRetry = parsed;
    }
  }

  const quota = raw['quota-exceeded'] ?? raw.quotaExceeded;
  if (isRecord(quota)) {
    config.quotaExceeded = {
      switchProject: normalizeBoolean(quota['switch-project'] ?? quota.switchProject),
      switchPreviewModel: normalizeBoolean(
        quota['switch-preview-model'] ?? quota.switchPreviewModel
      ),
      antigravityCredits: normalizeBoolean(
        quota['antigravity-credits'] ?? quota.antigravityCredits
      ),
    };
  }

  const clean = raw.clean;
  if (isRecord(clean)) {
    const threshold = normalizeNumber(
      clean['used_percent_threshold'] ??
        clean.usedPercentThreshold ??
        clean['used-percent-threshold']
    );
    config.clean = {
      baseUrl: normalizeString(clean['base_url'] ?? clean.baseUrl ?? clean['base-url']),
      token: normalizeString(clean.token),
      targetTypes: normalizeStringList(
        clean['target_types'] ?? clean.targetTypes ?? clean['target-types']
      ),
      targetType: normalizeString(clean['target_type'] ?? clean.targetType ?? clean['target-type']),
      workers: normalizeNumber(clean.workers),
      deleteWorkers: normalizeNumber(
        clean['delete_workers'] ?? clean.deleteWorkers ?? clean['delete-workers']
      ),
      timeout: normalizeNumber(clean.timeout),
      retries: normalizeNumber(clean.retries),
      userAgent: normalizeString(clean['user_agent'] ?? clean.userAgent ?? clean['user-agent']),
      xaiInferenceUserAgent: normalizeString(
        clean['xai_inference_user_agent'] ??
          clean.xaiInferenceUserAgent ??
          clean['xai-inference-user-agent']
      ),
      xaiInferenceEnabled: normalizeBoolean(
        clean['xai_inference_enabled'] ??
          clean.xaiInferenceEnabled ??
          clean['xai-inference-enabled']
      ),
      xaiInferenceModel: normalizeString(
        clean['xai_inference_model'] ?? clean.xaiInferenceModel ?? clean['xai-inference-model']
      ),
      xaiInferencePrompt: normalizeString(
        clean['xai_inference_prompt'] ?? clean.xaiInferencePrompt ?? clean['xai-inference-prompt']
      ),
      usedPercentThreshold: threshold,
      sampleSize: normalizeNumber(clean['sample_size'] ?? clean.sampleSize ?? clean['sample-size']),
    };
  }

  config.usageStatisticsEnabled = normalizeBoolean(
    raw['usage-statistics-enabled'] ?? raw.usageStatisticsEnabled
  );
  config.redisUsageQueueRetentionSeconds = normalizeNumber(
    raw['redis-usage-queue-retention-seconds'] ?? raw.redisUsageQueueRetentionSeconds
  );
  config.requestLog = normalizeBoolean(
    raw['request-log'] ?? raw.requestLog ?? observabilityLogs?.['request-log']
  );
  config.loggingToFile = normalizeBoolean(
    raw['logging-to-file'] ?? raw.loggingToFile ?? observabilityLogs?.['logging-to-file']
  );
  const logsMaxTotalSizeMb =
    raw['logs-max-total-size-mb'] ??
    raw.logsMaxTotalSizeMb ??
    observabilityLogs?.['logs-max-total-size-mb'];
  if (typeof logsMaxTotalSizeMb === 'number' && Number.isFinite(logsMaxTotalSizeMb)) {
    config.logsMaxTotalSizeMb = logsMaxTotalSizeMb;
  } else if (typeof logsMaxTotalSizeMb === 'string' && logsMaxTotalSizeMb.trim() !== '') {
    const parsed = Number(logsMaxTotalSizeMb);
    if (Number.isFinite(parsed)) {
      config.logsMaxTotalSizeMb = parsed;
    }
  }
  config.errorLogsMaxFiles = normalizeNumber(
    raw['error-logs-max-files'] ??
      raw.errorLogsMaxFiles ??
      observabilityLogs?.['error-logs-max-files']
  );
  const pprof = isRecord(raw.pprof) ? raw.pprof : observabilityPprof;
  if (pprof) {
    config.pprofEnable = normalizeBoolean(pprof.enable);
    config.pprofAddr = normalizeString(pprof.addr);
  }
  const plugins = raw.plugins;
  if (isRecord(plugins)) {
    config.pluginsEnabled = normalizeBoolean(plugins.enabled);
  } else {
    config.pluginsEnabled = normalizeBoolean(raw['plugins-enabled'] ?? raw.pluginsEnabled);
  }
  config.wsAuth = normalizeBoolean(raw['ws-auth'] ?? raw.wsAuth);
  config.forceModelPrefix = normalizeBoolean(raw['force-model-prefix'] ?? raw.forceModelPrefix);
  const routing = raw.routing;
  const strategyRaw = isRecord(routing)
    ? (routing.strategy ?? routing['strategy'])
    : (raw['routing-strategy'] ?? raw.routingStrategy);
  if (strategyRaw !== undefined && strategyRaw !== null) {
    config.routingStrategy = String(strategyRaw);
  }
  const cooldownRaw = isRecord(routing) && isRecord(routing.cooldown) ? routing.cooldown : null;
  const globalDisableCooling = normalizeBoolean(
    cooldownRaw
      ? (cooldownRaw['disable-cooling'] ??
          cooldownRaw.disableCooling ??
          cooldownRaw['disable_cooling'])
      : (raw['disable-cooling'] ?? raw.disableCooling ?? raw['disable_cooling'])
  );
  if (globalDisableCooling !== undefined) {
    config.disableCooling = globalDisableCooling;
  }
  const apiKeysRaw = raw['api-keys'] ?? raw.apiKeys;
  const accessRaw = isRecord(raw.access) ? raw.access : null;
  const clientApiKeysRaw = Array.isArray(apiKeysRaw)
    ? apiKeysRaw
    : accessRaw && Array.isArray(accessRaw['api-keys'])
      ? accessRaw['api-keys']
      : null;
  if (clientApiKeysRaw) {
    config.apiKeys = clientApiKeysRaw
      .map((key) => String(key))
      .filter((key) => key.trim() !== '');
  }
  const v8ApiKeysMap = isRecord(apiKeysRaw) ? apiKeysRaw : null;

  const geminiList = raw['gemini-api-key'] ?? raw.geminiApiKey ?? raw.geminiApiKeys;
  if (v8ApiKeysMap && Array.isArray(v8ApiKeysMap.gemini)) {
    config.geminiApiKeys = normalizeV8ProviderGroups(v8ApiKeysMap, 'gemini');
  } else if (Array.isArray(geminiList)) {
    config.geminiApiKeys = geminiList
      .map((item) => normalizeGeminiKeyConfig(item))
      .filter(Boolean) as GeminiKeyConfig[];
  }

  const interactionsList =
    raw['interactions-api-key'] ?? raw.interactionsApiKey ?? raw.interactionsApiKeys;
  if (v8ApiKeysMap && Array.isArray(v8ApiKeysMap.interactions)) {
    config.interactionsApiKeys = normalizeV8ProviderGroups(v8ApiKeysMap, 'interactions');
  } else if (Array.isArray(interactionsList)) {
    config.interactionsApiKeys = interactionsList
      .map((item) => normalizeGeminiKeyConfig(item))
      .filter(Boolean) as GeminiKeyConfig[];
  }

  const codexList = raw['codex-api-key'] ?? raw.codexApiKey ?? raw.codexApiKeys;
  if (v8ApiKeysMap && Array.isArray(v8ApiKeysMap.codex)) {
    config.codexApiKeys = normalizeV8ProviderGroups(v8ApiKeysMap, 'codex');
  } else if (Array.isArray(codexList)) {
    config.codexApiKeys = codexList
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const xaiList = raw['xai-api-key'] ?? raw.xaiApiKey ?? raw.xaiApiKeys;
  if (v8ApiKeysMap && Array.isArray(v8ApiKeysMap.xai)) {
    config.xaiApiKeys = normalizeV8ProviderGroups(v8ApiKeysMap, 'xai');
  } else if (Array.isArray(xaiList)) {
    config.xaiApiKeys = xaiList
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const claudeList = raw['claude-api-key'] ?? raw.claudeApiKey ?? raw.claudeApiKeys;
  if (v8ApiKeysMap && Array.isArray(v8ApiKeysMap.claude)) {
    config.claudeApiKeys = normalizeV8ProviderGroups(v8ApiKeysMap, 'claude');
  } else if (Array.isArray(claudeList)) {
    config.claudeApiKeys = claudeList
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const vertexList = raw['vertex-api-key'] ?? raw.vertexApiKey ?? raw.vertexApiKeys;
  if (v8ApiKeysMap && Array.isArray(v8ApiKeysMap.vertex)) {
    config.vertexApiKeys = normalizeV8ProviderGroups(v8ApiKeysMap, 'vertex');
  } else if (Array.isArray(vertexList)) {
    config.vertexApiKeys = vertexList
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const metaList = raw['meta-api-key'] ?? raw.metaApiKey ?? raw.metaApiKeys;
  if (v8ApiKeysMap && Array.isArray(v8ApiKeysMap.meta)) {
    config.metaApiKeys = normalizeV8ProviderGroups(v8ApiKeysMap, 'meta');
  } else if (Array.isArray(metaList)) {
    config.metaApiKeys = metaList
      .map((item) => {
        const hasBaseUrl =
          isRecord(item) &&
          typeof (item['base-url'] ?? item.baseUrl) === 'string' &&
          Boolean(String(item['base-url'] ?? item.baseUrl).trim());
        return normalizeProviderKeyConfig(item, hasBaseUrl);
      })
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const openaiList =
    raw['openai-compatibility'] ?? raw.openaiCompatibility ?? raw.openAICompatibility;
  if (v8ApiKeysMap && Array.isArray(v8ApiKeysMap['openai-compatibility'])) {
    config.openaiCompatibility = normalizeV8ProviderGroups(
      v8ApiKeysMap,
      'openai-compatibility'
    );
  } else if (Array.isArray(openaiList)) {
    config.openaiCompatibility = openaiList
      .map((item) => normalizeOpenAIProvider(item))
      .filter(Boolean) as OpenAIProviderConfig[];
  }
  const oauthExcluded = normalizeOauthExcluded(
    raw['oauth-excluded-models'] ?? raw.oauthExcludedModels
  );
  if (oauthExcluded) {
    config.oauthExcludedModels = oauthExcluded;
  }

  return config;
};

export {
  normalizeApiKeyEntry,
  normalizeGeminiKeyConfig,
  normalizeModelAliases,
  normalizeOpenAIProvider,
  normalizeProviderKeyConfig,
  normalizeHeaders,
  normalizeExcludedModels,
};
