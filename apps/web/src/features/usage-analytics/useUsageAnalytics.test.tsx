import { useEffect } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  useMonitoringAnalytics,
  type UseMonitoringAnalyticsParams,
  type UseMonitoringAnalyticsReturn,
} from '@/features/monitoring/hooks/useMonitoringAnalytics';
import type { MonitoringAnalyticsApiKeyStatRow } from '@/services/api/usageService';
import { useUsageAnalytics } from './useUsageAnalytics';

vi.mock('@/features/monitoring/hooks/useMonitoringAnalytics', () => ({
  useMonitoringAnalytics: vi.fn(),
}));

vi.mock('@/features/monitoring/hooks/useUsageData', () => ({
  useUsageData: () => ({ apiKeyAliases: [], loadApiKeyAliases: vi.fn() }),
}));

vi.mock('@/features/monitoring/services/monitoringMetaService', () => ({
  loadMonitoringMetaPayload: () => Promise.resolve({ authFiles: [], channels: [] }),
}));

vi.mock('@/stores', () => ({
  useConfigStore: (selector: (state: { config: null }) => unknown) => selector({ config: null }),
}));

const useMonitoringAnalyticsMock = vi.mocked(useMonitoringAnalytics);

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const emptyAnalyticsResponse = {
  generated_at_ms: 1,
  granularity: 'hour',
};
const HOUR_MS = 60 * 60 * 1000;

const createApiKeyStatRow = (
  overrides: Partial<MonitoringAnalyticsApiKeyStatRow> = {}
): MonitoringAnalyticsApiKeyStatRow => ({
  id: 'key-a',
  api_key_hash: 'key-a',
  calls: 6,
  success_calls: 6,
  failure_calls: 0,
  success_rate: 1,
  input_tokens: 600,
  output_tokens: 0,
  cached_tokens: 0,
  cache_read_tokens: 0,
  cache_creation_tokens: 0,
  total_tokens: 600,
  cost: 0.6,
  average_latency_ms: null,
  last_seen_ms: 1,
  ...overrides,
});

const localHourStartMs = (timestampMs: number) => {
  const date = new Date(timestampMs);
  date.setMinutes(0, 0, 0);
  return date.getTime();
};

