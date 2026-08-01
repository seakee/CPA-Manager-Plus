import { describe, expect, it } from 'vitest';
import type { MonitoringAnalyticsResponse } from '@/services/api/usageService';
import { buildSourceInfoMap } from '@/utils/sourceResolver';
import type { UsageRankRow } from './usageAnalyticsModel';
import {
  analyzeUsageBucket,
  buildApiKeyTrendSeries,
  buildApiKeyRows,
  buildCredentialRows,
  buildSelectedApiKeyTrendSeries,
  buildSelectedCredentialTrendSeries,
  buildDrilldownPreview,
  buildKeyAnomalies,
  buildModelKeyDistribution,
  buildMonitoringDetailUrl,
  buildProviderRows,
  buildUsageMatrix,
  buildUsageAnalyticsFilters,
  buildUsageAnalyticsFilterSelectorsInclude,
  buildUsageAnalyticsInclude,
  buildUsageAnomalyCauseKeys,
  buildUsageHeatmapCellDetail,
  buildUsageHeatmapCellDateOptions,
  buildUsageHeatmapChartData,
  buildUsageHeatmapHighlights,
  buildUsageHeatmapRangeContext,
  buildUsageHeatmap,
  buildUsageCredentialTimeline,
  buildUsageApiKeyTimeline,
  buildUsageTimeline,
  computeCacheHitRate,
  computeRowAverageCostPerCall,
  computeRowCacheHitRate,
  getUsageCacheTokens,
  getUsageRangeBounds,
  maskApiKeyHash,
  resolveUsageGranularity,
  USAGE_ANALYTICS_DEFAULT_FILTERS,
} from './usageAnalyticsModel';

const NOW_MS = Date.UTC(2026, 5, 4, 12, 0, 0);
const HOUR_MS = 60 * 60 * 1000;
const DAY_MS = 24 * HOUR_MS;

describe('usage analytics request model', () => {
  it('resolves time ranges and default granularity rules', () => {
    expect(USAGE_ANALYTICS_DEFAULT_FILTERS.timeRange).toBe('24h');
    expect(getUsageRangeBounds({ timeRange: '24h', customRange: null }, NOW_MS)).toEqual({
      fromMs: NOW_MS - DAY_MS,
      toMs: NOW_MS,
    });
    expect(
      resolveUsageGranularity({ ...USAGE_ANALYTICS_DEFAULT_FILTERS, timeRange: '24h' }, NOW_MS)
    ).toBe('hour');
    expect(
      resolveUsageGranularity({ ...USAGE_ANALYTICS_DEFAULT_FILTERS, timeRange: '7d' }, NOW_MS)
    ).toBe('hour');
    expect(
      resolveUsageGranularity({ ...USAGE_ANALYTICS_DEFAULT_FILTERS, timeRange: '30d' }, NOW_MS)
    ).toBe('day');
    expect(
      resolveUsageGranularity(
        {
          ...USAGE_ANALYTICS_DEFAULT_FILTERS,
          timeRange: 'custom',
          customRange: { startMs: NOW_MS - 8 * DAY_MS, endMs: NOW_MS },
        },
        NOW_MS
      )
    ).toBe('day');
  });

  it('maps model, API key, and status filters to analytics request fields', () => {
    expect(
      buildUsageAnalyticsFilters({
        model: 'gpt-4o',
        apiKeyHash: ' ABCDEF1234 ',
        status: 'success',
        minLatencyMs: '10000',
        cacheStatus: 'hit',
      })
    ).toEqual({
      models: ['gpt-4o'],
      api_key_hashes: ['abcdef1234'],
      include_failed: false,
      min_latency_ms: 10000,
      cache_status: 'hit',
    });
    expect(
      buildUsageAnalyticsFilters({
        model: 'all',
        apiKeyHash: 'all',
        status: 'failed',
      })
    ).toEqual({
      failed_only: true,
    });
  });

  it('builds the minimum analytics include for each active tab', () => {
    expect(buildUsageAnalyticsInclude('overview', 'day')).toEqual({
      summary: true,
      summary_profile: 'compact',
      summary_percentiles: true,
      summary_comparison: true,
      timeline: true,
      model_stats: true,
      channel_share: true,
      api_key_stats: true,
      anomaly_points: true,
      granularity: 'day',
    });
    expect(
      buildUsageAnalyticsInclude('trends', 'hour', {
        fromMs: 1_000,
        toMs: 2_000,
        limit: 8,
      })
    ).toEqual({
      summary: true,
      summary_profile: 'compact',
      summary_comparison: true,
      timeline: true,
      model_stats: true,
      api_key_stats: true,
      anomaly_points: true,
      granularity: 'hour',
      drilldown_preview: { from_ms: 1_000, to_ms: 2_000, limit: 8 },
    });
    expect(buildUsageAnalyticsInclude('models', 'day')).toEqual({
      summary: true,
      summary_profile: 'compact',
      timeline: true,
      model_stats: true,
      api_key_stats: true,
      granularity: 'day',
    });
    expect(buildUsageAnalyticsInclude('apiKeys', 'day')).toEqual({
      summary: true,
      summary_profile: 'compact',
      api_key_stats: true,
      granularity: 'day',
    });
    expect(buildUsageAnalyticsInclude('credentials', 'day')).toEqual({
      summary: true,
      summary_profile: 'compact',
      credential_stats: true,
      granularity: 'day',
    });
    expect(
      buildUsageAnalyticsInclude('heatmap', 'day', {
        fromMs: 1_000,
        toMs: 2_000,
      })
    ).toEqual({
      summary: true,
      summary_profile: 'compact',
      heatmap: true,
      granularity: 'day',
    });
    expect(buildUsageAnalyticsFilterSelectorsInclude()).toEqual({
      filter_options: true,
      filter_selectors: true,
    });
  });
});

