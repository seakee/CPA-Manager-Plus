/**
 * API 密钥管理
 */

import { apiClient } from './client';

const INVALID_API_KEY_LIST_RESPONSE = 'Invalid API key list response';

const isApiKeyListResponseRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const parseApiKeyListResponse = (data: unknown): string[] => {
  if (!isApiKeyListResponseRecord(data)) {
    throw new Error(INVALID_API_KEY_LIST_RESPONSE);
  }

  const hasKebabCaseField = Object.prototype.hasOwnProperty.call(data, 'api-keys');
  const hasCamelCaseField = Object.prototype.hasOwnProperty.call(data, 'apiKeys');
  if (!hasKebabCaseField && !hasCamelCaseField) {
    throw new Error(INVALID_API_KEY_LIST_RESPONSE);
  }

  const keys = hasKebabCaseField ? data['api-keys'] : data.apiKeys;
  if (keys == null) return [];
  if (!Array.isArray(keys)) {
    throw new Error(INVALID_API_KEY_LIST_RESPONSE);
  }

  if (!keys.every((key): key is string => typeof key === 'string')) {
    throw new Error(INVALID_API_KEY_LIST_RESPONSE);
  }

  return keys;
};

export const apiKeysApi = {
  async list(): Promise<string[]> {
    const data = await apiClient.get<unknown>('/api-keys');
    return parseApiKeyListResponse(data);
  },

  replace: (keys: string[]) => apiClient.put('/api-keys', keys),

  update: (index: number, value: string) => apiClient.patch('/api-keys', { index, value }),

  replaceValue: (oldValue: string, newValue: string) =>
    apiClient.patch('/api-keys', { old: oldValue, new: newValue }),

  delete: (index: number) => apiClient.delete(`/api-keys?index=${index}`),

  deleteValue: (value: string) => apiClient.delete(`/api-keys?value=${encodeURIComponent(value)}`),
};
