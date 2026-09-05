import { describe, expect, it } from 'vitest';
import type { AuthFileItem, CodexQuotaState, XaiQuotaState } from '@/types';
import type {
  AccountActionCandidate,
  MonitoringAnalyticsEventRow,
  MonitoringAnalyticsRecentFailure,
  MonitoringAnalyticsSummary,
  MonitoringAccountHistoryItem,
  MonitoringAccountWindowUsageItem,
} from '@/services/api';
import type { AccountRow } from './accountRows';
import {
  buildAccountDetailViewModel,
  type AccountDetailQuotaWindowInput,
} from './accountDetailViewModel';
import { accountWindowUsageRequestKey } from './accountWindowUsageRows';
import type { UsageValueRow } from './usageValueRows';

const CODEX_MAIN_SCOPE = { kind: 'family', key: 'codex_main', complete: true } as const;

type AccountRowOverrides = Omit<Partial<AccountRow>, 'quota'> & {
  quota?: Partial<AccountRow['quota']>;
};

const makeRow = (overrides: AccountRowOverrides = {}): AccountRow => {
  const { quota: quotaOverrides, ...rowOverrides } = overrides;
  const raw: AuthFileItem = {
    name: overrides.fileName ?? 'shared.codex.json',
    type: overrides.provider ?? 'codex',
    provider: overrides.provider ?? 'codex',
    authIndex: overrides.authIndex ?? '0',
    account: overrides.accountLabel ?? 'codex@example.com',
  };

  return {
    key: raw.name,
    selectionKey: `${raw.name}\u0000${overrides.authIndex ?? '0'}`,
    fileName: raw.name,
    accountLabel: String(raw.account),
    provider: 'codex',
    planType: 'plus',
    disabled: false,
    runtimeOnly: false,
    statusMessage: '',
    authIndex: '0',
    projectId: '',
    priority: 0,
    createdAtMs: null,
    updatedAtMs: null,
    authenticationAtMs: 0,
    rawCredentialStatusSuperseded: false,
    quota: {
      status: 'ok',
      remainingPercent: 80,
      usedPercent: 20,
      resetLabel: 'later',
      resetAtMs: null,
      resetAccuracy: 'unknown',
      planType: 'plus',
      source: 'cache',
      ...quotaOverrides,
    },
    usage: {
      success: 9,
      failure: 1,
      successRate: 90,
      recentRequests: [],
    },
    inspection: null,
    raw,
    ...rowOverrides,
  };
};

const makeCandidate = (
  overrides: Partial<AccountActionCandidate> = {}
): AccountActionCandidate => ({
  id: 1,
  actionType: 'reauth',
  status: 'pending',
  provider: 'codex',
  authFileName: 'shared.codex.json',
  authIndex: '0',
  accountSnapshot: 'codex@example.com',
  authLabel: 'codex@example.com',
  reason: 'expired',
  firstSeenAtMs: 100,
  lastSeenAtMs: 200,
  hitCount: 1,
  createdAtMs: 100,
  updatedAtMs: 200,
  ...overrides,
});

const makeWindowUsage = (
  overrides: Partial<MonitoringAccountWindowUsageItem> = {}
): MonitoringAccountWindowUsageItem => ({
  row_key: 'shared.codex.json\u00000',
  window_key: 'weekly',
  from_ms: 1000,
  to_ms: 2000,
  matched: true,
  total_requests: 10,
  success_calls: 9,
  failure_calls: 1,
  total_tokens: 1200,
  total_cost: 0.42,
  success_rate: 0.9,
  last_seen_ms: 1900,
  sync_status: 'ready',
  ...overrides,
});

const makeForecastWindow = (
  overrides: Partial<AccountDetailQuotaWindowInput> = {}
): AccountDetailQuotaWindowInput => ({
  key: 'weekly',
  providerWindowId: 'weekly',
  label: 'Weekly',
  kind: 'weekly',
  remainingPercent: 100,
  usedPercent: 0,
  resetLabel: 'later',
  resetAtMs: 10_000,
  resetAccuracy: 'exact',
  observedAtMs: 2_000,
  quotaProgressObservedAtMs: 2_000,
  limitWindowSeconds: 7 * 24 * 60 * 60,
  windowMode: 'fixed',
  cycleStartMs: 1_000,
  cycleEndMs: 10_000,
  modelScope: { kind: 'all', complete: true },
  ...overrides,
});

const buildForecastWindow = (
  options: {
    current?: Partial<MonitoringAccountWindowUsageItem>;
    previous?: Partial<MonitoringAccountWindowUsageItem>;
    quota?: Partial<AccountDetailQuotaWindowInput>;
  } = {}
) => {
  const row = makeRow({ provider: 'antigravity' });
  const quotaWindow = makeForecastWindow(options.quota);
  const providerWindowId = quotaWindow.providerWindowId ?? quotaWindow.key;
  const windowUsageByKey = new Map<string, MonitoringAccountWindowUsageItem>();

  if (options.current) {
    windowUsageByKey.set(
      accountWindowUsageRequestKey(
        row.selectionKey,
        providerWindowId,
        'current',
        quotaWindow.modelScope
      ),
      makeWindowUsage({ window_key: providerWindowId, ...options.current })
    );
  }
  if (options.previous) {
    windowUsageByKey.set(
      accountWindowUsageRequestKey(
        row.selectionKey,
        providerWindowId,
        'previous',
        quotaWindow.modelScope
      ),
      makeWindowUsage({ window_key: providerWindowId, ...options.previous })
    );
  }

  return buildAccountDetailViewModel(row, {
    quotaWindows: [quotaWindow],
    windowUsageByKey,
  }).quota.windows[0];
};

const makeHistory = (
  overrides: Partial<MonitoringAccountHistoryItem> = {}
): MonitoringAccountHistoryItem => ({
  row_key: 'shared.codex.json\u00000',
  account_key: 'codex@example.com',
  matched: true,
  total_requests: 12,
  success_calls: 10,
  failure_calls: 2,
  total_tokens: 2400,
  total_cost: 0.84,
  success_rate: 10 / 12,
  first_seen_ms: 100,
  last_seen_ms: 200,
  sync_status: 'ready',
  ...overrides,
});

const makeAnalyticsEvent = (
  overrides: Partial<MonitoringAnalyticsEventRow> = {}
): MonitoringAnalyticsEventRow => ({
  request_id: 'req-1',
  event_hash: 'event-1',
  timestamp_ms: 2000,
  model: 'gpt-5',
  endpoint: '/v1/responses',
  method: 'POST',
  path: '/v1/responses',
  auth_index: '0',
  source: 'shared.codex.json',
  source_hash: 'source-hash',
  api_key_hash: 'api-key-hash',
  account_snapshot: 'codex@example.com',
  auth_label_snapshot: 'codex@example.com',
  auth_file_snapshot: 'shared.codex.json',
  auth_provider_snapshot: 'codex',
  input_tokens: 10,
  output_tokens: 5,
  cached_tokens: 0,
  cache_read_tokens: 0,
  cache_creation_tokens: 0,
  reasoning_tokens: 0,
  total_tokens: 15,
  latency_ms: 120,
  failed: false,
  ...overrides,
});

const makeMonitoringValue = (
  row: AccountRow,
  overrides: Partial<UsageValueRow> = {}
): UsageValueRow => ({
  key: `monitoring:${row.selectionKey}`,
  accountLabel: row.accountLabel,
  fileName: row.fileName,
  provider: row.provider,
  requests: 7,
  successRate: 85.7,
  inputTokens: 800,
  outputTokens: 400,
  estimatedCost: 0.42,
  lastSeenMs: 1_700_000_000_000,
  rating: 'normal',
  source: 'monitoring',
  row,
  ...overrides,
});

