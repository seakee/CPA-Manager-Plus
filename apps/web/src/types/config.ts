/**
 * 配置相关类型定义
 * 与基线 /config 返回结构保持一致（内部使用驼峰形式）
 */

import type { GeminiKeyConfig, ProviderKeyConfig, OpenAIProviderConfig } from './provider';

export interface QuotaExceededConfig {
  switchProject?: boolean;
  switchPreviewModel?: boolean;
  antigravityCredits?: boolean;
}

export interface AuthPoolCleanConfig {
  baseUrl?: string;
  token?: string;
  targetTypes?: string[];
  targetType?: string;
  workers?: number;
  deleteWorkers?: number;
  timeout?: number;
  retries?: number;
  userAgent?: string;
  xaiInferenceUserAgent?: string;
  xaiInferenceEnabled?: boolean;
  xaiInferenceModel?: string;
  xaiInferencePrompt?: string;
  usedPercentThreshold?: number;
  sampleSize?: number;
}

export interface Config {
  debug?: boolean;
  proxyUrl?: string;
  requestRetry?: number;
  quotaExceeded?: QuotaExceededConfig;
  clean?: AuthPoolCleanConfig;
  usageStatisticsEnabled?: boolean;
  redisUsageQueueRetentionSeconds?: number;
  requestLog?: boolean;
  loggingToFile?: boolean;
  logsMaxTotalSizeMb?: number;
  pluginsEnabled?: boolean;
  wsAuth?: boolean;
  forceModelPrefix?: boolean;
  routingStrategy?: string;
  apiKeys?: string[];
  geminiApiKeys?: GeminiKeyConfig[];
  interactionsApiKeys?: GeminiKeyConfig[];
  codexApiKeys?: ProviderKeyConfig[];
  xaiApiKeys?: ProviderKeyConfig[];
  claudeApiKeys?: ProviderKeyConfig[];
  vertexApiKeys?: ProviderKeyConfig[];
  openaiCompatibility?: OpenAIProviderConfig[];
  oauthExcludedModels?: Record<string, string[]>;
  raw?: Record<string, unknown>;
}

export type RawConfigSection =
  | 'debug'
  | 'proxy-url'
  | 'request-retry'
  | 'quota-exceeded'
  | 'usage-statistics-enabled'
  | 'redis-usage-queue-retention-seconds'
  | 'request-log'
  | 'logging-to-file'
  | 'logs-max-total-size-mb'
  | 'plugins'
  | 'ws-auth'
  | 'force-model-prefix'
  | 'routing/strategy'
  | 'api-keys'
  | 'gemini-api-key'
  | 'interactions-api-key'
  | 'codex-api-key'
  | 'xai-api-key'
  | 'claude-api-key'
  | 'vertex-api-key'
  | 'openai-compatibility'
  | 'oauth-excluded-models';

export interface ConfigCache {
  data: Config;
  timestamp: number;
}
