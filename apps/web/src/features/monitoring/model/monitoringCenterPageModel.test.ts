import type { TFunction } from 'i18next';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  fetchAntigravityQuota,
  fetchClaudeQuota,
  fetchCodexQuota,
  fetchKimiQuota,
  fetchXaiQuota,
} from '@/utils/quota';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';
import type { MonitoringAccountQuotaTarget } from '@/features/monitoring/accountOverviewQuotaTargets';
import type {
  MonitoringAccountRow,
  MonitoringApiKeyRow,
} from '@/features/monitoring/hooks/useMonitoringData';
import {
  buildAccountOptions,
  buildApiKeyOptionsFromRows,
  buildChannelOptionsFromValues,
  buildAccountQuotaRefreshFailureEntry,
  buildMonitoringInitialStateFromQuery,
  buildModelOptionsFromValues,
  buildProviderOptionsFromValues,
  mergeObservedAccountQuotaEntry,
  mergeObservedAccountQuotaState,
  requestAccountQuota,
} from './monitoringCenterPageModel';
import { getDefaultMonitoringCenterUiState } from '@/features/monitoring/monitoringCenterUiState';

vi.mock('@/utils/quota', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/utils/quota')>();
  return {
    ...actual,
    fetchAntigravityQuota: vi.fn(),
    fetchClaudeQuota: vi.fn(),
    fetchCodexQuota: vi.fn(),
    fetchKimiQuota: vi.fn(),
    fetchXaiQuota: vi.fn(),
  };
});

const t = ((key: string, options?: Record<string, unknown>) => {
  const copy: Record<string, string> = {
    'antigravity_quota.title': 'Antigravity Quota',
    'claude_quota.title': 'Claude Quota',
    'claude_quota.plan_label': 'Plan',
    'claude_quota.plan_pro': 'Pro',
    'claude_quota.extra_usage_label': 'Extra Usage',
    'claude_quota.empty_windows': 'No Claude quota data',
    'claude_quota.five_hour': '5-hour limit',
    'codex_quota.title': 'Codex Quota',
    'codex_quota.empty_windows': 'No Codex quota data',
    'codex_quota.plan_label': 'Plan',
    'codex_quota.plan_free': 'Free',
    'codex_quota.monthly_window': 'Monthly limit',
    'codex_quota.window_usage_duration': '{{used}} / {{total}} used',
    'kimi_quota.title': 'Kimi Quota',
    'kimi_quota.empty_data': 'No Kimi quota data',
    'xai_quota.title': 'xAI Quota',
    'xai_quota.empty_data': 'No xAI quota data',
    'xai_quota.monthly_limit': 'Monthly billing limit',
    'xai_quota.monthly_credits': 'Monthly credits',
    'xai_quota.weekly_limit': 'Weekly limit',
    'xai_quota.used_percent': 'Used {{percent}}',
    'xai_quota.product_usage': '{{product}} usage',
    'xai_quota.pay_as_you_go_label': 'Pay-as-you-go',
    'xai_quota.on_demand_cap': 'On-demand cap',
    'xai_quota.usage_amount': '{{remaining}} / {{limit}} remaining',
    'xai_quota.partial_data': 'Some billing data is unavailable. Reason: {{details}}',
    'xai_quota.partial_unknown': 'The cause could not be determined',
    'xai_quota.official_api_health':
      'Official xAI API identity is reachable. Billing and remaining quota are unavailable for this OAuth credential.',
    'xai_quota.diagnostic_protocol_changed':
      'The billing endpoint returned data that cannot currently be recognized',
  };
  let value = copy[key] ?? key;
  Object.entries(options ?? {}).forEach(([name, replacement]) => {
    value = value.replace(`{{${name}}}`, String(replacement));
  });
  return value;
}) as TFunction;

