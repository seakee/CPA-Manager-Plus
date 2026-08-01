import { describe, expect, it } from 'vitest';
import {
  buildAccountRows,
  buildApiKeyRows,
  buildApiKeyDisplayMap,
  buildMonitoringEventsScopeKey,
  buildMonitoringAuthMetaMap,
  buildMonitoringSummary,
  buildRangeFilteredRows,
  buildScopeFilteredRows,
  mergeMonitoringEventsPageItems,
  resolveMonitoringDisplayEventItems,
  resolveMonitoringPresentationSnapshot,
  withoutMonitoringSnapshotEvents,
  type MonitoringEventRow,
  type MonitoringPresentationSnapshot,
} from './useMonitoringData';
import {
  buildAccountRowsFromAnalytics,
  buildApiKeyRowsFromAnalytics,
} from '../model/analyticsAdapters';
import type { MonitoringAnalyticsEventRow } from '@/services/api/usageService';
import { buildSourceInfoMap } from '@/utils/sourceResolver';
import { sha256Hex } from '@/utils/apiKeyHash';
import type { AuthFileItem } from '@/types';

const createMonitoringEventRow = (
  overrides: Partial<MonitoringEventRow> = {}
): MonitoringEventRow => ({
  id: overrides.id ?? 'row-1',
  timestamp: overrides.timestamp ?? '2026-05-09T01:12:43.000Z',
  timestampMs: overrides.timestampMs ?? Date.parse('2026-05-09T01:12:43.000Z'),
  dayKey: overrides.dayKey ?? '2026-05-09',
  hourLabel: overrides.hourLabel ?? '01:00',
  model: overrides.model ?? 'gpt-4.1',
  resolvedModel: overrides.resolvedModel,
  endpoint: overrides.endpoint ?? '/v1/chat/completions',
  endpointMethod: overrides.endpointMethod ?? 'POST',
  endpointPath: overrides.endpointPath ?? '/v1/chat/completions',
  sourceKey: overrides.sourceKey ?? 'source:alpha',
  source: overrides.source ?? 'alpha.json',
  sourceMasked: overrides.sourceMasked ?? 'a***',
  account: overrides.account ?? 'amount-myth-resend@duck.com',
  accountMasked: overrides.accountMasked ?? 'amo***@duck.com',
  authIndex: overrides.authIndex ?? 'auth-123456',
  authIndexMasked: overrides.authIndexMasked ?? 'auth...3456',
  authLabel: overrides.authLabel ?? 'alpha.json',
  projectId: overrides.projectId ?? 'project-alpha',
  apiKeyHash: overrides.apiKeyHash ?? 'api-key-hash',
  apiKeyLabel: overrides.apiKeyLabel ?? 'ak********sh',
  apiKeyMasked: overrides.apiKeyMasked ?? 'ak********sh',
  provider: overrides.provider ?? 'codex',
  planType: overrides.planType ?? 'pro',
  channel: overrides.channel ?? 'codex',
  channelHost: overrides.channelHost ?? 'example.com',
  channelDisabled: overrides.channelDisabled ?? false,
  failed: overrides.failed ?? false,
  statsIncluded: overrides.statsIncluded ?? true,
  latencyMs: overrides.latencyMs ?? 1200,
  ttftMs: overrides.ttftMs ?? 200,
  tokensPerSecond: overrides.tokensPerSecond ?? 5,
  inputTokens: overrides.inputTokens ?? 10,
  outputTokens: overrides.outputTokens ?? 5,
  reasoningTokens: overrides.reasoningTokens ?? 0,
  cachedTokens: overrides.cachedTokens ?? 3,
  cacheReadTokens: overrides.cacheReadTokens ?? 0,
  cacheCreationTokens: overrides.cacheCreationTokens ?? 0,
  totalTokens: overrides.totalTokens ?? 18,
  totalCost: overrides.totalCost ?? 0.12,
  taskKey: overrides.taskKey ?? 'task-1',
  searchText: overrides.searchText ?? 'amount myth resend',
});

