import { createElement, StrictMode } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  monitoringAnalyticsApi,
  type MonitoringAccountHistoryItem,
} from '@/services/api/usageService';
import { buildProviderRows } from '../ProviderTable/rowData';
import type { ProviderRecentUsageMap } from '../utils';
import {
  aggregateProviderHistoryItems,
  buildProviderHistoryStatusBarData,
  buildProviderHistoryTargets,
  useProviderAccountHistory,
} from './useProviderAccountHistory';

const authState = vi.hoisted(() => ({
  connectionStatus: 'connected',
  managementKey: 'test-mgmt-key',
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (s: typeof authState) => unknown) => selector(authState),
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => ({
    managerServiceAvailable: true,
    managerServiceBase: 'http://127.0.0.1:18319',
    requestMonitoringAvailable: true,
    checking: false,
    reason: '',
  }),
}));

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
    expect(rows[0].statusData.totalSuccess).toBe(2);
    expect(rows[0].statusData.totalFailure).toBe(1);
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

describe('useProviderAccountHistory re-entry cooldown', () => {
  let renderer: ReactTestRenderer | undefined;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-11-26T12:00:00Z'));
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    authState.connectionStatus = 'connected';
    authState.managementKey = 'test-mgmt-key';
  });

  afterEach(async () => {
    if (renderer) {
      await act(async () => renderer?.unmount());
    }
    renderer = undefined;
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('skips duplicate account-history fetch when leaving and re-entering within 5s cooldown, and revalidates after 5s', async () => {
    const getAccountHistorySpy = vi
      .spyOn(monitoringAnalyticsApi, 'getAccountHistory')
      .mockResolvedValue({
        generated_at_ms: Date.now(),
        checkpoint: { last_event_id: 1, latest_id: 1, pending: false, processed: 0 },
        items: [
          {
            row_key: 'openai:minimax:0',
            account_key: 'k1',
            matched: true,
            sync_status: 'complete',
            total_requests: 10,
            success_calls: 10,
            failure_calls: 0,
            total_tokens: 43144,
            total_cost: 0.01,
            success_rate: 100,
            first_seen_ms: 1_000,
            last_seen_ms: 2_000,
            latest_request: null,
            recent_requests: [],
          },
        ],
      });

    const openaiList = [
      {
        name: 'Minimax',
        baseUrl: 'https://api.minimaxi.com/v1',
        apiKeyEntries: [{ apiKey: 'sk-mm-1', authIndex: 'b375c6488dddc736' }],
      },
    ];

    function Harness({ enabled }: { enabled: boolean }) {
      useProviderAccountHistory({
        enabled,
        reentryCooldownMs: 5_000,
        gemini: [],
        codex: [],
        claude: [],
        vertex: [],
        openai: openaiList,
      });
      return null;
    }

    await act(async () => {
      renderer = create(createElement(Harness, { enabled: true }));
    });
    expect(getAccountHistorySpy).toHaveBeenCalledTimes(1);

    // Leave and return within 2s (< 5s cooldown): must NOT fire another request.
    vi.advanceTimersByTime(2_000);
    await act(async () => {
      renderer!.update(createElement(Harness, { enabled: false }));
    });
    await act(async () => {
      renderer!.update(createElement(Harness, { enabled: true }));
    });
    expect(getAccountHistorySpy).toHaveBeenCalledTimes(1);

    // Advance past 5s cooldown, leave and return: MUST revalidate.
    vi.advanceTimersByTime(4_000);
    await act(async () => {
      renderer!.update(createElement(Harness, { enabled: false }));
    });
    await act(async () => {
      renderer!.update(createElement(Harness, { enabled: true }));
    });
    expect(getAccountHistorySpy).toHaveBeenCalledTimes(2);
  });

  it('starts a fresh fetch on StrictMode replay instead of joining an aborted in-flight tracker', async () => {
    const signals: AbortSignal[] = [];
    let resolveSecond!: (value: {
      generated_at_ms: number;
      checkpoint: { last_event_id: number; latest_id: number; pending: boolean; processed: number };
      items: MonitoringAccountHistoryItem[];
    }) => void;

    const getAccountHistorySpy = vi
      .spyOn(monitoringAnalyticsApi, 'getAccountHistory')
      .mockImplementation((_base, _key, _body, signal) => {
        if (signal) signals.push(signal);
        if (signals.length === 1) {
          // First mount request stays pending until StrictMode cleanup aborts it.
          return new Promise(() => {});
        }
        return new Promise((resolve) => {
          resolveSecond = resolve;
        });
      });

    const openaiList = [
      {
        name: 'Minimax',
        baseUrl: 'https://api.minimaxi.com/v1',
        apiKeyEntries: [{ apiKey: 'sk-mm-1', authIndex: 'b375c6488dddc736' }],
      },
    ];
    const targetRowKey = buildProviderHistoryTargets({
      gemini: [],
      codex: [],
      claude: [],
      vertex: [],
      openai: openaiList,
    })[0].target.row_key;

    function Harness() {
      const { historyByRowKey } = useProviderAccountHistory({
        enabled: true,
        reentryCooldownMs: 5_000,
        gemini: [],
        codex: [],
        claude: [],
        vertex: [],
        openai: openaiList,
      });
      const total = Array.from(historyByRowKey.values())[0]?.totalRequests ?? 0;
      return createElement('span', { 'data-total': total });
    }

    await act(async () => {
      renderer = create(createElement(StrictMode, null, createElement(Harness)));
    });

    expect(getAccountHistorySpy).toHaveBeenCalledTimes(2);
    expect(signals[0]?.aborted).toBe(true);
    expect(signals[1]?.aborted).toBe(false);

    await act(async () => {
      resolveSecond({
        generated_at_ms: Date.now(),
        checkpoint: { last_event_id: 1, latest_id: 1, pending: false, processed: 0 },
        items: [
          {
            row_key: targetRowKey,
            account_key: 'k1',
            matched: true,
            sync_status: 'complete',
            total_requests: 10,
            success_calls: 10,
            failure_calls: 0,
            total_tokens: 43144,
            total_cost: 0.01,
            success_rate: 100,
            first_seen_ms: 1_000,
            last_seen_ms: 2_000,
            latest_request: null,
            recent_requests: [],
          },
        ],
      });
      await Promise.resolve();
    });

    expect(renderer!.root.findByType('span').props['data-total']).toBe(10);
  });
});
