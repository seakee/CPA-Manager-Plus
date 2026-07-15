import type { TFunction } from 'i18next';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    getSubscription: vi.fn(),
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

vi.mock('@/services/api/antigravitySubscription', () => ({
  antigravitySubscriptionApi: {
    get: mocks.getSubscription,
  },
}));

import {
  ANTIGRAVITY_AVAILABLE_MODELS_URLS,
  ANTIGRAVITY_QUOTA_SUMMARY_URLS,
  ANTIGRAVITY_USER_AGENT,
  CODEX_RATE_LIMIT_RESET_CREDITS_URL,
  CODEX_USAGE_URL,
  XAI_BILLING_MONTHLY_URL,
  XAI_BILLING_WEEKLY_URL,
} from './constants';
import {
  buildXaiBillingSummary,
  fetchXaiQuota,
  fetchAntigravityQuota,
  fetchClaudeQuota,
  fetchCodexQuota,
  mergeXaiBillingSummaries,
  probeXaiBilling,
} from './providerRequests';
import { XaiProbeError } from './xaiErrors';

const t = ((key: string) => key) as TFunction;

beforeEach(() => {
  mocks.getSubscription.mockReset();
  mocks.getSubscription.mockResolvedValue(null);
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
        'Chatgpt-Account-Id': 'acct-1',
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
        'Chatgpt-Account-Id': 'acct-1',
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

  it('uses localized reset credit errors for invalid detail payloads', async () => {
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
          unexpected: true,
        },
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
    expect(result.rateLimitResetCreditsError).toBe('codex_quota.reset_credits_invalid_payload');
  });
});

describe('fetchClaudeQuota', () => {
  it('keeps usage quota data when profile lookup fails', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          five_hour: {
            utilization: 12,
            resets_at: '2026-07-01T10:00:00Z',
          },
        },
      })
      .mockRejectedValueOnce(new Error('profile unavailable'));

    const result = await fetchClaudeQuota(
      {
        name: 'claude.json',
        type: 'claude',
        authIndex: 'claude-1',
      },
      t
    );

    expect(result.planType).toBeNull();
    expect(result.windows).toHaveLength(1);
    expect(result.windows[0]).toMatchObject({
      id: 'five-hour',
      usedPercent: 12,
    });
  });
});

describe('buildXaiBillingSummary', () => {
  it('normalizes cents fields from mixed object and snake-case payloads', () => {
    const summary = buildXaiBillingSummary({
      monthly_limit: { val: '15000' },
      used: { val: 3750 },
      on_demand_cap: '2500',
      billing_period_end: '2026-07-31T00:00:00Z',
    });

    expect(summary).toMatchObject({
      monthlyLimitCents: 15000,
      usedCents: 3750,
      includedUsedCents: 3750,
      onDemandCapCents: 2500,
      onDemandUsedCents: 0,
      onDemandUsedPercent: 0,
      billingPeriodEnd: '2026-07-31T00:00:00Z',
      usedPercent: 25,
    });
  });

  it('splits included and pay-as-you-go usage after monthly credits are exhausted', () => {
    const summary = buildXaiBillingSummary({
      monthly_limit: 10000,
      used: 12500,
      on_demand_cap: 5000,
    });

    expect(summary).toMatchObject({
      monthlyLimitCents: 10000,
      usedCents: 12500,
      includedUsedCents: 10000,
      onDemandCapCents: 5000,
      onDemandUsedCents: 2500,
      usedPercent: 100,
      onDemandUsedPercent: 50,
    });
  });

  it('normalizes weekly credit usage and product usage payloads', () => {
    const summary = buildXaiBillingSummary({
      current_period: {
        type: 'weekly',
        start: '2026-07-01T00:00:00Z',
        end: '2026-07-08T00:00:00Z',
      },
      credit_usage_percent: '42.5',
      product_usage: [
        { product: 'Grok 4', usage_percent: '30' },
        { product: '', usagePercent: null },
      ],
    });

    expect(summary).toMatchObject({
      periodType: 'weekly',
      usagePercent: 42.5,
      periodStart: '2026-07-01T00:00:00Z',
      periodEnd: '2026-07-08T00:00:00Z',
      productUsage: [
        { product: 'Grok 4', usagePercent: 30 },
        { product: 'Product 2', usagePercent: null },
      ],
      monthlyLimitCents: null,
      usedCents: null,
    });
  });
});