describe('useUsageAnalytics request orchestration', () => {
  let renderer: ReactTestRenderer | null = null;
  let latestResult: ReturnType<typeof useUsageAnalytics> | null = null;
  let selectorError = '';
  let credentialTimelineError = '';
  let credentialTimelineLoading = false;
  let apiKeyTimelineAvailable = true;
  let mainTimelineBucketCalls = 5;
  let apiKeyStats: MonitoringAnalyticsApiKeyStatRow[] = [createApiKeyStatRow()];
  const mainRefresh = vi.fn();
  const selectorRefresh = vi.fn();
  const auxiliaryRefresh = vi.fn();

  const resultFor = (params: UseMonitoringAnalyticsParams): UseMonitoringAnalyticsReturn => {
    const selectors = Boolean(params.include?.filter_selectors);
    const main = Boolean(params.include?.summary);
    const credentialTimeline = Boolean(params.include?.credential_timeline);
    const apiKeyTimeline = Boolean(params.include?.api_key_timeline);
    const mainTimelineStartMs =
      typeof params.fromMs === 'number' ? localHourStartMs(params.fromMs) : 0;
    const mainData = params.include?.credential_stats
      ? {
          ...emptyAnalyticsResponse,
          credential_stats: [
            {
              id: 'credential-a.json',
              auth_file_snapshot: 'credential-a.json',
              calls: 10,
              success_calls: 9,
              failure_calls: 1,
              success_rate: 0.9,
              input_tokens: 100,
              output_tokens: 50,
              cached_tokens: 0,
              cache_read_tokens: 0,
              cache_creation_tokens: 0,
              total_tokens: 150,
              cost: 1,
              average_latency_ms: 100,
              last_seen_ms: 1,
            },
          ],
        }
      : params.include?.api_key_stats
        ? {
            ...emptyAnalyticsResponse,
            timeline: [
              {
                bucket_ms: mainTimelineStartMs,
                label: '00:00',
                calls: mainTimelineBucketCalls,
                tokens: mainTimelineBucketCalls * 100,
                success: mainTimelineBucketCalls,
                failure: 0,
              },
              {
                bucket_ms: mainTimelineStartMs + 2 * HOUR_MS,
                label: '01:00',
                calls: mainTimelineBucketCalls,
                tokens: mainTimelineBucketCalls * 100,
                success: mainTimelineBucketCalls,
                failure: 0,
              },
            ],
            api_key_stats: apiKeyStats,
          }
        : emptyAnalyticsResponse;
    return {
      enabled: Boolean(params.fromMs && params.toMs),
      loading: credentialTimeline ? credentialTimelineLoading : false,
      error: selectors ? selectorError : credentialTimeline ? credentialTimelineError : '',
      data: selectors
        ? selectorError
          ? null
          : {
              ...emptyAnalyticsResponse,
              filter_options: {
                models: ['gpt-a'],
                api_key_hashes: ['key-a'],
                providers: ['codex'],
                auth_files: ['account.json'],
              },
            }
        : main
          ? mainData
          : apiKeyTimeline && apiKeyTimelineAvailable
            ? {
                ...emptyAnalyticsResponse,
                api_key_timeline: [
                  {
                    api_key_hash: 'key-a',
                    bucket_ms: mainTimelineStartMs,
                    calls: 2,
                    tokens: 200,
                    success: 2,
                    failure: 0,
                  },
                  {
                    api_key_hash: 'key-a',
                    bucket_ms: mainTimelineStartMs + 2 * HOUR_MS,
                    calls: 4,
                    tokens: 400,
                    success: 4,
                    failure: 0,
                  },
                ],
              }
            : credentialTimeline && !credentialTimelineError && !credentialTimelineLoading
              ? {
                  ...emptyAnalyticsResponse,
                  credential_timeline: [
                    {
                      id: 'credential-a.json',
                      auth_file_snapshot: 'credential-a.json',
                      bucket_ms: 1,
                      calls: 10,
                      tokens: 150,
                      success: 9,
                      failure: 1,
                    },
                  ],
                }
              : null,
      dataStale: false,
      lastRefreshedAt: null,
      serviceBase: 'http://manager.local',
      unavailableReason: '',
      refresh: selectors ? selectorRefresh : main ? mainRefresh : auxiliaryRefresh,
    };
  };

  const lastParams = (predicate: (params: UseMonitoringAnalyticsParams) => boolean) => {
    const calls = useMonitoringAnalyticsMock.mock.calls.map(([params]) => params).filter(predicate);
    return calls[calls.length - 1];
  };

  function Harness() {
    const result = useUsageAnalytics();
    useEffect(() => {
      latestResult = result;
    }, [result]);
    return null;
  }

  beforeEach(() => {
    selectorError = '';
    credentialTimelineError = '';
    credentialTimelineLoading = false;
    apiKeyTimelineAvailable = true;
    mainTimelineBucketCalls = 5;
    apiKeyStats = [createApiKeyStatRow()];
    latestResult = null;
    mainRefresh.mockReset();
    selectorRefresh.mockReset();
    auxiliaryRefresh.mockReset();
    useMonitoringAnalyticsMock.mockReset();
    useMonitoringAnalyticsMock.mockImplementation(resultFor);
  });

  afterEach(() => {
    renderer?.unmount();
    renderer = null;
  });

  const renderHook = async (initialEntry = '/usage-analytics') => {
    await act(async () => {
      renderer = create(
        <MemoryRouter initialEntries={[initialEntry]}>
          <Harness />
        </MemoryRouter>
      );
      await Promise.resolve();
    });
  };

  it('uses a tab-scoped main request and a tab-independent selector request', async () => {
    await renderHook();

    const overview = lastParams((params) => Boolean(params.include?.summary));
    const selectors = lastParams((params) => Boolean(params.include?.filter_selectors));
    expect(overview?.include).toEqual({
      summary: true,
      summary_profile: 'compact',
      summary_percentiles: true,
      summary_comparison: true,
      timeline: true,
      model_stats: true,
      channel_share: true,
      api_key_stats: true,
      anomaly_points: true,
      granularity: 'hour',
    });
    expect(JSON.parse(overview?.dataScopeKey ?? '{}')).toMatchObject({ activeTab: 'overview' });
    expect(selectors?.include).toEqual({ filter_options: true, filter_selectors: true });
    expect(JSON.parse(selectors?.dataScopeKey ?? '{}')).not.toHaveProperty('activeTab');
    expect(latestResult?.filterOptions).toMatchObject({
      models: ['gpt-a'],
      api_key_hashes: ['key-a'],
    });

    const selectorScope = selectors?.dataScopeKey;
    await act(async () => {
      latestResult?.setActiveTab('heatmap');
    });

    const heatmap = lastParams((params) => Boolean(params.include?.summary));
    const selectorsAfterTab = lastParams((params) => Boolean(params.include?.filter_selectors));
    expect(heatmap?.include).toEqual({
      summary: true,
      summary_profile: 'compact',
      heatmap: true,
      granularity: 'hour',
    });
    expect(JSON.parse(heatmap?.dataScopeKey ?? '{}')).toMatchObject({ activeTab: 'heatmap' });
    expect(selectorsAfterTab?.dataScopeKey).toBe(selectorScope);
  });

  it('fills missing buckets in the main usage timeline from the active range', async () => {
    await renderHook();

    const overview = lastParams((params) => Boolean(params.include?.summary));
    expect(typeof overview?.fromMs).toBe('number');
    const firstBucketMs = localHourStartMs(overview?.fromMs as number);

    expect(latestResult?.timeline.slice(0, 3).map((point) => point.bucketMs)).toEqual([
      firstBucketMs,
      firstBucketMs + HOUR_MS,
      firstBucketMs + 2 * HOUR_MS,
    ]);
    expect(latestResult?.timeline[1]).toMatchObject({
      requestCount: 0,
      totalTokens: 0,
      successCount: 0,
      failureCount: 0,
      averageLatencyMs: null,
      p95LatencyMs: null,
      p95TtftMs: null,
    });
  });

  it('loads exact timeline buckets for the visible client keys on overview and trends', async () => {
    await renderHook();

    const apiKeyTimeline = lastParams((params) => Boolean(params.include?.api_key_timeline));
    expect(apiKeyTimeline?.include).toEqual({ granularity: 'hour', api_key_timeline: true });
    expect(apiKeyTimeline?.filters).toMatchObject({ api_key_hashes: ['key-a'] });
    expect(JSON.parse(apiKeyTimeline?.dataScopeKey ?? '{}')).toMatchObject({
      activeTab: 'overview',
      apiKeyHashes: ['key-a'],
    });
    expect(
      latestResult?.apiKeyTrendSeries[0].points.slice(0, 3).map((point) => point.value)
    ).toEqual([2, 0, 4]);

    await act(async () => {
      latestResult?.setActiveTab('trends');
      await Promise.resolve();
    });

    const trendsTimeline = lastParams((params) => Boolean(params.include?.api_key_timeline));
    expect(JSON.parse(trendsTimeline?.dataScopeKey ?? '{}')).toMatchObject({
      activeTab: 'trends',
      apiKeyHashes: ['key-a'],
    });
  });

  it('never uses fallback API key IDs for default selections or timeline requests', async () => {
    apiKeyStats = [
      createApiKeyStatRow({
        id: 'unknown-client-api-key:missing-client-key',
        api_key_hash: '',
      }),
      createApiKeyStatRow({ id: 'real-key-row', api_key_hash: ' real-key-hash ' }),
    ];
    await renderHook();

    const overviewTimeline = lastParams((params) => Boolean(params.include?.api_key_timeline));
    expect(overviewTimeline?.filters).toMatchObject({ api_key_hashes: ['real-key-hash'] });
    expect(latestResult?.selectedApiKey?.apiKeyHash).toBe('real-key-hash');

    await act(async () => {
      latestResult?.setActiveTab('apiKeys');
      await Promise.resolve();
    });

    const selectedTimeline = lastParams(
      (params) => Boolean(params.include?.timeline) && !params.include?.summary
    );
    expect(selectedTimeline?.filters).toMatchObject({ api_key_hashes: ['real-key-hash'] });

    await act(async () => {
      latestResult?.setSelectedApiKeyHash('unknown-client-api-key:missing-client-key');
      await Promise.resolve();
    });

    const fallbackSelectionTimeline = lastParams(
      (params) => Boolean(params.include?.timeline) && !params.include?.summary
    );
    expect(fallbackSelectionTimeline?.filters).toMatchObject({ api_key_hashes: ['real-key-hash'] });
    expect(latestResult?.selectedApiKey?.apiKeyHash).toBe('real-key-hash');
  });

  it('uses only real API keys for exact trend series while keeping a leading fallback rank row', async () => {
    apiKeyStats = [
      createApiKeyStatRow({
        id: 'unknown-client-api-key:missing-client-key',
        api_key_hash: '',
        calls: 100,
        cost: 100,
      }),
      ...['key-a', 'key-b', 'key-c', 'key-d'].map((apiKeyHash, index) =>
        createApiKeyStatRow({
          id: apiKeyHash,
          api_key_hash: apiKeyHash,
          calls: 10 - index,
          cost: 10 - index,
        })
      ),
    ];

    await renderHook();

    const apiKeyTimeline = lastParams((params) => Boolean(params.include?.api_key_timeline));
    expect(latestResult?.apiKeyRows[0].id).toBe('unknown-client-api-key:missing-client-key');
    expect(apiKeyTimeline?.filters).toMatchObject({
      api_key_hashes: ['key-a', 'key-b', 'key-c', 'key-d'],
    });
    expect(latestResult?.apiKeyTrendSeries.map((series) => series.id)).toEqual([
      'key-a',
      'key-b',
      'key-c',
      'key-d',
    ]);
    expect(
      latestResult?.apiKeyTrendSeries[0].points.slice(0, 3).map((point) => point.value)
    ).toEqual([2, 0, 4]);
  });

  it('does not approximate fallback-only API key rows as trend series', async () => {
    apiKeyStats = [
      createApiKeyStatRow({
        id: 'unknown-client-api-key:missing-client-key',
        api_key_hash: '',
        calls: 100,
        cost: 100,
      }),
    ];
    apiKeyTimelineAvailable = false;

    await renderHook();

    expect(latestResult?.apiKeyRows).toHaveLength(1);
    expect(latestResult?.apiKeyRows[0].id).toBe('unknown-client-api-key:missing-client-key');
    expect(latestResult?.apiKeyTrendSeries).toEqual([]);
  });

  it('uses complete API key totals for approximate trend shares while hiding fallback rows', async () => {
    apiKeyStats = [
      createApiKeyStatRow({
        id: 'unknown-client-api-key:missing-client-key',
        api_key_hash: '',
        calls: 90,
      }),
      createApiKeyStatRow({
        id: 'real-key-row',
        api_key_hash: 'real-key-hash',
        calls: 10,
      }),
    ];
    mainTimelineBucketCalls = 50;
    apiKeyTimelineAvailable = false;

    await renderHook();

    expect(latestResult?.apiKeyRows.map((row) => row.id)).toEqual([
      'unknown-client-api-key:missing-client-key',
      'real-key-hash',
    ]);
    expect(latestResult?.apiKeyTrendSeries.map((series) => series.id)).toEqual(['real-key-hash']);
    expect(
      latestResult?.apiKeyTrendSeries[0].points.slice(0, 3).map((point) => point.value)
    ).toEqual([5, 0, 5]);
  });

  it('omits an initial fallback API key filter from analytics requests', async () => {
    await renderHook('/usage-analytics?api_key_hash=unknown-client-api-key%3Alegacy-filter');

    const main = lastParams((params) => Boolean(params.include?.summary));
    expect(main?.filters).not.toHaveProperty('api_key_hashes');
  });

  it('keeps a fallback-only aggregate visible without enabling a key timeline request', async () => {
    apiKeyStats = [
      createApiKeyStatRow({
        id: 'unknown-client-api-key:missing-client-key',
        api_key_hash: '',
      }),
    ];
    await renderHook();

    const selectedTimeline = lastParams(
      (params) => Boolean(params.include?.timeline) && !params.include?.summary
    );
    expect(latestResult?.selectedApiKey?.id).toBe('unknown-client-api-key:missing-client-key');
    expect(selectedTimeline?.fromMs).toBeUndefined();
    expect(selectedTimeline?.toMs).toBeUndefined();
    expect(selectedTimeline?.filters).not.toHaveProperty('api_key_hashes');
  });

  it('does not couple selector failures to the main page error and refreshes both requests', async () => {
    selectorError = 'selector failed';
    await renderHook();

    expect(latestResult?.error).toBe('');
    expect(latestResult?.filterOptions).toBeUndefined();

    act(() => {
      latestResult?.refresh();
    });
    expect(mainRefresh).toHaveBeenCalledTimes(1);
    expect(selectorRefresh).toHaveBeenCalledTimes(1);
  });

  it('loads only the selected credential timeline after the credential ranking', async () => {
    await renderHook();

    await act(async () => {
      latestResult?.setActiveTab('credentials');
      await Promise.resolve();
    });

    const credentials = lastParams((params) => Boolean(params.include?.credential_stats));
    expect(credentials?.include).toEqual({
      summary: true,
      summary_profile: 'compact',
      credential_stats: true,
      granularity: 'hour',
    });

    const timeline = lastParams((params) => Boolean(params.include?.credential_timeline));
    expect(timeline?.include).toEqual({ granularity: 'hour', credential_timeline: true });
    expect(timeline?.filters).toMatchObject({ credential_ids: ['credential-a.json'] });
    expect(JSON.parse(timeline?.dataScopeKey ?? '{}')).toMatchObject({
      activeTab: 'credentials',
      selectedCredentialID: 'credential-a.json',
    });
    expect(latestResult?.credentialTrendSeries).toHaveLength(1);
    expect(latestResult?.timeline).toEqual([]);
  });

  it('exposes selected credential timeline loading and error states', async () => {
    credentialTimelineLoading = true;
    await renderHook();

    await act(async () => {
      latestResult?.setActiveTab('credentials');
      await Promise.resolve();
    });
    expect(latestResult?.credentialTrendLoading).toBe(true);
    expect(latestResult?.credentialTrendError).toBe('');

    credentialTimelineLoading = false;
    credentialTimelineError = 'timeline failed';
    await act(async () => {
      renderer?.update(
        <MemoryRouter initialEntries={['/usage-analytics']}>
          <Harness />
        </MemoryRouter>
      );
      await Promise.resolve();
    });
    expect(latestResult?.credentialTrendLoading).toBe(false);
    expect(latestResult?.credentialTrendError).toBe('timeline failed');
    expect(latestResult?.credentialRows).toHaveLength(1);
  });
});
