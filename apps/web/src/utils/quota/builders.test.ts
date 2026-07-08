import { describe, expect, it } from 'vitest';
import { buildAntigravityQuotaGroups, buildKimiQuotaRows } from './builders';

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

    expect(groups.map((group) => group.label)).toEqual(['Claude/GPT', 'Gemini']);
    expect(groups.find((group) => group.id === 'claude-gpt')?.buckets[0]).toMatchObject({
      label: 'Claude/GPT',
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
});

describe('buildKimiQuotaRows', () => {
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
      '90s',
    ]);
  });
});
