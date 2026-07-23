import { describe, expect, it } from 'vitest';
import type {
  MonitoringAnalyticsChannelShareRow,
  MonitoringAnalyticsEventRow,
} from '@/services/api/usageService';
import { buildSourceInfoMap } from '@/utils/sourceResolver';
import {
  buildAnalyticsFilters,
  buildMonitoringAccountFilterValue,
  buildChannelRowsFromAnalytics,
  buildFailureRowsFromAnalytics,
  buildFailureSourceRowsFromAnalytics,
  buildFilterOptionsFromAnalytics,
  buildUsageDetailsFromAnalyticsEvents,
} from './analyticsAdapters';

describe('buildUsageDetailsFromAnalyticsEvents', () => {
  it('maps resolved model and auth project snapshots into usage details', () => {
    const events: MonitoringAnalyticsEventRow[] = [
      {
        event_hash: 'event-1',
        timestamp_ms: Date.UTC(2026, 4, 20, 1, 2, 3),
        model: 'alias-model',
        resolved_model: 'upstream-model',
        endpoint: 'POST /v1/chat/completions',
        method: 'POST',
        path: '/v1/chat/completions',
        auth_index: 'auth-1',
        source: 'source.json',
        source_hash: 'source-hash',
        api_key_hash: 'api-key-hash',
        account_snapshot: 'account@example.com',
        auth_label_snapshot: 'label',
        auth_provider_snapshot: 'codex',
        auth_project_id_snapshot: 'project-1',
        reasoning_effort: 'medium',
        input_tokens: 10,
        output_tokens: 5,
        cached_tokens: 0,
        cache_read_tokens: 4,
        cache_creation_tokens: 1,
        reasoning_tokens: 1,
        total_tokens: 18,
        latency_ms: 123,
        ttft_ms: 45,
        failed: true,
        fail_status_code: 429,
        fail_summary: 'rate limit exceeded',
      },
    ];

    const details = buildUsageDetailsFromAnalyticsEvents(events);

    expect(details[0]).toMatchObject({
      __modelName: 'alias-model',
      __resolvedModel: 'upstream-model',
      auth_project_id_snapshot: 'project-1',
      reasoning_effort: 'medium',
      latency_ms: 123,
      ttft_ms: 45,
      tokens: {
        cached_tokens: 0,
        cache_read_tokens: 4,
        cache_creation_tokens: 1,
      },
      failed: true,
      fail_status_code: 429,
      fail_summary: 'rate limit exceeded',
    });
  });

  it('trusts backend-deduped cached tokens from analytics events', () => {
    const events: MonitoringAnalyticsEventRow[] = [
      {
        event_hash: 'event-cache',
        timestamp_ms: Date.UTC(2026, 4, 20, 1, 2, 3),
        model: 'mixed-cache-model',
        endpoint: 'POST /v1/chat/completions',
        method: 'POST',
        path: '/v1/chat/completions',
        auth_index: 'auth-1',
        source: 'source.json',
        source_hash: 'source-hash',
        api_key_hash: 'api-key-hash',
        account_snapshot: '',
        auth_label_snapshot: '',
        auth_provider_snapshot: '',
        input_tokens: 100,
        output_tokens: 20,
        cached_tokens: 5,
        cache_read_tokens: 4,
        cache_creation_tokens: 1,
        reasoning_tokens: 0,
        total_tokens: 130,
        latency_ms: null,
        failed: false,
      },
    ];

    const details = buildUsageDetailsFromAnalyticsEvents(events);

    expect(details[0].tokens.cached_tokens).toBe(5);
    expect(details[0].tokens.cache_read_tokens).toBe(4);
    expect(details[0].tokens.cache_creation_tokens).toBe(1);
  });
});

