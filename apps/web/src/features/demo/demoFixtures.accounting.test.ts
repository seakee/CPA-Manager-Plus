import { describe, expect, it } from 'vitest';
import { getDemoDashboardSummary, getDemoMonitoringAnalytics } from './demoFixtures';

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const collectCanonicalTokenRecords = (
  value: unknown,
  path = 'root',
  result: Array<{ path: string; record: Record<string, unknown> }> = []
) => {
  if (Array.isArray(value)) {
    value.forEach((item, index) => collectCanonicalTokenRecords(item, `${path}[${index}]`, result));
    return result;
  }
  if (!isRecord(value)) return result;

  const required = [
    'input_tokens',
    'output_tokens',
    'non_reasoning_output_tokens',
    'reasoning_tokens',
    'total_tokens',
  ];
  if (required.every((key) => typeof value[key] === 'number')) {
    result.push({ path, record: value });
  }
  Object.entries(value).forEach(([key, nested]) =>
    collectCanonicalTokenRecords(nested, `${path}.${key}`, result)
  );
  return result;
};

describe('demo token accounting fixtures', () => {
  it('keeps every canonical token record non-overlapping after rounding', () => {
    const records = collectCanonicalTokenRecords({
      dashboard: getDemoDashboardSummary(),
      analytics: getDemoMonitoringAnalytics(),
    });

    expect(records.length).toBeGreaterThan(0);
    records.forEach(({ path, record }) => {
      const input = Number(record.input_tokens);
      const output = Number(record.output_tokens);
      const nonReasoning = Number(record.non_reasoning_output_tokens);
      const reasoning = Number(record.reasoning_tokens);
      const cacheRead = Number(record.cache_read_tokens ?? 0);
      const cacheWrite = Number(record.cache_creation_tokens ?? 0);
      const unclassified = Number(record.unclassified_tokens ?? 0);
      const total = Number(record.total_tokens);

      expect(input, `${path}.input_tokens`).toBeGreaterThanOrEqual(cacheRead + cacheWrite);
      expect(output, `${path}.output_tokens`).toBe(nonReasoning + reasoning);
      expect(total, `${path}.total_tokens`).toBe(input + output + unclassified);
      if (record.accounting_quality === 'complete') {
        expect(unclassified, `${path}.unclassified_tokens`).toBe(0);
      }
    });
  });

  it('excludes unclassified tokens from generated costs', () => {
    const dashboard = getDemoDashboardSummary();
    const dashboardBillableTokens = dashboard.today.input_tokens + dashboard.today.output_tokens;
    expect(dashboard.today.total_cost).toBe(
      Number(((dashboardBillableTokens / 1_000_000) * 22.9).toFixed(2))
    );

    const analytics = getDemoMonitoringAnalytics();
    const incompletePoint = (analytics.timeline ?? []).find(
      (point) => (point.unclassified_tokens ?? 0) > 0
    );
    expect(incompletePoint).toBeDefined();
    const timelineBillableTokens =
      (incompletePoint?.input_tokens ?? 0) + (incompletePoint?.output_tokens ?? 0);
    expect(incompletePoint?.cost).toBe(
      Number(((timelineBillableTokens / 1_000_000) * 18.6).toFixed(2))
    );
  });
});
