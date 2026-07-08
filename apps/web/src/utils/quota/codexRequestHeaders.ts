import { CODEX_REQUEST_HEADERS } from './constants';

export const buildCodexUsageRequestHeaders = (
  accountId?: string | null,
  options: { userAgent?: string } = {}
): Record<string, string> => {
  const headers: Record<string, string> = {
    ...CODEX_REQUEST_HEADERS,
  };

  const trimmedAccountId = String(accountId ?? '').trim();
  if (trimmedAccountId) {
    headers['Chatgpt-Account-Id'] = trimmedAccountId;
  }

  const userAgent = String(options.userAgent ?? '').trim();
  if (userAgent) {
    headers['User-Agent'] = userAgent;
  }

  return headers;
};

export const buildCodexResetCreditsRequestHeaders = (
  accountId?: string | null
): Record<string, string> => ({
  ...buildCodexUsageRequestHeaders(accountId),
  Accept: 'application/json',
  'OpenAI-Beta': 'codex-1',
  Originator: 'Codex Desktop',
});
