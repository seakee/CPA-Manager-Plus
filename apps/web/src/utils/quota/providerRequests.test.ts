import type { TFunction } from 'i18next';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    request: vi.fn(),
  },
}));

vi.mock('@/services/api/apiCall', () => ({
  apiCallApi: {
    request: mocks.request,
  },
  getApiCallErrorMessage: (result: { statusCode: number; bodyText?: string }) =>
    `${result.statusCode} ${result.bodyText ?? ''}`.trim(),
}));

import {
  CODEX_RATE_LIMIT_RESET_CREDITS_URL,
  CODEX_USAGE_URL,
} from './constants';
import { fetchCodexQuota } from './providerRequests';

const t = ((key: string) => key) as TFunction;

beforeEach(() => {
  mocks.request.mockReset();
});

describe('fetchCodexQuota', () => {
  it('fetches reset credit details after usage and prefers detail counts', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          plan_type: 'plus',
          rate_limit_reset_credits: {
            available_count: 1,
          },
        },
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          available_count: 2,
          credits: [
            {
              id: 'credit-1',
              reset_type: 'codex_rate_limits',
              status: 'available',
              granted_at: '2026-06-01T00:00:00Z',
              expires_at: '2026-06-30T00:00:00Z',
            },
          ],
        },
      });

    const result = await fetchCodexQuota(
      {
        name: 'codex.json',
        type: 'codex',
        authIndex: ' auth-1 ',
        id_token: { account_id: 'acct-1' },
      },
      t
    );

    expect(mocks.request).toHaveBeenCalledTimes(2);
    expect(mocks.request.mock.calls[0][0]).toMatchObject({
      authIndex: 'auth-1',
      method: 'GET',
      url: CODEX_USAGE_URL,
      header: expect.objectContaining({
        Authorization: 'Bearer $TOKEN$',
        'ChatGPT-Account-Id': 'acct-1',
      }),
    });
    expect(mocks.request.mock.calls[1][0]).toMatchObject({
      authIndex: 'auth-1',
      method: 'GET',
      url: CODEX_RATE_LIMIT_RESET_CREDITS_URL,
      header: expect.objectContaining({
        Accept: 'application/json',
        'OpenAI-Beta': 'codex-1',
        Originator: 'Codex Desktop',
        'ChatGPT-Account-Id': 'acct-1',
      }),
    });
    expect(mocks.request.mock.calls[1][1]).toMatchObject({ timeout: 8000 });
    expect(result.rateLimitResetCreditsAvailableCount).toBe(2);
    expect(result.rateLimitResetCredits).toHaveLength(1);
    expect(result.rateLimitResetCreditsError).toBeNull();
  });

  it('keeps usage quota data when reset credit details fail', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          plan_type: 'plus',
          rate_limit_reset_credits: {
            available_count: 1,
          },
        },
      })
      .mockResolvedValueOnce({
        statusCode: 502,
        hasStatusCode: true,
        header: {},
        bodyText: 'bad gateway',
        body: null,
      });

    const result = await fetchCodexQuota(
      {
        name: 'codex.json',
        type: 'codex',
        authIndex: 'auth-1',
      },
      t
    );

    expect(result.rateLimitResetCreditsAvailableCount).toBe(1);
    expect(result.rateLimitResetCredits).toEqual([]);
    expect(result.rateLimitResetCreditsError).toBe('502 bad gateway');
  });
});
