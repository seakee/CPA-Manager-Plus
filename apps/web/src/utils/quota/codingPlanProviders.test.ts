import { describe, expect, it } from 'vitest';
import type { AuthFileItem } from '@/types';
import type { Config } from '@/types/config';
import {
  buildZhipuAuthIndexBaseMap,
  computeStableAuthIndex,
  fetchCodingPlanAuthFiles,
  isOpencodeProvider,
  mergeCodingPlanAuthFiles,
  resolveCodingPlanProvider,
} from './codingPlanProviders';

const file = (overrides: Partial<AuthFileItem>): AuthFileItem => ({
  name: 'test.json',
  ...overrides,
});

describe('buildZhipuAuthIndexBaseMap', () => {
  it('maps auth indices of bigmodel/z.ai keys to their origins', () => {
    const config = {
      claudeApiKeys: [
        { apiKey: 'k1', baseUrl: 'https://open.bigmodel.cn/api/anthropic', authIndex: 'idx-1' },
        { apiKey: 'k2', baseUrl: 'https://api.z.ai/api/anthropic', authIndex: 'idx-2' },
        { apiKey: 'k3', baseUrl: 'https://api.deepseek.com/anthropic', authIndex: 'idx-3' },
        { apiKey: 'k4', baseUrl: 'https://open.bigmodel.cn/api/anthropic' },
      ],
    } as unknown as Config;

    const map = buildZhipuAuthIndexBaseMap(config);
    expect(map.get('idx-1')).toBe('https://open.bigmodel.cn');
    expect(map.get('idx-2')).toBe('https://api.z.ai');
    expect(map.has('idx-3')).toBe(false);
    expect(map.size).toBe(2);
  });

  it('handles null config', () => {
    expect(buildZhipuAuthIndexBaseMap(null).size).toBe(0);
  });
});

describe('resolveCodingPlanProvider', () => {
  const zhipuBases = new Map([['idx-z', 'https://open.bigmodel.cn']]);

  it('identifies zhipu claude-api-key credentials by auth index', () => {
    expect(resolveCodingPlanProvider(file({ provider: 'claude', authIndex: 'idx-z' }), zhipuBases)).toBe('zhipu');
    expect(resolveCodingPlanProvider(file({ provider: 'claude', 'auth_index': 'idx-z' }), zhipuBases)).toBe('zhipu');
  });

  it('leaves ordinary claude credentials alone', () => {
    expect(resolveCodingPlanProvider(file({ provider: 'claude', authIndex: 'other' }), zhipuBases)).toBeNull();
    expect(resolveCodingPlanProvider(file({ provider: 'claude' }), zhipuBases)).toBeNull();
  });

  it('identifies opencode by compat-name provider', () => {
    expect(resolveCodingPlanProvider(file({ provider: 'opencode-go' }), zhipuBases)).toBe('opencode');
    expect(resolveCodingPlanProvider(file({ provider: 'OpenCode-42' }), zhipuBases)).toBe('opencode');
    expect(isOpencodeProvider('opencode')).toBe(true);
    expect(isOpencodeProvider('codex')).toBe(false);
  });
});

describe('fetchCodingPlanAuthFiles / mergeCodingPlanAuthFiles', () => {
  const config = {
    claudeApiKeys: [
      {
        apiKey: 'abcdefgh1234',
        baseUrl: 'https://open.bigmodel.cn/api/anthropic',
      },
      {
        apiKey: 'otherkey9999',
        baseUrl: 'https://api.deepseek.com/anthropic',
      },
    ],
    openaiCompatibility: [
      {
        name: 'opencode-go',
        baseUrl: 'https://opencode.ai/zen/go/v1',
        apiKeyEntries: [{ apiKey: 'sk-opencode7777' }],
      },
      {
        name: 'other-compat',
        baseUrl: 'https://example.com/v1',
        apiKeyEntries: [{ apiKey: 'sk-x' }],
      },
    ],
  } as unknown as Config;

  it('synthesizes zhipu and opencode rows with replicated auth indices', async () => {
    const synthesized = await fetchCodingPlanAuthFiles(config);
    expect(synthesized.map((f) => f.provider)).toEqual(['zhipu', 'opencode']);
    expect(synthesized[0]).toMatchObject({
      provider: 'zhipu',
      runtimeOnly: 'true',
      status: 'active',
    });
    // authIndex replicates coreauth.stableAuthIndex: sha256(seed)[:8] hex.
    const seed = 'claude-api-key:https://open.bigmodel.cn/api/anthropic+abcdefgh1234';
    const expected = await computeStableAuthIndex(seed);
    expect(synthesized[0].authIndex).toBe(expected);
    expect((synthesized[0] as Record<string, unknown>).zhipu_base_url).toBe(
      'https://open.bigmodel.cn'
    );
    expect(synthesized[0].label).toContain('1234');
    expect(synthesized[1]).toMatchObject({ provider: 'opencode' });
    expect(synthesized[1].label).toContain('7777');
    const ocSeed = 'openai-compatibility:https://opencode.ai/zen/go/v1+sk-opencode7777';
    expect(synthesized[1].authIndex).toBe(await computeStableAuthIndex(ocSeed));
  });

  it('marks disabled compat providers', async () => {
    const cfg = {
      openaiCompatibility: [
        {
          name: 'opencode-go',
          baseUrl: 'https://opencode.ai/zen/go/v1',
          disabled: true,
          apiKeyEntries: [{ apiKey: 'sk-opencode7777' }],
        },
      ],
    } as unknown as Config;
    const synthesized = await fetchCodingPlanAuthFiles(cfg);
    expect(synthesized[0].disabled).toBe(true);
  });

  it('maps excluded-models ["*"] to disabled for zhipu keys', async () => {
    const cfg = {
      claudeApiKeys: [
        {
          apiKey: 'abcdefgh1234',
          baseUrl: 'https://open.bigmodel.cn/api/anthropic',
          excludedModels: ['*'],
        },
        {
          apiKey: 'zzzzzzzz9999',
          baseUrl: 'https://api.z.ai/api/anthropic',
        },
      ],
    } as unknown as Config;
    const synthesized = await fetchCodingPlanAuthFiles(cfg);
    expect(synthesized[0].disabled).toBe(true);
    expect(synthesized[1].disabled).toBeUndefined();
  });

  it('skips auth indices already present in backend files', () => {
    const backend = [
      { name: 'real.json', provider: 'zhipu', authIndex: 'z1' },
    ] as import('@/types').AuthFileItem[];
    const synthesized = [
      { name: 'zhipu-coding-plan-1', provider: 'zhipu', authIndex: 'z1' },
      { name: 'opencode-go-1', provider: 'opencode', authIndex: 'o1' },
    ] as import('@/types').AuthFileItem[];
    expect(mergeCodingPlanAuthFiles(backend, synthesized).map((f) => f.authIndex)).toEqual([
      'z1',
      'o1',
    ]);
  });
});
