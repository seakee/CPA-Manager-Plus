import { describe, expect, it } from 'vitest';
import type { AuthFileItem } from '@/types';
import type {
  MonitoringAnalyticsAccountStatRow,
  MonitoringAnalyticsSummary,
} from '@/services/api/usageService';
import { buildAccountRows, type AccountQuotaStores } from './accountRows';
import {
  buildUsageValueRowFromMonitoringSummary,
  buildFallbackTimeline,
  buildUsageValueRowsFromMonitoring,
  buildUsageValueRowsFromRecent,
  buildUsageValueSummary,
  filterUsageValueRows,
} from './usageValueRows';

const emptyStores = (): AccountQuotaStores => ({
  antigravityQuota: {},
  claudeQuota: {},
  codexQuota: {},
  kimiQuota: {},
  devinQuota: {},
  xaiQuota: {},
});

const makeStat = (
  overrides: Partial<MonitoringAnalyticsAccountStatRow> = {}
): MonitoringAnalyticsAccountStatRow => ({
  id: 'stat-1',
  calls: 0,
  success_calls: 0,
  failure_calls: 0,
  success_rate: 0,
  input_tokens: 0,
  output_tokens: 0,
  cached_tokens: 0,
  cache_read_tokens: 0,
  cache_creation_tokens: 0,
  total_tokens: 0,
  cost: 0,
  average_latency_ms: null,
  last_seen_ms: 0,
  ...overrides,
});

const makeSummary = (
  overrides: Partial<MonitoringAnalyticsSummary> = {}
): MonitoringAnalyticsSummary => ({
  total_calls: 0,
  success_calls: 0,
  failure_calls: 0,
  success_rate: 0,
  input_tokens: 0,
  output_tokens: 0,
  cached_tokens: 0,
  cache_read_tokens: 0,
  cache_creation_tokens: 0,
  reasoning_tokens: 0,
  total_tokens: 0,
  total_cost: 0,
  average_latency_ms: null,
  zero_token_calls: 0,
  rpm_30m: 0,
  tpm_30m: 0,
  avg_daily_requests: 0,
  avg_daily_tokens: 0,
  approx_tasks: 0,
  approx_task_failures: 0,
  approx_task_success_rate: 0,
  zero_token_models: [],
  ...overrides,
});