describe('usage analytics adapters', () => {
  it('builds heatmap chart data from non-empty valid request buckets only', () => {
    expect(
      buildUsageHeatmapChartData([
        {
          weekday: 2,
          hour: 1,
          requestCount: 128,
          successCount: 126,
          failureCount: 2,
          totalTokens: 1000,
          estimatedCost: 3.25,
          failureRate: 2 / 128,
        },
        {
          weekday: 2,
          hour: 2,
          requestCount: 0,
          successCount: 0,
          failureCount: 0,
          totalTokens: 0,
          estimatedCost: 0,
          failureRate: 0,
        },
        {
          weekday: 9,
          hour: 1,
          requestCount: 12,
          successCount: 12,
          failureCount: 0,
          totalTokens: 100,
          estimatedCost: 0.5,
          failureRate: 0,
        },
      ])
    ).toEqual([[1, 2, 128, 128, 126, 2, 1000, 3.25, 2 / 128]]);
  });

  it('normalizes heatmap color values and builds cell detail without changing raw metrics', () => {
    const points = [
      {
        weekday: 1,
        hour: 9,
        requestCount: 10,
        successCount: 9,
        failureCount: 1,
        totalTokens: 100,
        estimatedCost: 1,
        failureRate: 0.1,
      },
      {
        weekday: 1,
        hour: 10,
        requestCount: 20,
        successCount: 20,
        failureCount: 0,
        totalTokens: 400,
        estimatedCost: 2,
        failureRate: 0,
      },
      {
        weekday: 2,
        hour: 9,
        requestCount: 5,
        successCount: 3,
        failureCount: 2,
        totalTokens: 50,
        estimatedCost: 0.5,
        failureRate: 0.4,
      },
    ];

    expect(buildUsageHeatmapChartData(points, 'totalTokens', 'byWeekday')).toEqual([
      [9, 1, 0.25, 10, 9, 1, 100, 1, 0.1],
      [10, 1, 1, 20, 20, 0, 400, 2, 0],
      [9, 2, 1, 5, 3, 2, 50, 0.5, 0.4],
    ]);

    const detail = buildUsageHeatmapCellDetail(points, { weekday: 1, hour: 9 }, 'requestCount');
    expect(detail).toMatchObject({
      metricValue: 10,
      overallBaseline: 35 / 3,
      weekdayBaseline: 15,
      hourBaseline: 7.5,
      rank: 2,
      totalCells: 3,
    });

    const highlights = buildUsageHeatmapHighlights(points);
    expect(highlights.requestPeaks[0].value).toBe(20);
    expect(highlights.costPeaks[0].value).toBe(2);
    expect(highlights.failureRisks[0]).toMatchObject({
      metric: 'failureRate',
      value: 0.4,
    });
  });

  it('builds heatmap range context from the active time window', () => {
    const context = buildUsageHeatmapRangeContext(
      {
        fromMs: Date.UTC(2026, 5, 8, 0, 0, 0),
        toMs: Date.UTC(2026, 5, 16, 0, 0, 0),
      },
      'en-US',
      'UTC'
    );

    expect(context.dayCount).toBe(8);
    expect(context.sampleWindowCount).toBe(8 * 24);
    expect(context.rangeLabel).toContain('06/08');
    expect(context.dateOptions[0]).toMatchObject({
      key: '2026-06-08',
      label: '06/08',
      sampleWindowCount: 24,
    });
    expect(context.cellSamples['1-9']).toMatchObject({
      dateKeys: ['2026-06-08', '2026-06-15'],
      dateLabels: ['06/08', '06/15'],
      overflowCount: 0,
      sampleCount: 2,
    });
    expect(
      buildUsageHeatmapCellDateOptions(context, { weekday: 1, hour: 9 }).map((option) => option.key)
    ).toEqual(['2026-06-08', '2026-06-15']);
    expect(
      buildUsageHeatmapCellDateOptions(context, { weekday: 2, hour: 9 }).map((option) => option.key)
    ).toEqual(['2026-06-09']);
    expect(buildUsageHeatmapCellDateOptions(context, null)).toHaveLength(8);
  });

  it('maps heatmap contributor fields from backend analytics', () => {
    const rows = buildUsageHeatmap([
      {
        weekday: 1,
        hour: 9,
        calls: 3,
        success: 2,
        failure: 1,
        tokens: 300,
        cost: 4,
        failure_rate: 1 / 3,
        model_contributors: [
          {
            key: 'gpt-a',
            label: 'gpt-a',
            calls: 2,
            success: 1,
            failure: 1,
            tokens: 200,
            cost: 2,
            failure_rate: 0.5,
            share: 2 / 3,
          },
        ],
        api_key_contributors: [
          {
            key: 'abcdef1234567890',
            calls: 2,
            success: 1,
            failure: 1,
            tokens: 200,
            cost: 2,
            failure_rate: 0.5,
            share: 2 / 3,
          },
        ],
        provider_contributors: [
          {
            key: 'openai',
            label: 'openai',
            calls: 2,
            success: 1,
            failure: 1,
            tokens: 200,
            cost: 2,
            failure_rate: 0.5,
            share: 2 / 3,
          },
        ],
      },
    ]);

    expect(rows[0].modelContributors?.[0]).toMatchObject({
      key: 'gpt-a',
      requestCount: 2,
      failureRate: 0.5,
      share: 2 / 3,
    });
    expect(rows[0].apiKeyContributors?.[0]).toMatchObject({
      key: 'abcdef1234567890',
      label: 'abcdef1234567890',
    });
    expect(rows[0].providerContributors?.[0]).toMatchObject({
      key: 'openai',
      estimatedCost: 2,
    });
  });

  it('keeps detailed token and cost fields from backend timeline buckets', () => {
    const points = buildUsageTimeline(
      [
        {
          bucket_ms: NOW_MS,
          label: '',
          calls: 3,
          tokens: 111,
          total_tokens: 120,
          success: 2,
          failure: 1,
          input_tokens: 80,
          output_tokens: 30,
          cached_tokens: 10,
          cache_read_tokens: 8,
          cache_creation_tokens: 4,
          cost: 0.42,
          average_latency_ms: 250,
        },
      ],
      'hour'
    );

    expect(points[0]).toMatchObject({
      requestCount: 3,
      totalTokens: 120,
      inputTokens: 80,
      outputTokens: 30,
      cachedTokens: 10,
      cacheReadTokens: 8,
      cacheCreationTokens: 4,
      estimatedCost: 0.42,
      failureCount: 1,
      successRate: 2 / 3,
      averageLatencyMs: 250,
    });
  });

  it('combines compatible, cache-read, and cache-creation buckets for display', () => {
    expect(
      getUsageCacheTokens({
        cachedTokens: 0,
        cacheReadTokens: 80,
        cacheCreationTokens: 20,
      })
    ).toBe(100);
    expect(
      getUsageCacheTokens({
        cachedTokens: 5,
        cacheReadTokens: 4,
        cacheCreationTokens: 1,
      })
    ).toBe(10);
  });

  it('builds selected credential trend series from backend credential timeline buckets', () => {
    const credentialTimeline = buildUsageCredentialTimeline(
      [
        {
          id: 'credential-a',
          label: 'prod-auth',
          bucket_ms: NOW_MS,
          bucket_label: '06/04',
          calls: 3,
          tokens: 300,
          success: 3,
          failure: 0,
          total_tokens: 300,
          cost: 0.3,
        },
        {
          id: 'credential-a',
          label: 'prod-auth',
          bucket_ms: NOW_MS + HOUR_MS,
          bucket_label: '06/04 13:00',
          calls: 5,
          tokens: 500,
          success: 4,
          failure: 1,
          total_tokens: 500,
          cost: 0.5,
        },
      ],
      'hour'
    );
    const rows: UsageRankRow[] = [
      {
        id: 'credential-a',
        label: 'prod-auth',
        requestCount: 8,
        successCount: 7,
        failureCount: 1,
        successRate: 7 / 8,
        totalTokens: 800,
        inputTokens: 800,
        outputTokens: 0,
        cachedTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        estimatedCost: 0.8,
        averageLatencyMs: null,
        share: 1,
      },
    ];

    const result = buildSelectedCredentialTrendSeries(rows[0], credentialTimeline, 'requestCount');

    expect(result).toHaveLength(1);
    expect(result[0].id).toBe('credential-a');
    expect(result[0].points.map((point) => point.value)).toEqual([3, 5]);
  });

  it('resolves credential labels from the same source metadata as request monitoring', () => {
    const credentialDisplayContext = {
      authMetaMap: new Map(),
      authFileMap: new Map(),
      sourceInfoMap: buildSourceInfoMap({
        codexApiKeys: [{ apiKey: 'sk-Key-secret-e9GW', prefix: 'Codex Team Key' }],
      }),
      channelByAuthIndex: new Map(),
    };

    const credentialRows = buildCredentialRows(
      [
        {
          id: 'source-hash-a',
          source: 'm:sk-K...e9GW',
          source_hash: 'source-hash-a',
          auth_index: '',
          auth_file_snapshot: '',
          account_snapshot: '',
          auth_label_snapshot: '',
          auth_provider_snapshot: 'codex',
          calls: 3,
          success_calls: 3,
          failure_calls: 0,
          success_rate: 1,
          input_tokens: 100,
          output_tokens: 20,
          cached_tokens: 0,
          cache_read_tokens: 0,
          cache_creation_tokens: 0,
          total_tokens: 120,
          cost: 0.12,
          average_latency_ms: null,
          last_seen_ms: NOW_MS,
        },
      ],
      undefined,
      credentialDisplayContext
    );
    const credentialTimeline = buildUsageCredentialTimeline(
      [
        {
          id: 'source-hash-a',
          source: 'm:sk-K...e9GW',
          source_hash: 'source-hash-a',
          auth_index: '',
          auth_file_snapshot: '',
          account_snapshot: '',
          auth_label_snapshot: '',
          auth_provider_snapshot: 'codex',
          bucket_ms: NOW_MS,
          bucket_label: '06/04',
          calls: 3,
          tokens: 120,
          success: 3,
          failure: 0,
          total_tokens: 120,
          cost: 0.12,
        },
      ],
      'hour',
      credentialDisplayContext
    );

    expect(credentialRows[0]).toMatchObject({
      id: 'source-hash-a',
      label: 'Codex Team Key',
      provider: 'codex',
    });
    expect(credentialRows[0].label).not.toContain('m:sk-K');
    expect(credentialTimeline[0]).toMatchObject({
      id: 'source-hash-a',
      label: 'Codex Team Key',
    });
  });

  it('does not estimate selected credential trend when backend timeline is missing', () => {
    const row: UsageRankRow = {
      id: 'credential-a',
      label: 'prod-auth',
      requestCount: 8,
      successCount: 7,
      failureCount: 1,
      successRate: 7 / 8,
      totalTokens: 800,
      inputTokens: 800,
      outputTokens: 0,
      cachedTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      estimatedCost: 0.8,
      averageLatencyMs: null,
      share: 1,
    };

    expect(buildSelectedCredentialTrendSeries(row, [], 'requestCount')).toEqual([]);
  });

  it('builds selected API key trend from the filtered timeline without estimating share', () => {
    const row: UsageRankRow = {
      id: 'abcdef1234567890',
      label: 'sk-****7890',
      apiKeyHash: 'abcdef1234567890',
      requestCount: 10,
      successCount: 9,
      failureCount: 1,
      successRate: 0.9,
      totalTokens: 700,
      inputTokens: 700,
      outputTokens: 0,
      cachedTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      estimatedCost: 0.7,
      averageLatencyMs: null,
      share: 0.25,
    };
    const timeline = buildUsageTimeline(
      [
        {
          bucket_ms: NOW_MS + HOUR_MS,
          label: '13:00',
          calls: 7,
          tokens: 500,
          total_tokens: 500,
          success: 6,
          failure: 1,
          cost: 0.5,
        },
        {
          bucket_ms: NOW_MS,
          label: '12:00',
          calls: 3,
          tokens: 200,
          total_tokens: 200,
          success: 3,
          failure: 0,
          cost: 0.2,
        },
      ],
      'hour'
    );

    const result = buildSelectedApiKeyTrendSeries(row, timeline, 'requestCount');

    expect(result).toHaveLength(1);
    expect(result[0].id).toBe('abcdef1234567890');
    expect(result[0].points.map((point) => point.value)).toEqual([3, 7]);
  });

  it('builds overview API key trends from exact per-key buckets', () => {
    const rows: UsageRankRow[] = [
      {
        id: 'key-a',
        label: 'Key A',
        apiKeyHash: 'key-a',
        requestCount: 8,
        successCount: 8,
        failureCount: 0,
        successRate: 1,
        totalTokens: 800,
        inputTokens: 800,
        outputTokens: 0,
        cachedTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        estimatedCost: 0.8,
        averageLatencyMs: null,
        share: 0.8,
      },
      {
        id: 'key-b',
        label: 'Key B',
        apiKeyHash: 'key-b',
        requestCount: 2,
        successCount: 2,
        failureCount: 0,
        successRate: 1,
        totalTokens: 200,
        inputTokens: 200,
        outputTokens: 0,
        cachedTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        estimatedCost: 0.2,
        averageLatencyMs: null,
        share: 0.2,
      },
    ];
    const timeline = buildUsageTimeline(
      [
        { bucket_ms: NOW_MS, label: '12:00', calls: 5, tokens: 500, success: 5, failure: 0 },
        {
          bucket_ms: NOW_MS + HOUR_MS,
          label: '13:00',
          calls: 5,
          tokens: 500,
          success: 5,
          failure: 0,
        },
      ],
      'hour'
    );
    const apiKeyTimeline = buildUsageApiKeyTimeline(
      [
        { api_key_hash: 'key-a', bucket_ms: NOW_MS, calls: 2, tokens: 200, success: 2, failure: 0 },
        {
          api_key_hash: 'key-a',
          bucket_ms: NOW_MS + HOUR_MS,
          calls: 6,
          tokens: 600,
          success: 6,
          failure: 0,
        },
        { api_key_hash: 'key-b', bucket_ms: NOW_MS, calls: 3, tokens: 300, success: 3, failure: 0 },
      ],
      'hour'
    );

    const result = buildApiKeyTrendSeries(rows, timeline, apiKeyTimeline, 'requestCount');

    expect(result).toHaveLength(2);
    expect(result[0].points.map((point) => point.value)).toEqual([2, 6]);
    expect(result[1].points.map((point) => point.value)).toEqual([3, 0]);
  });

  it('does not render selected API key trend when the filtered timeline is missing', () => {
    const row = {
      id: 'abcdef1234567890',
      label: 'sk-****7890',
      requestCount: 0,
      successCount: 0,
      failureCount: 0,
      successRate: 0,
      totalTokens: 0,
      inputTokens: 0,
      outputTokens: 0,
      cachedTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      estimatedCost: 0,
      averageLatencyMs: null,
      share: 0,
    };

    expect(buildSelectedApiKeyTrendSeries(row, [], 'requestCount')).toEqual([]);
  });

  it('filters API key rows by keyword and never exposes a raw key value', () => {
    const data: Pick<MonitoringAnalyticsResponse, 'api_key_stats'> = {
      api_key_stats: [
        {
          id: 'hash-a',
          api_key_hash: 'abcdef1234567890',
          account_snapshot: 'team-alpha',
          auth_label_snapshot: 'prod',
          calls: 10,
          success_calls: 9,
          failure_calls: 1,
          success_rate: 0.9,
          input_tokens: 100,
          output_tokens: 20,
          cached_tokens: 5,
          cache_read_tokens: 3,
          cache_creation_tokens: 2,
          total_tokens: 120,
          cost: 1.25,
          average_latency_ms: null,
          last_seen_ms: NOW_MS,
          contexts: [
            {
              id: 'ctx-a',
              account_snapshot: 'team-alpha',
              auth_provider_snapshot: 'codex',
              auth_index: 'auth-1',
              source: 'source-a',
              source_hash: 'source-hash-a',
              calls: 10,
              success_calls: 9,
              failure_calls: 1,
              success_rate: 0.9,
              failure_rate: 0.1,
              total_tokens: 120,
              cost: 1.25,
              average_latency_ms: 250,
              last_seen_ms: NOW_MS,
            },
          ],
        },
      ],
    };

    const rows = buildApiKeyRows(data.api_key_stats, undefined, 'team-alpha');

    expect(rows).toHaveLength(1);
    expect(rows[0].label).toBe('sk-****7890');
    expect(rows[0].label).not.toContain('abcdef1234567890');
    expect(rows[0].contexts?.[0]).toMatchObject({
      account: 'team-alpha',
      authIndex: 'auth-1',
      estimatedCost: 1.25,
      failureRate: 0.1,
      provider: 'codex',
      requestCount: 10,
      source: 'source-a',
      sourceHash: 'source-hash-a',
    });
    expect(maskApiKeyHash('sk-live-raw-secret-value')).toBe('sk-****alue');
  });

  it('resolves API key aliases by hash across analytics views', () => {
    const displayMap = new Map([
      [
        'abcdef1234567890',
        {
          label: 'Team Alpha Key',
          masked: 'sk-****7890',
          copyValue: 'sk-team-alpha-original',
        },
      ],
    ]);
    const apiKeyRows = buildApiKeyRows(
      [
        {
          id: 'hash-a',
          api_key_hash: 'ABCDEF1234567890',
          account_snapshot: 'team-alpha',
          calls: 10,
          success_calls: 9,
          failure_calls: 1,
          success_rate: 0.9,
          input_tokens: 100,
          output_tokens: 20,
          cached_tokens: 5,
          cache_read_tokens: 3,
          cache_creation_tokens: 2,
          total_tokens: 120,
          cost: 1.25,
          average_latency_ms: null,
          last_seen_ms: NOW_MS,
          models: [
            {
              model: 'gpt-4o',
              calls: 10,
              success_calls: 9,
              failure_calls: 1,
              success_rate: 0.9,
              input_tokens: 100,
              output_tokens: 20,
              cached_tokens: 5,
              cache_read_tokens: 3,
              cache_creation_tokens: 2,
              total_tokens: 120,
              cost: 1.25,
              last_seen_ms: NOW_MS,
            },
          ],
        },
      ],
      undefined,
      'team alpha key',
      displayMap
    );

    expect(apiKeyRows[0]).toMatchObject({
      apiKeyHash: 'abcdef1234567890',
      label: 'Team Alpha Key',
      apiKeyCopyValue: 'sk-team-alpha-original',
    });
    expect(
      buildUsageMatrix({
        apiKeyRows,
        credentialRows: [],
        dimension: 'apiKeyModel',
        metric: 'requestCount',
      }).rowLabels
    ).toEqual(['Team Alpha Key']);

    const heatmapRows = buildUsageHeatmap(
      [
        {
          weekday: 1,
          hour: 9,
          calls: 10,
          success: 9,
          failure: 1,
          tokens: 120,
          cost: 1.25,
          failure_rate: 0.1,
          api_key_contributors: [
            {
              key: 'ABCDEF1234567890',
              label: 'ABCDEF1234567890',
              calls: 10,
              success: 9,
              failure: 1,
              tokens: 120,
              cost: 1.25,
              failure_rate: 0.1,
              share: 1,
            },
          ],
        },
      ],
      displayMap
    );
    expect(heatmapRows[0].apiKeyContributors?.[0].label).toBe('Team Alpha Key');

    const drilldownRows = buildDrilldownPreview(
      [
        {
          event_hash: 'event-a',
          timestamp_ms: NOW_MS,
          model: 'gpt-4o',
          endpoint: '/v1/chat/completions',
          method: 'POST',
          path: '/v1/chat/completions',
          auth_index: '0',
          source: 'codex',
          source_hash: 'source-a',
          api_key_hash: 'ABCDEF1234567890',
          account_snapshot: 'team-alpha',
          auth_label_snapshot: 'prod',
          auth_provider_snapshot: 'openai',
          input_tokens: 60,
          output_tokens: 40,
          cached_tokens: 0,
          cache_read_tokens: 0,
          cache_creation_tokens: 0,
          reasoning_tokens: 0,
          total_tokens: 100,
          latency_ms: 250,
          failed: false,
          header_error_kind: 'rate_limit',
          header_error_code: 'retry_after',
          header_trace_id: 'req-header',
          header_quota_plan_type: 'plus',
          header_quota_used_percent: 87,
          header_quota_recover_at_ms: 1780000060000,
        },
      ],
      [],
      displayMap
    );
    expect(drilldownRows[0]).toMatchObject({
      apiKeyHash: 'abcdef1234567890',
      apiKeyLabel: 'Team Alpha Key',
    });
  });

  it('builds API key/model matrices and key anomaly rows from ranked usage rows', () => {
    const apiKeyRows = buildApiKeyRows(
      [
        {
          id: 'hash-a',
          api_key_hash: 'abcdef1234567890',
          account_snapshot: 'team-alpha',
          calls: 100,
          success_calls: 92,
          failure_calls: 8,
          success_rate: 0.92,
          input_tokens: 1000,
          output_tokens: 500,
          cached_tokens: 120,
          cache_read_tokens: 90,
          cache_creation_tokens: 30,
          total_tokens: 1500,
          cost: 9,
          average_latency_ms: 300,
          last_seen_ms: NOW_MS,
          models: [
            {
              model: 'gpt-4o',
              calls: 80,
              success_calls: 74,
              failure_calls: 6,
              success_rate: 0.925,
              input_tokens: 900,
              output_tokens: 400,
              cached_tokens: 100,
              cache_read_tokens: 80,
              cache_creation_tokens: 20,
              total_tokens: 1300,
              cost: 8,
              last_seen_ms: NOW_MS,
            },
          ],
        },
      ],
      undefined
    );

    const matrix = buildUsageMatrix({
      apiKeyRows,
      credentialRows: [],
      dimension: 'apiKeyModel',
      metric: 'requestCount',
    });
    const anomalies = buildKeyAnomalies(apiKeyRows);

    expect(matrix.rowLabels).toEqual(['sk-****7890']);
    expect(matrix.columnLabels).toEqual(['gpt-4o']);
    expect(matrix.cells[0]).toMatchObject({
      rowLabel: 'sk-****7890',
      columnLabel: 'gpt-4o',
      requestCount: 80,
      failureRate: 6 / 80,
      value: 80,
    });
    expect(anomalies[0]).toMatchObject({
      id: 'abcdef1234567890',
      label: 'sk-****7890',
      severity: 'high',
      reasonKey: 'usage_analytics.anomaly_reason_cost_spike',
    });
  });

  it('does not double-count provider/model matrix rows from API key and credential projections', () => {
    const usageRow = (overrides: Partial<UsageRankRow>): UsageRankRow => ({
      id: 'row',
      label: 'row',
      requestCount: 0,
      successCount: 0,
      failureCount: 0,
      successRate: 0,
      totalTokens: 0,
      inputTokens: 0,
      outputTokens: 0,
      cachedTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      estimatedCost: 0,
      averageLatencyMs: null,
      share: 0,
      ...overrides,
    });
    const apiKeyRows = [
      usageRow({
        id: 'hash-a',
        label: 'sk-****7890',
        apiKeyHash: 'abcdef1234567890',
        provider: 'OpenAI',
        requestCount: 10,
        successCount: 10,
        totalTokens: 100,
        models: [
          usageRow({
            id: 'gpt-4o',
            label: 'gpt-4o',
            model: 'gpt-4o',
            requestCount: 10,
            successCount: 10,
            totalTokens: 100,
            estimatedCost: 1,
          }),
        ],
      }),
    ];
    const credentialRows = [
      usageRow({
        id: 'credential-a',
        label: 'prod',
        provider: 'OpenAI',
        authFile: 'prod.json',
        requestCount: 10,
        successCount: 10,
        totalTokens: 100,
        models: [
          usageRow({
            id: 'gpt-4o',
            label: 'gpt-4o',
            model: 'gpt-4o',
            requestCount: 10,
            successCount: 10,
            totalTokens: 100,
            estimatedCost: 1,
          }),
        ],
      }),
    ];

    const matrix = buildUsageMatrix({
      apiKeyRows,
      credentialRows,
      dimension: 'providerModel',
      metric: 'requestCount',
    });

    expect(matrix.rowLabels).toEqual(['OpenAI']);
    expect(matrix.columnLabels).toEqual(['gpt-4o']);
    expect(matrix.cells[0]).toMatchObject({
      rowLabel: 'OpenAI',
      columnLabel: 'gpt-4o',
      requestCount: 10,
      totalTokens: 100,
      value: 10,
    });
  });

  it('keeps provider request, cost, and token shares separate', () => {
    const rows = buildProviderRows([
      {
        auth_index: 'auth-a',
        auth_provider_snapshot: 'codex',
        calls: 80,
        success: 78,
        failure: 2,
        tokens: 900,
        cost: 9,
        average_latency_ms: null,
      },
      {
        auth_index: 'auth-b',
        auth_provider_snapshot: 'mimo',
        calls: 20,
        success: 18,
        failure: 2,
        tokens: 100,
        cost: 1,
        average_latency_ms: null,
      },
    ]);

    expect(rows[0]).toMatchObject({
      label: 'codex',
      requestShare: 0.8,
      costShare: 0.9,
      tokenShare: 0.9,
      share: 0.8,
    });
    expect(rows[1]).toMatchObject({
      label: 'mimo',
      requestShare: 0.2,
      costShare: 0.1,
      tokenShare: 0.1,
      share: 0.2,
    });
  });

  it('preserves normalized cache totals while merging provider model aliases', () => {
    const usageRow = (overrides: Partial<UsageRankRow>): UsageRankRow => ({
      id: 'row',
      label: 'row',
      requestCount: 0,
      successCount: 0,
      failureCount: 0,
      successRate: 0,
      totalTokens: 0,
      inputTokens: 0,
      outputTokens: 0,
      cachedTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      estimatedCost: 0,
      averageLatencyMs: null,
      share: 0,
      ...overrides,
    });
    const credentialRows = [
      usageRow({
        id: 'credential-a',
        label: 'a',
        provider: 'OpenAI',
        models: [
          usageRow({
            id: 'internal-fast',
            label: 'internal-fast',
            model: 'internal-fast',
            inputTokens: 100,
            cacheReadTokens: 90,
            cacheHitTokens: 90,
            cacheHitInputTokens: 100,
          }),
        ],
      }),
      usageRow({
        id: 'credential-b',
        label: 'b',
        provider: 'OpenAI',
        models: [
          usageRow({
            id: 'internal-fast',
            label: 'internal-fast',
            model: 'internal-fast',
            inputTokens: 100,
            cacheReadTokens: 50,
            cacheCreationTokens: 50,
            cacheHitTokens: 50,
            cacheHitInputTokens: 200,
          }),
        ],
      }),
    ];

    const rows = buildProviderRows(
      [
        {
          auth_index: 'auth-a',
          auth_provider_snapshot: 'OpenAI',
          calls: 2,
          success: 2,
          failure: 0,
          tokens: 200,
          cost: 0,
          average_latency_ms: null,
        },
      ],
      [],
      credentialRows
    );

    expect(rows[0].cacheRate).toBeCloseTo(140 / 300, 6);
  });

  it('estimates drilldown preview cost from model cost per token', () => {
    const rows = buildDrilldownPreview(
      [
        {
          event_hash: 'event-a',
          timestamp_ms: NOW_MS,
          model: 'gpt-4o',
          endpoint: '/v1/chat/completions',
          method: 'POST',
          path: '/v1/chat/completions',
          auth_index: '0',
          source: 'codex',
          source_hash: 'source-a',
          api_key_hash: 'abcdef1234567890',
          account_snapshot: 'team-alpha',
          auth_label_snapshot: 'prod',
          auth_provider_snapshot: 'openai',
          input_tokens: 60,
          output_tokens: 40,
          cached_tokens: 0,
          cache_read_tokens: 0,
          cache_creation_tokens: 0,
          reasoning_tokens: 0,
          total_tokens: 100,
          latency_ms: 250,
          failed: false,
          header_error_kind: 'rate_limit',
          header_error_code: 'retry_after',
          header_trace_id: 'req-header',
          header_quota_plan_type: 'plus',
          header_quota_used_percent: 87,
          header_quota_recover_at_ms: 1780000060000,
        },
      ],
      [
        {
          id: 'gpt-4o',
          label: 'gpt-4o',
          model: 'gpt-4o',
          requestCount: 10,
          successCount: 10,
          failureCount: 0,
          successRate: 1,
          totalTokens: 1000,
          inputTokens: 700,
          outputTokens: 300,
          cachedTokens: 0,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          estimatedCost: 2,
          averageLatencyMs: null,
          share: 1,
        },
      ]
    );

    expect(rows[0]).toMatchObject({
      eventHash: 'event-a',
      model: 'gpt-4o',
      estimatedCost: 0.2,
      headerErrorKind: 'rate_limit',
      headerErrorCode: 'retry_after',
      headerTraceId: 'req-header',
      headerQuotaPlanType: 'plus',
      headerQuotaUsedPercent: 87,
      headerQuotaRecoverAtMs: 1780000060000,
    });
  });
});