const createPresentationSnapshot = (id: string): MonitoringPresentationSnapshot => {
  const row = createMonitoringEventRow({ id });
  return {
    summary: buildMonitoringSummary([row]),
    timeline: [{ label: id, requests: 1, tokens: row.totalTokens, cost: row.totalCost }],
    timelineGranularity: 'hour',
    hourlyDistribution: [],
    modelShareRows: [],
    channelRows: [],
    modelRows: [],
    failureSourceRows: [],
    taskBuckets: [],
    recentFailures: [],
    accountRows: buildAccountRows([row]),
    apiKeyRows: buildApiKeyRows([row]),
    filterOptions: {
      accountRows: buildAccountRows([row]),
      apiKeyRows: buildApiKeyRows([row]),
      providers: [row.provider],
      models: [row.model],
      channels: [row.channel],
      headerTraceIds: [],
    },
    filteredRows: [row],
    eventsHasMore: id.includes('more'),
    eventsLoadingMore: false,
    eventsRetentionLimited: false,
    eventsTotalCount: 1,
    eventsLoadedCount: 1,
    lastRefreshedAt: new Date(1_768_759_000_000),
  };
};

describe('analytics aggregate row adapters', () => {
  const authMetaMap = buildMonitoringAuthMetaMap([
    {
      name: 'team.json',
      provider: 'codex',
      authIndex: 'auth-123456',
      path: '/tmp/auths/team.json',
      account: 'team@example.com',
      label: 'Team Account',
    },
  ]);
  const authFileMap = new Map();
  const sourceInfoMap = buildSourceInfoMap({});
  const channelByAuthIndex = new Map([
    [
      'auth-123456',
      {
        key: 'codex',
        name: 'Codex',
        baseUrl: 'https://api.openai.com',
        host: 'api.openai.com',
        disabled: false,
        authIndices: ['auth-123456'],
        modelNames: ['gpt-4.1'],
      },
    ],
  ]);

  it('builds account rows from full backend aggregates instead of event pages', () => {
    const rows = buildAccountRowsFromAnalytics(
      [
        {
          id: 'team@example.com',
          account_snapshot: 'team@example.com',
          auth_label_snapshot: 'Team Account',
          auth_provider_snapshot: 'codex',
          auth_indices: ['auth-123456'],
          sources: ['team.json'],
          source_hashes: ['source-hash'],
          calls: 3,
          success_calls: 2,
          failure_calls: 1,
          success_rate: 2 / 3,
          input_tokens: 31,
          output_tokens: 12,
          cached_tokens: 0,
          cache_read_tokens: 0,
          cache_creation_tokens: 0,
          total_tokens: 43,
          cost: 0.42,
          average_latency_ms: 1200,
          last_seen_ms: 1_768_759_000_000,
          models: [
            {
              model: 'gpt-4.1',
              calls: 3,
              success_calls: 2,
              failure_calls: 1,
              success_rate: 2 / 3,
              input_tokens: 31,
              output_tokens: 12,
              cached_tokens: 0,
              cache_read_tokens: 0,
              cache_creation_tokens: 0,
              total_tokens: 43,
              cost: 0.42,
              last_seen_ms: 1_768_759_000_000,
            },
          ],
        },
      ],
      authMetaMap,
      authFileMap,
      sourceInfoMap,
      channelByAuthIndex
    );

    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      account: 'team@example.com',
      totalCalls: 3,
      failureCalls: 1,
      totalTokens: 43,
      totalCost: 0.42,
    });
    expect(rows[0].models[0]).toMatchObject({ model: 'gpt-4.1', totalCalls: 3 });
  });

  it('builds api key rows from full backend aggregates and keeps aliases', () => {
    const rows = buildApiKeyRowsFromAnalytics(
      [
        {
          id: 'client-key-hash',
          api_key_hash: 'client-key-hash',
          account_snapshot: 'team@example.com',
          auth_label_snapshot: 'Team Account',
          auth_provider_snapshot: 'codex',
          auth_indices: ['auth-123456'],
          sources: ['team.json'],
          source_hashes: ['source-hash'],
          calls: 3,
          success_calls: 2,
          failure_calls: 1,
          success_rate: 2 / 3,
          input_tokens: 31,
          output_tokens: 12,
          cached_tokens: 0,
          cache_read_tokens: 0,
          cache_creation_tokens: 0,
          total_tokens: 43,
          cost: 0.42,
          average_latency_ms: 1200,
          last_seen_ms: 1_768_759_000_000,
          models: [],
        },
      ],
      authMetaMap,
      authFileMap,
      sourceInfoMap,
      channelByAuthIndex,
      new Map([
        [
          'client-key-hash',
          { label: 'Team Key', masked: 'sk********ey', copyValue: 'sk-client-key-original' },
        ],
      ])
    );

    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      apiKeyHash: 'client-key-hash',
      apiKeyLabel: 'Team Key',
      apiKeyMasked: 'sk********ey',
      apiKeyCopyValue: 'sk-client-key-original',
      totalCalls: 3,
      failureCalls: 1,
      totalTokens: 43,
    });
  });
});