describe('buildAnalyticsFilters', () => {
  it('maps failed-only status and known accounts into backend filters', () => {
    const filters = buildAnalyticsFilters(
      {
        account: 'alice@example.com',
        status: 'failed',
      },
      new Map([
        [
          'auth-1',
          {
            authIndex: 'auth-1',
            label: 'Alice',
            account: 'alice@example.com',
            provider: 'codex',
            status: 'active',
            disabled: false,
            unavailable: false,
            runtimeOnly: false,
            planType: 'pro',
            updatedAt: '',
          },
        ],
      ]),
      []
    );

    expect(filters).toMatchObject({
      auth_indices: ['auth-1'],
      failed_only: true,
    });
    expect(filters.accounts).toBeUndefined();
  });

  it('falls back to account snapshot filters when auth metadata cannot resolve an account', () => {
    const filters = buildAnalyticsFilters(
      {
        account: 'legacy@example.com',
      },
      new Map(),
      []
    );

    expect(filters).toEqual({
      accounts: ['legacy@example.com'],
    });
  });

  it('maps account filter tokens into exact backend identity filters', () => {
    expect(
      buildAnalyticsFilters(
        {
          account: buildMonitoringAccountFilterValue({
            account: 'OpenAI Compatible',
            authIndices: ['openai-auth'],
          }),
        },
        new Map(),
        []
      )
    ).toEqual({
      auth_indices: ['openai-auth'],
    });

    expect(
      buildAnalyticsFilters(
        {
          account: buildMonitoringAccountFilterValue({
            account: 'OpenAI Compatible',
            sourceHashes: ['source-hash'],
          }),
        },
        new Map(),
        []
      )
    ).toEqual({
      source_hashes: ['source-hash'],
    });

    expect(
      buildAnalyticsFilters(
        {
          account: buildMonitoringAccountFilterValue({
            account: 'OpenAI Compatible',
            apiKeyHashes: ['API-Key-Hash'],
          }),
        },
        new Map(),
        []
      )
    ).toEqual({
      api_key_hashes: ['api-key-hash'],
    });
  });

  it('falls back to provider filters when auth metadata cannot resolve a provider', () => {
    const filters = buildAnalyticsFilters(
      {
        provider: 'legacy-provider',
      },
      new Map(),
      []
    );

    expect(filters).toEqual({
      providers: ['legacy-provider'],
    });
  });

  it('maps usage analytics drilldown dimensions into backend filters', () => {
    const filters = buildAnalyticsFilters(
      {
        authFile: 'codex-auth.json',
        projectId: 'project-1',
        requestType: 'codex',
        minLatencyMs: 10_000,
        cacheStatus: 'hit',
      },
      new Map(),
      []
    );

    expect(filters).toEqual({
      auth_files: ['codex-auth.json'],
      project_ids: ['project-1'],
      request_types: ['codex'],
      min_latency_ms: 10_000,
      cache_status: 'hit',
    });
  });
});

