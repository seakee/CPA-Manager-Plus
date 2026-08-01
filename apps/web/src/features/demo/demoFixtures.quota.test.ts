import { describe, expect, it } from 'vitest';
import { getDemoApiCallResult } from './demoFixtures';

const CLAUDE_USAGE_URL = 'https://api.anthropic.com/api/oauth/usage';

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