describe('mergeXaiBillingSummaries', () => {
  it('uses weekly fields from the primary summary and monthly fields from the fallback', () => {
    const weekly = buildXaiBillingSummary({
      currentPeriod: {
        type: 'weekly',
        start: '2026-07-01T00:00:00Z',
        end: '2026-07-08T00:00:00Z',
      },
      creditUsagePercent: 60,
      productUsage: [{ product: 'Grok 4', usagePercent: 75 }],
    });
    const monthly = buildXaiBillingSummary({
      monthly_limit: 10000,
      used: 2500,
      on_demand_cap: 5000,
      billing_period_end: '2026-08-01T00:00:00Z',
    });

    expect(mergeXaiBillingSummaries(weekly, monthly)).toMatchObject({
      periodType: 'weekly',
      usagePercent: 60,
      periodEnd: '2026-07-08T00:00:00Z',
      productUsage: [{ product: 'Grok 4', usagePercent: 75 }],
      monthlyLimitCents: 10000,
      usedCents: 2500,
      billingPeriodEnd: '2026-08-01T00:00:00Z',
    });
  });
});

describe('fetchXaiQuota', () => {
  it('requests weekly and monthly billing and merges their summaries', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          config: {
            current_period: {
              type: 'weekly',
              start: '2026-07-01T00:00:00Z',
              end: '2026-07-08T00:00:00Z',
            },
            credit_usage_percent: 40,
            product_usage: [{ product: 'Grok 4', usage_percent: 25 }],
          },
        },
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          config: {
            monthly_limit: 10000,
            used: 3000,
            on_demand_cap: 5000,
            billing_period_end: '2026-08-01T00:00:00Z',
          },
        },
      });

    const result = await fetchXaiQuota(
      {
        name: 'xai.json',
        type: 'xai',
        authIndex: 'xai-1',
        metadata: {
          user: {
            id: 'user-123',
          },
        },
      },
      t
    );

    expect(mocks.request).toHaveBeenCalledTimes(2);
    expect(mocks.request.mock.calls[0][0]).toMatchObject({
      authIndex: 'xai-1',
      method: 'GET',
      url: XAI_BILLING_WEEKLY_URL,
      header: expect.objectContaining({
        Authorization: 'Bearer $TOKEN$',
        'x-xai-token-auth': 'xai-grok-cli',
        'x-userid': 'user-123',
      }),
    });
    expect(mocks.request.mock.calls[1][0]).toMatchObject({
      authIndex: 'xai-1',
      method: 'GET',
      url: XAI_BILLING_MONTHLY_URL,
      header: expect.objectContaining({
        'x-userid': 'user-123',
      }),
    });
    expect(result).toMatchObject({
      periodType: 'weekly',
      usagePercent: 40,
      productUsage: [{ product: 'Grok 4', usagePercent: 25 }],
      monthlyLimitCents: 10000,
      usedCents: 3000,
      billingPeriodEnd: '2026-08-01T00:00:00Z',
    });
  });

  it('keeps monthly billing data when weekly billing fails', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 500,
        hasStatusCode: true,
        header: {},
        bodyText: 'weekly down',
        body: null,
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          config: {
            monthly_limit: 20000,
            used: 5000,
          },
        },
      });

    const result = await fetchXaiQuota(
      {
        name: 'xai.json',
        type: 'xai',
        authIndex: 'xai-1',
      },
      t
    );

    expect(result).toMatchObject({
      periodType: 'monthly',
      monthlyLimitCents: 20000,
      usedCents: 5000,
      usedPercent: 25,
      partial: true,
      diagnostics: [
        expect.objectContaining({ classification: 'upstream_error', statusCode: 500 }),
      ],
    });
  });

  it('marks a one-sided xAI billing response as partial while keeping usable data', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 500,
        hasStatusCode: true,
        header: {},
        bodyText: 'weekly down',
        body: null,
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: { config: { monthly_limit: 20000, used: 5000 } },
      });

    const result = await probeXaiBilling({ name: 'xai.json', type: 'xai', authIndex: 'xai-1' }, t);

    expect(result).toMatchObject({
      partial: true,
      summary: { monthlyLimitCents: 20000, usedCents: 5000 },
    });
    expect(result.failures).toHaveLength(1);
  });

  it('prefers a verified xAI quota signal when both billing requests fail differently', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 500,
        hasStatusCode: true,
        header: {},
        bodyText: 'weekly down',
        body: null,
      })
      .mockResolvedValueOnce({
        statusCode: 402,
        hasStatusCode: true,
        header: {},
        bodyText: '{"code":"subscription:free-usage-exhausted"}',
        body: { code: 'subscription:free-usage-exhausted' },
      });

    await expect(
      probeXaiBilling({ name: 'xai.json', type: 'xai', authIndex: 'xai-1' }, t)
    ).rejects.toMatchObject({
      decision: { classification: 'free_quota_exhausted' },
    });
  });

  it('prefers auth invalid over a generic forbidden billing failure', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 403,
        hasStatusCode: true,
        header: {},
        bodyText: 'forbidden',
        body: { error: 'forbidden' },
      })
      .mockResolvedValueOnce({
        statusCode: 401,
        hasStatusCode: true,
        header: {},
        bodyText: 'invalid credentials',
        body: { error: 'invalid credentials' },
      });

    await expect(
      probeXaiBilling({ name: 'xai.json', type: 'xai', authIndex: 'xai-1' }, t)
    ).rejects.toMatchObject({
      decision: { classification: 'auth_invalid', suggestedAction: 'reauth' },
    });
  });

  it('throws the upstream error when weekly and monthly billing both fail', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 500,
        hasStatusCode: true,
        header: {},
        bodyText: 'weekly down',
        body: null,
      })
      .mockResolvedValueOnce({
        statusCode: 503,
        hasStatusCode: true,
        header: {},
        bodyText: 'monthly down',
        body: null,
      });

    await expect(
      fetchXaiQuota(
        {
          name: 'xai.json',
          type: 'xai',
          authIndex: 'xai-1',
        },
        t
      )
    ).rejects.toThrow('500 weekly down');
  });

  it('classifies empty successful xAI billing payloads as protocol changes', async () => {
    mocks.request.mockResolvedValue({
      statusCode: 200,
      hasStatusCode: true,
      header: {},
      bodyText: '',
      body: { config: {} },
    });

    await expect(
      probeXaiBilling({ name: 'xai.json', type: 'xai', authIndex: 'xai-1' }, t)
    ).rejects.toMatchObject({
      decision: { classification: 'protocol_changed', suggestedAction: 'keep' },
    });
  });

  it.each([402, 429])(
    'preserves xAI free usage exhaustion under HTTP %i as a structured error',
    async (statusCode) => {
      mocks.request.mockResolvedValue({
        statusCode,
        hasStatusCode: true,
        header: { 'retry-after': ['3600'] },
        bodyText: '{"code":"subscription:free-usage-exhausted"}',
        body: { code: 'subscription:free-usage-exhausted' },
      });

      const promise = fetchXaiQuota({ name: 'xai.json', type: 'xai', authIndex: 'xai-1' }, t);

      await expect(promise).rejects.toMatchObject({
        name: 'XaiProbeError',
        status: statusCode,
        decision: {
          classification: 'free_quota_exhausted',
          suggestedAction: 'disable',
          retryAfterSeconds: 3600,
        },
      });
    }
  );

  it('classifies an xAI spending limit without treating it as invalid auth', async () => {
    mocks.request.mockResolvedValue({
      statusCode: 403,
      hasStatusCode: true,
      header: {},
      bodyText: '{"code":"personal-team-blocked:spending-limit"}',
      body: { code: 'personal-team-blocked:spending-limit' },
    });

    await expect(
      fetchXaiQuota({ name: 'xai.json', type: 'xai', authIndex: 'xai-1' }, t)
    ).rejects.toMatchObject({
      decision: {
        classification: 'spending_limit',
        suggestedAction: 'disable',
      },
    });
  });

  it('keeps generic xAI 403 responses reviewable and non-destructive', async () => {
    mocks.request.mockResolvedValue({
      statusCode: 403,
      hasStatusCode: true,
      header: {},
      bodyText: '{"error":"Forbidden"}',
      body: { error: 'Forbidden' },
    });

    await expect(
      fetchXaiQuota({ name: 'xai.json', type: 'xai', authIndex: 'xai-1' }, t)
    ).rejects.toMatchObject({
      decision: {
        classification: 'permission_unknown',
        suggestedAction: 'keep',
        needsReview: true,
      },
    });
  });

  it('reports an outdated Grok client without suggesting account mutation', async () => {
    mocks.request.mockResolvedValue({
      statusCode: 426,
      hasStatusCode: true,
      header: {},
      bodyText: '{"error":"client version is too old"}',
      body: { error: 'client version is too old' },
    });

    try {
      await fetchXaiQuota({ name: 'xai.json', type: 'xai', authIndex: 'xai-1' }, t);
      throw new Error('expected fetchXaiQuota to reject');
    } catch (error) {
      expect(error).toBeInstanceOf(XaiProbeError);
      expect(error).toMatchObject({
        decision: {
          classification: 'client_outdated',
          suggestedAction: 'keep',
        },
      });
    }
  });
});

