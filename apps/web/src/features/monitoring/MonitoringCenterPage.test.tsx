import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { TFunction } from 'i18next';
import { AccountExpandedDetails, AccountOverviewCard } from './MonitoringCenterPage';
import { MonitoringSummarySection } from '@/features/monitoring/components/MonitoringSummarySection';
import {
  buildPrimarySummaryCards,
  buildSecondarySummaryCards,
} from '@/features/monitoring/model/monitoringCenterPageModel';
import type { MonitoringSummary } from '@/features/monitoring/hooks/useMonitoringData';
import { buildEmptyMonitoringStatusData } from '@/features/monitoring/accountOverviewState';
import { buildRealtimeSourceDisplay } from '@/features/monitoring/realtimeSourceDisplay';

const t = ((key: string, options?: Record<string, unknown>) => {
  const copy: Record<string, string> = {
    'monitoring.account_overview_enable_all': 'Enable all',
    'monitoring.account_overview_disable_all': 'Disable all',
    'monitoring.restore_account_scope': 'Restore account scope',
    'monitoring.focus_account': 'Focus account',
    'monitoring.account_overview_enabled_label': 'Enabled',
    'monitoring.account_overview_enabled_label_short': 'Enabled',
    'auth_files.status_toggle_label': 'Enabled',
    'monitoring.account_overview_health_label': 'Health',
    'monitoring.account_overview_health_hint': 'Health hint',
    'monitoring.account_overview_scope_current_filters': 'Scope: current filters',
    'monitoring.account_overview_scope_range': 'Scope: {{range}}',
    'monitoring.account_overview_tokens_title': 'Tokens Usage',
    'monitoring.account_overview_token_structure': 'Token Structure',
    'monitoring.account_overview_models_top': 'Model Usage Top {{count}}',
    'monitoring.account_overview_models_all': 'Model Usage Details',
    'monitoring.account_overview_model_calls_short': 'Calls',
    'monitoring.account_overview_model_success_rate_short': 'Success',
    'monitoring.account_overview_model_input_tokens_short': 'Input',
    'monitoring.account_overview_model_output_tokens_short': 'Output',
    'monitoring.account_overview_model_cached_tokens_short': 'Cache',
    'monitoring.account_overview_model_total_tokens_short': 'Total Tokens',
    'monitoring.account_overview_model_total_cost_short': 'Total Cost',
    'monitoring.account_overview_view_all': 'View All',
    'monitoring.account_overview_collapse_models': 'Collapse',
    'monitoring.account_overview_no_models': 'No model details',
    'monitoring.total_calls': 'Total calls',
    'monitoring.call_success_rate': 'Success rate',
    'monitoring.calls': 'Calls',
    'stats.success': 'Success',
    'stats.failure': 'Failure',
    'monitoring.latest_request_time': 'Latest request',
    'monitoring.column_success_rate': 'Success rate',
    'monitoring.success_calls': 'Success calls',
    'monitoring.failure_calls': 'Failure calls',
    'monitoring.total_tokens': 'Total Tokens',
    'monitoring.input_tokens': 'Input Tokens',
    'monitoring.output_tokens': 'Output Tokens',
    'monitoring.cached_tokens': 'Cached Tokens',
    'monitoring.cache_read_tokens': 'Cache Read Tokens',
    'monitoring.cache_creation_tokens': 'Cache Creation Tokens',
    'monitoring.cache_read_tokens_short': 'Read',
    'monitoring.cache_creation_tokens_short': 'Create',
    'monitoring.estimated_cost': 'Estimated Cost',
    'monitoring.estimated_cost_hint': 'Configured model prices',
    'monitoring.estimated_cost_missing': 'No configured model prices',
    'monitoring.accounts_suffix': 'accounts',
    'monitoring.groups_suffix': 'groups',
    'monitoring.reasoning_tokens': 'Reasoning',
    'monitoring.of_token_mix': 'Share',
    'monitoring.of_input_tokens': 'Input share',
    'usage_stats.model_price_model': 'Model',
    'monitoring.last_sync': 'Last sync',
    'monitoring.account_quota_title': 'Account Quota',
    'monitoring.account_quota_loading': 'Loading quota...',
    'monitoring.account_quota_load_failed': 'Failed to load quota: {{message}}',
    'monitoring.account_quota_empty': 'No queryable quota is available for this account.',
    'monitoring.account_quota_idle': 'Click refresh quota',
    'monitoring.account_quota_refresh_button': 'Refresh',
    'monitoring.account_quota_retry_button': 'Retry',
    'monitoring.account_quota_reset_at': 'Reset',
    'monitoring.filter_provider': 'Provider',
    'monitoring.column_host': 'Host',
    'monitoring.source': 'Source',
    'status_bar.no_requests': 'No requests',
  };
  let value = copy[key] ?? key;
  Object.entries(options ?? {}).forEach(([name, replacement]) => {
    value = value.replace(`{{${name}}}`, String(replacement));
  });
  return value;
}) as TFunction;

