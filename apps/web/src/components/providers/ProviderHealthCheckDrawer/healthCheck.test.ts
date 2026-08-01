import { describe, expect, it } from 'vitest';
import type { OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import { buildProviderRows } from '../ProviderTable/rowData';
import type { ProviderRecentUsageMap } from '../utils';
import {
  buildProviderHealthCheckItems,
  getProviderHealthCheckApplyActions,
  summarizeProviderHealthCheckItems,
  type ProviderHealthCheckItem,
} from './healthCheck';

const emptyUsageByProvider = new Map() as ProviderRecentUsageMap;

describe('provider health check model', () => {
  it('expands key-based providers and OpenAI key entries into check items', () => {
    const codex: ProviderKeyConfig[] = [
      {
        apiKey: 'sk-codex-key-123456',
        baseUrl: 'https://codex.example.com/v1',
      },
    ];
    const openai: OpenAIProviderConfig[] = [
      {
        name: 'mixed',
        baseUrl: 'https://mixed.example.com/v1',
        apiKeyEntries: [{ apiKey: 'key-a' }, { apiKey: 'key-b', authIndex: 'auth-b' }],
      },
    ];

    const rows = buildProviderRows({
      gemini: [],
      codex,
      claude: [],
      vertex: [],
      openai,
      usageByProvider: emptyUsageByProvider,
    });
    const items = buildProviderHealthCheckItems(rows);

    expect(items).toHaveLength(3);
    expect(items[0]).toMatchObject({
      providerKind: 'codex',
      providerIndex: 0,
    });
    expect(items[0]).not.toHaveProperty('openAIKeyIndex');
    expect(items[1]).toMatchObject({
      providerKind: 'openai',
      providerIndex: 0,
      openAIKeyIndex: 0,
      providerLabel: 'OpenAI · mixed',
      providerSubtitle: 'https://mixed.example.com/v1',
      targetLabel: 'Key #1',
    });
    expect(items[2]).toMatchObject({
      providerKind: 'openai',
      providerIndex: 0,
      openAIKeyIndex: 1,
      providerLabel: 'OpenAI · mixed',
      targetLabel: 'Key #2',
      detailLabel: 'auth-index: auth-b',
    });
  });

  it('builds xAI API key health-check items with xAI identity', () => {
    const rows = buildProviderRows({
      gemini: [],
      codex: [],
      xai: [{ apiKey: 'xai-key', baseUrl: 'https://api.x.ai/v1' }],
      claude: [],
      vertex: [],
      openai: [],
      usageByProvider: emptyUsageByProvider,
    });

    expect(buildProviderHealthCheckItems(rows)).toEqual([
      expect.objectContaining({
        providerKind: 'xai',
        providerLabel: expect.stringContaining('xAI'),
        providerSubtitle: 'https://api.x.ai/v1',
      }),
    ]);
  });

  it('summarizes progress from item statuses', () => {
    const items = [
      { status: 'success' },
      { status: 'error' },
      { status: 'running' },
      { status: 'pending' },
    ] as ProviderHealthCheckItem[];

    expect(summarizeProviderHealthCheckItems(items)).toEqual({
      total: 4,
      pending: 1,
      running: 1,
      success: 1,
      error: 1,
      completed: 2,
      percent: 50,
    });
  });

  it('enables OpenAI providers when any key succeeds and disables when all keys fail', () => {
    const items = [
      { providerKey: 'openai:a', status: 'error' },
      { providerKey: 'openai:a', status: 'success' },
      { providerKey: 'openai:b', status: 'error' },
      { providerKey: 'openai:b', status: 'error' },
      { providerKey: 'codex:c', status: 'pending' },
    ] as ProviderHealthCheckItem[];

    const actions = getProviderHealthCheckApplyActions(items);

    expect(actions.get('openai:a')).toBe('enable');
    expect(actions.get('openai:b')).toBe('disable');
    expect(actions.has('codex:c')).toBe(false);
  });
});
