import type { TFunction } from 'i18next';
import { describe, expect, it } from 'vitest';
import type {
  UsageRankRow,
  UsageSummaryDelta,
  UsageSummaryMetrics,
  UsageTimelinePoint,
} from './usageAnalyticsModel';
import {
  buildCredentialDetailCards,
  buildUsageApiKeySummaryCards,
  buildUsageEntitySummaryCards,
  buildUsageModelSummaryCards,
  buildUsageOverviewSummaryCards,
  buildUsageTrendSummaryCards,
  formatUsageDurationMs,
} from './usageAnalyticsPresentation';

const t = ((key: string, options?: Record<string, unknown>) => {
  if (!options) return key;
  return Object.entries(options).reduce(
    (value, [name, replacement]) => value.replace(`{{${name}}}`, String(replacement)),
    key
  );
}) as TFunction;

const summary: UsageSummaryMetrics = {
  requestCount: 2400,
  totalTokens: 120000,
  inputTokens: 70000,
  outputTokens: 42000,
  cachedTokens: 3000,
  cacheReadTokens: 4000,
  cacheCreationTokens: 1000,
  estimatedCost: 18.75,
  averageCostPerCall: 0.0078,
  successRate: 0.975,
  failureCount: 60,
  averageLatencyMs: 260,
  p95LatencyMs: 420,
  p95TtftMs: null,
  rpm30m: 0,
  tpm30m: 0,
};

const summaryDelta: UsageSummaryDelta = {
  hasComparison: true,
  requestCount: 0.12,
  totalTokens: -0.08,
  estimatedCost: 0.05,
};

const timelinePoint = (overrides: Partial<UsageTimelinePoint> = {}): UsageTimelinePoint => ({
  bucketMs: 1,
  bucketEndMs: 2,
  label: '10:00',
  requestCount: 10,
  totalTokens: 1000,
  inputTokens: 600,
  outputTokens: 300,
  cachedTokens: 100,
  cacheReadTokens: 80,
  cacheCreationTokens: 20,
  reasoningTokens: 10,
  estimatedCost: 1,
  successCount: 9,
  failureCount: 1,
  successRate: 0.9,
  failureRate: 0.1,
  averageLatencyMs: 200,
  p95LatencyMs: 320,
  p95TtftMs: 100,
  cacheHitRate: 0.12,
  averageTokensPerRequest: 100,
  ...overrides,
});