const createTarget = (
  overrides: Partial<MonitoringAccountQuotaTarget>
): MonitoringAccountQuotaTarget => ({
  key: overrides.key ?? 'claude::1::auth.json',
  provider: overrides.provider ?? 'claude',
  authIndex: overrides.authIndex ?? '1',
  authLabel: overrides.authLabel ?? 'Auth',
  fileName: overrides.fileName ?? 'auth.json',
  file: overrides.file ?? {
    name: overrides.fileName ?? 'auth.json',
    type: overrides.provider ?? 'claude',
    authIndex: overrides.authIndex ?? '1',
  },
  accountId: overrides.accountId ?? null,
  planType: overrides.planType ?? null,
});

const createAccountRow = (
  account: string,
  overrides: Partial<MonitoringAccountRow> = {}
): MonitoringAccountRow => ({
  id: account,
  account,
  displayAccount: account,
  accountMasked: account,
  authLabels: [],
  authIndices: [],
  channels: [],
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
  lastSeenAt: 1,
  recentPattern: [],
  models: [],
  ...overrides,
});

const createApiKeyRow = (apiKeyHash: string, label: string): MonitoringApiKeyRow => ({
  id: apiKeyHash,
  apiKeyHash,
  apiKeyLabel: label,
  apiKeyMasked: label,
  isUnknown: false,
  authLabels: [],
  sourceLabels: [],
  channels: [],
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
  lastSeenAt: 1,
  models: [],
});

describe('monitoringCenterPageModel filter options', () => {
  it('keeps Chinese compact all-filter labels contextual', () => {
    const keys = [
      'filter_all_accounts_short',
      'filter_all_providers_short',
      'filter_all_models_short',
      'filter_all_channels_short',
      'filter_all_api_keys_short',
      'filter_all_statuses_short',
    ] as const;

    expect(keys.map((key) => zhCN.monitoring[key])).toEqual([
      '全部账号',
      '全部提供方',
      '全部模型',
      '全部渠道',
      '全部调用方密钥',
      '全部状态',
    ]);
    expect(keys.map((key) => zhTW.monitoring[key])).toEqual([
      '全部帳號',
      '全部提供方',
      '全部模型',
      '全部渠道',
      '全部呼叫方密鑰',
      '全部狀態',
    ]);
    expect(new Set(keys.map((key) => zhCN.monitoring[key])).size).toBe(keys.length);
    expect(new Set(keys.map((key) => zhTW.monitoring[key])).size).toBe(keys.length);
  });

  it('maps usage analytics drilldown query into initial realtime filters', () => {
    const initialState = {
      ...getDefaultMonitoringCenterUiState(),
      searchInput: 'retained search',
    };
    const state = buildMonitoringInitialStateFromQuery(
      '?from_ms=1780000000000&to_ms=1780003600000&model=gpt-4o&api_key_hash=abcdef1234&status=failed&provider=OpenAI&auth_file=codex-auth.json&project_id=project-1&request_type=codex&search=req-42&min_latency_ms=10000&cache_status=hit',
      initialState
    );

    expect(state).toMatchObject({
      activeDataTab: 'realtime',
      timeRange: 'custom',
      selectedModel: 'gpt-4o',
      selectedApiKeyHash: 'abcdef1234',
      selectedStatus: 'failed',
      selectedProvider: 'OpenAI',
      searchInput: 'req-42',
    });
    expect(state.customStartInput).toBeTruthy();
    expect(state.customEndInput).toBeTruthy();
  });

  it('keeps alternate candidates when a dynamic filter already has a selected value', () => {
    expect(
      buildProviderOptionsFromValues(['codex', 'gemini'], 'codex', t).map((item) => item.value)
    ).toEqual(['all', 'codex', 'gemini']);
    expect(
      buildAccountOptions(
        [createAccountRow('alice@example.com'), createAccountRow('bob@example.com')],
        'alice@example.com',
        t
      ).map((item) => item.value)
    ).toEqual(['all', 'alice@example.com', 'bob@example.com']);
    expect(
      buildModelOptionsFromValues(['gpt-a', 'gpt-b'], 'gpt-a', t).map((item) => item.value)
    ).toEqual(['all', 'gpt-a', 'gpt-b']);
    expect(
      buildChannelOptionsFromValues(['Primary', 'Backup'], 'Primary', t).map((item) => item.value)
    ).toEqual(['all', 'Backup', 'Primary']);
    expect(
      buildApiKeyOptionsFromRows(
        [createApiKeyRow('key-a', 'Key A'), createApiKeyRow('key-b', 'Key B')],
        'key-a',
        t
      ).map((item) => item.value)
    ).toEqual(['all', 'key-a', 'key-b']);
  });

  it('uses account row filter values for account options', () => {
    expect(
      buildAccountOptions(
        [
          createAccountRow('OpenAI Compatible', {
            filterValue: 'auth:openai-auth',
          }),
        ],
        'auth:openai-auth',
        t
      ).map((item) => item.value)
    ).toEqual(['all', 'auth:openai-auth']);
  });
});