describe('buildAccountRows', () => {
  it('keeps raw auth indices for account-level auth file linking', () => {
    const rows = buildAccountRows([
      createMonitoringEventRow(),
      createMonitoringEventRow({
        id: 'row-2',
        timestampMs: Date.parse('2026-05-09T02:12:43.000Z'),
        authIndex: 'auth-999999',
        authIndexMasked: 'auth...9999',
        sourceKey: 'source:beta',
      }),
    ]);

    expect(rows).toHaveLength(1);
    expect(rows[0].authIndices).toEqual(['auth-123456', 'auth-999999']);
    expect(rows[0].sourceKeys).toEqual(['source:alpha', 'source:beta']);
  });
});

describe('buildMonitoringAuthMetaMap', () => {
  it('maps legacy auth indices to current auth metadata', () => {
    const authFiles: AuthFileItem[] = [
      {
        name: 'alice.json',
        provider: 'codex',
        authIndex: 'current-auth-index',
        path: '/tmp/auths/alice.json',
        account: 'alice@example.com',
      },
    ];

    const map = buildMonitoringAuthMetaMap(authFiles);

    expect(map.get('current-auth-index')?.account).toBe('alice@example.com');
    expect(map.get('6bf749cb7db0e15c')?.account).toBe('alice@example.com');
  });
});

describe('buildApiKeyDisplayMap', () => {
  it('prefers stored aliases while preserving masked configured keys', () => {
    const apiKey = 'sk-alias-test-key';
    const apiKeyHash = sha256Hex(apiKey);
    const map = buildApiKeyDisplayMap([apiKey], [{ apiKeyHash, alias: 'Team A', updatedAtMs: 1 }]);

    expect(map.get(apiKeyHash)?.label).toBe('Team A');
    expect(map.get(apiKeyHash)?.masked).toMatch(/^sk/);
    expect(map.get(apiKeyHash)?.copyValue).toBe(apiKey);
  });

  it('masks key-like aliases before display', () => {
    const apiKey = 'sk-alias-test-key';
    const apiKeyHash = sha256Hex(apiKey);
    const map = buildApiKeyDisplayMap(
      [apiKey],
      [{ apiKeyHash, alias: 'sk-proj-secret-value-123456', updatedAtMs: 1 }]
    );

    expect(map.get(apiKeyHash)?.label).toMatch(/^sk/);
    expect(map.get(apiKeyHash)?.label).toContain('**');
    expect(map.get(apiKeyHash)?.label).not.toContain('secret-value');
  });
});

describe('buildApiKeyRows', () => {
  it('groups usage by client api key and aggregates model spend', () => {
    const rows = buildApiKeyRows(
      [
        createMonitoringEventRow({
          id: 'row-1',
          apiKeyHash: 'hash-a',
          apiKeyLabel: 'Team A',
          apiKeyMasked: 'sk********aa',
          model: 'gpt-4.1',
          totalTokens: 18,
          totalCost: 0.12,
        }),
        createMonitoringEventRow({
          id: 'row-2',
          apiKeyHash: 'hash-a',
          apiKeyLabel: 'Team A',
          apiKeyMasked: 'sk********aa',
          model: 'gpt-4.1',
          failed: true,
          totalTokens: 7,
          totalCost: 0.03,
        }),
      ],
      new Map([
        ['hash-a', { label: 'Team A', masked: 'sk********aa', copyValue: 'sk-original-aa' }],
      ])
    );

    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      apiKeyHash: 'hash-a',
      apiKeyLabel: 'Team A',
      apiKeyCopyValue: 'sk-original-aa',
      totalCalls: 2,
      successCalls: 1,
      failureCalls: 1,
      totalTokens: 25,
      totalCost: 0.15,
    });
    expect(rows[0].models[0]).toMatchObject({
      model: 'gpt-4.1',
      totalCalls: 2,
      failureCalls: 1,
    });
  });

  it('keeps unknown client keys separated by source and channel', () => {
    const rows = buildApiKeyRows([
      createMonitoringEventRow({
        id: 'unknown-1',
        apiKeyHash: '',
        apiKeyLabel: '-',
        apiKeyMasked: '-',
        sourceKey: 'source:a',
        authIndex: 'auth-a',
        channel: 'channel-a',
      }),
      createMonitoringEventRow({
        id: 'unknown-2',
        apiKeyHash: '',
        apiKeyLabel: '-',
        apiKeyMasked: '-',
        sourceKey: 'source:b',
        authIndex: 'auth-b',
        channel: 'channel-b',
      }),
    ]);

    expect(rows).toHaveLength(2);
    expect(rows.every((row) => row.isUnknown)).toBe(true);
  });
});

