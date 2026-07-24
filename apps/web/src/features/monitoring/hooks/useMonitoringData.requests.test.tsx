import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { MonitoringAnalyticsResponse } from '@/services/api/usageService';
import type { MonitoringDataTab } from '../monitoringCenterUiState';
import type { UseMonitoringAnalyticsParams } from './useMonitoringAnalytics';

const { useMonitoringAnalyticsMock } = vi.hoisted(() => ({
  useMonitoringAnalyticsMock: vi.fn(),
}));

vi.mock('../services/monitoringMetaService', () => ({
  loadMonitoringMetaPayload: vi.fn(async () => ({
    authFiles: [],
    channels: [],
    error: '',
  })),
}));

vi.mock('./useMonitoringAnalytics', () => ({
  useMonitoringAnalytics: useMonitoringAnalyticsMock,
}));

import { useMonitoringData, type UseMonitoringDataReturn } from './useMonitoringData';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const refresh = vi.fn(async () => undefined);
const lastRefreshedAt = new Date(1_800_000_000_000);
const modelPrices = {};
const scopeFilters = { model: 'gpt-active' };
const summary = {
  total_calls: 2,
  success_calls: 2,
  failure_calls: 0,
  success_rate: 1,
  input_tokens: 20,
  output_tokens: 10,
  reasoning_tokens: 0,
  cached_tokens: 0,
  cache_read_tokens: 0,
  cache_creation_tokens: 0,
  cache_hit_rate: 0,
  total_tokens: 30,
  total_cost: 0,
  average_cost_per_call: 0,
  average_latency_ms: null,
  p95_latency_ms: null,
  p95_ttft_ms: null,
  zero_token_calls: 0,
  rpm_30m: 0,
  tpm_30m: 0,
  avg_daily_requests: 0,
  avg_daily_tokens: 0,
  approx_tasks: 0,
  approx_task_failures: 0,
  approx_task_success_rate: 1,
  zero_token_models: [],
};

const mainResponse: MonitoringAnalyticsResponse = {
  generated_at_ms: 1_800_000_000_000,
  granularity: 'hour',
  summary,
};

const events = {
  items: [
    {
      event_hash: 'event-1',
      timestamp_ms: 1_800_000_000_000,
      model: 'gpt-active',
      endpoint: '/v1/responses',
      method: 'POST',
      path: '/v1/responses',
      auth_index: 'auth-a',
      source: 'alice.json',
      source_hash: 'source-a',
      api_key_hash: 'key-a',
      account_snapshot: 'alice@example.com',
      auth_label_snapshot: 'Alice',
      auth_provider_snapshot: 'codex',
      input_tokens: 10,
      output_tokens: 5,
      cached_tokens: 0,
      cache_read_tokens: 0,
      cache_creation_tokens: 0,
      reasoning_tokens: 0,
      total_tokens: 15,
      latency_ms: 100,
      failed: false,
    },
  ],
  next_before_ms: 1_799_999_999_000,
  next_before_id: 7,
  has_more: true,
  total_count: 2,
};

const accountsResponse: MonitoringAnalyticsResponse = {
  ...mainResponse,
  account_stats: [
    {
      id: 'alice@example.com',
      account_snapshot: 'alice@example.com',
      auth_label_snapshot: 'Alice',
      auth_provider_snapshot: 'codex',
      auth_indices: ['auth-a'],
      sources: ['alice.json'],
      source_hashes: ['source-a'],
      calls: 2,
      success_calls: 2,
      failure_calls: 0,
      success_rate: 1,
      input_tokens: 20,
      output_tokens: 10,
      cached_tokens: 0,
      cache_read_tokens: 0,
      cache_creation_tokens: 0,
      total_tokens: 30,
      cost: 0,
      average_latency_ms: 100,
      last_seen_ms: 1_800_000_000_000,
    },
  ],
  events,
};

