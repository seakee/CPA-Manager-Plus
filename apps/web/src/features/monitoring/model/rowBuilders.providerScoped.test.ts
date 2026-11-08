import { describe, expect, it } from 'vitest';
import {
  buildAccountRows,
  buildScopeFilteredRows,
} from './rowBuilders';
import type { MonitoringEventRow } from './types';

const eventRow = (overrides: Partial<MonitoringEventRow> = {}): MonitoringEventRow =>
  ({
    id: 'event-1',
    timestamp: '2026-08-24T00:00:00.000Z',
    timestampMs: Date.parse('2026-08-24T00:00:00.000Z'),
    dayKey: '2026-08-24',
    hourLabel: '00:00',
    model: 'model-x',
    endpoint: 'POST /v1/responses',
    endpointMethod: 'POST',
    endpointPath: '/v1/responses',
    sourceKey: 'source:key',
    source: 'source.json',
    sourceIdentity: 'source.json',
    sourceMasked: 'source.json',
    account: 'same@example.com',
    accountIdentity: 'same@example.com',
    accountMasked: 'sam***@example.com',
    authIndex: '-',
    authIndexIdentity: '',
    authIndexMasked: '-',
    authLabel: 'Same Account',
    authLabelIdentity: 'Same Account',
    projectId: '-',
    apiKeyHash: '',
    apiKeyLabel: '-',
    apiKeyMasked: '-',
    provider: 'codex',
    providerIdentity: 'codex',
    planType: '-',
    channel: 'codex',
    channelHost: '-',
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
    taskKey: 'task-1',
    searchText: 'same@example.com',
    ...overrides,
  }) as MonitoringEventRow;

describe('provider-scoped monitoring account fallback filters', () => {
  it('keeps account-provider local filtering isolated by persisted provider identity', () => {
    const codex = eventRow({
      id: 'codex-event',
      provider: 'codex',
      providerIdentity: 'codex',
    });
    const antigravity = eventRow({
      id: 'antigravity-event',
      provider: 'antigravity',
      providerIdentity: 'antigravity',
      channel: 'antigravity',
    });

    const filtered = buildScopeFilteredRows([codex, antigravity], {
      account: 'account-provider:codex|same%40example.com',
    });

    expect(filtered.map((row) => row.id)).toEqual(['codex-event']);
  });

  it('uses persisted label identity for fallback filterValue instead of enriched display account', () => {
    const [row] = buildAccountRows([
      eventRow({
        account: 'metadata@example.com',
        accountIdentity: '',
        authLabel: 'Shared Label',
        authLabelIdentity: 'Shared Label',
        source: '-',
        sourceIdentity: '',
        authIndex: '-',
        authIndexIdentity: '',
        apiKeyHash: '',
        provider: 'codex',
        providerIdentity: 'codex',
      }),
    ]);

    expect(row.filterValue).toBe('account-provider:codex|Shared%20Label');

    const filtered = buildScopeFilteredRows(
      [
        eventRow({
          id: 'persisted-label-event',
          account: 'metadata@example.com',
          accountIdentity: '',
          authLabel: 'Shared Label',
          authLabelIdentity: 'Shared Label',
          source: '-',
          sourceIdentity: '',
          authIndex: '-',
          authIndexIdentity: '',
          apiKeyHash: '',
          provider: 'codex',
          providerIdentity: 'codex',
        }),
      ],
      { account: row.filterValue }
    );

    expect(filtered.map((item) => item.id)).toEqual(['persisted-label-event']);
  });
});

describe('account average output TPS', () => {
  it('averages per-request rates instead of dividing token and duration totals', () => {
    const [row] = buildAccountRows([
      eventRow({ outputTokens: 100, latencyMs: 1000, ttftMs: 900 }),
      eventRow({ outputTokens: 100, latencyMs: 10000, tokensPerSecond: 999 }),
    ]);
    expect(row.averageOutputTps).toBe(55);
    expect(row.models[0].averageOutputTps).toBe(55);
  });

  it.each([
    { failed: true },
    { outputTokens: 0 },
    { outputTokens: -1 },
    { outputTokens: Infinity },
    { outputTokens: NaN },
    { latencyMs: 0 },
    { latencyMs: null },
    { latencyMs: undefined },
    { latencyMs: -100 },
    { latencyMs: Infinity },
    { latencyMs: NaN },
  ])('excludes invalid samples %j', (invalid) => {
    const sample = eventRow({ outputTokens: 100, latencyMs: 1000, ...invalid });
    const [empty] = buildAccountRows([sample]);
    expect(empty.averageOutputTps).toBeNull();
    expect(empty.models[0].averageOutputTps).toBeNull();
    const [mixed] = buildAccountRows([sample, eventRow({ outputTokens: 100, latencyMs: 10000 })]);
    expect(mixed.averageOutputTps).toBe(10);
  });

  it('weights different models by valid sample counts and respects model filtering', () => {
    const samples = [
      eventRow({ model: 'fast', outputTokens: 100, latencyMs: 1000 }),
      eventRow({ model: 'fast', outputTokens: 100, latencyMs: 1000 }),
      eventRow({ model: 'slow', outputTokens: 100, latencyMs: 10000 }),
    ];
    const [row] = buildAccountRows(samples);
    expect(row.averageOutputTps).toBe(70);
    expect(row.models.map((model) => model.averageOutputTps)).toEqual([100, 10]);
    expect(buildAccountRows(buildScopeFilteredRows(samples, { model: 'slow' }))[0].averageOutputTps).toBe(10);
  });
});