describe('buildRangeFilteredRows', () => {
  it('matches a raw api key search through its hash without breaking text search', () => {
    const rows = [
      createMonitoringEventRow({
        id: 'hash-a',
        apiKeyHash: 'hash-a',
        searchText: 'hash-a team a',
      }),
      createMonitoringEventRow({
        id: 'hash-b',
        apiKeyHash: 'hash-b',
        searchText: 'hash-b team b',
      }),
    ];

    expect(buildRangeFilteredRows(rows, 'all', null, 'team b', '').map((row) => row.id)).toEqual([
      'hash-b',
    ]);
    expect(
      buildRangeFilteredRows(rows, 'all', null, 'unmatched raw key', 'hash-a').map((row) => row.id)
    ).toEqual(['hash-a']);
  });
});

describe('buildScopeFilteredRows', () => {
  it('applies failed-only status filtering to realtime rows locally', () => {
    const rows = [
      createMonitoringEventRow({ id: 'success-row', failed: false }),
      createMonitoringEventRow({ id: 'failed-row', failed: true }),
    ];

    expect(buildScopeFilteredRows(rows, { status: 'failed' }).map((row) => row.id)).toEqual([
      'failed-row',
    ]);
  });

  it('matches account focus against account and fallback display fields', () => {
    const rows = [
      createMonitoringEventRow({
        id: 'alice-row',
        account: 'alice@example.com',
        authLabel: 'Alice Auth',
      }),
      createMonitoringEventRow({
        id: 'legacy-row',
        account: '',
        authLabel: 'Legacy Auth',
        source: 'legacy-source',
      }),
      createMonitoringEventRow({
        id: 'bob-row',
        account: 'bob@example.com',
        authLabel: 'Bob Auth',
      }),
    ];

    expect(
      buildScopeFilteredRows(rows, { account: 'alice@example.com' }).map((row) => row.id)
    ).toEqual(['alice-row']);
    expect(buildScopeFilteredRows(rows, { account: 'Legacy Auth' }).map((row) => row.id)).toEqual([
      'legacy-row',
    ]);
  });

  it('applies latency and cache drilldown filters to realtime rows locally', () => {
    const rows = [
      createMonitoringEventRow({
        id: 'fast-cache-hit',
        latencyMs: 2_000,
        cachedTokens: 5,
      }),
      createMonitoringEventRow({
        id: 'slow-cache-hit',
        latencyMs: 12_000,
        cacheReadTokens: 3,
      }),
      createMonitoringEventRow({
        id: 'slow-cache-miss',
        latencyMs: 15_000,
        cachedTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
      }),
      createMonitoringEventRow({
        id: 'unknown-latency',
        latencyMs: null,
      }),
    ];

    expect(buildScopeFilteredRows(rows, { minLatencyMs: 10_000 }).map((row) => row.id)).toEqual([
      'slow-cache-hit',
      'slow-cache-miss',
    ]);
    expect(buildScopeFilteredRows(rows, { cacheStatus: 'hit' }).map((row) => row.id)).toEqual([
      'fast-cache-hit',
      'slow-cache-hit',
      'unknown-latency',
    ]);
    expect(
      buildScopeFilteredRows(rows, { minLatencyMs: 10_000, cacheStatus: 'miss' }).map(
        (row) => row.id
      )
    ).toEqual(['slow-cache-miss']);
  });

  it('clamps local summary cache rates and respects resolved GPT-5.6 aliases', () => {
    expect(
      buildMonitoringSummary([
        createMonitoringEventRow({ inputTokens: 10, cachedTokens: 100 }),
      ]).cacheHitRate
    ).toBe(1);

    expect(
      buildMonitoringSummary([
        createMonitoringEventRow({
          model: 'internal-fast',
          resolvedModel: 'openai/gpt-5.6-sol',
          inputTokens: 152_600,
          cachedTokens: 0,
          cacheReadTokens: 151_000,
          cacheCreationTokens: 1_000,
        }),
      ]).cacheHitRate
    ).toBeCloseTo(151_000 / 152_600, 6);
  });
});