const apiKeysResponse: MonitoringAnalyticsResponse = {
  ...mainResponse,
  api_key_stats: [
    {
      id: 'key-a',
      api_key_hash: 'key-a',
      account_snapshot: 'alice@example.com',
      auth_label_snapshot: 'Alice',
      auth_provider_snapshot: 'codex',
      auth_indices: ['auth-a'],
      sources: ['alice.json'],
      source_hashes: ['source-a'],
      calls: 2,
      success_calls: 2,
      failure_calls: 0,
      success_rate: 1,
      input_tokens: 20,
      output_tokens: 10,
      cached_tokens: 0,
      cache_read_tokens: 0,
      cache_creation_tokens: 0,
      total_tokens: 30,
      cost: 0,
      average_latency_ms: 100,
      last_seen_ms: 1_800_000_000_000,
    },
  ],
  events,
};

const realtimeResponse: MonitoringAnalyticsResponse = {
  ...mainResponse,
  events,
};

const selectorResponse: MonitoringAnalyticsResponse = {
  generated_at_ms: 1_800_000_000_000,
  granularity: 'hour',
  filter_options: {
    account_count: 2,
    api_key_count: 3,
    accounts: ['alice@example.com'],
    account_stats: [
      {
        id: 'source-only',
        account_snapshot: '',
        auth_label_snapshot: '',
        auth_provider_snapshot: 'openai',
        auth_indices: [],
        sources: ['k:source-only'],
        source_hashes: ['source-only-hash'],
        calls: 0,
        success_calls: 0,
        failure_calls: 0,
        success_rate: 1,
        input_tokens: 0,
        output_tokens: 0,
        cached_tokens: 0,
        cache_read_tokens: 0,
        cache_creation_tokens: 0,
        total_tokens: 0,
        cost: 0,
        average_latency_ms: null,
        last_seen_ms: 0,
      },
    ],
    api_key_hashes: ['key-a'],
    models: ['gpt-active'],
    providers: ['codex'],
  },
};

const legacySelectorResponse: MonitoringAnalyticsResponse = {
  generated_at_ms: 1_800_000_000_000,
  granularity: 'hour',
  filter_options: {
    account_stats: [
      {
        ...accountsResponse.account_stats![0],
        auth_indices: ['auth-a', 'auth-b'],
      },
    ],
    api_key_stats: apiKeysResponse.api_key_stats,
    models: ['gpt-active'],
    providers: ['codex'],
  },
};

let currentSelectorResponse = selectorResponse;
let currentSelectorError = '';

const resultFor = (params: UseMonitoringAnalyticsParams) => ({
  enabled: true,
  loading: false,
  error: params.include?.filter_selectors ? currentSelectorError : '',
  data: params.include?.filter_selectors
    ? currentSelectorResponse
    : params.include?.account_stats
      ? accountsResponse
      : params.include?.api_key_stats
        ? apiKeysResponse
        : realtimeResponse,
  dataStale: false,
  lastRefreshedAt,
  serviceBase: 'http://manager.local',
  unavailableReason: '' as const,
  refresh,
});