describe('usageAnalyticsPresentation', () => {
  it('formats usage durations as ms below one second and s above it', () => {
    expect(formatUsageDurationMs(null)).toBe('-');
    expect(formatUsageDurationMs(420)).toBe('420ms');
    expect(formatUsageDurationMs(1000)).toBe('1s');
    expect(formatUsageDurationMs(102188)).toBe('102.2s');
  });

  it('builds the overview summary as eight request, health, cost, and token cards', () => {
    const cards = buildUsageOverviewSummaryCards({
      anomalyCount: 3,
      locale: 'en',
      reasoningTokens: 1200,
      summary,
      summaryDelta,
      t,
    });

    expect(cards.map((card) => card.label)).toEqual([
      'usage_analytics.metric_request_count',
      'usage_analytics.success_rate',
      'usage_analytics.metric_failure_count',
      'usage_analytics.metric_estimated_cost',
      'usage_analytics.metric_total_tokens',
      'usage_analytics.metric_input_tokens',
      'usage_analytics.metric_output_tokens',
      'usage_analytics.metric_cached_tokens',
    ]);
    expect(cards[1]).toMatchObject({
      accent: 'green',
      icon: 'success',
      meta: 'usage_analytics.metric_p95_latency 420ms',
      tone: 'good',
    });
    expect(cards[4].meta).toContain('usage_analytics.metric_reasoning_tokens');
    expect(cards[7].meta).toContain('usage_analytics.cache_read_rate');
    expect(cards[7]).toMatchObject({ value: '8.0K', valueTitle: '8,000' });
  });

  it('shows fine-grained cache buckets in credential detail cache totals', () => {
    const cards = buildCredentialDetailCards({
      locale: 'en',
      row: {
        id: 'credential-a',
        label: 'credential-a',
        requestCount: 10,
        successCount: 10,
        failureCount: 0,
        successRate: 1,
        totalTokens: 1000,
        inputTokens: 700,
        outputTokens: 300,
        cachedTokens: 0,
        cacheReadTokens: 80,
        cacheCreationTokens: 20,
        estimatedCost: 1,
        averageLatencyMs: 200,
        share: 1,
      },
      t,
    });

    expect(cards[3].meta).toBe('usage_analytics.metric_cached_tokens 100');
  });

  it('builds trend summary cards from peak buckets and comparison deltas', () => {
    const cards = buildUsageTrendSummaryCards({
      locale: 'en',
      summaryDelta,
      timeline: [
        timelinePoint({ label: '10:00', requestCount: 10, failureRate: 0, p95LatencyMs: 320 }),
        timelinePoint({
          label: '11:00',
          requestCount: 80,
          failureRate: 0.2,
          p95LatencyMs: 102188,
        }),
      ],
      t,
    });

    expect(cards[0]).toMatchObject({
      label: 'usage_analytics.trend_peak_request_bucket',
      meta: '80 usage_analytics.metric_request_count',
      value: '11:00',
    });
    expect(cards[2].value).toBe('+12.0%');
    expect(cards[5]).toMatchObject({ tone: 'bad', value: '20.0%' });
    expect(cards[6].value).toBe('102.2s');
  });

  it('uses entity-specific anomaly labels for entity summaries', () => {
    const cards = buildUsageEntitySummaryCards({
      activeAccent: 'blue',
      activeCount: 4,
      activeIcon: 'key',
      activeLabel: 'usage_analytics.active_api_keys',
      activeMeta: 'usage_analytics.summary_meta',
      anomalyCount: 2,
      anomalyLabel: 'usage_analytics.anomaly_keys',
      locale: 'en',
      summary,
      t,
    });

    expect(cards).toHaveLength(5);
    expect(cards[0]).toMatchObject({
      accent: 'blue',
      icon: 'key',
      value: '4',
    });
    expect(cards[4]).toMatchObject({
      accent: 'red',
      icon: 'anomaly',
      label: 'usage_analytics.anomaly_keys',
      tone: 'bad',
      value: '2',
    });
  });

  it('builds model summary cards from model-dimension stats', () => {
    const modelRow = (overrides: Partial<UsageRankRow>): UsageRankRow => ({
      id: 'model',
      label: 'model',
      requestCount: 100,
      successCount: 100,
      failureCount: 0,
      successRate: 1,
      totalTokens: 1000,
      inputTokens: 600,
      outputTokens: 400,
      cachedTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      estimatedCost: 10,
      averageLatencyMs: null,
      share: 0.5,
      ...overrides,
    });
    const cards = buildUsageModelSummaryCards({
      locale: 'en',
      modelRows: [
        modelRow({ id: 'gpt-5.5', label: 'gpt-5.5', share: 0.9, successRate: 0.99 }),
        modelRow({ id: 'gpt-5.4-mini', label: 'gpt-5.4-mini', share: 0.06, successRate: 0.71 }),
        modelRow({ id: 'glm-5', label: 'glm-5', share: 0.04, successRate: 0.25, requestCount: 0 }),
      ],
      summary,
      t,
    });

    expect(cards.map((card) => card.label)).toEqual([
      'usage_analytics.active_models',
      'usage_analytics.model_top_cost_share',
      'usage_analytics.model_lowest_success',
      'usage_analytics.model_long_tail_share',
      'usage_analytics.metric_estimated_cost',
    ]);
    expect(cards[0].value).toBe('3');
    expect(cards[1]).toMatchObject({ meta: 'gpt-5.5', tone: 'warn', value: '90.0%' });
    // glm-5 has zero requests, so the lowest-success slot falls to gpt-5.4-mini.
    expect(cards[2]).toMatchObject({ meta: 'gpt-5.4-mini', tone: 'bad', value: '71.0%' });
    expect(cards[3].value).toBe('10.0%');

    // A 100% top share is trivially true with a single costed model — no warn tone.
    const singleCostedCards = buildUsageModelSummaryCards({
      locale: 'en',
      modelRows: [
        modelRow({ id: 'gpt-5.5', label: 'gpt-5.5', share: 1, estimatedCost: 10 }),
        modelRow({ id: 'glm-5', label: 'glm-5', share: 0, estimatedCost: 0 }),
      ],
      summary,
      t,
    });
    expect(singleCostedCards[1].tone).toBeUndefined();
    expect(singleCostedCards[1].value).toBe('100.0%');
  });

  it('builds API key summary cards from key-dimension stats', () => {
    const keyRow = (overrides: Partial<UsageRankRow>): UsageRankRow => ({
      id: 'key',
      label: 'sk-****0001',
      apiKeyHash: 'hash-0001',
      requestCount: 100,
      successCount: 100,
      failureCount: 0,
      successRate: 1,
      totalTokens: 1000,
      inputTokens: 600,
      outputTokens: 400,
      cachedTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      estimatedCost: 10,
      averageLatencyMs: null,
      share: 0.5,
      ...overrides,
    });
    const cards = buildUsageApiKeySummaryCards({
      apiKeyRows: [
        keyRow({ id: 'k1', label: 'sk-****0001', share: 0.9, successRate: 0.99 }),
        keyRow({ id: 'k2', label: 'sk-****0002', share: 0.06, successRate: 0.71 }),
        keyRow({ id: 'k3', label: 'sk-****0003', share: 0.04, successRate: 0.25, requestCount: 0 }),
      ],
      keyAnomalyCount: 2,
      locale: 'en',
      summary,
      t,
    });

    expect(cards.map((card) => card.label)).toEqual([
      'usage_analytics.active_api_keys',
      'usage_analytics.api_key_top_cost_share',
      'usage_analytics.api_key_lowest_success',
      'usage_analytics.metric_average_cost_per_call',
      'usage_analytics.anomaly_keys',
    ]);
    expect(cards[0].value).toBe('3');
    expect(cards[1]).toMatchObject({ meta: 'sk-****0001', tone: 'warn', value: '90.0%' });
    // k3 has zero requests, so the lowest-success slot falls to k2.
    expect(cards[2]).toMatchObject({ meta: 'sk-****0002', tone: 'bad', value: '71.0%' });
    expect(cards[4]).toMatchObject({ tone: 'bad', value: '2' });

    // A 100% top share is trivially true with a single costed key — no warn tone.
    const singleCostedCards = buildUsageApiKeySummaryCards({
      apiKeyRows: [
        keyRow({ id: 'k1', share: 1, estimatedCost: 10 }),
        keyRow({ id: 'k2', share: 0, estimatedCost: 0 }),
      ],
      keyAnomalyCount: 0,
      locale: 'en',
      summary,
      t,
    });
    expect(singleCostedCards[1].tone).toBeUndefined();
    expect(singleCostedCards[1].value).toBe('100.0%');
    expect(singleCostedCards[4].tone).toBeUndefined();
  });
});