describe('buildMonitoringEventsScopeKey', () => {
  it('keeps moving ranges stable when only the end time changes', () => {
    const first = buildMonitoringEventsScopeKey(
      'today',
      { startMs: 1_768_755_200_000, endMs: 1_768_759_000_000 },
      '',
      '',
      {},
      'hour'
    );
    const second = buildMonitoringEventsScopeKey(
      'today',
      { startMs: 1_768_755_200_000, endMs: 1_768_759_005_000 },
      '',
      '',
      {},
      'hour'
    );

    expect(second).toBe(first);
  });

  it('keeps custom ranges tied to the explicit end time', () => {
    const first = buildMonitoringEventsScopeKey(
      'custom',
      { startMs: 1_768_755_200_000, endMs: 1_768_759_000_000 },
      '',
      '',
      {},
      'hour'
    );
    const second = buildMonitoringEventsScopeKey(
      'custom',
      { startMs: 1_768_755_200_000, endMs: 1_768_759_005_000 },
      '',
      '',
      {},
      'hour'
    );

    expect(second).not.toBe(first);
  });
});

describe('mergeMonitoringEventsPageItems', () => {
  const createAnalyticsEvent = (
    eventHash: string,
    timestampMs: number
  ): MonitoringAnalyticsEventRow => ({
    event_hash: eventHash,
    timestamp_ms: timestampMs,
    model: 'gpt-4.1',
    endpoint: 'POST /v1/chat/completions',
    method: 'POST',
    path: '/v1/chat/completions',
    auth_index: 'auth-1',
    source: 'source-1',
    source_hash: 'source-hash-1',
    api_key_hash: 'api-key-hash-1',
    account_snapshot: 'alice@example.com',
    auth_label_snapshot: 'alice.json',
    auth_provider_snapshot: 'codex',
    input_tokens: 10,
    output_tokens: 5,
    cached_tokens: 0,
    cache_read_tokens: 0,
    cache_creation_tokens: 0,
    reasoning_tokens: 0,
    total_tokens: 15,
    latency_ms: 1200,
    ttft_ms: 200,
    failed: false,
  });

  it('merges root refresh results without dropping previously rendered events', () => {
    const previous = [
      createAnalyticsEvent('event-2', 1_768_759_001_000),
      createAnalyticsEvent('event-1', 1_768_759_000_000),
    ];
    const nextPage = [
      createAnalyticsEvent('event-3', 1_768_759_002_000),
      createAnalyticsEvent('event-2', 1_768_759_001_000),
    ];

    expect(
      mergeMonitoringEventsPageItems(previous, nextPage, null).map((item) => item.event_hash)
    ).toEqual(['event-3', 'event-2', 'event-1']);
  });

  it('keeps only the newest 2000 deduplicated events', () => {
    const previous = Array.from({ length: 2_000 }, (_, index) =>
      createAnalyticsEvent(`old-${index}`, 10_000 - index)
    );
    const nextPage = Array.from({ length: 500 }, (_, index) =>
      createAnalyticsEvent(`new-${index}`, 20_000 - index)
    );

    const merged = mergeMonitoringEventsPageItems(previous, nextPage, null);
    expect(merged).toHaveLength(2_000);
    expect(merged[0]?.event_hash).toBe('new-0');
    expect(merged[499]?.event_hash).toBe('new-499');
    expect(merged[merged.length - 1]?.event_hash).toBe('old-1499');
  });
});

describe('withoutMonitoringSnapshotEvents', () => {
  it('keeps aggregate presentation data without retaining event rows', () => {
    const snapshot = createPresentationSnapshot('cached');
    const cached = withoutMonitoringSnapshotEvents(snapshot);

    expect(cached.summary).toBe(snapshot.summary);
    expect(cached.filteredRows).toEqual([]);
    expect(cached.eventsLoadedCount).toBe(0);
    expect(cached.eventsTotalCount).toBe(0);
    expect(cached.eventsHasMore).toBe(false);
  });
});