describe('useMonitoringData analytics requests', () => {
  let renderer: ReactTestRenderer | null = null;
  let latestResult: UseMonitoringDataReturn | null = null;

  const lastParams = (predicate: (params: UseMonitoringAnalyticsParams) => boolean) => {
    const calls = useMonitoringAnalyticsMock.mock.calls
      .map(([params]) => params as UseMonitoringAnalyticsParams)
      .filter(predicate);
    return calls[calls.length - 1];
  };

  function Harness({ activeDataTab }: { activeDataTab: MonitoringDataTab }) {
    const result = useMonitoringData({
      config: null,
      modelPrices,
      timeRange: 'today',
      searchQuery: '',
      scopeFilters,
      activeDataTab,
    });
    useEffect(() => {
      latestResult = result;
    }, [result]);
    return null;
  }

  beforeEach(() => {
    vi.spyOn(Date, 'now').mockReturnValue(1_800_000_100_000);
    latestResult = null;
    currentSelectorResponse = selectorResponse;
    currentSelectorError = '';
    refresh.mockClear();
    useMonitoringAnalyticsMock.mockReset();
    useMonitoringAnalyticsMock.mockImplementation(resultFor);
  });

  afterEach(() => {
    renderer?.unmount();
    renderer = null;
    vi.restoreAllMocks();
  });

  const renderTab = async (activeDataTab: MonitoringDataTab) => {
    await act(async () => {
      if (renderer) {
        renderer.update(<Harness activeDataTab={activeDataTab} />);
      } else {
        renderer = create(<Harness activeDataTab={activeDataTab} />);
      }
      await Promise.resolve();
      await Promise.resolve();
    });
  };

  it('switches the main include by tab while keeping one tab-independent selector scope', async () => {
    await renderTab('accounts');

    const accounts = lastParams((params) => Boolean(params.include?.summary));
    const selectors = lastParams((params) => Boolean(params.include?.filter_selectors));
    expect(accounts?.include).toEqual({
      summary: true,
      summary_profile: 'compact',
      account_stats: true,
      events_page: { limit: 500, before_ms: null, before_id: null },
      granularity: 'hour',
    });
    expect(JSON.parse(accounts?.dataScopeKey ?? '{}')).toMatchObject({
      activeDataTab: 'accounts',
    });
    expect(accounts?.filters).toEqual({ models: ['gpt-active'] });
    expect(selectors?.include).toEqual({ filter_options: true, filter_selectors: true });
    expect(selectors?.filters).toBeUndefined();
    const selectorScope = selectors?.dataScopeKey;

    await renderTab('apiKeys');
    const apiKeys = lastParams((params) => Boolean(params.include?.summary));
    expect(apiKeys?.include).toEqual({
      summary: true,
      summary_profile: 'compact',
      api_key_stats: true,
      events_page: { limit: 500, before_ms: null, before_id: null },
      granularity: 'hour',
    });
    expect(JSON.parse(apiKeys?.dataScopeKey ?? '{}')).toMatchObject({
      activeDataTab: 'apiKeys',
    });
    expect(lastParams((params) => Boolean(params.include?.filter_selectors))?.dataScopeKey).toBe(
      selectorScope
    );
    expect(latestResult?.filterOptions.accountRows.map((row) => row.account)).toContain(
      'alice@example.com'
    );
    expect(latestResult?.filterOptions.apiKeyRows.map((row) => row.apiKeyHash)).toContain('key-a');
    expect(latestResult?.filterOptions.accountRows.map((row) => row.filterValue)).toContain(
      'source:source-only-hash'
    );
    expect(latestResult?.filterOptions.accountCount).toBe(2);
    expect(latestResult?.filterOptions.apiKeyCount).toBe(3);
    expect(latestResult?.filteredRows).toHaveLength(1);
  });

  it('keeps event cursors on realtime pagination without creating a render loop', async () => {
    await renderTab('realtime');

    expect(lastParams((params) => Boolean(params.include?.summary))?.include?.events_page).toEqual({
      limit: 500,
      before_ms: null,
      before_id: null,
    });
    expect(latestResult?.eventsHasMore).toBe(true);

    await act(async () => {
      latestResult?.loadMoreEvents();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(lastParams((params) => Boolean(params.include?.summary))?.include?.events_page).toEqual({
      limit: 500,
      before_ms: 1_799_999_999_000,
      before_id: 7,
    });

    await renderTab('accounts');
    expect(lastParams((params) => Boolean(params.include?.summary))?.include?.events_page).toEqual({
      limit: 500,
      before_ms: null,
      before_id: null,
    });

    await renderTab('realtime');
    expect(lastParams((params) => Boolean(params.include?.summary))?.include?.events_page).toEqual({
      limit: 500,
      before_ms: null,
      before_id: null,
    });
    expect(useMonitoringAnalyticsMock.mock.calls.length).toBeLessThan(30);
  });

  it('uses legacy selector rows for counts without collapsing distinct filter identities', async () => {
    currentSelectorResponse = legacySelectorResponse;

    await renderTab('apiKeys');

    expect(latestResult?.filterOptions.accountRows.map((row) => row.filterValue)).toEqual([
      'auth:auth-a',
      'auth:auth-a,auth-b',
    ]);
    expect(latestResult?.filterOptions.accountCount).toBe(1);
    expect(latestResult?.filterOptions.apiKeyCount).toBe(1);
  });

  it('surfaces selector failures without blocking the main analytics result', async () => {
    currentSelectorError = 'selector timeout';

    await renderTab('accounts');

    expect(latestResult?.loading).toBe(false);
    expect(latestResult?.summary.totalCalls).toBe(2);
    expect(latestResult?.error).toBe('selector timeout');
  });
});
