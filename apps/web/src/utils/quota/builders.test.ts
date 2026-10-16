import { describe, expect, it } from 'vitest';
import type { KimiUsagePayload } from '@/types';
import {
  buildAntigravityQuotaGroups,
  buildKimiQuotaRows,
  hasAntigravityGeminiGroup,
  mergeAntigravityQuotaGroups,
} from './builders';

describe('buildAntigravityQuotaGroups', () => {
  it('builds Antigravity groups from the real models payload shape', () => {
    const groups = buildAntigravityQuotaGroups({
      models: {
        'gemini-3.5-flash-low': {
          displayName: 'Gemini 3.5 Flash (Medium)',
          quotaInfo: {
            remainingFraction: 1,
            resetTime: '2026-06-29T02:18:21Z',
          },
          apiProvider: 'API_PROVIDER_GOOGLE_GEMINI',
          modelProvider: 'MODEL_PROVIDER_GOOGLE',
        },
        'gemini-pro-agent': {
          displayName: 'Gemini 3.1 Pro (High)',
          quotaInfo: {
            remainingFraction: 0.75,
            resetTime: '2026-06-29T02:18:21Z',
          },
          apiProvider: 'API_PROVIDER_GOOGLE_GEMINI',
          modelProvider: 'MODEL_PROVIDER_GOOGLE',
        },
        'gemini-3.1-flash-lite': {
          displayName: 'Gemini 3.1 Flash Lite',
          quotaInfo: {
            remainingFraction: 0.9,
            resetTime: '2026-06-29T02:18:21Z',
          },
          apiProvider: 'API_PROVIDER_GOOGLE_GEMINI',
          modelProvider: 'MODEL_PROVIDER_GOOGLE',
        },
        'gemini-3.1-flash-image': {
          displayName: 'Gemini 3.1 Flash Image',
          quotaInfo: {
            remainingFraction: 1,
            resetTime: '2026-06-29T02:18:21Z',
          },
          apiProvider: 'API_PROVIDER_GOOGLE_GEMINI',
          modelProvider: 'MODEL_PROVIDER_GOOGLE',
        },
        chat_20706: {
          quotaInfo: {
            remainingFraction: 1,
          },
          apiProvider: 'API_PROVIDER_INTERNAL',
          modelProvider: 'MODEL_PROVIDER_GOOGLE',
        },
        'claude-sonnet-4-6': {
          displayName: 'Claude Sonnet 4.6 (Thinking)',
          quotaInfo: {
            remainingFraction: 0.5,
            resetTime: '2026-06-24T10:32:10Z',
          },
          apiProvider: 'API_PROVIDER_ANTHROPIC_VERTEX',
          modelProvider: 'MODEL_PROVIDER_ANTHROPIC',
        },
        'gpt-oss-120b-medium': {
          displayName: 'GPT-OSS 120B (Medium)',
          quotaInfo: {
            remainingFraction: 0.6,
            resetTime: '2026-06-24T10:32:10Z',
          },
          apiProvider: 'API_PROVIDER_OPENAI_VERTEX',
          modelProvider: 'MODEL_PROVIDER_OPENAI',
        },
      },
      agentModelSorts: [
        {
          displayName: 'Recommended',
          groups: [
            {
              modelIds: [
                'gemini-3.5-flash-low',
                'gemini-pro-agent',
                'claude-sonnet-4-6',
                'gpt-oss-120b-medium',
              ],
            },
          ],
        },
      ],
      tieredModelIds: {
        flash: ['gemini-3.5-flash-low'],
        flashLite: ['gemini-3.1-flash-lite'],
        pro: ['gemini-pro-agent'],
      },
      commandModelIds: ['gemini-3.5-flash-low'],
      imageGenerationModelIds: ['gemini-3.1-flash-image'],
      tabModelIds: ['chat_20706'],
    });

    expect(groups.map((group) => group.label)).toEqual(['Claude', 'Gemini']);
    expect(groups.find((group) => group.id === 'claude-gpt')?.buckets[0]).toMatchObject({
      label: 'Claude',
      remainingFraction: 0.5,
      description: 'claude-sonnet-4-6, gpt-oss-120b-medium',
    });
    expect(groups.find((group) => group.id === 'gemini')?.buckets[0]).toMatchObject({
      label: 'Gemini',
      remainingFraction: 0.75,
    });
    expect(groups.find((group) => group.id === 'gemini')?.models).toHaveLength(4);
    expect(groups.find((group) => group.id === 'gemini')?.models).toEqual(
      expect.arrayContaining([
        'gemini-3.5-flash-low',
        'gemini-3.1-flash-lite',
        'gemini-pro-agent',
        'gemini-3.1-flash-image',
      ])
    );
    expect(groups.some((group) => group.id === 'tab-models')).toBe(false);
    expect(groups.some((group) => group.models?.includes('chat_20706'))).toBe(false);
  });

  it('keeps a shared Antigravity group reset aligned with its limiting models', () => {
    const groups = buildAntigravityQuotaGroups({
      models: {
        'claude-limited-a': {
          displayName: 'Claude Limited A',
          quotaInfo: {
            remainingFraction: 0,
            resetTime: '2026-07-29T14:00:00Z',
          },
          apiProvider: 'API_PROVIDER_ANTHROPIC_VERTEX',
        },
        'claude-limited-b': {
          displayName: 'Claude Limited B',
          quotaInfo: {
            remainingFraction: 0,
            resetTime: '2026-07-29T16:00:00Z',
          },
          apiProvider: 'API_PROVIDER_ANTHROPIC_VERTEX',
        },
        'gpt-available': {
          displayName: 'GPT Available',
          quotaInfo: {
            remainingFraction: 0.8,
            resetTime: '2026-07-29T12:00:00Z',
          },
          apiProvider: 'API_PROVIDER_OPENAI_VERTEX',
        },
      },
    });

    expect(groups.find((group) => group.id === 'claude-gpt')?.buckets[0]).toMatchObject({
      remainingFraction: 0,
      resetTime: '2026-07-29T16:00:00Z',
    });
    });
  });

  it('merges groups from summary and models without duplicate entries', () => {
    const payload = {
      groups: [
        {
          displayName: 'Claude and GPT models',
          buckets: [
            {
              bucketId: 'claude-5h',
              displayName: '5 hour window',
              remainingFraction: 0.85,
              resetTime: '2026-07-30T10:00:00Z',
            },
          ],
        },
      ],
      models: {
        'gemini-2.5-pro': {
          displayName: 'Gemini 2.5 Pro',
          quotaInfo: {
            remainingFraction: 0.9,
            resetTime: '2026-07-30T12:00:00Z',
          },
          apiProvider: 'API_PROVIDER_GOOGLE_GEMINI',
          modelProvider: 'MODEL_PROVIDER_GOOGLE',
        },
        'claude-sonnet-4-6': {
          displayName: 'Claude Sonnet 4.6',
          quotaInfo: {
            remainingFraction: 0.5,
            resetTime: '2026-07-30T10:00:00Z',
          },
          apiProvider: 'API_PROVIDER_ANTHROPIC_VERTEX',
          modelProvider: 'MODEL_PROVIDER_ANTHROPIC',
        },
      },
      tieredModelIds: {
        pro: ['gemini-2.5-pro'],
      },
    };

    const groups = buildAntigravityQuotaGroups(payload as any);
    expect(groups).toHaveLength(2);
    expect(groups.map((g) => g.label)).toEqual(['Claude and GPT models', 'Gemini']);
    expect(hasAntigravityGeminiGroup(groups)).toBe(true);
    expect(groups.find((g) => g.id === 'gemini')?.buckets[0]).toMatchObject({
      label: 'Gemini',
      remainingFraction: 0.9,
    });
  });

  it('mergeAntigravityQuotaGroups correctly merges missing Gemini group while keeping primary Claude', () => {
    const primary = [
      {
        id: 'claude-gpt',
        label: 'Claude and GPT models',
        buckets: [
          {
            id: 'claude-gpt-shared',
            label: '5-hour limit',
            remainingFraction: 0.4,
          },
        ],
      },
    ];
    const fallback = [
      {
        id: 'claude-gpt',
        label: 'Claude',
        buckets: [{ id: 'claude-shared', label: 'Claude', remainingFraction: 0.2 }],
      },
      {
        id: 'gemini',
        label: 'Gemini',
        buckets: [{ id: 'gemini-shared', label: 'Gemini', remainingFraction: 0.8 }],
      },
    ];

    const merged = mergeAntigravityQuotaGroups(primary, fallback);
    expect(merged).toHaveLength(2);
    expect(merged[0].id).toBe('claude-gpt');
    expect(merged[0].buckets[0].remainingFraction).toBe(0.4);
    expect(merged[1].id).toBe('gemini');
    expect(merged[1].buckets[0].remainingFraction).toBe(0.8);
});

