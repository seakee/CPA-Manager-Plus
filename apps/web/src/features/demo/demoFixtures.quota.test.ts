import { describe, expect, it } from 'vitest';
import { getDemoApiCallResult } from './demoFixtures';

const CLAUDE_USAGE_URL = 'https://api.anthropic.com/api/oauth/usage';
const CODEX_USAGE_URL = 'https://chatgpt.com/backend-api/wham/usage';

describe('Claude quota demo fixtures', () => {
  it('provides limits-only base quotas plus multiple fictional scoped models', () => {
    const result = getDemoApiCallResult({
      authIndex: 'claude-team-01',
      url: CLAUDE_USAGE_URL,
    });

    expect(result.body).toMatchObject({
      limits: [
        {
          kind: 'session',
          group: 'session',
          percent: 44,
          scope: null,
        },
        {
          kind: 'weekly_all',
          group: 'weekly',
          percent: 31,
          scope: null,
        },
        {
          kind: 'weekly_scoped',
          group: 'weekly',
          percent: 78,
          scope: { model: { display_name: 'Demo Model A' } },
        },
        {
          kind: 'model_scoped',
          group: 'weekly',
          percent: 12,
          scope: { model: { displayName: 'Demo Model B' } },
          is_active: false,
        },
        {
          kind: 'model_scoped',
          group: 'weekly',
          percent: 42,
          scope: { model: { displayName: 'Demo Model B' } },
          is_active: false,
        },
      ],
    });
    expect(result.body).not.toHaveProperty('five_hour');
    expect(result.body).not.toHaveProperty('seven_day');
  });

  it('keeps the research account on the legacy payload without limits', () => {
    const result = getDemoApiCallResult({
      authIndex: 'claude-research-02',
      url: CLAUDE_USAGE_URL,
    });

    expect(result.body).toMatchObject({
      five_hour: { utilization: 18 },
      seven_day: { utilization: 22 },
    });
    expect(result.body).not.toHaveProperty('limits');
  });
});

describe('Codex spend-control demo fixture', () => {
  it('keeps the Team account on the #935 zero-budget scenario after quota refresh', () => {
    const result = getDemoApiCallResult({
      authIndex: 'codex-team-01',
      url: CODEX_USAGE_URL,
    });

    expect(result.body).toMatchObject({
      plan_type: 'team',
      rate_limit: {
        allowed: true,
        limit_reached: false,
        primary_window: { used_percent: 55, limit_window_seconds: 18_000 },
        secondary_window: { used_percent: 9, limit_window_seconds: 604_800 },
      },
      credits: {
        has_credits: false,
        unlimited: false,
        balance: null,
        overage_limit_reached: false,
      },
      spend_control: {
        reached: true,
        individual_limit: {
          source: 'workspace_spend_controls',
          unit: 'credit',
          limit: '0',
          used: '0.0',
          remaining: '0.0',
          used_percent: 100,
          remaining_percent: 0,
          reset_after_seconds: expect.any(Number),
          reset_at: expect.any(Number),
        },
      },
      rate_limit_reached_type: null,
    });
  });
});