describe('buildFilterOptionsFromAnalytics', () => {
  it('maps backend option aggregates into stable dropdown candidates', () => {
    const options = buildFilterOptionsFromAnalytics(
      {
        account_stats: [
          {
            id: 'alice@example.com',
            account_snapshot: 'alice@example.com',
            auth_label_snapshot: 'Alice Auth',
            auth_provider_snapshot: 'codex',
            auth_indices: ['auth-1'],
            sources: ['alice.json'],
            source_hashes: ['source-a'],
            calls: 1,
            success_calls: 1,
            failure_calls: 0,
            success_rate: 1,
            input_tokens: 1,
            output_tokens: 1,
            cached_tokens: 0,
            cache_read_tokens: 0,
            cache_creation_tokens: 0,
            total_tokens: 2,
            cost: 0,
            average_latency_ms: null,
            last_seen_ms: 1,
            models: [],
          },
          {
            id: 'bob@example.com',
            account_snapshot: 'bob@example.com',
            auth_label_snapshot: 'Bob Auth',
            auth_provider_snapshot: 'gemini',
            auth_indices: ['auth-2'],
            sources: ['bob.json'],
            source_hashes: ['source-b'],
            calls: 1,
            success_calls: 1,
            failure_calls: 0,
            success_rate: 1,
            input_tokens: 1,
            output_tokens: 1,
            cached_tokens: 0,
            cache_read_tokens: 0,
            cache_creation_tokens: 0,
            total_tokens: 2,
            cost: 0,
            average_latency_ms: null,
            last_seen_ms: 2,
            models: [],
          },
        ],
        api_key_stats: [
          {
            id: 'key-a',
            api_key_hash: 'key-a',
            account_snapshot: 'alice@example.com',
            auth_label_snapshot: 'Alice Auth',
            auth_provider_snapshot: 'codex',
            auth_indices: ['auth-1'],
            sources: ['alice.json'],
            source_hashes: ['source-a'],
            calls: 1,
            success_calls: 1,
            failure_calls: 0,
            success_rate: 1,
            input_tokens: 1,
            output_tokens: 1,
            cached_tokens: 0,
            cache_read_tokens: 0,
            cache_creation_tokens: 0,
            total_tokens: 2,
            cost: 0,
            average_latency_ms: null,
            last_seen_ms: 1,
            models: [],
          },
        ],
        channel_share: [
          {
            auth_index: 'auth-1',
            auth_provider_snapshot: 'codex',
            calls: 1,
            success: 1,
            failure: 0,
            tokens: 2,
            cost: 0,
            average_latency_ms: null,
          },
          {
            auth_index: 'auth-2',
            auth_provider_snapshot: 'gemini',
            calls: 1,
            success: 1,
            failure: 0,
            tokens: 2,
            cost: 0,
            average_latency_ms: null,
          },
        ],
        model_stats: [
          {
            model: 'gpt-a',
            calls: 1,
            success_calls: 1,
            failure_calls: 0,
            success_rate: 1,
            input_tokens: 1,
            output_tokens: 1,
            cached_tokens: 0,
            cache_read_tokens: 0,
            cache_creation_tokens: 0,
            total_tokens: 2,
            cost: 0,
          },
          {
            model: 'gpt-b',
            calls: 1,
            success_calls: 1,
            failure_calls: 0,
            success_rate: 1,
            input_tokens: 1,
            output_tokens: 1,
            cached_tokens: 0,
            cache_read_tokens: 0,
            cache_creation_tokens: 0,
            total_tokens: 2,
            cost: 0,
          },
        ],
      },
      new Map([
        [
          'auth-1',
          {
            authIndex: 'auth-1',
            label: 'Alice Auth',
            account: 'alice@example.com',
            provider: 'codex',
            status: 'active',
            disabled: false,
            unavailable: false,
            runtimeOnly: false,
            planType: 'pro',
            updatedAt: '',
          },
        ],
      ]),
      new Map(),
      buildSourceInfoMap({}),
      new Map([
        [
          'auth-1',
          {
            key: 'primary:0',
            name: 'Primary Channel',
            baseUrl: 'https://primary.example.com',
            host: 'primary.example.com',
            disabled: false,
            authIndices: ['auth-1'],
            modelNames: [],
          },
        ],
      ]),
      new Map([['key-a', { label: 'Key A', masked: 'sk********aa' }]])
    );

    expect(options.accountRows.map((row) => row.account).sort()).toEqual([
      'alice@example.com',
      'bob@example.com',
    ]);
    expect(options.apiKeyRows.map((row) => row.apiKeyLabel)).toEqual(['Key A']);
    expect(options.providers).toEqual(['codex', 'gemini']);
    expect(options.models).toEqual(['gpt-a', 'gpt-b']);
    expect(options.channels).toEqual(['Primary Channel', 'gemini']);
  });

  it('maps lightweight selector values without requiring aggregate rows', () => {
    const options = buildFilterOptionsFromAnalytics(
      {
        accounts: ['alice@example.com', 'bob@example.com'],
        api_key_hashes: ['key-a'],
        providers: ['codex'],
        models: ['gpt-a'],
      },
      new Map(),
      new Map(),
      buildSourceInfoMap({}),
      new Map([
        [
          'auth-1',
          {
            key: 'primary:0',
            name: 'Primary Channel',
            baseUrl: 'https://primary.example.com',
            host: 'primary.example.com',
            disabled: false,
            authIndices: ['auth-1'],
            modelNames: [],
          },
        ],
      ]),
      new Map([['key-a', { label: 'Key A', masked: 'sk********aa' }]])
    );

    expect(options.accountRows.map((row) => row.account)).toEqual([
      'alice@example.com',
      'bob@example.com',
    ]);
    expect(options.accountRows.map((row) => row.filterValue)).toEqual([
      'account:alice%40example.com',
      'account:bob%40example.com',
    ]);
    expect(options.apiKeyRows.map((row) => row.apiKeyLabel)).toEqual(['Key A']);
    expect(options.providers).toEqual(['codex']);
    expect(options.models).toEqual(['gpt-a']);
    expect(options.channels).toEqual(['Primary Channel', 'codex']);
  });

  it('uses auth or source identities for OpenAI-compatible account option rows', () => {
    const options = buildFilterOptionsFromAnalytics(
      {
        account_stats: [
          {
            id: 'openai-provider',
            account_snapshot: '',
            auth_label_snapshot: '',
            auth_provider_snapshot: 'openai',
            auth_indices: ['openai-auth'],
            sources: ['k:upstream-key'],
            source_hashes: ['source-a'],
            calls: 1,
            success_calls: 1,
            failure_calls: 0,
            success_rate: 1,
            input_tokens: 1,
            output_tokens: 1,
            cached_tokens: 0,
            cache_read_tokens: 0,
            cache_creation_tokens: 0,
            total_tokens: 2,
            cost: 0,
            average_latency_ms: null,
            last_seen_ms: 2,
            models: [],
          },
          {
            id: 'openai-provider-without-auth',
            account_snapshot: '',
            auth_label_snapshot: '',
            auth_provider_snapshot: 'openai',
            auth_indices: [],
            sources: ['k:upstream-key-2'],
            source_hashes: ['source-b'],
            calls: 1,
            success_calls: 1,
            failure_calls: 0,
            success_rate: 1,
            input_tokens: 1,
            output_tokens: 1,
            cached_tokens: 0,
            cache_read_tokens: 0,
            cache_creation_tokens: 0,
            total_tokens: 2,
            cost: 0,
            average_latency_ms: null,
            last_seen_ms: 1,
            models: [],
          },
        ],
      },
      new Map(),
      new Map(),
      buildSourceInfoMap({
        openaiCompatibility: [
          {
            name: 'OpenAI Compatible',
            baseUrl: 'https://compat.example.com',
            apiKeyEntries: [{ apiKey: 'upstream-key', authIndex: 'openai-auth' }],
          },
        ],
      }),
      new Map([
        [
          'openai-auth',
          {
            key: 'openai:0',
            name: 'OpenAI Compatible',
            baseUrl: 'https://compat.example.com',
            host: 'compat.example.com',
            disabled: false,
            authIndices: ['openai-auth'],
            modelNames: [],
          },
        ],
      ]),
      new Map()
    );

    expect(options.accountRows.map((row) => row.filterValue)).toEqual([
      'auth:openai-auth',
      'source:source-b',
    ]);
    expect(options.accountRows[0].sourceKeys).toContain('openai:0:0');
  });
});

