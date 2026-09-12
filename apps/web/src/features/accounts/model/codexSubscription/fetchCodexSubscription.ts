import { apiCallApi, getApiCallErrorMessage } from '@/services/api/apiCall';
import { CODEX_SUBSCRIPTIONS_URL } from '@/utils/quota/constants';
import { buildCodexUsageRequestHeaders } from '@/utils/quota/codexRequestHeaders';
import { normalizeAuthIndex } from '@/utils/quota/parsers';
import { parseSubscriptionsResponse } from './parseSubscriptionsResponse';
import type { CodexSubscriptionFetchInput, CodexSubscriptionRecord } from './types';

export class CodexSubscriptionFetchError extends Error {
  readonly errorKind: string;

  constructor(errorKind: string, message: string) {
    super(message);
    this.name = 'CodexSubscriptionFetchError';
    this.errorKind = errorKind;
  }
}

export const classifyCodexSubscriptionError = (error: unknown): string => {
  if (error instanceof CodexSubscriptionFetchError) return error.errorKind;
  return 'network';
};

export const fetchCodexSubscription = async (
  input: CodexSubscriptionFetchInput
): Promise<CodexSubscriptionRecord> => {
  const accountId = input.accountId.trim();
  const authIndex = normalizeAuthIndex(input.authIndex);
  if (!accountId) {
    throw new CodexSubscriptionFetchError('missing_account', 'Missing chatgpt_account_id');
  }
  if (!authIndex) {
    throw new CodexSubscriptionFetchError('missing_auth_index', 'Missing auth_index');
  }

  const url = `${CODEX_SUBSCRIPTIONS_URL}?account_id=${encodeURIComponent(accountId)}`;
  let result;
  try {
    result = await apiCallApi.request({
      authIndex,
      method: 'GET',
      url,
      header: {
        ...buildCodexUsageRequestHeaders(accountId),
        Accept: 'application/json',
      },
    });
  } catch (error) {
    throw new CodexSubscriptionFetchError(
      'network',
      error instanceof Error ? error.message : 'Failed to fetch Codex subscription'
    );
  }

  if (result.statusCode < 200 || result.statusCode >= 300) {
    throw new CodexSubscriptionFetchError('http', getApiCallErrorMessage(result));
  }

  const fetchedAtMs = input.nowMs ?? Date.now();
  const record = parseSubscriptionsResponse(result.body ?? result.bodyText, accountId, fetchedAtMs);
  if (!record) {
    throw new CodexSubscriptionFetchError('invalid_payload', 'Invalid subscriptions payload');
  }
  return record;
};
