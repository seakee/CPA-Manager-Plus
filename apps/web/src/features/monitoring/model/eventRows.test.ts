import { describe, expect, it } from 'vitest';
import { buildRealtimeSourceDisplay } from '@/features/monitoring/realtimeSourceDisplay';
import type { ModelPrice, UsageDetailWithEndpoint } from '@/utils/usage';
import { buildSourceInfoMap } from '@/utils/sourceResolver';
import { buildEventRows } from './eventRows';

const buildRows = (
  overrides: Partial<UsageDetailWithEndpoint> = {},
  modelPrices: Record<string, ModelPrice> = {}
) =>
  buildEventRows(
    [
      {
        timestamp: '2026-05-19T10:00:00Z',
        source: 'alice@example.com',
        auth_index: 'auth-1',
        latency_ms: 1500,
        ttft_ms: 500,
        tokens: {
          input_tokens: 10,
          output_tokens: 20,
          total_tokens: 30,
        },
        failed: false,
        __modelName: 'gpt-5.4',
        __endpoint: 'POST /v1/chat/completions',
        __endpointMethod: 'POST',
        __endpointPath: '/v1/chat/completions',
        __timestampMs: Date.parse('2026-05-19T10:00:00Z'),
        ...overrides,
      },
    ],
    new Map(),
    new Map(),
    { byAuthIndex: new Map(), bySource: new Map(), byIdentityKey: new Map() },
    new Map(),
    modelPrices,
    new Map()
  );