describe('MonitoringCenterPage summary cards', () => {
  it('renders all request monitoring summary metrics in one ordered grid with large values intact', () => {
    const summary: MonitoringSummary = {
      totalCalls: 25_500,
      successCalls: 23_600,
      failureCalls: 1_900,
      successRate: 0.999,
      inputTokens: 2_783_500_000,
      outputTokens: 11_700_000,
      reasoningTokens: 5_000_000,
      cachedTokens: 2_595_300_000,
      cacheReadTokens: 444_400_000,
      cacheCreationTokens: 555_500_000,
      totalTokens: 2_795_200_000,
      totalCost: 9_999_999.99,
      averageLatencyMs: 999,
      rpm30m: 0,
      tpm30m: 0,
      avgDailyRequests: 0,
      avgDailyTokens: 0,
      approxTasks: 0,
      approxTaskFailures: 0,
      approxTaskSuccessRate: 0,
      zeroTokenCalls: 0,
      zeroTokenModels: [],
    };
    const primaryCards = buildPrimarySummaryCards({
      summary,
      accountCount: 999,
      failedGroupCount: 88,
      hasPrices: true,
      locale: 'en',
      t,
    });
    const secondaryCards = buildSecondarySummaryCards(summary, 'en', t);

    const html = renderToStaticMarkup(
      <MonitoringSummarySection primaryCards={primaryCards} secondaryCards={secondaryCards} />
    );
    const labels = [
      'Total calls',
      'Success rate',
      'Failure calls',
      'Estimated Cost',
      'Total Tokens',
      'Input Tokens',
      'Output Tokens',
      'Cached Tokens',
    ];
    let previousIndex = -1;

    labels.forEach((label) => {
      const index = html.indexOf(label);
      expect(index).toBeGreaterThan(previousIndex);
      previousIndex = index;
    });

    expect(html).toContain('_summaryGrid');
    expect(html.match(/<strong/g)).toHaveLength(8);
    expect(html).toContain('25.5K');
    expect(html).toContain('1.9K');
    expect(html).toContain('2.8B');
    expect(html).toContain('2.6B');
    expect(html).toContain('role="tooltip"');
    expect(html).toContain('2,795,200,000');
    expect(html).toContain('2,783,500,000');
    expect(html).toContain('2,595,300,000');
    expect(html).toContain('$9,999,999.99');
    expect(html).toContain('Reasoning 5.0M');
    expect(html).toContain('Share 99.6%');
    expect(html).toContain('Share 0.4%');
    expect(html).toContain('Input share 93.2% · Create 555.5M · Read 444.4M');
  });

  it('hides cache creation and read meta when both cached sub-metrics are zero', () => {
    const secondaryCards = buildSecondarySummaryCards(
      {
        totalCalls: 1,
        successCalls: 1,
        failureCalls: 0,
        successRate: 1,
        inputTokens: 1_000,
        outputTokens: 0,
        reasoningTokens: 0,
        cachedTokens: 932,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        totalTokens: 1_000,
        totalCost: 0,
        averageLatencyMs: null,
        rpm30m: 0,
        tpm30m: 0,
        avgDailyRequests: 0,
        avgDailyTokens: 0,
        approxTasks: 0,
        approxTaskFailures: 0,
        approxTaskSuccessRate: 0,
        zeroTokenCalls: 0,
        zeroTokenModels: [],
      },
      'en',
      t
    );
    const cachedCard = secondaryCards.find((card) => card.label === 'Cached Tokens');

    expect(cachedCard?.meta).toBe('Input share 93.2%');
  });
});

