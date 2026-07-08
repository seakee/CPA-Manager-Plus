import type { AxiosRequestConfig } from 'axios';
import type { TFunction } from 'i18next';
import type { AuthFileItem, CodexUsagePayload } from '@/types';
import {
  CODEX_RATE_LIMIT_RESET_CREDITS_CONSUME_URL,
  CODEX_USAGE_URL,
} from '@/utils/quota/constants';
import { buildCodexUsageRequestHeaders } from '@/utils/quota/codexRequestHeaders';
import { createStatusError } from '@/utils/quota/formatters';
import { normalizeAuthIndex, parseCodexUsagePayload } from '@/utils/quota/parsers';
import { fetchCodexQuota, type CodexQuotaData } from '@/utils/quota/providerRequests';
import { resolveCodexChatgptAccountId } from '@/utils/quota/resolvers';
import { apiCallApi, getApiCallErrorMessage, type ApiCallResult } from './apiCall';

export type CodexUsageRequestParams = {
  authIndex: string;
  accountId?: string | null;
  userAgent?: string;
  requestConfig?: AxiosRequestConfig;
};

export type CodexUsageRawResult = {
  result: ApiCallResult;
  payload: CodexUsagePayload | null;
};

export { buildCodexUsageRequestHeaders };

export const requestCodexUsageRaw = async ({
  authIndex,
  accountId,
  userAgent,
  requestConfig,
}: CodexUsageRequestParams): Promise<CodexUsageRawResult> => {
  const result = await apiCallApi.request(
    {
      authIndex,
      method: 'GET',
      url: CODEX_USAGE_URL,
      header: buildCodexUsageRequestHeaders(accountId, { userAgent }),
    },
    requestConfig
  );

  return {
    result,
    payload: parseCodexUsagePayload(result.body ?? result.bodyText),
  };
};

export const requestCodexUsagePayload = async (
  params: CodexUsageRequestParams,
  options: { emptyMessage?: string } = {}
): Promise<CodexUsagePayload> => {
  const { result, payload } = await requestCodexUsageRaw(params);
  if (result.statusCode < 200 || result.statusCode >= 300) {
    throw new Error(getApiCallErrorMessage(result));
  }
  if (!payload) {
    throw new Error(options.emptyMessage || 'No Codex quota data available');
  }
  return payload;
};

export const createCodexRedeemRequestId = () => {
  if (typeof globalThis.crypto?.randomUUID === 'function') {
    return globalThis.crypto.randomUUID();
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
};

export const consumeCodexRateLimitResetCredit = async (
  file: AuthFileItem,
  t?: TFunction
): Promise<ApiCallResult> => {
  const rawAuthIndex = file['auth_index'] ?? file.authIndex;
  const authIndex = normalizeAuthIndex(rawAuthIndex);
  if (!authIndex) {
    throw new Error(t?.('codex_quota.missing_auth_index') ?? 'Auth file missing auth_index');
  }

  const accountId = resolveCodexChatgptAccountId(file);
  const result = await apiCallApi.request({
    authIndex,
    method: 'POST',
    url: CODEX_RATE_LIMIT_RESET_CREDITS_CONSUME_URL,
    header: buildCodexUsageRequestHeaders(accountId),
    data: JSON.stringify({
      redeem_request_id: createCodexRedeemRequestId(),
    }),
  });

  if (result.statusCode < 200 || result.statusCode >= 300) {
    throw createStatusError(getApiCallErrorMessage(result), result.statusCode);
  }

  return result;
};

export const resetCodexQuota = async (
  file: AuthFileItem,
  t: TFunction
): Promise<CodexQuotaData> => {
  await consumeCodexRateLimitResetCredit(file, t);
  return fetchCodexQuota(file, t);
};
