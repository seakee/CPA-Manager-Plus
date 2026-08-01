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
    expect(openaiRow?.stats).toEqual({ success: 3, failure: 3 });
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
});