describe('MonitoringCenterPage account card', () => {
  it('prefers readable channel names in realtime source cells', () => {
    const display = buildRealtimeSourceDisplay(
      {
        account: 'alice@example.com',
        accountMasked: 'ali***@example.com',
        authLabel: 'alice',
        channel: 'Claude Relay',
        channelHost: 'relay.example.com',
        provider: 'openai',
        source: 'Team Key',
        sourceMasked: 'Team Key',
      },
      t
    );

    expect(display.primary).toBe('Claude Relay');
    expect(display.meta).toBe('Provider: openai');
  });

  it('shows one realtime source meta value by priority', () => {
    const baseRow = {
      account: 'alice@example.com',
      accountMasked: 'ali***@example.com',
      authLabel: 'alice',
      channel: '-',
      channelHost: 'relay.example.com',
      source: 'Team Key',
      sourceMasked: 'Team Key',
    };

    expect(
      buildRealtimeSourceDisplay(
        {
          ...baseRow,
          provider: 'openai',
        },
        t
      ).primary
    ).toBe('relay.example.com');
    expect(
      buildRealtimeSourceDisplay(
        {
          ...baseRow,
          provider: 'openai',
        },
        t
      ).meta
    ).toBe('Provider: openai');

    expect(
      buildRealtimeSourceDisplay(
        {
          ...baseRow,
          provider: '-',
        },
        t,
        'full'
      ).meta
    ).toBe('alice@example.com');
    expect(
      buildRealtimeSourceDisplay(
        {
          ...baseRow,
          provider: '-',
        },
        t,
        'masked'
      ).meta
    ).toBe('ali***@example.com');
  });

  it('prefers resolved hosts over generic provider labels', () => {
    const display = buildRealtimeSourceDisplay(
      {
        account: '',
        accountMasked: '',
        authLabel: '',
        channel: 'codex',
        channelHost: 'api.freemodel.dev',
        provider: 'codex',
        source: 'm:fe_o_raw_c68c',
        sourceMasked: 'm:fe_o...c68c',
      },
      t
    );

    expect(display.primary).toBe('api.freemodel.dev');
    expect(display.meta).toBe('Provider: codex');
  });

  it('switches account labels between masked and full display with full tooltip text', () => {
    const row = {
      id: 'very-long-account-name@example.com',
      account: 'very-long-account-name@example.com',
      displayAccount: 'very-long-account-name@example.com',
      accountMasked: 'ver***@example.com',
      authLabels: ['alpha'],
      authIndices: ['1'],
      channels: ['default'],
      totalCalls: 1,
      successCalls: 1,
      failureCalls: 0,
      successRate: 1,
      inputTokens: 1,
      outputTokens: 1,
      cachedTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      totalTokens: 2,
      totalCost: 0,
      averageLatencyMs: null,
      lastSeenAt: Date.UTC(2026, 4, 10, 12, 0, 0),
      recentPattern: [true],
      models: [],
    };
    const renderCard = (accountDisplayMode: 'masked' | 'full') =>
      renderToStaticMarkup(
        <AccountOverviewCard
          row={row}
          authState={{
            files: [],
            toggleableFileNames: ['alpha.json'],
            enabledState: 'enabled',
          }}
          hasPrices={false}
          locale="en"
          t={t}
          accountDisplayMode={accountDisplayMode}
          isExpanded={false}
          isFocused={false}
          statusData={buildEmptyMonitoringStatusData({
            startMs: Date.UTC(2026, 4, 10, 0, 0, 0),
            endMs: Date.UTC(2026, 4, 10, 23, 59, 59),
          })}
          scopeText="Scope: 5/10 12:00 AM - 11:59 PM"
          statusUpdating={false}
          onToggle={() => {}}
          onFocus={() => {}}
          onToggleEnabled={() => {}}
          onRefreshQuota={() => {}}
        />
      );

    expect(renderCard('masked')).toContain('>ver***@example.com</span>');
    expect(renderCard('masked')).toContain('very-long-account-name@example.com');
    expect(renderCard('full')).toContain('>very-long-account-name@example.com</span>');
  });

  it('renders bulk action buttons for mixed account auth state', () => {
    const html = renderToStaticMarkup(
      <AccountOverviewCard
        row={{
          id: 'account@example.com',
          account: 'account@example.com',
          displayAccount: 'account@example.com',
          accountMasked: 'acc***@example.com',
          authLabels: ['alpha', 'beta'],
          authIndices: ['1', '2'],
          channels: ['default'],
          totalCalls: 10,
          successCalls: 8,
          failureCalls: 2,
          successRate: 0.8,
          inputTokens: 100,
          outputTokens: 50,
          cachedTokens: 10,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          totalTokens: 160,
          totalCost: 1.25,
          averageLatencyMs: 120,
          lastSeenAt: Date.UTC(2026, 4, 10, 12, 0, 0),
          recentPattern: [true, false],
          models: [],
        }}
        authState={{
          files: [],
          toggleableFileNames: ['alpha.json', 'beta.json'],
          enabledState: 'mixed',
        }}
        hasPrices
        locale="en"
        t={t}
        accountDisplayMode="masked"
        isExpanded={false}
        isFocused={false}
        statusData={buildEmptyMonitoringStatusData({
          startMs: Date.UTC(2026, 4, 10, 0, 0, 0),
          endMs: Date.UTC(2026, 4, 10, 23, 59, 59),
        })}
        scopeText="Scope: 5/10 12:00 AM - 11:59 PM"
        statusUpdating={false}
        onToggle={() => {}}
        onFocus={() => {}}
        onToggleEnabled={() => {}}
        onRefreshQuota={() => {}}
      />
    );

    expect(html).toContain('Enable all');
    expect(html).toContain('Disable all');
    expect(html).not.toContain('type="checkbox"');
  });

  it('renders expanded card model usage as readable metadata instead of a table', () => {
    const html = renderToStaticMarkup(
      <AccountOverviewCard
        row={{
          id: 'account@example.com',
          account: 'account@example.com',
          displayAccount: 'account@example.com',
          accountMasked: 'acc***@example.com',
          authLabels: ['alpha'],
          authIndices: ['1'],
          channels: ['default'],
          totalCalls: 221,
          successCalls: 220,
          failureCalls: 1,
          successRate: 0.995,
          inputTokens: 35_000_000,
          outputTokens: 68_500,
          cachedTokens: 33_900_000,
          cacheReadTokens: 1_200_000,
          cacheCreationTokens: 340_000,
          totalTokens: 35_068_500,
          totalCost: 23.04,
          averageLatencyMs: 120,
          lastSeenAt: Date.UTC(2026, 4, 10, 12, 0, 0),
          recentPattern: [true, true],
          models: [
            {
              model: 'gpt-5.5',
              totalCalls: 196,
              successCalls: 195,
              failureCalls: 1,
              successRate: 0.995,
              inputTokens: 33_400_000,
              outputTokens: 66_600,
              cachedTokens: 32_500_000,
              cacheReadTokens: 1_100_000,
              cacheCreationTokens: 300_000,
              totalTokens: 33_466_600,
              totalCost: 23.04,
              lastSeenAt: Date.UTC(2026, 4, 10, 12, 0, 0),
            },
            {
              model: 'codex-auto-review',
              totalCalls: 25,
              successCalls: 24,
              failureCalls: 1,
              successRate: 0.96,
              inputTokens: 1_600_000,
              outputTokens: 1_900,
              cachedTokens: 1_400_000,
              cacheReadTokens: 100_000,
              cacheCreationTokens: 40_000,
              totalTokens: 1_601_900,
              totalCost: 0,
              lastSeenAt: Date.UTC(2026, 4, 10, 12, 0, 0),
            },
          ],
        }}
        authState={{
          files: [],
          toggleableFileNames: ['alpha.json'],
          enabledState: 'enabled',
        }}
        hasPrices
        locale="en"
        t={t}
        accountDisplayMode="masked"
        isExpanded
        isFocused={false}
        statusData={buildEmptyMonitoringStatusData({
          startMs: Date.UTC(2026, 4, 10, 0, 0, 0),
          endMs: Date.UTC(2026, 4, 10, 23, 59, 59),
        })}
        scopeText="Scope: 5/10 12:00 AM - 11:59 PM"
        statusUpdating={false}
        onToggle={() => {}}
        onFocus={() => {}}
        onToggleEnabled={() => {}}
        onRefreshQuota={() => {}}
      />
    );

    expect(html).toContain('gpt-5.5');
    expect(html).toContain('<small>Calls</small><strong>196</strong>');
    expect(html).toContain('<small>Success</small><strong class="_goodText');
    expect(html).toContain('<small>Total Tokens</small><strong>33.5M</strong>');
    expect(html).toContain('<small>Total Cost</small><strong>$23.04</strong>');
    expect(html).not.toContain('<table');
  });

  it('uses a static enabled label beside the account toggle', () => {
    const html = renderToStaticMarkup(
      <AccountOverviewCard
        row={{
          id: 'disabled@example.com',
          account: 'disabled@example.com',
          displayAccount: 'disabled@example.com',
          accountMasked: 'dis***@example.com',
          authLabels: ['alpha'],
          authIndices: ['1'],
          channels: ['default'],
          totalCalls: 0,
          successCalls: 0,
          failureCalls: 0,
          successRate: 0,
          inputTokens: 0,
          outputTokens: 0,
          cachedTokens: 0,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          totalTokens: 0,
          totalCost: 0,
          averageLatencyMs: null,
          lastSeenAt: Date.UTC(2026, 4, 10, 12, 0, 0),
          recentPattern: [],
          models: [],
        }}
        authState={{
          files: [],
          toggleableFileNames: ['alpha.json'],
          enabledState: 'disabled',
        }}
        hasPrices
        locale="en"
        t={t}
        accountDisplayMode="masked"
        isExpanded={false}
        isFocused={false}
        statusData={buildEmptyMonitoringStatusData({
          startMs: Date.UTC(2026, 4, 10, 0, 0, 0),
          endMs: Date.UTC(2026, 4, 10, 23, 59, 59),
        })}
        scopeText="Scope: 5/10 12:00 AM - 11:59 PM"
        statusUpdating={false}
        onToggle={() => {}}
        onFocus={() => {}}
        onToggleEnabled={() => {}}
        onRefreshQuota={() => {}}
      />
    );

    expect(html).toContain('Enabled');
    expect(html).not.toContain('monitoring.account_overview_enabled_label_short');
  });

  it('renders table expanded details with token cards and cache columns in top model table', () => {
    const row = {
      id: 'account@example.com',
      account: 'account@example.com',
      displayAccount: 'account@example.com',
      accountMasked: 'acc***@example.com',
      authLabels: ['alpha'],
      authIndices: ['1'],
      channels: ['default'],
      totalCalls: 221,
      successCalls: 220,
      failureCalls: 1,
      successRate: 0.995,
      inputTokens: 35_000_000,
      outputTokens: 68_500,
      cachedTokens: 33_900_000,
      cacheReadTokens: 1_200_000,
      cacheCreationTokens: 340_000,
      totalTokens: 35_068_500,
      totalCost: 23.04,
      averageLatencyMs: 120,
      lastSeenAt: Date.UTC(2026, 4, 10, 12, 0, 0),
      recentPattern: [true, true],
      models: [
        {
          model: 'gpt-5.5',
          totalCalls: 196,
          successCalls: 195,
          failureCalls: 1,
          successRate: 0.995,
          inputTokens: 33_400_000,
          outputTokens: 66_600,
          cachedTokens: 32_500_000,
          cacheReadTokens: 1_100_000,
          cacheCreationTokens: 300_000,
          totalTokens: 33_466_600,
          totalCost: 23.04,
          lastSeenAt: Date.UTC(2026, 4, 10, 12, 0, 0),
        },
        {
          model: 'codex-auto-review',
          totalCalls: 25,
          successCalls: 24,
          failureCalls: 1,
          successRate: 0.96,
          inputTokens: 1_600_000,
          outputTokens: 1_900,
          cachedTokens: 1_400_000,
          cacheReadTokens: 100_000,
          cacheCreationTokens: 40_000,
          totalTokens: 1_601_900,
          totalCost: 0,
          lastSeenAt: Date.UTC(2026, 4, 10, 12, 1, 0),
        },
        {
          model: 'long-tail-model',
          totalCalls: 1,
          successCalls: 1,
          failureCalls: 0,
          successRate: 1,
          inputTokens: 100,
          outputTokens: 20,
          cachedTokens: 0,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          totalTokens: 120,
          totalCost: 0.01,
          lastSeenAt: Date.UTC(2026, 4, 10, 12, 2, 0),
        },
      ],
    };

    const html = renderToStaticMarkup(
      <AccountExpandedDetails
        row={row}
        hasPrices
        locale="en"
        t={t}
        summaryMetrics={[
          { key: 'total-tokens', label: 'Total Tokens', value: '35.1M' },
          { key: 'input-tokens', label: 'Input Tokens', value: '35.0M' },
          { key: 'output-tokens', label: 'Output Tokens', value: '68.5K' },
          { key: 'cached-tokens', label: 'Cached Tokens', value: '33.9M' },
        ]}
        onRefreshQuota={() => {}}
        variant="table"
      />
    );

    expect(html).toContain('Token Structure');
    expect(html).toContain('Input Tokens');
    expect(html).toContain('Output Tokens');
    expect(html).toContain('Cached Tokens');
    expect(html).not.toContain('Cache Read Tokens');
    expect(html).not.toContain('Cache Creation Tokens');
    expect(html).toContain('Model Usage Top 2');
    expect(html).toContain('View All');
    expect(html).not.toContain('<th>Read</th>');
    expect(html).not.toContain('<th>Create</th>');
    expect(html).toContain('<th>Total Tokens</th>');
    expect(html).toContain('<th>Latest request</th>');
    expect(html).toContain('gpt-5.5');
    expect(html).toContain('codex-auto-review');
    expect(html).not.toContain('long-tail-model');
  });

  it('renders a retry button when account quota refresh failed', () => {
    const html = renderToStaticMarkup(
      <AccountExpandedDetails
        row={{
          id: 'account@example.com',
          account: 'account@example.com',
          displayAccount: 'account@example.com',
          accountMasked: 'acc***@example.com',
          authLabels: ['alpha'],
          authIndices: ['1'],
          channels: ['default'],
          totalCalls: 0,
          successCalls: 0,
          failureCalls: 0,
          successRate: 0,
          inputTokens: 0,
          outputTokens: 0,
          cachedTokens: 0,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          totalTokens: 0,
          totalCost: 0,
          averageLatencyMs: null,
          lastSeenAt: Date.UTC(2026, 4, 10, 12, 0, 0),
          recentPattern: [],
          models: [],
        }}
        hasPrices={false}
        locale="en"
        t={t}
        summaryMetrics={[
          { key: 'total-tokens', label: 'Total Tokens', value: '0' },
          { key: 'input-tokens', label: 'Input Tokens', value: '0' },
          { key: 'output-tokens', label: 'Output Tokens', value: '0' },
          { key: 'cached-tokens', label: 'Cached Tokens', value: '0' },
        ]}
        quotaState={{
          status: 'error',
          targetKey: 'account@example.com',
          entries: [],
          error: 'upstream timeout',
        }}
        onRefreshQuota={() => {}}
        variant="table"
      />
    );

    expect(html).toContain('Failed to load quota: upstream timeout');
    expect(html).toContain('Retry');
  });
});
