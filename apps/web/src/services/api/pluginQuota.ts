import axios from 'axios';
import type { AuthFileItem } from '@/types';
import { normalizeUsageServiceBase } from './usageService';

const USAGE_SERVICE_TIMEOUT_MS = 30_000;

export interface PluginQuotaProviderDescriptor {
  plugin_id?: string;
  provider?: string;
  display_name?: string;
  supported_providers?: string[];
  supports_reset?: boolean;
}

export interface PluginQuotaProviderResponse {
  providers?: PluginQuotaProviderDescriptor[];
}

export interface PluginQuotaCredentialResponse {
  auth_index?: string;
  auth_file_name?: string;
  provider?: string;
  plugin_id?: string;
  display_name?: string;
  supports_reset?: boolean;
  observed_at_ms?: number;
  items?: Array<{
    key?: string;
    label?: string;
    value?: number | string | boolean | null;
    unit?: string;
    format?: string;
    currency?: string;
  }>;
}

const buildUrl = (base: string, path: string): string =>
  `${normalizeUsageServiceBase(base).replace(/\/+$/, '')}${path}`;

const authHeaders = (managementKey?: string) =>
  managementKey ? { Authorization: `Bearer ${managementKey}` } : undefined;

/**
 * The plugin quota routes belong to the Manager Server, not to CPA: the server
 * resolves the plugin provider catalogue, keys the observation on CPA's auth
 * index and persists the snapshot evidence the panel later reads back.
 */
export const pluginQuotaApi = {
  providers: async (
    base: string,
    managementKey?: string,
    signal?: AbortSignal
  ): Promise<PluginQuotaProviderResponse> => {
    const response = await axios.get<PluginQuotaProviderResponse>(
      buildUrl(base, '/v0/management/plugin-quota/providers'),
      { timeout: USAGE_SERVICE_TIMEOUT_MS, headers: authHeaders(managementKey), signal }
    );
    return response.data;
  },
  credential: async (
    base: string,
    managementKey: string | undefined,
    authIndex: string,
    signal?: AbortSignal
  ): Promise<PluginQuotaCredentialResponse> => {
    const response = await axios.get<PluginQuotaCredentialResponse>(
      buildUrl(base, '/v0/management/plugin-quota/credential'),
      {
        timeout: USAGE_SERVICE_TIMEOUT_MS,
        headers: authHeaders(managementKey),
        params: { auth_index: authIndex },
        signal,
      }
    );
    return response.data;
  },
  refresh: async (
    base: string,
    managementKey?: string,
    authIndex?: string,
    signal?: AbortSignal
  ): Promise<void> => {
    await axios.post(
      buildUrl(base, '/v0/management/plugin-quota/refresh'),
      undefined,
      {
        timeout: USAGE_SERVICE_TIMEOUT_MS,
        headers: authHeaders(managementKey),
        params: authIndex ? { auth_index: authIndex } : undefined,
        signal,
      }
    );
  },
};

export const readAuthFileQuotaProvider = (file: AuthFileItem | undefined): string => {
  if (!file) return '';
  const raw = file['quota_provider'] ?? file['quotaProvider'];
  return typeof raw === 'string' ? raw.trim() : '';
};
