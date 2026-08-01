import type {
  ApiKeyEntry,
  CloakConfig,
  GeminiKeyConfig,
  ModelAlias,
  OpenAIProviderConfig,
  ProviderKeyConfig,
} from '@/types';
import type { Config } from '@/types/config';
import { buildHeaderObject } from '@/utils/headers';

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
  }
  return Boolean(value);
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

      const name = item.name || item.id || item.model;
      if (!name) return null;
      const alias = item.alias || item.display_name || item.displayName;
      const priority = item.priority ?? item['priority'];
      const testModel = item['test-model'] ?? item.testModel;
      const entry: ModelAlias = { name: String(name) };
      if (alias && alias !== name) {
        entry.alias = String(alias);
      }
      if (priority !== undefined) {
        const parsed = Number(priority);
        if (Number.isFinite(parsed)) {
          entry.priority = parsed;
        }
      }
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

const normalizeApiKeyEntry = (entry: unknown): ApiKeyEntry | null => {
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
  if (!trimmed && !authIndex) return null;

  const proxyUrl = record ? (record['proxy-url'] ?? record.proxyUrl) : undefined;
  const headers = record ? normalizeHeaders(record.headers) : undefined;

  const result: ApiKeyEntry = {
    apiKey: trimmed,
    proxyUrl: proxyUrl ? String(proxyUrl) : undefined,
    headers,
  };
  if (authIndex) result.authIndex = authIndex;
  return result;
};

const normalizeProviderKeyConfig = (item: unknown): ProviderKeyConfig | null => {
  if (item === undefined || item === null) return null;
  const record = isRecord(item) ? item : null;
  const apiKey = record?.['api-key'] ?? record?.apiKey ?? (typeof item === 'string' ? item : '');
  const trimmed = String(apiKey || '').trim();
  const authIndex = normalizeAuthIndex(
    record?.['auth-index'] ?? record?.authIndex ?? record?.['auth_index']
  );
  if (!trimmed && !authIndex) return null;

  const config: ProviderKeyConfig = { apiKey: trimmed };
  const priority = record?.priority ?? record?.['priority'];
  if (priority !== undefined && priority !== null && String(priority).trim() !== '') {
    const parsed = Number(priority);
    if (Number.isFinite(parsed)) {
      config.priority = parsed;
    }
  }
  const prefix = normalizePrefix(record?.prefix ?? record?.['prefix']);
  if (prefix) config.prefix = prefix;
  const baseUrl = record ? (record['base-url'] ?? record.baseUrl) : undefined;
  const proxyUrl = record ? (record['proxy-url'] ?? record.proxyUrl) : undefined;
  if (baseUrl) config.baseUrl = String(baseUrl);
  const websockets = normalizeBoolean(record?.websockets ?? record?.['websockets']);
  if (websockets !== undefined) config.websockets = websockets;
  const disableCooling = normalizeBoolean(
    record?.['disable-cooling'] ?? record?.disableCooling ?? record?.disable_cooling
  );
  if (disableCooling !== undefined) config.disableCooling = disableCooling;
  const experimentalCchSigning = normalizeBoolean(
    record?.['experimental-cch-signing'] ??
      record?.experimentalCchSigning ??
      record?.experimental_cch_signing
  );
  if (experimentalCchSigning !== undefined) config.experimentalCchSigning = experimentalCchSigning;
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
    const sensitiveWords = normalizeExcludedModels(
      cloakRaw['sensitive-words'] ?? cloakRaw.sensitiveWords ?? cloakRaw.sensitive_words
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
    if (Object.keys(cloak).length) {
      config.cloak = cloak;
    }
  }

  return config;
};

