import { renderToStaticMarkup } from 'react-dom/server';
import type { TFunction } from 'i18next';
import { describe, expect, it, vi } from 'vitest';
import type { AccountDisplayMode } from '@/features/monitoring/accountOverviewState';
import enLocale from '@/i18n/locales/en.json';
import ruLocale from '@/i18n/locales/ru.json';
import zhCNLocale from '@/i18n/locales/zh-CN.json';
import zhTWLocale from '@/i18n/locales/zh-TW.json';
import type { MonitoringEventRow } from '@/features/monitoring/hooks/useMonitoringData';
import styles from '../MonitoringCenterPage.module.scss';
import { RealtimeEventsPanel } from './RealtimeEventsPanel';

const t = ((key: string, options?: Record<string, unknown>) => {
  const messages: Record<string, string> = {
    'common.loading': 'Loading',
    'common.copy': 'Copy',
    'common.yes': 'Yes',
    'common.no': 'No',
    'monitoring.account_overview_account_display_masked': 'Masked',
    'monitoring.account_overview_account_display_full': 'Full',
    'monitoring.account_overview_show_full_accounts_hint': 'Show full accounts',
    'monitoring.account_overview_show_masked_accounts_hint': 'Show masked accounts',
    'monitoring.cache_creation_tokens_short': 'Create',
    'monitoring.cache_read_tokens_short': 'Read',
    'monitoring.column_latency': 'Latency',
    'monitoring.column_model': 'Model',
    'monitoring.column_output_tps': 'TPS',
    'monitoring.column_source_api_key': 'Source / API Key',
    'monitoring.column_time': 'Time',
    'monitoring.column_type': 'Type',
    'monitoring.elapsed_short': 'Elapsed',
    'monitoring.executor_type_short': 'Executor',
    'monitoring.fail_status_code_short': 'HTTP',
    'monitoring.header_should_retry': 'Should retry',
    'monitoring.filter_account': 'Account',
    'monitoring.filter_status_failed': 'Failed only',
    'monitoring.filter_provider': 'Provider',
    'monitoring.client_ip': 'Client IP',
    'monitoring.x_forwarded_for_unverified': 'Forwarded chain (unverified)',
    'monitoring.user_agent': 'User-Agent',
    'monitoring.request_metadata': 'Request metadata',
    'monitoring.cached_tokens': 'Cached Tokens',
    'monitoring.cache_read_tokens': 'Cache Read Tokens',
    'monitoring.cache_creation_tokens': 'Cache Creation Tokens',
    'monitoring.realtime_usage_total_label': 'Total',
    'monitoring.realtime_usage_input_label': 'Input',
    'monitoring.realtime_usage_output_label': 'Output',
    'monitoring.realtime_usage_reasoning_label': 'Reasoning',
    'monitoring.realtime_usage_cached_label': 'Cached',
    'monitoring.realtime_usage_cache_read_label': 'Cache Read',
    'monitoring.realtime_usage_cache_creation_label': 'Cache Creation',
    'monitoring.realtime_cache_hit_label': 'Hit',
    'monitoring.realtime_cache_write_label': 'Write',
    'monitoring.load_more_events': 'Load more',
    'monitoring.log_rows': 'Rows',
    'monitoring.no_more_events': 'No more events',
    'monitoring.events_loaded_summary': 'Loaded {{loaded}} of {{total}} events',
    'monitoring.events_all_loaded': 'All {{total}} events loaded',
    'monitoring.events_retention_limited': 'Kept the newest {{loaded}} of {{total}} events',
    'monitoring.reasoning_service_short': 'Reasoning / Tier',
    'monitoring.realtime_reasoning_label': 'Reasoning',
    'monitoring.realtime_service_label': 'Service',
    'monitoring.recent_failures': 'Failures',
    'monitoring.realtime_api_key_hash': 'API Key hash',
    'monitoring.realtime_api_key_label': 'API Key',
    'monitoring.realtime_api_key_masked': 'Masked key',
    'monitoring.request_status': 'Status',
    'monitoring.result_failed': 'Failed',
    'monitoring.result_success': 'Success',
    'monitoring.provider_usage_xai_exhausted': 'xAI included free usage exhausted',
    'monitoring.provider_usage_remaining': 'Remaining',
    'monitoring.provider_usage_overage': 'Overage',
    'monitoring.provider_usage_rolling_24h': 'Rolling 24-hour window',
    'monitoring.provider_usage_estimated_recovery': 'Estimated recovery',
    'monitoring.provider_rate_limit': 'API rate limit',
    'monitoring.provider_rate_limit_requests': 'Requests',
    'monitoring.provider_rate_limit_tokens': 'Tokens',
    'monitoring.provider_data_policy': 'Data policy',
    'monitoring.provider_zero_retention': 'Zero retention',
    'monitoring.service_tier_short': 'Tier',
    'monitoring.request_service_tier_short': 'Requested tier',
    'monitoring.response_service_tier_short': 'Reported tier',
    'monitoring.this_call_cost': 'Cost',
    'monitoring.this_call_usage': 'Usage',
    'monitoring.this_call_cache_rate': 'This Call Cache Rate',
    'monitoring.this_call_cache_rate_short': 'Cache Rate',
    'monitoring.ttft_short': 'TTFT',
  };
  let message = messages[key] ?? key;
  if (options) {
    message = message.replace(/\{\{(\w+)\}\}/g, (_, name: string) =>
      String((options as Record<string, unknown>)[name] ?? '')
    );
  }
  return message;
}) as unknown as TFunction;

