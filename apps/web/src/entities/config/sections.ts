import type { Config, RawConfigSection } from '@/types/config';

export const CONFIG_SECTION_KEYS: RawConfigSection[] = [
  'debug',
  'proxy-url',
  'request-retry',
  'quota-exceeded',
  'request-log',
  'logging-to-file',
  'logs-max-total-size-mb',
  'plugins',
  'ws-auth',
  'force-model-prefix',
  'routing/strategy',
  'api-keys',
  'gemini-api-key',
  'interactions-api-key',
  'codex-api-key',
  'xai-api-key',
  'claude-api-key',
  'vertex-api-key',
  'openai-compatibility',
  'oauth-excluded-models',
];

export const extractConfigSectionValue = (config: Config | null, section?: RawConfigSection) => {
  if (!config) return undefined;
  switch (section) {
    case 'debug':
      return config.debug;
    case 'proxy-url':
      return config.proxyUrl;
    case 'request-retry':
      return config.requestRetry;
    case 'quota-exceeded':
      return config.quotaExceeded;
    case 'request-log':
      return config.requestLog;
    case 'logging-to-file':
      return config.loggingToFile;
    case 'logs-max-total-size-mb':
      return config.logsMaxTotalSizeMb;
    case 'plugins':
      return config.pluginsEnabled === undefined
        ? config.raw?.plugins
        : { enabled: config.pluginsEnabled };
    case 'ws-auth':
      return config.wsAuth;
    case 'force-model-prefix':
      return config.forceModelPrefix;
    case 'routing/strategy':
      return config.routingStrategy;
    case 'api-keys':
      return config.apiKeys;
    case 'gemini-api-key':
      return config.geminiApiKeys;
    case 'interactions-api-key':
      return config.interactionsApiKeys;
    case 'codex-api-key':
      return config.codexApiKeys;
    case 'xai-api-key':
      return config.xaiApiKeys;
    case 'claude-api-key':
      return config.claudeApiKeys;
    case 'vertex-api-key':
      return config.vertexApiKeys;
    case 'openai-compatibility':
      return config.openaiCompatibility;
    case 'oauth-excluded-models':
      return config.oauthExcludedModels;
    default:
      if (!section) return undefined;
      return config.raw?.[section];
  }
};