describe('resolveMonitoringDisplayEventItems', () => {
  const createAnalyticsEvent = (
    eventHash: string,
    timestampMs: number
  ): MonitoringAnalyticsEventRow => ({
    event_hash: eventHash,
    timestamp_ms: timestampMs,
    model: 'gpt-4.1',
    endpoint: 'POST /v1/chat/completions',
    method: 'POST',
    path: '/v1/chat/completions',
    auth_index: 'auth-1',
    source: 'source-1',
    source_hash: 'source-hash-1',
    api_key_hash: 'api-key-hash-1',
    account_snapshot: 'alice@example.com',
    auth_label_snapshot: 'alice.json',
    auth_provider_snapshot: 'codex',
    input_tokens: 10,
    output_tokens: 5,
    cached_tokens: 0,
    cache_read_tokens: 0,
    cache_creation_tokens: 0,
    reasoning_tokens: 0,
    total_tokens: 15,
    latency_ms: 1200,
    ttft_ms: 200,
    failed: false,
  });

  it('reuses persisted page items while the analytics response has no new event page', () => {
    const eventsPageItems = [createAnalyticsEvent('event-1', 1_768_759_000_000)];

    const first = resolveMonitoringDisplayEventItems({
      analyticsData: null,
      currentPageItems: null,
      eventsPageItems,
      eventsBeforeMs: null,
      dataStale: false,
    });
    const second = resolveMonitoringDisplayEventItems({
      analyticsData: null,
      currentPageItems: null,
      eventsPageItems,
      eventsBeforeMs: null,
      dataStale: false,
    });

    expect(first).toBe(eventsPageItems);
    expect(second).toBe(eventsPageItems);
  });

  it('keeps stale transitions on existing page items without creating a new array', () => {
    const eventsPageItems = [createAnalyticsEvent('event-1', 1_768_759_000_000)];
    const analyticsItems = [createAnalyticsEvent('event-2', 1_768_759_001_000)];

    expect(
      resolveMonitoringDisplayEventItems({
        analyticsData: { events: { items: analyticsItems } },
        currentPageItems: analyticsItems,
        eventsPageItems,
        eventsBeforeMs: null,
        dataStale: true,
      })
    ).toBe(eventsPageItems);
  });

  it('reuses persisted page items after the same analytics page has been absorbed', () => {
    const analyticsItems = [
      createAnalyticsEvent('event-2', 1_768_759_001_000),
      createAnalyticsEvent('event-1', 1_768_759_000_000),
    ];
    const eventsPageItems = [
      createAnalyticsEvent('event-2', 1_768_759_001_000),
      createAnalyticsEvent('event-1', 1_768_759_000_000),
    ];

    expect(
      resolveMonitoringDisplayEventItems({
        analyticsData: { events: { items: analyticsItems } },
        currentPageItems: analyticsItems,
        eventsPageItems,
        eventsBeforeMs: null,
        dataStale: false,
      })
    ).toBe(eventsPageItems);
  });
});

describe('resolveMonitoringPresentationSnapshot', () => {
  it('keeps the last stable presentation while a new uncached scope is loading', () => {
    const computed = createPresentationSnapshot('computed-empty-transition');
    const stable = createPresentationSnapshot('stable-all');
    const result = resolveMonitoringPresentationSnapshot({
      computedSnapshot: computed,
      scopeKey: 'failed',
      dataStale: true,
      cachedSnapshots: new Map(),
      lastStableSnapshot: stable,
    });

    expect(result.snapshot).toBe(stable);
    expect(result.hasPresentationSnapshot).toBe(true);
    expect(result.usingSnapshotFallback).toBe(true);
  });

  it('prefers a cached target scope presentation over the last stable scope', () => {
    const computed = createPresentationSnapshot('computed-transition');
    const stable = createPresentationSnapshot('stable-all');
    const cachedFailed = createPresentationSnapshot('cached-failed');
    const result = resolveMonitoringPresentationSnapshot({
      computedSnapshot: computed,
      scopeKey: 'failed',
      dataStale: true,
      cachedSnapshots: new Map([['failed', cachedFailed]]),
      lastStableSnapshot: stable,
    });

    expect(result.snapshot).toBe(cachedFailed);
    expect(result.hasPresentationSnapshot).toBe(true);
    expect(result.usingSnapshotFallback).toBe(true);
  });

  it('returns the computed presentation for fresh data', () => {
    const computed = createPresentationSnapshot('computed-fresh');
    const stable = createPresentationSnapshot('stable-all');
    const result = resolveMonitoringPresentationSnapshot({
      computedSnapshot: computed,
      scopeKey: 'all',
      dataStale: false,
      cachedSnapshots: new Map([['all', stable]]),
      lastStableSnapshot: stable,
    });

    expect(result.snapshot).toBe(computed);
    expect(result.hasPresentationSnapshot).toBe(true);
    expect(result.usingSnapshotFallback).toBe(false);
  });
});