const noop = vi.fn();

type PanelOverrides = {
  accountDisplayMode?: AccountDisplayMode;
  eventsHasMore?: boolean;
  eventsLoadingMore?: boolean;
  eventsRetentionLimited?: boolean;
  eventsTotalCount?: number;
  eventsLoadedCount?: number;
  hasPrices?: boolean;
};

const baseRow = (overrides: Partial<MonitoringEventRow> = {}): MonitoringEventRow => ({
  id: 'row-1',
  timestamp: '2026-04-25T00:00:00Z',
  timestampMs: Date.UTC(2026, 3, 25, 12, 34, 56),
  dayKey: '2026-04-25',
  hourLabel: '00:00',
  model: 'client-gpt',
  resolvedModel: 'gpt-5.4',
  endpoint: 'POST /v1/chat/completions',
  endpointMethod: 'POST',
  endpointPath: '/v1/chat/completions',
  sourceKey: 'source:user@example.com',
  source: 'user@example.com',
  sourceMasked: 'user@example.com',
  account: 'user@example.com',
  accountMasked: 'user@example.com',
  authIndex: '0',
  authIndexMasked: '0',
  authLabel: '0',
  projectId: '',
  apiKeyHash: '',
  apiKeyLabel: '-',
  apiKeyMasked: '-',
  provider: 'openai',
  planType: '-',
  channel: 'openai',
  channelHost: '-',
  channelDisabled: false,
  failed: false,
  statsIncluded: true,
  latencyMs: 1500,
  ttftMs: 500,
  tokensPerSecond: 13.3,
  inputTokens: 10,
  outputTokens: 20,
  reasoningTokens: 3,
  cachedTokens: 5,
  cacheReadTokens: 0,
  cacheCreationTokens: 0,
  totalTokens: 33,
  totalCost: 0,
  taskKey: 'task-1',
  searchText: '',
  ...overrides,
});

const renderPanel = (row: MonitoringEventRow, overrides: PanelOverrides = {}) =>
  renderToStaticMarkup(
    <RealtimeEventsPanel
      embedded
      rows={[row]}
      pagination={{
        currentPage: 1,
        totalPages: 1,
        pageItems: [row],
        startItem: 1,
        endItem: 1,
      }}
      pageSize={10}
      scopedFailureCount={row.failed ? 1 : 0}
      failedOnlyActive={false}
      eventsHasMore={overrides.eventsHasMore ?? false}
      eventsLoadingMore={overrides.eventsLoadingMore ?? false}
      eventsRetentionLimited={overrides.eventsRetentionLimited ?? false}
      eventsTotalCount={overrides.eventsTotalCount ?? 1}
      eventsLoadedCount={overrides.eventsLoadedCount ?? 1}
      overallLoading={false}
      hasPrices={overrides.hasPrices ?? false}
      accountDisplayMode={overrides.accountDisplayMode ?? 'masked'}
      locale="en-US"
      emptyState={<span>empty</span>}
      t={t}
      onToggleFailedOnly={noop}
      onAccountDisplayModeChange={noop}
      onPageChange={noop}
      onPageSizeChange={noop}
      onLoadMoreEvents={noop}
    />
  );