describe('fetchAntigravityQuota', () => {
  it('uses quota summary data and includes subscription plan data', async () => {
    mocks.getSubscription.mockResolvedValue({
      plan: 'pro',
      tierName: 'Antigravity Pro',
      tierId: 'g1-pro-tier',
    });
    mocks.request.mockResolvedValueOnce({
      statusCode: 200,
      hasStatusCode: true,
      header: {},
      bodyText: '',
      body: {
        groups: [
          {
            displayName: 'Gemini models',
            buckets: [
              {
                bucketId: 'gemini-weekly',
                displayName: 'Weekly limit',
                window: 'weekly',
                remainingFraction: 0.7,
                resetTime: '2026-07-02T00:00:00Z',
              },
            ],
          },
        ],
      },
    });

    const result = await fetchAntigravityQuota(
      {
        name: 'antigravity.json',
        type: 'antigravity',
        authIndex: 'ag-1',
        project_id: 'project-1',
      },
      t
    );

    expect(mocks.request).toHaveBeenCalledTimes(1);
    expect(mocks.request.mock.calls[0][0]).toMatchObject({
      url: ANTIGRAVITY_QUOTA_SUMMARY_URLS[0],
    });
    expect(mocks.getSubscription).toHaveBeenCalledWith('ag-1');
    expect(result.subscription).toEqual({
      plan: 'pro',
      tierName: 'Antigravity Pro',
      tierId: 'g1-pro-tier',
    });
    expect(result.groups[0]).toMatchObject({
      label: 'Gemini models',
      buckets: [
        {
          label: 'Weekly limit',
          remainingFraction: 0.7,
        },
      ],
    });
  });

  it('falls back to available models when summary endpoints have no usable data', async () => {
    ANTIGRAVITY_QUOTA_SUMMARY_URLS.forEach(() => {
      mocks.request.mockResolvedValueOnce({
        statusCode: 404,
        hasStatusCode: true,
        header: {},
        bodyText: 'not found',
        body: null,
      });
    });
    mocks.request.mockResolvedValueOnce({
      statusCode: 200,
      hasStatusCode: true,
      header: {},
      bodyText: '',
      body: {
        models: {
          'claude-sonnet-4-6': {
            displayName: 'Claude Sonnet 4.6',
            quotaInfo: { remainingFraction: 0.5 },
            apiProvider: 'API_PROVIDER_ANTHROPIC_VERTEX',
          },
          'gemini-3-pro-high': {
            displayName: 'Gemini 3 Pro',
            quotaInfo: { remainingFraction: 0.8 },
            apiProvider: 'API_PROVIDER_GOOGLE_GEMINI',
          },
        },
        agentModelSorts: [
          {
            groups: [{ modelIds: ['gemini-3-pro-high'] }],
          },
        ],
      },
    });

    const result = await fetchAntigravityQuota(
      {
        name: 'antigravity.json',
        type: 'antigravity',
        authIndex: 'ag-1',
        project_id: 'project-1',
      },
      t
    );

    expect(mocks.request).toHaveBeenCalledTimes(ANTIGRAVITY_QUOTA_SUMMARY_URLS.length + 1);
    expect(mocks.request.mock.calls[mocks.request.mock.calls.length - 1]?.[0]).toMatchObject({
      url: ANTIGRAVITY_AVAILABLE_MODELS_URLS[0],
    });
    expect(result.groups.map((group) => group.id)).toEqual(['claude-gpt', 'gemini']);
  });

  it('sends the generated Antigravity user agent', async () => {
    mocks.request.mockResolvedValue({
      statusCode: 403,
      hasStatusCode: true,
      header: {},
      bodyText: 'forbidden',
      body: null,
    });

    await expect(
      fetchAntigravityQuota(
        {
          name: 'antigravity.json',
          type: 'antigravity',
          authIndex: 'ag-1',
          project_id: 'project-1',
        },
        t
      )
    ).rejects.toThrow();

    expect(mocks.request.mock.calls[0][0]).toMatchObject({
      authIndex: 'ag-1',
      method: 'POST',
      header: expect.objectContaining({
        Authorization: 'Bearer $TOKEN$',
        'User-Agent': ANTIGRAVITY_USER_AGENT,
      }),
    });
  });
});
