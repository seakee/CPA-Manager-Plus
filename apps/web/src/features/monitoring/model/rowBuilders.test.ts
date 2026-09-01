import { describe, expect, it, vi, afterEach } from 'vitest';
import { buildRangeFilteredRows } from './rowBuilders';
import type { MonitoringEventRow } from './types';

const buildRow = (timestampMs: number): MonitoringEventRow => ({
  id: String(timestampMs),
  timestamp: new Date(timestampMs).toISOString(),
  timestampMs,
  dayKey: '2026-08-27',
  hourLabel: '00:00',
  model: 'gpt-4o',
  endpoint: 'POST /v1/chat/completions',
  endpointMethod: 'POST',
  endpointPath: '/v1/chat/completions',
  sourceKey: 'k',
  source: 's',
  sourceMasked: 's',
  account: 'a',
  accountMasked: 'a',
  authIndex: '1',
  authIndexMasked: '1',
  authLabel: 'l',
  projectId: 'p',
  apiKeyHash: 'h',
  apiKeyLabel: 'h',
  apiKeyMasked: 'h',
  provider: 'openai',
  planType: '-',
  channel: 'c',
  channelHost: 'h',
  channelDisabled: false,
  failed: false,
  statsIncluded: true,
  latencyMs: 100,
  ttftMs: null,
  tokensPerSecond: null,
  inputTokens: 1,
  outputTokens: 1,
  reasoningTokens: 0,
  cachedTokens: 0,
  cacheReadTokens: 0,
  cacheCreationTokens: 0,
  totalTokens: 2,
  totalCost: 0,
  taskKey: 'k',
  searchText: '',
});

afterEach(() => {
  vi.useRealTimers();
});

describe('buildRangeFilteredRows (yesterday half-open bounds)', () => {
  it('excludes a row whose timestamp is exactly at today local midnight', () => {
    const fakeNow = new Date(2026, 7, 28, 15, 30, 0, 0).getTime();
    vi.useFakeTimers();
    vi.setSystemTime(new Date(fakeNow));

    const todayStart = new Date(2026, 7, 28, 0, 0, 0, 0).getTime();
    const beforeYesterday = new Date(2026, 7, 27, 0, 0, 0, 0).getTime() - 1;
    const insideYesterday = new Date(2026, 7, 27, 12, 0, 0, 0).getTime();

    const rows = [buildRow(beforeYesterday), buildRow(insideYesterday), buildRow(todayStart)];

    const filtered = buildRangeFilteredRows(rows, 'yesterday', null, '');

    expect(filtered.map((r) => r.timestampMs)).toEqual([insideYesterday]);
  });

  it('keeps today, 7d, 14d, 30d, all using closed-now upper bound', () => {
    const fakeNow = new Date(2026, 7, 28, 15, 30, 0, 0).getTime();
    vi.useFakeTimers();
    vi.setSystemTime(new Date(fakeNow));

    const insideToday = new Date(2026, 7, 28, 12, 0, 0, 0).getTime();
    const row = buildRow(insideToday);

    for (const range of ['today', '7d', '14d', '30d', 'all'] as const) {
      expect(
        buildRangeFilteredRows([row], range, null, ''),
        `${range} should include a row inside the range`
      ).toHaveLength(1);
    }
  });
});