describe('monitoringCenterPageModel account quota', () => {
  beforeEach(() => {
    vi.mocked(fetchAntigravityQuota).mockReset();
    vi.mocked(fetchClaudeQuota).mockReset();
    vi.mocked(fetchCodexQuota).mockReset();
    vi.mocked(fetchXaiQuota).mockReset();
  });

  it('maps Claude usage windows into account quota entries', async () => {
    vi.mocked(fetchClaudeQuota).mockResolvedValue({
      windows: [
        {
          id: 'five-hour',
          label: '5-hour limit',
          labelKey: 'claude_quota.five_hour',
          usedPercent: 40,
          resetLabel: '05/20 12:00',
        },
        {
          id: 'weekly-scoped-fable%205%20max',
          label: 'Fable 5 Max',
          usedPercent: 100,
          resetLabel: '05/27 12:00',
        },
      ],
      planType: 'plan_pro',
      extraUsage: {
        is_enabled: true,
        used_credits: 150,
        monthly_limit: 500,
        utilization: null,
      },
    });

    const entry = await requestAccountQuota(createTarget({ provider: 'claude' }), t);

    expect(entry).toMatchObject({
      provider: 'claude',
      providerLabel: 'Claude Quota',
      metaLabels: ['Claude Quota', 'Plan: Pro', 'Extra Usage: $1.50 / $5.00'],
      windows: [
        {
          id: 'five-hour',
          label: '5-hour limit',
          remainingPercent: 60,
          resetLabel: '05/20 12:00',
        },
        {
          id: 'weekly-scoped-fable%205%20max',
          label: 'Fable 5 Max',
          remainingPercent: 0,
          resetLabel: '05/27 12:00',
        },
      ],
    });
  });

  it('maps Codex monthly quota windows into account quota entries', async () => {
    vi.mocked(fetchCodexQuota).mockResolvedValue({
      planType: 'free',
      subscriptionActiveUntil: null,
      rateLimitResetCreditsAvailableCount: null,
      rateLimitResetCredits: [],
      rateLimitResetCreditsError: null,
      windows: [
        {
          id: 'monthly',
          label: 'Monthly limit',
          labelKey: 'codex_quota.monthly_window',
          usedPercent: 5,
          resetLabel: '06/30 12:00',
          limitWindowSeconds: 2_592_000,
        },
      ],
    });

    const entry = await requestAccountQuota(
      createTarget({
        provider: 'codex',
        authIndex: '2',
        fileName: 'codex.json',
      }),
      t
    );

    expect(entry).toMatchObject({
      provider: 'codex',
      providerLabel: 'Codex Quota',
      metaLabels: ['Codex Quota', 'Plan: Free'],
      planType: 'free',
      windows: [
        {
          id: 'monthly',
          label: 'Monthly limit',
          remainingPercent: 95,
          resetLabel: '06/30 12:00',
          usageLabel: '1.5d / 30d used',
        },
      ],
    });
  });

  it('merges observed Codex account quota without dropping existing API-only windows', () => {
    const activeEntry = {
      key: 'codex::2::codex.json',
      provider: 'codex' as const,
      providerLabel: 'Codex Quota',
      authLabel: 'Auth',
      fileName: 'codex.json',
      planType: 'free',
      metaLabels: ['Codex Quota', 'Plan: Free'],
      windows: [
        {
          id: 'monthly',
          label: 'Monthly limit',
          remainingPercent: 95,
          resetLabel: '06/30 12:00',
          usageLabel: '1.5d / 30d used',
        },
        {
          id: 'spark-five-hour-0',
          label: 'spark 5-hour limit',
          remainingPercent: 70,
          resetLabel: '07/01 01:00',
          usageLabel: '1.5h / 5h used',
        },
      ],
    };
    const observedEntry = {
      key: 'codex::2::codex.json',
      provider: 'codex' as const,
      providerLabel: 'Codex Quota',
      authLabel: 'Auth',
      fileName: 'codex.json',
      planType: 'plus',
      metaLabels: ['Codex Quota', 'Plan: Plus', 'Observed from latest usage response headers'],
      windows: [
        {
          id: 'monthly',
          label: 'Monthly limit',
          remainingPercent: 55,
          resetLabel: '07/01 02:00',
          usageLabel: '13.5d / 30d used',
        },
      ],
    };

    const merged = mergeObservedAccountQuotaEntry(activeEntry, observedEntry);

    expect(merged).toMatchObject({
      planType: 'plus',
      metaLabels: ['Codex Quota', 'Plan: Plus', 'Observed from latest usage response headers'],
      windows: [
        {
          id: 'monthly',
          remainingPercent: 55,
          resetLabel: '07/01 02:00',
          usageLabel: '13.5d / 30d used',
        },
        {
          id: 'spark-five-hour-0',
          remainingPercent: 70,
          resetLabel: '07/01 01:00',
          usageLabel: '1.5h / 5h used',
        },
      ],
    });
  });

  it('marks manual quota refresh failures instead of treating cached entries as success', () => {
    const target = createTarget({
      provider: 'codex',
      key: 'codex::2::codex.json',
      authIndex: '2',
      fileName: 'codex.json',
    });
    const activeEntry = {
      key: 'codex::2::codex.json',
      provider: 'codex' as const,
      providerLabel: 'Codex Quota',
      authLabel: 'Auth',
      fileName: 'codex.json',
      planType: 'free',
      metaLabels: ['Codex Quota', 'Plan: Free'],
      windows: [
        {
          id: 'monthly',
          label: 'Monthly limit',
          remainingPercent: 95,
          resetLabel: '06/30 12:00',
          usageLabel: '1.5d / 30d used',
        },
      ],
    };

    const failedEntry = buildAccountQuotaRefreshFailureEntry(
      target,
      '502 bad gateway',
      t,
      activeEntry,
      null
    );

    expect(failedEntry).toMatchObject({
      key: 'codex::2::codex.json',
      error: '502 bad gateway',
      windows: [
        {
          id: 'monthly',
          remainingPercent: 95,
        },
      ],
    });
  });

  it('keeps header-updated fields on manual quota refresh failures', () => {
    const target = createTarget({
      provider: 'codex',
      key: 'codex::2::codex.json',
      authIndex: '2',
      fileName: 'codex.json',
    });
    const activeEntry = {
      key: 'codex::2::codex.json',
      provider: 'codex' as const,
      providerLabel: 'Codex Quota',
      authLabel: 'Auth',
      fileName: 'codex.json',
      planType: 'free',
      metaLabels: ['Codex Quota', 'Plan: Free'],
      windows: [
        {
          id: 'monthly',
          label: 'Monthly limit',
          remainingPercent: 95,
          resetLabel: '06/30 12:00',
          usageLabel: '1.5d / 30d used',
        },
        {
          id: 'spark-five-hour-0',
          label: 'spark 5-hour limit',
          remainingPercent: 70,
          resetLabel: '07/01 01:00',
          usageLabel: '1.5h / 5h used',
        },
      ],
    };
    const observedEntry = {
      key: 'codex::2::codex.json',
      provider: 'codex' as const,
      providerLabel: 'Codex Quota',
      authLabel: 'Auth',
      fileName: 'codex.json',
      planType: 'plus',
      metaLabels: ['Codex Quota', 'Plan: Plus', 'Observed from latest usage response headers'],
      windows: [
        {
          id: 'monthly',
          label: 'Monthly limit',
          remainingPercent: 55,
          resetLabel: '07/01 02:00',
          usageLabel: '13.5d / 30d used',
        },
      ],
    };

    const failedEntry = buildAccountQuotaRefreshFailureEntry(
      target,
      '502 bad gateway',
      t,
      activeEntry,
      observedEntry
    );

    expect(failedEntry).toMatchObject({
      planType: 'plus',
      error: '502 bad gateway',
      windows: [
        {
          id: 'monthly',
          remainingPercent: 55,
          resetLabel: '07/01 02:00',
        },
        {
          id: 'spark-five-hour-0',
          remainingPercent: 70,
          resetLabel: '07/01 01:00',
        },
      ],
    });
  });

  it('keeps API-only windows across repeated manual quota refresh failures', () => {
    const target = createTarget({
      provider: 'codex',
      key: 'codex::2::codex.json',
      authIndex: '2',
      fileName: 'codex.json',
    });
    const activeEntry = {
      key: 'codex::2::codex.json',
      provider: 'codex' as const,
      providerLabel: 'Codex Quota',
      authLabel: 'Auth',
      fileName: 'codex.json',
      planType: 'free',
      metaLabels: ['Codex Quota', 'Plan: Free'],
      windows: [
        {
          id: 'monthly',
          label: 'Monthly limit',
          remainingPercent: 95,
          resetLabel: '06/30 12:00',
          usageLabel: '1.5d / 30d used',
        },
        {
          id: 'spark-five-hour-0',
          label: 'spark 5-hour limit',
          remainingPercent: 70,
          resetLabel: '07/01 01:00',
          usageLabel: '1.5h / 5h used',
        },
      ],
    };
    const firstFailedEntry = buildAccountQuotaRefreshFailureEntry(
      target,
      '502 bad gateway',
      t,
      activeEntry,
      null
    );
    const observedEntry = {
      key: 'codex::2::codex.json',
      provider: 'codex' as const,
      providerLabel: 'Codex Quota',
      authLabel: 'Auth',
      fileName: 'codex.json',
      planType: 'plus',
      metaLabels: ['Codex Quota', 'Plan: Plus', 'Observed from latest usage response headers'],
      windows: [
        {
          id: 'monthly',
          label: 'Monthly limit',
          remainingPercent: 55,
          resetLabel: '07/01 02:00',
          usageLabel: '13.5d / 30d used',
        },
      ],
    };

    const secondFailedEntry = buildAccountQuotaRefreshFailureEntry(
      target,
      '504 timeout',
      t,
      firstFailedEntry,
      observedEntry
    );

    expect(secondFailedEntry.error).toBe('504 timeout');
    expect(secondFailedEntry.windows.map((window) => window.id)).toEqual([
      'monthly',
      'spark-five-hour-0',
    ]);
    expect(secondFailedEntry).toMatchObject({
      planType: 'plus',
      windows: [
        {
          id: 'monthly',
          remainingPercent: 55,
          resetLabel: '07/01 02:00',
        },
        {
          id: 'spark-five-hour-0',
          remainingPercent: 70,
          resetLabel: '07/01 01:00',
        },
      ],
    });
  });

  it('merges older header entries into failed account quota state without clearing the failure', () => {
    const target = createTarget({
      provider: 'codex',
      key: 'codex::2::codex.json',
      authIndex: '2',
      fileName: 'codex.json',
    });
    const state = {
      status: 'error' as const,
      targetKey: 'codex::2::codex.json',
      error: '502 bad gateway',
      failedAtMs: 2_000,
      entries: [
        {
          key: 'codex::2::codex.json',
          provider: 'codex' as const,
          providerLabel: 'Codex Quota',
          authLabel: 'Auth',
          fileName: 'codex.json',
          planType: 'free',
          metaLabels: ['Codex Quota', 'Plan: Free'],
          error: '502 bad gateway',
          failedAtMs: 2_000,
          windows: [
            {
              id: 'monthly',
              label: 'Monthly limit',
              remainingPercent: 95,
              resetLabel: '06/30 12:00',
              usageLabel: '1.5d / 30d used',
            },
            {
              id: 'spark-five-hour-0',
              label: 'spark 5-hour limit',
              remainingPercent: 70,
              resetLabel: '07/01 01:00',
              usageLabel: '1.5h / 5h used',
            },
          ],
        },
      ],
    };
    const observedEntry = {
      key: 'codex::2::codex.json',
      provider: 'codex' as const,
      providerLabel: 'Codex Quota',
      authLabel: 'Auth',
      fileName: 'codex.json',
      planType: 'plus',
      metaLabels: ['Codex Quota', 'Plan: Plus', 'Observed from latest usage response headers'],
      observedAtMs: 1_000,
      observedFromUsageHeaders: true,
      windows: [
        {
          id: 'monthly',
          label: 'Monthly limit',
          remainingPercent: 55,
          resetLabel: '07/01 02:00',
          usageLabel: '13.5d / 30d used',
        },
      ],
    };

    const merged = mergeObservedAccountQuotaState(state, [target], [observedEntry]);

    expect(merged).not.toBe(state);
    expect(merged).toMatchObject({
      status: 'error',
      error: '502 bad gateway',
      failedAtMs: 2_000,
      entries: [
        {
          planType: 'plus',
          error: '502 bad gateway',
          failedAtMs: 2_000,
          windows: [
            {
              id: 'monthly',
              remainingPercent: 55,
              resetLabel: '07/01 02:00',
            },
            {
              id: 'spark-five-hour-0',
              remainingPercent: 70,
              resetLabel: '07/01 01:00',
            },
          ],
        },
      ],
    });
  });

  it('recovers failed account quota state with newer header entries', () => {
    const target = createTarget({
      provider: 'codex',
      key: 'codex::2::codex.json',
      authIndex: '2',
      fileName: 'codex.json',
    });
    const state = {
      status: 'error' as const,
      targetKey: 'codex::2::codex.json',
      error: '502 bad gateway',
      failedAtMs: 1_000,
      entries: [
        {
          key: 'codex::2::codex.json',
          provider: 'codex' as const,
          providerLabel: 'Codex Quota',
          authLabel: 'Auth',
          fileName: 'codex.json',
          planType: 'free',
          metaLabels: ['Codex Quota', 'Plan: Free'],
          error: '502 bad gateway',
          failedAtMs: 1_000,
          windows: [
            {
              id: 'monthly',
              label: 'Monthly limit',
              remainingPercent: 95,
              resetLabel: '06/30 12:00',
              usageLabel: '1.5d / 30d used',
            },
            {
              id: 'spark-five-hour-0',
              label: 'spark 5-hour limit',
              remainingPercent: 70,
              resetLabel: '07/01 01:00',
              usageLabel: '1.5h / 5h used',
            },
          ],
        },
      ],
    };
    const observedEntry = {
      key: 'codex::2::codex.json',
      provider: 'codex' as const,
      providerLabel: 'Codex Quota',
      authLabel: 'Auth',
      fileName: 'codex.json',
      planType: 'plus',
      metaLabels: ['Codex Quota', 'Plan: Plus', 'Observed from latest usage response headers'],
      observedAtMs: 2_000,
      observedFromUsageHeaders: true,
      windows: [
        {
          id: 'monthly',
          label: 'Monthly limit',
          remainingPercent: 55,
          resetLabel: '07/01 02:00',
          usageLabel: '13.5d / 30d used',
        },
      ],
    };

    const merged = mergeObservedAccountQuotaState(state, [target], [observedEntry]);

    expect(merged).not.toBe(state);
    expect(merged).toMatchObject({
      status: 'success',
      error: '',
      failedAtMs: undefined,
      entries: [
        {
          planType: 'plus',
          observedAtMs: 2_000,
          observedFromUsageHeaders: true,
          windows: [
            {
              id: 'monthly',
              remainingPercent: 55,
              resetLabel: '07/01 02:00',
            },
            {
              id: 'spark-five-hour-0',
              remainingPercent: 70,
              resetLabel: '07/01 01:00',
            },
          ],
        },
      ],
    });
    expect(merged?.entries[0].error).toBeUndefined();
    expect(merged?.entries[0].failedAtMs).toBeUndefined();
  });

  it('does not merge later header entries when the account quota target set changed', () => {
    const target = createTarget({
      provider: 'codex',
      key: 'codex::2::codex.json',
      authIndex: '2',
      fileName: 'codex.json',
    });
    const state = {
      status: 'error' as const,
      targetKey: 'codex::1::old.json',
      error: '502 bad gateway',
      entries: [],
    };
    const observedEntry = {
      key: 'codex::2::codex.json',
      provider: 'codex' as const,
      providerLabel: 'Codex Quota',
      authLabel: 'Auth',
      fileName: 'codex.json',
      planType: 'plus',
      metaLabels: ['Codex Quota', 'Plan: Plus'],
      windows: [
        {
          id: 'monthly',
          label: 'Monthly limit',
          remainingPercent: 55,
          resetLabel: '07/01 02:00',
          usageLabel: '13.5d / 30d used',
        },
      ],
    };

    expect(mergeObservedAccountQuotaState(state, [target], [observedEntry])).toBe(state);
  });

  it('maps Antigravity grouped buckets into account quota entries', async () => {
    vi.mocked(fetchAntigravityQuota).mockResolvedValue({
      serverTimeOffsetMs: null,
      groups: [
        {
          id: 'agent',
          label: 'Agent',
          buckets: [
            {
              id: 'daily',
              label: 'Daily',
              window: '24h',
              remainingFraction: 0.25,
              resetTime: undefined,
            },
            {
              id: 'weekly',
              label: 'Weekly',
              window: '7d',
              remainingFraction: 0.5,
              resetTime: undefined,
            },
          ],
        },
      ],
    });

    const entry = await requestAccountQuota(
      createTarget({
        provider: 'antigravity',
        authIndex: '2',
        fileName: 'antigravity.json',
      }),
      t
    );

    expect(entry.metaLabels).toEqual(['Antigravity Quota']);
    expect(entry.windows).toMatchObject([
      {
        id: 'agent',
        label: 'Agent',
        remainingPercent: 25,
        resetLabel: '-',
        usageLabel: null,
      },
    ]);
  });

  it('maps Kimi quota rows without amount labels in account quota entries', async () => {
    vi.mocked(fetchKimiQuota).mockResolvedValue([
      {
        id: 'daily',
        label: 'Daily',
        used: 25,
        limit: 100,
        resetHint: '2026-07-31T00:00:00Z',
      },
    ]);

    const entry = await requestAccountQuota(
      createTarget({
        provider: 'kimi',
        authIndex: '4',
        fileName: 'kimi.json',
      }),
      t
    );

    expect(entry).toMatchObject({
      provider: 'kimi',
      providerLabel: 'Kimi Quota',
      windows: [
        {
          id: 'daily',
          label: 'Daily',
          remainingPercent: 75,
          usageLabel: null,
        },
      ],
    });
  });

  it('maps xAI billing into account quota entries', async () => {
    vi.mocked(fetchXaiQuota).mockResolvedValue({
      periodType: 'monthly',
      usagePercent: 100,
      productUsage: [],
      monthlyLimitCents: 10000,
      usedCents: 12500,
      includedUsedCents: 10000,
      onDemandCapCents: 5000,
      onDemandUsedCents: 2500,
      onDemandUsedPercent: 50,
      billingPeriodStart: '2026-05-01T00:00:00Z',
      billingPeriodEnd: '2026-06-01T00:00:00Z',
      usedPercent: 100,
    });

    const entry = await requestAccountQuota(
      createTarget({
        provider: 'xai',
        authIndex: '3',
        fileName: 'xai.json',
      }),
      t
    );

    expect(entry).toMatchObject({
      provider: 'xai',
      providerLabel: 'xAI Quota',
      metaLabels: ['xAI Quota', 'On-demand cap: $50.00'],
      windows: [
        {
          id: 'monthly-limit',
          label: 'Monthly credits',
          remainingPercent: 0,
          usageLabel: '$0.00 / $100.00 remaining',
        },
        {
          id: 'pay-as-you-go',
          label: 'Pay-as-you-go',
          remainingPercent: 50,
          usageLabel: '$25.00 / $50.00 remaining',
        },
      ],
    });
  });

  it('maps xAI weekly and product usage into account quota entries', async () => {
    vi.mocked(fetchXaiQuota).mockResolvedValue({
      periodType: 'weekly',
      usagePercent: 40,
      periodStart: '2026-07-01T00:00:00Z',
      periodEnd: '2026-07-08T00:00:00Z',
      productUsage: [{ product: 'Grok 4', usagePercent: 25 }],
      monthlyLimitCents: 10000,
      usedCents: 2500,
      includedUsedCents: 2500,
      onDemandCapCents: null,
      onDemandUsedCents: null,
      onDemandUsedPercent: null,
      billingPeriodStart: '2026-07-01T00:00:00Z',
      billingPeriodEnd: '2026-08-01T00:00:00Z',
      usedPercent: 25,
    });

    const entry = await requestAccountQuota(
      createTarget({
        provider: 'xai',
        authIndex: '3',
        fileName: 'xai.json',
      }),
      t
    );

    expect(entry).toMatchObject({
      provider: 'xai',
      providerLabel: 'xAI Quota',
      windows: [
        {
          id: 'weekly-limit',
          label: 'Weekly limit',
          remainingPercent: 60,
          usageLabel: 'Used 40%',
        },
        {
          id: 'product-0-Grok 4',
          label: 'Grok 4 usage',
          remainingPercent: 75,
          usageLabel: 'Used 25%',
        },
        {
          id: 'monthly-limit',
          label: 'Monthly credits',
          remainingPercent: 75,
          usageLabel: '$75.00 / $100.00 remaining',
        },
      ],
    });
  });

  it('maps official API health without synthesizing quota windows', async () => {
    vi.mocked(fetchXaiQuota).mockResolvedValue({
      periodType: 'unknown',
      usagePercent: null,
      productUsage: [],
      monthlyLimitCents: null,
      usedCents: null,
      includedUsedCents: null,
      onDemandCapCents: null,
      onDemandUsedCents: null,
      onDemandUsedPercent: null,
      usedPercent: null,
      officialApiHealth: {
        source: 'api.x.ai/v1/me',
        userId: 'user-1',
        teamId: 'team-1',
        teamBlocked: false,
      },
    });

    const entry = await requestAccountQuota(
      createTarget({ provider: 'xai', authIndex: '3', fileName: 'paid-xai.json' }),
      t
    );

    expect(entry).toMatchObject({
      provider: 'xai',
      metaLabels: [
        'xAI Quota',
        'Official xAI API identity is reachable. Billing and remaining quota are unavailable for this OAuth credential.',
      ],
      windows: [],
    });
  });

  it('maps partial xAI billing diagnostics to user-facing explanations', async () => {
    vi.mocked(fetchXaiQuota).mockResolvedValue({
      periodType: 'monthly',
      usagePercent: null,
      productUsage: [],
      monthlyLimitCents: 10_000,
      usedCents: 2_500,
      includedUsedCents: 2_500,
      onDemandCapCents: null,
      onDemandUsedCents: null,
      onDemandUsedPercent: null,
      usedPercent: 25,
      partial: true,
      diagnostics: [
        {
          classification: 'protocol_changed',
          statusCode: 200,
          message: 'xAI billing response schema changed',
        },
      ],
    });

    const entry = await requestAccountQuota(
      createTarget({
        provider: 'xai',
        authIndex: '3',
        fileName: 'xai.json',
      }),
      t
    );

    const metaLabels = entry.metaLabels ?? [];
    expect(metaLabels).toContain(
      'Some billing data is unavailable. Reason: The billing endpoint returned data that cannot currently be recognized'
    );
    expect(metaLabels.join(' ')).not.toContain('protocol_changed');
    expect(metaLabels.join(' ')).not.toContain('HTTP 200');
  });
});