describe('cache hit rate', () => {
  it('uses normalized total input as the denominator for Anthropic usage', () => {
    expect(
      computeCacheHitRate({
        inputTokens: 450,
        cacheReadTokens: 300,
        cacheCreationTokens: 50,
        cachedTokens: 0,
      })
    ).toBeCloseTo(300 / 450, 6);
  });

  it('falls back to cached tokens for OpenAI usage where input already includes cache', () => {
    expect(
      computeCacheHitRate({
        inputTokens: 1000,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        cachedTokens: 400,
      })
    ).toBeCloseTo(0.4, 6);
  });

  it('does not double count GPT-5.6 fine-grained cache tokens', () => {
    expect(
      computeCacheHitRate({
        modelName: 'openai/gpt-5.6-sol',
        inputTokens: 152_600,
        cacheReadTokens: 151_000,
        cacheCreationTokens: 1_000,
        cachedTokens: 0,
      })
    ).toBeCloseTo(151_000 / 152_600, 6);
  });

  it('returns 0 without input and clamps malformed ratios to 1', () => {
    expect(
      computeCacheHitRate({
        inputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        cachedTokens: 0,
      })
    ).toBe(0);
    expect(
      computeCacheHitRate({
        inputTokens: 10,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        cachedTokens: 1000,
      })
    ).toBe(1);
  });
});

