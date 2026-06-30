import { beforeEach, describe, expect, it, vi } from 'vitest';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    request: vi.fn(),
  },
}));

vi.mock('./apiCall', () => ({
  apiCallApi: {
    request: mocks.request,
  },
  getApiCallErrorMessage: (result: { statusCode: number; bodyText?: string }) =>
    `${result.statusCode} ${result.bodyText ?? ''}`.trim(),
}));

import { CODEX_RATE_LIMIT_RESET_CREDITS_CONSUME_URL } from '@/utils/quota/constants';
import { buildCodexUsageRequestHeaders, consumeCodexRateLimitResetCredit } from './codexQuota';

beforeEach(() => {
  mocks.request.mockReset();
});

describe('buildCodexUsageRequestHeaders', () => {
  it('does not include ChatGPT-Account-Id when account id is missing', () => {
    const headers = buildCodexUsageRequestHeaders(null);

    expect(headers).not.toHaveProperty('ChatGPT-Account-Id');
    expect(headers.Authorization).toBe('Bearer $TOKEN$');
  });

  it('includes trimmed account id when available', () => {
    const headers = buildCodexUsageRequestHeaders(' account-123 ');

    expect(headers['ChatGPT-Account-Id']).toBe('account-123');
  });

  it('allows Codex inspection to override User-Agent', () => {
    const headers = buildCodexUsageRequestHeaders('account-123', {
      userAgent: 'codex-test-agent',
    });

    expect(headers['User-Agent']).toBe('codex-test-agent');
  });
});

describe('consumeCodexRateLimitResetCredit', () => {
  it('posts a redeem request through api-call with the Codex auth index', async () => {
    mocks.request.mockResolvedValue({
      statusCode: 200,
      hasStatusCode: true,
      header: {},
      bodyText: '{}',
      body: {},
    });

    await consumeCodexRateLimitResetCredit({
      name: 'codex-auth.json',
      type: 'codex',
      authIndex: ' auth-1 ',
      id_token: { account_id: 'acct-1' },
    });

    expect(mocks.request).toHaveBeenCalledTimes(1);
    const payload = mocks.request.mock.calls[0][0];
    expect(payload).toMatchObject({
      authIndex: 'auth-1',
      method: 'POST',
      url: CODEX_RATE_LIMIT_RESET_CREDITS_CONSUME_URL,
      header: expect.objectContaining({
        Authorization: 'Bearer $TOKEN$',
        'ChatGPT-Account-Id': 'acct-1',
      }),
    });
    expect(JSON.parse(payload.data)).toEqual({
      redeem_request_id: expect.any(String),
    });
  });
});
