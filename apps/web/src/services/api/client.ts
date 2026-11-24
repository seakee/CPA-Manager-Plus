/**
 * Axios API 客户端
 * 替代原项目 src/core/api-client.js
 */

import axios, { AxiosInstance, AxiosRequestConfig, AxiosResponse } from 'axios';
import type { ApiClientConfig, ApiError } from '@/types';
import {
  BUILD_DATE_HEADER_KEYS,
  COMMIT_HEADER_KEYS,
  CPA_SUPPORT_PLUGIN_HEADER_KEYS,
  REQUEST_TIMEOUT_MS,
  VERSION_HEADER_KEYS,
} from '@/utils/constants';
import { computeApiUrl, computeV8ApiUrl } from '@/utils/connection';
import { isRecord } from '@/utils/helpers';
import { assertPanelManagementTarget } from '@/panel/managementTarget';

export type ApiClientRequestScope = Pick<ApiClientConfig, 'apiBase' | 'managementKey'>;

export type ScopedApiRequestConfig = AxiosRequestConfig & {
  cpampScopedRequest?: true;
  cpampV8Namespace?: true;
};

export const toV8ManagementBaseUrl = (apiBase: string): string => {
  const normalized = String(apiBase ?? '')
    .trim()
    .replace(/\/+$/, '');
  if (!normalized) return '';
  return normalized.replace(/\/v0\/management$/i, '/v8/management');
};

export const createScopedApiRequestConfig = (
  scope: ApiClientRequestScope
): ScopedApiRequestConfig => ({
  baseURL: computeApiUrl(scope.apiBase),
  headers: scope.managementKey
    ? {
        Authorization: `Bearer ${scope.managementKey}`,
      }
    : {},
  cpampScopedRequest: true,
});

export const createScopedV8ApiRequestConfig = (
  scope: ApiClientRequestScope
): ScopedApiRequestConfig => ({
  baseURL: computeV8ApiUrl(scope.apiBase),
  headers: scope.managementKey
    ? {
        Authorization: `Bearer ${scope.managementKey}`,
      }
    : {},
  cpampScopedRequest: true,
  cpampV8Namespace: true,
});

class ApiClient {
  private instance: AxiosInstance;
  private apiBase: string = '';
  private managementKey: string = '';
  private connectionEpoch: number = 0;
  constructor() {
    this.instance = axios.create({
      timeout: REQUEST_TIMEOUT_MS,
      headers: {
        'Content-Type': 'application/json',
      },
    });

    this.setupInterceptors();
  }

  /**
   * 设置 API 配置
   */
  setConfig(config: ApiClientConfig): void {
    const nextApiBase = computeApiUrl(config.apiBase);
    const nextManagementKey = config.managementKey;
    if (nextApiBase !== this.apiBase || nextManagementKey !== this.managementKey) {
      this.connectionEpoch += 1;
    }
    this.apiBase = nextApiBase;
    this.managementKey = nextManagementKey;

    if (config.timeout) {
      this.instance.defaults.timeout = config.timeout;
    } else {
      this.instance.defaults.timeout = REQUEST_TIMEOUT_MS;
    }
  }

  getConnectionEpoch(): number {
    return this.connectionEpoch;
  }

  getConnectionScope(): ApiClientRequestScope {
    return {
      apiBase: this.apiBase,
      managementKey: this.managementKey,
    };
  }

  createCurrentScopedConfig(): ScopedApiRequestConfig {
    return createScopedApiRequestConfig(this.getConnectionScope());
  }

  createCurrentScopedV8Config(): ScopedApiRequestConfig {
    return createScopedV8ApiRequestConfig(this.getConnectionScope());
  }

  private readHeader(headers: Record<string, unknown> | undefined, keys: string[]): string | null {
    if (!headers) return null;

    const normalizeValue = (value: unknown): string | null => {
      if (value === undefined || value === null) return null;
      if (Array.isArray(value)) {
        const first = value.find(
          (entry) => entry !== undefined && entry !== null && String(entry).trim()
        );
        return first !== undefined ? String(first) : null;
      }
      const text = String(value);
      return text ? text : null;
    };

    const headerGetter = (headers as { get?: (name: string) => unknown }).get;
    if (typeof headerGetter === 'function') {
      for (const key of keys) {
        const match = normalizeValue(headerGetter.call(headers, key));
        if (match) return match;
      }
    }

    const entries =
      typeof (headers as { entries?: () => Iterable<[string, unknown]> }).entries === 'function'
        ? Array.from((headers as { entries: () => Iterable<[string, unknown]> }).entries())
        : Object.entries(headers);

    const normalized = Object.fromEntries(
      entries.map(([key, value]) => [String(key).toLowerCase(), value])
    );
    for (const key of keys) {
      const match = normalizeValue(normalized[key.toLowerCase()]);
      if (match) return match;
    }
    return null;
  }

