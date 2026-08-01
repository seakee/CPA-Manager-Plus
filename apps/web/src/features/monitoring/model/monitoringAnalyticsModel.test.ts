import { describe, expect, it } from 'vitest';
import {
  buildMonitoringCenterAnalyticsInclude,
  buildMonitoringFilterSelectorsScopeKey,
  buildMonitoringFilterSelectorsInclude,
  mergeMonitoringAccountOptionRows,
} from './monitoringAnalyticsModel';
import type { MonitoringAccountRow } from './types';

const accountOptionRow = ({
  id,
  filterValue,
  account = 'Shared Account',
  totalCalls = 0,
}: {
  id: string;
  filterValue: string;
  account?: string;
  totalCalls?: number;
}): MonitoringAccountRow => ({
  id,
  account,
  filterValue,
  displayAccount: account,
  accountMasked: account,
  authLabels: [],
  authIndices: [],
  sourceKeys: [],
  channels: [],
  totalCalls,
  successCalls: totalCalls,
  failureCalls: 0,
  successRate: 1,
  inputTokens: 0,
  outputTokens: 0,
  cachedTokens: 0,
  cacheReadTokens: 0,
  cacheCreationTokens: 0,
  totalTokens: 0,
  totalCost: 0,
  averageLatencyMs: null,
  lastSeenAt: 0,
  recentPattern: [],
  models: [],
});

describe('buildMonitoringCenterAnalyticsInclude', () => {
  const eventsPage = {
    limit: 500,
    before_ms: 1_800_000_000_000,
    before_id: 42,
  };

  it('loads compact account aggregates with the bounded shared event page', () => {
    expect(buildMonitoringCenterAnalyticsInclude('accounts', 'day', eventsPage)).toEqual({
      summary: true,
      summary_profile: 'compact',
      account_stats: true,
      events_page: {
        limit: 500,
        before_ms: null,
        before_id: null,
      },
      granularity: 'day',
    });
  });

  it('loads compact API key aggregates with the bounded shared event page', () => {
    expect(buildMonitoringCenterAnalyticsInclude('apiKeys', 'hour', eventsPage)).toEqual({
      summary: true,
      summary_profile: 'compact',
      api_key_stats: true,
      events_page: {
        limit: 500,
        before_ms: null,
        before_id: null,
      },
      granularity: 'hour',
    });
  });

  it('keeps both event pagination cursors only on the realtime tab', () => {
    expect(buildMonitoringCenterAnalyticsInclude('realtime', 'hour', eventsPage)).toEqual({
      summary: true,
      summary_profile: 'compact',
      events_page: eventsPage,
      granularity: 'hour',
    });
  });
});

describe('buildMonitoringFilterSelectorsInclude', () => {
  it('keeps the legacy filter-options flag alongside lightweight selectors', () => {
    expect(buildMonitoringFilterSelectorsInclude()).toEqual({
      filter_options: true,
      filter_selectors: true,
    });
  });
});

describe('buildMonitoringFilterSelectorsScopeKey', () => {
  it('keeps moving ranges stable while their end time advances', () => {
    const first = buildMonitoringFilterSelectorsScopeKey(
      'today',
      { startMs: 1_800_000_000_000, endMs: 1_800_000_100_000 },
      '',
      ''
    );
    const second = buildMonitoringFilterSelectorsScopeKey(
      'today',
      { startMs: 1_800_000_000_000, endMs: 1_800_000_200_000 },
      '',
      ''
    );

    expect(second).toBe(first);
  });

  it('keeps custom ranges tied to both explicit bounds', () => {
    const first = buildMonitoringFilterSelectorsScopeKey(
      'custom',
      { startMs: 1_800_000_000_000, endMs: 1_800_000_100_000 },
      'gpt',
      'key-a'
    );
    const second = buildMonitoringFilterSelectorsScopeKey(
      'custom',
      { startMs: 1_800_000_000_000, endMs: 1_800_000_200_000 },
      'gpt',
      'key-a'
    );

    expect(second).not.toBe(first);
  });
});

describe('mergeMonitoringAccountOptionRows', () => {
  it('keeps distinct identity filters and removes only the redundant plain selector', () => {
    const rows = mergeMonitoringAccountOptionRows(
      [
        accountOptionRow({ id: 'auth-a-current', filterValue: 'auth:auth-a', totalCalls: 2 }),
        accountOptionRow({ id: 'auth-b-current', filterValue: 'auth:auth-b', totalCalls: 1 }),
      ],
      [
        accountOptionRow({ id: 'auth-a-selector', filterValue: 'auth:auth-a' }),
        accountOptionRow({ id: 'source-selector', filterValue: 'source:source-a' }),
        accountOptionRow({
          id: 'selector:shared account',
          filterValue: 'account:Shared%20Account',
        }),
        accountOptionRow({
          id: 'selector:other account',
          filterValue: 'account:Other%20Account',
          account: 'Other Account',
        }),
      ]
    );

    expect(rows.map((row) => row.filterValue)).toEqual([
      'auth:auth-a',
      'auth:auth-b',
      'source:source-a',
      'account:Other%20Account',
    ]);
    expect(rows[0]?.totalCalls).toBe(2);
  });
});