describe('buildKimiQuotaRows', () => {
  it('normalizes absolute, relative, and outer limit reset fields', () => {
    const observedAtMs = Date.parse('2026-07-29T10:00:00Z');
    const exactResetAt = '2026-07-29T12:00:00Z';
    const outerResetAt = '2026-07-29T13:00:00Z';
    const unixResetAt = Date.parse('2026-07-29T14:00:00Z');
    const rows = buildKimiQuotaRows(
      {
        limits: [
          {
            detail: { used: 1, limit: 10, resetTime: exactResetAt },
          },
          {
            detail: { used: 2, limit: 10, reset_in: 90 },
          },
          {
            resetAt: outerResetAt,
            detail: { used: 3, limit: 10 },
          },
          {
            detail: { used: 4, limit: 10, resetTime: String(unixResetAt / 1000) },
          },
        ],
      },
      { observedAtMs }
    );

    expect(rows[0]).toMatchObject({
      resetAtMs: Date.parse(exactResetAt),
      resetAccuracy: 'exact',
    });
    expect(rows[1]).toMatchObject({
      resetAtMs: observedAtMs + 90_000,
      resetAccuracy: 'estimated',
    });
    expect(rows[2]).toMatchObject({
      resetAtMs: Date.parse(outerResetAt),
      resetAccuracy: 'exact',
    });
    expect(rows[3]).toMatchObject({
      resetAtMs: unixResetAt,
      resetAccuracy: 'exact',
    });
  });

  it('rejects Kimi reset values that exceed the JavaScript date range', () => {
    const rows = buildKimiQuotaRows(
      {
        limits: [
          { detail: { used: 1, limit: 10, resetTime: Number.MAX_VALUE } },
          { detail: { used: 2, limit: 10, ttl: Number.MAX_VALUE } },
        ],
      },
      { observedAtMs: Date.parse('2026-07-29T10:00:00Z') }
    );

    expect(rows).toMatchObject([
      {
        resetAtMs: null,
        resetAccuracy: 'unknown',
      },
      {
        resetAtMs: null,
        resetAccuracy: 'unknown',
      },
    ]);
    expect(rows[0]).not.toHaveProperty('resetHint');
    expect(rows[1]).not.toHaveProperty('resetHint');
  });

  it('normalizes singular, plural, second, and empty duration units', () => {
    const rows = buildKimiQuotaRows({
      limits: [
        { window: { duration: 30, timeUnit: 'SECONDS' }, detail: { used: 1, limit: 10 } },
        { window: { duration: 45, timeUnit: 'SECOND' }, detail: { used: 1, limit: 10 } },
        { window: { duration: 60, timeUnit: 'MINUTES' }, detail: { used: 1, limit: 10 } },
        { window: { duration: 30, timeUnit: 'MINUTE' }, detail: { used: 1, limit: 10 } },
        { window: { duration: 6, timeUnit: 'HOURS' }, detail: { used: 1, limit: 10 } },
        { window: { duration: 1, timeUnit: 'HOUR' }, detail: { used: 1, limit: 10 } },
        { window: { duration: 7, timeUnit: 'DAYS' }, detail: { used: 1, limit: 10 } },
        { window: { duration: 1, timeUnit: 'DAY' }, detail: { used: 1, limit: 10 } },
        { window: { duration: 300, timeUnit: 'TIME_UNIT_MINUTE' }, detail: { used: 1, limit: 10 } },
        { window: { duration: 90, timeUnit: '' }, detail: { used: 1, limit: 10 } },
      ],
    });

    expect(rows.map((row) => row.labelParams?.duration)).toEqual([
      '30s',
      '45s',
      '1h',
      '30m',
      '6h',
      '1h',
      '7d',
      '1d',
      '5h',
      '90s',
    ]);
  });

  it('normalizes Kimi web fallback usages wrapper', () => {
    const rows = buildKimiQuotaRows({
      usages: [
        {
          scope: 'FEATURE_CODING',
          detail: {
            limit: '2048',
            used: '214',
            remaining: '1834',
            resetTime: '2026-01-09T15:23:13.716839300Z',
          },
          limits: [
            {
              window: { duration: 300, timeUnit: 'TIME_UNIT_MINUTE' },
              detail: {
                limit: '200',
                used: '139',
                remaining: '61',
                resetTime: '2026-01-06T13:33:02.717479433Z',
              },
            },
          ],
        },
      ],
    });

    expect(rows).toEqual([
      expect.objectContaining({
        id: 'usage-0-limit-0',
        labelKey: 'kimi_quota.scoped_limit_window',
        labelParams: { scope: 'Coding', duration: '5h' },
        used: 139,
        limit: 200,
      }),
      expect.objectContaining({
        id: 'usage-0-summary',
        labelKey: 'kimi_quota.scoped_weekly_limit',
        labelParams: { scope: 'Coding' },
        used: 214,
        limit: 2048,
      }),
    ]);
  });

  it('normalizes Kimi #699 payload with 5H limits before Weekly usage and assigns 7D duration to top-level usage', () => {
    const observedAtMs = Date.parse('2026-09-06T19:24:47.694Z');
    const resetTime = '2026-09-13T00:24:47.694450Z';
    const limitResetTime = '2026-09-06T19:24:47.694450Z';
    const rows = buildKimiQuotaRows(
      {
        user: {
          membership: {
            level: 'LEVEL_INTERMEDIATE',
          },
        },
        usage: {
          limit: '100',
          used: '17',
          remaining: '83',
          resetTime,
        },
        limits: [
          {
            window: {
              duration: 300,
              timeUnit: 'TIME_UNIT_MINUTE',
            },
            detail: {
              limit: '100',
              used: '16',
              remaining: '84',
              resetTime: limitResetTime,
            },
          },
        ],
        parallel: {
          limit: '20',
        },
        authentication: {
          method: 'METHOD_ACCESS_TOKEN',
          scope: 'FEATURE_CODING',
        },
        subType: 'TYPE_PURCHASE',
        domain: 'DOMAIN_NEXUS',
        version: 'GOODS_VERSION_V1',
      } as unknown as KimiUsagePayload,
      { observedAtMs }
    );

    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({
      id: 'limit-0',
      used: 16,
      limit: 100,
      limitWindowSeconds: 18_000,
      resetAtMs: Date.parse(limitResetTime),
    });
    expect(rows[1]).toMatchObject({
      id: 'summary',
      labelKey: 'kimi_quota.weekly_limit',
      used: 17,
      limit: 100,
      limitWindowSeconds: 604_800,
      resetAtMs: Date.parse(resetTime),
    });
  });

  it('normalizes Kimi top-level summary-only usage as 7D weekly quota', () => {
    const resetTime = '2026-09-13T00:24:47.694450Z';
    const rows = buildKimiQuotaRows({
      usage: {
        limit: '100',
        used: '17',
        remaining: '83',
        resetTime,
      },
    });

    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      id: 'summary',
      labelKey: 'kimi_quota.weekly_limit',
      used: 17,
      limit: 100,
      limitWindowSeconds: 604_800,
      resetAtMs: Date.parse(resetTime),
    });
  });

  it('does not alter Kimi normalization based on membership level', () => {
    const resetTime = '2026-09-13T00:24:47.694450Z';
    const rows = buildKimiQuotaRows({
      user: {
        membership: {
          level: 'LEVEL_ADVANCED',
        },
      },
      usage: {
        limit: '200',
        used: '50',
        resetTime,
      },
    } as unknown as KimiUsagePayload);

    expect(rows[0]).toMatchObject({
      id: 'summary',
      limitWindowSeconds: 604_800,
      used: 50,
      limit: 200,
      resetAtMs: Date.parse(resetTime),
    });
  });
});