describe('analytics failure source display', () => {
  const authMetaMap = new Map([
    [
      'auth-1',
      {
        authIndex: 'auth-1',
        label: 'Team Auth',
        account: 'alice@example.com',
        provider: 'codex',
        status: 'active',
        disabled: false,
        unavailable: false,
        runtimeOnly: false,
        planType: 'pro',
        updatedAt: '',
      },
    ],
  ]);
  const authFileMap = new Map([['auth-1', { name: 'Team Auth', type: 'codex' }]]);
  const sourceInfoMap = buildSourceInfoMap({});
  const channelByAuthIndex = new Map([
    [
      'auth-1',
      {
        key: 'relay:0',
        name: 'Production Relay',
        baseUrl: 'https://relay.example.com/v1',
        host: 'relay.example.com',
        disabled: false,
        authIndices: ['auth-1'],
        modelNames: [],
      },
    ],
  ]);

  it('uses channel metadata for channel share rows when auth metadata exists', () => {
    const rows = buildChannelRowsFromAnalytics(
      [
        {
          auth_index: 'auth-1',
          source: 'm:sk-a...zzzz',
          account_snapshot: 'snapshot@example.com',
          auth_label_snapshot: 'Snapshot Auth',
          auth_provider_snapshot: 'codex',
          calls: 10,
          success: 8,
          failure: 2,
          tokens: 1000,
          cost: 0.12,
          average_latency_ms: 120,
        },
      ],
      authMetaMap,
      authFileMap,
      sourceInfoMap,
      channelByAuthIndex
    );

    expect(rows[0].label).toBe('Production Relay');
    expect(rows[0].host).toBe('relay.example.com');
    expect(rows[0].authLabels).toEqual(['Team Auth']);
  });

  it('uses channel share snapshots when current auth metadata is missing', () => {
    const rows = buildChannelRowsFromAnalytics(
      [
        {
          auth_index: 'legacy-auth',
          source: 'm:sk-a...zzzz',
          account_snapshot: 'legacy@example.com',
          auth_label_snapshot: 'Legacy Auth',
          auth_provider_snapshot: 'codex',
          calls: 4,
          success: 3,
          failure: 1,
          tokens: 500,
          cost: 0.04,
          average_latency_ms: 200,
        } satisfies MonitoringAnalyticsChannelShareRow,
      ],
      new Map(),
      new Map(),
      sourceInfoMap,
      new Map()
    );

    expect(rows[0].label).toBe('Legacy Auth');
    expect(rows[0].provider).toBe('codex');
    expect(rows[0].label).not.toBe('legacy-auth');
  });

  it('uses readable account metadata for recent failures instead of hashes', () => {
    const rows = buildFailureRowsFromAnalytics(
      [
        {
          timestamp_ms: Date.UTC(2026, 4, 20, 1, 2, 3),
          model: 'gpt-test',
          api_key_hash: 'api-key-hash',
          source: 'm:sk-a...zzzz',
          source_hash: 'source-hash',
          auth_index: 'auth-1',
          account_snapshot: 'snapshot@example.com',
          auth_label_snapshot: 'Snapshot Auth',
          auth_provider_snapshot: 'codex',
          endpoint: 'POST /v1/chat/completions',
          duration_ms: 123,
          fail_status_code: 429,
          fail_summary: 'rate limit exceeded',
        },
      ],
      authMetaMap,
      authFileMap,
      sourceInfoMap,
      channelByAuthIndex
    );

    expect(rows[0].source).toBe('Team Auth');
    expect(rows[0].channel).toBe('Production Relay');
    expect(rows[0].source).not.toBe('source-hash');
  });

  it('uses readable source labels for failure source rows', () => {
    const rows = buildFailureSourceRowsFromAnalytics(
      [
        {
          source_hash: 'source-hash',
          auth_index: 'auth-1',
          calls: 10,
          failure: 2,
          last_seen_ms: Date.UTC(2026, 4, 20, 1, 2, 3),
          average_latency_ms: 120,
        },
      ],
      authMetaMap,
      authFileMap,
      sourceInfoMap,
      channelByAuthIndex
    );

    expect(rows[0].label).toBe('Team Auth');
    expect(rows[0].channel).toBe('Production Relay');
    expect(rows[0].label).not.toBe('source-hash');
  });
});