describe('buildEventRows', () => {
  it('calculates tokens per second from total latency', () => {
    const [row] = buildRows();

    expect(row.latencyMs).toBe(1500);
    expect(row.ttftMs).toBe(500);
    expect(row.tokensPerSecond).toBeCloseTo(20 / 1.5);
  });

  it('does not use TTFT to calculate tokens per second', () => {
    const [withoutTTFT] = buildRows({ ttft_ms: undefined });
    const [smallTTFT] = buildRows({ ttft_ms: 100 });
    const [invalidTTFT] = buildRows({ ttft_ms: 2000 });

    expect(withoutTTFT.tokensPerSecond).toBeCloseTo(20 / 1.5);
    expect(smallTTFT.tokensPerSecond).toBeCloseTo(20 / 1.5);
    expect(invalidTTFT.tokensPerSecond).toBeCloseTo(20 / 1.5);
  });

  it('does not calculate TPS without output tokens or total latency', () => {
    const [noOutput] = buildRows({ tokens: { output_tokens: 0 } });
    const [noLatency] = buildRows({ latency_ms: undefined });
    const [zeroLatency] = buildRows({ latency_ms: 0 });

    expect(noOutput.tokensPerSecond).toBeNull();
    expect(noLatency.tokensPerSecond).toBeNull();
    expect(zeroLatency.tokensPerSecond).toBeNull();
  });

  it('derives missing total from normalized input without adding cache twice', () => {
    const [row] = buildRows(
      {
        tokens: {
          input_tokens: 100,
          output_tokens: 20,
          cache_read_tokens: 40,
        },
      },
      {
        'gpt-5.4': { prompt: 1, completion: 2, cache: 0.1, cacheRead: 0.1 },
      }
    );

    expect(row.totalTokens).toBe(120);
    expect(row.totalCost).toBeCloseTo(0.000104);
  });

  it('keeps CPA executor and service tier metadata searchable', () => {
    const [row] = buildRows({
      executor_type: 'codex',
      service_tier: 'priority',
      request_service_tier: 'priority',
      response_service_tier: 'default',
      reasoning_effort: 'medium',
    });

    expect(row.executorType).toBe('codex');
    expect(row.serviceTier).toBe('priority');
    expect(row.requestServiceTier).toBe('priority');
    expect(row.responseServiceTier).toBe('default');
    expect(row.searchText).toContain('codex');
    expect(row.searchText).toContain('priority');
    expect(row.searchText).toContain('default');
    expect(row.searchText).toContain('medium');
  });

  it('keeps response header diagnostics searchable', () => {
    const [row] = buildRows({
      failed: true,
      fail_status_code: 429,
      response_metadata: {
        quota: {
          plan_type: 'plus',
          used_percent: 87,
          recover_at_ms: 1780000060000,
        },
        errors: {
          kind: 'rate_limit',
          code: 'retry_after',
        },
        trace: {
          primary_trace_id: 'req-header',
        },
      },
      header_quota_recover_at_ms: 1780000060000,
      header_quota_used_percent: 87,
      header_quota_plan_type: 'plus',
      header_error_kind: 'rate_limit',
      header_error_code: 'retry_after',
      header_trace_id: 'req-header',
    });

    expect(row.responseMetadata?.quota?.plan_type).toBe('plus');
    expect(row.headerQuotaUsedPercent).toBe(87);
    expect(row.headerTraceId).toBe('req-header');
    expect(row.searchText).toContain('rate_limit');
    expect(row.searchText).toContain('retry_after');
    expect(row.searchText).toContain('req-header');
    expect(row.searchText).toContain('plus');
  });

  it('derives response header diagnostics from metadata-only usage details', () => {
    const [row] = buildRows({
      failed: true,
      fail_status_code: 429,
      response_metadata: {
        quota: {
          active_limit: 'premium',
          used_percent: 92,
          recover_at_ms: 1780000120000,
        },
        errors: {
          kind: 'rate_limit',
          ide_error_code: 'usage_limit_reached',
        },
        trace: {
          primary_trace_id: 'req-metadata-only',
        },
      },
    });

    expect(row.headerQuotaPlanType).toBe('premium');
    expect(row.headerQuotaUsedPercent).toBe(92);
    expect(row.headerQuotaRecoverAtMs).toBe(1780000120000);
    expect(row.headerErrorKind).toBe('rate_limit');
    expect(row.headerErrorCode).toBe('usage_limit_reached');
    expect(row.headerTraceId).toBe('req-metadata-only');
    expect(row.searchText).toContain('usage_limit_reached');
    expect(row.searchText).toContain('req-metadata-only');
    expect(row.searchText).toContain('premium');
  });

  it('keeps shared provider display names available to realtime source cells', () => {
    const sharedKey = 'sk-shared1234567890abcdef';
    const sourceInfoMap = buildSourceInfoMap({
      codexApiKeys: [
        {
          apiKey: sharedKey,
          prefix: 'Shared Relay',
          baseUrl: 'https://api.shared.example/v1',
        },
      ],
      claudeApiKeys: [
        {
          apiKey: sharedKey,
          prefix: 'Shared Relay',
          baseUrl: 'https://api.shared.example/v1',
        },
      ],
    });
    const [row] = buildEventRows(
      [
        {
          timestamp: '2026-05-19T10:00:00Z',
          source: 'm:sk-s...cdef',
          auth_index: null,
          auth_provider_snapshot: 'codex',
          latency_ms: 1500,
          tokens: {
            input_tokens: 10,
            output_tokens: 20,
            total_tokens: 30,
          },
          failed: false,
          __modelName: 'gpt-5.4',
          __endpoint: 'POST /v1/chat/completions',
          __endpointMethod: 'POST',
          __endpointPath: '/v1/chat/completions',
          __timestampMs: Date.parse('2026-05-19T10:00:00Z'),
        },
      ],
      new Map(),
      new Map(),
      sourceInfoMap,
      new Map(),
      {},
      new Map()
    );

    const t = ((key: string) => key) as Parameters<typeof buildRealtimeSourceDisplay>[1];
    const display = buildRealtimeSourceDisplay(row, t);

    expect(row.source).toBe('Shared Relay');
    expect(row.sourceKey).toBe('shared:m:sk-s...cdef');
    expect(row.provider).toBe('codex');
    expect(display.primary).toBe('Shared Relay');
  });

  it('prefers multi-key OpenAI-compatible disambiguation in realtime source cells', () => {
    const sourceInfoMap = buildSourceInfoMap({
      openaiCompatibility: [
        {
          name: 'kuaileshifu',
          baseUrl: 'https://api.kuaileshifu.example/v1',
          apiKeyEntries: [
            { apiKey: 'sk-openai111111aaaa', authIndex: 'kuai-auth-1' },
            { apiKey: 'sk-openai222222bbbb', authIndex: 'kuai-auth-2' },
          ],
        },
      ],
    });
    const authMetaMap = new Map([
      [
        'kuai-auth-1',
        {
          authIndex: 'kuai-auth-1',
          label: 'kuaileshifu',
          account: 'kuaileshifu',
          provider: 'openai',
          status: 'active',
          disabled: false,
          unavailable: false,
          runtimeOnly: false,
          planType: '-',
          updatedAt: '',
        },
      ],
    ]);
    const channelByAuthIndex = new Map([
      [
        'kuai-auth-1',
        {
          key: 'openai:0',
          name: 'kuaileshifu',
          baseUrl: 'https://api.kuaileshifu.example/v1',
          host: 'api.kuaileshifu.example',
          disabled: false,
          authIndices: ['kuai-auth-1', 'kuai-auth-2'],
          modelNames: [],
        },
      ],
    ]);

    const [row] = buildEventRows(
      [
        {
          timestamp: '2026-05-19T10:00:00Z',
          source: 'm:sk-o...aaaa',
          auth_index: 'kuai-auth-1',
          account_snapshot: 'kuaileshifu',
          auth_label_snapshot: 'kuaileshifu',
          auth_provider_snapshot: 'openai',
          latency_ms: 1500,
          tokens: {
            input_tokens: 10,
            output_tokens: 20,
            total_tokens: 30,
          },
          failed: false,
          __modelName: 'gpt-4.1-mini',
          __endpoint: 'POST /v1/chat/completions',
          __endpointMethod: 'POST',
          __endpointPath: '/v1/chat/completions',
          __timestampMs: Date.parse('2026-05-19T10:00:00Z'),
        },
      ],
      authMetaMap,
      new Map(),
      sourceInfoMap,
      channelByAuthIndex,
      {},
      new Map()
    );

    const t = ((key: string) => {
      if (key === 'monitoring.filter_provider') return 'Provider';
      if (key === 'monitoring.column_host') return 'Host';
      if (key === 'monitoring.source') return 'Source';
      return key;
    }) as Parameters<typeof buildRealtimeSourceDisplay>[1];
    const display = buildRealtimeSourceDisplay(row, t);

    expect(row.source).toBe('kuaileshifu #1');
    expect(row.sourceMasked).toBe('kuaileshifu #1');
    expect(row.channel).toBe('kuaileshifu');
    expect(display.primary).toBe('kuaileshifu #1');
    expect(display.meta).toBe('Provider: openai');
  });
});
