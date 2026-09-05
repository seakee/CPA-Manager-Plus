import { describe, expect, it } from 'vitest';
import type { TFunction } from 'i18next';
import type { AuthFileItem, CodexQuotaState, CredentialScopedQuotaState } from '@/types';
import type { MonitoringAccountWindowUsageItem } from '@/services/api';
import { buildAccountRows, type AccountQuotaStores } from './accountRows';
import {
  buildAccountQuotaDisplayWindow,
  buildAccountQuotaDisplayWindows,
  getQuotaWindowShortLabel,
  isIntervalAccountQuotaWindow,
  isModelScopedAccountQuotaWindow,
  isStandardAccountQuotaListWindow,
  parseQuotaResetLabelMs,
  type TranslateQuotaWindowLabel,
} from './accountQuotaDisplayWindows';
import {
  buildQuotaCredentialIdentity,
  getQuotaCredentialStoreKey,
} from '@/utils/quota/credentialScope';
import { buildAccountDetailViewModel } from './accountDetailViewModel';
import { buildAccountQuotaWindowDefinitions } from './accountQuotaWindowDefinitions';
import { accountWindowUsageRequestKey } from './accountWindowUsageRows';

const emptyStores = (): AccountQuotaStores => ({
  antigravityQuota: {},
  claudeQuota: {},
  codexQuota: {},
  kimiQuota: {},
  xaiQuota: {},
});

const t = ((key: string, options?: Record<string, string | number>) => {
  const translations: Record<string, string> = {
    'antigravity_quota.group_gemini_models': 'Gemini models',
    'antigravity_quota.daily_limit': 'Daily limit',
    'claude_quota.extra_usage_label': 'Extra Usage',
    'kimi_quota.reset_hint': `resets in ${options?.hint ?? ''}`,
    'kimi_quota.weekly_limit': 'Weekly limit',
    'xai_quota.weekly_credits': 'Weekly credits',
    'xai_quota.monthly_credits': 'Monthly credits',
    'xai_quota.pay_as_you_go_label': 'Pay-as-you-go',
    'xai_quota.usage_amount': `${options?.remaining ?? '--'} / ${options?.limit ?? '--'} remaining`,
    'accounts.col_quota': 'Quota',
  };
  return translations[key] ?? key;
}) as TFunction;

const translateQuotaWindowLabel: TranslateQuotaWindowLabel = (label, labelKey, labelParams) =>
  labelKey ? t(labelKey, labelParams) : (label ?? 'Quota');

const buildRow = (file: AuthFileItem, stores: AccountQuotaStores = emptyStores()) => {
  const records = [
    stores.antigravityQuota,
    stores.claudeQuota,
    stores.codexQuota,
    stores.kimiQuota,
    stores.xaiQuota,
  ] as Array<Record<string, CredentialScopedQuotaState>>;
  records.forEach((record) => {
    const legacy = record[file.name];
    if (!legacy) return;
    const identity = buildQuotaCredentialIdentity(file);
    const storeKey = legacy.authFileKey || getQuotaCredentialStoreKey(file);
    record[storeKey] = { ...legacy, ...identity, authFileKey: storeKey };
  });
  return buildAccountRows([file], stores)[0];
};