describe('accountDetailViewModel', () => {
  it('uses the full unified plan label for credential details', () => {
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        provider: 'codex',
        planType: 'self_serve_business_prolite',
      })
    );

    expect(viewModel.identity.planPresentation).toMatchObject({
      rawPlanType: 'self_serve_business_prolite',
      canonicalPlanType: 'business_premium_5x',
      shortLabel: 'Business 5x',
      fullLabel: 'Business Premium 5x',
      known: true,
    });
    expect(viewModel.overview.credential.fields).toContainEqual(
      expect.objectContaining({
        key: 'planType',
        value: 'Business Premium 5x',
      })
    );
  });

  it('preserves unknown plan casing in credential details', () => {
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        provider: 'antigravity',
        planType: 'Antigravity Future',
        quota: { planType: 'Antigravity Future' },
      })
    );

    expect(viewModel.identity.planPresentation).toMatchObject({
      rawPlanType: 'Antigravity Future',
      canonicalPlanType: 'unknown:antigravity:antigravity future',
      shortLabel: 'Antigravity Future',
      fullLabel: 'Antigravity Future',
      known: false,
    });
    expect(viewModel.overview.credential.fields).toContainEqual(
      expect.objectContaining({
        key: 'planType',
        value: 'Antigravity Future',
      })
    );
  });

  it('keeps credential identity fields focused and hides missing values', () => {
    const populated = buildAccountDetailViewModel(
      makeRow({
        authIndex: 'auth-1',
        projectId: 'project-1',
        provider: 'xai',
        planType: 'pro',
        priority: 0,
      })
    );
    const sparse = buildAccountDetailViewModel(
      makeRow({ authIndex: '', projectId: '', planType: null, priority: null })
    );

    expect(populated.auth.fields.map((field) => field.key)).toEqual([
      'authIndex',
      'projectId',
      'runtime',
    ]);
    expect(sparse.auth.fields.map((field) => field.key)).toEqual(['runtime']);
  });

  it('matches window usage and action candidates by file name plus auth index', () => {
    const row = makeRow({
      selectionKey: 'shared.codex.json\u00001',
      authIndex: '1',
      accountLabel: 'second@example.com',
      raw: {
        name: 'shared.codex.json',
        type: 'codex',
        provider: 'codex',
        authIndex: '1',
        account: 'second@example.com',
      } as AuthFileItem,
    });
    const windowUsageByKey = new Map<string, MonitoringAccountWindowUsageItem>([
      [
        accountWindowUsageRequestKey('shared.codex.json\u00000', 'rate_limit:weekly'),
        makeWindowUsage({
          row_key: 'shared.codex.json\u00000',
          total_requests: 11,
          total_cost: 0.11,
        }),
      ],
      [
        accountWindowUsageRequestKey('shared.codex.json\u00001', 'rate_limit:weekly'),
        makeWindowUsage({
          row_key: 'shared.codex.json\u00001',
          total_requests: 22,
          total_cost: 0.22,
        }),
      ],
    ]);

    const viewModel = buildAccountDetailViewModel(row, {
      quotaWindows: [
        {
          key: 'weekly',
          providerWindowId: 'rate_limit:weekly',
          label: 'Weekly',
          kind: 'weekly',
          remainingPercent: 40,
          usedPercent: 60,
          resetLabel: 'later',
          fromMs: 1000,
          toMs: 2000,
          amountLabel: '40 / 100',
          groupLabel: 'Gemini models',
          description: 'Weekly shared model quota',
        },
      ],
      windowUsageByKey,
      actionCandidates: [
        makeCandidate({ id: 1, authIndex: '0', reason: 'first account' }),
        makeCandidate({
          id: 2,
          authIndex: '1',
          reasonCode: 'invalid_credentials',
          reason: 'second account',
          lastSeenAtMs: 300,
        }),
        makeCandidate({ id: 3, authIndex: undefined, reason: 'file-level fallback' }),
      ],
    });

    expect(viewModel.quota.windows[0].usage?.totalRequests).toBe(22);
    expect(viewModel.quota.windows[0].usage?.totalCost).toBe(0.22);
    expect(viewModel.quota.windows[0]).toMatchObject({
      providerWindowId: 'rate_limit:weekly',
      kind: 'weekly',
      amountLabel: '40 / 100',
      groupLabel: 'Gemini models',
      description: 'Weekly shared model quota',
    });
    expect(viewModel.strategy.actionCandidates).toHaveLength(1);
    expect(viewModel.strategy.actionCandidates[0]).toMatchObject({
      id: 2,
      reasonCode: 'invalid_credentials',
      reason: 'second account',
    });
  });

  it('estimates usage for calendar windows with reliable boundaries', () => {
    const row = makeRow();
    const nowMs = Date.now();
    const windowUsageByKey = new Map<string, MonitoringAccountWindowUsageItem>([
      [
        accountWindowUsageRequestKey(row.selectionKey, 'calendar-week', 'current'),
        makeWindowUsage({
          window_key: 'calendar-week',
          total_requests: 50,
          total_tokens: 500_000,
          total_cost: 5,
        }),
      ],
    ]);

    const viewModel = buildAccountDetailViewModel(row, {
      quotaWindows: [
        {
          key: 'calendar-week',
          providerWindowId: 'calendar-week',
          label: 'Calendar week',
          kind: 'weekly',
          remainingPercent: 50,
          usedPercent: 50,
          resetLabel: 'later',
          resetAtMs: nowMs + 60 * 60 * 1000,
          resetAccuracy: 'exact',
          observedAtMs: nowMs,
          quotaProgressObservedAtMs: nowMs,
          limitWindowSeconds: 7 * 24 * 60 * 60,
          windowMode: 'calendar',
          cycleStartMs: nowMs - 60 * 60 * 1000,
          cycleEndMs: nowMs + 60 * 60 * 1000,
          modelScope: { kind: 'all', complete: true },
        },
      ],
      windowUsageByKey,
    });

    expect(viewModel.quota.windows[0].forecast).toMatchObject({
      basis: 'quota',
      requests: 100,
      tokens: 1_000_000,
      cost: 10,
    });
  });

  it('keeps previous usage and forecast for model-scoped windows', () => {
    const row = makeRow({ provider: 'antigravity' });
    const nowMs = Date.now();
    const modelScope = { kind: 'family' as const, key: 'gemini', complete: true };
    const currentKey = accountWindowUsageRequestKey(
      row.selectionKey,
      'antigravity-gemini',
      'current',
      modelScope
    );
    const previousKey = accountWindowUsageRequestKey(
      row.selectionKey,
      'antigravity-gemini',
      'previous',
      modelScope
    );
    const windowUsageByKey = new Map<string, MonitoringAccountWindowUsageItem>([
      [
        currentKey,
        makeWindowUsage({
          window_key: 'antigravity-gemini',
          from_ms: nowMs - 60 * 60 * 1000,
          to_ms: nowMs,
          total_requests: 2,
          total_tokens: 6_400,
          total_cost: 0.02,
        }),
      ],
      [
        previousKey,
        makeWindowUsage({
          window_key: 'antigravity-gemini',
          from_ms: nowMs - 25 * 60 * 60 * 1000,
          to_ms: nowMs - 24 * 60 * 60 * 1000,
          total_requests: 20,
          total_tokens: 200_000,
          total_cost: 2,
        }),
      ],
    ]);

    const viewModel = buildAccountDetailViewModel(row, {
      quotaWindows: [
        {
          key: 'antigravity-gemini',
          providerWindowId: 'antigravity-gemini',
          label: 'Gemini',
          kind: 'daily',
          remainingPercent: 99,
          usedPercent: 1,
          resetLabel: 'later',
          resetAtMs: nowMs + 24 * 60 * 60 * 1000,
          resetAccuracy: 'exact',
          observedAtMs: nowMs,
          quotaProgressObservedAtMs: nowMs,
          limitWindowSeconds: 24 * 60 * 60,
          windowMode: 'fixed',
          cycleStartMs: nowMs - 60 * 60 * 1000,
          cycleEndMs: nowMs + 23 * 60 * 60 * 1000,
          modelScope,
        },
      ],
      windowUsageByKey,
    });

    expect(viewModel.quota.windows[0].previousUsage).toMatchObject({
      matched: true,
      totalRequests: 20,
      totalTokens: 200_000,
    });
    expect(viewModel.quota.windows[0].forecast).toMatchObject({
      basis: 'quota',
      requests: 200,
      tokens: 640_000,
      cost: 2,
    });
  });

  it('does not use partial model-scope usage as a forecast basis', () => {
    const row = makeRow({ provider: 'antigravity' });
    const nowMs = Date.now();
    const modelScope = { kind: 'family' as const, key: 'gemini', complete: true };
    const currentKey = accountWindowUsageRequestKey(
      row.selectionKey,
      'antigravity-gemini',
      'current',
      modelScope
    );
    const previousKey = accountWindowUsageRequestKey(
      row.selectionKey,
      'antigravity-gemini',
      'previous',
      modelScope
    );
    const windowUsageByKey = new Map<string, MonitoringAccountWindowUsageItem>([
      [
        currentKey,
        makeWindowUsage({
          window_key: 'antigravity-gemini',
          total_requests: 2,
          total_tokens: 6_400,
          total_cost: 0.02,
          last_seen_ms: nowMs,
          scope_match_status: 'partial',
          unmatched_requests: 1,
        }),
      ],
      [
        previousKey,
        makeWindowUsage({
          window_key: 'antigravity-gemini',
          total_requests: 20,
          total_tokens: 200_000,
          total_cost: 2,
          scope_match_status: 'partial',
          unmatched_requests: 2,
        }),
      ],
    ]);

    const viewModel = buildAccountDetailViewModel(row, {
      quotaWindows: [
        {
          key: 'antigravity-gemini',
          providerWindowId: 'antigravity-gemini',
          label: 'Gemini',
          kind: 'daily',
          remainingPercent: 99,
          usedPercent: 1,
          resetLabel: 'later',
          resetAtMs: nowMs + 24 * 60 * 60 * 1000,
          resetAccuracy: 'exact',
          observedAtMs: nowMs,
          limitWindowSeconds: 24 * 60 * 60,
          windowMode: 'fixed',
          cycleStartMs: nowMs - 60 * 60 * 1000,
          cycleEndMs: nowMs + 23 * 60 * 60 * 1000,
          modelScope,
        },
      ],
      windowUsageByKey,
    });

    expect(viewModel.quota.windows[0].currentUsage).toMatchObject({
      matched: true,
      scopeMatchStatus: 'partial',
      unmatchedRequests: 1,
    });
    expect(viewModel.quota.windows[0].previousUsage).toMatchObject({
      matched: true,
      scopeMatchStatus: 'partial',
      unmatchedRequests: 2,
    });
    expect(viewModel.quota.windows[0].forecast).toBeNull();
  });

  it('does not forecast an incomplete quota scope even when cached usage is present', () => {
    const row = makeRow({ provider: 'codex' });
    const nowMs = Date.now();
    const modelScope = { kind: 'feature' as const, key: 'future_feature', complete: false };
    const currentKey = accountWindowUsageRequestKey(
      row.selectionKey,
      'future-feature-weekly-0',
      'current',
      modelScope
    );
    const previousKey = accountWindowUsageRequestKey(
      row.selectionKey,
      'future-feature-weekly-0',
      'previous',
      modelScope
    );
    const windowUsageByKey = new Map<string, MonitoringAccountWindowUsageItem>([
      [
        currentKey,
        makeWindowUsage({
          window_key: 'future-feature-weekly-0',
          total_requests: 250,
          total_tokens: 29_000_000,
          total_cost: 21.23,
        }),
      ],
      [
        previousKey,
        makeWindowUsage({
          window_key: 'future-feature-weekly-0',
          total_requests: 250,
          total_tokens: 29_000_000,
          total_cost: 21.23,
        }),
      ],
    ]);

    const viewModel = buildAccountDetailViewModel(row, {
      quotaWindows: [
        {
          key: 'future-feature-weekly-0',
          providerWindowId: 'future-feature-weekly-0',
          label: 'Future Feature',
          kind: 'weekly',
          remainingPercent: 100,
          usedPercent: 0,
          resetLabel: 'later',
          resetAtMs: nowMs + 7 * 24 * 60 * 60 * 1000,
          resetAccuracy: 'exact',
          observedAtMs: nowMs,
          limitWindowSeconds: 7 * 24 * 60 * 60,
          windowMode: 'fixed',
          cycleStartMs: nowMs - 60 * 60 * 1000,
          cycleEndMs: nowMs + 7 * 24 * 60 * 60 * 1000,
          modelScope,
        },
      ],
      windowUsageByKey,
    });

    expect(viewModel.quota.windows[0].forecast).toBeNull();
  });

  it('does not fall back to an eligible previous cycle when usage is newer than quota observation', () => {
    const row = makeRow({ provider: 'antigravity' });
    const nowMs = Date.now();
    const modelScope = { kind: 'family' as const, key: 'gemini', complete: true };
    const currentKey = accountWindowUsageRequestKey(
      row.selectionKey,
      'antigravity-gemini',
      'current',
      modelScope
    );
    const previousKey = accountWindowUsageRequestKey(
      row.selectionKey,
      'antigravity-gemini',
      'previous',
      modelScope
    );
    const windowUsageByKey = new Map<string, MonitoringAccountWindowUsageItem>([
      [
        currentKey,
        makeWindowUsage({
          window_key: 'antigravity-gemini',
          total_requests: 1_300,
          total_tokens: 181_600_000,
          total_cost: 6.95,
          last_seen_ms: nowMs,
        }),
      ],
      [
        previousKey,
        makeWindowUsage({
          window_key: 'antigravity-gemini',
          total_requests: 20,
          total_tokens: 200_000,
          total_cost: 2,
        }),
      ],
    ]);

    const viewModel = buildAccountDetailViewModel(row, {
      quotaWindows: [
        {
          key: 'antigravity-gemini',
          providerWindowId: 'antigravity-gemini',
          label: 'Gemini',
          kind: 'daily',
          remainingPercent: 99,
          usedPercent: 1,
          resetLabel: 'later',
          resetAtMs: nowMs + 24 * 60 * 60 * 1000,
          resetAccuracy: 'exact',
          observedAtMs: nowMs - 1_000,
          quotaProgressObservedAtMs: nowMs - 1_000,
          limitWindowSeconds: 24 * 60 * 60,
          windowMode: 'fixed',
          cycleStartMs: nowMs - 60 * 60 * 1000,
          cycleEndMs: nowMs + 23 * 60 * 60 * 1000,
          modelScope,
          availability: 'active',
          currentCycle: {
            id: 2,
            activationId: 1,
            state: 'active',
            scheduledStartMs: nowMs - 60 * 60 * 1000,
            scheduledEndMs: nowMs + 23 * 60 * 60 * 1000,
            actualStartMs: nowMs - 60 * 60 * 1000,
            actualEndMs: null,
            durationSeconds: 24 * 60 * 60,
            boundaryAccuracy: 'exact',
            endReason: '',
            parentCycleId: null,
            forecastEligible: true,
          },
          previousCycle: {
            id: 1,
            activationId: 1,
            state: 'closed',
            scheduledStartMs: nowMs - 25 * 60 * 60 * 1000,
            scheduledEndMs: nowMs - 24 * 60 * 60 * 1000,
            actualStartMs: nowMs - 25 * 60 * 60 * 1000,
            actualEndMs: nowMs - 24 * 60 * 60 * 1000,
            durationSeconds: 24 * 60 * 60,
            boundaryAccuracy: 'exact',
            endReason: 'scheduled',
            parentCycleId: null,
            forecastEligible: true,
          },
        },
      ],
      windowUsageByKey,
    });

    expect(viewModel.quota.windows[0].currentUsage).toMatchObject({
      totalRequests: 1_300,
      totalTokens: 181_600_000,
      totalCost: 6.95,
      lastSeenMs: nowMs,
    });
    expect(viewModel.quota.windows[0].forecast).toBeNull();
  });

  it('resumes quota projection once the quota progress observation catches up with current usage', () => {
    const current = {
      total_requests: 1_300,
      total_tokens: 181_600_000,
      total_cost: 6.95,
      last_seen_ms: 1_500,
    };
    const previous = {
      total_requests: 149,
      total_tokens: 20_100_000,
      total_cost: 0.68,
    };

    const delayed = buildForecastWindow({
      current,
      previous,
      quota: { usedPercent: 66, observedAtMs: 2_000, quotaProgressObservedAtMs: 1_499 },
    });
    expect(delayed.forecast).toBeNull();

    const recovered = buildForecastWindow({
      current,
      previous,
      quota: { usedPercent: 66, observedAtMs: 2_000, quotaProgressObservedAtMs: 1_500 },
    });
    expect(recovered.forecast).toEqual({
      requests: 1_970,
      tokens: 275_151_515,
      cost: 10.53,
      basis: 'quota',
    });
    expect(recovered.forecast?.requests).toBeGreaterThan(current.total_requests);
    expect(recovered.forecast?.tokens).toBeGreaterThan(current.total_tokens);
    expect(recovered.forecast?.cost).toBeGreaterThanOrEqual(current.total_cost);
  });

  it('rejects previous fallback when current usage is ahead of quota progress provenance', () => {
    const window = buildForecastWindow({
      current: {
        total_requests: 100,
        total_tokens: 1_000_000,
        total_cost: 5,
        last_seen_ms: 1_500,
      },
      previous: {
        total_requests: 200,
        total_tokens: 2_000_000,
        total_cost: 10,
      },
      quota: {
        usedPercent: 50,
        observedAtMs: 2_000,
        quotaProgressObservedAtMs: 1_000,
      },
    });

    expect(window.forecast).toBeNull();
  });

  it('does not use quota provenance without a finite used percentage', () => {
    const window = buildForecastWindow({
      current: {
        total_requests: 100,
        total_tokens: 1_000_000,
        total_cost: 5,
        last_seen_ms: 1_900,
      },
      previous: {
        total_requests: 200,
        total_tokens: 2_000_000,
        total_cost: 10,
      },
      quota: {
        usedPercent: null,
        observedAtMs: 2_000,
        quotaProgressObservedAtMs: 2_000,
      },
    });

    expect(window.forecast).toEqual({
      requests: 200,
      tokens: 2_000_000,
      cost: 10,
      basis: 'previous',
    });
  });

  it('preserves previous fallback when current usage is unavailable', () => {
    const window = buildForecastWindow({
      previous: {
        total_requests: 149,
        total_tokens: 20_100_000,
        total_cost: 0.68,
      },
      quota: { usedPercent: null, observedAtMs: null },
    });

    expect(window.forecast).toEqual({
      requests: 149,
      tokens: 20_100_000,
      cost: 0.68,
      basis: 'previous',
    });
  });

  it('preserves previous fallback when current usage has no quota-aligned observation timestamp', () => {
    const window = buildForecastWindow({
      current: {
        total_requests: 100,
        total_tokens: 1_000_000,
        total_cost: 5,
        last_seen_ms: null,
      },
      previous: {
        total_requests: 200,
        total_tokens: 2_000_000,
        total_cost: 10,
      },
      quota: { usedPercent: null, observedAtMs: null },
    });

    expect(window.forecast).toEqual({
      requests: 200,
      tokens: 2_000_000,
      cost: 10,
      basis: 'previous',
    });
  });

  it('invalidates a forecast that is below every trusted current actual metric', () => {
    const window = buildForecastWindow({
      current: {
        total_requests: 1_000,
        total_tokens: 100_000_000,
        total_cost: 5,
        last_seen_ms: null,
      },
      previous: {
        total_requests: 100,
        total_tokens: 10_000_000,
        total_cost: 0.5,
      },
      quota: { usedPercent: null, observedAtMs: null },
    });

    expect(window.forecast).toBeNull();
  });

  it('invalidates a forecast when any metric is below trusted current actual', () => {
    const current = {
      total_requests: 1_000,
      total_tokens: 100_000_000,
      total_cost: 5,
      last_seen_ms: null,
    };
    const previousCases = [
      { total_requests: 999, total_tokens: 100_000_000, total_cost: 5 },
      { total_requests: 1_000, total_tokens: 99_999_999, total_cost: 5 },
      { total_requests: 1_000, total_tokens: 100_000_000, total_cost: 4.99 },
    ];

    for (const previous of previousCases) {
      const window = buildForecastWindow({
        current,
        previous,
        quota: { usedPercent: null, observedAtMs: null },
      });
      expect(window.forecast).toBeNull();
    }
  });

  it('keeps a normal quota forecast that covers trusted current actual', () => {
    const window = buildForecastWindow({
      current: {
        total_requests: 100,
        total_tokens: 1_000_000,
        total_cost: 5,
        last_seen_ms: 1_900,
      },
      quota: { usedPercent: 50, observedAtMs: 2_000 },
    });

    expect(window.forecast).toEqual({
      requests: 200,
      tokens: 2_000_000,
      cost: 10,
      basis: 'quota',
    });
  });

  it('allows a quota forecast equal to trusted current actual at full provider usage', () => {
    const window = buildForecastWindow({
      current: {
        total_requests: 100,
        total_tokens: 1_000_000,
        total_cost: 5,
        last_seen_ms: 1_900,
      },
      quota: { usedPercent: 100, observedAtMs: 2_000 },
    });

    expect(window.forecast).toEqual({
      requests: 100,
      tokens: 1_000_000,
      cost: 5,
      basis: 'quota',
    });
  });

  it('does not forecast from the previous cycle when the current cycle is not forecast eligible', () => {
    const row = makeRow({ provider: 'codex' });
    const nowMs = Date.now();
    const currentKey = accountWindowUsageRequestKey(row.selectionKey, 'weekly', 'current');
    const previousKey = accountWindowUsageRequestKey(row.selectionKey, 'weekly', 'previous');
    const windowUsageByKey = new Map<string, MonitoringAccountWindowUsageItem>([
      [
        currentKey,
        makeWindowUsage({
          window_key: 'weekly',
          total_requests: 2,
          total_tokens: 6_400,
          total_cost: 0.02,
          last_seen_ms: nowMs,
        }),
      ],
      [
        previousKey,
        makeWindowUsage({
          window_key: 'weekly',
          total_requests: 20,
          total_tokens: 200_000,
          total_cost: 2,
        }),
      ],
    ]);

    const viewModel = buildAccountDetailViewModel(row, {
      quotaWindows: [
        {
          key: 'weekly',
          providerWindowId: 'weekly',
          label: 'Weekly',
          kind: 'weekly',
          remainingPercent: 99,
          usedPercent: 1,
          resetLabel: 'later',
          resetAtMs: nowMs + 7 * 24 * 60 * 60 * 1000,
          resetAccuracy: 'estimated',
          observedAtMs: nowMs,
          limitWindowSeconds: 7 * 24 * 60 * 60,
          windowMode: 'fixed',
          cycleStartMs: nowMs,
          cycleEndMs: nowMs + 7 * 24 * 60 * 60 * 1000,
          modelScope: { kind: 'all', complete: true },
          availability: 'active',
          currentCycle: {
            id: 2,
            activationId: 1,
            state: 'active',
            scheduledStartMs: nowMs,
            scheduledEndMs: nowMs + 7 * 24 * 60 * 60 * 1000,
            actualStartMs: nowMs,
            actualEndMs: null,
            durationSeconds: 7 * 24 * 60 * 60,
            boundaryAccuracy: 'estimated',
            endReason: '',
            parentCycleId: null,
            forecastEligible: false,
          },
          previousCycle: {
            id: 1,
            activationId: 1,
            state: 'closed',
            scheduledStartMs: nowMs - 7 * 24 * 60 * 60 * 1000,
            scheduledEndMs: nowMs,
            actualStartMs: nowMs - 7 * 24 * 60 * 60 * 1000,
            actualEndMs: nowMs,
            durationSeconds: 7 * 24 * 60 * 60,
            boundaryAccuracy: 'exact',
            endReason: 'scheduled',
            parentCycleId: null,
            forecastEligible: true,
          },
        },
      ],
      windowUsageByKey,
    });

    expect(viewModel.quota.windows[0].forecast).toBeNull();
  });

  it('does not forecast a stale lifecycle current window from previous usage', () => {
    const row = makeRow({ provider: 'codex' });
    const nowMs = Date.now();
    const previousKey = accountWindowUsageRequestKey(row.selectionKey, 'weekly', 'previous');
    const windowUsageByKey = new Map<string, MonitoringAccountWindowUsageItem>([
      [
        previousKey,
        makeWindowUsage({
          window_key: 'weekly',
          total_requests: 20,
          total_tokens: 200_000,
          total_cost: 2,
        }),
      ],
    ]);

    const viewModel = buildAccountDetailViewModel(row, {
      quotaWindows: [
        {
          key: 'weekly',
          providerWindowId: 'weekly',
          label: 'Weekly',
          kind: 'weekly',
          remainingPercent: 99,
          usedPercent: 1,
          resetLabel: 'later',
          resetAtMs: nowMs + 7 * 24 * 60 * 60 * 1000,
          resetAccuracy: 'exact',
          observedAtMs: nowMs,
          limitWindowSeconds: 7 * 24 * 60 * 60,
          windowMode: 'fixed',
          cycleStartMs: nowMs - 60 * 60 * 1000,
          cycleEndMs: nowMs + 23 * 60 * 60 * 1000,
          modelScope: { kind: 'all', complete: true },
          availability: 'active',
          stale: true,
          currentCycle: {
            id: 2,
            activationId: 1,
            state: 'active',
            scheduledStartMs: nowMs - 60 * 60 * 1000,
            scheduledEndMs: nowMs + 23 * 60 * 60 * 1000,
            actualStartMs: nowMs - 60 * 60 * 1000,
            actualEndMs: null,
            durationSeconds: 7 * 24 * 60 * 60,
            boundaryAccuracy: 'exact',
            endReason: '',
            parentCycleId: null,
            forecastEligible: true,
          },
          previousCycle: {
            id: 1,
            activationId: 1,
            state: 'closed',
            scheduledStartMs: nowMs - 8 * 24 * 60 * 60 * 1000,
            scheduledEndMs: nowMs - 7 * 24 * 60 * 60 * 1000,
            actualStartMs: nowMs - 8 * 24 * 60 * 60 * 1000,
            actualEndMs: nowMs - 7 * 24 * 60 * 60 * 1000,
            durationSeconds: 7 * 24 * 60 * 60,
            boundaryAccuracy: 'exact',
            endReason: 'scheduled',
            parentCycleId: null,
            forecastEligible: true,
          },
        },
      ],
      windowUsageByKey,
    });

    expect(viewModel.quota.windows[0].forecast).toBeNull();
  });

  it('does not use stale quota progress without matched previous usage', () => {
    const row = makeRow({ provider: 'antigravity' });
    const nowMs = Date.now();
    const modelScope = { kind: 'family' as const, key: 'gemini', complete: true };
    const currentKey = accountWindowUsageRequestKey(
      row.selectionKey,
      'antigravity-gemini',
      'current',
      modelScope
    );
    const previousKey = accountWindowUsageRequestKey(
      row.selectionKey,
      'antigravity-gemini',
      'previous',
      modelScope
    );
    const windowUsageByKey = new Map<string, MonitoringAccountWindowUsageItem>([
      [
        currentKey,
        makeWindowUsage({
          window_key: 'antigravity-gemini',
          total_requests: 2,
          total_tokens: 6_400,
          total_cost: 0.02,
          last_seen_ms: nowMs,
        }),
      ],
      [
        previousKey,
        makeWindowUsage({
          window_key: 'antigravity-gemini',
          matched: false,
          total_requests: 0,
          total_tokens: 0,
          total_cost: 0,
        }),
      ],
    ]);

    const viewModel = buildAccountDetailViewModel(row, {
      quotaWindows: [
        {
          key: 'antigravity-gemini',
          providerWindowId: 'antigravity-gemini',
          label: 'Gemini',
          kind: 'weekly',
          remainingPercent: 99,
          usedPercent: 1,
          resetLabel: 'later',
          resetAtMs: nowMs + 7 * 24 * 60 * 60 * 1000,
          resetAccuracy: 'exact',
          observedAtMs: nowMs - 1_000,
          limitWindowSeconds: 7 * 24 * 60 * 60,
          windowMode: 'fixed',
          cycleStartMs: nowMs,
          cycleEndMs: nowMs + 7 * 24 * 60 * 60 * 1000,
          modelScope,
        },
      ],
      windowUsageByKey,
    });

    expect(viewModel.quota.windows[0].currentUsage).toMatchObject({
      matched: true,
      totalRequests: 2,
    });
    expect(viewModel.quota.windows[0].previousUsage).toMatchObject({ matched: false });
    expect(viewModel.quota.windows[0].forecast).toBeNull();
  });

  it('does not fall back to a previous cycle that ended early', () => {
    const row = makeRow({ provider: 'codex' });
    const nowMs = Date.now();
    const previousKey = accountWindowUsageRequestKey(row.selectionKey, 'weekly', 'previous');
    const windowUsageByKey = new Map<string, MonitoringAccountWindowUsageItem>([
      [
        previousKey,
        makeWindowUsage({
          window_key: 'weekly',
          total_requests: 200,
          total_tokens: 2_000_000,
          total_cost: 20,
        }),
      ],
    ]);

    const viewModel = buildAccountDetailViewModel(row, {
      quotaWindows: [
        {
          key: 'weekly',
          providerWindowId: 'weekly',
          label: 'Weekly',
          kind: 'weekly',
          remainingPercent: null,
          usedPercent: null,
          resetLabel: 'later',
          resetAtMs: nowMs + 7 * 24 * 60 * 60 * 1000,
          resetAccuracy: 'exact',
          limitWindowSeconds: 7 * 24 * 60 * 60,
          windowMode: 'fixed',
          cycleStartMs: nowMs,
          cycleEndMs: nowMs + 7 * 24 * 60 * 60 * 1000,
          modelScope: { kind: 'all', complete: true },
          availability: 'active',
          currentCycle: {
            id: 2,
            activationId: 1,
            state: 'active',
            scheduledStartMs: nowMs,
            scheduledEndMs: nowMs + 7 * 24 * 60 * 60 * 1000,
            actualStartMs: nowMs,
            actualEndMs: null,
            durationSeconds: 7 * 24 * 60 * 60,
            boundaryAccuracy: 'exact',
            endReason: '',
            parentCycleId: null,
            forecastEligible: true,
          },
          previousCycle: {
            id: 1,
            activationId: 1,
            state: 'closed',
            scheduledStartMs: nowMs - 7 * 24 * 60 * 60 * 1000,
            scheduledEndMs: nowMs,
            actualStartMs: nowMs - 3 * 24 * 60 * 60 * 1000,
            actualEndMs: nowMs,
            durationSeconds: 7 * 24 * 60 * 60,
            boundaryAccuracy: 'exact',
            endReason: 'early_reset',
            parentCycleId: null,
            forecastEligible: false,
          },
        },
      ],
      windowUsageByKey,
    });

    expect(viewModel.quota.windows[0].previousUsage).toMatchObject({
      matched: true,
      totalRequests: 200,
    });
    expect(viewModel.quota.windows[0].forecast).toBeNull();
  });

  it('accepts safely pre-scoped file-level action candidates', () => {
    const row = makeRow({ authIndex: '1' });
    const fileLevelCandidate = makeCandidate({
      id: 4,
      authIndex: undefined,
      reason: 'file-level fallback',
    });

    const viewModel = buildAccountDetailViewModel(row, {
      actionCandidates: [],
      matchedActionCandidates: [fileLevelCandidate],
    });

    expect(viewModel.strategy.actionCandidates).toHaveLength(1);
    expect(viewModel.strategy.actionCandidates[0]).toMatchObject({
      id: 4,
      reason: 'file-level fallback',
    });
  });

  it('builds diagnostics activity from the complete summary and latest known activity', () => {
    const row = makeRow();
    const viewModel = buildAccountDetailViewModel(row, {
      valueRow: makeMonitoringValue(row, { lastSeenMs: 2500 }),
      diagnosticsSummary: {
        total_calls: 42,
        failure_calls: 7,
        p95_latency_ms: 2345,
      } as MonitoringAnalyticsSummary,
      diagnosticsRecentFailure: {
        timestamp_ms: 1800,
        model: 'gpt-5',
        fail_status_code: 503,
        fail_summary: 'full-range failure',
      } as MonitoringAnalyticsRecentFailure,
      diagnosticsEvents: [makeAnalyticsEvent({ timestamp_ms: 2000 })],
      diagnosticsTotalCount: 1,
    });

    expect(viewModel.strategy.activity).toMatchObject({
      totalCalls: 42,
      failureCalls: 7,
      failureRate: (7 / 42) * 100,
      p95LatencyMs: 2345,
      latestActivityAtMs: 2500,
      latestSuccessAtMs: 2000,
      latestFailureAtMs: 1800,
      recentFailure: {
        timestampMs: 1800,
        reason: 'full-range failure',
        statusCode: 503,
        model: 'gpt-5',
      },
    });
  });

  it('uses a newer successful request as the current diagnosis over stale inspection advice', () => {
    const row = makeRow({
      inspection: {
        source: 'local',
        action: 'reauth',
        actionReason: 'expired token',
        actionStatus: 'pending',
        statusCode: 401,
        usedPercent: null,
        isQuota: false,
        runId: 0,
        resultId: -1,
        createdAtMs: 1000,
      },
    });
    const viewModel = buildAccountDetailViewModel(row, {
      recommendation: {
        row,
        action: 'reauth',
        priority: 'critical',
        reasonKey: 'accounts.recommend_reason_inspection',
      },
      diagnosticsEvents: [makeAnalyticsEvent({ timestamp_ms: 2000, failed: false })],
    });

    expect(viewModel.strategy.conclusion).toMatchObject({
      actionLabelKey: 'accounts.recommend_normal',
      reasonKey: 'accounts.recommend_normal_desc',
      priority: null,
      sourceLabelKey: 'accounts.latest_request_time_title',
      observedAtMs: 2000,
      evidenceStatus: 'current',
      evidenceStatusLabelKey: 'accounts.detail_diagnostic_evidence_current',
      latestActivityAtMs: 2000,
    });
    expect(viewModel.health.status).toBe('available');
  });

  it('uses a newer successful request over stale quota refresh failure advice', () => {
    const row = makeRow({
      quota: {
        status: 'error',
        error: 'upstream unavailable',
        errorStatus: 503,
        failedAtMs: 1_000,
      },
    });
    const viewModel = buildAccountDetailViewModel(row, {
      recommendation: {
        row,
        action: 'refresh',
        priority: 'medium',
        reasonKey: 'accounts.recommend_reason_error',
      },
      diagnosticsEvents: [makeAnalyticsEvent({ timestamp_ms: 2_000, failed: false })],
    });

    expect(viewModel.strategy.conclusion).toMatchObject({
      actionLabelKey: 'accounts.recommend_normal',
      reasonKey: 'accounts.recommend_normal_desc',
      priority: null,
      sourceLabelKey: 'accounts.latest_request_time_title',
      observedAtMs: 2_000,
      evidenceStatus: 'current',
    });
    expect(viewModel.health.status).toBe('available');
  });

  it('keeps a newer healthy Provider result as the detail diagnosis over stale inspection advice', () => {
    const row = makeRow({
      quota: {
        status: 'ok',
        remainingPercent: 80,
        usedPercent: 20,
        source: 'cache',
        fetchedAtMs: 2_000,
      },
      inspection: {
        source: 'server',
        action: 'reauth',
        actionReason: 'expired token',
        actionStatus: 'pending',
        statusCode: 401,
        usedPercent: null,
        isQuota: false,
        runId: 1,
        resultId: 2,
        createdAtMs: 1_000,
      },
    });
    const viewModel = buildAccountDetailViewModel(row);

    expect(viewModel.health.status).toBe('available');
    expect(viewModel.overview.decision).toMatchObject({
      basisLabelKey: 'accounts.quota_source_cache',
      observedAtMs: 2_000,
    });
    expect(viewModel.strategy.conclusion).toMatchObject({
      actionLabelKey: 'accounts.recommend_normal',
      reasonKey: 'accounts.recommend_normal_desc',
      sourceLabelKey: 'accounts.quota_source_cache',
      observedAtMs: 2_000,
      evidenceStatus: 'current',
    });
  });

  it('uses the quota refresh failure time as the detail authentication basis', () => {
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        quota: {
          status: 'error',
          error: 'HTTP 401 unauthorized',
          errorStatus: 401,
          failedAtMs: 2_500,
          fetchedAtMs: 1_000,
          source: 'cache',
        },
      })
    );

    expect(viewModel.health).toMatchObject({
      status: 'reauth',
      reasonKey: 'accounts.health_reason_reauth_quota_refresh',
    });
    expect(viewModel.overview.decision).toMatchObject({
      basisLabelKey: 'accounts.quota_source_cache',
      observedAtMs: 2_500,
    });
    expect(viewModel.strategy.conclusion).toMatchObject({
      reasonKey: 'accounts.recommend_reason_quota_auth',
      sourceLabelKey: 'accounts.quota_source_cache',
      observedAtMs: 2_500,
    });
  });

  it('records a newer HTTP 499 without replacing the credential diagnosis', () => {
    const row = makeRow({
      inspection: {
        source: 'local',
        action: 'reauth',
        actionReason: 'expired token',
        actionStatus: 'pending',
        statusCode: 401,
        usedPercent: null,
        isQuota: false,
        runId: 0,
        resultId: -1,
        createdAtMs: 1_000,
      },
    });
    const viewModel = buildAccountDetailViewModel(row, {
      diagnosticsEvents: [
        makeAnalyticsEvent({ timestamp_ms: 2_000, failed: true, fail_status_code: 499 }),
      ],
    });

    expect(viewModel.health.status).toBe('reauth');
    expect(viewModel.strategy.conclusion).toMatchObject({
      actionLabelKey: 'accounts.recommend_action_reauth',
      sourceLabelKey: 'accounts.inspection_source_local',
      observedAtMs: 1_000,
      evidenceStatus: 'current',
      latestActivityAtMs: 2_000,
    });
    expect(viewModel.strategy.activity.latestFailureAtMs).toBe(2_000);
    expect(viewModel.strategy.activity.latestHealthEvidenceAtMs).toBeNull();
  });

  it('uses a newer credential failure as the current diagnosis over a healthy inspection', () => {
    const row = makeRow({
      inspection: {
        source: 'server',
        action: 'keep',
        actionReason: 'healthy',
        actionStatus: 'none',
        statusCode: 200,
        usedPercent: 20,
        isQuota: false,
        runId: 1,
        resultId: 2,
        createdAtMs: 1000,
      },
    });
    const viewModel = buildAccountDetailViewModel(row, {
      diagnosticsRecentFailure: {
        timestamp_ms: 2200,
        model: 'gpt-5',
        fail_status_code: 401,
        fail_summary: 'token expired',
      } as MonitoringAnalyticsRecentFailure,
    });

    expect(viewModel.strategy.conclusion).toMatchObject({
      actionLabelKey: 'accounts.recommend_action_reauth',
      reasonKey: 'accounts.recommend_reason_request_auth',
      priority: 'critical',
      sourceLabelKey: 'accounts.latest_request_time_title',
      observedAtMs: 2200,
      evidenceStatus: 'current',
      latestActivityAtMs: 2200,
    });
    expect(viewModel.health.status).toBe('reauth');
  });

  it('uses the last qualified direction instead of any newer success or failure', () => {
    const row = makeRow({
      inspection: {
        source: 'local',
        action: 'reauth',
        actionReason: 'expired token',
        actionStatus: 'pending',
        statusCode: 401,
        usedPercent: null,
        isQuota: false,
        runId: 0,
        resultId: -1,
        createdAtMs: 1_000,
      },
    });
    const viewModel = buildAccountDetailViewModel(row, {
      diagnosticsEvents: [
        makeAnalyticsEvent({ timestamp_ms: 2_500, failed: true, fail_status_code: 401 }),
        makeAnalyticsEvent({ timestamp_ms: 2_000, failed: false }),
      ],
    });

    expect(viewModel.strategy.conclusion).toMatchObject({
      actionLabelKey: 'accounts.recommend_action_reauth',
      reasonKey: 'accounts.recommend_reason_request_auth',
      sourceLabelKey: 'accounts.latest_request_time_title',
      observedAtMs: 2_500,
      evidenceStatus: 'current',
    });
  });

  it('shows newer transient health without reviving an older request authentication failure', () => {
    for (const authenticatedEvent of [
      makeAnalyticsEvent({ timestamp_ms: 3_000, failed: false }),
      makeAnalyticsEvent({ timestamp_ms: 3_000, failed: true, fail_status_code: 429 }),
    ]) {
      const viewModel = buildAccountDetailViewModel(makeRow(), {
        diagnosticsEvents: [
          makeAnalyticsEvent({ timestamp_ms: 5_000, failed: true, fail_status_code: 503 }),
          makeAnalyticsEvent({ timestamp_ms: 4_000, failed: true, fail_status_code: 502 }),
          authenticatedEvent,
          makeAnalyticsEvent({ timestamp_ms: 2_000, failed: true, fail_status_code: 401 }),
        ],
      });

      expect(viewModel.health.status).toBe('exception');
      expect(viewModel.strategy.recommendation).toMatchObject({
        action: 'review',
        reasonKey: 'accounts.recommend_reason_request_failure',
      });
      expect(viewModel.strategy.conclusion).toMatchObject({
        actionLabelKey: 'accounts.recommend_action_review',
        reasonKey: 'accounts.recommend_reason_request_failure',
        sourceLabelKey: 'accounts.latest_request_time_title',
        observedAtMs: 5_000,
        evidenceStatus: 'current',
      });
    }
  });

  it('counts one transient failure only once when detail sources repeat it', () => {
    const repeatedFailure = {
      timestamp_ms: 3_000,
      failed: true,
      fail_status_code: 503,
      fail_summary: 'upstream unavailable',
    };
    const viewModel = buildAccountDetailViewModel(makeRow(), {
      history: makeHistory({
        latest_request: repeatedFailure,
        recent_requests: [repeatedFailure],
        last_seen_ms: repeatedFailure.timestamp_ms,
      }),
      diagnosticsEvents: [
        makeAnalyticsEvent({
          ...repeatedFailure,
          event_hash: 'repeated-event',
          request_id: 'repeated-request',
        }),
      ],
      diagnosticsRecentFailure: {
        timestamp_ms: repeatedFailure.timestamp_ms,
        model: 'gpt-5',
        api_key_hash: 'api-key-hash',
        source_hash: 'source-hash',
        auth_index: '0',
        endpoint: '/v1/responses',
        duration_ms: 120,
        fail_status_code: repeatedFailure.fail_status_code,
        fail_summary: repeatedFailure.fail_summary,
      },
    });

    expect(viewModel.health.status).toBe('available');
    expect(viewModel.strategy.activity).toMatchObject({
      latestActivityAtMs: 3_000,
      latestFailureAtMs: 3_000,
      latestHealthDirection: null,
      latestHealthEvidenceAtMs: null,
    });
  });

  it('keeps two distinct transient failures as current negative evidence', () => {
    const latestFailure = {
      timestamp_ms: 3_000,
      failed: true,
      fail_status_code: 503,
      fail_summary: 'upstream unavailable',
    };
    const viewModel = buildAccountDetailViewModel(makeRow(), {
      history: makeHistory({
        latest_request: latestFailure,
        recent_requests: [
          latestFailure,
          {
            timestamp_ms: 2_500,
            failed: true,
            fail_status_code: 502,
            fail_summary: 'bad gateway',
          },
        ],
        last_seen_ms: latestFailure.timestamp_ms,
      }),
    });

    expect(viewModel.health.status).toBe('exception');
    expect(viewModel.strategy.activity).toMatchObject({
      latestHealthDirection: 'negative',
      latestHealthEvidenceAtMs: 3_000,
    });
    expect(viewModel.strategy.conclusion.reasonKey).toBe(
      'accounts.recommend_reason_request_failure'
    );
  });

  it('combines distinct transient failures from history and diagnostic events', () => {
    const viewModel = buildAccountDetailViewModel(makeRow(), {
      history: makeHistory({
        latest_request: {
          timestamp_ms: 3_000,
          failed: true,
          fail_status_code: 503,
          fail_summary: 'upstream unavailable',
        },
        recent_requests: [
          {
            timestamp_ms: 3_000,
            failed: true,
            fail_status_code: 503,
            fail_summary: 'upstream unavailable',
          },
        ],
        last_seen_ms: 3_000,
      }),
      diagnosticsEvents: [
        makeAnalyticsEvent({
          timestamp_ms: 2_500,
          failed: true,
          fail_status_code: 502,
          fail_summary: 'bad gateway',
        }),
      ],
    });

    expect(viewModel.health.status).toBe('exception');
    expect(viewModel.strategy.activity).toMatchObject({
      latestHealthDirection: 'negative',
      latestHealthEvidenceAtMs: 3_000,
    });
    expect(viewModel.strategy.conclusion.reasonKey).toBe(
      'accounts.recommend_reason_request_failure'
    );
  });

  it('keeps requests current when CPA persists runtime state after request start', () => {
    const requestAtMs = 1_700_000_002_000;
    const persistedAtMs = 1_700_000_003_000;
    const row = makeRow({
      createdAtMs: persistedAtMs,
      updatedAtMs: persistedAtMs,
      raw: {
        name: 'shared.codex.json',
        type: 'codex',
        provider: 'codex',
        authIndex: '0',
        account: 'codex@example.com',
        modtime: persistedAtMs,
      },
    });
    const viewModel = buildAccountDetailViewModel(row, {
      history: makeHistory({
        latest_request: { timestamp_ms: requestAtMs, failed: false },
        recent_requests: [{ timestamp_ms: requestAtMs, failed: false }],
        last_seen_ms: requestAtMs,
      }),
    });

    expect(viewModel.strategy.activity).toMatchObject({
      latestActivityAtMs: requestAtMs,
      latestSuccessAtMs: requestAtMs,
      latestHealthDirection: 'positive',
      latestHealthEvidenceAtMs: requestAtMs,
    });
    expect(viewModel.strategy.conclusion.sourceLabelKey).toBe('accounts.latest_request_time_title');
  });

  it('does not use a successful request to clear a quota recommendation', () => {
    const row = makeRow({
      quota: {
        status: 'exhausted',
        remainingPercent: 0,
        usedPercent: 100,
      },
    });
    const viewModel = buildAccountDetailViewModel(row, {
      recommendation: {
        row,
        action: 'disable',
        priority: 'critical',
        reasonKey: 'accounts.recommend_reason_exhausted',
      },
      diagnosticsEvents: [makeAnalyticsEvent({ timestamp_ms: 2000, failed: false })],
    });

    expect(viewModel.strategy.conclusion).toMatchObject({
      actionLabelKey: 'accounts.recommend_action_disable',
      reasonKey: 'accounts.recommend_reason_exhausted',
      evidenceStatus: 'current',
    });
  });

  it('uses the latest request as the detail decision basis for request-only quota limits', () => {
    const requestAtMs = 2_000;
    const viewModel = buildAccountDetailViewModel(makeRow(), {
      history: makeHistory({
        latest_request: {
          timestamp_ms: requestAtMs,
          failed: true,
          fail_status_code: 429,
          fail_summary: 'rate limit exceeded',
        },
        recent_requests: [
          {
            timestamp_ms: requestAtMs,
            failed: true,
            fail_status_code: 429,
            fail_summary: 'rate limit exceeded',
          },
        ],
        last_seen_ms: requestAtMs,
      }),
    });

    expect(viewModel.overview.decision).toMatchObject({
      status: 'limited',
      reasonKey: 'accounts.health_reason_limited_request',
      basisLabelKey: 'accounts.latest_request_time_title',
      observedAtMs: requestAtMs,
    });
  });

  it('exposes inspection source and localizes keyed inspection reasons', () => {
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        inspection: {
          source: 'local',
          action: 'reauth',
          actionReason: 'monitoring.codex_inspection_reason_reauth',
          actionStatus: 'pending',
          statusCode: 401,
          usedPercent: null,
          isQuota: false,
          runId: 0,
          resultId: -1,
          createdAtMs: 1000,
        },
      })
    );

    expect(viewModel.strategy.inspectionFields).toEqual(
      expect.arrayContaining([
        {
          key: 'source',
          labelKey: 'accounts.detail_inspection_source',
          value: 'accounts.inspection_source_local',
          valueKind: 'i18n',
        },
        {
          key: 'reason',
          labelKey: 'accounts.detail_reason',
          value: 'monitoring.codex_inspection_reason_reauth',
          valueKind: 'i18n',
        },
      ])
    );
  });

  it('does not expose a stale pending action status for a keep inspection', () => {
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        inspection: {
          source: 'server',
          action: 'keep',
          actionReason: 'healthy',
          actionStatus: 'pending',
          statusCode: 200,
          usedPercent: 20,
          isQuota: false,
          runId: 1,
          resultId: 2,
          createdAtMs: 1000,
        },
      })
    );

    expect(viewModel.strategy.inspectionFields).not.toEqual(
      expect.arrayContaining([{ key: 'actionStatus' }])
    );
  });

  it('exposes xAI official API reachability without billing details', () => {
    const xaiQuota: XaiQuotaState = {
      status: 'success',
      billing: {
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
      },
    };

    const viewModel = buildAccountDetailViewModel(
      makeRow({ provider: 'xai', fileName: 'paid-xai.json' }),
      { xaiQuota }
    );

    expect(viewModel.quota.diagnostics).toContainEqual({
      key: 'xaiOfficialApiHealth',
      labelKey: 'xai_quota.official_api_plan',
      value: 'xai_quota.official_api_health',
      valueKind: 'i18n',
    });
  });

  it('keeps raw secrets and candidate evidence out of the drawer contract', () => {
    const row = makeRow({
      key: 'secret.codex.json',
      selectionKey: 'secret.codex.json\u00000',
      fileName: 'secret.codex.json',
      raw: {
        name: 'secret.codex.json',
        type: 'codex',
        provider: 'codex',
        authIndex: '0',
        account: 'secret@example.com',
        access_token: 'sk-raw-access-secret',
        refresh_token: 'sk-raw-refresh-secret',
        cookie: 'raw-cookie-secret',
        id_token: {
          email: 'secret@example.com',
          token: 'id-token-secret',
        },
      } as AuthFileItem,
    });
    const codexQuota: CodexQuotaState = {
      status: 'success',
      windows: [],
      rateLimitResetCreditsAvailableCount: 2,
      authFileKey: 'secret.codex.json::0',
    };

    const viewModel = buildAccountDetailViewModel(row, {
      codexQuota,
      history: makeHistory(),
      actionCandidates: [
        makeCandidate({
          authFileName: 'secret.codex.json',
          evidence: {
            raw_json: 'evidence-secret',
            fail_body: 'failure-body-secret',
          },
        }),
      ],
    });
    const serialized = JSON.stringify(viewModel);

    expect(viewModel.quota.resetCreditsAvailableCount).toBe(2);
    expect(viewModel.strategy.actionCandidates[0].hasEvidence).toBe(true);
    expect(serialized).not.toContain('sk-raw-access-secret');
    expect(serialized).not.toContain('sk-raw-refresh-secret');
    expect(serialized).not.toContain('raw-cookie-secret');
    expect(serialized).not.toContain('id-token-secret');
    expect(serialized).not.toContain('evidence-secret');
    expect(serialized).not.toContain('failure-body-secret');
  });

  it('keeps overview activity scoped to matched seven-day monitoring data', () => {
    const row = makeRow();
    const viewModel = buildAccountDetailViewModel(row, {
      history: makeHistory({
        total_requests: 99,
        total_tokens: 12345,
        total_cost: 6.78,
      }),
      valueRow: makeMonitoringValue(row),
    });

    expect(viewModel.history?.successRate).toBeCloseTo(83.333, 2);
    expect(viewModel.overview.activity).toMatchObject({
      scope: 'monitoring_7d',
      scopeDays: 7,
      sourceLabelKey: 'accounts.value_source_monitoring',
      hasActivity: true,
    });
    expect(viewModel.overview.activity.metrics).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ key: 'requests', value: 7 }),
        expect.objectContaining({ key: 'tokens', value: 1200 }),
        expect.objectContaining({ key: 'cost', value: 0.42 }),
        expect.objectContaining({
          key: 'lastSeenMs',
          labelKey: 'accounts.detail_overview_activity_last_active',
          value: 1_700_000_000_000,
          valueKind: 'timestamp',
        }),
      ])
    );
    expect(viewModel.overview.activity.metrics).not.toEqual(
      expect.arrayContaining([
        expect.objectContaining({ key: 'requests', value: 99 }),
        expect.objectContaining({ key: 'tokens', value: 12345 }),
        expect.objectContaining({ key: 'cost', value: 6.78 }),
      ])
    );
    expect(viewModel.overview.decision).toMatchObject({
      basisLabelKey: 'accounts.quota_source_cache',
      observedAtMs: null,
    });
  });

  it('uses newer inspection evidence over an older quota-refresh reauth result', () => {
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        quota: {
          status: 'error',
          error: 'quota refresh failed: HTTP 401',
          fetchedAtMs: 1_700_000_100_000,
          source: 'cache',
        },
        inspection: {
          source: 'server',
          action: 'reauth',
          actionReason: 'monitoring.codex_inspection_reason_reauth',
          actionStatus: 'none',
          statusCode: 401,
          usedPercent: null,
          runId: 1,
          resultId: 1,
          createdAtMs: 1_700_000_200_000,
        },
      })
    );

    expect(viewModel.health.reasonKey).toBe('accounts.health_reason_reauth_inspection');
    expect(viewModel.overview.attention).toMatchObject({
      actionLabelKey: 'accounts.recommend_action_reauth',
      reasonKey: 'accounts.recommend_reason_inspection',
      targetTab: 'diagnostics',
    });
    expect(viewModel.overview.decision).toMatchObject({
      basisLabelKey: 'accounts.detail_overview_basis_inspection',
      observedAtMs: 1_700_000_200_000,
    });
  });

  it('attributes merged header failures to the observed header instead of cached quota', () => {
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        quota: {
          status: 'ok',
          source: 'cache',
          fetchedAtMs: 1_700_000_100_000,
          observedAtMs: 1_700_000_300_000,
          observedErrorKind: 'rate_limit',
          observedErrorCode: 'usage_limit_reached',
        },
        inspection: {
          source: 'server',
          action: 'disable',
          actionReason: 'monitoring.codex_inspection_reason_disable',
          actionStatus: 'none',
          statusCode: 429,
          usedPercent: null,
          runId: 1,
          resultId: 1,
          createdAtMs: 1_700_000_200_000,
        },
      })
    );

    expect(viewModel.health.reasonKey).toBe('accounts.health_reason_limited_header');
    expect(viewModel.overview.decision).toMatchObject({
      basisLabelKey: 'accounts.quota_source_observed_header',
      observedAtMs: 1_700_000_300_000,
    });
  });

  it('uses the cooldown record and disable time as the overview decision basis', () => {
    const viewModel = buildAccountDetailViewModel(makeRow(), {
      quotaCooldown: {
        authFileName: 'shared.codex.json',
        authIndex: '0',
        recoverAtMs: 1_700_001_000_000,
        createdAtMs: 1_700_000_100_000,
        disabledAtMs: 1_700_000_200_000,
      },
    });

    expect(viewModel.health.reasonKey).toBe('accounts.health_reason_limited_cooldown');
    expect(viewModel.overview.decision).toMatchObject({
      basisLabelKey: 'accounts.detail_overview_basis_cooldown',
      observedAtMs: 1_700_000_200_000,
    });
  });

  it('summarizes mixed Antigravity model groups without marking the credential unavailable', () => {
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        provider: 'antigravity',
        quota: {
          status: 'ok',
          remainingPercent: 66,
          usedPercent: 34,
          resetLabel: '2026-07-30T02:00:00Z',
          planType: 'pro',
          source: 'cache',
        },
      }),
      {
        quotaWindows: [
          {
            key: 'gemini:five-hour',
            label: 'Five hour',
            groupLabel: 'Gemini models',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: '2026-07-30T02:00:00Z',
            resetAtMs: Date.parse('2026-07-30T02:00:00Z'),
          },
          {
            key: 'claude:five-hour',
            label: 'Five hour',
            groupLabel: 'Claude and GPT models',
            remainingPercent: 82,
            usedPercent: 18,
            resetLabel: '2026-07-30T01:00:00Z',
            resetAtMs: Date.parse('2026-07-30T01:00:00Z'),
          },
          {
            key: 'claude:weekly',
            label: 'Weekly',
            groupLabel: 'Claude and GPT models',
            remainingPercent: 66,
            usedPercent: 34,
            resetLabel: '2026-08-04T02:00:00Z',
            resetAtMs: Date.parse('2026-08-04T02:00:00Z'),
          },
        ],
      }
    );

    expect(viewModel.overview.decision.status).toBe('partial');
    expect(viewModel.overview.capacity).toMatchObject({
      kind: 'group_availability',
      statusLabelKey: 'accounts.health_partial',
      availableGroupCount: 1,
      totalGroupCount: 2,
    });
    expect(viewModel.overview.capacity.fields).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          key: 'overviewLimitedGroups',
          value: 'Gemini models',
        }),
        expect.objectContaining({
          key: 'overviewGroupRecovery',
          value: Date.parse('2026-07-30T02:00:00Z'),
          valueKind: 'quota_reset',
        }),
      ])
    );
  });

  it('uses the conservative recovery window in overview capacity', () => {
    const earlierResetAtMs = Date.parse('2026-07-30T04:00:00Z');
    const laterResetAtMs = Date.parse('2026-07-30T06:00:00Z');
    const viewModel = buildAccountDetailViewModel(makeRow(), {
      quotaWindows: [
        {
          key: 'weekly-base',
          label: 'Weekly base',
          kind: 'weekly',
          remainingPercent: 0,
          usedPercent: 100,
          resetLabel: '2026-07-30T04:00:00Z',
          resetAtMs: earlierResetAtMs,
          resetAccuracy: 'exact',
          modelScope: CODEX_MAIN_SCOPE,
        },
        {
          key: 'weekly-model',
          label: 'Weekly model',
          kind: 'weekly',
          remainingPercent: 0,
          usedPercent: 100,
          resetLabel: '2026-07-30T06:00:00Z',
          resetAtMs: laterResetAtMs,
          resetAccuracy: 'exact',
          modelScope: CODEX_MAIN_SCOPE,
        },
      ],
    });

    expect(viewModel.overview.decision.status).toBe('weekly_exhausted');
    expect(viewModel.overview.capacity.fields).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ key: 'overviewQuotaWindow', value: 'Weekly model' }),
        expect.objectContaining({
          key: 'reset',
          value: laterResetAtMs,
          valueKind: 'quota_reset',
        }),
      ])
    );
  });

  it('does not fall back to a stale summary reset when the limiting window is unknown', () => {
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        quota: {
          status: 'exhausted',
          remainingPercent: 0,
          usedPercent: 100,
          resetLabel: '2026-07-30T04:00:00Z',
          resetAtMs: Date.parse('2026-07-30T04:00:00Z'),
          resetAccuracy: 'exact',
        },
      }),
      {
        quotaWindows: [
          {
            key: 'weekly-known',
            label: 'Weekly known',
            kind: 'weekly',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: '2026-07-30T04:00:00Z',
            resetAtMs: Date.parse('2026-07-30T04:00:00Z'),
            resetAccuracy: 'exact',
            modelScope: CODEX_MAIN_SCOPE,
          },
          {
            key: 'weekly-unknown',
            label: 'Weekly unknown',
            kind: 'weekly',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: '-',
            resetAtMs: null,
            resetAccuracy: 'unknown',
            modelScope: CODEX_MAIN_SCOPE,
          },
        ],
      }
    );

    expect(viewModel.overview.capacity.fields).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ key: 'overviewQuotaWindow', value: 'Weekly unknown' }),
      ])
    );
    expect(viewModel.overview.capacity.fields).not.toEqual(
      expect.arrayContaining([expect.objectContaining({ key: 'reset' })])
    );
  });

  it('uses credential update time and prefers a live subscription end time', () => {
    const liveSubscriptionUntil = '2026-08-31T23:59:59Z';
    const liveSubscriptionUntilSeconds = Date.parse(liveSubscriptionUntil) / 1000;
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        createdAtMs: 1_700_000_000_000,
        updatedAtMs: 1_700_100_000_000,
        raw: {
          name: 'shared.codex.json',
          type: 'codex',
          provider: 'codex',
          authIndex: '0',
          id_token: {
            plan_type: 'pro',
            chatgpt_subscription_active_until: '2026-09-30T23:59:59Z',
          },
        },
      }),
      {
        codexQuota: {
          status: 'success',
          windows: [],
          planType: 'pro',
          subscriptionActiveUntil: liveSubscriptionUntilSeconds,
        },
      }
    );

    expect(viewModel.overview.credential.fields).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          key: 'updatedAtMs',
          labelKey: 'accounts.detail_updated_at',
          value: 1_700_100_000_000,
        }),
        expect.objectContaining({
          key: 'subscriptionUntilMs',
          labelKey: 'accounts.detail_subscription_until',
          value: Date.parse(liveSubscriptionUntil),
          valueKind: 'quota_reset',
        }),
      ])
    );
    expect(viewModel.overview.credential.fields.map((item) => item.key)).not.toContain(
      'createdAtMs'
    );
    expect(viewModel.overview.credential.targetTab).toBe('config');
  });

  it('falls back to the Codex ID token subscription end time and marks its source', () => {
    const tokenSubscriptionUntil = '2026-09-30T23:59:59Z';
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        raw: {
          name: 'shared.codex.json',
          type: 'codex',
          provider: 'codex',
          authIndex: '0',
          id_token: {
            plan_type: 'plus',
            chatgpt_subscription_active_until: tokenSubscriptionUntil,
          },
        },
      }),
      {
        codexQuota: {
          status: 'success',
          windows: [],
          planType: 'plus',
          subscriptionActiveUntil: 'invalid-date',
        },
      }
    );

    expect(viewModel.overview.credential.fields).toContainEqual(
      expect.objectContaining({
        key: 'subscriptionUntilMs',
        labelKey: 'accounts.detail_subscription_until_token',
        value: Date.parse(tokenSubscriptionUntil),
      })
    );
  });

  it('reads subscription end claims from JSON and JWT id_token containers', () => {
    const jsonSubscriptionUntil = '2026-09-30T23:59:59Z';
    const jwtSubscriptionUntil = '2026-10-31T23:59:59Z';
    const jwtPayload = globalThis
      .btoa(
        JSON.stringify({
          plan_type: 'plus',
          chatgpt_subscription_active_until: jwtSubscriptionUntil,
        })
      )
      .replace(/\+/g, '-')
      .replace(/\//g, '_')
      .replace(/=+$/g, '');
    const jsonViewModel = buildAccountDetailViewModel(
      makeRow({
        planType: null,
        quota: { planType: null },
        raw: {
          name: 'json-token.codex.json',
          type: 'codex',
          provider: 'codex',
          authIndex: '0',
          metadata: {
            id_token: JSON.stringify({
              plan_type: 'plus',
              chatgpt_subscription_active_until: jsonSubscriptionUntil,
            }),
          },
        },
      })
    );
    const jwtViewModel = buildAccountDetailViewModel(
      makeRow({
        planType: null,
        quota: { planType: null },
        raw: {
          name: 'jwt-token.codex.json',
          type: 'codex',
          provider: 'codex',
          authIndex: '0',
          attributes: {
            id_token: `e30.${jwtPayload}.signature`,
          },
        },
      })
    );

    expect(jsonViewModel.overview.credential.fields).toContainEqual(
      expect.objectContaining({
        key: 'subscriptionUntilMs',
        labelKey: 'accounts.detail_subscription_until_token',
        value: Date.parse(jsonSubscriptionUntil),
      })
    );
    expect(jwtViewModel.overview.credential.fields).toContainEqual(
      expect.objectContaining({
        key: 'subscriptionUntilMs',
        labelKey: 'accounts.detail_subscription_until_token',
        value: Date.parse(jwtSubscriptionUntil),
      })
    );
  });

  it('does not infer subscription validity from another provider or an invalid token claim', () => {
    const invalidCodex = buildAccountDetailViewModel(
      makeRow({
        raw: {
          name: 'shared.codex.json',
          type: 'codex',
          provider: 'codex',
          authIndex: '0',
          id_token: {
            plan_type: 'plus',
            chatgpt_subscription_active_until: 'not-a-date',
          },
        },
      })
    );
    const claude = buildAccountDetailViewModel(
      makeRow({
        provider: 'claude',
        raw: {
          name: 'claude.json',
          type: 'claude',
          provider: 'claude',
          authIndex: '0',
          id_token: {
            chatgpt_subscription_active_until: '2026-09-30T23:59:59Z',
          },
        },
      }),
      {
        codexQuota: {
          status: 'success',
          windows: [],
          subscriptionActiveUntil: '2026-10-31T23:59:59Z',
        },
      }
    );

    expect(invalidCodex.overview.credential.fields.map((item) => item.key)).not.toContain(
      'subscriptionUntilMs'
    );
    expect(claude.overview.credential.fields.map((item) => item.key)).not.toContain(
      'subscriptionUntilMs'
    );
  });

  it('does not present a stale subscription end time for a Free Codex plan', () => {
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        planType: 'plus',
        raw: {
          name: 'free.codex.json',
          type: 'codex',
          provider: 'codex',
          authIndex: '0',
          id_token: {
            plan_type: 'plus',
            chatgpt_subscription_active_until: '2026-09-30T23:59:59Z',
          },
        },
      }),
      {
        codexQuota: {
          status: 'success',
          windows: [],
          planType: 'FREE',
          subscriptionActiveUntil: '2026-10-31T23:59:59Z',
        },
      }
    );

    expect(viewModel.overview.credential.fields.map((item) => item.key)).not.toContain(
      'subscriptionUntilMs'
    );
  });

  it('drops subscription timestamps outside the JavaScript date range', () => {
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        raw: {
          name: 'shared.codex.json',
          type: 'codex',
          provider: 'codex',
          authIndex: '0',
          id_token: {
            plan_type: 'plus',
            chatgpt_subscription_active_until: Number.MAX_VALUE,
          },
        },
      }),
      {
        codexQuota: {
          status: 'success',
          windows: [],
          subscriptionActiveUntil: Number.MAX_VALUE,
        },
      }
    );

    expect(viewModel.overview.credential.fields.map((item) => item.key)).not.toContain(
      'subscriptionUntilMs'
    );
  });

  it('drops unmatched lifetime history instead of presenting it as credential evidence', () => {
    const viewModel = buildAccountDetailViewModel(makeRow(), {
      history: makeHistory({
        matched: false,
        total_requests: 999,
        total_tokens: 99999,
        total_cost: 99,
      }),
    });

    expect(viewModel.history).toBeNull();
    expect(viewModel.overview.activity.scope).toBe('recent_snapshot');
    expect(viewModel.overview.activity.metrics).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ key: 'requests', value: 10 }),
        expect.objectContaining({ key: 'successCalls', value: 9 }),
        expect.objectContaining({ key: 'failureCalls', value: 1 }),
      ])
    );
    expect(viewModel.overview.activity.metrics).not.toEqual(
      expect.arrayContaining([expect.objectContaining({ value: 999 })])
    );
  });

  it('labels recent request data as fallback and omits synthetic cost and token metrics', () => {
    const viewModel = buildAccountDetailViewModel(makeRow());
    const metricKeys = viewModel.overview.activity.metrics.map((metric) => metric.key);

    expect(viewModel.value).toMatchObject({
      source: 'recent',
      requests: 10,
      estimatedCost: null,
    });
    expect(viewModel.overview.activity).toMatchObject({
      scope: 'recent_snapshot',
      scopeDays: null,
      sourceLabelKey: 'accounts.value_source_recent',
    });
    expect(metricKeys).not.toContain('cost');
    expect(metricKeys).not.toContain('tokens');
  });

  it('falls back to the selected credential when monitoring data belongs to another row', () => {
    const row = makeRow();
    const otherRow = makeRow({
      selectionKey: 'shared.codex.json\u00001',
      authIndex: '1',
      accountLabel: 'other@example.com',
    });
    const viewModel = buildAccountDetailViewModel(row, {
      valueRow: makeMonitoringValue(otherRow, { requests: 77 }),
    });

    expect(viewModel.value.source).toBe('recent');
    expect(viewModel.overview.activity.scope).toBe('recent_snapshot');
    expect(viewModel.overview.activity.metrics).toContainEqual(
      expect.objectContaining({ key: 'requests', value: 10 })
    );
  });

  it('exposes explicit missing states without inventing quota or activity evidence', () => {
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        quota: {
          status: 'unknown',
          remainingPercent: null,
          usedPercent: null,
          resetLabel: '-',
          planType: null,
          source: 'none',
        },
        usage: {
          success: 0,
          failure: 0,
          successRate: null,
          recentRequests: [],
        },
      })
    );

    expect(viewModel.overview.capacity).toMatchObject({
      remainingPercent: null,
      hasData: false,
    });
    expect(viewModel.overview.activity.hasActivity).toBe(false);
    expect(viewModel.overview.decision.observedAtMs).toBeNull();
    expect(viewModel.overview.decision.basisLabelKey).toBe(
      'accounts.detail_overview_basis_credential_state'
    );
  });

  it('aggregates recent status from request buckets and preserves the current status message', () => {
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        statusMessage: 'rate_limit_reached',
        updatedAtMs: 1_000,
        raw: { ...makeRow().raw, status_code: 429 },
        quota: {
          status: 'ok',
          remainingPercent: 80,
          usedPercent: 20,
          resetLabel: 'later',
          source: 'cache',
          fetchedAtMs: 500,
        },
        usage: {
          success: 99,
          failure: 1,
          successRate: 99,
          recentRequests: [
            { success: 2, failed: 1 },
            { success: 3, failed: 0 },
          ],
        },
      })
    );

    expect(viewModel.overview.recentStatus).toMatchObject({
      success: 5,
      failure: 1,
      successRate: (5 / 6) * 100,
      statusMessage: 'rate_limit_reached',
    });
    expect(viewModel.overview.decision).toMatchObject({
      basisLabelKey: 'accounts.detail_overview_basis_credential_state',
      observedAtMs: 1_000,
    });
  });

  it('omits neutral or superseded raw status messages from the current overview diagnosis', () => {
    const unavailableQuota = {
      status: 'unknown' as const,
      remainingPercent: null,
      usedPercent: null,
      resetLabel: '-',
      source: 'none' as const,
    };
    const currentAuthFailure = buildAccountDetailViewModel(
      makeRow({
        statusMessage: 'unauthorized',
        updatedAtMs: 1_000,
        raw: { ...makeRow().raw, status_code: 401 },
        quota: unavailableQuota,
      })
    );
    const supersededAuthFailure = buildAccountDetailViewModel(
      makeRow({
        statusMessage: 'unauthorized',
        updatedAtMs: 1_000,
        raw: { ...makeRow().raw, status_code: 401 },
        quota: unavailableQuota,
      }),
      {
        history: makeHistory({
          latest_request: { timestamp_ms: 2_000, failed: false },
          recent_requests: [{ timestamp_ms: 2_000, failed: false }],
          last_seen_ms: 2_000,
        }),
      }
    );
    const neutralCancellation = buildAccountDetailViewModel(
      makeRow({
        statusMessage: 'context canceled',
        updatedAtMs: 3_000,
        raw: { ...makeRow().raw, status_code: 499 },
        quota: unavailableQuota,
      })
    );

    expect(currentAuthFailure.overview.recentStatus.statusMessage).toBe('unauthorized');
    expect(supersededAuthFailure.overview.recentStatus.statusMessage).toBe('');
    expect(neutralCancellation.overview.recentStatus.statusMessage).toBe('');
  });

  it('treats a failed cached quota lookup without values or windows as missing capacity data', () => {
    const viewModel = buildAccountDetailViewModel(
      makeRow({
        quota: {
          status: 'error',
          remainingPercent: null,
          usedPercent: null,
          resetLabel: '-',
          resetAtMs: null,
          source: 'cache',
          error: 'quota refresh failed',
        },
      })
    );

    expect(viewModel.overview.capacity).toMatchObject({
      hasData: false,
      descriptionKey: 'accounts.detail_overview_capacity_missing_desc',
    });
  });

  it('only adds the attention block when an action is available', () => {
    const row = makeRow();
    const normalViewModel = buildAccountDetailViewModel(row);
    const attentionViewModel = buildAccountDetailViewModel(row, {
      recommendation: {
        row,
        action: 'refresh',
        priority: 'high',
        reasonKey: 'accounts.recommend_reason_low',
      },
    });

    expect(normalViewModel.overview.attention).toBeNull();
    expect(attentionViewModel.overview.attention).toEqual({
      priority: 'high',
      actionLabelKey: 'accounts.recommend_action_refresh',
      reasonKey: 'accounts.recommend_reason_low',
      targetTab: 'quota',
    });
  });
});