describe('RealtimeEventsPanel', () => {
  const expectedDate = new Date(baseRow().timestampMs).toLocaleDateString('en-US', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  });
  const expectedTime = new Date(baseRow().timestampMs).toLocaleTimeString('en-US', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  });

  it('renders CPA v7.1.18 usage details for failed rows', () => {
    const markup = renderPanel(
      baseRow({
        failed: true,
        executorType: 'codex',
        reasoningEffort: 'medium',
        serviceTier: 'priority',
        requestServiceTier: 'priority',
        responseServiceTier: 'default',
        cacheReadTokens: 4,
        cacheCreationTokens: 1,
        failStatusCode: 429,
        failSummary: 'rate limit exceeded',
      })
    );

    expect(markup).toContain(
      `class="${styles.realtimeSettingsColumn}">Reasoning / Tier</th>`
    );
    expect(markup).toContain('>TPS</th>');
    expect(markup).toContain(styles.realtimeTpsColumn);
    expect(markup).toContain(styles.realtimeLatencyColumn);
    expect(markup).toContain(styles.realtimeTimeColumn);
    expect(markup.match(new RegExp(styles.realtimeCenteredColumn, 'g'))).toHaveLength(2);
    expect(markup.match(new RegExp(styles.realtimeUsageColumn, 'g'))).toHaveLength(2);
    expect(markup.match(new RegExp(styles.realtimeCacheRateColumn, 'g'))).toHaveLength(2);
    expect(markup.match(new RegExp(styles.realtimeCostColumn, 'g'))).toHaveLength(2);
    expect(markup.match(new RegExp(styles.realtimeSettingsColumn, 'g'))).toHaveLength(2);
    expect(markup).not.toContain('>Success Rate</th>');
    expect(markup).not.toContain('>Calls</th>');
    expect(markup.indexOf('>Usage</th>')).toBeLessThan(markup.indexOf('>Cache Rate</th>'));
    expect(markup.indexOf('>Cache Rate</th>')).toBeLessThan(markup.indexOf('>Cost</th>'));
    expect(markup).toContain('Source / API Key');
    expect(markup).not.toContain('>Executor: codex<');
    expect(markup).not.toContain('Executor: codex');
    expect(markup).toContain('>Reasoning</span><span class=');
    expect(markup).toContain('>medium</span>');
    expect(markup).toContain('>Service</span><span class=');
    expect(markup).toContain('>priority</span>');
    expect(markup).not.toContain('default</span>');
    expect(markup).toContain(styles.realtimeReasoningValue);
    expect(markup).toContain(styles.realtimeServiceValue);
    expect(markup).toContain('client-gpt');
    expect(markup).toContain('gpt-5.4');
    expect(markup).not.toContain('Resolved');
    expect(markup).not.toContain('POST /v1/chat/completions');
    expect(markup).toContain('Failed');
    expect(markup).toContain('>Elapsed</th>');
    expect(markup).toContain('>TTFT</span><span class=');
    expect(markup).toContain('500 ms');
    expect(markup).toContain('Elapsed');
    expect(markup).toContain('1.5 s');
    expect(markup).toContain('>13</span>');
    expect(markup).not.toContain('>Gen</span>');
    expect(markup).not.toContain('>E2E</span>');
    expect(markup).toContain(styles.realtimeUsageCell);
    expect(markup).toContain('>↑</span>10');
    expect(markup).toContain('>↓</span>20');
    expect(markup).toContain('>Total</span>');
    expect(markup).toContain('>Input</span>');
    expect(markup).toContain('>Output</span>');
    expect(markup).toContain('>Reasoning</span>');
    expect(markup).toContain('>Cached</span>');
    expect(markup).toContain('>Cache Read</span>');
    expect(markup).toContain('>Cache Creation</span>');
    expect(markup).toContain(styles.realtimeUsageTooltip);
    expect(markup).toContain(styles.realtimeUsageTooltipLeftBelow);
    expect(markup).toContain('role="tooltip"');
    expect(markup).toContain(styles.realtimeFailureTooltip);
    expect(markup).toContain(styles.realtimeFailureTooltipBelow);
    expect(markup).toContain('aria-describedby=');
    expect(markup).toContain('aria-label="HTTP 429 · rate limit exceeded"');
    expect(markup).toContain('aria-label="Copy"');
    expect(markup).toContain('HTTP 429');
    expect(markup).toContain(`<span class="${styles.realtimeStatusCode}">HTTP 429</span>`);
    expect(markup).toContain('rate limit exceeded');
  });

  it('renders structured xAI free-usage exhaustion evidence', () => {
    const markup = renderPanel(
      baseRow({
        provider: 'xai',
        failed: true,
        failStatusCode: 429,
        failSummary: '{"code":"subscription:free-usage-exhausted"}',
        responseMetadata: {
          errors: { should_retry: true },
          provider_usage: {
            provider: 'xai',
            kind: 'included_free_usage',
            state: 'exhausted',
            code: 'subscription:free-usage-exhausted',
            model: 'grok-4.5-build-free',
            unit: 'tokens',
            actual: 1_024_413,
            limit: 1_000_000,
            remaining: 0,
            overage: 24_413,
            window_kind: 'rolling_24h',
            recover_at_ms: Date.UTC(2026, 3, 26, 12, 34, 56),
            recover_at_estimated: true,
          },
          rate_limit: {
            requests: { limit: 21, remaining: 21 },
            tokens: { limit: 1_000_000, remaining: 1_000_000 },
          },
          data_policy: { retention_mode: 'zdr', zero_retention: true },
        },
      })
    );

    expect(markup).toContain('xAI included free usage exhausted');
    expect(markup).toContain('1,024,413 / 1,000,000 tokens');
    expect(markup).toContain('Remaining 0');
    expect(markup).toContain('Overage 24,413');
    expect(markup).toContain('Rolling 24-hour window');
    expect(markup).toContain('Estimated recovery');
    expect(markup).toContain('API rate limit');
    expect(markup).toContain('Data policy');
    expect(markup).toContain('Zero retention');
  });

  it('does not render diagnostics for a successful request', () => {
    const markup = renderPanel(
      baseRow({
        responseMetadata: {
          rate_limit: {
            requests: { limit: 21, remaining: 20 },
            tokens: { limit: 1_000_000, remaining: 999_000 },
          },
          data_policy: { zero_retention: false },
        },
      })
    );

    expect(markup).toContain('Success');
    expect(markup).not.toContain('API rate limit');
    expect(markup).not.toContain('Requests 20 / 21');
    expect(markup).not.toContain('Tokens 999000 / 1000000');
    expect(markup).not.toContain('Zero retention: No');
    expect(markup).not.toContain(styles.realtimeFailureTooltip);
  });

  it('renders safe defaults when optional usage fields are missing', () => {
    const markup = renderPanel(baseRow({ reasoningTokens: 0 }));

    expect(markup).toContain('<colgroup>');
    expect(markup.match(/<col\b/g)).toHaveLength(10);
    expect(
      markup.match(new RegExp(`class="[^"]*${styles.realtimeSettingValue}[^"]*">-</span>`, 'g'))
    ).toHaveLength(2);
    expect(markup).toContain(
      `class="${styles.realtimeSettingsColumn}">Reasoning / Tier</th>`
    );
    expect(markup).toContain('>TPS</th>');
    expect(markup).toContain('Success');
    expect(markup).toContain('>Elapsed</th>');
    expect(markup).toContain('>TTFT</span><span class=');
    expect(markup).toContain(expectedDate);
    expect(markup).toContain(expectedTime);
    expect(markup).toContain('>↑</span>10');
    expect(markup).toContain('>↓</span>20');
    expect(markup).toContain('>Reasoning</span><span class=');
    expect(markup).toContain('>0</span>');
    expect(markup).toContain(styles.realtimeUsageTooltip);
    expect(markup).toContain('role="tooltip"');
    expect(markup).toContain('aria-describedby=');
    expect(markup).not.toContain('HTTP');
  });

  it('keeps realtime request estimated cost at three decimal places', () => {
    const markup = renderPanel(baseRow({ totalCost: 0.1264 }), { hasPrices: true });

    expect(markup).toContain('$0.126');
  });

  it('renders API key alias inside the source cell without adding another column', () => {
    const markup = renderPanel(
      baseRow({
        apiKeyHash: '1234567890abcdef',
        apiKeyLabel: 'Team A',
        apiKeyMasked: 'sk-...cdef',
        executorType: 'codex',
      })
    );

    expect(markup).toContain('<th>Source / API Key</th>');
    expect(markup).toContain('API Key: Team A');
    expect(markup).not.toContain('#12345678');
    expect(markup).toContain('API Key hash: 1234567890abcdef');
    expect(markup).toContain('Masked key: sk-...cdef');
    expect(markup).toMatch(/class="[^"]*realtimeApiKeyLine[^"]*" title=/);
    expect(markup).toContain('Executor: codex');
    expect(markup).not.toContain('>Executor: codex<');
  });

  it('keeps long realtime model names constrained with a full title', () => {
    const longModel =
      'claude-opus-4-6-thinking-with-a-very-long-provider-routing-suffix-for-realtime-monitoring';
    const markup = renderPanel(baseRow({ model: longModel, resolvedModel: longModel }));

    expect(markup).toContain(`title="${longModel}"`);
    expect(markup).toContain(longModel);
    expect(markup).toMatch(/class="[^"]*realtimeModelCell[^"]*"/);
    expect(markup).toMatch(/class="[^"]*realtimeModelText[^"]*"/);
  });

  it('renders the requested model first and the upstream resolved model second', () => {
    const markup = renderPanel(
      baseRow({
        model: 'deepseek-v4-flash',
        requestedModel: 'deepseek-v4-flash(max)',
        resolvedModel: 'resolved-deepseek-v4-flash',
      })
    );

    expect(markup).toContain(
      'title="deepseek-v4-flash(max)\nresolved-deepseek-v4-flash"'
    );
    expect(markup.indexOf('>deepseek-v4-flash(max)</span>')).toBeLessThan(
      markup.indexOf('>resolved-deepseek-v4-flash</small>')
    );
    expect(markup).not.toContain('>deepseek-v4-flash</span>');
  });

  it('does not duplicate an unchanged requested and resolved model', () => {
    const model = 'deepseek-chat(region-us)';
    const markup = renderPanel(
      baseRow({
        model,
        requestedModel: model,
        resolvedModel: model,
      })
    );

    expect(markup).toContain(`title="${model}"`);
    expect(markup).toContain(`>${model}</span>`);
    expect(markup).not.toContain(`>${model}</small>`);
  });

  it('switches realtime source labels between masked and full display', () => {
    const row = baseRow({
      source: 'very-long-user@example.com',
      sourceMasked: 'ver***@example.com',
      account: 'very-long-user@example.com',
      accountMasked: 'ver***@example.com',
      authLabel: '',
      channel: 'openai',
      channelHost: '-',
      provider: 'openai',
    });
    const maskedMarkup = renderPanel(row);
    const fullMarkup = renderPanel(row, { accountDisplayMode: 'full' });

    expect(maskedMarkup).toContain('>ver***@example.com</span>');
    expect(maskedMarkup).toContain(
      'title="ver***@example.com · Provider: openai · very-long-user@example.com'
    );
    expect(fullMarkup).toContain('>very-long-user@example.com</span>');
    expect(fullMarkup).toContain('title="very-long-user@example.com · Provider: openai');
  });

  it('switches the primary source text instead of adding an account metadata line', () => {
    const row = baseRow({
      source: 'visible-user@example.com',
      sourceMasked: 'vis***@example.com',
      account: 'visible-user@example.com',
      accountMasked: 'vis***@example.com',
      authLabel: '',
      channel: 'openai',
      channelHost: '-',
      provider: 'openai',
    });
    const maskedMarkup = renderPanel(row);
    const fullMarkup = renderPanel(row, { accountDisplayMode: 'full' });

    expect(maskedMarkup).toContain('>vis***@example.com</span>');
    expect(maskedMarkup).not.toContain('<small>Account: vis***@example.com</small>');
    expect(fullMarkup).toContain('>visible-user@example.com</span>');
    expect(fullMarkup).not.toContain('<small>Account: visible-user@example.com</small>');
  });

  it('exposes request metadata through a keyboard-expandable disclosure only in full mode', () => {
    const row = baseRow({
      clientIp: '192.0.2.10',
      xForwardedFor: '203.0.113.5, 198.51.100.8',
      userAgent: 'test-client/1.0',
    });
    const maskedMarkup = renderPanel(row);
    const fullMarkup = renderPanel(row, { accountDisplayMode: 'full' });

    for (const value of ['192.0.2.10', '203.0.113.5', 'test-client/1.0']) {
      expect(maskedMarkup).not.toContain(value);
      expect(fullMarkup).toContain(value);
    }
    expect(maskedMarkup).not.toContain('Request metadata');
    expect(fullMarkup).toContain(`<details class="${styles.realtimeRequestMetadata}">`);
    expect(fullMarkup).toContain('<summary>Request metadata</summary>');
    expect(fullMarkup).toContain('Client IP: 192.0.2.10');
    expect(fullMarkup).toContain(
      'Forwarded chain (unverified): 203.0.113.5, 198.51.100.8'
    );
    expect(fullMarkup).toContain('User-Agent: test-client/1.0');
  });

  it('renders a ttft placeholder when ttft is missing', () => {
    const markup = renderPanel(baseRow({ ttftMs: null }));

    expect(markup).toContain('>TPS</th>');
    expect(markup).toContain('>Elapsed</th>');
    expect(markup).not.toContain('500 ms');
    expect(markup).toContain('1.5 s');
    expect(markup).toContain('>TTFT</span><span class=');
    expect(markup).toContain('>--</span>');
    expect(markup).toContain('>Elapsed</span><span class=');
    expect(markup).toContain('>1.5 s</span>');
  });

  it('keeps latency warning and error tone classes on plain text metrics', () => {
    const warningMarkup = renderPanel(baseRow({ latencyMs: 20_000, ttftMs: 1_000 }));
    const errorMarkup = renderPanel(baseRow({ latencyMs: 35_000, ttftMs: 1_000 }));

    expect(warningMarkup).toMatch(/class="[^"]*realtimeMetricText[^"]*warnText[^"]*"/);
    expect(errorMarkup).toMatch(/class="[^"]*realtimeMetricText[^"]*badText[^"]*"/);
  });

  it('colors normal millisecond and second metrics green for both ttft and elapsed time', () => {
    const markup = renderPanel(baseRow({ latencyMs: 470, ttftMs: 120 }));

    expect(markup).toMatch(/class="[^"]*realtimeMetricText[^"]*goodText[^"]*">120 ms/);
    expect(markup).toMatch(/class="[^"]*realtimeMetricText[^"]*goodText[^"]*">470 ms/);
  });

  it('renders each cache detail in the token usage tooltip', () => {
    const markup = renderPanel(
      baseRow({
        cachedTokens: 4,
        cacheReadTokens: 4,
        cacheCreationTokens: 1,
      })
    );

    expect(markup).toContain('>Cached</span><span class=');
    expect(markup).toContain('>Cache Read</span><span class=');
    expect(markup).toContain('>Cache Creation</span><span class=');
    expect(markup).toContain('>4</span>');
    expect(markup).toContain('>1</span>');
  });

  it('shows zero legacy cache alongside GPT-5.6 cache read and write metrics', () => {
    const markup = renderPanel(
      baseRow({
        model: 'gpt-5.6-sol',
        inputTokens: 152_600,
        cachedTokens: 0,
        cacheReadTokens: 151_000,
        cacheCreationTokens: 1_000,
      })
    );

    expect(markup).toContain('>Cached</span><span class=');
    expect(markup).toContain('>Cache Read</span><span class=');
    expect(markup).toContain('>Cache Creation</span><span class=');
    expect(markup).toContain('>151.0K</span>');
    expect(markup).toContain('>1.0K</span>');
    expect(markup).toContain('aria-label="Total: 33, Input: 152.6K, Output: 20, Reasoning: 3, Cached: 0, Cache Read: 151.0K, Cache Creation: 1.0K"');
    expect(markup).toContain('tabindex="0"');
  });

  it('shows the loaded vs total summary with a load-more action when more pages exist', () => {
    const markup = renderPanel(baseRow(), {
      eventsHasMore: true,
      eventsLoadedCount: 500,
      eventsTotalCount: 8000,
    });

    expect(markup).toContain('Loaded 500 of 8000 events');
    expect(markup).toContain('Load more');
    expect(markup).not.toContain('Loaded 8000 of 8000');
  });

  it('renders a single pagination footer without a duplicate all-loaded banner once fully loaded', () => {
    const markup = renderPanel(baseRow(), {
      eventsHasMore: false,
      eventsLoadedCount: 8000,
      eventsTotalCount: 8000,
    });

    expect(markup).toContain('monitoring.pagination_info');
    expect(markup).not.toContain('All 8000 events loaded');
    expect(markup).not.toContain('Load more');
  });

  it('shows the retention limit without a load-more action at the memory cap', () => {
    const markup = renderPanel(baseRow(), {
      eventsHasMore: false,
      eventsRetentionLimited: true,
      eventsLoadedCount: 2000,
      eventsTotalCount: 8000,
    });

    expect(markup).toContain('Kept the newest 2000 of 8000 events');
    expect(markup).not.toContain('Load more');
  });

  it('falls back to the loaded count when the backend omits a larger total', () => {
    const markup = renderPanel(baseRow(), {
      eventsHasMore: true,
      eventsLoadedCount: 500,
      eventsTotalCount: 500,
    });

    expect(markup).toContain('Loaded 500 of 500 events');
    expect(markup).toContain('Load more');
  });

  it('does not render an inline HTTP status code when a failed row has none', () => {
    const markup = renderPanel(
      baseRow({
        failed: true,
        failStatusCode: null,
        failSummary: 'upstream connection reset',
      })
    );

    expect(markup).toContain('Failed');
    expect(markup).toContain('upstream connection reset');
    expect(markup).not.toContain(styles.realtimeStatusCode);
  });

  it('renders direction-1 cache hit rate with write-only note and structured hover card details', () => {
    const hitAndWriteMarkup = renderPanel(
      baseRow({
        inputTokens: 152_600,
        cachedTokens: 0,
        cacheReadTokens: 151_000,
        cacheCreationTokens: 1_000,
      })
    );

    expect(hitAndWriteMarkup).toMatch(
      new RegExp(`class="[^"]*${styles.realtimeCacheRateValue}[^"]*${styles.goodText}[^"]*">99\\.0%<`)
    );
    expect(hitAndWriteMarkup).toContain(styles.realtimeCacheRateInteractive);
    expect(hitAndWriteMarkup).not.toContain(styles.realtimeCacheRateWriteNote);
    expect(hitAndWriteMarkup).toContain('cache-rate-tooltip-row-1');
    expect(hitAndWriteMarkup).toContain(
      'aria-label="This Call Cache Rate: 99.0%, Hit: 151.0K, Write: 1.0K, Input: 152.6K"'
    );
    expect(hitAndWriteMarkup).toContain(
      `<span class="${styles.realtimeUsageTooltipLabel}">Hit</span><span class="${styles.realtimeUsageTooltipValue}">151.0K</span>`
    );
    expect(hitAndWriteMarkup).toContain(
      `<span class="${styles.realtimeUsageTooltipLabel}">Write</span><span class="${styles.realtimeUsageTooltipValue}">1.0K</span>`
    );

    const legacyPlusReadMarkup = renderPanel(
      baseRow({
        inputTokens: 1_000,
        cachedTokens: 500,
        cacheReadTokens: 300,
        cacheCreationTokens: 0,
      })
    );
    expect(legacyPlusReadMarkup).toMatch(
      new RegExp(`class="[^"]*${styles.realtimeCacheRateValue}[^"]*${styles.goodText}[^"]*">80\\.0%<`)
    );
    expect(legacyPlusReadMarkup).not.toContain(styles.realtimeCacheRateWriteNote);
    expect(legacyPlusReadMarkup).toContain(
      `<span class="${styles.realtimeUsageTooltipLabel}">Hit</span><span class="${styles.realtimeUsageTooltipValue}">800</span>`
    );

    const mediumMarkup = renderPanel(
      baseRow({
        inputTokens: 1_000,
        cachedTokens: 500,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
      })
    );
    expect(mediumMarkup).toContain(`<span class="${styles.realtimeCacheRateValue}">50.0%</span>`);
    expect(mediumMarkup).not.toContain(styles.realtimeCacheRateWriteNote);

    const writeOnlyMarkup = renderPanel(
      baseRow({
        inputTokens: 68_000,
        cachedTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 64_000,
      })
    );
    expect(writeOnlyMarkup).toMatch(
      new RegExp(
        `class="[^"]*${styles.realtimeCacheRateValue}[^"]*${styles.realtimeCacheRateMuted}[^"]*">0\\.0%<`
      )
    );
    expect(writeOnlyMarkup).not.toContain(`realtimeCacheRateValue ${styles.badText}`);
    expect(writeOnlyMarkup).toContain(
      `<span class="${styles.realtimeCacheRateWriteNote}">Write 64.0K</span>`
    );
    expect(writeOnlyMarkup).toContain(
      'aria-label="This Call Cache Rate: 0.0%, Hit: 0, Write: 64.0K, Input: 68.0K"'
    );

    const uncachedMarkup = renderPanel(
      baseRow({
        inputTokens: 1_200,
        cachedTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
      })
    );
    expect(uncachedMarkup).toMatch(
      new RegExp(
        `class="[^"]*${styles.realtimeCacheRateValue}[^"]*${styles.realtimeCacheRateMuted}[^"]*">0\\.0%<`
      )
    );
    expect(uncachedMarkup).not.toContain(styles.realtimeCacheRateWriteNote);
    expect(uncachedMarkup).toContain(
      'aria-label="This Call Cache Rate: 0.0%, Hit: 0, Write: 0, Input: 1.2K"'
    );

    const zeroInputMarkup = renderPanel(
      baseRow({
        failed: true,
        inputTokens: 0,
        outputTokens: 0,
        cachedTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        totalTokens: 0,
      })
    );
    expect(zeroInputMarkup).toMatch(
      new RegExp(
        `class="[^"]*${styles.realtimeCacheRateValue}[^"]*${styles.realtimeCacheRateMuted}[^"]*">--<`
      )
    );
    expect(zeroInputMarkup).not.toContain(styles.realtimeCacheRateInteractive);
    expect(zeroInputMarkup).not.toContain('cache-rate-tooltip-row-1');
    expect(zeroInputMarkup).toContain('aria-label="This Call Cache Rate: --"');
  });

  it('defines realtime cache rate and hit/write labels across all four locales', () => {
    const keys = [
      'this_call_cache_rate',
      'this_call_cache_rate_short',
      'realtime_cache_hit_label',
      'realtime_cache_write_label',
    ] as const;

    for (const locale of [enLocale, zhCNLocale, zhTWLocale, ruLocale]) {
      const monitoring = locale.monitoring as Record<string, string>;
      for (const key of keys) {
        expect(monitoring[key]?.trim().length).toBeGreaterThan(0);
      }
    }
  });
});