describe('accountQuotaDisplayWindows', () => {
  describe('quota window classification', () => {
    it.each(['fixed', 'calendar', 'rolling'] as const)(
      'classifies an account-wide %s window as standard quota',
      (windowMode) => {
        const window = {
          windowMode,
          source: 'claude' as const,
          modelScope: { kind: 'all' as const, complete: true },
        };

        expect(isIntervalAccountQuotaWindow(window)).toBe(true);
        expect(isModelScopedAccountQuotaWindow(window)).toBe(false);
        expect(isStandardAccountQuotaListWindow(window)).toBe(true);
      }
    );

    it.each([
      { kind: 'models' as const, models: ['gemini-3-pro'], complete: true },
      { kind: 'family' as const, key: 'gemini', complete: true },
    ])('classifies an explicitly model-scoped fixed window as model quota', (modelScope) => {
      const window = { windowMode: 'fixed' as const, source: 'antigravity' as const, modelScope };

      expect(isIntervalAccountQuotaWindow(window)).toBe(true);
      expect(isModelScopedAccountQuotaWindow(window)).toBe(true);
      expect(isStandardAccountQuotaListWindow(window)).toBe(false);
    });

    it('keeps the complete Codex main family in standard quota', () => {
      const window = {
        windowMode: 'fixed' as const,
        source: 'codex' as const,
        modelScope: { kind: 'family' as const, key: 'codex_main', complete: true },
      };

      expect(isModelScopedAccountQuotaWindow(window)).toBe(false);
      expect(isStandardAccountQuotaListWindow(window)).toBe(true);
    });

    it.each([
      { kind: 'billing' as const, windowMode: 'non_window' as const },
      { kind: 'payg' as const, windowMode: 'non_window' as const },
      { kind: 'product' as const, windowMode: 'non_window' as const },
      { kind: 'unknown' as const, windowMode: 'unknown' as const },
    ])('does not classify a $kind $windowMode item as standard quota', ({ kind, windowMode }) => {
      const window = {
        kind,
        windowMode,
        source: 'xai' as const,
        modelScope: { kind: 'all' as const, complete: true },
      };

      expect(isIntervalAccountQuotaWindow(window)).toBe(false);
      expect(isStandardAccountQuotaListWindow(window)).toBe(false);
    });

    it('keeps a fixed billing interval out of the list while retaining interval semantics', () => {
      const window = {
        kind: 'billing' as const,
        windowMode: 'fixed' as const,
        source: 'xai' as const,
        modelScope: { kind: 'all' as const, complete: true },
      };

      expect(isIntervalAccountQuotaWindow(window)).toBe(true);
      expect(isStandardAccountQuotaListWindow(window)).toBe(false);
    });

    it('fails closed when a provider reports an incomplete account-wide scope', () => {
      const window = {
        windowMode: 'fixed' as const,
        source: 'antigravity' as const,
        modelScope: { kind: 'all' as const, complete: false },
      };

      expect(isModelScopedAccountQuotaWindow(window)).toBe(true);
      expect(isStandardAccountQuotaListWindow(window)).toBe(false);
    });
  });

  it('rolls legacy yearless reset labels into the next calendar year', () => {
    const nowMs = new Date(2026, 11, 31, 23, 0, 0, 0).getTime();

    expect(parseQuotaResetLabelMs('01/01 01:30', nowMs)).toBe(
      new Date(2027, 0, 1, 1, 30, 0, 0).getTime()
    );
    expect(parseQuotaResetLabelMs('02/31 10:00', nowMs)).toBeNull();
  });

  it('parses ambiguous legacy reset labels using the formatter locale order', () => {
    const nowMs = new Date(2026, 0, 1, 0, 0, 0, 0).getTime();

    expect(parseQuotaResetLabelMs('04/05, 10:30', nowMs, 'en-US')).toBe(
      new Date(2026, 3, 5, 10, 30, 0, 0).getTime()
    );
    expect(parseQuotaResetLabelMs('04/05, 10:30', nowMs, 'en-GB')).toBe(
      new Date(2026, 4, 4, 10, 30, 0, 0).getTime()
    );
    expect(parseQuotaResetLabelMs('04.05., 10:30', nowMs, 'ru-RU')).toBe(
      new Date(2026, 4, 4, 10, 30, 0, 0).getTime()
    );
  });

  it('rejects numeric reset labels outside the JavaScript date range', () => {
    expect(parseQuotaResetLabelMs(String(Number.MAX_VALUE))).toBeNull();
  });

  it('rejects an invalid normalized reset timestamp and falls back to a parseable label', () => {
    const nowMs = new Date(2026, 11, 31, 23, 0, 0, 0).getTime();
    const window = buildAccountQuotaDisplayWindow({
      key: 'legacy',
      label: 'Legacy window',
      remainingPercent: 50,
      usedPercent: 50,
      resetLabel: '01/01 01:30',
      resetAtMs: Number.MAX_VALUE,
      resetAccuracy: 'exact',
      nowMs,
    });

    expect(window.resetAtMs).toBe(new Date(2027, 0, 1, 1, 30, 0, 0).getTime());
    expect(window.resetAccuracy).toBe('unknown');
  });

  it.each(['claude', 'antigravity', 'kimi', 'xai'] as const)(
    'keeps observedAt compatibility for %s when progress provenance is absent',
    (source) => {
      const window = buildAccountQuotaDisplayWindow({
        key: `${source}-window`,
        label: 'Quota',
        remainingPercent: 60,
        usedPercent: 40,
        resetLabel: '-',
        source,
        observedAtMs: 2_000,
      });

      expect(window.quotaProgressObservedAtMs).toBe(2_000);
    }
  );

  it('keeps Codex explicit unknown progress provenance from falling back to observedAt', () => {
    const window = buildAccountQuotaDisplayWindow({
      key: 'five-hour',
      label: '5H',
      remainingPercent: 40,
      usedPercent: 60,
      resetLabel: '-',
      source: 'codex',
      observedAtMs: 2_000,
      quotaProgressObservedAtMs: null,
    });

    expect(window.quotaProgressObservedAtMs).toBeNull();
  });

  it('falls back to observedAt for legacy Codex windows without explicit provenance', () => {
    const window = buildAccountQuotaDisplayWindow({
      key: 'five-hour',
      label: '5H',
      remainingPercent: 40,
      usedPercent: 60,
      resetLabel: '-',
      source: 'codex',
      observedAtMs: 2_000,
    });

    expect(window.quotaProgressObservedAtMs).toBe(2_000);
  });

  it('uses auth-index scoped Codex quota and preserves request window ranges', () => {
    const resetAtMs = Date.parse('2026-07-09T14:00:00Z');
    const quota: CodexQuotaState = {
      status: 'success',
      windows: [
        {
          id: 'primary',
          label: 'Primary',
          usedPercent: 75,
          resetLabel: '2026-07-09T14:00:00Z',
          resetAtMs,
          resetAccuracy: 'exact',
          limitWindowSeconds: 18_000,
        },
      ],
    };
    const row = buildRow({ name: 'shared.codex.json', type: 'codex', authIndex: '1' });

    const windows = buildAccountQuotaDisplayWindows(row, {
      stores: emptyStores(),
      getDisplayCodexQuota: () => quota,
      translateQuotaWindowLabel,
      t,
      nowMs: Date.parse('2026-07-09T12:00:00Z'),
    });

    expect(windows).toHaveLength(1);
    expect(windows[0]).toMatchObject({
      key: 'primary',
      kind: 'five_hour',
      remainingPercent: 25,
      usedPercent: 75,
      resetAtMs,
      resetAccuracy: 'exact',
      limitWindowSeconds: 18_000,
      source: 'codex',
    });
    expect(windows[0].fromMs).toBe(Date.parse('2026-07-09T09:00:00Z'));
    expect(windows[0].toMs).toBe(Date.parse('2026-07-09T12:00:00Z'));
    expect(getQuotaWindowShortLabel(windows[0])).toBe('5H');
  });

  it('maps Claude quota windows through translated labels', () => {
    const stores = {
      ...emptyStores(),
      claudeQuota: {
        'claude.json': {
          status: 'success',
          windows: [
            {
              id: 'seven_day',
              label: 'Weekly',
              labelKey: 'kimi_quota.weekly_limit',
              usedPercent: 40,
              resetLabel: '07/10, 12:00',
              resetAtMs: Date.parse('2026-07-10T12:00:00Z'),
              resetAccuracy: 'exact',
              limitWindowSeconds: 7 * 24 * 60 * 60,
              modelScope: { kind: 'all', complete: true },
            },
          ],
          extraUsage: {
            is_enabled: true,
            used_credits: 150,
            monthly_limit: 500,
            utilization: null,
          },
        },
      },
    } satisfies AccountQuotaStores;
    const row = buildRow({ name: 'claude.json', type: 'claude' }, stores);

    const windows = buildAccountQuotaDisplayWindows(row, {
      stores,
      translateQuotaWindowLabel,
      t,
    });

    expect(windows).toHaveLength(2);
    expect(windows[0]).toMatchObject({
      key: 'seven_day',
      label: 'Weekly limit',
      kind: 'weekly',
      remainingPercent: 60,
      resetAtMs: Date.parse('2026-07-10T12:00:00Z'),
      resetAccuracy: 'exact',
      limitWindowSeconds: 7 * 24 * 60 * 60,
      modelScope: { kind: 'all', complete: true },
      source: 'claude',
    });
    expect(windows[1]).toMatchObject({
      key: 'extra-usage',
      label: 'Extra Usage',
      kind: 'monthly',
      remainingPercent: 70,
      usedPercent: 30,
      amountLabel: '$1.50 / $5.00',
      source: 'claude',
    });
  });

  it('flattens Antigravity groups while retaining group and bucket metadata', () => {
    const stores = {
      ...emptyStores(),
      antigravityQuota: {
        'ag.json': {
          status: 'success',
          groups: [
            {
              id: 'gemini',
              label: 'Gemini models',
              models: ['gemini-3-pro'],
              description: 'models within this group: gemini-3-pro',
              buckets: [
                {
                  id: 'daily',
                  label: 'Daily limit',
                  window: 'daily',
                  remainingFraction: 0.42,
                  resetTime: '2026-07-10T00:00:00Z',
                  description: 'Daily model quota',
                },
                {
                  id: 'month-end-five-hour',
                  label: '5 Hour Limit',
                  window: '5h',
                  remainingFraction: 0.52,
                  resetTime: '2026-07-09T10:00:00Z',
                },
              ],
            },
            {
              id: 'claude-gpt',
              label: 'Claude and GPT models',
              buckets: [
                {
                  id: 'weekly',
                  label: 'Weekly limit',
                  window: 'weekly',
                  remainingFraction: 0.65,
                  resetTime: '2026-07-15T00:00:00Z',
                },
              ],
            },
          ],
        },
      },
    } satisfies AccountQuotaStores;
    const row = buildRow({ name: 'ag.json', type: 'antigravity' }, stores);

    const windows = buildAccountQuotaDisplayWindows(row, {
      stores,
      translateQuotaWindowLabel,
      t,
    });

    expect(windows).toHaveLength(3);
    expect(windows[0]).toMatchObject({
      key: 'gemini:daily',
      label: 'Daily limit',
      kind: 'daily',
      remainingPercent: 42,
      usedPercent: 58,
      groupLabel: 'Gemini models',
      description: 'Daily model quota',
      resetAtMs: Date.parse('2026-07-10T00:00:00Z'),
      resetAccuracy: 'exact',
      limitWindowSeconds: 24 * 60 * 60,
      modelScope: { kind: 'models', models: ['gemini-3-pro'], complete: true },
      source: 'antigravity',
    });
    expect(getQuotaWindowShortLabel(windows[0])).toBe('24H');
    expect(windows[1]).toMatchObject({
      key: 'gemini:month-end-five-hour',
      kind: 'five_hour',
      remainingPercent: 52,
      usedPercent: 48,
      limitWindowSeconds: 5 * 60 * 60,
      modelScope: { kind: 'models', models: ['gemini-3-pro'], complete: true },
    });
    expect(getQuotaWindowShortLabel(windows[1])).toBe('5H');
    expect(windows[2]).toMatchObject({
      key: 'claude-gpt:weekly',
      kind: 'weekly',
      limitWindowSeconds: 7 * 24 * 60 * 60,
      modelScope: { kind: 'family', key: 'claude_gpt', complete: true },
    });
  });

  it('carries non-Codex fetchedAt through the production forecast chain', () => {
    const fetchedAtMs = Date.parse('2026-07-01T00:00:00Z');
    const resetAtMs = fetchedAtMs + 5 * 60 * 60 * 1000;
    const nowMs = fetchedAtMs + 60 * 60 * 1000;
    const stores = {
      ...emptyStores(),
      antigravityQuota: {
        'ag.json': {
          status: 'success',
          fetchedAtMs,
          groups: [
            {
              id: 'gemini',
              label: 'Gemini models',
              models: ['gemini-3-pro'],
              buckets: [
                {
                  id: 'five-hour',
                  label: '5 Hour Limit',
                  window: '5h',
                  remainingFraction: 0.5,
                  resetTime: new Date(resetAtMs).toISOString(),
                },
              ],
            },
          ],
        },
      },
    } satisfies AccountQuotaStores;
    const row = buildRow({ name: 'ag.json', type: 'antigravity' }, stores);
    const displayWindows = buildAccountQuotaDisplayWindows(row, {
      stores,
      translateQuotaWindowLabel,
      t,
      nowMs,
    });
    const [definition] = buildAccountQuotaWindowDefinitions(displayWindows, nowMs);
    const currentUsage: MonitoringAccountWindowUsageItem = {
      row_key: row.selectionKey,
      window_key: definition.providerWindowId,
      from_ms: definition.cycleStartMs ?? fetchedAtMs,
      to_ms: nowMs,
      matched: true,
      total_requests: 100,
      success_calls: 100,
      failure_calls: 0,
      total_tokens: 1_000_000,
      total_cost: 5,
      success_rate: 1,
      last_seen_ms: fetchedAtMs - 100,
      sync_status: 'ready',
      scope_match_status: 'complete',
    };
    const detail = buildAccountDetailViewModel(row, {
      quotaWindows: [{ ...definition, resetLabel: definition.display.resetLabel }],
      windowUsageByKey: new Map([
        [
          accountWindowUsageRequestKey(
            row.selectionKey,
            definition.providerWindowId,
            'current',
            definition.modelScope
          ),
          currentUsage,
        ],
      ]),
    });

    expect(displayWindows[0]).toMatchObject({
      usedPercent: 50,
      observedAtMs: fetchedAtMs,
      quotaProgressObservedAtMs: fetchedAtMs,
    });
    expect(definition).toMatchObject({
      observedAtMs: fetchedAtMs,
      quotaProgressObservedAtMs: fetchedAtMs,
    });
    expect(detail.quota.windows[0].forecast).toMatchObject({ basis: 'quota' });
  });

  it('adds Kimi usage amounts and formatted reset hints', () => {
    const stores = {
      ...emptyStores(),
      kimiQuota: {
        'kimi.json': {
          status: 'success',
          rows: [
            {
              id: 'weekly',
              labelKey: 'kimi_quota.weekly_limit',
              used: 3,
              limit: 10,
              resetHint: '2d',
              resetAtMs: Date.parse('2026-07-31T10:00:00Z'),
              resetAccuracy: 'estimated',
              scope: 'FEATURE_CODING',
              limitWindowSeconds: 7 * 24 * 60 * 60,
            },
          ],
        },
      },
    } satisfies AccountQuotaStores;
    const row = buildRow({ name: 'kimi.json', type: 'kimi' }, stores);

    const windows = buildAccountQuotaDisplayWindows(row, {
      stores,
      translateQuotaWindowLabel,
      t,
    });

    expect(windows[0]).toMatchObject({
      key: 'weekly',
      label: 'Weekly limit',
      kind: 'weekly',
      remainingPercent: 70,
      usedPercent: 30,
      resetLabel: 'resets in 2d',
      resetAtMs: Date.parse('2026-07-31T10:00:00Z'),
      resetAccuracy: 'estimated',
      limitWindowSeconds: 7 * 24 * 60 * 60,
      modelScope: { kind: 'all', complete: true },
      amountLabel: '3 / 10',
      source: 'kimi',
    });
  });

  it('splits xAI billing into monthly and pay-as-you-go windows', () => {
    const stores = {
      ...emptyStores(),
      xaiQuota: {
        'xai.json': {
          status: 'success',
          billing: {
            periodType: 'monthly',
            usagePercent: null,
            productUsage: [],
            monthlyLimitCents: 10_000,
            usedCents: 12_500,
            includedUsedCents: 10_000,
            onDemandCapCents: 5_000,
            onDemandUsedCents: 2_500,
            onDemandUsedPercent: 50,
            billingPeriodEnd: '2026-07-31T00:00:00Z',
            usedPercent: 100,
          },
        },
      },
    } satisfies AccountQuotaStores;
    const row = buildRow({ name: 'xai.json', type: 'xai' }, stores);

    const windows = buildAccountQuotaDisplayWindows(row, {
      stores,
      translateQuotaWindowLabel,
      t,
    });

    expect(windows).toHaveLength(2);
    expect(windows[0]).toMatchObject({
      key: 'billing',
      label: 'Monthly credits',
      kind: 'billing',
      remainingPercent: 0,
      amountLabel: '$0.00 / $100.00 remaining',
      source: 'xai',
    });
    expect(windows[1]).toMatchObject({
      key: 'pay-as-you-go',
      label: 'Pay-as-you-go',
      kind: 'payg',
      remainingPercent: 50,
      amountLabel: '$25.00 / $50.00 remaining',
      source: 'xai',
    });
    expect(getQuotaWindowShortLabel(windows[1])).toBe('PAYG');
  });

  it('keeps a monthly xAI billing period as an interval while hiding it from the list', () => {
    const periodStartMs = Date.parse('2026-08-01T00:00:00Z');
    const periodEndMs = Date.parse('2026-09-01T00:00:00Z');
    const stores = {
      ...emptyStores(),
      xaiQuota: {
        'xai.json': {
          status: 'success',
          billing: {
            periodType: 'monthly',
            usagePercent: 20,
            periodStart: '2026-08-01T00:00:00Z',
            periodEnd: '2026-09-01T00:00:00Z',
            productUsage: [{ product: 'Grok Code Fast', usagePercent: 20 }],
            monthlyLimitCents: 10_000,
            usedCents: 2_000,
            includedUsedCents: 2_000,
            onDemandCapCents: null,
            onDemandUsedCents: null,
            onDemandUsedPercent: null,
            billingPeriodStart: '2026-08-01T00:00:00Z',
            billingPeriodEnd: '2026-09-01T00:00:00Z',
            usedPercent: 20,
          },
        },
      },
    } satisfies AccountQuotaStores;
    const row = buildRow({ name: 'xai.json', type: 'xai' }, stores);

    const windows = buildAccountQuotaDisplayWindows(row, {
      stores,
      translateQuotaWindowLabel,
      t,
      nowMs: Date.parse('2026-08-15T00:00:00Z'),
    });
    const periodWindow = windows.find((window) => window.key === 'credits-period');

    expect(periodWindow).toMatchObject({
      kind: 'billing',
      windowMode: 'fixed',
      cycleStartMs: periodStartMs,
      cycleEndMs: periodEndMs,
      modelScope: { kind: 'all', complete: true },
    });
    expect(periodWindow).toBeDefined();
    expect(isIntervalAccountQuotaWindow(periodWindow!)).toBe(true);
    expect(isModelScopedAccountQuotaWindow(periodWindow!)).toBe(false);
    expect(isStandardAccountQuotaListWindow(periodWindow!)).toBe(false);
  });

  it('shows xAI weekly credits as a separate quota window', () => {
    const billingPeriodEndMs = Date.parse('2026-07-08T00:00:00Z');
    const stores = {
      ...emptyStores(),
      xaiQuota: {
        'xai.json': {
          status: 'success',
          billing: {
            periodType: 'weekly',
            usagePercent: 42,
            periodStart: '2026-07-01T00:00:00Z',
            billingPeriodEnd: String(billingPeriodEndMs / 1000),
            productUsage: [{ product: 'Grok Code Fast', usagePercent: 37 }],
            monthlyLimitCents: 10_000,
            usedCents: 4_000,
            includedUsedCents: 4_000,
            onDemandCapCents: null,
            onDemandUsedCents: null,
            onDemandUsedPercent: null,
            usedPercent: 40,
          },
        },
      },
    } satisfies AccountQuotaStores;
    const row = buildRow({ name: 'xai.json', type: 'xai' }, stores);

    const windows = buildAccountQuotaDisplayWindows(row, {
      stores,
      translateQuotaWindowLabel,
      t,
    });

    expect(windows).toHaveLength(3);
    expect(windows[0]).toMatchObject({
      key: 'credits-period',
      label: 'Weekly credits',
      kind: 'weekly',
      remainingPercent: 58,
      usedPercent: 42,
      resetAtMs: billingPeriodEndMs,
      resetAccuracy: 'exact',
      limitWindowSeconds: 7 * 24 * 60 * 60,
      cycleStartMs: Date.parse('2026-07-01T00:00:00Z'),
      cycleEndMs: billingPeriodEndMs,
      windowMode: 'fixed',
      source: 'xai',
    });
    expect(windows[1]).toMatchObject({
      key: 'billing',
      label: 'Monthly credits',
      remainingPercent: 60,
      resetAtMs: billingPeriodEndMs,
      resetAccuracy: 'exact',
      source: 'xai',
    });
    expect(windows[2]).toMatchObject({
      key: 'product-0-grok-code-fast',
      label: 'Grok Code Fast',
      kind: 'product',
      remainingPercent: 63,
      usedPercent: 37,
      resetAtMs: billingPeriodEndMs,
      resetAccuracy: 'exact',
      source: 'xai',
    });
  });

  it('does not create a monthly window from an on-demand billing reset alone', () => {
    const stores = {
      ...emptyStores(),
      xaiQuota: {
        'xai.json': {
          status: 'success',
          billing: {
            periodType: 'weekly',
            usagePercent: 0,
            periodStart: '2026-08-13T00:00:00Z',
            periodEnd: '2026-08-20T00:00:00Z',
            productUsage: [],
            monthlyLimitCents: null,
            usedCents: null,
            includedUsedCents: null,
            onDemandCapCents: 5_000,
            onDemandUsedCents: 0,
            onDemandUsedPercent: 0,
            billingPeriodEnd: '2026-09-01T00:00:00Z',
            usedPercent: null,
          },
        },
      },
    } satisfies AccountQuotaStores;
    const row = buildRow({ name: 'xai.json', type: 'xai' }, stores);

    const windows = buildAccountQuotaDisplayWindows(row, {
      stores,
      translateQuotaWindowLabel,
      t,
    });

    expect(windows.map((window) => window.key)).toEqual(['credits-period', 'pay-as-you-go']);
  });

  it('does not create a monthly window from weekly protobuf zero placeholders', () => {
    const stores = {
      ...emptyStores(),
      xaiQuota: {
        'xai.json': {
          status: 'success',
          billing: {
            periodType: 'weekly',
            usagePercent: 0,
            periodStart: '2026-08-13T00:00:00Z',
            periodEnd: '2026-08-20T00:00:00Z',
            productUsage: [],
            monthlyLimitCents: null,
            usedCents: 0,
            includedUsedCents: 0,
            onDemandCapCents: 0,
            onDemandUsedCents: 0,
            onDemandUsedPercent: null,
            billingPeriodEnd: '2026-09-01T00:00:00Z',
            usedPercent: null,
          },
        },
      },
    } satisfies AccountQuotaStores;
    const row = buildRow({ name: 'xai.json', type: 'xai' }, stores);

    const windows = buildAccountQuotaDisplayWindows(row, {
      stores,
      translateQuotaWindowLabel,
      t,
    });

    expect(windows.map((window) => window.key)).toEqual(['credits-period']);
  });

  it('does not create a monthly window when usage exists without limit evidence', () => {
    const stores = {
      ...emptyStores(),
      xaiQuota: {
        'xai.json': {
          status: 'success',
          billing: {
            periodType: 'monthly',
            usagePercent: null,
            productUsage: [],
            monthlyLimitCents: null,
            usedCents: 500,
            includedUsedCents: 500,
            onDemandCapCents: null,
            onDemandUsedCents: null,
            onDemandUsedPercent: null,
            billingPeriodEnd: '2026-09-01T00:00:00Z',
            usedPercent: null,
          },
        },
      },
    } satisfies AccountQuotaStores;
    const row = buildRow({ name: 'xai.json', type: 'xai' }, stores);

    expect(
      buildAccountQuotaDisplayWindows(row, {
        stores,
        translateQuotaWindowLabel,
        t,
      })
    ).toEqual([]);
  });

  it('does not render billing windows for official API identity health', () => {
    const stores = {
      ...emptyStores(),
      xaiQuota: {
        'paid-xai.json': {
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
        },
      },
    } satisfies AccountQuotaStores;
    const row = buildRow({ name: 'paid-xai.json', type: 'xai' }, stores);

    expect(
      buildAccountQuotaDisplayWindows(row, {
        stores,
        translateQuotaWindowLabel,
        t,
      })
    ).toEqual([]);
  });
});
