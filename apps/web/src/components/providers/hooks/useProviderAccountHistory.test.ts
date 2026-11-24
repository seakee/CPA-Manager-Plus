import { describe, expect, it } from 'vitest';
import type { MonitoringAccountHistoryItem } from '@/services/api/usageService';
import { buildProviderRows } from '../ProviderTable/rowData';
import type { ProviderRecentUsageMap } from '../utils';
import {
  aggregateProviderHistoryItems,
  buildProviderHistoryStatusBarData,
  buildProviderHistoryTargets,
} from './useProviderAccountHistory';

describe('useProviderAccountHistory helpers', () => {
  it('builds deduplicated targets across single-key, interactions, and multi-key OpenAI providers', () => {
    const targets = buildProviderHistoryTargets({
      gemini: [{ apiKey: 'sk-gem', authIndex: 'gem-idx-1' }],
      interactions: [{ apiKey: 'sk-int', authIndex: 'int-idx-1' }],
      codex: [{ apiKey: 'sk-cdx', authIndex: 'cdx-idx-1' }],
      xai: [{ apiKey: 'sk-xai', authIndex: 'xai-idx-1' }],
      claude: [{ apiKey: 'sk-cla', authIndex: 'cla-idx-1' }],
      vertex: [{ apiKey: 'sk-vtx' }],
      openai: [
        {
          name: 'OpenRouter',
          baseUrl: 'https://openrouter.ai/api/v1',
          authIndex: 'oa-idx-1',
          apiKeyEntries: [
            { apiKey: 'sk-oa-1', authIndex: 'oa-idx-1' },
            { apiKey: 'sk-oa-2', authIndex: 'oa-idx-2' },
          ],
        },
      ],
    });

    expect(targets.map((entry) => entry.target.auth_provider_snapshot)).toEqual([
      'gemini',
      'gemini-interactions',
      'codex',
      'xai',
      'claude',
      'openai-compatibility',
      'openai-compatibility',
    ]);
    expect(targets.map((entry) => entry.target.auth_index)).toEqual([
      'gem-idx-1',
      'int-idx-1',
      'cdx-idx-1',
      'xai-idx-1',
      'cla-idx-1',
      'oa-idx-1',
      'oa-idx-2',
    ]);
  });

  it('aggregates multiple OpenAI entry histories onto one provider row and builds a 10-slot StatusBarData', () => {
    const lists = {
      gemini: [],
      interactions: [],
      codex: [],
      xai: [],
      claude: [],
      vertex: [],
      openai: [
        {
          name: 'OpenRouter',
          baseUrl: 'https://openrouter.ai/api/v1',
          apiKeyEntries: [
            { apiKey: 'sk-oa-1', authIndex: 'oa-idx-1' },
            { apiKey: 'sk-oa-2', authIndex: 'oa-idx-2' },
          ],
        },
      ],
    };
    const targets = buildProviderHistoryTargets(lists);
    expect(targets).toHaveLength(2);

    const items: MonitoringAccountHistoryItem[] = [
      {
        row_key: targets[0].target.row_key,
        account_key: 'k1',
        matched: true,
        sync_status: 'complete',
        total_requests: 12,
        success_calls: 11,
        failure_calls: 1,
        total_tokens: 120_000,
        total_cost: 0.45,
        success_rate: 91.66,
        first_seen_ms: 1_000,
        last_seen_ms: 2_000,
        latest_request: { timestamp_ms: 2_000, failed: true, fail_status_code: 429 },
        recent_requests: [
          { timestamp_ms: 1_000, failed: false },
          { timestamp_ms: 2_000, failed: true, fail_status_code: 429 },
        ],
      },
      {
        row_key: targets[1].target.row_key,
        account_key: 'k2',
        matched: true,
        sync_status: 'complete',
        total_requests: 8,
        success_calls: 8,
        failure_calls: 0,
        total_tokens: 80_000,
        total_cost: 0.3,
        success_rate: 100,
        first_seen_ms: 1_500,
        last_seen_ms: 3_000,
        latest_request: { timestamp_ms: 3_000, failed: false },
        recent_requests: [{ timestamp_ms: 3_000, failed: false }],
      },
    ];

    const historyMap = aggregateProviderHistoryItems(targets, items);
    const openAIRowKey = targets[0].providerRowKey;
    const aggregated = historyMap.get(openAIRowKey);
    expect(aggregated).toBeDefined();
    expect(aggregated?.totalRequests).toBe(20);
    expect(aggregated?.successCalls).toBe(19);
    expect(aggregated?.failureCalls).toBe(1);
    expect(aggregated?.totalTokens).toBe(200_000);
    expect(aggregated?.totalCost).toBeCloseTo(0.75, 5);
    expect(aggregated?.lastSeenMs).toBe(3_000);
    expect(aggregated?.recentStatusData.blocks.slice(-3)).toEqual([
      'success',
      'failure',
      'success',
    ]);

    const rows = buildProviderRows({
      ...lists,
      usageByProvider: new Map() as ProviderRecentUsageMap,
      historyByRowKey: historyMap,
    });
    expect(rows[0].stats).toEqual({ success: 19, failure: 1 });
    expect(rows[0].history?.totalTokens).toBe(200_000);
    expect(rows[0].history?.recentStatusData.totalSuccess).toBe(2);
    expect(rows[0].history?.recentStatusData.totalFailure).toBe(1);
    expect(rows[0].recentSuccess).toBe(2);
    expect(rows[0].recentFailure).toBe(1);
    expect(rows[0].statusData.blocks).toHaveLength(10);
    expect(rows[0].statusData.totalSuccess).toBe(0);
  });

  it('pads recent requests to 10 blocks in chronological order', () => {
    const bar = buildProviderHistoryStatusBarData([
      { timestamp_ms: 2_000, failed: true },
      { timestamp_ms: 1_000, failed: false },
    ]);
    expect(bar.blocks).toHaveLength(10);
    expect(bar.blocks.slice(0, 8)).toEqual(Array(8).fill('idle'));
    expect(bar.blocks.slice(8)).toEqual(['success', 'failure']);
    expect(bar.successRate).toBe(50);
  });
});