describe('model rank derivations', () => {
  const rankRow = (overrides: Partial<UsageRankRow>): UsageRankRow => ({
    id: 'row',
    label: 'row',
    requestCount: 0,
    successCount: 0,
    failureCount: 0,
    successRate: 0,
    totalTokens: 0,
    inputTokens: 0,
    outputTokens: 0,
    cachedTokens: 0,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
    estimatedCost: 0,
    averageLatencyMs: null,
    share: 0,
    ...overrides,
  });

  it('derives per-row cache hit rate and average cost per call', () => {
    const row = rankRow({
      requestCount: 50,
      inputTokens: 450,
      cacheReadTokens: 300,
      cacheCreationTokens: 50,
      estimatedCost: 10,
    });
    expect(computeRowCacheHitRate(row)).toBeCloseTo(300 / 450, 6);
    expect(computeRowAverageCostPerCall(row)).toBeCloseTo(0.2, 6);
    expect(computeRowAverageCostPerCall(rankRow({ estimatedCost: 10 }))).toBe(0);
  });

  it('uses model-aware cache semantics for GPT-5.6 rank rows', () => {
    const row = rankRow({
      model: 'gpt-5.6-sol',
      label: 'gpt-5.6-sol',
      inputTokens: 152_600,
      cacheReadTokens: 151_000,
      cacheCreationTokens: 1_000,
    });
    expect(computeRowCacheHitRate(row)).toBeCloseTo(151_000 / 152_600, 6);
  });

  it('uses server-normalized cache totals when a display alias hides GPT-5.6', () => {
    const row = rankRow({
      model: 'internal-fast',
      label: 'internal-fast',
      inputTokens: 152_600,
      cacheReadTokens: 151_000,
      cacheCreationTokens: 1_000,
      cacheHitTokens: 151_000,
      cacheHitInputTokens: 152_600,
      cacheHitRate: 151_000 / 152_600,
    });
    expect(computeRowCacheHitRate(row)).toBeCloseTo(151_000 / 152_600, 6);
    expect(computeRowCacheHitRate(rankRow({ models: [row] }))).toBeCloseTo(151_000 / 152_600, 6);
  });

  it('builds the reverse key distribution for a model from API key breakdowns', () => {
    const apiKeyRows = [
      rankRow({
        id: 'hash-a',
        label: 'sk-****aaaa',
        models: [
          rankRow({ id: 'gpt-5.5', label: 'gpt-5.5', totalTokens: 900 }),
          rankRow({ id: 'glm-5', label: 'glm-5', totalTokens: 50 }),
        ],
      }),
      rankRow({
        id: 'hash-b',
        label: 'sk-****bbbb',
        models: [rankRow({ id: 'gpt-5.5', label: 'gpt-5.5', totalTokens: 100 })],
      }),
      rankRow({ id: 'hash-c', label: 'sk-****cccc', models: [] }),
    ];

    const distribution = buildModelKeyDistribution('gpt-5.5', apiKeyRows);

    expect(distribution).toEqual([
      { id: 'hash-a', label: 'sk-****aaaa', totalTokens: 900, share: 0.9 },
      { id: 'hash-b', label: 'sk-****bbbb', totalTokens: 100, share: 0.1 },
    ]);
    expect(buildModelKeyDistribution('unknown-model', apiKeyRows)).toEqual([]);
  });

  it('caps the reverse key distribution at the requested limit', () => {
    const apiKeyRows = Array.from({ length: 6 }, (_, index) =>
      rankRow({
        id: `hash-${index}`,
        label: `sk-****${index}`,
        models: [rankRow({ id: 'gpt-5.5', totalTokens: 100 - index })],
      })
    );
    expect(buildModelKeyDistribution('gpt-5.5', apiKeyRows)).toHaveLength(4);
  });
});

