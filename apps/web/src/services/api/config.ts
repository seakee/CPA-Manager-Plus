/**
 * 配置相关 API
 */

import { apiClient } from './client';
import type { Config } from '@/types';
import { isRecord } from '@/utils/helpers';
import { normalizeConfigResponse, normalizeV8ProviderGroups } from './transformers';
import { providersApi } from './providers';

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
const readErrorStatus = (error: unknown): number | undefined => {
  if (!isRecord(error)) return undefined;
  if (typeof error.status === 'number') return error.status;
  if (isRecord(error.response) && typeof error.response.status === 'number') {
    return error.response.status;
  }
  return undefined;
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

const isV8UnsupportedStatus = (error: unknown): boolean => {
  const status = readErrorStatus(error);
  return status === 404 || status === 405;
};

type V8ConfigOutcome =
  | { kind: 'ok'; data: unknown; headers: unknown; etag?: string }
  | { kind: 'empty'; etag?: string }
  | { kind: 'unsupported' };

export const configApi = {
  /**
   * 获取配置（会进行字段规范化，并在 CPA v8 可用时叠加 api-keys 别名与 authIndex）
   */
  async getConfig(options?: { syncLatestV8Etag?: boolean }): Promise<Config> {
    const requestEpoch =
      typeof apiClient.getConnectionEpoch === 'function'
        ? apiClient.getConnectionEpoch()
        : undefined;
    const v8WriteVersionBefore = providersApi?.getV8EtagMutationVersion?.();

    const v8Promise: Promise<V8ConfigOutcome> =
      typeof apiClient.getRawV8 === 'function'
        ? apiClient
            .getRawV8('/config/api-keys')
            .then(
              (response): V8ConfigOutcome => ({
                kind: 'ok',
                data: response.data,
                headers: response.headers,
                etag: readResponseEtag(response.headers),
              })
            )
            .catch((error: unknown): V8ConfigOutcome => {
              if (isV8MissingConfigPathError(error)) {
                return { kind: 'empty', etag: readErrorEtag(error) };
              }
              if (isV8UnsupportedStatus(error)) {
                return { kind: 'unsupported' };
              }
              throw error;
            })
        : Promise.resolve({ kind: 'unsupported' });

    const [raw, v8Outcome] = await Promise.all([apiClient.get('/config'), v8Promise]);
    const epochMatches =
      requestEpoch === undefined ||
      typeof apiClient.getConnectionEpoch !== 'function' ||
      apiClient.getConnectionEpoch() === requestEpoch;
    const v8VersionMatches =
      v8WriteVersionBefore === undefined ||
      v8WriteVersionBefore === providersApi?.getV8EtagMutationVersion?.();
    const shouldSyncLatestV8Etag =
      options?.syncLatestV8Etag !== false && epochMatches && v8VersionMatches;
    const normalized = normalizeConfigResponse(raw);

    if (v8Outcome.kind === 'ok') {
      const etag = v8Outcome.etag;
      if (shouldSyncLatestV8Etag) {
        providersApi?.setLatestV8ConfigEtag?.(etag, { passiveRead: true });
      }
      const v8ApiKeys = isRecord(v8Outcome.data) ? v8Outcome.data : {};
      normalized.v8Supported = true;
      if (etag) {
        normalized.v8Etag = etag;
      }
      normalized.geminiApiKeys = normalizeV8ProviderGroups(v8ApiKeys, 'gemini', etag);
      normalized.interactionsApiKeys = normalizeV8ProviderGroups(v8ApiKeys, 'interactions', etag);
      normalized.codexApiKeys = normalizeV8ProviderGroups(v8ApiKeys, 'codex', etag);
      normalized.xaiApiKeys = normalizeV8ProviderGroups(v8ApiKeys, 'xai', etag);
      normalized.claudeApiKeys = normalizeV8ProviderGroups(v8ApiKeys, 'claude', etag);
      normalized.vertexApiKeys = normalizeV8ProviderGroups(v8ApiKeys, 'vertex', etag);
      normalized.metaApiKeys = normalizeV8ProviderGroups(v8ApiKeys, 'meta', etag);
      normalized.openaiCompatibility = normalizeV8ProviderGroups(
        v8ApiKeys,
        'openai-compatibility',
        etag
      );
    } else if (v8Outcome.kind === 'empty') {
      const etag = v8Outcome.etag;
      if (shouldSyncLatestV8Etag) {
        providersApi?.setLatestV8ConfigEtag?.(etag, { passiveRead: true });
      }
      normalized.v8Supported = true;
      if (etag) {
        normalized.v8Etag = etag;
      }
      normalized.geminiApiKeys = [];
      normalized.interactionsApiKeys = [];
      normalized.codexApiKeys = [];
      normalized.xaiApiKeys = [];
      normalized.claudeApiKeys = [];
      normalized.vertexApiKeys = [];
      normalized.metaApiKeys = [];
      normalized.openaiCompatibility = [];
      if (isRecord(normalized.raw)) {
        normalized.raw['api-keys'] = {};
      }
    } else {
      if (shouldSyncLatestV8Etag) {
        providersApi?.setLatestV8ConfigEtag?.(undefined, { passiveRead: true });
      }
      normalized.v8Supported = false;
    }

    return normalized;
  },
  /**
   * 获取原始配置（不做转换）
   */
  getRawConfig: () => apiClient.get('/config'),

  /**
   * 更新 Debug 模式
   */
  updateDebug: (enabled: boolean) => apiClient.put('/debug', { value: enabled }),

  /**
   * 更新代理 URL
   */
  updateProxyUrl: (proxyUrl: string) => apiClient.put('/proxy-url', { value: proxyUrl }),

  /**
   * 清除代理 URL
   */
  clearProxyUrl: () => apiClient.delete('/proxy-url'),

  /**
   * 更新重试次数
   */
  updateRequestRetry: (retryCount: number) => apiClient.put('/request-retry', { value: retryCount }),

  /**
   * 配额回退：切换项目
   */
  updateSwitchProject: (enabled: boolean) =>
    apiClient.put('/quota-exceeded/switch-project', { value: enabled }),

  /**
   * 配额回退：切换预览模型
   */
  updateSwitchPreviewModel: (enabled: boolean) =>
    apiClient.put('/quota-exceeded/switch-preview-model', { value: enabled }),

  /**
   * 请求日志开关
   */
  updateRequestLog: (enabled: boolean) => apiClient.put('/request-log', { value: enabled }),

  /**
   * 写日志到文件开关
   */
  updateLoggingToFile: (enabled: boolean) => apiClient.put('/logging-to-file', { value: enabled }),

  /**
   * 获取日志总大小上限（MB）
   */
  async getLogsMaxTotalSizeMb(): Promise<number> {
    const data = await apiClient.get<Record<string, unknown>>('/logs-max-total-size-mb');
    const value = data?.['logs-max-total-size-mb'] ?? data?.logsMaxTotalSizeMb ?? 0;
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : 0;
  },

  /**
   * 更新日志总大小上限（MB）
   */
  updateLogsMaxTotalSizeMb: (value: number) =>
    apiClient.put('/logs-max-total-size-mb', { value }),

  /**
   * 获取错误日志保留文件数上限
   */
  async getErrorLogsMaxFiles(): Promise<number> {
    const data = await apiClient.get<Record<string, unknown>>('/error-logs-max-files');
    const value = data?.['error-logs-max-files'] ?? data?.errorLogsMaxFiles ?? 10;
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : 10;
  },

  /**
   * 更新错误日志保留文件数上限
   */
  updateErrorLogsMaxFiles: (value: number) =>
    apiClient.put('/error-logs-max-files', { value }),

  /**
   * WebSocket 鉴权开关
   */
  updateWsAuth: (enabled: boolean) => apiClient.put('/ws-auth', { value: enabled }),

  /**
   * 获取强制模型前缀开关
   */
  async getForceModelPrefix(): Promise<boolean> {
    const data = await apiClient.get<Record<string, unknown>>('/force-model-prefix');
    return Boolean(data?.['force-model-prefix'] ?? data?.forceModelPrefix ?? false);
  },

  /**
   * 更新强制模型前缀开关
   */
  updateForceModelPrefix: (enabled: boolean) => apiClient.put('/force-model-prefix', { value: enabled }),

  /**
   * 获取路由策略
   */
  async getRoutingStrategy(): Promise<string> {
    const data = await apiClient.get<Record<string, unknown>>('/routing/strategy');
    const strategy = data?.strategy ?? data?.['routing-strategy'] ?? data?.routingStrategy;
    return typeof strategy === 'string' ? strategy : 'round-robin';
  },

  /**
   * 更新路由策略
   */
  updateRoutingStrategy: (strategy: string) => apiClient.put('/routing/strategy', { value: strategy }),
};