describe('usageValueRows', () => {
  it('derives fallback timeline failures from the remaining request count', () => {
    const timeline = buildFallbackTimeline([
      {
        key: 'monitoring:one',
        accountLabel: 'One',
        fileName: 'one.json',
        provider: 'codex',
        requests: 10,
        successRate: 80,
        inputTokens: 100,
        outputTokens: 50,
        estimatedCost: 0,
        lastSeenMs: null,
        rating: 'normal',
        source: 'monitoring',
      },
    ]);

    expect(timeline[0]).toMatchObject({ calls: 10, success: 8, failure: 2 });
  });

  it('builds fallback value rows from auth-file recent request buckets', () => {
    const files: AuthFileItem[] = [
      {
        name: 'codex-active.json',
        type: 'codex',
        email: 'active@example.com',
        recent_requests: [
          { success: 4, failed: 1 },
          { success: 5, failed: 0 },
        ],
      },
      {
        name: 'claude-idle.json',
        type: 'claude',
        recent_requests: [{ success: 0, failed: 1 }],
      },
    ];
    const rows = buildUsageValueRowsFromRecent(buildAccountRows(files, emptyStores()));

    expect(rows[0]).toMatchObject({
      key: 'recent:codex-active.json',
      accountLabel: 'active@example.com',
      provider: 'codex',
      requests: 10,
      successRate: 90,
      estimatedCost: 0.18,
      rating: 'normal',
      source: 'recent',
    });
    expect(rows[1]).toMatchObject({
      requests: 1,
      successRate: 0,
      rating: 'low',
    });
  });

  it('matches monitoring stats to account rows by auth index or account snapshot', () => {
    const accountRows = buildAccountRows(
      [
        {
          name: 'codex-a.json',
          type: 'codex',
          authIndex: 'auth-a',
          email: 'a@example.com',
        },
        {
          name: 'claude-b.json',
          type: 'claude',
          email: 'b@example.com',
        },
      ],
      emptyStores()
    );

    const rows = buildUsageValueRowsFromMonitoring(accountRows, [
      makeStat({
        id: 'by-auth-index',
        auth_indices: ['auth-a'],
        calls: 120,
        success_rate: 0.95,
        input_tokens: 1000,
        output_tokens: 400,
        cost: 1.25,
        last_seen_ms: 1000,
      }),
      makeStat({
        id: 'by-account-snapshot',
        account_snapshot: 'b@example.com',
        calls: 10,
        success_rate: 0.8,
        input_tokens: 200,
        output_tokens: 50,
        cost: 0.2,
        last_seen_ms: 2000,
      }),
    ]);

    expect(rows[0]).toMatchObject({
      accountLabel: 'a@example.com',
      fileName: 'codex-a.json',
      provider: 'codex',
      requests: 120,
      successRate: 95,
      rating: 'high',
      row: accountRows[0],
    });
    expect(rows[1]).toMatchObject({
      accountLabel: 'b@example.com',
      fileName: 'claude-b.json',
      provider: 'claude',
      successRate: 80,
      row: accountRows[1],
    });
  });

  it('uses the filtered summary across split account stats and keeps the latest activity time', () => {
    const row = buildAccountRows(
      [
        {
          name: 'codex.json',
          type: 'codex',
          authIndex: 'auth-1',
          email: 'current@example.com',
        },
      ],
      emptyStores()
    )[0];
    const value = buildUsageValueRowFromMonitoringSummary(
      row,
      makeSummary({
        total_calls: 15,
        success_calls: 12,
        failure_calls: 3,
        success_rate: 0.8,
        input_tokens: 1_000,
        output_tokens: 300,
        total_tokens: 1_500,
        total_cost: 0.75,
      }),
      [
        makeStat({
          id: 'old-label',
          account_snapshot: 'old@example.com',
          calls: 6,
          last_seen_ms: 1_700_000_000_100,
        }),
        makeStat({
          id: 'current-label',
          account_snapshot: 'current@example.com',
          calls: 9,
          last_seen_ms: 1_700_000_000_900,
        }),
      ]
    );

    expect(value).toMatchObject({
      requests: 15,
      successRate: 80,
      inputTokens: 1_000,
      outputTokens: 300,
      totalTokens: 1_500,
      estimatedCost: 0.75,
      lastSeenMs: 1_700_000_000_900,
      source: 'monitoring',
      row,
    });
  });

  it('aggregates split account stats when a legacy response omits summary', () => {
    const row = buildAccountRows(
      [{ name: 'codex.json', type: 'codex', authIndex: 'auth-1' }],
      emptyStores()
    )[0];
    const value = buildUsageValueRowFromMonitoringSummary(row, undefined, [
      makeStat({
        id: 'first',
        calls: 4,
        success_calls: 3,
        input_tokens: 100,
        output_tokens: 20,
        total_tokens: 140,
        cost: 0.1,
        last_seen_ms: 100,
      }),
      makeStat({
        id: 'second',
        calls: 6,
        success_calls: 5,
        input_tokens: 200,
        output_tokens: 30,
        total_tokens: 260,
        cost: 0.2,
        last_seen_ms: 200,
      }),
    ]);

    expect(value).toMatchObject({
      requests: 10,
      successRate: 80,
      inputTokens: 300,
      outputTokens: 50,
      totalTokens: 400,
      lastSeenMs: 200,
    });
    expect(value?.estimatedCost).toBeCloseTo(0.3);
  });

  it('keeps a successful empty monitoring response as an empty monitoring result', () => {
    const row = buildAccountRows(
      [{ name: 'codex.json', type: 'codex', authIndex: 'auth-1' }],
      emptyStores()
    )[0];

    expect(buildUsageValueRowFromMonitoringSummary(row, undefined, [])).toMatchObject({
      requests: 0,
      successRate: null,
      inputTokens: 0,
      outputTokens: 0,
      totalTokens: 0,
      estimatedCost: 0,
      lastSeenMs: null,
      source: 'monitoring',
      row,
    });
  });

  it('summarizes value rows with request-weighted average success rate', () => {
    const summary = buildUsageValueSummary(
      [
        {
          key: 'one',
          accountLabel: 'one',
          fileName: 'one.json',
          provider: 'codex',
          requests: 100,
          successRate: 90,
          inputTokens: 0,
          outputTokens: 0,
          estimatedCost: 1,
          lastSeenMs: null,
          rating: 'high',
          source: 'monitoring',
        },
        {
          key: 'two',
          accountLabel: 'two',
          fileName: 'two.json',
          provider: 'claude',
          requests: 10,
          successRate: 50,
          inputTokens: 0,
          outputTokens: 0,
          estimatedCost: 0.5,
          lastSeenMs: null,
          rating: 'low',
          source: 'monitoring',
        },
      ],
      'monitoring'
    );

    expect(summary.weeklyValue).toBe(1.5);
    expect(summary.historicalValue).toBe(1.5);
    expect(summary.highValueAccounts).toBe(1);
    expect(summary.lowActivityAccounts).toBe(1);
    expect(summary.averageSuccessRate).toBeCloseTo((90 * 100 + 50 * 10) / 110);
    expect(summary.source).toBe('monitoring');
  });

  it('filters usage rows by provider and search text', () => {
    const rows = buildUsageValueRowsFromRecent(
      buildAccountRows(
        [
          { name: 'codex-a.json', type: 'codex', email: 'alice@example.com' },
          { name: 'claude-b.json', type: 'claude', email: 'bob@example.com' },
        ],
        emptyStores()
      )
    );

    expect(filterUsageValueRows(rows, { provider: 'codex', search: '' })).toHaveLength(1);
    expect(
      filterUsageValueRows(rows, { provider: 'all', search: 'bob' }).map((row) => row.fileName)
    ).toEqual(['claude-b.json']);
  });
});