const normalizeGeminiKeyConfig = (item: unknown): GeminiKeyConfig | null => {
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
  if (!trimmed && !authIndex) return null;

  const config: GeminiKeyConfig = { apiKey: trimmed };
  const priority = record?.priority ?? record?.['priority'];
  if (priority !== undefined && priority !== null && String(priority).trim() !== '') {
    const parsed = Number(priority);
    if (Number.isFinite(parsed)) {
      config.priority = parsed;
    }
  }
  const prefix = normalizePrefix(record?.prefix ?? record?.['prefix']);
  if (prefix) config.prefix = prefix;
  const baseUrl = record ? (record['base-url'] ?? record.baseUrl ?? record['base_url']) : undefined;
  if (baseUrl) config.baseUrl = String(baseUrl);
  const proxyUrl = record
    ? (record['proxy-url'] ?? record.proxyUrl ?? record['proxy_url'])
    : undefined;
  if (proxyUrl) config.proxyUrl = String(proxyUrl);
  const disableCooling = normalizeBoolean(
    record?.['disable-cooling'] ?? record?.disableCooling ?? record?.disable_cooling
  );
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
  if (Array.isArray(provider['api-key-entries'])) {
    apiKeyEntries = provider['api-key-entries']
      .map((entry) => normalizeApiKeyEntry(entry))
      .filter(Boolean) as ApiKeyEntry[];
  } else if (Array.isArray(provider['api-keys'])) {
    apiKeyEntries = provider['api-keys']
      .map((key) => normalizeApiKeyEntry({ 'api-key': key }))
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
  const disableCooling = normalizeBoolean(
    provider['disable-cooling'] ?? provider.disableCooling ?? provider.disable_cooling
  );
  if (disableCooling !== undefined) result.disableCooling = disableCooling;
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

  config.debug = normalizeBoolean(raw.debug);
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
  config.requestLog = normalizeBoolean(raw['request-log'] ?? raw.requestLog);
  config.loggingToFile = normalizeBoolean(raw['logging-to-file'] ?? raw.loggingToFile);
  const logsMaxTotalSizeMb = raw['logs-max-total-size-mb'] ?? raw.logsMaxTotalSizeMb;
  if (typeof logsMaxTotalSizeMb === 'number' && Number.isFinite(logsMaxTotalSizeMb)) {
    config.logsMaxTotalSizeMb = logsMaxTotalSizeMb;
  } else if (typeof logsMaxTotalSizeMb === 'string' && logsMaxTotalSizeMb.trim() !== '') {
    const parsed = Number(logsMaxTotalSizeMb);
    if (Number.isFinite(parsed)) {
      config.logsMaxTotalSizeMb = parsed;
    }
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
  const apiKeysRaw = raw['api-keys'] ?? raw.apiKeys;
  if (Array.isArray(apiKeysRaw)) {
    config.apiKeys = apiKeysRaw.map((key) => String(key)).filter((key) => key.trim() !== '');
  }

  const geminiList = raw['gemini-api-key'] ?? raw.geminiApiKey ?? raw.geminiApiKeys;
  if (Array.isArray(geminiList)) {
    config.geminiApiKeys = geminiList
      .map((item) => normalizeGeminiKeyConfig(item))
      .filter(Boolean) as GeminiKeyConfig[];
  }

  const interactionsList =
    raw['interactions-api-key'] ?? raw.interactionsApiKey ?? raw.interactionsApiKeys;
  if (Array.isArray(interactionsList)) {
    config.interactionsApiKeys = interactionsList
      .map((item) => normalizeGeminiKeyConfig(item))
      .filter(Boolean) as GeminiKeyConfig[];
  }

  const codexList = raw['codex-api-key'] ?? raw.codexApiKey ?? raw.codexApiKeys;
  if (Array.isArray(codexList)) {
    config.codexApiKeys = codexList
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const xaiList = raw['xai-api-key'] ?? raw.xaiApiKey ?? raw.xaiApiKeys;
  if (Array.isArray(xaiList)) {
    config.xaiApiKeys = xaiList
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const claudeList = raw['claude-api-key'] ?? raw.claudeApiKey ?? raw.claudeApiKeys;
  if (Array.isArray(claudeList)) {
    config.claudeApiKeys = claudeList
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const vertexList = raw['vertex-api-key'] ?? raw.vertexApiKey ?? raw.vertexApiKeys;
  if (Array.isArray(vertexList)) {
    config.vertexApiKeys = vertexList
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const openaiList =
    raw['openai-compatibility'] ?? raw.openaiCompatibility ?? raw.openAICompatibility;
  if (Array.isArray(openaiList)) {
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