  private readBooleanHeader(
    headers: Record<string, unknown> | undefined,
    keys: string[]
  ): boolean | null {
    const value = this.readHeader(headers, keys);
    if (value === null) return null;

    const normalized = value.trim().toLowerCase();
    if (['1', 'true', 'yes', 'on'].includes(normalized)) return true;
    if (['0', 'false', 'no', 'off'].includes(normalized)) return false;
    return null;
  }

  private requestTargetsCurrentConfig(config?: AxiosRequestConfig): boolean {
    const stripManagementSuffix = (value: string) =>
      value.replace(/\/+$/, '').replace(/\/v(?:0|8)\/management$/i, '');
    const requestBase = stripManagementSuffix(String(config?.baseURL ?? ''));
    const currentBase = stripManagementSuffix(this.apiBase);
    const requestAuthorization = this.readHeader(
      config?.headers as Record<string, unknown> | undefined,
      ['Authorization']
    );
    const currentAuthorization = this.managementKey ? `Bearer ${this.managementKey}` : '';

    return requestBase === currentBase && (requestAuthorization ?? '') === currentAuthorization;
  }

  /**
   * 设置请求/响应拦截器
   */
  private setupInterceptors(): void {
    // 请求拦截器
    this.instance.interceptors.request.use(
      (config) => {
        const scopedConfig = config as ScopedApiRequestConfig;
        const scopedRequest = scopedConfig.cpampScopedRequest === true;
        const v8Namespace = scopedConfig.cpampV8Namespace === true;
        // Explicitly scoped requests retain the connection captured when a multi-step
        // operation began. Regular requests continue to use the current global config.
        if (!scopedRequest) {
          config.baseURL = v8Namespace ? toV8ManagementBaseUrl(this.apiBase) : this.apiBase;
        } else if (v8Namespace) {
          config.baseURL = toV8ManagementBaseUrl(String(config.baseURL ?? ''));
        }
        if (config.url && !v8Namespace) {
          // Normalize deprecated Gemini endpoint to the current path.
          config.url = config.url.replace(/\/generative-language-api-key\b/g, '/gemini-api-key');
        }

        assertPanelManagementTarget(config.baseURL ?? '', config.url ?? '');

        // 添加认证头
        if (!scopedRequest && this.managementKey) {
          config.headers.Authorization = `Bearer ${this.managementKey}`;
        }

        return config;
      },
      (error) => Promise.reject(this.handleError(error))
    );

    // 响应拦截器
    this.instance.interceptors.response.use(
      (response) => {
        const headers = response.headers as Record<string, string | undefined>;
        const version = this.readHeader(headers, VERSION_HEADER_KEYS);
        const commit = this.readHeader(headers, COMMIT_HEADER_KEYS);
        const buildDate = this.readHeader(headers, BUILD_DATE_HEADER_KEYS);
        const supportsPlugin = this.readBooleanHeader(headers, CPA_SUPPORT_PLUGIN_HEADER_KEYS);
        const targetsCurrentConfig = this.requestTargetsCurrentConfig(response.config);

        // 触发版本更新事件（后续通过 store 处理）
        if (targetsCurrentConfig && (version || commit || buildDate)) {
          window.dispatchEvent(
            new CustomEvent('server-version-update', {
              detail: {
                version: version || null,
                commit: commit || null,
                buildDate: buildDate || null,
              },
            })
          );
        }
        if (targetsCurrentConfig && supportsPlugin !== null) {
          window.dispatchEvent(
            new CustomEvent('server-plugin-support-update', {
              detail: { supportsPlugin },
            })
          );
        }

        return response;
      },
      (error) => Promise.reject(this.handleError(error))
    );
  }

