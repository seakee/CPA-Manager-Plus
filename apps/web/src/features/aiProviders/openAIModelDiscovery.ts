import { modelsApi } from '@/services/api';
import type { ApiKeyEntry } from '@/types';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { buildHeaderObject, hasHeader, type HeaderEntry } from '@/utils/headers';

type OpenAIModelDiscoveryOptions = {
  baseUrl: string;
  headers: HeaderEntry[];
  apiKeyEntries: ApiKeyEntry[];
  proxyRequiresSavedEntryMessage: string;
  fetchModels?: typeof modelsApi.fetchModelsViaApiCall;
};

export const discoverOpenAIModels = async ({
  baseUrl,
  headers,
  apiKeyEntries,
  proxyRequiresSavedEntryMessage,
  fetchModels = modelsApi.fetchModelsViaApiCall,
}: OpenAIModelDiscoveryOptions) => {
  const headerObject = buildHeaderObject(headers);
  const firstEntry = apiKeyEntries.find(
    (entry) =>
      entry.apiKey?.trim() ||
      normalizeAuthIndex(entry.authIndex) ||
      entry.proxyUrl?.trim() ||
      Object.keys(entry.headers ?? {}).length > 0
  );
  const firstKey = firstEntry?.apiKey?.trim();
  const authIndex = normalizeAuthIndex(firstEntry?.authIndex) ?? undefined;
  const resolvedHeaders = { ...headerObject, ...(firstEntry?.headers ?? {}) };

  if (firstEntry?.proxyUrl?.trim() && !authIndex) {
    throw new Error(proxyRequiresSavedEntryMessage);
  }

  const hasAuthHeader = hasHeader(resolvedHeaders, 'authorization');
  return fetchModels(
    baseUrl,
    hasAuthHeader ? undefined : firstKey,
    resolvedHeaders,
    authIndex
  );
};
