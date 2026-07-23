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
  CLAUDE_PROFILE_URL,
  CLAUDE_USAGE_URL,
  CODEX_RATE_LIMIT_RESET_CREDITS_URL,
  CODEX_USAGE_URL,
  XAI_BILLING_MONTHLY_URL,
  XAI_BILLING_WEEKLY_URL,
  XAI_CLI_CHAT_PROXY_BASE_URL,
  XAI_GROK_CLIENT_VERSION,
  DEFAULT_XAI_INSPECTION_MODEL,
  DEFAULT_XAI_INSPECTION_PROMPT,
  XAI_INFERENCE_USER_AGENT,
  XAI_OFFICIAL_API_BASE_URL,
  XAI_OFFICIAL_API_ME_URL,
} from './constants';
import { formatQuotaResetTime } from './formatters';
import {
  buildXaiBillingSummary,
  fetchXaiQuota,
  fetchAntigravityQuota,
  fetchClaudeQuota,
  fetchCodexQuota,
  mergeXaiBillingSummaries,
  probeXaiBilling,
  probeXaiInference,
  probeXaiQuota,
} from './providerRequests';
import { XaiProbeError } from './xaiErrors';

const t = ((key: string) => key) as TFunction;

const completedXaiInferenceResponse = () => ({
  object: 'response',
  status: 'completed',
  error: null,
  output: [
    {
      type: 'message',
      content: [{ type: 'output_text', text: 'OK' }],
    },
  ],
});

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
  it('adds model-scoped weekly limits from the existing usage request', async () => {
    const resetAt = '2026-07-08T21:00:00+00:00';
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
          seven_day: {
            utilization: 34,
            resets_at: '2026-07-07T10:00:00Z',
          },
          iguana_necktie: {
            utilization: 56,
            resets_at: '2026-07-09T10:00:00Z',
          },
          limits: [
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 100,
              resets_at: resetAt,
              scope: {
                model: {
                  id: null,
                  display_name: 'Fable 5 Max',
                },
              },
              is_active: true,
            },
          ],
        },
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {},
      });

    const result = await fetchClaudeQuota(
      {
        name: 'claude.json',
        type: 'claude',
        authIndex: 'claude-1',
      },
      t
    );

    expect(mocks.request).toHaveBeenCalledTimes(2);
    expect(mocks.request.mock.calls.map(([request]) => request.url)).toEqual([
      CLAUDE_USAGE_URL,
      CLAUDE_PROFILE_URL,
    ]);
    expect(result.windows.map((window) => window.id)).toEqual([
      'five-hour',
      'seven-day',
      'weekly-scoped-fable%205%20max',
      'iguana-necktie',
    ]);
    expect(result.windows[2]).toEqual({
      id: 'weekly-scoped-fable%205%20max',
      label: 'Fable 5 Max',
      usedPercent: 100,
      resetLabel: formatQuotaResetTime(resetAt),
    });
    expect(result.windows[3]).toMatchObject({
      id: 'iguana-necktie',
      labelKey: 'claude_quota.iguana_necktie',
    });
  });

  it('restores base windows from a limits-only response before scoped weekly rows', async () => {
    const sessionResetAt = '2026-07-01T10:00:00Z';
    const weeklyResetAt = '2026-07-07T10:00:00Z';
    const scopedResetAt = '2026-07-08T21:00:00+00:00';
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          limits: [
            {
              kind: 'session',
              group: 'session',
              percent: '18',
              resets_at: sessionResetAt,
              is_active: true,
            },
            {
              kind: 'weekly_all',
              group: 'weekly',
              percent: 125,
              resetsAt: weeklyResetAt,
              scope: null,
              isActive: true,
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 42,
              reset_at: scopedResetAt,
              scope: { model: { display_name: 'Fable 5 Max' } },
              is_active: true,
            },
          ],
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

    expect(result.windows).toEqual([
      {
        id: 'five-hour',
        label: 'claude_quota.five_hour',
        labelKey: 'claude_quota.five_hour',
        usedPercent: 18,
        resetLabel: formatQuotaResetTime(sessionResetAt),
      },
      {
        id: 'seven-day',
        label: 'claude_quota.seven_day',
        labelKey: 'claude_quota.seven_day',
        usedPercent: 125,
        resetLabel: formatQuotaResetTime(weeklyResetAt),
      },
      {
        id: 'weekly-scoped-fable%205%20max',
        label: 'Fable 5 Max',
        usedPercent: 42,
        resetLabel: formatQuotaResetTime(scopedResetAt),
      },
    ]);
  });

  it('fills only a missing base window and keeps top-level values authoritative', async () => {
    const topLevelResetAt = '2026-07-01T10:00:00Z';
    const fallbackResetAt = '2026-07-07T10:00:00Z';
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          five_hour: {
            utilization: 9,
            resets_at: topLevelResetAt,
          },
          limits: [
            {
              kind: 'session',
              group: 'session',
              percent: 99,
              resets_at: '2026-07-02T10:00:00Z',
              scope: null,
            },
            {
              kind: 'weekly',
              group: 'weekly',
              percent: 41,
              resets_at: fallbackResetAt,
            },
            {
              kind: 'weekly_all',
              group: 'weekly',
              percent: 88,
              resets_at: '2026-07-08T10:00:00Z',
              scope: null,
            },
          ],
        },
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {},
      });

    const result = await fetchClaudeQuota(
      {
        name: 'claude.json',
        type: 'claude',
        authIndex: 'claude-1',
      },
      t
    );

    expect(result.windows).toEqual([
      {
        id: 'five-hour',
        label: 'claude_quota.five_hour',
        labelKey: 'claude_quota.five_hour',
        usedPercent: 9,
        resetLabel: formatQuotaResetTime(topLevelResetAt),
      },
      {
        id: 'seven-day',
        label: 'claude_quota.seven_day',
        labelKey: 'claude_quota.seven_day',
        usedPercent: 88,
        resetLabel: formatQuotaResetTime('2026-07-08T10:00:00Z'),
      },
    ]);
  });

  it('uses valid limits fallbacks when top-level windows contain no displayable data', async () => {
    const sessionResetAt = '2026-07-01T10:00:00Z';
    const weeklyResetAt = '2026-07-07T10:00:00Z';
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          five_hour: {
            utilization: 'bad',
            resets_at: 'not-a-date',
          },
          seven_day: {
            utilization: null,
            resets_at: '',
          },
          limits: [
            {
              kind: 'session',
              group: 'session',
              percent: 17,
              resets_at: sessionResetAt,
              scope: null,
            },
            {
              kind: 'weekly_all',
              group: 'weekly',
              percent: 29,
              resets_at: weeklyResetAt,
              scope: null,
            },
          ],
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

    expect(result.windows).toEqual([
      {
        id: 'five-hour',
        label: 'claude_quota.five_hour',
        labelKey: 'claude_quota.five_hour',
        usedPercent: 17,
        resetLabel: formatQuotaResetTime(sessionResetAt),
      },
      {
        id: 'seven-day',
        label: 'claude_quota.seven_day',
        labelKey: 'claude_quota.seven_day',
        usedPercent: 29,
        resetLabel: formatQuotaResetTime(weeklyResetAt),
      },
    ]);
  });

  it('selects complete current base candidates and prefers weekly_all independent of order', async () => {
    const sessionResetAt = '2026-07-02T10:00:00Z';
    const weeklyResetAt = '2026-07-08T10:00:00Z';
    const createUsageBody = (weeklyLimits: Array<Record<string, unknown>>) => ({
      limits: [
        {
          kind: 'session',
          group: 'session',
          percent: null,
          resets_at: sessionResetAt,
          scope: null,
          is_active: true,
        },
        {
          kind: 'session',
          group: 'session',
          percent: 90,
          resets_at: '2026-07-01T12:00:00Z',
          scope: null,
          is_active: true,
        },
        {
          kind: 'session',
          group: 'session',
          percent: 20,
          resets_at: sessionResetAt,
          scope: null,
          is_active: true,
        },
        ...weeklyLimits,
      ],
    });
    const weekly = {
      kind: 'weekly',
      group: 'weekly',
      percent: 30,
      resets_at: weeklyResetAt,
      scope: null,
      is_active: true,
    };
    const weeklyAll = {
      kind: 'weekly_all',
      group: 'weekly',
      percent: 40,
      resets_at: weeklyResetAt,
      scope: null,
      is_active: true,
    };

    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: createUsageBody([weekly, weeklyAll]),
      })
      .mockRejectedValueOnce(new Error('profile unavailable'))
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: createUsageBody([weeklyAll, weekly]),
      })
      .mockRejectedValueOnce(new Error('profile unavailable'));

    const first = await fetchClaudeQuota(
      { name: 'claude-a.json', type: 'claude', authIndex: 'claude-a' },
      t
    );
    const reversed = await fetchClaudeQuota(
      { name: 'claude-b.json', type: 'claude', authIndex: 'claude-b' },
      t
    );

    const expectedWindows = [
      {
        id: 'five-hour',
        label: 'claude_quota.five_hour',
        labelKey: 'claude_quota.five_hour',
        usedPercent: 20,
        resetLabel: formatQuotaResetTime(sessionResetAt),
      },
      {
        id: 'seven-day',
        label: 'claude_quota.seven_day',
        labelKey: 'claude_quota.seven_day',
        usedPercent: 40,
        resetLabel: formatQuotaResetTime(weeklyResetAt),
      },
    ];
    expect(first.windows).toEqual(expectedWindows);
    expect(reversed.windows).toEqual(expectedWindows);
  });

  it('orders base candidates by freshness, completeness, then kind precedence', async () => {
    const currentSessionResetAt = '2026-07-02T10:00:00Z';
    const weeklyResetAt = '2026-07-08T10:00:00Z';
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          limits: [
            {
              kind: 'session',
              group: 'session',
              percent: 90,
              resets_at: '2026-07-01T10:00:00Z',
              scope: null,
              is_active: true,
            },
            {
              kind: 'session',
              group: 'session',
              percent: null,
              resets_at: currentSessionResetAt,
              scope: null,
              is_active: true,
            },
            {
              kind: 'weekly_all',
              group: 'weekly',
              percent: null,
              resets_at: weeklyResetAt,
              scope: null,
              is_active: true,
            },
            {
              kind: 'weekly',
              group: 'weekly',
              percent: 30,
              resets_at: weeklyResetAt,
              scope: null,
              is_active: true,
            },
          ],
        },
      })
      .mockRejectedValueOnce(new Error('profile unavailable'));

    const result = await fetchClaudeQuota(
      { name: 'claude.json', type: 'claude', authIndex: 'claude-1' },
      t
    );

    expect(result.windows).toEqual([
      {
        id: 'five-hour',
        label: 'claude_quota.five_hour',
        labelKey: 'claude_quota.five_hour',
        usedPercent: null,
        resetLabel: formatQuotaResetTime(currentSessionResetAt),
      },
      {
        id: 'seven-day',
        label: 'claude_quota.seven_day',
        labelKey: 'claude_quota.seven_day',
        usedPercent: 30,
        resetLabel: formatQuotaResetTime(weeklyResetAt),
      },
    ]);
  });

  it('skips unsafe base fallback candidates without losing valid scoped limits', async () => {
    const scopedResetAt = '2026-07-08T21:00:00+00:00';
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          limits: [
            {
              kind: 'session',
              group: 'session',
              percent: 10,
              resets_at: '2026-07-01T10:00:00Z',
              scope: { model: { display_name: 'Scoped session' } },
            },
            {
              kind: 'session',
              group: 'session',
              percent: 20,
              resets_at: '2026-07-01T10:00:00Z',
              is_active: false,
            },
            {
              kind: 'weekly_all',
              group: 'weekly',
              percent: null,
              resets_at: 'not-a-date',
              scope: null,
            },
            {
              kind: 'monthly',
              group: 'monthly',
              percent: 30,
              resets_at: '2026-08-01T10:00:00Z',
              scope: null,
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 42,
              resets_at: scopedResetAt,
              scope: { model: { display_name: 'Healthy scoped model' } },
            },
          ],
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

    expect(result.windows).toEqual([
      {
        id: 'weekly-scoped-healthy%20scoped%20model',
        label: 'Healthy scoped model',
        usedPercent: 42,
        resetLabel: formatQuotaResetTime(scopedResetAt),
      },
    ]);
  });

  it('preserves over-limit scoped percentages for the renderer to clamp', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          limits: [
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 125,
              scope: { model: { display_name: 'Over Limit' } },
            },
          ],
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

    expect(result.windows).toEqual([
      {
        id: 'weekly-scoped-over%20limit',
        label: 'Over Limit',
        usedPercent: 125,
        resetLabel: '-',
      },
    ]);
  });

  it('keeps inactive scoped weekly limits visible and prefers an active duplicate', async () => {
    const dormantResetAt = '2026-07-08T10:00:00Z';
    const activeResetAt = '2026-07-10T10:00:00Z';
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          limits: [
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 35,
              resets_at: dormantResetAt,
              scope: { model: { id: 'model-dormant', display_name: 'Dormant model' } },
              is_active: false,
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 95,
              resets_at: activeResetAt,
              scope: { model: { id: 'model-shared', display_name: 'Shared inactive' } },
              is_active: false,
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 20,
              resets_at: activeResetAt,
              scope: { model: { id: 'model-shared', display_name: 'Shared active' } },
              is_active: true,
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 95,
              resets_at: '2026-07-10T12:00:00Z',
              scope: { model: { id: 'model-unknown', display_name: 'Unknown inactive' } },
              is_active: false,
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 20,
              resets_at: '2026-07-10T12:00:00Z',
              scope: { model: { id: 'model-unknown', display_name: 'Unknown preferred' } },
            },
          ],
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

    expect(result.windows).toEqual([
      {
        id: 'weekly-scoped-id-model-dormant',
        label: 'Dormant model',
        usedPercent: 35,
        resetLabel: formatQuotaResetTime(dormantResetAt),
      },
      {
        id: 'weekly-scoped-id-model-shared',
        label: 'Shared active',
        usedPercent: 20,
        resetLabel: formatQuotaResetTime(activeResetAt),
      },
      {
        id: 'weekly-scoped-id-model-unknown',
        label: 'Unknown preferred',
        usedPercent: 20,
        resetLabel: formatQuotaResetTime('2026-07-10T12:00:00Z'),
      },
    ]);
  });

  it('prefers the current reset period before activity and usage for scoped duplicates', async () => {
    const percentResetAt = '2026-07-09T10:00:00Z';
    const newerResetAt = '2026-07-10T10:00:00Z';
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          limits: [
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 10,
              resets_at: percentResetAt,
              scope: { model: { id: 'model-percent', display_name: 'Old percent' } },
              is_active: false,
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 80,
              resets_at: percentResetAt,
              scope: { model: { id: 'model-percent', display_name: 'Fresh percent' } },
              is_active: false,
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 50,
              resets_at: '2026-07-08T12:00:00Z',
              scope: { model: { id: 'model-reset', display_name: 'Old reset' } },
              is_active: true,
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 50,
              resets_at: newerResetAt,
              scope: { model: { id: 'model-reset', display_name: 'Fresh reset' } },
              is_active: true,
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 90,
              resets_at: '2026-07-08T14:00:00Z',
              scope: { model: { id: 'model-conservative', display_name: 'Higher usage' } },
              is_active: true,
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 20,
              resets_at: '2026-07-11T14:00:00Z',
              scope: { model: { id: 'model-conservative', display_name: 'Later reset' } },
              is_active: false,
            },
          ],
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

    expect(result.windows).toEqual([
      {
        id: 'weekly-scoped-id-model-percent',
        label: 'Fresh percent',
        usedPercent: 80,
        resetLabel: formatQuotaResetTime(percentResetAt),
      },
      {
        id: 'weekly-scoped-id-model-reset',
        label: 'Fresh reset',
        usedPercent: 50,
        resetLabel: formatQuotaResetTime(newerResetAt),
      },
      {
        id: 'weekly-scoped-id-model-conservative',
        label: 'Later reset',
        usedPercent: 20,
        resetLabel: formatQuotaResetTime('2026-07-11T14:00:00Z'),
      },
    ]);
  });

  it('prefers an active percent-only scoped candidate over an inactive reset-only candidate in both orders', async () => {
    const resetAt = '2026-07-10T10:00:00Z';
    const activePercentOnly = {
      kind: 'weekly_scoped',
      group: 'weekly',
      percent: 35,
      scope: { model: { id: 'model-partial', display_name: 'Active partial' } },
      is_active: true,
    };
    const inactiveResetOnly = {
      kind: 'weekly_scoped',
      group: 'weekly',
      percent: null,
      resets_at: resetAt,
      scope: { model: { id: 'model-partial', display_name: 'Inactive partial' } },
      is_active: false,
    };

    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: { limits: [activePercentOnly, inactiveResetOnly] },
      })
      .mockRejectedValueOnce(new Error('profile unavailable'))
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: { limits: [inactiveResetOnly, activePercentOnly] },
      })
      .mockRejectedValueOnce(new Error('profile unavailable'));

    const first = await fetchClaudeQuota(
      { name: 'claude-a.json', type: 'claude', authIndex: 'claude-a' },
      t
    );
    const reversed = await fetchClaudeQuota(
      { name: 'claude-b.json', type: 'claude', authIndex: 'claude-b' },
      t
    );

    const expected = [
      {
        id: 'weekly-scoped-id-model-partial',
        label: 'Active partial',
        usedPercent: 35,
        resetLabel: '-',
      },
    ];
    expect(first.windows).toEqual(expected);
    expect(reversed.windows).toEqual(expected);
  });

  it('ignores unscoped duplicates, unrelated kinds, and malformed scoped limits', async () => {
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
          seven_day: {
            utilization: 34,
            resets_at: '2026-07-07T10:00:00Z',
          },
          limits: [
            {
              kind: 'session',
              group: 'session',
              percent: 12,
              resets_at: '2026-07-01T10:00:00Z',
              scope: null,
            },
            {
              kind: 'weekly',
              group: 'weekly',
              percent: 34,
              resets_at: '2026-07-07T10:00:00Z',
              scope: null,
            },
            {
              kind: 'weekly_all',
              group: 'weekly',
              percent: 35,
              resets_at: '2026-07-08T10:00:00Z',
              scope: null,
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 50,
              resets_at: '2026-07-08T10:00:00Z',
              scope: { model: { display_name: '   ' } },
            },
            {
              kind: 'monthly_scoped',
              group: 'monthly',
              percent: 50,
              resets_at: '2026-08-01T10:00:00Z',
              scope: { model: { display_name: 'Unrelated' } },
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: null,
              resets_at: 'not-a-date',
              scope: { model: { display_name: 'Broken' } },
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 25,
              resets_at: '2026-07-08T10:00:00Z',
              scope: { model: { display_name: 123 } },
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: null,
              resets_at: 'not-a-date',
              scope: { model: { display_name: 'Inactive' } },
              is_active: false,
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 25,
              resets_at: '2026-07-08T10:00:00Z',
              scope: null,
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 25,
              resets_at: '2026-07-08T10:00:00Z',
              scope: 'invalid',
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 25,
              resets_at: '2026-07-08T10:00:00Z',
              scope: { model: null },
            },
            null,
          ],
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

    expect(result.windows.map((window) => window.id)).toEqual(['five-hour', 'seven-day']);
  });

  it('sorts and deduplicates multiple model-scoped weekly limits', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          five_hour: {
            utilization: 10,
            resets_at: '2026-07-01T10:00:00Z',
          },
          seven_day: {
            utilization: 20,
            resets_at: '2026-07-07T10:00:00Z',
          },
          seven_day_oauth_apps: {
            utilization: 30,
            resets_at: '2026-07-07T11:00:00Z',
          },
          limits: [
            {
              kind: 'modelScoped',
              group: 'weekly',
              percent: 70,
              resetsAt: '2026-07-10T10:00:00Z',
              scope: { model: { id: 'model-z', displayName: 'Zulu' } },
            },
            {
              kind: 'weekly-scoped',
              group: 'weekly',
              percent: 40,
              resets_at: '2026-07-08T10:00:00Z',
              scope: { model: { id: 'model-a1', details: { display_name: 'Alpha' } } },
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 50,
              resets_at: '2026-07-09T10:00:00Z',
              scope: { model: { id: 'model-a2', display_name: 'Alpha' } },
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 90,
              resets_at: '2026-07-11T10:00:00Z',
              scope: { model: { id: 'model-z', display_name: 'Zulu renamed' } },
            },
          ],
        },
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {},
      });

    const result = await fetchClaudeQuota(
      {
        name: 'claude.json',
        type: 'claude',
        authIndex: 'claude-1',
      },
      t
    );

    expect(result.windows.map((window) => [window.id, window.label, window.usedPercent])).toEqual([
      ['five-hour', 'claude_quota.five_hour', 10],
      ['seven-day', 'claude_quota.seven_day', 20],
      ['weekly-scoped-id-model-a1', 'Alpha (model-a1)', 40],
      ['weekly-scoped-id-model-a2', 'Alpha (model-a2)', 50],
      ['weekly-scoped-id-model-z', 'Zulu renamed', 90],
      ['seven-day-oauth-apps', 'claude_quota.seven_day_oauth_apps', 30],
    ]);
  });

  it('deduplicates the same scoped model with and without an id in both orders', async () => {
    const resetAt = '2026-07-10T10:00:00Z';
    const withId = {
      kind: 'weekly_scoped',
      group: 'weekly',
      percent: 40,
      resets_at: resetAt,
      scope: { model: { id: 'model-shared', display_name: 'Shared Model' } },
      is_active: true,
    };
    const withoutId = {
      kind: 'weekly_scoped',
      group: 'weekly',
      percent: 40,
      resets_at: resetAt,
      scope: { model: { id: null, display_name: 'Shared Model' } },
      is_active: true,
    };

    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: { limits: [withoutId, withId] },
      })
      .mockRejectedValueOnce(new Error('profile unavailable'))
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: { limits: [withId, withoutId] },
      })
      .mockRejectedValueOnce(new Error('profile unavailable'));

    const withoutIdFirst = await fetchClaudeQuota(
      { name: 'claude-a.json', type: 'claude', authIndex: 'claude-a' },
      t
    );
    const withIdFirst = await fetchClaudeQuota(
      { name: 'claude-b.json', type: 'claude', authIndex: 'claude-b' },
      t
    );

    const expectedWindows = [
      {
        id: 'weekly-scoped-id-model-shared',
        label: 'Shared Model',
        usedPercent: 40,
        resetLabel: formatQuotaResetTime(resetAt),
      },
    ];
    expect(withoutIdFirst.windows).toEqual(expectedWindows);
    expect(withIdFirst.windows).toEqual(expectedWindows);
  });

  it('preserves non-equivalent label-only scoped data instead of dropping it', async () => {
    const resetAt = '2026-07-10T10:00:00Z';
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          limits: [
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 40,
              resets_at: resetAt,
              scope: { model: { id: 'model-shared', display_name: 'Shared Model' } },
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 20,
              resets_at: resetAt,
              scope: { model: { display_name: 'Shared Model' } },
            },
          ],
        },
      })
      .mockRejectedValueOnce(new Error('profile unavailable'));

    const result = await fetchClaudeQuota(
      { name: 'claude-1.json', type: 'claude', authIndex: 'claude-1' },
      t
    );

    expect(result.windows.map((window) => [window.id, window.label, window.usedPercent])).toEqual([
      ['weekly-scoped-id-model-shared', 'Shared Model (model-shared)', 40],
      ['weekly-scoped-shared%20model', 'Shared Model', 20],
    ]);
  });

  it('ignores unrelated nested display names but accepts model details display names', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          limits: [
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 10,
              scope: { model: { metadata: { display_name: 'Ignored' } } },
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 20,
              scope: { model: { details: { display_name: 'Details model' } } },
            },
          ],
        },
      })
      .mockRejectedValueOnce(new Error('profile unavailable'));

    const result = await fetchClaudeQuota(
      { name: 'claude-1.json', type: 'claude', authIndex: 'claude-1' },
      t
    );

    expect(result.windows.map((window) => window.label)).toEqual(['Details model']);
  });

  it('keeps a label-only scoped entry separate when the label maps to multiple ids', async () => {
    const resetAt = '2026-07-10T10:00:00Z';
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          limits: [
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 40,
              resets_at: resetAt,
              scope: { model: { id: 'model-a', display_name: 'Shared Model' } },
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 50,
              resets_at: resetAt,
              scope: { model: { id: 'model-b', display_name: 'Shared Model' } },
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 60,
              resets_at: '2026-07-11T10:00:00Z',
              scope: { model: { id: null, display_name: 'Shared Model' } },
            },
          ],
        },
      })
      .mockRejectedValueOnce(new Error('profile unavailable'));

    const result = await fetchClaudeQuota(
      { name: 'claude.json', type: 'claude', authIndex: 'claude-1' },
      t
    );

    expect(result.windows.map((window) => [window.id, window.usedPercent])).toEqual([
      ['weekly-scoped-id-model-a', 40],
      ['weekly-scoped-id-model-b', 50],
      ['weekly-scoped-shared%20model', 60],
    ]);
  });

  it('isolates malformed Unicode labels instead of failing the whole refresh', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {
          five_hour: {
            utilization: 10,
            resets_at: '2026-07-01T10:00:00Z',
          },
          limits: [
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 25,
              resets_at: '2026-07-08T10:00:00Z',
              scope: { model: { display_name: '\ud800' } },
            },
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 50,
              resets_at: '2026-07-09T10:00:00Z',
              scope: { model: { display_name: 'Healthy' } },
            },
          ],
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

    expect(result.windows.map((window) => window.id)).toEqual([
      'five-hour',
      'weekly-scoped-healthy',
      'weekly-scoped-utf16-d800',
    ]);
  });

  it('preserves legacy Claude window output when limits are absent', async () => {
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
          seven_day: {
            utilization: 34,
            resets_at: '2026-07-07T10:00:00Z',
          },
        },
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: {},
      });

    const result = await fetchClaudeQuota(
      {
        name: 'claude.json',
        type: 'claude',
        authIndex: 'claude-1',
      },
      t
    );

    expect(result.windows.map((window) => window.id)).toEqual(['five-hour', 'seven-day']);
    expect(result.windows.every((window) => window.labelKey)).toBe(true);
  });

  it('keeps usage quota data when profile lookup fails', async () => {
    const scopedResetAt = '2026-07-08T21:00:00+00:00';
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
          limits: [
            {
              kind: 'weekly_scoped',
              group: 'weekly',
              percent: 100,
              resets_at: scopedResetAt,
              scope: { model: { display_name: 'Fable 5 Max' } },
            },
          ],
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
    expect(result.windows).toHaveLength(2);
    expect(result.windows[0]).toMatchObject({
      id: 'five-hour',
      usedPercent: 12,
    });
    expect(result.windows[1]).toEqual({
      id: 'weekly-scoped-fable%205%20max',
      label: 'Fable 5 Max',
      usedPercent: 100,
      resetLabel: formatQuotaResetTime(scopedResetAt),
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

  it('preserves the billing reset for weekly plans with positive on-demand credits', () => {
    const summary = buildXaiBillingSummary({
      currentPeriod: {
        type: 'USAGE_PERIOD_TYPE_WEEKLY',
        end: '2026-07-29T00:00:00Z',
      },
      onDemandCap: { val: 5000 },
      onDemandUsed: { val: 1000 },
      billingPeriodEnd: '2026-08-01T00:00:00Z',
    });

    expect(summary).toMatchObject({
      periodType: 'weekly',
      onDemandCapCents: 5000,
      onDemandUsedCents: 1000,
      onDemandUsedPercent: 20,
      billingPeriodEnd: '2026-08-01T00:00:00Z',
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
      diagnostics: [expect.objectContaining({ classification: 'upstream_error', statusCode: 500 })],
    });
  });

  it('falls back to official API identity health when both CLI billing endpoints deny access', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 403,
        hasStatusCode: true,
        header: {},
        bodyText: '{"error":"Access denied"}',
        body: { error: 'Access denied' },
      })
      .mockResolvedValueOnce({
        statusCode: 403,
        hasStatusCode: true,
        header: {},
        bodyText: '{"error":"Access denied"}',
        body: { error: 'Access denied' },
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: { user_id: 'user-1', team_id: 'team-1', team_blocked: false },
      });

    const result = await fetchXaiQuota(
      { name: 'paid-xai.json', type: 'xai', authIndex: 'xai-paid-1' },
      t
    );

    expect(mocks.request).toHaveBeenCalledTimes(3);
    expect(mocks.request.mock.calls[2][0]).toEqual({
      authIndex: 'xai-paid-1',
      method: 'GET',
      url: XAI_OFFICIAL_API_ME_URL,
      header: {
        Authorization: 'Bearer $TOKEN$',
        accept: 'application/json',
      },
    });
    expect(result).toMatchObject({
      periodType: 'unknown',
      usagePercent: null,
      productUsage: [],
      officialApiHealth: {
        source: 'api.x.ai/v1/me',
        userId: 'user-1',
        teamId: 'team-1',
        teamBlocked: false,
      },
      partial: false,
      diagnostics: [],
    });
  });

  it.each([
    {
      name: 'an explicit entitlement denial',
      statusCode: 403,
      body: { error: 'Need a Grok subscription' },
      classification: 'entitlement_denied',
    },
    {
      name: 'an ambiguous payment-required response',
      statusCode: 402,
      body: { error: 'Payment required' },
      classification: 'quota_or_entitlement_unknown',
    },
  ])(
    'does not call the official API fallback for $name',
    async ({ body, classification, statusCode }) => {
      mocks.request.mockResolvedValue({
        statusCode,
        hasStatusCode: true,
        header: {},
        bodyText: JSON.stringify(body),
        body,
      });

      await expect(
        fetchXaiQuota({ name: 'paid-xai.json', type: 'xai', authIndex: 'xai-paid-1' }, t)
      ).rejects.toMatchObject({
        decision: { classification },
      });

      expect(mocks.request).toHaveBeenCalledTimes(2);
      expect(mocks.request.mock.calls.map(([request]) => request.url)).not.toContain(
        XAI_OFFICIAL_API_ME_URL
      );
    }
  );

  it.each([
    { name: 'null team_blocked', body: { user_id: '', team_id: '', team_blocked: null } },
    {
      name: 'an invalid team_blocked value',
      body: { user_id: ' ', team_id: '', team_blocked: 'unknown' },
    },
    {
      name: 'a numeric team_blocked value',
      body: { user_id: '', team_id: '', team_blocked: 0 },
    },
    {
      name: 'a non-string identity value',
      body: { user_id: false, team_id: '', team_blocked: null },
    },
  ])('rejects official API identity payloads with empty IDs and $name', async ({ body }) => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 403,
        hasStatusCode: true,
        header: {},
        bodyText: '{"error":"Access denied"}',
        body: { error: 'Access denied' },
      })
      .mockResolvedValueOnce({
        statusCode: 403,
        hasStatusCode: true,
        header: {},
        bodyText: '{"error":"Access denied"}',
        body: { error: 'Access denied' },
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: JSON.stringify(body),
        body,
      });

    await expect(
      fetchXaiQuota({ name: 'paid-xai.json', type: 'xai', authIndex: 'xai-paid-1' }, t)
    ).rejects.toBeInstanceOf(XaiProbeError);

    expect(mocks.request).toHaveBeenCalledTimes(3);
  });

  it('does not hide a blocked official API team behind the health fallback', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 403,
        hasStatusCode: true,
        header: {},
        bodyText: '{"error":"Access denied"}',
        body: { error: 'Access denied' },
      })
      .mockResolvedValueOnce({
        statusCode: 403,
        hasStatusCode: true,
        header: {},
        bodyText: '{"error":"Access denied"}',
        body: { error: 'Access denied' },
      })
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: { user_id: 'user-1', team_id: 'team-1', team_blocked: true },
      });

    await expect(
      fetchXaiQuota({ name: 'paid-xai.json', type: 'xai', authIndex: 'xai-paid-1' }, t)
    ).rejects.toMatchObject({
      decision: { classification: 'spending_limit', suggestedAction: 'disable' },
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
      statusCode: 200,
    });
    expect(result.failures).toHaveLength(1);
  });

  it('preserves usable billing data while exposing a one-sided spending limit as blocking', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        bodyText: '',
        body: { config: { credit_usage_percent: 3 } },
      })
      .mockResolvedValueOnce({
        statusCode: 402,
        hasStatusCode: true,
        header: {},
        bodyText: '{"code":"personal-team-blocked:spending-limit"}',
        body: { code: 'personal-team-blocked:spending-limit' },
      });

    const result = await probeXaiQuota({ name: 'xai.json', type: 'xai', authIndex: 'xai-1' }, t);

    expect(result).toMatchObject({
      partial: true,
      summary: { usagePercent: 3 },
      statusCode: 200,
      blockingFailure: {
        decision: { classification: 'spending_limit', suggestedAction: 'disable' },
      },
    });
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

  it('prefers an explicit entitlement denial over an earlier generic forbidden failure', async () => {
    mocks.request
      .mockResolvedValueOnce({
        statusCode: 403,
        hasStatusCode: true,
        header: {},
        bodyText: 'forbidden',
        body: { error: 'forbidden' },
      })
      .mockResolvedValueOnce({
        statusCode: 403,
        hasStatusCode: true,
        header: {},
        bodyText: 'Need a Grok subscription',
        body: { error: 'Need a Grok subscription' },
      });

    await expect(
      fetchXaiQuota({ name: 'xai.json', type: 'xai', authIndex: 'xai-1' }, t)
    ).rejects.toMatchObject({
      decision: { classification: 'entitlement_denied', suggestedAction: 'disable' },
    });
    expect(mocks.request).toHaveBeenCalledTimes(2);
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

describe('probeXaiInference', () => {
  it('defaults missing auth metadata to the OAuth CLI endpoint', async () => {
    const body = completedXaiInferenceResponse();
    mocks.request.mockResolvedValue({
      statusCode: 200,
      hasStatusCode: true,
      header: {},
      bodyText: JSON.stringify(body),
      body,
    });

    await probeXaiInference(
      {
        name: 'xai-legacy.json',
        type: 'xai',
        authIndex: 'xai-legacy-1',
        metadata: { base_url: XAI_OFFICIAL_API_BASE_URL },
      },
      t
    );

    expect(mocks.request.mock.calls[0][0]).toMatchObject({
      url: `${XAI_CLI_CHAT_PROXY_BASE_URL}/responses`,
      header: {
        'x-xai-token-auth': 'xai-grok-cli',
        'x-grok-client-version': XAI_GROK_CLIENT_VERSION,
        'User-Agent': XAI_INFERENCE_USER_AGENT,
      },
    });
  });

  it('sends a non-streamed real inference request through the OAuth CLI endpoint', async () => {
    const body = completedXaiInferenceResponse();
    mocks.request.mockResolvedValue({
      statusCode: 200,
      hasStatusCode: true,
      header: {},
      bodyText: JSON.stringify(body),
      body,
    });

    const result = await probeXaiInference(
      {
        name: 'xai-oauth.json',
        type: 'xai',
        authIndex: 'xai-oauth-1',
        metadata: { auth_kind: 'oauth', user_id: 'user-123' },
      },
      t
    );

    expect(result).toEqual({ statusCode: 200 });
    expect(mocks.request).toHaveBeenCalledTimes(1);
    const request = mocks.request.mock.calls[0][0];
    expect(request).toMatchObject({
      authIndex: 'xai-oauth-1',
      method: 'POST',
      url: `${XAI_CLI_CHAT_PROXY_BASE_URL}/responses`,
      header: {
        Authorization: 'Bearer $TOKEN$',
        Accept: 'application/json',
        'Content-Type': 'application/json',
        'x-xai-token-auth': 'xai-grok-cli',
        'x-grok-client-version': XAI_GROK_CLIENT_VERSION,
        'User-Agent': XAI_INFERENCE_USER_AGENT,
        'x-userid': 'user-123',
      },
    });
    expect(JSON.parse(request.data)).toEqual({
      model: DEFAULT_XAI_INSPECTION_MODEL,
      input: DEFAULT_XAI_INSPECTION_PROMPT,
      stream: false,
    });
  });

  it('uses the official endpoint without CLI-only headers for API credentials', async () => {
    const body = completedXaiInferenceResponse();
    mocks.request.mockResolvedValue({
      statusCode: 200,
      hasStatusCode: true,
      header: {},
      bodyText: JSON.stringify(body),
      body,
    });

    await probeXaiInference(
      {
        name: 'xai-api.json',
        type: 'xai',
        authIndex: 'xai-api-1',
        metadata: { auth_kind: 'api_key', using_api: true, user_id: 'user-123' },
      },
      t
    );

    expect(mocks.request.mock.calls[0][0]).toMatchObject({
      url: `${XAI_OFFICIAL_API_BASE_URL}/responses`,
      header: {
        Authorization: 'Bearer $TOKEN$',
        Accept: 'application/json',
        'Content-Type': 'application/json',
        'User-Agent': XAI_INFERENCE_USER_AGENT,
      },
    });
    expect(mocks.request.mock.calls[0][0].header).not.toHaveProperty('x-xai-token-auth');
    expect(mocks.request.mock.calls[0][0].header).not.toHaveProperty('x-grok-client-version');
    expect(mocks.request.mock.calls[0][0].header).not.toHaveProperty('x-userid');
  });

  it('forces the official endpoint after verified official identity fallback', async () => {
    const body = completedXaiInferenceResponse();
    mocks.request.mockResolvedValue({
      statusCode: 200,
      hasStatusCode: true,
      header: {},
      bodyText: JSON.stringify(body),
      body,
    });

    await probeXaiInference(
      {
        name: 'xai-paid-oauth.json',
        type: 'xai',
        authIndex: 'xai-paid-oauth-1',
        metadata: { user_id: 'user-123' },
      },
      t,
      undefined,
      { routeMode: 'official' }
    );

    expect(mocks.request.mock.calls[0][0]).toMatchObject({
      url: `${XAI_OFFICIAL_API_BASE_URL}/responses`,
      header: {
        Authorization: 'Bearer $TOKEN$',
        Accept: 'application/json',
        'Content-Type': 'application/json',
        'User-Agent': XAI_INFERENCE_USER_AGENT,
      },
    });
    expect(mocks.request.mock.calls[0][0].header).not.toHaveProperty('x-xai-token-auth');
    expect(mocks.request.mock.calls[0][0].header).not.toHaveProperty('x-grok-client-version');
    expect(mocks.request.mock.calls[0][0].header).not.toHaveProperty('x-userid');
  });

  it('uses the configured model and prompt in the real inference request', async () => {
    const body = completedXaiInferenceResponse();
    mocks.request.mockResolvedValue({
      statusCode: 200,
      hasStatusCode: true,
      header: {},
      bodyText: JSON.stringify(body),
      body,
    });

    await probeXaiInference({ name: 'xai.json', type: 'xai', authIndex: 'xai-1' }, t, undefined, {
      model: 'grok-custom',
      prompt: 'Return a short health response.',
      userAgent: 'xai-custom-agent',
    });

    expect(JSON.parse(mocks.request.mock.calls[0][0].data)).toMatchObject({
      model: 'grok-custom',
      input: 'Return a short health response.',
      stream: false,
    });
    expect(mocks.request.mock.calls[0][0].header).toMatchObject({
      'User-Agent': 'xai-custom-agent',
    });
  });

  it('honors explicit using_api=false even when auth_kind is absent', async () => {
    const body = completedXaiInferenceResponse();
    mocks.request.mockResolvedValue({
      statusCode: 200,
      hasStatusCode: true,
      header: {},
      bodyText: JSON.stringify(body),
      body,
    });

    await probeXaiInference(
      {
        name: 'xai-explicit-cli.json',
        type: 'xai',
        authIndex: 'xai-explicit-cli-1',
        metadata: { using_api: false, base_url: XAI_OFFICIAL_API_BASE_URL },
      },
      t
    );

    expect(mocks.request.mock.calls[0][0]).toMatchObject({
      url: `${XAI_CLI_CHAT_PROXY_BASE_URL}/responses`,
      header: { 'x-xai-token-auth': 'xai-grok-cli' },
    });
  });

  it('rejects a proxy response without status_code as a protocol change', async () => {
    mocks.request.mockResolvedValue({
      statusCode: 0,
      hasStatusCode: false,
      header: {},
      bodyText: '',
      body: '',
    });

    await expect(
      probeXaiInference({ name: 'xai.json', type: 'xai', authIndex: 'xai-1' }, t)
    ).rejects.toMatchObject({ decision: { classification: 'protocol_changed' } });
  });

  it.each([
    { statusCode: 401, body: { error: 'invalid credentials' }, classification: 'auth_invalid' },
    {
      statusCode: 402,
      body: { error: 'Payment required' },
      classification: 'quota_or_entitlement_unknown',
    },
    { statusCode: 429, body: { error: 'Too many requests' }, classification: 'rate_limited' },
  ])(
    'classifies an inference HTTP $statusCode response',
    async ({ statusCode, body, classification }) => {
      mocks.request.mockResolvedValue({
        statusCode,
        hasStatusCode: true,
        header: {},
        bodyText: JSON.stringify(body),
        body,
      });

      await expect(
        probeXaiInference({ name: 'xai.json', type: 'xai', authIndex: 'xai-1' }, t)
      ).rejects.toMatchObject({ decision: { classification } });
    }
  );

  it('rejects a successful HTTP response without completed model output', async () => {
    mocks.request.mockResolvedValue({
      statusCode: 200,
      hasStatusCode: true,
      header: {},
      bodyText: '',
      body: null,
    });

    await expect(
      probeXaiInference({ name: 'xai.json', type: 'xai', authIndex: 'xai-1' }, t)
    ).rejects.toMatchObject({ decision: { classification: 'protocol_changed' } });
  });

  it('accepts a completed non-streaming response with output text', async () => {
    const body = completedXaiInferenceResponse();
    mocks.request.mockResolvedValue({
      statusCode: 200,
      hasStatusCode: true,
      header: {},
      bodyText: JSON.stringify(body),
      body,
    });

    await expect(
      probeXaiInference({ name: 'xai.json', type: 'xai', authIndex: 'xai-1' }, t)
    ).resolves.toEqual({ statusCode: 200 });
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