  /**
   * 错误处理
   */
  private handleError(error: unknown): ApiError {
    if (axios.isAxiosError(error)) {
      const responseData: unknown = error.response?.data;
      const responseRecord = isRecord(responseData) ? responseData : null;
      const errorValue = responseRecord?.error;
      const responseMessage =
        typeof responseRecord?.message === 'string' ? responseRecord.message.trim() : '';
      const errorText = typeof errorValue === 'string' ? errorValue.trim() : '';
      const isMachineErrorCode =
        Boolean(errorText) && /^[a-z0-9_.-]+$/i.test(errorText) && !errorText.includes(' ');
      const message =
        typeof errorValue === 'string'
          ? responseMessage
            ? !errorText || isMachineErrorCode || responseMessage === errorText
              ? responseMessage
              : `${errorText}: ${responseMessage}`
            : errorValue
          : isRecord(errorValue) && typeof errorValue.message === 'string'
            ? errorValue.message
            : typeof responseRecord?.message === 'string'
              ? responseRecord.message
              : error.message || 'Request failed';
      const rawHeaders: unknown = error.response?.headers;
      const responseHeaders = isRecord(rawHeaders) ? rawHeaders : undefined;
      const rawEtag = this.readHeader(responseHeaders, ['etag', 'ETag']);
      const trimmedEtag = rawEtag?.trim() ? rawEtag.trim() : undefined;
      const normalizedEtag =
        trimmedEtag && /^w\//i.test(trimmedEtag)
          ? trimmedEtag.slice(2).trim() || undefined
          : trimmedEtag;
      const apiError = new Error(message) as ApiError;
      apiError.name = 'ApiError';
      apiError.status = error.response?.status;
      apiError.code = error.code;
      apiError.details = responseData;
      apiError.data = responseData;
      apiError.headers = responseHeaders;
      apiError.etag = normalizedEtag;
      // 401 未授权 - 触发登出事件
      if (error.response?.status === 401 && this.requestTargetsCurrentConfig(error.config)) {
        window.dispatchEvent(new Event('unauthorized'));
      }

      return apiError;
    }

    const fallbackMessage =
      error instanceof Error
        ? error.message
        : typeof error === 'string'
          ? error
          : 'Unknown error occurred';
    const fallback = new Error(fallbackMessage) as ApiError;
    fallback.name = 'ApiError';
    return fallback;
  }

  /**
   * GET 请求
   */
  async get<T = unknown>(url: string, config?: AxiosRequestConfig): Promise<T> {
    const response = await this.instance.get<T>(url, config);
    return response.data;
  }

  /**
   * POST 请求
   */
  async post<T = unknown>(url: string, data?: unknown, config?: AxiosRequestConfig): Promise<T> {
    const response = await this.instance.post<T>(url, data, config);
    return response.data;
  }

  /**
   * PUT 请求
   */
  async put<T = unknown>(url: string, data?: unknown, config?: AxiosRequestConfig): Promise<T> {
    const response = await this.instance.put<T>(url, data, config);
    return response.data;
  }

  /**
   * PATCH 请求
   */
  async patch<T = unknown>(url: string, data?: unknown, config?: AxiosRequestConfig): Promise<T> {
    const response = await this.instance.patch<T>(url, data, config);
    return response.data;
  }

  /**
   * DELETE 请求
   */
  async delete<T = unknown>(url: string, config?: AxiosRequestConfig): Promise<T> {
    const response = await this.instance.delete<T>(url, config);
    return response.data;
  }

  /**
   * 获取原始响应（用于下载等场景）
   */
  async getRaw<T = BlobPart>(url: string, config?: AxiosRequestConfig): Promise<AxiosResponse<T>> {
    return this.instance.get<T>(url, config);
  }

  /**
   * CPA v8 Management GET 请求
   */
  async getV8<T = unknown>(url: string, config?: AxiosRequestConfig): Promise<T> {
    const response = await this.instance.get<T>(url, {
      ...(config || {}),
      cpampV8Namespace: true,
    } as ScopedApiRequestConfig);
    return response.data;
  }

  /**
   * CPA v8 Management 获取原始响应（含 ETag 等响应头）
   */
  async getRawV8<T = unknown>(url: string, config?: AxiosRequestConfig): Promise<AxiosResponse<T>> {
    return this.instance.get<T>(url, {
      ...(config || {}),
      cpampV8Namespace: true,
    } as ScopedApiRequestConfig);
  }

  /**
   * CPA v8 Management PUT 请求
   */
  async putV8<T = unknown>(
    url: string,
    data?: unknown,
    config?: AxiosRequestConfig
  ): Promise<T> {
    const response = await this.instance.put<T>(url, data, {
      ...(config || {}),
      cpampV8Namespace: true,
    } as ScopedApiRequestConfig);
    return response.data;
  }

  /**
   * CPA v8 Management PUT 请求（保留原始响应与 ETag 头）
   */
  async putRawV8<T = unknown>(
    url: string,
    data?: unknown,
    config?: AxiosRequestConfig
  ): Promise<AxiosResponse<T>> {
    return this.instance.put<T>(url, data, {
      ...(config || {}),
      cpampV8Namespace: true,
    } as ScopedApiRequestConfig);
  }

  /**
   * 发送 FormData
   */
  async postForm<T = unknown>(
    url: string,
    formData: FormData,
    config?: AxiosRequestConfig
  ): Promise<T> {
    const response = await this.instance.post<T>(url, formData, {
      ...config,
      headers: {
        ...(config?.headers || {}),
        'Content-Type': 'multipart/form-data',
      },
    });
    return response.data;
  }

  /**
   * 保留对 axios.request 的访问，便于下载等场景
   */
  async requestRaw(config: AxiosRequestConfig): Promise<AxiosResponse> {
    return this.instance.request(config);
  }
}

// 导出单例
export const apiClient = new ApiClient();