describe('usage anomaly drilldown', () => {
  it('detects request, cost, average-token, and cache-hit anomalies', () => {
    const timeline = buildUsageTimeline(
      [
        {
          bucket_ms: NOW_MS,
          label: '',
          calls: 10,
          tokens: 100,
          success: 10,
          failure: 0,
          input_tokens: 100,
          output_tokens: 0,
          cache_read_tokens: 80,
          cost: 1,
        },
        {
          bucket_ms: NOW_MS + HOUR_MS,
          label: '',
          calls: 25,
          tokens: 500,
          success: 24,
          failure: 1,
          input_tokens: 500,
          output_tokens: 0,
          cache_read_tokens: 100,
          cost: 3,
        },
      ],
      'hour'
    );

    const analysis = analyzeUsageBucket(timeline, NOW_MS + HOUR_MS);

    expect(analysis?.anomalies.map((item) => item.key)).toEqual([
      'request_spike',
      'cost_spike',
      'token_per_request_spike',
      'cache_hit_drop',
    ]);
    expect(analysis?.causeKeys).toEqual([
      'usage_analytics.cause_request_spike',
      'usage_analytics.cause_cost_spike',
      'usage_analytics.cause_token_per_request_spike',
      'usage_analytics.cause_cache_hit_drop',
    ]);
  });

  it('uses direction-aware anomaly cause copy', () => {
    expect(
      buildUsageAnomalyCauseKeys({
        requestCount: -0.8,
        totalTokens: -0.8,
        inputTokens: -0.8,
        outputTokens: -0.8,
        cachedTokens: 0,
        cacheCreationTokens: 0,
        estimatedCost: -0.7,
        cacheHitRate: 0,
        averageTokensPerRequest: 0,
      })
    ).toEqual(['usage_analytics.cause_request_drop', 'usage_analytics.cause_cost_drop']);
  });

  it('detects cache growth from fine-grained cache buckets', () => {
    const timeline = buildUsageTimeline(
      [
        {
          bucket_ms: NOW_MS,
          label: '',
          calls: 1,
          tokens: 100,
          success: 1,
          failure: 0,
          input_tokens: 100,
          cache_read_tokens: 10,
          cache_creation_tokens: 0,
        },
        {
          bucket_ms: NOW_MS + HOUR_MS,
          label: '',
          calls: 1,
          tokens: 100,
          success: 1,
          failure: 0,
          input_tokens: 100,
          cache_read_tokens: 25,
          cache_creation_tokens: 5,
        },
      ],
      'hour'
    );

    const analysis = analyzeUsageBucket(timeline, NOW_MS + HOUR_MS);

    expect(analysis?.changes.cachedTokens).toBe(2);
    expect(analysis?.causeKeys).toContain('usage_analytics.cause_cache_growth');
  });

  it('builds stable monitoring detail query parameters', () => {
    const point = buildUsageTimeline(
      [
        {
          bucket_ms: NOW_MS,
          label: '',
          calls: 1,
          tokens: 1,
          success: 1,
          failure: 0,
        },
      ],
      'hour'
    )[0];

    expect(
      buildMonitoringDetailUrl(point, {
        model: 'gpt-4o',
        apiKeyHash: ' ABCDEF1234 ',
        provider: 'OpenAI',
        authFile: 'codex-auth.json',
        projectId: 'project-1',
        requestType: 'codex',
        status: 'failed',
        searchQuery: ' req-42 ',
        minLatencyMs: '10000',
        cacheStatus: 'hit',
      })
    ).toBe(
      `/monitoring?from_ms=${NOW_MS}&to_ms=${NOW_MS + HOUR_MS}&model=gpt-4o&api_key_hash=abcdef1234&provider=openai&auth_file=codex-auth.json&project_id=project-1&request_type=codex&status=failed&search=req-42&min_latency_ms=10000&cache_status=hit`
    );
  });
});
