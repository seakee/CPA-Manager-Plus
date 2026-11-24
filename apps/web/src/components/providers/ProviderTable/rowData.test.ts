import { describe, expect, it } from 'vitest';
import type { OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import { buildRecentRequestCompositeKey } from '@/utils/recentRequests';
import type { ProviderRecentUsageMap } from '../utils';
import { buildProviderRows } from './rowData';

const emptyInput = {
  gemini: [],
  codex: [],
  claude: [],
  vertex: [],
  openai: [],
  usageByProvider: new Map() as ProviderRecentUsageMap,
};

describe('buildProviderRows', () => {
  it('maps key-based configs to rows with masked label and enabled flag', () => {
    const codex: ProviderKeyConfig[] = [
      {
        apiKey: 'sk-test-key-123456',
        baseUrl: 'https://codex.example.com/v1',
        priority: 7,
        models: [{ name: 'gpt-5.4' }, { name: 'gpt-5.5' }],
      },
      {
        apiKey: 'sk-disabled-key',
        baseUrl: 'https://disabled.example.com/v1',
        excludedModels: ['*', 'gpt-5.4'],
      },
    ];

    const rows = buildProviderRows({ ...emptyInput, codex });

    expect(rows).toHaveLength(2);
    expect(rows[0].kind).toBe('codex');
    expect(rows[0].originalIndex).toBe(0);
    expect(rows[0].label).not.toContain('test-key');
    expect(rows[0].label.startsWith('sk')).toBe(true);
    expect(rows[0].enabled).toBe(true);
    expect(rows[0].modelCount).toBe(2);
    expect(rows[0].modelNames).toEqual(['gpt-5.4', 'gpt-5.5']);
    expect(rows[0].keyCount).toBe(1);

    expect(rows[1].enabled).toBe(false);
    expect(rows[1].originalIndex).toBe(1);
  });

  it('maps xAI API key configs as a distinct provider kind', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      xai: [
        {
          apiKey: 'xai-secret-key',
          baseUrl: 'https://api.x.ai/v1',
          websockets: true,
          models: [{ name: 'grok-4.5' }],
        },
      ],
    });

    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      kind: 'xai',
      baseUrl: 'https://api.x.ai/v1',
      modelNames: ['grok-4.5'],
      enabled: true,
    });
  });

  it('maps openai providers with name label, key count and disabled flag', () => {
    const openai: OpenAIProviderConfig[] = [
      {
        name: 'qwen2api',
        baseUrl: 'https://qwen.example.com/v1',
        apiKeyEntries: [{ apiKey: 'k1' }, { apiKey: 'k2' }],
        models: [{ name: 'qwen3.6-plus' }],
        disabled: true,
      },
    ];

    const rows = buildProviderRows({ ...emptyInput, openai });

    expect(rows).toHaveLength(1);
    expect(rows[0].kind).toBe('openai');
    expect(rows[0].label).toBe('qwen2api');
    expect(rows[0].keyCount).toBe(2);
    expect(rows[0].enabled).toBe(false);
    expect(rows[0].haystack).toContain('qwen2api');
    expect(rows[0].haystack).toContain('https://qwen.example.com/v1');
  });

  it('aggregates stats from usage map for key configs and openai entries', () => {
    const usageByProvider: ProviderRecentUsageMap = new Map([
      [
        'codex',
        new Map([
          [
            buildRecentRequestCompositeKey('https://codex.example.com/v1', 'sk-1'),
            { success: 5, failed: 2, recentRequests: [] },
          ],
        ]),
      ],
      [
        'mixed',
        new Map([
          [
            buildRecentRequestCompositeKey('https://mixed.example.com/v1', 'k1'),
            { success: 1, failed: 0, recentRequests: [] },
          ],
          [
            buildRecentRequestCompositeKey('https://mixed.example.com/v1', 'k2'),
            { success: 2, failed: 3, recentRequests: [] },
          ],
        ]),
      ],
    ]);

    const rows = buildProviderRows({
      ...emptyInput,
      codex: [{ apiKey: 'sk-1', baseUrl: 'https://codex.example.com/v1' }],
      openai: [
        {
          name: 'mixed',
          baseUrl: 'https://mixed.example.com/v1',
          apiKeyEntries: [{ apiKey: 'k1' }, { apiKey: 'k2' }],
        },
      ],
      usageByProvider,
    });

    const codexRow = rows.find((row) => row.kind === 'codex');
    const openaiRow = rows.find((row) => row.kind === 'openai');

    expect(codexRow?.stats).toEqual({ success: 5, failure: 2 });
    expect(codexRow?.usageStatsCoverage).toBe('full');
    expect(openaiRow?.stats).toEqual({ success: 3, failure: 3 });
    expect(openaiRow?.keyCount).toBe(2);
    expect(openaiRow?.usageStatsCoverage).toBe('full');
  });

  it('does not interpret unsupported empty-key usage records as keyless OpenAI stats', () => {
    const baseUrl = 'https://keyless.example.com/v1';
    const usageByProvider: ProviderRecentUsageMap = new Map([
      [
        'keyless',
        new Map([
          [
            buildRecentRequestCompositeKey(baseUrl, ''),
            {
              success: 8,
              failed: 1,
              recentRequests: [],
            },
          ],
        ]),
      ],
    ]);
    const rows = buildProviderRows({
      ...emptyInput,
      openai: [{ name: 'keyless', baseUrl, apiKeyEntries: [] }],
      usageByProvider,
    });

    expect(rows[0].stats).toEqual({ success: 0, failure: 0 });
    expect(rows[0].keyCount).toBe(0);
    expect(rows[0].usageStatsCoverage).toBe('unavailable');
  });

  it('marks mixed keyed/keyless OpenAI rows as partial statistics', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      openai: [
        {
          name: 'mixed-keyless',
          baseUrl: 'https://mixed-keyless.example/v1',
          apiKeyEntries: [
            { apiKey: 'tracked-key' },
            { apiKey: '', proxyUrl: 'socks5://proxy:1080' },
          ],
        },
      ],
    });

    expect(rows[0].keyCount).toBe(1);
    expect(rows[0].usageStatsCoverage).toBe('partial');
  });

  it('keeps row keys unique across kinds with identical configs', () => {
    const config: ProviderKeyConfig = {
      apiKey: 'sk-same',
      baseUrl: 'https://same.example.com/v1',
    };

    const rows = buildProviderRows({
      ...emptyInput,
      codex: [config],
      claude: [{ ...config }],
      vertex: [{ ...config }],
    });

    const keys = rows.map((row) => row.key);
    expect(new Set(keys).size).toBe(keys.length);
  });

  it('prefers configured provider alias for label, sortName, and haystack while preserving maskedKey preview', () => {
    const rows = buildProviderRows({
      ...emptyInput,
      codex: [
        {
          name: 'Codex Primary Pool',
          apiKey: 'sk-codex-secret-9988',
          baseUrl: 'https://codex-primary.example/v1',
          prefix: 'cx',
          proxyUrl: 'http://127.0.0.1:7890',
          weight: 3,
          cloak: { mode: 'always' },
          disableCooling: true,
          excludedModels: ['*', 'gpt-4.1', 'o1-preview'],
        },
      ],
    });

    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      alias: 'Codex Primary Pool',
      label: 'Codex Primary Pool',
      sortName: 'Codex Primary Pool',
      prefix: 'cx',
      hasProxy: true,
      weight: 3,
      cloakMode: 'always',
      disableCooling: true,
      excludedModelCount: 2,
      enabled: false,
    });
    expect(rows[0].maskedKey.startsWith('sk')).toBe(true);
    expect(rows[0].haystack).toContain('codex primary pool');
  });

  it('prefers SQLite history metrics while keeping statusData assembled from CPA recent buckets', () => {
    const usageByProvider: ProviderRecentUsageMap = new Map([
      [
        'claude',
        new Map([
          [
            buildRecentRequestCompositeKey('https://api.anthropic.com', 'sk-ant-1'),
            {
              success: 19,
              failed: 1,
              recentRequests: [
                { time: '16:40-16:50', success: 2, failed: 0 },
                { time: '16:50-17:00', success: 0, failed: 0 },
                ...Array.from({ length: 16 }, () => ({ time: '17:00-17:10', success: 0, failed: 0 })),
                { time: '19:40-19:50', success: 3, failed: 0 },
                { time: '19:50-20:00', success: 1, failed: 1 },
              ],
            },
          ],
        ]),
      ],
    ]);
    const historyStatusData = {
      blocks: Array.from({ length: 10 }, (_, idx) => (idx === 4 ? ('failure' as const) : ('success' as const))),
      blockDetails: Array.from({ length: 10 }, (_, idx) => ({
        success: idx === 4 ? 0 : 1,
        failure: idx === 4 ? 1 : 0,
        rate: idx === 4 ? 0 : 1,
        startTime: 1_700_000_000_000 + idx * 60_000,
        endTime: 1_700_000_060_000 + idx * 60_000,
      })),
      successRate: 90,
      totalSuccess: 9,
      totalFailure: 1,
    };
    const historyByRowKey = new Map([
      [
        'claude:sk-ant-1::https://api.anthropic.com::::0',
        {
          matched: true,
          totalRequests: 40,
          successCalls: 38,
          failureCalls: 2,
          totalTokens: 670120,
          totalCost: 2.15,
          successRate: 95,
          lastSeenMs: 1_700_000_999_000,
          recentRequests: [],
          recentStatusData: historyStatusData,
        },
      ],
    ]);

    const rows = buildProviderRows({
      ...emptyInput,
      claude: [{ name: 'Claude Official', apiKey: 'sk-ant-1', baseUrl: 'https://api.anthropic.com' }],
      usageByProvider,
      historyByRowKey,
    });

    expect(rows[0].stats).toEqual({ success: 38, failure: 2 });
    expect(rows[0].statusData.blocks).toHaveLength(10);
    expect(rows[0].statusData.blocks[0]).toBe('success');
    expect(rows[0].statusData.blockDetails[0]).toMatchObject({ success: 2, failure: 0, rate: 1 });
    expect(rows[0].statusData.blocks[9]).toBe('mixed');
    expect(rows[0].statusData.blockDetails[9]).toMatchObject({
      success: 4,
      failure: 1,
      rate: 0.8,
    });
    expect(rows[0].lastSeenMs).toBe(1_700_000_999_000);
  });

  it('computes displayEndpoint, official endpoint flag, v8 multi-key meta, model aliases, and latestFailure', () => {
    const groupRecord = {
      name: 'Gemini Pool',
      keys: [{ 'api-key': 'g-key-1' }, { 'api-key': 'g-key-2' }],
    };
    const historyStatusData = {
      blocks: ['failure' as const, 'success' as const],
      blockDetails: [],
      successRate: 50,
      totalSuccess: 1,
      totalFailure: 1,
    };
    const rowKey0 = 'gemini:g-key-1::https://generativelanguage.googleapis.com/::::0';
    const historyByRowKey = new Map([
      [
        rowKey0,
        {
          matched: true,
          totalRequests: 12,
          successCalls: 11,
          failureCalls: 1,
          totalTokens: 24000,
          totalCost: 0.15,
          successRate: 91.7,
          lastSeenMs: 1_700_000_500_000,
          recentRequests: [
            {
              timestamp_ms: 1_700_000_500_000,
              failed: false,
            },
            {
              timestamp_ms: 1_700_000_400_000,
              failed: true,
              fail_status_code: 429,
              fail_summary: 'RESOURCE_EXHAUSTED: quota exceeded',
            },
          ],
          recentStatusData: historyStatusData,
        },
      ],
    ]);

    const rows = buildProviderRows({
      ...emptyInput,
      gemini: [
        {
          name: 'Gemini Pool',
          apiKey: 'g-key-1',
          baseUrl: 'https://generativelanguage.googleapis.com/',
          models: [
            { name: 'gemini-2.5-pro', alias: 'g25-pro', displayName: 'Gemini 2.5 Pro' },
            { name: 'gemini-2.5-flash', alias: 'gemini-2.5-flash' },
          ],
          v8Source: {
            family: 'gemini',
            groupIndex: 0,
            keyIndex: 0,
            group: groupRecord,
            groups: [groupRecord],
          },
        },
        {
          name: 'Gemini Pool',
          apiKey: 'g-key-2',
          baseUrl: 'http://127.0.0.1:8080/v1/',
          v8Source: {
            family: 'gemini',
            groupIndex: 0,
            keyIndex: 1,
            group: groupRecord,
            groups: [groupRecord],
          },
        },
      ],
      historyByRowKey,
    });

    expect(rows[0].displayEndpoint).toBe('generativelanguage.googleapis.com');
    expect(rows[0].isOfficialEndpoint).toBe(true);
    expect(rows[0].v8GroupKeyMeta).toEqual({ index: 1, total: 2 });
    expect(rows[0].modelEntries).toEqual([
      { name: 'gemini-2.5-pro', alias: 'g25-pro', displayName: 'Gemini 2.5 Pro' },
      { name: 'gemini-2.5-flash' },
    ]);
    expect(rows[0].modelFilterKeys).toEqual(['gemini-2.5-pro', 'g25-pro', 'gemini-2.5-flash']);
    expect(rows[0].haystack).toContain('gemini 2.5 pro');
    expect(rows[0].statsSource).toBe('history');
    expect(rows[0].latestFailure).toEqual({
      statusCode: 429,
      summary: 'RESOURCE_EXHAUSTED: quota exceeded',
      timestampMs: 1_700_000_400_000,
      isMostRecent: false,
    });

    expect(rows[1].displayEndpoint).toBe('http://127.0.0.1:8080/v1');
    expect(rows[1].isOfficialEndpoint).toBe(false);
    expect(rows[1].v8GroupKeyMeta).toEqual({ index: 2, total: 2 });
    expect(rows[1].statsSource).toBe('none');
    expect(rows[1].latestFailure).toBeUndefined();
  });
});
